# Native install

Running CoreRouter directly on a host — no containers. Docker Compose is the
faster path to a working stack; this is the path for a machine you intend to
operate, where you want systemd to own the processes and the datastores to be
already-managed services.

The two paths share one configuration model and one set of code paths. Nothing
here behaves differently from the container image except where it lives on disk.

## Contents

- [Prerequisites](#prerequisites)
- [Datastores](#datastores)
- [Build and install the gateway](#build-and-install-the-gateway)
- [Configure](#configure)
- [Migrate](#migrate)
- [Run under systemd](#run-under-systemd)
- [Intelligence workers](#intelligence-workers)
- [Dashboard](#dashboard)
- [Observability](#observability)
- [Verify](#verify)
- [Upgrade](#upgrade)

---

## Prerequisites

| Component | Minimum | Notes |
| --- | --- | --- |
| Go | 1.27 | Only to build. The module pins `go 1.27.1`; `GOTOOLCHAIN` will fetch it if the local one is older. |
| PostgreSQL | 16 | The system of record. |
| Redis | 7 | Rate limits, budgets, credential cache. |
| ClickHouse | 24.8 | Optional. Traces and analytics; inference works without it. |
| NATS | 2.10 | Optional, **with JetStream enabled** (`-js`). Eval and replay need durable consumers. |
| Node.js | 20 | Only for the dashboard. |
| Python | 3.11 | Only for the workers. |

Nothing above is required to *start* the gateway. A missing optional dependency
is reported — as a degraded check in `/ready` and as a counter in
`corerouter_async_dropped_total` — rather than failing startup. PostgreSQL is the
exception: without it the gateway reports itself unready and a load balancer
should drain it.

## Datastores

Install them with your distribution's packages or from upstream. The only
CoreRouter-specific steps are the databases, users and the JetStream flag.

```bash
# PostgreSQL
sudo -u postgres psql <<'SQL'
CREATE ROLE corerouter LOGIN PASSWORD 'change-me';
CREATE DATABASE corerouter OWNER corerouter;
SQL

# Redis: appendonly keeps rate-limit and budget counters across a restart.
# Set appendonly yes in redis.conf.

# ClickHouse
clickhouse-client --query "CREATE DATABASE IF NOT EXISTS corerouter"

# NATS, with JetStream
nats-server -js -sd /var/lib/nats
```

Schema is **not** created here. CoreRouter embeds its migrations and applies them
itself, including to ClickHouse.

## Build and install the gateway

```bash
git clone https://github.com/corerouter/corerouter
cd corerouter

# Version, commit and build date are stamped into the binary and exported on
# corerouter_build_info, which is what makes "which build is running?" answerable.
make build          # → bin/corerouter

sudo install -m 0755 bin/corerouter /usr/local/bin/corerouter
corerouter version
```

Create the service account and directories:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin corerouter
sudo install -d -m 0750 -o corerouter -g corerouter /etc/corerouter
sudo install -d -m 0750 -o corerouter -g corerouter /var/lib/corerouter
```

## Configure

```bash
sudo install -m 0640 -o corerouter -g corerouter \
  config.example.yaml /etc/corerouter/config.yaml
sudo $EDITOR /etc/corerouter/config.yaml
```

Then put the secrets in a separate, tighter file. A unit file is world-readable;
an `EnvironmentFile` can be `0600`.

```bash
sudo install -d -m 0750 /etc/corerouter
sudo bash -c 'cat > /etc/corerouter/env <<EOF
CR_ADMIN_KEY='"$(openssl rand -hex 24)"'
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
EOF'
sudo chmod 0600 /etc/corerouter/env
sudo chown corerouter:corerouter /etc/corerouter/env
```

`CR_ADMIN_KEY` is not optional in production: with `admin.require_scope: true`,
validation refuses to start without it. That is deliberate — a production install
that cannot be administered is a worse outcome than one that refuses to boot.

Confirm the gateway sees what you intend, with secrets redacted:

```bash
sudo -u corerouter CR_CONFIG_FILE=/etc/corerouter/config.yaml corerouter config
```

## Migrate

`auto_migrate: true` applies migrations at startup, which is convenient for a
single host. Under change control, turn it off and migrate as a release step:

```bash
# In the config: database.auto_migrate: false
sudo -u corerouter CR_CONFIG_FILE=/etc/corerouter/config.yaml corerouter migrate
```

Migrations are applied in order, recorded with a checksum, and a mismatch on an
already-applied migration is an error rather than a silent re-run. Both Postgres
and ClickHouse are migrated by the same command.

## Run under systemd

```bash
sudo install -m 0644 deploy/systemd/corerouter.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now corerouter
systemctl status corerouter
```

The unit carries a sandbox — `ProtectSystem=strict`, `NoNewPrivileges`,
`PrivateTmp`, restricted address families — because the gateway needs no
privileges and keeps no writable state.

Give the gateway the secret file by adding a drop-in:

```bash
sudo systemctl edit corerouter
```

```ini
[Service]
EnvironmentFile=/etc/corerouter/env
```

Then:

```bash
sudo systemctl restart corerouter
journalctl -u corerouter -f
```

## Intelligence workers

The workers handle scoring, prompt analysis and telemetry rollups. They are a
separate process on purpose: that work is CPU-bound and bursty, and it must never
compete with the request path.

```bash
sudo install -d -m 0750 -o corerouter -g corerouter /opt/corerouter
sudo -u corerouter python3 -m venv /opt/corerouter/venv
sudo -u corerouter /opt/corerouter/venv/bin/pip install ./workers

sudo install -m 0644 deploy/systemd/corerouter-worker.service /etc/systemd/system/
sudo install -m 0600 -o corerouter -g corerouter /dev/null /etc/corerouter/worker-env
sudo bash -c 'cat > /etc/corerouter/worker-env <<EOF
CR_WORKER_NATS_URL=nats://localhost:4222
CR_WORKER_METRICS_ADDR=127.0.0.1:9101
CR_WORKER_LOG_FORMAT=json
EOF'
sudo chmod 0600 /etc/corerouter/worker-env

sudo systemctl daemon-reload
sudo systemctl enable --now corerouter-worker
```

To enable rubric-based evaluation with a local judge, fill in
`CR_WORKER_PROVIDER_URL` and `CR_WORKER_PROVIDER_MODEL`. Left empty, judging is
disabled and the deterministic scorers are the only signal.

## Dashboard

```bash
cd dashboard
npm ci
npm run build
```

Serve it however you prefer. The two things it needs:

| Variable | Value |
| --- | --- |
| `COREROUTER_API_URL` | The gateway as seen from the dashboard **server**, e.g. `http://127.0.0.1:8080` |
| `COREROUTER_ADMIN_KEY` | The same value as the gateway's `CR_ADMIN_KEY` |

The browser never holds the admin key: Next route handlers proxy `/admin/v1/*`
server-side. Put a reverse proxy in front and terminate TLS there.

With the standalone build:

```bash
NODE_ENV=production \
COREROUTER_API_URL=http://127.0.0.1:8080 \
COREROUTER_ADMIN_KEY=... \
PORT=3000 HOSTNAME=127.0.0.1 \
node dashboard/.next/standalone/server.js
```

## Temporary public tunnels

A native gateway can expose itself through a Cloudflare quick tunnel exactly
like the container does — the manager spawns `cloudflared` as a child
process, so no extra service is needed:

1. Install `cloudflared` for the service user and confirm
   `cloudflared --version` works.
2. Set `CR_TUNNEL_ENABLED=true` (or `tunnel.enabled: true`), optionally
   `CR_TUNNEL_BINARY=/usr/local/bin/cloudflared` to pin the path.
3. Restart the service and create the tunnel from the dashboard
   (`/tunnels`) or `POST /admin/v1/tunnels/create`.

See [tunnel.md](tunnel.md) for targets, security notes and the full API.

## Observability

The gateway exposes Prometheus metrics at `/metrics` and exports OTLP traces to
`telemetry.otlp_endpoint`. Point Prometheus at the gateway and at the workers'
`/metrics`, and load `deploy/prometheus/rules/corerouter.yml` for the alert set.

The configs under `deploy/` (collector, Loki, Promtail, Grafana provisioning and
dashboards) are written to be used directly; they only assume the network names
used by `docker-compose.yml`, so adjust the hostnames for a native layout.

## Verify

The same checks as the Docker path, against the native listener. `scripts/smoke.sh`
automates all of this; point it at a native install with overrides:

```bash
GATEWAY_URL=http://127.0.0.1:8080 DASHBOARD_URL=http://127.0.0.1:3000 bash scripts/smoke.sh
# Windows native: powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1 -GatewayUrl http://127.0.0.1:8080
```

```bash
# Liveness: must not depend on downstreams.
curl -s localhost:8080/health | jq

# Readiness: does check them, because this is what a load balancer asks.
curl -s localhost:8080/ready | jq
# → {"status":"ready","checks":{"postgres":"ok","providers":"2 configured",...}}

# Metrics are on the same listener.
curl -s localhost:8080/metrics | grep corerouter_build_info

# A real completion.
curl -s localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $COREROUTER_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}' | jq
```

If `/ready` reports `providers: 0 configured`, the gateway will refuse traffic:
at least one provider must have a working adapter. A provider with a missing
credential is configured but has no adapter, which is the usual cause.

## Upgrade

```bash
cd corerouter
git fetch --tags
git checkout vX.Y.Z
make build

sudo systemctl stop corerouter
sudo install -m 0755 bin/corerouter /usr/local/bin/corerouter

# Migrate before starting the new binary when auto_migrate is off.
sudo -u corerouter CR_CONFIG_FILE=/etc/corerouter/config.yaml corerouter migrate

sudo systemctl start corerouter
curl -s localhost:8080/version | jq
```

The gateway drains on `SIGTERM`: it stops accepting, finishes in-flight streaming
responses, flushes the telemetry buffer, then exits. The systemd unit allows 60
seconds for that, which must exceed `http.shutdown_timeout` plus the flush.

```bash
# Workers
sudo -u corerouter /opt/corerouter/venv/bin/pip install --upgrade ./workers
sudo systemctl restart corerouter-worker

# Dashboard
cd dashboard && npm ci && npm run build && sudo systemctl restart corerouter-dashboard
```
