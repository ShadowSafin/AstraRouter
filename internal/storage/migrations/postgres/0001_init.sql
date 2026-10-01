-- CoreRouter initial schema.
--
-- Design notes that matter for later evolution:
--
--  * Identifiers are UUIDs minted by the application, so a row can be created
--    without a write-then-read round trip and ids stay unique across replicas
--    and across an export/import.
--  * Enumerated values are TEXT with CHECK constraints rather than native enums.
--    Adding a value to a Postgres enum requires a schema change that cannot run
--    inside a transaction with other work; a CHECK constraint can be replaced
--    cheaply.
--  * Complex nested policy structures are JSONB rather than normalized tables.
--    A routing policy is read and written whole, never queried by its inner
--    fields, so normalizing it would add joins on the hot path for no benefit.
--  * Timestamps are TIMESTAMPTZ everywhere; the application stores UTC.
--  * Deleting a tenant cascades to its keys, policies and budgets, but NOT to
--    usage_records: billing history must survive tenant deletion for audit.

-- Statements are applied by the migration runner inside a single transaction, so
-- this file deliberately contains no BEGIN or COMMIT of its own.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT        PRIMARY KEY,
    checksum   TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Tenants
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tenants (
    id                       UUID        PRIMARY KEY,
    slug                     TEXT        NOT NULL,
    name                     TEXT        NOT NULL,
    status                   TEXT        NOT NULL DEFAULT 'active',
    plan                     TEXT        NOT NULL DEFAULT '',
    default_routing_policy_id UUID,
    labels                   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenants_status_check
        CHECK (status IN ('active', 'disabled', 'pending', 'revoked')),
    CONSTRAINT tenants_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{1,62}$')
);

CREATE UNIQUE INDEX IF NOT EXISTS tenants_slug_key ON tenants (slug);

-- ---------------------------------------------------------------------------
-- API keys
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS api_keys (
    id                UUID        PRIMARY KEY,
    tenant_id         UUID        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name              TEXT        NOT NULL DEFAULT '',
    -- prefix is the non-secret display excerpt; key_hash is the SHA-256 digest.
    -- The plaintext token is never persisted.
    prefix            TEXT        NOT NULL,
    key_hash          TEXT        NOT NULL,
    scopes            TEXT[]      NOT NULL DEFAULT '{}',
    status            TEXT        NOT NULL DEFAULT 'active',
    expires_at        TIMESTAMPTZ,
    last_used_at      TIMESTAMPTZ,
    routing_policy_id UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    created_by        TEXT        NOT NULL DEFAULT '',
    CONSTRAINT api_keys_status_check
        CHECK (status IN ('active', 'revoked', 'expired'))
);

-- The authentication path is a single lookup by digest.
CREATE UNIQUE INDEX IF NOT EXISTS api_keys_hash_key ON api_keys (key_hash);
CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys (tenant_id);
-- Used by the dashboard to list keys and by support to find a key from a prefix.
CREATE INDEX IF NOT EXISTS api_keys_prefix_idx ON api_keys (prefix);
CREATE INDEX IF NOT EXISTS api_keys_status_idx ON api_keys (status) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Providers
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS providers (
    id              UUID        PRIMARY KEY,
    name            TEXT        NOT NULL,
    kind            TEXT        NOT NULL,
    base_url        TEXT        NOT NULL,
    -- api_key_env names the environment variable holding the credential. Secrets
    -- deliberately do not live in this table.
    api_key_env     TEXT        NOT NULL DEFAULT '',
    auth_style      TEXT        NOT NULL DEFAULT 'bearer',
    header_name     TEXT        NOT NULL DEFAULT '',
    headers         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    organization    TEXT        NOT NULL DEFAULT '',
    project         TEXT        NOT NULL DEFAULT '',
    capabilities    TEXT[]      NOT NULL DEFAULT '{}',
    status          TEXT        NOT NULL DEFAULT 'active',
    weight          INTEGER     NOT NULL DEFAULT 1,
    priority        INTEGER     NOT NULL DEFAULT 100,
    timeout_ms      INTEGER     NOT NULL DEFAULT 0,
    max_concurrency INTEGER     NOT NULL DEFAULT 0,
    region          TEXT        NOT NULL DEFAULT '',
    labels          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT providers_kind_check
        CHECK (kind IN ('openai', 'anthropic', 'ollama', 'vllm', 'openai_compatible')),
    CONSTRAINT providers_status_check
        CHECK (status IN ('active', 'degraded', 'disabled', 'pending')),
    CONSTRAINT providers_auth_style_check
        CHECK (auth_style IN ('bearer', 'header', 'none')),
    CONSTRAINT providers_weight_check CHECK (weight >= 0),
    CONSTRAINT providers_name_format CHECK (name <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS providers_name_key ON providers (name);
CREATE INDEX IF NOT EXISTS providers_status_idx ON providers (status);

-- ---------------------------------------------------------------------------
-- Models (registry)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS models (
    id                          UUID        PRIMARY KEY,
    provider_id                 UUID        NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    name                        TEXT        NOT NULL,
    aliases                     TEXT[]      NOT NULL DEFAULT '{}',
    display_name                TEXT        NOT NULL DEFAULT '',
    version                     TEXT        NOT NULL DEFAULT '',
    context_window              INTEGER     NOT NULL DEFAULT 0,
    max_output_tokens           INTEGER     NOT NULL DEFAULT 0,
    capabilities                TEXT[]      NOT NULL DEFAULT '{}',
    -- Pricing is USD per one million tokens, matching provider price sheets so
    -- operators can copy values without unit conversion.
    input_cost_per_million        DOUBLE PRECISION NOT NULL DEFAULT 0,
    output_cost_per_million       DOUBLE PRECISION NOT NULL DEFAULT 0,
    cached_input_cost_per_million DOUBLE PRECISION NOT NULL DEFAULT 0,
    status                      TEXT        NOT NULL DEFAULT 'active',
    quality_tier                INTEGER     NOT NULL DEFAULT 0,
    rate_limit_rpm              INTEGER     NOT NULL DEFAULT 0,
    rate_limit_tpm              INTEGER     NOT NULL DEFAULT 0,
    deprecated_at               TIMESTAMPTZ,
    metadata                    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT models_status_check
        CHECK (status IN ('active', 'degraded', 'deprecated', 'disabled')),
    CONSTRAINT models_pricing_check
        CHECK (input_cost_per_million >= 0 AND output_cost_per_million >= 0),
    CONSTRAINT models_quality_tier_check
        CHECK (quality_tier BETWEEN 0 AND 5),
    CONSTRAINT models_provider_name_key UNIQUE (provider_id, name)
);

-- Alias lookup is a containment query, so a GIN index is the right structure.
CREATE INDEX IF NOT EXISTS models_aliases_idx ON models USING GIN (aliases);
CREATE INDEX IF NOT EXISTS models_aname_idx ON models (name);
CREATE INDEX IF NOT EXISTS models_provider_idx ON models (provider_id);

-- ---------------------------------------------------------------------------
-- Routing policies
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS routing_policies (
    id          UUID        PRIMARY KEY,
    -- A NULL tenant_id marks a platform-wide policy.
    tenant_id   UUID        REFERENCES tenants (id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    -- Lower priority wins a tie between equally specific matches.
    priority    INTEGER     NOT NULL DEFAULT 100,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    match       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    strategy    TEXT        NOT NULL DEFAULT 'priority',
    targets     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    fallback    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    retry       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    timeout     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    limits      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- version increments on every write and is cited in audit events.
    version     INTEGER     NOT NULL DEFAULT 1,
    created_by  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT routing_policies_strategy_check
        CHECK (strategy IN ('priority', 'weighted', 'lowest_cost', 'lowest_latency', 'highest_quality')),
    CONSTRAINT routing_policies_targets_check
        CHECK (jsonb_typeof(targets) = 'array'),
    CONSTRAINT routing_policies_name_format CHECK (name <> '')
);

-- Postgres does not consider two NULLs equal in a unique index, so a global
-- policy name would not be protected by a plain UNIQUE constraint. Two partial
-- indexes express the intent precisely.
CREATE UNIQUE INDEX IF NOT EXISTS routing_policies_tenant_name_key
    ON routing_policies (tenant_id, name) WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS routing_policies_global_name_key
    ON routing_policies (name) WHERE tenant_id IS NULL;
CREATE INDEX IF NOT EXISTS routing_policies_enabled_idx
    ON routing_policies (priority, id) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Usage records (billing system of record)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS usage_records (
    id                 UUID        PRIMARY KEY,
    request_id         TEXT        NOT NULL,
    trace_id           TEXT        NOT NULL DEFAULT '',
    -- Nullable, and deliberately NOT a foreign key with cascade: billing history
    -- must outlive the tenant row it refers to.
    tenant_id          UUID,
    api_key_id         UUID,
    provider           TEXT        NOT NULL DEFAULT '',
    model              TEXT        NOT NULL DEFAULT '',
    requested_model    TEXT        NOT NULL DEFAULT '',
    policy_id          UUID,
    request_type       TEXT        NOT NULL DEFAULT '',
    prompt_tokens      INTEGER     NOT NULL DEFAULT 0,
    completion_tokens  INTEGER     NOT NULL DEFAULT 0,
    total_tokens       INTEGER     NOT NULL DEFAULT 0,
    cached_prompt_tokens INTEGER   NOT NULL DEFAULT 0,
    -- estimated marks usage the provider did not report, keeping estimates out
    -- of exact billing reports.
    estimated          BOOLEAN     NOT NULL DEFAULT FALSE,
    cost_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_ms         BIGINT      NOT NULL DEFAULT 0,
    provider_latency_ms BIGINT     NOT NULL DEFAULT 0,
    attempts           INTEGER     NOT NULL DEFAULT 0,
    fallback_used      BOOLEAN     NOT NULL DEFAULT FALSE,
    cache_hit          BOOLEAN     NOT NULL DEFAULT FALSE,
    outcome            TEXT        NOT NULL DEFAULT 'success',
    error_code         TEXT        NOT NULL DEFAULT '',
    streaming          BOOLEAN     NOT NULL DEFAULT FALSE,
    status             INTEGER     NOT NULL DEFAULT 200,
    client_ip          TEXT        NOT NULL DEFAULT '',
    user_agent         TEXT        NOT NULL DEFAULT '',
    end_user           TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT usage_records_outcome_check
        CHECK (outcome IN ('success', 'fallback', 'error', 'rejected', 'canceled')),
    CONSTRAINT usage_records_tokens_check
        CHECK (prompt_tokens >= 0 AND completion_tokens >= 0 AND total_tokens >= 0)
);

-- The dashboard's default view is "recent traffic for a tenant", so the
-- composite index leads with tenant and orders by time.
CREATE INDEX IF NOT EXISTS usage_records_tenant_created_idx
    ON usage_records (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_created_idx
    ON usage_records (created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_provider_created_idx
    ON usage_records (provider, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_model_created_idx
    ON usage_records (model, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_request_idx ON usage_records (request_id);
-- Partial index over failures keeps the error view fast without indexing the
-- overwhelming majority of successful rows twice.
CREATE INDEX IF NOT EXISTS usage_records_errors_idx
    ON usage_records (created_at DESC) WHERE outcome IN ('error', 'rejected');

-- ---------------------------------------------------------------------------
-- Request logs (per-request detail for the dashboard)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS request_logs (
    id                 UUID        PRIMARY KEY,
    request_id         TEXT        NOT NULL,
    trace_id           TEXT        NOT NULL DEFAULT '',
    tenant_id          UUID,
    api_key_id         UUID,
    requested_model    TEXT        NOT NULL DEFAULT '',
    routed_model       TEXT        NOT NULL DEFAULT '',
    provider           TEXT        NOT NULL DEFAULT '',
    policy_id          UUID,
    strategy           TEXT        NOT NULL DEFAULT '',
    status             INTEGER     NOT NULL DEFAULT 0,
    outcome            TEXT        NOT NULL DEFAULT '',
    error_code         TEXT        NOT NULL DEFAULT '',
    error_message      TEXT        NOT NULL DEFAULT '',
    latency_ms         BIGINT      NOT NULL DEFAULT 0,
    attempts           INTEGER     NOT NULL DEFAULT 0,
    fallback_used      BOOLEAN     NOT NULL DEFAULT FALSE,
    prompt_tokens      INTEGER     NOT NULL DEFAULT 0,
    completion_tokens  INTEGER     NOT NULL DEFAULT 0,
    cost_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- decision is the full RouteDecision, stored so an operator can answer "why
    -- did this request go there?" long after the in-memory state is gone.
    decision           JSONB,
    client_ip          TEXT        NOT NULL DEFAULT '',
    user_agent         TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS request_logs_request_id_key ON request_logs (request_id);
CREATE INDEX IF NOT EXISTS request_logs_created_idx ON request_logs (created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_tenant_created_idx
    ON request_logs (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_status_idx ON request_logs (status);
CREATE INDEX IF NOT EXISTS request_logs_errors_idx
    ON request_logs (created_at DESC) WHERE status >= 400;

-- ---------------------------------------------------------------------------
-- Audit events (append-only control-plane history)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS audit_events (
    id          UUID        PRIMARY KEY,
    action      TEXT        NOT NULL,
    resource    TEXT        NOT NULL,
    resource_id TEXT        NOT NULL DEFAULT '',
    tenant_id   UUID,
    actor_key_id UUID,
    -- actor_label is captured at write time so the event stays readable after
    -- the actor is deleted.
    actor_label TEXT        NOT NULL DEFAULT '',
    actor_ip    TEXT        NOT NULL DEFAULT '',
    before      JSONB,
    after       JSONB,
    metadata    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    request_id  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_action_check
        CHECK (action IN ('create', 'update', 'delete', 'login', 'rotate', 'revoke')),
    CONSTRAINT audit_events_resource_check
        CHECK (resource IN ('tenant', 'api_key', 'provider', 'model', 'routing_policy', 'budget', 'settings'))
);

CREATE INDEX IF NOT EXISTS audit_events_created_idx ON audit_events (created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_resource_idx
    ON audit_events (resource, resource_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_events_tenant_idx ON audit_events (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Budgets
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS budgets (
    id                 UUID        PRIMARY KEY,
    tenant_id          UUID        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    -- scope narrows the budget to tenant, key or policy level.
    scope              TEXT        NOT NULL DEFAULT 'tenant',
    scope_id           TEXT        NOT NULL DEFAULT '',
    period             TEXT        NOT NULL DEFAULT 'monthly',
    limit_usd          DOUBLE PRECISION NOT NULL,
    -- spent_usd is a reconciled figure; the authoritative counter lives in Redis
    -- and is flushed here periodically.
    spent_usd          DOUBLE PRECISION NOT NULL DEFAULT 0,
    enforced           BOOLEAN     NOT NULL DEFAULT TRUE,
    alert_thresholds_usd DOUBLE PRECISION[] NOT NULL DEFAULT '{}',
    reset_at           TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT budgets_scope_check CHECK (scope IN ('tenant', 'key', 'policy')),
    CONSTRAINT budgets_period_check
        CHECK (period IN ('daily', 'weekly', 'monthly', 'total')),
    CONSTRAINT budgets_limit_check CHECK (limit_usd >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS budgets_scope_key
    ON budgets (tenant_id, scope, scope_id, period);
CREATE INDEX IF NOT EXISTS budgets_tenant_idx ON budgets (tenant_id);

-- ---------------------------------------------------------------------------
-- Provider status snapshots
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS provider_status_snapshots (
    id            UUID        PRIMARY KEY,
    provider_id   UUID        NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    state         TEXT        NOT NULL DEFAULT 'unknown',
    window_ms     BIGINT      NOT NULL DEFAULT 0,
    request_count INTEGER     NOT NULL DEFAULT 0,
    success_count INTEGER     NOT NULL DEFAULT 0,
    error_count   INTEGER     NOT NULL DEFAULT 0,
    success_rate  DOUBLE PRECISION NOT NULL DEFAULT 0,
    error_rate    DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_p50_ms BIGINT     NOT NULL DEFAULT 0,
    latency_p95_ms BIGINT     NOT NULL DEFAULT 0,
    latency_p99_ms BIGINT     NOT NULL DEFAULT 0,
    total_tokens  BIGINT      NOT NULL DEFAULT 0,
    total_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    message       TEXT        NOT NULL DEFAULT '',
    captured_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT provider_status_state_check
        CHECK (state IN ('healthy', 'degraded', 'unhealthy', 'unknown'))
);

CREATE INDEX IF NOT EXISTS provider_status_provider_captured_idx
    ON provider_status_snapshots (provider_id, captured_at DESC);

-- ---------------------------------------------------------------------------
-- Settings (single-row-per-key platform configuration)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT        PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_by TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
