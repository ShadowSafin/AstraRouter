# Architecture

How AstraRouter is put together and, more usefully, why. Each section ends with
the failure mode it is designed around, because that is what the design is
actually optimising for.

## Contents

- [Processes](#processes)
- [The request path](#the-request-path)
- [Routing](#routing)
- [Failure handling](#failure-handling)
- [Policy resolution](#policy-resolution)
- [Cost control](#cost-control)
- [Data stores](#data-stores)
- [Telemetry](#telemetry)
- [Configuration](#configuration)
- [Security](#security)

---

## Processes

Four kinds of process, each with one job:

| Process | Language | Responsibility |
| --- | --- | --- |
| `astrarouter` gateway | Go | The synchronous request path: authentication, policy, routing, provider calls, and the durable write of what happened. |
| `astrarouter-worker` | Python | Everything asynchronous and CPU-bound: candidate scoring, prompt analysis, telemetry rollups, optional LLM judging. |
| dashboard | TypeScript / Next.js | Read-only operator console, plus a server-side proxy that holds the admin credential. |
| Observability stack | third-party | Collector, Prometheus, Loki, Promtail, Grafana. |

**Why the split.** The gateway's latency budget belongs to the user, so nothing
best-effort may run inline with it: scoring a batch of candidates or aggregating
an hour of usage must not delay a token. Conversely, telemetry is allowed to be
lossy — it must never make inference fail. Putting the lossy, bursty work in
another process (and another language, where the numeric libraries are) makes
that separation structural rather than a matter of discipline.

**Failure mode it addresses.** A scoring loop competing with the request path for
CPU is a latency bug waiting to happen; a worker crash that takes down the
gateway is an availability bug. Both are impossible with separate processes.

## The request path

```
POST /v1/chat/completions
  │
  ├─ middleware/recover           panic → 500, never a broken connection
  ├─ middleware/identity          assigns X-Request-ID, opens the trace span
  ├─ middleware/cors              answers preflight before any auth work
  ├─ middleware/compress
  ├─ auth                         verify the key (Redis-cached), enforce `inference`
  │
  ├─ request context              tenant, key, requested model, streaming flag,
  │                               plus routing intent from X-AstraRouter-* headers
  │
  ├─ cache lookup                 exact → prefix → semantic, subject to policy
  │
  ├─ policy.Resolver.Resolve      most specific match wins
  ├─ policy.Enforcer.Check        rate limits and budgets, before any spend
  │
  ├─ classifier.Classify          task type, rules-first and deterministic
  ├─ shaping.Plan                 prompt normalization, trimming, prefixes
  │
  ├─ routing.Engine.Decide        build candidates → filter → order → pick
  ├─ routing.Executor.Execute     run the chain, with retry per provider
  │     ├─ provider adapter call  (streaming or buffered)
  │     └─ on failure → retry? fallback? stop?
  │
  ├─ tools loop                   only when policy allows gateway execution
  │
  ├─ response                     OpenAI-shaped body + a `astrarouter` metadata block
  └─ telemetry.Recorder           usage row, trace, log, metrics — off the hot path
```

Three properties are load-bearing:

1. **`writeJSON` encodes into a buffer before writing any header.** A
   serialization failure therefore still produces a valid error response instead
   of a truncated body under a `200` — the classic way a JSON handler fails
   silently.
2. **The metadata block is attached to errors as well as successes.** A client
   that logs a failure also captures which policy and provider were involved,
   which is the information you need at 3am and never have.
3. **Token ceilings are only applied when the client asked for one.** A policy
   ceiling bounds what a caller may request and what the gateway budgets for; it
   is never substituted in as the request's `max_tokens`. When a ceiling does
   bound generation, the response says so in `astrarouter.completion`.

**Failure mode it addresses.** "It returned 200 with half a JSON body" and "the
error told me nothing about what was attempted" are both un-debuggable from the
client side. Both are prevented by ordering.

## Routing

The engine is a pure function of the candidate set plus observable signals
(health, price, observed latency). No network calls happen inside it, which is
what makes routing unit-testable and reproducible when replaying a historical
request.

```
policy.Targets
   │
   ▼  resolve against the catalogue
candidates (provider + model + pricing + capabilities + health)
   │
   ▼  filter
   · model must be registered and usable
   · provider must be enabled, and have a working adapter
   · capabilities must intersect the request's requirements
   · prompt + output must fit the context window
   · health state must be eligible (relaxed if nothing else is left)
   · projected cost must fit the ceiling (relaxation applies too)
   · rate limits and budgets must not be exhausted
   │
   ▼  order by strategy
priority · weighted · lowest_cost · lowest_latency · highest_quality
   │
   ▼
RouteDecision { chosen, candidates, chain, retry, timeout, fallback, reason }
```

`weighted` selection is deterministic for a given request id. A retry inside one
request therefore makes the *same* choice as the original attempt rather than
randomly re-rolling, and a replayed trace reproduces the decision exactly.

Filtering relaxes in a defined order when it would otherwise empty the candidate
set: health first, then cost ceiling. Returning "no route" because a provider is
degraded — when it is the only provider that serves the model — is worse than
routing to a struggling provider and recording that it was degraded. The decision
carries a `degraded` flag so the relaxation is visible instead of silent.

**Failure mode it addresses.** Cascading unavailability. A provider outage that
empties the candidate set turns a partial outage into a total one.

## Failure handling

Three independent budgets, because they fail differently and diagnosing a single
collapsed knob is impossible:

| Budget | Scope | Meaning when exceeded |
| --- | --- | --- |
| `RetryPolicy` | one provider | Retry the same target with backoff (jittered, seeded from the request id so it stays deterministic). |
| `FallbackPolicy` | the chain | Move to a different provider. |
| `TimeoutPolicy` | total / per attempt / connect / stream idle / first token | Stop. |

The worst case is `FallbackPolicy.MaxAttempts` providers ×
`RetryPolicy.MaxAttempts` calls each, bounded in practice by the total timeout.

**Timeouts are hard budgets; latency targets are not.** `latency_target_ms` is a
ranking signal for choosing between healthy candidates. It was once also reused
as the hard per-attempt deadline, which meant the default 10s target cancelled
every generation longer than ten seconds — mid-sentence, with no signal that it
had been cut. Generation budgets now come only from the timeout policy, and a
streaming request that inherits no stated budget is granted a floor large enough
to finish. A budget an operator sets explicitly is never raised behind their back.

**A streaming attempt is committed.** Once a provider has begun a stream, a retry
cannot produce a better answer, only a longer one with a duplicated opening. So
the executor refuses to fail over after bytes reach the client and reports a
stream-started error instead of appending a second attempt to the first.

**Fallback eligibility is explicit when listed.** A policy with `on_error_codes`
gets *exactly* those codes — the list is exhaustive, not additive. An operator
who enumerates the codes that justify failover is narrowing behaviour
deliberately, so a code outside the list must not fail over even if the error's
own flag would allow it.

**Provider attribution after failover.** The routing decision records where the
request was *sent first*; the usage record and the response metadata record
**who answered**. These differ exactly on the requests an operator most needs to
trace, so the gateway walks the attempt trace backwards to the last successful
attempt and reports that provider. Attributing a failed-over request to its
intended provider would make an outage invisible in the usage table.

Health is a circuit breaker per provider, fed by both active probes and passive
observation of live traffic:

```
consecutive failures ≥ 3   → degraded   (still eligible, deprioritized)
consecutive failures ≥ 9   → unhealthy  (excluded)
rolling error rate > 50%   → degraded
```

The thresholds are asymmetric on purpose. Being too eager to mark a provider
unhealthy offloads its traffic onto its peers, which is a worse failure mode than
occasionally routing to a provider that is merely struggling. A successful probe
does not by itself close an open circuit; a probe that succeeds while live
traffic fails would otherwise flap.

**Failure mode it addresses.** Retry storms and thundering-herd failover. Bounded
budgets plus deterministic jitter keep a struggling provider from being finished
off by the clients trying to avoid it.

## Policy resolution

Policies match on model (exact or glob), request type, tenant, API key, estimated
prompt size, required capability, region, data sensitivity, endpoint scope and
streaming. All populated fields must match; empty fields are wildcards.

Matching policies are ordered by **specificity first, then priority**.
Specificity is a coarse weighted sum (`API key 1000 > tenant 500 > model 200 >
capability 100 > streaming 50 > type 25 > size 10`). The weights need only
produce a total order between obviously-different rules, not a meaningful
cardinal scale.

That ordering is what makes a rule set safe to grow: a specific rule added later
still wins over a catch-all added earlier, regardless of insertion order. Sort by
priority alone and a new broad rule silently captures traffic the specific rules
were written to protect.

The fine-grained engine (`internal/policy/engine.go`) then evaluates rules in a
fixed order — scope → batch/interactive → size → model/provider allow/deny →
region → sensitivity → cost/latency — so a denial reason is deterministic. The
first failure denies with a `deny_reason` visible to admins; warnings such as
"near budget" or "latency clamped" do not block.

**Failure mode it addresses.** A policy edit that silently reroutes traffic by
being inserted ahead of an existing rule.

## Cost control

Three mechanisms, checked before any spend:

- **Ceiling** — `max_cost_per_request_usd`, applied against the *projected* cost
  of a candidate. The engine can therefore skip an expensive candidate rather
  than discovering the overrun after the completion.
- **Rate limits** — requests and tokens per minute, enforced in Redis with an
  in-process fallback when Redis is unavailable (where limits become
  per-replica rather than global).
- **Budgets** — daily and monthly spend, per tenant and per policy.

The budget counter is keyed by a **rendered period label** (`d:2026-03-15`,
`w:2026-…-W11`, `m:2026-03`, `total`). Writing a counter under one key and reading
it under another is a real bug class here — the counter would be incremented
faithfully and never once consulted. `policy.PeriodKey` is the single function
that renders the label, and Redis sets the TTL from the label's first byte.

Cost is an estimate from the registry's price table, not a billing statement. It
is deliberately the same number budgets enforce against, so the console and the
limiter never disagree.

## Prompt shaping

`internal/shaping` normalizes system messages, compresses whitespace, trims to
history and token budgets (system messages preserved), injects prefixes and
guardrails, and sets structured-output and tool hints.

Every step appears in the plan with its token delta and is visible in traces. A
trimmed prompt reports `astrarouter.shaping`, so a short answer is never mistaken
for a model that stopped early.

## Data stores

| Store | Holds | Why not something else |
| --- | --- | --- |
| **PostgreSQL** | Tenants, API keys, providers, the model registry, policies, budgets, request log, usage, audit | The system of record needs transactions and foreign keys. A policy write that half-applies is worse than a failed write. |
| **Redis** | Rate-limit counters, budget spend, credential cache, provider health snapshot, response cache | Ephemeral, high-churn, atomic increments. Its loss degrades the gateway; it does not corrupt it. |
| **ClickHouse** | Full request traces, span timelines, provider status snapshots | Columnar, high-volume, analytical. One row per request per attempt would bloat Postgres and make dashboards slow. |
| **NATS JetStream** | Usage events, eval jobs, replay jobs, audit stream, health events | Durable consumers. A worker restart resumes rather than losing work. |

The usage row is in Postgres and the trace is in ClickHouse on purpose: usage must
be complete because it is billed, whereas traces are high-volume and analytical.
They duplicate a few fields for that reason.

**Degradation policy.** Each dependency declares whether it is `required`:

- Postgres down → the gateway reports itself **unready**, so a load balancer
  drains it. Authentication and policy reads genuinely cannot proceed.
- Redis down → **degraded**: rate limits fall back to per-process, caching
  stops. Reported in `/ready` but readiness does **not** fail.
- ClickHouse down → analytics writes are dropped and counted
  (`astrarouter_async_dropped_total`). Inference is unaffected.
- NATS down → async work is dropped and counted. Inference is unaffected.

## Telemetry

Prometheus metrics are scrape-friendly and cardinality-bounded by construction:
tenants, providers and models are labels; request ids, user ids and prompts never
are. Nothing per-request is a label, which is the single most common way an
observability stack is destroyed.

Metric namespaces: `astrarouter_gateway_*`, `astrarouter_provider_*`,
`astrarouter_routing_*`, `astrarouter_usage_*`, `astrarouter_cache_*`,
`astrarouter_policy_*`, `astrarouter_auth_*`, `astrarouter_async_*`, plus
`astrarouter_build_info`. The workers share the prefix with
`astrarouter_worker_*`, so one scrape config and one dashboard cover both.

Traces are exported over OTLP to the collector, which fans out to a file sink by
default and to a real backend (Tempo, Jaeger, another collector) by uncommenting
one exporter. The collector is the seam: swapping trace backends is never a Go
change.

The record pipeline is **asynchronous and lossy by design**. It batches to
ClickHouse and Postgres, counts what it drops, and never blocks a response on a
write. `astrarouter_async_queue_depth` and the drop counter are what make that
trade-off visible rather than invisible.

**Failure mode it addresses.** A slow analytics write adding latency to every
request; and the corruption of the observability stack by an unbounded label.

## Configuration

```
built-in defaults  <  config file  <  AR_* environment
```

The environment mapping is written out explicitly in `internal/config/env.go`
rather than derived by reflecting over struct tags. That is more code, but the
set of operator-facing variables is discoverable by reading one file, and
renaming a struct field cannot silently change the interface.

Validation is fail-fast where a mistake is dangerous and permissive where it is
merely unwise:

- production + scoped admin + no bootstrap key → **refuse to start**
- traces enabled + no OTLP endpoint → **refuse to start**
- CORS wildcard in production → **refuse to start**
- an unreachable optional dependency → start, report unready or degraded

`astrarouter config` prints the resolved configuration with every secret
redacted. Redaction is applied to the *copy*, so the running config is never
mutated by being printed.

## Security

- **Key storage.** Only a SHA-256 digest is stored; the plaintext is returned
  exactly once at creation. A plain hash (not bcrypt/argon2) is correct here
  because the token is 256 bits of CSPRNG output — there is no dictionary to
  attack and the gateway must verify without a key-stretching cost.
- **Scopes.** `inference`, `usage:read`, `models:read`, `policies:admin`,
  `providers:admin`, `keys:admin`, `tenants:admin`, and `*`. Admin routes narrow
  further by method and path, so a read scope cannot perform a write.
- **Anonymous access** is a development affordance only and is rejected outright
  in production by validation. Admin surfaces never fall back to it, even in
  development: an unauthenticated read of every tenant's usage would be a serious
  exposure.
- **Trusted proxies.** `X-Forwarded-For` is honoured only from configured CIDRs.
  Accepting it from anywhere lets a client forge its own source address in logs
  and rate-limit keys.
- **Log redaction.** A configurable header deny-list plus value redaction in the
  workers, where captured prompts would otherwise be the most likely path for
  personal data to escape. Request bodies are not logged by default.
- **Dashboard credential.** The browser never holds the admin key. Next route
  handlers proxy `/admin/v1/*` server-side, so CORS never has to be widened to
  expose administrative routes.

## Repository layout

```
cmd/astrarouter/          CLI: serve | migrate | config | version
internal/
  domain/                types and rules; no I/O
  config/                defaults, file + env loading, validation, redaction
  providers/             one adapter per provider kind, plus the HTTP client
  routing/               the engine, executor, health tracker and catalogue
  policy/                resolution, rate limiting and budgets
  auth/                  API key verification and caching
  storage/               Postgres, Redis, ClickHouse, NATS; embedded migrations
  telemetry/             Prometheus metrics, OTLP traces, the async record pipeline
  classifier/            rules-first task classification
  shaping/               prompt normalization and trimming
  cache/                 exact, prefix and semantic response cache
  scoring/               explainable provider and model scores
  guardrails/            kill switches, caps, forced circuits
  tools/                 registry, single-invocation execution, bounded loop
  eval/, replay/, feedback/, analytics/
  tunnel/                Cloudflare quick-tunnel supervision
  admin/                 validation, credential sealing, probing, sync
  api/                   HTTP surface: inference, health, admin
  bootstrap/             wires everything together and applies the catalogue
workers/                 Python intelligence tier (scoring, analysis, rollups)
dashboard/               Next.js operator console
deploy/                  Dockerfiles, otel, loki, promtail, grafana, prometheus, systemd
```

---

Related: [Overview](overview.md) · [Routing](routing.md) · [Database](database.md) · [Observability](observability.md) · [Back to README](../README.md)