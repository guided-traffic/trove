# NuGet remote repositories

Status: built (M3, phase 4): the remote class of NuGet, the proxy and cache of a NuGet v3 feed (R-NUGET-014, R-NUGET-015, R-NUGET-031). The virtual class
of NuGet is built and merges a remote member's documents (R-NUGET-017). The forwarded search is built (R-NUGET-016, 2026-11-02): a remote sends a search to
its upstream unless a name of the query is protected, and a virtual repository merges a remote member's forwarded results (R-NUGET-018). The vulnerability
documents, the symbol files and the embedded files are not built.

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
| A registration index and its pages | | the pages are lists of leaves; a page the index lists without a bound or an address is no index; a leaf without a parseable version is left out | `remote.ParseIndex`, `ParsePage` |
| A package | `limits.maxArtifactBytes` (else `proxy.max_artifact_bytes`) while it is received | the archive and the `.nuspec` are read like an upload's; the declared ID and version are the path's (below) | `InspectFill` |

The registration documents a client receives are rendered from the stored ones and are not passed through: the addresses are trove's, the paging is
trove's, the hive filter of R-NUGET-004 is applied, and a member of a catalog entry that trove does not name is kept only when it holds no string that looks
like an `http` or `https` address (Q-NUGET-52). The three web links for people (`projectUrl`, `licenseUrl`, `iconUrl`) are passed through as the upstream wrote
them, as are the `deprecation` and `vulnerabilities` members; a client shows them to a person, and trove neither follows nor fetches them.

## The identity of a cached package

NuGet v3 announces no digest of a package (H-35), so the declared identity is the one check that the upstream served the package that was asked for. The
package is inspected after the digest check and before the item is committed, outside any transaction, and no byte of it reaches a client before that:

- The ID the `.nuspec` declares must equal the ID of the path under Unicode simple case folding, and the version's URL form must equal the path's. A package
  that fails is 502 `upstream_package_mismatch` to every request of the fetch, and nothing is stored: no blob, no item, no row and no negative entry, so
  the right package is served as soon as the upstream serves it.
- A package the archive reader refuses is served and cached as the upstream sent it, logged and counted in
  `trove_nuget_remote_unparseable_packages_total{repo}`, and gets no NuGet row. The bytes are never altered. Every refusal of the reader counts, not only "not
  a ZIP archive, no or several `.nuspec`, a `.nuspec` that does not parse": the hook treats any error of kind `invalid` as an unreadable package
  ([remote_fill.go](../../internal/formats/nuget/remote_fill.go) `InspectFill`), and that includes the limits, the name rules, duplicate and overlapping entries and
  an ID or version that does not parse. The identity check is not applied to those bytes (H-338).
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
| The text of `q` as the client typed it | yes, unless a name in it is protected (below) | `mayForward` |
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

**What comes back.** The answer is bounded like a metadata document (the repository's `limits.maxMetadataBytes`, else `proxy.max_metadata_bytes`, while it
is received; `MaxDocumentBytes` in memory) and kept as received in the replica's memory only (`proxy_query_results`, for `nuget.search.remote_result_ttl`;
never in Valkey or PostgreSQL). It is filtered on every use, so a claim or a grant change applies to a cached answer as soon as the replica has applied it:

- a result whose ID the remote's include and exclude patterns do not admit, that a claim or a pin rule keeps away, that is no NuGet ID, or of which the
  caller may read no version is removed, and `totalHits` is reduced by the results removed from the page (H-347);
- a version the caller may not read is removed; when it is the upstream's latest, the result shows none of the description, links, `deprecation` and
  `vulnerabilities` that describe it (Q-NUGET-78);
- every registration address is the serving repository's; `projectUrl`, `licenseUrl`, `iconUrl` and a vulnerability's `advisoryUrl` are links for people and
  pass as the upstream wrote them; a member of a result that R-NUGET-016 does not name is dropped.

An upstream failure, an open circuit, a missing search resource and an answer that is no search document fall back to the cache-only search with 200 and are
counted in `trove_nuget_search_forward_failures_total{repo}`.

## What this does not cover

### H-336 · A cached package is inspected under the default archive limits, not the configured ones

Stage: `InspectFill`, the hook that reads a package a remote repository fetched ([remote_fill.go](../../internal/formats/nuget/remote_fill.go)).

Subject: every remote NuGet repository on an installation that set `nuget.archive.*` other than its defaults.

Live today, and only where those settings differ from the defaults. The hook of the remote engine receives the spooled package and the remote's context and
no handle on the replica, so it reads the archive with the defaults of `nuget.archive` (65,535 entries, 4 GiB declared, a compression ratio of 100, a `.nuspec`
of 1 MiB). The push route applies the configured values. What is read into memory stays bounded by the fixed bounds of R-ARCH-028 either way.

What makes it real: because a refusal of the reader on a fill means "serve without the identity check" (H-338), a configured limit tighter than the default
would not refuse more packages on a remote; it would let more of them past the identity check. Handing the configured limits to the hook is therefore only
right after H-338 is closed. The addition (a field of `RemoteContext`) changes the frozen contract and is an ADR of its own
([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)).

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

### H-338 · A package the archive reader refuses is cached without its identity check

Stage: `InspectFill` ([remote_fill.go](../../internal/formats/nuget/remote_fill.go)), on a cache fill of a `.nupkg` by a remote repository.

Subject: every remote NuGet repository; the package a client receives for an ID and version it asked for.

Live today (confirmed by reading the code; found by the security audit of 2026-11-02 and not reproduced with a crafted archive). Every error of kind `invalid`
from the archive reader returns "no inspection", and the engine stores and serves the bytes. The upstream (or anyone between it and trove on an `http` upstream)
can therefore serve package B under the path of package A and have it cached for good, by making B's archive one the reader refuses: a `.nuspec` that
compresses better than 100:1, more than 65,535 entries, more than 4 GiB declared, or names the push road refuses (traversal, duplicates, overlapping entries,
central directory and local headers that disagree). Those are the archives the push road refuses to protect clients, and a remote serves them.

What makes it real: an upstream that is malicious or compromised, or an `http` upstream. The fix lets only "no ZIP archive", "no `.nuspec`" and "not XML"
pass as not-a-package, and answers every other refusal with 502 and stores nothing (open finding F-25 of
[promt.md](../projektplan/promt.md#8--open-findings)).

### H-339 · One registration index of an upstream can list any number of pages

Stage: the rendering of a remote's registration index and version list ([handlers_remote.go](../../internal/formats/nuget/handlers_remote.go) `pageBounds`,
`storedPages`, `remoteVersions`).

Subject: the replica that renders, its memory and the upstream request budget of the remote.

Live today (confirmed by reading the code). `pageBounds` takes every page the index lists and `storedPages` fetches and stores each, eight at a time; nothing
caps how many pages one index may list, and the rendering holds the index and every page in memory at once, each page up to `MaxDocumentBytes` inflated. One
client request for the index of an ID can therefore cause as many upstream fetches, cache entries and URL-map rows as the upstream lists pages, and a
rendering that fails caches nothing, so each retry pays again. What makes it real: an upstream that is malicious or compromised, or an `http` upstream. The
fix caps the pages per index (502 `nuget_invalid_upstream_document`) and bounds the inflated bytes of one rendering (open finding F-25).

### H-340 · The identity check folds Unicode case, the rows keep the declared spelling

Stage: `InspectFill` and the rows of a cached package ([remote_fill.go](../../internal/formats/nuget/remote_fill.go), [id.go](../../internal/formats/nuget/id/id.go)
`SameID`).

Subject: the NuGet rows of a cached package whose declared ID differs from the path's ID outside ASCII.

Live today (confirmed by reading the code). `SameID` is `strings.EqualFold`, which treats U+212A KELVIN SIGN as `k` and U+017F LONG S as `s`; the rows are keyed
by the declared ID, the item by the path's ID. A package whose `.nuspec` declares such an ID passes the check for the ASCII path, its rows land under a
different package than its item, and the later eviction leaves an orphan package row. The client receives the package it asked for by path; what is wrong is
search and the package view of the remote. What makes it real: an upstream that serves such a `.nuspec`. The fix keys the rows by the path's ID once the check
passed, or compares the lower-cased forms of R-NUGET-003 (open finding F-25).

### H-347 · The count of a forwarded search tells a caller with row-level reads how many upstream matches it may not see

Stage: the filter of a forwarded answer ([search_remote.go](../../internal/formats/nuget/search_remote.go) `keepForCaller`), for a remote addressed directly
and for a remote member of a virtual repository.

Subject: a caller whose read of a remote is decided per package or version (a grant with package patterns, a token scope, an endpoint with exposure rules)
searching a remote whose upstream holds packages that caller may not read; also the packages an exclude pattern or a claim keeps away from the remote,
when the query itself names none of them.

Live today, as R-NUGET-016 and R-NUGET-034 specify it: `totalHits` is the upstream's count less the results removed from the returned page. The upstream's
matches beyond the page are counted whatever the caller may read, and a request with `take=0` returns the upstream's count unreduced, so
`q=packageid:<name>&take=0` tells whether a package of a guessed name exists upstream, and a free-text query how many match. The names themselves stay
hidden.

What makes it real: an upstream that holds packages the caller may not read and that the remote's credential reads, such as another installation or a
private feed. For nuget.org the count is public anyway. Closing it gives a caller with row-level reads a count of what the pages seen so far support (the
results kept, and one page more while the upstream reports more), which changes the count R-NUGET-034 promises and needs a decision of its own.
