-- AstraRouter tunnel sessions: temporary public exposure audit trail.
--
-- One row per tunnel session: what was exposed, where it pointed, the public
-- URL Cloudflare minted, and how the session ended. Rows are write-mostly and
-- tiny; retention is the operator's choice via the standard pruning tooling.
-- Only one session per gateway may be non-terminal at a time; that invariant
-- is enforced by the manager, not by a partial unique index, so a crashed
-- gateway can always be reconciled on restart without manual cleanup.

CREATE TABLE IF NOT EXISTS tunnel_sessions (
    id          UUID        PRIMARY KEY,
    tenant_id   UUID        REFERENCES tenants (id) ON DELETE SET NULL,
    target      TEXT        NOT NULL DEFAULT 'gateway',
    target_addr TEXT        NOT NULL DEFAULT '',
    public_url  TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL DEFAULT 'starting',
    started_at  TIMESTAMPTZ,
    url_at      TIMESTAMPTZ,
    stopped_at  TIMESTAMPTZ,
    stop_reason TEXT        NOT NULL DEFAULT '',
    last_error  TEXT        NOT NULL DEFAULT '',
    reconnects  INTEGER     NOT NULL DEFAULT 0,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tunnel_sessions_status_check
        CHECK (status IN ('starting', 'running', 'stopped', 'failed'))
);

CREATE INDEX IF NOT EXISTS tunnel_sessions_status_idx ON tunnel_sessions (status, created_at DESC);
CREATE INDEX IF NOT EXISTS tunnel_sessions_tenant_idx ON tunnel_sessions (tenant_id, created_at DESC) WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tunnel_sessions_created_idx ON tunnel_sessions (created_at DESC);
