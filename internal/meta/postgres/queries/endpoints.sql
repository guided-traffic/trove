-- Queries of the endpoint exposure rules (R-EP-012 to R-EP-016) and of the
-- endpoint rate-limit classes (R-EP-015).
--
-- Identifiers are read as text and written through text::uuid, instants are read
-- as microseconds since the epoch and the pattern dimensions travel as JSON text,
-- so that the domain package never imports the driver types (R-ARCH-002). The
-- writes run on the caller's meta.Tx and notify the change area authz there; the
-- build query runs inside the one REPEATABLE READ READ ONLY transaction of the
-- grant index (R-AUTHZ-028).

-- name: EndpointsExposureBuild :many
-- Every rule of an active repository, the input of the exposure ceiling in the
-- grant index. A repository that is being deleted exposes nothing: its rules are
-- left out like the repository itself (AuthzBuildRepositories).
SELECT r.id::text AS id, r.endpoint, r.name, r.repo_id, r.enabled,
       r.packages::text AS packages, r.paths::text AS paths, r.versions::text AS versions,
       r.prereleases, r.dist_tags::text AS dist_tags, r.revision
  FROM endpoint_exposure_rule r
  JOIN repositories p ON p.id = r.repo_id AND p.lifecycle = 'active'
 ORDER BY r.endpoint, r.repo_id, r.name;

-- name: EndpointsListRules :many
-- The rules of one endpoint by name, with the repository they belong to. An
-- endpoint holds at most 1,000 rules (R-EP-012), so the list handler pages over
-- this one bounded read.
SELECT r.id::text AS id, r.endpoint, r.name, r.description, r.repo_id, p.key AS repository, p.format, p.class,
       r.enabled, r.packages::text AS packages, r.paths::text AS paths, r.versions::text AS versions,
       r.prereleases, r.dist_tags::text AS dist_tags, r.revision,
       (extract(epoch FROM r.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM r.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name
  FROM endpoint_exposure_rule r
  JOIN repositories p ON p.id = r.repo_id
  LEFT JOIN principal cb ON cb.id = r.created_by
  LEFT JOIN principal ub ON ub.id = r.updated_by
 WHERE r.endpoint = sqlc.arg(endpoint)::text
 ORDER BY r.name, r.id;

-- name: EndpointsGetRule :many
SELECT r.id::text AS id, r.endpoint, r.name, r.description, r.repo_id, p.key AS repository, p.format, p.class,
       r.enabled, r.packages::text AS packages, r.paths::text AS paths, r.versions::text AS versions,
       r.prereleases, r.dist_tags::text AS dist_tags, r.revision,
       (extract(epoch FROM r.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM r.updated_at) * 1000000)::bigint AS updated_us,
       COALESCE(cb.kind, '')::text AS created_by_kind, COALESCE(cb.name, '')::text AS created_by_name,
       COALESCE(ub.kind, '')::text AS updated_by_kind, COALESCE(ub.name, '')::text AS updated_by_name
  FROM endpoint_exposure_rule r
  JOIN repositories p ON p.id = r.repo_id
  LEFT JOIN principal cb ON cb.id = r.created_by
  LEFT JOIN principal ub ON ub.id = r.updated_by
 WHERE r.endpoint = sqlc.arg(endpoint)::text AND r.name = sqlc.arg(name)::text;

-- name: EndpointsRepositoryByKey :many
-- The repository a rule names, resolved in the transaction of the write: an
-- active repository only, so a key that is being deleted is unknown.
SELECT id, key, format, class FROM repositories WHERE key = sqlc.arg(key)::text AND lifecycle = 'active';

-- name: EndpointsLockRuleCount :one
-- Counts the rules of an endpoint and locks them, so that two creates near the
-- limit of 1,000 rules per endpoint are serialised (R-EP-012).
SELECT count(*)::bigint AS rules
  FROM (SELECT 1 FROM endpoint_exposure_rule WHERE endpoint = sqlc.arg(endpoint)::text FOR UPDATE) locked;

-- name: EndpointsInsertRule :many
-- No row back means the name exists on the endpoint (409).
INSERT INTO endpoint_exposure_rule (id, endpoint, name, description, repo_id, enabled, packages, paths, versions,
                                    prereleases, dist_tags, created_by, updated_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(endpoint)::text, sqlc.arg(name)::text, sqlc.arg(description)::text,
        sqlc.arg(repo_id)::bigint, sqlc.arg(enabled)::boolean, sqlc.arg(packages)::text::jsonb, sqlc.arg(paths)::text::jsonb,
        sqlc.arg(versions)::text::jsonb, sqlc.arg(prereleases)::boolean, sqlc.arg(dist_tags)::text::jsonb,
        NULLIF(sqlc.arg(actor)::text, '')::uuid, NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (endpoint, name) DO NOTHING
RETURNING id::text AS id, revision;

-- name: EndpointsUpdateRule :many
-- The replacement that carries the optimistic check: a negative expected revision
-- is the deliberate blind write (If-Match: *); a revision that does not match
-- returns no row. Name, endpoint and repository are immutable.
UPDATE endpoint_exposure_rule
   SET description = sqlc.arg(description)::text, enabled = sqlc.arg(enabled)::boolean,
       packages = sqlc.arg(packages)::text::jsonb, paths = sqlc.arg(paths)::text::jsonb,
       versions = sqlc.arg(versions)::text::jsonb, prereleases = sqlc.arg(prereleases)::boolean,
       dist_tags = sqlc.arg(dist_tags)::text::jsonb,
       revision = revision + 1, updated_at = now(), updated_by = NULLIF(sqlc.arg(actor)::text, '')::uuid
 WHERE endpoint = sqlc.arg(endpoint)::text AND name = sqlc.arg(name)::text
   AND (sqlc.arg(expected)::bigint < 0 OR revision = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, revision;

-- name: EndpointsDeleteRule :many
DELETE FROM endpoint_exposure_rule
 WHERE endpoint = sqlc.arg(endpoint)::text AND name = sqlc.arg(name)::text
   AND (sqlc.arg(expected)::bigint < 0 OR revision = sqlc.arg(expected)::bigint)
RETURNING id::text AS id, revision;

-- name: EndpointsRuleRevision :many
SELECT revision FROM endpoint_exposure_rule WHERE endpoint = sqlc.arg(endpoint)::text AND name = sqlc.arg(name)::text;

-- name: EndpointsRuleCounts :many
-- The rule counts per endpoint name, the configured ones and the inert ones
-- (R-EP-016, H-58).
SELECT endpoint, count(*)::bigint AS rules, (count(*) FILTER (WHERE enabled))::bigint AS enabled
  FROM endpoint_exposure_rule
 GROUP BY endpoint
 ORDER BY endpoint;

-- name: EndpointsCountServingReplicas :one
-- The n of the fallback share of an endpoint rate-limit class (R-EP-015): the live
-- replicas that serve the endpoints of this replica's pool, which are the web
-- replicas of the same pool and every replica of the development role all.
SELECT count(*)::int AS live
  FROM replica
 WHERE heartbeat_at >= now() - make_interval(secs => sqlc.arg(stale_secs)::float8)
   AND ('all' = ANY(roles) OR ('web' = ANY(roles) AND COALESCE(pool, '') = sqlc.arg(pool)::text));
