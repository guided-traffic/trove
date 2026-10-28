# Content API operation catalog

This page catalogs the management and content operations a full-featured repository manager
exposes over HTTP besides the native package-manager protocols: storage items and their
properties, deploy and download helpers, copy, move and delete, signed URLs, storage maintenance,
format-specific index recalculation and helper endpoints, repository configuration, federation,
replication, search, trash and garbage collection, first-generation release sets and their
distribution, server-side plugins, live logs, webhook subscriptions and build information. Each
entry states purpose, inputs, outputs, error cases, required privilege and notable behavior,
described generically; it does not prescribe paths or field names. Behavior of the underlying
features lives on the pages linked from each entry. User, group, token, permission, OIDC and
system administration operations are in [api-admin-operations.md](api-admin-operations.md).

Entries marked **MVP** together form the minimal management API the first release needs:
repository create, read, update, list and delete; item information; deploy, download and delete
of files; an on-demand garbage-collection run; and the common conventions.
Everything else is graded by the rubric in [README.md](README.md).

## Conventions

### API-001 · Common operation conventions
Priority: **MVP**

Conventions shared by the operations on this page, as far as they are specified.

| Convention | Behavior |
|---|---|
| Authentication | Every operation except the webhook operations accepts HTTP Basic and a bearer access token; Basic may carry a legacy API key in place of the password. A legacy API-key request header is accepted in addition only by the format-specific helpers, repository configuration, federation, search, first-generation release-set storage and retention, plugin and live-log operations; item, deploy, download, copy/move, signed-URL, storage, replication, trash, garbage-collection, build and distribution-service operations accept Basic and bearer only. Webhook operations accept bearer access tokens only; the storage summary requires an access token. |
| Anonymous access | Many read operations require a permission on the target and allow this caller to be anonymous: the anonymous user qualifies when anonymous access is enabled and it holds that permission. |
| Error envelope | JSON object with a list of errors, each carrying the HTTP status and a human-readable message. |
| Common statuses | 400 malformed request or invalid combination; 401 missing or invalid credentials; 403 authenticated but lacking the permission; 404 target missing; 500 unexpected failure. |
| Media types | JSON is always accepted and produced; a product-specific JSON media type per document kind is accepted as an alias with the same shape. |
| Boolean query flags | Usually `0`/`1` integers; some newer operations use `true`/`false`. |
| Repository filters | A comma-separated list of repository keys. |
| Times | Older operations use milliseconds since the Unix epoch, newer ones ISO 8601 strings; some accept both. |
| Plain-text results | Several mutating operations reply with a plain-text confirmation instead of JSON. |
| Multi-valued parameters | `key=value1,value2|key2=value3` style lists; reserved characters escaped with an encoded backslash (`%5C`) or separated with `;` where the operation says so. |

Interactions: EVT-070 in [events-and-extensibility.md](events-and-extensibility.md)
(machine-readable API description).

## Unreferenced binary pruning

Pruning removes binaries that exist in the blob store but are referenced by no item. The process
walks the store directory by directory, can be stopped, and can be resumed from a recorded
position. It is separate from regular garbage collection (API-113).

### API-002 · Start, monitor and stop unreferenced-binary pruning
Priority: **P1**

Three operations: start (or resume), status, stop.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Start or resume | Optional body: dry-run flag (default false); directory to resume from (absent = first directory); position within that directory to resume from (default 0; taken from the status report's last-handled-directory processed count); minimum binary age in days (default 1; 0 = ignore age). | 202 with an informational message. | 401; 403; 412 when a run is already active or cannot start. |
| Status | None. | State (running, stopped, finished, error); dry-run flag; timing (start time in epoch milliseconds and as local date-time without offset, elapsed milliseconds and as text, report time in both forms); progress as processed directories over total (e.g. `3 / 256`); totals (binaries processed, binaries removed, bytes reclaimed); last handled directory (name, its own state, processed, removed, bytes reclaimed, timing); error text when failed. | 401; 403; 412 when no pruning run exists. |
| Stop | None. | 202 with an informational message. | 401; 403; 412 when no run is active. |

Privilege: system administrator for all three.

Behavior:

- Dry run counts orphans per directory without deleting them, to estimate the effect.
- Binaries younger than the minimum age are skipped.
- Date-time strings in the status carry no time-zone offset (local ISO 8601, not RFC 3339).

Interactions: STOR-038 in [storage-and-blobs.md](storage-and-blobs.md); API-113; API-029.

## Item information and properties

An item is a file or folder inside a repository. The item-information operation has several
mutually exclusive modes selected by query parameters; each mode is listed as its own entry
because privilege and output differ.

### API-003 · Item information (file or folder)
Priority: **MVP**

Returns the metadata of one file or folder.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and item path. No mode parameter. |
| Output, folder | Self link, repository key, path, created time and creator, last-modified time and modifier, last-updated time, and the direct children (relative link and folder flag each). |
| Output, file | Self link, download URL, repository key, path, upstream URL (cached remote files), created/creator, modified/modifier, last-updated, size in bytes (as a string), MIME type, actual checksums (MD5, SHA-1, SHA-256) and the original checksums supplied by the client or upstream (same three). |
| Errors | 400 invalid parameter combination or malformed path; 401; 403; 404 item not found. |
| Privilege | Read access on the item; anonymous allowed when enabled. |

Behavior: JSON and a product-specific media type per result kind are both offered. Sizes are
strings, times ISO 8601.

UI: item details pane, see ART-041 in [artifacts.md](artifacts.md).

Interactions: ART-027 in [artifacts.md](artifacts.md).

### API-004 · File list
Priority: **P1**

Lists the files (and optionally folders) under a folder, flat or recursively.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, folder path, list mode; optional deep flag (0/1); optional maximum depth (deep mode only); optional include-folders flag (0/1); optional include-metadata-timestamps flag (0/1); optional include-root-path flag (0/1). |
| Output | Self link, folder creation time, and per entry: relative path, size (`-1` for folders), last-modified time, folder flag, SHA-1 (files), and — when requested — a map of metadata names to their timestamps. |
| Errors | 400; 401; 403; 404. |
| Privilege | Authenticated (non-anonymous) user with read access. |

Behavior: flat listing is the default. Listing parameters may be combined with the list mode but
with no other mode.

Interactions: ART-028 in [artifacts.md](artifacts.md).

### API-005 · Download statistics of a file
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Repository key, file path, statistics mode. |
| Output | Self link, last-download time (milliseconds since epoch), download count, user who downloaded last. |
| Errors | 400; 401; 403; 404. |
| Privilege | Read access. |

Interactions: ART-029 in [artifacts.md](artifacts.md); API-098.

### API-006 · Last-modified item under a path
Priority: **P2**

Returns the most recently modified item at a path; for a folder, the search is recursive.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, path, last-modified mode. |
| Output | Link to the most recently modified item and its last-modified time. |
| Errors | 400; 401; 403; 404. |
| Privilege | Authenticated user with deploy permission. |

Interactions: ART-030 in [artifacts.md](artifacts.md).

### API-007 · Read item properties
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Repository key, item path, properties mode with an optional comma-separated list of property keys. |
| Output | Self link and a map from property key to its list of values. |
| Errors | 400; 401; 403; 404 item not found. |
| Privilege | Read access; anonymous allowed when enabled. |

Interactions: ART-022 in [artifacts.md](artifacts.md).

### API-008 · Effective permissions on an item
Priority: **P1**

Lists which users and groups hold which actions on an item.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, item path, permissions mode. |
| Output | Self link and principals: users (name to list of action codes) and groups (name to list of action codes). |
| Errors | 400; 401; 403; 404. |
| Privilege | Only the general rule for basic item information is specified (read access, anonymous allowed); whether a stricter privilege applies is unspecified. |

Interactions: ART-031 in [artifacts.md](artifacts.md); [identity-and-access.md](identity-and-access.md).

### API-009 · Set item properties
Priority: **P1**

Attaches properties to a file, folder, repository or release set.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, item path; property list in the form `key1=value1[,value2][|key2=value3]` (pairs may also be separated by `;`); recursive flag (0/1, default 1 for folders). |
| Output | 204, no body. |
| Errors | 400 missing or malformed property list; 401; 403 without annotate permission; 404 item or repository not found. |
| Privilege | Annotate permission on the item; anonymous allowed when enabled. |

Behavior:

- To use `,`, `\`, `|` or `=` inside a key or value, prefix it with an encoded backslash
  (`%5C`).
- Only local repositories and remote caches support properties.
- An optional system-wide cap on the number of items a recursive property change may touch makes
  the request fail when a folder holds more items; off by default, no default value when on.

Interactions: ART-023 in [artifacts.md](artifacts.md); EVT-005 in
[events-and-extensibility.md](events-and-extensibility.md).

### API-010 · Delete item properties
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Repository key, item path; comma-separated property keys (mandatory); recursive flag (0/1, default 1 for folders). |
| Output | 204, no body. |
| Errors | 400 missing or malformed key list; 401; 403; 404. |
| Privilege | A permitted user, anonymous allowed when enabled; the exact action required is unspecified (set and partial update require annotate). |

Behavior: local repositories and remote caches only.

Interactions: ART-025 in [artifacts.md](artifacts.md).

### API-011 · Update item properties (partial)
Priority: **P1**

Adds, changes and removes several properties in one JSON request.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, item path; optional recursive flag (0/1); optional atomic flag (0/1); JSON body holding a map from property key to a single string, an array of strings, or null (null removes the key). |
| Output | 204, no body. |
| Errors | 400 malformed body or missing property map; 401; 403 without annotate permission; 404. |
| Privilege | Annotate permission. |

Behavior: the exact semantics of the atomic flag are unspecified. Local
repositories and remote caches only.

Interactions: ART-024 in [artifacts.md](artifacts.md).

### API-012 · Compute SHA-256 for legacy items
Priority: **P2**

Computes the SHA-256 of a file — or, recursively, of every file in a folder — and stores it as a
property. Exists for items stored before SHA-256 was computed natively.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and path (JSON body, both mandatory). |
| Output | 200. |
| Errors | 400; 401; 403. |
| Privilege | System administrator. |

Behavior: only meaningful for a system that once stored items without SHA-256; a system that
computes SHA-256 on every upload has nothing to backfill.

Interactions: ART-010 in [artifacts.md](artifacts.md); STOR-005 in [storage-and-blobs.md](storage-and-blobs.md).

## Deploy

Deploy operations write files into local (or federated) repositories over plain HTTP, independent
of any package-manager protocol. Properties can be attached in the same request (see ART-003 in
[artifacts.md](artifacts.md)).

### API-013 · Deploy a file or create a folder
Priority: **MVP**

Uploads one file to a repository path, or creates an empty folder.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and target path; the file content as the request body. To create a folder, the path ends with `/` and the body is empty. Optional request headers carrying the expected SHA-1 and/or SHA-256 of the content. Optional properties in the path (see ART-003). |
| Output | 201 with: repository key, path, created time and creator, download URL, MIME type, size (string), actual checksums (MD5, SHA-1, SHA-256), the original checksums as supplied, and a self link. |
| Errors | 400 invalid path or malformed request; 401; 403 without deploy permission; 404 repository not found; 405 when the path resolves to a repository class that does not accept uploads (for example a virtual repository); 409 duplicate artifact or checksum mismatch. |
| Privilege | Deploy permission on the target path; anonymous allowed when enabled. |

Behavior:

- When an expected checksum is supplied and does not match the received content, the upload is
  rejected.
- For large uploads the created time can be later than the last-modified time: last-modified
  records when the upload started, created is set when the upload is committed.

Interactions: ART-002, ART-001 and ART-003 in [artifacts.md](artifacts.md); REPO-038 in
[repositories.md](repositories.md) (default deployment repository of a virtual repository);
REPO-022 (deployment checksum policy); EVT-004 in
[events-and-extensibility.md](events-and-extensibility.md).

### API-014 · Deploy by checksum
Priority: **P1**

Creates a file from content the system already stores, without transferring the bytes.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and target path; a mandatory request header that switches to checksum deployment; at least one checksum header: SHA-1, SHA-256, or a generic checksum header whose algorithm is inferred from its length (32 hex = MD5, 40 = SHA-1, 64 = SHA-256). Body optional. Optional properties as with API-013. |
| Output | 201 with the same document as API-013. |
| Errors | 400 missing or invalid checksum header; 401; 403 without deploy permission; 404 when no readable content with that checksum exists, meaning the client must upload the content. |
| Privilege | Deploy permission on the target; anonymous allowed when enabled. The content is reused only if the caller can read an existing artifact with that checksum. |

Behavior: a 404 tells the client to upload the content with API-013. If the switch
header is explicitly false, a normal upload with body takes place and succeeds with 201; the
submitted checksums are then recorded as uploaded and identical.

Interactions: ART-004 in [artifacts.md](artifacts.md); STOR-003 in [storage-and-blobs.md](storage-and-blobs.md); API-096.

### API-015 · Deploy and extract an archive
Priority: **P2**

Uploads an archive and stores its extracted entries under a target folder, preserving the
archive's directory structure; the archive file itself is not stored.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and a path ending in the archive file name (entries are extracted relative to its folder); a mandatory request header that enables extraction; an optional header requesting atomic extraction; the archive (zip, tar, tar.gz or tgz) as body. |
| Output | 201, plain text. |
| Errors | 400 invalid archive or missing header; 401; 403 without deploy permission. |
| Privilege | Deploy permission; anonymous allowed when enabled. |

Behavior: one HTTP request for the whole archive. Entries are deployed sequentially by default; a
system setting deploys them with several parallel workers per archive. Compressed archives cost
noticeably more CPU than plain tar. What atomic extraction guarantees on failure is unspecified.

Interactions: ART-005 and ART-006 in
[artifacts.md](artifacts.md).

## Download and retrieval helpers

### API-016 · Download a file
Priority: **MVP**

Returns the content of a file from a repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and file path. Optional "skip statistics" flag (default false) that leaves download statistics untouched. Optional trace flag that simulates the request and returns a textual explanation of how it would be resolved. Optional property constraints in the path (see ART-012). |
| Output | The file bytes; with trace, a plain-text resolution report. |
| Errors | 401; 403 without read permission; 404 not found. |
| Privilege | Read permission; anonymous allowed when enabled. |

Latest-version placeholders in the version segment of the path:

| Placeholder | Resolves to |
|---|---|
| Maven `SNAPSHOT` version | Latest integration (snapshot) build of that version. |
| Release placeholder | Latest release version; for non-Maven layouts chosen by alphabetical sorting. |
| Integration placeholder | Latest integration version; non-Maven layouts only, replacing the folder and file integration revision tokens of the repository layout. |

Behavior:

- Release and integration placeholders cannot be mixed in one path.
- Only local repositories, remote caches and virtual repositories take part in latest-version
  resolution; the layout must define both the folder and the file integration revision tokens.
- A system option switches "latest" from version ordering to creation date (requires restart).

Interactions: ART-011, ART-013 and
ART-012 in [artifacts.md](artifacts.md); REPO-036 and REPO-046 to REPO-049 in
[repositories.md](repositories.md); API-105; API-005.

### API-017 · Download a single entry from an archive
Priority: **P2**

Returns one entry of a stored archive without downloading the whole archive.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, archive path, and the entry path, separated from the archive path by `!/` (for example `…/lib-1.0.jar!/META-INF/MANIFEST.MF`). |
| Output | The entry's bytes (text or binary). |
| Errors | 400 invalid path, archive missing, or malformed entry path; 401; 403; 404 archive or entry missing. |
| Privilege | Read permission; anonymous allowed when enabled. |

Behavior: the separator must be exactly `!` followed by `/`.

Interactions: ART-014 and
ART-045 in [artifacts.md](artifacts.md); API-093.

### API-018 · Download all artifacts of a build as an archive
Priority: **P2**

Packs the artifacts of one build run into a single archive.

| Aspect | Detail |
|---|---|
| Inputs | Build name and number (both mandatory; the number may be a "latest" placeholder); optional build status (latest run with that status); optional repository list; archive type (zip, tar, tar.gz, tgz; mandatory); optional mappings, each an input regular expression and an optional output path template that may reference capture groups (e.g. `$1/sources/$2.jar`). |
| Output | The archive (zip, tar or gzip media type). |
| Errors | 400 malformed request; 401; 403. |
| Privilege | Read permission; anonymous allowed when enabled. |

Behavior: mappings both filter the artifacts and, through the output template, rename them inside
the archive.

Interactions: ART-016 in [artifacts.md](artifacts.md); API-107;
[builds-and-ci.md](builds-and-ci.md).

### API-019 · Download a folder or repository as an archive
Priority: **P2**

Packs everything below a folder (or a repository root) into one archive.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; path (empty for the root); archive type (zip, tar, tar.gz, tgz; mandatory); optional flag to include checksum files. |
| Output | The archive. |
| Errors | 400 invalid path characters or archive type; 401; 403 without read permission on the path, or when the vulnerability scanner blocks the download; 404. |
| Privilege | Read permission on the path. |

Behavior: only local repositories and remote caches; folder download must be enabled in the
system settings.

Interactions: ART-015 in [artifacts.md](artifacts.md);
[operations-and-monitoring.md](operations-and-monitoring.md) (general settings);
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### API-020 · Server-side download (cache warm-up)
Priority: **P2**

Makes the server download a file — for example to fill a remote repository's cache — without
streaming the bytes to the caller, or while streaming only progress marks.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and file path; content mode: omitted (full content), none, or progress; progress-mark interval in bytes (default 1024). |
| Output | Full content; nothing; or a text stream of progress marks ending with a completion line (bytes transferred over total). |
| Errors | 400 invalid or non-printable path; 401; 403; 404. |
| Privilege | Read permission; anonymous allowed when enabled. |

Interactions: ART-017 in [artifacts.md](artifacts.md); [remote-repositories.md](remote-repositories.md).

### API-021 · RubyGems version list
Priority: **P2**

Returns all versions of one gem with their metadata.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, gem name. |
| Output | Array of versions: authors, build time, creation time, description, download count, metadata map, version number, summary, platform, required Ruby version, prerelease flag, licenses, requirements, SHA-256. JSON or YAML. |
| Errors | 401; 404 gem not found. |
| Privilege | Authenticated user. |

Interactions: ART-052 in [artifacts.md](artifacts.md); [formats/README.md](formats/README.md).

## Copy, move and delete

### API-022 · Delete a file or folder
Priority: **MVP**

Deletes a file or a folder from a local repository or a remote cache.

| Aspect | Detail |
|---|---|
| Inputs | Repository key and path. |
| Output | 204, no body. |
| Errors | 401; 403 without delete permission; 404 item missing; 405 when the path resolves to a repository class that does not support deletion (for example a remote or virtual repository; a remote repository's cache does support deletion). |
| Privilege | Delete permission; anonymous allowed when enabled. |

Interactions: ART-034 in [artifacts.md](artifacts.md); the trash can
(API-111); EVT-004 in [events-and-extensibility.md](events-and-extensibility.md).

### API-023 · Copy a file or folder
Priority: **P1**

Copies a file or a folder tree to another path, in the same or another repository.

| Aspect | Detail |
|---|---|
| Inputs | Source repository key and path; target as `/<repository key>/<path>` (mandatory); dry-run flag (0/1, default 0); suppress-layout-translation flag (0/1, default 1); fail-fast flag (0/1). |
| Output | List of messages, each with a level (info, warning, error) and text, e.g. a summary of how many files and folders were copied. |
| Errors | 400 invalid parameters or source missing; 401; 403. |
| Privilege | A permitted user; anonymous allowed when enabled. The exact permissions checked on source and target are unspecified. |

Behavior:

- Supported for local, remote (cache) and federated repositories.
- Dry run validates and reports without changing anything.
- With layout translation enabled (flag 0), paths are translated between the source and target
  repository layouts; the default suppresses translation.

Interactions: ART-032 in [artifacts.md](artifacts.md); REPO-051 in
[repositories.md](repositories.md); STOR-002 in [storage-and-blobs.md](storage-and-blobs.md); API-163.

### API-024 · Move a file or folder
Priority: **P1**

Moves a file or folder tree to another path or repository.

| Aspect | Detail |
|---|---|
| Inputs | As API-023. |
| Output | Message list as API-023. |
| Errors | 400; 401; 403. |
| Privilege | As API-023. |

Behavior: supported for local repositories only. Dry run and layout translation as for copy.

Interactions: ART-033 in [artifacts.md](artifacts.md); API-023.

## Signed download URLs

### API-025 · Create a signed download URL
Priority: **P2**

Issues a time-limited URL that lets anyone holding it download one file without credentials.

| Aspect | Detail |
|---|---|
| Inputs | Full path of the file (repository key and path; mandatory); either a validity in seconds from now or an absolute expiry time (epoch milliseconds). |
| Output | The signed URL as a JSON string: the normal download URL plus a signature query parameter. |
| Errors | 400 malformed request; 401; 403 without administrator or manage permission. |
| Privilege | System administrator, or manage permission on the repository. |

Behavior: default validity 24 hours; maximum one year (365 days). The JSON request must not
contain an encoded space (`%20`).

Interactions: ART-018 in [artifacts.md](artifacts.md); API-026.

### API-026 · Rotate the signed-URL key
Priority: **P2**

Replaces the key used to sign and verify signed URLs.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | 201. |
| Errors | 401; 403. |
| Privilege | System administrator. |

Behavior: signed URLs issued before the rotation no longer validate. Whether a grace period
applies is unspecified.

Interactions: ART-019 in [artifacts.md](artifacts.md).

## Legacy external distribution

### API-027 · Push artifacts to a discontinued external distribution service
Priority: **Out of scope**

Deployed artifacts to an external public distribution service (since discontinued) and recorded
them in a distribution repository. Inputs were a target distribution repository, artifact paths,
publish (default true), overwrite (default false), signing passphrase, asynchronous (default
false) and dry-run (default false) flags. Deprecated; kept here only for completeness of the
inventory. The successor is distribution of release sets (API-125). See
ART-053 in [artifacts.md](artifacts.md).

## System operations

### API-028 · Invalidate cached remote metadata
Priority: **P1**

Marks the cached metadata files under a remote repository cache path as stale, so the next
request refreshes them from the upstream.

| Aspect | Detail |
|---|---|
| Inputs | Repository path (cache key plus optional sub-path). |
| Output | JSON message confirming the path that was processed. |
| Errors | 401; 403 without manage or delete permission on the remote repository. |
| Privilege | Manage or delete permission on the remote repository. |

Behavior:

- Only metadata files (for example package indexes fetched from public registries such as the npm
  registry, PyPI or Maven Central) are invalidated; immutable binaries stay cached.
- On the next request for an invalidated file the upstream is tried first; if it is unreachable
  the stale copy is served.
- Clients may see slower responses right after invalidation, while metadata is refetched.
- Use case: repairing corrupted or inconsistent cached metadata.

Interactions: ART-050 in [artifacts.md](artifacts.md); [remote-repositories.md](remote-repositories.md)
(metadata cache period, offline behavior).

### API-029 · List background tasks
Priority: **P2**

Lists background jobs that are scheduled or running.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Per task: identifier, type, state (scheduled, running, stopped, cancelled), description, node identifier in a cluster, and start time for running tasks. |
| Errors | 401; 403. |
| Privilege | System administrator. |

Interactions: ART-051 in [artifacts.md](artifacts.md); [operations-and-monitoring.md](operations-and-monitoring.md);
[ha-and-deployment.md](ha-and-deployment.md).

### API-030 · Rebalance redundant storage
Priority: **P2**

Triggers balancing between the redundant storage units of a sharded file store; skipped when
balancing already runs. Balancing starts at once instead of only being flagged to run after the
next full garbage collection.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Plain-text confirmation. |
| Errors | 401; 403. |
| Privilege | System administrator. |

Behavior: relevant only to sharded file stores with redundant storage units; an S3-backed blob
store leaves redundancy to the object store.

Interactions: STOR-015 in [storage-and-blobs.md](storage-and-blobs.md).

## Storage information

### API-031 · Storage summary
Priority: **P1**

Reports storage use for binaries, the blob store and each repository.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Binaries: number of distinct binaries, their total size, total logical artifact size, the stored binary size as a percentage of the logical artifact size (a measure of what deduplication saves), item count, artifact count. Store: storage type, location, total, used and free space. Per repository: key, class, folder count, file count, used space, item count, format, share of total. |
| Errors | 401; 403. |
| Privilege | System administrator, or an access token scoped to read storage information. |

Behavior: numeric values are pre-formatted strings with thousands separators, units and
percentages (for example `"3.48 GB"`, `"5.82%"`), except the per-repository counts, which are
integers. The summary can be recalculated on demand with API-032.

Interactions: STOR-039 in [storage-and-blobs.md](storage-and-blobs.md).

### API-032 · Recalculate the storage summary
Priority: **P1**

Schedules recalculation of the storage summary.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | 202 with a plain-text confirmation. |
| Errors | 401; 403. |
| Privilege | System administrator. |

## Metrics

### API-033 · Application metrics in OpenMetrics format
Priority: **P1**

Returns the system's metrics in the [OpenMetrics](https://openmetrics.io/) / Prometheus text
exposition format.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Plain-text metrics. |
| Errors | 401; 403. |
| Privilege | System administrator. |

Behavior: two variants exist — one for self-managed installations and one for managed (SaaS)
deployments, the latter with application-level metrics such as free disk space of the
application's home directory. Metric names and the exact set of metrics are not specified here.

Interactions: [operations-and-monitoring.md](operations-and-monitoring.md).

## Format-specific helper operations

Operations tied to one package format: browsing helpers for formats whose clients need them, and
recalculation of repository indexes. Format behavior itself lives in
[formats/README.md](formats/README.md). Unless an entry says otherwise, the required privilege
for these operations is unspecified (only that 401 and 403 can occur). Every recalculation
operation replies with plain text.

### API-034 · Puppet module and release listing
Priority: **P2**

Four read operations on a Puppet repository.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| List modules | Repository key. | Paginated envelope: total, limit, offset, links to the current, next and previous page, results. | 401; 403; 500 |
| Module details | Repository key; module identifier `<owner>-<module>`. | Module document (shape unspecified). | 401; 403; 404; 500 |
| List releases | Repository key. | Paginated envelope as for modules. | 401; 403; 500 |
| Release details | Repository key; release identifier `<owner>-<module>-<version>`. | Release document (shape unspecified). | 401; 403; 404; 500 |

Privilege: unspecified. Page size and paging parameters: unspecified.

### API-035 · Source-control repository listing (tags, branches, refs)
Priority: **P2**

For source-control (VCS) repositories, which serve the content of hosted Git repositories
(including GitHub releases and repositories nested in subgroups): list the tags or branches of
a hosted source repository, or download its Git references file.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| List tags | Repository key; owner (user or organization); source repository name. | Array of entries: name, commit ID, branch flag. | 401; 403; 500 |
| List branches | As for tags. | As for tags. | 401; 403; 500 |
| Download references file | As for tags. | The Git references document. | 401; 403; 500 |

Behavior: also applies to source repositories nested in subgroups. Privilege: unspecified.

### API-036 · Source-control snapshot archives
Priority: **P2**

Downloads a complete tag, branch, commit or release of a hosted source repository as one archive
through a source-control remote repository.

| Operation | Inputs | Notes |
|---|---|---|
| Tag archive | Repository key, owner, source repository, tag; archive format (tar.gz default, or zip). | A short form omits the source repository and uses the owner as repository. |
| Branch archive | Repository key, owner, source repository, branch; archive format; optional property filter (e.g. `qa=approved`). | Short form as for tags. |
| Commit archive | Repository key, owner, source repository, commit ID; archive format. | — |
| Release archive | Repository key, owner, source repository, release name; archive format. | Release archives of GitHub releases. |
| Subgroup tag / branch archive | As tag or branch, for a source repository nested in a subgroup (addressed through an extra path segment that marks archive references); optional property filter. | Whether these variants take an archive format is unspecified. |

Output: the archive bytes. Errors: 401; 403; 404; 500. Privilege: unspecified.

Behavior: the property filter makes the download conditional on matching properties. Open: tag
downloads either accept the filter as branch downloads do or do not accept it; the spec decides.

### API-037 · Single file from a source-control tag or branch
Priority: **P2**

Downloads one file from a tag or branch without fetching the whole snapshot.

| Aspect | Detail |
|---|---|
| Inputs | Repository key, owner, source repository (or the short form without it), and a combined reference `<tag or branch>!<file path>`. Subgroup variants use the same extra path segment as the subgroup snapshot archives (API-036). |
| Output | The file bytes. |
| Errors | 401; 403; 404; 500. |
| Privilege | Unspecified. |

### API-038 · Recalculate the index of other formats
Priority: **P2**

One operation per format that rebuilds the repository-level index from the packages stored in the
repository. All take the repository key; the table lists what differs.

<details>
<summary>Per-format recalculation operations</summary>

| Format | Repository classes | Extra inputs | Execution |
|---|---|---|---|
| Alpine | Local | — | Asynchronous |
| CocoaPods | unspecified | — | unspecified |
| Opkg | Local; covers every feed location | Asynchronous flag; write-properties flag (0/1) | Synchronous by default |
| Bower | unspecified | — | unspecified |
| CRAN | Local | Asynchronous flag | Synchronous by default |
| Cargo | Local and remote | Asynchronous flag | Synchronous by default |
| Conda | Local | Asynchronous flag | Synchronous by default |
| Conan | Local | Asynchronous flag | Synchronous by default |
| Pub (Dart/Flutter) | Local and remote | Asynchronous flag | Asynchronous |
| RubyGems | unspecified | — | unspecified |
| Swift | unspecified | — | unspecified |
| Terraform | unspecified; recalculates the module or provider index | — | unspecified |

</details>

Output: plain text. Errors: 401; 403; 500. Privilege: unspecified.

Interactions: REPO-009 in [repositories.md](repositories.md).

### API-039 · Recalculate RPM (YUM) metadata
Priority: **P2**

Rebuilds the YUM metadata of a repository from the RPM packages it holds.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; for virtual repositories, the path of the repodata folder to calculate; asynchronous flag. |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: local, virtual and federated repositories. Synchronous by default.

### API-040 · Recalculate NuGet metadata
Priority: **P1**

Re-reads every NuGet package in the repository and re-derives the package properties from each
package's embedded nuspec manifest.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: local repositories, remote caches, remote and virtual repositories. Synchronous or
asynchronous execution is unspecified.

Interactions: REPO-030 in [repositories.md](repositories.md);
[formats/README.md](formats/README.md) (NuGet).

### API-041 · Recalculate npm indexes
Priority: **P1**

Rebuilds the npm package indexes of a repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: local repositories.

Interactions: REPO-029 in [repositories.md](repositories.md);
[formats/README.md](formats/README.md) (npm).

### API-042 · Calculate a Maven index
Priority: **P2**

Calculates and caches a Maven repository index for the named repositories.

| Aspect | Detail |
|---|---|
| Inputs | Comma-separated repository keys; force flag (0/1). |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: for a virtual repository, all member repositories that the aggregated index should
include must be named explicitly.

### API-043 · Calculate Maven metadata
Priority: **P1**

Recalculates the `maven-metadata.xml` files below a folder of a local repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; folder path; non-recursive flag (default false, i.e. recursive). |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: works on local repositories only; without a POM file in the repository the
calculation fails.

### API-044 · Generate a Maven POM
Priority: **P2**

Creates a POM for a stored JAR, either from the POM embedded in the JAR or as a default POM.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; JAR path. |
| Output | Success status (body unspecified). |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

### API-045 · Recalculate PyPI indexes
Priority: **P1**

Rebuilds the package and repository indexes of a PyPI repository from the stored artifact
metadata.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; asynchronous flag. |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: local repositories only; synchronous by default.

### API-046 · PyPI JSON project metadata
Priority: **P1**

Serves PyPI project metadata as JSON, for the latest version or for a given version.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; package name; optional version. |
| Output | JSON metadata (shape unspecified). |
| Errors | 401; 403; 404; 500. |
| Privilege | Unspecified. |

### API-047 · Recalculate Debian metadata
Priority: **P2**

Rebuilds the `Packages` and `Release` index files of a local Debian repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; asynchronous flag; write-properties flag (0/1). |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: local repositories only; synchronous by default. What the write-properties flag writes
is unspecified.

### API-048 · Recalculate coordinates of cached Debian packages
Priority: **P2**

Recalculates the Debian package coordinates of the packages in a remote cache.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | Plain text. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: remote caches only.

### API-049 · Create a Debian snapshot
Priority: **P2**

Creates a snapshot of a Debian distribution, identified by a tag, in a target repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; asynchronous flag (0/1); body with snapshot tag, target repository and distribution name (all mandatory). |
| Output | Plain text. |
| Errors | 400; 401; 403; 500. |
| Privilege | Unspecified. |

### API-050 · Recalculate a Helm chart index
Priority: **P1**

Rebuilds the `index.yaml` of a Helm repository, completely or for one path.

| Operation | Inputs | Output |
|---|---|---|
| Full recalculation | Repository key. | Plain text. |
| Partial recalculation | Repository key; path to recalculate. | Plain text. |

Errors: 401; 403; 500. Privilege: unspecified. Behavior: local and federated repositories.

### API-051 · Relocate misplaced CRAN archives
Priority: **P2**

Moves CRAN archive files from an outdated location to the correct one, when the corresponding
system option is enabled.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | JSON (shape unspecified). |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: a mutating operation exposed as a read (GET) request.

## Repository configuration

The operations in this section manage repository definitions. The configuration document they
exchange is described field by field in [repositories.md](repositories.md) and
[remote-repositories.md](remote-repositories.md); this section covers only the operation contract.

### API-052 · Create a repository
Priority: **MVP**

Creates one repository from a full configuration document. Applies to local, remote, virtual and
federated repositories.

| Aspect | Detail |
|---|---|
| Inputs | Repository key (path); configuration document (body) whose shape depends on the repository class. Mandatory in the document: key and class. A remote repository additionally needs the upstream URL, unless it is a project-scoped connection to a shared repository, in which case the shared repository's key is mandatory instead. A virtual repository needs its ordered member list. |
| Output | Success with a plain-text confirmation (not JSON). |
| Errors | 400 when the key already exists, the configuration is invalid, or the named shared upstream is not connectable for the repository's project; 401; 403 without administrator rights; 500. |
| Privilege | System administrator. |

Behavior:

- Fields left out take the defaults of the repository class and format.
- The format enumeration differs per class: local accepts 35 values, remote 33, virtual 30,
  federated 31 (for example, a terraform state-backend format exists only for local repositories;
  virtual repositories accept no Cargo, CocoaPods, Opkg or Vagrant). See REPO-006 in
  [repositories.md](repositories.md).
- Assigning the repository to a project makes the environment list mandatory.
- A local repository with a non-empty member list becomes federated while its class stays
  `local` in the document.
- Plain JSON and a product-specific JSON media type per class are both accepted; the body shape is
  identical.

Interactions: REPO-053 in [repositories.md](repositories.md) (field reference);
API-065 (which shared repositories may be named as upstream).

### API-053 · Create repositories in a batch
Priority: **P1**

Creates several repositories, of mixed formats and classes, in one request.

| Aspect | Detail |
|---|---|
| Inputs | JSON array of configuration documents, each with its key and the class-specific shape of API-052. |
| Output | 201 with plain-text lines, one per repository. |
| Errors | 400 when the batch size limit is exceeded, a key is missing, a configuration is invalid or a shared upstream is not connectable; 401; 403 without administrator rights; 409 on a key conflict; 500. |
| Privilege | System administrator. |

Behavior: one invalid element fails the entire batch request. The batch size limit is
configurable; its default for this operation is unspecified (the read counterpart,
API-059, defaults to 100).

Interactions: REPO-061 in [repositories.md](repositories.md).

### API-054 · Update a repository configuration
Priority: **MVP**

Changes an existing repository's configuration from a full or partial document.

| Aspect | Detail |
|---|---|
| Inputs | Repository key (path); partial or full configuration document (body). |
| Output | Success with a plain-text confirmation. |
| Errors | 400 on invalid configuration, a non-connectable shared upstream, or turning off the project-scoped connection without supplying an upstream URL; 401; 403; 404 when the repository does not exist; 500. |
| Privilege | System administrator. |

Behavior:

- The repository class cannot be changed.
- Omitted optional fields keep their stored values (merge semantics).
- A regular remote repository becomes a project-scoped connection by setting the
  project-scoped flag and naming the shared repository; the reverse requires clearing the flag
  and supplying an upstream URL in the same request.

Interactions: REPO-054 in [repositories.md](repositories.md); REPO-015 (project-scoped
connections).

### API-055 · Update repositories in a batch
Priority: **P1**

Updates several repositories of mixed formats and classes in one request.

| Aspect | Detail |
|---|---|
| Inputs | JSON array of configuration documents, each carrying its key; per-element rules as in API-054. |
| Output | Success, possibly with a plain-text confirmation. |
| Errors | 400 (batch limit exceeded, missing key, non-connectable shared upstream, project-scoped flag cleared without URL); 401; 403; 404 when none of the keys exist; 500. |
| Privilege | System administrator. |

Behavior: one invalid element fails the entire batch request, as for API-053.

Interactions: REPO-062 in [repositories.md](repositories.md).

### API-056 · Check whether repositories exist
Priority: **P2**

Answers whether any repository of the given classes exists, optionally within one project.

| Aspect | Detail |
|---|---|
| Inputs | Repository classes (repeatable filter); optional project key (absent = all projects). |
| Output | Existence flag, the classes that were checked, and the project scope that was applied. |
| Errors | 401; 403; 500. |
| Privilege | System administrator. |

Interactions: REPO-060 in [repositories.md](repositories.md).

### API-057 · Get a repository configuration
Priority: **MVP**

Returns the complete stored configuration of one repository.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | The class-specific configuration document, including fields that do not apply to the repository's format. |
| Errors | 401; 403 when the caller may not see the configuration; 404; 500. |
| Privilege | Administrators receive the full document; other permitted users receive a reduced document. |

Behavior: for a project-scoped remote repository the upstream URL is returned empty and the
shared repository's key is returned instead; the reduced document for non-administrators still
contains that key. Secrets are part of the configuration model of remote repositories (upstream
user name and password); which fields the reduced document omits is unspecified.

Interactions: REPO-055 in [repositories.md](repositories.md).

### API-058 · Get a format-filtered repository configuration
Priority: **P1**

Same as API-057, but the document contains only fields relevant to the repository's
format.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | Configuration document reduced to format-relevant fields. |
| Errors | 401; 403; 404; 500. |
| Privilege | As API-057. |

Interactions: REPO-056 in [repositories.md](repositories.md).

### API-059 · Get repository configurations by key
Priority: **P1**

Returns the configurations of a named set of repositories.

| Aspect | Detail |
|---|---|
| Inputs | List of repository keys (repeatable query value; mandatory). |
| Output | Map from repository key to its class-specific configuration document. |
| Errors | 400 when no keys are given or the item limit is exceeded; 401; 403; 500. |
| Privilege | Administrators receive full documents; others receive reduced documents. |

Behavior: default batch size 100 keys, configurable.

Interactions: REPO-059 in [repositories.md](repositories.md).

### API-060 · Get all repository configurations
Priority: **P1**

Returns every repository configuration, grouped by class.

| Aspect | Detail |
|---|---|
| Inputs | Optional format filter and optional class filter (comma-separated values each). |
| Output | Object keyed by class (local, remote, virtual, federated, release-set storage, and possibly others) whose values are arrays of configuration documents. |
| Errors | 401; 403; 500. |
| Privilege | System administrator. |

Behavior: the two filters are retained for compatibility; their exact matching rules are
unspecified.

Interactions: REPO-058 in [repositories.md](repositories.md); REPO-066 (release-set storage
repositories).

### API-061 · List repositories
Priority: **MVP**

Returns a minimal record for every repository the caller may see.

| Aspect | Detail |
|---|---|
| Inputs | Optional class filter (local, remote, virtual, federated, distribution); optional format filter; optional project key. All filters may be omitted. |
| Output | Array of records: key, class, description, client URL, format, and the shared repository's key for project-scoped remote repositories (null for every other repository). |
| Errors | 401; 403; 500. |
| Privilege | Any user with access, anonymous included when anonymous access is enabled. |

Behavior: the client URL of a project-scoped remote repository is returned empty. The format in
the output is capitalized (for example "Maven", "Generic"), unlike the lowercase values used in
configuration documents.

UI: repository administration list, see REPO-007 in [repositories.md](repositories.md).

Interactions: REPO-057 in [repositories.md](repositories.md).

### API-062 · Shared remote repository configuration (deprecated)
Priority: **P2**

Reads the configuration that a remote repository exposes as shared. Deprecated in favor of
API-057. What a consumer does with the shared configuration is unspecified for this operation.

| Aspect | Detail |
|---|---|
| Inputs | Remote repository key. |
| Output | Remote repository configuration (sanitized for non-administrators). |
| Errors | 401; 403; 404; 500. |
| Privilege | Any authenticated user for a shared remote repository; administrator otherwise. |

Interactions: REPO-065 in [repositories.md](repositories.md).

### API-063 · Delete a repository
Priority: **MVP**

Removes a repository and all of its content.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | A deletion report: repository key, human-readable status message, number of artifacts deleted in this attempt, success flag; on failure additionally the total failure count and a capped list of per-artifact failures (HTTP status and message each). |
| Errors | See the status table. |
| Privilege | System administrator. |

<details>
<summary>Status codes</summary>

| Status | Meaning | Report |
|---|---|---|
| 200 | Configuration removed, all artifacts deleted. | Success report |
| 202 | Another deletion of the same repository is running (lock held); retry later. | Success report |
| 207 | Some artifacts could not be deleted; the configuration is kept. Delete the rest and retry. | Failure report with per-artifact failures |
| 400 | Invalid key: blank, the trash repository, or the support-bundle repository. | Failure report |
| 401 | Authentication failed. | Standard error envelope |
| 403 | Permission denied, validation failure, or every artifact failed to delete. | Failure report |
| 404 | Repository does not exist. | Success report with the success flag set; rely on the status code |
| 409 | An import into the repository is in progress, or a server-side hook vetoed the deletion. | Failure report |
| 424 | A federated repository could not be removed from its remote members. | Failure report |
| 500 | Unexpected failure, including failure to acquire the deletion lock. | Failure report |

</details>

Behavior:

- Deletion is serialized per repository through a lock; a concurrent call returns 202.
- Artifacts protected by a deletion-protection property cause per-artifact 403 failures; the
  repository then survives with 207.
- The per-artifact failure list is capped at a configurable maximum (default 50); the failure
  count always holds the true total.

Interactions: REPO-063 in [repositories.md](repositories.md); EVT-048 in
[events-and-extensibility.md](events-and-extensibility.md) (repository configuration hooks).

### API-064 · Delete repositories in a batch
Priority: **P1**

Deletes several repositories, best-effort per repository.

| Aspect | Detail |
|---|---|
| Inputs | JSON array of repository keys (at least one; order preserved). |
| Output | Batch report: overall status message plus one report per repository in request order, each shaped like the report of API-063. |
| Errors | See the status table. |
| Privilege | System administrator. |

<details>
<summary>Status codes</summary>

| Status | Meaning | Body |
|---|---|---|
| 200 | Every repository succeeded, including the case where every key was missing. | Batch report |
| 207 | Mixed results. | Batch report |
| 400 | Empty or missing key list, or early validation failed before any work. | Status message only, no per-repository reports |
| 401 | Authentication failed. | Standard error envelope |
| 403 | All repositories failed: either early validation (status message only, e.g. a build-information repository was named) or per-repository failures (batch report). | Either shape |
| 409 / 424 / 500 | All repositories failed for that reason (import running or hook veto / federation removal / unexpected). | Batch report |

</details>

Behavior: one failing repository does not abort the rest. Clients must inspect every
per-repository report, also on 200, because a missing key counts as success.

Interactions: REPO-064 in [repositories.md](repositories.md).

### API-065 · List repositories connectable from a project
Priority: **P2**

Lists the repositories shared with a project that a project-scoped remote repository of that
project may name as its upstream.

| Aspect | Detail |
|---|---|
| Inputs | Project key (path); optional format filter; optional key substring filter; optional result limit. |
| Output | List of repository keys. |
| Errors | 400; 401; 403; 500; 503 (temporarily unavailable, retry). |
| Privilege | Project administrator of that project, or system administrator. |

Interactions: REPO-014 and REPO-015 in [repositories.md](repositories.md).

### API-066 · List project-scoped consumers of a repository
Priority: **P2**

Shows which project-scoped remote repositories, owned by other projects, use a shared repository
as their upstream.

| Aspect | Detail |
|---|---|
| Inputs | Repository key (path); optional project key filter; optional result limit. |
| Output | List of consumers: consuming repository key, owning project key and display name, share time (milliseconds since epoch), environments of the share. |
| Errors | 400; 401; 403; 500; 503. |
| Privilege | System administrator; a project administrator of the filtered project when the project filter is given. |

Interactions: REPO-014 in [repositories.md](repositories.md).

## Federated repositories

A federated repository is a local repository whose content and configuration are kept in sync
with member repositories on other instances. Synchronization is either built in (older mode) or
handled by a separate federation synchronization service (newer mode); several monitoring
operations exist only in one of the two modes and answer 405 in the other. The feature itself is
described in [replication-and-federation.md](replication-and-federation.md). The required
privilege for the operations in this section is unspecified (401 and 403 can occur everywhere).

### API-067 · Convert a local repository to federated
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Local repository key. |
| Output | Plain-text confirmation. |
| Errors | 400; 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: inside a project, every member of the federation must carry the same project key.

### API-068 · Convert federated repositories back to local
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | JSON array of federated repository keys. |
| Output | Either a plain confirmation string, or an object mapping each repository that failed to its failure reason. |
| Errors | 400 when repositories failed to convert; 401; 403; 404; 500. |
| Privilege | Unspecified. |

Behavior: refused while the repository still belongs to an active federation that has further
members.

### API-069 · List federated repositories with configuration drift
Priority: **Later**

Returns the keys of local federated repositories whose configuration differs from at least one
remote member's.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Array of repository keys. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

### API-070 · Recover exhausted federation queues
Priority: **Later**

Clears the error state of event queues whose retries are exhausted, so that delivery to remote
members is attempted again.

| Aspect | Detail |
|---|---|
| Inputs | Optional repository key (absent = all federated repositories); force flag (default false). |
| Output | JSON (shape unspecified). |
| Errors | 400; 401; 403; 500. |
| Privilege | Unspecified. |

### API-071 · Reconnect to the federation service
Priority: **Out of scope**

Re-establishes the connection to the separate federation synchronization service when it failed at
startup.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Plain text. |
| Errors | 401; 403; 424 when the service cannot be reached; 500. |
| Privilege | Unspecified. |

Priority rationale: specific to a deployment with a separate synchronization service; a design
that keeps federation in-process has no counterpart.

### API-072 · Full synchronization of a federated repository
Priority: **Later**

Runs a full synchronization between a federated repository and its members; a forced variant interrupts a stuck full sync already in progress.

| Operation | Inputs | Output |
|---|---|---|
| Full sync | Repository key; optional member URL (absent = all remote members); dry-run flag (default false). | Message and a list of result paths. |
| Forced full sync | Repository key; optional member URL. | Message and a list of result paths. |

Errors: 400; 401; 403; 500. Privilege: unspecified.

Behavior: the dry run (comparison mode) reports which local files are missing on the remote
members — as a generated JSON listing — without transferring anything.

### API-073 · Propagate creation time during full sync
Priority: **Later**

Turns on or off the propagation of artifact creation times during full synchronization.

| Aspect | Detail |
|---|---|
| Inputs | Enabled flag (mandatory). |
| Output | JSON (shape unspecified). |
| Errors | 400 when the flag is missing; 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: relevant with the separate federation synchronization service.

### API-074 · Resynchronize a member's configuration
Priority: **Later**

Resynchronizes the configuration of a federated repository's member, for example after a
network failure.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | JSON (shape unspecified). |
| Errors | 400; 401; 403; 500. |
| Privilege | Unspecified. |

### API-075 · Remove a member from all federations
Priority: **Later**

Removes one instance (identified by base URL) from every federation it belongs to.

| Aspect | Detail |
|---|---|
| Inputs | Base URL of the member, as plain-text body. |
| Output | JSON (shape unspecified). |
| Errors | 400 invalid URL, unknown member, or the remote member's configuration could not be read; 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: requires the separate federation synchronization service.

### API-076 · Federation connection details of a repository
Priority: **Later**

Shows, per source-target pair, whether metadata events flow promptly.

| Aspect | Detail |
|---|---|
| Inputs | Repository key. |
| Output | Per member connection: local repository key and instance URL, remote repository key and instance URL, status, lag in milliseconds, queued event count, disabled flag, supported flag. |
| Errors | 400; 401; 403; 405 when the separate synchronization service is not in use; 500. |
| Privilege | Unspecified. |

### API-077 · Federation state list and summary
Priority: **Later**

| Operation | Inputs | Output |
|---|---|---|
| State list | Optional comma-separated status filter (for example healthy, error). | Per federation: local repository key, priority (high or standard), and per member: URL, repository key, aggregated status, supported, disabled and lagging flags. |
| State summary | None. | Count of members per state: healthy, not available, delayed, pending full sync, full sync running, error, disabled. |

Errors: 400; 401; 403; 405 when the separate synchronization service is not in use; 500.
Privilege: unspecified.

### API-078 · Prioritize a federated repository
Priority: **Later**

Gives a federated repository's event queue priority access to system resources.

| Aspect | Detail |
|---|---|
| Inputs | Repository key; priority level (high or standard; mandatory). |
| Output | Plain text. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Unspecified. |

Behavior: at most 10 repositories can be prioritized at the same time per instance.

### API-079 · Legacy federation status (built-in synchronization)
Priority: **Later**

Status operations of the built-in synchronization mode; each answers 405 when the separate
synchronization service is in use.

| Operation | Inputs | Output |
|---|---|---|
| Repository sync status (two versions; the second allows a wider range of member statuses) | Repository key. | Local key; binary task counts (in progress, failing — first version); per-mirror event status; per-artifact federation status (shapes unspecified). 404 when the repository is unknown. |
| Sync state of all federated repositories | None. | Per repository: key, status (syncing, full sync running, disabled, out of sync), members (remote key and URL). |
| Mirror lag | Optional lag threshold in milliseconds (only lagging pairs above it are returned). | Per pair: local key, remote URL, remote key, lag in milliseconds, event registration time (epoch milliseconds). |
| Unavailable mirrors | None. | Per unsynchronized mirror: local key, remote URL, remote key, status. Mirrors are unsynchronized when newly added, while a local repository is being converted, or after an error threshold is crossed. |

Errors: 401; 403; 405; 500. Privilege: unspecified.

Behavior: the repository status operations are expensive for the database and should be used
sparingly.

### API-080 · Failed binary transfer tasks
Priority: **Later**

Binary tasks fetch file content from another member; a task fails after the maximum number of
retrieval attempts (default 10).

| Operation | Inputs | Output |
|---|---|---|
| List failures | Repository key (mandatory); page size (default 1000); page number starting at 1. | Per task: SHA-1, repository key, creation time (epoch milliseconds), failure count. |
| Replay | Repository key (mandatory); optional SHA-1 (absent = all failed tasks of the repository). | Number of tasks queued again. |
| Delete | Repository key (mandatory); optional SHA-1 (absent = all). | Number of tasks deleted. |

Errors: 400 when the repository key is missing; 401; 403; 500. Privilege: unspecified.

### API-081 · Instance trust pairing
Priority: **Later**

Establishes and removes trust between two instances for a use case, with a one-time pairing
token generated on one side and exchanged on the other.

| Operation | Inputs | Output |
|---|---|---|
| Generate | Use case. | Pairing token. |
| Exchange | Use case; pairing token (body). | Message. |
| Unregister | Use case; base URL of the paired instance (body); force flag (default false). | Message. |

Use cases: bidirectional federated-repository trust; receiving-only (unidirectional) federation
trust; cold-storage pairing; cold-storage pairing for a named namespace. An unknown use case
returns 404.

Errors: 400 (exchange, unregister); 401; 403; 404; 500. Privilege: unspecified.

Interactions: RET-028 in
[retention-and-cleanup.md](retention-and-cleanup.md); pairing tokens in
[access-tokens.md](access-tokens.md); cold storage in
[retention-and-cleanup.md](retention-and-cleanup.md).

## Replication

Replication copies repository content between instances: push from a local repository to
repositories on other instances (one target, or several with multi-push), and pull into a remote
repository's cache from another instance. Scheduling is by cron expression or event-based. The
feature is described in [replication-and-federation.md](replication-and-federation.md). Where a
privilege follows only from the 403 description ("not an administrator"), the entries say so.

### API-082 · Replication configuration document
Priority: **P2**

The document that the replication configuration operations exchange; one per target.

<details>
<summary>Replication configuration fields</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Target URL | URL of the target repository on the other instance; required for push from a local repository. | unspecified |
| Socket timeout | Milliseconds. | unspecified |
| User name, password | Credentials at the target. | unspecified |
| Event-based replication | Enable event-based replication; applies to push and pull. | unspecified |
| Enabled | Whether this replication runs. | unspecified |
| Cron expression | Schedule. | unspecified |
| Sync deletes | Delete at the target what was deleted at the source. | false |
| Sync properties | Replicate properties. | true |
| Sync statistics | Replicate download statistics. | unspecified |
| Repository key | Source repository; read-only, returned in responses. | — |
| Replication key | Generated identifier of this replication; read-only. | — |
| Include path prefix pattern | Only replicate paths matching. | unspecified |
| Exclude path prefix pattern | Skip paths matching. | unspecified |
| Proxy | Key of a configured outbound proxy. | unspecified |
| Disable proxy | Bypass any proxy. | unspecified |
| Check binary existence in store | Checksum-only replication for targets whose storage receives binaries through storage-level replication (see API-090). | unspecified |

</details>

Behavior: the repository key and the replication key are returned in responses and are not
required on input.

### API-083 · Get, set, update and delete a repository's replication
Priority: **P2**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Get | Repository key. | One configuration document, or an array of them when several targets exist (multi-push). | 400 repository not found or invalid key; 401; 403; 404 |
| Set (add or replace) | Repository key; one document or an array. | 200. | 400; 401; 403 |
| Update | Repository key; full or partial document. | 200. | 400; 401; 403; 404 repository or replication missing |
| Delete | Repository key; optional target URL (absent = delete all multi-push replications of the source). | 200. | 401; 403; 404 |

Privilege: system administrator (from the 403 descriptions).

Behavior:

- Supported for local and remote repositories (delete: local repositories and remote caches).
- Set accepts exactly what get returns. With an array, the schedule and the event-based flag of
  the first element apply to all.

### API-084 · Replication status of a path
Priority: **P2**

| Aspect | Detail |
|---|---|
| Inputs | Repository key plus optional path. |
| Output | Overall status and last completion time; per target: URL, repository key, status, last completion; and a map from target repository key to status and last completion. Status values: unknown, never run, incomplete, error, warn, ok, inconsistent, partial failure. Times ISO 8601 with offset; last completion null when never completed. |
| Errors | 401; 403 without read permission; 404. |
| Privilege | Read permission. |

Behavior: derived from replication properties written on the items. Supported for local
repositories, remote caches and remote repositories.

### API-085 · Run replication now
Priority: **P2**

Schedules an immediate replication run.

| Aspect | Detail |
|---|---|
| Inputs | Repository path: a local repository (push) or a remote cache (pull), optionally with a sub-path; optional strategy name (values unspecified); optional array of targets, each with URL, user name, password and proxy name (all four used by push only; every target field is optional for pull), disable-proxy flag, sync-properties flag (default true), sync-deletes flag (default false), include and exclude path prefix patterns, check-binary-existence flag. |
| Output | 200. |
| Errors | 400; 401; 403. |
| Privilege | Unspecified beyond "permitted user". |

Behavior: the class of the named repository decides push or pull. Without targets in the body,
every configured replication of the repository is triggered.

### API-086 · Create, replace or update multi-push replication
Priority: **P2**

| Aspect | Detail |
|---|---|
| Inputs | Local repository key; body with a common cron expression (mandatory), a common event-based flag (mandatory) and the list of replication documents (mandatory). |
| Output | 200. |
| Errors | 400; 401; 403. |
| Privilege | System administrator (from the 403 descriptions). |

Behavior: two operations — create-or-replace and update — with the same body. Local repositories
only.

### API-087 · Enable or disable replications in bulk
Priority: **P2**

| Aspect | Detail |
|---|---|
| Inputs | Action (enable or disable); include patterns and exclude patterns, both matched against target repository URLs. |
| Output | 200. |
| Errors | 400; 401; 403. |
| Privilege | Unspecified beyond "permitted user". |

Behavior: selects replications by repository or by target server through the URL patterns.

### API-088 · Event-based pull replication channels
Priority: **P2**

Event-based pull replication keeps a long-lived event stream open from the source instance to
each subscribed remote repository.

| Operation | Inputs | Output | Notes |
|---|---|---|---|
| List subscribers | Local repository key. | Array of subscriber identifiers (instance address, instance identifier and repository keys combined in one string). | Administrator. |
| Close all channels | None. | 200. | Forces subscribers to reconnect. Administrator. |
| Establish channel (internal) | Peer request. | 200. | Peer-to-peer; called by the pulling instance. |
| Remove channel (internal) | Peer request. | 200. | Peer-to-peer. |
| Propagate events (internal) | Event batch from a peer. | 200. | The receiver checks the peer through a request header that has to match the service identity the caller authenticated with. |

Errors: 401 (internal operations reject anonymous calls); 403 (caller is not an administrator, or
not a permitted replication peer). The internal operations are not meant to be called by users.

### API-089 · Block and unblock replication globally
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Get global state | None. | Whether pull and whether push replication is blocked. |
| Block | Push flag (default true); pull flag (default true). | Plain-text confirmation. |
| Unblock | Push flag (default true); pull flag (default true). | Plain-text confirmation. |

Errors: 401; 403. Privilege: system administrator (from the 403 descriptions).

Behavior: a false flag leaves that replication direction unchanged. While blocked, no
replication is triggered.

### API-090 · Checksum (metadata-only) replication
Priority: **P2**

For setups where the storage layer itself replicates binaries, lets push and pull replication
transfer only metadata and trust that the target store already holds the content. The trust
window exists for migrations.

| Operation | Inputs | Output |
|---|---|---|
| Configure | Allow-binary-existence-check flag (mandatory); days to trust the source's checksums (0–30, default 0). | 200. |
| Get | None. | Allow flag and the date the trust expires (format varies). |

Errors: 400 (configure); 401; 403. Privilege: system administrator. Self-managed installations
only.

Priority rationale: replication is P2 by the rubric. The case it serves exists for trove as
well: two sites on S3 buckets linked by the object store's own cross-region replication receive
the binaries that way, so only metadata has to travel between the instances.

## Search

All search operations return references to stored items, by default as links to the item
information operation (API-003). Unless stated otherwise they accept an optional
comma-separated list of repository keys that restricts the search. Several accept an optional
result-detail request header that adds the full item information, the item properties, or both to
each hit. Search behavior beyond the operation contract (result caps, permission filtering) is in
[search-and-query.md](search-and-query.md).

### API-091 · Query language execution
Priority: **P2**

Runs a query written in the system's structured query language and returns matching records.

| Aspect | Detail |
|---|---|
| Inputs | Query text as a plain-text body. |
| Output | Result list (records with the requested fields, e.g. repository, path, name, type, size, created/modified/updated times and actors) and a range block: start position, end position, total, limit, and an optional notification (for example that the hard result limit trimmed the output). |
| Errors | 400 invalid query; 401; 403; 500. |
| Privilege | Any authenticated user; some domains or queries require an administrator. |

Behavior: the query text is limited to 6,000 characters by default (configurable).

Interactions: SRCH-023 to SRCH-047 in [search-and-query.md](search-and-query.md).

### API-092 · Name search
Priority: **P1**

Finds files whose name contains a given fragment.

| Aspect | Detail |
|---|---|
| Inputs | Name fragment (mandatory); repository filter; optional result-detail header (info, properties, or both). |
| Output | List of item-information links (plus details when requested). |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: searches local repositories and remote caches only; naming a virtual repository is not
supported.

Interactions: SRCH-004 in [search-and-query.md](search-and-query.md).

### API-093 · Archive entry search (deprecated)
Priority: **Later**

Finds archives that contain an entry (for example a class file) matching a name pattern.

| Aspect | Detail |
|---|---|
| Inputs | Entry name pattern (e.g. `*Logger.class`); repository filter. |
| Output | List of entries, each with the entry's full name inside the archive and the item links of the archives that contain it. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: searches local repositories and remote caches; deprecated in favor of the query
language.

Interactions: SRCH-005 in [search-and-query.md](search-and-query.md).

### API-094 · Maven coordinate search
Priority: **P1**

Finds artifacts by Maven group, artifact, version and classifier.

| Aspect | Detail |
|---|---|
| Inputs | Group, artifact, version, classifier (at least one); repository filter; "specific" flag (default false); optional result-detail header. |
| Output | Standard mode: list of item links. Specific mode: download URL, extension, version and classifier per hit. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: only for repositories whose layout supports Maven-style coordinates (Maven, Gradle,
Ivy, SBT). Searching remote repositories requires exact group and artifact. The specific flag
enables virtual and remote repositories and minimizes the output.

Interactions: SRCH-006 in [search-and-query.md](search-and-query.md).

### API-095 · Property search
Priority: **P1**

Finds items by property values.

| Aspect | Detail |
|---|---|
| Inputs | Arbitrary query parameters: each parameter name is a property key, its value a comma-separated list of accepted values; repository filter (local, remote cache or virtual); optional result-detail header. |
| Output | List of item links. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: a property given without a value matches any value.

Interactions: SRCH-007 in [search-and-query.md](search-and-query.md); ART-021 in
[artifacts.md](artifacts.md).

### API-096 · Checksum search
Priority: **P1**

Finds artifacts by MD5, SHA-1 or SHA-256.

| Aspect | Detail |
|---|---|
| Inputs | One checksum (MD5, SHA-1 or SHA-256); repository filter (local, remote cache or virtual); optional result-detail header. |
| Output | List of item links. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Interactions: SRCH-008 in [search-and-query.md](search-and-query.md); API-014.

### API-097 · Bad or missing client checksum search
Priority: **P2**

Lists artifacts whose client-supplied MD5 or SHA-1 is missing or differs from the server's value.

| Aspect | Detail |
|---|---|
| Inputs | Checksum type (MD5 or SHA-1, mandatory); repository filter. |
| Output | Per hit: item link, server checksum, client checksum (empty string when missing). |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: at most 10,000 results. The output fields are named for MD5 even when SHA-1
is requested; whether they then carry SHA-1 values is unspecified.

Interactions: SRCH-009 in [search-and-query.md](search-and-query.md).

### API-098 · Not-downloaded-since search
Priority: **P1**

Lists artifacts that were not downloaded since a point in time.

| Aspect | Detail |
|---|---|
| Inputs | Cut-off time (milliseconds since epoch, mandatory); optional "created before" time (same unit; defaults to the cut-off); repository filter (local or caches). |
| Output | Per hit: item link and last-download time (ISO 8601). |
| Errors | 400; 401; 403; 500. |
| Privilege | Authenticated (non-anonymous) user with read access. |

Interactions: SRCH-010 in [search-and-query.md](search-and-query.md);
[retention-and-cleanup.md](retention-and-cleanup.md).

### API-099 · Date-range search on selected date fields
Priority: **P1**

Lists artifacts with any of the selected date fields inside a range.

| Aspect | Detail |
|---|---|
| Inputs | From (milliseconds since epoch or ISO 8601, mandatory); to (same forms; defaults to now); date fields (comma-separated subset of created, last modified, last downloaded; mandatory); repository filter (local or caches). |
| Output | Per hit: item link and the requested date fields. |
| Errors | 400, also when both bounds are missing; 401; 403; 500. |
| Privilege | Authenticated (non-anonymous) user. |

Behavior: a hit is returned when any of the selected fields lies within the range.

Interactions: SRCH-011 in [search-and-query.md](search-and-query.md).

### API-100 · Created-in-date-range search
Priority: **P1**

Lists artifacts created within a time range.

| Aspect | Detail |
|---|---|
| Inputs | From (milliseconds since epoch, mandatory); to (defaults to now); repository filter (local or remote cache). |
| Output | Per hit: item link and creation time (ISO 8601). |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Authenticated (non-anonymous) user. |

Interactions: SRCH-012 in [search-and-query.md](search-and-query.md).

### API-101 · Path pattern search
Priority: **P1**

Lists files of one repository matching an Ant-style path pattern.

| Aspect | Detail |
|---|---|
| Inputs | One pattern of the form `<repository key>:<ant pattern>`. |
| Output | Repository base URL, the pattern as given, and the matching relative paths. |
| Errors | 400; 401; 403; 500. |
| Privilege | Authenticated (non-anonymous) user. |

Behavior: the recursive wildcard `**` is rejected to bound result size.

Interactions: SRCH-013 in [search-and-query.md](search-and-query.md).

### API-102 · Builds that use an artifact as dependency
Priority: **P2**

Lists the build runs whose recorded dependencies include an artifact.

| Aspect | Detail |
|---|---|
| Inputs | SHA-1 of the artifact (mandatory). |
| Output | List of build-run links. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Interactions: SRCH-014 in [search-and-query.md](search-and-query.md); API-162.

### API-103 · License search
Priority: **Later**

Lists artifacts already tagged with license information.

| Aspect | Detail |
|---|---|
| Inputs | Inclusion flags per license status — unapproved (default on), unknown (default on), not found (default off), neutral (default off), approved (default off); auto-find flag (default off); repository filter (local, remote cache or virtual). |
| Output | Per hit: item link, tagged license, auto-found license (with auto-find), status (approved, unapproved, neutral, unknown). |
| Errors | 400; 401; 403; 500. |
| Privilege | System administrator. |

Behavior: auto-find tries to detect new license information and can slow the search
considerably; it still only covers already-tagged artifacts. Searching for a specific license is
done through property search on the license property.

Interactions: SRCH-015 in [search-and-query.md](search-and-query.md).

### API-104 · Version list search
Priority: **P2**

Lists all versions of a module identified by group and artifact.

| Aspect | Detail |
|---|---|
| Inputs | Group; artifact; optional version pattern with `*` and `?` wildcards; remote flag (default off); repository filter (local, remote, virtual). |
| Output | Per version: version string and whether it is an integration (snapshot) version. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: without a version, release and integration versions are returned; with an integration
version such as `1.0-SNAPSHOT`, only integration versions. With the remote flag, Maven remotes
consult the upstream metadata file and other layouts list remote folders where remote listing is
enabled — this can be very slow. Integration detection depends on the repository layout's
artifact path pattern containing the base revision token followed by the file integration
revision token with only literals between them.

Interactions: SRCH-016 in [search-and-query.md](search-and-query.md); REPO-046 to REPO-049 in
[repositories.md](repositories.md).

### API-105 · Latest version by layout
Priority: **P2**

Returns the latest version of a module, sorted with Maven's version ordering.

| Aspect | Detail |
|---|---|
| Inputs | Group; artifact; optional version pattern (wildcards); remote flag (default off); repository filter (local, remote cache, virtual). |
| Output | The version string as plain text. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Behavior: without a version the latest release is returned; with an integration version, the
latest integration version. The layout's artifact path pattern must contain the organization and
module tokens.

Interactions: SRCH-017 in [search-and-query.md](search-and-query.md);
API-016 (latest-version placeholders).

### API-106 · Latest version by properties
Priority: **P2**

Returns the highest value of the lowercase `version` property among matching artifacts.

| Aspect | Detail |
|---|---|
| Inputs | Repository key or a wildcard meaning all repositories; path or a wildcard meaning all paths; list-files flag (default off); any further query parameters act as property filters. |
| Output | Latest version; with list-files, the URLs of the artifacts carrying it. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Authenticated (non-anonymous) user with read access to each artifact's repository. |

Behavior: Maven version ordering; only artifacts whose version property key is exactly lowercase
are considered.

Interactions: SRCH-018 in [search-and-query.md](search-and-query.md).

### API-107 · Build artifacts search
Priority: **P1**

Lists download URLs of all artifacts produced by a build run.

| Aspect | Detail |
|---|---|
| Inputs | Build name and build number (both mandatory; the number may be a "latest" placeholder); optional build status (latest run with that status); optional repository list; optional list of regular-expression patterns that filter the artifacts. |
| Output | List of download URLs. |
| Errors | 400; 401; 403; 500. |
| Privilege | Read access; anonymous allowed when enabled. |

Interactions: SRCH-019 in [search-and-query.md](search-and-query.md); API-018.

### API-108 · Parent manifest lists of a container image
Priority: **P2**

Finds all multi-platform manifest lists that reference a given image manifest.

| Aspect | Detail |
|---|---|
| Inputs | Container repository key (path); image name and tag (body, both mandatory). |
| Output | List of manifest lists: image name, digest, tags. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Read access on the container repository. |

Interactions: SRCH-022 in [search-and-query.md](search-and-query.md).

### API-109 · Container image catalog
Priority: **P1**

Lists the image names in a container repository, in the shape of the registry catalog listing
that Docker registries serve (paginated by page size and last returned name).

| Aspect | Detail |
|---|---|
| Inputs | Container repository key; page size; last name of the previous page. |
| Output | List of image names. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | Read access. |

Behavior: two system options (both off by default) make this listing and the tag listing
(API-110) of a remote repository fetchable from a cache: a switch that enables the fallback, and
the URL of the upstream registry whose catalog and tags are cached. The remote repository's
retrieval cache period sets how long the cached listing is reused. How remote repositories answer
without these options is unspecified.

Interactions: SRCH-020 in [search-and-query.md](search-and-query.md).

### API-110 · Container tag listing
Priority: **P1**

Lists the tags of one image in a container repository.

| Aspect | Detail |
|---|---|
| Inputs | Container repository key; image name; page size; last tag of the previous page. |
| Output | Image name and its tags. |
| Errors | 400; 401; 403; 404; 500. |
| Privilege | User with read access; whether anonymous access suffices is unspecified. |

Behavior: only tags that satisfy the OCI and Docker tag naming rules are returned (digest-shaped
entries are filtered out); a system option disables the filter.

Interactions: SRCH-021 in [search-and-query.md](search-and-query.md).

## Trash can and garbage collection

Deleted items go to a system trash can and are purged once their retention period expires; see
the trash can entries of [artifacts.md](artifacts.md) and [storage-and-blobs.md](storage-and-blobs.md).

### API-111 · Trash can operations
Priority: **P1**

| Operation | Inputs | Output | Errors | Privilege |
|---|---|---|---|---|
| Empty | None. | 202. | 400 (permission failure reported as 400); 401 | System administrator |
| Purge one item | Repository key with optional path inside the trash (e.g. a whole repository's trashed content, or one item). | 204. | 400 (permission failure); 401 | System administrator, project administrator, or manage or delete permission |
| Restore | Source path in the trash; destination path (mandatory); optional number of items per database transaction (at least 1). | 202 with a plain-text confirmation. | 400 (permission failure); 401; 404 item not in the trash | System administrator, project administrator, or manage permission |

Behavior:

- Purging is permanent.
- Restoring may target a different repository or path than the original location.
- Permission failures are reported as 400 instead of 403 in this group; clients should treat both
  as authorization failures.

Interactions: ART-036, ART-037 and
ART-038 in [artifacts.md](artifacts.md); API-022;
API-113.

### API-112 · Remove redundant trash directory records
Priority: **P2**

Starts a background task that deletes redundant metadata records of trash directories, which
slow down the light garbage-collection cycle and degrade database performance over time.

| Aspect | Detail |
|---|---|
| Inputs | Optional: records per database query (default 1000, ≥ 1); pause between batches in milliseconds (default 1000, ≥ 1); maximum number of batches (default -1 = unlimited). |
| Output | 202 with an informational message. |
| Errors | 400 invalid parameters; 401; 403. |
| Privilege | System administrator. |

Behavior: runs in the background in batches, pausing between batches to limit database load.

Interactions: ART-039 in [artifacts.md](artifacts.md).

### API-113 · Run garbage collection
Priority: **MVP**

Triggers a garbage-collection run.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | 200. |
| Errors | 401; 403. |
| Privilege | System administrator. |

Behavior: each run purges trash items whose retention has expired. Orphan binaries (content no
longer referenced) are deleted only by the full cycle, which runs every n-th iteration; n is
configurable, default 20.

Interactions: STOR-036 and
STOR-037 in [storage-and-blobs.md](storage-and-blobs.md);
API-002; API-111.

## First-generation release sets

A release set is a named, versioned list of artifacts that is signed and distributed to edge
instances. The first generation is managed partly by the repository manager (storage, import,
retention) and partly by a separate distribution service (creation, signing, distribution). The
concepts are in [distribution-and-lifecycle.md](distribution-and-lifecycle.md). The required
privilege for the operations in this section is unspecified.

### API-114 · List and read stored release sets
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| List release sets | Kind: target (received) or source (created here); default target. | Map from release-set name to versions (version, creation time, status). | 401; 403; 500 |
| List versions | Release-set name. | Versions (version, creation time, status). | 401; 403; 404; 500 |
| Get version | Name, version; format: JSON or signed JWS. | Name, version, description, creation time, artifacts (repository path, checksum, properties). | 401; 403; 404; 500 |

Interactions: DIST-001 and DIST-034 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-115 · Delete a source release-set version
Priority: **Later**

Deletes a release-set version on the instance where it was created. Deleting it on receiving
instances goes through the distribution service (API-126).

| Aspect | Detail |
|---|---|
| Inputs | Name, version. |
| Output | Plain text. |
| Errors | 401; 403; 404; 500. |
| Privilege | Unspecified. |

### API-116 · Import a release-set version from an archive
Priority: **Later**

Imports a release-set version from an exported zip, for both release-set generations.

| Aspect | Detail |
|---|---|
| Inputs | Either the zip as the request body, or the name of a file previously placed in the instance's import directory; dry-run flag (default false). |
| Output | 202 when the import is triggered; status through the import-status operation. |
| Errors | 400 import failed; 401; 403; 500. |
| Privilege | Unspecified. |

Behavior: a file taken from the import directory is deleted after import. Dry run checks the
preconditions only.

Status operation: inputs name and version (format JSON only); output status in progress, not
exported, failed, completed; errors 401, 403, 404, 500. The import runs on the receiving (edge)
instance.

Interactions: DIST-034 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md); API-133.

### API-117 · Retention of first-generation release sets
Priority: **Later**

Retention rules that delete old first-generation release-set versions.

| Operation | Inputs | Output |
|---|---|---|
| Get / set activation | Enabled flag (set). | Enabled flag. When disabled, versions marked "keep" can be deleted like any other. |
| Get / set rules | Automatic flag; versions to keep per release set; maximum age in days; cron schedule; maximum deletions per background run. | 200 (set); the rules (get). |
| Candidate report | Rule type (maximum versions per release set, or maximum age; mandatory); include-kept flag; offset (default 0); limit (default 100); order by name, version, distribution time (default) or keep flag; direction (default ascending); case-insensitive name/version pattern with `*` (default `*`). | Total and items: name, version, creation time, distribution time, keep flag. |
| History | Offset (default 0); limit (default 100); order by name, version, operation, time (default) or actor; direction (default descending). | Total and items: name, version, operation (delete, keep, unkeep), time, actor. |
| Delete | List of name/version pairs; dry-run flag (default false). | 200 (body unspecified). |
| Keep / unkeep | Lists of name/version pairs to mark keep and to unmark. | 200 (body unspecified). |

Errors: 400 on malformed bodies; 401; 403; 405 when the operation is not available on this
instance's configuration (report, history, delete, keep); 500. Privilege: unspecified.

Behavior: a version that meets any configured rule is subject to retention. Setting rules
accepts a partial document, but a value cannot be unset — only overwritten.

Interactions: RET-039 in [retention-and-cleanup.md](retention-and-cleanup.md).

## Distribution service

A separate distribution service creates and signs first-generation release sets and distributes
first- and second-generation release sets from the source instance to registered edge instances.
Concepts are in [distribution-and-lifecycle.md](distribution-and-lifecycle.md). Unless an entry
names a privilege, it is unspecified. The service's error envelope extends the common one (see
API-001) with a message, an HTTP status code and an optional detail string.

### API-118 · Distribution service metrics
Priority: **Later**

Returns the distribution service's metrics in OpenMetrics text format.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Plain-text metrics. |
| Errors | 401; 403. |
| Privilege | Permission to read system metrics. |

Behavior: the metric set covers release-set counts (created, signed, deleted; sizes; artifacts
per release set, overall and per format), distribution and remote-deletion requests per trigger
(API or UI) and per generation, outcomes per status, failures broken down by phase and cause
(transaction open, artifact transfer, transaction close, deletion; permission, conflict, proxy
authentication, missing repository, signature, timeout), scanner approvals and rejections, stuck
distributions per step, queue depth and in-flight distributions per priority, oldest in-flight
age per priority, retries per issue type, service version, and API requests per endpoint. Metric
names are not specified here.

Interactions: API-033; [operations-and-monitoring.md](operations-and-monitoring.md).

### API-119 · Distribution target rules
Priority: **Later**

The shared rule model that selects edge instances in distribute, remote-delete, priority and
permission operations.

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Site name | Edge instance name; `*` matches all. | unspecified |
| City name | City filter. | unspecified |
| Country codes | ISO 3166-1 alpha-2 filters. | unspecified |
| Priority | Per-target scheduling priority: low, medium or high (second generation, remote deletes, priority updates). | Inherits the operation's base priority; base default medium |

Behavior: when several rules match a target, later rules override earlier ones. Higher-priority
work is processed before lower-priority work.

### API-120 · Read release sets in the distribution service
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| List release sets | Optional start position (paging). | Array: name, version, status. | 401 |
| List versions of one release set | Name; optional start position. | Array: version, creation time, status. | 401; 404 |
| Latest version | Name. | Version details (shape unspecified). | 401; 404 |
| Version details | Name, version; format: JWS or JOSE. | Name, version, description, creation time, status, artifacts (repository path, checksum, properties); or the signed form. | 401; 404 |
| Version checksum | Name, version (HEAD request). | SHA-256 of the release-set version in a response header; no body. | 401; 404 |

Privilege: unspecified.

Interactions: DIST-038 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-121 · Create, update and delete release sets
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Create | Body: name, version, content specification (queries selecting artifacts) and optional signing settings (shape unspecified); optional project key; optional signing passphrase in a request header. | 200 (body unspecified). | 400; 401 |
| Update | Name, version; body as for create; optional signing passphrase header. | 200. | 400; 401 |
| Delete version | Name, version; archive flag (archive instead of permanent delete). | 204. | 401; 403 |
| Delete all versions | Name; archive flag. | 204. | 401; 403 |

Behavior: supplying the passphrase on create signs the release set immediately.

Interactions: DIST-038.

### API-122 · Sign a release-set version
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version; optional signing passphrase header. |
| Output | 200 with JSON (shape unspecified). |
| Errors | 400 invalid request or signing-key problem; 401. |
| Privilege | Unspecified. |

Behavior: the signature uses the GPG key configured in the distribution service; unsigned release
sets cannot be distributed.

### API-123 · Trigger a vulnerability scan of a release-set version
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version. |
| Output | 200 with JSON (shape unspecified). |
| Errors | 401; 403. |
| Privilege | Unspecified. |

Behavior: the scanner checks the release set's artifacts for known vulnerabilities and license
compliance.

Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md).

### API-124 · First-generation distribution status
Priority: **Later**

| Operation | Inputs | Output |
|---|---|---|
| All release sets | None. | Array of status records (shape unspecified). |
| One release set | Name. | Array of status records. |
| One version | Name, version. | Array of status records. |
| One distribution | Name, version, tracker ID (or the user-facing friendly ID). | Tracker ID, status, initiating user, start time, type, base priority, and per edge instance: name, service ID, status, effective priority. |

Errors: 401. Privilege: unspecified.

### API-125 · Distribute a first-generation release-set version
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version; body: dry-run flag (default false), target rules (API-119), auto-create-missing-repositories flag. |
| Output | Dry run: 200 with the tracker ID and the list of target edge instances (service ID, name, type). Real run: 202 (body unspecified). |
| Errors | 401; 403. |
| Privilege | Unspecified; destination permissions (API-137) govern which targets a user may distribute to. |

Behavior: only signed release sets can be distributed.

Interactions: DIST-027 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-126 · Delete a first-generation release-set version from edge instances
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version; body: dry-run flag (default false), target rules, and what to do with the source copy after a successful remote delete (keep or delete). |
| Output | Dry run: 200 with tracker ID and targets. Real run: 202 (body unspecified). |
| Errors | 401; 403. |
| Privilege | Unspecified. |

### API-127 · Abort a first-generation distribution
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version. |
| Output | 202 with the edge instances that received the abort signal; 204 when nothing was in progress. |
| Errors | 401; 403. |
| Privilege | Unspecified. |

### API-128 · Create, sign and distribute in one request
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Target rules (at least one); content specification (queries; optional project key); optional signing passphrase header (needed only for a passphrase-protected key). |
| Output | 202 with tracker ID, targets, and the generated release-set name and version. |
| Errors | 400 invalid body or signing-key problem; 401; 403. |
| Privilege | Unspecified. |

Behavior: the service generates name and version.

Interactions: DIST-039 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-129 · List registered edge instances
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Optional limit; include-source flag (default false). |
| Output | Edge instances: service ID, name, type. |
| Errors | 401. |
| Privilege | Unspecified. |

Interactions: DIST-024 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-130 · Distribute a second-generation release-set version
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version; optional storing repository key; optional project key; optional signing passphrase header; body: dry-run flag (default false), target rules, auto-create-missing-repositories flag, path-mapping modifications, allow-insecure-TLS flag, include-evidence flag (default false), base priority (default medium). |
| Output | 200 with tracker ID and targets (also for a dry run). |
| Errors | 400 invalid request or signing-key problem; 401; 409 when a distribution of that version is already running. |
| Privilege | Unspecified. |

Behavior: including evidence ships the evidence records attached to the artifacts; it depends on
an evidence-distribution option being enabled.

Interactions: DIST-027 and
DIST-028 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-131 · Abort, remote-delete and track second-generation distributions
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Abort | Name, version; optional storing repository and project. | 202 with signaled edge instances; 204 when nothing runs. | 401; 403 |
| Remote delete | Name, version; optional storing repository and project; body: dry-run flag, target rules, auto-create flag, base priority (default medium). | Dry run: 200 with tracker ID and targets. Real run: 202 (body unspecified). | 401; 403 |
| List trackers | Name, version; optional storing repository and project. | Per tracker: ID, overall status, initiating user, start time, type (distribute or delete), base priority. | 401; 403 |
| Tracker details | Name, version, tracker ID or friendly ID. | As for first-generation tracker details. | 401; 403 |

Privilege: unspecified.

Interactions: DIST-030, DIST-032 and
DIST-029 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-132 · Change the priority of a running distribution
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | Name, version, tracker ID or friendly ID; body with a new base priority and/or target rules carrying per-target priorities (at least one). |
| Output | Tracker ID and friendly ID, release-set name and version, effective base priority, and per target: name, status, previous priority, effective priority, result. |
| Errors | 400 invalid priority or no matching targets; 401; 403 without distribution administrator permission; 404 tracker not found. |
| Privilege | Distribution administrator. |

Per-target results: applied (distribution to that target had not started; the new priority is
used), recorded (already started; running work keeps the old priority, retries may use the new
one), skipped (already finished), unchanged (target not addressed by the request).

Interactions: DIST-031 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-133 · Export and import release sets for disconnected sites
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Export | Name, version; optional signing passphrase header. | 202; runs asynchronously and produces a zip. | 400; 401 |
| Export status | Name, version. | Status: in progress, not exported, failed, completed. | 401 |
| Delete export | Name, version. | 200. | 401 |
| Import (on the receiving repository manager) | File name of the exported zip (mandatory); dry-run flag (default false). | 200 with JSON. | 400; 401; 403 |

Privilege: unspecified.

Interactions: API-116.

### API-134 · Release-set signing key
Priority: **Later**

| Operation | Inputs | Output |
|---|---|---|
| Get public key | Key name. | The public key as text. |
| Propagate | Key name. | Propagation report per edge instance (shape unspecified). |

Errors: 401; 403. Privilege: administrator. Behavior: this key verifies the integrity of release
sets on the edge instances.

Interactions: DIST-004 and DIST-026 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-135 · GPG keys for artifact signatures
Priority: **Later**

GPG key pairs used to sign release sets and to verify individual artifact signatures on edge
instances.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Set key pair | ASCII-armored public key, ASCII-armored private key, alias. | 200 (body unspecified). | 400 invalid key format; 401 |
| Get default public key | None. | Public key (JSON). | 401 |
| Propagate | A single alias, or the all flag (default false). | Propagation report per edge instance. | 401 |
| List aliases | None. | Array of alias records. | 401 |
| Get by alias | Alias. | Public key. | 401; 404 |
| Rename alias | Current alias; new alias. | 200 (body unspecified). | 401; 404 |
| Delete | Alias. | 200. | 401; 404 |

Privilege: unspecified.

### API-136 · Distribution service tokens
Priority: **Out of scope**

The distribution service issues its own OAuth2-style tokens (form-encoded requests).

| Operation | Inputs | Output | Notes |
|---|---|---|---|
| Create | Grant type client credentials; user name; optional scopes (administrator or groups, space-separated); validity in seconds (0 = default); refreshable flag. | 201 with access token, validity, scope, token type, refresh token when refreshable. | Non-administrators may only create tokens for themselves and never with administrator scope (403). |
| Refresh | Grant type refresh token; refresh token; current access token; optional validity and refreshable flag. | 200, same shape. | — |
| List | None. | Token ID, issuer, subject, issued-at and expiry (epoch seconds), refreshable flag. | Administrator. |
| Revoke | Either the token value or the token ID (not both), per RFC 7009. | 200. | Administrator; 404 unknown token. |

Priority rationale: trove issues all tokens from one place; see
[access-tokens.md](access-tokens.md) and [api-admin-operations.md](api-admin-operations.md).

### API-137 · Destination permissions
Priority: **Later**

Control which users and groups may distribute to, or delete from, which edge instances.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| List | None. | Array of permissions. | 401 |
| Get | Permission name. | Permission. | 401; 404 |
| Create or update | Permission name; destinations (mandatory; site, city, country-code rules) and principals (users and groups mapped to actions distribute and/or delete). | 200 with the stored permission. | 400 (e.g. destinations missing); 401; 409 conflict |
| Delete | Permission name. | 204. | 401; 404 |

Behavior: identifier, name (taken from the path) and resource type (always "destination") are
read-only. Privilege: administrator.

Interactions: DIST-035 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md);
[identity-and-access.md](identity-and-access.md).

### API-138 · Stuck distributions and emergency actions
Priority: **Later**

| Operation | Inputs | Output |
|---|---|---|
| List stuck distributions | None. | Per entry: release-set name and version, generation (first, second, or second-generation end-to-end variant), friendly distribution ID, status (storing in progress, scan in progress, queued, in progress, validate, close in progress, abort in progress), tracker ID. |
| Generate execution token | Action: stop all, or unlock stuck. | Encrypted token valid for 3 minutes. |
| Execute action | Action; dry-run flag (default **true**); the execution token as plain-text body. | 202 in both modes. Unlock: the stuck distributions found plus a flag that is false when nothing was actually unlocked (dry run). Stop all: counts of aborted trackers, edge trackers and edge tracker artifacts, and whether the task queue was flushed. |

Errors: 400 invalid action, or token expired or issued for another action; 401; 403.
Privilege: administrator.

Behavior: stop-all force-stops every in-progress distribution across the cluster, marks all
non-final trackers aborted and flushes the task queue. Execution needs a short-lived token from a
separate call and runs as a dry run unless that is explicitly turned off.

Interactions: DIST-036 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### API-139 · Redistribute recent release sets to one edge instance
Priority: **Later**

| Aspect | Detail |
|---|---|
| Inputs | End date (YYYY-MM-DD, mandatory); number of days to look back (1 to a configured maximum, default maximum 7; mandatory); edge instance name (mandatory); auto-create-missing-repositories flag (default false); base priority (default low for this operation). |
| Output | Number of release sets triggered and their identifiers (repository/name/version). |
| Errors | 400 invalid date, day count out of range, or unknown edge instance; 401; 403. |
| Privilege | Administrator. |

Behavior: selects every release set distributed within the window that ends on the given date.

### API-140 · Distribution service health and settings
Priority: **Later**

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Ping | None. | Message and status code; 500 when a dependent service is unavailable. | 401 when anonymous access is disabled |
| System information | None. | Version, service ID, status (stable or unstable) and, when unstable, which dependencies are down. | 401 |
| Get / update settings | Usage-reporting (call-home telemetry) flag. | The settings (get); 200 without body (update). | 401; 403 |

Privilege: administrator for settings; ping may be anonymous depending on configuration.

Interactions: DIST-037 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

## Server-side plugins

Server-side plugins are scripts (Groovy source) that the system loads and that can declare named
executions, build staging strategies and build promotions. The plugin model itself is described
in EVT-063 to EVT-069 in [events-and-extensibility.md](events-and-extensibility.md). A fixed
privilege for the operations below is unspecified (401 and 403 can occur). The plugin listings are
filtered by the caller's permissions, and each declared execution names its permitted users and
groups; how those lists gate the execute operation is unspecified.

### API-141 · Execute a named plugin execution
Priority: **Later**

Runs a named execution declared by a loaded plugin.

| Aspect | Detail |
|---|---|
| Inputs | Execution name (path); optional parameters as `p1=v1[,v2][|p2=v3]`; optional asynchronous flag (0/1; default synchronous). |
| Output | Plain text produced by the execution (for example `OK`). |
| Errors | 401; 403; 404 when no execution plugins are loaded or the name is unknown; 500. |
| Privilege | Unspecified. The execution declares permitted users and groups (see API-143); whether the system enforces them on this call is unspecified. |

Behavior: multi-valued parameters are separated by semicolons (recommended) or by an encoded pipe
character.

Interactions: EVT-066 in [events-and-extensibility.md](events-and-extensibility.md).

### API-142 · Download plugin source
Priority: **Later**

Returns the source code of a loaded plugin.

| Aspect | Detail |
|---|---|
| Inputs | Plugin name. |
| Output | Script source text (Groovy source media type). |
| Errors | 401; 403; 404; 500. |
| Privilege | Unspecified. |

Interactions: EVT-069 in [events-and-extensibility.md](events-and-extensibility.md).

### API-143 · List plugin executions and staging strategies
Priority: **Later**

Lists the executions and staging strategies that loaded plugins declare, filtered by what the
caller may see; optionally only one of the two kinds.

| Aspect | Detail |
|---|---|
| Inputs | Optional kind: executions or staging. |
| Output | Executions (name, version, description, permitted users, permitted groups, parameters) and staging strategies (name, version, description, parameters). |
| Errors | 401; 403; 500. |
| Privilege | Unspecified; results are filtered by the caller's permissions. |

Interactions: EVT-069 in [events-and-extensibility.md](events-and-extensibility.md).

### API-144 · Get a build staging strategy
Priority: **Later**

Asks a plugin-defined staging strategy how a release build should be staged.

| Aspect | Detail |
|---|---|
| Inputs | Strategy name (path); build name (mandatory); optional parameters (`p1=v1[,v2][|p2=v3]`, semicolon or encoded pipe for multiple values). |
| Output | Default module version (module ID, next release version, next development version); version-control settings (use release branch, release branch name, create tag, tag URL or name, tag comment, next-development commit comment); promotion settings (target repository, comment, status). |
| Errors | 401; 403; 404; 500. |
| Privilege | Unspecified. |

Interactions: EVT-067 in [events-and-extensibility.md](events-and-extensibility.md).

### API-145 · Execute a plugin-defined build promotion
Priority: **Later**

Runs a named promotion declared by a plugin against one build run.

| Aspect | Detail |
|---|---|
| Inputs | Promotion name, build name, build number (path); optional parameters. |
| Output | Plain text (for example `OK`). |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Interactions: EVT-067 in [events-and-extensibility.md](events-and-extensibility.md);
API-163.

### API-146 · Reload plugins
Priority: **Later**

Reloads plugins whose files changed since the last load, independent of the automatic refresh
interval.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Plain text listing the loaded plugin files. |
| Errors | 401; 403; 500. |
| Privilege | Unspecified. |

Interactions: EVT-063 in [events-and-extensibility.md](events-and-extensibility.md).

## Live logs

### API-147 · Tail server logs over the API
Priority: **P2**

Reads server log files in chunks over HTTP, without shell access to the nodes.

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Configuration | None. | Available logs, available cluster nodes, and the suggested refresh interval in milliseconds. | 401; 403; 500 |
| Data | Log identifier (mandatory); optional file size. | A content chunk of at most 300 KB, the log file's last-modified time, the response time, and the current total file size. | 400 when the identifier is missing or empty; 401; 403; 500 |

Privilege: unspecified (403 can occur).

Behavior: the configuration returns a refresh interval for polling. The meaning of the file-size
input is unspecified beyond its name, and how a cluster node is selected is unspecified.

Interactions: [operations-and-monitoring.md](operations-and-monitoring.md) (logs).

## Webhook subscriptions

Webhook subscriptions bind an event filter to exactly one delivery handler. The event catalog,
payloads and delivery semantics are in [events-and-extensibility.md](events-and-extensibility.md)
(EVT-001 to EVT-034); this section covers the management operations.

Privilege for all operations in this section: system administrator, project administrator, a user
whose role grants webhook management, or an access token scoped to webhook administration with the
matching action (read for listing, fetching and troubleshooting; write for create and update;
delete for delete; execute for test). Project administrators may create subscriptions only in the
artifact, artifact-property, container-image and build domains. Authentication is by access token.

### API-148 · Webhook subscription document
Priority: **P1**

The document that create, update, read and test exchange.

<details>
<summary>Subscription fields</summary>

| Field (descriptive) | Meaning | Default / constraint |
|---|---|---|
| Key | Unique subscription identifier, immutable. | Mandatory on create; starts with a letter, followed by at least one letter, digit, `_` or `-` (so at least two characters); at most 500 characters. |
| Project key | Project that owns the subscription. | Needed when every chosen event type supports project scope; on update it must equal the stored value. |
| Description | Free text. | unspecified |
| Enabled | Whether events are delivered. | false |
| Event domain | Domain identifier, for example artifact, build, release-set promotion. | Mandatory |
| Event types | Event types of the domain to subscribe to. | Mandatory, at least one |
| Criteria | Domain-specific filter: any local / any remote / any federated repository; any build; any release set; explicit repository keys; selected builds; include and exclude path patterns; registered release-set names; selected release sets (name to versions); selected environments; application keys; lifecycle stages. | unspecified |
| Handlers | Delivery handlers. | Exactly one |
| Debug | Record execution details for troubleshooting. | false |

</details>

<details>
<summary>Handler fields</summary>

| Handler kind | Field (descriptive) | Meaning | Default / constraint |
|---|---|---|---|
| Standard webhook | Target URL | Delivery endpoint. | Mandatory, URI |
| Standard webhook | Proxy | Key of a configured outbound proxy (not a URL); not used on managed (SaaS) deployments. | unspecified |
| Standard webhook | Secret | Shared secret; returned only in encrypted form; when updating without rotation, send the returned value back unchanged. | Optional |
| Standard webhook | Use secret for signing | Use the secret to sign the payload (see EVT-024). | false |
| Standard webhook | Custom headers | Name/value pairs added to the request. | unspecified |
| Templated webhook | Target URL | Delivery endpoint. | Mandatory, URI |
| Templated webhook | Proxy | As above. | unspecified |
| Templated webhook | Method | POST, PUT, GET, PATCH or DELETE. | POST |
| Templated webhook | Payload | JSON body template using Go template placeholders that address event fields. | unspecified |
| Templated webhook | Headers | Name/value pairs. | unspecified |
| Templated webhook | Named secrets | Secrets referenced from the template; names start with a letter or `_`, then letters, digits or `_`. Values are write-only. | unspecified |

</details>

Behavior: the secret values of templated handlers are never returned, only their names.

Interactions: EVT-018 to EVT-028 in [events-and-extensibility.md](events-and-extensibility.md).

### API-149 · List webhook subscriptions
Priority: **P1**

Returns all subscriptions visible to the caller.

| Aspect | Detail |
|---|---|
| Inputs | None. |
| Output | Array of subscription documents (API-148) with secrets masked as described there. |
| Errors | 401; 403. |
| Privilege | Webhook read (see section introduction). |

### API-150 · Create a webhook subscription
Priority: **P1**

Registers a new subscription.

| Aspect | Detail |
|---|---|
| Inputs | Subscription document (key, enabled flag, event filter and handler mandatory). |
| Output | 201 with the stored document. |
| Errors | 400 invalid input; 401; 403. |
| Privilege | Webhook write; project administrators limited to the domains named in the section introduction. |

### API-151 · Get a webhook subscription
Priority: **P1**

Returns one subscription by key.

| Aspect | Detail |
|---|---|
| Inputs | Subscription key. |
| Output | Subscription document. |
| Errors | 401; 403; 404. |
| Privilege | Webhook read. |

### API-152 · Replace a webhook subscription
Priority: **P1**

Replaces a subscription's definition.

| Aspect | Detail |
|---|---|
| Inputs | Subscription key (path); document with enabled flag, event filter and handler mandatory. |
| Output | 204, no body. |
| Errors | 400 (also when the project key differs from the stored one); 401; 403; 404. |
| Privilege | Webhook write. |

Behavior: replace semantics. For templated handlers the full secret list is sent: a named secret
without value keeps its stored value, a value adds or rotates it, an empty value wipes it, and a
name left out of the list removes that secret. For the standard handler, an unchanged encrypted
secret preserves the stored secret.

### API-153 · Delete a webhook subscription
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Subscription key. |
| Output | 204, no body. |
| Errors | 401; 403; 404. |
| Privilege | Webhook delete. |

### API-154 · Test a webhook subscription
Priority: **P1**

Sends a test delivery for a subscription definition supplied in the request, without storing it.

| Aspect | Detail |
|---|---|
| Inputs | A complete subscription document, as for create. |
| Output | 200 on success. |
| Errors | 400; 401; 403. |
| Privilege | Webhook execute. |

Behavior: whether the test uses a sample payload of the selected event type is unspecified.

Interactions: EVT-031 in [events-and-extensibility.md](events-and-extensibility.md).

### API-155 · Stream or fetch webhook delivery records
Priority: **P1**

Returns delivery records, either as a live stream or as a historical slice.

| Aspect | Detail |
|---|---|
| Inputs | Optional subscription key filter; optional target URL filter; optional start time; optional end time; optional maximum record count (times as Unix timestamps). |
| Output | Per record: timestamp, elapsed milliseconds, errors, request (method, URL, headers, payload, retries attempted), response (status, headers, body), event (identifier, subscription key, domain, event type, data, source). |
| Errors | 401; 403. |
| Privilege | Webhook read. |

Behavior:

- Without start time: a live stream, filterable by subscription and target.
- Start only: records since that time, then the stream stays open and continues live.
- Start with end or count: a bounded historical fetch; end and count are invalid without start.
- Start = 0 returns everything still retained. The store keeps the last 10,000 records; older ones
  are purged every 30 seconds.

Interactions: EVT-032 and EVT-033 in [events-and-extensibility.md](events-and-extensibility.md).

## Build information

Build information records what a CI run produced and consumed: build name and number, timing,
CI agent and build tool, version-control state, modules with their artifacts and dependencies
(checksums, scopes), environment properties, linked issues, and retention hints. Records are
stored per project in a build-information repository. Behavior is described in
[builds-and-ci.md](builds-and-ci.md). Permissions on builds are granted per build name (read,
deploy, delete) in addition to repository permissions; see
[identity-and-access.md](identity-and-access.md).

### API-156 · Build information document
Priority: **P1**

The document uploaded by API-158 and returned by API-162.

<details>
<summary>Build information fields</summary>

| Field group (descriptive) | Content |
|---|---|
| Identity | Schema version; build name; build number; build type (Maven, Gradle, Ant, Ivy, generic); start time (ISO 8601 with milliseconds and offset); duration in milliseconds. |
| Tooling | Build tool name and version; CI server name and version; CI server URL; version of the repository-manager integration plugin used; the repository-manager user that deployed. |
| Version control | List of entries: revision, commit message, branch, repository URL. |
| License checks | Run checks after the build; include published artifacts; auto-discover licenses; dependency scopes to check; notification recipients. |
| Retention hints | Delete build artifacts too; maximum number of builds to keep; earliest build date to keep (epoch milliseconds); build numbers never to discard. |
| Modules | List of modules: identifier, type, properties; artifacts (type, SHA-1, SHA-256, MD5, name, path, original deployment repository); dependencies (type, SHA-1, SHA-256, MD5, identifier, scopes, requested-by chains as lists of identifiers). |
| Issues | Issue tracker name and version; aggregate-issues flag; aggregation status; affected issues (key, URL, summary, aggregated flag). |
| Properties | Environment variables and properties captured from the CI server. |

</details>

Behavior: all modules must carry the build name and build number as properties and correct SHA-1
and MD5 checksums, otherwise they are not properly linked into the build record.

### API-157 · List builds
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Optional project key. |
| Output | Self link and per build name: relative link and last start time. |
| Errors | 401; 403. |
| Privilege | Read permission on the build (or basic read); anonymous allowed when enabled. |

### API-158 · Upload build information
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Build information document (API-156); optional project key. |
| Output | 200. |
| Errors | 400 malformed; 401; 403. |
| Privilege | Deploy permission on the build; delete permission as well when an existing record with the same name and number is overwritten. |

Interactions: EVT-007 in [events-and-extensibility.md](events-and-extensibility.md) (build
events).

### API-159 · List the runs of a build
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Build name; optional project key. |
| Output | Self link and per run: relative link (build number) and start time. |
| Errors | 401; 403; 404 unknown build. |
| Privilege | Read permission on the build (or basic read); anonymous allowed when enabled. |

### API-160 · Delete build runs
Priority: **P1**

Two forms: by query parameters, and by JSON body (the body form also handles build numbers with
special characters).

| Aspect | Detail |
|---|---|
| Inputs | Build name; build numbers (comma-separated, or a list in the body form); delete-artifacts flag (default off); delete-all flag (default off; build numbers may then be omitted); project key (body form; default project when absent). |
| Output | Plain-text confirmation listing the deleted runs. |
| Errors | 400 no build numbers or malformed; 401; 403 without delete permission; 404 unknown build (query form). |
| Privilege | Delete permission on the build. |

Behavior: with the delete-artifacts flag, the artifacts linked to the runs are deleted together
with the records.

### API-161 · Append modules to a build run
Priority: **P2**

| Aspect | Detail |
|---|---|
| Inputs | Parent build name and number; optional parent start time (ISO 8601); optional build-information repository key; optional project key; body: list of modules (shape as in API-156). |
| Output | 204. |
| Errors | 400; 401; 403; 404 build record not found. |
| Privilege | Deploy and delete permission on the build. |

### API-162 · Get a build run
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Build name and number; optional start time; optional older build number to compare against (diff); optional project key (default build-information repository when absent). |
| Output | Self link and the build information document. |
| Errors | 401; 403 without read permission on the build; 404. |
| Privilege | Read permission on the build. |

Behavior: with the diff input, the response compares the run's contents with the older run; the
shape of the comparison result is unspecified.

### API-163 · Promote a build run
Priority: **P1**

Changes a run's status and optionally copies or moves its artifacts and dependencies to a target
repository, attaching properties.

<details>
<summary>Promotion inputs</summary>

| Input (descriptive) | Meaning | Default |
|---|---|---|
| Build name, number | The run to promote (path). | — |
| Project key | Project of the build (default build-information repository when absent). | unspecified |
| Status | New status of the run; the only change when no target repository is given. | unspecified |
| Comment | Reason for the promotion. | unspecified |
| CI user | User who invoked the promotion on the CI server. | Mandatory |
| Timestamp | Time the promotion was received (ISO 8601). | Mandatory |
| Dry run | Validate without changing anything. | false |
| Source repository | Where the contents are taken from; resolved automatically when absent. | unspecified |
| Target repository | Where they go; may be a virtual repository. | unspecified |
| Copy | Copy instead of move. | false (move) |
| Artifacts | Include the build's artifacts. | true |
| Dependencies | Include the build's dependencies. | false |
| Scopes | Dependency scopes to include (additive), e.g. compile, runtime. | All scopes |
| Properties | Properties to set on the promoted artifacts, also without a target repository. | unspecified |
| Fail fast | Abort on the first error. | true |

</details>

| Aspect | Detail |
|---|---|
| Output | List of messages, each with level (error, warning, info) and text. |
| Errors | 401; 403 without deploy permission on the build; 404 unknown build. |
| Privilege | Deploy permission on the build (plus the repository permissions the copy or move needs; unspecified). |

Interactions: API-023; API-024; API-145.

### API-164 · Rename a build
Priority: **P2**

| Aspect | Detail |
|---|---|
| Inputs | Current build name; new name (mandatory); project key (mandatory when the build belongs to a project). |
| Output | Plain text stating that the rename has started. |
| Errors | 400; 401; 403; 404. |
| Privilege | Deploy and delete permission on the build. |

Behavior: the rename runs asynchronously; it is meant to keep build records in sync with a renamed
CI job.

### API-165 · Set build retention
Priority: **P1**

| Aspect | Detail |
|---|---|
| Inputs | Build name; asynchronous flag (default true); body: delete-artifacts flag, maximum number of runs to keep, earliest start date to keep (ISO 8601), build numbers never to discard. |
| Output | 200. |
| Errors | 400; 401; 403; 404. |
| Privilege | Delete permission on the build. |

Behavior: setting retention does not delete runs immediately. Synchronous mode waits for the
cleanup unless a system option forces asynchronous execution.

Interactions: RET-037 and RET-023 in
[retention-and-cleanup.md](retention-and-cleanup.md).

### API-166 · Promote a container image
Priority: **P1**

Copies or moves a container image (or every tag of an image) from one repository to another,
optionally renaming image and tag.

| Aspect | Detail |
|---|---|
| Inputs | Source repository key; target repository (mandatory); image name (mandatory); optional target image name (default: same); optional tag (absent = all tags of the image); optional target tag (default: same); copy flag (default false = move). |
| Output | Plain text. |
| Errors | 400; 401; 403; 404 repository or image not found. |
| Privilege | A permitted user; the exact permissions checked are unspecified. |

Behavior: supports local and federated repositories, and also Helm charts stored as OCI images.

Interactions: [formats/README.md](formats/README.md) (Docker/OCI).

## Notes for implementers

- **Encodings are not uniform, and clients send exactly what each operation specifies.** Boolean
  flags are `0`/`1` in older operations and `true`/`false` in newer ones; times are epoch
  milliseconds, ISO 8601 with offset, or local date-times without offset (pruning status). Accept
  the specified form per operation rather than normalizing the whole API.
- **Several successful mutations answer with plain text, not JSON** — repository create and
  update, index recalculation, build deletion and rename, container promotion. A compatible
  implementation keeps the specified status codes; clients cannot rely on a JSON body there.
- **Repository deletion reports success-shaped bodies for missing repositories** (404 with the
  success flag set; batch delete answers 200 when every key is missing). Clients must check the
  status code and every per-repository report, not only the overall flag (API-063,
  API-064).
- **Batch semantics differ:** batch create and batch update fail as a whole when one element is
  invalid; batch delete is best-effort per repository with 207 for mixed results.
- **Item information modes are mutually exclusive.** Combining two mode parameters is a 400;
  listing parameters are valid only with the list mode. Sizes are strings, and folders report
  size `-1` in listings (API-003, API-004).
- **Property lists use separators and escaping.** Pairs are separated by `|` or `;`, values by `,`,
  and literal `, \ | =` need an encoded backslash (`%5C`) before them. Property search instead
  takes arbitrary query parameters as property keys (API-009,
  API-095).
- **Deploy by checksum uses 404 as a protocol signal** meaning "content unknown, upload it", not
  "repository missing"; the client then falls back to a normal upload (API-014).
- **Project-scoped remote repositories have an empty upstream URL** in every read operation; the
  shared repository's key takes its place. Validation and UI must not treat the empty URL as a
  misconfiguration (API-057, API-061).
- **Trash-can operations report missing permission as 400, not 403.** Expired trash items are
  purged by garbage-collection runs, orphan binaries only by the full cycle (every n-th run,
  default 20) (API-111, API-113).
- **Latest-version placeholders in download paths resolve only over local repositories, remote
  caches and virtual repositories**, cannot mix release and integration forms, and need both
  integration revision tokens in the repository layout (API-016).
