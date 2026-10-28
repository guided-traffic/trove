# RPM package format

This page catalogs what a repository manager does to act as a YUM/DNF repository for RPM-based
Linux distributions: hosting RPM packages in local repositories with automatically computed
`repodata` metadata (at a configurable folder depth, with optional file lists, group files and
module metadata), signing that metadata with GPG keys, proxying public mirrors in remote
repositories, merging both in virtual repositories, and client setup for YUM and DNF. The
metadata format is the one produced by the `createrepo` tool
([createrepo](http://createrepo.baseurl.org/)) and read by YUM and DNF; where an entry relies on
client documentation, it says so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path and
`https://repo.example.com` for the system's base URL; the concrete path layout is the
implementation's choice. `rpm-local`, `rpm-remote` and `rpm-virtual` are example repository keys.

## Repositories and metadata

### PKG-RPM-001 · RPM repositories in all three repository classes
Priority: **P2**

RPM is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's own RPMs; the system computes their metadata
  (PKG-RPM-003);
- remote repositories proxy and cache a public mirror (PKG-RPM-014);
- virtual repositories merge local and remote RPM repositories behind one `baseurl`
  (PKG-RPM-015).

Supported clients: YUM 4.18.2 and later, DNF 4.19.2 and later. DNF is the primary client and YUM
is legacy. Both read repository definitions from `/etc/yum.repos.d/`.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the RPM format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-RPM-002 · Metadata folder depth
Priority: **P2**

Decides at which directory level the system looks for RPMs and where it writes `repodata/`, so
one repository can contain several independent YUM repositories.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Metadata folder depth | local | Directory level below the repository root at which metadata is computed. 0: the whole repository is one YUM repository with `repodata/` at the root. 1: every first-level folder becomes its own YUM repository with its own `repodata/` (for a layout `<name>/<version>/<arch>/<file>.rpm`, one YUM repository per package name). | 0 |

- Clients point their `baseurl` at the folder that contains `repodata/`, i.e. the repository
  root for depth 0 and `<repo-url>/<folder>` for larger depths.
- Changing the depth of an existing repository does not move metadata: packages indexed at the old
  depth may need re-indexing (PKG-RPM-006) or moving, and clients must change their `baseurl`.

UI location: repository settings, basic tab, RPM settings section.

### PKG-RPM-003 · Automatic metadata calculation
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Calculate RPM metadata automatically | local | Recomputes the metadata at the configured depth whenever relevant content changes. | unspecified (example value: off) |

Triggers when on:

1. deploying, deleting, copying or moving an RPM file;
2. importing content (system-wide or repository import).

Manual triggers are always available (PKG-RPM-006). The metadata produced either way is what YUM
and DNF clients receive.

Edge case: a calculation removes RPM metadata that existed before it as a result of manual deploys
or imports, including metadata stored as SQLite database files. A repository that relied on
hand-made `repodata/` content loses it on the first calculation.

### PKG-RPM-004 · Generated repodata files
Priority: **P2**

The metadata is equivalent to the basic-mode output of `createrepo`. It is computed in-process,
without the `createrepo` binary or any other external program on the host.

| File in `repodata/` | Content |
|---|---|
| `primary.xml.gz` | Primary metadata of every RPM (name, version, dependencies and so on). |
| `filelists.xml.gz` | Every file contained in every RPM. Its content depends on file-list indexing (PKG-RPM-005); whether the file is omitted or written without entries while indexing is off is unspecified. |
| `other.xml.gz` | Miscellaneous per-package information. |
| `repomd.xml` | Index of the other metadata files with their checksums (SHA-1 or SHA-256, PKG-RPM-010); also references group and module metadata (PKG-RPM-007, PKG-RPM-008). |

Signature files are described in PKG-RPM-012.

### PKG-RPM-005 · File-list indexing
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Index file lists | local | Also computes `filelists.xml`. | off |

- Rationale for the default: in repositories with many packages, recomputing `filelists.xml` on
  every client-driven change is expensive.
- With it off, clients cannot find which package provides a given file (for example
  `dnf provides /usr/bin/foo`).
- A virtual repository's `filelists.xml` is incomplete when any member has file-list indexing off.

UI location: repository settings, basic tab, RPM settings section.

### PKG-RPM-006 · Recalculate YUM metadata
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Recalculate YUM metadata | repository key; path of the `repodata` folder (for virtual repositories); run asynchronously (yes/no, default no) | plain-text confirmation | unspecified (answers 401/403) |

- Supported by local, virtual and federated repositories. For a local repository it recomputes
  the metadata from the RPMs it holds; for a virtual repository it recomputes the merged metadata
  at the given path.
- Synchronous by default.
- UI: the "recalculate index" row action on the repository list (REPO-009 in
  [../repositories.md](../repositories.md)).

### PKG-RPM-007 · YUM group files
Priority: **P2**

A YUM group bundles RPMs for one purpose (for example "Development Tools"); clients install it with
`dnf group install <id>` or `yum groupinstall <id>`. The system attaches group files to the
metadata, like `createrepo -g`.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Group file names | local | Comma-separated list of group XML file names that the calculation attaches as group information. | unspecified (optional) |

Behavior when a group XML file is deployed into a `repodata/` folder:

1. the system creates the gzip-compressed copy next to it;
2. it starts a metadata calculation of the repository;
3. it references both the XML and the `.gz` file from `repomd.xml`.

Limits and edge cases:

- each `repodata/` folder holds at most one group file; several groups are separate elements inside
  that one XML file; different depth folders may use different group file names;
- only files whose names are listed in the setting are attached;
- the group install and group update commands require the group to be deployed to the root of
  the local repository.

Group files can be produced by hand or with `yum-groups-manager` from `yum-utils`.

### PKG-RPM-008 · Module metadata
Priority: **P2**

For modular repositories, a `modules.yaml` file listing the desired modules is deployed into the
`repodata/` folder; a subsequent metadata calculation adds it to `repomd.xml` and the other
metadata. Whether deploying `modules.yaml` triggers the calculation by itself (as group files do)
is unspecified; the deploy is followed by an explicit index calculation (PKG-RPM-006).

### PKG-RPM-009 · Weak-dependency tags
Priority: **P2**

RPMs can declare optional ("weak") dependencies with the `Recommends` tag; DNF and YUM install
them by default.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Write Recommends tags | instance-wide, RPM package settings (PKG-RPM-011) | When on, `Recommends` entries are included in `primary.xml`, so clients install weak dependencies; when off, clients get only the requested packages. | unspecified |

Turning it on increases transfer volume because clients pull recommended packages by default.
Clients can still opt out (`--setopt=install_weak_deps=False` or `install_weak_deps=False` in
`dnf.conf` / `yum.conf`).

### PKG-RPM-010 · SHA-256 checksums in repomd.xml
Priority: **P2**

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| SHA-256 checksums in repomd.xml | instance-wide, RPM package settings (PKG-RPM-011) | Off: `repomd.xml` lists only SHA-1 checksums for the metadata files. On: it lists only SHA-256 checksums (`type="sha256"` entries) for each metadata file. | off |

The system keeps SHA-256 checksums of packages internally in both modes. The setting applies to
local repositories; its effect on virtual metadata is unspecified.

### PKG-RPM-011 · RPM package settings screen
Priority: **P2**

An administrator screen (administration, settings, package settings, RPM section) with the
instance-wide switches of PKG-RPM-009 and PKG-RPM-010.

- Each switch can also be set in the system's startup configuration. When it is set there (to on
  or off), the UI checkbox is disabled and shows that value, so one value is enforced for the whole
  instance. When it is not set there, the UI controls it.
- Required privilege: administrator.

## Signing

### PKG-RPM-012 · Metadata signing with GPG
Priority: **P2**

The system signs the repository metadata with a GPG key, for verification by YUM and DNF; it does
not sign individual RPMs.

- Clients verify the metadata signature with `repo_gpgcheck=1` and fetch the public key from the
  URL in `gpgkey`. The client setup snippet (PKG-RPM-017) points `gpgkey` at
  `<baseurl>/repomd.xml.key`, so the system serves the repository's public key under that name.
- The name of the signature file is unspecified. DNF and YUM look for a detached
  ASCII-armored signature `repodata/repomd.xml.asc` next to `repomd.xml` (client documentation).
- Package signatures (`gpgcheck=1`) come from the RPMs themselves, signed before upload; the system
  does not add them.
- Key selection and rotation: PKG-RPM-013.

### PKG-RPM-013 · Signing keys for RPM repositories
Priority: **P2**

Local RPM repositories use the same primary and secondary GPG key assignment as Debian
repositories, with the same operations and rotation procedure (PKG-DEB-018 in
[debian.md](debian.md)), and the instance-wide default GPG key (PKG-DEB-019) as the older
single-key mechanism. Keys come from the central key store (IAM-046 in
[../identity-and-access.md](../identity-and-access.md)).

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Primary signing key | local | GPG key pair that signs `repomd.xml`. | unspecified (optional) |
| Secondary signing key | local | Standby key for rotation. | unspecified (optional) |

Virtual RPM repositories: the virtual configuration schema carries the two key fields without
restricting them to a format, but the per-format virtual settings list them only for Debian.
Whether a virtual RPM repository signs its merged `repomd.xml`, and with which key, is
unspecified.

## Remote and virtual repositories

### PKG-RPM-014 · Remote RPM repositories
Priority: **P2**

A remote RPM repository proxies a YUM/DNF mirror and needs no special configuration for it
(RMT-001 in [../remote-repositories.md](../remote-repositories.md)). Its only format-specific
setting:

| Setting | Meaning | Default |
|---|---|---|
| List remote artifacts | Lets users browse the upstream mirror's content in the artifact browser before anything is cached (ART-044 in [../artifacts.md](../artifacts.md)). | off |

- The upstream URL is the folder that contains the upstream `repodata/` folder, for example
  `https://mirror.example.org/centos/7/os/x86_64/`.
- `repomd.xml` and the metadata files are cached and refreshed according to the metadata retrieval
  cache period (RMT-018); RPMs are cached on first request.
- In the artifact browser, the cache of a remote repository is shown under the repository key with
  a cache suffix (RMT-003).
- The client setup instructions give the `baseurl` to use (UI-021 in
  [../ui-inventory.md](../ui-inventory.md)).

Edge case: end-of-life distribution releases move from the regular mirrors to
archive ("vault") mirrors, so the upstream URL has to be changed when a release reaches end of
life.

### PKG-RPM-015 · Virtual RPM repositories
Priority: **P2**

A virtual RPM repository merges the metadata of its local and remote members behind one
`baseurl` (REPO-033, REPO-037 in [../repositories.md](../repositories.md)).

- Merged metadata can be recalculated for a given `repodata` path (PKG-RPM-006).
- Signing of the merged metadata is unspecified (PKG-RPM-013).
- Not supported: YUM and DNF commands that use `updateinfo.xml` (errata/advisory commands such as
  `dnf updateinfo`).
- `filelists.xml` is incomplete if any member has file-list indexing off (PKG-RPM-005).
- Merge rules for the same package version in several members are unspecified (general rules:
  REPO-035).

## Deploying and client setup

### PKG-RPM-016 · Deploy RPM packages
Priority: **P2**

- An RPM is deployed with a plain HTTP `PUT` to a path inside a local repository, below the
  folder that holds the metadata for the configured depth:
  `PUT <repo-url>/<path-under-metadata-root>/<file>.rpm`. No coordinates or properties are
  required; the metadata is computed from the RPM header.
- Deploy by checksum (ART-004) and deploy of an archive that is extracted on arrival (ART-005) work
  as for any artifact ([../artifacts.md](../artifacts.md)).
- UI: the deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md)).
- Required permission: deploy on the target path (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-RPM-017 · YUM and DNF client configuration
Priority: **P2**

The client setup instructions render a `.repo` file for `/etc/yum.repos.d/`:

```ini
[internal]
name=internal
baseurl=https://<user>:<token>@repo.example.com/rpm-virtual/<path-to-repodata-folder>
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://<user>:<token>@repo.example.com/rpm-virtual/<path-to-repodata-folder>/repomd.xml.key
```

- `baseurl` points at the repository root for depth 0, otherwise at the folder that contains
  `repodata/` (PKG-RPM-002).
- Unsigned repositories use `gpgcheck=0` and omit `repo_gpgcheck` and `gpgkey`.
- The recommended `baseurl` is a virtual repository.
- Commands that work: `dnf install <name>` (optionally `<name>-<version>`),
  `dnf search`, `dnf list available`, `dnf upgrade`, the YUM equivalents, and the group commands of
  PKG-RPM-007.
- YUM/DNF variables (`$releasever`, `$arch`, `$basearch`) can be used in `baseurl` to share one
  `.repo` file across hosts; `$basearch` is the base architecture (`i386` for `i686`).
- UI: client setup dialog, RPM instructions (UI-021, UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

### PKG-RPM-018 · Client authentication
Priority: **P2**

- YUM and DNF send HTTP Basic credentials embedded in `baseurl` and `gpgkey`; the password part can
  be an access token (TOK-034 in [../access-tokens.md](../access-tokens.md)).
- Anonymous read works when anonymous access is enabled (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)).
- Clients behind an outbound proxy set `proxy`, `proxy_username` and `proxy_password` in
  `yum.conf` / `dnf.conf`.
- Clients can present a TLS client certificate (`sslclientcert`, `sslclientkey`, with
  `sslverify=1`) where the server requires mutual TLS (AUTHN-034).

## Notes for implementers

- `baseurl` must point at the parent of `repodata/`; with a metadata depth above 0 the repository
  root is not a valid YUM repository.
- The first automatic or manual calculation deletes pre-existing hand-made metadata, including
  SQLite metadata databases (PKG-RPM-003).
- Group files must be referenced from `repomd.xml` together with their gzip copy, and each
  `repodata/` folder supports one group file only (PKG-RPM-007).
- `repomd.xml` lists either SHA-1 or SHA-256 checksums, not both (PKG-RPM-010); older clients may
  depend on SHA-1, so the switch is instance-wide and off by default.
- `filelists.xml` is off by default; `dnf provides <path>` silently finds nothing until it is
  turned on (PKG-RPM-005).
- Serve the public key at `<baseurl>/repomd.xml.key`, the location the client snippet uses
  (PKG-RPM-012), and the signature where DNF expects it (`repodata/repomd.xml.asc`); a missing
  signature with `repo_gpgcheck=1` makes the client refuse the repository.
- Virtual repositories do not serve `updateinfo.xml`; advisory-based updates fail there
  (PKG-RPM-015).
- Re-sign the metadata in the same step that rewrites `repomd.xml`; clients cache `repomd.xml` and
  compare checksums, so a mismatch between metadata and signature breaks every client.
- Changing the metadata depth or moving packages invalidates client `baseurl` values
  (PKG-RPM-002).
