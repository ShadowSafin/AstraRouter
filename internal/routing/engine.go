package routing

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// Defaults holds engine-wide fallbacks applied when a policy is silent. They are
// converted from the routing section of the configuration file by the bootstrap
// layer, which keeps this package free of a dependency on the config package.
type Defaults struct {
	Strategy    domain.RoutingStrategy
	MaxAttempts int
	Fallback    domain.FallbackPolicy
	Retry       domain.RetryPolicy
	Timeout     domain.TimeoutPolicy
	Limits      domain.PolicyLimits
	// LatencyPriors are static latency estimates in milliseconds, keyed by model
	// or provider name, consulted before real observations exist.
	LatencyPriors map[string]int64
	// DeregisteredModelStatuses lists the model statuses that are ineligible in
	// the strict pass. It exists so tests and unusual deployments can treat
	// deprecated models as fully routable.
	DeregisteredModelStatuses []domain.ModelStatus
}

// DefaultDefaults returns conservative defaults matching the shipped
// configuration file.
func DefaultDefaults() Defaults {
	return Defaults{
		Strategy:    domain.StrategyPriority,
		MaxAttempts: 2,
		Fallback:    domain.DefaultFallbackPolicy(),
		Retry:       domain.DefaultRetryPolicy(),
		Timeout:     domain.DefaultTimeoutPolicy(),
		Limits: domain.PolicyLimits{
			MaxOutputTokens:   4096,
			LatencyTargetMS:   10000,
			RequestsPerMinute: 600,
		},
	}
}

// Availability reports whether a provider has a usable adapter.
//
// Routing consults this so a provider that failed adapter construction (a bad
// base URL, a missing credential, an unimplemented kind) is excluded from
// candidate selection rather than being selected and then failing on the request
// path. That turns a configuration error into a routing explanation instead of a
// user-visible 502.
type Availability interface {
	Available(providerID, providerName string) bool
}

// NopAvailability reports every provider as available.
type NopAvailability struct{}

// Available implements Availability.
func (NopAvailability) Available(string, string) bool { return true }

// Engine resolves requests to route decisions.
type Engine struct {
	catalogue    Catalogue
	policies     PolicyResolver
	health       HealthProvider
	availability Availability
	defaults     Defaults
	logger       *slog.Logger
	// Phase 2 optional intelligence hooks. Nil preserves Phase 1 behavior.
	scores     ScoreProvider
	guardrails GuardrailChecker
}

// EngineOption customizes an engine.
type EngineOption func(*Engine)

// WithDefaults overrides the engine-wide defaults.
func WithDefaults(d Defaults) EngineOption {
	return func(e *Engine) { e.defaults = d }
}

// WithAvailability supplies the adapter availability check.
func WithAvailability(a Availability) EngineOption {
	return func(e *Engine) {
		if a != nil {
			e.availability = a
		}
	}
}

// WithLogger attaches a logger.
func WithLogger(l *slog.Logger) EngineOption {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// NewEngine constructs a routing engine.
func NewEngine(catalogue Catalogue, policies PolicyResolver, health HealthProvider, opts ...EngineOption) *Engine {
	e := &Engine{
		catalogue:    catalogue,
		policies:     policies,
		health:       health,
		availability: NopAvailability{},
		defaults:     DefaultDefaults(),
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Resolve produces the route decision for a request.
//
// It mutates exactly three fields on the request context, all of which are
// derived facts the rest of the pipeline needs:
//
//   - MaxOutputTokens  the completion allowance after applying policy limits
//   - Deadline         the absolute time by which a response must be produced
//   - Resolution       the decision itself
//
// Nothing else is touched, which is what lets the executor and the API layer
// treat the context as read-only.
func (e *Engine) Resolve(ctx context.Context, rc *domain.RequestContext) (*domain.RouteDecision, error) {
	if rc == nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "routing was called with a nil request context")
	}
	if strings.TrimSpace(rc.RequestedModel) == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "you must provide a model parameter")
	}

	index, err := e.index(ctx)
	if err != nil {
		return nil, err
	}

	policy, err := e.resolvePolicy(ctx, rc)
	if err != nil {
		return nil, err
	}

	// An endpoint scope constrains this request beyond the stored policy. The
	// override folds into a policy copy so shared rows never leak one scope's
	// constraints into another request.
	if rc.EndpointOverride != nil {
		policy, err = applyEndpointOverride(rc, index, policy)
		if err != nil {
			return nil, err
		}
	}

	targets := policy.Targets
	if len(targets) == 0 {
		// A policy with no targets is normal for the synthesized default: it
		// means "route to every provider that serves the requested model".
		targets = e.synthesizeTargets(index, rc.RequestedModel)
		if len(targets) == 0 {
			return nil, domain.NewError(domain.ErrCodeNotFound,
				fmt.Sprintf("no provider serves model %q", rc.RequestedModel))
		}
	}

	e.applyLimits(rc, policy)

	ceiling := e.costCeiling(rc, policy)
	builder := newCandidateBuilder(index, e.health, e.availability, rc, policy, ceiling, e.defaults)

	// Three passes, from strict to permissive. Each relaxation is recorded in the
	// decision so an operator can see that quality of service was traded away,
	// rather than discovering it from a graph after the fact.
	passes := []constraintSet{
		{requireHealthy: true, enforceCostCeiling: true, excludeDeprecated: true},
		{requireHealthy: false, enforceCostCeiling: true, excludeDeprecated: true},
		{requireHealthy: false, enforceCostCeiling: false, excludeDeprecated: false},
	}

	var (
		candidates []domain.Candidate
		pass       int
	)
	for i, cs := range passes {
		built := builder.build(targets, cs)
		if hasEligible(built) {
			candidates = built
			pass = i
			break
		}
		// Keep the last attempt so its rejection reasons can be reported.
		if i == len(passes)-1 {
			candidates = built
			pass = i
		}
	}

	eligible := filterEligible(candidates)
	if len(eligible) == 0 {
		return nil, noRouteError(rc, targets, candidates)
	}

	observed := e.latencyObservations()
	ranked := rankCandidates(eligible, policy.Strategy, hashRequestSeed(rc), observed, e.defaults.LatencyPriors)
	if len(ranked) == 0 {
		return nil, noRouteError(rc, targets, candidates)
	}

	// Phase 2 intelligence: guardrail filtering, score-informed ordering,
	// task-aware cost/latency/capability preferences. No-ops when hooks are nil.
	intelNotes := []string{}
	timeoutStrategy := ""
	retryStrategy := ""
	if e.scores != nil || e.guardrails != nil || rc.Task.Task != "" {
		var strategyNote string
		ranked, intelNotes, strategyNote = applyIntelligence(ranked, rc, policy, e.scores, e.guardrails)
		_ = strategyNote
		if len(ranked) == 0 {
			return nil, noRouteError(rc, targets, candidates)
		}
	}
	// Derive timeout/retry/prompt strategies for observability.
	timeoutStrategy, derivedTimeout := deriveTimeoutStrategy(policy, rc)
	retryStrategy = deriveRetryStrategy(policy, rc)
	promptStrategy := derivePromptStrategy(rc)
	// Apply derived timeout (clamped, never looser than policy total).
	effectiveTimeout := policy.Timeout.Normalize()
	if derivedTimeout.PerAttempt > 0 && derivedTimeout.PerAttempt < effectiveTimeout.PerAttempt {
		effectiveTimeout.PerAttempt = derivedTimeout.PerAttempt
	}
	if derivedTimeout.Total > effectiveTimeout.Total {
		effectiveTimeout.Total = derivedTimeout.Total
	}

	// Chain length is bounded by the fallback policy. The ranked order doubles as
	// the fallback order, which is what makes "cheapest first, then next cheapest"
	// behave sensibly after a failure, and what keeps a weighted rotation's
	// failover order consistent with its primary choice.
	ordered := ranked
	limit := policy.Fallback.MaxAttempts
	if !policy.Fallback.Enabled {
		limit = 1
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > len(ordered) {
		limit = len(ordered)
	}

	chainTargets := make([]domain.RouteTarget, 0, limit)
	for _, c := range ordered[:limit] {
		chainTargets = append(chainTargets, c.Target)
	}

	chosen := chainTargets[0]
	estimated := ordered[0].EstimatedCostUSD

	skipped := make([]domain.SkippedTarget, 0, len(candidates))
	for _, c := range candidates {
		if c.Eligible {
			continue
		}
		skipped = append(skipped, domain.SkippedTarget{Target: c.Target, Reason: c.RejectionReason})
	}

	// A decision is "degraded" when the policy's first declared target did not
	// serve it: either it was ineligible, or a relaxation pass was needed.
	degraded := pass > 0 || ordered[0].Group != 0

	decision := &domain.RouteDecision{
		RequestID:            rc.RequestID,
		PolicyID:             policy.ID,
		PolicyName:           policy.Name,
		Strategy:             policy.Strategy,
		Chosen:               chosen,
		Chain:                domain.FallbackChain{Targets: chainTargets, Skipped: skipped, MaxAttempts: limit},
		Retry:                policy.Retry,
		Timeout:              effectiveTimeout,
		Fallback:             policy.Fallback,
		EstimatedCost:        domain.Cost{USD: estimated},
		CostCeilingUSD:       ceiling,
		Candidates:           ordered,
		Degraded:             degraded,
		DecidedAt:            domain.Now(),
		MatchedPolicyVersion: policy.Version,
		Task:                 rc.Task,
		Shaping:              rc.ShapePlan,
		UseCache:             rc.PolicyDecision != nil && rc.PolicyDecision.UseCache,
		TimeoutStrategy:      timeoutStrategy,
		RetryStrategy:        retryStrategy,
		PromptStrategy:       promptStrategy,
		ScoreNotes:           intelNotes,
		CostBeforeUSD:        estimated,
		CostAfterUSD:         estimated,
	}
	if rc.PolicyDecision != nil {
		decision.Rejected = rc.PolicyDecision.RejectedTargets
	}
	decision.Reason = explainDecision(policy, ordered, pass, len(candidates))
	if rc.EndpointOverride != nil && rc.EndpointID != "" {
		decision.Reason += "; endpoint scope " + rc.EndpointID
	}
	rc.Resolution = decision

	return decision, nil
}

// index returns an indexed view of the catalogue, preferring a cached snapshot
// when the catalogue provides one.
func (e *Engine) index(ctx context.Context) (*StaticCatalogue, error) {
	if s, ok := e.catalogue.(interface{ Snapshot() *StaticCatalogue }); ok {
		return s.Snapshot(), nil
	}
	models, err := e.catalogue.Models(ctx)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "failed to read the model registry").Wrap(err)
	}
	providers, err := e.catalogue.Providers(ctx)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "failed to read the provider registry").Wrap(err)
	}
	return NewStaticCatalogue(models, providers), nil
}

// resolvePolicy returns the applicable policy, falling back to a synthesized one
// so the engine has exactly one downstream code path.
func (e *Engine) resolvePolicy(ctx context.Context, rc *domain.RequestContext) (*domain.RoutingPolicy, error) {
	if e.policies != nil {
		policy, err := e.policies.Resolve(ctx, rc)
		if err != nil {
			return nil, domain.NewError(domain.ErrCodeInternal, "failed to resolve the routing policy").Wrap(err)
		}
		if policy != nil {
			policy.Normalize()
			return policy, nil
		}
	}
	policy := e.defaultPolicy()
	return policy, nil
}

// defaultPolicy synthesizes the policy used when nothing matches.
func (e *Engine) defaultPolicy() *domain.RoutingPolicy {
	p := &domain.RoutingPolicy{
		ID:          "",
		Name:        "default",
		Description: "synthesized: no stored policy matched this request",
		Enabled:     true,
		Strategy:    e.defaults.Strategy,
		Fallback:    e.defaults.Fallback,
		Retry:       e.defaults.Retry,
		Timeout:     e.defaults.Timeout,
		Limits:      e.defaults.Limits,
		CreatedAt:   domain.Now(),
		UpdatedAt:   domain.Now(),
	}
	if p.Fallback.MaxAttempts <= 0 {
		p.Fallback.MaxAttempts = e.defaults.MaxAttempts
	}
	if p.Strategy == "" {
		p.Strategy = domain.StrategyPriority
	}
	p.Normalize()
	return p
}

// synthesizeTargets builds a target list from the registry for a requested model.
//
// Ordering is by provider priority, then by the model's quality tier, which means
// an operator expresses the default routing preference through fields they set
// anyway rather than needing a policy row for every model.
func (e *Engine) synthesizeTargets(index *StaticCatalogue, requestedModel string) []domain.RouteTarget {
	matches := index.FindModels(requestedModel)
	if len(matches) == 0 {
		return nil
	}

	type ordered struct {
		model    domain.Model
		provider domain.Provider
	}
	entries := make([]ordered, 0, len(matches))
	for _, m := range matches {
		p, ok := index.ProviderByID(m.ProviderID)
		if !ok {
			continue
		}
		entries = append(entries, ordered{model: m, provider: p})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.provider.Priority != b.provider.Priority {
			return a.provider.Priority < b.provider.Priority
		}
		if a.model.QualityTier != b.model.QualityTier {
			return a.model.QualityTier > b.model.QualityTier
		}
		return a.provider.Name < b.provider.Name
	})

	out := make([]domain.RouteTarget, 0, len(entries))
	for i, entry := range entries {
		out = append(out, domain.RouteTarget{
			ProviderID:   entry.provider.ID,
			ProviderName: entry.provider.Name,
			Model:        entry.model.Name,
			Alias:        requestedModel,
			Priority:     i + 1,
			Weight:       entry.provider.EffectiveWeight(),
		})
	}
	return out
}

// applyLimits derives the effective completion allowance and request deadline.
func (e *Engine) applyLimits(rc *domain.RequestContext, policy *domain.RoutingPolicy) {
	limit := policy.Limits.MaxOutputTokens
	if limit <= 0 {
		limit = e.defaults.Limits.MaxOutputTokens
	}

	// The client's own request is recorded before anything is substituted, so
	// downstream code can tell a silent client from one that asked for a
	// specific length. That distinction is what makes a truncated answer
	// reportable instead of invisible.
	if rc.RequestedOutputTokens <= 0 {
		rc.RequestedOutputTokens = rc.MaxOutputTokens
	}

	requested := rc.MaxOutputTokens
	switch {
	case requested <= 0:
		// No client preference. rc.MaxOutputTokens becomes the policy ceiling,
		// which is used for costing and context fitting, but it is deliberately
		// not sent upstream: a ceiling the client never asked for must not
		// silently become its maximum. See effectiveMaxTokens.
		requested = limit
	case limit > 0 && requested > limit:
		// A client asking for more than policy allows is clamped rather than
		// rejected. Rejecting would break clients that request a large maximum
		// defensively, which is common.
		requested = limit
	}
	rc.MaxOutputTokens = requested

	if rc.LatencyTargetMS <= 0 {
		rc.LatencyTargetMS = policy.Limits.LatencyTargetMS
	}
	if rc.LatencyTargetMS <= 0 {
		rc.LatencyTargetMS = e.defaults.Limits.LatencyTargetMS
	}

	// The deadline is the total timeout measured from receipt. Setting it here
	// means every downstream stage (retry backoff, fallback, streaming) shares one
	// view of how much budget remains.
	if rc.Deadline.IsZero() {
		total := policy.Timeout.Total
		if total <= 0 {
			total = e.defaults.Timeout.Total
		}
		received := rc.ReceivedAt
		if received.IsZero() {
			received = domain.Now()
			rc.ReceivedAt = received
		}
		rc.Deadline = received.Add(total)
	}
}

// costCeiling combines the client's ceiling with the policy's.
//
// The tighter of the two wins, because either party may wish to spend less than
// the other allows. A zero ceiling in both places means unbounded.
func (e *Engine) costCeiling(rc *domain.RequestContext, policy *domain.RoutingPolicy) float64 {
	ceiling := rc.CostCeilingUSD
	policyCeiling := policy.Limits.MaxCostPerRequestUSD
	if policyCeiling <= 0 {
		policyCeiling = e.defaults.Limits.MaxCostPerRequestUSD
	}
	switch {
	case ceiling <= 0:
		ceiling = policyCeiling
	case policyCeiling > 0 && policyCeiling < ceiling:
		ceiling = policyCeiling
	}
	return ceiling
}

// latencyObservations returns observed per-provider latency, when the health
// source can supply it.
func (e *Engine) latencyObservations() map[string]time.Duration {
	if provider, ok := e.health.(interface {
		LatencyEstimates() map[string]time.Duration
	}); ok {
		return provider.LatencyEstimates()
	}
	return nil
}

// constraintSet describes how strict candidate filtering is for a pass.
type constraintSet struct {
	// requireHealthy excludes providers whose health is unhealthy or whose
	// circuit breaker is open.
	requireHealthy bool
	// enforceCostCeiling excludes candidates whose projected cost exceeds the
	// request's ceiling.
	enforceCostCeiling bool
	// excludeDeprecated excludes models marked deprecated or disabled.
	excludeDeprecated bool
}

// hasEligible reports whether any candidate survived a pass.
func hasEligible(candidates []domain.Candidate) bool {
	for _, c := range candidates {
		if c.Eligible {
			return true
		}
	}
	return false
}

// filterEligible returns only the eligible candidates, preserving order.
func filterEligible(candidates []domain.Candidate) []domain.Candidate {
	out := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Eligible {
			out = append(out, c)
		}
	}
	return out
}

// explainDecision renders the human-readable justification stored on the
// decision and shown in the dashboard's request detail view.
func explainDecision(policy *domain.RoutingPolicy, ranked []domain.Candidate, pass, totalCandidates int) string {
	var b strings.Builder

	if policy.ID == "" {
		b.WriteString("no stored policy matched; routed by registry defaults")
	} else {
		fmt.Fprintf(&b, "policy %q matched", policy.Name)
	}
	fmt.Fprintf(&b, "; %d of %d candidate(s) eligible", len(ranked), totalCandidates)

	switch pass {
	case 1:
		b.WriteString("; health filter relaxed because no healthy candidate was available")
	case 2:
		b.WriteString("; health and cost ceiling relaxed because no candidate satisfied both")
	}

	if len(ranked) > 0 {
		fmt.Fprintf(&b, "; selected %s by %s strategy",
			ranked[0].Target.Ref().String(), string(policy.Strategy))
		if n := len(ranked); n > 1 {
			fmt.Fprintf(&b, " (%d fallback(s) available)", n-1)
		}
	}
	return b.String()
}

// noRouteError builds a diagnostic error listing why candidates were rejected.
func noRouteError(rc *domain.RequestContext, targets []domain.RouteTarget, candidates []domain.Candidate) error {
	// Group identical rejection reasons so the message stays short even when a
	// policy names fifty targets.
	counts := map[string]int{}
	for _, c := range candidates {
		if c.RejectionReason == "" {
			continue
		}
		counts[c.RejectionReason]++
	}

	reasons := make([]string, 0, len(counts))
	for reason, n := range counts {
		if n > 1 {
			reasons = append(reasons, fmt.Sprintf("%s (%d targets)", reason, n))
		} else {
			reasons = append(reasons, reason)
		}
	}
	sort.Strings(reasons)

	msg := fmt.Sprintf("no provider is eligible for model %q", rc.RequestedModel)
	switch {
	case len(candidates) == 0 && len(targets) == 0:
		msg = fmt.Sprintf("no provider serves model %q", rc.RequestedModel)
	case len(reasons) > 0:
		msg = fmt.Sprintf("%s: %s", msg, strings.Join(reasons, "; "))
	}

	err := domain.NewError(domain.ErrCodeUnavailable, msg)
	if len(candidates) == 0 && len(targets) == 0 {
		err = domain.NewError(domain.ErrCodeNotFound, msg)
	}
	return err
}
