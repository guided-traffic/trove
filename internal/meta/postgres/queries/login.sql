-- Queries of the login area (WP1.1): UI sessions (R-AUTH-010 to R-AUTH-013),
-- browser login flows (R-AUTH-005, R-AUTH-006), the break-glass account and its
-- lockout (R-AUTH-014, R-AUTH-038) and the session-cleanup task (R-AUTH-043).
--
-- The session and oidc packages are domain packages and import no driver
-- (R-ARCH-002), so every parameter is a plain Go type: identifiers travel as
-- text and are cast to uuid here, durations as seconds. Times that a replica
-- compares are returned as ages relative to the database's now() (seconds), so a
-- replica turns them into deadlines on its own monotonic clock and no two clocks
-- are ever compared. Statements that return at most one row are :many, so the
-- caller never meets the driver's no-rows error.

-- name: LockPrincipal :many
-- The principal row lock of R-AUTH-006 step 10 and R-AUTH-010: logins of one
-- user and the session limit run one after the other on every replica.
SELECT id::text AS id FROM principal WHERE id = sqlc.arg(id)::text::uuid FOR UPDATE;

-- name: SessionBySecret :many
-- The validation read of R-AUTH-010 with the principal's status. Ages are in
-- seconds against the database clock.
SELECT s.id::text AS id, s.csrf_sha256, s.principal_id::text AS principal_id,
       p.kind AS principal_kind, p.name AS principal_name, p.status AS principal_status,
       coalesce(u.is_break_glass, false)::bool AS is_break_glass,
       s.auth_method, coalesce(s.provider_id, '')::text AS provider_id,
       s.authenticated_at, (s.ended_at IS NOT NULL)::bool AS ended,
       extract(epoch FROM now() - s.last_seen_at)::float8 AS seen_ago_s,
       extract(epoch FROM s.idle_timeout)::float8 AS idle_s,
       extract(epoch FROM s.absolute_expires_at - now())::float8 AS absolute_left_s,
       extract(epoch FROM now() - s.authenticated_at)::float8 AS authenticated_ago_s
  FROM ui_session s
  JOIN principal p ON p.id = s.principal_id
  LEFT JOIN app_user u ON u.principal_id = s.principal_id
 WHERE s.secret_sha256 = sqlc.arg(secret_sha256)::bytea;

-- name: TouchSession :many
-- The conditional touch of R-AUTH-010: at most one change per session and minute
-- whatever the number of replicas. It returns the row's last_seen_at age whether
-- or not it changed the row; the outer SELECT reads the snapshot before the
-- update, so the CTE's value wins when there is one.
WITH upd AS (
    UPDATE ui_session SET last_seen_at = now()
     WHERE id = sqlc.arg(id)::text::uuid AND ended_at IS NULL
       AND last_seen_at < now() - interval '60 seconds'
    RETURNING last_seen_at
)
SELECT extract(epoch FROM now() - coalesce((SELECT last_seen_at FROM upd), s.last_seen_at))::float8 AS seen_ago_s
  FROM ui_session s
 WHERE s.id = sqlc.arg(id)::text::uuid AND s.ended_at IS NULL;

-- name: EndSessionIfIdle :many
-- Ends a session whose idle deadline has passed according to the row
-- (R-AUTH-010, Q-AUTH-13). No row back: another replica touched it meanwhile.
UPDATE ui_session SET ended_at = now(), end_reason = 'idle'
 WHERE id = sqlc.arg(id)::text::uuid AND ended_at IS NULL AND last_seen_at + idle_timeout <= now()
RETURNING id::text AS id;

-- name: EndSessionIfAbsolute :many
UPDATE ui_session SET ended_at = now(), end_reason = 'absolute'
 WHERE id = sqlc.arg(id)::text::uuid AND ended_at IS NULL AND absolute_expires_at <= now()
RETURNING id::text AS id;

-- name: LiveSessionsOfPrincipal :many
-- The sessions that count against max_per_user, oldest first (R-AUTH-010).
SELECT id::text AS id
  FROM ui_session
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND ended_at IS NULL
   AND absolute_expires_at > now() AND last_seen_at + idle_timeout > now()
 ORDER BY created_at, id;

-- name: EndSessionsByID :many
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE id = ANY(sqlc.arg(ids)::text[]::uuid[]) AND ended_at IS NULL
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: EndSessionBySecret :many
-- The fixation rule of R-AUTH-010: the cookie presented at sign-in is ended.
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE secret_sha256 = sqlc.arg(secret_sha256)::bytea AND ended_at IS NULL
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: InsertSession :exec
INSERT INTO ui_session (id, secret_sha256, csrf_sha256, principal_id, auth_method, provider_id, idp_sid, id_token,
                        authenticated_at, idle_timeout, absolute_expires_at, created_addr, user_agent)
VALUES (sqlc.arg(id)::text::uuid, sqlc.arg(secret_sha256)::bytea, sqlc.arg(csrf_sha256)::bytea,
        sqlc.arg(principal_id)::text::uuid, sqlc.arg(auth_method)::text,
        NULLIF(sqlc.arg(provider_id)::text, ''), NULLIF(sqlc.arg(idp_sid)::text, ''), NULLIF(sqlc.arg(id_token)::text, ''),
        now(), make_interval(secs => sqlc.arg(idle_s)::float8), now() + make_interval(secs => sqlc.arg(absolute_s)::float8),
        NULLIF(sqlc.arg(created_addr)::text, '')::inet, NULLIF(sqlc.arg(user_agent)::text, ''));

-- name: SessionForLogout :many
-- Logout reads the row in any state: it is idempotent (R-AUTH-013).
SELECT s.id::text AS id, s.csrf_sha256, s.principal_id::text AS principal_id,
       coalesce(s.provider_id, '')::text AS provider_id, coalesce(s.id_token, '')::text AS id_token,
       (s.ended_at IS NULL AND s.absolute_expires_at > now() AND s.last_seen_at + s.idle_timeout > now())::bool AS live
  FROM ui_session s
 WHERE s.secret_sha256 = sqlc.arg(secret_sha256)::bytea;

-- name: EndSessionsOfPrincipal :many
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND ended_at IS NULL
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: EndAllSessions :many
-- trove admin sessions end --all (R-AUTH-015), in batches.
UPDATE ui_session SET ended_at = now(), end_reason = 'revoked'
 WHERE id IN (SELECT id FROM ui_session WHERE ended_at IS NULL LIMIT 1000)
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: EndBreakGlassSessions :many
-- Every open break-glass session (R-AUTH-014: rotation, disabled account,
-- trove admin break-glass end-sessions).
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE auth_method = 'break_glass' AND ended_at IS NULL
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: ListLiveSessionsOfPrincipal :many
-- The session list of R-AUTH-010 (wave 2 API) and of trove admin.
SELECT id::text AS id, created_at, last_seen_at, coalesce(host(created_addr), '')::text AS created_addr,
       coalesce(user_agent, '')::text AS user_agent, auth_method, coalesce(provider_id, '')::text AS provider_id
  FROM ui_session
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND ended_at IS NULL
   AND absolute_expires_at > now() AND last_seen_at + idle_timeout > now()
 ORDER BY created_at DESC, id;

-- name: CountLiveBreakGlassSessions :many
SELECT count(*)::bigint AS n
  FROM ui_session
 WHERE auth_method = 'break_glass' AND ended_at IS NULL
   AND absolute_expires_at > now() AND last_seen_at + idle_timeout > now();

-- name: UserIDByName :many
SELECT id::text AS id FROM principal WHERE kind = 'user' AND name = sqlc.arg(name)::text;

-- name: DeleteOldSessions :execrows
-- session-cleanup (R-AUTH-043): rows 30 days after their end; a session that
-- expired without a request ends at the earlier of its two deadlines (R-AUTH-010).
DELETE FROM ui_session
 WHERE id IN (
    SELECT id FROM ui_session
     WHERE (ended_at IS NOT NULL AND ended_at < now() - interval '30 days')
        OR (ended_at IS NULL AND least(absolute_expires_at, last_seen_at + idle_timeout) < now() - interval '30 days')
     LIMIT 1000);

-- name: InsertLoginFlow :exec
-- The flow state of R-AUTH-005; state and nonce are stored as hashes only.
INSERT INTO oidc_login_flow (state_sha256, provider_id, browser_binding_sha256, code_verifier, nonce_sha256,
                             purpose, reauth_session_id, return_to, expires_at)
VALUES (sqlc.arg(state_sha256)::bytea, sqlc.arg(provider_id)::text, sqlc.arg(browser_binding_sha256)::bytea,
        sqlc.arg(code_verifier)::text, sqlc.arg(nonce_sha256)::bytea, sqlc.arg(purpose)::text,
        NULLIF(sqlc.arg(reauth_session_id)::text, '')::uuid, sqlc.arg(return_to)::text,
        now() + make_interval(secs => sqlc.arg(ttl_s)::float8));

-- name: TakeLoginFlow :many
-- R-AUTH-006 step 1: DELETE ... RETURNING makes the flow single-use on any
-- replica; it commits on its own, before any outbound call.
DELETE FROM oidc_login_flow WHERE state_sha256 = sqlc.arg(state_sha256)::bytea
RETURNING provider_id, browser_binding_sha256, code_verifier, nonce_sha256, purpose,
          coalesce(reauth_session_id::text, '')::text AS reauth_session_id, return_to,
          (expires_at > now())::bool AS live;

-- name: CountPendingFlows :many
-- max_pending_flows of R-AUTH-038 counts the unexpired browser and command-line flows.
SELECT ((SELECT count(*) FROM oidc_login_flow WHERE expires_at > now())
      + (SELECT count(*) FROM cli_login WHERE expires_at > now() AND status IN ('pending', 'approved')))::bigint AS n;

-- name: DeleteExpiredLoginFlows :execrows
DELETE FROM oidc_login_flow
 WHERE state_sha256 IN (SELECT state_sha256 FROM oidc_login_flow WHERE expires_at <= now() LIMIT 1000);

-- name: DeleteExpiredCLILogins :execrows
DELETE FROM cli_login
 WHERE id IN (SELECT id FROM cli_login WHERE expires_at <= now() LIMIT 1000);

-- name: UserByIdentity :many
-- The re-authentication check of R-AUTH-011: whose identity the ID token proves.
SELECT user_id::text AS user_id FROM user_identity
 WHERE issuer = sqlc.arg(issuer)::text AND subject = sqlc.arg(subject)::text;

-- name: GetLockout :many
-- Every local-login attempt reads the row; no replica caches it (R-AUTH-038).
-- locked_for_s and hold_for_s are the seconds left, 0 when not in force.
SELECT failures,
       CASE WHEN locked_until > now() THEN extract(epoch FROM locked_until - now()) ELSE 0 END::float8 AS locked_for_s,
       CASE WHEN password_changed_at > now() - make_interval(secs => sqlc.arg(hold_s)::float8)
            THEN extract(epoch FROM password_changed_at + make_interval(secs => sqlc.arg(hold_s)::float8) - now())
            ELSE 0 END::float8 AS hold_for_s,
       locked_until, password_changed_at
  FROM auth_lockout
 WHERE account_key = sqlc.arg(account_key)::text;

-- name: RecordLockoutFailure :many
-- One failure, counted atomically on any replica (R-AUTH-038). A window that has
-- passed, or a lock that has expired, starts the count again. locked_now is true
-- for exactly the failure that locked the account.
INSERT INTO auth_lockout AS l (account_key, failures, window_started_at, locked_until)
VALUES (sqlc.arg(account_key)::text, 1, now(),
        CASE WHEN 1 >= sqlc.arg(threshold)::int THEN now() + make_interval(secs => sqlc.arg(duration_s)::float8) END)
ON CONFLICT (account_key) DO UPDATE SET
    failures = CASE WHEN l.window_started_at <= now() - make_interval(secs => sqlc.arg(window_s)::float8)
                          OR l.locked_until <= now() THEN 1 ELSE l.failures + 1 END,
    window_started_at = CASE WHEN l.window_started_at <= now() - make_interval(secs => sqlc.arg(window_s)::float8)
                                   OR l.locked_until <= now() THEN now() ELSE l.window_started_at END,
    locked_until = CASE
        WHEN l.locked_until > now() THEN l.locked_until
        WHEN (CASE WHEN l.window_started_at <= now() - make_interval(secs => sqlc.arg(window_s)::float8)
                         OR l.locked_until <= now() THEN 1 ELSE l.failures + 1 END) >= sqlc.arg(threshold)::int
             THEN now() + make_interval(secs => sqlc.arg(duration_s)::float8)
        WHEN l.locked_until <= now() THEN NULL
        ELSE l.locked_until END
RETURNING failures, (locked_until IS NOT NULL AND locked_until = now() + make_interval(secs => sqlc.arg(duration_s)::float8))::bool AS locked_now,
          locked_until;

-- name: ResetLockoutFailures :exec
UPDATE auth_lockout SET failures = 0, window_started_at = now()
 WHERE account_key = sqlc.arg(account_key)::text AND failures > 0;

-- name: Unlock :many
-- trove admin break-glass unlock (R-AUTH-015); was_locked is the state before.
WITH old AS (
    SELECT account_key, (locked_until > now())::bool AS was_locked
      FROM auth_lockout WHERE account_key = sqlc.arg(account_key)::text FOR UPDATE
)
UPDATE auth_lockout l SET locked_until = NULL, failures = 0, window_started_at = now()
  FROM old WHERE l.account_key = old.account_key
RETURNING coalesce(old.was_locked, false)::bool AS was_locked;

-- name: OpenRotationHold :many
-- R-AUTH-014, Q-AUTH-14: the first replica that notices a changed break-glass
-- password opens the hold; concurrent detections serialize on the row, so one
-- rotation opens one hold. A row back means this call opened it.
INSERT INTO auth_lockout AS l (account_key, failures, window_started_at, password_changed_at)
VALUES (sqlc.arg(account_key)::text, 0, now(), now())
ON CONFLICT (account_key) DO UPDATE SET password_changed_at = now()
 WHERE l.password_changed_at IS NULL OR l.password_changed_at <= now() - make_interval(secs => sqlc.arg(hold_s)::float8)
RETURNING account_key;

-- name: BreakGlassUser :many
SELECT p.id::text AS id, p.name, p.status
  FROM app_user u JOIN principal p ON p.id = u.principal_id
 WHERE u.is_break_glass;

-- name: InsertBreakGlassPrincipal :many
-- The break-glass user row (R-AUTH-014): a user with origin system. A name taken
-- by another user leaves no row; the caller reports it.
INSERT INTO principal (id, kind, name) VALUES (sqlc.arg(id)::text::uuid, 'user', sqlc.arg(name)::text)
ON CONFLICT (kind, name) DO NOTHING
RETURNING id::text AS id;

-- name: InsertBreakGlassUser :exec
INSERT INTO app_user (principal_id, origin, is_break_glass, display_name)
VALUES (sqlc.arg(principal_id)::text::uuid, 'system', true, 'Break-glass administrator')
ON CONFLICT DO NOTHING;

-- name: RenameBreakGlassUser :execrows
UPDATE principal SET name = sqlc.arg(name)::text WHERE id = sqlc.arg(id)::text::uuid AND name <> sqlc.arg(name)::text;

-- name: PrincipalKindByName :many
SELECT p.id::text AS id, coalesce(u.is_break_glass, false)::bool AS is_break_glass
  FROM principal p LEFT JOIN app_user u ON u.principal_id = p.id
 WHERE p.kind = 'user' AND p.name = sqlc.arg(name)::text;

-- name: EndSessionOfPrincipal :many
-- One live session of a principal (DELETE /users/me/sessions/{id}, R-AUTH-010); a
-- session of another principal or an ended one matches nothing.
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE id = sqlc.arg(id)::text::uuid AND principal_id = sqlc.arg(principal_id)::text::uuid AND ended_at IS NULL
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;

-- name: EndSessionsOfPrincipalExcept :many
-- Every open session of a principal but one (DELETE /users/me/sessions?keepCurrent=true).
UPDATE ui_session SET ended_at = now(), end_reason = sqlc.arg(reason)::text
 WHERE principal_id = sqlc.arg(principal_id)::text::uuid AND ended_at IS NULL
   AND id <> sqlc.arg(keep_id)::text::uuid
RETURNING id::text AS id, encode(secret_sha256, 'hex')::text AS secret_sha256_hex;
