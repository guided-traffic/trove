-- trove:phase expand
--
-- Projects (R-AUTHZ-026, Q-AUTHZ-174, Q-AUTHZ-175): the unique constraints of
-- repositories (00020) and authz_policy (00023) from their indexes, the table of the grants of project
-- policies, and the guards on principals of project service accounts.

ALTER TABLE authz_policy ADD CONSTRAINT authz_policy_id_project UNIQUE USING INDEX authz_policy_id_project;
ALTER TABLE repositories ADD CONSTRAINT repositories_project_key UNIQUE USING INDEX repositories_project_key;

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

-- A project service account is the principal of nothing global (Q-AUTHZ-175): not
-- of a role binding, not of an entry of a global policy, and in a project policy
-- only of its own project. SQLSTATE AI005; the API answers 422 before it gets here.
-- The first argument names the column, the second says what the table holds
-- ('global' or 'project').
CREATE FUNCTION project_account_principal_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    p uuid;
BEGIN
    SELECT project_id INTO p FROM service_account WHERE principal_id = NEW.principal_id;
    IF p IS NULL THEN
        RETURN NEW;
    END IF;
    IF TG_ARGV[0] = 'global' OR p IS DISTINCT FROM (to_jsonb(NEW) ->> 'project_id')::uuid THEN
        RAISE EXCEPTION 'a project service account is not a principal of %.%', TG_TABLE_NAME, 'principal_id'
              USING ERRCODE = 'AI005';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER authz_role_binding_project_account BEFORE INSERT OR UPDATE OF principal_id ON authz_role_binding
    FOR EACH ROW EXECUTE FUNCTION project_account_principal_guard('global');
CREATE TRIGGER authz_policy_entry_project_account BEFORE INSERT OR UPDATE OF principal_id ON authz_policy_entry
    FOR EACH ROW EXECUTE FUNCTION project_account_principal_guard('global');
CREATE TRIGGER authz_project_policy_entry_account BEFORE INSERT OR UPDATE OF principal_id, project_id ON authz_project_policy_entry
    FOR EACH ROW EXECUTE FUNCTION project_account_principal_guard('project');

-- No repository is shared between projects (Q-AUTHZ-206, Q-REPO-212): a virtual
-- repository lists members, pins rules to members and deploys into repositories of
-- its own project only. The first argument names the column of the referenced
-- repository, the second the column of the virtual repository. SQLSTATE AI006; the
-- service answers 422 first, and a move of a repository that is still referenced
-- answers 409 repository_in_use.
CREATE FUNCTION virtual_same_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    other uuid;
    own   uuid;
BEGIN
    SELECT project_id INTO other FROM repositories WHERE id = (to_jsonb(NEW) ->> TG_ARGV[0])::bigint;
    SELECT project_id INTO own FROM repositories WHERE id = (to_jsonb(NEW) ->> TG_ARGV[1])::bigint;
    IF other IS DISTINCT FROM own THEN
        RAISE EXCEPTION '%.% must name a repository of the project of %.%',
              TG_TABLE_NAME, TG_ARGV[0], TG_TABLE_NAME, TG_ARGV[1] USING ERRCODE = 'AI006';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER virtual_members_project BEFORE INSERT OR UPDATE OF member_id ON virtual_members
    FOR EACH ROW EXECUTE FUNCTION virtual_same_project_guard('member_id', 'virtual_id');
CREATE TRIGGER virtual_deploy_targets_project BEFORE INSERT OR UPDATE OF target_id ON virtual_deploy_targets
    FOR EACH ROW EXECUTE FUNCTION virtual_same_project_guard('target_id', 'virtual_id');
CREATE TRIGGER virtual_pin_members_project BEFORE INSERT OR UPDATE OF member_id ON virtual_pin_members
    FOR EACH ROW EXECUTE FUNCTION virtual_same_project_guard('member_id', 'virtual_id');

-- A repository changes its project only while nothing of its project holds it by ID
-- (R-REPO-060, Q-REPO-217): a key selector of a policy of the project, a virtual
-- repository that lists it as a member, pins a rule to it or deploys into it, and for
-- a virtual repository its own members and deployment target; a global policy's key
-- selector follows it. A project policy's key selector names a repository of the
-- policy's project only, as AI006 holds it for virtual repositories. SQLSTATE AI007;
-- the move answers 409 repository_in_use first and a policy write 422
-- unknown_repository, both under the row locks of the project and the repository, and
-- this guard holds for every write that would not.
CREATE FUNCTION repository_move_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.project_id IS NOT DISTINCT FROM OLD.project_id THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM authz_policy_selector s JOIN authz_policy a ON a.id = s.policy_id
                WHERE s.repository_id = OLD.id AND a.project_id = OLD.project_id)
       OR EXISTS (SELECT 1 FROM virtual_members WHERE member_id = OLD.id OR virtual_id = OLD.id)
       OR EXISTS (SELECT 1 FROM virtual_pin_members WHERE member_id = OLD.id)
       OR EXISTS (SELECT 1 FROM virtual_deploy_targets WHERE target_id = OLD.id OR virtual_id = OLD.id) THEN
        RAISE EXCEPTION 'the repository % is held by its project and does not change it', OLD.id USING ERRCODE = 'AI007';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER repositories_move_guard BEFORE UPDATE OF project_id ON repositories
    FOR EACH ROW EXECUTE FUNCTION repository_move_guard();

CREATE FUNCTION policy_selector_project_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    own   uuid;
    other uuid;
BEGIN
    IF NEW.repository_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT project_id INTO own FROM authz_policy WHERE id = NEW.policy_id;
    IF own IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT project_id INTO other FROM repositories WHERE id = NEW.repository_id;
    IF other IS DISTINCT FROM own THEN
        RAISE EXCEPTION 'authz_policy_selector.repository_id must name a repository of the project of the policy'
              USING ERRCODE = 'AI007';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER authz_policy_selector_project BEFORE INSERT OR UPDATE OF repository_id, policy_id ON authz_policy_selector
    FOR EACH ROW EXECUTE FUNCTION policy_selector_project_guard();
