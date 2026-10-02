package domain

import "time"

// RequestLog is the per-request detail record rendered by the dashboard's request
// log view.
//
// It intentionally duplicates a few fields from UsageRecord: the two serve
// different readers. UsageRecord is the billing row and must be complete and
// cheap to aggregate; RequestLog is the debugging row and carries the routing
// decision and error text, which are too large to keep in the billing table.
type RequestLog struct {
	ID               string       `json:"id"`
	RequestID        string       `json:"request_id"`
	TraceID          string       `json:"trace_id,omitempty"`
	TenantID         string       `json:"tenant_id,omitempty"`
	APIKeyID         string       `json:"api_key_id,omitempty"`
	RequestedModel   string       `json:"requested_model,omitempty"`
	RoutedModel      string       `json:"routed_model,omitempty"`
	Provider         string       `json:"provider,omitempty"`
	PolicyID         string       `json:"policy_id,omitempty"`
	Strategy         string       `json:"strategy,omitempty"`
	Status           int          `json:"status"`
	Outcome          UsageOutcome `json:"outcome"`
	ErrorCode        ErrorCode    `json:"error_code,omitempty"`
	ErrorMessage     string       `json:"error_message,omitempty"`
	LatencyMS        int64        `json:"latency_ms"`
	Attempts         int          `json:"attempts"`
	FallbackUsed     bool         `json:"fallback_used"`
	PromptTokens     int          `json:"prompt_tokens"`
	CompletionTokens int          `json:"completion_tokens"`
	CostUSD          float64      `json:"cost_usd"`
	// Decision is the full routing decision, retained so an operator can answer
	// "why did this go there?" after the fact.
	Decision  *RouteDecision `json:"decision,omitempty"`
	ClientIP  string         `json:"client_ip,omitempty"`
	UserAgent string         `json:"user_agent,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// TimeBucket is one point on a time series.
type TimeBucket struct {
	// Start is the inclusive start of the bucket.
	Start time.Time `json:"start"`
	// End is the exclusive end of the bucket.
	End time.Time `json:"end"`
	// Interval is the bucket width, e.g. "1h".
	Interval string `json:"interval"`
	// Requests is the number of requests in the bucket.
	Requests int64 `json:"requests"`
	// Successes, Errors and Rejections partition Requests by outcome.
	Successes  int64 `json:"successes"`
	Errors     int64 `json:"errors"`
	Rejections int64 `json:"rejections"`
	// Fallbacks counts requests that used a non-primary target.
	Fallbacks        int64 `json:"fallbacks"`
	CacheHits        int64 `json:"cache_hits"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// CostUSD is the summed cost in the bucket.
	CostUSD float64 `json:"cost_usd"`
	// LatencyP50MS, LatencyP90MS, LatencyP95MS and LatencyP99MS are bucket
	// percentiles. p90 is carried alongside p95 because tail latency that only
	// shows up at p95 is often already visible at p90, and operators triage on
	// the earlier signal.
	LatencyP50MS int64 `json:"latency_p50_ms"`
	LatencyP90MS int64 `json:"latency_p90_ms"`
	LatencyP95MS int64 `json:"latency_p95_ms"`
	LatencyP99MS int64 `json:"latency_p99_ms"`
}

// UsageSummary is an aggregate over a query range.
type UsageSummary struct {
	Requests   int64 `json:"requests"`
	Successes  int64 `json:"successes"`
	Errors     int64 `json:"errors"`
	Rejections int64 `json:"rejections"`
	Canceled   int64 `json:"canceled"`
	Fallbacks  int64 `json:"fallbacks"`
	// ErrorRate is the ratio of non-success outcomes to requests.
	ErrorRate float64 `json:"error_rate"`
	// FallbackRate is the ratio of fallback requests to requests.
	FallbackRate float64 `json:"fallback_rate"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	TotalCostUSD float64 `json:"total_cost_usd"`
	// AvgCostUSD is the mean cost per request.
	AvgCostUSD float64 `json:"avg_cost_usd"`
	// AvgLatencyMS is the mean end-to-end latency.
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	LatencyP50MS int64   `json:"latency_p50_ms"`
	LatencyP90MS int64   `json:"latency_p90_ms"`
	LatencyP95MS int64   `json:"latency_p95_ms"`
	LatencyP99MS int64   `json:"latency_p99_ms"`
	// UniqueTenants and UniqueKeys support the platform overview.
	UniqueTenants int64 `json:"unique_tenants"`
	UniqueKeys    int64 `json:"unique_keys"`
}

// DimensionRow is one aggregate for a single value of a grouping dimension:
// a provider, a model, a tenant, a policy, a request type, an outcome or an
// error code. One shape for every breakdown keeps the analytics API and the
// dashboard tables uniform, and means a new dimension needs no new type.
//
// The rate and per-unit fields are derived from the counts rather than stored,
// so a consumer can never divide by a different denominator than the one the
// counts came from.
type DimensionRow struct {
	// Key is the dimension value. An empty key means "unattributed" (for
	// example a request that arrived before a tenant was resolved).
	Key string `json:"key"`

	Requests   int64 `json:"requests"`
	Successes  int64 `json:"successes"`
	Errors     int64 `json:"errors"`
	Rejections int64 `json:"rejections"`
	Fallbacks  int64 `json:"fallbacks"`
	CacheHits  int64 `json:"cache_hits"`

	PromptTokens       int64 `json:"prompt_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	CachedPromptTokens int64 `json:"cached_prompt_tokens"`

	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	LatencyP50MS int64   `json:"latency_p50_ms"`
	LatencyP90MS int64   `json:"latency_p90_ms"`
	LatencyP95MS int64   `json:"latency_p95_ms"`
	LatencyP99MS int64   `json:"latency_p99_ms"`

	// Derived metrics. Computed once, in one place, from the counts above.
	SuccessRate       float64 `json:"success_rate"`
	ErrorRate         float64 `json:"error_rate"`
	FallbackRate      float64 `json:"fallback_rate"`
	CacheHitRate      float64 `json:"cache_hit_rate"`
	CostPerSuccessUSD float64 `json:"cost_per_success_usd"`
	// TokensPerRequest supports spotting prompts that grew unexpectedly.
	TokensPerRequest float64 `json:"tokens_per_request"`
}

// CacheSplit separates cached responses from provider-served ones, which is what
// makes "did caching help?" answerable: savings are the difference between the
// two populations, not a number invented by the cache.
type CacheSplit struct {
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	HitAvgMS  float64 `json:"hit_avg_latency_ms"`
	MissAvgMS float64 `json:"miss_avg_latency_ms"`
	// MissCostUSD is the mean cost of a provider-served request, the counterfactual
	// a hit avoided.
	MissCostUSD        float64 `json:"miss_avg_cost_usd"`
	HitTokens          int64   `json:"hit_total_tokens"`
	MissTokens         int64   `json:"miss_total_tokens"`
	CachedPromptTokens int64   `json:"cached_prompt_tokens"`
}

// StrategyRow is one aggregate for a routing strategy, read from the request
// log rather than the billing row: how a request was routed is debugging data,
// not billing data.
type StrategyRow struct {
	Strategy     string  `json:"strategy"`
	Requests     int64   `json:"requests"`
	Fallbacks    int64   `json:"fallbacks"`
	Errors       int64   `json:"errors"`
	AvgAttempts  float64 `json:"avg_attempts"`
	FallbackRate float64 `json:"fallback_rate"`
}

// ProviderBreakdownRow is one row of a per-provider aggregate.
type ProviderBreakdownRow struct {
	Provider     string  `json:"provider"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	ErrorRate    float64 `json:"error_rate"`
	Fallbacks    int64   `json:"fallbacks"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	LatencyP95MS int64   `json:"latency_p95_ms"`
}

// ModelBreakdownRow is one row of a per-model aggregate.
type ModelBreakdownRow struct {
	Model        string  `json:"model"`
	Provider     string  `json:"provider,omitempty"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// Overview is the dashboard landing page payload.
type Overview struct {
	// Window is the range the figures cover.
	Window TimeRange `json:"window"`
	// Summary is the aggregate over the window.
	Summary UsageSummary `json:"summary"`
	// PreviousSummary is the aggregate over the immediately preceding window,
	// included so the dashboard can render deltas without a second request.
	PreviousSummary UsageSummary `json:"previous_summary"`
	// Series is the bucketed time series for charts.
	Series []TimeBucket `json:"series"`
	// Providers and Models are the top contributors in the window.
	Providers []ProviderBreakdownRow `json:"providers"`
	Models    []ModelBreakdownRow    `json:"models"`
	// ProviderHealth is the current health of every configured provider.
	ProviderHealth []ProviderHealth `json:"provider_health"`
}

// TimeRange is a closed-open time interval.
type TimeRange struct {
	// From is inclusive.
	From time.Time `json:"from"`
	// To is exclusive.
	To time.Time `json:"to"`
	// Interval is the bucket width for series queries.
	Interval string `json:"interval"`
}

// Duration returns the range width.
func (r TimeRange) Duration() time.Duration { return r.To.Sub(r.From) }

// Contains reports whether t falls within the range.
func (r TimeRange) Contains(t time.Time) bool {
	return !t.Before(r.From) && t.Before(r.To)
}

// Previous returns the equally sized window immediately before this one, which
// is what the dashboard compares against.
func (r TimeRange) Previous() TimeRange {
	width := r.Duration()
	return TimeRange{From: r.From.Add(-width), To: r.From, Interval: r.Interval}
}

// ParseInterval converts an interval name into a duration.
//
// The set is deliberately small: a fixed menu keeps the SQL bucketing code simple
// and prevents an operator from requesting a one-second bucket over a year of
// data, which would be an expensive way to render an unreadable chart.
func ParseInterval(name string) (time.Duration, bool) {
	switch name {
	case "1m", "minute":
		return time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "1h", "hour":
		return time.Hour, true
	case "1d", "day":
		return 24 * time.Hour, true
	case "1w", "week":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// IntervalForWidth chooses a reasonable bucket width for a time range.
//
// The goal is roughly 60 to 200 points: fewer and the chart looks coarse, more and
// the payload grows without adding visible detail.
func IntervalForWidth(width time.Duration) string {
	switch {
	case width <= 30*time.Minute:
		return "1m"
	case width <= 3*time.Hour:
		return "5m"
	case width <= 3*24*time.Hour:
		return "1h"
	case width <= 45*24*time.Hour:
		return "1d"
	default:
		return "1w"
	}
}
