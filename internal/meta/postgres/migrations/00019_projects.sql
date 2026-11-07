-- trove:phase expand
--
-- Projects (R-AUTHZ-026, Q-AUTHZ-206 to Q-AUTHZ-209, Q-REPO-212): the container of
-- repositories and the unit of delegated administration. A project has members with
-- a role (reader, developer, maintainer, owner), repositories, project policies and
-- project service accounts. Every change of these tables notifies the area `authz`
-- in its transaction (R-AUTHZ-028); a change of a repository's project notifies
-- `repo_config` as well.
--
-- Every repository belongs to exactly one project and its key is unique within it
-- (UNIQUE (project_id, key), attached in 00024). The service accounts, policies and
-- CI mappings of a project carry the project key as the first segment of their
-- stored name, `<project key>/<name>`, and never change their project; the names
-- of instance-wide objects have no `/`. `/` is in no project, repository or object
-- key, so no prefix or overlap rule is needed (Q-AUTHZ-209).
--
-- The grants of a project policy live in authz_project_policy_entry, not in
-- authz_policy_entry; the latter refuses a row for a project policy (trigger below,
-- SQLSTATE AI004), so a project policy never turns global.
--
-- Expand class (R-HA-018). The tables that already exist are touched without a long
-- lock: the columns are added plain, their foreign keys NOT VALID, their indexes are
-- built CONCURRENTLY in the steps 00020 to 00023, the unique constraints come from
-- such indexes (00024), and the foreign keys are validated in 00025. The tables this
-- step creates are empty and take their indexes and keys inline.

CREATE TABLE project (
    id                   uuid        PRIMARY KEY,
    key                  text        NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9-]{1,31}$'),
    display_name         text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
    description          text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 1024),
    -- version is the ETag of the project (R-AUTHZ-026) and is registered with
    -- meta.RegisterRevisionColumn.
    version              bigint      NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid        REFERENCES principal (id) ON DELETE SET NULL,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid        REFERENCES principal (id) ON DELETE SET NULL
);

-- A project's members are users and groups (Q-AUTHZ-183, Q-AUTHZ-207): a service
-- account or a CI mapping is no member and reaches a project through its policies.
-- The role grants its actions on every repository of the project (Q-AUTHZ-208).
CREATE TABLE project_member (
    project_id    uuid NOT NULL REFERENCES project (id) ON DELETE CASCADE,
    principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    role          text NOT NULL CHECK (role IN ('reader', 'developer', 'maintainer', 'owner')),
    PRIMARY KEY (project_id, principal_id)
);
CREATE INDEX project_member_principal ON project_member (principal_id);
CREATE INDEX project_member_owner ON project_member (project_id) WHERE role = 'owner';
CREATE TRIGGER project_member_kind BEFORE INSERT OR UPDATE OF principal_id ON project_member
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user', 'group');

-- The key of a project is part of the stored names of its accounts, policies and
-- mappings and never changes (SQLSTATE AI003).
CREATE FUNCTION project_key_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the key of a project cannot change' USING ERRCODE = 'AI003';
END $$;
CREATE TRIGGER project_key_fixed BEFORE UPDATE OF key ON project
    FOR EACH ROW WHEN (NEW.key IS DISTINCT FROM OLD.key) EXECUTE FUNCTION project_key_immutable();

-- A repository belongs to exactly one project (Q-AUTHZ-206). The project cannot be
-- deleted while a repository belongs to it (the default NO ACTION; the service
-- answers 409 project_not_empty first). The column is written in the transaction
-- that creates the repository, and by a move to another project (Q-REPO-212).
ALTER TABLE repositories ADD COLUMN project_id uuid NOT NULL;
ALTER TABLE repositories ADD CONSTRAINT repositories_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES project (id) NOT VALID;

-- A project service account belongs to its project for good (Q-AUTHZ-169). The
-- project is deleted only without accounts (NO ACTION). The column is set when
-- the account is created and never changes.
ALTER TABLE service_account ADD COLUMN project_id uuid;
ALTER TABLE service_account ADD CONSTRAINT service_account_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES project (id) NOT VALID;

-- A project policy: deleted with its project. Its grants are stored in
-- authz_project_policy_entry, not in authz_policy_entry (Q-AUTHZ-174). The
-- unique constraint (00024) lets the entry table name the project together with
-- the policy, so a row for a global policy cannot exist.
ALTER TABLE authz_policy ADD COLUMN project_id uuid;
ALTER TABLE authz_policy ADD CONSTRAINT authz_policy_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES project (id) ON DELETE CASCADE NOT VALID;

-- A row of authz_policy_entry for a project policy is refused: its grants have
-- their own table, so a project policy cannot become global through this one
-- (SQLSTATE AI004).
CREATE FUNCTION authz_policy_entry_global_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM authz_policy WHERE id = NEW.policy_id AND project_id IS NOT NULL) THEN
        RAISE EXCEPTION 'the grants of a project policy belong in authz_project_policy_entry'
              USING ERRCODE = 'AI004';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER authz_policy_entry_global BEFORE INSERT OR UPDATE OF policy_id ON authz_policy_entry
    FOR EACH ROW EXECUTE FUNCTION authz_policy_entry_global_only();

-- A project service account is no group member (Q-AUTHZ-175): a global group's
-- grants would reach the project's account, and the group's other members' grants
-- through it. SQLSTATE AI002; the API answers 422 project_account_not_groupable
-- before it gets here.
CREATE FUNCTION group_member_not_project_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM service_account WHERE principal_id = NEW.member_id AND project_id IS NOT NULL) THEN
        RAISE EXCEPTION 'a project service account cannot be a group member'
              USING ERRCODE = 'AI002';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER group_membership_project_account BEFORE INSERT OR UPDATE OF member_id ON group_membership
    FOR EACH ROW EXECUTE FUNCTION group_member_not_project_account();

-- The stored name of a project service account is <project key>/<name>, an
-- instance-wide one has no `/`, and the project never changes (Q-AUTHZ-169,
-- Q-AUTHZ-209). SQLSTATE AI003; the API answers 422 first.
CREATE FUNCTION service_account_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    k text;
    n text;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.project_id IS DISTINCT FROM OLD.project_id THEN
        RAISE EXCEPTION 'the project of a service account cannot change' USING ERRCODE = 'AI003';
    END IF;
    SELECT name INTO n FROM principal WHERE id = NEW.principal_id;
    IF n IS NULL THEN
        RAISE EXCEPTION 'the principal of a service account does not exist' USING ERRCODE = 'AI003';
    END IF;
    IF NEW.project_id IS NULL THEN
        IF position('/' IN n) > 0 THEN
            RAISE EXCEPTION 'the name of an instance-wide service account has no project prefix' USING ERRCODE = 'AI003';
        END IF;
    ELSE
        SELECT key INTO k FROM project WHERE id = NEW.project_id;
        IF k IS NULL OR left(n, char_length(k) + 1) <> k || '/' OR char_length(n) = char_length(k) + 1
           OR position('/' IN substr(n, char_length(k) + 2)) > 0 THEN
            RAISE EXCEPTION 'the name of a project service account is <project key>/<name>' USING ERRCODE = 'AI003';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER service_account_project BEFORE INSERT OR UPDATE OF project_id ON service_account
    FOR EACH ROW EXECUTE FUNCTION service_account_project_guard();

-- The same for a policy: <project key>/<name> for a project policy, no `/` otherwise,
-- the project never changes (SQLSTATE AI003).
CREATE FUNCTION authz_policy_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    k text;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.project_id IS DISTINCT FROM OLD.project_id THEN
        RAISE EXCEPTION 'the project of a policy cannot change' USING ERRCODE = 'AI003';
    END IF;
    IF NEW.project_id IS NULL THEN
        IF position('/' IN NEW.name) > 0 THEN
            RAISE EXCEPTION 'the name of an instance-wide policy has no project prefix' USING ERRCODE = 'AI003';
        END IF;
    ELSE
        SELECT key INTO k FROM project WHERE id = NEW.project_id;
        IF k IS NULL OR left(NEW.name, char_length(k) + 1) <> k || '/' OR char_length(NEW.name) = char_length(k) + 1
           OR position('/' IN substr(NEW.name, char_length(k) + 2)) > 0 THEN
            RAISE EXCEPTION 'the name of a project policy is <project key>/<name>' USING ERRCODE = 'AI003';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER authz_policy_project BEFORE INSERT OR UPDATE OF project_id, name ON authz_policy
    FOR EACH ROW EXECUTE FUNCTION authz_policy_project_guard();
