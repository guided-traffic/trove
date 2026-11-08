-- The statements of the files transfer and the items API (R-STOR-007, R-STOR-012,
-- R-STOR-016 to R-STOR-018, R-STOR-015). Parameters are plain Go types: a
-- principal id travels as text and is cast to uuid here, a digest as bytea. A
-- statement that may find no row is :many, so the caller needs no driver error.
-- A name that a join does not find is the empty string.
-- Paths are compared in the "C" collation (the column's), so that the range
-- [prefix + "/", prefix + "0") is exactly the subtree of prefix.

-- name: GetLiveItem :many
-- The item lookup of every download route. version_id names the stored version the item belongs to (0: none) and the
-- lateral join blk is the block of the vulnerability scanning (R-VULN-015): the finding that blocks the version in this
-- repository, read with the repository's policy as parameters so that a download costs no further query. enforce is false
-- for a repository that is not scanned and for every caller that is no download; the join then reads nothing.
--
-- A download costs this one statement (R-ARCH-027), so the rows the serve-time check of the format decides on are read here
-- too, for the key the caller names (internal/servefacts): facts_kind is '' for a caller that needs none, and the part of the
-- statement of another kind reads nothing. The tombstone of npm and NuGet, the NuGet version row and the facts of an npm
-- remote version are the statements NPMTombstoned, NuGetServeState and NPMRemoteTarballFacts, which the check still runs
-- itself when the facts were not read for its key. For an OCI repository (oci_local) the tombstone and the manifest row are those of
-- the image name facts_package and of the digest the item's blob is. entry_probe asks whether the cache entry of a remote repository's artifact
-- exists at the item's path (variant entry_variant) and is not marked gone: the remote hit is the item and its entry.
SELECT i.id, i.repo_id, i.path, i.parent_path, i.kind, i.blob_sha256, i.size, i.media_type,
       i.declared_sha256, i.declared_sha512, i.declared_sha1, i.declared_md5, i.declared_mismatch,
       i.explicit_folder, i.generated, i.upstream_url,
       i.created_at, i.modified_at, i.updated_at,
       COALESCE(CASE cp.kind WHEN 'ci_mapping' THEN 'ci' ELSE cp.kind END || ':' || cp.name, '')::text AS created_by_name,
       COALESCE(CASE mp.kind WHEN 'ci_mapping' THEN 'ci' ELSE mp.kind END || ':' || mp.name, '')::text AS modified_by_name,
       b.sha1 AS blob_sha1, b.md5 AS blob_md5, b.sha512 AS blob_sha512, b.content_status AS content_status,
       COALESCE(i.version_id, 0)::bigint AS version_id,
       COALESCE(blk.id::text, '')::text AS block_finding_id,
       COALESCE(blk.advisory_id, '')::text AS block_advisory_id,
       COALESCE(blk.kind, '')::text AS block_kind,
       COALESCE(blk.status, '')::text AS block_status,
       (CASE WHEN sqlc.arg(facts_kind)::text IN ('npm_local', 'npm_remote', 'nuget_local', 'oci_local') THEN sqlc.arg(facts_kind)::text ELSE '' END)::text AS fact_kind,
       (CASE sqlc.arg(facts_kind)::text
            WHEN 'npm_local' THEN EXISTS (
                SELECT 1 FROM npm_tombstones t
                 WHERE t.repo_id = i.repo_id AND t.name = sqlc.arg(facts_package)::text AND t.version = sqlc.arg(facts_version)::text)
            WHEN 'nuget_local' THEN EXISTS (
                SELECT 1 FROM nuget_tombstone t
                 WHERE t.repository_id = i.repo_id AND t.lower_id = sqlc.arg(facts_package)::text AND t.lower_version = sqlc.arg(facts_version)::text)
            WHEN 'oci_local' THEN EXISTS (
                SELECT 1 FROM oci_tombstones t
                 WHERE t.repo_id = i.repo_id AND t.name = sqlc.arg(facts_package)::text AND t.digest = i.blob_sha256)
            ELSE false END)::boolean AS fact_tombstoned,
       (sqlc.arg(facts_kind)::text = 'oci_local' AND EXISTS (
            SELECT 1 FROM oci_manifests m
             WHERE m.repo_id = i.repo_id AND m.name = sqlc.arg(facts_package)::text AND m.digest = i.blob_sha256))::boolean AS fact_oci_manifest,
       (ngv.listed IS NOT NULL)::boolean AS fact_nuget_stored,
       COALESCE(ngv.listed, false)::boolean AS fact_nuget_listed,
       COALESCE(nf.known, false)::boolean AS fact_npm_known,
       COALESCE(nf.listed, false)::boolean AS fact_npm_listed,
       COALESCE(nf.integrity, '')::text AS fact_npm_integrity,
       COALESCE(nf.shasum, '')::text AS fact_npm_shasum,
       COALESCE(nf.doc_stored, false)::boolean AS fact_npm_doc_stored,
       COALESCE(nf.doc_current, false)::boolean AS fact_npm_doc_current,
       COALESCE(nf.gone_us, 0)::bigint AS fact_npm_gone_us,
       COALESCE(nf.serve_removed, false)::boolean AS fact_npm_serve_removed,
       (CASE WHEN sqlc.arg(entry_probe)::boolean THEN EXISTS (
            SELECT 1 FROM remote_cache_entries e JOIN blobs eb ON eb.sha256 = e.blob_sha256
             WHERE e.repo_id = i.repo_id AND e.path = i.path AND e.variant = sqlc.arg(entry_variant)::text AND e.upstream_gone_at IS NULL)
            ELSE false END)::boolean AS entry_stored
  FROM items i
  LEFT JOIN blobs b ON b.sha256 = i.blob_sha256
  LEFT JOIN principal cp ON cp.id = i.created_by
  LEFT JOIN principal mp ON mp.id = i.modified_by
  LEFT JOIN LATERAL (
        SELECT f.id, f.advisory_id, f.kind, f.status
          FROM vuln_finding f
         WHERE sqlc.arg(enforce)::boolean AND f.version_id = i.version_id
           AND f.status IN ('needs_triage', 'confirmed', 'blocked')
           AND (f.status = 'blocked' OR (f.kind = 'malicious' AND sqlc.arg(block_malicious)::boolean))
         ORDER BY (f.kind = 'malicious' AND sqlc.arg(block_malicious)::boolean) DESC, f.severity_rank DESC, f.advisory_id
         LIMIT 1) AS blk ON true
  LEFT JOIN LATERAL (
        SELECT nv.listed
          FROM packages p
          JOIN package_versions pv ON pv.package_id = p.id
          JOIN nuget_version nv ON nv.version_id = pv.id
         WHERE sqlc.arg(facts_kind)::text = 'nuget_local' AND p.repo_id = i.repo_id AND p.name = sqlc.arg(facts_package)::text
           AND pv.version = sqlc.arg(facts_version)::text AND pv.trashed_at IS NULL
         LIMIT 1) AS ngv ON true
  LEFT JOIN LATERAL (
        SELECT (v.version IS NOT NULL) AS known,
               COALESCE(v.listed, false) AS listed,
               COALESCE(v.integrity, '') AS integrity,
               COALESCE(v.shasum, '') AS shasum,
               (e.repo_id IS NOT NULL) AS doc_stored,
               COALESCE((extract(epoch FROM e.upstream_gone_at) * 1000000)::bigint, 0) AS gone_us,
               COALESCE(e.expires_at > clock_timestamp()
                        AND e.validated_at > COALESCE((SELECT (s.value #>> '{}')::timestamptz FROM system_state s WHERE s.key = 'restored_at'), '-infinity'::timestamptz),
                        false) AS doc_current,
               COALESCE(r.config #>> '{npm,serveRemovedVersions}', '') = 'true' AS serve_removed
          FROM repositories r
          LEFT JOIN remote_cache_entries e
                 ON e.repo_id = r.id AND e.path = sqlc.arg(facts_package)::text COLLATE "C" AND e.variant = 'full' AND e.kind = 'metadata'
          LEFT JOIN npm_remote_versions v
                 ON v.repo_id = r.id AND v.name = sqlc.arg(facts_package)::text AND v.version = sqlc.arg(facts_version)::text
         WHERE sqlc.arg(facts_kind)::text = 'npm_remote' AND r.id = i.repo_id) AS nf ON true
 WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.path = sqlc.arg(path)::text AND i.trashed_at IS NULL;

-- name: LockLiveItem :many
-- The first statement of a deploy's link step: two uploads to one path serialise
-- here, and the second is an overwrite (R-STOR-005).
SELECT id, kind, blob_sha256, size, explicit_folder
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND trashed_at IS NULL
   FOR UPDATE;

-- name: InsertImplicitFolders :execrows
-- The missing ancestors of a file or folder, from the top, as a JSON array of
-- {"path", "parent"} objects (sqlc has no multi-array unnest). A folder that exists
-- stays as it is (explicit or not); a file at an ancestor path is found by the
-- caller with ListItemKinds and refused as path_conflict.
INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder, created_by, modified_by)
SELECT sqlc.arg(repo_id)::bigint, f ->> 'path', f ->> 'parent', 'folder', false,
       NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid
  FROM jsonb_array_elements(sqlc.arg(folders)::text::jsonb) AS f
ON CONFLICT (repo_id, path) WHERE trashed_at IS NULL DO NOTHING;

-- name: ListItemKinds :many
SELECT path, kind
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = ANY(sqlc.arg(paths)::text[]) AND trashed_at IS NULL;

-- name: LockFolderShared :many
-- A deploy holds its parent folder FOR SHARE until it commits, so the removal of
-- an empty implicit folder (LockFolderExclusive) waits for it and then sees the
-- new child.
SELECT id
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND kind = 'folder' AND trashed_at IS NULL
   FOR SHARE;

-- name: LockFoldersShared :many
-- The whole ancestor chain of a deploy, FOR SHARE, deepest first: the order in
-- which the removal of folders takes its exclusive locks (bottom up), so the two
-- cannot wait for each other in a circle. The caller compares the count with the
-- chain: a folder that a concurrent delete removed is missing from the result.
SELECT id, path
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = ANY(sqlc.arg(paths)::text[]) AND kind = 'folder' AND trashed_at IS NULL
 ORDER BY path DESC
   FOR SHARE;

-- name: InsertFileItem :many
-- A concurrent upload to the same path wins the unique index; no row comes back
-- and the caller locks the winner and treats its own upload as an overwrite.
INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size, media_type,
                   declared_sha256, declared_sha512, declared_sha1, declared_md5, upstream_url,
                   generated, created_by, modified_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(parent)::text, 'file',
        sqlc.arg(blob_sha256)::bytea, sqlc.arg(size)::bigint, NULLIF(sqlc.arg(media_type)::text, ''),
        sqlc.narg(declared_sha256)::bytea, sqlc.narg(declared_sha512)::bytea, sqlc.narg(declared_sha1)::bytea, sqlc.narg(declared_md5)::bytea,
        NULLIF(sqlc.arg(upstream_url)::text, ''), sqlc.arg(generated)::boolean,
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (repo_id, path) WHERE trashed_at IS NULL DO NOTHING
RETURNING id;

-- name: InsertExplicitFolder :many
INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder, created_by, modified_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(path)::text, sqlc.arg(parent)::text, 'folder', true,
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (repo_id, path) WHERE trashed_at IS NULL DO NOTHING
RETURNING id;

-- name: ReplaceFileContent :one
-- An overwrite in place (R-STOR-005): the old blob is released by the statement
-- trigger and found by the incremental collection. Trash is not enabled in M1
-- (R-STOR-022 is P1), so the replaced content is not kept.
UPDATE items
   SET blob_sha256 = sqlc.arg(blob_sha256)::bytea, size = sqlc.arg(size)::bigint,
       media_type = NULLIF(sqlc.arg(media_type)::text, ''),
       declared_sha256 = sqlc.narg(declared_sha256)::bytea, declared_sha512 = sqlc.narg(declared_sha512)::bytea,
       declared_sha1 = sqlc.narg(declared_sha1)::bytea, declared_md5 = sqlc.narg(declared_md5)::bytea,
       declared_mismatch = false, upstream_url = NULLIF(sqlc.arg(upstream_url)::text, ''),
       generated = sqlc.arg(generated)::boolean,
       modified_at = now(), updated_at = now(), modified_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE id = sqlc.arg(id)::bigint
RETURNING id;

-- name: MarkFolderExplicit :execrows
UPDATE items SET explicit_folder = true, updated_at = now()
 WHERE id = sqlc.arg(id)::bigint AND NOT explicit_folder;

-- name: CountSubtree :one
-- The live items at a path and below it. lo and hi bound the subtree in the
-- "C" collation: [path + "/", path + "0"); an empty path is the whole repository
-- (an empty hi means no upper bound).
SELECT count(*)::bigint
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND trashed_at IS NULL
   AND (path = sqlc.arg(path)::text OR (path >= sqlc.arg(lo)::text AND (sqlc.arg(hi)::text = '' OR path < sqlc.arg(hi)::text)));

-- name: ListSubtreeBatch :many
-- A keyset batch of the subtree in path order, files and folders. The delete
-- checks every item of it before anything is removed. max_id bounds the batch to
-- the items that existed when the delete was authorized (0: no bound).
SELECT id, path, kind, blob_sha256, size
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND trashed_at IS NULL
   AND (path = sqlc.arg(path)::text OR (path >= sqlc.arg(lo)::text AND (sqlc.arg(hi)::text = '' OR path < sqlc.arg(hi)::text)))
   AND path > sqlc.arg(after)::text
   AND (sqlc.arg(max_id)::bigint = 0 OR id <= sqlc.arg(max_id)::bigint)
   AND (NOT sqlc.arg(files_only)::bool OR kind = 'file')
 ORDER BY path
 LIMIT sqlc.arg(row_limit)::int;

-- name: DeleteItemsByID :many
-- Returns what was removed, so that the event of a file carries its digest and
-- size. The statement trigger of 00002 queues the released blobs for collection.
DELETE FROM items
 WHERE id = ANY(sqlc.arg(ids)::bigint[]) AND repo_id = sqlc.arg(repo_id)::bigint
RETURNING id, path, kind, blob_sha256, size, COALESCE(version_id, 0)::bigint AS version_id;

-- name: LockFolderExclusive :many
SELECT id, explicit_folder
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND kind = 'folder' AND trashed_at IS NULL
   FOR UPDATE;

-- name: LockFolderExclusiveFree :many
-- LockFolderExclusive for a removing transaction, which may hold a package row that a deploy
-- holding this folder FOR SHARE waits for: it never waits. A folder another transaction holds
-- is skipped; FolderLive tells it from a folder that is gone, and the caller removes it after
-- the commit in a transaction of its own (Q-NUGET-88).
SELECT id, explicit_folder
  FROM items
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND kind = 'folder' AND trashed_at IS NULL
   FOR UPDATE SKIP LOCKED;

-- name: FolderLive :one
SELECT EXISTS (
    SELECT 1 FROM items
     WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text AND kind = 'folder' AND trashed_at IS NULL
);

-- name: FolderHasChildren :one
SELECT EXISTS (
    SELECT 1 FROM items
     WHERE repo_id = sqlc.arg(repo_id)::bigint AND parent_path = sqlc.arg(path)::text AND trashed_at IS NULL
);

-- name: DeleteItemByIDOne :execrows
DELETE FROM items WHERE id = sqlc.arg(id)::bigint;

-- name: ListChildrenDirect :many
-- The live children of a folder in path order, after a cursor. folders=false
-- leaves folders out.
SELECT i.id, i.path, i.kind, i.size, i.modified_at, i.blob_sha256, b.sha1 AS blob_sha1
  FROM items i
  LEFT JOIN blobs b ON b.sha256 = i.blob_sha256
 WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.parent_path = sqlc.arg(path)::text AND i.trashed_at IS NULL
   AND i.path > sqlc.arg(after)::text
   AND (sqlc.arg(folders)::bool OR i.kind = 'file')
 ORDER BY i.path
 LIMIT sqlc.arg(row_limit)::int;

-- name: ListChildrenRecursive :many
-- The live items below a folder to a depth (max_depth is the most slashes the
-- path may hold below the folder; a negative value is no bound), in path order.
SELECT i.id, i.path, i.kind, i.size, i.modified_at, i.blob_sha256, b.sha1 AS blob_sha1
  FROM items i
  LEFT JOIN blobs b ON b.sha256 = i.blob_sha256
 WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.trashed_at IS NULL
   AND i.path >= sqlc.arg(lo)::text AND (sqlc.arg(hi)::text = '' OR i.path < sqlc.arg(hi)::text)
   AND i.path > sqlc.arg(after)::text
   AND (sqlc.arg(folders)::bool OR i.kind = 'file')
   AND (sqlc.arg(max_slashes)::int < 0 OR cardinality(string_to_array(i.path, '/')) - 1 <= sqlc.arg(max_slashes)::int)
 ORDER BY i.path
 LIMIT sqlc.arg(row_limit)::int;

-- name: ItemPathByID :many
-- The position behind a listing cursor that ended on an entry the caller may
-- not read: the cursor holds the item's id, never its path (R-AUTHZ-016). A
-- trashed item still marks its place; a purged one ends the traversal.
SELECT path FROM items WHERE id = sqlc.arg(id)::bigint AND repo_id = sqlc.arg(repo_id)::bigint;

-- name: GetItemStats :many
SELECT s.downloads, s.last_download_at,
       COALESCE(CASE p.kind WHEN 'ci_mapping' THEN 'ci' ELSE p.kind END || ':' || p.name, '')::text AS last_downloader_name
  FROM item_download_stats s
  LEFT JOIN principal p ON p.id = s.last_downloader
 WHERE s.item_id = sqlc.arg(item_id)::bigint;

-- name: UpsertDownloadStats :execrows
-- One flush of the statistics buffer (R-STOR-015), a JSON array of
-- {"id", "n", "at", "who"} objects: the rows go in in item id order
-- and join items, so the counts of an item removed since its download are dropped
-- instead of failing the batch on the foreign key.
INSERT INTO item_download_stats (item_id, downloads, last_download_at, last_downloader)
SELECT i.id, (d ->> 'n')::bigint, (d ->> 'at')::timestamptz, NULLIF(d ->> 'who', '')::uuid
  FROM jsonb_array_elements(sqlc.arg(stats)::text::jsonb) AS d
  JOIN items i ON i.id = (d ->> 'id')::bigint
 ORDER BY i.id
ON CONFLICT (item_id) DO UPDATE
   SET downloads = item_download_stats.downloads + EXCLUDED.downloads,
       last_download_at = greatest(item_download_stats.last_download_at, EXCLUDED.last_download_at),
       last_downloader = CASE WHEN item_download_stats.last_download_at IS NULL
                                OR EXCLUDED.last_download_at >= item_download_stats.last_download_at
                              THEN EXCLUDED.last_downloader ELSE item_download_stats.last_downloader END;

-- name: TouchRepositoryActivity :execrows
-- One flush of the activity buffer (R-REPO-012): at most one write per repository
-- and minute and replica; the later instant wins. A JSON array of {"id", "at"}.
UPDATE repositories r
   SET last_activity_at = greatest(r.last_activity_at, (a ->> 'at')::timestamptz)
  FROM jsonb_array_elements(sqlc.arg(activity)::text::jsonb) AS a
 WHERE r.id = (a ->> 'id')::bigint;
