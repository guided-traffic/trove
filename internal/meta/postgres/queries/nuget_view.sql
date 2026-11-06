-- The NuGet side of the package view (R-API-029, R-EP-016): the statements behind
-- internal/formatdata/nuget/view.go. The generic rows answer names, versions and counts
-- (package_view.sql); these answer what only the NuGet tables hold. A trashed version is
-- invisible to every NuGet read (R-NUGET-005), so each statement joins package_versions.

-- name: NuGetViewSummaries :many
-- The description (the summary when there is none) and the tags of the given versions.
SELECT nv.version_id,
       COALESCE(NULLIF(btrim(nv.metadata ->> 'description'), ''), btrim(nv.metadata ->> 'summary'), '')::text AS description,
       CASE WHEN jsonb_typeof(nv.metadata -> 'tags') = 'array' THEN (nv.metadata -> 'tags')::text ELSE '[]' END::text AS tags
  FROM nuget_version nv
  JOIN package_versions pv ON pv.id = nv.version_id
 WHERE nv.version_id = ANY(sqlc.arg(ids)::bigint[])
   AND pv.trashed_at IS NULL;

-- name: NuGetViewDetails :many
-- The metadata, the listing state and the declared ID of each given version, with the digest
-- and the size of the blob of the lead item, from which the README is read when it is asked
-- for. blob_sha256 and blob_size are empty for a version without a stored package.
SELECT nv.version_id,
       nv.metadata::text AS metadata,
       nv.listed,
       nv.declared_id,
       COALESCE(i.blob_sha256, ''::bytea)::bytea AS blob_sha256,
       COALESCE(b.size, 0)::bigint AS blob_size
  FROM nuget_version nv
  JOIN package_versions pv ON pv.id = nv.version_id
  LEFT JOIN items i ON i.id = pv.lead_item_id
  LEFT JOIN blobs b ON b.sha256 = i.blob_sha256
 WHERE nv.version_id = ANY(sqlc.arg(ids)::bigint[])
   AND pv.trashed_at IS NULL;

-- name: NuGetViewFind :many
-- Versions of the given repositories (qualified names, <project>/<key>) whose description (the summary when there is none)
-- contains the text, case-insensitively, or whose tags contain the keyword. One of text and
-- keyword is set. At most max_rows rows; the caller asks for one more than it accepts and
-- intersects the ids with what the subject may read.
SELECT nv.version_id
  FROM nuget_version nv
  JOIN package_versions pv ON pv.id = nv.version_id
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
  JOIN project rp ON rp.id = r.project_id
 WHERE pv.trashed_at IS NULL
   AND (rp.key || '/' || r.key) = ANY(sqlc.arg(repos)::text[])
   AND ((sqlc.arg(text)::text <> ''
         AND position(lower(sqlc.arg(text)::text) in lower(COALESCE(NULLIF(btrim(nv.metadata ->> 'description'), ''), nv.metadata ->> 'summary', ''))) > 0)
     OR (sqlc.arg(keyword)::text <> '' AND jsonb_typeof(nv.metadata -> 'tags') = 'array'
         AND (nv.metadata -> 'tags') @> to_jsonb(sqlc.arg(keyword)::text)))
 ORDER BY nv.version_id
 LIMIT sqlc.arg(max_rows)::int;

-- name: NuGetViewStoredLocal :many
-- The versions a hosted repository serves of an ID, listed or not: live, newest arrival
-- first (the version id grows with every insert), at most max_rows (R-EP-016).
SELECT pv.version::text AS version
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN nuget_version nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text AND pv.trashed_at IS NULL
 ORDER BY pv.id DESC
 LIMIT sqlc.arg(max_rows)::int;

-- name: NuGetViewStoredRemote :many
-- The versions a remote repository holds in its cache of an ID, newest published first
-- (the version id breaks a tie), at most max_rows.
SELECT pv.version::text AS version
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN nuget_version nv ON nv.version_id = pv.id
 WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(name)::text AND pv.trashed_at IS NULL
 ORDER BY nv.published_at DESC, pv.id DESC
 LIMIT sqlc.arg(max_rows)::int;
