# Dashboard

The dashboard is a Next.js operator console. It is a **control plane**: it changes
how the gateway routes and what it costs, and it shows you what the gateway is
doing. It does not serve inference.

The browser never holds the admin key. Next route handlers proxy `/admin/v1/*`
server-side, so CORS never has to be widened to expose administrative routes.

## Authentication

The console requires an operator account. Opening it for the first time shows a
setup screen that creates one administrator and then closes itself permanently;
every visit after that shows a login form.

### First run

On a fresh installation the gateway reports that no operator exists, and the
dashboard renders **Create the admin account**. The form asks for a username, a
password and a confirmation, with a live strength meter.

There are no default credentials. The account is created with the password you
type, and nothing is written to the log or the response beyond the username.

The form closes once you continue. It is gated on a latch in the settings table
rather than on the absence of user rows, so **deleting every operator does not
reopen it** — otherwise anyone who could reach the console would be able to claim
it again. A second call to the setup endpoint returns `403`.

### Signing in

Username and password, checked against an Argon2id hash. After
`max_failed_attempts` consecutive failures the account locks; the delay doubles on
each further round up to `max_lockout_duration`. A locked account refuses the
correct password too — that is the point of a lockout — and the response says so
plainly rather than pretending the credentials were wrong.

A wrong password and an unknown username return the **same** message, so the form
cannot be used to discover which accounts exist.

### Sessions

A successful login sets an `httpOnly` cookie holding an opaque 32-byte token. Only
the token's SHA-256 is stored, so a database disclosure yields no usable session.
Sessions expire after `session_ttl`, and after `idle_ttl` without use — that check
happens when the session is presented, so no background job is needed. Logging out
revokes the row rather than deleting it, so the record of who was signed in
survives the session.

| Attribute | Value |
| --- | --- |
| `httpOnly` | Always. Script cannot read the session. |
| `Secure` | Follows the request: on behind TLS, off over plain HTTP. Force it with `cookie_secure`. |
| `SameSite` | `Lax`. `Strict` would drop the cookie when you arrive from a link and read as a broken login. |
| `Path` | `/` |

> **Recovering a lost password is a database operation.** There is no reset link and
> no default account, by design — a recovery path is a backdoor. Set
> `AR_CREDENTIALS_KEY` and keep a database backup.

### What the login actually protects

The dashboard's `/api/gateway/*` proxy refuses to attach the admin key unless the
gateway has confirmed the session. Before this existed, that proxy accepted any
`/admin/v1/*` call with no user authentication at all, which made "can reach the
dashboard" equivalent to "is an operator".

Route middleware redirects an unauthenticated visitor to `/login`, but that is a
convenience, not the boundary: middleware runs in a different runtime and cannot
reach the database. The gateway's answer is what enforces anything.

The admin key still authenticates the admin API itself, so scripts, CI and the
smoke suite are unaffected.

## Pages

### Overview

Landing page: request volume, success rate, latency, spend and top models and
providers over the selected range, with a comparison against the previous window
and current provider health. This is the page to check first when something looks
wrong.

### Requests

Every request with its outcome, latency, tokens, cost, provider and policy.
Expand a row for the **explain** view: task classification, policy verdict,
prompt shaping plan, cache decision, candidate scores and the reason each rejected
target was rejected. Filter by outcome, provider, model, error code, minimum
status or a text search.

### Errors

Failures only, with a `by_code` rollup. Defaults to `status_min=400`. Useful for
answering "is this one bad provider or every provider".

### Usage

Aggregate spend and tokens with per-provider and per-model breakdowns, plus a
bucketed time series.

### Analytics

Deeper series: throughput, latency percentiles, success rate, token throughput and
cost over time, with provider and model breakdowns.

### Providers

Add, edit, disable and delete providers; set and clear credentials; run
one-click model discovery; run connectivity tests with inline results; probe
health; and kill or revive a provider. Provider rows show health state and
`adapter_ready`.

### Models

Add, edit, disable and delete models under a provider. Discovery fills the list;
your pricing, aliases, priorities and disables survive a re-sync.

### Playground

Test an endpoint live: pick a model, an endpoint scope and a policy, send a
prompt, and read the answer next to the routing decision that produced it —
provider, attempts, cache, fallback, tokens and the full debug decision. Both
streaming and buffered runs, tool calling, and side-by-side compare of two
targets. Runs go through the real inference API, not a simulation. See
[Playground](playground.md).

### Policies

Create, edit and delete routing policies. Scalar fields get their own inputs;
complex blocks — targets, limits, timeouts, retry, fallback — are edited as JSON
with server-side validation. **Reload** forces a resolver refresh.

### Endpoints

Named scopes with routing overrides, each with a copy-paste call example showing
the inference URL and the `X-AstraRouter-Endpoint` header.

### Tenants

Create, edit, disable and delete tenants. Deleting a tenant that still holds
active keys takes a force path.

### Keys

Mint, edit, rotate and revoke keys, with a copy-once banner. Rotation keeps the
key's identity and swaps only the secret; the old secret stops working
immediately rather than after the cache TTL.

### Budgets

Daily and monthly spend ceilings per tenant, plus a hard per-request cap that
flows into guardrails.

### Overrides

Health and routing overrides. Revoking is writing the inverse row, so history is
preserved. Each shows its scope, reason, actor and expiry.

### Scores

Provider and model quality rankings with a blended [0,1] score and a
human-readable explanation of what produced it.

### Cache

Hit rate, exact/prefix/semantic split, bypass reasons, reuse count, latency saved,
average lookup time, top prompts, TTL and tier switches, per-scope policies,
recent entries (metadata only), the invalidation trail, and scoped flush controls.
See [Caching](caching.md).

### Tools

The tool registry: create, edit, enable and disable. Built-ins cannot be deleted
— their handlers are fixed.

### Tool policies

Mode, tool allow/deny globs, numeric bounds and flags, with the same ranges the
API enforces.

### Agent runs

Every bounded run with its status, work done and latency. Expanding a row shows
the model/tool step trace and the per-invocation history with arguments and
outcomes.

### Replay and evaluations

Create replay jobs against recorded requests across chosen providers and models;
read evaluation runs with scored results and regression flags.

### Audit

Control-plane history, filterable by tenant and resource. Every mutation is here
with before and after values.

### Tunnels

Status cards, a copyable public URL, a target selector with an admin-surface
warning, session history, and guidance when the `cloudflared` binary is missing.
See [Cloudflare tunnel](installation/cloudflare-tunnel.md).

### Settings

Build identity, uptime, dependency state, provider health, and the **redacted**
effective configuration.

## Common workflows

**Add a provider and start routing to it**

1. **Providers** → Add. Name, kind, base URL, credential source.
2. Tick **Sync models**, then **Save**.
3. **Test** with the default checks; read the inline results.
4. **Policies** → edit the target list to include it, or add a policy that does.
5. Confirm on **Requests** that traffic is landing there.

**Change what a tenant pays**

1. **Budgets** → set the daily or monthly ceiling.
2. Optionally set a hard per-request cap.
3. Check **Budgets** and **Overview** to confirm enforcement.

**Investigate a wrong answer**

1. **Requests** → filter by `search` or by outcome.
2. Expand the row; read the explain view.
3. Check whether prompt shaping trimmed the prompt — a trimmed prompt reports
   `astrarouter.shaping`, and a shortened answer is not the same as a model that
   stopped early.
4. Check **Scores** if the model was chosen on quality.

**Roll back a bad routing change**

1. **Policies** → the policy in question.
2. Adjust the target list or strategy.
3. **Reload**.
4. Confirm on **Requests**.

**Flush the cache after a model change**

**Cache** → choose the scope (tenant, model, provider) and flush. Every flush is
audited and published. See [Caching](caching.md).

## Running it

With Compose it is already running at `http://127.0.0.1:3000`.

Standalone:

```bash
cd dashboard
npm ci
npm run build
```

```bash
NODE_ENV=production \
ASTRAROUTER_API_URL=http://127.0.0.1:8080 \
ASTRAROUTER_ADMIN_KEY=... \
PORT=3000 HOSTNAME=127.0.0.1 \
node .next/standalone/server.js
```

| Variable | Meaning |
| --- | --- |
| `ASTRAROUTER_API_URL` | The gateway as seen from the dashboard **server** |
| `ASTRAROUTER_ADMIN_KEY` | The same value as the gateway's `AR_ADMIN_KEY` |
| `NEXT_PUBLIC_ASTRAROUTER_API_URL` | Gateway URL advertised to the browser, for direct non-admin calls |

Put a reverse proxy in front and terminate TLS there. The dashboard has no
authentication of its own — whoever reaches it and holds the admin credential can
change routing. Do not expose it to the internet without that in mind, which is
why the tunnel page warns before exposing it.

## Development

```bash
make dashboard-install     # npm ci
make dashboard-typecheck   # tsc --noEmit
make dashboard-build       # production build
```

| Path | Contains |
| --- | --- |
| `src/app/<page>/page.tsx` | Route entry for one page |
| `src/components/views/` | The view component behind each route |
| `src/components/ui/` | Primitives: card, button, input, badge, table, states |
| `src/components/charts/` | Time series and bar list |
| `src/components/layout/` | App shell and sidebar |
| `src/lib/api.ts` | Typed admin client |
| `src/lib/charts.ts` | Series normalization, with its own tests |
| `src/hooks/use-admin.ts` | Queries and mutations |

A page is a thin route entry; the view holds the data fetching and presentation.
Adding a page means adding both plus a sidebar link.

## Common problems

| Symptom | Cause | Fix |
| --- | --- | --- |
| Dashboard loads but every panel is empty | The gateway is unreachable from the dashboard server | Check `ASTRAROUTER_API_URL` from the server's network, not the browser's |
| `401` on load | `ASTRAROUTER_ADMIN_KEY` does not match `AR_ADMIN_KEY` | Make them identical |
| Edits appear then vanish after a restart | The row is `bootstrap`-managed | Re-create it through the API so it becomes `api`-managed |
| A model you edited is missing after discovery | Discovery skips existing rows | Expected — your edits are preserved |
| Charts are empty for a custom range | No data in that window | Check the range picker and that requests were flowing |
| The page 404s on a new route | Route entry missing | Add `src/app/<name>/page.tsx` |
| Redirected to `/login` but you are signed in | The cookie expired or was revoked | Sign in again; the session cookie has no `Secure` attribute over plain HTTP, so a browser may drop it on some networks |
| Login always returns "the username or password is incorrect" with the right password | The account is locked out | Wait for `lockout_duration`; the delay doubles on each further round. `429` says so explicitly when locked |
| Login says the body is not valid JSON | A stale dashboard build against a newer gateway | Rebuild the dashboard image |
| Every panel 401s after login | The gateway rejected the session | Check the gateway log; `docker compose logs gateway` |
| The setup form will not submit | First-run setup already completed | Expected — setup is latched. Recover access in the database if the credentials are lost |

---

Related: [API reference](api.md) · [Routing](routing.md) · [Caching](caching.md) · [Providers](providers.md) · [Back to README](../README.md)