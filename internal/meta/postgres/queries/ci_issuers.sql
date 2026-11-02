-- Queries of the CI issuers and their signing keys (R-CI-001 to R-CI-005, Q-CI-12, Q-CI-16), the
-- statements of work package W1 (docs/developer/ci-oidc.md). Identifiers are read as text and written
-- through text::uuid, so the domain package never imports the driver types (R-ARCH-002). Statements
-- that return at most one row are :many, so the caller never meets the driver's no-rows error.
-- Intervals are read and written as seconds. Every write of an issuer runs on the caller's meta.Tx, in
-- which it notifies the area ci_mappings; every write of a key row notifies ci_issuer_keys, and only
-- when the key set's content changed (R-CI-003). W1 adds its further statements here.

-- name: CiIssuerList :many
-- Every issuer with its mapping count and the summary of its key row: the keys of the stored set (the
-- caller counts the usable and the ignored ones), the fetch time and the last error.
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us,
       (SELECT count(*) FROM ci_mappings m WHERE m.issuer_id = i.id)::bigint AS mapping_count,
       COALESCE(k.jwks, '{"keys": []}'::jsonb)::text AS jwks,
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

-- name: CiIssuerByID :many
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_issuers i
 WHERE i.id = sqlc.arg(id)::text::uuid;

-- name: CiIssuerByIDShare :many
-- The exchange's read (R-CI-007): FOR SHARE on the issuer row, so a concurrent issuer write waits for the
-- exchange transaction and the exchange compares version and mappings_version with what it evaluated.
SELECT i.id::text AS id, i.name, i.kind, i.issuer, COALESCE(i.discovery_url, '')::text AS discovery_url, COALESCE(i.jwks_uri, '')::text AS jwks_uri, i.allowed_audiences, i.exclusive_audience,
       i.allowed_algorithms, i.allowed_typ, i.allow_missing_typ, extract(epoch FROM i.clock_skew)::bigint AS clock_skew_secs,
       i.required_claims, i.public_issuer, i.pin_claims, i.single_use_subject_tokens,
       COALESCE(extract(epoch FROM i.max_ttl), 0)::bigint AS max_ttl_secs, COALESCE(i.ca_bundle_ref, '')::text AS ca_bundle_ref, i.use_outbound_proxy, i.job_token_fallback,
       i.enabled, i.description, i.source, i.removed_from_configuration, i.version, i.mappings_version,
       (extract(epoch FROM i.created_at) * 1000000)::bigint AS created_us,
       (extract(epoch FROM i.updated_at) * 1000000)::bigint AS updated_us
  FROM ci_issuers i
 WHERE i.id = sqlc.arg(id)::text::uuid FOR SHARE OF i;

-- name: CiIssuerUpdate :many
-- The full replacement of an issuer row by id, after CiIssuerLock took the row: the caller compared the
-- version it read. name and source are immutable. version moves by one.
UPDATE ci_issuers
   SET kind = sqlc.arg(kind)::text, issuer = sqlc.arg(issuer)::text,
       discovery_url = NULLIF(sqlc.arg(discovery_url)::text, ''), jwks_uri = NULLIF(sqlc.arg(jwks_uri)::text, ''),
       allowed_audiences = sqlc.arg(allowed_audiences)::text[], exclusive_audience = sqlc.arg(exclusive_audience)::boolean,
       allowed_algorithms = sqlc.arg(allowed_algorithms)::text[], allowed_typ = sqlc.arg(allowed_typ)::text[],
       allow_missing_typ = sqlc.arg(allow_missing_typ)::boolean,
       clock_skew = make_interval(secs => sqlc.arg(clock_skew_secs)::bigint::float8),
       required_claims = sqlc.arg(required_claims)::text[], public_issuer = sqlc.arg(public_issuer)::boolean,
       pin_claims = sqlc.arg(pin_claims)::text[], single_use_subject_tokens = sqlc.arg(single_use_subject_tokens)::boolean,
       max_ttl = CASE WHEN sqlc.arg(max_ttl_secs)::bigint > 0 THEN make_interval(secs => sqlc.arg(max_ttl_secs)::bigint::float8) END,
       ca_bundle_ref = NULLIF(sqlc.arg(ca_bundle_ref)::text, ''), use_outbound_proxy = sqlc.arg(use_outbound_proxy)::boolean,
       job_token_fallback = sqlc.arg(job_token_fallback)::text::jsonb, enabled = sqlc.arg(enabled)::boolean,
       description = sqlc.arg(description)::text, removed_from_configuration = sqlc.arg(removed_from_configuration)::boolean,
       version = version + 1, updated_at = now()
 WHERE id = sqlc.arg(id)::text::uuid
RETURNING version, (extract(epoch FROM updated_at) * 1000000)::bigint AS updated_us;

-- name: CiIssuerMappingCount :many
SELECT count(*)::bigint AS n FROM ci_mappings WHERE issuer_id = sqlc.arg(id)::text::uuid;

-- name: CiIssuerHasIssuances :many
-- Whether the exchange ever issued a token through the issuer: a token exists only with its issuance
-- record, written in the exchange transaction (R-CI-020), so no row means nothing to revoke.
SELECT 1::int AS one FROM ci_token_issuances WHERE issuer_id = sqlc.arg(id)::text::uuid LIMIT 1;

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
SELECT (jwks = sqlc.arg(jwks)::text::jsonb) AS same, (extract(epoch FROM fetched_at) * 1000000)::bigint AS fetched_us
  FROM ci_issuer_keys WHERE issuer_id = sqlc.arg(issuer_id)::text::uuid FOR UPDATE;

-- name: CiKeysStore :exec
-- Inserts the row or replaces it, unless a concurrent fetch stored a set with a later fetched_at. The
-- fetch time is the time the replica received the response, never later than now(), so a fetch that
-- finished earlier but stores later does not overwrite a later one. A stored set clears the last error.
INSERT INTO ci_issuer_keys (issuer_id, jwks, jwks_uri, fetched_at, etag, next_refresh_at)
VALUES (sqlc.arg(issuer_id)::text::uuid, sqlc.arg(jwks)::text::jsonb, sqlc.arg(jwks_uri)::text,
        LEAST(to_timestamp(sqlc.arg(fetched_us)::bigint / 1000000.0), now()),
        NULLIF(sqlc.arg(etag)::text, ''), now() + make_interval(secs => sqlc.arg(next_refresh_secs)::bigint::float8))
ON CONFLICT (issuer_id) DO UPDATE
   SET jwks = EXCLUDED.jwks, jwks_uri = EXCLUDED.jwks_uri, fetched_at = EXCLUDED.fetched_at, etag = EXCLUDED.etag,
       next_refresh_at = EXCLUDED.next_refresh_at, last_error = NULL, last_error_at = NULL
 WHERE ci_issuer_keys.fetched_at < EXCLUDED.fetched_at;

-- name: CiKeysRecordError :exec
-- A failed fetch: the last good set stays, the next attempt is due after min_refresh_interval (R-CI-003).
-- An issuer that never had a set gets a placeholder row (an empty set, fetched_at at the epoch), so the
-- failure is visible in the key status and the age check fails closed; the placeholder is replaced by
-- the first stored set. The class, the HTTP status and the host, never a response body.
INSERT INTO ci_issuer_keys (issuer_id, jwks, jwks_uri, fetched_at, next_refresh_at, last_error, last_error_at)
VALUES (sqlc.arg(issuer_id)::text::uuid, '{"keys": []}'::jsonb, sqlc.arg(jwks_uri)::text, to_timestamp(0),
        now() + make_interval(secs => sqlc.arg(retry_secs)::bigint::float8), sqlc.arg(last_error)::text, now())
ON CONFLICT (issuer_id) DO UPDATE
   SET last_error = EXCLUDED.last_error, last_error_at = EXCLUDED.last_error_at, next_refresh_at = EXCLUDED.next_refresh_at;

-- name: CiKeysDue :many
-- The run of ci-keys-refresh: every enabled issuer whose next_refresh_at has passed or that has no
-- row yet (R-CI-030).
SELECT i.id::text AS id, i.name
  FROM ci_issuers i
  LEFT JOIN ci_issuer_keys k ON k.issuer_id = i.id
 WHERE i.enabled AND (k.issuer_id IS NULL OR k.next_refresh_at <= now())
 ORDER BY k.next_refresh_at NULLS FIRST, i.name;

-- name: CiIssuerConfigured :many
-- The issuers of the configuration, for the removal step of trove migrate (Q-CI-16).
SELECT id::text AS id, name, enabled, removed_from_configuration FROM ci_issuers WHERE source = 'configuration' ORDER BY name;
