# Repositories and pull-through proxy

Status: specification — not implemented

This page specifies trove's repository model and the management API around it: the configuration
document of a repository, keys and addresses, validation, create, read, update, list and delete. It
specifies how virtual repositories resolve requests, including protection against dependency
confusion through pin rules, namespace claims and a shadowing check on first publishes. And it
specifies the pull-through proxy of remote repositories end to end: settings and defaults, cache
state in PostgreSQL, collapsing of concurrent misses within a replica and across replicas through a
Valkey lock, metadata freshness, negative caching in Valkey, upstream failure handling and offline
modes, upstream authentication and stored secrets, client certificates, outbound HTTP proxies and
CA bundles, egress address rules, redirects, size limits, integrity verification, metadata URL
rewriting, cache invalidation and the settings that drive cache cleanup. It specifies the mirror mode of a remote
repository, in which background passes, jobs on a queue of their own, store what the upstream offers within the remote's
bounds, with throttling, a lower priority than clients, and status (R-REPO-050 to R-REPO-059). It states how all of this
behaves on n interchangeable replicas behind a Service without session affinity, while Valkey is
unavailable, and while a replica drains (R-REPO-049). Bytes, items and the operations on stored
items (delete, copy, move, properties, trash can) are specified in [storage.md](storage.md); who
may do what in [authorization.md](authorization.md); format-specific metadata in
[formats/npm.md](formats/npm.md) and [formats/nuget.md](formats/nuget.md).

Covers: [repositories.md](../features/repositories.md) (`REPO-*`),
[remote-repositories.md](../features/remote-repositories.md) (`RMT-*`), the cache-invalidation
entry ART-050 of [artifacts.md](../features/artifacts.md), the repository operation entries of
[api-content-operations.md](../features/api-content-operations.md) (`API-*`; of the replication entries API-082 to API-085,
pulled forward by the mirror mode, Q-REPO-143), BEYOND-028 of
[beyond-baseline.md](../features/beyond-baseline.md) and IAM-045 of
[identity-and-access.md](../features/identity-and-access.md).

## Where this page hands over

| Topic | Owner |
|---|---|
| Base tables (`repositories`, `items`, `blobs`, `packages`), upload and download pipeline, spool, delete, copy, move, properties, property sets, trash can, garbage collection, quotas, repository caps enforcement, the remote-cache cleanup job | [storage.md](storage.md) |
| Permission model, cache-fill rule, virtual-repository gate, capabilities and roles, projects, package ownership, existence hiding | [authorization.md](authorization.md) |
| Format plug-in interface in Go, job framework, change areas and signals (R-ARCH-018), the Valkey usage map and client (R-ARCH-030, R-ARCH-031), `cluster.FlightLock` (R-ARCH-033), per-replica state rules (R-ARCH-032), process shutdown (R-ARCH-024), the stored-secret key | [architecture.md](architecture.md) |
| npm and NuGet: document classes, metadata merge, URL rewrite details, digest sources, version-removal tracking, format settings | [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| Public base URL and trusted proxies, metrics catalog, upstream log record, audit storage, cache-invalidation endpoint surface, configuration as code and `secretRef`, read-only maintenance mode | [operations-and-observability.md](operations-and-observability.md) |
| API conventions (error envelope, paging, job resource), UI | [api-and-ui.md](api-and-ui.md) |
| Helm values for CA bundles and secret mounts, egress `NetworkPolicy`, database connection budget, Valkey topology and degraded mode (R-HA-056, R-HA-057), termination grace period (R-HA-011), node drain (R-HA-059) | [ha-and-helm.md](ha-and-helm.md) |
| Export classes of this page's tables, leaving remote caches out of a backup, raising revision columns on restore | [backup-and-restore.md](backup-and-restore.md) (R-BAK-004, R-BAK-008, R-BAK-022) |

## Terms

| Term | Meaning on this page |
|---|---|
| Canonical path | The repository-relative path a format plug-in assigns to a request or stored file: `/`-separated, no leading slash, no empty, `.` or `..` segment, percent-decoded exactly once. npm example: document `@acme/util`, tarball `@acme/util/-/util-1.4.0.tgz`. |
| Request kind | `artifact` (immutable binary), `metadata` (expiring document), `search`, `write` or `other`, assigned by the format plug-in (R-REPO-003). |
| Package name | The format's package identifier, normalized by the format plug-in (npm: as published, scoped names as `@scope/name`; NuGet: the ASCII-lowercased ID for IDs entering local repositories (ASCII only); for cached upstream packages with legacy non-ASCII IDs, the ID segment of the client's request path ([formats/nuget.md](formats/nuget.md) R-NUGET-003)). |
| Routing name | A name the format plug-in derives for an artifact that belongs to no package, so that patterns, claims and pin rules can protect it (NuGet symbol files: the first segment of the symbol key without its last extension, lowercased). It is not a package name for anything else, authorization included (R-REPO-003). |
| Name pattern | A name glob of [R-AUTHZ-005](authorization.md), evaluated by the same matcher against the package name (or routing name) with the format's comparison rule there (npm case-sensitive, NuGet case-insensitive): `*` any run of characters except `/`, `**` any run including `/`, `?` one character except `/`, `\` makes the next character literal. An exact name and a prefix ending in `*` (`@acme/*`, `acme-*`, `Acme.*`), the syntax of NuGet's [package source mapping](https://learn.microsoft.com/en-us/nuget/consume-packages/package-source-mapping), are a subset of it and mean the same here, in permissions and in retention. Because `*` does not cross `/`, a bare `*` on npm matches every unscoped name and no scoped one; `@*/*` matches every scoped name and `**` every name. NuGet IDs contain no `/`, so `*` and `**` both match every ID. |
| Most specific wins | The routing precedence among claim and pin-rule patterns that match the same name (R-REPO-023); include and exclude lists need none (R-REPO-005). An exact name beats any pattern with a wildcard; a longer literal part beats a shorter one; with equal literal parts, a trailing `*` beats a trailing `**`. Claim and pin-rule patterns are therefore limited to the forms for which this order is total: an exact name, or a literal (possibly empty) followed by one trailing `*` or `**` (R-REPO-023). |
| Effective member list | A virtual repository's members after nested virtual repositories are expanded and duplicates removed (R-REPO-018). |
| Cache entry | What a remote repository stored for one canonical path and variant: an artifact, which is also an item of the repository ([storage.md](storage.md)), or a metadata document. |
| Fresh, expired, stale | A metadata entry is fresh until its expiry, expired afterwards; an expired entry served because the upstream was not or could not be asked is stale. Artifacts never expire. |
| Negative entry | A remembered upstream 404 or 410 for one canonical path and variant of a remote repository: a key in Valkey with a bounded copy in each replica's memory (R-REPO-028). It can make trove answer 404 and nothing else. |
| Flight | The fetch of one cache entry from the upstream. Concurrent requests for the same (repository, canonical path, variant) share one flight, within a replica and across replicas (R-REPO-026). The replica that fetches is the flight's holder; requests that wait for it are followers (same replica) or waiters (other replicas). |
| Cache-fill check | The check that guards contacting the upstream: `read` plus the action named by the remote repository's `cacheFillAction` ([R-AUTHZ-008](authorization.md)). |
| Upstream origin | Scheme, host and port of the remote repository's upstream URL ([RFC 6454](https://www.rfc-editor.org/rfc/rfc6454)). |
| Secret field | A configuration field holding a credential. It takes `{"value": "…"}` or `{"secretRef": {…}}` and is never returned (R-REPO-033). |

Error codes on this page (`invalid_key`, `upstream_unavailable`, …) are carried in the error
envelope of [api-and-ui.md](api-and-ui.md) on `/api/v1` endpoints and in the error body the format
page prescribes on package-protocol endpoints. Denials (401, 403, 404 for hidden resources) follow
[R-AUTHZ-016](authorization.md). Examples use `https://trove.example.com` as the public base URL
and `$TROVE_TOKEN` as a trove token (INV-8).

## Repository model

### R-REPO-001 · Repository classes, format and immutable identity
Priority: **MVP** · Covers: REPO-001, REPO-002, REPO-016

- Every repository MUST have exactly one class, `local`, `remote` or `virtual` (INV-5), and exactly
  one format from the format catalog (R-REPO-003). Class, format and key are fixed at creation, and the
  project changes only through a move (R-REPO-060). An update that changes any of them MUST be refused with 422 `validation_failed` with `immutable` in `errors[]`; there is no class
  conversion and no rename (rename is create, copy per [storage.md](storage.md), delete).
- A local repository stores what clients deploy. A remote repository stores what it fetched from its
  single upstream (R-REPO-024). A virtual repository stores no content and answers through its
  members (R-REPO-017, R-REPO-019).
- A deploy of content the format does not recognise as a package (for example a text file through
  `/files/` into an npm repository) MUST be passed to the format plug-in, which MAY refuse it with
  422 `not_a_package`. npm and NuGet refuse it: unindexed foreign content in a format repository is
  invisible to clients and only costs storage.
- Federated and release-set storage classes are not part of the model (see Deferred).

Data model. The base table `repositories (id, project_id, key, format, class, config jsonb, created_at,
updated_at)` is defined in [storage.md](storage.md) (R-STOR-003). This page owns the content of
`config` (R-REPO-004) except its `storage` member, and adds:

```sql
ALTER TABLE repositories
  ADD COLUMN lifecycle        text NOT NULL DEFAULT 'active'
                              CHECK (lifecycle IN ('active', 'deleting')),
  ADD COLUMN config_version   bigint NOT NULL DEFAULT 1,     -- ETag, R-REPO-011; R-BAK-022
  ADD COLUMN claims_version   bigint NOT NULL DEFAULT 1,     -- claims ETag, R-REPO-023; R-BAK-022
  ADD COLUMN created_by       uuid,                          -- principal id
  ADD COLUMN updated_by       uuid,
  ADD COLUMN last_activity_at timestamptz,                   -- R-REPO-012
  ADD CONSTRAINT repositories_key_dots CHECK (key !~ '\.\.' AND key !~ '\.$');
```

The M1 migration adds `lifecycle`, `config_version`, `created_by`, `updated_by`, `last_activity_at` and the key
constraint; `claims_version` comes with the migration of R-REPO-023 (Q-REPO-11). The repository's project is part of
the base table: `project_id` is required and the key is unique within the project ([storage.md](storage.md) R-STOR-003,
[authorization.md](authorization.md) R-AUTHZ-026).

`config` never contains secret values; they are in `repository_secrets` (R-REPO-033). References
that need referential integrity (members, deploy targets, pin targets, client certificates,
outbound proxies) are relational tables on this page.

`config_version` and `claims_version` are client-visible ETags, so both MUST be registered with
`meta.RegisterRevisionColumn`, and a restore raises them above every value the old installation
can have issued ([backup-and-restore.md](backup-and-restore.md) R-BAK-022). Otherwise a client
holding the ETag of a version written after the snapshot could pass `If-Match` against different
content after the restore, and a claims `PUT` could replace a claim set, `global` claims
included, that its author never saw (R-REPO-023).

Acceptance criteria:

- `POST /api/v1/projects/web/repositories` with `{"key":"npm-local","class":"local","format":"npm"}` returns
  201; a following `PATCH` with `{"format":"nuget"}` returns 422 `validation_failed` with `immutable` in `errors[]` at `/format`.
- A create with `"class":"federated"` returns 422 `validation_failed` with `unsupported_class` in `errors[]`.
- `curl -X PUT --data-binary @notes.txt -H "Authorization: Bearer $TROVE_TOKEN"
  https://trove.example.com/files/web/npm-local/notes.txt` returns 422 `not_a_package`.

### R-REPO-002 · Repository keys and client addresses
Priority: **MVP** · Covers: REPO-004, REPO-005

- Every repository belongs to exactly one project and is addressed inside it
  ([Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project),
  [Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared),
  [authorization.md](authorization.md) R-AUTHZ-026). A key MUST match `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$` (INV-5, 2 to 64
  characters: the component grammar of the OCI distribution specification, so a key is valid in every image reference,
  [Q-OCI-3](formats/oci.md#q-oci-3--repository-and-project-keys-follow-the-oci-component-grammar-for-every-format)).
  A key therefore starts and ends with a letter or digit and holds no `..`, no trailing `.` (trailing dots are
  stripped by some reverse proxies and Windows clients) and no mixed separator such as `.-`, `._` or `___`. Keys are unique within the project across
  all classes; two projects can each have a repository of the same key (`web/npm-remote`, `mobile/npm-remote`).
  Violations answer 422 `validation_failed` with `invalid_key` in `errors[]`; a key taken in the project answers 409
  `already_exists`.
- A key carries no project prefix: the project is part of the address, and no key or key prefix is reserved for a
  project.
- No suffix is reserved. The cache view of a remote repository is addressed with `~cache`
  (R-REPO-032); `~` cannot occur in a key, so the cache address never collides with a key and remote
  keys keep the full 64 characters. `~cache` belongs to the repository segment, never to the project segment.
- Reserved names: `batch-create`, `batch-update`, `batch-get`, `batch-delete` and `quick-setup`
  are literal sub-paths of `/api/v1/projects/<projectKey>/repositories` (R-REPO-014, R-REPO-015) and MUST be rejected
  as keys in every project with 422 `reserved_name` ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7). The
  OpenAPI key schema lists them as `x-trove-reserved-names`.
- Outside its project a repository is named by its qualified name `<projectKey>/<repoKey>` (`web/npm-internal`): in
  instance-wide lists, in the selectors of instance contexts ([authorization.md](authorization.md) R-AUTHZ-004), in audit
  events, log fields, metric labels and job targets. Inside its project (virtual members, deploy targets, project
  policies) it is named by its bare key.
- A key stays taken in its project while its repository is being deleted and becomes free when the deletion job
  completes (R-REPO-013); a move frees it in the source project in the move's transaction (R-REPO-060).
- Addresses of a repository (INV-6):

| Address | Purpose |
|---|---|
| `/api/<format>/<projectKey>/<repoKey>/…` | Package-manager protocol, e.g. `/api/npm/web/npm-all/`, `/api/nuget/web/nuget-all/v3/index.json` |
| `/api/<format>/<projectKey>/<repoKey>~cache/…`, `/files/<projectKey>/<repoKey>~cache/<path>` | Cache view of a remote repository (R-REPO-032) |
| `/files/<projectKey>/<repoKey>/<path>` | Raw file access ([storage.md](storage.md)) |
| `/api/v1/projects/<projectKey>/repositories/<repoKey>` | Configuration |

- One parser reads the project and the repository segment of every route that names a repository
  ([architecture.md](architecture.md) R-ARCH-005): each segment must match its key pattern, and a `%` in either is refused,
  so an escaped `/` can never shift a segment (as on the Bastion, [bastion.md](bastion.md) R-BAS-018). An address without
  the repository segment (`/api/npm/web`) answers 404, and so does an address whose project does not exist or is one the
  caller cannot see: a project answers exactly like a missing repository ([authorization.md](authorization.md)
  R-AUTHZ-016).
- A move to another project changes every address of the repository (R-REPO-060); a client configured with the old
  address gets 404.
- Recommended, not enforced: `<format>-<purpose>-<class>` for local and remote repositories (`npm-release-local`);
  virtual repositories drop the class part (`npm`). The team is the project and is not repeated in the key.

Acceptance criteria:

- Creating repositories with the keys `Npm`, `a`, `-x`, `a..b`, `x.` and `npm~cache` each returns
  422 `validation_failed` with `invalid_key` in `errors[]`; `npm-local` and `a1` are accepted.
- With `web/npm-remote` in place, creating `npm-remote` in project `mobile` returns 201, and creating it again in `web`
  returns 409 `already_exists`.
- Creating a repository with the key `batch-create` or `quick-setup` returns 422 `reserved_name`.
- With remote `npm-remote` in project `web`, `GET /api/npm/web/npm-remote~cache/lodash` reaches the cache view,
  and creating a repository named `npm-remote-cache` succeeds.
- `GET /api/npm/web/web`, `GET /api/npm/web/nope/npm-remote/lodash` (no project `nope`) and `GET /api/npm/web/web%2Fx/npm-remote/lodash`
  each answer 404.

### R-REPO-003 · Format catalog and request classification
Priority: **MVP** · Covers: REPO-006

- Each format plug-in ([architecture.md](architecture.md)) MUST register: name; supported classes;
  layout name (R-REPO-006); protocol base path; default upstream URL and the `extraHosts` entries
  it proposes for that upstream (R-REPO-024); the probe resource of the upstream test (R-REPO-024;
  npm: `-/ping`, empty for a format whose URL is the service index); default metadata and not-found cache periods; the
  subject of include and exclude patterns (`name` or `path`, R-REPO-005); whether it supports
  priority members (R-REPO-022), pin rules and namespace claims (R-REPO-023), and repository caps
  (R-REPO-007); its package-name normalization; for formats with package names, the
  `packageOwnership` its local repositories hold while their document sets none
  ([authorization.md](authorization.md) R-AUTHZ-018, Q-AUTHZ-2, Q-AUTHZ-155: `enforce` for npm and
  NuGet); and, for formats with package names, the
  canonical path of a package's package document, the document whose existence upstream stands for
  the package's existence (npm: the package document `n`; NuGet: the version list
  `{lower_id}/index.json`). R-REPO-029 and R-REPO-048 use that path. A format whose upstream has no document per package
  (an APT archive lists its packages in repository-wide indexes) reports no package document, and its remote adapter then
  names a `NameServer` that answers R-REPO-048 from the rows of the indexes the remote holds, in the scope of the virtual
  repository whose exposure the check decides ([Q-REPO-270](#q-repo-270--a-format-without-a-package-document-answers-the-shadowing-check-from-its-index-rows-in-the-scope-of-the-virtual-repository), [formats/debian.md](formats/debian.md) R-DEB-023).
- The MVP formats are `npm` and `nuget`, each with all three classes (INV-14).
- For every package-protocol and `/files/` request the plug-in MUST produce a request descriptor:
  canonical path; request kind; normalized package name and version where the request concerns a
  package, or a routing name the format derives for an artifact that belongs to no package (NuGet
  symbol files: the first segment of the symbol key without its last extension, lowercased;
  [formats/nuget.md](formats/nuget.md) R-NUGET-015, Q-NUGET-7); a variant for content-negotiated
  metadata (npm's abbreviated document, for example); for artifacts, the expected digests when the
  format's metadata announces them (R-REPO-041).
- Patterns, claims and pin rules see the routing name; authorization does not. No other rule
  treats a routing name as a package name: it creates no package rows and is not subject to gone
  packages (R-REPO-029) or the shadowing check (R-REPO-048).
- A path the plug-in cannot classify MUST be treated as `metadata`. Misclassifying a binary as
  metadata costs a refetch after the metadata period; misclassifying metadata as immutable freezes
  package lists for good.

API:

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/formats` | 200, list of format descriptors | any caller; anonymous where anonymous access is on |

```json
{
  "formats": [
    {
      "name": "npm",
      "classes": ["local", "remote", "virtual"],
      "layout": "npm",
      "protocolBasePath": "/api/npm/{project}/{key}/",
      "defaultUpstreamUrl": "https://registry.npmjs.org/",
      "defaultMetadataTtlSeconds": 7200,
      "defaultNotFoundTtlSeconds": 1800,
      "patternSubject": "name",
      "priorityMembers": true,
      "pinsAndClaims": true,
      "caps": false
    }
  ]
}
```

Acceptance criteria:

- `curl -s https://trove.example.com/api/v1/formats` lists `npm` and `nuget`, each with the
  classes `local`, `remote` and `virtual`.
- Creating a repository with `"format":"cobol"` returns 422 `validation_failed` with `unknown_format` in `errors[]`.

### R-REPO-004 · Configuration document and common settings
Priority: **MVP** · Covers: REPO-010, REPO-016, REPO-017, REPO-028, RMT-030

A repository is one JSON document. It is the request body of create (R-REPO-009), the response of
read (R-REPO-010) and the object body of configuration as code
([R-OPS-029](operations-and-observability.md)).

| Member | Meaning | Default (proposed) | Owner |
|---|---|---|---|
| `key`, `class`, `format` | R-REPO-001, R-REPO-002 | — (required) | this page |
| `description` | Public description, shown to everyone who can read the repository | `""` (≤ 2000 characters) | this page |
| `adminNotes` | Notes shown only to holders of `repositories:read-config` | `""` (≤ 8000 characters) | this page |
| `includePatterns`, `excludePatterns` | R-REPO-005 | `[]` (everything), `[]` | this page |
| `disabled` | Takes no part in resolution, download or deploy. Setting it records `repository.disabled`, journaled, so a restore keeps it set ([authorization.md](authorization.md) R-AUTHZ-031, Q-AUTHZ-238) | `false` | this page |
| `requireAuthentication` | Anonymous requests are challenged even where anonymous read is granted | `false` | this page |
| `priorityMember` | R-REPO-022; local and remote only | `false` | this page |
| `hideExistence` | `inherit`, `true`, `false` | `inherit` | [R-AUTHZ-017](authorization.md) |
| `layout` | R-REPO-006, read-only | the format's layout | this page |
| `project` | The key of the repository's project, taken from the address of the create request (R-REPO-009); read-only, changed only by a move (R-REPO-060) | — (every repository is in one project) | [R-AUTHZ-026](authorization.md) |
| `local` | `packageOwnership`, `ownershipDomain` ([R-AUTHZ-018](authorization.md), [R-AUTHZ-020](authorization.md)); `autoClaim` (R-REPO-023); `caps` (R-REPO-007). An omitted `packageOwnership` is the format's default, stored with the document; `ownershipDomain` answers `not_applicable` until R-AUTHZ-020 (P1) is built (Q-AUTHZ-155) | see owners | mixed |
| `remote` | R-REPO-024; `cacheFillAction` within it is [R-AUTHZ-008](authorization.md) | — | this page |
| `virtual` | R-REPO-017 | — | this page |
| `storage` | Upload size, quota, presigned downloads, client checksum policy | see owner | [storage.md](storage.md) |
| `vulnerabilities` | From M4b: scanning and download blocking of a local or remote repository of a format with the vulnerability adapter (`enabled`, `matchPublicAdvisories`, `blockMalicious`, `onMissingData`; no severity threshold, Q-VULN-20); `not_applicable` on virtual repositories and other formats | see owner | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-002 |
| `npm`, `nuget`, … | Format settings, under a member named after the format. The service validates it against the JSON Schema the format registers (`GET /api/v1/formats` serves it as `settingsSchema`), reports a violation as 422 `validation_failed` at `/<format>/<member>` (`invalid_value`), and answers `not_applicable` for a setting of another class and for the member of another format (Q-ARCH-229, Q-REPO-122). Built: `npm`; `nuget` joins with its format | see owner | format page |

- A class section that does not match the class (for example `virtual` on a local repository)
  answers 422 `validation_failed` with `not_applicable` in `errors[]`.
- Pin rules, namespace claims, extra hosts and upstream credentials are generic members of this
  page (`virtual.pinning`, the claims API, `remote.extraHosts`, `remote.authMode`,
  `remote.username`, `remote.password`) for every format (Q-REPO-5). A format section MUST NOT
  define members for these mechanisms. A format page adds semantics (npm: which account name the
  public registry expects with `basic`), proposes defaults through the format catalog (NuGet:
  `*.nuget.org` as an extra host without credentials) and MAY narrow the generic rules, for
  example forbid `sendCredentials: true`; it never widens them. Format settings live under a
  member named after the format (`npm`, `nuget`), never under a generic name.
- `disabled: true` MUST make every package-protocol and `/files/` request to the repository answer
  404 `repository_disabled`, MUST remove it from virtual resolution (R-REPO-019) and MUST leave the
  management API working. The repository list shows it as inactive (R-REPO-012).
- `requireAuthentication: true` MUST answer an anonymous request with 401 and the challenge of
  [R-AUTHZ-016](authorization.md), even when the anonymous principal holds `read`. It is offered
  for every format and class, not only the catalog's NuGet and Maven cases.

Acceptance criteria:

- With `disabled: true` on `npm-local`, `npm view @acme/util --registry
  https://trove.example.com/api/npm/web/npm-local/` fails with `E404`, while the same command against
  the virtual repository `npm-all` still succeeds when another member holds the package.
- With anonymous read granted and `requireAuthentication: true` on `npm-local`, an unauthenticated
  `curl -i https://trove.example.com/api/npm/web/npm-local/@acme%2futil` returns 401 with a `Basic`
  challenge.
- `GET /api/v1/projects/web/repositories/npm-local` by a caller without `repositories:read-config` contains no
  `adminNotes`.

### R-REPO-005 · Include and exclude patterns
Priority: **MVP** · Covers: REPO-011, RMT-034

- A path or package is admitted when it matches at least one include pattern (an empty include list
  admits everything) and no exclude pattern.
- The subject of the patterns is declared by the format (R-REPO-003):
  - `name` (npm, NuGet): patterns are name patterns (see Terms) matched against the normalized
    package name of the request. A routing name the format derives for a package-less artifact
    (R-REPO-003; NuGet symbol files) counts as the package name here. Only requests whose
    descriptor carries neither a package name nor a routing name (service index, search, ping)
    are admitted as package-less. One pattern therefore covers a package's documents and files
    alike, which avoids the trap of path patterns that admit the tarball but not the document.
  - Name patterns are matched by the matcher of [R-AUTHZ-005](authorization.md), the same code
    that evaluates permission and retention patterns ([storage.md](storage.md) Q-STOR-9), so a
    pattern copied between them selects the same names. On npm, `*` does not cross `/`: a bare
    `*` covers unscoped names only, `@*/*` covers every scoped name and `**` every name. A list
    that contains `*` on an npm repository (a format whose name rule takes scoped names,
    [Q-REPO-109](#q-repo-109--pattern_unscoped_only-is-decided-by-the-formats-name-rule)) but neither `**` nor `@*/*` returns the warning
    `pattern_unscoped_only` (R-REPO-008), because `excludePatterns: ["*"]` would otherwise look
    like "exclude everything" while scoped names, usually the internal ones, still reach the
    upstream. Include and exclude lists have no precedence among their patterns; most specific
    wins applies only to claims and pin rules (R-REPO-023).
  - `path` (generic and path-based formats, when they arrive): the path patterns of
    [R-AUTHZ-005](authorization.md), evaluated by the same matcher against the canonical path
    (Ant-style, [Apache Ant](https://ant.apache.org/manual/dirtasks.html#patterns)): `?` one
    character except `/`, `*` within one segment, `**` any number of segments, `\` escapes.
- Patterns MUST apply to: resolution of downloads; virtual membership (a member is consulted only
  for admitted requests); upstream fetching (a remote repository never asks its upstream for
  anything it does not admit, also when addressed directly); deploys to a local repository
  (refused with 403 `not_admitted`); copy and move targets ([storage.md](storage.md)).
- Patterns MUST apply to every caller on package-protocol and `/files/` requests, administrators
  included: they are routing rules, and an administrator's build must not fetch an internal name
  from a public upstream either. Under `/files/` and in the items API the name patterns of a format
  with package names are matched against the package the format gives the path; a path it places in
  no package is package-less and admitted ([authorization.md](authorization.md) Q-AUTHZ-160). The
  management item API shows items outside the patterns (stored before a pattern change, for example)
  so they can be removed; its delete removes them, and deletes in a disabled repository, for a holder
  of `content:bypass` only, and applies the patterns and the disabled flag as `/files/` does for every
  other caller (Q-AUTHZ-162).
- Changing patterns MUST NOT delete stored items.
- Limits: at most 100 patterns per list, each at most 1024 characters; violations answer 422
  `invalid_pattern`.
- A change that narrows a list (an exclude pattern added, an include pattern taken out of a list that keeps restricting, a
  first include pattern) records `repository.narrowed`, so a restore keeps it narrowed (R-REPO-061).

Acceptance criteria:

- Remote `npm-remote` with `excludePatterns: ["@acme/*"]`: `npm view @acme/util --registry
  https://trove.example.com/api/npm/web/npm-remote/` fails with `E404`, and a counting stub upstream
  records no request.
- Local `npm-local` with `includePatterns: ["@acme/*"]`: `npm publish` of `left-pad` fails with 403
  `not_admitted`; publishing `@acme/left-pad` succeeds.
- Saving `excludePatterns: ["*"]` on `npm-remote` returns the warning `pattern_unscoped_only`;
  afterwards `npm view lodash` through it fails with `E404` and the stub records no request, while
  `npm view @partner/sdk` reaches the stub. With `["**"]` both fail and the stub records no request.
- A unit test runs the R-AUTHZ-005 matcher's test table through the routing code path and gets the
  same results.

### R-REPO-006 · Layouts (minimal)
Priority: **MVP** · Covers: REPO-012, REPO-046, REPO-047

- Each format has exactly one built-in layout, named like the format (`npm`, `nuget`). A layout is
  code in the format plug-in, not configuration: it defines the canonical path scheme and derives
  module information (package name, version, file role) from a canonical path.
- The `layout` member is read-only and equals the format's layout. A create that names another
  layout answers 422 `validation_failed` with `unsupported_layout` in `errors[]`.
- Module information MUST be recorded on items (the package and version rows of
  [storage.md](storage.md)), because the package view, cleanup and latest-version resolution need
  it.
- Later formats bring their own built-in layouts (a Maven 2 layout with the Maven family, a flat
  layout with the generic format). User-defined layouts, pattern tokens, integration-revision
  expressions and path translation between layouts are deferred (REPO-048 to REPO-052).

Acceptance criteria:

- `GET /api/v1/projects/web/repositories/nuget-local` returns `"layout":"nuget"`; a create with
  `"layout":"maven-2"` for an npm repository returns 422 `validation_failed` with `unsupported_layout` in `errors[]`.
- After `npm publish` of `@acme/util@1.4.0`, the tarball item belongs to package `@acme/util`,
  version `1.4.0`.

### R-REPO-007 · Repository caps
Priority: **P1** · Covers: REPO-025, REPO-027

The caps are settings here; [R-STOR-044](storage.md) enforces them after each push, and the format
plug-in's version-group hook decides what a group is.

| Member (`local.caps.…`) | Meaning | Default (proposed) | Constraint |
|---|---|---|---|
| `maxVariantsPerGroup` | Distinct snapshot or pre-release variants kept per version group (format-defined) | `0` (no cap) | `0` or ≥ 2 |
| `maxTagsPerImage` | Distinct tag names kept per image (container formats) | `0` (no cap) | ≥ 0 |
| `keptTagRevisions` | Earlier revisions kept per tag name when a tag is overwritten (container formats) | `0` | ≥ 0 |

- All three caps are counts, never ages (RET-035): distinct variants per version group, distinct
  tag names per image, kept revisions per tag name. Federated repositories are not part of the
  MVP (INV-5); when they arrive, their caps use the same count semantics, not an age in days.
- When a cap is exceeded, the oldest entries beyond it are removed ([R-STOR-044](storage.md)).
  "Oldest" is the earliest creation of the entry's current version: a variant's deploy time, and
  for a tag the time the tag name last received a push, so a re-pushed tag counts as new and a tag
  that keeps receiving pushes is never the oldest. No tag name is exempt, `latest` included.
- Removals by caps go to the trash can when it is enabled ([R-STOR-022](storage.md)), for
  snapshots and tags alike, and are audited like any delete. When a tag is removed, its kept
  revisions go with it.
- A cap the format does not support answers 422 `validation_failed` with `not_applicable` in `errors[]`.
- A variant cap of 1 is refused with 422 `out_of_range`: an in-flight multi-file deploy would remove
  its own siblings.
- The container API version and the legacy manifest switch of REPO-027 belong to the container
  format page.

Acceptance criteria:

- `PATCH` with `{"local":{"caps":{"maxVariantsPerGroup":1}}}` returns 422 `out_of_range`; with `5`
  it is stored and read back.
- On an npm repository, `maxTagsPerImage: 3` returns 422 `validation_failed` with `not_applicable` in `errors[]`.
- With the container format and `maxTagsPerImage: 3` on `docker-local`, pushing `latest`, `t1`,
  `t2`, re-pushing `latest`, then pushing `t3` removes `t1`, not `latest`, and `t1` is in the trash
  can ([R-STOR-044](storage.md)).

## Repository management API

All operations below are part of the OpenAPI document at `/api/v1/openapi.json` (INV-6). Every
change writes an audit event (INV-16; actor, action, repository, outcome; secrets as fingerprints)
in the same transaction as the change and calls `cluster.Notify("repo_config", …)` with the
repository id in that transaction ([architecture.md](architecture.md) R-ARCH-018). Replicas MUST
apply a change within 2 seconds of commit (INV-12), with or without Valkey: the PostgreSQL change
log brings it within `cluster.epoch_poll_interval`, and Valkey's message within milliseconds.

Two in-memory structures depend on the area: every replica's repository snapshot (R-REPO-049) and
the grant index of [R-AUTHZ-028](authorization.md), which is built from each repository's key,
class, format, project, virtual membership, deploy target and authorization settings. The writes
that change an input of the index MUST therefore call `cluster.Notify("repo_config", …)` in their
own transaction, wherever they run:

- every repository create, including batch create, quick setup and configuration-as-code apply
  (R-REPO-009, R-REPO-014, R-REPO-015); key, class and format change only through create and
  delete (R-REPO-001);
- every repository delete: the first transaction, which sets `lifecycle = 'deleting'` and with
  `detach=true` removes the repository from member lists, pin rules and deploy targets, and the
  job transaction that removes the row (R-REPO-013);
- every change of the virtual membership (R-REPO-017, R-REPO-018), the deploy target (R-REPO-021),
  the project (a move, R-REPO-060) or one of the authorization
  settings `cacheFillAction`, `packageOwnership`, `hideExistence` and `requireAuthentication`
  ([R-AUTHZ-031](authorization.md)).

The payload names the repository id and the `config_version` the write left, never a grant:
`{"id", "key", "op", "version"}`, where a create writes version 1 and an update and the mark for
deletion raise it. A cache keyed by the version drops only the entries of older versions
([formats/nuget.md](formats/nuget.md) Q-NUGET-94). R-AUTHZ-028's completeness test runs every
write operation of this page's API against a test database and fails when an input of the index
changed in a transaction that advanced neither the `authz` nor the `repo_config` epoch. Without
the notification, a replica would keep deciding on the old member list or the old
`cacheFillAction` until the index's `authz.cache.safety_ttl` rebuild.

Capability and action names are those of [authorization.md](authorization.md) (R-AUTHZ-010): `repositories:read-config`
(roles `admin`, `security-admin`, `auditor`, `repository-admin`), `repositories:write-config` (roles `admin`,
`repository-admin`), `settings:read` and `settings:write`, repository actions `read`, `deploy`,
`delete`, `manage`. Within its project, the
role `owner` allows what `repositories:read-config` and `repositories:write-config` allow, with the restrictions of
[authorization.md](authorization.md) R-AUTHZ-026 for an Owner without the capability; every other role reads a
repository's consumer view as far as its `read` reaches.

### R-REPO-008 · Configuration validation
Priority: **MVP** · Covers: REPO-053, REPO-054, RMT-002

The server MUST validate every create and update against all rules below in one pass and report
every violation (JSON pointer, code, message) in one 422 response. Malformed JSON and unknown
members answer 400 `malformed_request`: a configuration API that silently drops a misspelt setting
leaves the operator believing it is in effect.

| Rule | Code |
|---|---|
| Key syntax (R-REPO-002) | `invalid_key` |
| An Owner's `secretRef` other than the stored one (an Owner without `repositories:write-config`), or a stored credential kept while its hosts change (R-REPO-033, Q-REPO-127) | `secret_value_required`, also the top-level code |
| An Owner's `outboundProxy.mode` (an Owner without `repositories:write-config`) other than `default` or `sourceNetwork`, unless stored and kept with the url and the extra hosts (Q-REPO-128) | `outbound_path_restricted`, also the top-level code |
| Key is a reserved name (R-REPO-002) | `reserved_name` |
| Key taken in the project, also by a repository being deleted | `already_exists` (409) |
| Class or format unknown, or the format does not support the class | `unsupported_class`, `unknown_format` |
| Class, format or key changed on update | `immutable` |
| Pattern syntax or limits (R-REPO-005); claim or pin-rule pattern outside the forms R-REPO-023 admits | `invalid_pattern` |
| Member missing from the virtual repository's project, written with a `/`, being deleted, of another format, duplicated, or the virtual repository itself | `invalid_member` |
| Nesting cycle or depth over the limit (R-REPO-018) | `virtual_cycle`, `virtual_too_deep` |
| Deploy target not a local repository in the effective member list (R-REPO-021), or written with a `/` | `invalid_deploy_target` |
| Pin rule member not in the effective member list (R-REPO-023) | `invalid_pin_member` |
| Upstream URL not absolute `http`/`https`, or carrying user info or a fragment (R-REPO-024) | `invalid_upstream_url` |
| Numeric setting outside its range (R-REPO-024 and others) | `out_of_range` |
| `remote.sourceNetwork` not a CIDR prefix (R-REPO-024) | `invalid_source_network` |
| Referenced client certificate or outbound proxy missing | `unknown_reference` |
| Secret field with both `value` and `secretRef`, or a `secretRef` outside the rules of R-OPS-029 | `invalid_secret` |
| Custom header name or value invalid (R-REPO-036) | `invalid_header` |
| Member not applicable to class or format | `not_applicable` |

Warnings do not block a save; they are returned in a `warnings` list, for example
`dependency_confusion_unprotected` (R-REPO-023), `claim_overlaps` (R-REPO-023),
`pattern_unscoped_only` (R-REPO-005), `plain_http_upstream` or `source_network_single_address`
(R-REPO-024).

Acceptance criteria:

- A create of a virtual npm repository whose members are a NuGet repository and a missing key
  returns one 422 response with two `invalid_member` entries.
- A create with `"remote":{"url":"https://user:pw@registry.example.com/"}` returns 422
  `invalid_upstream_url`.
- A create with the misspelt member `"excludePattern"` returns 400 `malformed_request`.

### R-REPO-009 · Create a repository
Priority: **MVP** · Covers: REPO-053, RMT-002, API-052

| | |
|---|---|
| Request | `POST /api/v1/projects/{projectKey}/repositories`, body: the configuration document; omitted settings take their defaults, format defaults included |
| Success | 201, `Location: /api/v1/projects/<projectKey>/repositories/<key>`, `ETag: "<config_version>"`, body: the stored document as R-REPO-010 returns it, plus `warnings` |
| Errors | 400 `malformed_request`; 401; 403; 404 for a project that does not exist or that the caller cannot see; 409 `already_exists` (the key is taken in the project); 422 (R-REPO-008) |
| Permission | `repositories:write-config`, or the role `owner` in the project ([R-AUTHZ-026](authorization.md), Q-AUTHZ-173) |

- Create is never an upsert. Configuration, relational rows, sealed secrets and the audit event are
  written in one transaction.
- A format's data port may add rows in that transaction: the repository service runs the creation hook the composition root
  registered for the format, after the repository row and before the audit event, and its error rolls the creation back (a Debian
  local or virtual repository gets its first signing key this way, [formats/debian.md](formats/debian.md) R-DEB-015,
  [Q-REPO-273](#q-repo-273--a-formats-data-port-adds-rows-in-the-transaction-that-creates-its-repository)).
- The repository belongs to the project of the path from the transaction that creates it; a `project` member in the
  body that names another project answers 422 `validation_failed` with `immutable` at `/project`. A virtual repository's
  members and deploy target are bare keys of the same project (R-REPO-017, R-REPO-021).
- Responses are JSON, not plain-text confirmations.

```json
{
  "key": "nuget-remote",
  "class": "remote",
  "format": "nuget",
  "description": "Proxy of nuget.org",
  "remote": {
    "url": "https://api.nuget.org/v3/index.json",
    "authMode": "basic",
    "username": "proxy",
    "password": { "secretRef": { "file": "nuget-upstream-token" } },
    "cacheFillAction": "read"
  }
}
```

Acceptance criteria:

- `curl -sS -X POST -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json'
  --data @npm-remote.json https://trove.example.com/api/v1/projects/web/repositories` returns 201, and a `GET`
  of the `Location` shows `metadataTtlSeconds: 7200` and `notFoundTtlSeconds: 1800` from the npm
  format defaults.
- Repeating the call returns 409 `already_exists`; the same call against `/api/v1/projects/mobile/repositories` returns
  201: `mobile/npm-remote` is another repository.
- The same call with a token lacking `repositories:write-config`, whose user is no Owner of `web`, returns 403 and
  creates nothing; against a project that does not exist it returns 404.

### R-REPO-010 · Read a repository configuration
Priority: **MVP** · Covers: REPO-055, REPO-056, API-057, API-058

| | |
|---|---|
| Request | `GET /api/v1/projects/{projectKey}/repositories/{repoKey}` |
| Success | 200, `ETag: "<config_version>"`, body: the full document or the consumer view |
| Errors | 401; 404 per [R-AUTHZ-016](authorization.md) |
| Permission | `repositories:read-config`, or the role `owner` in the repository's project: full document; `read` on the repository (by a grant or a role): consumer view (Q-REPO-20) |

- The document contains only members that apply to the repository's class and format; the
  catalog's format-filtered read is therefore the only read.
- Secret fields are never returned: an inline secret reads as `{"set": true}`, a reference reads back
  as the `secretRef` it is (a file name or variable name is not secret).
- The consumer view contains `key`, `project`, `class`, `format`, `description`, `layout` and the client
  addresses, as [R-AUTHZ-016](authorization.md) defines it. It omits upstream settings, notes, pins,
  claims and members: an internal registry host or another team's repository key is information
  too.

Acceptance criteria:

- With `repositories:read-config`, `GET /api/v1/projects/web/repositories/nuget-remote` returns `remote.url` and
  `"password": {"secretRef": {"file": "nuget-upstream-token"}}`; for an inline secret it returns
  `{"set": true}` and never the value.
- A user holding only `read` on `nuget-remote` receives the consumer view without a `remote`
  member; a user holding nothing on it receives 404.

### R-REPO-011 · Update a repository configuration
Priority: **MVP** · Covers: REPO-054, RMT-002, API-054

| | |
|---|---|
| Request | `PATCH /api/v1/projects/{projectKey}/repositories/{repoKey}` with `Content-Type: application/merge-patch+json` ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)); or `PUT /api/v1/projects/{projectKey}/repositories/{repoKey}` with a complete document (omitted settings revert to defaults). `If-Match: "<config_version>"` required (428 without it; `If-Match: *` for a deliberate blind write). |
| Success | 200, new `ETag`, body: the stored document plus `warnings` |
| Errors | 400; 401; 403; 404; 409 managed by configuration as code ([R-OPS-029](operations-and-observability.md)); 412 `precondition_failed` with `currentEtag` ([api-and-ui.md](api-and-ui.md) R-API-009); 422 (R-REPO-008); 428 `precondition_required` |
| Permission | as create (R-REPO-009); the authorization members (`cacheFillAction`, `packageOwnership`, `ownershipDomain`, `hideExistence`) per [R-AUTHZ-012](authorization.md) |

- Merge-patch semantics: arrays (members, patterns, headers, pin rules) are replaced as a whole;
  `null` removes an optional setting; an omitted secret field stays unchanged; a secret field set to
  `null` clears it.
- `If-Match` is required on `PATCH` and `PUT` ([api-and-ui.md](api-and-ui.md) R-API-012,
  Q-API-2). Without it the update MUST be refused with 428 `precondition_required` and nothing
  changes; a stale version MUST be refused with 412 `precondition_failed`; `If-Match: *` passes
  for a repository that exists and is the explicit blind write. The arrays above are replaced as a
  whole, so a stale write of a virtual repository's `members` could put a remote member ahead of
  the local one again, a dependency-confusion path (R-REPO-023). The UI sends the `ETag` it loaded
  and never `If-Match: *` ([api-and-ui.md](api-and-ui.md) R-UI-042), so two administrators editing
  the same repository cannot overwrite each other unnoticed.
- Requests, examples and acceptance criteria on this page that `PATCH` or `PUT` a repository send
  the `If-Match` the caller read, unless they say otherwise.
- A change of `remote.url`, `extraHosts`, `authMode`, `username`, `password`, `clientCertificate`
  or `headers` MUST invalidate all metadata and negative entries of the repository (scope `all`,
  path empty, R-REPO-045): what the old upstream said is no evidence about the new one. Cached
  artifacts are kept.
- `PUT` never creates a repository; a missing key answers 404.

Acceptance criteria:

- `PATCH` with `{"remote":{"cache":{"metadataTtlSeconds":600}}}` changes only that setting; the
  next `GET` shows it and a new `ETag`.
- Two `PATCH` calls with the same `If-Match` value: the first returns 200, the second 412 with
  `currentEtag` set to the first call's new `ETag`.
- `curl -X PATCH -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/merge-patch+json' -d '{"description":"npm proxy"}' https://trove.example.com/api/v1/projects/web/repositories/npm-remote`
  without `If-Match` returns 428 `precondition_required` and the next `GET` shows the old
  `ETag`; the same call with `-H 'If-Match: *'` returns 200.
- After a change of `remote.url`, the next `npm view lodash` through the repository causes an
  upstream request although the cached document was fresh.

### R-REPO-012 · List repositories
Priority: **MVP** · Covers: REPO-007, REPO-057, REPO-058, API-060, API-061

| | |
|---|---|
| Request | `GET /api/v1/repositories?class=&format=&q=&state=&project=&view=&limit=&cursor=` (every project); `GET /api/v1/projects/{projectKey}/repositories?class=&format=&q=&state=&view=&limit=&cursor=` (one project) |
| Success | 200, `{"items": [...], "nextCursor": "…"}` (paging per [api-and-ui.md](api-and-ui.md) R-API-010, Q-API-6) |
| Errors | 400 invalid filter; 401 |
| Permission | any caller, anonymous included; the result contains only repositories on which the caller holds `read` by a grant or a role ([R-AUTHZ-016](authorization.md)), every repository, those being deleted included, for a holder of `repositories:read-config` (Q-REPO-19), and every repository of its project for an Owner; `view=full` requires `repositories:read-config`, or on the project's list the role `owner`. The project's list answers 404 for a project the caller cannot see, exactly as for one that does not exist: a caller sees a project when it holds `projects:read` or `repositories:read-config`, is a member of it, or reads one of its repositories whatever the filters and the cursor ([authorization.md](authorization.md) R-AUTHZ-016) |

- Filters: `class` and `format` take comma-separated lists; `q` matches key and description
  case-insensitively; `project` filters by project key; `state=inactive` returns disabled
  repositories, offline remote repositories and remote repositories whose circuit is open
  (R-REPO-029).
- `view=summary` (default) returns per repository: key, project, class, format, description, client address,
  `disabled`, `lastActivityAt`, project, and for remote repositories the availability (`online`,
  `offline`, `assumed-offline`). Holders of `repositories:read-config` also get the upstream URL.
- `view=full` returns complete documents (the catalog's "all configurations").
- Class and format values are spelled as in configuration documents (lower case).
- The list is sorted by project key, then key, in byte order (`ORDER BY p.key COLLATE "C", r.key COLLATE "C"`),
  whatever the collation the database was created with, because the cursor is the last (project, key) pair of a page
  compared byte by byte: under
  `en_US.UTF-8` the hyphen weighs nothing at the first level, so `weba` sorted before `web-b` and the
  page after `weba` skipped `web-b` (`TestR_REPO_012_ListPagesEveryKeyOnceInByteOrder`). The key column
  itself carries the database's collation ([H-404](#h-404--the-repository-key-column-carries-the-databases-collation)).
- `lastActivityAt` is the latest upload to or download from the repository. Each replica MUST
  aggregate activity in memory and write it at most once per minute per repository, so downloads do
  not turn into database writes. No request reads that buffer; it is flushed at shutdown
  ([architecture.md](architecture.md) R-ARCH-024 step 6), and a crash loses at most one minute of
  activity of that replica (R-REPO-049).

Acceptance criteria:

- `curl -s -H "Authorization: Bearer $TROVE_TOKEN"
  'https://trove.example.com/api/v1/repositories?class=remote&format=npm'` returns only npm remote
  repositories, each with an availability state.
- An anonymous caller, with anonymous read granted on `web/npm-all` only, sees exactly `web/npm-all`.
- `GET /api/v1/projects/web/repositories` lists the repositories of `web` only; with `npm-remote` in `web` and in
  `mobile`, the instance-wide list returns both, each with its project.
- `view=full` without `repositories:read-config` returns 403.

### R-REPO-013 · Delete a repository
Priority: **MVP** · Covers: REPO-063, RMT-002, API-063

| | |
|---|---|
| Request | `DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}?detach=true\|false` |
| Success | 204 for a virtual repository (no content); 202 with a job reference for local and remote repositories; repeated while the job runs: 202 with the same job |
| Errors | 401; 403; 404; 409 `repository_referenced` naming the referencing virtual repositories, when `detach` is false; 409 managed by configuration as code |
| Permission | as create (R-REPO-009) |

- Only virtual repositories of the same project can list a repository (R-REPO-017), so `detach` edits virtual
  repositories of the project and of no other, whoever deletes.

- The first transaction MUST set `lifecycle = 'deleting'`. From then on the repository is not
  resolvable, not addressable by clients, absent from virtual resolution and listed only to holders
  of `repositories:read-config`. With `detach=true`, the same transaction removes it from every
  member list, pin rule and deploy-target reference.
- A background job ([architecture.md](architecture.md)) removes items permanently, bypassing the
  trash can ([storage.md](storage.md), R-STOR-018), and removes cache entries, URL mappings
  and invalidations in batches. The claims of the repository stay in effect until the row goes and
  are removed with it (the foreign key cascades): a `global` claim keeps protecting its names while the
  job runs, which a batch-wise removal would end early (Q-REPO-106). The transaction that removes the
  row of a repository that held claims calls `cluster.Notify("claims", …)` with the repository id
  ([architecture.md](architecture.md) R-ARCH-018), so every replica drops them from its in-memory trie
  within 2 seconds (R-REPO-023); without that, a `global` claim of the deleted repository would keep
  refusing its names on every replica until that replica restarts. The repository's negative entries in Valkey are left to expire: their
  keys carry the repository id, which an identity column never hands out again (R-REPO-028). The job
  runs on the `worker` role and is resumable after a replica restart. The repository row goes last,
  which frees the key; that transaction calls `cluster.Notify("repo_config", …)`, because the row
  is an input of the grant index (Repository management API above), and removes the package
  ownership rows of the repository's ownership domain, which carry no foreign key to the row
  ([authorization.md](authorization.md) R-AUTHZ-018, Q-AUTHZ-156).
- Job progress (items total and removed, errors) is read from the job resource.
- Both `repository.deleted` events (phase `started` and `completed`) carry the revocation journal's
  entry: a restore to a point before the deletion brings the repository back disabled, not deleted
  ([authorization.md](authorization.md) R-AUTHZ-031, Q-AUTHZ-239).

Acceptance criteria:

- Deleting `npm-local` while `npm-all` lists it returns 409 naming `npm-all`; with `detach=true` it
  returns 202, `npm-all` no longer lists it, and `npm view @acme/util --registry
  …/api/npm/web/npm-local/` returns `E404` at once.
- Deleting `web/npm-local` leaves `mobile/npm-local` and the virtual repositories of `mobile` untouched.
- After the job completes, a new repository named `npm-local` can be created in `web` and is empty.
- Killing the `worker` replica that runs the job does not prevent completion: another `worker`
  replica resumes it.

### R-REPO-060 · Move a repository to another project
Priority: **MVP** · Covers: trove-only

Every repository belongs to exactly one project, and a move to another project stays possible and changes the repository's
address ([Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project),
[Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)). The move is an
operation of its own ([Q-REPO-217](#q-repo-217--a-repository-moves-to-another-project-through-an-operation-of-its-own-and-only-while-nothing-of-its-project-names-it)):

| | |
|---|---|
| Request | `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/move`, body `{"project": "<target project key>"}`; no `If-Match`: the path names the source |
| Success | 200, `Location: /api/v1/projects/<target>/repositories/<repoKey>`, the new `ETag`, `Trove-Authz-Epoch`; body: the document as R-REPO-010 returns it at the new address |
| Errors | 400 `malformed_request`; 401; 403; 404 for a source repository or a target project the caller cannot see; 409 `repository_in_use` naming the objects of the source project that name the repository in the problem member `references`, `[{"kind": "virtual_member", "name": "npm-all"}]`; 409 `already_exists` when the target project has the key, also for a repository being deleted there; 409 managed by configuration as code ([R-OPS-029](operations-and-observability.md)) |
| Permission | `repositories:write-config`, or the role `owner` in both the source and the target project ([authorization.md](authorization.md) R-AUTHZ-026); recent authentication ([api-and-ui.md](api-and-ui.md) R-API-007) |

- **Only while nothing of the source project names it.** The move is refused with 409 `repository_in_use` while a virtual
  repository of the source project lists the repository as a member or as its deployment target (R-REPO-017, R-REPO-021),
  a key selector of a policy of the source project names it ([authorization.md](authorization.md) R-AUTHZ-004), an inline
  grant of a CI mapping of the source project's tier names it ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-013), or its
  `ownershipDomain` names a shared ownership domain of the source project ([authorization.md](authorization.md)
  R-AUTHZ-020, P1). A virtual repository moves only without members and deployment target, which are repositories of the
  source project. The answer lists each referencing object by kind and name, as far as the caller may read it; the caller
  removes the references and moves again. Nothing is rewritten on the caller's behalf. The kinds of `references` are
  `virtual_member`, `pin_rule` and `virtual_deployment_target` (a virtual repository of the source project names it), `policy`
  (a key selector of the source project's policy, by its name within the project), `ci_mapping` (an inline grant of a mapping of
  the source project's tier names its key), and for a virtual repository that moves `member` and `deployment_target` (its own,
  which keep it from moving).
- **A global grant does not ride along with an Owner's move.** A caller without `repositories:write-config`, an Owner of both
  projects, is refused with 409 `repository_in_use` as well while a key selector of a global policy names the repository: the
  reference has the kind `global_policy` and the name of the global policy. That grant is held by the repository's ID and would
  follow it into the target project, whose other Owners can neither see nor remove it; an administrator of trove moves the
  repository, or removes the selector, and the Owner moves it then
  ([Q-REPO-232](#q-repo-232--an-owners-move-is-refused-while-a-global-policy-names-the-repository-by-key)). A pattern or class
  selector of a global policy matches by address and refuses no move.
- **Serialised with the writers that name it.** A policy write binds its key selectors under share locks of its project and
  of the repository ([authorization.md](authorization.md) R-AUTHZ-004), and a virtual repository's write its members under
  share locks, so such a write and a move run one after the other and the move sees what the write named. The database
  holds the rule for every writer: a repository that a key selector of a policy of its project or a virtual repository of
  its project holds by ID does not change its project (SQLSTATE `AI007`; a move that meets it answers 409
  `repository_in_use`). That is the second line: the same-project guard on the member tables of a virtual repository
  locks the member repository and the virtual repository `FOR SHARE` before it reads their projects, and the guard on the
  key selectors of project policies locks the selected repository the same way, so a member or selector write that skips
  the service's locks still meets a move in either order, and the move or the write is refused
  ([Q-REPO-234](#q-repo-234--a-member-write-locks-its-repositories-in-the-database-so-the-move-guard-sees-every-order)).
- **One transaction.** The move takes the row locks of both projects in key order and of the repository, changes the
  repository's `project_id`, raises its `config_version`, writes the audit events `repository.moved` (project: the source,
  repository: the former name, journaled) and `repository.moved_in` (project: the target, repository: the new name), both
  with the source and the target project, so the audit of each project shows the move
  ([Q-REPO-233](#q-repo-233--a-move-is-recorded-in-the-audit-of-both-projects)), and calls `cluster.Notify("authz", …)` and `cluster.Notify("repo_config", …)` with the repository id
  ([architecture.md](architecture.md) R-ARCH-018), as the change of an input of the grant index (Repository management
  API above). A target project that holds the key, also a repository being deleted there, answers 409 `already_exists` and
  nothing changes. A target equal to the source changes nothing and answers 200 without an event
  ([authorization.md](authorization.md) Q-AUTHZ-193). A repository being deleted is not addressable and answers 404.
- **What moves with it.** Items, packages, cached entries, claims, package owners, findings, mirror state and running jobs
  are held by the repository's ID and stay as they are; no content is copied. A pass of a mirror in progress, or queued with the old name in its arguments, goes on: it
  resolves its repository by the ID at its start and before every package ([R-REPO-053](#r-repo-053--mirror-passes-as-jobs-on-the-queue-mirror)),
  and the name is for logs, audit events and metrics only. The key becomes free in the source project
  and is taken in the target project.
- **What changes.** Every address of the repository changes (R-REPO-002): the old address answers 404 from the move's
  epoch on, on every replica within the bound of R-AUTHZ-028. The roles and policies of the source project stop reaching
  it, those of the target project reach it. An object outside the project that holds the repository by its ID (a key
  selector of a global policy, an exposure rule of [endpoints.md](endpoints.md) R-EP-012) keeps holding it and reads back
  the new qualified name; what matches by address (a key pattern or class selector, a host-root binding of
  [endpoints.md](endpoints.md) R-EP-006, a client's registry URL) matches the new address or no longer matches.
- **Narrowing.** A move is journaled as a narrowing ([authorization.md](authorization.md) R-AUTHZ-031) and reaches the
  Bastion's feed ([bastion.md](bastion.md) R-BAS-015).

Acceptance criteria:

- An administrator creates the global policy `libs-global` with the key selector `web/npm-libs`; the move of `web/npm-libs` to
  `mobile` by an Owner of both projects answers 409 `repository_in_use` with `references: [{"kind": "global_policy", "name":
  "libs-global"}]`, the same move by the administrator answers 200, and with the selector replaced by `web/npm-*` the Owner's
  move answers 200.
- An Owner of `web` and of `mobile` moves `web/npm-libs` to `mobile` (200). `npm view @acme/util --registry
  https://trove.example.com/api/npm/web/npm-libs/` then fails with `E404` on every replica, the same command against
  `https://trove.example.com/api/npm/mobile/npm-libs/` shows the versions published before the move, and no blob is
  written. A member of `web` with the role `developer` and no role in `mobile` gets `E404` at the new address.
- While the virtual repository `npm-all` of `web` lists `npm-libs`, the move answers 409 `repository_in_use` naming
  `npm-all` and changes nothing; after the member is removed it succeeds.
- With `mobile/npm-libs` in place the move answers 409 `already_exists`.
- An Owner of `web` with the role `developer` in `mobile` gets 403; one that is no member of `mobile` gets 404.
- A global policy with the key selector `web/npm-libs` reads back `mobile/npm-libs` after the move and keeps granting.

### R-REPO-061 · Narrowings of a repository in the revocation journal
Priority: **MVP** · Covers: trove-only

A restore to a point in time *t* returns every repository to its configuration and its claims at *t* ([backup-and-restore.md](backup-and-restore.md)
R-BAK-024, [ha-and-helm.md](ha-and-helm.md) R-HA-025). The changes of this page that narrow what a repository serves after *t*
are therefore copied into the revocation journal of [ha-and-helm.md](ha-and-helm.md) R-HA-054 and re-applied by every restore, as
a disable and the authorization settings are ([authorization.md](authorization.md) R-AUTHZ-031). Replay only narrows: where
re-applying a part to the restored repository could widen what it serves, the repository is disabled instead (`disabled: true`,
R-REPO-004) and the entry is reported `suspended`.

| Audit event, flagged `journal: revocation` | Recorded | Entry carries |
|---|---|---|
| `repository.narrowed` | By an update (R-REPO-011) whose change narrows one of the parts below, beside its `repository.updated`, in the same transaction; an update that narrows nothing records none ([Q-REPO-245](#q-repo-245--a-narrowing-update-records-repositorynarrowed-beside-repositoryupdated-with-only-the-parts-that-narrowed)) | `repositoryId` (decimal text, as `repository.disabled` carries it), `key` (the qualified name, for display), and each part that narrowed, only when it did: `include`, `excludeAdded`, `autoClaim`, `membersRemoved`, `pinsAdded`, `pinMembersRemoved`, `vulnerabilities` |
| `repository.claims_changed` | By every change of a repository's claims (R-REPO-023): a claims write of the API and the auto claim of a first publish; every event carries its entry ([Q-REPO-250](#q-repo-250--every-claims-change-carries-a-journal-entry-and-a-replayed-manual-claim-that-overlaps-another-repositorys-claim-disables-its-repository)) | `repositoryId`, `key`, `source` (`manual`, `auto`) and `added`: the claims created and those widened from `members` to `global`, each `{pattern, scope}`; a removal and a narrowing to `members` widen and are not part of it. The `added` list of the event's details keeps listing every claim whose scope changed, a narrowing included, as R-OPS-016 has it |

**What narrows.** The update compares the stored document and the rows of its virtual section with the new ones, in the
transaction that writes them (the comparison that fills `changes`):

| Part | Narrows when | Entry |
|---|---|---|
| `includePatterns` (R-REPO-005) | the new list is not empty, and the old one is empty or holds a pattern that no pattern of the new one provably covers (the same pattern in the folded form of the format's name rule, or a literal with one trailing `*` or `**` that takes in the other); an emptied list admits everything, and a list that only widens or changes case never narrows | `include`: the new list |
| `excludePatterns` | the new list holds a pattern the old one lacks | `excludeAdded`: those patterns |
| `local.autoClaim` (R-REPO-023) | its effective value (an absent member is the format's default) goes from false to true | `autoClaim: true` |
| `virtual.members` (R-REPO-017) | a member of the old list is not in the new one, compared by repository ID | `membersRemoved`: `[{repositoryId, key}]` |
| `virtual.pinning` (R-REPO-023) | a pattern that no rule held is held by a rule, or a pattern that a rule held before and after loses members | `pinsAdded`: `[{pattern, members: [{repositoryId, key}]}]`; `pinMembersRemoved`: the same shape, with the members the pattern lost |
| `vulnerabilities` ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-002) | a member moves towards more blocking, in the order of Q-VULN-69: `enabled` to true, `matchPublicAdvisories` to a higher rank, `blockMalicious` to true, `onMissingData` to `block` | `vulnerabilities`: the member before and after, an absent member as its defaults |

The other members are not journaled ([Q-REPO-246](#q-repo-246--patterns-auto-claims-virtual-members-pin-rules-and-the-vulnerabilities-member-are-journaled-upstream-storage-and-format-settings-are-not)):
`description`, `adminNotes`, `priorityMember`, `virtual.deployTarget`, `virtual.mergedMetadataTtlSeconds`, the `remote` section
apart from `cacheFillAction`, `storage`, `local.caps`, `local.ownershipDomain` and the format members. A restore reverts them; the
security-relevant ones are [H-569](../security/backup-and-restore.md#h-569--upstream-storage-and-format-settings-of-a-repository-come-back-with-a-restore).
`disabled`, `requireAuthentication`, `hideExistence`, `local.packageOwnership` and `remote.cacheFillAction` keep the events of
R-AUTHZ-031. A widening part of an update that also narrows (a pattern added to an include list, a member added) is not part of
the entry.

**Replay of `repository.narrowed`** ([ha-and-helm.md](ha-and-helm.md) R-HA-054), in one transaction, on the repository of the
entry's ID as the restored database holds it, locked. A repository that is missing or being deleted is `subject unknown`; a part
that does not fit the repository's class (members of a repository that is not virtual) is left out. Every part is applied; a
part that fails closed disables the repository, and the other parts are still applied, so the repository is narrowed when an
administrator enables it again.

1. `include`: a restored list that is empty takes the journaled list; one that is not keeps only the patterns that the journaled
   list holds as well or covers (a literal with one trailing wildcard that takes in the restored pattern), compared in the folded
   form of the format's name rule, so a consolidating edit narrows to the pattern inside. When none is left, the repository is disabled ([Q-REPO-247](#q-repo-247--a-replayed-include-list-keeps-the-patterns-both-lists-hold-and-none-left-disables-the-repository)).
2. `excludeAdded`: each pattern the restored list lacks is added. A list that would hold more than 100 patterns disables the
   repository.
3. `autoClaim`: set to `true`.
4. `membersRemoved`: each member is taken out of the member list, out of every pin rule and, when it is the deploy target, out of
   the deploy target. When a removed member is a local repository that holds a claim of scope `members` and any other member of
   the restored repository stays (one the entry does not remove), the virtual repository is disabled instead of losing that
   member: the claim would leave effect in it, and the names it claims would reach the member that stays, a remote one or a local
   one that does not hold them ([Q-REPO-248](#q-repo-248--a-replayed-member-removal-that-would-take-a-members-claim-out-of-effect-beside-another-member-disables-the-virtual-repository)).
5. `pinMembersRemoved`: the members are taken out of the rule that holds the pattern; a rule left without members answers 404 for
   its names. When no rule holds the pattern, the repository is disabled.
6. `pinsAdded`: when a rule holds the pattern, its members are reduced to those the entry names. Otherwise a rule with the pattern
   and those of the entry's members that are members of the restored repository is added, unless a claim in effect in the
   repository or a pattern of another rule of the restored repository overlaps the pattern (the rules this entry adds beside it are
   not counted: any rule selects a subset of the members a name reached before); then the repository is disabled ([Q-REPO-249](#q-repo-249--a-replayed-pin-rule-is-added-only-where-no-claim-and-no-other-rule-overlaps-it)).
7. `vulnerabilities`: each member takes the more blocking of its restored and its journaled after-value. When `enabled` or
   `matchPublicAdvisories` moved, a `vuln-rescan` of the repository is enqueued in the same transaction
   ([ha-and-helm.md](ha-and-helm.md) [Q-HA-180](ha-and-helm.md#q-ha-180--a-replay-handler-starts-follow-up-work-by-writing-a-job-row-in-its-own-transaction)).
   A repository without the member (virtual, or of a format without the vulnerability adapter) leaves the part out.

A pattern that steps 1, 2 and 6 write is checked as an update checks it (the format's pattern rules, R-REPO-008); an entry with a
pattern that fails the check, with an ID that is no ID, with a list longer than an update can write (100 patterns, the members a
virtual repository can hold, the pin patterns it can hold) or with no part at all is skipped as malformed, before anything is written. A changed repository raises `config_version` and is told to the change
areas `repo_config` and `authz`, as the replay of a setting is; the replay writes no audit event. The outcome is `suspended` when a
part disabled the repository, `applied` when a part changed it, and `already in effect` otherwise; a repository that is disabled
already stays as it is and a part that fails closed there reports nothing, so a second replay finds every entry `already in effect`.
The detail names the parts and, for a suspension, prints the entry, so an administrator can review it and re-apply what it says.

**Replay of `repository.claims_changed`**, on the local repository of the entry's ID (a repository of another class or of a
format without claims is `subject unknown`), for each added claim:

- the restored repository holds the pattern with the journaled scope, or with `global` for a journaled `members`: nothing;
- it holds the pattern with `members` and the entry says `global`: the scope is widened to `global`;
- an auto claim (`source: auto`) is one exact name of scope `members`, else the entry is skipped as malformed; it is created as a
  first publish creates it, unless a claim of another repository that is in effect together with it covers the name (the name
  then keeps resolving from that repository);
- a manual claim is created with its scope. When a claim of another repository that is in effect together with it overlaps the
  pattern (either claim is `global`, or the two repositories are members of one virtual repository: the condition of
  `claim_overlaps`), the claim is created and the claiming repository is disabled, because the claim could take names from the
  other repository (Q-REPO-250).

The limit of 1000 claims does not stop a replayed claim: a claim only narrows, and a dropped one leaves a name unclaimed. A
repository that ends above the limit is listed in the replay output as a warning.

An entry without its `added` member is skipped as malformed. A created claim has no creator, `claims_version` rises,
and the change area `claims` is told. The outcome is `suspended` when the repository was disabled, `applied` when a claim was
created or widened, and `already in effect` otherwise.

Acceptance criteria (project `web`: `npm-internal` local with `local.autoClaim: true`, `npmjs` remote of the public registry,
`npm-all` virtual over both with `npm-internal` first; a trove backup is taken at *t*, and each criterion restores it):

- After *t*, `excludePatterns: ["@evil/*"]` is set on `npmjs`. After the restore, `npm view @evil/pkg` through `npm-all` fails
  with `E404` without an upstream request, and the replay output lists `repository.narrowed web/npmjs … applied`.
- After *t*, `@acme/new-lib` is published to `npm-internal` for the first time, and the test upstream then serves a package of
  that name. After the restore the version is gone, `npm view @acme/new-lib` through `npm-all` fails with `E404`, the test
  upstream records no request for the name, and the output lists `repository.claims_changed web/npm-internal … applied`.
- After *t*, `npm-internal` is removed from `npm-all`. After the restore, `npm-all` is disabled and the entry is `suspended`,
  because `npm-internal` holds claims of scope `members` and `npm-all` keeps `npmjs`.
- After *t*, `vulnerabilities.blockMalicious` is set to `true` on `npmjs`, which was `false` at *t*. After the restore the member
  reads `true`.
- Hand-written entries: a `repository.narrowed` whose `include` shares no pattern with the restored list disables the
  repository (`suspended`); a manual claim that overlaps a claim of another member of `npm-all` is created and its repository is
  disabled; no entry adds a member, a pattern to an include list or a member to a pin rule.

### R-REPO-014 · Batch operations
Priority: **P1** · Covers: REPO-059, REPO-061, REPO-062, REPO-064, API-053, API-055, API-059, API-064

| Request | Semantics | Success | Permission |
|---|---|---|---|
| `POST /api/v1/projects/{projectKey}/repositories/batch-create` `{"repositories": [doc, …]}` | All or nothing, one transaction; members may reference repositories created in the same batch | 201 with the stored documents | as create |
| `POST /api/v1/projects/{projectKey}/repositories/batch-update` `{"updates": [{"key", "ifMatch", "patch"}, …]}` | All or nothing, one transaction, merge patch per entry; `ifMatch` required per entry | 200 with the stored documents | as update |
| `POST /api/v1/projects/{projectKey}/repositories/batch-get` `{"keys": [...]}` | R-REPO-010 per key | 200 `{"repositories": {"<key>": doc}, "missing": [...]}` | as read, per key |
| `POST /api/v1/projects/{projectKey}/repositories/batch-delete` `{"keys": [...], "detach": false}` | Best effort per key, in request order | 202 with one result per key (job reference or error) | as delete |

- The actions are sub-path verbs ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7); their names
  are reserved repository keys (R-REPO-002). All of them accept `Idempotency-Key`
  ([api-and-ui.md](api-and-ui.md) R-API-014).
- Every `batch-update` entry MUST carry `ifMatch` ([api-and-ui.md](api-and-ui.md) R-API-012): the
  `config_version` the caller read, or `*` to overwrite whatever is stored, so a blind write is an
  explicit choice. An entry without it fails the batch with 428 `precondition_required` naming its
  index; a stale value fails it with 412.
- The batch size is limited by `repositories.batch_limit`; larger batches answer 400
  `batch_too_large`. A failing create or update entry answers 422 (or 404, 412, 428) naming the
  index of every failing entry.
- Keys are bare keys of the path's project; members may name repositories of that project only (R-REPO-017).
- A key the caller may not see is reported under `missing`, like a key that does not exist.

Configuration:

```yaml
repositories:
  batch_limit: 100  # default (proposed)
```

Acceptance criteria:

- A `batch-create` of `npm-local`, `npm-remote` and a virtual `npm-all` over both succeeds in one
  call; the same call with an invalid third entry creates nothing.
- A `batch-get` of 101 keys returns 400 `batch_too_large`.
- A `batch-update` of two entries, the second without `ifMatch`, returns 428 naming index 1 and
  changes nothing; with `"ifMatch": "*"` on the second entry it succeeds.

### R-REPO-015 · Quick setup of a repository set
Priority: **P1** · Covers: REPO-008

| | |
|---|---|
| Request | `POST /api/v1/projects/{projectKey}/repositories/quick-setup` `{"prefix": "billing", "formats": ["npm", "nuget"]}` |
| Success | 201 with the created documents |
| Errors | 409 when any generated key exists in the project (nothing created); 422 invalid prefix or format |
| Permission | as create |

- Per format, one transaction creates, in the path's project, `<prefix>-<format>-local`, `<prefix>-<format>-remote`
  (upstream: the format's default upstream URL) and the virtual `<prefix>-<format>` with members
  local then remote, the local repository as deploy target, and `autoClaim` on for the local
  repository (R-REPO-023).
- The prefix MUST be a valid key fragment of at most 20 characters.

Acceptance criteria:

- Quick setup in project `web` with prefix `billing` and format `npm` creates `billing-npm-local`,
  `billing-npm-remote` and `billing-npm`; `npm install lodash --registry
  https://trove.example.com/api/npm/web/billing-npm/` succeeds, and `npm publish` to the same registry
  lands in `billing-npm-local`.

### R-REPO-016 · Recalculate a repository's index
Priority: **P1** · Covers: REPO-009

| | |
|---|---|
| Request | `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/reindex` |
| Success | 202 with a job reference |
| Errors | 404; 405 for formats or classes without derived metadata; 409 `reindex_running` naming the queued or running job |
| Permission | `repositories:write-config`, or `manage` on the repository |

- The format plug-in rebuilds the repository's derived metadata from its items. npm and NuGet
  metadata (npm package documents, NuGet registrations and version lists) is rendered per request
  from package rows ([architecture.md](architecture.md) R-ARCH-012), so there are no stored
  documents to rebuild: for them a reindex rebuilds the package and version rows from the
  repository's items, incrementing `rev_seq` on every package row it changes, and the next request
  renders from the rebuilt rows. A format with a job-built repository-wide index (the exception
  of R-ARCH-012) also rebuilds that index. On a virtual repository the operation drops its
  merged-metadata cache (R-REPO-020) on every replica.
- **The drop travels through a change area.** The transaction that records a virtual
  repository's reindex calls `cluster.Notify("cache_invalidation", …)` with the virtual
  repository's id ([architecture.md](architecture.md) R-ARCH-018). A replica that applies a
  `cache_invalidation` change naming a virtual repository drops that repository's merged entries.
  The drop therefore reaches every replica at its next change-log poll, also one that missed the
  Valkey message and while Valkey is unavailable. The signal area `metadata_changed` of R-REPO-020
  is not enough here: it is delivered at most once, and not at all without Valkey.
  [formats/npm.md](formats/npm.md) R-NPM-035 relies on this.
- A reindex is an `exclusive` job of [architecture.md](architecture.md) R-ARCH-016 with the key
  `index:<repository id>`: while one is queued or running, a new request answers 409
  `reindex_running` naming it. Automatic rebuilds of job-built indexes (R-ARCH-012) are `coalesce`
  jobs on the same key, so a manual reindex and an automatic rebuild of one repository never run
  at the same time. Neither takes an advisory-lock lease: leases carry registered numbers only
  (R-ARCH-017). Clients are served the previous metadata until the rebuilt metadata commits.

Acceptance criteria:

- After the package and version rows of `@acme/util` in `npm-local` are deleted in a test
  database, `npm view @acme/util` fails; after a reindex job completes, it succeeds with all
  versions.
- A second `POST …/repositories/npm-local/reindex` while the first job is queued or running
  returns 409 `reindex_running` naming that job.
- With Valkey stopped, the merged document of `lodash` is cached on replica B, and a reindex of
  `npm-all` is requested on replica A: after the job completes, the next `npm view lodash` through
  `npm-all` on B, sent `cluster.epoch_poll_interval` plus 1 s later, is merged again
  (`trove_cache_requests_total{cache="merged_metadata",result="miss"}` increments on B).

## Virtual repositories

### R-REPO-017 · Virtual repository membership
Priority: **MVP** · Covers: REPO-033

| Member (`virtual.…`) | Meaning | Default (proposed) | Constraint |
|---|---|---|---|
| `members` | Ordered member keys: bare keys of the virtual repository's project | `[]` (warning `no_members`) | ≤ `repositories.max_virtual_members`; same format; no duplicates |
| `deployTarget` | R-REPO-021 | `null` | local repository in the effective member list |
| `mergedMetadataTtlSeconds` | R-REPO-020 | `7200` | 0–86400 |
| `pinning` | R-REPO-023 | `{"rules": []}` | R-REPO-023 |
| `effectiveMembers` | R-REPO-018, read-only | — | — |

- Members and the deploy target are repositories of the virtual repository's own project, named by their bare keys (a
  project context, [authorization.md](authorization.md) R-AUTHZ-004): a key that names no repository of the project, or
  a value with a `/`, answers 422 `validation_failed` with `invalid_member` (`invalid_deploy_target`). No repository is
  shared across projects ([Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project), [Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)): a project that needs a public
  registry has a remote repository of its own, and the blob store keeps a package fetched by two projects once (INV-4).
- Members MUST be of the virtual repository's format. In the MVP they are local or remote
  repositories; virtual members arrive with R-REPO-018 and are refused with 422 `validation_failed` with `invalid_member` in `errors[]`
  until then.
- Members and their order can change without touching client configuration; a change takes effect
  on all replicas within 2 seconds and drops the repository's merged-metadata cache (R-REPO-020).
  A member taken out records `repository.narrowed`, and a restore takes it out again or, where its claims would leave effect
  beside another member, disables the virtual repository (R-REPO-061).
- Recommended practice, shown in the UI: clients use a virtual repository; local members come first.
- The members, the deploy target and the pin rules are rows (the data model below), not part of `repositories.config`: a
  read renders them into `virtual`, an update replaces them in the transaction that raises `config_version`, and the grant
  index reads the member lists and the deploy targets from the same rows ([R-AUTHZ-009](authorization.md)). A member is
  checked in the update's transaction under a share lock, so it cannot be marked for deletion between the check and the
  commit ([Q-REPO-104](#q-repo-104--members-deploy-target-and-pin-rules-are-rows-and-the-document-is-rendered-from-them)).

Data model:

```sql
CREATE TABLE virtual_members (
  virtual_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  position   integer NOT NULL CHECK (position >= 0),
  member_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
  PRIMARY KEY (virtual_id, position),
  UNIQUE (virtual_id, member_id),
  CHECK (virtual_id <> member_id)
);
CREATE INDEX virtual_members_member ON virtual_members (member_id);

CREATE TABLE virtual_deploy_targets (
  virtual_id bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  target_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT
);
```

Configuration:

```yaml
repositories:
  max_virtual_members: 100 # default (proposed)
```

Acceptance criteria:

- A virtual `npm-all` with members `npm-local`, `npm-remote` serves `npm install lodash --registry
  https://trove.example.com/api/npm/web/npm-all/`.
- With two replicas, a `PATCH` that reverses the member order is visible in the resolution
  explanation (R-REPO-019) of both replicas within 2 seconds, also with Valkey stopped.

### R-REPO-018 · Nested virtual repositories
Priority: **P1** · Covers: REPO-034

- A virtual repository MAY list virtual repositories of the same format as members.
- The effective member list MUST be computed by depth-first expansion in member order, keeping only
  the first occurrence of each repository. It is recomputed whenever a repository on the way
  changes and is returned read-only as `virtual.effectiveMembers`, each entry with the chain of
  virtual repositories it was reached through.
- Nesting depth is limited by `repositories.max_virtual_depth`; deeper compositions answer 422
  `virtual_too_deep`. A cycle MUST be refused at save time with 422 `virtual_cycle`. Membership
  changes MUST be serialized with the transaction-scoped advisory lock `pg_advisory_xact_lock(4, 1)`
  (category `config`, object type 1 = virtual membership; [architecture.md](architecture.md)
  R-ARCH-017), so two concurrent updates cannot form a cycle that neither transaction sees.
- A nested virtual repository keeps its rules for its subtree: a request reaches a member only if
  every virtual repository on the member's chain admits it (R-REPO-005) and the caller passes each
  one's gate ([R-AUTHZ-009](authorization.md)); each virtual repository's pin rules restrict its own
  subtree (R-REPO-023). Deploy targets and merged-metadata periods of nested virtual repositories
  are ignored; those of the requested virtual repository apply.

Configuration:

```yaml
repositories:
  max_virtual_depth: 5 # default (proposed)
```

Acceptance criteria:

- `npm-all` contains `npm-team` (virtual over `npm-team-local`) and `npm-remote`;
  `effectiveMembers` lists `npm-team-local` then `npm-remote`, with `npm-team` in the chain of the
  first.
- Updating `npm-team` to include `npm-all` returns 422 `virtual_cycle`; two concurrent updates that
  together would form a cycle leave exactly one committed.

### R-REPO-019 · Virtual resolution algorithm
Priority: **MVP** · Covers: REPO-035, REPO-036

For a request to virtual repository V with descriptor d (R-REPO-003) by caller c, the server MUST:

1. Apply the gate of [R-AUTHZ-009](authorization.md): c holds `read` on V; an anonymous caller
   additionally needs `read` on every member, otherwise 401. Answer 404 if V is disabled or V's
   patterns do not admit d.
2. Take V's effective member list in order; drop members that are disabled, being deleted, or whose
   patterns (or those of a virtual repository on their chain) do not admit d.
3. Apply pin rules and namespace claims (R-REPO-023). They depend on the package name and on stored
   configuration only, never on c's permissions. A name the format's name rule cannot fold is no name of the
   format: V admits nothing under it, no remote member is consulted or served from its cache, and a remote
   repository addressed directly refuses it (Q-REPO-123).
4. Drop members on which c does not hold `read` for d. Unreadable members are skipped silently; they
   never turn the answer into 403 ([R-AUTHZ-009](authorization.md)).
5. Order the remaining members stably: priority members first (R-REPO-022, P1), else list order.
6. Resolve by kind:
   - `artifact`, in three tiers, first hit wins: (a) local members; (b) remote members' caches,
     without contacting any upstream; (c) remote members' upstreams through the remote request path
     (R-REPO-025), only for members on which c passes the cache-fill check. A stored item that the
     member's format withholds (a version that was unpublished and is stored and tombstoned, a
     version a remote document stopped listing) is a miss of its tier, not the answer: the walk
     goes on to the next member that has the file (Q-REPO-117). A latest-version request
     (format-defined: a request whose answer is "the newest version") is the exception: all members,
     upstreams included, are consulted and the newest version wins (REPO-036).
   - `metadata`: collect the document from every remaining member that knows the package (remote
     members through R-REPO-025, which refreshes expired entries for callers that pass the
     cache-fill check) and merge them (R-REPO-020).
   - `search` and `other`: format page.
   - `write`: route to the deploy target (R-REPO-021).
7. When nothing was found: answer 404 if every consulted member answered "not found"; otherwise
   answer with the failure of the first member that failed transiently (502, 503 or 504 per
   R-REPO-029), because "not found" would be a false statement about an item that may exist.
   A member that the caller may not populate and whose cache lacks the item is not consulted and
   counts as "not found"; a failure after the first byte of an artifact ends the request, because
   another member cannot continue it (Q-REPO-111). A remote member whose cache holds the item with
   its object lost has failed transiently with `503 content_unavailable`, whether or not the caller
   may populate it: the walk goes on, and that is the answer when nothing serves the item (Q-REPO-205).

- Artifact responses MUST carry `Trove-Resolved-From: <member key>`; the member is one the caller
  can read, so the header discloses nothing new. On an endpoint with exposure rules the header is
  never sent, because members are not exposed there ([endpoints.md](endpoints.md) R-EP-018).
- **Resolution explanation.** `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/resolution?path=<client path>`
  evaluates steps 1 to 6 for the calling principal without transferring content and without
  contacting upstreams, and returns per member the decision and its reason (`disabled`,
  `not_admitted`, `pinned-out`, `claimed-elsewhere`, `refused-claimed`, `no-read`, `no-cache-fill`,
  `hit`, `miss`, `negative`, `offline`, `circuit-open`). Members the caller cannot read appear only
  as a count (`"unreadableMembers": 2`) unless the caller holds `repositories:read-config`.
  Permission: the gate of step 1. The members are listed in the order of the member list; the
  decision of a member that routing keeps is `hit` or `miss` for a local member and, for a remote
  member, `hit` when its cache holds the path, else `no-cache-fill` when the caller may not
  populate it, else `negative`, `offline`, `circuit-open` or `miss` as its state says
  (Q-REPO-114). `refused-claimed` is a remote member that refuses the name because of a global
  claim (R-REPO-023). `decidedBy` names the pattern of the claim (`claim`) or of the pin rule
  (`pin`) that narrowed the members; its `claimedBy` lists only the claimants the caller can read,
  unless the caller holds `repositories:read-config`. When the virtual repository is disabled or
  its patterns do not admit the path, every member shows `disabled` or `not_admitted`.

```json
{
  "repository": "npm-all",
  "descriptor": { "path": "@acme/util/-/util-1.4.0.tgz", "kind": "artifact", "package": "@acme/util" },
  "decidedBy": { "claim": "@acme/*", "claimedBy": ["npm-local"] },
  "members": [
    { "key": "npm-local", "class": "local", "decision": "hit" },
    { "key": "npm-remote", "class": "remote", "decision": "pinned-out" }
  ],
  "unreadableMembers": 0
}
```

Acceptance criteria:

- `left-pad@1.0.0` exists in `npm-local` and in the stub upstream of `npm-remote`; members are
  ordered remote first: `npm install left-pad@1.0.0 --registry …/api/npm/web/npm-all/` downloads the
  local tarball (`Trove-Resolved-From: npm-local`), because local members form the first tier.
- A caller with `read` on `npm-all` and `npm-remote` only receives the upstream package and no error
  from `npm-local`.
- On `npm-remote` with `cacheFillAction: deploy`, a caller with `read` but without `deploy` gets 404
  for a tarball not yet cached, and the explanation shows `no-cache-fill` for `npm-remote`.
- With the stub upstream down and nothing cached, the request returns 502, not 404.

### R-REPO-020 · Merged metadata and its cache
Priority: **MVP** · Covers: REPO-037

- For metadata requests, a virtual repository MUST answer with a document merged from the members of
  R-REPO-019 step 6. Merge rules per document type are on the format pages
  ([formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md)).
- For every version, the merged document MUST take the version entry from the member that tier
  resolution would pick for that version's artifact, so the digest a client reads and the bytes it
  downloads come from the same member.
- Merged documents MAY be cached per replica in memory (registered as the cache `merged_metadata`
  of [architecture.md](architecture.md) R-ARCH-018), and only there: a merged document is
  served to clients, so it is never stored in or read from Valkey
  ([architecture.md](architecture.md) R-ARCH-030, Q-ARCH-16). The cache key MUST include the virtual
  repository, the canonical path, the variant, the endpoint of the request
  ([endpoints.md](endpoints.md) R-EP-018) and a SHA-256 fingerprint of the endpoint and of the
  ordered list of members that took part after permission filtering, each with its class and
  whether the caller may fill its cache ([R-AUTHZ-008](authorization.md); Q-REPO-110): two callers
  who see different members, or who may fill different remote members, never share an entry
  ([R-AUTHZ-009](authorization.md) rule 4); callers who see the same members with the same rights do.
  A caller who may not fill a remote member gets that member's document restricted to the versions
  whose artifacts are cached (Q-REPO-113), which is why the right is part of the key. A path that
  belongs to no package, a period of 0 and `proxy.merged_metadata_cache_bytes: 0` keep nothing.
- A merged entry expires at the earlier of `mergedMetadataTtlSeconds` after merging and the earliest
  expiry of the contributing remote entries, so staleness does not add up across the two layers.
- **Local members: `rev_seq` check.** Every merged entry MUST record the `rev_seq` of every
  eligible local member, recorded as 0 when the member does not hold the package, and every request
  that finds the entry MUST compare them with the current values in one `RevSeqs` query; any
  difference makes the entry a miss ([architecture.md](architecture.md) R-ARCH-015, Q-ARCH-9). The
  check sits in the core's merged-metadata cache, not in a format's merge, and is a MUST for every
  format. A version published to, deleted from or restored into a local member is therefore
  visible through the virtual repository on every replica as soon as its commit has returned,
  whatever the period and whether or not a notification has arrived. The 0 for an absent package
  covers the first local publish of a name that so far came only from a remote member.
- **Early eviction.** A change of a member's metadata (publish, delete, restore, remote refresh
  with new content) also signals the area `metadata_changed` with repository id and package-name
  hash, which drops the affected merged entries on all replicas. The format sends it, from the
  hook that stored the change, because only the format knows the package name; the proxy engine
  sends none of its own, and a fill that stored nothing sends none (Q-REPO-116). It is a signal area of
  [architecture.md](architecture.md) R-ARCH-018: the signal leaves through the replica's post-commit
  coalescer to Valkey Pub/Sub (Q-ARCH-8), at most once, and not at all while Valkey is unavailable.
  It only evicts early. Visibility of local changes never depends on it (the `rev_seq` check above),
  and a remote refresh made on another replica reaches a merged entry at the latest when that entry
  expires, which is never later than the contributing remote entries. Claim changes reach the merged
  entries through their change area `claims` (R-REPO-023), whose bound does not depend on Valkey.
- A remote member that fails transiently (502, 503 or 504 of the remote request path) and has no
  stored copy is left out of the merge; the result MUST NOT be cached, and the response carries
  `Cache-Status: trove; detail=partial` (R-REPO-047). A member that answers "not found", or that
  the caller may not populate and that has nothing stored, is a miss and does not make the merge
  partial. If no member contributes, the answer is 404 when every consulted member said "not found"
  and the failure of the first member that failed otherwise (R-REPO-019 step 7). Pin rules and claims keep internal names independent of remote members, so a partial
  merge cannot switch a package's origin.

Configuration:

```yaml
proxy:
  merged_metadata_cache_bytes: 268435456  # default (proposed), per replica
```

Acceptance criteria:

- After `npm publish` of `@acme/util@1.5.0` to `npm-local`, `npm view @acme/util versions
  --registry …/api/npm/web/npm-all/` on the other replica lists `1.5.0` immediately, also with Valkey
  stopped.
- With Valkey stopped and `local.autoClaim: false` on `npm-local` (so no new claim changes
  the member set), the merged document of `lodash`, served so far only by `npm-remote`, is cached
  on replica B; a `manage` holder publishes `lodash@99.0.0` to `npm-local` on replica A
  (R-REPO-048); the next `npm view lodash versions` through `npm-all` on B lists `99.0.0`.
- User A (read on both members) and user B (read on `npm-local` only) request the same document in
  turn; B's response contains no version that exists only upstream.

### R-REPO-021 · Default deployment target
Priority: **MVP** · Covers: REPO-038

- `virtual.deployTarget` names a local repository in the effective member list. Without one, the
  virtual repository is read-only: writes answer 405 `no_deploy_target`, and format pages leave the
  publish resources out of their service documents.
- A write through the virtual address (format publish, `PUT /files/<projectKey>/<virtual>/<path>`) MUST be
  executed as if addressed to the target: the target's patterns, format rules, ownership and
  permissions apply ([R-AUTHZ-009](authorization.md) rule 5). Format writes such as npm deprecate and
  dist-tag changes or NuGet delete, unlist and relist are routed to the target as the format pages
  define.
- A raw `DELETE /files/<projectKey>/<virtual>/<path>` answers 405 ([storage.md](storage.md)).
- An update that removes the target from the members without changing the target answers 422
  `invalid_deploy_target`.

Acceptance criteria:

- `npm publish --registry https://trove.example.com/api/npm/web/npm-all/` stores the package in
  `npm-local`; `dotnet nuget push Acme.Widgets.1.0.0.nupkg --source
  https://trove.example.com/api/nuget/web/nuget-all/v3/index.json --api-key "$TROVE_TOKEN"` stores it
  in `nuget-local`.
- Without a target, `npm publish` to `npm-all` fails with `E405`.

### R-REPO-022 · Priority members
Priority: **P1** · Covers: REPO-018, RMT-037

- `priorityMember: true` on a local or remote repository MUST (a) place it ahead of non-priority
  members within each resolution tier of every virtual repository that contains it, and (b) restrict
  metadata merging: if any priority member holds the package, only priority members contribute.
- Whether a priority member holds the package MUST be decided without regard to the caller's
  permissions, for the reason given in R-REPO-023.
- Pin rules and claims are evaluated first and are not overridden by priority. They are the
  recommended control; priority members exist for configurations that rely on them.
- Support is a format capability (`priorityMembers` in the format catalog, R-REPO-003) and holds for
  every protocol the format serves. npm and NuGet (v3) support it.
- A protocol added to a supporting format later (NuGet v2, P2) MUST apply priority members exactly
  as the format's existing protocols do; a repository never honours priority on one of its
  protocols and ignores it on another.
- Formats not in trove yet (the catalog's exclusion lists name Chef, Debian, CocoaPods, Cargo and
  others) do not support priority members until their format page declares the capability. A
  container format page MUST define how priority interacts with tag resolution before it declares
  the capability.
- `priorityMember: true` on a virtual repository, or on a repository whose format lacks the
  capability, answers 422 `validation_failed` with `not_applicable` in `errors[]`.

Acceptance criteria:

- `npm-local` (`local.autoClaim: false`, no claims) is a priority member holding `util@1.0.0`,
  published by a `manage` holder (R-REPO-048); the stub upstream holds `util@9.9.9`: `npm view
  util versions --registry …/api/npm/web/npm-all/` lists only `1.0.0`; after the local package is
  deleted, it lists the upstream versions. (With the default `autoClaim: true`, the claim of
  R-REPO-023 keeps answering 404 after the deletion.)
- `nuget-all` = [`nuget-org`, `nuget-partner`]; `nuget-partner` (remote, priority member) serves
  `Partner.Sdk` `2.0.0`, the stub behind `nuget-org` serves `Partner.Sdk` `9.0.0`: the version list
  `GET …/api/nuget/web/nuget-all/v3/flatcontainer/partner.sdk/index.json` contains only `2.0.0`.
- `PATCH /api/v1/projects/web/repositories/npm-all` with `{"priorityMember": true}` returns 422
  `not_applicable`.

### R-REPO-023 · Dependency-confusion protection: pin rules and namespace claims
Priority: **MVP** · Covers: BEYOND-028

Dependency confusion ([Birsan 2021](https://medium.com/@alex.birsan/dependency-confusion-4a5d60fec610))
lets a public package with an internal package's name, usually with a higher version, win
resolution. Tier order alone protects only names that currently exist internally; a name that is
not published yet, was deleted or is misspelt falls through to the remote members. trove closes
this on the server with two mechanisms, both using name patterns (see Terms; most specific wins).
Claim and pin-rule patterns are matched by the R-AUTHZ-005 matcher like every name pattern, but
are limited to an exact name or a literal (possibly empty) followed by one trailing `*` or `**`,
the forms for which most specific wins is a total order; any other pattern answers 422
`invalid_pattern`. A bare `*` therefore claims or pins every unscoped npm name and no scoped one.

**Namespace claims (local repositories).** A local repository holds a list of claimed name
patterns.

- With `local.autoClaim: true` (default (proposed) for formats that support claims), the first
  deploy of a package name that no claim of that repository covers creates an exact claim with
  scope `members` in the same transaction. Claims are not removed when packages are deleted: the
  deleted case is exactly when protection matters. Patterns are stored in the folded form of the format's name rule.
  A repository holds at most 1000 claims; an auto claim that would exceed the limit is not created, the deploy
  proceeds and its audit event records `autoClaim: limit` ([Q-REPO-106](#q-repo-106--claim-and-pin-rule-patterns-are-stored-folded-a-manual-claim-replaces-an-automatic-one-and-the-limit-spares-the-deploy)).
- An auto claim MUST NOT take a name from another repository. When a claim of another repository
  that is in effect in a virtual repository containing the deploying repository (directly or
  through nesting) covers the name, the deploy proceeds without creating the auto claim, and its
  audit event records `autoClaim: skipped`. In those virtual repositories the name keeps resolving
  from the claiming repository. Otherwise the exact auto claim, being the most specific match,
  would let any `deploy` holder of one team's repository take over a name that another team
  claims in a shared virtual repository. Taking a name over on purpose is a manual claim, which
  needs `manage` and is reported as `claim_overlaps`. A manual claim of a pattern that an automatic claim of the same
  repository holds turns that claim into a manual one.
- The decision of an auto claim is serialised: it takes the configuration lock of the auto claims
  (`pg_advisory_xact_lock(4, 5)`, R-ARCH-017) when the repository is a member of a virtual repository, then the row of the
  repository (`FOR NO KEY UPDATE`), and it reads the claims after both. Two first publishes of one name into two members of one
  virtual repository therefore create one claim (the second records `autoClaim: skipped`), and two first publishes into one
  repository cannot pass the limit of 1000 ([Q-REPO-285](#q-repo-285--the-decision-of-an-auto-claim-takes-the-auto-claim-configuration-lock-for-a-virtual-member-and-the-row-of-the-repository)).
  Both locks end with the publish's transaction, so a publish that rolls back leaves no claim and no lock. A write of the
  manual claims (`PUT …/claims`) takes the same configuration lock as its first statement, before the repository row, so it and
  the auto claim of a first publish decide one after the other and the manual write's `claim_overlaps` sees a claim the publish
  committed.
- Claims for names that do not exist yet are created explicitly; this reserves a scope or prefix
  before its first publish.
- **Claim scope.** Every claim has a scope. `members` (the default, and the only scope an auto
  claim gets) is in effect only in virtual repositories that contain the claiming repository,
  directly or through nesting. `global` is in effect in every virtual repository of the format and
  is the only scope that remote refusal honours. Creating or widening a `global` claim MUST require
  `repositories:write-config`; `manage` on the repository suffices for `members` claims. Without
  this split, one deploy of `react` into any local repository, even a personal sandbox that no
  virtual repository contains, would make `react` unresolvable in every virtual repository and,
  through remote refusal, in every remote repository of the format. The claims of a repository are
  replaced as one set, so a request that narrows a `global` claim to `members` or leaves it out of the set
  needs `repositories:write-config` as well ([Q-REPO-105](#q-repo-105--a-global-claim-is-changed-only-with-repositorieswrite-config-also-when-it-is-narrowed-or-dropped)).
- **In a virtual repository**, for a package name n without an applicable pin rule: if a claim in
  effect in V covers n (a `global` claim of any local repository of the format, or a `members`
  claim of a member of V), only the local members of V whose claims are the most specific match for
  n are consulted; if V has no such member, the answer is 404. Remote members are not asked: no
  cache lookup, no upstream request, no negative-cache entry.
- **Remote refusal.** With `proxy.claims.remote_refusal: true`, every remote repository of the format,
  also when addressed directly, answers 404 for a name covered by a `global` claim and does not
  serve cached copies of it, unless the request comes through a virtual repository whose pin rule
  names that remote repository for n. A name the format's name rule cannot fold is refused by every
  remote repository whatever the switch says: no claim can be looked up for what is no name, and
  looking it up as written would miss the claim on its folded form (Q-REPO-123).
- Overlapping claims of different repositories are allowed and reported as the warning
  `claim_overlaps` for a manual claim whose pattern covers a name that a claim of another repository of the
  format covers too, when either claim is `global` or the two repositories are members of one virtual
  repository (the text does not name the other repository, which the caller need not be able to read); under most-specific matching, a team that claims `@acme/billing-*` takes those
  names from a broader `@acme/*` claim. Who may publish a name is decided by package ownership
  ([R-AUTHZ-018](authorization.md)); claims decide where a name resolves from; whether a first
  publish may shadow a package that a remote member serves is decided by R-REPO-048.

**Pin rules (virtual repositories).** `virtual.pinning.rules` is a list of
`{"packages": [name patterns], "members": [member keys]}`.

- For a package name n, the most specific pattern over all rules of V selects its rule; only that
  rule's members are consulted for every request about n (documents, artifacts, latest-version,
  search results). When none of them has the package, the answer is 404.
- Rule members MUST be in V's effective member list. A rule may name a remote member, for a partner
  registry that owns a scope; that is the only way a claimed name reaches a remote repository. A pattern
  appears in one rule of V only (422 `invalid_pattern`), so the most specific pattern selects one rule; a rule
  with no members answers 404 for its names; the members of a rule are kept in the order of V's member list
  ([Q-REPO-107](#q-repo-107--pin-rule-edge-cases-one-pattern-one-rule-an-empty-rule-is-a-block-and-a-detach-keeps-it)).
- Pin rules take precedence over claims; claims over priority members (R-REPO-022).

**Independence from the caller.** Both mechanisms MUST be evaluated on stored configuration only,
never on the caller's permissions. If they honoured permissions, a caller who cannot read the
internal member would fall through to the public package of the same name and receive exactly the
confused dependency; such a caller receives 404 instead (H-7 names the remaining disclosure).

- Each replica keeps all claims and pin rules of all repositories in memory (a prefix trie per
  format, keyed by each pattern's literal part; candidates on the path are confirmed by the
  R-AUTHZ-005 matcher, so `*` stops at `/`), loaded at start and updated through the change area
  `claims` ([architecture.md](architecture.md) R-ARCH-018); a lookup costs no database query. A
  claim change, including the auto claim of a name's first publish, is in force on every replica
  within 2 seconds of its commit, with or without Valkey. The auto claim writes the area once per
  name and repository, never per upload of a later version.
- On create or update of a virtual repository that has local and remote members, no pin rule, and
  local members without `autoClaim` and without claims, the response carries the warning
  `dependency_confusion_unprotected`.
- **Across a restore.** A claim created or widened to `global`, the auto claim of a first publish included, is journaled with
  `repository.claims_changed`; a pin rule added for a name, a member taken out of a rule and `autoClaim` switched on are
  journaled with `repository.narrowed`. A restore re-applies them, so a name that was claimed or pinned after the restore point
  does not fall through to a remote member again (R-REPO-061).

Data model:

```sql
CREATE TABLE namespace_claims (
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  pattern    text NOT NULL CHECK (length(pattern) BETWEEN 1 AND 256),
  source     text NOT NULL CHECK (source IN ('manual', 'auto')),
  scope      text NOT NULL DEFAULT 'members' CHECK (scope IN ('members', 'global')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid,
  PRIMARY KEY (repo_id, pattern),
  CHECK (source = 'manual' OR scope = 'members')
);

CREATE TABLE virtual_pin_rules (
  virtual_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  position         integer NOT NULL CHECK (position >= 0),
  package_patterns text[] NOT NULL CHECK (cardinality(package_patterns) BETWEEN 1 AND 50),
  PRIMARY KEY (virtual_id, position)
);

CREATE TABLE virtual_pin_members (
  virtual_id bigint NOT NULL,
  position   integer NOT NULL,
  member_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
  PRIMARY KEY (virtual_id, position, member_id),
  FOREIGN KEY (virtual_id, position) REFERENCES virtual_pin_rules ON DELETE CASCADE
);
```

API:

| Method and path | Body / response | Permission |
|---|---|---|
| `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/claims` | 200 `{"claims": [{"pattern": "@acme/*", "source": "manual", "scope": "global", "createdAt": …}]}`, `ETag: "<claims_version>"` | `read` on the repository |
| `PUT /api/v1/projects/{projectKey}/repositories/{repoKey}/claims` | `{"claims": [{"pattern": "@acme/*", "scope": "global"}]}`; `scope` defaults to `members`; replaces the manual claims; auto claims stay unless listed under `"removeAuto"`; `If-Match: "<claims_version>"` required → 200 with the new `ETag`; 412 `precondition_failed` when stale; 428 `precondition_required` without it | `manage` on the repository for `members` claims; `repositories:write-config` when the request adds, keeps, narrows or removes a `global` claim |
| `PATCH /api/v1/projects/{projectKey}/repositories/{repoKey}` | `{"virtual": {"pinning": {"rules": [...]}}}` with `If-Match: "<config_version>"` (R-REPO-011) | as update |

- The claims of a repository are one revisioned resource: `claims_version` increases with every
  change of its claims, manual or automatic (R-REPO-048), in the transaction that makes it. A `PUT`
  replaces the manual claim set as a whole, so a stale one could drop a `global` claim another
  administrator just added and reopen a dependency-confusion path; it therefore requires `If-Match`
  ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2), with `If-Match: *` as the deliberate blind
  write. A 412 after an automatic claim was added means: read again, then write. The claims are listed sorted
  by pattern in byte order; a write that changes nothing keeps `claims_version`. A pattern outside the two
  forms is 422 `validation_failed` with `invalid_pattern` at `/claims/<i>/pattern`; a repository that cannot
  hold claims (not local, or a format without them) answers 422 with `not_applicable` at `/claims`.

Configuration:

```yaml
proxy:
  claims:
    remote_refusal: true  # default (proposed), Q-REPO-4
```

Limits: at most 1000 claims per repository and 200 pin rules per virtual repository; patterns at
most 256 characters.

Acceptance criteria (counting stub upstream behind `npm-remote` and `nuget-remote`):

- `npm-local` claims `@acme/*` with scope `global`. The stub publishes `@acme/util@99.0.0`: `npm view @acme/util
  versions --registry …/api/npm/web/npm-all/` lists only local versions; `npm view @acme/brand-new`
  fails with `E404`; `npm view @acme/brand-new --registry …/api/npm/web/npm-remote/` fails with `E404`
  as well; the stub records no request for either name.
- `autoClaim: true` and `left-pad@1.0.0` published to `npm-local` by a holder of `manage` on it
  (R-REPO-048); the stub holds `left-pad@2.0.0`.
  `npm install left-pad --registry …/api/npm/web/npm-all/` installs `1.0.0`. After the local package is
  deleted, the install fails with `E404` instead of installing `2.0.0`. A user with `read` on
  `npm-all` but not on `npm-local` gets `E404`, not `2.0.0`.
- `nuget-local` claims `Acme.*` with scope `global`: `dotnet add package Acme.Internal` against
  `nuget-remote` directly fails with a not-found error, and the stub records no request.
- `npm-sandbox` (local, `autoClaim: true`, member of no virtual repository) receives a publish of
  `react@99.0.0`. The claim reads back with scope `members`; `npm view react versions --registry
  …/api/npm/web/npm-all/` still lists the upstream versions, and `npm-remote` addressed directly still
  serves `react`.
- A principal holding `manage` on `npm-local` but not `repositories:write-config` gets 403 on
  `PUT …/claims` with a `global` claim, and 200 with the same pattern as a `members` claim (both
  with the current `If-Match`).
- `PUT …/repositories/npm-local/claims` without `If-Match` answers 428 and changes no claim. After
  a publish to `npm-local` with `autoClaim: true` adds an automatic claim, a `PUT` with the `ETag`
  read before the publish answers 412 with the new tag as `currentEtag`.
- A pin rule `{"packages": ["@partner/*"], "members": ["npm-partner-remote"]}` in `npm-all` resolves
  `@partner/sdk` from that remote even though `npm-local` claims `@partner/*`.
- `npm-team-a` (manual claim `@acme/*`, scope `members`) and `npm-team-b` are both members of
  `npm-all`. A holder of `deploy` on `npm-team-b` publishes `@acme/util@9.9.9` there: the publish
  succeeds, `GET …/repositories/npm-team-b/claims` lists no claim for `@acme/util`, and `npm view
  @acme/util versions --registry …/api/npm/web/npm-all/` lists only the versions in `npm-team-a`.

### R-REPO-048 · Shadowing check on the first publish of a name
Priority: **MVP** · Covers: BEYOND-028

Local members form the first resolution tier, and the first publish of a name creates a claim
(R-REPO-023). A publish of a name that a remote member already serves therefore replaces the public
package for every client of the virtual repositories that contain the local repository, and hides
the versions that exist only upstream (H-10). Such a publish MUST be the deliberate decision of a
`manage` holder, never the side effect of a `deploy` grant (Q-REPO-6).

**When the check runs.** On every write that would give a local repository L, whose format
supports claims (`pinsAndClaims`, R-REPO-003), a version of the normalized package name n: a
deploy addressed to L directly or through a virtual repository's deploy target (R-REPO-021), and,
from P1, a copy, move or trash-can restore into L ([storage.md](storage.md) R-STOR-019,
R-STOR-023). It runs when both hold:

- L holds no version of n, and
- no claim of L, manual or automatic, of either scope, covers n.

Item operations report a refusal per item (`shadows_remote_package`, `shadow_check_unavailable`)
and leave that item unchanged. Without them, a `deploy` holder could publish a name into a
repository that no virtual repository contains, where nothing is exposed, and copy it into L
afterwards.

The check is never skipped for lack of the engine: a replica without the virtual engine does not start, and a Host built
without one refuses the publish (Q-ARCH-230).

Later versions of a name L already holds or claims are never checked, so a public package that
someone registers after an internal name's first publish cannot block the internal team's
releases. With `autoClaim: true` the check therefore runs once per name and repository.

**Exposure set.** The pairs (V, R) where V is an active virtual repository whose effective member
list contains L (R-REPO-018) and R is a remote member of V that R-REPO-019 steps 2 and 3, evaluated
for n on stored configuration only, would keep together with L: L and R are enabled and admitted by
their own patterns and by those of every virtual repository on their chains; if a pin rule of V
selects n, it names both L and R; and no claim in effect in V covers n (where one does, remote
members are not consulted for n, so nothing remote is shadowed). An empty exposure set (L is in no
virtual repository, or no remote member would be consulted for n) lets the publish proceed without
any upstream request.

**Whether R serves n.** Decided without regard to the caller and without the cache-fill check
(R-AUTHZ-008), on the canonical path of n's package document, which the format registers
(R-REPO-003; npm: the package document `n`; NuGet: the version list `{lower_id}/index.json`,
R-NUGET-015):

1. A stored metadata entry for that path (any variant), fresh or expired and not marked upstream
   gone (R-REPO-029): served.
2. A stored entry marked upstream gone: not served. Negative entries (R-REPO-028) are not
   consulted. They live in Valkey, and a value read from Valkey may make trove refuse, never admit
   a write ([architecture.md](architecture.md) R-ARCH-030); "not served" admits a `deploy` holder's
   publish. A path for which R holds only a negative entry goes on to step 3.
3. R offline (R-REPO-030, R-REPO-031) and nothing stored: not served; no upstream request.
4. R's circuit open (R-REPO-029) and nothing stored: undetermined; no upstream request.
5. Otherwise a probe: one `GET` of that path through R's fetch client, with its egress rules, TLS
   verification, credentials, redirect rules, size limits and fetch slots (R-REPO-033 to
   R-REPO-040), bounded by R's timeouts. 2xx: served. 404 or 410: not served. Timeout, 401, 403,
   429, 5xx and every other failure: undetermined. Probe outcomes count toward R's circuit like any
   other upstream request. The probe stores nothing, neither an entry nor a negative entry: under
   `cacheFillAction: deploy` a stored document would advertise versions on behalf of a publisher
   who need not hold that action.

The probes of one publish run concurrently. They run after the format has read the declared
identity and fine-grained authorization has passed (R-ARCH-014 steps 4 and 5), because the outcome
depends on whether the caller holds `manage`, and before the commit (step 6); for item operations,
before the operation's transaction. No database transaction is held open during a probe.

**Outcome.**

| Result over the exposure set | Caller holds `manage` on the coordinate in L, or `content:bypass` | Otherwise |
|---|---|---|
| Empty, or no R serves n | proceed | proceed |
| At least one R serves n | proceed; security audit event `repository.remote_package_shadowed` (package, L, the pairs (V, R), actor) in the commit transaction | 403 `shadows_remote_package`; nothing stored, no claim created |
| None serves n, at least one undetermined | proceed; the publish's audit event records `shadowCheck: undetermined` | 503 `shadow_check_unavailable` with `Retry-After` (the remaining circuit-open time, else 30 s); nothing stored |

- The check comes in addition to `deploy` and package ownership ([R-AUTHZ-018](authorization.md));
  it never grants anything. A refusal is audited as a denial with reason `deny.shadowing`.
- An unreachable upstream fails closed: it must not be a way around the check. Only first publishes
  of names outside L's claims are affected while a remote member's upstream is down. A remote
  repository is asked once per publish however many virtual repositories of the exposure set contain
  it; when several answer "undetermined" the Retry-After is the longest of them; an error while
  reading the stored state fails the publish and is no verdict; a replica without the remote engine
  treats every remote repository as undetermined (Q-REPO-115).
- **Approval in advance.** A manual claim of L that covers n satisfies the check, because the check
  runs only when no claim of L covers n. A `members` claim needs `manage` on L (R-REPO-023), so it
  is the same decision taken ahead of time. This is the path for CI: a `manage` holder claims
  `lodash` (or `@acme/*`) on `npm-patches`, after which a release job holding only `deploy`
  publishes there.
- The error body names the package and the virtual repositories in which the publish would shadow a
  remote package, limited to those the caller holds `read` on. It never names remote members.
- The check reads L's committed versions and claims before the commit transaction and needs no
  lock. A concurrent first publish of the same name can at worst make the check run once more than
  needed; an uncommitted publish never causes it to be skipped.
- Membership and other configuration changes do not re-run the check for names a local member
  already holds (H-10).

Acceptance criteria (`npm-all` = [`npm-local`, `npm-remote`], `nuget-all` = [`nuget-local`,
`nuget-remote`], counting stub upstreams; `ci-bot` holds `deploy` but not `manage` on the local
repositories, `lead` holds both; `packageOwnership: off` on the local repositories, so ownership
plays no part):

- The stub serves `lodash`. `ci-bot` runs `npm publish` of `lodash@4.17.22` through `npm-all`: it
  fails with `E403` and a message naming `shadows_remote_package`; `npm-local` holds no `lodash`
  and no claim for it; `npm view lodash version --registry …/api/npm/web/npm-all/` still shows the
  upstream version.
- `lead` publishes the same tarball: it succeeds, and a `repository.remote_package_shadowed` event
  names `npm-local`, `npm-all` and `npm-remote`. `ci-bot` then publishes `lodash@4.17.23`, which
  succeeds, and the stub records no request for `lodash` during that publish.
- `lead` sets the manual claim `left-pad` (scope `members`) on `npm-local`; `ci-bot` then publishes
  `left-pad@9.0.0` successfully although the stub serves `left-pad`.
- `ci-bot` publishes `acme-brand-new@1.0.0`, which the stub does not know: it succeeds after exactly
  one stub request for the name, and no negative entry exists afterwards, neither in the replica's
  memory nor in Valkey.
- The stub serves `acme-brand-new-3`, and a negative entry for its package document is written into
  Valkey by hand with a current creation time. `ci-bot`'s publish of `acme-brand-new-3@1.0.0`
  answers 403 `shadows_remote_package` after one stub request.
- `npm-sandbox` (member of no virtual repository): `ci-bot` publishes `react@99.0.0`; it succeeds
  and the stub records no request.
- The circuit of `npm-remote` is open and nothing is stored for `brand-new-2`: `ci-bot`'s publish
  of it answers 503 `shadow_check_unavailable` with `Retry-After`; `lead`'s succeeds.
- `dotnet nuget push Newtonsoft.Json.99.0.0.nupkg --source …/api/nuget/web/nuget-all/v3/index.json
  --api-key "$CI_BOT_TOKEN"` fails with 403 while the stub serves `newtonsoft.json`.
- P1, with copy: `ci-bot` publishes `lodash@4.17.22` to `npm-sandbox` (succeeds, nothing exposed)
  and then copies it to `npm-local` with `POST /api/v1/content/copy`: the item is reported with
  `shadows_remote_package`, and `npm-local` holds no `lodash`.

## Remote repositories (pull-through proxy)

### R-REPO-024 · Remote repository settings
Priority: **MVP** · Covers: RMT-001, RMT-002, RMT-004, RMT-015

A remote repository proxies exactly one upstream and caches what clients ask for. In the default mode `pullThrough`
nothing is fetched ahead of time (INV-11); in the mode `mirror` background passes also store what the upstream offers
within the remote's patterns and bounds, before a client asks (R-REPO-050 to R-REPO-059). Clients cannot deploy into it. Every setting below is a member of the
`remote` section; omitted members take the default, format defaults come from the format catalog
(R-REPO-003).

<details>
<summary>All remote settings</summary>

| Member (`remote.…`) | Meaning | Default (proposed) | Range | Catalog | Spec |
|---|---|---|---|---|---|
| `url` | Upstream base URL (NuGet: the service index URL) | — (required) | absolute `http`/`https`, no user info, no fragment | RMT-004 | here |
| `extraHosts` | Further hosts that upstream metadata or redirects may point to: `[{"pattern": "*.nuget.org", "sendCredentials": false}]` | `[]`; a format proposes entries for its well-known upstream, applied when `url` is omitted or equals it (Q-REPO-91) | ≤ 20 | — | R-REPO-043 |
| `authMode` | `none`, `basic`, `bearer` | `none` | | RMT-005, RMT-006 | R-REPO-033 |
| `username` | Upstream account | `""` | ≤ 256 | RMT-005 | R-REPO-033 |
| `password` | Password or token; secret field | unset | ≤ 8 KiB | RMT-005 | R-REPO-033 |
| `clientCertificate` | Name of a stored client certificate | `null` | | RMT-007 | R-REPO-034 |
| `headers` | Extra request headers | `[]` | ≤ 32 | RMT-009 | R-REPO-036 |
| `forwardAuthOnRedirect` | Send credentials to other origins on redirects | `false` | | RMT-012 | R-REPO-039 |
| `outboundProxy` | `{"mode": "default"\|"named"\|"direct", "name": …}` | `{"mode": "default"}` | | RMT-016 | R-REPO-037 |
| `connectTimeoutMs` | TCP and TLS connection establishment | `10000` | 1000–120000 | RMT-015 | here |
| `readTimeoutMs` | Longest time without receiving a byte | `15000` | 1000–600000 | RMT-015 | here |
| `sourceNetwork` | Network prefix from which each replica takes its own local address for upstream connections (Q-REPO-9) | `null` (kernel choice) | an IPv4 or IPv6 CIDR prefix | RMT-015 | here |
| `cacheFillAction` | `read` or `deploy` | `read` | | IAM-023 | [R-AUTHZ-008](authorization.md) |
| `cache.storeArtifacts` | `false` = pass-through | `true` | | RMT-017 | R-REPO-044 |
| `cache.metadataTtlSeconds` | Metadata period | format default (npm: `7200`, NuGet: `300`) | 0–2592000 | RMT-018 | R-REPO-027 |
| `cache.metadataRefreshTimeoutSeconds` | Wait for a refresh before serving stale | `60` | 1–600 | RMT-019 | R-REPO-027 |
| `cache.notFoundTtlSeconds` | Negative-cache period | format default (npm and NuGet: `1800`) | 0–86400 | RMT-020 | R-REPO-028 |
| `cache.maxStaleSeconds` | Upper bound for serving stale metadata, `0` = unbounded | `0` | 0–31536000 | — | R-REPO-029 |
| `cache.unusedRetentionHours` | Remove cache content unused this long, `0` = keep | `0` | 0–87600 | RMT-021 | R-REPO-046 |
| `integrity.checksumPolicy` | `verify-if-available`, `require`, `ignore`, `pass-through` | `verify-if-available` | | RMT-022 | R-REPO-041 |
| `integrity.blockUnexpectedContentTypes` | Refuse error pages in place of content | `true` | | RMT-023 | R-REPO-042 |
| `integrity.allowedContentTypes` | Exemptions from the blocked list | `[]` | ≤ 20 | RMT-023 | R-REPO-042 |
| `availability.mode` | `online` or `offline` | `online` | | RMT-030 | R-REPO-030 |
| `availability.failureThreshold` | Consecutive failures that open the circuit | `5` | 1–100 | RMT-029 | R-REPO-029 |
| `availability.circuitOpenSeconds` | Assumed-offline period, `0` = never | `300` | 0–86400 | RMT-029 | R-REPO-029 |
| `limits.maxArtifactBytes` | Per-artifact limit, `null` = system default | `null` | | — | R-REPO-040 |
| `limits.maxMetadataBytes` | Per-document limit, `null` = system default | `null` | | — | R-REPO-040 |
| `limits.maxConcurrentFetches` | Upstream fetches of this repository in flight across all replicas, shared out per replica (Q-REPO-8) | `64` | 1–4096 | — | R-REPO-040 |
| `mode` | `pullThrough` or `mirror` | `pullThrough` | | API-082 | R-REPO-050 |
| `mirror.paused` | No pass starts; a running one stops before its next package | `false` | | API-082 | R-REPO-050, R-REPO-053 |
| `mirror.intervalSeconds` | From the start of one pass to the start of the next | `3600` | 300–604800 | API-082 | R-REPO-053 |
| `mirror.fullPassIntervalSeconds` | Age of the last full pass that makes the next one full; `0` = only when needed | `604800` | `0`, 86400–7776000 | — | R-REPO-051 |
| `mirror.discovery` | `auto`, `feed`, `listing`, `names` | `auto` | | — | R-REPO-051 |
| `mirror.concurrency` | Ceiling of the pass's concurrent background fetches | `4` | 1–64 | — | R-REPO-054, R-REPO-055 |
| `mirror.maxVersionsPerPackage` | Newest versions per package the pass stores, `0` = all | `0` | 0–10000 | — | R-REPO-051 |
| `mirror.includePrereleases` | Whether the pass stores prerelease versions | `true` | | — | R-REPO-051 |
| `mirror.maxBytes` | Byte bound of the repository's stored content for the pass, `null` = none of its own | `null` | 1048576–2^62 | — | R-REPO-052 |

</details>

- `url` is stored normalized (scheme and host lower case, default port removed, an empty path as
  `/`). An omitted `url` takes the format's default upstream; a format without one makes it required
  (422 `invalid_upstream_url`). The upstream request for a canonical path is derived from it by the
  format plug-in. A plain `http` URL is accepted with the warning `plain_http_upstream`. A URL that
  points at a denied address is accepted too: the refusal is `egress_denied` at the first request
  (R-REPO-038).
- The stored section is canonical: every member of the table is present, with the default where the
  request omitted it, and a read returns exactly that. `password` reads `null`, `{"set": true}` or its
  reference (R-REPO-033); `sourceNetwork` reads as the network (`10.99.0.77/24` is stored as
  `10.99.0.0/24`).
- Until their requirements are built, `clientCertificate` (R-REPO-034), `headers` (R-REPO-036),
  `outboundProxy.mode: named` (R-REPO-037), `cache.storeArtifacts: false` (R-REPO-044) and a
  `cache.unusedRetentionHours` above 0 (R-REPO-046) are refused with 422 `not_applicable`: a setting
  that is accepted and not enforced would leave the operator believing it is in effect.
  `outboundProxy.mode: direct` is built and means no proxy (Q-REPO-91).
- `sourceNetwork` names a prefix, never a single address: replicas are interchangeable, and every
  pod has its own address ([Kubernetes network model](https://kubernetes.io/docs/concepts/services-networking/):
  "Each pod in a cluster gets its own unique cluster-wide IP address"), so one stored address could
  fit one pod at most (Q-REPO-9). Each replica binds the repository's upstream connections to the
  first of its own interface addresses inside the prefix, enumerated at start and every 60 s.
  - A replica without such an address answers the repository's fetches with 502
    `source_network_unavailable`. The failure does not count toward the circuit, which all replicas
    share (R-REPO-029); it is logged at most once per minute and repository and counted in
    `trove_remote_source_network_unavailable_total{repo}` (R-REPO-047). There is no fallback to the
    kernel's choice, which could leave through an interface the upstream's firewall rejects.
  - A value that is not a CIDR prefix answers 422 `invalid_source_network`. A prefix that holds a
    single address (`/32`, `/128`) is stored with the warning `source_network_single_address`,
    because it can fit one pod only.
  - The enumeration happens when the client is built and on the first dial after the last one is 60 s old;
    a failing enumeration keeps the last good address, and one that finds no address in the prefix drops
    it (Q-ARCH-198).
  - The upstream test reports the local address the probing replica bound, or
    `source_network_unavailable`.

Data model (scalar settings live in `repositories.config`; references and secrets are relational):

```sql
CREATE TABLE remote_references (
  repo_id                 bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  client_certificate_name text REFERENCES client_certificates (name) ON DELETE RESTRICT,
  outbound_proxy_name     text REFERENCES outbound_proxies (name) ON DELETE RESTRICT
);
```

API, besides R-REPO-009 to R-REPO-013:

| Method and path | Purpose | Response | Permission |
|---|---|---|---|
| `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/upstream-test` | Probe the saved upstream configuration | 200 with the result below | `repositories:write-config`, or the role `owner` in the project (R-AUTHZ-026) |
| `POST /api/v1/projects/{projectKey}/upstream-tests` | Probe an unsaved `remote` section (body: `format` and `remote`); secret values in the body are used once and not stored | 200 with the result below | `repositories:write-config`, or the role `owner` in the project |

```json
{
  "reachable": true,
  "httpStatus": 200,
  "authenticated": true,
  "durationMs": 184,
  "tls": { "peerSubject": "CN=registry.npmjs.org", "notAfter": "2027-03-01T00:00:00Z" },
  "outboundProxy": null,
  "sourceAddress": null,
  "error": null
}
```

The probe requests the format's probe resource (npm: the registry ping; NuGet: the service index,
whose required resources [formats/nuget.md](formats/nuget.md) checks) through the same client,
egress rules, proxy and credentials that real fetches use (Q-REPO-90).

- The answer is always 200 for a repository that can be tested: an upstream that fails is a result
  with `reachable: false` and the reason in `error`. `reachable` is true when the upstream answered
  with an HTTP response, whatever its status; `authenticated` is false for 401 and 403 and `null`
  when there was no response, and for the `401` of a format whose upstream authenticates with a token handshake and whose
  probe resource answers it with a Bearer challenge, unless the repository's `authMode` is `bearer` (OCI, [Q-REPO-280](#q-repo-280--the-upstream-test-reads-a-bearer-challenge-of-a-handshake-format-as-reachable)); `tls` describes the leaf certificate of the host that answered and is
  `null` over plain `http`; `outboundProxy` is `null` until named proxies exist.
- `error` is `null` or `{"code", "message"}` with one of `dns_error`, `connection_refused`,
  `timeout`, `tls_untrusted`, `egress_denied`, `source_network_unavailable`,
  `upstream_secret_unavailable`, `offline`, `too_many_redirects`, `insecure_redirect` and
  `request_failed`. The message names the host and never repeats the URL, whose query may carry a
  token, or a secret.
- A repository in `availability.mode: offline` and every repository while `proxy.global_offline` is on
  are not contacted: the result is `reachable: false` with `offline` (R-REPO-030, R-REPO-031).
- The test reads and writes no circuit state, counts nothing toward it and leaves cache entries
  alone (R-REPO-029). It runs on the replica that received it; with a `sourceNetwork`,
  `sourceAddress` is the address that replica bound.
- `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/upstream-test` answers 404 `repository_not_found` for a
  missing project or key and 400 `not_applicable` for a local or virtual repository. `POST
  /api/v1/projects/{projectKey}/upstream-tests` checks
  the section like a create (422 with every violation, 400 for a misspelt member); a secret value in
  it is used for the probe and stored nowhere; `{"set": true}` is 422 `invalid_secret`, because it
  refers to a stored value.
- An Owner's saved test reads the repository by project and key from PostgreSQL again: a repository that moved out of
  the project after the step decided answers 404 and is not contacted, as a replace, a patch and a delete do. The
  unsaved test of an Owner without `repositories:write-config` follows the rules of its creates: no
  `secretRef` (422 `secret_value_required`, Q-REPO-127), no `sourceNetwork` and no `outboundProxy.mode`
  but `default` (422 `outbound_path_restricted`, Q-REPO-128).

Acceptance criteria:

- A remote created with only `url` reads back every member of the table with its default.
- The upstream test against `https://registry.npmjs.org/` returns `reachable: true`; against an
  unknown host it returns `reachable: false` with a DNS error and leaves the circuit state alone.
- `"connectTimeoutMs": 50` returns 422 `out_of_range`.
- Two replicas each have a secondary interface in `10.99.0.0/24` (`10.99.0.11`, `10.99.0.12`), and
  the remote has `"sourceNetwork": "10.99.0.0/24"`: the stub records requests from both addresses
  and from no other. A third replica without such an interface answers 502
  `source_network_unavailable`, and the circuit stays closed on all three.
- `"sourceNetwork": "10.99.0.11"` returns 422 `invalid_source_network`; `"10.99.0.11/32"` is stored
  with the warning `source_network_single_address`.
- A create with `"url": "http://registry.internal.example/"` is saved with the warning
  `plain_http_upstream`; `"clientCertificate": "mtls"` returns 422 `not_applicable`.
- The upstream test of a repository in offline mode sends no request and reports `offline`; the test
  of a local repository returns 400 `not_applicable`.

### R-REPO-025 · Remote request path
Priority: **MVP** · Covers: RMT-001, RMT-033

For a request with descriptor d (R-REPO-003) to remote repository R by caller c, the server MUST
execute these steps in order:

1. R disabled → 404 `repository_disabled`.
2. d not admitted by R's patterns, or d's package name refused because of a claim (R-REPO-023) →
   404, no upstream request.
3. c lacks `read` on R for d → 401, 403 or 404 per [R-AUTHZ-016](authorization.md).
4. Look up the cache entry (R, path, variant). An entry marked upstream gone, and every entry of a
   package that is gone from R, counts as absent here and in every later step (R-REPO-029; gone
   packages below). An artifact entry, or a metadata entry that is fresh and not invalidated
   (R-REPO-045), is served: `Cache-Status: trove; hit; ttl=<seconds>`. A format MAY require a
   current package document before it serves a cached artifact. npm does: a cached tarball is
   served only while its package document's entry is unexpired and validated after
   `system_state.restored_at`; otherwise the request first revalidates the document under these
   steps (one flight, R-REPO-026) and then applies the format's removal marks to the outcome
   ([formats/npm.md](formats/npm.md) R-NPM-025, Q-NPM-13; R-REPO-029).
5. Request through the cache view, or with `Cache-Control: only-if-cached` → R-REPO-032.
6. R offline, global offline (R-REPO-031), or read-only maintenance mode with `cacheFill: false`
   ([R-OPS-024](operations-and-observability.md)) → serve an existing entry as stale, otherwise 404
   `not_cached_offline`.
7. R's circuit open (R-REPO-029) → serve an existing entry as stale, otherwise 503
   `upstream_assumed_offline` with `Retry-After`.
8. A valid negative entry (R-REPO-028; the replica's L1 first, then Valkey) → 404,
   `Cache-Status: trove; hit; detail=negative`. While Valkey is unavailable, only the L1 answers.
9. c fails the cache-fill check ([R-AUTHZ-008](authorization.md)), which every caller fails on an
   endpoint with `cache_fill: false` ([endpoints.md](endpoints.md) R-EP-019) → serve an existing
   expired entry as stale without contacting the upstream, otherwise 403 `populate_not_permitted`
   (R-AUTHZ-008; through a virtual repository the member counts as a miss). No negative entry is
   written.
10. The collapsing protocol (R-REPO-026). Only the request that fetches, once it holds the flight
    or goes on without the cross-replica lock, takes the fetch slots of R-REPO-040; then the fetch,
    classification of the outcome (R-REPO-029), verification (R-REPO-041, R-REPO-042), storage and
    response. The repository's share of the slots is returned when the upstream exchange ends, before
    verification and storage; the replica's slot when the fetch ends (R-REPO-040, Q-REPO-235).

- **Gone packages** (R-REPO-029). A request whose descriptor names a package that is gone from R,
  other than the request for that package's package document, MUST answer 404 after step 3,
  without serving a stored entry and without an upstream request for its path. One exception keeps
  the mark self-healing for clients that request artifacts without documents: when the package
  document has no valid negative entry (it expired or was invalidated, R-REPO-045), the request
  first refreshes the package document under these steps (one flight, R-REPO-026) and waits for
  it; a 2xx clears the mark and the request continues at step 4, any other outcome answers 404.
  The cache view never refreshes and answers 404. The package document request itself follows the
  steps; its stored entry counts as absent, so its negative entry answers (step 8) or the upstream
  is asked.
- `HEAD` is answered from the cache when an entry exists; otherwise the upstream is asked with `HEAD`
  under steps 1 to 10, and nothing is stored.
- `Range` requests are served from a stored artifact ([storage.md](storage.md)). For an uncached
  artifact the whole artifact is fetched first (one flight), then the range is served.
- Client query parameters are not forwarded unless the format plug-in maps them (search queries).
- The upstream request URL MUST be built from `url` and the canonical path by percent-encoding
  every path segment again ([RFC 3986, section 3.3](https://www.rfc-editor.org/rfc/rfc3986#section-3.3):
  `%` becomes `%25`; separators inside a name, such as npm's scoped-name `/`, follow the format
  page). A canonical path is never concatenated into an upstream URL as it is. It has been decoded
  once (Terms), so a client that sent `%252e%252e` would otherwise make trove request `..` from
  the upstream, with the upstream credentials of R-REPO-033.
- Fetched content is committed through the upload pipeline of [storage.md](storage.md) (spool,
  hashing, deduplication, R-STOR-004 and R-STOR-005). Artifacts become items of R (with the item's
  `upstream_url` set): they can be browsed, annotated, copied and deleted. Metadata documents are
  cache entries only; their bytes are blobs as well (INV-4), counted as references by garbage
  collection, but they are not items.
- **Object store unavailable** ([R-STOR-036](storage.md)): an artifact miss is streamed to the client
  without storing it, under the pass-through rules of R-REPO-044 (verification included); a
  refreshed metadata document is served but not stored, and entries are not updated. The
  requests that waited for the flight fetch for themselves, and a ranged request is answered
  503 `object_store_unavailable`, because the range is served from a stored item (Q-REPO-95).
- **Lost content** ([storage.md](storage.md) R-STOR-027, [ha-and-helm.md](ha-and-helm.md) R-HA-024, Q-REPO-202 to
  Q-REPO-204): an entry whose blob is marked `missing` or `corrupt`, by the read that found its object gone or damaged or by the
  scrub, counts as absent in step 4 and every later step, and is filled again under steps 5 to 10, one flight per cluster
  (R-REPO-026); the commit of the same content writes the object again (R-STOR-005). A metadata document is fetched without
  validators. An artifact keeps its recorded digest (R-REPO-041): an upstream that serves other bytes now repairs nothing. A
  client's request that cannot fill it (the cache view, `only-if-cached`, offline, an open circuit, a negative entry, the
  populate check, an upstream failure) is answered `503 content_unavailable` with `Retry-After`, never the `404` of an uncached
  item and never `500`; a document the upstream answers 404 or 410 is marked gone as on any refresh (R-REPO-029). A document
  whose object turns out to be lost when its bytes are read is filled again once and served when the upstream's copy has the
  digest the request read, else answered `503` with `Retry-After: 1`. Through a virtual repository a remote member's lost
  cached copy is a miss of the cache tier and a transient failure of the member (R-REPO-019 step 7): the member's fetch tier
  fills it again for a caller who may populate it, and a caller who may not gets the `503`, never the `404` (Q-REPO-205); a
  local member's loss is that member's answer. A merged document keeps listing a version whose cached artifact is lost
  (Q-REPO-206). An entry marked `missing` whose object is back (found by the read's recheck, once per blob and 30 s,
  [storage.md](storage.md) Q-STOR-153) is served again without an upstream fetch. A fill that leaves the item lost (an
  artifact whose upstream serves other bytes, a document the format keeps) is not repeated for 5 minutes on the replica, and a
  request that waited for it on another replica does not fetch it again; the `503` carries the rest of the 5 minutes as
  `Retry-After` (Q-REPO-205).
- Replicas SHOULD keep recently served metadata bytes in a bounded in-memory cache keyed by blob
  digest, so a fresh hit costs no object-store round trip. Which digest a request serves is always
  read from `remote_cache_entries`; the key is the content address, so this cache can never make
  one replica answer differently from another (INV-18, R-REPO-049). It is never shared through
  Valkey ([architecture.md](architecture.md) R-ARCH-030). Its size is `proxy.metadata_cache_bytes`
  (128 MiB by default); a document above an eighth of it is not kept.
- A `Volatile` request of a format (a package search) is passed through to the upstream and not stored: the engine keeps the
  upstream's answer, as received, in the registered per-replica cache `proxy_query_results` for the period the format asks, at most
  one hour, within `proxy.query_cache_bytes` (8 MiB by default; `0` turns the cache off). The request takes a fetch slot, meets the
  circuit and the egress, TLS, redirect and credential rules of any fetch, and its host must be the upstream's or an `extraHosts`
  entry; a copy that is current is served without contacting the upstream, to every caller. Without one, a repository that is offline,
  the cache view, `only-if-cached` and a caller who may not populate (R-AUTHZ-008, R-EP-019) send nothing, and the format answers from
  its own cache-only form. A 404 or 410 writes no negative entry (architecture.md R-ARCH-013 step 6, Q-ARCH-270).

Data model:

```sql
CREATE TABLE remote_cache_entries (
  id                     bigint GENERATED ALWAYS AS IDENTITY UNIQUE,     -- joins the old and new rows of the blob release trigger (Q-REPO-86)
  repo_id                bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path                   text COLLATE "C" NOT NULL,
  variant                text NOT NULL DEFAULT '',
  kind                   text NOT NULL CHECK (kind IN ('artifact', 'metadata')),
  item_id                bigint REFERENCES items (id) ON DELETE CASCADE,  -- artifacts (storage.md)
  blob_sha256            bytea NOT NULL REFERENCES blobs (sha256),       -- a blob reference
  upstream_url           text NOT NULL,     -- final URL after redirects, without user info
  upstream_etag          text,
  upstream_last_modified timestamptz,
  fetched_at             timestamptz NOT NULL,   -- last 200 from the upstream
  validated_at           timestamptz NOT NULL,   -- last 200 or 304
  expires_at             timestamptz,            -- NULL for artifacts
  upstream_gone_at       timestamptz,            -- metadata: upstream answered 404/410 (R-REPO-029)
  last_used_at           timestamptz NOT NULL,   -- metadata use, written in batches (R-REPO-046)
  PRIMARY KEY (repo_id, path, variant),
  CHECK ((kind = 'artifact') = (item_id IS NOT NULL)),
  CHECK ((kind = 'artifact') = (expires_at IS NULL)),
  CHECK (kind = 'metadata' OR upstream_gone_at IS NULL)
);
CREATE INDEX remote_cache_entries_blob ON remote_cache_entries (blob_sha256);       -- every blob reference is indexed (R-STOR-003)
CREATE INDEX remote_cache_entries_item ON remote_cache_entries (item_id) WHERE item_id IS NOT NULL;
CREATE INDEX remote_cache_entries_unused ON remote_cache_entries (repo_id, last_used_at)
  WHERE kind = 'metadata';
CREATE INDEX remote_cache_entries_gone ON remote_cache_entries (repo_id)
  WHERE upstream_gone_at IS NOT NULL;                 -- loads the gone packages (R-REPO-029)
-- Triggers blob_ref_guard, blob_ref_release and blob_ref_release_update as on items (storage.md R-STOR-003).
```

Acceptance criteria:

- First `npm install lodash@4.17.21 --registry …/api/npm/web/npm-remote/`: the stub upstream records one
  document and one tarball request; the response carries `Cache-Status: trove; fwd=uri-miss;
  fwd-status=200; stored`. A second install records no upstream request and carries `hit`.
- With `cacheFillAction: deploy`, a user with `read` only installs `lodash@4.17.21` (cached) and
  gets `E403` (`populate_not_permitted`) for `lodash@4.17.20` (not cached); the stub records no
  request.
- `curl -I …/files/web/npm-remote/express/-/express-4.21.2.tgz` for an uncached tarball returns the
  upstream's headers and leaves no cache entry.
- With the S3 store stopped, an uncached tarball is still installed through `npm-remote` and no cache entry
  is written.
- A unit test of the upstream URL builder turns the canonical segment `%2e%2e` into `%252e%252e`
  and `a b` into `a%20b`; no input produces an upstream path that decodes to a `..` segment.

### R-REPO-026 · Collapsing concurrent misses (single-flight and Valkey flight lock)
Priority: **MVP** · Covers: trove-only

Concurrent requests for the same uncached or expired entry MUST cause one upstream fetch per
cluster, not one per request (INV-11). Since 2026-10-28 the cross-replica part is a lock in Valkey
behind `cluster.FlightLock` ([architecture.md](architecture.md) R-ARCH-033, Q-ARCH-6), not a
PostgreSQL advisory lock. The protocol:

1. **Flight key.** (repository id, canonical path, variant). `<f>`, the lowercase hex SHA-256 of
   `repository id 0x00 path 0x00 variant` (the id in decimal digits), names the flight in Valkey: the lock
   `trove:<ks>:lock:fetch:v1:<f>` and the outcome channel `trove:<ks>:fill:v1:<f>`
   ([architecture.md](architecture.md) R-ARCH-030). The full hash leaves no collision case
   (Q-ARCH-7).
2. **In-process.** Requests on one replica join one flight
   ([singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)). The leader runs steps 3 to 6;
   followers wait for its result and then read the stored entry, or receive its error.
3. **Cross-replica lock.** The leader calls `FlightLock.Acquire` and never talks to Valkey itself.
   `Acquire` tries once: `SET trove:<ks>:lock:fetch:v1:<f> <token> NX PX 30000`, with the TTL
   `cluster.flight_lock_ttl` (30 s by default) and a token unique to this acquisition
   ([architecture.md](architecture.md) R-ARCH-033).
   If it acquires the flight, it repeats the checks of R-REPO-025 steps 4 to 8, because another
   replica may have just stored the entry, recorded an upstream 404 as a negative entry or opened
   the circuit. If one of them decides, the leader answers from it and releases the flight.
   Otherwise it takes its fetch slots (R-REPO-040) and fetches. While the fetch, verification and
   commit run, `FlightLock` extends the hold every third of `cluster.flight_lock_ttl` with a
   compare-and-set on its token ([SET … IFEQ](https://valkey.io/commands/set/)).
4. **Waiting.** If another replica holds the flight, the leader calls `FlightLock.Wait`: it
   subscribes to the outcome channel, checks the lock once more, and returns on the outcome, when
   the lock is gone, or after `proxy.lock_poll_interval_ms`, after which it checks again. Polling
   stays mandatory, because Pub/Sub delivers at most once
   ([Pub/Sub](https://valkey.io/topics/pubsub/)). After `Wait` returns, the leader repeats the
   checks of R-REPO-025 steps 4 to 8: a valid entry is served, a valid negative entry answers 404,
   an open circuit answers as step 7 says. Only when none of them decides does it call `Acquire`
   again (the holder failed, or its lock expired). Without the negative-entry check, every waiting
   replica would ask the upstream once more, one after another, for a path the holder just found
   missing. With `notFoundTtlSeconds: 0` nothing records the miss, and that is what happens.
   - Waiting is bounded by `proxy.fetch_wait_timeout_seconds`. When the bound ends without a valid
     entry, the leader fetches without the cross-replica lock instead of failing, counted in
     `trove_flight_lock_bypass_total{reason="wait_timeout"}`.
   - For expired metadata, the refresh timeout of R-REPO-027 ends the client's wait earlier: the
     client is served the stale entry. The flight keeps waiting, detached like a fetch, until
     `proxy.fetch_wait_timeout_seconds`, and then refreshes without the lock (`wait_timeout`). A
     slow refresh that another replica holds is therefore not duplicated within that bound, and a
     held lock cannot keep a document stale beyond it.
   - A lock that is held too long, or written by someone else ([architecture.md](architecture.md)
     H-49), can therefore delay a fetch or a refresh by at most `proxy.fetch_wait_timeout_seconds`,
     never fail a request.
5. **Commit and release.** The blob is committed through [storage.md](storage.md); the entry upsert
   runs in one short transaction, the cache-fill transaction, which also writes the populate audit
   event (below). The upsert keeps an entry with a later `fetched_at` unless the format's `OnFill`
   answered that it recorded this document as the newer one by an order of its own (Q-REPO-120). An upstream 404 or 410 is recorded as a negative entry
   (R-REPO-028) before the release. After the entry has committed, after the negative entry has
   been written, or after the fetch has failed, `FlightHold.Release` deletes the lock only if it
   still carries this hold's token ([DELIFEQ](https://valkey.io/commands/delifeq/)) and publishes
   `stored` or `failed` on the outcome channel. A woken waiter therefore finds the committed entry
   or the negative entry through step 4's checks. The leader's own client receives the withheld
   tail (R-REPO-041) only after the entry has committed. No PostgreSQL connection is held and no
   transaction is open while acquiring, waiting, transferring or extending
   ([architecture.md](architecture.md) R-ARCH-019).
6. **Holder loss.** A holder that shuts down releases with `failed` (shutdown bullet below). A
   holder that dies without releasing (process killed, node lost) frees the flight when its key
   expires, after at most `cluster.flight_lock_ttl`; waiters acquire it at their next check and
   fetch. Neither case depends on a database session or on TCP keepalive.

- **Valkey unavailable.** `Acquire` and `Wait` return at once (the replica's Valkey breaker,
  [architecture.md](architecture.md) R-ARCH-031), and the leader fetches under its in-process
  single-flight only (`reason="valkey_unavailable"`): at most one upstream fetch per replica and
  flight. No request fails because Valkey failed (INV-12).
- **The lock is an optimization, not a correctness condition** (INV-11). A lock lost in a Sentinel
  failover, a bypass and an unavailable Valkey all lead to concurrent fetches of one entry, and
  these are correct: blob commits are idempotent by content address, and the entry upsert keeps the
  newer `fetched_at` for metadata and refuses a different digest for an artifact (R-REPO-041).
  - Such duplicates are the normal degraded mode now, not a rare race, so the losing side is
    specified. A leader whose artifact upsert is refused because another flight committed a
    different digest MUST abort its own client before the withheld tail, as on an integrity
    failure (R-REPO-041). It MUST serve its followers and waiters from the committed entry and
    write the security event `proxy.upstream_digest_changed`.
  - No client completes a download whose bytes differ from the stored artifact.
- **Detached fetch.** The leader's fetch runs detached from its initiating client: a client that
  disconnects does not abort a fetch others wait for. The leader's own client MAY receive the body
  while it is fetched, under the withheld-tail rule of R-REPO-041; followers and waiters are served
  from the stored entry.
- **Shutdown and drain** (Q-REPO-7). From `SIGTERM` on, a replica leads no new cross-replica
  flight.
  - A request on a draining replica joins a flight that another replica holds, as a waiter (steps 4
    and 5), but never calls `Acquire`. When no other replica holds the flight, it fetches under its
    in-process single-flight only (`reason="draining"`).
  - Fetches it already leads keep running detached and commit as usual. Those still running 5 s
    before the shutdown deadline are cancelled and released with `failed`
    ([architecture.md](architecture.md) R-ARCH-024 step 4), so waiters on other replicas take over
    at once.
  - A cancelled fetch leaves no entry: its spool file is the pod's own scratch space (INV-13), and
    no other replica continues a partial body.
- **Populate audit** ([authorization.md](authorization.md) R-AUTHZ-029). Only the request that
  fetches from the upstream writes the transactional populate event (`authz.decision`, allowed
  populate), in its cache-fill transaction (step 5); if the event cannot be written, the fill
  fails. A request that joins a flight, in-process (step 2) or as a waiter through the Valkey
  flight lock (step 4), fetched nothing and writes none; its read is audited as a read. A request
  that goes on without the cross-replica lock (`valkey_unavailable`, `wait_timeout`, `draining`)
  fetches and writes one. The number of populate events therefore follows the number of upstream
  fetches, not the number of replicas or concurrent requesters.
- Every response served from a collapsed flight carries `collapsed` in `Cache-Status`.
- The protocol opens no PostgreSQL connection of its own, so it adds nothing to the connection
  budget of [ha-and-helm.md](ha-and-helm.md) R-HA-016.

Configuration:

```yaml
proxy:
  lock_poll_interval_ms: 1000      # default (proposed)
  fetch_wait_timeout_seconds: 120  # default (proposed)
# The lock's TTL is cluster.flight_lock_ttl (30 s, architecture.md R-ARCH-033).
```

Acceptance criteria (two replicas, counting stub upstream that delays every response by 2 s):

- 100 concurrent requests for the same uncached tarball, 50 per replica: the stub records exactly
  one `GET`; all 100 responses are 200 with identical bytes; at least 99 carry `collapsed` or `hit`;
  exactly one allowed populate event exists for the tarball's path. Meanwhile `pg_locks` shows no
  advisory lock of category 2 (`fetch`, retired, [architecture.md](architecture.md) R-ARCH-017) and
  none held by a connection of class `main` (R-ARCH-019), and `pg_stat_activity` shows no trove
  connection `idle in transaction`.
- The stub streams a 200 MiB artifact slowly, and the leader's pod is killed with `SIGKILL`
  mid-transfer. A waiter on the other replica acquires the flight within `cluster.flight_lock_ttl`
  plus `proxy.lock_poll_interval_ms`, fetches, and completes every waiting request. No entry for a
  partial body exists at any time.
- The same with `kubectl delete pod` and a transfer that outlasts the leader's shutdown deadline:
  the waiter acquires within `proxy.lock_poll_interval_ms` of the leader's `failed`, and the
  leader's own client, retrying through the Service, is served by the other replica.
- A request reaches a replica during its drain delay for a path that no flight holds: no lock key
  of that replica appears in Valkey (`valkey-cli GET trove:<ks>:lock:fetch:v1:<f>`), the request is
  served, and `trove_flight_lock_bypass_total{reason="draining"}` increments.
- With Valkey stopped, all 100 requests of the first criterion succeed, and the stub records at
  most one `GET` per replica.
- With every message on the outcome channels dropped by a fault proxy, the first criterion still
  records exactly one `GET`, and the waiters complete within the stub's delay plus one poll
  interval.
- 100 concurrent requests, 50 per replica, for a path the stub answers with 404: the stub records
  exactly one `GET`, and all 100 responses are 404.
- A lock key written by hand with a foreign token, and `proxy.fetch_wait_timeout_seconds: 5`: a
  request for that path is served after about 5 s, and
  `trove_flight_lock_bypass_total{reason="wait_timeout"}` increments.
- The same foreign lock key, written without expiry, on an expired metadata document, with
  `metadataRefreshTimeoutSeconds: 2` and `proxy.fetch_wait_timeout_seconds: 5`: the request is
  served the stale document after about 2 s, the stub records one conditional request about 5 s
  after the request arrived, and the next request is a fresh hit.
- With Valkey stopped, the stub serves different bytes, and no announced digest, to two concurrent
  fetches of one artifact on two replicas: exactly one cache entry exists, every completed client
  response matches it, and a `proxy.upstream_digest_changed` event exists.
- A test over `internal/proxy` fails on an import of the Valkey client and on any `pg_advisory*` or
  `pg_try_advisory*` call; the proxy reaches the lock only through `cluster.FlightLock`.

### R-REPO-027 · Metadata freshness and refresh timeout
Priority: **MVP** · Covers: RMT-018, RMT-019

- A metadata entry is fresh for `metadataTtlSeconds` after `validated_at`; `0` means revalidate on
  every request. The upstream's own `Cache-Control` is ignored for this decision, so behavior follows
  the configuration alone.
- Revalidation MUST be conditional when the entry has validators: `If-None-Match` with the stored
  ETag, else `If-Modified-Since` ([RFC 9110, section 13](https://www.rfc-editor.org/rfc/rfc9110#section-13)).
  A 304 updates `validated_at` and `expires_at` without a body transfer. Because revalidation is a
  header exchange, collapsed across replicas (R-REPO-026) and bounded by the fetch limits
  (R-REPO-040), a period of `0` MUST be accepted; there is no minimum, unlike the catalog's 30
  seconds (Q-REPO-2).
- An entry validated after a request first looked it up counts as fresh for that request, whatever
  the period: its `validated_at` differs from the value the request read in R-REPO-025 step 4, or
  the entry was absent then. Both values come from PostgreSQL, so no replica clock takes part, and
  a waiter on a replica whose clock runs ahead is served like any other. Collapsed followers and
  cross-replica waiters that re-read the entry (R-REPO-026 steps 2 to 4) are therefore served by
  the flight they waited for; without this rule a period of `0` would make every waiter fetch
  again after the holder released the lock.
- If a refresh has not completed within `metadataRefreshTimeoutSeconds`, the stale entry MUST be
  served (`Cache-Status: trove; fwd=stale; detail=served-stale`); the refresh continues in the
  background, bounded by the read timeout, and updates the entry when it completes. This is the
  stale-if-error behavior of [RFC 5861](https://www.rfc-editor.org/rfc/rfc5861) applied to slow
  upstreams. The background refresh is the flight's fetch and keeps its lock (R-REPO-026). No later
  request depends on it: one that arrives before it completes finds the entry still expired and
  joins the flight or starts a new one, on whichever replica it reaches. On shutdown it is
  cancelled like any other fetch ([architecture.md](architecture.md) R-ARCH-024 step 4) and stores
  nothing.
- Format defaults: npm 7200 s; NuGet 300 s for the service index, version lists, registrations
  and vulnerability data ([formats/nuget.md](formats/nuget.md) Q-NUGET-2). The not-found default
  stays 1800 s for both formats (R-REPO-028).

Acceptance criteria (`metadataTtlSeconds: 60`):

- Two `npm view lodash` within 60 s: one upstream request. After 61 s: one conditional request with
  `If-None-Match`; the stub answers 304; the response carries `Cache-Status: trove; fwd=stale;
  fwd-status=304`.
- The stub delays the document by 90 s: the client receives the stale document after about 60 s;
  once the stub has answered, the next request is a fresh hit with the new content.
- With `metadataTtlSeconds: 0`, a `PATCH` is accepted; each of five sequential `npm view lodash`
  causes one conditional upstream request, and 20 concurrent ones, 10 per replica on two replicas,
  while the stub delays its answer by 1 s, cause one.

### R-REPO-028 · Negative cache
Priority: **MVP** · Covers: RMT-020

- An upstream 404 or 410 for (R, path, variant) MUST be remembered for `notFoundTtlSeconds`; during
  that period requests answer 404 without contacting the upstream. `0` disables negative caching. No
  other status is cached negatively (R-REPO-029). Only requests that passed the cache-fill check
  reach the upstream, so only they create entries.
- **Where entries live** ([architecture.md](architecture.md) Q-ARCH-16). A negative entry is shared
  ephemeral state, not a record (INV-3).
  - It is the Valkey key `trove:<ks>:neg:v1:<repo id>:<f>`, with `<f>` as in R-REPO-026, and the
    value `{"s": <404 or 410>, "c": <time the upstream request was sent, Unix ms>}`
    ([architecture.md](architecture.md) R-ARCH-030).
  - It is written with `SET … PX <notFoundTtlSeconds × 1000>` after the upstream has answered,
    outside every database transaction, and before the flight's holder releases the flight, so
    waiters on other replicas find it (R-REPO-026 steps 4 and 5).
  - Each replica keeps a bounded L1 copy of the entries it wrote or read
    (`proxy.negative_l1_entries`, least recently used out). A lookup asks the L1 first, then
    Valkey, and copies a Valkey hit into the L1.
  - Until 2026-10-28 negative entries were rows of the PostgreSQL table `remote_negative_entries`,
    with a cleanup job. Both are gone: the entries are small, written on the hot path and disposable,
    which is the state INV-3 assigns to Valkey.
- **Readers validate.** An entry counts only while all of the following hold:
  - `c` lies between now minus the repository's current `notFoundTtlSeconds` and now plus 5 s;
  - no invalidation of scope `negative` or `all` that covers its path (R-REPO-045) was created
    later than `c` minus 5 s.

  A malformed entry is ignored and deleted, and the key's own expiry is not trusted. The 5 s are the
  clock allowance of R-ARCH-030, applied in the invalidation's favour: an entry written in the
  seconds after an invalidation costs one more upstream request, while an entry from before it
  never survives it as long as replica and database clocks differ by less than 5 s. Because `c` is
  the time the request was sent, an upstream answer that crossed an invalidation does not survive
  it either.
- **Refusal only.** A negative entry can make trove answer 404 and nothing else. Nothing on this page
  admits a write, grants access or serves content because of one (R-ARCH-030). In particular, the
  shadowing check does not read negative entries (R-REPO-048).
- **Valkey unavailable**: negative caching continues per replica in the L1 alone, so each replica
  asks the upstream at most once per path and period ([ha-and-helm.md](ha-and-helm.md) R-HA-057).
  **Valkey lost its data**: one more upstream request per path. Neither case changes an answer that
  the upstream would give.
- Negative entries are never deleted to invalidate them: invalidations take effect in the readers
  (R-REPO-045), and a change of the upstream identity is such an invalidation (R-REPO-011). The
  entries of a deleted repository expire on their own; repository ids come from an identity column
  and are never handed out again (R-REPO-013).
- Requests refused by patterns or claims never reach the upstream and create no negative entry.
- Negative entries are not part of a backup. A restore starts without any, because it moves the
  installation to a new keyspace ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).

Configuration:

```yaml
proxy:
  negative_l1_entries: 100000  # default (proposed), per replica; about 10 MiB
```

Acceptance criteria:

- `npm view does-not-exist-4711` twice through `npm-remote`: one upstream request; the second
  response carries `Cache-Status: trove; hit; detail=negative`. With two replicas, the second request
  on the other replica causes no upstream request either.
- The stub then publishes the package; within the period it stays invisible; after an invalidation
  with scope `negative` (R-REPO-045), the next request on either replica reaches the stub and
  succeeds.
- With Valkey stopped, ten sequential requests for the missing path, five per replica, cause at most
  two upstream requests.
- `valkey-cli FLUSHALL` after the first request: the next request reaches the stub once and answers
  404.
- A negative entry written into Valkey with a creation time older than an invalidation of its path
  is ignored, and the request reaches the upstream.

### R-REPO-029 · Upstream failure handling and the assumed-offline circuit
Priority: **MVP** · Covers: RMT-029, RMT-033

Outcome classification:

| Upstream outcome | Counts toward the circuit | Metadata request | Artifact request |
|---|---|---|---|
| 2xx | resets it | verify, store, serve | verify, store, serve |
| 304 | resets it | refresh validity, serve stored | — |
| 404, 410 | no | negative entry; a stored document is marked `upstream_gone` (below) | negative entry; 404 |
| 401, 403 | no | stale if stored, else 502 `upstream_auth_failed` | 502 `upstream_auth_failed` |
| 429 | yes; `Retry-After` (capped at `circuitOpenSeconds`) opens the circuit at once | stale if stored, else 503 with `Retry-After` | 503 with `Retry-After` |
| 500, 502, 503, 504 | yes | stale if stored, else 502 `upstream_error` | 502 `upstream_error` |
| connection, DNS or TLS handshake failure (not certificate verification) | yes | stale if stored, else 502 `upstream_unreachable` | 502 `upstream_unreachable` |
| connect or read timeout | yes | stale if stored, else 504 `upstream_timeout` | 504 `upstream_timeout` |
| certificate verification, egress refusal, redirect violation, other 4xx, size limit, integrity failure | no | stale if stored, else 502 with the specific code | 502 with the specific code |
| unexpected content type (R-REPO-042) | yes | stale if stored, else 502 | 502 |

- Stale serving is bounded by `cache.maxStaleSeconds` when that is not `0`; beyond it the failure is
  returned. The bound counts from the end of the metadata period and applies to a failed or slow
  refresh and to an open circuit; offline mode, the cache view and a caller without the populate
  right serve any stored entry (Q-REPO-96).
- One immediate retry after 250 ms MUST be made for a connection failure that occurred before any
  request byte was sent; no other automatic retry happens inside a client request.
- **Upstream gone** (Q-REPO-3). When the upstream answers 404 or 410 for a metadata document that
  is stored, the entry MUST be marked `upstream_gone_at` instead of being deleted, a negative entry
  is written and the audit event `proxy.upstream_gone` is emitted on the first such answer. The
  document then answers 404 on the remote address, on its cache view and as a contribution to
  virtual merges. A later 2xx for the document clears the mark.
- **Gone packages.** When the marked document is the package document of package n (the path the
  format registers, R-REPO-003), n is gone from R: every request to R whose descriptor names n
  MUST answer 404, cached artifacts included, on R's address, on its cache view and as a member of
  every virtual repository (cache tier and upstream tier alike), and R asks its upstream for
  nothing of n but the package document (R-REPO-025, gone packages). Cached items are not deleted:
  they stay listed in the management item API and the cache entry list (R-REPO-032), where an
  operator can delete them or copy them into a local repository, which is how a build keeps a
  package the upstream withdrew legitimately. A 2xx for the package document clears the mark and
  serves everything again.
- Each replica MUST hold the gone packages of every remote repository in memory (they are few),
  loaded at start from the marked package-document entries. The marking or clearing transaction
  calls `cluster.Notify("upstream_state", …)` with the repository's id
  ([architecture.md](architecture.md) R-ARCH-018), and every replica then reloads that repository's
  gone packages; a replica whose change log has a gap reloads all of them. Serving a cached artifact
  therefore costs no extra query, and a mark reaches every replica within 2 s, with or without
  Valkey. `upstream_state` is a change area, not a signal area ([architecture.md](architecture.md)
  R-ARCH-018): gone marks and circuit transitions are rare, and only a change area keeps the 2 s
  bound while Valkey is unavailable.
- A format that tracks removal per version replaces both rules with its own: npm hides removed
  versions and their tarballs, and gone packages, unless `npm.serveRemovedVersions` is on
  ([formats/npm.md](formats/npm.md), R-NPM-026).
  - npm reads the core's `upstream_gone_at` of the package document's cache entry in the lookup
    that serves each request, and keeps no copy of it: neither the in-memory gone packages above
    nor a mark of its own.
  - npm serves a cached artifact only after its package document is current: unexpired, and
    validated after `system_state.restored_at`; otherwise the request revalidates the document
    first, in one flight ([formats/npm.md](formats/npm.md) R-NPM-025, Q-NPM-13; R-REPO-025 step 4).
    A lockfile install, which never requests documents, therefore sees a takedown within one
    metadata period, and while the upstream is reachable a restore cannot make a version servable
    again that the upstream withdrew after the snapshot.

  Formats that do not track versions leave a version withdrawn from a package that still exists
  reachable by exact path (H-5).
- **Circuit.** Each remote repository has a row in `remote_upstream_state`, in PostgreSQL, because
  every replica must take the same decision ([architecture.md](architecture.md) R-ARCH-030).
  - A counted failure increments `consecutive_failures` atomically (`UPDATE … RETURNING`), and only
    while the circuit is closed (`WHERE open_until IS NULL OR open_until <= now()`). At
    `failureThreshold`, `open_until` is set to now plus `circuitOpenSeconds`.
  - The writing replica takes the state from `RETURNING` at once. An `UPDATE` that returns no row
    found the circuit already open: that replica reads `open_until` and holds the open state at
    once, without waiting for the change log. The opening and the closing transaction call
    `cluster.Notify("upstream_state", …)` with the repository's id, so every replica holds the
    state in memory within 2 s, with or without Valkey, and reloads all of it after a gap in its
    change log. A burst of failures on many replicas therefore stops writing as soon as each replica has
    seen the opening, at the latest after its first statement that matches no row. The number of
    counted failures in flight at once is bounded by the fetch budget of R-REPO-040 in any case.
  - While open, no request reaches the upstream (R-REPO-025 step 7). After `open_until`, the first
    request on each replica probes, and the replica's other requests answer as while open until
    that probe ends; success closes the circuit for all replicas, failure reopens it. With n
    replicas, at most n probes run per period.
  - A success writes only when the counter is non-zero, so healthy traffic causes no writes.
    `circuitOpenSeconds: 0` disables the circuit.
- Any success resets the counter, so a flaky link cannot keep a repository offline. The catalog's ten
  background retries with growing pauses are replaced by this threshold and probe.
- The background requests of a mirror (R-REPO-053) neither count toward the circuit nor reset it, and never probe; they
  only read it and wait while it is open (R-REPO-054, Q-REPO-139). The table above classifies client requests.

Data model:

```sql
CREATE TABLE remote_upstream_state (
  repo_id              bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  open_until           timestamptz,
  last_failure_at      timestamptz,
  last_failure         text CHECK (length(last_failure) <= 1024),
  last_success_at      timestamptz
);
```

Acceptance criteria (`failureThreshold: 5`, `circuitOpenSeconds: 300`):

- The stub answers 503 five times in a row; the sixth request for an expired, stored document is
  served stale without an upstream request, on both replicas; a request for an uncached path
  returns 503 `upstream_assumed_offline` with `Retry-After`; `trove_remote_upstream_up` is 0 on
  both replicas within 2 s.
- After 300 s the next request probes; the stub answers 200; later requests reach the upstream
  normally.
- The stub answers 401: the client receives 502 `upstream_auth_failed`, the circuit stays closed,
  nothing is negatively cached.
- `nuget-remote` has `Acme.Gone` `1.0.0` cached; after its period, the stub answers 404 for the
  version list: `GET …/v3/flatcontainer/acme.gone/index.json` and `GET
  …/v3/flatcontainer/acme.gone/1.0.0/acme.gone.1.0.0.nupkg` return 404 through `nuget-remote`,
  through `nuget-remote~cache` and through `nuget-all` on both replicas within 2 s; the stub records
  no request for the `.nupkg`; a `proxy.upstream_gone` event exists; and the `.nupkg` item is
  still listed by the management item API.
- The stub then serves `Acme.Gone` again. After the negative period, a request for the `.nupkg`
  alone causes one stub request for the version list, clears the mark and is served.

### R-REPO-030 · Offline mode per repository
Priority: **MVP** · Covers: RMT-030

- `availability.mode: offline` MUST stop all upstream traffic of the repository. Stored entries are
  served regardless of expiry, expired metadata with `Cache-Status: trove; hit;
  detail=served-stale`; uncached requests answer 404 `not_cached_offline`. A 404 rather than a 5xx
  is deliberate: the operator decided the item is not available here, and clients should not retry.
- In virtual resolution an offline member contributes its cache tier and stored metadata only.
- Offline repositories appear in the inactive view (R-REPO-012).

Acceptance criteria:

- With `npm-remote` offline: `npm install lodash@4.17.21` (cached) succeeds; `npm install
  express@4.21.2` (uncached) fails with `E404`; the stub records no request.

### R-REPO-031 · Global offline switch
Priority: **P1** · Covers: RMT-031

- A system-wide switch MUST put every remote repository into the behavior of R-REPO-030, whatever its
  own mode. It is effective when either the configuration key `proxy.global_offline` is true or the
  runtime switch is on; the configuration key cannot be overridden through the API.
- The runtime switch is a database-backed setting with a revision ([api-and-ui.md](api-and-ui.md)
  R-API-012). The changing transaction calls `cluster.Notify("upstream_state", …)`
  ([architecture.md](architecture.md) R-ARCH-018), so the switch is effective on all replicas
  within 2 s, with or without Valkey. It is the kill switch for an incident such as a compromised upstream, which is
  why it does not wait for a configuration rollout, and why its bound must not depend on Valkey.
- `GET` returns `ETag: "<revision>"`, and `PUT` requires `If-Match` (R-API-012, Q-API-2): 428
  `precondition_required` without it, 412 `precondition_failed` when stale. The guard matters most
  when the switch is turned off: an administrator working from a view loaded before a second
  incident cannot end the new one unnoticed. To turn the switch on in an incident without a
  preceding `GET`, a script sends `If-Match: *`, the explicit blind write; the UI never does
  (R-UI-042).

| Method and path | Body | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/proxy-offline` | — | `{"enabled": false, "fromConfiguration": false, "reason": "", "changedAt": …, "changedBy": …}`, `ETag: "<revision>"` | `settings:read` |
| `PUT /api/v1/system/proxy-offline` | `{"enabled": true, "reason": "upstream incident 2026-10-28"}`, `If-Match` required | 200 with the new `ETag`; 409 `offline_by_configuration`; 412 `precondition_failed`; 428 `precondition_required`; audit `proxy.global_offline_changed` | `settings:write` |

Data model (one row; it arrives with this requirement):

```sql
CREATE TABLE proxy_offline_switch (
  singleton  boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
  enabled    boolean     NOT NULL DEFAULT false,
  reason     text        NOT NULL DEFAULT '',
  revision   bigint      NOT NULL DEFAULT 1,      -- ETag (R-API-012); revision column (R-BAK-022)
  updated_at timestamptz NOT NULL DEFAULT now(),  -- changedAt
  updated_by uuid                                 -- changedBy
);
```

`revision` MUST be registered with `meta.RegisterRevisionColumn`, so a restore raises it above
every `ETag` the old installation can have issued ([backup-and-restore.md](backup-and-restore.md)
R-BAK-022). Otherwise a `PUT` carrying an `ETag` read after the snapshot could pass `If-Match`
against the restored switch, which may hold a different state, and turn it off unnoticed.

Configuration:

```yaml
proxy:
  global_offline: false  # default (proposed)
```

Acceptance criteria:

- `curl -X PUT -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -H 'If-Match: *' -d '{"enabled":true,"reason":"upstream incident 2026-10-28"}' https://trove.example.com/api/v1/system/proxy-offline`:
  within 2 s no replica sends upstream requests, also with Valkey stopped; cached installs still
  succeed, and `GET` reports `enabled: true` with the reason and a new `ETag`.
- A `PUT` with `enabled: false` and the `ETag` read before that change answers 412 and the switch
  stays on; the same `PUT` without `If-Match` answers 428.
- With `proxy.global_offline: true` in the configuration, a `PUT` with `enabled: false` and the
  current `If-Match` returns 409 `offline_by_configuration`.

### R-REPO-032 · Cache view and cached content
Priority: **MVP** · Covers: RMT-003

- `<repoKey>~cache` addresses the cache view of remote repository `<repoKey>` of its project on the protocol and
  raw-file paths (R-REPO-002). It MUST serve stored entries only, never contact the upstream, ignore expiry
  and negative entries, and answer 404 for anything not stored, for documents marked upstream
  gone and for every entry of a gone package (R-REPO-029). It needs `read` on the remote
  repository and is the address for clients that must not trigger upstream checks.
- A request to the remote address with `Cache-Control: only-if-cached` MUST be answered from storage
  or with 504, as [RFC 9111, section 5.2.1.7](https://www.rfc-editor.org/rfc/rfc9111#section-5.2.1.7)
  prescribes.
- Cached artifacts are items: they are listed, carry their upstream URL, can be deleted (permanently,
  no trash can), annotated and copied to a local repository through the item operations of
  [storage.md](storage.md) (R-STOR-018 to R-STOR-020), for example to keep a package that
  disappeared upstream. Items of a gone package (R-REPO-029) stay listed and copyable; only
  serving them to clients stops.
- Metadata entries are listed for diagnosis by `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/cache/entries?pathPrefix=&kind=&limit=&cursor=`
  (path, variant, kind, fetched, validated, expires, upstream gone, last used, upstream URL), in the
  `items` and `nextCursor` envelope of [api-and-ui.md](api-and-ui.md) R-API-010, in the byte order of
  path and variant; permission `repositories:read-config` or `manage` on the repository, the
  exposure ceiling of the endpoint binding both. `kind` is `metadata` (the default) or `artifact`,
  `pathPrefix` limits the list to the entries that an invalidation of that prefix would cover
  (R-REPO-045), and a repository that is not remote answers `400 not_applicable` (Q-OPS-155).

Acceptance criteria:

- `npm view lodash --registry …/api/npm/web/npm-remote~cache/` returns the stored document even when it
  has expired, and the stub records no request; for an uncached package it fails with `E404`.
- `curl -H 'Cache-Control: only-if-cached' …/files/web/npm-remote/express/-/express-4.21.2.tgz` for an
  uncached tarball returns 504.

### R-REPO-033 · Upstream authentication and stored secrets
Priority: **MVP** · Covers: RMT-005, RMT-006, IAM-045

- `authMode: basic` MUST send `Authorization: Basic` with `username` and `password`
  ([RFC 7617](https://www.rfc-editor.org/rfc/rfc7617)) preemptively on every request to the upstream
  origin. `bearer` MUST send `Authorization: Bearer <password>`
  ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750)) and ignore `username`. `none` sends no
  credentials. Format-specific rules (which username the public npm registry expects) are on the
  format pages.
- **Token handshakes** ([Q-REPO-255](#q-repo-255--a-handshake-token-goes-to-the-upstream-origin-only-and-the-realm-is-asked-under-the-credential-rule),
  [Q-OCI-82](formats/oci.md#q-oci-82--the-engine-asks-the-format-before-a-request-with-the-last-challenge-so-only-the-first-request-of-a-replica-is-challenged)).
  A protocol that authenticates with a token service of its own (a container registry answering `401` with
  `WWW-Authenticate: Bearer realm=…`, R-OCI-021) is read by the format and carried out by the engine, which owns the I/O. The
  engine asks the format for the token request on a `401` and, once it has seen a challenge of the repository, before a request,
  with the last challenge it remembered for the repository's configuration version; the format takes the scope from the request
  and only the realm and the service from the challenge. Then it:
  - refuses an address that is neither the upstream origin nor an `extraHosts` entry (`502 foreign_host_denied`), one that leaves
    an `https` upstream for `http` (`502 insecure_redirect`), and one that names a user (`502 upstream_error`); nothing is sent and
    the service sees no request;
  - sends the request through the repository's client, so the credential goes to the token service by the rule below and by no
    other: to the upstream origin and to `extraHosts` entries with `sendCredentials: true`; an `extraHosts` entry without it is
    asked anonymously, and a repository with `authMode: none` sends none anywhere. The credential goes to the token service's host
    and to no redirect target of it, whatever `forwardAuthOnRedirect` says;
  - reads at most 64 KiB of the answer (`502 upstream_error` beyond) and hands a `2xx` answer to the format, which returns an
    `Authorization` value that is a valid header field value of at most 8 KiB; the original request is sent with it. That value goes
    to the upstream origin only: it replaces the repository's credential there, and it follows no redirect and goes to no
    `extraHosts` entry, whether or not the entry has `sendCredentials`;
  - keeps the value per replica, in memory, until 10 s before the lifetime the format read (60 s when it read none, at most one
    hour), and never in PostgreSQL or Valkey ([architecture.md](architecture.md) R-ARCH-032). The key is the repository, its
    configuration version, and a digest of the format's key (at most 1 KiB), the token service (scheme, host, port, path and the
    service it names) and the credential in effect, so a changed configuration, another token service and a rotated secret (which
    moves no configuration version) never meet a token obtained before;
  - treats the remembered challenge as a hint, never as the answer: when the step before a request fails (the format, the token
    service) the challenge is forgotten and the request is sent without a token, so that its own `401` runs the handshake with its
    own challenge; only a request to the upstream origin is sent with a token fetched before it; a challenge is remembered only
    from the origin and after the token it led to was accepted, for 5 minutes;
  - answers a `401` to a token the replica held with one fresh handshake, after dropping the rejected token (and only that one);
    a `401` to a token fetched for the request is followed by one handshake with the `401`'s own challenge when that names another
    key or token service (the registry moved it), and otherwise is the answer (`502 upstream_auth_failed`);
  - runs one request to the token service for the concurrent requests of a replica that need the same token; the request is not
    ended by the caller that started it, only by the engine's shutdown or after 30 s;
  - makes a failure of the token service the answer of the request, counting toward no circuit: `upstream_auth_failed` for its `401`
    or `403`, `upstream_rate_limited` (503) for `429`, `upstream_error` for `500`, `502`, `503` and `504`,
    `upstream_unexpected_status` for any other status, `egress_denied`, `tls_untrusted`, `upstream_timeout` or `upstream_unreachable`
    for a failure of the transfer. An egress or TLS refusal writes the security event of R-REPO-038 and counts
    `trove_remote_egress_denied_total` with the token service's host. For a mirror's request a rate-limit answer of the token service
    is the rate-limit answer of R-REPO-054;
  - runs no handshake for `authMode: bearer`: the stored secret is the token.
- Credentials go only to the upstream origin and to `extraHosts` entries with `sendCredentials:
  true`; redirects follow R-REPO-039. They travel over `https` only: a hop that is not `https` gets
  none, unless the upstream origin itself is `http` ([architecture.md](architecture.md) Q-ARCH-224).
  A format page MAY forbid more; neither MVP format does
  ([formats/npm.md](formats/npm.md) R-NPM-023 restates this rule, and the NuGet format proposes
  its `*.nuget.org` extra host with `sendCredentials: false`).
- **Secret fields** (`password`, sensitive header values, client-certificate private keys, outbound
  proxy passwords) take one of:
  - `{"value": "…"}`: accepted through the API only, never in startup files
    ([R-OPS-029](operations-and-observability.md)); sealed at once;
  - `{"secretRef": {"file": "<name>"}}` or `{"secretRef": {"env": "TROVE_CAC_<NAME>"}}`: resolved
    on every replica at use time under the rules of R-OPS-029 (a file directly under
    `config_as_code.secrets_dir`, only variables with the `TROVE_CAC_` prefix), so trove's own
    secrets can never be routed to an upstream. A changed file takes effect on each replica within
    10 s of the change, which keeps R-REPO-033's bound of 60 s (Q-REPO-89). The file content has its
    trailing line break removed and holds at most 8 KiB.
- The write and the unsaved upstream test of an Owner without `repositories:write-config` ([authorization.md](authorization.md)
  R-AUTHZ-026) give a secret as a value:
  a `secretRef` is refused (422 `secret_value_required`), except the reference the repository holds,
  sent back unchanged. A stored credential, sealed or referenced, stays only while the hosts it goes
  to stay: such a write that changes `url`, adds an `extraHosts` entry with
  `sendCredentials: true` or turns `forwardAuthOnRedirect` on carries a new value or no credential
  (422 `secret_value_required` otherwise,
  [Q-REPO-127](#q-repo-127--a-project-administrator-gives-an-upstream-secret-as-a-value-and-a-stored-one-stays-with-its-hosts)).
  A holder of `repositories:write-config` is not bound by it
  ([H-281](../security/data-and-secret-flow.md#h-281--a-holder-of-repositorieswrite-config-can-send-any-file-of-secrets_dir-to-an-upstream-of-their-choice)).
- Secret values MUST never be returned by any API, log line, audit event or error message. Reads show
  `{"set": true}` for sealed values and the reference itself for references. Clients that re-submit a
  document they read therefore cannot blank or leak a secret: `{"set": true}` keeps the stored value,
  an omitted or `null` secret removes it, and `{"set": true}` for a repository that has no stored
  value is 422 `invalid_secret` (Q-REPO-88). A new value reads like the old one, so a write that
  carries one moves `config_version` all the same: the clients built with the old value are replaced
  and the `ETag` changes.
- **Sealing** (Q-REPO-1). Inline values MUST be sealed with the one stored-secret mechanism of
  [architecture.md](architecture.md) R-ARCH-021 (IAM-045; the chart side is
  [ha-and-helm.md](ha-and-helm.md) R-HA-027). This page defines no key, format or rotation of its
  own. It relies on these properties of R-ARCH-021 and its tests check them for repository
  secrets: AES-256-GCM with a fresh 96-bit random nonce per value; associated data binding the
  ciphertext to table, primary key (here `repo_id` and `field`) and column, so a sealed value
  copied into another row or field does not decrypt; a key identifier in the sealed form, so keys
  rotate by re-sealing (the `secrets-reencrypt` task) while old keys still decrypt.
- A secret that cannot be unsealed or resolved makes fetches of that repository fail with 502
  `upstream_secret_unavailable`, before any request is sent, and is logged when the repository's
  client is built; other repositories are unaffected. A replica does not check every repository at
  start-up ([H-280](../security/data-and-secret-flow.md#h-280--a-secret-that-cannot-be-resolved-is-found-at-its-first-use)); the upstream
  test reports it at any time.
- The walk of the task `secrets-reencrypt` over this column reseals each value under the active key with the same row key, and counts a value that does not open as failed without touching it (`repository.ResealSecrets`).
- URLs in logs and audit events are written without user info and with query values redacted.

Data model:

```sql
CREATE TABLE repository_secrets (
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  field      text NOT NULL CHECK (field ~ '^/[A-Za-z0-9/._~-]{1,255}$'),  -- JSON pointer, e.g. '/remote/password', '/remote/headers/X-Api-Key'
  sealed     text,               -- inline value, sealed (R-ARCH-021 format v1:<key id>:<base64>)
  secret_ref jsonb,              -- {"file": …} or {"env": …}
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, field),
  CHECK ((sealed IS NULL) <> (secret_ref IS NULL))
);
```

Acceptance criteria:

- A remote with `authMode: basic` in front of a private stub registry: `npm install @partner/sdk`
  succeeds; the stub records `Authorization: Basic …` on every request to its origin; the read shows
  `"password": {"set": true}`.
- A `pg_dump` of the database contains no plaintext of the configured token.
- Copying the `sealed` value of `npm-partner`'s `/remote/password` row into the row of another
  repository makes that repository's fetches fail with 502 `upstream_secret_unavailable`.
- `{"secretRef": {"env": "TROVE_DATABASE_PASSWORD"}}` is refused with 422 `invalid_secret`.
- With `authMode: bearer`, the stub records `Authorization: Bearer <token>` and no Basic header.
- `PATCH` with `{"remote": {"password": {"value": "new"}}}` moves `config_version` and the next
  request, on every replica, carries the new token; `{"remote": {"password": null}}` removes the
  row; sending back a document that was read changes nothing.
- An Owner of project `web` without `repositories:write-config` patching the `url` of `partner`, whose sealed token an
  administrator of trove stored, gets 422 `secret_value_required` at `/remote/password` and the new
  host receives nothing; the same patch with `{"password": {"value": "own"}}` succeeds and the saved
  upstream test sends `own`. Its unsaved test with `{"secretRef": {"file": "partner-token"}}` answers
  422 `secret_value_required` and sends nothing.
- A `secretRef` file that disappears after the client was built fails the repository's next fetches
  with 502 `upstream_secret_unavailable` and sends no request; it works again within the refresh
  interval after the file is back.

### R-REPO-034 · Client TLS certificates for upstreams
Priority: **P1** · Covers: RMT-007

- The system keeps named client certificates: a PEM bundle with the certificate chain and an
  unencrypted private key (PKCS #8, PKCS #1 or SEC 1). Password-protected keys are refused with 422
  `encrypted_key_unsupported`, a key that does not match the leaf certificate with 422
  `key_mismatch`.
- A remote repository selects one by name; it is presented only on connections made for that
  repository to its upstream origin and its `extraHosts`.
- The private key is a secret field (R-REPO-033) and never returned.
- Deleting a certificate in use answers 409 `certificate_in_use` naming the repositories.
- Expiry is exported as `trove_client_certificate_expiry_timestamp_seconds{name}` so alerts can
  fire before upstream access breaks.

Data model:

```sql
CREATE TABLE client_certificates (
  name               text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  certificate_pem    text NOT NULL,
  private_key        text  NOT NULL,   -- sealed (R-ARCH-021 format)
  subject            text NOT NULL,
  issuer             text NOT NULL,
  not_before         timestamptz NOT NULL,
  not_after          timestamptz NOT NULL,
  sha256_fingerprint text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
```

API:

| Method and path | Body | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/client-certificates` | — | 200, name, subject, issuer, validity, fingerprint, referencing repositories | `settings:read` |
| `POST /api/v1/system/client-certificates` | `{"name": "partner-mtls", "pem": {"value": "-----BEGIN CERTIFICATE-----…"}}` | 201; 409 name exists; 422 | `settings:write` |
| `DELETE /api/v1/system/client-certificates/{name}` | — | 204; 404; 409 `certificate_in_use` | `settings:write` |

Acceptance criteria:

- A stub upstream that requires client certificates rejects the remote without one (502
  `upstream_tls_failed`) and serves it once `clientCertificate` names a valid certificate; the
  upstream test reports the presented subject.
- Uploading an encrypted PEM returns 422 `encrypted_key_unsupported`.

### R-REPO-035 · Trusted CA bundles
Priority: **P1** · Covers: RMT-008

- Outbound TLS for remote repositories (upstreams, extra hosts, redirect targets, outbound proxies
  reached over TLS) MUST verify the server certificate against the system roots, if enabled, plus
  every certificate in `proxy.outbound.tls.ca_files`. No per-repository switch disables verification.
- The bundle files are mounted from Kubernetes Secrets or ConfigMaps ([ha-and-helm.md](ha-and-helm.md))
  and MUST be reloaded within 60 s of a change without a restart; new connections use the new pool,
  open connections are not torn down. The reload is built in M0: the outbound clients read their trust
  store again at most every 10 s on use (`DefaultTrustCheckEvery`), so the bound holds with margin, and the
  object store's `storage.s3.ca_file` takes the same path ([R-STOR-030](storage.md)). The mechanism is
  described in [security/transport-encryption.md](../security/transport-encryption.md).
- A verification failure answers 502 `tls_untrusted`, is logged as a security event and does
  not count toward the circuit (R-REPO-029): a certificate that suddenly fails to verify may be an
  interception attempt, not an outage.
  The code is `tls_untrusted`, as in the result of the repository test, the event `proxy.tls_untrusted` and the registry (Q-REPO-251).
- TLS 1.2 is the minimum version.

Configuration:

```yaml
proxy:
  outbound:
    tls:
      use_system_roots: true                        # default (proposed)
      ca_files: []                                  # default (proposed)
      # ca_files: [/etc/trove/ca/corp-root.pem]   # example
      min_version: "1.2"                            # default (proposed)
```

Acceptance criteria:

- A remote to a stub whose certificate comes from a private CA fails with 502
  `tls_untrusted`; after the CA file is added to the mounted Secret, a request within 60 s
  succeeds without a pod restart.

### R-REPO-036 · Custom upstream request headers
Priority: **P1** · Covers: RMT-009

- `headers` is a list of `{"name", "value", "sensitive"}` sent on every request to the upstream
  origin. Names MUST be RFC 9110 tokens; values MUST NOT contain CR, LF or NUL; at most 32 headers of
  at most 8 KiB each.
- Refused names: `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, `TE`,
  `Trailer`, `Keep-Alive`, `Proxy-Authorization`, `Proxy-Connection`, `Cookie`. `Authorization` is
  accepted only with `authMode: none` and is then always sensitive.
- A sensitive value is a secret field (R-REPO-033).
- Custom headers are treated like credentials: they are not sent to other origins on redirects unless
  `forwardAuthOnRedirect` is on (R-REPO-039).

Acceptance criteria:

- With `{"name": "X-Api-Key", "value": {"value": "k-123"}, "sensitive": true}` the stub records the
  header on every request; the read shows `"value": {"set": true}`; a redirect to a second stub host
  carries no such header.
- `{"name": "Host", …}` returns 422 `invalid_header`.

### R-REPO-037 · Outbound HTTP proxies
Priority: **P1** · Covers: RMT-016, RMT-048, RMT-049

- The system keeps named outbound proxy definitions: proxy URL (`http://` or `https://`, host and
  port), optional username and password (a secret field, sent as `Proxy-Authorization: Basic` to the
  proxy only, never to targets), host patterns that bypass the proxy, and the scopes it is the
  default for: `repositories` (remote repositories) and `system` (every other service that uses the
  system outbound proxy, such as webhooks in [R-OPS-042](operations-and-observability.md) or GitLab
  discovery in [gitlab-ci-oidc.md](gitlab-ci-oidc.md)). At most one definition is the default per
  scope.
- The remote repository of an Owner without `repositories:write-config` takes `default`; `direct`, and a `named` proxy once it
  exists, stay an administrator's choice ([Q-REPO-128](#q-repo-128--a-project-administrators-remote-repository-takes-the-installations-outbound-path)).
- A remote repository's `outboundProxy.mode` is resolved per request: `direct` → no proxy, whatever
  the defaults; `named` → that definition; `default` → the definition that is default for
  `repositories`, else, with `proxy.outbound.proxy_from_environment: true`, the `HTTPS_PROXY`,
  `HTTP_PROXY` and `NO_PROXY` variables ([ProxyFromEnvironment](https://pkg.go.dev/net/http#ProxyFromEnvironment)),
  else direct. Because the default is resolved per request, changing it affects every repository in
  `default` mode at once and never rewrites stored configurations.
- NTLM proxy authentication is not supported; such proxies are used through an authenticating helper
  proxy (CNTLM, for example) configured as a plain HTTP proxy.
- Deleting a definition that a repository names answers 409 `proxy_in_use`.

Data model:

```sql
CREATE TABLE outbound_proxies (
  name           text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  proxy_url      text NOT NULL,
  username       text NOT NULL DEFAULT '',
  password       text,                 -- sealed (R-ARCH-021 format), or NULL with a reference in password_ref
  password_ref   jsonb,
  bypass_hosts   text[] NOT NULL DEFAULT '{}',
  default_scopes text[] NOT NULL DEFAULT '{}',   -- 'repositories', 'system'
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
-- one default per scope: enforced in the writing transaction
```

API:

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/system/outbound-proxies` | 200, list with `password: {"set": true}` or the reference, and referencing repositories | `settings:read` |
| `POST /api/v1/system/outbound-proxies` | 201; 409 name exists or scope already has a default; 422 | `settings:write` |
| `PATCH /api/v1/system/outbound-proxies/{name}` | 200 (merge patch) | `settings:write` |
| `DELETE /api/v1/system/outbound-proxies/{name}` | 204; 409 `proxy_in_use` | `settings:write` |

```json
{
  "name": "corp-egress",
  "proxyUrl": "http://egress.corp.example:3128",
  "username": "trove",
  "password": { "secretRef": { "file": "corp-egress-password" } },
  "bypassHosts": ["*.corp.example"],
  "defaultScopes": ["repositories", "system"]
}
```

Configuration:

```yaml
proxy:
  outbound:
    proxy_from_environment: false # default (proposed)
```

Acceptance criteria:

- With `corp-egress` as default for `repositories`, a new remote in `default` mode reaches its
  upstream through the proxy (the proxy log shows a `CONNECT`); a remote in `direct` mode does not.
- Removing the default role from `corp-egress` makes `default`-mode remotes go direct without any
  change to them.

### R-REPO-038 · Egress address rules for remote repositories
Priority: **MVP** · Covers: trove-only

- Every connection made for a remote repository (upstream, extra hosts, redirect targets, URLs taken
  from upstream metadata, outbound proxies, the advisory provider of an npm audit forward,
  [formats/npm.md](formats/npm.md) R-NPM-038) MUST check each resolved IP address inside the dialer's
  `Control` hook, after DNS resolution and before connecting, so a DNS answer that changes between
  check and use cannot pass. Addresses in `allow_cidrs` are permitted even inside a denied range;
  addresses in a denied range are refused; everything else is permitted.
- Denied by default: loopback, unspecified, link-local (IPv4 and IPv6, which includes the
  169.254.169.254 cloud metadata endpoints), the IPv6 metadata address `fd00:ec2::254`, the metadata
  addresses `100.100.100.200` and `168.63.129.16`, the deprecated IPv4-compatible range `::/96`, multicast and
  broadcast, and the IPv4-mapped IPv6 forms of all of these (ranges per the IANA
  [IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and
  [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) special-purpose registries).
- Before any CIDR rule runs: a zoned IPv6 address (`fe80::1%eth0`, `::1%1`, any address with a zone) is refused,
  whatever `allow_cidrs` says, because a zone scopes an address to an interface of this host and no prefix
  contains a zoned address ([Q-REPO-10](#q-repo-10--zoned-ipv6-and-the-nat64-prefix-in-the-egress-rules)).
  An IPv4-mapped IPv6 address is checked as the IPv4 address it maps to. An address of the NAT64 prefix
  `64:ff9b::/96` that no rule decides is checked as the IPv4 address in its last 32 bits, so
  `64:ff9b::a9fe:a9fe` is refused like `169.254.169.254`. The NAT64 prefix is the well-known one; a
  network-specific prefix is not checked ([H-78](../security/egress.md#h-78--the-nat64-check-knows-only-the-well-known-prefix)).
- Private ranges ([RFC 1918](https://www.rfc-editor.org/rfc/rfc1918)), shared address space and
  unique-local IPv6 are permitted by default, unlike for webhooks
  ([R-OPS-042](operations-and-observability.md)): remote URLs are set by repository administrators,
  and internal registries are the common case. A second layer is the cluster operator's egress policy: the Helm chart
  renders ingress rules only ([ha-and-helm.md](ha-and-helm.md) R-HA-042, ADR-154;
  [H-516](../security/egress.md#h-516--the-chart-restricts-no-egress-of-its-pods)).
- A refused connection answers 502 `egress_denied`, is logged as a security event and does not count
  toward the circuit.
- Through an outbound proxy, trove resolves and checks the target name itself before sending, but
  the proxy connects on its own (H-6).

Configuration:

```yaml
proxy:
  outbound:
    address_policy:
      deny_cidrs:                 # default (proposed): the ranges listed above
        - 127.0.0.0/8
        - ::1/128
        - 0.0.0.0/8
        - ::/128
        - 169.254.0.0/16
        - fe80::/10
        - fd00:ec2::254/128
        - ::/96
        - 100.100.100.200/32
        - 168.63.129.16/32
        - 224.0.0.0/4
        - ff00::/8
        - 255.255.255.255/32
      allow_cidrs: []             # default (proposed)
      # allow_cidrs: [127.0.0.1/32]  # example: a local sidecar registry
```

Acceptance criteria:

- A remote with upstream `http://169.254.169.254/latest/` returns 502 `egress_denied` on the first
  request, and the upstream test reports the same.
- A stub document that points an artifact at `http://127.0.0.1:9000/x.tgz` cannot make the server
  connect there (502 `egress_denied`).
- A host name that resolves to `10.20.0.5` is reachable by default; `::ffff:127.0.0.1` is refused.

### R-REPO-039 · Upstream redirects
Priority: **MVP** · Covers: RMT-012

- Redirects (301, 302, 303, 307, 308; [RFC 9110, section 15.4](https://www.rfc-editor.org/rfc/rfc9110#section-15.4))
  MUST be followed up to `proxy.max_redirects` hops; more answer 502 `too_many_redirects`.
- A redirect from `https` to `http` MUST be refused with 502 `insecure_redirect`; so is a URL in
  the metadata of an `https` upstream that points to `http` (R-REPO-043).
- Every hop passes the egress rules (R-REPO-038) and TLS verification (R-REPO-035). A format page MAY
  restrict redirect targets further (npm: only hosts it accepts for tarballs).
- Credentials, the client certificate and custom headers MUST be sent only to the upstream origin and
  to `extraHosts` with `sendCredentials: true`. With `forwardAuthOnRedirect: true` they also go to the
  other origins of the redirect chain. A hop that is not `https` gets none of them, unless the
  upstream origin itself is `http` (Q-ARCH-224). The default matters: many registries redirect
  downloads to pre-signed object-storage URLs, which reject an extra `Authorization` header and must
  not receive the credential anyway.
- The client certificate follows the same rule as the credentials: it is presented only on connections to
  those hosts. The first request to a host that is neither the upstream origin nor a `sendCredentials`
  host never carries credentials, also with `forwardAuthOnRedirect`, which widens redirect hops only
  (Q-ARCH-199).
- The entry is stored under the original request key; the final URL is recorded as `upstream_url`.
  Redirect responses themselves are not cached.

Configuration:

```yaml
proxy:
  max_redirects: 5  # default (proposed)
```

Acceptance criteria:

- A stub answers the tarball request with 302 to a second stub host: the tarball is cached and the
  second host records no `Authorization` header; with `forwardAuthOnRedirect: true` it does.
- Six chained redirects return 502 `too_many_redirects`; a redirect to `http://` returns 502
  `insecure_redirect`.

### R-REPO-040 · Size and concurrency limits
Priority: **MVP** · Covers: trove-only

- An artifact larger than the effective `maxArtifactBytes`, or a metadata document larger than
  `maxMetadataBytes`, MUST be refused: at once when `Content-Length` announces it, otherwise by
  aborting when the limit is crossed. Nothing is stored; the client gets 502
  `upstream_item_too_large`; nothing is negatively cached, so raising the limit takes effect at once.
  The repository value applies when set, else the system value.
- **Fetch slots.** Two limits apply to every upstream fetch, shadowing-check probes included
  (R-REPO-048):
  - `proxy.max_concurrent_fetches` protects the replica itself and counts per replica. A fetch counts from the moment
    it takes its slot until it ends, its verification, the format's inspection and its commit included: that is the work
    the replica does for it.
  - `limits.maxConcurrentFetches` protects the upstream. A fetch counts from the moment it takes its slot until its
    upstream exchange ends (below), not while trove stores what it received. It is a budget for the whole installation
    (Q-REPO-8), divided among the replicas without coordination between them. Each replica allows
    `ceil(maxConcurrentFetches ÷ n)` fetches of the repository at a time. n is the number of live
    replicas that fill client misses in the replica registry
    ([operations-and-observability.md](operations-and-observability.md) R-OPS-003), at least 1,
    refreshed with the registry heartbeat: `packages` and its pools after the cut into roles, `web` before it and in the
    upgrade bridge, or `all` in the local profile ([architecture.md](architecture.md) Q-ARCH-297). `api`
    replicas fill nothing and are not counted, and neither are `worker` replicas. The count is `cluster.FillCount`
    ([internal/cluster/replica_roles.go](../../internal/cluster/replica_roles.go)), a count of its own that the proxy's
    slots read (`proxy.Options.Replicas`); the limiters of the client listener count the replicas of their own role and
    pool instead (R-ARCH-034), so the two numbers differ by design (Q-OPS-165).
  - The cluster-wide total therefore stays within `maxConcurrentFetches + n − 1` whatever the
    replica count, once every replica has read the current n, which after a scale event takes one
    heartbeat. A budget smaller than n admits one fetch per replica. Valkey plays no part, so the
    bound holds while Valkey is unavailable.
- The upstream test of R-REPO-024 is not a fetch and takes no slot: it sends one request of the format's probe resource per
  call (verified: `internal/proxy/upstream/probe.go`), bounded by the management API's rate-limit classes. The bound below
  therefore holds plus the tests running at that moment (Q-ARCH-297).
- A slot is taken by the request that fetches, after it holds the flight or has gone on without
  the cross-replica lock (R-REPO-026 step 3). Its two parts are returned at different moments
  (Q-REPO-235):
  - the repository's share when the **upstream exchange** ends: the response body has been read to its end into the
    spool (R-STOR-004), or the response has no body the fetch reads (a `304`, a `404` or `410`, a refusal before the
    first byte), or the request failed or was cancelled;
  - the replica's slot when the fetch ends, after verification (R-REPO-041), the format's inspection, the commit and the
    entry, negative entry or gone mark it writes.

  A fetch that is storing what it received therefore holds no share of the upstream's budget, and a slow commit does
  not keep the next fetch of the repository from the upstream; the replica still counts it, and the flight still ends
  only after the commit, so whoever waits for the flight (R-REPO-026) is answered from the committed entry as before.
  Requests that fetch outside a flight, shadowing-check probes and pass-through artifact requests (R-REPO-044), take
  theirs directly; a request that stores nothing returns both parts when it ends. In-process followers and
  cross-replica waiters take none, so a replica's share is never spent on waiting for another replica's transfer. A
  fetch that finds no free slot waits up to `proxy.queue_timeout_seconds`, then answers 503 with `Retry-After: 5` and
  releases its flight with `failed`. This bounds the burst a large invalidation can send to the upstream.
- Spool admission and its failure answer are those of [R-STOR-004](storage.md).
- The background fetches of a mirror take a lower-priority share of the same budget, which client fetches pre-empt
  (R-REPO-055); the shares of client-serving replicas above are not reduced by it.

Configuration:

```yaml
proxy:
  max_artifact_bytes: 10737418240 # default (proposed), 10 GiB
  max_metadata_bytes: 268435456   # default (proposed), 256 MiB
  max_concurrent_fetches: 256     # default (proposed), per replica
  queue_timeout_seconds: 10       # default (proposed)
```

Acceptance criteria:

- With `limits.maxArtifactBytes: 1048576`, a 2 MiB tarball returns 502 `upstream_item_too_large` and
  leaves no item; after the limit is raised, the same request succeeds at once.
- With `limits.maxConcurrentFetches: 4` on two replicas and a slow stub, 20 concurrent requests for
  distinct uncached tarballs, 10 per replica, keep at most 4 upstream connections open in total and
  at most 2 per replica. After a third replica has registered, the same load keeps at most 6 open
  (`ceil(4 ÷ 3)` = 2 per replica).
- With `limits.maxConcurrentFetches: 4` on two replicas, replica A leads two slow fetches. Replica B
  receives requests for the same two tarballs and for two other uncached tarballs: B fetches the
  other two at once, without a queue wait, because waiting for A's flights takes no slot.
- With `limits.maxConcurrentFetches: 1` on one replica and the commit of a fetched tarball held back on purpose, a
  request for a second uncached tarball of the repository reaches the upstream while the first commit is still held,
  and its upstream record shows a `slot_wait_ms` below the first tarball's transfer time; once the commit is let go, both
  tarballs are stored once and both clients get them whole. A held-back document fill behaves the same.
- With `limits.maxConcurrentFetches: 2` on one replica, every commit held back and 20 concurrent requests for distinct
  uncached tarballs, the upstream serves all 20 before any commit is let go and never has more than 2 open at once.
  With `proxy.max_concurrent_fetches: 3` in addition, the upstream serves 3 of them and the fourth waits for a slot until
  a commit is let go: the replica's slot counts a fetch until its store ends.

### R-REPO-041 · Integrity verification against upstream digests
Priority: **MVP** · Covers: RMT-022

- The format plug-in supplies the digests the upstream announces for an artifact (npm: the `dist`
  integrity value, [Subresource Integrity](https://www.w3.org/TR/SRI/) form, else its SHA-1; NuGet:
  per [formats/nuget.md](formats/nuget.md); content-addressed protocols: the digest in the reference).
  Digests are computed while spooling ([R-STOR-002](storage.md)) and compared at the end.

| `integrity.checksumPolicy` | Behavior |
|---|---|
| `verify-if-available` (default) | A mismatch refuses the artifact. Without an announced digest the computed digests are recorded and the artifact is accepted. |
| `require` | Also refuses an artifact without an announced SHA-256 or stronger digest. A format whose upstream announces none (NuGet before R-NUGET-029) declares the policy unsupported in the catalog (`ChecksumRequireUnsupported`); creating or updating a remote repository of it with `require` answers 422 `not_applicable` at `/remote/integrity/checksumPolicy` (Q-ARCH-269). |
| `ignore` | Announced digests are not compared. |
| `pass-through` | As `ignore`; for formats with separate checksum files, the upstream's checksum files are served unchanged. |

- A format page MAY make its rule stricter than the policy and say so (npm always verifies and
  refuses `ignore` and `pass-through`, R-NPM-025, Q-NPM-79). Digests that are part of a protocol's addressing are always verified.
- **Withheld tail.** The leader MAY stream the body to its own client while fetching, but MUST withhold
  the final 64 KiB until verification has passed and, when the artifact is stored, until its entry
  has committed: a duplicate flight on another replica may have committed different bytes first,
  and the upsert then refuses this one (R-REPO-026). On a mismatch it MUST abort the client connection
  without completing the message (HTTP/1.1: close before the announced length or the final chunk;
  HTTP/2: reset the stream), so no client receives a complete response with unverified content
  ([RFC 9112, section 8](https://www.rfc-editor.org/rfc/rfc9112#section-8)). The leader streams
  only when the upstream announced the length of the body, and the status line goes out with the
  first byte beyond the withheld tail, so an error before that is a plain 502 (Q-REPO-94). A body
  whose announced length fits in the withheld tail is not streamed: the stored entry answers the
  leader like a waiter, with every header of a stored answer; a streamed answer carries the headers
  of a stored answer that do not depend on the body (`Accept-Ranges`, `Cache-Control`), not its
  validators ([formats/nuget.md](formats/nuget.md) Q-NUGET-86). Nothing is stored;
  collapsed followers get 502 `upstream_integrity_mismatch`; a security event is logged; nothing is
  negatively cached.
- **Immutable stays immutable.** When refreshed metadata announces, for an artifact already cached, a
  digest different from the cached one, the server MUST keep the cached artifact, MUST NOT replace it,
  and MUST write the security audit event `proxy.upstream_digest_changed` naming repository, path and
  both digests. Clients then fail their own integrity check loudly instead of silently receiving
  different bytes for the same version; an operator resolves the case by deleting the cached item.
  Package clients assume that one package ID and version always stand for the same content
  ([NuGet implementation guide](https://learn.microsoft.com/en-us/nuget/api/implementation-guide)).

Acceptance criteria:

- The stub serves a tarball whose bytes do not match the document's `integrity` value: `npm install`
  fails, no item exists afterwards, and `trove_remote_integrity_failures_total` increments.
- With `require`, an artifact whose metadata announces only SHA-1 is refused with 502
  `upstream_digest_missing`.
- The stub changes the `integrity` of an already cached version: the cached tarball keeps being served
  unchanged, `npm ci` fails with `EINTEGRITY`, and a `proxy.upstream_digest_changed` event exists.

### R-REPO-042 · Unexpected content types
Priority: **MVP** · Covers: RMT-023

- With `integrity.blockUnexpectedContentTypes: true`, a response whose media type is in
  `proxy.blocked_content_types` MUST be refused with 502 `upstream_content_type_blocked` and not stored,
  unless the format plug-in declares that type expected for the request (HTML index pages of the PyPI
  simple API, for example) or the repository lists it in `integrity.allowedContentTypes`.
- The refusal counts toward the circuit (R-REPO-029): an HTML page in place of a package is nearly
  always a captive portal, a login page or an intermediary's error page, which is an availability
  problem.

Configuration:

```yaml
proxy:
  blocked_content_types: # default (proposed)
    - text/html
    - application/xhtml+xml
```

Acceptance criteria:

- The stub answers a tarball request with 200 and `Content-Type: text/html`: the client receives 502
  `upstream_content_type_blocked`, no item is stored, and the failure counter increments.

### R-REPO-043 · Metadata URL rewriting
Priority: **MVP** · Covers: trove-only

- In every metadata document served, the format plug-in MUST rewrite the URLs of resources clients
  fetch next (npm tarball URLs; NuGet resource, registration and package content URLs) so they point
  to the address the document was requested through: the remote repository, its cache view, or the
  virtual repository. Other URLs (home pages, source links) stay unchanged. Artifacts are never
  modified.
- Rewriting MUST happen when a document is served, not when it is stored, using the base URL of
  [operations-and-observability.md](operations-and-observability.md) (including its additional base
  URLs and trusted proxies), which on a further endpoint is that endpoint's response base URL
  ([endpoints.md](endpoints.md) R-EP-003). The stored upstream document stays unchanged, so a base URL change needs
  no cache flush and one host name's URLs never leak into another's responses.
- A rewritten path maps back to its upstream URL as follows: if the upstream URL equals the one the
  format derives from `url` and the canonical path, nothing is recorded; otherwise the mapping is
  recorded in `remote_url_map` when the document is parsed. The server MUST NOT derive an upstream
  host from a client request path: no client can choose the host trove fetches from.
  The engine reads the map for a metadata path as it does for an artifact (a page of a listing that the index does
  not inline: the registration pages of NuGet), and applies the host rule below to the recorded URL. A format whose
  addressing follows a discovery document gets that document resolved first (R-NUGET-014, Q-ARCH-268).
- The map follows the stored document: a fill that kept the stored document, or whose upsert
  lost to a newer one (R-REPO-026), records no mapping, so the map and the document always
  describe one document (Q-REPO-119, R-NPM-024).
- A document MUST record mappings only for canonical paths of its own package. The format builds
  the path from the package identity the document was fetched for (name or ID, version, file name),
  never from the path of the upstream URL, and ignores and counts an entry that names another
  package. A document of package A can therefore neither create nor replace the mapping of an
  artifact of package B.
- A recorded URL on a host that is neither the upstream origin nor in `extraHosts` MUST be refused
  with 502 `foreign_host_denied` without a request. A recorded URL that leaves an `https` upstream
  for `http` is refused with 502 `insecure_redirect`, as a redirect to `http` is (R-REPO-039,
  [architecture.md](architecture.md) Q-ARCH-224). `remote.extraHosts` is the only host list;
  formats have none of their own and may only narrow it (Q-REPO-5).
- The ETag of a rewritten document is derived from the stored document's digest and the serving
  address, so client conditional requests work.

Data model:

```sql
CREATE TABLE remote_url_map (
  repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  path         text COLLATE "C" NOT NULL,
  upstream_url text NOT NULL,
  recorded_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, path)
);
```

Acceptance criteria:

- `npm view lodash dist.tarball --registry https://trove.example.com/api/npm/web/npm-all/` prints
  `https://trove.example.com/api/npm/web/npm-all/lodash/-/lodash-4.17.21.tgz`.
- A request with `Host: evil.example` returns a document whose tarball URLs still name
  `trove.example.com`, and the next client sees the same.
- A stub document whose tarball lives on `cdn.partner.example` fails with 502 `foreign_host_denied`
  until `extraHosts` contains `cdn.partner.example`; afterwards the tarball is fetched from there and
  served under the trove path.
- A stub document for package `evil` whose version entry names a tarball URL of `lodash` records a
  mapping only for `evil`'s own canonical path; `remote_url_map` holds no row for any `lodash`
  path afterwards.

### R-REPO-044 · Pass-through mode
Priority: **P1** · Covers: RMT-017

- With `cache.storeArtifacts: false`, artifacts MUST be streamed from the upstream to the client
  without being stored; verification and the withheld tail (R-REPO-041) still apply. Metadata is
  cached as usual, because rewriting and freshness depend on it. Negative caching, the circuit and
  offline modes apply; uncached artifacts are unavailable while the upstream cannot be asked.
- Artifact requests are not collapsed across clients (nothing is shared afterwards), and no items are
  created, so browsing, properties, copy and cleanup do not apply to them.
- A format whose plug-in requires stored artifacts refuses the setting with 422 `validation_failed` with `not_applicable` in `errors[]`.
- Use case: a replica group next to another trove instance that already caches the upstream.

Acceptance criteria:

- With pass-through on, two installs of the same tarball cause two upstream artifact requests and
  leave no item; the package document is fetched once within its period.

### R-REPO-045 · Cache invalidation
Priority: **MVP** · Covers: RMT-026, ART-050

The operation surface is [R-OPS-021](operations-and-observability.md): `POST
/api/v1/projects/{projectKey}/repositories/{repoKey}/cache/invalidate` with `{"path": "<prefix>", "scope": "metadata" |
"negative" | "all"}`, answer `200 {"invalidated": n}`, permission `manage` on the repository
([R-AUTHZ-008](authorization.md)), audit `repository.cache_invalidated`. It is needed in the MVP,
because RMT-026 is. This requirement specifies its semantics and mechanism.

- Invalidation MUST make the covered metadata entries (scope `metadata`), the covered negative entries
  (`negative`) or both (`all`) count as expired, in constant time regardless of cache size. Artifacts
  are never invalidated, and nothing is deleted.
- A prefix covers a canonical path when it is empty, equal to the path, or equal to its leading
  segments: `@acme` covers `@acme/util` but not `@acmecorp/util`. A prefix is empty or a sequence of
  segments without a leading or trailing slash, without an empty segment, without `.` or `..` as a
  segment, valid UTF-8 without control characters, and at most 256 bytes; anything else answers `422
  validation_failed` with the pointer `/path` (Q-REPO-100).
- **Mechanism.** An invalidation is a row (repository, prefix, scope, time). Its time is taken inside
  the inserting transaction as its first statement, not at the start of the transaction (Q-REPO-101).
  A metadata entry is invalid when an invalidation of its repository with a covering prefix and a
  matching scope is newer than its `validated_at`. A negative entry is invalid under the rule of R-REPO-028: its time is its
  creation time `c`, compared with a 5 s allowance in the invalidation's favour.
  - Each replica keeps the invalidations in memory, loaded at start. The inserting transaction
    calls `cluster.Notify("cache_invalidation", …)` with the repository id
    ([architecture.md](architecture.md) R-ARCH-018), so an invalidation is in force on every
    replica within milliseconds with Valkey and within `cluster.epoch_poll_interval` without.
    The change carries the whole row, so a replica applies it without a query, and a request
    asks the in-memory set only: the cost of a lookup is the number of segments of the path,
    whatever the number of rows (Q-REPO-102; the payload: Q-OPS-154).
    Until 2026-10-28 this was `NOTIFY trove_cache_invalidation`; trove issues no `NOTIFY` any
    more ([architecture.md](architecture.md) Q-ARCH-15).
  - A background job on the `worker` role later folds each invalidation into the metadata entries
    validated before it, in batches, setting `expires_at` to the invalidation time and, when the
    last batch is done, `folded_at` on the row (Q-REPO-87). An entry
    refreshed after the invalidation keeps its own expiry, and no entry gets a later expiry than it
    had. Invalidating a million entries is therefore one insert, not a long `UPDATE`. The task is
    `proxy-invalidation-fold` ([operations-and-observability.md](operations-and-observability.md)
    R-OPS-019, Q-OPS-153); Q-REPO-103 has the batching and the last check before a deletion.
  - Negative entries live in Valkey and are never deleted to invalidate them. Readers apply the
    invalidation instead, so a row of scope `negative` or `all` MUST stay until no negative entry it
    covers can still count: 86,405 s after its creation, the largest `notFoundTtlSeconds` plus the
    clock allowance. A row of scope `metadata` is deleted once it is folded and one hour has passed
    since the fold, after one more fold pass that finds an entry a fill stored late (Q-REPO-103).
  - The job deletes a row of scope `negative` or `all` once it is folded and that period has
    passed (after the same last fold pass). Its deletions call `cluster.Notify("cache_invalidation", …)` with the repository id
    too, so every replica drops deleted rows from memory. The job deletes no negative entry: they
    are in Valkey and expire on their own (R-REPO-028). A replica never drops a row the database still
    holds: an `all` row whose fold is late would otherwise let unfolded metadata entries count as
    fresh again. The in-memory set therefore stays bounded by the rows in PostgreSQL, however often
    invalidations are made.
  - The `invalidated` count is the number of covered metadata entries at the time of the call, those
    marked upstream gone included; negative entries in Valkey are not counted, because counting them
    would need a scan of the keyspace, so the scope `negative` answers 0 (Q-REPO-101).
- Each entry is refreshed on its next request under R-REPO-027. A failed refresh serves the stale
  copy, which can show an old package list; the UI therefore shows the circuit state next to the
  result and tells the operator to verify the refresh.
- On a virtual repository, the merged entries under the prefix are dropped (R-REPO-020), on every
  replica through `cluster.Notify("cache_invalidation", …)` with the virtual repository's id and
  the prefix, as for a reindex (R-REPO-016); on a local repository the call answers 400.

Data model:

```sql
CREATE TABLE remote_cache_invalidations (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  prefix     text COLLATE "C" NOT NULL,   -- '' = whole repository
  scope      text NOT NULL CHECK (scope IN ('metadata', 'negative', 'all')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid,
  folded_at  timestamptz                  -- set by the fold job (Q-REPO-87)
);
CREATE INDEX remote_cache_invalidations_repo ON remote_cache_invalidations (repo_id);
CREATE INDEX remote_cache_invalidations_unfolded ON remote_cache_invalidations (created_at) WHERE folded_at IS NULL;
```

Acceptance criteria:

- With 1,000,000 cached metadata entries, an invalidation with an empty path returns within one second,
  and the next `npm view` of any cached package causes a conditional upstream request on every
  replica. With Valkey stopped, the same holds for requests that start `cluster.epoch_poll_interval`
  plus 1 s after the call returned.
- With `notFoundTtlSeconds: 86400`, a negative entry for `@acme/late` exists, and `@acme` is
  invalidated with scope `negative`. After the background job has run and a test clock has moved one
  hour ahead, the entry still does not count, and the next request reaches the upstream.
- Invalidating `@acme` refreshes `@acme/util` on its next request but not `@acmecorp/util`.
- With the stub down, a request after invalidation returns the stale document with
  `detail=served-stale`.

### R-REPO-046 · Unused cache cleanup setting
Priority: **P1** · Covers: RMT-021

- `remote.cache.unusedRetentionHours` (`0`: keep forever) is the per-repository setting that the
  remote-cache cleanup job of [R-STOR-045](storage.md) applies. That job, its schedule and its manual
  start belong to [storage.md](storage.md); removal is permanent (no trash can), since the upstream
  can supply the content again.
- Use of a cached artifact is its item's last download, or its creation if it was never downloaded
  ([R-STOR-015](storage.md)). Downloads through a virtual repository and through the cache view MUST
  count as downloads of the member's item.
- The same job MUST remove metadata entries whose `last_used_at` is older than the period. Replicas
  record metadata use in memory and write `last_used_at` in batches at most once per minute, and only
  when the stored value is more than one hour older, so serving documents causes no per-request
  writes. No request reads that buffer; it is flushed at shutdown, and a crash loses at most one
  batch, which the entry's next use records again (R-REPO-049).
- To keep an item permanently, copy it to a local repository.

Acceptance criteria:

- With `unusedRetentionHours: 1`, a tarball last downloaded two hours ago through `npm-all` is
  removed by a cleanup run and fetched from the upstream again on the next install; a tarball
  installed ten minutes ago stays.

## Mirror mode

A remote repository runs in one of two modes. `pullThrough`, the default, is everything above: the remote fetches what
clients ask for and nothing else. `mirror` keeps all of that and adds background passes, jobs of the job framework
([architecture.md](architecture.md) R-ARCH-016) on their own queue `mirror`, that discover the packages the upstream offers
within the remote's bounds and store them before a client asks (owner, 2026-11-02;
[ADR-151](../decisions.md#adr-151--a-remote-repository-can-mirror-its-upstream-in-the-background)). Clients are served by
R-REPO-025 in both modes; the mirror only fills the same cache earlier. This section specifies the mode and its settings
(R-REPO-050), what a mirror discovers (R-REPO-051), its storage bounds (R-REPO-052), the passes (R-REPO-053), upstream
throttling (R-REPO-054), the priority of clients (R-REPO-055), status and operations (R-REPO-056), observability
(R-REPO-057), replicas, Valkey, drains and backups (R-REPO-058) and the test suites (R-REPO-059). What "discoverable" means
for each format is on the format pages: [formats/npm.md](formats/npm.md#mirror-discovery) and
[formats/nuget.md](formats/nuget.md#mirror-discovery).

### R-REPO-050 · The mirror mode and its settings
Priority: **MVP** · Covers: API-082, API-083

- `remote.mode` is `pullThrough` (the default) or `mirror`. Every other remote setting of R-REPO-024 applies in both
  modes: the upstream, its credentials and egress rules, the patterns, the cache periods, the circuit, offline mode and the
  limits.
- The mirror's settings are the members of `remote.mirror`. They are present in the canonical section in both modes, with
  their defaults where the request omitted them, and take effect only in mode `mirror`:

  | Member (`remote.mirror.…`) | Meaning | Default (proposed) | Range |
  |---|---|---|---|
  | `paused` | No pass starts; a running pass stops before its next package (R-REPO-053) | `false` | |
  | `intervalSeconds` | From the start of one pass to the start of the next | `3600` | 300–604800 |
  | `fullPassIntervalSeconds` | Age of the last full pass after which the next pass is full although an incremental one could run; `0`: only when an incremental pass cannot run (R-REPO-051) | `604800` | `0`, or 86400–7776000 |
  | `discovery` | `auto`, `feed`, `listing` or `names` (R-REPO-051) | `auto` | |
  | `concurrency` | Ceiling of the pass's concurrent background fetches (R-REPO-054, R-REPO-055) | `4` | 1–64 |
  | `maxVersionsPerPackage` | Newest versions of a package the pass stores, by the format's version order; `0`: all | `0` | 0–10000 |
  | `includePrereleases` | Whether prerelease versions are stored | `true` | |
  | `maxBytes` | Byte bound of the repository's stored content beyond which the pass adds nothing; `null`: no bound of its own (R-REPO-052) | `null` | 1048576–2^62 |

- A stored section written before these members existed reads back with them at their defaults: the read fills them, and
  no migration rewrites stored documents (Q-REPO-130). A document read and sent back therefore saves unchanged.
- `mode: mirror` on a format whose remote adapter cannot discover packages (the catalog fact `Mirror`,
  [architecture.md](architecture.md) R-ARCH-008) answers 422 `not_applicable` at `/remote/mode`, as R-REPO-024 refuses every
  setting that would be accepted and not enforced. npm, NuGet and OCI can: their remote adapters implement the lister
  ([formats/npm.md](formats/npm.md#mirror-discovery), [formats/nuget.md](formats/nuget.md#mirror-discovery),
  [formats/oci.md](formats/oci.md#mirror-discovery)). A
  `discovery` that the upstream does not offer is not refused at save, because only the upstream can answer it (as for
  NuGet's resources, [formats/nuget.md](formats/nuget.md) Q-NUGET-14): the pass reports `discovery_unavailable`
  (R-REPO-056).
- Turning the mode to `mirror` makes the next dispatch (at most 60 s later, R-REPO-053) start a full pass. Turning it back
  to `pullThrough` stops a running pass before its next package; stored content, the cursor of the last complete pass and
  the status stay (Q-REPO-148), so turning it on again continues incrementally.
- The members of `remote.mirror` and `remote.mode` are not part of the upstream identity of R-REPO-011: changing them
  invalidates nothing.
- Who may set them: a holder of `repositories:write-config`; an Owner without `repositories:write-config` only keeps what is stored, pauses,
  resumes or turns the mode back to `pullThrough` (Q-REPO-142; 422 `mirror_restricted` otherwise). While a mirror stays a
  mirror it also keeps the `includePatterns`, `excludePatterns`, `remote.url` and `remote.extraHosts` as stored: they decide what
  the mirror covers and where it reads from, so a write that changes one answers 422 `mirror_restricted` at its pointer
  (Q-REPO-193); a write that turns the mode back to `pullThrough` may change them. What a format's mirror keeps beyond the
  generic members is the format's member `<format>.mirror` (`oci.mirror`, [formats/oci.md](formats/oci.md) R-OCI-026): an
  Owner keeps it as stored, in either mode, like `remote.mirror` (the format's defaults on a create), and a write that changes
  it answers 422 `mirror_restricted` at `/<format>/mirror` (Q-OCI-22, Q-REPO-295).
- The well-known upstream of a format needs a bound before a mirror of it is accepted (R-REPO-052, 422 `mirror_unbounded`). The
  url of a mirror carries no query (422 `invalid_value` at `/remote/url`, Q-REPO-195).

Configuration (example):

```yaml
key: npm-types                        # example
class: remote
format: npm
includePatterns: ["@types/*"]         # example: the bound that R-REPO-052 asks for
remote:
  url: https://registry.npmjs.org/
  mode: mirror                        # default (proposed): pullThrough
  mirror:
    paused: false                     # default (proposed)
    intervalSeconds: 3600             # default (proposed)
    fullPassIntervalSeconds: 604800   # default (proposed)
    discovery: auto                   # default (proposed)
    concurrency: 4                    # default (proposed)
    maxVersionsPerPackage: 5          # example; default (proposed) 0, all
    includePrereleases: false         # example; default (proposed) true
    maxBytes: 107374182400            # example, 100 GiB; default (proposed) null
```

Acceptance criteria:

- A remote created with only `url` reads back `"mode": "pullThrough"` and every member of `remote.mirror` with its default.
- A row whose stored `remote` section lacks `mode` and `mirror` (written by a test as an earlier release stored it) reads
  back with both at their defaults, and a `PUT` of that document with its `ETag` answers 200 and changes nothing else.
- `"mirror": {"concurrency": 0}` answers 422 `out_of_range` at `/remote/mirror/concurrency`; `"discovery": "crawl"` 422
  `invalid_value` at `/remote/mirror/discovery`.
- A remote of the conformance kit's test format, whose adapter does not discover, answers 422 `not_applicable` at
  `/remote/mode` for `mode: mirror`.
- A remote in mode `pullThrough` never has a `mirror-pass` job, and the counting stub upstream records no request while
  no client asks, over two dispatch intervals.

### R-REPO-051 · What a mirror discovers, and what bounds it
Priority: **MVP** · Covers: trove-only

- **Discovery** yields package names; for each name the format's **plan** yields the documents a client of that package
  needs and the artifacts of its versions, newest first by the format's version order ([architecture.md](architecture.md)
  R-ARCH-008, the optional mirror interface). The mirror stores the artifacts and keeps the documents current through the
  ordinary metadata path of R-REPO-025 and R-REPO-027: a stored document that is fresh is used as it is, an expired one is
  revalidated, so a pass also refreshes the metadata of the packages it holds. The plan of a package asks for a stored document
  that was validated before the pass's attempt began as for an expired one (one conditional request per package and attempt,
  Q-REPO-167), so that it sees the version a feed announced and not a copy that is fresh by its period but older than the change;
  an artifact that is stored costs no request.
- **Methods**, in the order `auto` tries them:

  | Method | What it reads | Passes |
  |---|---|---|
  | `feed` | An append-only change feed or catalog of the upstream with a position (cursor) | A full pass reads it from its beginning; an incremental pass reads what changed after the cursor of the last complete pass |
  | `listing` | A paged listing of every package of the upstream | Every pass is full |
  | `names` | The include patterns without a wildcard, read as exact names | Every pass is full |

  Whatever the method, every full pass also takes the exact names of the include patterns and the names of the packages
  the repository already holds (its package rows, [storage.md](storage.md) R-STOR-003; paged under the bound of 10,000 pending
  names, Q-REPO-158), so that a package that clients
  pulled is kept current and a name a feed missed is caught. `auto` takes the first method the upstream offers; a method
  set explicitly that the upstream does not offer ends the pass with `discovery_unavailable`. What each upstream offers, and
  what of it trove uses, is on the format pages.
- **A pass is full** when the repository has no cursor of a complete feed pass; when the method differs from the one of
  that cursor; when `fullPassIntervalSeconds` is not `0` and the last full pass is older; when the include or exclude
  patterns changed since the last complete pass (the state keeps a fingerprint of both lists); and when it is requested
  (R-REPO-056). Otherwise a `feed` pass is incremental.
- **Patterns bound everything** (R-REPO-005). A name that the include and exclude patterns do not admit, or that a claim
  refuses (R-REPO-023, R-REPO-025 step 2), is never asked for, not even its document. A format may use the literal prefix of
  an include pattern to narrow a listing request it sends (npm's key ranges); it still checks every name it returns.
- **Versions.** `maxVersionsPerPackage` keeps the newest n versions of the plan, after `includePrereleases: false` has
  removed the prerelease ones. A format may name versions that always count (npm: the version of the `latest` dist-tag,
  [formats/npm.md](formats/npm.md) Q-NPM-71; OCI: the tags an exact pattern of `oci.mirror.tags` names, Q-OCI-180). A version
  outside the bound is not stored by the pass and stays available to clients by pull-through. The bound counts **versions**, not
  items: a version may have many items (an OCI tag's child manifests, configurations and layers carry the tag as their version),
  which are kept or dropped together, and an artifact that two kept versions name is filled once per attempt (Q-REPO-295). A
  format whose versions cost a request each to plan reads the bounds through the optional `discover.Bounded` of the upstream it is
  lent and leaves out the versions the engine would drop (Q-OCI-182).
- **Removed upstream.** The mirror deletes nothing (Q-REPO-144). A version or package the upstream withdrew is handled by the
  refreshed document as for every remote: gone packages of R-REPO-029, npm's removal marks of R-NPM-026. A feed's deletion
  record makes the pass refresh that package's document and store nothing.
- **Completeness.** A listing the upstream truncates (nuget.org's search caps `skip`) completes the pass with
  `discoveryComplete: false` and the reason (R-REPO-056); the pass is complete for everything it could see.

Acceptance criteria (integration, counting stub upstreams of R-REPO-059):

- An npm stub whose feed lists `@acme/a`, `@acme/b`, `@acme/c`, `left-pad` and `@other/x`, and a mirror with
  `includePatterns: ["@acme/*"]`: the first pass stores every tarball of the three `@acme` packages; the stub records no
  request for a path of `left-pad` or `@other/x`.
- With `maxVersionsPerPackage: 2` and `includePrereleases: false`, a package with versions `1.0.0`, `1.1.0`, `2.0.0-rc.1`
  and `1.2.0` (the `latest` tag on `1.1.0`): the pass stores `1.2.0` and `1.1.0`, and `npm install @acme/a@1.0.0` through the
  remote still succeeds by pull-through.
- After a complete feed pass, the stub publishes `@acme/a@1.3.0`: the next pass is incremental, asks the feed after the
  stored cursor, fetches only the document of `@acme/a` and the new tarball, and stores it.
- Adding `@acme2/*` to the include patterns makes the next pass full.
- A package that a client pulled through the remote before the mode was switched, and that the feed does not list, is
  refreshed by the next full pass.

### R-REPO-052 · Storage bounds of a mirror
Priority: **MVP** · Covers: trove-only

A mirror of a public registry would be terabytes. The bounds below keep it to what the operator chose; client pull-through is
never bounded by them.

- **A bound is required for a well-known upstream.** A mirror whose `url` is the format's well-known upstream (the catalog's
  default upstream: `https://registry.npmjs.org/`, `https://api.nuget.org/v3/index.json`) is refused with 422
  `mirror_unbounded` at `/remote/mode` unless the repository has a non-empty include list in which every pattern has a literal
  part before its first wildcard (`@types/*`, `Microsoft.Extensions.*`, `lodash`; not `*`, `**`, `@*/*` or `?x`), or a
  `maxBytes` (Q-REPO-132). A mirror of any other upstream without either is saved with the warning `mirror_unbounded`.
- **`maxBytes`** bounds the repository's logical bytes: the folded counter `repository_usage.logical_bytes` of
  [storage.md](storage.md) R-STOR-003, the number the repository quota of R-STOR-029 reads, which counts what clients filled
  as well. Before each artifact the pass reads the counter (one primary-key read) and adds the bytes it stored since the
  counter's last fold; it fetches no artifact whose announced size would cross the bound, and an artifact without an
  announced size is fetched and counted afterwards. The bound can therefore be passed by at most `concurrency` artifacts in
  flight and one fold interval (H-505).
- **When a bound is reached**, the pass adds no further artifact: it stops, keeps its pending names, ends with the outcome
  `bound_reached`, writes the audit event `mirror.bound_reached` once per transition and shows the bound in the status. Later
  passes check the counter first and end at once with the same outcome, without an upstream request, while the bound holds.
  Raising `maxBytes`, or content deleted below it, lets the next pass continue with the pending names; the bound is cleared when a
  pass ends `complete` or `partial` (Q-REPO-159, [H-527](../security/mirror.md#h-527--the-bounds-base-is-a-lagging-counter-and-a-per-process-tally)). Client requests to the
  repository are unaffected: they fetch and store as in mode `pullThrough`.
- **Quotas and capacity** ([storage.md](storage.md) R-STOR-029, P1). When quotas are built, a repository quota
  (`storage.quota`) bounds a mirror like `maxBytes` (the lower one wins; the bound is `quota`), and the pass adds no artifact
  while the system's used capacity is at or above `warnPercent` (the bound is `capacity`), so that a mirror never pushes the
  installation to the limit at which client uploads are refused. Until then `maxBytes` is the mirror's only bound (H-506).
- **Spool.** Background fills spool like every fill (R-STOR-004). A refused spool admission is a wait of the item, not a
  failure: the pass retries it after 5 s.

Acceptance criteria:

- An npm mirror of `https://registry.npmjs.org/` without include patterns and without `maxBytes` answers 422
  `mirror_unbounded`; with `includePatterns: ["@types/*"]` it is created; with `["**"]` it is refused again; with only
  `"maxBytes": 10737418240` it is created. A mirror of a stub upstream with neither is created with the warning
  `mirror_unbounded`.
- A mirror of a stub with 30 MiB of tarballs and `maxBytes: 10485760` ends its pass with `bound_reached`, stores at most
  10 MiB plus `concurrency` times the largest tarball, and writes one `mirror.bound_reached` event; `npm install` of a version
  it did not store succeeds through the same remote; after `maxBytes` is raised to 64 MiB, the next pass stores the rest.

### R-REPO-053 · Mirror passes as jobs on the queue `mirror`
Priority: **MVP** · Covers: trove-only

- **Where passes run.** The dispatch and the passes are jobs of the job framework ([architecture.md](architecture.md)
  R-ARCH-016) on their own queue `mirror`. Whatever process claims that queue runs them: every process that claims jobs, or
  a pool of its own that claims only this queue ([ha-and-helm.md](ha-and-helm.md) R-HA-062). A process that serves clients
  and claims no jobs only enqueues and reads the state. Nothing below depends on which process that is.
- **Dispatch.** The periodic task `mirror-dispatch` (every minute, queue `maintenance` so that passes that hold the slots of
  the queue `mirror` for hours cannot starve it, Q-ARCH-321, `exclusive`; [architecture.md](architecture.md) R-ARCH-016) ensures a `mirror_state` row for every remote repository in mode `mirror`
  of the repository snapshot and enqueues a `mirror-pass` job under the dedup key `mirror-pass:<repository id>`
  (`exclusive`) for every row that is due: not paused, `next_pass_at` passed or a pass requested (R-REPO-056), and
  `rate_limited_until` passed. One pass per repository runs at a time; an enqueue while one is queued or running returns it. A
  host that is not ready (`Host.Ready`, Q-ARCH-312) fails the dispatch with the cause instead of skipping every repository, and
  a repository whose stored document does not load is a pause: its pass and its pending names stay (Q-REPO-191).
- **A pass** (job `mirror-pass`, queue `mirror`, `MaxAttempts: 1`, no timeout) runs these steps:
  1. **Claim.** One statement makes the job the owner of the repository's pass: it writes the job id and the claiming
     process's instance id into `mirror_state` (the fence), starting a new pass when none is in progress (a pass whose kind the caller decided from the row, Q-REPO-156) and taking over the
     one in progress otherwise; a new pass also gives the pending names their attempts back and consumes a request for a pass
     (R-REPO-056), while a pass that is taken over leaves it for the pass after it, so that a request for a full pass is not lost
     (Q-REPO-189). The claim leaves the upstream's wait alone (Q-REPO-187). Every later write of the pass is conditional on the fence; a write that matches no row
     means another job owns the pass, and the job stops at once without writing (Q-ARCH-287).
  2. **Discover.** While fewer than 10,000 rows are pending (every row of the repository counts, the ones left after three
     attempts included, Q-REPO-190), the pass asks the format for the next page of names (R-REPO-051)
     and, in one transaction, inserts them into `mirror_pending` (a name already pending is kept once) and stores the
     format's cursor of the pass. The repository is read from the snapshot before each page (the stops below); a request that a
     client pre-empted is sent again after five seconds and is no failure of the pass (Q-REPO-184); a name the current patterns
     or claims refuse is never inserted, and one that was inserted under earlier patterns is dropped when a pass is claimed and
     before its package is taken (Q-REPO-188).
  3. **Fill.** It takes pending names in name order; for each package it asks the format's plan, fills the documents and the
     artifacts through the remote engine (R-REPO-025 steps 4 to 10, with the fetch priority of R-REPO-055: an artifact that
     is already stored costs no upstream request), and then, in one transaction, deletes the pending row and adds to the
      pass's counters. An artifact that the host refuses (`*RefusedError`: the upstream does not have it, the scanning blocks it,
     the format's gate or the populate check refuses it) is counted `refused` and the next artifact goes on; a transient failure
     of an artifact does not keep the others of the package from being stored, and the package then fails once
     (Q-REPO-185): it keeps its row with one more attempt and `not_before` one minute later per attempt (other than by
     throttling, R-REPO-054); after three attempts it is left to the next pass and counted as failed. An artifact that came back
     with a digest other than the announced one, or whose document announces none to compare with (`*PermanentError`:
     `upstream_integrity_mismatch`, `upstream_digest_missing`), is fetched wrong again by a retry: the package is counted failed
     after that attempt, with the reason, and the next pass tries it again (Q-REPO-252); when another artifact of the package failed
     transiently in the same attempt, the package keeps its retries and that failure is the recorded reason.
  4. **End.** (The summary and the job are two writes: the end of the pass is written first and the job is marked
     `succeeded` when the pass returns, so a reader may see the outcome while the job is still `running`; the pass does not
     depend on its job, Q-REPO-135.) When discovery has ended and every pending row is done or left, one transaction writes the summary of the pass,
     the format's cursor for the next incremental pass, the time of the last full pass, the fingerprint of the patterns,
     clears the pass, resets the failure count and sets `next_pass_at` to the pass's start plus `intervalSeconds`, or now when
     that has passed. The outcome is `complete`, or `partial` when packages were left; the audit event
     `mirror.pass_completed` is written. A pass that ends `bound_reached` advances the cursor only when its discovery had ended
     (Q-REPO-155).
- **Stops.** A pass checks the repository snapshot before each package: a repository deleted, disabled, paused or no longer
  in mode `mirror` ends the pass with the outcome `stopped`; a pause keeps the pass in progress for the resume, a mode change
  or a deletion clears it (Q-REPO-155, Q-REPO-162); a disabled repository is a pause. Offline mode and the global offline switch
  (R-REPO-030, R-REPO-031) end the attempt with `offline`, without counting a failure; the pass stays and the next attempt is
  due after `min(intervalSeconds, 300 s)` (Q-REPO-161). The snapshot is also read before each page of discovery, so that a
  pause or offline mode that arrives during a long discovery takes effect at once. A stored document that does not load is a pause
  (Q-REPO-191).
- **Drain and restart** ([architecture.md](architecture.md) R-ARCH-024). A process that shuts down while it runs a pass
  cancels the pass's in-flight fetches (nothing partial is stored; the host answers such a request with `ErrShuttingDown`, which
  stops the pass like the end of its own context and writes nothing, Q-ARCH-320) and releases the job with its attempt unchanged; another
  process that claims the queue claims it,
  takes over the fence and continues with the pending names and the stored cursor. The package that was in progress is
  filled again; what it had stored is skipped.
- **Crash and failover.** A process that dies loses its lease; the reaper ends the job (`MaxAttempts: 1`), the next dispatch
  enqueues a new job for the pass in progress, which takes over the fence and continues; a write of the dead process that
  still arrives matches no row. A PostgreSQL failover is ridden out by the statements' retry (R-HA-009); a lost commit of a
  deleted pending row fills that package again, which stores nothing twice (content addressing, R-REPO-026).
- **Errors.** A failure of the pass itself (discovery fails other than by throttling, a database error) ends the job with
  the error: the state records it, counts one more consecutive failure, writes `mirror.pass_failed`, and sets `next_pass_at`
  to now plus `min(60 s × 2^(failures − 1), 6 h)` with a jitter of ±20 % (Q-REPO-136). The pass in progress stays and is
  continued by the next job. A complete pass resets the count.
- **Data model.** The tables below are the design; the work package that builds the engine adds them in a migration of its
  own, when the process topology is settled (a migration on `main` is immutable, [ADR-148](../decisions.md#adr-148--migrations-on-main-are-immutable-and-a-defect-is-fixed-forward)):

```sql
CREATE TABLE mirror_state (
  repo_id                 bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
  next_pass_at            timestamptz,             -- NULL: due at the next dispatch
  pass_requested_at       timestamptz,             -- a request of R-REPO-056 not yet taken
  pass_requested_by       uuid,
  pass_requested_full     boolean NOT NULL DEFAULT false,
  -- the pass in progress; pass_started_at NULL: none
  pass_started_at         timestamptz,
  pass_kind               text CHECK (pass_kind IN ('full', 'incremental')),
  pass_method             text CHECK (pass_method IN ('feed', 'listing', 'names')),
  pass_job_id             uuid,                    -- the fence: the owning job and the process that claimed it
  pass_holder             uuid,
  pass_cursor             jsonb CHECK (pass_cursor IS NULL OR octet_length(pass_cursor::text) <= 4096),
  pass_discovery_done     boolean NOT NULL DEFAULT false,
  pass_names              bigint NOT NULL DEFAULT 0,
  pass_packages_done      bigint NOT NULL DEFAULT 0,
  pass_packages_failed    bigint NOT NULL DEFAULT 0,
  pass_items_stored       bigint NOT NULL DEFAULT 0,
  pass_bytes_stored       bigint NOT NULL DEFAULT 0,
  pass_discovery_complete boolean NOT NULL DEFAULT true,
  pass_discovery_note     text CHECK (length(pass_discovery_note) <= 256),
  -- the last complete pass
  feed_cursor             jsonb CHECK (feed_cursor IS NULL OR octet_length(feed_cursor::text) <= 4096),
  feed_method             text CHECK (feed_method IN ('feed', 'listing', 'names')),
  patterns_fingerprint    bytea,
  last_full_pass_at       timestamptz,
  last_pass               jsonb,                   -- the summary R-REPO-056 shows
  -- failures, throttling and bounds
  consecutive_failures    integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  last_error_at           timestamptz,
  last_error_code         text CHECK (length(last_error_code) <= 64),
  last_error              text CHECK (length(last_error) <= 1024),
  rate_limited_until      timestamptz,
  rate_limit_waits        bigint NOT NULL DEFAULT 0,
  rate_limit_wait_seconds bigint NOT NULL DEFAULT 0,
  last_rate_limit_at      timestamptz,
  last_rate_limit_status  integer,
  bound_reached           text CHECK (bound_reached IN ('bytes', 'quota', 'capacity')),
  bound_reached_at        timestamptz,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CHECK ((pass_started_at IS NULL) = (pass_kind IS NULL))
);
CREATE INDEX mirror_state_due ON mirror_state (next_pass_at);

CREATE TABLE mirror_pending (
  repo_id     bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name        text COLLATE "C" NOT NULL CHECK (length(name) <= 1024),
  queued_at   timestamptz NOT NULL DEFAULT now(),
  attempts    integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  not_before  timestamptz,
  last_error  text CHECK (length(last_error) <= 1024),
  PRIMARY KEY (repo_id, name)
);
```

Acceptance criteria (integration):

- A pass of a stub with 200 packages is released by a shutdown of the process that runs it after 50 packages: the job is
  queued again with its attempt unchanged, a second process ends the pass, and the stub records every tarball once, apart from those of the
  package in progress at the shutdown.
- The process that runs a pass loses its lease (the test stops its heartbeat): the next dispatch after the lease expiry
  enqueues a new job that ends the pass from the pending names; a pending-row delete sent afterwards under the old fence
  changes no row.
- The stub answers 500 to every discovery request: the failure count grows to 3 and `next_pass_at` lies 60 s, 120 s and
  240 s (±20 %) after each failure; after the stub recovers, the next pass completes and the count is 0.
- Pausing the mirror during a pass ends it with `stopped` before its next package; resuming continues it from the pending
  names; switching the mode to `pullThrough` clears it.

### R-REPO-054 · Upstream throttling of a mirror
Priority: **MVP** · Covers: trove-only

- **Rate-limit answers** (Q-REPO-137). The mirror treats as a rate-limit answer to any of its requests, discovery or fill:
  - `429 Too Many Requests` ([RFC 6585, section 4](https://www.rfc-editor.org/rfc/rfc6585#section-4));
  - `503` with a `Retry-After` header (a rate limit even when its value is a date in the past or does not parse: it is then a
    rate limit with no hint, Q-REPO-183), and `503` whose body, read up to 4 KiB, is an S3 error with the code `SlowDown`
    ([Amazon S3 error responses](https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html));
  - `403`, `429` or `503` with `RateLimit-Remaining: 0` or `X-RateLimit-Remaining: 0`; the reset then comes from
    `RateLimit-Reset` (seconds) or `X-RateLimit-Reset` (seconds since the epoch) when `Retry-After` is absent. The value is read
    by its integer before the first `;`, so `0;w=21600`, which a registry sends with the window of its quota, is 0
    ([Q-REPO-256](#q-repo-256--the-quota-header-is-read-by-its-integer-before-the-first-semicolon)); a value whose first element
    is no integer is not a quota header.

  The token service of a handshake (R-REPO-033) is no different: its `429` or `503` answer to a request of the pass is a rate-limit answer.

  Other `5xx` answers, timeouts and connection failures are failures of the item (R-REPO-053) with the same back-off as an
  answer without a hint, but they do not halve the window.
- **How long it waits.** `Retry-After` as delta seconds or as an HTTP date
  ([RFC 9110, section 10.2.3](https://www.rfc-editor.org/rfc/rfc9110#section-10.2.3)), capped at 6 h; a date in the past or
  an unreadable value counts as absent. Without a hint the wait is exponential with jitter: after the k-th rate-limit answer
  in a row it is drawn uniformly from `[d/2, d]` with `d = min(300 s, 1 s × 2^(k−1))`. A success resets k. A wait is at least one second, and only the answer that begins a wait counts as one (Q-REPO-160). The
  end of every wait is stored in `rate_limited_until` while it lasts, whatever its length, so that a job that is released in the
  middle of it does not resume at once in another process; a job that starts while it lies ahead sends nothing and ends with the
  result `{"skipped": "rate_limited"}`, and the run operation answers 409 `mirror_rate_limited` (Q-REPO-187).
- **While it waits** the pass sends nothing to the upstream: no discovery request and no new fill; fetches already in flight
  finish. After the wait it continues by itself.
- **A long wait ends the attempt.** A wait longer than 300 s writes `rate_limited_until`, keeps the pass in progress, and ends
  the job with the result `{"deferred": "rate_limited", "until": …}`; the dispatch enqueues the next job at that time, which
  continues the pass. No job slot is held through a long wait: every worker of the pass, the ones that are blocked behind the
  wait included, is released when it begins (Q-ARCH-323).
- **Fewer concurrent fetches** (Q-REPO-138). The pass's window `w` starts at 1 in every attempt. It doubles after `w`
  successful fetches in a row until it reaches its ceiling or a rate-limit answer arrives (slow start), and afterwards grows
  by one after `w` successes in a row. A rate-limit answer halves it, rounded down and at least 1, once per wait: answers of
  fetches that were in flight when the wait began do not halve it again, and their successes do not grow it (Q-REPO-168). The ceiling is `concurrency`, held below the
  background share of R-REPO-055.
- **The circuit is the clients'** (Q-REPO-139). No outcome of a background request moves or resets the circuit of R-REPO-029:
  a rate limit the mirror provokes never turns clients away. The mirror reads the circuit: while it is open, the pass sends
  nothing and waits for it to close; it never probes. Open means open: after the open period the circuit is half-open, the
  pass goes on without claiming the probe and without resetting anything (Q-REPO-150). A client request keeps R-REPO-029's
  classification: for a client, a `503` with `SlowDown` is still `upstream_error`.
- Every rate-limit answer is counted and its wait added (R-REPO-057); the state keeps the number of waits, their total
  seconds, the time and status of the last one and `rate_limited_until` (R-REPO-056); `mirror.rate_limited` is written at
  most once per pass and hour.

Acceptance criteria (integration, stub modes of R-REPO-059):

- The stub answers the eleventh tarball request with `429` and `Retry-After: 7`: it records no request of the pass for 7 s,
  then the pass continues and completes; `trove_mirror_concurrency{repo}` is at most half of its value before the answer.
- `Retry-After` as an HTTP date 5 s ahead pauses the pass for 5 s (±1 s).
- `503` with the body `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>` and no
  `Retry-After`, three times in a row: the pauses lie within [0.5 s, 1 s], [1 s, 2 s] and [2 s, 4 s]; the pass then
  completes and every tarball is stored once.
- `403` with `X-RateLimit-Remaining: 0` and `X-RateLimit-Reset` 4 s ahead pauses the pass for about 4 s.
- `Retry-After: 3600`: the job ends with the result `deferred`, the status shows `rateLimit.until` an hour ahead, and the stub
  records no request of the mirror in the next 60 s. A unit test with a fake clock shows the next job continuing the pass at
  that time.
- While the stub throttles the pass, `trove_remote_upstream_up{repo}` stays 1 on every replica and a client
  `npm install` of a stored version through the remote succeeds without an upstream request.

### R-REPO-055 · Client requests first
Priority: **MVP** · Covers: trove-only

A request from a client is served before the background sync. The mirror uses a lower-priority share of the remote's fetch
budget (`limits.maxConcurrentFetches`, R-REPO-040, Q-REPO-8, Q-REPO-97), and client fetches pre-empt it (Q-REPO-140).

- **The share.** A pass runs at most `B = min(concurrency, max(1, floor(maxConcurrentFetches ÷ 4)))` background fetches of
  the repository at a time; its window (R-REPO-054) never exceeds B. The client shares of R-REPO-040 are not reduced: every
  client-serving replica keeps `ceil(maxConcurrentFetches ÷ n)`. The cluster-wide total therefore stays within
  `maxConcurrentFetches + n − 1 + B`, and within `maxConcurrentFetches + n − 1` while clients press (below).
- **Runs outside a pass** (Q-REPO-272, Q-REPO-305). The background fetches of a remote repository that no mirror pass runs - the parse job of a
  Debian remote's indexes, the fetch of the release documents a virtual build merges - are background fetches in the sense of this requirement:
  the same slots, the same pre-emption, the same ceiling `B`, the same flight rules and the same credential and egress rules as a pass's. They
  have no pass, fence or window of the pass; each run has a window of its own below `B` (R-REPO-054).
- **Slots.** A background fetch takes a fetch slot of the replica it runs on: the replica's `proxy.max_concurrent_fetches`
  and a per-repository background counter; it never queues: it waits outside the slots until one is free. It takes the
  cross-replica flight of R-REPO-026 like any fetch, so a client that asks for the same item joins it as a follower or a
  waiter instead of fetching twice. A background fetch takes its slot before it enters a flight (Q-REPO-179), so that no client waits for a flight whose fetch has not begun;
  the e2e set `mirror` measured `npm ci` at 41 s against 12 s before that rule ([developer/e2e.md](../developer/e2e.md#the-mirror-tests)).
- **The end of the exchange** (Q-REPO-235). The background counter and the repository's share are returned when the
  upstream exchange of the item the slot was taken for ends (R-REPO-040), the replica's slot when the fetch ends. From
  the end of its exchange a background fetch is no longer one that a client fetch or the signal below pre-empts: a
  pre-emption frees an upstream slot, and a fetch that is storing what it received holds none; cancelling it would throw
  away a whole transfer that the pass then repeats. Returning the share does not end the context the fetch runs under,
  so its commit is not cancelled by it. A discovery document that a background fetch resolves under its slot before the
  item (R-REPO-040, the format's discovery) returns nothing: the slot was taken for the item.
- **Who runs what** (phase 6a, [architecture.md](architecture.md) R-ARCH-001). A client-serving replica on this page is one
  that fills client misses: `packages` (and `web` in the upgrade bridge); the process that claims the queue `mirror` is a
  `worker`, or a worker pool that claims `mirror` alone ([ha-and-helm.md](ha-and-helm.md) R-HA-062). The two meet only
  through PostgreSQL, Valkey and the signal below.
- **Pre-emption on one replica.** Where client fetches and background fetches run in one process (one that serves clients and claims the queue
  `mirror`, such as role `all`), a background
  fetch starts only while no client fetch of the repository waits for a slot, and a client fetch that finds no free slot
  pre-empts the youngest background fetch of the same repository, or of any repository when the replica's limit is the
  one exhausted, and takes its slot at once.
- **Pre-emption across replicas.** A client-serving replica whose client fetch of repository R has to wait for a slot raises the
  signal `mirror_yield` with R's id, at most once per second per repository ([architecture.md](architecture.md) R-ARCH-018,
  Q-ARCH-286). Every process that runs background fetches of R, the sender included, pre-empts those that no client of its own
  has joined, and starts none of R for 30 s after the last signal (Q-ARCH-309, Q-ARCH-310). While Valkey is unavailable the signal is lost and the pass keeps
  within B, which the bound above counts.
- **What happens to a pre-empted fetch.** Its upstream request is cancelled and its partial body discarded: no blob, no cache
  entry, no negative entry; its flight is released with `failed`, so that a waiter fetches for itself at once (R-REPO-026);
  the item stays in the pass and is fetched again later. A pre-emption is neither a failure of the item nor a rate-limit
  answer, moves no circuit and is counted in `trove_mirror_preempted_total{repo}`.
- A background fetch that a follower of its own replica has joined is not pre-empted.

Acceptance criteria:

- Unit (one process that serves clients and claims the queue, `maxConcurrentFetches: 2`, so B = 1): while the background holds one slot, two client
  fetches of uncached tarballs arrive; the second pre-empts the background fetch and waits less than 50 ms for its slot; the
  pre-empted tarball is stored once, later, by the pass. A background fetch whose upstream exchange has ended and whose
  commit is held back on purpose is pre-empted neither by a client fetch nor by the signal, holds no background count,
  and its item is stored once the commit is let go.
- Integration (one client-serving replica and one process that claims the queue `mirror`, `maxConcurrentFetches: 8`, so
  B = 2; the stub delays every tarball by 2 s): the pass runs two background fetches; eight client fetches take the
  client-serving replica's eight slots without a wait; a ninth waits, the process that runs the pass pre-empts both
  background fetches, and 1 s later the stub has at most eight tarball fetches in flight (Q-REPO-171).
- e2e (set `mirror`, R-REPO-059): a pass of 300 packages runs while `npm ci` of the 400-dependency fixture runs through the
  same remote on three client-serving replicas; `npm ci` succeeds within 120 % of its time without the pass, the stub never has more
  than `maxConcurrentFetches + n − 1 + B` connections open. The time ratio is enforced by the nightly run, the bound in every run
  (Q-REPO-197). Pre-emption is proved by a second part with the contention forced (Q-REPO-253): a pass runs while the stub holds a tarball
  1.5 s, 24 clients ask the three client-serving replicas for distinct tarballs the pass has not stored (their slots hold 9), all are served
  with 200, and `trove_mirror_preempted_total` grew.

### R-REPO-056 · Mirror status and operations
Priority: **MVP** · Covers: API-084, API-085

| Method and path | Purpose | Response | Permission |
|---|---|---|---|
| `GET /api/v1/projects/{projectKey}/repositories/{repoKey}/mirror` | The mirror's status | 200 with the document below | as the full configuration read (R-REPO-010): `repositories:read-config`, `manage` on the repository, or the role `owner` in its project; 400 `not_applicable` for a local or virtual repository (Q-API-199) |
| `POST /api/v1/projects/{projectKey}/repositories/{repoKey}/mirror/run` | Request a pass now; body `{"full": false}` (optional) | 202 with the job reference of R-API-015 | as an update (R-REPO-011): `repositories:write-config`, or `manage` on the repository, which the roles `maintainer` and `owner` hold on their project's repositories (Q-API-199) |

```json
{
  "mode": "mirror",
  "state": "running",
  "pass": {
    "kind": "incremental", "method": "feed", "startedAt": "2026-11-04T10:00:00Z",
    "namesDiscovered": 120, "packagesDone": 80, "packagesPending": 40, "packagesFailed": 0,
    "itemsStored": 311, "bytesStored": 52428800, "discoveryDone": false,
    "job": {"id": "0193f2c4-…", "kind": "mirror-pass", "state": "running", "href": "/api/v1/system/jobs/0193f2c4-…"}
  },
  "lastCompletePass": {
    "kind": "full", "method": "feed", "startedAt": "2026-11-03T10:00:00Z", "finishedAt": "2026-11-03T13:12:09Z",
    "outcome": "complete", "packages": 1200, "packagesFailed": 0, "itemsStored": 5021, "bytesStored": 9126805504,
    "discoveryComplete": true, "discoveryNote": null
  },
  "nextPassAt": "2026-11-04T11:00:00Z",
  "lastFullPassAt": "2026-11-03T13:12:09Z",
  "errors": {"consecutiveFailures": 0, "lastAt": null, "lastCode": null, "lastMessage": null},
  "rateLimit": {"waits": 12, "waitSeconds": 340, "lastAt": "2026-11-04T10:20:31Z", "lastStatus": 429, "until": null},
  "bound": {"reached": null, "at": null, "maxBytes": 107374182400, "usedBytes": 9200000000}
}
```

- `state` is one of `off` (mode `pullThrough`), `paused`, `scheduled` (no pass in progress), `running` (a pass in progress whose
  job is running), `waiting` (a pass in progress whose job ended for a long rate-limit wait or a failure, or is queued again after
  a release, and waits for its next run), `bound_reached` and `failing` (three or more failures in a row); the first that holds,
  in the order `off`, `paused`, `failing`, `bound_reached`, `running`, `waiting`, `scheduled` (Q-API-196). The status is read from
  PostgreSQL by whichever replica answers; a remote in mode `mirror` that has no `mirror_state` row yet answers `scheduled` with
  nulls (Q-REPO-165). `pass.job` is a job reference of R-API-015, `null` once the job row is removed; `lastCompletePass` is the
  stored `last_pass` document and `null` when there is none or it cannot be read (Q-API-197).
- `packagesPending` is the number of pending names a pass still takes (attempts below three), the figure of the gauge
  (Q-OPS-173); `failing` is three failures in a row (`mirror.FailingFailures`), the threshold of the alert; `rateLimit.until` is
  null once the wait is over; `lastCompletePass` is the summary of the last pass attempt that ended with one of five outcomes (`complete`, `partial`,
  `bound_reached`, `offline`, `stopped`; Q-REPO-155); `lastCompletePass.outcome` is one of them; `pass` and
  `lastCompletePass` are `null` when there is none. `errors.lastMessage` names hosts, never a URL or a credential
  (R-REPO-024's rule for the upstream test).
- The run operation records the request and enqueues the pass; a running or queued pass answers 409
  `mirror_pass_running` naming its job; a remote in mode `pullThrough` answers 409 `mirror_off`, a paused one 409
  `mirror_paused`; 409 `mirror_rate_limited` with the extension `until` while the upstream's wait lies ahead (Q-REPO-187); a local or virtual repository 400 `not_applicable`. `{"full": true}` makes the requested pass full. The
  request writes `mirror.pass_requested`. It writes only `pass_requested_at`, `pass_requested_by` and `pass_requested_full` of
  `mirror_state`, in the transaction that enqueues the job and writes the event (Q-API-198, Q-REPO-163); a refusal writes nothing.
- **Pause and resume** are the member `remote.mirror.paused`, changed with the repository's `PATCH` and its `If-Match`
  (R-REPO-011, Q-REPO-141); there is no separate operation.
- The passes are jobs: `GET /api/v1/system/jobs?kind=mirror-pass` lists them (R-OPS-019).
- The admin UI shows this status on the remote repository's screen ([api-and-ui.md](api-and-ui.md) R-UI-021, Q-UI-104).

Acceptance criteria:

- During a pass the status answers `state: running` with counts that grow; after it, `lastCompletePass.outcome: complete`
  and `pass: null`; the same answer comes from every replica.
- A user with `read` on the remote and nothing else gets 403 for the status (404 where the repository is hidden,
  R-AUTHZ-016); a holder of `repositories:read-config` gets 200.
- Two `POST …/mirror/run` in a row answer 202 and 409 `mirror_pass_running` with the same job; on a `pullThrough` remote 409
  `mirror_off`; on a local repository 400 `not_applicable`.
- `PATCH` with `{"remote": {"mirror": {"paused": true}}}` makes the status `paused` and ends a running pass before its next
  package.

### R-REPO-057 · Mirror observability
Priority: **MVP** · Covers: trove-only

- **Metrics** ([operations-and-observability.md](operations-and-observability.md) R-OPS-008, Q-OPS-158; the label `repo` has
  one value per mirrored repository):

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `trove_mirror_passes_total` | counter | `repo`, `kind` (`full`, `incremental`), `outcome` (`complete`, `partial`, `bound_reached`, `offline`, `stopped`, `failed`, `deferred`) | Pass attempts by how they ended |
| `trove_mirror_items_total` | counter | `repo`, `result` (`stored`, `present`, `bounded`, `failed`, `preempted`, `refused`) | Artifacts of plans: fetched and stored, already stored, left by a bound, failed, pre-empted, refused by the host (Q-REPO-185) |
| `trove_mirror_bytes_total` | counter | `repo` | Bytes the passes stored |
| `trove_mirror_rate_limited_total` | counter | `repo`, `reason` (`too_many_requests`, `slow_down`, `retry_after`, `quota_header`) | Rate-limit answers to the mirror (R-REPO-054) |
| `trove_mirror_rate_limit_wait_seconds_total` | counter | `repo` | Seconds the passes waited for rate limits |
| `trove_mirror_concurrency` | gauge | `repo` | The running pass's window in the process that runs it |
| `trove_mirror_preempted_total` | counter | `repo` | Background fetches pre-empted by clients (R-REPO-055) |
| `trove_mirror_pending_packages` | gauge | `repo` | Pending names of the pass in progress that a pass still takes, 0 without a pass (from PostgreSQL) |
| `trove_mirror_last_complete_pass_timestamp_seconds` | gauge | `repo` | End of the last pass that ended `complete`, `partial` or `bound_reached` (`mirror_state.last_complete_pass_at`, Q-OPS-172); no series before the first |
| `trove_mirror_interval_seconds` | gauge | `repo` | The configured `intervalSeconds` |
| `trove_mirror_consecutive_failures` | gauge | `repo` | Failed pass attempts in a row (from PostgreSQL); only for a mirror that is not paused |
| `trove_mirror_next_pass_timestamp_seconds` | gauge | `repo` | When the mirror next acts: the time a due pass is due, or for a pass in progress the later of that and its last write (from PostgreSQL); only for a mirror that is not paused |

  The five gauges that are not counters are exported by every process, whatever its role (the area `mirror` is on every role),
  from `mirror_state` and the repository snapshot through the database gauge source, read at most once per 30 s, so every
  process reports the same values and an installation without a process that claims the queue `mirror` still shows a mirror
  that nobody runs (Q-OPS-171). Only a remote repository in mode `mirror` has series. The alerts `TroveMirrorFailing` and
  `TroveMirrorStale` read the last two gauges ([operations-and-observability.md](operations-and-observability.md) R-OPS-046,
  Q-OPS-160, Q-OPS-171).
- **Upstream log** ([operations-and-observability.md](operations-and-observability.md) R-OPS-011): every background request
  writes a record with `flight_role: mirror` and `on_behalf_of` empty (`system` in the log, as for every job); the record names
  the pass's job in the field `job_id` (Q-REPO-154).
- **Audit** (Q-REPO-145). Every artifact and document a pass stores writes the populate event of a cache fill
  ([authorization.md](authorization.md) R-AUTHZ-008, `authz.write.allowed` with the action `populate`) in the fill transaction,
  with the system actor and `reason: mirror`, as a client's fill writes it with the client's. The pass adds
  `mirror.pass_requested` (transactional, the caller), `mirror.pass_completed`, `mirror.pass_failed`,
  `mirror.bound_reached` and `mirror.rate_limited` (asynchronous; [operations-and-observability.md](operations-and-observability.md)
  R-OPS-016, Q-OPS-159). A change of `remote.mode` or `remote.mirror` is a `repository.updated`.

Acceptance criteria:

- After a pass of a stub with 20 tarballs, `trove_mirror_items_total{result="stored"}` grew by 20 and the audit store holds
  20 `authz.write.allowed` events of tarballs with the actor `system` and `reason: mirror` for the repository (and one more per
  stored document, Q-REPO-169), and one `mirror.pass_completed`.
- During a throttled pass the upstream log records carry `flight_role: mirror`.

### R-REPO-058 · The mirror on n replicas, without Valkey and during a drain
Priority: **MVP** · Covers: trove-only

The rows below extend the table of R-REPO-049 and of [architecture.md](architecture.md) R-ARCH-032 for the mirror.

| State | Where | Across replicas | Valkey unavailable | Valkey lost its data | Drain or crash of the replica |
|---|---|---|---|---|---|
| Mirror state, pending names, cursors, the fence (R-REPO-053) | PostgreSQL | One copy; the fence names the one writer | Unaffected | Unaffected | Drain: the job is released and continued elsewhere; crash: the next dispatch continues the pass, the fence stops a late write |
| The pass's window, waits and in-flight fetches (R-REPO-054) | In the process that runs the pass | That process only | Unaffected | Unaffected | Cancelled 11 s before the shutdown deadline (with its job, Q-ARCH-329); nothing partial stored; the next attempt starts its window at 1 |
| Background slot counters (R-REPO-055) | Per-replica counters | The ceiling B per pass | Unaffected | Unaffected | End with the process |
| The signal `mirror_yield` (R-REPO-055) | Valkey Pub/Sub, at most once | Reaches the process that runs the pass | Lost: the pass keeps within B, which the cluster bound counts | As unavailable | — |
| Mirror gauges (R-REPO-057) | Each process that claims the queue `mirror`, from PostgreSQL | Equal values in each | Unaffected | Unaffected | End with the process |

Rules:

- **One writer.** One pass per repository at a time: the dedup key of the job and the fence. A pass and clients may store the
  same item at once; content addressing makes the second commit harmless (R-REPO-026).
- **Passes run where the queue `mirror` is claimed.** A process that serves clients and claims no jobs answers the status
  and the run operation from PostgreSQL and enqueues; while no process claims the queue, no pass runs and the status shows
  `scheduled` with an overdue `nextPassAt`.
- **Valkey grants nothing here either.** Dispatch, passes, the fence, the cursors and the bounds are in PostgreSQL. The only
  value from Valkey is the yield signal, which can only stop background fetches early.
- **Backup** ([backup-and-restore.md](backup-and-restore.md) R-BAK-004, Q-REPO-147). `mirror_state` has the export class
  `data`, `mirror_pending` `transient`. A restore keeps the state of the last complete pass (its cursor matches the restored
  cache, both being of one snapshot) and clears the pass in progress, `pass_requested_*`, `rate_limited_until` and
  `bound_reached`, and sets `next_pass_at` to the restore time, so the first pass after a restore rediscovers what the
  interrupted pass had pending. A restore without remote caches (R-BAK-008) also clears `feed_cursor`, `last_full_pass_at`
  and `patterns_fingerprint`, so that its first pass is full.

Acceptance criteria (e2e set `mirror` of R-REPO-059):

- `kubectl drain` of the node of the process that runs a pass of 300 packages: the pass ends in another process that claims
  the queue, and the
  stub records every tarball once, apart from at most `concurrency` fetches in flight at the drain.
- With Valkey stopped for a whole pass, the pass completes, and the status is the same on all three client-serving replicas.
- Two processes that claim the queue `mirror`, whose dispatches tick in the same minute: the job table never holds two
  unfinished `mirror-pass` jobs for one repository.

### R-REPO-059 · The mirror's test suites
Priority: **MVP** · Covers: trove-only

- **Counting stub upstreams** (`test/integration/mirror`), one per format, which count every request by path and can be told
  to throttle: npm with a package document per name, tarballs, a replication feed (`registry/_changes` with `since` and
  `limit`, `registry/_all_docs` with `startkey`, `endkey` and `limit`, the root with `update_seq`); NuGet with a service index
  with and without `Catalog/3.0.0`, catalog index, pages and leaves, `SearchQueryService` with `skip` and `take`, and the flat
  container. Throttling modes: `429` with `Retry-After` in seconds or as a date, `503` with `Retry-After`, `503` with the
  `SlowDown` body and no `Retry-After`, `403` and `429` with `X-RateLimit-Remaining: 0`, each for the n-th request or for a
  period; a delay per response. The NuGet stub lists `Catalog/3.0.0` by a switch (off by default), refuses a search `skip` from a
  configurable cap, and keeps its throttling as a switch of its own that counts from the moment it is set
  ([formats/nuget.md](formats/nuget.md) Q-NUGET-104, Q-NUGET-105).
- **Integration** (against the stack of [ha-and-helm.md](ha-and-helm.md) R-HA-051): every acceptance criterion of R-REPO-050
  to R-REPO-058 that names the integration level, the npm and NuGet discovery criteria of the format pages, the fence, the
  status and run operations with permission-narrowed callers and an Owner, and the audit events.
- **e2e** (set `mirror` on kind, three client-serving replicas and two processes that claim the queue `mirror`): npm and
  NuGet mirrors of the stub upstreams while
  `npm ci` and `dotnet restore` run through the same remotes, with the clients-first criterion of R-REPO-055; the drain of
  R-REPO-058; a restore of a stored package with the upstream stub stopped; the browser journey of the mirror panel
  (status, pause, run a pass) with axe ([api-and-ui.md](api-and-ui.md) Q-UI-104). The drain of R-REPO-058 is the set `mirror-long`, which runs
  in the nightly workflow (owner, 2026-11-01); the rest is the set `mirror`. The profile, the way the npm mirrors get the feed, the measure of the
  stub's connections, the baseline of the install time, the size of the passes and the CI wiring are Q-REPO-173 to Q-REPO-178.
- **The failure suite of phase 7** ([ha-and-helm.md](ha-and-helm.md) R-HA-052) adds the mirror to its drains, failovers and throttling; it is
  part of the set `mirror-long` (Q-REPO-198): the process of the pool that holds a pass lost (its pod force-deleted, its kind node stopped,
  its kind node frozen and thawed), the upstream throttling a pass while its process is drained or killed, and a PostgreSQL switchover and
  a Valkey failover during a pass.

Acceptance criteria:

- The tests of `test/integration/mirror` and the e2e set `mirror` pass, and each names the requirement it checks.
- e2e (set `mirror-long`, R-HA-052): the process that holds a pass of a hundred packages is lost, by `kubectl delete pod --grace-period=0
  --force`, by `docker stop` of its kind node (then `docker start`) and by `docker pause` of its kind node (thawed after another process has
  taken the pass over). The pass progresses again in another process within 5 minutes of the loss (`jobs.lease_duration` of 60 s, the
  reaper, the next `mirror-dispatch`), ends `complete` with each package counted once, the test upstream served every tarball and at most
  `concurrency` of them twice, and every `npm install` of a stored version through the same remote while the clients' path is whole
  succeeds (R-REPO-053, R-REPO-055, R-REPO-058).
- e2e (set `mirror-long`, R-HA-052): the upstream answers `429` with `Retry-After: 30` for 45 s while the node of the pass's process is
  drained, and `503` with the `SlowDown` body eight times while the pass's process is killed. The upstream's own request log shows no request
  of the mirror inside a wait that an answer named (`Retry-After`; at least one second without it), the throttled answers stay few (six and
  twelve at most), the pass ends `complete` with every tarball served, and a client install of a stored version during the wait sends the
  upstream nothing (R-REPO-054, Q-REPO-187).
- e2e (set `mirror-long`, R-HA-052): a PostgreSQL switchover and a Valkey failover during a pass of a hundred packages leave the pass
  `complete` with each package counted once and every tarball served exactly once (R-REPO-053 "Crash and failover", R-REPO-026).

## Behavior across replicas

### R-REPO-049 · Repository and proxy state on n replicas, without Valkey and during a drain
Priority: **MVP** · Covers: trove-only

The Kubernetes Service hands every request to a random `web` replica, without session affinity
(INV-12; kube-proxy "select[s] a backend Pod at random" by default,
[virtual IPs](https://kubernetes.io/docs/reference/networking/virtual-ips/)). Valkey may be
unavailable or lose its data at any time (INV-3), and any replica may be drained (INV-12). This
requirement lists every piece of state that this page keeps outside PostgreSQL and S3 and fixes how
it behaves in each of these cases. A new kind needs a row here, and in
[architecture.md](architecture.md) R-ARCH-032, before it ships.

| State | Where | Across replicas | Valkey unavailable | Valkey lost its data | Drain or crash of the replica |
|---|---|---|---|---|---|
| Repository snapshot, claims and pin rules, gone packages, circuit state, global offline switch, cache invalidations (R-REPO-008 to R-REPO-023, R-REPO-029, R-REPO-031, R-REPO-045) | In-process copies of PostgreSQL rows | Change areas `repo_config`, `claims`, `upstream_state`, `cache_invalidation` of R-ARCH-018: within 2 s of the commit | Within `cluster.epoch_poll_interval` | As unavailable | Nothing to hand over |
| Upstream clients with their connection pools (R-REPO-024, R-REPO-033) | In-process cache `proxy_upstream_clients`, one client per repository id and `config_version` (Q-REPO-89) | Change area `repo_config` evicts the repository's clients of older configuration versions (all of them on a deletion), so the second application of a change keeps the client built after the first (Q-NUGET-94); a changed configuration or secret is a new key, so a stale client is not reached | Within `cluster.epoch_poll_interval` | As unavailable | Ends with the process; `secretRef` files are read by each replica itself, 10 s apart |
| Cache entries, gone marks, URL mappings, stale copies (R-REPO-025, R-REPO-029, R-REPO-043) | PostgreSQL and S3 | One copy, read by every replica | Unaffected | Unaffected | Unaffected |
| Negative entries (R-REPO-028) | Valkey, plus a bounded L1 per replica | Shared through Valkey | L1 only: one upstream request per replica, path and period | One more upstream request per path | L1 ends with the process |
| Flights (R-REPO-026) | In-process single-flight, lock in Valkey | One upstream fetch per entry; waiters re-check entry, circuit and negative entry before they fetch | One fetch per replica and entry | A duplicate fetch | Leads no new flight once draining; releases with `failed` 5 s before the shutdown deadline; after a crash the lock expires within `cluster.flight_lock_ttl` |
| Merged metadata (R-REPO-020), metadata bytes (R-REPO-025) | In-process caches | Self-validating: `rev_seq`, member fingerprint, blob digest; a virtual repository's reindex or invalidation drops its entries through the change area `cache_invalidation` (R-REPO-016, R-REPO-045) | The early-eviction signal is lost; a remote refresh made elsewhere appears when the merged entry expires; a reindex or invalidation applies at the next change-log poll | As unavailable | Ends with the process |
| Fetch slots (R-REPO-040) | Per-replica counters | Shares of a cluster budget, from the replica registry | Unaffected | Unaffected | Ends with the process |
| Activity and use buffers: `lastActivityAt` (R-REPO-012), metadata `last_used_at` (R-REPO-046) | Per-replica, write-only | No request reads them | Unaffected | Unaffected | Flushed at shutdown; a crash loses at most one batch |
| Detached fetches and background refreshes (R-REPO-026, R-REPO-027) | In-process | Visible to others only through the committed entry | Unaffected | Unaffected | Cancelled 5 s before the shutdown deadline; nothing partial stored |
| Mirror state and passes (R-REPO-053 to R-REPO-055) | PostgreSQL; the pass's window and background slots in the process that runs it | The rows of R-REPO-058 | As R-REPO-058 | As R-REPO-058 | As R-REPO-058 |

Rules:

- **Valkey grants nothing.** This page reads two kinds of value from Valkey: negative entries, which
  can only make a remote path answer 404 for their checked lifetime, and flight locks, which can
  only delay a fetch or a refresh, by at most `proxy.fetch_wait_timeout_seconds`; a client that
  asked for expired metadata is served the stale copy meanwhile (R-REPO-026 step 4). No value read from
  Valkey admits a write, grants access or is served to a client (R-ARCH-030); R-REPO-048 therefore
  ignores negative entries.
- **No request depends on an earlier one's replica** (INV-18). Every protocol of this page that
  spans requests keeps its state in PostgreSQL:
  - `If-Match` revisions: `config_version`, `claims_version`, the revision of the proxy-offline
    switch (R-REPO-011, R-REPO-023, R-REPO-031), all three registered as revision columns, so a
    restore raises them ([backup-and-restore.md](backup-and-restore.md) R-BAK-022);
  - job references for delete and reindex (R-REPO-013, R-REPO-016);
  - paging cursors ([api-and-ui.md](api-and-ui.md) R-API-010);
  - idempotency keys of batch and quick-setup calls ([api-and-ui.md](api-and-ui.md) R-API-014).

  A client that resumes a cut download with `Range` is served by any replica, because cache entries
  are shared and the ETag is the content address.
- **Bounds hold without Valkey.** Every bound this page promises across replicas holds while Valkey
  is unavailable: 2 s for configuration, claims, circuit, gone packages and the global offline
  switch, and immediate visibility of local publishes through virtual repositories (R-REPO-020).
  Valkey only makes them faster.
- **No shared volume.** A cache fill spools to the pod's own scratch space ([storage.md](storage.md),
  INV-13). No replica continues another replica's partial transfer, and none needs to.
- **Backup.** Every table of this page has the export class `data`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-004). Negative entries and flight locks are
  in Valkey and are not backed up (INV-19).
  - **Restore without remote caches** ([backup-and-restore.md](backup-and-restore.md) R-BAK-008). A
    backup made with `backup.exclude.remote_caches` holds every table but not the blobs that only
    remote caches reference. Its restore loads everything and then empties each remote
    repository's cache: it deletes the repository's cached items through the storage delete path
    with the trash can bypassed ([storage.md](storage.md) R-STOR-018), so format hooks update
    their indexes and `rev_seq` grows. It deletes the repository's `remote_cache_entries` rows,
    gone marks included, and then the blob rows that are neither referenced nor in the backup's
    pool. The restored remote repositories then behave like freshly created ones with their
    configuration, and the first request for each path fetches from the upstream.
  - Every restore, with or without remote caches, starts without negative entries, and nothing
    has to delete any: they live in Valkey under the old keyspace, which the restored installation
    never reads, and the restore Job holds no Valkey credentials (R-REPO-028, R-BAK-022).
- **Connections.** Nothing on this page holds a PostgreSQL session or a connection across a wait on
  a client, an upstream or Valkey; the proxy's database work is transaction-scoped and runs through
  a transaction-mode pooler ([architecture.md](architecture.md) R-ARCH-019).

Acceptance criteria (e2e profile `scatter` of [architecture.md](architecture.md) R-ARCH-032: three
`web` replicas, every request balanced round robin, the edge's keep-alive to trove off; counting
stub upstream):

- `npm view does-not-exist-4711` three times through `npm-remote`: the stub records one request.
  With Valkey stopped, it records at most three.
- With Valkey stopped, `nuget-remote` has `Acme.Gone` `1.0.0` cached, and after its period the stub
  answers 404 for the version list: within 2 s every replica answers 404 for the cached `.nupkg`
  (R-REPO-029).
- `npm ci` of a fixture with 400 dependencies through `npm-all` on a cold cache, with
  `valkey-cli FLUSHALL` run twice during the install: the install succeeds, and the stub records at
  most three requests for any one path.
- With Valkey stopped for the whole e2e client suite in this profile, the suite passes, and every
  replica stays ready with `"degraded":["valkey"]`.
- `kubectl drain` of the node of one `web` replica during an `npm ci` loop on a cold cache through
  `npm-all`, while the stub delays every tarball by 2 s: every `npm ci` exits `0` with npm's default
  retries, and no cache entry exists for a partial body.

## Observability

### R-REPO-047 · Proxy and resolution observability
Priority: **MVP** · Covers: trove-only

- Every response of a remote or virtual repository MUST carry a `Cache-Status` header
  ([RFC 9211](https://www.rfc-editor.org/rfc/rfc9211)) with the cache name `trove`, except on an
  endpoint with exposure rules, where no answer carries it ([endpoints.md](endpoints.md) R-EP-018,
  Q-EP-60):

| Situation | Header value |
|---|---|
| Fresh hit | `trove; hit; ttl=<seconds left>` |
| Miss, fetched and stored | `trove; fwd=uri-miss; fwd-status=200; stored` |
| Expired, revalidated | `trove; fwd=stale; fwd-status=304` (or `fwd-status=200; stored`) |
| Stale served after a failed or slow refresh | `trove; fwd=stale; fwd-status=<status, if any>; detail=served-stale` |
| Stale served without asking (offline, circuit open, no cache-fill right) | `trove; hit; detail=served-stale` |
| Negative entry | `trove; hit; detail=negative` |
| Collapsed onto another request's fetch | additional parameter `collapsed` |
| Partial merge (R-REPO-020) | `trove; detail=partial` |
| Merged document served from the merged-metadata cache | `trove; hit; ttl=<seconds left>` |
| Merged document built for this request | `trove; fwd=miss` |

`trove_virtual_resolutions_total` counts one resolution per artifact request and per metadata
request of a virtual repository, by the tier that served it: `local`, `cache` (a merged document
served from the merged-metadata cache or built from stored remote documents only, an artifact from
a remote member's cache), `upstream` (an artifact fetched from, or a merge that refreshed a
document from, an upstream) and `none` (404 or a failure). `trove_shadow_checks_total` counts the
checks that ran: a write that the check does not apply to (L already holds a version, or a claim of
L covers the name) is not counted (Q-REPO-115).

- Metrics: the remote-repository metrics of [operations-and-observability.md](operations-and-observability.md)
  apply as defined there (`trove_remote_cache_lookups_total`, `trove_remote_upstream_requests_total`,
  `trove_remote_upstream_request_duration_seconds`, `trove_remote_upstream_up`, which is 0 while
  the circuit of R-REPO-029 is open, and `trove_remote_collapsed_requests_total`). This page adds,
  with the same label conventions:

| Metric | Type | Labels |
|---|---|---|
| `trove_flight_lock_bypass_total` | counter | `reason`: `valkey_unavailable` ([architecture.md](architecture.md) R-ARCH-033, which defines the metric), `wait_timeout` and `draining` (R-REPO-026). Until 2026-10-28 this row was `trove_remote_lock_fallbacks_total`, for the exhausted lock pool. |
| `trove_remote_source_network_unavailable_total` | counter | `repo` (R-REPO-024: the replica has no address in `sourceNetwork`) |
| `trove_remote_fetch_queue_wait_seconds` | histogram | `repo` |
| `trove_remote_integrity_failures_total` | counter | `repo`, `reason` (`mismatch`, `missing`, `digest_changed`) |
| `trove_remote_egress_denied_total` | counter | `repo` |
| `trove_virtual_resolutions_total` | counter | `repo`, `source` (`local`, `cache`, `upstream`, `none`) |
| `trove_client_certificate_expiry_timestamp_seconds` | gauge | `name` |
| `trove_shadow_checks_total` | counter | `repo` (the local repository), `outcome` (`no-exposure`, `clear`, `shadowing-approved`, `refused`, `undetermined-approved`, `unavailable`; R-REPO-048) |

- Every upstream request writes one record of the `upstream` log
  ([R-OPS-011](operations-and-observability.md)); this page adds the fields `flight_role`
  (`leader`, `follower`, `waiter`) and `cache_status`. The role says how the request that sends
  the upstream request came to send it (Q-REPO-92): a `leader` fetches without having joined or
  waited first, a `follower` first joined a flight of its replica that ended with nothing to
  read and then fetched itself, a `waiter` first waited for another replica's flight that ended
  without an entry and then fetched.
- The same records carry `slot_wait_ms` (Q-REPO-236): how long the fetch waited for its fetch slot (R-REPO-040) before
  the request was sent, `0` when it took one at once. A background request reports the wait for its background slot,
  the yield windows of R-REPO-055 included (Q-REPO-179). The first upstream request sent under a slot reports the wait;
  every further one under the same slot (the retry of R-REPO-029, the item after the discovery document resolved under
  a background slot) reports `0`, and so does every hop after the first of a request that follows redirects (one request is one chain). A request that takes no slot (`Exchange`, the upstream test of R-REPO-024) has `null`.
  With `duration_ms` it tells a request that queued for the upstream's budget from one whose upstream was slow.
- Audit events (INV-16): `repository.created`, `repository.updated`, `repository.disabled` (an
  update that sets `disabled`, beside its `repository.updated`), `repository.narrowed` (an update that narrows, beside its
  `repository.updated`, journaled, R-REPO-061), `repository.deleted`,
  `repository.cache_invalidated`, `repository.claims_changed` (journaled, R-REPO-061), `proxy.global_offline_changed`,
  `proxy.client_certificate_changed`, `proxy.outbound_proxy_changed`. Security events:
  `proxy.integrity_mismatch`, `proxy.upstream_digest_changed`, `proxy.tls_untrusted`,
  `proxy.egress_denied`, `proxy.upstream_gone`, `repository.remote_package_shadowed` (R-REPO-048).
  Shadowing-check probes write `upstream` log records with `flight_role: probe`.

Acceptance criteria:

- `curl -sI …/api/npm/web/npm-remote/lodash` shows a `Cache-Status` header of the documented form on the
  first and on the second request.
- After a stub outage opens the circuit, `trove_remote_upstream_up{repo="npm-remote"}` is 0 on
  every replica within 2 s.

## Deferred

<details>
<summary>Deferred catalog entries (44)</summary>

| ID | Priority | One line |
|---|---|---|
| REPO-014 | P2 | Sharing a repository with other projects. No repository is shared (owner, Q-AUTHZ-206): a project that needs another project's packages reads them with a grant on that project's repository. |
| REPO-015 | P2 | A remote repository connected to a shared repository of another project. Each project has its own remote repositories (Q-REPO-212). |
| REPO-019 | P2 | Per-repository download redirect; the mechanism is R-STOR-014 in [storage.md](storage.md). |
| REPO-021 | Later | Indexing repositories for a vulnerability and license scanner. |
| REPO-032 | P2 | Local settings of further formats; each arrives with its format page. |
| REPO-039 | P2 | Virtual repositories fetching remote content on behalf of other instances. |
| REPO-042 | P2 | Signing keys on virtual repositories. |
| REPO-044 | P2 | Virtual settings of further formats; each arrives with its format page. |
| REPO-048 | P2 | Layout path patterns with tokens and custom tokens. |
| REPO-049 | P2 | Integration-revision expressions of layouts. |
| REPO-050 | P2 | Layout management (create, duplicate, test). |
| REPO-051 | P2 | Different layouts per class and path translation between them. |
| REPO-052 | P2 | Layout-based maintenance (snapshot cleanup, path conversion). |
| REPO-060 | P2 | Existence check for repositories of given classes. |
| REPO-065 | P2 | Deprecated shared-configuration read of a remote repository. |
| REPO-066 | Later | Immutable release-set storage repositories. |
| REPO-067 | P2 | Guided NuGet normalization migration of existing repositories. |
| REPO-068 | P2 | A system-wide repository search order outside virtual repositories. |
| RMT-010 | P2 | Static query parameters on every upstream request. |
| RMT-011 | P2 | Forwarding client query parameters to the upstream. |
| RMT-013 | P2 | A cookie store per remote repository. |
| RMT-014 | P2 | Turning off URL normalization of upstream requests. |
| RMT-024 | P2 | Skipping `HEAD` requests to the upstream. |
| RMT-025 | P2 | Eager background fetch of companion artifacts (Maven family). |
| RMT-027 | P2 | Deprecated cache pre-population operations. |
| RMT-028 | P2 | Download redirect for cached artifacts (with REPO-019). |
| RMT-032 | P2 | The unspecified hard-fail switch; R-REPO-029 makes failure behavior explicit instead. |
| RMT-035 | P2 | A cache layout that differs from the upstream layout. |
| RMT-038 | P2 | Listing upstream folders that are not cached. |
| RMT-039 | P2 | Sharing a remote configuration with all authenticated users. |
| RMT-040 | P2 | Chained remote repositories pointing at another trove instance, with extra exchanges. |
| RMT-041 | P2 | Reporting downloads back to a chained source. |
| RMT-042 | P2 | Synchronizing properties from a chained source. |
| RMT-043 | P2 | Detecting deletion at a chained source. |
| RMT-044 | P2 | Uncached retrieval for chained requests through virtual repositories. |
| RMT-045 | P2 | Searching through a chained remote. |
| RMT-046 | P2 | Parallel ranged download of large files from a chained source. |
| RMT-047 | P2 | Project-scoped remote repositories. |
| ART-044 | P2 | Browsing uncached remote content (with RMT-038). |
| API-086 | P2 | Multi-target push replication from a local repository; the mirror mode pulls only (Q-REPO-143). |
| API-087 | P2 | Enabling and disabling replications in bulk by target URL; a mirror is paused through its own document, and few repositories are mirrors (Q-REPO-143). |
| API-088 | P2 | Event channels between instances for event-based pull; a feed is polled by the passes, and peer channels need trove-to-trove chaining (RMT-040) first (Q-REPO-143). |
| API-089 | P2 | A global block of pull replication; the global offline switch (R-REPO-031) stops every mirror with all upstream traffic, `paused` stops one (Q-REPO-143). |
| API-090 | P2 | Checksum-only replication for stores linked by storage-level replication; the mirror always transfers the bytes it stores (Q-REPO-143). |

</details>

Out of scope: REPO-020 (download redirect through a CDN operated as part of a hosted service).

## Handed over

MVP and P1 entries of the covered catalog pages that this page does not specify:

| IDs | Owner |
|---|---|
| ART-003, ART-021 to ART-026, ART-032 to ART-034, ART-036 to ART-038 (properties, property sets, copy, move, delete, trash can) | [storage.md](storage.md), R-STOR-009 and R-STOR-018 to R-STOR-023 |
| IAM-023, IAM-024 (cache fills, access through virtual repositories) | [authorization.md](authorization.md), R-AUTHZ-008 and R-AUTHZ-009 |
| REPO-013, REPO-045 (projects; environments are P2) | [authorization.md](authorization.md), R-AUTHZ-026 and R-AUTHZ-009 |
| REPO-029, REPO-040 | [formats/npm.md](formats/npm.md) |
| REPO-030, REPO-031 | [formats/nuget.md](formats/nuget.md) |
| REPO-022, REPO-023, REPO-024, REPO-026, REPO-041, RMT-036 | the Maven-family format page, not yet in the page table |
| REPO-027 (container API version, legacy manifests), REPO-043 | the container format page, not yet in the page table; the tag caps of REPO-027 are R-REPO-007 |
| REPO-003 | the generic format page, not yet in the page table |
| UI part of REPO-007 | [api-and-ui.md](api-and-ui.md) |

## Deliberate deviations from the catalog

| Catalog behavior | trove | Why |
|---|---|---|
| Create and update answer plain text; duplicates and validation errors answer 400 | JSON; 409 for an existing key; 422 listing every violation (R-REPO-008, R-REPO-009) | Machine-readable, one round trip to fix a document |
| Configuration reads return every setting the system knows | Only members of the class and format (R-REPO-010) | No dead fields to misread |
| Unknown members in configuration documents tolerated | Refused with 400 (R-REPO-008) | A misspelt setting must not be silently ignored |
| Cache view through a reserved key suffix; shorter remote keys | `<key>~cache`; no reserved suffix, 64 characters for every class (R-REPO-002, R-REPO-032) | `~` cannot occur in keys |
| Patterns do not apply to administrators | They apply to every package-protocol request (R-REPO-005) | Routing safety must not depend on who runs the build |
| Patterns are paths for every format | Name patterns for npm and NuGet (R-REPO-005) | One pattern covers documents and files |
| Metadata period minimum 30 s | `0` allowed (R-REPO-027, Q-REPO-2) | Conditional, collapsed revalidation is cheap |
| Ten background retries before a repository counts as offline | Consecutive-failure threshold and probe (R-REPO-029) | Deterministic, shared across replicas |
| Repository delete answers with a report (200, 207) | Asynchronous job, 202 (R-REPO-013) | Deleting a large repository outlives an HTTP request |
| Non-package content accepted into format repositories | Refused for npm and NuGet (R-REPO-001) | Unindexed content is invisible to clients |
| NTLM proxies; proxy credentials reused for redirect target hosts | Basic proxy authentication only; proxy credentials go to the proxy only (R-REPO-037) | Smaller credential exposure |
| One local IP address for upstream connections | A network prefix; each replica takes its own address from it (R-REPO-024, Q-REPO-9) | Replicas are interchangeable, and every pod has its own address |
| Pull replication is a document per target with its own get, set, update and delete operations (API-082, API-083) | The mirror's settings are the members `remote.mode` and `remote.mirror` of the repository's document, read and changed with it (R-REPO-050, Q-REPO-130) | A remote has one upstream; one document, one `ETag`, one audit event, one configuration-as-code object |
| Replication runs on a cron expression | An interval per repository from the start of the last pass (R-REPO-053, Q-REPO-131) | Mirrors do not all start in the same minute, and a long pass is not started again on top of itself |
| Pull replication lists the source instance's storage | Discovery through the format's protocol: a feed, a listing or exact names (R-REPO-051) | Works against public registries, not only against another instance of the same system |
| Replication status is derived from properties written on items, per path (API-084) | One status document per repository from the mirror's state (R-REPO-056) | One read; stored items carry no bookkeeping |
| Deletes at the source can be synchronized | The mirror deletes nothing; a takedown reaches clients through the refreshed document (R-REPO-029, Q-REPO-144) | A deletion upstream must not destroy what builds pinned; an operator deletes on purpose |

## Security notes

- **Dependency confusion** is closed on the server by namespace claims and pin rules (R-REPO-023),
  evaluated on stored configuration, never on the caller's permissions, and enforced for remote
  repositories addressed directly as well for `global` claims. R-AUTHZ-009 of
  [authorization.md](authorization.md) relies on it for virtual repositories that a caller can
  read only in part. Auto claims stay within the virtual repositories that contain the claiming
  repository, so one publish into a sandbox cannot take a public name away from everyone (H-9).
- **Shadowing a public package is a `manage` decision.** The first publish of a name that a remote
  member of a containing virtual repository serves needs `manage` or a prior manual claim, and fails
  closed when the upstream cannot be asked; a broad `deploy` grant or a leaked CI token can no
  longer replace a public package for every consumer (R-REPO-048, H-10). The check also covers
  copy, move and restore into a local repository, and an auto claim never takes a name from
  another repository's claim (R-REPO-023).
- **Upstream takedowns reach the cache.** A package whose package document the upstream removed is
  no longer served from the cache, artifacts included, also to lockfile clients that request
  artifacts by exact path (R-REPO-029). Version-level removals depend on the format (H-5).
- **One pattern matcher.** Routing, permission and retention patterns are evaluated by the same
  R-AUTHZ-005 code, so a pattern copied between them cannot silently select something else
  (R-REPO-005). On npm a bare `*` does not cover scoped names; a save that relies on it returns
  `pattern_unscoped_only`, because `excludePatterns: ["*"]` would otherwise still send scoped
  internal names upstream.
- **Upstream paths are re-encoded.** The once-decoded canonical path is percent-encoded again
  before it reaches an upstream, and documents map only their own package's paths (R-REPO-025,
  R-REPO-043).
- **Cache fills** follow [R-AUTHZ-008](authorization.md). With the default `cacheFillAction: read`
  and anonymous read on a remote repository, anyone can make trove download arbitrary upstream
  packages; the concurrency limits (R-REPO-040) bound the rate, not the volume.
- **Merged metadata never crosses permission boundaries:** its cache key contains the fingerprint of
  the members the caller can read (R-REPO-020).
- **No client-chosen upstream hosts.** Upstream URLs come from configuration or from recorded upstream
  metadata, never from request paths (R-REPO-043); every connection passes the egress rules at dial
  time (R-REPO-038); redirects cannot downgrade to plain HTTP and carry no credentials to other
  origins by default (R-REPO-039).
- **Secrets** are write-only and either sealed or referenced; references cannot reach trove's own
  secrets (R-REPO-033). Logs carry redacted URLs.
- **Integrity:** announced digests are verified before a client can receive a complete response, and
  a changed digest for a cached version raises a security event instead of replacing content
  (R-REPO-041). TLS verification cannot be switched off per repository (R-REPO-035).
- **Cache poisoning through `Host`:** rewriting uses the configured base URLs at serve time
  (R-REPO-043).
- **Kill switch:** the global offline switch stops all upstream traffic within seconds (R-REPO-031),
  also while Valkey is unavailable, because it travels through the PostgreSQL change log.
- **Valkey grants nothing on this page.** The proxy reads negative entries and flight locks from
  Valkey. A negative entry can only make a remote path answer 404, and a flight lock can only delay
  a fetch or a metadata refresh until `proxy.fetch_wait_timeout_seconds`, after which the replica
  fetches without it; meanwhile expired metadata is served stale, so a held lock cannot freeze a
  package list (R-REPO-026, R-REPO-028). Cache entries, stale copies and merged documents come from PostgreSQL,
  S3 or the replica's own memory. The shadowing check ignores negative entries, so a forged entry
  cannot let a `deploy` holder shadow a public package (R-REPO-048). What remains is the denial of
  service that [architecture.md](architecture.md) H-49 describes.
- **Duplicate fetches never mix bytes.** Lost or bypassed flight locks lead to concurrent fetches;
  when they return different bytes for one artifact, the first commit wins, the other leader aborts
  its client before the tail, which every leader withholds until its entry has committed, and a
  security event is written (R-REPO-026, R-REPO-041).
- **Explanations do not leak member keys** to callers who cannot read those members (R-REPO-019).
- **A mirror amplifies the cache-fill question** of R-AUTHZ-008: it fills without a client, as the system, for every name its
  patterns admit. Switching a remote to `mirror` therefore needs `repositories:write-config`; an Owner without `repositories:write-config` keeps,
  pauses or turns off what an administrator set (Q-REPO-142). Its volume is bounded by its patterns, `maxBytes` and, once
  built, the quotas (R-REPO-052); a well-known public upstream cannot be mirrored without a bound. A mirror stores content
  before anyone asked for it, so a malicious version under its patterns is stored sooner (H-503); the download blocking of
  [vulnerability-scanning.md](vulnerability-scanning.md) applies when it is served. Its requests pass the same egress rules,
  credentials policy and host checks as client fetches; npm's replication host is the one destination a format adds
  (H-504). Rate limits the mirror provokes never close the circuit for clients (R-REPO-054).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-REPO-1 · Who specifies the stored-secret mechanism (IAM-045)?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) This page | R-REPO-033 specifies key handling, the sealed format and rotation for repository secrets | Everything about upstream credentials in one place | Client-certificate keys, outbound-proxy passwords and webhook secrets (R-OPS-042) need the same mechanism; a second specification means a second key, a second rotation procedure and a second backup item |
| (b) [architecture.md](architecture.md) for every stored secret | R-ARCH-021 owns the key ring, the sealed format `v1:<key id>:…`, rotation and the start-up canary; this page states what repository secrets rely on and uses it | One key ring, one rotation job, one backup item; R-ARCH-021 and the chart side (R-HA-027) already specify it | Every replica holds the key (H-8) |
| (c) References only | Secret fields accept only `secretRef`; nothing is sealed in PostgreSQL | No key to manage | Credentials cannot be entered through the API or UI at all; every new upstream token needs a Secret change and a file on every replica |
| (d) trove writes Kubernetes Secrets | Values entered through the API are written by trove into Kubernetes Secrets and read back at use time | No key ring of trove's own; the cluster's encryption at rest applies | trove needs RBAC to create and read Secrets in its namespace, so a compromised replica reads every Secret there, the database password included; the Kubernetes API becomes a stateful dependency, which INV-3 rules out |
| (e) External key management service | Values are sealed and opened by an external service (a cloud KMS or a secrets engine's transit API); trove holds no key | The key never sits on a replica, which closes H-8 | Every unseal becomes a call to a further stateful dependency beside PostgreSQL and S3, which INV-3 rules out for the baseline; an outage of that service stops every authenticated upstream fetch; many installations have no such service |

**Choice:** (b) — R-ARCH-021 is the one stored-secret mechanism for every stored secret, and
R-REPO-033 only uses it.

**Rationale:** (b) is the only option that keeps one key, one rotation and one backup procedure
while still allowing credentials to be typed into the API and UI. (a) would duplicate a
security-critical primitive, and two implementations of sealing drift apart. (c) gives up entering
credentials through the API, and with it through the UI, which uses only the API (INV-17). (d)
widens trove's privilege footprint to reading Secrets, which is worse than H-8. (e) is the only
option that removes H-8, but it makes a further service a hard dependency of every authenticated
fetch, and it does not need a mechanism of its own: a key ring whose keys an external service
unwraps fits into R-ARCH-021 as an additional key source. The development agent would revisit this
if an installation must keep the key off the replicas; (e) would then arrive as that key source,
not as a second mechanism.

**Applied in the spec:** R-REPO-033: the "Sealing" bullet says that inline values MUST be sealed
with R-ARCH-021 and that this page defines no key, format or rotation of its own. It lists the
properties this page relies on as checks (checked against R-ARCH-021: AES-256-GCM, 96-bit random
nonce, associated data of table, primary key and column, key identifier in the sealed form,
`secrets-reencrypt`), among them the binding to `repo_id` and `field`. A new acceptance criterion:
a sealed value copied into another repository's row fails with 502 `upstream_secret_unavailable`.

### Q-REPO-2 · Lower bound of the metadata period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stays (a). Cross-replica collapse now runs through a Valkey lock (R-REPO-026), so while Valkey is unavailable a repository set to `0` sends up to one conditional request per replica and flight instead of one per cluster; the per-repository fetch limit became a cluster budget (Q-REPO-8), so the bound on the upstream no longer grows with the replica count. The rationale names both. R-REPO-027's rule that serves collapsed waiters under `0` now compares database values instead of a replica clock.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Allow `0` | `metadataTtlSeconds: 0` revalidates on every request | Teams that need a just-published upstream version get it at once; revalidation is a conditional header exchange | More upstream round trips and request latency for repositories set to `0` |
| (b) Minimum 30 s | Values below 30 are refused, as in the catalog | Familiar bound; caps request volume per document | Up to 30 s of forced staleness; protects the upstream from nothing that collapsing and the fetch limits do not already prevent |
| (c) `0` only with validators | `0` applies when the upstream sends `ETag` or `Last-Modified`; otherwise a 30 s floor | Prevents repeated full transfers from upstreams without validators | The same setting behaves differently per upstream; one more branch to build and test, for a rare case in which the operator asked for fresh data anyway |

**Choice:** (a) — `0` is accepted and means "revalidate on every request"; there is no minimum.

**Rationale:** Revalidation is conditional, so a 304 costs headers only. Concurrent requests
collapse into one flight per cluster through the Valkey lock, and into one per replica while Valkey
is unavailable (R-REPO-026). The per-repository fetch budget, shared out over the replicas, the
per-replica fetch limit (R-REPO-040) and the circuit (R-REPO-029) bound the load. Both default upstreams send
validators (checked on 2026-10-28: the npm registry's package documents and nuget.org's version
lists answer with `ETag` and `Last-Modified`), so (c) would add a branch for upstreams nobody uses
by default. A floor therefore protects nothing and only takes freshness away from the operator. The
development agent would revisit this if upstreams answer 429 to trove because of repositories set
to `0`; a floor or a save-time warning would then be added.

**Applied in the spec:** R-REPO-027 says that a period of `0` MUST be accepted, with the reasons
(R-REPO-026, R-REPO-040). It now also says that an entry validated after a request first looked it
up is fresh for that request; without that rule, every cross-replica waiter of R-REPO-026 would
fetch again under `0`, so the collapse that justifies this option would not hold. On 2026-10-28
the rule changed from "validated after the request arrived" to a comparison of the `validated_at`
values the request read, because the waiters it serves run on other replicas, whose clocks need
not agree with the database's. Its acceptance criterion
covers five sequential requests (five conditional upstream requests) and 20 concurrent ones on two
replicas (one). R-REPO-024 and R-REPO-027 now list the NuGet format default of 300 s that
[formats/nuget.md](formats/nuget.md) decided (Q-NUGET-2), where they had still said 7200 s. The
catalog deviation row stays.

### Q-REPO-3 · Generic default when the upstream removes a cached document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Mark gone, hide the document, keep artifacts | The document is marked `upstream_gone` and answers 404 (also on the cache view and in merges); cached artifacts stay served by exact path | Builds pinned to a legitimately removed package keep working; least to build | Lockfile clients request artifacts by exact path (`npm ci`, R-NPM-025), so the cache keeps handing out what the upstream took down to every project that already pinned it; [formats/npm.md](formats/npm.md) rejected the same behavior for npm for that reason (Q-NPM-2) |
| (b) Mark the package gone, hide its document and every artifact | A 404 or 410 for a package's package document (R-REPO-003) marks the package gone: its documents and artifacts, cached or not, answer 404 on the remote, its cache view and in virtual repositories; items stay stored and copyable; formats that track versions replace the rule | A takedown reaches lockfile clients too; needs only what the generic layer has (the descriptor's package name and the registered package-document path); the same stance as npm's decided default | A build that needs a legitimately removed package breaks until someone copies it into a local repository; a spurious upstream 404 hides cached artifacts until the negative entry expires or is invalidated |
| (c) Keep serving the stale document | The upstream 404 is treated like an outage | Maximum availability | Keeps advertising and serving packages after their takedown |
| (d) Debounce | The mark is set only after a second 404 at least one negative period later; the stale document is served until then | Survives a transient upstream 404 | Delays every takedown by `notFoundTtlSeconds` (30 min by default), while a transient 404 already heals when its negative entry expires and can be cleared at once by an invalidation (R-REPO-045) |
| (e) (b) with a per-remote availability switch | As (b), plus a generic `remote.cache.serveUpstreamRemoved` that keeps serving | Availability-first remotes can opt out, as npm remotes can with `serveRemovedVersions` | A second switch with overlapping meaning next to npm's own, or a rename on the npm page; copying the package into a local repository already covers the legitimate case, per package instead of per remote |

**Choice:** (b) — a 404 or 410 for a package's package document marks the package gone, and the
remote stops serving its documents and artifacts, cached ones included, until the upstream serves
it again; formats that track removal per version replace the rule.

**Rationale:** The upstream is the authority on its own content, and a removal is mostly a
takedown: nuget.org does not let owners delete packages and deletes them itself only for
potentially harmful content or infringement
([deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages)).
The decisive client fact is that lockfile clients fetch artifacts by exact path. (a) therefore
keeps the payload flowing to every project that pinned it, which is why the npm page decided
against that behavior (Q-NPM-2). A generic default weaker than the one format that decided would
come back with every later format whose clients fetch by path. The earlier argument against (b),
that only the format knows which artifacts belong to a package, does not hold: every request
descriptor carries the normalized package name (R-REPO-003), and the format registers the
package-document path, which R-REPO-048 needs anyway. The availability cost is bounded: the mark
heals by itself after the negative period or at once through an invalidation, and a build that
needs a legitimately removed package keeps it by copying it into a local repository, which also
records that decision. (c) and (d) keep serving takedowns. (e) adds a second switch for a case that
copying covers. The development agent would revisit this if spurious upstream 404s cause outages
in practice, or if operators of formats other than npm ask for a per-remote opt-out; that would
then be one generic switch with npm's folded into it.

**Applied in the spec:** This review changed the choice from (a) to (b). R-REPO-003: formats
register the package-document path. R-REPO-025: step 4 treats entries of gone packages as absent,
and a new "Gone packages" bullet answers 404 after step 3 without an upstream request; when the
package document's negative entry is no longer valid, an artifact request first refreshes the
document, so the mark heals for clients that never request documents. The data model allows the
mark only on metadata entries and adds a partial index for loading. R-REPO-029: "Upstream gone" is
split into the document rule and a "Gone packages" rule; replicas hold the gone packages in memory,
reloaded through `cluster.Notify("upstream_state", …)`, the change area of
[architecture.md](architecture.md) R-ARCH-018, which needs neither `NOTIFY` nor Valkey (until
2026-10-28 the `NOTIFY` channel `trove_upstream_state`); npm's replacement is restated, and since
2026-10-28 it says that npm reads `upstream_gone_at` per request and serves a cached artifact only
after its package document is current ([formats/npm.md](formats/npm.md) Q-NPM-13). The NuGet acceptance
criterion now expects 404 for the cached `.nupkg` on every address and a self-healing request for
the `.nupkg` alone. R-REPO-032: the cache view answers 404 for gone packages, and their items stay
listed and copyable. Security notes: new bullet. H-5: narrowed to versions withdrawn from a package
that still exists, and renamed accordingly.

### Q-REPO-4 · Scope of remote refusal for claimed names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) System-wide, `global` claims only | Every remote repository of the format refuses `global`-claimed names, also when addressed directly; `proxy.claims.remote_refusal: true`; pin rules route a claimed name to a named remote | Closes the direct-access path where internal names leak to public registries; only `repositories:write-config` can create the effect | A remote addressed directly cannot serve a globally claimed name at all |
| (b) Inside containing virtual repositories only | Refusal applies only where a claiming member is next to the remote | No effect outside the virtual repositories that hold the name | A forgotten `.npmrc` or a hard-coded CI source pointing at the remote still sends the internal name upstream and installs the public package |
| (c) System-wide with a per-remote exemption | As (a), plus `remote.honourGlobalClaims: false`, settable with `repositories:write-config`, which exempts one remote | A partner remote addressed directly can serve a claimed scope | One setting on a public-registry remote reopens the leak for every client that addresses it; the partner case already works through a pin rule in a virtual repository; one more security setting to audit per remote |

**Choice:** (a) — every remote repository of the format refuses `global`-claimed names, also when
addressed directly, unless a pin rule of the requesting virtual repository names that remote.

**Rationale:** Clients that point straight at a remote repository are where dependency confusion
happens in practice. The request itself discloses the internal name upstream, and the answer is the
attacker's package. (b) leaves exactly that path open. (c) closes it by default but lets a single
setting reopen it for every client of a remote, while the legitimate case it serves, a partner
registry that owns a claimed scope, already works through a pin rule. Because refusal follows only
`global` claims, which need `repositories:write-config` (R-REPO-023), a publish can never cause it
as a side effect. The development agent would revisit this if installations need a directly
addressed remote to serve a globally claimed name; (c) would then be added, and the system switch
would not be turned off.

**Applied in the spec:** No change; the requirement text already follows this option (R-REPO-023,
"Remote refusal"; R-REPO-025 step 2; the `proxy.claims.remote_refusal` default).

### Q-REPO-5 · Generic or format-level placement of pins, extra hosts and upstream authentication

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Generic members | `virtual.pinning`, the claims API, `remote.extraHosts`, `remote.authMode`, `remote.username`, `remote.password` on this page for every format; format pages add semantics, propose defaults and may narrow | One validator, one `secretRef` syntax (R-OPS-029), uniform configuration-as-code documents; claims are cross-format (BEYOND-028) | A format with an unusual host model has to fit `extraHosts` or handle it in its protocol handshake |
| (b) Format-level members | `npm.pinned`, `npm.remote.tarballHosts`, `npm.upstreamAuth` with `secret://` references, and the same again per format | Each format page is self-contained | One mechanism implemented and validated once per format; a second reference syntax; a pattern copied between formats lands in a different member |
| (c) Split | Pins and credentials generic; extra hosts format-level, because the format knows which hosts its upstream needs | Host knowledge stays with the format | The egress-relevant settings of one remote sit in two sections, so a security review has to read both; formats already contribute host defaults through the format catalog |

**Choice:** (a) — pins, claims, extra hosts and upstream credentials are generic members on this
page. Format pages add semantics, may narrow these members and propose defaults, but never define
their own members for them.

**Rationale:** The mechanisms are identical across formats. Implementing them once means one code
path to secure and one place to audit, and configuration as code stays uniform. Formats keep their
knowledge through the format catalog, whose registration now includes the extra hosts a format
proposes for its default upstream (NuGet: `*.nuget.org` without credentials, R-NUGET-014), and
through semantics on their own pages (npm: which account name the public registry expects with
`basic`, R-NPM-023). The development agent would revisit this when a format needs a host or
credential model that `extraHosts` and `authMode` cannot express. A token realm of a container
registry is the expected case, and it belongs in the plug-in's handshake (R-REPO-033).

**Applied in the spec:** R-REPO-004: new bullet; the mechanisms are generic members, format
sections MUST NOT define their own, and format settings live under a member named after the format.
This review corrected its example and the one in R-REPO-033: both claimed that npm sends
credentials only to the upstream URL's own host, but [formats/npm.md](formats/npm.md) R-NPM-023
sends them to the upstream origin and to `extraHosts` with `sendCredentials: true`; the bullets now
name the real format contributions. R-REPO-003: the format registration includes the proposed
`extraHosts`. R-REPO-043: "`remote.extraHosts` is the only host list". The format pages are aligned
through the cross-page consequences.

### Q-REPO-6 · Local publish of a name that a remote member already serves (H-10)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stays (b). Negative entries moved to Valkey (R-REPO-028), and a value read from Valkey may only make trove refuse, never admit a write ([architecture.md](architecture.md) R-ARCH-030). The serve decision of R-REPO-048 therefore no longer reads negative entries: a name for which a remote holds only a negative entry is probed. "Stored state" in option (e) now means PostgreSQL entries only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse | The first publish of a name that a remote member of a containing virtual repository serves is refused for everyone; shadowing on purpose needs a manual claim created beforehand | Strongest default | Two steps even for an administrator; a patched fork of a public package, a legitimate case, always needs a separate claim first |
| (b) Require `manage` | Such a first publish needs `manage` on the coordinate (or `content:bypass`), or a manual claim of the local repository that covers the name, which needs `manage` itself; otherwise 403 `shadows_remote_package`; unreachable upstreams fail closed | A `deploy` grant alone, a broad grant or a leaked CI token, can no longer replace a public package; the deliberate case stays one step for a `manage` holder and is audited; CI is approved in advance through a claim | One upstream probe per new name and exposed remote member, which also tells that upstream the name; first publishes of new names answer 503 while such an upstream is unreachable |
| (c) Warn only | The publish succeeds; a security audit event, a metric and a response warning are emitted | No friction, no upstream dependency at publish time | Detects the attack after every consumer has resolved the replaced package |
| (d) Package-name patterns on deploy grants (state before this decision) | Only narrow `deploy` grants limit which names a principal can publish | Nothing to build | Safety depends on every grant being narrow; broad grants are common, and nothing tells the administrator that a grant is too broad |
| (e) As (b), decided from stored state only | "Serves" is decided only from the remote's stored entries; no probe | No upstream request and no 503 at publish time | A name nobody has resolved through the remote yet, or whose entries the cache cleanup removed (R-REPO-046), passes unchecked, so a `deploy` holder can still take a public name before its first consumer adopts it |

**Choice:** (b) — the first publish of a name that a remote member of a containing virtual
repository serves requires `manage` (or a prior manual claim), is audited, and fails closed when the
upstream cannot be asked (R-REPO-048).

**Rationale:** The harm in H-10 is that a `deploy` grant, the right that CI jobs and broad team
grants hold, silently becomes the power to replace a public package for every consumer. (b) turns
that into an explicit `manage` decision without blocking the legitimate case of patched forks. (a)
buys no protection that (b) lacks, since a `manage` holder can create the claim in the step before,
and it adds that step to every fork. (c) only reports damage that has already happened, and (d) is
the state that left H-10 live. (e) avoids the probe but leaves open exactly the names that matter
next: public packages a team is about to adopt. The check runs only on the first version of a name
outside the repository's claims. Squatting a public name after an internal first publish therefore
cannot block internal releases, and pre-claiming an internal prefix (`@acme/*`, `Acme.*`) removes
the check, and with it the probe, for that prefix. Probes store nothing and ignore the caller's
cache-fill right, so a frozen mirror stays frozen. A check that only publish ran could be bypassed:
publish into a repository that no virtual repository contains, then copy into the shared one; and
an auto claim, being an exact name, would take a name from another team's prefix claim. Both are
closed in the requirement text. The development agent would revisit this if collisions between
internal unscoped names and public names make the check fire often, or if probe latency becomes a
problem for publishing pipelines; a per-repository opt-out for repositories that hold forks by
design would then be weighed.

**Applied in the spec:** New requirement R-REPO-048 (MVP, covers BEYOND-028). It defines when the
check runs, the exposure set, the stored-state-first serve decision with a non-storing probe, the
outcome table (403 `shadows_remote_package`, 503 `shadow_check_unavailable`), approval in advance
through a manual claim, the error body and the acceptance criteria. This review extended it to the
P1 item operations that give a local repository its first version of a name (copy, move, trash-can
restore; refusals reported per item, with an acceptance criterion), and corrected the probe timing
to after R-ARCH-014 steps 4 and 5 and before step 6, because the outcome depends on the caller's
`manage`. R-REPO-003: formats register the package-document path the probe uses. R-REPO-023: an
auto claim is not created when another repository's claim in effect in a containing virtual
repository covers the name (new bullet and acceptance criterion); R-REPO-022 and R-REPO-023
acceptance criteria that publish a name the stub already serves now publish as a `manage` holder.
R-REPO-047: metric `trove_shadow_checks_total`, security event
`repository.remote_package_shadowed`, and probe log records. Security notes: new bullet. H-10:
rewritten to the remaining gap, with configuration changes and the disclosure of new names to
upstreams added in this review. On 2026-10-28: R-REPO-048 step 2 consults stored gone marks only,
no negative entries, with a new acceptance criterion in which a hand-written Valkey entry does not
let `ci-bot` publish; the security notes say so.

### Q-REPO-7 · Remote fetches on a replica that is shutting down

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A node drain, a rolling upgrade or a scale-down sends `SIGTERM` to a `web` replica that may hold
flights other replicas wait for (R-REPO-026). The replica keeps serving for
`health.shutdown_drain_delay` (5 s by default) and then gets `server.shutdown_timeout`, derived from
a 120 s grace period ([architecture.md](architecture.md) R-ARCH-024,
[ha-and-helm.md](ha-and-helm.md) R-HA-011). A fill spools to the pod's own scratch space, and no
volume is shared between pods (INV-13), so no other replica can continue a partial transfer. The
owner requires a drain without outage when the setup is sized for it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Finish, or cancel at the deadline | Running fetches continue detached and are cancelled 5 s before the shutdown deadline, released with `failed`; a draining replica starts and holds new flights like any other | Nothing to build beyond R-ARCH-024 | A flight first requested on the draining replica during its drain delay makes waiters on healthy replicas depend on a pod that is leaving; if it is cut, they start over |
| (b) (a), and a draining replica holds no new cross-replica flight | Running fetches as in (a). From `SIGTERM` on, a request on the draining replica joins a flight another replica holds, as a waiter, but never acquires the lock; with no flight held elsewhere, it fetches under its in-process single-flight only | Waiters on healthy replicas never start to depend on a pod that is leaving; one flag in the proxy, one bypass reason in the metric | During the drain delay, a path first requested on the draining replica and on another replica is fetched twice |
| (c) Cancel every fetch at `SIGTERM` | Release `failed` at once; waiters take over | The fastest hand-over | Throws away fetches that would have finished within the grace period, which for npm and NuGet is nearly every fetch; the draining replica's own clients are cut early |
| (d) Hand partial transfers over | The holder writes its partial body to S3 as a multipart upload, and a waiter continues with an upstream `Range` request | No transfer starts over | Needs `Range` support and validators that pin the bytes at every upstream; unverified partial bodies sit in S3; a large mechanism for MVP artifact sizes |
| (e) Fetch on the `worker` role | Misses become jobs; `web` replicas wait for them | `web` drains never touch a fetch | Adds queue latency to every miss; `worker` replicas drain too, with their own grace period, so the problem moves rather than goes |

**Choice:** (b) — a draining replica keeps finishing the fetches it holds until 5 s before its
deadline, then cancels them with `failed`, and from `SIGTERM` on it holds no new cross-replica
flight.

**Rationale:**
- npm tarballs and NuGet packages finish well within the grace period, so cancelling early (c) only
  loses work that was about to complete.
- A cut transfer cannot continue on another replica without shared storage, and ReadWriteMany is
  excluded (INV-13). (d) would rebuild that over S3 for artifacts that do not need it. (e) moves
  the drain problem to another role and slows every miss.
- (b) costs one flag. It confines duplicate fetches to the drain delay, and it removes the case in
  which waiters on healthy replicas lose progress to a drain they had no part in. A flight that
  started before `SIGTERM` can still be cut, and its waiters then take over at once instead of after
  the lock's TTL.
- Client facts that bound the damage: npm retries twice by default and allows 5 minutes per
  request; NuGet makes up to six tries and waits 100 s for response headers
  ([npm config](https://docs.npmjs.com/cli/v11/using-npm/config),
  [NuGet `HttpSourceRequest`](https://source.dot.net/NuGet.Protocol/HttpSource/HttpSourceRequest.cs.html)).
  A waiter whose holder was cut acquires the flight within one poll interval and fetches again, well
  inside both.
- Revisit when container-image layers or large generic files (P1) show flights cut at shutdown in
  `trove_http_transfers_cut_total` and the `draining` bypass count. (d) for those formats, or a
  dedicated Deployment with a longer grace period ([architecture.md](architecture.md) Q-ARCH-18),
  is then the next step.

**Applied in the spec:** R-REPO-026: the "Shutdown and drain" bullet, the bypass reason `draining`,
and acceptance criteria for a graceful delete that outlasts the deadline and for a request during
the drain delay. R-REPO-027: the background refresh is cancelled at shutdown and stores nothing.
R-REPO-049: the drain column of its table and a drain acceptance criterion. R-REPO-047: the bypass
reasons. Cross-page: [architecture.md](architecture.md) R-ARCH-033 (the reasons `draining` and
`wait_timeout`; `Acquire` is not called while draining) and R-ARCH-024 step 1;
[ha-and-helm.md](ha-and-helm.md) R-HA-011.

### Q-REPO-8 · Upstream fetch limits when the replica count changes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`limits.maxConcurrentFetches` counted per replica (32 by default), so the upstream saw the
configured value times the number of `web` replicas. Under the new constraints that number grows
with the load and changes with autoscaling, while the tolerance of an upstream such as a public
registry does not.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica | The earlier rule | No coordination; upstream capacity grows with trove's | The limit an operator sets is multiplied by a replica count that autoscaling changes; 20 replicas send 640 concurrent fetches to one upstream at the old default |
| (b) A cluster budget shared out per replica | Each replica allows `ceil(budget ÷ n)` fetches, with n the live client-serving replicas in the replica registry | One meaning whatever the replica count; no shared state, no Valkey round trip, no slot that a killed replica leaks; reuses the count that the rate-limit fallback already keeps (R-ARCH-034) | Rounding up admits up to n − 1 extra fetches, and a budget below n admits one per replica; after a scale event the shares lag by one heartbeat; one busy replica may queue while another has free slots |
| (c) A cluster-wide semaphore in Valkey | Slots held as Valkey keys with a TTL; (b) as the fallback | Exact bound while Valkey is up | Needs a slot set without scripts, on the command set R-ARCH-031 grants (`GET`, `SET`, `DELIFEQ`, `INCR`, `EXPIRE`, `MULTI`, `EXEC`): one round trip per slot tried, slots of killed holders blocked until their TTL, and one more Valkey dependency on every fetch, to protect a bound that (b) already keeps approximately |
| (d) A cluster-wide rate limit | The R-ARCH-034 limiter on fetch starts per second | Reuses the limiter | Limits starts, not connections: a slow upstream piles up open fetches anyway; the per-replica slots would still be needed |

**Choice:** (b) — `limits.maxConcurrentFetches` is a budget for the installation, shared out as
`ceil(budget ÷ n)` per replica from the replica registry; the default becomes 64 for the cluster.
`proxy.max_concurrent_fetches` stays per replica, because it protects the replica.

**Rationale:**
- A limit that changes with the replica count is not a limit an operator can set; this is the
  argument of [architecture.md](architecture.md) Q-ARCH-17 for rate limits, and it holds here for
  the same reason.
- (b) keeps the bound within `budget + n − 1` with nothing shared. The overshoot is small against
  what (a) allowed, and it holds while Valkey is unavailable.
- (c)'s exactness is not worth a Valkey call and a leak case on every fetch for a bound whose
  purpose is to stop bursts, not to meter. (d) does not bound open connections.
- Random distribution by the Service evens out the per-replica shares; an uneven share costs a
  queued fetch of up to `proxy.queue_timeout_seconds`, and collapsed requests take no slot.
- The default of 64 for the cluster is a proposal, not a measurement. It is below the earlier
  per-replica default times three replicas (96), but it does not grow to 640 at 20 replicas.
- Revisit if upstreams answer 429 to trove under the reference load (the budget is then too high,
  or (d) is added), if cold-cache builds queue on fetch slots (the default is then too low), or if
  budgets below the replica count become common ((c) is then the exact form).

**Applied in the spec:** R-REPO-040: fetch slots with the per-replica share, the bound, the queue
and a new acceptance criterion with two and three replicas. Only the request that fetches takes a
slot, after it holds the flight (R-REPO-025 step 10, R-REPO-026 step 3), so a replica's small share
is not spent on waiting for another replica's transfer; a second acceptance criterion checks that.
R-REPO-024: the settings table row
(default 64, range 1–4096). R-REPO-049: the fetch-slot row. Q-REPO-2: its rationale.
Cross-page: [operations-and-observability.md](operations-and-observability.md) R-OPS-003 (the
replica registry count is read by the proxy as well).

### Q-REPO-9 · Source address for upstream connections with interchangeable replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

RMT-015 lets a remote repository pick the local address of its upstream connections, for a host
with several interfaces and a firewall that admits one of them. R-REPO-024 stored that as one
address, `sourceAddress`, in the repository configuration, which every replica shares. Every pod
gets its own IP address ([Kubernetes network model](https://kubernetes.io/docs/concepts/services-networking/)),
and every role runs at least two replicas (INV-12).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One address (earlier) | `sourceAddress: 10.99.0.11` | Simple; the catalog's form | Fits one pod at most. Every other replica fails every fetch of the repository, and because the circuit is shared, its failures open the circuit for all replicas |
| (b) A network prefix | `sourceNetwork: 10.99.0.0/24`; each replica binds the first of its own addresses inside the prefix; a replica without one answers 502 `source_network_unavailable`, not counted toward the circuit | One configuration for all replicas (INV-2); keeps the catalog case of multi-homed pods, for example a secondary network to a partner's VPN; fails closed per replica | A dialer that enumerates interfaces; an error code and a metric |
| (c) Drop the setting | The platform decides the source: routes, an egress gateway, NAT | Nothing to build | Loses RMT-015's case where one destination is reachable through several interfaces and the kernel's choice is the wrong one |
| (d) A per-replica address in file configuration | Each pod's configuration names its own address | Exact addresses | Replicas of a role differ in configuration, against INV-2, and a rescheduled pod gets another address, so the mapping breaks on the next drain |

**Choice:** (b) — `remote.sourceNetwork` takes a CIDR prefix; each replica takes its own address in
it, and a replica without one fails that repository's fetches closed, without opening the shared
circuit.

**Rationale:**
- (a) cannot work with two replicas, and it turns one replica's local problem into an outage of
  the repository on every replica through the shared circuit.
- (d) breaks the rule that replicas of a role are identical and interchangeable, and node drains
  reschedule pods to new addresses.
- (c) is honest, but secondary networks exist exactly where this setting matters, and the dialer
  already has a hook per connection for the egress rules (R-REPO-038).
- (b) states the intent ("leave through this network") in a form that every replica can satisfy. A
  replica that cannot does not fall back to the kernel's choice, which could reach the upstream
  through an interface its firewall should never see.
- Revisit if no installation uses the setting by the time the P1 formats ship; (c) would then
  remove it.

**Applied in the spec:** R-REPO-024: the member is `sourceNetwork`, its bullet, the upstream-test
field `sourceAddress` and two acceptance criteria. R-REPO-008: `invalid_source_network`, and the
warning `source_network_single_address`. R-REPO-047: the metric
`trove_remote_source_network_unavailable_total`. Deliberate deviations: new row. Cross-page:
[operations-and-observability.md](operations-and-observability.md) metrics catalog (the new
counter). No other spec page names `sourceAddress` (checked on 2026-10-28).

### Q-REPO-10 · Zoned IPv6 and the NAT64 prefix in the egress rules

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

An audit found two ways past the CIDR rules: an IPv6 address with a zone is in no prefix, so no deny rule sees it, and an
address in the NAT64 prefix can stand for an internal IPv4 address that the deny list protects.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse a zoned IPv6 address before the CIDR rules, and check the IPv4 address of the well-known NAT64 prefix as well | two fixed checks; the deny list grows by four ranges | closes both paths with no configuration; the allow list cannot open a zoned address | a network-specific NAT64 prefix stays unchecked until a setting exists |
| (b) Strip the zone and check the address | the zone is dropped and the rules run on the bare address | keeps the rules as they are | a zone can name a different interface; the bare address may be allowed where the scoped one is not |
| (c) Make the NAT64 prefix a setting, with no default | the operator names the prefix | exact for every network | a cluster without the setting is unprotected by default |

**Choice:** (a). The deny list adds `::/96`, `100.100.100.200/32` and `168.63.129.16/32`, the deprecated IPv4-compatible range and
two metadata addresses of other clouds. A configurable prefix (c) is the next step if a network uses another one.

**Rationale:** A zoned address is a local-host address and no remote repository needs one, so refusing it costs nothing. The NAT64
check follows the translation of the well-known prefix, which is the one the cluster DNS64 setups use by default. The development
agent would revisit this when a network uses a different NAT64 prefix in a supported setup.

**Applied in the spec:** R-REPO-038 (the zoned and NAT64 rules, the deny list); the default of
`proxy.outbound.address_policy.deny_cidrs` in [config/outbound.go](../../internal/config/outbound.go) and in
[security/egress.md](../security/egress.md).

### Q-REPO-11 · Repository columns added in M1

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-001 adds six columns and a constraint to `repositories` in one `ALTER TABLE`. Two of them
belong to requirements outside M1: `claims_version` (the claims ETag, R-REPO-023, M2) and `project_id`
(R-AUTHZ-026, P1, a foreign key to a table that does not exist yet).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) All six columns in M1 | One migration as sketched | Matches the sketch | `project_id` cannot be created without the projects table; `claims_version` would be an ETag column no code writes |
| (b) The M1 columns now, the others with their requirements (chosen) | `lifecycle`, `config_version`, `created_by`, `updated_by`, `last_activity_at` and `repositories_key_dots` in `00010_repositories.sql` | No unused column; each later column is an expand step (`ADD COLUMN` with a default) of the work that needs it | The M2 and P1 work each add a migration |

**Choice:** (b). `created_by` and `updated_by` stay plain `uuid` columns as in the sketch, so a
repository outlives its creator. `repositories.config_version` is registered with
`meta.RegisterRevisionColumn`; `claims_version` registers with the migration that adds it.

**Applied in the spec:** R-REPO-001 data model (a note under the `ALTER TABLE`).

### Q-REPO-12 · The error code of a 422 with one or several violations

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-002 and R-REPO-009 say "returns 422 `invalid_key`"; R-REPO-008 says every violation comes in one
response; R-API-009 shows a 422 whose `code` is `validation_failed` and whose `errors[]` carry the details.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Always `validation_failed`, the specific code only in `errors[]` | One top-level code | Matches the example of R-API-009 | The acceptance criteria that say "422 `invalid_key`" would need a lookup into `errors[]` |
| (b) A single violation under its own code, several under `validation_failed` (chosen) | `errors[]` always lists every violation with its code; the top-level `code` is the violation's own when there is exactly one | Both readings hold: a client matching on `invalid_key` or `immutable_field` sees it at the top, a client iterating `errors[]` sees every violation | The top-level code depends on how many violations there are |

**Choice:** (b). Superseded by [Q-REPO-84](#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request): the top-level code is `validation_failed` whatever the number of violations.

**Rationale:** The acceptance criteria of this page read as top-level codes, and a client that created
one key wrongly wants exactly that. The development agent would revisit this if the API area settles on
one rule for every resource; the repository package then follows it in `validation.err`.

**Applied in the spec:** none (the criteria already read this way); `internal/repository/validate.go`.

### Q-REPO-13 · Management reads come from PostgreSQL, the list filters in the domain

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read and list from the replica's snapshot | One source for requests and management | No query per management read | A management read could be stale for the poll interval, against R-API-052 |
| (b) Read and list from PostgreSQL; filter and page over one read of the table (chosen) | `Service.Get` and `Service.List` query the table; the snapshot serves request paths only | Meets R-API-052 and Q-API-12; the number of repositories of an installation is small, so no dynamic query is needed | A list reads the whole table on every call |
| (c) A dynamic SQL list query (`listq`, R-ARCH-019) | Filter and keyset in SQL | Scales to very many repositories | Machinery for a table that holds dozens to hundreds of rows |

**Choice:** (b). `List` returns items sorted by key with a keyset `After` and a `Limit`, plus `HasMore`; the cursor
format is the API layer's (R-API-010).

**Rationale:** (c) would be the answer if installations reached tens of thousands of repositories. The
development agent would revisit this then.

**Applied in the spec:** none.

### Q-REPO-14 · One snapshot entry, flushed by every `repo_config` change

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One cache entry for the whole table, flushed on any change (chosen) | The change payload names the repository id (R-REPO-008), but the cache ignores it and the next read rebuilds | Trivially correct, including a missed or forged hint; one query rebuilds it | A change anywhere costs one rebuild on every replica |
| (b) One cache entry per key, evicted by the id in the payload | Finer eviction | Fewer rebuilds | The per-key set must also handle list calls and unknown keys; a payload that does not match a key needs a fallback flush |

**Choice:** (a). The payload is `{"id", "key", "op"}` so that a later consumer can evict by id.

**Rationale:** Repository changes are administrative and rare (R-ARCH-018 keeps change areas for such writes). The
development agent would revisit this if an installation changed repositories at a rate where rebuilds show in
the metrics.

**Applied in the spec:** none.

### Q-REPO-15 · A repository deletion records two events and joins a running job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One `repository.deleted` event when the job removes the row | Past tense matches the fact | One event | The decision (who, when) and the state change of the first transaction would have no event; R-REPO-008 requires an event for every change |
| (b) `repository.deleted` with `phase: started` in the first transaction and `phase: completed` in the last (chosen) | The first names the caller who asked; the second carries the same actor from the job arguments | Every committed change has its event in its own transaction | Two events per deletion |

**Choice:** (b). A repeated delete while the job runs records nothing and answers with the same job (the job kind is
exclusive per repository id). `detach` is accepted and has nothing to detach until virtual membership exists
(R-REPO-017); a virtual repository, which has no content, is removed in the first transaction with the `completed` phase.

**Rationale:** The development agent would revisit this if the audit area wants distinct event names for the two phases.

**Applied in the spec:** R-REPO-013 reads unchanged; `internal/audit/events_repository.go` declares the `phase` property.

### Q-REPO-16 · The shape of the stored document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-004 lists the members; it leaves open how an omitted setting, a `null`, an unknown member and the
members owned by other pages are stored.

| Question | Choice |
|---|---|
| What does `repositories.config` hold? | The settings: every member but `key`, `class`, `format` and the derived `layout`, with defaults filled in, so that what is stored and what is read back are one form |
| Unknown or duplicate member | 400 `invalid_document` at every level, also on a merge-patch result |
| `hideExistence` | a string: `"inherit"`, `"true"` or `"false"` (the contract's `Repository.HideExistence`) |
| `null` in a merge patch | removes the member, which reads back as its default |
| Members that other pages own (`storage`, `remote`, `virtual`) | carried as JSON objects without looking inside, until the page that owns them is implemented (`storage` for M1: a JSON object; `remote` and `virtual` cannot occur, because the generic format supports only the local class) |
| An update that changes no stored member | writes nothing: same `config_version`, no event, no change row |
| `PUT` with an omitted `key`, `class` or `format` | unchanged (a different value is `immutable_field`) |
| Members that cannot apply to a format | `not_applicable` in the same pass, decided by the format descriptor |

**Rationale:** A misspelt member must never be dropped silently (R-REPO-008). A no-op that moves the ETag would
make a client that re-sends an unchanged document race with itself. The development agent would revisit the
treatment of `storage` when R-STOR-003's settings are implemented.

**Applied in the spec:** none.

### Q-REPO-17 · The pattern matcher lives in `internal/repository`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-005 requires the matcher of R-AUTHZ-005. Layers (R-ARCH-002) let `authz` import `repository` but not the
reverse.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The matcher in `internal/repository`, exported as `CompilePattern`; authz calls it (chosen) | One implementation for both pages | "A pattern copied between them selects the same names" holds by construction | The authorization area depends on a function that lives in another package |
| (b) A leaf package `internal/pattern` | A neutral home | No dependency on the repository package | One more package; R-ARCH-003 would need a line |
| (c) Two copies | Each page owns its matcher | No coupling | The two drift apart |

**Choice:** (a), with a test table (path and name subjects) that carries the cases of R-AUTHZ-005.

**Rationale:** The development agent would move the matcher to a leaf package (b) if a third consumer appears, such as
retention (R-STOR-043).

**Applied in the spec:** none.

### Q-REPO-18 · The wait for an unknown key is `repositories.lookup_wait`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-011 step 2 bounds the wait of a lookup "like a wait for the database (R-HA-009)" and names no key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key of its own, default 5 s (chosen) | `repositories.lookup_wait` | Visible and tunable; a poll that cannot finish answers 503 with `Retry-After: 1` | One more key |
| (b) Reuse the database statement timeout | No new key | Nothing to document | The timeout bounds a statement, not a poll that waits on the control connection |

**Choice:** (a).

**Rationale:** 5 s is two orders above the poll's normal duration and below the client timeouts of npm and dotnet.
The development agent would revisit it if R-HA-009 gets a named bound.

**Applied in the spec:** R-ARCH-011 reads unchanged; the key is in [docs/developer/repositories.md](../developer/repositories.md).

### Q-REPO-19 · A holder of repositories:read-config sees every repository in the list

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 says the list contains only repositories on which the caller holds `read`, and `view=full` is the catalog of all configurations.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only readable repositories, for everyone | The list is filtered by `read` whoever asks | One rule | The repository administrator holds no content rights (R-AUTHZ-012), so the administration screen would list nothing; `view=full` would show a subset of the configurations |
| (b) Every repository for a holder of `repositories:read-config`, the readable ones for everyone else (chosen) | The capability reads configuration instance-wide (R-AUTHZ-013 "no per-repository configuration type") | The role that administers repositories sees them; repositories being deleted are visible to it, as R-REPO-013 says | A second rule in the list |

**Choice:** (b).

**Rationale:** Seeing that a repository exists and what it is configured as is what the capability grants; it grants no access to content. The development agent would revisit this if a project administrator, who holds the capability for the repositories of one project only (R-AUTHZ-026), needs the list narrowed to them.

**Applied in the spec:** R-REPO-012 (permission row).

### Q-REPO-20 · The consumer view needs `read`, and the ETag names the view

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-010 gives the consumer view to a caller who holds "any repository action", and R-API-012 says the ETag carries the view where the representation depends on it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any of the six actions | The handler decides each action until one is allowed | As written | Up to six decisions, five of them refusals that are audited as `authz.denied`; an action other than `read` does not let the caller see the repository elsewhere (R-AUTHZ-016 answers 404 without `read`) |
| (b) `read` (chosen) | One read-type decision, the row of R-AUTHZ-016 | One decision, one audit rule; a caller who may read gets the view and a caller who may not gets the 404 of every other read | A caller with only `deploy` gets 404 on the configuration read, as on `/files/` reads |

**Choice:** (b). The tag of the full document is `"<config_version>"`, the tag of the consumer view `"<config_version>-consumer"`; the answer of a write carries the first.

**Rationale:** The tag of a view that the caller cannot write through never has to be accepted as an `If-Match`. A caller holding only a write action on a repository has no use for the consumer view; the development agent would revisit this if one appears.

**Applied in the spec:** R-REPO-010 (permission row).

### Q-REPO-21 · A misspelt or malformed member of a repository document answers 400 invalid_document with its pointer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-008 says 400 `invalid_document` for malformed JSON and unknown members; R-API-003 says 400 `malformed_request` with the pointer of the member in `errors[0]` for every body.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `malformed_request` for every body | The conventions of the API own the error | One code for every resource | R-REPO-008 and the acceptance criteria of R-GENERIC-001 name `invalid_document` |
| (b) `invalid_document` with the pointer (chosen) | The repository document keeps its code and gains the pointer of the convention | Both pages are right: the status is 400, the code is the one the repository page documents, and `errors[0].pointer` names the member | The contract tests of the conventions need the code as a parameter (`apitest.Resource.MalformedCode`) |

**Choice:** (b). Superseded by [Q-REPO-84](#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request): the body error is 400 `malformed_request` with the pointer, and the repository code is not a top-level code any more.

**Rationale:** The code is what a client of the configuration API matches on. The development agent would revisit this if the conventions page makes the code of a body error uniform.

**Applied in the spec:** R-REPO-008 (the first paragraph reads unchanged; the pointer is the addition).

### Q-REPO-22 · The list filters: repeated or comma-separated values, and a project that selects nothing

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 takes `class` and `format` as comma-separated lists, R-API-011 as repeated parameters, and the `project` filter needs projects, which M1 does not have.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One form | Comma-separated only, or repeated only | A single rule | Contradicts one of the two pages |
| (b) Both forms; `project` selects nothing (chosen) | Values are split at commas and repeats are joined; a project key selects the repositories of the project, and there are none | The documented examples work; the parameter exists from the first release, so a client does not change when projects arrive | A filter that always answers an empty list |

**Choice:** (b).

**Rationale:** Accepting the parameter keeps the contract of the list stable; an unknown project key is not an error in a list (R-API-011). The development agent would revisit this when the projects table exists.

**Applied in the spec:** The operation `listRepositories` in api/openapi.yaml describes it.

### Q-REPO-23 · last_activity_at is flushed once a minute by a constant interval

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-012 asks for a write at most once per minute per repository and replica and a flush at shutdown, and names no configuration key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key `repositories.activity_flush_interval` | The interval is tunable | Tests and operators can change it | A second interval whose lower bound is the requirement; one more key to document |
| (b) A constant of one minute (chosen) | `repository.DefaultActivityInterval`; the tests construct the buffer with a shorter interval | The requirement is the interval; nothing to misconfigure | No operator control |

**Choice:** (b).

**Rationale:** "At most once per minute" is a ceiling the requirement fixes, so a tunable would only offer values that break it. The development agent would revisit this if an installation needs a lower write rate.

**Applied in the spec:** R-REPO-012 reads unchanged; [docs/developer/repositories.md](../developer/repositories.md) names the constant.

### Q-REPO-84 · A violation answers 422 validation_failed, a body error 400 malformed_request

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-API-009 fixes the top-level code of a 422 as `validation_failed` and of an unreadable or unknown-member body as
`malformed_request`; the repository page had its own top-level codes for a single violation (Q-REPO-12), for a misspelt member
(Q-REPO-21) and for a taken key (`repository_exists`), while the identity and policy areas used `conflict` and the code of the first
problem. Codes are a stable contract (R-ARCH-025), so one rule must hold for every resource.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The page keeps its codes | Q-REPO-12 and Q-REPO-21 stay | The criteria read as before | A client needs one rule per resource; the contract tests carry overrides (`apitest.Resource.MalformedCode`, `ImmutableCode`) |
| (b) R-API-009 everywhere (chosen) | 409 `already_exists`; 422 `validation_failed` with every violation, each with its own code, in `errors[]`; 400 `malformed_request` with the pointer of the member; `reserved_name` stays the top-level code of a reserved key, because R-API-004 defines it so | One rule; the overrides are gone | The criteria of this page read "422 `validation_failed` with `invalid_key` in `errors[]`" |

**Choice:** (b). The codes `repository_exists` and `immutable_field` are removed from the registry; a change to an immutable member
is the field code `immutable` of R-API-013. `invalid_document` stays as the field code of a document that the domain cannot read
at all (no pointer is known).

**Rationale:** a client that matches the top-level code of every 422 on one value, and the field codes in `errors[]`, needs no
resource-specific knowledge. The development agent would revisit this if clients need the specific code without reading `errors[]`.

**Applied in the spec:** the acceptance criteria and the code table of R-REPO-002, R-REPO-003, R-REPO-008, R-REPO-009, R-REPO-010
and R-REPO-011 of this page; Q-REPO-12 and Q-REPO-21 are marked superseded; R-API-009 ([Q-API-136](api-and-ui.md#q-api-136--one-rule-for-the-top-level-code-of-an-error)).

### Q-REPO-85 · The integer keys of `proxy.*` keep the unit in their name

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the configuration framework asks for `time.Duration` and `ByteSize` instead of an integer with a unit in its name ([../developer/configuration.md](../developer/configuration.md#adding-a-key)). This page names `proxy.lock_poll_interval_ms`, `proxy.fetch_wait_timeout_seconds` and `proxy.queue_timeout_seconds` as integers, and its acceptance criteria, the failure-mode analysis (H-49) and [architecture.md](architecture.md) R-ARCH-033 quote the names and the values (`proxy.fetch_wait_timeout_seconds: 5`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the names and integer values | `lock_poll_interval_ms: 1000`, `fetch_wait_timeout_seconds: 120`, `queue_timeout_seconds: 10`; the code reads them through `LockPollInterval()`, `FetchWaitTimeout()` and `QueueTimeout()` | Every quoted name, value and criterion stays true; the generated environment names do not change | Three keys break the framework's convention; a value is not self-describing without its name |
| (b) Rename to durations | `lock_poll_interval: 1s`, `fetch_wait_timeout: 2m`, `queue_timeout: 10s` | Follows the convention | Rewrites this page, R-ARCH-033, H-49 and every criterion that quotes a key; the work packages that were briefed with the old names diverge |

**Choice:** (a), with the ranges `lock_poll_interval_ms` 50 to 60000, `fetch_wait_timeout_seconds` 1 to 3600, `queue_timeout_seconds` 0 to 600, and the cross-field rule that the poll interval is below the wait timeout. The byte sizes (`max_artifact_bytes`, `max_metadata_bytes`, `merged_metadata_cache_bytes`) are `ByteSize` and accept a plain integer, so their quoted values stay valid. Keys of the npm section that the spec writes without a unit in the name (`npm.document_max_age`, `npm.publish.retry_window`) are durations: `document_max_age: 0s`.

**Rationale:** The names are a contract that criteria and a failure analysis quote, and a rename buys consistency at the price of rewriting them. The development agent would revisit this when the three keys are touched for another reason; the change is then a rename with a deprecation alias (the loader rejects unknown keys, so an alias needs a code change).

**Applied in the spec:** [formats/npm.md](formats/npm.md) R-NPM-013 (`document_max_age: 0s`); [../developer/configuration.md](../developer/configuration.md) names the exception.

### Q-REPO-86 · `remote_cache_entries` has a surrogate `id` so the blob release trigger applies unchanged

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an entry whose blob is replaced (a refreshed metadata document) must enqueue the replaced blob for collection ([storage.md](storage.md) R-STOR-003). `blob_ref_release_update()` joins the old and the new rows of an `UPDATE` on the column `id`; the primary key of the entry is `(repo_id, path, variant)`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A surrogate `id bigint GENERATED ALWAYS AS IDENTITY UNIQUE` | The existing function applies; `TestBlobReferenceRules` recognises the table unchanged | The smallest change; no second copy of the release logic | One more column and its unique index (8 bytes per entry) |
| (b) A second release function | Joins on `(repo_id, path, variant)` | No extra column | Two functions to keep in step; the migration test has to learn a second shape |
| (c) A generic function | Join columns passed in `TG_ARGV` | One function for every shape | Rewrites a function that `items` and every later table depend on |

**Choice:** (a). The primary key stays `(repo_id, path, variant)`; nothing references `id`.

**Rationale:** The rule "every table with a blob reference installs both triggers" is checked by catalog, and (a) keeps one implementation of it. The development agent would revisit this if the entry table grows so large that the second index matters; then (c) with `TG_ARGV` replaces it.

**Applied in the spec:** the data model of R-REPO-025 (the column, the blob and item indexes, the trigger note).

### Q-REPO-87 · Invalidation rows record their fold in `folded_at`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 keeps a row of scope `negative` or `all` for 86,405 s, and deletes it only "once it is folded and that period has passed". Without a record of the fold the job cannot tell a folded `all` row waiting for its period from one whose fold is still running, and readers must keep applying the unfolded ones.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A column `folded_at timestamptz` | NULL until the last batch of the fold has run; a partial index serves the job's scan of unfolded rows | One row carries its whole life; the job is restartable by the column alone | One more column |
| (b) A table of fold progress | One row per invalidation with a cursor | Records progress inside a fold | A second table and a join for a flag |
| (c) Decide by age alone | A row older than the period counts as folded | No schema | A late fold (a stopped worker) would drop the row early and let unfolded metadata entries count as fresh again, which the page forbids |

**Choice:** (a). The job sets `folded_at` when the fold of the row is complete; scope `metadata` rows are deleted at once, `negative` and `all` rows when `folded_at` is set and 86,405 s have passed since `created_at`.

**Rationale:** The page's own rule, that a replica never drops a row the database still holds before its fold, needs the flag. The development agent would revisit this if folds become resumable inside one row (then a cursor column joins it).

**Applied in the spec:** the data model and the fold bullet of R-REPO-045.

### Q-REPO-88 · A secret field reads {"set": true} and keeps its value in repository_secrets

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-033 stores a secret field's sealed value or reference in `repository_secrets` and says reads show `{"set": true}` for sealed values and the reference itself for references, so that a client re-submitting what it read neither blanks nor leaks the secret. The merge patch of R-REPO-011 is applied to the document that was read, so what the document holds for a secret decides what a patch can express. A new value reads exactly like the old one.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The marker and the reference stay in `repositories.config`; the rows hold the values | `password` is `{"set": true}` or `{"secretRef": …}` in the stored section; a request `{"value": …}` is sealed into the row and the marker written; `{"set": true}` keeps the row; absent or `null` removes it | One source for every read, the snapshot and the diff; a re-submitted document is byte-equal and writes nothing; a merge patch needs no special case | The reference exists in two places (the document and the row), written in one transaction; a secret change must move `config_version` by itself, because the marker does not change |
| (b) The document carries nothing; reads join the rows | The stored section has no `password`; every read adds it from `repository_secrets` | One place per reference | Every read, the snapshot and the diff need a second query; a re-submitted document without `password` would delete the secret, the leak-proof round trip the page asks for is lost |
| (c) A revision number of the secret in the document | `password: {"set": true, "revision": 3}` | The change is visible in the document | A member the spec does not name; a re-submitted document must carry the number or reset it |

**Choice:** (a). A write that carries a new value or a reference counts as a change even when the stored section reads the same, adds `remote` to the recorded changes and moves `config_version` (so the `ETag` changes and the clients are rebuilt). A `{"set": true}` next to a `value` or `secretRef`, which is what a merge patch of a read document produces, is the marker overridden by the new form; `value` and `secretRef` together are 422 `invalid_secret`.

**Rationale:** The page asks that a document which was read can be sent back, and that works only if the read form is a complete, stable form of the stored one. The development agent would revisit this when a second secret field is built (the sensitive header values of R-REPO-036, the client-certificate key): the same shape then applies per field pointer, and a revision member becomes worth it only if clients need to see a rotation in the document.

**Applied in the spec:** R-REPO-024 (the canonical section), R-REPO-033 (the bullet on re-submitted documents and the version).

### Q-REPO-89 · Upstream clients are cached per repository and version; secret files are read at use

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a remote repository's HTTP client owns a connection pool and is built from the settings, the unsealed password and the hop policy. It is per-replica state (R-ARCH-032, R-REPO-049) and needs a row in both tables. R-REPO-033 says a changed `secretRef` file takes effect within 60 s. The plan keyed the client by `(repository id, config_version, secret-file fingerprint)`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Key by repository id and `config_version`; the credential reads references at use | The client holds a secret object that reads the file or the variable again when its value is 10 s old (1 s after a failure) and fails the request with 502 `upstream_secret_unavailable` when it cannot | A rotated file changes nothing in the pool; no fingerprint to compute per request; the key is the one the ETag already is | Each replica opens the file at most every 10 s per repository |
| (b) Add a file fingerprint to the key | Every lookup stats the referenced files; a changed file builds a new client | The client is immutable | A stat per lookup; a rotation throws the connection pool away; a variable has no fingerprint |
| (c) Build a client per request | No cache | No state | A TLS handshake per request |

**Choice:** (a). The cache `proxy_upstream_clients` is registered in the area `repo_config`, bounded to 2,048 clients, with `cache.snapshot_safety_ttl` as its `SafetyTTL`; a change of the repository evicts its entries of older versions (every entry on a deletion; amended by [Q-NUGET-94](formats/nuget.md#q-nuget-94--a-forwarded-answer-fetched-under-the-remotes-current-configuration-outlives-both-applications-of-a-change), 2026-11-02, as the second application of a change on a replica otherwise dropped a current client), and the idle connections of the client a newer version replaced are closed. A refusal to resolve a secret is remembered for 5 s, so a flood of requests for a repository with a missing file costs one resolution per interval.

**Rationale:** The version key makes a stale client unreachable even if an eviction is missed, and the use-time read gives R-REPO-033's bound without touching the pool. The development agent would revisit this if reading a mounted file every 10 s shows up in a profile; the interval is a constant (`upstream.RefreshInterval`).

**Applied in the spec:** R-REPO-033 (the bound of 10 s), R-REPO-049 (the row), [architecture.md](architecture.md) R-ARCH-032 (the cache).

### Q-REPO-90 · The upstream test is a probe of the format's resource that answers 200 with a result

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-024 defines two operations and the shape of a result, and leaves open what the probe requests, what counts as "reachable" and "authenticated", how an unreachable upstream is answered, and what the test does for a repository that must not contact its upstream.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One GET of the format's probe resource through the repository's own client; every outcome is a 200 with a result | `reachable` is true for any HTTP response, `authenticated` is false for 401 and 403, `error` carries a stable code for a failure; offline repositories are not contacted | The test exercises exactly what a fetch does (credentials, egress rules, source network, redirects); an administrator reads one shape | A 500 from the upstream is "reachable" |
| (b) A transport failure is a 502 | The test answers like a fetch | Familiar status | The UI cannot tell "the test failed" from "the upstream is down"; the DNS case of the acceptance criterion is a result, not an error |
| (c) The test ignores offline | It always sends the request | Works while preparing to go online | The page says offline MUST stop all upstream traffic of the repository |

**Choice:** (a). The probe resource is a facts entry of the format catalog (`ProbePath`: npm `-/ping`; empty means the URL itself), requested with GET under the path of the upstream URL; at most 64 KiB of the body are read. A test of a repository with `availability.mode: offline`, or while `proxy.global_offline` is on, reports `offline` and sends nothing. The operation needs `repositories:write-config`, answers 404 for a missing key and 400 `not_applicable` for a repository that is not remote, and reads and writes no circuit state.

**Rationale:** A probe that shares the fetch path cannot report a configuration that fetches do not use. The development agent would revisit the offline rule if administrators need to test before switching a repository on; a separate `force` member would then be the way, not a silent exception.

**Applied in the spec:** R-REPO-003 (the registration), R-REPO-024 (the result, its codes and the criteria).

### Q-REPO-91 · What an omitted remote member takes, and which members are refused until they are built

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-024 lists a default for every member but says formats "propose entries for well-known upstreams" for `extraHosts`, and a URL "required"; three members, the client certificate, the headers and the named proxy, belong to P1 requirements, and two more members (pass-through, unused cleanup) to P1 behavior.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Proposals only for the well-known upstream; a P1 member is refused with `not_applicable` until it is built | `extraHosts` takes the format's entries when `url` is omitted or equals the format's upstream; `clientCertificate`, `headers`, `outboundProxy.mode: named`, `cache.storeArtifacts: false`, `cache.unusedRetentionHours` above 0 are 422 `not_applicable`; `outboundProxy.mode: direct` works | No host is trusted for a private upstream that was never meant to reach it; no accepted setting is silently ignored | An operator who sets a P1 member gets a refusal now instead of a no-op |
| (b) Always propose the format's hosts | `extraHosts` defaults to the proposal whatever the URL | Simpler default | A private NuGet feed would allow `*.nuget.org` redirects it never asked for |
| (c) Accept P1 members and ignore them | Stored, not enforced | Documents are portable to a later version | A header or a certificate that is not sent looks configured |

**Choice:** (a). The same rule makes `url` required for a format without a default upstream, normalizes an empty path to `/`, and, for the cache periods of a format that has none (no metadata documents), takes 0. The update path calls a hook, `RemoteDeps.OnUpstreamChanged`, in its transaction when `url`, `extraHosts`, `authMode`, `username`, `password`, `clientCertificate` or `headers` changed, where R-REPO-011 asks for the invalidation of R-REPO-045; the hook is bound by the cache invalidation.

**Rationale:** Refusing is the only behavior that keeps the stored document and the enforced behavior equal. The development agent would revisit it when the P1 requirements are built; each refusal is one line in `validateRemote` and goes with its requirement.

**Applied in the spec:** R-REPO-024 (the `extraHosts` row and the bullets on `url` and the refusals).

### Q-REPO-92 · The role of an upstream request in the flight is how its request came to send it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-OPS-011 and R-REPO-047 name the roles `leader`, `follower` and `waiter` for the upstream log and leave the difference between the last two undefined. A follower or a waiter that is served from the flight's entry sends no upstream request, so a role that only described joining would never appear in a record.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The role says how the request that sends the request got to be the one that sends it (chosen) | `leader`: it fetches without having joined or waited first (it holds the lock, or no lock exists: Valkey unavailable, draining); `follower`: it joined a flight of this replica that ended with nothing to read (a pass-through, an unstored refresh) and then fetched itself; `waiter`: it waited for another replica's flight that ended without an entry (the holder failed, its lock expired, the wait bound ran out) and then fetched; `probe` for a shadowing check | Every role occurs on records that exist; the three can be told apart in a post-mortem of a fetch storm | A role describes history, not only the current act |
| (b) The role says whether the request joined a flight | follower and waiter for every request that joined, whether it fetched or not | Simple | Never logged: a joiner sends no request, so the field would only ever read `leader` |
| (c) One value `waiter-bypass` | A single role for every fetch without the lock | Matches the plan's sketch | Cannot tell the in-process case from the cross-replica one |

**Choice:** (a).

**Rationale:** The log is read for what happened to a request, and only requests that send one are in it. The development agent would revisit the definition when a fourth way to fetch exists.

**Applied in the spec:** R-OPS-011 and R-REPO-047 define the three roles and the probe role.

### Q-REPO-93 · validated_at is the database clock at the fill statement, fetched_at the database clock when the headers arrive

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-027 compares entries with the invalidations by database time, R-REPO-026 keeps the newer `fetched_at`, and the acceptance criteria need a document fetched for 90 s to be fresh when it arrives.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `validated_at` and `expires_at` from `clock_timestamp()` in the fill statement; `fetched_at` read from the database when the response headers arrive (chosen) | The period starts when the document is stored; two overlapping fetches of one document are ordered by the time their answers began | One clock decides everything the invalidations and the upsert compare; a slow transfer does not shorten the period | A fill that fetched before an invalidation and commits after it counts as validated after it; the fold's last pass catches the entries a fill stored late (Q-REPO-103) |
| (b) Both from the replica clock | `time.Now()` at the end of the transfer | No extra round trip | Skew between replicas orders two fetches wrongly and breaks the comparison with the invalidation times |
| (c) Both from the database clock before the request is sent | The period starts when the request is sent | An invalidation during the transfer always wins | A transfer longer than the period stores an expired entry |

**Choice:** (a). `cache.maxStaleSeconds` counts from the end of the period (RFC 5861 `stale-if-error`), on the database clock.

**Rationale:** The extra statement per fetch is small against a transfer. The development agent would revisit it if invalidations during long transfers matter more than the period of a slow fetch.

**Applied in the spec:** R-REPO-027 (the clock of freshness) and R-REPO-029 (the bound of stale serving).

### Q-REPO-94 · The leader's client gets the body while it is fetched only when the length is announced; the status line goes out with the first released byte

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-041 lets the leader stream the body to its own client with the last 64 KiB withheld; a failure after the status line can only abort the connection, while R-REPO-040 owes a client a 502 when an item crosses the size limit.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stream when the upstream announced a length; otherwise answer from the stored entry (chosen) | A body of announced length cannot cross the limit; the status line is held back until the first byte beyond the withheld tail leaves, so a body that ends inside it still gets a clean error; a failure after the status line aborts the connection (`http.ErrAbortHandler`) | The first byte reaches the client early for the large artifacts the rule is for; limit and integrity failures are 502 where they can be | A chunked body without a length is served after the commit |
| (b) Always stream | Also without a length | One path | A size limit crossed mid-transfer cuts the client instead of answering 502 |
| (c) Never stream | Serve after the commit | Simplest | A 200 MiB artifact sends nothing for minutes and meets idle timeouts of the edge |

**Choice:** (a). A request with `Range` or a `HEAD` is not streamed: the range is served from the stored item (R-REPO-025), the `HEAD` stores nothing. The stream never stops the fetch: a client that stalls for a minute or leaves is dropped and the fetch goes on (R-REPO-026).

**Rationale:** The development agent would revisit it when a format needs conditional first fetches.

**Applied in the spec:** R-REPO-041 (the withheld tail) and R-REPO-040 (the refusal for an unannounced length).

### Q-REPO-95 · With the object store unavailable an artifact is streamed and not stored, a document is served and not stored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-025 says so and does not say what the requests that waited for the flight get, or what a ranged request gets.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The leader's client gets the verified body; the requests that waited fetch for themselves; a ranged request is 503 `object_store_unavailable` (chosen) | The commit fails with `object_store_unavailable`; the verified tail is released; no entry, no item; the followers find nothing to read and fetch under the slots, as a pass-through does (R-REPO-044) | Installs keep working in an outage; verification is not skipped | A burst of requests for one uncached artifact costs one upstream fetch each while the store is down |
| (b) 503 for every uncached artifact | Refuse until the store is back | No duplicate fetches | The requirement says an uncached artifact is still installed |
| (c) Spool for the waiters | Hand the spool file to the followers | One fetch | A spool is the pod's scratch space and its lifetime is the request's (INV-13) |

**Choice:** (a). The `Cache-Status` header of a streamed response is written before the commit result is known and says `stored`.

**Rationale:** The development agent would revisit it if the outage case needs the fetch collapsed.

**Applied in the spec:** R-REPO-025 (the object-store bullet).

### Q-REPO-96 · Which stale serving is bounded by maxStaleSeconds

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-029 bounds stale serving by `cache.maxStaleSeconds`; R-REPO-030 serves stored entries regardless of expiry in offline mode, and R-AUTHZ-008 serves what the cache holds to a caller who may not fill it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The bound applies where the engine chose to ask and could not: a failed or slow refresh and an open circuit (chosen) | Offline mode, the cache view, only-if-cached and a caller without the populate right serve any stored entry | Each rule says what its spec page says; the bound limits only what an outage would otherwise keep alive | An offline repository can serve a very old document, by the operator's decision |
| (b) The bound applies everywhere | Every stale answer is bounded | One rule | Contradicts R-REPO-030 and the cache view |
| (c) The bound applies to nothing but failures | Not to the open circuit | Narrower | An open circuit is the long outage the bound is for |

**Choice:** (a).

**Rationale:** The development agent would revisit it when an operator asks for a ceiling on offline mode.

**Applied in the spec:** R-REPO-029 (the bullet on stale serving).

### Q-REPO-97 · Fetch slots: the share is read when a fetch asks, and every upstream request takes one

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-040 divides `limits.maxConcurrentFetches` by the number of live replicas and says a probe, a pass-through and a fetch outside a flight take slots directly.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ceil(budget / n)` with n read at every acquisition; a waiter takes none; probes and HEAD requests take one; the wait polls every 250 ms (chosen) | A replica's counters only; n comes from the replica registry's count | A scale event is seen at the next acquisition; no coordination; the bound `budget + n - 1` holds | A queued fetch may wait up to 250 ms longer than a notification would |
| (b) n fixed at start | Read once | Cheaper | Wrong after a scale event until restart |
| (c) A cluster-wide semaphore in Valkey | Exact total | Exact | Depends on Valkey, which R-REPO-040 forbids |

**Choice:** (a).

**Rationale:** The development agent would revisit it if the 250 ms poll shows up in the queue-wait histogram.

**Applied in the spec:** R-REPO-040 states the slots of probes and HEAD requests.

### Q-REPO-98 · The circuit: a probe claims its slot when it sends, every success is one statement, and a replica follows through an asynchronous reload

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-029 says the first request on each replica after `open_until` probes and the others answer as open, that healthy traffic causes no writes, and that `trove_remote_upstream_up` is 0 on every replica within 2 s.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The probe slot is claimed right before the upstream request and given back when it ends; a success runs one conditional UPDATE that matches no row while the counter is 0; the state is a registered cache of area `upstream_state` that a change reloads in the background (chosen) | Requests that are answered earlier (negative entry, refusal to fill) never hold the slot; a 404 neither counts nor resets | The slot is never lost to a request that did not fetch; no write when healthy; the gauge follows without traffic for repositories the replica has served | A statement per success; a repository that a replica never served has no series on it |
| (b) Claim the slot at the circuit check | At step 7 | Earlier | A request that stops at step 8 or 9 leaves the slot taken |
| (c) Count in memory and write rarely | Batch the counter | Fewer writes | n replicas count a burst differently and open the circuit late |

**Choice:** (a). A failure whose UPDATE finds the circuit open reads the state and holds it open at once.

**Rationale:** The development agent would revisit it if the success statement is measurable in a profile.

**Applied in the spec:** R-REPO-029 (the circuit bullets).

### Q-REPO-99 · The gone-package gate and the URL map

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-029 and R-REPO-043 leave open how the gone set is keyed when the package document has variants, what the cache view does, and where the foreign-host check happens.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The gone set maps the path of each gone document to its variant; the gate refreshes the package document under the normal steps and answers 404 for the cache view and only-if-cached; a mapping is dropped when it equals the derived URL, ignored when it is not of the document's package, and checked against the hosts when the artifact is fetched (chosen) | A format that tracks removal is skipped; the refresh clears the mark through the fill | The mark heals for clients that never request documents; a document cannot choose another package's artifact; a host added to `extraHosts` takes effect for recorded URLs | Recorded URLs on a foreign host stay in the table until they are fetched or replaced |
| (b) Check the host when the mapping is recorded | Refuse the document | Earlier | A later `extraHosts` change would not apply to what was recorded |
| (c) Key the gone set by package name | The format maps both ways | Smaller set | Needs an inverse of `PackageDocumentPath` the contract does not have |

**Choice:** (a).

**Rationale:** The development agent would revisit it when a format with several variants of the package document exists.

**Applied in the spec:** R-REPO-029 and R-REPO-043 (no change of text; the criteria are tested).

### Q-REPO-100 · The prefix of an invalidation is whole segments, validated up front

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 defines when a prefix covers a path, and the call takes the prefix from a request body. A prefix such as `/@acme`, `@acme/` or `a//b` can never cover a canonical path, and the prefix travels in the change payload, whose limit is 1 KiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept any string | The matcher compares as written | No rule to document | The operator's typo is a silent no-op that still answers 200; the payload is unbounded |
| (b) Normalise | Trim leading and trailing slashes | Forgiving | `a/` and `a` become one thing; a client cannot tell what was recorded |
| (c) Validate | Empty, or segments without a leading or trailing slash, an empty segment or a dot segment; valid UTF-8 without control characters; at most 256 bytes; otherwise `422 validation_failed` with the pointer `/path` | A mistake is answered, not stored; the payload is bounded | A client that sends a trailing slash must learn the rule |

**Choice:** (c). The entry list applies the same rule to `pathPrefix`, as a query parameter with `400`.

**Rationale:** the longest npm package name is 214 characters, so 256 bytes covers the name rule of the MVP formats with the scope. The development agent would revisit the limit when a format with longer paths is added (Maven coordinates), by raising it together with the payload encoding, which carries the prefix as base64url (Q-OPS-154).

**Applied in the spec:** the prefix bullet of R-REPO-045; R-OPS-021.

### Q-REPO-101 · The row is stamped inside its transaction, and the count is of metadata entries only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 compares the row's time with the `validated_at` of an entry. The column default `now()` is the start of the transaction, and the transaction also counts up to a million entries.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `DEFAULT now()` | The time of the start of the transaction | Nothing to write | A fill that validates an entry while the transaction runs gets a `validated_at` after the row's time and escapes an invalidation that commits after it |
| (b) `clock_timestamp()` in the insert | The time of the statement; the insert is the first statement and the count follows it | The window shrinks to the rest of the transaction | Still the database's clock, not the commit's |
| (c) The application's clock | The replica stamps the row | One clock per replica | Replica clocks differ; the page compares with times that the database wrote |

**Choice:** (b). The count is of the metadata entries under the prefix (kind `metadata`, those marked gone included, no artifact), and the scope `negative` answers 0 without a query.

**Rationale:** a row can only err by the length of one count in the invalidation's disfavour, and the 5 s allowance of R-REPO-028 covers the negative entries that the replicas time themselves. The development agent would revisit this if the proxy engine stamps `validated_at` with a replica clock instead of the database's: the comparison then needs the same allowance as the negative entries.

**Applied in the spec:** the mechanism and count bullets of R-REPO-045.

### Q-REPO-102 · The matcher keeps the newest time per prefix and applies a row from its payload

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: every request for a cached document asks whether an invalidation covers it. The rows are few (bounded by the table and its retention) but the questions are many, and R-OPS-021 forbids a database round trip per request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A list of rows, scanned per request | Compare with every row of the repository | Trivial | The cost of a request grows with the number of invalidations |
| (b) A map from prefix to the newest creation time, per scope class | A lookup tries the empty prefix, each leading segment and the path: one map access per segment | Constant in the number of rows; no allocation; immutable snapshots need no lock | A change rebuilds the repository's maps (rare) |
| (c) A trie of segments | Walk the path | The same bound | More code for the same answer |

**Choice:** (b). A replica loads every row at start (a failure stops the start-up), applies a new row from its change payload without a query, and reloads a repository after a deletion notice, every repository after a flush of the area, and all of them every `cache.snapshot_safety_ttl`. A reload merges the rows that arrived by payload while its query ran. A row leaves memory only when a reload no longer finds it in the database.

**Rationale:** a lookup takes about 30 ns without an allocation with 50,000 rows (benchmark in the package). The safety reload covers the rows that the cascade of a repository deletion removed without a notice. The development agent would revisit this if the rows of a single repository grow into the millions, which the retention of R-REPO-045 should prevent.

**Applied in the spec:** the in-memory bullet of R-REPO-045.

### Q-REPO-103 · The fold never extends an expiry, does not skip locked rows and checks once more before a deletion

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-045 folds a row into the entries validated before it by setting `expires_at` to the invalidation time, and deletes a `metadata` row once it is folded. Two cases are left open. An entry that already expires before the row would get a later expiry from a literal `SET expires_at = <time>`. A fill that was in flight when the row committed can store an entry with a `validated_at` before the row after the fold ran, and the deleted row can no longer invalidate it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The literal statement | `SET expires_at = <time>` for the entries validated before the row | As written | Revives an entry that had expired |
| (b) `LEAST` and a condition | Only entries with `validated_at` before and `expires_at` after the row, set to the lesser, in batches of 5,000 locked in key order, each batch in its own transaction | Idempotent; shortens only; a run is resumable by the columns alone | Waits for a locked row instead of skipping it |
| (c) `SKIP LOCKED` | Leave a locked entry for the next run | Never waits | A skipped entry stays fresh in the database once the row is gone |
| Deleting a folded `metadata` row | (i) at once; (ii) one hour after the fold, with one more fold pass before the delete | (ii) finds the late fill | (ii) holds the row for an hour |

**Choice:** (b) with (ii): a folded `metadata` row is deleted one hour after `folded_at`, and every row is folded once more immediately before its deletion.

**Rationale:** an hour is far above the 60 s default of `cache.metadataRefreshTimeoutSeconds` and its 600 s limit, which bound how long a request waits for a fill. A fold of one million entries takes about 70 s on the integration stack (200 batches); the call that created the row does not wait for it. The development agent would revisit the hour if a fill can outlive it, for example through a detached refresh with a longer limit.

**Applied in the spec:** the fold and deletion bullets of R-REPO-045.

### Q-REPO-104 · Members, deploy target and pin rules are rows, and the document is rendered from them

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-017 and R-REPO-021 define the tables `virtual_members` and `virtual_deploy_targets`, and R-REPO-023 `virtual_pin_rules` and `virtual_pin_members`, with foreign keys that make the detach rule of R-REPO-013 hold in the database. The grant index, as built in M1, read the member list and the deploy target out of the `virtual` section of `repositories.config`. Milestone M1 never stored a virtual section (Q-REPO-16), so nothing has to be migrated.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rows are the source of truth | `repositories.config` keeps `virtual.mergedMetadataTtlSeconds` only; a read renders the section from the rows, a write replaces them in the transaction that raises `config_version`; the grant index reads the rows in its snapshot | `ON DELETE RESTRICT` enforces R-REPO-013 and the deploy-target rule; one copy; a member check under a share lock cannot race a deletion | Every read of a virtual repository loads four small queries; the update compares complete documents to find what changed |
| (b) The document is the source, the tables a copy | The JSON keeps everything and the service mirrors it into the tables | One read | Two copies that can differ; the foreign keys protect a mirror only |
| (c) Tables unused | The document keeps everything, no foreign keys | The index needs no change | R-REPO-013's rule is code only and racy; the schema of 00015 is dead |

**Choice:** (a). `Settings.Virtual` is a typed section (a misspelt member is a 400 like any other); `marshalConfig` writes the stored form and `marshalFull` the complete one. The members are locked `FOR SHARE` in id order while the update checks them, so the update that marks a member for deletion waits. A list read loads the rows of all virtual repositories in one query each.

**Rationale:** R-REPO-013 says the database holds the rule, and only (a) does. The development agent would revisit this if reading a virtual repository becomes a measured cost; the snapshot already holds the rendered form for the request path.

**Applied in the spec:** R-REPO-017 (the bullet on rows and the share lock).

### Q-REPO-105 · A global claim is changed only with `repositories:write-config`, also when it is narrowed or dropped

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-023 asks for `repositories:write-config` "when the request adds or keeps a `global` claim". A `PUT` replaces the manual claims as a set, so a request can also narrow a global claim to `members` or leave it out, which removes the protection of the name in every virtual repository and at every remote repository without ever containing the word `global`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any change that touches a global claim needs the permission | The permission is needed when the body holds a global claim, or the stored set holds a global claim that the body narrows or omits | Closes the way round the rule; the claim set stays one resource | A holder of `manage` only cannot edit the members claims of a repository that has a global claim through this `PUT` |
| (b) Exactly the text: adding or keeping | Narrowing and removal need `manage` only | Follows the words | A holder of `manage` can reopen a dependency-confusion path by omission |
| (c) Per-claim endpoints | `PUT`/`DELETE` of one claim | Permissions per claim | A different API; R-REPO-023 fixes the replace-all resource |

**Choice:** (a). The service takes a `GlobalGuard` and calls it inside the write transaction, after the `If-Match` comparison, when the request touches a global claim; the handler passes a function that asks the request's access holder for `repositories:write-config`. A refusal changes nothing.

**Rationale:** The sentence of R-REPO-023 names the risk (a stale or careless replace that drops a global claim), and (b) leaves that risk open for a holder of the lesser permission. The development agent would revisit this if operators ask for team-level editing of members claims on a repository with a global claim: then a body that leaves the global claims untouched could be allowed to omit them (merge semantics for the global part).

**Applied in the spec:** R-REPO-023 (the API table and the paragraph on claim scope).

### Q-REPO-106 · Claim and pin-rule patterns are stored folded, a manual claim replaces an automatic one, and the limit spares the deploy

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-023 leaves four details open. (1) A NuGet claim `Acme.*` and `acme.*` are one pattern for the matcher; the table key is the string. (2) A manual claim for a pattern that an automatic claim of the repository already holds hits the primary key `(repo_id, pattern)`. (3) The limit of 1000 claims per repository includes automatic claims, one per name, so a repository of 1,500 unscoped packages reaches it. (4) R-REPO-013 says the delete job removes claims in batches and notifies each batch; a batch-wise removal ends the protection of the names before the repository is gone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Folded storage; manual replaces automatic; at the limit the auto claim is skipped and the deploy goes on; claims go with the row | Patterns are stored in the format's folded form; a listed pattern of an auto claim becomes manual (the creator and time are the writer's); an auto claim at the limit is not created and the deploy's audit event records `autoClaim: limit`; the claims cascade with the repository row and the row's transaction notifies `claims` | One pattern, one row; a person's decision wins; no publish fails for a limit; names stay protected until the repository is gone | An auto claim is missing at the limit (the name is then protected only by the local tier); the displayed pattern is the folded form |
| (b) Store as written | Case variants are two rows | Shows what was typed | Two claims for one name; the order of precedence between them is accidental |
| (c) Refuse the deploy at the limit | 409 at the 1001st first publish | Never an unprotected name | A publish fails for a reason the publisher cannot fix; the fix is a manual claim `@scope/*` by someone else |
| (d) Batch-wise removal as written in R-REPO-013 | Claims go before the items | As specified | The names of a global claim are open to the upstream while the job runs |

**Choice:** (a).

**Rationale:** A claim that cannot be created must not turn a publish into an error, and a limit that is reached means a broad manual claim (`@scope/*`, `prefix-*`) is the right tool; the response of `GET .../claims` shows the count. The development agent would revisit the limit behavior if the raised audit value shows real installations hitting it, and would then offer (c) as a repository setting.

**Applied in the spec:** R-REPO-023 (limit, folded form, manual over auto), R-REPO-013 (claims go with the row).

### Q-REPO-107 · Pin-rule edge cases: one pattern, one rule; an empty rule is a block; and a detach keeps it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: "The most specific pattern over all rules of V selects its rule" has no answer when two rules hold the same pattern. A rule with no members could be read as "no restriction" or as "nothing". A detach (R-REPO-013) can remove the last member of a rule.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse a pattern in two rules; an empty rule answers 404 for its names; a detach leaves the rule; members are kept in member-list order | `invalid_pattern` "already used by rule n"; the rule is a deliberate block; a read returns a rule's members in the order of the member list | Total order, no accident; a detach never opens a name to the upstream; a document compares equal to itself | A rule that lost its members must be removed by an editor |
| (b) Earlier rule wins | Duplicates allowed | No new error | Order of rules becomes meaning |
| (c) A detach drops an emptied rule | The rule goes with its last member | No dead rules | A name that was pinned to a partner registry falls through to the claims and the upstream: the opposite of the purpose |

**Choice:** (a).

**Rationale:** Pin rules exist to keep a name from the wrong source; every default in (a) fails closed. The development agent would revisit the third point if the UI shows emptied rules as noise: it can offer to delete them.

**Applied in the spec:** R-REPO-023 (pin rules bullet), R-REPO-013 (detach).

### Q-REPO-108 · Routing reads the snapshot for members and pins and a cache of its own for claims

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-023 wants the claims and pin rules of all repositories in a prefix trie per format, rebuilt from the change area `claims`. Pin rules are part of the document of a virtual repository and change with `repo_config`; claims change with every first publish.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Pins in the snapshot, claims in a cache of their own | Members, deploy target and a pin index per virtual repository are built into the repository snapshot (area `repo_config`); the claim index is a registered cache `claims` on the area `claims`, holding repository ids; `Route` joins them through the snapshot of the same call | A first publish does not rebuild the snapshot; each is current within the change-log bound; no query per request | Two caches; a claim of a repository the snapshot does not know yet is a non-member for a moment (a global claim still applies) |
| (b) Everything in one cache on both areas | One trie holding claims and pins | One cache | Every first publish rebuilds the repository snapshot; the cost grows with the number of claims |
| (c) Query per request | Claims and pins from PostgreSQL | Always current | A database query on every resolution, against R-REPO-023's "a lookup costs no database query" |

**Choice:** (a). `Routing` is built on the `SnapshotReader` and registers the cache `claims` (`SafetyTTL` = `cache.snapshot_safety_ttl`). The area `claims` is also notified when pin rules change, so everything that routes reads one area.

**Rationale:** The two inputs change at different rates and both tolerate the propagation bound of R-ARCH-018. The development agent would revisit this if a measurement shows the join through the snapshot on the request path.

**Applied in the spec:** none beyond R-REPO-023's text, which already describes the trie and the area.

### Q-REPO-109 · `pattern_unscoped_only` is decided by the format's name rule

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-005 says the warning applies "on an npm repository", because `*` does not cross `/` and only npm has names that contain one. The model must not name formats (R-ARCH-008), and the catalog has no fact "names can be scoped".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ask the name rule | The warning is raised for a format with a name subject whose `Names.Normalize` accepts `@scope/name` | No new fact; follows what the format accepts; NuGet (IDs without `/`) never warns | A heuristic: a format whose rule accepts the probe but has no scoped names would warn |
| (b) A catalog fact `ScopedNames` | The format declares it | Exact | A change of the catalog contract owned by the format work packages |
| (c) Warn for every name-subject format | No detection | Simple | A false warning on NuGet |

**Choice:** (a), with the probe `@scope/name`.

**Rationale:** The warning is advice, not a rule, and (a) is correct for the two MVP formats. The development agent would revisit this with the first format that has `/` in names and a different rule, and then add the fact (b).

**Applied in the spec:** R-REPO-005 (the sentence on `pattern_unscoped_only`).

### Q-REPO-110 · The merged-metadata key carries the populate right and the endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-020 keyed a merged document by "the ordered list of members that took part after permission filtering". R-AUTHZ-008 restricts the document of a remote member that the caller may not populate to the versions whose artifacts are cached, and R-EP-019 lets an endpoint switch the right off for every caller. Two callers with the same readable members can therefore be owed different bytes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Fingerprint of the endpoint and the ordered (member id, class, mayFill) | The populate right is decided per member by `MemberUsableOn` before the key is made | Callers with equal rights share; no entry is served to a caller owed other bytes; costs one SHA-256 of a few dozen bytes | The same set of members may hold two entries (one per right) |
| (b) Keep the wording and never restrict a merge | A non-filling caller gets every version the remote document lists | One entry per member set | Violates R-EP-018: a version a filtered document lists is not downloadable, and a caller sees versions it cannot get |
| (c) Key by the subject | An entry per principal | Trivially correct | The cache stops sharing; its size grows with the number of callers |

**Choice:** (a). The key is the virtual repository, the canonical path, the variant, the endpoint and the fingerprint (`planFingerprint`); the key also carries the member ids and the hash of the package name so that an eviction finds the entries of a member or a package without a second index.

**Rationale:** (a) is the least that is correct: the right is the only caller-dependent input of the bytes besides the member set. The development agent would revisit this if a measurement showed many entries per member set, and then share the common prefix of the restricted and the unrestricted merge.

**Applied in the spec:** R-REPO-020 (the key), R-ARCH-015 step 4, formats/npm.md R-NPM-013 (the merge fingerprint inputs).

### Q-REPO-111 · The virtual engine meets its neighbours through interfaces it declares itself

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the virtual engine needs the remote engine (documents, caches, state, probes), the item store and package rows, the routing, the decision point and a format's adapter. The remote engine is built beside it, and the layers forbid a domain package to import a format.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Import the neighbours | `internal/virtual` imports `internal/proxy` and the formats | Fewer types | Cannot be built or tested before the proxy engine lands; the layer rule forbids the format import |
| (b) Consumer-side interfaces in `internal/virtual`, bridged in `internal/formathost` | `Router`, `Decider`, `Locals`, `Remotes`, `Tiers` and `Merger` are declared where they are used; `formathost` turns a `formats.Request` into a `Caller` and a `Descriptor`, a `VirtualAdapter` into a `Merger` and the Host's stores into `Tiers` | Builds and tests with fakes; the proxy engine binds by implementing five methods; the engine stays format-neutral | Two small bridge types to keep in step with the contract |
| (c) Put the engine into `formathost` | One package | No interfaces | `formathost` is an edge package that the dispatcher imports; the engine would be untestable without a Host |

**Choice:** (b). The errors of a remote member are classified by their kind: `NotFound` and the refusal to populate are a miss; `UpstreamFailed`, `UpstreamTimeout` and `Unavailable` are a transient failure (the merge drops the member and is partial, an artifact goes on to the next member and answers with the first such failure when nothing is found); anything else ends the request. A failure after the first byte of an artifact ends the request, because the next member cannot continue it. A replica without the remote engine treats every remote member as a transient failure.

**Rationale:** the classification is the one R-REPO-019 step 7 states, expressed in the kinds of R-ARCH-023. The development agent would revisit the interface list when the proxy engine's real API differs from plan section 3.4; the binding is documented in [virtual.md](../developer/virtual.md).

**Applied in the spec:** R-REPO-019 (step 7, the failure after the first byte, the member that cannot be populated).

### Q-REPO-112 · A merged entry validates itself on every hit and is never kept when partial

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-020 and R-ARCH-015 want a local publish visible on every replica when its commit has returned, with or without Valkey, a period that never outlives the remote entries, early eviction by `metadata_changed` and the change areas, and no storage of a partial merge.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The entry records the rev_seq of every eligible local member and one `RevSeqs` query checks them per hit | Valkey and the signal only evict early | Correct without any message; one indexed query per hit | A query per hit |
| (b) Evict by signal and change area only | No query | Cheaper | A lost signal shows stale local data until the period ends: violates R-ARCH-015 |
| (c) Build every merge, cache nothing | No state | Simplest | The remote and local documents are re-collected on every request |

**Choice:** (a). The record is the revision returned by `LocalDocument` (so a document and its revision always match), 0 for a member without the package, and the revision of a package row without a live version is recorded as it is. The cache is the registered `merged_metadata` cache with the fill rule of R-ARCH-018 on the areas `cache_invalidation`, `claims` and `repo_config`; a result that must not be kept (a partial merge, an entry that is already expired) weighs more than the whole cache, which the cache treats as "not stored". `claims`, `repo_config` and every payload this build cannot read flush the cache; a virtual invalidation drops the entries of that repository under the prefix, and one of a remote repository those built from it. `metadata_changed` carries `<repository id>:<hex SHA-256 of the package name>` and drops the entries that contain the member and the package. A path with no package name and a period of 0 are not cached. The expiry is the earlier of the period and the earliest expiry of the remote documents; the safety TTL is 2 hours.

**Rationale:** (a) is the only option that keeps the guarantee without a message. The development agent would revisit the query per hit if the budget of R-ARCH-027 is missed by it, and then batch it with the rendition cache's revision read.

**Applied in the spec:** R-REPO-020 (cacheable cases, partial), R-REPO-047 (the Cache-Status values of a merge).

### Q-REPO-113 · A caller who may not fill a remote member gets only the versions that are cached

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-008 says npm restricts the remote member's document of such a caller to versions whose tarballs are cached, and the format's `Merge` is the only code that can edit a document. The contract's `MemberDocument` had `CachedVersions` and no way to say that the caller may not populate the member.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A field `Restricted` on `MemberDocument` | The engine sets it from the populate right; the format lists only `CachedVersions` of a restricted part | Explicit; one bool; the kit can test it | A field added to a contract that is not yet frozen |
| (b) A nil `CachedVersions` means unrestricted | No new field | No contract change | A restricted part with nothing cached would need a non-nil empty map: an easy mistake that leaks versions |
| (c) The engine filters the document | The engine edits the bytes | The format cannot get it wrong | The engine would parse every format's document |

**Choice:** (a). `CachedVersions` of a part that is not restricted only moves a version to the second tier of the source rule. The set comes from `CachedArtifacts` of the remote engine below the artifact prefix of the package (the directory of an `index.json` document, else the document path) and the format's own `Classify` of each cached path.

**Rationale:** the restriction is a security property of the merge (R-AUTHZ-008, R-EP-018) and should not hang on a convention. The development agent would revisit this when the contract freezes (E3.3), and fold the flag into the part's tier if a second use appears.

**Applied in the spec:** none beyond R-REPO-020 and R-AUTHZ-008, which already state the rule; the field is in [formats.md](../developer/formats.md) and [virtual.md](../developer/virtual.md).

### Q-REPO-114 · The explanation names readable members in list order and hides the rest

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-019 lists the decisions and says members the caller cannot read appear only as a count unless the caller holds `repositories:read-config`. It does not say in which order the members come, what a member that routing dropped and the caller cannot read looks like, what the claimants of `decidedBy` may show, or what the explanation says when the virtual repository itself refuses.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) List order; unreadable members are counted whatever the route did to them; claimants filtered by read; a refusing virtual repository shows `disabled` or `not_admitted` for every member | One rule: a key reaches only a caller who may read it, or may read the configuration | No key leaks through a dropped member or a claimant; the list equals the member list the caller can edit | The resolution order is shown only through `class` and the tiers |
| (b) Resolution order, unreadable members counted only when kept | Shows the order a request uses | More informative | A dropped unreadable member would be named |
| (c) Answer 404 when the virtual repository refuses | Same as the client | Simple | An explanation exists to say why a client gets 404 |

**Choice:** (a). The state of a remote member (`negative`, `offline`, `circuit-open`) comes from the remote engine's `EntryState` and is reported only when the caller may populate the member and nothing is stored; the explanation makes no call that can reach an upstream. A holder of `repositories:read-config` gets the unreadable members as `no-read` and a count of 0. An operation on a repository that is not virtual answers 400 `not_applicable`.

**Rationale:** (a) keeps the security note of R-REPO-019 true for every field of the answer. The development agent would revisit the order if the UI wants the resolution order, and add a position to each line.

**Applied in the spec:** R-REPO-019 (the explanation paragraph).

### Q-REPO-115 · The shadowing check fails closed and asks each remote repository once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-048 fixes the outcomes. It leaves open how several virtual repositories that contain one remote repository are probed, what Retry-After is when several circuits are open, what an error of the stored-state read means, what a replica without the remote engine does, when the metric counts, and how a format learns the refusal and the pairs for the audit event.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One probe per remote repository; the longest retry; a read error fails the publish; no engine means undetermined; the metric counts the checks that ran; the outcome carries the refusal and the pairs | The smallest number of upstream requests, the safest answers, the format does one thing with the outcome | Fixed, testable rules | A new field in the contract's `ShadowOutcome` |
| (b) One probe per pair (V, R) | Simple loop | The same upstream asked twice for one publish | More load, contradicts "one probe per publish" |
| (c) A read error is undetermined | A publish by a manage holder goes on | Never refuses for a database error | A database fault would decide a security question |

**Choice:** (a). The probe is made on behalf of the publisher without the cache-fill check and stores nothing. `formats.ShadowOutcome` gains `Refusal` (the 403 or 503 to answer, naming the package and the virtual repositories the caller can read, never a remote member) and `Shadowed` (the pairs for `repository.remote_package_shadowed`). The codes `shadows_remote_package` and `shadow_check_unavailable` are registered in the problem catalog.

**Rationale:** the spec says an unreachable upstream must not be a way around the check and that a value read from outside may only make trove refuse; (a) extends that to the engine's own failures. The development agent would revisit the approach to read errors if the database fault window of a publish turns out to be a support cost.

**Applied in the spec:** R-REPO-048 (the fail-closed bullet), R-REPO-047 (the metric).

### Q-REPO-116 · The format signals a refresh, and the engine does not

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the fill transaction of a remote repository attached a `metadata_changed` signal whose payload was the bare repository id, and the npm hook `OnFill` attached the signal with the repository id and the package-name hash. The virtual engine parses the second form; a payload it cannot read drops every merged entry (R-REPO-020), so every fill of every remote repository emptied the merged-metadata cache of every replica and raised its generation, which also discarded the merges being built at that moment.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine sends none; the format's `OnFill` signals the package it stored | One signal per stored change, with the payload that names the package | The package name is the format's knowledge; a fill that kept the stored document sends nothing | A format that forgets the signal evicts late, until the entry expires (never later than the remote entries) |
| (b) The engine sends `virtual.PackageChanged(repo, package)` when the fill replaced the document | The engine owns the signal | A format cannot forget it | The engine would know a package name for every format (an ID for NuGet registrations), and two signals for one change |
| (c) Keep the bare id and make the consumer drop the entries of the repository | Coarser than the whole cache | Simple | Every refresh still empties the merged entries of every package of the repository |

**Choice:** (a). `Options.Signals` of the proxy engine is removed. `TestR_REPO_020_EarlyEviction` (internal/virtual) pins that a payload of one package drops only that package's merges, and `TestR_NPM_024_OnFillRecordsAFillAndKeepsTheNewerDocument` pins the payload the hook sends.

**Rationale:** the signal only evicts early; correctness never depended on it, so the damage was the cache's effectiveness, and a format that omits it is late, not wrong. The development agent would revisit the choice when a second remote format exists and both send the same payload: the engine could then own it through a method of the adapter.

**Applied in the spec:** R-REPO-020 (early eviction).

### Q-REPO-117 · A stored item the format withholds is a miss of its tier

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a local member can store the item of a version and have it tombstoned (a version withheld by a restore's replay, R-NPM-047). The merged document lists the version from a remote member, because the local document leaves out tombstoned versions. A tarball request found the stored item in tier (a); serving it ended in the format's 404, and the walk did not try the remote tiers, so the document advertised a version whose tarball was 404 through the same virtual repository.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The tiers ask the format's serve check; a not-found refusal is a miss of the tier | The walk continues with the next member | The document and the tarball agree (R-NPM-030); the check is the one the serve path runs anyway | One more indexed lookup per stored hit (the serve path repeats it) |
| (b) The format hides tombstoned items from `Stat` | The tier never sees the item | No second lookup | `Stat` is generic and also serves /files/ and the cache view, which must see the item |
| (c) Leave it | 404 | Nothing to build | A document that lists a version it cannot serve |

**Choice:** (a), in `internal/formathost/virtual.go` for the local tier and the remote cache tier. Any other refusal, such as an error of the mark lookup, ends the walk.

**Rationale:** the refusal is the format's decision about its own marks, and the tier walk is the generic rule of R-REPO-019. The development agent would revisit the choice if the lookup shows in a profile of virtual artifact requests; the check can then move into the `Stat` handle.

**Applied in the spec:** R-REPO-019 (artifact tiers).

### Q-REPO-118 · A first version that the shadowing check skipped is refused at the commit

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the shadowing check of R-REPO-048 asks nothing when the repository holds a live version of the name. An unpublish that removes that version between the check and the commit makes the publish the first version of the name in the commit transaction, which then claimed the name and recorded no shadowing decision, so a remote member's package could be shadowed without the R-REPO-048 decision.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit refuses with 503 `shadow_check_unavailable` and `Retry-After`; the retry runs the check | Fail closed, the client's retry is the repair | No check inside a transaction, which would hold a connection during upstream requests (R-NPM-046); narrow case | One failed publish in a rare race |
| (b) Run the check inside the commit | Never fails | No retry | Upstream requests inside a transaction |
| (c) Always run the check, also when a version is live | Never skipped | No race | A probe per publish of every version, against "once for a first version" of R-REPO-048 |

**Choice:** (a). The check reports that it was skipped and the hook of the commit refuses a first version in that case.

**Rationale:** the situation needs a concurrent unpublish of the last version of the name, and the answer 503 is the one a client already retries. The development agent would revisit the choice if the retry shows in the audit log of a deployment that unpublishes and republishes names in loops.

**Applied in the spec:** R-NPM-007 step 1 (the paragraph on the shadowing check).

### Q-REPO-119 · The URL map follows the stored document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the fill transaction recorded the URL mappings of the fetched document on every pass, also when the format kept the stored document (a superseded or stale copy) or the upsert kept a newer row. Two concurrent fills could then leave the map of the older document next to the newer stored document.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Record only when this fill's document was stored | The map and the document describe one document | Matches R-NPM-024; the order of the statements does not matter | The record must wait for the upsert's answer (it already does) |
| (b) Record always and repair on the next fill | Simple | Self-healing | A tarball fetched from the older document's URL in between |

**Choice:** (a): a fill that renewed the stored entry, or whose upsert returned no row, records no mapping.

**Rationale:** the digest check still refused a wrong byte stream, so the exposure was an avoidable 502; the map is cheap to keep right. The development agent would revisit the choice if a format stores more than one document per package.

**Applied in the spec:** R-REPO-043 (the bullet on the map).

### Q-REPO-120 · The format can tell the upsert to take a document whatever its fetch time

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-026 keeps the entry with the newer `fetched_at`, which is the time the response headers arrived. A format with an order of its own (npm: `time.modified`) can see a document that is newer by that order and was fetched earlier, because a lagging edge answered the older document after the newer one was fetched. The format then wrote the records of the newer document while the upsert kept the older entry, so the served document and its records described two documents (an installable version missing from the document, no URL map row for it).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `OnFill` answers with a decision: by fetch time (default), keep the stored document, or replace it whatever the stored fetch time; the upsert takes a `force` flag from it | The format's order outranks the core's only when the format says so | One owner per comparison; formats without an order keep R-REPO-026 unchanged | The interface answer grows from a bool to three values |
| (b) The engine drops the `fetched_at` condition when `OnFill` answers "replace" | No new value | Every format that answers "replace" silently loses the newer-fetch rule | A format without an order of its own loses R-REPO-026 |
| (c) The format writes the entry's `fetched_at` itself | No engine change | Format code writes a core table | Breaks the layering of R-ARCH-012 |

**Choice:** (a). The upsert with `force` still writes the document's own `fetched_at`, validators and freshness.

**Rationale:** the stored document, its records and its URL map have to describe one document whichever fill commits last (R-NPM-024); that is only possible when the decision that wrote the records and the decision that wrote the entry are the same one. The development agent would revisit the choice if a second format records an order of its own and the three values prove too coarse.

**Applied in the spec:** R-REPO-026 step 5 (below), [architecture.md](architecture.md) R-ARCH-012 (`OnFill`).

### Q-REPO-122 · The format's settings are a member of the document per format, decoded with the rest

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-004 says format settings live under a member named after the format, and the decoder refused one: it reads the document strictly into a struct, so an `npm` member was an unknown member (400), and npm's
settings could be written only by SQL. The member has to be validated against the format's schema (Q-ARCH-229), stored, read back, and covered by the reflection that finds unknown members of a merge patch.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One typed member per format (`Settings.NPM json.RawMessage`) and a table of them | Decoded, patched and unknown-member-checked like every other member; validated in `validateSections` | No custom marshalling of a type that is embedded in the API's representation (a `MarshalJSON` on `Document` would be promoted into it and drop the server's members); the OpenAPI schema lists the member by name | A format with settings adds a field and a table entry (checked by `formats.Validate`) |
| (b) A map `format -> raw` with custom (un)marshalling | Any format without a change in the model | No edit per format | The custom marshaller is promoted through the embedding; the reflection that finds unknown members needs a special case; the member is open-ended in the OpenAPI document |
| (c) Settings in a separate resource | `/repositories/{key}/settings` | No change to the document | Configuration as code (R-OPS-029) and the create form need one document; a second resource to keep in step |

**Choice:** (a). The member is stored in `repositories.config` in a canonical form (members in key order, no spacing), so that a document read and sent back is byte-equal to what is stored and an update that changes nothing is
not a change. A member of one format on a repository of another is `not_applicable`; a `null` member is an omitted one (R-REPO-011). A format is not named like another member of the document (`formats.Validate`).

**Rationale:** the set of formats is small and compiled in; a field per format with settings is one line, and the compiler and the reflection do the rest. The development agent would revisit it when a third format with settings appears and the table
grows by a line each time without anyone noticing: a generated member list would then pay for itself.

**Applied in the spec:** R-REPO-004 (the table row), [formats/npm.md](formats/npm.md) (Q-NPM-59). Code: `internal/repository/{document.go,validate.go}`.

### Q-REPO-123 · A name the format cannot fold fails closed in routing

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `catalog.Names.Normalize` folds a name and validates it. The routing used `if n, ok := NormalizeName(...); ok { name = n }` and otherwise looked up the name as written. For npm, where folding is validating and names are
case-sensitive, that is harmless. For a case-insensitive format the lookup of an unfolded spelling misses a claim or a pin rule on the folded form: a claim on `acme.*` would not cover a name written `ACME.Widgets 1` (that the rule rejects), and the remote member would be
asked: the dependency-confusion protection of R-REPO-023 would fail open.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `!ok` means "no name of the format": nothing is looked up, the virtual repository admits nothing under it, no remote member is consulted, a remote repository refuses it | Routing checks the second result of the rule | Fail closed at every site that looked up a claim or a pin; one rule | A format whose rule rejects a valid upstream name would block it: its rule must be the widest it accepts anywhere (the contract says so) |
| (b) Fold what can be folded and look up the rest as written | Today | No refusal | The failure it exists to prevent |
| (c) Fold in two functions (validate, fold) | Split the interface | A name can be valid and unfoldable | A second function for a case no format has; the contract freezes with both |

**Choice:** (a). The virtual route reports `Admitted` false and drops the remote members as `not_admitted` (no new decision value for the explanation API: it is a pattern-style refusal, and the generated clients keep their enum); `RemoteRefused` answers true
for such a name whatever `proxy.claims.remote_refusal` says, because the refusal is not about claims; the shadowing precondition `CoveredByOwnClaim` is false, so the check asks; `ClaimsFor` returns nothing. `Names.Normalize` is specified as the widest rule
the format accepts anywhere, with a rule that only local repositories apply checked in `Inspect`.

**Rationale:** npm's `Normalize` is `name.Parse`: the change refuses names that no handler lets through anyway. The development agent would revisit it when NuGet's rule is written: if its upstream and local rules differ, `Normalize` carries the upstream one and `Inspect` the local.

**Applied in the spec:** R-REPO-019 (step 3), R-REPO-023 (remote refusal), R-REPO-048. Code: `internal/repository/routing.go`, `internal/formats/catalog/catalog.go` (the doc of `Names`).

### Q-REPO-124 · The OpenAPI member of a format's settings is written by hand and compared with the registered schema

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the generated clients are built from the OpenAPI document, and the create form from the schema the format registers. A setting added to one must reach the other.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two statements and a test that compares them | `NpmSettings` in `api/openapi.yaml` and `settings.schema.json`; `TestR_ARCH_008_NpmSettingsSchemaEqualsTheOpenAPIMember` compares the properties, types, values and defaults | Typed clients; the schema is the one the service validates against; a drift fails a test | Two places to edit |
| (b) Generate the OpenAPI schema from the registered one | A generator step | One place | The OpenAPI document is edited by hand everywhere else and is the source of the generated types; a generated block in it breaks `generate-check` for people editing it |
| (c) `additionalProperties: true` in OpenAPI, the registered schema alone | No second statement | One place | The typed client has no setting names; the document says nothing |

**Choice:** (a).

**Rationale:** the comparison is a table of four properties. The development agent would revisit it when a format with many settings makes the second statement a burden.

**Applied in the spec:** R-REPO-004. Code: `api/openapi.yaml` (`NpmSettings`), `internal/api/v1/format_settings_test.go`.

### Q-REPO-125 · The project of a repository is written on create only, and a project administrator reads the whole document of its project

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)**: the project comes from the address of the create request and of every operation, so the document's `project` is read-only and the Owner reads the full document of its project's repositories.

Context: R-AUTHZ-026 assigns a repository to a project in the transaction that creates it and makes `project` read-only afterwards. Open: what a replace or a patch that names a project does, and what the list and the read show to a project administrator, who holds no `repositories:read-config`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `project` is a member of the representation, taken out of the document by the handler; equal to the stored value it is ignored, otherwise 422 `immutable`; project administrators read the full document of their project (chosen) | A body obtained by `GET` can be sent back; the service compares the stored project with the body's under the row lock; the list and the read treat an administrator of the repository's project as a holder of `repositories:read-config` for that repository only | One representation; no silent change of the assignment; the read-config equivalence of R-AUTHZ-026 holds per repository | The handler and the service each know the member |
| (b) `project` is ignored on a replace and a patch | Whatever it says, the stored project stays | Simplest | A client believes it moved a repository and it did not |
| (c) A second document for project administrators | A project-scoped route family | Obvious | Two documents for one repository (rejected in Q-AUTHZ-184) |

**Choice:** (a). The summary of the list names the project for every caller who sees the row, because the project filter of the UI needs it; the consumer view of a read does not. A project administrator also sees the repositories of its project that are being deleted, as a holder of the capability does, and `view=full` returns the repositories of its projects only. The endpoint's exposure ceiling binds it like every subject (R-EP-013).

**Rationale:** the member is part of what a client reads and writes, and a refused change is better than an ignored one. The development agent would revisit the summary rule if an installation treats project keys as confidential.

**Applied in the spec:** R-REPO-004 (the member), R-REPO-010 and R-REPO-012. Code: `internal/api/v1/repositories_api.go`, `internal/repository/project.go`.

### Q-REPO-126 · A project administrator's create names its project, and the refusals answer with their own code

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by [Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared) and [Q-AUTHZ-209](authorization.md#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)**: no key carries a prefix, so `project_prefix_required` and `unknown_project` go; the members and the deploy target are bare keys of the project and an unknown one is `invalid_member` (`invalid_deploy_target`).

Context: Q-AUTHZ-176 and Q-AUTHZ-178 fix the prefix and the virtual rule. Open: how the violations are reported, what a caller that only administers a project must send, and whether the prefix applies to a holder of `repositories:write-config`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Field errors of the validation pass, the code of the rule at the top as well; the prefix binds a project administrator only (chosen) | `project_prefix_required` at `/key`, `unknown_project` at `/project`, `repository_outside_project` at the member or the deploy target, all with `errors[]`; the top-level `code` is `project_prefix_required` or `repository_outside_project` when one of them is among the violations; a caller that is only a project administrator and sends no `project` gets 403 `forbidden`; an administrator of trove assigns any project with any key | Every violation of a document in one answer (R-REPO-008); the acceptance of R-AUTHZ-026 reads its code at the top | A second top-level code beside `reserved_name` |
| (b) Separate errors before validation | The first violation answers | Simple | A client fixes one problem per request |

**Choice:** (a).

**Rationale:** R-REPO-008 asks for every violation at once, and the contract names the codes. The development agent records no revisit condition.

**Applied in the spec:** R-REPO-008, R-REPO-009. Code: `internal/repository/validate.go`, `internal/repository/project.go`, `internal/repository/members.go`.

### Q-REPO-127 · A project administrator gives an upstream secret as a value, and a stored one stays with its hosts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a project administrator with `manageRepositories` creates and changes the project's remote repositories and tests
their upstream, saved and unsaved (Q-AUTHZ-173, Q-AUTHZ-184). The security audit of phase 5a found two ways to send a secret
that is not the project administrator's to a host it chooses: (1) a `secretRef` names any file of `config_as_code.secrets_dir`
or any `TROVE_CAC_` variable, and the references are visible in the full documents of its repositories, so an unsaved test or
a write with its own url sends a partner's token there; (2) a write that changes `url` of a repository an administrator of
trove assigned to the project keeps the sealed password (`{"set": true}`), and the next fetch or the saved test sends it to the
new origin, as an `extraHosts` entry with `sendCredentials` or `forwardAuthOnRedirect` does.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Values only, and no carry-over across a change of hosts (chosen) | A project administrator's write and unsaved test refuse a `secretRef` (422 `secret_value_required` at `/remote/password/secretRef`), except the reference the repository holds, sent back unchanged; a write that changes `url`, adds a `sendCredentials` pattern or turns `forwardAuthOnRedirect` on keeps no stored credential, sealed or referenced (422 at `/remote/password`): it gives a new value or none | The project administrator sends only what it knows; a document read and sent back still saves; the code is one comparison with the stored section under the row lock | A team whose upstream token an administrator stored asks the administrator, or enters the token itself, to move the url |
| (b) An allow list of references per project | The installation names the references each project may use | Teams can use mounted secrets | A configuration surface and its validation for a rare case; the carry-over remains |
| (c) Keep both and document them (H-281 for delegates) | Mitigation by mounting only what delegates may use | No change | A delegate exfiltrates every mounted partner token and every stored credential of its assigned repositories |
| (d) Refuse any url change of a repository with a stored credential | The url is fixed while a credential is stored | Simplest rule | The delegate cannot even move to a new path of the same upstream with its own new value |

**Choice:** (a). The comparison is with the canonical stored section: any change of `url` counts (a path on the same origin
included, because a reverse proxy can route paths to different operators), a `sendCredentials` pattern counts unless the same
pattern was stored with `sendCredentials`, and `forwardAuthOnRedirect` counts when it turns on; narrowing never needs a value.
A kept credential is refused whatever `authMode` says, so a later change of the mode cannot send it either. The rule binds the
project administrator only (`Write.Delegate`); a holder of `repositories:write-config` keeps references (H-281).

**Rationale:** a reference and a stored credential are the installation's, given to one upstream by an administrator; a
delegate may use them there and nowhere else. The development agent revisits (b) when installations ask for mounted secrets
per project, and the rule when R-REPO-034 and R-REPO-036 add named client certificates and sensitive headers, which are
references of the same kind ([H-403](../security/delegated-administration.md#h-403--what-a-project-administrator-still-reaches-of-an-upstream-an-administrator-configured)).

**Applied in the spec:** R-REPO-008 (code), R-REPO-024 (the tests), R-REPO-033 (the rule, an acceptance criterion). Code:
`internal/repository/delegated.go` (`checkDelegatedRemote`), `Service.CreateTxIn`, `Service.update`,
`Service.UpstreamTestUnsavedIn`; `internal/api/v1/upstream_api.go`. Gap:
[H-403](../security/delegated-administration.md#h-403--what-a-project-administrator-still-reaches-of-an-upstream-an-administrator-configured).

### Q-REPO-128 · A project administrator's remote repository takes the installation's outbound path

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `outboundProxy.mode: direct` bypasses the proxy that `default` resolves (`proxy.outbound.proxy_from_environment`, later
the named default of R-REPO-037), and `sourceNetwork` binds the upstream connections to an address of the pods' interfaces, for
example a secondary network to a partner's VPN whose firewall trusts that source (Q-REPO-9). Both are how the installation reaches
the network, and a partner that trusts a source address grants on it. Open: may a project administrator set them?

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Defaults for a project administrator; stored values stay with their url and extra hosts (chosen) | A project administrator's create and unsaved test take `outboundProxy.mode: default` and `sourceNetwork: null` (422 `outbound_path_restricted` at the member otherwise); its write keeps the stored values while `url` and the `extraHosts` patterns stay, and sets the defaults at any time | The outbound proxy keeps seeing every delegate's traffic; a trusted source address goes only to the upstream an administrator chose; a document read and sent back still saves | A team that needs a partner's network asks an administrator |
| (b) Allow both | The address rules apply at connect time either way | No rule | A delegate bypasses the organisation's proxy, or points a trusted source address at any host of a partner's network |
| (c) Refuse `direct` only while a proxy is configured | Depends on the configuration | Exact | A rule that changes meaning with a setting; `sourceNetwork` stays open |

**Choice:** (a). A new extra host counts as a new destination for these members, because fetches follow URLs of the upstream's
metadata to it from the same source address and path.

**Rationale:** the path to the network is installation configuration, like the address rules; the delegate's upstream gets the
path every other delegate's gets. The development agent revisits it when named proxies (R-REPO-037) give a project a proxy of its
own to choose.

**Applied in the spec:** R-REPO-008 (code), R-REPO-024 (the unsaved test), R-REPO-037 (the mode). Code:
`internal/repository/delegated.go`.

### Q-REPO-129 · A project administrator's delete leaves virtual repositories outside the project alone

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project) and [Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)**: only virtual repositories of the same project can list a repository, so a delete's `detach` never meets one outside the project.

Context: R-REPO-013's `detach=true` removes the repository from every virtual repository that lists it, and the 409 without it
names them. For a project administrator that edits virtual repositories outside its project (an administrator of trove may list
a project repository in a shared virtual repository) and names their keys, which R-AUTHZ-026 keeps from it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse while a virtual repository outside the project lists it, naming only the project's own (chosen) | 409 `repository_referenced` with or without `detach`; the detail says that virtual repositories outside the project list it, without keys, and names the project's referencing virtual repositories; with only the project's own, `detach` works as for anyone | No edit and no key outside the project; the shared composition stays an administrator's | The project administrator learns that some repository outside the project lists one of its repositories |
| (b) Detach from the project's virtual repositories only and delete | The others keep a member that is being deleted | The delete succeeds | A shared virtual repository silently loses a member when the job finishes |
| (c) The same answer for every caller | Unchanged | Simple | Edits outside the project and names foreign keys |

**Choice:** (a).

**Rationale:** the fact that some foreign reference exists is the least a refusal can say, and the administrator who placed it
decides. The development agent revisits it with repository sharing between projects (P2).

**Applied in the spec:** R-REPO-013 (the bullet, an acceptance criterion). Code: `internal/repository/members.go`
(`detachReferences`, `refuseForeignReferences`).

### Q-REPO-130 · The mirror is a mode of the remote repository, set in its document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner's request of 2026-11-02 keeps pull-through as the default of a remote repository and lets a remote be set
to full mirror. The catalog's pull replication (API-082, API-083) configures it as a document of its own per target.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The members `remote.mode` and `remote.mirror` of the repository document (chosen) | Read, changed, versioned, audited and declared as code with the rest of the remote | One `ETag`, one audit event, one configuration-as-code object; the upstream, credentials and patterns are the remote's anyway | The document grows by nine members |
| (b) A replication resource per repository (`/repositories/{key}/replication`) | The catalog's shape | Familiar to users of the catalog's product family | Two documents, two revisions and two audit trails describe one upstream; configuration as code needs a second object kind |
| (c) A repository class `mirror` | A fourth class | Visible in every list | INV-5 has three classes; a mirror is a remote with background passes, and every remote rule would have to name two classes |

**Choice:** (a). A stored section written before the members existed is read with them at their defaults
(`parseStoredRemote` applies the defaults before it decodes), so no migration rewrites `repositories.config` and an earlier
replica, which decodes the stored section without them, reads a document the new release wrote: `json.Unmarshal` ignores the
members it does not know. Between the contract wave and the engine's work package the mode is accepted and stored; the
status and run operations of R-REPO-056 do not exist until their work package lands, and the manual says that no pass runs
yet, so that nobody reads an inert mirror as running.

**Rationale:** the mode changes what a remote does, not which upstream it has; everything a mirror needs to know is already
in the remote's document. The development agent would revisit it if a remote ever needs several mirror targets (it cannot:
one upstream per remote, INV-5).

**Applied in the spec:** R-REPO-024 (the opening sentence and the table rows), R-REPO-050; [ADR-151](../decisions.md#adr-151--a-remote-repository-can-mirror-its-upstream-in-the-background).
Code: `internal/repository/remote.go` (`RemoteSettings.Mode`, `RemoteSettings.Mirror`, `parseStoredRemote`),
`internal/repository/validate_mirror.go`.

### Q-REPO-131 · Passes follow an interval per repository, enqueued by one dispatch task

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `intervalSeconds` per repository; one task `mirror-dispatch` every minute enqueues the due passes (chosen) | `next_pass_at` = start of the pass plus the interval | Passes spread out by their own start times; a pass that outlives its interval is not started twice; one registered task | Up to a minute between due time and start |
| (b) A cron expression per repository | The catalog's form | Exact times | The scheduler's tasks are registered at start (R-ARCH-016), so per-repository cron needs a scheduler of its own; mirrors with the same expression start in the same minute |
| (c) One long-running job per repository that sleeps between passes | No dispatch | Simple loop | Holds a job slot for ever; a drain must release a sleeping job; more mirrors than slots starve |

**Choice:** (a), the task in the queue `mirror` beside the passes, so that a long garbage collection in `maintenance` never
delays a dispatch.

**Rationale:** the start of the previous pass is the natural anchor for a background sync, and a minute of lag is nothing
against an hourly default. The development agent would revisit it if operators ask for passes at fixed wall-clock times.

**Applied in the spec:** R-REPO-050 (`intervalSeconds`), R-REPO-053 (dispatch); [architecture.md](architecture.md) R-ARCH-016
(the kinds and the queue).

### Q-REPO-132 · A mirror of a format's well-known upstream needs a bound

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a mirror of the public npm registry or of nuget.org without a bound would download terabytes before anyone notices.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept every mirror; rely on quotas | No rule | Simplest | Quotas are P1 (R-STOR-029); an unbounded mirror fills the object store |
| (b) Require include patterns for every mirror | Always a name bound | Clear | A mirror of a private upstream ("everything of our partner's feed") needs no name bound |
| (c) For the format's well-known upstream: an include list whose every pattern has a literal prefix, or `maxBytes`; elsewhere a warning (chosen) | 422 `mirror_unbounded` or the warning `mirror_unbounded` | Stops the expensive mistake where it is likely; leaves private upstreams to the operator; a deterministic rule on stored configuration | An internal proxy of the public registry under another URL is not caught (the warning is) |
| (d) Require both patterns and `maxBytes` | Double bound | Safest | Forces a byte number on a bounded name set where none is needed |

**Choice:** (c). A pattern counts as a bound when it has a literal part before its first wildcard: `*`, `**`, `@*/*` and `?x`
do not; one such pattern in the list makes the list unbounded.

**Rationale:** the public registries are the case that is both likely and ruinous; a byte bound or a prefix keeps the mirror
to what the operator meant. The development agent would revisit it when quotas are built (the system capacity guard of
R-REPO-052 then bounds every mirror) or if installations mirror public registries under other URLs.

**Applied in the spec:** R-REPO-050, R-REPO-052. Code: `internal/repository/validate_mirror.go` (`checkMirrorBound`).

### Q-REPO-133 · The byte bound counts the repository's logical bytes, and reaching it stops additions, not clients

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The folded `repository_usage.logical_bytes`, the quota's number (chosen) | One primary-key read per artifact; what clients filled counts too | The same number as R-STOR-029; no counter of its own; correct across passes, restores and deletes | Lags by one fold interval |
| (b) A counter of the bytes the mirror stored | Counted by the pass | Exact for the mirror's own share | Diverges from the repository's real size after deletes, restores and client fills |
| (c) Physical bytes after deduplication | The object store's view | What the bucket costs | Not per repository; a blob shared with another repository would count twice or never |

**Choice:** (a). Reaching the bound ends the pass with `bound_reached`; later passes end at once while it holds; client
fills are not bounded by it.

**Rationale:** an operator who sets 100 GiB means the repository's size; refusing clients would turn a background bound into
an outage. The development agent would revisit it if the fold lag lets mirrors overshoot measurably (H-505).

**Applied in the spec:** R-REPO-052.

### Q-REPO-134 · The versions a mirror stores: the newest n by the format's order, prereleases optional

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) All versions | No member | A full copy | Packages with thousands of versions (nightly builds) dominate the storage |
| (b) `maxVersionsPerPackage` and `includePrereleases`, with versions the format pins (chosen) | Newest n after the prerelease filter, by the format's version order | Bounded per package; one generic rule; the format keeps `latest` reachable | A build that needs an older version fetches it by pull-through |
| (c) Version ranges per package | `lodash@^4` | Precise | A configuration language per package; ranges differ per format |
| (d) Published after a date | `since` | Cuts history | npm's and NuGet's published times differ in meaning (NuGet sets 1900 for unlisted versions) |

**Choice:** (b), with `0` for all versions as the default.

**Rationale:** the newest versions are what builds resolve; older ones remain reachable through the same remote. The
development agent would revisit (c) if installations need ranges per package.

**Applied in the spec:** R-REPO-050, R-REPO-051; [formats/npm.md](formats/npm.md) Q-NPM-71.

### Q-REPO-135 · A pass keeps its discovered names in a pending table and fills them package by package

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job checkpoint holds the feed cursor; a page is filled before the cursor moves | The vulnerability sync's form (R-VULN-004) | No table | npm's feed pages hold up to 10,000 changes: a drain repeats hours of work; the checkpoint dies with the job, so a long rate-limit wait cannot end the job |
| (b) A table `mirror_pending` of discovered names and the pass's state in `mirror_state`, both outside the job (chosen) | Discovery appends names and moves the cursor in one transaction; the fill deletes a name when its package is stored | Resumable per package; the pass outlives a job (rate-limit waits, failures, crashes); bounded by pausing discovery at 10,000 pending names | Two tables; one statement per package |
| (c) One job per package | The job queue as the work list | Parallel across processes | Millions of jobs for a large mirror; the queue's own tables become the bottleneck; concurrency per upstream is lost |

**Choice:** (b). The pass is a durable object of its repository; jobs only carry it forward.

**Rationale:** a mirror pass can take days, and nothing that takes days may depend on one job living. The development agent
would revisit it if the pending table ever holds more than the bounded 10,000 rows per repository.

**Applied in the spec:** R-REPO-053 (steps and data model).

### Q-REPO-136 · A failed pass starts again on its own after a bounded back-off; a failing package waits for the next pass

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job framework's retries (`MaxAttempts` > 1) | Immediate re-runs | No code | The framework retries within seconds; a broken upstream is hit again at once; a lost lease and a failure count alike |
| (b) `MaxAttempts: 1`; the dispatch re-enqueues after `min(60 s × 2^(f−1), 6 h)` ±20 %; a package three attempts, then the next pass (chosen) | Back-off in `mirror_state` | Bounded, spread, survives restarts; one failing package never blocks the rest | A pass with a permanently broken package always ends `partial` |
| (c) Stop the mirror after n failures until an operator acts | Fail closed | Loud | The owner asked for a sync that starts again on its own |

**Choice:** (b).

**Rationale:** the upstream decides when it recovers; a capped exponential wait finds that moment without hammering it. The
development agent would revisit the cap of 6 h if mirrors of flaky upstreams stay behind for a day.

**Applied in the spec:** R-REPO-053 (errors), R-REPO-056 (`state: failing`).

### Q-REPO-137 · What counts as a rate-limit answer, and how long the mirror waits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `429` only | R-REPO-029's rule | Simple | S3-backed upstreams answer `503 SlowDown`, and some registries answer `403` with an exhausted quota header |
| (b) `429`; `503` with `Retry-After` or the S3 code `SlowDown`; `403`/`429`/`503` with a remaining count of 0; `Retry-After` in both forms, capped at 6 h; otherwise exponential back-off with jitter in `[d/2, d]` up to 300 s (chosen) | The answers that registries and object stores send | Honours every common signal; the jitter spreads retries; the cap keeps a wrong header from parking a mirror for days | The body of a `503` is read up to 4 KiB |
| (c) Also every `5xx` | Treat any error as throttling | Gentle | A broken upstream would slow the whole pass instead of failing an item |

**Choice:** (b). A wait longer than 300 s ends the job (`deferred`); the dispatch continues the pass when the wait is over.

**Rationale:** the mirror is the one client of the upstream that can always wait, so it waits as long as it is told. The
development agent would revisit the classification when an upstream in use signals throttling in another way.

**Applied in the spec:** R-REPO-054. Code: `internal/proxy/upstream/ratelimit.go` (`RateLimitOf`, `RateLimit`).

### Q-REPO-138 · The mirror's concurrency adapts: slow start, additive increase, halving on a rate limit

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A fixed `concurrency` | No adaptation | Predictable | After a rate limit the pass comes back at full width and is limited again |
| (b) AIMD with slow start: start at 1, double per window of successes up to the ceiling or the first rate limit, then +1 per window, halve once per wait (chosen) | TCP's congestion rule ([RFC 5681](https://www.rfc-editor.org/rfc/rfc5681)) | Converges below the upstream's limit and probes upward slowly; one halving per wait, not per answer in flight | A pass starts slowly after every restart |
| (c) A token-bucket rate learned from the answers | Requests per second | Matches rate limits that count requests | Needs a rate model per upstream; concurrency is what the budget of R-REPO-040 bounds |

**Choice:** (b), per pass, in the process that runs it; the ceiling is `concurrency` and the background share of R-REPO-055.

**Rationale:** a window that halves on a signal and grows slowly is the well-understood way to share a resource with unknown
capacity. The development agent would revisit (c) if upstreams in use limit by request rate rather than concurrency.

**Applied in the spec:** R-REPO-054.

### Q-REPO-139 · The mirror's requests neither move nor reset the circuit, and an open circuit pauses the mirror

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-029's circuit is shared by all replicas; a `429` opens it at once for its `Retry-After`. A mirror that
provokes rate limits would then turn clients away.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Background outcomes count as client outcomes do | One rule | Simple | A throttled mirror closes the upstream for every client: the background starves the foreground |
| (b) They neither count nor reset; the mirror waits while the circuit is open and never probes (chosen) | The mirror has its own back-off (R-REPO-054) | Clients see only their own failures; a dead upstream that clients found stops the mirror too | A mirror never helps detect an outage |
| (c) Count failures but not rate limits | Half way | Detects outages sooner | Errors of packages nobody asked for (one broken document) would open the circuit for clients |

**Choice:** (b).

**Rationale:** the circuit protects clients from waiting on an upstream that is down; only client outcomes say that. A
success of the mirror resetting it would hide a client outage just as badly. The development agent would revisit it if
mirrors of upstreams without client traffic need outage detection (the status's `failing` covers it today).

**Applied in the spec:** R-REPO-029 (a bullet), R-REPO-054.

### Q-REPO-140 · The background share, and how a client fill pre-empts it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the passes run in whatever process claims the queue `mirror` (R-REPO-053); client fills run in the client-serving
replicas. The budget `limits.maxConcurrentFetches` is shared among the client-serving replicas without coordination
(Q-REPO-8); the process that runs a pass need not be one of them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Reserve a part of the budget for the background and shrink the client shares | `ceil((budget − B) ÷ n)` per `web` replica | Exact budget | Clients get less whenever a mirror exists, the opposite of clients first |
| (b) A background share B = `min(concurrency, max(1, floor(budget ÷ 4)))` on top of the unchanged client shares; pre-emption in the process, and across replicas through the signal `mirror_yield` (chosen) | Bound `budget + n − 1 + B`, back to `budget + n − 1` while clients press | Clients are never slowed by the mirror; the bound stays fixed and small; without Valkey only the early yield is lost | The upstream may see up to B more connections than the budget while clients are idle |
| (c) A cluster-wide semaphore in Valkey | Exact sharing | Exact | Q-REPO-8 (c)'s objections: a Valkey round trip per fetch and slots leaked by killed holders |
| (d) The pass asks a client-serving replica to fetch for it | Background through the client path | One accounting | The background then occupies client slots, which is what has to be avoided |

**Choice:** (b). A pre-empted fetch is cancelled, stores nothing, releases its flight with `failed`, stays in the pass and is
not a failure; a fetch that a follower joined is not pre-empted.

**Rationale:** "clients first" means the background yields capacity, not that clients pay for it; a quarter of the budget is
enough for a background sync and small enough for the upstream. The development agent would revisit the quarter if passes
are too slow at the default budget of 64, or if upstreams complain at `budget + B`.

**Applied in the spec:** R-REPO-040 (a bullet), R-REPO-055; [architecture.md](architecture.md) R-ARCH-018 (the signal area),
Q-ARCH-286. Code: `internal/proxy/types.go` (`Priority`, `BackgroundShare`), `internal/proxy/slots.go` (`acquireBackground`; the pre-emption is the engine's work package).

### Q-REPO-141 · Pause is a member of the document; a pass on request is an operation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `remote.mirror.paused` in the document, and `POST …/mirror/run` (chosen) | Pause is configuration; a run is an action | A pause survives restarts and restores, is audited, `If-Match`-guarded and declared as code; a run is a one-off with a job | Pausing needs the document's revision |
| (b) `POST …/mirror/pause` and `…/resume` with state in `mirror_state` | Actions | No revision needed | State outside the document: lost in a restore of a transient table, invisible to configuration as code |
| (c) No operations: only the mode | Off is `pullThrough` | Fewest operations | Turning a mirror off and on loses nothing, but a pause during an incident should not look like a change of mode |

**Choice:** (a).

**Rationale:** whether a mirror runs is a decision about the repository and belongs where its other decisions are. The
development agent would revisit (b) if pausing from scripts without the revision becomes common.

**Applied in the spec:** R-REPO-050, R-REPO-056.

### Q-REPO-142 · Who may switch a remote to mirror

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a cache fill needs the populate right of R-AUTHZ-008, and a mirror fills without a client, for every name its
patterns admit. A project administrator with `manageRepositories` creates and changes the remote repositories of its
project (R-AUTHZ-026); Q-REPO-127 and Q-REPO-128 keep installation-level paths from it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Whoever may update the remote | The update right | No rule | A delegate decides how much of a public registry the installation downloads and stores |
| (b) `repositories:write-config` sets the mode and the mirror members; a project administrator keeps what is stored, changes `paused` and turns the mode back to `pullThrough` (chosen) | 422 `mirror_restricted` for any other change by a project administrator | Volume and egress stay an installation decision; a delegate can still stop or pause what costs | A team that wants a mirror asks an administrator |
| (c) A per-project allowance with a byte budget | Delegated mirrors within a budget | Self-service | A budget model for one feature; quotas per project do not exist |

**Choice:** (b). The comparison is with the canonical stored section under the row lock, as in Q-REPO-127: a document read and
sent back saves.

**Rationale:** the populate right decides who may make trove fetch one package; a mirror decides on all of them, so it takes
the installation's configuration right. The development agent would revisit (c) when projects get quotas.

**Applied in the spec:** R-REPO-050, the security notes; [authorization.md](authorization.md) R-AUTHZ-026 (a bullet). Code:
`internal/repository/delegated.go` (`checkDelegatedMirror`).

### Q-REPO-143 · Which pull-replication entries of the catalog the mirror pulls forward

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: API-082 to API-090 describe replication between instances: push from local repositories, pull into remote caches,
event channels, global blocks and checksum-only transfer, all P2.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) None: the mirror is trove-only | Nothing changes in the catalog | No commitment | The mirror does what API-082 to API-085 describe for pull, so it would be specified twice later |
| (b) Pull forward what a pull mirror is: the configuration document (API-082), its read and change (API-083), the status (API-084) and the run (API-085); leave push, bulk switches, peer channels, the global block and checksum replication (chosen) | Four entries become MVP | The beta's mirror covers the catalog's pull shape; the rest keeps its own design | API-082 and API-083 are covered in the repository document's form, a deliberate deviation |
| (c) All nine | Replication complete | One topic closed | Push, peer channels and checksum replication need trove-to-trove chaining (RMT-040) and storage-level replication, which the beta does not have |

**Choice:** (b). API-086 (multi-push), API-087 (bulk switches by target URL), API-088 (peer event channels), API-089 (global
block; the global offline switch stops every mirror, `paused` one) and API-090 (checksum-only) stay P2, in the page's
Deferred table with their reasons.

**Rationale:** the beta needs pull; everything left needs a second trove instance as a peer. The development agent would
revisit API-089 if installations run so many mirrors that pausing them one by one is a burden.

**Applied in the spec:** R-REPO-050 and R-REPO-056 (`Covers:`), Deferred, Deliberate deviations; the catalog priorities of
API-082 to API-085 in [api-content-operations.md](../features/api-content-operations.md).

### Q-REPO-144 · The mirror deletes nothing that the upstream removed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Delete what the upstream deleted (the catalog's "sync deletes") | A copy that follows deletions | Mirror equals upstream | A takedown upstream destroys what builds pinned; a compromised upstream can wipe the mirror |
| (b) Keep the items; the removal reaches clients through the refreshed document (chosen) | R-REPO-029's gone packages, npm's removal marks | A withdrawn version stops being served as for any remote; an operator can still copy it to a local repository | Storage of removed versions stays until an operator deletes it |

**Choice:** (b).

**Rationale:** R-REPO-029 already decides what a takedown means for a remote; a mirror must not decide it differently. The
development agent would revisit it when the remote-cache cleanup of R-STOR-045 is built, which could remove gone content
after a period.

**Applied in the spec:** R-REPO-051, Deliberate deviations.

### Q-REPO-145 · A mirror's fills are audited as cache fills, with the system as actor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One populate event per stored file, as for client fills, actor `system`, `reason: mirror` (chosen) | R-AUTHZ-008's event in the fill transaction | The same trail for every byte that entered the cache; a fill without its event does not commit | A full pass of a large mirror writes as many events as files |
| (b) One summary event per package | Fewer rows | Less audit volume | A stored file without an event of its own; differs from client fills |
| (c) Only pass events | Least volume | Cheap | The trail no longer says how a file entered the cache |

**Choice:** (a), with the pass events of R-REPO-057 on top.

**Rationale:** the owner asked for the mirror's fills to be audited like cache fills, and the volume is the volume of files a
pass stores, which the bounds limit. The development agent would revisit (b) if audit retention becomes a cost problem for
large mirrors.

**Applied in the spec:** R-REPO-057.

### Q-REPO-146 · A pass fills through the remote engine with a system caller

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Through R-REPO-025 steps 4 to 10 and the format's artifact gate, with a populate check that allows and records as the system (chosen) | The mirror is one more caller of the engine | Verification, size limits, content-type rules, URL maps, flights, removal marks and the vulnerability gate apply unchanged; collapsing with clients | Each item pays the engine's statements |
| (b) A bulk importer that writes entries and items directly | Faster | Fewer statements | A second fill path that has to repeat every rule of the engine and of the formats |

**Choice:** (a). The administrator's decision to mirror is the populate decision; `cacheFillAction` applies to clients.

**Rationale:** one fill path keeps every guarantee of this page true for mirrored content. The development agent would revisit
it if a pass's statement count dominates PostgreSQL ([architecture.md](architecture.md) R-ARCH-027).

**Applied in the spec:** R-REPO-053 (step 3), R-REPO-057.

### Q-REPO-147 · The mirror's state survives a restore, except the pass in progress

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Both tables transient | Start over after a restore | Nothing to reconcile | Every mirror runs a full pass after a restore, which for a feed means reading it from its beginning |
| (b) `mirror_state` data, `mirror_pending` transient; the pass in progress cleared; the cursor of the last complete pass kept, or cleared without remote caches (chosen) | The snapshot's cursor and cache belong together | The first pass after a restore is incremental from a consistent point | A restore rule of its own |

**Choice:** (b).

**Rationale:** the backup is transactionally consistent, so the cursor and the cache it describes are restored together; the
pending names of an interrupted pass are rediscovered from that cursor. The development agent would revisit it if the restore
job (phase 7) cannot apply a per-table rule.

**Applied in the spec:** R-REPO-058; [backup-and-restore.md](backup-and-restore.md) R-BAK-004 (the classes).

### Q-REPO-148 · Turning a mirror back to pull-through keeps what it stored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep content, the last complete pass and its cursor; clear the pass in progress (chosen) | A mode switch is cheap both ways | Turning the mode on again continues incrementally; nothing a build uses disappears | Content the mirror stored stays until cleanup or deletion |
| (b) Delete the mirrored content that no client used | Give the space back | Smaller | Destroys what builds may pin; "what no client used" is not tracked per item before R-STOR-045 |

**Choice:** (a).

**Rationale:** a mode switch must never be a deletion. The development agent would revisit (b) with the remote-cache cleanup of
R-STOR-045.

**Applied in the spec:** R-REPO-050, R-REPO-053 (stops).

### Q-REPO-149 · A background request's own outcomes stay with the request that led the flight

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a background request (R-REPO-055) shares the flights of R-REPO-026 with clients: a client that asks for an item a pass is
fetching joins the pass's flight as a follower, and the result of a flight is what every follower answers with. The outcomes only a
pass understands (pre-empted, the circuit is open, the spool refused admission, the upstream rate-limited it) must not reach a
client as its answer.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The outcomes travel in a field of the flight's result that only the leading background request reads (`flightResult.own`); to everybody else the result is empty and they fetch for themselves (chosen) | A pre-empted or throttled flight releases with `failed`, and a follower looks again and fetches with its own classification | A client is never handed a rate-limit or a pre-emption it did not cause; the existing rule for an empty result applies unchanged | One more field of the result |
| (b) The outcomes are errors of the flight like any other | Followers answer with them | No new field | A client gets a `*BackgroundWait` or `*upstream.RateLimitError` that is no problem answer: `500` for a pre-emption it only waited behind |
| (c) A background request never leads a flight | The pass joins or fetches outside any flight | No sharing problem | A client that asks for the item the pass is fetching fetches a second time (R-REPO-055: it joins as a follower) |

**Choice:** (a). A background request that joins a client's flight gets that flight's result as it is, with the client
classification of R-REPO-029; the pass treats it as a failure of the item.

**Rationale:** the flight is the cluster's one fetch of an item (R-REPO-026); keeping the pass's private outcomes out of it is what
lets a pre-emption release the flight with `failed` at once so that a waiter fetches for itself. The development agent would
revisit the joined case if passes are seen to lose rate-limit hints that way: a client's flight that met a `429` opens the circuit
at once, and the pass then waits on the open circuit instead.

**Applied in the spec:** R-REPO-054, R-REPO-055. Code: [internal/proxy/background.go](../../internal/proxy/background.go) (`resultOf`,
`backgroundOutcome`), [flight.go](../../internal/proxy/flight.go) (`flightResult.own`), [fetch.go](../../internal/proxy/fetch.go).

### Q-REPO-150 · An open circuit pauses a pass; a half-open one does not

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-054 says the pass sends nothing while the circuit is open and never probes. R-REPO-029 has a third state: after the
open period the circuit is half-open, and the next client request is the probe.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A background request waits while the circuit is open (`open_until` in the future, or another request of the replica holds the probe) and goes on when the period has passed, without claiming the probe and without resetting anything (chosen) | The wait is `*proxy.BackgroundWait{circuit_open}`, until the circuit's next state change, at least 1 s and at most 5 s | A pass resumes by itself on an installation with no client traffic; it never takes the clients' probe | While the upstream is still down, one background request per item fails after the period (a failure of the item, not counted) |
| (b) It waits until a client's request has closed the circuit | Half-open counts as open | The pass never sends to an upstream a client has not found alive | A mirror of an installation without client traffic never resumes after one outage |
| (c) It probes like a client | Its success closes the circuit | Self-healing | It moves the circuit: R-REPO-054 forbids it |

**Choice:** (a). The cap of 5 s keeps the pass looking again often enough to notice a client's success that closed the circuit early.

**Rationale:** the circuit belongs to the clients and the pass only reads it; a pass that waited for a probe nobody sends would
stall for good. The development agent would revisit it if mirrors of idle installations hammer a dead upstream: the item failures
back off with the pass's attempts (R-REPO-053).

**Applied in the spec:** R-REPO-054. Code: [internal/proxy/background.go](../../internal/proxy/background.go) (`circuitWait`),
[fetch.go](../../internal/proxy/fetch.go) (`run`, `inSlot`), [fill.go](../../internal/proxy/fill.go), [get.go](../../internal/proxy/get.go).

### Q-REPO-151 · A discovery request reaches the upstream, the extraHosts entries and the hosts the lister names, and the last never get a credential

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `discover.Upstream.Get` may reach a host that is neither the upstream nor an `extraHosts` entry: npm's replication host of
the public registry (Q-NPM-72). The credential of a repository goes only where it goes for clients (R-REPO-033), and a host the
lister names is not one of them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host check is `Client.AllowedMirror`; a request to a lister's host carries a mark in its context that makes the credential policy send no credential, no custom header and no client certificate on any hop, also where an `extraHosts` entry with `sendCredentials` matches the host (chosen) | The rule is the policy's, once per hop | Holds for redirects; independent of the settings of the repository | A small addition to the hop policy |
| (b) The client sends what the policy says; the lister's hosts must not match `sendCredentials` | Configuration decides | No code | An operator's `*.npmjs.com` entry with `sendCredentials` would send the registry's token to the replication host |
| (c) A second client without a credential for the lister's hosts | A pool of its own per repository | Isolated | Another pool and cache per remote, and the egress and redirect rules to keep equal |

**Choice:** (a). A host of the lister is matched by host name, any port, case-insensitively; the upstream's own host is never one;
a lister's host over `http` while the upstream is `https` is refused like any other hop of that kind.

**Rationale:** "never" is a property of the request, not of a setting somebody can change later. The development agent would revisit
(c) if a format needs headers of its own toward its discovery host.

**Applied in the spec:** R-REPO-051 (the `Upstream` contract is unchanged), [security/egress.md](../security/egress.md). Code:
[internal/proxy/upstream/client.go](../../internal/proxy/upstream/client.go) (`AllowedMirror`),
[internal/outbound/hop.go](../../internal/outbound/hop.go) (`WithoutCredentials`), [internal/proxy/get.go](../../internal/proxy/get.go).

### Q-REPO-152 · The body of a discovery request that names no bound is read up to 32 MiB

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `Upstream.Get(ctx, u, accept, max)` has `max` 0 for "the engine's bound for discovery documents" (R-REPO-051). The
largest page a format reads is an npm key range of 10,000 names or a NuGet search of 1,000 packages with their versions.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 32 MiB, `proxy.DiscoveryDocumentMaxBytes`, a constant (chosen) | A body above it is `502 upstream_item_too_large` | Room for the largest page seen and a stop for an endless stream | Not tunable |
| (b) The repository's `limits.maxMetadataBytes` | The bound of a metadata document | One knob | A repository tuned for package documents (a few MiB) would refuse a search page |
| (c) 8 MiB | Smaller | Less memory per call | A NuGet search of 1,000 packages with many versions can pass it |

**Choice:** (a). A caller that knows better passes its own `max`.

**Rationale:** the bound only has to stop an upstream that never ends; the format bounds what it keeps. The development agent would
make it a key if a real upstream's page passes 32 MiB.

**Applied in the spec:** R-REPO-051. Code: [internal/proxy/get.go](../../internal/proxy/get.go) (`DiscoveryDocumentMaxBytes`).

### Q-REPO-153 · A pre-emption moves the slot at once, and a waiting client keeps the background from starting

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-055 says a client fetch that finds no free slot pre-empts the youngest background fetch and "takes its slot at once",
and that a background fetch starts only while no client fetch of the repository waits.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The slot is given up at the moment of the pre-emption: the counters move under the slots' lock, the client takes the slot in the same step, and the cancelled request unwinds afterwards; the youngest is the one started last, a fetch that a follower of the replica joined is skipped, and a client that has to queue counts as waiting for its repository (chosen) | The client's wait is the lock, not the unwinding | The unit criterion (less than 50 ms) holds whatever the upstream does with the cancelled connection | For a moment one connection more than the limit is open |
| (b) The client waits until the cancelled request has released its slot | The counters follow the connections | Exact | The wait is as long as the unwinding of a transfer that is being cancelled |
| (c) Pre-empt only a fetch that has not sent its request | Cheap | No wasted transfer | Useless: the background fetches that matter are the long ones |

**Choice:** (a). The yield of another process (Q-ARCH-309) uses the same step for every background fetch of the repository that no
follower joined. A pre-emption is not counted here: the engine of the pass counts `ErrPreempted` (R-REPO-057), because it is the one
that knows the item.

**Rationale:** R-REPO-055's bound `maxConcurrentFetches + n − 1 + B` counts connections the replica opens, and an unwinding
connection is a few milliseconds long; the wait of a client is not. The development agent would revisit (a) if an upstream counts
connections strictly and the overshoot shows.

**Applied in the spec:** R-REPO-055. Code: [internal/proxy/slots.go](../../internal/proxy/slots.go) (`acquire`, `preempt`, `take`,
`acquireBackgroundJoined`).

### Q-REPO-154 · A pass's fill is Present when stored, runs the format's gate only when it fetches, and names its job in the upstream log

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `mirror.Host.Fill` stores one artifact of a plan "through the format's artifact gate and the remote engine": the gate
(`BeforeArtifact`) may revalidate the format's package document, and R-REPO-057 asks for the pass's job in the upstream log.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host asks the engine whether the artifact is stored first and answers `Present` with no gate and no request; otherwise it runs the gate (its requests, too, are background requests), then the engine's background fill; the upstream log record carries `flight_role: mirror`, `on_behalf_of: system` and `job_id` (chosen) | A pass over stored packages is a read of PostgreSQL | No upstream request for what exists; the gate's document request is the pass's | The gate's mark checks (a removed version) are not applied to what is stored: they are applied at serve time, as for every stored artifact |
| (b) Always run the gate | The marks decide first | One path | A pass over a million stored versions revalidates documents that were fresh |
| (c) Fill without the gate | The engine alone | Simple | A removed or blocked version would be stored; the digest the format announces would not be verified (R-REPO-041) |

**Choice:** (a). Offline, a negative entry, a refused populate check and a version blocked by the vulnerability scanning
(R-VULN-015) are failures of the item, not a wait: every rule of a client's fill applies, and the item is tried again by the next
pass. The populate event of the fill is written by the pass's populate check, with the system as actor and `mirror` as reason
(Q-REPO-145); `job_id` is a field of the upstream log for `purpose: remote` records of background requests only.

**Rationale:** the mirror is a client that has no client: everything it stores goes through the road a client's fill takes. The
development agent would revisit (a) if the stored-artifact check proves to be a cost of a pass over very large repositories.

**Applied in the spec:** R-REPO-055, R-REPO-057, [operations-and-observability.md](operations-and-observability.md) R-OPS-011. Code:
[internal/formathost/mirror_host.go](../../internal/formathost/mirror_host.go), [internal/proxy/fill.go](../../internal/proxy/fill.go),
[internal/outbound/roundtripper.go](../../internal/outbound/roundtripper.go).

### Q-REPO-155 · The summary of a pass is written whenever a pass attempt ends it or stops at a decision, and a pause, offline mode and a bound end the attempt differently

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The status of R-REPO-056 names five outcomes of `lastCompletePass` (`complete`, `partial`, `bound_reached`, `offline`, `stopped`), while R-REPO-053 says a pause keeps the pass and a mode change clears it. What the engine writes at each of them is not in either place.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `last_pass` is the summary of the last attempt that ended with one of the five outcomes; `complete`, `partial` and `bound_reached` clear the pass; a pause, a disabled repository and offline keep it; a mode change clears it and the pending names (chosen) | One summary shape for every end; the pass in progress survives exactly what is reversible | The status shows why the last attempt ended, a resume and a restored network continue where the pass was, a pass of hours is not thrown away by a night of offline mode | `lastCompletePass` is the last pass that *ended*, not only one that completed; a reader that wants completion reads `outcome` |
| (b) Write `last_pass` only for `complete` and `partial` | The name read literally | The summary is always a complete pass | The other three outcomes of R-REPO-056 could never be shown, and a stopped pass left no trace |
| (c) Clear the pass at every stop | No state survives a stop | Simple | A pause or an hour of offline mode would restart discovery from the beginning of the feed |

**Choice:** (a). The next pass after a pause or offline mode is due at once (`next_pass_at` = the stop) or after at most five minutes (offline, `min(intervalSeconds, 300 s)`, Q-REPO-161), and takes the fence over like after a crash. `bound_reached` advances the feed cursor only when discovery had ended; the pending names stay either way.

**Rationale:** the summary is what R-REPO-056 shows; a mode change is the one stop that deletes the pending names, because the pass is not coming back. The development agent would revisit it if D's status wants the last *complete* pass beside the last ended one.

**Applied in the spec:** R-REPO-053 (stops), R-REPO-056 (`lastCompletePass`). Code: `end.go` (`finish`, `end`). Tests: `TestR_REPO_053_APauseKeepsThePassAndAModeChangeClearsIt`, `TestR_REPO_053_OfflineEndsThePassWithoutAFailure`.

### Q-REPO-156 · The kind of a pass is decided before the claim and raised to full once the method is known

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-051 makes a pass full when the method differs from the one of the stored cursor, and R-REPO-053 makes the claim one statement. The method is known only after the upstream was asked, after the claim.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The caller decides the kind from the row (no cursor, fingerprint, interval, request) and hands it to the claim, which also reads `pass_requested_full` itself; the method raises an incremental pass to full when it is not the feed of the cursor (chosen) | The claim stays one statement; a request that arrives between the read and the claim is still honoured | No lock across an upstream request; the rule is a pure function with a table test | The kind of a pass can change once, at the start of its discovery |
| (b) Ask the upstream before the claim | The kind is final at the claim | One decision | An upstream request outside the fence; a failure then has no pass to record on |
| (c) Decide in SQL | No second step | One statement decides everything | The fingerprint, the interval and the method rule twice, in SQL and in a test double |

**Choice:** (a). `DecideKind` and `FinalKind` ([policy.go](../../internal/mirror/policy.go)) hold the rules; `SetMethod` stores the final kind, fenced.

**Rationale:** the order of the steps is the constraint. The development agent would revisit it if a method were known without a request.

**Applied in the spec:** R-REPO-051 (a pass is full), R-REPO-053 (claim). Tests: `TestR_REPO_051_APassIsFullOrIncrementalByTheRules`, `TestR_REPO_051_TheNextPassIsIncrementalAndAPatternChangeMakesItFull`, `TestR_REPO_051_AListingPassIsAlwaysFull`.

### Q-REPO-157 · A pass works with as many package workers as its ceiling, and the window counts requests

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-054 defines a window of concurrent fetches and R-REPO-053 fills package by package; a package has a plan request and one fetch per version.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `B` workers take packages in name order, each asks for its plan and fetches its versions one after the other; every request takes a slot of the window, which is at most `B` (chosen) | Parallel across packages, where upstream latency is; a package with one version still uses the window; one rule for plans, documents and tarballs | The window and the ceiling are enforced in one place (`throttle`), the workers only wait for a slot | A package with many versions is sequential |
| (b) One worker, concurrency inside a package | Fewer goroutines | Simple | The window stays below `B` for the many packages with one or two versions |
| (c) A worker per window slot, the window resizing the pool | No semaphore | Fewer idle goroutines | Resizing a pool that waits on I/O is the semaphore again, with more states |

**Choice:** (a). A name that a client pre-empted waits five seconds in memory and is retried in the same pass; a name that failed waits in its row (R-REPO-053 step 3). The pass does not end while a name waits for its retry.

**Rationale:** a package worker holds no request while it waits, so a long wait costs a goroutine and no slot. The development agent would revisit it if packages with thousands of versions dominate a pass.

**Applied in the spec:** R-REPO-053 (step 3), R-REPO-054 (the window). Code: `fill.go`, `throttle.go`. Tests: `TestR_REPO_054_SeveralWorkersStoreEveryItemOnceWithinTheCeiling`, `TestR_REPO_054_TheWindowStartsSlowHalvesOncePerWaitAndGrowsByOne`.

### Q-REPO-158 · The held package names and the exact include names of a full pass are paged through the pass cursor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-051 adds both to every full pass; a repository may hold more packages than the pending table takes (10,000, R-REPO-053 step 2), and the engine has no table of its own for the position.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The phase runs before the format's first page of a full pass; its position `{"mirrorHeldAfter": <last name>}` is stored in `pass_cursor` with the names, and the format's first request carries no cursor (chosen) | Bounded by the pending cap, resumable after a takeover, no new column | The column holds the engine's own value for the length of the phase and the format's afterwards | A takeover before the first format page repeats the phase from its last position |
| (b) Insert every held name at once | One step | Simple | A repository that holds a million packages breaks the 10,000 bound of the pending table |
| (c) After the format's last page | The cursor stays the format's | No wrapper | A name the feed missed is refreshed last, and the phase has no position either |

**Choice:** (a). The names go through the `Admits` check of the host; the exact include names are normalized by the format's name rule.

**Rationale:** one resumable mechanism for names the engine finds itself. The development agent would revisit it if the held names move into the format's plan.

**Applied in the spec:** R-REPO-051 (methods). Code: `discovery.go` (`heldStep`, `exactIncludes`). Test: `TestR_REPO_051_AFullPassAddsTheExactIncludeNamesAndTheHeldPackages`.

### Q-REPO-159 · The byte bound counts the folded counter and the pass's own stores since the counter's last fold, and a bound holds until a pass completes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-052 reads `repository_usage.logical_bytes` and adds what the pass stored since its last fold. The fold loop of R-STOR-003 is not built in this release (nothing writes `repository_usage` yet), and a bound that is cleared at the start of every pass would write `mirror.bound_reached` again whenever the counter sits just below the bound.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The pass keeps a list of (time, bytes) of its stores, adds those after `repository_usage.updated_at` (compared with the database's clock at the read), and clears `bound_reached` only when a pass ends `complete` or `partial`; a pass that finds the bound set and the counter at or above `maxBytes` ends at once (chosen) | One primary-key read per artifact; no double count after a fold; one event per transition; works before the fold exists | The tally is per process, so a restart forgets the stores of one fold interval (H-527) | The clock offset is measured, not guaranteed |
| (b) Count the unfolded deltas of `storage_usage_deltas` | Exact | No tally | The statement grows with the cluster's write rate, which R-STOR-003 refuses |
| (c) Clear the bound at the start of every pass | No stale bound | Simple | An event per pass while the repository sits at the bound |

**Choice:** (a). An artifact without an announced size is fetched and counted afterwards; an announced size that would cross the bound is left and ends the pass.

**Rationale:** R-REPO-052 asks for exactly this read. The development agent would revisit it when the fold exists and a pass can read the unfolded deltas of its own repository cheaply.

**Applied in the spec:** R-REPO-052. Code: `bounds.go`. Tests: `TestR_REPO_052_TheByteBoundStopsThePassAndHoldsUntilItIsRaised`, `TestR_REPO_052_TheBoundAddsWhatThePassStoredSinceTheLastFold`, `TestR_REPO_052_TheByteBoundEndsThePassOnceAndHoldsUntilItIsRaised`. Gap: [H-527](../security/mirror.md#h-527--the-bounds-base-is-a-lagging-counter-and-a-per-process-tally).

### Q-REPO-160 · A rate-limit wait is at least a second, only the answer that begins it counts as a wait, and a long wait is written when it begins

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-054 defines the waits, the halving once per wait and the long wait. Three points are open: an upstream that answers `Retry-After: 0`, the counters of an answer of a request that was in flight, and when `rate_limited_until` is written.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A wait is at least one second; a late answer is counted in `trove_mirror_rate_limited_total` and changes neither the wait nor the window; a wait over 300 s is written (`rate_limited_until`, `next_pass_at`) at once and stops new requests, the requests in flight finish, and the job ends with `deferred` (chosen) | No hot loop; one halving and one state write per wait; the next job is due even if this process dies at once | The in-flight requests of a deferred pass may still store content | A late answer cannot lengthen a wait |
| (b) Wait exactly what the upstream says | Literal | No floor | An upstream that keeps answering `429` with `Retry-After: 0` is asked in a tight loop |
| (c) Write the long wait when the job ends | One write at the end | Fewer writes | A process that dies between the answer and its end forgets the wait |

**Choice:** (a). `mirror.rate_limited` is written at most once per pass and hour; the hour is kept per process.

**Rationale:** the pass is the upstream's guest. The development agent would revisit the floor if an upstream asks for shorter waits legitimately.

**Applied in the spec:** R-REPO-054. Code: `throttle.go` (`rateLimited`), `request.go`. Tests: `TestR_REPO_054_ARetryAfterIsCappedAndNeverZero`, `TestR_REPO_054_ALongWaitEndsTheJobAndTheNextJobContinuesAtThatTime`.

### Q-REPO-161 · Offline mode keeps the pass and looks again after at most five minutes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-053 ends a pass offline without a failure but does not say whether the pass stays or when the next one is due.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The pass stays, the outcome is `offline`, `next_pass_at` is `min(intervalSeconds, 300 s)` away (chosen) | The end of an outage is noticed within minutes; nothing already discovered is lost | A dispatch every five minutes while offline | The status shows `outcome: offline` for the length of the outage |
| (b) The pass is cleared and the next one is due at the interval | Like a complete pass | Quiet | A feed pass of hours restarts from its beginning after a short outage |
| (c) A failure | Visible as an error | The alert fires | Offline mode is the operator's decision; it is no failure of the mirror |

**Choice:** (a). The checks of the offline mode and the global switch run when the attempt starts and before each package.

**Rationale:** offline mode is a state, not an error. The development agent would revisit the five minutes if clusters run offline for days.

**Applied in the spec:** R-REPO-053 (stops). Test: `TestR_REPO_053_OfflineEndsThePassWithoutAFailure`; integration `TestR_REPO_053_AnOfflineRepositoryEndsThePassWithoutAFailure`.

### Q-REPO-162 · The dispatch clears what a mode change left, and a restore clears through one function

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-058 says what a restore does to the state; Q-REPO-148 says a mode change clears the pass in progress. Neither names the process that does it when no pass is running, and phase 7's restore job does not exist.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every dispatch clears the pass in progress, its fence and the pending names of every remote repository of the snapshot that is not in mode mirror (a snapshot that lists nothing clears nothing); `PGStore.ClearAfterRestore(now, withoutRemoteCaches)` is the one statement of R-REPO-058, to be called by the restore job (chosen) | No pass survives a mode change whatever happened to its job; the restore rule is code with an integration test, not prose | Phase 7 has to call the function | A row of a repository that left the mode is touched once a minute until its pass is gone |
| (b) Only a running pass clears itself | Nothing in the dispatch | Simpler | A failed pass of a repository switched to pull-through would keep a stale pass until the mode is turned on again |
| (c) A restore hook in the framework | Generic | One mechanism for the vulnerability scanning too | No such mechanism exists (the restore is tested as the state it leaves, Q-VULN-58) |

**Choice:** (a). The fence is cleared with the pass, so a write of a pass that still runs matches no row.

**Rationale:** a mode change is the owner's decision and must be final. The development agent would revisit (c) when the restore job of phase 7 offers hooks.

**Applied in the spec:** R-REPO-053 (stops), R-REPO-058 (backup). Code: `store_pg.go` (`ClearInactive`, `ClearAfterRestore`). Tests: `TestR_REPO_053_TheDispatchClearsThePassOfARepositoryThatLeftTheMode`, integration `TestR_REPO_058_ARestoreClearsThePassAndKeepsOrForgetsTheCursor`.

### Q-REPO-163 · A request for a pass that the engine has not taken keeps a full flag already asked for

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the run writes `pass_requested_at`, `pass_requested_by` and `pass_requested_full` (R-REPO-053); the engine takes the request when
a pass starts. A second request can arrive before that, for example after the first one's job ended without a pass starting.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The new request overwrites the time and the caller; the flag is the new one or the old one while the old request is untaken (chosen) | `pass_requested_full = new OR (untaken AND old)` in `MirrorRequestPass` | A plain "run now" cannot take back the full pass that someone else asked for a minute ago | An operator cannot lower a request to plain; it ends when the engine takes it |
| (b) The new request overwrites all three | Last writer wins | Simple | A plain request loses a full one |
| (c) A queue of requests | One row each | Exact | A table for a flag; the pass starts once either way |

**Choice:** (a). The engine takes a request by clearing `pass_requested_at` (and the other two); a request that finds it clear starts over.
The caller recorded is the last requester.

**Rationale:** a full pass is the stronger request and costs more upstream traffic only once; losing it silently would be the
worse error. The development agent would revisit (a) if operators need to withdraw a request.

**Applied in the spec:** R-REPO-056 (the run), [mirror.md](../developer/mirror.md). Code: [mirror_api.sql](../../internal/meta/postgres/queries/mirror_api.sql).

### Q-REPO-164 · The run and the status read the repository from PostgreSQL, so a PATCH is seen at once on every replica

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the refusals of the run (`mirror_off`, `mirror_paused`, `not_applicable`) depend on `remote.mode` and `remote.mirror.paused`.
A replica's snapshot of the repositories follows a change through the change log within the epoch poll (R-ARCH-018).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the document from PostgreSQL with `repository.Service.Get` in each request (chosen) | One read per request | A resume followed by a run, or a pause followed by a run, answers as the PATCH left it, on whichever replica receives it | One more read per request of two low-volume operations |
| (b) Read the replica's snapshot | No read | Cheaper | A run right after a resume on another replica can answer 409 `mirror_paused` for one poll |

**Choice:** (a), as `GET /api/v1/repositories/{key}` does.

**Rationale:** the acceptance criterion "a PATCH with `paused: true` makes the status `paused`" is about the operator's next request, not
about the next poll. The development agent would revisit (b) if either operation became hot, which a pass's cadence makes unlikely.

**Applied in the spec:** R-REPO-056. Code: `serviceTargets.Target`.

### Q-REPO-165 · The status of a mirror that no pass has touched is `scheduled` with nulls, and a read creates no row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the dispatch creates the `mirror_state` row within a minute of the mode being set (R-REPO-053); until then, and for a remote in
mode `pullThrough` that was never a mirror, there is none.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Answer from the configuration and the usage with the members of the row at their zero: `scheduled` (or `off`, `paused`), no pass, no last pass, no times, zero counters (chosen) | `has_state` of `MirrorStatusRead`, which joins from the repository, not from the row | The wizard shows a mirror that was just switched on without an error; a read changes nothing, on a replica that may be a reader | The document does not say whether the dispatch has seen the repository |
| (b) Insert the row on the first read | Row exists | — | A read that writes; a race with the dispatch's insert |
| (c) 404 until the row exists | Honest | — | The UI would treat a new mirror as missing for up to a minute |

**Choice:** (a). The same read, for a mirror whose mode was turned back to `pullThrough`, answers `off` with what the row holds
(Q-REPO-148).

**Rationale:** the status describes the mirror the operator configured, and a minute of dispatch latency is not an error. The
development agent would revisit it if the UI needs to tell "not yet seen by the dispatch"; that would be a new member.

**Applied in the spec:** R-REPO-056. Code: `MirrorStatusRead`, `buildMirrorStatus`.

### Q-REPO-166 · `errors.lastMessage` is shown as the engine stored it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-056 says the message names hosts, never a URL or a credential (R-REPO-024's rule for the upstream test). The engine stores
`last_error` (at most 1024 characters, [migration 00040](../../internal/meta/postgres/migrations/00040_mirror.sql)) and writes the
same text into the log and the event of the failure.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Show the stored text unchanged: the engine is the one place that builds the message and keeps it free of URLs and credentials (chosen) | `last_error` as it is | One rule in one place, testable at its source; the API adds no second, different notion of "a URL" | The API relies on the engine; a message that breaks the rule reaches every reader of the status |
| (b) The API removes what looks like a URL | A filter on read | Defence in depth | A heuristic that changes messages the operator needs (`registry.example.org:8443`), and that the engine's own log and event do not have |
| (c) Show only `lastCode` | No free text | Safe by construction | The operator loses the host and the status that the message names |

**Choice:** (a). The engine's tests are the guard; the gap is recorded as
[H-530](../security/data-and-secret-flow.md#h-530--the-mirror-status-shows-the-engines-failure-text-as-stored).

**Rationale:** the rule of R-REPO-024 is kept where the text is made. The development agent would revisit (b) if the engine's text ever
includes a part of an upstream's answer.

**Applied in the spec:** R-REPO-056.

### Q-REPO-167 · A pass revalidates a stored document that was validated before its attempt began

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-051 says the mirror keeps documents current through the ordinary metadata path, "a stored document that is fresh is
used as it is". The integration suite found what that does to an incremental pass: the feed names `@acme/a` as changed, the plan asks
for its document, the document is fresh (a client or the previous pass fetched it less than `metadataTtlSeconds` ago) and is used,
so the plan lacks the new version; the cursor moves past the change, and nothing names the package again until it changes once more or
a full pass runs. The criteria of R-REPO-051 and of the npm and NuGet pages ("fetches only the document of `@acme/a` and the new
tarball", "refreshes the document of `@acme/b`") can only hold if the pass sees a document at least as new as its feed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the period: a fresh document is used as it is | The text of R-REPO-051 as it was | One rule; no extra request | A version the feed announced is missed until a full pass or another change; the criteria hold only when the period happens to have expired |
| (b) The plan's document is current only if it was validated after the attempt began (chosen) | `passUpstream.Document` asks the engine's `EnsureCurrent` with the start of the attempt as the bound, the mechanism of Q-NPM-13 | The plan always sees what the upstream had when the feed was read; one conditional request (304) per package and attempt; artifacts and client requests are unchanged | A full pass of n held packages sends n conditional requests, also for packages that did not change |
| (c) Force a refresh only for the names an incremental feed returns and for deletion records | The engine marks the name | Fewer requests on a full pass | A second path next to the period; the held names of a full pass can be as stale |

**Choice:** (b), in `formathost.passUpstream.Document` (`metadataSince`).

**Rationale:** the pass is the one place that must not read a document older than its own question. The cost is one conditional request
per package and attempt, which is what R-REPO-051 already says a pass does for an expired document. The development agent would revisit
(c) if the conditional requests of a full pass over very many held packages are measured to matter for an upstream's rate limit.

**Applied in the spec:** R-REPO-051. Code: `passUpstream.since`, `remote.metadataSince`. Test: `TestR_REPO_051_NpmPublicRegistryFeed`.

### Q-REPO-168 · A wait keeps the window it halved

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-054 halves the window once per wait and its criterion says `trove_mirror_concurrency` is at most half of its value before
the answer. The successes of the requests that were in flight when the wait began (up to `window − 1` of them) arrived after the
halving, counted towards "w successes in a row" and grew the halved window by one while the pass waited (4 to 2 to 3).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Count them | The window grows as soon as w successes arrive, whenever they were sent | Simplest | The gauge during the wait is above half; the pass restarts with more than half of its window |
| (b) The successes of requests sent before the wait began do not grow the window (chosen) | `throttle.success` compares the request's wait number with the current one, as `rateLimited` does for the answers | Symmetric with the rule that their rate-limit answers change nothing; the criterion holds | The window grows a little later |

**Choice:** (b).

**Rationale:** the answers of requests that were in flight say nothing about the rate after the wait. The development agent would
revisit it if the window is seen to recover too slowly after a wait.

**Applied in the spec:** R-REPO-054. Code: `throttle.success`. Tests: `TestR_REPO_054_SuccessesOfRequestsInFlightWhenTheWaitBeganDoNotGrowTheHalvedWindow`,
`TestR_REPO_054_RetryAfterSecondsPausesThePassAndHalvesItsWindow`.

### Q-REPO-169 · A pass writes a populate event per stored document as well as per stored artifact

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the bullet on the audit trail of R-REPO-057 says every artifact and document a pass stores writes the populate event; the
acceptance criterion counted 20 events for a pass of 20 tarballs. The pass stores the document of each package as well, and writes its
event, so the audit store holds 40.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The bullet is right, the criterion counts the tarballs' events (chosen) | 20 events of tarballs and one per stored document | One rule, as a client's fill | The criterion needs the words "of tarballs" |
| (b) Documents write no event | Only artifacts | The count of the criterion | A document stored by the system without a record; unlike a client's fill |

**Choice:** (a).

**Rationale:** the populate event is the record of who put what into the cache. The development agent would revisit (b) if the volume of
document events of a big mirror proves a burden of the audit store.

**Applied in the spec:** R-REPO-057. Test: `TestR_REPO_056_AStatusFollowsARunningPassAndEveryReplicaAnswersTheSame`.

### Q-REPO-170 · The integration suite reaches a stub as the public registry through a dial override

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: npm's `feed` is offered for `https://registry.npmjs.org/` only and reads `replicate.npmjs.com` (Q-NPM-69, Q-NPM-72); a test
needs a remote with that URL whose connections reach the stub on a loopback port, with the certificate and the Host header of the names.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A dial override of the outbound factory (chosen) | `outbound.WithDialOverride`, `app.Options.DialOverride`, `testenv.ReplicaOptions.DialOverride`: a map from `host:port` to the address connected to | The URL, the TLS name, the Host header and every rule that reads the URL stay those of the public registry; the egress policy applies to the address connected to, so the stack allows `127.0.0.1/32` as an operator would; production passes nothing | One more seam in the composition root |
| (b) Bind port 443 on the loopback and resolve the names | A resolver in the test process | No code change | A privileged port, a process-wide resolver, a name clash between parallel tests |
| (c) Let a repository setting name the replication host | A setting no operator needs | No seam in the root | A setting that exists for tests and widens what a repository can reach |

**Choice:** (a).

**Rationale:** only the destination of the connection changes, and the egress rules still judge the real address. The development agent
would revisit it if a second consumer wants the override to be a setting.

**Applied in the spec:** none; [developer/mirror.md](../developer/mirror.md). Code: `outbound.WithDialOverride`. Test: `TestDialOverride`.

### Q-REPO-171 · "Open connections" in the pre-emption criterion are the tarball fetches the stub is serving

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-055's integration criterion says the stub has at most eight open connections 1 s after the ninth fetch. The outbound
clients keep connections alive, so a TCP count includes idle ones that carry no fetch.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Count the tarball requests the stub is serving (chosen) | A counter in front of the stub's handler | It is the bound R-REPO-055 states: fetches against the upstream | Not a socket count |
| (b) Count TCP connections in the active state | `ConnState` of the stub's server | Closer to the words | A connection that finished its request and is idle is a different state in HTTP/1.1 and HTTP/2; the number depends on the transport |

**Choice:** (a).

**Rationale:** the budget of R-REPO-040 is a number of fetches. The development agent would revisit it if an upstream is found that
limits connections rather than requests.

**Applied in the spec:** R-REPO-055. Test: `TestR_REPO_055_AClientFetchThatHasToWaitPreemptsThePassOfAnotherProcess`.

### Q-REPO-172 · The 60 s after a long wait are observed through the engine's clock

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the criterion for `Retry-After: 3600` says the stub records no request of the mirror in the next 60 s, and a unit test with a
fake clock shows the next job continuing the pass at that time.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Wait 60 s in the suite | Real time | Literal | A minute of the suite for one assertion |
| (b) Watch 6 s, move the engine's clock 59 s on, dispatch again, then move it an hour on and clear the throttle (chosen) | `app.MirrorOverride.Clock` with an offset; the dispatch reads the clock | No request in the real seconds and none after 59 s on the engine's clock, and the continuation at the hour, all against the real stack | The wall time of the 60 s is not elapsed |

**Choice:** (b).

**Rationale:** what holds the pass is `rate_limited_until` and the due check of the dispatch, both of which read the engine's clock.
The development agent would revisit it if the wait were moved out of the engine's clock.

**Applied in the spec:** none. Test: `TestR_REPO_054_ALongWaitEndsTheJobDeferredAndTheNextJobContinuesAtThatTime`.

### Q-REPO-179 · A background request takes its slot before it enters a flight

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-REPO-149 left the pass's fetch to lead a flight of R-REPO-026 and to wait for its slot inside it. The leader held the
cross-replica flight lock while it waited (no timeout, blocked while a client waits for the repository and during the 30 s of the
yield window), so a client that joined waited on the flight without a deadline, clients of other replicas waited up to
`fetch_wait_timeout_seconds`, and after the pass's context ended the detached flight went on waiting and fetching.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The slot is taken before `collapse`; the fetch the request then leads runs under the slot's fate (chosen) | A request that may not run holds no lock and no flight; a pre-emption or the end of the pass's context cancels the fetch unless a follower of the replica joined it | No client waits under background rules; the lock is held only while a fetch runs | The slot is held while the request joins another request's flight |
| (b) A leader whose flight a client joined behaves like a client (client queue, its timeout, ignores waiting and the yield window) | The slot is taken inside the flight as before | Smaller change | The lock is still held while a background request that nobody joined may not run: clients of other replicas wait for it |
| (c) A background request never leads a flight | It fetches outside any flight | No sharing | A client that asks for the item the pass is fetching fetches a second time (R-REPO-055) |

**Choice:** (a). A request that joins another request's flight keeps its slot until the flight ends, bounded by the share B. When the
pass's context ends and a follower of the replica joined the flight, the fetch finishes (the client waits for it) without a slot; a
fetch nobody joined is cancelled.

**Rationale:** "a background request never holds the lock while it may not run" is the property clients need; (b) fixes the replica
and not the cluster. The development agent would revisit it if holding a slot while following a client's flight costs passes
measurable throughput.

**Applied in the spec:** R-REPO-055. Code: [internal/proxy/background.go](../../internal/proxy/background.go) (`bgSlot`, `holdBackground`),
[fill.go](../../internal/proxy/fill.go), [metadata.go](../../internal/proxy/metadata.go), [fetch.go](../../internal/proxy/fetch.go) (`run`).
Test: `TestR_REPO_055_AClientNeverWaitsUnderTheBackgroundRulesOfAFlightItJoined`.

### Q-REPO-180 · A background request that joined a client's flight reads a failure through the circuit and fetches once itself

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a pass's fill that joins a client's flight gets the flight's result, which carries the client classification (R-REPO-029): a
`429` was an item failure there, with no wait and no halving (R-REPO-054).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) When the joined flight ended with an error, the request reads the circuit: open is a wait (`circuit_open`); closed, it fetches once as the leader of a flight of its own, so that its own answer is classified as a pass's (chosen) | The client's `429` opened the circuit at once, so the pass waits; any other failure is repeated by the pass's own request | No new data on the flight; the rate-limit rule stays in one place | One more upstream request after a failure the client already met |
| (b) The flight carries the status, headers and body prefix for the pass | `RateLimitOf` on the client's answer | Exact | The flight's result grows for a rare case |
| (c) Leave it an item failure | Nothing to do | No code | The pass loses the wait and the halving exactly when the upstream throttles |

**Choice:** (a).

**Rationale:** the circuit is the shared memory of what the upstream just did. The development agent would revisit (b) if the second
request is seen to hit throttled upstreams that the circuit does not cover (`failureThreshold` above 1 for a `5xx`).

**Applied in the spec:** R-REPO-054. Code: [internal/proxy/fill.go](../../internal/proxy/fill.go). Test:
`TestR_REPO_054_ABackgroundRequestThatJoinedAClientsFlightReadsAThrottlingAnswerAsAWait`.

### Q-REPO-181 · The host maps the permanent refusals of a fill to `*mirror.RefusedError`, and the shutdown to `mirror.ErrShuttingDown`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the seam (`mirror.Host`) names a permanent refusal, which the engine counts and goes on from, and a request cancelled by
the shutdown, which stops the pass without a failure. The proxy's errors carry codes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host reads the codes of the engine's error: `not_found` for a negative entry, 404, 410 and a package marked gone (`CodeNotFound`); `blocked` for `vulnerability_blocked` and `vulnerability_data_missing`; `populate_denied` for `populate_not_permitted`; `gate_refused` for a not-found or forbidden answer of the format's gate; `shutting_down` for the code of the engine's cancelled request; the cause stays in the chain (chosen) | One table, in the host, tested | The engine stays free of the seam | A new refusal code of the engine needs a line |
| (b) The engine returns the seam's types | No mapping | Proxy imports `mirror` | A layer cycle in spirit (the engine is below the mirror) |
| (c) The mirror engine reads codes | Nothing in the host | The host stays thin | The mirror engine would know the proxy's codes |

**Choice:** (a). Offline (`not_cached_offline`) is none of them: it stays an error with its own code, which the engine reads and
stops the pass on (R-REPO-053). An integrity mismatch and an upstream error are failures of the item. `Ready` answers an error
wrapping `ErrShuttingDown` while the engine drains.

**Rationale:** a refusal that no retry changes must not be counted as a failure that makes the pass fail. The development agent would
revisit it if a code is found that fits two rows.

**Applied in the spec:** R-REPO-053 ([docs/developer/mirror.md](../developer/mirror.md) "What the host maps"). Code:
[internal/formathost/mirror_host.go](../../internal/formathost/mirror_host.go) (`mapFill`, `mapGate`, `mapBackground`). Test:
`TestMapFill`, `TestMapGate`, `TestMapShuttingDown`.

### Q-REPO-182 · The yield windows a replica keeps are purged when they pass and capped at 4096

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the signal `mirror_yield` names a repository by id, and any process that can publish names any id (H-526). A window was
removed only when a background request of that repository looked at it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every signal first drops the windows that have passed, and a new one is opened only while fewer than 4096 are held (chosen) | Bounded memory, no knowledge of the repositories needed | A forged flood costs at most 4096 entries; a legitimate window beyond the cap is not opened (the running fetches are still pre-empted) | A flood of 4096 live forged ids can hide a real window for 30 s |
| (b) Ignore the ids of unknown repositories | Exact | No forged entry at all | The engine does not know the repositories of other replicas' passes; a cache of ids would be one more thing to keep current |

**Choice:** (a).

**Rationale:** a delay of a pass by 30 s is within what H-526 already says a forged signal can do; the memory bound is what had to
be fixed. The development agent would revisit (b) if the engine gains a snapshot of the repositories it runs passes for.

**Applied in the spec:** R-REPO-055, [security/isolation-and-coordination.md](../security/isolation-and-coordination.md) (H-526). Code:
[internal/proxy/slots.go](../../internal/proxy/slots.go) (`yield`, `purgeYield`). Test: `TestYieldWindowsAreBounded`.

### Q-REPO-183 · One reader of Retry-After, and a `503` with an unusable one is a rate limit with no hint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-054 names `503` with a `Retry-After` header as a rate-limit answer, and says that a date in the past or an unreadable
value counts as absent, that is, as no hint. `RateLimitOf` classified such a `503` as an ordinary failure, and the client
classification (R-REPO-029) read the header with a second parser.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `upstream.ParseRetryAfter(v, now)` is the one reader; the client classification applies its cap and its floor of a second, `RateLimitOf` its hint rule (a date in the past is absent); a `503` with the header and no usable value is a rate limit with no hint (chosen) | The spec's sentence holds for the `503` | One parser | The hint rule of the pass and the cap of the client stay two small rules |
| (b) Keep the classification and edit the spec | A `503` with a bad header is a failure | No code | The pass backs off with the failure rule for an upstream that said "slow down" |

**Choice:** (a). `*upstream.RateLimitError` has `HTTPStatus()`, so the engine of the mirror reads the status through an interface
without importing the package.

**Rationale:** the header says the upstream wants the caller to slow down; whether its value is usable decides only the wait. The
development agent would revisit it if an upstream is found that sends `503` with a junk `Retry-After` as an ordinary failure.

**Applied in the spec:** R-REPO-054. Code: [internal/proxy/upstream/ratelimit.go](../../internal/proxy/upstream/ratelimit.go),
[internal/proxy/classify.go](../../internal/proxy/classify.go). Test: `TestRateLimitOf` (the two cases that stated the old rule were
changed), `TestParseRetryAfter`.

### Q-REPO-184 · A pre-empted discovery request is waited out and sent again, never a failure of the pass

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-055 lets a client fetch pre-empt a background request. The items of a fill handle it (the package comes back later); the discovery requests (the methods, a page of names, the document of a deleted package) had no rule and a pre-emption there failed the attempt.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `callDiscovery` waits `PreemptRetry` (5 s) and sends the same request again, counts `trove_mirror_preempted_total`, and ends only with the pass (chosen) | A client that presses cannot fail a mirror; the pass yields as R-REPO-055 says | One loop for every discovery request | A pass can wait as long as clients press |
| (b) A failure of the attempt | The merged behaviour | No code | Every busy hour of a repository would back the mirror off for minutes to hours |
| (c) Never pre-empt discovery | No rule in the host | Simple for the engine | A discovery request would hold a slot clients need; the contract of the seam says every background request may be pre-empted |

**Choice:** (a).

**Rationale:** the mirror is the guest of the upstream and of the clients. The development agent would revisit it if discovery requests were exempt from pre-emption in the host.

**Applied in the spec:** R-REPO-053 (step 2), R-REPO-055. Code: `request.go` (`callDiscovery`). Test: `TestR_REPO_055_APreEmptedDiscoveryRequestIsRetriedAndNeverFailsThePass`.

### Q-REPO-185 · A refused artifact is counted and skipped, and a transient failure finishes the other artifacts before the package fails once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The seam names `*RefusedError` for an artifact that a background fill cannot store until something outside the pass changes. The engine treated every error of an item as a failure of the package at once, so one removed version failed the package three times and kept the later versions from being stored.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `refused` is a new value of the label `result` of `trove_mirror_items_total`; the item is skipped and the package goes on and is done; a transient failure of an item is counted `failed`, the remaining items are tried, and the package records one failed attempt after them (chosen) | A refused version never blocks the others; a flaky item costs one attempt of the package per round, not one per item | The attempts and the retry are the package's, as R-REPO-053 step 3 says | A package with a refused item is done and is not retried by this pass |
| (b) Every error fails the package at once | The merged behaviour | Simple | A removed or blocked version keeps every later version of the package out of the mirror |
| (c) A refused artifact is a failure with no retry | Counts as failed | Visible in `failed` | The status shows packages failed for a decision the operator or the upstream made |

**Choice:** (a). The refusal reasons are the host's (`not_found`, `blocked`, `gate_refused`, `populate_denied`); they are not metric labels.

**Rationale:** a mirror stores what it may. The development agent would revisit the missing reason label if operators need to tell the refusals apart in metrics.

**Applied in the spec:** R-REPO-053 (step 3), R-REPO-057 (`items_total`). Code: `fill.go` (`fillPackage`, `failPackage`). Tests: `TestR_REPO_057_ARefusedItemIsCountedAndTheOthersGoOn`, `TestR_REPO_053_AFailingItemFinishesTheOthersAndTheFailureIsOneAttempt`.

### Q-REPO-186 · The end of the held-names phase is recorded in the pass cursor as its own value, and the engine's cursors are told by their structure

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-REPO-158 stores the position of the phase that adds the held names in `pass_cursor`; the end of the phase stored the same NULL as "not begun", so a repository with E exact include names and H held names, E + H between 10,000 and 10,000 + one page, ran the phase again after every fill and never reached the format's pages.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `{"mirrorHeldAfter": <name>}` while paging, `{"mirrorHeldDone": true}` from the end of the phase until the format's first page replaces it; a cursor is the engine's when it is an object with exactly that one member (chosen) | The phase ends once; a format's cursor that contains the words is not mistaken | No new column | The column holds the engine's value for the length of the phase |
| (b) A column `pass_held_done` | Explicit | Obvious | A migration for a flag that lives for one phase |
| (c) Look for the substring in the stored text | The merged check | No parse | A format cursor that holds the words is taken for the engine's |

**Choice:** (a).

**Rationale:** one small value, parsed by structure. The development agent would revisit it if the held names move into the format's plan.

**Applied in the spec:** R-REPO-051 (methods). Code: `discovery.go` (`parseEngineCursor`, `heldStep`). Tests: `TestR_REPO_051_TheEndOfTheHeldNamesPhaseIsRecordedAndTheFormatsPagesFollow`, `TestR_REPO_051_AnEngineCursorIsRecognizedByItsStructure`.

### Q-REPO-187 · The upstream's wait is stored whatever its length, a job that starts during it is skipped, and the run operation refuses with its end

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Only a wait over 300 s was written (`rate_limited_until`), and the claim cleared it; a job released in the middle of a short wait resumed at once in another process, and a run during a deferral sent requests the upstream had asked the mirror not to send.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `rate_limited_until` is written with every wait (a statement the wait counters already need), the claim does not touch it and the end clears it; `begin` skips a job while it lies ahead (`{"skipped": "rate_limited"}`); the run operation answers 409 `mirror_rate_limited` with the extension `until`; the status shows `rateLimit.until` only while it lies ahead (chosen) | No request during a wait, in this process or another; the operator is told when to ask again | One write per wait, as before | A new error code and a response description |
| (b) Keep only the long wait | The merged behaviour | No change | A release mid-wait resumes against an upstream that just said stop |
| (c) Let the run operation enqueue and the job skip | No new code | Smaller API | A 202 for a pass that cannot run; the operator waits for a job that does nothing |

**Choice:** (a). The dispatch already waits for `rate_limited_until`; the job's own check is for a pass enqueued by a request or a release.

**Rationale:** a wait is the upstream's decision. The development agent would revisit the code if the UI should offer a countdown.

**Applied in the spec:** R-REPO-054, R-REPO-056 (run, status). Code: `pass.go` (`begin`), `queries/mirror.sql`, `internal/api/v1/mirror_api.go`, `errs.CodeMirrorRateLimited`. Tests: `TestR_REPO_054_AJobDuringADeferralIsSkippedAndTheClaimKeepsTheWait`, `TestR_REPO_054_AShortWaitIsPersistedWhileItLastsAndTheEndClearsIt`, `TestR_REPO_056_ATakeoverKeepsTheRequestAndTheWaitAndANewPassConsumesTheRequest`, the run test of `internal/api/v1/mirror_api_test.go`.

### Q-REPO-188 · The current admission is checked before a name is asked for, and the pending names are dropped when a pass is claimed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A name is admitted when it is discovered. The patterns, the claims and the name rule can change before the pass reaches it, and a pending name was never admitted again: a mirror whose patterns were narrowed fetched what the operator had excluded.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Admits` is built again when the snapshot's config version changes; `fillPackage` checks it first and deletes a refused row as done; a claim drops the pending rows the current admission refuses; a deleted name the admission refuses is not refreshed (chosen) | R-REPO-051's promise (never asked for, not even its document) holds for names already pending | One extra check per package; a pass start pages the pending names once | A change of the claims alone (not the document) applies at the next claim or document version |
| (b) Only at discovery | The merged behaviour | Nothing to do | Excluded names are fetched after a change of the patterns |
| (c) Delete the pending table when the patterns change | Simple | Nothing to check | The fingerprint already makes the next pass full; the names of a running pass are lost and rediscovered |

**Choice:** (a).

**Rationale:** the patterns are the bound of everything the mirror does (R-REPO-005). The development agent would revisit it if the claims version were part of the snapshot entry.

**Applied in the spec:** R-REPO-051 (patterns bound everything). Code: `pass.go` (`rebuildAdmits`, `dropRefused`), `fill.go`. Tests: `TestR_REPO_051_APendingNameIsReAdmittedAndARefusedOneIsNeverAskedFor`, `TestR_REPO_051_ANewDocumentDuringThePassRebuildsTheAdmissionBeforeTheNextPackage`, `TestR_REPO_051_ARemovedNameThePatternsRefuseIsNotRefreshed`.

### Q-REPO-189 · A request for a pass is consumed by a new pass, not by the takeover of one in progress

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The claim cleared `pass_requested_*` whenever it ran; a request for a full pass that arrived while a pass was in progress was taken by the job that took that pass over, which was not full.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The claim clears the request only when a fresh pass starts; the row stays due while a request is set (chosen) | A full request is never lost; the pass after the one in progress is full | The request outlives a pass it did not start | A takeover leaves the row due, which an active job's dedup key absorbs |
| (b) Clear at every claim | The merged behaviour | No change | The full request is silently dropped |
| (c) Make the pass in progress full | Honours the request at once | Immediate | A pass cannot change kind half way (its cursor is the other kind's) |

**Choice:** (a).

**Rationale:** the operator asked for a full pass. The development agent would revisit it if a request should end a pass in progress.

**Applied in the spec:** R-REPO-053 (claim), R-REPO-056 (run). Code: `queries/mirror.sql` (`MirrorClaim`). Tests: `TestR_REPO_056_AFullRequestSurvivesTheTakeoverOfAPassInProgress`, integration `TestR_REPO_056_ATakeoverKeepsTheRequestAndTheWaitAndANewPassConsumesTheRequest`.

### Q-REPO-190 · The bound of 10,000 pending names counts every row, and a pass whose table is full of names that failed ends partial

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The count read the rows with fewer than three attempts; a name left after three attempts stays and is given its attempts back at the next claim, so a table of failed names was not bounded by the cap and discovery went on above it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PendingCount` counts every row of the repository; a pass that finds the table at its bound with nothing it can take ends `partial` with `discoveryComplete: false` and a note, and does not advance the cursor (chosen) | The table cannot grow past its bound whatever fails | The failed names that do not clear count against discovery until a pass takes them | A mirror of an upstream that fails 10,000 names makes no discovery progress until they succeed or the patterns narrow |
| (b) Delete the row at its final attempt and keep a count | The table is only live names | More room | A failed name is forgotten until a full pass rediscovers it, and `partial` loses its list |
| (c) The merged count | No change | No change | The bound is not a bound |

**Choice:** (a).

**Rationale:** the bound exists so that the table stays small (Q-REPO-135). The development agent would revisit (b) if failed names pile up in practice.

**Applied in the spec:** R-REPO-053 (step 2). Code: `pass.go` (`pendingFull`), `queries/mirror.sql`. Tests: `TestR_REPO_053_TheBoundCountsTheRowsThatFailedAndEndsThePassPartial`, integration `TestR_REPO_053_ThePendingCountTakesEveryRowAndDropsRefusedNamesUnderTheFence`. [docs/security/mirror.md](../security/mirror.md).

### Q-REPO-191 · A document that does not load is a pause, and a cursor is measured as the column holds it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A remote whose stored document fails to load is loaded disabled with no remote section; the engine took that for a repository that left mode mirror and cleared its pass and pending names. And the cursor check measured the compact JSON while the column's check measures the text PostgreSQL prints (a space after every comma), so a cursor between the two failed the statement as an internal error.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A repository of class remote whose remote section did not load is neither mirrored nor left: its row, pass and pending names stay, the dispatch enqueues nothing for it and a job that starts for it does nothing (`invalid_document`); the store measures a cursor with `octet_length(jsonb::text)` before the write and answers `ErrCursorTooLarge`, which the pass records as `cursor_too_large` (chosen) | A broken document costs no pass; the error code is the true one | One extra read per page with a cursor | A mirror with a broken document waits for its fix without a status line of its own |
| (b) Treat it as a mode change | The merged behaviour | No code | A transient storage fault deletes a pass of hours |
| (c) Measure the cursor with a margin | No read | Cheaper | Wrong in both directions |

**Choice:** (a).

**Rationale:** a mode change is the owner's decision (Q-REPO-162), a document that does not load is not. The development agent would revisit the cursor read if the format's cursors are bounded to a size that cannot differ.

**Applied in the spec:** R-REPO-053 (stops, step 2). Code: `engine.go` (`Dispatch`), `pass.go`, `store_pg.go` (`AddNames`). Tests: `TestR_REPO_053_ADocumentThatDoesNotLoadIsAPauseNotAModeChange`, integration `TestR_REPO_053_ACursorIsMeasuredAsTheColumnHoldsIt`.

### Q-REPO-192 · The held names are paged in the default collation, and the unused index of the state is dropped

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The held names were paged with `COLLATE "C"`, which the index of `packages (repo_id, name)` cannot serve, and migration 00040 created an index on `next_pass_at` that no statement filters by (the dispatch reads the table's few rows by `repo_id = ANY`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The page reads `name > $after ORDER BY name` in the column's collation, which the unique index serves; the index `mirror_state_due` is not created (migration 00040, changed in place before it is pushed) (chosen) | A page of held names is an index range, and the table has no write cost for an index nobody reads | The order is the database's, not bytewise | None: paging only needs a consistent order |
| (b) Keep both | No change | No migration edit | A sequential sort per page of a large repository; a write cost for nothing |
| (c) An index with `COLLATE "C"` on packages | Bytewise order | Matches pending | A second index on the busiest table |

**Choice:** (a).

**Rationale:** the page only needs an order that is the same on both sides of the comparison. The development agent would revisit it if a statement filters `mirror_state` by `next_pass_at`.

**Applied in the spec:** R-REPO-053 (data model, the index line). Code: `queries/mirror.sql`, `migrations/00040_mirror.sql`. Test: integration `TestR_REPO_057_TheStateKeepsTheLastCompletePassAndHasNoUnusedIndex`, `TestR_REPO_053_ThePendingCountTakesEveryRowAndDropsRefusedNamesUnderTheFence`.

### Q-REPO-193 · A project administrator keeps the scope of a mirror, not only its settings

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-REPO-142 froze `remote.mode` and `remote.mirror` for a project administrator. The bound of R-REPO-052 is checked on each
write by itself, so a delegate could change the include list of an administrator's `@acme/*` mirror to some seventy patterns with a
literal prefix each (`a*` to `z*`, `0*` to `9*`, `@a*` to `@z*`, `@0*` to `@9*`; each passes the bound rule, together they cover
every npm name), or point `remote.url` at another upstream, and the next pass would be a full one over what the administrator never
chose.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The stored include list, exclude list, url and extra hosts are frozen while the stored mode is mirror and the write keeps it (chosen) | 422 `mirror_restricted` at the pointer of the changed member; a write that also sets `pullThrough` may change them | Closes the widening and the redirect with the rule that already freezes the settings; the lists are compared as sets, so a document sent back in another order saves | A project administrator cannot narrow a mirror's patterns; it turns the mode back to `pullThrough` first |
| (b) A bound on the number or shape of patterns | At most n patterns, none a single character | No freeze | Any number is a choice; two-letter prefixes still cover a registry |
| (c) A byte bound a delegate cannot change | `maxBytes` becomes mandatory | Bounds the volume | The scope and the upstream stay a delegate's; volume is only one of the harms |

**Choice:** (a). The comparison is with the canonical stored section and document under the row lock, as in Q-REPO-127. A create by a
project administrator cannot be a mirror (Q-REPO-142), so the rule applies to updates, patches included (they build the next
document and take the same path).

**Rationale:** what a mirror covers and where it reads from are the same decision as its settings. The development agent would revisit
it with Q-REPO-142's condition, a per-project allowance.

**Applied in the spec:** R-REPO-050; [authorization.md](authorization.md) R-AUTHZ-026; [security/delegated-administration.md](../security/delegated-administration.md). Code:
`internal/repository/validate_mirror.go` (`checkDelegatedMirrorScope`), `delegated.go`.

### Q-REPO-194 · One predicate decides whether a url is a format's well-known upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the bound rule of R-REPO-052 compared the saved `remote.url` with the catalog's default upstream as strings, and the saved url
keeps a query; npm's lister ignored it and npm's document requests drop it. `https://registry.npmjs.org/?` was therefore saved with a
warning only and read the replication feed of the public registry unbounded.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `discover.SameUpstream(url, known)`, used by the bound rule and by the lister (chosen) | Scheme, host, effective port and path equal, no user information, query or fragment | One answer in one place; the discovery contract gains one function (an addition) | A url with a query is never "the well-known upstream", which is why a mirror refuses a query (Q-REPO-195) |
| (b) An optional interface of the format's remote adapter, called by the repository model | The format decides | A format may know more | The repository model reads the catalog, not adapters; two implementations to keep equal |
| (c) Normalize the query away on save | `remote.url` loses it | The two agree | Silently changes a stored upstream address that a partner's registry may need |

**Choice:** (a). NuGet's lister does not decide on the url (it reads the service index), so NuGet only shares the bound rule; the
same predicate covers `https://api.nuget.org/v3/index.json?` there.

**Rationale:** a decision that two places make with different rules is a way around the bound. The development agent would revisit
(b) if a format's well-known upstream needs more than the url (two hosts).

**Applied in the spec:** R-REPO-052. Code: `internal/mirror/discover/discover.go` (`SameUpstream`), `internal/repository/validate_mirror.go`,
`internal/formats/npm/mirror.go`.

### Q-REPO-195 · A mirror's url carries no query; a pull-through remote keeps what it stored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse a query (and `?` alone) in `remote.url` for mode mirror, 422 `invalid_value` at `/remote/url` (chosen) | Fragments and user information are refused for every remote already (R-REPO-024) | The formats' requests drop the query, so a mirror's url with one is the upstream without it, and no stored document that predates the mode can be broken | A mirror of an upstream that needs a query token on every request is not possible; none of the formats' upstreams does |
| (b) Refuse it for every remote | One rule | Simple | A stored pull-through remote with a query would no longer be sent back and saved (Q-REPO-130's read-back rule) |

**Choice:** (a). A mirror saved before this rule with a query in its url is refused on its next write until the query is removed; the
mode shipped without an engine, so none of them ran a pass.

**Rationale:** the refusal costs nothing where the query was harmless and closes the case where it was not. The development agent would
revisit (b) when the query of a remote's url is dropped by every format.

**Applied in the spec:** R-REPO-052 (the acceptance criteria). Code: `validate_mirror.go` (`checkMirrorURL`).

### Q-REPO-197 · The install-time bound of clients first is enforced nightly; the open requests and the pre-emptions per change

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-055's e2e criterion asks that `npm ci` through a remote whose pass runs takes at most 120 % of its time without
the pass, that the upstream never holds more than `maxConcurrentFetches + n − 1 + B` requests open, and that pre-emptions happen.
On a 17-CPU host the set `mirror` measured 97 % and 98 %; on the GitHub-hosted runner of Test and Release run 42111093693 (four
CPUs for the kind cluster, the stubs and the clients) the three pairs measured 82 %, 139 % and 128 %, a median of 136 %, while the
open requests stayed at 9 against the bound of 12 and the pre-emptions grew by 6. What clients first gives the clients is the
upstream's fetch slots; the CPU a pass spends on hashing, storing and verifying is shared with the installs on such a runner.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per change: the open-request bound, the pre-emptions and the installs are enforced, the time ratio is logged; the nightly run enforces the ratio with `TROVE_PERF_GATE=1` (chosen) | The rule of Q-ARCH-220 for timing bounds on the CI runners (the owner's option B of 2026-10-30) | The per-change leg tests what the mechanism controls; a time miss is still seen every night and becomes a finding | A regression of the ratio shows a day later |
| (b) Enforce the ratio per change | As specified | Immediate | Fails on the runner's CPU, not on trove: the slots held |
| (c) Raise the bound to 150 % | One number | Green on the runner | Hides a real regression on a fast host; the number is the runner's, not the requirement's |

**Choice:** (a). The nightly job `e2e-mirror` sets `TROVE_PERF_GATE=1` for the set `mirror`.

**Rationale:** the bound on the upstream's open requests is what R-REPO-055 controls and what a defect would break (the stall of a
client behind a background flight, Q-REPO-179, broke the ratio and is caught by it on any host). The development agent would
revisit (a) when the e2e legs run on runners whose CPU the installs do not share, or if the nightly ratio stays above the bound,
which then is a finding of its own. Revisited by [Q-REPO-211](#q-repo-211--the-nightly-install-time-ratio-of-clients-first-runs-on-the-large-class): the nightly ratio runs on `large`.

**Applied in the spec:** R-REPO-055 (the e2e criterion's ratio enforced nightly). Code: `test/e2e/mirror_clients_test.go`,
`.github/workflows/performance.yml` (`e2e-mirror`).

### Q-REPO-173 · The e2e set `mirror` runs in a profile of its own: the worker pool `mirror` of two processes, the three `packages` replicas as the client-serving ones

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-059 asks for three client-serving replicas and two processes that claim the queue `mirror`; R-HA-062 expresses a pool that claims one queue as an entry of `worker.pools`, and the main worker then excludes that queue. The dev profile already has a pool `vuln`; a list in a values file replaces the one before it. The status operation of R-REPO-056 is served by `api` pods, not `packages` pods (the roles of R-ARCH-001).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A profile `mirror` (values-scatter.yaml, then [values-mirror.yaml](../../deploy/dev/values-mirror.yaml)) with `worker.pools: [{name: mirror, queues: [mirror], replicaCount: 2}]` and the main worker unchanged (chosen) | Passes run only in the pool; clients are served by the three `packages` pods; the main worker keeps every other queue | The topology of R-ARCH-001 and R-HA-062 as specified: pass and clients meet only through PostgreSQL, Valkey and the signal; the pool's two processes are the two claimants of R-REPO-058 | The pool `vuln` is gone in this profile (the scanning is off in it) |
| (b) No pool: the main worker (two replicas) claims `mirror` with everything else | The default topology | No new profile | Passes share processes with other jobs; R-HA-062's exclusion is not exercised |
| (c) Role `all` | Clients and passes in one process | Pre-emption in the process | It is the integration tier's case (R-REPO-055), not the topology of the cut |

**Choice:** (a). The status criteria of R-REPO-058 are read from the three `api` pods, which serve the operation; a `packages` pod answers `404` (logged by the test, not asserted).

**Rationale:** the set has to prove the topology the specification fixes. The development agent would revisit (a) if the chart gives a pool a way to claim a queue in addition to the main worker's.

**Applied in the spec:** R-REPO-059 (the e2e set). Code: `deploy/dev/values-mirror.yaml`, `up.sh` (`TROVE_PROFILE=mirror`), `test/e2e/mirror_env_test.go`.

### Q-REPO-174 · The npm mirrors of the e2e set are remotes of the name `registry.npmjs.org`, which a CoreDNS rewrite makes the npm test upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the method `feed` is offered only for the well-known upstream (`https://registry.npmjs.org/`, R-REPO-051), whose replication service is `replicate.npmjs.com`. The method `names` takes the exact include patterns, at most `MaxPatterns` = 100 per list (R-REPO-005), so it cannot list a pass of 300 packages; the include patterns also bound what a client may pull through the remote.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A CoreDNS `rewrite` of both names to the Service `npmstub`, made and undone by the tests; the stub's certificate carries both names (chosen) | The remote's URL is the real one and the feed runs as against the public registry | The method of the product's main path is exercised end to end; no product change | The cluster's DNS is edited for the run; a run that is killed leaves the rewrite until the next run removes it (marker `# trove-e2e-mirror`) |
| (b) `names` against the private URL | Up to 100 exact patterns | No DNS change | Not the feed; at most 100 packages, so not the pass of 300 |
| (c) `hostAliases` on the trove pods | The names resolve in the pod | No cluster DNS edit | The chart has no such value; a value added for a test |

**Choice:** (a) for the load and the drain; (b) for the Valkey and the two-claimant tests, whose passes are small (40 and 3 names). The feed's incremental pass and the replication host's credential rule are the integration tier's.

**Rationale:** R-REPO-059 names the stub upstreams of both formats and a pass of 300 packages. The development agent would revisit (a) when the chart can resolve a name for its pods (`hostAliases`) or a remote can name its replication host.

**Applied in the spec:** R-REPO-059. Code: `ensurePublicNPMNames` in `mirror_env_test.go`, `deploy/dev/npmstub.yaml`.

### Q-REPO-175 · "Connections open" at the test upstream are the requests it is serving at once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-055 bounds the upstream's concurrent fetches at `maxConcurrentFetches + n − 1 + B`; its e2e criterion speaks of connections open at the stub.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The stub counts requests in flight (from the moment it has counted one, its delay included, to the end of the handler) and keeps the maximum since the last reset: `GET /-/stub/inflight` (chosen) | One number per fetch | It is what a fetch slot is; independent of how the client reuses connections | Not a count of TCP connections |
| (b) TCP connections (`ConnState`) | Open sockets | The words of the criterion | A client that keeps connections alive, or multiplexes (HTTP/2), makes the number say nothing about concurrent fetches |

**Choice:** (a), in both stubs (`test/npmstub/inflight.go`, `test/nugetstub/inflight.go`; no other behaviour changed).

**Rationale:** a fetch slot is one request. The development agent would revisit (a) if the outbound client ever opens one connection per fetch and a socket bound becomes a requirement.

**Applied in the spec:** R-REPO-055 (e2e criterion). Code: the two stubs, `npmInflight`, `nugetInflight`.

### Q-REPO-176 · The install time without the pass is measured on a second remote of the same settings whose mirror is paused

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `npm ci` through a remote fills the remote's cache; a second `npm ci` through the same remote is warm. "Within 120 % of its time without the pass, measured in the same run" needs two runs that start cold.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two remotes of one upstream and one set of settings: the baseline through the one with `mirror.paused: true`, the run with the pass through the other (chosen) | Both cold; only the pass differs | A fair comparison with the same code path (mode `mirror`) | Two remotes instead of "the same remote" |
| (b) The baseline first through the remote under test, then the run with the pass | One remote | The words of the criterion | The second run is warm and would pass for any pass |
| (c) The baseline through a `pullThrough` remote | The ordinary remote | Simple | Another mode; the difference is not only the pass |

**Choice:** (a). The baseline repeated on three further paused remotes ran 11.98 s, 11.99 s, 12.03 s: the measurement is stable, so 120 % is a tight criterion.

**Rationale:** the criterion is about the pass's cost. The development agent would revisit (a) if a remote's cache can be emptied through the API.

**Applied in the spec:** R-REPO-055 (e2e criterion). Code: `clientsFirstNPM`.

### Q-REPO-177 · The pass of the load lists every package of the test upstream; the drain's pass is exactly 300

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: include patterns bound a remote for clients as well (R-REPO-005), and a pass lists what they admit. `npm ci` of the reference project needs 707 packages of the corpus; the criterion asks for a pass of 300.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Load: no include patterns, `maxBytes` 4 GiB as the bound (R-REPO-052); the test upstream holds the 700 packages of its corpus and 300 more the test publishes, so the pass lists 1,005 names and has 300 packages the install does not touch. Drain: `includePatterns: ["e2e-mirror-<run>-*"]`, exactly the 300 (chosen) | The install set is inside the pass; the drain's pass is the criterion's | The pass has work of its own while the clients are served by the same remote | The load's pass is larger than 300 |
| (b) Patterns that admit 300 names only | Exactly 300 | The criterion's number | A client could pull no other package through the remote: the install of 707 would be refused |
| (c) A second remote for the clients | Separate | Exactly 300 for the pass | Not "the same remote" |

**Choice:** (a). The test upstream holds each tarball for 120 ms and each document for 20 ms in the load (so that fetches overlap) and a tarball for 1 s in the drain (so that the pass of 300 outlasts the pool's grace period).

**Rationale:** the criterion is the client-first rule on one remote. The development agent would revisit (a) if the spec's "300" is meant as the size of the whole pass.

**Applied in the spec:** R-REPO-055, R-REPO-058 (e2e criteria). Code: `clientsFirstNPM`, `TestMirrorLong_R_REPO_058_DrainOfTheNodeOfAPass`.

### Q-REPO-178 · The drain is the set `mirror-long` (nightly); the rest is the set `mirror` in a job of Test and Release that runs when the change touches the mirror

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner's rule of 2026-11-01 puts drains, failovers and soaks into the nightly workflow, on dispatch and local runs, never in the release path; the per-change legs run on `large` runners, and the legs that load the NuGet clients image on GitHub-hosted runners (commit 7e80aee0).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `mirror` per change in the job `e2e-mirror` of Test and Release, with a scope step like `e2e-vuln`; both sets nightly in the job `e2e-mirror` of HA and Performance (chosen) | The drain is out of the release path; the short set guards the mirror | The rule and the pattern of the vulnerability suite | Two jobs of one name in two workflows (as `e2e-vuln`) |
| (b) Everything in the nightly workflow | No per-change leg | Cheapest | R-REPO-055's clients-first criterion would run once a day only |
| (c) Everything per change | Simplest | One set | A drain of 5 minutes and a pass of 150 s in every change |

**Choice:** (a). `mirror` loads the NuGet clients image and runs on a GitHub-hosted runner; `mirror-long` needs the npm clients only and runs on `large`.

**Rationale:** as the owner's rule says. The development agent would revisit it when `mirror` has been seen to take longer than the other per-change legs.

**Applied in the spec:** R-REPO-059. Code: `.github/workflows/release.yml`, `.github/workflows/performance.yml`, `deploy/dev/e2e.sh`.

### Q-REPO-198 · The failure suite of the mirror: a remote and a range of names per scenario, the upstream's request log for the waits, the process lost three ways

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: phase 7's failure suite (R-HA-052) is to cover the mirror's pass: its process lost (R-REPO-053 "Crash and failover"), the upstream
throttling it while it moves (R-REPO-054) and the database and Valkey failing over. The drain of R-REPO-058 is one remote over the 300 extra
packages of the run; its pass lasts 150 s. The counting npm upstream counted requests by path, which says how many tarballs were served
but not when: a pass that sends nothing during a wait is a statement about times.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One remote per scenario over its own hundred of the extras (the include pattern `e2e-mirror-<run>-<digit>*`), the upstream's request log with arrival times, statuses and `Retry-After`, the process lost by a force-delete, a stop and a freeze of its node (chosen) | Every scenario starts from an empty store for its names, so "no tarball fetched twice beyond the fetches in flight" is exact; the waits are checked on the upstream's clock alone | No clock of the test host enters the assertions; a scenario that fails leaves the others unaffected; the freeze lets the lost process come back and write under an old fence | Seven scenarios of 3 to 7 minutes make the long set about 40 minutes |
| (b) One remote over the 300 extras for all scenarios, one after the other | Fewer remotes | Fewer API calls | The second scenario finds the first one's tarballs stored and asks the upstream for nothing: the counters prove nothing |
| (c) The waits read from the status (`rateLimit.until`) and the test host's clock | No new stub endpoint | Smaller stub | The status is trove's view, the test host's clock is not the cluster's: an assertion about what the upstream received would rest on the thing under test |

**Choice:** (a). The stub's `GET /-/stub/requests` returns the registry host's requests with their arrival and completion times, status and
`Retry-After`; a wait is held against the pass from 500 ms after the answer that named it (fetches already on their way) to its end less one
second (two clocks of the cluster's processes). A lost process must progress again within 5 minutes: the lease of 60 s, the reaper's tick and
one dispatch tick of a minute, with a margin.

**Rationale:** the criteria are statements about what the upstream saw, so the upstream is the witness. The development agent would revisit the
margin of the wait check if a fast cluster shows requests inside the tolerance, and the range size if the passes outlast the nightly budget.

**Applied in the spec:** R-REPO-053, R-REPO-054, R-REPO-059 (acceptance criteria). Code: `test/e2e/mirror_long_fault_test.go`,
`test/npmstub/requestlog.go`.

### Q-REPO-199 · The scenarios of the failure suite do not outlive each other: the remote is removed and gone before the next scenario, and a policy write that meets a moved version is read and written again

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first nightly run of `mirror-long` (run 42177273488) failed three scenarios at once with one cause, and the scenarios after them with its consequences. The suite's account reads
the remotes through one policy that lists them. Deleting a remote with content is a job (`202`), and the job's end removes the repository row
and, with it, moves the version of the policies that name it (R-AUTHZ-028). The next scenario's create wrote the policy on the version it had
read a few milliseconds before and got `412` (R-API-012), so its remote existed but its cleanup, registered after the create, never ran: the
unpaused remote went on to run passes against the test upstream during the scenarios after it, whose counters (tarballs served twice) and
wait checks (requests inside a wait) then counted its requests.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The cleanup is registered before the remote is created; removing a remote waits until the repository answers `404`; the policy write reads the version again and writes again on `412`, five times at most (chosen) | The scenario owns its remote from the first request on and leaves nothing running; a moved version is what `412` says and the client's answer to it is to reload | Each cause is closed at its own place; the pass conditions are unchanged | A scenario waits for the deletion job (seconds) |
| (b) One policy per scenario | No shared version | No retry | A policy per remote multiplies the policies of the run and the replicas' agreement checks |
| (c) Filter the upstream's log by the scenario's range of names | A leftover remote would not count | No wait for deletions | Hides a leftover instead of removing it, and a leftover still takes the pool's slots and the upstream's throttled answers |

**Choice:** (a).

**Rationale:** the counters of the suite are exact only when the scenario's remote is the only one that sends. The development agent would
revisit the retry count if a deletion job is seen to outlast five reads.

**Applied in the spec:** none (test harness; the product behaves as R-API-012 and R-AUTHZ-028 say). Code: `test/e2e/mirror_env_test.go`
(`drop`), `test/e2e/m1_env_test.go` (`ensurePolicy`), `test/e2e/mirror_long_fault_test.go` (`startHeldPass`).

### Q-REPO-200 · The node lost under a pass is cleared of the operators and the primaries first

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: in the nightly run 42192042297 the node-loss subtest of `TestMirrorLong_R_REPO_053_WorkerLostMidPass` saw the pass progress again 6 min 51 s after the stop
(bound 5 min) and three of four client installs through the remote answer `503`. The test had moved only the PostgreSQL primary off the node, by a switchover that ended
2 s before the stop; the node then hosted the PostgreSQL standby (the primary's synchronous partner, announced in its configuration at 09:14:16) and the pod of the
CloudNativePG operator. The primary's log shows `canceling wait for synchronous replication` for every commit from 09:14:18; the operator was replaced by Kubernetes
after its toleration of 300 s and dropped the standby from the synchronous set at 09:20:16, and the pass progressed at 09:21:09. The frozen-node subtest, whose node hosted
neither, progressed after 1 min 5 s. The pool's second process was on another node throughout, so the product did what the spec says; the database below it could not commit.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Widen the bound to the operator's replacement | 5 min becomes 10 | The test passes | The bound would measure Kubernetes' default toleration, and "the pass progresses within the lease and the dispatch tick" would no longer be checked |
| (b) Clear the node as the HA suite does ([Q-HA-106](ha-and-helm.md#q-ha-106--the-node-of-the-node-loss-scenario-is-chosen-first-and-cleared-not-searched-for)): operators, Valkey primary and PostgreSQL primary are moved first (chosen) | `clearNode` before `docker stop` and `docker pause` | The subtest measures the pass; the PostgreSQL and operator rows of R-HA-052 stay their own scenarios | The standby may stay on the node (see below) |
| (c) Also replace the standby | Delete the instance's pod and volume | A clean node | An instance rebuild of a minute inside every subtest; the loss of a standby is a row of its own |

**Choice:** (b).

**Rationale:** the pass condition is unchanged. The development agent would revisit the standby if the subtest is seen to fail on a stall that the operator's drop of the
standby (one cluster detection time) explains.

**Applied in the spec:** none (test harness). Code: `test/e2e/node_loss_test.go`, `test/e2e/mirror_long_fault_test.go` (`loseNode`).

### Q-REPO-201 · A throttled answer is not a fetch: the per-name bound of the tarballs counts what the upstream served

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: both subtests of `TestMirrorLong_R_REPO_054_ThrottledUpstreamAcrossAMove` ended `complete`, with no request inside a wait and 3 and 8 throttled answers (limits 6 and 12),
and failed on `assertFetchedOnce`: the tarball of one name was served 3 times (429 case), two names 4 and 6 times (503 case, `Count: 8`, the first eight tarball requests refused).
The stub's counters count every request of a path, the refused ones too; the 503 throttle refuses the first eight requests, which the pass's two fetches in flight repeat on the same
few names. The function's bound "no name more than twice" meant fetches and counted answers, and only the sum over names allowed the throttled ones (`throttled + concurrency`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Take the refused requests out of the per-name counts (chosen) | The request log's tarball requests with the throttle's status are subtracted by path; the sum is then bounded by the fetches in flight (`concurrency`) | "Served" means what the upstream delivered; R-REPO-054's bound on the throttled answers stays in its own check | The log must not have dropped requests (the helper already fails on that) |
| (b) Allow `1 + throttled` per name | Loosens the bound | No log read | A name fetched five times for no reason passes |
| (c) Throttle fewer requests | Count 2 | Fits "twice" | The scenario would no longer show a burst-free recovery |

**Choice:** (a).

**Rationale:** the condition of R-REPO-053 is that a lost process costs at most the fetches in flight; a throttled answer costs the upstream nothing. The development agent would revisit it if a
throttled answer is ever seen to carry a body the pass stores.

**Applied in the spec:** none (test harness). Code: `test/e2e/mirror_long_fault_test.go` (`assertFetchedOnce`).

### Q-REPO-202 · A cached item whose object is lost is a cache miss: filled again from the upstream, or 503 content_unavailable

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-62. On the dev cluster, after its object store had lost its data, a cached document of a remote repository
answered `500` and was never fetched again (seen by the failure suite and by the NuGet drain test). The metadata rows outlive
the objects. The engine handed the store's "no such object" of a document through as an unclassified error (`500 internal`),
and a cached tarball or package answered `503 content_unavailable` from the item store, also without a fetch. [storage.md](storage.md)
R-STOR-012 and R-STOR-027 and [ha-and-helm.md](ha-and-helm.md) R-HA-024 already said that a remote repository treats such an
item as a cache miss and fetches it again; nothing implemented it, and no read marked the blob.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A cache miss, filled again under steps 5 to 10 of R-REPO-025; a client that cannot fill it gets `503 content_unavailable` (chosen) | The read that finds the object gone marks its blob `missing` ([storage.md](storage.md) Q-STOR-151); every replica sees the mark in PostgreSQL; the fill is an ordinary fill in one flight, and its commit of the same content writes the object again | The remote has the upstream to restore from; one fetch per lost item; the recorded digest and the item's rows stay | A large loss turns into a burst of upstream fetches, bounded by R-REPO-040's slots |
| (b) Answer `503` and wait for an operator's restore or repair | No upstream traffic | The cache stays broken until someone acts, as on the dev cluster |
| (c) Delete the entry and the item when the object is missing, so that the next request misses | No new state | The recorded digest is gone, so the next fill accepts whatever the upstream serves (R-REPO-041 lost); the item's properties and statistics go; races with readers and collection |

**Choice:** (a), with these rules:

- **What is lost.** An entry or item whose blob's `content_status` is `missing` or `corrupt`. The item lookup and the entry
  lookup read the status in the statement they already make (`GetLiveItem`, `ProxyEntryGet`), so no request pays a further
  statement. A read that finds the object gone, the scrub, and the download verification of R-STOR-013 set it. A `missing`
  item is looked for in the store again before it counts as lost, at most once per blob and 30 s ([storage.md](storage.md)
  Q-STOR-153): an object that is back is a hit, with no upstream fetch.
- **Artifacts.** A lost item is a miss of step 4, and of `Engine.Cached` (the cache tier of a virtual repository, the mirror's
  "present"). A request that served a hit and found the object gone at the read, before anything was written (`edge.ServeItem`
  returns the error of the open instead of writing it), reads the item again: marked, it is filled; ok again (another request
  filled it, or the mark did not reach the row), it is served once more and a second loss is the answer. Nothing streams to the
  leader's client while the fetch runs: it is served from the stored item like the requests that waited, so that every outcome
  is a clean answer. The commit keeps the recorded digest (R-REPO-041's commit-time rule): an upstream that now serves other
  bytes repairs nothing, raises `proxy.upstream_digest_changed`, and the answer is the `503`. The vulnerability block answers as
  for any miss (R-VULN-015); `HEAD` asks the upstream's `HEAD` as for a miss.
- **Metadata documents.** A lost document is a miss of step 4, fetched without `If-None-Match` or `If-Modified-Since` (a `304`
  would renew an entry that has no bytes to serve). The fill writes the object again or stores the upstream's new copy. A format
  that keeps the stored document (npm keeps a stored document newer than the upstream's, R-NPM-024) leaves it lost: `503`. A
  `404` or `410` marks the document gone as on any refresh (R-REPO-029), and the answer is the format's `404`.
- **Answers.** A client's request that cannot fill a lost item is answered `503 content_unavailable` with `Retry-After`: the
  cause's (an open circuit's), else 30 s as for any missing object (R-STOR-012). The detail names why it was not filled (the cache
  view, offline, the circuit, the upstream's answer, the populate check, other content upstream). Never the `404` of an uncached
  item (the item exists, R-HA-024) and never `500`. A mirror's pass keeps the outcomes of a miss, which it reads (an open circuit is
  a wait, R-REPO-054). An object store that is unavailable during the fill keeps its own `503 object_store_unavailable`.
- **Observability.** The blob store counts `trove_storage_blobs_damaged_total{status="missing"}` once per blob and logs the
  digest; the engine logs the repository, the path, the digest and the status at most once a minute per item and replica; the
  chart's `TroveBlobMissing` reads the counter ([storage.md](storage.md) Q-STOR-152).
- **Concurrency.** The fill is R-REPO-026's: one flight per replica and the flight lock across replicas.

**Rationale:** the specification had decided the behaviour (R-STOR-027: "a remote repository treats an item with damaged content
as a cache miss and refetches it"); what was open was how a lost object is told apart and what a request answers that cannot fill
it. Marking the blob makes the loss a fact every replica reads in the statement it makes anyway, and the commit protocol's repair
path writes the object again without anything new. The development agent would revisit (b) for remotes whose upstream counts
requests against a quota, if refills after a large loss are seen to exhaust it.

**Applied in the spec:** R-REPO-025 ("Lost content"); [storage.md](storage.md) R-STOR-012 and R-STOR-027; [ha-and-helm.md](ha-and-helm.md)
R-HA-024. Code: `internal/proxy/lost.go`, `internal/proxy/artifact.go` (`Artifact`, `lookupHit`), `internal/proxy/metadata.go`,
`internal/proxy/fill.go`, `internal/proxy/flight.go` (`recheck`), `internal/proxy/fetch.go` (`request`), `internal/api/edge/content.go`
(`ServeItem`). Tests: `TestR_STOR_027_*` in `test/integration/npm/remote_lost_test.go` and `test/integration/nuget/remote_lost_test.go`,
`internal/proxy/lost_test.go`.

### Q-REPO-203 · A document found lost when its bytes are read is filled again once and served only with the digest the request read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine hands a format a `Document` whose bytes are read later, when the format needs them: `HEAD` and conditional
requests are answered from the digest alone. The first read of a lost object therefore happens inside `Document.Bytes`, after the
format may have used the digest: as the validator of its answer, and as the key of the caches that render documents (npm's
documents, NuGet's registrations, the merged documents of a virtual repository).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Bytes` fills the document again under the request path, once, and returns the bytes when the upstream's copy has the digest the request read; another digest is stored and answered `503` with `Retry-After: 1` (chosen) | One refill per read; the next request reads the new document | The first request is answered when the document did not change, which is the common case after a loss; the digest, the validator and the body stay one document | A request whose document changed upstream meanwhile gets a `503` and retries |
| (b) Return the refilled bytes whatever their digest | The first request is always answered | The validators and the digest-keyed render caches would describe another document than the body: one strong ETag, two bodies |
| (c) Read the bytes of every stored document before handing it over | The loss is found before the digest is used | An object-store read for every `HEAD` and conditional request, and for every hit the replica's bytes cache misses (R-STOR-012's principle that these need no object) |
| (d) Answer `503` and let the next request fill | Simplest | Every first request after a loss fails, also when the upstream's copy is the same |

**Choice:** (a). The refill runs for the request as it came (its mode, its populate check, its restore bound); the cache view and
`only-if-cached` never fill and answer the `503`. A context mark by digest keeps the bytes of the refilled document from being filled
a second time; a document the flight reads besides (a discovery document, whose object can be lost too) is filled as usual. A
document that a flight produced (a fill, a `304`, the entry another replica wrote while this one waited) does not fill its bytes: its
loss is the `503`, and the read has marked it for the next request.

**Rationale:** the validator of a document is its digest, and npm and NuGet clients retry a `503` that carries `Retry-After`. The
development agent would revisit (b) with a revision that does not reuse the digest if the `503` of a changed document is seen to
fail installs.

**Applied in the spec:** R-REPO-025 ("Lost content"). Code: `internal/proxy/metadata.go` (`storedDocument`, `bytesOf`),
`internal/proxy/lost.go` (`refillBytes`).

### Q-REPO-204 · Through a virtual repository a remote member's lost cached copy is a miss of the cache tier; a local member's loss is the answer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the second tier of R-REPO-019 serves a remote member's cached artifact without contacting its upstream. A cached copy whose
object is gone is found at the read, after the tier chose it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A `content_unavailable` of the cache tier before the first byte is a miss of the tier: the walk goes on, through the other members' caches to the fetch tier, which fills the item again; a local member's loss ends the walk with its `503` (chosen) | The cache tier is the remote's cache, and a lost item is not in it | A client of the virtual repository triggers the refill as one of the remote does | The walk asks one tier more, as for any miss |
| (b) Both tiers go on | One rule | A local member's loss is data loss; serving another member's file of the same path hides it and can serve other content than the local member published (the reason of R-REPO-023's pin rules) |
| (c) Neither goes on | Simplest | A client of the virtual repository answers `503` until somebody asks the remote directly |

**Choice:** (a). The mark the read sets makes `Engine.Cached` report the item as not cached from then on, so the cache tier is not
chosen again.

**Rationale:** it is the same rule as for the remote itself (Q-REPO-202). The development agent would revisit (b) only if an owner
asks for local members to be backed by remote ones, which is a feature, not a repair.

**Applied in the spec:** R-REPO-025 ("Lost content"). Code: `internal/virtual/artifact.go` (`Artifact`). Tests:
`TestR_REPO_019_ArtifactTiers` (`internal/virtual/artifact_test.go`), `TestR_STOR_027_ACachedNPMItemWhoseObjectIsMissingIsFilledAgainFromTheUpstream`.

### Q-REPO-205 · A lost item that cannot be filled is a 503 everywhere, and a fill that cannot restore it is not repeated for 5 minutes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an independent review of Q-REPO-202 to Q-REPO-204 found five gaps, each confirmed by a test on the code before this decision.
(1) Through a virtual repository a caller who may read the remote member and not fill it got `404` for a cached artifact whose object is
lost: the cache tier dropped the loss, the fetch tier skips a member the caller may not fill, and nothing else was found (the remote itself
answered `503`); npm and NuGet take a `404` as final. (2) An artifact whose upstream now serves other bytes was fetched whole on every
request, each with an unthrottled `proxy.upstream_digest_changed` and an object for collection, and a request that waited on another
replica fetched once more; a document that npm keeps (stored newer than the upstream's) the same. (3) A mirror's pass that joined a
client's refill reported the item present when the refill had left it lost. (4) A lost document beside a negative entry answered the
format's `404`. Such a pair exists: a negative entry is never removed by a fill, and a flight that went on without the cross-replica lock
(R-REPO-026: Valkey unavailable, the wait bound) can record a 404 while another one stores the document, with no gone mark, because the
404's flight did not see the entry.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One answer for a lost item that is not filled, and a backoff for a fill that cannot restore it (chosen) | `Engine.Cached` answers a lost item with `503 content_unavailable` that `proxy.IsLost` tells apart; the remote's request path and the mirror take it as a miss and fill, the virtual cache tier records it as the member's failure; a fill that commits and leaves the item lost starts a backoff of 5 minutes on its replica and releases the flight stored, and a request that waited for it elsewhere and finds the item lost still answers the `503` instead of fetching | Every route answers a lost item alike (R-HA-024: never `404`); the upstream sees at most one whole transfer per replica and 5 minutes, the audit log one event per item and hour per replica | The backoff is per replica: n replicas fetch up to n times per 5 minutes when their requests do not overlap |
| (b) A negative entry for the digest change, shared through Valkey | One fetch per cluster and period | A negative entry means "the upstream answered 404 or 410" (R-REPO-028): the explanation, the gone rules and every miss of the path would read it so, for up to 24 hours, and `notFoundTtlSeconds: 0` turns it off |
| (c) Report the item not cached, as before, and let the virtual repository count it as a miss | No change | Simplest | The `404` of (1) |

**Choice:** (a), with these rules:

- `Cached` (the cache tier, the mirror's "present", the explanation) answers a lost item with the `503`; `formathost`'s remote request path
  and the mirror's `Fill` treat it as a miss and fill it (Q-REPO-202), the explanation reports it as not cached, and
  [internal/virtual](../../internal/virtual/artifact.go) records it, and a hit that fails with `content_unavailable` before the first byte, as
  the member's transient failure (R-REPO-019 step 7): another member may still serve the item, the member's fetch tier fills it for a caller
  who may populate it, and otherwise the walk ends with that `503`. A corrupt object counts alike.
- A fill of a lost item that commits and leaves it lost (R-REPO-041 kept the recorded digest; the format kept the stored document) answers
  `503 content_unavailable` with a `Retry-After` of the rest of 5 minutes, and for those 5 minutes that replica answers so without a fetch
  (`unrepairedBackoff`), for client requests and passes alike. The flight is released stored; a request that waited for it, found the item
  lost before and finds it lost still, answers the same and starts its own replica's backoff. 5 minutes: neither cause changes within
  seconds, and each fetch is a whole transfer; the read's recheck of Q-STOR-153 still serves an object that comes back meanwhile.
- `proxy.upstream_digest_changed` is written once an hour per item and pair of digests on a replica, as the check of announced digests is
  (`trove_remote_integrity_failures_total{reason="digest_changed"}` counts every fetch).
- A pass that joined a client's flight reads the item again when it found it lost: still lost is a failure of the item, never Present.
- A lost document beside a negative entry answers the `503` (step 8), as an artifact does.

**Rationale:** the loss of an object is the store's, not the item's: no route may turn it into "does not exist". The backoff bounds what the
review found unbounded with state the replica holds anyway, without giving a negative entry a second meaning. The development agent would
revisit (b) with an entry of its own kind if the per-replica bound is seen to load an upstream with many replicas.

**Applied in the spec:** R-REPO-019 (step 7), R-REPO-025 ("Lost content"). Code: `internal/proxy/lost.go` (`IsLost`, `unrepairedBackoff`,
`noteUnrepaired`, `backedOff`), `internal/proxy/artifact.go` (`Cached`, `digestChanged`), `internal/proxy/flight.go` (`waitOnce`, `recheck`),
`internal/proxy/fill.go` (`filled`), `internal/proxy/metadata.go` (`beforeFetch`), `internal/virtual/artifact.go`, `internal/formathost/remote.go`,
`internal/formathost/mirror_host.go`, `internal/formathost/virtual_remotes.go`. Tests: `TestR_STOR_027_AVirtualAnswers503ForALostCachedTarballOfAMemberTheCallerMayNotFill`
(`test/integration/npm/remote_lost_test.go`), the four `TestR_STOR_027_*` of `test/integration/proxy/lost_test.go`, `TestR_REPO_019_ArtifactTiers`,
`internal/proxy/lost_test.go`.

### Q-REPO-206 · A merged document keeps listing a version whose cached artifact is lost

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: for a remote member the caller may not populate, the merged document of a virtual repository lists only the versions whose artifact
is cached (R-AUTHZ-008, `ProxyCachedArtifacts`), "so that every version a document lists can be downloaded". The statement does not read
`content_status`, so a version whose cached artifact is lost stays listed, and its download answers `503` (Q-REPO-205).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep listing it: `ProxyCachedArtifacts` ignores `content_status` (chosen) | No change to the statement | A loss of the store does not change what the documents say: a client resolves the same versions before, during and after the loss, gets a `503` that names the cause and retries, and the alert tells the operator | A client of that caller fails on the version until the object is back or a caller who may populate fills it |
| (b) Leave it out while the object is lost | Join `blobs` and filter on `ok` | Every listed version downloads | A client silently resolves another version (or none) because of a store fault, and back again when the object returns or the recheck heals the mark; lockfiles that pin the version fail with "no matching version", a permanent-looking error for a transient fault |

**Choice:** (a).

**Rationale:** R-HA-024 says the item exists and its content may come back; a document that hides it states otherwise, and dependency
resolution that changes with the store's health is worse than a retryable `503`. The development agent would revisit (b) if losses are seen
to last long enough that callers who may not populate cannot work around them.

**Applied in the spec:** R-REPO-025 ("Lost content"). Code: none (`internal/meta/postgres/queries/proxy.sql`, `ProxyCachedArtifacts`, unchanged).
Test: `TestR_STOR_027_AVirtualAnswers503ForALostCachedTarballOfAMemberTheCallerMayNotFill`.

### Q-REPO-211 · The nightly install-time ratio of clients first runs on the large class

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the scheduled nightly run 42194413882 failed `TestMirror_R_REPO_055_ClientsFirst/npm` on its ratio (F-63): the median install with a pass took
161 % of the best install without one (pairs of 14.2/12.2 s, 14.0/22.6 s, 14.0/23.4 s), on the GitHub-hosted runner (4 vCPUs) that Q-REPO-197's gate runs on.
The open requests stayed at 9 of 12 and the pre-emptions grew. The pod logs of the run (cluster-state artifact) split each client tarball request into its
phases: the upstream fetch took 122 ms at the median in every pair, with and without a pass; what grew in the two slow pairs was trove's own work around it,
the wait before the document fetch (p90 97 to 118 ms without a pass, 265 ms with), between the document and the tarball fetch (p90 168 to 211 ms, 451 to
530 ms) and after the fetch, storing and answering (p90 35 to 93 ms, 326 to 380 ms; 12 to 26 s against 82 s summed over the 707 tarballs). That is the work
the pass does itself, hashing, storing and verifying (CPU and the one object-store pod), taken from the installs on a machine that runs the cluster, both
stubs and the clients on 4 vCPUs, the case Q-REPO-197 named as its reason to revisit. On a 17-CPU host the same test measured 97 % and 98 %. The leg ran on that runner class only because it loads the
NuGet clients image, whose four copies outgrew a `large` pod's Docker storage; since Q-HA-102 the image goes into the control-plane node alone, for every set.
The suspicion of F-63 that the miss came from another workflow running at the same time does not hold for this class: each GitHub-hosted job has its own VM.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Run the set `mirror` of the nightly job on `large` (limit 16 CPUs) (chosen) | `runs-on: large` for that matrix row; the gate stays as it is | The ratio then measures what clients first controls, the upstream's slots, as on the 17-CPU host; the disk reason for the hosted runner is gone | `large` pods share their nodes with other legs (F-37); a miss there needs the same phase split before it is called trove's |
| (b) Keep the hosted runner and raise the bound | One number | Green | Hides a real regression; the number is the runner's (Q-REPO-197 (c)) |
| (c) Keep the hosted runner and give the pass a lower CPU share in the product | A CPU priority between processes | Would help a small single node | trove's pass runs in its own pool of `worker` processes, normally on other nodes; R-REPO-055 is about the upstream's fetch budget, and CPU between pods is the platform's (requests and limits) |

**Choice:** (a).

**Rationale:** the requirement's ratio is a statement about the fetch budget, and its failure must point at the fetch budget: the phase split showed the
upstream fetches unchanged and the local work slowed, which on 4 vCPUs is the pass's CPU. The bound and its estimator (the median with a pass against the best
without) are unchanged. The development agent would revisit this if the ratio misses on `large`, where the phase split would then point at trove.
Revisited by [Q-REPO-235](#q-repo-235--the-repositorys-share-of-a-fetch-slot-ends-with-the-upstream-exchange-the-replicas-slot-with-the-fetch)
and [Q-REPO-236](#q-repo-236--the-next-reading-of-clients-first-records-the-databases-waits-the-standbys-lag-the-runners-pressure-the-tests-events-and-the-slot-wait):
the ratio missed on `large` (131 % in nightly run 42258656348), and the phase split pointed at the fetch slot held through a commit that stalls.

**Applied in the spec:** none (CI wiring). Code: [performance.yml](../../.github/workflows/performance.yml) (`e2e-mirror`, the row `mirror`). Elsewhere:
[releasing.md](../developer/releasing.md#ci).

### Q-REPO-212 · A repository is addressed inside its project, and no repository is shared

Status: **Decided by the development agent on 2026-11-06**, on the owner's instruction to address repositories the way that is
technically best, without regard for existing clients ("mach es so wie es technisch am Sinnvollsten ist"). Built.

Context: every repository belongs to exactly one project, and no repository is shared
([Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project)). Today a repository has a key
that is unique in the installation and is addressed by it alone: `/api/<format>/<repoKey>/…`, `/files/<repoKey>/…`,
`/api/v1/repositories/<repoKey>…`; a repository a project administrator creates carries the project key as a prefix
(`web-libs`, `422 project_prefix_required`, [Q-AUTHZ-176](authorization.md#q-authz-176--names-of-a-project-are-prefixed-with-its-key-and-a-global-name-never-takes-a-project-prefix)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The project in the address, the key unique within the project (chosen) | `/api/<format>/<projectKey>/<repoKey>/…`, `/files/<projectKey>/<repoKey>/…`, `/api/v1/projects/<projectKey>/repositories/<repoKey>…`; `web` and `mobile` can each have `npm-remote` | The hierarchy is in the address and the schema, not in a naming rule; short keys; no prefix to reserve or check | Every route, client guide, endpoint and exposure rule, CI mapping and policy selector that names a repository changes |
| (b) A global key with the project key as a required prefix | `/api/npm/web-npm-remote/`; today's rule for project administrators applied to everybody | Fewer route changes | The hierarchy lives in a naming convention; the prefix rules and their overlap checks stay (`project_key_overlap`, H-399) |
| (c) A global key, the project only as a column | As today, project required | No route change | The address says nothing about where a repository belongs; two projects cannot both name a repository `npm-remote` |

**Choice:** (a). No repository is shared: a virtual repository lists only repositories of its own project, a remote
repository of a public registry exists in each project that needs it (the blob store is content-addressed and deduplicates,
INV-4 of [storage.md](storage.md), so the same package fetched by two projects is stored once), a policy selector, a deployment
target and a CI mapping name repositories of one project. A move to another project changes the repository's address.

**Rationale:** the project is the container of its repositories; putting it in the address makes the address say what the
data model says, and takes the prefix convention and its overlap rules out of the repository's name. Clients and existing
data are not a cost before the first stable release ([CLAUDE.md](../../CLAUDE.md)). The same reasoning applies to the names of
a project's permission policies and service accounts, which carry the project prefix today (Q-AUTHZ-176); the owner decided
them the same way ([Q-AUTHZ-209](authorization.md#q-authz-209--a-projects-policies-and-service-accounts-are-named-within-the-project-administrators-keep-instance-wide-ones)).

**Applied in the spec:** R-REPO-001, R-REPO-002, R-REPO-004, R-REPO-009 to R-REPO-016, R-REPO-017, R-REPO-021,
R-REPO-060 and every address of this page; [authorization.md](authorization.md) R-AUTHZ-004
([Q-AUTHZ-216](authorization.md#q-authz-216--repository-selectors-name-the-project-in-instance-contexts-and-a-bare-key-inside-a-project)),
R-AUTHZ-026; [formats/npm.md](formats/npm.md) R-NPM-001, [formats/nuget.md](formats/nuget.md) R-NUGET-001 and
[formats/generic.md](formats/generic.md) R-GENERIC-001 with their examples; [architecture.md](architecture.md) R-ARCH-005,
R-ARCH-008, R-ARCH-011; [api-and-ui.md](api-and-ui.md) R-API-004, R-API-022; [endpoints.md](endpoints.md) R-EP-006
([Q-EP-76](endpoints.md#q-ep-76--a-host-root-binds-a-repository-by-project-and-key-and-the-label-binding-goes)),
R-EP-012; [bastion.md](bastion.md) R-BAS-018; [README.md](README.md) INV-6; [ADR-156](../decisions.md#adr-156--projects-own-their-repositories-members-hold-roles-names-are-scoped-to-the-project).

### Q-REPO-217 · A repository moves to another project through an operation of its own, and only while nothing of its project names it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: every repository belongs to exactly one project, and "a move to another project stays and changes the repository's
address" ([Q-AUTHZ-206](authorization.md#q-authz-206--every-repository-belongs-to-exactly-one-project),
[Q-REPO-212](#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared)). Assignment and
unassignment (`PUT`/`DELETE /projects/{key}/repositories/{repoKey}`, `?move=true`, `?from=`) go with the optional project.
Inside the source project, other objects name the repository by key: the member lists and deployment targets of virtual
repositories, key selectors of project policies, inline grants of the project's CI mappings and, from P1, a shared ownership
domain. After a move they would name a repository of another project, which a project's objects never do (R-AUTHZ-004).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `POST …/repositories/{repoKey}/move` with `{"project": "<target>"}`, refused while the source project names it (chosen) | 409 `repository_in_use` lists the referencing objects; 409 `already_exists` when the target has the key; allowed for `repositories:write-config` or an Owner of both projects | The path states the source, so no `If-Match` or `from` is needed; nothing grants or resolves differently without the people who own those objects acting; the operation is easy to audit and to journal | The caller removes references by hand before a move |
| (b) `PATCH` of `project` on the repository document | A field of the configuration | No new operation | The document's own URL changes under the `PATCH`; an `If-Match` on `config_version` couples a move with every configuration edit (the reason Q-AUTHZ-205 rejected it); a merge patch carries no precondition about references |
| (c) Move and rewrite the references | The same transaction removes the repository from the source project's virtual repositories, policy selectors and CI grants | One call | A virtual repository silently loses a member and a policy a selector; what clients resolve and who may read changes without the owners of those objects seeing it |
| (d) No move: create in the target, copy, delete | As a rename is (R-REPO-001) | No special case | Copies content, loses claims, package owners, findings and mirror state, changes the ID that global key selectors and exposure rules hold |

**Choice:** (a). The plan behind this decision named the conflict code `repository_exists`; the API answers a taken key with
409 `already_exists` everywhere ([Q-REPO-84](#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request),
[Q-API-136](api-and-ui.md#q-api-136--one-rule-for-the-top-level-code-of-an-error)), so the move does too. A virtual repository
moves only without members and deployment target. Objects outside the project that hold the repository by its ID follow it;
what matches by address does not.

**Rationale:** a move changes who may read the repository and how clients reach it; the objects of the source project that name
it were written by people who decided about that repository in that project, and they are told by a refusal rather than
edited behind their back. An Owner of both projects is the narrowest caller who may decide for both sides, and
`repositories:write-config` acts in every project already. The development agent would revisit this if moves become routine
and removing references by hand a burden; then (c) with an explicit `"detach": true`, as R-REPO-013's delete has it.

**Applied in the spec:** R-REPO-060 (new), R-REPO-002 (a move changes the address), R-REPO-004 (`project`);
[authorization.md](authorization.md) R-AUTHZ-004 (key selectors follow the ID), R-AUTHZ-026 (the Owner's table, the audit
event), R-AUTHZ-031 (the journal); [api-and-ui.md](api-and-ui.md) R-API-007, R-API-022; [bastion.md](bastion.md) R-BAS-015;
[endpoints.md](endpoints.md) R-EP-006, R-EP-012. Q-AUTHZ-179 and Q-AUTHZ-205 are marked superseded.

### Q-REPO-232 · An Owner's move is refused while a global policy names the repository by key

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a key selector of a global policy is stored by the repository's ID and keeps naming the repository when it moves
([Q-AUTHZ-216](authorization.md#q-authz-216--repository-selectors-name-the-project-in-instance-contexts-and-a-bare-key-inside-a-project)),
and [Q-REPO-217](#q-repo-217--a-repository-moves-to-another-project-through-an-operation-of-its-own-and-only-while-nothing-of-its-project-names-it)
checks only what the source project names. An Owner of two projects who moves a repository so carries a grant an administrator
of trove wrote for one project into the other, where its other Owners neither see the global policy nor can remove it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The grant follows, as for an administrator | Nothing changes | No refusal | An Owner moves an administrator's grant into a project whose Owners did not agree to it and cannot undo it |
| (b) Refuse every move while a global key selector names it | The kind `global_policy` for every caller | One rule | An administrator who wrote the grant moves the repository with it on purpose, and is the one who could remove it anyway |
| (c) Refuse an Owner's move without `repositories:write-config` while a global key selector names it (chosen) | 409 `repository_in_use`, kind `global_policy`, the policy's name; an administrator moves it | The grant moves only by the hand of someone who may change it; the Owner learns whom to ask | The Owner learns the name of a global policy on a repository of its project, not its entries |
| (d) Drop the global grant from the moved repository | The move deletes the selector or the policy's hold on the ID | The Owner is not blocked | A move would rewrite an administrator's policy behind its back, which Q-REPO-217 refused for project objects |

**Choice:** (c). The handler tells the service whether the caller holds `repositories:write-config`
(`MoveRepository(…, restricted)`); a restricted move lists, after the references of the source project, every global policy
with a key selector on the repository's ID as `{"kind": "global_policy", "name": "<policy>"}`. A pattern or class selector is
stored without the ID and refuses nothing. An exposure rule, which also holds the repository by ID
([endpoints.md](endpoints.md) R-EP-012), refuses nothing either: it is a ceiling and grants no one anything.

**Rationale:** whoever moves a grant into a project should be someone who may change that grant; an administrator of trove is,
an Owner is not. The policy's name is what the Owner gives the administrator, and it tells no more than that a global policy
names its own repository. The development agent would revisit this if Owners get a read of the global policies that touch their
projects, or if exposure rules start to grant.

**Applied in the spec:** R-REPO-060 (the refusal, the kind, the criterion); `api/openapi.yaml` (`moveRepository`,
`RepositoryReference.kind`).

### Q-REPO-233 · A move is recorded in the audit of both projects

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an audit event names one project (`audit_event.project`, the `project` filter of `GET /api/v1/audit/events`,
[authorization.md](authorization.md) Q-AUTHZ-209). `repository.moved` named the source project only, so the audit of the
target project showed a repository that arrived without any event: no `repository.created` there, and no move.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The source only (as before) | One event, `project` the source | No new type | The target's audit cannot tell where a repository came from |
| (b) The target only | One event, `project` the target, the new name | The repository's history continues under its new name | The source's audit loses a repository without an event |
| (c) The same type twice | `repository.moved` in each project | One type to filter | A filter on the type counts every move twice; the journal entry, which the restore reads once, is on one of two equal events |
| (d) Two types, one per project (chosen) | `repository.moved` in the source under the former name, journaled; `repository.moved_in` in the target under the new name, without a journal entry; equal details | Each project's audit shows the move once; the type `repository.moved` still counts every move once and carries the one journal entry | One more event type |

**Choice:** (d). Both events are written in the move's transaction with the same details (the repository's ID and key, both
projects by ID and key, the epoch, the configuration version); `repository.moved_in` has the subject and the `repository` column
of the new name. The restore and the Bastion's feed read the journal entry of `repository.moved` only.

**Rationale:** the audit of a project should explain every repository it holds and every one it lost, and a journal entry is a
fact about the installation, recorded once. The development agent would revisit this if `audit_event` gains several projects per
event; then one event would serve both views.

**Applied in the spec:** R-REPO-060 (the events); [authorization.md](authorization.md) R-AUTHZ-026 (the project's events), the
event table of R-AUTHZ-029.

### Q-REPO-234 · A member write locks its repositories in the database, so the move guard sees every order

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: F-68 asked whether the guard of a move (`AI007`) sees a member write that does not take `LockRepositoriesByKeys …
FOR SHARE`. Reproduced against PostgreSQL with two connections and raw SQL. A member write in flight makes the move wait:
`project_id` is a column of the unique index `(project_id, key)`, so its update takes the strongest row lock, which conflicts
with the `FOR KEY SHARE` that the member's foreign key takes; the move then re-runs its guard and refuses (`AI007`). The
other order was open: the same-project guard of the member row (`AI006`, migration 00024) read the repository's project
before the foreign key waited, so a move in flight was invisible to it, the write passed on the old project and landed
next to a repository of another project once the move committed. `policy_selector_project_guard` (`AI007`) read the project
of a key selector's repository the same way, and the same test reproduced it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A test pins every member write path to the locks | A test reads the Go write paths | No schema change | Holds only for today's Go paths; a new path or a manual write is unguarded, and the order that failed is the one the guard itself got wrong |
| (b) A separate trigger that locks the member repository | A `BEFORE` trigger per member table | Explicit | Fires before or after the same-project guard by trigger name, so the order needs a naming rule |
| (c) The same-project guard locks first (chosen) | `virtual_same_project_guard` locks the referenced repository and the virtual repository `FOR SHARE` in ID order, then reads both projects | One indexed row lock per row and no new trigger; the read follows the lock, so both orders end in one state; also covers pin members and deployment targets, which share the function | The member write now waits for an uncommitted move of either repository |

**Choice:** (c), migration 00045. `FOR SHARE` rather than `FOR KEY SHARE`, so the lock conflicts with every update of the row,
not only with a key update. Locking in ID order keeps two writers from waiting on each other. `policy_selector_project_guard`
locks the selected repository `FOR SHARE` before it reads its project; the project of a policy never changes (`AI003`), so its
row is not locked.

**Rationale:** the database is the only place that sees every writer, and the defect was an order inside the guard, which a
test of the Go paths would not have found. Revisit when another guard reads a referenced row's attributes before its
foreign key has waited.

**Applied:** R-REPO-060 (the second line); migration `00045_virtual_member_locks_repository.sql`;
`TestR_REPO_060_AMemberWriteAndAMoveOfItsRepositoryAreSerialisedByTheDatabase` (member and key selector writes, both orders)
([test/integration/postgres/move_guard_race_test.go](../../test/integration/postgres/move_guard_race_test.go));
[database.md](../developer/database.md), [delegated-administration.md](../security/delegated-administration.md).

### Q-REPO-235 · The repository's share of a fetch slot ends with the upstream exchange, the replica's slot with the fetch

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `TestMirror_R_REPO_055_ClientsFirst/npm` missed its bound on the `large` class (131 % in nightly run 42258656348, F-63), the case
[Q-REPO-211](#q-repo-211--the-nightly-install-time-ratio-of-clients-first-runs-on-the-large-class) named as its reason to revisit. The pod logs of
that run, joined by `trace_id`, split each client tarball fill into the upstream exchange, the part of the commit before its last transaction
(spool, the blob row's lookup, the object write) and the last transaction (blob row, item, cache entry, the populate and `artifact.cached` events,
commit). The upstream answered in 122 ms at the median in all six installs, every attempt the first; the part before the last transaction stayed at
2 ms at the median; the last transaction's 90th percentile rose from 15 to 39 ms without a pass to 81 and 104 ms in the two slow loaded pairs, in
windows of 3 to 4 s on all three `packages` replicas at once. The fetch slot is held through all of it: `fetcher.run` releases the slot after
`inSlot` returns, and `inSlot` includes the commit ([internal/proxy/fetch.go](../../internal/proxy/fetch.go)); a background request holds its slot
until `Fill` or `Metadata` returns ([internal/proxy/fill.go](../../internal/proxy/fill.go), [metadata.go](../../internal/proxy/metadata.go)). With
`maxConcurrentFetches` 8 on three replicas the cluster has 9 client slots, and `npm ci` keeps about 15 requests in flight, so a slower commit
lowers the whole install's throughput (50 down to 21 to 26 requests per second in the windows). R-REPO-040 says what each limit protects:
`limits.maxConcurrentFetches` the upstream, `proxy.max_concurrent_fetches` the replica.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep both until the fetch ends, as built | The slot covers the store | One release point; nothing changes | The upstream's budget is spent waiting for the database: a commit that stalls throttles every fetch of the repository on the replica although the upstream is idle, and a pre-emption during the commit throws away a whole transfer that the pass repeats |
| (b) Return the slot after the object write, before the last transaction | A release inside the commit protocol | Covers the transaction that stalled | The object write sits inside the rounds of `blob.Store.CommitAll`, which may write again in a later round, so the release would live in the blob store's protocol; the write took 2 ms at the median in the same run, so (b) gains nothing over (c) for that cost; the upstream was done before it |
| (c) Return the whole slot when the upstream exchange ends | One release point, earlier | Simplest; the upstream's bound holds | The replica's limit no longer covers the store: a body up to `storage.spool.memory_threshold` (1 MiB) is spooled in memory and not charged to the spool budget (R-STOR-004), the flight hold's extension goroutine is bounded by that limit ([architecture.md](architecture.md) Q-ARCH-196), and a fetch whose client went away runs on detached; during a stall nothing per replica would bound them but the requests in flight |
| (d) Return the repository's share and the background count when the upstream exchange ends; keep the replica's slot until the fetch ends (chosen) | Each limit is held while what it protects is in use | The upstream's budget counts upstream exchanges only, so a slow commit no longer keeps the next fetch from the upstream; every bound the replica's limit gives stays (Q-ARCH-196, the in-memory spools); a fetch that is storing is not pre-empted, so no finished transfer is thrown away | Two release points the code must keep apart, the background slot's included; the database may see more commits of one repository at once, bounded by the requests in flight and the replica's limit |
| (e) Raise the budget of the test's remotes, or make the commit cheaper | Configuration, or a change of the transaction | No change of the engine | The first hides the coupling, which any installation with a slow database meets; the second needs the stall's cause, which no reading has named yet ([Q-REPO-236](#q-repo-236--the-next-reading-of-clients-first-records-the-databases-waits-the-standbys-lag-the-runners-pressure-the-tests-events-and-the-slot-wait)) |

**Choice:** (d).

- The **upstream exchange** of a fetch ends when its response body has been read to its end into the spool, when the response has no body
  the fetch reads (a `304`, a `404` or `410`, a refusal before the first byte), or when the request failed or was cancelled. Then the
  repository's share is returned, and for a background fetch its background count, and the fetch leaves the set of running background
  fetches that a client fetch or the signal `mirror_yield` pre-empts. Returning the share does not end the context the fetch runs under.
- The replica's slot (`proxy.max_concurrent_fetches`) is returned when the fetch ends: after verification, the format's inspection, the commit,
  and the entry, negative entry or gone mark it writes. A request that stores nothing (a `HEAD`, a probe, a pass-through) returns both when it ends.
- A background request's slot is taken for its item (Q-REPO-179). A discovery document resolved under the same slot before the item returns
  nothing; the item's exchange returns the share.
- A fetch that was pre-empted gave its whole slot up at the pre-emption (as before); the end of its exchange, if it comes, does nothing.
- The flight still ends after the commit: followers and waiters (R-REPO-026) are answered from the committed entry, and the leader's own client
  still gets the withheld tail only after the commit (R-REPO-041).

**Rationale:** the upstream is done with a fetch once its body has been read; holding the upstream's share through trove's own commit makes the
upstream's budget a function of the database's latency, which is the coupling the split of F-63 measured, and it makes a pre-emption cancel a
transfer that had already completed. The replica's limit protects work that goes on until the commit ends, so it stays with the fetch. (d) does
not make a stalled commit faster: a request in a stall window still waits for its own commit. In such a window the install is then bounded by
npm's requests in flight over each request's own time, not by the 9 slots over a slot time that includes the commit; how much of the 131 % that
recovers is what the next nightly reading measures, with the records of Q-REPO-236. The development agent would revisit (d) if that reading
names contention between the commits of one repository as the stall (more of them at once would deepen it), or if fetches that are storing exhaust
the replica's limit (`trove_remote_fetch_queue_wait_seconds` grows with the replica's limit the exhausted one).

**Applied in the spec:** R-REPO-025 (step 10), R-REPO-040 (the two parts of a slot, two acceptance criteria), R-REPO-055 (the end of the exchange,
the unit criterion). Code: [internal/proxy/slots.go](../../internal/proxy/slots.go), [fetch.go](../../internal/proxy/fetch.go),
[artifact.go](../../internal/proxy/artifact.go), [metadata.go](../../internal/proxy/metadata.go), [background.go](../../internal/proxy/background.go).
Tests: `TestR_REPO_040_TheRepositorysShareIsReturnedWhenTheExchangeEnds`, `TestR_REPO_040_TheReplicasSlotIsHeldUntilTheFetchEnds`,
`TestR_REPO_055_ABackgroundFetchPastItsExchangeIsNotPreempted` (unit); `TestR_REPO_040_ABlockedCommitDoesNotHoldTheUpstreamShare`,
`TestR_REPO_040_TheUpstreamBoundHoldsWhileEveryCommitIsHeld`, `TestR_REPO_055_ABackgroundFillWhoseCommitIsHeldIsNotPreempted`,
`TestR_REPO_055_TheDiscoveryDocumentUnderABackgroundSlotKeepsTheItemsShare` (integration). Elsewhere: [developer/proxy.md](../developer/proxy.md),
[developer/mirror.md](../developer/mirror.md), [manual/admin/repositories.md](../manual/admin/repositories.md).

### Q-REPO-236 · The next reading of clients first records the database's waits, the standby's lag, the runner's pressure, the test's events and the slot wait

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the split of F-63 (Q-REPO-235) names where the extra time of a loaded install went, not why: the last transaction of the cache commit
stalls for 3 to 4 s on all three `packages` replicas at once, while the read-only transaction and the object write before it do not; it happens
without a pass too, less. The artifacts of the readings hold no database, disk or CPU measurement; the cluster state is taken only when the job
fails and after `TestMain` switched the profile back, which removes the pool's processes and with them the pass's logs; and the test logs a pair
only at its end, so a stall cannot be placed against what the test was doing. Open, in order:

1. Is the time spent in PostgreSQL (a statement, the commit) or between the statements, in trove or on the way?
2. If in PostgreSQL, what does it wait for? The dev cluster replicates synchronously (`synchronous: {method: any, number: 1}` in
   [postgres.yaml](../../deploy/dev/postgres.yaml)), so every committing write waits for the standby's flush; the primary's own WAL flush, a lock
   between commits and CPU are the other candidates. A read-only transaction waits for neither flush, which fits the split.
3. Was the machine short of CPU or IO at those seconds? The kind nodes, both stubs and the clients share one runner.
4. What were the pass and the test doing then (a pass's start, a repository's deletion between pairs and the collection after it)?
5. After Q-REPO-235, do client fetches still queue for the upstream's share in the windows?

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One instrument per open question, in the dev cluster and the clients-first npm subtest (chosen) | The table below | A miss names its cause, or narrows it to the candidates left | Several small additions to the harness |
| (b) Every instrument the analysis listed | (a) and `log_lock_waits`, `log_autovacuum_min_duration`, a log field or histogram of the commit transaction's duration | Nothing is missing | `log_lock_waits` reports only waits longer than `deadlock_timeout` (1 s) while the stalls are about 90 ms, and lowering `deadlock_timeout` changes the deadlock detector for every set; the other two repeat what (a) records |
| (c) Nothing new: read the next misses with what exists | | Nothing to build | Three readings named the transaction and none its cause; the next one would leave the same gap |

| What is recorded | Where | The question it answers | Built |
|---|---|---|---|
| `log_min_duration_statement: 50ms` with `log_parameter_max_length: 0` | [postgres.yaml](../../deploy/dev/postgres.yaml), every profile | 1. A statement or `COMMIT` of the role `trove` above 50 ms in the primary's log means the server saw the stall; none rules out one slow statement only: the time is then between statements (`idle in transaction` in the sampler) or spread over several (`active` backends). The setting is made on the role `trove` (`ALTER ROLE`, in the bootstrap's `postInitApplicationSQL`), which survives the `DROP DATABASE` of the upgrade set, and the operator's own statements run as `postgres`, so they never land in the log; `log_parameter_max_length` stays cluster-wide. 50 ms lies between the transaction's median idle (10 ms) and in a window (90 ms). No bound value is logged: the log goes into a CI artifact, and a value can be a token's hash | yes |
| `track_wal_io_timing: on` | postgres.yaml | 2. The WAL write and fsync times, which PostgreSQL 18 keeps in `pg_stat_io` and only with this switch | yes |
| A sampler on the primary every 250 ms, one `psql` session for the clients-first npm subtest, one JSON line per sample: `pg_stat_activity` (the sessions that are not idle and not waiting for work, wait event type `Activity`) counted by backend type, state, wait event type and wait event and the first 48 characters of the query; `pg_stat_replication` (sync state, write, flush and replay lag, bytes behind); `pg_stat_wal`; the rows of `pg_stat_io` that wrote, evicted or synced (the WAL rows among them); `pg_stat_checkpointer` | `test/e2e`, `.out/e2e/clients-first-npm-pg.jsonl` | 1 and 2. `SyncRep` waits with a flush lag point at the standby, `WALWrite` or `WALSync` with a growing fsync time at the primary's disk, `Lock` waits at commits that contend, active backends without a wait at CPU, `idle in transaction` at trove between statements; an autovacuum worker or a checkpoint in the window shows too | yes |
| The runner's pressure every second: `/proc/pressure/cpu`, `io` and `memory` (`some` and `full`, their totals) and the test process's cgroup `cpu.stat` and `cpu.pressure` | `test/e2e`, `.out/e2e/clients-first-npm-pressure.jsonl` (what the test process sees: the machine's `/proc/pressure`, the job's cgroup) | 3 | yes |
| The cgroup that holds the kind nodes every second: a helper container of the pinned tools image through the job's Docker daemon (`--cgroupns=host`), `cpu.stat` (use, throttling), `cpu.pressure` and `io.pressure`; with a fixed name per cluster that the next start removes first and that is removed when the readings stop; a run that is killed leaves it, and its loop ends after an hour. On the `large` runner the nodes run inside the job's Docker-in-Docker container, so the test process's cgroup above is not theirs | `test/e2e`, `.out/e2e/clients-first-npm-nodes.jsonl` | 3 | yes |
| The test's events with their wall time: each pair's creates, the installs' start and end, the pass's request and start, the drop and the end of the pass's halt | `test/e2e`, the test log and `.out/e2e/clients-first-npm-events.jsonl` | 4, and the time axis every other record is read against | yes |
| The cluster's logs when one loaded install is above the bound, whether the bound is enforced or logged | `test/e2e` runs `deploy/dev/cluster-logs.sh` into `.out/e2e/clients-first-npm-cluster-logs/` before the next subtest | 4: the logs of the pool's processes, which run the pass, are kept while they exist, and a per-change run that only logs the ratio leaves them too | yes |
| `slot_wait_ms` on the upstream record of a remote fetch (R-REPO-047) | the engine | 5, and for an operator: queued for the budget, or a slow upstream | yes |
| `log_lock_waits` | | a lock wait's holder | no: see (b); the sampler shows `Lock` waits, and their holder is the next question only if it does |
| `log_autovacuum_min_duration` | | an autovacuum run's statistics | no: the sampler shows an autovacuum worker and its table in a window; its statistics are not the question |
| A log field or histogram of the commit transaction's duration | | its duration | no: it is measurable already. The audit event of a client fill takes the transaction's `now()` as `occurred_at` and its log line is written after the commit, so their difference is the transaction's duration as trove saw it, which the split used; its cause is what is missing |

**Choice:** (a). The two cluster-wide PostgreSQL settings and the role's slow-statement setting apply in every profile: the dev cluster has one PostgreSQL, made by `make dev-up` before any profile,
and a slow-statement log helps the cluster state of every set. The samplers, the events and the kept logs belong to the clients-first npm subtest
alone. R-REPO-055's bound and its estimator (the median install with a pass against the best without) are unchanged.

**Rationale:** each record answers one of the open questions, and together they tell the candidates apart: the standby, the primary's disk, a
lock, CPU in the database, the machine, or trove between statements. The development agent would revisit (a) when a reading names the cause
(the records that did not contribute are then removed with the fix), or when a miss leaves the question open with these records: then
`log_lock_waits` with a lower `deadlock_timeout` in the profile `mirror`, or a sampler on the standby.

**Applied in the spec:** R-REPO-047 (`slot_wait_ms`), [operations-and-observability.md](operations-and-observability.md) R-OPS-011. Code:
[deploy/dev/postgres.yaml](../../deploy/dev/postgres.yaml), [test/e2e/mirror_clients_test.go](../../test/e2e/mirror_clients_test.go),
`test/e2e/mirror_readings_test.go`, [internal/outbound/factory.go](../../internal/outbound/factory.go) (`Call`),
[roundtripper.go](../../internal/outbound/roundtripper.go). Tests: `TestR_OPS_011_TheSlotWaitIsARemoteRecordsField` (unit),
`TestR_OPS_011_TheUpstreamRecordOfAFetchCarriesItsSlotWait` (integration). Elsewhere: [developer/e2e.md](../developer/e2e.md#the-mirror-tests),
[developer/local-profile.md](../developer/local-profile.md), [developer/observability.md](../developer/observability.md).

### Q-REPO-253 · The e2e criterion "pre-emptions grew" is proved with forced contention, not by the install of the ratio

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: since the repository's share of a slot ends with the upstream exchange (Q-REPO-235), the clients of `npm ci` rarely find the slots full,
so they rarely pre-empt: Test and Release run 42445876912 measured the ratio at 99 % (82 %, 97 %, 97 %) with `trove_mirror_preempted_total` at 0 over
the three pairs, and the criterion "grew" of R-REPO-055 failed although the rule it checks is intact. A client that finds no slot still
pre-empts a background fetch that is in its upstream exchange (Q-REPO-235 only exempts one that is storing), which the unit and integration
tests of R-REPO-055 prove with contention they force. The pairs of the ratio cannot force any: their stub delays, the number of requests in
flight and the settings are the estimator's, and a change there changes what is measured.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep "grew over the three pairs" | As built | No change | Fails whenever the install is fast enough that no client waits: it asserts the opposite of what the split achieves |
| (b) Slow the pass's upstream requests in the pairs, or lower the pairs' slot budget | Contention by configuration | The assertion stays where it is | The stub cannot tell the pass from the clients, so the clients' fetches slow too and the ratio is inflated; a lower budget changes the bound |
| (c) A second part of the e2e test forces the contention on its own remote; the pairs log the counter (chosen) | `TestMirror_R_REPO_055_ClientsFirst/preemption`: a pass over 84 names runs while the stub holds a tarball 1.5 s (a delay keyed by the kind of request for the whole stub, held while the subtest runs; the pass's two background fetches sit in their exchange), then 24 clients ask the three replicas for 24 other tarballs of the remote at once (the replicas' slots hold 9) | Deterministic: the clients wait, raise `mirror_yield`, and the pool's processes pre-empt; the estimator, the bound and the pair structure are untouched; the criterion "`trove_mirror_preempted_total` grew" is asserted as specified | A second remote and about a minute of the set |
| (d) Drop the criterion | Pre-emption is proved by the unit and integration tests | Simplest | The e2e would no longer show the rule across processes and replicas, which only kind has |

**Choice:** (c). The pairs log `trove_mirror_preempted_total` and assert nothing about it; the subtest `preemption` asserts that it grew and that every
client fetch was answered 200.

**Rationale:** the criterion was written when a slot was held through the commit, so that contention was common; it checks that the client-first
rule works across processes, which contention alone shows. The development agent would revisit (c) if the pairs show a client waiting behind a
background fetch's exchange (`trove_remote_fetch_queue_wait_seconds` of the repository above the pass-free pairs'), which would be a regression of
the rule that this subtest does not see.

**Applied in the spec:** R-REPO-055 (the e2e criterion). Code: [test/e2e/mirror_clients_test.go](../../test/e2e/mirror_clients_test.go)
(`clientsFirstPreemption`). Elsewhere: [developer/e2e.md](../developer/e2e.md#the-mirror-tests).

### Q-REPO-245 · A narrowing update records `repository.narrowed` beside `repository.updated`, with only the parts that narrowed

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: F-72. A repository update that narrows what the repository serves (fewer include patterns, more exclude patterns, a
virtual member taken out, a stronger `vulnerabilities` member) was recorded as `repository.updated` without a journal entry, so a
restore to a point before it served what the update withheld. One update can narrow one part and widen another (a member out,
another in).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Flag `repository.updated`, entry with the whole document before and after, replayed by the update rule of R-HA-054 | One event; the comparator decides | No new event name | An update that narrows one part and widens another is neither a narrowing nor contained in its before-state, so it is suspended whenever the restored state differs from the after-state: almost every mixed update would disable its repository; every description change ships a document |
| (b) A dedicated `repository.narrowed` beside `repository.updated`, written only when the comparison finds a narrowing part, with only those parts (chosen) | The precedent of `repository.disabled` (Q-AUTHZ-238) and `authz.settings.changed` | Each part is a removal the replay re-applies to whatever state it finds; the widening half of a mixed update is never replayed; the audit trail can be filtered for narrowings; the Bastion's feed reads one event (Q-BAS-146) | One more event name; the writer compares two states |
| (c) Not journaled | — | Nothing to build | The gap of H-569 stays |

**Choice:** (b). The writer is `recordUpdate` in `internal/repository/service.go`: it has the stored and the new document and,
for a virtual repository, the stored and the new rows (`curRel`, `rel`, by repository ID).
**Rationale:** a removal can be re-applied to any state without widening it, which a whole after-state cannot; the parts are
small, and the entry exists only when there is something to replay. The development agent would revisit this if a member gained
narrowing parts that cannot be expressed as a removal.
**Applied:** R-REPO-061, R-REPO-005, R-REPO-017; [ha-and-helm.md](ha-and-helm.md) R-HA-054 (the table);
[operations-and-observability.md](operations-and-observability.md) R-OPS-016; [bastion.md](bastion.md) R-BAS-015.

### Q-REPO-246 · Patterns, auto claims, virtual members, pin rules and the vulnerabilities member are journaled; upstream, storage and format settings are not

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: the repository document has more members that a change can tighten (R-REPO-004, R-REPO-024). R-HA-054 journals what
bears access and what withdraws a version.

| Member | Journaled | Why |
|---|---|---|
| `includePatterns`, `excludePatterns` | yes | They decide which names a repository serves, fetches and takes part in virtual resolution with (R-REPO-005) |
| `virtual.members` | yes | A member out takes its content out of the virtual repository and out of a token scoped to it (R-AUTHZ-009 rule 2, Q-AUTHZ-163) |
| `virtual.pinning`, `local.autoClaim` | yes | They keep a name from falling through to a remote member (R-REPO-023) |
| `vulnerabilities` | yes | More blocking withholds versions (R-VULN-002, R-VULN-015) |
| `disabled`, `requireAuthentication`, `hideExistence`, `local.packageOwnership`, `remote.cacheFillAction` | by R-AUTHZ-031 | Their own events |
| `virtual.deployTarget` | no | A write through the virtual address runs with the target's permissions (R-REPO-021): the target routes, it grants nothing |
| `priorityMember`, `virtual.mergedMetadataTtlSeconds`, `description`, `adminNotes`, `local.caps` | no | Order, freshness, text and retention: none changes what a caller may reach |
| `remote` (URL, extra hosts, credentials, integrity, availability, mirror) and `storage` | no, H-569 | They decide how trove talks to an upstream and what it accepts from it and from clients, not what a caller may reach; re-applying them cannot be checked against the restored upstream state |
| format members (`npm`, `nuget`) | no, H-569 | The format pages own them (R-REPO-004); a format page that withholds versions through a member flags its own event |

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The members of the first table that bear access or withhold content (chosen) | As above | Every journaled part has an order or a removal the replay can apply without widening | The upstream and format settings come back with a restore (H-569) |
| (b) Every member that can be ordered | Upstream hardening too (`forwardAuthOnRedirect` off, an extra host removed, `checksumPolicy: require`) | Fewer reverts | Orders for settings that bear no access, each with its own replay rule; an extra host removed while the URL changed has no meaningful replay |

**Choice:** (a).
**Rationale:** the journal re-applies decisions about access and content; how trove reaches an upstream is configuration that
an operator re-checks after a restore, and H-569 names each case. The development agent would revisit this when an upstream
setting starts deciding who may read what, or when a format page asks for its member.
**Applied:** R-REPO-061 (the not-journaled list); [backup-and-restore.md](../security/backup-and-restore.md) H-569.

### Q-REPO-247 · A replayed include list keeps the patterns both lists hold, and none left disables the repository

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: an include list admits the union of its patterns, and an empty one admits everything (R-REPO-005). The entry carries the
list after the update. The restored list can be empty, the before-state, or another list (a pattern added after *t* and not
journaled, because adding one widens).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Remove the patterns the update removed | The entry carries the removed patterns | Exact when the restored list is the before-state | An update from an empty list removes nothing and is lost; removing the last pattern widens to everything |
| (b) Set the journaled list | As an update would | Simple | A pattern the restored list lacks would be added: widening |
| (c) Keep the patterns both lists hold; an empty restored list takes the journaled list; none left disables the repository (chosen) | The intersection in the folded form of the format's name rule, where a restored pattern that a journaled one covers counts as held by both (`[@acme/ui-*, @x/*]` to `[@acme/*]` narrows to `@acme/ui-*`) | Never admits what either list does not; covers the update from an empty list; the empty intersection fails closed | Two patterns that match the same names but are spelled differently (`a*` and `a**` for flat names) count as different and narrow more |

**Choice:** (c).
**Rationale:** the intersection is the one list that is provably no wider than both the restored and the journaled one, and an
empty one cannot be written because it would admit everything. The development agent would revisit this if include lists gained
a way to admit nothing.
The writer does not journal an include edit whose old patterns are all covered by the new ones (`[@acme/ui-*]` to `[@acme/*]`, a change of case): that edit widens, and a replay would otherwise intersect the restored list with the wider one. Containment is proved for the same folded pattern and for a literal with one trailing wildcard; anything else counts as a narrowing, which the intersection keeps safe.
**Applied:** R-REPO-061 step 1; `includeNarrowed` in `internal/repository/narrowed.go`.

### Q-REPO-248 · A replayed member removal that would take a `members` claim out of effect beside another member disables the virtual repository

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: a claim of scope `members` is in effect only in the virtual repositories that contain the claiming repository
(R-REPO-023). Taking a local member out of a virtual repository therefore takes its claims out of effect there, and the claimed
names resolve from the remaining members: a remote one, and also another local one that does not hold them, whose name resolution
the claim had kept off the names. A journal object can be forged by a holder of the runtime S3 credential (R-HA-054), and the
entry cannot say whether another member's claim covers the same names.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Remove the member as recorded | The live change's effect | Exact for a genuine entry | A forged removal of a claiming member opens dependency confusion for every name it claims |
| (b) Remove it and add an empty pin rule per claim | The names answer 404 | Keeps the rest of the repository serving | The replay invents configuration that no administrator wrote |
| (c) Disable the virtual repository instead, when the member holds a `members` claim and a remote member stays (first choice) | The fail-closed step that R-HA-054 asks for where removing one object widens another, as Q-CI-11 does for mappings | Nothing reaches the remote member; the output names the entry for review | A forged removal of a claiming member gives its names to another local member that stays |
| (d) Disable the virtual repository instead, when the member holds a `members` claim and any member stays (chosen, after the security review of F-72) | As (c) for every member that stays | Nothing a claim reserved reaches any member that did not hold it | A genuine removal of a claiming member from a virtual repository that keeps another member is disabled until reviewed; only the removal of the last member applies |

**Choice:** (d). Claims of scope `global` stay in effect without the member, so they do not trigger it.
**Rationale:** the removal is a narrowing for the member's content and a widening for the names it claims; only the disable is
both. The development agent would revisit this if the replay could tell whether another member's claim covers the same names, which would let the removal apply where the staying member holds them.
**Applied:** R-REPO-061 step 4, R-REPO-017.

### Q-REPO-249 · A replayed pin rule is added only where no claim and no other rule overlaps it

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: a pin rule decides the members consulted for its names and takes precedence over claims; it is the only way a claimed
name reaches a remote member (R-REPO-023). Adding a rule narrows a name only when its members are among those consulted before,
which depends on the claims and rules of the restored state.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Add the rule as recorded | The live change | Simple | A rule that names a remote member for a claimed name widens; a forged one could route any claimed name to a remote |
| (b) Not journaled | — | — | A rule added after *t* to keep a scope internal is lost |
| (c) Add the rule when no claim in effect and no pattern of another rule overlaps its pattern, with its members reduced to the restored members; otherwise disable the repository (chosen) | Without an overlapping claim or rule every member is consulted for the name, so any subset narrows | Provably narrowing where applied, fail-closed elsewhere; the overlap test is `ClaimPattern.Overlaps` | A genuine rule that refines a claim disables the repository until reviewed |

A member taken out of a rule is always a narrowing (the rule still selects the name) and is applied as recorded; a pattern
held by a rule of the restored repository has its members reduced to the journaled ones.
**Choice:** (c).
**Rationale:** the replay applies a rule only where it can show that the rule consults no member the name did not reach before.
The development agent would revisit this if the replay evaluated the consulted members per name.
**Applied:** R-REPO-061 steps 5 and 6, R-REPO-023.

### Q-REPO-250 · Every claims change carries a journal entry, and a replayed manual claim that overlaps another repository's claim disables its repository

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

Context: a claim keeps a name from falling through to remote members (R-REPO-023). A restore to a point before an auto claim loses
the version that created it and the claim, so the name reaches a remote member, which is the case dependency confusion uses: the
clients know the name. A claim that overlaps another repository's claim at equal or higher specificity takes names from it, which
a forged entry must not do (R-HA-054).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Flag `repository.claims_changed`; every event carries `added` (created, or widened to `global`); a removal-only change carries an empty list (chosen) | The precedent of Q-AUTH-18 and Q-AUTH-198: a type that cannot be written without its entry cannot drop a narrowing | One event for the API and the auto claim; the auto claim of every first publish is journaled | One journal object per first publish of a name |
| (b) A dedicated `repository.claims_added` | Only additions | No empty entries | One more event beside one that already lists `added` |
| (c) Manual claims only | — | Fewer objects | Loses the auto claim, the case that matters most |

The replay of a manual claim that overlaps a claim of another repository in effect together with it creates the claim and
disables the claiming repository: a claim of a disabled repository stays in effect (`inEffect` in `internal/repository/routing.go`
reads membership, not state), so the overlapping names answer 404 instead of moving to it. An auto claim follows the rule of the
first publish and is not created where another repository's claim covers the name. The limit of 1000 claims does not stop a
replayed claim: a claim only narrows, and a dropped one leaves a name unclaimed, the case dependency confusion uses. The entry of
an auto claim carries exactly one claim, and a repository that ends above the limit is noted in the replay output.
**Choice:** (a).
**Rationale:** the auto claim is the protection of every name published after the restore point; the disable is the one state in
which an overlapping claim takes nothing and lets nothing through. The development agent would revisit this if the journal
volume of first publishes became a cost.
**Applied:** R-REPO-061, R-REPO-023; `internal/audit/events_repository.go`, `recordClaims` in `internal/repository/claims.go`.


### Q-REPO-251 · The refusal of an untrusted upstream certificate keeps the code `tls_untrusted`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-035 named the download answer `upstream_tls_untrusted`; the code, the result of the upstream test (R-REPO-024), the audit event `proxy.tls_untrusted` and the error registry all say `tls_untrusted` (F-77).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rename the code to `upstream_tls_untrusted` | The download code gets the prefix the other download codes have | Uniform `upstream_` prefix on download codes | The test endpoint, the event and the OpenAPI enum say `tls_untrusted` for the same condition: two names or four renames |
| (b) Keep `tls_untrusted` and fix the spec (chosen) | One name for the condition wherever it appears | The condition has one name in the test result, the event, the registry and the answer | Not every download code carries the prefix |
| (c) Two codes | One for the test result, one for the download | Each surface keeps its own convention | One condition, two names, a mapping to remember |

**Choice:** (b). The rule is one name per TLS, egress or source-network condition (`tls_untrusted`, `egress_denied`, `source_network_unavailable`) on every surface. It is not a rule for all upstream failures: a timeout is `timeout` in the test result and `upstream_timeout` on a download.

**Rationale:** the three conditions have no other meaning on a download than in the test, and an operator who reads the event, the test result and the answer finds the same word. The development agent would revisit it if the codes of the test result were to be prefixed as a whole.

**Applied in the spec:** R-REPO-035 (the answer and its acceptance criterion). The error registry ([codes.go](../../internal/errs/codes.go)) and the OpenAPI enum already said `tls_untrusted`. Test: the classification table in [proxy_test.go](../../internal/proxy/proxy_test.go) (`tls_untrusted`, 502, security event, not counted).

### Q-REPO-252 · A fill that came back with the wrong digest fails its package at once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the mirror pass treated `upstream_integrity_mismatch` like any failure of an item: three attempts, a minute longer apart each, so a pass over a tampered tarball stayed busy for about three minutes and fetched the tampered bytes three times (found by the test of F-76).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `*mirror.PermanentError` for `upstream_integrity_mismatch` and `upstream_digest_missing`; the package fails at once (chosen) | `PackageFailed` takes the attempts to the limit in one write; the pass goes on with the other packages | A tampered upstream costs one fetch per pass; the reason is recorded | A mismatch that was a broken transfer waits for the next pass |
| (b) `*mirror.RefusedError` | Counted refused, the package is done | No retry | The package is not a failure: the status shows nothing wrong with a mirror of a tampered upstream |
| (c) Keep the retries | Three attempts | A broken transfer heals within the pass | Three fetches of the same wrong bytes, a busy pass |

**Choice:** (a). `PermanentError` in [host.go](../../internal/mirror/host.go), `fillPackage` and `failPackage` in [fill.go](../../internal/mirror/fill.go), `mapFill` and `mapGate` in [mirror_host.go](../../internal/formathost/mirror_host.go), the `permanent` argument of `MirrorPackageFailed`.

**Rationale:** the engine already verifies the digest while the body is spooled, so a second fetch can only repeat the answer of a tampered upstream. The development agent would revisit it if mismatches caused by transfer faults are seen in practice (retry once).

**Applied in the spec:** R-REPO-053 step 3. Tests: `TestR_REPO_053_APermanentlyFailedFillFailsThePackageAtOnceAndThePassEnds`, `TestR_REPO_053_AnIntegrityFailureOfAFillIsPermanentForThePass`, and the mirror integration test `TestR_REPO_041_AMirrorPassOfAnNpmRemoteStoresNoTarballThatDoesNotMatchItsAnnouncedIntegrityAndEnds`.

### Q-REPO-255 · A handshake token goes to the upstream origin only, and the realm is asked under the credential rule

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine carries out the token handshake of a format (R-OCI-021). The challenge comes from the upstream, so the upstream
names the realm host, and two credentials are in play: the repository's own (what the realm may need to issue a token for a private
image) and the token the realm issues (what the registry accepts). The hop policy of the upstream client removes every
`Authorization` header and adds the repository's credential where R-REPO-033 allows, so a token set on a request would be removed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The realm is asked through the repository's client under the one credential rule; the token is attached by a mark of the request's context (`outbound.WithAuthorization`) and the hop policy sends it to the upstream origin only, in place of the credential (chosen) | The credential reaches the realm exactly where R-REPO-033 says; the token reaches the registry and nobody else | One rule for every credential; a redirect or an `extraHosts` entry never receives a token meant for the registry; the check of the realm's host is the client's `Allowed` | The hop policy knows a second kind of value |
| (b) The format sets `Authorization` itself and the engine passes it | The hop policy lets a request's own header stand on the hosts it admits | No mark | The policy's guarantee that no header of a caller or an adapter crosses a host boundary is gone for every format |
| (c) The token goes to every host that may receive the credential | The mark follows the credential rule | One rule | A realm host with `sendCredentials` (Docker Hub's `auth.docker.io`) would receive the token it issued, and a registry that redirects to a credential host would hand the token on |

**Choice:** (a).

**Rationale:** the upstream chooses the realm, so the host rule must be applied to the realm's address before a byte is sent, and the
credential must never reach a host the operator did not name. A token is valid for the registry that asked for it. The development
agent would revisit it for a registry whose blob host needs the token (none of the well-known ones does).

**Applied in the spec:** R-REPO-033 (the handshake bullet), [formats/oci.md](formats/oci.md) R-OCI-021. The token's key also holds the token service and a digest of the credential in effect (a rotation of a secret behind a reference moves no configuration version), and the request to the token service carries the credential on its first hop only (`outbound.WithCredentialsOnTheFirstHopOnly`). Code: `outbound.WithAuthorization`
in [hop.go](../../internal/outbound/hop.go), `Engine.token` in [authenticator.go](../../internal/proxy/authenticator.go). Tests:
`TestR_REPO_033_HandshakeTokenGoesToTheOriginOnly`, `TestHandshake` (`test/integration/proxy`).

### Q-REPO-256 · The quota header is read by its integer before the first semicolon

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Docker Hub answers `RateLimit-Remaining: 0;w=21600`, the integer with the window of its quota as a parameter. The parser of
R-REPO-054 read the whole value as an integer, so such an answer was not a quota answer and a mirror went on asking.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The integer before the first `;`, for every format (chosen) | `0;w=21600` is 0 | The header is the same for every upstream that sends it; one parser | A value that was never an integer elsewhere now parses |
| (b) The format parses it | A hook per format | Format-specific | Every upstream that sends a parameter needs its format to say so |

**Choice:** (a).

**Rationale:** a parameter after a semicolon is part of the header's grammar, not a different number. The development agent would revisit
it for an upstream that sends a list.

**Applied in the spec:** R-REPO-054, [formats/oci.md](formats/oci.md) R-OCI-025. Code: `upstream.QuotaRemaining` in
[ratelimit.go](../../internal/proxy/upstream/ratelimit.go). Test: `TestR_REPO_054_QuotaHeaderParse`.

### Q-REPO-285 · The decision of an auto claim takes the auto-claim configuration lock for a virtual member, and the row of the repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `Routing.AutoClaim` read the claims, decided on "another repository's claim covers the name" and on the limit of 1000, and
inserted, without a lock. Two first publishes of one name into two members of one virtual repository touch different rows, so both
passed the check and both inserted the exact claim (the claim of one repository would then be in effect against the other's). Two
first publishes of different names into one repository one claim below the limit both passed the count. A unique constraint cannot
express the first rule (the claims of two repositories that share no virtual repository may name the same name), and the count is no
constraint either.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The configuration lock `(4, 5)` for a repository that is a virtual member, then the repository row `FOR NO KEY UPDATE` (chosen) | Both end with the publish's transaction; the claims are read after both | Fits the lock categories of R-ARCH-017 (no hashed key); a repository outside every virtual repository takes no global lock; the row lock also orders the count; one fixed order | The first publishes of virtual members run their decision one after the other for the rest of their transaction, across all formats |
| (b) A row lock on every virtual repository containing the repository | The shared virtual repository orders the two publishes | Narrower than a global lock | The delete of a repository locks its row before the virtual repositories and a virtual repository's write locks itself before its members: no order of the two row locks fits both, and either one deadlocks with one of them |
| (c) An advisory lock keyed by a hash of the name | One lock per name | Only publishes of one name wait | R-ARCH-017 forbids a hashed second key, and the count of the limit is not covered |
| (d) Check again after the insert and delete the second claim | No lock | None | A claim visible for a moment can already have been honoured by the routing of another replica, and a delete is a second write the journal records |

**Choice:** (a).

**Rationale:** (a) is the only option that decides on committed claims without a lock order that a delete or a virtual write can
reverse: the configuration lock comes first, the row second, and no other writer takes either after a row it holds in the other order
(the rows a publish holds before, its package and the foreign keys of the repository, conflict with neither lock; `FOR UPDATE` on the
repository would deadlock two publishers that both hold the foreign keys, hence `FOR NO KEY UPDATE`, which the claims-version update
needs anyway). The cost is bounded: only a first publish of a name into a virtual member takes the global lock, and it holds it for
the rest of its transaction. The development agent would revisit it when first publishes into virtual members are frequent enough
for that wait to show in the publish latency; the narrower key would then be a registered object type per virtual repository.

**Applied in the spec:** R-REPO-023 (the serialisation bullet), R-ARCH-017 (the `config` category lists object type 5). Code:
`lockAutoClaim` in [routing.go](../../internal/repository/routing.go), `ConfigAutoClaims` in
[locks.go](../../internal/cluster/locks.go), queries `RepositoryIsMember` and `LockRepositoryForAutoClaim`
([claims.sql](../../internal/meta/postgres/queries/claims.sql)). Tests: `TestAutoClaimConcurrentPublishes`
(`test/integration/repository`). A manual claims write takes the configuration lock too, as its first statement and so in the same
order (F-108; `PutClaims` in [claims.go](../../internal/repository/claims.go), test `TestAutoClaimAndManualClaimOfOneName`).

### Q-REPO-270 · A format without a package document answers the shadowing check from its index rows, in the scope of the virtual repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the shadowing check of R-REPO-048 asks whether a remote member already serves a package name, by the stored package document or one
probe of it. An APT archive has no document per package: its packages are listed in repository-wide indexes, and which of them a client sees
depends on the suites, components and architectures of the virtual repository it reads through.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An optional `NameServer` on the remote adapter, asked per pair of a virtual repository and a remote member, with the virtual repository's scope (chosen) | `NameServer.ServesName(h, NameQuery{Remote, Via, ViaSettings, Name})` reads the parsed rows in that scope; `known` false means a suite of the scope is not settled yet | The answer is exact for the scope; a name that exists only in a component the virtual repository does not serve does not block an upload; no probe of an upstream that has no document | The check asks per pair and not once per remote; the contract grows by an optional interface |
| (b) Probe an index file as the package document | One GET of a `Packages` file | No new interface | The index is megabytes, names every package, and says nothing about the scope |
| (c) Treat a format without a document as having no shadowing | `PackageDocumentPath` reports false and the check passes | Nothing to build | Dependency confusion through a virtual repository (R-REPO-023) is open for the format |

**Choice:** (a).

**Rationale:** the check stays fail-closed: an undetermined scope is 503 `shadow_check_unavailable` for a caller without `manage`, with the
fetch and parse of what settles it enqueued. The development agent would revisit it if a second format without package documents needed a
different question.

**Applied in the spec:** R-REPO-003, R-REPO-048 (the Debian work package edits its text), [formats/debian.md](formats/debian.md) R-DEB-023. Code:
`formats.NameServer`, `formats.RemoteCaps.Names`, `virtual.NameServers` (`Options.Names`), `formathost.NewNameServers`.

### Q-REPO-272 · A job run of a remote repository gets an upstream outside a mirror pass

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the Debian format fetches a remote's release documents and index files from jobs that no client request and no mirror pass runs: the
parse of a remote's indexes (R-DEB-023) and the build of a virtual repository that merges its members' releases (R-DEB-026). They need the
egress, credential, background share (R-REPO-055) and throttling (R-REPO-054) rules of a pass, without a pass, its fence or its window.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Host.BackgroundUpstream(ctx, repo)` returns a `discover.Upstream` for one job run (chosen) | The same interface a lister reads in a pass; `Document` and `Artifact` behave as they do for a pass | One vocabulary for a pass and a job; the rules of R-REPO-054 and R-REPO-055 apply without a copy | The host grows a method |
| (b) The job builds its own client | Each job its HTTP client | No host change | Egress, credential and throttling rules are then copied |

**Choice:** (a). The method is declared with the contract of the Debian format and answers `not_implemented` until the work package of the
background fetches builds it.

**Rationale:** the development agent would revisit it if a job needed to move the circuit of a repository, which a pass's outcomes do not.

**Applied in the spec:** R-REPO-055 (the background share also covers these runs: the bullet "Runs outside a pass"), [Q-REPO-305](#q-repo-305--a-run-outside-a-pass-has-a-window-of-its-own-and-ends-on-a-long-wait). Code:
[background_upstream.go](../../internal/formathost/background_upstream.go), [window.go](../../internal/mirror/window.go). Tests: the unit tests of both and
`TestR_REPO_055_ABackgroundUpstreamFetchesAndStoresWithoutAPass` in [background_test.go](../../test/integration/mirror/background_test.go).

### Q-REPO-305 · A run outside a pass has a window of its own and ends on a long wait

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the upstream of a job run outside a pass ([Q-REPO-272](#q-repo-272--a-job-run-of-a-remote-repository-gets-an-upstream-outside-a-mirror-pass))
sends the same background requests as a pass, which the engine already holds to the slots and the ceiling `B` of R-REPO-055. What a pass adds on
top is its window and its waits (R-REPO-054): it starts at 1, grows while the upstream answers, halves on a rate-limit answer and sends nothing
during a wait. A job has no pass state to store a wait in, no job result to defer with, and a Debian index parse may send a request per index.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A window per run, the pass's throttle without the pass (chosen) | `mirror.Window`: starts at 1, ceiling `B`, waits as R-REPO-054 says inside the call; a wait over 300 s ends the run with a `*mirror.DeferredError` that carries the end of the wait | The same behaviour towards a rate-limiting upstream as a pass; no state to keep; the job retries at the given time | Two runs of one repository do not see each other's waits: the slots and `B` still bound them together |
| (b) The engine's slots only | No window, no wait | Nothing to build | A rate-limit answer fails the job and its retry asks again at once |
| (c) Store the wait in `mirror_state` | Runs and passes share `rate_limited_until` | One wait for everything | A repository that is no mirror has no row; a job would write the state of a pass it is not |

**Choice:** (a).

**Rationale:** the rules of R-REPO-054 are about not provoking an upstream, whoever sends the requests; the engine's slot cap alone does not slow a
run down after an answer. The wait is not stored: the next run of the job starts with a window of 1, which is cautious, and the upstream answers
again if it still limits. The development agent would revisit it if a repository with several runs at once were rate limited in practice, which
points to the shared wait of (c). Not built: the metrics of R-REPO-057 (`trove_mirror_rate_limited_total`, the wait seconds) count the answers of
a pass only; a run's answers appear in the upstream log with the job's id.

**Applied in the spec:** R-REPO-055 (the bullet "Runs outside a pass"). Code: [window.go](../../internal/mirror/window.go),
[background_upstream.go](../../internal/formathost/background_upstream.go).

### Q-REPO-273 · A format's data port adds rows in the transaction that creates its repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: every Debian local and virtual repository has a signing key from the transaction that creates it (R-DEB-015); a repository without
one would answer its first request with an unsigned index or fail it. The key is a row of the format's own table.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A creation hook the composition root registers per format, run by the repository service inside the creating transaction (chosen) | `repository.RegisterCreateHook(format, fn)`; `fn(ctx, tx, repo, class)`; an error rolls the creation back | No window without a key; the repository package imports no format; the contract of the format is untouched | A seam in the repository service |
| (b) The first build creates the key | Lazy | No hook | `signing-key.asc` and the first sources line have nothing to trust until a build; two builds may race to create it |
| (c) A job after the creation | Asynchronous | No hook | The same window as (b) |

**Choice:** (a).

**Rationale:** the hook is a seam of the repository service and not of the format contract, so it needs no ADR of the contract. The development
agent would revisit it if the architecture review prefers (b); the deviation is then recorded on [formats/debian.md](formats/debian.md) and
`signing-key.asc` answers 404 until the first build.

**Applied in the spec:** R-REPO-009 (above). Code: [create_hooks.go](../../internal/repository/create_hooks.go), `Service.CreateTx`.

### Q-REPO-280 · The upstream test reads a Bearer challenge of a handshake format as reachable

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the probe of an OCI remote requests `/v2/`. A registry answers it `401` with `WWW-Authenticate: Bearer realm=…` to every client that holds no
token, the repository's credential included, because the credential is judged by the realm and not by the registry. R-REPO-024 reads a `401` as
`authenticated: false`, which would tell every administrator of a working Docker Hub remote that its credential was refused.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A catalog fact `ProbeChallengeReachable`: the `401` of such a format with a Bearer challenge is reachable and `authenticated` is `null` (chosen) | The test says what it knows: the registry answered and wants a token | No false verdict; no new field of the result (`authenticated` is nullable already) | A refused credential is not found by the test; a fact in the catalog (an addition to the contract surface, ADR-176) |
| (b) The probe runs the handshake | The test judges the credential | The administrator learns about a wrong password | A second request to a second host in a test of one resource, with the credential rules of R-REPO-033; the format would have to build it |
| (c) `authenticated: true` for the challenge | Reads as success | Nothing to build | A statement the test did not check |

**Choice:** (a). The fact is `catalog.Facts.ProbeChallengeReachable`, set for OCI with `ProbePath: "v2/"`; the `Www-Authenticate` of the answer is read for
the scheme `Bearer` only, so a `401` with Basic is still `authenticated: false`.

**Rationale:** the development agent would revisit it when the test is to judge the credential of a registry (option (b) then runs the handshake of the
format's `Authenticator`).

**Applied in the spec:** R-REPO-024, [formats/oci.md](formats/oci.md) R-OCI-021. Code: `Clients.probe` in [probe.go](../../internal/proxy/upstream/probe.go). Tests:
`TestR_REPO_024_ABearerChallengeOfAHandshakeFormatIsReachable`, `TestR_REPO_024_TheUpstreamTestReadsABearerChallengeAsReachable`.

### Q-REPO-281 · The probe of the registry-root format is at the root of the host

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-REPO-280's probe resource for OCI is `/v2/`, and the generic probe appends the probe path to the path of the upstream URL. For a registry the path of
`remote.url` is a namespace prefix of image names (R-OCI-021), so `https://ghcr.io/acme` probed `/acme/v2/`, a path no registry serves, and the `404` read as
`authenticated: true`: a false verdict on the credential. The audit also asked what a Bearer challenge means with `authMode: bearer`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) For the format that declares `RegistryRoot`, the probe path is joined at the root of the host; with `authMode: bearer` a Bearer challenge is a refusal of the stored token (chosen) | `/v2/` is asked where the registry has it | The verdict is the registry's | A rule keyed on a catalog fact other than the probe path |
| (b) A catalog fact for the probe's base | Explicit | | A second fact for the one format that has a prefix |

**Choice:** (a).

**Rationale:** the development agent would revisit it for a second format whose URL path is not a base of its API.

**Applied in the spec:** R-REPO-024, [formats/oci.md](formats/oci.md) R-OCI-021. Code: `probeURL` in [probe.go](../../internal/proxy/upstream/probe.go). Test:
`TestR_REPO_024_TheProbeOfTheRegistryRootFormatIsAtTheRootOfTheHost`, `TestR_REPO_024_TheUpstreamTestOfAPrefixedRegistryAsksTheRootOfTheHost`.

### Q-REPO-295 · The version bound counts versions, a format's mirror member is kept from an Owner, and an artifact is filled once per attempt

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the plan of an OCI image has many items per version (R-OCI-026: the tag is the version of its child manifests, configuration and layers), and
`SelectItems` counted items, so `maxVersionsPerPackage: 2` would have kept two blobs. Two tags of an image share layers, which the engine would have
checked against `maxBytes` and asked the host for twice. The format's mirror member `oci.mirror` (Q-OCI-22) is kept from an Owner like the patterns, but the
validation knew only the generic members.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `SelectItems` decides at the first item of a version and keeps the version whole; the fill skips a path it already filled in the attempt; the validation compares the canonical `<format>.mirror` member of the stored and the written document in either mode (chosen) | One rule for every format; npm and NuGet have one item per version and behave as before | The comparison is by value: an Owner cannot write the format's defaults explicitly into a repository that has none | |
| (b) A `Version` that is a list, or a per-version item group | Explicit grouping | A contract change for every format | |
| (c) The format selects versions itself | Nothing in the engine | The engine's bound (R-REPO-051) would be a format matter | |

**Choice:** (a).

**Rationale:** the version is the unit of R-REPO-051 whatever it is made of. The development agent would revisit the by-value comparison if an Owner needs to write
the defaults, and the path dedupe if a format's plan lists one path in two versions that the bound treats differently (the first fill wins).

**Applied in the spec:** R-REPO-050, R-REPO-051. Code: `SelectItems` ([policy.go](../../internal/mirror/policy.go)), `fillPackage` ([fill.go](../../internal/mirror/fill.go)),
`checkDelegatedFormatMirror` ([validate_mirror.go](../../internal/repository/validate_mirror.go)). Tests: `TestR_REPO_051_TheVersionBoundsCountVersionsNotItems`,
`TestR_REPO_051_AVersionOfSeveralItemsIsStoredWholeAndASharedArtifactOnce`, `TestR_OCI_026_AnOwnerKeepsTheFormatsMirrorSettings`.

## What this does not cover

### H-4 · Malicious content from a legitimate upstream

Verification proves only that the bytes match what the upstream announced. A malicious package
published to a public registry is fetched, cached and served like any other until an advisory names it:
the vulnerability scanning of M4b ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-015) blocks
a version only once its advisory has arrived, it matches identities and not content (H-310 and H-315 there), and
trove applies no vetting of new packages and no minimum-age rule of its own (an npm client's release-age
policy reads the publish times trove serves, [formats/npm.md](formats/npm.md) R-NPM-012). Claims and pin rules
stop name confusion, not malicious public packages. `cacheFillAction: deploy` narrows who can bring new packages in.

### H-5 · Version removals reach cached artifacts only where the format tracks versions

Component: upstream-gone handling of remote repositories (R-REPO-029). Subject: cached artifacts of
a version that the upstream withdrew while the package itself stays, in formats that do not track
removal per version; in the MVP that is NuGet. Live with the MVP. When a whole package disappears
upstream, R-REPO-029 stops serving its document and its cached artifacts. When only one version
disappears from a package document that still exists, the generic layer cannot tell which artifacts
the refreshed document no longer covers: clients that resolve through the document stop seeing the
version, but a request by exact path, through the remote, its cache view or a virtual repository,
still receives the cached artifact. npm tracks versions (R-NPM-026) and is not affected. What makes
it real: an upstream that deletes single versions (nuget.org deletes packages only for potentially
harmful content or infringement,
[deleting packages](https://learn.microsoft.com/en-us/nuget/nuget-org/policies/deleting-packages)),
and a client or script that downloads by exact path. An operator has to find and delete such items
by hand. Copies that clients downloaded earlier are never recalled.

### H-6 · Remote repositories reach private networks

The egress rules permit private ranges by default, because internal registries are common. A holder
of `repositories:write-config` can point a remote repository or the upstream test at an internal
HTTP service and read its responses through the cache. With an outbound proxy, trove checks the
target name's resolution, but the proxy connects on its own. The mitigations are the egress
`NetworkPolicy` and narrowed `deny_cidrs`; there is no per-repository egress scope.

### H-7 · Claimed names are observable

Claims and pin rules ignore the caller's permissions by design (R-REPO-023). A caller with `read` on
a virtual repository can learn that an internal name or prefix is claimed by comparing a 404 with the
public package of the same name. Names leak; contents do not.

### H-8 · The stored-secret key sits on every replica

Every replica holds the key that unseals upstream credentials, client-certificate keys and proxy
passwords: `packages` replicas (and the bridge's `web`) make upstream requests, `worker` replicas run the
mirror passes, and every replica opens the key ring's start-up canary ([architecture.md](architecture.md) R-ARCH-021). Exec access to a trove pod, or read access
to the key's Secret, yields all of them. Adding `web` replicas for load adds copies of the key, not
new holders of a different kind. Valkey never holds a secret or a value derived from one
([architecture.md](architecture.md) R-ARCH-030). Integration with an external key management service
is not specified.

### H-9 · Member-scoped claims do not protect other virtual repositories

Component: namespace claims (R-REPO-023). Subject: a virtual repository V2 of the same format that
does not contain the local repository holding a name, and remote repositories addressed directly.
Live with the MVP. Auto claims have scope `members`, so a name published only to `npm-team-x` is
protected in every virtual repository that contains `npm-team-x`, but V2 still resolves the name
from its remote members, and with it a public package of the same name. Clients configured against
V2, or against a remote repository directly, receive that package. What makes it real: several
virtual repositories with different local members, and internal names that nobody turned into a
`global` claim. The warning `dependency_confusion_unprotected` covers only virtual repositories
whose local members have no claims at all.

### H-10 · Deploy on a local member shadows public packages for every consumer

Component: virtual resolution (R-REPO-019, tier order), auto claims (R-REPO-023) and the shadowing
check (R-REPO-048). Subject: clients of virtual repositories that contain a local repository next
to remote members. Local members form the first resolution tier, and the first publish of a name
claims it, so a local `lodash@4.17.22` replaces the public `lodash` for every client of those
virtual repositories and hides the versions that exist only upstream.

Narrowed in the MVP by R-REPO-048 (Q-REPO-6). The first publish of a name that a remote member
serves needs `manage` or a prior manual claim, and the check fails closed while the upstream cannot
be asked. A `deploy` grant alone, however broad, and a leaked CI token can no longer do it. What
remains, live with the MVP:

- **Deliberate shadowing.** A `manage` holder, or a `deploy` holder inside a manual claim, still
  replaces the public package for every client. This is by design, since patched forks are
  legitimate. It is audited (`repository.remote_package_shadowed`), not prevented.
- **Configuration changes are not checked.** The check runs at publish time only. A local
  repository that collected names while no containing virtual repository had remote members, a
  sandbox for example, shadows without any decision once it is added to a virtual repository with
  remote members. The same happens when a remote member is added next to it, re-enabled, or its
  patterns are widened, and when a local repository whose auto claims are exact names joins a
  virtual repository in which another repository claims a prefix covering them (R-REPO-023 skips
  the auto claim only for claims in effect at publish time). These changes need
  `repositories:write-config`. What makes it real: adding sandbox or team repositories, or new
  remotes, to a shared virtual repository.
- **Offline remotes count as not serving.** A remote in offline mode (R-REPO-030, R-REPO-031) that
  holds no stored document of a name does not count. A name published while it is offline shadows
  that remote's package once it is back online.
- **Existence oracle.** The 403 `shadows_remote_package` tells a `deploy` holder that some remote
  member of a containing virtual repository serves a name, also when it cannot read that remote (a
  private partner registry, for example). Names leak; contents and remote keys do not, as in H-7.
- **Probes disclose new names upstream.** A first publish of a name outside every claim in effect
  sends one request for that name to the upstream of each exposed remote member, public
  registries included. The name of an internal package that was never resolved through trove
  before thereby reaches the upstream operator once. Claiming the internal prefix beforehand
  (`@acme/*`, `Acme.*`) removes the probe: with a claim of the publishing repository the check does
  not run, and with a claim in effect in the virtual repository nothing there is exposed.

### H-404 · The repository key column carries the database's collation

Component: `repositories.key` (migration `00002_storage_core.sql`, `text NOT NULL UNIQUE`) and its unique index. Subject: the
index that makes keys unique and finds a repository by key. [ha-and-helm.md](ha-and-helm.md) says that text columns which carry
indexed keys, repository keys among them, are declared with `COLLATE "C"`; this one is not, so the column takes the collation
the database was created with (`en_US.utf8` in the integration stack, the PostgreSQL image's default). The list's order no longer
depends on it (R-REPO-012, the query sorts with `COLLATE "C"`). Dormant: a collation update of the database server's operating
system (the glibc 2.28 change of how punctuation sorts) changes the order of keys with `-` and `.` under an existing index, which
can then miss a key or admit a duplicate until it is rebuilt. What makes it real: an operating-system upgrade of the database
server without `REINDEX` on a database created with a libc collation. Mitigation: create the database with the `C` collation
(`initdb --locale=C`), or reindex after such an upgrade; the fix is an expand migration that declares the column `COLLATE "C"`.

### H-503 · A mirror stores a malicious version before anyone asked for it

Component: the passes of R-REPO-053. Subject: every repository whose clients resolve through a mirrored remote. A pull-through
remote stores a version when a client first asks for it; a mirror stores every version its patterns and version bounds admit as
soon as the upstream lists it, so a malicious version published under a mirrored name (H-4) is in the cache within one interval,
also when no build would have chosen it. The vulnerability scanning evaluates stored versions and blocks a malicious one when it is
served ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-015); between the store and the advisory a client that resolves
the version gets it, as with pull-through. Live since the passes of R-REPO-053 are built (phase 6b). What makes it real: a mirror of a public upstream whose
patterns cover a name that an attacker publishes to, and a client that resolves `latest` before the advisory arrives.

### H-504 · npm's replication host is contacted although it is not an extraHosts entry

Component: the npm discovery of [formats/npm.md](formats/npm.md#mirror-discovery) (Q-NPM-72). Subject: the outbound traffic of an
npm mirror of the public registry. The format declares `replicate.npmjs.com` as a discovery host of its well-known upstream, so the
mirror's feed requests go to a host that the remote's `extraHosts` does not list. The egress address rules apply at connect time and
no credential is sent there, but an operator who reads `extraHosts` as the complete list of the remote's destinations is wrong for
this one host. Live since the npm discovery is built (phase 6b, [mirror.go](../../internal/formats/npm/mirror.go)). What makes it
real: an outbound allow list kept from `extraHosts` alone.

### H-505 · The byte bound of a mirror can be passed by the artifacts in flight and the fold lag

Component: `remote.mirror.maxBytes` (R-REPO-052). Subject: the object store's capacity. The bound reads the folded usage counter,
which lags by up to one fold interval (`storage.usage.fold_interval`, 5 s by default), and the pass checks it before each artifact,
so up to `concurrency` artifacts in flight and those committed within the lag can pass it, each up to `limits.maxArtifactBytes`.
Live since R-REPO-052 is built (phase 6b; the mechanism behind the lag is
[H-527](../security/mirror.md#h-527--the-bounds-base-is-a-lagging-counter-and-a-per-process-tally)). What makes it real: a bound set close to the store's capacity with large artifacts.

### H-506 · Until quotas are built, only maxBytes bounds a mirror of an upstream that is not well known

Component: R-REPO-052 and [storage.md](storage.md) R-STOR-029 (P1). Subject: the installation's object store. A holder of
`repositories:write-config` can mirror an upstream other than the format's well-known one without include patterns and without
`maxBytes`; the warning `mirror_unbounded` is the only stop, and the system capacity guard that would stop the pass at `warnPercent`
exists only with R-STOR-029. Live for the configuration and, since R-REPO-053 is built (phase 6b), for the passes.
What makes it real: a large private or proxying upstream mirrored without a bound, filling the store until client publishes fail.

### H-507 · A hostile upstream can make a mirror send as many requests as its feed lists names

Component: the discovery of R-REPO-051. Subject: the outbound request volume of a mirror and the time of the process that runs
its passes. The patterns bound
which names are asked for, and the byte bound what is stored, but not how many names a feed or a listing returns under an admitted
prefix: an upstream that lists millions of names below `@acme/` makes each pass ask for each of their documents. The requests go to
that upstream only, under the window of R-REPO-054. Live since R-REPO-053 is built (phase 6b). What makes it real: a mirror of an upstream
that an attacker controls or has compromised, with a broad admitted prefix.

