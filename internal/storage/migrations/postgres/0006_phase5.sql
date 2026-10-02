-- Synapass Phase 5 schema: local request cache policy and audit.
--
-- The live body always lives in Redis. These tables hold the policy rows that
-- decide when caching applies, the invalidation audit trail, and lightweight
-- entry metadata for the dashboard (top prompts, hit counts). Additive only.

-- ---------------------------------------------------------------------------
-- Cache policies (per-scope rules overriding the global config)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cache_policies (
    id              UUID        PRIMARY KEY,
    tenant_id       UUID        REFERENCES tenants (id) ON DELETE CASCADE,
    endpoint_id     TEXT        NOT NULL DEFAULT '',
    api_key_id      UUID,
    provider        TEXT        NOT NULL DEFAULT '',
    model           TEXT        NOT NULL DEFAULT '',
    name            TEXT        NOT NULL DEFAULT '',
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    ttl_seconds     INTEGER     NOT NULL DEFAULT 300,
    semantic_enabled BOOLEAN    NOT NULL DEFAULT FALSE,
    semantic_threshold DOUBLE PRECISION NOT NULL DEFAULT 0.92,
    prefix_enabled  BOOLEAN     NOT NULL DEFAULT FALSE,
    bypass_tools    BOOLEAN     NOT NULL DEFAULT TRUE,
    allow_nondeterministic BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS cache_policies_tenant_idx ON cache_policies (tenant_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS cache_policies_endpoint_idx ON cache_policies (endpoint_id) WHERE endpoint_id <> '';
CREATE INDEX IF NOT EXISTS cache_policies_provider_idx ON cache_policies (provider) WHERE provider <> '';

-- ---------------------------------------------------------------------------
-- Cache invalidations (audit trail for every flush)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cache_invalidations (
    id         UUID        PRIMARY KEY,
    tenant_id  UUID        REFERENCES tenants (id) ON DELETE SET NULL,
    scope      TEXT        NOT NULL DEFAULT 'tenant',
    target     TEXT        NOT NULL DEFAULT '',
    reason     TEXT        NOT NULL DEFAULT 'manual_flush',
    actor      TEXT        NOT NULL DEFAULT '',
    removed    INTEGER     NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT cache_invalidations_scope_check
        CHECK (scope IN ('tenant', 'model', 'provider', 'key', 'all'))
);

CREATE INDEX IF NOT EXISTS cache_invalidations_tenant_idx ON cache_invalidations (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS cache_invalidations_created_idx ON cache_invalidations (created_at DESC);

-- ---------------------------------------------------------------------------
-- Cache entries: Phase 5 serving-context columns
-- ---------------------------------------------------------------------------

ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';
ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS policy_id TEXT NOT NULL DEFAULT '';
ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS endpoint_id TEXT NOT NULL DEFAULT '';
ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS api_key_id UUID;
ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS prompt_preview TEXT NOT NULL DEFAULT '';
ALTER TABLE cache_entries ADD COLUMN IF NOT EXISTS reuse_count BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS cache_entries_model_idx ON cache_entries (model, created_at DESC);
CREATE INDEX IF NOT EXISTS cache_entries_provider_idx ON cache_entries (provider, created_at DESC) WHERE provider <> '';
