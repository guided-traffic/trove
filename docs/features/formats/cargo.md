# Cargo (Rust crates) format

This page catalogs what a repository manager does to act as a Cargo registry for Rust crates:
hosting crates in local repositories behind a sparse HTTP index, caching `crates.io` or another
instance in remote repositories, the Cargo web API for publish, yank and search, index
recalculation, client configuration and authentication, and the reasons the format has no virtual
or federated repositories. The registry protocol is defined by the Cargo project in
[Registries](https://doc.rust-lang.org/cargo/reference/registries.html),
[Index Format](https://doc.rust-lang.org/cargo/reference/registry-index.html) and
[Web API](https://doc.rust-lang.org/cargo/reference/registry-web-api.html); where an entry states
protocol behavior, it links that source. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/cargo/<repo-key>` stands for the base URL of one
Cargo repository and `sparse+https://repo.example.com/cargo/<repo-key>/index/` for its index URL
(the path layout is the implementation's choice), `https://repo.example.com/files/<repo-key>/<path>`
stands for raw file access to a repository, and `cargo-local` and `cargo-remote` are example
repository keys.

## Repositories and addressing

### PKG-CARGO-001 · Cargo repositories: local and remote only
Priority: **P2**

Cargo is supported as a local and a remote repository format; there are no virtual Cargo
repositories (REPO-006 in [../repositories.md](../repositories.md)).

Open: federation for Cargo is either unsupported, with a client-side workaround
(PKG-CARGO-013), or supported: the index-migration rule names federated Cargo repositories,
federation excludes only one format (the Terraform state backend), and the federated form has
Cargo-specific fields (PKG-CARGO-003, PKG-CARGO-005). The spec decides. A reading consistent
with both: federated Cargo repositories can be created and mirror content, but a crate published
on one member keeps that member's index URL, so consumers of other members need source
replacement.

- Local repositories store the organization's crates and compute their index (PKG-CARGO-002).
- Remote repositories cache `crates.io` or a Cargo repository on another instance
  (PKG-CARGO-010, PKG-CARGO-011).
- Why no virtual repositories: each index entry names, per dependency, the registry the dependency
  comes from (Cargo's `registry` field), and the published `.crate` carries the same information.
  A virtual repository would have to rewrite those references to point at itself, which changes
  the crate checksum that clients verify; Cargo offers no client-side way to remap every registry
  for all users. Workarounds are client-side source replacement (PKG-CARGO-013).

Prerequisites for creating a Cargo repository: the system administrator or a project administrator
role, and a configured external base URL of the system (OPS-051 in
[../operations-and-monitoring.md](../operations-and-monitoring.md); UI-044 in
[../ui-inventory.md](../ui-inventory.md)). Presumably the base URL is needed because the index
configuration file publishes absolute download and API URLs (PKG-CARGO-002).

UI location: administration, repositories, create repository, local or remote, Cargo format
(UI-031).

Supported clients: Cargo 1.49.0 and later; the sparse index needs Cargo 1.60 or later, and Cargo
1.60 to 1.67 offer it only as a nightly feature that must be enabled with `-Z sparse-registry` on
every command (sparse is stable from 1.68).

### PKG-CARGO-002 · Sparse registry index of a repository
Priority: **P2**

Every Cargo repository serves a sparse HTTP index: plain files fetched one per HTTP request, no Git
clone involved. Defined by the Cargo project
([Index Format](https://doc.rust-lang.org/cargo/reference/registry-index.html)):

| Request, relative to the index URL | Response |
|---|---|
| `config.json` | JSON with `dl` (download URL template), `api` (web API base URL, no trailing slash; without it `cargo publish` does not work) and `auth-required` (all requests need a token) |
| `1/<name>`, `2/<name>`, `3/<c>/<name>`, `<ab>/<cd>/<name>` | Index file of one crate: one JSON object per line and per version. The directory is derived from the lowercase name: 1- and 2-character names in `1/` and `2/`, 3-character names under `3/<first char>/`, longer names under `<first two>/<third and fourth>/` (`cargo` is `ca/rg/cargo`) |

`dl` may contain the markers `{crate}`, `{version}`, `{prefix}`, `{lowerprefix}` and
`{sha256-checksum}`; without markers, Cargo appends `/{crate}/{version}/download`.

<details>
<summary>Index entry (one line per version)</summary>

| Field | Meaning |
|---|---|
| `name` | Crate name, case preserved |
| `vers` | SemVer 2.0 version |
| `deps` | Dependencies, each with `name`, `req`, `features`, `optional`, `default_features`, `target`, `kind` (`normal`, `dev`, `build`), `registry` (index URL of another registry, or null for the same registry), `package` (original name when renamed) |
| `cksum` | SHA-256 of the `.crate` file |
| `features`, `features2` | Feature map; `features2` holds the extended syntax and requires `v` ≥ 2 |
| `yanked` | Yank flag; the only field that may change after the line is written |
| `links` | Native library link name or null |
| `v` | Entry schema version, 1 when absent |
| `rust_version` | Minimum supported Rust version, optional |
| `pubtime` | Original publish time, `yyyy-mm-ddThh:mm:ssZ`, optional |

</details>

Protocol rules a compatible registry follows:

- Each version appears once per crate, ignoring SemVer build metadata (`1.0.7` and `1.0.7+extra`
  collide).
- Nonexistent crates answer `404`, `410` or `451`.
- Cargo caches index files and revalidates with `If-None-Match` (ETag preferred) or
  `If-Modified-Since`; `304` saves the transfer.
- Authentication: Cargo first requests `config.json`; on `401` it retries with its token. The
  `401` may carry a `WWW-Authenticate` challenge with a `login_url` telling the user where to get
  a token. Registries that require authentication set `auth-required: true`; Cargo then also
  sends the `Authorization` header on crate downloads.
- Suggested crate-name restrictions (from crates.io's practice): ASCII alphanumerics, `-` and `_`,
  first character alphabetic, case-insensitive and `-`/`_`-insensitive collision checks, at most
  64 characters, no reserved names such as `nul`. Which restrictions the system enforces is
  unspecified.

The crates themselves are stored at `crates/<name>/<name>-<version>.crate` inside the repository
(PKG-CARGO-007).

### PKG-CARGO-003 · Sparse index setting and the retired Git index
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Sparse index support | local, remote, federated | Serve the repository's index as a sparse HTTP index | unspecified for local and remote; off in the federated form |

UI location: repository form, basic tab, general settings section. Also listed in REPO-032 of
[../repositories.md](../repositories.md).

- The Git-based index is retired. The system refuses to create a Cargo repository, or to save
  changes to one, while it is set to the Git index.
- A repository still on the Git index is migrated by switching sparse index support on and then
  recalculating the index (PKG-CARGO-012). Before that, `cargo publish`, `cargo search` and
  similar client commands against it fail.
- A repository created with the sparse index needs no initialization time, because no Git clone
  is built in the background.
- The sparse index also makes it possible to route remote-repository traffic through an outbound
  proxy (RMT-016 in [../remote-repositories.md](../remote-repositories.md)).

## Client setup and authentication

### PKG-CARGO-004 · Cargo client configuration
Priority: **P2**

Registry definition in `$CARGO_HOME/config.toml` (default `~/.cargo/config.toml`):

```toml
[registry]
default = "company"
global-credential-providers = ["cargo:token"]

[registries.company]
index = "sparse+https://repo.example.com/cargo/cargo-local/index/"
```

Token in `$CARGO_HOME/credentials.toml`, used for API requests such as `publish` and `yank`:

```toml
[registries.company]
token = "<token>"
```

To route all `crates.io` dependencies through a remote repository, Cargo's
[source replacement](https://doc.rust-lang.org/cargo/reference/source-replacement.html) is used:

```toml
[source.crates-io]
replace-with = "company-remote"

[source.company-remote]
registry = "sparse+https://repo.example.com/cargo/cargo-remote/index/"
```

- The token can also be passed per command (`cargo publish --token "<token>"`) or through the
  environment variable `CARGO_REGISTRIES_<NAME>_TOKEN` (requires the `cargo:token` provider).
- Token values the system accepts in Cargo's `Authorization` header: a token as is, a
  Base64-encoded `user:password`, and (as written by the companion client, PKG-CARGO-014)
  `Bearer <token>` or `Basic <base64(user:password)>`. Cargo sends the stored value verbatim.
- Cargo requires a credential provider for authenticated registries; with none configured it
  refuses to send tokens ([Registry Authentication](https://doc.rust-lang.org/cargo/reference/registry-authentication.html)).
- Install and resolve: `cargo install <crate>` or `cargo build`; `--registry <name>` selects a
  non-default registry.
- The client setup dialog generates both snippets with the user's token (UI-021 and UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

Security: the `cargo:token` provider stores the token unencrypted in `credentials.toml`; the
operating-system providers (`cargo:libsecret`, `cargo:macos-keychain`, `cargo:wincred`) avoid that.

### PKG-CARGO-005 · Anonymous download and search for older clients
Priority: **P2**

Cargo clients without sparse-index support (version 1.67 and earlier, Git-index mode) send no
credentials on `install` and `search`. For repositories that otherwise require authentication,
those two operations then fail.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Allow anonymous download and search | local, remote, federated | Lets unauthenticated clients download crates and search this repository; it overrides the system-wide anonymous-access switch (REPO-028 in [../repositories.md](../repositories.md)) | unspecified for local and remote; off in the federated form |

Edge cases: the setting may be required for Cargo 1.67 and earlier. Its exact scope (index files,
crate downloads, search API) is unspecified beyond "download and search".

Security: turning it on exposes every crate of the repository to anonymous readers, independent of
the global anonymous-access setting (IAM-005 in
[../identity-and-access.md](../identity-and-access.md)).

## Publishing and lifecycle

### PKG-CARGO-006 · Publish through the Cargo web API
Priority: **P2**

`cargo publish --registry <name>` uploads a crate to a local repository. Defined by the Cargo
project ([Web API: publish](https://doc.rust-lang.org/cargo/reference/registry-web-api.html#publish)):

- `PUT <api>/api/v1/crates/new`, `Content-Type: application/octet-stream`, `Authorization`
  header with the token.
- Body: 32-bit little-endian length of a JSON metadata document, the document, 32-bit
  little-endian length of the `.crate` file, the file.
- The metadata uses the publish field names (`vers`, `deps[].version_req`,
  `deps[].explicit_name_in_toml` and so on), which differ from the index field names; the
  registry computes `cksum` itself, and the publish document carries extra fields (description,
  readme, license, keywords) that do not go into the index.
- Success is `2xx` with an optional `warnings` object; failures carry
  `{"errors": [{"detail": "…"}]}`, which Cargo shows to the user.
- The index need not be updated before the response; Cargo polls the index for a short time and
  warns if the version does not appear.

The system stores the crate at `crates/<name>/<name>-<version>.crate`, adds the version to the
index and makes it downloadable.

Required privilege: deploy permission on the local repository.

Edge cases: re-publishing an existing version must be refused (index uniqueness, PKG-CARGO-002);
the error code the system uses is unspecified.

### PKG-CARGO-007 · Deploy a crate file by path
Priority: **P2**

Besides `cargo publish`, a crate can be uploaded as a plain file, through the generic deploy
operation (ART-002 in [../artifacts.md](../artifacts.md)) or the UI deploy dialog (UI-013 in
[../ui-inventory.md](../ui-inventory.md)):

```shell
curl -u "<user>:<token>" -T hello-1.0.0.crate \
  "https://repo.example.com/files/cargo-local/crates/hello/hello-1.0.0.crate"
```

- The target path must be exactly `crates/<name>/<name>-<version>.crate`. Anything else is stored
  but not recognized as a crate and not indexed.
- `cargo publish` is recommended instead, because path deploys are the usual source of indexing
  errors.

Edge cases: how the system derives index metadata (dependencies, features) from a path-deployed
crate is unspecified; it has to read the `Cargo.toml` inside the archive.

### PKG-CARGO-008 · Yank and unyank
Priority: **P2**

Yanking removes a version from dependency resolution for new lockfiles without deleting the crate;
existing lockfiles keep working. Supported for local repositories.

| Operation | Client command | Protocol request ([Web API](https://doc.rust-lang.org/cargo/reference/registry-web-api.html#yank)) | Effect |
|---|---|---|---|
| Yank | `cargo yank <crate> --version <v> --token "<token>"` | `DELETE <api>/api/v1/crates/<crate>/<v>/yank` | `yanked: true` in the index line; response `{"ok": true}` |
| Unyank | `cargo yank <crate> --version <v> --undo` | `PUT <api>/api/v1/crates/<crate>/<v>/unyank` | `yanked: false`; response `{"ok": true}` |

(`--vers` is an accepted spelling of `--version`.)

Interactions: the yank state reaches chained remote repositories and replication targets only when
property synchronization is on (RMT-042 in [../remote-repositories.md](../remote-repositories.md);
replication REPL-001 in [../replication-and-federation.md](../replication-and-federation.md)). This implies
the yank state is kept as an item property; the property name is unspecified.

Required privilege: unspecified (the protocol only requires a valid token).

### PKG-CARGO-009 · Cargo web API surface
Priority: **P2**

The Cargo web API defines more endpoints than publish and yank. Support in the system:

| Endpoint (Cargo protocol) | Purpose | Support |
|---|---|---|
| `PUT /api/v1/crates/new` | Publish | yes (PKG-CARGO-006) |
| `DELETE /api/v1/crates/{crate}/{version}/yank`, `PUT …/unyank` | Yank, unyank | yes (PKG-CARGO-008) |
| `GET /api/v1/crates?q=<query>&per_page=<n>` | Search; `per_page` default 10, max 100; response `crates[]` with `name`, `max_version`, `description`, plus `meta.total` | implied: `cargo search` works on sparse-index repositories (PKG-CARGO-003) |
| `GET`/`PUT`/`DELETE /api/v1/crates/{crate}/owners` | List, add, remove owners | unspecified |
| `/me` | URL shown by `cargo login` | unspecified |

Every request carries the token in `Authorization`; an invalid token should yield `403`. Unknown
query parameters and JSON fields must be ignored.

## Remote repositories

### PKG-CARGO-010 · Remote repository for crates.io
Priority: **P2**

A Cargo remote repository caches crates and index files from an upstream registry. It has two
upstream addresses:

| Setting | Meaning | Value for crates.io |
|---|---|---|
| Upstream URL | Base address the remote fetches from (RMT-004) | `https://index.crates.io` |
| Registry URL | Address of the upstream's index | `https://index.crates.io` |

UI location: remote repository form, basic tab. Defaults are unspecified.

- Crates are cached on first request (RMT-001 in
  [../remote-repositories.md](../remote-repositories.md)); the sparse index is required for remote
  repositories as well (PKG-CARGO-003).
- Clients use the remote either as a named registry or, more commonly, as the replacement for
  `crates-io` (PKG-CARGO-004).

Edge cases: how the remote rewrites the upstream's `config.json` (its `dl` and `api` values must
point at the remote repository, not at crates.io) is unspecified. Index files of crates.io name
dependencies from crates.io with a null `registry` ("same registry"); served unchanged through a
remote they resolve against the remote itself, which suits both the named-registry and the
source-replacement setup (an inference from the Cargo index format).

### PKG-CARGO-011 · Chained Cargo remote
Priority: **P2**

A Cargo remote can point at a Cargo repository on another instance of the system (RMT-040 in
[../remote-repositories.md](../remote-repositories.md)). The two URLs then differ:

| Setting | Value |
|---|---|
| Upstream URL | The raw repository address of the source repository on the other instance (its plain file URL, not the Cargo protocol URL) |
| Registry URL | The source repository's sparse index address on the other instance (the address clients would put after `sparse+`) |

Interactions: yank state propagates only with property synchronization (PKG-CARGO-008).

## Index maintenance

### PKG-CARGO-012 · Recalculate the Cargo index
Priority: **P2**

Rebuilds the package and version metadata (the index) of a Cargo repository.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate Cargo metadata | repository key; flag "run asynchronously" | `200` with a plain-text message; `401`, `403`, `500` on failure | unspecified for the API operation; the UI action requires an administrator |

- Supported for local and remote repositories.
- UI location: artifact browser tree, context menu of the repository, "recalculate index" (UI-011
  in [../ui-inventory.md](../ui-inventory.md); REPO-009 in
  [../repositories.md](../repositories.md)).
- Needed after migrating from the Git index (PKG-CARGO-003).

Open: the recalculation either runs synchronously by default with an opt-in asynchronous mode,
or is always asynchronous; the spec decides. Recalculating a local repository also indexes the
remote repositories; which remotes and why is unspecified.

## Federation

### PKG-CARGO-013 · Source replacement across federated members
Priority: **Later**

Because index entries and crates carry the registry URL of each dependency (PKG-CARGO-001), a
crate published to one federation member references that member's index URL. The system cannot
change those URLs without changing checksums, so virtual repositories and federation are
unsupported for Cargo (for the open federation question see PKG-CARGO-001). The workaround is
client-side [source replacement](https://doc.rust-lang.org/cargo/reference/source-replacement.html): on every
developer machine, each other member's index is declared as a source that is replaced by the
member the client uses, so dependencies recorded against any member resolve against one. For a
client that uses member A (`replace-with` may name a registry from the `[registries]` table):

```toml
[registries.member-a]
index = "sparse+https://repo.example.com/cargo/cargo-member-a/index/"

[source.member-b]
registry = "sparse+https://repo.example.com/cargo/cargo-member-b/index/"
replace-with = "member-a"
```

- A configuration may declare the replacement in both directions (A replaced by B and B by A)
  in one file; the Cargo documentation does not say how such a mutual declaration is resolved.
- Cargo assumes a replacement source holds exactly the same crates as the source it replaces, so
  the workaround is only sound when the members are fully synchronized.

Interactions: federation in [../replication-and-federation.md](../replication-and-federation.md).

## Companion client

### PKG-CARGO-014 · Companion client setup and build metadata for Cargo
Priority: **P2**

The companion command-line client (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) offers a
setup command and a wrapper for Cargo.

<details>
<summary>Setup command and wrapper behavior</summary>

Setup command, run once per environment:

| Input | Meaning | Default |
|---|---|---|
| Connection profile | Server profile to use (BLD-035) | the default profile |
| Remote repository | Remote Cargo repository used for resolution; when given, no publish registry is configured | interactive selection |
| Project | Project used to list repositories interactively | none |

- Writes into `$CARGO_HOME/config.toml`: a registry for resolution made the default, a source
  replacement of `crates-io` by that registry, `global-credential-providers = ["cargo:token"]`, and,
  when a local repository is chosen interactively, a second registry for publishing.
- Writes into `$CARGO_HOME/credentials.toml` (file mode `0600`) the token as
  `Bearer <access token>` or `Basic <base64(user:password)>`, depending on the profile.
- Merges into existing files, keeps unrelated keys, overwrites its own entries on re-run; a
  resolution-only re-run removes a previously written publish registry and its token.
- After token expiry, Cargo fails with `401` until the profile is refreshed and setup re-run.

Wrapper:

- Runs `cargo` with all native arguments; the wrapper's own options (build name and number,
  module, project, connection profile) may appear anywhere and are stripped before the call.
- Credential injection happens only for `build`, `install` and `publish`: each registry in the
  Cargo configuration whose index URL is served by the selected server (same host and base path)
  gets `CARGO_REGISTRIES_<NAME>_TOKEN` and the `cargo:token` provider. Every other subcommand runs
  with whatever credentials Cargo already has.
- With build name and number (flags or environment), `build` and `install` record dependencies,
  `publish` records dependencies and the published `.crate` (from `target/package`, or looked up on
  the server through the query language when Cargo deleted the file; see
  [../search-and-query.md](../search-and-query.md)). One build module per crate, identified as
  `<crate-name>:<version>`; `-p` limits which workspace members become modules.
- Build name without number, or the reverse, is an error. The wrapper never uploads the build
  record itself (BLD-039).
- The transparent command interception (BLD-042) does not cover `cargo`.

</details>

## Notes for implementers

- Cargo sends the stored token verbatim as the `Authorization` value; accept a bare token as well
  as `Bearer` and `Basic` forms, and never require a scheme prefix.
- Authentication on the sparse index starts with an unauthenticated `config.json`; answer `401`
  there (not `403` or `404`) so Cargo retries with its token, and set `auth-required: true` so it
  also authenticates crate downloads.
- `config.json` must carry absolute `dl` and `api` URLs that point back at the serving repository;
  a wrong external base URL breaks publish and download.
- Index files are append-mostly: lines must not change after they are written, except `yanked`.
  Serve correct `ETag`/`Last-Modified` and honor conditional requests; Cargo refreshes index files
  on most commands.
- The publish JSON and the index JSON use different field names (`version_req` vs `req`,
  `explicit_name_in_toml` vs `package`, renamed-dependency name swapped); translate, do not copy.
- A dependency's `registry` is null for "same registry"; when serving a remote, keep null for
  crates.io dependencies and translate the current index URL to null.
- No virtual repositories: lockfiles pin the index URL, so each Cargo repository URL a client uses
  is permanent for that project.
- Path-deployed crates are indexed only at `crates/<name>/<name>-<version>.crate`.
