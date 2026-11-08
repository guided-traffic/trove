-- The reads of the OCI format's data port (internal/formatdata/oci; R-OCI-007, R-OCI-016, R-OCI-018). The package is a domain
-- package and imports no driver types, so a digest travels as bytea and a time as text cast to timestamptz. The writes and the
-- listings of the tags, the catalog and the sweep are written by the work package that fills the port.

-- name: OCIGetManifest :one
-- The row of a manifest of an image name (R-OCI-007).
SELECT repo_id, name, digest, media_type, artifact_type, subject, size, content_size, annotations, annotations_cut, created_at
FROM oci_manifests
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest)::bytea;

-- name: OCIListReferences :many
-- What a manifest names: its manifests and blobs (R-OCI-007).
SELECT target, kind, COALESCE(platform, '')::text AS platform
FROM oci_references
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND manifest = sqlc.arg(manifest)::bytea
ORDER BY kind, target;

-- name: OCIReferencedBy :many
-- The manifests of the name that name the target: the deletion of a manifest or a blob that another manifest still names is
-- refused (R-OCI-018).
SELECT manifest
FROM oci_references
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND target = sqlc.arg(target)::bytea
ORDER BY manifest;

-- name: OCIListReferrers :many
-- The manifests whose subject is the digest, in digest order after the cursor, optionally of one artifact type (R-OCI-016).
-- A NULL cursor starts at the first digest. A manifest the journal replay withheld is not listed (R-OCI-033).
SELECT repo_id, name, digest, media_type, artifact_type, subject, size, content_size, annotations, annotations_cut, created_at
FROM oci_manifests
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND subject = sqlc.arg(subject)::bytea
  AND (sqlc.arg(artifact_type)::text = '' OR artifact_type = sqlc.arg(artifact_type)::text)
  AND (sqlc.narg(after)::bytea IS NULL OR digest > sqlc.narg(after)::bytea)
  AND NOT EXISTS (SELECT 1 FROM oci_tombstones t
                  WHERE t.repo_id = oci_manifests.repo_id AND t.name = oci_manifests.name AND t.digest = oci_manifests.digest)
ORDER BY digest
LIMIT sqlc.arg(max_rows)::int;

-- name: OCIGetTombstone :one
-- The withheld mark of a manifest the journal replay withheld after a restore (R-OCI-033).
SELECT withheld_at
FROM oci_tombstones
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = sqlc.arg(digest)::bytea;
