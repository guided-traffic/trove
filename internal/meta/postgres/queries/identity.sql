-- Queries of the identity packages (internal/auth/principal, internal/auth/token;
-- R-AUTH-016 to R-AUTH-040). Those packages are domain packages and import no
-- driver, so every parameter is a plain Go type: identifiers travel as text and
-- are cast to uuid here, an optional value is the empty string, a negative number
-- or a NULL narg, and an interval is a number of seconds. Statements that return
-- at most one row are :many so the caller never meets the driver's no-rows error.

-- Principals -----------------------------------------------------------------

-- name: IdentityPrincipalByID :many
SELECT p.id::text AS id, p.kind, p.name, p.status, coalesce(u.is_break_glass, false) AS is_break_glass
  FROM principal p LEFT JOIN app_user u ON u.principal_id = p.id
 WHERE p.id = sqlc.arg(id)::text::uuid;

-- name: IdentityPrincipalByName :many
SELECT p.id::text AS id, p.kind, p.name, p.status, coalesce(u.is_break_glass, false) AS is_break_glass
  FROM principal p LEFT JOIN app_user u ON u.principal_id = p.id
 WHERE p.kind = sqlc.arg(kind)::text AND p.name = sqlc.arg(name)::text;

-- name: IdentityGroupsOfMember :many
SELECT DISTINCT g.group_id::text AS id, p.name
  FROM group_membership g JOIN principal p ON p.id = g.group_id
 WHERE g.member_id = sqlc.arg(member_id)::text::uuid
 ORDER BY p.name;

-- name: IdentityLockPrincipal :many
SELECT id::text AS id, kind, status FROM principal WHERE id = sqlc.arg(id)::text::uuid FOR UPDATE;

-- name: IdentityInsertPrincipal :many
INSERT INTO principal (id, kind, name, created_by)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(kind)::text, sqlc.arg(name)::text, NULLIF(sqlc.arg(created_by)::text, '')::uuid)
ON CONFLICT (kind, name) DO NOTHING
RETURNING id::text AS id;

-- name: IdentitySetPrincipalStatus :many
UPDATE principal SET status = sqlc.arg(status)::text
 WHERE id = sqlc.arg(id)::text::uuid AND status <> sqlc.arg(status)::text
RETURNING id::text AS id;

-- name: IdentityRenamePrincipal :many
UPDATE principal SET name = sqlc.arg(name)::text
 WHERE id = sqlc.arg(id)::text::uuid AND name <> sqlc.arg(name)::text
   AND NOT EXISTS (SELECT 1 FROM principal o WHERE o.kind = principal.kind AND o.name = sqlc.arg(name)::text)
RETURNING id::text AS id;

-- name: IdentityDeletePrincipal :many
DELETE FROM principal WHERE id = sqlc.arg(id)::text::uuid RETURNING id::text AS id;

-- Users ----------------------------------------------------------------------

-- name: IdentityInsertUser :exec
INSERT INTO app_user (principal_id, email, email_verified, display_name, origin, last_login_at, last_interactive_auth_at)
VALUES (sqlc.arg(id)::text::uuid, NULLIF(sqlc.arg(email)::text, ''), sqlc.arg(email_verified)::boolean,
        NULLIF(sqlc.arg(display_name)::text, ''), sqlc.arg(origin)::text,
        CASE WHEN sqlc.arg(logged_in)::boolean THEN now() END, CASE WHEN sqlc.arg(logged_in)::boolean THEN now() END);

-- name: IdentityGetUser :many
SELECT p.id::text AS id, p.name, p.status, p.created_at,
       coalesce(u.email, '')::text AS email, u.email_verified, coalesce(u.display_name, '')::text AS display_name,
       u.origin, u.is_break_glass, coalesce(u.disabled_reason, '')::text AS disabled_reason,
       u.last_login_at, coalesce(u.scim_external_id, '')::text AS scim_external_id,
       coalesce((SELECT i.last_claims -> 'groupClaimMissing' FROM user_identity i
                  WHERE i.user_id = u.principal_id AND i.last_claims ? 'groupClaimMissing'
                  ORDER BY i.id DESC LIMIT 1), 'null'::jsonb)::text AS group_claim_missing
  FROM principal p JOIN app_user u ON u.principal_id = p.id
 WHERE p.kind = 'user' AND p.name = sqlc.arg(name)::text;

-- name: IdentityListUsers :many
-- Keyset order by name. status is active, disabled, locked (the break-glass account
-- while its lockout is in force, R-AUTH-038) or ''; origin and group are '' for any;
-- admin is 'true', 'false' or '' (the user holds the role admin directly or through
-- a group, or is the break-glass account).
SELECT p.id::text AS id, p.name, p.status, p.created_at,
       coalesce(u.email, '')::text AS email, u.email_verified, coalesce(u.display_name, '')::text AS display_name,
       u.origin, u.is_break_glass, coalesce(u.disabled_reason, '')::text AS disabled_reason,
       u.last_login_at, coalesce(u.scim_external_id, '')::text AS scim_external_id, 'null'::text AS group_claim_missing
  FROM principal p JOIN app_user u ON u.principal_id = p.id
 WHERE p.kind = 'user'
   AND p.name > sqlc.arg(after)::text
   AND (sqlc.arg(query)::text = '' OR p.name ILIKE '%' || sqlc.arg(query)::text || '%' OR u.email ILIKE '%' || sqlc.arg(query)::text || '%')
   AND (sqlc.arg(status)::text = ''
        OR (sqlc.arg(status)::text = 'locked' AND u.is_break_glass
            AND EXISTS (SELECT 1 FROM auth_lockout l WHERE l.account_key = 'break-glass' AND l.locked_until > now()))
        OR (sqlc.arg(status)::text <> 'locked' AND p.status = sqlc.arg(status)::text))
   AND (sqlc.arg(origin)::text = '' OR u.origin = sqlc.arg(origin)::text)
   AND (sqlc.arg(group_name)::text = '' OR EXISTS (
          SELECT 1 FROM group_membership m JOIN principal g ON g.id = m.group_id
           WHERE m.member_id = p.id AND g.name = sqlc.arg(group_name)::text))
   AND (sqlc.arg(admin)::text = '' OR (sqlc.arg(admin)::text = 'true') = (
          u.is_break_glass OR EXISTS (
            SELECT 1 FROM authz_role_binding b
             WHERE b.role = 'admin'
               AND (b.principal_id = p.id
                    OR b.principal_id IN (SELECT m.group_id FROM group_membership m WHERE m.member_id = p.id)))))
 ORDER BY p.name
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityAdminVia :many
-- Why the given users hold the role admin (R-AUTH-016 isAdmin and adminVia): "direct"
-- for a binding on the user, "group:<name>" for a binding on a group the user is a
-- member of.
SELECT b.principal_id::text AS user_id, 'direct'::text AS via
  FROM authz_role_binding b
 WHERE b.role = 'admin' AND b.principal_id = ANY(sqlc.arg(ids)::text[]::uuid[])
UNION
SELECT m.member_id::text AS user_id, 'group:' || g.name AS via
  FROM authz_role_binding b
  JOIN group_membership m ON m.group_id = b.principal_id
  JOIN principal g ON g.id = b.principal_id
 WHERE b.role = 'admin' AND m.member_id = ANY(sqlc.arg(ids)::text[]::uuid[])
 ORDER BY user_id, via;

-- name: IdentityGroupRoles :many
-- The system roles bound to a group, read only on the group record (R-AUTH-018).
SELECT role FROM authz_role_binding WHERE principal_id = sqlc.arg(group_id)::text::uuid ORDER BY role;

-- name: IdentityBreakGlassLockedUntil :many
-- The end of the lockout of the break-glass account while it is in force (R-AUTH-038);
-- no row when it is not locked.
SELECT locked_until FROM auth_lockout WHERE account_key = 'break-glass' AND locked_until > now();

-- name: IdentityUserIdentities :many
SELECT provider_id, issuer, (subject IS NOT NULL)::boolean AS linked FROM user_identity
 WHERE user_id = sqlc.arg(user_id)::text::uuid ORDER BY provider_id;

-- name: IdentityUserMemberships :many
SELECT p.name AS group_name, m.source
  FROM group_membership m JOIN principal p ON p.id = m.group_id
 WHERE m.member_id = sqlc.arg(member_id)::text::uuid
 ORDER BY p.name, m.source;

-- name: IdentityUpdateUserFields :exec
UPDATE app_user SET email = CASE WHEN sqlc.arg(set_email)::boolean THEN NULLIF(sqlc.arg(email)::text, '') ELSE email END,
                    display_name = CASE WHEN sqlc.arg(set_display_name)::boolean THEN NULLIF(sqlc.arg(display_name)::text, '') ELSE display_name END
 WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: IdentityRecordLogin :exec
UPDATE app_user SET email = NULLIF(sqlc.arg(email)::text, ''), email_verified = sqlc.arg(email_verified)::boolean,
                    display_name = NULLIF(sqlc.arg(display_name)::text, ''),
                    last_login_at = now(), last_interactive_auth_at = now()
 WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: IdentitySetUserDisabled :exec
UPDATE app_user SET disabled_reason = NULLIF(sqlc.arg(reason)::text, ''),
                    disabled_at = CASE WHEN sqlc.arg(reason)::text = '' THEN NULL ELSE now() END
 WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: IdentityUserDisabledReason :many
SELECT coalesce(disabled_reason, '')::text AS reason, is_break_glass FROM app_user WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: IdentityEndSessions :many
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE principal_id = sqlc.arg(id)::text::uuid AND ended_at IS NULL
RETURNING id::text AS id;

-- name: IdentityCountSessions :one
SELECT count(*) FROM ui_session WHERE principal_id = sqlc.arg(id)::text::uuid AND ended_at IS NULL;

-- name: IdentityUserByIdentity :many
SELECT user_id::text AS user_id FROM user_identity
 WHERE issuer = sqlc.arg(issuer)::text AND subject = sqlc.arg(subject)::text;

-- name: IdentityClaimPendingLink :many
UPDATE user_identity SET subject = sqlc.arg(subject)::text, issuer = sqlc.arg(issuer)::text, linked_at = now()
 WHERE id = (SELECT i.id FROM user_identity i JOIN principal p ON p.id = i.user_id
              WHERE i.provider_id = sqlc.arg(provider_id)::text AND i.subject IS NULL
                AND p.kind = 'user' AND p.name = sqlc.arg(username)::text
              LIMIT 1)
   AND subject IS NULL
RETURNING user_id::text AS user_id;

-- name: IdentityInsertIdentity :many
INSERT INTO user_identity (user_id, provider_id, issuer, subject, linked_at)
VALUES (sqlc.arg(user_id)::text::uuid, sqlc.arg(provider_id)::text, sqlc.arg(issuer)::text,
        NULLIF(sqlc.arg(subject)::text, ''), CASE WHEN sqlc.arg(subject)::text <> '' THEN now() END)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: IdentitySetLastClaims :exec
UPDATE user_identity SET last_claims = sqlc.arg(claims)::text::jsonb
 WHERE issuer = sqlc.arg(issuer)::text AND subject = sqlc.arg(subject)::text;

-- name: IdentityDeleteIdentitiesOfUser :one
WITH d AS (DELETE FROM user_identity WHERE user_id = sqlc.arg(user_id)::text::uuid RETURNING 1) SELECT count(*) FROM d;

-- name: IdentityReplaceIdentities :exec
-- Replaces the pending links of a user: the identities listed stay or are added
-- as pending (a linked identity is never removed here).
DELETE FROM user_identity
 WHERE user_id = sqlc.arg(user_id)::text::uuid AND subject IS NULL
   AND NOT (provider_id = ANY(sqlc.arg(providers)::text[]));

-- Groups ---------------------------------------------------------------------

-- name: IdentityInsertGroup :exec
INSERT INTO user_group (principal_id, description, auto_join, origin)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(description)::text, sqlc.arg(auto_join)::boolean, sqlc.arg(origin)::text);

-- name: IdentityGetGroup :many
SELECT p.id::text AS id, p.name, g.description, g.auto_join, g.origin, p.created_at,
       (SELECT count(DISTINCT m.member_id) FROM group_membership m WHERE m.group_id = p.id) AS member_count
  FROM principal p JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = 'group' AND p.name = sqlc.arg(name)::text;

-- name: IdentityListGroups :many
SELECT p.id::text AS id, p.name, g.description, g.auto_join, g.origin, p.created_at,
       (SELECT count(DISTINCT m.member_id) FROM group_membership m WHERE m.group_id = p.id) AS member_count
  FROM principal p JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = 'group' AND p.name > sqlc.arg(after)::text
   AND (sqlc.arg(query)::text = '' OR p.name ILIKE '%' || sqlc.arg(query)::text || '%')
   AND (sqlc.arg(origin)::text = '' OR g.origin = sqlc.arg(origin)::text)
 ORDER BY p.name
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityUpdateGroup :exec
UPDATE user_group SET description = CASE WHEN sqlc.arg(set_description)::boolean THEN sqlc.arg(description)::text ELSE description END,
                      auto_join = CASE WHEN sqlc.arg(set_auto_join)::boolean THEN sqlc.arg(auto_join)::boolean ELSE auto_join END
 WHERE principal_id = sqlc.arg(id)::text::uuid;

-- name: IdentityGroupMembers :many
SELECT mp.kind AS member_kind, mp.name AS member_name, m.source
  FROM group_membership m JOIN principal mp ON mp.id = m.member_id
 WHERE m.group_id = sqlc.arg(group_id)::text::uuid
   AND (mp.kind, mp.name) > (sqlc.arg(after_kind)::text, sqlc.arg(after_name)::text)
   AND (sqlc.arg(source)::text = '' OR m.source = sqlc.arg(source)::text)
 ORDER BY mp.kind, mp.name, m.source
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityMemberSources :many
SELECT source FROM group_membership
 WHERE group_id = sqlc.arg(group_id)::text::uuid AND member_id = sqlc.arg(member_id)::text::uuid ORDER BY source;

-- name: IdentityAddMembership :many
INSERT INTO group_membership (group_id, member_id, source)
VALUES (sqlc.arg(group_id)::text::uuid, sqlc.arg(member_id)::text::uuid, sqlc.arg(source)::text)
ON CONFLICT DO NOTHING
RETURNING group_id::text AS group_id;

-- name: IdentityRemoveMembership :many
DELETE FROM group_membership
 WHERE group_id = sqlc.arg(group_id)::text::uuid AND member_id = sqlc.arg(member_id)::text::uuid AND source = sqlc.arg(source)::text
RETURNING group_id::text AS group_id;

-- name: IdentityCountMemberships :one
SELECT count(*) FROM group_membership WHERE group_id = sqlc.arg(group_id)::text::uuid;

-- name: IdentityCountMembershipsOfMember :one
SELECT count(*) FROM group_membership WHERE member_id = sqlc.arg(member_id)::text::uuid;

-- name: IdentityOIDCMemberships :many
-- The groups a member holds through one provider.
SELECT p.id::text AS group_id, p.name AS group_name
  FROM group_membership m JOIN principal p ON p.id = m.group_id
 WHERE m.member_id = sqlc.arg(member_id)::text::uuid AND m.source = sqlc.arg(source)::text
 ORDER BY p.name;

-- name: IdentityGroupsByName :many
SELECT p.id::text AS id, p.name FROM principal p JOIN user_group g ON g.principal_id = p.id
 WHERE p.kind = 'group' AND p.name = ANY(sqlc.arg(names)::text[]);

-- name: IdentityAutoJoinGroups :many
SELECT p.id::text AS id, p.name FROM principal p JOIN user_group g ON g.principal_id = p.id
 WHERE g.auto_join ORDER BY p.name;

-- name: IdentityGroupHasAdminRole :one
SELECT EXISTS (SELECT 1 FROM authz_role_binding WHERE principal_id = sqlc.arg(id)::text::uuid);

-- Service accounts -----------------------------------------------------------

-- name: IdentityInsertServiceAccount :exec
INSERT INTO service_account (principal_id, description) VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(description)::text);

-- name: IdentityGetServiceAccount :many
SELECT p.id::text AS id, p.name, p.status, p.created_at, s.description
  FROM principal p JOIN service_account s ON s.principal_id = p.id
 WHERE p.kind = 'service_account' AND p.name = sqlc.arg(name)::text;

-- name: IdentityListServiceAccounts :many
SELECT p.id::text AS id, p.name, p.status, p.created_at, s.description
  FROM principal p JOIN service_account s ON s.principal_id = p.id
 WHERE p.kind = 'service_account' AND p.name > sqlc.arg(after)::text
   AND (sqlc.arg(query)::text = '' OR p.name ILIKE '%' || sqlc.arg(query)::text || '%')
 ORDER BY p.name
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityUpdateServiceAccount :exec
UPDATE service_account SET description = sqlc.arg(description)::text WHERE principal_id = sqlc.arg(id)::text::uuid;

-- Tokens ---------------------------------------------------------------------

-- name: IdentityTokenBySecret :many
-- The request path: one indexed lookup by the secret hash, joined with the owner.
SELECT t.id::text AS id, t.type, t.scope, t.principal_id::text AS owner_id,
       p.kind AS owner_kind, p.name AS owner_name, p.status AS owner_status, coalesce(u.is_break_glass, false) AS owner_break_glass,
       t.expires_at, coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired,
       (t.revoked_at IS NOT NULL)::boolean AS revoked, t.hint,
       coalesce(t.rejection_logged_at IS NULL OR t.rejection_logged_at < now() - interval '1 hour', true)::boolean AS rejection_due
  FROM api_token t JOIN principal p ON p.id = t.principal_id LEFT JOIN app_user u ON u.principal_id = p.id
 WHERE t.secret_sha256 = sqlc.arg(secret_sha256)::bytea;

-- name: IdentityMarkRejectionLogged :many
UPDATE api_token SET rejection_logged_at = now()
 WHERE id = sqlc.arg(id)::text::uuid AND (rejection_logged_at IS NULL OR rejection_logged_at < now() - interval '1 hour')
RETURNING id::text AS id;

-- name: IdentityCountActiveTokens :one
SELECT count(*) FROM api_token
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND type = sqlc.arg(type)::text
   AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now());

-- name: IdentityInsertToken :one
-- ttl_secs is relative to the database clock, negative for no expiry; cap_micros (Unix microseconds, 0 for none)
-- keeps a derived token from outliving the token that created it, whatever the clocks say (R-AUTH-027).
INSERT INTO api_token (id, type, secret_sha256, hint, principal_id, description, scope, self_rotatable, created_by, created_via,
                       expires_at, rotated_from, derived_from)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(type)::text, sqlc.arg(secret_sha256)::bytea, sqlc.arg(hint)::text,
        sqlc.arg(principal_id)::text::uuid, sqlc.arg(description)::text, sqlc.arg(scope)::text::jsonb, sqlc.arg(self_rotatable)::boolean,
        NULLIF(sqlc.arg(created_by)::text, '')::uuid, sqlc.arg(created_via)::text,
        CASE WHEN sqlc.arg(ttl_secs)::float8 < 0 THEN NULL
             WHEN sqlc.arg(cap_micros)::bigint > 0
                  THEN least(now() + make_interval(secs => sqlc.arg(ttl_secs)::float8), to_timestamp(sqlc.arg(cap_micros)::bigint / 1000000.0))
             ELSE now() + make_interval(secs => sqlc.arg(ttl_secs)::float8) END,
        NULLIF(sqlc.arg(rotated_from)::text, '')::uuid, NULLIF(sqlc.arg(derived_from)::text, '')::uuid)
RETURNING expires_at, created_at;

-- name: IdentityTokenRow :many
SELECT t.id::text AS id, t.type, t.hint, t.principal_id::text AS owner_id, p.kind AS owner_kind, p.name AS owner_name,
       t.description, t.scope, t.self_rotatable, t.created_at, coalesce(t.created_by::text, '')::text AS created_by,
       t.created_via, t.expires_at, t.revoked_at, coalesce(t.revoked_by::text, '')::text AS revoked_by,
       coalesce(t.revocation_reason, '')::text AS revocation_reason, coalesce(t.rotated_from::text, '')::text AS rotated_from,
       coalesce(t.derived_from::text, '')::text AS derived_from, t.last_used_at, coalesce(host(t.last_used_addr), '')::text AS last_used_addr,
       coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired, t.ci_context
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE t.id = sqlc.arg(id)::text::uuid;

-- name: IdentityListTokens :many
-- Keyset order by id descending (a UUIDv7 sorts by creation time).
SELECT t.id::text AS id, t.type, t.hint, t.principal_id::text AS owner_id, p.kind AS owner_kind, p.name AS owner_name,
       t.description, t.scope, t.self_rotatable, t.created_at, coalesce(t.created_by::text, '')::text AS created_by,
       t.created_via, t.expires_at, t.revoked_at, coalesce(t.revoked_by::text, '')::text AS revoked_by,
       coalesce(t.revocation_reason, '')::text AS revocation_reason, coalesce(t.rotated_from::text, '')::text AS rotated_from,
       coalesce(t.derived_from::text, '')::text AS derived_from, t.last_used_at, coalesce(host(t.last_used_addr), '')::text AS last_used_addr,
       coalesce(t.expires_at IS NOT NULL AND t.expires_at <= now(), false)::boolean AS expired, t.ci_context
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE (sqlc.arg(after)::text = '' OR t.id < sqlc.arg(after)::text::uuid)
   AND (sqlc.arg(owner_id)::text = '' OR t.principal_id = sqlc.arg(owner_id)::text::uuid)
   AND (sqlc.arg(type)::text = '' OR t.type = sqlc.arg(type)::text)
   AND (CASE sqlc.arg(state)::text
          WHEN 'active'  THEN t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > now())
          WHEN 'expired' THEN t.revoked_at IS NULL AND t.expires_at IS NOT NULL AND t.expires_at <= now()
          WHEN 'revoked' THEN t.revoked_at IS NOT NULL
          ELSE true END)
   AND (sqlc.arg(expires_before_unix)::bigint = 0 OR t.expires_at < to_timestamp(sqlc.arg(expires_before_unix)::bigint))
   AND (sqlc.arg(expires_after_unix)::bigint = 0 OR t.expires_at > to_timestamp(sqlc.arg(expires_after_unix)::bigint))
   AND (sqlc.arg(last_used_before_unix)::bigint = 0 OR t.last_used_at IS NULL OR t.last_used_at < to_timestamp(sqlc.arg(last_used_before_unix)::bigint))
 ORDER BY t.id DESC
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityRevokeCascade :many
-- Revokes a token and, in the same statement, every unrevoked token whose
-- derived_from chain leads to it; with follow_rotation also the rotation
-- successors and their derived tokens (R-AUTH-029). The root keeps its own
-- revocation fields when it is already revoked.
WITH RECURSIVE tree(id) AS (
    SELECT t0.id FROM api_token t0 WHERE t0.id = sqlc.arg(id)::text::uuid
    UNION
    SELECT c.id FROM api_token c JOIN tree ON c.derived_from = tree.id
     OR (sqlc.arg(follow_rotation)::boolean AND c.rotated_from = tree.id)
)
UPDATE api_token a
   SET revoked_at = now(), revoked_by = NULLIF(sqlc.arg(revoked_by)::text, '')::uuid,
       revocation_reason = CASE WHEN a.id = sqlc.arg(id)::text::uuid THEN sqlc.arg(reason)::text ELSE 'parent_revoked' END
  FROM tree
 WHERE a.id = tree.id AND a.revoked_at IS NULL
RETURNING a.id::text AS id, encode(a.secret_sha256, 'hex') AS secret_hex, a.principal_id::text AS owner_id,
          a.type, (a.id = sqlc.arg(id)::text::uuid) AS is_root;

-- name: IdentityTokenBySecretForRevoke :many
SELECT t.id::text AS id, t.principal_id::text AS owner_id, p.name AS owner_name, p.kind AS owner_kind
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE t.secret_sha256 = sqlc.arg(secret_sha256)::bytea;

-- name: IdentityRevokeAllOfPrincipal :many
UPDATE api_token SET revoked_at = now(), revoked_by = NULLIF(sqlc.arg(revoked_by)::text, '')::uuid,
                     revocation_reason = sqlc.arg(reason)::text
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND revoked_at IS NULL
RETURNING id::text AS id;

-- name: IdentityCountTokensOfPrincipal :one
SELECT count(*) FROM api_token WHERE principal_id = sqlc.arg(principal_id)::text::uuid;

-- name: IdentitySetTokenExpiry :many
-- Rotation: the predecessor expires at the earlier of its expiry and now + overlap.
UPDATE api_token SET expires_at = least(coalesce(expires_at, 'infinity'::timestamptz), now() + make_interval(secs => sqlc.arg(overlap_secs)::float8))
 WHERE id = sqlc.arg(id)::text::uuid AND revoked_at IS NULL
RETURNING expires_at, encode(secret_sha256, 'hex') AS secret_hex;

-- name: IdentityDeleteExpiredTokens :one
WITH d AS (
    DELETE FROM api_token WHERE id IN (
        SELECT id FROM api_token
         WHERE least(coalesce(revoked_at, 'infinity'::timestamptz), coalesce(expires_at, 'infinity'::timestamptz))
               < now() - make_interval(secs => sqlc.arg(retention_secs)::float8)
         ORDER BY id LIMIT sqlc.arg(max_rows)::int)
    RETURNING 1)
SELECT count(*) FROM d;

-- name: IdentityTokensEnteringNotice :many
SELECT t.id::text AS id, t.type, t.hint, t.description, t.expires_at, p.kind AS owner_kind, p.name AS owner_name, p.id::text AS owner_id
  FROM api_token t JOIN principal p ON p.id = t.principal_id
 WHERE t.revoked_at IS NULL AND t.type IN ('pat', 'sat') AND t.expiry_notified_at IS NULL
   AND t.expires_at IS NOT NULL AND t.expires_at > now()
   AND t.expires_at <= now() + make_interval(secs => sqlc.arg(notice_secs)::float8)
 ORDER BY t.expires_at
 LIMIT sqlc.arg(max_rows)::int;

-- name: IdentityMarkExpiryNotified :many
UPDATE api_token SET expiry_notified_at = now() WHERE id = sqlc.arg(id)::text::uuid AND expiry_notified_at IS NULL
RETURNING id::text AS id;

-- Runtime settings -----------------------------------------------------------

-- name: IdentityGetSettings :many
-- Intervals travel as seconds; -1 is NULL (the YAML value applies).
SELECT anonymous_access,
       coalesce(extract(epoch FROM pat_default_ttl), -1)::float8 AS pat_default_secs,
       coalesce(extract(epoch FROM pat_max_ttl), -1)::float8 AS pat_max_secs,
       coalesce(extract(epoch FROM sat_default_ttl), -1)::float8 AS sat_default_secs,
       coalesce(extract(epoch FROM sat_max_ttl), -1)::float8 AS sat_max_secs,
       coalesce(extract(epoch FROM expiry_notice), -1)::float8 AS expiry_notice_secs,
       (extract(epoch FROM updated_at) * 1000000)::bigint AS revision
  FROM auth_settings;

-- name: IdentityUpdateSettings :exec
UPDATE auth_settings
   SET anonymous_access = sqlc.arg(anonymous_access)::boolean,
       pat_default_ttl = CASE WHEN sqlc.arg(pat_default_secs)::float8 < 0 THEN NULL ELSE make_interval(secs => sqlc.arg(pat_default_secs)::float8) END,
       pat_max_ttl = CASE WHEN sqlc.arg(pat_max_secs)::float8 < 0 THEN NULL ELSE make_interval(secs => sqlc.arg(pat_max_secs)::float8) END,
       sat_default_ttl = CASE WHEN sqlc.arg(sat_default_secs)::float8 < 0 THEN NULL ELSE make_interval(secs => sqlc.arg(sat_default_secs)::float8) END,
       sat_max_ttl = CASE WHEN sqlc.arg(sat_max_secs)::float8 < 0 THEN NULL ELSE make_interval(secs => sqlc.arg(sat_max_secs)::float8) END,
       expiry_notice = CASE WHEN sqlc.arg(expiry_notice_secs)::float8 < 0 THEN NULL ELSE make_interval(secs => sqlc.arg(expiry_notice_secs)::float8) END,
       updated_at = greatest(now(), updated_at + interval '1 microsecond'), updated_by = NULLIF(sqlc.arg(updated_by)::text, '')::uuid
 WHERE singleton;

-- Group members, one row per member (R-AUTH-018) --------------------------------

-- name: IdentityGroupMemberPage :many
-- The members of a group ordered by kind and name, each with all its sources; the
-- optional source keeps the members that hold it. after_kind and after_name are the
-- last member of the previous page ('' on the first).
SELECT mp.kind AS member_kind, mp.name AS member_name, array_agg(m.source ORDER BY m.source)::text[] AS sources
  FROM group_membership m JOIN principal mp ON mp.id = m.member_id
 WHERE m.group_id = sqlc.arg(group_id)::text::uuid
 GROUP BY mp.kind, mp.name
HAVING (mp.kind, mp.name) > (sqlc.arg(after_kind)::text, sqlc.arg(after_name)::text)
   AND (sqlc.arg(source)::text = '' OR sqlc.arg(source)::text = ANY(array_agg(m.source)))
 ORDER BY mp.kind, mp.name
 LIMIT sqlc.arg(max_rows)::int;

-- User preferences (R-API-021) ----------------------------------------------------

-- name: PreferencesGet :many
SELECT coalesce(locale, '')::text AS locale, coalesce(theme, '')::text AS theme, keyboard_shortcuts,
       coalesce(project_filter, '')::text AS project_filter, revision
  FROM user_preferences WHERE user_id = sqlc.arg(user_id)::text::uuid;

-- name: PreferencesUpsert :many
-- A user without a row has the defaults at revision 1; the first write inserts
-- revision 2. if_revision is 0 for "no condition" (If-Match: *); a row that exists
-- is updated only when its revision matches, so a concurrent write is a 412.
INSERT INTO user_preferences AS p (user_id, locale, theme, keyboard_shortcuts, project_filter, revision)
VALUES (sqlc.arg(user_id)::text::uuid, NULLIF(sqlc.arg(locale)::text, ''), NULLIF(sqlc.arg(theme)::text, ''),
        sqlc.arg(keyboard_shortcuts)::boolean, NULLIF(sqlc.arg(project_filter)::text, ''), 2)
ON CONFLICT (user_id) DO UPDATE
   SET locale = EXCLUDED.locale, theme = EXCLUDED.theme, keyboard_shortcuts = EXCLUDED.keyboard_shortcuts,
       project_filter = EXCLUDED.project_filter, revision = p.revision + 1, updated_at = now()
 WHERE sqlc.arg(if_revision)::bigint = 0 OR p.revision = sqlc.arg(if_revision)::bigint
RETURNING coalesce(p.locale, '')::text AS locale, coalesce(p.theme, '')::text AS theme, p.keyboard_shortcuts,
          coalesce(p.project_filter, '')::text AS project_filter, p.revision;
