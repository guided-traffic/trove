# NuGet

Status: specification — not implemented

This page specifies how trove serves NuGet packages: the NuGet v3 server API for local, remote
and virtual repositories under `/api/nuget/<repoKey>/`, package ingestion and `.nuspec` parsing,
version normalization and SemVer 2.0.0 handling, the NuGet additions to the storage schema, push,
unlist, relist and delete, symbol packages and the symbol server, proxying nuget.org with URL
rewriting, merging of members in virtual repositories, the NuGet side of client authentication and
authorization, client compatibility, NuGet on interchangeable replicas with and without Valkey,
pushes across drains and retries, NuGet data in backups and restores, and the acceptance tests.
NuGet v2 (OData) is deferred (INV-14). Mechanisms shared by all formats are owned by other pages and
only refined here: repository model, virtual resolution and the pull-through proxy by
[repositories-and-proxy.md](../repositories-and-proxy.md); items, blobs, uploads and the trash can
by [storage.md](../storage.md); credentials by
[identity-and-authentication.md](../identity-and-authentication.md); permissions and package
ownership by [authorization.md](../authorization.md); audit and metrics by
[operations-and-observability.md](../operations-and-observability.md); roles, caches, Valkey and
the request lifecycle by [architecture.md](../architecture.md); the backup format and the restore
procedure by [backup-and-restore.md](../backup-and-restore.md).

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
default upstream URL `https://api.nuget.org/v3/index.json`, with the proposed extra host
`{"pattern": "*.nuget.org", "sendCredentials": false}` for it (R-NUGET-014); default metadata
period 300 s and not-found period 1800 s (R-NUGET-015); pattern subject `name` (R-REPO-005);
priority members (R-REPO-022), pin rules and namespace claims (R-REPO-023) supported; package
name = package ID, lowercased as R-NUGET-003 defines `lower_id`; package-document path
`{lower_id}/index.json`, the flat-container version list, whose existence upstream stands for the
ID's existence (used by the gone rule of R-REPO-029 and the shadowing probe of R-REPO-048);
request descriptors as in R-NUGET-015, where symbol files carry a routing name derived from the file
name in the symbol key.

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
- Rendered documents (the service index, the flat-container version list and the registration
  documents) carry a weak `ETag`, `W/"…"` (Q-NUGET-9); `If-None-Match` with a matching value
  answers 304, because it uses the weak comparison
  ([RFC 9110, section 13.1.2](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.2)). The value
  covers the NuGet plug-in's render version and the document's inputs:
  - from local rows: the package's `packages.rev_seq` ([architecture.md](../architecture.md)
    R-ARCH-009), the hive and `<base>`;
  - for remote documents: the stored upstream document's digest and the serving address
    (R-REPO-043);
  - through a virtual repository: the fingerprint of the members that contribute to the ID
    (R-REPO-020's fingerprint, taken after permission filtering and after the pin rules and claims
    of R-NUGET-017), the `rev_seq` of every eligible local member and the stored digest of every
    contributing remote document. A push to a local member therefore changes the virtual
    document's ETag, and so does a claim or pin rule that drops a member for the ID without any
    push;
  - for the service index: the repository's configuration version and `<base>`; from P1, for a
    remote or virtual repository also the stored digests of the upstream service indexes and the
    member fingerprint that decide whether the vulnerability entry is listed (R-NUGET-023).

  The same value is sent with and without gzip content coding, and replicas of two releases send
  the same value for documents built from the same inputs; RFC 9110 calls such a shared validator
  weak ([section 8.8.1](https://www.rfc-editor.org/rfc/rfc9110#section-8.8.1)). The gzip hives
  answer with `Vary: Accept-Encoding`. A release that must make clients drop documents they cached
  raises the render version. Package files and other stored files keep the strong ETag of
  [storage.md](../storage.md) (R-STOR-012), which `Range` with `If-Range` needs. Search and
  autocomplete responses carry no `ETag`.
- Status codes follow the protocol: 200, 201, 204, 304, 400 invalid parameters or package, 401
  missing or invalid credentials, 403 or 404 per [authorization.md](../authorization.md)
  (R-AUTHZ-016), 405 operation not offered by this repository class, 409 conflict, 413 too large,
  502 or 503 for upstream and storage failures as [repositories-and-proxy.md](../repositories-and-proxy.md)
  and [storage.md](../storage.md) define them, 507 for a reached quota or capacity limit
  ([storage.md](../storage.md) R-STOR-029, P1).
- `Retry-After` on a NuGet protocol endpoint is at most 30 s (proposed), whatever value the owning
  page computes. The NuGet clients wait for the `Retry-After` of any
  answer before their next attempt, and that wait counts against the one `--timeout` budget of the
  whole command (R-NUGET-032). A longer value would spend a push's budget on waiting, or stall a
  restore for minutes, before the client's last attempt.
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
  answers 304, and the ETag starts with `W/`.
- `curl -I` on every read resource returns the same `Content-Type`, `ETag` and `Content-Length`
  as the corresponding GET, on any replica of the same release (R-NUGET-030).
- After a push of a new version of `Acme.Widgets` to `nuget-local`, a conditional request to
  `nuget-all`'s registration index with the ETag obtained before the push answers 200.
- `Contoso.Util` comes only from `nuget-org` through `nuget-all`. After a claim on `Contoso.*` is
  created for `nuget-local` through the management API, with no push, a conditional request with
  the earlier ETag of `nuget-all`'s version list for `contoso.util` answers 404 (R-NUGET-017), not
  304. With a pin rule that sends `Contoso.*` to `nuget-org` and a local copy of `Contoso.Util`
  2.0.0, adding `nuget-local` to the rule changes the ETag of the version list.

### R-NUGET-003 · Package IDs, versions and normalization
Priority: **MVP** · Covers: PKG-NUGET-008, PKG-NUGET-009, PKG-NUGET-011, PKG-NUGET-042, REPO-030, REPO-031

Package IDs:

- **Local rule.** An ID that enters a local repository (push, raw deploy, upload, copy or move)
  MUST match `^[A-Za-z0-9_]+([.-][A-Za-z0-9_]+)*$` and be at most 100 characters; violations
  answer 400 (Q-NUGET-3). This is NuGet's general ID rule intersected with the restricted ASCII
  set that nuget.org has required for new IDs since 2026-06-15 and for every push since
  2026-07-15, and that `dotnet pack` warns about from .NET 11 on
  ([NU5052](https://learn.microsoft.com/en-us/dotnet/core/compatibility/sdk/11/nuget-pack-nu5052-packageid),
  [announcement](https://github.com/NuGet/Announcements/issues/75)). The announcement's regular
  expression admits `_` only as the first character, while the NU5052 page admits it anywhere;
  trove follows NU5052, because `_` is ASCII and raises no case-mapping question, and refusing it
  would reject IDs the SDK packs without a warning.
- Dependency IDs inside a `.nuspec` follow the upstream rule below, not the local rule, because a
  local package may depend on a legacy upstream ID. Where trove would have to lowercase a
  non-ASCII dependency ID to build a URL, it omits that optional URL instead (R-NUGET-009).
- **Upstream rule.** An ID of a package cached by a remote repository (R-NUGET-015) MUST match
  NuGet's general rule `^\w+([.-]\w+)*$`, where a word character is one of the Unicode categories
  L, Mn, Nd or Pc (the .NET definition of `\w`; Go's `regexp` `\w` is ASCII-only and MUST NOT be
  used for it), and be at most 100 characters. This keeps the legacy IDs restorable that nuget.org
  still serves outside the ASCII set (about 119 IDs per the announcement above).
- IDs are case-insensitive. The canonical package name is `lower_id`. For an ID under the local
  rule it is the ASCII-lowercased ID, which equals .NET's `ToLowerInvariant` for ASCII. For a
  cached upstream package it is the ID segment of the request path that filled the cache,
  percent-decoded as UTF-8 with only its ASCII letters lowercased: for non-ASCII characters this is
  the client's own lowercasing, which the upstream accepted. The declared ID MUST equal it under
  Unicode simple case folding (R-NUGET-015). trove never lowercases a non-ASCII ID itself, so no ID
  can map to different URLs on client and server. A cached package whose ID violates the local rule
  cannot be copied or moved into a local repository (400). [authorization.md](../authorization.md)
  compares name patterns against `lower_id`.
- The package's display name is the casing of the version that created the `packages` row; it does
  not change on later pushes in other casing, which are accepted (Q-NUGET-5). Every version keeps
  the casing of its own `.nuspec` in its catalog entry.

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
- A zero fourth part and build metadata are both dropped from the URL form, whatever their
  combination: `2.5.1.0+sha.9f2c` has the URL form `2.5.1`, is stored at
  `{lower_id}/2.5.1/{lower_id}.2.5.1.nupkg` and appears as `2.5.1+sha.9f2c` in catalog entries and
  search. No stored path or file name keeps a zero fourth part or build metadata; the original
  string survives only in `nuget_version.original_version`.
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
| `2.6.1.0+sha.9f2c` | `2.6.1` | `2.6.1` | 409 |
| `1.0.0-Beta.2` | `1.0.0-beta.2` | `1.0.0-BETA.2` | 409 |
| `1.0.0-beta` | `1.0.0-beta` | `1.0.0-beta.1` | 201 |

- The flat container list of a package holding `1.0.0`, `1.0.0-rc.10`, `1.0.0-rc.2`,
  `1.0.0-alpha`, `1.0.0.1` and `0.9.0` is
  `["0.9.0","1.0.0-alpha","1.0.0-rc.2","1.0.0-rc.10","1.0.0","1.0.0.1"]`.
- `curl <feed>/flatcontainer/ACME.Widgets/1.2.3.0/acme.widgets.1.2.3.nupkg` returns the same bytes
  as the lowercase canonical URL.
- After pushing `Acme.Widgets` `2.6.1.0+sha.9f2c`, `/files/nuget-local/acme.widgets/2.6.1/acme.widgets.2.6.1.nupkg`
  exists and the SemVer 2 registration leaf shows `"version": "2.6.1+sha.9f2c"`.
- Pushing a package with ID `Müller.Logging` or `Acme..Widgets` answers 400. A stub upstream
  serving `Müller.Logging` 1.0.0 lets `dotnet restore` through `nuget-org` restore it, and its
  `lower_id` is `müller.logging`.
- A push of `ACME.Widgets` 1.2.4 after `Acme.Widgets` 1.2.3 succeeds; search still shows
  `Acme.Widgets`, and the 1.2.4 catalog entry shows `"id": "ACME.Widgets"`.
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
4. Validate `id` (R-NUGET-003: the local rule for content entering a local repository, the
   upstream rule for remote cache fills), `version`, every dependency ID (upstream rule) and every
   dependency range. On the package pipeline a
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
   the NuGet side rows below; increment `packages.rev_seq` (R-ARCH-009). After the commit, the
   early-eviction signal of R-REPO-020 goes to the post-commit coalescer of
   [architecture.md](../architecture.md) R-ARCH-018. It travels through Valkey, is best-effort, and
   nothing below waits for it.

- The `.nupkg` is stored byte for byte as received; trove never rewrites a package, because
  package signatures and lock-file hashes would fail on the client
  ([implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)). A
  `.signature.p7s` entry is kept and not verified (H-36).
- Metadata is complete when the ingestion request returns: a client that pushes and immediately
  restores or searches sees the new version, on any replica and whether or not Valkey is
  available (R-ARCH-012 in [architecture.md](../architecture.md), R-NUGET-030). Every metadata
  request reads the current `rev_seq`, and search runs in SQL per request, so no replica can keep
  serving a document from before the push. There is no asynchronous metadata calculation for NuGet;
  every protocol document is rendered from the rows below, and the generic `packages.rev_seq`
  (R-ARCH-009) is the package's change sequence that keys R-ARCH-012's rendition cache and the
  ETags of R-NUGET-002. NuGet keeps no counter of its own: the core reads `rev_seq` without format
  code, also for the merged-metadata check of virtual repositories (R-ARCH-015).
- Raw deploy (R-STOR-007): `PUT /files/<repoKey>/{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg`
  runs this pipeline; a declared ID or version that does not match the path answers 400
  (R-AUTHZ-005). A `.snupkg` at its canonical path runs R-NUGET-020. Every other path or file type
  answers 409 with a reason (R-STOR-007), including the derived files, which only the plug-in
  writes.
- Upload through the management API without a target path (the UI deploy dialog,
  [api-and-ui.md](../api-and-ui.md)): the plug-in derives the canonical path from the `.nuspec`.
- Copy and move (R-STOR-019, P1): the plug-in hooks copy or move the NuGet side rows in the same
  transaction. A version that the target holds with other bytes, or that is tombstoned there
  (R-NUGET-006), is refused as a per-item conflict with the code `nuget_version_exists` or
  `nuget_version_deleted`, whatever `overwrite` says; a version the target holds with identical
  bytes counts as `unchanged`. Sources may be NuGet local repositories and remote caches; a target
  local repository applies the local ID rule of R-NUGET-003. A move removes the version from its
  source repository and therefore writes a tombstone there (R-NUGET-006), because clients of the
  source may have cached the version.
- Raw delete (R-STOR-018) of a `.nupkg` removes the whole version: the `OnItemsRemoved` hook removes
  the derived items, the symbol package and the side rows and writes the version's tombstone
  (R-NUGET-006) in the same transaction; with the trash can enabled (R-STOR-022) the version is
  trashed and restored as a unit, and the `OnItemsRestored` hook removes the tombstone. A raw
  delete of a derived item alone answers 409.

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

Include and exclude patterns, claims and pin rules are name patterns matched against `lower_id`
(R-REPO-005, R-REPO-023), so `Acme.*` covers a package's files and metadata alike. Every
package path starts with `{lower_id}/`, so raw file access and listings group a package the same
way. Paths that name no package (`.nuget/`, `.vulnerabilities/`, `.symbols/` in remote caches)
follow R-NUGET-015.

Data model. `repositories`, `packages`, `package_versions` and `items` are the core tables of
R-STOR-003 in [storage.md](../storage.md). NuGet fills them as follows and adds side tables:

| Core column | NuGet value |
|---|---|
| `packages.name` | `lower_id` |
| `packages.display_name` | ID casing of the first stored version |
| `packages.rev_seq` | incremented in every transaction that changes the package's core or NuGet side rows: ingestion, unlist, relist, hard delete, trash restore, copy, move, remote cache fill, reindex (R-ARCH-009, R-ARCH-012) |
| `package_versions.version` | URL form |
| `package_versions.display_version` | full normalized version |
| `package_versions.lead_item_id` | the `.nupkg` item |

```sql
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
| `PUT <feed>/package` | `multipart/form-data`; the first part is the raw `.nupkg`; further parts, part names and part headers are ignored | 201 pushed, or the identical package was already stored (empty body); 400 invalid package; 401; 403 (including `deny.ownership` and `shadows_remote_package`); 409 `nuget_version_exists`, `nuget_version_deleted` or `nuget_version_reserved` (URL form compared); 413 too large; 503 `shadow_check_unavailable`; 507 Insufficient Storage `quota_exceeded` when a repository quota or the system capacity limit is reached ([storage.md](../storage.md) R-STOR-029, P1) | `deploy` on the ID, ownership (R-AUTHZ-007, R-AUTHZ-018) |

- The request runs the ingestion pipeline of R-NUGET-005. A body that is not
  `multipart/form-data` MAY be accepted as the raw `.nupkg`, for scripted uploads with `curl`.
- This is an API-key endpoint (R-NUGET-012).
- Versions are immutable for the lifetime of the repository (Q-NUGET-6). These rules hold
  whatever permissions the caller holds, `overwrite` included (R-AUTHZ-006 lets a format refuse
  overwrites), and for every ingestion path into a local repository (protocol push, raw deploy,
  upload; copy and move report them per item, R-NUGET-005):
  - A push whose URL form exists in the repository with other bytes answers 409
    `nuget_version_exists`.
  - A protocol push (`PUT <feed>/package`) of the identical `.nupkg`, compared by SHA-256 with the
    stored lead item, answers 201 and changes nothing: no row is written, `rev_seq` stays, no
    `package.published` event is written, and `trove_nuget_push_replayed_total` counts it
    (Q-NUGET-6). The NuGet clients repeat a push on their own after a transport failure
    (R-NUGET-032), and this rule makes such a repeat succeed when its first attempt had committed.
    Raw deploy and upload keep answering 409 for an existing version.
  - A push whose URL form is tombstoned answers 409 `nuget_version_deleted`, identical bytes
    included. The one exception is a tombstone of kind `reserved`, which a restore's journal
    replay writes for a version published after the backup's snapshot (R-NUGET-033, Q-NUGET-10):
    it admits the `.nupkg` whose SHA-512 it records, removes itself in that push's commit
    transaction, and answers 409 `nuget_version_reserved` to any other bytes.

  The NuGet clients' skip-duplicate option (`--skip-duplicate`, `-SkipDuplicate`) relies on 409;
  it also skips a tombstoned version, whose restore then fails with 404.
- **Tombstones.** Every removal of a version from a local repository writes a `nuget_tombstone`
  row with the SHA-512 of the removed `.nupkg`, in the transaction that removes the version: hard
  delete through the protocol (R-NUGET-007), raw and management deletes (R-NUGET-005), a move to
  another repository (R-NUGET-005), cleanup policies and repository caps
  ([storage.md](../storage.md), P1). Unlisting and evicting a remote cache entry write none.
  Tombstones survive the trash can and its purge. Restoring the version from the trash can
  (R-STOR-023) removes its tombstone in the restore transaction, because the original bytes come
  back. No API, UI or `trove admin` operation removes a tombstone, and a push of identical bytes is
  refused like any other; only that restore, the identical push against a `reserved` tombstone
  (above) and the deletion of the whole repository remove one, as for npm (R-NPM-008). Every
  backup exports the tombstones (R-NUGET-033).
- **Shadowing check** ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-048,
  Q-REPO-6). The first push of an ID into a local repository that holds no version of it and no
  claim covering it runs the check, whether the push is addressed to the repository or to a
  virtual repository that deploys into it. When a remote member of a containing virtual
  repository serves the ID, a caller without `manage` on the ID (or `content:bypass`) gets 403
  `shadows_remote_package`; when no member serves it but at least one cannot be asked, 503
  `shadow_check_unavailable` with `Retry-After`. Nothing is stored in either case. The path the
  check looks up and probes is the ID's package document, the flat-container version list
  `{lower_id}/index.json` (R-NUGET-015). `PUT <feed>/package` answers with these codes, and
  `dotnet nuget push` and `nuget push` report them as status code and reason phrase
  (`403 (Forbidden)`, `503 (Service Unavailable)`), because a failed push prints no response body;
  the code is in the one-line body of R-NUGET-002 for `curl` and in the audit event
  (`deny.shadowing`). A symbol push never runs the check, because it needs the version to exist
  already (R-NUGET-020).
- Concurrent pushes of the same version, on the same or different replicas, create exactly one
  version (unique index in the commit transaction): the first to commit answers 201, later pushes
  of other bytes answer 409, and later protocol pushes of the same bytes answer 201 without change.
  Pushes and deletions first create or lock the package's `packages` row (`FOR UPDATE`) and check
  versions and tombstones only after that, so a push racing a hard delete of the same version sees
  either the version or its tombstone, never neither. The rule holds whichever replica each request
  reaches, because nothing in it is replica state (R-NUGET-030).
- The row lock and the version and tombstone checks run inside the commit transaction of R-STOR-005,
  after the body has been spooled and the blob written. No database connection or lock is held
  while the body streams, the shadowing check asks an upstream or the blob goes to object storage
  ([architecture.md](../architecture.md) R-ARCH-019). A push of other bytes to an existing version
  therefore spools and stores its body before it gets its 409. An object written for it has no
  committed blob row; it is an orphan, which the orphan sweep removes from P1 on
  ([storage.md](../storage.md) R-STOR-026).
- A push to a remote repository answers 405. A push to a virtual repository is R-NUGET-019.

Data model:

```sql
CREATE TABLE nuget_tombstone (           -- outlives the trash can and its purge; never purged
    repository_id bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    lower_id      text        NOT NULL,
    lower_version text        NOT NULL,  -- URL form (R-NUGET-003)
    sha512        bytea       NOT NULL,  -- of the removed .nupkg; for 'reserved' the journaled one
    deleted_at    timestamptz NOT NULL,
    deleted_by    uuid        NOT NULL,  -- for 'reserved' the system principal of the restore
    kind          text        NOT NULL DEFAULT 'removed'
                  CHECK (kind IN ('removed', 'reserved')),  -- 'reserved': R-NUGET-033, Q-NUGET-10
    PRIMARY KEY (repository_id, lower_id, lower_version)
);
```

Acceptance criteria:

- `dotnet nuget push out/Acme.Widgets.1.2.3.nupkg --source trove-local --api-key "$TOKEN"` (dotnet
  10 SDK) succeeds; the same command with the same file again exits 0 and changes nothing (one
  version row, one `package.published` event); a rebuilt `Acme.Widgets.1.2.3.nupkg` with other
  bytes fails with 409, and with `--skip-duplicate` it exits 0.
- `nuget push Acme.Widgets.1.2.4.nupkg -Source trove-local -ApiKey %TOKEN%` (nuget.exe) succeeds.
- Ten parallel `curl -X PUT -F package=@Acme.Widgets.2.0.0.nupkg` requests of one file, spread
  across two replicas, all answer 201 and leave one version row and one `package.published`
  event. Ten parallel pushes of ten different builds of `Acme.Widgets` 2.0.0 yield one 201 and nine
  409.
- `curl -X PUT … <base>/api/nuget/nuget-org/v3/package` answers 405.
- After a hard delete of `Acme.Widgets` 1.2.3 (R-NUGET-007), pushing a rebuilt 1.2.3 answers 409
  `nuget_version_deleted`, also for a token holding `overwrite` and also after the trash can was
  purged; pushing 1.2.5 succeeds. With the trash can enabled, restoring the deleted version brings
  back the original SHA-512 and removes the tombstone.
- After moving `Acme.Widgets` 1.2.6 from `nuget-local` to another local repository (P1), pushing a
  rebuilt 1.2.6 to `nuget-local` answers 409 `nuget_version_deleted`.
- `nuget-all` = [`nuget-local`, `nuget-org`], the stub upstream serves `newtonsoft.json`, and no
  claim covers it: `dotnet nuget push Newtonsoft.Json.99.0.0.nupkg --source trove-local --api-key
  "$TOKEN"` with a token holding `deploy` but not `manage` fails with `403 (Forbidden)`; the same
  file sent with `curl -X PUT` answers 403 with a body starting `shadows_remote_package:`; nothing
  is stored and no claim is created. While the circuit of `nuget-org` is open and nothing is stored
  for `acme.brand.new`, its first push with that token answers 503 `shadow_check_unavailable`.

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
- `deleteMode` decides only what the protocol `DELETE` does (Q-NUGET-1): `unlist` (the default)
  or `delete`. Unlisting an already unlisted version answers 204 again.
- Hard delete (protocol `DELETE` with `deleteMode: delete`; a delete through the management API or
  the UI; a raw delete of the `.nupkg`, R-NUGET-005) removes or trashes the version as a unit per
  [storage.md](../storage.md) (R-STOR-018, R-STOR-022) and writes its tombstone (R-NUGET-006), so
  the version number can never return with other bytes. Management, UI and raw deletes are hard
  deletes whatever `deleteMode` says; they need the same `delete` permission and ownership.
  Relisting a hard-deleted version answers 404. A protocol `DELETE` of a version for which the
  repository holds a tombstone answers 204 without change, in either mode, so a delete that the
  client repeats after its first attempt had committed does not fail (R-NUGET-032). The
  permission and ownership checks of the table run first, so the 204 tells nothing to a caller
  who could not delete the version. One exception changes something: in `delete` mode, a
  `reserved` tombstone (R-NUGET-033) becomes `removed`, so the journaled bytes are refused too.
  Other `DELETE` requests for a version the repository does not hold answer 404.
- The repository settings page in the UI shows the effective `deleteMode`, so users know what
  `dotnet nuget delete` does in that repository.
- Unlist and relist increment `packages.rev_seq` (R-ARCH-009), so the next search, version list or
  registration request on any replica shows the change, through virtual repositories too
  (R-ARCH-012, R-ARCH-015). The early-eviction signal of R-REPO-020 follows the commit and is
  best-effort (R-NUGET-005 step 6).

Configuration (per local repository: the format member `nuget` of the repository document,
[repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-004; not the `nuget` section of
the system file):

```yaml
nuget:
  deleteMode: unlist         # default (proposed); unlist | delete; decided in Q-NUGET-1
```

Acceptance criteria:

- `dotnet nuget delete Acme.Widgets 1.2.3 --source trove-local --api-key "$TOKEN" --non-interactive`
  exits 0. Afterwards `dotnet package search Acme.Widgets --source trove-local --exact-match`
  shows no `1.2.3`; the flat container list still contains `1.2.3`; a project pinned to `[1.2.3]`
  still restores; the SemVer 2 registration shows `"listed": false`. Running the delete command a
  second time exits 0 as well.
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
| `dependencyGroups` | per group `targetFramework` (verbatim, omitted when absent) and `dependencies` with `id`, `range` (verbatim; omitted when the `.nuspec` gave none) and `registration` (the dependency's index URL in the same hive of the same repository; omitted, as the protocol allows, when the dependency ID is outside the ASCII local rule of R-NUGET-003) |
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
- `curl -X POST -b "__Host-trv_session=$S" <feed>/package/acme.widgets/1.2.3` answers 401.

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

- `remote.url` is the upstream's v3 service index URL. The default for a new NuGet remote is
  `https://api.nuget.org/v3/index.json`.
- On create, update and the upstream test (R-REPO-024 probes the service index), the plug-in
  requires a `PackageBaseAddress/3.0.0` resource and at least one `RegistrationsBaseUrl` type in
  the upstream index; otherwise the configuration is refused with 422 naming the missing resource.
  A v2-only upstream is refused.
- Resource discovery picks, from the upstream index: `PackageBaseAddress/3.0.0`; the registration
  hive in the order `RegistrationsBaseUrl/3.6.0`, `/3.4.0`, unversioned; `SearchQueryService/3.5.0`
  before the unversioned type; the autocomplete resource likewise; `VulnerabilityInfo/6.7.0` (P1).
  The upstream index is a metadata cache entry (R-NUGET-015); rediscovery happens when it expires.
- Discovered resources may live on other hosts than the service index. They are fetched only when
  their host is the upstream origin or matches `remote.extraHosts`; otherwise 502
  `foreign_host_denied` (R-REPO-043). `extraHosts` is the generic member of R-REPO-024 (Q-REPO-5);
  NuGet defines no host list of its own. For its default upstream the NuGet format registers the
  proposed entry `{"pattern": "*.nuget.org", "sendCredentials": false}` in the format catalog
  (R-NUGET-001, R-REPO-003), because nuget.org serves search and symbols from other `nuget.org`
  hosts.
- A trove repository on another instance is a valid upstream: its service index is a plain v3
  index, so no special mode is needed.
- The remote's own service index lists trove URLs only (R-NUGET-002); it never lists publish
  resources, and publish requests answer 405.

Configuration (per remote repository; `remote` belongs to
[repositories-and-proxy.md](../repositories-and-proxy.md) and is shown for context; the format
member `nuget` is NuGet's, R-REPO-004):

```yaml
key: nuget-org                                   # example
class: remote
format: nuget
remote:
  url: https://api.nuget.org/v3/index.json       # default (proposed) for NuGet remotes
  extraHosts:                                    # proposed by the format for nuget.org (R-REPO-003)
    - pattern: "*.nuget.org"
      sendCredentials: false
nuget:
  forwardSearch: true                            # default (proposed); R-NUGET-016, decided in Q-NUGET-4
  symbolServerUrl: https://symbols.nuget.org/download/symbols   # default (proposed); empty disables; P1
  vulnerabilityInfo: true                        # default (proposed); P1
```

Acceptance criteria:

- Creating a remote with only `remote.url` set to nuget.org's index succeeds; the upstream test
  reports it reachable.
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
| `flatcontainer/{lower_id}/index.json` | metadata; the ID's package document (R-REPO-003) | `{lower_id}/index.json` | upstream package content `{lower_id}/index.json` |
| `.nupkg` | artifact | `{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg` | upstream package content URL; when that answers 404 but the upstream registration lists the version, the leaf's `packageContent` (recorded in `remote_url_map`) |
| `.nuspec` | artifact | `{lower_id}/{lower_version}/{lower_id}.nuspec` | extracted from the cached `.nupkg`; when that is not cached, upstream package content `.nuspec` |
| any registration document | metadata | `{lower_id}/.registration/index.json` and `{lower_id}/.registration/page/{n}.json` | upstream registration index of the discovered hive, plus every non-inlined page |
| search, autocomplete | search | — | R-NUGET-016 |
| symbol file (P1) | artifact; routing name = first segment of the key (the file name the debugger looks for) without its last extension, lowercased | `.symbols/<key>` | R-NUGET-022 |
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
- Every cached `.nupkg` is parsed like an upload (R-NUGET-005 steps 1 to 4 with the upstream ID
  rule of R-NUGET-003, without the `SymbolsPackage` rejection) and recorded with `origin = 'cache'`
  rows; a package that fails parsing is still served and cached, logged and counted (R-NUGET-028),
  but gets no NuGet rows. The bytes are never altered. A cached `.nupkg` whose `.nuspec` parses but
  declares an ID that differs from the request's ID segment under Unicode simple case folding, or a
  version whose URL form differs from the canonical path, MUST be refused with 502
  `upstream_package_mismatch` and not stored. NuGet v3 announces no content digest (H-35), so the
  declared identity is the only check that the upstream served the package that was asked for.
- Patterns and package-less requests (Q-NUGET-7). Include and exclude patterns of the remote
  (R-REPO-005), namespace claims and pin rules (R-REPO-023) are name patterns matched against the
  descriptor's package name, so `excludePatterns: ["Acme.*"]` keeps internal IDs away from
  nuget.org for version lists, registrations, packages and search results alike. The upstream
  service index, the vulnerability index and pages, search and autocomplete name no package: the
  plug-in marks them repository-level, and R-REPO-005 admits them, so a remote restricted to a few
  ID prefixes still discovers its upstream. Search has its own protection (R-NUGET-016). Symbol
  files are not package-less: their descriptor carries the routing name of the table above (the
  key's first segment without its last extension, lowercased: `acme.widgets` for
  `acme.widgets.pdb/<index>/acme.widgets.pdb`, for the compressed probe
  `acme.widgets.pdb/<index>/acme.widgets.pd_` and for a binary key
  `acme.widgets.dll/<index>/acme.widgets.dll` alike), because a debugger probes every configured
  symbol location for every module it loads, internal assemblies included, and SDK projects name
  the package after the assembly by default
  ([pack target](https://learn.microsoft.com/en-us/nuget/reference/msbuild-targets#pack-target)).
  Every key shape therefore gets a routing name; none is forwarded unprotected. The routing name
  serves patterns, claims, pin rules and the gone rule below only; authorization of an upstream
  symbol file stays repository-level (R-NUGET-013).
- **Gone IDs** (R-REPO-029, Q-REPO-3). The version list `{lower_id}/index.json` is the ID's package
  document. When the upstream answers 404 or 410 for it, the ID is marked gone: the remote and its
  cache view answer 404 for every request that names the ID (the version list, every registration
  document, the cached `.nupkg` and `.nuspec`, from P1 the embedded files and the symbol files
  whose routing name is the ID), without an upstream request for those paths, and the remote
  contributes nothing of the ID to virtual repositories, so a virtual repository answers 404 unless
  another member serves it. The only upstream request for the ID is the version list: when a client
  asks for it, or when another request for the ID arrives after the version list's negative entry
  has expired or was invalidated, which then refreshes the version list first (R-REPO-025, gone
  packages). The mark clears when the upstream serves the version list again. The
  cached items stay stored, listed in the management item API and the cache view's entry list, and
  copyable into a local repository (R-REPO-032), which is how a build keeps a package the upstream
  withdrew legitimately. NuGet does not track removal per version: a version withdrawn from an ID
  that still exists upstream disappears from the refreshed version list and registrations, but its
  cached `.nupkg` stays downloadable by exact path ([repositories-and-proxy.md](../repositories-and-proxy.md)
  H-5).
- Digests (R-REPO-041): NuGet v3 package metadata announces no content digest, so in the MVP the
  plug-in supplies none; `verify-if-available` records the computed digests.
  `remote.integrity.checksumPolicy: require` on a NuGet remote is refused with 422 until
  R-NUGET-029 supplies digests (H-35).
- Metadata cache period (Q-NUGET-2): the NuGet format registers a default metadata period of
  300 s (`cache.metadataTtlSeconds`, R-REPO-024) for the upstream service index, version lists,
  registrations and vulnerability data, and keeps the not-found period at 1800 s. A refresh is a
  conditional request, collapsed within a replica and, through the Valkey flight lock, across
  replicas (R-REPO-026, R-REPO-027, [architecture.md](../architecture.md) R-ARCH-033), so an
  unchanged document costs one 304 per period per remote repository, not one per client. While
  Valkey is unavailable, the collapse works per replica, and the cost rises to one 304 per period
  per `web` replica (R-NUGET-031). Administrators may set any period in the range of R-REPO-024 per
  remote. `.nupkg`, `.nuspec`, icons and symbol files are immutable.
- Push, delete and relist are not offered (405).

Acceptance criteria (against a stub v3 upstream in CI and, in a nightly job, against nuget.org):

- `dotnet restore` of a project referencing `Newtonsoft.Json 13.0.3` through `nuget-org` succeeds;
  the `.nupkg` is cached; a second restore with the upstream unreachable succeeds.
- Every URL in `curl <base>/api/nuget/nuget-org/v3/registration-semver2/newtonsoft.json/index.json`
  except `projectUrl`, `licenseUrl` and `iconUrl` starts with `<base>/api/nuget/nuget-org/v3/`.
- For a stub upstream with 200 versions of one ID in non-inlined pages, the remote's index uses
  trove's paging and contains all 200 leaves.
- With `excludePatterns: ["Acme.*"]`,
  `curl <base>/api/nuget/nuget-org/v3/flatcontainer/acme.widgets/index.json` answers 404 and the
  stub upstream records no request. With `includePatterns: ["Newtonsoft.*"]` instead, restoring
  `Newtonsoft.Json 13.0.3` through `nuget-org` succeeds, which needs the upstream service index.
- With default settings, a version the stub upstream adds to an already cached version list is
  restorable through `nuget-org` at most 300 s (plus the refresh) later; while nothing changes,
  the stub records only conditional requests answered 304, at most one per document per 300 s
  across both replicas (Valkey available; R-NUGET-031 states the bound without it).
- 50 concurrent restores of the same uncached package across two replicas cause one upstream
  `.nupkg` request (Valkey available; at most one per replica without it, R-NUGET-031).
- The SHA-512 of a `.nupkg` served by the remote equals the one served by the upstream.
- A stub upstream that serves `Other.Pkg 1.0.0` at the content URL of `acme.widgets` 1.0.0 causes
  502 `upstream_package_mismatch`, and no item exists afterwards.
- `Acme.Gone` 1.0.0 is cached through `nuget-org`; after the metadata period the stub answers 404
  for its version list. Then `registration-semver2/acme.gone/index.json`, the `.nuspec` and the
  `.nupkg` answer 404 through `nuget-org`, `nuget-org~cache` and `nuget-all`, the stub records no
  request for any of them, and the `.nupkg` item is still listed by the management item API (the
  generic criterion is R-REPO-029's). After the stub serves the version list again and the negative
  period has passed, `dotnet restore` of `Acme.Gone 1.0.0` through `nuget-all` succeeds.

### R-NUGET-016 · Search and autocomplete through a remote repository
Priority: **MVP** · Covers: PKG-NUGET-024, PKG-NUGET-028

- `forwardSearch: true`: search requests are forwarded to the upstream's search resource with the
  same `q`, `prerelease`, `semVerLevel` and `packageType`, and with `skip` and `take` clamped to
  trove's limits (R-REPO-025 lets the plug-in map these parameters). Result URLs are rewritten as
  in R-NUGET-015 (`registration`, `versions[].@id` into the hive that matches the request's
  `semVerLevel`); `owners`, `verified`, `deprecation`, `vulnerabilities`, download counts and
  `packageTypes` are kept. Results whose ID is excluded by the remote's patterns, refused by remote
  refusal (a `global` claim, R-REPO-023), or not readable by the caller are removed, and
  `totalHits` is reduced by the number removed from the returned page. Through a virtual
  repository, R-NUGET-018 step 2 also removes IDs that its claims and pin rules keep away from this
  remote.
- **Withheld queries** (Q-NUGET-4). A query is not forwarded when one of its terms is protected;
  the remote then answers that request with its cache-only search. The plug-in splits `q` into
  terms as R-NUGET-010 does (a phrase into its words), drops a `field:` prefix and lowercases each
  term like an ID (R-NUGET-003). A term is protected when, for a package whose `lower_id` equals
  the term, an exclude pattern of the remote, a namespace claim in effect for the request or a pin
  rule in effect for the request would keep the package away from this remote (R-REPO-005,
  R-REPO-023; through a virtual repository the claims and pin rules in effect in that virtual
  repository, on the remote directly the `global` claims when remote refusal is on). A term is also
  protected when it equals the literal part of such a prefix pattern without the `*` and without a
  trailing `.`, `-` or `_` (for `Acme.*`: `acme`). Include patterns do not protect terms: they
  restrict what a remote serves, not what is sensitive. The check uses the in-memory claim trie of
  R-REPO-023 and adds no database query. Every replica keeps that trie current from the
  PostgreSQL change log (area `claims`, [architecture.md](../architecture.md) R-ARCH-018), so after a
  claim change two replicas can disagree about one query for at most that propagation bound, with
  or without Valkey. Withheld requests are counted (R-NUGET-028).
- Forwarded responses are kept in the registered in-process cache `nuget_remote_search` of each
  `web` replica (Q-NUGET-8, [architecture.md](../architecture.md) R-ARCH-018), for
  `nuget.search.remote_result_ttl` and within `nuget.search.remote_result_cache_bytes`.
  - The key is the remote repository and the upstream request URL with its parameters. The entry is
    the upstream response as received.
  - Pattern, claim, pin-rule and permission filtering and the URL rewriting run on every use, so a
    claim or grant change applies to cached results as soon as the replica has applied it. A
    change of the remote's configuration flushes its entries (area `repo_config`).
  - Nothing is persisted and nothing is written to Valkey: search results are content served to
    clients ([architecture.md](../architecture.md) Q-ARCH-16).
  - Two replicas may answer one query from copies fetched up to `remote_result_ttl` apart, and
    either may ask the upstream instead. Both answers meet the same contract, which is why this
    per-replica state does not break INV-18 (R-NUGET-030).
  - Forwarding needs `read` on the remote. It stores nothing in the repository, so the cache fill
    rule does not apply.
- When the upstream search fails or the circuit is open (R-REPO-029), the remote answers from its
  cache-only search.
- `forwardSearch: false`: the remote answers with the cache-only search, which is R-NUGET-010 over
  its `origin = 'cache'` rows.
- Autocomplete (P1) follows the same two modes.

Configuration (system file):

```yaml
nuget:
  search:
    remote_result_ttl: 60s                 # default (proposed)
    remote_result_cache_bytes: 33554432    # default (proposed), 32 MiB per web replica
```

Acceptance criteria:

- `dotnet package search Newtonsoft --source trove-org` returns `Newtonsoft.Json` with upstream
  download counts; every `registration` URL points at `nuget-org`.
- With `forwardSearch: false`, the same search returns only packages restored through the remote
  before.
- With the upstream unreachable, search answers 200 from the cache-only mode.
- `nuget-local` claims `Acme.*` (scope `members`) and holds `Acme.Widgets`. Through `nuget-all`,
  `q=acme`, `q=acme.payments` and `q=packageid:Acme.Widgets` answer 200 and the stub upstream
  records no search request; `q=newtonsoft` is forwarded. With `excludePatterns: ["Contoso.*"]` on
  `nuget-org`, `q=contoso.secret` sent to `nuget-org` directly is not forwarded either.
- `q=partner` through `nuget-all` is answered and cached on both replicas; then `nuget-local`
  claims `Partner.*`. Within 1 s, and with Valkey stopped within `cluster.epoch_poll_interval`
  plus 1 s, neither replica returns a `Partner.*` result from `nuget-org` for `q=partner`, although
  both still hold the cached upstream response.

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
- Pin rules and namespace claims (R-REPO-023) apply per ID, matched against `lower_id` and
  evaluated on stored configuration only, never on the caller's permissions:
  - For an ID that a pin rule of the virtual repository selects, only that rule's `members`
    contribute to its version list, registration, downloads and search results; a rule may name a
    remote member (a partner feed), which is the only way a claimed ID reaches a remote.
  - Otherwise, for an ID that a claim in effect in the virtual repository covers (a `members` claim
    of a local member, which `local.autoClaim: true` creates on the ID's first publish, or a
    `global` claim of any local NuGet repository), only the local members holding the most specific
    claim contribute; remote members contribute nothing and are not asked upstream, and without
    such a member the answer is 404.
  - The first publish of an ID that a remote member serves needs `manage` or a prior manual claim
    (R-REPO-048, R-NUGET-006); an auto claim is not created when another repository's claim in
    effect in a containing virtual repository covers the ID (R-REPO-023).
- A publish to a member is visible through the virtual repository at once, on every replica: the
  core's merged-metadata cache checks the `packages.rev_seq` of every eligible local member on each
  hit, notification or not ([architecture.md](../architecture.md) R-ARCH-015, Q-ARCH-9). The
  early eviction of R-REPO-020, a best-effort signal through Valkey, only drops entries sooner; a
  merged entry whose remote input was refreshed expires with that entry when the signal is lost.
  A claim that the first push creates (`local.autoClaim`) reaches the other replicas through the
  change log within the bound of R-ARCH-018. A merge without a transiently failing remote member is served
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
        members: [nuget-local]
```

Acceptance criteria:

- With `nuget-all` as the only source (`<clear />` in `nuget.config`), `dotnet restore` of a
  project referencing `Acme.Widgets 1.2.3` (local) and `Newtonsoft.Json 13.0.3` (remote) succeeds.
- `Contoso.Util 2.0.0` in `nuget-local`, pushed by a holder of `manage` on it (R-REPO-048), and
  `Contoso.Util` `2.0.0` and `2.1.0` at the stub upstream; `local.autoClaim: false` on
  `nuget-local`, no claims and no rules: the virtual version list is `["2.0.0","2.1.0"]`; the
  `2.0.0` download carries `Trove-Resolved-From: nuget-local` and its registration entry carries
  the local metadata. With `local.autoClaim: true` on `nuget-local` (the push creates the claim)
  the list is `["2.0.0"]` and the stub records no request for `contoso.util` afterwards.
- A pin rule `{"packages": ["Partner.*"], "members": ["nuget-org"]}` in `nuget-all` resolves
  `Partner.Sdk` from `nuget-org` although `nuget-local` claims `Partner.*`.
- Push through `nuget-all`, then `dotnet restore --no-http-cache` through `nuget-all` on the other
  replica within one second sees the new version, also with Valkey stopped.
- With the stub upstream down and nothing cached, local packages still restore through
  `nuget-all`, and the version list of a remote-only ID answers 502, not 404 (R-REPO-019).

### R-NUGET-018 · Virtual repository: merged search and autocomplete
Priority: **MVP** · Covers: PKG-NUGET-026, PKG-NUGET-028

Search through a virtual repository merges the result lists of the members that remain after
R-REPO-019's filtering:

1. Each member is queried in parallel with the request's filters, `skip=0` and
   `take = min(skip + take, virtualMergeWindow)`. Local members use R-NUGET-010, remote members
   R-NUGET-016, including its withheld-query rule evaluated with this virtual repository's claims
   and pin rules.
2. Results of a remote member are removed for every ID that this virtual repository's pin rules
   or claims keep away from that member (R-REPO-023, as in R-NUGET-017): an ID whose selected pin
   rule does not name the member, or, without a pin rule, an ID that a claim in effect here
   covers.
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
- A package present in `nuget-local` (`local.autoClaim: false`, pushed by a `manage` holder,
  R-REPO-048) and at the stub upstream, neither pinned nor claimed, appears once with the union of
  its versions; when a pin rule names only `nuget-local` for it, or a claim of `nuget-local` covers
  it, only with the local versions.
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
| `PUT <feed>/symbolpackage`, body as package push, carrying a `.snupkg` | 201 stored; 400 invalid symbol package; 401; 403; 404 no package with that ID and version in the repository; 409 a different symbol package for that version exists; 413; 507 Insufficient Storage `quota_exceeded` when a repository quota or the system capacity limit is reached ([storage.md](../storage.md) R-STOR-029, P1) | `deploy` on the ID, ownership |

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
- The symbol push is one request. The client sends it only after the package push has succeeded
  (R-NUGET-032), and the matching `.nupkg` is looked up in PostgreSQL, so the two pushes may reach
  different replicas (R-NUGET-030). The push extracts nothing: it validates, stores the
  `.snupkg` and commits.
- Serving PDBs to debuggers is P1 (R-NUGET-021). From then on, the symbol push's commit
  transaction enqueues the indexing job of that one symbol package (Q-NUGET-11). The release that
  ships R-NUGET-021 enqueues the same job for every symbol package stored before, through a data
  migration ([architecture.md](../architecture.md) R-ARCH-026).

Data model:

```sql
CREATE TABLE nuget_symbol_package (
    version_id  bigint PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    item_id     bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,   -- the .snupkg
    indexed_at  timestamptz,                                               -- set by R-NUGET-021
    index_error text                                                       -- R-NUGET-021: why a package could not be indexed
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

- Indexing runs as a job on the `worker` role, never in the push request (Q-NUGET-11).
  - One job per symbol package. The symbol push's commit transaction enqueues kind
    `nuget-symbol-index` on queue `index`, with dedup key `nuget-symbol-index:<version id>` in mode
    `exclusive` ([architecture.md](../architecture.md) R-ARCH-016). The jobs of one repository
    therefore spread over every `worker` replica; no repository's indexing waits for one replica.
  - The job reads the `.snupkg` from object storage with ranged reads: the central directory
    first, then each `.pdb` entry. It spools one extracted PDB at a time, with the spool
    reservation of [storage.md](../storage.md) R-STOR-004, and never the whole archive, so a symbol
    package larger than the worker's spool ([ha-and-helm.md](../ha-and-helm.md) R-HA-035) can
    still be indexed. Each `.pdb` becomes a generated item (storage layout of R-NUGET-005), and its
    key is computed and stored lowercased. For a Portable PDB the job also computes the checksum:
    SHA-256 over the file with the 20-byte PDB ID zeroed
    ([PE/COFF PDB checksum entry](https://github.com/dotnet/runtime/blob/main/docs/design/specs/PE-COFF.md)).
  - One transaction inserts the package's rows and sets `indexed_at`. It first locks the
    `nuget_symbol_package` row and skips the package when `indexed_at` is already set. A job
    released by a drain ([ha-and-helm.md](../ha-and-helm.md) R-HA-011), retried after a crash or
    started by a reindex therefore commits nothing twice. Objects written by a run that did not
    commit are orphans, removed by the orphan sweep from P1 on
    ([storage.md](../storage.md) R-STOR-026).
  - A package that cannot be indexed is not retried forever. A `.pdb` whose streams cannot be
    parsed, or one larger than the worker's spool can reserve, sets `index_error` (`unparseable`,
    `too_large`) together with `indexed_at`, is logged and counted (R-NUGET-028), and the job
    succeeds; that package's keys answer 404. Transient failures (object storage, a spool
    reservation that may succeed later) fail the attempt, and R-ARCH-016 retries it with backoff.
    A reindex (R-NUGET-025) clears `index_error` and tries again.
  - Until its symbol package is indexed, a key answers 404. Proposed bound: a key is servable
    within 30 s at p99 after the symbol push's 201, under the reference load of
    [architecture.md](../architecture.md) R-ARCH-027.
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
- After `dotnet nuget push` of a `.nupkg` with its `.snupkg`, the PDB's key answers 200 within
  30 s. With the `worker` Deployment scaled to zero, the push still answers 201 and the key answers
  404; after scaling back, the key answers 200 without another push.
- 200 symbol packages are pushed to `nuget-local` within one minute, and the node of one `worker`
  is drained while their jobs run. Every key answers 200 afterwards, no job counts the interrupted
  attempt, and no `nuget_symbol_file` row exists twice. Before the drain,
  `trove_jobs_running{kind="nuget-symbol-index"}` is above 0 on both `worker` replicas at the same
  time.
- A `.snupkg` whose `.pdb` carries a valid Portable PDB signature but a corrupt `#Pdb` stream ends
  with `index_error = 'unparseable'`; a valid symbol package pushed after it is servable within the
  bound above.
- Visual Studio 2022 with Just My Code off and `<feed>/symbols/` as symbol location steps into
  `Acme.Widgets` source when debugging a consumer (manual check, recorded per release).

### R-NUGET-022 · Symbol proxy and symbol resolution through virtual repositories
Priority: **P1** · Covers: PKG-NUGET-031, PKG-NUGET-032

- A remote repository with a non-empty `nuget.symbolServerUrl` answers
  `GET <feed>/symbols/<key>` by requesting `<symbolServerUrl>/<key>` (key lowercased) through the
  remote request path, forwarding a `SymbolChecksum` header when present; the public gallery's
  symbol server reportedly requires that header for Portable PDBs (not verified against Microsoft
  documentation). The host must be the upstream origin or in `remote.extraHosts` (R-NUGET-014). Results
  are cached as immutable artifacts under `.symbols/<key>`; a cached Portable PDB is checked
  against the request's `SymbolChecksum` as in R-NUGET-021.
- An upstream 404 enters the negative cache only for requests that carried a `SymbolChecksum`
  header or for Windows PDB keys, so a probe without header cannot hide a file from a later
  request with header.
- The descriptor's variant (R-REPO-026) is the request's `SymbolChecksum` value, normalized
  (lowercased, the checksums sorted and joined with `;`; empty without the header). Flight locks
  and negative entries, which live in Valkey under keys hashed from it
  ([architecture.md](../architecture.md) R-ARCH-030), are therefore kept apart per checksum. A miss
  under one checksum never hides the file from a request with another. A cached file is served to
  every request whose checksum it matches (R-NUGET-021).
- Patterns, claims and pin rules see the routing name of a symbol request (the key's first segment
  without its last extension, R-NUGET-015). A remote with `excludePatterns: ["Acme.*"]`, or a
  virtual repository in which `Acme.*` is claimed or pinned to local members, never asks the
  upstream symbol server for `acme.widgets.pdb` (nor for its `.pd_` probe); a remote with `includePatterns: ["Newtonsoft.*"]` serves
  `newtonsoft.json.pdb` and nothing else. An internal assembly whose name no protecting pattern
  covers is still requested upstream when a local member does not hold it (Security notes).
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
- With `Acme.*` claimed by `nuget-local`, a request through `nuget-all` for an `acme.payments.pdb`
  key that no local member holds answers 404 and the stub symbol server records no request.

### R-NUGET-023 · Vulnerability information
Priority: **P1** · Covers: PKG-NUGET-051

NuGet 6.7 and later download the
[vulnerability resource](https://learn.microsoft.com/en-us/nuget/api/vulnerability-info) during
restore and audit packages locally.

- A remote repository with `nuget.vulnerabilityInfo: true` whose upstream offers
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
  `Content-Security-Policy: sandbox; default-src 'none'` and
  `Cross-Origin-Resource-Policy: same-site`; for a package without README it returns 404.

### R-NUGET-025 · Metadata reindex
Priority: **P1** · Covers: PKG-NUGET-007, API-040, FMT-012

The operation `POST /api/v1/repositories/{key}/reindex` is owned by
[repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-016: 202 with a job reference,
one run per repository, 409 `reindex_running`, permission system administrator or `manage`). For
NuGet repositories the plug-in:

- re-parses every `.nupkg` of a local repository, or every cached `.nupkg` of a remote repository,
  and rewrites `metadata`, `search_doc`, `is_prerelease`, `is_semver2`, `package_types`,
  `declared_id` and `sort_key` of `nuget_version` in batches, keeping listing state, publish time
  and download counts. It reads each archive with ranged reads from object storage (central
  directory, `.nuspec`, embedded files) and never spools a whole package, so the worker's spool
  bounds no package size;
- creates missing NuGet rows for `.nupkg` items that have none (for example after an import);
- extracts missing embedded files (R-NUGET-024);
- enqueues the `nuget-symbol-index` job (R-NUGET-021) of every symbol package without
  `indexed_at`, and of every one with `index_error` after clearing it. It does not index symbols
  itself, so a reindex and symbol indexing never wait for each other;
- runs as a job on the `worker` role ([architecture.md](../architecture.md) R-ARCH-016), never on
  a `web` replica, and checkpoints progress per `lower_id`. A job released at a drain keeps
  `attempt` unchanged ([ha-and-helm.md](../ha-and-helm.md) R-HA-011), and a job resumed on another
  replica skips finished IDs;
- increments `packages.rev_seq` (R-ARCH-009) of every package whose rows it changed, in the batch's
  transaction, so no rendition cached under the old value is served again (R-ARCH-012);
- never changes stored bytes.

On a virtual repository the operation only drops the merged-metadata cache (R-REPO-016).

Acceptance criteria:

- After corrupting `metadata` of a fixture version in the database, the reindex restores it and
  search returns the original description.
- Killing the replica that runs the job resumes it elsewhere without reprocessing finished IDs.
- `kubectl drain` of the node that runs the job: the job is `queued` again before its pod exits,
  with `attempt` unchanged, and another `worker` finishes it from its checkpoint.

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

The NuGet acceptance suite runs the client commands of this page against a running trove in the
production shape: the local profile of [ha-and-helm.md](../ha-and-helm.md) R-HA-051, deployed by
the Helm chart.
- two `web` and two `worker` replicas on different nodes;
- PostgreSQL with a standby;
- Valkey with Sentinel, in the reference topology of R-HA-056;
- S3-compatible storage.

| Runner | Client | Scope |
|---|---|---|
| Linux | .NET 10 SDK, version pinned in the harness | every MVP criterion of this page |
| Linux, `scatter` profile ([architecture.md](../architecture.md) R-ARCH-032) | .NET 10 SDK | R-NUGET-030, on every change that touches the NuGet plug-in |
| Linux, Valkey stopped | .NET 10 SDK | R-NUGET-031, nightly |
| Linux, pooler overlay (`POOLER=1`, R-HA-051: transaction-mode pooler) | .NET 10 SDK | every MVP criterion, nightly, so no NuGet path depends on session state ([architecture.md](../architecture.md) R-ARCH-019) |
| Windows | nuget.exe, version pinned in the harness, and .NET 10 SDK | push, install, delete, search, `nuget.config` with credentials |
| Linux, nightly | .NET 10 SDK | remote and symbol criteria against nuget.org and its symbol server |
| Manual, per release | Visual Studio 2022 | criteria marked manual |

- The NuGet loads of the node-drain test ([ha-and-helm.md](../ha-and-helm.md) R-HA-059) and the
  NuGet packages of the backup suite ([backup-and-restore.md](../backup-and-restore.md) R-BAK-029)
  come from this suite's fixtures. The drain load runs, in parallel: pushes of packages with symbol
  packages; restores through `nuget-all` of local packages and of remote packages not yet cached,
  from a stub upstream that delays every response by 2 s; search walks; and, from P1, symbol
  requests. Every `dotnet` command of it exits 0 while a node of each role is drained.
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
| Push of the identical bytes of a stored version (R-NUGET-006) | none; counted in `trove_nuget_push_replayed_total` |
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
  `trove_nuget_search_forward_failures_total{repository}`,
  `trove_nuget_search_withheld_total{repository}` (R-NUGET-016),
  `trove_nuget_push_replayed_total{repository}` (R-NUGET-006) and, from P1,
  `trove_nuget_symbol_index_lag_seconds` (age of the oldest symbol package without `indexed_at`,
  R-NUGET-021) and `trove_nuget_symbol_index_failures_total{repository,reason}` (packages that
  ended with `index_error`). The forwarded-search cache reports through the core's
  `trove_cache_requests_total{cache="nuget_remote_search"}`.
- `package.published` and `package.deleted` of local NuGet repositories go to the revocation
  journal of [ha-and-helm.md](../ha-and-helm.md) R-HA-054, with the entry of R-NUGET-033. Both
  event types are shared with npm, which journals neither ([formats/npm.md](npm.md) Q-NPM-12), and
  the registry flag of R-OPS-014 is per event type. The flag on these two types therefore means
  "journaled when the producer supplies an entry": the NuGet plug-in supplies one for local
  repositories, npm supplies none, and no outbox row is written without an entry (cross-page,
  R-OPS-014, R-OPS-015).

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
  origin or in `remote.extraHosts`, and supplies the decoded hash (SHA-512) as the announced digest to
  R-REPO-041. The leaf is cached as an immutable artifact under
  `{lower_id}/{lower_version}/.catalog-leaf.json`.
- An upstream without catalog leaves, or a leaf without hash, supplies no digest; the policy of
  R-REPO-041 decides (`verify-if-available` accepts, `require` refuses with 502
  `upstream_digest_missing`). With this requirement in place,
  `remote.integrity.checksumPolicy: require` is allowed on NuGet remotes.
- Whether the leaf hash always matches the repository-signed package that nuget.org serves was
  not verified; the nightly job settles it before the requirement ships.

Acceptance criteria:

- A stub upstream serving a `.nupkg` whose bytes differ from its leaf's `packageHash`: `dotnet
  restore` fails, no item exists afterwards, and the integrity failure counter of R-REPO-041
  increments.
- In the nightly job, restoring 50 popular nuget.org packages through a remote with
  `remote.integrity.checksumPolicy: require` succeeds.

### R-NUGET-030 · NuGet on interchangeable replicas (INV-18)
Priority: **MVP** · Covers: trove-only

The Kubernetes Service spreads NuGet requests over the `web` replicas at random and without session
affinity (INV-12, [architecture.md](../architecture.md) R-ARCH-032). A restore, a push or a
debugging session is a series of independent requests. Any of them may reach any replica, including,
during a rolling upgrade, a replica of the previous release. The NuGet plug-in keeps no state between
requests on a replica:

| Flow | Consecutive requests | Where the state between them lives |
|---|---|---|
| Push, then symbol push | `PUT <feed>/package`, then `PUT <feed>/symbolpackage`, sent only after the first succeeded (R-NUGET-032) | The committed version rows; the symbol push looks the `.nupkg` up in PostgreSQL (R-NUGET-020) |
| Push, unlist, relist or delete, then restore or search | the write, then version list, registration documents or search | The committed rows and `packages.rev_seq`, which every metadata request reads (R-ARCH-012); search runs in SQL per request (R-NUGET-010) |
| The same through a virtual repository | the write through `nuget-all`, then reads through `nuget-all` | As above, plus the merged cache's `rev_seq` check of every local member (R-ARCH-015) |
| First push of an ID with `local.autoClaim` | the push, then reads of that ID through a virtual repository | The claim row, which other replicas apply from the change log within the bound of R-ARCH-018. The pushed version itself is visible at once. |
| Registration paging | the index, then page URLs | The page bounds in the URL; a page is answered from rows at request time (R-NUGET-009) |
| Search and autocomplete paging | one request per page, `skip` and `take` | None. Every page is computed again: from SQL with `lower_id` as tie-breaker (R-NUGET-010), from the upstream (R-NUGET-016), or as a fresh merge window (R-NUGET-018). |
| Removal, then a push of the same version | the delete or move, then the push | The `nuget_tombstone` row, checked under the `packages` row lock (R-NUGET-006) |
| Concurrent misses of one remote document | parallel requests on several replicas | The cache entry in PostgreSQL; the Valkey flight lock only saves upstream requests (R-NUGET-015, R-NUGET-031) |
| Credential challenge | `401`, then the same request with credentials | None; the credential travels with every request (R-NUGET-012) |
| Debugger symbol probes (P1) | one request per module and key | The `nuget_symbol_file` rows (R-NUGET-021) |
| Reindex and symbol indexing (P1) | jobs | The job row and its `checkpoint`, or `indexed_at` (R-NUGET-025, R-NUGET-021) |

- `X-NuGet-Session-Id` is only logged (R-NUGET-002), and nothing is keyed on it.
- The plug-in adds one kind of per-replica state to the table of R-ARCH-032: the cache of forwarded
  search results (R-NUGET-016, Q-NUGET-8). It declares no package-level mutable variable. Every
  other cache it uses is a registered core cache keyed by `rev_seq`, a stored digest or an epoch.
- **Same answer on every replica.** A rendered NuGet document (service index, version list,
  registration index, page and leaf) or search response is a function of these inputs only:
  - PostgreSQL rows and stored upstream documents;
  - the base URL and the hive;
  - the content coding;
  - the trove release.

  Nothing replica-specific enters the body: no replica name, no clock reading other than stored
  timestamps, no map iteration order (objects are written with a fixed key order). Replicas of one
  release therefore return byte-identical bodies with equal ETags. Replicas of two releases return
  equal weak ETags for documents built from the same inputs (Q-NUGET-9).
- A push uses the spool of the replica that received it, for that one request (R-HA-035, INV-13).
  NuGet has no multi-request upload: a package or a symbol package travels whole in one `PUT`, and
  the `multipart/form-data` envelope is parsed while it streams (R-NUGET-006, R-NUGET-020).

Acceptance criteria, in the `scatter` profile of R-ARCH-032 (three `web` replicas, every request to
the next replica in turn, upstream keep-alive off):
- 50 iterations of `dotnet nuget push` of a `.nupkg` with its `.snupkg`, each followed at once by
  `dotnet restore --no-http-cache` through `nuget-all`, all succeed, with Valkey running and again
  with Valkey stopped.
- A `curl` walk of the search of `nuget-local` with `take=25`, continued until `data` is empty,
  returns every package exactly once and the same `totalHits` on every page. The same walk on
  `nuget-all` returns no package twice.
- For a fixture package with 130 versions, the SemVer 2 registration index and its three pages,
  fetched in sequence, contain each version once.
- These documents are fetched once from each replica: the service index, a version list, the
  index, a page and a leaf of each of the three registration hives (with and without
  `Accept-Encoding: gzip`), and one search page. Each document has the same body SHA-256 and the same
  `ETag` on all three replicas.

### R-NUGET-031 · Valkey unavailable or emptied
Priority: **MVP** · Covers: trove-only

NuGet keeps nothing of its own in Valkey. It reads no document, search result, symbol file or
authorization result from it ([architecture.md](../architecture.md) R-ARCH-030, Q-ARCH-16). It uses
core mechanisms that Valkey speeds up, and each degrades as R-ARCH-030 states. A NuGet request
never fails because Valkey failed (R-ARCH-031).

| Mechanism NuGet uses | With Valkey | Valkey unavailable | Valkey lost its data (failover, flush, restart) |
|---|---|---|---|
| Collapsing remote misses and conditional refreshes: upstream service index, version lists, registrations, `.nupkg`, symbol files, vulnerability documents (R-NUGET-015) | One upstream request per document and flight, across all replicas | At most one per document and flight per `web` replica. An unchanged document costs up to one 304 per period per replica (Q-NUGET-2). | A flight in progress may be fetched twice; content addressing makes the duplicate harmless (R-ARCH-033) |
| Negative cache: unknown IDs' version lists, missing packages, symbol misses (R-NUGET-015, R-NUGET-022) | One upstream request per missing path per not-found period | The per-replica L1 only: up to one upstream request per missing path, period and replica | The next request for each path asks the upstream once |
| Early eviction of merged documents after a remote refresh (R-NUGET-017) | Within milliseconds | Merged entries expire with the remote entries they were built from (R-REPO-020). Local members stay exact through `rev_seq`. | As unavailable |
| Claims, pin rules, patterns and repository settings that routing and the withheld-query rule read (R-NUGET-016, R-NUGET-017) | Applied within milliseconds | Applied within the change-log bound of R-ARCH-018 | Unaffected |
| Rate limits that apply to NuGet requests, such as the failed-credential throttle ([identity-and-authentication.md](../identity-and-authentication.md) R-AUTH-038, R-ARCH-034) | Cluster-wide | Per-replica shares | The counters restart |

- **Gone IDs are not Valkey state.** The `upstream_gone` mark of a version list is a PostgreSQL row
  (R-REPO-029). A flushed or failed-over Valkey therefore never makes a package that the upstream
  withdrew downloadable again. It costs at most one upstream request for the version list, which
  answers 404 again.
- Push, symbol push, unlist, relist and delete use Valkey only for best-effort signals after their
  commit, and behave the same without it.

Acceptance criteria:
- With Valkey stopped for the whole run, the MVP criteria of this page pass, and every replica stays
  ready with `"degraded":["valkey"]`.
- With Valkey stopped, 50 concurrent restores of one uncached package, 25 per replica on two
  replicas, cause at most two upstream `.nupkg` requests. An unchanged version list costs at most one
  conditional request per replica per 300 s.
- `valkey-cli FLUSHALL` during the NuGet loads of the drain test of
  [ha-and-helm.md](../ha-and-helm.md) R-HA-059 makes no `dotnet` command fail. Neither does a
  Sentinel failover forced with `SENTINEL FAILOVER trove` during those loads; afterwards
  `trove_flight_lock_bypass_total` stops rising within `valkey.sentinel.refresh_interval`, so the
  replicas collapse across replicas again on the new primary.
- After `FLUSHALL`, the `.nupkg` of `Acme.Gone` from R-NUGET-015's criterion still answers 404
  through `nuget-org`. The stub records no `.nupkg` request and at most one version-list request per
  replica.

### R-NUGET-032 · Pushes and deletes across drains, restarts and client retries
Priority: **MVP** · Covers: trove-only

How the NuGet clients send writes. Read on 2026-10-28 in the NuGet.Client sources on the `dev`
branch
([`PackageUpdateResource.cs`](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/Resources/PackageUpdateResource.cs),
[`HttpRetryHandler.cs`](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/HttpSource/HttpRetryHandler.cs),
[`HttpRetryHandlerRequest.cs`](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/HttpSource/HttpRetryHandlerRequest.cs),
[`EnhancedHttpRetryHelper.cs`](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/EnhancedHttpRetryHelper.cs),
[`TimeoutUtility.cs`](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/Utility/TimeoutUtility.cs)):
- Package and symbol pushes are `PUT` requests with `Transfer-Encoding: chunked`, so they declare
  no `Content-Length`.
- A push to any host but the public gallery's symbol host, and a `DELETE`, are tried up to three
  times (`DefaultMaxTries = 3`, the first attempt included). Every attempt builds a new request
  from the file.
- A retry follows a 5xx status, a transport exception, and 408 or 429 unless `NUGET_RETRY_HTTP_429`
  is `false`.
- A `Retry-After` on any answer is waited for before the next attempt, capped at one hour by
  default (`NUGET_OBSERVE_RETRY_AFTER` switches the wait off).
- `--timeout` is one budget for the whole command: every package, every attempt and every wait.
  When it runs out, the command fails without a further attempt.
- The symbol package is pushed only after the package push has succeeded.
- A `DELETE` answered 404 fails the command.

Not verified: which released client versions carry exactly this code; the acceptance criteria below
run against the versions pinned in R-NUGET-027.

trove's side:
- **Post-body work is bounded.** After the body of a push has arrived, the NuGet plug-in reads the
  central directory, every local header name, the `.nuspec` (at most `max_nuspec_bytes`) and, for a
  symbol package, the first bytes of each `.pdb`; from P1 it extracts at most three embedded files
  of 1 MiB each (R-NUGET-024). None of it grows with the archive's uncompressed size, so a push
  stays within the bound P-11 that R-ARCH-024 step 5 relies on. Symbol indexing is a job
  (R-NUGET-021).
- **Spool.** A chunked push reserves spool budget in increments while it streams
  ([storage.md](../storage.md) R-STOR-004). It can therefore neither exceed the role's spool budget
  nor make the kubelet evict the pod for an overfull `emptyDir` (R-HA-035). A push that cannot
  reserve its next increment fails with 503 and `Retry-After` (at most 30 s, R-NUGET-002); the
  client waits that long and retries it, through the Service and so possibly on another replica.
- **Drain.** A push or symbol push that is running when its replica receives `SIGTERM` has until
  `server.shutdown_timeout` to complete ([architecture.md](../architecture.md) R-ARCH-024). If one is
  cut while its body still arrives, nothing is committed, and the client's next attempt reaches
  another replica through the Service. One whose body has arrived is not cut between its commit and
  its status line (R-ARCH-024 step 5).
- **Answer lost after the commit.** The answer to a committed push can still be lost: the process
  or node is lost, the network fails, or a commit overruns the shutdown margin. The client's next
  attempt then carries the same bytes and answers 201 without changing anything (R-NUGET-006,
  Q-NUGET-6). A symbol push behaves the same (R-NUGET-020).
- **Unlist, relist and delete are repeatable.** A repeated unlist answers 204 and a repeated relist
  200 (R-NUGET-007). A hard delete of a version that the repository has already tombstoned answers
  204, so a delete repeated after its first attempt committed does not fail.

Acceptance criteria:
- A fault-injection build drops the connection of a push after its commit and before its status
  line. `dotnet nuget push` with default settings exits 0. The repository holds one version row and
  one `package.published` event for it, and `trove_nuget_push_replayed_total` has risen by 1.
- `dotnet nuget push` of a 300 MiB package over a link limited to 10 MiB/s runs while the `web` pod
  receiving it is deleted. The command exits 0, and the version restores through another replica.
- With `web.spool.sizeLimit: 3Gi` and `spool.headroom: 1GiB`, three concurrent pushes of 1 GiB
  packages: two `dotnet nuget push` commands exit 0. The third exits 0 or reports
  `503 (Service Unavailable)`, and in that case succeeds when run again. `kubectl get events` shows
  no eviction.
- With `deleteMode: delete`, `dotnet nuget delete` of the same version, run twice, exits 0 both
  times. A fault-injection build that drops the connection after the delete's commit makes the
  command exit 0 as well.

### R-NUGET-033 · NuGet data in backups and restores
Priority: **MVP** · Covers: trove-only

The backup format, its consistency and the restore procedure belong to
[backup-and-restore.md](../backup-and-restore.md) (INV-19). NuGet contributes the following.

- **Export classes** (R-BAK-004). `nuget_version`, `nuget_tombstone`, `nuget_symbol_package` and,
  from P1, `nuget_symbol_file` are `data`, and NuGet has no `transient` table. Tombstones must
  survive a restore, because they keep a deleted version number from returning (Q-NUGET-6). No
  NuGet table is part of a foreign-key cycle, so none needs a `CycleBreak`.
- **Blobs.** Every NuGet file is an item with a blob reference, so a backup holds all of them
  (R-BAK-006):
  - the `.nupkg` and the `.snupkg`;
  - the generated `.nuspec`, and from P1 the extracted PDBs and embedded files;
  - the cached files of remote repositories.

  After a restore, nothing NuGet serves has to be rebuilt; only the in-process caches of every
  replica start empty.
- **ETags after a restore.** The ETags of local documents contain `packages.rev_seq`, and that of
  the service index `repositories.config_version` (R-NUGET-002). The restore raises both by 2^32
  (R-BAK-022). A client that cached a document from the old installation after the snapshot
  therefore gets 200, not 304. Remote documents take their ETags from stored upstream digests and
  need nothing.
- **Remote caches excluded** (`backup.exclude.remote_caches`, R-BAK-008). Emptying a NuGet remote's
  cache removes its `origin = 'cache'` rows and items through the storage hooks. It writes no
  tombstone, because tombstones belong to local repositories (R-NUGET-006). The upstream service
  index (`.nuget/index.json`) is fetched again on first use.
- **Versions changed after the snapshot** (Q-NUGET-10). Two kinds of audit events on local NuGet
  repositories are flagged for the revocation journal ([ha-and-helm.md](../ha-and-helm.md)
  R-HA-054):
  - every event that adds a version (`package.published`; from P1 also the copy and move events
    of [storage.md](../storage.md) R-STOR-019);
  - every event that removes one (`package.deleted`, and the source side of a move).

  Each entry is `{"repository": <id>, "id": <lower_id>, "version": <URL form>, "sha512": <hex of the
  .nupkg>}`. The replay that every restore runs (R-BAK-024) applies the entries newer than the
  snapshot as follows:

| Journaled after the snapshot | State in the restored database | Replay outcome |
|---|---|---|
| A version entered the repository | Absent, without a tombstone | A tombstone of kind `reserved` with the journaled SHA-512 is written (`applied`) |
| A version entered the repository | Present, or tombstoned | Nothing (`already in effect`) |
| A version was removed | Present with the journaled SHA-512 | Nothing is deleted. The version is reported `revived` in the replay output and counted in `system.restore.journal_replayed` (H-50). |
| A version was removed | Present with other bytes | Nothing (`subject unknown`) |
| A version was removed | Absent, with a `reserved` tombstone | The tombstone becomes `removed` (`applied`), so the deleted bytes stay refused as they were before the restore |
| A version was removed | Absent, without a tombstone | A `removed` tombstone with the journaled SHA-512 is written (`applied`) |
| A version was removed | Absent, with a `removed` tombstone | Nothing (`already in effect`) |
| Any | The repository is unknown | `subject unknown` |

  Entries are applied in the order of R-HA-054. A version published and then deleted after the
  snapshot therefore ends with a `removed` tombstone, as in the old installation; a `reserved`
  one would have admitted the deleted bytes again.

  A `reserved` tombstone refuses a push of other bytes with 409 `nuget_version_reserved` and admits
  the identical `.nupkg` (R-NUGET-006). A pipeline that rebuilds a lost version byte-identically
  therefore restores it. One example is .NET 11 and later with the original
  `DeterministicTimestamp` ([deterministic packages](https://learn.microsoft.com/en-us/nuget/create-packages/deterministic-packages)).
  Any other build has to take a new version number.

  Without the replay, nothing is reserved: a restore run with `--skip-revocation-journal`
  (R-BAK-024, for the disaster in which the source bucket and its journal are gone), an
  installation that ran with `backup.revocation_journal.enabled: false`, and entries not yet
  shipped when the old database stopped all leave lost version numbers free (H-50).

Acceptance criteria, run in the suite of [backup-and-restore.md](../backup-and-restore.md)
R-BAK-029:
- Backup B is taken. Afterwards `Acme.Widgets` 3.0.0 is pushed and `Acme.Widgets` 2.0.0 is
  hard-deleted, and B is restored. A push of a rebuilt 3.0.0 answers 409 `nuget_version_reserved`.
  A push of the original 3.0.0 file answers 201, and the version restores through `nuget-all`. The
  replay output lists 3.0.0 as `applied` and 2.0.0 as `revived`.
- After B, `Acme.Widgets` 3.1.0 is pushed and then hard-deleted. After B is restored, a push of the
  original 3.1.0 file answers 409 `nuget_version_deleted`.
- The same restore run with `--skip-revocation-journal` prints, besides H-37's warning, that NuGet
  version numbers published after the snapshot are not reserved; a rebuilt 3.0.0 then answers 201.
- After B is restored, `dotnet package search Acme --source <restored nuget-all> --exact-match`
  lists exactly the versions of B's snapshot. A conditional request with an ETag that the old
  installation issued after B's snapshot answers 200.
- After a restore with `backup.exclude.remote_caches: true`, `nuget_tombstone` holds no row of a
  remote repository, and `dotnet restore` of `Newtonsoft.Json 13.0.3` through `nuget-org` fetches
  it from the stub upstream once.

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
  nosniff`, `Content-Security-Policy: sandbox; default-src 'none'` and
  `Cross-Origin-Resource-Policy: same-site`, set by the core so no handler can drop them
  ([architecture.md](../architecture.md) R-ARCH-007); icons are served only as `image/png` or
  `image/jpeg` after a content check. An XML document can run script in a browser through
  XHTML-namespaced elements, which the sandbox prevents. They share one origin in the MVP
  (R-ARCH-007); an optional content origin is P1 (R-ARCH-029).
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
  claims (R-REPO-023) apply to NuGet IDs case-insensitively through `lower_id` (R-NUGET-017,
  R-NUGET-018): the first publish of an ID claims it for its local repository
  (`local.autoClaim`), a `global` claim also makes every NuGet remote refuse the ID when addressed
  directly, and the first publish of an ID that a remote member serves needs `manage`
  (R-REPO-048). Client setup recommends one virtual source with `<clear />` (R-NUGET-026). The
  remaining exposure is tracked where the mechanism lives: IDs that no claim in effect and no pin
  rule covers, `members` claims that do not protect virtual repositories without the claiming
  repository (H-9), and deliberate or configuration-driven shadowing (H-10), in
  [repositories-and-proxy.md](../repositories-and-proxy.md); prefixes without an owner in
  [authorization.md](../authorization.md) (H-21).
- **Search terms leave the network.** With `forwardSearch` on, search terms typed by users go to
  the upstream (R-NUGET-016), except queries with a term that an exclude pattern, a claim or a pin
  rule protects. Free-text terms that no such pattern covers (`payments`, a project code name)
  still leave; installations for which those are sensitive turn forwarding off per remote.
- **Symbol probes leave the network.** Debuggers request a PDB for every loaded module. A symbol
  request whose routing name (the file name in the key) no exclude pattern, claim or pin rule
  covers, and that no local member answers, goes to the upstream symbol server and discloses the
  assembly name (R-NUGET-022, P1).
- **Upstream takedowns.** When nuget.org removes an ID, its version list answers 404 and the
  remote stops serving the ID's documents and cached packages on every address (R-NUGET-015,
  R-REPO-029), so lock-file restores by exact path stop receiving it too. A version withdrawn from
  an ID that still exists stays downloadable by exact path from the cache, because NuGet does not
  track removal per version ([repositories-and-proxy.md](../repositories-and-proxy.md) H-5).
- **Deleted versions stay reserved.** A hard-deleted or moved version leaves a tombstone, so nobody
  holding `deploy`, or even `overwrite`, can bring the version number back with other bytes and
  silently replace what lock files and client caches recorded (R-NUGET-006).
- **API keys are full tokens.** The value in `X-NuGet-ApiKey` is a trove token; client setup
  recommends tokens limited to `deploy` on one repository and short-lived CI tokens
  ([gitlab-ci-oidc.md](../gitlab-ci-oidc.md)). `nuget.config` encrypts stored secrets only on
  Windows; elsewhere the instructions use environment-variable references.
- **Symbol files can disclose source.** PDBs may contain source paths and embedded source; they
  are protected by the same `read` permission as the package (R-NUGET-013).
- **Valkey never decides what a NuGet client receives.** No NuGet document, search result, symbol
  file or permission is read from Valkey (R-NUGET-031). Whoever can write under trove's Valkey
  prefix can do two things ([architecture.md](../architecture.md) H-49):
  - hide a remote's version list, package or symbol file behind a forged negative entry, for that
    entry's checked lifetime;
  - delay collapsed fetches.

  They cannot change bytes or metadata. They also cannot make a package the upstream withdrew
  downloadable again, because the gone mark lives in PostgreSQL.
- **Forwarded search results stay in the replica.** Upstream search responses are cached only in
  each `web` replica's memory, and filtered for patterns, claims and permissions on every use
  (R-NUGET-016, Q-NUGET-8). A cached response can therefore never show a caller an ID that a claim
  or grant made since then hides.
- **Restores keep version numbers reserved.** A version published after a backup's snapshot is
  absent after a restore, and while the journal replay runs, its number stays reserved for the
  identical bytes (R-NUGET-033, Q-NUGET-10), so a rebuild cannot silently replace what clients
  cached from the old installation. A version published and deleted after the snapshot stays
  refused. Whoever holds the runtime object-store credential can forge journal entries. A forged
  entry can reserve a version number for good or report a version as `revived`; it cannot delete or
  replace a package. A version deleted after the snapshot comes back with the restore, and a restore
  without the journal reserves nothing (H-50).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-NUGET-1 · Default meaning of a NuGet delete

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Unlist by default, hard delete per repository | The protocol `DELETE` unlists; `nuget.deleteMode: delete` turns it into a hard delete for one repository; management, UI and raw deletes are always hard | The protocol's own reading, as on the public gallery; consumers pinned to the version or restoring from lock files keep working; teams that clean CI feeds with `dotnet nuget delete` opt in per repository | One client command with two behaviors; the UI has to show which one applies |
| (b) Hard delete by default | Every protocol `DELETE` removes the version (to the trash can when it is enabled) | What the command name suggests; storage is freed at once | A routine cleanup command breaks every consumer of that version; the trash can is P1, so in the MVP the removal is final |
| (c) Protocol always unlists, no setting | Hard delete only through the management API, the UI or a raw delete | One behavior per path, nothing to configure | Scripted cleanup with the NuGet client becomes impossible and needs a second tool, while both paths need the same `delete` permission anyway |

**Choice:** (a) — the protocol `DELETE` unlists unless the repository sets `deleteMode: delete`; management, UI and raw deletes are hard deletes.

**Rationale:** Unlisting is reversible and never breaks a restore that already resolved the
version. That matters most in the MVP, where the trash can (R-STOR-022) is P1 and a hard delete
is final. A real removal stays one setting or one management call away for whoever holds `delete`.
With the tombstones of Q-NUGET-6, a hard delete leaves the version number reserved, so offering it
through the protocol cannot turn into an overwrite in two steps. (b) turns a common cleanup command
into an outage for consumers.
(c) removes the scripted cleanup path and gains no security, because both paths check the same
permission and ownership. Purging unlisted versions automatically after a period is a retention
policy ([storage.md](../storage.md)), not a delete mode. The development agent would revisit this
if users expect `dotnet nuget delete` to free storage, or if unlisted versions pile up in a way
cleanup policies cannot handle.

**Applied in the spec:** R-NUGET-007: `deleteMode: unlist` is the decided default and governs only
the protocol `DELETE`; management, UI and raw deletes are named as hard deletes; a hard delete
writes a tombstone (R-NUGET-006); unlisting an unlisted version answers 204; the UI shows the
effective mode; a criterion checks the repeated delete. The setting lives in the format member
`nuget` of the repository document (`nuget.deleteMode`), because
[repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-004 (Q-REPO-5) puts format
settings under a member named after the format, never under a generic name; R-NUGET-014,
R-NUGET-022 and R-NUGET-023 use the same member for `forwardSearch`, `symbolServerUrl` and
`vulnerabilityInfo`.

### Q-NUGET-2 · Metadata cache period of NuGet remotes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stands. Refreshes are now collapsed across replicas through the Valkey flight lock instead of a PostgreSQL lock ([architecture.md](../architecture.md) R-ARCH-033). While Valkey is unavailable, each `web` replica refreshes on its own, so an unchanged document costs up to one 304 per period per replica instead of one per remote (R-NUGET-031). Option (b), the rationale and R-NUGET-015 now say so.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 7200 s | Keep the generic default of R-REPO-024 for the service index, version lists, registrations and vulnerability data | Fewest upstream requests; same value as npm | A version released upstream stays unresolvable through trove for up to two hours, so automated dependency-update merge requests opened minutes after a release fail CI |
| (b) 300 s as the NuGet format default | The NuGet plug-in registers 300 s for those documents; the not-found period stays 1800 s | New upstream versions resolve within five minutes. Each refresh is a conditional request collapsed across replicas through Valkey, so an unchanged document costs one 304 per period per remote; while Valkey is unavailable, one per period per `web` replica | About 24 times more revalidations of hot documents than (a); upstream latency is felt more often, bounded by the refresh timeout that serves the stale copy (R-REPO-027) |
| (c) 0 s | Revalidate on every use | Always current | Every package of every restore waits for an upstream round trip; an upstream slowdown slows every restore up to the refresh timeout |
| (d) Long period plus catalog-driven invalidation | Keep 7200 s, and let a singleton job follow the upstream's catalog resource with a cursor, dropping cached documents of IDs that changed (the pattern the NuGet implementation guide suggests for feeds that mirror nuget.org) | Freshness within the polling interval at the lowest request count per cached ID | Works only for upstreams that offer a catalog; other v3 feeds, trove instances among them, fall back to 7200 s; a cursor, a scheduler lease and catalog parsing are new scope, and trove consumes catalog leaves only from P1 on (R-NUGET-029) |

**Choice:** (b) — the NuGet format registers a default metadata period of 300 s and keeps 1800 s for the not-found period.

**Rationale:** A restore asks for the version list first. With (a), the time between an upstream
release and its first successful restore through trove is up to two hours, which is exactly the
window in which update bots open merge requests. 300 s keeps that window short. The extra load is
small because refreshes are conditional, collapsed in-process and, through Valkey, across replicas
(R-REPO-026, R-ARCH-033), and mostly answered with 304. Even with Valkey down and, for example, 20
`web` replicas, a hot document costs at most 20 conditional requests per 300 s, far below one per
client, and the outage is alerted on (R-HA-057). A merged virtual entry never outlives the remote entries it was built
from (R-REPO-020), so the virtual period adds nothing. (c) puts the upstream on the critical path of
every restore and gains about five minutes of freshness. (d) is the efficient answer for nuget.org,
but it works for one kind of upstream only and needs new machinery; (b) behaves the same for every
v3 upstream and needs nothing beyond R-REPO-027. The not-found period stays at the generic 1800 s.
A version list of an existing package is never a negative entry, so the period only governs IDs
the upstream does not know: mostly internal names that no claim covers, and typos. Each expiry of
such an entry sends the name upstream again, so a shorter period would multiply that disclosure. A
public ID requested before it existed upstream becomes visible when the entry expires, or at once
after an invalidation (R-REPO-045). The development agent would revisit this if upstream request
metrics show rate limiting by the upstream, in which case (d) would be added on top of (b) for
upstreams with a catalog, or if users need newly created public IDs faster than the not-found period
allows. The same holds if the per-replica refreshes of a Valkey outage, at the replica count an
installation runs, are what draws the upstream's rate limiting.

**Applied in the spec:** R-NUGET-001 lists the 300 s and 1800 s defaults in the format
registration; R-NUGET-015 states the period, the documents it covers and the conditional refresh,
and gains an acceptance criterion for upstream changes within 300 s.
[repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-024 and R-REPO-027 list the
300 s NuGet default as well (Q-REPO-2). Revisited on 2026-10-28: R-NUGET-015 names the Valkey
flight lock and the per-replica cost while Valkey is unavailable, and its criteria state that
they assume Valkey; R-NUGET-031 holds the degraded bounds and their criteria.

### Q-NUGET-3 · Non-ASCII package IDs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) ASCII on push, upstream IDs pass through | IDs entering a local repository follow NuGet's ID rule restricted to ASCII (the set nuget.org now enforces); IDs of cached upstream packages follow NuGet's general Unicode rule, keyed by the client's lowercased request path, identity checked by Unicode simple case folding | The rule nuget.org applies to every push and `dotnet pack` warns about; legacy public packages stay restorable; trove never lowercases a non-ASCII ID itself | Two ID rules in the plug-in; a cached legacy ID cannot be copied into a local repository |
| (b) NuGet's full Unicode rule everywhere | Unicode IDs on push too, with lowercasing that matches .NET's invariant culture | Accepts everything an older client packs | trove would have to reproduce .NET's case tables across runtimes; it creates exactly the IDs the ecosystem is retiring for security reasons |
| (c) ASCII everywhere | Cache fills of packages outside the ASCII set fail | One rule, simplest code | About 119 legacy IDs that nuget.org still serves become unrestorable through trove: a client-compatibility regression with no security gain |

**Choice:** (a) — ASCII-restricted IDs for everything that enters a local repository; upstream IDs pass through under NuGet's general rule, keyed by the client's lowercased request path.

**Rationale:** The question is settled by the ecosystem itself. nuget.org has required the
restricted ASCII set for new IDs since 2026-06-15 and refuses every non-conforming push since
2026-07-15, and `dotnet pack` warns from .NET 11 on (NU5052), for trove's reason: lowercasing
differs between .NET Framework, modern .NET, Windows and Linux, so two visibly different IDs can
map to one URL
([NU5052](https://learn.microsoft.com/en-us/dotnet/core/compatibility/sdk/11/nuget-pack-nu5052-packageid),
[announcement](https://github.com/NuGet/Announcements/issues/75)). (b) would rebuild that hazard
inside trove. (c) breaks restores of the roughly 119 legacy IDs, which the announcement says remain
available. Under (a), trove never has to lowercase a non-ASCII ID: a cached package is keyed by
the path the client built, and the declared ID is only compared with it. A mismatch fails closed
with 502, so a case-table difference can make a legacy package unrestorable but cannot make one
ID reach another package's row or grant. Two clients that lowercase a legacy ID differently can at
worst create two cache rows for one upstream package; the bytes are still stored once (INV-4). Where
the two sources disagree (the announcement's expression admits `_` only as the first character,
NU5052 anywhere), trove follows NU5052, the rule the SDK applies when packing. The development
agent would revisit this if nuget.org retires the legacy IDs (then (c) would suffice), or if a
legacy ID fails the identity check in practice.

**Applied in the spec:** R-NUGET-003 splits the ID rules into a local rule and an upstream rule
(including the .NET definition of `\w`, which Go's `regexp` does not provide), states the dates and
the `_` reading, defines `lower_id` for both, holds dependency IDs to the upstream rule and forbids
copying non-conforming cached IDs into local repositories; R-NUGET-005 step 4 selects the rule and
validates dependency IDs; R-NUGET-009 omits the optional dependency `registration` URL for a
non-ASCII dependency ID, so trove never lowercases one; R-NUGET-015 compares the declared ID by
Unicode simple case folding; R-NUGET-003 gains criteria for a refused non-ASCII push and a restored
legacy ID.

### Q-NUGET-4 · Forwarding search to the upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Forward by default | Remote search forwards every query to the upstream; `forwardSearch: false` per remote turns it off | Visual Studio's browse tab and `dotnet package search` find public packages that nobody restored yet | Every query leaves the network, including queries for internal package names typed into a virtual repository's search |
| (b) Cache-only by default | Remote search shows only packages someone already restored | No search term leaves the network | Discovery of public packages through trove does not work out of the box; developers bypass trove and add nuget.org as a source, which reopens dependency confusion |
| (c) Forward by default, withhold protected queries | As (a), but a query is answered from the cache only when one of its terms names an ID that an exclude pattern, a claim or a pin rule keeps away from that remote | Discovery works; queries for the organization's own namespaces stay inside; reuses the routing decision that already exists | Free-text terms outside protected namespaces still leave; slightly more logic than (a) |

**Choice:** (c) — forward by default, switchable per remote, and never forward a query with a term that an exclude pattern, claim or pin rule protects.

**Rationale:** Search is how developers discover public packages, and a cache-only search pushes
them to add nuget.org directly as a second source, the setup the client instructions warn against
(R-NUGET-026). Forwarding queries for claimed or excluded names discloses exactly the names the
administrator has marked as internal, and gains nothing: R-NUGET-018 removes remote results for
claimed and pinned IDs anyway. (c) closes that leak with the in-memory claim trie that resolution
already uses, at the cost of one function. The residual exposure (free-text terms such as a project
code name) is named in the Security notes, and `forwardSearch: false` remains for installations
that need more. The development agent would revisit this if users report missing public results
for withheld queries, or if a deployment needs forwarding off by default.

**Applied in the spec:** R-NUGET-014 sets `nuget.forwardSearch: true` as the per-remote default;
R-NUGET-016 gains the withheld-query rule and two criteria; R-NUGET-018 applies it per virtual
repository; R-NUGET-028 adds the counter `trove_nuget_search_withheld_total`; the Security notes
describe what still leaves the network.

### Q-NUGET-5 · ID casing on later pushes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) First casing is the display name | Any casing is accepted; the version that created the package fixes the display name; each version keeps its own casing in its catalog entry | Stable display; matches NuGet's case-insensitive IDs; no push is refused for casing | A deliberate casing correction does not change the display name |
| (b) Refuse other casing | A push whose ID casing differs from the stored one answers 409 | One casing everywhere | Blocks a legitimate casing correction for no protocol reason; CI fails on a cosmetic difference |
| (c) Highest version's casing is the display name | The display name follows the casing of the highest stored version | A casing correction takes effect with the next release; backports of older versions do not flip it | The display name becomes a derived value that changes on delete and unlist; a single mis-cased release renames the package in every search result |

**Choice:** (a) — accept any casing; the version that created the package fixes its display name.

**Rationale:** NuGet treats IDs case-insensitively, so only the display is at stake. (a) keeps it
stable and costs nothing. (b) refuses valid packages. (c) adds a recomputation on every version
change for a cosmetic gain. A fourth variant, "the latest push defines the display name", was not
listed because every backport push would flip the casing in search results. Each version's catalog
entry still shows the casing its `.nuspec` declares, so clients see the corrected casing for new
versions. The development agent would revisit this if package owners ask for casing corrections; a
management operation that changes the display name would then be the smallest addition.

**Applied in the spec:** R-NUGET-003 states that the display name comes from the version that
created the `packages` row and does not change on later pushes, and gains an acceptance criterion
for a push in other casing.

### Q-NUGET-6 · Overwriting an existing version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice moves from (c) to (d). Requests now reach replicas at random, and replicas are drained and replaced at any time. The NuGet clients repeat a push on their own after a transport failure (R-NUGET-032). Under (c), a repeat whose first attempt had committed failed with 409, so a lost answer failed a CI job although its package was stored. Under (d), a protocol push of the identical `.nupkg` to a stored version answers 201 without change; everything else of (c) stays. A restore's effect on version numbers is the separate question Q-NUGET-10.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Immutable while stored | A repeated push answers 409; after a hard delete the version number is free again | Simple; no extra table | Delete and re-push replace a version in two steps: client caches and lock files that recorded the old bytes silently diverge or fail |
| (b) Opt-in overwrite | A per-repository setting lets callers with `overwrite` replace a version | Convenient for scratch feeds that reuse versions | NuGet clients cache by ID and version in the global packages folder and lock files record content hashes, so a replaced package diverges between machines; the implementation guide forbids modifying packages because lock files and signature validation report changed bytes as tampering |
| (c) Immutable for good, with tombstones | A repeated push answers 409; every removal of a version from a local repository (hard delete, raw delete, move, cleanup) leaves a tombstone, so the number never returns with other bytes; restoring from the trash can brings the original back | One ID and version mean one content forever, as for npm (R-NPM-008); hard delete through the protocol becomes safe to offer | A version deleted by mistake cannot be pushed again after the trash can was purged (in the MVP, which has no trash can, at once); the next version number has to be used; tombstone rows accumulate with every cleanup deletion and are never purged (one small row per version); a client's automatic repeat of a push whose answer was lost after the commit fails with 409 |
| (d) (c), and a repeated protocol push of identical bytes succeeds | As (c), but `PUT <feed>/package` with the `.nupkg` already stored for that version (same SHA-256) answers 201 and changes nothing. Other bytes still answer 409. A tombstone still refuses identical bytes, except a `reserved` one (Q-NUGET-10). Raw deploy and upload keep 409. | Every guarantee of (c). A push stays safe to repeat across drains, crashes and network failures, which is the case the clients' own retry exists for. | A second principal holding `deploy` and ownership on the ID gets 201 for bytes someone else pushed (it learns nothing it did not already have); two answers for "already there", depending on the bytes |

**Choice:** (d) — versions are immutable for the lifetime of the repository; every removal, a move included, leaves a tombstone, and only a trash-can restore brings a version back; a protocol push of the identical bytes of a stored version answers 201 without change.

**Rationale:** (b) breaks what every NuGet client relies on: the global packages folder and lock
files treat one ID and version as one content. (a) still lets anyone with `deploy` and `delete`
achieve the same effect in two steps, which defeats immutability as a supply-chain property. (c)
makes "same ID and version, same bytes" hold per repository, matches the npm rule (R-NPM-008), and
costs one small table and one lookup in the commit transaction. A move writes a tombstone too:
clients of the source repository may have cached the version, so a rebuilt version under the same
number there would diverge exactly as after a delete. Development feeds get unique versions from
pre-release counters instead. INV-9's `overwrite` action has no effect on NuGet repositories, which
R-AUTHZ-006 allows. As on the npm page (Q-NPM-8, decided without a purge), no operation purges a
tombstone and a push of identical bytes against a tombstone is refused.

(d) adds one case. The dotnet CLI and nuget.exe try a push up to three times on a 5xx answer or a
transport exception, and build each attempt from the same file (R-NUGET-032, read in the
NuGet.Client sources). The answer to a committed push can be lost in several ways: R-ARCH-024
avoids it for a drain, but a crash, a node loss, a network failure or a commit that overruns the
shutdown margin still lose it. The automatic repeat then carries exactly the stored bytes. Under (c)
it fails with 409, and the CI job fails although the package is stored. Drains and pod
replacements are routine under the owner's constraints, and R-ARCH-024 protects a push only within
its shutdown margin; every loss that remains would surface as a failed job. Answering 201 for
identical bytes keeps "one ID and version, one content" intact, because nothing changes. A
tombstone still refuses them, because a deletion is a decision that a push must not undo. Raw
deploy and upload keep 409: no client repeats them on its own, and their existing-path rules belong
to [storage.md](../storage.md).

The development agent would revisit this if an import from another system needs to reuse deleted
version numbers, or if tombstone growth from cleanup policies becomes measurable; a
system-administrator-only, audited purge would be the addition then, for both formats at once. It
would also revisit (d) if users rely on 409 to detect that a pipeline re-ran with an unchanged
package.

**Applied in the spec:** R-NUGET-006 states lifetime immutability with the codes
`nuget_version_exists` and `nuget_version_deleted`, defines tombstones (which removals write them,
a move included; that they survive the trash can; that only a restore or deleting the repository
removes them; that identical bytes are refused too), adds the `nuget_tombstone` table, the locking
that orders a push against a concurrent delete, and acceptance criteria for a hard delete and a
move; R-NUGET-005 reports copy and move conflicts per item with the same codes (identical bytes
count as `unchanged`, R-STOR-019) and writes the source tombstone on a move; R-NUGET-007 (hard
delete) refers to it; the Security notes gain "Deleted versions stay reserved". Revisited on
2026-10-28 (option (d)): R-NUGET-006 answers a protocol push of a stored version's identical bytes
with 201 and no change, splits the 409 cases, and rewrites the concurrency rule and two criteria;
the response table lists the new outcome; R-NUGET-028 adds `trove_nuget_push_replayed_total`;
R-NUGET-032 states the client retry behavior this rests on and tests a push whose answer was lost
after the commit.

### Q-NUGET-7 · Patterns and repository-level upstream documents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Package-less requests admitted; symbol files get a routing name | The upstream service index, vulnerability documents, search and autocomplete are repository-level and admitted (R-REPO-005); a symbol request carries the file name of its key without the extension as routing name, so exclude patterns, claims and pin rules apply to it | Follows the name patterns of R-REPO-005; a restricted remote keeps discovering its upstream; internal assembly names are not probed upstream when their namespace is protected | Assembly names that no protecting pattern covers still reach the upstream symbol server |
| (b) Package-less requests admitted; symbol requests filtered by their own patterns | As (a) for documents, but symbol requests carry no routing name; each remote gets separate symbol include and exclude patterns (`nuget.symbolIncludePatterns`, `nuget.symbolExcludePatterns`) matched against the file name in the key | Exact control where assembly names and package IDs differ | A second pattern list per remote that administrators must keep in step with the package patterns; claims and pin rules, which protect internal names in virtual repositories without per-remote configuration, never reach symbol requests; with the lists empty, every internal assembly name is probed upstream as in (c) |
| (c) All package-less requests admitted, symbol files included | Symbol requests are repository-level like the service index | Simplest | A debugger probes every configured symbol location for every module it loads, so internal assembly names go to the public symbol server in every debugging session through a virtual repository with a nuget.org remote |

**Choice:** (a) — requests that name no package are admitted, and symbol requests carry a routing name derived from the file name in the symbol key so patterns, claims and pin rules protect them.

**Rationale:** Name patterns are already the rule for NuGet (R-REPO-005), and they admit requests
that concern no package, which settles the service index and vulnerability documents. Path
patterns on every canonical path are not an option on this page: R-REPO-005 rules them out for
NuGet, and they would make a remote restricted to `Newtonsoft.*` unable to read its upstream's
service index. The remaining question was symbol files. Debuggers ask for a PDB for every module,
internal ones included, and SDK projects name the package after the assembly by default
([pack target](https://learn.microsoft.com/en-us/nuget/reference/msbuild-targets#pack-target)). A
routing name taken from the file name in the key therefore lets the patterns and claims that protect
`Acme.*` packages also protect `Acme.*` assemblies, with no new configuration. (b) gives finer
control, but its protection starts only when an administrator maintains a second list per remote,
and claims and pin rules never reach it; (c) sends every internal assembly name upstream. The
routing name is taken from the key's first segment without its last extension, not only from keys
ending in `.pdb`, because debuggers also send compressed (`.pd_`) and pointer probes and may ask a
configured symbol server for binaries; a rule limited to `.pdb` keys would leave those shapes
without a name and forward them unprotected. The routing name does not change authorization: upstream symbol files stay
repository-level for permissions. The development agent would revisit this if assembly and package
names diverge widely in practice, in which case the symbol patterns of (b) would be added on top of
the routing name, not instead of it.

**Applied in the spec:** R-NUGET-001 names pattern subject `name` and the symbol routing name;
R-NUGET-005 replaces the path pattern `acme.*/**` with the name pattern `Acme.*`; R-NUGET-015 marks
package-less documents repository-level, defines the routing name for every symbol key shape and
updates its pattern criteria; R-NUGET-022 replaces the `.symbols/**` rule and gains a criterion; the
Security notes name the residual symbol-probe disclosure.

### Q-NUGET-8 · Forwarded search results across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-016 caches the upstream's answers to forwarded searches for `remote_result_ttl` (60 s). The
load is now spread over n `web` replicas at random (INV-12), so a per-replica cache hits about 1/n
as often for one user's consecutive pages. The upstream may then see up to n requests per distinct
query and period. Valkey must not hold content that trove serves ([architecture.md](../architecture.md)
Q-ARCH-16), and its values are limited to 1 KiB (R-ARCH-030).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per-replica cache, registered and bounded | A registered in-process cache per `web` replica holds the upstream response as received; patterns, claims, pin rules, permissions and URL rewriting apply on every use | No shared state and nothing served from Valkey. INV-18 holds: any replica may answer from its copy or ask the upstream, within the same period. Claim and grant changes act on cached results at once. | The hit rate falls with the replica count; the upstream sees at most n × distinct queries per period |
| (b) Shared cache in Valkey | One copy per query for all replicas | One upstream request per query and period | Search results are served content. A writer to Valkey could inject package IDs, download counts or `verified` flags into the list developers choose from (Q-ARCH-16), which would need an integrity MAC under a key Valkey never sees. A page of up to 1000 results exceeds the 1 KiB value limit of R-ARCH-030. |
| (c) Shared cache in PostgreSQL | An unlogged table of responses with a cleanup job | One upstream request per query and period; not served from Valkey | A write per distinct query on the system of record, for data that is worthless after a minute |
| (d) No cache | Every search is forwarded | Simplest; always current | Every search and every page that Visual Studio's browse tab loads reaches the upstream, and an upstream slowdown slows every search |

**Choice:** (a) — forwarded search results are cached in each `web` replica's memory, unfiltered, and filtered on every use; nothing is shared through Valkey or PostgreSQL.

**Rationale:**
- (b) would make Valkey part of what developers pick packages from, the line Q-ARCH-16 drew, and
  its values could not even hold a full result page.
- (c) puts disposable write load on the system of record, which the move of the negative cache to
  Valkey has just taken off it.
- The upstream cost of (a) is bounded by n × distinct queries per minute, and for the default
  upstream it hits no documented rate limit. Microsoft's rate-limit page names nuget.org's v3
  search as "not rate limited currently" (page last updated in 2020,
  [rate limits](https://learn.microsoft.com/en-us/nuget/api/rate-limits)).
- Keeping the entry unfiltered and filtering on every use makes a new claim protect cached results
  as soon as the replica applies it, which (b) and (c) would need as well.
- (d) gives up the hits that (a) still has for popular queries across many users, for no gain in
  correctness.
- The development agent would revisit this if `trove_nuget_search_forward_failures_total` shows the
  upstream answering 429 to forwarded searches, or if an upstream other than nuget.org suffers from
  the per-replica fan-out. (b) with a MAC on each entry, and pages split below the value limit, is
  then the next step.

**Applied in the spec:** R-NUGET-016: the cache is the registered core cache `nuget_remote_search`
with the new key `nuget.search.remote_result_cache_bytes`, holds unfiltered upstream responses,
filters and rewrites on every use, is flushed by repository configuration changes, and is
named as the plug-in's per-replica state; it gains a criterion for a claim that hides cached results.
R-NUGET-030 lists it. R-NUGET-028: its metrics come from the core cache. Security notes: "Forwarded
search results stay in the replica". Cross-page: a row in [architecture.md](../architecture.md)
R-ARCH-032's state table.

### Q-NUGET-9 · ETags of rendered documents on replicas of different releases

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-002 gave rendered documents a strong ETag derived from `rev_seq` or the stored upstream
digest, the hive and the base URL. Requests now reach any replica at random, including replicas of
the previous release during every rolling upgrade (R-ARCH-025). The gzip hives also send the same
value with and without content coding. RFC 9110 calls a validator weak if it is shared by
representations with different bytes, naming gzip explicitly
([section 8.8.1](https://www.rfc-editor.org/rfc/rfc9110#section-8.8.1)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Strong ETag from the inputs, as before | `rev_seq` or digest, hive, base URL | No change | Wrong by RFC 9110 for the gzip hives. During every rolling upgrade, N and N-1 may render different bytes under one strong value, and a cache that combines ranges or trusts byte equality is misled. |
| (b) Strong ETag that also covers content coding and trove release | As (a), plus the coding and the release version | Strictly correct | Every upgrade turns every cached validator stale. During a rollout the value differs between replicas, so roughly every second conditional request answers 200. |
| (c) Weak ETag `W/"…"` over a render version and the inputs | The plug-in's render version, `rev_seq` or digest, hive, base URL; for virtual documents the member fingerprint, every local member's `rev_seq` and every remote input's digest | `If-None-Match` uses the weak comparison, so 304s keep working across replicas, codings and releases. A release that must invalidate documents raises the render version. | Clients cannot use the value for `Range` with `If-Range`, which no NuGet client does on metadata. Someone has to remember to raise the render version when a fix must reach cached documents. |
| (d) No ETag on rendered documents | — | Simplest | Proxies and scripts lose 304s; every revalidation transfers the whole document |

**Choice:** (c) — rendered NuGet documents carry a weak ETag over a render version and their inputs; stored files keep the strong, content-addressed ETag.

**Rationale:**
- A weak validator says "semantically the same", which is exactly what holds when two replicas
  render one package's rows, whatever their release and coding.
- 304s are what the ETag is for here. RFC 9110 evaluates `If-None-Match` with the weak comparison
  ([section 13.1.2](https://www.rfc-editor.org/rfc/rfc9110#section-13.1.2)), so (c) keeps them at
  the cost of nothing a NuGet client uses.
- (b) is correct but throws the 304s away at every upgrade and during every rollout.
- Writing out the inputs of a virtual document's value fixes an ambiguity of the earlier text: a
  value over the member fingerprint alone would have answered 304 after a push to a local member.
  R-REPO-020's fingerprint covers permission filtering only, so the value takes it after the pin
  rules and claims of R-NUGET-017; otherwise a new claim, which removes a remote member's versions
  without any push, would have answered 304 with the old version list.
- Package files keep their strong ETags, because resuming a download needs them (R-STOR-012).
- The development agent would revisit this if a client or proxy in use mishandles weak ETags on
  JSON documents. (b) is the fallback.

**Applied in the spec:** R-NUGET-002: weak ETags for rendered documents, their inputs including
those of virtual documents (the member fingerprint after pin rules and claims) and of the service
index (from P1 also the inputs of the vulnerability entry), the same value for both codings,
`Vary: Accept-Encoding` on the gzip hives, the render version; three new criteria. R-NUGET-030: the
same-answer rule and its criterion across three replicas. R-NUGET-033: ETags after a restore.

### Q-NUGET-10 · Versions changed after a backup's snapshot

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A restore (INV-19, MVP) returns an installation to its backup's snapshot. Versions pushed after the
snapshot are gone, and their numbers have no tombstone. A pipeline that runs again then pushes the
same number, generally with other bytes, which breaks the promise of Q-NUGET-6 for every client that
fetched the old bytes. Versions deleted after the snapshot come back. The revocation journal
([ha-and-helm.md](../ha-and-helm.md) R-HA-054) already records chosen audit events outside
PostgreSQL and replays them after every restore (R-BAK-024).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept the snapshot as it is | Nothing is journaled; the gap is documented | No new mechanism | A rebuilt version reuses a number that clients already resolved. Projects with lock files fail with a content-hash mismatch; others silently use different bytes on different machines. |
| (b) Journal additions and removals; replay reserves lost numbers and reports revived versions | Additions and removals on local NuGet repositories are journaled. Replay writes a `reserved` tombstone for each version that entered after the snapshot and is absent now; it admits only the journaled SHA-512. A removal of a version that is absent turns or writes its tombstone into `removed`; a removal of a version that is present is reported as `revived`, not applied. | Q-NUGET-6 holds across a restore, including for a version published and deleted after the snapshot. A pipeline that rebuilds byte-identically restores the version, and any other build takes a new number. Replay only restricts, so a forged entry cannot add or change content. Reuses an existing mechanism, with one table row on R-HA-054. | One journal object per push or delete; a forged entry can reserve a number for good; revived versions stay until an administrator acts; nothing is reserved when the replay does not run (journal skipped or disabled, entries not yet shipped) (H-50) |
| (c) As (b), and replay deletes revived versions again | Removals are applied | No revived versions | Whoever holds the runtime object-store credential could forge entries that delete package versions during a restore and tombstone their numbers. The MVP has no trash can to undo that (R-STOR-022, P1); R-HA-054 disables principals instead of deleting them for the same reason. |
| (d) Recover lost versions from the old blob store | The restore copies post-snapshot packages from the source bucket | Nothing is lost | The source may be what was lost, and INV-19 restores into an empty installation; it would need the old database to know what to copy |

**Choice:** (b) — publishes and deletions on local NuGet repositories are journaled; replay reserves the numbers of lost versions for their exact bytes and reports revived versions without deleting them.

**Rationale:**
- Restore is an MVP operation now, so the window in which a lost version number is reused is no
  longer an edge case.
- NuGet clients treat one ID and version as one content in the global packages folder and in lock
  files. A reused number is therefore worse than a missing one: a missing version fails loudly with
  404, while a reused one fails with a hash mismatch or not at all.
- Since .NET 11, `dotnet pack` builds byte-identical packages when the timestamp is fixed
  ([deterministic packages](https://learn.microsoft.com/en-us/nuget/create-packages/deterministic-packages)),
  so admitting the journaled SHA-512 lets reproducible pipelines restore what was lost.
- (c) would let a forged entry delete package versions and tombstone their numbers, and nothing in
  the MVP undoes that. Where R-HA-054 removes principals, it disables them rather than deleting
  them so that a forged entry stays reversible; a NuGet version has no such reversible state
  before the trash can exists.
- The cost of a forged entry under (b) is a reserved number, which availability alone pays. The
  runtime object-store credential needed to forge one can already delete blobs.
- npm answers the removal half differently. [formats/npm.md](npm.md) Q-NPM-12 replays an
  unpublish by withholding the restored version: replay tombstones it and deletes nothing, and
  `trove admin npm reinstate` reverses it. It reserves no lost numbers (npm's H-51). Both decisions
  wait for the trash can (P1, R-STOR-022, R-STOR-023), where one rule for both formats is
  preferable. Until then the difference is a cross-page item for the owner's walkthrough.
- The development agent would revisit this when the trash can (P1) exists. Replay could then move
  revived versions to the trash instead of only reporting them, which keeps them reversible and
  closes the revival half of H-50. It would also revisit it if the npm rule is settled differently
  for both formats.

**Applied in the spec:** R-NUGET-033: the export classes, the journal entry, the replay table
(including removals of absent versions, so a version published and deleted after the snapshot
stays refused) and criteria; R-NUGET-006: the `reserved` tombstone kind, the code
`nuget_version_reserved` and the identical-bytes exception; R-NUGET-007: a delete in `delete` mode
turns a `reserved` tombstone into `removed`; R-NUGET-028: the journal flag; the Security notes;
H-50. Cross-page: [ha-and-helm.md](../ha-and-helm.md) R-HA-054 (a row for these events and the
outcome `revived`), [operations-and-observability.md](../operations-and-observability.md) R-OPS-014
(a journal flag that only NuGet's producer fills for these shared event types).

### Q-NUGET-11 · Where symbol indexing runs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-021 (P1) indexed symbol packages at push time. That means extracting every PDB, writing it to
object storage and computing its key and checksum before the 201. Pushes now run on `web` replicas
that may be drained at any time. R-ARCH-024 keeps a push from being cut between its commit and its
status line only for work bounded by P-11 (1 s plus size ÷ 150 MiB/s). It also moves work that
outlasts a request to jobs on the `worker` role (R-ARCH-001).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In the symbol push request | Extract, key and checksum before the 201 | A key is servable as soon as the push returns | After the body has arrived, the push writes every extracted PDB to object storage, which typically holds more bytes than the compressed `.snupkg`. Large symbol packages exceed P-11, so a drain can cut them after the commit, and heavy work lands on `web`. |
| (b) One job per symbol package on the `worker` role | The push's commit enqueues `nuget-symbol-index` for that package (dedup key per version, `exclusive`); the job extracts and indexes; keys answer 404 until it commits | The push stays a plain upload within P-11. Indexing scales and drains with `worker`: the packages of one repository are indexed on every `worker` replica at once. A package that cannot be indexed fails alone. The same kind serves the backfill and the reindex. | A key becomes servable after a delay (proposed bound 30 s at p99); a debugger that loads a module right after the push misses its symbols once. One job row per symbol push. |
| (c) No extraction; serve PDBs from the `.snupkg` | Keys are computed at push from the spool; PDB bytes are served by ranged reads of the stored `.snupkg` and inflated per request | No extra objects | The key and the Portable PDB checksum need each PDB inflated in full at push, so post-body work still grows with size. Every debugger request inflates, and raw access to PDBs through `/files/` disappears. |
| (d) One coalesced job per repository on the `worker` role | As (b), but the dedup key is `index:<repository id>` in mode `coalesce`, and the job indexes every package of the repository that is not yet indexed | Fewer job rows during a burst of pushes; never runs alongside a reindex of the same repository | All indexing of a repository runs on one `worker` at a time, so the busiest repository, often the only NuGet local repository of an organization, is bounded by one replica. One package that fails parsing fails the repository's job until `max_attempts` and holds back every package pushed after it. A manual reindex answers 409 while a symbol job waits under the shared key (R-ARCH-016). |

**Choice:** (b) — the symbol push only validates and stores the `.snupkg`; one `nuget-symbol-index` job per symbol package on the `worker` role extracts and indexes it.

**Rationale:**
- The owner's drain constraint and the role split both point to a job: the request does what every
  upload does, and the unbounded part moves to the role that is built for long work and releases it
  cleanly at a drain.
- The owner expects load beyond one replica. (d) would bound each repository's indexing by one
  `worker`, and in a typical installation that is all NuGet indexing. (b) spreads it over every
  `worker` replica.
- A per-package job isolates failures. A crafted or broken PDB ends its own job with
  `index_error`, never the indexing of everything pushed after it.
- Without a shared dedup key, (b) needs another guard against two runs of one package. The row lock
  and the `indexed_at` check in the indexing transaction are that guard. A reindex only enqueues
  jobs, so it never extracts a package itself.
- The delay costs little. Debuggers fetch symbols when a module loads, typically long after the
  package was built.
- (c) does not remove the size-proportional work from the push and adds work to every symbol
  request.
- The development agent would revisit this if users need symbols the moment a push returns. A
  synchronous fast path for symbol packages far below P-11's size bound would then be added on top
  of (b). It would also revisit it if the job rows of busy CI feeds weigh on the queue, in which case
  a batch per repository with per-package claims would replace one row per push.

**Applied in the spec:** R-NUGET-021: indexing as one job per symbol package with kind, queue,
dedup key and mode; ranged reads of the archive and one spooled PDB at a time; the per-package
transaction with its row lock and `indexed_at` check; `index_error` for packages that cannot be
indexed; the 404 until indexed; the delay bound; criteria for the delay, for `worker` at zero
replicas, for a drain with jobs on both `worker` replicas, and for a broken PDB. R-NUGET-020: the
push extracts nothing; the `index_error` column; a data migration enqueues the backfill.
R-NUGET-025: a reindex enqueues jobs instead of indexing symbols. R-NUGET-028:
`trove_nuget_symbol_index_lag_seconds` and `trove_nuget_symbol_index_failures_total`. Cross-page:
a row for `nuget-symbol-index` in [architecture.md](../architecture.md) R-ARCH-016's kind table;
the worker spool note in [ha-and-helm.md](../ha-and-helm.md).

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

### H-50 · A restore revives NuGet versions deleted after the backup's snapshot and can free lost version numbers

Stage: `trove backup restore` ([backup-and-restore.md](../backup-and-restore.md) R-BAK-021) and
platform restores ([ha-and-helm.md](../ha-and-helm.md) R-HA-025), with the journal replay of
R-HA-054 as R-NUGET-033 applies it.

**Revived versions.** Subject: every local NuGet repository; versions hard-deleted after the
snapshot or restore point, and from P1 versions moved away. Every principal with `read` on the ID
can download such a version again.

Live in the MVP whenever a restore runs, because INV-19 makes restore an MVP operation. The
restored database holds the version again. Replay lists it as `revived` but does not delete it
(Q-NUGET-10): a journal entry can be forged by whoever holds the runtime object-store credential,
and the MVP has no trash can that would make such a deletion reversible
([storage.md](../storage.md) R-STOR-022, P1).

What makes it real:
- a version that was deleted because it was malicious or contained a secret;
- an administrator who restores and does not act on the `revived` list;
- deletions whose journal entries had not been shipped when the old database stopped. R-HA-054
  names this residual gap, and these deletions are not reported at all.

**Free version numbers.** Subject: every local NuGet repository; versions published after the
snapshot whose journal entry the replay never sees. Their numbers carry no tombstone after the
restore, so a rebuild can publish other bytes under a number that clients already resolved, which
Q-NUGET-6 exists to prevent.

Live in the MVP in three cases (R-NUGET-033):
- a restore with `--skip-revocation-journal`, which [backup-and-restore.md](../backup-and-restore.md)
  R-BAK-024 provides for the disaster in which the source bucket, and with it the journal, is gone,
  one of the disasters a backup exists for;
- an installation that ran with `backup.revocation_journal.enabled: false` during the interval;
- publishes whose entries had not been shipped when the old database stopped (normally less than
  one `backup.revocation_journal.ship_interval`).

What makes it real: a pipeline that runs again after the restore and builds the lost version with
other bytes. Projects with lock files fail with a content-hash mismatch; projects without them, and
machines whose global packages folder holds the old bytes, use different content under one version.
