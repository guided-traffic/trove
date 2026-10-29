-- name: GetSystemState :one
SELECT value, updated_at, updated_by
  FROM system_state
 WHERE key = $1;

-- name: UpsertSystemState :exec
INSERT INTO system_state (key, value, updated_by)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE
   SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by;

-- name: DeleteSystemState :execrows
DELETE FROM system_state WHERE key = $1;
