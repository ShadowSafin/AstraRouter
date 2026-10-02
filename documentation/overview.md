# Overview

AstraRouter is an AI inference gateway and control plane. It sits between your
applications and the model providers you use, presents a single
OpenAI-compatible API, and decides — per request, per policy — which provider
serves the call, what happens when that provider fails, and what it cost.

It is built to be operated. Every routing decision is recorded, every provider is
health-checked, spend is metered and capped, and the whole system exposes
Prometheus metrics, OpenTelemetry traces and structured logs out of the box.

## The problem

Teams end up with an inference stack they did not choose to have. Something like
this is common by the time four applications are live:

- **Three SDK integrations.** Each service hardcodes a different provider and its
  own copy of the error handling, so a `429` means something slightly different
  in each.
- **No failover.** One upstream hiccup becomes an outage, because nothing knows
  there is a second provider configured.
- **No visibility.** "What did we spend last week, and which model produced the
  bad answers?" is answered by reading invoices and guessing.
- **No ceiling.** A loop bug in one service produces a bill nobody notices until
  it arrives.
- **No policy.** Which model a request reaches is decided by whichever provider
  was convenient when the code was written, not by what the request needs.

Each of these is individually small. Together they are an operating burden that
competes with the product.

## What AstraRouter does about it

**One endpoint.** `POST /v1/chat/completions`, OpenAI-compatible. Any client that
speaks OpenAI works by changing its base URL. The `astrarouter` metadata block
rides alongside the standard fields and is ignored by clients that do not know
about it.

**One routing decision per request.** Providers are registered with their real
capabilities and prices. A routing policy says which of them a request may reach,
in what order, and what to do when one fails. The decision is computed from the
request plus observable state, and recorded so you can see why it was made.

**Failure is a first-class path.** Retry, fallback, circuit breaking and timeouts
are bounded and configured per policy. When a provider is unhealthy it stops
receiving traffic; when it recovers it comes back. A request that fails over
reports who actually answered, which is the field that differs from the intended
provider exactly when you need it.

**Spend is enforced, not observed.** Per-request ceilings, rate limits, and daily
and monthly budgets are checked before the call, not reconciled afterwards.

**Everything is explainable.** Each request carries a task classification, a
policy verdict, a cache decision, a prompt-shaping plan and a routing reason.
`/admin/v1/requests/{id}/explain` returns all of it in one payload.

```
                    ┌─────────────────────────────────────────────────────────────┐
    client ─────────▶  gateway  :8080      │  workers              │  dashboard    │
    OpenAI SDK       │  · auth / API keys   │  · scoring / eval     │  · usage      │
    curl             │  · policy resolution │  · prompt analysis    │  · providers  │
                     │  · routing + retry   │  · telemetry rollups  │  · policies   │
                     │  · fallback chains   │  · judge (optional)   │  · budgets    │
                     │  · rate + budgets    │                       │  · keys       │
                     └───────┬──────────────┴───────────┬───────────┴───────────────┘
                             │                          │
               ┌─────────────┴────────────┐             │
               ▼                          ▼             ▼
         PostgreSQL                   Redis           NATS (JetStream)
         system of record        limits, cache,       usage events, eval jobs,
                                 health state         audit stream
               │
               ▼
         ClickHouse  ── usage, traces, request logs ──▶ OTel collector ──▶ Prometheus / Loki / Grafana
```

## What is delivered

| Area | Capability |
| --- | --- |
| Gateway | OpenAI-compatible `/v1/chat/completions` (streaming and buffered), `/v1/models` |
| Adapters | OpenAI, Anthropic Messages, Ollama, vLLM, and any OpenAI-compatible server |
| Routing | `priority`, `weighted`, `lowest_cost`, `lowest_latency`, `highest_quality`; retry, per-attempt and total timeouts, fallback chains |
| Policy | Match on model, request type, tenant, key, prompt size, capability, region, endpoint; ordered by specificity then priority |
| Control plane | Full CRUD for providers, models, tenants, keys and policies through the admin API and the dashboard, with no restarts |
| Health | Active probes plus passive observation, with a circuit breaker per provider |
| Cost control | Per-request ceilings, request and token rate limits, daily and monthly budgets |
| Caching | Exact, prefix and semantic tiers over Redis, policy-aware and invalidatable |
| Tools | Client-executed by default; bounded gateway-side execution behind a policy when you want it |
| Guardrails | Kill switches, tenant emergency overrides, hard budget caps, forced circuit states |
| Eval and replay | Replay recorded requests across providers, score outputs deterministically, flag regressions |
| Auth | SHA-256-hashed API keys with scopes, revocation and an invalidating cache |
| Observability | Prometheus, OTLP traces, JSON logs, ClickHouse analytics, Grafana dashboards and alert rules |
| Console | Next.js dashboard for usage, requests, errors, providers, models, policies, budgets, tenants and keys |
| Deployment | Docker Compose, native systemd, and temporary Cloudflare tunnels |

Not implemented: `POST /v1/completions`, `/v1/embeddings` and `/v1/responses`
return `501` with code `not_implemented`, so a client gets a clear answer rather
than a `404` that reads as a misconfigured base URL.

## Who it is for

**Application engineers** point an SDK at the gateway and stop caring which
provider serves them.

**Platform and SRE teams** get a control plane: change routing, cap spend, kill a
provider, without editing config files or restarting anything.

**Teams with several providers** get one place to compare them on latency, cost
and observed quality, and to shift traffic without touching application code.

## Where to go next

- [Getting started](getting-started.md) — run it and make a call.
- [Architecture](architecture.md) — how the pieces fit and why.
- [Providers](providers.md) — register your first provider.
- [Routing](routing.md) — control which provider answers.

---

Related: [Getting started](getting-started.md) · [Architecture](architecture.md) · [FAQ](faq.md) · [Glossary](glossary.md) · [Back to README](../README.md)