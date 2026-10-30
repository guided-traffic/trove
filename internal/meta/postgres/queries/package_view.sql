-- The package view across repositories (R-API-029): internal/packages/view.
--
-- Every statement counts only the versions the caller may read, in the statement
-- that counts them. The caller's right is passed in as data, three ways:
--   whole        keys of the repositories whose every package and version the caller
--                may read (authz.ReadAll);
--   rows         one entry "<repository key>\x1f<package name>\x1f<version>" per
--                version the caller may read in the other repositories
--                (authz.ReadRows), decided by the decision point row by row;
--   withheld     ids of stored versions the format does not serve.
-- A repository in neither list contributes no row. The scope parameters (formats,
-- repos) narrow the rows a request asks about and never widen them.

-- name: PackageViewList :many
-- One row per package (format and normalized name) with at least one visible
-- version, aggregated over the visible versions, in keyset order. sort is name,
-- createdAt or updatedAt; the tie-breakers are name and format, in the direction of
-- the sort. q_ids and keyword_ids are the versions whose description or keywords the
-- format matched (a package matches when one of its visible versions is among them).
-- exact_name, when set, restricts the rows to one package. Names are ordered bytewise
-- (collation C), so the order does not depend on the database's locale and the keyset
-- comparison of a cursor means the same on every replica.
WITH vis AS (
  SELECT pv.id AS vid, pv.version, pv.created_at, (p.name COLLATE "C")::text AS name, p.display_name, r.format,
         COALESCE((SELECT s.downloads FROM item_download_stats s WHERE s.item_id = pv.lead_item_id), 0)::bigint AS downloads
    FROM package_versions pv
    JOIN packages p ON p.id = pv.package_id
    JOIN repositories r ON r.id = p.repo_id
   WHERE pv.trashed_at IS NULL
     AND NOT (pv.id = ANY(sqlc.arg(withheld)::bigint[]))
     AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
     AND (NOT sqlc.arg(has_repos)::boolean OR r.key = ANY(sqlc.arg(repos)::text[]))
     AND (sqlc.arg(name_prefix)::text = '' OR starts_with(lower(p.name), lower(sqlc.arg(name_prefix)::text)))
     AND (sqlc.arg(exact_name)::text = '' OR (r.format = sqlc.arg(exact_format)::text AND p.name = sqlc.arg(exact_name)::text))
     AND (r.key = ANY(sqlc.arg(whole)::text[])
          OR concat_ws(chr(31), r.key, p.name, pv.version) = ANY(sqlc.arg(rows)::text[]))
), grouped AS (
  SELECT v.format, v.name, min(v.display_name)::text AS display_name,
         min(v.created_at)::timestamptz AS created_at, max(v.created_at)::timestamptz AS updated_at,
         count(DISTINCT v.version)::bigint AS version_count, sum(v.downloads)::bigint AS downloads
    FROM vis v
   GROUP BY v.format, v.name
  HAVING (sqlc.arg(q)::text = '' OR position(lower(sqlc.arg(q)::text) in lower(v.name)) > 0 OR bool_or(v.vid = ANY(sqlc.arg(q_ids)::bigint[])))
     AND (sqlc.arg(keyword)::text = '' OR bool_or(v.vid = ANY(sqlc.arg(keyword_ids)::bigint[])))
     AND (NOT sqlc.arg(has_from)::boolean OR min(v.created_at) >= NULLIF(sqlc.arg(created_from)::text, '')::timestamptz)
     AND (NOT sqlc.arg(has_to)::boolean OR min(v.created_at) < NULLIF(sqlc.arg(created_to)::text, '')::timestamptz)
)
SELECT g.format, g.name, g.display_name, g.created_at, g.updated_at, g.version_count, g.downloads
  FROM grouped g
 WHERE NOT sqlc.arg(has_after)::boolean
    OR CASE sqlc.arg(sort)::text
         WHEN 'createdAt' THEN CASE WHEN sqlc.arg(descending)::boolean
              THEN (g.created_at, g.name, g.format) < (NULLIF(sqlc.arg(after_time)::text, '')::timestamptz, sqlc.arg(after_name)::text, sqlc.arg(after_format)::text)
              ELSE (g.created_at, g.name, g.format) > (NULLIF(sqlc.arg(after_time)::text, '')::timestamptz, sqlc.arg(after_name)::text, sqlc.arg(after_format)::text) END
         WHEN 'updatedAt' THEN CASE WHEN sqlc.arg(descending)::boolean
              THEN (g.updated_at, g.name, g.format) < (NULLIF(sqlc.arg(after_time)::text, '')::timestamptz, sqlc.arg(after_name)::text, sqlc.arg(after_format)::text)
              ELSE (g.updated_at, g.name, g.format) > (NULLIF(sqlc.arg(after_time)::text, '')::timestamptz, sqlc.arg(after_name)::text, sqlc.arg(after_format)::text) END
         ELSE CASE WHEN sqlc.arg(descending)::boolean
              THEN (g.name, g.format) < (sqlc.arg(after_name)::text, sqlc.arg(after_format)::text)
              ELSE (g.name, g.format) > (sqlc.arg(after_name)::text, sqlc.arg(after_format)::text) END
       END
 ORDER BY
   (CASE WHEN sqlc.arg(sort)::text = 'createdAt' AND NOT sqlc.arg(descending)::boolean THEN g.created_at END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(sort)::text = 'createdAt' AND sqlc.arg(descending)::boolean THEN g.created_at END) DESC NULLS LAST,
   (CASE WHEN sqlc.arg(sort)::text = 'updatedAt' AND NOT sqlc.arg(descending)::boolean THEN g.updated_at END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(sort)::text = 'updatedAt' AND sqlc.arg(descending)::boolean THEN g.updated_at END) DESC NULLS LAST,
   (CASE WHEN NOT sqlc.arg(descending)::boolean THEN g.name END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(descending)::boolean THEN g.name END) DESC NULLS LAST,
   (CASE WHEN NOT sqlc.arg(descending)::boolean THEN g.format END) ASC NULLS LAST,
   (CASE WHEN sqlc.arg(descending)::boolean THEN g.format END) DESC NULLS LAST
 LIMIT sqlc.arg(max_rows)::int;

-- name: PackageViewVersions :many
-- The visible rows of the given packages (format and normalized name), one per version per repository, with the repository, the size of the
-- version's files (when asked) and the downloads of its lead item. packages holds
-- "<format>\x1f<name>" entries. The caller groups the rows per version.
SELECT pv.id AS version_id, r.format, p.name, p.display_name, pv.version, pv.created_at,
       r.id AS repo_id, r.key AS repo_key, r.class AS repo_class,
       CASE WHEN sqlc.arg(with_size)::boolean
            THEN COALESCE((SELECT sum(i.size) FROM items i WHERE i.version_id = pv.id AND i.kind = 'file' AND i.trashed_at IS NULL), 0)
            ELSE 0 END::bigint AS size_bytes,
       COALESCE((SELECT s.downloads FROM item_download_stats s WHERE s.item_id = pv.lead_item_id), 0)::bigint AS downloads
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
 WHERE pv.trashed_at IS NULL
   AND NOT (pv.id = ANY(sqlc.arg(withheld)::bigint[]))
   AND concat_ws(chr(31), r.format, p.name) = ANY(sqlc.arg(packages)::text[])
   AND (NOT sqlc.arg(has_repos)::boolean OR r.key = ANY(sqlc.arg(repos)::text[]))
   AND (r.key = ANY(sqlc.arg(whole)::text[])
        OR concat_ws(chr(31), r.key, p.name, pv.version) = ANY(sqlc.arg(rows)::text[]))
 ORDER BY pv.created_at, pv.id;

-- name: PackageViewFiles :many
-- The files of a version: the live file items linked to it.
SELECT i.path, i.size, encode(i.blob_sha256, 'hex')::text AS sha256
  FROM items i
 WHERE i.version_id = sqlc.arg(version_id)::bigint AND i.kind = 'file' AND i.trashed_at IS NULL
 ORDER BY i.path;

-- name: PackageViewCandidates :many
-- The (repository, package, version) triples of the given repositories, for the
-- repositories whose rows the decision point decides one by one. At most max_rows;
-- the caller asks for one more than it accepts.
SELECT r.key AS repo_key, p.name, pv.version
  FROM package_versions pv
  JOIN packages p ON p.id = pv.package_id
  JOIN repositories r ON r.id = p.repo_id
 WHERE pv.trashed_at IS NULL
   AND r.key = ANY(sqlc.arg(keys)::text[])
   AND (cardinality(sqlc.arg(formats)::text[]) = 0 OR r.format = ANY(sqlc.arg(formats)::text[]))
   AND (sqlc.arg(name_prefix)::text = '' OR starts_with(lower(p.name), lower(sqlc.arg(name_prefix)::text)))
   AND (sqlc.arg(exact_name)::text = '' OR p.name = sqlc.arg(exact_name)::text)
 ORDER BY r.key, p.name, pv.version
 LIMIT sqlc.arg(max_rows)::int;
