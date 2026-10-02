-- Dashboard authentication: the operator identity behind the console.
--
-- Distinct from api_keys, which are machine credentials. These rows are human
-- identities: they hold a slow password hash rather than a fast token digest,
-- they are rate limited and lockable, and they authenticate a browser session
-- rather than an Authorization header.

CREATE TABLE IF NOT EXISTS dashboard_users (
    id                UUID        PRIMARY KEY,
    -- Stored lowercased so login is case-insensitive. The original casing is not
    -- retained: a display name is not worth making username lookup ambiguous.
    username          TEXT        NOT NULL,
    -- Argon2id, PHC-encoded. Never the plaintext, never reversible.
    password_hash     TEXT        NOT NULL,
    enabled           BOOLEAN     NOT NULL DEFAULT true,
    -- Consecutive failures, reset on success. Drives the lockout window.
    failed_attempts   INTEGER     NOT NULL DEFAULT 0,
    locked_until      TIMESTAMPTZ,
    last_login_at     TIMESTAMPTZ,
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dashboard_users_username_key UNIQUE (username),
    CONSTRAINT dashboard_users_username_format
        CHECK (username ~ '^[a-z0-9][a-z0-9._-]{2,31}$'),
    CONSTRAINT dashboard_users_failed_attempts_nonneg
        CHECK (failed_attempts >= 0)
);

CREATE TABLE IF NOT EXISTS dashboard_sessions (
    id              UUID        PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES dashboard_users (id) ON DELETE CASCADE,
    -- SHA-256 of the opaque session token. The token itself is only ever in the
    -- client's cookie, so a database disclosure yields no usable session.
    token_hash      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    -- Set on logout or on password change. A revoked row is retained rather than
    -- deleted so an audit of who was signed in survives.
    revoked_at      TIMESTAMPTZ,
    -- Best-effort client attribution for the audit trail.
    ip              TEXT        NOT NULL DEFAULT '',
    user_agent      TEXT        NOT NULL DEFAULT '',
    CONSTRAINT dashboard_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT dashboard_sessions_expiry_check
        CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS dashboard_sessions_user_idx
    ON dashboard_sessions (user_id, expires_at DESC);
CREATE INDEX IF NOT EXISTS dashboard_sessions_expiry_idx
    ON dashboard_sessions (expires_at);

-- The first-run latch.
--
-- Setup is gated on this row rather than on "no users exist", because deleting
-- every user would otherwise silently reopen first-run setup to anyone who can
-- reach the surface. Once written, the row is what closes setup permanently;
-- reopening it is an explicit admin-key operation, never an accident.
--
-- `value` is JSONB, so the timestamp is stored as a JSON string.
INSERT INTO settings (key, value, updated_by)
VALUES ('dashboard_auth.setup_completed_at', '""'::jsonb, '')
ON CONFLICT (key) DO NOTHING;
