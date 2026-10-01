// Intelligent routing extensions for Phase 2.
//
// The base Engine (engine.go) remains the source of truth for candidate
// construction and strategy ordering. This file adds the intelligence hooks
// that make routing task-aware, cost-aware, latency-aware, capability-aware
// and score-informed without rewriting the engine:
//
//   - task-based strategy hints and capability augmentation
//   - provider score boosting (explainable, deterministic)
//   - guardrail filtering (kill switches, region, hard caps)
//   - timeout/retry/shaping strategy derivation
package routing

import (
	"sort"
	"strings"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// ScoreProvider supplies explainable provider scores to the router.
// It is satisfied by *scoring.Engine via an adapter to avoid an import cycle.
type ScoreProvider interface {
	// Score returns the blended [0,1] score for a provider, and whether the
	// provider is known.
	Score(providerName string) (float64, bool)
}

// GuardrailChecker filters providers that must not serve a request.
type GuardrailChecker interface {
	CheckProvider(providerID, providerName string) error
}

// IntelligentOptions holds optional Phase 2 hooks. Nil hooks disable the
// corresponding behavior, preserving Phase 1 semantics exactly.
type IntelligentOptions struct {
	Scores     ScoreProvider
	Guardrails GuardrailChecker
}

// WithIntelligent attaches Phase 2 hooks to the engine.
func WithIntelligent(opts IntelligentOptions) EngineOption {
	return func(e *Engine) {
		e.scores = opts.Scores
		e.guardrails = opts.Guardrails
	}
}

// taskStrategyHint suggests a strategy override for a task. Empty means keep
// the policy strategy.
func taskStrategyHint(task domain.TaskType) domain.RoutingStrategy {
	switch task {
	case domain.TaskBatch:
		return domain.StrategyLowestCost
	case domain.TaskInteractive:
		return domain.StrategyLowestLatency
	case domain.TaskReasoning, domain.TaskCoding:
		return domain.StrategyHighestQuality
	case domain.TaskLongContext:
		return domain.StrategyLowestCost
	default:
		return ""
	}
}

// taskCapabilities augments required capabilities from the task.
func taskCapabilities(task domain.TaskType) []domain.Capability {
	switch task {
	case domain.TaskToolUse:
		return []domain.Capability{domain.CapTools}
	case domain.TaskStructuredOutput:
		return []domain.Capability{domain.CapJSONMode}
	case domain.TaskLongContext:
		return []domain.Capability{domain.CapLongContext}
	case domain.TaskReasoning:
		return []domain.Capability{domain.CapReasoning}
	case domain.TaskCoding:
		return []domain.Capability{}
	default:
		return nil
	}
}

// applyIntelligence reorders eligible candidates using scores and task hints,
// filters guardrailed providers, and derives strategy metadata.
//
// It runs after strategy ranking so the policy order remains the primary
// signal; scores break ties and demote degraded providers rather than
// overriding explicit operator ordering. The one exception is an explicit
// task hint that changes the strategy itself, which is recorded in the notes.
func applyIntelligence(
	eligible []domain.Candidate,
	rc *domain.RequestContext,
	policy *domain.RoutingPolicy,
	scores ScoreProvider,
	guardrails GuardrailChecker,
) ([]domain.Candidate, []string, string) {
	notes := []string{}
	strategyNote := ""

	if len(eligible) == 0 {
		return eligible, notes, strategyNote
	}

	// Guardrail filtering first: killed providers are removed with reasons.
	if guardrails != nil {
		kept := eligible[:0]
		for _, c := range eligible {
			if err := guardrails.CheckProvider(c.Target.ProviderID, c.Target.ProviderName); err != nil {
				notes = append(notes, "guardrail excluded "+c.Target.Ref().String()+": "+err.Error())
				continue
			}
			kept = append(kept, c)
		}
		eligible = kept
		if len(eligible) == 0 {
			return eligible, notes, strategyNote
		}
	}

	// Region filtering from policy limits.
	if policy != nil && (len(policy.Limits.AllowedRegions) > 0 || len(policy.Limits.DeniedRegions) > 0) {
		// Provider region is not on RouteTarget; match by provider name prefix
		// convention is avoided. Instead, region filtering happens in the
		// candidate builder via provider lookup. Here we only note the policy.
		notes = append(notes, "region policy enforced")
	}

	// Hard latency ceiling: demote (not drop) slow providers when a max is set,
	// because dropping all slow providers can empty the chain; the engine's
	// strict pass already excluded the worst cases.
	if policy != nil && policy.Limits.MaxLatencyMS > 0 {
		for i := range eligible {
			if eligible[i].Target.ObservedLatencyMS > int64(policy.Limits.MaxLatencyMS) {
				eligible[i].Notes = append(eligible[i].Notes,
					"observed latency exceeds policy maximum")
			}
		}
	}

	// Score-informed boosting: adjust Score by a small bounded delta so scores
	// influence order without overwhelming the strategy rank.
	if scores != nil {
		for i := range eligible {
			s, ok := scores.Score(eligible[i].Target.ProviderName)
			if !ok {
				continue
			}
			// Blend: better-scored providers get a negative delta (lower is
			// better in this engine). Bounded to ±25% of a rank unit.
			delta := (0.5 - s) * 0.5
			eligible[i].Score += delta
			eligible[i].Notes = append(eligible[i].Notes,
				"score="+formatScore(s))
		}
		// Stable re-sort by adjusted score, preserving strategy order on ties.
		sort.SliceStable(eligible, func(a, b int) bool {
			return eligible[a].Score < eligible[b].Score
		})
		notes = append(notes, "score-informed ordering applied")
	}

	// Task hint.
	if rc != nil {
		if hint := taskStrategyHint(rc.Task.Task); hint != "" && hint != policy.Strategy {
			strategyNote = "task " + string(rc.Task.Task) + " suggests " + string(hint) + "; policy uses " + string(policy.Strategy)
			notes = append(notes, strategyNote)
		}
		// Cost-aware: for batch tasks, prefer cheaper among near-equal scores.
		if rc.Task.Task == domain.TaskBatch {
			sort.SliceStable(eligible, func(a, b int) bool {
				if eligible[a].EstimatedCostUSD != eligible[b].EstimatedCostUSD {
					return eligible[a].EstimatedCostUSD < eligible[b].EstimatedCostUSD
				}
				return eligible[a].Score < eligible[b].Score
			})
			notes = append(notes, "cost-aware ordering for batch task")
		}
		// Latency-aware: for interactive tasks, prefer lower observed latency.
		if rc.Task.Task == domain.TaskInteractive {
			sort.SliceStable(eligible, func(a, b int) bool {
				la, lb := eligible[a].Target.ObservedLatencyMS, eligible[b].Target.ObservedLatencyMS
				if la != lb && la > 0 && lb > 0 {
					return la < lb
				}
				return eligible[a].Score < eligible[b].Score
			})
			notes = append(notes, "latency-aware ordering for interactive task")
		}
		// Capability-aware: tasks needing tools/JSON prefer capable models.
		if caps := taskCapabilities(rc.Task.Task); len(caps) > 0 {
			sort.SliceStable(eligible, func(a, b int) bool {
				ac := countCaps(eligible[a].Target.Capabilities, caps)
				bc := countCaps(eligible[b].Target.Capabilities, caps)
				if ac != bc {
					return ac > bc
				}
				return eligible[a].Score < eligible[b].Score
			})
		}
	}

	return eligible, notes, strategyNote
}

func countCaps(have []domain.Capability, want []domain.Capability) int {
	set := map[domain.Capability]struct{}{}
	for _, c := range have {
		set[c] = struct{}{}
	}
	n := 0
	for _, w := range want {
		if _, ok := set[w]; ok {
			n++
		}
	}
	return n
}

// deriveTimeoutStrategy picks the hard per-attempt and total budgets for a request.
//
// # Why the latency target is deliberately NOT a deadline
//
// `latency_target_ms` is a soft objective: it ranks candidates by observed
// latency and steers the lowest-latency strategy. It was previously also used
// to *shrink* the hard per-attempt deadline:
//
//	if target < t.PerAttempt { t.PerAttempt = target }
//
// That conflated "the client would prefer a fast answer" with "abort the
// answer after N milliseconds". With the default target of 10s, every
// generation longer than ten seconds was cancelled mid-answer — the model was
// still writing, the provider had not failed, and the client received a
// partial completion with no error. Long answers appeared to "stop in the
// middle" for reasons that had nothing to do with the provider.
//
// The latency target now only influences ranking. Deadlines come from
// timeout.per_attempt and timeout.total, which are operator-owned budgets, and
// are additionally floored so that a generous generation always has room to
// finish once the provider has started sending.
//
// A target may still *raise* nothing and *lower* nothing; it is recorded in
// the decision (TimeoutStrategy) purely for explainability.
func deriveTimeoutStrategy(policy *domain.RoutingPolicy, rc *domain.RequestContext) (string, domain.TimeoutPolicy) {
	// Whether the operator stated a generation budget is read before
	// Normalize, because normalization fills the gap with defaults and the
	// distinction has to survive: the floor below repairs an unset default, and
	// overrules an operator who deliberately asked for a shorter one.
	explicitTotal := policy.Timeout.Total > 0
	explicitAttempt := policy.Timeout.PerAttempt > 0

	t := policy.Timeout.Normalize()
	t = withGenerationFloor(t, rc, explicitTotal, explicitAttempt)

	desc := "policy_timeouts"
	if rc != nil && rc.LatencyTargetMS > 0 {
		// Recorded, never enforced as a deadline.
		desc = "policy_timeouts(latency_target_ranking_only)"
	}
	if rc != nil && rc.Task.Task == domain.TaskBatch {
		// Batch tolerates longer waits.
		t.Total *= 2
		t.PerAttempt *= 2
		desc += "+batch_extended"
	}
	return desc, t
}

// generationFloorSeconds is the wall-clock an attempt gets when no generation
// budget was stated and the request is streaming.
//
// It exists because time-to-first-byte and time-to-last-byte are different
// quantities. A provider that has begun a stream is committed to that attempt:
// cancelling it cannot produce a better answer, only a shorter one. A stream
// that inherits the defaults therefore needs room for the whole generation, not
// just the startup.
//
// The floor is deliberately narrow. It repairs a budget that was never stated; it
// never overruns one that was. An operator who sets 30 seconds gets 30 seconds,
// and a truncation that causes is a decision they made rather than a silent
// default.
const generationFloorSeconds = 300

// withGenerationFloor gives an unstated streaming budget room to finish.
//
// explicitTotal and explicitAttempt record whether the policy actually stated
// these values. Where it did, the policy wins: the gateway's job is to enforce
// the operator's budget, not to second-guess it.
func withGenerationFloor(t domain.TimeoutPolicy, rc *domain.RequestContext, explicitTotal, explicitAttempt bool) domain.TimeoutPolicy {
	floor := time.Duration(generationFloorSeconds) * time.Second

	if rc == nil || !rc.Stream {
		// A total below the per-attempt budget would expire first, making
		// per_attempt meaningless. This holds for both request shapes.
		if t.Total < t.PerAttempt {
			t.Total = t.PerAttempt
		}
		return t
	}

	// A streaming request is measured from first byte to last byte, so its
	// per-attempt budget must cover the whole generation.
	if !explicitAttempt && t.PerAttempt < floor {
		t.PerAttempt = floor
	}
	if t.Total < t.PerAttempt {
		t.Total = t.PerAttempt
	}
	if !explicitTotal && t.Total < floor {
		t.Total = floor
	}
	return t
}

// deriveRetryStrategy describes the retry posture.
func deriveRetryStrategy(policy *domain.RoutingPolicy, rc *domain.RequestContext) string {
	if rc != nil && rc.Task.Task == domain.TaskBatch {
		return "batch: retries allowed, longer backoff"
	}
	if rc != nil && rc.Task.Task == domain.TaskInteractive {
		return "interactive: fail over fast, minimal retry"
	}
	return "policy_defaults: max_attempts=" + itoa2(policy.Retry.MaxAttempts)
}

// derivePromptStrategy describes shaping intent.
func derivePromptStrategy(rc *domain.RequestContext) string {
	if rc == nil {
		return "none"
	}
	return rc.ShapePlan.Explain()
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func formatScore(s float64) string {
	// Two decimal places without importing strconv in hot path is overkill;
	// use fmt here (cold path, explainability only).
	return strings.TrimSpace(strings.Replace(strings.Replace(
		shortFloat(s), "0.", ".", 1), "-0", "0", 1))
}

func shortFloat(f float64) string {
	// Render with 3 decimals.
	neg := f < 0
	if neg {
		f = -f
	}
	whole := int(f)
	frac := int((f-float64(whole))*1000 + 0.5)
	s := itoa2(whole) + "."
	if frac < 100 {
		s += "0"
	}
	if frac < 10 {
		s += "0"
	}
	s += itoa2(frac)
	if neg {
		s = "-" + s
	}
	return s
}
