-- trove:phase expand
--
-- Revisions for the identity resources and the runtime authentication settings
-- (R-API-012, Q-API-25). A user, a group and a service account are one row of
-- `principal` plus the row of their kind, so one column serves the three:
-- principal.revision is the ETag of the user, the group or the service account.
-- auth_settings.revision replaces the change time in microseconds that served as
-- the ETag of the singleton row (Q-AUTH-41). Both columns are registered with
-- meta.RegisterRevisionColumn, so a restore raises them (R-BAK-022).
--
-- The revision of a principal grows whenever a member of its representation that
-- an administrator or the sign-in can change changes: the name, the status, the
-- profile data of a user (e-mail, display name, disabled reason), the identity
-- links, the description and auto-join flag of a group, the description of a
-- service account. A trigger does that on the table that holds the member, so no
-- writer (the API, the sign-in, a later SCIM client) can forget it. Counters that
-- change on every sign-in (last_login_at, last_interactive_auth_at) are not
-- members: a sign-in that changes nothing leaves the revision alone. Group
-- memberships are edited as deltas and carry no ETag (R-API-012), so they do not
-- bump it either.

ALTER TABLE principal     ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1);
ALTER TABLE auth_settings ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1);

CREATE FUNCTION principal_revision_self() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END
$$;

CREATE TRIGGER principal_revision BEFORE UPDATE OF name, status ON principal
    FOR EACH ROW WHEN (OLD.name IS DISTINCT FROM NEW.name OR OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION principal_revision_self();

-- bump_principal_revision(column) is attached to a table that holds a member of
-- a principal's representation; the argument names the column that holds the
-- principal's id.
CREATE FUNCTION principal_revision_bump() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pid uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        pid := (to_jsonb(OLD) ->> TG_ARGV[0])::uuid;
    ELSE
        pid := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
    END IF;
    UPDATE principal SET revision = revision + 1 WHERE id = pid;
    RETURN NULL;
END
$$;

CREATE TRIGGER app_user_revision AFTER UPDATE ON app_user
    FOR EACH ROW WHEN (OLD.email IS DISTINCT FROM NEW.email OR OLD.display_name IS DISTINCT FROM NEW.display_name
                       OR OLD.disabled_reason IS DISTINCT FROM NEW.disabled_reason)
    EXECUTE FUNCTION principal_revision_bump('principal_id');

CREATE TRIGGER user_group_revision AFTER UPDATE ON user_group
    FOR EACH ROW WHEN (OLD.description IS DISTINCT FROM NEW.description OR OLD.auto_join IS DISTINCT FROM NEW.auto_join)
    EXECUTE FUNCTION principal_revision_bump('principal_id');

CREATE TRIGGER service_account_revision AFTER UPDATE ON service_account
    FOR EACH ROW WHEN (OLD.description IS DISTINCT FROM NEW.description)
    EXECUTE FUNCTION principal_revision_bump('principal_id');

CREATE TRIGGER user_identity_revision AFTER INSERT OR UPDATE OR DELETE ON user_identity
    FOR EACH ROW EXECUTE FUNCTION principal_revision_bump('user_id');
