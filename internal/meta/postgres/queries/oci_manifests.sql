-- The statements of the OCI data port for manifests: the rows a push writes, the checks it makes under the package's lock, and
-- the reads of the package view (internal/formatdata/oci/store_manifests.go; R-OCI-007, R-OCI-013, R-OCI-031, R-OCI-033).
-- A digest travels as bytea; the format's paths are built by the format and arrive as text.

-- name: OCIPutManifest :exec
-- Records a manifest of an image name; one that is recorded already changes nothing (its row is a function of its bytes).
INSERT INTO oci_manifests (repo_id, name, digest, media_type, artifact_type, subject, size, content_size, annotations, annotations_cut)
VALUES (
  sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(digest)::bytea, sqlc.arg(media_type)::text,
  NULLIF(sqlc.arg(artifact_type)::text, ''), sqlc.narg(subject)::bytea, sqlc.arg(size)::bigint, sqlc.arg(content_size)::bigint,
  sqlc.narg(annotations)::jsonb, sqlc.arg(annotations_cut)::boolean
)
ON CONFLICT (repo_id, name, digest) DO NOTHING;

-- name: OCISetIndexContentSize :exec
-- Sets the content size of an index to the sum of its children's, saturated at the largest bigint (R-OCI-031, Q-OCI-130). The
-- required children are stored before the index (the commit transaction holds them against a removal) and a listed child cannot be
-- removed while the index exists, so their sum is final and, for a child that is an index itself, correct at any depth. A foreign
-- child (a descriptor with urls) is not required: it adds nothing here when it is not stored yet, and nothing later (H-672).
UPDATE oci_manifests m
   SET content_size = LEAST(COALESCE((
         SELECT sum(c.content_size)
           FROM oci_references r
           JOIN oci_manifests c ON c.repo_id = r.repo_id AND c.name = r.name AND c.digest = r.target
          WHERE r.repo_id = m.repo_id AND r.name = m.name AND r.manifest = m.digest AND r.kind = 'manifest'), 0),
         9223372036854775807)::bigint
 WHERE m.repo_id = sqlc.arg(repo_id)::bigint AND m.name = sqlc.arg(name)::text AND m.digest = sqlc.arg(digest)::bytea;

-- name: OCITagSizes :many
-- The size of the image each of the tags with the given version ids points at: the content size of its manifest, which an index
-- holds as the sum of its children (R-OCI-031, Q-OCI-130). One statement for any number of packages.
SELECT pv.id AS version_id, m.content_size
FROM package_versions pv
JOIN packages p ON p.id = pv.package_id
JOIN items i ON i.id = pv.lead_item_id
JOIN oci_manifests m ON m.repo_id = p.repo_id AND m.name = p.name AND m.digest = i.blob_sha256
WHERE pv.id = ANY(sqlc.arg(version_ids)::bigint[]) AND pv.trashed_at IS NULL AND i.trashed_at IS NULL
ORDER BY pv.id;

-- name: OCIPutReferences :exec
-- The edges of a manifest, one per target; an edge that is recorded already changes nothing. They travel as one JSON array of
-- {"target": "<hex>", "kind": "...", "platform": "..."} (sqlc has no multi-array unnest); an empty platform is none.
INSERT INTO oci_references (repo_id, name, manifest, target, kind, platform)
SELECT sqlc.arg(repo_id)::bigint, sqlc.arg(name)::text, sqlc.arg(manifest)::bytea, decode(e ->> 'target', 'hex'), e ->> 'kind',
       NULLIF(e ->> 'platform', '')
FROM jsonb_array_elements(sqlc.arg(refs)::jsonb) AS e
ON CONFLICT (repo_id, name, manifest, target) DO NOTHING;

-- name: OCIExistingItems :many
-- The live files among the paths (R-OCI-013 step 4: what a manifest names must be stored under the name).
SELECT path::text AS path
FROM items
WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = ANY(sqlc.arg(paths)::text[]) AND kind = 'file' AND trashed_at IS NULL;

-- name: OCILockExistingItems :many
-- The same inside the commit transaction, with the rows held against a concurrent removal until the manifest's rows are written.
SELECT path::text AS path
FROM items
WHERE repo_id = sqlc.arg(repo_id)::bigint AND path = ANY(sqlc.arg(paths)::text[]) AND kind = 'file' AND trashed_at IS NULL
ORDER BY path
FOR SHARE;

-- name: OCIWithheldAmong :many
-- The digests among the given ones that the journal replay withheld under the name (R-OCI-033).
SELECT digest
FROM oci_tombstones
WHERE repo_id = sqlc.arg(repo_id)::bigint AND name = sqlc.arg(name)::text AND digest = ANY(sqlc.arg(digests)::bytea[]);

-- name: OCITagViews :many
-- The tags with the given version ids and the rows of the manifests they point at, for the package view (R-OCI-031): the lead item
-- of the version is the tag item, whose blob is the manifest's. Any number of packages in one statement.
SELECT pv.id AS version_id, pv.version::text AS tag, m.digest, m.media_type, COALESCE(m.artifact_type, '')::text AS artifact_type,
       m.size, m.content_size
FROM package_versions pv
JOIN packages p ON p.id = pv.package_id
JOIN items i ON i.id = pv.lead_item_id
JOIN oci_manifests m ON m.repo_id = p.repo_id AND m.name = p.name AND m.digest = i.blob_sha256
WHERE pv.id = ANY(sqlc.arg(version_ids)::bigint[]) AND pv.trashed_at IS NULL AND i.trashed_at IS NULL
ORDER BY pv.id;

-- name: OCIChildPlatforms :many
-- The children the indexes of the given versions name, with the platform of the edge and the child's own facts (R-OCI-031).
SELECT pv.id AS version_id, COALESCE(r.platform, '')::text AS platform, c.digest, c.media_type, c.size, c.content_size
FROM package_versions pv
JOIN packages p ON p.id = pv.package_id
JOIN items i ON i.id = pv.lead_item_id
JOIN oci_references r ON r.repo_id = p.repo_id AND r.name = p.name AND r.manifest = i.blob_sha256 AND r.kind = 'manifest'
JOIN oci_manifests c ON c.repo_id = r.repo_id AND c.name = r.name AND c.digest = r.target
WHERE pv.id = ANY(sqlc.arg(version_ids)::bigint[]) AND pv.trashed_at IS NULL AND i.trashed_at IS NULL
ORDER BY pv.id, r.platform NULLS LAST, r.target;

-- name: OCIStoredTags :many
-- The tags a local repository serves of a name, newest push first: live versions whose manifest the replay did not withhold.
SELECT pv.version::text AS tag
FROM package_versions pv
JOIN packages p ON p.id = pv.package_id
JOIN items i ON i.id = pv.lead_item_id
WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text AND pv.trashed_at IS NULL AND i.trashed_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM oci_tombstones t WHERE t.repo_id = p.repo_id AND t.name = p.name AND t.digest = i.blob_sha256)
ORDER BY pv.id DESC
LIMIT sqlc.arg(max_rows)::int;

-- name: OCIWithheldVersions :many
-- The live versions (tags) of the repositories (by "<project>/<repository>") that point at a manifest the replay withheld.
SELECT pv.id
FROM package_versions pv
JOIN packages p ON p.id = pv.package_id
JOIN repositories r ON r.id = p.repo_id
JOIN project rp ON rp.id = r.project_id
JOIN items i ON i.id = pv.lead_item_id
JOIN oci_tombstones t ON t.repo_id = p.repo_id AND t.name = p.name AND t.digest = i.blob_sha256
WHERE pv.trashed_at IS NULL AND (rp.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[]);
