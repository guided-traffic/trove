# Authorization

Status: implemented in M1 (WP1.3): the decision engine, the compiled grant index with its invalidation, the policy and
role-binding store, the token-scope ceiling, the epochs of `Trove-Authz-Min-Epoch` and the decision audit, and the
management operations over them (policies, role bindings, the checks of the caller; [below](#management-operations)). M2
adds the populate check and `MemberUsable` ([below](#the-populate-check-and-memberusable)) and package ownership: the check
and the claim in a writer's commit transaction, the owner operations of R-AUTHZ-019 with their HTTP operations, the
deletion hook ([below](#package-ownership)). Not built yet: the callers of the populate check (the remote and virtual
engines) and of the ownership check (the format writers), CI inline grants and the CI project owner, the
shadowing check, projects and delegated management, and the effective-permissions and explain operations of R-AUTHZ-024,
which are P1 (Q-AUTHZ-27).

The specification is [authorization.md](../spec/authorization.md); this page says where each rule lives in the code. The
security view is [permissions.md](../security/permissions.md).

## What has to be in your head first

- **Deny by default, allow-only.** A grant adds rights, nothing removes them except the exclude patterns of the same
  resource set, the token scope and the endpoint ceiling. No action implies another (R-AUTHZ-001).
- **Two entry points decide, nothing else does.** `Decide` (`DecideOn` in a request) for a repository action on a
  coordinate, and `Require` (`RequireOn`) for a capability of a management operation. All are methods of `*authz.Engine`.
- **A request takes its index once.** `authz.Decider` is the per-request contract: `Begin` returns the index, every other
  question has an `…On` form that is answered on it (`DecideOn`, `ReadableOn`, `ExposedOn`, `PathsAllowedOn`, `RequireOn`,
  `HoldsOn`, `CheckOn`, `MayPopulateOn`, `PopulateOn`, `MemberUsableOn`), `Know` advances it when the request learns a
  repository key (Q-AUTHZ-159). The edge holds the index with the subject in `edge.Access`. `Authorizer` (`Decide`,
  `Readable`, `Require`) is the same questions with a fresh index per call, for a job or a command.
- **The subject carries the endpoint.** `Subject.Endpoint` selects the exposure ceiling of every decision, whatever the
  context; only `internal/authz` and `internal/api/edge` build a `Subject` (Q-EP-20).
- **A format's name rule comes from the catalog.** Names and patterns of grants, token scopes and exposure rules are folded
  with the `catalog.Names` of the repository's format; nothing in this package names a format (`Options.Formats`,
  `ServiceOptions.Formats`, `NewExposureCeiling(modes, reg)`; nil is `catalog.Default()`).
- **Decisions read memory only.** The engine evaluates a compiled, immutable `Index` held in an atomic pointer. It never
  queries PostgreSQL or Valkey while deciding (R-AUTHZ-015).
- **PostgreSQL decides, Valkey accelerates.** The index follows the change areas `authz` and `repo_config` through
  `cluster.Changes`; a Valkey message only starts a check earlier. Everything here works with Valkey stopped.
- **The answer is made before the lookup.** A refused read is `DenyHidden` (404), identical to a missing resource, because
  authorization runs before any existence check (R-AUTHZ-016).
- **Populating is a question of its own.** Bringing new content into a remote cache needs `read` plus the repository's
  `cacheFillAction`, never through the session cookie and never on an endpoint with `cache_fill: false`; the subject carries
  that endpoint setting (`Subject.CacheFill`, Q-AUTHZ-153).
- **Ownership is decided where the rows are.** `DecideOn` decides the actions in memory; whether the subject may change a
  package name, and whether its publish claims it, is decided by `Ownership.CheckTx`/`ClaimTx` in the writer's commit
  transaction, on the claim row read `FOR UPDATE` (R-AUTHZ-018, Q-AUTHZ-154).

## Pages and files

| File | Responsibility |
|---|---|
| [authz.go](../../internal/authz/authz.go) | The contract: `Decider`, `Authorizer`, `Prober`, `Subject` (with `Endpoint`, the endpoint's `CacheFill` and the `*scope.Scope` ceiling), `Coordinate`, `Decision`, `Outcome`, `Action`, `Capability`; `RequestKind` is the catalog's |
| [decide.go](../../internal/authz/decide.go) | `Decide`, `Begin`/`Know`/`DecideOn`, `Require`/`RequireOn`, `Holds`/`HoldsOn`, `Readable`/`ReadableOn`, `Check`/`CheckOn`, `Effective`; the evaluation of R-AUTHZ-015; token scope as a ceiling; the pattern cache for scopes |
| [engine.go](../../internal/authz/engine.go) | `Engine`, `Options`: the index lifecycle, the build loop, the watcher, the `cluster.Listener`, the coalesced epoch read |
| [wait.go](../../internal/authz/wait.go) | `forRequest`: confirmation, the stale wait, `Trove-Authz-Min-Epoch` |
| [index.go](../../internal/authz/index.go) | `Index`, `Label`, `Snapshot`, `BuildIndex` (a pure function), the principal set of a request |
| [source.go](../../internal/authz/source.go) | `Source` and its PostgreSQL implementation: the `REPEATABLE READ READ ONLY` build and the epoch read |
| [roles.go](../../internal/authz/roles.go) | The five system roles and the capability table of R-AUTHZ-010 |
| [action.go](../../internal/authz/action.go), [ref.go](../../internal/authz/ref.go) | Action sets and presets; principal references and the IDs of the built-in principals |
| [pattern.go](../../internal/authz/pattern.go), [selector.go](../../internal/authz/selector.go) | The limits, the folding of names and patterns with the format's catalog rule (`fold`, `namesIn`) and the include and exclude semantics of R-AUTHZ-005 on top of `repository.CompilePattern`, the one compiler of the syntax (Q-AUTHZ-25); repository selectors (R-AUTHZ-004) |
| [errors.go](../../internal/authz/errors.go) | `Decision.Err` (the table of R-AUTHZ-016), `NotFound`, `ParseMinEpoch`, the reason codes |
| [ceiling.go](../../internal/authz/ceiling.go), [exposure.go](../../internal/authz/exposure.go), [exposure_service.go](../../internal/authz/exposure_service.go) | `Ceiling` (evaluated on the request's index, for the endpoint of the subject); the endpoint exposure of R-EP-013 compiled into the index (`ExposureCeiling`) and its rule service; see [endpoints.md](endpoints.md#exposure-r-ep-012-to-r-ep-014) |
| [validate.go](../../internal/authz/validate.go), [store.go](../../internal/authz/store.go), [bindings.go](../../internal/authz/bindings.go), [policy.go](../../internal/authz/policy.go) | `Service`: validate, create (`CreateIn` on the caller's transaction), update, delete and read policies; bind and unbind roles |
| [policy_list.go](../../internal/authz/policy_list.go) | The filters of the policy list (`principal`, `expandGroups`, `repository`) and the warnings of a page |
| [settings.go](../../internal/authz/settings.go) | `SettingsHook`, the `repository.Hooks` that records `authz.settings.changed` in the transaction of a repository update (R-AUTHZ-031) |
| [populate.go](../../internal/authz/populate.go) | The populate check of R-AUTHZ-008 (`MayPopulateOn`, `PopulateOn`) and `MemberUsableOn` of R-AUTHZ-015 |
| [ownership.go](../../internal/authz/ownership.go) | Package ownership: `Ownership` with `CheckTx`, `ClaimTx`, `EnforceTx` for writers, `PrecheckWrite` (the same rule read-only, in a transaction of its own, for a writer that decides before it reads a body; Q-ARCH-226), the owner operations `Owners`, `AddOwner`, `RemoveOwner`, `OwnedBy`, and `OwnershipHook` for the repository deletion |
| [ownership.sql](../../internal/meta/postgres/queries/ownership.sql) | The statements of ownership: the locked read of a claim, the claim (`INSERT … ON CONFLICT DO NOTHING`), owners, release, the deletion of a domain, the list by owner |
| [audit.go](../../internal/authz/audit.go), [events_authz.go](../../internal/audit/events_authz.go) | The decision audit, the event registry entries |
| [metrics.go](../../internal/authz/metrics.go) | The `trove_authz_*` families |
| [authz.sql](../../internal/meta/postgres/queries/authz.sql) | Every statement (sqlc); identifiers travel as text, instants as microseconds |
| [wire_authz.go](../../internal/app/wire_authz.go), [access.go](../../internal/app/access.go) | The composition root: the engine at step 8, the header wrapper, the dev-profile bridge |
| [wire_ownership.go](../../internal/app/wire_ownership.go) | The ownership service for the API (through `authzServices`) and the ownership hook in the repository hook list |
| [authorize.go](../../internal/api/v1/authorize.go), [access.go](../../internal/api/v1/access.go), [subject.go](../../internal/api/v1/subject.go) | The declaration forms of `x-trove-permission` and the step that begins the request's index for them; `NewAccess` over the `Decider` and `requestHolder` for a handler that decides; the conversion `Principal` to `Subject` |
| [authz_api.go](../../internal/api/v1/authz_api.go) | The handlers of the permission operations, thin over `Service` and `Engine` |
| [ownership_api.go](../../internal/api/v1/ownership_api.go) | The handlers of the ownership operations, thin over `Ownership` on the request's holder |

## The model

```text
Subject (who, groups, credential scope, MinEpoch)  +  Coordinate (repository, package/path, kind, actions, endpoint)
        │                                                  │
        └────────────── Engine.Decide ─────────────────────┘
                              │ forRequest: confirmed index of at least MinEpoch, else wait / 503 / 412
                              ▼
   anonymous gate ► endpoint ceiling (every action, addressed repository) ► repository lookup ►
        requireAuthentication ► virtual gate ► per action: content:bypass ► grants (principal set × index) ∩ token scope  ──► Decision
```

- **Principal set** (`Index.principalIDs`): a user or break-glass subject is its own ID, its groups and the built-in
  `authenticated`; a service account its own ID and groups, never `authenticated`; a CI subject the IDs of its mapping
  references (looked up by the names the index saw in policy entries) and no `authenticated`; anonymous only the built-in
  `anonymous`. Groups come from `Subject.Groups`, resolved by the authentication stage at request time; tokens carry none.
- **Coordinate.** A coordinate with neither package nor path is repository-level and matches every grant that covers the
  repository; callers must pass the path or the package of an item whenever there is one, otherwise a restricted grant
  would match.
- **Outcomes** map one to one to the rows of R-AUTHZ-016: `Allow`, `DenyUnauthenticated` (401), `DenyHidden` (404),
  `DenyVisible` (403), `DenyStale` (503 `authz_stale`), `DenyEpochUnknown` (412 `authz_epoch_unknown`). `Decision.Err()`
  returns the `*errs.Error`; `Decision.Reason` carries the code of R-AUTHZ-029 (`deny.no-grant`, `deny.scope`,
  `deny.virtual-gate`, `deny.anonymous-partial-virtual`, `deny.exposure`, `deny.endpoint_read_only`, `deny.stale`,
  `deny.capability`, `deny.unauthenticated`, `deny.populate`, `deny.ownership`, `allow.grant`, `allow.role`).
  `Decision.Err()` answers `deny.populate` with 403 `populate_not_permitted` and `deny.ownership` with 403
  `package_owned_by_other_owner`, which names only the coordinate the caller sent.
- **Which refusal is which** (`Engine.refusal`): endpoint-read-only is 403; exposure is 404 for every subject; an anonymous
  subject is always 401 (reads and writes alike); a read-type request (kinds `artifact`, `metadata`, `search`, or a request
  that asks for nothing but `read`) refused on `read` is 404 unless hiding is off for the repository; everything else is 403
  naming the first refused action. A refused write is 403 whether or not the target exists.
- **Hidden equals missing.** `authz.NotFound()` is the one error for both. Code that answers a missing resource on the same
  route builds its answer from it, so the bytes are identical by construction.
- **System roles and capabilities.** Roles come from `authz_role_binding` rows of the subject and its groups; the
  break-glass kind holds `admin` without a row. A session exercises every role of its user. A token exercises all roles
  only with `admin: true` and only if the subject holds `admin` at use time; otherwise its capabilities are the `system`
  items that one of the subject's roles grants, and `content:bypass` is never a system item. `content:bypass` makes
  `Decide` allow every action except where the ceiling refuses.
- **Token scope** (`scopeAdmits`): `inherit` admits everything; otherwise an element must match the repository selectors
  (key, key pattern, class, format filter), the package and path patterns, and list the action. A malformed element admits
  nothing. A scope never grants: `granted` is evaluated first, so `deny.scope` is reported only when a grant exists.
- **Virtual repositories** (R-AUTHZ-009, as far as `Decide` is concerned): `read` on the virtual repository is the gate
  (refusal: `deny.virtual-gate`); anonymous callers pass only if anonymous reads the repository and every member,
  recursively; a request for a content-changing action is decided on the deploy target; grants other than `read` and
  `manage` on a virtual repository are masked away when the index is built. The member checks of resolution are
  `MemberUsableOn` ([below](#the-populate-check-and-memberusable)), which the virtual resolver asks per member.

## The grant index

`BuildIndex(snapshot)` turns the rows of one snapshot into:

- `repos`: the active repositories by key with class, format, `requireAuthentication`, `hideExistence`, virtual members and
  deploy target (the rows of `virtual_members` and `virtual_deploy_targets`, read in the same snapshot as
  `Snapshot.VirtualMembers` and `DeployTargets`; the remaining settings are decoded from the `config` document by
  `parseRepoConfig`; [virtual.md](virtual.md));
- `grants[repositoryID][principalID]`: the compiled grants (actions, package and path matchers, policy name). Selectors are
  expanded against the repository list; `NuGet` repositories compile their package patterns case-folded;
- `bindings[principalID]`: the system roles; `ci[name]`: the principal IDs of CI mappings named by entries.

Nothing in it is a decision or a token. A policy whose selector or pattern does not compile is skipped and logged; skipping
only removes rights.

### Build, label and install

`Source.Snapshot` runs one `REPEATABLE READ READ ONLY` transaction on the request pool: `SET TRANSACTION ISOLATION LEVEL`
is issued before the first query, the statement timeout is `authz.cache.max_staleness`, and the first query reads
`epoch` and `changed_at` of `authz` and `repo_config`. The result is labelled with those four values (`Label`); two labels
are equal only if the instants are equal too, which is how a reused epoch after a failover that lost commits is told apart.

`Engine.install` swaps the index in atomically if its epochs are not lower than the installed index's, or if a flush is
pending (then whatever its epochs). A discarded build is counted (`trove_authz_index_builds_discarded_total`).

### What triggers a build

| Trigger | Where | Notes |
|---|---|---|
| `startup` | `Engine.Start` | A failing first build stops the start: a replica never serves without an index |
| `poll` | `watchLoop` (every 50 ms) | The watcher compares `Changes.AreaState` with the label (memory only). The build is skipped when the index already matches the applied state |
| `message` | `hintLoop` | `ApplyChange` (from the poll, a Valkey message or this replica's own commit) only raises a hint; the hint loop reads the epochs from the database through the coalesced reader and requests a build if they differ from the label, so a forged or duplicated message costs at most one indexed query per 50 ms. The build is skipped only when the index label equals the epochs the hint read, never because the applied state matches: a replica's own commit is applied by the poll later, and a `poll` build would keep the old grants until then (`TestR_AUTHZ_028_OwnWriteIsInForceWithoutWaitingForThePoll`) |
| `flush` | `FlushArea` | The change log found a gap, an unknown payload version or an epoch that went backwards; the next build is installed whatever its epochs |
| `safety` | `watchLoop` | A full rebuild after `authz.cache.safety_ttl`, jittered by 10 percent |
| `min_epoch` | `waitForEpoch` | A request needs an epoch the installed index lacks |

Pending triggers coalesce by strength (`triggerRank`): a flush beats a message, a message beats a poll.

Builds are coalesced (`requestBuild`: one runs, one waits) and a failed build retries with a backoff of at most 2 s.

### Confirmation and staleness

An index is *confirmed* if its own build started less than `authz.cache.max_staleness` ago, or if the last change-log poll
started less than that ago and the poll's applied epochs equal the label (`Engine.confirmed`). A request that finds the
index unconfirmed waits on the installed-channel and a 10 ms ticker until it is, holding no database connection, for at
most the request's own deadline or `database.retry.max_elapsed`, then answers `DenyStale` (503 with `Retry-After`).
Staleness grows when the poll cannot reach PostgreSQL or a needed build fails; it never grows because Valkey is unavailable.
The staleness waits do not themselves trigger a build, by design: a blocked poll must show up as `authz_stale`.

### `Trove-Authz-Min-Epoch`

The composition root reads the header after authentication (`app.withMinEpoch`) into `Principal.MinEpoch`; `edge.SubjectFrom`
copies it to `Subject.MinEpoch`. `forRequest` ignores it for an anonymous subject. If the installed index has that epoch,
nothing happens (`current`). Otherwise `epochReader.read` returns the epochs from a read that *started after the request
arrived*; concurrent callers share one read and reads start at most every 50 ms per replica. An epoch the database has not
reached answers 412 at once (`unknown`); otherwise the request asks for a `min_epoch` build and waits until the installed
index has the epoch (`waited`) or the budget ends (`timeout`, 503).

The writes return their epoch: `afterEpoch` registers a `meta.Tx.Before` hook after the `Changes.Notify` call, so it runs
after the change log bumped the area's row in the same transaction (the row is locked until commit, so the value is the
transaction's own) and reads it for `Trove-Authz-Epoch`; the audit event of the write is recorded in the same hook, so it
carries the epoch.

### Pinning an index to a request

Before it takes the index, `Decide` (and `Begin(ctx, sub, keys...)` for the keys it is given) looks the repository of the
request up (`Engine.know`): a key the index lacks is looked up in the repository snapshot (which asks PostgreSQL and runs the
change-log poll) and, when the repository exists, the index is caught up to the database's epochs (`CatchUp`), so a repository
created through another replica is usable at once (R-API-052). The result of the lookup is not used, which keeps authorization
before existence (R-AUTHZ-016). `Options.Repositories` is the reader; the files service and the repository API do not repeat it.

`Decide` takes the installed index at the moment of the call. A request that decides several coordinates calls `Begin` once
and `DecideOn(idx, …)` per coordinate, so that all of them see one index (R-AUTHZ-015 `index.ForRequest`); a repository key
the request learns afterwards goes through `Engine.Know(ctx, idx, sub, key)`, which returns the index to decide on from then
on (the same one when it holds the key). `edge.Access` ([access.go](../../internal/api/edge/access.go)) keeps the subject, the
index and the decider of a request, travels in the request context (`WithAccess`, `AccessFrom`, `Resolve`) and moves its
index only forward. Losing the holder costs a second index, never a ceiling, because the ceiling is in the subject.

## Policies, role bindings and the checks for the caller

`Service` is the domain API behind the operations of wave 2. Its functions return what a handler needs and add no HTTP:

| Function | Result |
|---|---|
| `Validate(ctx, input)` | `Report{Errors, Warnings, Covers}` for `POST /authz/validate-policy` |
| `Create`, `Update(…, expectedVersion, …)`, `Delete(…, expectedVersion)` | the policy and the epoch of the write; `authz.AnyVersion` is `If-Match: *`; the handler answers 428 for a missing `If-Match` |
| `Get`, `List` | a policy with its warnings; every policy sorted by name (cursor paging is the handler's) |
| `BindRole`, `UnbindRole`, `ListBindings` | `created` (201 or 200), the epoch; 409 `auto_join_admin_conflict` (the group row is locked first, as `UpdateGroup` locks it), 409 `last_admin` from `principal.LastAdminGuard` (active holders, not bindings; Q-AUTHZ-70), 404 |
| `Engine.Check`, `Engine.Effective` | `POST /authz/check` (up to 100 checks, one index) and `GET /authz/me` |

Validation answers 422 with the code of the **first** problem and every problem as a field error with a JSON pointer:

| Code | Rule |
|---|---|
| `invalid_action`, `invalid_pattern`, `unknown_principal`, `unknown_repository`, `unknown_format`, `duplicate_principal` | R-AUTHZ-021, R-AUTHZ-004, R-AUTHZ-005 |
| `anonymous_write_action`, `anonymous_dynamic_selector` | R-AUTHZ-003 |
| `authenticated_write_action` | R-AUTHZ-030 |
| `action_not_allowed` | a CI mapping never holds `manage` (R-AUTHZ-014) |
| `virtual_action_not_applicable` | a key selector of a virtual repository accepts `read` and `manage` only |
| `role_not_allowed`, `unknown_role` | R-AUTHZ-010 |

Warnings (`path_patterns_on_package_format`, `selector_matches_nothing`, `anonymous_populates_remote`) are computed from the
repository list and returned with `Create`, `Update`, `Get` and `Validate`.

Every write does, in one transaction: the row changes, `Changes.Notify("authz", …)` with a payload that names the type and
the ID of the object (never a grant), and the audit event. `authz.policy.updated`, `.deleted` and `authz.role.unbound` carry a
journal entry with the policy ID and name and the state before and after, identified by principal and repository IDs
(R-AUTHZ-031). The index is rebuilt from the tables; there is nothing incremental.

## Declaring the permission of an operation

Every operation in `api/openapi.yaml` carries `x-trove-permission`. `parseRequirement` accepts exactly these forms, and
`LoadDocument` stops the start for anything else:

| Declaration | Enforced as |
|---|---|
| `public: true` | nobody is checked |
| `authenticated: true` [`publicSwitch: …`] | any authenticated caller |
| `permission: <capability>` | `Access.Require(ctx, capability)`; the name must be a capability of R-AUTHZ-010 |
| `role: <role>` | `Access.Require(ctx, "role:<role>")`: the subject holds the role and its credential exercises roles |
| `action: deploy`, `repository: query.repository` [, `package: …`] [, `path: query.path`] | `ActionAccess.RequireAction` with a coordinate built from the request; sources are `query.<name>` and `path.<name>` |

A source the request lacks is the empty string and names no repository, so the request is refused before the handler.
Decisions that depend on the request body are the handler's, using the request's `edge.Access` (or `authz.Authorizer` in
a job). For a capability, a role or an action declaration the step takes the request's index before it checks the declaration
(`Server.begin`, `v1.Beginner`) and stores the holder in the context, so the check and every question of the handler are answered
on one index; a handler of a `public` or `authenticated` operation that decides begins the holder itself (`requestHolder`,
Q-API-158; [http-api.md](http-api.md) "One index per request").

The existing operations of M0 declare `settings:read` (formerly `system.read`).

## The populate check and `MemberUsable`

[populate.go](../../internal/authz/populate.go) answers R-AUTHZ-008 for a coordinate of a remote repository, in this order:

| Step | Refuses with |
|---|---|
| `read` on the coordinate (the ceiling, the grants, the scope) | the read's own refusal: 404, 401 or 503, never 403 for a caller who may not read |
| a session cookie (`Subject.SessionID` or `AuthMethod == "session"`) | `deny.populate` |
| an endpoint with `cache_fill: false` (`Subject.Endpoint != ""` and `!Subject.CacheFill`) | `deny.populate` |
| not a remote repository, or a `cacheFillAction` that names neither `read` nor `deploy` | `deny.populate` |
| the action of `cacheFillAction` (`read` by default, `deploy` for a frozen mirror) | `deny.populate`, `Missing` the action |

`MayPopulateOn` is the question form: nothing is counted or audited. The remote engine asks it where a refusal is served
from the cache (an expired document served stale); `MemberUsableOn` asks it for a remote member. `PopulateOn` is the form
for a request that has nothing to serve without a fetch: the refusal is counted and recorded as `authz.denied` with
`deny.populate` like a refused write (also with `authz.audit.reads: none`), while a refused read stays a read. An allowed
populate is recorded by the fetching request in its fill transaction with `RecordAllowed` and the decision's `Sources` (the
grants that allowed `read` and the populate action).

`MemberUsableOn(ctx, idx, sub, member, c)` returns `(read, fill)`: `read` on the member for the request's coordinate, with
the exposure ceiling kept on the repository the client addressed (`Coordinate.Addressed`, Q-EP-11), and for a remote member
the populate check. Whether the member needs its upstream (nothing cached, an expired document) is the resolver's to know;
nothing is audited, because a member the subject may not use is skipped silently (R-AUTHZ-009 rule 2).

`Subject.CacheFill` is set by `edge.SubjectOf` from the same `httpserver.Endpoint` that names `Subject.Endpoint`; a subject
without an endpoint (a job, a command) has no cache-fill ceiling. The policy warning `anonymous_populates_remote` (M1,
`warningsFor` in [validate.go](../../internal/authz/validate.go)) is unchanged: it flags an `anonymous` read entry on a remote
repository that fills on read, whatever the anonymous switch says.

## Package ownership

The rules are R-AUTHZ-018 and R-AUTHZ-019; the decisions behind the code are Q-AUTHZ-154 to Q-AUTHZ-158.

**Where it applies.** `CheckTx` checks a coordinate whose repository, or a virtual repository's deploy target, is local, has
a format with package names (`catalog.Names`), carries a package name, and whose `packageOwnership` in the index is
`enforce`. The index reads `local.packageOwnership` from the repository document (`repoInfo.ownership`); an unreadable
document is read as `enforce`. The domain is `repo:<repositories.id>`; `ownershipDomain` is refused by the repository
validation until R-AUTHZ-020. The default of a format is `catalog.Facts.OwnershipDefault`, which the repository validation
stores whenever a local repository's document sets none (Q-AUTHZ-155,
[repositories.md](repositories.md#formats)).

**States.** No row is `unclaimed`; `released` counts as unclaimed; `claimed` with owners is owned; `claimed` without owners
(its last owner was a deleted principal, rule 6) needs `manage`.

| The name | A holder of the action may | `manage` may | A publish claims |
|---|---|---|---|
| owned, the subject is an owner (user, group, service account, CI mapping by principal ID) | change it | change it | — |
| owned by others | — (403 `package_owned_by_other_owner`) | change it | — |
| unclaimed or released, no stored version (`FirstVersion`) | change it | change it | yes, for the grants' principals |
| unclaimed or released, with versions | — | change it | yes |
| claimed without owners | — | change it | yes |

`content:bypass` passes every row and claims for the subject itself. Only a publish (`OwnershipWrite.Publish`) claims; the
owners of a claim are the principals of every grant that allowed `deploy` on the coordinate (`factsOf`), so a group grant
makes the group the owner. A publish that would claim with no such principal is refused rather than recording an empty
claim.

**In the writer's transaction.** A writer decides the actions with `DecideOn` first, takes its package row lock, and then
calls `EnforceTx(ctx, tx, idx, sub, c, OwnershipWrite{Publish, FirstVersion})` (or `CheckTx`, then `ClaimTx(ctx, tx, chk)`)
with the coordinate it decided on and the request's index. `CheckTx` reads the claim row `FOR UPDATE` and the owners in the
next statement; a refusal is returned as the error and recorded as `authz.denied` with `deny.ownership`. `ClaimTx` inserts the
claim with `ON CONFLICT DO NOTHING`; when another transaction claimed first, the insert waits for it, returns no row, and
`ClaimTx` reads again and decides again (rule 5), so of two first publishes on two replicas exactly one wins without a retry,
an advisory lock or Valkey. A released name or a claimed one without owners is claimed again with an `UPDATE` of the locked
row. The formats contract wraps `EnforceTx` as `PackageWriter.Claim` (C-14).

**Owner operations.** `Owners`, `AddOwner`, `RemoveOwner` and `OwnedBy` take the request's index and subject and run their own
transaction; `AddOwnerTx` and `RemoveOwnerTx` run in the caller's (the management API runs `AddOwnerTx` in the transaction of
an `Idempotency-Key`). Adding and removing answer 401 for an anonymous caller and 403 `ci_token_restricted` for a CI token before any
lookup; the permission (an owner, `manage`, `content:bypass`; `manage` or `content:bypass` when no owner is left or for
`release`) is decided on the owners read under the row lock, before the owner reference is resolved, and a refusal is 403
naming `manage` whether or not the repository or name exists. `AddOwner` on an unclaimed or released name creates the claim
(`pre-assigned` without versions, `added` with); 409 `already_exists` for an existing owner; 422 `unknown_principal` for an
unknown reference, `anonymous`, `authenticated` and, until M4, a CI project. `RemoveOwner` of the last owner is 409
`last_owner` unless `release`, which sets the state `released`. `OwnedBy` lists the entries naming one principal by key
order with an `OwnedCursor`; a caller without `policies:read` (the identity or a member of the group) sees only names of
repositories it may read. It reads one row more than the page, and the cursor is the key of the last row read for the page,
shown or not, so a page may be short and still have a next one.

**HTTP.** The declarations decide only what they can, because the permission depends on the owners: `getPackageOwners` a
repository-level `read` on `path.repoKey` (the action form passes a path value unnormalized, while the service normalizes the
name with the format's rule before it decides `read` on it), the writes and `listOwnedPackages` an authenticated caller;
both writes need recent authentication through a session (R-API-007). The handlers take the holder with `requestHolder` and
ask `AuthzServices.Owners` on it. The package name and an owner reference with a `/` (`ci:<issuer>/<mapping>`) are one
percent-encoded path segment; the router matches segments escaped, so `@acme%2Ffrontend` arrives as `@acme/frontend`. A 201
carries `Location` of the owners view, on which a GET shows the new owner.

**Deletion.** `OwnershipHook` (`repository.Hooks`) deletes the rows of `repo:<id>` in the transaction that removes the
repository row; the owners follow by foreign key (Q-AUTHZ-156).

**Wiring.** [wire_ownership.go](../../internal/app/wire_ownership.go): `ownershipService` builds `authz.NewOwnership` over the
engine of step 8 and the database (the engine's recorder records the events); `authzServices` hands it to the handlers as
`AuthzServices.Owners`. `ownershipHook` is the last entry of the hook list in `wireRepository`. Until a format writer calls
`EnforceTx`, nothing claims a name.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `authz.hide_existence` | `true` | A refused read answers 404; `false` answers 403 for a repository that exists (R-AUTHZ-017) |
| `authz.cache.max_staleness` | `10s` | No decision on an index unconfirmed for longer; at least 3 × `cluster.epoch_poll_interval` (config validation and `authz.New` refuse a lower value, naming the key) |
| `authz.cache.safety_ttl` | `10m` | Full rebuild at least this often |
| `authz.audit.reads` | `denied` | `none`, `denied` or `all` (allowed reads too) |
| `audit.denial_dedup_window`, `audit.record_anonymous_denials` | see the audit section | read by the decision audit |

## Audit

| Event | Delivery | When |
|---|---|---|
| `authz.denied` | async | a refusal (outcome `denied`): writes and management operations always, reads with `authz.audit.reads` `denied` or `all`, anonymous only with `audit.record_anonymous_denials`; `deny.stale` included |
| `authz.read.allowed` | async | an allowed read with `authz.audit.reads: all` |
| `authz.write.allowed` | transactional | the writer calls `Engine.RecordAllowed(ctx, tx, sub, coordinate, decision)` in the transaction of the change |
| `authz.policy.created`, `.updated`, `.deleted`; `authz.role.bound`, `.unbound` | transactional | the writes of `Service`; updated, deleted and unbound are journaled |
| `authz.settings.changed` | transactional, journaled | `SettingsHook`, called by the repository update when `requireAuthentication`, `hideExistence`, `cacheFillAction` or `packageOwnership` changed; the event and its journal entry name the changed settings, before and after |
| `authz.denied` with `deny.populate` or `deny.ownership` | async | `PopulateOn` and `CheckTx`/`ClaimTx`; recorded like a refused write, whatever `authz.audit.reads` says |
| `authz.ownership.claimed` | transactional | `ClaimTx`: the owners and the claim ID that `package_ownership.claim_event` stores (Q-AUTHZ-158) |
| `authz.ownership.added` | transactional | `AddOwner`: the owner, its principal ID, the reason (`added`, `pre-assigned`) |
| `authz.ownership.removed`, `.released` | transactional, journaled | `RemoveOwner`; the journal entry names domain, format, package and the owner's principal ID (R-AUTHZ-031) |

**The engine does not deduplicate** (Q-AUTHZ-24). It records every refusal with `Repository`, `Path` (the path, else the package
name), `details.required_action` (the actions joined by a comma, or the capability of a management operation) and the actor's
token ID, and the audit writer collapses them by the key of R-OPS-015: the first occurrence at once with `details.count` 1, one
summary per window with the exact number of repeats and `repeat_of`. The client address of an anonymous refusal and the trace ID of
every event come from the request context through `v1.WithRequestSource` (Q-OPS-49). Do not add a second stage: a producer-side
summary arrives inside the writer's window and is counted as one repeat.

## Metrics

`trove_authz_epoch`, `trove_authz_index_rebuild_seconds`, `trove_authz_index_rebuilds_total{trigger}`,
`trove_authz_index_staleness_seconds`, `trove_authz_stale_waits_total{outcome}`, `trove_authz_evaluation_seconds`,
`trove_authz_decisions_total{outcome,reason,action}`, `trove_authz_min_epoch_total{outcome}` and
`trove_authz_index_builds_discarded_total` (R-AUTHZ-028, R-AUTHZ-032).

## Wiring

`App.wireAuthz` (step 8, between the identity and files markers) builds the `Engine` over `authz.NewPostgresSource`, the
replica's `cluster.Changes`, the authz and audit configuration and starts it as a component. It sets `a.m1.authorizer` and
`a.m1.authzEngine`. `a.m1.anonymousAccess` is the runtime switch of R-AUTH-037 that the identity area sets before this
step; nil means anonymous access is off. A replica without PostgreSQL wires nothing, and nobody holds a permission.
`App.access()` returns `bridgeAccess`: the engine behind the dev-profile bridge of `system.bootstrap_anonymous_read`
(Q-AUTHZ-16), which opens the system information and the replica list only; `endpointStage` puts the request path into the
context for it (Q-AUTHZ-71).

`Decider.ExposedOn` (in [ceiling.go](../../internal/authz/ceiling.go); `edge.Access.Exposed`) applies the endpoint's exposure
alone: the repository list and read of a holder of `repositories:read-config` use it through `visibleKeys`, since that caller
needs no read grant but is bound by the ceiling (R-EP-014).

The runtime anonymous switch is `Options.AnonymousAccess(ctx) (bool, error)`; the engine reads it once per request for an
anonymous subject (`withAnonymous`, kept on the subject), with the request's context, and an error is 503 `authz_stale`, not
401. `Engine.Holds` (interface `authz.Prober`; `v1.Holds` for an `Access`) answers "does the caller hold this permission?" without
recording `authz.denied`: the rate limit's administrator exemption and the handlers that pick between two answers use it
(`authz.ContentBypass`, `authz.SettingsRead`, ...); `Require` is for an operation that is refused.

Writes of other areas that change grant-index inputs call hooks of this package in their own transaction
([hooks.go](../../internal/authz/hooks.go)): `PrincipalHook` (`principal.AuthzHook`, given to the manager and the provisioner)
before a principal row is deleted and after a rename, and `PolicyHook` (`repository.Hooks`) before a repository row is removed.
Each notifies the area and raises the version of every policy whose representation changes (a principal in an entry, a key
selector on the repository), so the ETag of the policy changes with it; a principal that nothing names moves nothing (R-AUTHZ-028,
Q-AUTHZ-132). A new write that touches `authz_*` tables or their foreign keys belongs in the completeness test
`TestR_AUTHZ_028_IdentityAndRepositoryWritesAdvanceTheEpoch`.

`wireAuthz` also builds the `authz.Service` when the identity and repository wiring ran (`a.m1.principals`,
`a.m1.repositories`) and sets `a.m1.authzService`; `startClient` hands it and the engine to the handlers as `v1.AuthzServices`.
`*authz.Service` is the `principal.AdminBinder` of `service-account create --admin` (R-AUTH-015, `BindAdmin`): the command that
creates a service account with `--admin` builds its own manager and passes the service. The repository service is built with
`authz.NewSettingsHook`, `NewExposureHook` and `NewPolicyHook` in `wireRepository`. Its `Formats` is the format catalog
(`internal/formats/catalog`), so a policy selector names only a registered format.

## Management operations

The document declares every operation with its `x-trove-permission`; the table is generated from it. All are under `/api/v1`.
Writes answer `Trove-Authz-Epoch` (R-AUTHZ-032); an existing role binding answers the current epoch, and a replay of an
idempotent policy create answers the epoch of the first answer (`Outcome.Finish`, see [http-api.md](http-api.md)).

| operationId | Method and path | Permission | Notes |
|---|---|---|---|
| `listPermissionPolicies` | `GET /permission-policies` | `policies:read` | filters `principal`, `expandGroups` (default true), `repository`; `sort=name\|-name`; cursor paging |
| `createPermissionPolicy` | `POST /permission-policies` | `policies:write` | `Idempotency-Key` optional; `201` with `Location`, `ETag`, epoch; `409 already_exists`; `422` with the code of the first problem |
| `getPermissionPolicy` | `GET /permission-policies/{name}` | `policies:read` | `ETag` is the version; `304` |
| `replacePermissionPolicy` | `PUT /permission-policies/{name}` | `policies:write` | `If-Match` required (`428`), `412` with `currentEtag`, `*` is a blind write |
| `patchPermissionPolicy` | `PATCH /permission-policies/{name}` | `policies:write` | merge patch (Q-AUTHZ-26); computed on one version and written conditional on it |
| `deletePermissionPolicy` | `DELETE /permission-policies/{name}` | `policies:write` | `If-Match` optional; `204` with the epoch |
| `validatePermissionPolicy` | `POST /authz/validate-policy` | `policies:read` | `200` with `valid`, `errors`, `warnings`, `covers` |
| `listRoles` | `GET /roles` | any authenticated caller | the five roles and their capabilities |
| `listRoleBindings` | `GET /role-bindings` | `roles:read` | filters `role`, `principal`; cursor paging |
| `bindRole` | `PUT /role-bindings/{role}/{principalRef}` | `roles:write` | `201` new, `200` existing; `409 auto_join_admin_conflict`; `422 role_not_allowed` |
| `unbindRole` | `DELETE /role-bindings/{role}/{principalRef}` | `roles:write` | `204`; `404`; `409 last_admin` |
| `getAuthzMe` | `GET /authz/me` | any authenticated caller | roles, capabilities, principals, scope summary, epoch |
| `checkPermissions` | `POST /authz/check` | any authenticated caller | up to 100 checks, one index; nothing is audited |
| `getPackageOwners` | `GET /repositories/{repoKey}/packages/{packageName}/owners` | action `read` on the repository; `read` on the package in the handler | `state` `claimed`, `released` or `unclaimed`; 404 for a caller without `read` and where ownership does not exist |
| `addPackageOwner` | `POST /repositories/{repoKey}/packages/{packageName}/owners` | authenticated; an owner, `manage` or `content:bypass` | `Idempotency-Key` optional; `201` with `Location`; pre-assigns an unclaimed name; `403` (also `ci_token_restricted`), `409 already_exists`, `422 unknown_principal`; recent sign-in |
| `removePackageOwner` | `DELETE /repositories/{repoKey}/packages/{packageName}/owners/{owner}` | authenticated; as adding, `release=true` needs `manage` | `204`; `403`; `404`; `409 last_owner`; recent sign-in |
| `listOwnedPackages` | `GET /package-owners` | authenticated; `policies:read` or the caller's own identity or group | `owner` required, `repository`; cursor paging; `422 unknown_principal` |

The handlers ([authz_api.go](../../internal/api/v1/authz_api.go)) do only what the conventions need: strict decoding into
`authz.Policy` (so a body obtained by GET can be sent back), `If-Match` into an expected version, the cursor over a sorted list,
the headers. `Service.List` and `Select` read PostgreSQL on every call (R-API-052); the page assembles the warnings of its own
items. The list is not a keyset query (Q-AUTHZ-28).

## How to

**Add a capability.** Add the constant and the entry of `allCapabilities` in [roles.go](../../internal/authz/roles.go) and
[authz.go](../../internal/authz/authz.go), put it in the columns of the roles in `roleCaps`, add the table row to
R-AUTHZ-010, then use it in `x-trove-permission`. `parseRequirement` refuses a name that is not in the list.

**Protect a new management operation.** Declare `x-trove-permission` in the document; run `make generate`; implement the
handler. The shared step runs the check before the handler.

**Protect a content operation of a format or `/files/`.** Build the `Coordinate` (the canonical path, the normalized package
name, the actions; the endpoint is the subject's), take the subject from `edge.SubjectFrom(r)` (it carries `MinEpoch` and the
endpoint), call `edge.Begin` once and decide each coordinate with the holder (`DecideOn`), and answer `decision.Err()`. For a
missing resource build the answer from `authz.NotFound()`.

**Protect a mutating operation on a package name** (the "Ownership" column of R-AUTHZ-007):

1. Decide the actions with `DecideOn` on the request's index, as above.
2. Open the commit transaction and take the package row lock; read whether the package holds a live version.
3. Call `Ownership.EnforceTx(ctx, tx, idx, sub, c, authz.OwnershipWrite{Publish: <stores a version>, FirstVersion: <held none>})`
   with the same coordinate and index; return its error, which is the 403 to answer.
4. Write the rows. The claim and its audit event commit with them.

**Add an audit event.** Register it in [events_authz.go](../../internal/audit/events_authz.go) with its delivery, flag a
type that removes access or updates an access-bearing object with `Journal: audit.JournalRevocation` and pass
`Event.Journal` from the transaction that makes the change.

**The exposure ceiling.** `Options.Ceiling` is the endpoints area's `ExposureCeiling` (wired in
[wire_exposure.go](../../internal/app/wire_exposure.go)); `evaluate` asks it for every action of a coordinate right after
the anonymous gate and before the repository lookup, on the index the request decides on. The endpoint is
`Subject.Endpoint` (`ceilingRefuses` passes it to `Admits`). The rules are rows of the snapshot
(`Snapshot.Exposure`, query `EndpointsExposureBuild`), so they follow the change area `authz` like a policy.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | [internal/authz](../../internal/authz) | `TestE1_3_AuthorizationCases` (284 cases against the compiled engine), patterns and selectors, the engine's change-log behaviour on fakes, the 422 rules, the audit, `Decision.Err`; names carry the requirement IDs |
| Unit | [internal/api/v1/authorize_test.go](../../internal/api/v1/authorize_test.go), [internal/config/authz_test.go](../../internal/config/authz_test.go) | the declaration forms, the staleness floor |
| Integration | [test/integration/authz](../../test/integration/authz) | E1.3 against PostgreSQL, the policy and role store, the reused epoch, the consistent build, E1.12 with three replicas, Valkey stopped and propagation slowed to 30 s |
| Unit | [populate_test.go](../../internal/authz/populate_test.go), [ownership_test.go](../../internal/authz/ownership_test.go) | `TestR_AUTHZ_008_PopulateCheck` (session cookie, frozen mirror, endpoint cache fill, anonymous), `TestR_AUTHZ_015_MemberUsable` (the table), `TestR_AUTHZ_018_OwnershipRules` (rules 1, 2, 3, 6 on the read state), `TestR_AUTHZ_018_WhenOwnershipIsChecked`, `TestR_AUTHZ_018_ClaimOwners` |
| Integration | [ownership_test.go](../../test/integration/authz/ownership_test.go) | `TestR_AUTHZ_018_TwoReplicasClaimOneNewName` (a held claim on replica a, replica b's insert waiting on it, exactly one wins; a rolled-back claim; 20 pairs started at once), group owner, unclaimed with versions needs `manage`, the owner operations of R-AUTHZ-019 (pre-assignment, 409 `last_owner`, release, concurrent removals, `ci_token_restricted`, 403 for refused writes), the deletion hook through the real deletion job, the populate check and the `anonymous_populates_remote` warning on PostgreSQL |
| Integration, HTTP | [ownership_api_test.go](../../test/integration/authz/ownership_api_test.go) | `TestR_AUTHZ_019_OwnershipOperationsOverHTTP`: the four operations on two replicas through `idapi.Handler` with the replicas' engines, every answer validated against the document (the encoded name, `Location`, 409 and an `Idempotency-Key` replay, 403 whatever exists, `ci_token_restricted`, 422, `last_owner` and release, the list with its cursor) |
| Integration, HTTP | [api_test.go](../../test/integration/authz/api_test.go) on the stack of [test/integration/stack](../../test/integration/stack) (its handler is `idapi.Handler` of [test/integration/idapi](../../test/integration/idapi) with the header stand-in for authentication and the replica's own engine, so the identity, login, authorization and audit tests drive one chain) | the policy lifecycle with `ETag` and `If-Match`, merge patch, idempotent create with the epoch replayed, the 422 codes, `validate-policy`, the filters of the list, `apitest.RunContract` on `/permission-policies` over three replicas, roles and bindings with `last_admin` and a limit=1 walk, `authz/check` and `authz/me` including the `authenticated` cases of E1.3, E1.12 through HTTP (twenty parallel requests with `Trove-Authz-Min-Epoch` on two replicas, a grant and its removal, Valkey stopped), and a validator that checks every answer of every operation against the document |

Run the integration area with the shared stack:
`set -a; . .dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/authz/...`.

Not covered by tests: Valkey-assisted propagation of the authz area (the change log's own tests cover the fast path), a
PostgreSQL failover under load, and the pause-a-build hook of R-AUTHZ-028's acceptance list (the monotonic install rule is
unit-tested on the fakes).
