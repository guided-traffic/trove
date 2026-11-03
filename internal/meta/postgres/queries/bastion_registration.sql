-- Queries of the Bastion registrations on the main installation (R-BAS-004, R-BAS-005; the statements of work
-- package 1, docs/developer/bastion.md). A registration is a principal of the kind 'bastion' plus the row of
-- the table bastion (00032); the principal row itself is written by the identity statements
-- (IdentityInsertPrincipal with kind 'bastion', IdentitySetPrincipalStatus, IdentityDeletePrincipal), the
-- tokens by IdentityInsertToken and the revocation statements, so that the Bastion token follows the same
-- storage, validation and revocation bound as every token (R-AUTH-022, R-AUTH-023, R-AUTH-029). This file holds
-- what is specific to registrations. Identifiers travel as text and are cast here, an optional value is the
-- empty string, and statements that return at most one row are :many (the pattern of identity.sql).

-- name: BastionInsert :exec
-- The subtype row of a registration whose principal row was just inserted. The kind guard (AI001) refuses a
-- principal of another kind; the name rule is the principal check principal_bastion_name.
INSERT INTO bastion (principal_id, description) VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(description)::text);

-- name: BastionGet :many
-- A registration by name. revision is the ETag (R-API-012); enabled is the principal's status.
SELECT p.id::text AS id, p.name, (p.status = 'active')::boolean AS enabled, p.created_at, p.revision, b.description
  FROM principal p JOIN bastion b ON b.principal_id = p.id
 WHERE p.kind = 'bastion' AND p.name = sqlc.arg(name)::text;

-- name: BastionLock :many
-- The registration by name, locking the principal row: an update or a delete compares If-Match with the
-- revision in the transaction that writes.
SELECT p.id::text AS id, p.name, (p.status = 'active')::boolean AS enabled, p.revision
  FROM principal p JOIN bastion b ON b.principal_id = p.id
 WHERE p.kind = 'bastion' AND p.name = sqlc.arg(name)::text
   FOR UPDATE OF p;

-- name: BastionList :many
-- Keyset order by name; there are at most bastions.max_registrations of them, so the caller may pass an empty
-- after and a generous max_rows.
SELECT p.id::text AS id, p.name, (p.status = 'active')::boolean AS enabled, p.created_at, p.revision, b.description
  FROM principal p JOIN bastion b ON b.principal_id = p.id
 WHERE p.kind = 'bastion' AND p.name > sqlc.arg(after)::text
 ORDER BY p.name
 LIMIT sqlc.arg(max_rows)::int;

-- name: BastionCount :one
-- The registrations that exist, for bastions.max_registrations (R-BAS-004).
SELECT count(*) FROM principal WHERE kind = 'bastion';

-- name: BastionUpdateDescription :exec
UPDATE bastion SET description = sqlc.arg(description)::text WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: BastionSetEnabled :many
-- Enable or disable (R-BAS-004): a disabled registration refuses its tokens like a disabled service account
-- (R-AUTH-020). No row when the status already is the one asked for.
UPDATE principal SET status = CASE WHEN sqlc.arg(enabled)::boolean THEN 'active' ELSE 'disabled' END
 WHERE id = sqlc.arg(id)::text::uuid AND kind = 'bastion'
   AND status <> CASE WHEN sqlc.arg(enabled)::boolean THEN 'active' ELSE 'disabled' END
RETURNING id::text AS id;

-- name: BastionCountActiveTokens :one
-- The unrevoked, unexpired tokens of a registration, against auth.tokens.max_active_per_principal.bst
-- (R-BAS-004). Taken under BastionLock, so two issuances cannot both pass the limit.
SELECT count(*) FROM api_token
 WHERE principal_id = sqlc.arg(id)::text::uuid AND type = 'bst'
   AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: BastionListTokens :many
-- The tokens of a registration for the registration's document (R-BAS-004, R-BAS-007): ID, hint, expiry,
-- creation and last use. Revoked tokens are listed only when include_revoked is set. Newest first.
SELECT t.id::text AS id, t.hint, t.description, t.created_at, t.expires_at, t.revoked_at, t.last_used_at,
       coalesce(host(t.last_used_addr), '')::text AS last_used_addr,
       coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired
  FROM api_token t
 WHERE t.principal_id = sqlc.arg(id)::text::uuid AND t.type = 'bst'
   AND (sqlc.arg(include_revoked)::boolean OR t.revoked_at IS NULL)
 ORDER BY t.id DESC;

-- name: BastionListTokensOfAll :many
-- The same for the list of registrations: one read for all of them.
SELECT t.principal_id::text AS bastion_id, t.id::text AS id, t.hint, t.description, t.created_at, t.expires_at,
       t.last_used_at, coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired
  FROM api_token t
 WHERE t.type = 'bst' AND t.revoked_at IS NULL
 ORDER BY t.principal_id, t.id DESC;

-- name: BastionTokenOwner :many
-- The registration a token belongs to, for the revocation of one token by ID (R-BAS-004): no row when the
-- token is not a Bastion token of that registration.
SELECT t.id::text AS id, t.principal_id::text AS bastion_id, t.revoked_at
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE t.id = sqlc.arg(id)::text::uuid AND t.type = 'bst' AND p.kind = 'bastion' AND p.name = sqlc.arg(name)::text;

-- name: BastionTokenBySecret :many
-- The lookup of the Bastion authenticator (internal/bastionmain.TokenAuthenticator): one indexed read by the
-- secret hash, joined with the registration; a row of another type is never returned here. rejection_due says
-- whether token.rejected is due for this token (at most once an hour, as for every token, R-AUTH-023 step 4).
SELECT t.id::text AS token_id, t.principal_id::text AS bastion_id, p.name, (p.status = 'active')::boolean AS enabled,
       t.expires_at, coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired,
       (t.revoked_at IS NOT NULL)::boolean AS revoked, t.hint,
       coalesce(t.rejection_logged_at IS NULL OR t.rejection_logged_at < now() - interval '1 hour', true)::boolean AS rejection_due
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE t.secret_sha256 = sqlc.arg(secret_sha256)::bytea AND t.type = 'bst' AND p.kind = 'bastion';

