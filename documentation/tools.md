# Tools

Synapass supports tool calling. Tools are advertised to the model, the model
emits `tool_calls`, and either the client or the gateway runs them.

> **Client-executed tools are the default.** Gateway-side execution is opt-in via
> `tools.gateway_execution: true`. With it off, the gateway forwards tool
> definitions and returns the model's `tool_calls` untouched — which is exactly
> what agentic applications such as OpenCode and Claude Code need. Nothing runs
> inside the gateway unless you turn this on.

## The core loop

1. **Register tools** — `/tools` page or `POST /admin/v1/tools`.
2. **Write a tool policy** — `/tool-policies` page or `PUT /admin/v1/tool-policies`.
   Without one, every request runs under the built-in manual default.
3. **Send a request** with `tools`, and optionally
   `"tool_execution": {"mode": "automatic"}`.
4. **Read the answer** plus `synapass.tool_run`: what ran, how many steps, why
   it stopped.
5. **Inspect the trace** on `/agent-runs`, which shows the model/tool step trace
   and every invocation with its arguments and outcome.

## Request contract

Three additive fields on `POST /v1/chat/completions`. Omitting all of them is
exactly the pre-tools behaviour.

| Field | Meaning |
| --- | --- |
| `tools` | OpenAI-style tool definitions. Validated before routing: a malformed tool is a `400` naming the field, not an opaque upstream rejection. |
| `tool_choice` | `none`, `auto`, `required`, or `{"type":"function","function":{"name":"…"}}`. `required` under a disabled policy is a `403`. |
| `tool_execution` | `{"mode": "manual"}` (default) or `{"mode": "automatic"}`. The client may only narrow what policy allows. |
| `response_format` | `{"type":"json_object"}` or `{"type":"json_schema","json_schema":{…}}`. |

```bash
curl -s $GATEWAY/v1/chat/completions \
  -H "Authorization: Bearer $SYNAPASS_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role":"user","content":"What time is it?"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "now",
        "description": "current time",
        "parameters": {"type":"object","properties":{}}
      }
    }]
  }' | jq
```

The selected model must have the `tools` capability, or it is filtered out of the
candidate set before any provider is called.

## Execution modes

| Mode | Behaviour | `tool_run.status` |
| --- | --- | --- |
| **manual** (default) | The model's `tool_calls` reach the client untouched, with `finish_reason: tool_calls`. Nothing runs inside the gateway. | `client_executed` |
| **automatic**, non-streaming, gateway execution on | Safe tools run in the gateway in a bounded loop. The client gets one final answer with `finish_reason: stop`. | `gateway_executed` |
| **automatic**, streaming, gateway execution on | Refused with a `400`. Streamed tool-call frames have already reached the client, so a multi-step run cannot be un-sent. | — |

When gateway execution is **disabled**, an `automatic` request is *clamped to
manual* rather than rejected, registry tools are never advertised onto tool-less
requests, and built-in tools are not seeded. The streaming refusal never fires,
because the request is treated as manual.

## Enabling gateway execution

```yaml
tools:
  gateway_execution: true
```

```dotenv
SYNAPASS_TOOLS_GATEWAY_EXECUTION=true
```

With it on, `now` and `echo` are seeded at startup. Everything below about
automatic runs applies only in this mode.

## Safety model

- **Executability is derived, not granted.** Only `builtin` tools with a known
  handler run inside the gateway. Registering an external tool with
  `"executable": true` is impossible — the field is not writable.
- **`now` and `echo` are the only gateway-executable tools.** Everything else is
  advertised and argument-validated, then handed back.
- **JSON Schema is an allow-listed subset:** `type`, `properties`, `required`,
  `enum`, `const`, numeric/string/array bounds, `allOf`/`anyOf`/`oneOf`/`not`,
  local `$ref`, plus inert annotations. An unknown keyword is a `400`, because a
  constraint the gateway does not enforce must not look enforced.
- **Every mutation writes an audit event.**

## Bounds

Bounds are operator-owned and client-narrowable:

| Bound | Range | Meaning |
| --- | --- | --- |
| `max_steps` | 1–10 | Model/tool round trips in one run. |
| `max_tool_calls` | 1–64 | Total calls in one run. |
| `max_run_seconds` | 1–600 | Wall clock for one run. |
| allowed / denied tool globs | — | Deny wins. |
| allowed / denied provider globs | — | Deny wins. |
| sensitive-data blocking | — | Refuse runs over sensitive payloads. |
| approval routing | — | Flag calls for client approval. |

A run stopped by a bound **explains itself** rather than returning a dangling
tool call: the answer names what was executed and why it stopped, because the
tool results live in the gateway's run and the client cannot resume it.

## Tool policies

```bash
curl -s -X PUT $GATEWAY/admin/v1/tool-policies \
  -H "Authorization: Bearer $SYNAPASS_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "support-agent",
    "mode": "automatic",
    "max_steps": 4,
    "max_tool_calls": 8,
    "max_run_seconds": 120,
    "denied_tools": ["*admin*"]
  }' | jq
```

Omitting `enabled` means enabled on create, and leaves the value alone on update —
creating a policy with only a mode and bounds stores one that takes effect, not a
silently inert one.

## Response metadata

A request that involved tools carries `synapass.tool_run`:

```json
"tool_run": {
  "mode": "automatic",
  "status": "gateway_executed",
  "run_id": "731b8540-…",
  "steps": 3,
  "calls": 3,
  "executed": 3,
  "stop_reason": "stopped at the 3-step limit",
  "tools": ["now", "now", "now"],
  "latency_ms": 8383
}
```

`status` is `client_executed`, `gateway_executed`, `gateway_failed` when the run
itself errored, or `denied` when policy refused.

## Admin API

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/tools` | `usage:read` | Every registered tool. |
| `POST` | `/tools` | `providers:admin` | Register. `name` immutable; a `builtin` requires a known `handler`. |
| `PUT` | `/tools/{id}` | `providers:admin` | Partial update. |
| `POST` | `/tools/{id}/enabled` | `providers:admin` | `{"enabled": bool}`. |
| `DELETE` | `/tools/{id}` | `providers:admin` | Remove a registry entry. History is kept. |
| `GET` | `/tool-policies` | `usage:read` | All policies. |
| `PUT` | `/tool-policies` | `providers:admin` | Upsert by name + tenant. |
| `DELETE` | `/tool-policies/{id}` | `providers:admin` | Delete. Requests fall back to the manual default. |
| `GET` | `/tool-invocations` | `usage:read` | Every model-requested call with arguments, status and outcome. |
| `GET` | `/tool-invocations/{id}` | `usage:read` | One invocation with its execution record. |
| `GET` | `/agent-runs` | `usage:read` | Every bounded run with terminal status and work done. |
| `GET` | `/agent-runs/{id}` | `usage:read` | The run plus its full step trace. |

`parameters` must be a JSON Schema object and is compiled on write. `owner: tenant`
requires a `tenant_id` that exists.

## Structured output

A request with `response_format` carries `synapass.structured`
(`requested`, `valid`, `schema`, `error`, `repaired`).

When an answer contains a fenced JSON object and the extraction is unambiguous,
the gateway repairs it and says so in `repaired` rather than failing a response
the caller could have used. A genuine schema violation is a `400` — returning
content the caller cannot parse is worse than telling it why.

## Observability

| Metric | Meaning |
| --- | --- |
| `synapass_tools_runs_total{tenant,mode,status}` | Runs by mode and outcome |
| `synapass_tools_run_duration_seconds{mode}` | Run latency |
| `synapass_tools_invocations_total{tenant,tool,status}` | Per-tool calls |
| `synapass_tools_persist_errors_total{tenant}` | Trace persistence failures |

Tool names are operator-registered and finite, so the per-tool label is bounded
like providers and models.

NATS `ar.tool.run.completed` carries the finished run's summary, retained 30 days
in `SYNAPASS_EVENTS` alongside audit and health events.

The run row is written *before* the first step, because `agent_steps` has a
foreign key to `agent_runs`. A persistence failure does not fail the request, but
it is logged, counted and flagged on the event.

## Non-goals

- No remote code execution: external tools never run inside the gateway.
- No prompt-based shim for models without native tool support; a model that
  cannot emit tool calls simply never triggers a run.
- No streaming gateway-side execution.
- No multi-agent orchestration: one request is one bounded run.
- No approval UI beyond the `requires_approval` routing flag. The client is the
  approver.

---

Related: [API reference](api.md) · [Caching](caching.md) · [Dashboard](dashboard.md) · [Back to README](../README.md)