# Virtual repositories: members, deploy target, pin rules, claims and routing

The caller-independent part of virtual repositories ([R-REPO-017](../spec/repositories-and-proxy.md) to R-REPO-023,
R-REPO-048): which repositories a virtual repository lists, which of them take part in a request, which local
repository owns a name, and the API that edits the claims. Code: [internal/repository](../../internal/repository/)
(the files in the table below), the statements in [queries/virtual.sql](../../internal/meta/postgres/queries/virtual.sql)
and [queries/claims.sql](../../internal/meta/postgres/queries/claims.sql), the handlers in
[claims_api.go](../../internal/api/v1/claims_api.go). The resolution itself (per-caller steps, tiers, the merged
metadata, the shadowing check, the explanation API) is the virtual engine of [R-REPO-019](../spec/repositories-and-proxy.md)
and R-REPO-020, in [internal/virtual](../../internal/virtual/); its sections start at [The virtual engine](#the-virtual-engine).

What exists today: the model and its validation, the routing and the claim index, the auto claim of a first publish, the
claims API, the detach of R-REPO-013, the grant index reading the members from their rows, and the engine: the plan, the
tiers of an artifact, the merged-metadata cache, the shadowing check, the deploy target and the explanation API. What does not
exist yet: nested virtual repositories (R-REPO-018, a member that is virtual answers `invalid_member`), `effectiveMembers`,
every format that has package names (so no repository of a real format can hold a claim or be resolved until npm lands), and
the binding of the proxy engine to the engine's `Remotes` seam (below): until it is bound, a remote member is a member that
cannot be asked. The tests use a format registered in a test catalog and the fakes of
[virtualtest](../../internal/virtual/virtualtest/).

## Files

| File | Responsibility |
|---|---|
| [virtual.go](../../internal/repository/virtual.go) | `VirtualSettings`, `Pinning`, `PinRule`; the rows of a virtual repository (`virtualRel`) and how they are read (`loadVirtualRels`), rendered into the document and linked into the snapshot (`linkVirtual`); `Repository.Virtual`, `Members`, `DeployTarget` |
| [members.go](../../internal/repository/members.go) | the checks that need PostgreSQL (`resolveVirtual`: member exists, same format, not being deleted, not virtual; deploy target local; the warning `dependency_confusion_unprotected`), the replace of the rows (`writeVirtual`), the detach of a deleted repository (`detachReferences`) |
| [validate_virtual.go](../../internal/repository/validate_virtual.go) | the rules a document decides alone (`validateVirtual`: duplicates, the deploy target among the members, pin-rule shape) and the warning `pattern_unscoped_only` (`warnUnscopedOnly`) |
| [pins.go](../../internal/repository/pins.go) | the prefix trie of claim and pin-rule patterns (`patTrie`) and the pin index of one virtual repository (`pinIndex.best`) |
| [claims.go](../../internal/repository/claims.go) | `ClaimPattern` (the two admitted forms, specificity, overlap), the claim index (`claimIndex`), and the API service: `Service.Claims`, `Service.PutClaims`, the audit event |
| [routing.go](../../internal/repository/routing.go) | `Routing`: `Route`, `RemoteRefused`, `CoveredByOwnClaim`, `ClaimsFor`, `ExposureSet`, `AutoClaim` |
| [claims_api.go](../../internal/api/v1/claims_api.go) | `GET` and `PUT /api/v1/repositories/{repoKey}/claims` |
| [app/wire_virtual.go](../../internal/app/wire_virtual.go) | builds `Routing` after the repository snapshot; `wireRepository` calls it, the result is `a.m1.routing` |

## Where the data lives

- **Members, deploy target and pin rules are rows** (`virtual_members`, `virtual_deploy_targets`, `virtual_pin_rules`,
  `virtual_pin_members`; Q-REPO-104). `repositories.config` keeps only `virtual.mergedMetadataTtlSeconds`; `marshalConfig` writes
  that form and `marshalFull` the complete document, which is what an update compares to find the changed members. The
  foreign keys are `ON DELETE RESTRICT` on the member side, so a listed repository cannot be deleted.
- **Reads render the document** from both (`Service.Get`, `List`, `Create`, `Patch` all go through `stored`, which loads
  `loadVirtualRels` in the same transaction). A pin rule's members come back in the order of the member list.
- **Claims are rows of `namespace_claims`**, revisioned by `repositories.claims_version`. Patterns are stored in the folded
  form of the format's name rule.
- **The grant index reads the same rows**: `authz.Snapshot.VirtualMembers` and `DeployTargets` are read in the snapshot of the
  index build (`AuthzBuildVirtualMembers`, `AuthzBuildDeployTargets`), so `members` and `deployTarget` of a `repoInfo` come from
  there and no longer from the document.

## Change areas and caches

| What changes | Written by | Area | Seen through |
|---|---|---|---|
| Members, deploy target, pin rules, `mergedMetadataTtlSeconds` | `Service.Create`, `Patch`, `Replace` (the update bumps `config_version`) | `repo_config` | the repository snapshot (`Repository.Members()`, `Virtual()`), the grant index |
| Pin rules | the same write, additionally | `claims` | the snapshot is the reader; the area is notified so everything that routes reads one area |
| Claims | `Service.PutClaims`, `Routing.AutoClaim`, the delete job (when the row of a repository with claims goes) | `claims` | the claim index (`claims` cache, `cache.Registry`), filled under the fill rule of R-ARCH-018 |
| A detach | `detachReferences` in the transaction that marks the repository for deletion | `repo_config` (one event per virtual repository) and `claims` | as above |

The snapshot carries, per virtual repository, the resolved member `Repository` values in list order, the deploy target and
the pin index; per repository `keys` (id to key) and `containing` (member id to its virtual repositories). The claim index is a
second cache because a claim write is frequent (every first publish) and must not rebuild the snapshot. It holds ids only, and
`Route` resolves them through the snapshot of the same call. Both work without Valkey: the PostgreSQL change log carries every
change within `cluster.epoch_poll_interval` (tested with two in-process replicas and no Valkey client in
[test/integration/repository](../../test/integration/repository/)).

## Routing API for the virtual engine

`Routing` is built by `repository.NewRouting(RoutingOptions{Runner, Reader *SnapshotReader, Catalog, Caches, Notifier, Recorder, RemoteRefusal func() bool, SafetyTTL, Log})`.
In the app it is `a.m1.routing` (read under `a.mu`); it is nil on a replica without PostgreSQL. Every method is caller
independent: stored configuration only, never a permission.

```go
type RouteRequest struct {
	Name string // normalized package name or routing name; "" for a request without one
	Path string // canonical path, for formats whose patterns match paths
}
type Route struct {
	Admitted bool                // V's own patterns admit the request (step 1: else 404)
	Members  []Repository        // taking part, in resolution order
	Dropped  map[string]string   // member key -> DropDisabled | DropNotAdmitted | DropPinnedOut | DropClaimedElsewhere
	Decision RouteDecision       // {Kind: DecidedByPin|DecidedByClaim|"", Pattern, ClaimedBy []string}
}
func (rt *Routing) Route(ctx context.Context, virtual Repository, req RouteRequest) (Route, error)
func (rt *Routing) RemoteRefused(ctx context.Context, remote Repository, name string, via *Repository) (bool, error)
func (rt *Routing) CoveredByOwnClaim(ctx context.Context, local Repository, name string) (bool, error)
func (rt *Routing) ClaimsFor(ctx context.Context, format, name string) ([]ClaimMatch, error) // most specific first
func MostSpecificClaims(matches []ClaimMatch) []ClaimMatch
func (rt *Routing) ExposureSet(ctx context.Context, local Repository, name string) ([]Exposure, error) // R-REPO-048 pairs (V, R)
func (rt *Routing) AutoClaim(ctx context.Context, tx meta.Tx, local Repository, name string, by Caller) (AutoClaimResult, error)

func (r Repository) Virtual() (VirtualSettings, bool)   // members (keys), deploy target key, merged TTL, pin rules
func (r Repository) Members() []Repository              // the member repositories in list order
func (r Repository) DeployTarget() (Repository, bool)   // R-REPO-021
```

`Route` implements R-REPO-019 steps 2, 3 and 5. A member is dropped as `disabled` when it is disabled or being deleted,
as `not_admitted` when its include and exclude lists do not admit the request (name patterns match the normalized name, path
patterns the path; a request without a name is admitted), as `pinned-out` when the pin rule that the name selects does not
list it, and as `claimed-elsewhere` when a claim in effect in V covers the name and the member is not among the repositories
with the most specific claim. A pin rule decides before claims. A claim is in effect in V when its scope is `global`, or its
repository is a member of V. If a claim decides and no member is left, the answer is 404 and no remote member is asked: the
engine must not consult the cache or the upstream for it. The members left are ordered priority members first when the format
declares `PriorityMembers`, else in list order. Per-member `read`, the cache-fill check, the tiers and the merge belong to the
engine and follow `Route`.

`Route` takes the member `Repository` values from the snapshot of the `virtual` it is given, so pass a value that came from
`Reader.ByKey` or `List`; a `Repository` from `Service.Get` has no resolved members. `Admitted` is false when the virtual
repository's own patterns refuse the request.

`AutoClaim` is called inside the publish transaction of a local repository whose format supports claims, after the package row
is locked and the ownership claim has been made. It returns an error only for a name the format rejects or a database failure,
never to refuse a publish; its result is for the deploy's audit event. The spec fixes only `autoClaim: skipped`
([R-REPO-023](../spec/repositories-and-proxy.md)); the other values of the last column are the ones this page proposes:

| `Reason` | Meaning | `autoClaim` in the deploy event |
|---|---|---|
| (`Created` is true) | the exact claim `Pattern` was inserted, `claims_version` moved, `repository.claims_changed` (source `auto`) recorded, `claims` notified | `autoClaim: created` |
| `disabled` | `local.autoClaim` is false, or the format has no claims | `autoClaim: off` |
| `covered` | a claim of the repository, of either scope, already covers the name (also a concurrent first publish that inserted it) | none |
| `skipped` | a claim of another repository is in effect in a virtual repository that contains this one: a `members` claim of a repository that shares a virtual repository with it, or a `global` claim while it is a member of any virtual repository | `autoClaim: skipped` |
| `limit` | the repository holds 1000 claims | `autoClaim: limit` |

The reads run in `tx`, so the decision is made on committed claims; two replicas publishing the same new name insert one claim
and move the revision once. `CoveredByOwnClaim` and `ExposureSet` are the two inputs of the shadowing check (R-REPO-048) and
read the replica's index and snapshot; they run before the transaction.

`ClaimsFor` is the lookup by normalized name for engines that need the claimants (the search and audit-forwarding filters of
R-NPM-033 withhold every name any claim or pin rule covers).

## Claims API

| Operation | Declaration | Behavior |
|---|---|---|
| `GET /api/v1/repositories/{repoKey}/claims` | `read` on the repository | the claims sorted by pattern (byte order), `ETag: "<claims_version>"`, `304` on `If-None-Match`; a repository that cannot hold claims answers an empty list |
| `PUT /api/v1/repositories/{repoKey}/claims` | `manage` on the repository, `x-trove-if-match: required` | replaces the manual set; `removeAuto` lists automatic claims to drop; 428 without `If-Match` (the shared step), 412 with `currentEtag` when stale |

`Service.PutClaims(ctx, caller, key, pre, in, guard)` runs in one transaction under the row lock: compare `claims_version`, validate
(`invalid_pattern`, `invalid_value`, `out_of_range`, `not_applicable`), call `guard` when the request adds, keeps, narrows or removes a
global claim, write, bump `claims_version`, record `repository.claims_changed` and notify `claims`. The handler passes
`Server.globalClaimGuard`, which asks the request's access holder for `repositories:write-config` (Q-REPO-105). A write that changes
nothing keeps the revision and records nothing. The response carries the warning `claim_overlaps` for a manual claim that overlaps a
claim of another repository which is in effect together with it; its text does not name the other repository.

## Patterns

A claim or pin-rule pattern is an exact name, or a literal (possibly empty) followed by one trailing `*` or `**`
(`ParseClaimPattern`); everything else is `invalid_pattern`, up to 256 characters. The pattern is folded with the format's name rule
before it is parsed and stored (`parseFolded`), then matched by the same compiler as permissions and include lists
(`CompilePattern`, R-AUTHZ-005): `*` stops at `/`, so a bare `*` claims unscoped names only. Specificity is `rank()`: exact beats prefix,
a longer literal beats a shorter one, `*` beats `**` at an equal literal. `TestMostSpecificIsTotal` checks over every pattern and name
of a small alphabet that two different patterns that cover a name never rank equal.

## Deleting a repository

`DeleteTx` calls `detachReferences` before it marks a repository for deletion. Without `detach` a repository that is a member, a
deploy target or a pin-rule member answers 409 `repository_referenced` naming the virtual repositories. With `detach` the same
transaction removes the references, moves the `config_version` of each virtual repository, records `repository.updated` (`changes:
["virtual"]`) for each and notifies `claims`. A pin rule that loses its last member stays and answers 404 for its names. The claims of a
deleted repository stay in effect until its row goes (so a global claim keeps protecting the names while the job runs); the job's
last transaction notifies `claims` when the repository had any, so every replica drops them within the change-log bound.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `repositories.max_virtual_members` | `100` | the most members one virtual repository lists (`out_of_range` at `/virtual/members`) |
| `proxy.claims.remote_refusal` | `true` | read by `Routing.RemoteRefused` through `RoutingOptions.RemoteRefusal` |
| `proxy.merged_metadata_cache_bytes` | `256MiB` | the size of the cache `merged_metadata` of one replica (`virtual.Options.CacheBytes`); `0` turns the cache off |
| `virtual.mergedMetadataTtlSeconds` (a repository setting) | `7200` | the period of a merged entry, `0` to `86400`; `0` keeps nothing |

## Tests

| Test | Covers |
|---|---|
| [claims_test.go](../../internal/repository/claims_test.go), [routing_test.go](../../internal/repository/routing_test.go), [validate_virtual_test.go](../../internal/repository/validate_virtual_test.go) | pattern forms, total order, claim and pin lookup, routing (claims, pins, priority, admission, the R-AUTHZ-005 matcher table through `routeOf`), remote refusal, validation, the stored form |
| [virtual_test.go](../../test/integration/repository/virtual_test.go) | members and deploy target against PostgreSQL, order change on two replicas within 2 s without Valkey, detach, pin rules, warnings |
| [claims_test.go](../../test/integration/repository/claims_test.go), [claims_http_test.go](../../test/integration/repository/claims_http_test.go) | claims service and API (428, 412, global permission), the auto claim and its skip rule, two replicas, the exposure set, the delete job |

## How to

**Call the auto claim from a format's publish.** In the publish transaction, after the version row and the ownership claim: look the
local repository up with `Reader.ByKey`, call `Routing.AutoClaim(ctx, tx, repo, name, caller)` with the normalized name, put the result
in the deploy's audit event (`autoClaim`), and do nothing else with it. Do not call it for a later version of a name the repository holds.

**Add a new pin or claim form.** Do not: both are limited to the forms for which `rank()` is total. A new form needs a new total order,
a new trie confirmation and a change of R-REPO-023.

## The virtual engine

The engine ([internal/virtual](../../internal/virtual/)) is R-REPO-019 steps 1 to 6 for one caller, the merged-metadata cache of
R-REPO-020, the shadowing check of R-REPO-048 and the explanation. It is a domain package and knows no format and no request:
what it needs of its neighbours it declares as interfaces ([Q-REPO-111](../spec/repositories-and-proxy.md)), and
[formathost](../../internal/formathost/virtual.go) turns a `formats.Request` into what the engine takes.

| File | Responsibility |
|---|---|
| [types.go](../../internal/virtual/types.go) | `Descriptor`, `Caller`, `Plan`, `Member`, `Part`, `Merged`, `Strategy`, `Tier` and the seams: `Router`, `Decider`, `Locals`, `Remotes`, `Tiers`, `Merger` |
| [engine.go](../../internal/virtual/engine.go) | `Options`, `New`, `Start`/`Stop` (the `metadata_changed` subscription), `Plan`, `planFingerprint`, `DeployTarget` |
| [artifact.go](../../internal/virtual/artifact.go) | `Artifact`: the three tiers, `Trove-Resolved-From`, the 404-or-failure rule of step 7, the failure after the first byte |
| [metadata.go](../../internal/virtual/metadata.go) | `Metadata`: strategy, cache lookup and validation, `build` (one goroutine per member), `mergeFingerprint`, `firstMatch` |
| [mcache.go](../../internal/virtual/mcache.go) | the registered cache `merged_metadata`: key, entry, `weigh`, `onMergedChange`, the payload of `metadata_changed` |
| [shadow.go](../../internal/virtual/shadow.go) | `ShadowCheck`, `ShadowOutcome` and its error |
| [explain.go](../../internal/virtual/explain.go) | `Explain` and the `Explanation` of the API |
| [metrics.go](../../internal/virtual/metrics.go) | `trove_virtual_resolutions_total`, `trove_shadow_checks_total` |
| [virtualtest](../../internal/virtual/virtualtest/) | the fakes and fixtures: repositories with members, `Rows` (package rows), `Merger` (a test format), `Remote` (a programmable remote engine), `Artifacts` (tiers), `Router`, `NewDecider` (the real decision point over a few grants) |
| [formathost/virtual.go](../../internal/formathost/virtual.go) | `NewResolver` (the `formats.Resolver` over the engine), `NewVirtualLocals` (`Locals` over the package rows and the item store) |
| [api/v1/resolution_api.go](../../internal/api/v1/resolution_api.go) | `GET /api/v1/repositories/{repoKey}/resolution?path=` |
| [app/wire_virtual_engine.go](../../internal/app/wire_virtual_engine.go) | the wiring step `virtual-engine` (inputs: `infra`, `virtualArea`, `authzArea`; the item store from the files area): builds the engine as a component; `resolutionExplainer` for the API |

### The request path

`Plan(ctx, caller, v, descriptor)` is steps 1 to 5: `Routing.Route` (patterns, pin rules, claims, order; the caller plays no part),
then `Decider.MemberUsableOn` for each member the route kept, which is the per-member `read` and the populate check
(R-AUTHZ-009 rules 2 and 3). The plan holds the members in resolution order with `MayFill`, the number of members the caller cannot read,
and the fingerprint. The plan does not repeat the gate on the virtual repository (rule 1) nor the rule for anonymous callers (rule 6):
the coarse check of the request decided both through `DecideOn` before the format ran; `Plan` of a request that skipped it would
consult only the members the caller can read, which is safe, but would not refuse.

`Artifact(ctx, caller, plan, tiers, w)`: local members in order, then the caches of the remote members, then the upstreams of the
remote members with `MayFill`. The first hit is served with `Trove-Resolved-From: <member>` (never on an endpoint with rules). A member
the caller may not fill is not consulted in the third tier; when the reason is the endpoint's `cache_fill: false`
(`Subject.Endpoint != "" && !Subject.CacheFill`), `Options.FillRefused` counts it in `trove_endpoint_cache_fill_refused_total`, the remote
engine's family (`proxy.Engine.FillRefused`, Q-EP-58). Nothing found: 404 if every consulted member said "not found" (or refused to
populate, which counts as a miss), else the failure of the first member that failed transiently. A failure after the first byte ends the
request.

On an endpoint with rules (`Caller.Rules`) no answer names a member (R-EP-018, Q-EP-56): `Artifact` and `Metadata` pass every error through
`Caller.asAddressed`, which keeps kind, code, `Retry-After` and headers, writes a detail that names the virtual repository, keeps the
member's error as the logged cause, and turns a `NotFound` into the engine's one `404`.

`Metadata(ctx, caller, plan, merger)`: by `merger.Strategy`. `FirstMatch` serves the document of the first member that has it (local members,
then remote members) and caches nothing. `Merge` and `Newest` (resolved as `Merge`; revalidating the remote members first, R-REPO-036, waits
for the first format with a latest-version request) collect one document per member concurrently and call `merger.Merge`.

### The merged-metadata cache

| Aspect | Rule |
|---|---|
| Key | `mergedKey`: virtual id, path, variant, endpoint, the plan fingerprint (endpoint and ordered member id, class, populate right: D-7, Q-REPO-110), the member ids and the SHA-256 of the package name (the last two make eviction a scan of keys) |
| Entry | merged bytes, merge fingerprint, the rev_seq of every eligible local member (0 when absent), expiry |
| Hit | the entry is not expired and one `Locals.RevSeqs` call returns the recorded revisions; otherwise the entry is removed and the document is built again. No message is needed |
| Expiry | the earlier of `virtual.mergedMetadataTtlSeconds` after the merge and the earliest `ExpiresAt` of the remote documents; the cache's safety TTL is 2 hours |
| Not cached | a partial merge, an entry already expired (a stale remote document), a path with no package name, a period of 0, `proxy.merged_metadata_cache_bytes: 0` |
| Partial | a remote member that failed transiently and has no copy is left out; `Cache-Status: trove; detail=partial`. A member that answers not found, or that the caller may not populate and that has nothing stored, is a miss and not a reason for `partial` |
| Cache-Status | `trove; hit; ttl=<s>` for a hit, `trove; fwd=miss` for a built document, `trove; detail=partial` |
| Early eviction | the signal `metadata_changed` with `PackageChanged(repoID, name)` drops the entries that contain the member and the package; a coarse signal or a payload that does not parse drops everything. The area `cache_invalidation` drops by `invalidation.ParsePayload`: `virt` the entries of that virtual repository under the prefix (whole segments), `add` the entries that contain that remote repository under the prefix, `del` nothing; `claims`, `repo_config` and an unknown kind flush |
| Fill rule | the cache's `Load` (R-ARCH-018): a result computed while one of the three areas changed is served and not stored |

`PackageChanged` is the payload that a publisher of a local change (the `OnCommit` of a format's writer, after its commit) and the remote engine
(after a refresh with new content) publish on the signal area `metadata_changed`. A format cannot import the engine, so `formats.PackageChanged` spells the same payload
(a test in `internal/formathost` pins the two); the npm format attaches it with `PackageWriter.Notify` to every change of the rows of a local package (publish, tag change, unpublish, restore).
The remote engine does not publish it yet; the engine is correct without it.

### The contract of a format's merge

A format implements `formats.VirtualAdapter` (`Strategy`, `Merge`, `LocalDocument`); `formathost` builds the engine's `Merger` from it. What the
engine requires:

- `LocalDocument` returns the revision the document was rendered at **whether or not** it found a document (a package row without a live
  version has a `rev_seq`), and renders from committed rows, never from a cache the engine does not validate.
- `Merge` gets the parts in resolution order. A part has `Tier` `Local` or `Remote`; the version entry comes from the lowest (tier, position)
  that holds it, where a remote part's version is in the second tier when `CachedVersions` has it (R-REPO-020). A part with `Restricted` set
  lists **only** `CachedVersions` of its document (R-AUTHZ-008, Q-REPO-113): the field is the only thing that stops a caller who may not fill a
  mirror from being offered a version it cannot download.
- The merged bytes hold no absolute link: the format rewrites links to the response base URL after the cache, which is why the base URL is not in
  the key.
- `ArtifactPrefix` is derived by `formathost` from `Format.PackageDocumentPath` (the document's path, or its directory when the document is an
  `index.json`, then a slash) and the version of a cached path from `Format.Classify`; a format whose cached artifacts live elsewhere has no
  cached versions in a merge.

### The shadowing check

`ShadowCheck(ctx, caller, local, name, docPath)` runs after the write was authorized and before the commit, in no transaction: it does nothing when
`Locals.HoldsVersion` or `Router.CoveredByOwnClaim` says the check does not apply, asks `Router.ExposureSet`, then each distinct remote repository
**once** and concurrently: `Remotes.EntryState` (a stored document that is not gone serves the name; one marked gone does not; `Negative` is never
read) and, with nothing stored, `Remotes.Probe` with the call of the publisher without the populate check. The outcome follows the table of R-REPO-048;
`manage` is `Decider.CheckOn`, which audits nothing. `ShadowOutcome.Err(name)` is the 403 or 503; the formats see it as `formats.ShadowOutcome.Refusal`,
with the pairs in `Shadowed` for the audit event of the commit transaction. The codes are registered in `errs` (`shadows_remote_package`,
`shadow_check_unavailable`). A format calls `Host.Resolver().ShadowCheck(ctx, rq, name)` for a first version of a name and answers `Refusal` when
`Proceed` is false.

### The explanation API

`Explain` is `Plan` for every member of the list, then `decide` for each: the reasons of the route, `hit`/`miss` for a local member
(`Locals.Holds`), and for a remote member `refused-claimed` (`Router.RemoteRefused`), `hit` (`Remotes.Cached`), `no-cache-fill`, then
`EntryState`: `negative`, `offline`, `circuit-open`, `miss`. It makes no call that can reach an upstream. The handler
[resolution_api.go](../../internal/api/v1/resolution_api.go) classifies the path with the format's `Classify`, passes `Subject` and the request's
index, and answers 503 on a replica without the engine. The operation declares `read` on the virtual repository, which the shared step decides.

### The seams

The parameter types of `ctx` are elided below.

```go
// Router: *repository.Routing implements it.
type Router interface {
	Route(ctx, virtual repository.Repository, req repository.RouteRequest) (repository.Route, error)
	RemoteRefused(ctx, remote repository.Repository, name string, via *repository.Repository) (bool, error)
	CoveredByOwnClaim(ctx, local repository.Repository, name string) (bool, error)
	ExposureSet(ctx, local repository.Repository, name string) ([]repository.Exposure, error)
}
// Decider: authz.Decider implements it.
type Decider interface {
	MemberUsableOn(ctx, idx *authz.Index, sub authz.Subject, member string, c authz.Coordinate) (read, fill bool)
	ReadableOn(ctx, idx *authz.Index, sub authz.Subject, keys []string) ([]string, error)
	HoldsOn(idx *authz.Index, sub authz.Subject, permission string) bool
	CheckOn(idx *authz.Index, sub authz.Subject, items []authz.CheckItem) ([]bool, error)
}
// Locals: formathost.NewVirtualLocals(pkgs, items); *packages.Store implements RevSeqs.
type Locals interface {
	RevSeqs(ctx, repoIDs []int64, name string) (map[int64]int64, error)
	Holds(ctx, m repository.Repository, d Descriptor) (bool, error)
	HoldsVersion(ctx, m repository.Repository, name string) (bool, error)
}
```

### Binding the proxy engine

The remote engine is bound: `Options.Remotes` takes `formathost.NewVirtualRemotes` over the proxy engine ([wire_virtual_engine.go](../../internal/app/wire_virtual_engine.go), [proxy.md](proxy.md) "Binding to the virtual engine"). Without a remote engine the virtual engine would treat every remote member as one that cannot be asked (502 for an artifact, `partial` for a merge, `offline` in the explanation, undetermined
for the shadowing check). The seam it binds:

```go
type Remotes interface {
	Cached(ctx, rc RemoteCall) (bool, error)                      // proxy.Engine.Cached: Request{Path, Kind from the path}
	Metadata(ctx, rc RemoteCall) (*RemoteDoc, error)              // proxy.Engine.Metadata: Request{Mode Normal, MayFill: rc.MayFill}
	EntryState(ctx, repoID int64, path string) (EntryState, error) // proxy.Engine.EntryState, plus the three fields below
	Probe(ctx, rc RemoteCall) (ProbeResult, error)                // proxy.Engine.Probe: Served | NotServed | Undetermined(retryAfter)
	CachedArtifacts(ctx, repoID int64, prefix string) ([]string, error) // proxy.Engine.CachedArtifacts
}
```

| Engine field | Source |
|---|---|
| `RemoteCall.MayFill` | the engine's `Request.MayFill`: without it `Metadata` serves what the cache holds (an expired document too) and never contacts the upstream; a path the cache lacks answers `NotFound` or `populate_not_permitted`, both a miss |
| `RemoteDoc` | `proxy.Document`: `Digest`, `Bytes`, `ExpiresAt`, `Stale`; `Fetched` is true when the call contacted the upstream (its `CacheStatus` is not a hit) |
| `EntryState.Stored`, `GoneAt` | R-REPO-048 steps 1 and 2: any variant stored, the gone mark |
| `EntryState.Negative`, `Offline`, `CircuitOpenFor` | only for the explanation: a peek at the negative entry (the L1, then Valkey; unavailable Valkey means false), `remote.availability.mode`/the global switch, the circuit copy's remaining open time. The shadowing check ignores all three |
| `Probe` | offline: `NotServed`; open circuit: `Undetermined` with the remaining time; else one GET through the fetch client, storing nothing and writing no negative entry |

`RemoteCall` carries the remote and virtual repositories, the path, variant and package, the subject (for the audit and the upstream log, with
`flight_role: probe` for a probe) and the endpoint. Two more lines complete the wiring when the formats with a virtual class register: the Host
takes `formathost.NewResolver(engine, nil)` as `Options.Resolver` in `wireFormatHost`
([wire_formats.go](../../internal/app/wire_formats.go); the step now takes `virtualEngineArea` as an input and runs after `wireVirtualEngine`), and the format's writer calls `ShadowCheck`
(npm: `shadowCheck` in [publish.go](../../internal/formats/npm/publish.go); a proceeding outcome reaches the publish transaction, which writes `repository.remote_package_shadowed` and `shadowCheck: undetermined`).
The management API already takes the engine (`Options.Resolution`, set in `startClient` of [app.go](../../internal/app/app.go) by `resolutionExplainer`).

### Tests of the engine

| Test | Covers |
|---|---|
| [artifact_test.go](../../internal/virtual/artifact_test.go) | R-REPO-019 tiers, header, 404 and 502, step 1 to 5, R-AUTHZ-009 rules 3 and 5 to 8 with the real decision point (rules 2 and 4 are in the next row) |
| [metadata_test.go](../../internal/virtual/metadata_test.go) | R-REPO-020 (different readable members, shared entries, the rev_seq check without any signal, partial merges, the fingerprint of D-7, expiry, early eviction, the areas), R-ARCH-015 across two engines |
| [shadow_test.go](../../internal/virtual/shadow_test.go) | R-REPO-048: every outcome row, one probe per remote repository, no negative entry, concurrent probes |
| [explain_test.go](../../internal/virtual/explain_test.go) | R-REPO-019 decisions, hidden members, `decidedBy` |
| [exposure_test.go](../../internal/virtual/exposure_test.go) | R-EP-018: a member's failure of an artifact and of a document names only the addressed repository on an endpoint with rules and keeps code and `Retry-After`, a missing item is the one 404, the member stays visible elsewhere; R-EP-019: the members skipped on an endpoint without cache fill are counted, a subject's missing populate right is not |
| [virtual_test.go (formathost)](../../internal/formathost/virtual_test.go) | the bridge: caller, descriptor, tiers on a request of the member, merger, deploy target, shadow outcome |
| [resolution_api_test.go](../../internal/api/v1/resolution_api_test.go) | the operation: declared permission, descriptor, 400, 404, 503, answers validated against the document |
| [virtual_test.go (integration)](../../test/integration/virtual/virtual_test.go) | three replicas on PostgreSQL without Valkey: publish on A visible on B and C at once (50 iterations), claims reaching the merged entries, the explanation against the real routing, the shadowing check against the real exposure set and with no transaction open while a probe runs |

**Bind a format to the engine.** Implement `VirtualAdapter` as above, list `Virtual` in the descriptor's classes, and run check 5 of the conformance kit ([formattest](../../internal/formats/formattest/), the per-version source rule). The kit does not test `Restricted` yet: add a merge case with a restricted part in the format's own tests. Add nothing to `internal/virtual`.
