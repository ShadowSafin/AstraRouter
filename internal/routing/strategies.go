package routing

import (
	"hash/fnv"
	"sort"
	"strconv"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// defaultLatencyPriorMS is the latency assumed for a provider with no history.
//
// A middling value is chosen on purpose. Assuming a very low latency would make
// an unmeasured provider win every lowest-latency comparison until it
// accumulated real data; assuming a very high one would make a newly added
// provider unreachable. A middle value lets warm providers keep their advantage
// while a new provider still gets traffic once it is the best remaining option.
const defaultLatencyPriorMS int64 = 800

// rankCandidates orders eligible candidates according to a strategy.
//
// Every strategy sorts ascending by Candidate.Score, so callers compare uniformly.
// The ordering is fully deterministic for a given (strategy, candidates, seed)
// triple: no randomness is drawn at call time, which is what makes routing
// reproducible in a replay and stable when a request is retried.
func rankCandidates(
	candidates []domain.Candidate,
	strategy domain.RoutingStrategy,
	seed string,
	observed map[string]time.Duration,
	priors map[string]int64,
) []domain.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	out := make([]domain.Candidate, len(candidates))
	copy(out, candidates)

	switch strategy {
	case domain.StrategyLowestCost:
		rankByLowestCost(out)
	case domain.StrategyLowestLatency:
		rankByLowestLatency(out, observed, priors)
	case domain.StrategyHighestQuality:
		rankByHighestQuality(out)
	case domain.StrategyWeighted:
		out = rankByWeight(out, seed)
	default:
		rankByPriority(out)
	}
	return out
}

// rankByPriority implements the default hand-ordered strategy.
//
// Lower Priority wins. Ties are broken by provider priority and then by name so
// the order is stable even when a policy lists several targets at the same
// priority, which happens when targets are generated rather than hand-written.
func rankByPriority(candidates []domain.Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Target.Priority != b.Target.Priority {
			return a.Target.Priority < b.Target.Priority
		}
		if a.Score != b.Score {
			return a.Score < b.Score
		}
		return targetSortKey(a) < targetSortKey(b)
	})
}

// rankByLowestCost sorts by projected request cost, then by priority so an
// equally priced pair keeps the operator's intent.
func rankByLowestCost(candidates []domain.Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.EstimatedCostUSD != b.EstimatedCostUSD {
			return a.EstimatedCostUSD < b.EstimatedCostUSD
		}
		if a.Target.Priority != b.Target.Priority {
			return a.Target.Priority < b.Target.Priority
		}
		return targetSortKey(a) < targetSortKey(b)
	})
}

// rankByLowestLatency sorts by observed latency, falling back to the configured
// prior for the model and then to a fixed default.
func rankByLowestLatency(candidates []domain.Candidate, observed map[string]time.Duration, priors map[string]int64) {
	for i := range candidates {
		candidates[i].Score = float64(latencyFor(candidates[i], observed, priors))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Score != b.Score {
			return a.Score < b.Score
		}
		return targetSortKey(a) < targetSortKey(b)
	})
}

// latencyFor resolves the latency estimate for a candidate in milliseconds.
func latencyFor(c domain.Candidate, observed map[string]time.Duration, priors map[string]int64) int64 {
	// An explicit observation on the target wins: it is measured, not assumed.
	if c.Target.ObservedLatencyMS > 0 {
		return c.Target.ObservedLatencyMS
	}
	if d, ok := observed[c.Target.ProviderID]; ok && d > 0 {
		return d.Milliseconds()
	}
	if priors != nil {
		if ms, ok := priors[c.Target.Model]; ok && ms > 0 {
			return ms
		}
		// A prior keyed by the provider name is also honoured, which lets an
		// operator express "everything on the local cluster is fast".
		if ms, ok := priors[c.Target.ProviderName]; ok && ms > 0 {
			return ms
		}
	}
	return defaultLatencyPriorMS
}

// rankByHighestQuality sorts by the operator-assigned quality tier, then by cost.
func rankByHighestQuality(candidates []domain.Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Target.QualityTier != b.Target.QualityTier {
			return a.Target.QualityTier > b.Target.QualityTier
		}
		if a.EstimatedCostUSD != b.EstimatedCostUSD {
			return a.EstimatedCostUSD < b.EstimatedCostUSD
		}
		return targetSortKey(a) < targetSortKey(b)
	})
}

// rankByWeight implements weighted routing as a deterministic rotation.
//
// Classic weighted routing draws a random number per request. Drawing from the
// request id instead produces the same long-run traffic distribution while
// remaining reproducible: the same request routed twice lands on the same
// provider, which matters for debugging and for replay. The rotation starts at
// the selected candidate and wraps around, so weights shape not only the primary
// choice but the failover order behind it.
func rankByWeight(candidates []domain.Candidate, seed string) []domain.Candidate {
	total := 0
	for _, c := range candidates {
		total += weightOf(c)
	}
	if total <= 0 {
		rankByPriority(candidates)
		return candidates
	}

	// A stable hash of the seed yields a point in [0, total).
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	point := int(h.Sum64() % uint64(total))

	cumulative := 0
	start := 0
	for i, c := range candidates {
		cumulative += weightOf(c)
		if point < cumulative {
			start = i
			break
		}
	}

	rotated := make([]domain.Candidate, 0, len(candidates))
	rotated = append(rotated, candidates[start:]...)
	rotated = append(rotated, candidates[:start]...)

	// Scores record the rotation position so the decision record explains the
	// order without re-deriving it.
	for i := range rotated {
		rotated[i].Score = float64(i)
	}
	return rotated
}

// weightOf returns a candidate's effective weight, defaulting to one.
//
// Zero is treated as one rather than as "never selected": a policy author who
// omits a weight means "normal share", and silently excluding a target because of
// a missing field would be a surprising way to lose a fallback.
func weightOf(c domain.Candidate) int {
	if c.Target.Weight > 0 {
		return c.Target.Weight
	}
	return 1
}

// targetSortKey produces a total order tiebreaker for a candidate.
func targetSortKey(c domain.Candidate) string {
	return c.Target.ProviderName + "\x00" + c.Target.Model
}

// hashRequestSeed derives a stable per-request seed for deterministic strategies.
func hashRequestSeed(rc *domain.RequestContext) string {
	if rc == nil {
		return ""
	}
	// The request id alone is enough; including the model would make two
	// requests for the same prompt diverge for no reason.
	return string(rc.RequestID)
}

// formatFloat renders a float for log and reason strings without exponent
// notation, which would be unreadable in an operator-facing message.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
