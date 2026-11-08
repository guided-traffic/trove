# NuGet remote repositories

Status: built (M3, phase 4): the remote class of NuGet, the proxy and cache of a NuGet v3 feed (R-NUGET-014, R-NUGET-015, R-NUGET-031). The virtual class
of NuGet is built and merges a remote member's documents (R-NUGET-017). The forwarded search is built (R-NUGET-016, 2026-11-02): a remote sends a search to
its upstream unless a name of the query is protected, and a virtual repository merges a remote member's forwarded results (R-NUGET-018). trove's own
vulnerability documents are built on every class and come from trove's advisory data, not from the upstream
([vuln_documents.go](../../internal/formats/nuget/vuln_documents.go), [vulnerability-scanning.md](vulnerability-scanning.md)); not built are the pass-through
of the upstream's vulnerability information (`nuget.vulnerabilityInfo: upstream`, R-NUGET-023, P1, which the settings schema refuses), the symbol files
(R-NUGET-021, R-NUGET-022) and the embedded files (R-NUGET-024), whose routes answer as not implemented
([handlers_stub.go](../../internal/formats/nuget/handlers_stub.go)).

This page describes what an upstream NuGet feed can make trove do and show, and what limits that. The upstream is a party trove does not trust: its documents
decide which addresses trove contacts, which bytes it stores and what its clients are told. The outbound rules it relies on (addresses, TLS, credentials,
redirects) are on [egress.md](egress.md), [transport-encryption.md](transport-encryption.md) and [data-and-secret-flow.md](data-and-secret-flow.md); the
patterns, claims and the shadowing check on [repository-model.md](repository-model.md); the read permission on [permissions.md](permissions.md). The
requirements are R-NUGET-014, R-NUGET-015, R-NUGET-016 and R-NUGET-031 in [spec/formats/nuget.md](../spec/formats/nuget.md); the code is
[internal/formats/nuget](../../internal/formats/nuget) (`remote.go`, `remote_fill.go`, `handlers_remote.go`, `search_remote.go`) and its leaves
[remote](../../internal/formats/nuget/remote) and [registration](../../internal/formats/nuget/registration).

## Where an address comes from

A NuGet feed names its resources in its service index, and the resources may live on other hosts than the index (nuget.org serves search and symbols from
other hosts of its domain). trove therefore reads the service index of the upstream first and builds every later upstream address from what it announced
and the canonical path of the request, never from a host or a URL a client sent (R-NUGET-015, R-REPO-043).

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The service index is a metadata document of the repository: stored, revalidated, collapsed and counted like every other, one request per period per repository | the engine's discovery step (`Discoverer`), [remote.go](../../internal/formats/nuget/remote.go) | 502 `upstream_error` naming the missing resource, when the index is no service index or lacks `PackageBaseAddress/3.0.0` or a registration resource (Q-NUGET-14); nothing is stored |
| 2 | Every resource address is absolute `http` or `https`, with a host and without user info; a query and a fragment are dropped | [discovery.go](../../internal/formats/nuget/remote/discovery.go) `ParseDiscovery` | 502 `upstream_error` |
| 3 | Every path segment appended to a resource is one escaped segment; a segment that is empty, a dot, a dot-dot, or holds a NUL or a backslash never reaches the wire | `remote.Join` | the path is no resource of the remote: 404 |
| 4 | The host of the address is the upstream's origin or matches `remote.extraHosts`; a URL that leaves an `https` upstream for `http` is refused | the upstream client of the engine (`Allowed`) | 502 `foreign_host_denied`, 502 `insecure_redirect`; no request is sent |
| 5 | The credential of the remote goes to the origin and to the `extraHosts` entries with `sendCredentials`, and nowhere else | the client's hop policy | the request carries no `Authorization` to a host the remote does not send it to |

The address of a page and the address of a package that a registration names are kept as URL-map rows (R-REPO-043) only for a path of the package the
document was fetched for, and only where they differ from the address the service index derives. They pass the steps 3 to 5 again when they are used.
trove sends no request for them while they are only stored.

## What is read of an upstream document

| Document | Bound | What is checked | Where |
|---|---|---|---|
| Any metadata document | the repository's `limits.maxMetadataBytes` (else `proxy.max_metadata_bytes`) while it is received, then `MaxDocumentBytes` (128 MiB) for what is read into memory | the document is the document its path promises, before anything is stored; the previous entry stays on a failure | `PrepareFill` |
| A gzip-coded hive | the inflated size is bounded by `MaxDocumentBytes` whatever the stored size is | the stream starts with the gzip magic and inflates within the bound | `remote.Decode` |
| The version list | | `{"versions": [...]}`; a string that is no version is left out of the rendering | `remote.VersionList` |
| A registration index and its pages | at most 512 pages per index, else no index (nothing stored, no page fetched); at the fill at most 32,768 leaves in one page and inlined in one index, else no document (nothing stored, no URL-map row written); per rendering at most 32,768 versions and `MaxDocumentBytes` inflated over the index and all its pages together, the pages read and converted one at a time (Q-NUGET-83) | the pages are lists of leaves; a page the index lists without a bound or an address is no index; a leaf without a parseable version is left out | `remote.ParseIndex`, `ParsePage`, `remote.Budget` |
| A package | `limits.maxArtifactBytes` (else `proxy.max_artifact_bytes`) while it is received | the archive and the `.nuspec` are read like an upload's; the declared ID and version are the path's (below) | `InspectFill` |

The registration documents a client receives are rendered from the stored ones and are not passed through: the addresses are trove's, the paging is
trove's, the hive filter of R-NUGET-004 is applied, and a member of a catalog entry that trove does not name is kept only when it holds no string that looks
like an `http` or `https` address (Q-NUGET-52). The three web links for people (`projectUrl`, `licenseUrl`, `iconUrl`) are passed through as the upstream wrote
them; a client shows them to a person, and trove neither follows nor fetches them. The `deprecation` and `vulnerabilities` members are passed through without
the upstream's JSON-LD `@id` and `@type` at any depth (they name the upstream's catalog and registration) and without any other member that holds an address;
the `advisoryUrl` of an advisory is kept as a fourth web link for people when it is an absolute `http` or `https` address without user info, and dropped
otherwise (Q-NUGET-85). A virtual repository with a remote member renders the same entries: its merge reads the member's stored index and pages with the
remote's own reading and conversion, within the same bounds (Q-NUGET-82).

## The identity of a cached package

NuGet v3 announces no digest of a package (H-35), so the declared identity is the one check that the upstream served the package that was asked for. The
package is inspected after the digest check and before the item is committed, outside any transaction, and no byte of it reaches a client before that:

- The ID the `.nuspec` declares must equal the ID of the path once both are lowercased character by character with Unicode's simple lowercase mapping, the way
  a client lowercases an ID into its path (U+212A KELVIN SIGN matches `k`, U+017F LONG S does not match `s`), and the version's URL form must equal the
  path's. A package that fails is 502 `upstream_package_mismatch` to every request of the fetch, and nothing is stored: no blob, no item, no row and no
  negative entry, so the right package is served as soon as the upstream serves it. The rows of an accepted package are keyed by the path's ID, the name of
  its item (Q-NUGET-84). A path whose ID holds a character a client's lowercasing would change (U+212A KELVIN SIGN, a capital letter outside ASCII)
  is the 404 of a missing resource before anything is asked of the upstream, and a forwarded search result with such an ID is removed: an upstream that
  lowercases it itself would serve another ID's package, while the patterns, claims and pin rules compare the name character by character
  (R-NUGET-003, `id.ClientForm`).
- Content that no reader takes for a package is served and cached as the upstream sent it, logged and counted in
  `trove_nuget_remote_unparseable_packages_total{repo}`, and gets no NuGet row: no ZIP end record in the window a ZIP reader searches, a ZIP archive without a
  `.nuspec` at its root, a `.nuspec` that is not well-formed XML. The bytes are never altered. A client cannot read such content as a package either.
- Every other refusal of the reader - a limit, a name the push road refuses, duplicate or overlapping entries, a central directory the local headers
  contradict, a second end record after the one the reader reads or an entry `archive/zip` would read from another offset (Q-NUGET-89), encryption,
  several `.nuspec` files, a `.nuspec` of another encoding or shape - and an ID or version that does not parse is 502
  `upstream_package_mismatch`, nothing stored: the identity cannot be checked, and another reader may read the archive as a package
  ([remote_fill.go](../../internal/formats/nuget/remote_fill.go) `InspectFill`, `notAPackage`; Q-NUGET-84).
- The `.nuspec` file of a remote is fetched from the upstream as its own cached artifact and is not checked (Q-NUGET-54).

The rows of a cached package (`origin = 'cache'`) come from the package's own `.nuspec`, so search and the package view of a remote show what the package
declares, and not what the upstream's registration says (H-337).

## The forwarded search

A remote with `nuget.forwardSearch: true` (the default) sends a client's search to the search resource the upstream's service index names (R-NUGET-016,
[search_remote.go](../../internal/formats/nuget/search_remote.go)), through the engine's pass-through (Q-ARCH-270). A virtual repository asks each remote
member the same way (R-NUGET-018).

**What leaves the installation with a forwarded query:**

| What | Leaves | Where it is decided |
|---|---|---|
| The terms of `q` as trove parsed them (R-NUGET-010), written back in the query syntax: each term lowercased like an ID, a field with its colon, a phrase in quotes; never the client's text, so nothing leaves that the withheld rule did not see | yes, unless a name in them is protected (below), or a `version:` term is no version (the query matches nothing) | `mayForward`, `forwardedQ` |
| `skip` and `take` (clamped to `nuget.search.max_skip` and `max_take`), `prerelease`, `semVerLevel` (sent only as `2.0.0`), `packageType` (lowercased) | yes, in the form trove validated | `forwardParams` |
| The caller's name, token, cookie or address | no; the request carries `User-Agent: trove/<version>`, the remote's own credential and custom headers only to a host its hop policy names (step 5 above), and a trace context only with `tracing.propagate_to_upstreams` | the engine's upstream client ([internal/outbound](../../internal/outbound)) |
| The address | the search resource of the stored service index; its host is the upstream's origin or an `extraHosts` entry, as for every address (steps 1, 2 and 4 above) | the host resolves the index (Q-ARCH-282), the engine checks the host |

**Withheld queries.** A query is answered from the remote's cache, and nothing is sent, when a name it carries is one that an exclude pattern of the remote,
a claim in effect or a pin rule keeps away from it, or the literal stem of such a prefix pattern (`acme` for `Acme.*`); `Resolver.KeepsAway` decides in
memory. The names are each term without its field, a phrase word by word, and every run of ID characters inside a word, so punctuation does not carry a
protected name past the rule (`acme!` asks about `acme`, Q-NUGET-80). Through a virtual repository the claims and pin rules in effect there count; on a remote
addressed directly the `global` claims, while remote refusal is on. Include patterns protect nothing. Withheld queries are counted in
`trove_nuget_search_withheld_total{repo}`. Free-text words that name no protected package (`payments`, a project code name) still leave; an installation
for which they are sensitive sets `forwardSearch: false` on the remote.

Nothing is sent either with `forwardSearch: false`, on the cache view `<key>~cache`, on an endpoint with `cache_fill: false`, for an offline remote, and for a
caller who may not fill the remote while no answer to the same query is cached on the replica (the populate check of R-AUTHZ-008, Q-NUGET-79).

**What comes back.** The answer is bounded like a metadata document while it is received (the repository's `limits.maxMetadataBytes`, else
`proxy.max_metadata_bytes`) and read within 8 MiB inflated, a gzip coding the upstream applies anyway included; a larger answer is no answer and the
cache-only search answers, counted as a failure. A result that lists more than 32,768 versions is removed, versions are deduplicated in linear time, and a
request that ends stops the filter (Q-NUGET-90). The answer is kept as received in the replica's memory only (`proxy_query_results`, for
`nuget.search.remote_result_ttl`; never in Valkey or PostgreSQL). It is filtered on every use, so a claim or a grant change applies to a cached answer as soon as the replica has applied it:

- a result whose ID the remote's include and exclude patterns do not admit, that a claim or a pin rule keeps away, that is no NuGet ID, or of which the
  caller may read no version is removed, and `totalHits` is reduced by the results removed from the page (H-347);
- a version the caller may not read is removed; when it is the upstream's latest, the result shows none of the description, links, `deprecation` and
  `vulnerabilities` that describe it (Q-NUGET-78);
- for a caller whose read is decided per version, a result the upstream may have matched on a hidden version is removed and counted as removed: a
  `version:` term that names no readable version, or a hidden upstream latest version with any other term in the query (`matchedOnReadable`), so the
  presence of a result tells nothing of a hidden version;
- every registration address is the serving repository's; `projectUrl`, `licenseUrl` and `iconUrl` are links for people and pass as the upstream wrote
  them; `deprecation` and `vulnerabilities` are cleaned as in the registration, every `@id` and `@type` dropped at any depth and an `advisoryUrl` kept
  only as an absolute `http` or `https` link without user info (Q-NUGET-85); a member of a result that R-NUGET-016 does not name is dropped.

An upstream failure, an open circuit, a missing search resource and an answer that is no search document fall back to the cache-only search with 200 and are
counted in `trove_nuget_search_forward_failures_total{repo}`.

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-35](../spec/formats/nuget.md#h-35--cached-nuget-packages-are-not-checked-against-an-upstream-digest): cached NuGet packages are not checked against an upstream digest.
- [H-5](../spec/repositories-and-proxy.md#h-5--version-removals-reach-cached-artifacts-only-where-the-format-tracks-versions): a version the upstream withdrew stays reachable by exact path from a NuGet remote, which does not track removals per version.

### H-336 · A cached package is inspected under the default archive limits, not the configured ones

Stage: `InspectFill`, the hook that reads a package a remote repository fetched ([remote_fill.go](../../internal/formats/nuget/remote_fill.go)).

Subject: every remote NuGet repository on an installation that set `nuget.archive.*` other than its defaults.

Live today, and only where those settings differ from the defaults. The hook of the remote engine receives the spooled package and the remote's context and
no handle on the replica, so it reads the archive with the defaults of `nuget.archive` (65,535 entries, 4 GiB declared, a compression ratio of 100, a `.nuspec`
of 1 MiB). The push route applies the configured values. What is read into memory stays bounded by the fixed bounds of R-ARCH-028 either way, and a limit
the hook meets refuses the package with 502 `upstream_package_mismatch` (Q-NUGET-84), so no package passes the identity check unread.

What makes it real: an installation that raised `max_entries` or `max_uncompressed_bytes` above the defaults to accept large packages on push cannot restore
such a package through a remote: the fill refuses it under the defaults. An installation that tightened a limit has its remote accept packages its push road
would refuse, with their identity checked. Handing the configured limits to the hook is a field of `RemoteContext`, a change of the frozen contract and an
ADR of its own ([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)).

### H-337 · The registration of a remote is what the upstream says, not what the package declares

Stage: the rendering of the registration documents and of the version list of a remote ([handlers_remote.go](../../internal/formats/nuget/handlers_remote.go)).

Subject: every remote NuGet repository; the dependency groups, the description, the authors, the `listed` flag, the deprecation and the version list that a client sees.

Live today. The registration index, its pages and the version list are rendered from the stored documents of the upstream. They are not compared with the packages:
a version can be listed whose package the upstream does not serve (the restore then fails with the upstream's 404), and a leaf can show dependencies or a
description that differ from the `.nuspec` inside the package. Only the identity of a package that is downloaded is checked against the path; nothing ties a catalog
entry to the package it describes.

What makes it real: a client resolves its dependency graph from the registration (the `.nuspec` inside the package is read after the download), so an upstream
that serves a graph in its registration that differs from its packages decides which packages a restore selects. That is the upstream's own authority over its
feed, which a remote repository accepts by being configured; the gap is that trove cannot tell the two apart for a version it has not cached, and shows the
registration of a version that it has cached unchanged. The cached rows of origin `cache` come from the package and are what search shows.

### H-347 · The count of a forwarded search tells a caller with row-level reads how many upstream matches it may not see

Stage: the filter of a forwarded answer ([search_remote.go](../../internal/formats/nuget/search_remote.go) `keepForCaller`), for a remote addressed directly
and for a remote member of a virtual repository, and the count of the merged search through a virtual repository
([search_virtual.go](../../internal/formats/nuget/search_virtual.go) `runVirtualSearch`).

Subject: a caller whose read of a remote is decided per package or version (a grant with package patterns, a token scope, an endpoint with exposure rules)
searching a remote whose upstream holds packages that caller may not read; also the packages an exclude pattern or a claim keeps away from the remote,
and through a virtual repository the packages its exposure view and its own patterns keep out, when the query itself names none of them.

Live today, as R-NUGET-016 and R-NUGET-034 specify it: `totalHits` is the upstream's count less the results removed from the returned page. The upstream's
matches beyond the page are counted whatever the caller may read. Through a virtual repository the merge narrows the results it holds by the view, the
patterns and the claims and subtracts what it removed, but only within the merge window (`nuget.search.virtual_merge_window`); beyond it a remote member's
forwarded count is added as reported. A remote member that answers from its cache scans with the read scope and every pattern and counts exactly
(`TestR_REPO_005_TheCacheSearchOfARemoteMemberAppliesTheVirtualRepositorysPatterns`). Three requests return the upstream's count with nothing subtracted:
`take=0`; a `skip` past the upstream's results, so the page the upstream returns is empty; and through a virtual repository a `skip` at or beyond the
merge window, where the members are asked to count only. So `q=packageid:<name>&take=0` tells whether a package of a guessed name exists upstream, and a
free-text query how many match. The names themselves stay hidden.

What makes it real: an upstream that holds packages the caller may not read and that the remote's credential reads, such as another installation or a
private feed. For nuget.org the count is public anyway. Closing it gives a caller with row-level reads a count of what the pages seen so far support (the
results kept, and one page more while the upstream reports more), which changes the count R-NUGET-034 promises and needs a decision of its own.

### H-348 · A remote member whose registration page cannot be read fails the merged registration, not only itself

Stage: the merge of a virtual repository's registration entries, where the NuGet virtual adapter reads the pages a remote member's stored index lists by
address ([virtual_remote.go](../../internal/formats/nuget/virtual_remote.go) `remoteMemberEntries`, Q-NUGET-82).

Subject: every virtual NuGet repository with a remote member; the registration documents of an ID that the remote member's upstream lists with pages by
address (128 or more versions on nuget.org), including the versions the local members hold.

Live today. The virtual engine leaves out a remote member it cannot ask and serves the rest as a partial merge (R-REPO-020), but a page is read inside the
format's merge, which can only succeed or fail as a whole: the contract gives a merge no way to mark one member partial. A page the upstream lists and does
not serve (`nuget_invalid_upstream_document`), or a page that cannot be fetched while it is not stored, answers the virtual repository's registration
documents of that ID with 502, where a failing index would have left the member out. The version list and the package files are not affected; a member
the caller may not fill leaves out the pages it has not stored instead of failing.

The same holds for the bounds of one rendering (Q-NUGET-83): the budget of a merge is shared by its remote members
([remote/budget.go](../../internal/formats/nuget/remote/budget.go) `take`, [virtual.go](../../internal/formats/nuget/virtual.go) `Merge`), so one member whose
registration is larger than the budget answers the whole merged registration with 502 instead of being left out.

What makes it real: an upstream that is down, or that lists a page it does not serve, for an ID that a local member of the same virtual repository also holds
and that no claim or pin rule keeps away from the remote. A claim of the local member (`local.autoClaim`) keeps the remote out of the ID's merge and closes
it for that ID (R-REPO-023).

### H-353 · Forwarded searches share the remote's circuit and fetch slots with its downloads

Stage: the engine's query path that a forwarded search takes ([internal/proxy/query.go](../../internal/proxy/query.go) `queryFetch`, the classification in
[classify.go](../../internal/proxy/classify.go)). npm's audit forward does not take it: it goes through `Engine.Exchange`, which takes no fetch slot and
neither reads nor moves the circuit ([internal/proxy/exchange.go](../../internal/proxy/exchange.go)).

Subject: every remote with `forwardSearch` on; the downloads of its uncached packages.

Live today. A query takes a slot of the remote's `limits.maxConcurrentFetches` and is admitted by the remote's circuit like a download, and a 429 or 5xx
answer of the upstream's search host counts towards opening that circuit. A burst of searches against a search host that rate-limits or fails therefore
opens the circuit of the whole remote, and every request for a package that is not cached answers 503 for `availability.circuitOpenSeconds`, for every
caller; queries also hold slots that downloads wait for (`proxy.queue_timeout`). Search falls back to the cache meanwhile, so search itself degrades
gracefully; downloads do not. What makes it real: an upstream whose search resource lives on another host or service than its package content (nuget.org's
search runs on its own hosts) and fails or rate-limits on its own, and callers that search often (IDE package browsers). Closing it gives queries a circuit
and a small slot budget of their own in the engine; it changes the engine's contract of Q-ARCH-270 and needs a decision of its own.

