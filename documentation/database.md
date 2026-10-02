# Database

AstraRouter keeps state in four stores, chosen so each one is the right shape for
what it holds. This page covers what lives where, how migrations work, and the
persistence boundaries that matter when something goes wrong.

## The four stores

| Store | Role | If it is down |
| --- | --- | --- |
| **PostgreSQL** | System of record | Gateway reports **unready**; a load balancer should drain it |
| **Redis** | Limits, budgets, credential cache, response cache | **Degraded**: limits fall back to per-process, caching stops; readiness still passes |
| **ClickHouse** | Traces and analytics | Analytics writes are dropped and counted; inference unaffected |
| **NATS JetStream** | Usage events, eval and replay jobs, audit stream | Async work is dropped and counted; inference unaffected |

PostgreSQL needs transactions and foreign keys — a policy write that half-applies
is worse than a failed write. Redis needs atomic increments on high-churn
ephemeral data. ClickHouse is columnar and analytical; one row per request per
attempt would bloat Postgres and make dashboards slow. NATS gives durable
consumers, so a worker restart resumes rather than losing work.

The usage row is in Postgres and the trace is in ClickHouse on purpose: usage must
be complete because it is billed, whereas traces are high-volume and analytical.
They duplicate a few fields for that reason.

## Postgres tables

### Core

| Table | Holds |
| --- | --- |
| `tenants` | Tenant identity, slug, plan, status |
| `api_keys` | SHA-256 digest, prefix, scopes, expiry, routing policy binding |
| `providers` | Kind, base URL, status, priority, `api_key_env`, `managed_by` |
| `provider_credentials` | AES-256-GCM sealed secrets, one active per provider |
| `provider_test_results` | Connectivity, model-listing and sample-check history |
| `models` | Context window, max output, pricing, capabilities, priority, status |
| `routing_policies` | The rule sets, with targets, limits, budgets and timeouts |
| `usage_records` | Per-request tokens and estimated cost — the billable row |
| `request_logs` | Request and response metadata for the operator view |
| `audit_events` | Control-plane mutations, with before/after values |
| `budgets` | Daily and monthly spend ceilings, plus hard per-request caps |
| `provider_status_snapshots` | Health history |
| `settings` | Runtime settings |

### Intelligence

| Table | Holds |
| --- | --- |
| `policy_decisions` | The verdict per request, with the reason |
| `task_classifications` | Task label, confidence and the signals that produced it |
| `prompt_shapes` | Each shaping step and its token delta |
| `cache_entries` | Prompt hash and preview, model, provider, policy, hit counts |
| `cache_policies` | Per-scope cache rules |
| `cache_invalidations` | Flush audit: scope, target, reason, actor, removed count |
| `provider_scores`, `model_scores` | Explainable quality scores per window |
| `replay_jobs`, `evaluation_runs`, `evaluation_results` | Offline comparison |
| `feedback_events` | User-submitted scores and comments |
| `endpoints` | Named scopes with routing overrides |
| `circuit_breaker_state` | Persisted breaker state across restarts |
| `audit_overrides` | Health and routing overrides |

### Tools and tunnels

| Table | Holds |
| --- | --- |
| `tools` | The tool registry |
| `tool_policies` | Mode, bounds, allow/deny globs |
| `tool_invocations`, `tool_executions` | Every model-requested call and its execution |
| `agent_runs`, `agent_steps` | Bounded runs and their step traces |
| `tunnel_sessions` | Tunnel lifecycle: target, URL, timing, stop reason |

## ClickHouse tables

| Table | Holds |
| --- | --- |
| `astrarouter.request_traces` | One row per request, with span timings |
| `astrarouter.trace_attempts` | One row per provider attempt |
| `astrarouter.route_decisions` | Chosen target, candidates, rejections |
| `astrarouter.usage_events` | Raw token and cost events |
| `astrarouter.usage_daily` | Pre-aggregated daily rollups |
| `astrarouter.provider_status_snapshots` | Health over time |
| `astrarouter.request_intelligence` | Task, shaping, policy verdict, cache kind, scores |
| `astrarouter.provider_scores` | Score history |
| `astrarouter.eval_results` | Evaluation and replay outcomes |
| `astrarouter.cache_events` | Hit, miss and bypass events |

## Redis keys

| Pattern | Holds |
| --- | --- |
| `response:tenant:<id>:exact:<hash>` | Cached response bodies, tenant-namespaced |
| `response:tenant:<id>:prefix:<hash>` | Prefix-tier entries |
| Rate-limit counters | Requests and tokens per minute |
| Budget counters | Keyed by rendered period label (`d:…`, `w:…`, `m:…`, `total`) with TTL from the label |
| Credential cache | Resolved API keys, invalidated on revocation |

Bodies live in Redis; only metadata reaches Postgres. That keeps the system of
record small while leaving the dashboard able to inspect what is cached without
ever serving a stored body.

## NATS subjects

| Subject | Carries |
| --- | --- |
| Usage events | Per-request usage for rollups |
| Eval and replay jobs | Durable work for the workers |
| `ar.tool.run.completed` | Finished run summaries, retained 30 days in `ASTRAROUTER_EVENTS` |
| `ar.cache.invalidated` | Flush summary |
| `ar.tunnel.status` | Tunnel lifecycle |
| Audit stream | Control-plane events |

## Migrations

Migrations are embedded in the binary and applied in order. Each is recorded in
`schema_migrations` with a checksum, and a mismatch on an already-applied
migration is an **error**, not a silent re-run — a migration that changed after it
was applied means the two databases are not the same shape.

| Migration | Adds |
| --- | --- |
| `0001_init` | Core schema |
| `0002_phase2` | Intelligence tables |
| `0003_audit_phase2` | Extra audit columns |
| `0004_phase3` | Provider credentials and test results |
| `0005_phase4` | Tool registry, policies, invocations, runs, steps |
| `0006_phase5` | Cache policies and invalidation trail |
| `0007_tunnel` | Tunnel sessions |
| `0008_audit_tunnel` | Extra audit columns |
| ClickHouse `0001` | Trace and usage tables |
| ClickHouse `0002` | Intelligence tables |

### Applying them

Compose sets `AR_POSTGRES_AUTO_MIGRATE=true`, so the gateway migrates at startup.
Under change control, turn that off and migrate as a release step:

```bash
astrarouter migrate
```

The same command migrates both Postgres and ClickHouse.

## Ownership: `managed_by`

Catalogue rows carry `managed_by: bootstrap | api`. The bootstrapper applies the
configuration file **only to rows it owns**, so a provider, model or policy
edited through the API survives a deploy. To hand a row back to the file, delete
it through the API and let the seeder recreate it.

This is the mechanism that stops a dashboard edit from being silently reverted by
the next `docker compose up`.

## Persistence boundaries worth knowing

**The record pipeline is lossy on purpose.** Usage rows, traces and audit events
are written asynchronously and batched. A response never blocks on a write. The
cost is that a crash can lose the last few seconds of telemetry, which is why
`astrarouter_async_dropped_total` and `astrarouter_async_queue_depth` exist. What is
synchronous — authentication, policy, budgets — is never lossy.

**Audit covers the control plane, not inference traffic.** A provider created
through the API is audited, with before and after values. An inference request is
not: its accountability need is served by `usage_records`, and auditing every
request would dwarf the signal. An audit write failure is logged and never fails
the mutation it accompanies, because refusing a completed action would leave the
system matching neither the operator's intent nor the audit log.

**Overrides are events, not edits.** Revoking a kill switch means writing the
inverse row (`enabled: false`), so the log keeps what was true and when.

**Tool run rows are written before the first step**, because `agent_steps` has a
foreign key to `agent_runs`. A persistence failure does not fail the request, but
it is logged, counted and flagged on the event.

## Retention

| Data | Kept |
| --- | --- |
| Usage records | Until you prune them; they are the billing basis |
| Request traces in ClickHouse | TTL-managed; prune by partition |
| Audit events | Until you prune them |
| NATS `ASTRAROUTER_EVENTS` | 30 days |
| Redis response cache | `cache.response_ttl`, default `5m` |
| Budget counters | TTL derived from the period label |

Prune ClickHouse by partition rather than row-by-row; it is built for whole-part
deletes.

## Common problems

| Symptom | Cause | Fix |
| --- | --- | --- |
| `/ready` returns `503` | Postgres unreachable | Check connectivity and credentials |
| `/ready` says `redis: degraded` | Redis down | Limits are per-process; caching stopped. Fix Redis, or accept degraded mode. |
| `astrarouter_async_dropped_total` climbing | ClickHouse or NATS down, or the queue is saturated | Restore the dependency; watch queue depth |
| `migration checksum mismatch` | A migration file changed after it was applied | Restore the original file, or reconcile deliberately |
| Dashboard edits reverted on restart | The row is `bootstrap`-managed | Re-create it through the API so it becomes `api`-managed |
| A budget never trips | Counter keyed under a different label | Do not hand-roll counter keys; use `policy.PeriodKey` |

---

Related: [Architecture](architecture.md) · [Observability](observability.md) · [Providers](providers.md) · [Back to README](../README.md)