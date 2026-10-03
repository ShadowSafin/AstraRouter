package domain

import "time"

// UsageOutcome is the terminal state of a request, used for aggregation.
type UsageOutcome string

const (
	OutcomeSuccess  UsageOutcome = "success"
	OutcomeFallback UsageOutcome = "fallback"
	OutcomeError    UsageOutcome = "error"
	OutcomeRejected UsageOutcome = "rejected"
	OutcomeCanceled UsageOutcome = "canceled"
)

// UsageRecord is the durable accounting row for one served request. It is the
// system of record for billing and for the dashboard's cost views, which is why
// it duplicates a few fields that also appear in the trace: the trace store is
// sampled and high-volume, whereas usage must be complete.
type UsageRecord struct {
	ID        string    `json:"id"`
	RequestID RequestID `json:"request_id"`
	TraceID   string    `json:"trace_id,omitempty"`
	TenantID  string    `json:"tenant_id"`
	APIKeyID  string    `json:"api_key_id,omitempty"`
	// Provider and Model identify the attempt that produced the answer.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// RequestedModel is what the client asked for, preserved for route analysis.
	RequestedModel string      `json:"requested_model,omitempty"`
	PolicyID       string      `json:"policy_id,omitempty"`
	RequestType    RequestType `json:"request_type"`
	Usage          TokenUsage  `json:"usage"`
	Cost           Cost        `json:"cost"`
	// EstimateCost is the routing-time projection for this request. Comparing
	// it with Cost measures estimate quality; on cache hits Cost is zero while
	// EstimateCost records what serving it would have cost, which is how cache
	// savings are computed without a second query.
	EstimateCost Cost `json:"estimate_cost"`
	// PricingVersionID is the versioned sheet the final cost was computed
	// from, empty when the static registry price applied. Together with
	// Breakdown it makes every row re-auditable after price changes.
	PricingVersionID string `json:"pricing_version_id,omitempty"`
	// PricingSource names the winning resolution scope (tenant, model,
	// provider, global or registry), so "why this price" needs no archaeology.
	PricingSource string `json:"pricing_source,omitempty"`
	// Breakdown is the exact line-item account of Cost. Stored per row so a
	// price change tomorrow cannot rewrite what a request cost yesterday.
	Breakdown CostBreakdown `json:"breakdown,omitempty"`
	// CostBeforeUSD/CostAfterUSD bracket routing-time optimization: the
	// cheapest-eligible cost before shaping versus after. Their difference is
	// the routing saving, persisted here because the trace store is sampled
	// and savings must be complete.
	CostBeforeUSD float64 `json:"cost_before_usd,omitempty"`
	CostAfterUSD  float64 `json:"cost_after_usd,omitempty"`
	// EndpointID is the admin-managed endpoint scope, when the request arrived
	// under one, enabling spend-by-endpoint reporting.
	EndpointID string `json:"endpoint_id,omitempty"`
	// LatencyMS is the total server-side latency the client observed.
	LatencyMS int64 `json:"latency_ms"`
	// ProviderLatencyMS is the latency of the successful upstream attempt only.
	ProviderLatencyMS int64 `json:"provider_latency_ms"`
	// Attempts counts provider calls made, across all providers.
	Attempts int `json:"attempts"`
	// FallbackUsed is true when the first target did not serve the request.
	FallbackUsed bool `json:"fallback_used"`
	// CacheHit is reserved for the response cache; always false in phase 1 but
	// present in the schema so enabling caching needs no migration.
	CacheHit bool         `json:"cache_hit"`
	Outcome  UsageOutcome `json:"outcome"`
	// ErrorCode is the normalized failure code for unsuccessful requests.
	ErrorCode ErrorCode `json:"error_code,omitempty"`
	Streaming bool      `json:"streaming"`
	// Status is the HTTP status returned to the client.
	Status int `json:"status"`
	// ClientIP and UserAgent support abuse investigation.
	ClientIP  string `json:"client_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	// EndUser is the optional end-user identifier passed via the request body.
	EndUser   string    `json:"end_user,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AuditAction is the verb of an audited operation.
type AuditAction string

const (
	AuditCreate     AuditAction = "create"
	AuditUpdate     AuditAction = "update"
	AuditDelete     AuditAction = "delete"
	AuditLogin      AuditAction = "login"
	AuditRotate     AuditAction = "rotate"
	AuditRevoke     AuditAction = "revoke"
	AuditAllow      AuditAction = "allow"
	AuditDeny       AuditAction = "deny"
	AuditOverridden AuditAction = "override"
	AuditProbe      AuditAction = "probe"
	// AuditLogout records the end of an operator console session.
	AuditLogout AuditAction = "logout"
	// AuditPasswordChange records a console operator rotating their own password.
	AuditPasswordChange AuditAction = "password_change"
)

// AuditResource names the kind of object an audit event concerns.
type AuditResource string

const (
	ResourceTenant        AuditResource = "tenant"
	ResourceAPIKey        AuditResource = "api_key"
	ResourceProvider      AuditResource = "provider"
	ResourceModel         AuditResource = "model"
	ResourceRoutingPolicy AuditResource = "routing_policy"
	ResourceBudget        AuditResource = "budget"
	ResourceSettings      AuditResource = "settings"
	ResourceEndpoint      AuditResource = "endpoint"
	ResourceOverride      AuditResource = "override"
	ResourceReplayJob     AuditResource = "replay_job"
	ResourceEvaluation    AuditResource = "evaluation"
	ResourceCache         AuditResource = "cache"
	ResourceFeedback      AuditResource = "feedback"
	ResourceTunnel        AuditResource = "tunnel"
	// ResourceDashboardUser is a human operator account for the console.
	ResourceDashboardUser AuditResource = "dashboard_user"
	// ResourceDashboardSession is one signed-in console session.
	ResourceDashboardSession AuditResource = "dashboard_session"
	// ResourcePricingVersion is one immutable price sheet entry.
	ResourcePricingVersion AuditResource = "pricing_version"
	// ResourceCostAnomaly is one flagged spend deviation.
	ResourceCostAnomaly AuditResource = "cost_anomaly"
)

// AuditEvent is an immutable record of a control-plane mutation.
//
// Audit events are append-only and are written for administrative actions only.
// Inference traffic is far too high-volume to audit at the event store, and its
// accountability need (who spent what) is satisfied by UsageRecord.
type AuditEvent struct {
	ID         string        `json:"id"`
	Action     AuditAction   `json:"action"`
	Resource   AuditResource `json:"resource"`
	ResourceID string        `json:"resource_id"`
	// TenantID scopes the event; empty for platform-wide changes.
	TenantID string `json:"tenant_id,omitempty"`
	// ActorKeyID is the key that performed the action.
	ActorKeyID string `json:"actor_key_id,omitempty"`
	// ActorLabel is a display name captured at write time so the event remains
	// readable after the actor is deleted.
	ActorLabel string `json:"actor_label,omitempty"`
	ActorIP    string `json:"actor_ip,omitempty"`
	// Before and After capture the state change as JSON for diffing.
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
	// Metadata carries action-specific context, e.g. the reason for revocation.
	Metadata  map[string]string `json:"metadata,omitempty"`
	RequestID RequestID         `json:"request_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// TraceSpanKind classifies a span within a request trace.
type TraceSpanKind string

const (
	SpanHTTP       TraceSpanKind = "http"
	SpanAuth       TraceSpanKind = "auth"
	SpanPolicy     TraceSpanKind = "policy"
	SpanClassifier TraceSpanKind = "classifier"
	SpanShaping    TraceSpanKind = "shaping"
	SpanRouting    TraceSpanKind = "routing"
	SpanScoring    TraceSpanKind = "scoring"
	SpanProvider   TraceSpanKind = "provider"
	SpanFallback   TraceSpanKind = "fallback"
	SpanCache      TraceSpanKind = "cache"
	SpanGuardrail  TraceSpanKind = "guardrail"
	SpanEval       TraceSpanKind = "eval"
	SpanPersist    TraceSpanKind = "persist"
)

// TraceAttempt records one provider call attempt inside a request trace. It is
// the detailed counterpart to UsageRecord: usage says what it cost, attempts say
// what happened.
type TraceAttempt struct {
	// Number is the 1-based global attempt index across all providers.
	Number int `json:"number"`
	// Target is the provider/model attempted.
	Target RouteTarget `json:"target"`
	// StartedAt is relative to the trace start, in milliseconds, which keeps
	// traces compact and easy to render as a waterfall.
	StartedOffsetMS int64 `json:"started_offset_ms"`
	DurationMS      int64 `json:"duration_ms"`
	// Status is the HTTP status from the provider, 0 when no response arrived.
	Status int `json:"status,omitempty"`
	// ErrorCode is empty on success.
	ErrorCode ErrorCode `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
	// RetryTriggered and FallbackTriggered explain the transition that followed.
	RetryTriggered    bool `json:"retry_triggered"`
	FallbackTriggered bool `json:"fallback_triggered"`
	// BackoffMS is the delay applied after this attempt.
	BackoffMS int64 `json:"backoff_ms,omitempty"`
	// FirstTokenMS is the time to first streamed token, when streaming.
	FirstTokenMS int64      `json:"first_token_ms,omitempty"`
	Usage        TokenUsage `json:"usage,omitempty"`
}

// TraceSpan is a coarse timeline entry for the phases of a request.
type TraceSpan struct {
	Kind       TraceSpanKind     `json:"kind"`
	Name       string            `json:"name"`
	StartMS    int64             `json:"start_ms"`
	DurationMS int64             `json:"duration_ms"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// RequestTrace is the complete story of one request: the decision that was made,
// every attempt that followed, and the phase timings. It is stored in ClickHouse
// because it is high-volume and analytical; the Postgres usage row remains the
// billing system of record.
type RequestTrace struct {
	ID        string    `json:"id"`
	RequestID RequestID `json:"request_id"`
	TraceID   string    `json:"trace_id,omitempty"`
	TenantID  string    `json:"tenant_id"`
	APIKeyID  string    `json:"api_key_id,omitempty"`
	// Decision is the routing decision, embedded so a trace is self-contained.
	Decision *RouteDecision `json:"decision,omitempty"`
	// Attempts is every provider call, in order.
	Attempts []TraceAttempt `json:"attempts"`
	// Spans is the phase timeline.
	Spans []TraceSpan `json:"spans,omitempty"`
	// Outcome and ErrorCode mirror the usage record for fast filtering.
	Outcome   UsageOutcome `json:"outcome"`
	ErrorCode ErrorCode    `json:"error_code,omitempty"`
	StartedAt time.Time    `json:"started_at"`
	TotalMS   int64        `json:"total_ms"`
	// ClientStreamed is true when the response was delivered as SSE. Streaming
	// requests are still traced, but their total latency reflects the last byte.
	ClientStreamed bool      `json:"client_streamed"`
	CreatedAt      time.Time `json:"created_at"`
}

// ProviderStatusSnapshot is a periodic rollup of provider behaviour, used by the
// dashboard's health history and by the routing engine as a latency prior.
type ProviderStatusSnapshot struct {
	ID           string      `json:"id"`
	ProviderID   string      `json:"provider_id"`
	ProviderName string      `json:"provider_name,omitempty"`
	State        HealthState `json:"state"`
	// WindowMS is the aggregation window the metrics cover.
	WindowMS int64 `json:"window_ms"`
	// RequestCount, SuccessCount and ErrorCount are the raw counters.
	RequestCount int `json:"request_count"`
	SuccessCount int `json:"success_count"`
	ErrorCount   int `json:"error_count"`
	// SuccessRate and ErrorRate are derived ratios in [0,1].
	SuccessRate float64 `json:"success_rate"`
	ErrorRate   float64 `json:"error_rate"`
	// LatencyP50MS, LatencyP95MS and LatencyP99MS are window percentiles.
	LatencyP50MS int64 `json:"latency_p50_ms"`
	LatencyP95MS int64 `json:"latency_p95_ms"`
	LatencyP99MS int64 `json:"latency_p99_ms"`
	// TotalTokens and TotalCostUSD aggregate consumption in the window.
	TotalTokens  int64     `json:"total_tokens"`
	TotalCostUSD float64   `json:"total_cost_usd"`
	Message      string    `json:"message,omitempty"`
	CapturedAt   time.Time `json:"captured_at"`
}

// BudgetPeriod is the window a budget applies to.
type BudgetPeriod string

const (
	BudgetDaily   BudgetPeriod = "daily"
	BudgetWeekly  BudgetPeriod = "weekly"
	BudgetMonthly BudgetPeriod = "monthly"
	BudgetTotal   BudgetPeriod = "total"
)

// Budget is a spend limit attached to a tenant, key or policy.
type Budget struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	// Scope narrows the budget: "tenant", "key" or "policy".
	Scope   string       `json:"scope"`
	ScopeID string       `json:"scope_id,omitempty"`
	Period  BudgetPeriod `json:"period"`
	// LimitUSD is the maximum spend in the period.
	LimitUSD float64 `json:"limit_usd"`
	// SpentUSD is maintained in Redis and reconciled to Postgres periodically;
	// it is advisory rather than authoritative.
	SpentUSD float64 `json:"spent_usd"`
	// Enforced blocks requests once the limit is reached; when false the budget
	// is observational and only alerts.
	Enforced bool `json:"enforced"`
	// AlertThresholdsUSD trigger notifications at these spend levels.
	AlertThresholdsUSD []float64 `json:"alert_thresholds_usd,omitempty"`
	// ResetAt is the next period boundary.
	ResetAt   time.Time `json:"reset_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Remaining returns the unspent amount, never negative.
func (b *Budget) Remaining() float64 {
	if b.SpentUSD >= b.LimitUSD {
		return 0
	}
	return b.LimitUSD - b.SpentUSD
}

// Exceeded reports whether a budget blocks further spend.
func (b *Budget) Exceeded(additional float64) bool {
	if !b.Enforced {
		return false
	}
	return b.SpentUSD+additional > b.LimitUSD
}
