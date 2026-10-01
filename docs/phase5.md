# Phase 5: Local Request Cache

Phase 5 makes CoreRouter faster and cheaper by reusing prior responses
wherever it is safe. The cache is a first-class, policy-aware part of the
request flow — not an afterthought bolted on beside routing.

## Request flow

1. authenticate the request
2. apply tenant and endpoint policy
3. evaluate the cache decision (streaming, sensitivity, tools, live-data,
   determinism, policy and endpoint switches)
4. compute the full cache key
5. check exact → prefix (short prompts only) → semantic
6. on hit, return the cached response with `corerouter.cache_hit: true`
7. on miss, route to the provider as usual
8. store the final response with serving metadata when eligible
9. record metrics, traces and entry metadata
10. expose everything in `/cache` on the dashboard

The public inference API is unchanged. A hit returns the same JSON shape as
a live response; only the `corerouter` block gains `cache_hit`, `cache_kind`,
`cache_similarity` and `cache_reuse_count`.

## Cache key design

`internal/cache/key.go` builds the exact key from every field that can
change the answer:

- tenant + API key (isolation is structural, not conventional)
- model (requested; provider validated post-route from stored metadata)
- full message content incl. system/developer turns and tool-call history
- tool definitions (full schemas, not just names) + `tool_choice`
- generation settings: max tokens, temperature, top_p/top_k/min_p,
  repetition/presence/frequency penalties, seed, n
- response contract: `response_format` type + schema hash, reasoning effort
- routing policy id + version, endpoint scope, sensitivity, end user

`PromptHash` and `ToolsHash` are stored alongside the body so a hit can
validate that the world has not changed (model swap, policy edit, tool
schema change, endpoint move) and turn into a miss instead of a stale hit.

Redis keys are tenant-namespaced: `response:tenant:<id>:exact:<hash>`.
A tenant flush deletes exactly that namespace. Legacy bare keys are read
for rolling upgrades but never written.

## Policy engine

`internal/cache/policy.go` decides per request, safety first:

| Bypass reason | Meaning |
|---|---|
| `bypass_requested` | `X-CoreRouter-No-Cache: true` |
| `streaming` | streams are never cached |
| `sensitive_request` | any non-`public` sensitivity label |
| `policy_disabled` | routing policy has caching off |
| `endpoint_cache_disabled` | endpoint override `use_cache: false` |
| `tool_request` | tools attached and not verifiably safe |
| `live_data_request` | prompt looks like live facts (opt-out via config) |
| `nondeterministic_request` | temp>0 unseeded without opt-in |
| `multi_sample_request` | `n > 1` |
| `multimodal_request` | image parts |
| `response_too_large` | body over `max_response_bytes` |

Per-scope `cache_policies` rows override the global config. Resolution:
key > endpoint > tenant+model > tenant+provider > tenant > global.
A matching disabled row bypasses; no match means global defaults apply.

Deterministic built-ins (`now`, `echo`, or registry-verified
`safety==safe && executable`) are reusable; every other tool bypasses by
default. Endpoint scopes are namespaced, not bypassed.

## Layers

- **Exact** (`exact:`): byte-identical normalized requests. The reliable
  tier; gate with `response_cache` + `exact_enabled`.
- **Prefix** (`prefix:`): system/developer prefix hash. Served as a full
  response **only for short prompts** (`normalized length <= prefix_length`).
  A shared 256-char head with a different tail never hits — that was the
  Phase 2 correctness gap this gate closes. Long chats still benefit via
  prefix accounting and system-prefix reuse visibility.
- **Semantic** (`semantic:`): deterministic word-bag cosine over an
  in-process index (no ML model on the hot path), tenant- and
  model-isolated, behind `semantic_threshold` (default 0.92). Hits carry
  the similarity score in traces and metrics.

## Invalidation

`internal/cache/invalidation.go` + `POST /admin/v1/cache/invalidate`:

- `tenant`: deletes `response:tenant:<id>:*` + that tenant's semantic index
- `model` / `provider`: tenant-scoped when a tenant is given, else global
  (hashes cannot be mapped to models without an index, so the reason
  records the blast radius honestly)
- `key`: one exact entry
- `all`: everything known

Every flush writes a `cache_invalidations` row (scope, target, reason,
actor, removed count), publishes `cr.cache.invalidated` on NATS, bumps
`corerouter_cache_invalidations_total{scope,reason}` and clears the
`cache_entries` metadata for the scope. Change the provider, model,
policy, tenant settings or tool definitions — then flush the affected
scope; entries also expire via TTL.

## Storage

- **Redis**: bodies as `CachedPayload{body, meta}` envelopes under the
  tenant namespace, TTL-bounded.
- **PostgreSQL** (`0006_phase5.sql`): `cache_policies`, `cache_invalidations`,
  and `cache_entries` metadata (prompt hash + preview, model, provider,
  policy, hit/reuse counts). Powers inspect, top-prompts and the audit
  trail without ever serving bodies.
- **ClickHouse**: existing `cache_events` / `cache_hit` columns continue to
  receive per-request rows via the async recorder.

## Observability

- Prometheus: `corerouter_cache_hits_total{tenant,kind}`,
  `misses_total{tenant}`, `bypass_total{tenant,reason}`,
  `lookup_duration_seconds{tenant,outcome}`,
  `invalidations_total{scope,reason}`,
  `latency_saved_seconds{tenant,kind}`,
  `semantic_similarity{tenant}`.
- Traces: `CacheTrace{hit, kind, key, lookup_ms, similarity,
  bypass_reason, reuse_count, latency_saved_ms}` on every request.
- NATS: `cr.cache.invalidated` with the same summary as the audit row.
- Dashboard `/cache`: hit rate, exact/prefix/semantic splits, bypass
  reasons, reuse count, latency saved, lookup average, top prompts, TTL
  and tier switches, per-scope policies, recent entries (metadata only)
  and invalidation events, plus scoped flush controls.

## Safety rules

- Never reuse across tenants, keys, models, policies, endpoints,
  sensitivities or tool definitions.
- Never cache streams, `n>1`, images, sensitive payloads, oversized
  responses, or state-changing tool traffic by default.
- Semantic reuse needs an explicit similarity threshold and is
  tenant/model isolated with the score always visible.
- A provider/model/policy/tool change turns hits into misses; a manual
  flush is predictable, scoped and audited.

## Config

```yaml
cache:
  response_cache: false   # master switch
  exact_enabled: true     # exact reuse needs both switches
  response_ttl: 5m
  max_response_bytes: 262144
  semantic_enabled: true
  semantic_threshold: 0.92
  prefix_enabled: true
  prefix_length: 256
  bypass_tool_requests: true
  allow_nondeterministic: false
  bypass_live_data: true
  max_semantic_entries: 2000
```

Env overrides: `CR_CACHE_RESPONSE_ENABLED/TTL`,
`CR_CACHE_EXACT_ENABLED`, `CR_CACHE_SEMANTIC_ENABLED/THRESHOLD`,
`CR_CACHE_PREFIX_ENABLED/LENGTH`, `CR_CACHE_BYPASS_TOOLS`,
`CR_CACHE_ALLOW_NONDETERMINISTIC`, `CR_CACHE_BYPASS_LIVE_DATA`,
`CR_CACHE_MAX_SEMANTIC_ENTRIES`.

## Admin API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/cache/stats?tenant_id=` | extended stats + top prompts + config |
| `GET` | `/cache/inspect?tenant_id=&model=&provider=&limit=` | entry metadata (no bodies) |
| `POST` | `/cache/invalidate` | `{scope, tenant_id, model, provider, key, reason}` |
| `GET` | `/cache/policies?tenant_id=` | per-scope rules |
| `PUT` | `/cache/policies` | upsert a rule |
| `DELETE` | `/cache/policies/{id}` | delete a rule |
| `GET` | `/cache/invalidations?tenant_id=&limit=` | flush audit trail |

## Tests

`internal/cache/phase5_test.go` covers key isolation (tenant, key, model,
temperature, policy, endpoint, format, tools, tool schemas), the prefix
length gate, tenant flush isolation, every bypass rule, and semantic
tenant isolation. Run with `go test ./internal/cache/ -count=1`.
