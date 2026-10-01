# Security Policy

Please report vulnerabilities privately. Do not open a public issue for a security
problem.

## Reporting a vulnerability

Email **security@corerouter.dev** with:

- A description of the issue and its impact.
- Steps to reproduce, ideally a request body.
- The version or commit (`corerouter version`).
- Any `request_id` from a failing response.

You will get an acknowledgement within **3 business days** and an assessment with a
remediation plan within **10 business days**. We will tell you when a fix ships and
credit you in the release notes unless you would rather we did not.

Please give us a reasonable window to publish a fix before disclosing. We aim for
**90 days** from acknowledgement, and sooner for anything with a working exploit.

## Scope

CoreRouter holds credentials and forwards prompts, so it is in scope for:

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
- **Provider credentials need `CR_CREDENTIALS_KEY`.** Without it the data key
  derives from `CR_ADMIN_KEY`, so rotating the admin key orphans stored secrets.
  Set it explicitly for anything you intend to operate.
- **The dashboard never holds the admin key in the browser.** Next route handlers
  proxy `/admin/v1/*` server-side.
- **Request bodies are not logged by default.** The redactor has a deny-list of
  headers and value patterns; extend it rather than bypassing it.
- **Tunnel URLs are public.** Anyone holding a live `trycloudflare.com` URL can
  reach what someone on your LAN could. Prefer exposing the gateway over the
  dashboard.

## Hardening before production

CoreRouter refuses to start in a configuration it considers unsafe. These are the
settings worth reviewing:

| Check | Setting |
| --- | --- |
| `CR_ADMIN_KEY` is set and strong | Required when `admin.require_scope: true` |
| CORS names real origins | A wildcard is rejected in production |
| `X-Forwarded-For` only from your proxies | `http.trusted_proxies` |
| Anonymous access is off | Never on in production |
| Traces have an endpoint | Tracing enabled with no OTLP endpoint refuses to start |
| The dashboard sits behind your own TLS terminator | It has no authentication of its own |
| Scope minimums per key | A key only needs `inference` unless it also reads |

`corerouter config` prints the resolved configuration with every secret redacted —
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