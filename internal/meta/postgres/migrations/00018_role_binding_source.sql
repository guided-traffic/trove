-- trove:phase expand
--
-- Role bindings declared in the system configuration (R-AUTHZ-010, Q-AUTHZ-170).
-- source says who keeps a binding: 'api' for one made through the management API
-- or an administration command, 'configuration' for one that trove migrate applied
-- from authz.role_bindings. trove migrate is the only writer of 'configuration':
-- it inserts a declared binding or turns an existing one to 'configuration', and
-- removes a 'configuration' binding that is no longer declared. The API refuses to
-- remove one (409 role_binding_managed_by_configuration). A binary of the
-- previous release inserts without the column and so writes 'api', which is what
-- it means. The grant index does not read the column: the rights of a binding do
-- not depend on who keeps it.

ALTER TABLE authz_role_binding
    ADD COLUMN source text NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'configuration'));
