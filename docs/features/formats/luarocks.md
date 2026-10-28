# LuaRocks package format

This page catalogs what a repository manager does to act as a rocks server for LuaRocks, the
package manager of the Lua language: hosting rocks and rockspecs in local repositories with an
automatically maintained manifest, proxying `luarocks.org` in remote repositories, aggregating both
in virtual repositories, chained and federated variants, the upload protocol of `luarocks upload`,
client configuration and authentication. LuaRocks defines no formal server specification; where an
entry relies on the [LuaRocks documentation](https://github.com/luarocks/luarocks/wiki) or the
[LuaRocks client sources](https://github.com/luarocks/luarocks), it says so. Repository classes
in general are described in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/luarocks/<repo-key>` stands for the rocks-server
URL of one repository (the path layout is the implementation's choice), `luarocks-local`,
`luarocks-remote` and `luarocks-virtual` are example repository keys, and `${TOKEN}` is an access
token.

## Repositories and addressing

### PKG-LUA-001 · LuaRocks repositories in all repository classes
Priority: **P2**

LuaRocks is supported as a local, remote, virtual, chained remote and federated repository format
(REPO-006 in [../repositories.md](../repositories.md)):

- local repositories hold proprietary Lua modules under fine-grained access control; the system
  maintains the manifest the client needs and extracts package metadata for browsing and search
  (PKG-LUA-010, PKG-LUA-012);
- remote repositories proxy the public LuaRocks registry or another instance (PKG-LUA-013,
  PKG-LUA-014);
- federated repositories synchronize packages across instances (PKG-LUA-015);
- virtual repositories expose all of them under one URL (PKG-LUA-016);
- cleanup and archive policies apply (PKG-LUA-017);
- the native `luarocks` client configures, uploads and installs.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the LuaRocks format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)). Scanner indexing (REPO-021) is unspecified
for LuaRocks.

### PKG-LUA-002 · Rocks-server URL
Priority: **P2**

LuaRocks calls any directory reachable over HTTP that holds rocks, rockspecs and manifest files a
rocks server. Every LuaRocks repository serves one under its *rocks-server URL*, which clients
list in `rocks_servers` (PKG-LUA-006) and pass to `--only-server` or the upload configuration
(PKG-LUA-008). Rocks, rockspecs and manifests sit directly at the root of that URL; no other
layout is specified.

## Protocol

### PKG-LUA-003 · Manifest files
Priority: **P2**

The client resolves packages from a manifest: a Lua table file listing every rock, its versions
and architectures, and its dependencies. Requested in this order (LuaRocks client sources,
`luarocks/manif.lua`), the first one found wins:

| File at the rocks-server root | Note |
|---|---|
| `manifest-<lua-version>.json` | Tried first only by LuaJIT-based clients |
| `manifest-<lua-version>.zip` | Zipped manifest for one Lua version, for example `manifest-5.4.zip` |
| `manifest-<lua-version>` | Manifest for one Lua version |
| `manifest` | Version-independent manifest; not requested when the client asks for versioned manifests only |

- The system serves `manifest` and `manifest-<version>` files and maintains them so
  `luarocks-admin make-manifest` is never run by hand; which of the zipped and JSON variants it
  serves is unspecified.
- The client caches manifests locally; the cache lifetime is the client's.

### PKG-LUA-004 · Rocks, rockspecs and package identity
Priority: **P2**

| File | Meaning |
|---|---|
| `<package>-<version>-<revision>.rockspec` | Build recipe (Lua table); portable |
| `<package>-<version>-<revision>.src.rock` | Source rock: rockspec plus sources; portable |
| `<package>-<version>-<revision>.<arch>.rock` | Binary rock for one platform, for example `linux-x86_64`; must match the client's architecture |

- The system extracts name, version and revision from file names and rockspec content.
- Version strings carry a revision suffix (`1.0.2-1`). Package views show the extracted name and
  version; the client resolves on the full rock identity including revision.
- Rocks and rockspecs must be uploaded to the repository root; the system computes metadata and
  the manifest from the files there. Whether files in subfolders are ignored or rejected is
  unspecified.

### PKG-LUA-005 · Upload protocol
Priority: **P2**

`luarocks upload` speaks the upload API of `luarocks.org`. The flow below is taken from the
LuaRocks client sources (`luarocks/upload/api.lua`, `luarocks/cmd/upload.lua`).

| Step | Request | Purpose |
|---|---|---|
| 1 | `GET <server>/api/tool_version?current=<client-tool-version>` | Returns JSON with `version`; the client warns on a mismatch |
| 2 | `GET <server>/api/1/<key>/check_rockspec?package=<name>&version=<version>` | Returns JSON with `module` (package exists) and `version` (revision exists); an existing revision aborts the upload unless `--force` |
| 3 | `POST <server>/api/1/<key>/upload` with `multipart/form-data` part `rockspec_file` (and `rockspec_sig` with `--sign`) | Stores the rockspec; returns JSON with `module_url`, `is_new`, `manifests` and `version.id` |
| 4 | `POST <server>/api/1/<key>/upload_rock/<version.id>` with part `rock_file` (and `rock_sig`) | Stores the packed `.src.rock` |

- `<server>` is the `server` value of the upload configuration and `<key>` its `key` value
  (PKG-LUA-008); errors come back as JSON with an `errors` array.
- Uploads go to local repositories, or to a virtual repository that contains a local repository
  as deployment target (REPO-038 in [../repositories.md](../repositories.md)).
- Whether the system validates `<key>` or authenticates only through the Basic credentials in the
  server URL, and whether it implements two-factor verification (`verify_tfa`) of newer clients,
  is unspecified.
- Required privilege: deploy on the target repository; overwriting an existing revision with
  `--force` additionally needs the overwrite action (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

## Client setup and authentication

### PKG-LUA-006 · Client configuration for installs
Priority: **P2**

```lua
rocks_servers = {
  "https://repo.example.com/luarocks/luarocks-virtual"
}
```

| Configuration file | Scope |
|---|---|
| `~/.luarocks/config.lua` | User |
| `~/.luarocks/config-<lua-version>.lua`, for example `config-5.4.lua` | User, for one Lua 5.x version; when it exists, the plain `config.lua` is ignored |
| `/etc/luarocks/config.lua` | System |

`luarocks config config_files` shows which files the client reads. The client setup instructions
(UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md)) offer the snippets with the user's
token filled in.

### PKG-LUA-007 · Install authentication
Priority: **P2**

The LuaRocks client never prompts for credentials. Two methods work for installs:

| Method | Configuration | Notes |
|---|---|---|
| `.netrc` | `fs_use_modules = false` and `variables = { CURL = "curl -n" }` in the config file, credentials in `~/.netrc` (`machine repo.example.com login <user> password ${TOKEN}`), file mode `600` | Forces downloads through curl, which reads `.netrc`; Unix-like systems |
| Credentials in the URL | `"https://<user>:${TOKEN}@repo.example.com/luarocks/luarocks-virtual"` in `rocks_servers` | Token stored in plain text; avoid on shared machines |

The token is used as the Basic password (TOK-034 in [../access-tokens.md](../access-tokens.md)).

### PKG-LUA-008 · Upload configuration and authentication
Priority: **P2**

The client reads upload settings from `~/.luarocks/upload_config.lua`:

```lua
key = "${TOKEN}"
server = "https://<user>:${TOKEN}@repo.example.com/luarocks/luarocks-local"
```

```shell
luarocks upload ./lpeg-1.0.2-1.rockspec       # rockspec first; packs and uploads the .src.rock too
```

- For uploads the client ignores `Authorization: Bearer` and its `--user`/`--password` options;
  credentials must be in the `server` URL as Basic authentication.
- Options: `--skip-pack` uploads only the rockspec, `--force` replaces an existing revision,
  `--sign` attaches GPG signatures, `--debug` prints every HTTP request.

### PKG-LUA-009 · Install commands
Priority: **P2**

| Goal | Command |
|---|---|
| Latest version | `luarocks install lpeg` |
| Specific version | `luarocks install lpeg 1.0.2` |
| One repository only | `luarocks install lpeg --only-server="https://repo.example.com/luarocks/luarocks-local"` |

Leave out `--only-server` when a virtual repository should fall back to its remote members.

## Local repositories

### PKG-LUA-010 · Automatic manifest maintenance
Priority: **P2**

In a local repository, every add, delete, copy or move of a rock or rockspec triggers an
asynchronous recalculation of the package metadata and the manifest. The delay depends on system
load. Consequence for clients: a just-uploaded rock may be missing from the manifest for a short
time.

### PKG-LUA-011 · Recalculate the LuaRocks index
Priority: **P2**

An on-demand full recalculation of a local repository rebuilds the manifest and the metadata of
all its rocks and rockspecs; it is recommended after bulk imports or layout changes.

- UI location: artifact browser, context menu of the repository, "recalculate index" (REPO-009 in
  [../repositories.md](../repositories.md)).
- Runs asynchronously. An API operation for it is unspecified.

### PKG-LUA-012 · Package views for LuaRocks
Priority: **P2**

The extracted name, version and revision populate the package list and search (ART-048 and ART-049
in [../artifacts.md](../artifacts.md)). The package view is for browsing and visibility; it does
not replace the client's resolution or manifest semantics.

## Remote, chained, federated and virtual repositories

### PKG-LUA-013 · Remote LuaRocks repositories
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | Rocks server to proxy | `https://luarocks.org/repositories/rocks/` |

- Behaves like any remote (RMT-001 in [../remote-repositories.md](../remote-repositories.md)):
  manifests are metadata cached for the retrieval cache period (RMT-018), rocks are cached on first
  download (inferred from the general remote model; the split of metadata and content for LuaRocks
  is unspecified).
- The remote does not list the upstream; only pulled or uploaded files appear in the artifact
  browser.

### PKG-LUA-014 · Chained remote LuaRocks repositories
Priority: **P2**

A remote whose upstream URL is a LuaRocks repository on another instance of the system, with
content synchronization and download statistics (RMT-040, RMT-041 in
[../remote-repositories.md](../remote-repositories.md)).

### PKG-LUA-015 · Federated LuaRocks repositories
Priority: **Later**

LuaRocks repositories can be federated: members and synchronization settings are configured at
creation (REPL-018 in [../replication-and-federation.md](../replication-and-federation.md)).
Whether manifests are synchronized or recalculated on each member is unspecified.

### PKG-LUA-016 · Virtual LuaRocks repositories
Priority: **P2**

A virtual repository aggregates local and remote LuaRocks repositories under one rocks-server URL
(REPO-033 in [../repositories.md](../repositories.md)); uploads need a local member as deployment
target (PKG-LUA-005). How member manifests are merged into the served manifest, and which member
wins for a rock present in several members, are unspecified.

### PKG-LUA-017 · Cleanup and archive policies for LuaRocks
Priority: **P2**

Package cleanup and archive policies (RET-022 and RET-027 in
[../retention-and-cleanup.md](../retention-and-cleanup.md)) apply to LuaRocks packages; deletions
trigger the manifest recalculation of PKG-LUA-010.

## Limits

### PKG-LUA-018 · Limitations
Priority: **P2**

| Limit | Detail |
|---|---|
| Remote browsing | Only pulled or uploaded files are browsable |
| Authentication | No interactive prompt; configure `.netrc` or URL credentials before installing; uploads need URL credentials |
| Version display | Extracted name and version only; revision matters to the client |
| Binary rocks | Platform-specific; must match the client architecture |

## Notes for implementers

- Clients request `manifest-<lua-version>.zip`, then `manifest-<lua-version>`, then `manifest`;
  serve consistent content under all names, or clients on different Lua versions see different
  package sets.
- The manifest is Lua source loaded by the client; generate it with proper string escaping, since
  a crafted package name could otherwise inject code into every client that loads the manifest.
- A manifest must list only rocks that are actually downloadable from the same rocks server;
  listing rocks of other members in a virtual manifest requires serving them at the virtual root.
- `luarocks upload` sends the rockspec and the rock in two requests linked by `version.id`; when a
  rock is to follow and the rockspec response lacks `version.id`, the client aborts with "Invalid
  response from server".
- Upload credentials arrive as Basic userinfo in the server URL, and the upload configuration
  (PKG-LUA-008) also puts the token into `key`, which the client places in every upload path;
  redact that path segment in access logs.
- Recalculation is asynchronous; `luarocks install` immediately after `luarocks upload` can miss
  the new revision, which CI scripts should expect.
- Binary rocks for several architectures share one package version; the manifest groups them by
  architecture, and the client picks the one matching its platform or falls back to the source
  rock.
