-- The suggestions of the search field (R-API-060 to R-API-064): internal/packages/view (packages), internal/auth/principal (users
-- and groups) and internal/api/v1 (projects). Each statement reads a bounded number of rows; the repositories are matched over the
-- request's grant index and need no statement (Q-API-273).

-- name: SearchCustomPlans :exec
-- The package statements of a request run after this one, in its transaction: each is planned for the text it gets. A prepared
-- statement may otherwise take a generic plan after its fifth run, and a generic plan cannot know whether the text makes the
-- word index or the name order the cheaper read; it chose a walk over every package for a rare word (R-API-064).
SET LOCAL plan_cache_mode = force_custom_plan;

-- name: SearchPackagesByPrefix :many
-- Statement A of R-API-064: the packages whose lower-cased name starts with the text, in the repositories the caller reads whole
-- or in part, in the order of packages_name_lower. low is the text and high the least string above every string that starts with
-- it: in byte order a name starts with the text exactly when it lies in [low, high), and the bounds are values, not a pattern, so
-- the B-tree serves a generic plan too and no character of the text is an operator. A package needs a live version its format
-- serves (withheld are the versions the formats do not serve, as on GET /api/v1/packages).
SELECT p.id, p.repo_id, (p.name COLLATE "C")::text AS name, p.display_name, r.format
  FROM packages p
  JOIN repositories r ON r.id = p.repo_id
 WHERE p.repo_id = ANY(sqlc.arg(repos)::bigint[])
   AND lower(p.name) COLLATE "C" >= sqlc.arg(low)::text
   AND lower(p.name) COLLATE "C" < sqlc.arg(high)::text
   AND EXISTS (SELECT 1 FROM package_version_listing pv
                WHERE pv.package_id = p.id AND pv.trashed_at IS NULL AND NOT (pv.id = ANY(sqlc.arg(withheld)::bigint[])))
 ORDER BY lower(p.name) COLLATE "C", r.format, p.repo_id
 LIMIT sqlc.arg(max_rows)::int;

-- name: SearchPackagesByWords :many
-- Statement B of R-API-064: the packages each of whose query words begins a word of the name (packages_name_words), in the same
-- repositories, without the packages of statement A (skip). query is a to_tsquery text built from runs of a to z and 0 to 9 of
-- the text only ('w1':* & 'w2':*), so no operator of the tsquery syntax reaches it from a request. It runs after
-- SearchCustomPlans: planned for its text, it reads packages_name_words for a rare word and may walk packages_name_lower for a
-- word so common that the first max_rows names in order hold enough matches.
SELECT p.id, p.repo_id, (p.name COLLATE "C")::text AS name, p.display_name, r.format
  FROM packages p
  JOIN repositories r ON r.id = p.repo_id
 WHERE p.repo_id = ANY(sqlc.arg(repos)::bigint[])
   AND p.name_words @@ to_tsquery('simple'::regconfig, sqlc.arg(query)::text)
   AND NOT (p.id = ANY(sqlc.arg(skip)::bigint[]))
   AND EXISTS (SELECT 1 FROM package_version_listing pv
                WHERE pv.package_id = p.id AND pv.trashed_at IS NULL AND NOT (pv.id = ANY(sqlc.arg(withheld)::bigint[])))
 ORDER BY lower(p.name) COLLATE "C", r.format, p.repo_id
 LIMIT sqlc.arg(max_rows)::int;

-- name: SearchProjects :many
-- Every project with what the suggestions show and decide: the key, the display name and the level (R-AUTHZ-035 rule 8). The
-- caller filters to the projects it may see.
SELECT p.key, p.display_name, p.visibility
  FROM project p
 ORDER BY p.key COLLATE "C";

-- name: SearchUsers :many
-- The users of which every term is part of the lower-cased username or display name, best first by the ranks of R-API-062: 0 the
-- username equals the text, 1 it starts with it, 2 the display name or one of its words starts with it, 3 any other; then by
-- username. At most max_rows; the caller asks for one more than it shows.
SELECT p.id::text AS id, p.name AS username, u.display_name, (p.status = 'disabled')::boolean AS disabled
  FROM principal p
  JOIN app_user u ON u.principal_id = p.id
 WHERE p.kind = 'user'
   AND NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(terms)::text[]) t
                    WHERE strpos(lower(p.name), t) = 0 AND strpos(lower(coalesce(u.display_name, '')), t) = 0)
 ORDER BY CASE WHEN lower(p.name) = sqlc.arg(text)::text THEN 0
               WHEN starts_with(lower(p.name), sqlc.arg(text)::text) THEN 1
               WHEN strpos(' ' || regexp_replace(lower(coalesce(u.display_name, '')), '\s+', ' ', 'g'), ' ' || sqlc.arg(text)::text) > 0 THEN 2
               ELSE 3 END,
          p.name COLLATE "C"
 LIMIT sqlc.arg(max_rows)::int;

-- name: SearchGroups :many
-- The groups of which every term is part of the lower-cased name, best first: 0 the name equals the text, 1 it starts with it,
-- 3 any other; then by name. At most max_rows.
SELECT p.name, g.description
  FROM principal p
  JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = 'group'
   AND NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(terms)::text[]) t WHERE strpos(lower(p.name), t) = 0)
 ORDER BY CASE WHEN lower(p.name) = sqlc.arg(text)::text THEN 0
               WHEN starts_with(lower(p.name), sqlc.arg(text)::text) THEN 1
               ELSE 3 END,
          p.name COLLATE "C"
 LIMIT sqlc.arg(max_rows)::int;
