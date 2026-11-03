-- Queries of the identity mappings (R-CI-009, R-CI-017, R-CI-019, Q-CI-10, Q-CI-15), the statements of
-- work package W3 (docs/developer/ci-oidc.md). Identifiers are read as text and written through
-- text::uuid (R-ARCH-002). Every write runs on the caller's meta.Tx after CiIssuerLock took the
-- issuer's row FOR UPDATE; it increments the issuer's mappings_version (CiIssuerBumpMappings) and
-- notifies the area ci_mappings. The match, grants and options columns hold the wire form of
-- internal/ci/claims (lowerCamelCase JSON). W3 adds its further statements here.

-- name: CiMappingList :many
-- The mappings of an issuer in evaluation order: the system tier first, then the projects' tiers by key, each by ascending priority (R-CI-009).
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE i.name = sqlc.arg(issuer)::text
 ORDER BY (m.tier <> 'system'), m.tier, m.priority;

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
 ORDER BY (m.tier <> 'system'), m.tier, m.priority;

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

-- name: CiMappingCountOfTier :one
-- The mappings of one tier of an issuer: a project's tier holds at most ci.mappings.max_per_project of them, so no
-- project fills the issuer's ci.mappings.max_per_issuer alone (Q-CI-49).
SELECT count(*) FROM ci_mappings WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid AND tier = sqlc.arg(tier)::text;

-- name: CiMappingsActive :many
-- The mappings whose contributions a CI token still carries at use time (Q-CI-47): enabled, with an active principal.
-- Read by the build of the grant index in its snapshot (internal/authz); a contribution of a mapping that is not
-- listed is dropped by the evaluator once the index is at least as fresh as the token's exchange.
SELECT m.id::text AS id
  FROM ci_mappings m JOIN principal p ON p.id = m.principal_id
 WHERE m.enabled AND p.status = 'active'
 ORDER BY m.id;

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

-- name: CiMappingVersions :many
-- The issuer's two counters, read before the mapping rows of a set: a write that lands between the
-- two statements leaves the set newer than its label, so the exchange compares, finds a mismatch and
-- evaluates again from PostgreSQL (R-CI-009, Q-CI-15). The other order would label a set with a
-- counter newer than its content.
SELECT version, mappings_version FROM ci_issuers WHERE id = sqlc.arg(issuer_id)::text::uuid;

-- name: CiMappingsOfIssuerIDShare :many
-- The set the exchange re-reads inside its transaction (R-CI-007): every mapping row FOR SHARE, so a
-- concurrent write of one of them waits for the exchange.
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE m.issuer_id = sqlc.arg(issuer_id)::text::uuid
 ORDER BY (m.tier <> 'system'), m.tier, m.priority
   FOR SHARE OF m;

-- name: CiMappingUpdate :many
-- The full replacement of a mapping row after the issuer row was locked; name, tier and project are
-- immutable. version moves by one.
UPDATE ci_mappings
   SET priority = sqlc.arg(priority)::int, enabled = sqlc.arg(enabled)::boolean, subject_kinds = sqlc.arg(subject_kinds)::text[],
       match = sqlc.arg(match)::text::jsonb, grants = sqlc.arg(grants)::text::jsonb,
       max_ttl = CASE WHEN sqlc.arg(max_ttl_secs)::bigint > 0 THEN make_interval(secs => sqlc.arg(max_ttl_secs)::bigint::float8) END,
       options = sqlc.arg(options)::text::jsonb, description = sqlc.arg(description)::text,
       version = version + 1, updated_at = now()
 WHERE id = sqlc.arg(id)::text::uuid
RETURNING version;

-- name: CiMappingPriorityHolder :many
-- The mapping, other than id, that holds the priority in the issuer's tier: the 409 priority_taken.
SELECT name FROM ci_mappings
 WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid AND tier = sqlc.arg(tier)::text AND priority = sqlc.arg(priority)::int
   AND id <> sqlc.arg(id)::text::uuid;

-- name: CiMappingHasIssuances :many
-- Whether an unexpired token was issued through the mapping: a token exists only with its issuance record
-- (R-CI-020), so no row means nothing to revoke. Bounded to the unexpired records (the index ci_issuances_expiry),
-- not to every record of the issuer within the retention, because the caller holds the issuer row FOR UPDATE; an
-- expired token is not revocable, so the rows left out are ones the revocation would skip (Q-CI-53).
SELECT 1::int AS one FROM ci_token_issuances
 WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid AND expires_at > now() AND sqlc.arg(id)::text::uuid = ANY(mapping_ids) LIMIT 1;

-- name: CiMappingPolicyBindings :many
-- The policies whose principal list names one of the mapping principals (R-CI-013: the binding is
-- stored once, in the policy): the principal, the policy, the actions the entry gives it, the ID of
-- the policy and the key of its project (empty for a global policy).
SELECT e.principal_id::text AS principal_id, pol.name AS policy, e.actions, pol.id::text AS policy_id, COALESCE(pr.key, '')::text AS project
  FROM authz_policy_entry e
  JOIN authz_policy pol ON pol.id = e.policy_id
  LEFT JOIN project pr ON pr.id = pol.project_id
 WHERE e.principal_id = ANY(sqlc.arg(principal_ids)::text[]::uuid[])
UNION ALL
SELECT e.principal_id::text, pol.name, e.actions, pol.id::text, COALESCE(pr.key, '')::text
  FROM authz_project_policy_entry e
  JOIN authz_policy pol ON pol.id = e.policy_id
  LEFT JOIN project pr ON pr.id = pol.project_id
 WHERE e.principal_id = ANY(sqlc.arg(principal_ids)::text[]::uuid[])
 ORDER BY 1, 2;

-- name: CiPoliciesLock :many
-- The named policies, locked in name order so that two writers never wait for each other crosswise;
-- project is the key of the project of a project policy, empty for a global one.
SELECT p.id::text AS id, p.name, COALESCE(pr.key, '')::text AS project, COALESCE(p.project_id::text, '')::text AS project_id
  FROM authz_policy p LEFT JOIN project pr ON pr.id = p.project_id
 WHERE p.name = ANY(sqlc.arg(names)::text[])
 ORDER BY p.name
   FOR UPDATE OF p;

-- name: CiPolicyEntryDelete :exec
-- Removes the entry of a principal from a policy of either kind.
WITH g AS (DELETE FROM authz_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid)
DELETE FROM authz_project_policy_entry WHERE policy_id = sqlc.arg(policy_id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: CiPolicyTouch :exec
-- Moves the version of a policy whose principal list changed, so its ETag changes (R-AUTHZ-028).
UPDATE authz_policy SET version = version + 1, updated_at = now() WHERE id = sqlc.arg(id)::text::uuid;

-- name: CiGroupsFound :many
SELECT p.name FROM principal p JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = 'group' AND p.name = ANY(sqlc.arg(names)::text[]);

-- name: CiGroupActions :many
-- Every action the named groups hold in any policy, for the write rules of R-CI-010 and R-CI-011.
SELECT DISTINCT x.action::text AS action FROM (
    SELECT unnest(e.actions) AS action FROM authz_policy_entry e JOIN principal p ON p.id = e.principal_id
     WHERE p.kind = 'group' AND p.name = ANY(sqlc.arg(names)::text[])
    UNION ALL
    SELECT unnest(e.actions) FROM authz_project_policy_entry e JOIN principal p ON p.id = e.principal_id
     WHERE p.kind = 'group' AND p.name = ANY(sqlc.arg(names)::text[])
) x ORDER BY 1;

-- name: CiProjectLock :many
-- The project row, locked: a project delete counts the project's CI mappings under the same lock, so a
-- create and a delete never miss each other. No row back means the project does not exist.
SELECT id::text AS id FROM project WHERE key = sqlc.arg(key)::text FOR UPDATE;

-- name: CiProjectID :many
SELECT id::text AS id FROM project WHERE key = sqlc.arg(key)::text;

-- name: CiProjectRepositories :many
-- Which of the keys are active repositories of the project.
SELECT key FROM repositories
 WHERE project_id = sqlc.arg(project_id)::text::uuid AND lifecycle = 'active' AND key = ANY(sqlc.arg(keys)::text[]);

-- name: CiIssuerAudiences :many
-- The first allowed audience and the kind of an issuer, for the snippet.
SELECT i.id::text AS id, i.allowed_audiences, i.kind, i.enabled FROM ci_issuers i WHERE i.name = sqlc.arg(name)::text;

-- name: CiMappingByID :many
SELECT m.id::text AS id, m.issuer_id::text AS issuer_id, i.name AS issuer, m.principal_id::text AS principal_id, m.tier, COALESCE(m.project_id::text, '')::text AS project_id,
       COALESCE(m.delegation_id::text, '')::text AS delegation_id, m.name, m.priority, m.enabled, m.subject_kinds,
       m.match, m.grants, COALESCE(extract(epoch FROM m.max_ttl), 0)::bigint AS max_ttl_secs, m.options, m.description, m.version,
       m.created_by, (extract(epoch FROM m.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM m.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_mappings m JOIN ci_issuers i ON i.id = m.issuer_id
 WHERE m.id = sqlc.arg(id)::text::uuid;

-- name: CiRepositoryFormats :many
-- The format of each of the keys that is an active repository, for the snippet: it configures the repositories
-- of the client's format only.
SELECT key, format FROM repositories WHERE lifecycle = 'active' AND key = ANY(sqlc.arg(keys)::text[]);
