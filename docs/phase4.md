# Phase 4: tool calling, structured output and agent runs

Phase 4 turns CoreRouter from a request router into an agent runtime.
Models can call tools, answers can be contract-checked against a JSON Schema,
and every gateway-side run leaves a durable step trace an operator can
inspect.

> **Client-executed tools are the default.** Gateway-side execution is
> opt-in via `tools.gateway_execution: true` (or
> `CR_TOOLS_GATEWAY_EXECUTION=true`). When it is off — the default — an
> `automatic` request is clamped to `manual` rather than rejected, registry
> tools are never advertised onto tool-less requests, and built-in tools are
> not seeded. The gateway forwards tool definitions and returns the model's
> `tool_calls` untouched, which is exactly what agentic apps such as OpenCode
> and Claude Code need to run their own tools. Everything below about
> automatic runs applies only when gateway execution is enabled.

## The core loop

1. Register tools (`/tools` page or `POST /admin/v1/tools`). The built-in
   `now` and `echo` tools are seeded at startup when gateway execution is
   enabled; external tools are discovery-only and always executed by the
   client.
2. Write a tool policy (`/tool-policies` page or `PUT /admin/v1/tool-policies`).
   Without one, every request runs under the built-in manual default: tools
   are offered to the model, execution stays client-side.
3. Send a chat request with `tools` and, to let the gateway run them (gateway
   execution enabled),
   `"tool_execution": {"mode": "automatic"}`.
4. Read the answer plus `corerouter.tool_run`: what ran, how many steps it
   took, and why it stopped.
5. Open `/agent-runs`, expand the run, and read the step trace and every tool
   invocation with its arguments and outcome.

## Request contract (`POST /v1/chat/completions`)

Three additive fields; omitting all three is exactly the pre-Phase-4 behaviour:

| Field | Meaning |
| --- | --- |
| `tools` | OpenAI-style tool definitions. Validated before routing: a malformed tool is a `400` with a field name, not an opaque upstream rejection. |
| `tool_choice` | `none`, `auto`, `required`, or `{"type":"function","function":{"name":"…"}}`. `required` under a disabled policy is a `403`. |
| `tool_execution` | `{"mode": "manual"}` (default) or `{"mode": "automatic"}`. The client may only narrow what the policy allows. |
| `response_format` | `{"type": "json_object"}` or `{"type": "json_schema", "json_schema": {...}}`. A mismatch fails the request; the report lands in `corerouter.structured`. |

Execution modes:

- **Manual (default).** The model's `tool_calls` reach the client untouched,
  with `finish_reason: tool_calls` and a `tool_run.status` of
  `client_executed`. Nothing runs inside the gateway.
- **Automatic, non-streaming (gateway execution enabled).** Safe tools run
  inside the gateway in a bounded loop; the client gets one final answer with
  `finish_reason: stop` and a `tool_run.status` of `gateway_executed`.
  External tools are skipped with an explanation injected for the model, never
  executed. When gateway execution is disabled, automatic is clamped to manual
  and the tool calls stream back to the client instead.
- **Automatic, streaming (gateway execution enabled).** Refused with a clear
  `400`: streamed tool-call frames have already reached the client, so a
  multi-step run cannot be un-sent. Stream the model's tool calls and execute
  them client-side instead. When gateway execution is disabled this refusal
  never fires: the request is treated as manual.

Bounds (operator-owned, client-narrowable): max steps (1–10), max tool calls
(1–64), max wall-clock seconds (1–600), max result bytes, allowed/denied tool
and provider globs (deny wins), approval routing, sensitive-data blocking.

A run stopped by a bound explains itself rather than returning a dangling
tool call: the answer names what was executed and why it stopped, because the
tool results live in the gateway's run and the client cannot resume it.

## Safety model

- Executability is derived, not granted: only `builtin` tools with a known
  handler run inside the gateway. Registering an external tool with
  `"executable": true` is impossible — the field is not even writable.
- `now` and `echo` are the only gateway-executable tools. Everything else is
  advertised and argument-validated, then handed back.
- JSON Schema is an allow-listed subset (`type`, `properties`, `required`,
  `enum`, `const`, numeric/string/array bounds, `allOf`/`anyOf`/`oneOf`/`not`,
  local `$ref`, plus inert annotations). An unknown keyword is a `400`, not a
  silently unenforced constraint.
- Tool and endpoint-scoped requests bypass the response cache (Phase 4 behaviour; Phase 5 namespaces endpoint scopes in the key and reuses safe deterministic tool requests instead).
- Every mutation writes an audit event.

## Admin surface (`/admin/v1`)

| Endpoint | Notes |
| --- | --- |
| `GET/POST /tools`, `PUT/DELETE /tools/{id}`, `POST /tools/{id}/enabled` | Registry CRUD. Single resources return the bare object, matching Phase 3. |
| `GET/PUT /tool-policies`, `DELETE /tool-policies/{id}` | Upsert matches on name + tenant. Omitting `enabled` means enabled on create, and leaves the value alone on update. |
| `GET /tool-invocations`, `GET /tool-invocations/{id}` | Per-call history with arguments, status and outcome. |
| `GET /agent-runs`, `GET /agent-runs/{id}` | Runs with their step trace. |

Scope rules follow the Phase 3 convention (`providers:admin` for writes;
history reads stay `usage:read`).

## Dashboard pages

- `/tools` — registry table with create/edit/enable/delete. Built-ins cannot
  be deleted; their handlers are fixed.
- `/tool-policies` — mode, tool allow/deny globs, numeric bounds and flags,
  with the same ranges the API enforces.
- `/agent-runs` — every run with status, work done and latency; expanding a
  row shows the model/tool step trace and the per-invocation history.
- The Requests page is unchanged: tool metadata travels with the run, and the
  run id joins back to the request.

## Observability

- Metrics: `corerouter_tools_runs_total{tenant,mode,status}`,
  `corerouter_tools_run_duration_seconds{mode}`,
  `corerouter_tools_invocations_total{tenant,tool,status}`,
  `corerouter_tools_persist_errors_total{tenant}`. Tool names are
  operator-registered and finite, so the per-tool label is bounded like
  providers and models.
- NATS: `cr.tool.run.completed` carries the finished run's summary (retained
  30 days in `COREROUTER_EVENTS`, alongside audit and health).
- The run row is written before the first step, because `agent_steps` has a
  foreign key to `agent_runs`. A persistence failure does not fail the
  request, but it is logged, counted and flagged on the event.

## Verification

- `go test ./...` — 17+ packages, including the HTTP tool suite
  (`internal/api/tools_phase4_test.go`: validation, compatibility, gateway
  execution, refusal paths, structured output, bounded-run notices) and the
  schema allow-list tests.
- `scripts/smoke.ps1` / `scripts/smoke.sh` — Phase 4 section exercises tool
  CRUD, policy upsert, invocation history and agent-run detail against a live
  stack.
- Live: migration `0005_phase4.sql`, seeded `now`/`echo`, an automatic run
  against a real provider, and the full durability chain
  (`tool_invocations` → `tool_executions` → `agent_runs` → `agent_steps`)
  confirmed in PostgreSQL.

## Non-goals

- No remote code execution: external tools never run inside the gateway.
- No prompt-based tool shim for models without native tool support; a model
  that cannot emit tool calls simply never triggers a run.
- No streaming gateway-side execution (see above).
- No multi-agent orchestration: one request is one bounded run.
- No approval UI beyond the `requires_approval` routing flag; the client is
  the approver.
