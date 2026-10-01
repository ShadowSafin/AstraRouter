<div align="center">

# CoreRouter

**An OpenAI-compatible gateway that decides, per request, which AI provider serves it — and proves it.**

Routing · failover · caching · policy · budgets · full request lineage

[Quick start](#quick-start) · [Documentation](documentation/) · [Troubleshooting](documentation/troubleshooting.md)

</div>

---

## The problem

Inference stacks rarely get chosen. They accrete. By the time a few applications
are live you have SDK integrations per service, no failover when an upstream has a
bad hour, no answer to "what did we spend, and which model produced the bad
answers?", and a bill that arrives after the runaway loop rather than before it.

## What CoreRouter is

A gateway and control plane that sits between your applications and your model
providers. It presents **one OpenAI-compatible endpoint** and decides, per request
and per policy, which provider serves it, what happens when that provider fails,
and what it cost.

Changing providers is a dashboard edit instead of a deployment.

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

## Key features

**Routing** — Five strategies (`priority`, `weighted`, `lowest_cost`,
`lowest_latency`, `highest_quality`) over a registry that knows each model's real
context window, capabilities and price. Policies match on model, tenant, key,
task, size, region, sensitivity and endpoint, resolved by specificity so a
specific rule always beats a broad one.

**Reliability** — Bounded retry with deterministic jitter, ordered fallback chains,
per-provider circuit breakers fed by active probes and live traffic, and separate
budgets for connect, first-token, stream-idle, per-attempt and total time.

**Policy and cost** — Per-request ceilings checked against projected cost *before*
the call, request and token rate limits, and daily and monthly budgets. Dials, not
invoices.

**Caching** — Exact, prefix and semantic tiers that are tenant-isolated by
construction, policy-aware about what may be reused, and invalidatable by scope
with an audit trail. Off by default.

**Tools** — Client-executed by default, which is what agentic applications expect.
Optional bounded gateway-side execution with operator-owned limits, an allow-listed
schema subset, and a durable step trace per run.

**Observability** — Every request carries a task classification, policy verdict,
cache decision, shaping plan and routing reason, readable at
`/admin/v1/requests/{id}/explain`. Prometheus metrics, OTLP traces, JSON logs,
Grafana dashboards and alert rules ship in the box.

**Control plane** — Providers, models, tenants, keys, policies, endpoints,
budgets, overrides, tools and tunnels, all managed through the dashboard or the
admin API, all effective without a restart, all audited.

## Deployment modes

| Mode | Best for | Start here |
| --- | --- | --- |
| **Docker Compose** | Trying it, demos, a single host | [docker.md](documentation/installation/docker.md) |
| **Native + systemd** | A machine you intend to operate | [native.md](documentation/installation/native.md) |
| **Cloudflare tunnel** | Temporary public access from elsewhere | [cloudflare-tunnel.md](documentation/installation/cloudflare-tunnel.md) |

All three share one configuration model and the same code.

## Quick start

**Prerequisites:** Docker with Compose v2, and a provider API key.

```bash
git clone https://github.com/shadowsafin/corerouter.git
cd corerouter

cp .env.example .env
openssl rand -hex 24        # paste into CR_ADMIN_KEY in .env
$EDITOR .env                # add CR_ADMIN_KEY and your provider key

docker compose up -d --build
```

| Service | URL |
| --- | --- |
| Gateway | <http://127.0.0.1:8080> |
| Dashboard | <http://127.0.0.1:3000> |
| Grafana | <http://127.0.0.1:3001> |
| Prometheus | <http://127.0.0.1:9090> |

If port 8080 is taken, move the host port rather than the container port — gateway
configuration does not change:

```dotenv
GATEWAY_PORT=18080
NEXT_PUBLIC_COREROUTER_API_URL=http://localhost:18080
```

Check it came up, then mint a key and make a call:

```bash
export GATEWAY=http://127.0.0.1:8080
curl -s $GATEWAY/ready | jq

export CR_ADMIN_KEY=<from .env>
TENANT=$(curl -s $GATEWAY/admin/v1/tenants -H "Authorization: Bearer $CR_ADMIN_KEY" | jq -r '.tenants[0].id')
export CR_KEY=$(curl -s $GATEWAY/admin/v1/keys \
  -H "Authorization: Bearer $CR_ADMIN_KEY" -H 'Content-Type: application/json' \
  -d "{\"tenant_id\":\"$TENANT\",\"name\":\"first-key\",\"scopes\":[\"inference\"]}" | jq -r .plaintext)

curl -s $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' | jq
```

Verify the whole stack end to end — no provider key needed:

```bash
bash scripts/smoke.sh                                        # Linux / macOS
powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1  # Windows
```

Full walkthrough: **[documentation/getting-started.md](documentation/getting-started.md)**

## API

Any OpenAI client works by changing the base URL.

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="cr_live_...")

answer = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Summarize refunds in one line."}],
    max_tokens=256,
)
print(answer.choices[0].message.content)
```

Every response carries a namespaced `corerouter` block. OpenAI SDKs ignore it; you
can read it to know what actually happened:

```json
{
  "choices": [{ "index": 0, "message": { "role": "assistant", "content": "…" }, "finish_reason": "stop" }],
  "usage": { "prompt_tokens": 26, "completion_tokens": 81, "total_tokens": 107 },
  "corerouter": {
    "request_id": "5e0804c7-…",
    "provider": "openai-prod",
    "requested_model": "gpt-4o-mini",
    "routed_model": "gpt-4o-mini",
    "fallback_used": false,
    "latency_ms": 2851,
    "estimated_cost_usd": 0.0000026
  }
}
```

`provider` is who **answered**, not who was chosen first — the two differ exactly
on the requests you most need to trace. An answer that was cut short says so:

```json
"completion": {
  "finish_reason": "length",
  "truncated": true,
  "reason": "max_tokens",
  "requested_tokens": 256,
  "applied_tokens": 256,
  "budget_ms": 300000
}
```

Callers can steer a single request without an operator editing a policy:

```bash
curl -s $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H "X-CoreRouter-No-Fallback: true" \
  -H "X-CoreRouter-Max-Cost-USD: 0.05" \
  -H "X-CoreRouter-Debug: true" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

Complete reference: **[documentation/api.md](documentation/api.md)**

## Dashboard

<http://127.0.0.1:3000> — the operator console.

| Page | What it is for |
| --- | --- |
| **Overview** | Volume, success rate, latency, spend, provider health |
| **Requests** | Every request, with a per-request **explain** view |
| **Errors** | Failures with a `by_code` rollup |
| **Providers** | Add, test, sync models, probe, kill or revive |
| **Models** | Context window, pricing, capabilities, priority |
| **Policies** | Routing rules, targets, limits, timeouts, fallback |
| **Endpoints** | Named routing scopes with copy-paste examples |
| **Budgets** | Daily and monthly ceilings, hard per-request caps |
| **Keys** | Mint, rotate and revoke |
| **Scores** | Provider and model quality with explanations |
| **Cache** | Hit rates, bypass reasons, per-scope rules, scoped flushes |
| **Tools** | Registry, policies, and agent runs with step traces |
| **Tunnels** | Temporary public access |

The browser never holds the admin key: Next route handlers proxy `/admin/v1/*`
server-side.

Details: **[documentation/dashboard.md](documentation/dashboard.md)**

## Providers

Five kinds ship built in. Adding one is a form submission or an API call.

| Kind | Base URL | Extra sampling controls |
| --- | --- | --- |
| `openai` | `https://api.openai.com/v1` | — |
| `anthropic` | `https://api.anthropic.com` | `top_k` |
| `ollama` | `http://host:11434` | `top_k`, `min_p`, `repeat_penalty` |
| `vllm` | your server | `seed`, `top_k`, `min_p`, `repeat_penalty` |
| `openai_compatible` | your server | `top_k`, `min_p`, `repetition_penalty` |

```bash
curl -s $GATEWAY/admin/v1/providers \
  -H "Authorization: Bearer $CR_ADMIN_KEY" -H 'Content-Type: application/json' \
  -d '{"name":"openai-prod","kind":"openai","base_url":"https://api.openai.com/v1",
       "api_key_env":"OPENAI_API_KEY","sync_models":true}' | jq
```

Secrets are referenced, never embedded: a provider names an environment variable,
or holds an AES-256-GCM sealed credential you write through a dedicated endpoint.
Model discovery fills the registry from the provider's remote catalogue while
preserving your local pricing, aliases and disables.

Details: **[documentation/providers.md](documentation/providers.md)**

## Configuration

Three layers, lowest precedence first:

1. **Built-in defaults** — the stack boots with no config file.
2. **A config file** — [`config.example.yaml`](config.example.yaml) documents every setting.
3. **Environment variables** — every `CR_*` variable wins.

```bash
corerouter config        # print the resolved configuration, secrets redacted
```

Redaction is applied to a copy, so printing the configuration never mutates the
running one.

## Documentation

| | |
| --- | --- |
| **[Overview](documentation/overview.md)** | What it is for and the problem it solves |
| **[Getting started](documentation/getting-started.md)** | Empty directory to working call in five minutes |
| **[Architecture](documentation/architecture.md)** | How the pieces fit and why |
| **[Installation](documentation/installation/docker.md)** | [Docker](documentation/installation/docker.md) · [Native](documentation/installation/native.md) · [Tunnel](documentation/installation/cloudflare-tunnel.md) |
| **[Providers](documentation/providers.md)** | Kinds, credentials, testing, health |
| **[Routing](documentation/routing.md)** | Strategies, policies, fallback, cost control |
| **[Caching](documentation/caching.md)** | Tiers, policy, invalidation, visibility |
| **[Tools](documentation/tools.md)** | Registry, execution modes, safety, bounds |
| **[API reference](documentation/api.md)** | Every endpoint, header and error code |
| **[Dashboard](documentation/dashboard.md)** | Pages and workflows |
| **[Database](documentation/database.md)** | Tables, migrations, persistence boundaries |
| **[Observability](documentation/observability.md)** | Metrics, traces, logs, alerting |
| **[Troubleshooting](documentation/troubleshooting.md)** | Symptoms, causes, checks |
| **[FAQ](documentation/faq.md)** | Short practical answers |
| **[Glossary](documentation/glossary.md)** | What each term means here |
| **[Changelog](documentation/changelog.md)** | What each delivery phase added |

## Troubleshooting

Start here when something misbehaves:

| Symptom | Look at |
| --- | --- |
| Gateway will not start | The refusal message in the log; validation is specific |
| `/ready` is 503 | `checks` names the dependency; `providers: 0 configured` means no usable adapter |
| All requests fail `401` | The provider credential; run `POST /admin/v1/providers/{id}/test` |
| A provider gets no traffic | `explain` on the request, or `corerouter_provider_health` |
| Answers stop mid-sentence | `corerouter.completion` — `reason` and `budget_ms` say which limit |
| Failover did not happen | The policy's `on_error_codes` list |
| Latency is high | `corerouter_routing_candidates` — one candidate means no choice |
| Dashboard panels empty | `COREROUTER_API_URL` from the dashboard **server** |

Full guide: **[documentation/troubleshooting.md](documentation/troubleshooting.md)**

## Development

```bash
make help              # every target
make test              # Go + Python suites
make lint              # go vet, golangci-lint, ruff, mypy
make dashboard-install dashboard-typecheck
make up                # the whole stack, if Docker is available
```

| Suite | Command |
| --- | --- |
| Go | `go test ./internal/...` |
| Python | `cd workers && python -m unittest discover -s tests -t .` |
| Dashboard | `cd dashboard && npm run typecheck` |

Start reading at `internal/api/chat.go` for the request path, `internal/routing`
for the decision logic, `internal/domain` for the vocabulary.

## Contributing

Issues and pull requests are welcome. Please read
**[CONTRIBUTING.md](CONTRIBUTING.md)** first — it covers the commit style, the
test expectations, and the documentation requirement for behaviour changes.

Security reports go through **[SECURITY.md](SECURITY.md)**, not the issue tracker.

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE).

Contributions are accepted under the same terms. Apache 2.0 over MIT because it
includes an express patent grant and a patent-termination clause, which matters
for infrastructure software that companies deploy and redistribute.