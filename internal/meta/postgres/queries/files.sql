-- The statements of the files transfer and the items API (R-STOR-007, R-STOR-012,
-- R-STOR-016 to R-STOR-018, R-STOR-015). Parameters are plain Go types: a
-- principal id travels as text and is cast to uuid here, a digest as bytea. A
-- statement that may find no row is :many, so the caller needs no driver error.
-- A name that a join does not find is the empty string.
-- Paths are compared in the "C" collation (the column's), so that the range
-- [prefix + "/", prefix + "0") is exactly the subtree of prefix.

-- name: GetLiveItem :many
SELECT i.id, i.repo_id, i.path, i.parent_path, i.kind, i.blob_sha256, i.size, i.media_type,
       i.declared_sha256, i.declared_sha512, i.declared_sha1, i.declared_md5, i.declared_mismatch,
       i.explicit_folder, i.generated, i.upstream_url,
       i.created_at, i.modified_at, i.updated_at,
       COALESCE(CASE cp.kind WHEN 'ci_mapping' THEN 'ci' ELSE cp.kind END || ':' || cp.name, '')::text AS created_by_name,
       COALESCE(CASE mp.kind WHEN 'ci_mapping' THEN 'ci' ELSE mp.kind END || ':' || mp.name, '')::text AS modified_by_name,
       b.sha1 AS blob_sha1, b.md5 AS blob_md5, b.sha512 AS blob_sha512, b.content_status AS content_status
  FROM items i
  LEFT JOIN blobs b ON b.sha256 = i.blob_sha256
  LEFT JOIN principal cp ON cp.id = i.created_by
  LEFT JOIN principal mp ON mp.id = i.modified_by
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
