# Temporary public tunnels

CoreRouter can expose one local service to the public internet through a
temporary Cloudflare tunnel — no port forwarding, no DNS, no firewall
changes. Creating a tunnel mints a disposable `*.trycloudflare.com` URL that
works from anywhere in the world until you stop it.

## How it works

1. You create a tunnel (dashboard `/tunnels` or `POST /admin/v1/tunnels/create`),
   naming what to expose: `gateway`, `dashboard`, or an explicit loopback
   `host:port`.
2. The gateway spawns `cloudflared tunnel --url <target>` as a supervised
   child process and captures the public URL from its output.
3. The URL is stored on the session row and shown in the dashboard and API
   response — usable immediately.
4. Stopping (or restarting, or gateway shutdown) kills the process; the URL
   dies with it. A restart always mints a **new** URL.

Everything through the tunnel is still CoreRouter: the same authentication,
routing policies, rate limits, budgets and cache. The tunnel is a network
path, not a bypass. Admin surfaces stay behind the admin credential, exactly
as on localhost.

Opening the tunnel URL in a browser lands on a small landing page that names
the endpoints (`POST /v1/chat/completions`, `GET /v1/models`, `/health`,
`/admin/v1`) rather than a bare 404 — a gateway reached from a phone should
tell you where to go, not look broken.

## Targets

| Target | Exposes | Notes |
| --- | --- | --- |
| `gateway` (default) | The inference API on the gateway's own port | What remote apps and phones usually need. API keys still required. |
| `dashboard` | The Next.js UI | Convenient, but it is an admin surface: anyone with the URL still needs the admin credential, and you should prefer `gateway` unless you need the UI remotely. The dashboard warns you. |
| `127.0.0.1:port` | Any local port you name | Loopback only — the manager refuses remote hosts, so a tunnel can never turn CoreRouter into a proxy for someone else's infrastructure. Disable entirely with `tunnel.allow_custom_targets: false`. |

## Docker

The gateway image ships a pinned `cloudflared` binary, so there is nothing
to install:

```powershell
# One-time: allow creation (still exposes nothing by itself).
$env:CR_TUNNEL_ENABLED = "true"
docker compose up -d gateway
```

Then open the dashboard → **Tunnels** → Create, or:

```powershell
$headers = @{ Authorization = "Bearer $env:CR_ADMIN_KEY" }
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:18080/admin/v1/tunnels/create `
  -Headers $headers -ContentType "application/json" -Body '{"target":"gateway"}'
```

To point the `dashboard` target at the UI inside Docker, the compose file
already sets `CR_TUNNEL_DASHBOARD_TARGET=http://dashboard:3000`.

Quick tunnels need outbound HTTPS (and ideally UDP) to Cloudflare from the
gateway container. No inbound ports are required — that is the point.

## Native install

1. Install `cloudflared` on the gateway host from
   https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
   (`winget install cloudflare.cloudflared`, `choco install cloudflared`,
   or the `.deb`/`.rpm`), and confirm `cloudflared --version` works for the
   service user.
2. Enable creation in `/etc/corerouter/config.yaml` (or
   `CR_TUNNEL_ENABLED=true` in the systemd drop-in):

   ```yaml
   tunnel:
     enabled: true
   ```

3. Restart the service (`systemctl restart corerouter`) and create the tunnel
   from the dashboard or API as above.

If `cloudflared` is missing, creation fails with a 404 naming the binary and
where to get it — the gateway itself is unaffected. To pin a managed install,
set `tunnel.binary` to the absolute path.

A minimal systemd drop-in (`/etc/systemd/system/corerouter.service.d/tunnel.conf`):

```ini
[Service]
Environment=CR_TUNNEL_ENABLED=true
```

## API

All tunnel endpoints live under `/admin/v1` and require admin authorization;
mutations require full admin scope.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/tunnels/create` | `{"target":"gateway"}` (default). 201 with the running session, including the public URL. 409 when one is already active. |
| `POST` | `/tunnels/stop` | `{"id":"…"}` optional; empty stops the active tunnel. Stopping an already-terminal id returns its row. |
| `POST` | `/tunnels/restart` | `{"target":"…"}` optional (defaults to the active target). Mints a new URL. |
| `GET` | `/tunnels/status` | Never fails: `{enabled, binary_available, binary_path, active}` for dashboards and scripts to branch on. |
| `GET` | `/tunnels/current-url` | Stable shape with `url: null` when idle — no error parsing needed. |
| `GET` | `/tunnels/history?limit=` | Recent sessions: target, URL, status, reconnects, teardown reason. |

Every mutation writes an audit event (`resource: tunnel`) and publishes a
`cr.tunnel.status` NATS event.

## Observability

- Dashboard `/tunnels`: status cards, copyable URL, target selector with an
  admin-surface warning, session history, binary-missing guidance.
- Prometheus: `corerouter_tunnel_up{target}` (1 while exposed),
  `corerouter_tunnel_sessions_total{target,outcome}`,
  `corerouter_tunnel_restarts_total{target}`.
- Logs: creation, public URL capture, unexpected exits with reconnect counts,
  shutdown teardown — all tagged with the session id.
- PostgreSQL `tunnel_sessions`: the full audit trail (target, URL, timing,
  stop reason, errors). A restart crash-marks stale rows on boot, so a dead
  tunnel is never reported as running.

## Security notes

- Opt-in twice: the feature flag **and** an explicit create action. Enabling
  the flag exposes nothing.
- Auth is unchanged: inference needs an API key, admin needs the admin
  credential, through the tunnel or otherwise. Health probes are public by
  design, on localhost too.
- Only loopback destinations can be exposed; remote `host:port` targets are
  rejected before any process spawns.
- Tunnel URLs are random per session and die with the process. Treat a live
  URL as public: anyone who has it can reach the login/keys surface the same
  way they could on your LAN.
- Prefer the `gateway` target. The `dashboard` target puts the admin UI on
  the internet — useful, but say so in your change log.

## Limits (non-goals for this phase)

- One active tunnel per gateway. Need two services public at once? Run a
  second gateway or a manual `cloudflared` alongside.
- No custom domains, no Cloudflare Access policies, no tunnel tokens — quick
  tunnels only. A named-tunnel follow-up would add those without changing
  this surface.
- No automatic expiry timer: stop the tunnel when you are done, or restart
  the gateway (shutdown teardown stops it when `stop_on_shutdown` is true,
  the default).
