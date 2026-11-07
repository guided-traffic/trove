-- The statements of the replay of a download block (R-HA-054, R-VULN-013, H-489): internal/vuln/triage/journal_replay.go.

-- name: JournalVulnFindingLock :many
-- The finding a replayed block names, locked for the change: its immutable identity (ecosystem, package, version) and its
-- status (Q-VULN-115).
SELECT f.id::text AS id, f.ecosystem::text AS ecosystem, f.package_name::text AS package_name, f.version::text AS version,
       f.status::text AS status
  FROM vuln_finding f
 WHERE f.id = sqlc.arg(id)::text::uuid
   FOR UPDATE;

-- name: JournalVulnFindingBlock :one
-- Sets the download block again (Q-VULN-114): status blocked from whatever the restored finding has, the resolution and the
-- dismissal reason cleared as for every status a finding leaves, the comment kept, the revision raised, and no principal
-- recorded as the one who changed it.
UPDATE vuln_finding
   SET status = 'blocked', status_before_resolution = NULL, resolved_reason = NULL, resolved_at = NULL, dismissal_reason = NULL,
       revision = revision + 1, status_changed_at = now(), status_changed_by = NULL
 WHERE id = sqlc.arg(id)::text::uuid
RETURNING revision;
