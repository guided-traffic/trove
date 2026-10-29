-- trove:phase expand
--
-- Management API conventions (M1, WP1.6): idempotency keys (R-API-014), the
-- caller's UI preferences (R-API-021) and the canary rows of the stored-secret
-- key ring (R-ARCH-021, R-API-049).

-- The stored outcome of an idempotent create or job start, keyed by the principal
-- and the client's key. The row is inserted in the transaction of the change, so a
-- retry behaves the same on every replica (R-API-014, R-API-051). No foreign key:
-- a row only protects a retry for api.idempotency_retention. Backup export class
-- transient; a scheduled task deletes the rows past expires_at hourly (R-API-014).
CREATE TABLE api_idempotency_keys (
    principal_id      uuid        NOT NULL,
    idem_key          text        NOT NULL CHECK (char_length(idem_key) BETWEEN 1 AND 255),
    method            text        NOT NULL,
    route             text        NOT NULL,
    request_sha256    bytea       NOT NULL CHECK (octet_length(request_sha256) = 32),
    response_status   smallint    NOT NULL,
    response_headers  jsonb       NOT NULL DEFAULT '{}',
    response_body     jsonb,
    secret_issued_id  text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    PRIMARY KEY (principal_id, idem_key)
);
CREATE INDEX api_idempotency_keys_expiry ON api_idempotency_keys (expires_at);

-- revision is the ETag of GET /api/v1/users/me/preferences and is registered with
-- meta.RegisterRevisionColumn. The user is app_user(principal_id): the
-- specification's sketch names the table users, which does not exist (Q-API-15).
CREATE TABLE user_preferences (
    user_id             uuid        PRIMARY KEY REFERENCES app_user (principal_id) ON DELETE CASCADE,
    locale              text        CHECK (locale IN ('en', 'de')),
    theme               text        CHECK (theme IN ('light', 'dark')),
    keyboard_shortcuts  boolean     NOT NULL DEFAULT false,
    project_filter      text,
    revision            bigint      NOT NULL DEFAULT 1,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- One row per key ID of the key ring: the known canary text sealed under that
-- key. A replica opens the row of every key it holds at start-up, and the status
-- of R-API-049 reports the result per key ID. Backup export class data: a restore
-- opens the backup's canary rows with the configured ring before it loads anything
-- (R-BAK-020).
CREATE TABLE secret_key_canary (
    key_id      text        PRIMARY KEY,
    ciphertext  text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
