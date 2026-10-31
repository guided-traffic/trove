A remote repository is a pull-through cache of an upstream registry, for example `https://registry.npmjs.org/`. trove fetches a package document or a tarball from the upstream the first time a client asks for it, stores it, and serves it from then on. Every tarball address in the documents it serves points back at {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}}, so your client fetches every tarball through trove, with its credential.

The repository is read-only: `npm publish`, dist-tag changes and unpublish answer `405` (`E405`) with `npm_class_not_writable`. Publish to a local repository, or through a virtual repository to its deployment target.

### What is cached, and for how long

- A package document is kept for the repository's metadata period (two hours unless the repository sets another) and then checked with the upstream by a conditional request. A version published upstream therefore shows up within that period.
- A package the upstream does not have is remembered for the negative period (30 minutes unless the repository sets another), so a package that appears upstream after somebody asked for it can take up to that long to show up.
- A tarball is cached for good, because its bytes never change. trove compares it with the integrity the upstream document announces before it stores it; a mismatch answers `502` `upstream_integrity_mismatch` and nothing is stored.
- A package document that is not valid, or that names another package or other versions than it should, is refused with `502` `npm_invalid_upstream_document`, and the copy trove had stays in service.

### While the upstream is down

trove serves what it has: a package document past its period is served as it was fetched last, and cached tarballs are served. What was never cached cannot be: the request answers `502` (`upstream_unreachable`, `upstream_error`) or `504` (`upstream_timeout`), and after repeated failures `503` `upstream_assumed_offline` until trove has reached the upstream again. A remote repository that an administrator switched offline serves its cache only and answers `404` `not_cached_offline` for everything else.

### Versions the upstream removes

Public registries remove malicious versions and packages. When the upstream's document no longer lists a version, trove stops serving it: the documents leave it out and its tarball answers `404`, also when it is cached and also for an install from a lockfile, which asks for no document. Once a document's period has passed, trove checks it with the upstream before it serves one of its cached tarballs, so a withdrawal reaches lockfile installs too. A package the upstream deleted answers `404`.

An administrator can keep the tarballs of such versions available with the repository setting `serveRemovedVersions`; the documents still leave the version out. A build that needs a withdrawn version for good publishes its tarball to a local repository.

### When an administrator invalidates the cache

A holder of the manage permission on the repository can make cached documents and cached misses count as expired, for the whole repository or below a package prefix (`POST /api/v1/repositories/<key>/cache/invalidate`). trove then asks the upstream again on the next request, and still serves the stored copy when the upstream fails. Tarballs are never invalidated.

### Names the upstream never sees

A name that a local repository claims for the whole installation is refused by every remote repository, also when the remote repository is addressed directly: the answer is `404`, and no request reaches the upstream. That keeps an internal name from being fetched from a public registry.
