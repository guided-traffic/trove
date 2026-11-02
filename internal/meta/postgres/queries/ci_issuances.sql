-- Queries of the issuance records, the revocation of CI tokens and the two retention tasks (R-CI-017,
-- R-CI-018, R-CI-020, R-CI-030), the statements of work package W5 (docs/developer/ci-oidc.md). The
-- record is written by the exchange (ci_exchange.sql); this file reads it and ends the tokens. Every
-- statement of a revocation runs on the caller's meta.Tx and is followed by the cache invalidation of
-- the area auth with the token hashes (R-AUTH-023). W5 adds its further statements here.

-- name: CiIssuanceList :many
-- Newest first with the token state joined; the filters of R-CI-020 are optional, active is
-- 'true' (unexpired and unrevoked), 'false' (the others) or '' (both); 0 for a time or the cursor is none. A token row deleted by its retention
-- task reads as expired. The cursor is (issued_at, token_id) of the last row of the previous page.
SELECT s.token_id::text AS token_id, i.name AS issuer, s.issuer_id::text AS issuer_id, s.mapping_ids::text[] AS mapping_ids,
       s.mapping_names, s.subject_kind, COALESCE(s.jti, '')::text AS jti, s.project_id, COALESCE(s.project_path, '')::text AS project_path,
       COALESCE(s.namespace_id, '')::text AS namespace_id, COALESCE(s.pipeline_id, '')::text AS pipeline_id, s.job_id,
       COALESCE(s.ref, '')::text AS ref, COALESCE(s.ref_type, '')::text AS ref_type, COALESCE(s.ref_protected::text, '')::text AS ref_protected,
       COALESCE(s.pipeline_source, '')::text AS pipeline_source, COALESCE(s.environment, '')::text AS environment,
       COALESCE(s.sha, '')::text AS sha, COALESCE(s.user_id, '')::text AS user_id, COALESCE(s.user_login, '')::text AS user_login,
       s.claims, s.grants, COALESCE(s.requested_scope, '')::text AS requested_scope, s.granted_scope, s.cross_project,
       (extract(epoch FROM s.issued_at) * 1000000)::bigint AS issued_us, (extract(epoch FROM s.expires_at) * 1000000)::bigint AS expires_us,
       COALESCE(host(s.client_ip), '')::text AS client_ip, COALESCE(s.user_agent, '')::text AS user_agent,
       COALESCE(extract(epoch FROM t.revoked_at) * 1000000, 0)::bigint AS revoked_us
  FROM ci_token_issuances s
  JOIN ci_issuers i ON i.id = s.issuer_id
  LEFT JOIN api_token t ON t.id = s.token_id
 WHERE (sqlc.arg(issuer)::text = '' OR i.name = sqlc.arg(issuer)::text)
   AND (sqlc.arg(project_id)::text = '' OR s.project_id = sqlc.arg(project_id)::text)
   AND (sqlc.arg(project_path)::text = '' OR s.project_path = sqlc.arg(project_path)::text)
   AND (sqlc.arg(pipeline_id)::text = '' OR s.pipeline_id = sqlc.arg(pipeline_id)::text)
   AND (sqlc.arg(job_id)::text = '' OR s.job_id = sqlc.arg(job_id)::text)
   AND (sqlc.arg(mapping)::text = '' OR sqlc.arg(mapping)::text = ANY(s.mapping_names))
   AND (sqlc.arg(active)::text = ''
        OR (sqlc.arg(active)::text = 'true') = (t.id IS NOT NULL AND t.revoked_at IS NULL AND s.expires_at > now()))
   AND (sqlc.arg(from_us)::bigint = 0 OR s.issued_at >= to_timestamp(sqlc.arg(from_us)::bigint / 1000000.0))
   AND (sqlc.arg(to_us)::bigint = 0 OR s.issued_at < to_timestamp(sqlc.arg(to_us)::bigint / 1000000.0))
   AND (sqlc.arg(after_us)::bigint = 0
        OR (s.issued_at, s.token_id) < (to_timestamp(sqlc.arg(after_us)::bigint / 1000000.0), sqlc.arg(after_id)::text::uuid))
 ORDER BY s.issued_at DESC, s.token_id DESC
 LIMIT sqlc.arg(max_rows)::int;

-- name: CiIssuanceTokensToRevoke :many
-- The tokens a bulk revoke would end (R-CI-020): unexpired, unrevoked, matching the filter. The
-- filter has its issuer and at least one of the others, which the caller checked.
SELECT s.token_id::text AS token_id, t.secret_sha256
  FROM ci_token_issuances s
  JOIN ci_issuers i ON i.id = s.issuer_id
  JOIN api_token t ON t.id = s.token_id
 WHERE i.name = sqlc.arg(issuer)::text
   AND t.revoked_at IS NULL AND t.expires_at > now()
   AND (sqlc.arg(project_id)::text = '' OR s.project_id = sqlc.arg(project_id)::text)
   AND (sqlc.arg(pipeline_id)::text = '' OR s.pipeline_id = sqlc.arg(pipeline_id)::text)
   AND (sqlc.arg(job_id)::text = '' OR s.job_id = sqlc.arg(job_id)::text)
   AND (sqlc.arg(mapping)::text = '' OR sqlc.arg(mapping)::text = ANY(s.mapping_names))
 FOR UPDATE OF t;

-- name: CiRevokeTokens :many
-- Ends the tokens of the given IDs (R-CI-017): reason is mapping_deleted, admin or owner as the
-- token table names them. Returns the IDs and the hashes for the cache invalidation.
UPDATE api_token SET revoked_at = now(), revoked_by = NULLIF(sqlc.arg(revoked_by)::text, '')::uuid, revocation_reason = sqlc.arg(reason)::text
 WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND revoked_at IS NULL
RETURNING id::text AS id, secret_sha256;

-- name: CiIssuanceTokensOfMapping :many
-- The unexpired tokens issued through a mapping, by its principal: disabling or deleting the mapping
-- ends them in the same transaction (R-CI-017).
SELECT t.id::text AS id, t.secret_sha256
  FROM api_token t
 WHERE t.principal_id = sqlc.arg(principal_id)::text::uuid AND t.type = 'cit' AND t.revoked_at IS NULL AND t.expires_at > now()
 FOR UPDATE;

-- name: CiIssuanceTokensOfIssuer :many
-- The unexpired tokens issued through an issuer (disable, delete).
SELECT t.id::text AS id, t.secret_sha256
  FROM ci_token_issuances s
  JOIN api_token t ON t.id = s.token_id
 WHERE s.issuer_id = sqlc.arg(issuer_id)::text::uuid AND t.revoked_at IS NULL AND t.expires_at > now()
 FOR UPDATE OF t;

-- name: CiReplayDeleteExpired :execrows
-- ci-replay-cleanup (R-CI-018, R-CI-030): rows whose subject_expires_at + the issuer's clock skew +
-- retention_secs has passed, in the order of the expiry index, at most max_rows per transaction.
DELETE FROM ci_subject_token_uses u
 WHERE (u.issuer_id, u.jti) IN (
        SELECT d.issuer_id, d.jti
          FROM ci_subject_token_uses d JOIN ci_issuers i ON i.id = d.issuer_id
         WHERE d.subject_expires_at + i.clock_skew + make_interval(secs => sqlc.arg(retention_secs)::bigint::float8) < now()
         ORDER BY d.subject_expires_at
         LIMIT sqlc.arg(max_rows)::int);

-- name: CiIssuanceDeleteExpired :execrows
-- ci-issuance-retention (R-CI-020, R-CI-030): records whose expires_at is older than retention_secs.
DELETE FROM ci_token_issuances
 WHERE token_id IN (
        SELECT token_id FROM ci_token_issuances
         WHERE expires_at < now() - make_interval(secs => sqlc.arg(retention_secs)::bigint::float8)
         ORDER BY expires_at
         LIMIT sqlc.arg(max_rows)::int);
