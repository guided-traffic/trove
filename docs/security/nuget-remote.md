# NuGet remote repositories

Status: built (M3, phase 4): the remote class of NuGet, the proxy and cache of a NuGet v3 feed (R-NUGET-014, R-NUGET-015, R-NUGET-031). The forwarded
search, the vulnerability documents, the symbol files, the embedded files and the virtual class of NuGet are not built and have no page.

This page describes what an upstream NuGet feed can make trove do and show, and what limits that. The upstream is a party trove does not trust: its documents
decide which addresses trove contacts, which bytes it stores and what its clients are told. The outbound rules it relies on (addresses, TLS, credentials,
redirects) are on [egress.md](egress.md), [transport-encryption.md](transport-encryption.md) and [data-and-secret-flow.md](data-and-secret-flow.md); the
patterns, claims and the shadowing check on [repository-model.md](repository-model.md); the read permission on [permissions.md](permissions.md). The
requirements are R-NUGET-014, R-NUGET-015 and R-NUGET-031 in [spec/formats/nuget.md](../spec/formats/nuget.md); the code is
[internal/formats/nuget](../../internal/formats/nuget) (`remote.go`, `remote_fill.go`, `handlers_remote.go`) and its leaves
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
- A package that cannot be read (not a ZIP archive, no or several `.nuspec`, a `.nuspec` that does not parse) is served and cached as the upstream sent it,
  logged and counted in `trove_nuget_remote_unparseable_packages_total{repo}`, and gets no NuGet row. The bytes are never altered. The identity check is
  therefore not applied to bytes that are no package; those bytes are what the upstream answered for that path, with the media type the engine's block list
  lets through (R-REPO-042).
- The `.nuspec` file of a remote is fetched from the upstream as its own cached artifact and is not checked (Q-NUGET-54).

The rows of a cached package (`origin = 'cache'`) come from the package's own `.nuspec`, so search and the package view of a remote show what the package
declares, and not what the upstream's registration says (H-337).

## What this does not cover

### H-336 · A cached package is inspected under the default archive limits, not the configured ones

Stage: `InspectFill`, the hook that reads a package a remote repository fetched ([remote_fill.go](../../internal/formats/nuget/remote_fill.go)).

Subject: every remote NuGet repository on an installation that set `nuget.archive.*` tighter than its defaults.

Live today, and only where those settings differ from the defaults. The hook of the remote engine receives the spooled package and the remote's context and
no handle on the replica, so it reads the archive with the defaults of `nuget.archive` (65,535 entries, 4 GiB declared, a compression ratio of 100, a `.nuspec`
of 1 MiB). The push route applies the configured values. What is read into memory stays bounded by the fixed bounds of R-ARCH-028 either way; what differs is the
central directory a configured, smaller limit would have refused.

What makes it real: an operator who lowered `nuget.archive.max_entries` to refuse a class of archives, and an upstream (or a compromised one) that serves such an
archive. The package is read with the default bound, its identity is checked, and it is stored and served; a client that restores it receives it. The effect is
bounded by `limits.maxArtifactBytes` of the remote and by the time limit of the reader; kit check 6 runs the crafted archives of R-ARCH-028 through the hook
and holds it to the time and heap bounds (Q-ARCH-281). A fix hands the replica's limits to the hook through the contract, which is frozen since the end of M3:
the addition (a field of `RemoteContext`) is an ADR of its own ([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)).

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
