# Login and sessions

Status: implemented in M1 (WP1.1) for the browser login through OpenID Connect, the server-side UI session, CSRF, logout and
the break-glass account, in [internal/auth/oidc](../../internal/auth/oidc) and [internal/auth/session](../../internal/auth/session).
The authentication stage that reads the session cookie on every request belongs to the identity area and calls
[`session.Validator`](../../internal/auth/session/session.go); the provisioner that creates users and synchronizes groups is the
identity area's too ([`principal.Provisioner`](../../internal/auth/principal/principal.go)). Requirements: R-AUTH-003 to R-AUTH-015,
R-AUTH-041 to R-AUTH-043 in [identity-and-authentication.md](../spec/identity-and-authentication.md).

This page says whom the login trusts with what, where each login secret lives, which checks a request passes, and how an end, a
lock or a rotation reaches every replica.

## Parties and what each is trusted with

```text
  browser ──(1) /auth/oidc/{p}/start──► trove web replica ──flow row──► PostgreSQL
     │                                        │                            ▲
     │◄──302 to the IdP (state, nonce, PKCE)──┘                            │
     ├──(2) sign-in at the IdP──► IdP                                      │
     │◄──302 code, state, iss────┘                                         │
     └──(3) /auth/oidc/{p}/callback──► any web replica ──DELETE flow──────►│
                                          │──token, JWKS, userinfo──► IdP (outbound client, 10 s each)
                                          │──provisioner (identity) + session row──────────────────────►│
     ◄──__Host-trv_session, __Host-trv_csrf──┘
  Valkey: change messages only; never read for a session, a flow or a lock
```

| Party | Trusted with | Not trusted with |
|---|---|---|
| The IdP | the identity (`iss`, `sub`), the mapped claims, through group rules possibly administrator rights | the redirect target (trove derives every URL from `server.base_url`), the algorithm (`allowed_signing_algs`, never `none` or `HS*`) |
| The browser | holding the three `__Host-` cookies | the `Host` header, `return_to` (only `/ui/…` paths), the origin of an unsafe request (checked) |
| PostgreSQL | every login state: flows, sessions, lockout, rotation hold | — |
| Valkey | nothing of the login: it carries the change messages of the area `auth` as early evictions | sessions, flows, locks; a lost or forged message neither hides nor delays an end |
| A sibling subdomain | nothing | planting or shadowing a cookie: the `__Host-` prefix refuses `Domain` and a path other than `/` (Q-AUTH-2) |

## Where the login secrets live

| Secret | Where it lives | Where it never is |
|---|---|---|
| Session secret (`__Host-trv_session`, 32 random bytes) | the browser; PostgreSQL holds its SHA-256 (`ui_session.secret_sha256`) | logs, events, Valkey, the change log (a change names the hash of an ended secret, [changes.go](../../internal/auth/session/changes.go)) |
| CSRF token (`__Host-trv_csrf`, readable by the UI) | the browser; its SHA-256 in `ui_session.csrf_sha256` | as above |
| Browser binding (`__Host-trv_oidc_binding`, 10 min) | the browser; its SHA-256 in `oidc_login_flow` | as above |
| `state` and `nonce` | the authorization request and the ID token; their SHA-256 in `oidc_login_flow` | — |
| PKCE `code_verifier` | `oidc_login_flow.code_verifier` in clear for at most 10 minutes; the row is deleted by the callback that uses it (`DELETE … RETURNING`) or by `session-cleanup` | backups: the table is `transient` |
| The provider's ID token | `ui_session.id_token`, the `id_token_hint` of RP-initiated logout (R-AUTH-013) | every API answer; backups (`transient`) |
| The provider's access and refresh token | the callback's memory until userinfo has answered | anywhere afterwards (INV-8) |
| OIDC client secret | a mounted file, read again when its modification time or size changes ([provider.go](../../internal/auth/oidc/provider.go), `secretSource`), or one environment variable | PostgreSQL, the configuration file |
| Break-glass password | a mounted file on `web` replicas; the replica keeps the Argon2id salt and key and the SHA-256 of the file, never the plaintext ([argon.go](../../internal/auth/oidc/argon.go)) | PostgreSQL, `worker` pods, logs and events (the presented username is recorded, the password never) |

## Checks a login passes

- **Start** ([flow.go](../../internal/auth/oidc/flow.go), `HandleStart`): a known provider whose discovery succeeded; the
  pending-flow count under `max_pending_flows`; for `purpose=reauth` a live session; `state`, `nonce` and verifier from
  `crypto/rand`; `return_to` reduced to a `/ui/` path ([service.go](../../internal/auth/oidc/service.go), `SafeReturnTo`).
- **Rate limits** ([service.go](../../internal/auth/oidc/service.go), `allow` and `pendingFull`): `GET /auth/oidc/{providerId}/start`
  and `POST /auth/local/login` take one count of the limiter `auth.flow_starts` per client address
  (`auth.rate_limit.starts_per_address` per minute) and answer 429 with `Retry-After` above it. The limiter is the cluster
  limiter set's, counted in Valkey and falling back to per-replica shares while Valkey is away
  (`wireFailureLimiter`, [wire_api.go](../../internal/app/wire_api.go)); a refused request still counts, so an address that keeps
  sending stays refused. `auth.rate_limit.max_pending_flows` unexpired flow rows answer 503 `too_many_pending_logins`; the count
  is read from PostgreSQL at most every 5 s per replica.
- **Callback**, in the order of R-AUTH-006: the flow row is deleted and returned in one statement that commits before any
  outbound call, so a callback is single-use on every replica and holds no transaction while the IdP answers; the flow's
  provider is the path's (mix-up defense); the binding cookie's SHA-256 is the flow's (login CSRF); `iss` per RFC 9207; then the
  code exchange with the verifier.
- **ID token** ([jws.go](../../internal/auth/oidc/jws.go), [idtoken.go](../../internal/auth/oidc/idtoken.go)): compact JWS only;
  `RS*`, `PS*`, `ES256`, `ES384` within `allowed_signing_algs`; the key by `kid` from the provider's JWK Set, of the algorithm's
  type and curve, RSA at least 2048 bits, `use` absent or `sig`; `crit` refused; then `iss`, `aud` and `azp`, `exp`/`nbf` with
  `clock_skew`, `iat` within `max_id_token_age`, the nonce compared by hash in constant time, `sub` present (Q-AUTH-31).
- **Claims and gates** ([claims.go](../../internal/auth/oidc/claims.go)): the username in NFC, lower case, with the prefix,
  against the pattern and the reserved names (the break-glass username among them); `require_verified_email`;
  `login_requires_groups`; the group rules, whose results outside the group-name rule are dropped. All of this runs before the
  provisioner sees the login, so a gated identity creates no user (Q-AUTH-36).
- **Re-authentication**: the ID token's (`iss`, `sub`) must be linked to the session's user before anything is provisioned, and
  the provisioner's principal must be that user again (R-AUTH-011).

## Sessions and their end

- **Creation** ([store.go](../../internal/auth/session/store.go), `Create`): one transaction that locks the principal row, ends
  the session the browser presented (fixation) or the one a re-authentication replaces, ends the oldest over `max_per_user`, and
  inserts the new row. Concurrent logins of one user on several replicas cannot exceed the limit.
- **Validation** (`Validate`): a malformed cookie is refused without a query. The replica answers from its `auth_sessions` cache
  only while its last change-log poll started less than `auth.revocation_bound` ago; otherwise it reads PostgreSQL, sharing one
  query per credential, and a database that does not answer gives 503, never an acceptance (Q-AUTH-10). The idle and absolute
  deadlines are decided from the row: a replica whose own view has passed a deadline reads the row again and ends the session
  only if the row says so (Q-AUTH-13).
- **Ends** (`Logout`, `EndForPrincipal`, `EndBreakGlass`, `EndAll`): the end and its change message of the area `auth` commit
  together, so every replica drops the session within the revocation bound whatever Valkey does.

## CSRF

[csrf.go](../../internal/auth/session/csrf.go): an unsafe request authenticated by the cookie needs the `X-CSRF-Token` header
whose SHA-256 is the session's (constant-time compare) and an `Origin` equal to the base URL's origin, or no `Origin` and
`Sec-Fetch-Site: same-origin`; `null` and both headers absent are refused. The local login applies the origin rule alone.
`POST /auth/logout` passes both; `GET /auth/logout` is not routed and answers 405. The error page and the redirects send
`Cache-Control: no-store`; the error page also `frame-ancestors 'none'`.

## The break-glass account

[breakglass.go](../../internal/auth/oidc/breakglass.go):

- Every attempt reads the `auth_lockout` row; a lock answers 429 even for the right password, a rotation hold 503. Failures are
  counted in one `INSERT … ON CONFLICT DO UPDATE`, so attempts on several replicas at the same moment are all counted, and a
  Valkey flush cannot reset the count.
- One Argon2id verification per attempt, for unknown usernames too, at least `m=65536,t=3,p=4`, in one of
  `max_concurrent_verifications` slots of the replica; no slot within 1 s gives 503 `server_busy`.
- A changed password file opens the rotation hold in the same transaction that ends every break-glass session; only after that
  commit does the replica switch to the new file. A rotated file that holds no usable password refuses every local login instead
  of keeping the old one (Q-AUTH-14).
- A replica with the account disabled ends the remaining break-glass sessions at start and every break-glass session presented
  to it.
- Every login decision is recorded: `auth.login.succeeded` and `auth.login.failed`, `auth.breakglass.login` and
  `auth.breakglass.locked`, `auth.logout` and `auth.session.ended` go to the audit recorder of the replica
  ([events_login.go](../../internal/audit/events_login.go)); the `trove admin` commands write their events (actor `admin-cli`,
  pod and OS user) through an audit writer of their own, in the transaction of the change.
- The single factor and the IdP's authentication strength are [H-17](../spec/identity-and-authentication.md#h-17--break-glass-account-is-single-factor)
  and [H-18](../spec/identity-and-authentication.md#h-18--authentication-strength-at-the-idp-is-not-checked); a rotation that no
  running replica notices is [H-52](../spec/identity-and-authentication.md#h-52--a-break-glass-rotation-that-no-running-replica-notices).

## What this does not cover

### H-91 · Provisioning and the session commit in two transactions

Stage: the callback, after the ID token is valid (R-AUTH-006 step 10, [flow.go](../../internal/auth/oidc/flow.go), `callback`).
Subject: the user's claims and `oidc:<provider>` memberships. Live in this build.

The provisioner applies the claims and the group memberships in its own transaction; the session is created in a second one
(Q-AUTH-32). A replica that dies, or a session insert that fails, between the two commits leaves the memberships of that login in
force without a session. The memberships are those the IdP vouched for at that moment, so the effect equals a login the user
abandoned after the callback; no user is half-created. What makes it real: an administrator who relies on "group changes reach
trove only with a completed login" (R-AUTH-008) to time a membership change.

### H-93 · An enabled break-glass account without a password file starts silently

Stage: the start of a `web` replica ([breakglass.go](../../internal/auth/oidc/breakglass.go), `newBreakGlass`). Subject: the
recovery path. Live whenever `auth.break_glass.enabled` is true (the default) and `password_file` is empty.

R-AUTH-014 asks the replica to refuse to start without a readable password file; it starts instead, logs a warning, and refuses
every local login with 403 `local_login_disabled` (Q-AUTH-33). A configured file that cannot be read, a weak plaintext and a
weak hash still stop the replica. What makes it real: an operator who believes the break-glass account works because the
default says enabled and finds out during the outage that needs it.
