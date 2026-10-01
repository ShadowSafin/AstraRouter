package routing

import (
	"sync"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// healthWindow is the number of recent observations retained per provider. A
// fixed-size ring is used rather than a time-bucketed histogram because the
// engine only needs a rolling success ratio and a latency percentile, and a ring
// gives both with constant memory and no background aggregation.
const healthWindow = 64

// HealthTracker maintains rolling per-provider health from live traffic and from
// active probes.
//
// Two signals feed it:
//
//   - passive observation of real requests, which is free and reflects the
//     traffic the gateway actually sends;
//   - active probes, which detect a provider that has gone dark when no traffic
//     is flowing.
//
// Passive data wins when it exists, because a synthetic probe hitting an empty
// endpoint can succeed while real completions fail (a quota exhausted on one
// model, for instance). This is why consecutive-failure counts are tracked
// separately from probe results.
type HealthTracker struct {
	mu sync.RWMutex
	// states holds the derived assessment per provider.
	states map[string]*providerState
	// degradeThreshold is the consecutive failure count that marks a provider
	// degraded. It is configurable so an operator can tune sensitivity, which
	// matters a lot on flaky local runtimes.
	degradeThreshold int
	// unhealthyThreshold is the count that removes a provider from rotation.
	unhealthyThreshold int
	// window is the retention duration for observations.
	window time.Duration
}

// providerState is the mutable per-provider health record.
type providerState struct {
	providerID   string
	providerName string

	consecutiveFailures int
	consecutiveSuccess  int

	// observations is a ring of recent outcomes.
	observations []observation
	next         int
	filled       bool

	// lastHealth is the most recent rendered assessment.
	lastHealth domain.ProviderHealth

	// activeProbe holds the last probe result separately, so a probe failure is
	// visible in the dashboard without immediately failing real traffic.
	activeProbe *domain.ProviderHealth

	// circuitOpenUntil implements a time-boxed circuit breaker: once a provider
	// trips, it stays out of rotation for a cooldown even if failures continue,
	// which prevents a permanently failing provider from being retried forever.
	circuitOpenUntil time.Time
}

// observation is one recorded request outcome.
type observation struct {
	at        time.Time
	success   bool
	latency   time.Duration
	errorCode domain.ErrorCode
}

// HealthConfig tunes the tracker.
type HealthConfig struct {
	// DegradeThreshold is the consecutive failure count that marks a provider
	// degraded. Zero selects domain.DegradeThreshold.
	DegradeThreshold int
	// UnhealthyThreshold is the count that removes a provider from rotation.
	// Zero selects three times the degrade threshold.
	UnhealthyThreshold int
	// Window is the retention duration for observations. Zero selects five
	// minutes.
	Window time.Duration
	// CircuitCooldown is how long a tripped provider stays out of rotation.
	// Zero selects thirty seconds.
	CircuitCooldown time.Duration
}

// defaultCircuitCooldown is deliberately short. A long cooldown on a provider
// that recovered means traffic keeps going to a more expensive fallback for no
// reason, which is a worse outcome than one failed probe request.
const defaultCircuitCooldown = 30 * time.Second

// NewHealthTracker constructs a tracker.
func NewHealthTracker(cfg HealthConfig) *HealthTracker {
	degrade := cfg.DegradeThreshold
	if degrade <= 0 {
		degrade = domain.DegradeThreshold
	}
	unhealthy := cfg.UnhealthyThreshold
	if unhealthy <= 0 {
		unhealthy = degrade * 3
	}
	window := cfg.Window
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &HealthTracker{
		states:             map[string]*providerState{},
		degradeThreshold:   degrade,
		unhealthyThreshold: unhealthy,
		window:             window,
	}
}

// state returns the mutable state for a provider, creating it if needed. The
// caller must hold the write lock.
func (h *HealthTracker) stateLocked(providerID, providerName string) *providerState {
	s, ok := h.states[providerID]
	if !ok {
		s = &providerState{
			providerID:   providerID,
			providerName: providerName,
			observations: make([]observation, healthWindow),
			lastHealth: domain.ProviderHealth{
				ProviderID:   providerID,
				ProviderName: providerName,
				State:        domain.HealthUnknown,
				CheckedAt:    domain.Now(),
				Source:       "initial",
			},
		}
		h.states[providerID] = s
	}
	if providerName != "" {
		s.providerName = providerName
	}
	return s
}

// RecordSuccess records a successful provider call.
func (h *HealthTracker) RecordSuccess(providerID, providerName string, latency time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := h.stateLocked(providerID, providerName)
	s.consecutiveFailures = 0
	s.consecutiveSuccess++
	// A success clears the circuit breaker immediately: the cheapest way to learn
	// that a provider recovered is to let real traffic through again.
	s.circuitOpenUntil = time.Time{}
	s.push(observation{at: time.Now(), success: true, latency: latency})
	h.recomputeLocked(s)
}

// RecordFailure records a failed provider call.
func (h *HealthTracker) RecordFailure(providerID, providerName string, code domain.ErrorCode, latency time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := h.stateLocked(providerID, providerName)
	s.consecutiveFailures++
	s.consecutiveSuccess = 0
	s.push(observation{at: time.Now(), success: false, latency: latency, errorCode: code})
	h.recomputeLocked(s)

	if s.consecutiveFailures >= h.unhealthyThreshold {
		s.circuitOpenUntil = time.Now().Add(defaultCircuitCooldown)
	}
}

// RecordProbe records an active probe result.
func (h *HealthTracker) RecordProbe(health domain.ProviderHealth) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := h.stateLocked(health.ProviderID, health.ProviderName)
	probe := health
	s.activeProbe = &probe
	s.lastHealth.ProbeLatencyMS = health.ProbeLatencyMS
	s.lastHealth.LatencyMS = health.LatencyMS
	s.lastHealth.CheckedAt = health.CheckedAt
	s.lastHealth.Message = health.Message
	s.lastHealth.Source = "active_probe"

	// A passing probe does not clear a circuit that real traffic opened; only a
	// real success does. This is intentional: probes and real completions test
	// different things, and trusting a probe over observed traffic would let a
	// partially-broken provider back into rotation.
	if health.State == domain.HealthHealthy && s.consecutiveFailures == 0 {
		s.lastHealth.State = domain.HealthHealthy
		s.lastHealth.SuccessRate = 1
		s.lastHealth.ErrorRate = 0
	}
}

// push appends an observation to the ring.
func (s *providerState) push(o observation) {
	s.observations[s.next] = o
	s.next = (s.next + 1) % len(s.observations)
	if s.next == 0 {
		s.filled = true
	}
}

// recent returns the observations in the ring, oldest first.
func (s *providerState) recent() []observation {
	cutoff := time.Now().Add(-5 * time.Minute)
	out := make([]observation, 0, len(s.observations))
	count := s.next
	if s.filled {
		count = len(s.observations)
	}
	for i := 0; i < count; i++ {
		idx := s.next - count + i
		if idx < 0 {
			idx += len(s.observations)
		}
		o := s.observations[idx]
		if o.at.IsZero() || o.at.Before(cutoff) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// recomputeLocked refreshes the derived health assessment.
func (h *HealthTracker) recomputeLocked(s *providerState) {
	obs := s.recent()

	total := len(obs)
	successes := 0
	var latencySum time.Duration
	for _, o := range obs {
		if o.success {
			successes++
		}
		latencySum += o.latency
	}

	var successRate, errorRate float64
	var avgLatency time.Duration
	if total > 0 {
		successRate = float64(successes) / float64(total)
		errorRate = 1 - successRate
		avgLatency = latencySum / time.Duration(total)
	}

	state := domain.EvaluateState(s.consecutiveFailures, errorRate, successRate)
	// The consecutive-failure thresholds are applied on top of the statistical
	// verdict because a brand-new provider with three failures has a success rate
	// near zero but too little data for the ratio to be meaningful.
	switch {
	case s.consecutiveFailures >= h.unhealthyThreshold:
		state = domain.HealthUnhealthy
	case s.consecutiveFailures >= h.degradeThreshold:
		if state == domain.HealthHealthy {
			state = domain.HealthDegraded
		}
	}

	message := s.lastHealth.Message
	if s.consecutiveFailures > 0 {
		last := obs[len(obs)-1]
		if last.errorCode != "" {
			message = "last error: " + string(last.errorCode)
		}
	}

	s.lastHealth = domain.ProviderHealth{
		ProviderID:          s.providerID,
		ProviderName:        s.providerName,
		State:               state,
		CheckedAt:           time.Now().UTC(),
		LatencyMS:           avgLatency.Milliseconds(),
		SuccessRate:         successRate,
		ErrorRate:           errorRate,
		ConsecutiveFailures: s.consecutiveFailures,
		Message:             message,
		Source:              "passive_observation",
	}
	if s.activeProbe != nil {
		s.lastHealth.ProbeLatencyMS = s.activeProbe.ProbeLatencyMS
	}
}

// Health returns the current assessment for a provider.
//
// A provider with no observations reports HealthUnknown, which the engine treats
// as eligible: refusing to route to a provider that has simply not been used yet
// would make a cold start impossible.
func (h *HealthTracker) Health(providerID string) (domain.ProviderHealth, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	s, ok := h.states[providerID]
	if !ok {
		return domain.ProviderHealth{}, false
	}
	health := s.lastHealth
	// A tripped circuit breaker is reported as unhealthy regardless of the
	// statistical verdict, because it is the state the engine will act on.
	if !s.circuitOpenUntil.IsZero() && time.Now().Before(s.circuitOpenUntil) {
		health.State = domain.HealthUnhealthy
		health.Message = "circuit breaker open"
	}
	return health, true
}

// CircuitOpen reports whether a provider is temporarily out of rotation.
func (h *HealthTracker) CircuitOpen(providerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	s, ok := h.states[providerID]
	if !ok {
		return false
	}
	return !s.circuitOpenUntil.IsZero() && time.Now().Before(s.circuitOpenUntil)
}

// Snapshot returns every provider's current health, in a stable order.
func (h *HealthTracker) Snapshot() []domain.ProviderHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]domain.ProviderHealth, 0, len(h.states))
	for id := range h.states {
		health := h.states[id].lastHealth
		if s := h.states[id]; !s.circuitOpenUntil.IsZero() && time.Now().Before(s.circuitOpenUntil) {
			health.State = domain.HealthUnhealthy
			health.Message = "circuit breaker open"
		}
		out = append(out, health)
	}
	// Stable ordering keeps the dashboard table from jumping between refreshes.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ProviderName < out[i].ProviderName {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// LatencyEstimates returns the observed average latency per provider, used by the
// lowest-latency strategy.
func (h *HealthTracker) LatencyEstimates() map[string]time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make(map[string]time.Duration, len(h.states))
	for id, s := range h.states {
		obs := s.recent()
		if len(obs) == 0 {
			continue
		}
		var sum time.Duration
		for _, o := range obs {
			sum += o.latency
		}
		out[id] = sum / time.Duration(len(obs))
	}
	return out
}
