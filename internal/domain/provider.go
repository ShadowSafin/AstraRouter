package domain

import "time"

// ProviderKind identifies which adapter implementation serves a provider.
//
// The kind is data, not behaviour: routing selects among providers using only
// this enum and the capability set, so registering a new provider never
// requires a change to the routing engine.
type ProviderKind string

const (
	// ProviderOpenAI is the hosted OpenAI API.
	ProviderOpenAI ProviderKind = "openai"
	// ProviderAnthropic is the hosted Anthropic Messages API.
	ProviderAnthropic ProviderKind = "anthropic"
	// ProviderOllama is a local Ollama daemon.
	ProviderOllama ProviderKind = "ollama"
	// ProviderVLLM is a vLLM OpenAI-compatible server.
	ProviderVLLM ProviderKind = "vllm"
	// ProviderOpenAICompatible covers any third-party server that speaks the
	// OpenAI wire format (llama.cpp, LM Studio, Together, Groq gateways, ...).
	ProviderOpenAICompatible ProviderKind = "openai_compatible"
)

// Valid reports whether the kind has a registered adapter.
func (k ProviderKind) Valid() bool {
	switch k {
	case ProviderOpenAI, ProviderAnthropic, ProviderOllama, ProviderVLLM, ProviderOpenAICompatible:
		return true
	default:
		return false
	}
}

// Local reports whether the kind is typically a self-hosted runtime. Local
// providers generally have no marginal per-token cost and much lower latency
// variance, which routing uses when choosing between candidates.
func (k ProviderKind) Local() bool {
	return k == ProviderOllama || k == ProviderVLLM
}

// AuthStyle describes how credentials are attached to upstream requests.
type AuthStyle string

const (
	// AuthBearer sends "Authorization: Bearer <token>".
	AuthBearer AuthStyle = "bearer"
	// AuthHeader sends a custom header, configured via HeaderName.
	AuthHeader AuthStyle = "header"
	// AuthNone sends no credentials, correct for unauthenticated local servers.
	AuthNone AuthStyle = "none"
)

// DefaultAuthStyle returns the credential placement a provider kind expects when
// the operator did not choose one.
//
// The mapping is a property of the provider protocol, not of a deployment, so it
// lives here rather than in the transport or persistence layer. It has to live in
// exactly one place: persistence records a concrete style, and transport applies
// it. If the two disagreed, a provider configured without an explicit style would
// be stored as bearer and then sent a bearer token that Anthropic rejects.
func DefaultAuthStyle(kind ProviderKind) AuthStyle {
	switch kind {
	case ProviderAnthropic:
		// The Messages API authenticates with a custom header, never a bearer
		// token.
		return AuthHeader
	case ProviderOllama:
		// A local daemon accepts no credential; sending a bogus Authorization
		// header is worse than sending none.
		return AuthNone
	default:
		return AuthBearer
	}
}

// DefaultAuthHeader returns the header name used for AuthHeader credentials on a
// provider kind, or an empty string when the kind has no convention.
func DefaultAuthHeader(kind ProviderKind) string {
	if kind == ProviderAnthropic {
		return "x-api-key"
	}
	return ""
}

// Provider is a configured upstream inference endpoint.
type Provider struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Kind ProviderKind `json:"kind"`
	// BaseURL is the root used to build request URLs, e.g.
	// "https://api.openai.com/v1". Adapters append their own paths.
	BaseURL string `json:"base_url"`
	// APIKeyEnv names the environment variable holding the credential. The
	// secret itself is never stored in the database or returned over the API.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// APIKey may be set directly (for local, unauthenticated runtimes) but is
	// redacted on read.
	APIKeyInline string `json:"-"`
	// AuthStyle and HeaderName control credential placement.
	AuthStyle  AuthStyle `json:"auth_style"`
	HeaderName string    `json:"header_name,omitempty"`
	// Headers are extra static headers, e.g. an Anthropic version pin.
	Headers map[string]string `json:"headers,omitempty"`
	// Organization and Project are OpenAI-specific routing identifiers.
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
	// Capabilities is the provider-wide capability set. Individual models may
	// narrow it further.
	Capabilities []Capability `json:"capabilities"`
	// Status gates the provider. StatusDegraded keeps it eligible but deprioritized.
	Status Status `json:"status"`
	// Weight biases weighted routing strategies; zero is treated as one.
	Weight int `json:"weight,omitempty"`
	// TimeoutMS overrides the policy timeout for this provider.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// MaxConcurrency caps simultaneous in-flight attempts.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
	// Priority orders providers when a policy does not specify an order. Lower
	// values are preferred.
	Priority int `json:"priority,omitempty"`
	// Region is informational metadata used in dashboards.
	Region string `json:"region,omitempty"`
	// Notes is free-form operator documentation shown in the dashboard.
	Notes string `json:"notes,omitempty"`
	// Environment marks where this provider is intended to serve.
	Environment Environment `json:"environment,omitempty"`
	// ManagedBy records whether the row is owned by the config bootstrapper
	// or by an operator working through the admin API.
	ManagedBy ManagedBy `json:"managed_by,omitempty"`
	// Labels carry operator metadata.
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// CapabilitySet returns the provider capabilities as a set.
func (p *Provider) CapabilitySet() CapabilitySet {
	return NewCapabilitySet(p.Capabilities...)
}

// EffectiveWeight returns the routing weight, defaulting to 1.
func (p *Provider) EffectiveWeight() int {
	if p.Weight <= 0 {
		return 1
	}
	return p.Weight
}

// Credential returns the inline credential, which the configuration layer
// resolves from APIKeyEnv at load time.
func (p *Provider) Credential() string { return p.APIKeyInline }

// HealthState is the coarse health verdict for a provider.
type HealthState string

const (
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
	HealthUnknown   HealthState = "unknown"
)

// Eligible reports whether the state permits serving traffic.
func (h HealthState) Eligible() bool {
	return h == HealthHealthy || h == HealthDegraded
}

// ProviderHealth is a point-in-time health assessment produced by the active
// probe loop and by passive observation of live traffic. The routing engine
// reads the most recent assessment; the dashboard renders its history.
type ProviderHealth struct {
	ProviderID   string      `json:"provider_id"`
	ProviderName string      `json:"provider_name,omitempty"`
	State        HealthState `json:"state"`
	// CheckedAt is when the assessment was produced.
	CheckedAt time.Time `json:"checked_at"`
	// LatencyMS is the observed probe or request latency.
	LatencyMS int64 `json:"latency_ms"`
	// SuccessRate is the rolling success ratio in [0,1] over the window.
	SuccessRate float64 `json:"success_rate"`
	// ConsecutiveFailures drives the circuit breaker's open/closed decision.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// ErrorRate is the rolling failure ratio in [0,1] over the window.
	ErrorRate float64 `json:"error_rate"`
	// Message carries a human-readable explanation, typically the last error.
	Message string `json:"message,omitempty"`
	// Source distinguishes an active probe from passive traffic observation.
	Source string `json:"source,omitempty"`
	// ProbeLatencyMS is the latency of the synthetic probe, when performed.
	ProbeLatencyMS int64 `json:"probe_latency_ms,omitempty"`
}

// DegradeThreshold is the consecutive-failure count at which a provider is
// considered unhealthy by default. It is a package constant rather than a
// per-provider setting because the heuristic must be uniform across the fleet
// for the circuit breaker to behave predictably.
const DegradeThreshold = 3

// EvaluateState derives a health state from observed failures.
//
// The thresholds are intentionally asymmetric: a provider needs three
// consecutive failures to be marked degraded but nine to be taken out of
// service entirely. Being too eager to mark a provider unhealthy causes
// cascading load on its peers, which is a worse failure mode than occasionally
// routing to a struggling provider.
func EvaluateState(consecutiveFailures int, errorRate, successRate float64) HealthState {
	switch {
	case consecutiveFailures >= DegradeThreshold*3:
		return HealthUnhealthy
	case consecutiveFailures >= DegradeThreshold:
		return HealthDegraded
	case errorRate > 0.5:
		return HealthDegraded
	case successRate > 0 && successRate < 0.5:
		return HealthDegraded
	default:
		return HealthHealthy
	}
}
