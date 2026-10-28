# Generic format

This page catalogs the generic format: repositories that store files of any kind without
format-specific metadata, indexes or client protocol. A generic repository is the place for
installers, archives, documents, media, build outputs, HTTP build caches and every format the
system does not support natively; a generic remote repository proxies an arbitrary HTTP download
source, and a generic virtual repository puts several repositories behind one URL. Plain HTTP
`PUT` and `GET` are the client protocol, so most of the behavior is the system's general item
handling, described in [../artifacts.md](../artifacts.md); this page states what is specific to,
or especially relevant for, the generic format and links the general entries. Repository classes
in general are described in [../repositories.md](../repositories.md), pull-through caching in
general in [../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/files/<repo-key>/` stands for the URL of one
repository (the path layout is the implementation's choice), `generic-local`, `generic-remote` and
`generic-virtual` are example repository keys, and `${TOKEN}` is an access token.

## Repositories

### PKG-GENERIC-001 · Generic repositories in all three repository classes
Priority: **P1**

Generic is a repository format like any other and is offered for local, remote and virtual
repositories (REPO-003 and REPO-006 in [../repositories.md](../repositories.md)); the API's format
sets also list it for federated repositories (REPL-026 in
[../replication-and-federation.md](../replication-and-federation.md)).

Behavior:

- A generic repository accepts any file at any path. It does not parse the file, compute package
  metadata, maintain an index or answer a format-specific client protocol.
- A file stored in a generic repository is an *artifact* (an item with checksums and properties),
  never a *package*: it does not appear as a package with versions in the package list (ART-001 and
  ART-048 in [../artifacts.md](../artifacts.md)).
- A native client of a specific format (yum, gem, npm and so on) needs a repository of that
  format; a generic repository does not serve it.
- Typical uses: installers, media and other loose files; proxying formats
  the system does not support; storage for build outputs and an HTTP build cache (PKG-GENERIC-009).

Creation: system administrator or project administrator. UI location: administration,
repositories, create repository, choose the class, then the generic format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)). When the format is omitted on API creation, the
configuration schema defaults it to generic (REPO-002).

Interactions: scanner indexing is available for generic repositories (REPO-021 in
[../repositories.md](../repositories.md); SEC-042 in
[../security-scanning-and-curation.md](../security-scanning-and-curation.md)); retention policies
apply to generic content through their path and property conditions (RET-003 and RET-006 in
[../retention-and-cleanup.md](../retention-and-cleanup.md)); generic is among the formats for which
downloads can be redirected to signed object-storage URLs (STOR-032 in
[../storage-and-blobs.md](../storage-and-blobs.md)).

### PKG-GENERIC-002 · Non-conformant content belongs in a generic repository
Priority: **P1**

Format repositories carry built-in logic for parsing metadata, building index files and optimizing
resolution. The system does not block an upload whose content does not match the repository's
format (for example an image or text file in an RPM repository), but it does not index such a file
and does not update the repository's metadata for it. Such content belongs in a generic
repository, because non-conformant files in a format repository degrade indexing and performance.

Consequences an implementation has to preserve:

- No rejection: a mismatching upload into a format repository succeeds (unless a format-specific
  rule such as a strict layout rejects it; see the format pages) and is stored as a plain item.
- Format metadata is computed only from conformant uploads.
- A generic repository never rejects content on format grounds.

### PKG-GENERIC-003 · Free-form paths and the repository layout reference
Priority: **P1**

A generic repository imposes no path structure: the client chooses the full path, and folders are
created implicitly by the paths of deployed files (or explicitly, ART-002 in
[../artifacts.md](../artifacts.md)).

Every repository still references a layout (REPO-012 in [../repositories.md](../repositories.md)).
For generic repositories the layout only matters for layout-based maintenance and module
identification (REPO-046 and REPO-052); it has no effect on storage or resolution.

| Setting | Meaning | Default |
|---|---|---|
| Repository layout | Named layout used to identify modules and versions in paths | unspecified for generic; a generic local repository can use, for example, a flat "simple" layout or the Maven 2 layout |

Edge cases: because the layout is decorative for generic content, version-oriented functions
(latest-version lookups, version cleanup) only work on generic content that happens to follow the
referenced layout.

## Deploy and download

### PKG-GENERIC-004 · Deploy any file
Priority: **P1**

The client protocol of the generic format is the system's generic upload: an HTTP `PUT` of the raw
file content to `<repository URL>/<path>` (ART-002 in [../artifacts.md](../artifacts.md)).

```bash
curl -fsS -H "Authorization: Bearer ${TOKEN}" -T installer-2.4.1.msi \
  "https://repo.example.com/files/generic-local/tools/installer/2.4.1/installer-2.4.1.msi"   # example
```

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Deploy a file | Repository key, path, raw body; optional properties as matrix parameters (ART-003); optional checksum headers | Item information of the stored file | Deploy on the target path; replacing an existing file is governed by the overwrite rules of ART-002 |
| Deploy by checksum | Repository key, path, checksum headers, no body | Stored item if the content already exists anywhere in the system, otherwise "not found" so the client sends the content | Deploy (ART-004) |
| Deploy an archive and extract it | Archive (`zip`, `tar`, `tar.gz`, `tgz`), target folder | Extracted files below the target | Deploy (ART-005) |
| Multi-part upload | Large file in parts | Stored file | Deploy; needs S3 or Google Cloud Storage as the binary store (ART-007) |
| Deploy through a virtual repository | As above | Written to the virtual repository's default deployment repository | Deploy on that local repository (REPO-038) |

UI location: artifact browser, deploy dialog (ART-006 in [../artifacts.md](../artifacts.md), UI-013
in [../ui-inventory.md](../ui-inventory.md)).

Capabilities the companion CLI relies on (BLD-036 in [../builds-and-ci.md](../builds-and-ci.md)):
properties attached at upload time, checksum-first upload that skips content the server already
has, server-side extraction of an uploaded archive, multi-part upload of large files (only when the
system stores binaries in S3 or Google Cloud Storage), and folders as items that can carry
properties. The client splits an upload into parts only above a size threshold; its client
defaults are 200 MiB as threshold, 5 parts and 20 MiB per part, and a part count of 0
turns splitting off. The client can also preserve symbolic links on Linux and macOS; how the
server records a link is unspecified.

Edge cases:

- An uploaded file name that conflicts with the include/exclude patterns of the target repository
  is rejected (REPO-011).
- Uploading to a remote repository is not possible; remote repositories are filled by caching only.

### PKG-GENERIC-005 · Download any file
Priority: **P1**

The generic download is an HTTP `GET` of `<repository URL>/<path>` on a local, remote or virtual
generic repository (ART-011 in [../artifacts.md](../artifacts.md)).

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Download a file | Repository key, path; optional matrix parameters that constrain resolution by properties (ART-012) | File content | Read |
| Download an entry from inside an archive | Archive path plus entry path | Entry content (ART-014) | Read |
| Download a folder as an archive | Folder path, archive type | Archive (ART-015) | Read |

Capabilities the companion CLI relies on (BLD-036 in [../builds-and-ci.md](../builds-and-ci.md)):

- Parallel multi-part download of one file: the client splits files above a size threshold
  (5120 KB, 3 parts by default in the client) into parts fetched in parallel. This presumes that
  the server answers byte-range requests; the server side is unspecified.
- Checksum verification of downloaded files against the server's checksums (ART-008), skippable on
  the client.
- Download skip when the local file's checksum already matches.
- Sorting, limit and offset over the matched items (for example "the ten most recently modified
  files"), which the client resolves through the query language (SRCH-033 in
  [../search-and-query.md](../search-and-query.md)).
- The client's option to download single archive entries by pattern is deprecated and no longer
  supported by newer server versions.

Edge cases: see PKG-GENERIC-006 for files in a remote repository that are not cached yet.

## Remote and virtual repositories

### PKG-GENERIC-006 · Generic remote repository for any HTTP source
Priority: **P1**

A generic remote repository proxies and caches files from any HTTP(S) location, for example a
vendor download site, a mirror, a Git hosting provider's archive URLs or a format the system does
not support (RMT-001 in [../remote-repositories.md](../remote-repositories.md)). A request for
`<repository URL>/<path>` is forwarded to `<upstream URL>/<path>` on a cache miss; the response is
stored and served. All common remote settings apply (RMT-004 to RMT-037). Generic-specific
settings:

| Setting | Meaning | Default |
|---|---|---|
| List remote content | Lets users browse the upstream's content through this repository, including items not cached yet (RMT-038) | off |
| Propagate query parameters | Query parameters of the client request are passed on to the upstream (RMT-011) | unspecified |
| Retrieve SHA-256 from the upstream | See PKG-GENERIC-007 | unspecified |

UI location: basic tab (list remote content) and advanced tab (the other two) of a generic
remote repository.

Edge cases:

- Search does not see uncached files. Searches and query-based operations only return items the
  system already knows: files in local repositories and files already cached by a remote. A file
  that a plain `GET` through the remote would fetch is therefore *not* found by a search-driven
  client download until it has been fetched once. A client that needs it must request it by URL
  first. Transitive search through a virtual repository (PKG-GENERIC-008) only reaches remotes
  that are members of that virtual repository, so it is no substitute for the first `GET` on a
  standalone generic remote with an empty cache.
- The same holds for chained remote repositories (RMT-040) and for edge instances until the item
  is cached locally.
- Source absence detection on chained remotes (RMT-043 in
  [../remote-repositories.md](../remote-repositories.md)) is not supported for generic
  repositories.

### PKG-GENERIC-007 · SHA-256 lookup for uncached items
Priority: **P2**

Some tools ask for an artifact's SHA-256 checksum before downloading the artifact. Against a remote
repository that has not cached the item yet, such a checksum request would fail with "not found".
With this setting the remote repository retrieves the SHA-256 from the upstream when the item is
not cached, serves it, and later computes and stores its own checksum when the file is downloaded
and cached, serving it as usual from then on.

| Setting | Meaning | Default |
|---|---|---|
| Retrieve SHA-256 from the upstream | Answer checksum requests for uncached items with the upstream's SHA-256 | unspecified |

Limits: generic remote repositories only (RMT-022 in
[../remote-repositories.md](../remote-repositories.md)). How the upstream checksum is located
(companion `.sha256` file, response header or other) is unspecified.

### PKG-GENERIC-008 · Generic virtual repository
Priority: **P1**

Like any virtual repository, a generic one puts several member repositories (local, remote,
virtual) behind one URL (REPO-033 in [../repositories.md](../repositories.md)). Specific to the
generic format:

- Unlike every other format, a generic virtual repository may aggregate member repositories of
  *any* format, not only generic ones.
- It maintains no metadata of its own; a request is resolved by walking the members in order
  (REPO-035) and serving the first hit.
- Deploys through the virtual repository go to its default deployment repository (REPO-038).

A search that targets a virtual repository covers its local members and the caches of its remote
members (SRCH-035 in [../search-and-query.md](../search-and-query.md)). The companion CLI's
transitive mode extends this to the remote members themselves, limited to the first five
(SRCH-039). This adds load when many remote repositories sit behind the virtual repository;
transitive mode reaches only the member remotes, never an arbitrary URL.

Edge cases: because members of other formats may be aggregated, format-specific behavior
(metadata merging, URL rewriting) is not applied; a client of a specific format must use a
virtual repository of that format.

## Workflows on generic content

### PKG-GENERIC-009 · HTTP build cache on a generic repository
Priority: **P2**

A generic local repository can serve as the HTTP remote cache of a build tool that speaks a plain
`PUT`/`GET` cache protocol, for example Bazel's HTTP/1.1 remote cache:

```bash
bazel build --remote_cache=https://<user>:${TOKEN}@repo.example.com/files/bazel-cache   # example
```

- Bazel stores action results under `ac/<sha256>` and output files (content-addressed blobs) under
  `cas/<sha256>`, relative to the cache URL, uploading with `PUT` and downloading with `GET`.
- The repository needs no special settings; any generic local repository works.
- Credentials are embedded in the cache URL as basic credentials (user name plus token).

Security note: anyone who can write to the cache can poison the outputs of later builds. Grant
access to the cache, and the right to change cached entries, only to selected users or build
service accounts (IAM-019 in
[../identity-and-access.md](../identity-and-access.md)).

Validation: check the server's request log for the cache's `PUT` and `GET` requests after the
first build.

Edge cases: whether writing an existing `ac/` or `cas/` path again needs the overwrite permission
is unspecified.

### PKG-GENERIC-010 · Bulk file operations on generic content
Priority: **P2**

Generic repositories are mostly operated in bulk by scripts and the companion CLI (BLD-036 and
BLD-037 in [../builds-and-ci.md](../builds-and-ci.md)). The server-side capabilities these clients
combine:

| Client operation | Server capability used |
|---|---|
| Search by path pattern, with property filters, exclusions, build or release-set filters, sorting, limit, offset, count only | Query-language search (SRCH-023 to SRCH-033 in [../search-and-query.md](../search-and-query.md)); selectable output fields: name, repository, path, MD5, SHA-1, SHA-256, size, created and modified timestamps and users, properties |
| Copy and move, preserving or flattening the hierarchy, with placeholders built from regular-expression groups | Copy and move items (ART-032, ART-033 in [../artifacts.md](../artifacts.md)) |
| Delete by pattern with confirmation and dry run | Delete items (ART-034); the trash can applies (ART-036) |
| Set and delete properties on files and, optionally, folders | Set and delete item properties (ART-023, ART-025) |
| Upload with "sync deletes": below a given path, remove every item that does not match the pattern of the files just uploaded | Search plus delete |
| Set or delete properties only on the items of the repository named first in the pattern, without walking the rest of the path pattern | Set and delete item properties (ART-023, ART-025) |
| Fail when nothing matched (exit code 2) | Client-side; relies on searches returning an empty result rather than an error |

Edge cases:

- Dry runs still contact the server to resolve the list of affected items.
- Retries in the client repeat an HTTP attempt within one run; they do not resume an interrupted
  process. A re-run skips files that are already complete through the checksum comparison.

### PKG-GENERIC-011 · Generic repository as cross-format copy and move target
Priority: **P1**

Copying or moving an item between repositories (ART-032 and ART-033 in
[../artifacts.md](../artifacts.md)) normally requires a target of the same format as the item. A
generic repository is the exception: it accepts copies and moves of items of any format. Example:
a Debian package can go to another Debian local repository or to a generic local repository,
nowhere else.

- The copy and move dialog of the artifact browser offers as targets the local repositories, and
  the virtual repositories that have a default deployment repository (REPO-038 in
  [../repositories.md](../repositories.md)), whose format is either generic or the item's own.
- An item copied into a generic repository becomes a plain file there: no package metadata, no
  index entry (PKG-GENERIC-001).

Edge cases: whether a copy or move from a generic repository into a format repository is checked
against that format, and whether the operation API enforces the same rule as the dialog, is
unspecified.

## Notes for implementers

- The generic format has no client protocol beyond `PUT` and `GET` on item paths. Every capability
  a generic client uses is a general item capability; implement those once and do not special-case
  generic paths.
- A generic virtual repository is the only virtual repository that may mix formats. Do not apply
  format-specific merging or URL rewriting there, and do not let the format check of virtual
  membership reject non-generic members.
- Clients that locate files by search miss uncached remote content. A remote that only proxies an
  upstream will look empty to them until a plain `GET` has populated the cache.
- Checksum-first uploads are what make repeated bulk uploads cheap: a checksum-only deploy answers
  404 when the content is unknown (or unreadable for the caller), and the client then sends the
  body (ART-004).
- Multi-part upload needs S3 or Google Cloud Storage on the server side; how a client learns
  whether the server offers it is unspecified.
- Allow a generic repository as copy and move target for items of every format; for all other
  targets keep the same-format rule (PKG-GENERIC-011).
- The client's parallel download presumes byte-range support on every generic download path,
  including files served from a remote repository's cache.
- Accept content of any type and name in a generic repository; the only rejections are include and
  exclude patterns, path rules common to all repositories, and permissions.
- A build cache on a generic repository writes many small content-addressed files; decide
  explicitly whether rewriting an existing path needs the overwrite permission there.
