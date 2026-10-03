-- Synapass cost intelligence phase: carry estimate-vs-actual accounting into
-- the analytics store so ClickHouse-backed views can score estimate quality
-- without joining back to Postgres.

ALTER TABLE synapass.usage_events
    ADD COLUMN IF NOT EXISTS estimate_cost_usd Float64 DEFAULT 0;

ALTER TABLE synapass.usage_events
    ADD COLUMN IF NOT EXISTS pricing_source LowCardinality(String) DEFAULT '';

ALTER TABLE synapass.usage_events
    ADD COLUMN IF NOT EXISTS endpoint_id String DEFAULT '';
