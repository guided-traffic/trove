# M1 contracts

The shared Go contracts of milestone M1 (access core, [roadmap](../coverage/roadmap.md)). The work packages
build in parallel worktrees; this page says which package and which marked section belongs to whom, so that
they merge without conflicts. Where a contract must change, the owner says so in the report and updates this
page in the same change.

The database tables are not on this page: see [database.md](database.md) (migrations `00006` and up).

## Areas

| Area (marker name) | Work package | Content |
|---|---|---|
| `audit` | WP1.7 | Event registry with the journal flag, recorder, outbox row, audit query API |
| `api` | WP1.6 | Conventions (errors, cursors, ETags, idempotency), jobs and tasks, settings, rate limits, CORS, GC trigger, key-ring status |
| `identity` | WP1.2 | Users, groups, service accounts, tokens, the authentication stage |
| `login` | WP1.1 | OIDC, sessions, CSRF, logout, break-glass, bootstrap commands |
| `authz` | WP1.3 | Policies, roles, evaluation, compiled grant index, decision audit |
| `repository` | WP1.4 | Repository model and API, generic local class |
| `files` | WP1.5 | `/files/` raw transfer, items API |
| `endpoints` | WP1.9 | Credential rules per endpoint, bound tokens, exposure rules and their ceiling |
| `ui` | WP1.8 | `web/` only; it consumes the OpenAPI types and adds no Go contract |

## Package map

```text
internal/
├── httpserver/principal.go      edge   Principal, Credential, Scope: what the chain stores (all areas read it)
├── audit/                       domain WP1.7  registry.go, event.go (contract); events_<area>.go per area; recorder
├── auth/                        domain WP1.2  auth.go: Authenticator, Result, EndpointRules (contract); the stage
│   ├── principal/               domain WP1.2  Store, Principal, LoginClaims, Provisioner (contract); the table's code
│   ├── token/                   domain WP1.2  Type, Scope, Info, Validator (contract); format, store, rotation
│   ├── session/                 domain WP1.1  Info, Validator (contract); sessions, CSRF
│   └── oidc/                    domain WP1.1  providers, login flow, callback, break-glass
├── authz/                       domain WP1.3  authz.go: Subject, Coordinate, Decision, Authorizer (contract); the engine
├── repository/                  domain WP1.4  repository.go: Repository, Class, Reader (contract); store, snapshot
├── api/v1/subject.go            edge   SubjectFrom, NewAccess: the one conversion Principal -> Subject (WP1.3 and WP1.6 keep it)
├── app/m1.go, m1_convert.go     app    m1Wiring (components by area), authenticate/access selection, Result -> Principal
└── config/m1.go                 leaf   the empty sections Auth, Authz, API, Repositories with markers
```

Import graph (an arrow is "imports"; nothing points back up, `archtest` enforces the layers):

```text
app ──► api/v1, httpserver, auth, authz, repository, audit, (everything it wires)
api/v1 ─► httpserver, authz, auth/principal, auth/token, repository, audit
httpserver ─► errs                          (imports no domain package; Principal is plain values)
authz ─► auth/principal, auth/token, repository, audit
auth ─► auth/principal, auth/token, auth/session
auth/oidc ─► auth/principal, auth/session, audit
auth/session, auth/token ─► auth/principal
auth/principal, repository ─► errs, meta (and audit where they record events)
audit ─► meta, errs                         (imported by every domain package)
```

Rules that follow:

- `authz` never imports package `auth` (only `auth/principal` and `auth/token`), and no domain package imports
  `httpserver`. The edge type and the domain type are converted at two places only: `principalFromResult`
  ([m1_convert.go](../../internal/app/m1_convert.go)) from `auth.Result` to `httpserver.Principal`, and
  `v1.SubjectFrom` ([subject.go](../../internal/api/v1/subject.go)) from `httpserver.Principal` to `authz.Subject`.
  Extend both when a field is added to either side.
- `httpserver.Principal` carries no administrator flag. Whether the caller holds a capability is decided by
  `authz` from role bindings, the credential's `Scope` and the principal `Kind` (break-glass holds `admin`
  without a binding, R-AUTHZ-010).
- Groups on the principal are resolved by the authentication stage at request time from the `auth_principals`
  cache (R-AUTHZ-002, R-AUTH-023). A token never carries a group list.

## Shared types

| Type | File | Defined by contract | Owner of the implementation |
|---|---|---|---|
| `httpserver.Principal`, `Credential`, `Scope`, `ScopeGrant`, `GroupRef`, `PrincipalKind`, `AuthMethod`, `TokenType`, `AnonymousPrincipal()` | [principal.go](../../internal/httpserver/principal.go) | the value the chain stores; `Anonymous` stays, `Name` stays | WP1.2 fills it |
| `auth.Authenticator`, `Result`, `Credential`, `EndpointRules`, `Method` | [auth.go](../../internal/auth/auth.go) | the authentication stage: credential sources and precedence of R-AUTH-001 to a `Result` or a 401 | WP1.2 (stage), WP1.9 (`EndpointRules` content, R-EP-010) |
| `principal.Store`, `Principal`, `Kind`, `Status`, `GroupRef`, `ErrNotFound` | [principal.go](../../internal/auth/principal/principal.go) | read side of the principal table, cache-backed | WP1.2 |
| `principal.Provisioner`, `LoginClaims` | same | the seam login -> identity: just-in-time user, group sync, `user.*` events in its own transaction | WP1.2 implements, WP1.1 calls |
| `token.Validator`, `Info`, `Scope`, `ScopeGrant`, `Type`, `Err*` | [token.go](../../internal/auth/token/token.go) | validation of a presented token (R-AUTH-023), scope as a ceiling | WP1.2 |
| `session.Validator`, `Info`, `ErrInvalid`, `ErrCSRF` | [session.go](../../internal/auth/session/session.go) | resolves the session cookie, applies CSRF (R-AUTH-012) | WP1.1 |
| `authz.Authorizer` (`Decide`, `Readable`, `Require`), `Subject`, `Coordinate`, `Decision`, `Outcome`, `Action`, `Capability`, `RequestKind` | [authz.go](../../internal/authz/authz.go) | the decision point; action constants of R-AUTHZ-006, capability names of R-AUTHZ-010 | WP1.3 |
| `repository.Repository`, `Class`, `Reader`, `KeyPattern`, `ReservedKeys()` | [repository.go](../../internal/repository/repository.go) | the read model and the lookup by key | WP1.4 |
| `audit.EventType`, `Register`, `Freeze`, `Lookup`, `Types`; `Event`, `Actor`, `Subject`, `Source`; `Recorder`, `Nop`; `Writer`, `Store` | [registry.go](../../internal/audit/registry.go), [event.go](../../internal/audit/event.go), [recorder.go](../../internal/audit/recorder.go), [store.go](../../internal/audit/store.go) | registry with the journal flag; the recorder interface and its implementation (`a.m1.recorder`); the query and maintenance (`a.m1.auditStore`). `Event` also carries `Repository`, `Path`, `Endpoint`, `Actor.TokenID`, `Actor.AuthMethod` and the outcome `Denied` (Q-OPS-40) | WP1.7; page [audit.md](audit.md) |
| `v1.Access` (existing), `v1.NewAccess`, `v1.SubjectFrom` | [handlers.go](../../internal/api/v1/handlers.go), [subject.go](../../internal/api/v1/subject.go) | the permission check of the management API | WP1.3 |

### Request flow, principal to decision

```text
request ─► chain stage 6 (httpserver.Chain.Authenticate)
            app.authenticate(rt)  ── endpointRules(rt) ──► auth.Authenticator.Authenticate(r, rules)
              1. NuGet API key (M2)   2. Authorization: Bearer|Basic -> token.Validator
              3. session cookie -> session.Validator (+ CSRF)   4. none -> anonymous if allowed
              a present credential that is invalid -> 401 (never falls through)
            principalFromResult(res) ──► httpserver.Principal ──► WithPrincipal(ctx)
       ─► stage 7 rate limit (a.m1.rateLimit)
       ─► handler
            management API: authorizingMux -> Server.decide -> v1.Access.Require(ctx, permission)
                              v1.NewAccess -> authz.Authorizer.Require(ctx, SubjectFrom(principal), permission)
            files / formats: sub := v1.SubjectFrom(httpserver.PrincipalFrom(ctx))
                              authz.Authorizer.Decide(ctx, sub, Coordinate{...}) -> Decision
                              Allow | DenyUnauthenticated 401 | DenyHidden 404 | DenyVisible 403 | DenyStale 503 (R-AUTHZ-016)
```

`Decision.Outcome` maps one to one to the status rows of R-AUTHZ-016. Authorization runs before the existence
lookup, so `DenyHidden` is byte-identical to a missing resource. The mapping from `Decision` to an `*errs.Error`
(`Decision.Err`) is added by WP1.3 in `internal/authz/errors.go`; the other areas call it.

The M0 permission name `system.read` is `settings:read` (R-AUTHZ-010). WP1.3 migrates `api/openapi.yaml` to the
capability names and makes `Require` accept the old name until then (`authz.LegacySystemRead`). WP1.3 also
extends `Requirement` and `parseRequirement` in [authorize.go](../../internal/api/v1/authorize.go) for the
`action`/`repository`/`path` and `role` forms of R-API-008; no other area edits that file.

## Marked sections in shared files

Each area edits only between its own `>>> M1 <area>` and `<<< M1 <area>` lines and does not move, reformat or
remove the markers of others. Pairs are separated by an unchanged line, so concurrent inserts merge. Delete
nothing outside your markers.

| File | Section | Markers (areas) |
|---|---|---|
| [api/openapi.yaml](../../api/openapi.yaml) | `tags:` (after `meta`) | audit, api, identity, login, authz, repository, files, endpoints |
| | `paths:` (before `components:`) | the same eight |
| | `components.schemas` (end of file) | the same eight |
| [internal/errs/codes.go](../../internal/errs/codes.go) | constants (after the specific codes) | the same eight |
| | `registry` slice (end) | the same eight |
| [internal/cluster/valkey_keys.go](../../internal/cluster/valkey_keys.go) | key families (end of file) | api, identity |
| [internal/cluster/areas.go](../../internal/cluster/areas.go) | `init` (`RegisterArea`) and the area-name constants | identity, authz, repository |
| [internal/config/m1.go](../../internal/config/m1.go) | fields of `Auth` | identity, login |
| | `Authz`, `API`, `Repositories` | authz, api, repository |
| [internal/app/m1.go](../../internal/app/m1.go) | fields of `m1Wiring` | audit, api, identity, login, authz, repository, files, endpoints |
| [internal/app/app.go](../../internal/app/app.go) `bootstrap` | step 4 (after the PostgreSQL component) | audit |
| | step 8 (domain snapshots and caches), in this order | repository, identity, login, authz, files, endpoints, api |
| [internal/app/app.go](../../internal/app/app.go) `startClient` | routes outside `/api/v1/` | login (`/auth/`), files (`/files/`) |

After editing a marker in `api/openapi.yaml` run `make generate`; after a config field, `make generate` too
(`schema.json`). A change area needs its `cluster_epoch` row from the migration that ships it
([database.md](database.md)); a key family exists only inside `internal/cluster` (Q-ARCH-16), so an area that
needs one writes the mechanism in `internal/cluster/valkey_<mechanism>.go` and registers the family at its marker.
The audit event files (`internal/audit/events_<area>.go`) are not shared files: each area owns its own.

### Wiring order

`bootstrap` builds in this order and each step reads earlier components from `a.m1`: audit recorder (step 4),
then at step 8 the repository snapshot, the principal store and authenticator, sessions and login, the grant
index (authz needs the repository snapshot and the principal store), the files and endpoint components, and
last the API limiter. Fields are set once before the client listener starts. `app.authenticate` and `app.access`
fall back to the M0 stand-ins while `a.m1.authenticator` or `a.m1.authorizer` is nil; the area that wires the
component deletes the stand-in it replaced. `audit.Freeze()` runs in `freezeRegistries`.

## Error codes

Codes are stable snake_case strings (R-ARCH-025), not numbers; a "range" is the set an area registers. A code
that several pages mention is registered by the first area in the table's order and reused by the others as a
constant. The list is the starting set from the spec pages; an owner adds more inside its markers.

| Area | Codes |
|---|---|
| `api` | `invalid_cursor`, `idempotency_in_progress`, `idempotency_key_reused`, `precondition_required`, `reserved_name`, `invalid_request` (R-API-004 to R-API-022) |
| `audit` | `invalid_audit_range` (R-OPS-017) |
| `identity` | `invalid_token`, `credentials_required`, `invalid_credentials`, `principal_disabled`, `invalid_scope`, `invalid_ttl`, `ttl_exceeds_maximum`, `default_above_maximum`, `exceeds_ceiling`, `non_expiring_not_allowed`, `pat_not_allowed`, `ci_token_restricted`, `token_already_issued`, `managed_by_scim`, `last_admin`, `anonymous_not_allowed`, `auto_join_admin_conflict` |
| `login` | `session_expired`, `reauthentication_required`, `csrf_failed`, `browser_mismatch`, `flow_expired`, `provider_unavailable`, `break_glass_restricted`, `break_glass_immutable`, `break_glass_rotating` |
| `authz` | `authz_stale`, `authz_epoch_unknown`, `role_not_allowed`, `authenticated_write_action`, `action_not_allowed`, `invalid_action`, `invalid_pattern`, `unknown_principal`, `virtual_action_not_applicable` |
| `repository` | `repository_exists`, `repository_disabled`, `repository_referenced`, `immutable_field`, `invalid_key`, `invalid_member`, `invalid_document`, `not_applicable`, `unknown_format`, `unsupported_class`, `batch_too_large`, `not_a_package`, `no_deploy_target` |
| `files` | `invalid_path`, and the codes of R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-016, R-STOR-018 |
| `endpoints` | `endpoint_read_only`, and the codes of R-EP-012 to R-EP-017 (exposure rule validation) |

`unauthenticated`, `forbidden`, `not_found` and the other generic codes already exist; use them.

## Valkey and change-log names (reserved)

| Kind | Name | Area | Where specified |
|---|---|---|---|
| Key family | `rl` v1 (`rl:v1:<limiter>:<b>:<window>`), expiry 2 x window | `api` (the limiter `cluster.NewLimiter`, also used by identity for `auth.credential_failures`, `auth.flow_starts`, `auth.device_code_failures`) | R-ARCH-034, R-AUTH-038 |
| Key family | `tokseen` v1 (`tokseen:v1:<t>`), expiry `auth.rate_limit.recent_token_window` | `identity` | R-ARCH-030, R-AUTH-038 |
| Change area | `auth` (token, session, principal, group and settings changes; the caches `auth_tokens`, `auth_sessions`, `auth_principals`, `auth_settings`) | `identity`, with `login` notifying it when a session ends | R-AUTH-023, R-ARCH-018 |
| Change area | `authz` (policies, role bindings, exposure rules, authorization settings) | `authz`; `endpoints` notifies it for exposure rules | R-AUTHZ-028 |
| Change area | `repo_config` (repository rows, virtual membership) | `repository` | R-REPO-008, R-AUTHZ-028 |

Valkey never holds a grant, a decision or a token (Q-AUTHZ-11, Q-AUTH-9); the names above carry counters and
change messages only. Every feature stays correct with Valkey stopped.

## Audit event naming

Names are `<domain>.<event>` or `<domain>.<object>.<event>`: lower snake case, the last segment in past tense,
at most three segments (R-OPS-014, validated by `audit.Register`). A name is never reused. The spelling of the
authentication events follows R-AUTH-041 (Q-OPS-37 in [operations-and-observability.md](../spec/operations-and-observability.md)).
Each area registers its types in `internal/audit/events_<area>.go` (one file per area, no shared edit).

| Area | Domains (prefixes) | Examples |
|---|---|---|
| `login` | `auth.`, `cli_login.` | `auth.login.succeeded`, `auth.login.failed`, `auth.breakglass.login`, `auth.logout`, `auth.session.ended`, `auth.csrf_failed` |
| `identity` | `token.`, `user.`, `group.`, `serviceaccount.`, `settings.auth.` | `token.created`, `token.revoked` (journal), `token.rejected`, `user.disabled` (journal), `group.members.changed` (journal), `settings.auth.changed` |
| `authz` | `authz.`, `admin_role.` | `authz.policy.created`, `authz.denied` (async), `authz.role.bound` |
| `repository` | `repository.` | `repository.created`, `repository.updated`, `repository.deleted` |
| `files` | `artifact.` | `artifact.deployed`, `artifact.deleted`, `artifact.downloaded` (async) |
| `endpoints` | `endpoint.` | `endpoint.exposure.created`, `endpoint.exposure.updated` and `.deleted` (journal) |
| `api` | `system.` | `system.task_triggered`, `system.config_applied` |
| `audit` | `audit.` | `audit.exported` |

Journal: an event that removes access or updates an access-bearing object is registered with
`Journal: audit.JournalRevocation` and, as a rule, `EntryRequired`; its producer passes `Event.Journal`. Only a
transactional type may carry the flag. `authz.denied` and `token.rejected` are deduplicated per R-OPS-015.

## Management API path prefixes

Paths are those the spec pages name; an owner does not rename them. All are under `/api/v1/` unless noted.

| Area | Prefixes |
|---|---|
| `audit` | `/audit/events`, `/audit/events/{id}`, `/audit/events/export` |
| `api` | `/system/jobs`, `/system/jobs/{id}`, `/system/jobs/{id}/cancel`, `/system/tasks`, `/system/tasks/{name}/run` (the GC trigger is `gc`), `/system/encryption` (key-ring status), `/storage/gc-runs`; conventions for every list; CORS and rate limits as middleware |
| `identity` | `/users`, `/users/{username}`, `/users/me`, `/users/{username}/tokens`, `/users/me/tokens`, `/groups`, `/groups/{name}`, `/groups/{name}/members`, `/service-accounts`, `/service-accounts/{name}`, `/service-accounts/{name}/tokens`, `/tokens`, `/tokens/{id}`, `/tokens/{id}/rotate`, `/tokens/current`, `/tokens/revoke`, `/settings/auth` |
| `login` | `/auth/login-options`, `/auth/whoami`, `/users/me/sessions`, `/users/{username}/sessions`, `/users/{breakGlassName}/unlock`; outside `/api/v1/`: `/auth/oidc/{providerId}/start`, `/auth/oidc/{providerId}/callback`, `/auth/logout`, `/auth/local/login` |
| `authz` | `/permission-policies`, `/roles`, `/role-bindings`, `/authz/me`, `/authz/effective`, `/authz/check`, `/authz/explain`, `/authz/validate-policy` |
| `repository` | `/repositories`, `/repositories/{key}`, `/repositories/batch-create` and the other reserved sub-paths, `/repositories/quick-setup`, `/repositories/{key}/client-setup`, `/formats` |
| `files` | `/files/{repoKey}/{path}` (raw transfer, outside `/api/v1/`), `/repositories/{repoKey}/items`, `/repositories/{repoKey}/children`, `/repositories/{repoKey}/stats` |
| `endpoints` | `/endpoints`, `/endpoints/{endpoint}`, `/endpoints/{endpoint}/exposure-rules`, `/endpoints/{endpoint}/exposure-rules/{name}`, `/endpoints/{endpoint}/check-exposure` |

Not decided by this page and left to the owner, with the spec as the reference: the settings page of
authorization (R-AUTHZ-031), the `/system/*` read-only pages not listed above (operations-and-observability.md), and the
form of the `x-trove-permission` of each operation (R-API-008).

## Decisions taken with the contracts

| Decision | Choice | Why |
|---|---|---|
| Where the principal lives | `httpserver.Principal` is a plain edge value; `auth.Result` and `authz.Subject` are the domain types; two converters at the seam | the layer rules forbid a domain package from importing `httpserver`; one type in a leaf package would have moved `Principal` out of the edge layer |
| How `authz` satisfies `v1.Access` | `Authorizer.Require(ctx, Subject, permission)` and the adapter `v1.NewAccess` | the domain package cannot read the edge's context key |
| Groups on the principal | filled by the stage from the principal cache at request time | R-AUTHZ-002: membership is resolved at evaluation, tokens carry no groups |
| Principal store package | `internal/auth/principal`, added to R-ARCH-003 | the store is below token, session, oidc and authz, which avoids a cycle through package `auth` |
| Event spelling | R-AUTH-041 names (Q-OPS-37) | the owning page defines the events and their journal entries |
| State in `audit` | `internal/audit` joins the packages allowed a frozen registry (`archtest/state.go`, R-ARCH-032) | the event registry is filled at initialisation and frozen like the job registry |
| Empty config sections | `Auth`, `Authz`, `API`, `Repositories` exist as empty structs in `config/m1.go` | owners only add fields in their markers and never edit `Config` |
