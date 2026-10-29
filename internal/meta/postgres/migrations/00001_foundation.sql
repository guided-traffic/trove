-- trove:phase expand
--
-- Foundation: the default privileges of the two-role setup (R-HA-014), the
-- migration bookkeeping tables (R-HA-018) and the key-value table system_state
-- (R-OPS-024) with the three keys that every installation has before its first
-- replica starts: installation_id, keyspace_id and incarnation.

-- Every later table of this schema is reachable for the runtime role without
-- further grants. In single-role mode the role does not exist and nothing is
-- granted. The statements run in the schema the runner put on search_path.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO trove_app', current_schema());
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO trove_app', current_schema());
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO trove_app', current_schema());
    END IF;
END $$;

CREATE TABLE schema_migration (
    version        integer     PRIMARY KEY,
    name           text        NOT NULL,
    class          text        NOT NULL CHECK (class IN ('expand', 'contract')),
    release        text        NOT NULL,
    requires_app   text,
    transactional  boolean     NOT NULL,
    checksum       bytea       NOT NULL,
    applied_at     timestamptz NOT NULL DEFAULT now(),
    duration_ms    integer     NOT NULL,
    CHECK ((class = 'contract') = (requires_app IS NOT NULL))
);

CREATE TABLE schema_state (
    id             boolean     PRIMARY KEY DEFAULT true CHECK (id),
    applied        integer     NOT NULL,
    compat_floor   text        NOT NULL
);

CREATE TABLE system_state (
    key         text        PRIMARY KEY,
    value       jsonb       NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid
);

INSERT INTO system_state (key, value) VALUES
    ('installation_id', to_jsonb(gen_random_uuid()::text)),
    ('keyspace_id',     to_jsonb(gen_random_uuid()::text)),
    ('incarnation',     to_jsonb(gen_random_uuid()::text));
