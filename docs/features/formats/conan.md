# Conan (C/C++) format

This page catalogs what a repository manager does to act as a Conan remote for C and C++
packages: hosting recipes and binary packages with recipe and package revisions in local
repositories, caching another Conan server in remote repositories, combining both in virtual
repositories, the Conan server protocol, login and forced authentication, upload and install
through the Conan client, metadata recalculation, and support for the legacy Conan 1 client. The
protocol is defined by the Conan project; its public documentation covers the client side
([conan remote](https://docs.conan.io/2/reference/commands/remote.html),
[uploading packages](https://docs.conan.io/2/tutorial/conan_repositories/uploading_packages.html)),
while the server routes are defined in the client and reference-server sources
([conan-io/conan](https://github.com/conan-io/conan): `conan/internal/rest/rest_routes.py`,
`conans/server/rest/controller/v2/`). Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/conan/<repo-key>` stands for the Conan remote URL
of one repository (the path layout is the implementation's choice), and `conan-local`,
`conan-remote` and `conan-virtual` are example repository keys. "Conan remote" means the client's
name for a server URL; "remote repository" means the system's pull-through repository class.

## Repositories and addressing

### PKG-CONAN-001 · Conan repositories in all three repository classes
Priority: **P2**

Conan is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)).

- A client's Conan remote is always a local or a virtual repository of the system.
- Remote repositories cache another Conan server; clients cannot install from them directly and
  reach them only as members of a virtual repository (PKG-CONAN-011).
- The system lays out recipes and binaries for every platform configuration the client produces
  without extra configuration (PKG-CONAN-008).
- Creating a repository requires the system administrator or a project administrator role; for
  others the create action is not offered. UI location: administration, repositories, create
  repository, then class and Conan format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

Supported clients: Conan 2 (recommended) and Conan 1 as a legacy client (PKG-CONAN-004).

### PKG-CONAN-002 · Conan server protocol of a repository
Priority: **P2**

Every Conan repository a client addresses implements the Conan server API version 2, the
revision-aware protocol. Routes relative to the Conan remote URL, from the Conan sources; user and
channel are replaced by `_` when a reference has none:

<details>
<summary>Server routes used by the Conan 2 client</summary>

| Method and path | Purpose | Response |
|---|---|---|
| `GET v1/ping` | Capability probe | Header `X-Conan-Server-Capabilities` with a comma-separated list. Conan 2 requires `revisions`; `checksum_deploy` is optional |
| `GET v2/users/authenticate` | Exchange HTTP Basic credentials for a token | Plain-text token |
| `GET v2/users/check_credentials` | Validate the current token | Plain-text user name; `401` makes the client ask for credentials |
| `GET v2/conans/search?q=<pattern>[&ignorecase=False]` | Search recipes | `{"results": ["<name>/<version>@<user>/<channel>", …]}` |
| `GET v2/conans/<name>/<version>/<user>/<channel>/latest` | Latest recipe revision | `{"revision": "<rrev>", "time": "<ISO 8601>"}` |
| `GET v2/conans/<ref>/revisions` | All recipe revisions | `{"revisions": [{"revision": …, "time": …}]}` |
| `GET v2/conans/<ref>/revisions/<rrev>/files` | Recipe file list | `{"files": {"<path>": {}, …}}` |
| `GET`, `PUT v2/conans/<ref>/revisions/<rrev>/files/<path>` | Download or upload one recipe file | file content |
| `GET v2/conans/<ref>/search?list_only=…`, `GET v2/conans/<ref>/revisions/<rrev>/search` | Binary packages of a recipe (revision) | package information |
| `GET v2/conans/<ref>/revisions/<rrev>/packages/<package_id>/latest` | Latest package revision | `{"revision": "<prev>", "time": …}` |
| `GET …/packages/<package_id>/revisions` | All package revisions | as for recipe revisions |
| `GET …/packages/<package_id>/revisions/<prev>/files` | Package file list | as for recipe files |
| `GET`, `PUT …/packages/<package_id>/revisions/<prev>/files/<path>` | Download or upload one package file | file content |
| `DELETE v2/conans/<ref>`, `DELETE v2/conans/<ref>/revisions/<rrev>` | Remove a recipe with all revisions, or one revision | — |
| `DELETE …/revisions/<rrev>/packages` | Remove all binaries of a recipe revision | — |
| `DELETE …/packages/<package_id>`, `DELETE …/packages/<package_id>/revisions/<prev>` | Remove a binary with all its revisions, or one revision | — |

`<ref>` abbreviates `<name>/<version>/<user>/<channel>`.

</details>

- Files of a recipe revision: `conanfile.py`, `conanmanifest.txt`, `conan_export.tgz`,
  `conan_sources.tgz` (exported sources) and optional `metadata/…` files. Files of a package
  revision: `conaninfo.txt`, `conanmanifest.txt`, `conan_package.tgz`.
- After a successful `authenticate`, the client sends `Authorization: Bearer <token>` on every
  request.
- The revision-aware API is supported; support for its individual routes, in particular search,
  package search and deletion, is unspecified.

## Client setup and authentication

### PKG-CONAN-003 · Conan 2 client configuration and login
Priority: **P2**

```shell
conan remote add company https://repo.example.com/conan/conan-virtual
conan remote login -p <token> company <user>
```

- The remote name is a local alias; a generated snippet uses the repository key, and the user may
  pick another name.
- The password is the user's token; the client exchanges it for a server token through the
  authentication route (PKG-CONAN-002).
- For CI, `conan remote auth <pattern>` reads `CONAN_LOGIN_USERNAME*` and `CONAN_PASSWORD*`
  environment variables instead of prompting.
- The client setup dialog generates the commands with the user's token (UI-021, UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

Interactions: anonymous access and forced authentication (PKG-CONAN-005); tokens TOK-005 and
TOK-034 in [../access-tokens.md](../access-tokens.md).

### PKG-CONAN-004 · Conan 1 legacy client
Priority: **P2**

Conan 1 clients are supported as a legacy tool. They must run with revisions enabled, which needs
Conan 1.13 or later:

```shell
conan remote add company https://repo.example.com/conan/conan-virtual
export CONAN_REVISIONS_ENABLED=1
conan user -p <token> -r company <user>
conan upload "fmt/9.1.0@acme/stable" -r company --all
```

- Conan 1 references have the form `<name>/<version>@<user>/<channel>`; the channel usually
  expresses stability.
- `--all` uploads the recipe with all its binaries; the Conan 2 equivalent is `--confirm` (`-c`)
  with a pattern (PKG-CONAN-006).

### PKG-CONAN-005 · Anonymous access and forced authentication
Priority: **P2**

When anonymous access is enabled on the system (IAM-005 in
[../identity-and-access.md](../identity-and-access.md)), the system does not challenge the Conan
client for credentials by default, so a logged-out client works anonymously and an unauthenticated
request for something the anonymous user may not see fails instead of prompting.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Force authentication | local, remote, virtual | The repository asks the Conan client for credentials before serving any request, even when anonymous access is enabled | off for local and virtual; unspecified for remote |

UI location: create and edit repository form. Also listed as REPO-028 in
[../repositories.md](../repositories.md).

Client-side counterparts defined by Conan: `conan remote add --force-auth` (never use anonymous
access for this remote) and `conan remote auth --force`.

Security: anonymous access is discouraged because it can expose existing and future repositories
to unauthenticated users.

## Upload and install

### PKG-CONAN-006 · Upload recipes and binary packages
Priority: **P2**

```shell
conan upload "<pattern>" --remote=company --confirm
```

- `<pattern>` is a recipe reference such as `fmt/9.1.0` or a wildcard such as `*` for everything
  in the local cache; the recipe and its binaries are uploaded.
- Only uploads through the Conan client are indexed. Recipes deployed through the UI or the generic
  deploy operation are stored but not indexed, which leads to inconsistent indexes and failed
  resolutions; this path is not permitted.
- The client uploads the files of a revision in sorted order so that `conanmanifest.txt` comes
  last; a revision whose manifest is missing is incomplete.
- With the `checksum_deploy` capability the client first tries to deploy each file by checksum
  only and sends the content when the server does not know it (ART-004 in
  [../artifacts.md](../artifacts.md)). Whether the system advertises this capability is
  unspecified.

Required privilege: deploy permission on the target repository; for a virtual target, its default
deployment repository receives the upload (REPO-038 in [../repositories.md](../repositories.md)).

### PKG-CONAN-007 · Install packages
Priority: **P2**

```shell
conan install . -r company
```

Installs the dependencies declared in `conanfile.txt` or `conanfile.py` from the repository. By
default the client asks for the latest recipe revision and the latest package revision of each
reference unless the user pins a revision.

## Revisions and storage

### PKG-CONAN-008 · Revisions and storage layout
Priority: **P2**

Recipe revisions and package revisions let the content behind one reference change without
overwriting what was uploaded before; each upload of changed content creates a new revision.

- Storage layout of a binary inside a repository (example with recipe revision `1`,
  package ID `9999` and package revision `4`):
  `<user>/<name>/<version>/<channel>/<recipe revision>/package/<package ID>/<package revision>/…`.
- The client uploads every revision's files under its own revision root; earlier revisions stay
  untouched, which is what makes packages immutable on the server.
- On every upload through the Conan client, the system writes a time-stamp marker file under the
  root of each recipe revision and each package revision. It holds the deployment time in
  milliseconds since the epoch (for example `1547984992855`). The marker is created only for
  Conan-client uploads. "Latest revision" answers (PKG-CONAN-002) are presumably derived from it;
  that is an inference and unspecified.

Edge cases: revision ordering when two uploads carry the same time stamp is unspecified.

### PKG-CONAN-009 · Migration of revisionless packages
Priority: **P2**

Packages stored by the revisionless Conan protocol (server API v1) are converted to the revision
layout automatically.

- The migration starts after an upgrade has finished on all nodes of the cluster.
- While it runs, all Conan protocol endpoints are blocked; they become available again afterwards.
- Migrated packages get revision `0` as both recipe revision and package revision.

| Setting (system-wide) | Meaning | Default |
|---|---|---|
| Migration workers | Number of parallel workers for the conversion; a change takes effect after a restart | 2 |

Relevance for a new implementation: only when importing content from a revisionless Conan server.

### PKG-CONAN-010 · Recalculate Conan metadata
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate Conan metadata | repository key; flag "run asynchronously" | `200` with a plain-text message; `401`, `403`, `500` on failure | unspecified |

- Recomputes the package and revision metadata of the repository.
- Synchronous by default; asynchronous on request.
- Local repositories only.

Interaction: REPO-009 (recalculate repository index) in [../repositories.md](../repositories.md).

## Remote and virtual repositories

### PKG-CONAN-011 · Remote Conan repositories
Priority: **P2**

A Conan remote repository proxies and caches another Conan server reached at its upstream URL
(RMT-001 and RMT-004 in [../remote-repositories.md](../remote-repositories.md)). The form shows a
pre-filled upstream URL to confirm or change; the pre-filled value is unspecified.

- Clients cannot install from a remote repository directly; it must be a member of a virtual Conan
  repository (PKG-CONAN-012).
- The only Conan-specific remote setting is force authentication (PKG-CONAN-005).

### PKG-CONAN-012 · Virtual Conan repositories
Priority: **P2**

A virtual Conan repository aggregates local and remote members under one Conan remote URL (REPO-033
in [../repositories.md](../repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Metadata cache period | How long merged metadata is cached before members are asked again (REPO-037, REPO-044) | 7200 s |
| Force authentication | PKG-CONAN-005 | off |

Edge cases: how "latest revision" is decided when several members hold revisions of the same
reference is unspecified.

## Scanning

### PKG-CONAN-013 · Scanning Conan packages and C/C++ builds
Priority: **Later**

With the vulnerability scanner (package risk data SEC-001 and the scanning entries in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md)):

- Conan packages in the system are scanned like other package types; results are shown on the
  `conanmanifest.txt` file only. An optional vendor attribute in the recipe reduces false
  positives.
- Conan builds can be scanned through build metadata that a Conan client extension produces.
- Arbitrary C/C++ builds can be scanned through a hand-made build record (BLD-001 to BLD-005 in
  [../builds-and-ci.md](../builds-and-ci.md)): one module typed as C/C++, and per dependency the
  SHA-1 (MD5 optional) and an identifier `[<vendor>:]<name>:<version>`. Every library to be scanned
  must be listed explicitly; the record is uploaded with the build publish operation and then
  scanned.

## Companion client

### PKG-CONAN-014 · Companion client for Conan
Priority: **P2**

The companion command-line client (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) offers a
configuration command and a wrapper for Conan 2.

<details>
<summary>Configuration command and wrapper behavior</summary>

| Setting | Meaning | Default |
|---|---|---|
| Resolution repository and connection | Repository and server profile for installs | prompted when missing |
| Deployment repository and connection | Repository and server profile for uploads | prompted when missing |
| Global configuration | Configuration for all projects; a project overrides it | off |

- The configuration command writes a per-project configuration and also registers the Conan remote
  in the local Conan client; it replaces a manual `conan remote add`.
- The wrapper runs native Conan subcommands (`install`, `create`, `upload`, …) with build name,
  build number, module and project options; with build name and number it collects build metadata
  (BLD-039) and prints how to publish it.
- `conan create` needs a `conanfile.py`; `conan install` accepts `conanfile.txt` too.
- The wrapper uses Conan 2 syntax; Conan 1 flags such as `--all` fail with "unrecognized
  arguments".
- The transparent command interception (BLD-042) does not cover `conan`.

</details>

## Notes for implementers

- `v1/ping` must return `X-Conan-Server-Capabilities` containing `revisions` even on the v2 server;
  Conan 2 refuses remotes without it, and a `200` without the header is treated as "not a Conan
  remote".
- A ping answered with `401`/`403` and no capability header is treated as an authentication error
  (the client prompts and does not cache capabilities), not as "not a Conan remote"; a protected
  ping is therefore allowed.
- Authentication is a token exchange: Basic on `v2/users/authenticate`, then Bearer. The returned
  token must work as a Bearer credential on every route; `check_credentials` must answer `401` for
  an invalid or missing token so the client prompts.
- Forced authentication exists because, with anonymous access on, a client that is not challenged
  never sends credentials; answering `401` rather than `404` to anonymous requests for protected
  content gives the client the challenge it needs.
- `_` in the user and channel path segments means "none"; references with and without user/channel
  are different packages.
- JSON answers (search, revision lists, file lists, latest revision) must carry
  `Content-Type: application/json` (optionally with `; charset=utf-8`); the Conan 2 client rejects
  any other content type as "not json" even when the body is valid JSON.
- Never overwrite a revision; new content means a new revision, and "latest" is decided by
  upload time.
- Treat a revision without `conanmanifest.txt` as incomplete; the client uploads it last.
- Index only content uploaded through the protocol; generic file uploads into a Conan repository
  bypass revision bookkeeping.
