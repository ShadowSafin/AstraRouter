-- AstraRouter Phase 4: tool registry, tool execution and bounded agent runs.
--
-- Phase 1-3 stored catalogue configuration (providers, models, policies) and
-- telemetry, but nothing durable recorded what a tool call did. Phase 4 adds:
--
--   tools            the registry of callable tools, with their JSON schemas
--   tool_policies    who may call tools, and whether the gateway may execute
--   tool_invocations one row per model-requested tool call
--   tool_executions  the gateway's result for an executed call
--   agent_runs       a bounded multi-step tool run
--   agent_steps      one row per model round-trip inside a run
--
-- The migration is idempotent and safe to re-run.

-- ---------------------------------------------------------------------------
-- Tool registry
-- ---------------------------------------------------------------------------
--
-- One row per callable tool. `parameters` holds the tool's JSON Schema, which
-- is validated by the gateway before it is written. A tool is only executable
-- when `kind = 'builtin'` and `executable` is true: external tools are
-- registered for discovery and schema declaration, but the gateway never runs
-- operator-supplied network endpoints.

CREATE TABLE IF NOT EXISTS tools (
    id                 UUID        PRIMARY KEY,
    name               TEXT        NOT NULL,
    description        TEXT        NOT NULL DEFAULT '',
    kind               TEXT        NOT NULL DEFAULT 'external',
    owner              TEXT        NOT NULL DEFAULT 'platform',
    -- tenant_id scopes a tenant-owned tool; NULL marks a platform tool.
    tenant_id          UUID        REFERENCES tenants (id) ON DELETE CASCADE,
    parameters         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    strict             BOOLEAN     NOT NULL DEFAULT FALSE,
    safety_level       TEXT        NOT NULL DEFAULT 'safe',
    executable         BOOLEAN     NOT NULL DEFAULT FALSE,
    handler            TEXT        NOT NULL DEFAULT '',
    version            TEXT        NOT NULL DEFAULT '',
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    requires_approval  BOOLEAN     NOT NULL DEFAULT FALSE,
    labels             JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tools_kind_check
        CHECK (kind IN ('builtin', 'external')),
    CONSTRAINT tools_owner_check
        CHECK (owner IN ('platform', 'tenant')),
    CONSTRAINT tools_safety_check
        CHECK (safety_level IN ('safe', 'sensitive', 'dangerous')),
    CONSTRAINT tools_name_format CHECK (name <> '')
);

-- A tool name is unique per owner scope. Postgres treats NULLs as distinct in a
-- plain unique index, so platform tools (tenant_id NULL) need a partial index
-- and tenant tools use the composite one.
CREATE UNIQUE INDEX IF NOT EXISTS tools_platform_name_key
    ON tools (name) WHERE tenant_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tools_tenant_name_key
    ON tools (tenant_id, name) WHERE tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tools_enabled_idx ON tools (enabled) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Tool policies
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tool_policies (
    id                 UUID        PRIMARY KEY,
    -- tenant_id NULL is the platform default every tenant inherits.
    tenant_id          UUID        REFERENCES tenants (id) ON DELETE CASCADE,
    name               TEXT        NOT NULL,
    enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    mode               TEXT        NOT NULL DEFAULT 'manual',
    allowed_tools      TEXT[]      NOT NULL DEFAULT '{}',
    denied_tools       TEXT[]      NOT NULL DEFAULT '{}',
    allowed_providers  TEXT[]      NOT NULL DEFAULT '{}',
    denied_providers   TEXT[]      NOT NULL DEFAULT '{}',
    require_approval   BOOLEAN     NOT NULL DEFAULT FALSE,
    max_steps          INTEGER     NOT NULL DEFAULT 3,
    max_tool_calls     INTEGER     NOT NULL DEFAULT 8,
    max_result_bytes   INTEGER     NOT NULL DEFAULT 32768,
    max_run_seconds    INTEGER     NOT NULL DEFAULT 60,
    block_sensitive    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tool_policies_mode_check
        CHECK (mode IN ('disabled', 'manual', 'automatic')),
    CONSTRAINT tool_policies_steps_check CHECK (max_steps BETWEEN 0 AND 10),
    CONSTRAINT tool_policies_calls_check CHECK (max_tool_calls BETWEEN 0 AND 64),
    CONSTRAINT tool_policies_name_format CHECK (name <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS tool_policies_platform_name_key
    ON tool_policies (name) WHERE tenant_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tool_policies_tenant_name_key
    ON tool_policies (tenant_id, name) WHERE tenant_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Tool invocations and executions
-- ---------------------------------------------------------------------------
--
-- tool_invocations is the fact "the model asked to call X with these
-- arguments", written whether the call ran, was denied, or was handed back to
-- the client. The payload lives in tool_executions so listing invocations
-- stays cheap.

CREATE TABLE IF NOT EXISTS tool_invocations (
    id                  UUID        PRIMARY KEY,
    request_id          TEXT        NOT NULL DEFAULT '',
    tenant_id           UUID,
    run_id              UUID,
    step                INTEGER     NOT NULL DEFAULT 1,
    tool_call_id        TEXT        NOT NULL DEFAULT '',
    tool_name           TEXT        NOT NULL,
    arguments           TEXT        NOT NULL DEFAULT '',
    parsed_arguments    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status              TEXT        NOT NULL DEFAULT 'pending',
    deny_reason         TEXT        NOT NULL DEFAULT '',
    latency_ms          BIGINT      NOT NULL DEFAULT 0,
    result_bytes        INTEGER     NOT NULL DEFAULT 0,
    error_code          TEXT        NOT NULL DEFAULT '',
    provider            TEXT        NOT NULL DEFAULT '',
    model               TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tool_invocations_status_check
        CHECK (status IN ('pending', 'executed', 'denied', 'failed', 'skipped', 'invalid'))
);

CREATE INDEX IF NOT EXISTS tool_invocations_request_idx ON tool_invocations (request_id);
CREATE INDEX IF NOT EXISTS tool_invocations_tenant_idx ON tool_invocations (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS tool_invocations_tool_idx ON tool_invocations (tool_name, created_at DESC);
CREATE INDEX IF NOT EXISTS tool_invocations_run_idx ON tool_invocations (run_id) WHERE run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS tool_executions (
    id              UUID        PRIMARY KEY,
    invocation_id   UUID        NOT NULL REFERENCES tool_invocations (id) ON DELETE CASCADE,
    tool_name       TEXT        NOT NULL,
    content         TEXT        NOT NULL DEFAULT '',
    success         BOOLEAN     NOT NULL DEFAULT FALSE,
    error_message   TEXT        NOT NULL DEFAULT '',
    latency_ms      BIGINT      NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tool_executions_invocation_idx ON tool_executions (invocation_id);

-- ---------------------------------------------------------------------------
-- Agent runs
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS agent_runs (
    id               UUID        PRIMARY KEY,
    request_id       TEXT        NOT NULL DEFAULT '',
    tenant_id        UUID,
    status           TEXT        NOT NULL DEFAULT 'completed',
    steps            INTEGER     NOT NULL DEFAULT 0,
    tool_calls       INTEGER     NOT NULL DEFAULT 0,
    provider         TEXT        NOT NULL DEFAULT '',
    model            TEXT        NOT NULL DEFAULT '',
    total_latency_ms BIGINT      NOT NULL DEFAULT 0,
    stop_reason      TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_runs_status_check
        CHECK (status IN ('running', 'completed', 'step_limit', 'call_limit', 'time_limit', 'failed', 'denied'))
);

CREATE INDEX IF NOT EXISTS agent_runs_request_idx ON agent_runs (request_id);
CREATE INDEX IF NOT EXISTS agent_runs_tenant_idx ON agent_runs (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS agent_steps (
    id               UUID        PRIMARY KEY,
    run_id           UUID        NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    step             INTEGER     NOT NULL,
    kind             TEXT        NOT NULL DEFAULT 'model',
    provider         TEXT        NOT NULL DEFAULT '',
    model            TEXT        NOT NULL DEFAULT '',
    tool_calls       INTEGER     NOT NULL DEFAULT 0,
    latency_ms       BIGINT      NOT NULL DEFAULT 0,
    tokens           INTEGER     NOT NULL DEFAULT 0,
    detail           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_steps_kind_check CHECK (kind IN ('model', 'tool')),
    CONSTRAINT agent_steps_step_check CHECK (step >= 1)
);

CREATE INDEX IF NOT EXISTS agent_steps_run_idx ON agent_steps (run_id, step);

-- ---------------------------------------------------------------------------
-- Audit enumerations
-- ---------------------------------------------------------------------------
--
-- Phase 4 adds the execute action and the tool/tool_policy/agent_run resources.
-- Mirrors the widening pattern from 0003 and 0004: drop then re-add, because the
-- constraints are named and Postgres cannot extend a CHECK in place.

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_action_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_action_check
        CHECK (action IN ('create', 'update', 'delete', 'login', 'rotate', 'revoke',
                          'allow', 'deny', 'override', 'probe', 'test', 'enable', 'disable',
                          'execute'));

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_resource_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_resource_check
        CHECK (resource IN ('tenant', 'api_key', 'provider', 'model', 'routing_policy',
                            'budget', 'settings', 'endpoint', 'override', 'replay_job',
                            'evaluation', 'cache', 'feedback', 'credential', 'test_result',
                            'tool', 'tool_policy', 'agent_run'));