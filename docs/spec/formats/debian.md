# Debian and Ubuntu APT repositories

Status: specification — built (phase T-17, closed; FULL.md block D row "Debian / APT repositories", the work packages and their evidence in
[apt-work-packages.md](../../projektplan/apt-work-packages.md)): local, remote (modes `pullThrough` and `mirror`) and virtual repositories for
`apt` and `apt-get` on Debian and Ubuntu; what stays is in [Deferred](#deferred) and in the gaps below

This page specifies how trove serves APT repositories: the format `deb` for local, remote and virtual repositories in the
Debian repository format; how a local repository takes `.deb` uploads and lists them in suites and components; how trove
builds the repository-wide indexes (`Packages`, `Release`) as signed, consistent snapshots in a job and serves them by hash
across replicas; where the signing keys live, how they rotate and how clients learn them; how a remote repository passes an
upstream's signed release documents through byte for byte, verifies them against configured keys and serves every index file
by the hash its release names; what a mirror of an APT archive keeps; how a virtual repository merges its members into one
index that trove signs; the limits on untrusted package and index content; operation on interchangeable replicas; and the test
suites. Mechanisms shared by all formats are owned by other pages and only refined here:

| Topic | Owner |
|---|---|
| Repository model, virtual resolution, remote request path, mirror engine, bounds, throttling, the reindex operation | [repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-016 to R-REPO-029, R-REPO-041, R-REPO-048, R-REPO-050 to R-REPO-059) |
| Items, blobs, the upload pipeline, generated items, trash can, garbage collection | [storage.md](../storage.md) (R-STOR-003 to R-STOR-005, R-STOR-012, R-STOR-018, R-STOR-022 to R-STOR-024, R-STOR-039) |
| Credential sources, challenges, tokens, anonymous access | [identity-and-authentication.md](../identity-and-authentication.md) (R-AUTH-001, R-AUTH-002) |
| Actions, patterns, evaluation, ownership, virtual gate | [authorization.md](../authorization.md) (R-AUTHZ-005 to R-AUTHZ-009, R-AUTHZ-016, R-AUTHZ-018) |
| Format contract, job-built indexes, jobs, stored-secret key ring, untrusted input, read budget | [architecture.md](../architecture.md) (R-ARCH-008 to R-ARCH-012, R-ARCH-016, R-ARCH-021, R-ARCH-027, R-ARCH-028) |
| Export classes, revision columns, revocation journal replay, restore without remote caches | [backup-and-restore.md](../backup-and-restore.md) (R-BAK-004, R-BAK-008, R-BAK-022, R-BAK-024) |

Covers: [formats/debian.md](../../features/formats/debian.md) (`PKG-DEB-*`); the section [Catalog coverage](#catalog-coverage)
lists every entry with what this phase builds, what stays in FULL.md and what is not offered.

External references, normative wherever this page does not say otherwise:
[Debian repository format](https://wiki.debian.org/DebianRepository/Format),
[Debian Policy, control files](https://www.debian.org/doc/debian-policy/ch-controlfields.html),
[deb(5)](https://manpages.debian.org/stable/dpkg-dev/deb.5.en.html),
[deb-version(7)](https://manpages.debian.org/stable/dpkg-dev/deb-version.7.en.html),
[sources.list(5)](https://manpages.debian.org/stable/apt/sources.list.5.en.html),
[apt-secure(8)](https://manpages.debian.org/stable/apt/apt-secure.8.en.html),
[apt_auth.conf(5)](https://manpages.debian.org/stable/apt/apt_auth.conf.5.en.html),
[OpenPGP, RFC 4880](https://www.rfc-editor.org/rfc/rfc4880) and [RFC 9580](https://www.rfc-editor.org/rfc/rfc9580).

## Conventions on this page

| Placeholder | Meaning |
|---|---|
| `<base>` | trove's public base URL of the endpoint the request arrived on; `https://trove.example.com` in examples (# example) |
| `<repo URL>` | `<base>/api/deb/<projectKey>/<repoKey>`, the URI of an APT source line, without a trailing slash; route paths below are relative to it |
| `<suite>` | a suite (distribution) name as APT names it in a source line: `stable`, `bookworm`, `noble-updates` (# example) |
| `<component>`, `<arch>` | a component (`main`) and an architecture (`amd64`, `arm64`, `all`) (# example) |
| `$TROVE_TOKEN` | a trove token (`trv_pat_…`, `trv_sat_…` or `trv_cit_…`, INV-8) |

Project and repository keys used in examples (# example): the project `web` with `deb-local` (local), `debian` (remote of
`https://deb.debian.org/debian/`, mode `pullThrough`), `debian-mirror` (remote of the same upstream, mode `mirror`) and `apt`
(virtual over `deb-local` and `debian`, deploy target `deb-local`). A source line therefore reads
`deb [signed-by=/etc/apt/keyrings/trove-web-apt.asc] https://trove.example.com/api/deb/web/apt stable main`.

Priorities: every requirement below is **P2** (block D of the roadmap), built now because the owner set the APT registry as
task T-17 (promt.md section 10); what this phase leaves to FULL.md is in [Deferred](#deferred).

## Client and library behaviour this page relies on

<details>
<summary>Verified against the real clients on 2026-11-08, and what is not verified yet</summary>

Verified by the two reviews of the plan with probe scripts in containers (`debian:13`: APT 3.0.4 with `sqv` 1.3.0; `debian:12`:
`gpgv` 2.2.40; `ubuntu:24.04`: `gpgv` 2.4.4; `ubuntu:22.04`: APT 2.4.15 with `gpgv` 2.2.27):

| Fact | How it was verified | Where this page uses it |
|---|---|---|
| A release signed by keys A and B updates with a keyring holding only A, only B, or both; a keyring holding only an unknown key C fails | `apt-get update --error-on=any` on Debian 13 (`sqv`) and Ubuntu 22.04 (`gpgv`) | Rotation by co-signing (R-DEB-017, Q-DEB-11) |
| Served an older `Date` after a newer one, APT keeps the newer list, exits 0 and prints nothing | Date now − 3600 s after a current one | Rollback protection is per client only (R-DEB-019, H-624) |
| APT refuses a `Release` `Date` more than 10 s ahead of the client clock ("not valid yet"); +5 s passes, +30 s fails | Dates now + 5 s, + 30 s, + 600 s | Dates trail the database clock (R-DEB-010, Q-DEB-30) |
| `sqv` refuses a signature created after the client's now (2 s ahead fails); `gpgv` accepts + 5 s | Signatures 1, 2, 3, 5 s ahead | Signature times trail the database clock (R-DEB-016, Q-DEB-30) |
| `sqv` refuses a signature made before its key's creation time | A key created 30 s in the future, a key created after its signature | Key creation time one hour back (R-DEB-015, Q-DEB-30) |
| Ed25519 as public-key algorithm 27 (RFC 9580), as the OpenPGP module v1.5.2 writes it by default, is unreadable for GnuPG 2.2.27, 2.2.40 and 2.4.7 ("unsupported public-key algorithm (27)"); algorithm 22 (EdDSALegacy) verifies on all three | Keys and signatures made with the module, verified with `gpgv` | Keys are EdDSALegacy over Ed25519 (Q-DEB-10) |
| The OpenPGP module adds a random `salt@notations.openpgpjs.org` notation to each signature | Packet dump of the module's signature | Signatures are not deterministic (R-DEB-016) |
| Debian 13's archive keyring holds 9 certificates and is 75,839 bytes armored | The keyring file of `debian:13` | The bound of `deb.upstreamKeys` (R-DEB-020) |
| For a flat source `deb <url> ./`, APT requests `<url>/./InRelease` | `apt-get --print-uris` on Ubuntu 24.04 | The flat root (R-DEB-003, Q-DEB-39) |
| `sqv` and `gpgv` read the module's algorithm-22 signatures: with keys made by the module (v1.5.2; EdDSALegacy, key created now − 1 h, `Date` and signature now − 60 s), one key verifies; an `InRelease` co-signed by k1 and k2 (`clearsign.EncodeMulti`) verifies with a keyring of k1 or of k1 and k2; a `Release.gpg` alone (`ArmoredDetachSign`, no `InRelease`) verifies; a foreign keyring fails | `apt-get update --error-on=any` on Debian 13 (`sqv` 1.3.0), Debian 12 (`gpgv` 2.2.40), Ubuntu 24.04 (`gpgv` 2.4.4) and Ubuntu 22.04 (`gpgv` 2.2.27) | R-DEB-015 to R-DEB-017, Q-DEB-10, Q-DEB-11 |
| `sqv` checks the signing key against its binding self-signature at the data signature's time and refuses a data signature older than every binding self-signature ("Signing key … is not bound: No binding signature at time …"), even when the key's creation time is older; `gpgv` accepts it | A key created an hour back whose only self-signature is fresh, signing at now − 60 s | The self-signature carries the key's creation time; signatures wait for `BoundSince` (R-DEB-015, R-DEB-016, Q-DEB-40) |
| The OpenPGP module v1.5.2 requires `github.com/cloudflare/circl` v1.6.3; the OpenPGP module and `github.com/ulikunitz/xz` v0.5.17 are BSD-licensed, `circl` v1.6.3 is BSD-3-Clause | `go mod graph` and the modules' licence files | Q-DEB-24 |
| The signer of `internal/openpgp` (built 2026-11-08, stage 1 of T-17) makes documents the four clients accept: an `InRelease` (`ClearSign`) and a `Release.gpg` alone (`DetachSign`) by one key, co-signed by two keys with a keyring of the first or of the second, and by an imported key whose only self-signature is 30 s old and which signs 29 s after the key was bound; a keyring holding another key fails with the missing key named | `apt-get update --error-on=any` against `file:` repositories with `signed-by=<armored key>` on `debian:13` (APT 3.0 with `sqv`), `debian:12`, `ubuntu:24.04` and `ubuntu:22.04` (all with `gpgv`) | R-DEB-015 to R-DEB-017, Q-DEB-40 |
| The counting stub archive (`test/debstub`) is read by the same four clients: its `InRelease` as `gpg` frames it (the signed text lacks the final line ending), its indexes, the flat directory `deb <url> binary/`, the flat root `deb <url> ./` and the suite `testing` with `contrib`; `apt-get install` of its packages installs them and the installed script prints its line. A flat directory's Release names `Suite: binary`, or APT warns "Conflicting distribution" | `apt-get update --error-on=any` and `apt-get install` against `file:` copies of the stub's tree on the four images | R-DEB-035, Q-DEB-73 |
| `internal/openpgp` accepts Debian 13's archive keyring as `deb.upstreamKeys`: package `debian-archive-keyring` 2025.1 holds 9 certificates, 55,918 bytes binary and 75,803 bytes armored without armor headers (75,839 with the headers of `gpg --enarmor`) | The keyring file of `debian:13` through `ParseCertificates` | R-DEB-020, R-DEB-034 |

Not verified, and to be proven by the package that builds the mechanism:

- `sqv`'s exact tolerance below 2 s; the thresholds at which a client clock that trails the database fails (about 60 s on `sqv`,
  about 70 s on `gpgv`, R-DEB-010) are derived from the verified limits above, not measured;
- which suites of `deb.debian.org` publish pdiffs, and how APT treats a pdiff it cannot fetch (R-DEB-023);
- whether APT follows a redirect of `/./` (the flat root is served without one, Q-DEB-39);
- the speed of the xz writer on a merged index of 50 MB (WP8's bound test runs with `compression: ["xz"]`).

</details>

## Requirements

### R-DEB-001 · The format `deb`, its classes and the repository address
Priority: **P2** · Covers: PKG-DEB-001

trove MUST offer the format `deb` for all three repository classes of INV-5. One format serves Debian, Ubuntu and every other
archive in the Debian repository format; its clients are `apt`, `apt-get` and every tool that reads an APT source (Q-DEB-1).

Format registration (R-REPO-003):

| Fact | Value |
|---|---|
| ID, display name, layout | `deb`, `Debian (APT)`, `deb` |
| Classes | `local`, `remote`, `virtual` |
| Protocol base path | `/api/deb/{project}/{key}/` |
| Upstream presets ([R-REPO-062](../repositories-and-proxy.md#r-repo-062--upstream-presets-of-a-format)) | Debian (`debian`, `https://deb.debian.org/debian/`, the default), Debian security updates (`debian-security`, `https://deb.debian.org/debian-security/`), Ubuntu (`ubuntu`, `https://archive.ubuntu.com/ubuntu/`), Ubuntu security updates (`ubuntu-security`, `https://security.ubuntu.com/ubuntu/`) and Ubuntu ports (`ubuntu-ports`, `https://ports.ubuntu.com/ubuntu-ports/`, where arm64 and the other ports live); no extra hosts and no archive keys (Q-REPO-326); probe path empty (the upstream test reports reachability of the URL) |
| Cache periods of a remote | metadata 600 s, not-found 300 s (Q-DEB-29) |
| Pattern subject | `name`: the binary package name |
| Package names | yes; normalization is the identity after the grammar of R-DEB-002 (upper case is refused, never folded) |
| Priority members, pin rules and namespace claims | yes; repository caps no |
| `packageOwnership` default of a local repository | `enforce` |
| Package document | none: `PackageDocumentPath` reports `ok` false, because an APT archive has no document per package; a remote answers the shadowing check from its parsed index rows (R-DEB-023, Q-REPO-270) |
| `VerifyAnnouncedDigests` | `false`: a by-hash digest is always verified because it addresses the file (R-DEB-021); a pool file is verified when a parsed index names its digest (R-DEB-022) |
| Data port | `internal/formatdata/deb` (R-DEB-008) |
| Settings | the member `deb` of the repository document (R-DEB-008, R-DEB-024) |

**Address.** An APT source names the repository URL `<base>/api/deb/<projectKey>/<repoKey>` and one suite; APT appends
`dists/<suite>/…` for indexes and the `Filename` of an index entry for package files:

```text
# /etc/apt/sources.list.d/trove-web-apt.sources (deb822 form)        # example
Types: deb
URIs: https://trove.example.com/api/deb/web/apt
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/trove-web-apt.asc
```

- Every repository of every project is reached on the same host and port; no host-root binding is needed or offered for the
  format in this phase.
- Raw file access (`/files/<projectKey>/<repoKey>/<path>`, INV-6) reads package files and the generated index files of a local
  repository (R-DEB-003). A raw deploy into a Debian repository answers 409, as for every format that is not served at
  `/files/` ([architecture.md](../architecture.md) Q-ARCH-223); a raw delete of a package file runs R-DEB-013, of a generated
  file answers 409 `generated_item` (Q-STOR-181).

Acceptance criteria:

- `curl -s <base>/api/v1/formats | jq '.formats[] | select(.name=="deb")'` lists the classes `local`, `remote`, `virtual`, the
  protocol base path `/api/deb/{project}/{key}/` and the five upstream presets above, `debian` the default.
- With the source above, `apt-get update` and `apt-get install trove-hello` succeed in a Debian 13 container without
  `trusted=yes` (e2e, R-DEB-035).
- `curl -X PUT --data-binary @hello.deb <base>/files/web/deb-local/pool/hello.deb` answers 409 and stores nothing.

### R-DEB-002 · Names: packages, versions, architectures, suites and components
Priority: **P2** · Covers: PKG-DEB-002

| Name | Grammar | Notes |
|---|---|---|
| Package | `^[a-z0-9][a-z0-9+.-]{1,127}$` | [Debian Policy 5.6.1](https://www.debian.org/doc/debian-policy/ch-controlfields.html#source); the package name of authorization, patterns, claims and ownership |
| Version | `[<epoch>:]<upstream>[-<revision>]`: epoch `[0-9]{1,9}`; upstream starts with a digit and holds `[A-Za-z0-9.+~-]` (a hyphen only when a revision follows, never a colon); revision `[A-Za-z0-9.+~]+`; at most 128 characters | [deb-version(7)](https://manpages.debian.org/stable/dpkg-dev/deb-version.7.en.html) |
| Architecture | `^[a-z0-9][a-z0-9-]{0,31}$`; `all` stands for architecture-independent packages; `any` and wildcards are refused | |
| Suite of a local or virtual repository | `^[a-z0-9][a-z0-9._+-]{0,63}$` (no `/`) | the `<suite>` of `dists/<suite>/` |
| Component of a local or virtual repository | `^[a-z0-9][a-z0-9._+-]{0,63}$` (no `/`) | |

- **Version order** is dpkg's ([deb-version(7)](https://manpages.debian.org/stable/dpkg-dev/deb-version.7.en.html)): the epoch
  numerically, then the upstream version and the revision, each compared by alternating runs of non-digits (letters before
  other characters, `~` before everything including the end of the string) and digits (numerically). The view's `Compare`, the
  mirror plan and `maxVersionsPerPackage` use it.
- **No pre-releases.** Debian versions carry no pre-release marker that every archive uses alike (`~` marks pre-releases and
  backports alike), so `Versioning.Prerelease` is false for every version and `includePrereleases` has no effect on the format
  (Q-DEB-17).
- A remote repository serves what its upstream lists under any suite and component name APT can request; an upstream entry
  whose package name or version breaks the grammar is left out of the parsed rows and of a virtual merge and counted
  (R-DEB-023, R-DEB-034).

Acceptance criteria:

- Unit table of the version order, among others: `1.0~rc1` < `1.0` < `1.0-1` < `1.0+b1` < `1.1~` < `1.1` < `1:0.1`, and `2.10-3`
  equals `0:2.10-3`.
- Uploading a package whose control file says `Package: Hello` answers 422 `deb_invalid_control` naming the field `Package`; a
  version `1.0:2` (a colon without an epoch) the same naming `Version`; `distribution=Stable` answers 400 `deb_invalid_coordinate`.

### R-DEB-003 · Repository layout and request classification
Priority: **P2** · Covers: PKG-DEB-002, PKG-DEB-013

| Path below `<repo URL>` | Classes | Route kind | Content |
|---|---|---|---|
| `dists/<suite>/InRelease` | all | `Metadata` | the `Release` text with inline signatures (R-DEB-016, R-DEB-019) |
| `dists/<suite>/Release`, `dists/<suite>/Release.gpg` | all | `Metadata` | the unsigned `Release` and its detached signatures |
| `dists/<suite>/<component>/binary-<arch>/Packages.gz`, `….xz` | local, virtual | `Metadata` | the index of the suite, component and architecture (R-DEB-011) |
| `dists/<suite>/<…>/by-hash/SHA256/<hex>` | all | `Download` | an index file by its SHA-256 (R-DEB-012, R-DEB-021) |
| any other path below `dists/<suite>/` | remote | `Metadata` | an index file the stored release lists, a pdiff patch below a `*.diff/` directory, else 404 (R-DEB-021) |
| `pool/<…>` | all | `Download` | package files; in a local repository at the canonical path of R-DEB-008; a virtual repository resolves it by the artifact tiers (R-DEB-027) |
| `members/<memberKey>/<path>` | virtual | `Download` | a package file of a member, by the member's path (R-DEB-025, R-DEB-027) |
| `<dir>/InRelease`, `<dir>/Release`, `<dir>/Release.gpg` outside `dists/` | remote | `Metadata` | the release documents of a flat repository whose source line names `<dir>/` as its suite; `<dir>` may be empty (the root, `./`) (Q-DEB-23, Q-DEB-39) |
| `<dir>/<index name>` outside `dists/`, where the file name is an index name: `Packages`, `Sources`, `Contents-<arch>`, `Translation-<lang>`, `Index` of a `*.diff/` directory, each optionally with `.gz`, `.xz`, `.bz2`, `.lzma` or `.zst` | remote | `Metadata` | an index file of a flat repository (R-DEB-021) |
| `<dir>/<file>` outside `dists/` with any other file name | remote | `Download` | a package file of a flat repository |
| `signing-key.asc` | all | `Computed` | the public keys clients trust (R-DEB-018) |
| `upload/<file>` (`PUT`) | local; virtual with a deploy target | `Upload` | R-DEB-006, R-DEB-028 |

- **Classify.** A path below `pool/` or `members/<memberKey>/` whose file name has the form `<package>_<version>_<arch>.<ext>`
  belongs to the package and the version of its name (the version without its epoch, which file names omit). Every other path
  (release documents, index files, by-hash files, `signing-key.asc`) belongs to no package: it is a coordinate of the
  repository without a package name, which only grants without package patterns cover (R-DEB-004, Q-DEB-20). A pool path whose
  file name does not follow the form has no package and gets the part of its file name before the first `_` as its routing
  name, so that patterns and claims still protect it (R-REPO-003).
- Classification is by path and file name only, never by stored state (R-ARCH-008): a `.deb` in a flat directory is a package
  file, an index name in a flat directory an index file.
- **The flat root.** For `deb <repo URL> ./` APT requests `<repo URL>/./InRelease` (verified, see
  [Client and library behaviour](#client-and-library-behaviour-this-page-relies-on)). `net/http.ServeMux` would redirect a path
  with a dot segment; the dispatcher instead hands the format's routes the path as received when its first segment after the
  repository address is `.`, and the format reads a leading `./` as the root flat directory. Any other dot or empty segment
  answers 400 `invalid_path` (Q-DEB-39, Q-ARCH-385).
- Local and virtual repositories answer 404 for any path below `dists/` that is not a file of their published generation; no
  path below `dists/` is ever an upload target (R-ARCH-012). The route table has one route per path shape, not one per class
  (`deb.dists.get`, `deb.dists.by_hash`, `deb.pool.get`), because the format's `Match` is blind to the class of the repository and two
  routes that overlap in path would match one request twice; the decision "not a file of the generation" is the handler's
  (Q-DEB-45).
- Release documents and index files of a local or virtual repository are generated items (R-DEB-010); package files are items
  of the upload pipeline; a remote's files are cache entries (R-REPO-025).

Acceptance criteria:

- The route table's examples (R-ARCH-010 check 1) cover every row; `Classify` maps `pool/h/hello/hello_2.10-3_amd64.deb` to
  package `hello`, version `2.10-3`, and `dists/stable/main/binary-amd64/Packages.gz` to no package.
- `GET <repo URL>/dists/stable/main/binary-amd64/Packages.bz2` of a local repository answers 404.
- `Classify` maps `binary/trove-hello_1.0_all.deb` of a remote to a package file and `binary/Packages.gz` to an index file;
  `GET <repo URL>/./InRelease` of a remote whose `remote.url` is a flat directory answers its `InRelease` without a redirect, and
  `GET <repo URL>/a/./b` answers 400.

### R-DEB-004 · Permissions per operation
Priority: **P2** · Covers: trove-only

The coordinate of every request and the actions it needs (R-AUTHZ-007 rules; this table joins it for the format):

| Client operation | Coordinate | Actions | Ownership |
|---|---|---|---|
| `apt-get update`: release documents, index files, by-hash files; `signing-key.asc` | repository path without a package (only grants without package patterns cover it, Q-DEB-20) | `read` | — |
| The same on a virtual repository | as above, on the virtual repository and on every member (R-DEB-027) | `read` | — |
| Package file download (`apt-get install`, `apt-get download`, `apt-get source` through a remote) | name, path | `read`; a remote fill also its `cacheFillAction` (R-AUTHZ-008) | — |
| `members/<memberKey>/<path>` on a virtual repository | the member's coordinate | `read` on the virtual repository (gate) and on the member; a remote member's `cacheFillAction` for a fill | — |
| Upload of a new package file | name, version | `deploy`; for the first version of the name in the repository also the shadowing check (R-REPO-048) | yes |
| Upload of the same bytes again (adds coordinates) | name, version | `deploy` | yes |
| Upload of other bytes for a stored package, version and architecture | — | refused by the format: 409 `deb_version_exists` (Q-DEB-4) | — |
| Coordinates of a stored file: adding / removing (R-DEB-009) | name, version | `deploy` / `delete` | yes |
| Delete a package file | name, path | `delete` | yes |
| Index state, with or without waiting (R-DEB-014) | repository level | `read` | — |
| Reindex (R-REPO-016) | repository level | as R-REPO-016 (`manage`) | — |
| Signing keys: list and public certificates / create, promote or remove the next key, confirm or reinstate a withheld key (R-DEB-015, R-DEB-017, R-DEB-032) | repository level | `read` / `manage` | — |

- Index paths carry no package because an index lists every package of its suite: a grant narrowed to package patterns would
  otherwise read the names, versions and control data of packages it may not read. A principal with only package-scoped `read`
  can download the package files it may read but cannot use the repository as an APT source (Q-DEB-20).
- Raw access through `/files/` maps as R-AUTHZ-007 says; generated files are read-only (Q-STOR-181).

Acceptance criteria (`TestR_AUTHZ_007_DebActionMap`, one test per row, principals with exactly the action against one without
it, repositories with `packageOwnership: off`):

- With `read` and `deploy` but not `delete` on `deb-local`, an upload succeeds, adding a coordinate succeeds, removing one answers
  403 and `DELETE <repo URL>/pool/…` answers 403.
- A principal with `read` on `deb-local` narrowed to `packages.include: ["acme-*"]` gets 404 for `dists/stable/InRelease` and 200
  for `pool/a/acme-tool/acme-tool_1.0_amd64.deb`.
- With `read` but not `manage`, `POST …/deb/signing-keys` answers 403 and `GET …/deb/signing-keys` 200.

### R-DEB-005 · Errors and response headers
Priority: **P2** · Covers: trove-only

APT shows the status line of a failed request and does not read bodies; other tools (`curl`, CI scripts) do. The format's
`WriteError` (R-ARCH-008) therefore answers standard status codes with the problem details of R-ARCH-023 and these codes:

| Status | Code | When |
|---|---|---|
| 400 | `deb_distribution_required`, `deb_invalid_coordinate`, `deb_invalid_file_name`, `invalid_path` | upload parameters (R-DEB-006); a dot segment other than the flat root (R-DEB-003) |
| 403 | `deb_members_unreadable` | a virtual's index read by a caller who cannot read every member (R-DEB-027) |
| 404 | `deb_not_in_release`, `deb_no_upstream_keys` | a remote index path the stored release does not list (R-DEB-021); `signing-key.asc` of a remote without keys (R-DEB-018) |
| 403, 405 | `forbidden`, `no_deploy_target` | an upload through a virtual repository without a deploy target: the decision point refuses `deploy` on it with 403 before the format runs, and a target that is gone by then answers 405 (R-DEB-028, Q-DEB-67) |
| 409 | `deb_version_exists`, `deb_filename_conflict`, `deb_withheld`, `generated_item`, `deb_no_next_key`, `deb_key_in_use`, `deb_key_state`, `reindex_running` | R-DEB-006, R-DEB-013, R-DEB-014, R-DEB-015, R-DEB-017, R-DEB-032 |
| 413 | `payload_too_large` | a bound of R-DEB-034 (the code of the bounded readers, `untrusted.TooLarge`, Q-DEB-44) |
| 422 | `deb_invalid_archive`, `deb_invalid_control`, `deb_unsupported_compression`, `deb_key_unsupported`, `deb_upstream_keys_required`, `deb_member_unverified`, `deb_mirror_suites_required` | R-DEB-007, R-DEB-015, R-DEB-020, R-DEB-024 |
| 501 | `deb_not_implemented` | a route whose work package is not built yet (the skeleton of WP0) |
| 502 | `deb_upstream_signature_invalid`, `deb_release_invalid`, `deb_index_mismatch`, `upstream_integrity_mismatch`, `upstream_digest_missing` | R-DEB-019 to R-DEB-022 |
| 503 | `shadow_check_unavailable` | a first upload whose shadowing check is undetermined (R-DEB-023, R-REPO-048) |

Response headers:

| Files | `Content-Type` | `Cache-Control` |
|---|---|---|
| `InRelease`, `Release` | `text/plain; charset=utf-8` | `private, max-age=0, must-revalidate` |
| `Release.gpg` | `application/pgp-signature` | as above |
| `Packages.gz`, `Packages.xz` at their plain path | `application/gzip`, `application/x-xz` | as above |
| by-hash files, package files | by file type; `.deb` is `application/vnd.debian.binary-package` | `private, max-age=31536000, immutable` |
| `signing-key.asc` | `application/pgp-keys` | `private, max-age=0, must-revalidate` |

- Every file is an item or a cache entry, so `ETag` (the SHA-256), `Last-Modified`, conditional requests and `Range` follow
  [storage.md](../storage.md) R-STOR-012; APT revalidates `InRelease` with `If-Modified-Since`.
- Responses are always `private` (the anonymous principal's read cannot be asked at response time, as for npm, Q-NPM-28).
- A package file served through a virtual repository carries `Trove-Resolved-From: <memberKey>` (R-REPO-019).

Acceptance criteria:

- An upload whose body is no `ar` archive answers 422 with an `application/problem+json` body whose `code` is
  `deb_invalid_archive`.
- `HEAD` of a by-hash file answers `Cache-Control: private, max-age=31536000, immutable` and the item's `ETag`; `HEAD` of
  `dists/stable/InRelease` answers `private, max-age=0, must-revalidate`.

### R-DEB-006 · Uploading a binary package
Priority: **P2** · Covers: PKG-DEB-004, PKG-DEB-005

A package file is uploaded with one request (Q-DEB-3):

```sh
curl -fsS -u "ci-bot:${TROVE_TOKEN}" --upload-file ./hello_2.10-3_amd64.deb \
  "https://trove.example.com/api/deb/web/deb-local/upload/hello_2.10-3_amd64.deb?distribution=stable&component=main"
```

- `PUT <repo URL>/upload/<file>` with the `.deb` as body. `<file>` is informational and must end in `.deb` (400
  `deb_invalid_file_name` otherwise); trove names the stored file itself (R-DEB-008). `distribution` is required (400
  `deb_distribution_required`), `component` defaults to `main`; both may repeat and may hold comma-separated values, at most 16
  of each per request, and the file gets every combination as a coordinate (R-DEB-009). A value outside the grammar of R-DEB-002
  answers 400 `deb_invalid_coordinate`.
- The architecture comes from the control file, never from the request.
- **Steps.** The coarse check `deploy`; the upload pipeline (admission, spool, digests, R-STOR-004); reading the control data
  (R-DEB-007); the fine check of `deploy` on the declared name and version, ownership (R-AUTHZ-018) and, for the first version of
  the name in the repository, the shadowing check (R-REPO-048); then one transaction commits the item, the package and version
  rows, the format's rows, raises `change_seq` of every suite the file is listed in and enqueues the index build (R-DEB-010).
- **Answers.** 201 for a new file; 200 when the identical bytes are stored already for the package, version and architecture, in
  which case the coordinates of the request are added to the stored ones (Q-DEB-4). The body:

```json
{
  "path": "pool/h/hello/hello_2.10-3_amd64.deb",
  "package": "hello", "version": "2.10-3", "architecture": "amd64",
  "sha256": "5f1c…",
  "coordinates": [{"distribution": "stable", "component": "main"}],
  "index": [{"distribution": "stable", "changeSeq": 42, "publishedSeq": 41}]
}
```

- **Refusals.** Other bytes for a stored package, version and architecture answer 409 `deb_version_exists`, also for a holder of
  `overwrite`: delete the file first (R-DEB-013). Another version whose file name is the same (versions that differ only by
  their epoch) answers 409 `deb_filename_conflict`. Bytes a restore's replay withholds answer 409 `deb_withheld` (R-DEB-032).
- **No wait.** The answer is sent when the commit is durable and never waits for the index (R-ARCH-012 rule 3, Q-DEB-25). A
  client that must see the package in the index waits with the index state read of R-DEB-014, using `changeSeq` from the
  answer.

Acceptance criteria:

- The upload above answers 201 with `changeSeq` n; `GET …/deb/index?waitFor=stable:n` then answers `publishedSeq` ≥ n, and
  `apt-get update && apt-get install hello=2.10-3` through `deb-local` succeeds.
- The same bytes again with `distribution=testing` answer 200 and list the coordinates `stable/main` and `testing/main`.
- Other bytes for `hello 2.10-3 amd64` answer 409 `deb_version_exists` for a principal with `deploy` and `overwrite`.
- `distribution=stable,testing&component=main&component=contrib` gives four coordinates; no `distribution` answers 400.

### R-DEB-007 · Reading a package file and its control data
Priority: **P2** · Covers: PKG-DEB-009, PKG-DEB-010

- **Archive.** A `.deb` is an `ar` archive ([deb(5)](https://manpages.debian.org/stable/dpkg-dev/deb.5.en.html)): the first
  member is `debian-binary` holding `2.<n>` and a newline; then `control.tar`, `control.tar.gz`, `control.tar.xz` or
  `control.tar.zst`; then `data.tar` with any compression; members whose names start with `_` (signatures of package signing
  tools) may follow and are not read. Another order, a duplicate or unknown member, or a control archive in another compression
  answers 422 `deb_invalid_archive` or `deb_unsupported_compression`. The data archive is never decompressed.
- **Control file.** Only the member `./control` (or `control`) of the control archive is read; it must be a regular file. It is
  one deb822 paragraph ([Debian Policy, chapter 5](https://www.debian.org/doc/debian-policy/ch-controlfields.html)):
  `Package`, `Version`, `Architecture`, `Maintainer` and `Description` are required; names follow R-DEB-002.
- **Strict syntax, because trove copies the paragraph into its indexes** (R-DEB-034): field names match
  `^[A-Za-z][A-Za-z0-9-]{0,63}$` and appear once (compared without case); values are UTF-8 without control characters other than
  tab; a continuation line starts with a space or a tab and is not blank after it (` .` stands for an empty line of a
  description); exactly one paragraph. A violation answers 422 `deb_invalid_control` naming the field, and nothing is stored: a
  package trove cannot index is refused, never stored unindexed (Q-DEB-4).
- **Normalized paragraph.** The fields in their order, without the fields trove computes for an index (`Filename`, `Size`,
  `MD5sum`, `SHA1`, `SHA256`, `SHA512`, `Description-md5`), at most 1 MiB. It is stored in the row of the file (R-DEB-008) and is
  what the index lists.
- **Identity.** The declared package and version are the coordinate of authorization (R-AUTHZ-005) and the package and version
  rows.
- **Properties** (read-only, below `deb.`, FMT-008): `deb.package`, `deb.version`, `deb.architecture`, `deb.source`, and one
  `deb.coordinate` value `<distribution>/<component>` per coordinate.

Acceptance criteria (unit tests over crafted files and integration over HTTP):

- A control file with two paragraphs, with a blank continuation line, with `Package` twice, or with a field name `Pack age`
  answers 422 `deb_invalid_control`; one with `Filename: ../../etc/passwd` is accepted and the index lists the canonical
  `Filename`, not the declared one.
- A package built by `dpkg-deb` with `control.tar.zst` (the Ubuntu default) and one with `control.tar.xz` (the Debian default)
  are accepted; one with `control.tar.bz2` answers 422 `deb_unsupported_compression`.
- The item API shows `deb.package=hello`, `deb.version=2.10-3`, `deb.architecture=amd64` and `deb.coordinate=stable/main`.

### R-DEB-008 · Storage model and the format's tables
Priority: **P2** · Covers: PKG-DEB-002

- **Canonical path** (Q-DEB-5): `pool/<prefix>/<source>/<package>_<version without epoch>_<architecture>.deb`, where `<source>`
  is the name in the `Source` field (without a version in parentheses) or, without one, the package name, and `<prefix>` is the
  first character of `<source>`, or its first four characters when it starts with `lib`, as the Debian archive lays out its pool.
- **Items and packages.** The package is the `Package` field (`packages.name`), the version the `Version` field with its epoch
  (`package_versions.version`); each architecture's file is an item of that version. The first file of a version is its lead
  item; a file of another architecture uploaded later joins the version in its own commit (R-STOR-039).
- **Generated items.** A local and a virtual repository hold the files of their published index generations as generated items
  (`items.generated`, no package, no version), written only by the index build (R-DEB-010, Q-ARCH-380, Q-STOR-180).
- **The format's tables** (data port `internal/formatdata/deb`). The rows are written by the storage hooks in the transaction
  that writes the items; every change of a package raises its `rev_seq` (R-ARCH-009) and every change of a suite's content its
  `change_seq`. They carry no blob reference: the items hold the blobs (R-STOR-003). The signing keys are R-DEB-015, the
  remote's rows R-DEB-023.

```sql
CREATE TABLE deb_suites (                   -- a suite of a local or virtual repository
  repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  suite         text COLLATE "C" NOT NULL CHECK (suite ~ '^[a-z0-9][a-z0-9._+-]{0,63}$'),
  change_seq    bigint NOT NULL DEFAULT 1,   -- raised by every change of the suite's content or keys (revision column)
  built_seq     bigint NOT NULL DEFAULT 0,   -- change_seq the published generation was built from (revision column)
  generation    bigint NOT NULL DEFAULT 0,   -- number of the published generation
  release_date  timestamptz,                 -- Date of the published Release
  published_at  timestamptz,
  state         text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'current', 'failed')),
  reason        text CHECK (length(reason) <= 256),
  inputs        jsonb,                       -- virtual: fingerprint of each member's input (R-DEB-026)
  checked_at    timestamptz,                 -- virtual: last check of the members
  PRIMARY KEY (repo_id, suite)
);

CREATE TABLE deb_binaries (                 -- one row per stored package file of a local repository
  item_id        bigint PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
  repo_id        bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  package        text COLLATE "C" NOT NULL,
  version        text COLLATE "C" NOT NULL,
  architecture   text COLLATE "C" NOT NULL,
  source         text COLLATE "C" NOT NULL,
  control        text NOT NULL CHECK (octet_length(control) <= 1048576),  -- the normalized paragraph (R-DEB-007)
  UNIQUE (repo_id, package, version, architecture)
);

CREATE TABLE deb_coordinates (              -- the suites and components a file is listed in (R-DEB-009)
  item_id    bigint NOT NULL REFERENCES deb_binaries (item_id) ON DELETE CASCADE,
  repo_id    bigint NOT NULL,
  suite      text COLLATE "C" NOT NULL,
  component  text COLLATE "C" NOT NULL CHECK (component ~ '^[a-z0-9][a-z0-9._+-]{0,63}$'),
  PRIMARY KEY (item_id, suite, component),
  FOREIGN KEY (repo_id, suite) REFERENCES deb_suites (repo_id, suite) ON DELETE CASCADE
);
CREATE INDEX deb_coordinates_suite ON deb_coordinates (repo_id, suite, component);

CREATE TABLE deb_generations (              -- the published and the retained generations of a suite (R-DEB-012)
  repo_id        bigint NOT NULL,
  suite          text COLLATE "C" NOT NULL,
  generation     bigint NOT NULL,
  built_seq      bigint NOT NULL,
  published_at   timestamptz NOT NULL DEFAULT now(),
  superseded_at  timestamptz,
  files          jsonb NOT NULL,             -- {"<path relative to dists/<suite>/>": "<sha256 hex>", …}
  signed_by      text[] NOT NULL,            -- fingerprints of the keys that signed it
  PRIMARY KEY (repo_id, suite, generation),
  FOREIGN KEY (repo_id, suite) REFERENCES deb_suites (repo_id, suite) ON DELETE CASCADE
);

CREATE TABLE deb_tombstones (               -- a package file the journal replay withheld (R-DEB-032); written by the replay only
  repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path         text COLLATE "C" NOT NULL,
  sha256       bytea NOT NULL CHECK (octet_length(sha256) = 32),
  withheld_at  timestamptz NOT NULL DEFAULT now(),
  deleted_by   uuid,                         -- the principal of the journaled deletion
  PRIMARY KEY (repo_id, path)
);
```

- `deb_suites.change_seq` and `built_seq` are registered revision columns (R-ARCH-009): clients hold `changeSeq` values for the
  index state read (R-DEB-014), and a restore raises both by 2^32 (R-DEB-032).
- **Settings** of the member `deb` (R-REPO-004; the remote's mirror members are R-DEB-024):

| Member (`deb.…`) | Classes | Meaning | Default (proposed) | Range |
|---|---|---|---|---|
| `architectures` | local, virtual | Architectures every suite's indexes are built for; a local repository adds the architectures of its stored files | `["amd64", "arm64"]` | 1–32 names of R-DEB-002, not `all` |
| `compression` | local, virtual | Index variants besides gzip, which is always built | `[]` | subset of `["xz"]` |
| `origin` | local, virtual | `Origin` of every `Release` | `"trove"` | 1–64 characters, printable ASCII without `:` |
| `label` | local, virtual | `Label` of every `Release` | the repository key | as `origin` |
| `suites` | virtual | Suites served besides those of the local members | `[]` | 0–32 names of R-DEB-002 |
| `components` | virtual | Components merged | `["main"]` | 1–32 names of R-DEB-002 |
| `upstreamKeys` | remote | ASCII-armored OpenPGP public keys of the upstream (R-DEB-020) | `null` | at most 256 KiB, 1–32 certificates |
| `mirror` | remote | R-DEB-024 | | |

- A change of `architectures`, `compression`, `origin`, `label`, `suites` or `components` raises `change_seq` of every suite of
  the repository in its transaction, so the next build reflects it.

Acceptance criteria:

- `hello 2.10-3 amd64` (source `hello`) is stored at `pool/h/hello/hello_2.10-3_amd64.deb`, `libxml2-utils 2.9.14+dfsg-1.3 amd64`
  (source `libxml2`) at `pool/libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb`; `GET /files/web/deb-local/<path>` returns the
  uploaded bytes.
- A remote created without `deb` reads back `"deb": {"upstreamKeys": null, "mirror": {…the defaults of R-DEB-024…}}`; a local
  one `"deb": {"architectures": ["amd64", "arm64"], "compression": [], "origin": "trove", "label": "deb-local"}`.

### R-DEB-009 · Coordinates: suites and components of a package file
Priority: **P2** · Covers: PKG-DEB-005, PKG-DEB-006

- A file is listed in the `Packages` index of every suite and component it has as a coordinate, under its architecture. A file
  of architecture `all` is listed under every architecture of the suite and in the suite's `binary-all` index.
- The architectures of a suite are `deb.architectures` plus the architectures of its stored files, at most 32. An index of an
  architecture without files is built empty, so that a client of that architecture can still install the suite's `all` files.
- **Changing coordinates.** Uploads add coordinates (R-DEB-006).
  `PUT /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/coordinates` with
  `{"path": "pool/h/hello/hello_2.10-3_amd64.deb", "coordinates": [{"distribution": "stable", "component": "main"}]}` replaces a
  file's coordinates: an added coordinate needs `deploy`, a removed one `delete`, both with ownership (R-DEB-004). An empty list
  answers 422 `validation_failed` with the field error `invalid_value` at `/coordinates` (a file without coordinates is deleted instead). It answers 200 with the body of R-DEB-006. The decisions are taken before the transaction from the file as it was read; if the file's coordinates changed meanwhile so that the change differs, the request answers 409 `conflict` and is applied by nobody. This
  is how a package moves from one suite to another (`testing` to `stable`).
- **A removed coordinate is journaled** (Q-DEB-34). Pulling a package out of a suite narrows what clients get, so the change
  writes `deb.coordinates.removed` with a journal entry (repository, path, SHA-256, the removed coordinates, actor) in its
  transaction, and a restore removes them again (R-DEB-032). An added coordinate is not journaled.
- **Bounds.** 64 suites per repository, 32 components per suite, 32 coordinates per file; more answers 422 `validation_failed` with the
  field error `out_of_range` at `/coordinates` (Q-DEB-49). A request names at most 64 suites, 32 components and 32 coordinates; the
  bounds of the repository and of a suite are checked in the transaction after the rows are written, so a crossing rolls the change back.
- A suite stays while a file is listed in it; a suite without files keeps its last generation with empty indexes until a
  reindex drops it (R-DEB-014).

Acceptance criteria:

- An `all` file with the coordinate `stable/main` appears in `binary-amd64`, `binary-arm64` and `binary-all` of `stable/main`,
  with the same paragraph.
- After the coordinates of a file change from `testing/main` to `stable/main`, the next published generation of `testing` no
  longer lists it and that of `stable` does.
- A principal with `deploy` but not `delete` adds `stable/contrib` to a file and gets 403 removing `stable/main`.

### R-DEB-010 · Index builds: a job, a consistent snapshot, a delay bound
Priority: **P2** · Covers: PKG-DEB-008

The indexes of a suite are signed and stored in several compressed variants, so they are built by a job, the exception of
[architecture.md](../architecture.md) R-ARCH-012 item 2 (Q-DEB-6):

- **The job.** Kind `deb-index`, queue `index`, `coalesce` under the key `index:<repository id>` (the key R-REPO-016 uses), enqueued
  in the transaction of every change of a suite with the delay `deb.index_delay`. A change during a build leaves one job waiting,
  which builds what the running one missed. An enqueue whose run time is earlier than the waiting job's moves the waiting job to
  that earlier time, so "without delay" never waits behind a delayed job (Q-ARCH-384, built in the job queue).
- **A build** reads, in one `REPEATABLE READ` snapshot, the suites whose `change_seq` exceeds their `built_seq` (every suite for a
  full build) with their rows; renders each suite's files (R-DEB-011); compresses and signs them (R-DEB-016); and hands them to
  the **publication port** of the item store (Q-STOR-182, ADR-170), which writes every new file's blob (R-STOR-005) outside any
  transaction and then commits **one transaction** that upserts the generated items at their plain paths and at the by-hash
  paths of **every** file of the generation (also those an earlier generation stored already, so no file of the generation can be
  missing after the commit), removes the by-hash paths retention gives up (R-DEB-012), and runs the build's own writes: it
  inserts the `deb_generations` rows, marks the previous generations superseded and sets `built_seq`, `generation`,
  `release_date`, `published_at` and `state` of each suite, each only while the suite's `built_seq` is still below the
  `change_seq` the build read. A slower build of an older snapshot therefore never replaces a newer generation: its transaction
  rolls back.
- **Last-Modified.** Every item a generation writes carries the `Release` `Date` as its `modified_at`, so two generations never
  share a `Last-Modified` and APT's `If-Modified-Since` never gets a stale 304 (Q-STOR-182, R-STOR-012).
- **What readers see.** A reader sees a suite's previous generation or its new one, never a mixture, because one transaction
  publishes every file of a generation. A client that read `InRelease` before the commit fetches the index files that
  `InRelease` names by hash (R-DEB-012), on any replica. Every replica serves the same bytes: they are items, not renderings.
- **Date** (Q-DEB-30). APT refuses a `Date` more than 10 s ahead of the client's clock, and `sqv` refuses a signature made after
  the client's now (both verified). The `Date` of a new `Release` is therefore the build's PostgreSQL time minus 60 s, in whole
  seconds, and at least the previous `Date` plus one second, so it never goes backwards for a suite, also across a database
  failover. When the previous `Date` plus one second would lie more than one second after the PostgreSQL time minus 60 s (more
  than one build per second for a while), the build waits until it does not, so a `Date` is never later than the PostgreSQL time
  minus 59 s. A client whose clock trails the database's by up to 59 s accepts every `Release` and signature. One that trails by
  more fails until its clock catches up, differently per verifier (the guide's troubleshooting, R-DEB-029): on Debian 13, whose
  APT checks the signature with `sqv` before the `Date`, from about 60 s behind with "OpenPGP signature verification failed … created
  after the --not-after date … is not signed"; on the `gpgv` clients (Debian 12, Ubuntu 22.04 and 24.04), which accept a signature
  a few seconds ahead and a `Date` up to 10 s ahead, from about 70 s behind with "Release file … is not valid yet". The thresholds
  follow from the verified limits; they are not measured.
- **Failures.** A build holds no transaction while it renders, compresses, signs or writes blobs (R-ARCH-019). A failed build
  publishes nothing: the suite keeps its generation and gets `state: failed` with the reason; the job is retried by R-ARCH-016,
  and the next change enqueues again. A drain or a crash during a build releases or expires the job, and another worker builds
  from the rows.
- **Keys and settings.** A key change (R-DEB-017) and a settings change (R-DEB-008) raise `change_seq` of every suite and enqueue
  a build without delay, so trove re-signs at once.
- **Retention and housekeeping run only inside this job** (Q-DEB-33): the by-hash files a generation gives up are removed by the
  commit of a later build of the same repository, and the periodic task `deb-index-refresh` only enqueues a `deb-index` job (for
  a repository whose retention is due, a virtual repository whose check is due, or a remote's housekeeping, R-DEB-021); it
  deletes nothing itself. Every removal is therefore serialized with every publication under `index:<repository id>`.
- A build never reads a package file: everything it lists comes from rows.
- **Delay bound** (R-ARCH-012 item 2; measured from `deb_suites.dirty_since`, the first change no build published yet, Q-DEB-58): from the answer of an upload to the commit of a generation that lists it, p99 at most
  `deb.index_delay` + 5 s while Valkey is available and `deb.index_delay` + `jobs.poll_interval` + 5 s while it is not, for a
  local repository of 10,000 package files in three suites under ten uploads per second spread over three replicas (reference
  sizes, proposed). `trove_deb_index_delay_seconds` measures it.

Configuration (`trove.yaml`):

```yaml
deb:
  index_delay: 2s              # default (proposed); 1s–60s: wait after the first change before a build starts
  virtual_index_interval: 60s  # default (proposed); 10s–1h: least time between two builds of a virtual repository (R-DEB-026)
  by_hash_keep: 1h             # default (proposed); 10m–7d: how long a superseded generation's files stay (R-DEB-012)
  index_wait_max: 30s          # default (proposed); 1s–120s: the longest wait of an index state read (R-DEB-014)
```

Acceptance criteria:

- Fifty uploads of distinct packages into one suite, in parallel through three replicas: every package is listed in the
  published `Packages` of the suite; `generation` grows monotonically; fewer builds ran than uploads.
- A build stopped between its blob writes and its commit (fault injection) leaves the previous generation served byte for byte;
  the next build publishes.
- With Valkey stopped, an upload is published within `deb.index_delay` + `jobs.poll_interval` + 5 s.
- Every published `Release` has a `Date` at least 30 s before the PostgreSQL time of the assertion and later than the previous
  generation's; twenty key changes in five seconds publish `Date`s that are never later than the PostgreSQL time minus 59 s; the
  `Last-Modified` of `InRelease` equals its `Date`.
- A by-hash item that a test deletes between two builds is present again after the next build that lists it (the commit upserts
  every by-hash item of its generation).
- `TestR_DEB_010_DelayBound` holds the bound under the reference load in the nightly performance run (`make test-performance` with
  `TROVE_PERF_GATE=1`, [architecture.md](../architecture.md) Q-ARCH-293, Q-DEB-59); per change it asserts that every upload of a smaller load is published
  and the p50.

### R-DEB-011 · Index content: Packages, Release and their variants
Priority: **P2** · Covers: PKG-DEB-006, PKG-DEB-011, PKG-DEB-014, PKG-DEB-015

- **`Packages`** of a suite, component and architecture: one paragraph per listed file, the file's normalized paragraph
  (R-DEB-007) followed by `Filename` (the canonical path; through a virtual repository the member path of R-DEB-025), `Size`,
  `MD5sum` and `SHA256`. A local repository orders the paragraphs by package name, version (dpkg order) and architecture; a
  virtual repository by member order and, within a member, in the member's own order, because it merges by streaming
  (R-DEB-025, Q-DEB-38). Equal inputs give equal bytes. The `Description` is the full multi-line value; there is no
  `Description-md5`.
- **Variants** (Q-DEB-7): `Packages.gz` always (gzip without file name and with modification time 0, so equal content gives
  equal bytes); `Packages.xz` when `deb.compression` lists `xz`. No uncompressed file, no bzip2, LZMA or zstd variant.
- `binary-all/Packages…` of every component lists the suite's `all` files.
- Local and virtual repositories build no `Sources`, `Contents-*`, `i18n/Translation-*`, per-component `Release` or pdiff files
  (Q-DEB-7, Q-DEB-28).
- **`Release`** of a suite:

```text
Origin: trove
Label: deb-local
Suite: stable
Codename: stable
Date: Sun, 08 Nov 2026 10:00:00 UTC
Architectures: amd64 arm64
Components: contrib main
Acquire-By-Hash: yes
Description: web/deb-local stable
MD5Sum:
 9a1e…  1834 contrib/binary-all/Packages.gz
 …
SHA256:
 4c2b…  1834 contrib/binary-all/Packages.gz
 …
```

  `Origin` and `Label` come from the settings (R-DEB-008); `Architectures` lists the suite's architectures without `all` (APT
  finds the `all` files in every architecture's index); every index file of the suite is listed under both checksum fields with
  its size, sorted by path, and with them the size and checksums of each `Packages` before its compression, which is not a file of the
  repository (Q-DEB-56). A local repository's `Release` has no `Valid-Until`
  ([H-624](#h-624--troves-own-release-files-carry-no-valid-until)); a virtual repository's carries the earliest `Valid-Until` of
  the member releases it merged, when any has one (R-DEB-025, Q-DEB-32). There is no `Signed-By` field.
- `InRelease` and `Release.gpg` are R-DEB-016.

Acceptance criteria:

- The integration test parses a published `Packages.gz`: the fields of each paragraph are the uploaded control fields in order,
  then `Filename`, `Size`, `MD5sum` and `SHA256` equal to the item's; `Release` lists every index file with matching size and
  digests.
- Two full builds from unchanged rows publish byte-identical `Packages.gz` and `Packages.xz`.
- With `deb.compression: ["xz"]`, `apt-get update` in Debian 13 fetches `Packages.xz` by hash.

### R-DEB-012 · By-hash files and their retention
Priority: **P2** · Covers: PKG-DEB-013

- Every index file of a generation is also a generated item at `<directory>/by-hash/SHA256/<hex>` (the same blob), and the
  `Release` says `Acquire-By-Hash: yes`. APT then fetches every index file by its SHA-256, so the file it gets after reading an
  `InRelease` is the one that `InRelease` names, whichever replica answers and whichever generation is current by then.
- **Retention.** A by-hash file stays while a kept generation names it: the current one, the superseded ones for
  `deb.by_hash_keep` after they were superseded, and the three newest superseded ones whatever their age (Q-DEB-8, Q-DEB-51). Files no kept generation names
  are removed only in the commit of a `deb-index` job of the repository (R-DEB-010, Q-DEB-33): the next build, or the job the
  periodic task `deb-index-refresh` enqueues when a repository's retention is due and no change triggers a build.
- Only SHA-256: APT asks for the strongest hash a `Release` lists; `MD5Sum` in the `Release` serves other tools.
- A plain path serves the current generation's file.

Acceptance criteria:

- A client reads `InRelease` of generation n; three builds publish n+1 to n+3 within ten seconds; its by-hash requests for
  generation n's files answer 200 on each replica. After `deb.by_hash_keep` and a further build they answer 404, while those of the
  last three generations still answer 200.

### R-DEB-013 · Deleting packages
Priority: **P2** · Covers: PKG-DEB-006

- `DELETE <repo URL>/pool/<path>` (format route), `DELETE /files/<projectKey>/<repoKey>/pool/<path>` and the item API (R-STOR-018)
  remove a package file; it goes to the trash can (R-STOR-022) unless purged. The removal hook raises `change_seq` of every suite
  the file is listed in and enqueues the build; the rows stay while the item is in the trash can (a build lists files that are not
  trashed) and go with its permanent removal. A restore from the trash can (R-STOR-023, the `ItemsRestorer` hook) raises the
  suites' `change_seq` again.
- The deletion writes the audit event `deb.package.deleted` in its transaction, with an entry in the revocation journal
  (R-DEB-032).
- Removing the last file of an architecture leaves that architecture's index while the architecture is in `deb.architectures`
  or another file has it.
- A generated item cannot be deleted (409 `generated_item`, Q-STOR-181); a virtual repository answers 405 (R-STOR-018).

Acceptance criteria:

- After `DELETE <repo URL>/pool/h/hello/hello_2.10-3_amd64.deb`, the next generation does not list it and `apt-get install
  hello=2.10-3` fails with "Version '2.10-3' for 'hello' was not found"; after a restore from the trash can it is listed again.
- `DELETE /files/web/deb-local/dists/stable/InRelease` answers 409 `generated_item`.

### R-DEB-014 · Index state and recalculation
Priority: **P2** · Covers: PKG-DEB-016

| Method and path | Purpose | Permission |
|---|---|---|
| `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/index` | Each suite's state (a virtual repository answers too, Q-DEB-66) | `read` on the repository |
| The same with `?waitFor=<suite>:<changeSeq>[,…]` | Waits until every named suite's `publishedSeq` reaches its value, or `deb.index_wait_max` passed; answers the state either way. A virtual repository has no change number of its own: `waitFor` answers 400 `validation_failed` | `read` |
| `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/reindex` | A full build ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-016): every suite rebuilt from the rows, suites without files dropped, rows of items that no longer exist removed; for a virtual repository the members read again | as R-REPO-016 |

```json
{
  "suites": [
    {"distribution": "stable", "changeSeq": 42, "publishedSeq": 42, "generation": 17,
     "releaseDate": "2026-11-08T10:00:00Z", "state": "current", "signedBy": ["3F2A…9C0D"]},
    {"distribution": "testing", "changeSeq": 9, "publishedSeq": 8, "generation": 5,
     "releaseDate": "2026-11-08T09:58:12Z", "state": "pending"}
  ]
}
```

- The wait holds no transaction: it reads the suite rows at most every 250 ms. It is a read and no upload waits for it
  (Q-DEB-25).
- The reindex endpoint is built with this phase; a format without a job-built index answers 405 `not_applicable` until its own
  rebuild exists (R-REPO-016).
- **A reindex and the automatic build share one key** (Q-ARCH-384). A reindex while an automatic build of the repository is
  waiting upgrades that waiting job to a full build and moves it to now; it answers 202 with that job. Only a reindex that meets a
  waiting or running reindex (a full build requested through this endpoint) answers 409 `reindex_running`. A reindex while an
  automatic build runs waits as the one job behind it.

Acceptance criteria:

- An upload's `changeSeq` n: `GET …/deb/index?waitFor=stable:n` answers within the delay bound of R-DEB-010 with `publishedSeq`
  ≥ n; for a suite that no change reaches it answers after `deb.index_wait_max` with the current state.
- A second `POST …/reindex` while the first is queued or running answers 409 `reindex_running`; a reindex while an automatic
  build waits with a delay of 60 s answers 202 with that job, which runs at once as a full build; after the last file of `testing`
  was deleted, a reindex makes `dists/testing/InRelease` answer 404.

### R-DEB-015 · Signing keys: one per repository, where the private key lives
Priority: **P2** · Covers: PKG-DEB-018, PKG-DEB-019

- **A key per repository from its creation.** Every local and every virtual Debian repository has a primary signing key from
  the transaction that creates it (a creation hook the format's data port registers, Q-REPO-273). There is no unsigned Debian
  repository, no instance-wide default key and no key store shared by name: a key belongs to one repository.
- **The key type** (Q-DEB-10). trove generates an OpenPGP version 4 key whose primary key uses public-key algorithm 22
  (EdDSALegacy) over the curve Ed25519, with the user ID `trove <projectKey>/<repoKey>`, no expiry, signing capability and no
  subkeys. Algorithm 27 (Ed25519 of RFC 9580), which the OpenPGP module writes by default, is never generated or imported:
  GnuPG 2.2 and 2.4, the verifier of Debian 12 and of Ubuntu 22.04 and 24.04, cannot read it (verified, see
  [Client and library behaviour](#client-and-library-behaviour-this-page-relies-on)).
- **The key's creation time** (Q-DEB-30) is the creating transaction's PostgreSQL time minus one hour, so every signature trove
  makes with it is later than the key (R-DEB-016), on every client whose clock trails the database by less than that hour; `sqv`
  refuses a signature made before its key (verified).
- **The binding self-signature carries the key's creation time** (Q-DEB-40). `sqv` checks a signing key against its binding
  self-signature at the time of the data signature, and refuses a data signature older than every binding self-signature even when
  the key itself is older (verified; `gpgv` accepts it). The generator therefore makes the self-signature at the key's creation time
  (the module's `Config.Time`). Every key trove holds records `bound_since`, the creation time of its earliest valid binding
  self-signature: for a generated key the key's creation time, for an imported key whatever its certificate holds (an operator who
  strips an expiry before the import makes GnuPG write a fresh self-signature).
- **Where the private key lives.** In PostgreSQL, sealed with the stored-secret key ring of
  [architecture.md](../architecture.md) R-ARCH-021 (AES-256-GCM, bound to its row and column), in `deb_signing_keys.private_key`,
  registered in the sealed-column registry, so `trove admin secrets status`, the start-up canary and `secrets-reencrypt` cover it
  (the walk is `ResealSigningKeys`, built with the migration, so registering the column never stops a key-ring rotation).
  It is opened only by the index build (R-DEB-010) and by the self-certification at generation or import, in memory, for the
  signing call. It is never written anywhere else: not to a log, to Valkey, to a replica's disk, to an API answer or to the
  configuration export (R-OPS-032 exports no key at all). No API signs data a caller chooses: trove is no signing oracle.
- **Backups** carry the sealed private key (export class `data`), so a restore with the key ring in escrow (R-BAK-028) keeps the
  repository's signature identity. A key an administrator retires has its private key erased (the column is set to `NULL`); its
  public certificate stays, so the record of which key signed which generation stays readable. A restore can bring back the
  sealed private key of a key retired after the snapshot; the journal replay then withholds it and never erases it (R-DEB-032).
- **Import** (for an archive whose clients already trust a key): an ASCII-armored secret key, unprotected or with its passphrase
  in the same request (used once, never stored), whose primary key is EdDSALegacy (algorithm 22) over Ed25519 or RSA of at least
  3072 bits, can sign, is not revoked, has **no expiry**, was **created no later than the PostgreSQL time** of the request, has
  its earliest valid binding self-signature (`bound_since`, Q-DEB-40) **no later than the PostgreSQL time** of the request (a
  self-signature dated ahead would stall every build that signs with the key until that time; refusing it caps the build's wait
  at about 61 s), and has no subkeys that would sign instead. Anything else, algorithm 27 and version 6 keys included, answers 422
  `deb_key_unsupported`. A fingerprint that `deb_signing_keys` holds in **any** state, of this or another repository, answers
  409 `deb_key_in_use`, so a retired or withheld key cannot come back by import (Q-DEB-31). An imported key becomes the next
  key (R-DEB-017); a repository's first key is always generated.
- **States.** `primary` (signs, served in `signing-key.asc`), `next` (co-signs, served), `withheld` (set only by the journal
  replay: neither signs nor is served, the sealed private key is kept; an administrator confirms the retirement or reinstates
  it, R-DEB-032), `retired` (neither signs nor is served, the private key erased).

```sql
CREATE TABLE deb_signing_keys (
  id           uuid PRIMARY KEY,
  repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  fingerprint  text NOT NULL CHECK (fingerprint ~ '^[0-9A-F]{40}$'),
  algorithm    smallint NOT NULL CHECK (algorithm IN (1, 22)),                -- OpenPGP public-key algorithm: RSA or EdDSALegacy
  bits         integer NOT NULL,
  key_created  timestamptz NOT NULL,                                          -- the key's own creation time (Q-DEB-30)
  bound_since  timestamptz NOT NULL,                                          -- earliest valid binding self-signature (Q-DEB-40)
  state        text NOT NULL CHECK (state IN ('primary', 'next', 'withheld', 'retired')),
  origin       text NOT NULL CHECK (origin IN ('generated', 'imported')),
  public_key   text NOT NULL CHECK (octet_length(public_key) <= 65536),       -- ASCII-armored certificate
  private_key  text,                                                          -- sealed (R-ARCH-021); NULL once retired
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  promoted_at  timestamptz,
  withheld_at  timestamptz,
  retired_at   timestamptz,
  retired_by   uuid,
  CHECK ((state = 'retired') = (private_key IS NULL))
);
CREATE UNIQUE INDEX deb_signing_keys_primary ON deb_signing_keys (repo_id) WHERE state = 'primary';
CREATE UNIQUE INDEX deb_signing_keys_next    ON deb_signing_keys (repo_id) WHERE state = 'next';
CREATE UNIQUE INDEX deb_signing_keys_fpr     ON deb_signing_keys (fingerprint);                -- every state, every repository
```

| Method and path (below `/api/v1/projects/{projectKey}/repositories/{repoKey}/deb`) | Purpose | Permission |
|---|---|---|
| `GET /signing-keys` | Keys with fingerprint, algorithm, bits, key creation time, state, origin, times and public certificate | `read` |
| `POST /signing-keys` with `{"generate": true}` or `{"privateKey": "<armored>", "passphrase": "…"}` | Creates the next key (R-DEB-017); 409 when one exists | `manage` |
| `POST /signing-keys/promote` | Makes the next key primary and retires the old one (R-DEB-017) | `manage` |
| `DELETE /signing-keys/{fingerprint}` | Retires the next key, or confirms the retirement of a withheld key (its private key is erased); the primary answers 409 `deb_key_state` | `manage` |
| `POST /signing-keys/{fingerprint}/reinstate` | Returns a withheld key: to `primary` when the repository has none, else to `next` when it has none, else 409 `deb_key_state` | `manage` |

Acceptance criteria:

- After `deb-local` is created, `GET …/deb/signing-keys` lists one `primary` key with `algorithm` 22 whose `origin` is
  `generated`, and the sealed column's value starts with `v1:<active key id>:`; a unit test parses the generated certificate and
  asserts public-key algorithm 22, packet version 4, no expiry, a key creation time one hour before the creating transaction's
  time, and a binding self-signature whose creation time equals the key's (so `bound_since` equals `key_created`).
- A unit test fails when `deb_signing_keys.private_key` is missing from the sealed-column registry; no answer of the key
  operations, and no line of the request log, contains `PRIVATE KEY BLOCK`.
- Importing an RSA key of 2048 bits, an Ed25519 key of algorithm 27, a key with an expiry, or a key created an hour after the
  request's time answers 422 `deb_key_unsupported`; an RSA key of 4096 bits answers 201 with `state: next`; importing the same key
  into a second repository, or again after it was retired, answers 409 `deb_key_in_use`.

### R-DEB-016 · Signatures of `InRelease` and `Release.gpg`
Priority: **P2** · Covers: PKG-DEB-017

- Each build signs the `Release` of each suite twice: `InRelease` in the cleartext signature framework (RFC 9580 section 7,
  signature type 0x01) and `Release.gpg` as an ASCII-armored detached signature over the exact bytes of `Release` (type 0x00).
  Both carry a signature by the primary key and, while a next key exists, a second one by the next key (R-DEB-017). Signatures
  are version 4.
- Hash SHA-512. **Signature creation time** (Q-DEB-30): the `Release` `Date` of R-DEB-010 (the build's PostgreSQL time minus
  60 s or a little later, never later than that time minus 59 s), and at least one second after the creation time of each key
  that signs **and** after its `bound_since` (Q-DEB-40): the signature time is at least the later of the key's creation and its
  earliest binding self-signature, plus one second. For a key whose `bound_since` is younger than that (an imported key whose only
  self-signature is fresh), the build waits until the rule holds, about a minute after the import. Neither APT verifier accepts what trove would otherwise produce under a client clock that trails the database:
  `sqv` refuses a signature made after the client's now, and APT a `Date` more than 10 s ahead (both verified).
- Signatures are not deterministic: the OpenPGP module adds a random salt notation to each (verified), so two builds of equal
  rows publish equal `Packages` files but different `InRelease` and `Release.gpg` bytes. This page prescribes no subpacket layout
  beyond the creation time and the issuer fingerprint the module writes.
- The text `InRelease` signs is `Release` byte for byte (after the framework's dash-unescaping). `InRelease`, `Release` and
  `Release.gpg` of a suite belong to one generation, because one transaction publishes them (R-DEB-010).
- A build without a usable primary key (after a journal replay, R-DEB-032) publishes nothing: the suite's state is `failed` with
  the reason `no_signing_key`.
- The signer is the leaf package `internal/openpgp` (Q-DEB-24, Q-ARCH-383); the index build hands it the opened key for one
  call. The format plug-in never sees a private key.

Acceptance criteria:

- Integration: `InRelease` verifies with the repository's public key and its signed text equals `Release`; `Release.gpg`
  verifies over `Release`; while a next key exists, each file verifies with either key alone; every signature's creation time is
  at least 30 s before the PostgreSQL time of the assertion and later than both the creation time and the `bound_since` of its
  key; an imported key whose only self-signature is younger than its creation time by a day signs only after that self-signature.
- e2e: `apt-get update` accepts the repository in Debian 13 (APT verifies with `sqv`) and Debian 12, Ubuntu 24.04 and Ubuntu 22.04
  (APT verifies with `gpgv`), without `trusted=yes`; with a keyring that holds another key it fails with an error that names the
  missing key (R-DEB-035). That `sqv` and `gpgv` read the module's algorithm-22 signatures, single and co-signed, is verified
  ([Client and library behaviour](#client-and-library-behaviour-this-page-relies-on)); the e2e rows keep it proven per change.

### R-DEB-017 · Key rotation and what clients see
Priority: **P2** · Covers: PKG-DEB-018

APT has no channel through which a repository could hand clients a new key; a key change reaches a client only when the
client's keyring file is replaced. Rotation therefore has an overlap in which both keys sign (Q-DEB-11). That APT accepts a
release with one trusted signature among several, and refuses one with only unknown signers, is verified on APT 3.0 with `sqv`
and APT 2.4 with `gpgv` ([Client and library behaviour](#client-and-library-behaviour-this-page-relies-on)).

1. **Create the next key** (`POST …/deb/signing-keys`, generated or imported). From the build it enqueues at once, every suite is
   signed by the primary and the next key, and `signing-key.asc` serves both certificates. A client that holds only the old key
   keeps verifying the old signature; a client whose keyring file is replaced trusts both. A generated next key's creation time
   is an hour back (R-DEB-015), so a client that holds only the new key verifies its signatures from the first build on.
2. **Distribute.** The operator replaces the clients' keyring files (the configure snippet's key command again, R-DEB-029) while
   both keys sign. The key list shows since when the next key exists; trove cannot know which clients replaced their file
   ([H-621](#h-621--trove-cannot-tell-which-clients-still-trust-only-a-retired-key)).
3. **Promote** (`POST …/deb/signing-keys/promote`): the next key becomes primary; the old primary is retired: it stops signing,
   leaves `signing-key.asc` and its private key is erased. The promotion writes the audit event `deb.signing_key.promoted` with
   a journal entry naming the old and the new fingerprint (R-DEB-032). A client that never replaced its file fails
   `apt-get update` with an unverified signature from then on: it never accepts an index signed by a key it does not trust,
   which is the failure APT is built to show.

- Retiring the next key before a promotion (`DELETE …/signing-keys/{fingerprint}`) ends the overlap the same way and writes
  `deb.signing_key.retired` with a journal entry.
- Every key change raises `change_seq` of every suite of the repository in its transaction and enqueues a build without delay,
  so the re-signed files are published within the delay bound of R-DEB-010.
- At most one next key: a second create answers 409 `deb_key_state`; a promotion without a next key answers 409 `deb_no_next_key`; the
  primary cannot be retired directly (409 `deb_key_state`). The changes of one repository's keys run one after the other (Q-DEB-75).
- A repository without a primary key (a replay withheld it, R-DEB-032) can still get one: create a next key and promote it. The promotion
  retires nobody and its event names no old fingerprint (Q-DEB-74).

| Client's keyring holds | Before step 1 | Steps 1 and 2 | After step 3 |
|---|---|---|---|
| The old key only | verifies | verifies (old signature) | fails: unverified |
| Both keys (file replaced in step 2) | — | verifies | verifies (new signature) |
| The new key only | fails | verifies (new signature) | verifies |

Acceptance criteria:

- e2e rotation row on Debian 13 (`sqv`), Ubuntu 24.04 and Ubuntu 22.04 (`gpgv` 2.2): each cell of the table holds for a client with
  the old keyring only, one whose keyring is replaced in step 2 and one that holds only the new key from step 1 on (one client whose
  keyring file and lists are replaced per cell, Q-DEB-107). On Debian 13 the row runs once more with an imported next key whose only binding self-signature was made
  just before the import (as GnuPG writes one when an expiry is removed): the "new key only" client verifies every signature the
  builds publish, because none predates that self-signature (Q-DEB-40).
- Integration: after the promotion `InRelease` carries one signature, by the new key; the retired row's `private_key` is `NULL`;
  the journal holds the `deb.signing_key.promoted` entry with both fingerprints.

### R-DEB-018 · The public key for clients
Priority: **P2** · Covers: PKG-DEB-020

- `GET <repo URL>/signing-key.asc` answers one ASCII-armored block of certificates (`application/pgp-keys`): for a local or virtual
  repository the primary and, while one exists, the next key; for a remote repository the certificates of `deb.upstreamKeys`
  (404 `deb_no_upstream_keys` without them, R-DEB-020).
- Permission: `read` without package restriction (R-DEB-004); anonymous where the repository allows anonymous reads. The block's
  SHA-256 is its `ETag`.
- The configure snippet installs the block as `/etc/apt/keyrings/trove-<projectKey>-<repoKey>.asc` and names it in `Signed-By`
  (R-DEB-029). No setup of trove asks for `trusted=yes`.
- The key list (R-DEB-015) shows the same certificates with their fingerprints, so that an operator can compare a fingerprint
  through a second channel before trusting the key.

Acceptance criteria:

- The fetched block verifies the repository's `InRelease`; anonymous `GET` on a repository without anonymous read answers 401;
  a principal with package-scoped read gets 404.

### R-DEB-019 · Remote repository: upstream and release documents
Priority: **P2** · Covers: PKG-DEB-021

- `remote.url` is the root of the upstream archive, the directory that holds `dists/` and `pool/` (`https://deb.debian.org/debian/`,
  `http://archive.ubuntu.com/ubuntu/`). A flat repository's source line names its directory as the suite (`binary/`), below the
  same root, or `./` when `remote.url` is the flat directory itself (Q-DEB-23, Q-DEB-39).
- **Release documents** of a release directory (`dists/<suite>/` or a flat directory): `InRelease`, `Release` and `Release.gpg`,
  each a metadata document with the remote's metadata period, **passed through byte for byte**: the remote adapter's `Rewrite` is
  the identity, and trove never signs a remote's files (Q-DEB-13).
- **Before a release document is stored** (`PrepareFill`): at most 16 MiB; `InRelease` parses as one cleartext-signed message,
  and a `Hash` armor header, when present, matches its signatures (RFC 9580 deprecates the header, so its absence is no error,
  Q-DEB-35); the text parses as one deb822 paragraph with a `Date` and checksum fields whose lines parse (R-DEB-034); the `Date`
  is not more than 10 minutes after the PostgreSQL time (Q-DEB-35). With `deb.upstreamKeys` set, also: a signature verifies
  (R-DEB-020); `Suite` or `Codename` equals the requested suite name of a `dists/` directory; a `Valid-Until` has not passed. A
  failure stores nothing and answers 502 (`deb_release_invalid`, `deb_upstream_signature_invalid`); the stored copy stays.
- **No rollback below a verified release** (`OnFill`, Q-DEB-35, Q-DEB-42): `deb_remote_releases.floor_date` holds the newest
  `Date` of any release that was ever verified for the directory; it stays when `verified_by` is cleared (R-DEB-020) and only rises.
  While keys are set, a received release whose `Date` is older than the floor keeps the stored document and renews only its
  freshness (`FillKeepStored`, R-ARCH-008), counted in `trove_deb_release_rollbacks_total`, even when the stored document is
  itself unverified now: the directory then fails closed (no build, pass or plain index path uses an unverified release) until a
  release at or above the floor verifies. An older signed release replayed by a mirror or an attacker, also one signed by a key
  that stays in the set, therefore never replaces a newer one that was verified once. Between unverified releases the received copy replaces the
  stored one by fetch time, as for every format: a forged far-future `Date` on an unverified remote then cannot freeze it, and
  each client's own APT keeps its newer list against an older `Date` (verified). The 10-minute bound above keeps a forged or
  mistaken future `Date` from freezing a verified remote.
- **`Release` and `Release.gpg` are one pair.** A request for either makes both current before it answers (both are revalidated
  when either expired), so a client receives the pair of one fetch; with keys, the pair verifies before the request is answered
  and the `Release` is then recorded as verified, and until it is the `Release` names no file of its directory (Q-DEB-78,
  [H-736](../../security/deb-remote.md#h-736--a-release-and-its-releasegpg-are-stored-before-they-are-verified-as-a-pair)). A
  client that reads `InRelease` (every APT since 1.1) needs neither.
- The stored release that names a directory's index files (R-DEB-021) is the `InRelease` when the upstream has one, else the
  verified `Release`; it is recorded in `deb_remote_releases` (R-DEB-023).
- **An absent suite is recorded** (Q-DEB-41). When the upstream answers 404 or 410 for both `InRelease` and `Release` of a
  `dists/` directory, the remote writes a row with `document = 'absent'` in `deb_remote_releases`; a later successful release
  fetch deletes it, and a record older than the remote's not-found period counts as unknown, so the next check fetches again.
- A write to a remote repository answers 405. Upstream credentials, egress rules, TLS and redirects are R-REPO-024 and R-REPO-033
  to R-REPO-039, unchanged.

Acceptance criteria (integration, the counting stub of R-DEB-035):

- `GET <repo URL>/dists/stable/InRelease` through `debian` returns bytes whose SHA-256 equals the stub's file; a second request
  within 600 s sends the stub nothing; after 600 s the stub records one conditional request.
- With keys, the stub answering an `InRelease` with an older `Date` leaves the stored one served; without keys the older one
  replaces it; a `Date` 20 minutes ahead answers 502 `deb_release_invalid` with or without keys; with a broken signature and keys
  configured, the request answers 502 `deb_upstream_signature_invalid` and the stored one stays; an `InRelease` without a `Hash`
  armor header is stored.
- After the stub publishes a new generation, `Release` and `Release.gpg` fetched through `debian` verify as a pair.

### R-DEB-020 · Remote repository: verifying the upstream's signatures
Priority: **P2** · Covers: PKG-DEB-021

- **`deb.upstreamKeys`** holds the upstream archive's public certificates, ASCII-armored (1 to 32, at most 256 KiB: Debian 13's
  archive keyring holds 9 certificates in 75,839 bytes, verified), checked on save: a block that does not parse, holds secret key
  material, or holds a revoked, expired or non-signing key or an RSA key below 2048 bits answers 422 `invalid_value` with the
  reason. The repository document shows their fingerprints read-only in `deb.upstreamKeyFingerprints`.
- **With keys**, a release document is stored only when at least one of its signatures verifies with a key of the set that is
  valid **now** (not expired, not revoked) and was valid at the signature's creation time, whose creation time lies before the
  signature's, with a signature creation time no more than 10 minutes after the PostgreSQL time, and a hash of SHA-256 or
  stronger (Q-DEB-35); signatures by other keys are ignored while one verifies. A key the operator left in the set after it
  expired can therefore not sign a backdated release. The verifying key's fingerprint is recorded with the release (R-DEB-023).
- **A change of the keys re-verifies** (Q-DEB-32, Q-DEB-42). An update that removes fingerprints clears `verified_by` of every
  stored release that a removed key verified, in its transaction, and enqueues the remote's `deb-index` job, which re-verifies
  those releases from their stored bytes against the new set (no upstream request: the cache view of the engine, Q-DEB-82, Q-DEB-83)
  and records the verifying fingerprint where one verifies. A release no key of the new set verifies counts as expired, and its next revalidation is sent **without validators**
  (no `If-None-Match`, no `If-Modified-Since`: a HEAD whose answer says "moved", then the GET, Q-DEB-81), so the upstream answers
  with a body that `PrepareFill` verifies; an upstream 304 never marks a release verified. Nothing uses a release while its
  `verified_by` is cleared and keys are set: a request for a file of its directory answers 502 `deb_upstream_signature_invalid`. The removal of fingerprints is journaled as a part of
  `repository.narrowed` (`debUpstreamKeysRemoved`, R-REPO-061, Q-REPO-274), so a restore removes them again (R-DEB-032).
- **Without keys** release documents are stored unverified and clients verify with their own keyrings (the bytes are the
  upstream's, R-DEB-019; [H-623](#h-623--a-pull-through-remote-without-upstream-keys-stores-what-its-upstream-sends)). Such a remote cannot be a mirror (422 `deb_upstream_keys_required` at `/deb/upstreamKeys` for
  `remote.mode: mirror`) and cannot be a member of a virtual repository (422 `deb_member_unverified` at the member's pointer),
  because both would hand on content nobody checked: a virtual repository signs it with trove's key (R-DEB-025). Its document
  carries the warning `deb_upstream_unverified`. Removing the keys of a mirror or of a virtual member answers the same 422.
- Failures are counted in `trove_deb_upstream_signature_failures_total{repo, reason}` and written as the security audit event
  `deb.upstream_signature_invalid`, at most once per repository, release directory and hour.
- The keys are public data, stored in the repository document as given; they rotate when the operator changes them (the
  upstream archive's own key changes are the operator's to follow,
  [H-626](#h-626--a-verified-remote-fails-closed-when-its-upstream-signs-with-a-new-key)).

Acceptance criteria:

- The stub signs with key A: with `upstreamKeys` [A] `apt-get update` through `debian` succeeds; with [B] the release answers
  502 and nothing is stored; with the stub signing with A and an unknown key C and `upstreamKeys` [A] it succeeds; with the stub
  signing with SHA-1 only it answers 502.
- Creating a mirror without `upstreamKeys` answers 422 `deb_upstream_keys_required`; adding a remote without keys as a member of
  `apt` answers 422 `deb_member_unverified`.
- The stub signs with an expired key D left in `upstreamKeys` [A, D], with a backdated signature time: the release answers 502.
  Removing A from [A, B] while the stored release was verified by A and is also signed by B: the job re-verifies it from the stored
  bytes with B and the stub records no request; when it is signed by A alone, it stays unverified, the next request revalidates
  without validators and verifies the stub's newly signed release with B, and an upstream answer of 304 to a conditional request
  would not have marked it verified. The journal holds `repository.narrowed` with `debUpstreamKeysRemoved`.
- With `floor_date` set by a verified release, the stub serving an older release signed by a key that stays in the set leaves the
  stored release served (or the directory failing closed while the stored one is unverified) and counts a rollback.
- Debian 13's archive keyring (9 certificates, 75,839 bytes) is accepted as `upstreamKeys`.

### R-DEB-021 · Remote repository: index files as one snapshot
Priority: **P2** · Covers: PKG-DEB-003, PKG-DEB-013, PKG-DEB-021

Every index file a client fetches after a release document must be the file that release names (Q-DEB-15):

- **By-hash paths** (`<directory>/by-hash/SHA256/<hex>`, also `SHA512`) are content: fetched once from the same upstream path,
  verified against the digest in the path whatever `checksumPolicy` says (the digest addresses the file, R-REPO-041), immutable,
  with the cross-replica flight of R-REPO-026. `MD5Sum` and `SHA1` by-hash paths answer 404 without an upstream request.
- **Plain index paths** (a path relative to the release directory that the stored release lists) are metadata documents at that
  path, served only while their SHA-256 equals the hash the stored release names: otherwise trove revalidates them first (one
  conditional request), and a mismatch after that answers 502 `deb_index_mismatch` (the upstream is inconsistent) and stores
  nothing new. The fill of an index checks the fetched document against the listed hash in its own transaction and stores nothing
  on a mismatch; a stored document that is another file than the stored release lists is not served: the listed file is read from
  the upstream outside the cache, verified against its hash and served without being stored (Q-DEB-80,
  [H-740](../../security/deb-remote.md#h-740--an-index-in-the-window-between-a-refreshed-release-and-its-stored-document-costs-an-upstream-request-per-client-request)).
- A path below `dists/<suite>/` (or an index name in a flat directory) that the stored release does not list answers 404
  `deb_not_in_release` without an upstream request; files below a `*.diff/` directory (pdiff patches, which the release lists
  only through their `Index`) are passed through as content.
- An index path of a directory without a stored release fetches the release first (`InRelease`, else `Release`).
- **Across replicas.** Release documents and index files are single copies in PostgreSQL and the object store, so every replica
  serves the same release and the same bytes for a hash. The one window is an archive without by-hash: a client that read an
  older release than trove's current one gets a plain file of the newer release and reports a hash mismatch until its next
  update. Archives with by-hash, which include the Debian and Ubuntu archives, have no such window.
- **Housekeeping.** By-hash files and plain index documents that neither the stored release nor a release stored within
  `deb.by_hash_keep` names are removed by a `deb-index` job of the remote repository, under its key `index:<repository id>`,
  which the periodic task `deb-index-refresh` enqueues (Q-DEB-33); the mirror's rule that it deletes nothing covers package
  files, not superseded index files (Q-DEB-17). The names of the releases replaced within `deb.by_hash_keep` are the rows of
  `deb_remote_files` with `current = false`; the job forgets the rows older than the period and removes the by-hash items no remaining
  row names. A plain index document is one cache entry that the next fill replaces in place (Q-DEB-84).
- **Flat root.** `remote.url` naming the flat directory itself and the source line `deb <repo URL> ./` make the root the release
  directory (R-DEB-003, Q-DEB-39).

Acceptance criteria:

- Six clients on three replicas run `apt-get update` through `debian` against the stub with by-hash: the stub records one request
  per by-hash file.
- Against the stub without by-hash: after the stub publishes a new generation, a plain `Packages.gz` answers the old bytes while
  the old release is stored and the new bytes once the release was refreshed, never bytes that no stored release names; the stub
  serving a `Packages.gz` that matches no release answers 502 `deb_index_mismatch`.
- `GET <repo URL>/dists/stable/main/binary-amd64/Packages.bz2`, not listed, answers 404 and the stub records nothing; the flat
  directory `binary/` of the stub serves `binary/InRelease`, `binary/Packages.gz` and its package files; a remote of the stub's
  flat directory with `deb <repo URL> ./` serves `./InRelease` and installs a package in `ubuntu:24.04` (e2e).

### R-DEB-022 · Remote repository: pool files and their digests
Priority: **P2** · Covers: PKG-DEB-021

- Package files, and every other path that is no release document, index file or by-hash file, are content: filled on the first
  request (R-REPO-025) with the cross-replica flight (R-REPO-026), immutable.
- **Announced digest.** The remote's artifact gate (`FactsGate.BeforeArtifactWith`) returns the SHA-256 and size that the parsed
  rows (R-DEB-023) list for the path, and the engine verifies against them on a fill (R-REPO-041). The item lookup reads those rows
  in its own statement (the serve-facts kind `deb_remote`, Q-ARCH-381), so a remote hit stays at one statement (R-ARCH-027). For a
  path no parsed index lists, `checksumPolicy: verify-if-available` stores it unverified, counted in
  `trove_deb_unverified_fills_total`, and `require` refuses it with 502 `upstream_digest_missing`
  ([H-625](#h-625--pool-files-no-parsed-index-names-are-stored-unverified)).
- When two parsed indexes list different digests for one path, the newest parsed index decides; a stored file is never replaced
  (R-REPO-041, `proxy.upstream_digest_changed`).
- Patterns and claims apply to the package of the file name (R-DEB-003).

Acceptance criteria:

- The stub serving a package file with one byte changed (its `Packages` lists the original digest): `apt-get install` fails, the
  request answered 502 `upstream_integrity_mismatch` and nothing is stored.
- Six concurrent `apt-get install` of one package through three replicas: the stub records one request per package file.

### R-DEB-023 · Remote repository: parsed index rows
Priority: **P2** · Covers: PKG-DEB-022

- Every `Packages` index a remote stores, and every `Sources` index of a mirror with `sources: true`, is parsed into rows by the job
  `deb-remote-parse` (queue `index`, `coalesce` per repository, release directory and index path), enqueued in the transaction
  that stores the index file. A run reads, when it starts, the hash that the **currently stored** release names for its index
  path and parses that index (fetching it through the background path when it is not stored), never a hash handed to it at enqueue
  time; an enqueue while a run of the same path is going on leaves one job waiting, so a release stored during a run is parsed by
  the next one and no trigger is lost (Q-DEB-36). A newer index replaces the rows of the same index path in one transaction; a run
  for an index whose SHA-256 was parsed already does nothing (Q-DEB-16).
- **Rows follow the release, not the clients** (Q-DEB-36). APT applies pdiffs by default when a release lists a `*.diff/Index`,
  so a client may never fetch a full `Packages` again, and rows that waited for a client's fetch would stay stale indefinitely.
  When a new release document of a directory is stored, its `OnFill` therefore enqueues `deb-remote-parse` for every index path of
  that directory that has rows and whose hash the new release changes; the job fetches the full index (by hash, else at its plain
  path) through the background path of the remote engine (Q-REPO-272) and parses it. A virtual build enqueues the same job for
  every remote member index it merged (R-DEB-026), so the rows cover what the virtual repository serves.
- The rows are no client's dependency: until they exist, the gate of R-DEB-022 has no digest to compare.

```sql
CREATE TABLE deb_remote_releases (           -- the stored release document of a release directory of a remote
  repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  dir           text COLLATE "C" NOT NULL,   -- "dists/bookworm", or a flat directory; "" for the root
  document      text NOT NULL CHECK (document IN ('InRelease', 'Release', 'absent')),  -- 'absent': the upstream has no such suite
  sha256        bytea CHECK (octet_length(sha256) = 32),
  release_date  timestamptz,
  by_hash       boolean,
  verified_by   text,                        -- fingerprint of the key that verified it; NULL without upstreamKeys or once cleared
  floor_date    timestamptz,                 -- the newest Date of any release verified for this directory (Q-DEB-42)
  stored_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, dir, document),
  CHECK ((document = 'absent') = (sha256 IS NULL)),
  CHECK ((document = 'absent') = (release_date IS NULL))
);

CREATE TABLE deb_remote_indexes (            -- a Packages or Sources index whose rows are parsed
  repo_id     bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  dir         text COLLATE "C" NOT NULL,
  path        text COLLATE "C" NOT NULL,     -- relative to dir, without the compression suffix: "main/binary-amd64/Packages"
  sha256      bytea NOT NULL CHECK (octet_length(sha256) = 32),  -- of the parsed file
  entries     integer NOT NULL,
  skipped     integer NOT NULL DEFAULT 0,
  parsed_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, dir, path)
);

CREATE TABLE deb_remote_entries (
  repo_id       bigint NOT NULL,
  dir           text COLLATE "C" NOT NULL,
  index_path    text COLLATE "C" NOT NULL,
  package       text COLLATE "C" NOT NULL,
  version       text COLLATE "C" NOT NULL,
  architecture  text COLLATE "C" NOT NULL,   -- "source" for an entry of a Sources index
  path          text COLLATE "C" NOT NULL,   -- the Filename (or a file of a source entry), relative to remote.url
  sha256        bytea NOT NULL CHECK (octet_length(sha256) = 32),
  size          bigint NOT NULL CHECK (size >= 0),
  FOREIGN KEY (repo_id, dir, index_path) REFERENCES deb_remote_indexes (repo_id, dir, path) ON DELETE CASCADE
);
CREATE INDEX deb_remote_entries_path ON deb_remote_entries (repo_id, path);
CREATE INDEX deb_remote_entries_name ON deb_remote_entries (repo_id, package);

CREATE TABLE deb_remote_files (               -- the files the stored releases list (migration 00064, Q-DEB-79)
  repo_id        bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  dir            text COLLATE "C" NOT NULL,
  path           text COLLATE "C" NOT NULL,   -- relative to dir: "main/binary-amd64/Packages.xz"
  sha256         bytea NOT NULL CHECK (octet_length(sha256) = 32),
  size           bigint NOT NULL CHECK (size >= 0),
  current        boolean NOT NULL,            -- listed by the release that is stored now
  superseded_at  timestamptz,                 -- when the release that listed it was replaced; kept for deb.by_hash_keep
  PRIMARY KEY (repo_id, dir, path, sha256),
  CHECK (current = (superseded_at IS NULL))
);
```

- **Bounds** (R-DEB-034): 1 GiB decompressed, 1,000,000 paragraphs, 1 MiB per field; a paragraph without `Package`, `Version`,
  `Architecture`, `Filename`, `Size` and `SHA256`, or with a name or version outside R-DEB-002, is skipped and counted; a crossed
  bound fails the run with the reason and keeps the earlier rows.
- **Uses.** The digests of R-DEB-022; the shadowing check; mirror discovery ([Mirror discovery](#mirror-discovery)); the package
  screens and the `deb.*` properties of cached files (R-DEB-030), which replaces a manual calculation of coordinates for a cache.
- **The shadowing check in the scope of the virtual repository** (Q-DEB-37, Q-REPO-270). For each pair of a virtual repository V
  and its remote member R in the exposure set of R-REPO-048, the check asks R's `NameServer` whether R serves the name **in V's
  scope**: the suites, components and architectures V serves (R-DEB-025). R serves the name when a parsed row of an index in that
  scope lists it. It does not serve it only when **every suite** of V's scope is settled for R and none lists it: a suite is
  settled when R has a stored release for it whose indexes of the scope are all parsed, or when R has recorded that its upstream
  does not have the suite (a row with `document = 'absent'` in `deb_remote_releases`, written in PostgreSQL when the upstream
  answers 404 or 410 for the suite's release documents; a negative entry in Valkey never admits, R-REPO-048 step 2). A suite of
  V's scope for which R has neither a stored release nor such a record, or whose indexes are not all parsed, makes the answer
  undetermined (Q-DEB-41): the caller without `manage` gets 503 `shadow_check_unavailable`, and the check enqueues the release
  fetch and the parse through the background path (Q-REPO-272), so a retry decides. A remote that is offline is not served
  (R-REPO-048 step 3). A name that exists only in a component or architecture V does not serve is not served through V and does
  not block the upload.
- The rows are cache rows: a restore without remote caches deletes them, and the rows of `deb_remote_files` with them, with the cache
  (R-BAK-008).
- **Where the jobs run.** `deb-remote-parse` and the remote's `deb-index` job run on the roles that claim jobs; a parse reads the
  index through the background path as a by-hash file when the release has `Acquire-By-Hash` and at its plain path otherwise, and
  replaces the rows of its index path in one transaction. A job with no index path fetches the release of its directory (and
  records an absent suite) and enqueues the parse of the indexes of its scope that lack rows (Q-DEB-90).

Acceptance criteria:

- Within ten seconds after `apt-get update` through `debian`, the rows of `main/binary-amd64` exist and their count is the number of
  the stub's paragraphs; after the stub adds a version, the rows of that index are replaced and list it.
- A crafted `Packages` with 2,000,000 paragraphs fails its run with `too_many_entries` and the earlier rows stay.
- In `apt` (members `deb-local`, `debian`), a `deploy` holder's first upload of `trove-hello`, which the stub lists, answers 403
  `shadows_remote_package`; of `acme-tool`, which it does not list, 201.
- With `apt` serving `main` and `contrib`, the first upload of `contrib-only`, which the stub lists only in `contrib`, answers 403
  `shadows_remote_package`; before `contrib` was parsed it answers 503 `shadow_check_unavailable` and the parse is enqueued; with
  `apt` serving only `main`, the same upload answers 201.
- `apt` lists `testing` in `deb.suites`, and `debian` never fetched the release of `testing`: a `deploy` holder's first upload of a
  name the stub does not list anywhere answers 503 `shadow_check_unavailable` and enqueues the fetch; after it, the upload answers
  201. With a stub that answers 404 for `testing`, the record `absent` is written and the upload answers 201 without a parse.
- After the stub publishes a new release whose `main/binary-amd64/Packages` changed and offers it only through a pdiff, the rows
  of that index are replaced within ten seconds of the release being stored, without a client fetching the full index.

### R-DEB-024 · Mirror settings of a Debian remote
Priority: **P2** · Covers: trove-only

A remote repository of the format runs in either mode of R-REPO-050. What a mirror of an APT archive keeps is in
[Mirror discovery](#mirror-discovery) (Q-DEB-17, built: Q-DEB-91); its own settings are `deb.mirror`, present in both modes and effective in mode
`mirror`:

| Member (`deb.mirror.…`) | Meaning | Default (proposed) | Range |
|---|---|---|---|
| `suites` | Suites (below `dists/`) to mirror; required in mode `mirror` | `[]` | 1–32 |
| `components` | Components whose indexes and package files are kept | `["main"]` | 1–32 |
| `architectures` | Architectures whose `Packages` indexes and package files are kept; `all` is always kept | `["amd64"]` | 1–16 names of R-DEB-002, not `all` |
| `translations` | Languages of `i18n/Translation-<language>` kept | `["en"]` | 0–16 |
| `sources` | Whether `Sources` indexes and source package files are kept | `false` | |

- The generic members keep their meaning: `maxVersionsPerPackage` counts the newest versions of a package name across the
  mirrored suites by dpkg order; `includePrereleases` has no effect (R-DEB-002); `maxBytes` bounds the repository's bytes
  (R-REPO-052).
- `remote.mode: mirror` with an empty `suites` answers 422 `deb_mirror_suites_required`; without `deb.upstreamKeys` 422
  `deb_upstream_keys_required` (R-DEB-020).
- A pass uses a release document only while it is verified by a key of the current set and its `Valid-Until` has not passed;
  otherwise the suite is noted (`release_unverified`, `release_expired`) and plans nothing (Q-DEB-32). A release older than the
  directory's `floor_date` is never stored while keys are set (R-DEB-019); after a restore without remote caches, which deletes the
  floor with the rows, the first release stored is taken as it is ([H-630](#h-630--a-restore-without-remote-caches-forgets-the-newest-upstream-release-a-remote-had-seen)).
- `deb.mirror` decides what the mirror covers, as the patterns do, so an Owner without `repositories:write-config` keeps it as
  stored (R-REPO-050, 422 `mirror_restricted`).
- The well-known upstream (`https://deb.debian.org/debian/`) needs R-REPO-052's bound (include patterns with a literal part or
  `maxBytes`) besides `suites`.

Acceptance criteria:

- A remote created without `deb` reads back `"mirror": {"suites": [], "components": ["main"], "architectures": ["amd64"],
  "translations": ["en"], "sources": false}`.
- `"mode": "mirror"` with `suites: []` answers 422 `deb_mirror_suites_required`; `"architectures": ["all"]` answers 422
  `invalid_value` at `/deb/mirror/architectures/0`.
- An Owner of project `web` without `repositories:write-config` changing `deb.mirror.suites` of `debian-mirror` gets 422
  `mirror_restricted`.

### R-DEB-025 · Virtual repository: what it serves and how it merges
Priority: **P2** · Covers: PKG-DEB-023

- **What it serves.** The suites of `deb.suites` and those of its local members; for each, the components of `deb.components`
  and the architectures of `deb.architectures`, plus `binary-all`. The index of every architecture also lists the architecture `all`
  packages of the members, as a local repository's does (Q-DEB-110). A remote member contributes to every served suite its
  upstream has; a member whose upstream answers 404 for a suite (or a local member without the suite) contributes nothing to it
  and is no failure (R-DEB-026).
- **Which member releases may be merged** (Q-DEB-32). A remote member's release document is merged only when all of these hold;
  otherwise the build fails with the reason and keeps the previous generation:
  - its `verified_by` is a fingerprint of the member's **current** `deb.upstreamKeys` (else `member_unverified: <key>`): a release
    stored before the keys existed, or verified by a key the operator removed since, is fetched and verified anew first
    (R-DEB-020);
  - its `Valid-Until`, when it has one, has not passed (else `member_expired: <key>`);
  - its `Date` is not older than the `Date` the previous generation merged for that member, recorded in `deb_suites.inputs`
    (else `member_rollback: <key>`). The virtual repository's own rows survive a restore without remote caches, so an older
    upstream release that a remote accepts after losing its stored copy never becomes a newer trove generation. The recorded
    `Date` of a member is dropped, in the transaction of the change, when the member's upstream identity changes (R-REPO-011: a
    new `remote.url` is another archive) and when the member leaves the virtual repository (Q-DEB-43); taking a member out and
    adding it back is therefore the deliberate, audited way to accept an older release of its upstream. A reindex does not reset
    it.
  - Every build asks the remote engine for the release of the suite as a background request (R-DEB-026): the engine serves its stored
    copy while it is fresh and revalidates it when it is not, and it stores a newer document only when a current key verifies it
    (R-DEB-020). A newer release that no current key verifies is therefore not stored and the build keeps merging the last verified
    copy, as it does when the upstream is down; `member_unverified` is the answer for a stored copy that no current key verifies (a
    key change took the verification away) and for a member without keys (Q-DEB-64).
- **Freshness carried over.** The virtual repository's `Release` carries the earliest `Valid-Until` of the member releases it
  merged, when any has one, so a frozen upstream suite expires for the virtual repository's clients as it would for the
  upstream's: trove re-signs no staleness into a fresh signature.
- **Members** are local and remote Debian repositories of the project (R-REPO-017); a remote member without `deb.upstreamKeys` is
  refused (422 `deb_member_unverified`, R-DEB-020). Nested virtual repositories wait for R-REPO-018.
- **Architecture `all`** (Q-DEB-110). The merged `binary-all` index is the merge of the members' `binary-all` indexes. The merged index of an
  architecture is the merge, per member and in member order, of the member's index of that architecture and then its `binary-all`; a
  package that both list (Debian and Ubuntu list `all` packages in both) is kept once. An upstream that keeps its `all` packages in
  `binary-all` only is therefore carried whole, and a client of one architecture finds the `all` dependencies of its packages.
- **Merge** (Q-DEB-18). For each suite, component and architecture, the members are walked in the order of R-REPO-019 steps 2, 3
  and 5 (patterns, pin rules and claims, priority members) on stored configuration only, never by the caller (Q-DEB-19). A local
  member contributes the paragraphs of its **published** generation's index (so the virtual repository never lists a file its
  member does not list yet), a remote member those of the index its stored release names. A paragraph is kept when its member's
  patterns admit the name, the pin rules and claims let the member serve the name, and no earlier member contributed the same
  package, version and architecture. A name a claim gives to one member comes only from that member, in every version.
- **Member paths.** The `Filename` of every kept paragraph becomes `members/<memberKey>/<the member's Filename>`; every other
  field is kept as the member serves it. The client then downloads each file from exactly the member whose paragraph it read
  (R-DEB-027), so the digest it checks and the bytes it gets come from one member (R-REPO-020).
- **Order, by streaming** (Q-DEB-38). The merged index lists the members' paragraphs in member order and, within a member, in the
  member's own order; the build streams each member's index once and remembers the package, version and architecture it kept as
  128-bit hashes (at most 1,000,000 per index, R-DEB-034), so memory stays bounded by that set and not by the index. Equal inputs
  give equal bytes.
- **Signature.** The virtual repository's `Release` has its own `Origin`, `Label`, `Suite`, `Codename`, `Architectures` and
  `Components` and is signed with the virtual repository's own key (R-DEB-015, R-DEB-016). Its clients trust that key and no
  upstream's, which is why trove merges only remote members whose upstream signatures it verified.
- Source packages, translations and `Contents` are not served through a virtual repository (Q-DEB-7, Q-DEB-28).
- **Dependency confusion.** APT installs the highest version among every entry of every source. A remote paragraph for an
  internal name with a higher version would win, so claims are the protection: a name claimed by a local member loses every
  remote paragraph, and the automatic claim of a first upload makes that the default for names published locally (R-REPO-023).

Acceptance criteria:

- `trove-hello 1.0 all` in `deb-local` and, with other bytes, in the stub behind `debian`: the merged `Packages` of `apt` lists one
  paragraph, `deb-local`'s, with `Filename: members/deb-local/pool/t/trove-hello/trove-hello_1.0_all.deb`.
- A claim `acme-*` of `deb-local`; the stub lists `acme-tool 9.9`, `deb-local` holds `acme-tool 1.0`: the merged index lists only
  `1.0`, and `apt-get install acme-tool` through `apt` installs `1.0`.
- A member whose exclude patterns hold `bad-*` contributes no `bad-x`.
- A remote member whose stored release was stored before `upstreamKeys` were set, or verified by a key removed since, is fetched
  and verified before the build merges it; the stub then signing with a key outside the set fails the build with
  `member_unverified: debian` and the previous generation stays. A member release whose `Valid-Until` passed fails it with
  `member_expired`; the stub serving an older `Date` after the remote lost its stored copy fails it with `member_rollback`, a
  reindex of `apt` fails the same way, and taking `debian` out of `apt` and adding it back lets the next build merge it.
- The stub's `stable` with `Valid-Until` in six days: the merged `Release` of `apt` carries that `Valid-Until`; a suite in
  `deb.suites` that the stub does not have contributes nothing and the build publishes.

### R-DEB-026 · Virtual repository: index builds and remote members
Priority: **P2** · Covers: PKG-DEB-023

- A virtual repository's indexes are built by the job of R-DEB-010 under the key `index:<virtual repository id>`, into generated
  items of the virtual repository (Q-STOR-180), with the snapshot, monotony and retention rules of R-DEB-010 and R-DEB-012.
- **Triggers.** A local member's publication: its build's commit enqueues every virtual Debian repository that lists the member,
  with a delay that keeps `deb.virtual_index_interval` since the virtual repository's last build; a change of the members, pin
  rules or settings, and a change of a member that is a remote's keys or patterns, without delay (an earlier run time moves a
  waiting job, Q-ARCH-384); a change of claims is seen by the next check, because the claim revisions are part of the inputs
  (Q-DEB-65); and the periodic task
  `deb-index-refresh` (queue `maintenance`, every minute, `exclusive`), which enqueues a `deb-index` job for each virtual
  repository whose last check is older than its `virtual.mergedMetadataTtlSeconds`, so a change upstream reaches the virtual
  repository without a client request (Q-DEB-21). The task enqueues and deletes nothing itself (Q-DEB-33).
- **Remote members during a build.** Their release documents and index files go through the remote engine as background requests:
  `discover.Upstream.Document` for release documents and `discover.Upstream.Artifact` for by-hash index files, from an `Upstream`
  that the host builds for one job run of one remote repository outside a mirror pass (`BackgroundUpstream`, declared by WP0,
  Q-REPO-272), with the background share of R-REPO-055 and the throttling of R-REPO-054. A stored copy is used while fresh and
  revalidated when expired, under the rules of R-DEB-025. A member that fails transiently contributes its stored copy; a member
  without one makes the build publish nothing and keep the previous generation (`state: failed`, reason
  `member_unavailable: <key>`), because a signed index without that member's packages would remove them from every client. A
  member that answers 404 for the suite contributes nothing and is no failure.
- The build enqueues `deb-remote-parse` for every remote member index it merged whose rows are missing or older, so the shadowing
  check has rows in the virtual repository's scope (R-DEB-023).
- A build whose inputs did not change (each member's published generation or stored release digest and `Date`, the settings, a
  digest of the claim revisions of the Debian repositories, the fingerprints of the signing keys, recorded in `deb_suites.inputs`)
  commits only its check time (Q-DEB-65).
- Each index file is merged as a stream (R-DEB-025); no build holds a whole index in memory.
- **Delay bound** (proposed): from a local member's publication to the virtual repository's, p99 at most
  `deb.virtual_index_interval` + 60 s, with a remote member whose index holds 70,000 paragraphs and `deb.compression: ["xz"]` (the
  speed of the xz writer on such an index is not measured yet; the bound test is where it is).

Acceptance criteria:

- An upload to `deb-local` is listed through `apt` within the bound.
- With `virtual.mergedMetadataTtlSeconds: 60`, a version the stub adds is listed through `apt` within two minutes, without a client
  request in between.
- The stub stopped after `debian` stored its release: the next build of `apt` publishes from the stored copy. The stub stopped for a
  suite `debian` never stored: the build fails with `member_unavailable: debian` and the previous generation is served.

### R-DEB-027 · Virtual repository: reads, the gate and member paths
Priority: **P2** · Covers: PKG-DEB-023

- **The gate.** Besides `read` on the virtual repository (R-AUTHZ-009 rule 1), its release documents, index files, by-hash files
  and `signing-key.asc` need `read` without package restriction on every member (R-DEB-004), because their merged index is one for
  every caller (Q-DEB-19). An authenticated caller who lacks it on a member gets 403 `deb_members_unreadable`; an anonymous caller
  401 (R-AUTHZ-009 rule 6).
- **Member paths.** `members/<memberKey>/<path>` resolves on that member, which must be a member of the virtual repository (404
  otherwise), with the member's own checks: `read` on the member for the coordinate, the cache-fill check of a remote member
  (R-AUTHZ-008: not stored and not fillable by the caller is 404), the member's serve check, its patterns, and the virtual
  repository's pin rules and claims for the name. The answer carries `Trove-Resolved-From: <memberKey>`. A local member's file costs
  one statement (R-ARCH-027).
- A `pool/<path>` request to a virtual repository (a client with an index from elsewhere) is resolved by the artifact tiers of
  R-REPO-019.
- Raw access through `/files/` to a virtual repository resolves through the members only; its generated items are served on the
  format's routes.

Acceptance criteria:

- A principal with `read` on `apt` and `deb-local` but not on `debian` gets 403 `deb_members_unreadable` for
  `dists/stable/InRelease` of `apt`; an anonymous caller with `read` on `apt` only gets 401.
- With `cacheFillAction: deploy` on `debian`, a reader's `members/debian/pool/…` of a file `debian` has not stored answers 404;
  `members/other/pool/…` for a repository that is no member answers 404.

### R-DEB-028 · Virtual repository: uploads through the deploy target
Priority: **P2** · Covers: PKG-DEB-023

- `PUT <virtual repo URL>/upload/<file>` runs R-DEB-006 on the deploy target (R-REPO-021, R-AUTHZ-009 rule 5); the dispatcher sends
  the write to the target and the format has no code of its own for it (Q-DEB-67). Without a target the decision point refuses
  `deploy` on the virtual repository with 403 before the format runs, as it does for npm and NuGet; 405 `no_deploy_target` is the
  core's answer when the target is gone after the decision. The answer's `index` entries name the target's suites; the virtual
  repository publishes by R-DEB-026.

Acceptance criteria:

- An upload through `apt` is stored in `deb-local` and listed through `apt` within the bound of R-DEB-026; through a virtual
  repository without a deploy target it answers 403 (Q-DEB-67).

### R-DEB-029 · Client setup snippets and guide
Priority: **P2** · Covers: PKG-DEB-026, PKG-DEB-027

The format's setup templates (client `apt`, the variant is the suite; [api-and-ui.md](../api-and-ui.md) R-API-030) render, for
the repository the reader looks at:

```sh
# configure: trust the repository's keys and add the source (deb822 form)
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL -u "alice:${TROVE_TOKEN}" https://trove.example.com/api/deb/web/apt/signing-key.asc \
  | sudo tee /etc/apt/keyrings/trove-web-apt.asc >/dev/null
sudo tee /etc/apt/sources.list.d/trove-web-apt.sources >/dev/null <<'SOURCES'
Types: deb
URIs: https://trove.example.com/api/deb/web/apt
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/trove-web-apt.asc
SOURCES

# authenticate: credentials in a file only root can read, never in the source URL
sudo install -m 0600 /dev/null /etc/apt/auth.conf.d/trove-web-apt.conf
printf 'machine trove.example.com/api/deb/web/apt\nlogin alice\npassword %s\n' "${TROVE_TOKEN}" \
  | sudo tee /etc/apt/auth.conf.d/trove-web-apt.conf >/dev/null

# install
sudo apt-get update && sudo apt-get install hello
```

- A one-line `sources.list.d/*.list` variant (`deb [signed-by=…] <repo URL> <suite> <component>`) for systems that prefer it.
- **Publish** (local, and virtual with a deploy target): the `curl --upload-file` of R-DEB-006 and the index wait of R-DEB-014.
- **Remote:** `Signed-By` names the remote's `signing-key.asc` when `deb.upstreamKeys` is set; otherwise the distribution's own
  keyring (`/usr/share/keyrings/debian-archive-keyring.gpg`, `/usr/share/keyrings/ubuntu-archive-keyring.gpg`), with the note that
  trove did not verify the upstream. `deb-src` lines appear only for remote repositories.
- An anonymously readable repository drops the `-u` option and the authentication step.
- No snippet uses `trusted=yes`, `allow-insecure`, `apt-key` or credentials in a URL (they would end up in world-readable files and
  in logs).
- **Guide** sections ([api-and-ui.md](../api-and-ui.md) R-UI-049, R-API-053) for every class and topic the kit's check 8 requires, with
  `maintain` (key rotation, R-DEB-017; a withheld key after a restore, R-DEB-032) and `troubleshooting`: an unverified signature
  after a promotion; a client clock that trails trove's database (R-DEB-010): on Debian 13 from about 60 s "OpenPGP signature
  verification failed … created after the --not-after date … is not signed", on the `gpgv` clients from about 70 s "Release file
  … is not valid yet", both cured by the client's time synchronisation; a hash mismatch on an upstream without by-hash, 403 `deb_members_unreadable`, `member_unverified`,
  `member_expired` and `member_rollback` in a virtual repository's index state (an older upstream release is accepted
  deliberately by taking the member out and adding it back, Q-DEB-43).

Acceptance criteria:

- The rendered snippets of each class, with the test's token, run unchanged in the e2e images (R-DEB-035); the kit's check 8
  passes for the format.

### R-DEB-030 · Package screens
Priority: **P2** · Covers: PKG-DEB-010

- The view adapter orders versions by dpkg order (`Compare`, R-DEB-002) and describes a version from its rows: the first line of
  `Description`, and as metadata the architectures, the coordinates, `Maintainer`, `Section`, `Priority`, `Installed-Size`,
  `Homepage`, `Depends` and `Source`. There is no README, so the format has no wiki content
  ([authorization.md](../authorization.md) R-AUTHZ-034) and a repository's wiki level decides nothing for it.
- For a remote repository the screens show the versions whose files are stored, described from the parsed rows (R-DEB-023).
- **The repository screen** ([api-and-ui.md](../api-and-ui.md) R-UI-021, Q-DEB-100 to Q-DEB-102): a local or virtual Debian repository
  has the tabs **Signing keys** (R-DEB-015, R-DEB-017, R-DEB-032: the list, the next key generated or imported, the promotion, the
  removal of the next key, the confirmation or reinstatement of a withheld key; a private key is never shown) and **Index**
  (R-DEB-014: each suite's state with the reason of a failed build in words, and the full rebuild). A remote repository has neither;
  its format tab shows the read-only `upstreamKeyFingerprints` beside the editable `upstreamKeys`. The settings of R-DEB-008 and
  R-DEB-024 are the format tab: each list as one entry per line, `compression` as a checkbox.

Acceptance criteria:

- The package screen of `hello` in `deb-local` lists `2.10-3` with the architectures `amd64` and `arm64` and the coordinate
  `stable/main`; the API returns them in `metadata`.

### R-DEB-031 · Debian on n replicas, without Valkey and during a drain
Priority: **P2** · Covers: trove-only

| State | Where | Across replicas | Valkey unavailable | Drain or crash of the replica |
|---|---|---|---|---|
| Package files, rows, generated index files, suites | PostgreSQL and S3 | One copy | Unaffected | Unaffected; a cut upload is retried by the client and commits once |
| Index builds | Jobs on PostgreSQL (R-ARCH-016) | One build per repository at a time (`coalesce`) | Pick-up within `jobs.poll_interval` | The job is released or expires; another worker builds from the rows; nothing partial is published |
| Signing keys | Sealed in PostgreSQL; opened in the build's memory for one call | Every worker can sign | Unaffected | The opened key goes with the process |
| A remote's release documents, index files and package files | PostgreSQL and S3 | One copy | Unaffected | Unaffected |
| Concurrent fills of one file | The flight of R-REPO-026 | One upstream request | One per replica (R-ARCH-033) | The flight is released |
| Parsed release maps for plain index paths | The replica's rendition cache, keyed by the release's SHA-256 | Each replica parses its own | Unaffected | Gone; parsed again |

- Valkey grants nothing for the format: no file, row, key, state or permission is read from it.
- No session affinity is needed (INV-12): every request of `apt-get update` may reach another replica, and an upload on one replica
  is published for all of them by the build.

Acceptance criteria:

- The e2e set `deb` runs in the `scatter` profile (R-ARCH-032): every `apt-get` row succeeds with each request on another
  `packages` replica.
- With Valkey stopped, uploads, `apt-get update` and `apt-get install` through local, remote and virtual repositories succeed
  (closing run).

### R-DEB-032 · Debian data in backups and restores
Priority: **P2** · Covers: trove-only

- **Export classes** (R-BAK-004): every table of the format is `data`. `deb_remote_releases`, `deb_remote_indexes` and
  `deb_remote_entries` are the remote's cache rows: a restore without remote caches (R-BAK-008) deletes them with the cache.
- **Revision columns.** `deb_suites.change_seq` and `built_seq` are raised by 2^32 (R-BAK-022), so no `changeSeq` a client held
  before is reached falsely.
- **Fix-up.** A restore enqueues a full build of every Debian local and virtual repository, which runs when workers start; its
  `Date` is later than every earlier one (R-DEB-010), so clients take the rebuilt indexes.
- **A deleted package does not come back** (Q-DEB-22), as for npm (R-NPM-047) and OCI. The deletion of a package file writes the
  event `deb.package.deleted` with a journal entry (R-HA-054) naming the repository, the path, the package, version and
  architecture, the SHA-256 and the actor. The replay, when the restored repository holds the file at that path with that digest,
  writes its mark in `deb_tombstones` and raises the `change_seq` of the file's suites (`applied`); a file marked already is
  `already in effect`; a file the repository does not hold is `subject unknown`. A marked file is withheld: the serve check refuses
  it with 404 on the format's routes and on `/files/`, read with the item in one statement (the serve-facts kind `deb_local`,
  Q-ARCH-381); builds leave it out; an upload of the same bytes answers 409 `deb_withheld`. An administrator confirms the deletion
  through the item API (the mark stays) or reverses it with `trove admin deb reinstate <projectKey>/<repoKey> <path>`, which removes
  the mark only while the file is stored with that digest and writes `deb.reinstate` (not journaled).
- **A retired key does not sign again, and the replay erases nothing** (Q-DEB-31, Q-DEB-22). The replay runs with the owner
  credential, but its entries come from the journal in the object store, which a holder of the runtime S3 credential could write
  (R-HA-054); like every replay it therefore only restricts and never deletes content, here: never a private key.
  - `deb.signing_key.promoted` (repository, old fingerprint, new fingerprint, actor): when the restored repository holds the new
    key (as `next`, which is the common case of a key created before the snapshot and promoted after it), the replay makes it
    `primary` and sets the old one `withheld` (`applied`); the clients that trust the new key keep working. When it does not hold
    the new key (created after the snapshot), the replay sets the old primary `withheld`, so the repository has no primary: its
    builds fail with `no_signing_key` until an administrator acts, and the generation published before the restore stays served
    ([H-622](#h-622--a-signing-key-created-after-the-newest-backup-is-lost-by-a-restore)).
  - `deb.signing_key.retired` (repository, fingerprint, actor): the key is set `withheld` (`applied`); a key withheld or retired
    already is `already in effect`; a key the restored repository does not hold is `subject unknown`.
  - A withheld key neither signs nor is served in `signing-key.asc`, and keeps its sealed private key. An administrator confirms the
    retirement (`DELETE …/signing-keys/{fingerprint}`, which erases the private key and writes `deb.signing_key.retired`) or
    reinstates the key (`POST …/signing-keys/{fingerprint}/reinstate`, R-DEB-015), so a forged entry costs availability until an
    administrator acts, never a key ([H-629](#h-629--a-forged-journal-entry-can-withhold-a-repositorys-signing-key-until-an-administrator-acts)).
    Each change raises `change_seq` of every suite of the repository, so the next build re-signs with what is left. The replay
    output lists every repository with a withheld key or without a primary.
- **Trust narrowings come back too** (Q-DEB-34).
  - `deb.coordinates.removed` (repository, path, SHA-256, removed coordinates): when the restored repository holds the file at that
    path with that digest, the replay removes those coordinates from it (`applied`); a file left without coordinates is withheld as
    for a deletion (a mark in `deb_tombstones`), never deleted.
  - The `debUpstreamKeysRemoved` part of `repository.narrowed` (R-REPO-061, Q-REPO-274): the replay removes those fingerprints from
    the restored `deb.upstreamKeys` and clears `verified_by` of the releases they verified; a remote left without keys that is a
    mirror or a member of a virtual repository is disabled (`suspended`), because it may neither mirror nor be merged unverified.

Acceptance criteria:

- The restore drill of R-BAK-029 holds a package uploaded before the snapshot: after the restore `apt-get install` of it succeeds.
- A package deleted after the snapshot is withheld after the restore: no index lists it, its pool path answers 404, the replay
  reports `applied`, and an upload of the same bytes answers 409 `deb_withheld` until `trove admin deb reinstate`.
- A key created before the snapshot and promoted after it: after the restore the new key is `primary`, the old one `withheld`
  with its sealed private key in place, `apt-get update` with the new keyring succeeds; confirming the retirement erases the old
  private key, and reinstating instead makes it `next`.
- A key created and promoted after the snapshot: after the restore the old primary is `withheld`, the builds fail with
  `no_signing_key`, and the replay output names the repository.
- A coordinate removed after the snapshot and a fingerprint removed from `deb.upstreamKeys` after it are removed again after the
  restore, and a second replay reports both `already in effect`.

### R-DEB-033 · Audit events and metrics
Priority: **P2** · Covers: trove-only

| Event | When | Journal |
|---|---|---|
| `authz.write.allowed` with `deploy` or `delete` | uploads, coordinate changes, deletions (R-AUTHZ-029) | — |
| `deb.package.deleted` | a package file removed | yes |
| `deb.coordinates.changed` | coordinates replaced (R-DEB-009) | — |
| `deb.coordinates.removed` | coordinates removed from a file, beside `deb.coordinates.changed` (R-DEB-009) | yes |
| `deb.signing_key.created` | R-DEB-015 | — |
| `deb.signing_key.promoted` | a next key promoted; names the new and, when there was a primary, the old fingerprint (R-DEB-017, Q-DEB-74) | yes |
| `deb.signing_key.retired` | a next key removed, or the retirement of a withheld key confirmed | yes |
| `deb.signing_key.reinstated` | a withheld key reinstated (R-DEB-015) | — |
| `deb.reinstate` | a withheld file reinstated | — |
| `repository.narrowed` with `debUpstreamKeysRemoved` | fingerprints removed from `deb.upstreamKeys` (R-DEB-020, R-REPO-061) | yes |
| `deb.upstream_signature_invalid` (security) | a remote's release failed verification; at most once per repository, directory and hour | — |

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `trove_deb_index_builds_total` | counter | `class`, `outcome` (`published`, `unchanged`, `failed`) | Index builds |
| `trove_deb_index_build_seconds` | histogram | `class` | Duration of a build |
| `trove_deb_index_delay_seconds` | histogram | `class` | From the first change a build covers to its commit (R-DEB-010) |
| `trove_deb_upstream_signature_failures_total` | counter | `repo`, `reason` | R-DEB-020 |
| `trove_deb_release_rollbacks_total` | counter | `repo` | Older release documents kept out (R-DEB-019) |
| `trove_deb_unverified_fills_total` | counter | `repo` | Package files stored without an announced digest (R-DEB-022) |
| `trove_deb_remote_parse_total` | counter | `outcome` (`parsed`, `skipped`, `failed`) | Runs of `deb-remote-parse` |

Acceptance criteria:

- After the e2e set `deb`, `trove_deb_index_builds_total{outcome="published"}` grew and the request log contains no
  `PRIVATE KEY` and no token.

### R-DEB-034 · Limits for untrusted content
Priority: **P2** · Covers: trove-only

Every uploaded package and every upstream document is untrusted input ([architecture.md](../architecture.md) R-ARCH-028); the
format reads it through `internal/formats/untrusted` with these bounds, which only tighten the defaults:

| Input | Bounds |
|---|---|
| `.deb` archive (`untrusted.NewAr`) | at most 8 members, names of at most 16 bytes, member sizes that add up to the body; the data archive is skipped, never decompressed |
| Control archive | 16 MiB compressed, 64 MiB decompressed, 1,000 entries, a ratio of 100:1, `control` at most 1 MiB, no link in place of `control` |
| deb822 paragraphs (control, `Release`, `Packages`, `Sources`) | 512 fields per paragraph, 1 MiB per field, field names and continuation lines as R-DEB-007 |
| Release documents | 16 MiB, 100,000 checksum lines; one cleartext message; a `Hash` armor header, when present, matches its signatures |
| `Packages` and `Sources` of a remote; a member index a virtual build merges | R-DEB-023: 1 GiB decompressed, 1,000,000 paragraphs |
| OpenPGP input | `deb.upstreamKeys`: 256 KiB, 32 certificates; an imported secret key: 64 KiB; 32 signatures per document |
| Every pass | the 30 s deadline of R-ARCH-028, except the parse job (its own timeout of 10 minutes) |

- **Index injection.** Every paragraph trove writes is built from parsed fields, never from bytes copied unparsed: a field value
  cannot hold a blank line (which would start a new paragraph), a computed field (`Filename`, `Size`, the digests) is never taken
  from the input, and field names follow R-DEB-007. A virtual merge validates each remote paragraph the same way and leaves one that
  fails out, counted.
- Paths in an upstream's `Filename` that are absolute, hold `..` or a backslash are skipped by the parse (R-DEB-023), so a remote
  never fills a path outside its repository.
- The conformance kit's crafted inputs (R-ARCH-010) gain a `.deb` whose control archive expands to 10 GiB, a control file with
  100,000 fields, an `ar` archive with a duplicate `control.tar.gz`, and an `InRelease` with 200 signatures; each is refused within
  5 s with 413 or 422.

Acceptance criteria:

- The crafted inputs above are refused within 5 s, and the replica's memory stays within 512 MiB of its baseline (R-ARCH-028).
- A remote `Packages` whose paragraph says `Filename: ../../../etc/shadow` is skipped and counted, and nothing outside the
  repository's paths is requested from the upstream.

### R-DEB-035 · Test suites
Priority: **P2** · Covers: trove-only

- **Unit:** the grammars and the version order of R-DEB-002; the deb822 parser and writer with crafted inputs and fuzz targets
  (kit `fuzz.go`); the `ar` reader; the `Packages` and `Release` renderers (determinism, order, field rules); the merge of R-DEB-025
  over fixtures; the OpenPGP signer and verifier against keys generated in the test; the format conformance kit with no check
  skipped.
- **Counting stub archive** (`test/debstub`): a deterministic APT archive generated in code (no recorded third-party data) with
  real, installable `.deb` files (`trove-hello` and friends, each installing a script that prints a fixed line), the suites `stable`
  and `testing`, the components `main` and `contrib`, the architectures `amd64`, `arm64` and `all`, a `Sources` index with one
  source package, a flat directory `binary/` and a flat root, an index of 70,000 synthetic paragraphs for the bounds, signed by
  test keys (EdDSALegacy, generated from fixed seeds) with creation times an hour back; counters per host, method and path;
  switches: by-hash on or off, `InRelease` absent (only `Release` and `Release.gpg`), an `InRelease` without a `Hash` armor
  header, a new generation of a suite (with the upstream's by-hash history and pdiffs: a `*.diff/Index` and its patches), a
  signature by an unknown key, by an expired key with a backdated signature time, a broken signature, a SHA-1 signature, an older
  `Date`, a `Date` 20 minutes ahead, a passed `Valid-Until`, a `Valid-Until` six days ahead, a suite that answers 404, a package
  file that does not match its index, an index that matches no release, a delay per response, down, and the throttling modes of
  R-REPO-059. The stub as built (T-17 WP5): the large index is the suite `bulk`; the keys a, b, c (valid) and d (expired) have fixed
  seeds and creation dates (Q-DEB-70); `InRelease` is framed as `gpg` writes it and indexes are plain and gzip (Q-DEB-73); the paths,
  the switches and the control API are described in [developer/deb.md](../../developer/deb.md).
- **Integration** (`test/integration/deb`, the stack of R-HA-051: PostgreSQL, Valkey with Sentinel, S3, several replicas): every
  acceptance criterion of this page that names the protocol, over HTTP; `TestR_AUTHZ_007_DebActionMap`; remote and virtual against
  the stub; mirror passes in `test/integration/mirror` (the Debian criteria of [Mirror discovery](#mirror-discovery)); the
  read budget rows of R-ARCH-027 (`test/integration/querybudget`).
- **e2e** (kind, the chart, the `scatter` profile, three `packages` replicas, two workers): client pods from the images
  `debian:13`, `debian:12`, `ubuntu:24.04` and `ubuntu:22.04`, pinned by digest in `tools/versions.env` (Q-DEB-108), trusting the dev CA through
  `Acquire::https::CAInfo`, with the rendered setup snippets (Q-DEB-27). APT runs with `--error-on=any`, so a warning fails the row,
  and never with `trusted=yes`. Set `deb` (every change that touches the format): `apt-get update` and `apt-get install` of a
  package and running it, in each image, through `deb-local` (after an upload by the test and the index wait), `debian` in mode
  `pullThrough`, `debian-mirror` after a pass with the stub stopped, and `apt` (a local and a remote package; the claim row); an
  anonymous read; a keyring with another key, which must fail; the rotation table of R-DEB-017 on Debian 13, Ubuntu 24.04 and
  Ubuntu 22.04; the flat root `deb <repo URL> ./` on Ubuntu 24.04. Set `deb-long` (nightly, R-HA-052): the delay bound of
  R-DEB-010 under load, the bound of R-DEB-026 with `compression: ["xz"]`, and the drain of the worker that runs a build of a
  virtual repository with the large index. One cluster shares one clock, so the time rules of Q-DEB-30 are proven by the
  integration asserts of R-DEB-010 and R-DEB-016, not by the e2e set. The set `deb` carries a Debian mirror of the stub (`TestDebMirror`), and a pass on two worker processes while clients install through it and
  the drain of the node whose process runs a Debian pass are not built (R-REPO-058, R-REPO-059, Q-REPO-306).
- **Closing run** (the full run of Test and Release on the commit that closes the phase): the set `deb` with Valkey stopped, and a
  read-only check against the Debian archive: a `pullThrough` remote of `https://deb.debian.org/debian/` with Debian's archive key
  (fetched in the test and checked against a fingerprint pinned in the test) and `apt-get install hello` in `debian:13`, at most 30
  upstream requests.

Acceptance criteria: the suites are green in CI for every change that touches the format, and the closing run is green.

## Mirror discovery

What a Debian remote in mode `mirror` discovers and stores ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-050
to R-REPO-052; tests name R-REPO-051). The engine, the bounds and the throttling are the core's; this section is what the format
contributes through its mirror lister (R-ARCH-008, `MirrorLister`). An APT archive is not mirrored without a choice: the Debian
archive alone holds many suites, four components, several architectures and the sources of everything. The mirror keeps the
suites, components and architectures the operator names (Q-DEB-17).

- **What an archive offers.** No list of its suites: a suite is known by its name. Per suite a release document that lists the
  components, the architectures and every index file with its size and digests; a `Packages` index per component and architecture
  that lists every binary package with its `Filename`, `Size` and `SHA256`; a `Sources` index per component.
- **Methods** (`discover.Method`):
  - `feed`: not offered; APT archives have no change feed.
  - `listing` (what `auto` takes): the `Packages` indexes of the mirrored suites, components and architectures and `binary-all`,
    and the `Sources` indexes with `sources: true`. Every pass is full.
  - `names`: the include patterns without a wildcard, read as package names and looked up in the same indexes.
  - As for every method, the names the repository holds are added (R-REPO-051).
- **The index step** (the first page of a pass). For each mirrored suite: its release document through `discover.Upstream.Document`
  (stored, revalidated when expired or when validated before the pass's attempt began, R-REPO-051), verified by a key of the
  current set and not expired (R-DEB-020, R-DEB-024); then each
  kept index file the way a client asks for it: by hash through `discover.Upstream.Artifact` when the release says
  `Acquire-By-Hash: yes`, else at its plain path through `Document`, in every compression variant the release lists and the upstream
  serves (a listed variant the upstream answers 404 for, such as an uncompressed `Packages`, is skipped); the
  `i18n/Translation-<language>` files of `translations` the same way; then the parse of R-DEB-023 for each `Packages` and `Sources`
  the pass needs, which the lister runs in the pass through the data port instead of waiting for the job (Q-DEB-91). A client takes
  the strongest hash a release lists, so with a `SHA512` section each index file is kept by that hash as well (Q-DEB-92); every
  variant the release lists is kept, not only the one the parse reads (Q-DEB-95). A suite the upstream does
  not have is noted (`suite_unavailable: <suite>`) and skipped; a component or architecture its release does not list is noted and
  skipped. The notes are `suite_unavailable: <suite>`, `release_unverified: <suite>`, `release_expired: <suite>` and
  `index_unlisted: <path>`, carried in the note of the first page (Q-DEB-94).
- **Names.** The distinct package names of the pass's parsed indexes that the scope admits (patterns, claims, the name rule of
  R-DEB-002), in name order, in pages of the engine's limit, with the cursor `{"last": "<name>"}` within the pass (at most 4 KiB);
  there is no cursor between passes.
- **The plan of a name.** The rows of the name in the pass's indexes: per version the package files of the mirrored architectures
  and of `all` and, with `sources: true`, the files of its source package; each an item with its path, its announced size and its
  version, newest first by dpkg order. The digests reach the engine through the gate of R-DEB-022 from the same rows, so every file
  a pass stores is verified. A version counts once for `maxVersionsPerPackage`, however many files it has (the engine counts
  versions, not items: the correction of `SelectItems` that the OCI phase brings, built here if it is not on main yet).
- **Moving suites.** Every pass revalidates each release document; a changed release brings new index files, new rows and new
  versions to plan; the files of older versions stay, because the mirror deletes nothing (R-REPO-051). Superseded index files are
  the housekeeping of R-DEB-021.
- **What stays out.** Index files of other suites, components and architectures; `Contents-*`, `dep11` and `cnf` files, other
  translation languages, pdiffs, `debian-installer` indexes and `.udeb` files; `Sources` unless `sources: true`. A client asking
  for one of them is served by pull-through while the upstream is reachable. While it is not, such a request fails, so a client
  whose APT configuration asks for an index target the mirror does not keep (`dep11` of desktop systems, `cnf` of Ubuntu's
  `command-not-found`) sees `apt-get update` fail offline
  ([H-628](#h-628--an-offline-mirror-serves-only-clients-whose-index-targets-it-keeps)).
- **Hosts.** The upstream origin; `MirrorHosts` is empty.
- **Offline.** After a complete pass, `apt-get update` and `apt-get install` of a mirrored version succeed with the upstream
  unreachable: the stored release document (served stale by R-REPO-029), its kept index files and the package files. A release
  whose `Valid-Until` passes while the upstream stays unreachable is refused by the clients themselves; trove cannot sign an
  upstream's release anew.
- **Bounds.** R-REPO-052 applies; the sizes the rows announce let `maxBytes` be checked before each fetch. A component of a Debian
  suite holds tens of thousands of packages per architecture, so the defaults keep one component and one architecture, and the
  well-known upstream needs include patterns or `maxBytes` as well (R-DEB-024).

Acceptance criteria (integration, the stub of R-DEB-035):

- A stub with `stable` (components `main` and `contrib`; architectures `amd64`, `arm64`, `all`) and a mirror with `suites:
  ["stable"]` and the defaults: the pass stores `stable`'s `InRelease`, the `Packages` files of `main/binary-amd64` and
  `main/binary-all`, `main/i18n/Translation-en` and every package file those indexes list; the stub records no request for a file of
  `contrib` or `arm64`.
- With `includePatterns: ["trove-*"]` and `maxVersionsPerPackage: 1`, and `trove-hello` in the versions `1.0` and `1.1`: the pass
  stores `1.1` of each `trove-*` name only; `apt-get install trove-hello=1.0` through the remote still succeeds by pull-through.
- A second pass with nothing changed upstream sends one conditional request per release document and no request for an index or
  package file; after the stub publishes a generation with one new version, the next pass stores its index files and the new file.
- With `maxBytes` below the mirrored size the pass ends `bound_reached`, and a package it did not store still installs by
  pull-through.
- After a pass, with the stub stopped, `apt-get update && apt-get install trove-hello` through the remote succeeds.
- The stub signing with an unknown key: the pass's discovery fails with `deb_upstream_signature_invalid` and nothing new is stored.

## Test plan

Where each requirement is proven (R-DEB-035 has the suites; the testing rule of the phase run asks for integration over HTTP
against PostgreSQL, Valkey with Sentinel and S3 plus the conformance kit, and e2e with the real clients on several replicas):

| Requirements | Unit | Integration (`test/integration/deb`, `…/mirror`) | e2e set |
|---|---|---|---|
| R-DEB-001 to R-DEB-005 (format, names, layout, permissions, errors) | grammars, version order, route table, `Classify` | formats API, `TestR_AUTHZ_007_DebActionMap`, error shapes, headers | `deb` |
| R-DEB-006 to R-DEB-009 (upload, control, storage, coordinates) | `ar` reader, deb822 parser, crafted inputs, canonical paths | uploads over HTTP, idempotency, immutability, coordinates API, properties | `deb` (upload by the test, install) |
| R-DEB-010 to R-DEB-014 (builds, content, by-hash, deletion, state) | renderers, determinism | parallel uploads on three replicas, fault-injected build, Valkey stopped, by-hash retention, trash, reindex, index wait | `deb`; `deb-long` (delay bound) |
| R-DEB-015 to R-DEB-018 (keys, signatures, rotation, public key) | signer and verifier | key API, sealing, import rules, dual signatures, promotion, journal entry | `deb` (verification in four images, a wrong keyring fails, the rotation table) |
| R-DEB-019 to R-DEB-023 (remote) | release parser, anti-rollback decision | the counting stub: byte-for-byte release, verification, rollback, pair, by-hash, plain-path check, pool digests, parse rows, shadowing check | `deb` (remote `pullThrough`) |
| R-DEB-024, Mirror discovery | plan over fixtures | stub passes (suites, components, architectures, patterns, versions, bound, moved suite, unknown key, offline) | `deb` (a pass, `TestDebMirror`; a pass on two worker processes while clients install and the drain are not built) |
| R-DEB-025 to R-DEB-028 (virtual) | merge over fixtures | merge order, claims, member paths, gate, member without stored copy, refresh, uploads through the target | `deb` (local and remote packages through `apt`, the claim row); `deb-long` (drain during a build) |
| R-DEB-029, R-DEB-030 (setup, screens) | templates render, kit check 8 | view API | `deb` (rendered snippets), browser journey of the package and key screens |
| R-DEB-031 to R-DEB-035 (replicas, backups, metrics, limits, suites) | crafted inputs (kit) | Valkey stopped, journal replay of deletions and retirements, read budget rows | `deb` in `scatter`; closing run with Valkey stopped; restore drill (R-BAK-029) |

## Catalog coverage

Every entry of the catalog page, with what this phase does (Q-DEB-2). "Built" means specified on this page and built in T-17;
"FULL.md" means it stays in the FULL.md row for a later phase; "Not offered" means trove decided against it.

<details>
<summary>PKG-DEB-001 to PKG-DEB-028</summary>

| Entry | Decision | Where |
|---|---|---|
| PKG-DEB-001 Three classes | Built; the federated class is outside the phase (no federation exists) | R-DEB-001 |
| PKG-DEB-002 Automatic layout and coordinates | Built; coordinates are rows, not properties, and trove chooses the pool path | R-DEB-003, R-DEB-008, R-DEB-009, Q-DEB-5 |
| PKG-DEB-003 Flat layout | Not offered for local and virtual repositories; flat upstreams served by remote pull-through | R-DEB-021, Q-DEB-23 |
| PKG-DEB-004 Deploy with coordinates | Built as `PUT upload/<file>` with query parameters; the deploy dialog's Debian option stays in FULL.md | R-DEB-006, Q-DEB-3 |
| PKG-DEB-005 Several coordinate sets | Built: repeated or comma-separated parameters, the coordinates operation | R-DEB-006, R-DEB-009 |
| PKG-DEB-006 Architecture-independent packages | Built | R-DEB-009, R-DEB-011, R-DEB-013 |
| PKG-DEB-007 Source packages | Built for remote repositories (pull-through, and a mirror with `sources: true`); uploads of source packages and a generated `Sources` stay in FULL.md | R-DEB-021, R-DEB-024, Q-DEB-28 |
| PKG-DEB-008 Automatic index calculation | Built as a job with a delay bound | R-DEB-010 |
| PKG-DEB-009 Validation and index status | Built stricter: an invalid package is refused, never stored unindexed; no switch | R-DEB-007, Q-DEB-4 |
| PKG-DEB-010 Control fields as properties | Built in part: read-only `deb.*` properties and the package screen; a search over control fields stays with the search work of FULL.md (M8) | R-DEB-007, R-DEB-030 |
| PKG-DEB-011 Index compression | Built: gzip always, xz on request; bzip2 and LZMA not offered | R-DEB-011, Q-DEB-7 |
| PKG-DEB-012 Debug-symbol packages | FULL.md | Deferred |
| PKG-DEB-013 Acquire-by-hash | Built, always on, SHA-256, retained by time and generations; no switch | R-DEB-012, R-DEB-021, Q-DEB-8 |
| PKG-DEB-014 MD5 in `Packages` | Built: always written; no switch | R-DEB-011 |
| PKG-DEB-015 Origin and Label | Built as repository settings, effective with the build their change enqueues | R-DEB-008, R-DEB-011 |
| PKG-DEB-016 Recalculate metadata | Built as the reindex of R-REPO-016 | R-DEB-014 |
| PKG-DEB-017 Release signing | Built | R-DEB-016 |
| PKG-DEB-018 Keys per repository and rotation | Built: a generated key per repository, a next key that co-signs, promotion; keys are not picked by name from a shared store | R-DEB-015, R-DEB-017, Q-DEB-9, Q-DEB-11 |
| PKG-DEB-019 Instance-wide default key | Not offered: every repository has its own key | Q-DEB-9 |
| PKG-DEB-020 Public key for clients | Built | R-DEB-018 |
| PKG-DEB-021 Remote repositories | Built; browsing an upstream before anything is cached is not offered (an APT archive has no listing besides its indexes) | R-DEB-019 to R-DEB-023 |
| PKG-DEB-022 Coordinates of cached remote packages | Built automatically from the parsed index rows; no manual action | R-DEB-023 |
| PKG-DEB-023 Virtual repositories | Built | R-DEB-025 to R-DEB-028 |
| PKG-DEB-024 Create a snapshot | FULL.md | Deferred |
| PKG-DEB-025 Resolve and delete snapshots | FULL.md | Deferred |
| PKG-DEB-026 APT source configuration | Built (deb822 and one-line forms; `deb-src` for remotes) | R-DEB-029 |
| PKG-DEB-027 Authenticated access | Built through `auth.conf.d`; credentials in a source URL are not recommended | R-DEB-029 |
| PKG-DEB-028 Companion CLI | Not offered: trove has no companion CLI | Q-DEB-2 |

</details>

## Changes to other pages

The decisions of this page change other pages. Each change is made by the work package that builds its mechanism, in the same
change as its code ([apt-work-packages.md](../../projektplan/apt-work-packages.md)), because several blocks are held equal to the
code by tests (the contract blocks of R-ARCH-008 and R-ARCH-009 by `TestR_ARCH_008_TheWrittenContractEqualsTheCode`). The decision
IDs of other pages below are reserved for those edits and recorded there by the package that applies them:

| Page | Change | Decision |
|---|---|---|
| [README.md](../README.md) | Pages table: the row of this page (made with this page) | Q-DEB-1 |
| [architecture.md](../architecture.md) | R-ARCH-005 and Q-ARCH-42: a `.` segment directly after the address of a repository whose format declares a flat root reaches the format's routes uncleaned; R-ARCH-008: the optional interface `NameServer` of a remote adapter (`RemoteCaps.Names`, asked with a `NameQuery` that names the virtual repository whose scope decides) and the serve-facts kinds `deb_local` (a withheld local file) and `deb_remote` (the digest a parsed index announces for a remote path); R-ARCH-010: the format's crafted inputs; R-ARCH-012: the format as the first job-built index, its generated items and its delay bound; R-ARCH-015: the `rev_seq` check does not apply to a job-built merged index, whose visibility is the format's delay bound; R-ARCH-016: an enqueue into a `coalesce` key with an earlier run time moves the waiting job earlier, and a reindex upgrades a waiting automatic build to a full one; R-ARCH-027: the read budget rows of the format (a plain index path of a remote costs two statements); R-ARCH-028: `untrusted.NewAr` and the xz and zstd readers; R-ARCH-032: the per-replica cache of parsed release maps; the leaf packages of the format and `internal/openpgp` in the layer table; the stale pointer of the setup templates (R-API-030) | Q-ARCH-380, Q-ARCH-381, Q-ARCH-382, Q-ARCH-383, Q-ARCH-384, Q-ARCH-385, ADR-170, ADR-172 |
| [repositories-and-proxy.md](../repositories-and-proxy.md) | R-REPO-003: a format without a package document names its remote's `NameServer`; R-REPO-009: a format's data port may add rows in the transaction that creates a repository of the format (the first signing key); R-REPO-016: the endpoint is built, 405 for formats without a job-built index until their rebuild exists, and a reindex upgrades a waiting automatic build; R-REPO-019 and R-REPO-020: a Debian virtual repository's merged index is one stored generation for every caller (not a per-replica cache, no `rev_seq` check, visibility by the format's delay bound, a partial merge never published) and its index reads need `read` on every member; R-REPO-048: the shadowing check asks the `NameServer` in the virtual repository's scope when the format has no package document; R-REPO-050: Debian can mirror, `deb.mirror` is kept like the patterns; R-REPO-052: the well-known Debian upstream; R-REPO-061 and Q-REPO-246: the removal of fingerprints from `deb.upstreamKeys` is the part `debUpstreamKeysRemoved` of `repository.narrowed`; the background `Upstream` for one job run of one remote repository outside a pass | Q-REPO-270, Q-REPO-271, Q-REPO-272, Q-REPO-273, Q-REPO-274 |
| [storage.md](../storage.md) | R-STOR-003: a virtual repository of a format with a job-built index holds the generated items of its indexes; the publication port that writes a generation's generated items (blobs first, one transaction with the caller's own writes, removals, `modified_at` given by the caller); R-STOR-018, R-STOR-019, R-STOR-007: generated items are never written, deleted, copied or moved by a client (409 `generated_item`); R-STOR-028: the summary lists a virtual repository that holds generated items; R-STOR-029: generated items count toward usage and are exempt from quota admission | Q-STOR-180, Q-STOR-181, Q-STOR-182 |
| [authorization.md](../authorization.md) | R-AUTHZ-007: the per-format notes list the Debian table of R-DEB-004 and `TestR_AUTHZ_007_DebActionMap`; R-AUTHZ-009: the gate of a Debian virtual repository's index reads (rule 4 for a format whose merged index cannot depend on the caller) | Q-DEB-19, Q-DEB-20 |
| [backup-and-restore.md](../backup-and-restore.md) | R-BAK-004: the format's tables; R-BAK-008: the remote's parsed rows go with the cache; R-BAK-022: the full build after a restore; R-BAK-024: the Debian entries (deletions, removed coordinates, key promotions and retirements) and that the key replay withholds and never erases | Q-DEB-22, Q-DEB-31, Q-DEB-34 |
| [ha-and-helm.md](../ha-and-helm.md) | R-HA-054: the journal flags of `deb.package.deleted`, `deb.coordinates.removed`, `deb.signing_key.promoted` and `deb.signing_key.retired`; R-HA-052: the set `deb-long` and the Debian rows of `mirror-long` | Q-DEB-22, Q-DEB-31, Q-DEB-34 |
| [api-and-ui.md](../api-and-ui.md) | The management operations of R-DEB-009, R-DEB-014 and R-DEB-015 (with confirm and reinstate of a withheld key); the `apt` setup templates and guide; the signing key panel and the index state on the repository screen | Q-DEB-12 |
| [operations-and-observability.md](../operations-and-observability.md) | The audit events and metrics of R-DEB-033; the job kinds `deb-index`, `deb-remote-parse` and the task `deb-index-refresh` | — |
| [endpoints.md](../endpoints.md) | R-EP-018: the format has no exposure adapter, so an endpoint with exposure rules does not serve it | Q-DEB-26 |
| [vulnerability-scanning.md](../vulnerability-scanning.md) | Deferred: Debian packages are not matched in this phase | Q-DEB-26 |
| [decisions.md](../../decisions.md) | ADR-170, ADR-171, ADR-172 and the table rows of Q-DEB-1 to Q-DEB-43 | — |

## Deferred

| Catalog ID or topic | Priority | Note |
|---|---|---|
| PKG-DEB-004 (deploy dialog) | P2 | The UI's deploy dialog gains a Debian option with distribution and component fields. |
| PKG-DEB-007 (local sources) | P2 | Uploads of source packages (`.dsc` with its files) and a generated `Sources`, with `deb-src` for local repositories. |
| PKG-DEB-010 (search) | P2 | Searching control fields, with the search work of FULL.md M8. |
| PKG-DEB-012 | P2 | Debug-symbol packages (`.ddeb`) and their indexes. |
| PKG-DEB-024, PKG-DEB-025 | P2 | Point-in-time snapshots of a suite's indexes. |
| `.udeb` and `debian-installer` indexes | P2 | Installer components in local repositories and mirrors. |
| `dput` uploads | P2 | Uploads of a signed `.changes` with its files, which needs a keyring of uploaders. |
| Translations and `Contents` for local and virtual repositories | P2 | Long descriptions are in `Packages` today; a virtual repository does not merge its remote members' translations. |
| Mirrors of flat repositories | P2 | Flat upstreams are served by pull-through (Q-DEB-23). |
| `Valid-Until` on trove's own releases | P2 | With a periodic re-signing ([H-624](#h-624--troves-own-release-files-carry-no-valid-until)). |
| Signing keys in an external key service | P2 | A key management service or an HSM that signs without handing out the key (Q-DEB-9). |
| Debian vulnerability matching | P2 | OSV's Debian data is keyed by source package and release; it needs a mapping of its own (Q-DEB-26). |
| Copy and move of Debian packages | P1 | With R-STOR-019: the coordinates travel with the file. |
| Debian on endpoints with exposure rules, through the Bastion, in nested virtual repositories | P2 | Q-DEB-26; nested virtual repositories with R-REPO-018. |

## Security notes

- **Clients trust a key, not a host.** Every index trove generates is signed with the repository's own key, and the setup installs
  that key under `Signed-By` for that one source. A key belongs to one repository, so trusting `web/apt` trusts nothing else on the
  same installation (Q-DEB-9).
- **The private keys never leave trove.** They are sealed with the stored-secret key ring, opened only by the index build for one
  signing call, and returned by no API; a holder of the ring and of a database copy can sign for every repository
  ([H-620](#h-620--a-holder-of-the-stored-secret-key-ring-and-a-database-copy-can-sign-for-every-debian-repository)). An imported
  key stays known to whoever imported it ([H-627](#h-627--an-imported-signing-key-stays-known-to-whoever-imported-it)).
- **Rotation fails closed.** After a promotion, a client that kept only the old key refuses the repository instead of accepting
  anything; the overlap in which both keys sign is the window for replacing keyring files (R-DEB-017).
- **A restore cannot revive a retired key, and a forged journal entry cannot destroy one.** The replay withholds keys whose
  retirement or promotion it finds and never erases a private key; an administrator confirms or reinstates (R-DEB-032, H-629).
- **Times trail the database clock.** Release dates and signature times lie a minute behind the database's clock and keys an
  hour behind it, so clients whose clocks trail a little still verify, and `sqv` never sees a signature from its future
  (R-DEB-010, R-DEB-015, R-DEB-016).
- **Re-signing only what was verified, and only while it is fresh.** A virtual repository signs upstream content with trove's
  key, so it merges a remote member's release only while a key of the member's current set verified it, its `Valid-Until` has not
  passed and its `Date` did not go back, and carries the earliest member `Valid-Until` into its own `Release`; a remote without
  keys can be neither a member nor a mirror (R-DEB-020, R-DEB-025).
- **Upstream releases pass unchanged and never go back.** A remote serves the upstream's signed bytes, so clients can verify the
  upstream themselves; an older verified release never replaces a newer verified one, and a release dated more than ten minutes
  ahead is refused (R-DEB-019).
- **Trust narrowings survive a restore.** Removed upstream keys, removed coordinates, deleted packages and retired keys are
  journaled and re-applied, fail-closed (R-DEB-032).
- **Indexes are written from parsed fields.** An uploaded control file or an upstream paragraph cannot add paragraphs or computed
  fields to an index, and upstream paths cannot leave the repository (R-DEB-007, R-DEB-034).
- **Dependency confusion.** APT takes the highest version from every source; claims keep a remote's paragraphs of an internal name
  out of a virtual repository's index (R-DEB-025).
- **Index reads need unrestricted read.** An index lists every package of a suite; a principal narrowed to some package names reads
  its package files but not the indexes (Q-DEB-20).
- **Credentials stay out of source lines.** The setup writes tokens to an `auth.conf.d` file only root can read (R-DEB-029).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the
walkthrough.

### Q-DEB-1 · One format `deb` at `/api/deb/`, on the page `formats/debian.md` with the prefix `R-DEB`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Format ID `deb`, display name "Debian (APT)" (chosen) | `/api/deb/<projectKey>/<repoKey>`; one format for Debian, Ubuntu and every archive in the Debian repository format | The word every APT source line starts with; short URLs; one format, because the archive format is one | The catalog page is called `debian.md`, so the page and the ID differ |
| (b) `debian` | The catalog's name | Matches the page | Ubuntu users read a Debian name in every URL; longer |
| (c) `apt` | The client's name | Familiar | The archive is read by more than APT (`debootstrap`, `aptly`, mirror tools); an ID after one client misleads |

**Choice:** (a).

**Rationale:** the archive format is the protocol, and `deb` names it as source lines do. The development agent would revisit the
ID only if a second Debian-family format (`.udeb` installers as a format of their own) needed a distinct one.

**Applied in the spec:** R-DEB-001; [README.md](../README.md) pages table.

### Q-DEB-2 · What this phase builds and what stays in FULL.md

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Local, remote (both modes) and virtual repositories for binary packages, signed indexes with rotation, by-hash snapshots, verified upstreams, the mirror, source packages through remotes (chosen) | FULL.md keeps local source packages, snapshots, debug symbols, installer packages, `dput`, the deploy dialog option, control-field search, translations for local and virtual repositories, mirrors of flat archives, `Valid-Until`, keys in a key service, Debian vulnerability matching | What `apt-get update` and `apt-get install` need through every class, with verification on the client, is built completely | Snapshots and local source packages wait |
| (b) The owner's minimum without signing | Unsigned indexes, clients with `trusted=yes` | Smallest | The owner asked for signature verification on the client |
| (c) The whole catalog | Snapshots, sources, debug symbols too | Done at once | Each brings machinery of its own (a snapshot store, a three-file upload, a debug component) that no owner workload asks for yet |

**Choice:** (a). Not offered at all: an instance-wide default key, a companion CLI, the flat layout for local and virtual
repositories, bzip2 and LZMA indexes, browsing an upstream before anything is cached.

**Rationale:** the client path is built end to end in every class, signed and verified; what needs machinery of its own waits for
a workload. The development agent would revisit an item when an owner workload needs it.

**Applied in the spec:** [Catalog coverage](#catalog-coverage), [Deferred](#deferred).

### Q-DEB-3 · Packages are uploaded with `PUT upload/<file>` and query parameters

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PUT <repo URL>/upload/<file>?distribution=…&component=…` (chosen) | One request, trove names the stored file | One `curl --upload-file` in a CI job, no client tool; the parameters GitLab's Debian package API takes too; the path never conflicts with the canonical one | No standard Debian upload tool speaks it |
| (b) Matrix parameters on a free path (`PUT …/pool/x.deb;…`) | Coordinates in the path | Familiar from generic stores | Brittle (a stray space breaks the coordinates); a client-chosen path conflicts with Q-DEB-5 |
| (c) `dput` with a signed `.changes` | Debian's upload tool | The Debian way | Needs a keyring of uploaders and the processing of `.changes` files; a feature of its own (Deferred) |
| (d) Multipart `POST` | `curl -F` | Familiar | Nothing over (a) |

**Choice:** (a).

**Rationale:** a single authenticated request is what a CI job can run anywhere, and the canonical path stays trove's. The
development agent would revisit it when an owner workload uploads with `dput`.

**Applied in the spec:** R-DEB-006, R-DEB-028, R-DEB-029.

### Q-DEB-4 · A package file is immutable per package, version and architecture; an invalid package is refused

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Immutable; identical bytes again are idempotent and add coordinates; delete before replacing; invalid control data answers 422 (chosen) | One package, version and architecture is one set of bytes | APT caches and verifies by version: other bytes under a known version break every client that read the earlier index; "same version, other bytes" is a supply-chain pattern; nothing invisible is stored | A rebuilt package needs a new version or a deletion first |
| (b) Replace with `overwrite` | As raw files | Convenient for snapshots | Clients that saw the earlier index fail with hash mismatches; an overwrite hides a substitution |
| (c) Store invalid packages unindexed with a status | A stored file nobody can install | The upload does not fail | The failure is silent for the uploader and invisible to clients |

**Choice:** (a).

**Rationale:** APT's model is immutable versions, and a refusal is visible where it happens. The development agent would revisit
replacement if snapshot repositories for nightly builds become a workload, then as a setting of their own.

**Applied in the spec:** R-DEB-006, R-DEB-007, R-DEB-004.

### Q-DEB-5 · trove derives the pool path from the control data

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `pool/<prefix>/<source>/<package>_<version>_<architecture>.deb` (chosen) | The path is a function of the package | One path per package, version and architecture; Debian's pool layout; the path tells the package | A client cannot choose the path |
| (b) Any path the client names | The catalog's free paths | Flexible | The same package under two paths is two files; a path no longer tells what it holds |
| (c) With the component (`pool/main/…`) | Debian's exact layout | Familiar | A file in two components has two candidate paths |

**Choice:** (a).

**Rationale:** a derived path makes the immutability of Q-DEB-4 a path rule and the layout predictable. The development agent
would revisit it if a migration from another archive needs its paths kept.

**Applied in the spec:** R-DEB-008, R-DEB-003.

### Q-DEB-6 · Indexes are built by a coalescing job into generated items that one transaction publishes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A `coalesce` job per repository renders, compresses and signs, writes blobs, and one transaction publishes plain and by-hash generated items; a `Date` that never goes back (chosen) | R-ARCH-012 item 2 | Signed once; every replica serves the same bytes; a generation is all or nothing; by-hash history exists because files are kept | Visibility after an upload is bounded, not immediate |
| (b) Rendered per request from rows and cached per replica (R-ARCH-012 item 1) | Like npm documents | Read-your-writes | Each replica compresses and signs itself: byte identity across replicas depends on library versions, which a rolling upgrade breaks; an older generation for by-hash cannot be rendered from current rows |
| (c) Rendered in the upload's transaction | Synchronous | Immediate | Uploads of a suite serialize behind compression and signing; R-ARCH-012 item 3 forbids a publish that waits for an index |

**Choice:** (a), with the delay bound of R-DEB-010.

**Rationale:** APT's indexes are signed snapshots, which is the case R-ARCH-012 item 2 names. The development agent would revisit
the job's granularity (per suite instead of per repository) if one busy suite delays the others beyond the bound.

**Applied in the spec:** R-DEB-010, R-DEB-012, R-DEB-026; [Changes to other pages](#changes-to-other-pages) (R-ARCH-012,
Q-ARCH-380, ADR-170).

### Q-DEB-7 · The index set: `Packages.gz` always, `Packages.xz` on request, SHA-256 by-hash, no further index types

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) gzip always, xz by setting; `MD5Sum` and `SHA256` in `Release`; SHA-256 by-hash; no uncompressed file, no bzip2, LZMA or zstd; no `Sources`, `Contents` or translations for local and virtual repositories (chosen) | What APT needs | Every APT reads gzip; xz for large virtual indexes; `MD5Sum` costs nothing (blobs carry MD5) and helps other tools; full descriptions in `Packages` make translations unnecessary | A virtual repository shows remote packages with their short descriptions only |
| (b) The catalog's variants (gzip plus bzip2, LZMA, xz) | More variants | Matches the catalog | Two encoders no supported APT needs, one with no writer in the standard library |
| (c) Uncompressed files too | All forms | Readable with `curl` | Doubles the bytes of every large merged index for no client |

**Choice:** (a).

**Rationale:** each listed file is one APT reads. The development agent would revisit translations for virtual repositories if
users miss long descriptions of remote packages.

**Applied in the spec:** R-DEB-011, R-DEB-012, R-DEB-025.

### Q-DEB-8 · By-hash files are kept for a time window and at least three generations

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `deb.by_hash_keep` (1 h) after supersession, and always the last three generations (chosen) | Time and count | A slow client on a busy repository and a client of a quiet one both find their files | A busy repository keeps an hour of index files |
| (b) A fixed count of three generations | As the catalog's history | Bounded | A busy repository cycles three generations in seconds, so a client downloading a large index loses it mid-update |
| (c) Keep every generation | Never remove | Simple | Storage grows with every build |

**Choice:** (a). Removals happen only in the commit of a `deb-index` job of the repository, which also upserts every by-hash
item of the generation it publishes, so a removal and a publication never race (Q-DEB-33).

**Rationale:** the window a client needs is time, not builds. The development agent would revisit the default if the storage of
superseded virtual indexes grows beyond what operators accept.

**Applied in the spec:** R-DEB-012, R-DEB-021.

### Q-DEB-9 · Signing keys: one per repository, generated by trove, the private key sealed with the stored-secret key ring

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key per local and virtual repository, generated at creation, sealed in PostgreSQL with the key ring of R-ARCH-021, opened only by the index build; import possible (chosen) | trove holds and rotates the keys | No new Secret and no operator step; every worker can sign; a backup keeps the identity; rotation is an API call; trusting one repository trusts no other; the same custody as sealed upstream credentials | A holder of the ring and a database copy can sign for every repository (H-620); sealed keys are in backups |
| (b) One instance key in a Kubernetes Secret, mounted like the key ring | The operator generates it with gpg | The key is outside the database | A client that trusts it for one repository trusts every repository of every project; rotation needs roll-outs; the UI cannot create a key |
| (c) A key per repository in Kubernetes Secrets | One Secret per repository | Outside the database | A Secret and chart values per repository; self-service repositories cannot get a key without the operator |
| (d) An external key service or HSM that signs | The key never leaves it | Strongest custody | A new outbound dependency and a service the owner's stack does not have; later (Deferred) |
| (e) No signing | Clients use `trusted=yes` | Nothing to build | The owner asked for verification on the client |

**Choice:** (a). There is no instance-wide default key (PKG-DEB-019) and no key store shared by name.

**Rationale:** per-repository keys bind trust to one source, which is what `Signed-By` is for; the key ring already carries the
trust of sealed credentials, so the custody adds no new root of trust. The development agent would revisit (d) when an
installation must hold its keys in an HSM.

**Applied in the spec:** R-DEB-015, R-DEB-016, R-DEB-032; [Security notes](#security-notes); ADR-171.

### Q-DEB-10 · EdDSALegacy (algorithm 22) over Ed25519 in version 4 keys, SHA-512 signatures; imports also RSA of at least 3072 bits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Revised
after the review of 2026-11-08, which found the first wording ("version 4 Ed25519") ambiguous and its likely reading unreadable.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Public-key algorithm 22 (EdDSALegacy) over Ed25519, version 4 keys and signatures, SHA-512 (chosen) | The Ed25519 form GnuPG has read since 2.1 | The module's keys and signatures verified on `sqv` 1.3.0 and on `gpgv` 2.2.27, 2.2.40, 2.4.4 and 2.4.7, single and co-signed; small and fast | The module writes algorithm 27 by default, so the generator must ask for 22 explicitly; signatures carry a random salt notation of the module and are not deterministic |
| (b) Algorithm 27 (Ed25519 of RFC 9580) | The current standard's Ed25519 | Newest | GnuPG 2.2.27, 2.2.40 and 2.4.7 refuse it ("unsupported public-key algorithm (27)", verified): Debian 12 and Ubuntu 22.04 and 24.04 could not verify |
| (c) RSA 4096 generated | The classic | Read everywhere | Larger and slower for no client gain |
| (d) Version 6 keys | The newest key format | Current standard | Not read by GnuPG 2.2 |

**Choice:** (a). Imported keys may be algorithm 22 or RSA of at least 3072 bits; algorithm 27 and version 6 keys answer 422
`deb_key_unsupported`; a unit test asserts the algorithm octet of every generated key.

**Rationale:** the client images' verifiers read algorithm 22 and not 27. The development agent would revisit (b) when the oldest
supported client's GnuPG reads algorithm 27.

**Applied in the spec:** R-DEB-015, R-DEB-016,
[Client and library behaviour](#client-and-library-behaviour-this-page-relies-on).

### Q-DEB-11 · Rotation with a next key that co-signs, a promotion and a journaled retirement

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A next key signs alongside the primary and is served in `signing-key.asc`; a promotion retires the old key, erases it and journals the promotion (chosen) | An overlap in which both keys verify | Clients switch at their own pace inside the overlap; APT accepts a release with one trusted signature among several and refuses one with only unknown signers (verified on APT 3.0 with `sqv` and APT 2.4 with `gpgv`); a restore withholds the old key again (R-DEB-032) | Clients must replace a file; trove cannot see which did |
| (b) Switch at once | Replace the key | Simple | Every client breaks at the same moment |
| (c) A standby second key that does not sign | The catalog's primary and secondary | Familiar | Clients have nothing to verify with the new key before the switch |
| (d) A keyring package that trove publishes into each repository | Clients update the key through APT | Automatic distribution | A package trove would build, version and sign into every repository; a feature of its own |

**Choice:** (a).

**Rationale:** APT's verification accepts any one trusted signature (verified on both APT generations), so co-signing is the
overlap it supports; the new key's creation time an hour back (Q-DEB-30) lets a client that holds only the new key verify from
the first co-signed build on `sqv` too. The development agent would revisit (d) if fleets cannot replace keyring files by their
configuration management.

**Applied in the spec:** R-DEB-017, R-DEB-016, R-DEB-032.

### Q-DEB-12 · Clients fetch the public key from the repository's address; the setup installs it under `Signed-By`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `<repo URL>/signing-key.asc` with the repository's read permission; the deb822 snippet with a `Signed-By` path (chosen) | One key file per source | The key comes with the credentials the client already has; the address names the repository; rotation replaces one file | A package-scoped reader cannot fetch it (Q-DEB-20) |
| (b) A global, anonymous key endpoint by fingerprint | `/keys/<fingerprint>` | No credentials | Reveals repositories to anyone; not tied to the address a client configures |
| (c) The key inline in the `Signed-By` field | One file | No separate key file | Every sources file must be edited at a rotation; APT before 2.3 does not read it |

**Choice:** (a).

**Rationale:** the source and its key share the address and the permission. The development agent would revisit (c) as an
optional snippet if users ask for single-file setups.

**Applied in the spec:** R-DEB-018, R-DEB-029.

### Q-DEB-13 · A remote passes the upstream's release documents through byte for byte and refuses a rollback

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Byte for byte; between verified releases an older `Date` keeps the stored release; a `Date` more than 10 minutes ahead is refused; `Release` and `Release.gpg` as a pair (chosen) | The upstream's signatures reach the client | Clients verify the upstream with the keyrings they have; trove adds the rollback protection a client has only for itself (APT keeps its own newer list against an older `Date`, verified) | A remote serves whatever the upstream signed, also without trove's verification |
| (b) Re-sign a remote's indexes | trove signs everything | One trust anchor | Clients trust trove for the Debian archive; every remote needs a key and builds; the upstream's signature never reaches the client |
| (c) Pass through without checks | No parsing | Simple | A broken mirror's older release replaces a newer one for every client |

**Choice:** (a).

**Rationale:** the upstream's signature is the strongest statement a client can check, and trove must not weaken it. The
development agent would revisit (b) for a remote only together with a feature that changes upstream content (none exists).

**Applied in the spec:** R-DEB-019, R-DEB-021.

### Q-DEB-14 · Upstream signatures are verified against configured keys; required for a mirror and for a virtual member

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `deb.upstreamKeys` optional for a pull-through remote, required for a mirror and for a member of a virtual repository (chosen) | Verification where trove hands content on | A virtual repository signs only what trove verified; a mirror stores only verified content; a quick proxy of a third-party archive still works, with clients verifying | Operators supply the keys |
| (b) Always required | No unverified remote | Uniform | A pull-through remote of any archive needs its key first, although clients verify it anyway |
| (c) Never verified | Clients verify | Nothing to configure | A virtual repository would sign whatever an HTTP upstream or a party in between delivered |
| (d) Ship the Debian and Ubuntu archive keys as defaults | Built-in keys | No setup for the two archives | trove would follow every key change of two archives; every Debian host already has them |

**Choice:** (a).

**Rationale:** trust is handed on only where trove signs or keeps content for later; elsewhere the client's own verification
holds. The development agent would revisit (b) if unverified pull-through remotes cause incidents.

**Applied in the spec:** R-DEB-020, R-DEB-024, R-DEB-025; [H-623](#h-623--a-pull-through-remote-without-upstream-keys-stores-what-its-upstream-sends).

### Q-DEB-15 · A remote serves index files by the hash its stored release names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) By-hash paths as content verified by their digest; plain index paths served only while they match the stored release (chosen) | The release is the snapshot | Every replica serves the files one release names; archives with by-hash have no window at all | A plain path costs a second statement |
| (b) Plain index files as metadata documents with their own periods | Independent documents | No format logic | A refreshed `Packages` under an older `InRelease` gives every client a hash mismatch until both expire |
| (c) Rewrite the release to by-hash only | Force by-hash | No window | Changes signed bytes (Q-DEB-13) |

**Choice:** (a).

**Rationale:** the release document is the snapshot APT reasons about. The development agent would revisit (b) for flat archives
if their index names prove too irregular to check against the release.

**Applied in the spec:** R-DEB-021.

### Q-DEB-16 · A remote parses the `Packages` indexes it stores into rows, in a job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rows per index (package, version, architecture, path, digest, size), by the job `deb-remote-parse` (chosen) | One parsed source | Digests for pool fills, the shadowing check, mirror plans and screens from one table; off every client's path | Rows for every index a remote stores |
| (b) Parse in the fill's transaction | Rows with the index | No gap | Tens of thousands of rows inside a client's request |
| (c) No rows; stream indexes when needed | Nothing stored | Less data | A mirror plan or a shadowing check per name would read whole indexes per name |

**Choice:** (a).

**Rationale:** the archive's only per-package data is inside its indexes; parsing once serves four consumers. The development agent
would revisit (b) if the gap between an index fill and its rows matters to a workload with `checksumPolicy: require`.

**Applied in the spec:** R-DEB-022, R-DEB-023, [Mirror discovery](#mirror-discovery); Q-REPO-270.

### Q-DEB-17 · What a mirror of an APT archive covers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The named suites; components `main`, architectures `amd64` and `all`, translations `en`, no sources by default; their index files as clients request them; every package file they list within the patterns, versions and `maxBytes`; nothing else (chosen) | The operator names the slice | Bounded by default; serves `apt-get update` and `apt-get install` offline for that slice; versions counted per version | A client of another architecture or component falls back to pull-through |
| (b) The whole archive | Every suite, component, architecture and source | Complete | Terabytes for the Debian archive, and nothing says which part clients use |
| (c) Only what clients pulled | Pull-through | No setting | No offline guarantee |
| (d) The upstream's by-hash history too | Older index files | Complete history | Files nobody asks for after the next update |

**Choice:** (a). `includePrereleases` has no effect, because Debian versions carry no common pre-release marker; superseded index
files are housekept (R-DEB-021) while package files are never removed (R-REPO-051).

**Rationale:** the owner asked that a full mirror keep a chosen part of an upstream within R-REPO-051 and R-REPO-052; suites,
components and architectures are the dimensions an APT client chooses by. The development agent would revisit the defaults if the
owner's clients need `arm64` or `contrib` by default.

**Applied in the spec:** R-DEB-024, [Mirror discovery](#mirror-discovery), R-DEB-002.

### Q-DEB-18 · Virtual merge: the first member wins per package, version and architecture after claims; `Filename` names the member

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Members in resolution order; a paragraph kept unless an earlier member gave the same package, version and architecture or a claim or pattern refuses it; `Filename` rewritten to `members/<memberKey>/…` (chosen) | One paragraph per version, one member per paragraph | A client downloads from exactly the member whose digest it read, even when two members hold one path with other bytes; claims protect internal names | `Filename` differs from the member's own |
| (b) Keep the members' `Filename` and resolve pool paths by tiers | No rewrite | Paths as upstream | Two members with one path and other bytes (a local rebuild, two remotes) give a client bytes that do not match the paragraph it read |
| (c) Union without removing duplicates | Every paragraph | Nothing hidden | Duplicate versions with other digests; APT picks one at random |
| (d) The highest version wins across members | APT's own rule | Newest packages | Defeats claims: a public package with a higher version replaces an internal one |

**Choice:** (a).

**Rationale:** R-REPO-020 requires that the digest a client reads and the bytes it downloads come from one member; the member path
makes it a property of the URL. The development agent would revisit it if nested virtual repositories make member paths too long.

**Applied in the spec:** R-DEB-025, R-DEB-027.

### Q-DEB-19 · One merged index per suite for every caller who can read every member

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The merged index does not depend on the caller; reading it needs unrestricted `read` on every member (chosen) | One signed snapshot | One build, one signature, one by-hash history; no caller sees content of a member it cannot read | A caller who may read only some members cannot use the virtual repository's index |
| (b) A merged index per set of readable members, as R-REPO-020 caches npm documents | Per member set | Partial views work | A signed snapshot with its by-hash history per member set; unbounded builds |
| (c) Every member for every reader of the virtual repository | Ignore member reads | Simplest | Shows package names and control data of members a caller may not read (R-AUTHZ-009 rule 4) |

**Choice:** (a). The cache-fill right on a remote member is still decided per download (R-DEB-027).

**Rationale:** a signed snapshot cannot vary by caller; R-AUTHZ-009 rule 6 already asks the same of anonymous callers. The
development agent would revisit (b) if owner groups need virtual repositories over members they may only partly read.

**Applied in the spec:** R-DEB-025, R-DEB-027; Q-REPO-271.

### Q-DEB-20 · Index paths are coordinates without a package

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A path of the repository without a package: only grants without package patterns cover it (R-AUTHZ-005, Q-AUTHZ-160) (chosen) | Indexes need unrestricted read | A narrowed grant never reads names or control data of packages it may not read | A package-scoped reader cannot use the repository as an APT source |
| (b) A repository-level coordinate, as a search | Matches every grant | Narrowed readers can update | An index lists every package, so a narrowed grant reads all of them |
| (c) An index filtered per caller | Per-caller files | Exact | A signed snapshot per caller |

**Choice:** (a).

**Rationale:** the index is the whole suite; reading it is reading every package's metadata. The development agent would revisit
it with per-caller signed views, if ever.

**Applied in the spec:** R-DEB-003, R-DEB-004, R-DEB-018.

### Q-DEB-21 · Virtual suites, components and architectures are settings; a periodic refresh; a member without a stored copy keeps the previous index

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Settings name what the virtual repository serves; `deb-index-refresh` rebuilds after `mergedMetadataTtlSeconds`; a failing member with a stored copy contributes it, one without fails the build (chosen) | Bounded and proactive | Builds run off the client path; a client never sees an index without a member's packages | A new suite needs a setting |
| (b) Suites discovered from client requests | Implicit | No setting | A request for any name creates suites and upstream fetches |
| (c) Refresh on a client's read | Lazy | Only used suites refresh | A read that writes and enqueues; the client waits for a merge of large indexes |

**Choice:** (a).

**Rationale:** a virtual repository's indexes are signed artefacts built in the background like a local repository's. The
development agent would revisit (b) if operators find the suite list a burden.

**Applied in the spec:** R-DEB-025, R-DEB-026.

### Q-DEB-22 · Deletions, removed coordinates and key changes are journaled, and the replay withholds

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Revised
after the review of 2026-11-08: the first version let the replay erase a retired key's private key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `deb.package.deleted`, `deb.coordinates.removed`, `deb.signing_key.promoted` and `deb.signing_key.retired` in the journal; the replay withholds a deleted file, removes the coordinates again, promotes a restored next key and withholds keys, and never erases a private key (chosen) | As npm, NuGet and OCI | A package deleted for cause, a package pulled from a suite and a key retired because it leaked do not come back with a restore; the common rotation (a key created before the snapshot, promoted after it) keeps working after a restore; a forged entry costs availability until an administrator acts, never a key | A restore after a rotation whose key the backup lacks leaves a repository without a primary until an administrator acts |
| (b) No journal entries | The restore is the truth | Simple | A restore serves a malicious package again and signs with a leaked key |
| (c) The replay deletes: removes files, erases private keys | Exact | No withheld state | Irreversible on a mistaken or forged entry; a holder of the runtime S3 credential could forge one entry per repository and destroy every Debian signing identity after the next restore; against R-HA-054 and the replay handler's contract |

**Choice:** (a). Q-DEB-31 decides the key states; Q-DEB-34 the narrowings.

**Rationale:** the replay only restricts, never deletes (R-HA-054). The development agent would revisit the key case if restores
after rotations become routine.

**Applied in the spec:** R-DEB-009, R-DEB-013, R-DEB-015, R-DEB-017, R-DEB-032, R-DEB-033.

### Q-DEB-23 · Flat repositories only through remotes, by pull-through

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A remote serves a flat directory (`deb <url> binary/`) as a release directory; local and virtual repositories have no flat layout; mirrors take `dists/` suites only (chosen) | Proxy what exists, generate the modern layout | Third-party flat archives can be proxied; generated repositories keep by-hash | A flat archive cannot be mirrored yet |
| (b) A flat layout for local repositories | The catalog's deprecated layout | Matches old setups | No by-hash, no virtual membership, no client needs it |
| (c) No flat support | `dists/` only | Smallest | Flat archives of build services and vendors could not be proxied |

**Choice:** (a).

**Rationale:** the flat form matters only for archives trove does not control. The development agent would revisit mirrors of flat
archives when one is needed offline.

**Applied in the spec:** R-DEB-003, R-DEB-019, R-DEB-021.

### Q-DEB-24 · Libraries: an OpenPGP module, the module it requires, and an xz module; the `ar` reader in `untrusted`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Revised
after the review of 2026-11-08: the OpenPGP module brings a third module.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `github.com/ProtonMail/go-crypto` for OpenPGP (v1.5.2 at the review, which requires `github.com/cloudflare/circl` v1.6.3) and `github.com/ulikunitz/xz` (v0.5.17) for xz, each pinned once in `go.mod`; the `ar` reader written in `internal/formats/untrusted`; zstd from the pinned `klauspost/compress` (chosen) | Maintained pure-Go libraries behind a leaf package | Cleartext and detached signatures, armor, EdDSALegacy keys and verification of untrusted signatures from a maintained implementation; xz read (control archives, upstream indexes) and write (`Packages.xz`) from one module; the OpenPGP and xz modules are BSD-licensed and `circl` v1.6.3 BSD-3-Clause (verified) | Three new modules; `ulikunitz/xz` has one maintainer and a past denial-of-service advisory (CVE-2021-29482), so it is only ever read through the bounded readers of R-DEB-034; the xz writer's speed on large indexes is not measured (WP8's bound test) |
| (b) `golang.org/x/crypto/openpgp` | Already a dependency's sibling | No new module | Frozen and deprecated upstream; no EdDSA |
| (c) OpenPGP packets written by hand | No module | Signing is small | Verifying upstream signatures parses untrusted packets: a parser of its own to secure |
| (d) `gpg` or `sqv` as a subprocess | The verifiers clients use | Same code as clients | A binary in the image, a keyring on disk and a process per signature |

**Choice:** (a). The OpenPGP use sits behind the leaf package `internal/openpgp`, so no other package imports the module; the
generator asks the module for algorithm 22 explicitly (Q-DEB-10).

**Rationale:** parsing untrusted OpenPGP is a job for a maintained library. The development agent would revisit the choice if a
module stops being maintained, and the xz writer if WP8's bound test misses with it.

**Applied in the spec:** R-DEB-007, R-DEB-016, R-DEB-020, R-DEB-034; Q-ARCH-382, Q-ARCH-383.

### Q-DEB-25 · No upload waits for an index; an index state read can wait

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The upload answers with `changeSeq`; `GET …/deb/index?waitFor=` waits up to `deb.index_wait_max` (chosen) | A separate read waits | R-ARCH-012 item 3 holds; CI jobs and tests wait without sleeping | Two requests |
| (b) `?wait=true` on the upload | The upload waits | One request | Forbidden by R-ARCH-012 item 3; ties every upload to the slowest build |
| (c) Nothing to wait with | Eventual only | Nothing to build | Scripts sleep and guess |

**Choice:** (a).

**Rationale:** the rule keeps uploads fast; the read gives scripts a deterministic point. The development agent would revisit the
polling inside the wait if it shows in the database load.

**Applied in the spec:** R-DEB-006, R-DEB-014.

### Q-DEB-26 · No exposure rules, no Bastion and no vulnerability matching for the format in this phase

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The format has no exposure adapter and is not served on an endpoint with exposure rules or through the Bastion; Debian packages are not matched against advisories yet (chosen) | Later | An endpoint's narrowed view of a signed index is the problem of Q-DEB-19 per endpoint; Debian advisories are keyed by source package and release | These features wait |
| (b) Build them now | Complete | Uniform with npm | Each needs a design of its own |

**Choice:** (a).

**Rationale:** each needs its own mapping onto signed indexes or source packages. The development agent would revisit it when an
endpoint or an audit needs Debian content.

**Applied in the spec:** [Deferred](#deferred); [Changes to other pages](#changes-to-other-pages) (endpoints.md,
vulnerability-scanning.md).

### Q-DEB-27 · The real clients: four images, verification on, a stub archive generated in code

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `debian:13`, `debian:12`, `ubuntu:24.04` and `ubuntu:22.04` pinned by digest; `--error-on=any`; never `trusted=yes`; the rotation row on Debian 13, Ubuntu 24.04 and Ubuntu 22.04; a stub archive generated in code (chosen) | Both verifiers and the oldest supported LTS | `sqv` (Debian 13) and `gpgv` 2.2 and 2.4 both verify trove's signatures; the rotation runs on both verifier generations; a warning fails a row (`--error-on=any` exists on APT 2.4, verified by the review's probes); deterministic data | Four images per row |
| (b) One image | Faster | Smaller matrix | Misses the verifier difference |
| (c) Responses recorded from the Debian archive | Real data | Realistic | Third-party data in the repository; recorded signatures expire |

**Choice:** (a). One cluster shares one clock, so the time rules of Q-DEB-30 are proven by integration asserts, not by e2e.

**Rationale:** the client's verification is the requirement, and it differs between the releases. The development agent would
revisit the matrix when Ubuntu 22.04 leaves standard support.

**Applied in the spec:** R-DEB-035, R-DEB-016, R-DEB-017.

### Q-DEB-28 · Source packages only through remotes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A remote passes `Sources` and source files through, a mirror keeps them with `sources: true`; local and virtual repositories have none (chosen) | `apt-get source` through remotes | Works for upstream sources at no cost | Organisations cannot publish their own sources yet |
| (b) Local source uploads now | `.dsc` and its files | Complete | A three-file upload with cross-checks and a `Sources` renderer for no current workload |

**Choice:** (a).

**Rationale:** pull-through of sources costs nothing; publishing them is a feature of its own. The development agent would revisit
(b) when a workload publishes sources.

**Applied in the spec:** R-DEB-011, R-DEB-021, R-DEB-024, R-DEB-025.

### Q-DEB-29 · Default upstream, cache periods and configuration defaults

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `https://deb.debian.org/debian/`; metadata 600 s, not-found 300 s; `deb.index_delay` 2 s, `deb.virtual_index_interval` 60 s, `deb.by_hash_keep` 1 h, `deb.index_wait_max` 30 s (chosen) | Debian's content network over HTTPS | TLS by default; by-hash; releases change a few times a day, so ten minutes keeps clients current at a conditional request each | Ubuntu users change the URL |
| (b) The Ubuntu archive | `http://archive.ubuntu.com/ubuntu/` | Popular | Plain HTTP by default |
| (c) No default upstream | Empty form | Neutral | R-REPO-052's bound for well-known upstreams keys on it |

**Choice:** (a).

**Rationale:** the default is the archive most installations start from, over TLS. The development agent would revisit the periods
after the first owner installation reports upstream load.

**Applied in the spec:** R-DEB-001, R-DEB-010, R-DEB-012, R-DEB-014.

### Q-DEB-30 · Release dates and signature times trail the database clock by a minute, key creation times by an hour

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08, which showed that the first rule (Date = the database clock) breaks Debian 13 clients.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Date` and signature time = the build's PostgreSQL time minus 60 s, at least the previous `Date` + 1 s, the build waiting instead of running ahead by more than a second; key creation = the creating transaction's PostgreSQL time minus one hour; `deb.index_delay` at least 1 s; imports created in the future refused (chosen) | Times behind the clock | `sqv` refuses a signature 2 s from its future and APT a `Date` more than 10 s ahead (verified); a client clock up to 59 s behind the database still verifies; `sqv` refuses a signature older than its key (verified), so keys an hour back let a "new key only" client verify from the first co-signed build | Index dates read a minute old |
| (b) `Date` = the database clock, at least previous + 1 s (the first version) | Exact time | Simple | A database clock 2 s ahead of a client breaks that client after every build on `sqv`; more than one build per second pushes the `Date` past APT's 10 s; a key created at "now" makes its first signatures older than the key on a client behind the clock |
| (c) Use each replica's clock | No database read | Simple | Replicas disagree; a `Date` could go back across builds on different workers |

**Choice:** (a). Integration asserts hold every signature time at least 30 s before the database's time and after its key's
creation (R-DEB-010, R-DEB-016); one kind cluster shares one clock, so e2e cannot show these failures.

**Rationale:** both verifiers refuse the future and `sqv` refuses signatures older than their key; trailing the clock is the only
side that never fails a client whose clock is a little behind. The development agent would revisit the margins if clients with
clocks more than a minute behind become a support case.

**Applied in the spec:** R-DEB-010, R-DEB-015, R-DEB-016, R-DEB-017,
[Client and library behaviour](#client-and-library-behaviour-this-page-relies-on).

### Q-DEB-31 · A key the replay finds retired or replaced is withheld, never erased; promotions are journaled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A state `withheld` (neither signs nor is served, sealed private key kept), set by the replay; an administrator confirms (erase) or reinstates; `deb.signing_key.promoted` journaled with both fingerprints so the replay promotes a restored next key; a fingerprint is unique in every state, so a retired or withheld key cannot be imported again (chosen) | Restrict, never delete | Within R-HA-054's contract; a forged entry is reversible; the common rotation survives a restore; no back door through import | A repository can be left without a primary after a restore until an administrator acts |
| (b) The replay erases the private key (the first version) | Retire for good | A leaked key is gone at once | Breaks the replay contract; a forged entry destroys a repository's signing identity for good |
| (c) The replay ignores key entries | Simple | Nothing to build | A restore revives a key retired because it leaked, and it signs again |

**Choice:** (a).

**Rationale:** a key is content the replay must not destroy, and a withheld key signs nothing, which is the restriction that
matters. The development agent would revisit an automatic confirmation if withheld keys pile up after restores.

**Applied in the spec:** R-DEB-015, R-DEB-017, R-DEB-032, R-DEB-033; Q-DEB-22.

### Q-DEB-32 · A virtual repository merges only verified, unexpired, not rolled-back member releases, and carries their earliest Valid-Until

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A member release counts only while a fingerprint of the member's current keys verified it, its `Valid-Until` has not passed and its `Date` is not older than the one the previous generation merged; a key removal clears `verified_by` of what the removed key verified; the virtual `Release` carries the earliest member `Valid-Until`; a mirror pass uses verified, unexpired releases only (chosen) | Hand on trust only while it holds | trove never re-signs content nobody verified, content verified by a key the operator withdrew, a frozen suite, or an older release a remote accepted after losing its copy; freeze protection reaches the virtual repository's clients | A build fails (and the previous generation stays) while a member release is stale |
| (b) Merge whatever release the member holds (the first version) | Trust the cache | Simple | Launders unverified, withdrawn-key, expired and rolled-back upstream content into a fresh trove signature |
| (c) Check verification only | Partial | Simpler | An expired upstream suite would be served as fresh by the virtual repository's signature |

**Choice:** (a).

**Rationale:** a virtual repository's signature is a statement trove makes about upstream content; it may only make it while the
upstream's own statement holds. The development agent would revisit the `Valid-Until` carry-over if an upstream's short validity
makes virtual builds fail more often than the upstream itself.

**Applied in the spec:** R-DEB-011, R-DEB-020, R-DEB-024, R-DEB-025, R-DEB-026, [Mirror discovery](#mirror-discovery).

### Q-DEB-33 · Retention and housekeeping run only inside the repository's index job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Removals only in the commit of a `deb-index` job under `index:<repository id>`; the periodic task only enqueues such jobs; the commit upserts every by-hash item of its generation (chosen) | One serialized writer per repository | A build cannot publish a generation whose by-hash file a concurrent cleanup just removed; belt and braces through the upsert | A repository without changes needs a job for its retention |
| (b) Retention in the periodic task (the first version) | A separate cleaner | Simple | Races builds: a reused by-hash item deleted by the task leaves a published generation with a 404 file |

**Choice:** (a).

**Rationale:** the publication and the removal of one file must not interleave. The development agent would revisit it if the
retention jobs add noticeable queue load.

**Applied in the spec:** R-DEB-010, R-DEB-012, R-DEB-021, R-DEB-026.

### Q-DEB-34 · Trust narrowings are journaled: removed upstream keys and removed coordinates

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08 (the class of narrowings F-72 and F-74 dealt with).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Journal both: `debUpstreamKeysRemoved` as a part of `repository.narrowed` (R-REPO-061) and `deb.coordinates.removed`; the replay removes them again, fail-closed: a remote left without keys that mirrors or is merged is disabled, a file left without coordinates is withheld (chosen) | Narrowings survive a restore | A restore does not bring back an upstream key the operator removed because it leaked, nor put a pulled package back into `stable` | Two more journal entries; an exception to Q-REPO-246's rule that format members are not journaled |
| (b) Record both as gaps | Known limits | Nothing to build | A leaked upstream key or a pulled package returns with every restore |
| (c) Journal every Debian setting change | Complete | Uniform | Widening changes are not narrowings; the journal only narrows (R-HA-054) |

**Choice:** (a).

**Rationale:** both changes withdraw trust that a restore must not re-grant. The development agent would revisit other format
members if one of them turns out to carry trust too.

**Applied in the spec:** R-DEB-009, R-DEB-020, R-DEB-032, R-DEB-033; Q-REPO-274.

### Q-DEB-35 · Remote release checks: no rollback between verified releases, no Date more than ten minutes ahead, keys valid now

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PrepareFill` refuses a `Date` more than 10 minutes after the database's time; the anti-rollback keeps the stored release only when both releases verified; a signing key must be valid now and at the signature's time, created before it, and the signature time no more than 10 minutes ahead; the `Hash` armor header is checked only when present (chosen) | Bounded checks | One forged or mistaken far-future release can no longer freeze a remote for good; an expired key left in the set cannot sign a backdated release; archives that omit the deprecated header work | A clock error upstream of more than 10 minutes is refused until it is fixed |
| (b) Anti-rollback by `Date` for every release, keys valid at signature time (the first version) | Simple | Strict | A far-future `Date` (forged on an unverified HTTP remote, or an upstream clock error) makes every genuine release "older" forever; a backdated signature by an expired key passes |
| (c) No anti-rollback | Nothing to freeze | Simple | A replayed older release replaces a newer one for every client |

**Choice:** (a). Ten minutes is far beyond APT's own 10 s, so trove refuses no release a client would accept for clock reasons.

**Rationale:** each rule closes one way to freeze a remote or to slip in an old signature. The development agent would revisit the
tolerance if a real upstream's clock drifts beyond it.

**Applied in the spec:** R-DEB-019, R-DEB-020, R-DEB-034.

### Q-DEB-36 · A remote's rows follow its release, not its clients' fetches

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A newly stored release enqueues the parse of every index path with rows whose hash it changes; the job fetches the full index through the background path; a virtual build enqueues the parse of what it merged (chosen) | Rows track the release | APT applies pdiffs and may never fetch a full index again; the rows stay current anyway | One background fetch of each changed index per release |
| (b) Parse only what clients fetch (the first version) | Lazy | No extra fetch | With pdiffs the rows stay stale indefinitely, and with them digests, the shadowing check and screens |
| (c) Parse pdiffs | Incremental | Fewer bytes | A pdiff applier in trove for no client need |

**Choice:** (a). The parse job is `coalesce` per index path and reads the hash the stored release names when it runs, so a
release stored during a run is parsed by the waiting job and no trigger is lost (revised after the second review of
2026-11-08). Which suites of the Debian archive publish pdiffs is not verified; the rule holds either way.

**Rationale:** the rows describe the release the remote serves. The development agent would revisit (c) if full-index fetches
cost noticeable upstream traffic.

**Applied in the spec:** R-DEB-023, R-DEB-026, [H-625](#h-625--pool-files-no-parsed-index-names-are-stored-unverified).

### Q-DEB-37 · The shadowing check asks a remote in the scope of the virtual repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `NameServer` answers for the suites, components and architectures the virtual repository serves: served when a parsed row of that scope lists the name, not served when every listed index of the scope is parsed and none lists it, undetermined otherwise (the parse is enqueued); virtual builds parse what they merge (chosen) | Decide on what V exposes | A name that exists only in an unparsed component can no longer read as "not served"; a name V does not expose does not block an upload | Before V's first build an upload of a new name can be undetermined (503) for a caller without `manage` |
| (b) Answer from whatever happens to be parsed (the first version) | Simple | No scope | Partial parses let a name be shadowed; no parse at all refuses every first upload |
| (c) Fetch the missing indexes during the upload | Decided at once | No 503 | Large upstream fetches inside a client's upload request |

**Choice:** (a).

**Rationale:** the shadowing check protects V's clients, so V's scope is the question. The development agent would revisit (c) if
the 503 window before a virtual repository's first build shows up in practice.

**Applied in the spec:** R-DEB-023, R-DEB-026; Q-REPO-270.

### Q-DEB-38 · A virtual merge streams in member order

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08, which found the global order of the first version incompatible with streaming.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Member order, and within a member the member's own order; a set of 128-bit hashes of the kept package, version and architecture (chosen) | One pass per member index | Memory bounded by the set, not by 50 MB indexes; deterministic for equal inputs | The merged index is not sorted by name (APT does not need it) |
| (b) Sort in memory | Global name order | Matches local repositories | Holds whole indexes in memory |
| (c) External sort | Global order, bounded memory | Sorted | Spool files and a merge phase per build for no client need |

**Choice:** (a).

**Rationale:** APT reads paragraphs in any order; boundedness matters, order does not. The development agent would revisit (c) if
a tool that reads trove's indexes needs sorted input.

**Applied in the spec:** R-DEB-011, R-DEB-025.

### Q-DEB-39 · The flat root `./` reaches the format without a redirect

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The dispatcher hands the format the path as received when its first segment after the repository address is `.`; the format reads a leading `./` as the flat root; other dot segments answer 400 (chosen) | Serve what APT asks for | APT sends `<url>/./InRelease` for `deb <url> ./` (verified); no dependence on APT following a redirect | A second case of uncleaned paths in the dispatcher (Q-ARCH-42's revisit condition) |
| (b) Let `ServeMux` redirect to the cleaned path | Nothing to build | Simple | Whether APT follows the redirect for every request is not verified; a redirect per index request |
| (c) No flat root | Directory flat repositories only | Smallest | Archives whose source line is `./` could not be proxied |

**Choice:** (a). Index files in flat directories are recognised by their file names (R-DEB-003), so a `.deb` beside them is a
package file.

**Rationale:** the format must answer the path APT sends. The development agent would revisit (b) if the dispatcher's exception
proves costly and APT is shown to follow the redirect.

**Applied in the spec:** R-DEB-003, R-DEB-019, R-DEB-021; Q-ARCH-385.

### Q-DEB-40 · A key's binding self-signature carries the key's creation time, and signatures wait for its earliest binding

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the second review of 2026-11-08, which verified that `sqv` refuses a data signature older than every binding self-signature
of its key, even when the key's creation time is older; `gpgv` accepts it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The generator makes the self-signature at the key's creation time (the module's `Config.Time`); every key records `bound_since`, its earliest valid binding self-signature; a signature's time is at least the later of the key's creation and `bound_since`, plus one second, and the build waits when that is still ahead (chosen) | Sign only after the key is bound | A "new key only" client on Debian 13 verifies from the first co-signed build, for generated and imported keys alike; an import whose only self-signature is fresh (an expiry removed just before) works after about a minute | A build after such an import waits about a minute |
| (b) Generate as before, self-signature at "now" | The module's default | No change | Signatures at now − 60 s predate the self-signature: `sqv` refuses them, and step 1 of a rotation breaks for "new key only" clients |
| (c) Refuse imports whose self-signature is younger than the key | Strict | No wait | Refuses the common case of a key whose expiry the operator just removed |

**Choice:** (a). WP2's unit test asserts that a generated key's self-signature time equals its creation time; the e2e rotation row
on Debian 13 runs once more with an imported key whose only self-signature is fresh.

**Rationale:** `sqv` evaluates the key's binding at the data signature's time, so trove must not sign before the binding exists.
The development agent would revisit the wait if imports with fresh self-signatures delay rotations noticeably.

**Applied in the spec:** R-DEB-015, R-DEB-016, R-DEB-017,
[Client and library behaviour](#client-and-library-behaviour-this-page-relies-on).

### Q-DEB-41 · A suite of the virtual repository's scope that the remote never fetched leaves the shadowing check undetermined

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the second review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) "Not served" needs every suite of V's scope settled for R: a stored release with all indexes of the scope parsed, or a PostgreSQL record `absent` written when the upstream answers 404 or 410 for the suite's release documents (expiring with the not-found period); anything else is undetermined and enqueues the fetch and the parse (chosen) | Decide only on what is known | A name in a suite R never fetched can no longer read as "not served" and be shadowed; a suite the upstream lacks settles without a parse | The first upload of a new name can answer 503 until the fetch ran |
| (b) Decide on the stored releases only (the first revision) | Vacuous for missing suites | No fetch | A suite without a stored release counts as "every listed index parsed": a name there can be shadowed |
| (c) Fetch the missing release and indexes during the upload | Decided at once | No 503 | Large upstream fetches inside a client's upload |

**Choice:** (a). A negative entry in Valkey never settles a suite, because a value from Valkey may refuse, never admit
(R-REPO-048 step 2).

**Rationale:** the check may admit a deploy holder's upload only on facts stored in PostgreSQL. The development agent would revisit
(c) if the 503 window shows up in practice.

**Applied in the spec:** R-DEB-019, R-DEB-023; Q-REPO-270.

### Q-DEB-42 · After a key change, releases are re-verified from their bytes or fetched without validators; a verified release sets a floor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the second review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The remote's `deb-index` job re-verifies the releases whose `verified_by` a key removal cleared from their stored bytes; one the new set does not verify is revalidated without validators, so a 304 never marks it verified; `floor_date` (the newest `Date` ever verified for the directory) survives the clearing, and while keys are set no older release is stored (chosen) | Verify bytes, never a status code; keep the floor | A release signed by a remaining key needs no upstream request; a re-signed upstream release is fetched and verified; an older release signed by a remaining key cannot replace one that was verified once | A directory whose release no remaining key verifies fails closed until the upstream publishes one at or above the floor |
| (b) Clear `verified_by` and rely on the next revalidation (the first revision) | Lazy | Simple | An upstream 304 renews the unverified copy; an older release signed by a remaining key replaces the newer one |
| (c) Delete the stored releases on a key change | Start over | Clean | Loses the floor; a rollback right after the change is accepted |

**Choice:** (a). The floor goes with the cache rows in a restore without remote caches
([H-630](#h-630--a-restore-without-remote-caches-forgets-the-newest-upstream-release-a-remote-had-seen)).

**Rationale:** trust is a property of the bytes and a key set, not of an HTTP status; the floor keeps the rollback protection across
a key change. The development agent would revisit the fail-closed case if operators remove keys before an upstream re-signs.

**Applied in the spec:** R-DEB-019, R-DEB-020, R-DEB-023, R-DEB-024, H-630.

### Q-DEB-43 · A virtual repository's record of a member's release Date is dropped when the member's upstream changes or it leaves

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added
after the second review of 2026-11-08.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The recorded `Date` goes when the member's upstream identity changes (R-REPO-011) and when the member leaves the virtual repository; a reindex keeps it; removing and re-adding the member is the deliberate way to accept an older release (chosen) | Reset on changes that make the record meaningless | A member pointed at another archive is not compared with the old one's dates; accepting a rollback is an explicit, audited membership change (`repository.narrowed` with `membersRemoved`) | An operator must know the remove-and-re-add step |
| (b) No reset (the first revision) | The record is forever | Simple | A member re-pointed at another archive fails `member_rollback` until its dates pass the old ones |
| (c) Also reset on every reindex | Convenient | One step | A routine reindex silently accepts a rolled-back upstream release |

**Choice:** (a).

**Rationale:** the record protects against a rollback of the same upstream; a different upstream or a re-added member starts anew
only through a change an administrator makes on purpose. The development agent would revisit (c) as an explicit flag if operators
ask for a one-step way.

**Applied in the spec:** R-DEB-025, R-DEB-029 (troubleshooting).

### Q-DEB-44 · The 413 of a crossed bound carries the code `payload_too_large`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-005 listed `content_too_large` for a bound of R-DEB-034. The bounded readers of `internal/formats/untrusted` (R-ARCH-028)
answer 413 with the registered code `payload_too_large`, and no code `content_too_large` exists.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `payload_too_large` (chosen) | The format's table names the code the readers raise | One code for every format; the tests that hold the readers hold the format | The page's first wording changes |
| (b) A second code `deb_content_too_large` | The format wraps the readers' error | The code names the format | Every reader's error is rewritten for one format, and clients of other formats see another code for the same bound |

**Choice:** (a).

**Rationale:** the code is the contract of the bound, not of the format. The development agent would revisit it if the format needed to tell
a crossed bound of a package from a crossed bound of an index.

**Applied in the spec:** R-DEB-005. Code: `internal/formats/untrusted` (`TooLarge`), `internal/formats/deb/errors.go`.

### Q-DEB-45 · One route per path shape, with the class decided by the handler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-003 gave the same path different kinds per class (a release document is `Computed` for a local repository and `Metadata` for
a remote one) and different suite grammars (a remote's suite may hold slashes, `stable/updates`). The format's `Match(method, path)`
does not know the class of the repository, and `formats.MatchRoutes` fails a request that two routes accept; the dispatcher checks the
class of the matched route afterwards (405 for a class that does not serve it).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One route per path shape for all classes; unified kinds; the handler decides what a class serves (chosen) | `deb.dists.get` (`Metadata`), `deb.dists.by_hash` (`Download`), `deb.pool.get` (`Download`), `deb.signing_key.get` (`Computed`); the remote-only routes (flat repositories) and the local-only ones (upload, delete) have disjoint paths | No ambiguity in any class combination; the kit's ambiguity check holds the table for the full format | A local repository's 404 for `Packages.bz2` is the handler's and no longer the router's |
| (b) Extend `Match` with the class | The contract grows by a parameter | Strict per-class tables | A change of the frozen contract (ADR-141) for one format |
| (c) Prefix the remote's routes | `remote/dists/...` | Disjoint paths | Not what APT requests |

**Choice:** (a). The kind of a release document or index is `Metadata` for every class (rendered from rows or fetched, both are what that
kind means), of a by-hash file `Download` (immutable content addressed by its hash, in every class). `pool/` is served by every class,
which R-DEB-027 needs for a virtual repository.

**Rationale:** the headers and the cache behaviour R-DEB-005 gives each file do not depend on the route kind. The development agent would
revisit it if the dispatcher learned to match by class.

**Applied in the spec:** R-DEB-003. Code: `internal/formats/deb/routes.go`.

### Q-DEB-46 · A Debian version names its file only through the local class, so the skeleton has no `VersionPather`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the decision point decides a version on the path of its file as well as on the package document's (Q-AUTHZ-255), through the
optional `VersionPather`. A Debian package has no document, and a version has one file per architecture below the pool directory of its
source package: `VersionPath(name, version)` cannot name a path from the package and version alone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The skeleton has none; the local class decides it with its package screens (chosen) | The kit's fixture holds no pool path of the form `<package>_<version>_<arch>.<ext>` until then; [H-720](#h-720--a-path-pattern-that-hides-a-package-file-does-not-hide-its-version-from-the-package-screens) states the gap | No wrong answer is frozen into the contract of the format; the skeleton has no version rows to list | A gap until the local class lands |
| (b) A guessed path (`pool/<p>/<name>/<name>_<version>_all.deb`) | A path that is right for nothing but `all` files with source = package | The kit is satisfied | A pattern that hides the real file would not hide the version |
| (c) A second optional interface that names every file of a version | The contract grows | Exact | A contract change before a second format needs it |

**Choice:** (a).

**Rationale:** the work package of the local class chooses between (c) and a rule that reads the stored files of a version through the
data port (`Find`), with the architecture reviewer; the development agent would revisit this choice when it does.

**Applied in the spec:** none until the local class lands. Code: the fixture of `internal/formats/deb/conformance_test.go`.

Revised by [Q-DEB-47](#q-deb-47--the-versionpath-of-a-debian-version-is-the-architecture-independent-file-of-a-source-package-of-the-packages-name).

### Q-DEB-47 · The `VersionPath` of a Debian version is the architecture-independent file of a source package of the package's name

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [Q-DEB-46](#q-deb-46--a-debian-version-names-its-file-only-through-the-local-class-so-the-skeleton-has-no-versionpather) left
the choice to the local class. `VersionPather.VersionPath(name, version)` receives no store and no source package; a version has one
file per architecture below the pool directory of its source package.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No `VersionPather`; the version is decided on the package only | Nothing is guessed | No wrong path | A path pattern on a version's file never hides the version, for any package (the gap H-720 described) |
| (b) The path `pool/<prefix>/<name>/<name>_<version without epoch>_all.deb` (chosen) | The file of the source package that bears the package's name, for architecture `all` | A pattern that addresses the package by name or the version by its file name hides the version; no contract change | A pattern that addresses a binary package by the directory of another source package, or one architecture, does not hide the version ([H-720](#h-720--a-path-pattern-that-hides-a-package-file-does-not-hide-its-version-from-the-package-screens)) |
| (c) A second optional interface that names every file of a version | The contract grows (ADR-141) | Exact | A change of the frozen contract for one format; the decision point would read the data port |

**Choice:** (b).

**Rationale:** the contract is frozen and the decision point asks without a store; (b) closes the common case, a pattern that names the
package or the version, and leaves the rest as a stated gap. The development agent would revisit (c) when a second format has a version
with several files.

**Applied in the spec:** R-DEB-004. Code: `VersionPath` in `internal/formats/deb/paths.go`.

### Q-DEB-48 · The change of coordinates runs behind a service of the management API that the composition root implements

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `PUT …/deb/coordinates` is a route of the management API, so it has no format `Request`, no `Host.Write` and no package writer.
It needs the decision point per side (deploy, delete), the ownership rule inside the transaction, the data port, the item properties
and the audit events. `internal/api/v1` imports no format package.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `v1.Options.Deb` holds a `DebCoordinateService`; the handler resolves the repository and the caller and passes the decision and the ownership as functions; `internal/app/wire_deb_api.go` implements it over the data port (chosen) | The handler stays thin; the service is the one place that knows the rows | A function-valued request | A second place besides the format that writes the file's rows |
| (b) The handler imports the format's rows and store | No service | One file | The API layer would depend on a format, which the layers forbid |
| (c) The change is a route of the format under `/api/deb/` | The format's own pipeline | A second address for an operation the spec puts into the management API | R-DEB-009 and the OpenAPI document name the management route |

**Choice:** (a).

**Rationale:** it keeps the layers and reuses the decision point and the ownership rule of the request. The development agent would
revisit it when a second format needs a management operation on its rows.

**Applied in the spec:** R-DEB-009. Code: `internal/api/v1/deb_coordinates.go`, `internal/app/wire_deb_api.go`.

### Q-DEB-49 · A crossed bound is 422 `validation_failed` with the field error `out_of_range`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-009 named a code `out_of_range` for 64 suites, 32 components and 32 coordinates. The registry of problem codes has no
such code; `out_of_range` is a field error code under `validation_failed` (repositories, claims).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 422 `validation_failed` with the field error `out_of_range` at `/coordinates` (chosen) | The shape every other bound of the API has | One shape for clients | The problem `code` is not `out_of_range` |
| (b) A new problem code `deb_out_of_range` | The format names it | Findable | A code for what the field error already says |

**Choice:** (a).

**Rationale:** the clients of the API already read field errors. The development agent would revisit it if a client needed the bound
without parsing the fields.

**Applied in the spec:** R-DEB-009, R-DEB-006. Code: `OutOfRange` in `internal/formats/deb/answer.go`, `checkBounds` in
`internal/formatdata/deb/local_store.go`.
### Q-DEB-50 · The rows of a package file are detached from the item, and the removal hook deletes them

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the item store deletes the item rows of a permanent removal and then runs `OnItemsRemoved` ("after the rows were deleted"). The
migration of the skeleton gave `deb_binaries.item_id` a cascading foreign key to `items`, so the row of the file (package, version,
architecture) and its coordinates were gone before the hook ran, and the hook could neither raise the suites of the file nor journal the
deletion (R-DEB-013, R-DEB-032).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Drop the foreign key (migration 00063); the hook reads the row, raises the suites and deletes the row (chosen) | The row outlives the item for the length of the transaction | The hook sees everything it needs; no change of the item store or the contract | Nothing in the schema stops a row from outliving its item if a removal path skipped the hook; every path runs the hook |
| (b) Keep the cascade; take a snapshot in `LockRemoval` and carry it to the hook in the transaction | The hook reads the snapshot | The schema keeps its guarantee | `LockRemoval` is not called for every removal; the snapshot travels in an untyped transaction value |
| (c) Change the item store to run the hook before it deletes | The hook sees the item | The simplest hook | A change of the order every format relies on (nuget and npm read digests from the event) |

**Choice:** (a).

**Rationale:** the format owns its rows, and the hook is the one place that removes them; `trove admin` checks of the schema do not
need the foreign key. The development agent would revisit (c) if a second format needed its rows at removal time.

**Applied in the spec:** R-DEB-013. Code: `internal/meta/postgres/migrations/00063_deb_binaries_detached.sql`, `Removed` in
`internal/formatdata/deb/local_store.go`, `OnItemsRemoved` in `internal/formats/deb/hooks.go`. Test: `test/integration/postgres/deb_test.go`.

### Q-DEB-51 · Retention keeps the current generation, the superseded ones within the keep time and the three newest superseded ones

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-012 says "the last three whatever their age". It does not say whether the current generation is one of the three.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The current generation, every superseded one younger than `deb.by_hash_keep`, and the three newest superseded ones (chosen) | Four generations stay at least | A client that read `InRelease` three builds ago can still fetch its files | One generation more than the other reading |
| (b) The three newest generations including the current one | Two superseded ones stay | Less to keep | A burst of three builds in seconds takes the first generation's files away from a client that is still fetching them |

**Choice:** (a).

**Rationale:** the by-hash files are small and a client that is slow loses an `apt-get update`. The development agent would revisit it if
the retained files grew a cost.

**Applied in the spec:** R-DEB-012. Code: `Retain` in `internal/formats/deb/index/retention.go`. Test: `TestR_DEB_012_Retention`.

### Q-DEB-52 · A build without a usable key marks its suites failed and succeeds; any other failure is retried

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-010 says a failed build is retried by R-ARCH-016 and R-DEB-016 says a build without a primary key publishes nothing and the
suite is `failed` with `no_signing_key`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `no_signing_key` marks the suites failed and ends the job without an error; every other failure marks the suite failed and fails the job, which is retried (chosen) | Five attempts for what can pass | No retry of what only a person can fix; a key change or the restore's fix-up enqueues a build | The job list shows a success for a repository that cannot publish; the state read and the metric show it |
| (b) Fail the job for a missing key as well | Uniform | Visible in the job list | Five identical failures, then a dead job, for a condition no retry changes |

**Choice:** (a).

**Rationale:** a missing key is a state, not a fault of the attempt. The development agent would revisit it if operators looked for the
condition in the job list.

**Applied in the spec:** R-DEB-010, R-DEB-016. Code: `Indexer.Build` in `internal/formatdata/deb/build.go`.

### Q-DEB-53 · Each suite is published in a transaction of its own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-010 says "one transaction publishes every file of a generation". A build can be for several suites.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One publication per suite (chosen) | The generation is a suite's | A reader sees a suite's previous generation or its new one; a failure of one suite does not hold back another; the monotonic condition is per suite | A client that reads two suites may see one new and one old |
| (b) One publication for all suites of a build | Atomic across suites | A consistent view of the repository | A suite that cannot be built (a bad row) blocks every other; `built_seq` is per suite anyway |

**Choice:** (a).

**Rationale:** `Release` documents are per suite and a client reads one suite. The development agent would revisit it if a client needed
two suites to agree.

**Applied in the spec:** R-DEB-010. Code: `buildSuite` in `internal/formatdata/deb/build.go`.

### Q-DEB-54 · A new suite starts at change_seq 1 and its upload enqueues the build; the periodic task is the safety net

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `deb_suites.change_seq` defaults to 1 and `built_seq` to 0, so a created suite is already due and the upload that creates it
does not raise it. The enqueue of the build must not hang on the raise.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every writer of a change enqueues the build when it changed anything, raised or not; the task `deb-index-refresh` enqueues a build for a local repository with a suite that has changes no build published (chosen) | A lost job (a restore, a queue purge) is repaired within a minute | Both cases are covered | A second path to the same job; the coalescing key makes it harmless |
| (b) Raise a created suite as well | One rule | change_seq 2 for a new suite | The default of the schema would be pointless |

**Choice:** (a).

**Rationale:** the first upload of a suite is the case that must not be missed; the integration tests found it. The development agent
would revisit it if the task's cost showed.

**Applied in the spec:** R-DEB-010. Code: `group.raise` in `internal/formatdata/deb/changes.go`, `EnqueueDue` in `refresh.go`.

### Q-DEB-55 · The index state is pending whenever changes are unpublished, and a wait for an unknown suite does not wait

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-014 lists the states `pending`, `current` and `failed` and a `waitFor` that waits "until every named suite's
`publishedSeq` reaches its value".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `pending` is computed: `changeSeq` above `publishedSeq` unless the last build failed; a suite the repository does not have is absent and its wait ends at once (chosen) | The state is never stale | A client that polls sees the truth between a change and the build's start | A wait for a suite that an upload is about to create does not wait; the upload's answer names its suite |
| (b) Wait for a suite that does not exist yet until the maximum | Tolerant | A client may start waiting before the upload | A typo costs `deb.index_wait_max` |

**Choice:** (a). The waits of one request share one budget of `deb.index_wait_max`.

**Rationale:** the upload answer carries the suite and its number, so a client never waits for a suite it did not just write. The
development agent would revisit it if a client had to wait before it wrote.

**Applied in the spec:** R-DEB-014. Code: `buildStore.Suites`, `WaitPublished`, `GetDebIndexState`.

### Q-DEB-56 · The Release lists the checksums of each Packages before its compression

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-011 lists the index files a suite serves, `Packages.gz` and optionally `Packages.xz`. A first `apt-get update` against a repository
built that way (Debian 13 with `sqv`, Ubuntu 22.04 with `gpgv`, 2026-11-09) verified `InRelease` and then fetched no index and installed nothing:
`apt-get indextargets` listed none and `apt-get install` answered "Unable to locate package". The same repository with the checksums of the
uncompressed `Packages` added to the `Release` worked on both.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) List `Packages` (uncompressed) in the hash lists with its size and digests, and serve no such file (chosen) | The `Release` names a path that answers 404 | What APT needs: it fetches the compressed file and checks the decompressed content against this entry | A `Release` entry without a file; a tool that fetches every listed path sees a 404 |
| (b) Also serve the uncompressed file as a generated item | Every listed path exists | Consistent | A third copy of every index, and R-DEB-011's "no uncompressed file" changes |

**Choice:** (a).

**Rationale:** it is the smallest thing the verified client needs, and APT never requests the uncompressed path when a compressed one is listed
(by hash or not). The development agent would revisit it if a client was found that requests it.

**Applied in the spec:** R-DEB-011. Code: `Suite.Listed` in `internal/formats/deb/index/release.go`. Test: `TestR_DEB_011_BuildLayoutAndRelease`, the integration test
`TestR_DEB_016_AptGetUpdateAndInstallAgainstALocalRepository`.

### Q-DEB-57 · The snapshot of an index build is a REPEATABLE READ transaction, an exception to R-ARCH-019

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-010 reads the suites, their files and their published generations "in one `REPEATABLE READ` snapshot", and R-ARCH-019 allows that isolation
level only for the backup and the grant-index build.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The snapshot is a transaction of the request pool that sets `REPEATABLE READ` before its first statement; R-ARCH-019 and its archtest rule name it as a third exception (chosen) | One consistent view of the rows a generation lists | The generation never lists a file without its coordinates, or a suite with the numbers of another moment | A third exception to a rule that had two |
| (b) `READ COMMITTED` reads in sequence | No exception | None | A file added between the reads of the coordinates and the binaries could be listed without a row, or a `change_seq` could be read after the files it covers |

**Choice:** (a). The transaction only reads and ends before anything is rendered, compressed, signed or written (R-ARCH-019).

**Rationale:** the numbers a build writes back (`built_seq`) must be those of the rows it listed. The development agent would revisit it if the reads
became one statement.

**Applied in the spec:** R-ARCH-019, R-DEB-010. Code: `snapshot` in `internal/formatdata/deb/build_store.go`, `isolationExempt` in `internal/archtest/sqlrules.go`.

### Q-DEB-58 · The delay of a build is measured from the first unpublished change, kept in the suite row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-033 defines `trove_deb_index_delay_seconds` as the time "from the first change a build covers to its commit". No row held that time.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A column `deb_suites.dirty_since` (migration 00065): set when a suite is created or raised while it is clean, cleared by the commit that publishes it, set to now when a change arrived during the build (chosen) | The time of the first unpublished change, per suite | Exact and read in the build's snapshot; survives a replica change | A column on a table that clients hold revision numbers of |
| (b) The enqueue time of the job | The job's `created_at` | No schema change | A coalesced job keeps the first change's time only until it starts; a change during a build is dated from the next job |
| (c) The upload's own time | Item `created_at` | Nothing new | Misses coordinate changes, deletions and key changes |

**Choice:** (a).

**Rationale:** the bound of R-DEB-010 is about the first change, and only the suite row knows which changes a generation covers. The development agent
would revisit it if the column cost showed on the hot update of `change_seq`.

**Applied in the spec:** R-DEB-010, R-DEB-033. Code: `queries/deb_local.sql` (`DebEnsureSuite`, `DebRaiseSuites`), `deb_build.sql` (`DebApplyGeneration`), `Indexer.Observe`.

### Q-DEB-59 · The delay bound is held by the performance gate, not by the e2e set deb-long

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-010 names the nightly set `deb-long` for `TestR_DEB_010_DelayBound`. The e2e sets run on kind clusters with clients; the bound is the time
between two events inside the database's clock and needs neither a cluster nor a client.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An integration test with three in-process replicas, run per change at a small load (every upload published, the p50) and by `make test-performance` with `TROVE_PERF_GATE=1` at the reference load (10,000 files in three suites, ten uploads per second for 90 s), with and without Valkey (chosen) | The same tier as P-13 and P-14 (Q-ARCH-220) | One clock; the nightly job `latency` already runs it on the large runner after the kind legs; no cluster | Not the chart's three pods: a replica here is a process of the test |
| (b) The e2e set `deb-long` on the kind cluster | What the spec names | The real chart | Timing on a shared cluster says nothing about the bound; the set is WP10's and does not exist yet |

**Choice:** (a). The set `deb-long` keeps what needs a cluster: the drain during a build (R-DEB-031).

**Rationale:** Q-ARCH-293 puts a wall-clock target where its cause can be isolated. The development agent would revisit it if the nightly numbers differed
from a cluster's.

**Applied in the spec:** R-DEB-010. Code: `test/integration/deblocal/delay_test.go`, `Makefile` (`test-performance`).

### Q-DEB-60 · The tests of the local class stay in test/integration/deblocal; the other classes get areas of their own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the plan names `test/integration/deb/` for WP9. `deblocal` exists since the local class (WP3) and holds its upload, coordinate, replay, index and apt tests.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The local class's rows extend `deblocal`; remote and virtual keep an area each (`debremote`, `debvirtual`) (chosen) | One area per class | No move of tests; no change of the Makefile or the CI legs, because `./test/integration/...` already runs the area; an area per class compiles and fails on its own | The plan's name `deb` is not used |
| (b) Move everything to `test/integration/deb/` | The plan's name | One place | A move that touches every test of three builders in flight |

**Choice:** (a).

**Rationale:** the area list of the CI leg and the shards is generated from the directory, so a name costs nothing and a move costs history. The development agent would
revisit it when the second class lands and the shared helpers want a package.

**Applied in the spec:** the test plan table. Code: `test/integration/deblocal`.

### Q-DEB-61 · A full build publishes again when the suite is built up to the number it read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-DEB-010 lets the build's commit write only while the suite's `built_seq` is below the `change_seq` the build read. A reindex of rows that did not change reads
`change_seq` equal to `built_seq` and so never published: `POST …/reindex` answered 202 and nothing happened. The integration test of the two full builds found it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit also writes while the suite is still at the generation the build started from; a full build writes when `built_seq` equals the number it read (chosen) | Linear generations by their number | A reindex and a restore's full build publish; two builds from one snapshot cannot both write | A build that reads a stale generation number is superseded and the suite is built by the next job |
| (b) A reindex raises `change_seq` first | The old condition holds | No change of the commit | A write in the reindex request and a revision number that moves for nothing |

**Choice:** (a).

**Rationale:** jobs of one repository run one at a time (the key `index:<repository id>`), so the generation check only guards a build whose lease was lost. The
development agent would revisit it if a reindex had to leave the revision numbers alone for another reason.

**Applied in the spec:** R-DEB-010, R-DEB-014. Code: `DebApplyGeneration` in `queries/deb_build.sql`, `GenerationRecord.PrevGeneration` and `Full`.

### Q-DEB-62 · A local member contributes the index file of its published generation, not its rows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The index file of the member's published generation, read from its generated items (chosen) | The member's `Packages.gz`, as the member serves it | The virtual repository never lists a file its member does not list yet; the member's own filters are already applied; one gzip stream per index, no row read | The virtual index follows the member by one build |
| (b) The member's rows at the time of the build | Read `deb_index_entries` of the member | No wait for the member's build | Lists files the member's own index does not list yet; a second renderer of the member's paragraphs |
| (c) Render the member's index again per build | The index builder on the member's snapshot | Independent of the member's publication | Two builds of one thing; the digests differ from the member's |

**Choice:** (a).

**Rationale:** a client that reads the virtual index and the member's index must see one description of a file, and the member path serves the member's file. The development agent would revisit it if the lag of one build showed.

**Applied in the spec:** R-DEB-025. Code: `openMemberIndex` in [virtual_build.go](../../../internal/formatdata/deb/virtual_build.go), `VirtualInputs` in [virtual_store.go](../../../internal/formatdata/deb/virtual_store.go).

### Q-DEB-63 · The order of the members and the names each serves are decided on stored configuration, one name at a time with a cache per build

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The member order from the routing with no name; per paragraph name, the members the patterns, pin rules and claims allow, remembered for the build (chosen) | `Routing.Route(virtual, {})` gives the order (priority first, disabled members dropped); `debVirtualRepos.Allowed` answers a name | The rules of R-REPO-019 are the routing's, not copied; caller independent; one lookup per distinct name | One routing call per distinct name of the archive (tens of thousands for a large one) |
| (b) A copy of the rules in the format | Patterns, pins and claims read from the document | No call into the routing | Two implementations of R-REPO-019 that drift |
| (c) Decide by the caller who triggered the build | The routing of the caller | Nothing to build | A signed snapshot that varies by caller (Q-DEB-19) |

**Choice:** (a).

**Rationale:** the claim that protects an internal name must be the claim of the routing. The development agent would revisit it if the routing call per name showed in the build time of a 70,000-paragraph index, which the bound test ran without it.

**Applied in the spec:** R-DEB-025. Code: `Allowed` in [wire_deb_virtual.go](../../../internal/app/wire_deb_virtual.go), `virtualSuite`.

### Q-DEB-64 · A remote member's release is asked of the remote engine at every build, and its stored verified copy is the member's release

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Through the engine as a background request, which serves its stored copy while fresh and revalidates it otherwise; the verified stored copy is merged (chosen) | `fetchRelease` and the `Document` of the background upstream; the rules of R-DEB-025 on what is stored | The engine's freshness, throttling and anti-rollback apply; a newer document no key verifies is never stored; a down upstream costs nothing | A newer unverifiable release does not fail the build: the last verified copy keeps being merged ([H-727](../../security/deb-virtual.md#h-727--a-member-whose-release-has-no-valid-until-can-be-held-at-its-last-verified-copy-for-as-long-as-the-upstream-is-unreachable-or-unverifiable)) |
| (b) Only the stored copy, never a request | Read the rows | No request from a build | A member is as old as the last client request or refresh job |
| (c) The build verifies the upstream's document itself | A second verification | A failed verification fails the build | Two verifiers; the engine would still store or refuse its own |

**Choice:** (a). `member_unverified` is the answer for a stored copy no current key verifies and for a member without keys; `member_expired` and `member_rollback` are checked on the stored copy against the `Date` the last build recorded for the same upstream address.

**Rationale:** the virtual repository trusts what the remote already decided to store. The development agent would revisit it if an operator needed the build to fail on an upstream that signs with a key the member does not hold, which H-626 already names for the remote.

**Applied in the spec:** R-DEB-025, R-DEB-026. Code: `remoteMember`, `admitRelease`, `releaseText` in [virtual_build.go](../../../internal/formatdata/deb/virtual_build.go).

### Q-DEB-65 · A build whose inputs did not change commits only its check time, and a change of claims is seen at the next check

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A fingerprint of the settings, the claim revisions of the Debian repositories, the keys and each member's generation or release digest; the periodic check builds when the last check is older than `virtual.mergedMetadataTtlSeconds` (chosen) | `deb_suites.inputs`, `DebClaimsFingerprint`, `DebVirtualsDue` | No new seam in the repository service; a rebuild of unchanged inputs costs a check | A change of claims waits for the next check ([H-725](../../security/deb-virtual.md#h-725--a-change-of-claims-reaches-the-merged-index-at-the-next-check-not-at-once)) |
| (b) A hook of the claims service that enqueues the builds | `repository.Hooks` gets `OnClaimsChanged` | A claim reaches the index at once | A change of the repository package for one format; the claims of an automatic claim fall inside a publication that builds anyway |
| (c) Build every time the task runs | No fingerprint | Simple | Signs a new generation every period; by-hash files pile up |

**Choice:** (a).

**Rationale:** the signed generation changes only when what it vouches for changes. The development agent would revisit (b) when a second format needs to hear of claims.

**Applied in the spec:** R-DEB-026. Code: `fingerprintOf`, `virtualSuite`, `RecordCheck`, `ClaimsFingerprint`.

### Q-DEB-66 · The index state of a virtual repository is readable and does not wait

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The state of its suites; `waitFor` is refused with 400 (chosen) | The same read as for a local repository | An operator sees `failed` and its reason (`member_unverified: debian`); one endpoint | A caller cannot wait for a virtual build by change number |
| (b) 400 `unsupported_class` as before | The route stays local | Nothing to change | The reason of a failed virtual build would be visible in the job list only |
| (c) `waitFor` waits for the next generation | A wait on the generation number | A script can wait | A second meaning of `changeSeq`; a virtual repository has no change number |

**Choice:** (a).

**Rationale:** a build that fails for a member is the case an operator has to see. The development agent would revisit (c) if the e2e sets needed to wait for a virtual build.

**Applied in the spec:** R-DEB-014. Code: `GetDebIndexState` in [deb_index.go](../../../internal/api/v1/deb_index.go).

### Q-DEB-67 · An upload through a virtual repository needs no code of the format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The dispatcher sends it to the deploy target and the format's local handler runs there (chosen) | `redirectClass` of the dispatcher (R-REPO-021), as for npm and NuGet | The target's permissions, ownership, shadowing check and rules apply by construction; the answer's `index` entries are the target's | A virtual repository without a target is refused at the decision point with 403, not by the handler with 405 |
| (b) A virtual handler for the upload route | The format resolves the target | Could answer 405 `no_deploy_target` | A second copy of the upload path (the OCI format needs one for its upload sessions) |

**Choice:** (a).

**Rationale:** the upload route of the local class is the only writer of rows. The development agent would revisit it if a Debian write needed the virtual repository itself.

**Applied in the spec:** R-DEB-028. Tests: `TestR_DEB_028_UploadsGoToTheDeploymentTarget`, `TestR_AUTHZ_007_DebActionMapOfTheVirtualClass`.

### Q-DEB-68 · A virtual build is enqueued by a publication of a member, by a change of the repository or a member, at creation, and by the period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the virtual class (T-17 WP8).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit of a local member's build (after it, with the delay that keeps `deb.virtual_index_interval` since the last build); an update of the virtual repository or of one of its members and the creation of a virtual repository (in their transactions, without delay); the task `deb-index-refresh` for a check older than the period (chosen) | `triggerVirtuals`, `debRepositoryHook`, the creation hook, `EnqueueVirtualsDue` | Each change reaches the index in the time R-DEB-026 names; a lost trigger is found by the period | The trigger of a publication is not in the publication's transaction ([H-726](../../security/deb-virtual.md#h-726--the-build-after-a-local-publication-is-enqueued-after-the-commit-that-published-not-in-it)) |
| (b) Only the period | The task alone | One path | An upload is listed after up to the period |
| (c) The publication of the member enqueues in its transaction | A call into the virtual store from the commit of the member | No lost trigger | The member's build knows virtual repositories; a failure of the virtual enqueue fails the member's publication |

**Choice:** (a).

**Rationale:** the period is the safety net and the events the speed. The development agent would revisit (c) if H-726 showed in practice.

**Applied in the spec:** R-DEB-026. Code: [virtual_build.go](../../../internal/formatdata/deb/virtual_build.go), [wire_deb_virtual.go](../../../internal/app/wire_deb_virtual.go), [refresh_virtual.go](../../../internal/formatdata/deb/refresh_virtual.go).

### Q-DEB-70 · The stub's test keys have a fixed creation date, not "an hour back"

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with
the counting stub archive (T-17 WP5).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The keys a, b and c are created on 2026-01-01 00:00:00 UTC, the key d on 2024-01-01 with a one-year lifetime and signs at 2024-06-01 (chosen) | A fixed date in the past, every self-signature at the creation time | The fingerprints are the same in every process, so a test pins them in `deb.upstreamKeys` and the whole certificate is reproducible; the creation time is earlier than every signature the stub makes by more than an hour as long as the clock is past it | Not relative to now: a test host whose clock is before 2026-01-01 would see keys from the future |
| (b) Created an hour before the stub starts | The wording of the plan | Relative to now | A new fingerprint at every start, which no test can pin; a stub restarted in the middle of a test changes its key |
| (c) Created at start, signatures one hour after | Nothing is in the past | Relative | Signatures from the future, which is the fault the keys are meant to avoid |

**Choice:** (a).

**Rationale:** the purpose of "an hour back" (R-DEB-015, Q-DEB-30) is that no signature precedes its key; a fixed past date keeps
that and adds reproducibility. The development agent would revisit (b) if a test needs the key to age relative to the clock.

**Applied in the spec:** R-DEB-035 (the stub's keys), [Client and library behaviour](#client-and-library-behaviour-this-page-relies-on).

### Q-DEB-71 · The OpenPGP library: error classes, bounds that default, and what the key carries

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with
`internal/openpgp` (T-17 WP2, library stage).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Four error classes (`ErrInvalid`, `ErrUnsupportedKey`, `ErrTooEarly`, `ErrVerify`) with the reason in the text; a `VerifyError` that names a `Reason` for the metric label; a zero `Limits` field means the bound of R-DEB-034; `Key` carries `Fingerprint`, `Algorithm`, `Bits`, `Created`, `BoundSince`, prints as its fingerprint and offers `Certificate`, `SecretArmored` and `EarliestSignature`; an imported key keeps its primary key alone (an encryption subkey is accepted and dropped) (chosen) | The API of plan section 2.2 plus what the callers need | The key API maps `ErrInvalid` and `ErrUnsupportedKey` to 422 `deb_key_unsupported` or `invalid_value` without parsing text; the build asks `EarliestSignature` for its wait (R-DEB-016); a log line or `%v` of a `Key` cannot leak the private part; the stored certificate is the same for every client | A caller that wants other bounds passes them |
| (b) Only the plan's functions, errors as plain text | Exactly 2.2 | Smallest | The callers parse messages; no wait computation without reading two fields |
| (c) Keep the subkeys of an imported key | Faithful | The certificate is what the operator imported | The sealed secret carries subkeys that never sign; a signing subkey is refused anyway |

**Choice:** (a). Import refusals are the rules of R-DEB-015 in this order: version, algorithm and size, a key whose secret part is
held elsewhere, revoked, cannot sign, an expiry (the current self-signature of any identity, or the newest direct-key signature),
created or bound in the future, a signing subkey. A signed test message proves the key before it is accepted, so a corrupt key fails at
the import and not at the first build.

**Rationale:** the callers (WP2's key store and API, WP4's build, WP6's remote) need to branch on the class and to wait on
`EarliestSignature`. The development agent would revisit (c) if clients are found to need an encryption subkey in `signing-key.asc`.

**Applied in the spec:** R-DEB-015, R-DEB-016, R-DEB-034.

### Q-DEB-72 · Verification: a hash header that does not match ignores the signature; signing subkeys count; certificates are read strictly

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with
`internal/openpgp` (T-17 WP2, library stage).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A signature whose hash the `Hash` header (when present) does not name is ignored like a signature by an unknown key; a signature by a signing subkey of a configured certificate counts and reports the certificate's fingerprint; `ParseCertificates` refuses any block it cannot read instead of skipping it, and keeps a certificate listed twice once; the text returned is the dash-unescaped lines joined by newlines as signed (chosen) | One rule: a message verifies when one acceptable signature does | A mixed message (one honest signature and one mislabelled) still verifies by the honest one, as APT does; an upstream that signs with a subkey works; an operator learns of a damaged key at the save | An upstream that mislabels every hash fails with `hash_header` |
| (b) A header that does not match every signature rejects the message | Strict reading of R-DEB-034 | Simplest to state | One mislabelled co-signature blocks a rotation window |
| (c) The module's default reader (skips unreadable certificates, first armor block only) | Less code | | A damaged key is silently dropped and the remote runs on fewer keys than configured |

**Choice:** (a). `Reason(err)` returns the strongest reason found (`bad_signature` over `key_invalid`, `signature_expired`, `future`,
`weak_hash`, `hash_header`, `unknown_key`, `malformed`, `no_signature`; `too_many` for a bound) for the metric label of R-DEB-020.

**Rationale:** R-DEB-034 says a `Hash` header, when present, "matches its signatures"; this reads it as a property of each signature
the verifier relies on. The development agent would revisit (b) if an upstream is found to depend on the header being wrong.

**Applied in the spec:** R-DEB-019, R-DEB-020, R-DEB-034.

### Q-DEB-73 · The stub frames InRelease as gpg does, serves gzip only, and names a flat directory's suite after the directory

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with
the counting stub archive (T-17 WP5).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The stub frames and signs `InRelease` by hand the way `gpg --clearsign` writes it (the signed text lacks the final line ending, the armor header follows the last line), indexes in plain text and gzip, no xz, zstd or bzip2, no `Contents` or translations, and a flat directory's Release names `Suite` and `Codename` after the directory (chosen) | The shape real archives have; one module for the signatures | trove's verifier meets gpg's framing, not its own; no second compression module for a test double; no APT warning from the flat directory (verified) | `Packages.xz` handling is not exercised by the stub (WP8's bound test and the unit tests do) |
| (b) The module's own `clearsign` encoder | Fewer lines | | The same code as trove's signer, so framing mistakes would cancel out |
| (c) Add xz output to the stub | Complete | Exercises xz | A second owner of the xz module while WP1 adds it |

**Choice:** (a). The stub signs with the module directly (`openpgp.Entity`, `packet.Signature`) and never imports `internal/openpgp`
outside its tests, which verify the stub's output with trove's verifier.

**Rationale:** the point of the stub is an upstream that is not trove. The development agent would revisit (c) when a remote test
needs `Packages.xz` from an upstream.

**Applied in the spec:** R-DEB-035, [Client and library behaviour](#client-and-library-behaviour-this-page-relies-on).

### Q-DEB-74 · A repository without a primary key promotes its next key, and the event then names no old fingerprint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the
signing keys (T-17 WP2).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A promotion needs a next key and nothing else; with no primary it retires nobody, and `deb.signing_key.promoted` (journaled) carries `newFingerprint` alone (chosen) | The way out of "no primary" without the old key | After the replay of a restore withheld the only key, or an administrator confirmed its retirement, a new generated or imported key can become primary; the replay of the entry makes that key primary again, which is what happened | The event's schema makes `oldFingerprint` optional |
| (b) Refuse a promotion without a primary | The strict reading | One state fewer | A repository whose key is lost can only get a primary by reinstating a withheld key; once that key is retired the repository can never sign again |
| (c) Make the new key primary at creation when there is no primary | No promotion | One step fewer | A next key is the only state that a client has time to learn (R-DEB-017); a primary at once skips the overlap |

**Choice:** (a).

**Rationale:** the overlap is about clients' keyring files, which a repository without a primary has no one to protect. The development
agent would revisit (c) if operators find the two calls tedious.

**Applied in the spec:** R-DEB-017, R-DEB-033; the registry's schema of `deb.signing_key.promoted`.

### Q-DEB-75 · The changes of one repository's keys run one after the other; the loser of a race answers 409

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the
signing keys (T-17 WP2).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every write operation and every replay step starts by locking the repository row (`FOR NO KEY UPDATE`), then reads the keys; a unique violation that still happens answers 409 `deb_key_state` (chosen) | A mutex per repository in the database | READ COMMITTED gives the statements after the lock the committed state of the winner; two creates give one 201 and the rest 409, two promotions one 200; across replicas | A key change waits for a concurrent update of the same repository's configuration (milliseconds) |
| (b) Rely on the partial unique indexes | Let the second insert fail | No lock | A 23505 is an internal error unless every statement maps it, and the second request cannot tell why |
| (c) SERIALIZABLE transactions | The database detects it | No explicit lock | The runner retries only on serialization failures, and the pool runs READ COMMITTED |

**Choice:** (a).

**Rationale:** integration test `TestR_DEB_015_ConcurrentChangesLeaveOneNextKey` found (b) answering 500 under six concurrent creates. The
development agent would revisit (c) if the transaction runner moves to a stricter isolation level.

**Applied in the spec:** R-DEB-017; [docs/security/deb-signing.md](../../security/deb-signing.md).

### Q-DEB-76 · The first key is made by a hook the composition root binds once; signing-key.asc is decided on the file with no package

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the
signing keys (T-17 WP2).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The hook of Q-REPO-273 is registered once per process and calls the store of the replica composed last; it makes a key for the local and the virtual class and none for the remote class; `signing-key.asc` asks the decision point for `read` of the file `signing-key.asc` with no package, and answers 404 when the repository has no primary or next key (chosen) | One registration, one store | A process that composes several replicas (the integration tests) does not panic on the second registration; a grant limited to packages does not reach the key, as R-DEB-004 says; a pattern on paths decides on the file's own path (conformance check 13) | The hook follows the last store: replicas of one process share one database, so it makes no difference |
| (b) Register per composition | One hook per store | Simple | `RegisterCreateHook` panics on the second registration |
| (c) Serve the key without a decision | The key is public | Cheaper | A repository the caller may not read would be known to exist; the kit's check 13 refuses a served file nobody decided |

**Choice:** (a). The remote class's `signing-key.asc` (the certificates of `deb.upstreamKeys`) stays with the remote class (R-DEB-018).

**Rationale:** the key of a repository is public to its readers, not to everybody. The development agent would revisit (b) if the hook
registry learns to replace a registration.

**Applied in the spec:** R-DEB-015, R-DEB-018; Q-REPO-273.

### Q-DEB-77 · The replay acts on the key in the state the entry assumes and on nothing else

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the
signing keys (T-17 WP2).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `retired` withholds a primary or next key of that repository; `promoted` makes the new key primary and withholds the old one only when the restored repository holds the new key as next and the old one as primary, withholds the old primary when the new key is absent, and finds a primary that is neither the old nor the new key, or a new key that is withheld or retired, "subject unknown"; the replay output notes a repository left without a primary (chosen) | The entry is a claim | A forged or mistaken entry for another repository's key, for a key that is not where the entry says, or for a repository that is no local or virtual Debian repository changes nothing; nothing is erased | An entry that disagrees with the restored state needs an administrator's look |
| (b) Apply every entry to whatever key has the fingerprint | Simple | Fewer cases | An entry could withhold the key of any repository, whatever it names |
| (c) Erase on `retired` | Exact | | Against R-HA-054: a forged entry would destroy a signing identity |

**Choice:** (a).

**Rationale:** the replay runs with the owner credential on entries a holder of the object store could write; each step is a check before a
restriction. The development agent would revisit the "subject unknown" cases if real restores produce them routinely.

**Applied in the spec:** R-DEB-032.

### Q-DEB-78 · A Release and its Release.gpg are verified as a pair when they are served, not when they are stored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PrepareFill` of a `Release` parses it; the pair is verified when a request for either is served (502 `deb_upstream_signature_invalid`) and the `Release` is then recorded as verified; until then it names no file of the directory (chosen) | The fill sees one document; the handler holds both | No second fetch inside a fill; an `InRelease` (every APT since 1.1) needs neither | A pair that does not verify is stored and refused on every request ([H-736](../../security/deb-remote.md#h-736--a-release-and-its-releasegpg-are-stored-before-they-are-verified-as-a-pair)) |
| (b) Fetch `Release.gpg` inside `PrepareFill` | The fill verifies both | Nothing unverified is stored | `PrepareFill` has the body of one document and no upstream client; a second request would bypass the engine's egress, credential and throttling rules |
| (c) Refuse a `Release` without `InRelease` | Only `InRelease` is stored | No pair | Old and third-party archives that publish `Release` and `Release.gpg` only would not work |

**Choice:** (a).

**Rationale:** The engine hands `PrepareFill` the body of the document it fetched and nothing else. The verified `InRelease` is what names files when the upstream has one; a `Release` is used only after the pair verified, by a client's request or by the job.

**Applied in the spec:** R-DEB-019. Code: [handlers_remote.go](../../../internal/formats/deb/handlers_remote.go) `verifyPair`.

### Q-DEB-79 · The files a stored release lists are rows of their own: `deb_remote_files`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `deb_remote_files` (migration 00065): one row per listed file of a directory's current release, kept after the release is replaced with `current = false` and `superseded_at` until `deb.by_hash_keep` has passed (chosen) | A statement answers "what does the release list for this path" | One lookup per request; the history gives the housekeeping its set of names; the purge of a restore deletes it with the other cache rows | About two thousand rows per release of a Debian suite |
| (b) Parse the stored document at every request | No table | Nothing to keep | A fill (`OnFill`) has no host to read the document with; a parse of a 200 KB document on every index request |
| (c) A `jsonb` column of the release row | No new table | One row | The history of replaced releases cannot be queried by hash; every read moves the whole list |

**Choice:** (a).

**Rationale:** The check of R-DEB-021 runs inside the fill transaction and on the hottest metadata route; a keyed lookup is the shape both need.

**Applied in the spec:** R-DEB-021, R-DEB-023. Code: [00064_deb_remote_files.sql](../../../internal/meta/postgres/migrations/00064_deb_remote_files.sql), [remote_store.go](../../../internal/formatdata/deb/remote_store.go).

### Q-DEB-80 · A fetched index is stored only when it is the listed file; a stored document that is not the listed file is replaced by a read outside the cache

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `OnFill` of an index compares the digest of the fetched document with the listed hash and answers 502 `deb_index_mismatch` without storing; a request that finds the stored document is another file reads the listed file with `Exchange`, checks its hash and serves it without storing it (chosen) | No index is stored or served that no stored release names | Both windows of an archive without by-hash (a refreshed release over a fresh index document, a fresh index over an old release) end in the right bytes or a 502 | A request in the window costs one request to the upstream ([H-740](../../security/deb-remote.md#h-740--an-index-in-the-window-between-a-refreshed-release-and-its-stored-document-costs-an-upstream-request-per-client-request)) |
| (b) 502 on every mismatch | Simple | No exchange | Every client fails in the seconds between the refresh of the release and the expiry of the index document |
| (c) Store what was fetched | The cache follows the upstream | No request outside the cache | The stored document then names no release; R-DEB-021 forbids it |

**Choice:** (a).

**Rationale:** APT fetches `InRelease` first and the index after it, so the release is nearly always the newer document; the read outside the cache bridges the rest.

**Applied in the spec:** R-DEB-021. Code: [remote.go](../../../internal/formats/deb/remote.go) `indexFilled`, [handlers_remote.go](../../../internal/formats/deb/handlers_remote.go) `serveIndex`, `exchangeIndex`.

### Q-DEB-81 · A directory whose stored release no key verifies serves nothing, and its release is revalidated without validators

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Index requests of the directory answer 502 `deb_upstream_signature_invalid` while its release is unverified; the adapter's revalidation of such a release is a HEAD whose answer says "moved", so the engine sends the GET with no `If-None-Match` and no `If-Modified-Since` (chosen) | A 304 can never mark a release verified, because none is asked for | No stored release is used on the authority of a key that was removed | A HEAD more than the conditional request; an upstream that refuses HEAD answers an error that delays the GET |
| (b) Keep serving the stored release until it expires | No outage | Available | The removal of a key would not take effect for a period; the spec says it does |
| (c) Delete the stored release | Clean | Nothing unverified is stored | The floor and the rollback protection go with it ([H-630](#h-630--a-restore-without-remote-caches-forgets-the-newest-upstream-release-a-remote-had-seen)) |

**Choice:** (a).

**Rationale:** The engine adds validators itself after the format has built the request; a `Revalidator` is the contract's way for a format to change what an expired document is revalidated with.

**Applied in the spec:** R-DEB-020. Code: [remote.go](../../../internal/formats/deb/remote.go) `RevalidationRequest`, [handlers_remote.go](../../../internal/formats/deb/handlers_remote.go) `requireVerified`.

### Q-DEB-82 · The re-verification reads the stored bytes through the cache view of the background upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The upstream of a background run offers `StoredDocument(path)`, the document as the cache holds it with no request (chosen) | The job asserts it (`debdata.StoredReader`) | R-DEB-020 says "from their stored bytes (no upstream request)"; it is true | One more capability of the host's background upstream |
| (b) `Document` | The contract as it is | No change in the host | It revalidates a stored document once per run, which is a request; and after a change of keys that request is the HEAD of Q-DEB-81 |
| (c) Read the cache blob from the data port | No change in the host | No request | The data port does not know the cache entry or the object store |

**Choice:** (a).

**Rationale:** The cache view of the engine (R-REPO-032) exists for exactly this: serve what is stored.

**Applied in the spec:** R-DEB-020. Code: [mirror_host.go](../../../internal/formathost/mirror_host.go) `stored`, [housekeeping.go](../../../internal/formatdata/deb/housekeeping.go) `reverify`.

### Q-DEB-83 · The `deb-index` job of a remote repository is its re-verification and housekeeping; the handler is chosen by the class of the repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `RegisterJobs` registers one handler for `deb-index` that looks up the class of the repository the job targets: a remote's job is `RemoteJobs.RunIndex`, every other the index build (chosen) | The key `index:<id>` of R-DEB-021 and R-DEB-020 is the one a reindex and a key change use, so the jobs coalesce | One kind; the builder of WP4 changes nothing | A lookup of the class when a job starts |
| (b) A kind of its own, `deb-remote-index` | No dispatch | Separate | The spec names `deb-index` and the key; a reindex of a remote would not find its job |
| (c) The build handler asks the class | No dispatch in `jobs.go` | One place | It edits the file the index builder replaces |

**Choice:** (a).

**Rationale:** The class is a fact of the repository, and the dispatch is eight lines.

**Applied in the spec:** R-DEB-020, R-DEB-021. Code: [jobs.go](../../../internal/formatdata/deb/jobs.go) `classHandler`, [housekeeping.go](../../../internal/formatdata/deb/housekeeping.go).

### Q-DEB-84 · The housekeeping of a remote removes by-hash files and the rows of replaced releases, and nothing else

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The task finds the remotes that have replaced releases past `deb.by_hash_keep` (`DebRemoteHousekeepingDue`) and enqueues their `deb-index` job; the job forgets those rows and removes by-hash items older than the period that no remembered file names (chosen) | Matches what accumulates: a by-hash path is a new item for each generation | The mirror's "deletes nothing" rule is about package files and is not touched | A plain index path that no release lists any more stays until its unused-retention period |
| (b) Also remove plain index documents | Complete | Nothing stays | A plain path is one cache entry that the next fill replaces in place; the engine's unused-retention rule already removes the unused |
| (c) Remove at the time a release is replaced | Immediate | No task | A client that read the old release a moment ago would find its by-hash files gone, which the keep period is for |

**Choice:** (a).

**Rationale:** Package files are never touched.

**Applied in the spec:** R-DEB-021. Code: [housekeeping.go](../../../internal/formatdata/deb/housekeeping.go), [refresh_remote.go](../../../internal/formatdata/deb/refresh_remote.go).

### Q-DEB-85 · A remote repository's cache owns no row of the local class: the commit hook does nothing for a fetched file

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `OnCommit` returns nil for a repository of the remote class (chosen) | The rows of a cache are `deb_remote_entries` | A fetched file is not refused as a raw deploy; the cache needs no version row for its serving | The package screens of a remote have no rows of the engine's own until R-DEB-030 reads the parsed rows |
| (b) Create package and version rows as npm does | Package screens work at once | Uniform with npm | The Debian identity of a file is a path and an architecture; the version rows belong to the screens (R-DEB-030) and are not needed to serve |
| (c) Refuse | As the hook did for a raw deploy | Unchanged | Every fill would answer 409 |

**Choice:** (a).

**Rationale:** The hook ran for fills before this class existed and answered 409 to a raw deploy; the fill is not a deploy.

**Applied in the spec:** R-DEB-001. Code: [hooks.go](../../../internal/formats/deb/hooks.go) `OnCommit`.

### Q-DEB-86 · A failed verification is counted and written as the security event where a client meets it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PrepareFill` tags its error with the label of the failure; the handler counts it and asks the data port for the event, which writes it at most once an hour per repository and directory in this replica (chosen) | `PrepareFill` has no host | The failure is counted where it is seen | A job's failure is logged and not counted; two replicas may each write the event once an hour ([H-738](../../security/deb-remote.md#h-738--a-failed-verification-is-written-once-an-hour-per-replica-and-only-where-a-client-meets-it)) |
| (b) A process-wide counter in the format | Counts everything | Complete | The format keeps no state outside its replica value (R-ARCH-032) |
| (c) Return the failure to the engine for counting | One place | Complete | The engine knows no Debian reasons |

**Choice:** (a).

**Rationale:** The label is the reason of `openpgp.Reason`; it never carries key material.

**Applied in the spec:** R-DEB-020. Code: [remote.go](../../../internal/formats/deb/remote.go) `SignatureReason`, [remote_audit.go](../../../internal/formatdata/deb/remote_audit.go).

### Q-DEB-87 · The fingerprints of `deb.upstreamKeys` are derived when a document is written and are read-only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The service takes the member out before the schema sees it and derives it from the keys it has just checked (chosen) | A client that writes back what it read is accepted | Always the fingerprints of the stored keys | The stored document holds a derived value |
| (b) Compute on every read | Nothing stored | No stale value | Every reader of the document parses the keys; the key list of a remote is a document member (R-DEB-020) |
| (c) Reject a document that carries it | Strict | No hidden rewrite | A client could not write back what it read |

**Choice:** (a).

**Rationale:** The keys are checked on save (R-DEB-020), so the fingerprints cost nothing more at that point.

**Applied in the spec:** R-DEB-020. Code: [validate_deb.go](../../../internal/repository/validate_deb.go).

### Q-DEB-88 · The conformance kit holds a Rewrite that is the identity to the identity

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A fixture that names the reason `Rewrite` is the identity needs no download links and the kit checks that every document comes back unchanged (chosen) | Check 4 stays for the formats that rewrite | A format cannot skip the check unnoticed: the reason is in the fixture | One more fixture field |
| (b) Skip check 4 | No kit change | Nothing to build | The kit refuses a skip for a format with a remote adapter |
| (c) Invent a link in the fixture | No kit change | The check runs | It proves nothing: the document of an archive has no link to the upstream |

**Choice:** (a).

**Rationale:** An APT archive's documents hold no address of the upstream; trove must not change a byte of a signed release.

**Applied in the spec:** R-ARCH-010. Code: [checks.go](../../../internal/formats/formattest/checks.go) `rewriteIdentity`.

### Q-DEB-89 · A key that expired after it was saved stays in the set and is judged when it is used

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Saving checks validity (`ParseCertificates`); verifying reads the set without it and judges each key by the time of the signature and by now (chosen) | An expired key does not break the verification by the others | R-DEB-020: a key left in the set after it expired cannot sign a backdated release | A stored set can hold a certificate that is no longer valid |
| (b) Read the set with `ParseCertificates` every time | One function | Simple | One expired certificate would make every release of the remote fail |
| (c) Drop an expired key from the set | Clean | No dead key | It rewrites an operator's document |

**Choice:** (a).

**Rationale:** The key library already judges validity at signature time and now (Q-DEB-35).

**Applied in the spec:** R-DEB-020. Code: [certs.go](../../../internal/openpgp/certs.go) `ParseStoredCertificates`.

### Q-DEB-90 · A Sources index is parsed when a job asks for it; the fill of a Packages index enqueues its parse

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the remote class (T-17 WP6).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `OnFill` of a plain `Packages` index enqueues its parse; a new release enqueues the parse of every index of the directory that has rows (Sources included); the first parse of a `Sources` index is enqueued by the mirror that asks for sources (chosen) | Packages rows exist after the first `apt-get update` | Sources are parsed only for a mirror with `sources: true` | The mirror work package enqueues the first Sources parse |
| (b) Parse every index a release lists | Complete | Everything is known | Contents and Translation indexes are not parsed (R-DEB-023); a Sources index is rows for a mirror only |
| (c) Parse at the first request for a package | Lazy | Nothing unused | The gate needs the digest at the first request |

**Choice:** (a).

**Rationale:** The parse job reads any `Packages` or `Sources` path (`ParseArgs.Path`), so the mirror adds no code.

**Applied in the spec:** R-DEB-023. Code: [remote_parse.go](../../../internal/formatdata/deb/remote_parse.go).
### Q-DEB-100 · The signing keys and the index state are two lazy tabs of a local or virtual repository's edit form

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the
Debian screens (T-17 WP11).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two tabs, **Signing keys** and **Index**, of the repository's edit form, for the classes local and virtual, created when the tab is opened (chosen) | The panels read their own endpoints; the form's save does not touch them; key changes act at once, each behind its own confirmation where it erases something | The edit form is where an operator looks for a repository's configuration; a read happens only when someone looks; the key operations are not part of the document, so they must not wait for "Save changes" | Two more tabs for one format |
| (b) One "Debian" tab holding settings, keys and index | One place | Fewer tabs | The settings are written by the form's save and the keys by their own calls in the same tab: a reader cannot tell which button belongs to which |
| (c) A screen of its own below the repository | A route per panel | Deep links | A second place to find a repository's configuration; the tabs of the form already carry links |

**Choice:** (a). The key changes are offered to the principals who may edit the repository (`repositories:write-config` or Owner of the
project, the rule of "Save changes"); the server enforces `manage` on every call and answers 403 otherwise, so the UI only decides what to
offer ([H-766](../../security/deb-signing.md#h-766--the-screen-offers-key-changes-to-the-principals-who-may-edit-the-repository-not-to-everyone-who-holds-manage)).
The promotion, the removal of the next key and the confirmation of a retirement ask first and say what they erase (the private key) and
what a client that never replaced its key file then sees; generating a next key and reinstating a withheld key ask nothing, because
neither erases anything. The list is read again after every call, also after a refusal: the server's state is what the page shows. An
imported secret key and its passphrase live in two form fields, are sent once and are cleared from the form after the answer, whatever
it was ([H-765](../../security/deb-signing.md#h-765--an-imported-secret-key-passes-through-the-operators-browser)).

**Rationale:** the screen follows the mechanism: a document setting is saved, a key operation is an act with consequences for the
clients. The development agent revisits this when an operator needs the panels without the permission to edit the document, or when a
deep link to the key list is asked for.

**Applied in the spec:** R-DEB-030 (the repository screen bullet), R-UI-021 in [api-and-ui.md](../api-and-ui.md).

### Q-DEB-101 · A remote repository shows the fingerprints of its upstream keys read-only, and has no key or index tab

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The format tab shows `upstreamKeyFingerprints` as a read-only list with copy buttons under the editable `upstreamKeys`; no Signing keys or Index tab for a remote (chosen) | The server-computed fingerprints are the only copy of what the pasted block holds | An operator compares the fingerprints with the archive's published ones before trusting the block (R-DEB-020); a remote has no keys of its own (`GET …/deb/signing-keys` answers 400 `not_applicable`) and builds no index | A remote's cached releases are not visible as suites on this screen |
| (b) Show the fingerprints inside the key panel for every class | One panel | Fewer components | The panel would answer 400 for a remote |
| (c) Leave the fingerprints to the API | No UI | Less code | The one fact an operator needs to verify the paste is not on the screen |

**Choice:** (a). The list follows the saved document: it is not recomputed from the text being typed, and a save never writes it back (the
member is read-only; the form's patch carries only what changed).

**Rationale:** R-DEB-020 gives `upstreamKeyFingerprints` for exactly this comparison. The development agent revisits this when the
remote's index state (the releases it holds and their verification, R-DEB-023) is exposed by an API.

**Applied in the spec:** R-DEB-030.

### Q-DEB-102 · The index panel reads again while a suite waits for its build, and says why a build failed in words

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A plain read, repeated every 3 s while any suite is `pending`, ended when none is or the tab is left; the reason of a `failed` suite in a sentence per code (chosen) | `GET …/deb/index` without `waitFor`; `no_signing_key`, `member_unverified`, `member_expired`, `member_rollback`, `deb_members_unreadable` have sentences, another code is shown as it is | A person watching an upload's build sees it finish; no request is held open; the member codes of a virtual repository (R-DEB-029) are readable | A pending suite costs one read every 3 s while the tab is open |
| (b) The long wait (`waitFor`) | One held request per suite | Fewer requests | The wait is for a client that knows a `changeSeq`; the page does not (it would invent the numbers it waits for) |
| (c) A single read when the tab opens | No polling | Simplest | A suite that is `pending` stays so on the screen until the operator reloads |

**Choice:** (a). The panel also offers **Rebuild all indexes** (R-DEB-014, `POST …/reindex`): it asks first (what is rebuilt, and that
clients keep reading the published indexes meanwhile), shows the queued job with a link to the jobs list, and shows a `409 reindex_running`
as the sentence "already queued or running", not as an error.

**Rationale:** the endpoint exists to let a caller wait; a screen that has no `changeSeq` to wait for reads. The development agent
revisits this when the screen is reached from an upload (then it knows the `changeSeq`).

**Applied in the spec:** R-DEB-014 is unchanged; the panel is described under R-DEB-030.

### Q-DEB-103 · The version's architectures, coordinates and control fields are one block of the Metadata tab

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A "Package details" block of the Metadata tab: architectures, suites and components (`stable/main`), maintainer, section, priority, installed size (KiB), depends, source package (chosen) | Read from `metadata` as R-DEB-030 lists it; a field the version lacks is not shown; `homepage` joins the Links block of the tab | The place where the other formats show their facts (Q-UI-177); the same tab ids | Dependencies are one line of text, as the control field writes them, not a list of links |
| (b) A tab of its own for coordinates | A tab per concept | Visible | A tab that exists for one format |

**Choice:** (a), with the values shown as the server sends them (the page does not parse `Depends`).

**Rationale:** the control field is the package's own statement; a parsed list of links would claim a resolution trove does not do for
this format. The development agent revisits this when the search of control fields is built (Deferred: PKG-DEB-010).

**Applied in the spec:** R-DEB-030.

### Q-DEB-104 · The setup dialog renders the default suite; a suite field is not built

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The dialog offers the client `apt`, renders the snippets the API sends and explains them; the suite is the template's default `stable`, the operator edits the `Suites:` line (chosen) | The UI sends no `scope`; the `-scoped` ids the API can send for a suite are explained too | Same as every format: the dialog has no variant input for any client (Q-API-235) | A repository whose suites are not `stable` shows a snippet the operator must correct |
| (b) A suite field in the dialog that sends `scope` | The snippets name the chosen suite | Correct for the reader's suite | A new control for one format; the dialog's scope handling does not exist for npm either; the suites come from the index state, which a reader of the setup may not read |

**Choice:** (a). The ids of the `apt` client are `apt.configure.shell`, `apt.configure.list`, `apt.install` and `apt.publish.curl`, and
the `-scoped` form of the two configure ids; each has an explanation, and `snippet-ids.fixture.ts` lists them (the test that renders
every format with a scope now includes Debian).

**Rationale:** R-UI-016 asks for the clients of a format; the suite variant is the API's, and a picker is a feature of the dialog, not of
the format. The development agent revisits this when the dialog offers a variant for any client.

**Applied in the spec:** R-DEB-029 is unchanged; `internal/api/v1/setup_snippet_ids_test.go` lists `deb` among the formats of the fixture.

### Q-DEB-91 · The index step of a pass runs in its first page, through the data port, and the names and plans read the rows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the Debian mirror (T-17 WP7).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The first page of a pass calls `MirrorStore.MirrorSync`, which fetches the release of each suite through the pass's upstream, keeps the index files of the scope and parses the Packages and Sources indexes into the rows of R-DEB-023 in the pass; every page then reads the names, and every plan the files, from those rows (chosen) | The pass sees its own rows at once; no wait for the queue; the fill hooks and the gate are the pull-through's | One code path for the parse (`RemoteJobs.parseIndex`); a pass resumed on another worker finds the rows | A pass holds the parse of the indexes of its suites before its first name ([H-746](../../security/deb-mirror.md#h-746--the-index-step-parses-every-index-of-the-scope-before-the-first-name-of-a-pass)) |
| (b) Enqueue the parse jobs and wait for them | The job does it | Existing job | The pass has to poll rows another process writes, and a job that fails stalls the pass without an error of its own |
| (c) Parse in the lister from memory | No rows | No tables | The plan of a name runs up to the pass's window at once and could not share the parse; the gate needs the rows for the digests anyway |

**Choice:** (a).

**Rationale:** The gate that verifies every file of a pass (R-DEB-022) reads the same rows, so they have to exist before the first plan.

**Applied in the spec:** Mirror discovery. Code: [mirror_sync.go](../../../internal/formatdata/deb/mirror_sync.go), [mirror.go](../../../internal/formats/deb/mirror.go).

### Q-DEB-92 · A release with a SHA512 section has its index files kept by that hash as well

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the Debian mirror (T-17 WP7).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `release.Release.SHA512` reads the SHA512 section; the pass keeps each listed variant of an index at `by-hash/SHA256/…` and, when listed, `by-hash/SHA512/…` (chosen) | APT takes the strongest hash a release lists and asks for exactly that path | An `apt-get update` with the upstream gone works for APT 2.4 and 3.0 | Two copies of one file in the store (the blob is shared by digest, only the item differs) |
| (b) SHA256 only | Simpler | What R-DEB-021 names | Found by hand: APT asked for the SHA512 path of the stub, got 503 offline and fell back to plain paths that were not stored |
| (c) Store the plain path as well | The client's fallback works | One more way | A plain path is a metadata entry the pass would have to keep in step with the release |

**Choice:** (a).

**Rationale:** The remote's `by-hash` route already serves SHA512 as content with the digest of its path.

**Applied in the spec:** Mirror discovery, R-DEB-021. Code: [release.go](../../../internal/formats/deb/release/release.go), [mirror_sync.go](../../../internal/formatdata/deb/mirror_sync.go) `fetchVariant`.

### Q-DEB-93 · A pass lists the names of the suites whose stored release a key verifies; expiry is judged when the pass begins

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the Debian mirror (T-17 WP7).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The index step judges each release (signature, suite, `Valid-Until`) and notes `release_unverified` or `release_expired`; the names and the plans read only suites whose stored release has `verified_by` (chosen) | A suite whose keys were removed stops being listed at once | No upstream read per plan | A release that expires in the middle of a pass is still planned from its rows ([H-744](../../security/deb-mirror.md#h-744--a-release-that-expires-during-a-pass-is-still-planned)) |
| (b) Judge at every call | Exact | No window | The plan of a name runs thousands of times per pass; each would read and verify every release |
| (c) Keep the judgement in the cursor | No window | Stateless | The cursor is 4 KiB and the engine stores it; a plan has none |

**Choice:** (a).

**Rationale:** The plan of a name has the upstream and a name, nothing of the page.

**Applied in the spec:** R-DEB-024, Q-DEB-32. Code: [mirror_store.go](../../../internal/formatdata/deb/mirror_store.go) `scopeKeys`.

### Q-DEB-94 · The notes of the index step travel in the note of the first page

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the Debian mirror (T-17 WP7).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `suite_unavailable: <suite>`, `release_unverified: <suite>`, `release_expired: <suite>` and `index_unlisted: <path>` are joined in `Page.Note` (chosen) | The only channel the contract has for words | Shown with the status when the discovery is truncated | The engine shows a note only with `Truncated` ([H-745](../../security/deb-mirror.md#h-745--the-notes-of-a-pass-are-shown-only-when-its-discovery-is-truncated)) |
| (b) An error | Seen | Loud | A suite the upstream does not have is no failure of the pass (R-DEB-024) |
| (c) A table of notes | Complete | Queryable | A new table for a word per suite |

**Choice:** (a).

**Rationale:** The spec asks for the notes (Mirror discovery); the contract carries them in the page.

**Applied in the spec:** Mirror discovery. Code: [mirror.go](../../../internal/formats/deb/mirror.go).

### Q-DEB-95 · A mirror asks for a suite's indexes in the variants the release lists, not the ones the format prefers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Added with the Debian mirror (T-17 WP7).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each listed variant of a Packages, Sources and Translation file of the scope is fetched (by hash or at its plain path); a variant the upstream answers 404 for is skipped; the parse reads the preferred one (chosen) | A client takes any variant its APT prefers (xz, gz, plain), offline | Matches R-DEB-024 | Two to six downloads per index instead of one |
| (b) Only the preferred variant | One download | Cheap | A client whose APT prefers another compression fails offline |
| (c) The compressions APT 3.0 prefers | Fewer downloads | Cheap | APT 2.4 and tools with another order would fail; the preference is the client's |

**Choice:** (a).

**Rationale:** Index files are small next to the package files a mirror keeps.

**Applied in the spec:** Mirror discovery. Code: [mirror_sync.go](../../../internal/formatdata/deb/mirror_sync.go) `mirrorIndexes`.

### Q-DEB-105 · The client images add curl from their own archive; apt and the verifiers are held at the pin

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The pinned image plus `curl` installed from the image's own archive without an exact version, after `apt`, `gpgv` and `sqv` are put on hold (chosen) | `apt-mark hold` first, then `apt-get install curl`; a dependency of curl that would upgrade a held package fails the build | The clients under test stay what the digest says; curl, which only uploads and fetches a key, needs no pin of its own; no pin that a superseded archive version turns into a red build | The curl version and its libraries are those of the archive on the day the image is built |
| (b) Exact apt versions of curl for each of the four images in `tools/versions.env` | Four more pins, as the npm clients image pins curl and jq | Reproducible down to curl | Four pins that a superseded package breaks without any change of the clients; curl is not under test |
| (c) The test uploads and fetches the key itself, the pod has no curl | The publish snippet and the configure snippet are not run in the pod | No second tool in the image | The snippets are the user's path (Q-DEB-27); R-DEB-006 and R-DEB-035 name `curl` as the uploader |
| (d) A static curl binary copied from another pinned image | One more image pin, no apt install | Image untouched but for one file | A third-party static build to trust and keep pinned for a helper |

**Choice:** (a). `test/e2e/aptclients/Dockerfile` holds the packages before it installs curl.

**Rationale:** The rows prove what APT and its verifier do; curl is the transport of two snippet lines. The hold turns the one way curl could change the client into a failed image build. The development agent revisits this when a tested curl behaviour appears in a row.

**Applied in the spec:** R-DEB-035 names the images as the clients; the test plan page ([developer/e2e.md](../../developer/e2e.md#the-apt-clients)) says what the images add.

### Q-DEB-106 · The APT client pods run as root in a namespace of Pod Security level baseline

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A namespace `trove-e2e-apt` of the level `baseline`, pods as root without a privileged container (chosen) | `apt-get install` runs as root; the level refuses privileged containers, host paths and extra capabilities | The least level that lets APT install packages; the OCI clients' privileged namespace is not widened | A third namespace beside `trove` and `trove-e2e-clients` |
| (b) The namespace `trove-e2e-clients` (level `privileged`) of the OCI clients | One namespace for all client pods | Nothing new to create | The OCI clients' teardown deletes the namespace and the APT pods with it; the Debian clients would run at a level they do not need |
| (c) The namespace `trove` (level `restricted`) | No new namespace | none | `restricted` refuses root, which `apt-get install` needs |

**Choice:** (a).

**Rationale:** APT drops to the user `_apt` for downloads and writes below `/usr` and `/var` as root: root without any other privilege is the whole need. The development agent revisits this when the Debian clients need a capability the level refuses.

**Applied in the spec:** none; [deploy/dev/apt-clients.yaml](../../../deploy/dev/apt-clients.yaml) carries the level.

### Q-DEB-107 · The rotation table runs one client per cell, in turn

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One client pod per image; for each cell the test replaces the keyring file and clears the lists, then runs `apt-get update --error-on=any` (chosen) | The cells of the table run in turn on the same client | Three pods per image would be nine more pods for what a file replacement shows; the lists are the only state APT keeps between runs, and clearing them gives every cell a client that has fetched nothing | The three clients of R-DEB-017's acceptance criterion are one client three times |
| (b) Three client containers per image, as the criterion says | Each holds one keyring from the start | Three clients that exist at the same time | Nine pods; a source that two clients read from the same URI is the same file for APT, and the three would share their `/etc/apt` only if they shared a pod |

**Choice:** (a). The "old key only" client is the first cell, "both keys" the second, "new key only" the third, at each of the three points of the table (before the next key, while both sign, after the promotion).

**Rationale:** The table describes what a client with a given keyring file sees; the client holds nothing else. The development agent revisits this when a row needs two clients to fetch at the same moment.

**Applied in the spec:** R-DEB-017's e2e criterion names the cells per client; the line "two client containers per image … and a third" now says one client whose keyring file and lists are replaced per cell (Q-DEB-107).

### Q-DEB-108 · The pins of the four images are in tools/versions.env

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `E2E_APTCLIENTS_DEBIAN13_IMAGE`, `…_DEBIAN12_IMAGE`, `…_UBUNTU2404_IMAGE`, `…_UBUNTU2204_IMAGE` in `tools/versions.env`, each the digest of the multi-architecture index of its tag; Renovate moves the digest only (chosen) | The place every other image pin has; `make versions-check` compares the four tags | One place, read by the Makefile, the shell scripts and the tests; the arm64 laptop and the amd64 runner run one release | none |
| (b) Pins in the Makefile, as R-DEB-035 first said | Variables of the Makefile | The text of the criterion | The Makefile reads `tools/versions.env` for every other tool (one version of every tool) |

**Choice:** (a). The tag never moves (the clients are four releases): a rule of `renovate.json` disables major, minor and patch updates of the Debian and Ubuntu pins, and `check-pins.mjs` fails a tag that left its release.

**Rationale:** One pin in one file. The development agent revisits this if a fifth release becomes a client.

**Applied in the spec:** R-DEB-035 says `tools/versions.env`.

### Q-DEB-109 · The mirror and virtual rows were written before their classes and switched on when the classes landed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `TestDebMirror` and `TestDebVirtual` written against this page, skipped with the reason while `debMirrorBuilt` and `debVirtualBuilt` were false, switched on by the change that found them green (chosen) | A one-line switch each | The rows were reviewed with the rest and cost one edit | Rows that had not run when written |
| (b) Nothing until the classes land | The rows are written later | No code that has not run | The set `deb` would have passed without them |

**Choice:** (a). Both rows are on and green; the switches are `true` in `test/e2e/deb_mirror_test.go` and `test/e2e/deb_virtual_test.go`. The first run of the virtual row found that the merged architecture index did not carry the stub's `all` packages (the stub keeps them in `binary-all` only; the real archives list them in both); [Q-DEB-110](#q-deb-110--the-merged-index-of-an-architecture-lists-the-architecture-all-packages-of-the-members) changed the product.

**Rationale:** The classes were built while WP10 was; the first run on kind after each landed decided what had to change in the row.

**Applied in the spec:** none.

### Q-DEB-110 · The merged index of an architecture lists the architecture all packages of the members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: F-118. The merge of a virtual repository read, for `main/binary-amd64/Packages`, only the members' `main/binary-amd64/Packages`. The archives of Debian and Ubuntu list their `all` packages in every architecture index and in `binary-all`, so nothing was lost through them; an upstream that keeps them in `binary-all` only (the test archive does) lost them from the merged architecture index. Reproduced on 2026-11-09 (`TestR_DEB_025_ThePackagesOfArchitectureAllOfARemoteMemberAreInEveryArchitectureIndex`): `trove-config`, an `all` package of the stub, was in the merged `binary-all` and missing from the merged `binary-amd64`. The merged `Release` lists `binary-all` among its hashed files and does not list `all` under `Architectures`, as a local repository's does not.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each member's `binary-all` is also read into the index of every architecture, after the member's own index of that architecture (chosen) | The merge of `binary-<arch>` walks two sources per member; the kept set drops a package both list | The merged repository looks like a local one and like Debian's own archives; no assumption about how an APT version treats `binary-all`; equal inputs still give equal bytes and an upstream that lists `all` in both gives the bytes it gave before | The `all` packages are in every architecture index (as in a local repository) |
| (b) Publish only `binary-all` and rely on the client | The merged `binary-all` is the only home of `all` packages | Smallest indexes | The client's behaviour depends on its APT version and on the `Architectures` field of the `Release`; the e2e row of the virtual repository showed a client that did not install the `all` dependencies |
| (c) State `No-Support-for-Architecture-all` in the merged `Release` and list `all` in every architecture index | The field tells APT not to fetch `binary-all` | Clients fetch one index less | A second statement of the same fact; the merged `binary-all` would still have to be served for clients that read it, and the local repository would have to change too |

**Choice:** (a). In `virtualSuite` ([virtual_build.go](../../../internal/formatdata/deb/virtual_build.go)) the sources of an index of an architecture other than `all` are the member's `binary-<arch>` and then its `binary-all`; the index of `all` stays the member's `binary-all`.

**Rationale:** A virtual repository is meant to be indistinguishable from a repository that holds the files itself, and a local repository already lists `all` in both places (R-DEB-009). The cost is the second read of the member's `binary-all` per architecture, which is a stored document read as a stream. The development agent would revisit it if an upstream lists a different set in `binary-all` than in its architecture indexes for the same package and version (the first one read would win, the architecture index of the member).

**Applied in the spec:** R-DEB-025 (what it serves, architecture `all`). Code: `virtualSuite` in [virtual_build.go](../../../internal/formatdata/deb/virtual_build.go). Test: `TestR_DEB_025_ThePackagesOfArchitectureAllOfARemoteMemberAreInEveryArchitectureIndex` ([merge_test.go](../../../test/integration/debvirtual/merge_test.go)), and the row `TestDebVirtual` ([deb_virtual_test.go](../../../test/e2e/deb_virtual_test.go)).

### Q-DEB-111 · A test that waits for a failed virtual build waits for the build job, not for a period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `TestR_DEB_025_AReleaseNoCurrentKeyVerifiesIsNotMerged` failed in Test and Release 42894886354 (shard c): "the suite to fail with member_unverified did not happen within 1m30s", 106 s against 16 s locally. The test changes the remote member's `deb.upstreamKeys`; `debRepositoryHook.OnUpdated` enqueues the build of the virtual repository in the same transaction and without delay, so the failed suite is the result of one job the test can name. The test polled the suite's state for 90 s. The log of the run shows the replica's readiness checks for the database, schema and object store timing out for minutes and the task `deb-index-refresh` failing with `closed pool` in that window: the runner stood still, and a poll of fixed length turned a slow build into a failed test. Verified: the build is enqueued by the change and is not waiting for the merge period (the test's `mergedMetadataTtlSeconds` is 3600). What closed the pool of the refresh task is found in [Q-DEB-120](#q-deb-120--the-periodic-refresh-task-runs-the-remote-part-of-its-own-registry-only).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Raise the 90 s | A longer poll | One number | Moves the failure to the next slower runner; a wrong result is found only after the whole period |
| (b) Wait for the build job of the virtual repository that started after the change to end, then assert the suite once (chosen) | `failedWith` takes the database clock at the change (`mark`) and waits for a `deb-index` job with the key `index:<id>` that started after it and ended; the assertion (state `failed`, reason prefix, generation unchanged, the last index still served) is the one of before and runs at once | A slow runner makes the test late and not red; a build that ends with another result fails at once with that result; the bound (5 minutes) is the hang of a job that never ends | Reads the job table of the stack, as other tests of the integration tier do |
| (c) Call `rebuild()` and poll | Trigger the build by hand | Looks explicit | A second build next to the one the change enqueued; the change's own path, which the test is about, is not the one that runs |

**Choice:** (b), in `failedWith` and `buildAfter` ([members_test.go](../../../test/integration/debvirtual/members_test.go)); the rollback test (`member_rollback`) uses the same helper.

**Rationale:** The event is the end of the job; the state the test asserts is its result. The development agent would revisit the choice if the job's retries (a failed attempt returns it to the queue) were to make a build that failed for another reason look unfinished: the 5-minute bound would then report the hang.

**Applied in the spec:** none (R-DEB-025 is unchanged). Test: `TestR_DEB_025_AReleaseNoCurrentKeyVerifiesIsNotMerged`, `TestR_DEB_025_AReleaseOlderThanTheRecordedDateIsNotMerged`.

### Q-DEB-120 · The periodic refresh task runs the remote part of its own registry only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the task `deb-index-refresh` failed three times with `closed pool` in Test and Release 42894886354 (shard c, `debvirtual`, 20:46:04 to 20:46:11), while the replica of the test was running and no shutdown had begun (its `shutting down` line is at 20:46:47). Verified in the code: `registerDebRemoteRefresh` appended a closure over the App to a process-wide list (`RegisterRefreshHook`) every time an App was built, and `RunRefreshJob` ran the whole list. A process that builds one replica per test, as the integration tests do, therefore ran the part of every replica built before it, on the pools those replicas had closed at their shutdown. Only a test that lasts past a minute boundary meets it, because the task is cron-scheduled every minute: `TestR_DEB_025_AReleaseNoCurrentKeyVerifiesIsNotMerged` took 106 s. In production a process holds one App, so the list held one part; the defect was the shape of the registration, which let a stopped replica's part run. Reproduced on the old code by a test that registers a failing part and then a good one: the task returns the first error.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The part is part of the registry (chosen) | `Handlers` (then `RemoteHandlers`) carries `Refresh`; `RegisterJobs` builds the task's handler from it, the local and virtual parts are plain calls of the bound indexer; `RegisterRefreshHook` and the list are removed | A registry holds its own part; nothing outlives a replica; fewer moving parts than a hook list | The virtual part is no longer registered from an `init` of its file |
| (b) Unregister the hook at the shutdown | `Stop` removes the closure | Keeps the list | Another process-wide mutable list to keep in step with every shutdown path; a replica that fails to start leaves its hook |
| (c) Skip a part whose database is closed | The hook checks the pool | Small | Hides the stale part instead of removing it; the part of a stopped replica would still read the wrong stack's rows if its pools were open |

**Choice:** (a), in [jobs.go](../../../internal/formatdata/deb/jobs.go) and [refresh.go](../../../internal/formatdata/deb/refresh.go); the composition root hands the part over in `debHandlers` ([wire_deb_remote.go](../../../internal/app/wire_deb_remote.go)).

**Rationale:** A value that belongs to one replica must be held by the registry that replica builds, not by a global. The development agent would revisit the choice if a second kind of task part appeared that is not tied to a replica: it would then be a field of the same struct. The bound indexer (`BindIndexer`) is still one per process; a process that runs two replicas at once uses the last one's builder for the index jobs (not a production shape; closed by [Q-DEB-121](#q-deb-121--the-index-builder-is-held-by-the-replica-that-builds-it-not-by-the-process)).

**Applied in the spec:** none (R-DEB-010 and R-DEB-026 are unchanged). Test: `TestR_DEB_010_TheRefreshTaskRunsTheRemotePartOfItsOwnRegistryOnly` ([refresh_test.go](../../../internal/formatdata/deb/refresh_test.go)).

### Q-DEB-121 · The index builder is held by the replica that builds it, not by the process

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [Q-DEB-120](#q-deb-120--the-periodic-refresh-task-runs-the-remote-part-of-its-own-registry-only) moved the remote part of the refresh task into the registry
and left `BindIndexer` and `Bound()`, one atomic pointer per process, which the composition root set on every replica that claims jobs (finding F-125). Verified in the code: a process
that starts two replicas, as the integration tests do (`deblocal` starts up to three of them), ran the jobs `deb-index` and the local and virtual parts of
`deb-index-refresh` of both on the builder of the replica started last, with its pools, publication port and fault injection. Not live in production,
where a process holds one replica.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The builder travels in the registry's handlers (chosen) | `Handlers.Indexer` (a getter, because the replica builds the builder after the registry) is read by `classHandler` and `runRefresh`; the App keeps its builder in a `late` value and gives it to tests as `App.DebIndexer()`; the process-wide pointer, `BindIndexer`, `Bound()` and the exported `RunIndexJob` are removed | The way Q-DEB-120 did it for the remote part: nothing outlives a replica, and a test addresses the builder of the replica it started | The tests that injected a fault through `debdata.Bound()` name their replica |
| (b) A map from replica to builder in the package | `BindIndexer(name, x)` | Keeps the call shape | Another process-wide list that has to follow every shutdown |
| (c) Leave it, document the limit | The pointer stays | No change | Two replicas in a process stay unequal, and a stopped replica's builder is what the next test's jobs run on |

**Choice:** (a), in [jobs.go](../../../internal/formatdata/deb/jobs.go) (`Handlers`, `RegisterJobs`, `classHandler`), [build.go](../../../internal/formatdata/deb/build.go) (`runIndexJob`),
[refresh.go](../../../internal/formatdata/deb/refresh.go) (`runRefresh`) and the composition root ([wire_deb_index.go](../../../internal/app/wire_deb_index.go),
[wire_deb_remote.go](../../../internal/app/wire_deb_remote.go)). `RemoteHandlers` is renamed `Handlers`: it holds more than the remote class's now.

**Rationale:** The same rule as Q-DEB-120: what belongs to one replica is held by the registry that replica builds. The development agent would revisit the choice if a replica
came to build two indexers (one per queue, say), which would then be two fields of the same struct.

**Applied in the spec:** none (R-DEB-010 is unchanged). Tests: `TestR_DEB_010_TheIndexJobRunsTheBuilderOfItsOwnRegistry` and
`TestR_DEB_010_AnIndexJobOfARegistryWithoutABuilderFailsPermanently` ([indexer_binding_test.go](../../../internal/formatdata/deb/indexer_binding_test.go)),
`TestR_DEB_010_EveryReplicaHasItsOwnIndexBuilder` ([replicas_test.go](../../../test/integration/deblocal/replicas_test.go)).

## What this does not cover

### H-620 · A holder of the stored-secret key ring and a database copy can sign for every Debian repository

The private signing keys of local and virtual Debian repositories are sealed with the stored-secret key ring of R-ARCH-021
(R-DEB-015, built in T-17). Whoever holds a key file of the ring and a copy of the database or of a backup can open every signing
key and sign indexes that every client of those repositories accepts, without trove recording anything. Clients still download
from trove over TLS, so a forged index helps only an attacker who can also serve it (a party on the client's path or a compromised
mirror of trove's content). Live once the format is built; real for an operator or attacker with read access to the key ring
Secret and the database, which already opens every sealed upstream credential ([H-579](oci.md#h-579--a-holder-of-the-stored-secret-key-ring-can-mint-registry-tokens) is the same trust for registry tokens).

### H-621 · trove cannot tell which clients still trust only a retired key

A rotation reaches a client only when its keyring file is replaced (R-DEB-017). trove sees requests, not the keys clients hold, so
it cannot warn before a promotion that clients still trust only the old key; those clients fail closed after the promotion. Live
once rotation is built; real for fleets whose keyring files are not managed centrally.

### H-622 · A signing key created after the newest backup is lost by a restore

A private key exists only sealed in the database (R-DEB-015). A key created after the newest backup is not in it. When such a key
was promoted after the snapshot, the journal replay finds the promotion but not the new key, withholds the old primary (R-DEB-032)
and leaves the repository without a primary: builds fail with `no_signing_key` until an administrator reinstates the old key or
creates and promotes a new one, and clients that trust only the lost key fail until they get the repository's current key. A key
created before the snapshot and promoted after it is not lost: the replay promotes it. Live once the format is built; real for
every restore that follows a rotation whose next key is newer than the backup.

### H-623 · A pull-through remote without upstream keys stores what its upstream sends

A remote without `deb.upstreamKeys` stores release documents unverified (R-DEB-020). Clients verify the upstream's signatures with
their own keyrings, so a forged release fails at the client, but the forged bytes stay in the cache and fail every client until the
next refresh or an operator's deletion; over a plain HTTP upstream a party on trove's path can cause this. Such a remote can be
neither a mirror nor a virtual member. Live once the remote class is built; real for remotes of HTTP archives without keys.

### H-624 · trove's own Release files carry no Valid-Until

A local repository's `Release` files have no `Valid-Until` (R-DEB-011), so a client accepts an old, validly signed `InRelease`
indefinitely: a party that can keep serving the client the generation it already holds keeps it on old packages (a freeze attack).
APT keeps its newer list when served an older `Date` (verified), so the attacker cannot roll a client back, only hold it. TLS to
trove prevents this for clients that reach trove directly. A virtual repository carries its member releases' earliest
`Valid-Until` (Q-DEB-32) and is not affected while a member has one. Live once the format is built; real for clients behind a
TLS-terminating proxy that an attacker controls.

### H-625 · Pool files no parsed index names are stored unverified

Under `checksumPolicy: verify-if-available`, a remote stores a package file without a digest to compare when no parsed index names
its path yet (R-DEB-022): between the storing of a new release and the end of the parse it enqueues (Q-DEB-36), or before the
first parse of an index. Rows follow the release, not the clients, so pdiffs do not keep them stale; the window is the parse's
duration. The client verifies the file against the index it read, so a wrong file fails at the client, and stays in the cache until
an operator deletes it. `require` closes the gap at the cost of 502 answers in that window. Live once the remote class is built;
real for upstreams that serve wrong pool files.

### H-626 · A verified remote fails closed when its upstream signs with a new key

A remote verifies against the keys its operator configured (R-DEB-020). When the upstream archive starts signing with a key that is
not in the set (Debian adds an archive key per release), trove refuses the new release with 502 until the operator adds the key;
clients keep failing their updates through that remote meanwhile. Live once the remote class is built; real at every archive key
change of an upstream.

### H-627 · An imported signing key stays known to whoever imported it

An imported key (R-DEB-015) was outside trove before the import; whoever held it can still sign indexes that the repository's
clients accept. trove erases its own copy at retirement but cannot make the importer forget it. Live once import is built; real
for every imported key until it is retired and the clients' keyring files no longer hold it.

### H-628 · An offline mirror serves only clients whose index targets it keeps

A mirror keeps `Packages`, the chosen translations and, on request, `Sources` of its suites (Q-DEB-17). APT on desktop systems also
asks for `dep11` metadata, and Ubuntu's `command-not-found` for `cnf` files; while the upstream is unreachable those requests fail,
and `apt-get update` reports errors although every package the mirror keeps installs. Live once the mirror is built; real for
desktop and Ubuntu clients that use a mirror offline.

### H-629 · A forged journal entry can withhold a repository's signing key until an administrator acts

The revocation journal lives in the object store, which the runtime S3 credential can write (R-HA-054). A forged
`deb.signing_key.retired` or `deb.signing_key.promoted` entry makes the replay after a restore withhold the named key (R-DEB-032).
Nothing is erased, and an administrator reinstates the key, but until then the repository publishes no new generation. Live once
the format is built; real for a holder of the runtime S3 credential combined with a restore.

### H-630 · A restore without remote caches forgets the newest upstream release a remote had seen

The anti-rollback of a remote compares a received release with `floor_date`, the newest `Date` it ever verified for the directory
(R-DEB-019, Q-DEB-42). A restore with `backup.exclude.remote_caches` deletes the stored releases and their floors with the cache
rows (R-BAK-008), so the first release a remote stores afterwards is taken as it is, even when it is older than one the remote had
served before. A virtual repository still refuses it by its own record (R-DEB-025), and each client's APT keeps its newer list
(verified), but a pull-through client that never saw the newer release and a mirror pass accept it. Live once the remote class is
built; real for restores without remote caches while an upstream mirror serves an old release.

### H-721 · The index of a deleted package stays fetchable by hash for the keep time

A deleted package file is gone from the next generation, and the `.deb` answers 404. The index files of the generations before it are
kept by hash for `deb.by_hash_keep` and for the three newest superseded generations whatever their age (R-DEB-012, Q-DEB-51), so the
paragraph that describes the deleted package (its control fields, size and digests, not its content) can be fetched by anyone who may read
the index and knows the hash of an older `Release`. A deletion for a reason that makes the description sensitive is therefore not complete
for at least the keep time. Live today for local repositories; an operator shortens `deb.by_hash_keep` to ten minutes at the lowest.

### H-722 · A repository whose builds fail keeps serving its last generation

A build that fails (no usable key, a row that does not render, a signing failure) publishes nothing: the suite keeps its previous signed
generation and the state read says `failed` with the reason (R-DEB-010). Clients keep getting the old index, so a change that was meant
to take a package away (a deletion, a moved coordinate) does not reach them until a build succeeds. Nothing but the state read, the job
list and the metric shows it. Live whenever a key is withheld or retired without a replacement (R-DEB-032, H-622) and for a suite whose
rows no longer render.

### H-720 · A path pattern that hides a package file does not hide its version from the package screens

The decision point decides a version on the path of its file (Q-AUTHZ-255), through the `VersionPather`, and a Debian version has one
file per architecture below the pool directory of its source package. `VersionPath` names the file of the source package that bears the
package's name, architecture `all` (Q-DEB-47). A grant whose path patterns hide `pool/…` by the package's name or by the version's file
name hides the version from the package screens and version lists; a pattern that addresses only the directory of another source
package, or one architecture's file name, hides the file, which stays hidden on the pool route, but not the version. Live today for
local repositories with such patterns; the file is never exposed by it.
