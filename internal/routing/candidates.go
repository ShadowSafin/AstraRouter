package routing

import (
	"fmt"
	"strings"

	"github.com/corerouter/corerouter/internal/domain"
)

// candidateBuilder turns a policy's target list into concrete, evaluated
// candidates.
//
// A target is a declaration ("prefer gpt-4o on the primary OpenAI account"); a
// candidate is a resolved fact ("gpt-4o on provider p-openai-1, costing
// $0.0021 for this prompt, currently healthy"). Separating the two means the same
// target list can produce a different candidate set as health and pricing change,
// and every exclusion carries a reason that survives into the audit trail.
type candidateBuilder struct {
	index        *StaticCatalogue
	health       HealthProvider
	availability Availability
	rc           *domain.RequestContext
	policy       *domain.RoutingPolicy
	ceiling      float64
	defaults     Defaults
	required     domain.CapabilitySet
}

// newCandidateBuilder constructs a builder for one request.
func newCandidateBuilder(
	index *StaticCatalogue,
	health HealthProvider,
	availability Availability,
	rc *domain.RequestContext,
	policy *domain.RoutingPolicy,
	ceiling float64,
	defaults Defaults,
) *candidateBuilder {
	if availability == nil {
		availability = NopAvailability{}
	}
	return &candidateBuilder{
		index:        index,
		health:       health,
		availability: availability,
		rc:           rc,
		policy:       policy,
		ceiling:      ceiling,
		defaults:     defaults,
		required:     rc.CapabilitySet(),
	}
}

// build evaluates every target and returns all resulting candidates, each marked
// eligible or ineligible with a reason. Ineligible candidates are retained rather
// than dropped so the decision record can answer "why not provider X?".
func (b *candidateBuilder) build(targets []domain.RouteTarget, cs constraintSet) []domain.Candidate {
	out := make([]domain.Candidate, 0, len(targets))

	for group, target := range targets {
		// The policy-level model allow and deny lists apply before any registry
		// lookup: a denied model must never appear as a candidate at all.
		if reason, denied := b.modelDenied(target.Model); denied {
			out = append(out, domain.Candidate{
				Target:          target,
				RejectionReason: reason,
				Group:           group,
			})
			continue
		}

		resolved := b.resolveTarget(target)
		if len(resolved) == 0 {
			out = append(out, domain.Candidate{
				Target:          target,
				RejectionReason: fmt.Sprintf("no registered model matches %q", target.Model),
				Group:           group,
			})
			continue
		}

		for _, pair := range resolved {
			candidate := b.evaluate(pair.provider, pair.model, target, cs)
			candidate.Group = group
			out = append(out, candidate)
		}
	}
	return out
}

// resolvedTarget is a provider/model pair a target expanded to.
type resolvedTarget struct {
	provider domain.Provider
	model    domain.Model
}

// resolveTarget expands a target declaration into concrete provider/model pairs.
//
// Two shapes are supported:
//
//   - provider plus model  -> exactly the named model on the named provider
//   - model alone          -> every provider serving that model or alias
//
// Allowing the second shape is what makes multi-provider failover expressible
// without enumerating every provider by hand, which is the common case.
func (b *candidateBuilder) resolveTarget(target domain.RouteTarget) []resolvedTarget {
	providerKey := target.ProviderID
	if providerKey == "" {
		providerKey = target.ProviderName
	}

	if providerKey != "" {
		provider, ok := b.index.Provider(providerKey)
		if !ok {
			return nil
		}
		model, ok := b.findModelOnProvider(provider.ID, target.Model)
		if !ok {
			return nil
		}
		return []resolvedTarget{{provider: provider, model: model}}
	}

	matches := b.index.FindModels(target.Model)
	out := make([]resolvedTarget, 0, len(matches))
	for _, m := range matches {
		provider, ok := b.index.ProviderByID(m.ProviderID)
		if !ok {
			continue
		}
		out = append(out, resolvedTarget{provider: provider, model: m})
	}
	return out
}

// findModelOnProvider locates a model by name or alias on a specific provider.
func (b *candidateBuilder) findModelOnProvider(providerID, nameOrAlias string) (domain.Model, bool) {
	key := strings.ToLower(strings.TrimSpace(nameOrAlias))
	if key == "" {
		return domain.Model{}, false
	}
	models := b.index.ModelsByProvider(providerID)
	// Exact matches are preferred over glob matches so a policy naming a model
	// explicitly is never satisfied by a different one that happens to match a
	// pattern.
	for _, m := range models {
		if strings.EqualFold(m.Name, key) {
			return m, true
		}
		for _, alias := range m.Aliases {
			if strings.EqualFold(alias, key) {
				return m, true
			}
		}
	}
	for _, m := range models {
		if domain.MatchModelPattern(key, strings.ToLower(m.Name)) {
			return m, true
		}
	}
	return domain.Model{}, false
}

// modelDenied applies the policy's allow and deny lists.
func (b *candidateBuilder) modelDenied(model string) (string, bool) {
	limits := b.policy.Limits
	if len(limits.DeniedModels) > 0 && domain.MatchAnyModelPattern(limits.DeniedModels, model) {
		return fmt.Sprintf("model %q is denied by policy", model), true
	}
	if len(limits.AllowedModels) > 0 && !domain.MatchAnyModelPattern(limits.AllowedModels, model) {
		return fmt.Sprintf("model %q is not in the policy allow list", model), true
	}
	// The requested alias must also be permitted, so a client cannot reach a
	// denied model by requesting an alias that resolves to it.
	if b.rc.RequestedModel != "" && len(limits.DeniedModels) > 0 &&
		domain.MatchAnyModelPattern(limits.DeniedModels, b.rc.RequestedModel) {
		return fmt.Sprintf("requested model %q is denied by policy", b.rc.RequestedModel), true
	}
	return "", false
}

// evaluate applies every eligibility rule to one provider/model pair.
//
// The order of the checks is deliberate: cheap structural checks run before the
// expensive capability intersection, and the reasons are phrased in terms of the
// operator's configuration rather than internal identifiers.
func (b *candidateBuilder) evaluate(provider domain.Provider, model domain.Model, target domain.RouteTarget, cs constraintSet) domain.Candidate {
	candidate := domain.Candidate{
		Target: domain.RouteTarget{
			ProviderID:           provider.ID,
			ProviderName:         provider.Name,
			Model:                model.Name,
			Alias:                firstNonEmptyString(target.Alias, b.rc.RequestedModel),
			Weight:               effectiveTargetWeight(target, provider),
			Priority:             target.Priority,
			MaxOutputTokens:      target.MaxOutputTokens,
			InputCostPerMillion:  model.InputCostPerMillion,
			OutputCostPerMillion: model.OutputCostPerMillion,
			Capabilities:         model.Capabilities,
			ContextWindow:        model.ContextWindow,
			QualityTier:          model.QualityTier,
			Kind:                 provider.Kind,
		},
		Eligible: true,
	}

	// --- structural checks ---
	if !provider.Status.IsUsable() {
		return reject(candidate, fmt.Sprintf("provider %s is %s", provider.Name, provider.Status))
	}
	if !b.availability.Available(provider.ID, provider.Name) {
		return reject(candidate, fmt.Sprintf("provider %s has no working adapter", provider.Name))
	}
	if !model.Usable() && cs.excludeDeprecated {
		return reject(candidate, fmt.Sprintf("model %s on %s is %s", model.Name, provider.Name, model.Status))
	}

	// --- Phase 2: provider allow/deny lists ---
	if len(b.policy.Limits.DeniedProviders) > 0 {
		for _, pat := range b.policy.Limits.DeniedProviders {
			if domain.MatchModelPattern(pat, provider.Name) || domain.MatchModelPattern(pat, provider.ID) {
				return reject(candidate, fmt.Sprintf("provider %s is denied by policy", provider.Name))
			}
		}
	}
	if len(b.policy.Limits.AllowedProviders) > 0 {
		allowed := false
		for _, pat := range b.policy.Limits.AllowedProviders {
			if domain.MatchModelPattern(pat, provider.Name) || domain.MatchModelPattern(pat, provider.ID) {
				allowed = true
				break
			}
		}
		if !allowed {
			return reject(candidate, fmt.Sprintf("provider %s is not in the policy allow list", provider.Name))
		}
	}

	// --- Phase 2: region constraints ---
	if len(b.policy.Limits.AllowedRegions) > 0 {
		if provider.Region == "" || !matchRegion(b.policy.Limits.AllowedRegions, provider.Region) {
			return reject(candidate, fmt.Sprintf("provider %s region %q is not permitted", provider.Name, provider.Region))
		}
	}
	if len(b.policy.Limits.DeniedRegions) > 0 {
		if provider.Region != "" && matchRegion(b.policy.Limits.DeniedRegions, provider.Region) {
			return reject(candidate, fmt.Sprintf("provider %s region %q is denied", provider.Name, provider.Region))
		}
	}
	// Request-level region pin.
	if b.rc.Region != "" && provider.Region != "" && !strings.EqualFold(b.rc.Region, provider.Region) {
		// Only enforce when the policy declares regions; otherwise treat as soft.
		if len(b.policy.Limits.AllowedRegions) > 0 || len(b.policy.Match.Regions) > 0 {
			return reject(candidate, fmt.Sprintf("provider %s region %q does not match requested %q", provider.Name, provider.Region, b.rc.Region))
		}
		candidate.Notes = append(candidate.Notes, fmt.Sprintf("region %q differs from requested %q", provider.Region, b.rc.Region))
	}

	// --- Phase 2: max latency hard ceiling ---
	if b.policy.Limits.MaxLatencyMS > 0 && b.health != nil {
		if h, known := b.health.Health(provider.ID); known && h.LatencyMS > int64(b.policy.Limits.MaxLatencyMS) {
			return reject(candidate, fmt.Sprintf("provider %s latency %dms exceeds the %dms maximum", provider.Name, h.LatencyMS, b.policy.Limits.MaxLatencyMS))
		}
	}

	// --- capability intersection ---
	// The provider advertises a runtime capability set; the model may narrow it.
	// A request is servable only when both agree, so a model registered without
	// the tools capability is never selected for a tool-calling request even on a
	// provider that supports tools generally.
	providerCaps := domain.NewCapabilitySet(provider.Capabilities...)
	if len(provider.Capabilities) == 0 {
		providerCaps = providerCapabilityDefault(provider.Kind)
	}
	modelCaps := model.CapabilitySet()
	for capability := range b.required {
		if !providerCaps.Contains(capability) {
			return reject(candidate, fmt.Sprintf("provider %s does not advertise the %q capability", provider.Name, capability))
		}
		if len(model.Capabilities) > 0 && !modelCaps.Contains(capability) {
			return reject(candidate, fmt.Sprintf("model %s does not advertise the %q capability", model.Name, capability))
		}
	}

	// --- context window ---
	completion := b.completionTokensFor(target)
	if model.ContextWindow > 0 && b.rc.PromptTokens+completion > model.ContextWindow {
		return reject(candidate, fmt.Sprintf("prompt plus completion (%d tokens) exceeds the %d token window of %s",
			b.rc.PromptTokens+completion, model.ContextWindow, model.Name))
	}

	// --- cost ceiling ---
	estimated := candidate.Target.EstimatedCost(b.rc.PromptTokens, completion).USD
	candidate.EstimatedCostUSD = estimated
	if cs.enforceCostCeiling && b.ceiling > 0 && estimated > b.ceiling {
		return reject(candidate, fmt.Sprintf("projected cost $%s exceeds the $%s ceiling",
			formatFloat(estimated), formatFloat(b.ceiling)))
	}

	// --- health ---
	if b.health != nil {
		health, known := b.health.Health(provider.ID)
		if known {
			candidate.Healthy = health.State.Eligible()
			candidate.Target.Health = health.State
			candidate.Target.ObservedLatencyMS = health.LatencyMS
			if cs.requireHealthy && !health.State.Eligible() {
				return reject(candidate, fmt.Sprintf("provider %s is %s", provider.Name, health.State))
			}
		} else {
			// No observations yet. Treating an unmeasured provider as healthy is
			// what allows a cold start; the circuit breaker will remove it
			// quickly if it turns out to be broken.
			candidate.Healthy = true
		}
	} else {
		candidate.Healthy = true
	}
	if b.health != nil {
		if breaker, ok := b.health.(interface{ CircuitOpen(string) bool }); ok && breaker.CircuitOpen(provider.ID) {
			candidate.Healthy = false
			if cs.requireHealthy {
				return reject(candidate, fmt.Sprintf("provider %s circuit breaker is open", provider.Name))
			}
			candidate.Notes = append(candidate.Notes, "circuit breaker open")
		}
	}

	// --- soft observations (do not affect eligibility) ---
	if b.rc.LatencyTargetMS > 0 && candidate.Target.ObservedLatencyMS > int64(b.rc.LatencyTargetMS) {
		candidate.Notes = append(candidate.Notes,
			fmt.Sprintf("observed latency %dms exceeds the %dms target", candidate.Target.ObservedLatencyMS, b.rc.LatencyTargetMS))
	}
	if model.Status == domain.ModelDeprecated {
		candidate.Notes = append(candidate.Notes, "model is deprecated")
	}
	if provider.Status == domain.StatusDegraded {
		candidate.Notes = append(candidate.Notes, "provider is degraded")
	}

	return candidate
}

// completionTokensFor returns the completion allowance applied to a target,
// honouring a per-target override so a cheaper model can be used with a shorter
// allowance rather than being skipped entirely.
func (b *candidateBuilder) completionTokensFor(target domain.RouteTarget) int {
	completion := b.rc.MaxOutputTokens
	if completion <= 0 {
		completion = b.defaults.Limits.MaxOutputTokens
	}
	if target.MaxOutputTokens > 0 && target.MaxOutputTokens < completion {
		completion = target.MaxOutputTokens
	}
	if completion < 0 {
		completion = 0
	}
	return completion
}

// reject marks a candidate ineligible with a reason.
func reject(candidate domain.Candidate, reason string) domain.Candidate {
	candidate.Eligible = false
	candidate.RejectionReason = reason
	return candidate
}

// effectiveTargetWeight resolves the weight used for weighted routing: an
// explicit target weight wins, otherwise the provider's own weight applies.
func effectiveTargetWeight(target domain.RouteTarget, provider domain.Provider) int {
	if target.Weight > 0 {
		return target.Weight
	}
	return provider.EffectiveWeight()
}

// providerCapabilityDefault mirrors the adapter default capability sets.
//
// It is duplicated from the providers package on purpose: routing must not import
// providers, because doing so would make the routing engine depend on HTTP client
// construction. The duplication is small, covers only the fallback case where an
// operator did not enumerate capabilities, and is asserted equal by a test.
func providerCapabilityDefault(kind domain.ProviderKind) domain.CapabilitySet {
	switch kind {
	case domain.ProviderOpenAI:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapTools, domain.CapParallelTool,
			domain.CapVision, domain.CapJSONMode, domain.CapJSONSchema, domain.CapEmbeddings,
			domain.CapLongContext, domain.CapReasoning, domain.CapSeed,
		)
	case domain.ProviderAnthropic:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapTools, domain.CapParallelTool,
			domain.CapVision, domain.CapJSONMode, domain.CapLongContext, domain.CapReasoning,
		)
	case domain.ProviderVLLM:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapJSONMode, domain.CapLongContext, domain.CapSeed,
		)
	case domain.ProviderOllama:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapLongContext, domain.CapSeed,
		)
	case domain.ProviderOpenAICompatible:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapJSONMode, domain.CapLongContext,
		)
	default:
		return domain.NewCapabilitySet(domain.CapChat, domain.CapStreaming)
	}
}

// firstNonEmptyString returns the first non-empty value.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// matchRegion reports whether region matches any pattern (exact or glob,
// case-insensitive).
func matchRegion(patterns []string, region string) bool {
	for _, pat := range patterns {
		if domain.MatchModelPattern(strings.ToLower(pat), strings.ToLower(region)) {
			return true
		}
		if strings.EqualFold(pat, region) {
			return true
		}
	}
	return false
}
