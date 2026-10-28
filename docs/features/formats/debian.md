# Debian package format

This page catalogs what a repository manager does to act as an APT repository for Debian and
Ubuntu clients: hosting binary and source packages in local repositories with automatically
computed `Packages` and `Release` indexes, proxying public archives in remote repositories,
merging both in virtual repositories, signing repository metadata with GPG keys (including key
assignment per repository and key rotation), point-in-time metadata snapshots, and client setup.
The repository format itself is defined by the Debian project
([Debian repository format](https://wiki.debian.org/DebianRepository/Format)); entries that rely on
that definition say so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path (APT clients
point at it), `<format-url>` for the repository's format endpoint URL (used for snapshots), and
`https://repo.example.com` for the system's base URL; the concrete path layout is the
implementation's choice. `deb-local`, `deb-remote` and `deb-virtual` are example repository keys.

## Repositories and layout

### PKG-DEB-001 · Debian repositories in all three repository classes
Priority: **P2**

Debian is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's own `.deb` packages and Debian source packages; the
  system computes their indexes (PKG-DEB-008);
- remote repositories proxy and cache a public archive such as an Ubuntu or Debian mirror
  (PKG-DEB-021);
- virtual repositories merge local and remote Debian repositories behind one APT source
  (PKG-DEB-023).

Creating a repository requires the system administrator or a project administrator role; other
users are not offered the create action. UI location: administration, repositories, create
repository, choose the class and the Debian format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)). A remote repository form comes with a pre-filled
upstream URL that the user confirms or changes; the pre-filled value is unspecified.

Supported APT client versions: unspecified.

Interactions: the federated repository class also accepts Debian (federation is outside this
page's scope, see [../replication-and-federation.md](../replication-and-federation.md)).

### PKG-DEB-002 · Automatic layout and package coordinates
Priority: **P2**

The default layout follows the Debian archive structure: package files live in a pool, and the
system generates the `dists/` tree that APT reads.

- A package file can be stored at any path inside the repository. Examples use `pool/` by
  Debian convention, but the path is free.
- Where a package appears in the indexes is decided by three *coordinates* attached to the stored
  file as properties: distribution (for example `bookworm`, `jammy`), component (for example
  `main`, `contrib`) and architecture (for example `amd64`, `i386`, `all`). The property keys are
  the implementation's choice; they are set at deploy time (PKG-DEB-004) or computed for cached
  remote packages (PKG-DEB-022).
- From the coordinates the system produces, per distribution, the `Release` file and, per
  component and architecture, a `binary-<arch>/Packages` index (with compressed variants,
  PKG-DEB-011). Each index entry must reference the package by its path relative to the
  repository root, because APT downloads `<repo-url>/<path>` (Debian repository format).
- One stored file can carry several coordinate sets (PKG-DEB-005).

This is the automatic layout, in contrast to the deprecated trivial layout
(PKG-DEB-003). Only automatic-layout repositories support source packages, acquire-by-hash and
membership in virtual repositories.

### PKG-DEB-003 · Legacy trivial (flat) layout
Priority: **Later**

A per-repository switch selects the deprecated flat layout instead of the automatic one.

| Setting | Meaning | Default |
|---|---|---|
| Trivial layout | Serves the repository in the deprecated flat Debian layout. | off (an example value; whether it is the default is unspecified) |

Limits:

- source packages are not supported (neither deploy nor resolve);
- acquire-by-hash (PKG-DEB-013) is not supported;
- a trivial-layout repository cannot be a member of a virtual repository; the setting exists on
  virtual repositories in the configuration schema but is not supported there.

How indexes are computed in this layout is unspecified. UI location: repository settings, basic
tab, Debian settings section.

## Deploying packages

### PKG-DEB-004 · Deploy a binary package with its coordinates
Priority: **P2**

A `.deb` file is deployed with a plain HTTP `PUT` to a path in a local repository, with the
coordinates attached as properties in the same request (matrix parameters, ART-003 in
[../artifacts.md](../artifacts.md)):

```text
PUT <repo-url>/pool/<file>.deb;<distribution-property>=bookworm;<component-property>=main;<architecture-property>=amd64
```

- Authentication: user name and password or token as HTTP Basic credentials (TOK-034 in
  [../access-tokens.md](../access-tokens.md)); required permission: deploy on the target path
  (IAM-022 in [../identity-and-access.md](../identity-and-access.md)).
- The deploy path format is strict: path, then each coordinate as `;key=value`. Superfluous spaces
  in a manually typed target path break the coordinates.
- After a deploy the index is recalculated asynchronously; it can take about a minute until the
  package appears.
- A deploy whose package fails metadata validation is stored but not indexed (PKG-DEB-009).

UI: the deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md)) has a "deploy as
Debian package" option. When set, it shows distribution, component and architecture fields and
updates the target path from them.

### PKG-DEB-005 · Several coordinate sets for one package
Priority: **P2**

One deployed file can be indexed under several distributions, components and architectures at
once by repeating a coordinate property with different values, for example two distributions,
two components and two architectures in one deploy path. The package then appears in every
combination of the given values. The values come either as repeated `;key=value` pairs or as
comma-separated values; an implementation should accept both.

### PKG-DEB-006 · Architecture-independent packages
Priority: **P2**

A package deployed with architecture `all`:

- appears in the `Packages` index of every other architecture under the same distribution and
  component, and additionally in a `binary-all` index that lists all `all` packages;
- is removed from all those indexes when it is deleted;
- keeps an architecture index alive: when the last architecture-specific package of an
  architecture is removed while an `all` package remains, the index for that architecture is
  preserved. To drop such an index, the administrator recalculates the repository metadata
  (PKG-DEB-016), which also cleans orphaned entries.

### PKG-DEB-007 · Source packages
Priority: **P2**

Local repositories accept Debian source packages so that `apt-get source` works.

- A source package consists of three files, each uploaded separately: the `.dsc` control file,
  the upstream tarball (`orig.tar.*`) and the Debian packaging tarball (`debian.tar.*`).
- The `.dsc` upload carries distribution and component as properties (no architecture). Source
  packages use property keys that differ from the binary-package keys. The
  `.dsc` upload is what updates the repository's source metadata; the two tarballs are uploaded
  without properties.
- Deploying source packages works only for local repositories; resolving them works from local
  and remote repositories, not from virtual repositories.
- Not supported with the trivial layout.

Clients reference the source index with a `deb-src` line (PKG-DEB-026).

## Metadata calculation

### PKG-DEB-008 · Automatic index calculation for local repositories
Priority: **P2**

The system generates APT-compliant indexes for local repositories, with no external tool.

- Triggers: deploying a package and deleting one (the deletion trigger follows from PKG-DEB-006).
  Whether copy, move and content import trigger it, as they do for RPM (PKG-RPM-003 in
  [rpm.md](rpm.md)), is unspecified for Debian. The calculation runs asynchronously after the
  change (see PKG-DEB-004 for the delay).
- Output per distribution: `Release` (and its signatures, PKG-DEB-017); per component and
  architecture: `Packages` plus compressed variants (PKG-DEB-011); for source packages a sources
  index.
- `Release` contains the Origin and Label fields (PKG-DEB-015) and, when acquire-by-hash is on,
  the acquire-by-hash flag (PKG-DEB-013).
- Packages that fail validation are skipped (PKG-DEB-009).

Interactions: a repository-wide recalculation is available on demand (PKG-DEB-016, REPO-009 in
[../repositories.md](../repositories.md)).

### PKG-DEB-009 · Package metadata validation and index status
Priority: **P2**

Before indexing a package the system checks that the relevant control-file fields are not empty,
so that a malformed package cannot corrupt the indexes. Which fields are checked is unspecified.

A package that fails validation:

- is not indexed;
- gets an index-status property whose value is the reason for the failure;
- shows a "not indexed" message with the reason in the Debian information tab of the item details
  in the artifact browser (ART-041 in [../artifacts.md](../artifacts.md));
- produces a log message explaining the failure.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Debian metadata validation | instance-wide, set in the system's startup configuration | Turns validation on or off. | on |

Malformed packages can be found with a property search on the index-status property (SRCH-007
in [../search-and-query.md](../search-and-query.md)) or with the query language.

### PKG-DEB-010 · Package metadata as searchable properties
Priority: **P2**

Entries from each package's control file are written as properties on the stored file when it is
deployed. Which control fields become properties is unspecified. The properties feed the package
search with Debian-specific criteria (SRCH-050 in [../search-and-query.md](../search-and-query.md)).
Packages deployed before the feature existed get the properties when the metadata recalculation
runs with property writing enabled (PKG-DEB-016).

### PKG-DEB-011 · Index compression formats
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Additional index compression formats | local, virtual | Compressed index variants produced besides gzip. Values: bzip2 (`.bz2`), LZMA (`.lzma`), XZ (`.xz`). Gzip (`.gz`) is always produced and cannot be turned off. | none selected (an optional multi-select; all three values appear as an example, and a default is unspecified) |

UI location: repository settings, basic tab, Debian settings section ("optional index compression
formats"); also settable through the repository configuration API (REPO-053 in
[../repositories.md](../repositories.md)).

### PKG-DEB-012 · Debug-symbol package indexing
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Index debug symbols | local, remote, virtual | Also indexes debug-symbol packages (`.ddeb`). The switch exists for all three classes. | unspecified |

How `.ddeb` packages are placed in the indexes (same `Packages` file or a separate debug
component) is unspecified. What the switch changes on a remote repository, which does not compute
its own indexes (PKG-DEB-021), is unspecified.

### PKG-DEB-013 · Acquire-by-hash
Priority: **P2**

The system supports APT's acquire-by-hash mechanism for automatic-layout repositories, so that
clients can fetch index files by checksum and avoid races between index and `Release` updates.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Acquire-by-hash | instance-wide, startup configuration | When on, every `Release` file declares acquire-by-hash support and the system adds the `by-hash` layout (MD5, SHA-1 and SHA-256) to all Debian repositories. | on |
| By-hash history | instance-wide, startup configuration | Number of index-generation cycles whose by-hash files are kept. | 3 |

Open: either the hashed objects are the packages, or, as in the Debian repository format, the
index files, held in `by-hash/<algorithm>/<checksum>` directories next to the index they belong
to; the spec decides. The exact set of files exposed by hash is unspecified. Not supported with
the trivial layout.

### PKG-DEB-014 · MD5 checksums in the Packages index
Priority: **P2**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Write MD5 into Packages | instance-wide, startup configuration | Adds each package's MD5 checksum to its `Packages` entry, for tools that validate with MD5 (for example Aptly). Takes effect after a restart. | off |

### PKG-DEB-015 · Origin and Label of the Release file
Priority: **P2**

The `Release` file carries Origin and Label fields. Both default to the system's own name. Two
properties set on the repository root folder override them (one for Origin, one for Label); the
property keys are the implementation's choice. When the override takes effect (immediately or at
the next index calculation) is unspecified.

### PKG-DEB-016 · Recalculate Debian metadata
Priority: **P2**

An operation that recomputes the `Packages` and `Release` metadata of a local repository from the
packages it holds. It also writes the control-file properties (PKG-DEB-010) to all packages by
default, and removes orphaned entries and indexes (PKG-DEB-006).

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Recalculate Debian metadata | repository key; run asynchronously (yes/no, default no); write properties (0 or 1) | plain-text confirmation | unspecified; the operation rejects unauthenticated (401) and unauthorized (403) callers |

- Supported by local repositories only.
- Synchronous by default: the call returns after the calculation; in asynchronous mode it returns
  immediately.
- UI: the "recalculate index" row action on the repository list (REPO-009 in
  [../repositories.md](../repositories.md)).

## Signing

### PKG-DEB-017 · Release signing with GPG
Priority: **P2**

The system signs repository metadata, never individual packages. When a GPG key is available for
the repository, every index calculation produces, next to `Release`:

| File | Content | Purpose |
|---|---|---|
| `Release.gpg` | detached signature of `Release` | classic APT verification |
| `InRelease` | `Release` with an inline (clear-text) signature | lets APT fetch one file instead of `Release` plus `Release.gpg` |

- Without a key, the repository is served unsigned; APT then accepts it only with an explicit
  trust override on the client (PKG-DEB-028).
- The key used is the repository's primary key (PKG-DEB-018) or, without one, presumably the
  instance-wide default key (PKG-DEB-019); the fallback is specified only for the companion
  client's key download, not for signing.
- Which key signs when a secondary key is also set (one or both signatures) is unspecified.

### PKG-DEB-018 · Signing keys per repository and key rotation
Priority: **P2**

Debian repositories (local and virtual), RPM repositories (local, PKG-RPM-013 in [rpm.md](rpm.md))
and Opkg repositories (local only, since Opkg has no virtual class) reference GPG key pairs from
the central key store (IAM-046 in [../identity-and-access.md](../identity-and-access.md)) by name:
a *primary* key that signs, and an optional *secondary* key kept as a standby for rotation. Both
key names are settings of Debian local and Debian virtual repositories.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Primary signing key | local, virtual | Name of the GPG key pair that signs the repository metadata. | unspecified (optional) |
| Secondary signing key | local, virtual | Name of a second GPG key pair, used as backup when the primary key is removed. | unspecified (optional) |

<details>
<summary>Key assignment operations</summary>

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Assign keys to a repository | repository key; key pair name (plain text) | confirmation; 400 for an invalid key name | administrator |
| Remove a key from a repository | repository key; key pair name | confirmation; 404 if repository or key unknown | administrator |
| Set primary key | repository key; key pair name | confirmation; 400 for an invalid key name | administrator |
| Remove primary key | repository key | confirmation; 404 if none set | administrator |
| Set secondary key | repository key; key pair name | confirmation; 400 for an invalid key name | administrator |
| Remove secondary key | repository key | confirmation; 404 if none set | administrator |
| Promote secondary to primary | repository key | confirmation; 400 if no secondary key exists; 404 for an unknown repository | administrator |
| Download primary public key | repository key | ASCII-armored PGP public key block; 404 if none set | administrator |
| Download secondary public key | repository key | ASCII-armored PGP public key block; 404 if none set | administrator |
| Get the public key assigned to a repository | repository key | public key as a plain-text attachment with a suitable file name; 404 if repository or key unknown | any authenticated user, or anonymous when anonymous access is on (AUTHN-036 in [../authentication-providers.md](../authentication-providers.md)) |

</details>

Rotation procedure the operations allow:

1. Add the new key pair to the key store (IAM-046) and set it as the repository's secondary key.
2. Distribute the secondary public key to clients, so they trust both keys.
3. Promote the secondary key to primary; the old primary can then be removed. Removing the primary
   without promoting also lets the secondary take over (IAM-046).

Whether the metadata is re-signed immediately after a key change or at the next index calculation
is unspecified; an implementation should re-sign at once, because APT clients reject an `InRelease`
signed by a key they no longer trust.

UI location: repository settings (the tab is unspecified for GPG keys; Alpine's RSA key sits on
the advanced tab, PKG-APK-006 in [alpine.md](alpine.md)); key store under security, key
management, signing keys tab (IAM-046). Keys can live in an external secret manager (IAM-047).

### PKG-DEB-019 · Instance-wide default GPG key
Priority: **P2**

An older, single-key mechanism predating per-repository keys: one GPG key pair for the whole
instance, used for Debian, RPM and Opkg metadata.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Set default public key | ASCII-armored public key (plain-text body) | confirmation; 400 for an invalid key format | administrator |
| Set default private key | ASCII-armored private key (plain-text body) | confirmation; 400 for an invalid key format | administrator |
| Set default key passphrase | passphrase, passed in a request header | confirmation; 400 for an invalid passphrase | administrator |
| Get default public key | none | public key as plain text; 404 if none configured | any authenticated user, or anonymous when anonymous access is on |

- Keys sent in a request body must be transmitted byte-exact (line breaks preserved).
- Open: the key serves either Debian, RPM and Opkg (as for setting the public key) or only Debian
  and Opkg (as for getting the public key and setting the passphrase); the spec decides.
  Recommendation: treat the key as shared by all three.
- Precedence between this key and a repository's primary key: the companion client uses the
  repository's primary key and falls back to the default key (PKG-DEB-028); the signing side is
  unspecified (PKG-DEB-017).

### PKG-DEB-020 · Public key for client trust
Priority: **P2**

Clients need the public key to verify `Release` signatures. The system serves it through the
"get the public key assigned to a repository" operation (PKG-DEB-018) and the default public key
operation (PKG-DEB-019). Both are readable without administrator rights, so a client can fetch
the key with the same credentials it uses for packages. The client installs it as a keyring and
references it with `signed-by` (PKG-DEB-026).

## Remote and virtual repositories

### PKG-DEB-021 · Remote Debian repositories
Priority: **P2**

A remote repository with the Debian format proxies an APT archive (for example an Ubuntu archive
mirror) and caches what clients request (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

- Index files, including the sources index, are cached and renewed according to the metadata
  retrieval cache period (RMT-018).
- Packages are cached on first request like any other remote artifact.
- Source packages can be resolved from remote repositories (PKG-DEB-007).
- Cached packages do not carry coordinates until they are computed (PKG-DEB-022).

| Setting | Meaning | Default |
|---|---|---|
| List remote artifacts | Lets users browse the upstream archive's content in the artifact browser before anything is cached (ART-044 in [../artifacts.md](../artifacts.md)). | off |
| Index debug symbols | As PKG-DEB-012. | unspecified |

### PKG-DEB-022 · Coordinate calculation for cached remote packages
Priority: **P2**

An action that reads the upstream indexes and writes distribution, component and architecture
as properties on the packages already cached in a remote repository, so they can be searched and,
when copied to a local repository, indexed there.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Calculate cached remote Debian coordinates | remote repository key | plain-text confirmation | unspecified; the operation rejects unauthenticated (401) and unauthorized (403) callers |

- Supported on the cache of remote repositories only.
- Incremental: after the first run, later runs only process packages newly added to the cache.
- A first run over a large cache is slow, so it is best triggered on demand, such as right before
  cached packages are copied into a local repository.

UI: a "calculate Debian coordinates" action in the context menu of a remote Debian repository in
the artifact browser.

### PKG-DEB-023 · Virtual Debian repositories
Priority: **P2**

A virtual Debian repository serves one APT source that merges the indexes of its local and remote
members (REPO-033, REPO-037 in [../repositories.md](../repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Members | Local and remote Debian repositories, in resolution order. Only automatic-layout repositories qualify. | unspecified |
| Indexed remote architectures | Architectures the virtual repository indexes for its remote members; limiting the list speeds up the first indexing. | `amd64` for new repositories (older defaults also included `i386`; `arm64,amd64` is an example value) |
| Additional index compression formats | As PKG-DEB-011. | none |
| Index debug symbols | As PKG-DEB-012. | unspecified |
| Primary and secondary signing key | As PKG-DEB-018; signs the merged metadata. | unspecified (optional) |
| Metadata cache period | How long merged metadata is reused before members are checked again; 0 means always check (REPO-037). | unspecified |

- Debian source packages cannot be resolved through a virtual repository (PKG-DEB-007).
- Merge rules when two members provide the same package version are unspecified (general rules:
  REPO-035).

## Snapshots

### PKG-DEB-024 · Create a Debian snapshot
Priority: **P2**

A snapshot freezes the metadata of one distribution of a Debian repository (local, remote or
virtual) at a point in time, so clients can keep installing from that state while the repository
moves on. It is useful as a fallback when later package changes break dependencies.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create Debian snapshot | source repository key; run asynchronously (0 or 1); body: tag (required, user-chosen snapshot name), target repository (required), distribution (required) | plain-text confirmation; 400 for an invalid request | unspecified; the operation rejects unauthenticated (401) and unauthorized (403) callers |

Behavior:

- All metadata files under `dists/<distribution>` of the source repository (`Release`,
  `Packages` and the other index files) are copied into the snapshot folder
  `snapshots/<tag>` of the target repository. Package files are not copied.
- The snapshot folder gets a property pointing to the source repository; the property key is the
  implementation's choice.
- Snapshots are immutable. Behavior when the tag already exists is unspecified.
- Snapshots are created only through the API; there is no UI action for them.
- Whether the target repository may differ from the source and which classes it may have is
  unspecified; a typical setup stores snapshots in a local repository dedicated to snapshots.

### PKG-DEB-025 · Resolve and delete snapshots
Priority: **P2**

- Clients address a snapshot through the repository's format endpoint, not the plain file URL:
  `deb <format-url>/snapshots/<tag> <distribution> <component>`. The snapshot root behaves like a
  Debian repository of its own.
- Package downloads through a snapshot are redirected to the packages in the source repository;
  the snapshot holds only metadata.
- Deleting a snapshot folder does not delete any package file.
- Interaction with retention: packages referenced by a snapshot are not protected from deletion in
  the source repository (no such protection is specified); a snapshot can therefore point at
  packages that no longer exist.

## Client setup and authentication

### PKG-DEB-026 · APT source configuration
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) show
one line per source in `/etc/apt/sources.list` (or a file under `sources.list.d/`):

```text
deb https://repo.example.com/deb-virtual bookworm main
deb-src https://repo.example.com/deb-local bookworm main
```

- `deb` lines resolve binary packages from local, remote and virtual repositories; `deb-src`
  lines resolve source packages from local and remote repositories.
- The recommended source is a virtual repository.
- After configuration, standard commands work: `apt-get update`, `apt-get install <package>`,
  `apt-get source <package>`.
- Signed repositories: install the public key (PKG-DEB-020) as a keyring and add
  `[signed-by=/etc/apt/keyrings/<file>.asc]` to the line.

### PKG-DEB-027 · Authenticated APT access
Priority: **P2**

- APT sends HTTP Basic credentials embedded in the source URL:
  `deb https://<user>:<token>@repo.example.com/deb-virtual <distribution> <components>`. The
  password part can be an access token (TOK-034) or an encrypted password (IAM-035 in
  [../identity-and-access.md](../identity-and-access.md)).
- Anonymous read works when anonymous access is enabled (AUTHN-036).
- APT can also read credentials from `/etc/apt/auth.conf.d/` (APT documentation), which keeps them
  out of the source line; whether client setup uses this option is unspecified.

### PKG-DEB-028 · Companion CLI integration for APT
Priority: **P2**

The companion CLI (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) configures APT against a
Debian repository and wraps `apt-get`. It does not collect build metadata and does not upload
packages; uploads use the CLI's generic upload with a Debian option that sets the coordinates
`<distribution>/<component>/<architecture>` (BLD-036).

Persistent setup writes three files per repository and distribution:

| File | Content | Mode |
|---|---|---|
| `/etc/apt/sources.list.d/<prefix>-<repo>-<dist>.list` | `deb` line with credentials embedded in the URL | `0600` |
| `/etc/apt/preferences.d/<prefix>-<repo>-<dist>.pref` | APT pin at priority 1001 for the repository's host | `0644` |
| `/etc/apt/keyrings/<prefix>-<repo>-<dist>.asc` | ASCII-armored repository public key (only in key-import mode) | `0644` |

| Option | Meaning | Default |
|---|---|---|
| Repository | Debian repository key; interactive runs offer a list of virtual Debian repositories. | prompt |
| Distribution | Codename such as `bookworm`; required when not interactive. | prompt |
| Components | Space-separated list. | `main` |
| Import key | Downloads the repository's public key and adds `signed-by`; mutually exclusive with the next option. | off |
| Trusted | Adds `trusted=yes` and skips signature checks; for unsigned test repositories only. | off |
| Remove | Deletes the files the setup created, optionally filtered by repository and distribution; idempotent. | — |

Server-side behavior the CLI relies on:

- To find the key, the CLI reads the repository configuration for its primary signing key and,
  if none is named, requests the instance-wide default public key (PKG-DEB-019). If neither
  exists, setup fails with "no configured signing key".
- Without either trust option, the source has neither `signed-by` nor `trusted=yes`, and APT
  relies on keys already trusted on the host.
- The pin priority 1001 allows downgrades: an older version in the repository can win over a
  newer one from another source.
- One-off mode writes a temporary source that replaces the system's sources for one command and
  never adds `signed-by`, so a signed repository fails with `NO_PUBKEY` unless its key is already
  trusted or the trust override is used.
- The credential embedded in the source file is not refreshed; when it expires the setup must be
  rerun.
- Setup checks repository, distribution and component values before it puts them into source
  lines or file names, and it never writes `deb-src` lines; source packages stay with manual
  configuration (PKG-DEB-026).
- Setup refreshes the package lists after it changes a source; repeating it with an unchanged
  source changes nothing. A rerun without the key-import option keeps an existing key file.

## Notes for implementers

- APT downloads `<repo-url>/<Filename>` exactly as the `Packages` entry says; the stored path and
  the `Filename` field must agree, including for packages stored outside `pool/`.
- Index recalculation is asynchronous; tests and CI jobs that deploy and immediately run
  `apt-get update` see the old index for up to about a minute.
- `InRelease` and `Release.gpg` must be regenerated in the same step as `Release`; a stale
  signature makes APT reject the whole source.
- An `all` package keeps an architecture's `Packages` index alive after the last
  architecture-specific package is gone (PKG-DEB-006); deleting indexes needs a full
  recalculation.
- Gzip-compressed indexes must always exist; APT and many tools expect `Packages.gz` even when
  other formats are configured.
- Packages that fail validation disappear from the index silently from the client's point of
  view; surface the reason as a property and in the UI (PKG-DEB-009).
- The public-key download operation must work for ordinary users and anonymous readers, while the
  primary/secondary key downloads are administrator-only (PKG-DEB-018); client tooling depends on
  the former.
- Snapshot URLs live under the format endpoint, unlike the normal APT source that points at the
  plain file URL (PKG-DEB-025).
- Coordinates on cached remote packages are missing until the coordinate calculation runs
  (PKG-DEB-022); copying uncalculated packages to a local repository leaves them unindexed.
- Credentials embedded in source lines end up in world-readable files unless the file mode is
  restricted; client setup snippets should say so.
