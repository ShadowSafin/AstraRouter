-- Captured request payloads for offline replay.
--
-- Telemetry rows (usage_records, request_logs, traces) describe what happened
-- but never the prompt itself, so a pasted request id could not be re-executed.
-- request_payloads keeps the normalized messages per request id, written
-- best-effort on the async telemetry path: a capture failure is logged and
-- never fails the request it describes.
--
-- Prompts can contain production data. Payloads carry no foreign keys and are
-- pruned with the request logs, so retention policy applies to both or neither.

CREATE TABLE IF NOT EXISTS request_payloads (
    request_id       TEXT        PRIMARY KEY,
    tenant_id        UUID,
    model            TEXT        NOT NULL DEFAULT '',
    messages         JSONB       NOT NULL DEFAULT '[]',
    max_output_tokens INT        NOT NULL DEFAULT 0,
    payload_bytes    INT         NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS request_payloads_tenant_idx
    ON request_payloads (tenant_id, created_at);
