-- name: SchemaTables :many
SELECT c.relname::text AS name
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND NOT c.relispartition
 ORDER BY c.relname;

-- name: SchemaForeignKeys :many
SELECT cl.relname::text  AS table_name,
       att.attname::text AS column_name,
       rcl.relname::text AS ref_table
  FROM pg_catalog.pg_constraint c
  JOIN pg_catalog.pg_class cl ON cl.oid = c.conrelid
  JOIN pg_catalog.pg_class rcl ON rcl.oid = c.confrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = cl.relnamespace
  CROSS JOIN LATERAL unnest(c.conkey) AS k(attnum)
  JOIN pg_catalog.pg_attribute att ON att.attrelid = c.conrelid AND att.attnum = k.attnum
 WHERE c.contype = 'f' AND n.nspname = $1
 ORDER BY cl.relname, att.attname;

-- name: SchemaBigintColumns :many
SELECT c.relname::text AS table_name, a.attname::text AS column_name
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
 WHERE n.nspname = $1
   AND c.relkind IN ('r', 'p')
   AND NOT c.relispartition
   AND a.attnum > 0
   AND NOT a.attisdropped
   AND a.atttypid = 20::oid
 ORDER BY c.relname, a.attnum;
