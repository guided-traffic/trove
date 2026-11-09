-- name: MaintenanceDeletionsState :one
-- The deletion pause of R-HA-025: when it was set and why; both empty while it is not set.
SELECT COALESCE((SELECT value #>> '{}' FROM system_state WHERE key = 'deletions_paused_at' AND value <> 'null'::jsonb), '')::text AS paused_at,
       COALESCE((SELECT value #>> '{}' FROM system_state WHERE key = 'deletions_paused_reason' AND value <> 'null'::jsonb), '')::text AS reason;

-- name: MaintenancePauseDeletions :execrows
-- Sets the deletion pause by hand (R-HA-025, trove admin maintenance pause-deletions). A pause that is already set keeps its time
-- and its reason; the count is 0 then.
INSERT INTO system_state (key, value) VALUES
    ('deletions_paused_at', to_jsonb(to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))),
    ('deletions_paused_reason', to_jsonb(sqlc.arg(reason)::text))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
 WHERE system_state.value = 'null'::jsonb;

-- name: MaintenanceResumeDeletions :execrows
-- Clears the deletion pause (R-HA-025 step 7, trove admin maintenance resume-deletions); the count is 0 when it was not set.
UPDATE system_state SET value = 'null'::jsonb, updated_at = now()
 WHERE key IN ('deletions_paused_at', 'deletions_paused_reason') AND value <> 'null'::jsonb;
