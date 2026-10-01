-- Tunnel audit resources.
--
-- Temporary tunnels write audit events with resource 'tunnel', but the
-- audit_events CHECK constraint still enumerates only the pre-tunnel set, so
-- every tunnel audit write was rejected by Postgres and lost (the tunnel
-- itself worked; only its audit trail was missing). This migration replaces
-- the constraint with the full set including 'tunnel'. It is idempotent and
-- safe to re-run.

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_resource_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_resource_check
        CHECK (resource IN ('tenant', 'api_key', 'provider', 'model', 'routing_policy',
                            'budget', 'settings', 'endpoint', 'override', 'replay_job',
                            'evaluation', 'cache', 'feedback', 'credential', 'test_result',
                            'tool', 'tool_policy', 'agent_run', 'tunnel'));
