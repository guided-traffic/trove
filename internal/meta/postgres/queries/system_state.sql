-- name: SystemStateRead :many
-- One key of system_state as JSON text; no row means the key was never written. Used by the banner and the maintenance mode
-- (R-OPS-024, R-OPS-025).
SELECT value::text AS value FROM system_state WHERE key = sqlc.arg(key)::text;

-- name: SystemStateWrite :exec
-- Writes one key of system_state with the actor that wrote it ('' for an actor without an ID).
INSERT INTO system_state (key, value, updated_by) VALUES (sqlc.arg(key)::text, sqlc.arg(value)::text::jsonb, NULLIF(sqlc.arg(updated_by)::text, '')::uuid)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by;
