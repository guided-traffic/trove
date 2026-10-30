-- Queries of package ownership (R-AUTHZ-018, R-AUTHZ-019).
--
-- The check and the claim of a write run on the writer's commit transaction; the
-- owner operations run in transactions of their own. Every statement that decides
-- reads the claim row with FOR UPDATE first, so the check and the claim of one
-- write and every owner change of the same name run one after the other. A name
-- without a row has nothing to lock: of two concurrent first publishes the primary
-- key of package_ownership decides (INSERT ... ON CONFLICT DO NOTHING, rule 5).
-- Identifiers travel as text and instants as microseconds, as in authz.sql.

-- name: OwnershipLockState :many
SELECT state FROM package_ownership
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text
   AND package_name = sqlc.arg(package_name)::text
   FOR UPDATE;

-- name: OwnershipState :many
SELECT state FROM package_ownership
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text
   AND package_name = sqlc.arg(package_name)::text;

-- name: OwnershipOwners :many
SELECT o.id::text AS id, o.owner_kind, COALESCE(o.principal_id::text, '')::text AS principal_id,
       COALESCE(p.kind, '')::text AS principal_kind, COALESCE(p.name, '')::text AS principal_name,
       COALESCE(o.ci_issuer_id::text, '')::text AS ci_issuer_id, COALESCE(o.ci_project_id, '')::text AS ci_project_id,
       o.display, o.reason, (extract(epoch FROM o.added_at) * 1000000)::bigint AS added_us,
       COALESCE(ab.kind, '')::text AS added_by_kind, COALESCE(ab.name, '')::text AS added_by_name
  FROM package_owner o
  LEFT JOIN principal p ON p.id = o.principal_id
  LEFT JOIN principal ab ON ab.id = o.added_by
 WHERE o.domain = sqlc.arg(domain)::text AND o.format = sqlc.arg(format)::text
   AND o.package_name = sqlc.arg(package_name)::text
 ORDER BY o.added_at, o.id;

-- name: OwnershipInsertClaim :many
-- No row back means a claim exists: under READ COMMITTED the insert waited for a
-- concurrent one to commit and then did nothing (R-AUTHZ-018 rule 5).
INSERT INTO package_ownership (domain, format, package_name, state, claim_event)
VALUES (sqlc.arg(domain)::text, sqlc.arg(format)::text, sqlc.arg(package_name)::text, 'claimed',
        sqlc.arg(claim_event)::text::uuid)
ON CONFLICT DO NOTHING
RETURNING domain;

-- name: OwnershipReclaim :exec
-- A released name, or a claimed one without owners, claimed again; the caller holds
-- the row lock.
UPDATE package_ownership SET state = 'claimed', claimed_at = now(), claim_event = sqlc.arg(claim_event)::text::uuid
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text
   AND package_name = sqlc.arg(package_name)::text;

-- name: OwnershipRelease :exec
UPDATE package_ownership SET state = 'released'
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text
   AND package_name = sqlc.arg(package_name)::text;

-- name: OwnershipInsertOwner :many
-- No row back means the principal owns the name already (package_owner_unique).
INSERT INTO package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason, added_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(domain)::text, sqlc.arg(format)::text, sqlc.arg(package_name)::text,
        'principal', sqlc.arg(principal_id)::text::uuid, sqlc.arg(display)::text, sqlc.arg(reason)::text,
        NULLIF(sqlc.arg(added_by)::text, '')::uuid)
ON CONFLICT DO NOTHING
RETURNING (extract(epoch FROM added_at) * 1000000)::bigint AS added_us;

-- name: OwnershipDeleteOwner :execrows
DELETE FROM package_owner WHERE id = sqlc.arg(id)::text::uuid;

-- name: OwnershipHasVersions :one
-- Whether the package holds a live version (R-AUTHZ-018 rule 1 and rule 3); the
-- name is the normalized name the generic package rows store.
SELECT EXISTS (
  SELECT 1 FROM packages p JOIN package_versions v ON v.package_id = p.id
   WHERE p.repo_id = sqlc.arg(repo_id)::bigint AND p.name = sqlc.arg(package_name)::text AND v.trashed_at IS NULL
) AS present;

-- name: OwnershipDeleteDomain :execrows
-- The rows of a repository's domain, removed in the transaction that deletes the
-- repository row (Q-AUTHZ-156); the owners go with them by the foreign key.
DELETE FROM package_ownership WHERE domain = sqlc.arg(domain)::text;

-- name: OwnershipOwnedBy :many
-- The names one principal owns, in key order after the cursor (R-AUTHZ-019
-- GET /package-owners). The repository key is resolved for the domain of a
-- repository; a shared domain (R-AUTHZ-020) has none.
SELECT o.domain, o.format, o.package_name, COALESCE(r.key, '')::text AS repository_key, s.state, o.reason,
       (extract(epoch FROM o.added_at) * 1000000)::bigint AS added_us
  FROM package_owner o
  JOIN package_ownership s ON s.domain = o.domain AND s.format = o.format AND s.package_name = o.package_name
  LEFT JOIN repositories r ON r.id = (CASE WHEN o.domain ~ '^repo:[0-9]+$' THEN substr(o.domain, 6)::bigint END)
 WHERE o.principal_id = sqlc.arg(principal_id)::text::uuid
   AND (sqlc.arg(domain)::text = '' OR o.domain = sqlc.arg(domain)::text)
   AND (o.domain, o.format, o.package_name) > (sqlc.arg(after_domain)::text, sqlc.arg(after_format)::text, sqlc.arg(after_name)::text)
 ORDER BY o.domain, o.format, o.package_name
 LIMIT sqlc.arg(max_rows)::int;
