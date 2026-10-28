# Opkg package format

This page catalogs what a repository manager does to serve `.ipk` packages to the `opkg` client
used on embedded Linux systems (OpenWrt, Yocto-based distributions): feeds at arbitrary paths,
automatic `Packages` index calculation per feed in local repositories, GPG signing of the indexes,
proxying public feeds in remote repositories, and client setup (there is no virtual Opkg
repository, PKG-OPKG-001). The feed format is defined by the opkg project
([opkg documentation](https://openwrt.org/docs/guide-user/additional-software/opkg)); where an
entry relies on client behavior, it says so. Throughout this page, `<repo-url>` stands for the URL
under which a repository's files are served by path and `https://repo.example.com` for the
system's base URL; the concrete path layout is the implementation's choice. `opkg-local` and `opkg-remote` are example repository keys.

## Repositories and feeds

### PKG-OPKG-001 · Opkg repositories
Priority: **P2**

Opkg is a repository format for local and remote repositories:

- local repositories hold an organization's own `.ipk` packages; the system computes the feed
  indexes (PKG-OPKG-003);
- remote repositories proxy and cache public feeds such as `downloads.openwrt.org`
  (PKG-OPKG-006);
- there is no virtual Opkg repository: virtual is not supported for Opkg and the virtual
  configuration schema does not accept the format (FMT-001 in [README.md](README.md)). Open: either
  it stays that way, or virtual Opkg repositories are offered and clients address them by a key
  such as one ending in `-virtual`; this catalog follows the first.

High availability, cloud object storage and replication also work for Opkg repositories and need
no format-specific behavior. Priority resolution (REPO-018 in
[../repositories.md](../repositories.md)) does not apply to Opkg repositories.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the opkg format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-OPKG-002 · Feeds at arbitrary paths
Priority: **P2**

A *feed* is a folder that contains `.ipk` packages and their index. A repository can hold any
number of feeds at any path depth, for example `my-distro/1.0/i386/` and
`my-distro/1.0/armv7a/`; the client lists each feed URL separately (PKG-OPKG-008). No coordinates
or properties are needed at deploy time; the feed is the folder the package is stored in.

## Metadata and signing

### PKG-OPKG-003 · Automatic index calculation per feed
Priority: **P2**

For local repositories the system computes the index of every feed location from the `.ipk`
packages in it, so no manual indexing is needed.

- Output per feed: `Packages` and a compressed variant `Packages.gz` (the client's `src/gz` form
  reads the compressed one), plus `Release` metadata as named by the recalculation operation. The
  exact file set and the compression formats beyond gzip are unspecified.
- After a deploy, the index is recalculated asynchronously; it takes about one minute until the
  package appears.
- Signature files are added when a signing key is available (PKG-OPKG-005).

### PKG-OPKG-004 · Recalculate Opkg metadata
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Recalculate Opkg metadata | repository key; run asynchronously (yes/no, default no); write properties (0 or 1) | plain-text confirmation | unspecified (answers 401/403) |

- Recomputes the `Packages` and `Release` metadata of every feed location of a local repository.
- Supported by local repositories only. Synchronous by default.
- The write-properties flag mirrors the Debian operation (PKG-DEB-016 in [debian.md](debian.md));
  which properties are written for `.ipk` packages is unspecified.
- Open: Opkg recalculation is either the Debian recalculation operation or a separate Opkg
  operation; this page follows the separate operation.
- UI: the "recalculate index" row action on the repository list (REPO-009 in
  [../repositories.md](../repositories.md)).

### PKG-OPKG-005 · Index signing with GPG
Priority: **P2**

The system signs the feed indexes with a GPG key; it does not sign individual `.ipk` packages.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Primary signing key | local | GPG key pair that signs the indexes. | unspecified (optional) |
| Secondary signing key | local | Standby key for rotation. | unspecified (optional) |

- Key assignment, rotation and the public-key download work as for Debian (PKG-DEB-018 in
  [debian.md](debian.md)); the instance-wide default GPG key (PKG-DEB-019) also covers Opkg.
- The name of the signature file is unspecified. With `check_signature` on, opkg
  downloads a detached signature `Packages.sig` next to `Packages` (client behavior).
- Keys come from the central key store (IAM-046 in
  [../identity-and-access.md](../identity-and-access.md)).

## Remote repositories

### PKG-OPKG-006 · Remote Opkg repositories
Priority: **P2**

A remote Opkg repository proxies a feed host and caches what clients request (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)). Its upstream URL can be either:

- one specific feed, for example
  `http://downloads.openwrt.org/chaos_calmer/15.05/adm5120/rb1xx/packages/luci`; or
- a base folder, for example `http://downloads.openwrt.org/chaos_calmer/15.05/`, with the
  individual feeds named as sub-paths in the client configuration.

Index caching follows the metadata retrieval cache period (RMT-018); that this applies to Opkg
specifically is inferred.

| Setting | Meaning | Default |
|---|---|---|
| List remote artifacts | Lets users browse the upstream feed host's content in the artifact browser before anything is cached (ART-044 in [../artifacts.md](../artifacts.md)). | off |

## Deploying and client setup

### PKG-OPKG-007 · Deploy Opkg packages
Priority: **P2**

```sh
curl -u <user>:<token> -X PUT \
  "https://repo.example.com/opkg-local/feeds/armv7a/my-app.ipk" -T my-app.ipk
```

- Plain HTTP `PUT` of the `.ipk` file to `<repo-url>/<feed-path>/<file>.ipk`.
- Required permission: deploy on the target path (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- UI: the deploy dialog of the artifact browser (UI-013 in
  [../ui-inventory.md](../ui-inventory.md)); the uploaded package shows up in the browser and index
  after about a minute.

### PKG-OPKG-008 · opkg client configuration
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) add feeds
and credentials to `/etc/opkg/opkg.conf`:

```text
src/gz internal-feed https://repo.example.com/opkg-local/my-distro/1.0/armv7a
option http_auth <user>:<token>
option check_signature true
```

- `src <name> <url>` reads the plain `Packages` index; `src/gz <name> <url>` reads `Packages.gz`.
- `option http_auth <user>:<password>` sends HTTP Basic credentials; the password part can be an
  access token (TOK-034 in [../access-tokens.md](../access-tokens.md)) or an encrypted password
  (IAM-035 in [../identity-and-access.md](../identity-and-access.md)). The option is `http_auth`
  as opkg defines it, not `https_auth`.
- Signature verification needs the repository's public GPG key in the opkg keyring
  (`opkg-key add <key-file>`; for opkg 0.4 and later the folder `/etc/opkg/keys` must exist first)
  and `option check_signature true`. If the key's trust level is too low, opkg fails with "No
  sufficiently trusted public keys found" and "Signature verification failed"; raising the key's
  trust level fixes it.
- There is no single opkg release; the client is built from source with GPG and curl support
  enabled (`--enable-gpg --enable-curl`), which signature checks and HTTPS authentication need.
- Install: `opkg update`, then `opkg install <name>`.

## Notes for implementers

- A feed is any folder with `.ipk` files; index every such folder of a local repository, at any
  depth (PKG-OPKG-002).
- Serve both `Packages` and `Packages.gz`; clients choose by `src` versus `src/gz`.
- Clients with `check_signature` fetch the signature next to the index; a missing signature file
  fails the whole feed, not just one package.
- Opkg repositories do not take part in priority resolution (PKG-OPKG-001).
- Index recalculation after deploy is asynchronous (about a minute), so a deploy
  followed immediately by `opkg update` can miss the package.
- Re-sign every feed index when the repository's primary key changes, otherwise clients that
  trust only the new key reject the old signatures.
