# Universal inference endpoint

`POST /v1/chat/completions` is the main public face of CoreRouter: one
OpenAI-compatible endpoint any app, SDK, CLI or agent can call. The gateway
authenticates, validates, classifies, routes (with fallback), shapes, executes
and records â€” the caller sees one coherent answer.

Base URL: `http://IP:PORT/v1`. Sibling surfaces: `GET /v1/models`,
`GET /v1/models/{model}`, `GET /health`, `GET /ready`.
`POST /v1/completions`, `/v1/embeddings` and `/v1/responses` answer `501`
rather than guessing at a format the client did not ask for.

## Calling it

Authenticate with a tenant API key (minted at `/keys` or on the `/keys`
dashboard page):

```http
Authorization: Bearer cr_live_â€¦
```

Every client library that speaks OpenAI works by changing the base URL.

### Chat (standard)

```bash
curl -s http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "bynara-fast",
    "messages": [{"role": "user", "content": "Summarize refunds in one line."}],
    "temperature": 0.3,
    "max_tokens": 256
  }'
```

### Plain prompt (shorthand for non-chat clients)

```bash
curl -s http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "bynara-fast", "prompt": "Say ok.", "system": "Be terse."}'
```

`prompt` expands to a single user message and `system` to a leading system
message; `prompt` and `messages` together are `400`. Everything downstream â€”
validation, classification, shaping, caching â€” sees the same chat shape either
way. Unknown fields are rejected (`400`) so a misspelled parameter fails
loudly instead of being silently ignored.

### Streaming

```bash
curl -N http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer $CR_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "bynara-fast", "messages": [{"role": "user", "content": "Hi"}],
       "stream": true, "stream_options": {"include_usage": true}}'
```

Frames are OpenAI-compatible SSE (`data: {...}` â€¦ `data: [DONE]`), with an
`event: error` frame if the stream fails mid-flight. A terminal usage frame is
sent only when `stream_options.include_usage` is true.

## SDK examples

Python (OpenAI SDK):

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:18080/v1", api_key="cr_live_â€¦")
answer = client.chat.completions.create(
    model="bynara-fast",
    messages=[{"role": "user", "content": "Summarize refunds in one line."}],
    temperature=0.3,
    max_tokens=256,
)
print(answer.choices[0].message.content)
print(answer.usage.total_tokens)

# The corerouter attribution block rides the raw JSON alongside the
# OpenAI fields:
raw = client.chat.completions.with_raw_response.create(
    model="bynara-fast",
    messages=[{"role": "user", "content": "Hi"}],
)
print(raw.parse().model_dump()["corerouter"]["request_id"])
```

Node.js (OpenAI SDK):

```js
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "http://127.0.0.1:18080/v1", apiKey: "cr_live_â€¦" });
const answer = await client.chat.completions.create({
  model: "bynara-fast",
  messages: [{ role: "user", content: "Summarize refunds in one line." }],
});
console.log(answer.choices[0].message.content);
```

Streaming (any SSE client): consume `data:` frames until `data: [DONE]`;
each frame parses as a `chat.completion.chunk`.

## Response shape

Non-streaming success:

```json
{
  "id": "cmpl-â€¦",
  "object": "chat.completion",
  "created": 1759250000,
  "model": "ling-3.0-flash-sante-free",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "â€¦"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 26, "completion_tokens": 81, "total_tokens": 107},
  "corerouter": {
    "request_id": "5e0804c7-â€¦",
    "provider": "bynara",
    "requested_model": "bynara-fast",
    "routed_model": "ling-3.0-flash-sante-free",
    "fallback_used": false,
    "cache_hit": false,
    "latency_ms": 2851,
    "estimated_cost_usd": 0.0
  }
}
```

The default `corerouter` block is stable attribution: who answered, what was
served, at what cost. Policy names, strategies, task labels, shaping notes and
route reasons are operational details and stay hidden unless the caller sends
`X-CoreRouter-Debug: true`, which restores the full block (`trace_id`,
`policy_id`, `policy_name`, `strategy`, `attempts`, `task`, `shaping`,
`route_reason`, â€¦). The dashboard and admin API always see everything.

Failures use the OpenAI error envelope with the same block attached, so a
logged failure still correlates:

```json
{
  "error": {"message": "â€¦", "type": "permission_error", "code": "permission_error"},
  "corerouter": {"request_id": "â€¦", "provider": "bynara", "fallback_used": true}
}
```

Only the final outcome is reported: intermediate provider failures that
fallback recovered from never surface. `X-Request-ID` is echoed on every
response for support correlation.

## Routing controls (optional headers)

All optional; without them the matched policy decides.

| Header | Effect |
| --- | --- |
| `X-CoreRouter-Policy` | Pin a policy id for this request |
| `X-CoreRouter-Endpoint` | Apply a named endpoint scope (`/endpoints` page): forced model, preferred providers/models, strategy, cost/latency caps, fallback blocking. Unknown slugs are `404`, disabled scopes `403`. Scoped requests are cached per-scope (endpoint is part of the cache key). |
| `X-CoreRouter-Max-Cost-USD` | Tighten the per-request cost ceiling |
| `X-CoreRouter-Latency-Target-Ms` | Prefer lower-latency targets |
| `X-CoreRouter-No-Fallback: true` | Exactly one attempt (compliance pinning) |
| `X-CoreRouter-No-Cache: true` | Bypass the response cache |
| `X-CoreRouter-Region`, `-Sensitivity`, `-Batch` | Feed policy matching |
| `X-CoreRouter-Debug: true` | Full routing internals in the response |

Policy denials, budget exhaustion and kill-switches return `403`/`429` with an
explaining message and zero provider calls.

## Observability

Every call records: request id, trace id, tenant, key, provider, models
(requested/routed), policy, task label, shaping, latency, usage, cost,
fallback attempts and outcome â€” visible in `/requests` (with per-request
`explain`), `/usage`, `/scores`, `/audit` and the metrics endpoint. Nothing
in the request path carries secrets: credentials live only in outbound
provider headers, which the log redactor always strips.
