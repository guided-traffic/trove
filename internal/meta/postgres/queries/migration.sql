-- name: ServerInfo :one
SELECT current_setting('server_version_num')::int AS version_num,
       current_setting('server_version')::text    AS version,
       current_setting('server_encoding')::text   AS encoding,
       current_user::text                         AS role_name,
       coalesce((SELECT r.rolsuper FROM pg_catalog.pg_roles r WHERE r.rolname = current_user), false)::boolean AS is_superuser,
       current_setting('max_connections')::int    AS max_connections,
       current_setting('superuser_reserved_connections')::int AS superuser_reserved;

-- name: SessionSettings :many
SELECT name::text, setting::text
  FROM pg_catalog.pg_settings
 WHERE name IN ('statement_timeout', 'lock_timeout', 'idle_in_transaction_session_timeout', 'search_path');

-- name: SchemaOwnedBy :one
SELECT pg_get_userbyid(nspowner)::text AS owner
  FROM pg_catalog.pg_namespace
 WHERE nspname = $1;

-- name: SchemaComment :one
SELECT coalesce(obj_description(to_regnamespace($1::text)::oid, 'pg_namespace'), '')::text AS comment;

-- name: TableExists :one
SELECT (to_regclass($1::text) IS NOT NULL)::boolean AS found;

-- name: GetSchemaState :one
SELECT applied, compat_floor FROM schema_state;

-- name: UpsertSchemaState :exec
INSERT INTO schema_state (id, applied, compat_floor)
VALUES (true, $1, $2)
ON CONFLICT (id) DO UPDATE SET applied = EXCLUDED.applied, compat_floor = EXCLUDED.compat_floor;

-- name: ListAppliedSteps :many
SELECT version, name, class, release, requires_app, transactional, checksum, applied_at, duration_ms
  FROM schema_migration
 ORDER BY version;

-- name: InsertAppliedStep :exec
INSERT INTO schema_migration (version, name, class, release, requires_app, transactional, checksum, duration_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: LiveReplicas :many
SELECT name, version, roles, heartbeat_at
  FROM replica
 WHERE heartbeat_at > now() - make_interval(secs => $1::double precision)
 ORDER BY name;

-- name: MigrationLockHolders :many
SELECT a.application_name::text AS application_name, l.mode::text AS mode
  FROM pg_catalog.pg_locks l
  JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
 WHERE l.locktype = 'advisory' AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d WHERE d.datname = current_database())
   AND l.classid = 3 AND l.objid = 0 AND l.objsubid = 2 AND l.granted
 ORDER BY a.application_name;

-- name: InvalidIndexes :many
SELECT c.relname::text AS name
  FROM pg_catalog.pg_index i
  JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND NOT i.indisvalid;

-- name: SchemaOwnerAccessible :one
SELECT pg_catalog.pg_has_role(current_user, nspowner, 'USAGE')::boolean AS accessible
  FROM pg_catalog.pg_namespace
 WHERE nspname = $1;

-- name: WritableBookkeeping :many
-- The bookkeeping tables of the schema that the current role can change without
-- owning them (neither owner nor member of the owning role).
SELECT c.relname::text AS name
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1
   AND c.relname IN ('schema_migration', 'schema_state')
   AND NOT pg_catalog.pg_has_role(current_user, c.relowner, 'USAGE')
   AND (   pg_catalog.has_table_privilege(c.oid, 'INSERT')
        OR pg_catalog.has_table_privilege(c.oid, 'UPDATE')
        OR pg_catalog.has_table_privilege(c.oid, 'DELETE')
        OR pg_catalog.has_table_privilege(c.oid, 'TRUNCATE'))
 ORDER BY c.relname;
