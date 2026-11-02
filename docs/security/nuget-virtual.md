# NuGet virtual repositories

Status: built (M3, phase 4): the virtual class of NuGet (R-NUGET-017 to R-NUGET-019): the merged version list and registrations, the package files
through the members, the merged search over the local members, and the writes through the deployment target. A remote member contributes its documents
and files; its contribution to the search waits for the forwarded search (R-NUGET-016).

This page describes how a NuGet virtual repository decides what a caller reads through it, what an endpoint with exposure rules lets through, how the
search fans out over the members, and where a write goes. A virtual repository holds no package of its own: every answer is built from members, so the
question on every road is which members take part for this caller and which repository's rules apply. The rules are those of the repository the client
addressed, the virtual one (`rq.Addressed()`), for exposure and for the scope of a token; the member decides the grants. The routing of virtual
repositories (patterns, claims, pin rules) is on [repository-model.md](repository-model.md), the decision point and the token scope on
[permissions.md](permissions.md) and [tokens-and-credentials.md](tokens-and-credentials.md), the exposure rules on
[endpoints-and-exposure.md](endpoints-and-exposure.md), and what a remote member's upstream can make trove do on [nuget-remote.md](nuget-remote.md). The
requirements are R-NUGET-017 to R-NUGET-019 and R-NUGET-034 in [spec/formats/nuget.md](../spec/formats/nuget.md) and R-AUTHZ-009 in
[spec/authorization.md](../spec/authorization.md); the code is [handlers_virtual.go](../../internal/formats/nuget/handlers_virtual.go),
[search_virtual.go](../../internal/formats/nuget/search_virtual.go), the bridge [formathost/virtual.go](../../internal/formathost/virtual.go) and the
engine [internal/virtual](../../internal/virtual).

## Reads: which members take part

| Step | What is decided | On which repository | Where | Answer |
|---|---|---|---|---|
| 1 | `read` on the virtual repository itself (the gate), before any member is consulted | the virtual repository | the dispatcher's route-level decision, `evaluate` ([decide.go](../../internal/authz/decide.go)) | 404 as for a missing repository; 401 for an anonymous caller who cannot read every member |
| 2 | `read` on the ID (and the version of a file) under the endpoint's ceiling | the virtual repository: the gate again and its exposure; the grant for the content is a member's (step 3) | `Authorize` in `serveVirtual` and `virtualFile` | 404 |
| 3 | The members that take part: the routing (patterns, claims, pin rules) for the ID, then `read` and the populate right per member | each member, decided as a read through the virtual repository | `Resolver.Plan` → `Engine.Plan` ([engine.go](../../internal/virtual/engine.go), `MemberUsableOn`) | a member the caller may not read takes no part, silently (R-AUTHZ-009 rule 2) |
| 4 | The merge of the documents of those members | the members | `Merge` ([merge](../../internal/formats/nuget/merge)) | a version two members hold comes from the first in resolution order |

A merged document is cached per replica under a key that holds the members that took part and their populate rights
([mcache.go](../../internal/virtual/mcache.go) `newMergedKey`), so a caller who reads fewer members never receives a merge built for one who reads more
(R-AUTHZ-009 rule 4). A token scoped to the virtual repository by its exact key reads the members through it and not on their own (Q-AUTHZ-163); the
coordinate of each member read carries the virtual repository as `Addressed`, which is what lifts the scope.

## Exposure: the rules of the addressed repository

On an endpoint with `exposure: rules`, what the endpoint exposes is decided on the repository the client addressed (Q-EP-11), the virtual one, never
on its members. A rule on a member does not narrow what the virtual repository serves, and a rule on the virtual repository narrows every member.

| Road | How the rules apply | Where |
|---|---|---|
| Version list, registrations | the package is checked with the view before the merge is asked for; the merged rows are narrowed by `exposes(viewOf(rq), id)` before the document is rendered and paged | `serveVirtual`, `virtualFlat`, `virtualRegistration` |
| `.nupkg`, `.nuspec` | `Authorize` on the version (the ceiling decides on the addressed repository) | `virtualFile` |
| Search | every row of a local member is decided by `MayRead` as a read through the virtual repository (`ReadScopeOn` with the addressed repository, Q-AUTHZ-168), so the ceiling applies the virtual repository's rules; every member's results are then narrowed by the view, and a result whose latest version the view hides is dropped whole (Q-NUGET-75) | `runVirtualSearch`, `exposedHit` |
| Validators | the ETag and the rendition key carry the endpoint and its exposure fingerprint; on an endpoint with rules the ETag follows the filtered rendering instead of the merge fingerprint (Q-NUGET-87) | `serveVirtual`, `serveVirtualFiltered` |

A search term meets only the versions the caller may read (Q-NUGET-74): a `version:` term must name an exposed version and the other terms are tested on
the latest exposed version, so `totalHits` tells nothing of a hidden version.

## Search: the fan-out over the members

The search through a virtual repository (R-NUGET-018) asks the members of `Resolver.Plan` (step 3 above, without a name: the routing applies no
pattern, claim or pin rule to a search) and merges their answers.

- **Rows.** A local member's rows are decided by the caller's read scope for that member, through the virtual repository (Q-AUTHZ-168); a key the
  virtual repository does not reach is read as nothing. The terms are matched per version and applied to the readable versions (Q-NUGET-74).
- **Patterns.** A member contributes only the IDs its own include and exclude patterns and the virtual repository's admit (Q-NUGET-77); the scan of a
  local member and the cache-only search of a remote member decide them per ID, a remote member's forwarded results are filtered after it answered. The patterns are read from the virtual repository's own
  member list; no store is read through it (H-329).
- **Claims and pin rules.** A result of a member is removed when the virtual repository's claims or pin rules keep its ID away from that member
  (`Resolver.KeepsAway`, Q-NUGET-57), so the search never lists what the documents of the same virtual repository deny.
- **Bounds.** At most four members are asked at once, remote members included, each scan holds one database connection per batch, and the scans of
  one request, a remote member's cache-only search included, share one budget of 200,000 packages; above it the request answers 400 and never a partial count (Q-NUGET-76). A request whose context ends while it
  waits for a slot asks no further member.
- **Count.** `totalHits` is the sum of the members' counts minus the duplicates and removals seen in the merge window (R-NUGET-018 step 6). A local
  member's count, and the count of a remote member that answers from its cache, is exact for the caller: its scan applied the read scope and the
  patterns before it counted. A remote member's forwarded count is the upstream's less what the page removed (H-346).

## Writes: the deployment target

A push, a symbol push, an unlist, a relist and a delete addressed to the virtual repository are executed on its deployment target (R-NUGET-019).

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The gate (`read` on the virtual repository), then the write's actions on the deployment target; a virtual repository without one refuses the write | `evaluate` ([decide.go](../../internal/authz/decide.go)), the dispatcher's route-level decision | 403 `forbidden` without a target (Q-NUGET-59); 403 `endpoint_read_only` on an endpoint with rules |
| 2 | The request is handed to the deployment target: `rq.Repo` becomes the target, and the write's handler runs as on the target addressed directly | `redirectClass` ([dispatcher.go](../../internal/api/dispatch/dispatcher.go)) | as the local repository answers |
| 3 | Ownership and the actions of the ID, on the target | the handler's `Authorize` and the writer's ownership check | 403 as on the target |

Grants of `deploy`, `overwrite` or `delete` on the virtual repository itself have no effect (R-AUTHZ-009 rule 5). The lift of a token scope that names
the virtual repository applies to reads only: a token writes through the virtual repository only when its scope names the deployment target with the
action (Q-AUTHZ-163). Unlist and relist never touch another member.

## What this does not cover

Gaps recorded on the pages of their mechanisms that apply here: the reach of a token scoped to a virtual repository follows its members
([H-326](permissions.md#h-326--a-token-scoped-to-a-virtual-repository-by-its-key-reaches-the-members-the-repository-has-when-it-is-used)); the cost of a
search scan for a caller whose reads are decided row by row
([H-335](permissions.md#h-335--a-nuget-search-by-a-caller-with-row-level-grants-scans-up-to-200000-matches-per-request)); a merged document is filtered
only where its handler calls the filter
([H-295](endpoints-and-exposure.md#h-295--a-merged-or-rewritten-document-is-filtered-only-where-its-handler-calls-the-filter)); the rank of a search result
([H-345](endpoints-and-exposure.md#h-345--the-rank-of-a-nuget-search-result-can-follow-the-id-casing-of-a-hidden-version)); a format that reads a member
without `rq.Member` ([H-329](repository-model.md#h-329--a-format-that-reads-a-member-without-rqmember-skips-the-per-member-read-check)); a merge that
ignores `Restricted` ([H-283](repository-model.md#h-283--honouring-restricted-is-up-to-the-formats-merge)).

### H-346 · The merged count takes a remote member's forwarded count before the view and the patterns narrow it

Stage: the `totalHits` of the search through a NuGet virtual repository ([search_virtual.go](../../internal/formats/nuget/search_virtual.go)
`runVirtualSearch`), for a remote member that forwards the search to its upstream ([search_remote.go](../../internal/formats/nuget/search_remote.go)).

Subject: how many packages of a remote member's upstream match a query, among them packages the endpoint does not expose or the patterns of the virtual
repository or the member keep out.

Live today, narrowly. A remote member that answers from its cache scans with the read scope and with its own and the virtual repository's patterns, as a
local member does, so its count is exact (`TestR_REPO_005_TheCacheSearchOfARemoteMemberAppliesTheVirtualRepositorysPatterns`). A forwarded count is the
upstream's less what the remote removed from the page it was asked for (H-347 on [nuget-remote.md](nuget-remote.md)); the merge then narrows every
result it holds by the view, the patterns and the claims and subtracts what it removed, but only within the merge window
(`nuget.search.virtual_merge_window`). The upstream's matches beyond that page are counted whatever the view or the virtual repository's patterns say, so
`take=0` through the virtual repository returns the upstream's count. What makes it real: a remote member whose upstream is a private feed, served
through a virtual repository on a customer endpoint; for a public upstream the count is what anyone learns from the upstream itself. Closing it is the
decision H-347 names: a count of what the pages seen support changes the count R-NUGET-034 promises.
