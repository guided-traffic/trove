# Repository model, paths and replica state

Status: the model, the path rules and the snapshot of milestone M1 are implemented, and the virtual repositories and their routing
of M2. The authorization of a repository request (who may read or deploy) is the perspective of [permissions.md](permissions.md);
this page covers what the repository model itself decides and what a replica believes about it.

This page describes three mechanisms of [internal/repository](../../internal/repository/) that have a security
relation: the canonical path every repository request passes before authorization, the include and exclude patterns
that act as routing rules, and the per-replica snapshot that tells a request path whether a repository exists, is
disabled or wants authentication.

## Canonical paths

Every repository-relative path passes `repository.CleanPath` ([path.go](../../internal/repository/path.go)) before any
authorization runs (R-ARCH-005). The function decodes the percent-escapes exactly once and only then looks at the
segments, so an escaped traversal (`%2e%2e`, `..%2f`, a doubly escaped dot that stays literal) cannot reach a
comparison as anything but what it is after the one decode. It refuses, with `400 invalid_path`: a malformed escape,
bytes that are not UTF-8, an empty, `.` or `..` segment, NUL, a backslash, any control character (C0, DEL and C1), a
path above 4096 bytes and a segment above 255. A path is a file or a folder, never both, which the items store enforces
([storage.md](../spec/storage.md) R-STOR-007). The path that authorization, the include and exclude patterns, the
item lookup and the object key derivation see is therefore one string, and none of them re-decodes it. The object key
of a blob is derived from the content digest only ([blob-store.md](../developer/blob-store.md)); no path reaches it.

## Patterns as routing rules

`includePatterns` and `excludePatterns` of a repository ([pattern.go](../../internal/repository/pattern.go)) apply to
every caller, administrators included (R-REPO-005): a deploy outside the patterns answers `403 not_admitted`, a read
or delete of a path outside them answers `404` as if it did not exist. The matcher is compiled when the snapshot is
built and cannot fail at request time; a stored list that does not compile marks the repository disabled (below).
Patterns are not permissions: a path that the patterns admit is still subject to the grants of the caller.

The patterns of a format with package names are name patterns, and `/files/` and the items API match them against the package
the format gives a raw path, not against the path: `@x/*` hides `@x/y/-/y-1.0.0.tgz` as it hides `@x/y` on the npm protocol
([files/formats.go](../../internal/files/formats.go), `admits`; Q-AUTHZ-160; `TestR_REPO_005_NameExcludeOfAnNpmRepositoryUnderFiles`).
The items API is the one road that deletes outside the patterns, and in a disabled repository, and only for a holder of
`content:bypass`, the administrator's cleanup of items stored before a pattern change; every other caller deletes there as
through `/files/` (Q-AUTHZ-162).

A listing applies the patterns as a read does. NuGet's search neither lists nor counts an ID its repository's patterns keep out,
and through a virtual repository a member contributes only the IDs that its own patterns and the virtual repository's admit; a
search has no name, so the routing of the plan applies none and the search decides each ID in its scan
([handlers_search.go](../../internal/formats/nuget/handlers_search.go) `patternsOf`, Q-NUGET-77;
`TestR_REPO_005_TheNuGetSearchOfALocalRepositoryAppliesItsPatterns`).

## The snapshot and the configuration document

The snapshot ([reader.go](../../internal/repository/reader.go)) holds, per repository, the settings that request paths
enforce: `disabled`, `requireAuthentication`, `hideExistence`, the compiled patterns and the lifecycle. It is an
in-process copy of PostgreSQL rows, rebuilt in full when a change of the area `repo_config` reaches the replica
(Valkey message at once, the change-log poll within `cluster.epoch_poll_interval` otherwise) and at the latest after
`cache.snapshot_safety_ttl`. Valkey grants nothing and carries no copy of it (R-REPO-049).

- **Fail closed on a bad row.** A row whose stored document does not decode, or whose patterns do not compile, is loaded
  disabled and logged. It cannot be written by the service, which validates first; it could appear through a manual
  edit or a restore of a damaged dump.
- **Strict documents.** A configuration document with an unknown or duplicate member is refused (400
  `invalid_document`), so a misspelt `requireAuthentication` can never be dropped silently and leave the operator
  believing a repository demands authentication.
- **Every change is audited with the change.** The audit event is inserted in the transaction of the write
  (R-OPS-015), so no repository exists, changes or disappears without its record. Until the audit recorder is wired
  into the composition root, the service is built with a recorder that discards events; that is a wiring state, not a
  property of the design.
- **Lookup of unknown keys.** A key that cannot be a key never reaches the database; any other unknown key costs one
  indexed query per distinct key and entry lifetime (`cache.unknown_repo_keys`, 60 s), so a flood of requests for
  invented keys cannot turn into a query flood (R-ARCH-011).
- **Deletion.** From the first transaction of a deletion the repository is `deleting`: it is not resolvable by clients,
  its key stays taken, and the items go in batches before the row (R-REPO-013). A deleted key can be created again and
  is empty.

## Virtual repositories: members, claims and pin rules

The routing of virtual repositories ([virtual.md](../developer/virtual.md)) decides which member a name resolves from, and
therefore which source a client's `npm install` trusts. It has four properties that matter for dependency confusion
([R-REPO-023](../spec/repositories-and-proxy.md)):

- **It does not look at the caller.** `Routing.Route` takes the virtual repository and the name and nothing else. A caller
  who cannot read the internal member is routed exactly as one who can, so it receives a 404 for a claimed name, never the
  public package of the same name. Per-member permissions are applied after the routing, by the engine. That the claim exists
  is observable by comparison (H-7).
- **A global claim is a separate privilege.** A `global` claim is in effect in every virtual repository of the format and, with
  `proxy.claims.remote_refusal`, at every remote repository. Writing one needs `repositories:write-config`; `manage` on the
  repository is enough for `members` claims. The claim set is one resource replaced under `If-Match` on `claims_version`
  (428 and 412), and a request that narrows or drops a global claim needs the permission as well, so a manager cannot reopen a
  name by omission (Q-REPO-105). The check runs inside the write transaction, after the revision compare, and a refusal changes
  nothing ([claims.go](../../internal/repository/claims.go) `PutClaims`, [claims_api.go](../../internal/api/v1/claims_api.go)).
- **An automatic claim cannot take a name.** The exact claim of a first publish is skipped when another repository's claim is in
  effect in a virtual repository that contains the publisher, and the deploy records `autoClaim: skipped`; taking a name over is
  a manual claim and needs `manage`. The decision reads committed claims in the publish transaction, and a concurrent first
  publish inserts one claim ([routing.go](../../internal/repository/routing.go) `AutoClaim`).
- **Claims and pin rules fail toward protection.** A claim whose repository is disabled or being deleted still decides: the name
  resolves from nothing (404), not from the remote members. The claims of a deleted repository stay in effect until its row goes.
  A member that cannot be linked into the snapshot is left out, never added. A repository that a virtual repository lists cannot
  be deleted without `detach`, and a detach leaves a pin rule that lost its last member as a rule that blocks its names
  (Q-REPO-107).

Every claim and pin-rule change is audited in its transaction (`repository.claims_changed`, `repository.updated`) and reaches the other
replicas through the change areas `claims` and `repo_config`, within `cluster.epoch_poll_interval` with or without Valkey. The members
and the deploy target the grant index uses for the virtual gate (R-AUTHZ-009) are the same rows ([permissions.md](permissions.md)).

- **A restore keeps the protection a claim or a pin rule gave a name.** A restore to a point before the first publish of a name
  loses the package and its auto claim together, and the name would reach a remote member again: the case dependency confusion
  uses, because the clients know the name. `repository.claims_changed` (every claims change, the auto claim included) and
  `repository.narrowed` (a pin rule added, a member out of a rule or the member list, `autoClaim` switched on, patterns and a
  stronger `vulnerabilities` member) are in the revocation journal, and every restore re-applies them
  ([R-REPO-061](../spec/repositories-and-proxy.md#r-repo-061--narrowings-of-a-repository-in-the-revocation-journal),
  [journal_replay.go](../../internal/repository/journal_replay.go)). Replay only narrows, and the journal can be written by
  whoever holds the runtime S3 credential, so each part that could widen what the restored repository serves disables the
  repository instead and reports the entry `suspended`:
  - a member removal whose member holds a claim of scope `members` while any other member stays, because the claim would leave
    effect in that virtual repository and its names would reach the member that stays, a remote one or a local one that does not
    hold them (Q-REPO-248);
  - a pin rule that a claim in effect or another rule overlaps, because a rule is the one way a claimed name reaches a remote
    member (Q-REPO-249);
  - an include list that shares no pattern with the restored one, because an empty list admits everything (Q-REPO-247);
  - a manual claim that overlaps a claim in effect together with it, because it could take names from the other repository; the
    claim is created and its repository disabled, and a claim of a disabled repository stays in effect (Q-REPO-250).

  An entry never adds a member, a pattern to an include list or a member to a pin rule, and an auto claim is not created where
  another repository's claim covers the name. The settings that are not journaled are listed under
  [H-569](backup-and-restore.md#h-569--upstream-storage-and-format-settings-of-a-repository-come-back-with-a-restore).

## Virtual resolution: what the engine decides per caller

The routing above is the same for everybody; the engine ([virtual.md](../developer/virtual.md)) adds what depends on the caller
([R-AUTHZ-009](../spec/authorization.md), [R-REPO-019](../spec/repositories-and-proxy.md), [R-REPO-020](../spec/repositories-and-proxy.md)):

- **A member the caller cannot read is never consulted.** `Resolver.Plan` asks `MemberUsableOn` for each member the route kept and
  leaves the others out, so a package that exists only in such a member answers 404, byte for byte as a package that does not exist
  (tested for artifacts and documents, `TestR_AUTHZ_009_PerMemberDecisions`).
- **A merged document is shared only between callers owed the same bytes.** The key of the merged-metadata cache holds the readable
  members, whether the caller may fill each remote member and the endpoint (Q-REPO-110), so a caller who may not fill a mirror, or who
  is on an endpoint with `cache_fill: false`, never receives the entry of one who may. The entry validates itself against the
  `rev_seq` of the local members on every hit and lives in the replica's memory only.
- **A caller who may not fill a mirror is offered only what it can download.** The engine marks the part `Restricted`; honouring it is the
  format's merge, which npm's does (H-283 names the formats to come).
- **The explanation names no member the caller cannot read.** Such members are a count, the claimants of `decidedBy` are filtered by `read`, and
  the keys appear only for a holder of `repositories:read-config`. The claim or pin pattern itself is shown (H-7 names the same disclosure).
- **The shadowing check fails closed.** An upstream that cannot be asked, a stored-state read that fails and a replica without the remote engine
  refuse a `deploy` holder; a `manage` holder proceeds on an undetermined answer and the commit records it. The probe stores nothing and no
  negative entry is read or written, so a forged entry in Valkey cannot admit a write. Each remote repository is probed once per publish
  (Q-REPO-115).

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-7](../spec/repositories-and-proxy.md#h-7--claimed-names-are-observable): claimed names are observable.
- [H-9](../spec/repositories-and-proxy.md#h-9--member-scoped-claims-do-not-protect-other-virtual-repositories): member-scoped claims do not protect other virtual repositories.
- [H-10](../spec/repositories-and-proxy.md#h-10--deploy-on-a-local-member-shadows-public-packages-for-every-consumer): deploy on a local member shadows public packages for every consumer.
- [H-404](../spec/repositories-and-proxy.md#h-404--the-repository-key-column-carries-the-databases-collation): the repository key column carries the database's collation.

### H-303 · A format setting is stored and returned as given, so a format's schema must not hold a secret

Component: the member named after the format in the repository document (`npm`; R-REPO-004, Q-ARCH-229) and the settings form rendered
from the format's schema ([web-ui.md](../developer/web-ui.md#the-repository-settings-form-rendered-from-the-formats-schema), Q-UI-78).
Subject: every holder of `repositories:read-config` and every Owner of the repository's project, who read the member in
`GET /api/v1/projects/{projectKey}/repositories/{repoKey}` and see it on the edit
form, in its API tab and in the conflict dialog (the audit event names the changed member, not its value). The upstream credential of a remote repository is
sealed and answered as `{"set": true}` ([data-and-secret-flow.md](data-and-secret-flow.md)); the format member has no such
treatment: what a format declares in its schema is stored in `repositories.config` and returned as it was written. Dormant: npm's
settings are switches and strategies. What makes it real: a format that adds a setting carrying a credential, for example an advisory
provider URL with user information (`https://user:token@host/`) or a token for an outside service. Hardening, in this order: a format
setting never carries a credential (a review item for every change of a `settings.schema.json`); `catalog.Register` refuses a schema
member marked `writeOnly` or `format: password` until the member has a sealed, write-only form like the upstream credential; a URL setting
refuses user information.

### H-283 · Honouring `Restricted` is up to the format's merge

Component: `VirtualAdapter.Merge` of a format ([virtual.md](../developer/virtual.md) "The contract of a format's merge"). Subject: the versions
a caller who may not fill a remote member is offered. The engine decides the populate right per member, keeps merges of different rights apart
(Q-REPO-110) and marks the part `Restricted`, but only the format can drop the versions of its own document. npm does: a part the caller may not fill supplies only the versions whose tarball is cached ([merge.go](../../internal/formats/npm/merge/merge.go),
`member.eligible`; `TestR_AUTHZ_008_AMergeListsOnlyTheCachedVersionsOfAMemberTheCallerMayNotFill`,
`TestR_AUTHZ_008_ACallerWhoMayNotFillSeesOnlyTheCachedVersionsOfARemoteMember`), and so does NuGet: a restricted part contributes only
its cached versions ([nuget/merge/merge.go](../../internal/formats/nuget/merge/merge.go), `Do`;
`TestR_AUTHZ_008_ARestrictedMemberListsOnlyWhatItHasCached`, `TestR_NUGET_017_AMemberTheCallerMayNotFillListsItsCachedVersionsOnly`,
`TestR_AUTHZ_008_ARestrictedRemoteMemberContributesTheCachedVersionsOfTheStoredPages`). What stays open is the next format: check 5 of the
conformance kit ([formattest/checks.go](../../internal/formats/formattest/checks.go), `merge`) does not cover `Restricted`, so a merge
that ignores the flag passes the kit, lists versions of an upstream that the caller
cannot download (a read-only caller of a frozen mirror then sees 404 for a version the document announced) and, for a caller on an endpoint
with `cache_fill: false`, reveals what the mirror's upstream publishes. Nothing is stolen and nothing is stored, but R-EP-018 ("every version
a filtered document lists is downloadable") is broken. Dormant for npm and NuGet; live with the next format that gets a virtual class, unless
its tests pin the two callers of Q-REPO-113 in both orders as npm's and NuGet's do.

### H-329 · A format that reads a member without `rq.Member` skips the per-member read check

Component: the read stores and the package writer of the formats host, scoped by `formats.RepositoryAllowed` ([scope.go](../../internal/formats/scope.go),
Q-ARCH-260). Subject: the members of a virtual repository a caller may not read. The scope of a request is its repository and every member below it, so a store
refuses a repository outside that set (a format cannot name another tenant's repository by id: `TestQ_ARCH_260_TheStoresRefuseARepositoryOutsideTheRequest`) and a
package writer is bound to the request's or the event's repository. The scope does not remember which members the format obtained through `rq.Member`, because
the engines hand members to a format themselves (`ServeCheck`, `LocalDocument`, `Merge`) and those calls would fail. A search that iterates `rq.Repo.Members()`
and reads each through the stores therefore reads members the caller has no grant on: the stores answer, and R-AUTHZ-009 rule 2 (a caller lists only what they may
read) is skipped. Dormant: npm's one loop over `rq.Repo.Members()` (the audit forwarder, [handlers_audit.go](../../internal/formats/npm/handlers_audit.go)) derives each member with `rq.Member` and authorizes it before it reads; NuGet's merged search (R-NUGET-018) fans out over the members of `Resolver.Plan`,
which leaves out every member the caller cannot read, and `TestR_NUGET_018_SearchMergesTheLocalMembers` ("a caller sees what the members it may read hold")
pins a caller without a grant on one member, so the gap stays dormant. The same search reads `rq.Repo.Members()` for one thing, the include and exclude
patterns of the members of the plan (`memberPatterns` in [search_virtual.go](../../internal/formats/nuget/search_virtual.go), Q-NUGET-77), and reads no
store through that list. What makes it real: a handler that iterates `rq.Repo.Members()` and reads the stores of what it finds.

### H-274 · A stored claim that does not parse protects nothing

Component: the claim index ([claims.go](../../internal/repository/claims.go) `buildClaimIndex`). Subject: the names of a claim row whose
pattern is outside the two admitted forms. Live with milestone M2. The service validates every pattern before it writes, and the
column only bounds the length, so such a row can come from a manual edit of the database or from a restore of a damaged dump. The
index skips it and logs `a stored claim does not parse and protects nothing` at error level; the names it was meant to cover resolve
as unclaimed (tier order, then the remote members) until the row is fixed. The pattern of a repository document that does not compile,
by contrast, disables the repository. What makes it real: a restore from a dump that was edited by hand. The remedy is to
read the log line after a restore and to replace the claim through the API.

### H-132 · A replica enforces the previous repository settings until its snapshot is rebuilt

Component: the repository snapshot (R-REPO-049). Subject: `disabled`, `requireAuthentication`, `hideExistence` and the
include and exclude patterns of a repository whose settings were just changed. Live with milestone M1. The replica that
handled the write applies the change at once; every other replica applies it when the change-log poll or the Valkey
message reaches it, at most `cluster.epoch_poll_interval` plus one query and the rebuild without Valkey (2 s with the
defaults, [architecture.md](../spec/architecture.md) R-ARCH-018). Within that window another replica still serves a
repository that was just disabled, answers an anonymous read on a repository that was just set to require
authentication, or admits a path that a pattern now excludes. The grant index of the authorization area has the same
bound for grants. What makes it real: a change made to stop an exposure that is under way, while a client keeps
requesting through a replica that has not polled yet.

### H-133 · Paths are compared byte for byte, without Unicode normalisation

Component: `CleanPath` and the items store. Subject: lookalike paths. Live with milestone M1. `Report.pdf`, `report.pdf`
and two spellings of `é` (one precomposed code point, one letter plus a combining accent) are four different paths, and a
generic repository stores all of them. A person reading a listing, or a pattern written for one spelling, can mistake one
for another. The rule is deliberate: trove never rewrites what a client sent, and an include or exclude pattern
therefore matches exactly the bytes it is written with. What makes it real: a repository whose uploaders are not
trusted and whose consumers pick files by reading names; the remedy is to grant `deploy` only to accounts that
publish.
