-- The statements of the replay of a download block (R-HA-054, R-VULN-013, R-VULN-026, Q-VULN-114, Q-VULN-115, Q-VULN-131):
-- internal/vuln/triage/journal_replay.go.

-- name: JournalVulnFindingLock :many
-- The finding a replayed block names by its ID, locked for the change: its immutable identity (repository, ecosystem, package,
-- version) and the shape it has (Q-VULN-115, Q-VULN-131 step 1).
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.ecosystem::text AS ecosystem, f.package_name::text AS package_name,
       f.version::text AS version, f.advisory_id::text AS advisory_id, f.status::text AS status,
       COALESCE(f.status_before_resolution, '')::text AS status_before_resolution,
       COALESCE(f.resolved_reason, '')::text AS resolved_reason, COALESCE(f.version_id, 0)::bigint AS version_id,
       COALESCE(f.package_id, 0)::bigint AS package_id
  FROM vuln_finding f
 WHERE f.id = sqlc.arg(id)::text::uuid
   FOR UPDATE;

-- name: JournalVulnFindingLockByIdentity :many
-- The finding of the identity the table's unique key names (repository, ecosystem, package, version, advisory), locked for the
-- change (Q-VULN-131 step 2).
SELECT f.id::text AS id, f.repo_id::bigint AS repo_id, f.ecosystem::text AS ecosystem, f.package_name::text AS package_name,
       f.version::text AS version, f.status::text AS status, COALESCE(f.status_before_resolution, '')::text AS status_before_resolution,
       COALESCE(f.resolved_reason, '')::text AS resolved_reason, COALESCE(f.version_id, 0)::bigint AS version_id,
       COALESCE(f.package_id, 0)::bigint AS package_id
  FROM vuln_finding f
 WHERE f.repo_id = sqlc.arg(repo_id)::bigint AND f.ecosystem = sqlc.arg(ecosystem)::text AND f.package_name = sqlc.arg(package_name)::text
   AND f.version = sqlc.arg(version)::text AND f.advisory_id = sqlc.arg(advisory_id)::text
   FOR UPDATE;

-- name: JournalVulnRepository :many
-- The repository an entry names, to tell one that can hold the finding from one that cannot (Q-VULN-131 step 3). No row: the
-- restored database does not hold it.
SELECT r.class::text AS class, r.lifecycle::text AS lifecycle, r.format::text AS format
  FROM repositories r
 WHERE r.id = sqlc.arg(id)::bigint;

-- name: JournalVulnStoredVersion :many
-- The package version a repository stores under the name and version of an entry (Q-VULN-131 step 4): the row the download joins
-- a finding to. No row: the version is not stored (it was evicted, or never was).
SELECT pv.id::bigint AS version_id, pv.package_id::bigint AS package_id
  FROM packages p
  JOIN package_versions pv ON pv.package_id = p.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(package_name)::text AND pv.version = sqlc.arg(version)::text
   AND pv.trashed_at IS NULL
 LIMIT 1;

-- name: JournalVulnFindingInsert :exec
-- A finding the restored database lacks, in the shape both download gates read (Q-VULN-131 steps 3 and 4): version_id 0 is a
-- version the repository does not store, whose finding is resolved while blocked. The other advisory members stay empty until the
-- first evaluation with complete data refreshes them.
INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, package_id, version_id, kind, severity, severity_rank,
                          fix_available, status, status_before_resolution, resolved_reason, resolved_at, advisory_modified)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(repo_id)::bigint, sqlc.arg(ecosystem)::text, sqlc.arg(package_name)::text, sqlc.arg(version)::text,
        sqlc.arg(advisory_id)::text, NULLIF(sqlc.arg(package_id)::bigint, 0), NULLIF(sqlc.arg(version_id)::bigint, 0), sqlc.arg(kind)::text,
        sqlc.arg(severity)::text, sqlc.arg(severity_rank)::smallint, false,
        CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN 'blocked' ELSE 'resolved' END,
        CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE 'blocked' END,
        CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE 'version_removed' END,
        CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE now() END,
        now());

-- name: JournalVulnFindingBlock :one
-- Sets the download block again in the shape both gates read (Q-VULN-114, Q-VULN-131 step 4): a stored version (version_id not 0)
-- makes the finding blocked and linked to it; a version that is not stored makes it resolved with version_removed while blocked and
-- unlinked. Either way the dismissal reason is cleared as for every status a finding leaves, the comment kept, the revision raised,
-- and no principal recorded as the one who changed it.
UPDATE vuln_finding
   SET status = CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN 'blocked' ELSE 'resolved' END,
       status_before_resolution = CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE 'blocked' END,
       resolved_reason = CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE 'version_removed' END,
       resolved_at = CASE WHEN sqlc.arg(version_id)::bigint <> 0 THEN NULL ELSE now() END,
       dismissal_reason = NULL,
       package_id = NULLIF(sqlc.arg(package_id)::bigint, 0), version_id = NULLIF(sqlc.arg(version_id)::bigint, 0),
       revision = revision + 1, status_changed_at = now(), status_changed_by = NULL
 WHERE id = sqlc.arg(id)::text::uuid
RETURNING revision;

-- name: JournalVulnFindingRaise :execrows
-- Raises the kind and the severity of a finding that a replay created to the stronger of its own and the entry's (Q-VULN-131): every
-- replayed copy can only make such a finding block more, so an earlier forged copy that named weaker facts is corrected by a later
-- genuine one. A finding restored from the backup is never raised: its facts are the evaluation's, and only a replay's creation (the
-- history row `detected` that names a journal entry, newer than the last restore's `restored_at`) marks the finding as the entries' own, so a finding an earlier restore created and the evaluation has owned since is not raised. Nothing changes, and no row counts, when
-- the finding is as strong already.
UPDATE vuln_finding f
   SET kind = CASE WHEN sqlc.arg(kind)::text = 'malicious' THEN 'malicious' ELSE kind END,
       severity = CASE WHEN sqlc.arg(severity_rank)::smallint > severity_rank THEN sqlc.arg(severity)::text ELSE severity END,
       severity_rank = GREATEST(severity_rank, sqlc.arg(severity_rank)::smallint),
       revision = revision + 1
 WHERE f.id = sqlc.arg(id)::text::uuid
   AND ((sqlc.arg(kind)::text = 'malicious' AND kind <> 'malicious') OR sqlc.arg(severity_rank)::smallint > severity_rank)
   AND EXISTS (SELECT 1 FROM vuln_finding_event e
                WHERE e.finding_id = f.id AND e.change = 'detected' AND e.details ? 'journal'
                  AND e.at > COALESCE((SELECT (v.value #>> '{}')::timestamptz FROM system_state v WHERE v.key = 'restored_at'), '-infinity'::timestamptz));
