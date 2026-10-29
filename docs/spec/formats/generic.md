# Generic format

Status: specification — local class implemented in milestone M1; remote and virtual classes not implemented

This page specifies the generic format: repositories that store files of any kind at paths the
client chooses, without package metadata, an index or a client protocol beyond plain HTTP `PUT`,
`GET` and `DELETE` on `/files/`. It specifies the local class in full (MVP, milestone M1, Q-ROAD-1)
and the remote and virtual classes as P1. It is the smallest format and the first entry of the
format catalog ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-003): what it adds
to the repository model is a descriptor, a flat layout, a request classification that never yields a
metadata document, and the rule that nothing is refused on format grounds. Everything else is the
general item handling of [storage.md](../storage.md) and is referenced, not repeated.

| Topic | Owner |
|---|---|
| Repository model: classes, keys, configuration document, include and exclude patterns, layouts, create, read, update, list, delete, repository state on n replicas | [repositories-and-proxy.md](../repositories-and-proxy.md) |
| Raw deploy, folder creation, streaming download, `Range` and conditional requests, download integrity, item information, folder listing, delete, copy and move, properties, trash can, quotas | [storage.md](../storage.md) |
| Request path, content headers on stored content, format plug-in contract, path canonicalisation | [architecture.md](../architecture.md) (R-ARCH-005, R-ARCH-007, R-ARCH-008, R-ARCH-011, R-ARCH-014) |
| Repository actions and the permission rules of a raw deploy (`deploy`, `overwrite`, `delete`) | [authorization.md](../authorization.md) (R-AUTHZ-005, R-AUTHZ-006) |
| `/files/` and the items operations as management API | [api-and-ui.md](../api-and-ui.md) (R-API-025) |

Covers: [features/formats/generic.md](../../features/formats/generic.md) (`PKG-GENERIC-*`), and the
generic-specific parts of [features/repositories.md](../../features/repositories.md) and
[features/artifacts.md](../../features/artifacts.md).

Conventions on this page:

- `https://trove.example.com` is the public base URL *(example)*; `generic-local` is an example
  local repository key, `generic-remote` and `generic-all` example remote and virtual keys;
  `$TROVE_TOKEN` is a trove token (INV-8).
- The *file URL* of a path is `<public base URL>/files/<repoKey>/<path>`. Paths in tables are
  relative to the repository root.
- Priorities: the local class is MVP, the remote and virtual classes are P1, three
  workflows on generic content are P2. The `Descriptor` of the format lists only the classes that
  are implemented (R-ARCH-008: `Classes` is the single source of truth), so a create of a remote or
  virtual generic repository answers 422 `unsupported_class` until the P1 requirements ship.

## The local class

### R-GENERIC-001 · The generic format and its descriptor
Priority: **MVP** · Covers: PKG-GENERIC-001

- The format name is `generic`. It is registered in the format catalog (R-REPO-003) with this
  descriptor; `GET /api/v1/formats` returns it:

| Field | Value | Meaning |
|---|---|---|
| `name` | `generic` | Format ID (`^[a-z][a-z0-9-]{1,31}$`) |
| `classes` | `["local"]`; `["local","remote","virtual"]` once R-GENERIC-009 and R-GENERIC-010 ship | Classes the implementation accepts |
| `layout` | `generic` | The flat layout (R-GENERIC-003) |
| `protocolBasePath` | `/files/{key}/` | The format has no protocol of its own; the raw file URL is the protocol |
| `defaultUpstreamUrl` | `null` | A generic remote repository names its upstream explicitly (R-GENERIC-009) |
| `defaultMetadataTtlSeconds`, `defaultNotFoundTtlSeconds` | `null`, `1800` | The format has no metadata documents, so no metadata period; only the not-found period (used by the P1 remote class) has a default |
| `patternSubject` | `path` | Include and exclude patterns are path patterns (R-GENERIC-007) |
| `priorityMembers`, `pinsAndClaims`, `caps` | `false` | No package names, so nothing for these mechanisms to act on |

- A generic repository accepts any file at any path. It does not parse the file, compute package
  metadata, maintain an index or answer a format-specific client protocol. A client of a specific
  format (npm, NuGet, …) needs a repository of that format.
- A file in a generic repository is an *item* with digests and a media type, never a *package*:
  it creates no package or version row and does not appear in the package view
  ([storage.md](../storage.md) R-STOR-003, R-STOR-016 `package` is `null`).
- The format has no format-specific settings: the configuration document has no `generic` member,
  and one that is sent answers 400 `invalid_document` like any unknown member (R-REPO-008).
- Typical uses: installers, archives, documents, media, build outputs, and content of formats trove
  does not support natively.

Acceptance criteria:

- `curl -s https://trove.example.com/api/v1/formats` lists `generic` with the field values above.
- `POST /api/v1/repositories` with `{"key":"generic-local","class":"local","format":"generic"}`
  returns 201; a body with a `generic` member returns 400 `invalid_document`; the same create with
  `"class":"remote"` returns 422 `unsupported_class` until R-GENERIC-009 ships.
- After a deploy into `generic-local`, `GET /api/v1/repositories/generic-local/items?path=…`
  shows `"package": null`.

### R-GENERIC-002 · Free-form paths
Priority: **MVP** · Covers: PKG-GENERIC-001

A generic repository imposes no path structure: the client chooses the full path, and folders arise
implicitly from the paths of deployed files or explicitly (R-STOR-007).

- A canonical path is what `repository.CleanPath` returns for the request path (R-ARCH-005): the
  percent-escapes are decoded exactly once, the segments are separated by `/`, and the result has
  no leading slash. A request path with an empty segment, a `.` or `..` segment, a NUL, a backslash
  or any control character (U+0000 to U+001F, U+007F) after decoding answers 400 `invalid_path`
  before authorization runs. A trailing `/` is not part of the path: it marks a folder (R-STOR-007,
  R-STOR-017) and `CleanPath` reports it separately.
- Further limits of the generic format: a canonical path is at most 4096 bytes and a segment at most
  255 bytes, both counted in UTF-8; longer values answer 400 `invalid_path`. The bytes must be
  valid UTF-8. Paths are compared byte for byte: case matters, and Unicode is not normalised, so
  `Report.pdf` and `report.pdf` are two items.
- A path names a file or a folder, never both: a deploy to a path below an existing file, or to a
  path that an existing folder occupies, answers 409 with the code `path_conflict` (the rule is
  enforced by the items store, [storage.md](../storage.md) R-STOR-007).
- Nothing in the path is interpreted: `.md5`, `.sha1` or `maven-metadata.xml` are ordinary names.
  Digest sidecar files are the repository's checksum policy ([storage.md](../storage.md)
  R-STOR-010, P1), not a property of the format.

Acceptance criteria:

- `curl -T a.bin https://trove.example.com/files/generic-local/a/b/c/d.bin` creates the folders
  `a`, `a/b` and `a/b/c` implicitly (R-STOR-017 lists them).
- `curl --path-as-is https://trove.example.com/files/generic-local/a/%2e%2e/x` answers 400 with the
  code `invalid_path`; so do `…/a//b`, `…/a%00b` and `…/a%5Cb`.
- `Report.pdf` and `report.pdf` deployed to one repository are two items.

### R-GENERIC-003 · The flat layout and request classification
Priority: **MVP** · Covers: PKG-GENERIC-001, PKG-GENERIC-005

- The layout of the format is named `generic` and is flat: it defines no module information. The
  `layout` member of a repository reads `"generic"` and cannot be set otherwise (R-REPO-006); a
  create that names another layout answers 422 `unsupported_layout`.
- For every `/files/` request on a generic repository the format produces the request descriptor of
  R-REPO-003 from the method and the canonical path:

| Method and path | Request kind | Package name, version |
|---|---|---|
| `GET`, `HEAD` on a path without a trailing `/` | `artifact` | none |
| `GET`, `HEAD` on a path with a trailing `/`, or on the repository root | `other` (the directory index of R-STOR-017, rendered by trove) | none |
| `PUT`, `DELETE` and every other method that changes state | `write` | none |

- A generic repository has no metadata documents, so no path is `metadata`: the rule that an
  unclassifiable path counts as `metadata` (R-REPO-003) never applies, and no cached document
  expires. Every stored file is immutable content that is served exactly as stored.
- The descriptor carries neither a package name nor a routing name, so the patterns, claims and
  pin rules that act on names never act on a generic repository, and the request counts as
  package-less for include and exclude patterns, which a generic repository evaluates on the path
  instead (R-GENERIC-007).

Acceptance criteria:

- A unit test of the format classifies `GET a/b.bin` as `artifact`, `GET a/` and `GET` of the root
  as `other`, and `PUT a/b.bin` and `DELETE a/b.bin` as `write`, each without a package name.
- `GET /api/v1/repositories/generic-local` returns `"layout":"generic"`; a create with
  `"layout":"maven-2"` returns 422 `unsupported_layout`.

### R-GENERIC-004 · Deploy any file
Priority: **MVP** · Covers: PKG-GENERIC-001, PKG-GENERIC-004

The client protocol for writing is `PUT /files/<repoKey>/<path>` with the raw content as body
([storage.md](../storage.md) R-STOR-007; request path R-ARCH-014):

```bash
curl -fsS -H "Authorization: Bearer $TROVE_TOKEN" -T installer-2.4.1.msi \
  "https://trove.example.com/files/generic-local/tools/installer/2.4.1/installer-2.4.1.msi"   # example
```

- The generic format refuses nothing on format grounds. No content check exists, so the response
  `409` "a format rule refuses the path" and the `not_a_package` refusal of R-REPO-001 never come
  from this format. What can refuse a deploy is limited to the include and exclude patterns of the
  repository (403 `not_admitted`, R-GENERIC-007), the path rules (R-GENERIC-002), permissions, the
  digest and length checks, size and quota limits, and a repository that is disabled (404
  `repository_disabled`) or being deleted (404 `repository_not_found`).
- Permissions are those of R-AUTHZ-006: `deploy` on the path creates a file that does not exist;
  an identical re-upload needs `deploy` only and answers `200`; replacing a file with different
  bytes needs `deploy` and `overwrite` and answers 403 without `overwrite`. The format does not
  declare versions immutable: whether a path may be rewritten is the permission model's decision
  alone.
- A `PUT` to a remote generic repository answers 405 with a reason naming the class; to a virtual
  one it follows the default deployment target or answers 405 `no_deploy_target` (R-ARCH-014,
  R-GENERIC-010, P1).
- Out of this page's MVP (specified elsewhere, P1): deploy by checksum ([storage.md](../storage.md)
  R-STOR-008), properties as matrix parameters or headers (R-STOR-009), multi-request upload
  sessions (R-STOR-011), and the extraction of an uploaded archive (the catalog entry ART-005 is
  not specified yet).

Acceptance criteria:

- `curl -fsS -u ci:$TROVE_TOKEN -T widget.bin https://trove.example.com/files/generic-local/tools/widget.bin`
  answers `201` with the item description of R-STOR-007; repeating it answers `200`.
- With `read` and `deploy` but not `overwrite` on `generic-local`, a second `PUT` of different bytes
  to the same path answers 403 and the first content is still served.
- A file named `package.json`, `x.tgz`, an empty file and a file of 200 MB are each accepted.

### R-GENERIC-005 · Download any file
Priority: **MVP** · Covers: PKG-GENERIC-001, PKG-GENERIC-005

The client protocol for reading is `GET` and `HEAD` on `/files/<repoKey>/<path>`, with the semantics
of [storage.md](../storage.md) R-STOR-012 (streaming, `Content-Length`, `ETag` as the content
address, `Repr-Digest`, a single byte range, `If-Range`, conditional requests) and R-STOR-013
(integrity verification), on the request path of R-ARCH-011.

- The response carries the content headers of R-ARCH-007 whatever the media type of the file: a
  generic repository is where `evil.svg` and `evil.html` are stored, and the core sets
  `Content-Disposition: attachment` and the sandbox policy before the handler runs.
- The media type is the one recorded at deploy time from `Content-Type`, or
  `application/octet-stream`; the generic format never derives a type from the content or the name.
- `GET` on a folder returns the directory index (R-STOR-017, P1) and, before it exists, 404; a
  folder is never served as an archive in the MVP.
- Download of a single entry from inside an archive, and of a folder as an archive, are not part of
  the MVP (the catalog entries ART-014 and ART-015 are not specified yet).
- The client's parallel multi-part download relies on `Range` on every generic download path, which
  R-STOR-012 guarantees on every replica; it holds for files served from a remote repository's cache
  as well (R-GENERIC-009).
- A repository that requires authentication challenges an anonymous request (R-REPO-004); a path
  outside the include and exclude patterns answers `404` as if it did not exist (R-GENERIC-007).

Acceptance criteria:

- `curl -r 0-99 -o part https://trove.example.com/files/generic-local/tools/widget.bin` answers
  `206` with 100 bytes equal to `head -c 100 widget.bin`.
- An uploaded `evil.svg` with an embedded script, opened from `/files/` in a browser, does not run
  the script; the response carries `Content-Disposition: attachment`, `nosniff`, the sandbox policy
  and `Cross-Origin-Resource-Policy: same-site` (R-ARCH-007).
- `curl -I …/files/generic-local/tools/widget.bin` answers `200` with `ETag: "<sha256 hex>"` and
  `Accept-Ranges: bytes`.

### R-GENERIC-006 · Items of a generic repository: information, listing and deletion
Priority: **MVP** · Covers: PKG-GENERIC-001

The items operations of R-API-025 act on a generic repository without a format hook:

- Item information (R-STOR-016) returns `package: null`, and `upstreamUrl: null` for the local class.
- Delete (R-STOR-018) removes a file, or a folder with its subtree, and needs `delete` on every
  item of the subtree. The format's `OnItemsRemoved` hook has nothing to update: a generic
  repository has no index.
- The listing of a folder (R-STOR-017, P1) is the only way to browse a generic repository without
  knowing the paths; entries the caller cannot read are omitted.

Acceptance criteria:

- `curl -X DELETE …/files/generic-local/tools/widget.bin` answers `204` and the next `GET` answers
  `404`.
- Deleting a folder that contains one file the caller may not delete answers 403 and removes
  nothing (R-STOR-018).

### R-GENERIC-007 · Include and exclude patterns on paths
Priority: **MVP** · Covers: PKG-GENERIC-004

A generic repository evaluates `includePatterns` and `excludePatterns` (R-REPO-005) as path
patterns against the canonical path: `?` one character except `/`, `*` within one segment, `**` any
number of segments, `\` escapes, by the matcher of R-AUTHZ-005.

- A path is admitted when it matches at least one include pattern (an empty list admits everything)
  and no exclude pattern. A folder path is admitted when any path below it could be; the folder
  marker `a/` is evaluated as the path `a`.
- A deploy to a path that is not admitted answers 403 `not_admitted` and stores nothing, for every
  caller, administrators included. A `GET`, `HEAD` or `DELETE` of such a path through `/files/`
  answers 404: stored items outside the patterns (from before a change of the patterns) stay in
  storage and remain visible to the management items API, so an administrator can remove them
  (R-REPO-005).
- Changing patterns never deletes items (R-REPO-005). Limits: at most 100 patterns per list, each at
  most 1024 characters; violations answer 422 `invalid_pattern`.

Acceptance criteria:

- `generic-local` with `includePatterns: ["releases/**"]`: `PUT …/files/generic-local/tmp/x.bin`
  answers 403 `not_admitted`; `PUT …/files/generic-local/releases/1.0/x.bin` answers 201.
- With `excludePatterns: ["**/*.pdb"]`, a deploy of `releases/1.0/x.pdb` answers 403 `not_admitted`.
- After the patterns change so that `releases/1.0/x.bin` is no longer admitted, `GET` of it answers
  404 and the management items API still returns it.

### R-GENERIC-008 · Repository settings of a generic local repository
Priority: **MVP** · Covers: PKG-GENERIC-001

Of the configuration document of R-REPO-004 a generic local repository uses:

| Member | Applies | Note |
|---|---|---|
| `key`, `class`, `format`, `description`, `adminNotes`, `disabled`, `requireAuthentication`, `hideExistence`, `includePatterns`, `excludePatterns` | yes | as on any repository |
| `layout` | read-only | `generic` (R-GENERIC-003) |
| `priorityMember` | no | answers 422 `not_applicable` when `true` (no priority members for a format without package names) |
| `local.packageOwnership`, `local.ownershipDomain`, `local.autoClaim` | no | 422 `not_applicable`: ownership and claims act on package names, and a generic item has none (R-AUTHZ-018) |
| `local.caps` | no | 422 `not_applicable`: caps count package version groups (R-REPO-007) |
| `storage` | yes | owned by [storage.md](../storage.md) |
| `remote`, `virtual` | no | 422 `not_applicable` on a local repository |

A repository created without these members reads back without them; no default is stored for a
member that does not apply.

Acceptance criteria:

- A create of a generic local repository with `"local":{"packageOwnership":"enforce"}` returns 422
  `not_applicable` at `/local/packageOwnership`.
- `GET /api/v1/repositories/generic-local` of a repository created with only key, class and format
  returns no `local`, `remote` or `virtual` member.

## Non-conformant content and other formats

### R-GENERIC-009 · Remote generic repository for any HTTP source
Priority: **P1** · Covers: PKG-GENERIC-006

A generic remote repository proxies and caches files from any HTTP(S) location, such as a vendor
download site or a mirror. A request for `/files/<repoKey>/<path>` is forwarded to
`<upstream URL>/<path>` on a cache miss; the response is stored as an item and served. All common
remote settings and rules of [repositories-and-proxy.md](../repositories-and-proxy.md) apply
(R-REPO-024 to R-REPO-047); the generic format adds:

| Member (`remote.…`) | Meaning | Default (proposed) |
|---|---|---|
| `listRemoteContent` | Let the items API browse the upstream's content, including items not cached yet | `false` |
| `propagateQueryParameters` | Pass the query parameters of the client request on to the upstream | `false` |

- Every file is `artifact` content: a cached file is never refreshed, and there are no metadata
  periods (`metadataTtlSeconds` does not apply and answers 422 `not_applicable`). The negative
  cache (R-REPO-028) applies.
- Search and query-based operations see only items already cached. A client that needs an uncached
  file requests it by URL first.
- The source-absence detection of chained remotes (R-REPO-029 "gone packages") does not apply:
  the format has no package document (`PackageDocumentPath` reports `ok = false`).
- `Range` works on cached files exactly as on local ones (R-GENERIC-005); a fill streams to its own
  client under the rules of R-REPO-041.

Acceptance criteria:

- With a stub upstream serving `/tools/x.zip`, a first `GET …/files/generic-remote/tools/x.zip`
  causes one upstream request and the second causes none.
- `PUT` to `generic-remote` answers 405.

### R-GENERIC-010 · Generic virtual repository
Priority: **P1** · Covers: PKG-GENERIC-008

A generic virtual repository puts several member repositories behind one URL
([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-017 to R-REPO-021).

- Unlike every other format, its members may be of any format: the same-format rule of
  R-REPO-017 does not apply to a generic virtual repository, and the member validation
  (`invalid_member`) accepts local and remote repositories of every format. This is the one
  exception to the format check of virtual membership.
- It keeps no metadata of its own. A request is resolved by walking the effective member list in
  order and serving the first hit (R-REPO-019); no format-specific merging, URL rewriting or
  metadata cache (R-REPO-020) is applied, because the paths of foreign members are served as the
  stored files they are.
- Deploys go to the default deployment target (R-REPO-021): a local repository that is a member.
  Without one, a `PUT` answers 405 `no_deploy_target`.

Acceptance criteria:

- A virtual `generic-all` with members `generic-local` and the npm repository `npm-local` serves
  `…/files/generic-all/<path>` for a file in either member, `generic-local` first.
- `PUT …/files/generic-all/x.bin` without a default deployment target answers 405
  `no_deploy_target`.

### R-GENERIC-011 · Non-conformant content and cross-format copy and move
Priority: **P1** · Covers: PKG-GENERIC-002, PKG-GENERIC-011

- A generic repository never rejects content on format grounds (R-GENERIC-004). Format repositories
  decide for themselves ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-001: npm
  and NuGet refuse non-package content with 422 `not_a_package`, because unindexed content in a
  format repository is invisible to clients and only costs storage). The place for such content is
  a generic repository.
- A generic local repository is the one target that accepts a copy or move of items of any format
  ([storage.md](../storage.md) R-STOR-019); every other target keeps the same-format rule. An item
  copied into a generic repository is a plain file there: no package, no index entry. The reverse
  direction, from a generic repository into a format repository, is checked against that format:
  the format's deploy rules decide, and a path that is not a package of that format is refused with
  `not_a_package`.
- The repository selector of the copy and move dialog offers local repositories whose format is
  `generic` or the item's own.

Acceptance criteria:

- Copying an npm tarball from `npm-local` to `generic-local` answers 200 and the copy shows
  `package: null`.
- Copying a text file from `generic-local` to `npm-local` answers 422 `not_a_package`.

### R-GENERIC-012 · Layout-based module identification
Priority: **P1** · Covers: PKG-GENERIC-003

A generic local repository can reference a named layout besides the flat one, for layout-based
maintenance (latest-version lookup, version cleanup) on content that follows it. The layout
decides nothing about storage or resolution; version-oriented functions work only on content that
happens to follow the referenced layout. The layouts and their `layout` setting arrive with the
families that bring them (a Maven 2 layout with the Maven family, R-REPO-006); until then the only
value is `generic` (R-GENERIC-003).

Acceptance criteria:

- With a second layout available, a repository that names it identifies modules and versions in
  its paths; a repository that keeps `generic` identifies none and version-oriented functions
  return nothing for it.

## Workflows on generic content

### R-GENERIC-013 · SHA-256 lookup for uncached items
Priority: **P2** · Covers: PKG-GENERIC-007

A generic remote repository with `remote.upstreamSha256` set retrieves the SHA-256 of an item from
the upstream when the item is not cached and a client asks for its checksum, and serves it. When the
file is downloaded and cached, the repository computes and stores its own digest and serves that
from then on. How the upstream checksum is located (a companion `.sha256` file or a response
header) is part of the setting and is specified when the requirement is scheduled.

### R-GENERIC-014 · HTTP build cache on a generic repository
Priority: **P2** · Covers: PKG-GENERIC-009

A generic local repository serves as the HTTP remote cache of a build tool that speaks a plain
`PUT`/`GET` cache protocol (for example `ac/<sha256>` and `cas/<sha256>` below the repository URL).
The repository needs no special settings. Access to such a repository is grant-controlled like any
other: anyone who can write to the cache can poison the outputs of later builds, so `deploy` and
`overwrite` are given only to the build service accounts that need them. Whether rewriting an
existing path needs `overwrite` is the permission model's decision (R-GENERIC-004); a deployment
that writes many small content-addressed files grants `overwrite` to its build account
deliberately or leaves it out, in which case an identical re-upload still succeeds with `200`.

### R-GENERIC-015 · Bulk file operations on generic content
Priority: **P2** · Covers: PKG-GENERIC-010

Scripted and command-line clients combine server capabilities: search by path pattern and
property, copy and move with flattening, delete by pattern with a dry run, property changes on many
items, and upload with removal of items not part of the upload. The server supplies the search,
copy, move, delete and property operations ([storage.md](../storage.md), search); this page adds no
generic-specific endpoint. A search that matches nothing returns an empty result, not an error.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-GENERIC-1 · The format is `generic`, flat paths, and the descriptor lists only implemented classes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The catalog says that the system's format sets list generic for every class, while M1 builds the
local class only (Q-ROAD-1).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The descriptor lists the implemented classes | `classes` is `["local"]` in M1 and grows with the P1 classes | One source of truth (R-ARCH-008); a create of an unimplemented class fails with a precise code; the UI offers nothing that does not work | A second change when the P1 classes land |
| (b) The descriptor lists all three classes and the create refuses the others | The catalog's matrix is visible from the first day | Matches the catalog | A second list of what works; the create form offers choices that fail |

**Choice:** (a).

**Rationale:** R-ARCH-008 forbids a second list of classes per format. The development agent would
revisit this if the format selector in the UI needed to show unimplemented classes greyed out.

**Applied in the spec:** R-GENERIC-001.

### Q-GENERIC-2 · Path limits of the generic format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-005 fixes which segments `CleanPath` refuses, and leaves the maximum length open for
`/files/`. R-ARCH-010 fixes 4 KiB for format routes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 4096 bytes per path, 255 bytes per segment, valid UTF-8 | The kit's 4 KiB; the file-system segment limit | Matches the route limit of the kit; segments stay usable when a repository is later exported to a file system; one rule for every format | Rejects the rare path above 255 bytes per segment |
| (b) No segment limit, 4096 bytes in total | Only the total is bounded | Fewer refusals | A segment of 4000 bytes cannot be extracted on a file system |
| (c) Accept any bytes | Do not validate UTF-8 | Handles any client | Paths are shown in the UI, in JSON and in listings; invalid UTF-8 breaks them |

**Choice:** (a).

**Rationale:** The limits are generous for installers and build outputs and protect the listing and
JSON representations. The development agent would revisit this if a real client needs longer
segments.

**Applied in the spec:** R-GENERIC-002; the limits are enforced by `repository.CleanPath`.

### Q-GENERIC-3 · A path is a file or a folder, not both

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse with 409 `path_conflict` | A deploy to `a/b` is refused when `a` is a file, and to `a` when `a/b` exists | Listings, the directory index and downloads stay unambiguous | A client that wants both has to choose names |
| (b) Allow both | A file `a` and a folder `a/` coexist | Object-store semantics | A folder `GET` and a file `GET` need different URLs (`a` and `a/`), which the browser and some proxies strip |

**Choice:** (a).

**Rationale:** The items table keys a live path once (`items_live_path`), so (b) cannot be stored.
The development agent would revisit this if object-store style prefixes became a requirement.

**Applied in the spec:** R-GENERIC-002.

### Q-GENERIC-4 · Paths outside the include and exclude patterns answer 404 on read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 404 on `GET`, `HEAD`, `DELETE` of a path outside the patterns; 403 `not_admitted` on a deploy | Stored items outside the patterns are invisible to `/files/` | The patterns are routing rules; a repository behaves as if the item did not exist, as R-REPO-005 requires for downloads | An item stored before a pattern change cannot be fetched through `/files/` |
| (b) Serve reads, refuse only writes | Patterns only gate writes | Old content stays reachable | A remote repository would also have to fetch what it does not admit; patterns stop being routing rules |

**Choice:** (a).

**Rationale:** R-REPO-005 applies patterns to the resolution of downloads and shows administrators
the items outside them through the management API. The development agent would revisit this if
operators ask for read-only legacy paths.

**Applied in the spec:** R-GENERIC-007.

### Q-GENERIC-5 · A generic virtual repository accepts members of every format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any format | Local and remote members of every format | The catalog's behaviour: one URL over mixed content | A format-specific route does not exist on the virtual repository, only `/files/` |
| (b) Generic members only | Same-format rule | Uniform rule | Loses the catalog's mixed-content case |

**Choice:** (a).

**Rationale:** the virtual generic repository serves files only, so no format merging is involved.
The development agent would revisit this if mixed members made the effective member list
hard to explain in the UI.

**Applied in the spec:** R-GENERIC-010.

## What this does not cover

### H-131 · Generic content is served as stored, to whoever can read it

Component: raw file serving of generic repositories (R-GENERIC-005). Subject: the confidentiality
and the integrity of stored content. Live with the MVP. A generic repository neither scans nor
validates what it stores; content headers (R-ARCH-007) keep a browser from running uploaded
content as part of trove's origin, but a downloaded installer or archive is exactly as trustworthy
as its uploader. Grant `deploy` and `overwrite` only to the accounts that publish, and use the
digests of the item information to verify downloads out of band. What makes it real: a write grant
on a repository that other builds consume.
