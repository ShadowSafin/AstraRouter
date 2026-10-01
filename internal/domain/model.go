package domain

import "time"

// ModelStatus is the lifecycle state of a registry entry.
type ModelStatus string

const (
	ModelActive     ModelStatus = "active"
	ModelDegraded   ModelStatus = "degraded"
	ModelDeprecated ModelStatus = "deprecated"
	ModelDisabled   ModelStatus = "disabled"
)

// Usable reports whether the model may receive traffic. Deprecated models keep
// serving existing traffic but are excluded from default routing.
func (m ModelStatus) Usable() bool {
	return m == ModelActive || m == ModelDegraded
}

// Model is a single servable model on a single provider.
//
// The registry is intentionally one-row-per-provider-model rather than one row
// per logical model: routing needs to know that "gpt-4o" on the primary OpenAI
// account and "gpt-4o" on a regional gateway have different base URLs, keys and
// observed latencies, and treating them as separate rows keeps the fallback
// chain honest.
type Model struct {
	ID           string `json:"id"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	// Name is the identifier the upstream provider expects on the wire.
	Name string `json:"name"`
	// Aliases are client-facing names that resolve to this model, e.g. "fast".
	// Several models may share an alias, in which case routing picks between
	// them; that is how graceful model migrations are expressed.
	Aliases []string `json:"aliases,omitempty"`
	// DisplayName is for human consumption in the dashboard.
	DisplayName string `json:"display_name,omitempty"`
	// Version is the upstream model revision, when the provider exposes one.
	Version string `json:"version,omitempty"`
	// ContextWindow is the maximum total tokens accepted.
	ContextWindow int `json:"context_window"`
	// MaxOutputTokens is the maximum completion tokens the model will emit.
	MaxOutputTokens int `json:"max_output_tokens"`
	// Capabilities narrows the provider capability set for this model.
	Capabilities []Capability `json:"capabilities"`
	// Pricing is quoted in USD per one million tokens, matching the unit used
	// on provider price sheets so operators can copy values directly.
	InputCostPerMillion       float64 `json:"input_cost_per_million"`
	OutputCostPerMillion      float64 `json:"output_cost_per_million"`
	CachedInputCostPerMillion float64 `json:"cached_input_cost_per_million,omitempty"`
	// Status gates eligibility.
	Status ModelStatus `json:"status"`
	// QualityTier is an operator-assigned 1..5 signal used by the quality-aware
	// routing strategy. Phase 1 uses it only for tie-breaking.
	QualityTier int `json:"quality_tier,omitempty"`
	// RateLimitRPM and RateLimitTPM mirror upstream quotas for planning.
	RateLimitRPM int `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM int `json:"rate_limit_tpm,omitempty"`
	// DeprecatedAt records when the model was retired upstream.
	DeprecatedAt *time.Time `json:"deprecated_at,omitempty"`
	// Priority orders models of the same provider when a policy does not
	// specify an order. Lower values are preferred, mirroring providers.
	Priority int `json:"priority,omitempty"`
	// Environment marks where this model is intended to serve.
	Environment Environment `json:"environment,omitempty"`
	// ManagedBy records whether the row is owned by the config bootstrapper
	// or by an operator working through the admin API.
	ManagedBy ManagedBy `json:"managed_by,omitempty"`
	// Metadata carries provider-specific extras for dashboards.
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// CapabilitySet returns the model capability set.
func (m *Model) CapabilitySet() CapabilitySet {
	return NewCapabilitySet(m.Capabilities...)
}

// Supports reports whether the model advertises every capability in want.
func (m *Model) Supports(want ...Capability) bool {
	return m.CapabilitySet().Has(want...)
}

// Usable reports whether the model may serve traffic.
func (m *Model) Usable() bool { return m.Status.Usable() }

// EstimatedCost prices a request given token counts. Prompt tokens beyond the
// cached prefix are billed at the full input rate; this deliberately ignores
// caching because the gateway cannot know whether the provider served the
// request from cache until after the fact.
func (m *Model) EstimatedCost(promptTokens, completionTokens int) Cost {
	in := float64(promptTokens) / 1_000_000 * m.InputCostPerMillion
	out := float64(completionTokens) / 1_000_000 * m.OutputCostPerMillion
	return Cost{USD: in + out}
}

// ActualCost prices a completed request, honouring cached prompt tokens when
// the provider reports them.
func (m *Model) ActualCost(usage TokenUsage) Cost {
	cached := usage.CachedPromptTokens
	if cached > usage.PromptTokens {
		cached = usage.PromptTokens
	}
	fresh := usage.PromptTokens - cached
	cachedRate := m.CachedInputCostPerMillion
	if cachedRate == 0 {
		cachedRate = m.InputCostPerMillion
	}
	in := float64(fresh)/1_000_000*m.InputCostPerMillion + float64(cached)/1_000_000*cachedRate
	out := float64(usage.CompletionTokens) / 1_000_000 * m.OutputCostPerMillion
	return Cost{USD: in + out}
}

// FitsPrompt reports whether a prompt fits the model window, leaving room for
// the requested completion.
func (m *Model) FitsPrompt(promptTokens, maxOutputTokens int) bool {
	window := m.ContextWindow
	if window <= 0 {
		return true
	}
	if m.MaxOutputTokens > 0 && maxOutputTokens > m.MaxOutputTokens {
		maxOutputTokens = m.MaxOutputTokens
	}
	return promptTokens+maxOutputTokens <= window
}
