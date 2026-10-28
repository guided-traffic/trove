# NuGet

Status: specification — not implemented

This page specifies how trove serves NuGet packages: the NuGet v3 server API for local, remote
and virtual repositories under `/api/nuget/<repoKey>/`, package ingestion and `.nuspec` parsing,
version normalization and SemVer 2.0.0 handling, the NuGet additions to the storage schema, push,
unlist, relist and delete, symbol packages and the symbol server, proxying nuget.org with URL
rewriting, merging of members in virtual repositories, the NuGet side of client authentication and
authorization, client compatibility and the acceptance tests. NuGet v2 (OData) is deferred
(INV-14). Mechanisms shared by all formats are owned by other pages and only refined here:
repository model, virtual resolution and the pull-through proxy by
[repositories-and-proxy.md](../repositories-and-proxy.md); items, blobs, uploads and the trash can
by [storage.md](../storage.md); credentials by
[identity-and-authentication.md](../identity-and-authentication.md); permissions and package
ownership by [authorization.md](../authorization.md); audit and metrics by
[operations-and-observability.md](../operations-and-observability.md).

Covers: this page draws on [formats/nuget.md](../../features/formats/nuget.md) (`PKG-NUGET-*`),
[remote-repositories.md](../../features/remote-repositories.md) (`RMT-*`) and
[repositories.md](../../features/repositories.md) (`REPO-*`), and for single entries on
[ui-inventory.md](../../features/ui-inventory.md) (UI-025),
[formats/README.md](../../features/formats/README.md) (FMT-011, FMT-012),
[api-content-operations.md](../../features/api-content-operations.md) (API-040),
[identity-and-access.md](../../features/identity-and-access.md) (IAM-005, IAM-022 to IAM-024),
[access-tokens.md](../../features/access-tokens.md) (TOK-034) and
[beyond-baseline.md](../../features/beyond-baseline.md) (BEYOND-028).

External reference: the NuGet server API as documented by Microsoft
([API overview](https://learn.microsoft.com/en-us/nuget/api/overview),
[implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)) is
normative wherever this page does not say otherwise.

## Conventions on this page

| Placeholder | Meaning |
|---|---|
| `<base>` | trove's configured public base URL ([architecture.md](../architecture.md)); `https://trove.example.com` in examples (# example) |
| `<feed>` | `<base>/api/nuget/<repoKey>/v3`, the v3 root of one repository |
| `{lower_id}` | package ID lowercased (R-NUGET-003) |
| `{lower_version}` | normalized version, lowercased, without build metadata (R-NUGET-003) |
| `$TOKEN` | a trove token (`trv_pat_…`, `trv_sat_…` or `trv_cit_…`, INV-8) |

Repository keys used in examples (# example): `nuget-local` (local), `nuget-org` (remote of
nuget.org), `nuget-all` (virtual over both, deploy target `nuget-local`).

## Requirements

### R-NUGET-001 · NuGet repositories and endpoint layout
Priority: **MVP** · Covers: PKG-NUGET-001, PKG-NUGET-002, PKG-NUGET-003, REPO-005

trove MUST offer the format `nuget` for all three repository classes of INV-5. Every NuGet
repository speaks the NuGet v3 protocol; there is no per-repository protocol switch.

Format registration (R-REPO-003 in [repositories-and-proxy.md](../repositories-and-proxy.md)):
name `nuget`; classes `local`, `remote`, `virtual`; protocol base path `/api/nuget/{key}/`;
default upstream URL `https://api.nuget.org/v3/index.json`; priority members and package pinning
supported; package name = package ID, canonicalized by ASCII lowercasing (R-NUGET-003); request
descriptors as in R-NUGET-015.

Endpoint layout. All protocol resources of one repository MUST live below `<feed>/`, so that
NuGet 6.7 and later, which pre-authenticate only URLs in or below the directory of an
authenticated URL, send credentials without a 401 round trip per resource
([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)).

| Resource | Path | Methods | Local | Remote | Virtual | Requirement |
|---|---|---|---|---|---|---|
| Service index | `<feed>/index.json`, also `<feed>/` and `<feed>` | GET, HEAD | yes | yes | yes | R-NUGET-002 |
| Package content (flat container) | `<feed>/flatcontainer/` | GET, HEAD | yes | yes | yes | R-NUGET-008 |
| Package metadata, SemVer 1 hive, uncompressed | `<feed>/registration/` | GET, HEAD | yes | yes | yes | R-NUGET-009 |
| Package metadata, SemVer 1 hive, gzip | `<feed>/registration-gz/` | GET, HEAD | yes | yes | yes | R-NUGET-009 |
| Package metadata, SemVer 2 hive, gzip | `<feed>/registration-semver2/` | GET, HEAD | yes | yes | yes | R-NUGET-009 |
| Search | `<feed>/search` | GET, HEAD | yes | yes | yes | R-NUGET-010 |
| Autocomplete (P1) | `<feed>/autocomplete` | GET, HEAD | yes | yes | yes | R-NUGET-011 |
| Package publish | `<feed>/package`, `<feed>/package/{id}/{version}` | PUT; DELETE, POST | yes | no | with deploy target | R-NUGET-006, R-NUGET-007 |
| Symbol package publish | `<feed>/symbolpackage` | PUT | yes | no | with deploy target | R-NUGET-020 |
| Symbol server (P1) | `<feed>/symbols/` | GET, HEAD | yes | yes | yes | R-NUGET-021, R-NUGET-022 |
| Vulnerability information (P1) | `<feed>/vulnerabilities/` | GET, HEAD | no | yes | yes | R-NUGET-023 |

- The service index MUST be answered with an identical body at all three spellings, without a
  redirect: clients are configured both with and without the trailing `index.json`.
- Any other path under `/api/nuget/<repoKey>/` MUST answer 404 with code `nuget_v2_unsupported`
  and a message naming the repository's v3 service index URL. This covers every NuGet v2 request
  shape (`$metadata`, `FindPackagesById()`, `Packages()`, `Search()`, `api/v2/…`), which a client
  sends when its source URL does not end in `.json` and lacks `protocolVersion="3"`.
- An unknown repository key answers 404. A key of a repository with another format answers 404
  with a message saying that the repository is not a NuGet repository.
- Every URL trove puts into a NuGet document MUST be absolute, built from `<base>`
  (R-REPO-043), and MUST point at the repository the request addressed. A virtual repository never
  exposes member URLs, a remote repository never exposes upstream URLs for resources clients fetch
  next (R-NUGET-015).
- Raw file access (`/files/<repoKey>/<path>`, INV-6) uses the storage layout of R-NUGET-005.

Acceptance criteria:

- `curl -sf -u x:$TOKEN <feed>/index.json`, `<feed>/` and `<feed>` return byte-identical bodies
  with status 200.
- `curl -i "<base>/api/nuget/nuget-local/FindPackagesById()?id='Acme.Widgets'"` returns 404 whose
  body contains `<base>/api/nuget/nuget-local/v3/index.json`.
- Every `@id` in the service index of `nuget-local`, `nuget-org` and `nuget-all` starts with that
  repository's own `<feed>/`.
- `curl -s <base>/api/v1/formats` lists `nuget` with the default upstream URL above.

### R-NUGET-002 · Service index and HTTP conventions
Priority: **MVP** · Covers: PKG-NUGET-041, PKG-NUGET-043, PKG-NUGET-044

The [service index](https://learn.microsoft.com/en-us/nuget/api/service-index) MUST carry
`"version": "3.0.0"` and one entry per resource type. Because a client only looks up the `@type`
values it knows, each resource MUST be listed under every alias in the table.

| Resource | `@type` values listed | Path |
|---|---|---|
| Package content | `PackageBaseAddress/3.0.0` | `<feed>/flatcontainer/` |
| Package publish | `PackagePublish/2.0.0` | `<feed>/package` |
| Symbol package publish | `SymbolPackagePublish/4.9.0` | `<feed>/symbolpackage` |
| Metadata, SemVer 1, uncompressed | `RegistrationsBaseUrl`, `RegistrationsBaseUrl/3.0.0-beta`, `RegistrationsBaseUrl/3.0.0-rc` | `<feed>/registration/` |
| Metadata, SemVer 1, gzip | `RegistrationsBaseUrl/3.4.0` | `<feed>/registration-gz/` |
| Metadata, SemVer 2, gzip | `RegistrationsBaseUrl/3.6.0` | `<feed>/registration-semver2/` |
| Search | `SearchQueryService`, `SearchQueryService/3.0.0-beta`, `SearchQueryService/3.0.0-rc`, `SearchQueryService/3.5.0` | `<feed>/search` |
| Autocomplete (P1) | `SearchAutocompleteService`, `…/3.0.0-beta`, `…/3.0.0-rc`, `…/3.5.0` | `<feed>/autocomplete` |
| Vulnerability information (P1) | `VulnerabilityInfo/6.7.0` | `<feed>/vulnerabilities/index.json` |
| README template (P1) | `ReadmeUriTemplate/6.13.0` | `<feed>/flatcontainer/{lower_id}/{lower_version}/readme` |

- Publish and symbol publish entries appear only on local repositories and on virtual
  repositories with a deploy target (R-NUGET-019). Remote repositories never list them.
- Autocomplete, vulnerability information and the README template appear once their P1
  requirements are implemented; the vulnerability entry only when R-NUGET-023 has data to offer.
- Resources the protocol defines but trove does not implement (catalog, repository signatures,
  package details, owner details, report abuse) MUST NOT be listed.

HTTP conventions for every NuGet endpoint:

- GET and HEAD are supported on every read resource; HEAD returns the GET headers without a body.
- JSON responses use `Content-Type: application/json; charset=utf-8`. Unknown query parameters
  are ignored.
- The service index, flat container and registration documents carry a strong `ETag`;
  `If-None-Match` with a matching value answers 304. For documents rendered from local rows the
  ETag derives from the package's revision (R-NUGET-005), the hive and `<base>`; for remote
  documents from the stored upstream document's digest and the serving address (R-REPO-043);
  through a virtual repository it also covers the fingerprint of the contributing members
  (R-REPO-020). Package files follow [storage.md](../storage.md) (R-STOR-012). Search and
  autocomplete responses carry no `ETag`.
- Status codes follow the protocol: 200, 201, 204, 304, 400 invalid parameters or package, 401
  missing or invalid credentials, 403 or 404 per [authorization.md](../authorization.md)
  (R-AUTHZ-016), 405 operation not offered by this repository class, 409 conflict, 413 too large,
  502 or 503 for upstream and storage failures as [repositories-and-proxy.md](../repositories-and-proxy.md)
  and [storage.md](../storage.md) define them.
- Error bodies on NuGet protocol endpoints are one line of `text/plain; charset=utf-8` in the form
  `<code>: <message>`, using the codes of the owning pages (for example `credentials_required`,
  `invalid_token`, `deny.ownership`, `no_deploy_target`). NuGet clients print status and reason
  phrase; the line serves people using `curl` and the logs. `/files/` and `/api/v1` keep their own
  error format.
- The request headers `X-NuGet-Protocol-Version` and `X-NuGet-Client-Version` are ignored.
  `X-NuGet-Session-Id` is recorded in the request log so the requests of one restore can be
  correlated.

<details>
<summary>Service index of a local repository (sketch)</summary>

```json
{
  "version": "3.0.0",
  "resources": [
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/flatcontainer/", "@type": "PackageBaseAddress/3.0.0" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/package", "@type": "PackagePublish/2.0.0" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/symbolpackage", "@type": "SymbolPackagePublish/4.9.0" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration/", "@type": "RegistrationsBaseUrl" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration/", "@type": "RegistrationsBaseUrl/3.0.0-beta" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration/", "@type": "RegistrationsBaseUrl/3.0.0-rc" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration-gz/", "@type": "RegistrationsBaseUrl/3.4.0" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/", "@type": "RegistrationsBaseUrl/3.6.0" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/search", "@type": "SearchQueryService" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/search", "@type": "SearchQueryService/3.0.0-beta" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/search", "@type": "SearchQueryService/3.0.0-rc" },
    { "@id": "https://trove.example.com/api/nuget/nuget-local/v3/search", "@type": "SearchQueryService/3.5.0" }
  ]
}
```

</details>

API: `GET <feed>/index.json` → 200; permission `read` at repository level (R-NUGET-013). The
service index is the first URL a client fetches, so it is where an unauthenticated client must be
challenged (R-NUGET-012).

Acceptance criteria:

- The service index of `nuget-local` lists exactly the types of the table that are implemented;
  `nuget-org` lists no `PackagePublish/2.0.0` or `SymbolPackagePublish/4.9.0`; a virtual
  repository without deploy target lists neither.
- `curl -sI -H "If-None-Match: <etag>" <feed>/registration-semver2/acme.widgets/index.json`
  answers 304.
- `curl -I` on every read resource returns the same `Content-Type`, `ETag` and `Content-Length`
  as the corresponding GET.

### R-NUGET-003 · Package IDs, versions and normalization
Priority: **MVP** · Covers: PKG-NUGET-008, PKG-NUGET-009, PKG-NUGET-011, PKG-NUGET-042, REPO-030, REPO-031

Package IDs:

- A pushed ID MUST match `^[A-Za-z0-9_]+([.-][A-Za-z0-9_]+)*$` and be at most 100 characters
  (NuGet's ID rule restricted to ASCII; Q-NUGET-3). Violations answer 400.
- IDs are case-insensitive. The canonical package name is `lower_id`, the ASCII-lowercased ID;
  [authorization.md](../authorization.md) compares name patterns against it. The package's display
  name is the casing of its first stored version; every version keeps the casing of its own
  `.nuspec` in its catalog entry (Q-NUGET-5).

Versions. NuGet versions are `Major[.Minor[.Patch[.Revision]]][-Prerelease][+Metadata]`
([package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning)).
trove keeps three forms per version:

| Form | Rule | Example for input `01.2.0.0-RC.1+sha.9f2c` | Used in |
|---|---|---|---|
| Original | verbatim from the `.nuspec` | `01.2.0.0-RC.1+sha.9f2c` | stored only |
| Full normalized | leading zeros removed from numeric parts, at least three numeric parts, fourth part only when non-zero, pre-release label in original case, build metadata kept | `1.2.0-RC.1+sha.9f2c` | registration catalog entries, search `version` and `versions[].version` |
| URL form | full normalized, lowercased, build metadata removed | `1.2.0-rc.1` | URLs, flat container version list, registration page bounds, duplicate detection, canonical paths |

- Every trove NuGet repository is normalized; there is no non-normalized mode and no creation
  option for one. Consequences: the flat container is always offered; the compatibility rules
  between normalized and non-normalized repositories (copy, move, virtual membership, upstream
  pairing) hold by construction; a normalization migration is not needed (see
  [Deferred](#deferred)).
- Two versions are equal when their URL forms are equal. `1`, `1.0`, `1.0.0` and `1.0.0.0` are
  one version; `1.0.0-beta` and `1.0.0-Beta` are one version; `3.0.0+a1` and `3.0.0+b2` are one
  version.
- A version string that NuGet cannot parse answers 400 on every ingestion path. A valid version
  is stored normalized; build metadata is never a reason to reject.
- Ordering follows SemVer 2.0.0 precedence with NuGet's extensions: an optional fourth numeric
  part, and pre-release identifiers compared case-insensitively. A release sorts above its
  pre-releases; numeric identifiers compare numerically and below alphanumeric ones.
- Each version stores a byte-comparable `sort_key` so PostgreSQL can order versions and pick the
  latest: four 4-byte big-endian numeric parts; then `0xFF` for a release, or for a pre-release per
  identifier `0x01` + one length byte + decimal digits (numeric) or `0x02` + lowercased ASCII +
  `0x00` (alphanumeric), terminated by `0x00`.
- Requests that carry an ID or version in the path (flat container, registration, publish) MUST
  accept any casing and any non-normalized spelling of the version and resolve it through the URL
  form; responses equal those for the canonical URL (no redirect).
- Dependency version ranges are parsed with NuGet's range grammar (`1.0`, `[1.0,2.0)`, `(,1.0]`,
  `[1.0]` and so on); an unparseable range answers 400.
- Conformance: the version parser, normalizer, comparer and range parser MUST agree with the
  `NuGet.Versioning` library on a committed golden-vector file (accept or reject, URL form, full
  normalized form, ordering of every pair). The vectors are generated by a small .NET 10 program
  referencing a pinned `NuGet.Versioning` version; CI regenerates them and fails on drift.

Acceptance criteria:

| Pushed version | Stored URL form | Second push of | Result of second push |
|---|---|---|---|
| `01.02.03.0` | `1.2.3` | `1.2.3+other` | 409 |
| `2.5` | `2.5.0` | `2.5.0.0` | 409 |
| `2.5.1.3` | `2.5.1.3` | `2.5.1.3+x` | 409 |
| `1.0.0-Beta.2` | `1.0.0-beta.2` | `1.0.0-BETA.2` | 409 |
| `1.0.0-beta` | `1.0.0-beta` | `1.0.0-beta.1` | 201 |

- The flat container list of a package holding `1.0.0`, `1.0.0-rc.10`, `1.0.0-rc.2`,
  `1.0.0-alpha`, `1.0.0.1` and `0.9.0` is
  `["0.9.0","1.0.0-alpha","1.0.0-rc.2","1.0.0-rc.10","1.0.0","1.0.0.1"]`.
- `curl <feed>/flatcontainer/ACME.Widgets/1.2.3.0/acme.widgets.1.2.3.nupkg` returns the same bytes
  as the lowercase canonical URL.
- The golden-vector test passes in CI.

### R-NUGET-004 · SemVer 2.0.0 classification and visibility
Priority: **MVP** · Covers: PKG-NUGET-014, PKG-NUGET-042

- A version is SemVer 2.0.0-specific (`is_semver2`) when its original pre-release label contains a
  dot, when it carries build metadata, or when any dependency range of the package has a bound
  that is itself SemVer 2.0.0-specific (the rule nuget.org applies,
  [package versioning](https://learn.microsoft.com/en-us/nuget/concepts/package-versioning)). The
  flag is computed at ingestion and stored.
- Visibility:

| Resource | SemVer 2.0.0-specific versions |
|---|---|
| Flat container version list and downloads | always visible |
| `registration/` and `registration-gz/` hives | excluded |
| `registration-semver2/` hive | included |
| Search and autocomplete without `semVerLevel` or with a value below 2.0.0 | excluded |
| Search and autocomplete with `semVerLevel` 2.0.0 or higher | included |

- `semVerLevel` is parsed as a version; an absent or unparseable value means 1.0.0.
- "Latest version" (search result `version`, merged views) is computed only over the versions
  visible under the request's filter, so a package whose newest version is SemVer 2.0.0-specific
  shows its newest SemVer 1 version to a legacy client.
- A package all of whose versions are excluded is absent: the SemVer 1 hives answer 404 for its
  registration index, search does not return it.
- trove offers no switch that turns the filter off. The filter exists for NuGet clients older
  than 4.3.0, which cannot parse SemVer 2.0.0 versions; serving them such versions breaks them.

Acceptance criteria (package `Acme.Widgets` with `1.0.0` and `1.1.0-ci.5+sha.abc`):

- `curl <feed>/registration/acme.widgets/index.json` lists only `1.0.0`;
  `curl <feed>/registration-semver2/acme.widgets/index.json` lists both, the second with
  `"version": "1.1.0-ci.5+sha.abc"`.
- `curl "<feed>/search?q=acme&prerelease=true"` returns `"version": "1.0.0"`; with
  `&semVerLevel=2.0.0` it returns `"version": "1.1.0-ci.5+sha.abc"`.
- `curl <feed>/flatcontainer/acme.widgets/index.json` returns `{"versions":["1.0.0","1.1.0-ci.5"]}`.
- `dotnet add package Acme.Widgets --prerelease` (dotnet 10 SDK) resolves version `1.1.0-ci.5`;
  without `--prerelease` it resolves `1.0.0`.

### R-NUGET-005 · Package ingestion, `.nuspec` parsing and storage layout
Priority: **MVP** · Covers: PKG-NUGET-005, PKG-NUGET-006, PKG-NUGET-021, FMT-011

Every `.nupkg` that enters a NuGet repository goes through the upload pipeline of
[storage.md](../storage.md) (R-STOR-004, R-STOR-005: admission with the repository's maximum upload
size, spool, one-pass SHA-256, SHA-1, MD5 and SHA-512, commit). That covers protocol push
(R-NUGET-006), raw deploy, upload through the management API and UI, copy and move, and remote
cache fills. The NuGet plug-in adds these steps between spooling and commit:

1. Open the archive as ZIP within the archive limits below (400 on violation). Entries are read
   through the central directory only. An archive with an encrypted entry, a split or multi-disk
   archive, or a central-directory name that differs from the entry's local header name is
   rejected (400).
2. Normalize entry names (`\` becomes `/`). Reject the archive (400) if a name is absolute or
   contains a `..` segment, a drive letter or NUL, or if two entries share a name compared
   case-insensitively. Then locate exactly one entry at the archive root whose name ends in
   `.nuspec`, compared case-insensitively (400 if none or several). Without these rules, two
   spellings of the `.nuspec`, or a duplicated name that one ZIP reader resolves to the first entry
   and another to the last, would let the identity trove authorizes differ from the identity a
   client installs.
3. Parse the `.nuspec` as XML: UTF-8 or UTF-16; a document type declaration is rejected (400);
   no entity expansion; elements matched by local name in any `nuspec` namespace version; at most
   `nuget.archive.max_nuspec_bytes`.
4. Validate `id` and `version` (R-NUGET-003) and every dependency range. On the package pipeline a
   package whose package types include `SymbolsPackage` is rejected with 400 and the message that
   symbol packages go to the symbol publish resource (R-NUGET-020).
5. Authorize with the declared ID as coordinate (R-AUTHZ-005 in
   [authorization.md](../authorization.md): the name the content declares is the name that is
   authorized and indexed), including the ownership check (R-AUTHZ-018). A caller holding no
   `deploy` grant on the repository at all is refused before the body is read, so unauthorized
   callers cannot fill the spool.
6. In the commit transaction (R-STOR-005): create the `packages` row if absent and the
   `package_versions` row (its unique index on the URL form makes a duplicate answer 409); create
   the item of the `.nupkg` as lead item and the derived items with `generated = true`; insert
   the NuGet side rows below; bump the package revision; publish the change notification of
   R-REPO-020 so merged caches drop.

- The `.nupkg` is stored byte for byte as received; trove never rewrites a package, because
  package signatures and lock-file hashes would fail on the client
  ([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)). A
  `.signature.p7s` entry is kept and not verified (H-36).
- Metadata is complete when the ingestion request returns: a client that pushes and immediately
  restores or searches sees the new version, on any replica (R-ARCH-011 in
  [architecture.md](../architecture.md)). There is no asynchronous metadata calculation for NuGet;
  every protocol document is rendered from the rows below, and `nuget_package_state.revision` is
  the package's change sequence that keys R-ARCH-012's rendition cache.
- Raw deploy (R-STOR-007): `PUT /files/<repoKey>/{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg`
  runs this pipeline; a declared ID or version that does not match the path answers 400
  (R-AUTHZ-005). A `.snupkg` at its canonical path runs R-NUGET-020. Every other path or file type
  answers 409 with a reason (R-STOR-007), including the derived files, which only the plug-in
  writes.
- Upload through the management API without a target path (the UI deploy dialog,
  [api-and-ui.md](../api-and-ui.md)): the plug-in derives the canonical path from the `.nuspec`.
- Copy and move (R-STOR-019, P1): the plug-in hooks copy or move the NuGet side rows in the same
  transaction; a version that already exists in the target answers 409. Sources may be NuGet
  local repositories and remote caches.
- Raw delete (R-STOR-018) of a `.nupkg` removes the whole version: the `OnItemsRemoved` hook removes
  the derived items, the symbol package and the side rows in the same transaction; with the trash
  can enabled (R-STOR-022) the version is trashed and restored as a unit. A raw delete of a derived
  item alone answers 409.

Metadata extracted from the `.nuspec` into `nuget_version.metadata`:

| Key | Source element or attribute | Used by |
|---|---|---|
| `title`, `summary`, `description`, `releaseNotes`, `copyright`, `language` | same-named elements | registration, search |
| `authors` | `authors` (kept as one string) | registration, search |
| `tags` | `tags`, split on whitespace | registration, search |
| `projectUrl`, `iconUrl`, `licenseUrl` | same-named elements | registration, search |
| `license` | `license` element: `type` (`expression` or `file`), `version`, value | registration (`licenseExpression`), R-NUGET-024 |
| `icon`, `readme` | paths of embedded files | R-NUGET-024 |
| `requireLicenseAcceptance`, `developmentDependency`, `serviceable` | same-named elements | registration |
| `minClientVersion` | attribute of `metadata` | registration |
| `repository` | `repository` element: `type`, `url`, `branch`, `commit` | UI |
| `dependencyGroups` | `dependencies/group` with `targetFramework` (verbatim) and each `dependency` `id` and `version` (verbatim range); a flat `dependencies` list becomes one group without `targetFramework` | registration |
| `packageTypes` | `packageTypes/packageType` `name` and `version`; none declared means `Dependency` | search filter, symbol checks |

Storage layout. Canonical paths inside a NuGet repository, as seen through `/files/<repoKey>/`;
the bytes live once in the blob store under their content address (INV-4).

| Canonical path | Item | Written by |
|---|---|---|
| `{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg` | lead item: the package as received | ingestion; remote cache fill |
| `{lower_id}/{lower_version}/{lower_id}.nuspec` | generated: the `.nuspec` bytes from inside the package | ingestion |
| `{lower_id}/{lower_version}/{lower_id}.{lower_version}.snupkg` | the symbol package | R-NUGET-020 |
| `{lower_id}/{lower_version}/.symbols/<entry path>` | generated: PDB files from the symbol package (P1) | R-NUGET-021 |
| `{lower_id}/{lower_version}/.embedded/icon`, `…/license`, `…/readme` | generated: embedded files (P1) | R-NUGET-024 |

Because every path starts with `{lower_id}/`, an include or exclude pattern such as `acme.*/**`
covers a package's files and metadata alike (R-REPO-005).

Data model. `repositories`, `packages`, `package_versions` and `items` are the core tables of
R-STOR-003 in [storage.md](../storage.md). NuGet fills them as follows and adds side tables:

| Core column | NuGet value |
|---|---|
| `packages.name` | `lower_id` |
| `packages.display_name` | ID casing of the first stored version |
| `package_versions.version` | URL form |
| `package_versions.display_version` | full normalized version |
| `package_versions.lead_item_id` | the `.nupkg` item |

```sql
CREATE TABLE nuget_package_state (
    package_id    bigint PRIMARY KEY REFERENCES packages (id) ON DELETE CASCADE,
    revision      bigint NOT NULL DEFAULT 1        -- bumped on every change; feeds ETags
);

CREATE TABLE nuget_version (
    version_id       bigint      PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    repository_id    bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE, -- denormalized for search
    lower_id         text        NOT NULL,         -- denormalized packages.name
    declared_id      text        NOT NULL,         -- ID casing of this version's .nuspec
    original_version text        NOT NULL,         -- verbatim from the .nuspec
    sort_key         bytea       NOT NULL,         -- R-NUGET-003
    is_prerelease    boolean     NOT NULL,
    is_semver2       boolean     NOT NULL,         -- R-NUGET-004
    listed           boolean     NOT NULL DEFAULT true,
    published_at     timestamptz NOT NULL,
    package_types    text[]      NOT NULL DEFAULT '{dependency}',  -- lowercased names
    metadata         jsonb       NOT NULL,         -- table above
    search_doc       tsvector    NOT NULL,         -- ID tokens, title, tags, authors, summary, description
    origin           text        NOT NULL CHECK (origin IN ('push', 'upload', 'copy', 'cache'))
);
CREATE INDEX nuget_version_latest ON nuget_version (repository_id, lower_id, sort_key DESC) WHERE listed;
CREATE INDEX nuget_version_search ON nuget_version USING gin (search_doc);
CREATE INDEX nuget_version_id_trgm ON nuget_version USING gin (lower_id gin_trgm_ops);
```

- Versions whose `package_versions.trashed_at` is set are invisible to every NuGet document.
- Size, digests and download counts come from `items`, `blobs` and `item_download_stats` of the
  lead item; NuGet stores no copies.
- Remote repositories write these rows with `origin = 'cache'` for the packages they cache
  (R-NUGET-015); virtual repositories have none.

Configuration (system file, INV-15):

```yaml
nuget:
  archive:
    max_nuspec_bytes: 1MiB          # default (proposed)
    max_entries: 65535              # default (proposed)
    max_uncompressed_bytes: 4GiB    # default (proposed)
    max_compression_ratio: 100      # default (proposed); per entry read into memory, at most R-ARCH-028's
```

`max_entries` and `max_uncompressed_bytes` bound what the central directory may declare and what a
streaming pass may decompress without keeping content. Entries read into memory (the `.nuspec`,
the embedded files of R-NUGET-024) stay within the limits of
[architecture.md](../architecture.md) R-ARCH-028, which a format can only tighten.

Acceptance criteria:

- `dotnet nuget push` followed within one second by `dotnet restore --no-http-cache` of a project
  that references the pushed version succeeds against `nuget-local`.
- The `.nupkg` downloaded from the flat container has the same SHA-512 as the pushed file
  (`sha512sum` comparison).
- Fixture packages answer 400: no `.nuspec`; two `.nuspec` entries at the root; a `.nuspec` with
  `<!DOCTYPE>`; ID `Acme..Widgets`; version `1.0.0-`; dependency range `(1.0)`; package type
  `SymbolsPackage`. A ZIP whose entry expands beyond the ratio limit answers 400 without
  exhausting memory or disk.
- Fixture packages answer 400 as well: root entries `Acme.Widgets.nuspec` and
  `acme.widgets.NUSPEC`; one `.nuspec` name present twice with different content; an entry
  `..\evil.dll`; a central-directory name that differs from its local header name.
- `curl -T Acme.Widgets.1.2.3.nupkg -u x:$TOKEN <base>/files/nuget-local/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg`
  answers 201 and the version appears in the flat container; the same file PUT to
  `…/acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg` answers 400; `curl -T notes.txt
  <base>/files/nuget-local/notes.txt` answers 409.

### R-NUGET-006 · Push through the package publish resource
Priority: **MVP** · Covers: PKG-NUGET-018, PKG-NUGET-020, PKG-NUGET-049

API ([package publish](https://learn.microsoft.com/en-us/nuget/api/package-publish-resource)):

| Request | Body | Responses | Permission |
|---|---|---|---|
| `PUT <feed>/package` | `multipart/form-data`; the first part is the raw `.nupkg`; further parts, part names and part headers are ignored | 201 pushed (empty body); 400 invalid package; 401; 403 (including `deny.ownership`); 409 version exists after normalization; 413 too large | `deploy` on the ID, ownership (R-AUTHZ-007, R-AUTHZ-018) |

- The request runs the ingestion pipeline of R-NUGET-005. A body that is not
  `multipart/form-data` MAY be accepted as the raw `.nupkg`, for scripted uploads with `curl`.
- This is an API-key endpoint (R-NUGET-012).
- Versions are immutable: a push whose URL form already exists in the repository answers 409,
  whatever its content and whatever permissions the caller holds (Q-NUGET-6). The NuGet clients'
  skip-duplicate option (`--skip-duplicate`, `-SkipDuplicate`) relies on exactly this status.
- Two concurrent pushes of the same version, on the same or different replicas, end with exactly
  one 201 and 409 for the others (unique index in the commit transaction).
- A push to a remote repository answers 405. A push to a virtual repository is R-NUGET-019.

Acceptance criteria:

- `dotnet nuget push out/Acme.Widgets.1.2.3.nupkg --source trove-local --api-key "$TOKEN"` (dotnet
  10 SDK) succeeds; the same command again fails with 409; with `--skip-duplicate` it exits 0.
- `nuget push Acme.Widgets.1.2.4.nupkg -Source trove-local -ApiKey %TOKEN%` (nuget.exe) succeeds.
- Ten parallel `curl -X PUT -F package=@Acme.Widgets.2.0.0.nupkg` requests spread across two
  replicas yield one 201 and nine 409.
- `curl -X PUT … <base>/api/nuget/nuget-org/v3/package` answers 405.

### R-NUGET-007 · Unlist, relist and delete
Priority: **MVP** · Covers: PKG-NUGET-022, PKG-NUGET-049

API (both are API-key endpoints, R-NUGET-012):

| Request | Effect | Responses | Permission |
|---|---|---|---|
| `DELETE <feed>/package/{id}/{version}` | unlist (default) or hard delete, per `deleteMode` | 204; 401; 403; 404 no such version | `delete` on the ID, ownership (R-AUTHZ-007) |
| `POST <feed>/package/{id}/{version}` | relist; succeeds also when already listed | 200; 401; 403; 404 | `deploy` on the ID, ownership (R-AUTHZ-007: relisting changes what version selection picks) |

- `{id}` and `{version}` are resolved case-insensitively through the URL form (R-NUGET-003).
- Unlisted versions: absent from search and autocomplete; present in the flat container version
  list and downloadable; `"listed": false` in registration entries. `published` keeps the real
  publish time; trove always emits `listed` explicitly instead of nuget.org's year-1900 marker.
- Hard delete (`deleteMode: delete`, or a raw delete of the `.nupkg`, R-NUGET-005) removes or
  trashes the version as a unit per [storage.md](../storage.md) (R-STOR-018, R-STOR-022).
  Relisting a hard-deleted version answers 404.
- Unlist and relist bump the package revision and publish the change notification of R-REPO-020.

Configuration (per local repository, `formatSettings` of the repository document,
[repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-004):

```yaml
formatSettings:
  deleteMode: unlist         # default (proposed); unlist | delete (Q-NUGET-1)
```

Acceptance criteria:

- `dotnet nuget delete Acme.Widgets 1.2.3 --source trove-local --api-key "$TOKEN" --non-interactive`
  exits 0. Afterwards `dotnet package search Acme.Widgets --source trove-local --exact-match`
  shows no `1.2.3`; the flat container list still contains `1.2.3`; a project pinned to `[1.2.3]`
  still restores; the SemVer 2 registration shows `"listed": false`.
- `curl -X POST -H "X-NuGet-ApiKey: $TOKEN" <feed>/package/acme.widgets/1.2.3` answers 200 and the
  version is back in search; a second POST answers 200 again. A token with `delete` and
  `annotate` but without `deploy` gets 403 for the POST.
- With `deleteMode: delete`, `nuget delete Acme.Widgets 1.2.4 -Source trove-local -ApiKey %TOKEN%
  -NonInteractive` succeeds and the `.nupkg` URL answers 404 afterwards.

### R-NUGET-008 · Package content (flat container)
Priority: **MVP** · Covers: PKG-NUGET-045

API ([package content](https://learn.microsoft.com/en-us/nuget/api/package-base-address-resource)),
permission `read` on the ID:

| Request | Response |
|---|---|
| `GET <feed>/flatcontainer/{lower_id}/index.json` | 200 `{"versions": [...]}`: every stored version of the ID, listed and unlisted, SemVer 1 and 2, in URL form, ascending by precedence; 404 when there is none |
| `GET <feed>/flatcontainer/{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg` | 200 `application/octet-stream`, the package bytes; 404 |
| `GET <feed>/flatcontainer/{lower_id}/{lower_version}/{lower_id}.nuspec` | 200 `application/xml`, the `.nuspec` bytes from inside the package; 404 |

- Every version in a list MUST be downloadable by the same caller with the same permissions.
- Package files are served like every item (R-STOR-012: `Range`, conditional requests, streaming).
  Through a virtual repository the response carries `Trove-Resolved-From` (R-REPO-019).
- `.nuspec` responses carry the package-content headers of the [Security notes](#security-notes).
- Download counts are kept by [storage.md](../storage.md) on the lead item (R-STOR-015, P1).

Acceptance criteria:

- After pushing `1.2.3` and `1.3.0-beta.1`, `curl <feed>/flatcontainer/acme.widgets/index.json`
  returns `{"versions":["1.2.3","1.3.0-beta.1"]}`.
- `curl -r 0-99` on a `.nupkg` URL returns 206 with 100 bytes.
- `dotnet restore -v diag` of a project referencing `Acme.Widgets 1.2.3` shows only
  `flatcontainer` URLs for the version list and the download.

### R-NUGET-009 · Package metadata (registrations)
Priority: **MVP** · Covers: PKG-NUGET-042, PKG-NUGET-046

API ([package metadata](https://learn.microsoft.com/en-us/nuget/api/registration-base-url-resource)),
permission `read` on the ID. Three hives per repository:

| Hive path | `@type` | `Content-Encoding` | SemVer 2.0.0-specific versions |
|---|---|---|---|
| `<feed>/registration/` | `RegistrationsBaseUrl` and its aliases | identity, always | excluded |
| `<feed>/registration-gz/` | `RegistrationsBaseUrl/3.4.0` | gzip when `Accept-Encoding` allows it | excluded |
| `<feed>/registration-semver2/` | `RegistrationsBaseUrl/3.6.0` | gzip when `Accept-Encoding` allows it | included |

Documents per hive `<hive>`:

| Document | URL | Content |
|---|---|---|
| Registration index | `<hive>{lower_id}/index.json` | `count`, `items` (pages); 404 when the ID has no version visible in this hive |
| Page, fetched separately | `<hive>{lower_id}/page/{lower}/{upper}.json` | `@id`, `count`, `items` (leaves), `lower`, `upper`, `parent` |
| Leaf | `<hive>{lower_id}/{lower_version}.json` | `@id`, `listed`, `packageContent`, `published`, `registration` |

- Paging: the visible versions, ascending, are cut into pages of `page_size`. When the package has
  fewer than `inline_below` visible versions, every page is inlined in the index with its `items`
  and `parent`, and its `@id` is `<index URL>#page/{lower}/{upper}`; otherwise pages are listed
  without `items` and fetched from their own URL. With the defaults this reproduces nuget.org's
  heuristic: 65 to 127 versions give two inlined pages.
- `lower` and `upper` are URL-form versions (inclusive). A page URL is answered with the visible
  leaves inside its bounds at request time, so a page URL from an older index stays valid.
- Leaf object in a page: `@id` (leaf URL), `catalogEntry` (object), `packageContent` (flat
  container `.nupkg` URL), `registration` (index URL).
- Catalog entry mapping:

| Property | Value |
|---|---|
| `@id` | the leaf URL |
| `id` | `declared_id` |
| `version` | full normalized version |
| `listed` | always emitted |
| `published` | `published_at`, ISO 8601 UTC |
| `packageContent` | flat container `.nupkg` URL (legacy duplicate) |
| `authors`, `description`, `iconUrl`, `language`, `licenseUrl`, `minClientVersion`, `projectUrl`, `requireLicenseAcceptance`, `summary`, `tags`, `title` | from `metadata`; absent values omitted |
| `licenseExpression` | `license` value when its type is `expression` |
| `dependencyGroups` | per group `targetFramework` (verbatim, omitted when absent) and `dependencies` with `id`, `range` (verbatim; omitted when the `.nuspec` gave none) and `registration` (the dependency's index URL in the same hive of the same repository) |
| `deprecation`, `vulnerabilities` | omitted for local packages; passed through for remote ones (R-NUGET-015) |
| `readmeUrl` | omitted; README access is R-NUGET-024 |

- A registration index offers no way to discover IDs; unknown IDs answer 404.
- The registration resource carries no owners or download counts; those are search's
  (R-NUGET-010).

Configuration (system file):

```yaml
nuget:
  registration:
    page_size: 64            # default (proposed)
    inline_below: 128        # default (proposed)
```

<details>
<summary>Registration index with one inlined page (sketch)</summary>

```json
{
  "count": 1,
  "items": [
    {
      "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/index.json#page/1.2.3/1.2.3",
      "count": 1,
      "lower": "1.2.3",
      "upper": "1.2.3",
      "parent": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/index.json",
      "items": [
        {
          "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/1.2.3.json",
          "packageContent": "https://trove.example.com/api/nuget/nuget-local/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",
          "registration": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/index.json",
          "catalogEntry": {
            "@id": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.widgets/1.2.3.json",
            "id": "Acme.Widgets",
            "version": "1.2.3",
            "authors": "Acme Platform Team",
            "description": "Widget primitives.",
            "listed": true,
            "published": "2026-10-28T09:14:03Z",
            "packageContent": "https://trove.example.com/api/nuget/nuget-local/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",
            "dependencyGroups": [
              {
                "targetFramework": "net8.0",
                "dependencies": [
                  {
                    "id": "Acme.Core",
                    "range": "[2.0.0, )",
                    "registration": "https://trove.example.com/api/nuget/nuget-local/v3/registration-semver2/acme.core/index.json"
                  }
                ]
              }
            ]
          }
        }
      ]
    }
  ]
}
```

</details>

Acceptance criteria:

- With a fixture package of 130 versions, the SemVer 2 index has 3 pages without `items` and each
  page URL returns its leaves; with 100 versions the index has 2 inlined pages; with 10 versions
  one inlined page.
- `curl -sI -H 'Accept-Encoding: gzip' <feed>/registration-gz/acme.widgets/index.json` shows
  `Content-Encoding: gzip`; the same against `<feed>/registration/` shows no compression.
- `dotnet restore` of a project that depends on `Acme.Widgets`, which depends on `Acme.Core`,
  resolves both from `nuget-local`.
- Visual Studio 2022 shows description, authors and dependency groups of `Acme.Widgets` in its
  package manager details pane (manual check, recorded per release).

### R-NUGET-010 · Search
Priority: **MVP** · Covers: PKG-NUGET-028, PKG-NUGET-047

API ([search](https://learn.microsoft.com/en-us/nuget/api/search-query-service-resource)):
`GET <feed>/search?q=&skip=&take=&prerelease=&semVerLevel=&packageType=`, permission `read` at
repository level; results and `totalHits` contain only IDs the caller may read (R-AUTHZ-016).

| Parameter | Rule |
|---|---|
| `q` | query (syntax below); empty or absent returns every package, paged |
| `skip` | default 0; above `max_skip`, negative or not an integer answers 400 |
| `take` | default `default_take`; above `max_take` is clamped; `0` returns only `totalHits`; negative or not an integer answers 400 |
| `prerelease` | `true` (case-insensitive) includes pre-release versions; anything else excludes them |
| `semVerLevel` | R-NUGET-004 |
| `packageType` | keep packages whose latest visible version declares that type (case-insensitive); a value that is not a valid package type name returns no results; empty means no filter |

Query syntax: terms separated by whitespace; double quotes group a phrase; `field:value` limits a
term to one field. Supported fields: `id` (ID contains the value), `packageid` (ID equals the
value), `title`, `tags`, `author`, `description`, `summary`. Other `field:` prefixes are treated as
free text. Every term must match (AND). A free-text term matches when it is contained in the ID,
is a prefix of an ID token (tokens split at `.`, `-`, `_` and at lower-to-upper case changes of
the display name), or matches title, tags, authors, summary or description through `search_doc`.

Ranking SHOULD put exact ID matches first, then ID prefix matches, then ID token-prefix matches,
then title, tag and other field matches; inside a rank, more downloads first. Ties MUST be broken
by `lower_id` ascending so paging is stable. An empty query orders by downloads descending, then
`lower_id`.

Response: `totalHits` (matching packages, independent of paging) and `data`, one object per
package whose latest version under the filters is visible:

| Property | Value |
|---|---|
| `id` | display name |
| `version` | full version of the latest visible version |
| `versions` | every listed version visible under `prerelease` and `semVerLevel`, ascending: `version` (full), `downloads`, `@id` (leaf URL in the `registration-semver2/` hive when `semVerLevel` is 2.0.0 or higher, else in `registration/`) |
| `registration` | the index URL in the same hive |
| `description`, `summary`, `title`, `iconUrl`, `licenseUrl`, `projectUrl`, `tags`, `authors` | from the latest visible version |
| `totalDownloads` | sum of `versions[].downloads` |
| `packageTypes` | `[{"name": …}]` of the latest visible version; at least `Dependency` |
| `owners`, `verified`, `deprecation`, `vulnerabilities` | omitted for local packages |

- `downloads` come from R-STOR-015 (P1); until that ships they are 0. The protocol requires the
  property, not a value.
- Unlisted versions never appear; a package with no listed visible version is absent.
- This requirement covers local repositories and the cache-only mode of remote repositories.
  Forwarding to an upstream is R-NUGET-016, merging in virtual repositories R-NUGET-018.
- Implementation hint: the candidate set comes from `nuget_version` (trigram index on `lower_id`,
  `search_doc`), the latest visible version per package from
  `DISTINCT ON (lower_id) … ORDER BY lower_id, sort_key DESC`; permission filtering, ranking and
  paging happen in SQL so `totalHits` is exact.

Configuration (system file):

```yaml
nuget:
  search:
    default_take: 20         # default (proposed)
    max_take: 1000           # default (proposed)
    max_skip: 3000           # default (proposed)
```

Acceptance criteria:

- `dotnet package search acme --source trove-local --format json` lists `Acme.Widgets` and
  `Acme.Core` with their latest stable versions; with `--prerelease` the latest pre-release
  appears; `--exact-match` lists every version of one ID.
- `nuget search acme -Source trove-local` (nuget.exe) lists both packages.
- `curl "<feed>/search?q=packageid:acme.widgets"` returns exactly one result;
  `curl "<feed>/search?q=&take=1&skip=1"` returns the second package of the empty-query order and
  the same `totalHits` as `skip=0`.
- `curl "<feed>/search?q=acme&packageType=DotnetTool"` returns only packages declaring that type.
- A user whose grant covers only `acme.core` sees `totalHits: 1` for `q=acme`.

### R-NUGET-011 · Autocomplete
Priority: **P1** · Covers: PKG-NUGET-048

API ([autocomplete](https://learn.microsoft.com/en-us/nuget/api/search-autocomplete-service-resource)),
permission and result filtering as R-NUGET-010:

| Request | Response |
|---|---|
| `GET <feed>/autocomplete?q=&skip=&take=&prerelease=&semVerLevel=&packageType=` | `totalHits`, `data`: display names whose `lower_id` starts with `q` or has an ID token starting with `q`, among packages with at least one listed visible version; ordered by downloads descending, then `lower_id` |
| `GET <feed>/autocomplete?id=&prerelease=&semVerLevel=` | `data`: listed visible versions of that ID (full form), ascending; empty array for an unknown ID |

`skip` and `take` follow R-NUGET-010. Remote repositories forward both forms to the upstream's
autocomplete resource when it has one and `forwardSearch` is on (R-NUGET-016); virtual merging is
R-NUGET-018.

Acceptance criteria:

- `curl "<feed>/autocomplete?q=acme.w"` returns `["Acme.Widgets"]`; `q=widg` returns it too (token
  prefix).
- `curl "<feed>/autocomplete?id=acme.widgets&prerelease=true&semVerLevel=2.0.0"` lists every
  listed version, including build metadata where present.
- Typing `Acme` in the Visual Studio 2022 package manager search box offers `Acme.Widgets`
  (manual check, recorded per release).

### R-NUGET-012 · Client authentication on NuGet endpoints
Priority: **MVP** · Covers: PKG-NUGET-016, PKG-NUGET-017, PKG-NUGET-018, REPO-028

Credential selection and validation are owned by
[identity-and-authentication.md](../identity-and-authentication.md) (R-AUTH-001, R-AUTH-002,
R-AUTH-033); 401, 403 and 404 decisions by [authorization.md](../authorization.md) (R-AUTHZ-016).
NuGet contributes:

- **API-key endpoints.** `PUT <feed>/package`, `DELETE` and `POST <feed>/package/{id}/{version}`
  and `PUT <feed>/symbolpackage`. Only there is `X-NuGet-ApiKey` examined, first, with the rule of
  R-AUTH-033: a well-formed token as the whole value or after the last `:` (the
  `<username>:<secret>` convention of older client setup templates) is the credential; any other
  value (a placeholder such as `az` that some CI snippets pass) is ignored and the
  `Authorization` header applies.
- **Basic and Bearer.** `Authorization: Basic` with any username and a trove token as password,
  and `Authorization: Bearer <token>`, on every NuGet endpoint (INV-8).
- **Session cookie.** Accepted for GET and HEAD only (R-AUTH-001), so a browser on the UI can
  download a package; never on API-key endpoints. A cross-site form can send `POST`, which is the
  relist method; it carries no credential trove accepts.
- **Challenge.** A 401 on a NuGet endpoint carries exactly `WWW-Authenticate: Basic
  realm="trove"`. This page sets the challenge for its endpoints (R-AUTHZ-016 allows format
  pages to): NuGet clients answer Basic, and offering NTLM or Negotiate besides Basic would make
  clients send Basic only after they restrict their valid authentication types in `nuget.config`.
- **Anonymous requests are always challenged, never answered 404** (R-AUTHZ-016). A client sends
  its configured credentials only after a 401
  ([authenticated feeds](https://learn.microsoft.com/en-us/nuget/consume-packages/consuming-packages-authenticated-feeds)).
- **Forced authentication.** `requireAuthentication: true` on a repository (R-REPO-004) answers
  every anonymous request with 401 even where the anonymous principal may read. Without it, a
  client with configured credentials never sends them for reads while anonymous read is granted,
  so its reads are attributed to the anonymous principal.
- **Virtual repositories.** An anonymous request through a virtual repository is challenged unless
  the anonymous principal may read the virtual repository and every member (R-AUTHZ-009 rule 6).
- The service index is subject to the same rules, which makes a client authenticate on its first
  request; NuGet 6.7 and later then pre-authenticate every resource below `<feed>/` (R-NUGET-001).

Configuration (common repository field, shown for context):

```yaml
requireAuthentication: false   # default (proposed), R-REPO-004
```

Acceptance criteria:

- Without any anonymous grant: `curl -i <feed>/index.json` answers 401 with exactly
  `WWW-Authenticate: Basic realm="trove"`; `curl -u anything:$TOKEN` answers 200;
  `curl -H "Authorization: Bearer $TOKEN"` answers 200; a revoked token answers 401.
- `dotnet nuget add source <feed>/index.json --name trove-local --username ci --password "$TOKEN"
  --store-password-in-clear-text`, then `dotnet restore` succeeds.
- `dotnet nuget push … --api-key az` with credentials in `nuget.config` succeeds (first attempt
  401, retry with Basic); `--api-key "$TOKEN"` without credentials succeeds; `--api-key "ci:$TOKEN"`
  succeeds; `--api-key <revoked token>` fails with 401.
- With anonymous read granted on `nuget-local`: `curl <feed>/index.json` answers 200 without
  credentials; after setting `requireAuthentication: true` it answers 401, and a restore with
  `nuget.config` credentials succeeds and is audited under the token's principal.
- `curl -X POST -b "trv_session=$S" <feed>/package/acme.widgets/1.2.3` answers 401.

### R-NUGET-013 · Permissions per NuGet operation
Priority: **MVP** · Covers: PKG-NUGET-019

The action map for NuGet protocol operations, package ownership, the virtual repository gate and
cache fills are normative in [authorization.md](../authorization.md) (R-AUTHZ-007, R-AUTHZ-008,
R-AUTHZ-009, R-AUTHZ-018). This page supplies the coordinate for every NuGet request and the
operations that table does not list:

| Operation | Coordinate | Action |
|---|---|---|
| Service index, search, autocomplete | repository level; results filtered per ID | `read` |
| Version list, registration documents, `.nupkg`, `.nuspec`, embedded files | `lower_id` and canonical path | `read` |
| Push, symbol push | declared ID | `deploy`, ownership |
| Unlist, hard delete | `lower_id` | `delete`, ownership |
| Relist | `lower_id` | `deploy`, ownership |
| Symbol file from a local repository | `lower_id` of the version that holds the file | `read` |
| Symbol file or vulnerability page from a remote upstream | repository level | `read`, plus the cache fill rule for uncached files |
| Metadata reindex (R-NUGET-025) | repository level | per R-REPO-016 |

- A package-name grant is all-or-nothing per ID: a caller sees every version of an ID or none.
- Through a virtual repository the caller needs `read` on the virtual repository itself; members
  the caller cannot read are skipped silently in every merge (R-AUTHZ-009, R-NUGET-017,
  R-NUGET-018). Publishing through a virtual repository is checked on its deploy target
  (R-NUGET-019).
- Example for GitLab CI (# example): an identity mapping of
  [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) grants `read` on `nuget-all` and `deploy` on
  `nuget-local` for package names matching `acme.payments.*`, derived from the job's
  `project_path`.

Acceptance criteria:

- A token with `read` only: restore succeeds, push answers 403, `dotnet nuget delete` answers 403.
- A token with `deploy` for `acme.payments.*`: pushing `Acme.Payments.Api` succeeds, pushing
  `Acme.Billing` answers 403 and no blob is written (storage metrics unchanged).
- A second CI mapping with `deploy` on the same pattern cannot push a new version of
  `Acme.Payments.Api` once the first mapping owns it (403 `deny.ownership`).

### R-NUGET-014 · Remote repository: upstream and resource discovery
Priority: **MVP** · Covers: PKG-NUGET-023, PKG-NUGET-024, RMT-004

Remote settings shared by all formats are fields of the `remote` section owned by
[repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-024). For NuGet:

- `remote.upstream.url` is the upstream's v3 service index URL. The default for a new NuGet remote
  is `https://api.nuget.org/v3/index.json`.
- On create, update and the upstream test (R-REPO-024 probes the service index), the plug-in
  requires a `PackageBaseAddress/3.0.0` resource and at least one `RegistrationsBaseUrl` type in
  the upstream index; otherwise the configuration is refused with 422 naming the missing resource.
  A v2-only upstream is refused.
- Resource discovery picks, from the upstream index: `PackageBaseAddress/3.0.0`; the registration
  hive in the order `RegistrationsBaseUrl/3.6.0`, `/3.4.0`, unversioned; `SearchQueryService/3.5.0`
  before the unversioned type; the autocomplete resource likewise; `VulnerabilityInfo/6.7.0` (P1).
  The upstream index is a metadata cache entry (R-NUGET-015); rediscovery happens when it expires.
- Discovered resources may live on other hosts than the service index. They are fetched only when
  their host is the upstream origin or matches `remote.upstream.extraHosts`; otherwise 502
  `foreign_host_denied` (R-REPO-043). For the default upstream the NuGet format proposes
  `extraHosts: [{"pattern": "*.nuget.org", "sendCredentials": false}]`, because nuget.org serves
  search and symbols from other `nuget.org` hosts.
- A trove repository on another instance is a valid upstream: its service index is a plain v3
  index, so no special mode is needed.
- The remote's own service index lists trove URLs only (R-NUGET-002); it never lists publish
  resources, and publish requests answer 405.

Configuration (per remote repository; `remote` belongs to
[repositories-and-proxy.md](../repositories-and-proxy.md) and is shown for context;
`formatSettings` is NuGet's):

```yaml
key: nuget-org                                   # example
class: remote
format: nuget
remote:
  upstream:
    url: https://api.nuget.org/v3/index.json     # default (proposed) for NuGet remotes
    extraHosts:                                  # proposed by the format for nuget.org
      - pattern: "*.nuget.org"
        sendCredentials: false
formatSettings:
  forwardSearch: true                            # default (proposed); R-NUGET-016, Q-NUGET-4
  symbolServerUrl: https://symbols.nuget.org/download/symbols   # default (proposed); empty disables; P1
  vulnerabilityInfo: true                        # default (proposed); P1
```

Acceptance criteria:

- Creating a remote with only `remote.upstream.url` set to nuget.org's index succeeds; the
  upstream test reports it reachable.
- Creating a remote whose upstream index lacks `PackageBaseAddress/3.0.0` (stub server) is refused
  with 422 naming it.
- A remote whose upstream is `nuget-all` on a second trove instance restores packages through
  both instances.

### R-NUGET-015 · Remote repository: proxying, caching and URL rewriting
Priority: **MVP** · Covers: PKG-NUGET-024, RMT-018, RMT-020, RMT-033, RMT-034

The remote request path (R-REPO-025), collapsing of concurrent misses (R-REPO-026), freshness and
stale serving (R-REPO-027, R-REPO-029), negative cache (R-REPO-028) and URL rewriting at serve
time (R-REPO-043) apply. The NuGet plug-in classifies requests into descriptors (R-REPO-003):

| Client request to the remote | Kind | Canonical path | Upstream request |
|---|---|---|---|
| service index | rendered | — | none; built from the remote's configuration |
| (discovery) | metadata | `.nuget/index.json` | upstream service index |
| `flatcontainer/{lower_id}/index.json` | metadata | `{lower_id}/index.json` | upstream package content `{lower_id}/index.json` |
| `.nupkg` | artifact | `{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg` | upstream package content URL; when that answers 404 but the upstream registration lists the version, the leaf's `packageContent` (recorded in `remote_url_map`) |
| `.nuspec` | artifact | `{lower_id}/{lower_version}/{lower_id}.nuspec` | extracted from the cached `.nupkg`; when that is not cached, upstream package content `.nuspec` |
| any registration document | metadata | `{lower_id}/.registration/index.json` and `{lower_id}/.registration/page/{n}.json` | upstream registration index of the discovered hive, plus every non-inlined page |
| search, autocomplete | search | — | R-NUGET-016 |
| symbol file (P1) | artifact | `.symbols/<key>` | R-NUGET-022 |
| vulnerability index and pages (P1) | metadata | `.vulnerabilities/index.json`, `.vulnerabilities/<name>.json` | R-NUGET-023 |

- Stored upstream documents stay unchanged. Registration documents are not passed through: at
  serve time the plug-in parses the stored upstream index and pages, drops versions the requested
  hive must not show (R-NUGET-004, using each leaf's version and dependency ranges), and renders
  its own index, pages and leaves with R-NUGET-009's paging. Catalog entry properties are copied,
  including `deprecation` and `vulnerabilities`; unknown properties are kept.
- URL rewriting in rendered documents:

| Property | Rewritten to |
|---|---|
| index, page and leaf `@id`, `parent`, `registration`, dependency `registration` | the serving repository's hive URLs |
| `packageContent`, `catalogEntry.packageContent` | the serving repository's flat container URL |
| `catalogEntry.@id` | the serving repository's leaf URL |
| `iconUrl` pointing into the upstream package content base | the serving repository's flat container `icon` URL (P1, R-NUGET-024); unchanged before that |
| `projectUrl`, `licenseUrl`, other `iconUrl` values | unchanged (web links shown to people) |

- Upstream URLs for package content are derived from the discovered `PackageBaseAddress` and the
  canonical path, never from a host named in a client request (R-REPO-043).
- Every cached `.nupkg` is parsed like an upload (R-NUGET-005 steps 1 to 4, without the
  `SymbolsPackage` rejection) and recorded with `origin = 'cache'` rows; a package that fails
  parsing is still served and cached, logged and counted (R-NUGET-028), but gets no NuGet rows.
  The bytes are never altered. A cached `.nupkg` whose `.nuspec` parses but declares an ID or
  version whose URL form differs from the canonical path MUST be refused with 502
  `upstream_package_mismatch` and not stored. NuGet v3 announces no content digest (H-35), so the
  declared identity is the only check that the upstream served the package that was asked for.
- Include and exclude patterns of the remote (R-REPO-005) and namespace claims (R-REPO-023)
  see the canonical paths above, so `acme.*/**` keeps internal IDs away from nuget.org. The
  upstream service index and the vulnerability documents name no package; the plug-in marks them
  repository-level and patterns are not applied to them, so a remote restricted to a few ID
  prefixes still discovers its upstream (Q-NUGET-7). Symbol files are not exempt: their keys carry
  assembly names.
- Digests (R-REPO-041): NuGet v3 package metadata announces no content digest, so in the MVP the
  plug-in supplies none; `verify-if-available` records the computed digests. `checksumPolicy:
  require` on a NuGet remote is refused with 422 until R-NUGET-029 supplies digests (H-35).
- Metadata cache period: the NuGet format default registered under R-REPO-003 applies to the
  service index, version lists, registrations and vulnerability data (R-REPO-024 lists 7200 s
  today; Q-NUGET-2 proposes 300 s). `.nupkg`, `.nuspec`, icons and symbol files are immutable.
- Push, delete and relist are not offered (405).

Acceptance criteria (against a stub v3 upstream in CI and, in a nightly job, against nuget.org):

- `dotnet restore` of a project referencing `Newtonsoft.Json 13.0.3` through `nuget-org` succeeds;
  the `.nupkg` is cached; a second restore with the upstream unreachable succeeds.
- Every URL in `curl <base>/api/nuget/nuget-org/v3/registration-semver2/newtonsoft.json/index.json`
  except `projectUrl`, `licenseUrl` and `iconUrl` starts with `<base>/api/nuget/nuget-org/v3/`.
- For a stub upstream with 200 versions of one ID in non-inlined pages, the remote's index uses
  trove's paging and contains all 200 leaves.
- With exclude pattern `acme.*/**`,
  `curl <base>/api/nuget/nuget-org/v3/flatcontainer/acme.widgets/index.json` answers 404 and the
  stub upstream records no request.
- 50 concurrent restores of the same uncached package across two replicas cause one upstream
  `.nupkg` request.
- The SHA-512 of a `.nupkg` served by the remote equals the one served by the upstream.
- A stub upstream that serves `Other.Pkg 1.0.0` at the content URL of `acme.widgets` 1.0.0 causes
  502 `upstream_package_mismatch`, and no item exists afterwards.

### R-NUGET-016 · Search and autocomplete through a remote repository
Priority: **MVP** · Covers: PKG-NUGET-024, PKG-NUGET-028

- `forwardSearch: true`: search requests are forwarded to the upstream's search resource with the
  same `q`, `prerelease`, `semVerLevel` and `packageType`, and with `skip` and `take` clamped to
  trove's limits (R-REPO-025 lets the plug-in map these parameters). Result URLs are rewritten as
  in R-NUGET-015 (`registration`, `versions[].@id` into the hive that matches the request's
  `semVerLevel`); `owners`, `verified`, `deprecation`, `vulnerabilities`, download counts and
  `packageTypes` are kept. Results whose ID is excluded by the remote's patterns, reserved, or not
  readable by the caller are removed, and `totalHits` is reduced by the number removed from the
  returned page.
- Forwarded responses are kept in an in-process cache for `nuget.search.remote_result_ttl`; they are
  not persisted. Forwarding needs `read` on the remote; it caches nothing, so the cache fill rule
  does not apply.
- When the upstream search fails or the circuit is open (R-REPO-029), the remote answers from its
  cache-only search.
- `forwardSearch: false`: the remote answers with the cache-only search, which is R-NUGET-010 over
  its `origin = 'cache'` rows.
- Autocomplete (P1) follows the same two modes.

Configuration (system file):

```yaml
nuget:
  search:
    remote_result_ttl: 60s     # default (proposed)
```

Acceptance criteria:

- `dotnet package search Newtonsoft --source trove-org` returns `Newtonsoft.Json` with upstream
  download counts; every `registration` URL points at `nuget-org`.
- With `forwardSearch: false`, the same search returns only packages restored through the remote
  before.
- With the upstream unreachable, search answers 200 from the cache-only mode.

### R-NUGET-017 · Virtual repository: merged package content and metadata
Priority: **MVP** · Covers: PKG-NUGET-026, REPO-035, REPO-036, REPO-037

Virtual resolution (R-REPO-019: member filtering, pinning, permission filtering, tiers), the merged
metadata cache (R-REPO-020) and dependency-confusion protection (R-REPO-023) are owned by
[repositories-and-proxy.md](../repositories-and-proxy.md). The NuGet merge rules:

| Request | Kind | Merge rule |
|---|---|---|
| Version list | metadata | union of the remaining members' version lists (URL form), deduplicated, ascending |
| `.nupkg`, `.nuspec`, embedded files | artifact | tier resolution of R-REPO-019: local members, then remote caches, then remote upstreams |
| Registration documents | metadata | union of the members' leaves for the requested hive; for a version held by several members, the entry of the member tier resolution would pick for that version's `.nupkg` (R-REPO-019); then R-NUGET-009's paging and URL rewriting to the virtual repository |

- Because the metadata winner and the content winner follow the same order, the registration
  entry a client reads always describes the bytes it downloads.
- NuGet v3 has no "latest version" artifact request; clients choose from version lists. The union
  already contains every member's versions, upstream ones included, which is what REPO-036 asks
  for.
- Pin rules and namespace claims (R-REPO-023) apply per ID: for a pinned ID, and for every ID that
  a local member claims (automatically on its first publish with `local.autoClaim`), remote
  members contribute nothing to its version list, registration,
  downloads or search results, and are not asked upstream.
- A publish to a member is visible through the virtual repository at once (R-REPO-020 drops the
  merged entries on all replicas). A merge without a transiently failing remote member is served
  with `Cache-Status: trove; detail=partial` and not cached (R-REPO-020).

Configuration (per virtual repository; `virtual` belongs to
[repositories-and-proxy.md](../repositories-and-proxy.md) and is shown for context):

```yaml
key: nuget-all                                   # example
class: virtual
format: nuget
virtual:
  members: [nuget-local, nuget-org]              # example
  deployTarget: nuget-local                      # example
  pinning:                                       # pin rules: R-REPO-023
    rules:
      - packages: ["Acme.*"]                     # example
        repositories: [nuget-local]
```

Acceptance criteria:

- With `nuget-all` as the only source (`<clear />` in `nuget.config`), `dotnet restore` of a
  project referencing `Acme.Widgets 1.2.3` (local) and `Newtonsoft.Json 13.0.3` (remote) succeeds.
- `Contoso.Util 2.0.0` in `nuget-local` and `Contoso.Util` `2.0.0` and `2.1.0` at the stub upstream,
  `autoClaim: false` on `nuget-local`, no claims and no rules: the virtual version list is `["2.0.0","2.1.0"]`; the `2.0.0`
  download carries `Trove-Resolved-From: nuget-local` and its registration entry carries the
  local metadata. With `autoClaim: true` on `nuget-local` (the publish creates the claim) the list
  is `["2.0.0"]` and the stub records no
  request for `contoso.util`.
- Push through `nuget-all`, then `dotnet restore --no-http-cache` through `nuget-all` on the other
  replica within one second sees the new version.
- With the stub upstream down and nothing cached, local packages still restore through
  `nuget-all`, and the version list of a remote-only ID answers 502, not 404 (R-REPO-019).

### R-NUGET-018 · Virtual repository: merged search and autocomplete
Priority: **MVP** · Covers: PKG-NUGET-026, PKG-NUGET-028

Search through a virtual repository merges the result lists of the members that remain after
R-REPO-019's filtering:

1. Each member is queried in parallel with the request's filters, `skip=0` and
   `take = min(skip + take, virtualMergeWindow)`. Local members use R-NUGET-010, remote members
   R-NUGET-016.
2. Results of remote members for pinned or claimed IDs (R-REPO-023) are removed.
3. The merged order is: packages whose ID equals a query term exactly (from any member), then the
   remaining results member by member in resolution order, each member's own order kept.
4. Results are deduplicated by `lower_id`. For a package found in several members, `versions` is
   the union (for equal versions the member that tier resolution would pick wins), `version` and
   the package-level fields come from the latest version of that union, `totalDownloads` is the
   sum.
5. The page `[skip, skip + take)` of the merged list is returned; positions beyond
   `virtual_merge_window` return an empty `data`.
6. `totalHits` is the sum of the members' `totalHits` minus the duplicates and removals seen in the
   window. It is an estimate; clients use it only to decide whether to load more.

All URLs in the merged result point at the virtual repository. Autocomplete (P1) merges the same
way: ID mode as a deduplicated union in the order above, version mode as the ascending union of the
members' versions.

Configuration (system file):

```yaml
nuget:
  search:
    virtual_merge_window: 1000   # default (proposed)
```

Acceptance criteria:

- `dotnet package search acme --source trove-all` returns local `Acme.*` packages before upstream
  results, and `q=packageid:newtonsoft.json` returns `Newtonsoft.Json` first.
- A package present in `nuget-local` and at the stub upstream, not pinned, appears once with the
  union of its versions; when pinned, only with the local versions.
- `curl "<feed>/search?q=&skip=1000&take=20"` on `nuget-all` answers 200 with empty `data`.
- Visual Studio 2022's browse tab on `nuget-all` loads more results while scrolling (manual check,
  recorded per release).

### R-NUGET-019 · Publishing through a virtual repository
Priority: **MVP** · Covers: PKG-NUGET-027, REPO-038

- Push, symbol push, unlist, relist and delete sent to a virtual repository are executed on its
  deploy target (`virtual.deployTarget`, R-REPO-021) and answer exactly as that local repository
  would. Permissions and ownership are checked on the target; the virtual repository's own `read`
  gate applies as well (R-AUTHZ-009 rule 5). Unlist and relist never touch other members.
- Without a deploy target, the publish resources are not listed (R-NUGET-002) and publish requests
  answer 405 `no_deploy_target`.

Acceptance criteria:

- `dotnet nuget push Acme.Widgets.1.4.0.nupkg --source trove-all --api-key "$TOKEN"` stores the
  package in `nuget-local`; `/files/nuget-local/acme.widgets/1.4.0/acme.widgets.1.4.0.nupkg`
  exists.
- After removing the deploy target, the same push fails with 405 and the service index of
  `nuget-all` lists no `PackagePublish/2.0.0`.

### R-NUGET-020 · Symbol package push
Priority: **MVP** · Covers: PKG-NUGET-029, PKG-NUGET-050

API ([symbol package publish](https://learn.microsoft.com/en-us/nuget/api/symbol-package-publish-resource)),
an API-key endpoint (R-NUGET-012):

| Request | Responses | Permission |
|---|---|---|
| `PUT <feed>/symbolpackage`, body as package push, carrying a `.snupkg` | 201 stored; 400 invalid symbol package; 401; 403; 404 no package with that ID and version in the repository; 409 a different symbol package for that version exists; 413 | `deploy` on the ID, ownership |

- Validation, beyond the archive and `.nuspec` rules of R-NUGET-005: the package types MUST include
  `SymbolsPackage`; the matching `.nupkg` (same URL form) MUST exist in the target repository;
  entries MUST have one of the extensions `.pdb`, `.nuspec`, `.xml`, `.psmdcp`, `.rels`, `.p7s`
  (the constraints nuget.org applies,
  [symbol packages](https://learn.microsoft.com/en-us/nuget/create-packages/symbol-packages-snupkg));
  every `.pdb` MUST be a Portable PDB or a Windows PDB (MSF 7.00) by its file signature.
- The `.snupkg` is stored as an item at its canonical path (R-NUGET-005) and recorded below.
  Re-pushing byte-identical content answers 201 without change; different content answers 409,
  because symbols must match the binaries of the immutable package.
- Legacy `.symbols.nupkg` packages carry no `SymbolsPackage` type and are rejected with 400. The
  dedicated symbol push address for nuget.exe older than 3.5 is not offered.
- NuGet clients push a `.snupkg` lying next to the pushed `.nupkg` automatically, the package first;
  each push hits its own resource.
- Symbol packages are removed with their package version (R-NUGET-005, R-NUGET-007).
- Serving PDBs to debuggers is P1 (R-NUGET-021); its backfill job indexes symbol packages stored
  before it ships.

Data model:

```sql
CREATE TABLE nuget_symbol_package (
    version_id  bigint PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    item_id     bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,   -- the .snupkg
    indexed_at  timestamptz                                                -- set by R-NUGET-021
);
```

Acceptance criteria:

- `dotnet pack -c Release -p:Version=1.5.0 -p:IncludeSymbols=true -p:SymbolPackageFormat=snupkg`,
  then `dotnet nuget push out/Acme.Widgets.1.5.0.nupkg --source trove-local --api-key "$TOKEN"`
  stores both files.
- `nuget push Acme.Widgets.1.5.1.snupkg -Source trove-local -ApiKey %TOKEN%` before the `.nupkg`
  answers 404; after pushing the `.nupkg` it answers 201; pushing it again answers 201.
- A `.snupkg` containing a `.dll` answers 400; a `.symbols.nupkg` answers 400.

### R-NUGET-021 · Symbol server for local repositories
Priority: **P1** · Covers: PKG-NUGET-030

Debuggers (Visual Studio, WinDbg, dotnet-symbol) request files with the Simple Symbol Query
Protocol ([SSQP](https://github.com/dotnet/symstore/blob/main/docs/specs/Simple_Symbol_Query_Protocol.md)):
`GET <feed>/symbols/<key>`, where the key is `<file>/<index>/<file>`
([key conventions](https://github.com/dotnet/symstore/blob/main/docs/specs/SSQP_Key_Conventions.md)).

| PDB kind | Index part of the key | Taken from |
|---|---|---|
| Portable PDB | GUID (32 lowercase hex) followed by `FFFFFFFF` | the PDB ID in the `#Pdb` stream |
| Windows PDB | GUID (32 lowercase hex) followed by the age in hex without leading zeros | the PDB info stream of the MSF container |

- Indexing runs at symbol push (and as a backfill job for symbol packages stored earlier): each
  `.pdb` is extracted to a generated item (storage layout of R-NUGET-005), its key is computed and
  stored lowercased, and for Portable PDBs its checksum is computed as SHA-256 over the file with
  the 20-byte PDB ID zeroed
  ([PE/COFF PDB checksum entry](https://github.com/dotnet/runtime/blob/main/docs/design/specs/PE-COFF.md)).
- Keys are compared case-insensitively. When several versions hold the same key, the newest
  version's file is served.
- When the request carries a `SymbolChecksum` header (`SHA256:<hex>`, several separated by `;`)
  and the PDB is portable, the file is served only if one listed checksum matches; otherwise 404.
- Response: 200 `application/octet-stream`; 404 for unknown keys, including the compressed
  (`.pd_`) and pointer (`file.ptr`) probes debuggers send.
- Authentication and permission as for package downloads (R-NUGET-012, R-NUGET-013).
- Users configure `<feed>/symbols/` as a symbol location in the debugger. Whether Visual Studio
  answers the Basic challenge for symbol servers was not verified; the acceptance test settles it.

Data model:

```sql
CREATE TABLE nuget_symbol_file (
    repository_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    symbol_key    text   NOT NULL,   -- lowercased "<file>/<index>/<file>"
    version_id    bigint NOT NULL REFERENCES nuget_symbol_package (version_id) ON DELETE CASCADE,
    item_id       bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,  -- extracted PDB
    pdb_kind      text   NOT NULL CHECK (pdb_kind IN ('portable', 'windows')),
    checksum      bytea,             -- SHA-256 per the PE/COFF rule; portable only
    PRIMARY KEY (repository_id, symbol_key, version_id)
);
```

Acceptance criteria:

- `curl -u x:$TOKEN <feed>/symbols/acme.widgets.pdb/<guid>ffffffff/acme.widgets.pdb` downloads the
  PDB whose bytes equal the entry in the `.snupkg`; `dotnet-symbol --server-path <feed>/symbols/
  -o out Acme.Widgets.dll` against a repository with anonymous read does the same.
- The same request with a wrong `SymbolChecksum` answers 404; with the right one 200.
- Visual Studio 2022 with Just My Code off and `<feed>/symbols/` as symbol location steps into
  `Acme.Widgets` source when debugging a consumer (manual check, recorded per release).

### R-NUGET-022 · Symbol proxy and symbol resolution through virtual repositories
Priority: **P1** · Covers: PKG-NUGET-031, PKG-NUGET-032

- A remote repository with a non-empty `formatSettings.symbolServerUrl` answers
  `GET <feed>/symbols/<key>` by requesting `<symbolServerUrl>/<key>` (key lowercased) through the
  remote request path, forwarding a `SymbolChecksum` header when present; the public gallery's
  symbol server reportedly requires that header for Portable PDBs (not verified against Microsoft
  documentation). The host must be the upstream origin or in `extraHosts` (R-NUGET-014). Results
  are cached as immutable artifacts under `.symbols/<key>`; a cached Portable PDB is checked
  against the request's `SymbolChecksum` as in R-NUGET-021.
- An upstream 404 enters the negative cache only for requests that carried a `SymbolChecksum`
  header or for Windows PDB keys, so a probe without header cannot hide a file from a later
  request with header.
- The canonical path `.symbols/<key>` is not under a package ID; a remote whose include patterns
  list only ID prefixes does not serve symbols unless `.symbols/**` is included.
- Symbol packages cannot be pushed into a remote repository (405).
- A virtual repository answers symbol requests from its members in tier order: local symbol
  files first, then remote caches, then remote upstreams.

Acceptance criteria:

- `curl` through `nuget-org` for the key and `SymbolChecksum` of a known nuget.org portable PDB
  returns the file (nightly job against the live symbol server); the second request is served from
  the cache.
- Through `nuget-all`, a key present in `nuget-local` is served locally without an upstream
  request; an unknown key requested with `SymbolChecksum` answers 404 and is not requested upstream
  again within the negative cache period.

### R-NUGET-023 · Vulnerability information
Priority: **P1** · Covers: PKG-NUGET-051

NuGet 6.7 and later download the
[vulnerability resource](https://learn.microsoft.com/en-us/nuget/api/vulnerability-info) during
restore and audit packages locally.

- A remote repository with `formatSettings.vulnerabilityInfo: true` whose upstream offers
  `VulnerabilityInfo/6.7.0` lists the resource in its own service index and serves
  `<feed>/vulnerabilities/index.json` and `<feed>/vulnerabilities/<name>.json`. Index and pages are
  metadata cache entries; page URLs are rewritten to the remote; `@updated` is kept, so clients
  re-download a page only when the upstream changed it.
- A virtual repository lists the resource when at least one readable remote member offers it.
  Its index is the concatenation of the members' entries, with `@name` rewritten to
  `<first 8 hex of SHA-256 of the member key>-<upstream name>` truncated to 32 characters, and
  page URLs pointing at the virtual repository. The index MUST NOT exceed 16 entries; members
  beyond that are dropped in resolution order and the drop is logged.
- Local repositories do not list the resource; trove holds no advisory data of its own. Because
  advisories are keyed by package ID, a virtual repository that includes a nuget.org remote also
  audits public packages restored from local members.
- Deprecation and vulnerability data inside registration entries of remote packages pass through
  (R-NUGET-015).

Acceptance criteria:

- `dotnet restore` through `nuget-all` of a project referencing a package version with a known
  advisory reports the matching `NU190x` warning.
- The virtual vulnerability index has at most 16 entries and every `@id` points at `nuget-all`.

### R-NUGET-024 · Embedded icon, license and README
Priority: **P1** · Covers: PKG-NUGET-055

Packages may embed an icon, a license file and a README
([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)).

- At ingestion (and by the reindex of R-NUGET-025 for older packages), the plug-in extracts the
  files the `.nuspec` names in `icon`, `license` (type `file`) and `readme` into generated items
  (storage layout of R-NUGET-005). Limits: icon at most 1 MiB and JPEG or PNG by content; license
  and README at most 1 MiB of UTF-8 text. Files outside the limits are not extracted and their URL
  is not emitted; the package is still accepted.
- Endpoints, permission `read` on the ID: `<feed>/flatcontainer/{lower_id}/{lower_version}/icon`
  (`image/png` or `image/jpeg`), `…/license` (`text/plain; charset=utf-8`), `…/readme`
  (`text/markdown; charset=utf-8`); 404 when the package embeds none.
- Emitted URLs: `iconUrl` and `licenseUrl` in registration and search point at these endpoints when
  the package embeds the file; the service index lists `ReadmeUriTemplate/6.13.0` with the
  `readme` template. Clients probe that template for every package they show, so the 404 path MUST
  be answered from PostgreSQL without touching object storage.
- Remote repositories proxy `icon` from the upstream package content base and rewrite `iconUrl`
  values that point there (R-NUGET-015). Virtual repositories resolve these files like `.nuspec`.
- Package-derived content carries the headers of the [Security notes](#security-notes).

Acceptance criteria:

- A package packed with `<PackageIcon>icon.png</PackageIcon>` and
  `<PackageReadmeFile>README.md</PackageReadmeFile>` shows its icon and README in the Visual
  Studio 2022 package manager when installed from `nuget-local` (manual check, recorded per
  release).
- `curl -I <feed>/flatcontainer/acme.widgets/1.5.0/readme` returns `text/markdown` with
  `Content-Security-Policy: sandbox; default-src 'none'`; for a package without README it returns
  404.

### R-NUGET-025 · Metadata reindex
Priority: **P1** · Covers: PKG-NUGET-007, API-040, FMT-012

The operation `POST /api/v1/repositories/{key}/reindex` is owned by
[repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-016: 202 with a job reference,
one run per repository, 409 `reindex_running`, permission system administrator or `manage`). For
NuGet repositories the plug-in:

- re-parses every `.nupkg` of a local repository, or every cached `.nupkg` of a remote repository,
  and rewrites `metadata`, `search_doc`, `is_prerelease`, `is_semver2`, `package_types`,
  `declared_id` and `sort_key` of `nuget_version` in batches, keeping listing state, publish time
  and download counts;
- creates missing NuGet rows for `.nupkg` items that have none (for example after an import);
- extracts missing embedded files (R-NUGET-024) and indexes symbol packages without
  `indexed_at` (R-NUGET-021);
- checkpoints progress per `lower_id`, so a job resumed on another replica skips finished IDs;
- never changes stored bytes.

On a virtual repository the operation only drops the merged-metadata cache (R-REPO-016).

Acceptance criteria:

- After corrupting `metadata` of a fixture version in the database, the reindex restores it and
  search returns the original description.
- Killing the replica that runs the job resumes it elsewhere without reprocessing finished IDs.

### R-NUGET-026 · Client compatibility and client setup instructions
Priority: **MVP** · Covers: PKG-NUGET-033, PKG-NUGET-034, PKG-NUGET-035, PKG-NUGET-036, UI-025

Supported clients: the .NET SDK 8.0, 9.0 and 10.0 (`dotnet`), nuget.exe 6.8 and later, Visual
Studio 2022 17.8 and later. Older NuGet clients down to 4.3 work without the pre-authentication of
R-NUGET-001 (each resource first draws a 401); clients older than 4.3 are not supported. The
acceptance suite (R-NUGET-027) pins the tested versions.

The client setup instructions in the UI (rendering owned by [api-and-ui.md](../api-and-ui.md))
always use the `index.json` URL, never a v2 address, and show per repository:

```shell
# .NET CLI; on Linux and macOS the password must be stored in clear text
dotnet nuget add source https://trove.example.com/api/nuget/nuget-all/v3/index.json \
  --name trove --username token --password "$TROVE_TOKEN" --store-password-in-clear-text
dotnet restore
dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source trove --api-key "$TROVE_TOKEN"

# nuget.exe
nuget sources add -Name trove -Source https://trove.example.com/api/nuget/nuget-all/v3/index.json -Username token -Password %TROVE_TOKEN%
nuget push Acme.Widgets.1.2.3.nupkg -Source trove -ApiKey %TROVE_TOKEN%
```

`nuget.config` for a repository checkout (values # example; `%TROVE_TOKEN%` is expanded from the
environment on every platform):

```xml
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="https://trove.example.com/api/nuget/nuget-all/v3/index.json" protocolVersion="3" />
  </packageSources>
  <packageSourceCredentials>
    <trove>
      <add key="Username" value="token" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>
</configuration>
```

- The instructions recommend one source, the virtual repository, with `<clear />`, so clients do
  not also query nuget.org directly. Where several sources are unavoidable they show NuGet's
  `packageSourceMapping` for the organization's ID prefixes.
- Visual Studio: Tools → NuGet Package Manager → Package Manager Settings → Package Sources, a
  name and the `index.json` URL. Symbol location (P1): Tools → Options → Debugging → Symbols,
  `<feed>/symbols/`.
- GitLab CI: the reference pipeline of [gitlab-ci-oidc.md](../gitlab-ci-oidc.md) (R-CI-028)
  obtains `TROVE_TOKEN` through the OIDC exchange and runs the push and restore lines above.
- The `nuget.config` elements trove relies on: `protocolVersion="3"` when the URL does not end in
  `.json`, credentials per source, `<clear />`, package source mapping, and from NuGet 7.6 the
  `NUGET_API_KEY` environment variable as an alternative to the API key option
  ([symbol packages](https://learn.microsoft.com/en-us/nuget/create-packages/symbol-packages-snupkg)).

Acceptance criteria:

- Each snippet of the UI, with placeholders filled, runs unmodified in the acceptance suite.
- The `nuget.config` above, with `TROVE_TOKEN` set, restores a project on Linux (dotnet 10 SDK)
  and on Windows (nuget.exe and dotnet).

### R-NUGET-027 · Acceptance test suite
Priority: **MVP** · Covers: trove-only

The NuGet acceptance suite runs the client commands of this page against a running trove (two
replicas, PostgreSQL, S3-compatible storage, deployed by the Helm chart of
[ha-and-helm.md](../ha-and-helm.md)).

| Runner | Client | Scope |
|---|---|---|
| Linux | .NET 10 SDK, version pinned in the harness | every MVP criterion of this page |
| Windows | nuget.exe, version pinned in the harness, and .NET 10 SDK | push, install, delete, search, `nuget.config` with credentials |
| Linux, nightly | .NET 10 SDK | remote and symbol criteria against nuget.org and its symbol server |
| Manual, per release | Visual Studio 2022 | criteria marked manual |

- Upstream behavior in CI comes from a counting stub v3 server with recorded nuget.org responses
  (non-inlined registration pages, unlisted versions, deprecations, vulnerabilities), so CI does
  not depend on the public internet.
- Fixture packages are generated by `dotnet pack` from small projects in the test tree, plus
  hand-built archives for the malformed cases of R-NUGET-005.
- The golden vectors of R-NUGET-003 run in the unit test tier.

Acceptance criteria: the suite is green in CI for every change that touches the NuGet plug-in or
the shared request path, and the nightly job is green before a release.

### R-NUGET-028 · Audit events and metrics for NuGet
Priority: **MVP** · Covers: trove-only

Event catalog, schema and metric conventions are owned by
[operations-and-observability.md](../operations-and-observability.md) (INV-16). NuGet emits:

| Operation | Audit event |
|---|---|
| Push accepted | `package.published` and `artifact.deployed` for the `.nupkg` |
| Symbol push accepted | `artifact.deployed` for the `.snupkg` |
| Unlist, relist | `package.metadata_changed` with `change` = `unlist` or `relist` |
| Hard delete | `package.deleted` and `artifact.deleted` |
| Reindex started | `system.task_triggered` |
| Refused push, delete or relist | the authentication or `authz.denied` events of the owning pages |

- Handler names for the HTTP metrics: `nuget.index`, `nuget.flatcontainer`, `nuget.registration`,
  `nuget.search`, `nuget.autocomplete`, `nuget.push`, `nuget.delete`, `nuget.relist`,
  `nuget.symbolpush`, `nuget.symbols`, `nuget.vulnerabilities`.
- Format metrics (# example names): `trove_nuget_virtual_member_skipped_total{repository,reason}`,
  `trove_nuget_remote_unparseable_packages_total{repository}`,
  `trove_nuget_search_forward_failures_total{repository}`.

Acceptance criteria:

- After a push, an unlist and a refused push, the audit API returns the events of the table with
  the right actors and outcomes.
- The handler names appear in `trove_http_requests_total` after one restore through `nuget-all`.

### R-NUGET-029 · Upstream package digests from catalog leaves
Priority: **P1** · Covers: RMT-022

nuget.org's registration entries point (through `catalogEntry.@id` in the stored upstream
document) at catalog leaves that carry `packageHash` and `packageHashAlgorithm` for the package
([catalog](https://learn.microsoft.com/en-us/nuget/api/catalog-resource)).

- On a cache miss for a `.nupkg`, the plug-in fetches that leaf when its host is the upstream
  origin or in `extraHosts`, and supplies the decoded hash (SHA-512) as the announced digest to
  R-REPO-041. The leaf is cached as an immutable artifact under
  `{lower_id}/{lower_version}/.catalog-leaf.json`.
- An upstream without catalog leaves, or a leaf without hash, supplies no digest; the policy of
  R-REPO-041 decides (`verify-if-available` accepts, `require` refuses with 502
  `upstream_digest_missing`). With this requirement in place, `checksumPolicy: require` is allowed
  on NuGet remotes.
- Whether the leaf hash always matches the repository-signed package that nuget.org serves was
  not verified; the nightly job settles it before the requirement ships.

Acceptance criteria:

- A stub upstream serving a `.nupkg` whose bytes differ from its leaf's `packageHash`: `dotnet
  restore` fails, no item exists afterwards, and the integrity failure counter of R-REPO-041
  increments.
- In the nightly job, restoring 50 popular nuget.org packages through a remote with
  `checksumPolicy: require` succeeds.

## Deferred

| Catalog ID | Priority | Note |
|---|---|---|
| PKG-NUGET-004 | P2 | NuGet v2 (OData) and v1 feed at the base address; until then v2 requests answer 404 with a hint (R-NUGET-001). |
| PKG-NUGET-010 | P2 | System-wide switch for normalizing new repositories; not applicable, every NuGet repository is normalized (R-NUGET-003). |
| PKG-NUGET-012 | P2 | Custom layouts and pushes into folders. |
| PKG-NUGET-013 | P2 | Normalization migration; not applicable to trove repositories. Importing non-normalized content from another system belongs to an import tool. |
| PKG-NUGET-015 | P2 | Maximum unique snapshots on NuGet repositories; retention belongs to the retention policies of [storage.md](../storage.md). |
| PKG-NUGET-025 | P2 | Dedicated mode for a remote whose upstream is another instance; a plain v3 remote already works (R-NUGET-014), only the upstream state check is deferred. |
| PKG-NUGET-037 | P2 | PowerShell package management (needs v2 for the classic module). |
| PKG-NUGET-038 | P2 | Chocolatey (recommended against v2). |
| PKG-NUGET-039 | P2 | Companion command-line client wrapping nuget and dotnet; not planned. |
| PKG-NUGET-040 | Later | Steering clients to the newest version that passes curation policies. |
| PKG-NUGET-052 | P2 | Repository signatures resource and repository signing (H-36). |
| PKG-NUGET-053 | P2 | Catalog resource for mirrors and indexers (trove consumes upstream leaves in R-NUGET-029 but offers none). |
| PKG-NUGET-054 | P2 | URI template resources; the README template is pulled into R-NUGET-024 (P1). |
| PKG-NUGET-056 | Later | Federated NuGet repositories (INV-5). |

## Security notes

- **No cookie on state-changing NuGet requests.** Relist is a `POST`, which a cross-site form can
  send. Because the session cookie is accepted on NuGet endpoints for GET and HEAD only and never
  on API-key endpoints (R-NUGET-012), a page in a logged-in user's browser cannot relist, unlist
  or push on the user's behalf.
- **Package-derived content.** `.nuspec`, README, license and icon responses come from untrusted
  package authors and are served from trove's origin. They carry `X-Content-Type-Options:
  nosniff` and `Content-Security-Policy: sandbox; default-src 'none'`; icons are served only as
  `image/png` or `image/jpeg` after a content check. An XML document can run script in a browser
  through XHTML-namespaced elements, which the sandbox prevents. Whether the UI and package
  endpoints share an origin is decided in [architecture.md](../architecture.md).
- **Parser hardening.** `.nuspec` parsing rejects document type declarations and expands no
  entities; archive limits bound entries, total size and compression ratio; entry names are never
  used as filesystem paths (R-NUGET-005).
- **Unauthorized spooling.** A caller without any `deploy` grant on the repository is refused
  before the body is read (R-NUGET-005 step 5).
- **Anonymous reads hide identities.** With anonymous read granted and `requireAuthentication`
  off, clients never send their configured credentials for reads, so downloads are attributed to
  the anonymous principal (R-NUGET-012). Repositories whose usage must be attributable set
  `requireAuthentication`.
- **Dependency confusion.** A virtual repository that merges local packages with nuget.org would
  offer a public package with an internal ID and a higher version. Pin rules and namespace
  claims (R-REPO-023) apply to NuGet IDs case-insensitively (R-NUGET-017,
  R-NUGET-018); client setup recommends one virtual source with `<clear />` (R-NUGET-026). The
  remaining exposure (names that are neither published locally nor pinned nor reserved) is
  tracked where the mechanism lives, in [authorization.md](../authorization.md) and
  [repositories-and-proxy.md](../repositories-and-proxy.md).
- **Search terms leave the network.** With `forwardSearch` on, search terms typed by users go to
  the upstream (R-NUGET-016). Installations for which internal names are sensitive turn it off.
- **API keys are full tokens.** The value in `X-NuGet-ApiKey` is a trove token; client setup
  recommends tokens limited to `deploy` on one repository and short-lived CI tokens
  ([gitlab-ci-oidc.md](../gitlab-ci-oidc.md)). `nuget.config` encrypts stored secrets only on
  Windows; elsewhere the instructions use environment-variable references.
- **Symbol files can disclose source.** PDBs may contain source paths and embedded source; they
  are protected by the same `read` permission as the package (R-NUGET-013).

## Open questions

### Q-NUGET-1 · Default meaning of a NuGet delete

Options: (a) unlist by default, hard delete as `deleteMode: delete` and through raw or management
deletes; (b) hard delete by default; (c) unlist, then purge automatically after a retention period.
Recommendation: (a). Projects restoring from lock files or exact versions keep working after an
unlist, which is the protocol's own default reading; a real removal stays one setting or one
management call away for whoever holds `delete`, and (c) belongs to the retention policies.

### Q-NUGET-2 · Metadata cache period of NuGet remotes

Options: (a) keep the NuGet format default of 7200 s that R-REPO-024 lists; (b) register 300 s as
the NuGet format default for the service index, version lists and registrations; (c) no caching,
with a conditional request on every use. Recommendation: (b). A restore asks for the version list
first, so a version released upstream stays unresolvable for the whole period; two hours make CI
fail on freshly bumped dependencies (an automated update pull request opened minutes after the
release), while 300 s keeps that window short and nuget.org's CDN absorbs the refreshes. (c) adds
an upstream round trip to every package of every restore.

### Q-NUGET-3 · Non-ASCII package IDs

Options: (a) ASCII-only IDs for pushes to local repositories, upstream IDs passed through as given;
(b) NuGet's full ID rule, whose word characters include Unicode letters, with lowercasing that
matches .NET's invariant culture exactly. Recommendation: (a). Lowercasing is part of every URL, of
duplicate detection and of permission matching; Go's and .NET's Unicode case tables differ in
corner cases, and an ID that lowercases differently on client and server is unreachable or matches
the wrong grant. Non-ASCII IDs are rare in internal feeds; (b) can follow with a dedicated
conformance test if a user needs it.

### Q-NUGET-4 · Forwarding search to the upstream

Options: (a) forward by default, switchable per remote; (b) search only cached packages by
default. Recommendation: (a). Visual Studio's browse tab and `dotnet package search` are how
developers discover public packages, and a cache-only search shows only what someone already
restored; installations that must not disclose search terms switch it off per remote.

### Q-NUGET-5 · ID casing on later pushes

Options: (a) accept any casing and keep the casing of the first version as the package's display
name; (b) reject pushes whose ID casing differs from the stored one with 409; (c) accept and let the
latest push define the display name. Recommendation: (a). NuGet treats IDs case-insensitively, so
(b) blocks a legitimate casing correction for no protocol reason, while (c) makes search results
flip casing with every push; each version still keeps its own casing in its catalog entry.

### Q-NUGET-6 · Overwriting an existing version

Options: (a) versions are immutable, a repeated push always answers 409; (b) an opt-in
per-repository setting that lets callers with `overwrite` (INV-9) replace a version.
Recommendation: (a). NuGet clients cache packages by ID and version in the global packages folder
and lock files record content hashes, so a replaced package silently diverges between machines or
fails restores; Microsoft's implementation guide assumes one ID and version always stand for the
same content. Development feeds get unique versions from pre-release counters instead. INV-9's
`overwrite` action then has no effect on NuGet repositories, like on npm.

### Q-NUGET-7 · Patterns and repository-level upstream documents

Options: (a) the format plug-in may mark documents that name no package (NuGet's upstream service
index and vulnerability data) as repository-level, and include and exclude patterns do not apply
to them; (b) patterns apply to every canonical path, and administrators who restrict a NuGet
remote with include patterns must also include `.nuget/**` and `.vulnerabilities/**`.
Recommendation: (a). Patterns exist to keep package names away from upstreams; these documents
reveal none, and under (b) a remote restricted to `newtonsoft.*/**` silently stops working because
it can no longer read its upstream's service index. The rule needs confirmation in R-REPO-005 of
[repositories-and-proxy.md](../repositories-and-proxy.md), which states that patterns apply to
every upstream fetch.

## What this does not cover

### H-35 · Cached NuGet packages are not checked against an upstream digest

Until R-NUGET-029 (P1) ships, and afterwards for upstreams without catalog leaves, a remote
repository stores the `.nupkg` bytes the upstream sends over TLS without comparing them with any
announced digest: NuGet v3 package metadata carries none. A compromised upstream or upstream CDN
therefore poisons the cache for every consumer, and R-REPO-041's rule that keeps cached artifacts
immutable cannot detect a changed package either. Clients that enforce signature validation
(`signatureValidationMode="require"` with trusted repository certificates) or lock files with
content hashes still detect the change on their side.

### H-36 · Package signatures are neither verified nor produced

trove keeps author and repository signatures inside packages untouched but does not verify them
on push or on caching, and it offers no repository signing and no repository signatures resource
(PKG-NUGET-052, P2). A local repository accepts an unsigned or wrongly signed package from anyone
who passes `deploy` and ownership for the ID; trust decisions about signers rest entirely on client
configuration.
