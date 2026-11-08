# Permissions

Status: implemented in M1 for the decision engine, the compiled grant index and its propagation, the token scope as a
ceiling, system roles, the epochs of `Trove-Authz-Min-Epoch` and the decision audit. M2 adds the populate check and package
ownership in the decision point, and their callers: npm's writers ask the ownership question for a publish, a provenance
bundle, a dist-tag write and an unpublish, the generic layer asks it for every delete of npm content through `/files/` and the
items API (Q-AUTHZ-161), the remote engine asks the populate check before a fill, the virtual engine asks `MemberUsableOn` per
member and the shadowing check before a first version, and the host decides a format's write again when it commits
(Q-ARCH-225). The role bindings of the system configuration (`authz.role_bindings`, applied by `trove migrate`) and the
auditor's metadata read (`content:metadata-read`) are built. Projects, their members and roles are on [delegated-administration.md](delegated-administration.md);
[what the engine does with them](#projects-and-project-policies) is below.

This page says what keeps a request from doing more than it was granted, how long a removed grant can still work, and what
an attacker who controls one of the inputs gains. The mechanics are in
[developer/authorization.md](../developer/authorization.md); the rules are in [spec/authorization.md](../spec/authorization.md).

## Where a decision is made

```text
 client ──► edge ──► authentication stage ──► handler ──► authz.Engine ──► compiled grant index (replica memory)
                      (credential → Principal)            Decide / Require        ▲
                                                                                  │ rebuilt from PostgreSQL
                                                         cluster.Changes ─────────┘ (epochs; Valkey only hints)
```

- The only code that says allow or deny is `authz.Engine` ([decide.go](../../internal/authz/decide.go)); the management API
  asks it through the shared step of [authorize.go](../../internal/api/v1/authorize.go), so no operation reaches its handler
  without the check its declaration names, and an operation without a declaration stops the start.
- Handlers receive the subject the authentication stage built. A token carries no group list and no administrator flag:
  groups are resolved at request time and roles come from the role-binding table (R-AUTHZ-002, R-AUTHZ-010).

## What narrows and what widens

| Mechanism | Effect | Where |
|---|---|---|
| Deny by default | A request with no grant, no role and no `content:bypass` is refused; a fresh installation has no policy | `evaluate` |
| Allow-only grants | A grant only adds; an exclude pattern removes only inside its own resource set | `BuildIndex`, `globSet.match` |
| No implication | `deploy` does not imply `read`, `manage` does not imply `deploy`; presets are expanded when written | [action.go](../../internal/authz/action.go) |
| Token scope | Intersected with the owner's rights at use time; never grants; a malformed element admits nothing. An element that admits `read` on a virtual repository also admits `read` on the members reached through it, for that request only (Q-AUTHZ-163, below) | `scopeAdmits`, `scopeAdmitsThrough` |
| Roles through tokens | A token exercises roles only with `admin: true` (and only if the owner holds `admin` at that moment) or through `system` items the owner's roles grant; `content:bypass` is never an item | `capabilities` |
| Metadata read | `content:metadata-read` allows `read` on a coordinate of kind metadata or search of every repository and nothing else: no artifact, no write, no coordinate without a kind; the repository part of a token scope narrows it like a grant (Q-AUTHZ-172, [below](#the-auditor)) | `allowedAction`, `metadataKind` |
| `authenticated` and `anonymous` | Entries hold `read` only; `anonymous` entries select repositories by key only; neither holds a role; a service account and a CI token are never `authenticated` | `Service.entryRules`, `Index.principalIDs` |
| Endpoint ceiling | Consulted first for every action and every subject, `content:bypass` included; nil is none | [ceiling.go](../../internal/authz/ceiling.go) |
| Virtual repositories | `read` on the virtual repository is a gate, not a source of content rights; anonymous passes only if it reads every member | `evaluate` |
| Populate check | Bringing new content into a remote cache needs `read` plus the repository's `cacheFillAction`; the session cookie never populates, so a link or an image in another site cannot make an administrator's browser fill a frozen mirror; an endpoint with `cache_fill: false` refuses every subject, the setting travelling in the subject so a collapsed or fanned-out decision keeps it | [populate.go](../../internal/authz/populate.go) |
| Package ownership | On an enforcing repository a name is changed by its owners, `manage` holders and `content:bypass` only, however wide a `deploy` grant's patterns are; an imported name with versions and no owner needs `manage`; the claim of a first publish is decided by the primary key in the commit transaction, so two replicas cannot both claim. A delete through `/files/` or the items API is a change of every package whose files it removes, asked under each package's row lock in the transaction that removes them or queues their job; one refusal refuses the whole delete (`TestR_AUTHZ_018_RawRemovalsAskOwnership`, `TestR_STOR_018_ARawRemovalAboveTheSyncLimitAsksOwnershipBeforeItsJob`) | [ownership.go](../../internal/authz/ownership.go), [hooks.go](../../internal/formathost/hooks.go) (`FilesSeam.CheckRemoval`) |
| Raw paths of package formats | `/files/` and the items API decide a path of an npm repository on the package and the version the format gives it, so a grant's name patterns, a token scope's and an exposure rule's apply to the tarball as to the package; a path the format places in no package does not match a name set with any pattern (Q-AUTHZ-160, `TestR_AUTHZ_005_FilesDecideNpmItemsOnTheirPackage`) | [files.go](../../internal/files/files.go) (`locate`), [paths.go](../../internal/authz/paths.go), [pattern.go](../../internal/authz/pattern.go) |

Principals are referenced by their immutable ID in `authz_policy_entry` and `authz_role_binding`, so a deleted and
re-created user, group or service account of the same name inherits nothing, and a repository deleted and re-created under
the same key is not covered by the old key selector (the policy reports `inert`).

## What a refusal reveals

- A refused read is `DenyHidden`. `Decision.Err()` returns `authz.NotFound()`, the one error that missing resources use too,
  and authorization runs before any lookup, so neither the body nor the time depends on existence.
- A refused write is 403 naming the action and the coordinate the caller sent, whether or not the target exists.
- An anonymous caller is always 401 (reads, writes, a partially readable virtual repository), so the status does not depend
  on what exists.
- A management operation is checked before any lookup (`Require`), so its 403 does not depend on the object.
- A refused populate is 403 `populate_not_permitted` only after `read` was allowed; a caller who may not read gets the read's
  404. A refused ownership check is 403 `package_owned_by_other_owner` naming only the coordinate the caller sent, never an
  owner.
- The owner operations decide like the rest: adding or removing an owner is 403 for a caller who may not, whether or not the
  repository or the name exists, and decided before the owner reference is resolved, so a refused caller cannot probe for
  principals; reading the owners is a read (404). A CI token never changes owners (403 `ci_token_restricted`, before any
  lookup): a pipeline runs its dependencies' install scripts, which must not be able to hand a package to another project.
- The package screens' operations (R-API-029) count and list only what the caller may read, and the filter is part of the statement
  that counts: the decision point classifies each repository for the caller (`authz.ReadScoper`, [visible.go](../../internal/authz/visible.go))
  and the statements of [package_view.sql](../../internal/meta/postgres/queries/package_view.sql) take the keys of the repositories read
  whole and the readable `repository, package, version` rows as data. No aggregate (`versionCount`, `downloadCount`, `createdAt`,
  `updatedAt`, `repositories`, `latestVersion`) and no `ETag` is computed from a version the caller may not read; a dist-tag in the details
  names a version only if the caller may read it. A package, a version or a README the caller may not read, a package `id` that is
  none and one that names nothing all answer the same `404` (`TestR_API_029_ListCountsOnlyWhatTheCallerMayRead`). The classification is
  checked against `DecideOn` for every subject, endpoint, repository, package and version of a fixture
  (`TestR_API_029_ReadScopeAgreesWithTheDecisionPoint`). On an endpoint with exposure rules a repository is never classified as read whole.
  A listing that belongs to a request through a virtual repository (NuGet's merged search) names the addressed repository, and every row
  is decided as a read through it: the exposure of the virtual repository and the lift of a token scope that names it apply, and a
  repository it does not reach reads as nothing (Q-AUTHZ-168, `TestQ_AUTHZ_168_AReadScopeThroughAVirtualRepositoryAgreesWithTheDecisionPoint`).
- The repositories of one project (`GET /api/v1/projects/{projectKey}/repositories`) answer an empty page only to a caller that sees
  the project: a holder of `projects:read` or `repositories:read-config`, a member (within its token's scope), or a caller that reads
  one of its repositories whatever the filters. To anyone else a project that exists and one that does not both answer 404 with the
  same detail, so the list is no oracle for project keys (`projectSeen` in
  [repositories_api.go](../../internal/api/v1/repositories_api.go), `hideProject` for the refusals of the other project routes;
  `TestR_AUTHZ_026_AMemberSeesItsOwnProjectsOnly`).
- `authz.hide_existence: false` (R-AUTHZ-017) turns the refusal of a read of an existing repository into 403. It is a
  troubleshooting switch; the endpoint-exposure 404 and the anonymous 401 do not change.

## How long a removed grant keeps working

The index is rebuilt from a `REPEATABLE READ READ ONLY` snapshot labelled with the epochs of `authz` and `repo_config` and
their `changed_at` values, so no build combines a policy's entries from one commit with its selectors from another, and an
epoch number reused after a PostgreSQL failover that lost commits is told apart by `changed_at` and flushed.

Every write that changes an input of the index moves the epoch of `authz` in its own transaction, also the writes of other areas:
deleting or renaming a user, group or service account that a policy or role binding names, and deleting a repository that a policy
selects by key, each call a hook of the authorization area (`authz.PrincipalHook`, `authz.PolicyHook`) that notifies the area and
raises the version of the policies whose representation changed. Without it a deleted principal would keep its grants on the
other replicas until the safety rebuild. A replica's own write starts a build of its own (trigger `message`) that is not skipped because
the poll has not applied the write yet, so the writing replica enforces it at once. The before-state in the revocation journal is
read under a row lock (`FOR UPDATE`), so two blind writes journal one after the other.

| Situation | Bound |
|---|---|
| Replica whose change-log poll works | the next poll (`cluster.epoch_poll_interval`) plus one build; with Valkey typically about 100 ms plus one build |
| Valkey stopped, messages lost or forged | the same bound; a message never advances the applied epoch and never confirms an index |
| Replica cut off from PostgreSQL, or whose build fails | never longer than `authz.cache.max_staleness` (10 s) plus one build: after that the replica stops deciding, waits like a request waits for the database, and answers 503 `authz_stale` |
| Client that sent `Trove-Authz-Min-Epoch` with the epoch of its change | the change is in force on the replica that answers, or the request waits or fails (503, 412) |

A holder of a valid credential can keep using a removed right for that long and no longer. Revoking the credential ends it
on the timeline of the identity area, independently of the index.

`Trove-Authz-Min-Epoch` can only make a replica wait or refuse; it cannot widen a permission. A malformed value is 400, an
anonymous request ignores the header, so the epoch cannot be probed without a credential, and the replica reads the
database epoch at most every 50 ms however many requests carry an unknown epoch.

## A token scope on a virtual repository

A virtual repository is read through its members: the gate is `read` on the virtual repository, and the content is decided on the
member that holds it (R-AUTHZ-009). A token scope is checked at both steps, so a scope that named only the virtual repository, which
is what **Create a token for this setup** creates, used to pass the gate and be refused by every member (Q-AUTHZ-163).

`scopeAdmitsThrough` in [decide.go](../../internal/authz/decide.go) lifts that refusal under four conditions, all required:

1. the action is `read`;
2. the coordinate carries the virtual repository the client addressed (`Coordinate.Addressed`), which the virtual tiers set for every member request (`formats.Request.Addressed()`, set only by `Request.Member`, so the populate check of a remote member and the revalidation of a stale member document included, Q-AUTHZ-165);
   A read that stands for a write (`GET {name}?write=true` on a virtual repository, R-NPM-029) is derived with `Request.WriteTarget` and carries no addressed repository, so the lift never applies to it: the scope must name the deployment target (Q-ARCH-252).
3. that repository reaches the member, directly or through nested virtual repositories (`Index.reaches`);
4. a scope element that lists the key of the virtual repository exactly admits `read` on it for the same package and path, so the element's `packages` and `paths` still narrow. A class selector (`@any-virtual`) or a key glob that covers the repository does not lift (Q-AUTHZ-164).

What does not change: the grants of the owner are evaluated before the scope and decide alone whether anything is allowed, so the lift
removes a refusal by the scope and grants nothing; a token never reads more through the virtual repository than its owner reads
through it. A member addressed directly (`/api/npm/<projectKey>/<member>/…`), a repository that is no member, search and listing across
repositories (decided on the repository itself), writes (decided on the deploy target, which a scope names itself) and every other action
are decided by the scope as before. Tests: `TestR_AUTHZ_013_AScopeOnAVirtualRepositoryCoversItsMembersWhenReadThroughIt` (the
cases above one by one, a token of a user without the right included) and
`TestR_AUTHZ_013_ASetupTokenOfAVirtualRepositoryReadsThroughIt` (over HTTP through a virtual npm repository, with the direct read of the
member refused).

## What Valkey can do to authorization

Nothing is written to Valkey or read from it by `internal/authz`. The change log delivers Valkey messages to the engine's
listener as hints; the engine reads the epochs from PostgreSQL before it builds. Whoever can write under trove's Valkey prefix
can make a replica run extra epoch reads (at most 20 per second per replica) and can delay a change by dropping messages
until the poll applies it. They cannot add a grant, suppress an audit event or make a replica decide on older grants than the
bound above.

## The dev-profile bridge

`system.bootstrap_anonymous_read: true` lets an anonymous caller hold `settings:read` for two operations, the system
information and the replica list, on the default endpoint, while that endpoint admits anonymous requests
([access.go](../../internal/app/access.go), `bridgeAccess`, `bootstrapPath`; the endpoint stage hands it the request path).
The other operations of `settings:read` (the scheduled tasks, the encryption status, the authentication settings) stay
closed (Q-AUTHZ-71; `TestBootstrapAnonymousReadOnlyOnDefaultEndpoint`). It opens no repository action, it is refused with an
`https` base URL other than localhost, and every other permission goes to the engine. The exposure it causes is the one
recorded as
[H-63](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-while-the-bootstrap-switch-is-on).

## Audit

Refusals are recorded as `authz.denied` with the outcome `denied`: asynchronous, sent by the engine without any collapsing and
deduplicated by the audit writer per replica, principal, required action, repository and path, and for an anonymous caller per
client address (R-OPS-015, Q-AUTHZ-24); the address and the trace ID are added to the event by the recorder wrapper of the
composition root from the request context. Policy and role changes are recorded in the transaction of the change, so no change
exists without its audit record, and the events that remove access (`authz.policy.updated`, `authz.policy.deleted`,
`authz.role.unbound`, and `authz.settings.changed` when a repository update tightens or loosens an authorization setting) carry
the journal entry that a restore replays (R-AUTHZ-031). Token values, hashes and claims are never part of an event.

Ownership changes are recorded in the transaction of the change: `authz.ownership.claimed` with the owners and the claim ID
the claim row stores, `.added`, and `.removed` and `.released` with the journal entry (domain, format, name, the owner's
principal ID). A refused populate that is answered 403 and a refused ownership check are recorded as `authz.denied`
(`deny.populate`, `deny.ownership`) like refused writes, also with `authz.audit.reads: none`; a populate refusal that is
answered from the cache is not a refusal and records nothing.

## The policy and role operations

Reading policies and bindings needs `policies:read` and `roles:read`, writing them `policies:write` and `roles:write` (the
auditor holds the reads, the administrator all four). Every write answers `Trove-Authz-Epoch`, and an authenticated caller may name
that epoch in `Trove-Authz-Min-Epoch` on its next request to see the change on whichever replica answers (R-AUTHZ-032;
`TestR_AUTHZ_032_MinEpochOverHTTP` shows it with Valkey stopped and a 30 s poll). `POST /api/v1/authz/check` and `GET
/api/v1/authz/me` tell a caller about its own credential only; a repository the caller cannot see answers `allowed: false`, the
same as one that does not exist. A role is bound to users, groups and service accounts only, and a group that takes members
automatically takes no role: binding a role and turning on auto-join both lock the group's row before each checks the other,
so the two cannot pass at the same moment (`TestR_AUTH_019_ABindingWaitsForAutoJoinOfTheGroup`,
`TestR_AUTH_019_AutoJoinWaitsForARoleBindingOfTheGroup`).

Who holds a role is changed only by a caller that may bind roles (R-AUTHZ-011, Q-AUTHZ-69). A change of a group that carries
any role (its members from either side, its name, description or auto-join flag, its deletion) and of a user or service
account that holds one directly or through a group (profile, identity links, disable, enable, delete) asks the decision point
for `roles:write` in the transaction of the change ([guards.go](../../internal/auth/principal/guards.go), `guardRoleHolder`;
`Caller.RoleHolders` set by the API in [auth_services.go](../../internal/api/v1/auth_services.go)). Without it,
`identities:write` would reach every capability: a security administrator joining an administrator group, a token scoped to
`identities:write` adding its owner, a pending identity link on an administrator claimed at the next login, a rename that lets
the identity provider's sync fill the group (`TestR_AUTHZ_011_IdentitiesWriteDoesNotReachTheRoles`). Ending sessions and
revoking tokens stay with `identities:write` and `tokens:revoke`; they only take access away.

`last_admin` (R-AUTHZ-022, Q-AUTHZ-70) counts the active users and service accounts that hold `admin`, not the bindings, and
it guards every change that can end the role: unbinding it, disabling or deleting a holder, deleting its group, removing it
from the group ([guards.go](../../internal/auth/principal/guards.go), `LastAdminGuard`). Each such change locks the `admin`
binding rows first and counts in a statement of its own afterwards, so two of them on different replicas run one after the
other (`TestR_AUTHZ_022_ConcurrentUnbindsLeaveAnAdministrator`, `TestR_AUTHZ_022_LastAdministratorAcrossTheIdentityOperations`).
The guard steps aside while the break-glass administrator is enabled.

## Role bindings from the configuration

`authz.role_bindings` (R-AUTHZ-010, Q-AUTHZ-170, Q-AUTHZ-171) lets the system configuration decide who holds a system role.
Only `trove migrate` writes these bindings: the chart's migration Job, with the migration credential (the schema owner in
two-role mode), under the migration lock and in one transaction that also writes the authz change and the audit events with
the actor `system:configuration` ([declared.go](../../internal/authz/declared.go), `Service.ApplyDeclared`;
[migrate_declared.go](../../internal/app/migrate_declared.go)). Every running replica applies the result from the change log
within the bound of R-AUTHZ-028, Valkey or not. No `web` or `worker` replica writes a binding with the source
`configuration`; the API answers `DELETE` of one with 409 `role_binding_managed_by_configuration` and `PUT` of one with 200,
leaving it to the configuration. The API's delete removes only rows with the source `api`, in the statement itself, so a
concurrent take-over by `trove migrate` cannot be lost (`AuthzDeleteAPIBinding`).

Who decides, and what makes it real:

- Whoever can change the chart values and run the upgrade (the GitOps repository and whoever merges to it) decides the role
  holders, as they decide the rest of the configuration and its Secrets.
- For a declared group, whoever controls the group's membership at the identity provider administers trove: with a GitHub
  team behind Dex, the team's maintainers and the organization's owners. trove sees the membership only at each member's
  sign-in (R-AUTH-008, H-212) and cannot tell who changed it at the provider.
- The group-sync rule that maps a claim value to the declared group's name is part of the same trust. A rule that a claim
  value anyone can obtain matches makes anyone a role holder; R-AUTH-007 refuses a `match_regex` without a namespace for a
  public issuer, and [H-85](tokens-and-credentials.md#h-85--group-names-from-the-login-flow-are-applied-as-given) applies.
- A declared group that does not exist is created as a local group without members, so it holds nobody until the sync or an
  administrator adds one.

The rules of the API bind the declaration (Q-AUTHZ-171): a declared auto-join group stops the run
(`auto_join_admin_conflict`), and so does a removal that would leave no active user or service account holding `admin` while
the break-glass administrator is disabled (`last_admin`), with an error that names the binding. A declaration the binary
cannot accept (an unknown role, two principals, `anonymous` or `authenticated`) fails at configuration load in every process
that reads the file. Tests: `TestR_AUTHZ_010_BindingsFromTheConfiguration`, `TestR_AUTHZ_022_DeclaredRemovalKeepsAnAdministrator`,
`TestR_AUTHZ_010_MigrateCommandAppliesTheDeclaredBindings`, `TestR_AUTHZ_010_RoleBindingsFromTheConfiguration`.

## The auditor

The role `auditor` (R-AUTHZ-011, Q-AUTHZ-172) reads the configuration, the identities, the policies and bindings, the token
metadata, the settings and the audit log, and through `content:metadata-read` the metadata of every repository. The decision
point allows that capability `read` on the request kinds metadata and search only ([decide.go](../../internal/authz/decide.go),
`allowedAction`): the repository list, package lists and pages, versions, the format's documents (an npm package document, a
NuGet version list, registration and search) and the folder listings of raw repositories. An artifact (a tarball, a `.nupkg`,
a `.nuspec`, a raw file) is refused like a missing one, every write with 403, and a coordinate without a request kind is no
metadata read. The gate of a virtual repository is decided with the request's kind, so an artifact through a virtual
repository stops at the gate. Exposure rules bind the auditor as every subject (R-EP-013). A token carries the capability only
when its scope lists the system item `content:metadata-read`, and its repository part narrows it like a grant (R-AUTHZ-013).
Tests: `TestQ_AUTHZ_172_AuditorReadsMetadataNeverArtifacts`, `TestQ_AUTHZ_172_AuditorReadsNpmMetadata`,
`TestQ_AUTHZ_172_AuditorReadsNuGetMetadata`.

What the auditor learns is everything the metadata says: every repository key, every package name and version, the dist-tags
and maintainers of an npm package, the dependencies of a NuGet version, the names, sizes and digests of raw files, internal
names included. It knows the digest of an artifact it cannot fetch. When the role is bound to a declared IdP group, whoever
controls that group decides who sees all of it.

## The ownership operations

Who may change the owners of a name is decided on the owners the transaction reads under the claim row's lock, so a refused
caller cannot change them in a race, and two removals of the last two owners cannot both pass (`last_owner`,
`TestR_AUTHZ_019_OwnerOperations`). The declarations admit an authenticated caller (or a repository-level `read` for the
owners view) and the handler decides the rest; an anonymous caller is 401 and a CI token 403 `ci_token_restricted` before
anything is looked up. Adding and removing owners need recent authentication through a session (R-API-007), because an added
owner may publish the name. Being an owner is a fact about the principal, so the owner path does not by itself respect a token's
scope: an owner that presents a token whose scope does not inherit its rights counts as an owner only where an element of the scope
lists `manage` on the repository and the name (`scopeReachesName` in [ownership.go](../../internal/authz/ownership.go), Q-AUTHZ-240);
a publish token, a token for another repository or for one system capability gets the 403 of any refused change and cannot hand
the owner's names to another principal. A session and an inheriting token keep the owner path
(`TestR_AUTHZ_019_ANarrowedTokenChangesOwnersOnlyWhereItsScopeListsManage`). `GET /api/v1/package-owners` shows a caller without `policies:read` only names of repositories it
may read. The package name and an owner reference are single path segments with an encoded `/`; the router matches escaped
segments, so `@acme%2F..%2Fx` is one name and never a path (`TestR_AUTHZ_019_OwnershipOperationsOverHTTP`).

## Projects and project policies

Every repository belongs to a project (R-AUTHZ-026). A member's role and a project's policies reach the project's repositories and
nothing else; what keeps that from widening is a property of the grant index and of the write, not of the caller's good behaviour.

| Mechanism | Effect | Where |
|---|---|---|
| Roles | A member's role (the highest over the user and its groups) grants its actions on every repository of its project, as one more source of the union, `role:<projectKey>:<role>`; the token's scope bounds it like a grant, and a token is a member only of the projects its scope reaches (`scopeReachesProject`, [delegated-administration.md](delegated-administration.md#what-a-role-grants-and-through-which-credential)). A service account, a CI identity, a Bastion registration and anonymous carry none, also as a member of a member group; a row of another kind and an auto-join group are skipped by the build ([delegated-administration.md](delegated-administration.md#what-a-role-grants-and-through-which-credential)) | `grantedBy`, `roleOf`, `TestR_AUTHZ_026_EachRoleGrantsItsActionsOnTheProjectsRepositories` |
| Selector contexts | A global policy, a token scope and an exposure rule name `<project>/<key>`, a pattern over project and key, or a class; a bare key there is refused, never read as a selector that matches nothing. A project policy names bare keys of its project only ([Q-AUTHZ-216](../spec/authorization.md#q-authz-216--repository-selectors-name-the-project-in-instance-contexts-and-a-bare-key-inside-a-project)) | `parseSelector`, `ValidateSelectorIn` |
| Scoped compile | A project policy covers only repositories of its project, whatever its stored selectors say. The match cache is keyed by project, so a global pattern and the same text in a project policy cannot share a result | `builder.covered`, `TestR_AUTHZ_026_MatchCacheKeyIncludesTheProject` |
| Write rules | A project policy is stored as `<project>/<name>`, grants to no `anonymous` or `authenticated`, to no CI mapping `manage`, to no service account of another project (to an Owner without a policies capability that answers `unknown_principal`, as for a name that does not exist), to no auto-join group; a global policy names no project service account; the project of a policy never changes; an Owner's update and delete compare the policy's project again under the policy lock | `Service.resolve`, `Service.projectRule`, `Service.Update`, `ownsPolicy` ([Q-AUTHZ-197](../spec/authorization.md#q-authz-197--what-a-project-administrators-policy-list-and-validation-tell-about-other-objects), [Q-AUTHZ-199](../spec/authorization.md#q-authz-199--a-delegates-policy-write-is-checked-under-the-policy-lock-and-its-project-lookups-run-in-the-writes-transaction), [Q-AUTHZ-200](../spec/authorization.md#q-authz-200--a-global-policy-never-takes-a-projects-prefix-and-a-project-policy-names-no-auto-join-group)) |
| Second entry table | The grants of a project policy are in `authz_project_policy_entry`; the database refuses a row of `authz_policy_entry` for a project policy (`AI004`) | `writeChildren`, `TestR_AUTHZ_026_ProjectPolicyGrantsHaveTheirOwnTable` |
| Roles and owners | A project service account takes no system role (`role_not_allowed`, also in `authz.role_bindings`) and owns no package outside its project; the build skips an entry of a global policy or of another project's policy and a role binding that names one ([Q-AUTHZ-198](../spec/authorization.md#q-authz-198--a-project-service-account-outside-its-project-is-refused-in-every-path-and-skipped-by-the-build)) | `Service.bindable`, `refuseForeignAccount` |

Removing a member, lowering a role or moving a repository out of a project takes effect on every replica within the bound of
R-AUTHZ-028 (`TestR_AUTHZ_028_AMemberChangeReachesAnotherReplica`, `TestR_REPO_060_AMoveTakesTheRepositoryOutOfItsProjectsRoles`).

## A Bastion registration holds nothing

A Bastion registration ([R-BAS-004](../spec/bastion.md#r-bas-004--bastion-registrations-and-the-bastion-token-on-the-main-installation)) is a
principal of the kind `bastion`, and no decision of this page is ever made about it: its token is refused by type on every route but the
Bastion interface, and the interface admits the recognition stage's proof, not a grant (`{bastion: true}`). It is never part of
`authenticated`, because no request of it reaches the decision point. The rule is held three times:

| Line | What refuses | Answer |
|---|---|---|
| The writes | a policy entry and its validation, a role binding, an owner record, a group member and a project member that names `bastion:<name>` ([Q-BAS-60](../spec/bastion.md#q-bas-60--a-bastion-registration-is-refused-by-every-write-path-with-one-top-level-422-principal_not_allowed)) | `422 principal_not_allowed`; `TestR_BAS_004_ABastionHoldsNoGrant` and the unit tests next to each path |
| The database | the guards of `authz_role_binding` and `group_membership` name the kinds they admit (`AI001`) | the row is refused; the same test writes around the API |
| The decision point | the kind `bastion` has an empty principal set: no `authenticated`, no own ID, no role, no capability ([decide.go](../../internal/authz/decide.go), [index.go](../../internal/authz/index.go)) | every decision refuses it, should a route ever ask; `TestR_BAS_004_ABastionRegistrationIsNeverPartOfAuthenticated` |

Managing registrations needs `endpoints:read` and `endpoints:write`, and revoking one Bastion token `tokens:revoke` as well
([Q-BAS-24](../spec/bastion.md#q-bas-24--bastion-registrations-are-managed-with-the-endpoint-capabilities)): `security-admin` holds all three,
`auditor` and `repository-admin` `endpoints:read` only. A token cut down to the system item `tokens:revoke` revokes a Bastion token and
nothing else (`TestR_BAS_004_CapabilitiesGateTheOperations`).

## Database footprint

The engine's statements read `authz_policy*`, `authz_project_policy_entry`, `authz_role_binding`, `project`, `project_admin`,
`repositories`, `principal` and `cluster_epoch`; the policy service also reads `service_account` for the project of an account
and writes the five `authz_*` tables and `authz_project_policy_entry` and, through `cluster.Changes`, `cluster_epoch` and `cluster_change`. Ownership reads
and writes `package_ownership` and `package_owner` and reads `packages` and `package_versions` (whether an unclaimed name holds
a version); it locks the claim row of a name `FOR UPDATE` in the writer's transaction and takes no advisory lock. The build runs
as a read-only transaction on the request pool, never on the control connection, so a slow build cannot delay the poll or the
heartbeats. See [privilege-footprint.md](privilege-footprint.md) for the grants of the runtime role.

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-21](../spec/authorization.md#h-21--no-reservation-of-name-prefixes): no reservation of name prefixes.
- [H-22](../spec/authorization.md#h-22--no-two-person-rule-for-permission-changes): no two-person rule for permission changes.
- [H-45](../spec/api-and-ui.md#h-45--hiding-unauthorized-resources-is-not-timing-safe): hiding unauthorized resources is not timing-safe.

### H-102 · A caller that decides several coordinates without `Begin` can straddle an index swap

`Decide` takes the installed index at the moment of the call. A request that decides several coordinates (the members of a
virtual repository, a copy) must call `Engine.Begin` once and `DecideOn` per coordinate to decide on one index; a caller that
calls `Decide` repeatedly can see two indexes if a build installs in between. Dormant: the callers that decide several coordinates
in one request do so on one begun index. The package dispatcher resolves one holder per request
([edge/access.go](../../internal/api/edge/access.go), `Resolve`, `Begin`) and the format host decides the members on that index
(`rq.Index`, [formathost/host.go](../../internal/formathost/host.go)) and re-checks a request's writes on one index begun before the
commit ([formathost/state.go](../../internal/formathost/state.go), `Decider.Begin`); the management API decides on the holder of its
shared step. Nothing enforces the rule for a new caller: it becomes live with code that calls `Decide` more than once per request.

### H-104 · The commit-time re-check decides on the grants, not on the credential

R-AUTHZ-015's re-check runs for every deploy of `/files/` and every write of a format: after the body has arrived and before
the commit transaction opens, the write is decided again on the committing replica's index, for a new file and an identical
re-upload as for an overwrite, and the recorded decision is that one ([deploy.go](../../internal/files/deploy.go), on a new holder begun after the body arrived, Q-ARCH-208;
`TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits` removes the grant while the body is arriving). Two things remain.

The credential is not authenticated again: a token revoked, or a session ended, while its upload runs still commits that
upload, because the subject the re-check decides for is the one the request started with. Live for every upload; what makes
it real is a stolen token whose holder starts a large upload before the revocation. The revocation stops every later request
within the bound of R-AUTH-023.

The re-check and the commit are two steps: a grant removed in the milliseconds between them does not stop the write. The
same holds for a format's write: the host decides every write the request was allowed again on the index of the moment before
the transaction opens, and the ownership check in it decides on that index (Q-ARCH-225,
`TestR_AUTHZ_015_APublishIsDecidedAgainWhenItCommits` removes the grant of an npm publish while its body is arriving); a grant
removed between that decision and the commit does not stop it, and the credential is not authenticated again either.

### H-396 · A project administrator's policy write is decided on the replica's index

The declaration admits an Owner of the project on the index of the request, which
may be up to `authz.cache.max_staleness` behind (R-AUTHZ-028), and the policy write does not decide again on the state its own
transaction reads. An Owner that was removed or demoted, or a group it belonged to that lost the role, can therefore still create,
replace or delete a project policy, and change members, for as long as the replica it reaches holds the older index: typically
100 ms plus one build, at most `cluster.epoch_poll_interval` plus 1 s on a replica whose change-log poll works, never longer than
`authz.cache.max_staleness`. Live for every project write; what makes it real is a member whose role is being removed and who acts
in that window. The write itself stays inside the project (the rules of the service read the database, not the index), and the
last-Owner rule counts the rows under the project's lock, so the window widens who may write, not what a project policy can grant.

### H-212 · The identity provider's group sync can remove the last administrator

The `last_admin` guard runs in the management operations only. The group sync of a login (R-AUTH-008) removes a membership
that the identity provider no longer reports, also the last membership in the group that carries `admin`, and the sync is
not refused (Q-AUTHZ-70, option c). Live whenever an administrator group is synchronised from the identity provider and the
break-glass administrator is disabled; what makes it real is a change of the group at the identity provider, by mistake or by
someone who administers the provider but not trove. R-HA-054 reports an installation without an administrator; recovery is
the break-glass administrator or `trove admin`.

### H-381 · Deleting a declared principal removes its configuration binding until the next `trove migrate`

Component: role bindings from the configuration (R-AUTHZ-010, Q-AUTHZ-171). Subject: a holder of `identities:write` and
`roles:write` (an administrator). Live today. Deleting a principal deletes its role bindings through the foreign key
(R-AUTHZ-002), the configuration's included, although the API refuses to remove such a binding directly. Until the next run of
`trove migrate` the binding is gone; that run creates a declared group again as a new, empty group (its synchronized members
return at their next sign-in) and stops for a declared user or service account. What makes it real: an administrator who
deletes the declared group or user, by mistake or to take the role away outside the GitOps review. The deletion is audited
(`group.deleted`, `authz.role.unbound` is not written for the cascade), and the last_admin guard still applies to it.

### H-299 · The cost of a package list for a caller with row-level grants depends on versions the caller cannot read

Live today for a caller whose read of a repository is narrowed by a package pattern, a path pattern, a token scope with elements or an
endpoint with exposure rules (`authz.ReadRows`), on every operation of R-API-029. Before the statements run, the service lists the
versions of those repositories (at most 200 000) and asks the decision point about each, so the time of an answer grows with the
versions in the repository, hidden ones included, and the answer is `400` above the bound whatever the caller may read of it. What
it reveals is an order of magnitude of the size of a repository the caller can read in part; what makes it real is a caller who can
measure the time of its own requests and who has such a grant. A caller with an unrestricted read of a repository (`ReadAll`) pays
nothing for the versions of other repositories. Closing it needs the pattern set compiled into the statement; Q-API-165 names the
condition for doing so.

### H-326 · A token scoped to a virtual repository by its key reaches the members the repository has when it is used

A token with a scope element that lists the key of a virtual repository reads whatever members the repository has at the time of the request, as far as
its owner reads them (Q-AUTHZ-163, Q-AUTHZ-164). Live today for every such token, including those the client setup page creates. What makes it real:

- An administrator holding `repositories:write-config` adds a member to the virtual repository after the token was issued: the token then reads that member's content through the virtual repository, within the owner's rights and no further. Nothing records the members a token was created with and the token list does not show the reach.
- Tokens that already named a virtual repository by key when this rule was introduced gained the members' reach once, at deploy, without a re-issue and without an audit event.
- The lift also lets the populate check fill the cache from the upstream on the token's behalf, for remote members with `cacheFillAction` `read`: such a token causes upstream requests and cache entries that it could not cause before.
- The key of the member that served a request reaches the token through the `Trove-Resolved-From` header and the resolution explanation ([artifact.go](../../internal/virtual/artifact.go), [engine.go](../../internal/virtual/engine.go), [explain.go](../../internal/virtual/explain.go)), although the token's creator may not hold `repositories:read-config`.

All of this stays inside the owner's own rights; the bound is the owner, the lifetime of the token (30 days by default on the setup page), and the administrators who edit the members. A scope by class or glob never lifts.

### H-327 · The walk over nested virtual repositories has no visited set

`Index.reaches` and `anonReadsAllMembers` ([decide.go](../../internal/authz/decide.go)) walk members recursively with a depth bound of 16 and no visited set. Dormant today: a virtual repository cannot contain another one ([members.go](../../internal/repository/members.go) refuses it). Before nested virtual repositories (R-REPO-018) arrive, the walk needs a visited set or a precomputed member closure, and `Addressed` (`formats.Request.Addressed()`) must stay the outermost repository, because the scope lift names the repository the client addressed.

### H-335 · A NuGet search by a caller with row-level grants scans up to 200,000 matches per request

Live today for a caller whose read of a NuGet repository is narrowed row by row (`authz.ReadRows`: a package pattern in a grant, a token
scope with elements, or an endpoint with exposure rules), and for every caller of a repository with include or exclude patterns (Q-NUGET-77),
who calls `GET …/v3/search` (R-NUGET-010, Q-NUGET-45). SQL cannot ask the decision point which versions the caller may read, so `totalHits`
stays exact by scanning the candidates in rank order and asking `MayRead` per listed version; the scan stops with `400` above 200,000
packages and never returns a partial count. The scan is one statement in one transaction that tests the terms on every listed visible
version of the repository, so that a term meets only the versions the caller may read (`NuGetSearchScan`, Q-NUGET-74), ranks the
repository once and hands its rows to the format in batches of 500 as they arrive (`Store.SearchScan`); the database work of one request
is one pass over the versions of the repository and a sort of its candidates, and the transaction holds a connection for the whole scan.
The CPU on the replica is proportional to the candidates (not to the page): a query that matches a large repository, repeated, can hold a replica and the database busy, and each request
pays the scan again because nothing caches it. A caller with an unrestricted read (`ReadAll`) of a repository without patterns is counted and
paged by SQL and pays no scan. What bounds it today is the 1,024-byte and 16-term limit of `q` and the 200,000 cap; nothing caches or
rate-limits the scan itself. Closing it needs the readable package names handed to the statement (the condition is in Q-NUGET-45 and
Q-NUGET-74); H-299 is the same cost for the package list. Through a virtual repository the scans of the members, a remote member's
cache-only search included, share the one budget of
200,000 packages, and at most four members are asked at once ([search_virtual.go](../../internal/formats/nuget/search_virtual.go), Q-NUGET-76),
so a request holds at most four database connections and looks at no more packages than one repository may, whatever the number of members.
