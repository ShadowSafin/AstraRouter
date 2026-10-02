# Getting started

From an empty directory to a working inference call. Docker Compose is the
shortest path; for a host install see [Installation: native](installation/native.md).

Expect about five minutes and a provider API key.

## Prerequisites

| Component | Version | Required? |
| --- | --- | --- |
| Docker with Compose v2 | any current | Yes, for this path |
| A provider API key | — | Yes, for a real answer |

PostgreSQL, Redis, ClickHouse and NATS are started for you by Compose.

## 1. Configure

```bash
git clone https://github.com/shadowsafin/corerouter.git
cd corerouter

cp .env.example .env
```

Generate an admin key and put it in `.env`:

```bash
openssl rand -hex 24
```

```dotenv
CR_ADMIN_KEY=<the value you just generated>
OPENAI_API_KEY=<your provider key>
```

Secrets are referenced by environment variable name, never embedded. A provider
record names `OPENAI_API_KEY`; the secret stays in your environment and out of
version control.

## 2. Start the stack

```bash
docker compose up -d --build
```

If port 8080 is taken on your host, move the host port rather than the container
port — gateway configuration does not change:

```dotenv
GATEWAY_PORT=18080
NEXT_PUBLIC_COREROUTER_API_URL=http://localhost:18080
```

Then `GATEWAY=http://127.0.0.1:${GATEWAY_PORT:-8080}` for the rest of this page.

| Service | URL |
| --- | --- |
| Gateway | <http://127.0.0.1:8080> |
| Dashboard | <http://127.0.0.1:3000> |
| Grafana | <http://127.0.0.1:3001> |
| Prometheus | <http://127.0.0.1:9090> |
| Worker metrics | <http://127.0.0.1:9101/metrics> |

## 3. Check it came up

```bash
curl -s $GATEWAY/health | jq
curl -s $GATEWAY/ready  | jq
```

`/health` is liveness and deliberately does not touch dependencies, so a database
blip cannot cause an orchestrator to restart every replica. `/ready` is what a
load balancer asks, and it does check them.

```json
{
  "status": "ready",
  "checks": { "postgres": "ok", "redis": "ok", "providers": "2 configured" }
}
```

`providers: 0 configured` means the gateway will refuse traffic: at least one
provider must have a working adapter, which usually means a missing credential.

## 4. Mint an API key

On first boot the gateway seeds one tenant, a small provider catalogue, and some
routing policies. API keys are never seeded — mint one:

```bash
export CR_ADMIN_KEY=<from .env>
TENANT=$(curl -s $GATEWAY/admin/v1/tenants \
  -H "Authorization: Bearer $CR_ADMIN_KEY" | jq -r '.tenants[0].id')

curl -s $GATEWAY/admin/v1/keys \
  -H "Authorization: Bearer $CR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d "{\"tenant_id\":\"$TENANT\",\"name\":\"first-key\",\"scopes\":[\"inference\"]}" | jq
```

The plaintext key is returned **exactly once** and stored only as a SHA-256
digest. Save it now:

```bash
export CR_KEY=cr_live_...
```

## 5. Make a call

```bash
curl -s $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' | jq
```

```json
{
  "id": "cmpl-...",
  "object": "chat.completion",
  "choices": [{ "index": 0, "message": { "role": "assistant", "content": "hello" }, "finish_reason": "stop" }],
  "usage": { "prompt_tokens": 9, "completion_tokens": 2, "total_tokens": 11 },
  "corerouter": {
    "request_id": "5e0804c7-...",
    "provider": "openai",
    "requested_model": "gpt-4o-mini",
    "routed_model": "gpt-4o-mini",
    "fallback_used": false,
    "latency_ms": 912,
    "estimated_cost_usd": 0.0000026
  }
}
```

The `corerouter` block is namespaced and additive. OpenAI clients ignore it.

### From an SDK

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

```js
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://127.0.0.1:8080/v1", apiKey: "cr_live_..." });
const answer = await client.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Summarize refunds in one line." }],
});
console.log(answer.choices[0].message.content);
```

### Streaming

```bash
curl -N $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],
       "stream":true,"stream_options":{"include_usage":true}}'
```

Frames are standard SSE: `data: {...}` … `data: [DONE]`. A failure after the
stream has started arrives as an `event: error` frame followed by `[DONE]`.

## 6. Register your own provider

The seeded catalogue is a starting point. To add a real one, open the dashboard at
<http://127.0.0.1:3000> → **Providers**, or use the API:

```bash
curl -s $GATEWAY/admin/v1/providers \
  -H "Authorization: Bearer $CR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-provider","kind":"openai","base_url":"https://api.openai.com/v1","api_key_env":"OPENAI_API_KEY"}' | jq
```

Tick **Sync models** to discover the provider's remote models in the same call,
then run **Test** to confirm connectivity. See [Providers](providers.md) for
credentials, kinds and precedence.

## 7. Confirm it was recorded

```bash
curl -s "$GATEWAY/admin/v1/requests?limit=3" -H "Authorization: Bearer $CR_ADMIN_KEY" | jq
curl -s $GATEWAY/metrics | grep corerouter_gateway_requests_total
```

Or open the dashboard and look at the Overview page — usage, cost and provider
health update as traffic arrives.

## Sign in to the dashboard

Open <http://127.0.0.1:3000> for the first time and you get a setup screen rather
than a login form. It creates the single console administrator and then closes
itself permanently:

```
Create the admin account
  Username          admin
  Password          •••••••••••••••••••  Strong
  Confirm password  •••••••••••••••••••
                   [ Create admin account ]
```

There are no default credentials. The password is stored as an Argon2id hash and
is never written down. After this, every visit shows a normal login form, and
failed attempts lock the account temporarily.

> If you lose the credentials, recovery is a database operation. There is no reset
> link and no default account, deliberately — a recovery path is a backdoor.

Details: [Dashboard → Authentication](dashboard.md#authentication)

## Verify the whole stack automatically

The smoke suite checks liveness, readiness, the admin surface, the dashboard
proxy, worker metrics, a full management cycle and the tool plane. It needs no
provider keys:

```bash
# Windows
powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1

# Linux / macOS
bash scripts/smoke.sh
```

Without a real provider key the sample completion is expected to fail *after*
routing, with an upstream `401`. That still proves the whole pipeline ran —
auth, policy, classification, shaping, routing, execution, recording — and the
failure appears in the request log. With a real key it returns `200`.

## Common next steps

| Goal | Document |
| --- | --- |
| Control which provider answers | [Routing](routing.md) |
| Reuse identical responses | [Caching](caching.md) |
| Cap spend per tenant | [Routing](routing.md#cost-control) |
| Sign in to the console | [Dashboard](dashboard.md#authentication) |
| Let the model call tools | [Tools](tools.md) |
| Use every endpoint | [API reference](api.md) |
| Run without Docker | [Installation: native](installation/native.md) |
| Reach it from a phone or laptop elsewhere | [Cloudflare tunnel](installation/cloudflare-tunnel.md) |

---

Related: [Overview](overview.md) · [Architecture](architecture.md) · [Installation: Docker](installation/docker.md) · [Troubleshooting](troubleshooting.md) · [Back to README](../README.md)