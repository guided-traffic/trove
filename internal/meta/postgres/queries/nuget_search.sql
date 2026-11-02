-- The search of the NuGet format's local repositories (internal/formatdata/nuget/search.go;
-- R-NUGET-010). A package matches by its latest visible version: the highest listed,
-- untrashed version that the request's pre-release and SemVer 2.0.0 filters leave
-- (R-NUGET-004), picked by DISTINCT ON over the index nuget_version_latest. The terms and
-- the type are tested against that row alone, so the metadata that matches is the metadata
-- the result shows.
--
-- The terms travel as one JSON array (sqlc has no multi-array unnest), each
-- {"field", "value", "tsq", "tsq_id"}: value is the folded text of the term, tsq a
-- to_tsquery text of prefix words joined by the phrase operator, tsq_id the same limited
-- to weight A, the ID tokens of search_doc. An empty tsq is a term without a word (only
-- punctuation); the substring test on the ID still serves it.
--
-- A free-text term matches when the ID contains it, or search_doc has the words as ID
-- token prefixes, title, tags, authors, summary or description (Q-NUGET-12, Q-NUGET-46). A
-- field term reads the same field of the metadata object. 'owner' and any other field
-- match nothing: a local package has no owners to match (Q-NUGET-46).
--
-- The rank is the best tier over the free-text terms: the ID equals the term (0), the ID
-- starts with it (1), the ID contains it or has an ID token that starts with it (2), any
-- other field (3). A query without a free-text term ranks every package 3, which is the
-- order of an empty query: lower_id (R-NUGET-010; the downloads of R-STOR-015 are 0 until
-- they ship). Ties break by lower_id in byte order (COLLATE "C"), so a page is stable and the
-- order does not depend on the collation of the installation's database.

-- name: NuGetSearchCount :one
-- How many packages match (totalHits for a caller who reads the repository whole).
WITH terms AS MATERIALIZED (
    SELECT e ->> 'field' AS field, e ->> 'value' AS value, NULLIF(e ->> 'tsq', '')::tsquery AS tsq
      FROM jsonb_array_elements(sqlc.arg(terms)::text::jsonb) AS e
), latest AS (
    SELECT DISTINCT ON (nv.lower_id)
           nv.lower_id, nv.package_types, nv.search_doc, nv.metadata
      FROM nuget_version nv
      JOIN package_versions pv ON pv.id = nv.version_id
     WHERE nv.repository_id = sqlc.arg(repository_id)::bigint
       AND nv.listed AND pv.trashed_at IS NULL
       AND (sqlc.arg(prerelease)::boolean OR NOT nv.is_prerelease)
       AND (sqlc.arg(semver2)::boolean OR NOT nv.is_semver2)
     ORDER BY nv.lower_id, nv.sort_key DESC
)
SELECT count(*)::bigint AS total
  FROM latest l
 WHERE (sqlc.arg(package_type)::text = '' OR sqlc.arg(package_type)::text = ANY (l.package_types))
   AND NOT EXISTS (
       SELECT 1 FROM terms t
        WHERE NOT COALESCE(CASE t.field
              WHEN '' THEN strpos(l.lower_id, t.value) > 0 OR l.search_doc @@ t.tsq
              WHEN 'id' THEN strpos(l.lower_id, t.value) > 0
              WHEN 'packageid' THEN l.lower_id = t.value
              WHEN 'version' THEN t.value <> '' AND EXISTS (
                   SELECT 1 FROM nuget_version v JOIN package_versions p ON p.id = v.version_id
                    WHERE v.repository_id = sqlc.arg(repository_id)::bigint AND v.lower_id = l.lower_id
                      AND v.listed AND p.trashed_at IS NULL AND p.version = t.value
                      AND (sqlc.arg(prerelease)::boolean OR NOT v.is_prerelease)
                      AND (sqlc.arg(semver2)::boolean OR NOT v.is_semver2))
              WHEN 'title' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'title', '')) @@ t.tsq
              WHEN 'tags' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'tags', '')) @@ t.tsq
              WHEN 'author' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'authors', '')) @@ t.tsq
              WHEN 'description' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'description', '')) @@ t.tsq
              WHEN 'summary' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'summary', '')) @@ t.tsq
              ELSE false
              END, false));

-- name: NuGetSearchPage :many
-- One page of the matches in rank order, each with its latest visible version and the
-- listed visible versions as one JSON array in ascending order. with_metadata false leaves
-- the metadata of the latest version out (the scan of a caller whose reads are decided row
-- by row needs the keys and the package types only).
WITH terms AS MATERIALIZED (
    SELECT e ->> 'field' AS field, e ->> 'value' AS value, NULLIF(e ->> 'tsq', '')::tsquery AS tsq,
           NULLIF(e ->> 'tsq_id', '')::tsquery AS tsq_id
      FROM jsonb_array_elements(sqlc.arg(terms)::text::jsonb) AS e
), latest AS (
    SELECT DISTINCT ON (nv.lower_id)
           nv.version_id, nv.lower_id, nv.declared_id, nv.sort_key, nv.package_types, nv.search_doc, nv.metadata,
           nv.is_prerelease, nv.is_semver2, nv.published_at, pv.version AS lower_version, pv.display_version,
           pk.display_name
      FROM nuget_version nv
      JOIN package_versions pv ON pv.id = nv.version_id
      JOIN packages pk ON pk.id = pv.package_id
     WHERE nv.repository_id = sqlc.arg(repository_id)::bigint
       AND nv.listed AND pv.trashed_at IS NULL
       AND (sqlc.arg(prerelease)::boolean OR NOT nv.is_prerelease)
       AND (sqlc.arg(semver2)::boolean OR NOT nv.is_semver2)
     ORDER BY nv.lower_id, nv.sort_key DESC
), ranked AS (
    SELECT l.*,
           COALESCE((SELECT min(CASE
                          WHEN l.lower_id = t.value THEN 0
                          WHEN starts_with(l.lower_id, t.value) THEN 1
                          WHEN strpos(l.lower_id, t.value) > 0 OR COALESCE(l.search_doc @@ t.tsq_id, false) THEN 2
                          ELSE 3 END)
                       FROM terms t WHERE t.field = ''), 3) AS tier
      FROM latest l
     WHERE (sqlc.arg(package_type)::text = '' OR sqlc.arg(package_type)::text = ANY (l.package_types))
       AND NOT EXISTS (
           SELECT 1 FROM terms t
            WHERE NOT COALESCE(CASE t.field
                  WHEN '' THEN strpos(l.lower_id, t.value) > 0 OR l.search_doc @@ t.tsq
                  WHEN 'id' THEN strpos(l.lower_id, t.value) > 0
                  WHEN 'packageid' THEN l.lower_id = t.value
                  WHEN 'version' THEN t.value <> '' AND EXISTS (
                       SELECT 1 FROM nuget_version v JOIN package_versions p ON p.id = v.version_id
                        WHERE v.repository_id = sqlc.arg(repository_id)::bigint AND v.lower_id = l.lower_id
                          AND v.listed AND p.trashed_at IS NULL AND p.version = t.value
                          AND (sqlc.arg(prerelease)::boolean OR NOT v.is_prerelease)
                          AND (sqlc.arg(semver2)::boolean OR NOT v.is_semver2))
                  WHEN 'title' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'title', '')) @@ t.tsq
                  WHEN 'tags' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'tags', '')) @@ t.tsq
                  WHEN 'author' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'authors', '')) @@ t.tsq
                  WHEN 'description' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'description', '')) @@ t.tsq
                  WHEN 'summary' THEN to_tsvector('simple', COALESCE(l.metadata ->> 'summary', '')) @@ t.tsq
                  ELSE false
                  END, false))
     ORDER BY tier, l.lower_id COLLATE "C"
    OFFSET sqlc.arg(row_offset)::bigint LIMIT sqlc.arg(row_limit)::bigint
)
SELECT r.lower_id, r.display_name, r.declared_id, r.version_id, r.lower_version, r.display_version, r.is_prerelease, r.is_semver2,
       to_char(r.published_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS published_at, r.package_types,
       CASE WHEN sqlc.arg(with_metadata)::boolean THEN r.metadata::text ELSE '' END::text AS metadata,
       (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                   'id', v.version_id, 'lower', p.version, 'full', p.display_version, 'declared', v.declared_id,
                   'pre', v.is_prerelease, 'sv2', v.is_semver2, 'at', v.published_at, 'types', v.package_types)
                 ORDER BY v.sort_key), '[]'::jsonb)::text
          FROM nuget_version v JOIN package_versions p ON p.id = v.version_id
         WHERE v.repository_id = sqlc.arg(repository_id)::bigint AND v.lower_id = r.lower_id
           AND v.listed AND p.trashed_at IS NULL
           AND (sqlc.arg(prerelease)::boolean OR NOT v.is_prerelease)
           AND (sqlc.arg(semver2)::boolean OR NOT v.is_semver2))::text AS versions
  FROM ranked r
 ORDER BY r.tier, r.lower_id COLLATE "C";

-- name: NuGetSearchVersions :many
-- The packages holding the listed versions the caller was found to read: the full hit of
-- each, with Latest the highest of those versions and the versions array limited to them.
-- refs is a JSON array of {"lower_id", "lower_version"}. The filters of the query were
-- applied by the scan that produced the refs.
WITH want AS (
    SELECT e ->> 'lower_id' AS lower_id, e ->> 'lower_version' AS lower_version
      FROM jsonb_array_elements(sqlc.arg(refs)::text::jsonb) AS e
), picked AS (
    SELECT nv.version_id, nv.lower_id, nv.declared_id, nv.sort_key, nv.package_types, nv.metadata,
           nv.is_prerelease, nv.is_semver2, nv.published_at, pv.version AS lower_version, pv.display_version,
           pk.display_name
      FROM nuget_version nv
      JOIN package_versions pv ON pv.id = nv.version_id
      JOIN packages pk ON pk.id = pv.package_id
      JOIN want w ON w.lower_id = nv.lower_id AND w.lower_version = pv.version
     WHERE nv.repository_id = sqlc.arg(repository_id)::bigint
       AND nv.listed AND pv.trashed_at IS NULL
), latest AS (
    SELECT DISTINCT ON (k.lower_id) k.*
      FROM picked k
     ORDER BY k.lower_id, k.sort_key DESC
)
SELECT l.lower_id, l.display_name, l.declared_id, l.version_id, l.lower_version, l.display_version, l.is_prerelease, l.is_semver2,
       to_char(l.published_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS published_at, l.package_types, l.metadata::text AS metadata,
       (SELECT COALESCE(jsonb_agg(jsonb_build_object(
                   'id', k.version_id, 'lower', k.lower_version, 'full', k.display_version, 'declared', k.declared_id,
                   'pre', k.is_prerelease, 'sv2', k.is_semver2, 'at', k.published_at, 'types', k.package_types)
                 ORDER BY k.sort_key), '[]'::jsonb)::text
          FROM picked k WHERE k.lower_id = l.lower_id)::text AS versions
  FROM latest l
 ORDER BY l.lower_id;

-- name: NuGetSearchScan :many
-- The scan of a caller whose read is decided per version (Q-NUGET-45, Q-NUGET-74): the terms
-- are tested on the row of every listed visible version, not on the latest alone, because the
-- latest version may be one the caller cannot read. A package is a candidate when some version
-- satisfies every term but the version: terms, and every version: term names one of its listed
-- visible versions. Each element of versions carries m, whether that version satisfies every
-- term but the version: terms; the format then applies the text terms to the latest version
-- the caller may read and the version: terms to the versions the caller may read, so a hidden
-- version decides neither whether a package matches nor what is counted. The rank is the one
-- of NuGetSearchPage, the tier of the latest visible version, so a caller who reads the
-- repository whole and one who reads it row by row see the packages in one order. Keys only:
-- the page is read afterwards with NuGetSearchVersions.
WITH terms AS MATERIALIZED (
    SELECT e ->> 'field' AS field, e ->> 'value' AS value, NULLIF(e ->> 'tsq', '')::tsquery AS tsq,
           NULLIF(e ->> 'tsq_id', '')::tsquery AS tsq_id
      FROM jsonb_array_elements(sqlc.arg(terms)::text::jsonb) AS e
), visible AS (
    SELECT nv.version_id, nv.lower_id, nv.declared_id, nv.sort_key, nv.package_types, nv.search_doc,
           nv.is_prerelease, nv.is_semver2, nv.published_at, pv.version AS lower_version, pv.display_version,
           NOT EXISTS (
               SELECT 1 FROM terms t
                WHERE t.field <> 'version' AND NOT COALESCE(CASE t.field
                      WHEN '' THEN strpos(nv.lower_id, t.value) > 0 OR nv.search_doc @@ t.tsq
                      WHEN 'id' THEN strpos(nv.lower_id, t.value) > 0
                      WHEN 'packageid' THEN nv.lower_id = t.value
                      WHEN 'title' THEN to_tsvector('simple', COALESCE(nv.metadata ->> 'title', '')) @@ t.tsq
                      WHEN 'tags' THEN to_tsvector('simple', COALESCE(nv.metadata ->> 'tags', '')) @@ t.tsq
                      WHEN 'author' THEN to_tsvector('simple', COALESCE(nv.metadata ->> 'authors', '')) @@ t.tsq
                      WHEN 'description' THEN to_tsvector('simple', COALESCE(nv.metadata ->> 'description', '')) @@ t.tsq
                      WHEN 'summary' THEN to_tsvector('simple', COALESCE(nv.metadata ->> 'summary', '')) @@ t.tsq
                      ELSE false
                      END, false)) AS matched
      FROM nuget_version nv
      JOIN package_versions pv ON pv.id = nv.version_id
     WHERE nv.repository_id = sqlc.arg(repository_id)::bigint
       AND nv.listed AND pv.trashed_at IS NULL
       AND (sqlc.arg(prerelease)::boolean OR NOT nv.is_prerelease)
       AND (sqlc.arg(semver2)::boolean OR NOT nv.is_semver2)
), candidates AS (
    SELECT v.lower_id,
           (array_agg(v.search_doc ORDER BY v.sort_key DESC))[1] AS latest_doc,
           array_agg(v.lower_version) AS lower_versions,
           jsonb_agg(jsonb_build_object(
               'id', v.version_id, 'lower', v.lower_version, 'full', v.display_version, 'declared', v.declared_id,
               'pre', v.is_prerelease, 'sv2', v.is_semver2, 'at', v.published_at, 'types', v.package_types, 'm', v.matched)
             ORDER BY v.sort_key) AS versions
      FROM visible v
     GROUP BY v.lower_id
    HAVING bool_or(v.matched)
), ranked AS (
    SELECT c.lower_id, c.versions,
           COALESCE((SELECT min(CASE
                          WHEN c.lower_id = t.value THEN 0
                          WHEN starts_with(c.lower_id, t.value) THEN 1
                          WHEN strpos(c.lower_id, t.value) > 0 OR COALESCE(c.latest_doc @@ t.tsq_id, false) THEN 2
                          ELSE 3 END)
                       FROM terms t WHERE t.field = ''), 3) AS tier
      FROM candidates c
     WHERE NOT EXISTS (
           SELECT 1 FROM terms t
            WHERE t.field = 'version' AND (t.value = '' OR NOT (t.value = ANY (c.lower_versions))))
)
SELECT r.lower_id, r.versions::text AS versions
  FROM ranked r
 ORDER BY r.tier, r.lower_id COLLATE "C"
OFFSET sqlc.arg(row_offset)::bigint LIMIT sqlc.arg(row_limit)::bigint;
