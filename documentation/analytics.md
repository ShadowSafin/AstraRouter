# Analytics

The analytics console answers operational questions about traffic that has already
been served: volume, latency, spend, cache effectiveness, routing behaviour and
failures. It is a read-only surface. Nothing here influences routing or provider
execution.

## Where the numbers come from

| Store | Used for |
| --- | --- |
| PostgreSQL `usage_records` | Every aggregate on the page: traffic, latency percentiles, cost, cache split, per-provider / per-model / per-tenant breakdowns. |
| PostgreSQL `request_logs` | Routing attribution (which strategy served the request) and its attempt counts. |
| Redis | Not used by analytics. Counters there are live rate-limit and budget state, not history. |
| ClickHouse | Write-only today: it is the long-term trace/usage sink Grafana reads. The admin API does not query it. |

The billing row (`usage_records`) is the source of truth for analytics rather
than the trace store because it is complete, cheap to aggregate and stable.
Anything an operator needs to act on must match what is billed.

## The endpoint

```
GET /admin/v1/analytics/report
```

Returns the whole page in one document, so every section describes the same
window and the page does not flicker section by section.

### Parameters

| Parameter | Meaning |
| --- | --- |
| `from`, `to` | RFC 3339 timestamps, or a Go duration such as `24h`. Defaults to the last 24 hours. |
| `interval` | Bucket width: `1m`, `5m`, `1h`, `1d`, `1w`. Defaults to a width chosen from the range so a chart has a readable number of points. |
| `tenant_id` | Restrict to one tenant. |
| `provider` | Restrict to one provider name. |
| `model` | Restrict to one routed model. |
| `outcome` | Restrict to one usage outcome (`success`, `fallback`, `error`, `rejected`, `canceled`). |
| `top` | Rows per ranking table. Defaults to 10, capped at 100. |
| `sort` | Ranking order: `requests` (default), `cost`, `errors`, `latency`, `tokens`. |

### Response shape

```jsonc
{
  "window": { "from": "...", "to": "...", "interval": "1h" },
  "summary": { "requests": 0, "cost_usd": 0, "latency_p90_ms": 0, "...": 0 },
  "previous_summary": { "...": 0 },
  "buckets": [ { "start": "...", "requests": 0, "latency_p90_ms": 0, "...": 0 } ],
  "comparison": [ { "metric": "p95_latency_ms", "current": 0, "previous": 0,
                    "change": 0, "change_ratio": 0, "higher_is_worse": true, "unit": "ms" } ],
  "providers": [ { "key": "openai", "requests": 0, "success_rate": 0, "cost_per_success_usd": 0 } ],
  "models":   [ "... same shape as providers" ],
  "tenants":  [ "... same shape as providers" ],
  "policies": [],
  "request_types": [],
  "outcomes": [],
  "errors": [],
  "cache":   { "hits": 0, "misses": 0, "hit_rate": 0, "latency_saved_total_ms": 0, "cost_saved_usd": 0, "active": true },
  "routing": { "strategies": [], "provider_switch_rate": 0, "avg_attempts": 0 },
  "generated_at": "..."
}
```

Every breakdown row is the same `DimensionRow` shape (`internal/domain/analytics.go`),
so a table can render any dimension without a bespoke type.

## How the aggregation is organised

- `internal/analytics/` holds the arithmetic: derived rates, period comparison,
  cache savings and routing rollups. Every function is **pure** — it takes rows
  that have already been read and returns figures. That is what makes the
  aggregation unit-testable without a database (`report_test.go`), and it keeps
  the arithmetic out of the transport layer.
- `internal/storage/records.go` holds the SQL. `UsageRepository.Grouped` is a
  single parameterized `GROUP BY` over a **whitelisted** dimension, so a new
  breakdown does not mean a new near-duplicate query (and a dimension can never
  be interpolated from caller input).
- `internal/api/analytics.go` composes the report: it resolves the window,
  runs the queries, calls the analytics functions and returns one payload.

Rates are always derived from the counts **on the same row**. A consumer cannot
divide a numerator from one aggregate by a denominator from another, which is the
usual way a dashboard invents a wrong percentage.

## Two deliberate design decisions

**The window is quantised.** The end of the range is snapped down to the bucket
width, keeping the width. Without this, `to` is "now" to the nanosecond, so two
requests a second apart resolve to different windows and nothing is ever
cacheable; the cost is that a chart can lag by at most one bucket. It also makes
the numbers stable while an operator reads them.

**The response is cached briefly.** A composed report runs around a dozen
aggregates. A process-local TTL cache (~20s, keyed on filters plus the resolved
window) makes reloads cheap and concurrent viewers share the work. A shared cache
would need cross-instance invalidation for no benefit at this scale; the TTL is
short enough that correctness is unaffected.

## Cache savings are estimates, and the UI says so

A cache hit has no counterfactual to measure. Savings are estimated as the mean
latency or cost of a *provider-served* request in the same window, applied to the
hits:

```
latency_saved_total_ms = (miss_avg_latency_ms - hit_avg_latency_ms) * hits
cost_saved_usd         = miss_avg_cost_usd * hits
```

The two populations are computed in a single query (`CacheSplit`) so a hit can
never be counted against a miss total from a different instant. A cache slower
than the provider reports zero saved, never a negative number.

Provider-side prompt caching (`cached_prompt_tokens`) is a different mechanism
and is reported separately, not folded into the response-cache figures.

## Known limitation: no endpoint dimension

`usage_records` and `request_logs` carry `tenant_id`, `provider`, `model`,
`requested_model`, `policy_id`, `request_type`, `outcome` and `error_code` — but
**no `endpoint_id`**. Per-endpoint analytics is therefore not offered rather than
being approximated from something that only correlates with it.

To add it: record `endpoint_id` on the usage row at write time (the routing
context already has it), backfill nothing (historical rows stay unattributed),
then add `DimensionEndpoint` to `internal/storage/records.go` and a table to the
console. The aggregation layer needs no change — it is dimension-agnostic.

## Extending

1. Add a dimension: add the constant and its column to `dimensionColumns`, then
   call `Grouped` from the handler and render a table from the returned rows.
2. Add a headline metric: extend `Compare` in `internal/analytics/report.go` and
   add a card. The comparison is positional; the order matches the KPI row.
3. Add a derived rate: put it in `EnrichRow` so it is computed once, from the
   row's own counts, for every dimension.

Run the tests with `go test ./internal/analytics/...` — the aggregation logic is
covered without needing a database.
