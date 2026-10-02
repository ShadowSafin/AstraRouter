-- Extend the audit vocabulary for console operator authentication.
--
-- 0009 added the dashboard auth tables but this constraint still closed the list
-- at 'tunnel', so every login, logout, setup and password change was rejected by
-- the database. The gateway logged the failure and continued, which is the
-- correct behaviour for an audit write but meant the events silently never
-- landed.
--
-- A new migration rather than an edit to 0009: applied migrations are recorded
-- with a checksum, and changing one is reported as a mismatch on purpose.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_resource_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_resource_check
    CHECK (resource = ANY (ARRAY[
        'tenant', 'api_key', 'provider', 'model', 'routing_policy', 'budget',
        'settings', 'endpoint', 'override', 'replay_job', 'evaluation', 'cache',
        'feedback', 'credential', 'test_result', 'tool', 'tool_policy',
        'agent_run', 'tunnel',
        'dashboard_user', 'dashboard_session'
    ]));

ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_action_check;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_action_check
    CHECK (action = ANY (ARRAY[
        'create', 'update', 'delete', 'login', 'rotate', 'revoke', 'allow',
        'deny', 'override', 'probe', 'test', 'enable', 'disable', 'execute',
        'logout', 'password_change'
    ]));
