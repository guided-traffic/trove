-- trove:phase expand
--
-- The runtime role reads the migration bookkeeping and never writes it. The
-- default privileges of the first migration give it INSERT, UPDATE and DELETE on
-- every later table, including schema_migration and schema_state, which are
-- created after them; with write access a SQL injection in the application could
-- raise schema_state.applied or lower compat_floor and so switch off the
-- compatibility gate of R-HA-018, or rewrite the checksums the runner verifies.
-- Only the migration role (the owner) writes them.
--
-- system_state stays writable: the runtime sets the deletion pause
-- (deletions_paused_at, R-HA-025) and later keys of its own. In single-role mode
-- the role does not exist and nothing is revoked.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
        EXECUTE format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %I.schema_migration, %I.schema_state FROM trove_app',
                       current_schema(), current_schema());
    END IF;
END $$;
