# NuGet

Status: specification — in implementation (phase 4): the format skeleton (registration, route table, service index, settings, configuration, error shape) is built; the operations behind the other routes, the data port and the remote and virtual classes are not

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
procedure by [backup-and-restore.md](../backup-and-restore.md); endpoints, their base URLs,
accepted credentials and exposure rules by [endpoints.md](../endpoints.md), whose exposure adapter
R-NUGET-034 implements.

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

The skeleton of the format (phase 4, WP3.1c) registers the class `local` alone; `virtual` joined with its
adapter (WP3.2c, Q-NUGET-56) and `remote` joins when its adapter exists, each in the change that writes the
adapter (Q-NUGET-28), and the format enums of the management API follow the registry (Q-API-190). Two kinds of path meet in the format
and are never mixed: the *protocol path* a client requests below `/api/nuget/<repoKey>/`
(`v3/flatcontainer/acme.widgets/index.json`) and the *canonical path* of an item or a cache entry
(`acme.widgets/index.json`, R-NUGET-005, R-NUGET-015). The routes match protocol paths; the descriptors
(`Classify`) and the package-document path speak canonical paths (Q-NUGET-29).

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
| Vulnerability information (from M4b; the pass-through P1) | `VulnerabilityInfo/6.7.0` | `<feed>/vulnerabilities/index.json` |
| README template (P1) | `ReadmeUriTemplate/6.13.0` | `<feed>/flatcontainer/{lower_id}/{lower_version}/readme` |

- Publish and symbol publish entries appear only on local repositories and on virtual
  repositories with a deploy target (R-NUGET-019). Remote repositories never list them.
- Autocomplete, vulnerability information and the README template appear once their P1
  requirements are implemented; the vulnerability entry only when R-NUGET-023 has data to offer.
  From M4b the vulnerability entry is listed on every repository class, local included, while
  `nuget.vulnerabilityInfo` is `local` and trove's NuGet advisory data is complete
  ([vulnerability-scanning.md](../vulnerability-scanning.md) R-VULN-018).
- Resources the protocol defines but trove does not implement (catalog, repository signatures,
  package details, owner details, report abuse) MUST NOT be listed.
- The service index is rendered by `nuget.ServiceIndex` from the repository's base URL, key and class
  alone. The MVP resources of the table are listed from the skeleton on, although the operations behind
  them are written by later work packages and answer 501 `nuget_not_implemented` until then
  (Q-NUGET-32).

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
  - through a virtual repository: the lower-case ID and the fingerprint of the members that
    contribute to it (R-REPO-020's fingerprint, taken after permission filtering and after the pin
    rules and claims of R-NUGET-017), the `rev_seq` of every eligible local member and the stored
    digest of every contributing remote document. A push to a local member therefore changes the
    virtual document's ETag, and so does a claim or pin rule that drops a member for the ID without
    any push; two IDs whose merges have one fingerprint keep two values (Q-NUGET-87);
  - for the service index: the repository's configuration version and `<base>`; from P1, for a
    remote or virtual repository also the stored digests of the upstream service indexes and the
    member fingerprint that decide whether the vulnerability entry is listed (R-NUGET-023); from
    M4b also whether trove's NuGet advisory data is complete, which decides whether trove's own
    entry is listed (R-VULN-018);
  - for every document: the endpoint of the request and its exposure fingerprint (R-NUGET-034,
    [endpoints.md](../endpoints.md) R-EP-018);
  - on an endpoint with `exposure: rules`, for the documents of local, remote and virtual
    repositories: the SHA-256 of the filtered identity rendering in place of `rev_seq`, of the
    stored upstream digests and of the member fingerprint, which move with versions the endpoint
    hides; the value then moves only with what the endpoint shows (R-NUGET-034, Q-NUGET-87).

  The same value is sent with and without gzip content coding, and replicas of two releases send
  the same value for documents built from the same inputs; RFC 9110 calls such a shared validator
  weak ([section 8.8.1](https://www.rfc-editor.org/rfc/rfc9110#section-8.8.1)). The gzip hives
  answer with `Vary: Accept-Encoding`. A release that must make clients drop documents they cached
  raises the render version. Package files and other stored files keep the strong ETag of
  [storage.md](../storage.md) (R-STOR-012), which `Range` with `If-Range` needs. Search and
  autocomplete responses carry no `ETag`.
- Status codes follow the protocol: 200, 201, 204, 304, 400 invalid parameters or package, 401
  missing or invalid credentials, 403 or 404 per [authorization.md](../authorization.md)
  (R-AUTHZ-016), from M4b 403 `vulnerability_blocked` or `vulnerability_data_missing` for a
  download that [vulnerability-scanning.md](../vulnerability-scanning.md) R-VULN-015 refuses (body
  of R-VULN-016), 405 operation not offered by this repository class, 409 conflict, 413 too large,
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

- **Local rule.** An ID that enters a local repository (push, upload, copy or move)
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
  used for it), and be at most 100 characters. A character outside the Basic Multilingual Plane
  is refused, because .NET sees it as two surrogates that `\w` does not match, and so is a trailing
  line feed, which a `$` anchor would let through. This keeps the legacy IDs restorable that nuget.org
  still serves outside the ASCII set (about 119 IDs per the announcement above).
- IDs are case-insensitive. The canonical package name is `lower_id`. For an ID under the local
  rule it is the ASCII-lowercased ID, which equals .NET's `ToLowerInvariant` for ASCII. For a
  cached upstream package it is the ID segment of the request path that filled the cache,
  percent-decoded as UTF-8 with only its ASCII letters lowercased: for non-ASCII characters this is
  the client's own lowercasing, which the upstream accepted. An ID with a character that the
  client's lowercasing would change (a capital letter outside ASCII, U+212A KELVIN SIGN) is in no
  path a client sends; such a path is no resource (404) and asks nothing of the upstream, and a
  forwarded search result with such an ID is removed, because an upstream would lowercase it into
  another ID's while patterns, claims and pin rules compare the name character by character
  (`id.ClientForm`; security audit of the fix wave). A client whose case table leaves such a
  character unchanged cannot restore that legacy ID through trove: the failure is closed, the
  direction Q-NUGET-3 accepts for a case-table difference. The declared ID MUST equal it once both
  are lowercased character by character with Unicode's simple lowercase mapping, as .NET's
  `ToLowerInvariant` lowercases an ID into its path, and the rows of the cached package take the path's
  ID (R-NUGET-015, Q-NUGET-84). trove never lowercases a non-ASCII ID to build a name or a URL, so no ID
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
  `0x00` (alphanumeric), terminated by `0x00`. An identifier is numeric when it consists of ASCII
  digits and its value is at most 2147483647 (so at most ten digits); a longer digit string is
  compared as text among the alphanumeric identifiers, as NuGet does. The key order is the
  version order: equal keys mean the same version, which are exactly the versions with one URL form.
- Accepted grammar (the one of `NuGet.Versioning`): white space around the string and around a
  numeric part is ignored; one to four numeric parts of ASCII digits, each at most 2147483647,
  leading zeros allowed; an optional pre-release label after the first `-` and optional build
  metadata after the first `+`, each a list of dot-separated, non-empty identifiers of
  `[0-9A-Za-z-]`; a numeric pre-release identifier has no leading zero (`1.0.0-01` is refused,
  `1.0.0-0` is valid). A version string is at most 255 bytes, as given and normalized
  (Q-NUGET-21). A pre-release identifier of `-` and digits, such as `1.0.0--5`, is ordered as
  text, not as a negative number (Q-NUGET-22).
- Requests that carry an ID or version in the path (flat container, registration, publish) MUST
  accept any casing and any non-normalized spelling of the version and resolve it through the URL
  form; responses equal those for the canonical URL (no redirect).
- Dependency version ranges are parsed with NuGet's range grammar (`1.0`, `[1.0,2.0)`, `(,1.0]`,
  `[1.0]` and so on); an unparseable range answers 400. A bare version is a minimum, inclusive
  (`2.0` is `[2.0.0, )`); `[1.0]` is the only single-version bracket form; an empty side is
  unbounded; at least one bound is required; the minimum must not be above the maximum; equal
  bounds need the same bracket kind on both sides. Floating versions (`1.0.*`, `*`) are refused
  (Q-NUGET-20). The normalized text of a range is NuGet's, for example `[1.0.0, 2.0.0)`.
- Conformance: the version parser, normalizer, comparer and range parser MUST agree with the
  `NuGet.Versioning` library on a committed golden-vector file (accept or reject, URL form, full
  normalized form, ordering of every pair, range bounds and the versions a range admits). The
  vectors are generated by a small .NET 10 program (`tools/nugetvectors`) referencing a pinned
  `NuGet.Versioning` version; CI regenerates them and fails on drift. The implementation is
  `internal/formats/nuget/version` (versions, `sort_key`, ranges) and `internal/formats/nuget/id`
  (ID rules); the vectors also hold the ID cases, evaluated with .NET's regular expression
  engine. The differences from the library are exactly the decisions Q-NUGET-21 and Q-NUGET-22.

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
  flag is computed at ingestion and stored. Release 0.9.0 and older classified a version by its own
  number alone; the background data migration `data_migration_nuget_semver2` recomputes the rows
  they stored, once per installation, with the same rule (Q-NUGET-93).
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
(R-NUGET-006), upload through the management API and UI, copy and move, and remote
cache fills. A raw deploy is not an ingestion path of NuGet (Q-NUGET-13). The NuGet plug-in adds these steps between spooling and commit:

1. Open the archive as ZIP within the archive limits below (400 on violation). Entries are read
   through the central directory only. An archive with an encrypted entry, a split or multi-disk
   archive, or a central-directory name that differs from the entry's local header name is
   rejected (400). So is an archive whose entry data overlaps another entry's or runs past the
   central directory, and one that the standard library reads differently from the bounded scan of
   the central directory (Q-NUGET-23): another end-of-central-directory signature after the end
   record whose comment ends the archive, or an entry whose data the standard library would read
   from another offset than the one the scan checked (Q-NUGET-89). Every refusal of steps 1 to 4, a bound crossed included,
   answers 400 with the code `nuget_invalid_package`; the reader keeps the rule that fired as the
   cause for the log and the audit event.
2. Normalize entry names (`\` becomes `/`). Reject the archive (400) if a name is absolute or
   contains a `..` segment, a drive letter or NUL, or if two entries share a name compared
   case-insensitively. The rules apply to the name and to the name with its percent escapes decoded
   once, the spelling a client resolves package parts by; the duplicate comparison uses the decoded
   spelling, and an escape that hides a path separator is refused. Control characters, invalid
   UTF-8, empty and `.` segments, and segments that consist only of dots and spaces are refused too
   (Q-NUGET-24). Then locate exactly one entry at the archive root whose name ends in
   `.nuspec`, compared case-insensitively (400 if none or several). Without these rules, two
   spellings of the `.nuspec`, or a duplicated name that one ZIP reader resolves to the first entry
   and another to the last, would let the identity trove authorizes differ from the identity a
   client installs.
3. Parse the `.nuspec` as XML: UTF-8 or UTF-16; a document type declaration is rejected (400);
   no entity expansion; elements matched by local name in any `nuspec` namespace version (the
   root's namespace is empty or starts with `http://schemas.microsoft.com/packaging/`, and children
   are matched in the root's namespace); a repeated `metadata`, `id`, `version`, `dependencies`,
   `packageTypes` or `license` element is refused, as is a dependency list that mixes groups and
   plain dependencies; at most `nuget.archive.max_nuspec_bytes` (Q-NUGET-25).
4. Validate `id` (R-NUGET-003: the local rule for content entering a local repository, the
   upstream rule for remote cache fills), `version`, every dependency ID (upstream rule) and every
   dependency range. On the package pipeline a
   package whose package types include `SymbolsPackage` is rejected with 400 and the message that
   symbol packages go to the symbol publish resource (R-NUGET-020).
5. Authorize with the declared ID as coordinate (R-AUTHZ-005 in
   [authorization.md](../authorization.md): the name the content declares is the name that is
   authorized and indexed), including the ownership check (R-AUTHZ-018). A caller holding no
   `deploy` grant on the repository at all is refused before the body is read, so unauthorized
   callers cannot fill the spool. A caller who holds `deploy` on the repository and no right to the
   declared ID does fill it once per request ([H-334](../../security/nuget-publish.md#h-334--a-push-is-read-in-full-before-the-permission-on-its-id-can-be-decided)).
6. In the commit transaction (R-STOR-005): create the `packages` row if absent and the
   `package_versions` row (its unique index on the URL form makes a duplicate answer 409); create
   the item of the `.nupkg` as lead item and the derived items with `generated = true`; insert
   the NuGet side rows below; increment `packages.rev_seq` (R-ARCH-009). The `.nupkg` is the lead of
   one commit and the derived items are its companions (`Commit.Companions` with `Generated`, Q-ARCH-228):
   all of them commit in this one transaction, the lead's hook first. After the commit, the
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
- Raw deploy (R-STOR-007) into a NuGet repository answers 409 before the body is read, for every
  path and file type: `PUT /files/<repoKey>/...` is accepted only by a format whose protocol base path
  is `/files/{key}/` (generic, Q-ARCH-223), and the answer names the protocol path of the repository,
  `/api/nuget/<key>/` (Q-NUGET-13). A package enters through the protocol push, the upload of the management API and the
  UI, copy and move. The derived files, which only the plug-in writes, are never a raw deploy's.
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
  trashed and restored as a unit, and the format's optional `OnItemsRestored` hook (`formats.ItemsRestorer`) removes the tombstone. A raw
  delete of a derived item alone answers 409: the `OnItemsRemoved` hook refuses it, which rolls the
  removal back (Q-NUGET-13).

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
    search_doc       tsvector    NOT NULL,         -- 'simple' configuration, weighted: ID tokens A, title and tags B, authors and summary C, description D (Q-NUGET-18)
    origin           text        NOT NULL CHECK (origin IN ('push', 'upload', 'copy', 'cache'))
);
CREATE INDEX nuget_version_latest ON nuget_version (repository_id, lower_id, sort_key DESC) WHERE listed;
CREATE INDEX nuget_version_search ON nuget_version USING gin (search_doc);
```

The sketch has no trigram index on `lower_id`: it would need `CREATE EXTENSION pg_trgm`, which no
migration of trove runs (Q-NUGET-12). The created schema adds CHECKs on the empty IDs and digest
lengths and the indexes of the foreign keys and the symbol job's queue (Q-NUGET-19).

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

`max_entries` and `max_uncompressed_bytes` bound what the central directory may declare (the sum of
the declared sizes) and what a streaming pass may decompress without keeping content; the NuGet
reader enforces them itself, because the shared zip reader can only tighten the defaults of
R-ARCH-028 (Q-NUGET-23). The compression ratio is checked for the entries read into memory. Entries read into memory (the `.nuspec`,
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
- The address is `<feed>/package` with or without one trailing slash: the clients extend the address of the service index by
  a slash before they PUT to it (Q-NUGET-66). `<feed>/symbolpackage` behaves the same (R-NUGET-020).
- Versions are immutable for the lifetime of the repository (Q-NUGET-6). These rules hold
  whatever permissions the caller holds, `overwrite` included (R-AUTHZ-006 lets a format refuse
  overwrites), and for every ingestion path into a local repository (protocol push,
  upload; copy and move report them per item, R-NUGET-005):
  - A push whose URL form exists in the repository with other bytes answers 409
    `nuget_version_exists`.
  - A protocol push (`PUT <feed>/package`) of the identical `.nupkg`, compared by SHA-512 with the
    stored lead item (Q-NUGET-36), answers 201 and changes nothing: no row is written, `rev_seq` stays, no
    `package.published` event is written, and `trove_nuget_push_replayed_total` counts it
    (Q-NUGET-6). The NuGet clients repeat a push on their own after a transport failure
    (R-NUGET-032), and this rule makes such a repeat succeed when its first attempt had committed.
    Upload keeps answering 409 for an existing version.
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
  Unlisting is no withdrawal: a holder of `deploy` and the ownership relists, and the version was
  downloadable throughout ([H-333](../../security/nuget-publish.md#h-333--unlisting-is-not-a-withdrawal-a-holder-of-deploy-relists)).
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
- A hard delete and a push of another version of the same ID run side by side without waiting for
  each other in a circle: the delete holds the package row and never waits inside its transaction
  for the ID's folder, which the push holds until it commits; a folder the delete empties while
  another transaction holds it is removed after the delete's commit, if it is still empty then
  (Q-NUGET-88).
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

- Every version in a list MUST be downloadable by the same caller with the same permissions. From
  M4b, one exception: a version blocked by [vulnerability-scanning.md](../vulnerability-scanning.md)
  R-VULN-015 stays listed and its package file answers 403 `vulnerability_blocked`
  ([H-311](../vulnerability-scanning.md#h-311--a-block-refuses-the-download-not-the-resolution-or-an-earlier-install)).
- The media type of the package is `application/octet-stream` and that of the `.nuspec`
  `application/xml`, whatever the item row recorded (Q-NUGET-44).
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
  leaves inside its bounds at request time, so a page URL from an older index stays valid. Bounds
  that are no range (a bound that is no version, `lower` above `upper`) are 404; a range with no
  visible version is a page with `"count": 0` and an empty `items`, while an ID with no visible
  version in the hive is 404 for every document (Q-NUGET-42). The `@id` of the page is built from
  the bounds in URL form, whatever spelling the request used.
- Leaf object in a page: `@id` (leaf URL), `catalogEntry` (object), `packageContent` (flat
  container `.nupkg` URL), `registration` (index URL).
- Catalog entry mapping:

| Property | Value |
|---|---|
| `@id` | the leaf URL |
| `id` | `declared_id` |
| `version` | full normalized version |
| `listed` | always emitted |
| `published` | `published_at`, ISO 8601 UTC in whole seconds (`2026-10-28T09:14:03Z`, cut and not rounded; Q-NUGET-41) |
| `packageContent` | flat container `.nupkg` URL (legacy duplicate) |
| `authors`, `description`, `iconUrl`, `language`, `licenseUrl`, `minClientVersion`, `projectUrl`, `requireLicenseAcceptance`, `summary`, `tags`, `title` | from `metadata`; absent values omitted; `requireLicenseAcceptance` is shown when the row holds it, `tags` as a list (Q-NUGET-43) |
| `licenseExpression` | `license` value when its type is `expression` |
| `dependencyGroups` | per group `targetFramework` (verbatim, omitted when absent) and `dependencies` with `id`, `range` (verbatim; omitted when the `.nuspec` gave none) and `registration` (the dependency's index URL in the same hive of the same repository; omitted, as the protocol allows, when the dependency ID is outside the ASCII local rule of R-NUGET-003) |
| `deprecation`, `vulnerabilities` | omitted for local packages; passed through for remote ones (R-NUGET-015) |
| `readmeUrl` | omitted; README access is R-NUGET-024 |

- Bytes. A document is written with its members in a fixed order: the order of the sketch below
  (a catalog entry: `@id`, `id`, `version`, then the members taken from `metadata` in alphabetical
  order, then `listed`, `published`, `packageContent`, `dependencyGroups`, `deprecation`,
  `vulnerabilities`), compact, with HTML characters unescaped. Nothing in a document depends on a
  map's order, on the replica or on a clock (R-NUGET-030). The hives `registration-gz/` and
  `registration-semver2/` answer with `Vary: Accept-Encoding`; gzip is chosen only when the client
  lists `gzip` with a quality above zero, and a wildcard is not consent (Q-NUGET-44). The standalone
  leaf document holds `@id`, `listed`, `packageContent`, `published` and `registration`.

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
| `q` | query (syntax below); empty or absent returns every package, paged; more than 1,024 bytes or more than 16 terms answers 400 (Q-NUGET-46) |
| `skip` | default 0; above `max_skip`, negative or not an integer answers 400 |
| `take` | default `default_take`; above `max_take` is clamped; `0` returns only `totalHits`; negative or not an integer answers 400 |
| `prerelease` | `true` (case-insensitive) includes pre-release versions; anything else excludes them |
| `semVerLevel` | R-NUGET-004 |
| `packageType` | keep packages whose latest visible version declares that type (case-insensitive); a value that is not a valid package type name returns no results; empty means no filter |

Query syntax: terms separated by whitespace; double quotes group a phrase (a quote that is never
closed runs to the end of the text); `field:value` and `field:"a phrase"` limit a term to one field.
Supported fields, matched case-insensitively:

| Field | The term matches when |
|---|---|
| `id` | the lower-case ID contains the value |
| `packageid` | the lower-case ID equals the value |
| `version` | the package has a listed version visible under the request's filters that equals the value after the normalization of R-NUGET-003 and that the caller may read (Q-NUGET-74); a value that is no version matches nothing |
| `title`, `tags`, `author`, `description`, `summary` | the words of that field of the latest visible version start with the words of the value |
| `owner` | never: a local package has no owners to match (Q-NUGET-46) |

A `field:` prefix that is not in the table leaves the whole word as free text, and a field without a
value is dropped. Every term must match (AND). A free-text term matches when it is contained in the
lower-case ID, or when its words, as prefixes in the order written, are found in `search_doc`: an ID
token (tokens split at `.`, `-`, `_` and at lower-to-upper case changes of the display name, so
`Newtonsoft.Json` and `NewtonsoftJson` are found by `json`), title, tags, authors, summary or
description. A package matches by its **latest visible version** (the highest listed, untrashed
version under `prerelease` and `semVerLevel`): terms and `packageType` are tested against that
version's row, so what matches is what the result shows (Q-NUGET-46). For a caller who may read
only some versions, that is the latest version the caller may read: a version the caller may not
read decides neither whether a package matches nor what `totalHits` counts (Q-NUGET-74). A package
the repository's include and exclude patterns do not admit (R-REPO-005) is neither listed nor
counted, as its documents answer 404 (Q-NUGET-77).

Ranking: a package takes the best tier over its free-text terms, in this order: the lower-case ID
equals the term; the ID starts with it; the ID contains it or an ID token starts with it; any other
field. Inside a tier, more downloads first (0 for every package until R-STOR-015 ships, so that
changes nothing yet), and ties are broken by `lower_id` in byte order, so paging is stable on every
installation whatever its database collation. A query without a free-text term, and so an empty
query, ranks every package equally: the order is downloads descending, then `lower_id`.

Response: `totalHits` (matching packages, independent of paging) and `data`, one object per
package whose latest version under the filters is visible:

| Property | Value |
|---|---|
| `@id`, `registration` | the index URL in the same hive as `versions[].@id` (below) |
| `@type` | `Package` |
| `id` | display name |
| `version` | full version of the latest visible version |
| `versions` | every listed version visible under `prerelease` and `semVerLevel`, ascending: `version` (full), `downloads`, `@id` (leaf URL in the `registration-semver2/` hive when `semVerLevel` is 2.0.0 or higher, else in `registration/`) |
| `description`, `summary`, `title`, `iconUrl`, `licenseUrl`, `projectUrl`, `tags`, `authors` | from the latest visible version |
| `totalDownloads` | sum of `versions[].downloads` |
| `packageTypes` | `[{"name": …}]` of the latest visible version; at least `Dependency` |
| `owners`, `verified`, `deprecation`, `vulnerabilities` | omitted for local packages |

- `downloads` come from R-STOR-015 (P1); until that ships they are 0. The protocol requires the
  property, not a value.
- Unlisted versions never appear; a package with no listed visible version is absent.
- This requirement covers local repositories and the cache-only mode of remote repositories.
  Forwarding to an upstream is R-NUGET-016, merging in virtual repositories R-NUGET-018.
- **Implementation.** One statement per request reads the latest visible version of every package
  of the repository (`DISTINCT ON (lower_id) … ORDER BY lower_id, sort_key DESC` over
  `nuget_version_latest`), tests the terms against that row (`search_doc`, the metadata fields, the
  ID for the substring tests) and ranks, counts and pages in SQL; no trigram index (Q-NUGET-12). The
  cost is linear in the packages of the repository, not in the matches: a term in the middle of a
  word of a description matches nothing, a substring of the ID does (Q-NUGET-46).
- **What the caller may read** is decided before anything is counted (R-AUTHZ-016), by the shape of
  the read the decision point reports for the repository (`Resolver.ReadScope`). A caller who reads
  the repository whole, of a repository without include and exclude patterns, is counted and paged
  by SQL. A caller whose read depends on the package or the version (a grant with package patterns,
  a token scope, an endpoint with exposure rules), and every caller of a repository with patterns,
  is served by a scan: the format reads the candidates in rank order, from one statement in one
  transaction in batches of 500 as they arrive, with the keys
  of every listed visible version and, per version, whether that version satisfies the terms other
  than `version:` (a candidate is a package with such a version whose `version:` terms each name a
  listed visible version). It asks the patterns and the decision point about each version, and keeps
  a package when a version is left, the latest of those satisfies the terms and every `version:`
  term names one of those (Q-NUGET-74); it counts them and keeps only the entries of the requested
  page. The page is then read once with the readable versions alone, so `version`, `versions`,
  `packageTypes` and `totalDownloads` never come from a version the caller may not read, and
  `packageType` is decided on the latest readable version. The scan reaches the last candidate, so
  `totalHits` is exact; it stops with 400 when the query matches more than 200,000 packages, all
  members of a virtual repository together (Q-NUGET-45, Q-NUGET-76). The rank of the scan is the
  rank of the SQL page, so both callers see the packages in one order.

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
- A user whose grant excludes `acme.widgets` sees `totalHits` without it and pages over the
  rest; `skip` and `take` count the packages that user may read.
- A repository whose `excludePatterns` hold `Acme.Secret*` neither lists nor counts
  `Acme.Secret.Thing` for any caller, while another repository without the pattern lists it
  (Q-NUGET-77).
- `curl "<feed>/search?q=title:%22widget+toolkit%22"` finds `Acme.Widgets` by a phrase in its title,
  `q=version:1.1.0` finds the packages that have that version, `q=owner:acme` returns nothing.
- `curl "<feed>/search?q=json"` finds `Newtonsoft.Json` and `NewtonsoftJsonSchema`; `q=tonsoft`
  finds both by the substring of the ID.

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
  `Authorization` header applies. The four routes declare it (`Route.APIKey`), and the
  authentication stage learns the declaration from the dispatcher's match of the request
  ([identity-and-authentication.md](../identity-and-authentication.md) Q-AUTH-186).
- **Basic and Bearer.** `Authorization: Basic` with any username and a trove token as password,
  and `Authorization: Bearer <token>`, on every NuGet endpoint (INV-8).
- **Session cookie.** Accepted for GET and HEAD only (R-AUTH-001), so a browser on the UI can
  download a package; never on API-key endpoints, whatever their method (Q-AUTH-187). A cross-site
  form can send `POST`, which is the relist method; it carries no credential trove accepts.
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
- The plug-in requires a `PackageBaseAddress/3.0.0` resource and at least one
  `RegistrationsBaseUrl` type in the upstream index. The configuration is not refused on create
  or update; the first request that needs the index answers 502 `upstream_error` naming the
  missing resource, and the upstream test of R-REPO-024 shows the same (Q-NUGET-14, Q-NUGET-51).
  A v2-only upstream fails the same way.
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
  vulnerabilityInfo: local                       # default (proposed) while scanning is on, from M4b; local | upstream (P1) | off (R-NUGET-023)
```

Acceptance criteria:

- Creating a remote with only `remote.url` set to nuget.org's index succeeds; the upstream test
  reports it reachable.
- A remote whose upstream index lacks `PackageBaseAddress/3.0.0` (stub server) answers its first
  request with 502 `upstream_error` naming it, and stores nothing.
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
| `.nupkg` | artifact | `{lower_id}/{lower_version}/{lower_id}.{lower_version}.nupkg` | upstream package content URL; where a registration of the ID that trove has read names another `packageContent` for the version, that address (recorded in `remote_url_map`, Q-NUGET-54) |
| `.nuspec` | artifact | `{lower_id}/{lower_version}/{lower_id}.nuspec` | upstream package content `.nuspec`, cached as an item of its own (Q-NUGET-54) |
| any registration document | metadata | `{lower_id}/.registration/index.json` and `{lower_id}/.registration/page/{lower}/{upper}.json` (the bounds of the page in URL form, Q-NUGET-51) | upstream registration index of the discovered hive, plus every non-inlined page |
| search, autocomplete | search | — | R-NUGET-016 |
| symbol file (P1) | artifact; routing name = first segment of the key (the file name the debugger looks for) without its last extension, lowercased | `.symbols/<key>` | R-NUGET-022 |
| vulnerability index and pages (P1) | metadata | `.vulnerabilities/index.json`, `.vulnerabilities/<name>.json` | R-NUGET-023 |

- Stored upstream documents stay unchanged. Registration documents are not passed through: at
  serve time the plug-in parses the stored upstream index and pages, drops versions the requested
  hive must not show (R-NUGET-004, using each leaf's version and dependency ranges), and renders
  its own index, pages and leaves with R-NUGET-009's paging. Catalog entry properties are copied;
  `deprecation` and `vulnerabilities` are copied without the upstream's JSON-LD `@id` and `@type` at
  any depth, with `advisoryUrl` kept as a web link when it is an absolute `http` or `https` address
  without user info (Q-NUGET-85); unknown properties that hold no address are kept (Q-NUGET-52).
- One rendering reads at most 512 pages of an index (an index that lists more is no registration
  index: 502 `nuget_invalid_upstream_document`, nothing stored), at most 32,768 versions (502
  `nuget_invalid_upstream_document`) and at most `MaxDocumentBytes` inflated over the index and all its
  pages together (502 `upstream_item_too_large`); the pages are read and converted one at a time
  (Q-NUGET-83).
- URL rewriting in rendered documents:

| Property | Rewritten to |
|---|---|
| index, page and leaf `@id`, `parent`, `registration`, dependency `registration` | the serving repository's hive URLs |
| `packageContent`, `catalogEntry.packageContent` | the serving repository's flat container URL |
| `catalogEntry.@id` | the serving repository's leaf URL |
| `iconUrl` pointing into the upstream package content base | the serving repository's flat container `icon` URL (P1, R-NUGET-024); unchanged before that |
| `projectUrl`, `licenseUrl`, other `iconUrl` values | unchanged (web links shown to people) |
| `advisoryUrl` of an entry of `vulnerabilities` | unchanged when it is an absolute `http` or `https` address without user info (a web link shown to people), dropped otherwise (Q-NUGET-85) |
| `@id` and `@type` inside `deprecation`, `vulnerabilities` and `alternatePackage` | dropped (Q-NUGET-85) |

- Upstream URLs for package content are derived from the discovered `PackageBaseAddress` and the
  canonical path, never from a host named in a client request (R-REPO-043).
- Every cached `.nupkg` is parsed like an upload (R-NUGET-005 steps 1 to 4 with the upstream ID
  rule of R-NUGET-003, without the `SymbolsPackage` rejection) and recorded with `origin = 'cache'`
  rows under the ID of the request path. Content that no reader takes for a package (no ZIP end
  record, a ZIP archive without a `.nuspec` at its root, a `.nuspec` that is not well-formed XML) is
  still served and cached, logged and counted (R-NUGET-028), but gets no NuGet rows; the bytes are
  never altered. Every other refusal of the reader (a limit, a name, duplicate or overlapping entries,
  central directory and local headers that disagree, several `.nuspec` files, a `.nuspec` of another
  encoding or shape) and an ID or version that does not parse MUST be refused with 502
  `upstream_package_mismatch`, because the identity cannot be checked (Q-NUGET-84). A cached `.nupkg`
  whose `.nuspec` declares an ID that differs from the request's ID segment (both lowercased as
  R-NUGET-003 says), or a version whose URL form differs from the canonical path, MUST be refused with
  502 `upstream_package_mismatch`. A refused package is not stored. NuGet v3 announces no content
  digest (H-35), so the declared identity is the only check that the upstream served the package that
  was asked for.
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

Acceptance criteria (against a stub v3 upstream in CI and, in the full run of Test and Release on the commit that closes the phase, against nuget.org):

- `dotnet restore` of a project referencing `Newtonsoft.Json 13.0.3` through `nuget-org` succeeds;
  the `.nupkg` is cached; a second restore with the upstream unreachable succeeds.
- Every URL in `curl <base>/api/nuget/nuget-org/v3/registration-semver2/newtonsoft.json/index.json`
  except `projectUrl`, `licenseUrl`, `iconUrl` and `advisoryUrl` starts with
  `<base>/api/nuget/nuget-org/v3/` (Q-NUGET-85); the `deprecation` and `vulnerabilities` of a version
  that has them are in the document.
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
  terms of `q` as R-NUGET-010 parses them, written back in the query syntax (so the upstream gets
  exactly the terms the withheld rule below checked, Q-NUGET-80), the same `prerelease`,
  `semVerLevel` and `packageType`, and with `skip` and `take` clamped to trove's limits
  (R-REPO-025 lets the plug-in map these parameters). A query with a `version:` term that is no
  version matches nothing (R-NUGET-010) and is not forwarded. The answer is read within 8 MiB
  inflated, and a result that lists more than 32,768 versions is removed (Q-NUGET-90). Result URLs are rewritten as
  in R-NUGET-015 (`registration`, `versions[].@id` into the hive that matches the request's
  `semVerLevel`); `owners`, `verified`, `deprecation`, `vulnerabilities`, download counts and
  `packageTypes` are kept, `deprecation` and `vulnerabilities` without the upstream's addresses as
  in R-NUGET-015 (Q-NUGET-85). Results whose ID is excluded by the remote's patterns, refused by remote
  refusal (a `global` claim, R-REPO-023), or not readable by the caller are removed, and
  `totalHits` is reduced by the number removed from the returned page. Through a virtual
  repository, R-NUGET-018 step 2 also removes IDs that its claims and pin rules keep away from this
  remote. A result is converted into the shape of a local result and rendered by the same code: a
  version that is no NuGet version and a result without a version are dropped, a caller whose read is
  decided per version sees only the versions it may read (R-NUGET-034), with `totalDownloads` their
  sum, and when the upstream's latest version is among the hidden ones the result shows none of the
  metadata, `deprecation` and `vulnerabilities` that describe it. For such a caller a result is also
  removed when a `version:` term names no version it may read, and when the upstream's latest
  version is hidden and the query has another term, because the upstream matched the terms against
  versions the caller may not read (as Q-NUGET-74 rules out for the local search); members of a result that neither
  R-NUGET-010's result table nor this bullet names are not passed on (Q-NUGET-78).
- **Withheld queries** (Q-NUGET-4). A query is not forwarded when one of its terms is protected;
  the remote then answers that request with its cache-only search. The plug-in splits `q` into
  terms as R-NUGET-010 does (a phrase into its words), drops a `field:` prefix and lowercases each
  term like an ID (R-NUGET-003); it also asks about every run of the characters an ID may hold inside
  a word, with and without a separator at its ends, so `acme!`, `acme*` and `unknown:acme` ask about
  `acme` (Q-NUGET-80). A term is protected when, for a package whose `lower_id` equals
  the term, an exclude pattern of the remote, a namespace claim in effect for the request or a pin
  rule in effect for the request would keep the package away from this remote (R-REPO-005,
  R-REPO-023; through a virtual repository the claims and pin rules in effect in that virtual
  repository, on the remote directly the `global` claims when remote refusal is on). A term is also
  protected when it equals the literal part of such a prefix pattern without the `*` and without a
  trailing `.`, `-` or `_` (for `Acme.*`: `acme`). Include patterns do not protect terms: they
  restrict what a remote serves, not what is sensitive. The check is `Resolver.KeepsAway` ([architecture.md](../architecture.md) Q-ARCH-271); it uses
  the in-memory claim trie of R-REPO-023 and adds no database query. Every replica keeps that trie current from the
  PostgreSQL change log (area `claims`, [architecture.md](../architecture.md) R-ARCH-018), so after a
  claim change two replicas can disagree about one query for at most that propagation bound, with
  or without Valkey. Withheld requests are counted (R-NUGET-028).
- Forwarded responses are kept in the engine's registered in-process cache `proxy_query_results` of
  each `web` replica (Q-NUGET-8, Q-NUGET-16, [architecture.md](../architecture.md) R-ARCH-013 step 6,
  R-ARCH-018, Q-ARCH-270), for `nuget.search.remote_result_ttl` and within `proxy.query_cache_bytes`
  (8 MiB by default). The plug-in calls `Host.Remote().Query` with the path `v3/search` (which names
  no package, so the populate check is the repository's, Q-NUGET-79), the clamped parameters and the
  period; the host resolves the remote's service index first, as a fetch does, and the engine owns
  the request, the cache, the circuit and the metrics ([architecture.md](../architecture.md)
  Q-ARCH-282).
  - The key is the remote repository, its configuration version (`config_version`) and the upstream
    request URL with its parameters. The entry is the upstream response as received.
  - Pattern, claim, pin-rule and permission filtering and the URL rewriting run on every use, so a
    claim or grant change applies to cached results as soon as the replica has applied it. A
    change of the remote's configuration drops its entries of the older configuration versions
    (area `repo_config`), and its deletion drops all of them. An entry fetched under the
    configuration the change produced stays through both of the replica's applications of that
    change ([architecture.md](../architecture.md) R-ARCH-018), and so does an entry whose fetch
    met a change of another repository (Q-NUGET-94).
  - Nothing is persisted and nothing is written to Valkey: search results are content served to
    clients ([architecture.md](../architecture.md) Q-ARCH-16).
  - Two replicas may answer one query from copies fetched up to `remote_result_ttl` apart, and
    either may ask the upstream instead. Both answers meet the same contract, which is why this
    per-replica state does not break INV-18 (R-NUGET-030).
  - Forwarding needs `read` on the remote. It stores nothing in the repository, but a query without
    a cached answer contacts the upstream only for a caller who passes the populate check of
    R-AUTHZ-008, as every other contact does (Q-ARCH-270); a caller who does not is served a cached
    answer or the cache-only search. The cache view (R-REPO-032) and an endpoint with
    `cache_fill: false` (R-NUGET-034, R-EP-019) answer with the cache-only search and use no cached
    answer either (Q-NUGET-79).
- When the upstream search fails, the circuit is open (R-REPO-029), the upstream announces no
  search resource or answers with something that is no search document, the remote answers from its
  cache-only search with 200, and counts it in `trove_nuget_search_forward_failures_total`
  (R-NUGET-028).
- `forwardSearch: false`: the remote answers with the cache-only search, which is R-NUGET-010 over
  its `origin = 'cache'` rows.
- Autocomplete (P1) follows the same two modes.

Configuration (system file):

```yaml
nuget:
  search:
    remote_result_ttl: 60s                 # default (proposed); the engine cuts it to one hour
proxy:
  query_cache_bytes: 8388608               # default, 8 MiB per web replica; 0 turns the cache off (Q-NUGET-16)
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
- Member documents (Q-NUGET-56). Two documents of an ID are merged, each at the canonical path the
  remote adapter stores it at (R-NUGET-015): the version list at `{lower_id}/index.json`, and the
  registration entries at `{lower_id}/.registration/index.json`. A local member contributes the
  rows of its stored versions; a remote member contributes its stored upstream document, inflated when
  the upstream coded it with gzip, and for the registration entries the format reads the pages the
  stored index lists by address through the member, as the remote itself reads them, and converts the
  leaves with the remote's conversion, so a version shows the same entry through the remote and through
  the virtual repository (Q-NUGET-82). A page that cannot be read fails the merge as an upstream
  failure instead of contributing a short list ([H-348](../../security/nuget-remote.md#h-348--a-remote-member-whose-registration-page-cannot-be-read-fails-the-merged-registration-not-only-itself)); a member the caller may not fill contributes
  the pages it has stored. The merged document is neutral: it holds
  no address and no hive, so one cache entry serves the three hives and every serving address; the
  handler renders the version list, the index, the pages and the leaves from it for the repository the
  client addressed, with R-NUGET-009's paging, the hive's SemVer 2.0.0 rule (R-NUGET-004) and the
  endpoint's exposure (R-NUGET-034). A remote member whose cache the caller may not fill contributes
  only the versions whose package it has cached (R-AUTHZ-008).
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
  with `Cache-Status: trove; detail=partial` and not cached (R-REPO-020); its rendering is not kept
  either, and `trove_nuget_virtual_member_skipped_total{repo,reason="unavailable"}` counts it.
- The read check is per member and per ID (`Resolver.Plan`). The merged entry is shared by every
  caller with the same members, so a grant narrower than the ID (a version pattern) is not applied
  inside the merge; the download of the version applies it, and the document may list a version the
  caller cannot fetch.

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
- Two local members that both hold `Acme.Widgets 1.5.0` list it once; the registration entry and the
  `.nupkg` are the first member's (`Trove-Resolved-From`), and the validator of every merged document
  is the same on every replica.

### R-NUGET-018 · Virtual repository: merged search and autocomplete
Priority: **MVP** · Covers: PKG-NUGET-026, PKG-NUGET-028

Search through a virtual repository merges the result lists of the members that remain after
R-REPO-019's filtering:

1. Each member is queried in parallel with the request's filters, `skip=0` and
   `take = min(skip + take, virtualMergeWindow)`, at most four members at a time, and the scans of
   the local members share the one bound of 200,000 packages of R-NUGET-010 (Q-NUGET-76). The
   members are those of `Resolver.Plan`, which leaves out every member the caller cannot read and
   says which remote members the caller may fill; rows are kept by the caller's
   `Resolver.ReadScope` (architecture.md Q-ARCH-271), which decides each row as a read through this
   virtual repository: its exposure rules and the lift of a token scope that names it apply, as they
   do to its documents (Q-EP-11, Q-AUTHZ-163, Q-AUTHZ-168, Q-NUGET-75). Local members use
   R-NUGET-010, remote members R-NUGET-016, including its withheld-query rule evaluated with this
   virtual repository's claims and pin rules. A member contributes only the IDs that its own include
   and exclude patterns and this virtual repository's admit (R-REPO-005, Q-NUGET-77), and on an
   endpoint with exposure rules only what the endpoint exposes of this virtual repository: versions
   outside its view are dropped, and a result whose latest version is outside it is dropped whole
   (R-NUGET-034, Q-NUGET-75).
2. Results of a member are removed for every ID that this virtual repository's pin rules or claims
   keep away from that member (R-REPO-023, as in R-NUGET-017), through `Resolver.KeepsAway`: an ID
   whose selected pin rule does not name the member, or, without a pin rule, an ID that a claim in
   effect here covers and the member does not hold. This applies to local members as well
   (Q-NUGET-57), so a search never lists what the version list, the registration and the download of
   the same virtual repository deny.
3. The merged order is: packages whose ID equals a query term exactly (from any member), then the
   remaining results member by member in resolution order, each member's own order kept.
4. Results are deduplicated by `lower_id`. For a package found in several members, `versions` is
   the union (for equal versions the member that tier resolution would pick wins), `version` and
   the package-level fields come from the latest version of that union, `totalDownloads` is the
   sum.
5. The page `[skip, skip + take)` of the merged list is returned; positions at or beyond
   `virtual_merge_window` return an empty `data`, and a page that crosses the window ends at it
   (Q-NUGET-58).
6. `totalHits` is the sum of the members' `totalHits` minus the duplicates and removals seen in the
   window, and never less than the packages the merge found. It is an estimate; clients use it only to
   decide whether to load more. A remote member contributes through the forwarded search of
   R-NUGET-016, which joins the merge at one seam (`Format.searchRemote`): the member is asked for the
   window like every member, its `totalHits` is the upstream's less the results R-NUGET-016 removed
   from it, and its results keep the members R-NUGET-016 keeps (download counts, owners, `verified`,
   `deprecation`, `vulnerabilities`), which a package of several members takes from the member that
   holds its latest version (Q-NUGET-81). A withheld query, an upstream failure, and a caller who
   may not fill the member when no answer is cached, make the member contribute its cache-only
   search.

All URLs in the merged result point at the virtual repository. Autocomplete (P1) merges the same
way: ID mode as a deduplicated union in the order above, version mode as the ascending union of the
members' versions.

Configuration (system file):

```yaml
nuget:
  search:
    virtual_merge_window: 1000   # default; 1 to 10000
```

Acceptance criteria:

- `dotnet package search acme --source trove-all` returns local `Acme.*` packages before upstream
  results, and `q=packageid:newtonsoft.json` returns `Newtonsoft.Json` first.
- A package present in `nuget-local` (`local.autoClaim: false`, pushed by a `manage` holder,
  R-REPO-048) and at the stub upstream, neither pinned nor claimed, appears once with the union of
  its versions; when a pin rule names only `nuget-local` for it, or a claim of `nuget-local` covers
  it, only with the local versions.
- `curl "<feed>/search?q=&skip=1000&take=20"` on `nuget-all` answers 200 with empty `data`.
- With the rule of R-NUGET-034 on the virtual `nuget-customers` and `Acme.Sdk` in its member
  `nuget-internal`, `search?q=acme&prerelease=true` on `customers` lists exactly 2.0.0 and 2.1.0,
  with the bound service account and with a token scoped to read on `nuget-customers` by its key.
- Visual Studio 2022's browse tab on `nuget-all` loads more results while scrolling (manual check,
  recorded per release).

### R-NUGET-019 · Publishing through a virtual repository
Priority: **MVP** · Covers: PKG-NUGET-027, REPO-038

- Push, symbol push, unlist, relist and delete sent to a virtual repository are executed on its
  deploy target (`virtual.deployTarget`, R-REPO-021) and answer exactly as that local repository
  would. Permissions and ownership are checked on the target; the virtual repository's own `read`
  gate applies as well (R-AUTHZ-009 rule 5). Unlist and relist never touch other members.
- Without a deploy target, the publish resources are not listed (R-NUGET-002) and publish requests
  are refused: the decision point denies a write on a virtual repository without a target, so the
  answer is 403 `forbidden` and nothing is stored; the dispatcher's 405 `no_deploy_target` stays behind
  it for a decision that admits the write (Q-NUGET-59).

Acceptance criteria:

- `dotnet nuget push Acme.Widgets.1.4.0.nupkg --source trove-all --api-key "$TOKEN"` stores the
  package in `nuget-local`; `/files/nuget-local/acme.widgets/1.4.0/acme.widgets.1.4.0.nupkg`
  exists.
- After removing the deploy target, the same push is refused with 403 and the service index of
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
  `.snupkg` and commits. Validation reads the first 32 bytes of each `.pdb` for its signature and
  no more (Q-NUGET-61); the `.snupkg` is linked to its version, so the removal of the version
  takes it along, and the push raises the package's revision like every write of the generic
  rows (Q-ARCH-212).
- The symbol push's commit transaction enqueues the indexing job of that one symbol package
  (Q-NUGET-11): kind `nuget-symbol-index` on queue `index`, dedup key
  `nuget-symbol-index:<version id>` in mode `exclusive`, arguments `version_id` and
  `repository_id`. Serving PDBs to debuggers is P1 (R-NUGET-021); until it ships, the job reads
  the state of the symbol package, records it as its result (`gone`, `indexed`, `stored`) and
  leaves `indexed_at` unset (Q-NUGET-60). The release that ships R-NUGET-021 enqueues the same
  job for every symbol package whose `indexed_at` is unset, through a data migration
  ([architecture.md](../architecture.md) R-ARCH-026).

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
- A `.snupkg` with a `.pdb` that is neither a Portable PDB (`BSJB`) nor a Windows PDB
  (`Microsoft C/C++ MSF 7.00`) answers 400; a different `.snupkg` for a version that holds one
  answers 409 `nuget_symbol_package_exists`.
- The push enqueues exactly one `nuget-symbol-index` job, also when it is repeated; with the
  `worker` Deployment running, the job succeeds on the `index` queue and its result names the
  state of the package.

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
  returns the file (the full run of Test and Release on the commit that closes the phase, against the live symbol server); the second request is served from
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

- `nuget.vulnerabilityInfo` is `local | upstream | off`
  ([vulnerability-scanning.md](../vulnerability-scanning.md) R-VULN-018, Q-VULN-10). `local`, from
  M4b, serves trove's own advisory data on every class; `upstream` is the pass-through of this
  requirement, on remote and virtual repositories; `off` lists no resource. The default is `local`
  while the installation's `vulnerabilities.enabled` is `true` and its OSV source is not `off`,
  otherwise `upstream` on remote and virtual repositories and `off` on local ones. Until this
  requirement is built, `upstream` is refused when the repository document is written, and the
  default where it would be `upstream` is `off`.
- A remote repository with `nuget.vulnerabilityInfo: upstream` whose upstream offers
  `VulnerabilityInfo/6.7.0` lists the resource in its own service index and serves
  `<feed>/vulnerabilities/index.json` and `<feed>/vulnerabilities/<name>.json`. Index and pages are
  metadata cache entries; page URLs are rewritten to the remote; `@updated` is kept, so clients
  re-download a page only when the upstream changed it.
- A virtual repository lists the resource when at least one readable remote member offers it.
  Its index is the concatenation of the members' entries, with `@name` rewritten to
  `<first 8 hex of SHA-256 of the member key>-<upstream name>` truncated to 32 characters, and
  page URLs pointing at the virtual repository. The index MUST NOT exceed 16 entries; members
  beyond that are dropped in resolution order and the drop is logged.
- Until M4b, local repositories do not list the resource, because trove holds no advisory data of
  its own then; from M4b they list trove's resource in mode `local` (R-VULN-018). Because
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
  --name trove --username trove --password "$TROVE_TOKEN" --store-password-in-clear-text
dotnet restore
dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source https://trove.example.com/api/nuget/nuget-local/v3/index.json --api-key "$TROVE_TOKEN"

# nuget.exe
nuget sources add -Name trove -Source https://trove.example.com/api/nuget/nuget-all/v3/index.json -Username trove -Password %TROVE_TOKEN%
nuget push Acme.Widgets.1.2.3.nupkg -Source https://trove.example.com/api/nuget/nuget-local/v3/index.json -ApiKey %TROVE_TOKEN%
```

Which snippets exist, as built (Q-NUGET-49): per client a configure, an install and, for the two command line clients, a
publish template; the snippets of the UI and of the guide are these templates rendered for a repository. The credential
parts (`--username … --password …`, `-Username … -Password …`, the `packageSourceCredentials` block) are written only for a
repository that is not anonymously readable, in the token spelling of the template's language: `%TROVE_TOKEN%` in a
`nuget.config` and in cmd, `${TROVE_TOKEN}` in a POSIX shell, `$env:TROVE_TOKEN` in PowerShell. The user name is the fixed
`trove` (the server accepts any, R-NUGET-012). The `dotnet` snippets exist for a POSIX shell and for PowerShell, the
`nuget.exe` snippets for cmd and PowerShell, and all three clients get the `nuget.config` below as a file; Visual Studio gets
the `nuget.config` and the `PackageReference` of a project (`<PackageReference Include="acme.widgets" Version="1.2.3" />`) and
nothing else, because it has no command line. A publish snippet names the repository the reader chose and carries the token
as the API key. `dotnet add package <id> --version <version>` and `nuget install <id> -Version <version> -Source trove` are
the install snippets; `nuget sources add` leaves the clear-text switch out because nuget.exe on Windows encrypts the password
and the guide says that Mono, Linux and macOS need `-StorePasswordInClearText`.

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
      <add key="Username" value="trove" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>
</configuration>
```

(`packageSourceCredentials` is written for a repository that needs a credential; for an anonymously readable one the file ends
after `packageSources`.)

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
- **The guide of NuGet** ([api-and-ui.md](../api-and-ui.md) R-UI-049, R-UI-019; owner 2026-10-31, Q-UI-83) ships
  with the format in M3: the format's `Guide()` covers local, remote and virtual repositories for the .NET CLI,
  nuget.exe and Visual Studio, with `nuget.config` as the configuration all three read. It explains, true to this
  page as built: push and the identical-bytes retry (R-NUGET-006, R-NUGET-032), unlist, relist and delete
  (R-NUGET-007), the token as password and as API key (R-NUGET-012), `<clear />` and package source mapping against
  dependency confusion together with namespace claims and the shadowing check, what a remote and a virtual repository
  serve, `NU1102` for a version hidden on a customer endpoint (R-NUGET-034), `packages.lock.json`, and the troubleshooting
  table of NuGet's answers. Its configure and install sections attach the snippets of this requirement, and kit check 8
  fails the format without the guide ([architecture.md](../architecture.md) R-ARCH-010). The guides area and the
  repository screen show it without a new screen.
  As built (Q-NUGET-50): the sections are `what` (one per class), `configure`, `authenticate`, `install`, `publish`,
  `maintain`, `lockfiles`, `endpoint` and `troubleshooting`; `publish` and `maintain` are written for the two command
  line clients only, `Guide()` returns the sections of the classes the descriptor lists, and the sections of the other
  classes are in the code from the start and appear with their class. The troubleshooting table names a code of the
  problem catalog under its status in every row. The version hidden on a customer endpoint, `NU1102`, and the
  `NU1101` of an ID that is not exposed at all are in the section `endpoint`.

Acceptance criteria:

- NuGet passes kit check 8 with its guide, and the How to use tab of `nuget-all` shows the configure snippet of the
  setup page for each client.
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
| Linux, Valkey stopped | .NET 10 SDK | R-NUGET-031, full run of Test and Release on the commit that closes the phase |
| Linux, pooler overlay (`POOLER=1`, R-HA-051: transaction-mode pooler) | .NET 10 SDK | every MVP criterion, in the same full run, so no NuGet path depends on session state ([architecture.md](../architecture.md) R-ARCH-019) |
| Windows | nuget.exe, version pinned in the harness, and .NET 10 SDK | push, install, delete, search, `nuget.config` with credentials |
| Linux, full run of Test and Release on the commit that closes the phase | .NET 10 SDK, nuget.exe on Mono | remote, search and virtual criteria against nuget.org, read-only (`TestNuGetClosingPublic`, Q-NUGET-91); the symbol criteria against its symbol server join with R-NUGET-022 (P1) |
| Manual, per release | Visual Studio 2022 | criteria marked manual |

- The NuGet loads of the node-drain test ([ha-and-helm.md](../ha-and-helm.md) R-HA-059) and the
  NuGet packages of the backup suite ([backup-and-restore.md](../backup-and-restore.md) R-BAK-029)
  come from this suite's fixtures. The drain load runs, in parallel: pushes of packages with symbol
  packages; restores through `nuget-all` of local packages and of remote packages not yet cached,
  from a stub upstream that delays every response by 2 s; search walks; and, from P1, symbol
  requests. Every `dotnet` command of it exits 0 while a node of each role is drained.
- Upstream behavior in CI comes from a counting stub v3 server ([test/nugetstub](../../../test/nugetstub)),
  so CI does not depend on the public internet. It generates a deterministic package set in code
  instead of replaying recorded nuget.org responses (Q-NUGET-26): a service index whose resources
  are spread over two hosts, the flat container, a plain and a gzip registration hive, one package
  with 200 versions in registration pages that are not inlined, search, and counters per host and
  path. Failure modes (down, a 2 s delay of every response, a package gone, a `packageHash` the
  bytes do not match, another package's bytes at a URL) are switched per test. Unlisted versions
  come from its `unlist` switch. Deprecations and vulnerabilities, which the generated set does not
  carry, join with R-NUGET-015 and R-NUGET-023 (P1) when those are built.
- Fixture packages are generated by `dotnet pack` from small projects in the test tree, plus
  hand-built archives for the malformed cases of R-NUGET-005.
- The golden vectors of R-NUGET-003 run in the unit test tier.

Acceptance criteria: the suite is green in CI for every change that touches the NuGet plug-in or
the shared request path, and the full run of Test and Release on the commit that closes the phase is green.

### R-NUGET-028 · Audit events and metrics for NuGet
Priority: **MVP** · Covers: trove-only

Event catalog, schema and metric conventions are owned by
[operations-and-observability.md](../operations-and-observability.md) (INV-16). NuGet emits:

| Operation | Audit event |
|---|---|
| Push accepted | `package.published`, and `artifact.deployed` for the `.nupkg` and for the generated `.nuspec` (the item store records one event per stored file, Q-NUGET-36) |
| Push of the identical bytes of a stored version (R-NUGET-006) | none; counted in `trove_nuget_push_replayed_total` |
| Symbol push accepted | `artifact.deployed` for the `.snupkg` |
| Unlist, relist | `package.metadata_changed` with `change` = `unlist` or `relist` |
| Hard delete | `package.deleted` and `artifact.deleted` |
| Reindex started | `system.task_triggered` |
| Refused push, delete or relist | the authentication or `authz.denied` events of the owning pages |

- Handler names for the HTTP metrics: `nuget.index`, `nuget.flatcontainer`, `nuget.registration`,
  `nuget.search`, `nuget.autocomplete`, `nuget.push`, `nuget.delete`, `nuget.relist`,
  `nuget.symbolpush`, `nuget.symbols`, `nuget.vulnerabilities`. A resource with several operations
  names its routes by the handler name and, after a dot, the operation: `nuget.flatcontainer.index`,
  `nuget.flatcontainer.nupkg`, `nuget.flatcontainer.nuspec`, `nuget.flatcontainer.embedded`,
  `nuget.registration.index`, `nuget.registration.page`, `nuget.registration.leaf`. The route name
  is the handler label of the request log and of `trove_http_requests_total` (Q-NUGET-30).
- Format metrics (# example names): `trove_nuget_virtual_member_skipped_total{repo,reason}`,
  `trove_nuget_remote_unparseable_packages_total{repo}` (the label is `repo`, as for the replay counter),
  `trove_nuget_search_forward_failures_total{repository}`,
  `trove_nuget_search_withheld_total{repository}` (R-NUGET-016),
  `trove_nuget_push_replayed_total{repo}` (R-NUGET-006; `repo` is the repository label R-OPS-007 allows) and, from P1,
  `trove_nuget_symbol_index_lag_seconds` (age of the oldest symbol package without `indexed_at`,
  R-NUGET-021) and `trove_nuget_symbol_index_failures_total{repository,reason}` (packages that
  ended with `index_error`). The forwarded-search cache reports through the core's
  `trove_cache_requests_total{cache="proxy_query_results"}`.
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
  not verified; the full run of Test and Release on the commit that closes the phase settles it before the requirement ships.

Acceptance criteria:

- A stub upstream serving a `.nupkg` whose bytes differ from its leaf's `packageHash`: `dotnet
  restore` fails, no item exists afterwards, and the integrity failure counter of R-REPO-041
  increments.
- In the full run of Test and Release on the commit that closes the phase, restoring 50 popular nuget.org packages through a remote with
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
- The plug-in adds one kind of per-replica state to the table of R-ARCH-032: the forwarded search
  results (R-NUGET-016, Q-NUGET-8), kept in the engine's registered cache `proxy_query_results`
  (Q-NUGET-16). It declares no package-level mutable variable. Every other cache it uses is a
  registered core cache keyed by `rev_seq`, a stored digest or an epoch.
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

## Endpoints

### R-NUGET-034 · NuGet documents on an endpoint with exposure rules
Priority: **MVP** · Covers: trove-only

The NuGet plug-in implements the exposure adapter of [endpoints.md](../endpoints.md) R-EP-018. On an
endpoint with `exposure: rules`, after the merge of a virtual repository and the rewrite of a remote
document:

- **Version list** (`flatcontainer/{lower_id}/index.json`): only exposed versions; 404 when none is
  left, like an unknown ID.
- **Registrations**, all three hives: leaves only of exposed versions. Paging (R-NUGET-009) runs over
  the exposed versions visible in the hive, so `count`, `lower`, `upper` and the inlining rule follow
  the filtered list; a registration index without an exposed version answers 404. Dependencies are
  not filtered: a dependency on an ID the endpoint does not expose fails the consumer's restore with
  NuGet's "unable to find package", which the rule's author checks with `check-exposure`.
- **Search and autocomplete** (R-NUGET-010, R-NUGET-011, R-NUGET-016, R-NUGET-018): only exposed IDs
  and versions; `version` is the latest exposed version visible under the request's filters,
  `totalDownloads` sums the exposed versions, and `totalHits` counts exposed packages only. A term
  matches only exposed versions: a `version:` term names an exposed version, and the other terms are
  tested on the latest exposed version (Q-NUGET-74). Through a virtual repository the rules of the
  virtual repository decide, as they do for its documents (Q-NUGET-75). On an
  endpoint with `cache_fill: false` a remote answers with its cache-only search (R-EP-019); with
  `cache_fill: true`, forwarded results are filtered per item and `totalHits` is reduced as
  R-NUGET-016 reduces it for removed results.
- **Package content.** `.nupkg`, `.nuspec` and embedded files of hidden versions answer the 404 of
  R-EP-013.
- **Pre-releases.** `prereleases: false` hides every version with a pre-release label, whatever the
  client's `prerelease` parameter says; the SemVer 2.0.0 visibility of R-NUGET-004 applies to what
  remains. Exposure does not change a version's listed state.
- **Service index.** On an endpoint with `exposure: rules` it lists no publish and no symbol publish
  resource, because the endpoint accepts no writes (Q-EP-68); a push, delete or relist sent to the
  package URL answers 403 `endpoint_read_only`. The symbol server and vulnerability resources (P1) are not listed there and
  answer 404 until their requirements specify their filter.
- **Version ranges** in rules use NuGet's interval notation (`[2.0,4.0)`; a bare `2.0` means "2.0
  or higher"), compared after the normalization of R-NUGET-003; `ValidateRange` refuses what the
  NuGet client's range parser refuses, floating versions included. The version names of a rule's
  `versions` include and exclude lists are not ranges: they are compared literally with the
  canonical form the format asks about, the normalized lower-case URL form (`2.1.0-beta.1`), so an
  exclude written as `2.1` or `2.1.0-Beta.1` hides nothing (Q-NUGET-62, Q-EP-69, H-380).
- **Validators.** The ETag of R-NUGET-002 includes the endpoint and its exposure fingerprint. For
  the documents of local, remote and virtual repositories it is derived from the filtered
  rendering, not from the package's revision, the stored upstream digests or the merge
  fingerprint, so a push, unlist, relist or delete of a hidden version, and an upstream change
  that touches only hidden versions, leave it as it was and a consumer polling with
  `If-None-Match` gets 304 (Q-NUGET-87).

Acceptance criteria (`Acme.Sdk` 1.9.0, 2.0.0, 2.1.0-beta.1 and 2.1.0 in `nuget-internal`, a member
of `nuget-customers`; a rule on `customers` exposing `Acme.Sdk` with `"range": "[2.0.0, )"` and no
pre-releases):

- On `customers`, `curl <feed>/flatcontainer/acme.sdk/index.json` returns
  `{"versions":["2.0.0","2.1.0"]}`; the `registration-semver2/` index lists the same two leaves;
  `search?q=acme&prerelease=true` returns `"version": "2.1.0"` with two `versions`. Through `default`
  all four versions are listed.
- On `customers`, `search?q=packageid:acme.sdk version:1.9.0` and a `description:` term that only a
  hidden version's description holds return `totalHits: 0`; a word of the 2.1.0 description finds
  `Acme.Sdk` while a hidden pre-release is newer.
- `dotnet add package Acme.Sdk --version 1.9.0` against `customers` fails with `NU1102`; a restore of
  2.1.0 with the bound token as password succeeds.
- `curl -X PUT <customers feed>/package` with the bound token as API key (`X-NuGet-ApiKey`) answers 403
  `endpoint_read_only`, and so do `DELETE` and `POST <feed>/package/acme.sdk/2.0.0`. `dotnet nuget push`
  against `customers`' `index.json` stops in the client, which finds no `PackagePublish` resource
  (Q-EP-68).

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
  rule protects, also when punctuation surrounds it (Q-NUGET-80). Free-text terms that no such
  pattern covers (`payments`, a project code name) still leave; installations for which those are
  sensitive turn forwarding off per remote. The count a forwarded answer reports for a caller whose
  read is decided per package is the upstream's less what the returned page removed
  ([nuget-remote.md](../../security/nuget-remote.md#h-347--the-count-of-a-forwarded-search-tells-a-caller-with-row-level-reads-how-many-upstream-matches-it-may-not-see) H-347).
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

### Q-NUGET-12 · No trigram index on package IDs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The first sketch of `nuget_version` had `nuget_version_id_trgm`, a GIN index on `lower_id` with
`gin_trgm_ops`, to answer a search term that is the middle of an ID. The operator class belongs to
the `pg_trgm` extension. No migration of trove creates an extension today, and an installation on a
managed PostgreSQL may run the application's migrations with a role that is not allowed to.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Create `pg_trgm` in the migration | `CREATE EXTENSION IF NOT EXISTS pg_trgm` in 00017, the index as sketched | Substring search on IDs is indexed | The first migration that needs a privilege beyond the owner of the schema; an install whose migration role may not create extensions cannot start the release; the extension is a second thing to upgrade and to restore |
| (b) No trigram index; token search on `search_doc` | `search_doc` carries the ID split at its separators (weight A); a term matches whole tokens and token prefixes (`to_tsquery('simple', 'wid:*')`); a term that is an ID prefix is also served by `nuget_version_latest` | No extension, no privilege; matches how NuGet clients search (words of an ID such as `Newtonsoft`, `Json`); the same GIN index serves title, tags and description | A term in the middle of a token (`tonsoft`) matches nothing; the result list differs from a substring match on those terms |
| (c) Substring search with `ILIKE '%term%'` and no index | A sequential scan over the repository's rows | Matches everything | Cost grows with the number of versions; every search scans |

**Choice:** (b) — no `CREATE EXTENSION`, no trigram index; search reads `search_doc`.

**Rationale:**
- A migration that needs more than the schema owner's rights changes what an installation must
  grant, for a gain on a query shape the clients do not send: the NuGet clients and the gallery
  search by words, and the protocol defines `q` as a search term, not as a substring.
- The ID tokens in `search_doc` make `Acme.Widgets` findable by `acme`, `widgets` and `wid`.
- (c) turns a cosmetic gain into a scan of the largest table of the format.
- The development agent would revisit this if users report that the middle of an ID must be found,
  or if a measured search plan shows the token prefix match too slow. The trigram index would then
  be added in a later expand step that creates the extension and the index, behind a documented
  privilege requirement.

**Applied in the spec:** R-NUGET-005: the sketch drops `nuget_version_id_trgm`. R-NUGET-010: the
implementation hint names `search_doc` and `nuget_version_latest` as the candidate sources.
Migration `00017_nuget.sql` carries no `CREATE EXTENSION`.

### Q-NUGET-13 · A raw deploy into a NuGet repository answers 409

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-005 let `PUT /files/<repoKey>/{lower_id}/{lower_version}/...nupkg` run the ingestion pipeline and answer 400 when path and declared identity differ. The file seam refuses a raw deploy into any repository whose format is not served at `/files/{key}/` before the body is read (Q-ARCH-223, `FilesSeam.RawDeploy`), and roadmap WP3.0h would have changed it: an admission of a deploy before the body, an inspection after the spool with the re-check of the declared name at the commit, and a veto of a delete of a derived item alone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A raw deploy answers 409 for every path; a raw delete of a derived file alone is refused by `OnItemsRemoved` (chosen) | Q-ARCH-223 stays as it is; the seam gets nothing | No contract addition at the freeze; one road in (push, upload, copy, move) for tombstone, ownership and shadowing rules; the generated files cannot be written by hand | `curl -T` of a `.nupkg` into `/files/` does not work; scripted uploads use the push, which MAY take a raw body (R-NUGET-006) |
| (b) The seam runs the format's ingestion rules on the raw road (WP3.0h) | Admission before the body, inspection after the spool, re-check at the commit, a veto seam, `generated` items refuse raw writes | One more way in for scripts | Three additions to the frozen files seam for a second road to the same pipeline; every rule of R-NUGET-006 (tombstones, ownership, the shadowing check) must be right on two roads |
| (c) 409 for raw deploy now, the seam later | As (a), with (b) as an additive extension | Nothing is built twice | Same as (a); (b) stays available because a new seam is an optional interface (R-ARCH-025) |

**Choice:** (a); (c) is how it is revisited. A raw delete of a `.nupkg` removes the version as a unit (`OnItemsRemoved`), and a raw delete of a derived file alone is a 409 from the same hook: an error of the hook rolls the removal back, so no veto seam is needed.

**Rationale:** the push MAY take a raw `.nupkg` body (R-NUGET-006), so a script has a road; the files seam stays the generic layer's. The development agent would revisit it when a user needs `/files/` upload of NuGet packages (a seam as an optional interface, with the three additions above) or when a second format needs the admission before the body.

**Applied in the spec:** R-NUGET-005 (the opening paragraph and the raw deploy bullet), R-NUGET-003 (the local rule), the immutability rule of R-NUGET-006. Code: none (the seam already behaves so); the test of the deviation is `TestR_STOR_007_RawDeployIntoAFormatRepositoryIsRefused`'s NuGet case, which belongs to the format's integration tests (roadmap WP3.4).

### Q-NUGET-16 · The forwarded-search cache is the engine's, sized by a proxy key

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-NUGET-8 put the forwarded search results in a registered cache `nuget_remote_search` sized by
`nuget.search.remote_result_cache_bytes`. The engine now passes `Volatile` requests through with a
cache of its own ([architecture.md](../architecture.md) Q-ARCH-270), registered at the start of the
process, before any format reads its configuration.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One engine cache `proxy_query_results`, sized by `proxy.query_cache_bytes`; the period stays `nuget.search.remote_result_ttl`, handed per call (chosen) | The format sets what only it knows, the period of its answers | One cache and one budget in the memory guard; the search of a second format shares it | The size key is not under `nuget.search` |
| (b) The format key sizes the engine cache | `Bind` hands the size to the engine | One key under `nuget.search` | The engine registers its caches at New, before the formats are bound; a second format with its own key could not share one cache |
| (c) Two caches | The engine's for the engine, the format's for the format | No change to Q-NUGET-8 | The same bytes twice, and a second set of egress and circuit rules |

**Choice:** (a). The cache is `proxy_query_results`, the size `proxy.query_cache_bytes` (8 MiB; Q-NUGET-8
proposed 32 MiB, which the memory guard of Q-ARCH-270 does not leave room for), the period `nuget.search.remote_result_ttl` (60 s). The key of the
cache is the repository, its configuration version and the method and URL of the upstream request,
so the entry is the upstream response as received, filtered and rewritten on every use, and a change
of the repository's configuration flushes its entries (area `repo_config`).

**Rationale:** Q-NUGET-8's reasoning (no shared state, filter on use, flush on configuration
change) is unchanged; only the owner of the bytes moves. The development agent revisits this when a
format needs a cache budget of its own for a different kind of answer.

**Applied in the spec:** R-NUGET-016 (the cache, the configuration block), R-NUGET-028 (the metric
label), R-NUGET-030's list of per-replica state. Q-NUGET-8 stands for the choice of a per-replica
cache; its cache name and size key are replaced by this decision.

### Q-NUGET-17 · The withheld-query rule is evaluated by the core, on terms

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-016 withholds a query when a term is protected: a claim, a pin rule or an exclude pattern
would keep a package of that name away from the remote, or the term is the literal stem of a prefix
pattern. The plug-in splits `q` into terms; the patterns, claims and pin rules are the core's and
the format sees none of them (R-ARCH-009).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Resolver.KeepsAway(member, term)` in the core, reading the claim index and the compiled patterns (chosen) | The format asks per term and member; the stems are looked up in the claim trie and in the pin rules | No pattern leaves the core; no database query; the same claim index as routing, so a claim change reaches it within the bound of R-ARCH-018 | The core knows the stem rule, a search idea |
| (b) The plug-in reads the claims through a list | A method that lists patterns | The rule is the plug-in's | Every claim, pin rule and exclude pattern of the installation would be visible to format code; a second evaluation of the matcher of R-AUTHZ-005 |
| (c) Probe with names | Ask `Plan` for `term + "."` and so on | No new method | Not a name of the format; an unfoldable name drops every remote (fail closed), so every term would be protected |

**Choice:** (a). A term that is no name of the format keeps nothing away. Include patterns do not
protect a term: they restrict what a repository serves. Through a virtual repository, a pin rule
that does not list the member, or, without a pin rule, a claim in effect there that the member does
not hold, keeps the name away; a prefix pattern of either keeps its stem away. On a remote addressed
directly, a global claim does so while the remote refusal is on (R-REPO-023). The member's own
exclude patterns apply in both cases. The stems of a pattern are its literal part with or without one
trailing `.`, `-` or `_`.

**Rationale:** the format passes names it folds with its own rule, and the core compares them with
patterns folded by the same rule, so the two cannot disagree on spelling. The development agent
revisits this when a second format wants another notion of a stem.

**Applied in the spec:** R-NUGET-016 (the withheld queries name `Resolver.KeepsAway`). Code:
`internal/repository/withheld.go`, `internal/virtual/engine.go`, `internal/formathost/virtual.go`.
Tests: `TestR_REPO_023_KeepsAwayAWithheldTerm`, `TestR_NUGET_016_BridgeKeepsAway`.

### Q-NUGET-18 · The text search configuration and weights of `search_doc`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`search_doc` is a `tsvector` that search ranks and filters on (R-NUGET-010). It needs a text search
configuration and a rule for who builds it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `simple` configuration, built in the insert statement from the parts | No stemming, no stop words; ID tokens weight A, title and tags B, authors and summary C, description D; the format hands over the parts, the statement calls `to_tsvector` | A package ID or a tag is not prose: `windows`, `tools` or `is` are searched as written; the same on every installation whatever `default_text_search_config` says; the weights give the ID the highest rank | Descriptions in English are not stemmed (`widget` does not match `widgets`) |
| (b) `english` configuration | Stemming and stop words | Matches word forms in descriptions | Stems IDs and tags, drops words such as `is` or `the` from a tag search, and fits one language; the result depends on the configuration of the installation unless it is named everywhere |
| (c) The format builds the vector text | The format renders the `tsvector` literal and the store casts it | The store knows nothing of weights | The store would have to trust a literal; the weights and configuration move into Go and into every writer (push, copy, cache fill, reindex) |

**Choice:** (a) — the `simple` configuration, weights A to D, built by the insert statement
(`NuGetInsertVersion`) from the parts of `rows.VersionInput`.

**Rationale:** one statement owns the vector, so the push, the copy, the cache fill and the
reindex (R-NUGET-025) produce the same one. `simple` is the only configuration that does not
change what an ID or a tag means. The development agent would revisit this if a search
for a word form of a description is asked for; an additional vector column with the `english`
configuration, ranked below the ID, would then be an expand step.

**Applied in the spec:** R-NUGET-005: the comment on `search_doc`. The statement is in
`internal/meta/postgres/queries/nuget_local.sql`.

### Q-NUGET-19 · One schema step for the whole NuGet format, with indexes and CHECKs beyond the sketch

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The tables of the format appear on three pages (R-NUGET-005, R-NUGET-006, R-NUGET-020) and one is
P1 (`nuget_symbol_file`, R-NUGET-021). The M2 migrations created every table of the milestone at
once (Q-ARCH-190), so that the generated models change in one place.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One migration, `00017_nuget`, with all four tables | `nuget_version`, `nuget_tombstone`, `nuget_symbol_package` and `nuget_symbol_file`, plus CHECKs and the indexes the foreign keys and the symbol queue need | One expand step; the registry and the backup export classes cover the P1 table from the start (R-NUGET-033); no migration follows in M6 | An empty table until M6 |
| (b) Without `nuget_symbol_file` | The P1 table arrives with R-NUGET-021 | No unused table | A second step in a later release, the first one that must be applied against replicas of the older release |
| (c) As the sketch, no extra indexes | Only the primary keys | Smallest file | A delete of an `items` row scans `nuget_symbol_package` and `nuget_symbol_file` for the cascade; the lag metric and the backfill scan the whole symbol table |

**Choice:** (a). Beyond the sketch: CHECKs that `lower_id`, `declared_id`, `original_version`
and `sort_key` are not empty, that `sha512` has 64 bytes and a symbol `checksum` 32, and that
`index_error` is at most 1024 characters; the indexes `nuget_symbol_package_item`,
`nuget_symbol_package_pending` (rows with `indexed_at IS NULL`), `nuget_symbol_file_version` and
`nuget_symbol_file_item`. `nuget_tombstone` has no foreign key to `package_versions` or `items`,
because it outlives both (R-NUGET-006). The journal of Q-NUGET-10 is a set of objects, not a table.

**Rationale:** a table that backups must export is cheaper to register once. The extra indexes
serve the cascades of the foreign keys and the two scans the symbol work does; none of them is on
a hot path of a push. The development agent would revisit this if M6 changes the shape of
`nuget_symbol_file`; it would then be an expand step like any other.

**Applied in the spec:** R-NUGET-005 notes the CHECKs and indexes under the sketch of
`nuget_version`; R-NUGET-006 and R-NUGET-020 keep their sketches. Migration `00017_nuget.sql` and
[tables.go](../../../internal/formatdata/nuget/tables.go) of the data port register all four tables as `data`
(the lines moved there from `internal/meta/postgres/tables_m3.go` when the format registered).

### Q-NUGET-20 · Dependency version ranges are parsed without floating versions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`NuGet.Versioning` parses a range either with or without floating versions (`1.0.*`, `*`,
`1.0.0-*`). A `.nuspec` dependency is an interval; floating belongs to a project file's
`PackageReference`, where the client resolves it against the feed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse floating versions | A range with `*` answers 400 on every ingestion path | A dependency range of a stored package always has an interval that `Satisfies` can test; the golden vectors are generated in the same mode, so there is one definition | A package that declares a floating dependency cannot be pushed or cached |
| (b) Accept and store the text | Floating ranges parse and print as written | Nothing is refused | trove would implement a second grammar and a floating resolution that nothing in the registry needs; the SemVer 2 classification of a dependency would have no bound to test |
| (c) Accept and fail at use | Parse succeeds, `Satisfies` fails | One parser | A state in which a stored range cannot be evaluated |

**Choice:** (a) — floating versions are refused.

**Rationale:** The registry reads ranges for two things: the `is_semver2` classification of the
package (R-NUGET-004) and the display of dependencies. Both need bounds. The resolution of
floating versions happens in the client, against the feed's version list, which trove serves
without parsing any range. The development agent would revisit this if a restore through a
remote fails on a cached package that declares a floating dependency: the remedy would then be
to keep the unparseable range text and treat the dependency as SemVer 1 for the classification,
not to add floating resolution.

**Applied in the spec:** R-NUGET-003 states the range grammar, the equal-bound rule and the
refusal of floating versions.

### Q-NUGET-21 · Length bound of a version string

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`NuGet.Versioning` has no length bound; the public gallery refuses versions of more than 64
characters. trove stores the original, normalized and URL forms and a `sort_key` of every
version, and rejects nothing else about their size.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 255 bytes, as given and normalized | `Parse` refuses a longer string, and a string whose normalized form (up to four bytes longer) exceeds 255 | One constant; bounds the stored forms, the key and every error detail; far above any real version | A version above 255 bytes that the library accepts is refused |
| (b) 64, the gallery's bound | Same, tighter | Matches the public feed | A private feed's own versions or a cached one above 64 would be refused although nothing in trove needs the bound |
| (c) No bound | As the library | Exact agreement | Client-chosen strings grow rows, keys and index entries without limit |

**Choice:** (a) — 255 bytes, checked on the string as given and on its normalized form.

**Rationale:** The normalized form can be longer than the input (`0` is `0.0.0`), and it is the
form that is parsed again from a stored row, so both bounds must hold or the stored form would
not round-trip; a fuzz target found exactly that. Real versions are far shorter. The development
agent would revisit this if a package on a public feed that restores in practice is longer.

**Applied in the spec:** R-NUGET-003 states the bound in the accepted grammar.

### Q-NUGET-22 · Pre-release identifiers that look like negative numbers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A pre-release identifier may contain `-`, so `1.0.0--5` has the identifier `-5`. The library
reads each identifier with `Int32.TryParse`, which accepts a sign: `-5` is minus five and `-0` is
zero. Verified with the pinned library: `1.0.0--0` and `1.0.0-0` have equal precedence but two
different normalized forms, and `1.0.0--5` sorts below `1.0.0-0`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Order such identifiers as text | `-5` is an alphanumeric identifier like `a` or `-` | Equal precedence and equal URL form stay the same thing, so duplicate detection by URL form, the order and `sort_key` cannot disagree; a key never needs a sign encoding | The order of versions that contain such an identifier differs from the library's |
| (b) Reproduce the library | Encode a signed number in the key | Exact agreement | `1.0.0--0` and `1.0.0-0` would have equal keys and distinct URL forms: two stored versions that the latest-version query cannot tell apart |
| (c) Refuse such versions | 400 | One rule, simple | Versions the library accepts become unservable |

**Choice:** (a) — an identifier of `-` and digits is ordered as text; the version is accepted.

**Rationale:** The order of two versions is observable only through "which is newest" and the flat
container list, and both come from `sort_key`. The rule "same version if and only if same URL
form" (R-NUGET-003) is what duplicate detection, the canonical paths and the tombstones rely on;
(b) would break it for a library quirk that no real package uses, and (c) turns an accepted
version into an error for the same quirk. Only the relative order of versions that contain such an
identifier differs from the library, and the golden-vector test names exactly those vectors and
bounds their number. A digit string above 2147483647 is text in the library too, so that rule
carries no deviation. The development agent would revisit this if the library or a client starts
to reject such identifiers (then (c)), or if a real package uses one.

**Applied in the spec:** R-NUGET-003 states the numeric-identifier rule of the `sort_key` and the
text ordering of signed identifiers.

### Q-NUGET-23 · How the package reader gets its archive limits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-005 sets `max_entries: 65535` and `max_uncompressed_bytes: 4GiB` as bounds on what the central
directory may declare, and says that entries read into memory stay within R-ARCH-028, which a format can only
tighten. The shared zip reader of `internal/formats/untrusted` applies the defaults of R-ARCH-028 to the
whole archive (10,000 entries, 2 GiB for a pass that keeps nothing) and can only lower them. A `.nupkg` with
more than 10,000 files exists (reference and runtime packs).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The NuGet reader scans the central directory itself | `internal/formats/nuget/nupkg` bounds the declared entries and sizes with the `nuget.archive` numbers, compares every local header name, and lets the standard library open entries only after both readers agree; the in-memory bounds are the R-ARCH-028 ones | Spec numbers hold; one place for the NuGet-specific name and overlap rules; the leaf imports only `errs` and the standard library | About 150 lines duplicate the scan of the shared reader |
| (b) Use `untrusted.NewZip` and let the page raise its limits | A second, format-declared ceiling next to the defaults | One scan implementation | Changes the "can only tighten" contract of R-ARCH-028 and the contract package of another work package |
| (c) Use `untrusted.NewZip` and lower the spec numbers to its defaults | 10,000 entries, 2 GiB | No duplicated code | Refuses real packages; the numbers on this page were chosen for them |

**Choice:** (a).

**Rationale:**
- Only the entries the pipeline reads (the `.nuspec`, later the embedded files and the first bytes of PDBs)
  are inflated, and each stays within 1 MiB, ratio 100:1 and 64 MiB in total, so the R-ARCH-028 bounds hold
  where content is held in memory. The declared-size bounds protect later passes that read more.
- The ratio is checked per entry that is read, not per declared entry: a stored or sparse data file may
  compress by more than 100:1 in a valid package, and the archive total already bounds what is declared.
- The development agent would revisit this if the shared reader gains a way for a format page to declare
  higher central-directory bounds, in which case the scan moves there and this package keeps the name rules.

**Applied in the spec:** R-NUGET-005 step 1 (what a refusal covers: split, encrypted, overlapping and
out-of-archive entries; a bound crossed answers 400 like every other rule of the step) and the configuration
note on `nuget.archive`.

### Q-NUGET-24 · The name rules of the central directory beyond the wording of R-NUGET-005

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-005 step 2 names backslash normalization, absolute names, `..`, drive letters, NUL and
case-insensitive duplicates. The NuGet client resolves package parts by their percent-decoded names, and
Windows ignores trailing dots and spaces in a path segment, so the listed rules leave spellings that two
readers resolve differently.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The listed rules only | Check the name as the archive spells it | Exactly the text | `%2e%2e/x`, `lib/.%2E/x`, `.../x` and a duplicate hidden behind `%20` pass |
| (b) The listed rules on both spellings, plus control characters, empty, dot and trailing-dot segments, and entry data that overlaps | Apply every rule to the normalized name and to the once percent-decoded name; the duplicate key is the decoded, case-folded name; an escape may not hide a separator | Closes the differences between readers; a lookup by another case or escape is unambiguous | A name that is odd but harmless (a segment `...`) is refused; none occurs in packages built by the NuGet tooling |
| (c) Refuse every backslash and every percent escape | The strictest reading | Simple | Refuses real packages: older tools write backslashes, and the packaging format percent-encodes characters such as spaces |

**Choice:** (b). A backslash is normalized to `/` as the step says and is not refused.

**Rationale:**
- Percent-decoding happens once, never recursively, which is what a client does; `%252e` stays `%2e`.
- Nothing in a name is used as a file-system path (R-ARCH-028), so the rules protect identity and lookups
  (the `.nuspec`, embedded files and symbols found by name), not a file system.
- One refusal code serves every rule: `nuget_invalid_package` with status 400, whether a structure or a
  bound is at fault, because R-NUGET-005 says "400 on violation" for all of them. The reader returns the
  reason as the error cause so that a test and the audit event can tell the rules apart.
- The development agent would revisit this if a widely used packaging tool writes names that rule (b)
  refuses.

**Applied in the spec:** R-NUGET-005 step 2 (the two spellings, the extra segment and control-character
rules, overlapping entries, the code).

### Q-NUGET-25 · How the `.nuspec` is read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Step 3 of R-NUGET-005 asks for UTF-8 or UTF-16, no document type declaration, no entity expansion and
elements matched by local name in any `nuspec` namespace version. Left open: what "any namespace version"
admits, what happens to repeated identity elements, how the value of an element with markup is read, and
which elements are required at this stage.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Match local names anywhere, first element wins, text of the element itself | The loosest reading | Accepts everything | `<id>` twice, or `<id>A<b/>B</id>`, gives the pipeline another identity than the client reads |
| (b) The root's namespace is empty or under `http://schemas.microsoft.com/packaging/`; children match by local name in that namespace; a repeated `metadata`, `id`, `version`, `dependencies`, `packageTypes` or `license` is refused; `id` and `version` hold text only; other elements read their text in document order; required are `id` and `version` | The identity is the one a client reads, and nothing else is judged here | Closes the identity ambiguity; leaves the content rules (authors, description) to the validations that own them | A document in a foreign namespace is refused (none is produced by the tooling) |
| (c) Validate against the schema | Required elements, types and order | Catches more | A client does not; a package a client installs would be refused, and the schema changed between versions |

**Choice:** (b). Dependency groups and plain dependencies in one document are refused (a client refuses
them as well); an empty group is kept, because it states that no dependency applies to that target
framework. The encoding declaration must match the encoding the document is written in; UTF-16 needs a byte
order mark.

**Rationale:**
- The id and version are what trove authorizes and indexes (R-AUTHZ-005); they have to be the strings a
  client installs. Everything else is metadata for display and search.
- Versions, IDs and ranges are returned as written; their grammar belongs to R-NUGET-003 and R-NUGET-004,
  so the reader never normalizes what another rule has to judge.
- The development agent would revisit this if real packages use another root namespace or a repeated
  element the rules above refuse.

**Applied in the spec:** R-NUGET-005 step 3 (namespaces, repeated elements, element text, the encoding
declaration).

### Q-NUGET-26 · What the counting test upstream serves, and how it has two hosts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-027 asked for a counting stub v3 server "with recorded nuget.org responses". The stub
serves the restore reference (E3.2: at least 100 packages), a package with 200 versions (R-NUGET-015),
failure modes, and counters per path. nuget.org spreads its resources over several hosts, and the
remote repository must follow the `@id` of each resource and allow the extra hosts (R-NUGET-014).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Recorded responses | Trimmed copies of nuget.org registrations, version lists and packages, committed as test data | Real shapes | Real packages and their metadata become repository content with their own licenses; a restore graph of 100 packages needs hundreds of recorded files; "200 versions in non-inlined pages" and "a hash that does not match" need hand edits of the recordings anyway |
| (b) Generated in code | A deterministic package set built in Go: valid `.nupkg` archives with a `.nuspec`, registrations and version lists rendered from the same model; the shapes follow the protocol documents | Any size and any failure is a few lines; nothing third-party is committed; the bytes, hashes and counters are known exactly | The shapes are the stub's reading of the protocol, so a deviation of nuget.org is not caught by CI; the full run on the commit that closes the phase runs against nuget.org for that |
| (c) Both | Generated set plus a recorded subset | Real shapes where they matter | Two sources of truth in one stub; the recorded part needs the licensing and maintenance of (a) |

Hosts: one listener serves every resource under any `Host`. The service index names two bases, the
API host (index, registrations) and the content host (flat container, search). They are configured
(`-public-url`, `-content-url`). Without configuration, a request under a loopback name gets the API
host `127.0.0.1` and the content host `localhost` on the same port, which is two hosts without a
resolver in a Go test; any other `Host` is used for both. A switch (`strict_hosts`) answers 404 to a
resource asked from the other host, so a test can prove that a client follows the index.

**Choice:** (b) — generated in code, with the two-host index above.

**Rationale:**
- The criteria that need a stub (200 versions, one request per `.nupkg`, a changed `packageHash`,
  a package that is gone, a delay of 2 s) are properties of the model, not of recordings; (b) states
  each as data.
- Nothing of nuget.org is committed, so there is no third-party content to track.
- The shape risk of (b) is bounded by the closing run against nuget.org that R-NUGET-027 already
  requires; the stub follows the field names and URL layout of the protocol documents.
- The development agent would revisit this if that closing run finds a shape the stub does not
  have; the fix is then to add the field to the generator and a case to its tests, and recordings
  would be considered only for a document too irregular to generate.

**Applied in the spec:** R-NUGET-027's bullet on upstream behavior in CI. Elsewhere:
[test/nugetstub](../../../test/nugetstub), [docs/developer/e2e.md](../../developer/e2e.md).

### Q-NUGET-27 · The reference solution of E3.2: how it is built and checked

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

E3.2 restores a reference solution of at least 100 packages through `nuget-all` with three clients
at once. The npm reference carries a lockfile resolved by the client against the stub.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A lock file resolved by the .NET SDK | `packages.lock.json` produced by `dotnet restore --use-lock-file` against the stub, committed | The graph the client really resolves | Regenerating needs the SDK image and a running stub on every corpus change; the lock file is not what a restore checks for graph size, and the SDK is not available in every developer environment |
| (b) A generated project, the graph checked in Go | `perf/nuget/reference` (solution, project, `nuget.config`) is written by `nugetstub reference` from the package set; a unit test walks the dependencies through the stub the way a restore does and requires at least 120 packages and one request per `.nupkg`; a second test fails when the committed files differ from a regeneration | No SDK needed to keep it correct; every dependency is a minimum-version range (`1.0.0`) and every version of a package has the same dependencies, so the graph does not depend on which versions the client picks | Not proof that the real client resolves the same graph; the e2e run of E3.2 is that proof |

**Choice:** (b).

**Rationale:**
- The property E3.2 depends on, the size of the restore graph, is a fact about the package set and
  is checked exactly, with a margin of 20 over the required 100.
- Minimum-version ranges and equal dependencies across versions make the resolution unique, so the
  Go walk and the client agree by construction; the e2e run compares them (the stub's counters
  must show one request per `.nupkg`).
- The development agent would revisit this if the e2e run resolves a different graph (a floating
  range or a warning that changes resolution); a lock file would then be added next to the project.

**Applied in the spec:** none (E3.2 of [roadmap.md](../../coverage/roadmap.md) already states the
requirement). Elsewhere: [perf/README.md](../../../perf/README.md).

### Q-NUGET-28 · The skeleton registers the class local; remote and virtual join with their adapters

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-001 lists three classes. `formats.Validate` refuses a format that lists a class and returns no adapter for it, and conformance checks 4 (rewrite) and 5 (merge) run over the adapters a format has; a skip is refused once the adapter exists. The remote adapter (discovery document, fill inspection, search forwarding) and the virtual adapter (merge, the artifact prefix) are separate work packages (WP3.2a, WP3.2c).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Classes: [local]` until the adapters exist (chosen) | The remote and virtual classes are added in the change that writes their adapter, with the format enums of the API and the catalog following the registry | The registry never lists a class the format cannot serve; no adapter is written twice; checks 4 and 5 are skipped for what does not exist and start the day the adapter does | The catalog facts that only a remote repository reads (upstream, periods, checksum rule) are declared before the class that reads them |
| (b) All three classes with adapters that answer 501 | `Remote()` and `Virtual()` return empty adapters | The class list is final from the first day | A repository of a class that serves nothing can be created through the API; the empty adapters must be thrown away; checks 4 and 5 need fixtures for them |
| (c) All three classes and no adapters | `Validate` panics at registration | None | Not possible |

**Choice:** (a). The facts of a remote repository (`DefaultUpstreamURL`, `DefaultExtraHosts`, the 300 s and 1800 s periods, `ChecksumRequireUnsupported`) and the `forwardSearch` member of the settings are declared now, so that the catalog answers for the whole format from the start and the adapters add no catalog change.

**Rationale:** a class is a promise that a repository of it works. The development agent revisits this when the remote adapter lands (the same change lists `remote` and adds it to the enums). The virtual adapter has landed (Q-NUGET-56): the class is listed and `VirtualFormatName` carries `nuget`.

**Applied in the spec:** R-NUGET-001 (the paragraph after the format registration). Code: [format.go](../../../internal/formats/nuget/format.go) `Descriptor`. Tests: `TestR_NUGET_001_TheDescriptorCarriesTheCatalogFacts`, `TestR_NUGET_001_ARepositoryCanBeCreatedAndItsServiceIndexAnswers` (a remote create is refused; a virtual one is created by `TestR_NUGET_001_AVirtualRepositoryOfTheFormatCanBeCreatedThroughTheManagementAPI`).

### Q-NUGET-29 · Routes match protocol paths, descriptors speak canonical paths

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: for npm a protocol path and a canonical path are one string (`lodash/-/lodash-1.0.0.tgz`). For NuGet they are not: the flat container serves `v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg` for the item `acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg` (R-NUGET-005), and the registrations are cache entries at `acme.widgets/.registration/index.json` (R-NUGET-015). Conformance check 3 asks `Classify` about the examples of every route and check 10 asks `Match` for the package-document path of a name; both assumed one string.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Classify` and `PackageDocumentPath` speak canonical paths; the fixture of the kit maps between the two (chosen) | `Fixture.Canonical` maps a route example to its canonical path (check 3), `Fixture.Protocol` maps a canonical package-document path to the path a client requests (check 10); `nuget.CanonicalPath` is the mapping the handlers use | `/files/`, the remote engine and the virtual engine see one kind of path; nothing in the format guesses which kind it was given; npm and generic leave both fields nil | Two optional fields in the fixture of the kit |
| (b) `Classify` accepts both kinds | A path starting with `v3/` is a protocol path | No kit change | The package ID `v3` has the canonical path `v3/index.json`, which is also the service index: the two cannot be told apart |
| (c) The canonical path is the protocol path | Items are stored below `v3/flatcontainer/` | No mapping | Contradicts the storage layout of R-NUGET-005 and the raw file address `/files/<key>/acme.widgets/…` of its acceptance criteria |

**Choice:** (a). A route that addresses no stored path (the service index, search, the writes) has no canonical path and is held to its kind alone.

**Rationale:** the kit exists to catch a format that confuses the two, so it has to know there are two. The development agent revisits this when a second format has a protocol prefix of its own.

**Applied in the spec:** R-NUGET-001 (the paragraph after the format registration). Code: [paths.go](../../../internal/formats/nuget/paths.go), [kit.go](../../../internal/formats/formattest/kit.go) (`Fixture.Canonical`, `Fixture.Protocol`), [checks.go](../../../internal/formats/formattest/checks.go). Docs: [formats.md](../../developer/formats.md). Tests: `TestR_ARCH_010_Conformance` of the NuGet package, `TestR_NUGET_001_CanonicalPathOfARoute`.

### Q-NUGET-30 · A route is named by its handler name and, after a dot, its operation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-028 names eleven handlers for the HTTP metrics. The route name is the handler label of the request log and of `trove_http_requests_total`, and a name is unique in the table of routes. The flat container has four routes (list, package, `.nuspec`, embedded files) and the registrations three (index, page, leaf).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The handler name plus `.operation` where a resource has several routes (chosen) | `nuget.flatcontainer.nupkg`, `nuget.registration.leaf`; a resource with one route keeps its bare name (`nuget.search`) | Unique, greppable, and a prefix match gives the handler name of the table; no change to the contract | The label has more values than the eleven of the table |
| (b) One route per handler name, dispatching inside | A matcher per resource and a handler that looks at the path | Exactly the names of the table | The route table stops being the list of operations; the coarse check, `Kind` and `Action` differ per operation and a route has one of each |
| (c) Free names and a mapping to the eleven for the metric | The metric label is derived | Both lists as they are | A second list to keep in step |

**Choice:** (a). Every operation of the table is one route with its own kind and action; the handlers are methods named in the table and live in `handlers_stub.go` until the package that builds them moves them into a file of its own, so the table is written once.

**Applied in the spec:** R-NUGET-028 (the handler names). Code: [routes.go](../../../internal/formats/nuget/routes.go). Tests: `TestR_NUGET_028_RouteNamesAreTheHandlerNames`.

### Q-NUGET-31 · The settings member and the configuration section hold what has a behaviour in the MVP

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the spec names `deleteMode` (R-NUGET-007), `forwardSearch`, `symbolServerUrl` and `vulnerabilityInfo` (R-NUGET-014) as members of the `nuget` member of a repository document, and the keys `nuget.archive.*`, `nuget.registration.*`, `nuget.search.*` in the system file. The schema is `additionalProperties: false` and R-REPO-008 refuses a setting rather than ignoring it. The metadata period of Q-NUGET-2 is the generic `cache.metadataTtlSeconds` of a remote repository (R-REPO-024) with the format's default of 300 s, not a member of `nuget`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The member holds `deleteMode` and `forwardSearch`; `symbolServerUrl` and `vulnerabilityInfo` arrive with R-NUGET-022 and R-NUGET-023 (chosen) | Each later member is an additive change of the schema, of `NugetSettings` and of the equality test | The schema accepts only what some code reads | The schema grows in three steps |
| (b) All four members now | The P1 members are accepted and stored | The schema is final | An administrator can set `symbolServerUrl` and nothing happens, which R-REPO-008 forbids |
| (c) The member is an opaque object | No schema | Nothing to keep in step | No validation, no create form, no equality with the document |

**Choice:** (a). The section of the system file is `nuget.archive` (`max_nuspec_bytes` 1 MiB, `max_entries` 65535, `max_uncompressed_bytes` 4 GiB, `max_compression_ratio` 100), `nuget.registration` (`page_size` 64, `inline_below` 128) and `nuget.search` (`default_take` 20, `max_take` 1000, `max_skip` 3000, `remote_result_ttl` 60 s, Q-NUGET-16). The two bounds on what is read into memory (`max_nuspec_bytes`, `max_compression_ratio`) can only be tightened, as R-ARCH-028 says of every format, so their ranges end at the defaults; `default_take` above `max_take` is refused.

**Rationale:** the development agent revisits this with R-NUGET-022 and R-NUGET-023, which add their members in the change that reads them.

**Applied in the spec:** none (R-NUGET-005, R-NUGET-007, R-NUGET-009, R-NUGET-010, R-NUGET-016 already state the keys and defaults). Code: [settings.schema.json](../../../internal/formats/nuget/settings.schema.json), [nuget.go](../../../internal/config/nuget.go). Docs: [configuration.md](../../developer/configuration.md). Tests: `TestNuGetDefaults`, `TestNuGetRanges`, `TestR_ARCH_008_NugetSettingsSchemaEqualsTheOpenAPIMember`.

### Q-NUGET-32 · An operation that is not built answers 501, and an upload is refused

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the skeleton routes every operation of the table and serves the service index; the data port, the ingestion, the documents and search are the work of later packages. Until they exist a request for one of them must answer something, and the roads into a repository that bypass the routes (the upload of the management API, copy and move) must not store a package that has no NuGet rows.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The routes exist and answer 501 `nuget_not_implemented`; `Inspect` refuses every upload (chosen) | The table is final and the kit runs over it; a client reads a failure of the server; the authorization of the coarse check has run | No package is stored without its rows; the later packages replace handlers and edit no table | Resources are listed in the service index that answer 501 for the time between the packages |
| (b) Leave a route out until its handler exists | The table grows | The service index lists what works | The path answers `nuget_v2_unsupported`, which says the wrong thing, and every package that adds a route changes the table of the others |
| (c) 404 for the unbuilt | A client reads "no such package" | None | A restore reads "no such package" and moves on to the next source, which hides the gap |

**Choice:** (a). The service index lists the MVP resources of R-NUGET-002 from the start.

**Rationale:** the window is one phase on a development branch. The development agent revisits this if a release is cut with a handler still a stub, which the roadmap does not plan.

**Applied in the spec:** R-NUGET-002 (the note on the resources). Code: [handlers_stub.go](../../../internal/formats/nuget/handlers_stub.go), [format.go](../../../internal/formats/nuget/format.go) `Inspect`. Tests: `TestR_NUGET_001_AnOperationThatIsNotBuiltAnswers501`. The `Inspect` clause is replaced by [Q-NUGET-38](#q-nuget-38--the-roads-around-the-push-route-inspect-reads-the-package-the-commit-refuses-it): `Inspect` reads the package, and the 501 moved to the commit of an upload that is not a push; the push, the unlist, the relist and the delete are built.

### Q-NUGET-33 · The clients image: one .NET SDK, and nuget.exe on Mono

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-027 runs the .NET 10 SDK on Linux and nuget.exe on a Windows runner. The e2e suite runs in kind on Linux, so a
nuget.exe row there needs a runtime for a .NET Framework executable. The SDK image of the golden vectors
(`NUGETVECTORS_SDK_IMAGE`) is Ubuntu 24.04, whose `universe` archive carries Mono 6.8.0.105 for amd64 and arm64.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The SDK image plus Mono from its Ubuntu, nuget.exe by version and SHA-256 | `test/e2e/nugetclients/Dockerfile`: `mono-complete` and `ca-certificates-mono` at an exact apt version, `nuget.exe` from dist.nuget.org checked against a digest in `tools/versions.env` | The protocol of nuget.exe (its own HTTP stack, `nuget.config` handling, `-ApiKey`, `search`, `install`) runs against trove on every change, not only on the Windows runner; one base image with the SDK of the vectors; nothing from a third-party apt repository | Mono 6.8 is unmaintained and its TLS stack stops at TLS 1.2 (a listener with `min_version: "1.3"` cannot be reached by it); the apt version is exact, so Ubuntu replacing the package fails the build until the pin is moved |
| (b) nuget.exe on the Windows runner only | No Mono; the Linux image has `dotnet` only | No unmaintained runtime in CI | The Windows runner is outside the per-change kind suite, so every nuget.exe-specific behavior (the pre-authentication of R-NUGET-001 for clients older than the SDK's, `-ApiKey` header, the symbol push address) is seen only on the closing run |
| (c) A current Mono from the Mono project's apt repository | A newer runtime | Maintained; TLS 1.3 may work | An apt source outside the pinned images, whose packages cannot be pinned by digest; a second source of base packages in a test image |

**Choice:** (a). The .NET SDK is the image of the golden vectors: `E2E_NUGETCLIENTS_SDK_IMAGE` and `NUGETVECTORS_SDK_IMAGE` are
the same value and `make versions-check` (`test/e2e/nugetclients/check-pins.mjs`) fails when they differ, so the repository has one
.NET SDK. nuget.exe is 7.9.0 (the NuGet.Versioning of the vectors is 7.9.0, too), with its SHA-256.

**Rationale:**
- Verified in this change, on linux/arm64: `nuget help` prints 7.9.0.83 on Mono 6.8.0.105; `nuget install` fetches a package
  from the counting test upstream over TLS and verifies the dev CA's chain; `nuget search` against the dev edge gets trove's
  HTTP status, not a certificate error; the same two commands without the CA fail on the certificate
  (`test/e2e/nugetclients/smoke.sh`, `TestNuGetClients`). Not verified: linux/amd64 (CI's architecture; the Ubuntu package and
  nuget.exe are the same, the image is not built there yet), and any push, delete or symbol push by nuget.exe on Mono.
- The Windows row of R-NUGET-027 stays: Mono is a substitute for the protocol coverage per change, not for the platform.
- The development agent would revisit this if a nuget.exe scenario of the suite fails on Mono and passes on Windows for a reason
  of the runtime (then that scenario runs on the Windows runner only and the Mono row keeps the rest), if Ubuntu drops Mono
  (then (b)), or if the suite needs TLS 1.3 on a listener nuget.exe talks to.

**Applied in the spec:** none (R-NUGET-027's table already lists nuget.exe; the Linux row for it is the addition this entry
describes). Elsewhere: [test/e2e/nugetclients](../../../test/e2e/nugetclients), [tools/versions.env](../../../tools/versions.env),
[docs/developer/e2e.md](../../developer/e2e.md#the-nuget-clients).

### Q-NUGET-34 · The e2e sets of the NuGet tests

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`deploy/dev/e2e.sh` runs a set as `go test -run <regexp>`. The npm sets list their test families by name. The NuGet tests are written
by later packages, and the tests that run only nightly or on the closing run (the HA scenarios, the additions of the last
run) must not run on every change.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An explicit list per set, as for npm | `^TestNuGet(Clients\|Matrix\|...)` | The set says what it contains | Every new test family edits the script, and a test whose family is not listed is silently not run by any set |
| (b) `^TestNuGet` with `-skip '^TestNuGet(Closing\|HA)'` | The sets `nuget-closing` (`^TestNuGetClosing`) and `nuget-ha` (`^TestNuGetHA`) take the families the name rule reserves; `nuget` takes the rest | A new test is in the per-change set by being named `TestNuGet...`; the exceptions are two prefixes, not a list | A name decides the tier: a nightly test must start with `TestNuGetClosing` or `TestNuGetHA` |
| (c) One set per test file | A set per `nuget_*_test.go` | Fine control | A file per set, and the same drift as (a) |

**Choice:** (b). `TROVE_E2E_RUN` replaces both the expression and the skip, so a single subtest can be run by hand. The set
`nuget-clients` runs `^TestNuGetClients$` alone (the smoke test of the pods).

**Rationale:** the run that finds "matched no test" a failure (`e2e.sh`) cannot find a family that no set names, so the rule must
be the one that includes by default. The development agent would revisit this if a third tier appears (then a prefix per tier, or a
list as in (a)).

**Applied in the spec:** none. Elsewhere: [deploy/dev/e2e.sh](../../../deploy/dev/e2e.sh) (marker `M3 nuget-clients`),
[docs/developer/e2e.md](../../developer/e2e.md#the-nuget-clients).

### Q-NUGET-35 · How the clients pod gets the dev CA, and what a test must set per run

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The dev CA is generated per cluster (cert-manager), so it cannot be in the image. `dotnet` reads its trust from OpenSSL
(`SSL_CERT_FILE`, `SSL_CERT_DIR`), Mono from its own store (`~/.config/.mono/certs`, filled by `cert-sync`). The pod's root
filesystem is read-only. NuGet keeps a package folder (`NUGET_PACKAGES`) and an HTTP cache (`NUGET_HTTP_CACHE_PATH`) in the home directory.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The entrypoint builds the trust at pod start | `entrypoint.sh` writes `$SSL_CERT_FILE` (the system roots plus the mounted CA), runs `cert-sync --user` on it, writes a `ready` file that the readiness probe checks, then becomes `fwd` | Every `kubectl exec` of every test finds the trust done; the CA is mounted, never baked; the pod is not ready before both runtimes trust the CA | A pod start takes a few seconds longer (the sync of about 120 roots) |
| (b) The CA is mounted and each test passes options | `-p:...`/`--ca` flags per call | No start step | `dotnet` and nuget.exe have no option for an extra CA; it would be a wrapper per call, with the sync cost on every Mono invocation |
| (c) The CA in the image | Built per cluster | No start step | The CA of a recreated cluster differs from the image's; a stale CA looks like a client bug (a stale copy of `.dev/pki/ca.crt` in a checkout showed exactly that while this was built: the clients failed on the certificate of a cluster that had been recreated) |

**Choice:** (a). A test gives every run its own `NUGET_PACKAGES` and `NUGET_HTTP_CACHE_PATH` below `/work`, because the
default folders are shared by all tests of a pod: a package already unpacked there, or a metadata document in the HTTP cache,
makes a restore skip the server and the counters of the test upstream (E3.2: one request per `.nupkg`) would count nothing.
`HOME` stays `/work` (an emptyDir), where Mono keeps its store.

**Rationale:** verified in a pod of the dev cluster (restricted security context, read-only root): both runtimes verify the edge
and the test upstream, and refuse a certificate without the CA. The development agent would revisit this if a client needs the CA
in a place this does not reach (a tool that reads `/etc/ssl/certs` directly): the entrypoint would then also write a hashed
directory for `SSL_CERT_DIR`.

**Applied in the spec:** none. Elsewhere: [test/e2e/nugetclients/entrypoint.sh](../../../test/e2e/nugetclients/entrypoint.sh),
[docs/developer/e2e.md](../../developer/e2e.md#the-nuget-clients).

### Q-NUGET-36 · The order of the checks of a push, and how a repeated push is told from a rebuild

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the identity of a package is inside it, so the permission on the ID, the number and the shadowing check can only be decided after the body is spooled and read (R-NUGET-005 step 5). R-NUGET-006 wants a repeated push to touch nothing and the commit to be safe against a concurrent delete. The data port already reads the SHA-512 of a stored `.nupkg` (it is the digest of the tombstone and of the journal entry); the specification compared the push with the stored lead item by SHA-256.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decide outside the transaction, again inside it; compare by SHA-512 (chosen) | Spool, read the package, authorize the declared ID, ask the ownership rule early, then read the number from PostgreSQL alone: stored with the same SHA-512 answers 201 and ends; stored with other bytes, or tombstoned, answers 409; otherwise run the shadowing check and commit. The commit transaction locks the package row, reads the versions and the tombstone after it, and decides again | A replay holds no lock, writes no blob, no row, no revision and no event; one digest serves the replay, the tombstone and the journal; a refusal costs no transaction | The number is decided twice |
| (b) Compare by SHA-256 through the item store | Stat the canonical path of the `.nupkg` and compare the item's SHA-256 | The wording of the first version of the requirement | Needs the item row and its path rules where the data port has the facts; a second digest beside the one the tombstone and the journal use |
| (c) Decide only in the commit transaction | Every push goes to the commit | One place | A replay would write its blob and take the package lock only to change nothing; a refused push would cost a transaction |

**Choice:** (a). Identical bytes are SHA-512 equal, exactly as they are SHA-256 equal.

**Rationale:** the replay is the case the client's own retry produces (R-NUGET-032), so it must be cheap and must leave nothing behind. The early check is an optimization and a better answer; the safety is the check inside the commit, which is also what a push racing a hard delete meets. A push whose commit loses a race for the path (the item store answers 403 for a path that took other content) is read again and answers what the number now says. The development agent would revisit this if the stored blob stopped carrying its SHA-512.

**Applied in the spec:** R-NUGET-006 (the comparison is by SHA-512); R-NUGET-028 (the label of `trove_nuget_push_replayed_total` is `repo`, the label name R-OPS-007 allows; the generated `.nuspec` records an `artifact.deployed` of its own, because the item store records one per file). Code: [handlers_publish.go](../../../internal/formats/nuget/handlers_publish.go), [hooks.go](../../../internal/formats/nuget/hooks.go). Tests: `TestR_NUGET_006_IdenticalBytesOfAStoredVersionAnswer201AndChangeNothing`, `TestR_NUGET_006_AReplayChangesNothing`, `TestR_NUGET_006_ConcurrentPushesOfOneVersion`.

### Q-NUGET-37 · What a GET of a stored file serves

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `ServeCheck` runs before the core serves any stored item, on the flat-container routes and on `/files/` alike. The item row says nothing about listing or about a removed number; the NuGet rows and the tombstone do.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Serve a stored version, listed or not; refuse a tombstoned one and one without a NuGet row (chosen) | One lookup of the NuGet row and the tombstone by the version of the path | Unlisting hides a version from search and keeps it restorable (R-NUGET-007); a file that reached the repository around the format is not served (fail closed); the check keeps no state, so it holds on every replica | One indexed lookup per served file |
| (b) Refuse an unlisted version | Unlisting stops downloads | Closer to a withdrawal | Breaks the restore of every project pinned to the version, which is what unlisting is meant not to do |
| (c) Check only the tombstone | A version without a NuGet row is served | Cheaper by a join | A file with no document that lists it is served from `/files/` |

**Choice:** (a).

**Rationale:** the specification defines unlisting as not-in-search and not-in-autocomplete, with the version still in the version list and downloadable. A tombstone only exists for a version that is not stored, so a tombstoned file that is still an item (a restore in progress, a trashed version) must not be served either. The check applies to local repositories; a remote repository's rules join with its adapter. The development agent would revisit this when the trash can exists (P1) and decides what a trashed version's files answer.

**Applied in the spec:** none beyond R-NUGET-007, which says the same. Code: [servecheck.go](../../../internal/formats/nuget/servecheck.go), statement `NuGetServeState`. Tests: `TestR_NUGET_007_AServeCheckDecidesOnTheNuGetRows`, `TestR_NUGET_007_UnlistRelistAndDeleteOverHTTP`.

### Q-NUGET-38 · The roads around the push route: `Inspect` reads the package, the commit refuses it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NUGET-32 made `Inspect` refuse every upload until the ingestion existed, so that no package is stored without its NuGet rows. The ingestion now exists for the push. The upload of the management API, copy and move reach the format through `Inspect` and `OnCommit`, and neither writes the generated `.nuspec` or the NuGet row.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Inspect` reads and names the package (422 for a refusal); `OnCommit` answers 501 to a commit that is not a push (chosen) | The identity and the rules are one code path; the kit's check 6 has a subject; the roads stay closed until their package exists | No package without its rows; the reading is tested once | The upload answers 501 after the body was read |
| (b) `Inspect` keeps refusing | Nothing changes for the roads around the push | Smallest | The kit's check 6 stays skipped with no subject, and the push has its reading in a function `Inspect` does not share |
| (c) Build the upload road now | The upload derives the path and writes the companions | Complete | It is the work of the package that builds upload, copy and move (R-NUGET-005) |

**Choice:** (a).

**Rationale:** the push answers 400 for what the package rules refuse (R-NUGET-005, the clients print the status), while `Inspect` answers 422 `content_refused` with the kit's contract (check 6 accepts 413 and 422) and as npm does. A path that names another ID or version than the package declares is refused, so that the identity a road authorizes is the identity the package declares. The development agent would revisit this when the upload road is built; the 501 then goes.

**Applied in the spec:** none (Q-NUGET-32 stays, its `Inspect` clause is replaced by this decision). Code: [format.go](../../../internal/formats/nuget/format.go) `Inspect`, [hooks.go](../../../internal/formats/nuget/hooks.go) `OnCommit`. Tests: `TestR_NUGET_005_InspectReadsTheIdentityOfAPackage`, `TestR_NUGET_006_AnUploadAroundThePushRouteIsRefused`, kit check 6.

### Q-NUGET-39 · A delete goes through the item store and the hook writes the tombstone

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a version leaves a local repository by the protocol `DELETE` in `delete` mode, by a raw or management delete of its `.nupkg`, and by a move (P1). Each must write the tombstone with the SHA-512 of the removed package in the transaction that removes the version (R-NUGET-006). The hook of the item store runs after the item rows are deleted, so the row that held the digest is gone when the hook runs.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The delete route removes the files through the package writer; `OnItemsRemoved` removes the version and writes the tombstone for every road; the removal event carries the SHA-256 of the removed content and the store reads the SHA-512 from the blob row (chosen) | One code path for the protocol, raw, management and move deletes | A raw delete cannot forget the tombstone; the route and the hook cannot disagree | A field is added to the contract's `RemovedItem` |
| (b) The route writes the tombstone and the rows, then removes the files | Reads the digest from the item before it goes | No contract change | A raw delete needs a second copy of the logic, which is the road a rule gets forgotten on |
| (c) Store the SHA-512 in `nuget_version` | The digest survives the item row | No contract change, one statement less | A column added to a migration that other work packages generate against; the digest would exist twice |

**Choice:** (a). `formats.RemovedItem` gains `SHA256`, set by the host and by the kit's Host. The blob row outlives the transaction, because its collection is queued.

**Rationale:** the rule "every removal writes a tombstone" is only safe where there is one place that removes. The generated `.nuspec` or an embedded file cannot be removed on its own while the package stays: the hook refuses that with 409 and the removal rolls back (Q-NUGET-13). A delete that finds a version without a package file fails whole (500) instead of removing the version without a tombstone. The development agent would revisit this if more formats need the digest of a removed item (the field is then part of the frozen contract anyway).

**Applied in the spec:** none (R-NUGET-005, R-NUGET-006 and R-NUGET-007 already say that a removal is a unit with a tombstone). Code: [hooks_remove.go](../../../internal/formats/nuget/hooks_remove.go), [handlers_delete.go](../../../internal/formats/nuget/handlers_delete.go), [formats/hooks.go](../../../internal/formats/hooks.go) `RemovedItem`, statement `NuGetInsertTombstone`. Tests: `TestR_NUGET_007_TheDeleteOfADeleteModeRepositoryRemovesTheVersionForGood`, `TestR_NUGET_005_ARawDeleteRemovesTheVersionAsAUnit`, `TestR_NUGET_005_ARawDeleteOfThePackageIsAHardDelete`.

### Q-NUGET-40 · The reads of a hook run in its transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the tombstone is read inside the commit transaction, after the package row lock (R-NUGET-006). The data port first read it on a connection of its own. Ten identical pushes at once, five on each of two replicas, answered nine of them 503 after the lock timeout of five seconds: each waiting transaction held a connection of the pool, and the one that held the lock needed a second connection to read the tombstone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Store.Tombstone` takes the transaction; a nil transaction reads outside one (chosen) | The hook reads in the transaction that holds the lock | Needs one connection per request; sees the state the lock protects (READ COMMITTED) | A nil-able argument |
| (b) A second connection for the read | As written first | One signature | The pool can starve itself: a transaction waits for a connection that only the transactions it blocks can free |
| (c) A larger pool | The symptom goes at this load | No code | It returns at a higher load |

**Choice:** (a). A hook never takes a second connection.

**Rationale:** the failure is a property of the design, and the test that found it is part of the suite. `READ COMMITTED` makes a read in the transaction after the lock see every removal that committed before. The development agent would revisit this if the runner's isolation level changed.

**Applied in the spec:** none. Code: [store.go](../../../internal/formats/nuget/store.go) `Tombstone`, [internal/formatdata/nuget/store.go](../../../internal/formatdata/nuget/store.go). Tests: `TestR_NUGET_006_ConcurrentPushesOfOneVersion`, `TestR_NUGET_006_APushRacingAHardDeleteSeesTheVersionOrItsTombstone`.

### Q-NUGET-41 · The instant a document shows: whole seconds, UTC

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `published` appears in every catalog entry and leaf (R-NUGET-009). PostgreSQL keeps `published_at` to the microsecond; the replicas must send the same bytes for the same rows (R-NUGET-030), and the spec's own sketch shows `2026-10-28T09:14:03Z`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Whole seconds, UTC, `Z`, cut (chosen) | `2026-10-28T09:14:03Z` | Matches the sketch; one spelling whatever the zone or the driver; a time never reads later than it was | Two pushes within a second show the same instant |
| (b) Milliseconds | `2026-10-28T09:14:03.987Z` | Finer | A fraction no client uses; the sketch changes |
| (c) The stored microseconds | `…03.987654Z` | Lossless | Trailing zeros are trimmed or not depending on the formatter, a second way to differ between releases |
| (d) An offset (`+00:00`) as nuget.org writes it | `2026-10-28T09:14:03+00:00` | Looks like nuget.org's | Two spellings of UTC to keep apart; the clients parse both |

**Choice:** (a).

**Rationale:**
- The order of versions comes from the version rules, never from `published`, so no client needs finer resolution than a person reading the date.
- Cutting and not rounding means a document never shows a time later than the commit.
- The development agent would revisit this if a NuGet client orders by `published` and two pushes within a second matter.

**Applied in the spec:** R-NUGET-009 (the `published` row). Code: [document.go](../../../internal/formats/nuget/document/document.go) `Timestamp`. Tests: `TestR_NUGET_030_TimestampIsFixedPrecision`.

### Q-NUGET-42 · What a registration page URL answers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-009 says a page URL is answered with the visible leaves inside its bounds at request time, so a URL from an older index stays valid. It does not say what happens when no leaf is inside, or when the bounds are no range.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A page for every range of an ID that has a visible version; 404 for bounds that are no range (chosen) | `lower`..`upper` inclusive, `count` 0 and an empty `items` when none is inside; `@id`, `lower`, `upper` in URL form | A URL from an older index never turns into an error because a version was deleted; the answer is the truth about the range | Any range of an existing ID answers 200 |
| (b) 404 when no leaf is inside | The page exists only while it holds a leaf | A URL that names nothing is an error | A client holding an older index fails its restore at the deletion of the last version of a page, and an ID's document turns on and off with deletions |
| (c) Only the bounds of the current index answer | A page is the page of the index being served | Strictest | Every push changes the bounds and breaks the pages of every cached index |

**Choice:** (a).

**Rationale:**
- It is the only option that keeps the promise of R-NUGET-009 for a delete, which is the one change that can empty a page.
- The extra 200s cost one read of rows the replica reads anyway for any other document of the ID.
- The development agent would revisit this if a client probes page URLs to find out whether an ID exists.

**Applied in the spec:** R-NUGET-009 (paging). Code: [registration.go](../../../internal/formats/nuget/registration/registration.go) `Doc.Page`. Tests: `TestR_NUGET_009_PageBoundsAreAnsweredAtRequestTime`, `TestR_NUGET_034_DocumentsOnAnEndpointWithExposureRules`.

### Q-NUGET-43 · What a catalog entry takes from the stored metadata

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `nuget_version.metadata` is a JSON object (R-NUGET-005) written at ingestion and rewritten by the reindex (R-NUGET-025); the registration documents render from it. The table of R-NUGET-005 names its keys but not what a registration does with an absent boolean, an empty group or an ID it cannot link.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A typed read of the documented keys, and absent stays absent (chosen) | `requireLicenseAcceptance` is shown only when the row holds it; a dependency group is kept with its `targetFramework` even when it has no dependency, and `{}` for a group with neither; `tags` is a list (a string is split on white space); `dependencies[].version` is the `range`; unknown keys are ignored; a damaged object is a 500 | Matches "absent values omitted"; an empty group keeps its meaning (the package needs nothing on that framework); a later release can add a key | A client that expects `requireLicenseAcceptance` always finds it absent for a package whose row lacks it |
| (b) Always emit `requireLicenseAcceptance` and an empty `dependencyGroups` as nuget.org does | Defaults filled in | Looks like nuget.org's | Documents show values the package never declared |
| (c) Pass the stored object through | The registration is the row | No mapping to keep | The row's keys (`releaseNotes`, `copyright`, `repository`, …) leak into a protocol that did not ask for them, and a key rename of the row changes the protocol |

**Choice:** (a). The keys are the ones of the table in R-NUGET-005.

**Rationale:**
- NuGet clients read an absent `requireLicenseAcceptance` as false and an absent `dependencies` as none, so omission costs nothing and declares nothing that was not declared.
- A group without a framework and without dependencies still tells a client something, so it stays.
- The reader is the contract with the writer: the work that writes the row must produce these keys, and the reindex repairs a row that does not.
- The development agent would revisit this if a client is found that requires the always-present members.

**Applied in the spec:** R-NUGET-009 (catalog entry mapping). Code: [metadata.go](../../../internal/formats/nuget/registration/metadata.go), [registration.go](../../../internal/formats/nuget/registration/registration.go). Tests: `TestR_NUGET_009_CatalogEntryFields`, `TestR_NUGET_009_ParseMetadata`, `TestR_NUGET_009_GoldenDocuments`.

### Q-NUGET-44 · How a document and a package file are delivered

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-002 fixes the weak validator and R-NUGET-009 the gzip hives; three details are left to the code: when gzip is chosen, whether a document carries a freshness lifetime, and which media type a package file has when the item row recorded another.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) gzip on explicit consent; no `Cache-Control` on documents; the media type of the protocol on files (chosen) | A wildcard in `Accept-Encoding` is not consent; the plain hive never compresses; a document is revalidated with its ETag; `.nupkg` is `application/octet-stream` and `.nuspec` `application/xml` | Every client reads the identity coding; a stale document cannot be served from a cache without asking; the type does not depend on who stored the item | One request per document and restore when the client's own cache has expired (NuGet keeps its HTTP cache for 30 minutes) |
| (b) A wildcard counts as consent | `Accept-Encoding: *` gets gzip | Fewer bytes for a client that never sends it anyway | A client that lists `*` may not read gzip |
| (c) `Cache-Control: private, max-age=N` on documents | As the npm documents do | Fewer requests | A client would keep a version list after a push for N seconds; a restore right after a push misses the version (R-NUGET-030's push-then-restore flow) |
| (d) The media type recorded in the item row | Whatever ingestion stored | One source | A row written by a restore or a reindex with another type changes the protocol |

**Choice:** (a). The validator's inputs are the render version, the repository and package ids, `packages.rev_seq`, the document, the key the document is served under, the base URL, the endpoint with its exposure fingerprint, the repository's configuration version and the paging settings; the content coding is not one.

**Rationale:**
- The push-then-restore flows of R-NUGET-030 cannot tolerate a lifetime on the version list.
- The paging settings are inputs because they change the bytes of an index, and a replica with other settings must not answer 304 for them.
- The development agent would revisit this if the cost of revalidation is measurable on a restore of a large graph; the answer would be a short lifetime on the registration pages only.

**Applied in the spec:** R-NUGET-008 (media types), R-NUGET-009 (bytes). Code: [documents.go](../../../internal/formats/nuget/documents.go), [handlers_flat.go](../../../internal/formats/nuget/handlers_flat.go), [document.go](../../../internal/formats/nuget/document/document.go). Tests: `TestR_NUGET_002_WeakETagIsSharedByGzipAndIdentity`, `TestR_NUGET_009_GzipHivesNegotiateAndThePlainHiveNever`, `TestR_NUGET_008_FilesOfAVersion`, `TestR_NUGET_009_PagingFollowsTheSettings`.

### Q-NUGET-45 · How `totalHits` stays exact for a caller whose reads are decided row by row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-010 and R-AUTHZ-016 say `totalHits` counts only what the caller may read. The decision point answers a listing through `Resolver.ReadScope`: `ReadAll` (no per-row question), `ReadNone`, or `ReadRows` (a grant with package patterns, a token scope, an endpoint with exposure rules), where `MayRead(repo, package, version)` decides each row in memory. SQL cannot ask it. The answer for `ReadRows` must be exact, must not hold every match in memory, and must not show a version the caller may not read.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Scan in rank order, keep the page, count the rest (chosen) | The store returns the matches in rank order in batches of 500 with the keys and package types of every listed visible version; the format asks `MayRead` per version, counts the packages with a readable version and keeps only the entries of `[skip, skip+take)`; the page is then read once for the readable versions alone. A query that matches more than 200,000 packages answers 400 | `totalHits` is exact; memory is bounded by `take` (at most `nuget.search.max_take`) and one batch; `version`, `versions`, `packageTypes` come from readable versions only; `ReadAll` callers, the common case, keep the cheap SQL count and page | Time is linear in the matches for a `ReadRows` caller; a caller with a narrow grant on a very large repository must narrow the query |
| (b) Fetch a window of `skip + take` candidates and filter | Read `max_skip + max_take` candidates, drop the unreadable | One statement, constant cost | `totalHits` is not exact, and a page can be short or empty while readable packages follow it |
| (c) Push the grant into SQL | Pass the include and exclude patterns to the statement | Exact and paged by SQL | A second implementation of the pattern grammar, the token scope and the exposure rules that must agree with the decision point forever (R-API-029 decided against it for the package listing) |
| (d) Answer without an exact `totalHits` for these callers | `totalHits` is the size of the page | No scan | Breaks R-NUGET-010 and the acceptance criterion `totalHits: 1` |

**Choice:** (a). The bound of 200,000 is the one the package listings use for the same decision (R-API-029): above it the request answers 400 and never a partial count. The latest version a result shows is the latest the caller may read, and the `packageType` filter is decided on that version, so an unreadable newer version neither appears nor changes what the caller sees.

**Rationale:** exactness and the absence of a second grammar outweigh the cost of the scan, which only callers with row-level grants pay, and only on repositories with many matches. The development agent would revisit this if a deployment reports 400 for such callers (the scan would then move into a statement over the readable package names the decision point can hand over), or if profiling shows the scan dominates the CPU of a replica.

**Applied in the spec:** R-NUGET-010 (the implementation paragraphs). Code: [handlers_search.go](../../../internal/formats/nuget/handlers_search.go) `runSearch`, `searchRows`; [search.go](../../../internal/formatdata/nuget/search.go). Tests: `TestR_AUTHZ_016_SearchCountsOnlyWhatTheCallerMayRead`, `TestR_AUTHZ_016_SearchPagesOverTheReadableRowsOnly`, `TestR_AUTHZ_016_TheScanReadsKeysAndFetchesOnlyThePage`, `TestR_AUTHZ_016_ThePackageTypeFilterUsesTheLatestReadableVersion`, `TestR_AUTHZ_016_AScanBeyondItsBoundIsRefusedNotCutShort`, and the integration tests `TestR_AUTHZ_016_TotalHitsCountsWhatTheCallerMayRead`, `TestR_AUTHZ_016_ARowByRowScanCrossesItsBatches`.

### Q-NUGET-46 · What a package is matched by, which fields exist, and how a query is bounded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-010 says a term matches "through `search_doc`", which is one vector per version, while a result is one object per package. The brief for the work package adds the fields `version:` and `owner:` to the ones in the requirement. Q-NUGET-12 chose token search over a trigram index, which loses a substring in the middle of a token, yet the requirement says a free-text term matches when "contained in the ID". A query is typed by anyone with `read`, and every term costs a test per package.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Match the latest visible version's row; substring by `strpos` on the ID; `version:` over the visible versions; `owner:` never matches; bounds of 1,024 bytes and 16 terms (chosen) | `DISTINCT ON (lower_id)` picks the row, every term is tested on it | What matches is what the result shows (title, tags and description of the latest version); the requirement's "contained in the ID" holds without an extension; the plan is the same for every term | A scan of the latest version of every package of the repository per request; text deep inside a word of a description is not found |
| (b) Match any version's row, then show the latest | Use the gin index on `search_doc` to find candidate versions | Uses the index | A package is found by a tag its latest version dropped; the result does not show why it matched |
| (c) Treat `owner:` as free text | The word `owner:alice` is searched in all fields | No field that matches nothing | A search for a person returns packages that happen to contain the word |
| (d) No bounds on the query | | Nothing to refuse | A request of thousands of terms multiplies the per-package test |

**Choice:** (a). Free text is matched by the ID substring or by the words of the term as prefixes, in the order written (the phrase operator), against the ID tokens (weight A) and the other fields; a field term reads that field of the metadata object. An unknown `field:` prefix stays free text. `owner:` is accepted so that a search for an owner does not turn into a word search, and matches nothing because a local package has no owners (the result has no `owners`). The rank tiers are exact ID, ID prefix, ID substring or token, other fields; ties by `lower_id` in byte order, so the order is the same on every installation whatever the collation of its database.

**Rationale:** one row per package is the only reading under which a result and a match agree. The cost of the scan is linear in the packages of one repository, which a local repository bounds by what its teams publish. The development agent would revisit this if a repository holds so many packages that the statement dominates a replica: the candidate set then returns to the gin index for the terms that are ID tokens and the substring test moves to a trigram index (an expand step, Q-NUGET-12). It would also revisit `owner:` if the requirement grows an owner table to read.

**Applied in the spec:** R-NUGET-010 (the table of fields, the ranking, the bounds, the implementation paragraph and three acceptance criteria). Code: [query.go](../../../internal/formats/nuget/search/query.go), [params.go](../../../internal/formats/nuget/search/params.go), [nuget_search.sql](../../../internal/meta/postgres/queries/nuget_search.sql). Tests: `TestR_NUGET_010_ParseSplitsTheQueryIntoTerms`, `TestR_NUGET_010_ParseBoundsTheNumberOfTerms`, `TestR_NUGET_010_ParamsRefuseWhatIsOutOfBounds`, and the integration tests `TestR_NUGET_010_RankingFollowsExactPrefixTokenAndOtherFields`, `TestR_NUGET_010_IDTokensAndSubstringsMatch`, `TestR_NUGET_010_FieldTermsLimitTheMatch`.

### Q-NUGET-47 · The search port: a count, a page, keys only, and a read of named versions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the skeleton declared `Search(ctx, SearchQuery) ([]SearchHit, error)` and left the cut to the format. Q-NUGET-45 needs the format to count in two ways and to read the page of a scan with the readable versions alone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Search` pages (`Offset`, `Limit`), `KeysOnly` leaves the metadata out, `Only` names the versions to read; `SearchCount` counts (chosen) | One method for the page and the scan, one for the count | The port stays two methods; the shared part of the statements is written once per statement | The twin of the port must honour the three query fields |
| (b) One method that returns every match | The format cuts | One method | Unbounded memory for the common caller |
| (c) A cursor object the format iterates | `OpenSearch` returns an iterator | Natural for a scan | A stateful value across the port and its transaction |

**Choice:** (a). `Only` replaces the terms and the page: the hits are the packages holding those listed versions, each with those versions alone and `Latest` the highest, so a version the caller may not read cannot reach the result through the metadata of a newer one. The tsquery text is built in the data port from the folded value of a term (letters and digits only, lower case), so no character a user types reaches the query language.

**Rationale:** it keeps the format free of SQL and the data port free of the permission model. The development agent revisits this if a second listing needs the same scan (autocomplete, R-NUGET-011), when the scan moves next to the shared code.

**Applied in the spec:** none (the port is not specified). Code: [store.go](../../../internal/formats/nuget/store.go), [read.go](../../../internal/formats/nuget/rows/read.go), [search.go](../../../internal/formatdata/nuget/search.go). Tests: the integration tests of Q-NUGET-45 and Q-NUGET-46, which run the three statements.

### Q-NUGET-48 · What the package screens read of a NuGet version, and where the README comes from

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the package screens of [api-and-ui.md](../api-and-ui.md) R-API-029 show a package under its original ID casing, a description, keywords, licenses, a README and a format-specific `metadata` object (the UI reads npm's members; the schema of the others is the format page's). The NuGet tables hold the metadata of a version as JSON (R-NUGET-005), not a README: the README is a file inside the `.nupkg` that the `.nuspec` names. R-NUGET-024 extracts it into a generated item, but that requirement is P1 and the ingestion of M3 does not write the item.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the file out of the stored `.nupkg` when the README is asked for (chosen) | The data port opens the lead item's blob with ranged reads (the central directory, then the one entry), through a reader the composition root binds | Works for pushed and cached packages from M3 on; no column, no item and no write path; the same `nupkg` leaf that ingests reads it; only the README tab pays | A README view costs a few ranged reads of the object store; `hasReadme` is "the `.nuspec` names a README file", so a file that is missing, larger than 1 MiB or not UTF-8 gives `hasReadme: true` and a `404` README |
| (b) The generated `.embedded/readme` item of R-NUGET-024 | The ingestion extracts the file; the view reads the item | Cheap, and `hasReadme` is exact | P1: nothing writes the item in M3, so no package would have a README; the view would need an item reader as well |
| (c) The README text in `nuget_version.metadata` | The ingestion copies up to 1 MiB into the JSON | One SQL read | Every registration and search read carries it; a restore or a reindex rewrites it |

**Choice:** (a), with these rules for the rest of the view:

- The description is the `.nuspec` `description`, the `summary` when there is none; the keywords are the `tags`; the licenses are the license expression when the license element is of type `expression`, and none for a license file or a bare `licenseUrl`.
- `metadata` holds `id` (the declared casing of this version), `listed`, `title`, `summary`, `releaseNotes`, `copyright`, `language`, `minClientVersion`, `licenseUrl`, `homepage` (`projectUrl`), `repositoryUrl`, `authors` (the authors string split at commas), `tags`, `packageTypes` (the names), `dependencyGroups` (`[{"targetFramework", "dependencies": [{"id", "range"}]}]`, a flat dependency list being one group without a framework) and, when true, `requireLicenseAcceptance` and `developmentDependency`; a member that does not apply is absent. `authors`, `homepage` and `repositoryUrl` are the names the UI already reads for npm.
- Versions order by `version.Compare`; two spellings NuGet treats as one version (the case of a label) are broken by the string, so the order is total as check 11 demands.
- `Stored` lists every live version of the ID, listed or not, in the URL form: newest arrival first for a local repository (the version id grows with every insert), newest published first for a remote one (the version id breaks a tie). A remote repository lists the versions it has cached, as the packages API does (R-API-029: remote repositories contribute their cached versions only); the version list of the upstream is a cached document and not a row. A virtual repository stores nothing.
- `Withheld` is empty: NuGet withholds no stored version. A removed version has no row but its tombstone (R-NUGET-006), and the replay of a restore reports the versions it would revive without touching them (Q-NUGET-10).

**Rationale:** (a) is the only option that gives M3 a README without a second ingestion path, and the cost lands on the one tab that needs it. The deviation of `hasReadme` is small and visible (an empty README tab); the exact answer needs the flag the P1 extraction can write when it lands, and the development agent would revisit this then: R-NUGET-024 can make `hasReadme` exact by writing the item and the view can read that instead. The ranged reads are not verified against the blob digest ([storage.md](../storage.md) R-STOR-013 verifies full reads only); the text is rendered by the sanitizer of R-UI-008, never executed.

**Applied in the spec:** none in a requirement; this block is the schema of the `metadata` object that [api-and-ui.md](../api-and-ui.md) R-API-029 leaves to the format page. Code: [view.go](../../../internal/formats/nuget/view.go), [formatdata/nuget/view.go](../../../internal/formatdata/nuget/view.go), [nuget_view.sql](../../../internal/meta/postgres/queries/nuget_view.sql), [wire_nuget_blobs.go](../../../internal/app/wire_nuget_blobs.go). Tests: `TestR_API_029_NuGetDescribesSummaryAndDetails`, `TestR_EP_016_NuGetStoredListsWhatTheRepositoryServes`, `TestQ_NUGET_48_TheDataPortReadsTheViewFromPostgreSQL`, `TestR_API_029_NuGetPackagesAreListedAndDescribed`.

### Q-NUGET-49 · The setup snippets: one command or file per client, the credential only where it is needed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-026 and R-UI-018 fix the commands. The core rewrites the token reference of a template by the reader's credential mode and, for a reader without credentials, drops every line that holds a reference (R-API-030). A NuGet command is one line with a continuation; dropping the line that holds `--password` leaves the line before it ending in a backslash, and dropping the `ClearTextPassword` line of a `nuget.config` leaves a source with a user name and no password. The user name is not checked (R-NUGET-012); `SetupData.Username` is not filled by the API, and the kit's check 8 refuses the word "token" in a template.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The credential part is conditional on `RequiresAuth`, with a fixed user name `trove` (chosen) | The `--username … --password …` part, its nuget.exe counterpart and the `packageSourceCredentials` block sit inside a conditional on `RequiresAuth`; the API clears `RequiresAuth` for credentials=none | One command per snippet, as the specification shows; an anonymous reader and a repository open to anonymous readers get a complete command and a valid file; no dropped-line case | A repository open to anonymous readers gets a snippet without credential even when the reader asked for the environment reference (a restore needs none; a push carries the API key) |
| (b) Always the credential, as npm's `.npmrc` | The credential is on a line of its own | The same shape for every reader | For credentials=none the line goes and leaves a dangling continuation or a source with a user name only |
| (c) Two commands: `add source`, then `update source … --username … --password …` | The credential line is a command of its own | Drops cleanly | Two commands where the specification shows one; `update source` is a different command to explain |

**Choice:** (a). The user name is the fixed `trove` (the specification's earlier `token` is a word check 8 refuses in a template, and `<username>` would need the API to fill a field it does not). The `dotnet` snippets come for a POSIX shell and for PowerShell, the `nuget.exe` snippets for cmd and PowerShell, each in the token spelling of its language (Q-API-187 to Q-API-189); the `nuget.config` is a file template for all three clients. Visual Studio has the `nuget.config` and the `PackageReference` of a project: the install template places the version through `VersionSpec` as `" Version="<version>`, which closes the `Include` attribute and opens the `Version` attribute, so the API's one substitution yields `<PackageReference Include="acme.widgets" Version="1.2.3" />`. The publish snippet names the repository the reader chose (a virtual repository forwards the push) and carries the token as the API key.

**Rationale:** the failure of (b) and (c) is in the one case the snippets exist for: a copy that works. (a) keeps the specification's single command and moves the decision to the data the API already clears. The development agent would revisit this if users of anonymously readable repositories need the credential in the file for pushes: the API key travels with the command, so none has asked. A push with `credentials=none` is one line that holds the reference and is dropped whole; a signed-in reader who asks for that gets an empty publish snippet (the guide's publish section is empty for an anonymous reader anyway).

**Applied in the spec:** R-NUGET-026 (the commands and the paragraph "Which snippets exist, as built"); [api-and-ui.md](../api-and-ui.md) R-UI-018. Code: [setup.go](../../../internal/formats/nuget/setup.go). Tests: `TestR_NUGET_026_TheTemplatesSpellTheCredentialInTheirOwnSyntax`, `TestR_NUGET_026_TheNuGetConfigIsWellFormed`, `TestR_NUGET_026_TheCommandsAreTheOnesOfTheSpecification`, `TestR_NUGET_026_TheInstallTemplatesTakeAVersion`, `TestR_NUGET_026_TheSetupSnippetsOfEveryClient`, `TestR_NUGET_026_TheSetupWithoutACredential`.

### Q-NUGET-50 · The guide: its sections, its clients, and the classes that are not registered yet

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-UI-049 and R-UI-019 want a guide for every class and every client the format supports, from the phase the format ships. The descriptor lists the class `local` only until the adapters of the others exist (Q-NUGET-28), and check 8 fails a guide section written for a class the descriptor does not list. Visual Studio has no command to push, unlist, relist or delete. The audit topic of the npm guide has no NuGet counterpart before the vulnerability information of R-NUGET-023 (P1).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Write all three classes now; `Guide()` returns the sections of the registered classes (chosen) | `guideSections()` holds the text of every class; `Guide()` drops a class the descriptor does not list from a section's classes, and a section left without class | The text exists and is tested from the first day (the tests render all three classes); the remote and virtual class need no guide work when they land; check 8 passes | The remote and virtual text cannot be reached through the API until the class is registered |
| (b) Write the text of a class with its adapter | The sections are added in WP3.2a and WP3.2c | Nothing unreachable in the code | Contradicts R-UI-019 for the phase in which the format ships its guide; the class text is written without the whole format around it |
| (c) Declare the classes in the descriptor now | The descriptor lists three classes | The guide is reachable for all | `formats.Validate` refuses a class without an adapter (Q-NUGET-28) |

**Choice:** (a). The sections are `what` (one per class), `configure`, `authenticate`, `install`, `publish`, `maintain`, `lockfiles`, `endpoint` and `troubleshooting`; no `audit` section. `publish` and `maintain` are written for `dotnet` and `nuget` only, for the classes `local` and `virtual`. The troubleshooting table names the codes of the push (`nuget_version_exists`, `nuget_version_deleted`, `nuget_version_reserved`) with the status the specification gives them; the test accepts a code that the ingestion has not registered yet with that status and checks it like any other once it is.

**Rationale:** the guide is the user's documentation of the format; the text for a class must not wait for the code of the class. The development agent would revisit this when the remote and virtual adapters land: the change that lists the class deletes nothing here, and the kit then covers the class's sections as well.

**Applied in the spec:** R-NUGET-026 (the guide bullet). Code: [guide.go](../../../internal/formats/nuget/guide.go), [guide/](../../../internal/formats/nuget/guide). Tests: `TestR_UI_049_NuGetHasAGuideForEveryVariant`, `TestR_UI_049_TheNuGetTroubleshootingCodesAreTroves`, `TestR_UI_049_EveryCodeTheNuGetGuideNamesExists`, `TestR_NUGET_026_TheGuideStatesTheDefaults`, `TestR_UI_049_TheNuGetGuideShowsTheSnippetsOfTheClientSetup`.

### Q-NUGET-51 · What a NuGet remote stores of an upstream, and where its registration pages live

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-015 lists the descriptors of a remote (the service index, the version list, the registration index and "every non-inlined page") and the three hives a client can ask for. The engine stores a metadata document per path and variant, keeps it fresh, and uses the URL map for metadata paths as well (Q-ARCH-268). The page path of the table was `page/{n}.json`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One variant `doc`, the bytes of the upstream as they came; one upstream hive stored (`RegistrationsBaseUrl/3.6.0`, else `/3.4.0`, else the unversioned type); the three hives are renderings; a page is keyed by its bounds, `page/{lower}/{upper}.json` (chosen) | The registration index is stored once and its non-inlined pages once each, under the paths the engine collapses and refreshes | One upstream request per document and period however many hives clients use; a stored page that is fresh can only ever be served for the range it holds | An upstream that has only the unversioned hive shows no SemVer 2.0.0 versions in any hive |
| (b) One stored document per trove hive | Three upstream hives are fetched | A hive could be passed through | Three times the requests and the storage; a client of the SemVer 1 hive would fetch a document the SemVer 2 hive already holds |
| (c) A page keyed by its position, `page/{n}.json` | The sketch of R-NUGET-015 | Short paths | The index can name another range for position `n` while the entry for `n` is still fresh, and trove would serve the old range's leaves for the new bounds until the entry expires |

**Choice:** (a). The service index is the discovery document at `.nuget/index.json`; a feed whose index lacks `PackageBaseAddress/3.0.0` or every registration type answers the first request with 502 `upstream_error` naming the resource, and nothing is stored (Q-NUGET-14). The index's `PrepareFill` records the URL-map row of a page whose address is not the one derived from the registration base, and of a leaf whose `packageContent` is not the derived package address; a derived address needs no row.

**Rationale:** the engine already owns freshness, collapsing and the circuit per path, so every document that has its own path gets them; the hive, the paging and the SemVer 2.0.0 filter are functions of the stored bytes and the serving repository (Q-NUGET-52), so they cost no request. The development agent revisits this when an upstream turns up that serves the SemVer 2.0.0 hive only in a form the stored hive cannot supply, or when a measurement shows that stored pages of old bounds pile up faster than the cache's retention removes them.

**Applied in the spec:** R-NUGET-015 (the table row of the registration documents). Code: [remote.go](../../../internal/formats/nuget/remote.go) `parseTarget`, `PrepareFill`, `RegistrationPagePath`; [discovery.go](../../../internal/formats/nuget/remote/discovery.go). Tests: `TestR_NUGET_015_ACanonicalPathIsTakenApartOrIsNoResourceOfARemote`, `TestR_NUGET_015_PrepareFillRecordsOnlyTheAddressesThatAreNotTheDerivedOnes`, `TestR_NUGET_014_DiscoveryPicksTheResourcesInTheOrderOfTheSpec`, `TestR_NUGET_014_AnUpstreamWithoutPackageContentAnswers502OnTheFirstFetch`, `TestR_NUGET_015_AnIDWithManyVersionsIsRePagedAndCollapsed`.

### Q-NUGET-52 · A remote's registration is parsed and rendered again; what it keeps of the upstream's entry

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by Q-NUGET-85**: `deprecation` and `vulnerabilities` are kept without the upstream's `@id` and `@type`, and `advisoryUrl` joins the web links of people; by Q-NUGET-83 for the bounds of one rendering.

Context: R-NUGET-015 says the stored upstream documents stay unchanged, registration documents are not passed through, trove renders its own index, pages and leaves with the paging of R-NUGET-009, catalog entry properties are copied and unknown properties are kept. Every address in the rendered documents except `projectUrl`, `licenseUrl` and `iconUrl` must be trove's. An upstream may store a hive gzip-coded whatever the request asked for.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Parse the stored index and pages into the rows the local renderer reads, keep `deprecation`, `vulnerabilities` and the unknown members that hold no address, and render with `registration.Doc`; inflate a gzip-coded document within the bound (chosen) | One renderer for both classes; the hive filter, the paging and the addresses are the renderer's | Deterministic bytes; the same `ETag` on every replica | A member that holds an address is not kept, and a catalog entry's order of members is trove's |
| (b) Pass the stored document through and replace the upstream's addresses in the text | A textual rewrite | Every member is kept | It cannot re-page (R-NUGET-009), cannot filter a hive, and misses an address in a member it does not know |
| (c) Keep every unknown member | (a) without the address rule | Nothing is lost | An unknown member such as `readmeUrl` or an alias list would carry the upstream's address into a document that must not |

**Choice:** (a). `readmeUrl` is left out as R-NUGET-009 says. The SemVer 2.0.0 flag of a version is the flag of its version and of the bounds of its dependency ranges (R-NUGET-004). A leaf whose version does not parse is left out; a missing `listed` is true; a missing ID is the ID of the request. `authors` as a list and `tags` as a string are read as well. The `ETag` of a document is a hash of the render version, the repository, the serving key, the response base URL, the endpoint with its exposure fingerprint, the configuration version, the paging, and the SHA-256 of every stored document the rendering reads; it is computed before the rendering, so a 304 costs the reads of the engine and no parse. The pages an index does not inline are listed from the replica's rendition cache under the index's digest, so the index is parsed once per document, not once per request. `Rewrite` of the adapter renders the semver2 hive with the default paging.

**Rationale:** the renderer is the one the local class tests pin down byte by byte, and a remote differs from a local repository only in where the rows come from. The address rule is the only way to keep "unknown properties are kept" and "no upstream address" true at once. The development agent revisits this if a client turns out to read a member that the address rule dropped.

**Applied in the spec:** none (R-NUGET-015 already states the behaviour; the address rule is the detail). Code: [catalog.go](../../../internal/formats/nuget/remote/catalog.go), [documents.go](../../../internal/formats/nuget/remote/documents.go), [handlers_remote.go](../../../internal/formats/nuget/handlers_remote.go), [registration.go](../../../internal/formats/nuget/registration/registration.go). Tests: `TestR_NUGET_015_ConvertTakesTheMetadataTheDocumentsRender`, `TestR_NUGET_004_ARangeWithASemVer2BoundMakesTheVersionSemVer2`, `TestR_NUGET_009_ARemoteRegistrationIsRenderedPerHiveWithTheDocumentsOfTheUpstream`, `TestR_NUGET_015_RegistrationsAreRenderedOnTheServingRepository`, `TestR_NUGET_015_AGzipHiveIsAnsweredToEveryClientInTheCodingItAccepts`, `TestR_ARCH_028_DecodeInflatesGzipWithinTheBoundAndOnlyThen`, `TestR_NUGET_030_ReplicasRenderTheSameBytesAndTheSameValidator`.

### Q-NUGET-53 · The packages a remote caches: inspected before they are stored, recorded with origin `cache`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by Q-NUGET-84**: only content no reader takes for a package is served without the identity check, every other refusal of the reader is 502 `upstream_package_mismatch`; the declared ID is compared as a client lower-cases it, not under case folding, and the rows take the path's ID.

Context: R-NUGET-015 parses every cached `.nupkg` like an upload, records it with the rows of origin `cache`, serves and caches a package that cannot be read, and refuses a package whose declared identity is not the asked one with 502 `upstream_package_mismatch`. Q-ARCH-267 gives the format a hook that runs before the item is committed and before a byte reaches a client.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `InspectFill` reads the package and holds its identity to the path; an unreadable package gives no inspection; `OnCommit` of a fill records the version from the inspection, and the removal of a cached item removes its version (chosen) | The hook is the check, the commit hook is the record | Nothing is stored for a mismatch; the rows are written in the transaction that stores the item | The hook has no replica, so the archive limits are the defaults (H-336) |
| (b) Record the rows from the registration | No inspection | Rows exist before the package | The rows would be the upstream's word, and a package that was never fetched would be a version (H-337) |
| (c) Refuse an unreadable package with 502 | Stricter | No uninspected bytes in the cache | R-NUGET-015 serves and caches what it cannot read, and a feed with an unusual package would fail a restore |

**Choice:** (a). The ID of the `.nuspec` is compared with the path's under Unicode simple case folding (`id.SameID`), the version by its URL form; the ID passes the upstream rule only through that equality, because the path's ID was validated by it. A symbol package is not refused. A version that is recorded already (the item was evicted and fetched again) keeps its row and takes the new item as its lead item. Nothing is claimed, no tombstone is written and no event is recorded: no request published anything and a remote has no owners. The removal of a `.nupkg` of a remote's cache (an eviction, the items API) removes the version row with its NuGet row and raises the package's revision, and writes no tombstone, because a remote can fetch the version again. An unreadable package is logged where it is found and counted at the commit, where the replica is known: `trove_nuget_remote_unparseable_packages_total{repo}` (the spec's label `repository` is `repo`, as for `trove_nuget_push_replayed_total`).

**Rationale:** the identity is the only check an upstream without a digest allows (H-35); doing it in the hook keeps every waiter of the fetch on one verdict. The development agent revisits the limits when the contract gives a fill hook the replica (H-336).

**Applied in the spec:** R-NUGET-028 (the label of the counter). Code: [remote_fill.go](../../../internal/formats/nuget/remote_fill.go), [hooks.go](../../../internal/formats/nuget/hooks.go), [hooks_remove.go](../../../internal/formats/nuget/hooks_remove.go). Tests: `TestR_NUGET_015_InspectFillHoldsTheDeclaredIdentityToThePath`, `TestR_NUGET_003_InspectFillComparesANonASCIIIDAsAClientLowerCasesIt`, `TestR_NUGET_015_AnUnreadablePackageIsServedAsItCameAndGetsNoRows`, `TestR_NUGET_015_AFilledPackageIsRecordedWithTheRowsOfOriginCache`, `TestR_NUGET_015_RemovingACachedPackageRemovesItsVersionAndWritesNoTombstone`, `TestR_NUGET_015_AnIdentityMismatchIs502AndStoresNothing`, `TestR_NUGET_015_APackageOfAnotherVersionAtTheURLOfThisOneIs502`, `TestR_NUGET_015_APackageThatIsNoPackageIsServedAsItCameCountedAndGetsNoRows`.

### Q-NUGET-54 · The `.nuspec` of a remote, and a package address the flat container does not serve

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-015 extracts the `.nuspec` from the cached `.nupkg` and falls back to the upstream's, and fetches the leaf's `packageContent` when the flat container answers 404 for a version the registration lists. The engine has one upstream URL per path (the derived one, or the URL-map row), records a 404 of an artifact as a negative entry, and has no second candidate.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The `.nuspec` is fetched from the upstream as an artifact of its own; the leaf's address is used once a registration of the ID that trove read has recorded it in the URL map (chosen) | Two paths, two cached artifacts; the map row is the address | No engine change; a restore from the flat container, as every client does, is one request per file | The `.nuspec` costs a request of its own and is not checked; the first request of a version whose flat container address differs from the registration's, before any registration was read, is the flat container's 404 |
| (b) Read the `.nuspec` out of the cached package and fall back to the upstream | The spec's sketch | One fetch for both files | The hook would have to store a second item of a fill, which the contract does not offer |
| (c) After a 404, read the registration and ask again | The spec's sketch | Covers the first request | The 404 is a negative entry for the path (R-REPO-028) and the second request would meet it; the engine would have to offer a candidate list |

**Choice:** (a). The registration read of an ID records the leaf addresses that differ from the derived ones, and the next request of that version uses the recorded address (`TestR_NUGET_015_APackageTheFlatContainerDoesNotHaveIsFetchedFromThePackageContentOfItsLeaf`). A client that restores from the flat container alone, against a feed whose flat container lacks a version its registration lists, is served by trove as the feed serves it: 404.

**Rationale:** the protocol's package content resource is the flat container, which a conforming feed serves for every version it lists (the test upstream does); the fallback protects against a feed that does not, at the cost of a contract change (a candidate list in the engine). That a real feed needs it is not verified. The development agent revisits this when a feed turns up where the first request fails, by adding the candidate to the engine.

**Applied in the spec:** R-NUGET-015 (the two table rows). Code: [remote.go](../../../internal/formats/nuget/remote.go) `packageMappings`, `UpstreamRequest`. Tests: the one named above, `TestR_NUGET_015_PrepareFillRecordsOnlyTheAddressesThatAreNotTheDerivedOnes`, `TestR_NUGET_015_APackageFileOfARemoteIsFetchedThroughTheEngineAfterTheReadPermission`.

### Q-NUGET-55 · The seam for the forwarded search: the adapter maps the search resource, a remote searches its cache until then

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the forwarded search of a remote (R-NUGET-016) is a later work package. The contract passes a `Volatile` request through `RemoteFetcher.Query`, which asks the adapter for the upstream request with `UpstreamQuery`. The road of `Query` builds the adapter's context without the discovery document (`internal/formathost/query.go`), and the search resource is announced in the service index.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The adapter maps the query to the search resource of `RemoteContext.Discovery` and answers an internal error when it is absent; the road of `Query` is extended by the work package that forwards (chosen) | The format side is done and tested; the host side is one call | The seam is exercised by a unit test; nothing changes in the engine now | The forwarded search needs a change in the host before it works |
| (b) Extend the road of `Query` in this package | The seam works end to end | Complete | It changes the host and the contract's tests of the engine, which belong to the package that forwards |
| (c) A reserved parameter that carries the resolved search URL | No host change | Works now | The key of the cached answer would hold an address the format put in; a second way to address the upstream beside the service index |

**Choice:** (a). Until the forwarding exists, the search of a remote repository answers from the NuGet rows of its cache (origin `cache`), as R-NUGET-016 describes for `nuget.forwardSearch: false`; the setting `forwardSearch: true` has no effect yet. The `Query` of a remote is not called by the format.

**Rationale:** the host side belongs to the package that gives the setting a meaning. The development agent revisits this when that package starts: it resolves the discovery document in `internal/formathost/query.go` the way `fetcher.discover` does and hands it to `UpstreamQuery`.

**Applied in the spec:** none. Code: [remote.go](../../../internal/formats/nuget/remote.go) `UpstreamQuery`, [discovery.go](../../../internal/formats/nuget/remote/discovery.go) `SearchURL`. Tests: `TestR_NUGET_016_TheQuerySeamMapsAPassThroughSearchWhenTheIndexIsResolved`, `TestR_NUGET_015_SearchURLSortsTheParameters`, `TestR_NUGET_015_APackageIsCachedWithItsRowsAndServedWhenTheUpstreamIsDown` (the cache-only search).

Revisited on 2026-11-02, when the forwarded search was built (finding F-20 of the phase 4 review): the choice stands, and the condition it named has come. The host resolves the service index on the road of `Query` the way `fetcher.discover` does and hands it to `UpstreamQuery` ([architecture.md](../architecture.md) Q-ARCH-282); the seam needed no change of the frozen contract. `forwardSearch: true` now forwards, and the sentence "the setting has no effect yet" of the choice no longer holds. What a forwarded answer becomes is Q-NUGET-78, when a remote forwards Q-NUGET-79, the names the withheld-query rule asks about Q-NUGET-80, and a remote member's part of the merged search Q-NUGET-81.

### Q-NUGET-56 · What the members of a virtual repository hand to the merge, and what the merge hands back

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by Q-NUGET-82**: a remote member's registration is read by the adapter from the stored index, inflated, and the pages it lists by address, and is merged whole; the merge fingerprint includes the merged bytes.

Context: the merged-metadata cache stores one entry per virtual repository, canonical path and variant, and keeps it until a member's revision changes. NuGet serves the same ID in three hives, under the address of whichever virtual repository the client named, with an exposure view of its endpoint, and a remote member stores the upstream's documents unchanged (R-NUGET-015). The merge must take one document per member and give one document back.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Merge rendered documents | Each member renders the registration for the serving address and hive; the merge splices the pages | The merged bytes are the answer | One cache entry per hive and per address; the remote document is rewritten before the merge; the paging of the union is computed from rendered pages |
| (b) A neutral document per member and a neutral merged document, rendered after the cache (chosen) | A local member hands over its stored versions with their metadata, a remote member its stored upstream index with every page inlined, the version list is the flat container's own document; the merge picks the entry of each version by tier and position and records the member; the handler renders the hive, the paging and the addresses | One cache entry serves three hives and every address; the merged bytes hold no URL a request chose; the same rendering code as a local repository, so the paging and the SemVer 2 rule cannot differ | The handler parses the merged document on a rendition miss; the remote adapter must be able to hand over an index with its pages inlined |
| (c) Merge the leaves from the leaf URLs | Fetch each leaf | None | A request per version |

**Choice:** (b). The paths are the ones R-NUGET-015 stores (`{lower_id}/index.json` and `{lower_id}/.registration/index.json`), the variants are `flat` and `registration`. A page the upstream lists by URL alone cannot be merged: the member fails as an upstream failure, and the merge never lists a prefix of its versions. The validator is the weak value of R-NUGET-002 over the merge fingerprint of the engine (the members after pin rules and claims, the revision of each local member, the digest of each remote document) and the serving inputs; a partial merge keeps neither the engine's entry nor the rendition.

**Rationale:** the engine already stores one entry per member set, and the ETag inputs of R-NUGET-002 are exactly the fingerprint; rendering after the cache keeps the format's single rendering path. The development agent revisits this when a remote repository with more than a few thousand versions of one ID makes the inlined index too large to merge on every rebuild (the member document would then carry only the entries of the versions the merge window shows), or when a second hive needs different member documents.

**Applied in the spec:** R-NUGET-017 (member documents, the partial merge, the per-ID read check). Code: [merge.go](../../../internal/formats/nuget/merge/merge.go), [upstream.go](../../../internal/formats/nuget/merge/upstream.go), [virtual.go](../../../internal/formats/nuget/virtual.go), [handlers_virtual.go](../../../internal/formats/nuget/handlers_virtual.go). Tests: `TestR_NUGET_017_TheVersionListIsTheUnionAscendingByPrecedence`, `TestR_NUGET_017_AVersionComesFromTheMemberTierResolutionWouldPick`, `TestR_NUGET_017_ARemoteMemberWithPagesByAddressIsMergedWhole` (Q-NUGET-82), `TestR_NUGET_017_ARegistrationEntryComesFromTheMemberTierResolutionPicks`, `TestR_NUGET_017_APartialMergeIsServedAndNeverKept`, the conformance kit's check 5, and the integration tests `TestR_NUGET_017_AVirtualRepositoryMergesTheDocumentsAndFilesOfItsMembers`, `TestR_NUGET_017_APushToAMemberIsVisibleThroughTheVirtualRepositoryAtOnce`.

### Q-NUGET-57 · Claims and pin rules keep a package away from local members in a search too

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-018 step 2 removes the results of *remote* members for the IDs that the pin rules and claims keep away. The version list, the registration and the download apply the same rules to local members (R-NUGET-017: a pin rule names the members that contribute, a claim leaves only the local members holding the most specific claim).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Remote members only | As written | Fewer calls | A search lists a local package that the version list of the same virtual repository answers 404 for; a client that picks it from the search fails on restore |
| (b) Every member (chosen) | `Resolver.KeepsAway(member, lower_id)` for each result of each member | Search, list, registration and download agree; the call reads the replica's in-memory index and runs no query | One in-memory lookup per result of the window |

**Choice:** (b). The totals are reduced by the removals, as for remote results.

**Rationale:** the requirement states the rule for the case that mattered when it was written (a remote member reaching an internal ID); the rule itself is the virtual repository's, not the member class's. The development agent revisits this if the lookup shows in a profile of a search at the largest window.

**Applied in the spec:** R-NUGET-018 step 2. Code: [search_virtual.go](../../../internal/formats/nuget/search_virtual.go) `runVirtualSearch`. Tests: `TestR_NUGET_018_AClaimOrAPinRuleKeepsAPackageAwayFromAMember`.

### Q-NUGET-58 · The window, the order and the total of a merged search

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-018 fixes the shape (each member asked for `min(skip + take, window)` results from 0, exact ID first, member order, deduplication by `lower_id`, an estimated total) and leaves the setting a proposal. Open: the bounds of the setting, what a page that crosses the window shows, what counts as an exact ID, and which total a page past the window answers.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `nuget.search.virtual_merge_window`, default 1000, 1 to 10000; the page ends at the window; an exact ID is a free-text word or an `id:` or `packageid:` term equal to the `lower_id`; a page at or beyond the window asks the members to count only (chosen) | A hard bound of work per request, independent of `skip`; the response never shows a position the merge did not fully order | `totalHits` stays meaningful beyond the window; the members' cost is the count alone | A client that pages past the window gets an empty page and must narrow the query |
| (b) The window grows with `skip` | Ask for `skip + take` | Deep paging works | Unbounded work per request, against `max_skip` of 3000 and `max_take` of 1000 |
| (c) A page that crosses the window shows what the members returned | The merge is longer than the window | Fewer empty pages | The order beyond the window is not the merge order |

**Choice:** (a). The total is the sum of the members' counts minus the removals and the duplicates seen in the window, and never below the number of packages the merge holds. The members are asked in parallel; the first error fails the request, because a local member's failure is the replica's.

**Rationale:** the window is the only protection against a deep skip multiplying the work by the number of members. The development agent revisits this if a deployment pages beyond 1000 results of a virtual repository in practice, or when the forwarded search of a remote member (R-NUGET-016) makes the cost of one member high enough to ask the remote for fewer results than the local members.

**Applied in the spec:** R-NUGET-018 steps 5 and 6 and its configuration block. Code: [nuget.go](../../../internal/config/nuget.go) `VirtualMergeWindow`, [search_virtual.go](../../../internal/formats/nuget/search_virtual.go). Tests: `TestR_NUGET_018_ThePackagesOfTheMembersAreMergedInTheOrderOfTheRequirement`, `TestR_NUGET_018_ThePageIsCutFromTheMergeAndTheMembersAreAskedForTheWindow`, `TestR_AUTHZ_016_TheReadScopeOfTheCallerAppliesToEachMemberSeparately`, and the integration test `TestR_NUGET_018_SearchMergesTheLocalMembers`.

### Q-NUGET-59 · A write to a virtual repository without a deployment target is refused with 403

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-019 says that without a deploy target a publish answers 405 `no_deploy_target`. The coarse check of the dispatcher decides the route's action on the addressed repository before the class is looked at, and the decision point (R-AUTHZ-009 rule 5) denies a write on a virtual repository that has no deployment target, for every caller including an administrator. The 405 of the dispatcher is therefore not reached; the npm format meets the same fact (R-NPM-029).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept the 403, change the requirement (chosen) | The write is refused with 403 `forbidden`, nothing is stored, the service index lists no publish resource | No change to the decision point for one format; the refusal is safe | The client sees 403 and not the clearer 405 |
| (b) Let the decision point admit the write so that the dispatcher answers 405 | A change in `internal/authz` | The message names the cause | A cross-format change of the decision point, outside the format; an admitted write on a repository that cannot take it |
| (c) Answer 405 from the format | The format checks the target before the dispatcher | Same message | The coarse check has refused already |

**Choice:** (a). The service index lists the publish resources exactly when `Resolver.DeployTarget` resolves one, so a client reads from the index that the repository takes no push.

**Rationale:** the refusal is the same fail-closed outcome with a less specific code. The development agent revisits this if the owner wants the 405 (the change is one case in the decision point and the dispatcher, for every format at once).

**Applied in the spec:** R-NUGET-019 (the second bullet and the second acceptance criterion). Code: [index.go](../../../internal/formats/nuget/index.go) `takesPushes`. Tests: `TestR_NUGET_019_ThePublishResourcesAreListedWhenThereIsADeploymentTarget`, and the integration test `TestR_NUGET_019_PublishingThroughAVirtualRepositoryGoesToItsDeploymentTarget`.

### Q-NUGET-60 · What the symbol indexing job does before the symbol server exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NUGET-11 decided that the symbol push enqueues one `nuget-symbol-index` job per symbol package, and R-NUGET-020 placed the enqueue "from R-NUGET-021 on". The work package that ships the symbol push also registers the job kind, so the pushes of the MVP already leave jobs behind. The extraction of PDBs, their keys and the checksum belong to the symbol server (R-NUGET-021, P1), which does not exist yet. The job needs a defined effect until then.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No enqueue until R-NUGET-021 ships | The push stores only; the P1 release backfills every package | No job rows that do nothing | The enqueue path, the kind, the queue and the dedup key ship untested with the P1 release; R-NUGET-021's acceptance test of a push that enqueues in the same transaction starts from nothing |
| (b) Enqueue now; the handler reads the state of the package, records it as its result and leaves `indexed_at` unset (chosen) | `gone` when the version or its symbol package was removed, `indexed` when `indexed_at` is set, `stored` otherwise; `files` is 0 | The queue, the kind, the dedup key and the transactional enqueue are exercised from the first push; every package stays pending, so the P1 release finds exactly the packages to index with `indexed_at IS NULL` and the partial index `nuget_symbol_package_pending` | A job row per symbol push that extracts nothing |
| (c) Enqueue now; the handler sets `indexed_at` | The package counts as indexed | The pending set stays empty | The P1 release would skip every package stored before it (it skips packages with `indexed_at` set), so it would need a second marker or a reindex of everything |
| (d) Build the extraction and the keys now | The full R-NUGET-021 job without the HTTP route | One step less later | The PDB readers (the `#Pdb` stream, the MSF container) and the ranged-read and spool code are P1 scope, unrelated to the push; building them here delays the push |

**Choice:** (b).

**Rationale:** it keeps the one property that matters for the later work, that `indexed_at IS NULL` means "not yet indexed", and it makes the transactional enqueue and the worker path a tested fact now. The handler is idempotent and reads one row. The development agent revisits this when R-NUGET-021 is built: the handler then gains the extraction and sets `indexed_at`, and the data migration enqueues the packages with `indexed_at` unset.

**Applied in the spec:** R-NUGET-020 (the job paragraph and the acceptance criteria). Code: [symbols.go](../../../internal/formatdata/nuget/symbols.go), [handlers_symbols.go](../../../internal/formats/nuget/handlers_symbols.go). Tests: `TestR_NUGET_020_TheIndexingJobRunsOnAWorkerAndRecordsWhatItFound`, `TestR_NUGET_020_ASymbolPushIsStoredLinkedAndQueuedForIndexing`.

### Q-NUGET-61 · How deep a symbol push looks, and what it writes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-020 requires every `.pdb` to be a Portable PDB or a Windows PDB "by its file signature" and says the push extracts nothing. A symbol package can hold files of tens of megabytes, and the push runs on a `web` replica (P-11). The item must also be removable with its version.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the first 32 bytes of each `.pdb`; link the item to the version; raise the package revision (chosen) | `BSJB` is a Portable PDB, the `Microsoft C/C++ MSF 7.00` banner a Windows PDB; the entry is held to the compression ratio of the archive rules | Constant memory per entry, whatever its size; the removal of the version takes the symbol package along with no code of the format; one rule for all generic writes | A `.pdb` with a valid signature and a corrupt body passes the push and ends in `index_error` at the job (R-NUGET-021), as its acceptance test expects |
| (b) Parse the PDB streams at the push | Validate the `#Pdb` stream or the MSF directory | Corrupt files are refused with 400 | Reads (and for a deflated entry inflates) the file in full inside the request; the parsers are P1 scope |
| (c) Store the item without linking it to the version | Only the `nuget_symbol_package` row ties them | One call less | The hard delete of a version removes the items of the version; an unlinked `.snupkg` would stay, orphaned, and a later symbol push for the number would meet a different file at its path |

**Choice:** (a). The extensions are compared case-insensitively on the decoded entry path, directory entries are skipped, and the first entry that breaks a rule is named in the 400. The dependencies of a symbol package are not read. The `.snupkg` is a plain item (not generated); its `nuget.id` and `nuget.version` properties are set like those of the other items of the version. The push raises `packages.rev_seq` because it changes the generic rows (the item link); that invalidates merged documents of virtual repositories earlier than needed, which is harmless.

**Rationale:** it meets the "extracts nothing" rule with memory bounded independently of the file size. The development agent revisits this if the P1 indexing shows corrupt packages are common enough to deserve an early refusal.

**Applied in the spec:** R-NUGET-020 (the symbol push paragraph and the acceptance criteria). Code: [symbols.go](../../../internal/formats/nuget/symbols/symbols.go), [head.go](../../../internal/formats/nuget/nupkg/head.go). Tests: `TestR_NUGET_020_ARefusedSymbolPackageIs400`, `TestR_NUGET_020_ASymbolPackageGoesWithItsVersion`, `TestR_NUGET_020_EveryPdbIsChecked`, `TestR_NUGET_020_HeadReadsTheStartOfAnEntryOnly`.

### Q-NUGET-62 · The version a NuGet rule is asked about: the normalized lower-case URL form

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a rule's range is compared by the format's adapter (`InRange`), which parses both sides, so `2.0` and `2.0.0.0` agree. Its `versions` include and exclude lists are patterns the decision point compares with the version string the format passes with a request. NuGet has several spellings of one version (`2.1`, `2.1.0.0`, `2.1.0+build`, `2.1.0-Beta.1`) and the format decides which one it asks about.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The normalized, lower-case URL form of R-NUGET-003 for every question (chosen) | Documents, files and search ask `lower_id` and `lower_version`; an administrator writes `2.1.0-beta.1` | One spelling for the route, the document and the decision, so a file is served exactly when a document lists it; no second normalizer in the hot path | A pattern in another spelling matches nothing, and an exclude of that kind exposes the version (H-380) |
| (b) The version as the client wrote it in the request | The path segment unchanged | Matches what a person types | Two requests for one version are decided differently; a document lists a version the file route refuses |
| (c) Ask under every spelling | The format asks for the original, the full and the URL form | A pattern in any of the three matches | Three decisions per version and row, and a fourth spelling (`2.1.0.0`) still escapes |

**Choice:** (a). The range is the supported way to name versions in any spelling; the manual says that the lists are compared letter by letter with the canonical form.

**Rationale:** the one spelling is what the registration page bounds, the flat container and the canonical paths use already (R-NUGET-003), so exposure cannot disagree with them. The development agent revisits this when the decision point can fold version patterns with a format rule (Q-EP-69 option (b)); (a) then keeps its meaning and the folding only widens what matches.

**Applied in the spec:** R-NUGET-034 (Version ranges). Code: [exposure.go](../../../internal/formats/nuget/exposure.go) `hiddenVersion`, [documents.go](../../../internal/formats/nuget/documents.go) `exposes`. Tests: `TestR_NUGET_034_RangeNotationOfRules`, `TestR_NUGET_034_VersionIncludesAndExcludesCompareTheCanonicalForm` (integration).

### Q-NUGET-63 · Where the exposure filter sits: on the rows before a document is rendered, and in the read scope for search

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-034 has the filter run "after the merge of a virtual repository and the rewrite of a remote document", and R-NUGET-009 pages over the exposed versions. npm filters the bytes of a merged document because its documents are one JSON object per package. A NuGet document is rendered by the format from rows, and the paging, the counts and the bounds depend on which rows are in it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A predicate over the version rows, applied before the document is rendered and paged; search by the read scope of the decision point; one version by `hiddenVersion` (chosen) | `viewOf` gives the handler its view, `exposes` the predicate over one ID, `Visible` applies it, `Resolver.ReadScope` carries the same rules for search | Paging, counts and bounds are computed once over the right list; no document is parsed back; a file and a document agree because both ask the view | Every handler that serves a version or a list must call it; a merge that yields bytes needs its rows back (H-295) |
| (b) Filter the rendered bytes | Render everything, remove leaves and rebuild bounds | One place for merged and local documents | Counts, bounds and inlining must be rebuilt by a second implementation of R-NUGET-009 |
| (c) A rendition per endpoint stored by the writer | Render on write | Fast reads | A rule change must re-render every document; the rule store is not the writer's |

**Choice:** (a). The ETag and the rendition key carry the endpoint and its exposure fingerprint, so a change of rule is a new document without any invalidation.

**Rationale:** the paging of R-NUGET-009 is a function of the list, so the filter belongs before it. The development agent revisits this if a remote or virtual class can only produce bytes; it then rebuilds rows from them before the predicate.

**Applied in the spec:** R-NUGET-034. Code: [handlers_flat.go](../../../internal/formats/nuget/handlers_flat.go), [handlers_registration.go](../../../internal/formats/nuget/handlers_registration.go), [handlers_search.go](../../../internal/formats/nuget/handlers_search.go) (unchanged: the scope holds the rules), [exposure.go](../../../internal/formats/nuget/exposure.go). Narrowed by Q-NUGET-75: the merged search of a virtual repository also filters every member's results with the view, because a remote member's results are decided by no read scope. Tests: `TestR_NUGET_034_AcceptanceDocumentsOfTheExampleRule`, `TestR_NUGET_034_PagingRunsOverTheExposedVersions`, `TestR_NUGET_034_SearchOnAnEndpointWithRules`, `TestR_NUGET_034_DocumentsOnAnEndpointWithRules` (integration).

### Q-NUGET-64 · Where the fixture packages of the e2e tests come from

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-NUGET-027 says the fixture packages are generated by `dotnet pack` from small projects in the test tree. The e2e tests need
many packages (an ID per scenario and run, so that a rerun never meets a stored version or a tombstone), and several builds of
one version with other bytes (the conflict and the replay).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Projects checked into the test tree, one per package | `test/e2e/nugetfixtures/<Id>/<Id>.csproj` | Literal reading of R-NUGET-027 | A directory per package ID, and an ID per run is not a checked-in name |
| (b) Archives built in Go (`archive/zip`) | The test writes a `.nupkg` itself | No SDK in the loop, fast | The package is not what a client produces; a field `dotnet pack` writes and the server mishandles is never seen |
| (c) One project template, rendered by the test and packed by the pod's SDK | `nugetPack(id, version, description)` writes `pack.csproj` and one class, runs `dotnet pack` in clients pod 0, reads the archive back; the bytes are kept for the run and written into any pod that pushes them | The package is the SDK's own; an ID, a version and a description (other bytes of one version) are arguments; a replay pushes the same bytes from every pod | One `dotnet pack` per distinct package (about 2 s each) |

**Choice:** (c). The template carries `NuGetAudit` off (the audit would ask each source for the vulnerability resource) and a
`<clear />` source, so the pack needs no network. The hand-built archives for the malformed cases of R-NUGET-005 stay where
they are, in the Go tests.

**Rationale:** the clients' reads and writes are the subject of the suite; a package built by the tool they come from is the
fixture that finds a mismatch (the first push of this suite found the trailing slash of Q-NUGET-66, which no test with a
hand-built request had sent). The development agent would revisit this if a scenario needs more than one class or several target frameworks
(then (a) for that package), or if the pack time dominates a run.

**Applied in the spec:** none (R-NUGET-027's bullet is met in the form of a template). Elsewhere:
[nuget_env_test.go](../../../test/e2e/nuget_env_test.go), [docs/developer/e2e.md](../../developer/e2e.md#the-nuget-tests).

### Q-NUGET-65 · How the push criteria of R-NUGET-012 get an anonymously readable feed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The criteria `--api-key "$TOKEN"` and `--api-key "ci:$TOKEN"` "without credentials" succeed only when the client can read the
service index without credentials, because a push reads the index first and the API key is examined on the write routes alone
(R-NUGET-012). The dev installation has `auth.anonymous.allowed: true` (the ceiling) and `anonymousAccess: false` (the
runtime switch, R-AUTH-040).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Switch `anonymousAccess` on in the test and back at its end | A policy grants `read` on `nuget-e2e-open` to `anonymous`; the test patches the setting and restores it in `t.Cleanup` | Tests the criterion as written, on the real switch; nothing in the installation changes beyond the test's duration | The switch is global while it is on: every repository with an anonymous grant is readable; the suite runs its tests one after the other and only this repository has such a grant |
| (b) Put the Basic credentials in `nuget.config` for these cases too | The key and the credentials are both present | No global state | Does not test "without credentials": the API key never decides, so a broken key rule is invisible |
| (c) Set `anonymousAccess` in the chart values of the dev profile | On for the whole cluster | No test-time switch | A different installation for every test of every suite |

**Choice:** (a), in `withAnonymousRead`, which also waits until all three web replicas answer the anonymous read. The revoked-key
case runs against this feed too, to show that a revoked key does not fall back to the anonymous principal that could read.

**Rationale:** the switch is documented to take effect on every replica within the revocation bound, and the helper waits for
exactly that. The development agent would revisit this if another suite runs in parallel with this one on the same cluster (then
(b) plus a feed of its own), or if the dev profile gets anonymous access for another reason (then the toggle goes).

**Applied in the spec:** none. Elsewhere: [nuget_env_test.go](../../../test/e2e/nuget_env_test.go).

### Q-NUGET-66 · The publish resources accept a trailing slash

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The first real push of the e2e suite (`dotnet nuget push` of the .NET 10 SDK) was answered 404: the service index names
`PackagePublish/2.0.0` as `<feed>/v3/package`, and the client sends `PUT <feed>/v3/package/`, with a slash it appends to the
address. The route table matched `v3/package` and `v3/symbolpackage` exactly, and `segments` refuses a trailing slash (only the
service index tolerates one), so every push of the SDK ended in `nuget_v2_unsupported`. The integration tests PUT to the exact
address and did not see it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The matcher of the two publish resources takes the path with or without one trailing slash | `matchPublish` in `match.go`; the route templates read `/v3/package[/]` and `/v3/symbolpackage[/]` | One place, the two routes that clients extend; the other routes keep the strict match that makes `v3//index.json` a refusal | Two spellings for two routes |
| (b) The service index names the address with a slash | `PackagePublish/2.0.0` = `<feed>/v3/package/` | The client then PUTs what it was told | A client may append a second slash, and nuget.exe and the SDK differ in what they do with a given address; the index is a published document with a byte-identical body (R-NUGET-001) |
| (c) The dispatcher strips a trailing slash for every format | A general rule of the request path | Every route is tolerant | Changes the match of the other formats and of `v3/flatcontainer/…/`, which today is a refusal; a contract change for one client habit |

**Choice:** (a). The relist and delete addresses `v3/package/{id}/{version}` are built by the clients from the same address
without a trailing slash problem (the delete and relist scenarios passed unchanged).

**Rationale:** the clients' address is fixed by the clients; the server decides which spelling it understands, and the narrowest
answer is the route that needs it. The development agent would revisit this if a second route turns out to be extended the
same way (then (c) with the dispatcher's clean path), or if the symbol push of nuget.exe sends yet another form (W2-SY owns that test).

**Applied in the spec:** R-NUGET-006 and R-NUGET-020 name the address `PUT <feed>/package`; both accept the trailing slash
(sentence added to R-NUGET-006). Code: [match.go](../../../internal/formats/nuget/match.go),
[routes.go](../../../internal/formats/nuget/routes.go). Tests: `TestR_NUGET_001_MatchResolvesEveryRoute` (the two cases with the
slash), and `TestNuGetPush` in the e2e suite, which failed with the 404 before the change.


### Q-NUGET-67 · A remote member of a virtual repository is asked for the stored upstream bytes under the variants of the merged documents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a virtual repository asks every member for the document it merges, with the variant of that document: `flat` for the version list and `registration` for the registration entries (Q-NUGET-56). A remote NuGet repository stores one variant of every metadata document, `doc`, the upstream's bytes (Q-NUGET-51), and its `PrepareFill` refused any other variant, so a virtual repository with a remote member answered 500 on its first version list. Neither package could see it alone: the remote class did not exist when the virtual one was written, and the virtual class did not exist when the remote one was.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The remote accepts `flat` and `registration` as stored variants of the same bytes (chosen) | `PrepareFill` and the upstream request treat the three variants alike; the engine keeps one entry per variant | One line; the engine's entries, validators and invalidation stay as they are | The upstream document is stored up to three times for a package that is read directly and through a virtual repository |
| (b) The virtual adapter asks remote members for `doc` | The variant of a member depends on its class | One stored copy | The virtual adapter learns the remote adapter's variant, and the merged-metadata cache keys on a variant per class |
| (c) One variant name for both | `doc` everywhere | One copy | The local member's document is not the upstream's bytes: the variants differ in content, not only in name |

**Choice:** (a). The same bytes under three names cost a few kilobytes per package and keep the two classes ignorant of each other.

**Rationale:** the documents are small and bounded by `MaxDocumentBytes`; the engine's validators make the three entries refresh independently, as they would for any two variants. The development agent revisits this when the engine can alias variants of one path.

**Applied in the spec:** none in a requirement. Code: `storesVariant` in [remote.go](../../../internal/formats/nuget/remote.go). Tests: `TestR_NUGET_015_...` fill cases in `remote_test.go` and `TestR_NUGET_017_AVirtualRepositoryMergesARemoteMemberThatGzipsAndPagesItsRegistration`.

### Q-NUGET-68 · The repositories of the remote and virtual e2e tests: a fresh pair per test, and a short circuit period where the test stops the upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the tests that read what the counting upstream recorded (one request per `.nupkg`, a cache that fills on the first request) need a cache that starts empty, and the test of a stopped upstream needs a circuit that closes again within the test. A remote repository opens its circuit after `failureThreshold` (5) failures for `circuitOpenSeconds` (300, R-REPO-029). The first run of the stopped-upstream test left the repository assumed offline for the five minutes after the upstream was back (`trove.remote_upstream_state.open_until` read 04:04:21 for a failure at 03:59:21), which is the specified behaviour and not a defect.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A remote and a virtual repository per test, named `nuget-<purpose>-<run>-remote` and `-all`, removed with `t.Cleanup`; the remote of the stopped-upstream test sets `availability.failureThreshold` 3 and `circuitOpenSeconds` 5 (chosen) | `upstreamRepos` in `nuget_upstream_test.go`; the virtual repository has `nuget-e2e` as first member and as deployment target | A cold cache for every test, whatever ran before; a circuit left open by one test touches nothing else; the shadowing check of a later first push (R-REPO-048) never meets a remote whose upstream was stopped | Four more repository writes per test, and each waits until three replicas read the new repository (at most 3 s, R-AUTHZ-028) |
| (b) One pair for the suite, the counters of the upstream reset between tests | A fixture like `nuget-e2e` | One creation | A package cached by an earlier test makes "one request per `.nupkg`" depend on test order; the stopped upstream would leave the circuit open for the next test |
| (c) The `nuget-all` of the dev profile | `up.sh` creates it | The name of the criterion | `up.sh` creates no NuGet repository (the wiring is a later step), and the cache is shared with whoever uses the cluster |

**Choice:** (a). The criterion's `nuget-all` is the role of the virtual repository, not its key.

**Rationale:** the cost of (a) is seconds per test and the benefit is that every assertion on the upstream's counters holds on a cold cache by construction. The development agent revisits this if the number of tests that need the pair makes the creation the larger part of the set's time.

**Applied in the spec:** none. Code: `upstreamRepos` in [nuget_upstream_test.go](../../../test/e2e/nuget_upstream_test.go). Tests: `TestNuGetRemote`, `TestNuGetVirtual`, `TestNuGetCollapse`, `TestNuGetClosing`.

### Q-NUGET-69 · How the edge drops the answer of a push: the Ingress rule of the npm test, with a path and a status

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.6 asks for a `dotnet nuget push` whose answer a fault-injecting edge drops, repeated by the client, exit 0. The npm test drops the answer of the first `PUT` below `/-rev/` through the `config-backend` annotation of trove's Ingress (a stick-table counter, `http-response silent-drop`). Two things differ for NuGet. The push is `PUT <feed>/v3/package` with or without a slash (Q-NUGET-66). And it first draws a 401: the client sends the push without credentials and repeats it with the `nuget.config` credentials, so the first `PUT` that matches is not the one worth losing; the first run of this test dropped the 401, the client repeated it, and the push went through without any replay.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The npm rule with the path as a parameter and a status condition: `path_end /v3/package /v3/package/`, and the drop only for an answer with status 201 (chosen) | `dropOneAnswerOf(t, pathACL, status)`; `dropOneAnswer` of npm is the call with the `/-rev/` path and no status | One mechanism for both formats; the drop is after the replica has committed, which is the case the criterion names | The edge has two HAProxy pods and each has its own stick table, so the push loses two answers (the third attempt is the last the client makes) |
| (b) A fault proxy in the clients pod (the forwarder `fwd` gets a drop switch) | The forwarder closes the connection after the answer's headers | Independent of the Ingress controller | A second way to produce the fault; the npm test would not use it; the forwarder is not on the path of a real client |
| (c) A hook in the server that commits and then closes the connection | A test-only branch | Exactly one drop | Test code in the release binary |

**Choice:** (a). The evidence that the client repeated the push is `trove_nuget_push_replayed_total` (R-NUGET-028), summed over the web pods, which rises by at least one: the repeat of a committed push is the replay of Q-NUGET-6. The test requires "at least one" because of the two stick tables.

**Rationale:** the criterion is about what the client does when it loses an answer, and the edge is where an answer is lost in production. The development agent revisits this if the Ingress controller is replaced or runs a single pod, where the count becomes exact.

**Applied in the spec:** none. Code: `dropOneAnswerOf` in [npm_replicas_test.go](../../../test/e2e/npm_replicas_test.go). Test: `TestNuGetPushRetry`.

### Q-NUGET-70 · Which of the remaining E3 cases run on every change and which in the closing run

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the set `nuget` runs on every change and has to stay within about 20 minutes on the CI runner (Q-NUGET-34); `nuget-closing` is the additions of the dispatch that closes the phase. On the dev cluster (linux/arm64, 2026-11-02) the set with the remote, virtual, E3.2, dropped-answer and customer-endpoint tests ran 234 s of test time (78 tests and subtests), of which E3.2 took 11 s for 120 packages on three clients at once and the dropped answer 33 s (the client waits for the answer it lost before it repeats the push).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Everything that needs no stopped Valkey runs on every change; the closing run holds what stops Valkey (chosen) | `nuget`: `TestNuGetRemote`, `TestNuGetVirtual`, `TestNuGetCollapse` (E3.2), `TestNuGetPushRetry`, `TestNuGetEndpoint` (E3.7), `TestNuGetReplicas`; `nuget-closing`: `TestNuGetClosing` (documents with Valkey stopped, 51 pushes with symbol packages each followed by a restore with Valkey running and again stopped, the cold restore of the reference solution with Valkey stopped, concurrent cold restores on two replicas with Valkey stopped) | R-NUGET-030 and E3.2, E3.6 (dropped answer), E3.7 are checked on every change that touches the plug-in, as R-NUGET-027 asks for the `scatter` row; each stop of Valkey costs half a minute or more (the health check needs two intervals to see it go); the closing set took 322 s here (the 51-iteration loops 76 s with Valkey running and 104 s stopped) | The Valkey-stopped criteria R-NUGET-031 are checked only at the closing run, as R-NUGET-027 says |
| (b) E3.2 in the closing run | The cold restore is the heaviest case | A shorter set | It takes 11 s here; the one case that proves the collapse across replicas would not run per change |
| (c) Everything on every change | The closing tests join `nuget` | One set | About 5 more minutes per change and a Valkey outage in every run, for criteria that R-NUGET-027 assigns to the closing run |

**Choice:** (a).

**Rationale:** the split follows the rows of R-NUGET-027: the `scatter` row on every change, the Valkey-stopped row at the end of the phase. The development agent revisits this when the CI runner's measured time for `nuget` leaves the 20 minutes (the numbers above are from a laptop).

**Applied in the spec:** none. Code: [nuget_closing_test.go](../../../test/e2e/nuget_closing_test.go); `deploy/dev/e2e.sh` needs no change (`nuget` skips `TestNuGet(Closing|HA)`, `nuget-closing` runs `^TestNuGetClosing`). Tests: as listed.

### Q-NUGET-71 · The customer endpoint test: a package ID of the run, an exact range for the hidden version, and what the clients can and cannot show

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.7 names `Acme.Sdk` in `nuget-customers`, a hidden version that `dotnet add package` fails on with `NU1102`, and a push that answers 403. Three things in the suite make that literal reading wrong. A stored version cannot be pushed again with other bytes, and `dotnet pack` does not produce the same bytes twice, so a fixed ID breaks the second run on a cluster. `dotnet add package --version 1.9.0` is the range `>= 1.9.0`, which NuGet resolves to the lowest version that satisfies it: with 2.0.0 exposed that is 2.0.0 and the command succeeds, so it does not show a hidden version. And with no publish resource in the service index, `dotnet nuget push` stops in the client with "the source does not support push" and never sends the request that answers 403.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The package is `E2e.Nuget<run>.Sdk`; the hidden version is asked for as the exact range `[1.9.0]`; the 403 is a raw `PUT <feed>/v3/package` with the bound token as `X-NuGet-ApiKey`, and `dotnet nuget push` is run as well and must fail; nuget.exe restores a `packages.config` (chosen) | The rule names the ID of the run; `NU1102` is compared with the failure for `[9.9.9]`, a version that exists nowhere | Every run starts from nothing; the failure shown is the one of R-EP-013 (hidden is missing); the 403 and its code `endpoint_read_only` are asserted | The ID is not the literal `Acme.Sdk` |
| (b) The literal `Acme.Sdk`, the repository emptied before a run | Delete the versions first | The names of the criterion | A delete leaves a tombstone (R-NUGET-006), so the second run cannot push the version again |
| (c) `dotnet add package` without a range, expecting `NU1102` | The notation of the criterion | None | It succeeds and installs 2.0.0 |

**Choice:** (a). nuget.exe 7.9 restores only a file named `packages.config` (or `packages.*.config`); the test writes one per directory (`exposed/`, `hidden/`) with `targetFramework="net48"`.

**Rationale:** the criterion's intent is that a hidden version behaves like a missing one for every client and that the endpoint offers no write; (a) shows both with the real clients and says where a client cannot reach the server. The development agent revisits this if a NuGet client sends a push without a publish resource.

**Applied in the spec:** none. Code: [nuget_endpoint_test.go](../../../test/e2e/nuget_endpoint_test.go). Test: `TestNuGetEndpoint`.

### Q-NUGET-72 · The Windows row of E3.1 and the Visual Studio checklist run in the owner walkthrough, not on a CI runner

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.1 asks for nuget.exe and the .NET 10 SDK on a Windows runner and for the manual Visual Studio 2022 checklist once against the M3 build. Every job of the project runs on the owner's self-hosted Linux runners (ARC); the e2e suite needs trove deployed in kind with its PostgreSQL, Valkey and S3, which a Windows runner cannot host. nuget.exe's protocol already runs on every change on Mono in the clients image (Q-NUGET-33), and the .NET 10 SDK on Linux runs the same NuGet client libraries the Windows SDK runs.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The Windows row and the Visual Studio checklist are run by hand against the beta candidate in the owner walkthrough of phase 8; the per-change suite keeps nuget.exe on Mono and the SDK on Linux (chosen) | Phase 8's walkthrough item "npm and .NET clients against local, remote and virtual repositories" takes both | Nothing outside the repository changes; the Windows-only behaviour (Windows credential providers, Visual Studio's browse tab paging) is looked at by a person on the real client | The Windows row is not automated; a regression in it is seen only at the walkthrough |
| (b) A GitHub-hosted `windows-latest` job against a trove reachable from it | The repository is public, so the minutes cost nothing | Automated | It needs a trove instance reachable from the internet with a public certificate, or trove with PostgreSQL, Valkey and S3 on Windows; both are infrastructure outside the repository, and the owner's standard is self-hosted runners |
| (c) A self-hosted Windows runner in the owner's cluster | Like the Linux runners | Automated, inside the owner's network | A machine change outside the repository that only the owner can make |

**Choice:** (a). The roadmap's E3.1 keeps both rows; their evidence is the walkthrough record.

**Rationale:** the protocol the Windows clients speak is covered on Linux on every change; what only Windows shows needs the real client in front of a person, which the walkthrough already plans. The development agent revisits this when the owner provides a Windows runner or approves a GitHub-hosted one against a reachable test instance.

**Applied in the spec:** none. Plan: [BETA.md](../../projektplan/BETA.md) phases 4 and 8; [roadmap](../../coverage/roadmap.md) E3.1.

### Q-NUGET-73 · The NuGet part of the drain test moves to phase 7 with the other long legs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.6 asks that variant A of the drain test gains the NuGet part of its load and that R-ARCH-024's drain criterion, whose load needs both formats, passes. On 2026-11-01 the owner moved the drain, failover and soak legs out of the per-change pipeline into `HA and Performance (nightly)`, and npm's drain reliability with them into phase 7 ([ha-and-helm.md](../ha-and-helm.md) Q-HA-67): the scenario needs about twelve cores and the CI runners are held to fewer. The nightly leg `nuget-ha` exists and runs its set (Q-ARCH-276, Q-ARCH-277); the set names no test yet.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The NuGet drain load (`TestNuGetHA`) and R-ARCH-024's drain criterion are built in phase 7 with npm's drain reliability (chosen) | Phase 7 "Node drain under npm and NuGet load with zero failed requests" takes it | One place for the drain work and its runner capacity; the per-change suite keeps the functional NuGet criteria of E3.6 (scatter, Valkey stopped, dropped answers, equal bodies and ETags) | E3.6 is not complete at the end of phase 4 |
| (b) Build `TestNuGetHA` now and let the nightly leg run it | Same scenario as npm's | Closer to E3.6's letter | It would run on the runners on which npm's same scenario is red for capacity, so it adds a red nightly leg without new information |

**Choice:** (a).

**Rationale:** the owner's rule of 2026-11-01 says long legs run nightly and their reliability is phase 7's; building the NuGet load where its capacity question is answered avoids a second leg that is red for the same reason. The development agent revisits this when phase 7 starts.

**Applied in the spec:** none. Plan: [BETA.md](../../projektplan/BETA.md) phases 4 and 7; [roadmap](../../coverage/roadmap.md) E3.6.

### Q-NUGET-74 · A search term meets only the versions the caller may read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-010 matches a package by the row of its latest visible version (Q-NUGET-46) and a `version:` term by any listed visible version. The scan of Q-NUGET-45 applied the caller's read only to the versions the answer shows, after the statement had matched. For a caller who may read only some versions (an endpoint with exposure rules, a grant or a token scope narrowed by package), a term therefore met versions that caller cannot read: on the E3.7 rule, `q=packageid:acme.sdk version:3.0.0-rc.1` answered `totalHits: 1` exactly when the hidden 3.0.0-rc.1 existed, a word of a hidden latest version's description found the package, and a word of the latest exposed version did not (the code review and the security audit of 2026-11-02, finding F-21). SQL cannot ask the decision point which versions are readable.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The scan tests the terms per version, the format applies them to what the caller may read (chosen) | A scan statement (`NuGetSearchScan`) returns every listed visible version of a candidate with a flag: the version satisfies every term but `version:`. A candidate has such a version, and each of its `version:` terms names one of its listed visible versions. The format keeps a package when the latest version the caller may read carries the flag and every `version:` term names a version the caller may read | Exact for every caller; the decision point stays the only judge of a row; a caller who reads the repository whole keeps the SQL count and page | The scan tests the terms on every visible version instead of the latest one, so its cost grows with the versions per package (H-335) |
| (b) Hand the readable (ID, version) pairs to SQL | A first pass lists every visible version, the decision point keeps the readable ones, a second statement matches over them | One predicate in SQL | The first pass reads every visible version of the repository whatever the terms, and the list crosses the port on each request |
| (c) Test the terms again in Go on the latest readable version | Fetch that version's metadata and match in Go | No new statement | A second matcher (prefix words, phrases, ID tokens, folding) that must agree with PostgreSQL's forever; the count needs the metadata of every candidate |
| (d) Record the leak as a gap | Keep the code | Nothing to build | Breaks R-NUGET-034 ("only exposed IDs and versions") and E3.7 on every customer endpoint |

**Choice:** (a). The flag covers every term but `version:`, whose test is an equality on the URL form that the format applies to the readable list. A version the store gave no flag matches nothing, so a store that does not answer the question lists nothing rather than everything. The rank of the scan is the rank of `NuGetSearchPage`, the tier of the latest visible version, so a caller who reads row by row sees the packages in the order of one who reads whole; for a term of several words that meets ID tokens, the tier can follow the ID casing of a newer hidden version ([H-345](../../security/endpoints-and-exposure.md#h-345--the-rank-of-a-nuget-search-result-can-follow-the-id-casing-of-a-hidden-version)).

**Rationale:** the terms must be applied where the caller's reads are, and only the decision point knows those. Flags per version keep the matcher in one place, PostgreSQL, and the decision in another, the decision point. The development agent revisits this when profiling shows that the per-version scan dominates a replica (the scan would then become option (b) with keyset paging), or when autocomplete (R-NUGET-011) is built and needs the same scan.

**Revisited** after the code review of the fix wave: the scan was read in batches of 500, each its own statement and transaction that ranked the whole repository again and paged by offset, so a request cost the batches times the versions and its batches saw different snapshots. The data port now reads `NuGetSearchScan` as one statement in one transaction and hands its rows over a batch at a time as they arrive (`Store.SearchScan`, the format's `Scanner`); a retried transaction starts the count over and gives the scan budget back what the abandoned attempt spent.

**Applied in the spec:** R-NUGET-010 (the field table, the matching paragraph, the implementation bullet), R-NUGET-034 (the search bullet and an acceptance criterion). Code: [nuget_search.sql](../../../internal/meta/postgres/queries/nuget_search.sql) `NuGetSearchScan`, [search.go](../../../internal/formatdata/nuget/search.go) `searchScan`, [read.go](../../../internal/formats/nuget/rows/read.go) `SearchQuery.PerVersion`, `SearchHit.ListedMatch`, [handlers_search.go](../../../internal/formats/nuget/handlers_search.go) `searchRows`, the twin in [nugettest](../../../internal/formats/nuget/nugettest/store.go). Tests: `TestR_NUGET_034_SearchTermsMeetOnlyTheVersionsTheCallerMayRead`, `TestR_NUGET_034_TheScanDecidesOnTheFlagsOfTheReadableVersions`, and the integration test `TestR_NUGET_034_SearchTermsMeetOnlyExposedVersions`.

### Q-NUGET-75 · The merged search decides on the virtual repository the client addressed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the merged search of R-NUGET-018 asked `Resolver.ReadScope` for the rows of its local members, and the decision point built each row's coordinate without the addressed repository. The exposure ceiling therefore looked up the members' rules, while Q-EP-11 decides exposure on the repository the client addressed, and the scope lift of Q-AUTHZ-163 never applied. With the rule on the virtual `nuget-customers` of the worked example ([endpoints.md](../endpoints.md)), the search listed nothing while the documents listed the exposed versions; a token scoped to the virtual key found nothing; and an operator who also exposed the members would have had the search follow the members' rules while the documents follow the virtual repository's (F-21). The results of a remote member (R-NUGET-016) are decided by no read scope at all.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The read scope decides through `rq.Addressed()`, and the merge filters every member's results with the view of the addressed repository (chosen) | `authz.ReadScoper.ReadScopeOn` takes the addressed repository and sets `Coordinate.Addressed` (Q-AUTHZ-168); `Resolver.ReadScope` passes `rq.Addressed()`; `runVirtualSearch` drops the versions outside `viewOf(rq)` and a result whose latest version is outside it | One rule for documents and search; the lift applies; the frozen signature of `Resolver.ReadScope` stays; a remote member's results meet the same view | For a local member the view filter repeats what the scope decided |
| (b) A new `Resolver` method for the merged search | `ReadScopeThrough(rq, virtual, keys)` | Explicit | Changes the frozen contract (an ADR); the addressed repository is already a fact of the request |
| (c) Filter by the view alone and keep the members' read scopes | Narrow the members' rows with the virtual repository's view | No change in the decision point | The lift still never applies, and the members' rules still remove rows the virtual repository exposes |

**Choice:** (a). A result whose latest version is outside the view is dropped whole and counted as removed (step 6 of R-NUGET-018), because its package-level fields (title, description, `version`) are that version's and the merge holds no other version's metadata; the forwarded search therefore hands over results whose latest version is the latest exposed one where it can.

**Rationale:** exposure and the lift are properties of what the client named (Q-EP-11, Q-AUTHZ-163), and `rq.Addressed()` already carries it to every other decision of the request. The development agent revisits this if a format needs a listing through a repository other than the one the request addressed.

**Applied in the spec:** R-NUGET-018 (step 1 and an acceptance criterion), R-NUGET-034 (the search bullet), [authorization.md](../authorization.md) R-AUTHZ-009 rule 4. Code: [visible.go](../../../internal/authz/visible.go), [virtual.go](../../../internal/formathost/virtual.go) `ReadScope`, [search_virtual.go](../../../internal/formats/nuget/search_virtual.go) `exposedHit`. Security: [nuget-virtual.md](../../security/nuget-virtual.md). Tests: `TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryAppliesItsExposure`, `TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryLiftsATokenScopeThatNamesIt`, `TestR_API_029_BridgeReadScope`, `TestR_NUGET_034_TheMergedSearchFiltersEveryMemberByTheViewOfTheAddressedRepository`, and the integration test `TestR_NUGET_034_SearchThroughAVirtualRepositoryOnAnEndpointWithRules`.

### Q-NUGET-76 · The fan-out of the merged search: four members at a time and one scan budget

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the merged search started one goroutine per member without a bound, and each local member's scan had its own budget of 200,000 packages (Q-NUGET-45). With `max_virtual_members` at its default of 100, one request could hold a database connection per member and pay up to 100 times the budget in permission checks (H-335, the security audit of 2026-11-02).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) At most four members at once, and one budget of 200,000 packages for the scans of a request (chosen) | A semaphore around the member searches; every batch of every scan is taken from one budget | A request holds at most four of the pool's default twenty connections and looks at no more packages than one repository may; the usual one to three members still run in parallel | A virtual repository of several large members answers 400 where each member alone would not; members beyond four wait for a slot |
| (b) A setting `nuget.search.virtual_concurrency` | Configurable bound | Tunable | A value nobody can choose without measurements; more configuration surface |
| (c) One member after the other | No parallelism | Least load | The latency of every member adds up, and a slow upstream holds the local members back |
| (d) Bound the concurrency, keep a budget per member | | Fewer refusals | Up to a hundred times the budget per request remains |

**Choice:** (a). The bound covers remote members as well, because each is an upstream query (R-NUGET-016). A request whose context ends while it waits for a slot asks no further member and answers with that error, never with a partial merge.

**Rationale:** four keeps the common virtual repository fully parallel and bounds the worst case by a constant; the budget is the one the package listings use for the same decision (R-API-029). The development agent revisits this when a deployment reports 400 on a virtual repository whose members are each within the bound, or when profiling shows requests waiting for member slots (then option (b)).

**Applied in the spec:** R-NUGET-018 step 1, R-NUGET-010 (the implementation bullet). Code: [search_virtual.go](../../../internal/formats/nuget/search_virtual.go) `maxMemberSearches`, [handlers_search.go](../../../internal/formats/nuget/handlers_search.go) `scanBudget`. Security: [permissions.md](../../security/permissions.md) H-335. Tests: `TestR_NUGET_018_AVirtualSearchAsksAFewMembersAtATime`, `TestR_NUGET_018_TheMembersOfAVirtualSearchShareOneScanBudget`, `TestR_NUGET_018_ACanceledSearchStopsAskingMembers`.

### Q-NUGET-77 · The search applies the include and exclude patterns: the repository's, and through a virtual repository its own and each member's

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the include and exclude patterns of R-REPO-005 are routing rules; `Authorize` applies them to every document and file, and the package list applies them too. The search applied none (the security audit of 2026-11-02), so `excludePatterns: ["Secret.*"]` left `Secret.Thing` listed with its description and versions while its documents answered 404. A search through a virtual repository has no name, so `Resolver.Plan`, which drops a member whose patterns refuse a name, applied no pattern either.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The patterns decide each ID in the scan; a caller who reads a repository with patterns whole is scanned too; through a virtual repository a member contributes the IDs its patterns and the virtual repository's admit (chosen) | The pattern test joins the read decision of the scan (`patternsOf`, `searchRepo`); the members' patterns come from the virtual repository's member list; a remote member's results are filtered after it answered | `totalHits` stays exact; the test is the one `Authorize` makes (`Repository.Admits` on the lower-case ID); no second pattern grammar | A caller who reads a repository with patterns whole pays the scan, and above 200,000 matching packages gets 400 instead of an answer |
| (b) Filter the page only | Count with SQL, drop excluded results from the page | Cheap | `totalHits` counts withdrawn packages, and pages come out short |
| (c) Compile the patterns into the statement | Globs as `LIKE` or regular expressions | Exact and paged by SQL | A second implementation of the pattern grammar and its folding that must agree with R-REPO-005 forever, which Q-NUGET-45 rejected for grants |

**Choice:** (a). The patterns of a member are read from the virtual repository's member list (`rq.Repo.Members()`), from which the plan was made; a planned member that is not on it is a fault of the plan and answers 500. No store is read through that list (H-329).

**Rationale:** a pattern withdraws a package from what a repository serves, and search is part of what it serves. The development agent revisits this when a repository with patterns holds so many packages that common queries reach the scan's bound; the patterns would then move into the statement as a compiled set, as R-API-029 plans for grants.

**Applied in the spec:** R-NUGET-010 (the matching paragraph, the implementation bullet and an acceptance criterion), R-NUGET-018 step 1. Code: [handlers_search.go](../../../internal/formats/nuget/handlers_search.go) `patternsOf`, `searchRepo`; [search_virtual.go](../../../internal/formats/nuget/search_virtual.go) `memberPatterns`. Security: [repository-model.md](../../security/repository-model.md), [nuget-virtual.md](../../security/nuget-virtual.md). Tests: the integration tests `TestR_REPO_005_TheNuGetSearchOfALocalRepositoryAppliesItsPatterns` and `TestR_REPO_005_TheNuGetSearchOfAVirtualRepositoryAppliesItsAndItsMembersPatterns`.

### Q-NUGET-78 · A forwarded result is converted into the shape of a local result, filtered per version, and keeps the members R-NUGET-016 names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-016 rewrites the addresses of a forwarded result as R-NUGET-015 does and keeps `owners`, `verified`, `deprecation`, `vulnerabilities`, the download counts and `packageTypes`. R-NUGET-034 shows a caller on an endpoint with rules only the exposed versions, with `version` the latest of them and `totalDownloads` their sum. R-NUGET-018 merges a remote member's results with the local ones by `lower_id`, the package-level fields coming from the latest version of the union. Open were how the upstream's answer travels through the filter and the merge, which of its members pass, and what a result shows whose latest version the caller may not read.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each result becomes the search row of a local package: the versions in URL form with their download counts, the result's metadata as the latest version's metadata, and the members a local package has no column for (`rows.Forwarded`) on the latest version; one renderer; a member that R-NUGET-010's result table and R-NUGET-016 do not name is not passed on; a hidden latest version takes its metadata with it (chosen) | `search_remote.go` converts, `search.Render` renders local, cached and forwarded results alike | One renderer and one merge for every result; the forwarded members move with the latest version in a union, as step 4 of R-NUGET-018 has the package-level fields move; nothing an upstream adds unannounced reaches a client; a caller never sees what a hidden version says | A member an upstream adds later is dropped until it is named; a result whose latest version is hidden shows no description |
| (b) Pass the upstream's JSON through and edit the addresses and the version list in place | A filter over a JSON tree | Every member the upstream sends reaches the client | A second renderer for the merge; members trove does not know pass unchecked, the opposite of the rule for the registration (Q-NUGET-52); the description of a hidden latest version stays |
| (c) Ask the upstream's registration of each result for the metadata of the latest readable version | One more document per result | A full result for a caller with a version rule | One more upstream request per result and page; a search fills the cache with registrations |

**Choice:** (a). A result whose ID is no NuGet ID, or that lists no version that parses, is removed like a result a pattern excludes, so `totalHits` is reduced for it too. The versions are deduplicated by their URL form and sorted ascending; `version` is the highest of them. The metadata (title, summary, description, authors, tags, the three web links, the package types) is the result's only when the upstream's own `version` is that highest version, and otherwise left out, as are `deprecation` and `vulnerabilities`, which describe the upstream's latest version. `owners` is a list (a single string is one owner). `totalDownloads` is the upstream's, or, once a version was removed for the caller, the sum of the versions shown. `deprecation` and `vulnerabilities` pass as received; the address in a vulnerability's `advisoryUrl` is a link for people like `projectUrl`.

**Rationale:** the search, the cache-only search and the merge already speak one row shape, and carrying the forwarded members on the latest version is exactly where R-NUGET-018 wants the package-level fields to come from; a second renderer for the upstream's JSON would have to repeat every rule of R-NUGET-010 and R-NUGET-034 a second time. Dropping the metadata of a hidden latest version is the fail-closed reading of R-NUGET-034 ("only exposed IDs and versions"); fetching the readable version's metadata (c) would turn a search into a registration fill. The development agent revisits this when a client is seen to rely on a member that is dropped, or when the decision on upstream addresses in `deprecation` and `vulnerabilities` (Q-NUGET-85, which the search follows) changes what a remote's registration passes on.

**Revisited** after the security audit of the fix wave: the upstream matches a query against every version, so a result kept for a caller who reads row by row could still answer whether a hidden version exists or what it says. Such a result is removed, with `totalHits` reduced, when a `version:` term names no readable version, and when the upstream's latest version is hidden and the query has another term (`matchedOnReadable`); a query without terms keeps it as above.

**Applied in the spec:** R-NUGET-016 (the first bullet). Code: [search_remote.go](../../../internal/formats/nuget/search_remote.go) `hit`, `readableOnly`, `matchedOnReadable`, [forwarded.go](../../../internal/formats/nuget/rows/forwarded.go), [document.go](../../../internal/formats/nuget/search/document.go) `Render`. Tests: `TestR_NUGET_016_AForwardedSearchIsRewrittenIntoTheRemoteAndKeepsTheUpstreamMembers`, `TestR_NUGET_034_AForwardedResultShowsOnlyTheVersionsTheCallerMayRead`, `TestR_NUGET_034_AForwardedResultMatchedOnAHiddenVersionIsRemoved`, `TestR_NUGET_016_ARemoteForwardsItsSearchAndFallsBackToItsCache` (`test/integration/nuget`).

### Q-NUGET-79 · When a remote forwards: the engine's populate check decides a contact, the cache view and an endpoint that fills no caches search the cache

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-016 said "Forwarding needs `read` on the remote. It stores nothing in the repository, so the cache fill rule does not apply." The engine's pass-through, decided later ([architecture.md](../architecture.md) Q-ARCH-270), contacts the upstream for a query without a cached answer only when the populate check of R-AUTHZ-008 allows it, and serves a cached answer to any caller. R-NUGET-034 has a remote answer with its cache-only search on an endpoint with `cache_fill: false`, and the cache view (R-REPO-032) never contacts the upstream. The populate check is asked for the coordinate the format's `Classify` gives the query's path, and `Classify("v3/search")` named the package `v3`, because `v3` is a valid ID. Open were which rule the format follows, and for which coordinate the check is asked.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine's rule decides a contact; the format answers with the cache-only search, without asking the engine, on the cache view, on an endpoint that fills no caches, with `forwardSearch: false` and for a withheld query; the query's path `v3/search` names no package (chosen) | The two cases R-NUGET-034 and R-REPO-032 name are visible in the request (`rq.CacheView`, `rq.Subject`); `Classify` answers `v3/search` and `v3/autocomplete` as a volatile path without a package | One rule for what may leave the replica (Q-ARCH-270); no audit event and no new contract member; a caller whose grants name packages forwards as its repository-level read of the route allows | A caller who may not fill the remote is served answers that other callers caused, and may see a package it cannot download because it is not cached |
| (b) `read` alone, as the old sentence says | A contact that bypasses the populate check | Forwarding for every reader | A second road for what may leave the replica; a read-only endpoint's search would reach the upstream, which R-EP-019 forbids |
| (c) Cache-only for every caller who may not fill, also when an answer is cached | The format needs the caller's fill right for a remote addressed directly | Consistent with the merge's rule that a caller who may not fill sees only cached versions (R-AUTHZ-008) | The contract gives that right only for the members of a plan (`PlannedMember.MayFill`); a remote addressed directly would need a new member of the frozen contract and an ADR |

**Choice:** (a), and R-NUGET-016's sentence is corrected. An upstream failure, an open circuit, an upstream that announces no search resource and an answer that is no search document fall back to the cache-only search with 200 and are counted in `trove_nuget_search_forward_failures_total`; `ErrOffline` (no contact allowed and no cached answer) falls back without being counted; an internal error is answered as one.

**Rationale:** the pass-through is the same act as a fetch minus the store, and Q-ARCH-270 gave it the fetch's rules for a contact; repeating a weaker rule in the format would let a search leave where a fetch may not. The cached answer a caller who may not fill receives is the upstream's public answer, filtered for that caller like every other use; a listed package that is not cached is refused at download as any uncached package is for that caller. The path names no package because a query names none, as R-NUGET-015 already says of search ("repository-level"); `v3/search` can never be a stored path of a package `v3`, so nothing is lost. The development agent revisits this when the owner wants option (c), or when the contract gains the fill right of a request for formats anyway.

**Applied in the spec:** R-NUGET-016 (the bullets on forwarding and on failures). Code: [search_remote.go](../../../internal/formats/nuget/search_remote.go) `mayForward`, `forwardSearch`, [paths.go](../../../internal/formats/nuget/paths.go) `isQueryPath`. Tests: `TestR_NUGET_016_TheRemoteSearchesItsCacheWhereItMayNotForward`, `TestR_NUGET_016_AnUpstreamThatCannotBeAskedOrFailsLeavesTheCacheSearch`, `TestR_NUGET_016_TheSearchPathOfAQueryNamesNoPackage`.

### Q-NUGET-80 · The withheld-query rule also asks about the runs of ID characters inside a word

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-016 asks `Resolver.KeepsAway` about each term of a query, after the field prefix is dropped and the term is folded like an ID, and a phrase word by word; a term that is no name of the format keeps nothing away (Q-NUGET-17). A word with a character an ID cannot hold (`acme!`, `acme*`, `"acme,"`, `unknown:acme`) is no name, so it was never protected, while an upstream's tokenizer reads `acme` from it and nuget.org learns the claimed name.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ask about the word and about every maximal run of letters, digits, `.`, `-` and `_` in it, with and without separators at its ends (chosen) | `withheldNames` in the format; `KeepsAway` unchanged | Punctuation an upstream ignores no longer carries a protected name past the rule; the core's rule stays one function | A few more in-memory lookups per query; a query such as `acme!` is withheld where the spec's letter would have forwarded it |
| (b) Ask about the term as written | The letter of R-NUGET-016 | Nothing to build | A trailing punctuation mark sends a claimed name to the upstream |
| (c) Also ask about every dot-separated part of a word | `contoso.acme` asks about `acme` | Closer to how some upstreams tokenize IDs | `contoso.acme` names no package below `Acme.*`; more queries withheld for names the claims do not cover |

**Choice:** (a).

**Rationale:** the rule exists so that a query for a protected namespace does not leave the network; a rule that a stray `!` defeats protects only careful users. Splitting further at dots (c) would withhold queries for other organisations' packages that merely contain the word. The development agent revisits this if users report public results missing for queries with punctuation, or when an upstream is seen to tokenize at characters that are part of IDs.

**Applied in the spec:** R-NUGET-016 (withheld queries; the upstream is sent the parsed terms the rule checked, and a query with a `version:` term that is no version is not forwarded, because Parse folds such a text to nothing the rule could ask about, found by the code review of the fix wave). Code: [search_remote.go](../../../internal/formats/nuget/search_remote.go) `withheldNames`, `forwardedQ`, `namesNoVersion`. Tests: `TestR_NUGET_016_TheWithheldRuleAsksAboutEveryNameAQueryCarries`, `TestR_NUGET_016_AWithheldQueryIsAnsweredFromTheCacheAndCounted`, `TestR_NUGET_016_TheUpstreamIsSentOnlyTheTermsTheWithheldRuleChecked`.

### Q-NUGET-81 · A remote member of a virtual repository is searched by the code that searches a remote addressed directly

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-018 step 6 said that a remote member contributes nothing until the forwarded search exists, and named the seam `Format.searchRemote`, which returns the member's hits and count like a local member's search. Step 1 wants the withheld-query rule evaluated with the virtual repository's claims and pin rules, step 2 removes what they keep away from the member, and the member's own include and exclude patterns decide what it serves.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `searchRemote` runs the search of the remote addressed directly on a request derived with `rq.Member`, with the virtual repository's request for the withheld rule and the read scope; it applies the member's patterns to the results and leaves step 2 to the merge; its count is the upstream's less its own removals (chosen) | One `remoteSearch` for both roads | The member's client, cache and populate check apply and the addressed repository stays the virtual one (Q-AUTHZ-165); a claim is asked about once per result; the merge stays unchanged | A planned member that is no member of the repository is an internal error, which a unit test with a hand-built repository cannot set up, so the virtual road is tested over the integration stack |
| (b) A merge-only path that asks the upstream for the window and converts the answer in the merge | The member is a source of rows to the merge | No member request | A second copy of the forwarding rules; the member's populate check and cache would be decided on the virtual repository |
| (c) Search the member's cache only through a virtual repository, forward only on the remote | Simplest | No new road | R-NUGET-018's acceptance criteria (local packages before upstream results) cannot pass; developers add nuget.org as a second source, the setup R-NUGET-026 warns against |

**Choice:** (a). The member is asked for the window like a local member (`take` up to `nuget.search.virtual_merge_window`, from 0), and its results carry their forwarded members into the merge (Q-NUGET-78).

**Rationale:** the rules of R-NUGET-016 are the remote's, whichever road reaches it; the virtual repository adds only its claims and pin rules, which `Resolver.KeepsAway` already evaluates per member. The development agent revisits this when the merge asks a remote member for fewer results than the local members (Q-NUGET-58's revisit condition), which would change what the member is asked for.

**Applied in the spec:** R-NUGET-018 step 6. Code: [search_virtual.go](../../../internal/formats/nuget/search_virtual.go) `searchRemote`, [search_remote.go](../../../internal/formats/nuget/search_remote.go) `searchRemoteMember`, `remoteSearch`. Tests: `TestR_NUGET_018_ARemoteMemberIsSearchedThroughTheVirtualRepositorysMembers`, `TestR_NUGET_018_AVirtualRepositoryForwardsThroughItsRemoteMember` (`test/integration/nuget`).

### Q-NUGET-82 · A remote member's registration reaches the merge as the remote reads it: inflated, with the pages its index lists, converted by the remote

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NUGET-56 wanted a remote member to hand the merge its upstream index with every page inlined, and Q-NUGET-67 hands the merge the bytes the remote stores. Those bytes are gzip-coded when the upstream codes them (nuget.org's registration hive answers gzip to a request for identity), and the index of an ID with 128 or more versions lists its pages by address. The merge decoded neither, so every nuget.org ID answered 502 through a virtual repository; the code review of 2026-11-02 found it with a probe test. With it: the merge converted an upstream entry with its own code (an entry without `id` rendered `"id":""`, the members the remote keeps were dropped), a version counted as cached from any cached file of it (a `.nuspec` alone listed the version to a caller who may not fill), and the merge fingerprint covered the index's digest alone, so a page that changed under an unchanged index kept the old `ETag` and rendering.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The NuGet virtual adapter reads the member's registration the way the remote's own handler does (chosen) | `Merge` inflates the stored index the engine handed over, reads the pages it lists by address through the member (`Request.Member`, the member's remote engine, its populate right), converts every leaf with `remote.Convert`, and hands the merge entries; the version list is inflated before it is merged | One reading and one conversion for the remote and the virtual repository, so a version shows the same entry through both; the frozen contract is unchanged; a page is fetched, stored and revalidated like the remote's own | A page that cannot be read fails the whole merge, because a merge cannot mark one member partial (H-348); a page has its own period, so the merged entry can hold an older page until the index's entry expires |
| (b) The remote stores an inlined index under the variant `registration` | `PrepareFill` would assemble index and pages | The merge reads one document | `PrepareFill` sees one fetched document and fetches nothing; the engine would have to fetch pages inside a fill |
| (c) A hook of the virtual engine through which a format hands over a member's further documents | The engine collects pages like documents | A page failure could make the merge partial | A change of the frozen contract (ADR-141) for one format's paging |

**Choice:** (a). `merge.Part` carries `Entries` for a remote member and `merge.FromRemote` converts the remote's versions, keeping the members the remote keeps (`Entry.Other`). A member the caller may not fill contributes the leaves of the pages it has stored: a page it would have to fetch is left out, since the merge lists only its cached versions anyway (R-AUTHZ-008). One budget bounds the reading of every remote member of a merge (Q-NUGET-83). The bridge counts a version as cached only when the artifact the format's fill hook inspects is cached, NuGet's `.nupkg` (`formathost` `CachedVersion`, a format without a hook keeps its classification). The virtual engine's merge fingerprint ends with the SHA-256 of the merged bytes, so it changes whenever the bytes do: a format may read more than the documents the engine handed it.

**Rationale:** a remote and a virtual repository that disagree about one version are two answers to one question; reading through the member keeps the populate right and the engine's collapsing on every page. The fingerprint was "changes whenever the merged bytes could", which a format that reads further documents falsifies; hashing the bytes makes it exact at the cost of one SHA-256 per merge. The development agent revisits this when the contract can mark a member partial from inside a merge (then a page failure becomes a partial merge and H-348 closes).

**Applied in the spec:** R-NUGET-017 (member documents); Q-NUGET-56 superseded in part. Code: [virtual_remote.go](../../../internal/formats/nuget/virtual_remote.go), [virtual.go](../../../internal/formats/nuget/virtual.go), [merge/upstream.go](../../../internal/formats/nuget/merge/upstream.go), [handlers_remote.go](../../../internal/formats/nuget/handlers_remote.go), [formathost/virtual.go](../../../internal/formathost/virtual.go), [virtual/metadata.go](../../../internal/virtual/metadata.go). Tests: `TestR_NUGET_017_AGzipCodedRemoteMemberIsMerged`, `TestR_NUGET_017_ARemoteMemberWithPagesByAddressIsMergedWhole`, `TestR_AUTHZ_008_ARestrictedRemoteMemberContributesTheCachedVersionsOfTheStoredPages`, `TestR_NUGET_017_ARemoteEntryShowsTheSameThroughTheVirtualRepository`, `TestR_NUGET_017_ARemoteEntryIsTheRemotesOwnConversion`, `TestR_AUTHZ_008_AVersionCountsAsCachedOnlyWhenTheArtifactItsFillRecordsIsCached`, `TestR_REPO_020_TheMergeFingerprintFollowsTheMergedBytes`, and the integration tests `TestR_NUGET_017_AVirtualRepositoryMergesARemoteMemberThatGzipsAndPagesItsRegistration` (the stub's mode `gzip_always` and the 200-version ID through `nuget-mixed`), `TestR_AUTHZ_008_AVersionWhoseOnlyCachedFileIsItsNuspecIsNotListedToACallerWhoMayNotFill`.

### Q-NUGET-83 · What one rendering reads of an upstream registration: 512 pages per index, 32,768 versions, and one document's bytes for all of it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: nothing bounded how many pages an upstream's registration index may list. Each page it lists by address costs an upstream request, a stored document and possibly a URL-map row, and the rendering held the index and every page in memory, each up to `MaxDocumentBytes` inflated. One client request could make trove fetch and store as many pages as the upstream lists, and a rendering that failed cached nothing, so each retry paid again (the security audit of 2026-11-02).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An index of more than 512 pages is no registration index; one rendering reads at most 32,768 leaves and at most `MaxDocumentBytes` inflated over the index and all its pages together, converting page by page (chosen) | `remote.ParseIndex` refuses the index, so a fill stores nothing and no page is fetched; `remote.Budget` bounds the rendering of a remote and the merge of a virtual repository | Every cost of one request is bounded by constants; a rendering holds at most one document's worth of upstream bytes, the bound the heap gate of R-ARCH-005 assumes of a document held in memory | An upstream ID with more than 32,768 versions cannot be rendered |
| (b) A cap on pages alone | 512 pages | Simple | Pages of tiny leaves still turn the byte bound into millions of rows |
| (c) A setting per remote | `remote.limits.maxRegistrationPages` | An operator can raise it | A setting for a case no known feed has, and a new member of the repository document |

**Choice:** (a). Over the page cap: 502 `nuget_invalid_upstream_document`, the previous stored index kept. Over the leaves: 502 `nuget_invalid_upstream_document`. Over the bytes: 502 `upstream_item_too_large`. The budget of a virtual merge is shared by its remote members. The leaf bound holds at the fill too (found by the security audit of the fix wave): a page of more than 32,768 leaves, and an index that inlines more than 32,768 over its pages, are refused when they are fetched, before a leaf is converted or a URL-map row written, so one fill writes at most that many rows.

**Rationale:** nuget.org pages hold 64 leaves, so 512 pages are 32,768 versions; the caps are far above the IDs the development agent knows of (not checked against nuget.org's largest ID), and they turn "as many as the upstream says" into a constant. The development agent revisits the caps when an upstream ID needs more.

**Applied in the spec:** R-NUGET-015 (the bounds of a registration). Code: [remote/documents.go](../../../internal/formats/nuget/remote/documents.go), [remote/budget.go](../../../internal/formats/nuget/remote/budget.go), [remote/catalog.go](../../../internal/formats/nuget/remote/catalog.go) (`Collector`), [handlers_remote.go](../../../internal/formats/nuget/handlers_remote.go). Tests: `TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIsNoDocument`, `TestR_NUGET_015_AFilledPageOrIndexHoldsNoMoreLeavesThanOneRenderingReads`, `TestR_NUGET_015_APageOfMoreLeavesThanTroveReadsIs502AndMapsNothing` (`test/integration/nuget`), `TestR_NUGET_015_ARenderingReadsOneDocumentsWorthOfAnUpstreamRegistration`, `TestR_NUGET_015_CollectorConvertsPageByPageAndKeepsTheFirstLeaf`, and the integration test `TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIs502AndFetchesNoPage`.

### Q-NUGET-84 · Which fetched package is served without its identity check, and how its declared ID is compared

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-NUGET-53 served and cached every package the archive reader refused, without the identity check. That covered more than "this is no package": a `.nuspec` that compresses better than 100:1, more than 65,535 entries, names the push road refuses, duplicate or overlapping entries, a central directory the local headers contradict, several `.nuspec` files, an ID or a version that does not parse. An upstream could serve package B under the path of package A in such an archive and have it cached under A for good. The identity check compared the declared ID with the path's under Unicode case folding, which matches U+017F LONG S with `s`, and the rows took the declared spelling, while the item took the path's, so an eviction left an orphan package row.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only content no reader takes for a package passes as it came: no ZIP end record in the window a reader searches, a ZIP archive without a `.nuspec` at its root, a `.nuspec` that is not well-formed XML; every other refusal, and an ID or version that does not parse, is 502 `upstream_package_mismatch` and stores nothing (chosen) | What passes cannot be taken by a client for another package; everything a client might read is checked or refused | Closes the bypass; the push road's protections hold for cached packages too | A legacy package that a client reads and trove's reader refuses (more than 65,535 entries, a `.nuspec` in another encoding) cannot be restored through trove |
| (b) Every refusal passes | Q-NUGET-53 | Every legacy package is served | The bypass above |
| (c) Always run the identity check, under the fixed bounds of R-ARCH-028 only | Read the `.nuspec` whatever the reader refused | Fewer refusals | The refusals of structure (overlap, a directory two readers read differently) are what makes the `.nuspec` trove reads possibly not the one a client reads |

| Option for the ID | What it means | For | Against |
|---|---|---|---|
| (a) Both sides lower-cased character by character with Unicode's simple lowercase mapping, as .NET's `ToLowerInvariant` does, must be equal; the rows take the path's ID (chosen) | A client builds the path by lower-casing the ID that way, so the declared ID equals the path's exactly when a client would ask for it there | U+212A KELVIN SIGN matches `k` (a client asks for `kelvin`), U+017F LONG S does not match `s`; no orphan rows | Go's and .NET's tables can differ in a character the development agent did not check |
| (b) Unicode simple case folding | Q-NUGET-53 | — | Accepts IDs no client asks for at that path |
| (c) The declared ID with its ASCII letters lower-cased equals the path's ID | The audit's proposal | Strict | A client asks for a legacy ID such as `Ünïcode.Pkg` as `ünïcode.pkg`, which this refuses; R-NUGET-003 promises those IDs stay restorable (Q-NUGET-3) |

**Choice:** (a) and (a). The pass-through cases are recognised by the reader's reason (`nupkg.ErrNotZip` with no end-record signature in the last 64 KiB and 22 bytes, `nupkg.ErrNuspecCount` with no root `.nuspec` as `archive/zip` lists the entries, `nuspec.ErrNotXML`), so a damaged archive that another reader may still open is refused. The detail of the 502 names the reader's reason. The counter `trove_nuget_remote_unparseable_packages_total` counts what passes. This deviates from the fix the task named for the comparison ("ASCII lower-case"), for the reason in (c).

**Rationale:** the identity is the only check an upstream without a digest allows (H-35); a package served without it must be one nobody can mistake for a package. The development agent revisits the list when a feed in use serves packages that clients read and trove's reader refuses (then the reader's limits, not the pass-through, should move), and the comparison if a difference between Go's and .NET's lowercase tables shows.

**Applied in the spec:** R-NUGET-003 (the comparison and the rows' ID), R-NUGET-015 (the fill); Q-NUGET-53 superseded in part. Code: [remote_fill.go](../../../internal/formats/nuget/remote_fill.go), [id.go](../../../internal/formats/nuget/id/id.go) `SameID`. Tests: `TestR_NUGET_015_APackageWhoseIdentityCannotBeCheckedIs502AndStoresNothing`, `TestR_NUGET_015_ContentThatIsNoPackageForAnyReaderIsServedAsItCame`, `TestR_NUGET_003_TheRowsOfACachedPackageAreKeyedByThePathsID`, `TestR_NUGET_003_SameID`, `TestR_NUGET_003_InspectFillComparesANonASCIIIDAsAClientLowerCasesIt`.

### Q-NUGET-85 · A remote's deprecation, vulnerabilities and alternate package come without the upstream's addresses; advisoryUrl stays a link for people

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-015 said both "catalog entry properties are copied, including `deprecation` and `vulnerabilities`" and "every URL except `projectUrl`, `licenseUrl` and `iconUrl` is trove's". nuget.org writes both members as JSON-LD: the deprecation, each advisory and the alternate package carry an `@id` in its catalog or registration, and an advisory carries `advisoryUrl`, a page on github.com. The remote and the merge copied the members verbatim, so the criterion failed on every package with an advisory (`newtonsoft.json` below 13.0.1 on nuget.org); the stub had none, so CI passed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Drop `@id` and `@type` at every depth; keep `advisoryUrl` when it is an absolute `http` or `https` address without user info, like `projectUrl`; drop any other member that holds an address (chosen) | A client reads `reasons`, `message`, `alternatePackage.id` and `.range`, `severity`, `advisoryUrl`, all kept | The advisory link a person follows stays; no address of the upstream's API remains | A client that read `@id` would lose it (none known) |
| (b) Rewrite each `@id` to trove's leaf with a fragment, the alternate package's to trove's registration index | The JSON-LD shape stays | Closest to nuget.org's bytes | Addresses that nothing resolves on trove's side (a leaf fragment), for no client that reads them |
| (c) Pass both members verbatim and amend the criterion | Q-NUGET-52 | Nothing lost | The upstream's catalog addresses in trove's documents |
| (d) Drop `advisoryUrl` too | Strict | Every URL trove's | The advisory a person needs to judge a vulnerable dependency is gone |

**Choice:** (a). The members are written compact with their keys in order; a member with nothing left is absent; a non-web `advisoryUrl` is dropped and its advisory kept with its severity. The local class has neither member. The stub's corpus deprecates `Newtonsoft.Json` 13.0.1 (with an alternate package) and records an advisory against it, with nuget.org's JSON-LD addresses, so the rule is in every run.

**Rationale:** `advisoryUrl` is a link shown to people, like `projectUrl`, which trove never follows; the JSON-LD addresses are the upstream's API, which trove replaces everywhere else. The development agent revisits this if a client turns out to read a dropped member.

**Applied in the spec:** R-NUGET-015 (the copy rule, the URL table and the criterion on the URLs of a registration), R-NUGET-016 (the forwarded search cleans both members the same way); Q-NUGET-52 superseded in part. Code: [remote/passthrough.go](../../../internal/formats/nuget/remote/passthrough.go) (`Deprecation`, `Vulnerabilities`), [remote/catalog.go](../../../internal/formats/nuget/remote/catalog.go), [search_remote.go](../../../internal/formats/nuget/search_remote.go) `upstreamResult.hit`, [test/nugetstub](../../../test/nugetstub/docs.go). Tests: `TestR_NUGET_015_DeprecationAndVulnerabilitiesComeWithoutTheUpstreamsAddresses`, `TestR_NUGET_016_AForwardedResultComesWithoutTheUpstreamsAddresses`, `TestR_NUGET_015_APassThroughMemberWithNothingLeftIsAbsent`, `TestR_NUGET_015_TheCorpusDeprecatesAVersionAndRecordsAnAdvisory`, and the integration tests `TestR_NUGET_015_RegistrationsAreRenderedOnTheServingRepository`, `TestR_NUGET_017_AVirtualRepositoryMergesARemoteMemberThatGzipsAndPagesItsRegistration`.

### Q-NUGET-86 · The first answer to a cache miss: a body within the withheld tail is answered from the stored entry, a longer one streams with the headers that do not depend on the body

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the request that fetches an artifact streams the upstream's body to its client while it arrives (R-REPO-041, Q-REPO-94); that answer carried the upstream's media type, the length and `Cache-Status`, and none of `ETag`, `Last-Modified`, `Accept-Ranges`, `Cache-Control` and `Repr-Digest`, which every later answer from the stored entry carries. The e2e test of E3.6 found it on the `.nuspec` of a remote and had to fetch the file once before it compared replicas. NuGet's `.nupkg` was not affected: the fill hook serves its leader from the stored entry (Q-ARCH-267).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A body whose announced length fits in the withheld tail (64 KiB) is not streamed: the stored entry answers it with every header of a stored answer; a longer body streams with `Accept-Ranges` and the `Cache-Control` of a stored answer, which the host names (`proxy.Request.Headers`) (chosen) | No byte of a body within the tail leaves before the commit anyway, so serving it from the entry costs nothing; a long body keeps its early first byte | Small files (a `.nuspec`, most documents of a package) answer the same on the first request as later; a streamed answer differs only in the validators the whole body decides | A body longer than 64 KiB still has no `ETag`, `Last-Modified` or `Repr-Digest` on its first answer |
| (b) Never stream: the leader is answered from the stored entry | Q-REPO-94 (c) | Every first answer is a stored one | Q-REPO-94 refused it: a large artifact sends nothing for minutes and meets idle timeouts of the edge |
| (c) Keep the stream as it was | — | No change | The difference E3.6 found |

**Choice:** (a). With the object store unavailable, a body that was held back (the fill hook, or a body within the tail) is replayed to the leader from the spool, verified, with the same headers. npm and every other format follow the same rule; their tarballs above 64 KiB stream as before.

**Rationale:** the withheld tail already decides when the first byte leaves; making the threshold of streaming the same number removes the only case in which streaming bought nothing and cost the headers. The validators of a longer body need the body. The development agent revisits this when a client needs a validator on the first answer of a large artifact (then the leader waits for the commit as a waiter does, and Q-REPO-94 is reopened).

**Applied in the spec:** [repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-041 (the withheld tail). Code: [proxy/artifact.go](../../../internal/proxy/artifact.go), [proxy/integrity.go](../../../internal/proxy/integrity.go), [proxy/types.go](../../../internal/proxy/types.go) `Request.Headers`, [formathost/remote.go](../../../internal/formathost/remote.go). Tests: the integration test `TestR_NUGET_015_TheFirstAnswerOfACacheMissCarriesTheHeadersOfAStoredAnswer` (directly and through a virtual repository), and the proxy and npm integration packages, which pass unchanged.

### Q-NUGET-87 · On an endpoint with rules the validator of a local or virtual document follows its filtered bytes, and a virtual validator names the ID

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the weak ETag of a local document was a hash of its inputs with `packages.rev_seq`, that of a virtual document one with the merge fingerprint (R-NUGET-002, Q-NUGET-9). Both rise with every write of the ID, a write of a version that an endpoint with rules hides included, while the filtered body stays the same, so a consumer polling with `If-None-Match` learned when hidden versions were pushed, unlisted, relisted or deleted (security audit of 2026-11-02, finding F-23). The virtual value did not name the ID either: the engine's fingerprint names the members and their revisions, so two IDs of one member at one revision shared a value (code review of the same day).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) On an endpoint with rules the SHA-256 of the filtered identity rendering takes the place of the revision and of the merge fingerprint among the inputs (chosen) | `serveFiltered` and `serveVirtualFiltered` take the identity rendering from the rendition cache, keyed as before, or render it, and hash it into `document.Inputs.Content` | The value moves exactly when the shown bytes move; every replica computes it alike, because a rendering is a function of its inputs (R-NUGET-030); gzip and identity share it; an empty `Content` leaves the hash as it was, so a local document outside such an endpoint keeps its value | A 304 on such an endpoint costs a cache hit, or after a write of the ID a read of the rows and a rendering, because the rendition cache is keyed by the revision |
| (b) A hash of the filtered inputs: the exposed versions with their listed state and a digest of their metadata | A pass over the rows before the 304 | No rendering for a 304 | A second definition of what a document shows beside the renderer, which has to follow every field the renderer adds |
| (c) Keep the revision and record the signal as a gap beside H-60 | Nothing changes | No cost | The signal stays, on the endpoints that exist for consumers who must not learn about unreleased versions |

**Choice:** (a). The variant of a virtual value carries the lower-case ID, as the remote's does.

**Rationale:** the bytes the endpoint shows are the one input that may move the value there, and hashing them is the one definition that cannot drift from the renderer; the cost falls on endpoints with rules only, and only after a write. The new inputs change the value of every virtual document and of every document on an endpoint with rules once: during the rolling upgrade a client may get a 200 where a 304 was possible, never a 304 for other bytes. The remote class first kept the digest of the stored upstream documents (Q-NUGET-52), which an upstream change of a hidden version moves; the development agent applied the same choice to it (`serveRemoteFiltered` in `serveRemote`), and the stored digests stay in the rendition key only. The development agent revisits this if the renderings on endpoints with rules show in the request latency.

**Applied in the spec:** R-NUGET-002 (the inputs of the value), R-NUGET-034 (Validators). Code: [documents.go](../../../internal/formats/nuget/documents.go), [handlers_virtual.go](../../../internal/formats/nuget/handlers_virtual.go), [handlers_remote.go](../../../internal/formats/nuget/handlers_remote.go), [document.go](../../../internal/formats/nuget/document/document.go). Tests: `TestR_NUGET_034_TheValidatorOnAnEndpointWithRulesFollowsOnlyWhatTheEndpointShows`, `TestR_NUGET_034_TheValidatorOfAVirtualDocumentOnAnEndpointWithRulesFollowsOnlyWhatItShows`, `TestR_NUGET_034_TheValidatorOfARemoteDocumentOnAnEndpointWithRulesFollowsOnlyWhatItShows`, `TestR_NUGET_002_TheValidatorOfAVirtualDocumentNamesThePackage`, `TestR_NUGET_002_AnEmptyContentKeepsTheValueOfEarlierReleases`.

### Q-NUGET-88 · A removal never waits for a folder inside its transaction; a folder another transaction holds goes after the commit

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a push of a new version holds the ID's folder and every other ancestor `FOR SHARE` from its link step to its commit (Q-STOR-144) and takes the package row in its storage hook (R-NUGET-006). A hard delete of another version takes the package row first, because the version and the tombstone are read under it (R-NUGET-006), and after removing the files it took the folders it emptied `FOR UPDATE`, waiting for the push. The two waited for each other in a circle, which PostgreSQL broke after `deadlock_timeout` with 40P01 and the retry repeated (code review of 2026-11-02, finding F-23). Every removal that locks a package row before its files takes the same order: a raw delete through `/files/` (the generic layer locks the packages in `DeleteSpec.Extra`), the eviction of a remote, an npm unpublish. The integration test that arranges the circle and then races six pushes against six hard deletes on two replicas counted 20 deadlocks in its database before the change, and three requests of the race answered 500 when the retry gave up.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A removing transaction takes a folder's lock only when it is free (`FOR UPDATE SKIP LOCKED`); a folder another transaction holds is removed after the commit in a transaction of its own (chosen) | `items.Store.removeEmptied` and `sweepAncestors` in `RemoveIn` and the synchronous `Delete`; the statements `LockFolderExclusiveFree` and `FolderLive`; the sweep runs from `meta.Tx.After`, waits for the holder, removes the folder if it is still empty and walks up | One rule for every removal and every format; no removal waits for a deploy, so no circle through a package row remains; an uncontended removal removes its folders in its own transaction as before | A folder that was held goes in a second transaction after the commit, and the answer of the request waits for it (bounded by `lock_timeout`); a process that dies between the two leaves an empty implicit folder until a later removal below it takes it |
| (b) The delete locks the folder chain before the package row | One order: folders, then the package row | The textbook form | The format's route has no way to lock folders: a method on `formats.PackageWriter` changes the frozen contract (ADR-141); the `/files/` road locks packages in `Extra` before the store sees a folder; every deploy below the ID waits for every delete |
| (c) Look for other children before taking the lock | Skip the lock when another child is visible | Small | The circle stays when the deleted version was the last committed child (the push's own child is not committed); two deletes that each see the other's child both keep a folder that ends up empty |
| (d) Keep it: the retry resolves it | — | Nothing to build | One `deadlock_timeout` per collision, and a 500 when collisions repeat, as the race showed |

**Choice:** (a).

**Rationale:** the removal is the side that can wait: an empty folder is a cosmetic state for one more short transaction, while a deploy cannot go on without its folders. The job `items-delete` removes its folders in transactions that hold nothing else and keeps waiting for them. The development agent revisits this if the sweep's log line ("was not removed after its commit", a `lock_timeout` behind a long deploy) shows in operation.

**Applied in the spec:** R-NUGET-007 (a hard delete beside a push of another version). Code: [sweep.go](../../../internal/items/sweep.go), [delete.go](../../../internal/items/delete.go), [files.sql](../../../internal/meta/postgres/queries/files.sql). Developer docs: [items.md](../../developer/items.md), [files.md](../../developer/files.md). Tests: `TestR_NUGET_007_APushAndAHardDeleteOfAnotherVersionNeverDeadlock`, `TestR_STOR_018_ARemovalDoesNotWaitForAFolderAnotherTransactionHolds`.

### Q-NUGET-89 · The package reader refuses a second end record after the one it reads and holds every entry to the data offset it checked

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the bounded scan of R-NUGET-005 step 1 (Q-NUGET-23) takes the end-of-central-directory record whose comment ends at the end of the archive. `archive/zip` of Go 1.27.3, which opens the entries, takes the last end-record signature whose 22-byte record fits, also when bytes follow its comment. An archive with a second end record inside the first one's comment was therefore read through two central directories: the scan checked one, the standard library served the other, and the two were compared by name, flags, method and sizes only (security audit of 2026-11-02, finding F-26). The unit test builds such a package whose second directory points the `.nuspec` at other bytes of the same size: before the change `Inspect` returned the identity `Evil.Widgets` of the bytes the scan never checked, while the scan had checked `Acme.Widgets`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse an end-record signature after the chosen record wherever a whole record fits, and compare each entry's `(*zip.File).DataOffset()` with the offset the scan computed from the local header it checked (chosen) | `endRecord` in [scan.go](../../../internal/formats/nuget/nupkg/scan.go), `readBy` in [nupkg.go](../../../internal/formats/nuget/nupkg/nupkg.go); 400 `nuget_invalid_package` with the reason that the two readers disagree | The two readers read one directory and take every entry's data from the place the scan checked; the offset comparison also catches a reader that moves the offsets, as `archive/zip` does for bytes in front of an archive | One more local header read per entry; a package whose comment holds the signature where a record fits is refused |
| (b) Take the record `archive/zip` takes | Follow the library's rule | No refusal | The check follows one library's heuristic, and the scan's rule exists so that a comment cannot carry an archive of its own (`TestR_NUGET_005_NupkgCommentCannotHideAnArchive`) |
| (c) Compare the checksums as well | Each entry's CRC-32 beside the offset | — | Once both readers read one directory the checksums are those of the same records |

**Choice:** (a).

**Rationale:** the reader's promise is that what was checked is what is served; refusing the ambiguous archive keeps that without following any reader's guess. The price is the refusal of a package whose comment holds the signature where a record fits. The development agent revisits this if a package from a real client is refused for a signature in its comment.

**Applied in the spec:** R-NUGET-005 step 1. Tests: `TestR_NUGET_005_AnEndRecordAfterTheOneTheScanChoseIsRefused`, `TestR_NUGET_005_EveryEntryIsReadFromTheOffsetTheScanChecked`; the seed and the corpus entry `second-end-record` of `FuzzR_ARCH_028_Inspect`.

### Q-NUGET-90 · A forwarded search answer is read within 8 MiB, and a result within the versions trove reads of one ID

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine keeps an upstream's search answer as received (`proxy_query_results`, Q-ARCH-270) and the format parses and filters it on every use, so that a claim or a grant change applies to a cached answer at once (R-NUGET-016). The answer was decoded within `MaxDocumentBytes` (128 MiB), a gzip coding the upstream applied although identity was asked included, and each result's versions were deduplicated by comparing every pair: an upstream could make every use of a cached answer inflate a small gzip stream to 128 MiB, and a result of 40,000 versions took about a minute of CPU (security audit of the fix wave, 2026-11-02).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the answer within 8 MiB inflated; remove a result that lists more than `remote.MaxLeaves` (32,768) versions; deduplicate with a set; stop when the request ends (chosen) | `maxSearchAnswerBytes`, `upstreamResult.hit`, `keepForCaller` | Each use costs at most 8 MiB of inflation and linear work; 8 MiB is the default of `proxy.query_cache_bytes`, beyond which the engine keeps no copy anyway; nuget.org answers `take=20` in some tens of KiB | A `take` near `nuget.search.max_take` over packages with long version lists can exceed it and is answered from the cache |
| (b) Keep the converted hits in the engine's cache instead of the bytes | Parse once per fetch | Least work per use | The engine stores bytes for every format (Q-ARCH-270); the frozen contract (ADR-141) would need an ADR |
| (c) Refuse a gzip coding when identity was asked | Strict | No inflation at all | nuget.org's hosts gzip some answers regardless; the search would fall back to the cache for them |

**Choice:** (a). The same cap of versions that one rendering of a registration reads (Q-NUGET-83) applies to one result, counted before deduplication, because the work is proportional to what the upstream sent.

**Rationale:** the bound follows the one place the answer is kept and keeps the per-use cost of a cached answer small and constant; a result beyond the version cap is the same refusal the registration gives for that ID. The development agent revisits this when a common client asks for pages that exceed 8 MiB, or with the next ADR on the format contract, which could hand the format a parsed copy (b).

**Applied in the spec:** R-NUGET-016 (the answer bound). Code: [search_remote.go](../../../internal/formats/nuget/search_remote.go) `maxSearchAnswerBytes`, `parseUpstreamSearch`, `upstreamResult.hit`, `keepForCaller`. Security: [nuget-remote.md](../../security/nuget-remote.md). Tests: `TestR_NUGET_016_AnUpstreamAnswerIsReadWithinItsBounds`.

### Q-NUGET-91 · The run against nuget.org is a read-only subset of the closing set, with the search first and an ID of more than 128 versions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.1 asks for "the run against nuget.org, in the full run of Test and Release on the commit that closes the phase".
R-NUGET-027's row asks for the remote and symbol criteria against nuget.org and its symbol server, and R-NUGET-015 and
R-NUGET-016 state their criteria against the stub in CI and against nuget.org in that run. The counting stub covers every
criterion that needs a switch: a stopped upstream, a delay, a mismatch, a gone ID and request counts. What only nuget.org shows
is its real shape:
- registrations stored gzip-encoded, whatever the request asked for;
- the pages of a large ID listed by address, not inlined;
- a search with download counts;
- vulnerabilities on old versions.

npm runs its public subset in `TestNPMClosing` (Q-NPM-46).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A read-only subset in the set `nuget-closing`: `TestNuGetClosingPublic` (chosen) | A remote of `https://api.nuget.org/v3/index.json` (no `extraHosts`, so the default `*.nuget.org`) and a virtual repository with `nuget-e2e` and that remote, both new for the run. The cases run in this order: (1) `dotnet package search Newtonsoft` through the remote while it has cached nothing, and the addresses of the API search; (2) nuget.exe installs Newtonsoft.Json 13.0.3 through the remote; (3) `dotnet restore` of Newtonsoft.Json 13.0.3 and Serilog 4.3.0 through the virtual repository; (4) the registration of `newtonsoft.json` and of `serilog` through both repositories, with every page followed and every address checked (Q-NUGET-92), and Serilog's versions counted | Every criterion of R-NUGET-015 and R-NUGET-016 that needs no switch runs against the real upstream once per phase, and nothing is written upstream. The cases that need a fix still in progress stay in the test and fail until the fix lands, so they cannot be forgotten | The run depends on nuget.org being reachable from the runner, and on the pinned versions staying published: unlisting keeps a version in the registration; deletion is rare |
| (b) The same cases in the set `nuget`, on every change | — | An earlier signal | CI on every change would depend on the public internet, which R-NUGET-027 rules out for the per-change run |
| (c) Recorded nuget.org answers in the stub | — | No network | Q-NUGET-26 chose a generated corpus; a recording ages and proves nothing about the upstream as it is on the day the phase closes |

**Choice:** (a). The search runs first because a remote searches its own cache until the forwarded search exists. After a
restore, a cache-only search would list Newtonsoft.Json too. On a remote that has cached nothing, a hit with nuget.org's
download count (at least 1,000,000) can only come from forwarding.

The packages are chosen as follows:
- Newtonsoft.Json 13.0.3 is the version R-NUGET-015 names. It has 86 versions in inlined pages.
- Serilog 4.3.0 has 602 versions in ten pages of 64 that nuget.org does not inline (2026-11-02), so the merge of a remote
  member must read the upstream's pages (R-NUGET-017).

Neither package has a dependency for `net10.0`, so a restore reads exactly these IDs. The count is asserted as "more than 128",
not as an exact number, because nuget.org adds versions.

Not in the subset:
- The symbol server: R-NUGET-022 is P1 and not built.
- The criteria that switch the upstream off: nuget.org cannot be switched off, and the stub covers them on every change.

**Rationale:** the closing run should prove the protocol against the upstream customers use, not only against the stub built
from its description, and it must not change that upstream. The development agent would revisit this in two cases:
- the CI runners cannot reach nuget.org;
- Serilog's registration changes shape (its pages inlined), in which case another ID with more than 128 versions in non-inlined
  pages takes its place.

**Applied in the spec:** R-NUGET-027 (the row of the closing run against nuget.org: the symbol criteria join with R-NUGET-022).
Code: [nuget_public_test.go](../../../test/e2e/nuget_public_test.go). Test: `TestNuGetClosingPublic`.

### Q-NUGET-92 · What the address check of the run against nuget.org counts as an address

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-015's criterion says that every URL of the registration index except `projectUrl`, `licenseUrl` and `iconUrl`
starts with `<base>/api/nuget/<key>/v3/`. nuget.org's documents carry three more kinds of URL:
- the IRIs of the JSON-LD `@context`, such as `http://schema.nuget.org/schema#`, and in a search answer an `@base` of the
  upstream's registration hive;
- the GitHub advisory page in `vulnerabilities[].advisoryUrl`, on `newtonsoft.json` below 13.0.1;
- the `@id` of a vulnerability or a deprecation.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every string member that is an absolute `http` or `https` URL, except `projectUrl`, `licenseUrl`, `iconUrl`, `advisoryUrl` and the whole `@context` (chosen) | `foreignAddresses` walks the document | It catches any upstream address a client could follow (a page, a `packageContent`, an `@id` in a vulnerability), including members that no table names | `advisoryUrl` is left out, as Q-NUGET-85 keeps it |
| (b) Only the members of R-NUGET-015's rewriting table | — | The letter of the table | It misses members nobody listed, such as the `@id` in `vulnerabilities`, which is what Q-NUGET-85 removes |
| (c) Every URL, without exceptions | — | Simple | The web links and the vocabulary IRIs are not trove's by design, so the check could never pass |

**Choice:** (a). `advisoryUrl` is a page for people, like `projectUrl`, and Q-NUGET-85 keeps it in the registration and the
forwarded search only as an absolute `http` or `https` address without user info, so the check leaves it out like the other
links for people; every `@id` and `@type` inside `deprecation` and `vulnerabilities` is dropped there and is checked here. A
NuGet client never requests an IRI of `@context`.

**Rationale:** the criterion is about where a client goes next, and every member that holds an address can send it somewhere.
Listing the exceptions instead of the rules keeps a new upstream member from slipping past the check. The development agent
would revisit this if a NuGet client starts to resolve JSON-LD IRIs, or if Q-NUGET-85 is revisited for `advisoryUrl`.

**Applied in the spec:** none (R-NUGET-015's criterion is unchanged; the check reads it). Code: `foreignAddresses` in
[nuget_public_test.go](../../../test/e2e/nuget_public_test.go).

### Q-NUGET-93 · The versions stored before the SemVer 2 classification counted dependency ranges are recomputed once, by a background job in Go

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: up to release 0.9.0 a push and a remote's fill classified a version as SemVer 2.0.0-specific by its own number
alone. Since the fix wave of 2026-11-02 a bound of one of its dependency ranges counts as well (R-NUGET-004). The flag is
stored and the rule applies only when a row is written, so a version stored before with a dependency on, say,
`[2.0.0-rc.1, )` keeps `is_semver2 = false`. It still shows to clients older than NuGet 4.3 in the `registration/` and
`registration-gz/` hives and in a search without `semVerLevel=2.0.0` (finding F-30 of the code review of the fix wave).

What constrains the answer:
- Migrations 00017 and 00018 are released and frozen (R-ARCH-026).
- Every row ever written carries its dependency groups, with the range strings of the `.nuspec` verbatim, in
  `nuget_version.metadata` (`rows.Dependency.Version`, since 0.8.0, the release of the push), so no stored `.nuspec`
  needs to be read.
- During a rolling upgrade from 0.9.0 its replicas still write rows the old way.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A background data migration in Go, `data_migration_nuget_semver2` (chosen) | A worker queues the job when it starts while `system_state` has no key `nuget_semver2_recomputed`. The job reads the rows that say SemVer 1 in keyset batches, classifies each with `version.PackageIsSemver2` from `original_version` and the ranges in `metadata`, sets the flag under the package row lock, raises the package's revision, and records the key at the end | The rule exists once, in the leaf the push runs and the golden vectors test (R-NUGET-003); the revision raise is the core writer's, so rendered documents, validators and caches follow (R-NUGET-030); resumable and visible in the job API, as R-HA-022 wants a backfill; it can wait for the replicas of 0.9.0 | A kind, an enqueue at the start of a worker and a `system_state` key for one correction |
| (b) Migration 00019 in SQL | An `UPDATE nuget_version` over the rows whose `metadata` holds a range with a bound that has a dotted pre-release label or build metadata | One statement; nothing runs later | SQL would re-implement the version and range grammar of R-NUGET-003 outside the tested leaf (bare versions, intervals, white space, four-part numbers), and a pattern that drifts from it decides silently; the revision would have to be raised in SQL too; R-HA-018 sends backfills to background migrations, never into the migration Job; it runs while replicas of 0.9.0 still write |
| (c) Leave the old rows | Only rows written from now on are right | Nothing to build | F-30 stays: legacy clients keep getting versions they cannot parse |

**Choice:** (a).
- **Trigger.** Every `worker` and `all` replica queues the job when it starts, in the transaction that finds the key
  absent; the dedup mode is `coalesce`, so at most one run waits and two never run together. A run that finds the key
  ends at once with the state `done`.
- **Gate.** While a live replica runs 0.9.0 or older (`postgres.ReplicasUpTo`), a run changes nothing, records the
  state `waiting` with the replicas it waits for, and queues the next run a minute later. This is R-HA-022's rule that a
  background migration starts only when the cluster runs the release that introduced it. The check repeats before the
  completion is recorded, for a replica of the old release that came up meanwhile.
- **Batches.** `jobs.background_migrations.batch_size` rows with `pause_between_batches` between them; the cursor
  (the last version id) and the counts are the job's checkpoint, so a run released at shutdown resumes.
- **Rows.** Only rows that say `false` are read, trashed versions included: the rule only gained a reason to say true.
  A row whose version or metadata does not parse keeps its flag, is counted as `unreadable` and is named in the log.
- **Write.** One transaction per package: the package row lock first (the order of every writer of the ID), the flag
  only where it is still `false`, then `packages.Writer.BumpRevision` with the `metadata_changed` signal the host's
  writer attaches (Q-ARCH-259).
- **No audit event.** It corrects a derived flag; no principal changed the package, and `package.metadata_changed`
  names an unlist or a relist. The job's result and the value of the key carry the counts (`examined`, `changed`,
  `packages`, `unreadable`).
- **Completion.** The key in `system_state`, the table of the installation's facts (R-OPS-024), as
  [Q-HA-29](../ha-and-helm.md#q-ha-29--where-the-garbage-collection-pause-is-stored) keeps the deletion pause there. No
  migration 00019, and not R-HA-022's `background_migration` table, which is not built. `system_state` has the export
  class `data` ([backup-and-restore.md](../backup-and-restore.md) R-BAK-004), so a restore brings back the
  key with the rows it belongs to.

**Rationale:** what must not drift is the rule that decides. (a) runs the function the push and the remote's fill run,
so a stored row and a new row cannot disagree. The revision goes up through the core writer, so every cache and
validator keyed by it follows without the migration knowing any of them. The development agent revisits this in two
cases:
- R-HA-022's framework is built: the state moves to `background_migration`, with pause and resume.
- An installation rolls back to 0.9.0 after the run and pushes on it: the key then has to be removed by hand and a
  worker restarted, as the admin manual describes.

**Applied in the spec:** R-NUGET-004 (the recomputation of the stored rows); [architecture.md](../architecture.md)
R-ARCH-016 (the kind). Code: [semver2.go](../../../internal/formatdata/nuget/semver2.go),
[nuget_semver2.sql](../../../internal/meta/postgres/queries/nuget_semver2.sql), `version.PackageIsSemver2` in
[range.go](../../../internal/formats/nuget/version/range.go), `postgres.ReplicasUpTo` in
[gate.go](../../../internal/meta/postgres/gate.go), `startFormatMigrations` in
[wire_formats.go](../../../internal/app/wire_formats.go). Tests: `TestR_NUGET_004_PackageSemver2`,
`TestR_NUGET_004_ADependencyRangeWithASemVer2BoundMakesAPushedVersionSemVer2Specific` and
`TestR_NUGET_004_TheRecomputationDecidesLikeAPush` over one table of cases
([dependency-semver2.json](../../../internal/formats/nuget/version/testdata/dependency-semver2.json)),
`TestR_HA_022_ReplicasUpToARelease`, and against PostgreSQL
`TestR_NUGET_004_AWorkerRecomputesTheSemVer2FlagOfVersionsStoredBeforeTheRule` and
`TestR_HA_022_TheSemVer2RecomputationWaitsForAReplicaOfTheOldRelease`
([semver2_test.go](../../../test/integration/nuget/semver2_test.go)).

### Q-NUGET-94 · A forwarded answer fetched under the remote's current configuration outlives both applications of a change

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NUGET-016 keeps a forwarded answer per replica in `proxy_query_results`, keyed by the remote, its configuration
version and the upstream URL (Q-ARCH-270), and its claim criterion needs every replica to still hold that answer when the
claim lands. A replica applies a change of `repo_config` twice: at once (the writer itself, or Valkey's message) and again
with the change-log poll up to `cluster.epoch_poll_interval` later ([architecture.md](../architecture.md) R-ARCH-018 steps
2 to 4). The cache dropped every copy of the repository on each application, and its fill (`Cache.Load`) stored nothing
while the applied epoch of `repo_config` moved. A copy fetched within one poll interval after the remote was created or
changed, under the configuration that change produced, was therefore dropped by the poll's second application, and a fill
that ran while the poll applied a change of any other repository was not kept at all. Test and Release run 41441061809
(2026-11-02) showed it: in `TestNuGetSearchClaimTiming` the first of three replicas cached the answer 0.97 s after the
remote's creation, the other two after 1.0 s, and after the claim only the first asked the upstream again (one new search).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A change drops the copies of older configuration versions only, and a fill is stored without the epoch check (chosen) | The `repo_config` payload carries the `config_version` the write left (`{"id", "key", "op", "version"}`); `proxy_query_results` drops the repository's copies whose key holds an older version, and every copy on `deleting` and `deleted` and on a payload without a version; `Engine.Query` stores a fetched answer with `Cache.Set` | The key already carries the version, which makes it R-ARCH-018's self-validating key: a copy is served only to a request that read the same configuration, so a change message only evicts early. A copy survives both applications of its own change and every change of another repository. The other consumers of the area ignore the new member | A fill that a change of its own repository overtook stores a copy under the older version; no request reads it, and it leaves with the period or the LRU |
| (b) The e2e test waits one poll interval after it creates its repositories | The test changes, the product does not | No code change | The extra upstream search after every change of a repository stays, per replica and query; a fill of any remote that meets any repository's change is still not kept; "both still hold the cached upstream response" would hold by timing only |
| (c) The poll skips a change the replica already applied from Valkey | Deduplicate by epoch | No payload change | A lost, early or forged message would then suppress the change; R-ARCH-018 step 4 excludes it |
| (d) No area for the cache: the versioned key and the period alone | No eviction at all | Simplest | The copies of an older configuration and of a deleted remote stay up to `remote_result_ttl` (at most one hour) in the 8 MiB budget |

**Choice:** (a). A payload without a version is the one an older replica writes during a rolling upgrade
([ha-and-helm.md](../ha-and-helm.md) R-HA-020), and an unreadable payload still flushes the whole cache. The version is the
row's `config_version` after the write: a create writes 1, an update and the mark for deletion raise it.

**Rationale:** the key was chosen self-validating so that change messages only evict early; the cache did not use that and
paid for every change twice on every replica, and the fill rule, which protects caches filled from PostgreSQL, kept answers
of the upstream out whenever anything in the installation changed a repository. The development agent revisits this if a
setting outside `config_version` starts to decide what an upstream answers, which the key would then have to carry.

**Applied in the spec:** R-NUGET-016 (the key and the change of the remote's configuration),
[repositories-and-proxy.md](../repositories-and-proxy.md) (the payload of `repo_config`, in the preamble of the management
API), [architecture.md](../architecture.md) Q-ARCH-270 (amended). Code: `changePayload` in
[service.go](../../../internal/repository/service.go), `registerQueries`, `queryEviction` and `Query` in
[query.go](../../../internal/proxy/query.go). Tests: `TestR_ARCH_013_ACopyOfTheCurrentConfigurationOutlivesTheChange` in
[query_test.go](../../../test/integration/proxy/query_test.go), `TestQueryEviction` in
[query_test.go](../../../internal/proxy/query_test.go), `TestCreateReadUpdate` (the payload's version) in
[crud_test.go](../../../test/integration/repository/crud_test.go), `TestNuGetSearchClaimTiming` in
[nuget_search_claims_test.go](../../../test/e2e/nuget_search_claims_test.go).

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
