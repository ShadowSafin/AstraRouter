# Phase 3: management and provisioning

Phase 3 turns CoreRouter from a view-only gateway into a manageable control
plane. Providers, models, tenants, API keys, policies, endpoints and overrides
are created, edited, disabled and deleted through the admin API and the
dashboard — no configuration-file edits, no restarts, no code changes.

## The core loop

1. Open the dashboard (`/providers`) or call `POST /admin/v1/providers`.
2. Attach the credential (dashboard credential form or
   `PUT /admin/v1/providers/{id}/credential`). Tick discovery (or pass
   `"sync_models": true`) to populate all remote models in the same call;
   the per-row Sync button re-runs discovery later.
3. Add one or more models (`/models` page or `POST /admin/v1/models`) — or
   rely on discovery, then tune pricing, aliases, priority and status.
4. Run the connectivity test (Test button or
   `POST /admin/v1/providers/{id}/test`) and read the stored results.
5. Set routing rules: policies (`/policies`), endpoint overrides
   (`/endpoints`), budget caps (`/budgets`).
6. Point a client at `http://IP:PORT/v1` with a tenant API key.
7. Watch requests, usage, cost and audit entries arrive.
8. Edit anything later from the same UI or API.

## Inference surface (`/v1`)

Unchanged from Phase 1/2 and still OpenAI-compatible:

| Endpoint | Auth | Notes |
| --- | --- | --- |
| `POST /v1/chat/completions` | tenant key (`inference`) | Streaming and non-streaming |
| `GET /v1/models` | tenant key | Alias-expanded registry |
| `GET /v1/models/{model}` | tenant key | Single entry |
| `POST /v1/completions`, `POST /v1/embeddings`, `POST /v1/responses` | — | `501 not_implemented` stubs |

An existing OpenAI SDK works by changing the base URL to the gateway.

## Admin surface (`/admin/v1`)

Full endpoint reference lives in `docs/api.md` ("Phase 3: management").
Scope rules: `/providers*` → `providers:admin` (model writes included,
model reads stay `usage:read`), `/keys*` → `keys:admin`,
`/tenants*` → `tenants:admin`, `/policies*` → `policies:admin`,
`GET /overrides` → `usage:read`, `POST /overrides` → `providers:admin`.

Conventions shared by every write:

- `POST` creates (`201`, `409` on natural-key conflict), `PUT` replaces with
  the natural key immutable (provider/model name, tenant slug),
  `PATCH` merges, `DELETE` removes (cascades noted per resource).
- Every mutation writes an audit event with before/after values and triggers
  an immediate runtime reload (catalogue + adapters + policies). The
  background refresh loop remains the convergence backstop.
- Validation failures return `400 invalid_request` with a specific message;
  unknown ids return `404`.

## Credentials

Provider secrets are stored AES-256-GCM encrypted in the
`provider_credentials` table — one active credential per provider.

- The plaintext is accepted only by `PUT .../credential`, sealed
  immediately, and never appears in logs, responses, audit events or the UI.
  Reads return `{has_credential, credential: {name, key_version, ...}}`.
- Resolution precedence at adapter-build time: `api_key_env` binding first,
  then the stored credential, then unauthenticated. Setting an env binding
  and a stored credential is allowed; the env binding wins (documented, not
  silent — the detail view shows both facts).
- Key material: explicit `CR_CREDENTIALS_KEY` (32 bytes, raw/hex/base64)
  wins; otherwise the data key derives from the admin key via
  HKDF-SHA256, so stock deployments need no new configuration. Rotating the
  admin key orphans stored secrets — re-save them after a rotation.
- Rotation is `PUT .../credential` again (audited as `rotate`); removal is
  `DELETE .../credential`. The next catalogue refresh rebuilds the adapter
  with the new secret.

Tenant API keys keep the Phase 1 model: CSPRNG plaintext returned once,
SHA-256 digest stored, prefix retained for display. `PATCH /keys/{id}` edits
metadata; `POST /keys/{id}/rotate` swaps the secret in place and drops the
old cache entry immediately.

## Ownership: `managed_by`

Catalogue rows carry `managed_by: bootstrap | api`. The bootstrapper applies
the configuration file only to rows it owns: a provider, model or policy
created or edited through the API is skipped on every restart, so dashboard
edits survive deploys. To hand a row back to the file, delete it through the
API and let the seeder recreate it.

## Provider testing

`POST /admin/v1/providers/{id}/test` runs up to three checks in order —
`connectivity` (adapter health check), `models` (remote listing, skipped with
a note for kinds without listing support), `sample` (a 16-token completion on
the first usable stored model, an explicit `model`, or the first remote
name). Checks never abort the run early; each step persists to
`provider_test_results` and the run is audited once with the summary. History
is `GET .../tests?limit=`.

## Dashboard pages

| Page | Phase 3 capability |
| --- | --- |
| `/providers` | Add/edit/disable/delete, credential set/clear (with one-call model discovery), Sync models button, test with inline results, probe/kill retained |
| `/models` | Add/edit/disable/delete under a provider (discovery fills the list; edits survive re-syncs) |
| `/tenants` | Create/edit/disable/delete (force path when keys remain) |
| `/keys` | Mint/edit/rotate/revoke with copy-once banners |
| `/policies` | Create/edit (scalar + JSON blocks)/delete/reload |
| `/endpoints` | Create/edit/delete routing scopes with a call example per row (one inference URL + `X-CoreRouter-Endpoint` header; scopes constrain routing: forced model, preferred lists, strategy, budgets, fallback block) |
| `/overrides` | List/create health and routing overrides (revoke = inverse row) |
| `/audit`, `/requests`, `/errors`, `/budgets`, `/scores`, `/cache`, `/replay`, `/evaluations`, `/settings` | Unchanged views; audit now shows the new actions |

## Deployments

Docker Compose and native installs share the same contract: everything above
is database state, so it works identically in both. Requirements:

- Run migrations (`CR_POSTGRES_AUTO_MIGRATE=true` in Compose, or
  `corerouter migrate` natively) to pick up `0004_phase3.sql`.
- Set `CR_CREDENTIALS_KEY` for serious deployments (see `.env.example`);
  development deployments derive it automatically.
- Existing `config.example.yaml` catalogues keep working; seeded rows are
  `bootstrap`-owned and upserted as before.

## Verification

- `go test ./...` — unit coverage for validation, sealing and the test
  runner (`internal/admin`), plus the existing suites.
- `python -m unittest discover -s tests -t .` in `workers/` (171 tests).
- `scripts/smoke.ps1` (11 checks) / `scripts/smoke.sh` (10 checks) — both
  end with a full management cycle: create provider + credential + model +
  tenant + key, rotate, connectivity test, audit check, cleanup.
- Dashboard: 17/17 pages return 200; `npm run build` passes in `dashboard/`.

## Non-goals (unchanged)

No self-learning routing, no autonomous optimization, no ML in the live
control plane, no dashboard redesign, no break in OpenAI compatibility.
