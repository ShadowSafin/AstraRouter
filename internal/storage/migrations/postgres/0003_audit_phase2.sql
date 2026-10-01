-- CoreRouter Phase 2.5: widen audit event enumerations for Phase 2 resources.
--
-- Phase 2 added control-plane actions (override/probe/allow/deny) and resources
-- (endpoint, override, replay_job, evaluation, cache, feedback) in the domain
-- model, but the 0001 CHECK constraints still enumerate only the Phase 1 set.
-- Every Phase 2 audit write (kill switches, cache invalidation, replay jobs,
-- endpoint edits, even provider probes recorded as audit events) was rejected
-- by Postgres and lost. This migration replaces both constraints with the full
-- set. It is idempotent and safe to re-run.

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_action_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_action_check
        CHECK (action IN ('create', 'update', 'delete', 'login', 'rotate', 'revoke',
                          'allow', 'deny', 'override', 'probe'));

ALTER TABLE IF EXISTS audit_events
    DROP CONSTRAINT IF EXISTS audit_events_resource_check;

ALTER TABLE IF EXISTS audit_events
    ADD CONSTRAINT audit_events_resource_check
        CHECK (resource IN ('tenant', 'api_key', 'provider', 'model', 'routing_policy',
                            'budget', 'settings', 'endpoint', 'override', 'replay_job',
                            'evaluation', 'cache', 'feedback'));
