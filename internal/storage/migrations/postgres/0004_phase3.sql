-- AstraRouter Phase 3: management and provisioning.
--
-- Phase 3 turns the catalogue from bootstrap-owned data into operator-managed
-- data. Providers, models and policies gain a managed_by marker so the
-- bootstrapper never overwrites a row an operator created or edited through
-- the admin API or dashboard. Provider credentials gain an encrypted store so
-- secrets can be saved through the UI instead of living only in environment
-- variables, and provider test runs gain a durable history table.
--
-- The migration is idempotent and safe to re-run.

-- ---------------------------------------------------------------------------
-- Providers: operator metadata + management ownership
-- ---------------------------------------------------------------------------

ALTER TABLE IF EXISTS providers
    ADD COLUMN IF NOT EXISTS notes TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS providers
    ADD COLUMN IF NOT EXISTS environment TEXT NOT NULL DEFAULT 'production';

ALTER TABLE IF EXISTS providers
    ADD COLUMN IF NOT EXISTS managed_by TEXT NOT NULL DEFAULT 'bootstrap';

ALTER TABLE IF EXISTS providers
    DROP CONSTRAINT IF EXISTS providers_environment_check;

ALTER TABLE IF EXISTS providers
    ADD CONSTRAINT providers_environment_check
        CHECK (environment IN ('production', 'internal', 'external', 'test'));

ALTER TABLE IF EXISTS providers
    DROP CONSTRAINT IF EXISTS providers_managed_by_check;

ALTER TABLE IF EXISTS providers
    ADD CONSTRAINT providers_managed_by_check
        CHECK (managed_by IN ('bootstrap', 'api'));

-- ---------------------------------------------------------------------------
-- Models: environment, fallback priority, management ownership
-- ---------------------------------------------------------------------------

ALTER TABLE IF EXISTS models
    ADD COLUMN IF NOT EXISTS environment TEXT NOT NULL DEFAULT 'production';

-- Priority orders models of the same provider when a policy does not specify
-- an order. Lower values are preferred, mirroring providers.priority.
ALTER TABLE IF EXISTS models
    ADD COLUMN IF NOT EXISTS priority INTEGER NOT NULL DEFAULT 100;

ALTER TABLE IF EXISTS models
    ADD COLUMN IF NOT EXISTS managed_by TEXT NOT NULL DEFAULT 'bootstrap';

ALTER TABLE IF EXISTS models
    DROP CONSTRAINT IF EXISTS models_environment_check;

ALTER TABLE IF EXISTS models
    ADD CONSTRAINT models_environment_check
        CHECK (environment IN ('production', 'internal', 'external', 'test'));

ALTER TABLE IF EXISTS models
    DROP CONSTRAINT IF EXISTS models_managed_by_check;

ALTER TABLE IF EXISTS models
    ADD CONSTRAINT models_managed_by_check
        CHECK (managed_by IN ('bootstrap', 'api'));

-- ---------------------------------------------------------------------------
-- Routing policies: management ownership
-- ---------------------------------------------------------------------------

ALTER TABLE IF EXISTS routing_policies
    ADD COLUMN IF NOT EXISTS managed_by TEXT NOT NULL DEFAULT 'bootstrap';

ALTER TABLE IF EXISTS routing_policies
    DROP CONSTRAINT IF EXISTS routing_policies_managed_by_check;

ALTER TABLE IF EXISTS routing_policies
    ADD CONSTRAINT routing_policies_managed_by_check
        CHECK (managed_by IN ('bootstrap', 'api'));

-- ---------------------------------------------------------------------------
-- Provider credentials (encrypted secrets)
-- ---------------------------------------------------------------------------
--
-- One active credential per provider. The secret is stored AES-256-GCM
-- encrypted; the gateway decrypts it at adapter-build time only. Plaintext
-- secrets never appear in logs, API responses, or dashboard views.

CREATE TABLE IF NOT EXISTS provider_credentials (
    provider_id UUID PRIMARY KEY REFERENCES providers (id) ON DELETE CASCADE,
    -- Name is a human label ("primary", "rotated 2026-09") for the audit view.
    name        TEXT        NOT NULL DEFAULT 'default',
    ciphertext  BYTEA       NOT NULL,
    nonce       BYTEA       NOT NULL,
    -- KeyVersion identifies which data key sealed the value, so a future key
    -- rotation can distinguish old rows.
    key_version INTEGER     NOT NULL DEFAULT 1,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Provider test results (connectivity checks)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS provider_test_results (
    id          UUID        PRIMARY KEY,
    provider_id UUID        NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    -- Kind is one of connectivity, models, sample.
    kind        TEXT        NOT NULL,
    success     BOOLEAN     NOT NULL DEFAULT FALSE,
    latency_ms  BIGINT      NOT NULL DEFAULT 0,
    status_code INTEGER     NOT NULL DEFAULT 0,
    message     TEXT        NOT NULL DEFAULT '',
    detail      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT provider_test_results_kind_check
        CHECK (kind IN ('connectivity', 'models', 'sample'))
);

CREATE INDEX IF NOT EXISTS provider_test_results_provider_idx
    ON provider_test_results (provider_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Audit enumerations: test action + credential resource
-- ---------------------------------------------------------------------------

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_action_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_action_check
        CHECK (action IN ('create', 'update', 'delete', 'login', 'rotate', 'revoke',
                          'allow', 'deny', 'override', 'probe', 'test', 'enable', 'disable'));

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_resource_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_resource_check
        CHECK (resource IN ('tenant', 'api_key', 'provider', 'model', 'routing_policy',
                            'budget', 'settings', 'endpoint', 'override', 'replay_job',
                            'evaluation', 'cache', 'feedback', 'credential', 'test_result'));
