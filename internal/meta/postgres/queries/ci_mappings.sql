-- Queries of the identity mappings (R-CI-009, R-CI-017, R-CI-019, Q-CI-10, Q-CI-15), the statements of
-- work package W3 (docs/developer/ci-oidc.md). Identifiers are read as text and written through
-- text::uuid (R-ARCH-002). Every write runs on the caller's meta.Tx after CiIssuerLock took the
-- issuer's row FOR UPDATE; it increments the issuer's mappings_version (CiIssuerBumpMappings) and
-- notifies the area ci_mappings. The match, grants and options columns hold the wire form of
-- internal/ci/claims (lowerCamelCase JSON). W3 adds its further statements here.

-- name: CiMappingList :many
-- The mappings of an issuer in evaluation order: tier, then ascending priority (R-CI-009).
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE i.name = sqlc.arg(issuer)::text
 ORDER BY m.tier, m.priority;

-- name: CiMappingsOfIssuerID :many
-- The same rows by issuer ID, for the mapping set a replica caches (R-CI-009): the set and the
-- issuer's versions are read in one transaction by the caller.
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE m.issuer_id = sqlc.arg(issuer_id)::text::uuid
 ORDER BY m.tier, m.priority;

-- name: CiMappingByName :many
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE i.name = sqlc.arg(issuer)::text AND m.name = sqlc.arg(name)::text;

-- name: CiMappingCount :one
SELECT count(*) FROM ci_mappings WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid;

-- name: CiMappingInsert :many
-- No row back means the name or the priority of the tier is taken; the caller tells them apart
-- (409 already_exists or priority_taken).
INSERT INTO ci_mappings (id, issuer_id, principal_id, tier, project_id, name, priority, enabled, subject_kinds, match, grants, max_ttl,
                         options, description, created_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(issuer_id)::text::uuid, sqlc.arg(principal_id)::text::uuid, sqlc.arg(tier)::text, NULLIF(sqlc.arg(project_id)::text, '')::uuid, sqlc.arg(name)::text,
        sqlc.arg(priority)::int, sqlc.arg(enabled)::boolean, sqlc.arg(subject_kinds)::text[], sqlc.arg(match)::text::jsonb,
        sqlc.arg(grants)::text::jsonb,
        CASE WHEN sqlc.arg(max_ttl_secs)::bigint > 0 THEN make_interval(secs => sqlc.arg(max_ttl_secs)::bigint::float8) END,
        sqlc.arg(options)::text::jsonb, sqlc.arg(description)::text, sqlc.arg(created_by)::text)
ON CONFLICT DO NOTHING
RETURNING id::text AS id, version;

-- name: CiMappingDelete :exec
DELETE FROM ci_mappings WHERE id = sqlc.arg(id)::text::uuid;

-- name: CiIssuerBumpMappings :one
-- Increments the issuer's mappings_version in the transaction of every mapping write (R-CI-009,
-- Q-CI-15); the exchange compares it under its FOR SHARE re-read.
UPDATE ci_issuers SET mappings_version = mappings_version + 1 WHERE id = sqlc.arg(id)::text::uuid
RETURNING mappings_version;

-- name: CiMappingPrincipalInsert :one
-- The mapping's principal (R-CI-009, Q-CI-10): kind ci_mapping, name <issuer>/<mapping>.
INSERT INTO principal (id, kind, name, created_by)
VALUES (sqlc.arg(id)::text::uuid, 'ci_mapping', sqlc.arg(name)::text, NULLIF(sqlc.arg(actor)::text, '')::uuid)
RETURNING id::text AS id;

-- name: CiMappingPrincipalSetStatus :exec
-- enabled mirrors to the principal's status (R-CI-017): active or disabled.
UPDATE principal SET status = sqlc.arg(status)::text WHERE id = sqlc.arg(id)::text::uuid AND kind = 'ci_mapping';

-- name: CiMappingPrincipalDelete :exec
-- Policy entries, role bindings and owner entries go with the principal through their foreign keys
-- (R-AUTHZ-002); a mapping created later under the same name is a new principal.
DELETE FROM principal WHERE id = sqlc.arg(id)::text::uuid AND kind = 'ci_mapping';

-- name: CiMappingListOfProject :many
-- The project's mappings of every issuer (Q-CI-26), in evaluation order of each issuer.
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier,
       COALESCE(m.project_id::text, '')::text AS project_id, COALESCE(m.delegation_id::text, '')::text AS delegation_id,
       m.name, m.priority, m.enabled, m.subject_kinds, m.match, m.grants,
       COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE m.tier = 'project:' || sqlc.arg(project_key)::text
 ORDER BY i.name, m.priority;

-- name: CiMappingCountOfProject :one
SELECT count(*) FROM ci_mappings WHERE project_id = sqlc.arg(project_id)::text::uuid;
