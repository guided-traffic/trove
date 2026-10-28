# Swift package format

This page catalogs what a repository manager does to act as a Swift package registry for the Swift
Package Manager (SwiftPM): hosting releases in local repositories, proxying another registry in
remote repositories, merging both behind one registry URL in virtual repositories, client
authentication and setup, publishing, index calculation, and the client behaviors that decide
whether dependencies resolve through the registry at all. The wire protocol is defined by the
Swift project in the
[Swift Package Registry Service Specification](https://github.com/swiftlang/swift-package-manager/blob/main/Documentation/PackageRegistry/Registry.md)
(cited below as "the registry specification") and, for authentication, by
[SE-0378](https://github.com/swiftlang/swift-evolution/blob/main/proposals/0378-package-registry-auth.md);
where an entry relies on those documents, it says so.
Repository classes in general are described in [../repositories.md](../repositories.md),
pull-through caching in [../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/swift/<repo-key>` stands for the registry URL of
one repository (the path layout is the implementation's choice), `swift-local`, `swift-remote` and
`swift-virtual` are example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-SWIFT-001 · Swift repositories in all three repository classes
Priority: **P2**

Swift is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)). Capabilities of the format as a whole:

- local repositories hold an organization's own releases; the system computes an index for them
  (PKG-SWIFT-012, PKG-SWIFT-013);
- remote repositories proxy another registry that speaks the registry specification and validate
  the metadata they receive (the kind of validation is unspecified) (PKG-SWIFT-014);
- virtual repositories expose local and remote members under one registry URL (PKG-SWIFT-016);
- the native SwiftPM command line is the client for publishing, resolving and managing packages.

Creating a repository requires the system administrator or a project administrator role; for
others the create action is not offered. UI location: administration, repositories, create
repository, then choose the class and the Swift format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)). A remote form asks to verify the upstream URL (its
pre-filled value is unspecified); a virtual form asks for one or more local or remote members.

Shared settings that do not apply to Swift, per repository class:

| Setting | Excluded for |
|---|---|
| Priority resolution (REPO-018) | Local repositories; for remote repositories Swift is not among the supported formats either |
| CDN download redirect (REPO-020) | Local and remote repositories |
| Remote retrieval for requests from other instances (REPO-039) | Virtual repositories |
| Scanner indexing (REPO-021) | Local repositories; open for remote repositories, probably excluded there as well |

Federation is open: either Swift repositories can be federated, since federation admits every
format except the Terraform state backend (REPL-026 in
[../replication-and-federation.md](../replication-and-federation.md)), or they cannot, since the
repository configuration leaves Swift out of its list of federated formats; the spec decides.

### PKG-SWIFT-002 · Registry URL and package identity
Priority: **P2**

Every Swift repository answers the registry protocol under its own base URL, the *registry URL*.
All endpoints of PKG-SWIFT-004 to PKG-SWIFT-009 are relative to it.

A package is identified by `<scope>.<name>` (for example `mona.LinkedList`); the registry
specification defines both parts:

| Part | Rule | Limit |
|---|---|---|
| Scope | Letters, digits and hyphens; no leading, trailing or doubled hyphen; case-insensitive | 39 characters |
| Name | Letters, digits, hyphens and underscores; no leading, trailing or doubled separator; case-insensitive | 100 characters |

- The scope is the package's owner or namespace and normally mirrors the
  scope in the source repository URL, the name is the one declared in `Package.swift`, and the
  version normally corresponds to a tag in the source repository.
- Case-insensitivity means `mona.LinkedList` and `MONA.linkedlist` are the same package; lookups
  and duplicate checks must fold case. Whether the system preserves the original spelling in
  responses is unspecified.
- Versions follow Semantic Versioning (the registry specification orders releases by precedence).

## Protocol endpoints

### PKG-SWIFT-003 · API version negotiation, media types and errors
Priority: **P2**

Conventions every endpoint of the registry specification follows:

| Aspect | Behavior |
|---|---|
| Transport | HTTPS is required by the specification. SwiftPM uses HTTPS by default; a configured registry is switched to HTTP by editing the scheme of the URL in the client's `registries.json`. |
| Version negotiation | The client sends `Accept: application/vnd.swift.registry.v1+<type>` with `json`, `zip` or `swift`. The server sets `Content-Type` and `Content-Version: 1`. |
| Missing `Accept` | The server may answer 400 or pick a version itself. |
| Unknown or invalid version | 400. Valid but unsupported version: 415. |
| Errors | RFC 7807 problem details (`application/problem+json`) with a `detail` text. |
| Rate limiting | 429 with `Retry-After`. |
| `HEAD` | Should be answered on every endpoint. |
| `OPTIONS *` | May be answered with `Link` entries pointing to the specification and its OpenAPI description. |

These details are required for SwiftPM to work and come from the registry specification.

### PKG-SWIFT-004 · List package releases
Priority: **P2**

`GET <registry-url>/<scope>/<name>` (an optional `.json` suffix is allowed) returns the releases
of one package:

- body: a JSON object with a top-level `releases` object keyed by version; each value may carry a
  `url` of the release resource and a `problem` object (for example status 410 for a removed
  release); the client treats releases with a `problem` as unavailable;
- `Link` header: `latest-version` (highest precedence release), optionally `canonical` and
  `alternate` (source repository URLs), optionally pagination relations (`first`, `prev`, `next`,
  `last`);
- releases should be listed highest precedence first, but clients must not rely on the order;
- 404 when the package does not exist.

In a virtual repository the list is the union of the members' lists (PKG-SWIFT-016). Which member
wins when two members hold the same version is unspecified; REPO-035 in
[../repositories.md](../repositories.md) describes the general resolution order.

### PKG-SWIFT-005 · Fetch release metadata
Priority: **P2**

`GET <registry-url>/<scope>/<name>/<version>` (optional `.json`) returns one release:

| Field | Meaning |
|---|---|
| `id` | `<scope>.<name>` |
| `version` | Release version |
| `resources` | Array; for a source release one entry named `source-archive` of type `application/zip` with `checksum` (hex SHA-256 of the archive) and, if signed, a `signing` object (base64 signature and format such as `cms-1.0.0`) |
| `metadata` | Release metadata submitted at publish time (description, repository URLs, license URL, author and so on) plus server-populated fields |
| `publishedAt` | ISO 8601 publication time recorded by the registry (optional) |

`Link` header: `latest-version`, `successor-version`, `predecessor-version`. The checksum is what
SwiftPM verifies the downloaded archive against (PKG-SWIFT-007) and records in
`Package.resolved`, so it must never change for a published release.

### PKG-SWIFT-006 · Fetch the package manifest
Priority: **P2**

`GET <registry-url>/<scope>/<name>/<version>/Package.swift` returns the release's manifest as
`text/x-swift` with `Content-Disposition: attachment; filename="Package.swift"`.

- The response must carry one `Link` entry with relation `alternate` for every version-specific
  manifest in the archive (files named `Package@swift-<major>[.<minor>[.<patch>]].swift`), with
  `filename` and `swift-tools-version` attributes.
- Query parameter `swift-version=<x.y>`: if `Package@swift-<x.y>.swift` exists, return it;
  otherwise answer 303 redirecting to the unqualified `Package.swift`.
- The server therefore has to read the manifest files out of the uploaded zip and parse the
  `// swift-tools-version:` comment of each.

The manifest-serving behavior comes from the registry specification.

### PKG-SWIFT-007 · Download a source archive
Priority: **P2**

`GET <registry-url>/<scope>/<name>/<version>.zip` returns the release's source archive
(`application/zip`).

| Header | Requirement (registry specification) |
|---|---|
| `Content-Length` | Required |
| `Content-Disposition` | `attachment; filename="<name>-<version>.zip"` |
| `Digest` | Optional `sha-256=<base64>` of the archive |
| `Accept-Ranges`, caching | Range requests and HTTP caching recommended |
| Signature headers | Required when the release is signed (format and base64 signature) |

- The server may redirect (303) to another host, for example a signed object-storage URL; the
  client must not follow a redirect that downgrades to plain HTTP.
- Mirrors may be advertised with `Link` relation `duplicate`.
- The client verifies the archive against the checksum from PKG-SWIFT-005.

### PKG-SWIFT-008 · Look up package identifiers by source URL
Priority: **P2**

`GET <registry-url>/identifiers?url=<source-repository-url>` returns
`{"identifiers": ["<scope>.<name>", …]}`; 400 without `url`, 404 when nothing is registered for
the URL.

- SwiftPM uses this lookup to replace dependencies declared by source-control URL with registry
  identities (PKG-SWIFT-017). Without it, URL-declared dependencies keep resolving from Git even
  when a registry is configured.
- The registry specification names the `repositoryURLs` array of the release metadata as one way
  to learn the mapping and recommends verifying ownership claims.
- The endpoint's behavior comes from the registry specification; how the system populates the
  mapping for local releases and whether a remote or virtual repository forwards the lookup
  upstream is unspecified.

### PKG-SWIFT-009 · Publish a release
Priority: **P2**

`PUT <registry-url>/<scope>/<name>/<version>` with a `multipart/form-data` body creates a release.
Publishing is possible to local repositories and, by the general rule, through a virtual repository
with a default deployment repository (REPO-038 in [../repositories.md](../repositories.md)).

| Part | Content type | Requirement |
|---|---|---|
| `source-archive` | `application/zip` | Required |
| `source-archive-signature` | `application/octet-stream` | Optional |
| `metadata` | `application/json` | Optional; release metadata per the specification's JSON schema |
| `metadata-signature` | `application/octet-stream` | Optional |

Client side:

```shell
swift package archive-source                                  # creates <name>.zip in the package root
swift package-registry publish mona.LinkedList 1.1.1 \
    --url https://repo.example.com/swift/swift-local          # SwiftPM 5.9 and later
```

For SwiftPM 5.8 and earlier, which has no publish command, the archive is uploaded with a plain
HTTP client:

```shell
curl -X PUT -u "user:${TOKEN}" \
  -H "Accept: application/vnd.swift.registry.v1+json" \
  -F source-archive="@LinkedList-1.1.1.zip" \
  https://repo.example.com/swift/swift-local/mona/LinkedList/1.1.1
```

Server behavior from the registry specification:

- authentication is required; a release, once published, never changes;
- an existing version answers 409; an archive or metadata the server refuses answers 422 (or 413
  for oversized metadata); a server that does not support publishing answers 405;
- success is 201 with `Location` of the new release, or 202 with a polling `Location` and
  `Retry-After` for asynchronous processing (requested by the client with
  `Prefer: respond-async`); an `Expect: 100-continue` request is either honored or answered 417;
- the server should compute the checksum the way `swift package compute-checksum` does (SHA-256
  of the archive bytes).

Required privilege: deploy on the target repository (IAM-022 in
[../identity-and-access.md](../identity-and-access.md)); overwriting is not possible through the
protocol because of the 409 rule. Deleting a release goes through generic item deletion (ART-034 in
[../artifacts.md](../artifacts.md)); whether the release list then reports it with a 410 problem
object or omits it is unspecified.

## Client authentication and setup

### PKG-SWIFT-010 · Client authentication
Priority: **P2**

SwiftPM sends HTTP Basic credentials or a bearer token to the registry host.

| Method | Client configuration | Notes |
|---|---|---|
| `.netrc` | `machine repo.example.com login <user> password ${TOKEN}` in `~/.netrc` | The primary setup; only the user-level netrc file is read (SE-0378). The token is used as the Basic password (TOK-034 in [../access-tokens.md](../access-tokens.md)). |
| Login command | `swift package-registry login <registry-url> --username <user> --password ${TOKEN}` (or `--token ${TOKEN}` for bearer) | Stores the credential in the macOS keychain or, elsewhere, in the netrc file. Used to bind credentials to one specific repository, bypassing project and global registry settings. |

- SE-0378 (shipped with SwiftPM 5.8) defines a login endpoint: SwiftPM sends `POST <registry-url>/login`
  with the credential in `Authorization` (`Basic` or `Bearer`); the server answers 200 for valid
  credentials, 401 otherwise, and 501 for an unsupported authentication method. A full login URL
  can be passed instead of the registry URL when the endpoint lives elsewhere. SwiftPM 5.8 and
  later need server-side support for their authentication flow (PKG-SWIFT-018), which presumably
  means implementing the login endpoint.
- Anonymous read access follows the general anonymous-access setting (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)). The registry specification
  asks for 401 without credentials and allows 403 or 404 for authenticated but unauthorized
  requests.

### PKG-SWIFT-011 · Client setup
Priority: **P2**

```shell
# per project (writes .swiftpm/configuration/registries.json)
swift package-registry set "https://repo.example.com/swift/swift-virtual"
# for all projects of the user
swift package-registry set --global "https://repo.example.com/swift/swift-virtual"
```

- The setup examples configure a virtual repository as the registry, which lets local and remote
  content resolve through one URL.
- The client setup instructions (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md))
  offer the same snippets with the user's token filled in.
- Dependencies are declared by registry identity in `Package.swift`:
  `.package(id: "mona.LinkedList", from: "1.1.0")`.

## Local repositories

### PKG-SWIFT-012 · Storage layout and index calculation
Priority: **P2**

Releases deployed to a local repository are indexed only when they sit at one of these paths:

| Layout | Origin |
|---|---|
| `<scope>/<name>/<name>-<version>.zip` | Deployment of the archive as a plain file (ART-002 in [../artifacts.md](../artifacts.md)) |
| `<scope>/<name>/<version>` | Deployment through the publish endpoint (PKG-SWIFT-009) |

Files elsewhere are stored but never appear in the registry responses. Which metadata the index
holds beyond what PKG-SWIFT-004 and PKG-SWIFT-005 need, and whether indexing runs automatically
after each deployment, is unspecified.

### PKG-SWIFT-013 · Recalculate the Swift index
Priority: **P2**

An asynchronous recalculation of a local Swift repository's index (REPO-009 in
[../repositories.md](../repositories.md)).

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate Swift index | Repository key | Plain-text acknowledgement; 401, 403 or 500 on failure | System or project administrator |

- UI location: artifact browser, context menu of the repository, "recalculate index".
- The recalculation also indexes remote repositories; how a remote repository's index is affected
  is unspecified.

## Remote repositories

### PKG-SWIFT-014 · Proxying a Swift registry
Priority: **P2**

A remote Swift repository proxies an upstream that implements the registry specification
(RMT-001 in [../remote-repositories.md](../remote-repositories.md)).

- Supported upstreams: private Swift registries, a Swift repository on
  another instance of the system (chained remote, RMT-040), and the public Tuist registry at
  `https://tuist.dev/api/registry/swift`.
- Not an upstream: `https://swiftpackageregistry.com` is an HTML catalog that points to Git
  repositories and hosts no packages.
- The default upstream URL is unspecified.
- Settings not supported for Swift remotes: CDN download redirect.
- Cached archives must keep the upstream's checksum and signature unchanged, since clients verify
  them against the release metadata.

### PKG-SWIFT-015 · External dependencies on remote repositories
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Allow external dependencies | Lets external dependencies be included in the remote repository | unspecified |
| External dependency patterns | Optional Ant-style include patterns restricting which external dependencies are included (REPO-048 in [../repositories.md](../repositories.md)) | unspecified |

What counts as an external dependency for Swift and how it is fetched is unspecified. Open: either
both fields apply to Swift remotes, as the remote-repository settings list them, or the remote
external-dependency fields apply to Docker repositories only (with "off" as default); the spec
decides. UI location: remote repository settings, format-specific section.

## Virtual repositories

### PKG-SWIFT-016 · Virtual Swift repositories
Priority: **P2**

A virtual Swift repository aggregates local and remote Swift repositories under one registry URL
(REPO-033 in [../repositories.md](../repositories.md)).

- Release lists, release metadata, manifests and archives resolve across members in the virtual
  repository's order (REPO-035).
- Publishing through the virtual URL goes to the default deployment repository (REPO-038);
  unspecified when none is set.
- Merge behavior for `Link` headers (`latest-version`, predecessor and successor) across members is
  unspecified.

## Resolution behavior

### PKG-SWIFT-017 · Replacing source-control dependencies with registry identities
Priority: **P2**

Since Swift 5.9, SwiftPM resolves dependencies from Git by default. To resolve through the
registry:

| Case | Command | Effect |
|---|---|---|
| Local repository | `swift package resolve` | Dependencies declared with `.package(id:…)` resolve from the registry. |
| Remote or virtual repository | `swift package --replace-scm-with-registry resolve` | SwiftPM also maps dependencies declared with `.package(url:…)` to registry identities (through PKG-SWIFT-008) and resolves them from the registry. The flag is required for remote and virtual repositories that proxy a registry such as Tuist. |

- Xcode does not perform this substitution. Direct dependencies declared by identity resolve
  through the registry; transitive dependencies declared by URL still resolve from Git. The
  remedy is to replace URL dependencies by identity dependencies in `Package.swift`. The flag
  above exists only for the SwiftPM command line.

### PKG-SWIFT-018 · Supported client versions
Priority: **P2**

| Item | Requirement |
|---|---|
| SwiftPM | 5.7 or later |
| SwiftPM 5.8 and later | Need a server that supports their authentication flow (PKG-SWIFT-010) |
| Publish command | SwiftPM 5.9 or later; earlier clients upload with a plain HTTP client (PKG-SWIFT-009) |

## Notes for implementers

- SwiftPM verifies every downloaded archive against the SHA-256 `checksum` of the release metadata
  and pins it in `Package.resolved`; recomputing or re-zipping an archive breaks every lockfile
  that references it.
- Scopes and names are case-insensitive; storage paths and database keys must normalize case, or
  the same package splits into two.
- `GET /<scope>/<name>/<version>` serves JSON metadata while `PUT` on the same path publishes;
  routing must dispatch on method and must not treat a path ending in `.zip` or `/Package.swift`
  as a version.
- Every response except manifests and archives must carry `Content-Version: 1`; SwiftPM's
  registry client rejects a response whose version differs from the one it asked for.
- Errors must be RFC 7807 problem documents; SwiftPM shows the `detail` text to the user.
- The manifest endpoint must advertise every `Package@swift-*.swift` variant in `Link` headers and
  redirect with 303 for a missing `swift-version`; otherwise multi-manifest packages resolve the
  wrong manifest on older toolchains.
- Without the identifier lookup (PKG-SWIFT-008), `--replace-scm-with-registry` silently falls back
  to Git, so a remote or virtual repository looks configured but is never used.
- A redirect to object storage for archive downloads must stay on HTTPS; SwiftPM refuses
  downgrades.
- Credentials are bound to the host in `.netrc`; registry URLs embedded in `Link` headers and
  release `url` fields must use the same host as the configured registry or the client sends them
  unauthenticated.
