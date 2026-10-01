package domain

import "time"

// RoutingStrategy selects how a policy's candidate set is ordered.
//
// Phase 1 is deliberately rule-based and deterministic. Every strategy is a
// pure function of the candidate list plus observable signals (health, price,
// observed latency), which makes routing unit-testable without network calls
// and reproducible when replaying historical requests.
type RoutingStrategy string

const (
	// StrategyPriority walks the declared target order and takes the first
	// eligible candidate. It is the default because it is the most predictable
	// strategy to operate.
	StrategyPriority RoutingStrategy = "priority"
	// StrategyWeighted picks pseudo-randomly among eligible candidates using
	// declared weights. Selection is deterministic for a given request id, so
	// retries and replay observe the same choice.
	StrategyWeighted RoutingStrategy = "weighted"
	// StrategyLowestCost sorts by estimated request cost.
	StrategyLowestCost RoutingStrategy = "lowest_cost"
	// StrategyLowestLatency sorts by observed p50 latency, falling back to a
	// static per-provider estimate when no observation exists.
	StrategyLowestLatency RoutingStrategy = "lowest_latency"
	// StrategyHighestQuality sorts by the model's operator-assigned quality
	// tier, then by cost. It exists because "route to the cheapest model that
	// can do the job" is only safe once the tier reflects measured quality.
	StrategyHighestQuality RoutingStrategy = "highest_quality"
)

// Valid reports whether the strategy is implemented.
func (s RoutingStrategy) Valid() bool {
	switch s {
	case StrategyPriority, StrategyWeighted, StrategyLowestCost, StrategyLowestLatency, StrategyHighestQuality:
		return true
	default:
		return false
	}
}

// PolicyMatch decides whether a policy applies to a request. All populated
// fields must match; empty fields are wildcards.
//
// Matching is non-overlapping by construction: the resolver sorts matching
// policies by specificity, then priority, so the most specific rule always wins
// regardless of insertion order.
type PolicyMatch struct {
	// Models are client-requested model names or aliases to match. Entries may
	// be exact ("gpt-4o") or glob ("gpt-4*").
	Models []string `json:"models,omitempty"`
	// RequestTypes restricts to specific API surfaces.
	RequestTypes []RequestType `json:"request_types,omitempty"`
	// TenantIDs restricts to tenants. Mutually exclusive with APIKeyIDs.
	TenantIDs []string `json:"tenant_ids,omitempty"`
	// APIKeyIDs restricts to specific keys, enabling per-application policies.
	APIKeyIDs []string `json:"api_key_ids,omitempty"`
	// EndpointIDs restricts to admin-managed endpoint scopes (Phase 2).
	EndpointIDs []string `json:"endpoint_ids,omitempty"`
	// MinPromptTokens and MaxPromptTokens bound the estimated prompt size.
	MinPromptTokens int `json:"min_prompt_tokens,omitempty"`
	MaxPromptTokens int `json:"max_prompt_tokens,omitempty"`
	// RequiredCapabilities must all be present on the request.
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
	// Streaming, when set, requires the request to (not) be streaming.
	Streaming *bool `json:"streaming,omitempty"`
	// Phase 2 extensions: task classes, batch/interactive, regions, sensitivity.
	TaskTypes []TaskType `json:"task_types,omitempty"`
	// Batch, when set, requires batch (true) or interactive (false) traffic.
	Batch *bool `json:"batch,omitempty"`
	// Regions constrains provider region; empty means any.
	Regions []string `json:"regions,omitempty"`
	// DataSensitivity lists sensitivity labels this rule governs (e.g. "pii",
	// "phi", "public"). Empty means any.
	DataSensitivity []string `json:"data_sensitivity,omitempty"`
}

// IsWildcard reports whether the match applies to every request.
func (m PolicyMatch) IsWildcard() bool {
	return len(m.Models) == 0 && len(m.RequestTypes) == 0 && len(m.TenantIDs) == 0 &&
		len(m.APIKeyIDs) == 0 && len(m.EndpointIDs) == 0 && m.MinPromptTokens == 0 && m.MaxPromptTokens == 0 &&
		len(m.RequiredCapabilities) == 0 && m.Streaming == nil &&
		len(m.TaskTypes) == 0 && m.Batch == nil && len(m.Regions) == 0 && len(m.DataSensitivity) == 0
}

// Specificity scores how narrowly a match is scoped. Higher is more specific.
// The weights are intentionally coarse: they only need to produce a total order
// between obviously-different rules, not a meaningful cardinal scale.
func (m PolicyMatch) Specificity() int {
	score := 0
	if len(m.APIKeyIDs) > 0 {
		score += 1000
	}
	if len(m.EndpointIDs) > 0 {
		score += 750
	}
	if len(m.TenantIDs) > 0 {
		score += 500
	}
	if len(m.Models) > 0 {
		score += 200
	}
	if len(m.RequiredCapabilities) > 0 {
		score += 100
	}
	if len(m.TaskTypes) > 0 {
		score += 80
	}
	if len(m.DataSensitivity) > 0 {
		score += 70
	}
	if len(m.Regions) > 0 {
		score += 60
	}
	if m.Streaming != nil {
		score += 50
	}
	if m.Batch != nil {
		score += 40
	}
	if len(m.RequestTypes) > 0 {
		score += 25
	}
	if m.MinPromptTokens > 0 || m.MaxPromptTokens > 0 {
		score += 10
	}
	return score
}

// RouteTarget is one concrete provider/model pair a policy may route to.
type RouteTarget struct {
	// ProviderID is the preferred key. ProviderName is accepted as a fallback
	// so policies can be written readably as YAML/JSON.
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	// Model is the upstream model name, or an alias resolved at load time.
	Model string `json:"model"`
	// Alias records the client-facing name this target was selected for.
	Alias string `json:"alias,omitempty"`
	// Weight biases StrategyWeighted; zero means the provider's own weight.
	Weight int `json:"weight,omitempty"`
	// Priority orders targets for StrategyPriority; lower wins. When zero the
	// target's position in the list is used, which keeps the common case of a
	// hand-ordered fallback list free of redundancy.
	Priority int `json:"priority,omitempty"`
	// MaxOutputTokens caps the completion for this target, allowing a cheaper
	// model to be used with a shorter allowance rather than being skipped.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Resolved pricing and capability data, copied from the registry when the
	// decision is built. Kept on the target so a decision is self-contained and
	// can be audited without re-reading the registry.
	InputCostPerMillion  float64      `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion float64      `json:"output_cost_per_million,omitempty"`
	Capabilities         []Capability `json:"capabilities,omitempty"`
	ContextWindow        int          `json:"context_window,omitempty"`
	QualityTier          int          `json:"quality_tier,omitempty"`
	Kind                 ProviderKind `json:"kind,omitempty"`
	// Health is the health state observed when the decision was made.
	Health HealthState `json:"health,omitempty"`
	// ObservedLatencyMS is the provider's recent p50 latency, when known.
	ObservedLatencyMS int64 `json:"observed_latency_ms,omitempty"`
}

// Ref returns the model reference for the target.
func (t RouteTarget) Ref() ModelRef {
	return ModelRef{
		ProviderID:   t.ProviderID,
		ProviderName: t.ProviderName,
		Model:        t.Model,
		Alias:        t.Alias,
	}
}

// IsLocal reports whether the target is a self-hosted runtime.
func (t RouteTarget) IsLocal() bool { return t.Kind.Local() }

// EstimatedCost prices a request against this target.
func (t RouteTarget) EstimatedCost(promptTokens, completionTokens int) Cost {
	in := float64(promptTokens) / 1_000_000 * t.InputCostPerMillion
	out := float64(completionTokens) / 1_000_000 * t.OutputCostPerMillion
	return Cost{USD: in + out}
}

// RetryPolicy governs repeated attempts against a single provider.
type RetryPolicy struct {
	// MaxAttempts counts the first try, so 1 disables retrying.
	MaxAttempts int `json:"max_attempts"`
	// InitialBackoff is the delay before the second attempt.
	InitialBackoff time.Duration `json:"initial_backoff"`
	// MaxBackoff caps the exponential growth.
	MaxBackoff time.Duration `json:"max_backoff"`
	// Multiplier grows the backoff between attempts.
	Multiplier float64 `json:"multiplier"`
	// Jitter randomizes the delay by up to ±20% to avoid synchronized retries
	// across replicas. Jitter is applied from the request id so it stays
	// deterministic under replay.
	Jitter bool `json:"jitter"`
	// RetryOn lists normalized error codes that justify another attempt. An
	// empty list means "use the code's own Retryable flag".
	RetryOn []ErrorCode `json:"retry_on,omitempty"`
	// HonorRetryAfter respects a provider's Retry-After hint, clamped by
	// MaxBackoff.
	HonorRetryAfter bool `json:"honor_retry_after"`
}

// DefaultRetryPolicy is the fallback used when neither policy nor configuration
// specifies retry behaviour. Two attempts with short backoff is deliberately
// conservative: long retry storms against a struggling provider are worse than
// failing over to a healthy one.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:     2,
		InitialBackoff:  150 * time.Millisecond,
		MaxBackoff:      2 * time.Second,
		Multiplier:      2.0,
		Jitter:          true,
		HonorRetryAfter: true,
	}
}

// Allows reports whether err justifies another attempt under this policy.
func (r RetryPolicy) Allows(attempt int, err error) bool {
	if attempt >= r.MaxAttempts {
		return false
	}
	e := AsError(err)
	if e == nil {
		return false
	}
	if len(r.RetryOn) > 0 {
		for _, code := range r.RetryOn {
			if code == e.Code {
				return true
			}
		}
		return false
	}
	return e.Retryable
}

// BackoffFor computes the delay before the attempt following attempt n
// (1-based). The result is clamped to MaxBackoff.
func (r RetryPolicy) BackoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := r.InitialBackoff
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	mult := r.Multiplier
	if mult <= 0 {
		mult = 2.0
	}
	d := float64(base)
	for i := 1; i < attempt; i++ {
		d *= mult
	}
	max := r.MaxBackoff
	if max <= 0 {
		max = 10 * time.Second
	}
	if d > float64(max) {
		d = float64(max)
	}
	return time.Duration(d)
}

// TimeoutPolicy governs how long CoreRouter will wait.
//
// Three distinct budgets are needed because they fail differently: a connect
// timeout means the host is unreachable, a per-attempt timeout means the model
// is too slow, and a stream idle timeout means a token stream stopped
// advancing. Collapsing them into one knob makes diagnosis impossible.
type TimeoutPolicy struct {
	// Total bounds the whole request including retries and fallbacks.
	Total time.Duration `json:"total"`
	// PerAttempt bounds a single provider call.
	PerAttempt time.Duration `json:"per_attempt"`
	// Connect bounds establishing the TCP/TLS connection.
	Connect time.Duration `json:"connect"`
	// StreamIdle bounds the gap between successive stream chunks.
	StreamIdle time.Duration `json:"stream_idle"`
	// FirstToken bounds time-to-first-byte for streaming requests.
	FirstToken time.Duration `json:"first_token"`
}

// DefaultTimeoutPolicy returns budgets suitable for interactive chat traffic
// that also leave room for long answers.
//
// The previous defaults (total 120s / per-attempt 60s) were sized for the
// latency of a short chat reply and silently truncated anything longer,
// because a per-attempt deadline shorter than a long generation is
// indistinguishable from a truncation bug. The defaults below are generous
// enough to finish a multi-thousand-token answer on a slow provider, while
// Connect and FirstToken stay tight so a genuinely stuck upstream is still
// abandoned quickly.
//
// Operators who need chat-shaped budgets set them explicitly per policy; they
// are now honored (see deriveTimeoutStrategy in internal/routing).
func DefaultTimeoutPolicy() TimeoutPolicy {
	return TimeoutPolicy{
		Total:      10 * time.Minute,
		PerAttempt: 5 * time.Minute,
		Connect:    10 * time.Second,
		StreamIdle: 90 * time.Second,
		FirstToken: 60 * time.Second,
	}
}

// Normalize fills in zero-valued fields from defaults so the rest of the system
// never has to special-case an unset duration.
func (t TimeoutPolicy) Normalize() TimeoutPolicy {
	d := DefaultTimeoutPolicy()
	if t.Total <= 0 {
		t.Total = d.Total
	}
	if t.PerAttempt <= 0 {
		t.PerAttempt = d.PerAttempt
	}
	if t.Connect <= 0 {
		t.Connect = d.Connect
	}
	if t.StreamIdle <= 0 {
		t.StreamIdle = d.StreamIdle
	}
	if t.FirstToken <= 0 {
		t.FirstToken = d.FirstToken
	}
	if t.PerAttempt > t.Total {
		t.PerAttempt = t.Total
	}
	return t
}

// FallbackPolicy controls whether and how CoreRouter moves to another provider.
type FallbackPolicy struct {
	// Enabled gates all fallback behaviour for the policy.
	Enabled bool `json:"enabled"`
	// MaxAttempts caps how many distinct providers may be attempted, counting
	// the first. Retries against a single provider are bounded separately by
	// RetryPolicy.MaxAttempts, so the two budgets are independent: the worst case
	// is MaxAttempts providers x RetryPolicy.MaxAttempts calls each, bounded in
	// practice by the request deadline.
	MaxAttempts int `json:"max_attempts"`
	// OnErrorCodes overrides the per-error FallbackEligible flag.
	OnErrorCodes []ErrorCode `json:"on_error_codes,omitempty"`
	// OnStatusCodes adds upstream HTTP statuses that should trigger fallback.
	OnStatusCodes []int `json:"on_status_codes,omitempty"`
	// BudgetAware skips fallback when the projected cost would exceed the
	// request's cost ceiling.
	BudgetAware bool `json:"budget_aware"`
	// BackoffBeforeFailover delays the next attempt, useful when the failure was
	// a rate limit shared across providers in the same region.
	BackoffBeforeFailover time.Duration `json:"backoff_before_failover"`
}

// DefaultFallbackPolicy enables a single failover by default: trying two
// providers covers the dominant real-world failure (one provider incident)
// without turning a systemic outage into a five-second stall for every client.
func DefaultFallbackPolicy() FallbackPolicy {
	return FallbackPolicy{
		Enabled:     true,
		MaxAttempts: 2,
		BudgetAware: true,
	}
}

// Allows reports whether err justifies moving to a different provider.
func (f FallbackPolicy) Allows(err error) bool {
	if !f.Enabled {
		return false
	}
	e := AsError(err)
	if e == nil {
		return false
	}
	if len(f.OnStatusCodes) > 0 && e.Status > 0 {
		for _, s := range f.OnStatusCodes {
			if s == e.Status {
				return true
			}
		}
	}
	if len(f.OnErrorCodes) > 0 {
		// An explicit list is exhaustive, not additive. An operator who lists the
		// codes that justify failover is narrowing the behaviour deliberately, so a
		// code outside the list must not fail over even though the error's own
		// FallbackEligible flag would allow it.
		for _, c := range f.OnErrorCodes {
			if c == e.Code {
				return true
			}
		}
		return false
	}
	return e.FallbackEligible
}

// PolicyLimits are the hard numeric guards a policy enforces.
type PolicyLimits struct {
	// MaxCostPerRequestUSD blocks a request whose projected cost exceeds it.
	MaxCostPerRequestUSD float64 `json:"max_cost_per_request_usd,omitempty"`
	// MaxPromptTokens rejects oversized prompts before contacting a provider.
	MaxPromptTokens int `json:"max_prompt_tokens,omitempty"`
	// MaxOutputTokens defaults the completion allowance when the client is
	// silent, and caps it when the client asks for more.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// LatencyTargetMS is the soft target used by StrategyLowestLatency and by
	// timeout derivation.
	LatencyTargetMS int `json:"latency_target_ms,omitempty"`
	// MaxLatencyMS is the hard ceiling: candidates slower than this are excluded.
	MaxLatencyMS int `json:"max_latency_ms,omitempty"`
	// RequestsPerMinute and TokensPerMinute feed Redis rate limiting.
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
	TokensPerMinute   int `json:"tokens_per_minute,omitempty"`
	// DailyBudgetUSD caps spend per tenant per UTC day.
	DailyBudgetUSD float64 `json:"daily_budget_usd,omitempty"`
	// MonthlyBudgetUSD caps spend per tenant per calendar month.
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd,omitempty"`
	// AllowedModels, when non-empty, is an allow list applied after matching.
	AllowedModels []string `json:"allowed_models,omitempty"`
	// DeniedModels is a deny list applied after the allow list.
	DeniedModels []string `json:"denied_models,omitempty"`
	// Phase 2: provider allow/deny lists (names or globs).
	AllowedProviders []string `json:"allowed_providers,omitempty"`
	DeniedProviders  []string `json:"denied_providers,omitempty"`
	// Phase 2: region constraints.
	AllowedRegions []string `json:"allowed_regions,omitempty"`
	DeniedRegions  []string `json:"denied_regions,omitempty"`
	// Phase 2: data sensitivity. DeniedSensitive lists labels that must not be
	// sent to third-party providers; AllowedSensitiveOnly, when non-empty,
	// restricts to listed labels.
	DeniedSensitive []string `json:"denied_sensitive,omitempty"`
	// Phase 2: request size guards.
	MaxRequestBytes int `json:"max_request_bytes,omitempty"`
	// Phase 2: batch vs interactive mode.
	BatchOnly       bool `json:"batch_only,omitempty"`
	InteractiveOnly bool `json:"interactive_only,omitempty"`
	// Phase 2: cache and shaping controls.
	CacheEnabled                *bool `json:"cache_enabled,omitempty"`
	BypassCacheForSensitive     bool  `json:"bypass_cache_for_sensitive,omitempty"`
	RequireCacheBypassSensitive bool  `json:"require_cache_bypass_sensitive,omitempty"`
	// Shaping limits.
	MaxHistoryMessages int `json:"max_history_messages,omitempty"`
}

// RoutingPolicy is the complete, versioned description of how a class of
// requests should be routed. Every field has a documented default so a minimal
// policy remains valid.
type RoutingPolicy struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Priority breaks ties between equally specific matches; lower wins.
	Priority int             `json:"priority"`
	Enabled  bool            `json:"enabled"`
	Match    PolicyMatch     `json:"match"`
	Strategy RoutingStrategy `json:"strategy"`
	// Targets is the ordered candidate list, also serving as the fallback chain.
	Targets  []RouteTarget  `json:"targets"`
	Fallback FallbackPolicy `json:"fallback"`
	Retry    RetryPolicy    `json:"retry"`
	Timeout  TimeoutPolicy  `json:"timeout"`
	Limits   PolicyLimits   `json:"limits"`
	// Version increments on every write; used for optimistic concurrency and
	// for citing the exact rule version in an audit event.
	Version int `json:"version"`
	// ManagedBy records whether the policy is owned by the config
	// bootstrapper or by an operator working through the admin API.
	ManagedBy ManagedBy `json:"managed_by,omitempty"`
	// CreatedBy identifies the operator who authored the policy.
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Normalize applies defaults to every unset field. It is called on load from
// the database and on create, so in-memory policies are always fully populated.
func (p *RoutingPolicy) Normalize() {
	if p.Strategy == "" {
		p.Strategy = StrategyPriority
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.Fallback.MaxAttempts <= 0 {
		if p.Fallback.Enabled {
			def := DefaultFallbackPolicy()
			p.Fallback.MaxAttempts = def.MaxAttempts
		} else {
			p.Fallback.MaxAttempts = 1
		}
	}
	if p.Retry.MaxAttempts <= 0 {
		p.Retry = DefaultRetryPolicy()
	}
	p.Timeout = p.Timeout.Normalize()
	if p.Limits.MaxOutputTokens <= 0 {
		// A ceiling that finishes an ordinary long answer, not one that cuts it
		// in half. The previous 4096 silently truncated longer generations at
		// finish_reason=length, which clients read as a truncated answer.
		p.Limits.MaxOutputTokens = 32768
	}
	// Default each target's priority to its list position so a hand-written
	// ordered list needs no priority fields.
	for i := range p.Targets {
		if p.Targets[i].Priority == 0 {
			p.Targets[i].Priority = i + 1
		}
	}
}

// MatchModels reports whether the requested model name matches the policy.
func (m PolicyMatch) MatchesModel(requested string) bool {
	if len(m.Models) == 0 {
		return true
	}
	for _, pattern := range m.Models {
		if MatchModelPattern(pattern, requested) {
			return true
		}
	}
	return false
}
