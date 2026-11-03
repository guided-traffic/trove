-- Queries of the issuance records, the CI tokens, their revocation, the ownership of CI projects and the two
-- retention tasks (R-CI-015, R-CI-017, R-CI-018, R-CI-020, R-CI-030), the statements of work package W5
-- (docs/developer/ci-oidc.md). The record is written by the exchange (ci_exchange.sql); this file inserts the
-- token row (internal/auth/token.IssueCITx), reads the record and ends the tokens. Every statement of a
-- revocation runs on the caller's meta.Tx and is followed by the cache invalidation of the area auth with the
-- token hashes (R-AUTH-023).

-- name: CiIssuanceList :many
-- Newest first with the token state joined; the filters of R-CI-020 are optional, active is
-- 'true' (state active), 'false' (the other states) or '' (both); 0 for a time or the cursor is none. The state is
-- computed once, in the lateral subquery st, for the column and for the filter (Q-CI-52): revoked (the token row says
-- so), expired (the record's expiry has passed, also when the retention task deleted the token row), removed (the token
-- row is gone while the record is unexpired: it went with the principal of a deleted mapping, api_token.principal_id
-- cascades) or active. The cursor is (issued_at, token_id) of the last row of the previous page. The issuer filter is
-- an equality on issuer_id against the issuer's ID looked up once (an InitPlan), so ci_issuances_issuer_issued serves it
-- (Q-CI-53); a join on the name would hide the selectivity from the planner.
SELECT s.token_id::text AS token_id, i.name AS issuer, s.issuer_id::text AS issuer_id, s.mapping_ids::text[] AS mapping_ids,
       s.mapping_names, s.subject_kind, COALESCE(s.jti, '')::text AS jti, s.project_id, COALESCE(s.project_path, '')::text AS project_path,
       COALESCE(s.namespace_id, '')::text AS namespace_id, COALESCE(s.pipeline_id, '')::text AS pipeline_id, s.job_id,
       COALESCE(s.ref, '')::text AS ref, COALESCE(s.ref_type, '')::text AS ref_type, COALESCE(s.ref_protected::text, '')::text AS ref_protected,
       COALESCE(s.pipeline_source, '')::text AS pipeline_source, COALESCE(s.environment, '')::text AS environment,
       COALESCE(s.sha, '')::text AS sha, COALESCE(s.user_id, '')::text AS user_id, COALESCE(s.user_login, '')::text AS user_login,
       s.claims, s.grants, COALESCE(s.requested_scope, '')::text AS requested_scope, s.granted_scope, s.cross_project,
       (extract(epoch FROM s.issued_at) * 1000000)::bigint AS issued_us, (extract(epoch FROM s.expires_at) * 1000000)::bigint AS expires_us,
       COALESCE(host(s.client_ip), '')::text AS client_ip, COALESCE(s.user_agent, '')::text AS user_agent,
       COALESCE(extract(epoch FROM t.revoked_at) * 1000000, 0)::bigint AS revoked_us, st.state::text AS state
  FROM ci_token_issuances s
  JOIN ci_issuers i ON i.id = s.issuer_id
  LEFT JOIN api_token t ON t.id = s.token_id
  CROSS JOIN LATERAL (SELECT CASE WHEN t.id IS NULL THEN CASE WHEN s.expires_at > now() THEN 'removed' ELSE 'expired' END
                                  WHEN t.revoked_at IS NOT NULL THEN 'revoked'
                                  WHEN s.expires_at <= now() THEN 'expired'
                                  ELSE 'active' END AS state) st
 WHERE (sqlc.arg(issuer)::text = '' OR s.issuer_id = (SELECT x.id FROM ci_issuers x WHERE x.name = sqlc.arg(issuer)::text))
   AND (sqlc.arg(project_id)::text = '' OR s.project_id = sqlc.arg(project_id)::text)
   AND (sqlc.arg(project_path)::text = '' OR s.project_path = sqlc.arg(project_path)::text)
   AND (sqlc.arg(pipeline_id)::text = '' OR s.pipeline_id = sqlc.arg(pipeline_id)::text)
   AND (sqlc.arg(job_id)::text = '' OR s.job_id = sqlc.arg(job_id)::text)
   AND (sqlc.arg(mapping)::text = '' OR sqlc.arg(mapping)::text = ANY(s.mapping_names))
   AND (sqlc.arg(active)::text = ''
        OR (sqlc.arg(active)::text = 'true') = (st.state = 'active'))
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
 ORDER BY t.id
 FOR UPDATE OF t;

-- name: CiRevokeTokens :many
-- Ends the tokens of the given IDs (R-CI-017): reason is mapping_deleted or admin, the reasons of the token table
-- (Q-CI-38). Returns the IDs and the hashes for the cache invalidation.
UPDATE api_token SET revoked_at = now(), revoked_by = NULLIF(sqlc.arg(revoked_by)::text, '')::uuid, revocation_reason = sqlc.arg(reason)::text
 WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND revoked_at IS NULL
RETURNING id::text AS id, secret_sha256;

-- name: CiIssuanceTokensOfMapping :many
-- The unexpired tokens a mapping's disable or delete ends, in the same transaction (R-CI-017, Q-CI-47): those it
-- owns (its principal), and with contributed those whose issuance record lists it among the contributing mappings
-- (the system tier, whose disable ends every token that carries its grants). A project tier's mapping ends only the
-- tokens it owns; the evaluator drops its contribution to the others. Both branches read unexpired rows only: the
-- records through the expiry index (expires_at > now(), the token row's expiry), so the caller, who holds the
-- issuer row FOR UPDATE, scans the tokens of the last ci.token.max_ttl, not the retention.
SELECT t.id::text AS id, t.secret_sha256
  FROM api_token t
 WHERE t.type = 'cit' AND t.revoked_at IS NULL AND t.expires_at > now()
   AND (t.principal_id = sqlc.arg(principal_id)::text::uuid
        OR (sqlc.arg(contributed)::boolean
            AND t.id IN (SELECT s.token_id FROM ci_token_issuances s
                          WHERE s.expires_at > now() AND s.issuer_id = sqlc.arg(issuer_id)::text::uuid
                            AND sqlc.arg(mapping_id)::text::uuid = ANY(s.mapping_ids))))
 ORDER BY t.id
 FOR UPDATE OF t;

-- name: CiIssuanceTokensOfIssuer :many
-- The unexpired tokens issued through an issuer (disable, delete), found through the unexpired records only (the
-- expiry index), so the caller, who holds the issuer row FOR UPDATE, scans the tokens of the last ci.token.max_ttl.
SELECT t.id::text AS id, t.secret_sha256
  FROM ci_token_issuances s
  JOIN api_token t ON t.id = s.token_id
 WHERE s.expires_at > now() AND s.issuer_id = sqlc.arg(issuer_id)::text::uuid AND t.revoked_at IS NULL AND t.expires_at > now()
 ORDER BY t.id
 FOR UPDATE OF t;

-- name: CiReplayDeleteExpired :execrows
-- ci-replay-cleanup (R-CI-018, R-CI-030): rows whose subject_expires_at + the issuer's clock skew +
-- retention_secs has passed, in the order of the expiry index, at most max_rows per transaction.
DELETE FROM ci_subject_token_uses u
 WHERE (u.issuer_id, u.jti) IN (
        SELECT d.issuer_id, d.jti
          FROM ci_subject_token_uses d JOIN ci_issuers i ON i.id = d.issuer_id
         WHERE d.subject_expires_at < now() - make_interval(secs => sqlc.arg(retention_secs)::bigint::float8)
           AND d.subject_expires_at + i.clock_skew + make_interval(secs => sqlc.arg(retention_secs)::bigint::float8) < now()
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

-- name: CiIssuanceGet :many
-- One record by its token ID, with the token state joined (GET /api/v1/ci/issuances/{tokenId}).
SELECT s.token_id::text AS token_id, i.name AS issuer, s.issuer_id::text AS issuer_id, s.mapping_ids::text[] AS mapping_ids,
       s.mapping_names, s.subject_kind, COALESCE(s.jti, '')::text AS jti, s.project_id, COALESCE(s.project_path, '')::text AS project_path,
       COALESCE(s.namespace_id, '')::text AS namespace_id, COALESCE(s.pipeline_id, '')::text AS pipeline_id, s.job_id,
       COALESCE(s.ref, '')::text AS ref, COALESCE(s.ref_type, '')::text AS ref_type, COALESCE(s.ref_protected::text, '')::text AS ref_protected,
       COALESCE(s.pipeline_source, '')::text AS pipeline_source, COALESCE(s.environment, '')::text AS environment,
       COALESCE(s.sha, '')::text AS sha, COALESCE(s.user_id, '')::text AS user_id, COALESCE(s.user_login, '')::text AS user_login,
       s.claims, s.grants, COALESCE(s.requested_scope, '')::text AS requested_scope, s.granted_scope, s.cross_project,
       (extract(epoch FROM s.issued_at) * 1000000)::bigint AS issued_us, (extract(epoch FROM s.expires_at) * 1000000)::bigint AS expires_us,
       COALESCE(host(s.client_ip), '')::text AS client_ip, COALESCE(s.user_agent, '')::text AS user_agent,
       COALESCE(extract(epoch FROM t.revoked_at) * 1000000, 0)::bigint AS revoked_us, st.state::text AS state
  FROM ci_token_issuances s
  JOIN ci_issuers i ON i.id = s.issuer_id
  LEFT JOIN api_token t ON t.id = s.token_id
  CROSS JOIN LATERAL (SELECT CASE WHEN t.id IS NULL THEN CASE WHEN s.expires_at > now() THEN 'removed' ELSE 'expired' END
                                  WHEN t.revoked_at IS NOT NULL THEN 'revoked'
                                  WHEN s.expires_at <= now() THEN 'expired'
                                  ELSE 'active' END AS state) st
 WHERE s.token_id = sqlc.arg(token_id)::text::uuid;

-- name: CiIssuanceListForProject :many
-- The records of tokens one of whose contributing mappings is of the project's tier (Q-CI-26), found through the
-- frozen bundle, so a record stays listed after its mapping was deleted; newest first, the cursor of
-- CiIssuanceList. The caller shows the project's mappings only.
SELECT s.token_id::text AS token_id, i.name AS issuer, s.issuer_id::text AS issuer_id, s.mapping_ids::text[] AS mapping_ids,
       s.mapping_names, s.subject_kind, COALESCE(s.jti, '')::text AS jti, s.project_id, COALESCE(s.project_path, '')::text AS project_path,
       COALESCE(s.namespace_id, '')::text AS namespace_id, COALESCE(s.pipeline_id, '')::text AS pipeline_id, s.job_id,
       COALESCE(s.ref, '')::text AS ref, COALESCE(s.ref_type, '')::text AS ref_type, COALESCE(s.ref_protected::text, '')::text AS ref_protected,
       COALESCE(s.pipeline_source, '')::text AS pipeline_source, COALESCE(s.environment, '')::text AS environment,
       COALESCE(s.sha, '')::text AS sha, COALESCE(s.user_id, '')::text AS user_id, COALESCE(s.user_login, '')::text AS user_login,
       s.claims, s.grants, COALESCE(s.requested_scope, '')::text AS requested_scope, s.granted_scope, s.cross_project,
       (extract(epoch FROM s.issued_at) * 1000000)::bigint AS issued_us, (extract(epoch FROM s.expires_at) * 1000000)::bigint AS expires_us,
       COALESCE(host(s.client_ip), '')::text AS client_ip, COALESCE(s.user_agent, '')::text AS user_agent,
       COALESCE(extract(epoch FROM t.revoked_at) * 1000000, 0)::bigint AS revoked_us, st.state::text AS state
  FROM ci_token_issuances s
  JOIN ci_issuers i ON i.id = s.issuer_id
  LEFT JOIN api_token t ON t.id = s.token_id
  CROSS JOIN LATERAL (SELECT CASE WHEN t.id IS NULL THEN CASE WHEN s.expires_at > now() THEN 'removed' ELSE 'expired' END
                                  WHEN t.revoked_at IS NOT NULL THEN 'revoked'
                                  WHEN s.expires_at <= now() THEN 'expired'
                                  ELSE 'active' END AS state) st
 WHERE s.grants->'contributions' @> jsonb_build_array(jsonb_build_object('project', jsonb_build_object('id', sqlc.arg(project_id)::text)))
   AND (sqlc.arg(issuer)::text = '' OR i.name = sqlc.arg(issuer)::text)
   AND (sqlc.arg(job_id)::text = '' OR s.job_id = sqlc.arg(job_id)::text)
   AND (sqlc.arg(active)::text = ''
        OR (sqlc.arg(active)::text = 'true') = (st.state = 'active'))
   AND (sqlc.arg(after_us)::bigint = 0
        OR (s.issued_at, s.token_id) < (to_timestamp(sqlc.arg(after_us)::bigint / 1000000.0), sqlc.arg(after_id)::text::uuid))
 ORDER BY s.issued_at DESC, s.token_id DESC
 LIMIT sqlc.arg(max_rows)::int;

-- name: CiProjectIDByKey :many
-- The ID of a trove project by its key, for the project's issuance list.
SELECT id::text AS id FROM project WHERE key = sqlc.arg(key)::text;

-- name: CiIssuanceUsage :one
-- The usage summary of a record from the audit store (R-CI-020): the events the token is the credential of, within
-- its lifetime (audit_event is partitioned by occurred_at, so the window bounds the scan).
SELECT count(*)::bigint AS requests, COALESCE((extract(epoch FROM max(occurred_at)) * 1000000)::bigint, 0)::bigint AS last_us
  FROM audit_event
 WHERE token_id = sqlc.arg(token_id)::text::uuid
   AND occurred_at >= to_timestamp(sqlc.arg(from_us)::bigint / 1000000.0)
   AND occurred_at < to_timestamp(sqlc.arg(to_us)::bigint / 1000000.0);

-- name: CiIssuanceDeployed :many
-- The package versions the token published, from the publish events of the formats (npm.publish, package.published).
SELECT DISTINCT COALESCE(repository, '')::text AS repository, COALESCE(details->>'package', '')::text AS package,
       COALESCE(details->>'version', '')::text AS version
  FROM audit_event
 WHERE token_id = sqlc.arg(token_id)::text::uuid AND action IN ('npm.publish', 'package.published') AND outcome = 'success'
   AND occurred_at >= to_timestamp(sqlc.arg(from_us)::bigint / 1000000.0)
   AND occurred_at < to_timestamp(sqlc.arg(to_us)::bigint / 1000000.0)
 ORDER BY 1, 2, 3
 LIMIT 1000;

-- name: CiTokensActive :many
-- trove_ci_tokens_active (R-CI-020): unexpired, unrevoked CI tokens per issuer, 0 for an issuer without one, read
-- by the database gauge source (R-OPS-007).
SELECT i.name, count(t.id)::bigint AS active
  FROM ci_issuers i
  LEFT JOIN ci_token_issuances s ON s.issuer_id = i.id AND s.expires_at > now()
  LEFT JOIN api_token t ON t.id = s.token_id AND t.revoked_at IS NULL AND t.expires_at > now()
 GROUP BY i.name
 ORDER BY i.name;

-- name: CiTokenOwner :many
-- The owner of a CI token being issued: a mapping principal, read FOR SHARE, so a concurrent disable of the
-- principal waits for the issuance or the issuance sees it (R-CI-017).
SELECT kind, name, status FROM principal WHERE id = sqlc.arg(id)::text::uuid FOR SHARE;

-- name: CiTokenInsert :one
-- A CI token (R-CI-017, R-CI-016): type cit, created_via ci-exchange, the frozen bundle in ci_context, the expiry
-- the database clock plus the computed lifetime, so the stored expiry is issued_at + ttl on every replica.
INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at, ci_context)
VALUES (sqlc.arg(id)::text::uuid, 'cit', sqlc.arg(secret_sha256)::bytea, sqlc.arg(hint)::text, sqlc.arg(principal_id)::text::uuid,
        sqlc.arg(description)::text, sqlc.arg(scope)::text::jsonb, 'ci-exchange',
        now() + make_interval(secs => sqlc.arg(ttl_secs)::float8), sqlc.arg(ci_context)::text::jsonb)
RETURNING created_at, expires_at;

-- name: CiTokenIssuers :many
-- The issuer names of revoked CI tokens, for the ci.token.revoked events of the token service's revocations.
SELECT id::text AS id, COALESCE(ci_context->>'issuer', '')::text AS issuer
  FROM api_token WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND type = 'cit'
 ORDER BY id;

-- name: CiIssuerIDByName :many
-- The ID of a CI issuer by name: the owner reference ci:<issuer>:project:<id> names the issuer, the store keeps
-- its ID (R-CI-015).
SELECT id::text AS id FROM ci_issuers WHERE name = sqlc.arg(name)::text;

-- name: CiMappingPrincipalsByName :many
-- The principals of CI mappings by name (<issuer>/<mapping>): the owners a claim records for an inline grant with
-- ownership none (R-AUTHZ-018 rule 2).
SELECT id::text AS id, name FROM principal WHERE kind = 'ci_mapping' AND name = ANY(sqlc.arg(names)::text[]) ORDER BY name;

-- name: CiOwnershipInsertProjectOwner :many
-- A CI project owner (R-CI-015): by issuer ID and GitLab project ID, project_path as the display value. No row back
-- means the project owns the name already (package_owner_unique).
INSERT INTO package_owner (id, domain, format, package_name, owner_kind, ci_issuer_id, ci_project_id, display, reason, added_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(domain)::text, sqlc.arg(format)::text, sqlc.arg(package_name)::text,
        'ci_project', sqlc.arg(ci_issuer_id)::text::uuid, sqlc.arg(ci_project_id)::text, sqlc.arg(display)::text,
        sqlc.arg(reason)::text, NULLIF(sqlc.arg(added_by)::text, '')::uuid)
ON CONFLICT DO NOTHING
RETURNING (extract(epoch FROM added_at) * 1000000)::bigint AS added_us;

-- name: CiOwnershipUpdateDisplay :execrows
-- project_path is the display value of a CI project owner, updated on every publish (R-CI-015, R-AUTHZ-018 rule 7).
UPDATE package_owner SET display = sqlc.arg(display)::text
 WHERE domain = sqlc.arg(domain)::text AND format = sqlc.arg(format)::text AND package_name = sqlc.arg(package_name)::text
   AND owner_kind = 'ci_project' AND ci_issuer_id = sqlc.arg(ci_issuer_id)::text::uuid AND ci_project_id = sqlc.arg(ci_project_id)::text
   AND display <> sqlc.arg(display)::text;

-- name: CiOwnershipOwnedByProject :many
-- The names a CI project owns, in key order after the cursor (R-AUTHZ-019 GET /package-owners?owner=ci:…:project:…).
SELECT o.domain, o.format, o.package_name, COALESCE(r.key, '')::text AS repository_key, s.state, o.reason,
       (extract(epoch FROM o.added_at) * 1000000)::bigint AS added_us
  FROM package_owner o
  JOIN package_ownership s ON s.domain = o.domain AND s.format = o.format AND s.package_name = o.package_name
  LEFT JOIN repositories r ON r.id = (CASE WHEN o.domain ~ '^repo:[0-9]+$' THEN substr(o.domain, 6)::bigint END)
 WHERE o.owner_kind = 'ci_project' AND o.ci_issuer_id = sqlc.arg(ci_issuer_id)::text::uuid AND o.ci_project_id = sqlc.arg(ci_project_id)::text
   AND (sqlc.arg(domain)::text = '' OR o.domain = sqlc.arg(domain)::text)
   AND (o.domain, o.format, o.package_name) > (sqlc.arg(after_domain)::text, sqlc.arg(after_format)::text, sqlc.arg(after_name)::text)
 ORDER BY o.domain, o.format, o.package_name
 LIMIT sqlc.arg(max_rows)::int;
