-- trove:phase expand
--
-- Identity (M1, WP1.2): the shared principal table (R-AUTH-016), users
-- (R-AUTH-016), groups and memberships (R-AUTH-018), service accounts
-- (R-AUTH-020), API tokens (R-AUTH-022, R-AUTH-023) and the runtime
-- authentication settings (R-AUTH-040). Only SHA-256 hashes of token secrets
-- are stored (R-AUTH-023). Sessions, login flows and the break-glass lockout are
-- in 00008_login.
--
-- Every change of a credential, a principal or a setting records itself in the
-- change area `auth` in its own transaction (R-AUTH-023); the area's row of
-- cluster_epoch is seeded here.

INSERT INTO cluster_epoch (area) VALUES ('auth');

CREATE TABLE principal (
    id          uuid        PRIMARY KEY,
    kind        text        NOT NULL CHECK (kind IN ('user', 'group', 'service_account', 'anonymous', 'authenticated', 'ci_mapping')),
    name        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid        REFERENCES principal (id) ON DELETE SET NULL,
    UNIQUE (kind, name),
    -- The name rule of every kind (R-AUTH-007, R-AUTH-016, R-AUTH-018, R-AUTH-020).
    -- The reserved names are not user names; the built-in kinds have one name each.
    CHECK (CASE kind
        WHEN 'user'            THEN name ~ '^[a-z0-9][a-z0-9._@+-]{0,127}$' AND name NOT IN ('anonymous', 'authenticated', 'system', 'me')
        WHEN 'group'           THEN name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
        WHEN 'service_account' THEN name ~ '^[a-z0-9][a-z0-9-]{1,62}$'
        WHEN 'anonymous'       THEN name = 'anonymous'
        WHEN 'authenticated'   THEN name = 'authenticated'
        WHEN 'ci_mapping'      THEN name ~ '^[a-z0-9][a-z0-9._-]{1,63}/[a-z0-9][a-z0-9._-]{1,63}$'
    END)
);
-- The built-in kinds have exactly one row (R-AUTH-037, R-AUTHZ-030).
CREATE UNIQUE INDEX principal_builtin_one ON principal (kind) WHERE kind IN ('anonymous', 'authenticated');

-- The two built-in principals have fixed identifiers, so code and the revocation
-- journal can name them without a lookup.
INSERT INTO principal (id, kind, name) VALUES
    ('00000000-0000-0000-0000-000000000001', 'anonymous',     'anonymous'),
    ('00000000-0000-0000-0000-000000000002', 'authenticated', 'authenticated');

-- The built-in principals can be neither deleted nor renamed nor disabled
-- (R-AUTH-037); a TRUNCATE of a restore does not fire row triggers.
CREATE FUNCTION principal_builtin_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the built-in principal % cannot be changed or deleted', OLD.name
          USING ERRCODE = 'AI001';
END $$;

CREATE TRIGGER principal_builtin_del BEFORE DELETE ON principal
    FOR EACH ROW WHEN (OLD.kind IN ('anonymous', 'authenticated')) EXECUTE FUNCTION principal_builtin_guard();
CREATE TRIGGER principal_builtin_upd BEFORE UPDATE OF id, kind, name, status ON principal
    FOR EACH ROW WHEN (OLD.kind IN ('anonymous', 'authenticated')) EXECUTE FUNCTION principal_builtin_guard();

-- Names the kinds a principal column may reference: a row of a subtype table, a
-- membership, a token, a session or a role binding pointing at a principal of
-- the wrong kind is refused (SQLSTATE AI001). The column name is the first
-- trigger argument, the allowed kinds follow. Foreign keys cannot express this.
CREATE FUNCTION principal_kind_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    k text;
BEGIN
    SELECT kind INTO k FROM principal WHERE id = (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
    IF k IS NULL OR NOT (k = ANY (TG_ARGV[1:])) THEN
        RAISE EXCEPTION '%.% must reference a principal of kind %, found %',
              TG_TABLE_NAME, TG_ARGV[0], array_to_string(TG_ARGV[1:], ' or '), coalesce(k, 'none')
              USING ERRCODE = 'AI001';
    END IF;
    RETURN NEW;
END $$;

CREATE TABLE app_user (
    principal_id              uuid        PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
    email                     text,
    email_verified            boolean     NOT NULL DEFAULT false,
    display_name              text,
    origin                    text        NOT NULL CHECK (origin IN ('jit', 'admin', 'scim', 'system')),
    is_break_glass            boolean     NOT NULL DEFAULT false,
    scim_external_id          text        UNIQUE,
    last_login_at             timestamptz,
    last_interactive_auth_at  timestamptz,
    disabled_reason           text        CHECK (disabled_reason IN ('admin', 'dormant', 'scim')),
    disabled_at               timestamptz,
    dormancy_warned_at        timestamptz
);
CREATE UNIQUE INDEX app_user_one_break_glass ON app_user (is_break_glass) WHERE is_break_glass;
CREATE TRIGGER app_user_kind BEFORE INSERT OR UPDATE OF principal_id ON app_user
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user');

CREATE TABLE user_group (
    principal_id      uuid    PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
    description       text    NOT NULL DEFAULT '',
    auto_join         boolean NOT NULL DEFAULT false,
    origin            text    NOT NULL CHECK (origin IN ('admin', 'oidc', 'scim')),
    scim_external_id  text    UNIQUE
);
CREATE TRIGGER user_group_kind BEFORE INSERT OR UPDATE OF principal_id ON user_group
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'group');

-- A principal is a member if any source says so (R-AUTH-018). The members are
-- users and service accounts; the built-in principals and CI mappings cannot be
-- members.
CREATE TABLE group_membership (
    group_id   uuid        NOT NULL REFERENCES user_group (principal_id) ON DELETE CASCADE,
    member_id  uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    source     text        NOT NULL CHECK (source IN ('manual', 'auto', 'scim') OR source ~ '^oidc:[a-z0-9][a-z0-9-]{0,31}$'),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, member_id, source)
);
CREATE INDEX group_membership_member ON group_membership (member_id);
CREATE TRIGGER group_membership_kind BEFORE INSERT OR UPDATE OF member_id ON group_membership
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('member_id', 'user', 'service_account');

-- service_account_owner (owners who manage an account's tokens, R-AUTH-020) is P1
-- and arrives with its work package.
CREATE TABLE service_account (
    principal_id  uuid PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
    description   text NOT NULL DEFAULT ''
);
CREATE TRIGGER service_account_kind BEFORE INSERT OR UPDATE OF principal_id ON service_account
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'service_account');

-- Token secrets are never stored: secret_sha256 is the SHA-256 of the full
-- 57-character string (R-AUTH-023). rotated_from and derived_from name another
-- token by id without a foreign key: the table registry of R-BAK-004 allows one
-- cycle-breaking column per table, and the retention task (token-cleanup) deletes
-- a parent before its children, so a lineage pointer may name a row that is gone
-- (Q-AUTH-15). The scope member `endpoints` binds a token to endpoints (R-EP-011).
CREATE TABLE api_token (
    id                   uuid        PRIMARY KEY,
    type                 text        NOT NULL CHECK (type IN ('pat', 'sat', 'cit')),
    secret_sha256        bytea       NOT NULL UNIQUE CHECK (octet_length(secret_sha256) = 32),
    hint                 text        NOT NULL,
    principal_id         uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    description          text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 1024),
    scope                jsonb       NOT NULL,
    self_rotatable       boolean     NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid        REFERENCES principal (id) ON DELETE SET NULL,
    created_via          text        NOT NULL CHECK (created_via IN ('ui', 'api', 'npm-web-login', 'device-login',
                                                                     'admin-cli', 'rotation', 'ci-exchange')),
    expires_at           timestamptz,
    revoked_at           timestamptz,
    revoked_by           uuid        REFERENCES principal (id) ON DELETE SET NULL,
    revocation_reason    text        CHECK (revocation_reason IN ('owner', 'admin', 'rotated', 'npm-logout', 'leaked',
                                                                  'principal_disabled', 'scim', 'mapping_deleted',
                                                                  'parent_revoked')),
    rotated_from         uuid,
    derived_from         uuid,
    last_used_at         timestamptz,
    last_used_addr       inet,
    rejection_logged_at  timestamptz,
    expiry_notified_at   timestamptz,
    ci_context           jsonb,
    CHECK (type = 'sat' OR expires_at IS NOT NULL),
    CHECK (revocation_reason IS NULL OR revoked_at IS NOT NULL)
);
-- The request path reads by secret_sha256 (the unique index). The partial indexes
-- serve the limit on active tokens per principal and the expiry notices; the full
-- one serves token listings and the cascade of a principal's deletion; the
-- remaining ones serve the retention task and the lineage cascades (R-AUTH-029).
CREATE INDEX api_token_principal_active ON api_token (principal_id) WHERE revoked_at IS NULL;
CREATE INDEX api_token_expiry_active    ON api_token (expires_at)   WHERE revoked_at IS NULL;
CREATE INDEX api_token_principal        ON api_token (principal_id, created_at DESC, id);
CREATE INDEX api_token_revoked          ON api_token (revoked_at)   WHERE revoked_at IS NOT NULL;
CREATE INDEX api_token_rotated_from     ON api_token (rotated_from) WHERE rotated_from IS NOT NULL;
CREATE INDEX api_token_derived_from     ON api_token (derived_from) WHERE derived_from IS NOT NULL;
CREATE TRIGGER api_token_kind BEFORE INSERT OR UPDATE OF principal_id ON api_token
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user', 'service_account', 'ci_mapping');

-- Runtime authentication settings: one row. NULL means the YAML value
-- applies. The row exists from the start so that a read always finds it.
CREATE TABLE auth_settings (
    singleton         boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    anonymous_access  boolean     NOT NULL DEFAULT false,
    pat_default_ttl   interval,
    pat_max_ttl       interval,
    sat_default_ttl   interval,
    sat_max_ttl       interval,
    expiry_notice     interval,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    updated_by        uuid        REFERENCES principal (id) ON DELETE SET NULL
);
INSERT INTO auth_settings (singleton) VALUES (true);
