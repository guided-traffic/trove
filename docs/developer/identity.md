# Identity: principals, tokens, the authentication stage

Work package WP1.2 (area `identity`) of milestone M1. The requirements are R-AUTH-016 to R-AUTH-032, R-AUTH-033,
R-AUTH-037, R-AUTH-038, R-AUTH-040 to R-AUTH-043 in [identity-and-authentication.md](../spec/identity-and-authentication.md);
the decisions of this area are Q-AUTH-18 to Q-AUTH-22 and Q-AUTH-41 to Q-AUTH-48 on that page. The security view is
[security/tokens-and-credentials.md](../security/tokens-and-credentials.md). The management API operations are described in
[the section below](#management-api); every handler is a thin call of the functions of this page.

## What to hold in your head

- **PostgreSQL decides, Valkey accelerates.** A token, a principal, a membership and a setting live in PostgreSQL. A replica
  caches them in memory (four caches, below). Valkey carries change messages (early eviction), the throttle counters and the
  throttle-exemption markers, and nothing that decides who a caller is (Q-AUTH-9).
- **Revocation is bounded at `auth.revocation_bound` (2 s) in every failure mode.** The poll of the change log is the
  guarantee; the freshness gate makes a replica that cannot poll stop answering from its caches.
- **Secrets exist once.** A token value is returned by the call that creates it. The database holds its SHA-256. No event,
  payload or log line holds the value.
- **Groups are resolved at request time.** The stage reads them from the principal cache for each request; a token carries no
  group list.

## Packages

| Package | Responsibility |
|---|---|
| [internal/auth](../../internal/auth) | The stage (`Stage`, implements `Authenticator`), the failure limiter interface and its per-replica implementation |
| [internal/auth/principal](../../internal/auth/principal) | The principal table: read side with cache (`PGStore`), write side (`Manager`), the login seam (`PGProvisioner`), the change payloads (`Change`), the freshness gate (`Gate`, `Flights`), the identity metrics (`Metrics`) |
| [internal/auth/token](../../internal/auth/token) | Token format, scopes, lifetime policy, `Service` (create, rotate, revoke, list, validate with cache), `SettingsStore` (runtime settings), the two scheduled tasks |
| [internal/audit/events_identity.go](../../internal/audit/events_identity.go) | The identity event types with their data schemas and journal flags |
| [internal/cluster/valkey_tokseen.go](../../internal/cluster/valkey_tokseen.go) | `TokenSeen`: the throttle-exemption marker (key family `tokseen`) |
| [internal/meta/postgres/queries/identity.sql](../../internal/meta/postgres/queries/identity.sql) | Every identity query; parameters are plain Go types because the packages above import no driver |
| [internal/app/wire_identity.go](../../internal/app/wire_identity.go) | The composition: `wireIdentity`, `registerIdentityJobs`, `publicRoute` |

### internal/auth

| File | Responsibility |
|---|---|
| `auth.go` | The contract: `Authenticator`, `Result`, `Credential`, `EndpointRules` (with `PublicRoute`) |
| `stage.go` | `Stage.Authenticate`: credential selection and precedence, endpoint rules, anonymous, challenges, error mapping |
| `limiter.go` | `FailureLimiter` (the narrow limiter interface), `LocalLimiter`, the per-request throttle |

### internal/auth/principal

| File | Responsibility |
|---|---|
| `principal.go` | The contract: `Store`, `Principal`, `Kind`, `Status`, `LoginClaims`, `Provisioner` |
| `store.go` | `PGStore`: `ByID`, `ByName`, `Groups` over the cache `auth_principals` |
| `manager.go`, `groups.go`, `serviceaccounts.go` | `Manager`: users, groups and memberships, service accounts (R-AUTH-016, R-AUTH-018, R-AUTH-020); `Caller.RoleHolders` is the `roles:write` check the API sets; `AuthzHook` (optional, `authz.PrincipalHook` in the wiring) is told of every deleted or renamed principal in the same transaction (R-AUTHZ-028); a duplicate create is `409 already_exists` |
| `guards.go` | `guardRoleHolder` (a principal that holds a system role, or a group that carries one, is changed only with `roles:write`, Q-AUTHZ-69) and `LastAdminGuard` (`last_admin` over active holders, locked on the `admin` binding rows; authz uses it in `UnbindRole`, Q-AUTHZ-70) |
| `provisioner.go` | `PGProvisioner.SignIn`: just-in-time user, identity link, rename, group sync (R-AUTH-007, R-AUTH-008) |
| `change.go` | The payload of the change area `auth` and its notify helpers |
| `gate.go` | `Gate` (freshness), `Flights` (shared lookups while the gate is closed), `Metrics` |
| `ids.go`, `errors.go`, `db.go` | UUIDv7 and name rules; the refusals; the `DB` and `Notifier` interfaces |

### internal/auth/token

| File | Responsibility |
|---|---|
| `token.go` | The contract: `Type`, `Scope`, `Info`, `Validator`, the `Err*` values |
| `format.go` | Generate, `Check` (syntax step), `Hash`, `Hint` (R-AUTH-022) |
| `scope.go` | aliases of the scope type (`Scope`, `ScopeGrant`, `Patterns`, `ScopeOptions`) and forwards (`ParseScope`, `DefaultScope`, `ParseStoredScope`); the type, `Parse` (validation, limits), `Marshal` and `Within` (the derivation rule) live in the leaf [internal/auth/scope](../../internal/auth/scope/scope.go), which the stage, `httpserver.Credential` and `authz.Subject` use as well. `scope.Options` (`ScopeOptions`) carries the cross-area checks (actions, system items, endpoints and the three pattern dimensions); `app.TokenScopeOptions` fills them from `authz.Actions()`, `authz.Capabilities()` and the authz validators (Q-AUTH-151) |
| `lifetime.go` | `resolveLifetime` (R-AUTH-026) |
| `service.go` | `Service`: `Create`, `Get`, `List`, the record types; `insert` with `lineageCheck` (locks the parent and the rotated token with their `derived_from` chains and decides on what it finds then) |
| `rotate.go`, `revoke.go` | `Rotate` (the successor keeps the lineage, Q-AUTH-89), `Revoke`, `RevokeByValue`, `RevokeAll`; `revokeTree` locks the tree before the cascade |
| `validate.go` | `Service.Check`/`Validate`: the validation path, the cache `auth_tokens`, `token.rejected` |
| `settings.go` | `SettingsStore`: cache `auth_settings`, `Effective`, `Update` (R-AUTH-040) |
| `tasks.go` | `Cleanup` (`token-cleanup`), `NoticeExpiring` (`token-expiry-notice`) |

## Request path

```text
request ─► chain stage 6 ─► app.authenticate ─► auth.Stage.Authenticate(r, rules)
  exchange route (POST)            ─► anonymous, Authorization not read
  Authorization present            ─► Bearer | Basic(any:token) ─► token.Check (syntax, endpoint type rule, no DB)
                                      ─► token.Service.Check(raw, Request{Throttle, Source})
                                           cache hit (gate open)          ─► Info
                                           throttle Exceeded? marker?     ─► 429 or continue
                                           PostgreSQL join with the owner ─► Info | ErrUnknown|Revoked|Expired|Owner
                                      ─► endpoint binding rules, groups from principal.Store
  session cookie, path rule        ─► session.Validator (+ CSRF)           (nil: ignored)
  nothing                          ─► anonymous | 401 credentials_required
```

- A credential that is present and invalid is a 401 and never falls through. Unknown Bearer values (JWTs, passwords,
  the break-glass password) are refused at the syntax step without a database query.
- Errors: `invalid_token` for malformed, unknown, revoked, expired and wrongly bound tokens (the response does not say
  which), `principal_disabled` for a disabled owner, `credentials_required` without a credential, `session_expired`,
  `rate_limited` (429 with `Retry-After`) when the address is throttled, 503 when PostgreSQL does not answer.
- The challenge is `Bearer realm="trove"` (plus `error="invalid_token"` after a rejected token) under `/api/v1/`, otherwise
  `Basic realm="trove"`.
- The session cookie is accepted on `/api/v1/` for every method, on `/files/` for GET, HEAD, PUT and DELETE, on
  `/api/<format>/` for GET and HEAD, and ignored elsewhere.
- Anonymous: the endpoint allows it (`EndpointRules.Anonymous`), `anonymousAccess` is on and the ceiling
  `auth.anonymous.allowed` is true (`SettingsStore.Effective`), and the method is safe if the path is under `/api/v1/`.
  Independently of the runtime switch, on an endpoint that allows anonymous requests, a request is anonymous when the
  route is in the always-open list (`/auth/`, login options, device endpoints, `POST /api/v1/tokens/revoke`, the CI
  exchange, the npm login start, `GET`/`HEAD` of `/` and `/ui/…`) or when `EndpointRules.PublicRoute` says the matched
  management operation is declared public (Q-AUTH-19). An endpoint that allows no anonymous request (a further endpoint
  by default, R-EP-010) answers every request without a credential with the same 401, the always-open routes included.
- Not built: the NuGet API-key source (M2), the GitLab job-token extension point (M4), last-used tracking (R-AUTH-031 is
  P1; `last_used_at` stays null).

## Caches and revocation propagation

| Cache | Key | Holds | Registered by |
|---|---|---|---|
| `auth_tokens` | SHA-256 of the token | `token.Info` with the owner's status; live tokens only | `token.NewService` |
| `auth_principals` | `i:<id>` and `n:<kind>:<name>` | principal and its groups | `principal.NewStore` |
| `auth_settings` | one key | the row of `auth_settings` | `token.NewSettingsStore` |
| `auth_sessions` | – | the login area's | login |

All four use `SafetyTTL = auth.tokens.cache_ttl` and the area `auth`. A write that ends, narrows or alters a credential,
a principal or a setting calls `Notify` on its transaction (`principal.NotifyTokens`, `NotifyPrincipals`,
`NotifySettings`); the poll of the change log applies it on every replica, Valkey only evicts earlier.

| Payload `t` | Written by | `auth_tokens` | `auth_principals` | `auth_settings` |
|---|---|---|---|---|
| `token` (`k`: ID and hash pairs) | revoke, rotate | evicts the hashes | – | – |
| `principal` (`ids`) | disable, enable, delete, rename, revoke all | flush | evicts the IDs and all name keys | – |
| `membership` (`ids`) | membership change, group sync | – | evicts the IDs | – |
| `group` (`ids`) | group rename or delete | – | flush | – |
| `settings` | settings update | – | – | flush |
| `session` | login area | – | – | – |
| `all`, or any other kind | – | flush | flush | flush |

The payload is under 1 KiB: more than 12 IDs or 6 tokens become `all`. Never a token value: the hash is the database's
lookup key and does not allow authentication.

**The freshness gate** (`principal.Gate`, wired to `cluster.Changes.FreshWithin(auth.revocation_bound)`): while the last
completed poll started more than the bound ago, `Open()` is false, every lookup reads PostgreSQL and nothing is cached;
concurrent lookups of one key share a query only if it started less than the bound before (`Flights`). A lookup that
PostgreSQL does not answer is 503. `trove_auth_cache_bypassed_total{reason="changes_stale"}` counts them.

**The fill rule** is the cache package's (`Load` reads the applied epoch before the query and caches only if it is
unchanged), so a fill that raced a revocation serves one request and is not cached.

## Brute-force protection (R-AUTH-038)

- `auth.FailureLimiter` is the interface the stage codes against (`Exceeded(ctx, bucket)`, `Count(ctx, bucket)`); it is the
  count-only form of R-ARCH-034, so `*cluster.Limiter` of the API area fits without an adapter. The bucket is the client
  address from `httpserver.RequestInfo.Client`; addresses in `auth.rate_limit.exempt_cidrs` are never throttled.
- The throttle is consulted only for a credential that is not in the replica's cache. In throttled mode a credential with
  a `tokseen` marker (`cluster.TokenSeen`, validated from PostgreSQL on any replica within
  `auth.rate_limit.recent_token_window`) is still validated; any other gets 429. The marker only lets the lookup run.
- Every rejection of a presented credential is counted (malformed, unknown, revoked, expired); a disabled owner and a
  storage failure are not. The throttle trips at the budget, not above it (Q-AUTH-20): 300 failures per minute mean 300
  validations.
- `auth.LocalLimiter` is the per-replica implementation with the sliding-window arithmetic and the share
  `burst ÷ live replicas` that the cluster limiter falls back to while Valkey is unavailable. A replica with a client
  listener gets the cluster limiter at `a.m1.failureLimiter` (`wireFailureLimiter`); `wireIdentity` uses `LocalLimiter`
  alone only on a process without one.
- Not here: the break-glass lockout (login), and the device-code limiter. The flow-start limit of the unauthenticated
  login routes is `a.m1.flowStarts`, registered next to the failure limiter and used by the login
  ([login.md](login.md#seams-to-the-other-areas)) and the revocation by value; both callers pass the bucket through
  `auth.AddressBucket`, which maps an IPv6 address to its `/64` (Q-AUTH-90).

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | `internal/auth/stage_test.go` | precedence, no fall-through, challenges, session path rules, anonymous rules, exchange routes, endpoint rules, the Basic switch, failure codes; E1.8 for Bearer and Basic against a shared fake limiter and against per-replica shares |
| Unit | `internal/auth/token/*_test.go` | the format test vector, scope validation, the derivation rule, lifetime table, the closed gate with an unreachable database (503), throttle consultation, cache eviction by payload kind |
| Unit | `internal/auth/principal/principal_test.go` | name rules, UUIDv7, payload helpers, shared lookups |
| Integration | `test/integration/identity/` | two or three in-process replicas against the shared stack, each with Valkey running or stopped: E1.2 (database text scan finds no token value; revocation within 2 s), the provisioner and group sync, concurrent first logins, memberships, disable/enable/delete, service accounts, creation limits under concurrency, derivation, rotation, revocation by value, settings and the anonymous switch across replicas, throttle markers, the scheduled tasks |
| Integration, HTTP | `test/integration/identity/api_*_test.go` | the same replicas serving the real handlers ([test/integration/idapi](../../test/integration/idapi): the production chain, the real stage and decision point), every answer validated against the document: `TestAPIOperations` reaches every identity operation and asserts `Uncovered` is empty; E1.2 over HTTP with Valkey running and stopped; concurrent idempotent creates on three replicas; E1.8 for Bearer and Basic across three replicas (cluster limiter, and the per-replica share without Valkey); the flow-start budget of the revocation by value; `audit_event` rows for token creation and revocation (E1.5) |

Run the integration area from the checkout root with the stack up (see [build-and-test.md](build-and-test.md)):
`set -a; . /Users/hfi/repos/trove/.dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/identity/...`

## Wiring and what other areas connect

`wireIdentity` runs at the identity marker of step 8 and fills `a.m1.principals`, `principalManager`, `provisioner`,
`tokens`, `authSettings` and `authenticator`. What it takes from, and gives to, the other areas:

| Point | File | Connected to |
|---|---|---|
| Audit recorder | `wire_api.go` `auditRecorder` | `a.m1.recorder` of step 4, a discarding recorder on a process without PostgreSQL |
| `a.m1.sessions` (`session.Validator`) | `m1.go` | login: `wireSessions` runs before `wireIdentity`; a nil store stays a nil interface (a worker has none) |
| `a.m1.failureLimiter`, `a.m1.flowStarts` | `m1.go` | `wireFailureLimiter`: `cluster.LimiterSet.NewLimiter("auth.credential_failures", …)` and `"auth.flow_starts"` |
| `BreakGlassName` | `wire_identity.go` | `auth.break_glass.username`, the reserved username of the provisioner and the manager |
| `Provisioner` | `a.m1.provisioner` | login: `wireLogin` hands it to the OIDC service, whose callback calls `SignIn` |
| `principal.Metrics` | `wire_identity.go` `authMetrics` | one instance per replica; the session store and the login count into the same families |
| `principal.AdminBinder` | `authz.Service.BindAdmin` | only `trove admin service-account create --admin` ([admin_identity.go](../../cmd/trove/admin_identity.go)); the running manager has none, no API operation binds a role on creation (Q-AUTH-47) |
| `v1.AuthServices` | `App.authServices` ([wire_identity.go](../../internal/app/wire_identity.go)) | the manager, the token service, the settings store, the session store, the login and the limiter `auth.flow_starts` that `startClient` passes to every endpoint's `v1.Options.Auth`; a component the replica lacks stays nil and its operations answer 503 |

## Management API

The handlers are in [internal/api/v1](../../internal/api/v1): `users_api.go`, `groups_api.go` (groups and service accounts),
`tokens_api.go`, `sessions_api.go` (login options, `whoami`, sessions), `authsettings_api.go`, `preferences_api.go`; the services they
need are `v1.AuthServices` ([auth_services.go](../../internal/api/v1/auth_services.go)) and the helpers of merge patches
([auth_patch.go](../../internal/api/v1/auth_patch.go)). The document is `api/openapi.yaml` between the `identity` and `login` markers.

| operationId | Method and path | `x-trove-permission` |
|---|---|---|
| `listUsers` | `GET /api/v1/users` | `identities:read` |
| `createUser` | `POST /api/v1/users` | `identities:write`, idempotent |
| `getCurrentUser` | `GET /api/v1/users/me` | authenticated (a user or the break-glass account) |
| `getUser` | `GET /api/v1/users/{username}` | `identities:read` |
| `updateUser` | `PATCH /api/v1/users/{username}` | `identities:write` |
| `deleteUser` | `DELETE /api/v1/users/{username}` | `identities:write` |
| `disableUser`, `enableUser` | `POST /api/v1/users/{username}/disable`, `/enable` | `identities:write` |
| `patchUserGroups` | `PATCH /api/v1/users/{username}/groups` | `identities:write` |
| `unlockBreakGlass` | `POST /api/v1/users/{username}/unlock` | role `admin` |
| `listGroups`, `getGroup` | `GET /api/v1/groups`, `/groups/{name}` | `identities:read` |
| `createGroup` | `POST /api/v1/groups` | `identities:write`, idempotent |
| `updateGroup`, `deleteGroup` | `PATCH`, `DELETE /api/v1/groups/{name}` | `identities:write` |
| `listGroupMembers` | `GET /api/v1/groups/{name}/members` | `identities:read` |
| `patchGroupMembers` | `PATCH /api/v1/groups/{name}/members` | `identities:write` |
| `listServiceAccounts`, `getServiceAccount` | `GET /api/v1/service-accounts`, `/service-accounts/{name}` | `identities:read` |
| `createServiceAccount` | `POST /api/v1/service-accounts` | `identities:write`, idempotent |
| `updateServiceAccount`, `deleteServiceAccount` | `PATCH`, `DELETE /api/v1/service-accounts/{name}` | `identities:write` |
| `disableServiceAccount`, `enableServiceAccount` | `POST /api/v1/service-accounts/{name}/disable`, `/enable` | `identities:write` |
| `createToken` | `POST /api/v1/tokens` | authenticated (handler: a user with a recent session, or a PAT under the derivation rule), idempotent |
| `listTokens` | `GET /api/v1/tokens` | authenticated (own tokens; all with `tokens:read`) |
| `getToken` | `GET /api/v1/tokens/{id}` | authenticated (404 for a token the caller may not see) |
| `getCurrentToken`, `revokeCurrentToken`, `rotateCurrentToken` | `GET`, `DELETE /api/v1/tokens/current`, `POST /api/v1/tokens/current/rotate` | authenticated (404 `not_a_token` for a session) |
| `revokeToken` | `DELETE /api/v1/tokens/{id}` | authenticated (owner, or `tokens:revoke`) |
| `rotateToken` | `POST /api/v1/tokens/{id}/rotate` | authenticated (owner of a PAT; administrator or the account itself for a SAT), idempotent |
| `revokeTokenByValue` | `POST /api/v1/tokens/revoke` | public, counted in `auth.flow_starts` |
| `revokeOwnTokens` | `DELETE /api/v1/users/me/tokens` | authenticated (a user) |
| `revokeUserTokens`, `revokeServiceAccountTokens` | `DELETE /api/v1/users/{username}/tokens`, `/service-accounts/{name}/tokens` | `tokens:revoke` |
| `createServiceAccountToken` | `POST /api/v1/service-accounts/{name}/tokens` | authenticated (an administrator, or a token of the account), idempotent |
| `getAuthSettings`, `updateAuthSettings` | `GET`, `PATCH /api/v1/settings/auth` | `settings:read`, `settings:write` |

How the pieces fit:

- **Idempotent creates are one transaction with the idempotency row.** `Idempotency.Do` hands the operation a transaction; the handler passes
  `principal.Join(ctx, tx)` to the manager or the token service, whose database is a `principal.Joinable`, so their `InTx` and `Read`
  use that transaction (Q-AUTH-46). A token create sets `Outcome.SecretID`: the stored outcome has no body, and a replay is
  `409 token_already_issued` with the token ID.
- **The caller's rights that depend on the data are decided in the handler.** The authorization step has checked the declaration; the
  handlers add the rules of the specification that need the record: the owner of a token, the derivation rule (`presenting` builds the
  parent `token.Info` from the token row), no impersonation, and `404` for what the caller may not see. A capability check is never
  repeated here, and neither is the recent sign-in of a session: the operations that mint a credential declare
  `x-trove-recent-auth: true` and the shared step enforces it (R-AUTH-011, [http-api.md](http-api.md), Q-API-40).
- **Every management `GET` reads PostgreSQL.** The handlers call `Manager.Get*`, `Tokens.Get`, `Settings.Read` and the preference query,
  which read the database, never the replica's cache of the authentication stage (R-API-052).
- **Merge patches** of users, groups and service accounts are decoded into structs of `json.RawMessage` members (`userPatchBody` and the
  like), so an absent member, a `null` and a value are told apart; the read-only members of the record are listed in the struct and
  ignored, so a body obtained by `GET` can be sent back (R-API-013). Every one of those patches, and the settings and
  preferences patches, require `If-Match` (Q-AUTH-50, Q-API-26, [http-api.md](http-api.md)).
- **Revisions of the identity resources** (Q-AUTH-50, migration `00013_identity_revisions`). `principal.revision` is the ETag of a
  user, a group and a service account, `auth_settings.revision` the one of the settings; both are registered revision columns.
  Triggers bump a principal's revision when a member of its representation changes (name, status, e-mail, display name,
  disabled reason, description, auto-join, an identity link), only when the value differs, so a sign-in that changes nothing and
  the login counters leave it alone; membership deltas do not move it. `Manager.UpdateUser`, `UpdateGroup` and
  `UpdateServiceAccount` take the revision the caller loaded (`UserPatch.Expected`, `GroupPatch.Expected`, a parameter) or
  `errs.AnyRevision`, and compare it under the lock of the principal row (`checkRevision`); the zero value matches nothing, so
  a caller that forgets it gets a 412. `SettingsStore.Update` writes with `SettingsPatch.Expected` in the `UPDATE` itself
  (`revision = $expected OR $expected < 0`) and reads the current revision for the 412 when no row came back. All of them answer
  `errs.Stale`. The `GET`s answer the `ETag` and honour `If-None-Match`; the bodies carry no revision member (Q-API-43).
- **The convention suite runs on all three** (`apitest.RunContract` at the end of `TestAPIOperations`, E1.6): cursor paging, `ETag`
  and 304, `If-Match` 428/412/200/`*`, merge patch and idempotent creates on users, groups and service accounts, every request
  answered by another of three replicas.
- **Token representations list `unknownEndpoints`** (R-EP-011, Q-EP-24): `toToken` asks `AuthServices.EndpointConfigured`, which the
  composition root fills with the predicate of the scope check.

Extension checklist, **a new identity operation**: add the path and schemas between the markers of `api/openapi.yaml` (the permission, the
`429`, the problem responses, `x-trove-reserved-names` for a literal next to a key), run `make generate`, write the handler on `*Server`
with `s.identityReady`, call the domain function, answer with `failIdentity`, add it to `identityOperations` in
[api_test.go](../../test/integration/identity/api_test.go) so that `Uncovered` fails until a test reaches it.

## Bootstrap commands

`trove admin service-account create`, `token create|revoke` and `user disable` (R-AUTH-015) are
[cmd/trove/admin_identity.go](../../cmd/trove/admin_identity.go). They build the manager and the token service over the
command's own connection, with empty caches and an `audit.Writer` of their own, and call the same domain functions as the API
with the actor `admin-cli`. `service-account create --admin` binds the role in the creating transaction through
`authz.Service.BindAdmin`, which notifies the area `authz` and records `role.bound`. `token create` prints only the secret on
standard output (default lifetime 15 minutes, the SAT maximum applies; `--scope-file` is validated by
`app.TokenScopeOptions`); `token revoke --value-stdin` reads the value from standard input and revokes with reason `leaked`.
The integration test is [admin_test.go](../../test/integration/login/admin_test.go).

## Extension checklists

**A new token creation path** (npm web login, device login, CI exchange): call `token.Service.Create` with `Via` set to
the `created_via` value, the path's `TTLRequest.Default` and the caller; add the value to the CHECK of
`api_token.created_via` only if it is missing; add a test in `test/integration/identity/tokens_test.go`.

**A new change of a credential or principal**: write it in a transaction that calls one of the `principal.Notify*` helpers
(or a new payload kind: add the constant in `change.go`, the reaction in `PGStore.onChange`, `Service.onChange` and
`SettingsStore`, and the table above); record the audit event with `RecordTx` in the same transaction; add a revocation test
across two replicas with Valkey stopped.

**A new identity event**: register it in `internal/audit/events_identity.go` with its schema (every array has `maxItems`),
mark it `JournalRevocation` if it removes access (and then always pass `Event.Journal`), and list it in R-AUTH-041.

**A new scope field**: extend `wireScope` and `Scope` in `scope.go`, validate in `validateScope`, extend `Within`, and
update R-AUTH-024.

## Contract additions

Additive changes to the shared contracts (no consumer breaks): `auth.EndpointRules.PublicRoute`;
`principal.LoginClaims` gained `Issuer`, `EmailVerified`, `GroupSync`, `ClaimState`, `KeepOnMissing`, `CreateGroups`, `JIT`,
`SyncUsername`, `Source` (Q-AUTH-21); the codes `non_expiring_restricted`, `token_limit_reached`,
`derivation_exceeds_parent`, `description_required`, `not_a_token`, `username_invalid` in `internal/errs/codes.go`.
