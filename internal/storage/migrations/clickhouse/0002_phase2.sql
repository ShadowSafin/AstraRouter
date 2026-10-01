-- CoreRouter Phase 2 ClickHouse additions: classifier, shaping, cache,
-- scoring and eval analytics.

-- Classification + policy + shaping per request.
CREATE TABLE IF NOT EXISTS corerouter.request_intelligence
(
    request_id       String,
    tenant_id        String,
    task             LowCardinality(String),
    task_confidence  Float64,
    task_signals     String,
    policy_name      LowCardinality(String),
    policy_allowed   UInt8,
    deny_reason      LowCardinality(String),
    shaping          String,
    cache_kind       LowCardinality(String),
    cache_hit        UInt8,
    score_notes      String,
    cost_before_usd  Float64,
    cost_after_usd   Float64,
    created_at       DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (tenant_id, created_at, request_id)
TTL toDateTime(created_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;

-- Provider score history.
CREATE TABLE IF NOT EXISTS corerouter.provider_scores
(
    provider_name    LowCardinality(String),
    window           LowCardinality(String),
    requests         UInt32,
    success_rate     Float64,
    avg_latency_ms   Float64,
    cost_per_success Float64,
    score            Float64,
    captured_at      DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(captured_at)
ORDER BY (provider_name, captured_at)
TTL toDateTime(captured_at) + INTERVAL 365 DAY DELETE
SETTINGS index_granularity = 8192;

-- Evaluation results.
CREATE TABLE IF NOT EXISTS corerouter.eval_results
(
    evaluation_id    String,
    request_id       String,
    provider         LowCardinality(String),
    model            String,
    score            Float64,
    latency_ms       UInt32,
    cost_usd         Float64,
    is_regression    UInt8,
    created_at       DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (evaluation_id, created_at, provider, model)
TTL toDateTime(created_at) + INTERVAL 365 DAY DELETE
SETTINGS index_granularity = 8192;

-- Cache hit/miss events for analytics.
CREATE TABLE IF NOT EXISTS corerouter.cache_events
(
    request_id       String,
    tenant_id        String,
    kind             LowCardinality(String),
    hit              UInt8,
    similarity       Float64,
    bypass_reason    String,
    created_at       DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (tenant_id, created_at, request_id)
TTL toDateTime(created_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;
