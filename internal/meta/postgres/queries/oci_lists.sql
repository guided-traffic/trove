-- The listings, the deletion cascade, the sweep and the journal replay of the OCI format's data port (internal/formatdata/oci;
-- R-OCI-015 to R-OCI-019, R-OCI-033). The package is a domain package and imports no driver types, so a digest travels as bytea
-- and a time as text cast to timestamptz.

-- name: OCITagPage :many
-- The tag items of an image name after a cursor path, in byte order (R-OCI-015). A tag whose manifest the journal replay withheld
-- is not listed (R-OCI-033). parent is "<name>/_tags", after_path "" or "<name>/_tags/<last>".
SELECT i.path
FROM items i
WHERE i.repo_id = sqlc.arg(repo_id)::bigint
  AND i.parent_path = sqlc.arg(parent)::text
  AND i.kind = 'file' AND i.trashed_at IS NULL
  AND i.path > sqlc.arg(after_path)::text
  AND NOT EXISTS (SELECT 1 FROM oci_tombstones t
                  WHERE t.repo_id = i.repo_id AND t.name = sqlc.arg(name)::text AND t.digest = i.blob_sha256)
ORDER BY i.path
LIMIT sqlc.arg(max_rows)::int;

-- name: OCIHoldsName :one
-- Whether the repository holds any live item below the image name: a tag, a manifest or a blob (R-OCI-015, the 404 of a name it
-- holds nothing of).
SELECT EXISTS (
  SELECT 1 FROM items
  WHERE repo_id = sqlc.arg(repo_id)::bigint
    AND parent_path IN (sqlc.arg(name)::text || '/_tags', sqlc.arg(name)::text || '/_manifests', sqlc.arg(name)::text || '/_blobs')
    AND kind = 'file' AND trashed_at IS NULL
)::boolean AS held;

-- name: OCICatalogNames :many
-- The image names of a repository that hold a manifest, in byte order after a cursor name (R-OCI-017). The catalog of the registry
-- root merges these per readable repository.
SELECT DISTINCT name::text AS name
FROM oci_manifests
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name > sqlc.arg(after)::text
ORDER BY name
LIMIT sqlc.arg(max_rows)::int;

-- name: OCITagsPointingAt :many
-- The tag items of an image name whose content is the manifest with the digest: the tags a deletion of the manifest takes (R-OCI-018).
SELECT path
FROM items
WHERE repo_id = sqlc.arg(repo_id)::bigint
  AND parent_path = sqlc.arg(parent)::text
  AND kind = 'file' AND trashed_at IS NULL
  AND blob_sha256 = sqlc.arg(digest)::bytea
ORDER BY path;

-- name: OCISubjectManifests :many
-- The manifests of an image name whose subject is the digest: the referrers a deletion of the subject takes (R-OCI-018).
SELECT digest
FROM oci_manifests
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND subject = sqlc.arg(subject)::bytea
ORDER BY digest;

-- name: OCIPutTombstone :execrows
-- The mark of a manifest the journal replay withheld (R-OCI-033); a mark that is there stays.
INSERT INTO oci_tombstones (repo_id, name, digest, withheld_at, deleted_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(digest)::bytea, sqlc.arg(withheld_at)::text::timestamptz,
        NULLIF(sqlc.arg(deleted_by)::text, '')::uuid)
ON CONFLICT (repo_id, name, digest) DO NOTHING;

-- name: OCIDeleteTombstone :execrows
-- Reinstates a withheld manifest: removes the mark (R-OCI-033).
DELETE FROM oci_tombstones
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest)::bytea;

-- name: OCITombstoneRead :one
-- The mark of a withheld manifest with the principal of the deletion (empty for none).
SELECT withheld_at, COALESCE(deleted_by::text, '')::text AS deleted_by
FROM oci_tombstones
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest)::bytea;

-- name: OCIDeleteManifest :execrows
-- The row of a manifest; its references go with it (the foreign key cascades, R-OCI-007).
DELETE FROM oci_manifests
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest)::bytea;

-- name: OCIReplayRepository :many
-- The local OCI repository a journal entry names. An entry about any other repository (a forged one included) finds nothing and is
-- "subject unknown".
SELECT r.id, (p.key || '/' || r.key)::text AS ref
FROM repositories r
JOIN project p ON p.id = r.project_id
WHERE r.id = sqlc.arg(repo_id)::bigint AND r.format = 'oci' AND r.class = 'local';

-- name: OCIManifestItemStored :one
-- Whether the manifest's item is stored (live) with the digest: the replay withholds and the command reinstates only a manifest
-- the repository holds with exactly that content (R-OCI-033).
SELECT EXISTS (
  SELECT 1 FROM items
  WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = sqlc.arg(path)::text
    AND kind = 'file' AND trashed_at IS NULL AND blob_sha256 = sqlc.arg(digest)::bytea
)::boolean AS stored;

-- name: OCISweepRepositories :many
-- The local OCI repositories the sweep walks (R-OCI-019): a remote's cached blobs are cache entries and are never swept.
SELECT r.id, p.key::text AS project_key, r.key::text AS repo_key
FROM repositories r
JOIN project p ON p.id = r.project_id
WHERE r.format = 'oci' AND r.class = 'local'
ORDER BY r.id;

-- name: OCIUnreferencedBlobs :many
-- The blob items of a repository that no manifest of their image name references and that are older than the cut-off, in path order
-- after a cursor (R-OCI-019). A blob that an upload session of the name holds a digest for and that is not finished is not a
-- candidate (the session is about to reference or has just committed it). name is the path without its last two segments.
SELECT i.path::text AS path,
       left(i.parent_path, length(i.parent_path) - 7)::text AS name,
       i.blob_sha256 AS digest,
       i.created_at
FROM items i
WHERE i.repo_id = sqlc.arg(repo_id)::bigint
  AND i.kind = 'file' AND i.trashed_at IS NULL
  AND right(i.parent_path, 7) = '/_blobs'
  AND i.path > sqlc.arg(after_path)::text
  AND i.created_at < sqlc.arg(before)::text::timestamptz
  AND NOT EXISTS (SELECT 1 FROM oci_references r
                  WHERE r.repo_id = i.repo_id AND r.name = left(i.parent_path, length(i.parent_path) - 7)
                    AND r.target = i.blob_sha256)
  AND NOT EXISTS (SELECT 1 FROM upload_sessions s
                  WHERE s.repo_id = i.repo_id AND s.binding = left(i.parent_path, length(i.parent_path) - 7)
                    AND s.digest = i.blob_sha256 AND s.state IN ('open', 'completing', 'assembled'))
ORDER BY i.path
LIMIT sqlc.arg(max_rows)::int;

-- name: OCIBlobStillUnreferenced :one
-- The sweep's re-check under the lock of the image name's package row, in the transaction that removes the blob item: no manifest
-- of the name references the digest and the item is the one the scan saw.
SELECT COALESCE(NOT EXISTS (SELECT 1 FROM oci_references r
                   WHERE r.repo_id = sqlc.arg(repo_id)::bigint AND r.name = sqlc.arg(name)::text AND r.target = sqlc.arg(digest)::bytea)
   AND EXISTS (SELECT 1 FROM items i
               WHERE i.repo_id = sqlc.arg(repo_id)::bigint AND i.path = sqlc.arg(path)::text AND i.kind = 'file'
                 AND i.trashed_at IS NULL AND i.blob_sha256 = sqlc.arg(digest)::bytea
                 AND i.created_at < sqlc.arg(before)::text::timestamptz)
   AND NOT EXISTS (SELECT 1 FROM upload_sessions s
                   WHERE s.repo_id = sqlc.arg(repo_id)::bigint AND s.binding = sqlc.arg(name)::text
                     AND s.digest = sqlc.arg(digest)::bytea AND s.state IN ('open', 'completing', 'assembled')), false)::boolean
  AS unreferenced;
