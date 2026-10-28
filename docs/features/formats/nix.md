# Nix package format

This page catalogs what a repository manager does for the Nix package manager: acting as an HTTP
binary cache (substituter) for build outputs in local, remote and virtual repositories, serving and
publishing Nix channels, signing cache entries with Ed25519 keys, client setup and authentication,
package identification for browsing, and build-metadata collection by the companion command-line
client. The binary-cache format is defined by the Nix project
([binary cache](https://nix.dev/manual/nix/latest/protocols/binary-cache/),
[`.narinfo`](https://nix.dev/manual/nix/latest/protocols/binary-cache/narinfo.html),
[`nix-cache-info`](https://nix.dev/manual/nix/latest/protocols/binary-cache/nix-cache-info.html));
where an entry relies on those documents or on the [Nix sources](https://github.com/NixOS/nix), it
says so. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/nix/<repo-key>` stands for the binary-cache URL of
one repository (the path layout is the implementation's choice), `nix-local`, `nix-remote` and
`nix-virtual` are example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-NIX-001 · Nix repositories in all repository classes
Priority: **P2**

Nix is supported as a local, remote, virtual and federated repository format:

- local repositories host an organization's own build outputs as a private binary cache and can
  sign them (PKG-NIX-006, PKG-NIX-009);
- remote repositories proxy public Nix infrastructure and other instances (PKG-NIX-016);
- virtual repositories aggregate members for one client configuration (PKG-NIX-017);
- federated repositories synchronize Nix content across sites (PKG-NIX-018);
- the system calculates package metadata from cache entries for the package list and search
  (PKG-NIX-014);
- channels can be proxied and published (PKG-NIX-007, PKG-NIX-008).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the Nix format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)). The class-support matrix
(REPO-006 in [../repositories.md](../repositories.md)) does not list Nix. Scanner indexing
(REPO-021) is unspecified.

### PKG-NIX-002 · Binary-cache URL of a repository
Priority: **P2**

Every Nix repository serves the binary-cache format under its *binary-cache URL*: clients list it
as a substituter (PKG-NIX-011) and as the target of `nix copy --to` (PKG-NIX-006). Channels live
under a channel sub-path of the same repository (PKG-NIX-007); the sub-path layout is the
implementation's choice.

## Binary-cache protocol

### PKG-NIX-003 · Cache information document
Priority: **P2**

`GET <binary-cache-url>/nix-cache-info` returns a line-oriented `Key: value` document
(`text/x-nix-cache-info`, Nix documentation):

| Key | Meaning |
|---|---|
| `StoreDir` | Store directory the cache was built for, normally `/nix/store`; a client with another store directory refuses the cache |
| `WantMassQuery` | `1` or `0`; default for whether the client may query many paths at once |
| `Priority` | Default substituter priority; lower is preferred (PKG-NIX-011) |

Clients cache this document for seven days. When uploading to a cache that lacks it, Nix writes one
(Nix sources); the system may answer such a write or provide the document itself. Which values the
system serves is unspecified.

### PKG-NIX-004 · Store-object metadata
Priority: **P2**

`GET <binary-cache-url>/<hash>.narinfo`, where `<hash>` is the hash part of a store path, returns
the store object's metadata as `Key: value` lines (Nix documentation):

| Key | Meaning |
|---|---|
| `StorePath` | Full store path |
| `URL` | Usually relative URL of the compressed NAR (PKG-NIX-005) |
| `Compression` | `xz`, `bzip2` (default when absent), `zstd`, `none` and so on |
| `FileHash`, `FileSize` | Hash and size of the compressed file |
| `NarHash`, `NarSize` | Hash and size of the uncompressed NAR |
| `References` | Space-separated store-path base names this object depends on |
| `Deriver` | Derivation that built it |
| `Sig` | Signature `<key-name>:<base64>`; may repeat (PKG-NIX-009) |
| `CA` | Content address, for content-addressed objects |

- A missing `.narinfo` must answer 404 (Nix treats 403 like 404 for this lookup, Nix sources); the
  client then builds locally or asks the next substituter.
- Clients probe many `.narinfo` files per build; response time of misses dominates substitution
  speed.

### PKG-NIX-005 · NAR archives
Priority: **P2**

The file named by a `.narinfo`'s `URL` (commonly `nar/<file-hash>.nar.xz`) is the possibly
compressed Nix Archive of the store object. The companion client's build metadata records
`.nar.xz` and `.narinfo` files as the artifacts of an upload (PKG-NIX-019), which confirms xz as
the compression used in practice; other compressions are unspecified.

### PKG-NIX-006 · Upload build outputs with `nix copy`
Priority: **P2**

```shell
nix copy --to "https://<user>:${TOKEN}@repo.example.com/nix/nix-local/" ./result
# or copy the outputs of an expression file:
nix copy --to "https://<user>:${TOKEN}@repo.example.com/nix/nix-local/" --file default.nix
```

- Nix checks existence with `HEAD` and uploads with `PUT`, relative to the target URL: first the
  NAR to `nar/<file-hash>.nar.<ext>` (`application/x-nix-nar`), then `<hash>.narinfo`
  (`text/x-nix-narinfo`) (Nix sources). The system must accept `PUT` of both file kinds at the
  paths Nix chooses.
- The modern command line must be enabled: `experimental-features = nix-command` in `nix.conf`
  (`nix-command flakes` for flake workflows).
- Targets: local repositories; for a virtual repository, the companion client rewrites the target
  to the virtual repository's default deployment repository and adds `--refresh` (PKG-NIX-019).
  Whether a plain `nix copy` to a virtual URL works is unspecified.
- When the repository has a signing key, the system signs the uploaded entries itself
  (PKG-NIX-009); clients need no signing flags.
- Required privilege: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

## Channels

### PKG-NIX-007 · Channel resolution
Priority: **P2**

A Nix channel provides a pinned snapshot of Nix expressions (for example Nixpkgs). Proxying and
publishing channels gives an organization stable, rollback-capable package sets even when upstream
channels move or disappear.

```shell
nix-channel --add "https://<user>:${TOKEN}@repo.example.com/nix/nix-remote/<channel-sub-path>/nixpkgs-25.11-darwin" 25.11-darwin
nix-channel --update
```

- For a remote repository the channel name must exactly match an official channel name of the
  [NixOS channel registry](https://channels.nixos.org/).
- Client behavior (Nix sources, `nix-channel`): the client first downloads the channel URL itself
  following redirects and continues from the final URL; it then requests `nixexprs.tar.zst`,
  `nixexprs.tar.xz` and `nixexprs.tar.bz2` below that URL, taking the first that exists. A channel
  URL that redirects to a concrete snapshot therefore pins that snapshot for the update.
- How the system maps a channel name to the latest published snapshot of PKG-NIX-008 is
  unspecified.

### PKG-NIX-008 · Publish a channel expression snapshot
Priority: **P2**

A channel snapshot is uploaded as one file with a plain `PUT`:

```shell
curl -X PUT -u "user:${TOKEN}" -T nixexprs.tar.xz \
  "https://repo.example.com/nix/nix-local/<channel-sub-path>/nixpkgs-25.11/nixpkgs/nixpkgs-25.11pre893393"
```

| Path element | Meaning |
|---|---|
| Channel name | For example `nixpkgs-25.11` |
| Flavor | `nixpkgs` or `nixos` |
| Snapshot identifier | For example `nixpkgs-25.11pre893393` |

- The uploaded file must be named `nixexprs.tar.xz`; the system enforces the name because the client
  looks for exactly that file.
- Required privilege: deploy on the repository.

## Signing

### PKG-NIX-009 · Ed25519 signing of cache entries
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Ed25519 signing key (primary) | Key pair from the central key store (IAM-046 in [../identity-and-access.md](../identity-and-access.md)) used to sign the repository's cache entries | unspecified; signing starts once a key is assigned |

- Applies to local repositories. Once a key is assigned, the system signs entries automatically
  when they are deployed or downloaded; no client flag or separate publish step is needed.
- Key setup: generate an Ed25519 key pair outside the system (for example with OpenSSL), add it in
  the key store as an Ed25519 key with a name and an alias, then select it on the repository's
  basic tab.
- The alias is the key name inside the signature (`Sig: <alias>:<base64>`) and must equal the name
  clients trust (PKG-NIX-010).
- What Nix verifies (Nix sources): an Ed25519 signature over the fingerprint
  `1;<store-path>;<nar-hash>;<nar-size>;<comma-separated full store paths of references>`. The
  system must compute exactly this string from the `.narinfo` fields.
- Prerequisite privilege: system or project administrator for keys and repository settings.

### PKG-NIX-010 · Client trust configuration
Priority: **P2**

```ini
extra-trusted-public-keys = my-nix-cache-1:cNpfNfHQEZ+rzOkvB985IKlWFT4nxl+G3HwxV6t92EY=
require-sigs = true
```

- `<alias>:<base64 public key>`; the alias is the one of PKG-NIX-009.
- With `require-sigs = true`, Nix accepts substitutes only when a trusted key signed them.
- The same block appears in the client setup instructions (UI-021 and UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).
- After editing a system-wide `nix.conf`, reload the Nix daemon (on macOS
  `sudo launchctl kickstart -k system/org.nixos.nix-daemon`).

## Client setup and authentication

### PKG-NIX-011 · Substituter configuration and priority
Priority: **P2**

```ini
# ~/.config/nix/nix.conf (user) or /etc/nix/nix.conf (system-wide, needs root)
substituters = https://<user>:${TOKEN}@repo.example.com/nix/nix-virtual?priority=20
extra-trusted-public-keys = my-nix-cache-1:<base64 public key>
require-sigs = true
experimental-features = nix-command
```

- `priority`: Nix queries substituters in priority order, lower first; the public
  `cache.nixos.org` has priority 40, so a value below 40 makes the system the first choice.
- Virtual repositories are the way to cover several repositories with one client
  configuration, so a single substituter entry can serve local and remote content.

### PKG-NIX-012 · Authentication methods
Priority: **P2**

The Nix client never prompts for credentials.

| Method | Use | Notes |
|---|---|---|
| Credentials in the URL | Substituters, `nix copy` targets, channel URLs | Recommended method: `https://<user>:${TOKEN}@host/…` |
| `.netrc` | Binary cache and file downloads | `~/.netrc`; multi-user installations ignore a client-specified `netrc-file` unless the user is trusted, so URL credentials are the fallback |
| `access-tokens` option | Fetchers such as `fetchTarball` or `fetchurl` against Git hosting APIs (for example `github.com=…`, `gitlab.com=…`) | Does not authenticate against the binary cache |

Tokens are used as Basic passwords (TOK-034 in [../access-tokens.md](../access-tokens.md)). When
Nix runs through the companion command-line client (PKG-NIX-019), the client may supply a
temporary netrc built from its own configured server connection.

### PKG-NIX-013 · Installing packages
Priority: **P2**

With substituter and channel configured, standard Nix commands resolve through the system:

| Method | Example |
|---|---|
| `nix-env` | `nix-env -iA nixpkgs.neovim` |
| `nix-shell` | `nix-shell -p neovim` |
| NixOS configuration | `environment.systemPackages = [ pkgs.neovim ];` in `/etc/nixos/configuration.nix` |

## Package identification

### PKG-NIX-014 · Package identification from store paths
Priority: **P2**

For the package list and search (ART-048 and ART-049 in [../artifacts.md](../artifacts.md)), the
system reads `StorePath` from each `.narinfo` and extracts the name and version encoded in it.

- Cache entries are grouped by extracted name and version rather than by store hash: every rebuild
  produces a new hash, so hash-based packages would multiply without limit and make browsing by
  logical package impossible.
- The package view is for discovery; it does not replace Nix's own identity and reproducibility
  guarantees.

| Limit | Effect |
|---|---|
| Free-form versions | Versions may hold dates, channel or branch names or Git identifiers; ordering may not reflect age or function, and "latest" is best effort |
| Duplicate or ambiguous versions | Builds with the same extracted version are aggregated without further distinction |
| Toolchain prefixes | Extracted names may carry prefixes added by language or toolchain builders |
| Overridden package names | Derivations that override the package name during evaluation can leave intermediate names in the store path, so the shown name may differ from the attribute users install |

### PKG-NIX-015 · Naming and versioning guidance
Priority: **P2**

Guidance for publishers to improve aggregation: stable explicit package names
without toolchain or runtime identifiers; clean, predictable, preferably semantic versions without
branch names, dates or commit hashes; build metadata kept separately; consistent names across
releases.

## Remote, virtual and federated repositories

### PKG-NIX-016 · Remote Nix repositories
Priority: **P2**

A remote Nix repository proxies Nix infrastructure and other instances (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)) and caches what clients request, so
builds keep working when the upstream is unavailable.

- The upstream named for remote Nix repositories, `search.nixos.org`, is a package search front
  end, while the public binary cache is `https://cache.nixos.org` and channels are served from
  `https://channels.nixos.org`. The default upstream URL and how a remote reaches both cache and
  channels are unspecified.
- Only pulled or deployed entries appear in the artifact browser; remote registries cannot be
  browsed.
- Signing applies to local repositories; whether a remote passes upstream signatures through
  unchanged is unspecified. Clients that trust `cache.nixos.org` keys can verify unchanged upstream
  entries.

### PKG-NIX-017 · Virtual Nix repositories
Priority: **P2**

A virtual repository aggregates local and remote Nix repositories behind one substituter URL
(REPO-033 in [../repositories.md](../repositories.md)). `.narinfo` lookups resolve in member order
(REPO-035); uploads go to the default deployment repository (REPO-038). Which `nix-cache-info` a
virtual repository serves is unspecified.

### PKG-NIX-018 · Federated Nix repositories
Priority: **Later**

Nix repositories can be federated to synchronize content across geographic sites (REPL-018 in
[../replication-and-federation.md](../replication-and-federation.md)). Whether signatures are
created per site or replicated is unspecified.

## Build metadata

### PKG-NIX-019 · Build metadata for Nix through the companion client
Priority: **P2**

The companion command-line client wraps native Nix commands and, when given a build name and number
together, records build metadata (BLD-039 and BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)).

| Wrapped command | Build metadata |
|---|---|
| `nix-env`, `nix-build`, `nix build` | Dependencies: the runtime closure from `nix path-info`, without the output root; scope `runtime` |
| `nix copy` | Artifacts: the uploaded `.nar.xz` and `.narinfo` files; build properties applied to the artifacts of the closure |
| `nix-channel`, other `nix` subcommands such as `flake lock` | Passed through, nothing recorded |

- Build name and build number must be given together; one without the other is an error, neither
  runs Nix without recording. An optional module name overrides the default module, and an
  optional project key and server profile select the target. The client strips all of these
  options before invoking Nix.
- Module type `nix`; checksums are the `narHash` values, resolved from the system's cache when
  missing locally, which requires uploading with `nix copy` before publishing the run.
- `nix-build` output paths are read from standard output; `nix build` (flakes) reads the `./result`
  link. Flake workflows need all flake files committed to Git.
- Uploading to a virtual repository: the client resolves the default deployment repository,
  rewrites the target URL to it and adds `--refresh`.
- The client writes no Nix configuration; substituters and channels must be configured first
  (PKG-NIX-011, PKG-NIX-007). CI setups write `nix.conf` from secrets.
- The client's interception mode for other package managers does not cover Nix (BLD-042).

## Limits

### PKG-NIX-020 · Limitations
Priority: **P2**

| Limit | Detail |
|---|---|
| Remote browsing | Not supported; only pulled or deployed entries are shown |
| Authentication | No interactive prompt; URL credentials or `.netrc` (PKG-NIX-012) |
| Version extraction | Best effort (PKG-NIX-014) |
| Signing | Local repositories only (PKG-NIX-009) |

## Notes for implementers

- `.narinfo` misses are the hot path: a single evaluation can probe thousands of hashes, so a miss
  must be cheap, cacheable as negative (RMT-020 in
  [../remote-repositories.md](../remote-repositories.md)) and answered 404.
- The signature fingerprint uses full store paths for `References`, while the `References` line in
  the `.narinfo` holds base names; mixing them up yields signatures Nix rejects.
- The fingerprint renders the NAR hash in Nix32 form (`sha256:<nix32>`); a signer that formats it
  in hex or base64 produces signatures Nix rejects.
- Channel updates follow redirects and use the final URL as base for `nixexprs.tar.*`; a channel URL
  that serves content instead of redirecting must still answer `nixexprs.tar.xz` below itself.
- `nix copy` uploads the NAR before the `.narinfo`; a `.narinfo` must not become visible before its
  NAR exists, or concurrent clients fetch a dangling reference.
- Tokens embedded in substituter URLs sit in plain text in `nix.conf` (the system-wide file is
  commonly readable by all local users); prefer short-lived tokens for CI and `.netrc` where the
  daemon allows it.
- `StoreDir` mismatches make Nix ignore the whole cache; a virtual repository must not merge caches
  built for different store directories.
