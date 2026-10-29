-- trove:phase expand
--
-- Login state (M1, WP1.1): the identity links of OIDC users (R-AUTH-007), the
-- server-side UI sessions (R-AUTH-010), the state of browser login flows
-- (R-AUTH-005), of command-line login flows (R-AUTH-034, R-AUTH-036) and the
-- break-glass lockout (R-AUTH-014, R-AUTH-038).
--
-- Every step of a multi-request flow reads its state from PostgreSQL and changes
-- it with a conditional statement (R-AUTH-042), so the tables below are the whole
-- login state of the cluster: nothing is kept in a replica's memory or in Valkey,
-- and a login works on any replica with Valkey stopped.

-- The key of an external identity is the pair (issuer, subject); a NULL subject is
-- a pending link of a pre-provisioned user (R-AUTH-007, R-AUTH-016).
CREATE TABLE user_identity (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES app_user (principal_id) ON DELETE CASCADE,
    provider_id  text        NOT NULL,
    issuer       text        NOT NULL,
    subject      text,
    linked_at    timestamptz,
    last_claims  jsonb,
    UNIQUE (issuer, subject),
    UNIQUE (user_id, provider_id)
);

-- Holds raw ID tokens for the logout hint: backup export class transient.
CREATE TABLE ui_session (
    id                   uuid        PRIMARY KEY,
    secret_sha256        bytea       NOT NULL UNIQUE CHECK (octet_length(secret_sha256) = 32),
    csrf_sha256          bytea       NOT NULL CHECK (octet_length(csrf_sha256) = 32),
    principal_id         uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    auth_method          text        NOT NULL CHECK (auth_method IN ('oidc', 'break_glass')),
    provider_id          text,
    idp_sid              text,
    id_token             text,
    authenticated_at     timestamptz NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    idle_timeout         interval    NOT NULL,
    absolute_expires_at  timestamptz NOT NULL,
    created_addr         inet,
    user_agent           text,
    ended_at             timestamptz,
    end_reason           text        CHECK (end_reason IN ('logout', 'idle', 'absolute', 'evicted', 'revoked',
                                                           'principal_disabled', 'reauth_rotated', 'password_changed')),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
-- The session limit per user and the list of a user's sessions read the active
-- ones; the full index serves the cascade of a principal's deletion; the other two
-- serve the session-cleanup task (R-AUTH-043).
CREATE INDEX ui_session_principal ON ui_session (principal_id) WHERE ended_at IS NULL;
CREATE INDEX ui_session_owner     ON ui_session (principal_id);
CREATE INDEX ui_session_ended     ON ui_session (ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX ui_session_expiry    ON ui_session (absolute_expires_at) WHERE ended_at IS NULL;
CREATE TRIGGER ui_session_kind BEFORE INSERT OR UPDATE OF principal_id ON ui_session
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user');

-- Holds PKCE verifiers in clear: backup export class transient. state_sha256 is
-- the key; the state itself is not stored. Deleted with DELETE ... RETURNING on
-- the callback, which makes a flow single-use on any replica (R-AUTH-006 step 1).
CREATE TABLE oidc_login_flow (
    state_sha256            bytea       PRIMARY KEY CHECK (octet_length(state_sha256) = 32),
    provider_id             text        NOT NULL,
    browser_binding_sha256  bytea       NOT NULL CHECK (octet_length(browser_binding_sha256) = 32),
    code_verifier           text        NOT NULL,
    nonce_sha256            bytea       NOT NULL CHECK (octet_length(nonce_sha256) = 32),
    purpose                 text        NOT NULL CHECK (purpose IN ('login', 'reauth')),
    reauth_session_id       uuid,
    return_to               text        NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    expires_at              timestamptz NOT NULL,
    CHECK ((purpose = 'reauth') = (reauth_session_id IS NOT NULL))
);
CREATE INDEX oidc_login_flow_expires ON oidc_login_flow (expires_at);

-- Command-line logins of npm and of the device flow, both P1 (R-AUTH-034,
-- R-AUTH-036). The table is created with the login state because the MVP counts
-- its pending rows together with oidc_login_flow (max_pending_flows, R-AUTH-038)
-- and deletes its expired rows in session-cleanup (R-AUTH-043). Backup export
-- class transient.
CREATE TABLE cli_login (
    id                    uuid        PRIMARY KEY,
    public_id_sha256      bytea       NOT NULL UNIQUE CHECK (octet_length(public_id_sha256) = 32),
    poll_secret_sha256    bytea       NOT NULL UNIQUE CHECK (octet_length(poll_secret_sha256) = 32),
    client_kind           text        NOT NULL CHECK (client_kind IN ('npm', 'device')),
    client_name           text,
    repository_key        text,
    user_code             text,
    requested_addr        inet,
    requested_ua          text,
    status                text        NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    decided_by            uuid        REFERENCES principal (id) ON DELETE CASCADE,
    token_scope           jsonb,
    token_ttl             interval,
    token_self_rotatable  boolean     NOT NULL DEFAULT false,
    refused_poll_count    integer     NOT NULL DEFAULT 0,
    last_refused_addr     inet,
    last_refused_at       timestamptz,
    last_polled_at        timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    expires_at            timestamptz NOT NULL
);
CREATE INDEX cli_login_expires ON cli_login (expires_at);
CREATE INDEX cli_login_user_code ON cli_login (user_code) WHERE user_code IS NOT NULL AND status = 'pending';
CREATE INDEX cli_login_decided_by ON cli_login (decided_by) WHERE decided_by IS NOT NULL;

-- The break-glass lockout and the rotation hold, counted in PostgreSQL so that a
-- Valkey failover or flush cannot hand a guesser a fresh threshold (R-AUTH-038,
-- Q-AUTH-11). One row per locked account, today only 'break-glass'. A failure is
-- counted with INSERT ... ON CONFLICT (account_key) DO UPDATE, and every
-- login attempt reads the row; no replica caches it. The counters of every other
-- limit on callers are disposable and live in Valkey with per-replica fallback
-- buckets, so they have no table.
CREATE TABLE auth_lockout (
    account_key          text        PRIMARY KEY,
    failures             integer     NOT NULL DEFAULT 0,
    window_started_at    timestamptz NOT NULL,
    locked_until         timestamptz,
    password_changed_at  timestamptz
);
