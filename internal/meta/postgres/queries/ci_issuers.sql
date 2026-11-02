-- Queries of the CI issuers and their signing keys (R-CI-001 to R-CI-005, Q-CI-12, Q-CI-16), the
-- statements of work package W1 (docs/developer/ci-oidc.md). Identifiers are read as text and written
-- through text::uuid, so the domain package never imports the driver types (R-ARCH-002). Statements
-- that return at most one row are :many, so the caller never meets the driver's no-rows error.
-- Intervals are read and written as seconds. Every write of an issuer runs on the caller's meta.Tx, in
-- which it notifies the area ci_mappings; every write of a key row notifies ci_issuer_keys, and only
-- when the key set's content changed (R-CI-003). W1 adds its further statements here.

-- name: CiIssuerList :many
-- Every issuer with its mapping count and the summary of its key row: the number of keys in the
-- stored set, the fetch time and the last error.
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us,
       (SELECT count(*) FROM ci_mappings m WHERE m.issuer_id = i.id)::bigint AS mapping_count,
       COALESCE(jsonb_array_length(k.jwks -> 'keys'), 0)::bigint AS key_count,
       COALESCE(extract(epoch FROM k.fetched_at) * 1000000, 0)::bigint AS fetched_us,
       COALESCE(k.last_error, '')::text AS last_error,
       COALESCE(extract(epoch FROM k.last_error_at) * 1000000, 0)::bigint AS last_error_us
  FROM ci_issuers i
  LEFT JOIN ci_issuer_keys k ON k.issuer_id = i.id
 ORDER BY i.name;

-- name: CiIssuerByName :many
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_issuers i
 WHERE i.name = sqlc.arg(name)::text;

-- name: CiIssuerByIssuer :many
-- The selection of R-CI-004 step 5: the exact iss, no normalization. The in-process copy answers the
-- request path; this is the read that fills it.
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_issuers i
 WHERE i.issuer = sqlc.arg(issuer)::text;

-- name: CiIssuerLock :many
-- Serialises the writers of one issuer (R-CI-002): an issuer write and every write of its mappings
-- take this lock first. No row back means the issuer does not exist.
SELECT id::text AS id, version, mappings_version, source, removed_from_configuration, enabled
  FROM ci_issuers WHERE name = sqlc.arg(name)::text FOR UPDATE;

-- name: CiIssuerInsert :many
-- No row back means the name or the issuer URL exists (409).
INSERT INTO ci_issuers (id, name, kind, issuer, discovery_url, jwks_uri, allowed_audiences, exclusive_audience, allowed_algorithms,
                        allowed_typ, allow_missing_typ, clock_skew, required_claims, public_issuer, pin_claims,
                        single_use_subject_tokens, max_ttl, ca_bundle_ref, use_outbound_proxy, job_token_fallback, enabled,
                        description, source)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(name)::text, sqlc.arg(kind)::text, sqlc.arg(issuer)::text,
        NULLIF(sqlc.arg(discovery_url)::text, ''), NULLIF(sqlc.arg(jwks_uri)::text, ''), sqlc.arg(allowed_audiences)::text[], sqlc.arg(exclusive_audience)::boolean,
        sqlc.arg(allowed_algorithms)::text[], sqlc.arg(allowed_typ)::text[], sqlc.arg(allow_missing_typ)::boolean,
        make_interval(secs => sqlc.arg(clock_skew_secs)::bigint::float8), sqlc.arg(required_claims)::text[], sqlc.arg(public_issuer)::boolean,
        sqlc.arg(pin_claims)::text[], sqlc.arg(single_use_subject_tokens)::boolean,
        CASE WHEN sqlc.arg(max_ttl_secs)::bigint > 0 THEN make_interval(secs => sqlc.arg(max_ttl_secs)::bigint::float8) END,
        NULLIF(sqlc.arg(ca_bundle_ref)::text, ''), sqlc.arg(use_outbound_proxy)::boolean, sqlc.arg(job_token_fallback)::text::jsonb,
        sqlc.arg(enabled)::boolean, sqlc.arg(description)::text, sqlc.arg(source)::text)
ON CONFLICT DO NOTHING
RETURNING id::text AS id, version;

-- name: CiIssuerDelete :exec
-- The mappings must be deleted first (ON DELETE RESTRICT); the key row, the replay set, the issuance
-- records and the project owners of the issuer go with it (ON DELETE CASCADE).
DELETE FROM ci_issuers WHERE id = sqlc.arg(id)::text::uuid;

-- name: CiKeysGet :many
SELECT jwks, jwks_uri, (extract(epoch FROM fetched_at) * 1000000)::bigint AS fetched_us, COALESCE(etag, '')::text AS etag,
       (extract(epoch FROM next_refresh_at) * 1000000)::bigint AS next_refresh_us,
       COALESCE(last_error, '')::text AS last_error,
       COALESCE(extract(epoch FROM last_error_at) * 1000000, 0)::bigint AS last_error_us
  FROM ci_issuer_keys WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid;

-- name: CiKeysLock :many
-- The write rule of R-CI-003: a key store locks the key row alone, never the ci_issuers row. The row
-- it reads is what it compares the fetched set with, to notify ci_issuer_keys only on a change.
SELECT jwks, (extract(epoch FROM fetched_at) * 1000000)::bigint AS fetched_us
  FROM ci_issuer_keys WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid FOR UPDATE;

-- name: CiKeysStore :exec
-- Inserts the row or replaces it, unless a concurrent fetch stored a set with a later fetched_at. A
-- stored set clears the last error.
INSERT INTO ci_issuer_keys (issuer_id, jwks, jwks_uri, fetched_at, etag, next_refresh_at)
VALUES (sqlc.arg(issuer_id)::text::uuid, sqlc.arg(jwks)::text::jsonb, sqlc.arg(jwks_uri)::text, now(),
        NULLIF(sqlc.arg(etag)::text, ''), now() + make_interval(secs => sqlc.arg(next_refresh_secs)::bigint::float8))
ON CONFLICT (issuer_id) DO UPDATE
   SET jwks = EXCLUDED.jwks, jwks_uri = EXCLUDED.jwks_uri, fetched_at = EXCLUDED.fetched_at, etag = EXCLUDED.etag,
       next_refresh_at = EXCLUDED.next_refresh_at, last_error = NULL, last_error_at = NULL
 WHERE ci_issuer_keys.fetched_at < EXCLUDED.fetched_at;

-- name: CiKeysRecordError :exec
-- A failed fetch on an existing row: the last good set stays, the next attempt is due after
-- min_refresh_interval (R-CI-003). The class, the HTTP status and the host, never a response body.
UPDATE ci_issuer_keys
   SET last_error = sqlc.arg(last_error)::text, last_error_at = now(),
       next_refresh_at = now() + make_interval(secs => sqlc.arg(retry_secs)::bigint::float8)
 WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid;

-- name: CiKeysDue :many
-- The run of ci-keys-refresh: every enabled issuer whose next_refresh_at has passed or that has no
-- row yet (R-CI-030).
SELECT i.id::text AS id, i.name
  FROM ci_issuers i
  LEFT JOIN ci_issuer_keys k ON k.issuer_id = i.id
 WHERE i.enabled AND (k.issuer_id IS NULL OR k.next_refresh_at <= now())
 ORDER BY k.next_refresh_at NULLS FIRST, i.name;
