A remote repository is a pull-through cache of an upstream NuGet feed, for example `https://api.nuget.org/v3/index.json`. trove fetches a document or a `.nupkg` from the upstream the first time a client asks for it, stores it, and serves it from then on. The service index, the registration documents and the version lists it serves point back at {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}}, so your client fetches every package through trove, with its credential.

The repository is read-only: `dotnet nuget push`, `nuget push`, delete and relist answer `405`. Push to a local repository, or through a virtual repository to its deployment target.

### What is cached, and for how long

- The upstream's service index, the version lists and the registration documents are kept for the repository's metadata period (five minutes unless the repository sets another) and then checked with the upstream again. A version published upstream shows up within that period.
- An ID the upstream does not have is remembered for the negative period (30 minutes unless the repository sets another).
- A `.nupkg` is cached for good, because its bytes never change. trove checks that the package inside is the ID and version the client asked for; a package that is not answers `502` `upstream_package_mismatch` and nothing is stored.
- Search is forwarded to the upstream while the repository's `forwardSearch` setting is on, and the answer is kept for a short time. A search term that an administrator protects, a claimed internal name for instance, is never forwarded.

### While the upstream is down

trove serves what it has: documents past their period and cached packages. What was never cached cannot be served: the request answers `502` (`upstream_unreachable`, `upstream_error`) or `504` (`upstream_timeout`), and after repeated failures `503` `upstream_assumed_offline` until trove has reached the upstream again. A remote repository an administrator switched offline serves its cache only and answers `404` `not_cached_offline` for the rest.

### When the upstream removes an ID

Public feeds remove malicious packages. When the upstream answers `404` for an ID, trove stops serving it: its documents and its cached packages answer `404` on every address, a restore from a lock file included. A single version the upstream withdrew from an ID that still exists stays downloadable from the cache by its exact address, because NuGet does not tell clients about the removal of one version.

### Names the upstream never sees

An ID that a local repository claims for the whole installation is refused by every remote repository, also when the remote repository is addressed directly: the answer is `404`, and no request reaches the upstream. That keeps an internal name from being fetched from a public feed.
