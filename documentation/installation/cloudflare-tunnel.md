# Installation: Cloudflare tunnel

AstraRouter can expose one local service to the public internet through a
temporary Cloudflare tunnel — no port forwarding, no DNS, no firewall changes.
Creating a tunnel mints a disposable `*.trycloudflare.com` URL that works from
anywhere until you stop it.

## How it works

1. You create a tunnel, naming what to expose: `gateway`, `dashboard`, or an
   explicit loopback `host:port`.
2. The gateway spawns `cloudflared tunnel --url <target>` as a supervised child
   process and captures the public URL from its output.
3. The URL is stored on the session row and shown in the dashboard and API
   response — usable immediately.
4. Stopping (or restarting, or gateway shutdown) kills the process and the URL
   dies with it. A restart always mints a **new** URL.

Everything through the tunnel is still AstraRouter: the same authentication,
routing policies, rate limits, budgets and cache. The tunnel is a network path,
not a bypass. Admin surfaces stay behind the admin credential, exactly as on
localhost.

Opening the tunnel URL in a browser lands on a small landing page naming the
endpoints rather than a bare 404 — a gateway reached from a phone should tell you
where to go, not look broken.

## Targets

| Target | Exposes | Notes |
| --- | --- | --- |
| `gateway` (default) | The inference API on the gateway's own port | What remote apps and phones usually need. API keys still required. |
| `dashboard` | The Next.js UI | Convenient, but it is an admin surface: anyone with the URL still needs the admin credential. Prefer `gateway` unless you need the UI remotely. The dashboard warns you. |
| `127.0.0.1:port` | Any local port you name | Loopback only — the manager refuses remote hosts, so a tunnel can never turn AstraRouter into a proxy for someone else's infrastructure. |

## Docker

The gateway image ships a pinned `cloudflared` binary, so there is nothing to
install:

```dotenv
# One-time: allow creation. This exposes nothing by itself.
AR_TUNNEL_ENABLED=true
```

```bash
docker compose up -d gateway
```

Then open the dashboard → **Tunnels** → Create, or:

```bash
curl -s $GATEWAY/admin/v1/tunnels/create \
  -H "Authorization: Bearer $AR_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"target":"gateway"}' | jq
```

The compose file already sets `AR_TUNNEL_DASHBOARD_TARGET=http://dashboard:3000`,
so the `dashboard` target resolves inside the network.

Quick tunnels need outbound HTTPS (and ideally UDP) to Cloudflare from the
gateway container. No inbound ports are required — that is the point.

## Native install

1. Install `cloudflared` on the gateway host and confirm `cloudflared --version`
   works for the service user:

   ```bash
   winget install cloudflare.cloudflared    # Windows
   brew install cloudflared                  # macOS
   # or the .deb / .rpm from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
   ```

2. Enable creation in `/etc/astrarouter/config.yaml`:

   ```yaml
   tunnel:
     enabled: true
   ```

3. Restart and create the tunnel from the dashboard or API as above.

```bash
sudo systemctl edit astrarouter
```

```ini
[Service]
Environment=AR_TUNNEL_ENABLED=true
Environment=AR_TUNNEL_BINARY=/usr/local/bin/cloudflared
```

If `cloudflared` is missing, creation fails with a `404` naming the binary and
where to get it — the gateway itself is unaffected. Pin a managed install with
`tunnel.binary`.

## API

All tunnel endpoints live under `/admin/v1` and require admin authorization;
mutations require full admin scope.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/tunnels/create` | `{"target":"gateway"}` (default). `201` with the running session including the public URL. `409` when one is already active. |
| `POST` | `/tunnels/stop` | `{"id":"…"}` optional; empty stops the active tunnel. Stopping an already-terminal id returns its row. |
| `POST` | `/tunnels/restart` | `{"target":"…"}` optional (defaults to the active target). Mints a new URL. |
| `GET` | `/tunnels/status` | Never fails: `{enabled, binary_available, binary_path, active}` for dashboards and scripts to branch on. |
| `GET` | `/tunnels/current-url` | Stable shape with `url: null` when idle — no error parsing needed. |
| `GET` | `/tunnels/history?limit=` | Recent sessions: target, URL, status, reconnects, teardown reason. |

Every mutation writes an audit event (`resource: tunnel`) and publishes a
`ar.tunnel.status` NATS event.

## Observability

- Dashboard `/tunnels`: status cards, copyable URL, target selector with an
  admin-surface warning, session history and binary-missing guidance.
- Prometheus: `astrarouter_tunnel_up{target}` (1 while exposed),
  `astrarouter_tunnel_sessions_total{target,outcome}`,
  `astrarouter_tunnel_restarts_total{target}`.
- Logs: creation, public URL capture, unexpected exits with reconnect counts,
  shutdown teardown — all tagged with the session id.
- PostgreSQL `tunnel_sessions`: the full audit trail. A restart crash-marks
  stale rows on boot, so a dead tunnel is never reported as running.

## Security

- **Opt in twice:** the feature flag *and* an explicit create action. Enabling
  the flag exposes nothing.
- **Authentication is unchanged** through the tunnel or not: inference needs an
  API key, admin needs the admin credential. Health probes are public by design,
  on localhost too.
- **Only loopback destinations** can be exposed; remote `host:port` targets are
  rejected before any process spawns. Set `tunnel.allow_custom_targets: false`
  to disable custom targets entirely.
- **Treat a live URL as public.** Anyone holding it can reach the same surfaces
  someone on your LAN could. URLs are random per session and die with the
  process.
- **Prefer the `gateway` target.** The `dashboard` target puts the admin UI on
  the internet — useful, but say so in your change log.

## Limits

- One active tunnel per gateway. To expose two services at once, run a second
  gateway or a manual `cloudflared` alongside.
- Quick tunnels only: no custom domains, no Cloudflare Access policies, no
  tunnel tokens.
- No automatic expiry timer. Stop the tunnel when you are done, or restart the
  gateway — shutdown teardown stops it when `stop_on_shutdown` is true, which is
  the default.

---

Related: [Installation: Docker](docker.md) · [Installation: native](native.md) · [Dashboard](../dashboard.md) · [Back to README](../../README.md)