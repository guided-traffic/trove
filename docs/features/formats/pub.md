# pub package format (Dart and Flutter)

This page catalogs what a repository manager does to act as a pub package repository for the Dart
and Flutter command lines: hosting packages in local repositories, proxying and caching an upstream
such as `https://pub.dev` in remote repositories, merging both behind one hosted URL in virtual
repositories, token authentication, publishing, metadata calculation and version handling. The
wire protocol is defined by the Dart team in the
[Hosted Pub Repository Specification Version 2](https://github.com/dart-lang/pub/blob/master/doc/repository-spec-v2.md)
(cited below as "the pub specification"); where an entry relies on it, it says so. Repository
classes in general are described in [../repositories.md](../repositories.md), pull-through caching
in [../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/pub/<repo-key>` stands for the hosted URL of one
repository and `https://repo.example.com/files/<repo-key>/<path>` for raw file access to it (both
path layouts are the implementation's choice), `pub-local`, `pub-remote` and `pub-virtual` are
example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-PUB-001 · pub repositories in all three repository classes
Priority: **P2**

pub is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's Dart and Flutter packages; the system calculates their
  package and release metadata (PKG-PUB-013, PKG-PUB-016);
- remote repositories proxy and cache an upstream pub repository (PKG-PUB-017);
- virtual repositories expose local and remote members under one hosted URL (PKG-PUB-018);
- the native `dart pub` and `flutter pub` commands publish and resolve.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the pub format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)). A remote form asks to verify the upstream
URL (its pre-filled value is unspecified); a virtual form asks for one or more members.

Shared settings excluded for pub, per repository class:

| Setting | Excluded for |
|---|---|
| Priority resolution (REPO-018 in [../repositories.md](../repositories.md)) | Local repositories; for remote repositories unspecified |
| CDN download redirect (REPO-020) | Local and remote repositories |
| Scanner indexing (REPO-021) | Local repositories; most likely remote repositories too (unspecified) |

Federation: open. Either every format except the Terraform state backend can be federated, pub
included (REPL-026 in [../replication-and-federation.md](../replication-and-federation.md)), or pub
is not a federated format; the spec decides.

### PKG-PUB-002 · Hosted URL of a repository
Priority: **P2**

Every pub repository answers the pub specification under its own base URL, the *hosted URL*. Rules
from the pub specification:

- it always has an `http://` or `https://` scheme and is normalized without a trailing slash; all
  endpoints are appended with a leading slash;
- it must not contain user information (`https://user:pass@host`), a query string or a fragment,
  so credentials cannot be embedded in the URL (PKG-PUB-011);
- it may contain a path prefix, which is how one host serves many repositories.

## Protocol endpoints

### PKG-PUB-003 · API version header, errors and retries
Priority: **P2**

| Aspect | Behavior (pub specification) |
|---|---|
| Version negotiation | The client sends `Accept: application/vnd.pub.v2+json`; responses use `Content-Type: application/vnd.pub.v2+json`. A request without `Accept` should be treated as version 2. |
| Client identity | `User-Agent: Dart pub <sdk-version>` |
| Error body | `{"error": {"code": "<code>", "message": "<message>"}}` on 4xx; the client prints the message. |
| Retries | The client retries failed requests with exponential backoff. |

Open: SemVer 2.0 versions are served either on the v2 pub API only, the only version the pub
specification defines, or on both a v2 and a v3 pub API. What a v3 API denotes is unspecified.

### PKG-PUB-004 · List all versions of a package
Priority: **P2**

`GET <hosted-url>/api/packages/<package>` returns the package document:

| Field | Meaning |
|---|---|
| `name` | Package name |
| `isDiscontinued`, `replacedBy` | Optional; discontinued flag and suggested replacement (PKG-PUB-010) |
| `advisoriesUpdated` | Optional; when present the client assumes PKG-PUB-008 is served |
| `latest` | The latest version entry |
| `versions` | Array of version entries |

Each version entry carries `version`, optional `retracted`, `archive_url`, optional
`archive_sha256` (hex SHA-256 of the archive) and `pubspec` (the package's `pubspec.yaml` as a
JSON object).

- The system serves this document from its per-package index (PKG-PUB-013) for local
  repositories, from the cached upstream document for remote repositories, and merged for virtual
  repositories.
- Which version is `latest` follows SemVer 2.0 precedence (PKG-PUB-015).
- `archive_url` values must point to the system, not to the upstream, so downloads go through the
  cache and stay authorized (PKG-PUB-005).

### PKG-PUB-005 · Archive download
Priority: **P2**

The client downloads a version with `GET <archive_url>`, following redirects; the final response
must be a gzipped tar archive.

- `archive_url` may be temporary and may carry a query string, for example a signed object-storage
  URL; the pub specification advises an expiry of at least 25 minutes.
- Authorization prefix rule: the client sends its bearer token to `archive_url` only when the
  hosted URL is a string prefix of `archive_url`. An archive URL outside that prefix (another path
  on the same host included) is fetched without credentials.
- The client verifies `archive_sha256` when present.

### PKG-PUB-006 · Publish a package version
Priority: **P2**

`dart pub publish` and `flutter pub publish` run a three-step flow defined by the pub
specification:

| Step | Request | Response |
|---|---|---|
| 1. Request an upload | `GET <hosted-url>/api/packages/versions/new` with `Authorization: Bearer` (required) | `{"url": "<upload-url>", "fields": {…}, "attestationUrl": "<url>"}` (`attestationUrl` optional, PKG-PUB-007) |
| 2. Upload | `POST <upload-url>` as `multipart/form-data` with every returned field plus a `file` part holding the `tar.gz` | `204 No Content` with `Location: <finalize-url>`, or `303 See Other` to it |
| 3. Finalize | `GET <finalize-url>` | `200` with `{"success": {"message": "…"}}`, or `400` with an error body |

- The upload and finalize URLs may be temporary and carry query strings; the flow is designed so
  the upload can go straight to object storage (pre-signed POST).
- The server may inspect the archive at finalize time and reject it with a 400 error body (the
  specification's examples: archive size limits, forbidden Git dependencies, invalid attestations,
  any other repository rule); the version counts as published only after a successful finalize.
- Publishing is specified only through the client commands; that the system follows this flow is
  implied by the client working against it. Overwriting an existing version and the
  answer to a duplicate publish are unspecified.
- Required privilege: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)). Publishing through a virtual repository
  goes to its default deployment repository (REPO-038 in [../repositories.md](../repositories.md)).

### PKG-PUB-007 · Publishing with an attestation
Priority: **P2**

Optional protocol extension (pub specification): the step-1 response carries
`attestationUrl`; the client posts a Sigstore bundle there before uploading the archive, and the
server verifies the bundle against the uploaded archive at finalize time (malformed bundle,
unverifiable signature or digest mismatch reject the publication). A repository that does not
support attestations omits `attestationUrl`, and a client holding an attestation then aborts.
Whether the system supports this is unspecified.

### PKG-PUB-008 · Security advisories for a package
Priority: **Later**

Optional endpoint of the pub specification: `GET <hosted-url>/api/packages/<package>/advisories`
returns `{"advisories": [<OSV records>], "advisoriesUpdated": "<date-time>"}` with fully populated
`affected[].versions`. The client uses it only when the package document carries
`advisoriesUpdated`. Whether the system serves it is unspecified; serving it requires
vulnerability data (see [../security-scanning-and-curation.md](../security-scanning-and-curation.md)).

### PKG-PUB-009 · Deprecated per-version endpoints
Priority: **P2**

The pub specification asks servers to keep serving two endpoints for older clients:

| Endpoint | Returns |
|---|---|
| `GET <hosted-url>/api/packages/<package>/versions/<version>` | `{"version", "archive_url", "pubspec"}` of one version |
| `GET <hosted-url>/packages/<package>/versions/<version>.tar.gz` | The archive, usually through a redirect |

Whether the system serves them is unspecified.

### PKG-PUB-010 · Discontinued packages and retracted versions
Priority: **P2**

The package document can mark a package as discontinued (with an optional replacement) and single
versions as retracted (pub specification). For remote repositories these flags come from the
upstream document. Whether local repositories can set them, and through which operation, is
unspecified.

## Client authentication and setup

### PKG-PUB-011 · Token authentication
Priority: **P2**

The pub client stores one opaque token per hosted URL and sends it as `Authorization: Bearer
<token>` (TOK-033 in [../access-tokens.md](../access-tokens.md)).

```shell
dart pub token add "https://repo.example.com/pub/pub-virtual"      # prompts for the token on stdin
flutter pub token add "https://repo.example.com/pub/pub-virtual"
```

Server answers required by the pub specification:

| Situation | Response | Client reaction |
|---|---|---|
| Missing, invalid or expired token | `401` with `WWW-Authenticate: Bearer realm="pub", message="<text>"` | Aborts, deletes the stored token, prints the message |
| Valid token without permission | `403` with the same header form | Aborts, keeps the token, prints the message |

- The client sends tokens only over HTTPS. The pub specification requires tokens to match
  `^[a-zA-Z0-9._~+/=-]+$` because they are inserted verbatim into the header, so the system's token
  alphabet must stay within that set.
- The `message` is the place to tell users where to create a token, for example the system's
  token page (TOK-025).
- Anonymous resolution follows the global anonymous-access setting (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)).

### PKG-PUB-012 · Client setup for Dart and Flutter
Priority: **P2**

1. Store the token (PKG-PUB-011).
2. Point the client at the repository:

   ```shell
   export PUB_HOSTED_URL="https://repo.example.com/pub/pub-virtual"
   ```

3. Resolve with `dart pub get` or `flutter pub get`; publish with `dart pub publish` or
   `flutter pub publish` after raising the version in `pubspec.yaml`.

Alternatives defined by the pub specification: `publish_to: <hosted-url>` in `pubspec.yaml` sets
the publish target per package, and a dependency can name its repository with
`hosted: {url: <hosted-url>, name: <package>}`. The client setup instructions (UI-021 and UI-026 in
[../ui-inventory.md](../ui-inventory.md)) offer these snippets with the user's token filled in.

## Local repositories

### PKG-PUB-013 · Storage layout and per-package index
Priority: **P2**

| Item | Location |
|---|---|
| Package archive | `<package>/<package>-<version>.tar.gz` |
| Version index | One JSON document per package in a hidden metadata folder of the repository, listing the package's versions |

- Archives deployed elsewhere are stored but never indexed.
- `dart pub publish` and `flutter pub publish` place archives correctly on their own; the layout
  matters for manual deployments (PKG-PUB-014).
- The index document is what PKG-PUB-004 serves; its stored form is the implementation's choice.

### PKG-PUB-014 · Deploy through the UI or file upload
Priority: **P2**

Archives can be deployed as plain files through the UI deploy dialog (choose the repository, drop
or select the file, keep or change the proposed target path) or the deploy operation (ART-002 and
ART-006 in [../artifacts.md](../artifacts.md)); the target path must follow PKG-PUB-013. Whether
the index is updated automatically after such a deployment or only through PKG-PUB-016 is
unspecified.

### PKG-PUB-015 · SemVer 2.0 versions
Priority: **P2**

pub repositories apply [Semantic Versioning 2.0](https://semver.org/#semantic-versioning-200):
pre-release identifiers with dots and build metadata are accepted (examples: `3.0.0-build.60`,
`1.0+git.52406`). Download requests are answered under SemVer 2.0 rules; when the
newest version of a package is a SemVer 2.0 version, it is returned as the latest. This holds for
local, remote and virtual repositories.

### PKG-PUB-016 · Recalculate pub repository metadata
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate pub metadata | Repository key; optional flag to run asynchronously | Plain-text acknowledgement; 401, 403 or 500 on failure | System administrator for the UI action; unspecified for the API operation, which answers 403 without the needed permission |

- Recomputes the package and release metadata of the repository, asynchronously. Open: it is
  supported either for local and remote repositories or for local repositories only; the spec
  decides. What it does on a remote repository is unspecified.
- UI location: artifact browser, context menu of the repository, "recalculate index" (REPO-009 in
  [../repositories.md](../repositories.md)).

## Remote and virtual repositories

### PKG-PUB-017 · Remote pub repositories
Priority: **P2**

A remote pub repository proxies and caches an upstream pub repository (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)), typically `https://pub.dev`.

- The default upstream URL is unspecified. Beyond the upstream URL, the behavior of a pub remote is
  unspecified; the next two points are inferred.
- Package documents are cached like other metadata for the metadata retrieval cache period
  (RMT-018), and their `archive_url` values are rewritten to the system; otherwise downloads
  bypass the cache and lose the bearer token (PKG-PUB-005).
- `archive_sha256` must be passed through unchanged.

### PKG-PUB-018 · Virtual pub repositories
Priority: **P2**

A virtual pub repository merges the package documents of its members (REPO-033 and REPO-037 in
[../repositories.md](../repositories.md)) and resolves archives in member order (REPO-035). The
merge rule for a version present in several members, and how `latest`, `isDiscontinued` and
`retracted` are combined, are unspecified.

### PKG-PUB-019 · Supported client versions
Priority: **P2**

A pub client of version 2.15.0-268.8.beta or later (the Dart SDK version) is required. No reason is
specified; the inferred one is that Dart 2.15 is where token authentication for third-party
repositories (PKG-PUB-011) arrived.

## Notes for implementers

- The hosted URL is matched as a plain string prefix for credentials: an `archive_url`, upload URL
  or finalize URL outside it is requested without the token and fails on a private repository.
- Return 401 only for a missing or invalid token; returning 401 for "valid token, no permission"
  makes the client delete a working token.
- The `WWW-Authenticate` header must use `Bearer realm="pub", message="…"`; the message is the only
  hint users see.
- The publish flow is three requests; a version must not become visible before the finalize
  request succeeds.
- `pubspec` in the package document is JSON converted from YAML; the client resolves dependency
  constraints and SDK constraints from it, so it must be complete, not a summary.
- Hosted URLs cannot carry credentials, so CI setups depend on `dart pub token add` with
  `--env-var` or a pre-seeded token store; plan the client setup instructions for that.
- Version ordering must follow SemVer 2.0 precedence, including pre-release identifiers, when
  choosing `latest`.
