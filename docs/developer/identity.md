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
| [internal/auth/registrytoken](../../internal/auth/registrytoken) | The registry token of the OCI token service (R-OCI-005): form, HKDF keys from the key ring, scope grammar, grant, challenge text, metrics; see [oci.md](oci.md#registry-tokens-and-the-token-service) |
| [internal/avatar](../../internal/avatar) | The user avatar (R-API-055): `Processor.Process` turns an upload into a 256 x 256 PNG, `Store` keeps it in the blob store and `user_avatars` |
| [internal/audit/events_identity.go](../../internal/audit/events_identity.go) | The identity event types with their data schemas and journal flags |
| [internal/audit/events_bastion.go](../../internal/audit/events_bastion.go) | `bastion.created`, `bastion.updated`, `bastion.disabled`, `bastion.enabled`, `bastion.deleted`; the disable and the deletion carry the journal entry (Q-BAS-145) |
| [internal/bastionmain](../../internal/bastionmain) | The Bastion registrations (`registrations.go`: `Registrations`, the service behind `/api/v1/bastions` and `trove admin bastion`) and the `TokenAuthenticator` over the token service (`authenticator.go`); see [bastion.md](bastion.md) |
| [internal/cluster/valkey_tokseen.go](../../internal/cluster/valkey_tokseen.go) | `TokenSeen`: the throttle-exemption marker (key family `tokseen`) |
| [internal/meta/postgres/queries/identity.sql](../../internal/meta/postgres/queries/identity.sql) | Every identity query; parameters are plain Go types because the packages above import no driver |
| [internal/app/wire_identity.go](../../internal/app/wire_identity.go) | The composition: `wireIdentity`, `registerIdentityJobs`, `publicRoute`, `apiKeyRoute` (the dispatcher's answer to "is this an API-key route", Q-AUTH-186) |

### internal/auth

| File | Responsibility |
|---|---|
| `auth.go` | The contract: `Authenticator`, `Result`, `Credential` (with `Registry`, the grant of a registry token), `EndpointRules` (with `PublicRoute` and `APIKeyRoute`), `HeaderAPIKey` |
| `stage.go` | `Stage.Authenticate`: credential selection and precedence (the API key first on the routes that declare it), endpoint rules, anonymous, challenges (the registry's below `/v2/`), error mapping; `byRegistryToken` (a `Bearer trv_rgt_` below `/v2/`, R-OCI-005); `AuthenticateTokenRequest` with `Presented`, `PresentedFrom`, `PresentedForm` for the OCI token service, whose route `/v2/_token` the chain does not authenticate (Q-OCI-51) |
| `limiter.go` | `FailureLimiter` (the narrow limiter interface), `LocalLimiter`, the per-request throttle |
| `standing.go` | `JobStanding`: what the subject a job carries (`authz.JobSubject`) holds now, for every batch of the delete job (Q-AUTHZ-260): the principal's status, kind, name and groups from `principal.Store` (a changed kind is refused), the token by ID (`TokenStanding`, `token.Service.Standing`) or the session by ID (`SessionStanding`, `session.Standing`); a principal that is not active and a withdrawn credential are the 401 a request would get (Q-AUTHZ-261, Q-AUTHZ-262), a purged token or session has a reason of its own (Q-STOR-194) |

### internal/auth/principal

| File | Responsibility |
|---|---|
| `principal.go` | The contract: `Store`, `Principal`, `Kind`, `Status`, `LoginClaims`, `Provisioner` |
| `store.go` | `PGStore`: `ByID`, `ByName`, `Groups` over the cache `auth_principals` |
| `manager.go`, `groups.go`, `serviceaccounts.go` | `Manager`: users, groups and memberships, service accounts (R-AUTH-016, R-AUTH-018, R-AUTH-020); `Caller.RoleHolders` is the `roles:write` check the API sets; `AuthzHook` (optional, `authz.PrincipalHook` in the wiring) is told of every deleted or renamed principal in the same transaction (R-AUTHZ-028); a duplicate create is `409 already_exists` |
| `serviceaccounts.go`, `projectaccounts.go` | The service accounts of the instance and of a project (Q-AUTHZ-169, Q-AUTHZ-209). A project's account is stored as `<projectKey>/<name>` in `principal.name` (the trigger `service_account_project` of 00019 keeps the prefix and `service_account.project_id` together, SQLSTATE `AI003`), so `UNIQUE (kind, name)` keeps both rules and the same name lives in every project and once instance-wide. The methods without `Project` in the name (`CreateServiceAccount`, `GetServiceAccount`, …) address instance-wide accounts: `loadAccountOf` refuses a name with a `/` and an account of another place, so the account of a project answers 404 there. The `…Project…` methods take a `qname.Object` (`ParseObject`, `ServiceAccount.Object`); `createServiceAccount` locks the project `FOR SHARE` (404 `not_found` for none), refuses `Admin` (422 `role_not_allowed`) and writes `project` into the `serviceaccount.*` events. `deleteAccount` revokes the account's tokens, notifies the change areas and only then deletes the principal row, whose token rows go with it by cascade. `ServiceAccountByRef` reads what follows `sa:` (`<name>` or `<project>/<name>`). `ListProjectServiceAccounts(project, query, after, limit)` matches `query` against the name within the project |
| `groups.go` (projects) | `refuseProjectAccount` in `changeMembers`: a project service account is no group member (422 `project_account_not_groupable`); `postgres.IsState(err, "AI002")` maps the trigger's refusal to the same answer (Q-AUTH-191) |
| `guards.go` | `guardRoleHolder` (a principal that holds a system role, or a group that carries one, is changed only with `roles:write`, Q-AUTHZ-69) and `LastAdminGuard` (`last_admin` over active holders, locked on the `admin` binding rows; authz uses it in `UnbindRole`, Q-AUTHZ-70) |
| `provisioner.go` | `PGProvisioner.SignIn`: just-in-time user, identity link, rename, group sync (R-AUTH-007, R-AUTH-008); `IdentityGroupsByName` leaves out a group with `sync_detached`, and `createOIDCGroup` answers an empty ID for a detached group of the name, so the login goes on without it (Q-AUTH-206) |
| `change.go` | The payload of the change area `auth` and its notify helpers |
| `token_events.go` | `RevokeAllTokensTx` (revoke every unrevoked token of a principal) and `RecordTokensRevoked`: the one place that builds `token.revoked` with its journal entry `{tokenId, reason}`, for the token service's revocations and for the disables and deletions of `Manager`; the events go through `audit.RecordAllTx`, so a principal's thousands of tokens cost a fixed number of statements (Q-AUTH-198) |
| `journal_replay.go` | The replay handlers of the identity events (R-HA-054): disable a user, a service account or a Bastion registration, delete a group, remove memberships, clear a group's auto-join flag (`autoJoinOrder`, the update rule), delete the pending links a `user.updated` removed (`replayUserUpdated`, Q-AUTH-205) and detach a group a `group.updated` renamed (`replayRenameDetach`, Q-AUTH-206: `sync_detached`, the `oidc:*` memberships deleted, the name never written). The entries are `userUpdatedEntry` and `groupUpdatedEntry`, read strictly (`readUserUpdated`, `readGroupUpdated`) |
| `gate.go` | `Gate` (freshness), `Flights` (shared lookups while the gate is closed), `Metrics` |
| `ids.go`, `errors.go`, `db.go` | UUIDv7 and name rules; the refusals; the `DB` and `Notifier` interfaces |

### internal/auth/token

| File | Responsibility |
|---|---|
| `token.go` | The contract: `Type`, `Scope`, `Info`, `Validator`, the `Err*` values |
| `format.go` | Generate, `Check` (syntax step), `Hash`, `Hint` (R-AUTH-022) |
| `scope.go` | aliases of the scope type (`Scope`, `ScopeGrant`, `Patterns`, `ScopeOptions`) and forwards (`ParseScope`, `DefaultScope`, `ParseStoredScope`); the type, `Parse` (validation, limits), `Marshal` and `Within` (the derivation rule) live in the leaf [internal/auth/scope](../../internal/auth/scope/scope.go), which the stage, `httpserver.Credential` and `authz.Subject` use as well. `scope.Options` (`ScopeOptions`) carries the cross-area checks (actions, system items, endpoints and the three pattern dimensions); `app.TokenScopeOptions` fills them from `authz.Actions()`, `authz.Capabilities()` and the authz validators (Q-AUTH-151). The `repositories` dimension is `authz.ValidateSelector`, which reads a selector in the instance context (`<projectGlob>/<keyGlob>[:<format>]` or `@any…`): a bare key is `400 invalid_scope` (R-AUTH-024, Q-AUTHZ-216) |
| `lifetime.go` | `resolveLifetime` (R-AUTH-026) |
| `service.go` | `Service`: `Create`, `Get`, `List` (`ListFilter.Line` narrows it to one token and its line: the tokens derived from it or from its rotation predecessors), the record types; `CreateRequest.Delegated` (an Owner of the project, without an administrator capability, issues the token of a project service account: no `system` item, `admin: false`, 422 `invalid_scope`; not self-rotatable, 422 `validation_failed` at `/selfRotatable`, Q-AUTH-192; both in `checkDelegated`; never non-expiring, capped at the SAT maximum, Q-AUTH-190); `token.created` and `token.revoked` carry the owner's `project`; `insert` with `lineageCheck` (locks the parent and the rotated token with their `derived_from` chains and decides on what it finds then) |
| `rotate.go`, `revoke.go` | `Rotate` (the successor keeps the lineage, Q-AUTH-89), `Revoke`, `RevokeByValue`, `RevokeAll`; `revokeTree` locks the tree before the cascade; `DescendsFrom` (whether a token's `derived_from` chain leads to another or to one of its rotation predecessors, the line of a narrowed token in the token API, Q-AUTH-199) |
| `validate.go` | `Service.Check`/`Validate`: the validation path, the cache `auth_tokens`, `token.rejected`; the type `bst` is refused here as unknown before the cache and the table (R-BAS-005); `CheckSecretHash`, the same path from the cache on for a registry token's binding (R-OCI-005) |
| `standing.go` | `Service.Standing`: a token by its ID from PostgreSQL, never the cache, for a job the token queued (Q-AUTHZ-261): `ErrRevoked`, `ErrUnknown`, or the facts with the scope and a CI token's bundle parsed strictly; an expired token answers its facts |
| `bastion.go` | The Bastion token (type `bst`, R-BAS-004): `CreateBastion`/`CreateBastionTx` (the lifetime rule of `auth.tokens.bst`, the limit per registration under the owner lock), `RevokeBastionToken`, `RevokeAllOfBastionTx` (deleting a registration), `AuthenticateBastion` (the validation of the interface, through the cache `auth_tokens` and `BastionTokenBySecret`) |
| `settings.go` | `SettingsStore`: cache `auth_settings`, `Effective`, `Update` (R-AUTH-040); the journal entry of `settings.auth.changed` carries `anonymousAccess` and both runtime maxima before and after, `null` for the YAML value (Q-AUTH-207) |
| `journal_replay.go` | The replay of the token events; `replaySettings` replays `settings.auth.changed` part by part: the anonymous switch (`switchOrder`) and each maximum (`maxOrder`, a total order with `null` the longest, so it never suspends), lowering by `IdentityLowerPATMax` and `IdentityLowerSATMax`, which also take a default above the new maximum down to it |
| `tasks.go` | `Cleanup` (`token-cleanup`), `NoticeExpiring` (`token-expiry-notice`) |

## Request path

```text
request ─► chain stage 6 ─► app.authenticate ─► auth.Stage.Authenticate(r, rules)
  exchange route (POST)            ─► anonymous, Authorization not read
  X-NuGet-ApiKey with a well-formed token (whole value or after the last ':'), Options.APIKeyHeader on,
    and rules.APIKeyRoute(r) true  ─► the same token path as Bearer, method api_key   (any other value: ignored)
  Authorization present            ─► Bearer | Basic(any:token) ─► token.Check (syntax, endpoint type rule, no DB)
                                      ─► token.Service.Check(raw, Request{Throttle, Source})
                                           cache hit (gate open)          ─► Info
                                           throttle Exceeded? marker?     ─► 429 or continue
                                           PostgreSQL join with the owner ─► Info | ErrUnknown|Revoked|Expired|Owner
                                      ─► endpoint binding rules, groups from principal.Store
  session cookie, path rule, not an API-key route ─► session.Validator (+ CSRF)   (nil: ignored)
  nothing                          ─► anonymous | 401 credentials_required
```

- A credential that is present and invalid is a 401 and never falls through. Unknown Bearer values (JWTs, passwords,
  the break-glass password) are refused at the syntax step without a database query.
- Errors: `invalid_token` for malformed, unknown, revoked, expired and wrongly bound tokens (the response does not say
  which), `principal_disabled` for a disabled owner, `credentials_required` without a credential, `session_expired`,
  `rate_limited` (429 with `Retry-After`) when the address is throttled, 503 when PostgreSQL does not answer.
- The challenge is `Bearer realm="trove"` (plus `error="invalid_token"` after a rejected token) under `/api/v1/`,
  `Basic realm="trove"` for a package client on `/files/` and `/api/<format>/`, and `Bearer realm="trove"` everywhere else
  and for a browser (Q-AUTH-173).
- The session cookie is accepted on `/api/v1/` for every method, on `/files/` for GET, HEAD, PUT and DELETE, on
  `/api/<format>/` for GET and HEAD, and ignored elsewhere and on `/api/v1/auth/login-options`, so an ended session's cookie
  cannot keep the sign-in screen from loading (Q-AUTH-177).
- Every path rule above reads the path of `rulePath`: the request path where the process serves the request's family
  (`EndpointRules.Served`), and `""` where it does not, so a request of an unserved family gets the challenge, the cookie
  rule and the anonymous verdict of `/does-not-exist` (Q-ARCH-302, Q-ARCH-306). On `api`, an ended session's cookie on
  `/files/x` is ignored as on an unknown path instead of answering `session_expired`.
- Anonymous: the endpoint allows it (`EndpointRules.Anonymous`), `anonymousAccess` is on and the ceiling
  `auth.anonymous.allowed` is true (`SettingsStore.Effective`), and the method is safe if the path is under `/api/v1/`.
  Independently of the runtime switch, on an endpoint that allows anonymous requests, a request is anonymous when the
  route is in the always-open list (`/auth/`, login options, device endpoints, `POST /api/v1/tokens/revoke`, the CI
  exchange, the npm login start, `GET`/`HEAD` of `/` and `/ui/…`) or when `EndpointRules.PublicRoute` says the matched
  management operation is declared public (Q-AUTH-19). An endpoint that allows no anonymous request (a further endpoint
  by default, R-EP-010) answers every request without a credential with the same 401, the always-open routes included.
- The NuGet API key (R-AUTH-033, Q-AUTH-186, Q-AUTH-187): `X-NuGet-ApiKey` is read only while `auth.clients.nuget_api_key_header` is on
  (`Options.APIKeyHeader`) and only on a request for which `EndpointRules.APIKeyRoute` answers true. The composition root wires it to
  `Dispatcher.APIKeyRoute` ([dispatcher.go](../../internal/api/dispatch/dispatcher.go)), which runs the dispatcher's own steps up to
  `Format.Match` and reports `Route.APIKey`; the shell profile has no dispatcher and so no API-key route. The stage asks at most once per request
  (`routeAnswer`), and only for a token-shaped header value or a session cookie the path rule would accept. The value is a token as a whole or after
  its last `:`; anything else (`az`) is ignored and `Authorization` applies; a token there is validated like a Bearer token, and a rejected one is a
  401 that never falls back. On an API-key route the session cookie is never a credential. The metric label is `nuget_apikey`, the principal's method
  `api_key`. The integration test is [test/integration/apikey](../../test/integration/apikey/apikey_test.go), on a test format with NuGet-shaped routes.
- Not built: the GitLab job-token extension point (M4).
- Last use (R-AUTH-031, Q-AUTH-178): the stage calls `Service.NoteUse` through its explicit `auth.Options.Uses` (Q-AUTH-183) once the whole
  authentication succeeded, after the endpoint binding and the owner's groups, cache hit or not; `Check` notes nothing
  ([lastused.go](../../internal/auth/token/lastused.go)). The buffer is a map from token ID to the latest time and address, under
  a mutex; `FlushLastUsed` takes it and writes one statement (`IdentityTouchTokensLastUsed`, rows locked in ID order, a row changes
  only when it is `auth.tokens.last_used_granularity` behind). A failed batch is dropped and counted
  (`trove_auth_lastused_flush_failures_total`). `lastUsedComponent` in [wire_identity.go](../../internal/app/wire_identity.go) runs
  the 60 s loop (`RunLastUsed`, whose flush runs under `context.WithoutCancel`) and is a `Flusher`: its `Flush` ends the loop, waits for the batch in
  flight and then writes what is left, before the pools close. The statement caps the time at `now()` (Q-AUTH-180). The integration test
  is `TestR_AUTH_031_LastUsedIsRecordedInBatches` in [test/integration/identity/lastused_test.go](../../test/integration/identity/lastused_test.go).

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
concurrent lookups of one key share a query only if it started less than the bound before (`Flights`; the query runs on its own and each
caller waits under its own context, Q-AUTH-208). A lookup that PostgreSQL does not answer is 503. `trove_auth_cache_bypassed_total{reason="changes_stale"}` counts them.

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

## The Bastion's token in the stage (R-BAS-008)

The stage never sees a Bastion token: `token.Service.Check` answers a `trv_bst_` value as an unknown token, and a Bastion request is recognized
before the stage runs ([bastion.md](bastion.md#package-2-the-main-installations-interface)). Three things there touch the stage: `auth.Credential.ExpiresAt`
(and `httpserver.Credential.ExpiresAt`) carry the validated token's expiry for the marking and the check; `auth.NewAddressThrottle(limiter, addr, exempt, scope)`
builds the throttle of one address, with a bucket prefix for a kind of credential that counts apart (the Bastion token, `bastionmain.ThrottleScope`);
and the check of the Bastion interface calls the same stage function as a direct request does, from a request of its own with the caller's address
(`httpserver.WithClient`), so the rules of this page apply to the caller of a Bastion unchanged.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | `internal/auth/stage_test.go` | precedence, no fall-through, challenges, session path rules, anonymous rules, exchange routes, endpoint rules, the Basic switch, failure codes; E1.8 for Bearer and Basic against a shared fake limiter and against per-replica shares |
| Unit | `internal/auth/token/*_test.go` | the format test vector, scope validation, the derivation rule, lifetime table, the closed gate with an unreachable database (503), throttle consultation, cache eviction by payload kind |
| Unit | `internal/auth/principal/principal_test.go` | name rules, UUIDv7, payload helpers, shared lookups; a Bastion is no group member (`TestR_BAS_004_ABastionIsNoGroupMember`) |
| Unit | `internal/auth/bastion_token_test.go`, `internal/bastionmain/authenticator_test.go` | `TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute` (Bearer, Basic and API key on the npm, raw-file and management routes with the real token service: 401 `invalid_token`, no read of the token table), the mapping of the authenticator's verdicts |
| Integration, HTTP | `test/integration/bastionreg/` | `TestR_BAS_004_*` and `TestR_BAS_005_*`: registrations, tokens, limits, capabilities, the refusals (`principal_not_allowed`), the events and their journal entries, the authenticator across two replicas, the admin commands (builds the binary) |
| Integration | `test/integration/identity/` | two or three in-process replicas against the shared stack, each with Valkey running or stopped: E1.2 (database text scan finds no token value; revocation within 2 s), the provisioner and group sync, concurrent first logins, memberships, disable/enable/delete, service accounts, creation limits under concurrency, derivation, rotation, revocation by value, settings and the anonymous switch across replicas, throttle markers, the scheduled tasks |
| Integration, HTTP | `test/integration/identity/api_*_test.go` | the same replicas serving the real handlers ([test/integration/idapi](../../test/integration/idapi): the production chain, the real stage and decision point), every answer validated against the document: `TestAPIOperations` reaches every identity operation and asserts `Uncovered` is empty; E1.2 over HTTP with Valkey running and stopped; concurrent idempotent creates on three replicas; E1.8 for Bearer and Basic across three replicas (cluster limiter, and the per-replica share without Valkey); the flow-start budget of the revocation by value; `audit_event` rows for token creation and revocation (E1.5) |

Run the integration area from the checkout root with the stack up (see [build-and-test.md](build-and-test.md)):
`set -a; . /Users/hfi/repos/trove/.dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/identity/...`

## Journaled narrowings (F-72)

Three identity changes that a restore would undo are journaled and replayed (R-AUTH-041, R-HA-054). The replay of each only removes or
narrows, and every entry is read strictly: a missing key, a wrong type or a list over 1000 entries is `skipped`.

| Change | Entry | Replay |
|---|---|---|
| A pending identity link removed (`PATCH /api/v1/users/{username}` with `identities`) | `user.updated`: `{principalId, name, pendingLinksRemoved}` on every update, the list empty when none was removed; `IdentityReplaceIdentities` returns the removed providers (`RETURNING provider_id`) | `IdentityDeletePendingLinks` deletes the pending links (`subject IS NULL`) of those providers; a linked identity is never removed; the break-glass account is malformed. No cache holds pending links (the login reads `user_identity` in its transaction), so no change area is told |
| A group renamed | `group.updated` gains `renamedFrom` when the update renamed | `sync_detached = true`, the `oidc:*` memberships deleted, `ChangeGroup` notified (flushes the principal cache); the name is never written. Already in effect when the restored name equals the entry's `name` or the group is detached |
| A token lifetime maximum | `settings.auth.changed` gains `tokens.pat.maxTTLSeconds` and `tokens.sat.maxTTLSeconds`, `{before, after}` each, all keys required, a value 60 s to 100 years (`config.MaxTokenLifetime`, the `max=876000h` of the two `max_ttl` keys and the settings API) or `null`; the parts are read independently | each maximum is lowered to the smaller value; `null` and a longer value change nothing; the change raises `auth_settings.revision` and tells the settings area |

The column `user_group.sync_detached` (migration 00052) is exported with its table: the backup classifies by table (`tables_m1.go`, class
`data`), so no registration changes. Only the replay sets it; `UpdateGroup` takes `false` only and refuses `true` with 422 `invalid_value`
at `/syncDetached`. A rename never changes it.

Tests: `TestR_AUTH_041_UserUpdatedEntriesAreReadStrictly`, `TestR_AUTH_041_GroupUpdatedEntriesAreReadStrictly`,
`TestR_AUTH_041_SettingsEntriesAreReadStrictly`, `TestR_AUTH_026_TheMaximumOrderTreatsNullAsTheLongest` (unit);
`TestR_AUTH_016_ARemovedPendingLinkIsJournaledInItsTransaction`, `TestR_AUTH_018_ARenameCarriesTheFormerNameInItsEntry`,
`TestR_AUTH_040_ASettingsChangeCarriesTheMaximaInItsEntry`, `TestR_AUTH_008_ADetachedGroupIsNeitherFilledNorCreated`,
`TestR_AUTH_018_SyncDetachedIsClearedOnlyToFalse` in [test/integration/identity/journal_identity_test.go](../../test/integration/identity/journal_identity_test.go)
(the writers); `TestR_AUTH_016_ARemovedPendingLinkStaysRemovedAfterARestore`, `TestR_AUTH_018_AGroupRenamedAfterTheRestorePointIsDetached`,
`TestR_AUTH_026_ALoweredMaximumStaysLoweredAfterARestore` in [test/integration/backup/journal_identity_narrowing_test.go](../../test/integration/backup/journal_identity_narrowing_test.go)
(the replay against a restored copy, with hand-written entries and a second pass).

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

### The routes of a project's service accounts (R-AUTHZ-026, Q-AUTHZ-209)

The accounts of a project live under `/api/v1/projects/{projectKey}/service-accounts[/{name}[/disable|/enable|/tokens]]`, which admit an Owner of
the project ([projects.md](projects.md)) besides the holders of `identities:read` and `identities:write`; the routes under `/api/v1/service-accounts/{name}`
address instance-wide accounts only. A project the caller is no member of answers 404 as a missing one does; a member that is not an Owner gets 403.
The handlers pass `qname.Object{Project, Name}` to the `…Project…` methods of the manager and the token service:

| Operation | Manager or token call |
|---|---|
| `createProjectServiceAccount` | `Manager.CreateProjectServiceAccountTx(ctx, tx, caller, project, NewServiceAccount{Name, Description})` |
| `listProjectServiceAccounts`, `getProjectServiceAccount` | `ListProjectServiceAccounts(ctx, project, q, after, limit)`, `GetProjectServiceAccount(ctx, obj)` |
| `updateProjectServiceAccount`, `disableProjectServiceAccount`, `enableProjectServiceAccount`, `deleteProjectServiceAccount` | `UpdateProjectServiceAccount`, `DisableProjectServiceAccount`, `EnableProjectServiceAccount`, `DeleteProjectServiceAccount`, each with the `qname.Object` |
| `createProjectServiceAccountToken` | the account from `GetProjectServiceAccount`, then `token.Service.CreateTx` with `Type: TypeSAT`, `OwnerID: account.ID`, `Delegated: true` for an Owner who holds no `identities:write`, `Admin: false` then |
| `revokeProjectServiceAccountTokens` | `token.Service.RevokeAll(ctx, caller, account.ID, reason)` |
| `listTokens` with `owner=sa:<project>/<name>` | `Manager.ServiceAccountByRef` for the owner filter |

| `createToken` | `POST /api/v1/tokens` | authenticated (handler: a user with a recent session, or a PAT under the derivation rule), idempotent |
| `listTokens` | `GET /api/v1/tokens` | authenticated (own tokens, through a narrowed token only its line, `ListFilter.Line`; all with `tokens:read`) |
| `getToken` | `GET /api/v1/tokens/{id}` | authenticated (404 for a token the caller may not see; a narrowed token sees only its line, `inLine` in `visible`) |
| `getCurrentToken`, `revokeCurrentToken`, `rotateCurrentToken` | `GET`, `DELETE /api/v1/tokens/current`, `POST /api/v1/tokens/current/rotate` | authenticated (404 `not_a_token` for a session) |
| `revokeToken` | `DELETE /api/v1/tokens/{id}` | authenticated (owner, through a narrowed token only its line, 404 otherwise; `tokens:revoke`; an Owner of the account's project) |
| `rotateToken` | `POST /api/v1/tokens/{id}/rotate` | authenticated (owner of a PAT; administrator or the account itself for a SAT; a narrowed token only its line, 404 for a token it does not see and 403 for one it sees through `tokens:read`), idempotent |
| `revokeTokenByValue` | `POST /api/v1/tokens/revoke` | public, counted in `auth.flow_starts` |
| `revokeOwnTokens` | `DELETE /api/v1/users/me/tokens` | authenticated (a user), `x-trove-owner-rights` (a session or an inheriting token) |
| `revokeUserTokens`, `revokeServiceAccountTokens` | `DELETE /api/v1/users/{username}/tokens`, `/service-accounts/{name}/tokens` | `tokens:revoke` |
| `createServiceAccountToken` | `POST /api/v1/service-accounts/{name}/tokens` | authenticated (an administrator, or a token of the account), idempotent |
| `getAuthSettings`, `updateAuthSettings` | `GET`, `PATCH /api/v1/settings/auth` | `settings:read`, `settings:write` |

How the pieces fit:

- **Idempotent creates are one transaction with the idempotency row.** `Idempotency.Do` hands the operation a transaction; the handler passes
  calls the `Tx` form of the manager or the token service inside it: `Manager.CreateUserTx`, `CreateGroupTx`,
  `CreateServiceAccountTx`, `Service.CreateTx` and `RotateTx` (`…Tx(ctx, tx, …)`, Q-AUTH-46, Q-AUTH-174). The methods without `Tx` open
  their own transaction and call the same body; a `Tx` form writes, records its change and its audit event, and reads the record it
  returns on `tx`, so a later write of the same transaction sees the principal rows of the earlier one and a rollback takes all of
  them away. Only the creates and rotations the API makes idempotent have a `Tx` form. A token create sets `Outcome.SecretID`: the stored outcome has no body, and a replay is
  `409 token_already_issued` with the token ID.
- **The caller's rights that depend on the data are decided in the handler.** The authorization step has checked the declaration; the
  handlers add the rules of the specification that need the record: the owner of a token, the derivation rule (`presenting` builds the
  parent `token.Info` from the token row), no impersonation, and `404` for what the caller may not see. A capability check is never
  repeated here, and neither is the recent sign-in of a session: the operations that mint a credential declare
  `x-trove-recent-auth: true` and the shared step enforces it (R-AUTH-011, [http-api.md](http-api.md), Q-API-40). Nor is the scope of the
  presenting token on the operations of the caller's own account: they declare `x-trove-owner-rights: required` and the shared step refuses
  a token whose scope does not inherit (Q-API-214). The token operations keep one rule of that kind in the handler, because it needs the
  record: a narrowed token sees, revokes and rotates by ownership only itself and the tokens derived from it (`inLine` in `visible`, so
  any other token of the owner is a 404; `ListFilter.Line` for the list; `token.Service.DescendsFrom`; a rotation outside the line of a
  token seen through `tokens:read` is 403 in `rotationParent`, Q-AUTH-199).
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

## User avatars (R-API-055)

A user uploads a picture for their own record; the server keeps a new image made from its pixels. The security view is
[user-avatars](../security/user-avatars.md); this is where the code is.

| File | Responsibility |
|---|---|
| [internal/avatar/process.go](../../internal/avatar/process.go) | The constants (`MaxUpload`, `OutputSide`, `MinSide`, `MaxPixels`, `MaxDecodeBytes`, `MaxScans`, `MaxBlockVisits`, `DecodeTimeout`, `MaxConcurrent`, `BusyWait`), the refusals (`ErrTooLarge`, `ErrUnsupportedType`, `ErrTypeMismatch`, `ErrAnimated`, `ErrDimensions`, `ErrCorrupt`, `ErrBusy`) and `Processor.Process`: sniff, header size, JPEG marker walk (`walkJPEG`, the scans and `scanVisits`) and memory estimate (`decodeCost`), slot, decode under a deadline (`ctxReader`), crop, scale, encode |
| [internal/avatar/worstcase_test.go](../../internal/avatar/worstcase_test.go), [worstcase_bench_unix_test.go](../../internal/avatar/worstcase_bench_unix_test.go) | The slowest images the checks accept, built in code (Paeth-filtered Adam7 16-bit PNG, progressive JPEGs with dense scans and with scans of end-of-band runs that spend `MaxBlockVisits`, a baseline JPEG of noise, a lossless WebP), each inside `MaxUpload`; the test that they are accepted and the timing test (both skipped under `-race`; `TROVE_PERF_GATE=1` decodes each input seven times and holds the least CPU time to one second; the wall time is only logged) and `BenchmarkProcessWorstCase` (wall and CPU time per op); [cpu_unix_test.go](../../internal/avatar/cpu_unix_test.go) reads the process's CPU time. [budget_test.go](../../internal/avatar/budget_test.go) holds the refusals by visit count and the deadline test of a file of few bytes |
| [internal/avatar/orient.go](../../internal/avatar/orient.go) | The EXIF orientation reader and `orient`, applied to the scaled square |
| [internal/avatar/store.go](../../internal/avatar/store.go) | `Store`: `Put` (process, spool, `Commit` with a link that upserts `user_avatars` and records the event), `Remove`, `Open` (lookup by digest, verified read) |
| [internal/api/v1/avatar_api.go](../../internal/api/v1/avatar_api.go) | The four handlers, `readImage`, `avatarErr` (the mapping of refusals to problems), the image headers; `AvatarService` is the seam the handlers take |
| [internal/app/wire_avatar.go](../../internal/app/wire_avatar.go) | `avatarService`: one `Store` per replica over the infra database, blob store and recorder, so the decoding slots are the replica's |
| [internal/meta/postgres/queries/avatars.sql](../../internal/meta/postgres/queries/avatars.sql) | `AvatarUpsert`, `AvatarDelete`, `AvatarByDigest`, `AvatarDigests` |
| [internal/meta/postgres/tables_avatar.go](../../internal/meta/postgres/tables_avatar.go) | The export class of `user_avatars` (`data`) |

- **The user record.** `principal.User.AvatarSHA256` is filled for a batch of users by `fillAvatars` in one statement (`GetUser` and
  `ListUsers`); `toUser` turns it into `avatarUrl` (`/api/v1/avatars/<sha256>` or `null`). The triggers of `user_avatars` bump
  `principal.revision`, so the ETag of the record follows the avatar (Q-API-210).
- **The blob.** The table has the `blob_sha256` column, its foreign key and index, and the three reference triggers of R-STOR-003, so
  the collector keeps the PNG while a row names it and collects it after a replacement, a removal or the deletion of the user. `Put` commits
  the blob and the row in one link transaction (R-STOR-005).
- **Adding a format** means a decoder in `config` and `decode` and a sniff rule in `check`, a fixture, a test per refusal of
  [process_test.go](../../internal/avatar/process_test.go), and a change of R-API-055; do not add a format whose decoder reads
  a stream the header does not bound.
- **A new format or a changed bound** needs a slowest-case input in `worstCases` and a reading of the benchmark in the cgroup of a
  pod, as Q-UI-173 did: `docker run --rm --cpus=1 -v "$PWD":/src -v "$HOME/go/pkg/mod":/go/pkg/mod -w /src -e GOTOOLCHAIN=local golang:1.27.3 go test -run xxx -bench ProcessWorstCase -benchtime 3x -count 5 ./internal/avatar/`
  (not on a loaded host: take the least CPU time, which is the image's cost, not the median). The inputs are not part of the plain run of `make test`: building them takes 14 s, so `make test-performance` runs them.
- **Tests.** Unit: `go test ./internal/avatar/` (fixtures in `testdata/`, made with a WebP encoder outside the repository; the
  animated ones are two-frame files). Integration, against the stack with the role `api`:
  [test/integration/api/avatar_test.go](../../test/integration/api/avatar_test.go). The refusal of a token limited to part of its
  owner's rights is the shared step's (`x-trove-owner-rights`, Q-API-214), not `avatarOwner`'s; its unit tests are in
  [owner_rights_test.go](../../internal/api/v1/owner_rights_test.go).

## Bastion registrations and tokens (R-BAS-004, R-BAS-005)

A Bastion registration is a principal of the kind `bastion` plus a row of the table `bastion`; its tokens are `api_token` rows of the type
`bst`. The API is `listBastions`, `createBastion`, `getBastion`, `updateBastion`, `deleteBastion`, `createBastionToken` and
`revokeBastionToken` in [bastion_registrations_api.go](../../internal/api/v1/bastion_registrations_api.go); the rules are
`bastionmain.Registrations`, which the handlers and `trove admin bastion` call. What differs from a service account:

- **The token API does not see a `bst` token** (`IdentityListTokens` and `IdentityTokenRow` exclude it): it is issued, listed and
  revoked through its registration. `token.Service.Check` refuses the type as unknown before the cache and the table.
- **Validation** is `token.Service.AuthenticateBastion` through the same `auth_tokens` cache, freshness gate and change area as every token;
  `bastionmain.Authenticator` maps its verdicts to `ErrTokenInvalid` and notes the use for `lastUsedAt`. Disabling a registration
  notifies `ChangePrincipal` (the cache flushes) and revokes nothing; deleting one revokes every token with its journal entry first
  (`principal.RevokeAllTokensTx`).
- **A registration holds nothing.** `authz.ParseRef` parses `bastion:<name>`; the policy writes, role bindings, owner records, group
  members and project members refuse it with `principal.BastionNotAllowed` (422 `principal_not_allowed`); the database guards are
  the second line.
- **Events**: the five `bastion.*` events and `token.created` / `token.revoked` with `resource_type` `bastion` (the token as the resource,
  the registration in `details.bastion`). `Update` records `bastion.updated` for a description, `bastion.disabled` (journaled,
  `registrationEntry`) for a disable and `bastion.enabled` for an enable; `Delete` journals `bastion.deleted` as well. The replay
  of both is `replayDisableAccount` with the kind bastion in `internal/auth/principal/journal_replay.go` (Q-BAS-145).
- **Wiring**: the `package 1` block of [wire_bastion.go](../../internal/app/wire_bastion.go) builds `bastionRegistrations` (passed as
  `v1.Options.Bastions`) and `bastionAuthenticator`, which the recognition stage of the interface consumes.

## Bootstrap commands

`trove admin service-account create`, `token create|revoke` and `user disable` (R-AUTH-015) are
[cmd/trove/admin_identity.go](../../cmd/trove/admin_identity.go). They build the manager and the token service over the
command's own connection, with empty caches and an `audit.Writer` of their own, and call the same domain functions as the API
with the actor `admin-cli`. `service-account create --admin` binds the role in the creating transaction through
`authz.Service.BindAdmin`, which notifies the area `authz` and records `role.bound`. `token create` prints only the secret on
standard output (default lifetime 15 minutes, the SAT maximum applies; `--scope-file` is validated by
`app.TokenScopeOptions`); `token revoke --value-stdin` reads the value from standard input and revokes with reason `leaked`.
The integration test is [admin_test.go](../../test/integration/login/admin_test.go).

`trove admin bastion create <name>`, `bastion token create <name> [--ttl <duration>]` and `bastion token revoke <name> <id>`
([admin_bastion.go](../../cmd/trove/admin_bastion.go), R-BAS-004) use the same `identityParts`, with `bastionmain.Registrations` on top;
`token create` prints only the secret. The integration test is [cli_test.go](../../test/integration/bastionreg/cli_test.go).

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

Additive changes to the shared contracts (no consumer breaks): `auth.EndpointRules.PublicRoute`; `auth.EndpointRules.APIKeyRoute`,
`auth.Options.APIKeyHeader`, `auth.HeaderAPIKey` and `dispatch.Dispatcher.APIKeyRoute` (Q-AUTH-186);
`principal.LoginClaims` gained `Issuer`, `EmailVerified`, `GroupSync`, `ClaimState`, `KeepOnMissing`, `CreateGroups`, `JIT`,
`SyncUsername`, `Source` (Q-AUTH-21); the codes `non_expiring_restricted`, `token_limit_reached`,
`derivation_exceeds_parent`, `description_required`, `not_a_token`, `username_invalid` in `internal/errs/codes.go`.
