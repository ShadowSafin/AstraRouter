package routing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// applyEndpointOverride folds an admin-managed endpoint scope into a copy of
// the resolved policy.
//
// Endpoints are per-request constraints, not stored policies: the override
// narrows this request's targets, strategy and budgets while the stored policy
// stays untouched for every other scope. The input policy is never mutated; a
// copy carries the changes so a shared or cached policy row cannot leak one
// scope's constraints into another request.
func applyEndpointOverride(rc *domain.RequestContext, index *StaticCatalogue, policy *domain.RoutingPolicy) (*domain.RoutingPolicy, error) {
	if rc == nil || rc.EndpointOverride == nil {
		return policy, nil
	}
	override := rc.EndpointOverride

	out := *policy
	out.Targets = append([]domain.RouteTarget(nil), policy.Targets...)

	// A forced model replaces the target list wholesale: the scope pins one
	// upstream model, and anything else serving would violate the pin.
	if model := strings.TrimSpace(override.ForceModel); model != "" {
		targets := synthesizeTargetsFor(index, model)
		if len(targets) == 0 {
			return nil, domain.NewError(domain.ErrCodeNotFound,
				fmt.Sprintf("no provider serves model %q for endpoint scope %q", model, rc.EndpointID))
		}
		out.Targets = targets
	}

	// Allow and deny lists narrow whatever the target list currently holds,
	// whether it came from the policy or from a forced model above.
	constrained := len(override.PreferredModels) > 0 || len(override.PreferredProviders) > 0
	if constrained {
		out.Targets = filterTargets(out.Targets, override)
		if len(out.Targets) == 0 {
			// The policy's declared targets did not overlap the scope. Falling
			// back to every provider that serves the requested model is what makes
			// a scope like "send this app's traffic to my own account" possible at
			// all: a policy usually names the hosted providers, and a scope that
			// could only ever subtract from them could never introduce one.
			out.Targets = filterTargets(synthesizeTargetsFor(index, rc.RequestedModel), override)
		}
		if len(out.Targets) == 0 {
			return nil, domain.Errorf(domain.ErrCodeUnavailable,
				"no provider is eligible for endpoint scope %q: model %q is not served by %s",
				rc.EndpointID, rc.RequestedModel, allowedSummary(override))
		}
	}

	if override.Strategy != "" && override.Strategy.Valid() {
		out.Strategy = override.Strategy
	}
	if override.MaxCostUSD > 0 {
		if out.Limits.MaxCostPerRequestUSD <= 0 || override.MaxCostUSD < out.Limits.MaxCostPerRequestUSD {
			out.Limits.MaxCostPerRequestUSD = override.MaxCostUSD
		}
	}
	if override.LatencyTargetMS > 0 {
		if rc.LatencyTargetMS <= 0 || override.LatencyTargetMS < rc.LatencyTargetMS {
			rc.LatencyTargetMS = override.LatencyTargetMS
		}
	}
	if override.BlockFallback {
		out.Fallback.Enabled = false
	}
	return &out, nil
}

// synthesizeTargetsFor builds an ordered target list for one model or alias,
// mirroring Engine.synthesizeTargets without needing an engine instance.
func synthesizeTargetsFor(index *StaticCatalogue, model string) []domain.RouteTarget {
	if index == nil {
		return nil
	}
	matches := index.FindModels(model)
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
	// Same ordering as the engine's synthesized targets: provider priority,
	// then model quality, so scope routing agrees with default routing.
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
			Alias:        model,
			Priority:     i + 1,
			Weight:       entry.provider.EffectiveWeight(),
		})
	}
	return out
}

// filterTargets keeps the targets a scope allows: a target survives when it
// matches at least one allowed model AND at least one allowed provider. An
// empty allow list on either axis means "unconstrained on that axis".
func filterTargets(targets []domain.RouteTarget, override *domain.EndpointRoutingOverride) []domain.RouteTarget {
	out := make([]domain.RouteTarget, 0, len(targets))
	for _, t := range targets {
		if len(override.PreferredModels) > 0 &&
			!domain.MatchAnyModelPattern(override.PreferredModels, t.Model) &&
			(t.Alias == "" || !domain.MatchAnyModelPattern(override.PreferredModels, t.Alias)) {
			continue
		}
		if len(override.PreferredProviders) > 0 &&
			!domain.MatchAnyModelPattern(override.PreferredProviders, t.ProviderID) &&
			!domain.MatchAnyModelPattern(override.PreferredProviders, t.ProviderName) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// allowedSummary renders the scope's allow lists for an error message, so an
// operator sees what the scope permits instead of guessing why nothing matched.
func allowedSummary(override *domain.EndpointRoutingOverride) string {
	var parts []string
	if len(override.PreferredModels) > 0 {
		parts = append(parts, fmt.Sprintf("models %v", override.PreferredModels))
	}
	if len(override.PreferredProviders) > 0 {
		parts = append(parts, fmt.Sprintf("providers %v", override.PreferredProviders))
	}
	if len(parts) == 0 {
		return "the scope's allow list"
	}
	return strings.Join(parts, " and ")
}
