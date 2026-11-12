-- trove:phase expand
--
-- The external flag of a user (R-AUTH-044, Q-AUTH-230) and the setting that new users take (R-AUTH-040). The runtime switch
-- of anonymous access is gone (R-AUTH-016, Q-AUTH-231): the visibility levels of 00073 open content instead.

ALTER TABLE app_user ADD COLUMN external boolean NOT NULL DEFAULT false;

ALTER TABLE auth_settings
    ADD COLUMN new_users_external boolean NOT NULL DEFAULT false,
    DROP COLUMN anonymous_access;

-- A change of the flag changes the user's representation, so it moves the principal's revision (the ETag of the user, R-API-012).
DROP TRIGGER app_user_revision ON app_user;
CREATE TRIGGER app_user_revision AFTER UPDATE ON app_user
    FOR EACH ROW WHEN (OLD.email IS DISTINCT FROM NEW.email OR OLD.display_name IS DISTINCT FROM NEW.display_name
                       OR OLD.disabled_reason IS DISTINCT FROM NEW.disabled_reason OR OLD.external IS DISTINCT FROM NEW.external)
    EXECUTE FUNCTION principal_revision_bump('principal_id');
