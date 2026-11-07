-- Queries of the create work package of the backup lane (R-BAK-004 to R-BAK-007, R-BAK-009, R-BAK-015). The run record and
-- the hold check are in backup.sql; this file holds what the snapshot reads from the catalog inside the snapshot, the pins
-- the copy phase pages through, the blob row the damage check reads, and the grant check of the preflight.

-- name: BackupCatalogTables :many
-- The tables the export covers (R-BAK-004): ordinary and partitioned tables of the schema, partitions through their parent.
-- estimated_bytes orders the export, largest first; a partitioned table counts its leaf partitions.
SELECT c.relname::text AS name,
       (CASE WHEN c.relkind = 'p'
             THEN coalesce((SELECT sum(pg_total_relation_size(pt.relid)) FROM pg_catalog.pg_partition_tree(c.oid) pt WHERE pt.isleaf), 0)
             ELSE pg_total_relation_size(c.oid)
        END)::bigint AS estimated_bytes
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = sqlc.arg(schema)::text AND c.relkind IN ('r', 'p') AND NOT c.relispartition
 ORDER BY c.relname;

-- name: BackupTableColumns :many
-- The exported column list of every table (R-BAK-004): generated columns are left out because COPY FROM cannot write them,
-- identity columns are in. A partitioned table's columns are its parent's.
SELECT c.relname::text AS table_name, a.attname::text AS column_name, pg_catalog.format_type(a.atttypid, a.atttypmod)::text AS column_type
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname = sqlc.arg(schema)::text AND c.relkind IN ('r', 'p') AND NOT c.relispartition
   AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
 ORDER BY c.relname, a.attnum;

-- name: BackupExportSnapshot :one
-- The first statement of the snapshot transaction after the table locks (R-BAK-005 step 2): it fixes the snapshot.
SELECT pg_catalog.pg_export_snapshot()::text AS snapshot_id, clock_timestamp()::timestamptz AS at, current_setting('server_version')::text AS server_version;

-- name: BackupSequences :many
-- last_value and the start of every sequence of the schema (R-BAK-005 step 6). last_value is NULL for a sequence nobody
-- called, which the manifest records as is_called false at the start value.
SELECT sequencename::text AS name, last_value, start_value
  FROM pg_catalog.pg_sequences
 WHERE schemaname = sqlc.arg(schema)::text
 ORDER BY sequencename;

-- name: ListBackupPins :many
-- The to-copy set of a run in digest order after a position, for the copy phase (R-BAK-006): the set is never held in
-- memory, so a first backup of millions of blobs stays bounded.
SELECT sha256, size FROM backup_blob_pin
 WHERE run_id = sqlc.arg(run_id)::text::uuid AND sha256 > sqlc.arg(after)::bytea
 ORDER BY sha256
 LIMIT sqlc.arg(row_limit)::int;

-- name: BackupBlobRow :many
-- The state of a blob row at READ COMMITTED, the check that tells source damage from a lost hold (R-BAK-006).
SELECT state, content_status FROM blobs WHERE sha256 = sqlc.arg(sha256)::bytea;

-- name: BackupTablesWithoutSelect :many
-- The tables and partitions of the schema the current role cannot read (R-BAK-015): the export locks and reads every one.
SELECT c.relname::text AS name
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = sqlc.arg(schema)::text AND c.relkind IN ('r', 'p')
   AND NOT pg_catalog.has_table_privilege(c.oid, 'SELECT')
 ORDER BY c.relname;

-- name: BackupSequencesWithoutSelect :many
-- Read through pg_sequences, not pg_class: the planner may test the privilege before the relkind filter, and
-- has_sequence_privilege fails on a table.
SELECT s.sequencename::text AS name
  FROM pg_catalog.pg_sequences s
 WHERE s.schemaname = sqlc.arg(schema)::text
   AND NOT pg_catalog.has_sequence_privilege((quote_ident(s.schemaname) || '.' || quote_ident(s.sequencename))::text, 'SELECT')
 ORDER BY s.sequencename;

-- name: BackupMissingPrivileges :many
-- For each "table:privilege" pair the current role lacks: the writes of the run record and the audit insert (R-BAK-015).
-- A table that does not exist counts as missing.
SELECT p::text AS missing
  FROM unnest(sqlc.arg(pairs)::text[]) AS p
 WHERE NOT coalesce(pg_catalog.has_table_privilege(pg_catalog.to_regclass(split_part(p, ':', 1)), split_part(p, ':', 2)), false);

-- name: RecordBackupEvent :exec
-- The audit event of the backup lane (R-BAK-014) through the function that writes the four backup events and nothing else
-- (Q-BAK-50); the backup login has EXECUTE on it and no INSERT on audit_event.
SELECT backup_record_event(sqlc.arg(action)::text, sqlc.arg(outcome)::text, sqlc.arg(replica)::text, sqlc.arg(reason)::text, sqlc.arg(details)::text::jsonb);

-- name: BackupCanRecordEvents :one
SELECT coalesce(pg_catalog.has_function_privilege(pg_catalog.to_regprocedure('backup_record_event(text,text,text,text,jsonb)'), 'EXECUTE'), false)::boolean AS ok;

-- name: BackupSchemaApplied :one
SELECT applied FROM schema_state;
