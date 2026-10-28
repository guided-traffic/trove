# Git LFS format

This page catalogs what a repository manager does to act as a Git Large File Storage (LFS) server:
storing the large-file objects that Git LFS keeps outside a Git repository, answering the Git LFS
Batch API and basic transfer adapter, file locking, client authentication, and removing objects no
Git ref references any more. The Git repository itself stays on the Git host; the repository
manager only holds the objects that LFS pointer files refer to. The protocol is defined by the Git
LFS project, not by any repository manager: the
[Batch API](https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md),
[basic transfers](https://github.com/git-lfs/git-lfs/blob/main/docs/api/basic-transfers.md),
[file locking](https://github.com/git-lfs/git-lfs/blob/main/docs/api/locking.md),
[server discovery](https://github.com/git-lfs/git-lfs/blob/main/docs/api/server-discovery.md) and
[authentication](https://github.com/git-lfs/git-lfs/blob/main/docs/api/authentication.md)
specifications. Where an entry states protocol behavior, it comes from those specifications.
Repository classes in general are described in [../repositories.md](../repositories.md).

Throughout this page, `https://repo.example.com/lfs/<repo-key>` stands for the LFS endpoint of one
repository (the path layout is the implementation's choice), `lfs-local`, `lfs-remote` and
`lfs-virtual` are example repository keys, and `<oid>` is the SHA-256 of an object's content in
lowercase hex.

## Repositories and addressing

### PKG-LFS-001 · Git LFS repositories in all three repository classes
Priority: **P2**

Git LFS is offered as a local, remote and virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)); the API's format sets also list it for federated
repositories (REPL-026 in [../replication-and-federation.md](../replication-and-federation.md)).

- **Local**: stores the LFS objects pushed by Git LFS clients.
- **Remote**: proxies and caches a Git LFS repository on another instance of the same kind of
  system, with the extra exchanges of a chained remote (RMT-040 in
  [../remote-repositories.md](../remote-repositories.md)). Whether a third-party LFS server (a Git
  host's own LFS endpoint) can be an upstream is unspecified.
- **Virtual**: aggregates local and remote LFS repositories. With a default deployment repository
  (REPO-038) the virtual repository is both download source and upload target, so the client
  needs a single endpoint.

Use cases: keeping binary assets (audio, video, datasets, graphics, design files) on the
organization's own network for faster builds and independence from external services; sharing
assets between teams through remote LFS repositories; access control through the system's
permission model.

Creation: system administrator or project administrator; UI location: administration,
repositories, create repository, choose the class and the Git LFS format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)). The UI asks for the repository key (all classes), the
upstream URL (remote) and the members plus default deployment repository (virtual).

Interactions: scanner indexing is not supported for Git LFS repositories (REPO-021 in
[../repositories.md](../repositories.md)). The repository forms do not offer priority resolution
(REPO-018) or the switch that disables resolution (REPO-017) for Git LFS repositories.

### PKG-LFS-002 · LFS endpoint of a repository
Priority: **P2**

Every Git LFS repository answers the LFS API under its own endpoint URL, which differs from the
repository's plain browsing URL by a format-specific path prefix. All Batch API and locking paths
are relative to this endpoint.

Because the repository manager is not the Git host, the client cannot discover the endpoint from
the Git remote URL (Git LFS would otherwise append `.git/info/lfs` to the remote). The endpoint is
configured explicitly in the Git repository, either committed for all users in `.lfsconfig`:

```ini
[lfs]
    url = "https://repo.example.com/lfs/lfs-virtual"
```

or per Git remote in `.git/config`, which Git LFS supports natively:

```ini
[remote "origin"]
    url = https://git.example.com/team/game-assets.git
    fetch = +refs/heads/*:refs/remotes/origin/*
    lfsurl = "https://repo.example.com/lfs/lfs-local"
```

- The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) shows these
  snippets for the selected repository, ready to copy.
- Replication (REPL-001 in [../replication-and-federation.md](../replication-and-federation.md))
  is the exception: a replication target is configured with the repository's plain URL, not with
  the LFS endpoint.

Edge cases: behind a reverse proxy the externally visible base URL (OPS-051 in
[../operations-and-monitoring.md](../operations-and-monitoring.md)) usually has to be set, because
the Batch API hands the client absolute URLs for each object (PKG-LFS-003) and those must point at
the externally reachable address.

## Protocol

### PKG-LFS-003 · Batch API
Priority: **P2**

The Batch API negotiates transfers for many objects in one request.

| | |
|---|---|
| Request | `POST <endpoint>/objects/batch` with `Accept: application/vnd.git-lfs+json` and `Content-Type: application/vnd.git-lfs+json` (a `charset=utf-8` parameter must be accepted) |
| Body | `operation` (`download` or `upload`); optional `transfers` (adapter names; absent means `basic`); optional `ref.name` (fully qualified ref, for ref-aware authorization; may be missing or null); `objects[]` with `oid` and `size` (integer ≥ 0); optional `hash_algo` (default `sha256`) |
| Success | `200` with `transfer` (one of the requested adapters, `basic` if none requested), `objects[]` (each with `oid`, `size`, optional `authenticated`, and `actions`), optional `hash_algo` |
| Download action | Every object needs a `download` action (`href`, optional `header` map, optional `expires_in` seconds or `expires_at` RFC 3339 timestamp; `expires_in` wins when both are given) or a per-object error |
| Upload actions | `upload` and optionally `verify`. For an object the server already has, the `actions` property is omitted entirely, which tells the client to skip it |
| Per-object errors | Inside a `200` response: `error.code` and `error.message`; codes 404 (object does not exist), 409 (hash algorithm not acceptable), 410 (object removed), 422 (validation error) |
| Request errors | 401 credentials needed (should carry an `LFS-Authenticate` header naming the scheme; Basic is assumed when absent); 403 read but no write access (upload only); 404 the caller cannot see a repository of that name; 422 none of the objects to upload is valid. Optional: 406 wrong `Accept`, 413 too many objects, 429 rate limit, 501, 507 insufficient storage, 509 bandwidth limit. Error bodies carry `message`, optional `request_id` and `documentation_url`, and no `objects` |
| Required privilege | `download`: read on the repository; `upload`: deploy on the repository (the specification speaks of read and write access; the mapping onto the system's actions follows IAM-019) |

```json
{"operation": "upload", "transfers": ["basic"], "ref": {"name": "refs/heads/main"},
 "objects": [{"oid": "ad1b8d6e1cafdf33e941a5de462ca7edfa8818a70c79feaf68e5ed53dec414c4", "size": 104857600}]}
```

Edge cases:

- Through a virtual repository, a download batch must find objects across all members, and an
  upload batch must hand out upload actions that write into the default deployment repository.
- Server errors such as 500, 502, 503 and 504 may make the client retry the request.
- The `ref` property was added later in the protocol; a server must work without it. How the
  system uses it (for example for branch-level permissions) is unspecified.

### PKG-LFS-004 · Object upload, download and verification (basic transfer adapter)
Priority: **P2**

The basic transfer adapter is the original one: the specification says every client and server
should support it, the Batch API notes that the Git LFS client currently supports only this
adapter, and it is the default when the request or response names no transfer adapter.

| Operation | Request | Response | Required privilege |
|---|---|---|---|
| Download object | `GET` on the download action's `href`, with the action's headers | `200` with the raw bytes (`application/octet-stream`) | Read |
| Upload object | `PUT` on the upload action's `href`, raw bytes, with the action's headers | `200` | Deploy |
| Verify upload | `POST` on the verify action's `href` with `{"oid": ..., "size": ...}` (LFS media type) | `200` means the object exists on the server | Deploy |

- The action URLs and headers are the server's choice: they may point at the system itself or,
  for example, at pre-signed object-storage URLs, in which case `header` may carry the credentials
  and `authenticated: true` tells the client not to look for credentials of its own.
- Action URLs must be absolute and reachable by the client (PKG-LFS-002).
- Git LFS is among the formats for which the system can redirect downloads to signed
  object-storage URLs (STOR-032 in [../storage-and-blobs.md](../storage-and-blobs.md)); whether
  that redirect is applied to the object `GET`, or already to the `href` handed out in the batch
  response, is unspecified.

Edge cases: whether the system checks that the uploaded bytes hash to the announced `oid` and size
is unspecified; by Git LFS definition the `oid` is the SHA-256 of the content.

### PKG-LFS-005 · Object storage layout and metadata
Priority: **P2**

The system stores each LFS object as an item named by its SHA-256, under a path sharded by the
first four hex characters, the same way the Git LFS client stores objects locally:

```text
<repo-key>/objects/<oid[0:2]>/<oid[2:4]>/<oid>
lfs-local/objects/ad/1b/ad1b8d6e1cafdf33e941a5de462ca7edfa8818a70c79feaf68e5ed53dec414c4   # example
```

- The client sends only the `oid` and the size, so the system stores and processes no further
  metadata for LFS objects: no file name, no Git path, no commit.
- Users may set properties on objects (ART-023 in [../artifacts.md](../artifacts.md)) for their own
  purposes; mapping an object to the file it represents in Git needs external logic that reads the
  pointer files in the Git repository.
- The objects appear in the artifact browser and in search like any other item.

Interactions: content-addressed blob storage deduplicates identical objects across repositories
(STOR-001 in [../storage-and-blobs.md](../storage-and-blobs.md)).

### PKG-LFS-006 · Deploy an LFS object through the UI
Priority: **P2**

In the deploy dialog (ART-006 in [../artifacts.md](../artifacts.md), UI-013 in
[../ui-inventory.md](../ui-inventory.md)), choosing a Git LFS repository as target and ticking
"deploy as Git LFS artifact" computes the target path from the uploaded file's SHA-256 according to
PKG-LFS-005. The user reviews the options and deploys.

| Option | Meaning | Default |
|---|---|---|
| Deploy as Git LFS artifact | Place the file at its content-addressed object path instead of a user-chosen path | unspecified |

Edge cases: a file deployed this way is only useful to Git LFS clients if a pointer file with the
same `oid` and size is committed to the Git repository; the dialog does not create one.

### PKG-LFS-007 · File locking
Priority: **P2**

With Git LFS file locking a user claims a tracked file for exclusive editing; depending on the
policy of the Git host, parallel changes by others are then warned about or refused. The target is
content Git cannot merge, such as 3D models, large images, video or design files. Locks only help
when the team agrees to take one before editing.
The system supports the Git LFS locking API; paths are relative to the LFS endpoint.

| Operation | Request | Response | Privilege (spec wording) |
|---|---|---|---|
| Create lock | `POST <endpoint>/locks` with `path` (relative to the repository root) and optional `ref.name` | `201` with `lock` (`id` string, `path`, `locked_at` RFC 3339, optional `owner.name` from the caller's identity); `409` with the existing `lock` when the path is already locked; `403` without push access | Push access |
| List locks | `GET <endpoint>/locks` with optional query values `path`, `id`, `cursor`, `limit`, `refspec` | `200` with `locks[]` (empty array when none) and optional `next_cursor` | Pull access |
| List locks for verification | `POST <endpoint>/locks/verify` with optional `ref.name`, `cursor`, `limit` | `200` with `ours[]` (locks owned by the caller), `theirs[]` (locks owned by others), optional `next_cursor` | Push access |
| Delete lock | `POST <endpoint>/locks/<id>/unlock` with optional `force` and `ref.name` | `200` with the deleted `lock`; `403` when the caller is not the owner and `force` is not set, or lacks push access | Push access; deleting another user's lock needs `force` |

- A path can be locked by one user only (exclusive lock per path and repository).
- Before a push, the client calls the verification operation: changed files matching `theirs` stop
  the push; files matching `ours` are listed so the user can unlock them afterwards.
- A server without locking should answer the locking paths with 404 (any 404 works; one with a
  JSON error body gives a better client message); this does not stop pushes.
- Errors carry `message` and optional `request_id` and `documentation_url`, like the Batch API.

Client commands (Git LFS): `git lfs lock <path>`, `git lfs unlock <path>` (or `-i <id>`, `--force`),
`git lfs locks` (filters by path, id, local or cached locks, verification listing, limit), each
with `-r <remote>` and `--json`.

Mapping "push access" and "pull access" onto the system's actions (deploy and read are the obvious
candidates, IAM-019 in [../identity-and-access.md](../identity-and-access.md)) and which role may
force-unlock are unspecified. Whether pushes to the Git host are blocked by locks
depends on the Git host and the client, not on the repository manager.

### PKG-LFS-008 · Client authentication
Priority: **P2**

- Git LFS authenticates with HTTP Basic credentials. The user enters the system's user name and
  password or token, not the Git host's credentials.
- Anonymous access to Git LFS repositories is off by default (global security setting, IAM-005
  in [../identity-and-access.md](../identity-and-access.md)). When anonymous access is allowed,
  the user may enter blank credentials at the prompt.
- Git credential helpers store the credentials so the client authenticates without prompting.
  Security note: Git's default helpers store credentials in plain text, which calls for extra care.
- A `401` should name the scheme in `LFS-Authenticate` rather than `WWW-Authenticate`, so browsers
  do not open a password dialog.
- Clients that reach the Git LFS server over SSH run `git-lfs-authenticate` on the SSH host to
  obtain an endpoint and headers; the system's SSH authentication (IAM-050 in
  [../identity-and-access.md](../identity-and-access.md)) serves Git LFS clients.

Edge cases: self-signed certificates behind a proxy make clients fail TLS verification; disabling
verification in Git's configuration (`http.sslverify = false`) is a workaround to be cleared with
the administrator. Security note: that disables server authentication for the whole Git
configuration scope.

## Maintenance

### PKG-LFS-009 · Cleanup of unreferenced LFS objects
Priority: **P2**

Objects that no Git ref references any more stay in the repository until removed. The companion
CLI (BLD-036 in [../builds-and-ci.md](../builds-and-ci.md)) offers a cleanup command that runs
against a local clone of the Git repository:

1. Determine the LFS repository: from the `--repo`-style argument, or auto-detected from
   `lfs.url` in `.lfsconfig` or `.git/config`; without either, the command fails with a detection
   error.
2. Search the repository for all stored objects (SRCH-013 in
   [../search-and-query.md](../search-and-query.md)).
3. Compute the set of objects referenced by the preserved Git refs of the local clone.
4. Delete every stored object not in that set (ART-034 in [../artifacts.md](../artifacts.md)),
   after a confirmation prompt.

<details>
<summary>Command options</summary>

| Option | Meaning | Default |
|---|---|---|
| Path to the Git working copy | Directory that contains `.git` | current directory |
| Repository | LFS repository key to clean | auto-detected from the LFS URL |
| Preserved refs | Comma-separated Git refs whose objects are kept; wildcards allowed (quote them in the shell) | `refs/remotes/*` |
| Dry run | List the objects that would be deleted, delete nothing | off |
| Quiet | Skip the confirmation prompt | off; on when the `CI` environment variable is set |
| Retries / wait between retries | HTTP retries and pause (`s` or `ms` suffix) | 3 / 0 |
| Skip TLS verification | | off |
| Output format | JSON or table | text |

</details>

Required privilege: read (search) and delete on the LFS repository.

Edge cases:

- Only refs present in the local clone are considered. Objects referenced by branches or tags
  that the clone has not fetched count as unreferenced and are deleted unless the preserved refs
  cover them. A dry run before the first real run is the recommended practice.
- Deleted objects go to the trash can where enabled (ART-036); the storage is reclaimed by
  garbage collection (STOR-036 in [../storage-and-blobs.md](../storage-and-blobs.md)).
- Open: `lfs.url` may hold the repository's plain URL or, as in the LFS client configuration of
  PKG-LFS-002, the LFS endpoint; whether auto-detection accepts one of the two forms or both is
  for the spec to decide.

There is no server-side, Git-aware cleanup; retention policies (RET-001 in
[../retention-and-cleanup.md](../retention-and-cleanup.md)) only see paths, ages and properties.

## Notes for implementers

- Git LFS replaces a file with its pointer text when an object download fails (wrong credentials,
  network error). Users then silently work with pointer files under the original names, so batch
  and download errors must be explicit and fast, never an HTML login page with status 200.
- Omit `actions` for objects the server already has in an upload batch; returning an upload action
  makes the client send gigabytes again.
- Return per-object errors with HTTP 200; reserve non-200 statuses for problems with the request as
  a whole. A 404 at batch level means "repository unknown", not "object missing".
- Action `href`s are absolute. Derive them from the configured external base URL, not from the
  inbound `Host` header, or clients behind proxies get unreachable URLs.
- Set an expiry (`expires_in`, preferred over `expires_at`) on any action that carries a
  temporary credential, so the client knows when the action stops working.
- Accept `application/vnd.git-lfs+json; charset=utf-8` and send `application/vnd.git-lfs+json` on
  every Batch and locking response.
- Answer all locking paths with 404 when locking is disabled: the specification guarantees only
  that a 404 does not halt a push and says nothing about how clients treat other statuses there.
- For a virtual repository, decide which members count for "already present" in an upload batch.
  Checking all members means an object cached only in a remote member is never written to the
  default deployment repository; which the system does is unspecified.
- Unreferenced-object cleanup is client-side and only as good as the refs in the local clone;
  expose a dry run and a clear list of what will be deleted in any server-side equivalent.
