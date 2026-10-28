# Conda package format

This page catalogs what a repository manager does to act as a conda channel: hosting conda
packages, whatever language they were built from (Python and R most often, but also C/C++,
Fortran, Java and others), in local repositories, proxying and caching an upstream channel in remote repositories, aggregating
both behind one channel URL in virtual repositories, computing the channel index, serving the
sharded index of [CEP 16](https://conda.org/learn/ceps/cep-0016/), and configuring the conda client.
The channel format itself is defined by the conda project (see the
[conda documentation](https://docs.conda.io/projects/conda/en/stable/) and the
[conda enhancement proposals](https://conda.org/learn/ceps/)); where an entry relies on those
sources rather than on the described system, it links them. Repository classes in general are
described in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/conda/<repo-key>` stands for the *channel URL* of
one repository (the path layout is the implementation's choice), `conda-local`, `conda-remote` and
`conda-virtual` are example repository keys, `ci-user` is an example user name and `${TOKEN}` an
access token.

## Repositories and addressing

### PKG-CONDA-001 · Conda repositories in all three repository classes
Priority: **P2**

Conda is offered as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's own packages with fine-grained access control; the
  system computes their index (PKG-CONDA-005);
- remote repositories proxy and cache an upstream channel (PKG-CONDA-011);
- virtual repositories aggregate local and remote members under one channel URL and keep the
  aggregated index current (PKG-CONDA-012).

Supported client: conda 4.12.0 or later; the latest client is recommended.

Creating a repository requires the system administrator or a project administrator role; for
other users the create action is not offered. UI location: administration, repositories, create
repository, then the class and the conda format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).
A local repository needs a key; a remote repository a key and a confirmed upstream URL (its
pre-filled value is unspecified); a virtual repository a key and its members (UI-032).

### PKG-CONDA-002 · Channel URL, platform subdirectories and sub-channels
Priority: **P2**

Every conda repository is a conda channel under its channel URL. Packages live in platform
subdirectories (`noarch`, `linux-64`, `osx-arm64`, `win-64` and so on), each with its own index
(PKG-CONDA-008):

```text
https://repo.example.com/conda/conda-local/linux-64/acme-2.3.1-py311_0.tar.bz2
https://repo.example.com/conda/conda-local/main/linux-64/acme-2.3.1-py311_0.tar.bz2   # sub-channel "main"
```

- A *sub-channel* is a path prefix inside one repository: a package deployed to
  `main/linux-64/…` belongs to the channel `<channel URL>/main`.
- Clients select a channel or sub-channel by full URL or by name relative to the configured channel
  alias: `conda install -c https://repo.example.com/conda/conda-virtual pandas`.
- Unspecified: whether sub-channels are supported in remote and virtual repositories, how deep they
  may nest, and whether a virtual repository merges same-named sub-channels of its members.

### PKG-CONDA-003 · Client authentication
Priority: **P2**

The conda client sends credentials embedded in the channel URL as HTTP Basic:

```yaml
channels:
  - https://ci-user:${TOKEN}@repo.example.com/conda/conda-virtual
```

- The user name must be URL-encoded (an `@` in the user name becomes `%40`).
- Client setup uses an identity token (TOK-005 in
  [../access-tokens.md](../access-tokens.md)) as the password (TOK-034).
- Anonymous reads work when anonymous access is switched on (IAM-005 in
  [../identity-and-access.md](../identity-and-access.md)).
- `ssl_verify: false` in `.condarc` turns off certificate validation on the client; client setup
  offers it as an option.
- Unspecified: Bearer tokens, and conda's token-in-path form (`/t/<token>/…`) used by some public
  channel hosts.

Security: credentials in channel URLs are stored in plain text in `.condarc`, and with
`ssl_verify: false` they travel to whoever can intercept the connection; a read-only token scoped to
the channel limits what a leaked credential grants.

## Deploying packages

### PKG-CONDA-004 · Deploy a package to its subdirectory path
Priority: **P2**

Conda packages reach a local repository through a plain file upload to the path that encodes the
platform subdirectory, optionally prefixed by a sub-channel:

```shell
curl -u ci-user:${TOKEN} -T ./linux-64/acme-2.3.1-py311_0.tar.bz2 \
  "https://repo.example.com/files/conda-local/main/linux-64/acme-2.3.1-py311_0.tar.bz2"
```

| Input | Meaning |
|---|---|
| Target path | `<subdir>/<package file>` or `<sub-channel>/<subdir>/<package file>`; the system derives the index location from it |
| Package file | a conda package, for example `.tar.bz2` |

- UI: artifact browser, deploy (ART-006, UI-013): choose the conda repository as target, upload,
  tick the option that treats the file as a conda package, and make sure the target path reflects
  the subdirectory.
- The URL above uses the generic file address of the repository (ART-002 in
  [../artifacts.md](../artifacts.md)); a conda-specific upload protocol (such as the one of
  public channel hosts' upload clients) is unspecified.
- Required permission: deploy on the repository.
- Unspecified: whether `.conda` (the newer zip-based package format) files are indexed, and what
  happens when the path's subdirectory disagrees with the package's own `info/index.json`.
- A UI warning against changing a source-package target path below `src/contrib` does not apply
  to conda: that path belongs to CRAN (PKG-CRAN-004 in [cran.md](cran.md)).

## Index and metadata

### PKG-CONDA-005 · Automatic index calculation
Priority: **P2**

The system recalculates the conda index of a local repository automatically whenever a conda
package is added, removed, copied or moved.

- The calculation runs asynchronously after the triggering action has completed, driven by
  storage events; it can take up to about 30 s, depending on system load.
- During that window clients may see an index that lacks the new package or still lists a removed
  one.
- A manual full recalculation is rarely needed (PKG-CONDA-006).

### PKG-CONDA-006 · Recalculate conda metadata
Priority: **P2**

| | |
|---|---|
| Inputs | Repository key; optional flag to run asynchronously. |
| Behavior | Recomputes the conda package and channel index of the repository. Synchronous by default. |
| Output | HTTP 200 with a plain-text body. |
| Errors | 401, 403, 500. |
| Repository classes | Local only. |
| Required privilege | Unspecified (the operation can answer 403). |

- UI: artifact browser, context menu of the repository node, "recalculate index"; also the
  repository row action of REPO-009 in [../repositories.md](../repositories.md). The UI path runs
  asynchronously.
- Duration: up to a few hours for large repositories, depending on number and size of packages.
- After enabling sharding (PKG-CONDA-009), local repositories must be recalculated once.

### PKG-CONDA-007 · Index calculation worker pool
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Conda index workers | Instance-wide number of worker threads that calculate conda indexes in parallel. | 5 |

An instance-level configuration property, not a UI or API setting; raising it is recommended at
larger scale.

### PKG-CONDA-008 · Channel index documents
Priority: **P2**

Each platform subdirectory of every conda repository serves the index documents conda reads:

| Document | Content |
|---|---|
| `<subdir>/repodata.json` | every package of the subdirectory with its dependencies, checksums and size |
| `<subdir>/current_repodata.json` | a reduced index with the newest versions, which some conda versions try first |

- Both stay available whatever the sharding setting (PKG-CONDA-009).
- The structure of these documents is conda's (`info`, `packages`, `packages.conda`, `removed`,
  `repodata_version`); which keys the system writes, and whether it serves compressed variants or a
  channel-level `channeldata.json`, is unspecified.

### PKG-CONDA-009 · Sharded channel index (CEP 16)
Priority: **P2**

An alternative to the single `repodata.json`, defined by
[CEP 16](https://conda.org/learn/ceps/cep-0016/): the client downloads a small shard index and then
only the shards of the packages it needs.

| Setting | Meaning | Default |
|---|---|---|
| Conda shards | Instance-wide. Serves the shard index and package shards for local, remote and virtual conda repositories. | off |
| Show sharding in client setup | Instance-wide. Adds the sharding configuration to the client setup instructions (PKG-CONDA-014). | unspecified |

| Path | Content |
|---|---|
| `<subdir>/repodata_shards.msgpack.zst` | shard index: per package name the SHA-256 of its shard |
| `<subdir>/<sha256>.msgpack.zst` | one package's records, addressed by content hash |

- Local repositories: every deploy or removal of a package produces or drops that package's
  shard from its metadata, and the shard index is patched rather than rebuilt. Packages that existed before sharding was
  switched on need a recalculation (PKG-CONDA-006).
- Remote repositories: the upstream's shard index and shards are fetched and cached in an internal
  area of the remote cache. Not every upstream channel offers shards; whether the system builds
  shards for an upstream without them is unspecified.
- Virtual repositories: the shard indexes of all members are merged.
- With the setting off, both shard paths answer 404; the classic index (PKG-CONDA-008) is unaffected.
- Client: `.condarc` needs `repodata_use_shards: true` and `solver: libmamba`.
- UI location: administration, package-format settings, conda section, checkbox. Required role:
  system administrator or project administrator (the rule for package settings in general).

### PKG-CONDA-010 · Package information and metadata search
Priority: **P2**

- UI: in the artifact browser (ART-040 in [../artifacts.md](../artifacts.md)), selecting a conda
  package file shows selected conda metadata; the fields are unspecified.
- The package search of the UI offers conda-specific platform and architecture fields (SRCH-050 in
  [../search-and-query.md](../search-and-query.md)); which stored properties back them is
  unspecified.
- Package cleanup policies treat `.tar.bz2` as the conda lead file (RET-009 in
  [../retention-and-cleanup.md](../retention-and-cleanup.md)).

## Remote repositories

### PKG-CONDA-011 · Proxying an upstream channel
Priority: **P2**

A conda remote repository fetches index documents and packages from an upstream channel (a public
channel or another instance) and caches them (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | RMT-004; the channel to proxy. | unspecified |
| Metadata retrieval cache period | RMT-018; the index files are kept and renewed according to it. | 7,200 s |

- Packages, once cached, are served from the cache; index files expire after the period.
- Shards: PKG-CONDA-009.
- Unspecified: how package URLs inside an upstream index are mapped to the remote repository, and
  whether sub-channel paths are passed through to the upstream.

## Virtual repositories

### PKG-CONDA-012 · Aggregated channel index
Priority: **P2**

A conda virtual repository serves one index per subdirectory that aggregates its members.

| Setting | Meaning | Default |
|---|---|---|
| Metadata cache period (virtual) | How long the aggregated index of remote members is kept before it is renewed; it is the virtual repository's general metadata cache period (REPO-037 in [../repositories.md](../repositories.md)). | open: either 600 s (10 minutes) or 7,200 s (the REPO-037 value); the spec decides |

- Changes in local members are reflected in the aggregated index immediately.
- For remote members the aggregated index is renewed on demand, at most once per cache period.
- Shard indexes are merged as well (PKG-CONDA-009).
- Merge rules (member order, the same package file in two members, priority resolution REPO-018)
  are unspecified for conda.
- Deploying through a virtual repository: unspecified.

## Clients

### PKG-CONDA-013 · conda client configuration
Priority: **P2**

`.condarc` in the user's home directory (created if absent):

```yaml
channel_alias: https://ci-user:${TOKEN}@repo.example.com/conda/conda-virtual
channels:
  - https://ci-user:${TOKEN}@repo.example.com/conda/conda-virtual
default_channels:
  - https://ci-user:${TOKEN}@repo.example.com/conda/conda-virtual
# optional
ssl_verify: false
# with sharding (PKG-CONDA-009)
repodata_use_shards: true
solver: libmamba
```

| Task | Command |
|---|---|
| Install from the configured channels | `conda install numpy` |
| Install from a specific channel or sub-channel | `conda install -c https://repo.example.com/conda/conda-virtual pandas` |

Pointing `channel_alias`, `channels` and `default_channels` at the system keeps conda from reaching
its built-in public channels. Compatibility with Mamba and other conda-compatible clients is
unspecified.

### PKG-CONDA-014 · Client setup instructions in the UI
Priority: **P2**

The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) generates the
`.condarc` snippet of PKG-CONDA-013 with the system's URL, the chosen repository and a token filled
in. The sharding lines are included only when the corresponding instance setting is on
(PKG-CONDA-009).

## Protocol reference

This section summarizes what a conda client requests from a channel, from the
[conda documentation](https://docs.conda.io/projects/conda/en/stable/), the
[conda source](https://github.com/conda/conda) and [CEP 16](https://conda.org/learn/ceps/cep-0016/).
It is the yardstick for client compatibility; which optional parts the described system serves is
unspecified unless an entry above says so. Paths are relative to the channel URL
(PKG-CONDA-002), or to a sub-channel below it.

### PKG-CONDA-015 · Channel endpoints
Priority: **P2**

<details>
<summary>Requests a conda client makes</summary>

| Request | Answer | Notes |
|---|---|---|
| `GET <subdir>/repodata.json` | the subdirectory's index | `<subdir>` is the client's platform plus `noarch`; conda treats a 404 for `noarch/repodata.json` as an invalid channel, while a 404 for a platform subdirectory counts as empty. |
| `GET <subdir>/current_repodata.json` | reduced index | tried first by some conda versions; a 404 makes them fall back to `repodata.json`. |
| Compressed variants of `repodata.json` | the same index, compressed | newer clients may probe them; a fast 404 lets them fall back. |
| `GET <subdir>/repodata_shards.msgpack.zst` | shard index | sharding only (PKG-CONDA-009). |
| `GET <subdir>/<sha256>.msgpack.zst` | one package shard | sharding only. |
| `GET <subdir>/<package file>` | the package | `.tar.bz2` or `.conda`. |
| Conditional requests (`If-None-Match`, `If-Modified-Since`) | 304 when unchanged | conda caches index documents locally and revalidates them. |

</details>

Index record keys a client needs per package: `name`, `version`, `build`, `build_number`,
`depends`, `subdir`, `md5`, `sha256`, `size`, and optionally `constrains`, `license`, `timestamp`,
`noarch`; `.tar.bz2` files are listed under `packages`, `.conda` files under `packages.conda`.

## Notes for implementers

- Always serve `noarch/repodata.json`, even empty (`{"info": {"subdir": "noarch"}, "packages": {},
  "packages.conda": {}}`); without it conda rejects the whole channel.
- A 404 for a platform subdirectory is acceptable and means "no packages"; do not answer 401 or 500
  there, or conda aborts the solve.
- List `.conda` files under `packages.conda` and `.tar.bz2` files under `packages`; many current
  upstream channels publish `.conda` only.
- The path of an uploaded file decides its subdirectory; validate it against the package's own
  `info/index.json` before indexing, or a mislabeled package breaks installs on another platform.
- Index updates are asynchronous (up to about 30 s); CI that publishes and immediately installs needs
  to wait or poll.
- Support conditional requests and stable `ETag` values on index documents; conda revalidates them on
  every solve.
- Shard file names are content hashes; a shard must never change under the same name, and the shard
  index must only reference shards that already exist.
- With credentials in the channel URL, every package URL the client builds carries them; keep all
  package links relative to, or under, the channel URL.
</content>
</invoke>
