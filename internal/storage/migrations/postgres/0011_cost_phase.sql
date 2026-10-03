-- Synapass cost intelligence phase: versioned pricing, exact per-request cost
-- accounting, budget alerts and spend anomalies.
--
-- usage_records is the billing system of record, so the new cost columns live
-- there rather than in a side table: every spend report stays a single-table
-- aggregation and can never disagree with itself.

-- ---------------------------------------------------------------------------
-- Pricing versions (immutable dated price sheets)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS pricing_versions (
    id                 UUID        PRIMARY KEY,
    -- global, provider, model or tenant. Tenant negotiates beat model sheets
    -- beat provider sheets beat the global default, by resolution order.
    scope              TEXT        NOT NULL DEFAULT 'global',
    -- The provider, model-row or tenant id the sheet overrides; NULL for the
    -- global default. No foreign key: a sheet must outlive the row it priced,
    -- exactly like billing history outlives tenants.
    scope_id           UUID,
    currency           TEXT        NOT NULL DEFAULT 'USD',
    -- Quoted in USD per one million tokens, matching provider price sheets.
    input_cost_per_million         DOUBLE PRECISION NOT NULL DEFAULT 0,
    output_cost_per_million        DOUBLE PRECISION NOT NULL DEFAULT 0,
    cached_input_cost_per_million  DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- Flat per-billable-attempt overhead, for providers that charge one.
    base_fee_usd                   DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- Inclusive start, exclusive end (NULL = current). A price change is a new
    -- row, never an edit, so history stays reproducible.
    effective_from     TIMESTAMPTZ NOT NULL DEFAULT now(),
    effective_to       TIMESTAMPTZ,
    created_by         TEXT        NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pricing_versions_scope_check
        CHECK (scope IN ('global', 'provider', 'model', 'tenant')),
    CONSTRAINT pricing_versions_price_check
        CHECK (input_cost_per_million >= 0 AND output_cost_per_million >= 0
           AND cached_input_cost_per_million >= 0 AND base_fee_usd >= 0),
    CONSTRAINT pricing_versions_window_check
        CHECK (effective_to IS NULL OR effective_to > effective_from)
);

-- Resolution reads "the live sheets for these scopes", so the index leads
-- with scope and orders by effectiveness.
CREATE INDEX IF NOT EXISTS pricing_versions_scope_effective_idx
    ON pricing_versions (scope, scope_id, effective_from DESC);

-- ---------------------------------------------------------------------------
-- usage_records cost accounting columns
-- ---------------------------------------------------------------------------

-- Routing-time projection: what the request was expected to cost. The delta
-- against cost_usd measures estimate quality; on cache hits cost_usd is zero
-- while this records what serving would have billed (the cache saving).
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS estimate_cost_usd NUMERIC(20,6) NOT NULL DEFAULT 0;

-- Which versioned sheet priced the request; NULL = static registry price.
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS pricing_version_id UUID;

-- Exact line-item account of cost_usd, so a later price change cannot rewrite
-- what the request cost.
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS cost_breakdown JSONB NOT NULL DEFAULT '{}';

-- Which sheet won resolution (tenant, model, provider, global, registry).
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS pricing_source TEXT NOT NULL DEFAULT '';

-- Optimization bracket: eligible cost before shaping versus after. Their
-- difference is the routing saving.
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS cost_before_usd DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS cost_after_usd DOUBLE PRECISION NOT NULL DEFAULT 0;

-- Admin-managed endpoint scope, for spend-by-endpoint reporting.
ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS endpoint_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS usage_records_endpoint_created_idx
    ON usage_records (endpoint_id, created_at DESC) WHERE endpoint_id <> '';

-- ---------------------------------------------------------------------------
-- Budget alerts (exactly-once threshold crossings)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS budget_alerts (
    id           UUID        PRIMARY KEY,
    budget_id    UUID        NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    tenant_id    UUID,
    threshold_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    spent_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
    limit_usd    DOUBLE PRECISION NOT NULL DEFAULT 0,
    period       TEXT        NOT NULL DEFAULT 'monthly',
    -- Anchors the alert to one budget period: the same threshold crossing in
    -- the next period fires again, within this period it never repeats.
    period_start TIMESTAMPTZ NOT NULL DEFAULT now(),
    fired_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT budget_alerts_period_check
        CHECK (period IN ('daily', 'weekly', 'monthly', 'total')),
    CONSTRAINT budget_alerts_unique_per_period
        UNIQUE (budget_id, threshold_usd, period_start)
);

CREATE INDEX IF NOT EXISTS budget_alerts_tenant_fired_idx
    ON budget_alerts (tenant_id, fired_at DESC);

-- ---------------------------------------------------------------------------
-- Cost anomalies (flagged spend deviations)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cost_anomalies (
    id           UUID        PRIMARY KEY,
    dimension    TEXT        NOT NULL DEFAULT '',
    key          TEXT        NOT NULL DEFAULT '',
    window_from  TIMESTAMPTZ NOT NULL DEFAULT now(),
    window_to    TIMESTAMPTZ NOT NULL DEFAULT now(),
    observed_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    expected_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    ratio        DOUBLE PRECISION NOT NULL DEFAULT 0,
    severity     TEXT        NOT NULL DEFAULT 'info',
    resolved_at  TIMESTAMPTZ,
    detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT cost_anomalies_severity_check
        CHECK (severity IN ('info', 'warning', 'critical')),
    CONSTRAINT cost_anomalies_dimension_check
        CHECK (dimension IN ('provider', 'model', 'tenant', 'endpoint'))
);

CREATE INDEX IF NOT EXISTS cost_anomalies_detected_idx
    ON cost_anomalies (detected_at DESC);
CREATE INDEX IF NOT EXISTS cost_anomalies_open_idx
    ON cost_anomalies (detected_at DESC) WHERE resolved_at IS NULL;

-- ---------------------------------------------------------------------------
-- Audit vocabulary for the cost surface
-- ---------------------------------------------------------------------------

-- Pricing sheets and anomaly acknowledgements are audited like any other
-- control-plane mutation; without the vocabulary entries the rows would be
-- rejected by the check below and silently never land.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_resource_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_resource_check
    CHECK (resource = ANY (ARRAY[
        'tenant', 'api_key', 'provider', 'model', 'routing_policy', 'budget',
        'settings', 'endpoint', 'override', 'replay_job', 'evaluation', 'cache',
        'feedback', 'credential', 'test_result', 'tool', 'tool_policy',
        'agent_run', 'tunnel',
        'dashboard_user', 'dashboard_session',
        'pricing_version', 'cost_anomaly'
    ]));
