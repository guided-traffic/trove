-- Queries of namespace claims (R-REPO-023, R-REPO-048). A claim is a row of
-- namespace_claims; the claims of a repository are one revisioned resource,
-- repositories.claims_version.

-- name: ListNamespaceClaims :many
-- Every claim of the installation with the format of its repository: the input
-- of the per-replica claim index.
SELECT c.repo_id, r.format, c.pattern, c.source, c.scope
  FROM namespace_claims c
  JOIN repositories r ON r.id = c.repo_id
 ORDER BY c.repo_id, c.pattern COLLATE "C";

-- name: ListRepositoryClaims :many
SELECT pattern, source, scope, created_at
  FROM namespace_claims
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY pattern COLLATE "C";

-- name: CountRepositoryClaims :one
SELECT count(*)::bigint FROM namespace_claims WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: ListOtherClaims :many
-- The claims of the other repositories of a format with the two facts that decide
-- whether one is in effect together with a repository: whether the two share a
-- virtual repository, and whether the repository is a member of any (a global
-- claim is in effect in every virtual repository, so it matters only to a
-- repository some virtual repository contains). R-REPO-023, R-REPO-048.
SELECT c.repo_id, r.key AS repo_key, c.pattern, c.scope,
       EXISTS (SELECT 1
                 FROM virtual_members a
                 JOIN virtual_members b ON b.virtual_id = a.virtual_id
                WHERE a.member_id = c.repo_id AND b.member_id = sqlc.arg(repo_id)::bigint) AS shares_virtual,
       EXISTS (SELECT 1 FROM virtual_members WHERE member_id = sqlc.arg(repo_id)::bigint) AS in_virtual
  FROM namespace_claims c
  JOIN repositories r ON r.id = c.repo_id
 WHERE r.format = sqlc.arg(format)::text AND c.repo_id <> sqlc.arg(repo_id)::bigint
 ORDER BY r.key, c.pattern COLLATE "C";

-- name: DeleteClaim :exec
DELETE FROM namespace_claims
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND pattern = sqlc.arg(pattern)::text;

-- name: UpsertManualClaim :exec
-- A manual claim replaces an automatic claim of the same pattern: the pattern
-- is then a decision of a person (Q-REPO-106). An unchanged manual claim is not
-- written, so it keeps its creation time.
INSERT INTO namespace_claims (repo_id, pattern, source, scope, created_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(pattern)::text, 'manual', sqlc.arg(scope)::text,
        NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (repo_id, pattern) DO UPDATE
   SET source = 'manual', scope = EXCLUDED.scope, created_by = EXCLUDED.created_by, created_at = now()
 WHERE namespace_claims.source <> 'manual' OR namespace_claims.scope <> EXCLUDED.scope;

-- name: InsertAutoClaim :execrows
-- The auto claim of a first publish: an existing claim of the pattern stays
-- as it is, so a second publish of the name changes nothing.
INSERT INTO namespace_claims (repo_id, pattern, source, scope, created_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(pattern)::text, 'auto', 'members', NULLIF(sqlc.arg(actor)::text, '')::uuid)
ON CONFLICT (repo_id, pattern) DO NOTHING;

-- name: ListClaimingRepositories :many
-- Which of the repositories hold at least one claim (the warning
-- dependency_confusion_unprotected of R-REPO-023).
SELECT DISTINCT repo_id FROM namespace_claims WHERE repo_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: BumpClaimsVersion :one
UPDATE repositories
   SET claims_version = claims_version + 1
 WHERE id = sqlc.arg(id)::bigint
RETURNING claims_version;
