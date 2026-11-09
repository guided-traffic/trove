-- The statements of the Debian format's remote class (R-DEB-019 to R-DEB-023, Q-DEB-35, Q-DEB-36, Q-DEB-41, Q-DEB-42): the stored
-- release documents with the verdict of their verification, the files they list, the parsed indexes and their entries. Every table is a
-- cache row: a restore without remote caches deletes them (R-BAK-008). Nullable times are read as a time and a flag, because the data
-- port may not import the driver's types (R-ARCH-002).

-- name: DebRemoteReleases :many
-- The stored documents of a release directory.
SELECT document, COALESCE(sha256, ''::bytea)::bytea AS sha256,
       (release_date IS NOT NULL)::boolean AS has_date, to_char(COALESCE(release_date, 'epoch'::timestamptz) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS release_date,
       COALESCE(by_hash, false)::boolean AS by_hash, COALESCE(verified_by, '')::text AS verified_by,
       (floor_date IS NOT NULL)::boolean AS has_floor, to_char(COALESCE(floor_date, 'epoch'::timestamptz) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS floor_date,
       to_char(stored_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS stored_at
  FROM deb_remote_releases
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text
 ORDER BY document;

-- name: DebRemoteReleasesOfDirs :many
-- The stored documents of several directories (the candidates of a path, the suites of a scope), 'absent' records included.
SELECT dir, document, COALESCE(sha256, ''::bytea)::bytea AS sha256,
       (release_date IS NOT NULL)::boolean AS has_date, to_char(COALESCE(release_date, 'epoch'::timestamptz) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS release_date,
       COALESCE(by_hash, false)::boolean AS by_hash, COALESCE(verified_by, '')::text AS verified_by,
       to_char(stored_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS stored_at
  FROM deb_remote_releases
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = ANY(sqlc.arg(dirs)::text[])
 ORDER BY dir, document;

-- name: DebRemoteLockDir :exec
-- Serializes the writers of one release directory: two fills of its release documents, or a fill and a re-verification.
SELECT pg_advisory_xact_lock(hashtextextended('deb_remote_dir:' || sqlc.arg(repo_id)::bigint::text || ':' || sqlc.arg(dir)::text, 0));

-- name: DebRemoteFloor :one
-- The newest Date of any release verified for the directory (Q-DEB-42), the epoch when none was.
SELECT to_char(COALESCE(max(floor_date), 'epoch'::timestamptz) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')::text AS floor_date, (max(floor_date) IS NOT NULL)::boolean AS has_floor
  FROM deb_remote_releases
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text;

-- name: DebRemoteUpsertRelease :exec
INSERT INTO deb_remote_releases (repo_id, dir, document, sha256, release_date, by_hash, verified_by, floor_date, stored_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(dir)::text, sqlc.arg(document)::text, sqlc.arg(sha256)::bytea, sqlc.arg(release_date)::text::timestamptz,
        sqlc.arg(by_hash)::boolean, NULLIF(sqlc.arg(verified_by)::text, ''),
        CASE WHEN sqlc.arg(has_floor)::boolean THEN sqlc.arg(floor_date)::text::timestamptz END, now())
ON CONFLICT (repo_id, dir, document) DO UPDATE
   SET sha256 = EXCLUDED.sha256, release_date = EXCLUDED.release_date, by_hash = EXCLUDED.by_hash,
       verified_by = EXCLUDED.verified_by, floor_date = EXCLUDED.floor_date, stored_at = now();

-- name: DebRemoteRaiseFloor :exec
-- Raises the floor of every document row of the directory; the floor only rises.
UPDATE deb_remote_releases SET floor_date = sqlc.arg(floor_date)::text::timestamptz
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND document <> 'absent'
   AND (floor_date IS NULL OR floor_date < sqlc.arg(floor_date)::text::timestamptz);

-- name: DebRemoteUpsertAbsent :exec
-- The upstream has no such suite (Q-DEB-41).
INSERT INTO deb_remote_releases (repo_id, dir, document, stored_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(dir)::text, 'absent', now())
ON CONFLICT (repo_id, dir, document) DO UPDATE SET stored_at = now();

-- name: DebRemoteDeleteAbsent :exec
DELETE FROM deb_remote_releases WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND document = 'absent';

-- name: DebRemoteClearVerifiedBy :many
-- A change of the keys removed fingerprints: the releases they verified are unverified until a key of the new set verifies them
-- (R-DEB-020, Q-DEB-42). floor_date stays.
UPDATE deb_remote_releases SET verified_by = NULL
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND verified_by = ANY(sqlc.arg(fingerprints)::text[])
RETURNING dir, document;

-- name: DebRemoteUnverified :many
-- The stored documents that no key verifies now, for the re-verification of a remote that has keys.
SELECT dir, document
  FROM deb_remote_releases
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND document <> 'absent' AND verified_by IS NULL
 ORDER BY dir, document;

-- name: DebRemoteVerify :execrows
UPDATE deb_remote_releases SET verified_by = sqlc.arg(verified_by)::text
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND document = sqlc.arg(document)::text;

-- name: DebRemoteSupersedeFiles :exec
-- The files of the release that is replaced stay for deb.by_hash_keep (R-DEB-021).
UPDATE deb_remote_files SET current = false, superseded_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND current;

-- name: DebRemoteInsertFiles :exec
INSERT INTO deb_remote_files (repo_id, dir, path, sha256, size, current, superseded_at)
SELECT sqlc.arg(repo_id)::bigint, sqlc.arg(dir)::text, f.p, f.s, f.z, true, NULL
  FROM (SELECT unnest(sqlc.arg(paths)::text[]) AS p, unnest(sqlc.arg(sha256s)::bytea[]) AS s, unnest(sqlc.arg(sizes)::bigint[]) AS z) AS f
ON CONFLICT (repo_id, dir, path, sha256) DO UPDATE SET current = true, superseded_at = NULL, size = EXCLUDED.size;

-- name: DebRemoteFileOf :one
-- The SHA-256 and size the current release of the directory lists for a path; no row when it lists none.
SELECT sha256, size FROM deb_remote_files
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND path = sqlc.arg(path)::text AND current;

-- name: DebRemoteFilesOfDir :many
SELECT path, sha256, size FROM deb_remote_files
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND current
 ORDER BY path;

-- name: DebRemoteHasFiles :one
SELECT EXISTS (SELECT 1 FROM deb_remote_files WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND current)::boolean;

-- name: DebRemoteIndexRows :many
-- The parsed indexes of a directory.
SELECT path, sha256, entries, skipped FROM deb_remote_indexes
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text
 ORDER BY path;

-- name: DebRemoteIndexesOfDirs :many
SELECT dir, path, sha256 FROM deb_remote_indexes
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = ANY(sqlc.arg(dirs)::text[]);

-- name: DebRemoteIndexSha :one
SELECT sha256 FROM deb_remote_indexes
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND path = sqlc.arg(path)::text;

-- name: DebRemoteUpsertIndex :exec
INSERT INTO deb_remote_indexes (repo_id, dir, path, sha256, entries, skipped, parsed_at)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(dir)::text, sqlc.arg(path)::text, sqlc.arg(sha256)::bytea, sqlc.arg(entries)::integer,
        sqlc.arg(skipped)::integer, now())
ON CONFLICT (repo_id, dir, path) DO UPDATE
   SET sha256 = EXCLUDED.sha256, entries = EXCLUDED.entries, skipped = EXCLUDED.skipped, parsed_at = now();

-- name: DebRemoteDeleteEntries :exec
DELETE FROM deb_remote_entries WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND index_path = sqlc.arg(path)::text;

-- name: DebRemoteInsertEntries :exec
INSERT INTO deb_remote_entries (repo_id, dir, index_path, package, version, architecture, path, sha256, size)
SELECT sqlc.arg(repo_id)::bigint, sqlc.arg(dir)::text, sqlc.arg(index_path)::text, e.pk, e.v, e.a, e.p, e.s, e.z
  FROM (SELECT unnest(sqlc.arg(packages)::text[]) AS pk, unnest(sqlc.arg(versions)::text[]) AS v,
               unnest(sqlc.arg(architectures)::text[]) AS a, unnest(sqlc.arg(paths)::text[]) AS p,
               unnest(sqlc.arg(sha256s)::bytea[]) AS s, unnest(sqlc.arg(sizes)::bigint[]) AS z) AS e;

-- name: DebRemoteDeleteIndex :exec
DELETE FROM deb_remote_indexes WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = sqlc.arg(dir)::text AND path = sqlc.arg(path)::text;

-- name: DebRemoteEntryDigest :many
-- What the newest parsed index lists for a path (R-DEB-022): the same ordering as the serve-facts kind deb_remote.
SELECT e.sha256, e.size, e.dir, e.index_path, e.package, e.version, e.architecture
  FROM deb_remote_entries e
  JOIN deb_remote_indexes x ON x.repo_id = e.repo_id AND x.dir = e.dir AND x.path = e.index_path
 WHERE e.repo_id = sqlc.arg(repo_id)::bigint AND e.path = sqlc.arg(path)::text
 ORDER BY x.parsed_at DESC, x.dir, x.path
 LIMIT 1;

-- name: DebRemoteNameLocations :many
-- The indexes that list a package or source name.
SELECT DISTINCT dir, index_path FROM deb_remote_entries
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND package = sqlc.arg(name)::text;

-- name: DebRemoteFilesOfDirs :many
-- The current files of several directories, for the settled-ness of a scope: which indexes the releases list.
SELECT dir, path, sha256 FROM deb_remote_files
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND dir = ANY(sqlc.arg(dirs)::text[]) AND current
 ORDER BY dir, path;

-- name: DebRemotePruneFiles :execrows
-- The files of releases that were replaced longer ago than deb.by_hash_keep go.
DELETE FROM deb_remote_files
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND NOT current AND superseded_at < sqlc.arg(before)::text::timestamptz;

-- name: DebRemoteOrphanByHash :many
-- By-hash files of the repository that no release it still remembers names, older than the grace (R-DEB-021, Q-DEB-33).
SELECT i.path
  FROM items i
 WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.kind = 'file' AND i.path LIKE 'dists/%/by-hash/SHA256/%'
   AND i.path > sqlc.arg(after)::text AND i.created_at < sqlc.arg(before)::text::timestamptz
   AND NOT EXISTS (SELECT 1 FROM deb_remote_files f
                    WHERE f.repo_id = i.repo_id AND f.sha256 = decode(substring(i.path FROM '[^/]+$'), 'hex'))
 ORDER BY i.path
 LIMIT sqlc.arg(batch)::integer;

-- name: DebRemoteHousekeepingDue :many
-- Remote repositories with replaced releases whose files are past deb.by_hash_keep, for the periodic task.
SELECT DISTINCT repo_id FROM deb_remote_files WHERE NOT current AND superseded_at < sqlc.arg(before)::text::timestamptz;

-- name: DebRemoteRepoMeta :one
-- The class, format, key and configuration document of a repository, for the jobs.
SELECT r.class::text AS class, r.format::text AS format, r.config AS config, r.key::text AS key, p.key::text AS project
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint;

-- name: DebRemotePurgeEntries :exec
-- A restore without remote caches deletes the rows of the remote (R-BAK-008): the entries first.
DELETE FROM deb_remote_entries WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebRemotePurgeIndexes :exec
DELETE FROM deb_remote_indexes WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebRemotePurgeFiles :exec
DELETE FROM deb_remote_files WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebRemotePurgeReleases :exec
DELETE FROM deb_remote_releases WHERE repo_id = sqlc.arg(repo_id)::bigint;
