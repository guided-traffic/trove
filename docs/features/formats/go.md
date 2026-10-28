# Go modules format

This page catalogs what a repository manager does to act as a Go module proxy: hosting an
organization's own modules in local repositories, caching public and private modules from a
module mirror or straight from version-control hosts in remote repositories, combining both behind
one `GOPROXY` URL in virtual repositories, proxying the Go checksum database, client setup and
authentication, and module publication. The wire protocol is defined by the Go project in the
[Go Modules Reference](https://go.dev/ref/mod), not by any repository manager; where an entry
states protocol behavior, it links that reference. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in general in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/go/<repo-key>` stands for the proxy URL of one
repository (the path layout is the implementation's choice), `go-local`, `go-remote` and
`go-virtual` are example repository keys, and `<module>` and `<version>` are a module path and a
module version.

## Repositories and addressing

### PKG-GO-001 · Go repositories in all three repository classes
Priority: **P1**

Go modules are supported as a local, a remote and a virtual repository format (see REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold modules the organization publishes itself (PKG-GO-006);
- remote repositories cache modules from a module mirror such as `https://proxy.golang.org`, from
  another instance of the system, or directly from a version-control host (PKG-GO-007 to
  PKG-GO-013);
- virtual repositories combine local and remote members under one proxy URL and are the only way
  clients reach remote content (PKG-GO-003).

Supported client: the `go` command from version 1.11.0. Both authenticated users and the anonymous
user (when anonymous access is enabled, IAM-005 in
[../identity-and-access.md](../identity-and-access.md)) can resolve modules.

Creating a repository requires the system administrator or a project administrator role; for
others the create action is not offered. UI location: administration, repositories, create
repository, then the class and the Go format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).
A local repository needs only its key. A remote repository form shows a pre-filled upstream URL
to confirm or change (the pre-filled value is unspecified). A virtual repository form asks for one
or more local or remote Go members.

### PKG-GO-002 · Module proxy endpoint of a repository
Priority: **P1**

Every Go repository a client may address answers the GOPROXY protocol under its own base URL. The
protocol consists only of `GET` requests without query parameters and without required headers.
Defined by the Go project ([GOPROXY protocol](https://go.dev/ref/mod#goproxy-protocol)):

| Request, relative to the repository base URL | Response |
|---|---|
| `<module>/@v/list` | Plain text, one known version per line. Pseudo-versions are not listed. |
| `<module>/@v/<version>.info` | JSON object with `Version` (required, canonical version) and `Time` (optional, RFC 3339). `<version>` may also be a branch name or revision identifier; the response then names the canonical version it resolves to. |
| `<module>/@v/<version>.mod` | The module's `go.mod` file, unmodified. If the version has no `go.mod`, a synthetic file containing only a `module` line with the requested path. |
| `<module>/@v/<version>.zip` | The module zip of that version. |
| `<module>/@latest` | Optional. Same format as `.info`, for the version the client should use when the list is empty or unsuitable. |

Protocol rules a compatible server follows:

- Module paths and versions in the URL are case-encoded: every uppercase letter becomes `!`
  followed by the lowercase letter (`example.com/M` is requested as `example.com/!m`).
- Success is `200`; redirects are followed. `404` and `410` mean "not available here, may exist
  elsewhere". After an entry followed by a comma, the client moves on to the next `GOPROXY` entry
  only on those two codes, and any other `4xx`/`5xx` ends resolution at this proxy; after an entry
  followed by `|`, it moves on after any error, timeouts included. Error bodies should be
  `text/plain` in UTF-8 or US-ASCII.
- Content served for a `.mod` or `.zip` request of a version must never change afterwards; clients
  verify it against `go.sum` and the checksum database (PKG-GO-014). Version lists and `.info`
  responses are not authenticated and may change.
- Module zip constraints ([module zip files](https://go.dev/ref/mod#zip-files)): at most 500 MiB
  compressed and 500 MiB uncompressed, `go.mod` and `LICENSE` at most 16 MiB each, every entry
  prefixed with `<module>@<version>/`, no two paths equal under Unicode case folding.

Which of these endpoints each repository class implements itself is unspecified; clients resolve
through local and virtual repositories (PKG-GO-003). Whether `@latest` is served is unspecified.

Interactions: repository addressing REPO-005 in [../repositories.md](../repositories.md);
negative caching of misses RMT-020 in [../remote-repositories.md](../remote-repositories.md).

### PKG-GO-003 · Resolution only through local and virtual repositories
Priority: **P1**

Clients resolve Go modules only from local or virtual Go repositories. A remote Go repository is
not addressed by clients directly: to use it, it is made a member of a virtual Go repository and
the client points `GOPROXY` at the virtual repository. This applies to every kind of Go remote
(module mirror, VCS-backed, chained instance).

Consequences:

- The recommended client target is a virtual repository that contains the local repositories, a
  module-mirror remote and any VCS-backed remotes (PKG-GO-016).
- A chained remote that points at another instance uses a virtual repository on the source side
  as its upstream; for Go this is mandatory (RMT-040 in
  [../remote-repositories.md](../remote-repositories.md)).

Edge cases: the response a client receives when it addresses a remote Go repository directly is
unspecified.

## Client setup and authentication

### PKG-GO-004 · Native `go` client configuration
Priority: **P1**

The `go` command is pointed at a repository through `GOPROXY`. The system's recommended form
embeds user name and credential in the URL:

```shell
export GOPROXY="https://<user>:<token>@repo.example.com/go/go-virtual"
```

- The credential is the user's password or, recommended, a token used as the password (identity
  token TOK-005 and token-as-password TOK-034 in [../access-tokens.md](../access-tokens.md)). The
  `go` command sends URL credentials as HTTP Basic authentication.
- Further client-side options defined by the Go project
  ([passing credentials to private proxies](https://go.dev/ref/mod#private-module-proxy-auth)):
  a `.netrc` entry for the proxy host, or from Go 1.24 `GOAUTH`, which can supply arbitrary
  request headers such as a Bearer token per URL prefix. The system's support for Bearer tokens on
  this endpoint is unspecified.
- With anonymous access enabled, the URL can omit credentials.
- Private module paths must be excluded from checksum-database verification (PKG-GO-014).
  `GONOSUMDB` does only that. `GOPRIVATE` also does it, but it is in addition the default for
  `GONOPROXY`, so the `go` command fetches matching modules straight from version control and
  never asks the system for them; a VCS-backed remote (PKG-GO-009) then sees no traffic. To
  resolve private modules through the system, set `GONOSUMDB` alone, or `GOPRIVATE` together with
  `GONOPROXY=none` (Go's [private modules](https://go.dev/ref/mod#private-modules)).
- The client setup dialog generates the snippet with the user's token and the selected
  repository (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md)).

Security: credentials in `GOPROXY` appear in shell history, process environments and CI logs; a
short-lived, read-scoped token limits the exposure.

### PKG-GO-005 · Companion client integration for Go
Priority: **P2**

A command-line companion client (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) wraps the
`go` command:

<details>
<summary>Behavior and settings of the Go wrapper</summary>

| Setting | Meaning | Default |
|---|---|---|
| Resolution repository and connection | Repository and server profile used for fetching modules | none |
| Deployment repository and connection | Repository and server profile used for publishing (PKG-GO-006) | none |
| Global configuration | Store the configuration for all projects instead of the current one; a project-level configuration overrides it | off |
| No VCS fallback (per run) | When on, a module missing from the repository fails the run instead of being fetched from its version-control origin | off (fallback allowed) |
| Build name, build number, module, project (per run) | Collect dependency metadata into a local build record (BLD-039) | none |

- The per-project configuration step must run before the first wrapped command; without it the
  wrapper fails with a "no configuration" error. The wrapper expects a `go.mod` in the working
  directory and recommends Go 1.16 or later.
- The wrapper sets `GOPROXY` to the resolution repository for the duration of the command only and
  overrides any value already set.
- A known trap: when the stored credential is a reference token (TOK-004)
  without a user name, builds succeed but fetching modules with the wrapper's `get` can fail with
  `401`; configuring a user name fixes it. This follows from PKG-GO-004: the `go` command sends
  Basic authentication, so the server must accept the token as a password for the given user.
- Troubleshooting hints: `404` when the resolution repository does not exist or the module is not
  cached (a Go virtual repository is the advised resolution target);
  `401`/`403` for invalid credentials or missing permissions.

</details>

## Publishing

### PKG-GO-006 · Publish a module version
Priority: **P1**

The `go` command has no publish operation; Go modules normally live in version control. To place
a module version in a local repository, the companion client packs the module from the project
root and uploads it.

| Input | Meaning | Default |
|---|---|---|
| Target repository | A local Go repository, or a virtual repository whose default deployment repository is a local Go repository (REPO-038 in [../repositories.md](../repositories.md)) | none (required) |
| Version | The module version to publish; must be a semantic version with the `v` prefix, such as `v1.0.0` | none (required) |
| Exclusions | Semicolon-separated file patterns with `*` and `?` wildcards to leave out of the module archive (for example tests or documentation); use forward slashes on every platform | none |
| Detailed summary | Report every uploaded file | off |
| Connection override | Server URL plus user and password, or an access token, given per run instead of the stored profile | none |
| Build name, build number, module, project | Record the uploaded files as build artifacts (BLD-039) | none |

- A version without the `v` prefix or not of the form `vX.Y.Z` is rejected by the server.
- The command publishes the package and its dependencies; which files beyond the module's own
  `.zip`, `.mod` and `.info` are uploaded is unspecified.
- The upload operation the client uses (path layout, method, which of `.zip`/`.mod`/`.info` the
  server derives itself) is unspecified. Whatever is stored must be served back through PKG-GO-002
  unchanged; publishing a version that already exists with different content
  would break every `go.sum` that recorded the first content.

Required privilege: deploy permission on the target local repository (permission model in
[../identity-and-access.md](../identity-and-access.md)).

## Remote repositories

### PKG-GO-007 · Go remote repository and upstream kind
Priority: **P1**

A Go remote repository is a caching proxy for one upstream: a module mirror such as
`https://proxy.golang.org`, a Go repository on another instance of the system, or a
version-control host serving private or public module source. Fetched files (module zips and the
other module files) are cached on first request. Cached files can be removed from the cache
(RMT-003); deploying into a remote repository is not possible.

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | Base URL of the upstream (RMT-004) | pre-filled, value unspecified |
| Upstream kind ("Git provider") | How the upstream is spoken to. Values: *proxy protocol* (the upstream implements the GOPROXY protocol: a module mirror or another instance), *GitHub cloud*, *GitHub server* (self-hosted GitHub Enterprise), *GitLab* (cloud and self-managed), *Bitbucket Cloud*, *Bitbucket Server* | unspecified |
| Resolve subgroups | Only offered for the GitLab kind (PKG-GO-012) | off |

UI location: remote repository form, basic tab, Go settings section.

Interactions: credentials RMT-005, caching periods RMT-018 and RMT-020, priority resolution
RMT-037 (all in [../remote-repositories.md](../remote-repositories.md)); a remote is used only as
a virtual member (PKG-GO-003).

### PKG-GO-008 · Module-mirror remote
Priority: **P1**

A remote whose upstream kind is *proxy protocol* and whose URL is `https://proxy.golang.org/` (or
another GOPROXY-protocol server, including another instance of the system) caches public modules
by plain GOPROXY requests.

Recommended setup, in this order:

1. Create the mirror remote with upstream kind *proxy protocol*.
2. Turn on priority resolution for it (advanced tab; REPO-018 in
   [../repositories.md](../repositories.md)).
3. Put it into the same virtual repository as every VCS-backed remote.

Purpose: public modules are then served from the mirror's cache or the mirror itself, so the
VCS-backed remotes see only requests for private modules. This avoids needless traffic to the
version-control hosts and their rate limits.

Edge cases: whether the mirror remote also covers checksum-database requests is described in
PKG-GO-014.

### PKG-GO-009 · VCS-backed remote for private modules
Priority: **P1**

A remote can proxy module source hosted directly on a version-control platform (GitHub, GitHub
Enterprise, GitLab, Bitbucket Cloud, Bitbucket Server). The system turns the platform's
repositories, tags and commits into GOPROXY responses for its virtual repositories.

Requirements and behavior:

- The source repositories must follow Go's module and versioning conventions
  ([Go Modules Reference](https://go.dev/ref/mod),
  [module release and versioning workflow](https://go.dev/doc/modules/release-workflow)); in
  particular, versions are tags of the form `vX.Y.Z` and major versions 2 and above carry the
  `/vN` path suffix.
- Credentials for the platform are stored on the remote (user name plus a platform token, RMT-005
  in [../remote-repositories.md](../remote-repositories.md)). Anonymous access to platforms is
  subject to their rate limits.
- The remote works only through a virtual repository (PKG-GO-003); per-platform setup is in
  PKG-GO-010 to PKG-GO-013. Plain version-control archive repositories, which share the provider
  idea but not the GOPROXY protocol, are a separate format (PKG-VCS-001 in [vcs.md](vcs.md)).
- How the system builds module zips, `.info` time stamps and version lists from the platform
  (clone, archive download or platform API) is unspecified. A compatible implementation must
  produce zips whose file set matches what the `go` command itself would produce from the same
  revision, because `go.sum` hashes the files inside the zip and the public checksum database holds
  hashes for public modules ([module zip files](https://go.dev/ref/mod#zip-files),
  [go.sum files](https://go.dev/ref/mod#go-sum-files)).

### PKG-GO-010 · GitHub and GitHub Enterprise upstreams
Priority: **P2**

| Upstream | Upstream URL | Upstream kind | Credentials | Extra setting |
|---|---|---|---|---|
| GitHub (cloud) | `https://github.com/` | GitHub cloud | GitHub user name and a personal access token, on the basic tab; GitHub rejects account passwords for API access | Forward credentials to redirect hosts turned on (RMT-012 in [../remote-repositories.md](../remote-repositories.md)), advanced tab |
| GitHub Enterprise, self-hosted | the instance's URL | GitHub server | user name and a classic personal access token with the `repo` scope | unspecified |
| GitHub Enterprise, cloud | the instance's URL | GitHub cloud | as above | unspecified |

Edge cases: credentials are needed even for public GitHub repositories in practice, because
anonymous API access is rate-limited. Token expiry is set on the GitHub side; the system's
behavior when the stored token expires is unspecified.

### PKG-GO-011 · GitLab upstream
Priority: **P1**

| Setting | Value for GitLab |
|---|---|
| Upstream URL | `https://gitlab.com/` for GitLab SaaS, the instance URL for self-managed GitLab |
| Upstream kind | GitLab |
| Resolve subgroups | optional (PKG-GO-012) |
| Credentials | GitLab user name and a personal access token with the `api` and `read_api` scopes, entered as the password |

After the remote is created, a virtual repository is created with the remote as member and an
include pattern that limits the member to GitLab module paths (REPO-011 in
[../repositories.md](../repositories.md)):

| GitLab variant | Include pattern on the virtual repository |
|---|---|
| SaaS | `**/gitlab.com/**` |
| Self-managed | `**/<gitlab-host>/**` |

Interactions: PKG-GO-016 (routing in the virtual repository); for the owner's setup, GitLab CI
jobs reach the virtual repository with tokens from the CI OIDC exchange
([../oidc-ci-integration.md](../oidc-ci-integration.md)).

### PKG-GO-012 · GitLab subgroup resolution
Priority: **P1**

GitLab nests projects in groups and subgroups (`gitlab.com/group/subgroup/project`). A module
path does not reveal where the project ends and a module subdirectory begins.

| Setting | Meaning | Default |
|---|---|---|
| Resolve subgroups | When on, the system determines from the URL path which segments are subgroups and which are a sub-module, and resolves the dependency at the right version. When off, the segment after the project candidate is treated as a sub-module. | off |

- Offered only when the upstream kind is GitLab.
- It exists because GitLab's API addressing of subgroup projects differs from the `.git` path
  form.
- Recommended practice: a dedicated remote whose URL points at the specific subgroup, next to a
  regular GitLab remote; the order of the two in the virtual repository decides which is asked
  first (REPO-035 in [../repositories.md](../repositories.md)).

### PKG-GO-013 · Bitbucket Cloud and Bitbucket Server upstreams
Priority: **P2**

| Upstream | Upstream URL | Upstream kind | Credentials (advanced tab) |
|---|---|---|---|
| Bitbucket Cloud | `https://bitbucket.org/` | Bitbucket Cloud | Bitbucket user name and an app password with read permission on repositories |
| Bitbucket Server (self-hosted) | the server URL, for example `https://git.example.com/` | Bitbucket Server | user name and an HTTP access token with read permission on repositories |

After creation the remote is added to a virtual repository, which clients use.

## Checksum database

### PKG-GO-014 · Checksum database proxy
Priority: **P1**

From Go 1.13 the `go` command verifies module hashes against a checksum database, by default
`sum.golang.org`. The system proxies and caches checksum-database requests for clients that use it
as their module proxy, so clients without direct internet access still verify public modules.
This works without extra configuration for remotes pointing at GitHub or
at another instance of the system; for other upstream kinds it is unspecified.

Protocol (defined by the Go project,
[checksum database](https://go.dev/ref/mod#checksum-database) and
[proxying a checksum database](https://go.dev/design/25530-sumdb#proxying-a-checksum-database)):

| Request, relative to the proxy URL | Meaning |
|---|---|
| `sumdb/<db-name>/supported` | Probe. `200`: the proxy serves this database, and the client then uses only the proxy for it, never a direct connection. `404`/`410`: the client tries the next `GOPROXY` entry or connects directly. Any other status: the database counts as unavailable. |
| `sumdb/<db-name>/latest` | Signed tree head of the log |
| `sumdb/<db-name>/lookup/<module>@<version>` | Record number, `go.sum` lines and signed tree head for one module version |
| `sumdb/<db-name>/tile/<H>/<L>/<K>[.p/<W>]` | Hash tile of the log |
| `sumdb/<db-name>/tile/<H>/data/<K>[.p/<W>]` | Record data for a tile |

`<db-name>` is the database name from the client's `GOSUMDB` (default `sum.golang.org`). Module
paths are case-encoded as in PKG-GO-002.

System-wide settings (not per repository):

| Setting | Meaning | Default |
|---|---|---|
| Checksum database proxying | Serve and cache checksum-database requests | on |
| Checksum database URL override | Upstream URL the system uses for checksum requests instead of the public database | the public Go checksum database |

Edge cases:

- Private modules (from VCS-backed remotes or local repositories) and some upstreams have no
  entries in the public database; the client then reports a missing or mismatching checksum. The
  fix is on the client: `GONOSUMDB` (or `GOPRIVATE` with `GONOPROXY=none`, see PKG-GO-004) for the
  private path prefixes, or `GOSUMDB=off` to disable verification entirely (not recommended).
- When proxying is disabled, the `supported` probe must answer `404` so clients fall back to a
  direct connection; how the system answers is unspecified.

Security: the proxy only relays signed data; clients verify signatures and inclusion proofs
themselves, so a compromised proxy cannot forge sums, only withhold them.

## Virtual repositories

### PKG-GO-015 · Following `go-import` meta tags
Priority: **P1**

Some module paths are vanity paths: the module's host answers `https://<module>?go-get=1` with an
HTML page carrying `<meta name="go-import" content="<root-path> <vcs> <repo-url> [<subdirectory>]">`,
which names the real repository ([finding a repository for a module path](https://go.dev/ref/mod#vcs-find)).
A virtual Go repository follows these tags to download modules from the announced repository.

| Setting | Meaning | Default |
|---|---|---|
| Follow go-import meta tags | The virtual repository follows repository roots announced in `go-import` tags of upstream responses to download remote modules | on |
| go-import allow list | Ant-style path patterns (REPO-048 in [../repositories.md](../repositories.md)) of the repository roots the system may follow. To allow only GitHub, replace the default with `**/github.com/**` | `**` (any source) |

UI location: virtual repository form, advanced tab. The same settings are listed as REPO-040 and
REPO-044 in [../repositories.md](../repositories.md).

Edge cases: which version-control systems in the tag (`git`, `hg`, `svn`, `mod`, …) the system
can follow is unspecified; `mod` means the target speaks the GOPROXY protocol.

Security: the tag lets the owner of any module path direct the system to fetch from an arbitrary
host. The default allow list permits every host; restricting it bounds where the system makes
outbound requests on behalf of clients.

### PKG-GO-016 · Routing module paths to members of a virtual repository
Priority: **P1**

A virtual Go repository asks its members in the configured order (REPO-035) and returns the first
match, with priority-resolution members asked first (REPO-018). Two mechanisms steer module paths
to the right member:

- Include and exclude patterns limit which paths a member is asked for (REPO-011, RMT-034), for
  example `**/gitlab.com/**` for a GitLab remote (PKG-GO-011).
- Priority resolution on the module-mirror remote (PKG-GO-008) keeps public modules away from the
  VCS-backed remotes.

Deploying through the virtual repository goes to its default deployment repository (REPO-038).

Edge cases: how version lists (`@v/list`) are combined when several members know the same module
(union or first match) is unspecified. The `go` command probes every path prefix of a package path
when looking for the providing module (for `golang.org/x/net/html` it asks for
`golang.org/x/net/html`, `golang.org/x/net`, `golang.org/x` and `golang.org`), so most requests to
a virtual repository end in `404`; these must stay fast and should be negatively cached (RMT-020 in
[../remote-repositories.md](../remote-repositories.md), which lists a shorter default negative
cache period for Go remotes, 600 s instead of 1,800 s).

## Notes for implementers

- Case-encode module paths and versions (`!` plus lowercase) in URLs and in storage keys; two
  modules that differ only in case are distinct.
- `.mod` and `.zip` content for a version is immutable once served. `go.sum` hashes the files
  inside the zip, so a regenerated zip with a different file set breaks every consumer.
- Answer unknown modules with `404` (or `410`), never `403` or `500`, unless the intent is to
  block: with a comma-separated `GOPROXY` only those two codes let the client move on.
- `@v/list` must not contain pseudo-versions; `.info` must accept branch names and commit
  identifiers and return the canonical version.
- `go` sends `GOPROXY` URL credentials as HTTP Basic; tokens must therefore work as passwords, and
  reference tokens need a user name alongside.
- A `200` on `sumdb/<name>/supported` commits the client to the proxy for all checksum traffic; if
  checksum proxying later fails, verification fails instead of falling back.
- Clients never address remote Go repositories; every remote is reached through a virtual
  repository, and the virtual repository's member order and patterns decide which upstream sees a
  module path.
- Private modules need `GONOSUMDB` on the client; `GOPRIVATE` alone silently routes them around
  the proxy (it implies `GONOPROXY`). Generated client setup snippets should say so.
