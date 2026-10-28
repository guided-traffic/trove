# Beyond the baseline: further formats and capabilities

The other pages of this catalog describe the baseline feature set of a full-featured repository
manager. This page goes past that baseline: package formats and registry capabilities that the
wider ecosystem defines but the baseline does not cover, or covers only partly. Every entry states what the format or
capability is, the specification or protocol it follows (linked to the standard, PEP, RFC or spec
repository), which clients use it, why it matters for trove, a rough implementation size, and a
priority under the rubric in [README.md](README.md). Protocol facts come from the cited sources as
read on 2026-10-28. Settings listed here are proposals derived from those sources; a default is
given only where a source states one, otherwise it is *unspecified*. Sizes mean: **small**, an
adapter on an existing format or mechanism without a new persistent model; **medium**, a new
protocol surface, index generation or metadata model; **large**, a new subsystem with its own keys,
logs or policy engine.

## Candidates already covered by the baseline

Evaluated and found covered, fully or for the part trove needs first. Only the gap listed in the
last column is catalogued on this page.

| Candidate | Where the baseline covers it | Remaining gap on this page |
|---|---|---|
| Chocolatey | PKG-NUGET-038 in [formats/nuget.md](formats/nuget.md). At the Chocolatey CLI 2.0 release the public community repository was NuGet v2 only (PKG-NUGET-004); the CLI from 2.0 also reads NuGet v3 feeds. | none; proxying the community repository depends on NuGet v2 |
| PowerShell Gallery | PKG-NUGET-037 in [formats/nuget.md](formats/nuget.md). The gallery is a NuGet v2 endpoint; the PSResourceGet module reads v2 and v3 feeds, but name-wildcard, command-name and DSC-resource searches are unavailable on v3 feeds. | none; same NuGet v2 dependency |
| Cargo sparse index | PKG-CARGO-002 and PKG-CARGO-003 in [formats/cargo.md](formats/cargo.md), including the `pubtime` field | publish time used for age gating: BEYOND-027 |
| PEP 691 JSON simple API, PEP 658/714 metadata files, PEP 700 fields | PKG-PYPI-012 and PKG-PYPI-028 in [formats/pypi.md](formats/pypi.md) | API versions 1.3 and 1.4: BEYOND-022, BEYOND-023 |
| Go checksum database proxying | PKG-GO-014 in [formats/go.md](formats/go.md) | a checksum database for hosted modules: BEYOND-026 |
| OCI 1.1 referrers and artifact types | PKG-OCI-008 to PKG-OCI-012 in [formats/oci.md](formats/oci.md) | remote and virtual referrers, lifecycle, type recognition, `ns` routing: BEYOND-015 to BEYOND-018 |
| WebAssembly modules on OCI | PKG-OCI-016 (single-module tool) | component layout and package discovery: BEYOND-012 |
| Editor extensions (VS Code compatible gallery, Open VSX upstream) | PKG-AI-018 in [formats/ai-and-ide.md](formats/ai-and-ide.md), remote only | hosted and virtual repositories with the Open VSX publish API: BEYOND-011 |
| Conda sharded repodata (CEP 16) | PKG-CONDA-009 in [formats/conda.md](formats/conda.md) | none |
| Terraform provider network mirror | PKG-TF-008 in [formats/terraform.md](formats/terraform.md) | none |
| npm provenance | pass-through only, PKG-NPM-024 and PKG-NPM-037 in [formats/npm.md](formats/npm.md) | provenance for locally published packages: BEYOND-021 |
| SBOM | generation (SEC-065), export (SEC-066) and import of supplied SBOMs into the scanner (SEC-067) in [security-scanning-and-curation.md](security-scanning-and-curation.md); all three depend on the scanning engine | format-neutral store for supplied SBOMs without the scanning engine: BEYOND-024 |
| Upstream package age gating | a condition of the curation service, SEC-014 | a plain remote-repository setting: BEYOND-027 |
| Dependency-confusion protection | priority resolution RMT-037 and include/exclude patterns RMT-034 in [remote-repositories.md](remote-repositories.md) | namespace claims: BEYOND-028 |
| Cache pre-warming | per path or folder, RMT-027 | lockfile- and SBOM-driven warming: BEYOND-029 |
| Quotas | disk thresholds STOR-040 in [storage-and-blobs.md](storage-and-blobs.md); project storage quota IAM-061 in [identity-and-access.md](identity-and-access.md) | multi-dimension tenant quotas and rate limits: BEYOND-030 |
| OIDC trust for CI systems | generic, GitHub Actions and Azure DevOps types, CIOIDC-002 to CIOIDC-004 in [oidc-ci-integration.md](oidc-ci-integration.md) | further presets, Kubernetes, SPIFFE: BEYOND-031 to BEYOND-033 |
| Snap packages, Microsoft Store source of winget | not catalogued | none: no public server-side protocol specification was found |

## Package formats

### BEYOND-001 · Arch Linux pacman repositories
Priority: **P2**

**What it is.** Binary package repositories for pacman, the package manager of Arch Linux and its
derivatives. A repository is a directory served over HTTP(S) holding a package database and the
package files.

**Specification.** [repo-add(8)](https://man.archlinux.org/man/repo-add.8),
[pacman.conf(5)](https://man.archlinux.org/man/pacman.conf.5), and the
[repo-add script](https://gitlab.archlinux.org/pacman/pacman/-/blob/master/scripts/repo-add.sh.in)
as the reference for the database entry format.

**Clients.** pacman (install, sync), makepkg (builds packages), repo-add and repo-remove (database
maintenance).

**Why it matters for trove.** Arch-based build images and workstations benefit from a caching
proxy of the public mirrors, and teams with internal packages need a hosted repository with access
control instead of a plain web directory.

**Size.** medium — the database must be regenerated on every upload and delete, and database
signatures need a server-side OpenPGP key.

Behavior:

- Client configuration is a section per repository with one or more `Server = <url>` lines. pacman
  expands `$repo` to the section name and `$arch` to the first configured architecture. Repositories
  listed first win when two repositories contain a package of the same name, regardless of version.
- From each server pacman fetches `<repo>.db` (the package database), optionally `<repo>.db.sig`,
  `<repo>.files` (file lists, only for file queries), and the package files with optional
  detached `.sig` files.
- The database is a tar archive, optionally compressed (`.db.tar`, `.db.tar.gz`, `.db.tar.xz`,
  `.db.tar.zst` and others listed in repo-add(8)); `<repo>.db` is a link to the compressed file.
  Each package is a directory `<name>-<version>/` with a `desc` file of `%KEY%` blocks, built from
  the package's `.PKGINFO`: FILENAME, NAME, BASE, VERSION, DESC, GROUPS, CSIZE, ISIZE, SHA256SUM,
  PGPSIG (base64 package signature, only when signatures are included), URL, LICENSE, ARCH,
  BUILDDATE, PACKAGER, REPLACES, CONFLICTS, PROVIDES, DEPENDS, OPTDEPENDS, MAKEDEPENDS,
  CHECKDEPENDS. The `.files` database additionally carries a `files` entry with a `%FILES%` list.
- One entry per package name: adding a package replaces the existing entry for that name, also with
  an older version, unless downgrade prevention is on.
- Signature checking on the client has the built-in default `SigLevel = Required TrustedOnly`:
  packages and databases must carry signatures from fully trusted keys. The `pacman.conf` that
  Arch Linux ships sets `SigLevel = Required DatabaseOptional` globally, so on a stock Arch system
  package signatures are required and database signatures are checked only when present. Package signatures normally come from
  the packager; since they are detached `.sig` files, a server key that clients mark as fully
  trusted could also sign packages, at the price of the server vouching for content it did not
  build. The database can only be signed by the server, because it regenerates it.
- A `CacheServer = <url>` line makes pacman try that server first for packages and signatures (not
  for databases) and keeps it in the pool after a 404, which suits a pull-through cache.
- Remote repositories: databases and their signatures are mutable metadata; package files are
  immutable. A virtual repository that merges databases changes the precedence semantics above and
  invalidates upstream database signatures; merging requires re-signing with the server's key.

| Setting | Meaning | Default |
|---|---|---|
| Repository name | Value clients use as `$repo`; also the database file name | unspecified |
| Architectures | Architecture subtrees served (`$arch`) | unspecified |
| Database compression | Archive extension of the generated database | unspecified |
| Include package signatures in the database | Embed `.sig` content as PGPSIG | off (repo-add includes them only with `--include-sigs`) |
| Database signing key | OpenPGP key that signs `<repo>.db` | unspecified |
| Prevent downgrade | Refuse an upload older than the version in the database | off (repo-add option `-p`) |
| Remove replaced package files | Delete the file of a replaced entry | off (repo-add option `-R`) |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Upload package | package file, optional detached `.sig` | database regenerated | deploy |
| Remove package | package name | entry removed, database regenerated | delete |
| Recalculate database | repository | rebuilt `.db` and `.files` with signatures | manage |
| Client fetch | database, signature or package path | file | read |

UI: repository settings, format section (architectures, signing key, signature options).

Limits and edge cases: repo-add rejects ASCII-armored package signatures; only binary detached
signatures are valid. The database must never reference a file the repository cannot serve.

Interactions: signing keys IAM-046 in [identity-and-access.md](identity-and-access.md); metadata
cache period RMT-018 in [remote-repositories.md](remote-repositories.md); content-addressed
storage STOR-001 in [storage-and-blobs.md](storage-and-blobs.md).

### BEYOND-002 · Homebrew bottle, API and download mirrors
Priority: **P2**

**What it is.** Homebrew installs prebuilt binaries ("bottles", gzipped tarballs) from an OCI
registry, reads formula and cask metadata from a signed JSON API, and downloads sources and cask
payloads from arbitrary URLs. Three environment variables redirect these to a mirror.

**Specification.** Homebrew's [environment variable reference](https://docs.brew.sh/Manpage#environment)
(source: [env_config.rb](https://github.com/Homebrew/brew/blob/main/Library/Homebrew/env_config.rb)),
[Bottles](https://docs.brew.sh/Bottles), and the API client in
[api.rb](https://github.com/Homebrew/brew/blob/main/Library/Homebrew/api.rb).

**Clients.** `brew` on macOS and Linux.

**Why it matters for trove.** Developer machines and CI runners install from Homebrew daily; a
mirror speeds installs, works in restricted networks, and records what was installed. The bottle
part reuses OCI remote repositories.

**Size.** medium — OCI proxying exists (P1); the work is a byte-exact JSON pass-through and a
URL-prefix download proxy that needs a host allowlist.

Behavior:

- `HOMEBREW_BOTTLE_DOMAIN`: download mirror for bottles and their manifests. If the mirror lacks a
  bottle or manifest, brew falls back to the default bottle domain. Default: Homebrew's default
  bottle domain (an OCI registry path, see the reference above).
- `HOMEBREW_ARTIFACT_DOMAIN`: prefix for all downloads. A source URL
  `https://example.com/foo.tar.gz` becomes `<prefix>/https://example.com/foo.tar.gz`. Bottle URLs
  have their domain replaced instead, so the OCI path `/v2/homebrew/core/<formula>/manifests/<tag>`
  is kept; a prefix that already contains `/v2` is not doubled
  (`https://mirror.example.com/v2/<sub-path>/homebrew/core/...`).
  `HOMEBREW_ARTIFACT_DOMAIN_NO_FALLBACK` makes brew fail instead of trying other URLs.
- `HOMEBREW_API_DOMAIN`: mirror for the JSON API, default `https://formulae.brew.sh/api`, with
  fallback to the default when the mirror is unavailable. The current client fetches
  `internal/packages.<bottle tag>.jws.json`. These files are JSON Web Signatures (a signature with
  key ID `homebrew-1`, RSA-PSS with SHA-512) verified against a public key built into brew, so the
  mirror must serve them byte for byte and cannot rewrite URLs inside them.
- Authentication: with `HOMEBREW_ARTIFACT_DOMAIN` set and neither
  `HOMEBREW_DOCKER_REGISTRY_TOKEN` (bearer) nor `HOMEBREW_DOCKER_REGISTRY_BASIC_AUTH_TOKEN` (base64
  user and password; `none` sends no header) set, brew sends no `Authorization` header for bottle
  downloads. The bottle repository then needs anonymous read, or clients set one of the two
  variables.
- Bottle layout in OCI: one image index per formula version, tagged with the version plus rebuild
  (`org.opencontainers.image.ref.name` annotation), one manifest per platform derived from the
  bottle's build data (architecture, OS, OS version; glibc version on Linux). brew treats bottle
  blobs addressed by `sha256` digest as immutable.
- Taps can point their formulas at their own bottle root with `root_url` in the `bottle do` block,
  so a private tap can publish bottles to a trove OCI repository.

| Setting | Meaning | Default |
|---|---|---|
| Bottle upstream | OCI remote upstream for bottles | Homebrew's default bottle domain |
| API upstream | Remote upstream for the JSON API files | `https://formulae.brew.sh/api` (Homebrew's default) |
| Anonymous bottle read | Allow brew's unauthenticated bottle requests | unspecified |
| Source host allowlist | Hosts the URL-prefix proxy may fetch from | unspecified |
| API metadata cache period | How long API files are served from cache | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Bottle pull | OCI manifest or blob request | manifest or blob | read |
| API file | path below the API base | the signed JSON file, unchanged | read |
| Prefixed download | `<prefix>/<absolute URL>` | the upstream file | read |

UI: repository settings of the remote repositories involved; client setup instructions list the
three variables.

Limits and edge cases: the URL-prefix proxy can be abused to reach arbitrary hosts (server-side
request forgery); it must enforce the allowlist and refuse redirects to non-allowed or internal
addresses. `HOMEBREW_NO_INSTALL_FROM_API` switches brew to Git checkouts of the taps, which this
entry does not cover.

Interactions: PKG-OCI-003 in [formats/oci.md](formats/oci.md); generic remotes in
[formats/generic.md](formats/generic.md); stale-metadata fallback RMT-019 in
[remote-repositories.md](remote-repositories.md); BEYOND-029.

### BEYOND-003 · winget REST sources
Priority: **P2**

**What it is.** The Windows Package Manager (winget) can use a REST source: an HTTP API that
returns package manifests. Installers are referenced by URL with a SHA-256 hash and are downloaded
from those URLs.

**Specification.** OpenAPI document of the REST source API, latest listed version 1.10.0:
[WinGet-1.10.0.yaml](https://github.com/microsoft/winget-cli-restsource/blob/main/documentation/WinGet-1.10.0.yaml);
client command [winget source](https://learn.microsoft.com/en-us/windows/package-manager/winget/source).

**Clients.** winget: `winget source add --name <name> --arg <url> --type Microsoft.Rest`, optional
`--header <value>` (sent as the `Windows-Package-Manager` header on every REST call),
`--trust-level`, `--explicit`. Adding a source requires administrator rights on the machine.

**Why it matters for trove.** Windows fleets want an internal catalog of approved installers;
winget is the built-in client.

**Size.** medium — a read API over stored manifests, a search engine over the match fields, and
installer hosting with URL rewriting.

Behavior:

- Client-facing operations: `GET /information` (server information), `POST /manifestSearch`
  (search, `200` with results or `204` with none), `GET /packageManifests/{PackageIdentifier}`
  (manifest; query parameters for version, channel and market). Every call may carry the
  `Windows-Package-Manager` header; paged results carry a continuation token.
- Server information must contain `SourceIdentifier` and `ServerSupportedVersions`; optional
  fields are `SourceAgreements`, `UnsupportedPackageMatchFields`, `RequiredPackageMatchFields`,
  `UnsupportedQueryParameters`, `RequiredQueryParameters` and `Authentication`.
- Search request: `MaximumResults`, `FetchAllManifests`, `Query` (keyword and match type),
  `Inclusions` and `Filters` (each a match field plus keyword and match type). Match types: Exact,
  CaseInsensitive, StartsWith, Substring, Wildcard, Fuzzy, FuzzySubstring. Match fields:
  PackageIdentifier, PackageName, Moniker, Command, Tag, PackageFamilyName, ProductCode,
  UpgradeCode, NormalizedPackageNameAndPublisher, Market, HasInstallerType.
- Authentication of the source is limited to `none` or Microsoft Entra ID (resource and optional
  scope); the information endpoint should not require authentication. Installer downloads allow
  the same two types plus a third, Microsoft Entra ID for Azure Blob Storage.
- The reference OpenAPI also defines management operations (create, update, delete of packages,
  versions, locales and installers, and whole package manifests); they are not used by the client.

| Setting | Meaning | Default |
|---|---|---|
| Source identifier | Value returned in server information | unspecified |
| Supported API versions | Versions announced to clients | unspecified |
| Source agreements | Agreement texts clients must accept | unspecified |
| Installer hosting | Rewrite installer URLs to trove-hosted copies | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Server information | none | information object | anonymous |
| Manifest search | search request | matching manifests or 204 | read |
| Get manifest | package identifier, version, channel, market | manifest | read |
| Upload manifest and installers | manifest set, installer files | stored package version | deploy |

UI: repository browser shows manifests and installers per package identifier and version.

Limits and edge cases: trove's tokens (INV-8 in [../spec/README.md](../spec/README.md)) cannot be
presented through the defined authentication types; a private source is therefore either anonymous
on a protected network or bound to Microsoft Entra ID. Installer SHA-256 values in manifests must
match the hosted copies exactly. The default community source uses a different, pre-indexed source
type that this entry does not cover.

Interactions: generic hosting of installer files in [formats/generic.md](formats/generic.md);
search in [search-and-query.md](search-and-query.md).

### BEYOND-004 · Flatpak applications from OCI repositories
Priority: **P2**

**What it is.** Flatpak can install applications and runtimes stored as OCI images when the remote
URL uses the `oci+https://` scheme. The client queries an index endpoint that lists the images with
their Flatpak labels and then pulls from the OCI registry named in the answer.

**Specification.** [flatpak-remote(5)](https://docs.flatpak.org/en/latest/flatpak-command-reference.html)
and the client implementation in
[flatpak-oci-registry.c](https://github.com/flatpak/flatpak/blob/main/common/flatpak-oci-registry.c)
and [flatpak-json-oci.c](https://github.com/flatpak/flatpak/blob/main/common/flatpak-json-oci.c).

**Clients.** `flatpak remote-add <name> oci+https://<host>/<path>`, then `flatpak install`.

**Why it matters for trove.** Linux desktop and kiosk fleets distribute internal applications as
Flatpaks; the OCI route reuses trove's container storage, permissions and replication.

**Size.** medium — an index endpoint computed from labels of stored image configs, on top of the
OCI format.

Behavior:

- The client appends `index/` to the remote URL path unless it already ends with `/index/`, then
  `static`, and sends `GET <url>/index/static?label:org.flatpak.ref:exists=1&architecture=<oci
  arch>&os=linux&tag=<tag>`. The tag comes from the URL fragment, `latest` when absent.
- Response: JSON with `Registry` (base URL of the OCI registry that serves the images) and
  `Results`, a list of repositories with `Name`, `Images` and optional `Lists`. Each image entry has
  `Digest`, `MediaType`, `OS`, `Architecture` (all mandatory), `Annotations`, `Labels` and `Tags`;
  each list entry has `Digest`, `Images`, `MediaType` and `Tags`.
- Labels the client reads include `org.flatpak.ref`, `org.flatpak.metadata`,
  `org.flatpak.installed-size`, `org.flatpak.download-size` and commit-metadata labels such as
  `org.flatpak.commit-metadata.xa.token-type`, `org.flatpak.commit-metadata.ostree.endoflife` and
  `org.flatpak.commit-metadata.ostree.endoflife-rebase`.
- Signatures for OCI remotes can be loaded from a separate URL using the container signature
  lookaside protocol; OSTree GPG verification does not apply.

| Setting | Meaning | Default |
|---|---|---|
| Flatpak index | Serve the index endpoint for an OCI repository | unspecified |
| Registry URL in answers | Base URL written into `Registry` | unspecified |
| Signature lookaside URL | Where signatures are served for clients configured to verify | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Index query | label filter, architecture, OS, tag | index JSON filtered to readable repositories | read |
| Push application image | OCI image with Flatpak labels | stored image, index updated | deploy |

UI: OCI repository settings, Flatpak section; image view shows the Flatpak ref.

Limits and edge cases: the index must list only repositories the caller may read (IAM-032 in
[identity-and-access.md](identity-and-access.md)). Which query parameters besides the four above
clients may send is unspecified.

Interactions: PKG-OCI-001 and PKG-OCI-008 in [formats/oci.md](formats/oci.md); BEYOND-017;
BEYOND-005 for the native repository format.

### BEYOND-005 · OSTree archive repositories for Flatpak
Priority: **Later**

**What it is.** The native Flatpak repository format: an OSTree repository in `archive` mode,
served by a static web server. Flathub, the central Flatpak repository, uses it.

**Specification.** OSTree [repository anatomy](https://ostreedev.github.io/ostree/repo/) and
[data formats](https://ostreedev.github.io/ostree/formats/); Flatpak
[Hosting a repository](https://docs.flatpak.org/en/latest/hosting-a-repository.html).

**Clients.** flatpak (`flatpak remote-add` with a `.flatpakrepo` file or URL), ostree.

**Why it matters for trove.** Proxying Flathub gives caching and an install record; hosting
internal applications in the native format avoids the OCI route's client configuration.

**Size.** large — hosting requires creating OSTree commits, dirtree and dirmeta objects, a signed
summary and static deltas; the cited specifications reference no Go implementation.

Behavior:

- Repository content: a `config` file (mode `archive-z2`, which means the modern `archive` format;
  optionally a `collection-id`), an `objects/` directory of content-addressed SHA-256 objects
  (`.commit`, `.dirtree`, `.dirmeta`, and `.filez` content objects, gzip-compressed, whose file
  name is the hash of the uncompressed content), refs, a `summary` file mapping refs to commit
  checksums and listing static deltas, an optional detached GPG signature of the summary, and
  static deltas under `deltas/<from prefix>/<from suffix>-<to>`.
- A pull issues one HTTP request per changed object; HTTP keep-alive (or HTTP/2) is required for
  acceptable performance. Static deltas bundle the change between two commits.
- A `.flatpakrepo` file carries the mandatory `Url` and optional keys: `GPGKey` (base64 public
  key), `Version`, `DefaultBranch`, `Subset`, `Title`, `Comment`, `Description`, `Icon`, `Homepage`,
  `Filter` and collection keys (`DeployCollectionID`, `DeploySideloadCollectionID`,
  `CollectionID`) ([flatpakrepo(5)](https://docs.flatpak.org/en/latest/flatpak-command-reference.html)).
- Remote proxy: objects and deltas are immutable; `summary`, its signature and refs are mutable
  metadata. A pass-through proxy keeps upstream signatures valid; any merge requires trove's own
  signing key.

| Setting | Meaning | Default |
|---|---|---|
| Collection ID | Collection identifier announced in `config` | unspecified |
| Summary signing key | GPG key for the summary signature | unspecified |
| Generate static deltas | Produce deltas between consecutive commits | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch repository file | path | file | read |
| Import commit | OSTree commit and objects, or a Flatpak bundle | updated refs and summary | deploy |

UI: repository settings, format section (collection ID, signing key).

Limits and edge cases: the summary grows linearly with refs and deltas; it must be regenerated and
re-signed on every ref change.

Interactions: BEYOND-004; signing keys IAM-046 in [identity-and-access.md](identity-and-access.md).

### BEYOND-006 · Julia package server protocol
Priority: **P2**

**What it is.** Julia's package manager obtains registries, package source trees and binary
artifacts from a package server over HTTPS, with GET and HEAD only. All resources except the
registry list are content-addressed and cacheable forever.

**Specification.** [Package and Storage Server Protocol Reference](https://pkgdocs.julialang.org/dev/protocol/).

**Clients.** Julia's `Pkg`, pointed at a server by `JULIA_PKG_SERVER` (default
`pkg.julialang.org`; an empty value disables servers and falls back to Git clones).

**Why it matters for trove.** Scientific and data teams using Julia need caching and offline
operation; the protocol's immutability makes it a natural fit for content-addressed storage.

**Size.** medium for proxying (resources are verified by Git tree hash, which needs tree hashing of
extracted tarballs); large for hosted private packages, which require generating registry trees.

Behavior:

| Resource | Content | Caching |
|---|---|---|
| `/registries` | One line per registry: `/registry/<uuid>/<hash>` naming the current tree hash | changes |
| `/registry/<uuid>/<hash>` | Tarball of the registry at that tree hash | forever |
| `/package/<uuid>/<hash>` | Tarball of the package source tree at that tree hash | forever |
| `/artifact/<hash>` | Tarball of an artifact with that tree hash | forever |

- Compression: current clients send `Accept-Encoding: zstd, gzip`; older ones gzip only or nothing.
  Clients detect the actual format by magic bytes (`28 B5 2F FD` zstd, `1F 8B` gzip).
- Authentication: bearer tokens (RFC 6750). The client reads
  `$(DEPOT_PATH[1])/servers/<server>/auth.toml` with `access_token` (required) and optional
  `expires_at`, `expires_in`, `refresh_token`, `refresh_url`. Before each request it refreshes an
  expired token by calling `refresh_url` with `Authorization: Bearer <refresh_token>`; it refuses a
  refresh URL that is not HTTPS, and the refresh URL may live on another server. How the first
  `auth.toml` is obtained is left to the server and user agent.
- Verification: servers and clients should verify that each resource matches its tree hash (Git
  tree SHA-1 today) and reject mismatches.
- Virtual: registries have distinct UUIDs, so a virtual repository can serve the union of members'
  `/registries` lines; the immutable resources resolve from any member that has them.

| Setting | Meaning | Default |
|---|---|---|
| Upstream package server | Remote upstream | `pkg.julialang.org` (client default) |
| Registry list cache period | How long `/registries` is cached | unspecified |
| Verify tree hashes | Reject upstream resources whose tree hash does not match | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| List registries | none | lines of registry resources | read |
| Get registry, package or artifact | UUID and tree hash, or artifact hash | tarball | read |
| Refresh token | refresh token | new `auth.toml` content | token holder |

UI: repository browser by registry, package UUID and artifact hash.

Limits and edge cases: the token-acquisition flow is server-defined; trove would issue an
`auth.toml` from its token UI (TOK pages in [access-tokens.md](access-tokens.md)). Hosted private
packages need a private registry tree whose updates change the `/registries` answer.

Interactions: STOR-001 in [storage-and-blobs.md](storage-and-blobs.md); RMT-018 in
[remote-repositories.md](remote-repositories.md).

### BEYOND-007 · Hackage repositories for Haskell
Priority: **P2**

**What it is.** Package repositories for Haskell's cabal-install. Package tarballs sit under
`/package/`, an index tarball holds every package description, and secure repositories add TUF
metadata so that mirrors cannot alter what clients accept.

**Specification.** [hackage-security](https://github.com/haskell/hackage-security) (design and
layout), [hackage-repo-tool](https://github.com/haskell/hackage-security/tree/master/hackage-repo-tool)
(file-based secure repositories), [cabal configuration](https://cabal.readthedocs.io/en/stable/config.html#repository-specification).

**Clients.** cabal-install: `repository <name>` with `url:`, and for secure repositories
`secure: True`, `root-keys:` and `key-threshold:`; hackage.haskell.org is always used in secure
mode with built-in root keys.

**Why it matters for trove.** Haskell teams need a mirror and a home for internal packages; the
secure mode is the most complete TUF deployment among package ecosystems.

**Size.** medium for a pass-through remote; large for local or virtual repositories, which need
their own TUF roles (BEYOND-025).

Behavior:

- Files: `package/<name>-<version>.tar.gz`; the index tarball with `<name>/<version>/<name>.cabal`
  and, in secure repositories, `<name>/<version>/package.json` (TUF targets with hash and size of
  the package tarball); `00-index.tar.gz` (legacy index); `root.json`, `timestamp.json`,
  `snapshot.json`, `mirrors.json`.
- The incremental index (`01-index.tar`) is append-only. When the server answers with
  `Accept-Ranges: bytes`, the security library updates its local copy by requesting only the new
  tail with an HTTP range request; without range support it downloads the whole index file again
  on every update. Range support is therefore not mandatory, but without it every index update
  costs a full download.
- Remote: all files are passed through unchanged. Because they are signed with the upstream's keys,
  clients keep their upstream root keys and treat trove as an untrusted mirror.
- Local and virtual: the index changes, so trove must hold its own root, snapshot, timestamp and
  targets keys and clients must be configured with trove's root key IDs and threshold. In the
  file-based tool, timestamp and snapshot are valid for three days and must be re-signed regularly.
- `.cabal` file revisions: the index may contain several revisions of a package description.

| Setting | Meaning | Default |
|---|---|---|
| Upstream | Remote upstream index | unspecified |
| Root key IDs and threshold | Published for client configuration (local and virtual) | unspecified |
| Timestamp and snapshot validity | Re-sign interval | unspecified (three days in the file-based tool) |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch file | path, optional byte range | file or range | read |
| Upload package | source tarball | tarball stored, index and TUF metadata updated | deploy |
| Revise description | package, version, new `.cabal` | new revision appended to the index | manage |

UI: repository settings, format section (root key IDs, re-sign schedule).

Limits and edge cases: re-signing must run as a singleton across replicas; expired timestamp
metadata makes every client fail.

Interactions: BEYOND-025; signing keys IAM-046 in [identity-and-access.md](identity-and-access.md).

### BEYOND-008 · opam repositories for OCaml
Priority: **P2**

**What it is.** Package repositories for opam, the OCaml package manager. A repository is a tree
of package definitions plus an archive of the whole tree for HTTP clients; source archives are
cached in a content-addressed directory.

**Specification.** [opam manual](https://opam.ocaml.org/doc/Manual.html), sections *Repositories*,
*repo*, *URLs*, *Checksums* and the `archive-mirrors` configuration field.

**Clients.** `opam repository add <name> <url>`, `opam admin index`, `opam admin cache`.

**Why it matters for trove.** OCaml shops need a mirror and private packages; the archive cache
layout maps directly onto trove's content-addressed blob store.

**Size.** medium — generation of the repository archive from uploaded definitions, plus a
content-addressed archive cache.

Behavior:

- Layout: `/repo` (repository file), `/packages/<name>/<name>.<version>/opam` (definition, with
  optional `url` and `descr` overrides and a `files/` directory), `/index.tar.gz` (the whole tree
  except the cache, required for HTTP clients), `/cache/` (source archives by checksum),
  `/archives/` (conventional place for archives served by the repository itself).
- The `repo` file's optional fields: `opam-version`, `browse`, `upstream`, `redirect`,
  `archive-mirrors`, `announce`, `stamp`.
- Archive mirrors: opam looks archives up by their first hash at
  `<mirror>/<hash-algo>/<first two hex characters>/<hash>`; `archive-mirrors` in the repo file may be
  relative to the repository root (for example `cache/`). If a mirror misses, opam fetches from the
  archive's original URL.
- Checksums are `md5=`, `sha256=` or `sha512=` followed by exactly 32, 64 or 128 hex characters.
- Remote: `/repo` and `/index.tar.gz` are mutable; cache entries are immutable. Sources pinned to
  version-control URLs cannot be served from the cache.

| Setting | Meaning | Default |
|---|---|---|
| Upstream repository | Remote upstream tree | unspecified |
| Upstream archive cache | Remote upstream for archives by hash | unspecified |
| Announce cache in `repo` | Write `archive-mirrors` pointing at trove | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch repository file | path | file | read |
| Fetch archive by hash | algorithm and hash | archive | read |
| Publish package version | `opam` file, optional source archive | tree and `index.tar.gz` regenerated | deploy |

UI: repository browser by package and version.

Limits and edge cases: how opam presents credentials to an HTTP repository is unspecified in the
manual; anonymous read may be required in practice.

Interactions: STOR-001 in [storage-and-blobs.md](storage-and-blobs.md); BEYOND-029.

### BEYOND-009 · CPAN repositories for Perl
Priority: **P2**

**What it is.** The CPAN mirror layout used by Perl installers: distributions under per-author
directories and a package index that maps every Perl package name to the distribution that
provides it. A private repository with this layout is commonly called a DarkPAN.

**Specification.** The layout as served by [www.cpan.org](https://www.cpan.org/) and consumed by
[cpanm](https://metacpan.org/dist/App-cpanminus/view/bin/cpanm) and
[cpm](https://metacpan.org/pod/App::cpm::Tutorial).

**Clients.** cpanm (`--mirror <url>`, repeatable, default `http://www.cpan.org/`; `--mirror-only`
to use the mirror's index instead of the metadata database service; `--from`/`-M` to use one URL as
the only source), cpm (`--resolver 02packages,<url>`).

**Why it matters for trove.** Long-lived Perl estates need a reproducible mirror and a place for
internal distributions.

**Size.** medium — the index needs package names and versions extracted from uploaded
distributions.

Behavior:

- Files: `authors/id/<A>/<AU>/<AUTHOR>/<Dist>-<version>.tar.gz`; a `CHECKSUMS` file per author
  directory (a PGP-signed Perl data structure with `md5`, `sha256`, `size`, `mtime`, `cpan_path`
  and, for compressed files, `md5-ungz` and `sha256-ungz`); `modules/02packages.details.txt.gz`;
  `authors/01mailrc.txt.gz`; `modules/03modlist.data.gz`.
- `02packages.details.txt` starts with header lines `File:`, `URL:`, `Description:`,
  `Columns: package name, version, path`, `Intended-For:`, `Written-By:`, `Line-Count:`,
  `Last-Updated:`, then a blank line, then one line per package: name, version (or `undef`), path
  relative to `authors/id/`.
- Remote: the index files are mutable; distributions are immutable.
- Local: an upload adds the distribution and its packages to the index. The public index assigns
  package names to authors; a private index must decide who may claim a package name (BEYOND-028).
- Virtual: when two members index the same package name, one line must win; member order is the
  natural rule.

| Setting | Meaning | Default |
|---|---|---|
| Upstream mirror | Remote upstream | unspecified |
| Author directory for uploads | `AUTHOR` used when a deployer has no CPAN identity | unspecified |
| Sign CHECKSUMS | Produce PGP-signed `CHECKSUMS` files | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch file | path | file | read |
| Upload distribution | tarball, author | stored, index regenerated | deploy |
| Recalculate index | repository | regenerated index files | manage |

UI: repository browser by author and distribution; search by package name.

Limits and edge cases: package-version extraction follows Perl conventions that are not restated
here (unspecified); an index line for a package whose distribution was deleted must be removed.

Interactions: BEYOND-028; search in [search-and-query.md](search-and-query.md).

### BEYOND-010 · JSR (JavaScript Registry)
Priority: **P2**

**What it is.** A registry for JavaScript and TypeScript modules with two client surfaces: a native
API used by runtimes with `jsr:` specifiers (Deno), and an npm compatibility layer that serves
JSR packages as npm packages under the `@jsr` scope.

**Specification.** [JSR API](https://jsr.io/docs/api), [npm compatibility](https://jsr.io/docs/npm-compatibility).

**Clients.** Deno (native API; registry URL from `JSR_URL`, default `https://jsr.io/`; Deno
derives the management API URL by replacing the path of that URL with `/api/`); npm, pnpm and Yarn
through the `@jsr` scope.

**Why it matters for trove.** JSR packages appear in npm dependency trees (MVP format); the
compatibility layer is reachable with existing npm support, the native API is not.

**Size.** small for the npm compatibility layer (an npm remote with a scoped client line); medium
for a native remote; large for hosting JSR packages, which requires computing module graphs at
publish time.

Behavior:

- npm compatibility: upstream `https://npm.jsr.io`; package `jsr:@<scope>/<name>` is npm package
  `@jsr/<scope>__<name>`. Clients add `@jsr:registry=<url>` to `.npmrc`. Package documents carry
  `name`, `description`, `repository`, `dist-tags` (`latest`), `versions` and `time`; yanked
  versions are omitted from `versions`, but their tarballs stay downloadable. The upstream may lag
  the native API by minutes.
- npm-layer tarballs are generated by the registry and can be regenerated for an unchanged
  version (a new "revision" with a new tarball URL and new checksums). Each tarball URL stays
  immutable, so lockfiles keep working, but the version entry in the package document is
  mutable: a remote must cache package documents with a metadata cache period and must never
  treat a version's `dist` as fixed.
- Native API: `GET /@<scope>/<name>/meta.json` (scope, name, `versions` map with `yanked` flags,
  optional `githubRepository`); `GET /@<scope>/<name>/<version>_meta.json` (file `manifest` with
  `size` and `sha256-<hex>` checksum per path, `moduleGraph1` or `moduleGraph2`, `exports`;
  immutable); `GET /@<scope>/<name>/<version>/<path>` (module files; immutable; conditional
  requests answer `304`).
- Requests must not accept `text/html` and must not send `Sec-Fetch-Dest: document`; otherwise the
  registry may return a rendered HTML page.
- Publishing uses a separate management API (OpenAPI at `/.well-known/openapi` of the API host)
  with `Authorization: Bearer` tokens or `Authorization: githuboidc <token>` from GitHub Actions.

| Setting | Meaning | Default |
|---|---|---|
| Upstream (npm layer) | npm remote upstream | `https://npm.jsr.io` (JSR's endpoint) |
| Upstream (native) | Native remote upstream | `https://jsr.io/` (Deno's default) |
| Package metadata cache period | Cache period of `meta.json` | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Package metadata | scope, name | `meta.json` | read |
| Version metadata | scope, name, version | `<version>_meta.json` | read |
| Module file | scope, name, version, path | file | read |

UI: npm repository browser for the `@jsr` scope; native repositories browse by scope and package.

Limits and edge cases: deleted versions disappear from package metadata and their files are
removed upstream; a cache keeps serving them unless invalidated (RMT-026 in
[remote-repositories.md](remote-repositories.md)).

Interactions: PKG-NPM-038 in [formats/npm.md](formats/npm.md); BEYOND-028 for the `@jsr` scope.

### BEYOND-011 · Hosted editor extensions with the Open VSX API
Priority: **P2**

**What it is.** Local and virtual repositories for VS Code compatible editor extensions (`.vsix`),
with the Open VSX publish API and the VS Code compatible gallery endpoints. The baseline proxies
public galleries only.

**Specification.** Open VSX server
[RegistryAPI](https://github.com/eclipse/openvsx/blob/master/server/src/main/java/org/eclipse/openvsx/RegistryAPI.java)
and [VSCodeAPI](https://github.com/eclipse/openvsx/blob/master/server/src/main/java/org/eclipse/openvsx/adapter/VSCodeAPI.java);
publishing CLI [ovsx](https://github.com/eclipse/openvsx/tree/master/cli).

**Clients.** Editors configured with a gallery service URL; `ovsx publish -r <registry URL> -p
<token>` (default registry `https://open-vsx.org`).

**Why it matters for trove.** Organizations build internal extensions and want to vet public
ones in one place; editor extensions run with the user's rights.

**Size.** medium — `.vsix` parsing, the native API, and the gallery query protocol.

Behavior:

- Publish: `POST /api/-/publish` with the `.vsix` as `application/octet-stream`; `201` with
  `Location` of the extension metadata; `400` (not publishable), `401` (token missing, invalid or
  expired), `403` (no publish right in the namespace).
- Other native operations: `POST /api/-/namespace/create`, `GET /api/{namespace}/verify-pat`,
  `POST /api/{namespace}/{extension}/delete`, `GET /api/{namespace}/{extension}` with optional
  target platform and version, `GET /api/{namespace}/{extension}/versions`, `GET /api/-/search`,
  `GET`/`POST /api/-/query`, `GET /api/v2/-/query`, `GET /api/version`.
- Gallery endpoints for editors: `GET`/`POST /vscode/gallery/extensionquery`,
  `GET /vscode/asset/{namespace}/{extension}/{version}/{assetType}/**`, `GET /vscode/item`,
  `GET /vscode/gallery/publishers/{namespace}/vsextensions/{extension}/{version}/vspackage`,
  `GET /vscode/unpkg/{namespace}/{extension}/{version}/**`,
  `GET /vscode/gallery/{namespace}/{extension}/latest`.
- Token transport: the current CLI calls `GET /api/version` and sends the token only as
  `Authorization: Bearer` when the reported `version` is 1.3.0 or later; otherwise, or when the
  endpoint is missing, it also sends the token as a `token` query parameter. When Basic credentials
  for a fronting proxy are configured, the token travels in `X-OpenVSX-Token` instead.
- The version document carries `version`, `maxExtensionSize` and optionally
  `trustedPublishingAudience`; the CLI's own publish size limit is 512 MiB unless the registry
  reports another.
- CLI publishing options include target platforms (`-t`), `--pre-release`, `--skip-duplicate`,
  and trusted publishing (`--trusted-publishing`, `--idToken` for CI systems such as GitLab that
  provide the token directly, `--oidcAudience`, default audience the registry URL).

| Setting | Meaning | Default |
|---|---|---|
| Namespace ownership | Which principals may publish in a namespace | unspecified |
| Maximum extension size | Reported in `/api/version` and enforced | unspecified |
| Accept trusted publishing | OIDC exchange for publish tokens | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Publish | `.vsix` | extension metadata, `201` | deploy in the namespace |
| Create namespace | name | namespace | manage |
| Delete extension or version | namespace, extension, versions | deleted | delete |
| Gallery query | query document | extension list | read |
| Download asset | namespace, extension, version, asset type | file | read |

UI: repository browser by namespace and extension, with target platforms and pre-release marks.

Limits and edge cases: access logs must drop the `token` query parameter; trove should answer
`/api/version` with a version of 1.3.0 or later so the CLI never needs it.

Interactions: PKG-AI-018 and PKG-AI-021 in [formats/ai-and-ide.md](formats/ai-and-ide.md);
CIOIDC-018 in [oidc-ci-integration.md](oidc-ci-integration.md) for trusted publishing.

### BEYOND-012 · WebAssembly components on OCI
Priority: **P2**

**What it is.** The CNCF layout for WebAssembly modules and components as OCI artifacts, and the
package-name mapping that the `wkg` tool uses to find components in OCI registries.

**Specification.** [Wasm OCI artifact layout](https://tag-runtime.cncf.io/wgs/wasm/deliverables/wasm-oci-artifact/);
wasm-pkg-tools [registry metadata](https://github.com/bytecodealliance/wasm-pkg-tools/blob/main/docs/registry-metadata.md).

**Clients.** `wkg` (`wkg get <namespace>:<name>@<version>`, `wkg publish`, `wkg oci pull`), Wasm
runtimes that pull from OCI registries.

**Why it matters for trove.** Component packages (WIT interfaces and libraries) are distributed
only through OCI registries; trove can serve them once it recognizes the layout and answers the
discovery document.

**Size.** small — OCI storage exists; the work is config parsing, a validation rule and one
well-known document.

Behavior:

- Manifest: OCI image manifest; config media type `application/vnd.wasm.config.v0+json`; exactly
  one layer of media type `application/wasm` (consumers reject more than one).
- Config fields: `created`, optional `author`, `architecture` = `wasm`, `os` (`wasip1` or `wasip2`;
  `wasip1` for plain modules), `layerDigests`, and `component` (required for components) with
  `exports`, `imports` (WIT identifiers such as `wasi:http/incoming-handler@0.2.0`) and optional
  `target`.
- Discovery: for a package registry domain `example.com`, `wkg` reads
  `https://example.com/.well-known/wasm-pkg/registry.json`:
  `{"preferredProtocol": "oci", "oci": {"registry": "<host>", "namespacePrefix": "<prefix>"}}`
  (deprecated form: `ociRegistry`, `ociNamespacePrefix`). Package `acme:foo@0.1.0` maps to
  `<host>/<prefix>acme/foo:0.1.0`. Tags must be valid semantic versions or the tool ignores them.

| Setting | Meaning | Default |
|---|---|---|
| Serve registry metadata | Answer the well-known document for a host name | unspecified |
| Target repository and namespace prefix | Values written into the document | unspecified (prefix default `""` per the format) |
| Reject multi-layer Wasm artifacts | Validation on push | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Registry metadata | none | `registry.json` | anonymous |
| Push and pull | OCI requests | manifests and blobs | deploy, read |
| Search by export or import | WIT identifier | matching components | read |

UI: OCI image view shows exports and imports for Wasm components.

Limits and edge cases: the config media type is versioned `v0`, which per its specification does
not imply instability.

Interactions: PKG-OCI-016 in [formats/oci.md](formats/oci.md); BEYOND-017.

### BEYOND-013 · Gentoo binary package hosts
Priority: **P2**

**What it is.** Binary package repositories ("binhosts") for Portage: a directory with a
`Packages` index and binary packages in the GPKG container format (or the older XPAK format).

**Specification.** Gentoo [Binary package guide](https://wiki.gentoo.org/wiki/Binary_package_guide);
[GLEP 78](https://www.gentoo.org/glep/glep-0078.html) (GPKG container format).

**Clients.** emerge (`--getbinpkg`/`-g`, `--getbinpkgonly`/`-G`, or `FEATURES="getbinpkg"`),
configured in `/etc/portage/binrepos.conf` (`sync-uri` pointing at the directory that holds
`Packages`, optional `priority`, `verify-signature` (from portage-3.0.74), `location` (the local
cache directory, default `/var/cache/binhost/<name>` from portage-3.0.77)). `PORTAGE_BINHOST` is
deprecated.

**Why it matters for trove.** Gentoo fleets build once and install everywhere; a binhost with
access control and caching replaces ad-hoc web directories.

**Size.** medium — the `Packages` index is generated from metadata inside uploaded packages.

Behavior:

- `Packages` starts with a header block of `KEY: value` lines (observed keys include
  `ACCEPT_KEYWORDS`, `ARCH`, `CHOST`, `FEATURES`, `PACKAGES` (count), `PROFILE`, `TIMESTAMP`, `TTL`,
  `USE`), then one block per package separated by blank lines (observed keys include `BUILD_ID`,
  `BUILD_TIME`, `CPV`, `DEFINED_PHASES`, `EAPI`, `KEYWORDS`, `MD5`, `PATH`, `RDEPEND`, `SHA1`,
  `SIZE`, `USE`, `MTIME`, `REPO`). `PATH` is the package file relative to the binhost, for example
  `<category>/<package>/<package>-<version>-<build id>.gpkg.tar`.
- GPKG: an uncompressed tar named `.gpkg.tar` whose members start with the format identifier
  `gpkg-1`, followed by `metadata.tar` and `image.tar` (each optionally compressed) and optional
  OpenPGP signatures. OpenPGP signing and verification exist only for GPKG.
- Signature verification is on by default for remote binhosts in current Portage (Gentoo news item
  of 2026-05-03, [Portage binpkg changes](https://www.gentoo.org/support/news-items/2026-05-03-portage-binpkg-changes.html)):
  a fetched package without a valid signature from a key the client trusts fails. A private
  binhost therefore either serves packages signed by its builders (signing happens at build time,
  `FEATURES="binpkg-signing"`, or afterwards with Portage's `gpkg-sign`), or every client sets
  `verify-signature = false` for it. The signature lives inside the `.gpkg.tar`, so the server
  cannot add one without changing the package file and its index checksums.
- Since portage-3.0.52, Portage trusts the `Packages` index of a package directory by default
  (`FEATURES=pkgdir-index-trusted`, for performance), so the index must match the files exactly.
- Remote: `Packages` is mutable (its `TTL` header field gives a refresh hint); package files are
  immutable.

| Setting | Meaning | Default |
|---|---|---|
| Upstream binhost | Remote upstream directory | unspecified |
| Require signed packages | Refuse GPKG uploads without valid signatures from trusted keys, so that clients with default verification never fetch a package they will reject | unspecified |
| Compressed index | Also serve a compressed index | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch | `Packages` or package path | file | read |
| Upload | `.gpkg.tar` | stored, `Packages` regenerated | deploy |
| Recalculate index | repository | regenerated `Packages` | manage |

UI: repository browser by category and package; per-package USE flags and build ID.

Limits and edge cases: several builds of one version coexist, distinguished by `BUILD_ID`; the
complete per-package key set is defined by Portage and not restated here (unspecified). XPAK
packages cannot carry signatures, so a binhost that still serves XPAK only works for clients that
disabled verification.

Interactions: IAM-049 in [identity-and-access.md](identity-and-access.md) for trusted keys;
RMT-018 in [remote-repositories.md](remote-repositories.md).

### BEYOND-014 · FreeBSD pkg repositories
Priority: **P2**

**What it is.** Binary package repositories for FreeBSD's `pkg`: package files plus a signed
catalogue created by `pkg repo`.

**Specification.** [pkg-repo(8)](https://man.freebsd.org/cgi/man.cgi?query=pkg-repo&sektion=8),
[pkg-repository(5)](https://man.freebsd.org/cgi/man.cgi?query=pkg-repository&sektion=5),
[pkg.conf(5)](https://man.freebsd.org/cgi/man.cgi?query=pkg.conf&sektion=5),
[fetch(3)](https://man.freebsd.org/cgi/man.cgi?query=fetch&sektion=3).

**Clients.** `pkg install`, `pkg update`, configured by UCL files in `/usr/local/etc/pkg/repos/`.

**Why it matters for trove.** FreeBSD appliances and build farms need a mirror and internal
packages under the same access control as other formats.

**Size.** medium — catalogue generation from package manifests and catalogue signing.

Behavior:

- Repository format version 2: `meta.conf` (UCL; fields include `version`, `packing_format`,
  `manifests`, `manifests_archive`, `data` (default `data`), `data_archive` (default `data`),
  `filesite`, `filesite_archive`), `data.pkg` (primary catalogue: package manifests, groups,
  expired packages), `packagesite.pkg` (deprecated, one compact JSON manifest per line, still
  generated), optional `files.pkg`, and the `.pkg` files.
- Signing: signer types `rsa` (signs the SHA-256 of the catalogue), `ecdsa` and `eddsa` (sign the
  BLAKE2 hash); external signing commands are supported. Clients set `SIGNATURE_TYPE` to `NONE`
  (default), `PUBKEY` (with `PUBKEY` path) or `FINGERPRINTS` (with a fingerprints directory).
- Client repository options: `URL`, `ENABLED` (default YES), `MIRROR_TYPE` (`NONE` default, `HTTP`
  or `SRV`; SRV requires `pkg+http://` or `pkg+https://`), `SIGNATURE_TYPE`, `PUBKEY`,
  `FINGERPRINTS`, `PRIORITY` (default 0, higher preferred), `IP_VERSION`, `SSH_ARGS`, and `ENV`
  (environment passed to the fetch library, which reads `HTTP_AUTH` as
  `basic:<realm>:<user>:<password>` for Basic credentials). URLs may contain `${ABI}`.
- Remote: upstream mirrors resolved through SRV are fetched by trove; clients see one HTTPS URL.

| Setting | Meaning | Default |
|---|---|---|
| ABI subtrees | ABI strings served under the repository | unspecified |
| Catalogue signing key and type | `rsa`, `ecdsa` or `eddsa` | unspecified |
| Generate file list | Produce `files.pkg` | unspecified (`pkg repo` produces it only with `-l`) |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch | catalogue or package path | file | read |
| Upload | `.pkg` file, ABI | stored, catalogue regenerated and signed | deploy |
| Recalculate catalogue | repository, ABI | regenerated catalogue | manage |

UI: repository browser by ABI and package origin.

Limits and edge cases: credentials travel through the fetch library's environment; trove tokens
work as the Basic password.

Interactions: IAM-046 in [identity-and-access.md](identity-and-access.md); RMT-018 in
[remote-repositories.md](remote-repositories.md).

## OCI artifacts and referrers

### BEYOND-015 · Referrers through remote and virtual repositories
Priority: **P1**

**What it is.** The referrers listing (signatures, SBOMs, attestations attached to an image) served
by remote and virtual OCI repositories, not only by local ones.

**Specification.** [OCI distribution specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md),
sections *Listing Referrers*, *Referrers Tag Schema* and *Enabling the Referrers API*;
[cosign bundle specification](https://github.com/sigstore/cosign/blob/main/specs/BUNDLE_SPEC.md).

**Clients.** cosign, notation, ORAS and other OCI 1.1 tools. From cosign 3.0 the Sigstore bundle
format and container signatures stored as OCI 1.1 referring artifacts are on by default
([cosign changelog](https://github.com/sigstore/cosign/blob/main/CHANGELOG.md), v3.0.0 and v2.6.0
sections); the older tag-based format remains available behind a flag.

**Why it matters for trove.** Verifying a signature of an image pulled through a proxy fails
unless the proxy also answers the referrers query; this blocks signature checks on every proxied
image.

**Size.** medium — a cached upstream query with tag-schema fallback, and a merge for virtual
repositories.

Behavior:

- Remote: a referrers request for a subject digest queries the upstream referrers endpoint. If the
  upstream answers 404 (no referrers API), the remote reads the upstream tag
  `<algorithm>-<encoded digest>` (truncated to 32 and 64 characters, disallowed characters replaced
  by `-`) and, if it is a valid image index, uses it. Otherwise the answer is an empty index; the
  endpoint itself never answers 404 for an existing repository.
- The list is mutable and cached with a metadata cache period; the referring manifests and blobs
  are immutable and fetched on demand through the same remote.
- If the upstream did not apply an `artifactType` filter (no `OCI-Filters-Applied` header), the
  remote filters and sets the header itself. Upstream pages (`Link: rel="next"`) are followed before
  caching.
- Virtual: the union of the members' lists for the subject digest, de-duplicated by digest, then
  filtered and paginated. Each descriptor keeps `artifactType` (or the config media type) and the
  annotations, as the specification requires.
- Legacy signature tags (`sha256-<hex>.sig` and similar) are ordinary tags and follow tag caching.

| Setting | Meaning | Default |
|---|---|---|
| Proxy referrers | Answer referrers requests from the upstream | unspecified |
| Referrers cache period | How long a fetched list is reused | unspecified |
| Tag-schema fallback | Read the upstream fallback tag when the upstream lacks the API | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| List referrers | repository name, subject digest, optional `artifactType` | image index | read |

UI: image view in remote and virtual repositories shows attached referrers with their types.

Limits and edge cases: an empty list must be cached only briefly (or not negatively cached at
all), otherwise signatures attached upstream after the first pull stay invisible. The subject
itself need not be cached for its referrers to be listed.

Interactions: PKG-OCI-009 to PKG-OCI-011 in [formats/oci.md](formats/oci.md); RMT-018 and RMT-020
in [remote-repositories.md](remote-repositories.md); BEYOND-019.

### BEYOND-016 · Referrer-aware lifecycle operations
Priority: **P2**

**What it is.** Rules that keep an image and the artifacts referring to it together through copy,
promotion, deletion, retention and garbage collection. The baseline leaves this unspecified.

**Specification.** OCI image specification [`subject` field](https://github.com/opencontainers/image-spec/blob/main/manifest.md)
(a weak association); distribution specification *Deleting Manifests*.

**Clients.** Any OCI 1.1 tool that attaches artifacts.

**Why it matters for trove.** Referrers are normally untagged manifests; a cleanup that deletes
untagged manifests silently removes every signature and SBOM, and a promotion that copies only the
image leaves verification failing in the target repository.

**Size.** medium — a graph walk over `subject` links in the metadata store, used by several
existing operations.

Behavior:

- Copy and promotion of a subject include its referrers transitively (a signature on an SBOM is a
  referrer of the SBOM).
- Delete of a subject: either cascade to its referrers or keep them as orphans; the specification
  requires registries to accept a referrer before its subject exists, so orphans are valid.
- Retention: referrers count as part of their subject and are not "untagged manifests"; referrers
  tag-schema indexes (`<algorithm>-<hex>` tags) are excluded from tag-count limits.
- Garbage collection: a referrer stays reachable while its subject exists; after the subject is
  gone, it becomes collectable after the grace period, unless orphans are kept.

| Setting | Meaning | Default |
|---|---|---|
| Cascade delete | Delete referrers with their subject | unspecified |
| Carry referrers on copy and promotion | Include the referrer graph | unspecified |
| Protect referrers from untagged cleanup | Treat referrers as part of the subject | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Referrer graph | repository, subject digest | tree of referrers with types | read |
| Delete with cascade | repository, subject digest | deleted manifests | delete |

UI: image view, delete dialog states how many referrers are affected.

Limits and edge cases: a referrer may reference a subject in the same repository only; cross-
repository subjects are not defined.

Interactions: PKG-OCI-012 in [formats/oci.md](formats/oci.md); ART-032 to ART-034 in
[artifacts.md](artifacts.md); RET-010 and RET-035 in [retention-and-cleanup.md](retention-and-cleanup.md);
STOR-036 in [storage-and-blobs.md](storage-and-blobs.md).

### BEYOND-017 · Artifact-type recognition
Priority: **P2**

**What it is.** Classifying stored OCI manifests by artifact type, so the UI, search and policies
can tell an image from a signature, an SBOM, a Helm chart or a Wasm component.

**Specification.** OCI image specification, *Guidelines for Artifact Usage* and *Guidance for an
Empty Descriptor*; IANA media type registrations; the cosign specifications.

**Clients.** All OCI clients; the classification is server-side.

**Why it matters for trove.** Without it, every artifact looks like an image, scanners try to
scan signatures, and users cannot find SBOMs.

**Size.** small — derived from fields already stored.

Behavior: the type is the manifest's `artifactType`, else the config media type. An artifact with
the empty config (`application/vnd.oci.empty.v1+json`, digest
`sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a`, size 2) must carry
`artifactType`. Unknown types must be stored and copied without error.

| Type value | Meaning |
|---|---|
| `application/vnd.dev.sigstore.bundle.v0.3+json` | Sigstore bundle; annotation `dev.sigstore.bundle.content` = `message-signature` or `dsse-envelope`, optional `dev.sigstore.bundle.predicateType` |
| `application/vnd.dev.cosign.simplesigning.v1+json` | Layer media type of the older tag-based cosign signature |
| `application/vnd.cncf.notary.signature` | Notary Project signature (config media type; the single layer is the envelope, `application/jose+json` or `application/cose`), BEYOND-034 |
| `application/vnd.cyclonedx+json` | CycloneDX SBOM (IANA, optional `version` parameter) |
| `application/spdx+json` | SPDX SBOM (IANA) |
| `application/vnd.cncf.helm.config.v1+json` | Helm chart (config media type) |
| `application/vnd.wasm.config.v0+json` | Wasm module or component (config media type) |

| Setting | Meaning | Default |
|---|---|---|
| Additional type labels | Operator-defined display names for further types | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Search by artifact type | type value | matching manifests | read |

UI: image list shows a type badge; filters by type.

Limits and edge cases: older artifacts lack `artifactType`; the config media type is the fallback
the specification names.

Interactions: PKG-OCI-008 in [formats/oci.md](formats/oci.md); BEYOND-012, BEYOND-024.

### BEYOND-018 · Upstream selection by source namespace
Priority: **P2**

**What it is.** A proxying registry may receive the client's original registry host in an `ns`
query parameter on pull requests and use it to choose the upstream.

**Specification.** OCI distribution specification, section *Registry Proxying*, present in the
development branch after release 1.1.1 and not part of a release as of 2026-10-28.

**Clients.** Clients configured to use a registry mirror; which clients send `ns` was not verified
for this entry.

**Why it matters for trove.** One virtual container repository can then act as the mirror for
several public registries, routing each pull to the remote whose upstream matches the original
host.

**Size.** small — a routing rule in virtual resolution.

Behavior: when `ns` is present and honored, the virtual repository resolves only through the
remote member whose upstream host equals `ns` and answers with `OCI-Namespace: <ns>`. Without `ns`,
normal resolution order applies. Credentials meant for the upstream registry are not accepted from
clients unless explicitly configured.

| Setting | Meaning | Default |
|---|---|---|
| Honor source namespace | Route by `ns` | unspecified |
| Host-to-member mapping | Explicit mapping where upstream hosts differ from `ns` values | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Pull with `ns` | repository name, reference, `ns` | manifest or blob | read |

UI: virtual repository settings, advanced section.

Limits and edge cases: an `ns` value without a matching member falls back to normal resolution or
is refused (choice unspecified).

Interactions: PKG-OCI-003 and PKG-OCI-004 in [formats/oci.md](formats/oci.md); REPO-035 in
[repositories.md](repositories.md).

## Signatures, provenance and attestations

### BEYOND-019 · Signature verification gate for Sigstore-signed content
Priority: **P2**

**What it is.** Verifying Sigstore signatures and bundles when content enters trove (remote
cache fill, deploy) or leaves it (download), against a policy per repository: accept, record, or
refuse.

**Specification.** [cosign signature specification](https://github.com/sigstore/cosign/blob/main/specs/SIGNATURE_SPEC.md),
[cosign bundle specification](https://github.com/sigstore/cosign/blob/main/specs/BUNDLE_SPEC.md),
[Sigstore client specification](https://github.com/sigstore/architecture-docs/blob/main/client-spec.md),
[TUF specification](https://theupdateframework.github.io/specification/latest/) for trust-root
distribution.

**Clients.** Producers: cosign (OCI), npm (`--provenance`), PyPI publishers (PEP 740).
Verification happens in trove.

**Why it matters for trove.** A pull-through proxy is the one place where all external content
passes; checking signatures there stops tampered or unsigned content before it reaches builds.

**Size.** large — verification libraries exist (for example sigstore-go), but the policy model,
trust-root refresh, per-format discovery and a verification cache are new.

Behavior:

- Discovery per format: OCI referrers with the Sigstore bundle type (BEYOND-015), or the older
  tag `sha256-<hex>.sig` whose layers carry the simple-signing payload and annotations
  `dev.cosignproject.cosign/signature` plus certificate and chain (the specification text names
  the `dev.cosignproject.cosign/` prefix for them, its examples use `dev.sigstore.cosign/`; both
  must be read); npm `dist.attestations.url` (BEYOND-021); PyPI `provenance` (BEYOND-022).
- Policy: trust roots (Sigstore public-good obtained through TUF, a private Sigstore deployment, or
  static public keys); accepted identities as pairs of certificate identity and OIDC issuer, exact
  or pattern; transparency-log inclusion and signed-time requirements; mode off, audit or enforce;
  scope by repository and package pattern.
- Keyless certificates are short-lived; validity is checked at signing time from the transparency
  log's integrated time or an RFC 3161 timestamp.
- Results are cached per digest and policy version and recomputed when the policy changes.
- In enforce mode a failing artifact is not cached and not served; in audit mode it is served and
  an audit event is written.

| Setting | Meaning | Default |
|---|---|---|
| Verification mode | off, audit, enforce | unspecified |
| Trust roots | Public-good, private, or keys | unspecified |
| Accepted identities | Identity and issuer pairs | unspecified |
| Require transparency log entry | Reject signatures without inclusion proof | unspecified |
| Trust-root refresh | TUF update interval; offline mirror for air-gapped sites | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Verify on demand | repository, artifact | result with identities and reasons | read |
| Manage policy | policy document | stored policy | manage |

UI: repository settings, security tab; artifact view shows verification status.

Limits and edge cases: tag-based signatures are racy to update (the specification says so);
multiple signatures in one manifest must each be evaluated.

Interactions: SEC-019 and SEC-021 in [security-scanning-and-curation.md](security-scanning-and-curation.md);
IAM-049 in [identity-and-access.md](identity-and-access.md); DIST-046 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md); BEYOND-020, BEYOND-025; X.509-based
Notary Project signatures in BEYOND-034.

### BEYOND-020 · SLSA provenance verification policy
Priority: **P2**

**What it is.** Checking SLSA build provenance attached to an artifact against expectations (which
builder, which build type, which source) before deploy, promotion or download.

**Specification.** [SLSA build provenance](https://slsa.dev/spec/v1.2/build-provenance), version
1.2, the current one (predicate type `https://slsa.dev/provenance/v1`, which always resolves to
the latest minor version; the v1.1 provenance page is marked retired); [in-toto Statement v1](https://github.com/in-toto/attestation/blob/main/spec/v1/statement.md);
[in-toto Bundle](https://github.com/in-toto/attestation/blob/main/spec/v1/bundle.md).

**Clients.** Builders that emit provenance (CI systems, cosign attestations, npm, PyPI).

**Why it matters for trove.** A signature only proves who signed; provenance states how and from
what the artifact was built, which is what a promotion gate needs.

**Size.** medium — parsing and policy evaluation on top of BEYOND-019.

Behavior:

- Statement: `_type` `https://in-toto.io/Statement/v1`, `subject` (each with `digest`; matched
  purely by digest), `predicateType`, `predicate`.
- Predicate: `buildDefinition` (`buildType`, `externalParameters`, `internalParameters`,
  `resolvedDependencies`) and `runDetails` (`builder.id`, `builder.version`,
  `builder.builderDependencies`, `metadata.invocationId`, `metadata.startedOn`,
  `metadata.finishedOn`, `byproducts`). Required for Build L1: `buildDefinition`, `runDetails`,
  `buildType`, `externalParameters`, `builder`, `builder.id`.
- Consumers accept only specific signer and builder pairs, treat `externalParameters` as untrusted
  input to be checked against expectations, and should reject unexpected fields in it.
- Sources of provenance: OCI referrers (DSSE bundle with a predicate-type annotation), npm
  attestations, PyPI attestations, and in-toto bundle files `<file>.intoto.jsonl` (JSON Lines of
  envelopes, media type `application/vnd.in-toto.bundle`; consumers ignore unrecognized lines and
  must not depend on order).

| Setting | Meaning | Default |
|---|---|---|
| Expected builder IDs | Accepted `builder.id` values per signer identity | unspecified |
| Expected build types | Accepted `buildType` values | unspecified |
| Source expectations | Required values in `externalParameters` per build type | unspecified |
| Enforcement points | deploy, promotion, download | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Evaluate provenance | artifact | verdict, matched builder, reasons | read |

UI: artifact view shows builder, source and verdict.

Limits and edge cases: the meaning of `externalParameters` depends on `buildType`; a policy must
be written per build type.

Interactions: DIST-044 to DIST-047 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md);
BEYOND-019, BEYOND-021, BEYOND-022.

### BEYOND-021 · npm provenance for locally published packages
Priority: **P1**

**What it is.** Accepting, storing and serving the Sigstore provenance bundle that npm attaches to
a publish, so that `npm audit signatures` and other verifiers see provenance for internal packages.

**Specification.** npm [provenance statements](https://docs.npmjs.com/generating-provenance-statements);
npm CLI publish code [libnpmpublish](https://github.com/npm/cli/blob/latest/workspaces/libnpmpublish/lib/publish.js);
attestation listing as served by the public registry.

**Clients.** npm (`npm publish --provenance` or `--provenance-file <path>`), `npm audit signatures`.

**Why it matters for trove.** npm generates provenance in GitLab CI (the owner's CI). A registry
that accepts only one publish attachment rejects such publishes; the MVP publish handler must at
least accept the second attachment, and storing it is the next step.

**Size.** medium — an extra attachment on publish, verification, an attestation endpoint, and a
field in version metadata.

Behavior:

- The publish document's `_attachments` holds the tarball (`<name>-<version>.tgz`,
  `application/octet-stream`, base64 `data`, `length`) and the bundle
  (`<name>-<version>.sigstore`, `content_type` = the bundle's media type, `data` = the bundle JSON as
  a string, not base64, `length`).
- The bundle's subject is the package URL `pkg:npm/<name>@<version>` with the SHA-512 of the tarball
  (hex). Server-side verification compares both with the published tarball.
- npm generates provenance automatically only in GitHub Actions (requires `id-token: write`) and
  GitLab CI (requires an ID token named `SIGSTORE_ID_TOKEN` with audience `sigstore`); elsewhere a
  pre-built bundle is passed with `--provenance-file`, which npm verifies locally.
- Before generating provenance (`--provenance`) without `--access public`, npm calls
  `GET /-/package/<escaped name>/visibility`; a 404 or `{"public": false}` makes npm refuse with
  "Can't generate provenance for new or private package, you must set `access` to public.", and any
  other error status aborts the publish.
- Serving: the version manifest's `dist.attestations` is `{"url": <attestations URL>,
  "provenance": {"predicateType": <type>}}`; `GET /-/npm/v1/attestations/<name>@<version>` returns
  `{"attestations": [{"predicateType": ..., "bundle": {...}}]}`. The public registry returns a
  SLSA provenance attestation and a publish attestation (`https://github.com/npm/attestation/tree/main/specs/publish/v0.1`)
  signed by the registry.
- `npm audit signatures` loads registry keys as the TUF target `<host><path>/keys.json` from the
  Sigstore TUF repository and falls back to `GET /-/npm/v1/keys`.

| Setting | Meaning | Default |
|---|---|---|
| Accept provenance attachments | Store the bundle instead of ignoring it | unspecified |
| Verify on publish | off, warn, enforce | unspecified |
| Visibility answer | What `/-/package/<name>/visibility` reports | unspecified |
| Issue publish attestations | Sign a registry publish attestation with trove's key | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Publish with provenance | publish document with two attachments | stored version and bundle | deploy |
| List attestations | name, version | attestation list | read |
| Package visibility | name | `{"public": …}` | read |

UI: package version view shows provenance (source repository, workflow, transparency-log entry).

Limits and edge cases: answering `public: true` for internal packages only satisfies the client
check; it does not make them public. Remote and virtual repositories keep passing upstream
attestation links through (PKG-NPM-024).

Interactions: PKG-NPM-013, PKG-NPM-015, PKG-NPM-024 and PKG-NPM-037 in
[formats/npm.md](formats/npm.md); CIOIDC-030 in [oidc-ci-integration.md](oidc-ci-integration.md);
BEYOND-019, BEYOND-020.

### BEYOND-022 · PyPI digital attestations (PEP 740)
Priority: **P2**

**What it is.** Uploading signed attestations with Python distributions and serving them as
provenance objects through the simple repository API.

**Specification.** [PEP 740](https://peps.python.org/pep-0740/) (Final), canonical text in the
[index-hosted attestations specification](https://packaging.python.org/en/latest/specifications/index-hosted-attestations/);
PyPI [Integrity API](https://docs.pypi.org/api/integrity/).

**Clients.** Twine (`twine upload --attestations`), publishing actions that produce attestations,
installers and auditing tools that read `provenance`.

**Why it matters for trove.** PyPI is P1; internal Python packages published from CI can carry
the same verifiable provenance as public ones.

**Size.** medium — upload-time verification, storage of mutable provenance objects, and two index
extensions.

Behavior:

- Upload: the multipart form gains an `attestations` field, a JSON array of one or more attestation
  objects. The index must verify each and reject the upload if any fails.
- Attestation object version 1: `version`, `verification_material` (`certificate` as base64 DER,
  `transparency_entries`), `envelope` (`statement`, base64 in-toto v1 Statement; `signature`,
  base64). Suite: X.509 per RFC 5280, ECDSA P-256 with SHA-256; DSSE v1 with payload type
  `application/vnd.in-toto+json` only.
- Statement rules: exactly one subject; `subject[0].name` is the distribution file name (compared
  after parsing, since it may be normalized); `subject[0].digest` contains `sha256` in hex;
  `predicateType` is `https://slsa.dev/provenance/v1` or `https://docs.pypi.org/attestations/publish/v1`.
- Provenance object: `{"version": 1, "attestation_bundles": [{"publisher": {"kind": ...,
  "claims": {...}, ...}, "attestations": [...]}]}`. It may change later (added attestations).
- Index: HTML file links may carry `data-provenance` (fully qualified, secure-origin URL); JSON file
  entries may carry `provenance` (URL or `null`) and then require `api-version` 1.3 or later.
- Twine attaches files matching `*.*.attestation` whose name starts with the distribution file
  name (for example `<file>.publish.attestation`) and warns, without failing, when the target is not
  PyPI or TestPyPI.
- PyPI's own Integrity API (`GET /integrity/<project>/<version>/<filename>/provenance`, `Accept:
  application/vnd.pypi.integrity.v1+json`; `200`, `403`, `404` no provenance, `406`) is
  index-specific; offering it is optional.

| Setting | Meaning | Default |
|---|---|---|
| Accept attestations | Verify and store uploaded attestations | unspecified |
| Accepted signer identities | Certificate identities valid for this repository | unspecified |
| Publisher kind and claims | How the uploading CI identity is recorded in `publisher` | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Upload with attestations | distribution, attestations | stored file and provenance | deploy |
| Get provenance | file | provenance object | read |

UI: file view in the PyPI browser shows attestations and the publisher.

Limits and edge cases: the publisher object can record the claims of the CI token used for upload
(GitLab claims from the exchange); remote repositories must rewrite upstream provenance URLs to
trove and cache the mutable objects with a metadata cache period.

Interactions: PKG-PYPI-006, PKG-PYPI-012 and PKG-PYPI-028 in [formats/pypi.md](formats/pypi.md);
CIOIDC-018 in [oidc-ci-integration.md](oidc-ci-integration.md); BEYOND-019, BEYOND-020.

### BEYOND-023 · Project status markers in the Python index (PEP 792)
Priority: **P2**

**What it is.** A per-project status in the simple index: active, archived, quarantined or
deprecated, with index and installer semantics.

**Specification.** [PEP 792](https://peps.python.org/pep-0792/) (Final), canonical text in the
[project status markers specification](https://packaging.python.org/en/latest/specifications/project-status-markers/)
and the [simple repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/).

**Clients.** pip, uv and other installers that read index metadata.

**Why it matters for trove.** It gives repository states that trove already needs (read-only
packages, blocked packages) a standard representation that Python clients understand.

**Size.** small — a per-project field and two index extensions.

| Status | Uploads | Downloads offered | Installer behavior |
|---|---|---|---|
| `active` (default) | allowed | yes | none |
| `archived` | refused | yes | may warn |
| `quarantined` | refused | no; the project page lists no files | may warn |
| `deprecated` | allowed | yes | may warn |

Behavior:

- A project has exactly one status; an index may implement a subset.
- API version 1.4. HTML: meta tags `pypi:project-status` (the PEP says should, the PyPA
  specification may; omissible for `active`) and `pypi:project-status-reason` (optional). JSON: a `project-status` object with
  `status` and optional `reason`. (The PEP's text names the key `project-status.state` while its
  example and the PyPA specification's normative text use `status`; one PyPA example places the
  values inside `meta`. The normative PyPA text is the reference.)
- Remote: upstream markers are passed through; for an upstream quarantine, cached files of that
  project should be withheld as well.

| Setting | Meaning | Default |
|---|---|---|
| Project status | One of the four values | `active` |
| Status reason | Free text | unspecified |
| Honor upstream quarantine | Withhold cached files of upstream-quarantined projects | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Set project status | repository, project, status, reason | updated index | manage |

UI: PyPI package view, status selector.

Limits and edge cases: the status is per project, not per version; per-version removal stays with
yanking.

Interactions: PKG-PYPI-028 in [formats/pypi.md](formats/pypi.md); SEC-021 in
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### BEYOND-024 · SBOM attachment store
Priority: **P2**

**What it is.** Storing CycloneDX and SPDX documents supplied by builds as attachments of any
package version, indexing their components by package URL, and answering "which stored artifacts
contain component X".

**Specification.** [CycloneDX](https://cyclonedx.org/specification/overview/) (IANA
`application/vnd.cyclonedx+json`), [SPDX](https://spdx.dev/use/specifications/) (IANA
`application/spdx+json`), [package URL](https://github.com/package-url/purl-spec).

**Clients.** SBOM generators in CI; OCI tools attaching SBOMs as referrers.

**Why it matters for trove.** When a vulnerability is announced, the first question is where an
affected component is used; supplied SBOMs answer it without a scanning engine.

**Size.** medium — a document store, a component index and the attach path for non-OCI formats.

Behavior:

- OCI: SBOMs arrive as referrers (BEYOND-015, BEYOND-017) and are indexed on push.
- Other formats: an attach operation binds an SBOM to repository, package and version.
- Binding check: when the SBOM's described component carries hashes, they are compared with the
  artifact; otherwise the binding is marked unverified.

| Setting | Meaning | Default |
|---|---|---|
| Accepted SBOM formats and versions | Which document types are indexed | unspecified |
| Maximum document size | Upper bound per SBOM | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Attach SBOM | repository, package, version, document | stored, indexed | annotate |
| List and download | artifact | documents | read |
| Component search | package URL, optional version range | artifacts containing it | read |
| Compare | two documents | added, removed, changed components | read |

UI: artifact view, SBOM tab; global component search.

Limits and edge cases: SPDX 3.0 serialization and its media type are unspecified here.

Interactions: SEC-065 to SEC-068 in [security-scanning-and-curation.md](security-scanning-and-curation.md);
DIST-044 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md); BEYOND-016.

### BEYOND-025 · TUF-signed repository metadata
Priority: **Later**

**What it is.** Serving repository metadata signed under The Update Framework, so clients detect
tampering, rollback and freeze attacks even when downloading through mirrors.

**Specification.** [TUF specification](https://theupdateframework.github.io/specification/latest/)
(roles root, targets, snapshot, timestamp, mirrors; files `root.json`, `targets.json`,
`snapshot.json`, `timestamp.json`; consistent snapshots); [PEP 458](https://peps.python.org/pep-0458/)
(Accepted); hackage-security (BEYOND-007).

**Clients.** cabal-install (secure repositories), Sigstore clients (trust root), npm (registry
keys through the Sigstore TUF repository).

**Why it matters for trove.** Local and virtual Hackage repositories need it; air-gapped
signature verification (BEYOND-019) needs a mirrored TUF trust root.

**Size.** large — key custody (an offline root key, online timestamp and snapshot keys), periodic
re-signing, consistent snapshots and expiry monitoring.

Behavior:

- Root delegates keys to the other roles; targets lists files with hashes and sizes (with
  delegations); snapshot lists the versions of targets metadata; timestamp names the current
  snapshot and expires quickly.
- PEP 458 recommends that timestamp, snapshot and the per-bin metadata expire after one day; the
  Hackage file-based tool uses three days for timestamp and snapshot.
- Re-signing runs as a singleton job across replicas.

| Setting | Meaning | Default |
|---|---|---|
| Role keys and thresholds | Per role | unspecified |
| Expiry per role | Validity periods | unspecified |
| Consistent snapshots | Version-prefixed metadata file names | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Fetch metadata | role file name | signed metadata | read |
| Rotate keys | role, new keys, signatures | new root version | administrator |

UI: system settings, signing keys; repository settings show TUF status and expiry.

Limits and edge cases: an expired timestamp blocks every client; monitoring is mandatory.

Interactions: INV-12 in [../spec/README.md](../spec/README.md) (singleton schedulers); IAM-046 in
[identity-and-access.md](identity-and-access.md); BEYOND-007, BEYOND-019.

### BEYOND-026 · Checksum database for hosted Go modules
Priority: **Later**

**What it is.** A Go checksum database (a transparency log of go.sum lines) operated by trove, so
that internal modules get the tamper evidence that public modules get from the public database.

**Specification.** [Go modules reference, checksum database](https://go.dev/ref/mod#checksum-database);
[design document](https://go.googlesource.com/proposal/+/master/design/25530-sumdb.md); Go packages
`golang.org/x/mod/sumdb`, `golang.org/x/mod/sumdb/tlog` and `golang.org/x/mod/sumdb/note`.

**Clients.** The `go` command, through `GOSUMDB="<name>+<public key> <url>"`.

**Why it matters for trove.** Today internal modules are excluded from verification with
`GOPRIVATE` or `GONOSUMDB`; a private database closes that gap, and the Go libraries fit trove's
Go backend.

**Size.** large — an append-only log with signed tree heads, tiles and key custody.

Behavior:

- Endpoints: `<base>/latest` (signed tree head as a signed note), `<base>/lookup/<module>@<version>`
  (record number, go.sum lines, signed tree head), `<base>/tile/<H>/<L>/<K>[.p/<W>]` (hash tiles,
  partial tiles with `.p/<W>`), `<base>/tile/<H>/data/<K>[.p/<W>]` (record data).
- The `go` command checks inclusion and consistency proofs and stores signed tree heads and tiles in
  its module cache. A database unknown to the `go` command must be configured with its public key.
- If a version is not yet logged, the database fetches it from its origin before replying.
- Consequence: `GOSUMDB` names one database; a client pointed at trove's database is verified
  only against it, so the private database must also log public modules (fetched through trove's
  Go remote).
- Records are never removed, also when a module version is deleted.

| Setting | Meaning | Default |
|---|---|---|
| Database name and key | Name and signing key announced to clients | unspecified |
| Module scope | Module path patterns logged | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Latest | none | signed tree head | read |
| Lookup | module, version | record and tree head | read |
| Tile | coordinates | tile data | read |

UI: Go repository settings; system signing keys.

Limits and edge cases: the log must be served consistently by all replicas; a fork (two different
heads) is detected by clients as an attack.

Interactions: PKG-GO-014 in [formats/go.md](formats/go.md); IAM-046 in
[identity-and-access.md](identity-and-access.md).

### BEYOND-034 · Notary Project signature verification for OCI artifacts
Priority: **P2**

**What it is.** Verifying Notary Project signatures, the second widely used signature format for
OCI artifacts next to Sigstore, when content enters or leaves trove, against an X.509 trust
store and a trust policy per repository. BEYOND-019 covers Sigstore only; the two formats share
discovery through referrers but differ in trust model (X.509 certificate chains to configured
roots instead of short-lived keyless certificates and a transparency log).

**Specification.** Notary Project specifications v1.1.0:
[signature specification](https://github.com/notaryproject/specifications/blob/v1.1.0/specs/signature-specification.md),
[signing scheme](https://github.com/notaryproject/specifications/blob/v1.1.0/specs/signing-scheme.md),
[trust store and trust policy](https://github.com/notaryproject/specifications/blob/v1.1.0/specs/trust-store-trust-policy.md).

**Clients.** notation (`notation sign`, `notation verify`); Kubernetes admission webhooks and
other verifiers that implement the specification.

**Why it matters for trove.** Organizations with an existing PKI sign images with X.509
certificates instead of Sigstore; without this entry the verification gate either ignores those
signatures or refuses the images.

**Size.** medium — the referrers discovery and the policy framework of BEYOND-019 are reused; the
new parts are envelope parsing (JWS and COSE), X.509 chain building, RFC 3161 timestamp
validation and revocation checks.

Behavior:

- Storage: a signature is an OCI image manifest whose config media type is
  `application/vnd.cncf.notary.signature` (config content unused), with exactly one layer holding
  the signature envelope (`application/jose+json` or `application/cose`), a required `subject`
  pointing at the signed manifest, and a required annotation
  `io.cncf.notary.x509chain.thumbprint#S256` listing the SHA-256 fingerprints of the signing
  certificate and its chain. Clients find signatures through the referrers API (BEYOND-015).
- Signed payload: media type `application/vnd.cncf.notary.payload.v1+json`, carrying the
  descriptor of the target artifact.
- Signing schemes: `notary.x509` (signing time is not authenticated; an optional RFC 3161
  countersignature from a timestamp authority proves it) and `notary.x509.signingAuthority`
  (the signing service supplies an authentic signing time).
- Trust store: named sets of root certificates of three kinds: certificate authorities, signing
  authorities and timestamp authorities. Intermediate certificates do not belong in it.
- Trust policy: one or more statements, each with registry scopes it applies to, a verification
  level, the named trust stores, and the trusted identities (attributes of the signing
  certificate's subject, or `*` for any certificate issued under the configured roots).
- Validations: integrity, authenticity, authentic timestamp, expiry (only when the signer set an
  expiry) and revocation (OCSP preferred over CRL, falling back to CRL when OCSP is unavailable).

| Verification level | Integrity | Authenticity | Authentic timestamp | Expiry | Revocation |
|---|---|---|---|---|---|
| strict | enforced | enforced | enforced | enforced | enforced |
| permissive | enforced | enforced | logged | logged | logged |
| audit | enforced | logged | logged | logged | logged |
| skip | skipped | skipped | skipped | skipped | skipped |

| Setting | Meaning | Default |
|---|---|---|
| Trust stores | Named sets of root certificates per kind | unspecified |
| Trust policy | Scopes, level, trust stores, trusted identities | unspecified |
| Timestamp verification | Always, or only after a certificate in the chain has expired | always (per the specification) |
| Revocation source access | Whether trove may reach OCSP responders and CRL endpoints (air-gapped sites cannot) | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Verify on demand | repository, artifact | result per signature with certificate subject and reasons | read |
| Manage trust stores and policy | certificates, policy document | stored configuration | manage |

UI: repository settings, security tab (shared with BEYOND-019); artifact view lists Notary
Project signatures with signer subject and verification status.

Limits and edge cases: the `skip` level cannot be applied to the global scope `*`. Revocation
checks need outbound access at verification time; a revocation status that cannot be determined
counts as a failure, which is enforced only at the `strict` level. The specification leaves trust
store distribution out of scope, so trove must define how certificates reach every replica.

Interactions: BEYOND-015, BEYOND-016, BEYOND-017, BEYOND-019; IAM-049 in
[identity-and-access.md](identity-and-access.md).

## Upstream protection and caching

### BEYOND-027 · Minimum release age for remote repositories
Priority: **P1**

**What it is.** A remote-repository setting that hides upstream versions younger than a threshold
from metadata and refuses their download, so freshly published (possibly malicious) versions are
not consumed before the ecosystem had time to react.

**Specification.** No server-side standard; per-ecosystem publish-time fields:
npm package documents (`time`), [PEP 700](https://peps.python.org/pep-0700/) `upload-time`, NuGet
[registration](https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource)
`published`, Cargo [index](https://doc.rust-lang.org/cargo/reference/registry-index.html)
`pubtime`, Go [proxy protocol](https://go.dev/ref/mod#goproxy-protocol) `.info` `Time`.

**Clients.** Server-side, effective for every client. Client-side equivalents exist: npm
`min-release-age` and `before`, pnpm `minimumReleaseAge` (minutes; the current documentation lists
a default of 1440), Yarn `npmMinimalAgeGate`, uv `exclude-newer`, pip `--uploaded-prior-to`
(added in pip 26.0 as an absolute timestamp; since pip 26.1 also a duration in days such as `P3D`;
effective only where the index supplies `upload-time`;
[pip changelog](https://pip.pypa.io/en/stable/news/)).

**Why it matters for trove.** Pull-through proxying is an owner constraint; enforcing a cooldown
once at the proxy protects every client, including those that cannot be configured.

**Size.** medium — a per-format metadata filter, tag rewriting and time-aware caching.

| Format | Time source | Caveat |
|---|---|---|
| npm | full document `time[<version>]` | abbreviated documents lack `time`; the remote must fetch the full one |
| PyPI | `upload-time` per file (JSON API 1.1 or later) | optional; HTML pages carry no time |
| NuGet v3 | registration `published` | on nuget.org an unlisted package has year 1900 |
| Cargo | index `pubtime` | optional |
| Go | `.info` `Time` | commit time, not publication time |
| Maven, OCI | none per version in standard metadata | only HTTP `Last-Modified` or first-seen time |

Behavior:

- Metadata served by the remote (and through virtual repositories) omits versions younger than the
  threshold. Tags that point at a hidden version are rewritten to the newest eligible version (npm
  `dist-tags.latest`); PyPI pages drop the files and the version entries.
- A direct request for a hidden version is refused.
- Exceptions by package pattern let internal or urgently needed packages through.
- Versions become eligible as time passes; cached metadata must expire no later than the next
  version's eligibility time.
- Missing time: treat as old, treat as young, or use the time trove first saw the version.

| Setting | Meaning | Default |
|---|---|---|
| Minimum release age | Threshold duration | unspecified (off) |
| Exceptions | Package patterns exempt from the rule | unspecified |
| Missing-time policy | old, young, or first-seen | unspecified |
| Refusal status | 404 or 403 with a reason | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Explain hidden version | repository, package, version | eligibility time and rule | read |

UI: remote repository settings, upstream protection section.

Limits and edge cases: a lockfile that pins a hidden version fails to install until the version
ages in; the refusal message should say when. Go's commit time can be far older than the tag,
which defeats the rule for Go unless first-seen time is used.

Interactions: SEC-014 and SEC-025 in [security-scanning-and-curation.md](security-scanning-and-curation.md);
PKG-NPM-027 in [formats/npm.md](formats/npm.md); RMT-018 in
[remote-repositories.md](remote-repositories.md).

### BEYOND-028 · Namespace claims against dependency confusion
Priority: **P1**

**What it is.** Package names, scopes or prefixes claimed by local repositories. In a virtual
repository, a claimed name resolves only from the claiming members; remote members are never asked
for it, and remotes refuse to cache it.

**Specification.** No server-side standard. Client-side counterparts: npm scoped registries
(`@scope:registry=`), NuGet [package source mapping](https://learn.microsoft.com/en-us/nuget/consume-packages/package-source-mapping)
(patterns: exact ID or prefix ending in `*`; most specific wins; NuGet 6.0 and later), uv's
`first-index` strategy and explicit indexes ([uv indexes](https://docs.astral.sh/uv/concepts/indexes/)).
A server-side metadata approach for Python, [PEP 708](https://peps.python.org/pep-0708/), was
rejected.

**Clients.** Server-side; effective for every client of a virtual repository.

**Why it matters for trove.** Priority resolution still falls back to a remote when the internal
package is missing (not yet published, deleted, or misspelled), which is exactly when an attacker's
public package with the same name wins. A claim closes that gap.

**Size.** small — a pattern table consulted during virtual resolution and remote fetch.

Behavior:

- Pattern syntax follows NuGet's mapping rules: exact name, or prefix ending in `*`; the most
  specific pattern wins. Names are compared after the format's normalization (npm lowercase, NuGet
  case-insensitive IDs, PyPI normalized names).
- A claimed name not found in the claiming members returns not found; no remote is consulted.
- Claims can be created automatically when a name is first published to a local repository.

| Setting | Meaning | Default |
|---|---|---|
| Claimed patterns | Per local repository | unspecified |
| Auto-claim on first publish | Claim a name when it is first deployed | unspecified |
| Remote refusal | Remotes refuse to fetch claimed names | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Manage claims | repository, patterns | stored claims | manage |
| Explain resolution | virtual repository, name | members consulted and why | read |

UI: local repository settings, namespace section; virtual repository resolution view.

Limits and edge cases: overlapping claims by two local repositories in the same virtual repository
must be rejected or ordered explicitly.

Interactions: RMT-034 and RMT-037 in [remote-repositories.md](remote-repositories.md); REPO-011 and
REPO-035 in [repositories.md](repositories.md); IAM-021 in [identity-and-access.md](identity-and-access.md).

### BEYOND-029 · Cache pre-warming from lockfiles and SBOMs
Priority: **P2**

**What it is.** Filling remote caches ahead of need from a dependency list: a lockfile or SBOM is
submitted, and every listed package version is fetched through the normal remote path.

**Specification.** Inputs are ecosystem formats: npm `package-lock.json` (`packages` with
`version`, `resolved`, `integrity`), NuGet `packages.lock.json` (resolved version and content
hash), [PEP 751](https://peps.python.org/pep-0751/) `pylock.toml` (Final; file URLs, hashes and
upload times), Cargo and Go lock files, CycloneDX or SPDX documents with package URLs.

**Clients.** Operators and CI jobs (for example before an offline window or a release build).

**Why it matters for trove.** Upstream outages and rate limits hit builds at the worst time;
warming the cache from the lockfiles that builds use removes the dependency on the upstream.

**Size.** medium — parsers per input format and a job runner with reporting.

Behavior:

- Each package goes through the same checks as a client request (permissions, age gate,
  curation, signature policy).
- Hashes in the input are compared with fetched content; a mismatch is reported, not cached.
- Optional scheduled refresh of metadata for packages requested recently, before their cache period
  expires.

| Setting | Meaning | Default |
|---|---|---|
| Accepted input formats | Lockfile and SBOM types | unspecified |
| Concurrency | Parallel upstream fetches | unspecified |
| Metadata refresh | Refresh hot metadata ahead of expiry | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Submit warm-up job | repository, input file | job ID | deploy on the target |
| Job status | job ID | fetched, failed, blocked, mismatched | read |

UI: remote repository, maintenance tab.

Limits and edge cases: warming must respect upstream rate limits (BEYOND-030 applies to upstream
fetch rates too).

Interactions: RMT-027 in [remote-repositories.md](remote-repositories.md); BEYOND-027.

## Tenancy

### BEYOND-030 · Per-tenant quotas and rate limits
Priority: **P2**

**What it is.** Limits per tenant (a repository, a group of repositories, or a principal) on stored
bytes, artifact count, request rate, download volume and upstream fetch rate, with standard HTTP
signalling.

**Specification.** [RFC 6585](https://www.rfc-editor.org/rfc/rfc6585) (429 Too Many Requests),
[RFC 9110](https://www.rfc-editor.org/rfc/rfc9110) (`Retry-After`), the IETF draft
[RateLimit header fields](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
(draft 11, not an RFC: `RateLimit-Policy` with `q`, `qu`, `w`, `pk`; `RateLimit` with `r`, `t`,
`pk`).

**Clients.** All; package managers retry on 429 with varying respect for `Retry-After`.

**Why it matters for trove.** One misbehaving CI pipeline or team must not exhaust storage,
bandwidth or the upstream's rate limit for everyone.

**Size.** medium — counters shared across replicas through PostgreSQL, accounting rules, and
enforcement points in every protocol handler.

Behavior:

- Storage: logical bytes (sum of artifact sizes) or physical bytes (unique blobs); with
  deduplication the two differ, and the rule must state which one counts.
- Request and download limits answer 429 with `Retry-After`; storage limits refuse deploys (STOR-040
  uses 413).
- Soft limits warn at thresholds; hard limits refuse.

| Setting | Meaning | Default |
|---|---|---|
| Tenant scope | repository, repository group, principal | unspecified |
| Storage limit and accounting | bytes; logical or physical | unspecified |
| Artifact count limit | number of stored items | unspecified |
| Request rate | requests per window | unspecified |
| Download volume | bytes per window | unspecified |
| Upstream fetch rate | per remote | unspecified |
| Warning thresholds | percentages | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Manage quotas | tenant, limits | stored | administrator |
| Usage | tenant | current usage per dimension | read |

UI: administration, quotas; repository view shows usage.

Limits and edge cases: exact rate limiting across replicas costs a database round trip per
request; approximate per-replica shares are an alternative.

Interactions: STOR-040 in [storage-and-blobs.md](storage-and-blobs.md); IAM-061 in
[identity-and-access.md](identity-and-access.md); BEYOND-029.

## CI and workload identity

### BEYOND-031 · Provider presets for further CI systems
Priority: **P1**

**What it is.** Ready-made provider configurations for CI systems beyond GitLab, GitHub Actions and
Azure DevOps: known issuer patterns, claim schemas for the mapping editor, and mandatory
restrictions. Token verification uses the generic OIDC type.

**Specification.** [CircleCI OIDC tokens](https://circleci.com/docs/guides/permissions-authentication/openid-connect-tokens/),
[Buildkite OIDC](https://buildkite.com/docs/agent/v3/cli-oidc),
[Bitbucket Pipelines OIDC](https://support.atlassian.com/bitbucket-cloud/docs/integrate-pipelines-with-resource-servers-using-oidc/),
[Jenkins OpenID Connect Provider plugin](https://plugins.jenkins.io/oidc-provider/).

**Clients.** CI jobs that exchange their ID token for a trove token.

**Why it matters for trove.** The rubric lists further CI OIDC providers as P1; presets prevent
the most common mistake, a mapping that matches too broadly.

**Size.** small — data and validation on top of CIOIDC-002.

| CI system | Issuer | Subject | Audience | Token delivery and lifetime |
|---|---|---|---|---|
| CircleCI | `https://oidc.circleci.com/org/<organization id>` | V1 `org/<org>/project/<project>/user/<user>`; V2 adds `/vcs-origin/<origin>/vcs-ref/<ref>`; V3 uses `:` as separator and carries `checkout-source`, `checkout-ref`, `config-source` and `config-ref` instead | organization ID by default | `CIRCLE_OIDC_TOKEN`, `CIRCLE_OIDC_TOKEN_V2` (cloud and server), `CIRCLE_OIDC_TOKEN_V3` (cloud only); expires one hour after issue |
| Buildkite | `https://agent.buildkite.com` | `organization:<org slug>:pipeline:<pipeline slug>:ref:<ref>:commit:<sha>:step:<step key>` by default; with `--subject-claim` the value of one immutable ID claim (`organization_id`, `pipeline_id`, `cluster_id`, `queue_id`, `build_id`, `job_id` or `agent_id`) | `https://buildkite.com/<org slug>` by default | `buildkite-agent oidc request-token` (`--audience`, `--lifetime`, `--claim`, `--subject-claim`); default expiry 5 minutes |
| Bitbucket Pipelines | per workspace, shown in the repository's OIDC settings | exact format open; it contains a per-run step UUID | workspace-specific default; up to 10 custom audiences of at most 150 characters | `oidc: true` on the step, `BITBUCKET_STEP_OIDC_TOKEN` |
| Jenkins (plugin) | `https://<jenkins host>/oidc` for root-level credentials | job URL by default; templates configurable | optional per credential | credential types for the token as text or as file; lifetime unspecified |

Useful claims: CircleCI `oidc.circleci.com/project-id`, `context-ids`, `ssh-rerun` (all token
versions), `vcs-origin` and `vcs-ref` (V1 and V2), `checkout-source`, `checkout-ref`,
`config-source` and `config-ref` (V2 and V3); Buildkite `organization_id`, `organization_slug`,
`pipeline_id`, `pipeline_slug`, `build_branch`, `build_tag`, `build_commit`, `step_key`,
`build_source`, `runner_environment` (optional through `--claim`: `cluster_id`, `cluster_name`,
`queue_id`, `queue_key`, `agent_tag:<name>`). Buildkite publishes discovery at
`https://agent.buildkite.com/.well-known/openid-configuration` and keys at
`https://agent.buildkite.com/.well-known/jwks`.

| Setting | Meaning | Default |
|---|---|---|
| Preset | CI system | unspecified |
| Tenant identifier | Organization ID, slug or workspace that the issuer or claims must carry | unspecified |
| Required claim restrictions | Claims every mapping must constrain | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Create provider from preset | preset, tenant identifier | provider configuration | administrator |

UI: administration, CI identity providers, "add from preset".

Limits and edge cases: mappings for Bitbucket must not match the full subject, because the step
UUID changes on every run; CircleCI jobs re-run with SSH access (`ssh-rerun: true`) should be
excluded from write mappings; Buildkite's default subject is built from slugs, which can be
renamed, so mappings should match the immutable `organization_id` and `pipeline_id` claims;
Jenkins issuer and claims depend on plugin configuration.

Interactions: CIOIDC-001, CIOIDC-002, CIOIDC-007 and CIOIDC-018 in
[oidc-ci-integration.md](oidc-ci-integration.md).

### BEYOND-032 · Kubernetes service-account token trust
Priority: **P2**

**What it is.** Accepting projected Kubernetes service-account tokens directly in the token
exchange, so in-cluster jobs obtain trove tokens without stored secrets and without a cloud IAM
detour.

**Specification.** Kubernetes [service accounts](https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/),
sections *ServiceAccount token volume projection* and *Service account issuer discovery*.

**Clients.** Pods with a projected `serviceAccountToken` volume (`audience`, `expirationSeconds`).

**Why it matters for trove.** trove runs on Kubernetes; build jobs in the same or other
clusters are the main consumers.

**Size.** small — a provider type over CIOIDC-002 with Kubernetes-specific defaults.

Behavior:

- The cluster's issuer is set by `--service-account-issuer` (repeatable; the first signs, all are
  accepted). Discovery is served at `<issuer>/.well-known/openid-configuration`, keys at
  `/openid/v1/jwks`.
- By default the discovery role is bound only to the group of all service accounts, not to
  anonymous users; trove can read the keys only if the cluster exposes them or the keys are
  configured statically.
- Without `--api-audiences`, the cluster's default audience is the issuer URL. trove must require
  its own audience, otherwise it accepts tokens meant for the API server.
- Subjects have the form `system:serviceaccount:<namespace>:<name>`.

| Setting | Meaning | Default |
|---|---|---|
| Issuer | Cluster issuer URL | unspecified |
| Key source | Discovery or static JWKS | unspecified |
| Required audience | Audience the projected token must carry | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Token exchange | service-account token | trove token | per mapping |

UI: administration, CI identity providers, Kubernetes type.

Limits and edge cases: a cluster that rotates its signing keys must be re-read; a token's
`expirationSeconds` is chosen by the pod.

Interactions: CIOIDC-002, CIOIDC-018 and CIOIDC-029 in [oidc-ci-integration.md](oidc-ci-integration.md).

### BEYOND-033 · SPIFFE JWT-SVID trust
Priority: **P2**

**What it is.** Accepting SPIFFE JWT-SVIDs as identities in the token exchange: workloads with a
SPIFFE ID obtain trove tokens, with signing keys taken from the trust domain's SPIFFE bundle.

**Specification.** [JWT-SVID](https://github.com/spiffe/spiffe/blob/main/standards/JWT-SVID.md),
[SPIFFE Trust Domain and Bundle](https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE_Trust_Domain_and_Bundle.md).

**Clients.** Workloads in SPIFFE-enabled environments.

**Why it matters for trove.** Platform teams with a SPIFFE deployment want one workload identity
for every service, including package access.

**Size.** small — a provider type that reads keys from a bundle and matches SPIFFE IDs.

Behavior:

- `sub` is the SPIFFE ID (`spiffe://<trust domain>/<path>`); `aud` must be present and contain
  trove's audience; `exp` must be present. The specification does not require `iss`, so the
  generic OIDC type (which checks the issuer) does not fit.
- `alg` is limited to the RSA, ECDSA and RSA-PSS families of RFC 7518; JWS compact serialization
  only; `typ`, if set, is `JWT` or `JOSE`.
- Keys come from the SPIFFE bundle as JWKs with `use` = `jwt-svid` and a `kid`; other entries are
  ignored.
- The trust domain in `sub` must match the configured trust domain.

| Setting | Meaning | Default |
|---|---|---|
| Trust domain | Accepted SPIFFE trust domain | unspecified |
| Bundle source | Bundle endpoint URL or static bundle | unspecified |
| Required audience | Audience value | unspecified |

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Token exchange | JWT-SVID | trove token | per mapping |

UI: administration, CI identity providers, SPIFFE type; mappings match SPIFFE ID path patterns.

Limits and edge cases: JWT-SVIDs are bearer tokens with replay risk across audiences; trove must
accept only tokens whose audience list contains its own value.

Interactions: CIOIDC-007 and CIOIDC-018 in [oidc-ci-integration.md](oidc-ci-integration.md);
BEYOND-032.

## Notes for implementers

- Signed metadata passes byte for byte: Homebrew's JWS API files, Hackage TUF files, OSTree
  summaries, pacman database signatures and FreeBSD catalogues break if re-serialized or
  URL-rewritten. Any merge in a virtual repository needs trove's own key and client
  reconfiguration.
- Referrers are untagged manifests. A cleanup rule for untagged manifests deletes every signature
  and SBOM unless referrers count as part of their subject (BEYOND-016).
- The referrers endpoint never answers 404 for an existing repository; clients interpret 404 as
  "no API" and fall back to the tag schema.
- npm publishes with provenance carry two attachments; the `.sigstore` attachment's `data` is plain
  JSON, the tarball's is base64. npm asks `/-/package/<name>/visibility` first and refuses private
  packages unless `--access public` is given.
- Publish times differ in meaning: Go `.info` `Time` is commit time, nuget.org reports 1900 for
  unlisted packages, npm abbreviated documents carry no `time`, PyPI `upload-time` is optional. A
  missing time must lead to an explicit policy decision, never to a silent pass.
- The Open VSX CLI puts the publish token into the query string unless `/api/version` reports 1.3.0
  or later; implement the endpoint and keep `token` out of access logs.
- URL-prefix download proxies (Homebrew's artifact domain) are server-side request forgery
  vectors; they need a host allowlist and redirect checks.
- CI token shapes differ: Bitbucket subjects contain a per-run step UUID, CircleCI's default
  audience is the organization ID, Buildkite tokens live five minutes by default, Kubernetes'
  default audience is the issuer, and JWT-SVIDs may lack `iss`.
- Some protocols need HTTP features beyond plain GET: Hackage clients update the index with byte
  ranges, OSTree pulls issue one request per object (keep-alive or HTTP/2), and Julia tarballs are
  themselves the compressed stream, detected by magic bytes.
- A Python index page must declare an `api-version` that covers the keys it emits: `provenance`
  needs 1.3, `project-status` needs 1.4. A merged virtual page inherits the highest feature any
  member contributes. Clients should warn on a newer minor version and must fail on a greater major
  version.
- Default client signature checks can turn an unsigned private repository into a broken one:
  pacman as configured on a stock Arch system requires package signatures from fully trusted keys,
  and current
  Portage verifies remote binary packages by default. A GPKG signature sits inside the package
  file, so adding one changes the file; a pacman package signature is a detached file that any key
  the clients trust can produce.
