-- trove:phase expand
--
-- Projects (R-AUTHZ-026, Q-AUTHZ-174, Q-AUTHZ-175): the unique constraint of
-- authz_policy from the index of 00023, the table of the grants of project
-- policies, and the guards on principals of project service accounts.

ALTER TABLE authz_policy ADD CONSTRAINT authz_policy_id_project UNIQUE USING INDEX authz_policy_id_project;

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
