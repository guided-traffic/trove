-- The npm side of the package view (R-API-029): the statements behind
-- internal/formatdata/npm/view.go. The generic rows answer names, versions and
-- counts (package_view.sql); these answer what only the npm tables hold.

-- name: NPMViewSummaries :many
-- The description and the keywords of the manifests of the given versions.
SELECT nv.version_id,
       COALESCE(nv.manifest ->> 'description', '')::text AS description,
       CASE WHEN jsonb_typeof(nv.manifest -> 'keywords') = 'array' THEN (nv.manifest -> 'keywords')::text ELSE '[]' END::text AS keywords
  FROM npm_versions nv
 WHERE nv.version_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: NPMViewDetails :many
-- The manifest, the deprecation, the README (when asked) and the dist-tags of the
-- package row of each given version.
SELECT nv.version_id,
       nv.manifest::text AS manifest,
       COALESCE(nv.deprecated, '')::text AS deprecated,
       (COALESCE(nv.readme, '') <> '')::boolean AS has_readme,
       CASE WHEN sqlc.arg(with_readme)::boolean THEN COALESCE(nv.readme, '') ELSE '' END::text AS readme,
       COALESCE(nv.readme_filename, '')::text AS readme_filename,
       COALESCE(np.dist_tags, '{}'::jsonb)::text AS dist_tags
  FROM npm_versions nv
  JOIN package_versions pv ON pv.id = nv.version_id
  LEFT JOIN npm_packages np ON np.package_id = pv.package_id
 WHERE nv.version_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: NPMViewFind :many
-- Versions of the given repositories whose description contains the text
-- (case-insensitively) or whose keywords contain the keyword. One of text and
-- keyword is set. At most max_rows rows; the caller asks for one more than it
-- accepts. The caller intersects the ids with what the subject may read.
SELECT nv.version_id
  FROM npm_versions nv
  JOIN package_versions pv ON pv.id = nv.version_id
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
 WHERE pv.trashed_at IS NULL
   AND r.key = ANY(sqlc.arg(repos)::text[])
   AND ((sqlc.arg(text)::text <> '' AND position(lower(sqlc.arg(text)::text) in lower(COALESCE(nv.manifest ->> 'description', ''))) > 0)
     OR (sqlc.arg(keyword)::text <> '' AND jsonb_typeof(nv.manifest -> 'keywords') = 'array'
         AND (nv.manifest -> 'keywords') @> to_jsonb(sqlc.arg(keyword)::text)))
 ORDER BY nv.version_id
 LIMIT sqlc.arg(max_rows)::int;

-- name: NPMViewWithheld :many
-- Stored versions the format does not serve: tombstoned and not trashed, which a
-- restore's replay leaves behind (R-NPM-047).
SELECT pv.id
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
  JOIN npm_tombstones t ON t.repo_id = p.repo_id AND t.name = p.name AND t.version = pv.version
 WHERE pv.trashed_at IS NULL
   AND r.key = ANY(sqlc.arg(repos)::text[]);
