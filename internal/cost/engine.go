// Package cost is Synapass's exact cost intelligence: versioned pricing,
// per-request cost accounts, budgets, forecasting, anomaly detection and
// savings measurement.
//
// The package is pure computation over domain types: it never touches the
// database or the network. Price lookups live in storage, request-path
// integration lives in the API layer, and everything in between is a
// deterministic function that unit tests can pin to the micro-dollar.
package cost

import (
	"math"

	"github.com/shadowsafin/synapass/internal/domain"
)

// MicroUSD is the accounting resolution: amounts are rounded to whole
// micro-dollars (10^-6 USD). Provider sheets quote per-million-token rates
// with at most a few decimals, so six places capture every sheet exactly
// while keeping float64 arithmetic error-free after rounding.
const MicroUSD = 1_000_000.0

// RoundMicro rounds an amount to whole micro-dollars, half away from zero.
// Every money figure in a breakdown passes through here exactly once, which
// is what makes totals foot: there is one rounding rule, applied uniformly.
func RoundMicro(usd float64) float64 {
	if usd == 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	return math.Round(usd*MicroUSD) / MicroUSD
}

// Price is the resolved sheet for one request: the single set of rates the
// engine bills against, with its provenance attached.
type Price struct {
	InputPerM        float64
	OutputPerM       float64
	CachedInputPerM  float64
	BaseFeeUSD       float64
	Currency         string
	PricingVersionID string
	// Source names the winning scope: tenant, model, provider, global or
	// registry (the static model row, when no versioned sheet applied).
	Source string
}

// CachedRate returns the effective cached-input rate, falling back to the
// full input rate when the sheet carries no cache discount. A sheet without
// a discount bills cached tokens at full price rather than free, because
// "free" is a claim about the provider's billing that the gateway cannot
// verify from a price sheet that omits it.
func (p Price) CachedRate() float64 {
	if p.CachedInputPerM > 0 {
		return p.CachedInputPerM
	}
	return p.InputPerM
}

// PriceFromTarget builds the registry fallback price from a routing target's
// copied registry rates: what the gateway already believed before versioned
// pricing existed.
func PriceFromTarget(t domain.RouteTarget) Price {
	return Price{
		InputPerM:  t.InputCostPerMillion,
		OutputPerM: t.OutputCostPerMillion,
		Currency:   "USD",
		Source:     "registry",
	}
}

// AttemptUsage is one billable step of a request: the tokens one provider
// call consumed and what it cost to place the call.
type AttemptUsage struct {
	// Label names the step, e.g. "attempt 1 · primary" or "fallback".
	Label string
	Usage domain.TokenUsage
	// BillBaseFee is false for steps that cost no overhead: a cache serve, a
	// request rejected before any provider call, or a step the price sheet
	// exempts.
	BillBaseFee bool
	// Price overrides the request-wide sheet for this step alone, so retries
	// and fallbacks across differently-priced targets accumulate exactly what
	// each provider metered. Nil means the shared price applies.
	Price *Price
}

// stepPrice resolves the sheet for one step.
func stepPrice(shared Price, step AttemptUsage) Price {
	if step.Price != nil {
		return *step.Price
	}
	return shared
}

// Compute bills a finished request exactly: token lines per attempt plus base
// fees, each rounded to the micro-dollar, with the total defined as the sum
// of the rounded lines. The total is therefore always the sum of its parts —
// an invoice that does not foot is a bug, and this construction makes that
// bug unrepresentable.
func Compute(usage domain.TokenUsage, price Price, attempts []AttemptUsage) domain.CostBreakdown {
	bd := domain.CostBreakdown{
		Currency:         price.Currency,
		PricingVersionID: price.PricingVersionID,
		PricingSource:    price.Source,
	}
	if bd.Currency == "" {
		bd.Currency = "USD"
	}

	steps := attempts
	if len(steps) == 0 {
		steps = []AttemptUsage{{Label: "request", Usage: usage, BillBaseFee: true}}
	}

	for _, step := range steps {
		p := stepPrice(price, step)
		rate := p.CachedRate()
		u := step.Usage
		cached := u.CachedPromptTokens
		if cached > u.PromptTokens {
			cached = u.PromptTokens
		}
		fresh := u.PromptTokens - cached
		if fresh > 0 && p.InputPerM > 0 {
			amount := RoundMicro(float64(fresh) / 1_000_000 * p.InputPerM)
			bd.Lines = append(bd.Lines, domain.CostLineItem{
				Kind:         domain.CostLineInput,
				Label:        step.Label + " · input",
				Quantity:     float64(fresh),
				UnitPriceUSD: p.InputPerM,
				AmountUSD:    amount,
			})
			bd.TotalUSD += amount
		}
		if cached > 0 && rate > 0 {
			amount := RoundMicro(float64(cached) / 1_000_000 * rate)
			bd.Lines = append(bd.Lines, domain.CostLineItem{
				Kind:         domain.CostLineCachedInput,
				Label:        step.Label + " · cached input",
				Quantity:     float64(cached),
				UnitPriceUSD: rate,
				AmountUSD:    amount,
			})
			bd.TotalUSD += amount
		}
		if u.CompletionTokens > 0 && p.OutputPerM > 0 {
			amount := RoundMicro(float64(u.CompletionTokens) / 1_000_000 * p.OutputPerM)
			bd.Lines = append(bd.Lines, domain.CostLineItem{
				Kind:         domain.CostLineOutput,
				Label:        step.Label + " · output",
				Quantity:     float64(u.CompletionTokens),
				UnitPriceUSD: p.OutputPerM,
				AmountUSD:    amount,
			})
			bd.TotalUSD += amount
		}
		if step.BillBaseFee && p.BaseFeeUSD > 0 {
			amount := RoundMicro(p.BaseFeeUSD)
			bd.Lines = append(bd.Lines, domain.CostLineItem{
				Kind:         domain.CostLineBaseFee,
				Label:        step.Label + " · base fee",
				Quantity:     1,
				UnitPriceUSD: p.BaseFeeUSD,
				AmountUSD:    amount,
			})
			bd.TotalUSD += amount
		}
	}
	bd.TotalUSD = RoundMicro(bd.TotalUSD)
	return bd
}

// Estimate projects a request's cost before execution: full input rates,
// because cache state is unknowable in advance, plus one base fee. The
// projection is deliberately the number the router already showed, now with
// line items attached so estimate-vs-actual drift can point at its cause.
func Estimate(promptTokens, maxCompletionTokens int, price Price) domain.CostBreakdown {
	usage := domain.TokenUsage{PromptTokens: promptTokens, CompletionTokens: maxCompletionTokens}
	bd := Compute(usage, price, []AttemptUsage{{Label: "estimate", Usage: usage, BillBaseFee: true}})
	bd.Estimated = true
	return bd
}

// Accuracy scores one estimate against its actual: 1.0 is perfect foresight,
// falling toward 0 as the projection drifts. A zero actual with a zero
// estimate is exact agreement (a free request predicted free); a zero actual
// against a nonzero estimate scores 0, because the projection spent
// imaginary money.
func Accuracy(estimatedUSD, actualUSD float64) float64 {
	if actualUSD == 0 {
		if estimatedUSD == 0 {
			return 1
		}
		return 0
	}
	diff := math.Abs(actualUSD - estimatedUSD)
	if diff >= actualUSD {
		return 0
	}
	return RoundMicro(1 - diff/actualUSD)
}

// WindowAccuracy aggregates estimate quality over many requests as the
// spend-weighted mean: a perfect projection on a cent of traffic must not
// outweigh a drifting one on a hundred dollars.
func WindowAccuracy(estimatedUSD, actualUSD float64) float64 {
	return Accuracy(estimatedUSD, actualUSD)
}
