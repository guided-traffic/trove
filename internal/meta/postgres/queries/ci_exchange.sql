-- Queries of the token exchange (R-CI-007, R-CI-018, R-CI-020), the statements of work package W4
-- (docs/developer/ci-oidc.md). They run on the exchange's one transaction: the re-read of the matched
-- mappings FOR SHARE (the issuer row is re-read by ci_issuers.sql CiIssuerByIDShare, the issuer service's
-- LockShareTx), the replay upsert, the restore cut-off, the issuance record. The token row is written
-- through internal/auth/token (IssueCITx), not from here.

-- name: CiExchangeLockMappings :many
-- The matched mappings, re-read FOR SHARE: still enabled, still at the version the evaluation used.
SELECT id::text AS id, enabled, version
  FROM ci_mappings WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) FOR SHARE;

-- name: CiExchangeUseJTI :one
-- The replay set (R-CI-018, Q-CI-13): the atomic upsert that serialises concurrent exchanges of one
-- jti on its row. exchange_count > 1 with single use on: the caller rolls back and rejects.
INSERT INTO ci_subject_token_uses AS u (issuer_id, jti, first_exchanged_at, last_exchanged_at, exchange_count, subject_expires_at)
VALUES (sqlc.arg(issuer_id)::text::uuid, sqlc.arg(jti)::text, now(), now(), 1, to_timestamp(sqlc.arg(expires_unix)::bigint))
ON CONFLICT (issuer_id, jti)
DO UPDATE SET exchange_count = u.exchange_count + 1, last_exchanged_at = now()
RETURNING exchange_count;

-- name: CiExchangeInsertIssuance :exec
-- The issuance record (R-CI-020), in the transaction that inserts the token row. claims and grants
-- are the allowlisted snapshot and the frozen bundle (internal/ci/claims.Frozen).
INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, jti, project_id, project_path,
                                namespace_id, pipeline_id, job_id, ref, ref_type, ref_protected, pipeline_source, environment,
                                sha, user_id, user_login, claims, grants, requested_scope, granted_scope, cross_project,
                                issued_at, expires_at, client_ip, user_agent)
VALUES (sqlc.arg(token_id)::text::uuid, sqlc.arg(issuer_id)::text::uuid, sqlc.arg(mapping_ids)::text[]::uuid[], sqlc.arg(mapping_names)::text[],
        sqlc.arg(subject_kind)::text, NULLIF(sqlc.arg(jti)::text, ''), sqlc.arg(project_id)::text, NULLIF(sqlc.arg(project_path)::text, ''),
        NULLIF(sqlc.arg(namespace_id)::text, ''), NULLIF(sqlc.arg(pipeline_id)::text, ''), sqlc.arg(job_id)::text, NULLIF(sqlc.arg(ref)::text, ''),
        NULLIF(sqlc.arg(ref_type)::text, ''), NULLIF(sqlc.arg(ref_protected)::text, '')::boolean, NULLIF(sqlc.arg(pipeline_source)::text, ''),
        NULLIF(sqlc.arg(environment)::text, ''), NULLIF(sqlc.arg(sha)::text, ''), NULLIF(sqlc.arg(user_id)::text, ''),
        NULLIF(sqlc.arg(user_login)::text, ''), sqlc.arg(claims)::text::jsonb, sqlc.arg(grants)::text::jsonb,
        NULLIF(sqlc.arg(requested_scope)::text, ''), sqlc.arg(granted_scope)::text, sqlc.arg(cross_project)::boolean, now(),
        to_timestamp(sqlc.arg(expires_us)::bigint / 1000000.0), NULLIF(sqlc.arg(client_ip)::text, '')::inet,
        NULLIF(sqlc.arg(user_agent)::text, ''));

-- name: CiExchangeClock :one
-- The database clock of the transaction and the authorization epoch (Q-CI-47): now() is the instant the token row's
-- expiry counts from (CiTokenInsert), so a contribution's own expiry, now() plus its window, never lies after the
-- token's; the epoch is what the evaluator compares its grant index with before it drops the contribution of a
-- mapping the index does not hold as active. Read after the issuer row FOR SHARE: every mapping write of the issuer
-- committed before it, with its notification of the area authz.
SELECT (extract(epoch FROM now()) * 1000000)::bigint AS now_us,
       COALESCE((SELECT epoch FROM cluster_epoch WHERE area = 'authz'), 0)::bigint AS authz_epoch;

-- name: CiExchangeRestoredAt :one
-- The restore cut-off of a single-use issuer (R-CI-018, Q-CI-13): the time the last restore finished, the
-- system_state key restored_at (R-BAK-022), in microseconds; 0 before the first restore. Read in the exchange's
-- transaction, never kept by a replica, so it holds on a replica that outlived a post-restore.
SELECT COALESCE((SELECT (extract(epoch FROM (value #>> '{}')::timestamptz) * 1000000)::bigint
                   FROM system_state WHERE key = 'restored_at'), 0)::bigint AS restored_us;
