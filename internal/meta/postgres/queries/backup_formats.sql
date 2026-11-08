-- The statements of the formats' parts of backups and restores (internal/formatdata/npm and internal/formatdata/nuget;
-- R-NPM-047, R-NUGET-033, R-BAK-008): the journal replay of unpublishes, publishes and deletes, `trove admin npm
-- reinstate` and the purge of remote caches. The packages are domain packages and import no driver types, so a time
-- travels as text and is cast to timestamptz here, a principal id as text cast to uuid.

-- name: BackupNPMReplayRepository :many
-- The npm local repository a journal entry names. An entry about any other repository (a forged one included) finds
-- nothing and is "subject unknown".
SELECT r.id, (p.key || '/' || r.key)::text AS ref
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint AND r.format = 'npm' AND r.class = 'local';

-- name: BackupNPMStoredVersion :many
-- The stored (not trashed) version of a package with the integrity its npm row holds. A replay withholds a version only
-- while it is stored with the integrity the entry names; reinstate brings back only a version stored with the
-- integrity of the tombstone.
SELECT pv.id AS version_id, nv.integrity
  FROM package_versions pv
  JOIN npm_versions nv ON nv.version_id = pv.id
 WHERE pv.package_id = sqlc.arg(package_id)::bigint
   AND pv.version = sqlc.arg(version)::text
   AND pv.trashed_at IS NULL;

-- name: BackupNPMInsertJournalTombstone :execrows
-- The tombstone of a version a replay withholds (R-NPM-047): source journal, the time and the actor of the unpublish the
-- entry records, and the entry's ID. A tombstone that exists is kept as it is.
INSERT INTO npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by, journal_entry)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(version)::text, sqlc.arg(integrity)::text, 'journal',
        sqlc.arg(at)::text::timestamptz, sqlc.arg(by)::text::uuid, sqlc.arg(journal_entry)::text::uuid)
ON CONFLICT (repo_id, name, version) DO NOTHING;

-- name: BackupNPMTombstone :many
-- The tombstone of a version with its source and, for a journal tombstone, the entry that withheld it.
SELECT integrity, source, COALESCE(journal_entry::text, '')::text AS journal_entry
  FROM npm_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text;

-- name: BackupNPMDeleteJournalTombstone :execrows
-- Removes the tombstone of a withheld version (R-NPM-047, Q-NPM-8). Only a journal tombstone goes, and only with the
-- integrity the caller saw on the stored version: the version's own bytes come back, never new ones.
DELETE FROM npm_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND version = sqlc.arg(version)::text
   AND source = 'journal' AND integrity = sqlc.arg(integrity)::text;

-- name: BackupRepositoryByRef :many
-- A repository by its project key and its key, for the commands that name one (`trove admin npm reinstate`).
SELECT r.id, r.format, r.class
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE p.key = sqlc.arg(project_key)::text AND r.key = sqlc.arg(repo_key)::text;

-- name: BackupRemoteRepositories :many
-- The remote repositories of a format with the names a storage hook dispatches on (R-BAK-008).
SELECT r.id, p.key AS project_key, r.key
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.format = sqlc.arg(format)::text AND r.class = 'remote'
 ORDER BY r.id;

-- name: BackupDeleteCacheEntries :execrows
-- The cache entries of one remote repository (R-BAK-008): the stored documents and the artifact entries whose items the
-- storage delete path removed. The blob references they held are released by the trigger of the table.
DELETE FROM remote_cache_entries WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: BackupNPMDeleteRemotePackages :execrows
-- The npm index rows of one remote repository's stored documents; the version rows cascade (R-NPM-047).
DELETE FROM npm_remote_packages WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: BackupNuGetReplayRepository :many
-- The NuGet local repository a journal entry names (R-NUGET-033).
SELECT r.id, (p.key || '/' || r.key)::text AS ref
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint AND r.format = 'nuget' AND r.class = 'local';

-- name: BackupNuGetInsertTombstone :execrows
-- A tombstone a replay writes: 'reserved' for a version published after the snapshot (the journaled bytes are admitted
-- again), 'removed' for one deleted after it. By is the system principal of the restore (the nil UUID).
INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
VALUES (sqlc.arg(repository_id)::bigint, sqlc.arg(lower_id)::text, sqlc.arg(lower_version)::text, sqlc.arg(sha512)::bytea,
        sqlc.arg(at)::text::timestamptz, sqlc.arg(by)::text::uuid, sqlc.arg(kind)::text)
ON CONFLICT (repository_id, lower_id, lower_version) DO NOTHING;

-- name: BackupNuGetPromoteReserved :execrows
-- A reserved number that a replayed entry turns into a removal: a version removed after the snapshot (the deleted bytes
-- stay refused), or an addition that names other bytes than the reservation (R-NUGET-033, Q-NUGET-112).
UPDATE nuget_tombstone
   SET kind = 'removed', deleted_at = sqlc.arg(at)::text::timestamptz, deleted_by = sqlc.arg(by)::text::uuid
 WHERE repository_id = sqlc.arg(repository_id)::bigint
   AND lower_id = sqlc.arg(lower_id)::text
   AND lower_version = sqlc.arg(lower_version)::text
   AND kind = 'reserved';
