# Virtual repositories: members, deploy target, pin rules, claims and routing

The caller-independent part of virtual repositories ([R-REPO-017](../spec/repositories-and-proxy.md) to R-REPO-023,
R-REPO-048): which repositories a virtual repository lists, which of them take part in a request, which local
repository owns a name, and the API that edits the claims. Code: [internal/repository](../../internal/repository/)
(the files in the table below), the statements in [queries/virtual.sql](../../internal/meta/postgres/queries/virtual.sql)
and [queries/claims.sql](../../internal/meta/postgres/queries/claims.sql), the handlers in
[claims_api.go](../../internal/api/v1/claims_api.go). The resolution itself (per-caller steps, tiers, the merged
metadata, the shadowing check, the explanation API) is the virtual engine of [R-REPO-019](../spec/repositories-and-proxy.md)
and R-REPO-020, which a later work package adds as `internal/virtual` and as sections of this page.

What exists today: the model and its validation, the routing and the claim index, the auto claim of a first publish, the
claims API, the detach of R-REPO-013, and the grant index reading the members from their rows. What does not exist yet:
nested virtual repositories (R-REPO-018, a member that is virtual answers `invalid_member`), `effectiveMembers`, and every
format that has package names, so no repository of a real format can hold a claim until npm lands. The tests use a
format registered in a test catalog.

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
