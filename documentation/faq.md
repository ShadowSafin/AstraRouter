# FAQ

Short, practical answers. For depth, follow the links.

## Getting started

### What is Synapass, in one sentence?

An OpenAI-compatible gateway that sits between your apps and your model
providers, and decides per request which provider serves it, what happens when
that provider fails, and what it costs. See [Overview](overview.md).

### Do I have to change my client code?

Base URL only. Any client that speaks OpenAI works by pointing at
`http://your-host:8080/v1`. The extra `synapass` response block is namespaced,
so OpenAI SDKs ignore it.

### Which provider does my traffic actually go to?

Whichever the routing policy picks — often not the first one configured. The
response tells you: `synapass.provider` is who **answered**, and
`routed_model` is what served.

### Do I need all four datastores?

No. Postgres is required. Redis is strongly recommended. ClickHouse and NATS are
optional — without them you lose analytics and background jobs, not inference.
See [Database](database.md).

### Which is faster, Docker or native?

Docker gets you a working stack in minutes; native is for a machine you intend to
operate. Performance is identical — same code, same configuration model.

## Routing

### Can I pin a request to one provider?

Yes. `X-Synapass-No-Fallback: true` gives exactly one attempt, and
`X-Synapass-Policy` pins the rule set. See [Routing](routing.md).

### Why did failover not happen?

Most often the error code was not in the policy's `on_error_codes`. That list is
exhaustive when present — an operator who enumerates the codes that justify
failover is narrowing behaviour deliberately.

### Why did failover not happen mid-stream?

Because a stream that has begun is committed. Retrying would append a second
attempt's tokens to the first, producing a duplicated, incoherent answer. You get
an `event: error` frame instead.

### What does `latency_target_ms` do?

It ranks candidates; it does not cancel anything. It is recorded on the decision
and used for ordering. Generation budgets come only from the timeout policy. This
was a real bug once — the target was reused as the hard deadline and truncated
everything over ten seconds.

### In what order are policies matched?

Specificity first, then priority. Specificity weights API key highest, then
tenant, model, capability, streaming, type and size. That ordering is what makes a
rule set safe to grow.

## Answers

### Why did my answer stop mid-sentence?

Check `synapass.completion` in the response. If `truncated` is true, `reason`
says which limit: `max_tokens` or `timeout`, and `budget_ms` tells you which
budget applied. Every truncation is now reported; nothing is cut silently.

### Why is `applied_tokens` absent?

Because the client asked for no limit, so the gateway sent none and the provider
applied its own default. The gateway only sends a ceiling the client asked for.

### My long buffered request timed out but streaming works.

That was a second, separate bug: the shared HTTP transport applied a 30-second
response-header timeout, which aborts a *buffered* request before its headers
arrive — a provider only sends headers once the whole answer exists. Removed.

### Does it cache by default?

No. `cache.response_cache` defaults to false. Turning it on deliberately, with
exact-only reuse first, is the safe sequence. See [Caching](caching.md).

### Will two tenants ever share a cache entry?

No. Tenant isolation is in the key and in the Redis namespace, not a convention.

## Tools

### Should I turn on gateway tool execution?

Usually not. The default is client-executed, which is what agentic apps such as
OpenCode and Claude Code expect. Turn it on when you want the gateway to run
deterministic built-ins in a bounded loop. See [Tools](tools.md).

### Can a registered tool run arbitrary code in the gateway?

No. Executability is derived from `kind`, never granted by a write. Only
`builtin` tools with a known handler execute — `now` and `echo`. External tools
are advertised, argument-validated, and handed back to you.

### Why was my automatic streaming tool request rejected?

Because streamed tool-call frames have already reached you, so a multi-step
gateway-side run cannot be un-sent. Stream the calls and run them client-side.

## Operations

### `/health` and `/ready` — which do I use?

`/health` for liveness: it deliberately does not check dependencies, so a
database blip cannot make an orchestrator restart every replica. `/ready` for
load balancers: it does check them.

### Is my data safe if ClickHouse is down?

Inference is unaffected. Analytics writes are dropped and counted in
`synapass_async_dropped_total`. Authentication, policy and budgets are
synchronous and never lossy.

### Are the costs in the dashboard real bills?

No — they are estimates from the price table you configured on each model. That is
deliberate: it is the same number budgets enforce against, so the console and the
limiter never disagree.

### Can I see where a request went and why?

Yes: `GET /admin/v1/requests/{id}/explain` returns the task classification, policy
verdict, shaping plan, cache decision, candidate scores and each rejection
reason. It is also on the dashboard's **Requests** page.

### How do I rotate a provider's secret?

`PUT /admin/v1/providers/{id}/credential` again. It audits as `rotate`. The next
catalogue refresh rebuilds the adapter with the new secret.

### I rotated `SYNAPASS_ADMIN_KEY`. What now?

Stored provider credentials are orphaned unless you set `SYNAPASS_CREDENTIALS_KEY`
explicitly, because the data key otherwise derives from the admin key. Re-save
each credential after rotating.

### Why is a dashboard edit reverted on restart?

The row is `bootstrap`-managed, so the configuration seeder recreates it.
Re-create it through the API so it becomes `api`-managed and survives deploys.

### Is it safe to expose the gateway on the internet?

Through a temporary tunnel, yes — with the same authentication, because the tunnel
is a network path and not a bypass. Treat the URL as public. Prefer exposing the
gateway over the dashboard. See
[Cloudflare tunnel](installation/cloudflare-tunnel.md).

## Development

### Where do I start reading the code?

`internal/api/chat.go` is the request path end to end. `internal/routing` is the
decision logic, `internal/domain` is the vocabulary. See
[Architecture](architecture.md).

### How do I run the tests?

```bash
make test                     # Go + Python
go test ./internal/...        # Go only
cd workers && python -m unittest discover -s tests -t .   # Python only
cd dashboard && npm run typecheck
```

### How do I add a page to the dashboard?

Add `src/app/<name>/page.tsx` as a thin route entry and the matching
`src/components/views/<name>-view.tsx`, then add a sidebar link. See
[Dashboard](dashboard.md).

---

Related: [Troubleshooting](troubleshooting.md) · [Glossary](glossary.md) · [Back to README](../README.md)