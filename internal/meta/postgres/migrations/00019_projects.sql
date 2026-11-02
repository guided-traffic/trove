-- trove:phase expand
--
-- Projects (R-AUTHZ-026, Q-AUTHZ-169, Q-AUTHZ-173): the unit of delegated
-- administration. A project has administrators (users and groups), repositories
-- assigned to it, project policies and project service accounts. Every change of
-- these tables notifies the area `authz` in its transaction (R-AUTHZ-028); an
-- assignment notifies `repo_config` as well.
--
-- Rolling upgrade (Q-AUTHZ-174). The binaries of 0.9.x build their grant index
-- without any notion of a project: they would read a project policy's pattern and
-- class selectors as global ones and grant beyond the project. The grants of a
-- project policy therefore live in authz_project_policy_entry, a table those
-- binaries do not read, so for them a project policy has no entries and covers
-- nobody (fail closed). authz_policy_entry refuses a row for a project policy
-- (trigger below), so a 0.9.x replica that rewrites such a policy fails instead
-- of turning its grants global. Both statements are enforced by the database, not
-- only by the new code.

CREATE TABLE project (
    id                   uuid        PRIMARY KEY,
    key                  text        NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9-]{1,31}$'),
    display_name         text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
    description          text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 1024),
    manage_policies      boolean     NOT NULL DEFAULT true,
    manage_repositories  boolean     NOT NULL DEFAULT false,
    -- version is the ETag of the project (R-AUTHZ-026) and is registered with
    -- meta.RegisterRevisionColumn.
    version              bigint      NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid        REFERENCES principal (id) ON DELETE SET NULL,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid        REFERENCES principal (id) ON DELETE SET NULL
);

-- Project administrators are users and groups (Q-AUTHZ-183): a service account
-- or a CI mapping never administers a project.
CREATE TABLE project_admin (
    project_id    uuid NOT NULL REFERENCES project (id) ON DELETE CASCADE,
    principal_id  uuid NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, principal_id)
);
CREATE INDEX project_admin_principal ON project_admin (principal_id);
CREATE TRIGGER project_admin_kind BEFORE INSERT OR UPDATE OF principal_id ON project_admin
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user', 'group');

-- A repository belongs to at most one project. The project cannot be deleted
-- while a repository is assigned (the default NO ACTION; the service answers
-- 409 project_not_empty first). Assignment is a write of this column, by an
-- administrator or in the transaction that creates the repository.
ALTER TABLE repositories ADD COLUMN project_id uuid REFERENCES project (id);
CREATE INDEX repositories_project ON repositories (project_id) WHERE project_id IS NOT NULL;

-- A project service account belongs to its project for good (Q-AUTHZ-169). The
-- project is deleted only without accounts (NO ACTION). The column is set when
-- the account is created and never changes.
ALTER TABLE service_account ADD COLUMN project_id uuid REFERENCES project (id);
CREATE INDEX service_account_project ON service_account (project_id) WHERE project_id IS NOT NULL;

-- A project policy: deleted with its project. Its grants are stored in
-- authz_project_policy_entry, not in authz_policy_entry (Q-AUTHZ-174). The
-- unique constraint lets the entry table name the project together with the
-- policy, so a row for a global policy cannot exist.
ALTER TABLE authz_policy ADD COLUMN project_id uuid REFERENCES project (id) ON DELETE CASCADE;
ALTER TABLE authz_policy ADD CONSTRAINT authz_policy_id_project UNIQUE (id, project_id);
CREATE INDEX authz_policy_project ON authz_policy (project_id) WHERE project_id IS NOT NULL;

CREATE TABLE authz_project_policy_entry (
    policy_id     uuid   NOT NULL,
    project_id    uuid   NOT NULL,
    principal_id  uuid   NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
    actions       text[] NOT NULL CHECK (cardinality(actions) > 0
                    AND actions <@ ARRAY['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage']::text[]),
    PRIMARY KEY (policy_id, principal_id),
    FOREIGN KEY (policy_id, project_id) REFERENCES authz_policy (id, project_id) ON DELETE CASCADE
);
CREATE INDEX authz_project_policy_entry_principal ON authz_project_policy_entry (principal_id);

-- A row of authz_policy_entry for a project policy is refused: the binaries of
-- the previous release write there, and a project policy they rewrite must fail
-- rather than become global (SQLSTATE AI004).
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

-- The name of a project service account starts with <project key>-, and its
-- project never changes (Q-AUTHZ-176). SQLSTATE AI003; the API answers
-- 422 project_prefix_required first.
CREATE FUNCTION service_account_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    k text;
    n text;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.project_id IS DISTINCT FROM OLD.project_id THEN
        RAISE EXCEPTION 'the project of a service account cannot change' USING ERRCODE = 'AI003';
    END IF;
    IF NEW.project_id IS NOT NULL THEN
        SELECT key INTO k FROM project WHERE id = NEW.project_id;
        SELECT name INTO n FROM principal WHERE id = NEW.principal_id;
        IF k IS NULL OR n IS NULL OR left(n, char_length(k) + 1) <> k || '-' OR char_length(n) = char_length(k) + 1 THEN
            RAISE EXCEPTION 'the name of a project service account starts with the project key and a hyphen'
                  USING ERRCODE = 'AI003';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER service_account_project BEFORE INSERT OR UPDATE OF project_id ON service_account
    FOR EACH ROW EXECUTE FUNCTION service_account_project_guard();
