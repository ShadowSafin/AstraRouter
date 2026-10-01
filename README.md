# CoreRouter

An AI inference gateway and control plane. CoreRouter sits between your
applications and the model providers, presents a single OpenAI-compatible API,
and decides — per request, per policy — which provider serves it, what happens
when that provider fails, and what it cost.

It is built to be operated: every routing decision is recorded, every provider
is health-checked, spend is metered and capped, and the whole thing exposes
Prometheus metrics, OpenTelemetry traces and structured logs out of the box.

```
                    ┌─────────────────────────────────────────────────────────────┐
   client ──────────▶  gateway  :8080      │  workers              │  dashboard    │
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

## What is in Phase 1

| Area | Delivered |
| --- | --- |
| Gateway | OpenAI-compatible `/v1/chat/completions` (streaming and non-streaming), `/v1/models` |
| Adapters | OpenAI, Anthropic Messages, Ollama, vLLM, and any OpenAI-compatible server |
| Routing | `priority`, `weighted`, `lowest_cost`, `lowest_latency`, `highest_quality`; retry, per-attempt and total timeouts, fallback chains |
| Health | Active probes plus passive observation, with a circuit breaker per provider |
| Policy | Match by model, request type, tenant, key, prompt size, capability; ordered by specificity then priority |
| Cost control | Per-request ceilings, request/token rate limits, daily and monthly budgets |
| Auth | SHA-256-hashed API keys with scopes, revocable, cached |
| Observability | Prometheus, OTLP traces, JSON logs, ClickHouse analytics, Grafana dashboards and alert rules |
| Intelligence tier | Python workers: candidate scoring, prompt analysis, telemetry rollups, optional LLM judge |
| Console | Next.js dashboard for usage, requests, errors, providers, models, policies, budgets, tenants and keys |
| Deployment | Docker Compose and a native systemd install |

Not in Phase 1: response caching is configured and schema-ready but not enabled,
`/v1/completions` and `/v1/embeddings` return `501`.

## What is new in Phase 2

Phase 2 turns the router into a policy-driven control plane. See
[`docs/phase2.md`](docs/phase2.md) for the full design.

| Area | Delivered |
| --- | --- |
| Policy engine | Tenant/key/endpoint rules, model+provider allow/deny, cost/latency ceilings, region and sensitivity constraints, batch vs interactive — with a `PolicyDecision` the router consumes directly |
| Task classification | Deterministic rules-first classifier (`chat`, `coding`, `summarization`, `extraction`, `reasoning`, `translation`, `tool-use`, `structured_output`, `long_context`, `high_priority_interactive`, `batch_offline`) in Go, mirrored in Python |
| Intelligent routing | Task-, cost-, latency-, capability- and score-aware ordering with guardrail filtering and derived timeout/retry/shaping strategies |
| Prompt shaping | Normalization, compression, context trimming, history summarization, structured/tool prompting, guardrail injection, provider adaptation — all visible in traces |
| Caching | Exact, prefix and semantic (embedding-assisted) tiers on Redis with bypass for sensitive requests, stats and invalidation |
| Provider scoring | Explainable success/latency/cost/feedback blends with per-task breakdowns |
| Eval + replay | Async replay jobs over NATS, offline comparisons, golden cases, regression flags, dashboard reports |
| Guardrails | Provider kill switches, tenant emergency overrides, hard budget caps, fallback blocks, forced circuits, visible deny reasons |
| Dashboard | Scores, cache analytics, replay/eval, endpoints, audit log, kill/revive, explain view |
| Observability | Classifier/shaping/scoring/guardrail/eval metrics, `request_intelligence` analytics, per-request explanations |

Every request now flows: authenticate → load policy → classify → shape → cache
→ health/scores → route → execute → fall back → persist. The gateway explains
why a route was chosen and why others were rejected.

## Quick start

### Docker Compose

```bash
cp .env.example .env
# Set CR_ADMIN_KEY to something you generate: openssl rand -hex 24
$EDITOR .env

docker compose up -d --build
```

If port 8080 is already taken on the host, keep the container port and move
the host port instead (gateway config is unchanged):

```bash
# in .env
GATEWAY_PORT=18080
NEXT_PUBLIC_COREROUTER_API_URL=http://localhost:18080
```

Then:

| Service | URL |
| --- | --- |
| Gateway | <http://localhost:8080> (`GATEWAY_PORT` when overridden) |
| Dashboard | <http://localhost:3000> |
| Grafana | <http://localhost:3001> (admin / the password in `.env`) |
| Prometheus | <http://localhost:9090> |
| Workers metrics | <http://localhost:9101/metrics> |

### Verify it runs (Phase 2.5 smoke test)

```bash
# Windows
powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1
# Linux / macOS
bash scripts/smoke.sh
```

The script checks gateway `/health` + `/ready`, providers/policies/models,
dashboard home + its gateway proxy, workers metrics, then mints a demo API key
(saved to `.smoke_key`, git-ignored), sends a sample `POST
/v1/chat/completions`, and confirms the request shows up in `/admin/v1/requests`
and in Prometheus metrics. 10 checks, no provider keys needed.

Without upstream provider keys the sample call is expected to fail *after*
routing (e.g. upstream `401` from OpenAI): that still proves the full pipeline
ran — auth, policy, classification, shaping, routing, execution and recording —
and the failure itself is visible in the request log. With real keys in
`OPENAI_API_KEY` / `ANTHROPIC_API_KEY` the same call returns `200`.

Seed data on first boot (from `config.example.yaml` via the bootstrap seeder):
one `default` tenant, 3 providers, 4 models, 5 routing policies. API keys are
never seeded — the smoke test mints one through the admin API.

### Native

See [`docs/native-install.md`](docs/native-install.md). The short version:

```bash
make build
sudo install -m 0755 bin/corerouter /usr/local/bin/corerouter
sudo install -m 0640 config.example.yaml /etc/corerouter/config.yaml
sudo systemctl enable --now corerouter
```

## Verify it works

Manual equivalents of what `scripts/smoke.ps1` / `scripts/smoke.sh` automate
(`GATEWAY` below is `http://localhost:8080`, or your `GATEWAY_PORT`):

```bash
# Public liveness (no credential required).
curl -s $GATEWAY/health | jq

# Readiness tells you whether a load balancer should send traffic here.
# Checks postgres + redis + that at least one provider has an adapter.
curl -s $GATEWAY/ready | jq

# Providers, policies and models as the dashboard sees them ($CR_ADMIN_KEY from .env).
curl -s $GATEWAY/admin/v1/providers -H "Authorization: Bearer $CR_ADMIN_KEY" | jq '.providers[] | {name, status, adapter_ready}'
curl -s $GATEWAY/admin/v1/policies -H "Authorization: Bearer $CR_ADMIN_KEY" | jq '.policies[] | .name'

# Mint a demo key (plaintext is returned once; keep it in .smoke_key).
TENANT=$(curl -s $GATEWAY/admin/v1/tenants -H "Authorization: Bearer $CR_ADMIN_KEY" | jq -r '.tenants[0].id')
curl -s $GATEWAY/admin/v1/keys -H "Authorization: Bearer $CR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d "{\"tenant_id\":\"$TENANT\",\"name\":\"demo\",\"scopes\":[\"inference\"]}" | jq

# A completion, exactly as an OpenAI SDK client would send it.
curl -s $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $COREROUTER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' | jq

# Confirm it was recorded (usage row + request log + metrics).
curl -s "$GATEWAY/admin/v1/requests?limit=3" -H "Authorization: Bearer $CR_ADMIN_KEY" | jq
curl -s $GATEWAY/metrics | grep -E 'corerouter_gateway_requests_total|corerouter_async_flushed_total'
```

A response carries a `corerouter` metadata block naming the policy, the strategy,
the provider that actually answered, how many attempts it took, and the estimated
cost. See [`docs/api.md`](docs/api.md).

## Repository layout

```
cmd/corerouter/          CLI: serve | migrate | config | version | health
internal/
  domain/                types and rules; no I/O
  config/                defaults, file + env loading, validation, redaction
  providers/             one adapter per provider kind, plus the HTTP client
  routing/               the engine, executor, health tracker and catalogue
  policy/                resolution, rate limiting and budgets
  auth/                  API key verification and caching
  storage/               Postgres, Redis, ClickHouse, NATS; embedded migrations
  telemetry/             Prometheus metrics, OTLP traces, the async record pipeline
  api/                   HTTP surface: inference, health, admin
  bootstrap/             wires everything together and applies the catalogue
workers/                 Python intelligence tier (scoring, analysis, rollups)
dashboard/               Next.js operator console
deploy/                  Dockerfiles, compose, otel, loki, promtail, grafana, prometheus, systemd
docs/                    architecture, native install, API reference
```

## Configuration

Configuration is layered, lowest precedence first:

1. **Built-in defaults** — a config file is optional; the defaults boot.
2. **A config file** — `--config`, `CR_CONFIG_FILE`, or the search order
   (`corerouter.yaml`, `config/corerouter.yaml`, `/etc/corerouter/config.yaml`, …).
3. **Environment variables** — every `CR_*` variable wins.

[`config.example.yaml`](config.example.yaml) documents every setting. To see the
resolved configuration with secrets redacted:

```bash
corerouter config
```

Secrets are always referenced, never embedded: a provider names an environment
variable (`api_key_env`) rather than carrying a key, which keeps the config file
committable.

## Development

```bash
make help            # what every target does
make test            # Go + Python suites
make lint            # go vet, golangci-lint, ruff, mypy
make dashboard-install dashboard-typecheck
make up              # the whole stack, if Docker is available
```

| Suite | Command | Count |
| --- | --- | --- |
| Go | `go test ./...` | domain, config, providers, routing, policy, auth, api |
| Python | `cd workers && python -m unittest discover -s tests -t .` | 162 |
| Dashboard | `cd dashboard && npm run typecheck` | — |

## Documentation

- [`docs/architecture.md`](docs/architecture.md) — how a request flows, why the
  datastores are split, and how each failure mode is handled.
- [`docs/native-install.md`](docs/native-install.md) — build, datastores,
  migrations, systemd, upgrade.
- [`docs/api.md`](docs/api.md) — the inference and administrative APIs.

## License

Proprietary. All rights reserved.
