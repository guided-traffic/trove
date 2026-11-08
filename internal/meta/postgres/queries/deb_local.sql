-- The statements of the Debian format's local class (R-DEB-006, R-DEB-008, R-DEB-009, R-DEB-013, R-DEB-030, R-DEB-032). The signing keys
-- are queries/deb_keys.sql, the index builds queries/deb_build.sql.

-- name: DebEnsureSuite :execrows
-- A suite exists from the first coordinate that names it; an existing one is left as it is. A new suite starts with change_seq 1, so it
-- needs no raise; the count says whether this call created it.
INSERT INTO deb_suites (repo_id, suite)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(suite)::text)
ON CONFLICT (repo_id, suite) DO NOTHING;

-- name: DebRaiseSuites :many
-- Raises change_seq of the suites, so the next build reflects the change (R-DEB-008); returns the numbers a client waits on.
UPDATE deb_suites SET change_seq = change_seq + 1
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = ANY(sqlc.arg(suites)::text[])
RETURNING suite, change_seq, built_seq;

-- name: DebSuiteSeqs :many
SELECT suite, change_seq, built_seq
  FROM deb_suites
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = ANY(sqlc.arg(suites)::text[])
 ORDER BY suite;

-- name: DebInsertBinary :execrows
-- The row of a package file; the file that holds the item already keeps its row.
INSERT INTO deb_binaries (item_id, repo_id, package, version, architecture, source, control)
VALUES (sqlc.arg(item_id)::bigint, sqlc.arg(repo_id)::bigint, sqlc.arg(package)::text, sqlc.arg(version)::text,
        sqlc.arg(architecture)::text, sqlc.arg(source)::text, sqlc.arg(control)::text)
ON CONFLICT (item_id) DO NOTHING;

-- name: DebInsertCoordinate :execrows
INSERT INTO deb_coordinates (item_id, repo_id, suite, component)
VALUES (sqlc.arg(item_id)::bigint, sqlc.arg(repo_id)::bigint, sqlc.arg(suite)::text, sqlc.arg(component)::text)
ON CONFLICT (item_id, suite, component) DO NOTHING;

-- name: DebDeleteCoordinate :execrows
DELETE FROM deb_coordinates
 WHERE item_id = sqlc.arg(item_id)::bigint AND suite = sqlc.arg(suite)::text AND component = sqlc.arg(component)::text;

-- name: DebCoordinatesOf :many
SELECT suite, component FROM deb_coordinates WHERE item_id = sqlc.arg(item_id)::bigint ORDER BY suite, component;

-- name: DebCoordinateCount :one
SELECT count(*)::bigint FROM deb_coordinates WHERE item_id = sqlc.arg(item_id)::bigint;

-- name: DebBinaryByItem :many
-- The row of a package file with its item: path, digest, size and whether the item is in the trash can.
SELECT b.item_id, b.repo_id, b.package, b.version, b.architecture, b.source, b.control,
       i.path, COALESCE(i.blob_sha256, ''::bytea)::bytea AS sha256, COALESCE(i.size, 0)::bigint AS size,
       (i.trashed_at IS NOT NULL)::boolean AS trashed
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE b.item_id = sqlc.arg(item_id)::bigint AND b.repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebBinaryRow :many
-- The row of a package file without its item, for the hook that runs after the item rows were deleted.
SELECT item_id, repo_id, package, version, architecture, source, control
  FROM deb_binaries
 WHERE item_id = sqlc.arg(item_id)::bigint AND repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebBinaryByPath :many
-- The row of the live package file at a canonical path.
SELECT b.item_id, b.repo_id, b.package, b.version, b.architecture, b.source, b.control,
       i.path, COALESCE(i.blob_sha256, ''::bytea)::bytea AS sha256, COALESCE(i.size, 0)::bigint AS size,
       false::boolean AS trashed
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE b.repo_id = sqlc.arg(repo_id)::bigint AND i.path = sqlc.arg(path)::text AND i.trashed_at IS NULL;

-- name: DebBinaryOf :many
-- The row stored for a package, version and architecture, live or in the trash can.
SELECT b.item_id, b.repo_id, b.package, b.version, b.architecture, b.source, b.control,
       i.path, COALESCE(i.blob_sha256, ''::bytea)::bytea AS sha256, COALESCE(i.size, 0)::bigint AS size,
       (i.trashed_at IS NOT NULL)::boolean AS trashed
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE b.repo_id = sqlc.arg(repo_id)::bigint AND b.package = sqlc.arg(package)::text
   AND b.version = sqlc.arg(version)::text AND b.architecture = sqlc.arg(architecture)::text;

-- name: DebDeleteBinary :execrows
DELETE FROM deb_binaries WHERE item_id = sqlc.arg(item_id)::bigint;

-- name: DebVersionFiles :one
-- The package files of a package and version the repository holds, trashed ones not counted.
SELECT count(*)::bigint
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE b.repo_id = sqlc.arg(repo_id)::bigint AND b.package = sqlc.arg(package)::text
   AND b.version = sqlc.arg(version)::text AND i.trashed_at IS NULL AND b.item_id <> sqlc.arg(except_item)::bigint;

-- name: DebTombstoneRead :many
SELECT repo_id, path, sha256, deleted_by::text AS deleted_by
  FROM deb_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text;

-- name: DebPutTombstone :execrows
INSERT INTO deb_tombstones (repo_id, path, sha256, withheld_at, deleted_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(sha256)::bytea, sqlc.arg(withheld_at)::text::timestamptz,
        NULLIF(sqlc.arg(deleted_by)::text, '')::uuid)
ON CONFLICT (repo_id, path) DO NOTHING;

-- name: DebDeleteTombstone :execrows
DELETE FROM deb_tombstones
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND sha256 = sqlc.arg(sha256)::bytea;

-- name: DebReplayRepository :many
-- The local Debian repository a journal entry names, with its keys; an entry about any other repository finds nothing.
SELECT r.id, (p.key || '/' || r.key)::text AS ref
  FROM repositories r
  JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(repo_id)::bigint AND r.format = 'deb' AND r.class = 'local';

-- name: DebFileStored :one
-- Whether a live item at the path holds exactly the bytes with the digest: the replay withholds, and the command reinstates, only a
-- file the repository holds with exactly that content (R-DEB-032).
SELECT EXISTS (
  SELECT 1 FROM items
   WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND kind = 'file'
     AND trashed_at IS NULL AND blob_sha256 = sqlc.arg(sha256)::bytea)::boolean;

-- name: DebViewRows :many
-- What the package screens show of versions: the files of each, with their control paragraph.
SELECT i.version_id, b.architecture, b.control, b.source
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE i.version_id = ANY(sqlc.arg(version_ids)::bigint[]) AND i.trashed_at IS NULL
 ORDER BY i.version_id, b.architecture;

-- name: DebViewCoordinates :many
SELECT i.version_id, c.suite, c.component
  FROM deb_coordinates c
  JOIN items i ON i.id = c.item_id
 WHERE i.version_id = ANY(sqlc.arg(version_ids)::bigint[]) AND i.trashed_at IS NULL
 GROUP BY i.version_id, c.suite, c.component
 ORDER BY i.version_id, c.suite, c.component;

-- name: DebStoredVersions :many
-- The versions the repository serves of a package, newest arrival first: those with a live file that is not withheld.
SELECT b.version
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
 WHERE b.repo_id = sqlc.arg(repo_id)::bigint AND b.package = sqlc.arg(package)::text AND i.trashed_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM deb_tombstones t WHERE t.repo_id = b.repo_id AND t.path = i.path)
 GROUP BY b.version
 ORDER BY max(i.created_at) DESC, b.version DESC
 LIMIT sqlc.arg(max_rows)::bigint;

-- name: DebWithheldVersions :many
-- The live versions of the repositories (by "<project>/<repository>") all of whose live files the replay withheld.
SELECT pv.id
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
  JOIN project rp ON rp.id = r.project_id
 WHERE pv.trashed_at IS NULL AND (rp.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[]) AND r.format = 'deb'
   AND EXISTS (SELECT 1 FROM items i JOIN deb_tombstones t ON t.repo_id = i.repo_id AND t.path = i.path
                WHERE i.version_id = pv.id AND i.trashed_at IS NULL)
   AND NOT EXISTS (SELECT 1 FROM items i
                    WHERE i.version_id = pv.id AND i.trashed_at IS NULL
                      AND NOT EXISTS (SELECT 1 FROM deb_tombstones t WHERE t.repo_id = i.repo_id AND t.path = i.path));

-- name: DebFindVersions :many
-- The versions of the repositories whose control data contains the text.
SELECT DISTINCT i.version_id
  FROM deb_binaries b
  JOIN items i ON i.id = b.item_id
  JOIN repositories r ON r.id = b.repo_id
  JOIN project rp ON rp.id = r.project_id
 WHERE (rp.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[]) AND i.trashed_at IS NULL AND i.version_id IS NOT NULL
   AND strpos(lower(b.control), lower(sqlc.arg(needle)::text)) > 0
 ORDER BY i.version_id
 LIMIT sqlc.arg(max_rows)::bigint;

-- name: DebSuiteCount :one
-- The suites of a repository, for the bound of 64 (R-DEB-009).
SELECT count(*)::bigint FROM deb_suites WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebComponentCount :one
-- The distinct components a suite has files in, for the bound of 32 (R-DEB-009).
SELECT count(DISTINCT component)::bigint FROM deb_coordinates
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text;
