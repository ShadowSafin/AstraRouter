-- CoreRouter Phase 2 schema: policy-driven control plane.
--
-- Adds durable entities for policy decisions, classification, shaping, cache
-- metadata, provider/model scores, replay/eval, overrides, circuit state and
-- feedback. All tables are additive; Phase 1 tables are untouched.

-- ---------------------------------------------------------------------------
-- Policy decisions (per-request verdicts, sampled retention)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS policy_decisions (
    id               UUID        PRIMARY KEY,
    request_id       TEXT        NOT NULL,
    tenant_id        UUID,
    api_key_id       UUID,
    policy_id        UUID,
    policy_name      TEXT        NOT NULL DEFAULT '',
    allowed          BOOLEAN     NOT NULL DEFAULT TRUE,
    deny_reason      TEXT        NOT NULL DEFAULT '',
    deny_message     TEXT        NOT NULL DEFAULT '',
    warnings         TEXT[]      NOT NULL DEFAULT '{}',
    matched_rules    TEXT[]      NOT NULL DEFAULT '{}',
    task             TEXT        NOT NULL DEFAULT '',
    use_cache        BOOLEAN     NOT NULL DEFAULT TRUE,
    endpoint_id      TEXT        NOT NULL DEFAULT '',
    policy_version   INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS policy_decisions_request_idx ON policy_decisions (request_id);
CREATE INDEX IF NOT EXISTS policy_decisions_tenant_created_idx ON policy_decisions (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS policy_decisions_created_idx ON policy_decisions (created_at DESC);

-- ---------------------------------------------------------------------------
-- Task classifications
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS task_classifications (
    id            UUID        PRIMARY KEY,
    request_id    TEXT        NOT NULL,
    tenant_id     UUID,
    task          TEXT        NOT NULL DEFAULT 'chat',
    secondary     TEXT[]      NOT NULL DEFAULT '{}',
    confidence    DOUBLE PRECISION NOT NULL DEFAULT 0,
    signals       TEXT[]      NOT NULL DEFAULT '{}',
    prompt_tokens INTEGER     NOT NULL DEFAULT 0,
    message_count INTEGER     NOT NULL DEFAULT 0,
    source        TEXT        NOT NULL DEFAULT 'rules',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS task_classifications_request_idx ON task_classifications (request_id);
CREATE INDEX IF NOT EXISTS task_classifications_task_idx ON task_classifications (task, created_at DESC);

-- ---------------------------------------------------------------------------
-- Prompt shapes
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS prompt_shapes (
    id          UUID        PRIMARY KEY,
    request_id  TEXT        NOT NULL,
    tenant_id   UUID,
    plan        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    tokens_in   INTEGER     NOT NULL DEFAULT 0,
    tokens_out  INTEGER     NOT NULL DEFAULT 0,
    truncated   INTEGER     NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS prompt_shapes_request_idx ON prompt_shapes (request_id);

-- ---------------------------------------------------------------------------
-- Cache entries (durable metadata; bodies live in Redis)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cache_entries (
    id             UUID        PRIMARY KEY,
    tenant_id      UUID,
    kind           TEXT        NOT NULL DEFAULT 'exact',
    cache_key      TEXT        NOT NULL,
    model          TEXT        NOT NULL DEFAULT '',
    prompt_hash    TEXT        NOT NULL DEFAULT '',
    embedding_hash TEXT        NOT NULL DEFAULT '',
    similarity     DOUBLE PRECISION NOT NULL DEFAULT 0,
    sensitive      BOOLEAN     NOT NULL DEFAULT FALSE,
    hit_count      INTEGER     NOT NULL DEFAULT 0,
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT cache_entries_kind_check CHECK (kind IN ('exact', 'prefix', 'semantic'))
);

CREATE UNIQUE INDEX IF NOT EXISTS cache_entries_key ON cache_entries (cache_key);
CREATE INDEX IF NOT EXISTS cache_entries_tenant_idx ON cache_entries (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Provider and model scores
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS provider_scores (
    id               UUID        PRIMARY KEY,
    provider_id      UUID,
    provider_name    TEXT        NOT NULL DEFAULT '',
    window_label     TEXT        NOT NULL DEFAULT '1h',
    requests         INTEGER     NOT NULL DEFAULT 0,
    success_rate     DOUBLE PRECISION NOT NULL DEFAULT 0,
    timeout_rate     DOUBLE PRECISION NOT NULL DEFAULT 0,
    refusal_rate     DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_latency_ms   DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_per_success DOUBLE PRECISION NOT NULL DEFAULT 0,
    fallback_rate    DOUBLE PRECISION NOT NULL DEFAULT 0,
    feedback_score   DOUBLE PRECISION NOT NULL DEFAULT 0,
    score            DOUBLE PRECISION NOT NULL DEFAULT 0,
    explanation      TEXT        NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS provider_scores_name_idx ON provider_scores (provider_name, updated_at DESC);

CREATE TABLE IF NOT EXISTS model_scores (
    id               UUID        PRIMARY KEY,
    provider_id      UUID,
    provider_name    TEXT        NOT NULL DEFAULT '',
    model            TEXT        NOT NULL DEFAULT '',
    window_label     TEXT        NOT NULL DEFAULT '1h',
    requests         INTEGER     NOT NULL DEFAULT 0,
    success_rate     DOUBLE PRECISION NOT NULL DEFAULT 0,
    avg_latency_ms   DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_per_success DOUBLE PRECISION NOT NULL DEFAULT 0,
    score            DOUBLE PRECISION NOT NULL DEFAULT 0,
    explanation      TEXT        NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS model_scores_model_idx ON model_scores (model, updated_at DESC);

-- ---------------------------------------------------------------------------
-- Replay jobs and evaluation runs/results
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS replay_jobs (
    id           UUID        PRIMARY KEY,
    tenant_id    UUID,
    name         TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'queued',
    request_ids  TEXT[]      NOT NULL DEFAULT '{}',
    dataset      TEXT        NOT NULL DEFAULT '',
    providers    TEXT[]      NOT NULL DEFAULT '{}',
    models       TEXT[]      NOT NULL DEFAULT '{}',
    max_requests INTEGER     NOT NULL DEFAULT 100,
    created_by   TEXT        NOT NULL DEFAULT '',
    progress     INTEGER     NOT NULL DEFAULT 0,
    total        INTEGER     NOT NULL DEFAULT 0,
    error        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS replay_jobs_tenant_idx ON replay_jobs (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS replay_jobs_status_idx ON replay_jobs (status);

CREATE TABLE IF NOT EXISTS evaluation_runs (
    id            UUID        PRIMARY KEY,
    tenant_id     UUID,
    replay_job_id UUID,
    dataset       TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'queued',
    created_by    TEXT        NOT NULL DEFAULT '',
    error         TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS evaluation_runs_tenant_idx ON evaluation_runs (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS evaluation_results (
    id             UUID        PRIMARY KEY,
    evaluation_id  UUID        NOT NULL REFERENCES evaluation_runs (id) ON DELETE CASCADE,
    request_id     TEXT        NOT NULL DEFAULT '',
    provider       TEXT        NOT NULL DEFAULT '',
    model          TEXT        NOT NULL DEFAULT '',
    score          DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_ms     BIGINT      NOT NULL DEFAULT 0,
    cost_usd       DOUBLE PRECISION NOT NULL DEFAULT 0,
    output_preview TEXT        NOT NULL DEFAULT '',
    is_regression  BOOLEAN     NOT NULL DEFAULT FALSE,
    metrics        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS evaluation_results_eval_idx ON evaluation_results (evaluation_id, score DESC);
CREATE INDEX IF NOT EXISTS evaluation_results_request_idx ON evaluation_results (request_id);

-- ---------------------------------------------------------------------------
-- Audit overrides (kill switches, emergency tenant overrides, caps)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS audit_overrides (
    id         UUID        PRIMARY KEY,
    tenant_id  UUID,
    kind       TEXT        NOT NULL DEFAULT '',
    target     TEXT        NOT NULL DEFAULT '',
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    reason     TEXT        NOT NULL DEFAULT '',
    actor      TEXT        NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_overrides_tenant_idx ON audit_overrides (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_overrides_kind_idx ON audit_overrides (kind, target);

-- ---------------------------------------------------------------------------
-- Circuit breaker state
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS circuit_breaker_state (
    provider_id UUID        PRIMARY KEY,
    state       TEXT        NOT NULL DEFAULT 'closed',
    failures    INTEGER     NOT NULL DEFAULT 0,
    opened_at   TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason      TEXT        NOT NULL DEFAULT '',
    overridden  BOOLEAN     NOT NULL DEFAULT FALSE,
    override_by TEXT        NOT NULL DEFAULT '',
    CONSTRAINT circuit_state_check CHECK (state IN ('closed', 'open', 'half_open', 'forced_open', 'forced_closed'))
);

-- ---------------------------------------------------------------------------
-- Feedback events
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS feedback_events (
    id         UUID        PRIMARY KEY,
    request_id TEXT        NOT NULL,
    tenant_id  UUID,
    score      DOUBLE PRECISION NOT NULL DEFAULT 0,
    comment    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS feedback_request_idx ON feedback_events (request_id);
CREATE INDEX IF NOT EXISTS feedback_tenant_idx ON feedback_events (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Endpoints (admin-managed route scopes)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS endpoints (
    id               UUID        PRIMARY KEY,
    tenant_id        UUID        REFERENCES tenants (id) ON DELETE CASCADE,
    slug             TEXT        NOT NULL,
    name             TEXT        NOT NULL DEFAULT '',
    description      TEXT        NOT NULL DEFAULT '',
    routing_override JSONB       NOT NULL DEFAULT '{}'::jsonb,
    enabled          BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS endpoints_tenant_slug_key ON endpoints (tenant_id, slug) WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS endpoints_global_slug_key ON endpoints (slug) WHERE tenant_id IS NULL;
