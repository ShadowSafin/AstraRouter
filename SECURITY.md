# Security Policy

Please report vulnerabilities privately. Do not open a public issue for a security
problem.

## Reporting a vulnerability

Email **security@synapass.dev** with:

- A description of the issue and its impact.
- Steps to reproduce, ideally a request body.
- The version or commit (`synapass version`).
- Any `request_id` from a failing response.

You will get an acknowledgement within **3 business days** and an assessment with a
remediation plan within **10 business days**. We will tell you when a fix ships and
credit you in the release notes unless you would rather we did not.

Please give us a reasonable window to publish a fix before disclosing. We aim for
**90 days** from acknowledgement, and sooner for anything with a working exploit.

## Scope

Synapass holds credentials and forwards prompts, so it is in scope for:

| In scope | Out of scope |
| --- | --- |
| Authentication and authorization bypass | Weaknesses in upstream providers |
| API key or admin key exposure | Missing hardening behind a reverse proxy you control |
| Provider credential handling | Denial of service from a single unauthenticated client (report anyway — rate-limit gaps matter) |
| Tenant isolation failures, including cache cross-talk | Reports from automated scanners with no demonstrated impact |
| Injection into policy, SQL or the event bus | Denial of service requiring authenticated access |
| Path traversal, SSRF through provider or tunnel configuration | Missing security headers on the dashboard, behind your own proxy |
| Log redaction gaps that expose secrets or prompts | Physical security of your deployment |
| Tunnel exposure beyond what is documented | |

A cache hit served across tenants is in scope and treated as high severity.
Tenant isolation is a structural property, not a convention.

## Handling secrets safely

Things to know when you work with this codebase:

- **Provider secrets are referenced, not embedded.** A provider record names an
  environment variable, or holds a credential sealed with AES-256-GCM. An inline
  `api_key` on a create or update is discarded on purpose.
- **API keys are hashed.** Only a SHA-256 digest is stored; the plaintext is
  returned exactly once. A leaked database yields no usable credential.
- **Provider credentials need `SYNAPASS_CREDENTIALS_KEY`.** Without it the data key
  derives from `SYNAPASS_ADMIN_KEY`, so rotating the admin key orphans stored secrets.
  Set it explicitly for anything you intend to operate.
- **The dashboard never holds the admin key in the browser.** Next route handlers
  proxy `/admin/v1/*` server-side, and refuse to attach the key at all unless the
  gateway has confirmed the operator's session.

## Console operator login

The dashboard is a control plane, so it is gated on a human identity rather than
on the admin key. The two are deliberately separate credential types:

| | API key | Console operator |
| --- | --- | --- |
| Material | 256 bits of CSPRNG output | Chosen by a person |
| Stored as | SHA-256 digest | Argon2id hash, PHC-encoded |
| Verified by | Index lookup | Key stretching, ~19 MiB and two iterations |
| Abuse control | Rate limits per key | Rate limits plus per-account lockout |

A fast hash would be wrong for a password: the whole point of key stretching is
that verifying it is expensive, and an API key's fast digest is safe only because
there is no dictionary to attack.

Things to know:

- **No default credentials.** The first operator is created through the setup
  screen; nothing is seeded.
- **First-run setup latches shut.** It is gated on a row in `settings`, not on the
  absence of users, so deleting every operator does not hand setup back to
  whoever can reach the console. There is deliberately no reset link and no
  recovery backdoor — recovery is a database operation.
- **Sessions are opaque and revocable.** The cookie holds 32 random bytes; only
  the SHA-256 is stored. Logout revokes the row rather than deleting it, so the
  record of who was signed in survives the session. A password change revokes
  every existing session, because a change that leaves sessions valid has not
  removed access from whoever held one.
- **Cookies are `httpOnly` and `SameSite=Lax`.** `Secure` follows the actual
  request scheme, so a plain-HTTP local install works; set `cookie_secure: true`
  when TLS is terminated where the gateway cannot see it.
- **Failed logins are indistinguishable.** A wrong password and an unknown
  username return the same `401`, so the login form cannot be used to enumerate
  accounts. A locked account returns `429` deliberately, because silently
  reporting "incorrect" would leave a legitimate operator no way to tell a
  lockout from a typo.
- **Console auth does not replace the admin key.** Programmatic access to
  `/admin/v1/*` still uses `SYNAPASS_ADMIN_KEY`, which is what scripts and CI rely on.
  The session gates the dashboard.
- **Request bodies are not logged by default.** The redactor has a deny-list of
  headers and value patterns; extend it rather than bypassing it.
- **Tunnel URLs are public.** Anyone holding a live `trycloudflare.com` URL can
  reach what someone on your LAN could. Prefer exposing the gateway over the
  dashboard.

## Hardening before production

Synapass refuses to start in a configuration it considers unsafe. These are the
settings worth reviewing:

| Check | Setting |
| --- | --- |
| `SYNAPASS_ADMIN_KEY` is set and strong | Required when `admin.require_scope: true` |
| CORS names real origins | A wildcard is rejected in production |
| `X-Forwarded-For` only from your proxies | `http.trusted_proxies` |
| Anonymous access is off | Never on in production |
| Traces have an endpoint | Tracing enabled with no OTLP endpoint refuses to start |
| The dashboard sits behind your own TLS terminator | It has no authentication of its own |
| Scope minimums per key | A key only needs `inference` unless it also reads |

`synapass config` prints the resolved configuration with every secret redacted —
the fastest way to confirm what the process actually sees. Redaction is applied to
a copy, so printing never mutates the running configuration.

## Supported versions

Security fixes land on the current release line. The project is pre-1.0, so there
is no long-term-support branch; fix forward and upgrade.

## Disclosure

We ask that you report through the address above rather than a public tracker,
and that you allow time for a fix. We will acknowledge your report, keep you
informed, and credit you unless you prefer otherwise.

---

Related: [README](README.md) · [Architecture](documentation/architecture.md) · [Contributing](CONTRIBUTING.md) · [Back to the documentation index](documentation/README.md)