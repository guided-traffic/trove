-- trove:phase expand
--
-- Authorization (M1, WP1.3): permission policies with their selectors, patterns
-- and entries (R-AUTHZ-021, R-AUTHZ-028) and the role bindings of the fixed system
-- roles (R-AUTHZ-022). Authorization is deny by default (R-AUTHZ-001): without a
-- row in these tables only the break-glass administrator can act.
--
-- The compiled grant index of every replica is built from these tables and from
-- repositories in one REPEATABLE READ READ ONLY transaction. Every write to them
-- calls cluster.Notify("authz", ...) in its transaction, which advances the epoch
-- of the area (R-AUTHZ-028); there is no table authz_epoch, the epoch is the row
-- of cluster_epoch seeded here. The projects of R-AUTHZ-026 (P1) add the column
-- authz_policy.project_id with their own migration.

INSERT INTO cluster_epoch (area) VALUES ('authz');

-- version is the ETag of the policy (R-AUTHZ-021) and is registered with
-- meta.RegisterRevisionColumn. resource_type 'build' belongs to P1
-- (R-AUTHZ-027); the value is allowed now because a CHECK cannot be widened by an
-- expand step.
CREATE TABLE authz_policy (
    id             uuid        PRIMARY KEY,
    name           text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    description    text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 1024),
    resource_type  text        NOT NULL DEFAULT 'repository' CHECK (resource_type IN ('repository', 'build')),
    version        bigint      NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    created_by     uuid        REFERENCES principal (id) ON DELETE SET NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid        REFERENCES principal (id) ON DELETE SET NULL
);

-- Repository selectors (R-AUTHZ-004). A key selector names its repository by id
-- and goes with it; the policy stays and reports itself as inert.
CREATE TABLE authz_policy_selector (
    policy_id      uuid     NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
    ordinal        smallint NOT NULL,
    kind           text     NOT NULL CHECK (kind IN ('key', 'pattern', 'class')),
    repository_id  bigint   REFERENCES repositories (id) ON DELETE CASCADE,
    key_pattern    text     CHECK (char_length(key_pattern) <= 1024),
    repo_class     text     CHECK (repo_class IN ('local', 'remote', 'virtual', 'any')),
    format         text,
    PRIMARY KEY (policy_id, ordinal),
    CHECK ((kind = 'key')     = (repository_id IS NOT NULL)),
    CHECK ((kind = 'pattern') = (key_pattern IS NOT NULL)),
    CHECK ((kind = 'class')   = (repo_class IS NOT NULL)),
    CHECK (kind <> 'key' OR format IS NULL)
);
CREATE INDEX authz_policy_selector_repo ON authz_policy_selector (repository_id) WHERE repository_id IS NOT NULL;

-- Package-name and path patterns (R-AUTHZ-005). The octet bound keeps a pattern
-- below the size limit of a btree entry.
CREATE TABLE authz_policy_pattern (
    policy_id  uuid NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
    dimension  text NOT NULL CHECK (dimension IN ('package', 'path', 'build')),
    mode       text NOT NULL CHECK (mode IN ('include', 'exclude')),
    pattern    text NOT NULL CHECK (char_length(pattern) BETWEEN 1 AND 1024 AND octet_length(pattern) <= 2000),
    PRIMARY KEY (policy_id, dimension, mode, pattern)
);

-- Grants. The actions are stored expanded; presets exist only in the API
-- (R-AUTHZ-001). anonymous and authenticated are principals like any other
-- (R-AUTHZ-003, R-AUTHZ-030); the rules that limit their actions are validation.
CREATE TABLE authz_policy_entry (
    policy_id     uuid   NOT NULL REFERENCES authz_policy (id) ON DELETE CASCADE,
    principal_id  uuid   NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    actions       text[] NOT NULL CHECK (cardinality(actions) > 0
                    AND actions <@ ARRAY['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage']::text[]),
    PRIMARY KEY (policy_id, principal_id)
);
CREATE INDEX authz_policy_entry_principal ON authz_policy_entry (principal_id);

-- Roles are bound to users, groups and service accounts only (R-AUTHZ-010); a
-- binding to anonymous, authenticated or a CI mapping is refused by the guard
-- (SQLSTATE AI001), the API answers 422 role_not_allowed before it gets there.
CREATE TABLE authz_role_binding (
    role          text        NOT NULL CHECK (role IN ('admin', 'security-admin', 'auditor', 'repository-admin', 'webhook-admin')),
    principal_id  uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    granted_by    uuid        REFERENCES principal (id) ON DELETE SET NULL,
    granted_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role, principal_id)
);
CREATE INDEX authz_role_binding_principal ON authz_role_binding (principal_id);
CREATE TRIGGER authz_role_binding_kind BEFORE INSERT OR UPDATE OF principal_id ON authz_role_binding
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user', 'group', 'service_account');
