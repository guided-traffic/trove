-- trove:phase contract
-- trove:requires-app >= 0.1.0
--
-- The principal kind 'authenticated' goes, and 'anonymous' stays only as the actor of a request without a credential
-- (R-AUTH-016, Q-AUTH-233, Q-AUTHZ-279): the visibility levels of 00073 replace both as a way to grant. Every grant row that
-- names either is deleted, the row of 'authenticated' (…0002) is deleted, and from here on no table that grants, assigns or
-- owns may name 'anonymous' (SQLSTATE AI009), so no entry can reopen content that the levels keep closed.

DELETE FROM authz_policy_entry          WHERE principal_id IN ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM authz_project_policy_entry  WHERE principal_id IN ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM authz_role_binding          WHERE principal_id IN ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM project_member              WHERE principal_id IN ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');
DELETE FROM package_owner               WHERE principal_id IN ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002');

-- The guards of the built-in rows named both kinds; the trigger function is the same, the triggers lose the second kind.
DROP TRIGGER principal_builtin_del ON principal;
DROP TRIGGER principal_builtin_upd ON principal;
DELETE FROM principal WHERE id = '00000000-0000-0000-0000-000000000002';

DROP INDEX principal_builtin_one;
CREATE UNIQUE INDEX principal_builtin_one ON principal (kind) WHERE kind = 'anonymous';

CREATE TRIGGER principal_builtin_del BEFORE DELETE ON principal
    FOR EACH ROW WHEN (OLD.kind = 'anonymous') EXECUTE FUNCTION principal_builtin_guard();
CREATE TRIGGER principal_builtin_upd BEFORE UPDATE OF id, kind, name, status ON principal
    FOR EACH ROW WHEN (OLD.kind = 'anonymous') EXECUTE FUNCTION principal_builtin_guard();

ALTER TABLE principal
    DROP CONSTRAINT principal_kind_check,
    ADD CONSTRAINT principal_kind_check
        CHECK (kind IN ('user', 'group', 'service_account', 'anonymous', 'ci_mapping', 'bastion'));

ALTER TABLE principal
    DROP CONSTRAINT principal_check,
    ADD CONSTRAINT principal_check CHECK (CASE kind
        WHEN 'user'            THEN name ~ '^[a-z0-9][a-z0-9._@+-]{0,127}$' AND name NOT IN ('anonymous', 'authenticated', 'system', 'me')
        WHEN 'group'           THEN name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
        WHEN 'service_account' THEN name ~ '^((?=[a-z0-9-]{2,32}/)[a-z][a-z0-9]*(?:-+[a-z0-9]+)*/)?[a-z0-9][a-z0-9-]{1,62}$'
        WHEN 'anonymous'       THEN name = 'anonymous'
        WHEN 'ci_mapping'      THEN name ~ '^[a-z0-9][a-z0-9._-]{1,63}/((?=[a-z0-9-]{2,32}/)[a-z][a-z0-9]*(?:-+[a-z0-9]+)*/)?[a-z0-9][a-z0-9._-]{1,63}$'
    END);

-- The API answers 422 principal_not_allowed before it gets here; a restore, a journal replay and a configuration file meet the
-- same refusal in the database. The triggers are named so that they fire before the
-- kind guards of the tables, which refuse the same row with AI001: the answer is AI009 whichever guard would have caught it.
CREATE FUNCTION principal_not_anonymous() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.principal_id = '00000000-0000-0000-0000-000000000001' THEN
        RAISE EXCEPTION '% cannot name the anonymous principal; a visibility level opens content', TG_TABLE_NAME
              USING ERRCODE = 'AI009';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER a_not_anonymous BEFORE INSERT OR UPDATE OF principal_id ON authz_policy_entry
    FOR EACH ROW EXECUTE FUNCTION principal_not_anonymous();
CREATE TRIGGER a_not_anonymous BEFORE INSERT OR UPDATE OF principal_id ON authz_project_policy_entry
    FOR EACH ROW EXECUTE FUNCTION principal_not_anonymous();
CREATE TRIGGER a_not_anonymous BEFORE INSERT OR UPDATE OF principal_id ON authz_role_binding
    FOR EACH ROW EXECUTE FUNCTION principal_not_anonymous();
CREATE TRIGGER a_not_anonymous BEFORE INSERT OR UPDATE OF principal_id ON project_member
    FOR EACH ROW EXECUTE FUNCTION principal_not_anonymous();
CREATE TRIGGER a_not_anonymous BEFORE INSERT OR UPDATE OF principal_id ON package_owner
    FOR EACH ROW EXECUTE FUNCTION principal_not_anonymous();
