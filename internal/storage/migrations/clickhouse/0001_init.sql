-- CoreRouter ClickHouse telemetry schema.
--
-- ClickHouse is the analytical store for high-volume traffic data. It is
-- deliberately NOT the billing system of record: the Postgres usage_records table
-- is authoritative because ClickHouse's eventual consistency and its lack of
-- unique constraints make it unsuitable for anything that must be exactly right.
-- What ClickHouse is good at, and why it is used here, is scanning billions of
-- rows to answer "what did latency look like for provider X over the last 90 days".
--
-- Schema conventions:
--
--  * MergeTree family, ordered by (tenant, time) because every dashboard query
--    filters by tenant and a time range.
--  * LowCardinality for enumerated strings: it turns repeated provider and model
--    names into dictionary codes, which is a large compression win at this volume.
--  * DateTime64(3) for millisecond precision, matching the gateway's telemetry.
--  * Denormalized: attempts are a separate flat table rather than an Array column,
--    because an attempt-level query ("which provider fails most on timeouts?") is
--    the common analytical question and a flat table answers it without array
--    joins.
--  * TTL expressions give automatic retention management, so an operator does not
--    need a cron job that deletes rows out from under a running query.

CREATE DATABASE IF NOT EXISTS corerouter;

-- ---------------------------------------------------------------------------
-- Request traces: one row per request.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS corerouter.request_traces
(
    trace_id         String,
    request_id       String,
    tenant_id        String,
    api_key_id       String,
    policy_id        String,
    strategy         LowCardinality(String),
    requested_model  String,
    routed_provider  LowCardinality(String),
    routed_model     String,
    outcome          LowCardinality(String),
    error_code       LowCardinality(String),
    attempts         UInt8,
    fallback_used    UInt8,
    degraded         UInt8,
    client_streamed  UInt8,
    estimated_cost_usd Float64,
    total_ms         UInt32,
    first_token_ms   UInt32,
    prompt_tokens    UInt32,
    completion_tokens UInt32,
    -- decision_json keeps the complete RouteDecision so an operator can inspect
    -- the candidate list for a specific historical request.
    decision_json    String,
    started_at       DateTime64(3, 'UTC'),
    created_at       DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(started_at)
ORDER BY (tenant_id, started_at, trace_id)
TTL toDateTime(started_at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

-- ---------------------------------------------------------------------------
-- Trace attempts: one row per provider call.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS corerouter.trace_attempts
(
    trace_id          String,
    request_id        String,
    tenant_id         String,
    attempt_number    UInt8,
    provider_id       String,
    provider_name     LowCardinality(String),
    provider_kind     LowCardinality(String),
    model             String,
    -- status is the upstream HTTP status, 0 when no response arrived.
    status            UInt16,
    error_code        LowCardinality(String),
    error_message     String,
    duration_ms       UInt32,
    started_offset_ms Int64,
    backoff_ms        UInt32,
    first_token_ms    UInt32,
    retry_triggered   UInt8,
    fallback_triggered UInt8,
    prompt_tokens     UInt32,
    completion_tokens UInt32,
    cost_usd          Float64,
    started_at        DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(started_at)
ORDER BY (tenant_id, started_at, trace_id, attempt_number)
TTL toDateTime(started_at) + INTERVAL 30 DAY DELETE
SETTINGS index_granularity = 8192;

-- ---------------------------------------------------------------------------
-- Provider status snapshots.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS corerouter.provider_status_snapshots
(
    provider_id     String,
    provider_name   LowCardinality(String),
    provider_kind   LowCardinality(String),
    state           LowCardinality(String),
    window_ms       UInt64,
    request_count   UInt32,
    success_count   UInt32,
    error_count     UInt32,
    success_rate    Float64,
    error_rate      Float64,
    latency_p50_ms  UInt32,
    latency_p95_ms  UInt32,
    latency_p99_ms  UInt32,
    total_tokens    UInt64,
    total_cost_usd  Float64,
    message         String,
    captured_at     DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(captured_at)
ORDER BY (provider_id, captured_at)
TTL toDateTime(captured_at) + INTERVAL 365 DAY DELETE
SETTINGS index_granularity = 8192;

-- ---------------------------------------------------------------------------
-- Raw usage events, plus a daily rollup materialized view.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS corerouter.usage_events
(
    request_id        String,
    tenant_id         String,
    api_key_id        String,
    provider          LowCardinality(String),
    model             String,
    requested_model   String,
    outcome           LowCardinality(String),
    error_code        LowCardinality(String),
    fallback_used     UInt8,
    cache_hit         UInt8,
    estimated         UInt8,
    streaming         UInt8,
    prompt_tokens     UInt32,
    completion_tokens UInt32,
    total_tokens      UInt32,
    cost_usd          Float64,
    latency_ms        UInt32,
    created_at        DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (tenant_id, created_at, request_id)
TTL toDateTime(created_at) + INTERVAL 400 DAY DELETE
SETTINGS index_granularity = 8192;

-- The rollup keeps dashboard cost and volume charts off the raw table. SummingMergeTree
-- collapses rows with an identical key at merge time, so a monthly scan reads a
-- tiny fraction of the raw data.
CREATE TABLE IF NOT EXISTS corerouter.usage_daily
(
    day               Date,
    tenant_id         String,
    provider          LowCardinality(String),
    model             String,
    request_count     UInt64,
    error_count       UInt64,
    fallback_count    UInt64,
    prompt_tokens     UInt64,
    completion_tokens UInt64,
    total_tokens      UInt64,
    total_cost_usd    Float64,
    latency_ms_sum    UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, provider, model)
TTL day + INTERVAL 730 DAY DELETE;

CREATE MATERIALIZED VIEW IF NOT EXISTS corerouter.usage_daily_mv
TO corerouter.usage_daily
AS
SELECT
    toDate(created_at)     AS day,
    tenant_id,
    provider,
    model,
    count()                AS request_count,
    countIf(outcome IN ('error', 'rejected')) AS error_count,
    countIf(fallback_used = 1) AS fallback_count,
    sum(prompt_tokens)     AS prompt_tokens,
    sum(completion_tokens) AS completion_tokens,
    sum(total_tokens)      AS total_tokens,
    sum(cost_usd)          AS total_cost_usd,
    sum(latency_ms)        AS latency_ms_sum
FROM corerouter.usage_events
GROUP BY day, tenant_id, provider, model;

-- ---------------------------------------------------------------------------
-- Routing decision analytics
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS corerouter.route_decisions
(
    request_id        String,
    tenant_id         String,
    policy_id         String,
    policy_name       LowCardinality(String),
    strategy          LowCardinality(String),
    requested_model   String,
    chosen_provider   LowCardinality(String),
    chosen_model      String,
    degraded          UInt8,
    candidate_count   UInt8,
    eligible_count    UInt8,
    skipped_count     UInt8,
    estimated_cost_usd Float64,
    reason            String,
    created_at        DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(created_at)
ORDER BY (tenant_id, created_at, request_id)
TTL toDateTime(created_at) + INTERVAL 90 DAY DELETE
SETTINGS index_granularity = 8192;
