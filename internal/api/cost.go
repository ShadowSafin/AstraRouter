package api

import (
	"context"
	"fmt"
	"time"

	"github.com/shadowsafin/synapass/internal/cost"
	"github.com/shadowsafin/synapass/internal/domain"
)

// priceCacheTTL bounds how long a resolved price sheet is trusted on the
// request path. Sheets change on human timescales; a minute of staleness is
// invisible on an invoice and keeps versioned pricing off the hot path.
const priceCacheTTL = time.Minute

// resolvePrice determines the exact sheet for one request: the effective
// versioned price for the serving tenant/provider/model, or the registry
// fallback from the routing target when no sheet applies.
//
// Name-to-ID mapping (provider/model rows) happens only on cache miss and is
// itself cached by the same key, so the steady state is a map lookup. Every
// error degrades to registry pricing rather than failing the request: a
// price lookup must never break inference.
func (s *Server) resolvePrice(ctx context.Context, tenantID, providerName, modelName string, target domain.RouteTarget) cost.Price {
	fallback := cost.PriceFromTarget(target)
	if s == nil || s.repos == nil || s.repos.Pricing == nil {
		return fallback
	}
	if s.priceCache == nil {
		return fallback
	}

	now := time.Now()
	key := cost.PriceKey(tenantID, modelName, providerName, now)
	if cached := s.priceCache.Get(key, now); cached != nil {
		if v := cost.Resolve(cached, now); v != nil {
			return cost.PriceForVersion(v)
		}
		return fallback
	}

	// Cache miss: map names to registry row ids, then load candidate sheets.
	// Each step tolerates failure independently — a provider that cannot be
	// resolved simply contributes no scoped sheets.
	providerID, modelID := "", ""
	if providerName != "" {
		if p, err := s.repos.Providers.GetByName(ctx, providerName); err == nil && p != nil {
			providerID = p.ID
		}
		if providerID == "" {
			providerID = target.ProviderID
		}
		if providerID == "" {
			providerID = providerName
		}
	}
	if providerID != "" && modelName != "" && s.repos.Models != nil {
		if models, err := s.repos.Models.ListByProvider(ctx, providerID); err == nil {
			for _, m := range models {
				if m.Name == modelName {
					modelID = m.ID
					break
				}
			}
		}
	}

	versions, err := s.repos.Pricing.EffectiveFor(ctx, tenantID, modelID, providerID)
	if err != nil {
		s.logger.Debug("pricing lookup failed, using registry rates",
			"provider", providerName, "model", modelName, "error", err)
		return fallback
	}
	s.priceCache.Set(key, versions, now)
	if v := cost.Resolve(versions, now); v != nil {
		return cost.PriceForVersion(v)
	}
	return fallback
}

// buildCostBreakdown produces the exact account for a finished request. Each
// trace attempt becomes its own billed step at that attempt's own target
// rates, so retries and fallbacks accumulate exactly what the providers
// metered. When the serving pair resolved to a versioned sheet, steps on
// that same provider/model bill at the versioned rates; other steps keep
// their registry rates, because a tenant's negotiated sheet prices their
// traffic, not another provider's failed attempt.
func (s *Server) buildCostBreakdown(
	ctx context.Context,
	rc *domain.RequestContext,
	decision *domain.RouteDecision,
	provider, model string,
	usage domain.TokenUsage,
	trace *domain.RequestTrace,
	requestErr error,
) (exact domain.Cost, estimate domain.Cost, bd domain.CostBreakdown) {
	if decision != nil {
		estimate = decision.EstimatedCost
	}
	if rc != nil && estimate.IsZero() && rc.PromptTokens > 0 {
		// No routing estimate survived (a pre-routing failure, say): project
		// from the prompt so estimate-quality tracking still has a number.
		estimate = domain.Cost{USD: cost.Estimate(rc.PromptTokens, 0,
			cost.PriceFromTarget(targetOf(decision))).TotalUSD}
	}

	billed := requestErr == nil && (traceAttempts(trace) > 0 ||
		usage.PromptTokens > 0 || usage.CompletionTokens > 0)
	if !billed {
		// Failed or rejected before any billable work: the books show zero
		// while the estimate stands, which is exactly the drift signal.
		return domain.Cost{}, estimate, domain.CostBreakdown{Currency: "USD"}
	}

	tenantID := ""
	if rc != nil {
		tenantID = rc.TenantID()
	}
	serving := s.resolvePrice(ctx, tenantID, provider, model, targetOf(decision))

	if estimate.IsZero() && serving.PricingVersionID != "" &&
		(usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		// Routing priced blind: the registry carries no rates, so the decision
		// projected zero, but a versioned sheet billed the request. The honest
		// routing-time projection under full information is the sheet estimate
		// — full input rates, because cache state is unknowable in advance —
		// and recording it keeps estimate accuracy measuring sheet quality
		// rather than flatlining at zero wherever sheets are in use.
		estimate = domain.Cost{USD: cost.Estimate(
			usage.PromptTokens, usage.CompletionTokens, serving).TotalUSD}
	}

	steps := attemptSteps(trace, provider, model, serving)
	if len(steps) == 0 {
		steps = []cost.AttemptUsage{{
			Label: "request", Usage: usage, BillBaseFee: traceAttempts(trace) > 0,
		}}
	}
	bd = cost.Compute(usage, serving, steps)
	return domain.Cost{USD: bd.TotalUSD}, estimate, bd
}

// attemptSteps converts trace attempts into billable steps, each priced at
// its own attempt target with the versioned sheet substituted where the
// attempt served the same provider/model the sheet was resolved for.
func attemptSteps(trace *domain.RequestTrace, provider, model string, serving cost.Price) []cost.AttemptUsage {
	if trace == nil {
		return nil
	}
	servingVersioned := serving.PricingVersionID != ""
	var steps []cost.AttemptUsage
	for i, a := range trace.Attempts {
		n := a.Number
		if n <= 0 {
			n = i + 1
		}
		name := a.Target.ProviderName
		if name == "" {
			name = a.Target.ProviderID
		}
		if name == "" {
			name = provider
		}
		m := a.Target.Model
		if m == "" {
			m = model
		}
		step := cost.AttemptUsage{
			Label:       fmt.Sprintf("attempt %d", n),
			Usage:       a.Usage,
			BillBaseFee: true,
		}
		if servingVersioned && name == provider && m == model {
			p := serving
			step.Price = &p
		} else {
			p := cost.PriceFromTarget(a.Target)
			step.Price = &p
		}
		steps = append(steps, step)
	}
	return steps
}

// targetOf extracts the chosen routing target, tolerating a nil decision.
func targetOf(decision *domain.RouteDecision) domain.RouteTarget {
	if decision == nil {
		return domain.RouteTarget{}
	}
	return decision.Chosen
}

// traceAttempts counts trace attempts tolerating a nil trace.
func traceAttempts(trace *domain.RequestTrace) int {
	if trace == nil {
		return 0
	}
	return len(trace.Attempts)
}
