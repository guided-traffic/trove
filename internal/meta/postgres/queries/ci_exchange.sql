-- Queries of the token exchange (R-CI-007, R-CI-018, R-CI-020), the statements of work package W4
-- (docs/developer/ci-oidc.md). They run on the exchange's one transaction: the re-read of the issuer
-- and of the matched mappings FOR SHARE, the replay upsert, the restore cut-off (packages.sql
-- ReadRestoredAt), the issuance record. W4 adds its further statements here; the token row is
-- written through internal/auth/token (IssueCITx), not from here.

-- name: CiExchangeLockIssuer :many
-- The exchange's re-read (R-CI-007): the issuer row FOR SHARE, so a write of the issuer or of one of
-- its mappings (which takes FOR UPDATE) waits for the exchange and the exchange for it. The versions
-- are compared with what the evaluation used; the settings that steps 5 to 9 of R-CI-004 read come
-- with it, for the second validation after a change of the issuer's version.
SELECT id::text AS id, name, issuer, enabled, version, mappings_version, allowed_audiences, exclusive_audience,
       allowed_algorithms, allowed_typ, allow_missing_typ, extract(epoch FROM clock_skew)::bigint AS clock_skew_secs,
       required_claims, pin_claims, single_use_subject_tokens, COALESCE(extract(epoch FROM max_ttl), 0)::bigint AS max_ttl_secs
  FROM ci_issuers WHERE id = sqlc.arg(id)::text::uuid FOR SHARE;

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
