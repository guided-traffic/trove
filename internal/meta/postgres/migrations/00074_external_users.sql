-- trove:phase expand
--
-- The external flag of a user (R-AUTH-044, Q-AUTH-230) and the setting that new users take (R-AUTH-040). The runtime switch
-- of anonymous access is gone (R-AUTH-016, Q-AUTH-231): the visibility levels of 00073 open content instead.

ALTER TABLE app_user ADD COLUMN external boolean NOT NULL DEFAULT false;

ALTER TABLE auth_settings
    ADD COLUMN new_users_external boolean NOT NULL DEFAULT false,
    DROP COLUMN anonymous_access;
