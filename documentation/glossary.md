# Glossary

AstraRouter-specific terms, defined. Where a term matches OpenAI's usage, the
difference is noted.

## The gateway

**Adapter** — the code that speaks one provider's wire format. One per provider
kind; it converts AstraRouter's normalized request to that provider's shape and
the response back.

**Candidate** — one provider+model pair that survived resolution against the
catalogue, with its price, capabilities and health attached. Routing orders and
filters candidates.

**Catalogue** — the in-memory view of registered providers and models, refreshed
from the database. Routing reads it; it never queries the database on the request
path.

**Chain** (fallback chain) — the ordered list of targets the executor may try
after the primary fails.

**Executor** — the component that runs a route decision: per-attempt deadlines,
retry with backoff, failover, and the guarantee that a started stream is never
appended to.

**Route decision** — the result of routing: the chosen target, the candidates, the
chain, the retry/timeout/fallback posture, and the reason. Recorded on every
request.

**Routing engine** — the pure function that turns a request plus a policy plus
observable state into a route decision. No network calls inside it, which is what
makes it testable and replayable.

**Strategy** — how candidates are ordered: `priority`, `weighted`,
`lowest_cost`, `lowest_latency`, `highest_quality`.

**Universal endpoint** — `POST /v1/chat/completions`, the one public inference
surface. "Universal" because any OpenAI-speaking client can call it.

## Policy

**Capability** — something a model can do: `chat`, `streaming`, `tools`,
`parallel_tools`, `json_mode`, `json_schema`, `vision`, `seed`. Required
capabilities are derived from the request body, never claimed by the client.

**Endpoint scope** — a named set of routing overrides (`/endpoints`) a caller
selects with `X-AstraRouter-Endpoint`. Forced model, preferred lists, strategy,
caps, fallback block.

**Policy** — a rule set that requests match on: model, request type, tenant, API
key, prompt size, capability, region, sensitivity, endpoint, streaming. Resolved
by specificity then priority.

**Policy decision** — the fine-grained engine's verdict on one request, with a
deny reason when it refuses.

**Specificity** — how narrowly a policy matches, as a weighted sum of which fields
it populated. Ordering by specificity before priority is what makes a rule set
safe to grow.

## Reliability

**Circuit breaker** — the per-provider state machine driven by active probes and
passive observation: `degraded` (deprioritised) then `unhealthy` (excluded).

**Degraded** — a mode rather than an outage. Redis down means per-process rate
limits and no caching; ClickHouse or NATS down means analytics is dropped. The
gateway keeps serving.

**Degraded route** — a decision made after the engine relaxed a filter it would
otherwise have enforced, because the alternative was no route at all. Carries a
`degraded` flag so the relaxation is visible.

**Failover** — moving to a different provider after a failure. Bounded by
`fallback.max_attempts`.

**Fallback** — see failover. The same concept, named differently in config
(`fallback`) and in prose.

**Relaxation** — the ordered loosening of routing filters (health first, then
cost) when they would empty the candidate set.

**Retry** — another attempt against the *same* provider, with jittered backoff.

**Stream-started error** — a failure after bytes reached the client. The request
is reported as failed and never retried, because the client already holds a
partial answer.

## Limits and cost

**Budget** — a daily or monthly spend ceiling for a tenant or policy.

**Ceiling** — an upper bound on what a client may request or spend. Distinct from
a target: a ceiling is not sent upstream as the request's `max_tokens`.

**Hard cap** — the absolute per-request spend limit, settable per tenant and
flowing into guardrails.

**Hard budget cap** — the same thing, described from the budget's side.

**Latency target** — a ranking signal for choosing between healthy candidates.
Not a deadline. It is recorded on the decision and used for ordering only.

**Period label** — the rendered key for a budget counter (`d:2026-03-15`,
`w:2026-…-W11`, `m:2026-03`, `total`). One function renders them so a counter is
never written under one key and read under another.

**Timeout policy** — the hard budgets: total, per-attempt, connect, first-token,
stream-idle.

**Truncation** — an answer that ended because a limit was reached rather than the
model finishing. Reported in `astrarouter.completion`; never silent.

## Intelligence

**Cache tier** — exact (identical request), prefix (shared system prompt, short
prompts only), semantic (similar prompt above a similarity threshold).

**Cache bypass** — a decision not to reuse, with a named reason
(`sensitive_request`, `tool_request`, `nondeterministic_request`, …).

**Eval** — scoring outputs deterministically against expected results, with
regression flags against golden thresholds.

**Explain** — the per-request explanation: task, policy verdict, shaping, cache
decision, scores and every rejected candidate's reason.

**Guardrail** — an override that blocks or constrains traffic: kill switch,
tenant emergency override, hard budget cap, fallback block, forced circuit.

**Judge** — an optional LLM used to score outputs against a rubric. Off by
default; the deterministic scorers are the signal when it is absent.

**Prompt shaping** — normalizing, compressing and trimming the prompt to fit a
target model. Every step is recorded with its token delta.

**Replay** — re-running recorded requests against current providers to compare
answers and cost.

**Score** — a blended [0,1] provider or model quality rank built from success,
latency, cost and feedback, with a human-readable explanation.

**Task classification** — the rules-first inference of what kind of work a request
is (`coding`, `summarization`, `extraction`, `reasoning`, `translation`,
`tool-use`, `structured_output`, `long_context`, `batch_offline`, …).

## Tools

**Agent run** — one bounded loop of model turns and tool executions, with a step
trace.

**Built-in tool** — a tool the gateway can execute itself. `now` and `echo` are
the only two.

**Client-executed** — the default: the model's `tool_calls` reach the client
untouched and the client runs them.

**Executable** — derived from a tool's `kind`, never granted by a write. External
tools are not executable inside the gateway, and the field is not writable.

**Gateway execution** — opt-in: safe tools run inside the gateway in a bounded
loop, and the client receives one final answer.

**Registry** — the list of tools AstraRouter knows about, whether or not the
gateway can execute them.

**Safety level** — a tool's declared risk (`safe`, `caution`, `dangerous`), used
by policy and by the cache decision.

**Tool policy** — the mode and bounds for one tenant or globally: max steps,
max calls, max seconds, allowed and denied tool globs.

## Control plane

**Admin key** — the control-plane credential (`AR_ADMIN_KEY`). Separate from
tenant inference keys and held by the dashboard's server, never the browser.

**Console operator** — a human account that can sign in to the dashboard. Stored
as an Argon2id hash rather than a digest, because an operator chooses a password
and the stored hash must be expensive to attack offline.

**First-run latch** — the settings row that closes initial setup permanently.
Gating on "no users exist" instead would mean deleting every operator reopens
setup to anyone who can reach the console.

**Key stretching** — deliberately slow password hashing (Argon2id here). The cost
is the protection: it makes an offline attack against a stolen hash expensive.

**Lockout** — refusing every password for an account, including the correct one,
after repeated failures. The delay doubles on each further round up to a bound, so
a sustained attack gets progressively more expensive without ever needing manual
intervention to recover a legitimate operator.

**Session token** — 32 bytes of CSPRNG output in an `httpOnly` cookie. Only its
SHA-256 is persisted, so a database disclosure yields no usable session.

**Idle expiry** — the limit on how long a session may go unused. Enforced when
the session is presented rather than by a background job, because that is the only
moment an idle session can actually be rejected.

**API key** — a tenant inference credential. Stored as a SHA-256 digest; the
plaintext is returned exactly once.

**Audit event** — a record of a control-plane mutation, with before and after
values. Inference traffic is not audited; usage records serve that need.

**Credential** — a provider's secret, sealed with AES-256-GCM. Distinct from an
API key, which is hashed rather than encrypted because it must be verifiable.

**Kill switch** — an override that takes a provider out of rotation immediately.
Revoking it writes the inverse row, so history is preserved.

**`managed_by`** — `bootstrap` or `api`, recording who owns a catalogue row so a
dashboard edit is not reverted by the next deploy.

**Override** — a time-bounded change to health or routing state. An event row, not
an edit.

**Scope** — an API key permission: `inference`, `usage:read`, `models:read`,
`policies:admin`, `providers:admin`, `keys:admin`, `tenants:admin`, `*`. Admin
routes narrow further by method and path.

**Seal** — encrypt a provider credential for storage.

**Tunnel** — a temporary Cloudflare quick tunnel exposing one local service at a
disposable URL.

## Storage and telemetry

**Async pipeline** — the lossy, batched write of usage, traces and events. It
never blocks a response and counts what it drops.

**Cardinality** — the number of unique label combinations in a metric. Bounded by
construction here: nothing per-request is a label.

**JetStream** — NATS' durable stream mode. Eval and replay jobs survive a worker
restart because of it.

**Lossy** — a signal that may be dropped under pressure. Telemetry is lossy by
design; authentication, policy and budgets never are.

**Metric namespace** — the `astrarouter_` prefix. Workers share it with
`astrarouter_worker_`.

**OTLP** — the OpenTelemetry protocol traces are exported with.

**Record** — one row of usage or request history. Usage records are billable;
request traces are analytical.

**Span** — one traced step: `classifier`, `shaping`, `cache`, `routing`,
`guardrail`, `provider`, `tools`, `eval`.

## Response fields

**`astrarouter` block** — the namespaced metadata AstraRouter adds to a response.
Stable attribution by default; full internals with `X-AstraRouter-Debug: true`.

**`completion` block** — how the answer was bounded and whether it finished.
Present only when something was capped or interrupted.

**`finish_reason`** — the provider's own reason: `stop`, `length`, `tool_calls`.
`length` means the model was still generating when the ceiling was reached.

**`fallback_used`** — the request did not go to the primary target.

**`routed_model`** — what actually served. Differs from `requested_model` on
exactly the requests you most need to trace.

---

Related: [Overview](overview.md) · [Architecture](architecture.md) · [FAQ](faq.md) · [Back to README](../README.md)