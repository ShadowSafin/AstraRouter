package cost

// Savings names where Synapass demonstrably avoided spend. Every figure is
// computed from recorded rows, never modeled: a saving is the difference
// between what was billed and what the same request would have billed
// without the platform feature, with both numbers on the books.
type Savings struct {
	// CacheUSD is spend avoided by served-from-cache responses: their
	// estimates (what serving would have billed) summed while actuals stayed
	// zero.
	CacheUSD float64 `json:"cache_usd"`
	// RoutingUSD is spend avoided by shaping and optimization: the sum of
	// (cost_before - cost_after) over requests where optimization lowered the
	// eligible cost.
	RoutingUSD float64 `json:"routing_usd"`
	// FallbackUSD is spend avoided when a fallback served cheaper than the
	// failed primary's estimate: primary estimate minus actual, floored at
	// zero per request so an expensive fallback never counts as negative
	// savings.
	FallbackUSD float64 `json:"fallback_usd"`
}

// Total is the combined avoided spend.
func (s Savings) Total() float64 { return RoundMicro(s.CacheUSD + s.RoutingUSD + s.FallbackUSD) }

// FallbackSaving scores one fallback request: what the failed primary was
// projected to cost minus what the request actually billed. A fallback that
// cost more than the primary's projection scores zero, not negative — the
// platform paid extra, but "negative savings" would punish the reliability
// feature for doing its job on an expensive model.
func FallbackSaving(primaryEstimateUSD, actualUSD float64) float64 {
	if primaryEstimateUSD <= actualUSD {
		return 0
	}
	return RoundMicro(primaryEstimateUSD - actualUSD)
}

// RoutingSaving scores one optimized request: the eligible cost before
// shaping minus the cost after. Shaping that raised the cost (a quality
// upgrade, say) scores zero here — it was not a saving.
func RoutingSaving(costBeforeUSD, costAfterUSD float64) float64 {
	if costBeforeUSD <= costAfterUSD {
		return 0
	}
	return RoundMicro(costBeforeUSD - costAfterUSD)
}
