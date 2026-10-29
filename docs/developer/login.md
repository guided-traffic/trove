# Login and sessions

The browser login through OpenID Connect, the server-side UI session, CSRF, logout and the break-glass account (work package
WP1.1). Requirements: R-AUTH-003 to R-AUTH-015 and R-AUTH-041 to R-AUTH-043 in
[identity-and-authentication.md](../spec/identity-and-authentication.md); decisions Q-AUTH-30 to Q-AUTH-40 on the same page (the API operations: Q-AUTH-41 to Q-AUTH-48).
The security view is [security/login-and-sessions.md](../security/login-and-sessions.md).

What to have in mind first:

- **Every step reads PostgreSQL.** A login is three requests (start, IdP, callback) that may reach three replicas; the flow lives
  in `oidc_login_flow`, the session in `ui_session`, the lockout and rotation hold in `auth_lockout`. A replica keeps nothing a
  later request needs, apart from caches and the provider documents it fetched itself (R-AUTH-042). Nothing of the login is ever
  read from or written to Valkey.
- **Two packages.** [internal/auth/session](../../internal/auth/session) creates, validates and ends sessions and implements
  `session.Validator`, which the identity area's authentication stage calls for every request with the cookie.
  [internal/auth/oidc](../../internal/auth/oidc) holds the providers, the browser routes and the break-glass account. Both are
  domain packages: they import no driver (every query parameter is a plain Go type, [login.sql](../../internal/meta/postgres/queries/login.sql))
  and no edge package (the composition root hands them the problem writer, the client address and the event source).
- **Ages, not clocks.** The session read returns ages against the database's `now()` (seconds since `last_seen_at`, seconds left
  to `absolute_expires_at`), and the replica turns them into deadlines on its own monotonic clock. No replica compares its wall
  clock with the database's or with another replica's.

## Files

| File | Responsibility |
|---|---|
| [session/session.go](../../internal/auth/session/session.go) | The contract: `Info`, `Validator`, `ErrInvalid`, `ErrCSRF`, the auth methods |
| [session/store.go](../../internal/auth/session/store.go) | `Store`: `Create` (principal row lock, fixation, re-authentication, `max_per_user`), `Validate` (cache and freshness gate, shared lookups, deadlines from the row, the conditional touch) |
| [session/end.go](../../internal/auth/session/end.go) | `Logout`, `EndForPrincipal` (on a caller's transaction), `EndPrincipalSessions`, `EndBreakGlass(Tx)`, `EndAll`, `List`, the `auth.session.ended` event |
| [session/csrf.go](../../internal/auth/session/csrf.go) | `CheckCSRF`, `SameOrigin`, `NormalizeOrigin`, the `auth.csrf_failed` bound per session and minute |
| [session/cookie.go](../../internal/auth/session/cookie.go), [ids.go](../../internal/auth/session/ids.go) | The three `__Host-` cookies and their attributes; `NewSecret`, `WellFormed`, `Hash`; `NewID` (UUIDv7) |
| [session/changes.go](../../internal/auth/session/changes.go) | The cache `auth_sessions` and its `OnChange`; the payloads this package writes into the change area `auth` |
| [session/cleanup.go](../../internal/auth/session/cleanup.go) | The maintenance task `session-cleanup` (R-AUTH-043), registered in `jobs.Default` |
| [session/errors.go](../../internal/auth/session/errors.go), [metrics.go](../../internal/auth/session/metrics.go) | `Problem` (401 `session_expired`, 403 `csrf_failed`), `ReauthenticationRequired`; the session metrics |
| [oidc/service.go](../../internal/auth/oidc/service.go) | `Service` (a component: `Start` runs the discovery loops and the password watcher), `CheckBaseURL`, `LoginOptions`, `SafeReturnTo`, the route patterns, the pending-flow count |
| [oidc/provider.go](../../internal/auth/oidc/provider.go) | `Provider`: discovery and its checks, refresh with backoff, the JWK Set and the throttled refetch for an unknown `kid`, the client secret file |
| [oidc/flow.go](../../internal/auth/oidc/flow.go) | `HandleStart`, `HandleCallback` with the steps of R-AUTH-006, the code exchange, userinfo, the re-authentication check, the login events |
| [oidc/jws.go](../../internal/auth/oidc/jws.go), [idtoken.go](../../internal/auth/oidc/idtoken.go) | The minimal JWS verifier (Q-AUTH-31) and the ID-token claim checks; the reason codes |
| [oidc/claims.go](../../internal/auth/oidc/claims.go) | Claim paths, the username rule, the group claim and its missing reasons, the group rules, the gates |
| [oidc/logout.go](../../internal/auth/oidc/logout.go) | `HandleLogout` and the RP-initiated logout URL |
| [oidc/breakglass.go](../../internal/auth/oidc/breakglass.go), [argon.go](../../internal/auth/oidc/argon.go) | The break-glass user row, the local login, the lockout, the verification slots, the rotation hold; Argon2id and the PHC string |
| [oidc/page.go](../../internal/auth/oidc/page.go) | The error page of a failed login |
| [config/login.go](../../internal/config/login.go) | The keys `auth.oidc`, `auth.login_page`, `auth.sessions`, `auth.break_glass`, their defaults and rules |
| [audit/events_login.go](../../internal/audit/events_login.go) | The `auth.*` event types |
| [app/wire_login.go](../../internal/app/wire_login.go) | The wiring at step 8 in two steps (`wireSessions` before the identity area, `wireLogin` after it) and the routes in `startClient` (login markers of [app.go](../../internal/app/app.go)) |
| [cmd/trove/admin_login.go](../../cmd/trove/admin_login.go) | `trove admin break-glass status\|unlock\|end-sessions`, `trove admin sessions end`; the command environment (database, change log, audit writer, session store) |
| [cmd/trove/admin_identity.go](../../cmd/trove/admin_identity.go) | `trove admin service-account create`, `token create\|revoke`, `user disable` (R-AUTH-015): the identity area's manager and token service over the command's own connection; `--admin` binds the role through `authz.Service.BindAdmin` |

## The flows, one fact each

| Flow | What happens | Where |
|---|---|---|
| Start | `state`, `nonce`, verifier from `crypto/rand`; their hashes and the verifier in one `INSERT`; the binding cookie reused when present; 302 | `HandleStart` |
| Callback step 1 | `DELETE … RETURNING` of the flow, committed alone before the first outbound call | `takeFlow` |
| Steps 2 to 5 | provider of the flow, binding hash, `iss`, the IdP's `error` | `checkResponse` |
| Steps 6 to 8 | code exchange, ID token, userinfo; each call 10 s through the outbound client | `exchange`, `validateIDToken`, `userinfo` |
| Step 10 | mapping and gates, then `Provisioner.SignIn` (its transaction), then `Store.Create` (another, Q-AUTH-32) | `callback` |
| Validation | cache while the poll is fresh, otherwise a shared query; deadlines from the row; touch every 60 s | `Store.Validate` |
| Logout | CSRF checks, the end and its change in one transaction, cookies cleared, `redirectTo` | `HandleLogout`, `Store.Logout` |
| Local login | origin, lockout row, slot, Argon2id, failure count or session | `HandleLocalLogin` |
| Rotation | the watcher sees the file change, opens the hold and ends the break-glass sessions, then switches | `BreakGlass.CheckRotation` |

## Seams to the other areas

| Seam | Direction | What the other side provides or calls |
|---|---|---|
| `principal.Provisioner.SignIn(ctx, LoginClaims)` | login calls identity | `a.m1.provisioner` is the identity area's `PGProvisioner`. It creates or updates the user, links (`Issuer`, `Subject`), syncs `Groups` (already mapped trove group names) as `GroupSync`, `ClaimState`, `KeepOnMissing` and `CreateGroups` say, and writes `user.*` events in its own transaction. A refusal is `principal.ErrUsernameTaken`, `ErrNotProvisioned`, `ErrUserDisabled` or `ErrUsernameInvalid`; `refusalReason` in [oidc/flow.go](../../internal/auth/oidc/flow.go) maps it to the failure reason. The fields are those of Q-AUTH-21 ([principal.go](../../internal/auth/principal/principal.go)). |
| `session.Validator` | identity's stage calls login | `Validate(ctx, cookie)` then `CheckCSRF(r, info)`; map errors with `session.Problem`. `a.m1.sessions` holds the `*session.Store` (nil on a worker); `wireSessions` sets it before `wireIdentity` hands it to the stage. |
| `Store.EndForPrincipal(ctx, tx, id, reason)` | identity calls login | Ends a user's sessions in the transaction of a disable or delete (R-AUTH-016). |
| Change area `auth` | identity registers it | The payload is `principal.Change` (Q-AUTH-22): `{"t":"session","s":[{"i":<session id>,"h":<hex SHA-256 of the secret>}]}` per ended session, or without the list for more than eight. The session cache evicts the named hashes, ignores `token`, `membership`, `group` and `settings`, and flushes on every other payload, since it holds the principal's status. The identity caches ignore `session`. |
| `oidc.Limiter` | login takes it from the cluster limiter set | `auth.flow_starts` in the take form (R-AUTH-038, `starts_per_address` per minute), registered in `wireFailureLimiter` ([wire_api.go](../../internal/app/wire_api.go)) and adapted by `takeLimiter`; it guards the start route and the local login. The pending-flow cap is `auth.rate_limit.max_pending_flows`. |
| `audit.Recorder` | audit provides | `a.m1.recorder` (`trove admin` commands build an `audit.Writer` of their own on their connection). |
| `principal.Metrics` | identity provides | One instance per replica (`App.authMetrics`) owns `trove_auth_attempts_total`, `trove_auth_ratelimited_total` and `trove_auth_cache_bypassed_total`; the session store and the login count into it. |
| `auth.revocation_bound`, `auth.tokens.cache_ttl`, `auth.break_glass.username` | identity's keys | The session store reads the first two; the break-glass username is the reserved name of the provisioner and the manager. |

## Management API

The operations that belong to the login are in [sessions_api.go](../../internal/api/v1/sessions_api.go) and
[users_api.go](../../internal/api/v1/users_api.go) (`UnlockBreakGlass`); the document holds them between the `login` markers. The other
operations of [identity.md](identity.md#management-api) apply to the signed-in user too.

| operationId | Method and path | `x-trove-permission` |
|---|---|---|
| `getLoginOptions` | `GET /api/v1/auth/login-options` | public; `oidc.Service.LoginOptions` plus `anonymousAccess` from the cached settings |
| `getWhoami` | `GET /api/v1/auth/whoami` | authenticated; the principal, its groups and the credential (a token's scope and expiry come from its row, a session shows `type: session`) |
| `listOwnSessions` | `GET /api/v1/users/me/sessions` | authenticated (a user or the break-glass account); `current` is the session of the request |
| `endOwnSession` | `DELETE /api/v1/users/me/sessions/{id}` | authenticated; `404` for a session of another user |
| `endOwnSessions` | `DELETE /api/v1/users/me/sessions?keepCurrent=` | authenticated |
| `listUserSessions` | `GET /api/v1/users/{username}/sessions` | `identities:read` |
| `endUserSessions` | `DELETE /api/v1/users/{username}/sessions` | `identities:write` (`Store.EndPrincipalSessions`) |
| `unlockBreakGlass` | `POST /api/v1/users/{username}/unlock` | role `admin` (`oidc.Unlock`, shared with `trove admin break-glass unlock`) |
| `getPreferences`, `updatePreferences` | `GET`, `PATCH /api/v1/users/me/preferences` | authenticated (a user); `ETag` is `user_preferences.revision`, a user without a row has revision 1 |

`session.Store` gained `EndOne` and `EndExcept` (queries `EndSessionOfPrincipal` and `EndSessionsOfPrincipalExcept` in
[login.sql](../../internal/meta/postgres/queries/login.sql)); each ends in one transaction with the change message of the area `auth`
and `auth.session.ended`. `v1.SessionService` and `v1.LoginService` are the narrow interfaces the handlers hold.

## Configuration

The keys and their defaults are the YAML blocks of R-AUTH-004, R-AUTH-007, R-AUTH-008, R-AUTH-010, R-AUTH-014 and R-AUTH-038,
with one change: `auth.oidc.providers` is a map keyed by the provider ID, and `order` sorts the login buttons (Q-AUTH-30).

```yaml
server:
  base_url: https://trove.example.com            # example; required with a provider (R-AUTH-003)
auth:
  oidc:
    providers:
      corp:                                      # example: the provider ID, part of the redirect URI
        order: 0                                 # default
        display_name: Corporate SSO              # example
        issuer: https://login.example.com/realms/acme   # example
        client_id: trove                         # example
        client_secret_file: /var/run/secrets/trove/oidc-corp/client-secret  # example; or client_secret_env
        client_auth_method: client_secret_basic  # default
  sessions:
    idle_timeout: 1h                             # default
  break_glass:
    enabled: true                                # default
    password_file: /var/run/secrets/trove/break-glass/password  # example
```

`server.base_url` is checked against the providers where the login is built (`oidc.CheckBaseURL`): the rule spans two sections,
so `trove admin config validate` does not report it.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | `internal/auth/oidc`, `internal/auth/session`, `internal/config/login_test.go`, `internal/app/wire_login_test.go` | JWS and key confusion, the ID-token table, claim mapping and group rules, `return_to`, Argon2id and the plaintext rule, slots; cookies, CSRF, change payloads; configuration rules; the route table |
| Integration | [test/integration/login](../../test/integration/login) | A fake OpenID Provider over TLS ([fakeidp_test.go](../../test/integration/login/fakeidp_test.go)) with switches for every failure of R-AUTH-006, and two or three in-process replicas behind a dispatcher that sends every request to the next one, without Valkey; a fake provisioner writes the principal rows (the real one is covered by [test/integration/identity](../../test/integration/identity)); the break-glass and identity-command tests ([admin_test.go](../../test/integration/login/admin_test.go)) run `trove admin` as a binary |
| Integration, HTTP | [api_test.go](../../test/integration/login/api_test.go), [api_env_test.go](../../test/integration/login/api_env_test.go) | with `env.realAPI` the replicas mount the real management API (the production chain, the real stage with the session validator, the real decision point; [test/integration/idapi](../../test/integration/idapi)) instead of the two stand-in routes: the login journey over HTTP on alternating replicas with no Valkey (login options, sign in, `whoami`, a write with and without the CSRF header, the session list and its ends, recent authentication, logout) and the break-glass unlock through the API; every answer is validated against the document |
| Integration, E1.5 | [e1_5_test.go](../../test/integration/login/e1_5_test.go) | every event type that exit criterion E1.5 names through the real routes on the `realAPI` harness with the fake provider and a real audit writer behind the spy (`spy.next`): a login, a failed login (wrong nonce), a token creation (twice) and revocation, a policy change and a denied deploy (`PUT /files/…` with a token; the files handler gets a blob store that is never reached); each is found through `GET /api/v1/audit/events?action=…` and in the log stream, and the denied deploy names the repository, the action and the caller. The extra services come in through `env.extend` (`idapi.Components.Options` and `Routes`) |

Run the integration area with the stack's environment, as [build-and-test.md](build-and-test.md) describes:
`go test -count=1 -tags integration ./test/integration/login/...`.

## How to add a provider option

1. Add the field to `config.AuthOIDCProvider` with its yaml name, its default in `Defaulted` and its rule in `validate`.
2. Read it in `internal/auth/oidc` from `Provider.cfg`, which is always defaulted.
3. Add a unit test of the rule and, when it changes the login, a switch of the fake provider and an integration test.
4. Update the YAML block of the owning requirement and the configuration example above.
