# Caching

CoreRouter can reuse a prior response instead of calling a provider again. The
cache is a policy-aware part of the request flow, not a layer bolted on beside
routing: most requests deliberately bypass it, and every reuse is scoped so one
tenant can never see another's answer.

Off by default. Turn it on deliberately:

```yaml
cache:
  response_cache: true
  exact_enabled: true
  response_ttl: 5m
```

## Request flow

1. Authenticate the request.
2. Apply tenant and endpoint policy.
3. Evaluate the cache decision — streaming, sensitivity, tools, live data,
   determinism, policy and endpoint switches.
4. Compute the full cache key.
5. Check **exact → prefix → semantic**.
6. On a hit, return the cached response with `corerouter.cache_hit: true`.
7. On a miss, route to the provider as usual.
8. Store the final response with serving metadata when eligible.
9. Record metrics, traces and entry metadata.
10. Surface everything on the dashboard's `/cache` page.

The public inference API does not change. A hit returns the same JSON shape as a
live response; only the `corerouter` block gains `cache_hit`, `cache_kind`,
`cache_similarity` and `cache_reuse_count`.

## Tiers

| Tier | Key | Serves | Gate |
| --- | --- | --- | --- |
| **Exact** | Full normalized request hash | Byte-identical requests | `response_cache` + `exact_enabled` |
| **Prefix** | System/developer prefix hash | Short prompts only | `prefix_enabled`, `prefix_length` |
| **Semantic** | Word-bag cosine over an in-process index | Similar prompts | `semantic_enabled`, `semantic_threshold` |

**Prefix** serves a full response only when the normalized prompt length is
`<= prefix_length` (default 256). A shared 256-character head with a different
tail never hits — that was a real correctness gap, and the length gate closes it.
Long chats still benefit through prefix accounting and system-prefix reuse
visibility.

**Semantic** is a deterministic word-bag cosine, tenant- and model-isolated,
behind `semantic_threshold` (default 0.92). No ML model runs on the request path.
Every hit carries its similarity score in traces and metrics, so a surprising hit
is always explicable.

## The cache key

`internal/cache/key.go` builds the key from every field that can change the
answer:

- tenant and API key — isolation is structural, not conventional
- requested model
- full message content, including system and developer turns and tool-call history
- tool definitions (full schemas, not just names) and `tool_choice`
- generation settings: max tokens, temperature, top_p/top_k/min_p, repetition,
  presence and frequency penalties, seed, n
- response contract: `response_format` type and schema hash, reasoning effort
- routing policy id and version, endpoint scope, sensitivity, end user

`PromptHash` and `ToolsHash` are stored beside the body so a hit can validate
that the world has not changed — model swap, policy edit, tool schema change,
endpoint move — and turn into a miss instead of a stale hit.

Redis keys are tenant-namespaced: `response:tenant:<id>:exact:<hash>`. A tenant
flush deletes exactly that namespace.

## When the cache is skipped

Safety first. Any of these bypasses reuse:

| Bypass reason | Meaning |
| --- | --- |
| `bypass_requested` | `X-CoreRouter-No-Cache: true` |
| `streaming` | Streams are never cached. |
| `sensitive_request` | Any sensitivity label other than `public`. |
| `policy_disabled` | The routing policy has caching off. |
| `endpoint_cache_disabled` | An endpoint override sets `use_cache: false`. |
| `tool_request` | Tools attached and not verifiably safe. |
| `live_data_request` | The prompt looks like it asks for live facts. |
| `nondeterministic_request` | `temperature > 0` unseeded, without opt-in. |
| `multi_sample_request` | `n > 1`. |
| `multimodal_request` | Image parts present. |
| `response_too_large` | Body over `max_response_bytes`. |

Two of these are tunable, and both default to the safe answer:

- `bypass_tool_requests: true` — but deterministic built-ins (`now`, `echo`, or a
  registry-verified `safety: safe && executable`) are reusable, so a tool-backed
  assistant still gets a cache.
- `allow_nondeterministic: false` — set it true only if your workload prefers
  speed over sampling variance.

Endpoint scopes are namespaced in the key rather than bypassed, so a scoped
request caches safely per scope.

## Policy resolution

Per-scope `cache_policies` rows override the global config, resolved in this
order:

```
key  >  endpoint  >  tenant+model  >  tenant+provider  >  tenant  >  global
```

A matching *disabled* row bypasses. No match means the global defaults apply.

```bash
curl -s "$GATEWAY/admin/v1/cache/policies?tenant_id=$TENANT" \
  -H "Authorization: Bearer $CR_ADMIN_KEY" | jq

curl -s -X PUT $GATEWAY/admin/v1/cache/policies \
  -H "Authorization: Bearer $CR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"support-bot","tenant_id":"'"$TENANT"'","ttl_seconds":900,"enabled":true,"scopes":["model:gpt-4o-mini"]}' | jq
```

## Invalidation

Entries also expire by TTL, but when something they depend on changes, flush the
affected scope:

```bash
curl -s -X POST $GATEWAY/admin/v1/cache/invalidate \
  -H "Authorization: Bearer $CR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"scope":"model","model":"gpt-4o-mini","reason":"fine-tune deployed"}' | jq
```

| Scope | Effect |
| --- | --- |
| `tenant` | Deletes `response:tenant:<id>:*` plus that tenant's semantic index. |
| `model` / `provider` | Tenant-scoped when a tenant is given, else global. |
| `key` | One exact entry. |
| `all` | Everything known. |

Hashes cannot be mapped back to models without an index, so a model or provider
flush records its blast radius honestly in the reason rather than pretending to be
surgical.

Every flush writes a `cache_invalidations` row (scope, target, reason, actor,
removed count), publishes `cr.cache.invalidated` on NATS, bumps
`corerouter_cache_invalidations_total{scope,reason}` and clears the `cache_entries`
metadata for that scope.

**Flush when you change** the provider, a model, a policy, tenant settings or tool
definitions.

## Visibility

The dashboard's `/cache` page shows hit rate, the exact/prefix/semantic split,
bypass reasons, reuse count, latency saved, average lookup time, top prompts, TTL
and tier switches, per-scope policies, recent entries (metadata only) and the
invalidation trail — plus scoped flush controls.

```bash
curl -s "$GATEWAY/admin/v1/cache/stats?tenant_id=$TENANT" -H "Authorization: Bearer $CR_ADMIN_KEY" | jq
curl -s "$GATEWAY/admin/v1/cache/inspect?tenant_id=$TENANT&limit=20" -H "Authorization: Bearer $CR_ADMIN_KEY" | jq
curl -s "$GATEWAY/admin/v1/cache/invalidations?limit=10" -H "Authorization: Bearer $CR_ADMIN_KEY" | jq
```

`inspect` returns entry metadata, never bodies — bodies stay in Redis.

Metrics:

| Metric | Meaning |
| --- | --- |
| `corerouter_cache_hits_total{tenant,kind}` | Hits by tier |
| `corerouter_cache_misses_total{tenant}` | Misses |
| `corerouter_cache_bypass_total{tenant,reason}` | Skips and why |
| `corerouter_cache_lookup_duration_seconds{tenant,outcome}` | Lookup latency |
| `corerouter_cache_invalidations_total{scope,reason}` | Flushes |
| `corerouter_cache_latency_saved_seconds{tenant,kind}` | Value delivered |
| `corerouter_cache_semantic_similarity{tenant}` | Score distribution |

## Configuration

```yaml
cache:
  response_cache: false        # master switch
  exact_enabled: true          # exact reuse needs both switches
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

Environment overrides: `CR_CACHE_RESPONSE_ENABLED`, `CR_CACHE_RESPONSE_TTL`,
`CR_CACHE_EXACT_ENABLED`, `CR_CACHE_SEMANTIC_ENABLED`,
`CR_CACHE_SEMANTIC_THRESHOLD`, `CR_CACHE_PREFIX_ENABLED`,
`CR_CACHE_PREFIX_LENGTH`, `CR_CACHE_BYPASS_TOOLS`,
`CR_CACHE_ALLOW_NONDETERMINISTIC`, `CR_CACHE_BYPASS_LIVE_DATA`,
`CR_CACHE_MAX_SEMANTIC_ENTRIES`.

## Turning it on safely

A sensible first deployment:

```yaml
cache:
  response_cache: true
  exact_enabled: true
  response_ttl: 5m
  semantic_enabled: false        # start with exact only
  prefix_enabled: false          # add prefix once exact is understood
  bypass_tool_requests: true
  allow_nondeterministic: false
```

Exact-only reuse is the tier whose correctness is easiest to argue. Enable prefix
and semantic separately, watch `corerouter_cache_hits_total{kind}`, and raise
`semantic_threshold` before lowering it.

## Common problems

| Symptom | Cause | Fix |
| --- | --- | --- |
| Hit rate is zero | `response_cache` is off, or `exact_enabled` is | Both switches must be on |
| Everything bypasses with `tool_request` | Tools attached | Verify the tools are deterministic built-ins, or accept the bypass |
| Everything bypasses with `nondeterministic_request` | Unseeded temperature > 0 | Seed it, or set `allow_nondeterministic: true` |
| Semantic hits look wrong | Threshold too low | Raise `semantic_threshold` toward `0.95`+ |
| A stale answer after a model change | Entries not flushed | Flush the model or provider scope |
| Hits across tenants | Not possible by construction | Check `tenant_id` is set on the key; entries are namespaced |

---

Related: [Routing](routing.md) · [Tools](tools.md) · [Database](database.md) · [Dashboard](dashboard.md) · [Back to README](../README.md)