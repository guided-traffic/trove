# Artifact management

This page covers what users and clients do with the individual items stored in repositories:
deploying files (single file, by checksum, as an exploded archive, with properties attached at
deploy time), downloading them (single files, entries inside archives, whole folders as archives,
time-limited signed links, templated downloads), annotating items with properties and property
sets, copying, moving and deleting items, recovering deleted items from the trash can, checksum
handling, item metadata read-outs (item information, file lists, download statistics) and the two
ways of looking at stored content: the package view and the file tree. Repository configuration is
in [repositories.md](repositories.md), search and the query language in
[search-and-query.md](search-and-query.md), physical storage and garbage collection in
[storage-and-blobs.md](storage-and-blobs.md), and the operation catalog in
[api-content-operations.md](api-content-operations.md).

## Content model

### ART-001 · Items, artifacts and packages
Priority: **MVP**

The system manages several object types; this page deals with the first two.

| Object | Meaning |
|---|---|
| Item | Anything addressable by repository key and path: the repository root, a folder or a file. Each item operation below states which of the three it accepts. |
| Artifact | A single stored file. Its content is identified by its checksum (see ART-008). |
| Package | A logical unit named by the format's own metadata, for example the name in an npm `package.json` or the image manifest of a Docker image. |
| Build, release set, scan result | Covered on [builds-and-ci.md](builds-and-ci.md), [distribution-and-lifecycle.md](distribution-and-lifecycle.md) and [security-scanning-and-curation.md](security-scanning-and-curation.md). |

Behavior:

- A package consists of one artifact (an npm tarball) or of several (a Docker image: a manifest
  plus one artifact per layer). The system parses the package metadata and tracks which artifacts
  belong to which package.
- How an upload is classified:

  | Uploaded content | Result |
  |---|---|
  | Any file into a generic repository | Stored as an artifact only. |
  | A package file into a repository of the same format | Stored and indexed as a package. |
  | A multi-artifact package pushed with the format's native client | The system assembles the package from the uploaded parts. |

- Format-specific repositories parse metadata and maintain indexes. Non-package content (images,
  text files and similar) placed in a format repository degrades indexing and performance; such
  content belongs in a generic repository.

Edge cases: moving a single artifact out of a multi-artifact package by hand (for example by drag
and drop in the file tree) can strip it of its package context and break the package.

Interactions: the package view (ART-048) and the file tree (ART-040) are two views on the same
items.

## Deploying artifacts

### ART-002 · Deploy a single artifact or create a folder
Priority: **MVP**

The generic upload: an HTTP `PUT` of the raw file content to the item's URL
(`<repository URL>/<path>`). Format-native clients use their own protocols; this operation works
for every repository that accepts deploys and is the basis of raw file access.

| | |
|---|---|
| Inputs | Repository key; path; request body with the binary content; optional request headers carrying the client's SHA-1 and/or SHA-256 of the content; optional matrix parameters (ART-003). |
| Create folder | A path ending in `/` with no body creates an empty folder. Matrix parameters attach properties to the folder. |
| Output | `201 Created` with a JSON description of the new item: repository, path, creation time, creator, download URL, MIME type, size in bytes (as a string), server-computed checksums, client-supplied ("original") checksums, and a URL of the item. |
| Privilege | deploy on the target path (anonymous possible where granted). |

<details>
<summary>Errors</summary>

| Status | Cause |
|---|---|
| 400 | Invalid path or malformed request. |
| 401 | Authentication failed. |
| 403 | No deploy permission on the repository. |
| 404 | Repository does not exist. |
| 405 | The target repository does not accept uploads, for example a virtual repository. A virtual repository accepts deploys only once it has a default deployment repository (see [repositories.md](repositories.md)). |
| 409 | Duplicate artifact, or a client-supplied checksum does not match the received content. |

</details>

Edge cases:

- A supplied SHA-1 or SHA-256 that does not match the content rejects the deploy (409).
- When an existing path counts as a duplicate (as opposed to being overwritten) is unspecified.
  Repository policies such as release/snapshot handling and include/exclude patterns can also
  reject the deploy (ART-006).
- For large uploads the creation time recorded for the item can be later than its last-modified
  time (ART-027).

Interactions: checksum computation (ART-008); deploy by checksum (ART-004); events on
[events-and-extensibility.md](events-and-extensibility.md).

### ART-003 · Properties attached at deploy time (matrix parameters)
Priority: **P1**

A deploy request may carry properties as matrix parameters: `;key=value` segments that can appear
anywhere in the request URL (the matrix-URI convention, in addition to query and path parameters).
On deploy, every matrix parameter becomes a property of the deployed artifact.

- Placement: when the matrix parameters are attached to the base URL of the deploy target (for
  example directly after the repository key), the artifact path follows them. Both forms below
  deploy the same file with the same two properties:

  ```text
  PUT <repository URL>;ci.run=8812;git.sha=4f2a9c1/com/example/widget/2.3/widget-2.3.jar   # example
  PUT <repository URL>/com/example/widget/2.3/widget-2.3.jar;ci.run=8812;git.sha=4f2a9c1   # example
  ```

- Build tools can template the deploy URL; Maven, for example, substitutes project properties such
  as a build number or a revision into a distribution URL that carries matrix parameters, so every
  deployed file receives the values of that build.
- UI: the target-path field of the deploy dialog (ART-006) accepts matrix parameters.

Interactions: the same syntax constrains resolution on download (ART-012). Properties set this way
follow the property model of ART-021 (length limits, multi-value).

### ART-004 · Deploy by checksum
Priority: **P1**

Lets a client create an artifact from content the system already stores, without sending the
bytes again.

| | |
|---|---|
| Inputs | Repository key; target path; a request header that switches deploy-by-checksum on (must be true); at least one checksum header: SHA-1, SHA-256, or a generic checksum header whose algorithm is inferred from its length (32 hex characters MD5, 40 SHA-1, 64 SHA-256); optional body; optional matrix parameters (ART-003). |
| Content found | If the system holds an artifact with that checksum **that the caller can read**, its content is copied server-side to the new path without any upload, and the call answers `201 Created` with the same item description as ART-002. |
| Content not found | `404 Not Found`, telling the client to upload the content with a regular deploy. |
| Switch set to false | The request behaves as a normal upload: `201 Created` even for new content, and the submitted checksums are recorded as verified-identical. |
| Privilege | deploy on the target path (anonymous possible where granted). |
| Errors | 400 no checksum header or an invalid checksum value; 401; 403 no deploy permission. |

Security: the read-permission condition is what keeps this operation from handing out content to
anyone who merely knows its hash.

Interactions: content-addressed storage and deduplication on
[storage-and-blobs.md](storage-and-blobs.md); checksum algorithms (ART-008).

### ART-005 · Deploy an archive and extract it on arrival
Priority: **P2**

Uploads one archive in a single HTTP request and deploys its entries as individual artifacts,
keeping the archive's internal folder structure. Only the extracted entries are stored, not the
archive itself.

| | |
|---|---|
| Inputs | Repository key; target path ending with the archive's file name; a request header that switches extraction on (must be true); optional request header requesting an atomic extraction; body: the archive. |
| Archive types | `zip`, `tar`, `tar.gz`, `tgz` |
| Output | `201 Created` with a plain-text body. |
| Privilege | deploy on the target path (anonymous possible where granted). |
| Errors | 400 invalid archive or missing switch header; 401; 403 no deploy permission. |

Settings:

| Setting | Meaning | Default |
|---|---|---|
| Atomic extraction (per request) | Extract in one atomic operation. | unspecified |
| Parallel extraction threads (system-wide) | Number of threads deploying the entries of one archive in parallel. | sequential deployment |

Limits and edge cases:

- Compressed archives (unlike plain `tar`) can cost considerable CPU on the server.
- The target path names the archive file and marks where the entries are extracted; the exact
  extraction root relative to that name, the behavior for entries that already exist, and partial
  failure without the atomic option are unspecified.

UI location: deploy dialog, "deploy as bundle" (ART-006).

### ART-006 · Deploy dialog in the UI
Priority: **P1**

UI location: file tree (ART-040), action menu, "Deploy (upload)".

Behavior:

- The user picks a target repository, then either a single-file tab (default) or a multi-file tab,
  and drops or selects files.
- An optional target path places the file below a custom path in the target repository; the field
  also accepts matrix parameters (ART-003).
- Options shown depending on the file type:

  | Option | Meaning | Default |
  |---|---|---|
  | Deploy according to layout | Exposes the tokens of the target repository's layout (module, version, classifier and so on) for editing; edits update the target path. See [repositories.md](repositories.md) (repository layouts). | unspecified |
  | Deploy as bundle | The uploaded `zip`, `tar`, `tar.gz` or `tgz` archive is extracted on deploy; the archive must already contain the folder structure the files should have in the repository (same mechanism as ART-005). | unspecified |
  | Deploy as Maven artifact | Exposes Maven coordinates (group ID, version, classifier, type), pre-filled by parsing the file name; edits update the target path. | unspecified |
  | Generate default POM / use the JAR's internal POM | For Maven artifacts whose target has no POM: deploy a generated POM or the POM embedded in the JAR. A manually edited POM must stay valid. | unspecified |

Limits:

- UI uploads are capped by the system-wide setting "maximum UI upload size (MB)" (general
  settings, administrator). The UI accepts files up to 100 MB; whether 100 MB is that setting's
  default or a fixed ceiling is unspecified. Larger files go through a native client, the companion
  CLI or multi-part upload (ART-007).
- File names may not contain `|`, `*`, `?` or `"`; `&` is allowed inside a name but not as a path
  token; a space directly before `/` is rejected.

Edge cases: the usual causes of a failed deploy are missing deploy permission, a path rejected by
the target repository's include/exclude patterns, and a conflict with its release/snapshot
handling policy. Deploying to a virtual repository requires that repository to have a default
deployment repository (one of its aggregated local repositories); deploys are then written there
and the client setup instructions for the virtual repository include deploy snippets (see
[repositories.md](repositories.md)).

### ART-007 · Multi-part upload of large files through object storage
Priority: **P2**

A client-driven upload that sends large files in parts directly to object storage and can resume
after a failure without losing the parts already sent; a plain single-request upload loses
everything on failure.

Sequence:

1. The client authenticates and announces the upload.
2. The client splits the file and asks the server for pre-signed object-storage URLs for the next
   batch of parts (five by default, equal to the number of parts sent concurrently).
3. The server returns the pre-signed URLs; the client uploads the parts directly to the bucket.
4. Steps 2–3 repeat until all parts are uploaded.
5. The client sends the complete-upload call to object storage, which merges the parts; merging
   may take time, and the client polls the server for progress meanwhile.
6. The server verifies the checksum of the merged object and reports success.

<details>
<summary>Settings</summary>

| Setting | Side | Meaning | Default |
|---|---|---|---|
| Multi-part upload enabled | Server, system-wide | Turns the feature on for self-hosted installations. | unspecified |
| Minimum file size for splitting | Client | Smaller files are uploaded in one request. | 200 MB |
| Concurrent parts per file | Client | Number of parts uploaded in parallel and number of pre-signed URLs requested per batch. | 5 |
| Part size | Client | Size of each part. | 20 MB |

Client settings apply to one invocation and revert to the defaults afterwards.

</details>

Limits and prerequisites:

- Requires S3 or Google Cloud Storage as the binary store.
- Not supported when the S3 store is sharded or uses client-side KMS encryption.
- With server-side encryption by an AWS KMS key, the identity used for the bucket needs
  `kms:Decrypt` and `kms:GenerateDataKey` on the key, because S3 has to read the encrypted parts
  to complete the upload. If the IAM user or role lives in the key's AWS account, the key policy
  must grant the two actions; from another account, both the key policy and the IAM identity must.

Interactions: storage backends on [storage-and-blobs.md](storage-and-blobs.md); checksum
verification of the result (ART-008).

## Checksums

### ART-008 · Checksum computation and exposure
Priority: **MVP**

On every deploy the system computes MD5, SHA-1 and SHA-256 of the content and stores them with
the file's metadata. Content identity is the checksum: two files with the same checksum are the
same content (ART-001, ART-004).

Two checksum sets are kept per file:

| Set | Source |
|---|---|
| Actual checksums | Computed by the server from the received bytes. |
| Original checksums | As supplied by the deploying client. What is reported when the client supplied none is unspecified. |

Where checksums appear:

| Place | Content |
|---|---|
| Deploy response (ART-002, ART-004) | Actual and original checksums. |
| Item information (ART-027) and the General tab of the item details | MD5, SHA-1, SHA-256, actual and original. |
| File list (ART-028) | SHA-1 per file. |
| Download response | Response headers carrying the file's checksums, including SHA-256. |
| Folder archive download (ART-015) | Optional checksum files, including SHA-256. |
| Checksum files on the browsing endpoint | `<file>.md5`, `<file>.sha1`, `<file>.sha256` are listed and downloadable next to the file, under the conditions below. |
| Search and query language | Search by checksum, search for bad checksums ([search-and-query.md](search-and-query.md)). |

Checksum files are served on the browsing endpoint (ART-042) when either

- the deploying client supplied the checksum with the upload, or
- the repository's checksum policy trusts server-generated checksums (ART-009);

in the second case the client need not send anything for the files to appear.

Edge cases: SHA-512 is not part of this checksum set; formats that need it (npm integrity, NuGet
package hash) are covered on [formats/npm.md](formats/npm.md) and
[formats/nuget.md](formats/nuget.md).

### ART-009 · Client checksum verification policy
Priority: **P1**

A per-repository setting, offered for Maven, Gradle, Ivy and sbt local repositories, that decides
what happens when a client's checksum for a deployed file is missing or disagrees with the one the
server computed.

| Policy | Behavior | Default |
|---|---|---|
| Verify against client checksums | If the client sent no valid checksum for a deployed file, a request for that file's checksum file answers `404`. If the client sent a checksum that conflicts with the server's, the checksum file answers `409 Conflict` until a correct checksum is deployed. | yes |
| Trust server-generated checksums | Client checksums are not verified; the server's checksums are served. The artifact is usable immediately, at the price of no end-to-end integrity check. | — |

How clients supply checksums: as request headers on the upload (ART-002, rejected with 409 on
mismatch) or by deploying the checksum after the artifact; the 409 on the checksum file lasts until
a valid checksum is deployed. Maven-style clients do the latter by uploading `.sha1`/`.md5` files
next to the artifact.

UI location: repository settings, format-specific section, "checksum policy" (see
[repositories.md](repositories.md)).

Edge cases: a mismatch has two different reactions, an immediate 409 on a deploy carrying checksum
headers (ART-002) and a 409 on the checksum file under the verify policy; how the two interact is
unspecified.

### ART-010 · SHA-256 backfill operation
Priority: **P2**

A legacy administrative operation from a time when SHA-256 was not computed at deploy time.

| | |
|---|---|
| Inputs | JSON body with repository key and path (file or folder), both required. |
| Behavior | Computes the SHA-256 of the file and attaches it as a property whose key names the algorithm. For a folder, every file below it is processed recursively. |
| Output | `200 OK`. |
| Privilege | administrator. |
| Errors | 400 malformed body; 401; 403 not an administrator. |

Related: a separate background migration fills in SHA-256 for files stored before native support
existed. For each record without SHA-256 it looks for records with the same SHA-1, reuses a known
SHA-256 among them, and otherwise computes it once and applies it to all of them. A system that computes SHA-256 for every file from the
first deploy (ART-008) needs neither; the operation matters only for API compatibility.

## Downloading artifacts

### ART-011 · Retrieve an artifact
Priority: **MVP**

The generic download: an HTTP `GET` of `<repository URL>/<path>` on a local, remote or virtual
repository. Remote repositories fetch and cache on a miss; virtual repositories resolve through
their members (see [remote-repositories.md](remote-repositories.md) and
[repositories.md](repositories.md)).

| | |
|---|---|
| Inputs | Repository key; path; optional matrix parameters (ART-012); optional version tokens in the path (ART-013); optional flag to skip updating download statistics; optional trace flag. |
| Output | `200 OK` with the content; response headers carry the file's checksums (ART-008). With the trace flag, a plain-text report of the resolution process instead of the content. |
| Privilege | read (anonymous possible where granted). |
| Errors | 401; 403 no read permission; 404 artifact does not exist. |

Settings (per request):

| Setting | Meaning | Default |
|---|---|---|
| Skip statistics | Do not count this download in the download statistics (ART-029). | false |
| Trace | Simulate the retrieval and return a verbose text report of the resolution process, for debugging. | off |

Interactions: signed URLs (ART-018) grant this operation without credentials; filtered resources
(ART-020) render the content on the way out; download blocking by scan policies is on
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### ART-012 · Property-constrained resolution (matrix parameters on download)
Priority: **P2**

A download request may carry matrix parameters that constrain which artifact is served. Two forms
exist:

| Form | Syntax | Rule |
|---|---|---|
| Non-conflicting | `;key=value` | The artifact qualifies if it has no property `key`, or has `key` with exactly `value`. It fails only when it carries `key` with a different value. |
| Mandatory | `;key+=value` | The artifact qualifies only if it carries `key` with exactly `value`. |

A non-qualifying artifact is answered with `404 Not Found`.

| Artifact has | Request carries | Result |
|---|---|---|
| `color=black` | `;color=black` | 200 |
| no `color` (nothing, or only `height=50`) | `;color=black` | 200 |
| `color=red` | `;color=black` | 404 |
| `color=black` | `;color+=black` | 200 |
| no `color` (nothing, or only `height=50`) | `;color+=black` | 404 |
| `color=red` | `;color+=black` | 404 |

Edge cases: matching of multi-valued properties (whether one matching value suffices) is
unspecified.

Interactions: deploy-time matrix parameters (ART-003); templated downloads receive the request's
matrix parameters as part of their property context (ART-020).

### ART-013 · Latest-version tokens in download paths
Priority: **P2**

A download path may name a version symbolically and is resolved to the newest matching file.

| Case | Token in the version position | Resolves to |
|---|---|---|
| Maven, latest integration | Maven's own `SNAPSHOT` version | The newest integration (snapshot) artifact. |
| Maven, latest release | a reserved latest-release token | The newest release artifact. |
| Other layouts, latest integration | a reserved latest-integration token, placed where the repository layout has its folder and file integration-revision tokens | The newest integration version. |
| Other layouts, latest release | the reserved latest-release token in the same places | The newest release version. |

Rules:

- A path may not combine an integration token with a release token.
- Only local repositories, remote-repository caches and virtual repositories take part; upstreams
  are not queried for this.
- For non-Maven layouts, both integration-revision tokens (folder and file) must be defined in the
  repository layout (see [repositories.md](repositories.md), repository layouts).
- For the non-Maven tokens, "newest" is decided by alphabetical sorting. A system-wide setting
  switches latest-version resolution to the creation date instead; changing it requires a restart.
  Default: alphabetical. How the Maven tokens order versions is unspecified.

### ART-014 · Download an entry from inside an archive
Priority: **P2**

Returns one file from inside a stored archive without downloading the archive. The path is the
archive's path, then `!`, then `/` and the entry path, for example
`<repository URL>/com/example/widget/2.3/widget-2.3.jar!/META-INF/MANIFEST.MF` *(example)*. The `/` after
the `!` is mandatory.

| | |
|---|---|
| Output | `200 OK` with the entry's content (text or binary). |
| Privilege | read on the archive (anonymous possible where granted). |
| Errors | 400 invalid path, archive missing or malformed entry path; 401; 403; 404 archive or entry not found. |

Supported archive formats: unspecified. Interactions: content browsing (ART-045).

### ART-015 · Download a folder or a whole repository as an archive
Priority: **P2**

Streams every artifact below a folder, or below a repository's root, as one archive.

| | |
|---|---|
| Inputs | Repository key; path (empty for the repository root); archive type (required): `zip`, `tar`, `tar.gz` or `tgz`; flag to include checksum files. |
| Output | `200 OK` with the archive. |
| Applies to | Local repositories and remote-repository caches only. |
| Privilege | authenticated user with read on the path; anonymous users only when the anonymous switch below is on. |
| Errors | 400 invalid path characters or archive type; 401; 403 no read permission, or download blocked by a scan policy; 404 repository or path not found. |

<details>
<summary>System settings (general settings, administrator)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enable folder download | Master switch; the operation is unavailable without it. | unspecified |
| Enable folder download for anonymous access | Allows anonymous users to use it. | unspecified |
| Maximum size (MB) | Largest total size of one folder download. | unspecified |
| Maximum number of files | Largest number of artifacts in one folder download (folder and all subfolders). | unspecified |
| Maximum parallel folder downloads | Number of folder downloads that may run concurrently. | unspecified |

</details>

Checksum files: when requested, checksum files (including SHA-256) are written into the archive
under the same conditions that make them visible on the browsing endpoint (ART-008); for
remote-repository caches the checksum policy does not apply and the checkbox always adds them.

Edge cases: if any file below the folder is blocked for download, the whole request fails with
`403 Forbidden`. The response when a size or count limit is exceeded, or when the parallel limit
is reached, is unspecified.

UI location: file tree, context menu of a folder, "Download", or the download button in the item
details; a dialog asks for the archive type and the "include checksum files" option.

### ART-016 · Download the artifacts of a build as an archive
Priority: **P2**

Returns one archive with all artifacts recorded for a build (see
[builds-and-ci.md](builds-and-ci.md)).

<details>
<summary>Request fields (JSON body)</summary>

| Field | Meaning | Default |
|---|---|---|
| Build name | Build to search by. | required |
| Build number | Build number, or a reserved keyword for the latest build number. | required |
| Build status | Restrict to the latest build with this status, e.g. "Released" *(example)*. | none |
| Repositories | Only search these repositories. | all repositories |
| Archive type | `zip`, `tar`, `tar.gz` or `tgz`. | required |
| Mappings | List of entries, each with an input regular expression matched against artifact paths and an optional output path for the file inside the archive that may use the expression's capture groups (`$1`, `$2`, …). Only matching artifacts are included. | none (all artifacts) |

</details>

| | |
|---|---|
| Output | `200 OK` with the archive. |
| Privilege | read (anonymous possible where granted). |
| Errors | 400 malformed body; 401; 403. |

### ART-017 · Server-side download without transferring content
Priority: **P2**

Makes the server perform a download, for example so that a remote repository fetches and caches a
large artifact ahead of use, without the triggering client receiving the bytes.

| | |
|---|---|
| Inputs | Repository key; path; content mode: full content (when omitted), none, or progress; progress mark interval in bytes. |
| Output | `200 OK`. Full mode returns the content; none returns nothing; progress returns a text stream with one mark per interval and a final line stating bytes completed out of the total. |
| Privilege | read (anonymous possible where granted). |
| Errors | 400 malformed path, or a repository key or path containing non-printable characters; 401; 403; 404 file not found. |

| Setting | Default |
|---|---|
| Progress mark interval | 1024 bytes |

### ART-018 · Time-limited signed download URLs
Priority: **P2**

Creates a URL that lets its holder download one artifact without credentials until it expires.

| | |
|---|---|
| Inputs | JSON body: repository path of the artifact (required); either a validity in seconds from creation or an absolute expiry (Unix epoch, milliseconds). |
| Output | `200 OK` with the signed URL as a JSON string: the artifact's download URL plus a signature query parameter. A sample signature value has the shape of a compact JWS with an HS256 header *(example)*; the signing scheme is otherwise unspecified. |
| Privilege | administrator, or a user with manage permission. |
| Errors | 400 malformed body; 401; 403 neither administrator nor manage permission. |

| Setting | Default | Limit |
|---|---|---|
| Validity | 24 hours | at most 365 days |

Edge cases: the path must not contain an encoded space (`%20`). Unspecified: precedence when both
validity forms are given; whether the URL is bound to the creator's permissions at download time;
whether a URL can be used more than once; individual revocation (the only invalidation mechanism is
key rotation, ART-019).

Interactions: the redirect of signed downloads to object storage or a CDN is on
[storage-and-blobs.md](storage-and-blobs.md).

### ART-019 · Rotate the signed-URL key
Priority: **P2**

Replaces the key used to sign and validate signed URLs (ART-018). Every signed URL created before
becomes invalid at once.

| | |
|---|---|
| Inputs | none |
| Output | `201 Created`. |
| Privilege | administrator. |
| Errors | 401; 403 not an administrator. |

### ART-020 · Filtered (templated) resources
Priority: **P2**

Any text artifact can be marked as *filtered*. A filtered artifact is rendered as a
[FreeMarker](https://freemarker.apache.org/) template on every download, so one stored file yields
content tailored to the request, for example by caller address, by the caller's groups, or by
property values on the artifact.

Template context:

| Variable | Content |
|---|---|
| Properties | The properties of the requested artifact merged with the matrix parameters of the request. On a key clash the artifact's own property wins. |
| Request | The current download request (for example the client address). |
| Security | The security context of the caller (for example the names of the caller's groups). |

UI location: item details (ART-041), General tab, in the remote-download section, a "filtered"
checkbox.

Required permission: annotate on the artifact.

Typical use: provisioning build-tool settings files (Maven, Gradle, Ivy). An administrator
generates a settings file through the client setup instructions, edits it to contain template
placeholders, deploys it as an artifact and marks it filtered; each user downloading it receives a
personalised file.

Example *(example)*: a file `scm.conf.xml` carries a property `scm.root` with the value
`https://scm.example.com`. The template loops over every value of `scm.root` and, inside that, over
every group of the caller, and emits one line per combination: the value followed by `/team-a`,
`/team-b` or `/shared` depending on whether that group is `dev-team-a`, `dev-team-b` or another
one. A caller whose only group is `dev-team-b` receives `https://scm.example.com/team-b`.

Unspecified: behavior on template errors, the checksum and size reported for rendered content,
caching of rendered output, and whether filtering applies to remote-repository caches.

## Properties

### ART-021 · Item property model
Priority: **P1**

Properties are searchable string key/value annotations on items.

| Aspect | Rule |
|---|---|
| Kinds | *Explicit* properties are set by users and tools. *Implicit* properties are maintained by the system (for example last update and last upload times) and can drive cleanup. |
| Values | A key maps to a list of string values (multi-valued). |
| Key length | at most 255 characters |
| Value length | at most 2400 characters |
| Targets | Files and folders; the set, update and delete operations (ART-023 – ART-025) also accept repository roots, and the set operation also accepts release sets. Builds are named as property carriers too. |
| Writable in | Local repositories and the caches of remote repositories. |
| Through virtual repositories | Readable, not writable. |
| Enforcement | None by design. Property sets (ART-026) only steer what the UI offers for tagging and searching; the server accepts any value, which keeps repository operations cheap and flexible. |
| Representation | An item's properties also form a small XML metadata document on that item (one root element, flat entries), which the API can read, write and remove like other XML metadata; the UI shows properties and metadata on separate tabs. |
| Required permission to add, edit or delete | annotate |

Properties feed search and the query language ([search-and-query.md](search-and-query.md)),
retention policies ([retention-and-cleanup.md](retention-and-cleanup.md)), CI metadata
([builds-and-ci.md](builds-and-ci.md)), event handlers and plugins
([events-and-extensibility.md](events-and-extensibility.md)) and are shown in the Properties tab
of the item details (ART-041). Ways to add them: UI, API, companion CLI, and deploy-time matrix
parameters (ART-003).

Federation: properties of items are replicated between federated repository members, property set
definitions are not (see [replication-and-federation.md](replication-and-federation.md)).

### ART-022 · Read item properties
Priority: **P1**

One of the read modes of the item information operation (ART-027).

| | |
|---|---|
| Inputs | Repository key, item path, optionally a comma-separated list of property keys to restrict the output. |
| Output | The item URL and a map from property key to the list of its values. |
| Privilege | Read on the item (anonymous access possible where anonymous has read). |
| Errors | 400 when combined with another read mode; 404 when the item does not exist. |

Edge cases: the response when the item has no properties, or none of the requested keys, is
unspecified.

### ART-023 · Set item properties
Priority: **P1**

Attaches properties to an existing item.

| | |
|---|---|
| Inputs | Repository key; item path; a property string (required); a recursive flag. |
| Output | 204 No Content. |
| Privilege | annotate (anonymous possible where granted). |
| Errors | 400 property string missing or malformed; 401; 403 no annotate permission; 404 item or repository missing. |
| Applies to | Local repositories and remote-repository caches only; files, folders, repository roots, release sets. |

Property string syntax:

- `key=value` pairs separated by `|` or `;`; multiple values of one key separated by `,`:
  `env=qa,staging|owner=team-a` *(example)*.
- To use `,`, `\`, `|` or `=` literally inside a key or value, precede it with a URL-encoded
  backslash `%5C`; `a=1%5C=1` sets key `a` to the value `1=1`.

Settings:

| Setting | Meaning | Default |
|---|---|---|
| Recursive flag | Apply to the folder and every item below it. | on (for folders, recursive is the default) |
| Maximum items per recursive property change | System-wide upper bound on how many items one recursive property change may touch; a change on a folder with more items fails. Protects the database. | off; no default value once enabled |

UI location: item details (ART-041), Properties tab: choose "property" or "property set", enter a
name or pick a set, enter a value or pick predefined values, optionally tick "add recursively",
confirm. The new property appears in the tab's table.

Edge cases: whether setting a key that already exists replaces its values or adds to them is
unspecified.

### ART-024 · Update item properties (partial update)
Priority: **P1**

Adds, changes and removes properties of an item in one request with a JSON body.

| | |
|---|---|
| Inputs | Repository key; item path; JSON object mapping each property key to a string (single value), an array of strings (multiple values) or `null` (remove the key); optional recursive flag (0/1); optional atomic flag (0/1). |
| Output | 204 No Content. |
| Privilege | annotate. |
| Errors | 400 body malformed or the property map missing; 401; 403 no annotate permission; 404 item or repository missing. |
| Applies to | Files, folders, repository roots in local repositories and remote-repository caches. |

The body is a JSON object with one member that holds the property map, for example the map
`{"stage": "qa", "targets": ["linux", "darwin"], "obsolete": null}` sets `stage` to one value,
`targets` to two values and removes `obsolete` *(example)*.

Unspecified: the exact semantics of the atomic flag, the default of the recursive flag, and whether
keys absent from the request are kept (the operation is an attach-and-modify, which
implies they are).

### ART-025 · Delete item properties
Priority: **P1**

| | |
|---|---|
| Inputs | Repository key; item path; comma-separated list of property keys (required); recursive flag. |
| Output | 204 No Content. |
| Privilege | annotate (anonymous possible where granted). |
| Errors | 400 key list missing or malformed; 401; 403; 404 item or repository missing. |
| Applies to | Local repositories and remote-repository caches; files, folders, repository roots. |
| Recursive flag default | on for folders |

UI location: item details, Properties tab, row action "delete" (this item only) or "delete
recursively" (this item and everything below it), each with a confirmation prompt.

### ART-026 · Property sets
Priority: **P1**

Administrator-defined, reusable collections of properties with optional predefined values, made
available per repository.

<details>
<summary>Property set definition</summary>

| Field | Meaning | Default |
|---|---|---|
| Set name | Unique name of the set. | none (required) |
| Property name | Unique name of a property within the set, e.g. `build.status` *(example)*. | none (required) |
| Value type | *Any value* (free entry), *single select* (exactly one of the predefined values), *multi select* (any number of the predefined values). | unspecified |
| Default value | Optional value proposed when the property is added. | none |
| Predefined values | List of allowed values for single and multi select. | empty |

</details>

Operations:

| Operation | Behavior | Privilege |
|---|---|---|
| Create set | Name plus any number of property definitions. | administrator |
| Edit set | Change, add or remove property definitions. | administrator |
| Delete set | Refused while the set is assigned to any repository; unassign first. | administrator |
| Assign to repository | Repository settings, advanced tab, property-set selector (dual list; drag and drop, per-item arrows, move-all arrows). Only assigned sets are offered when adding properties to items in that repository. | administrator or project administrator |
| Read definitions via API | Sets are a section of the global system configuration, which can be fetched as a whole. A dedicated create/update/delete operation for sets is unspecified. | administrator |

UI location: administration, repository-manager settings, "Property sets".

Edge cases: values are not enforced server-side (ART-021). In a federation, set definitions are
local to each member while item property values replicate; a member lacking a definition still
shows the replicated values but cannot manage them through the set, and predefined lists that
differ between members cause inconsistent filtering and display.

## Item information

### ART-027 · Item information
Priority: **MVP**

Returns metadata of a repository root, folder or file. The same operation has mutually exclusive
read modes; exactly one may be requested per call, otherwise 400.

| Mode | Returns | Entry |
|---|---|---|
| none (default) | Folder or file information (below). | this entry |
| properties | Property map. | ART-022 |
| permissions | Effective permissions. | ART-031 |
| statistics | Download statistics. | ART-029 |
| last modified | Most recently modified item. | ART-030 |
| list | File list. | ART-028 |

<details>
<summary>Returned information</summary>

| Information | Folder | File |
|---|---|---|
| URL of this information resource | yes | yes |
| Repository key, path | yes | yes |
| Created (timestamp, ISO 8601), created by (user) | yes | yes |
| Last modified (timestamp), modified by (user) | yes | yes |
| Last updated (timestamp) | yes | yes |
| Children: relative URI and folder flag per direct child | yes | — |
| Download URL | — | yes |
| Upstream URL the file was fetched from (remote caches) | — | yes |
| Size in bytes (serialized as a string) | — | yes |
| MIME type | — | yes |
| Checksums: MD5, SHA-1, SHA-256 as computed by the server | — | yes |
| Original checksums: as supplied by the deploying client | — | yes |

</details>

| | |
|---|---|
| Privilege | read on the item; anonymous possible where anonymous has read. |
| Errors | 400 invalid mode combination or malformed path; 401; 403; 404 item missing. |

Edge cases: for large uploads the creation time can be *later* than the last-modified time, because
last-modified is stamped when the upload starts and creation only once the finished upload is
written to the database.

UI location: item details, General tab (ART-041).

### ART-028 · File list
Priority: **P1**

A listing mode of ART-027.

<details>
<summary>Inputs and output</summary>

| Input | Meaning | Default |
|---|---|---|
| Deep | 0 = only direct children, 1 = whole subtree. | flat |
| Depth | Optional limit on the depth of a deep listing. | unspecified |
| Include folders | 0/1, list folders as entries. | unspecified |
| Include metadata timestamps | 0/1, add per-entry timestamps of metadata. | unspecified |
| Include root path | 0/1, include the listed folder itself. | unspecified |

| Output | Meaning |
|---|---|
| URL, created | Of the listed folder. |
| Per entry | Relative URI, size in bytes as a string (`-1` for folders), last modified, folder flag, SHA-1 (files), map of metadata name to timestamp (when requested). |

</details>

| | |
|---|---|
| Privilege | a non-anonymous user with access to the path; anonymous callers are refused. |
| Errors | as ART-027. |

Edge cases: ordering of entries and paging are unspecified.

### ART-029 · Download statistics
Priority: **P1**

Per-file statistics, recorded on every download.

| | |
|---|---|
| Inputs | Repository key, file path; statistics mode of ART-027. |
| Output | Download count, time of the last download (milliseconds since the epoch), user of the last download. |
| Privilege | read. |

The package view shows a per-version download count (ART-049). For Docker and OCI the count is per
tag. An optional shared-digest mode links the statistics of tags that reference the same manifest
digest: pulling any such tag, or the digest itself, updates the count of every tag with that digest
in the same repository and image that the caller can read (see PKG-DOCKER-030 in
[formats/docker.md](formats/docker.md)).

Edge cases: whether downloads through a virtual repository count on the underlying file, and
whether checksum-file or metadata requests count, is unspecified.

### ART-030 · Last-modified lookup
Priority: **P2**

| | |
|---|---|
| Inputs | Repository key, path; last-modified mode of ART-027. |
| Output | URL of the most recently modified item and its last-modified timestamp. For a folder the subtree is searched recursively. |
| Privilege | authenticated user with deploy permission on the path. |

### ART-031 · Effective permissions of an item
Priority: **P1**

| | |
|---|---|
| Inputs | Repository key, path; permissions mode of ART-027. |
| Output | Two maps: user name → list of granted actions, group name → list of granted actions. |
| Privilege | same as the default information mode: access to the item, anonymous possible where granted. |

UI location: item details, "Effective permissions" tab, listing users, groups and the permission
policies that grant access at that path. Permission model: see
[identity-and-access.md](identity-and-access.md).

## Copy, move and delete

### ART-032 · Copy items
Priority: **P1**

Copies a file or a folder (with its subtree) to another path, in the same or another repository.

<details>
<summary>Inputs and output</summary>

| Input | Meaning | Default |
|---|---|---|
| Source repository key and path | File or folder to copy. | required |
| Target | `/<target repository key>/<target path>`. | required |
| Dry run | 1 = only simulate and report. | 0 |
| Suppress layout translation | 1 = do not translate module paths between repositories with different layouts. | 1 |
| Fail fast | 1 = stop at the first error. | unspecified |

| Output | Meaning |
|---|---|
| `200 OK` | A list of messages, each with a level (info, warning, error) and a text, e.g. a summary of how many artifacts and folders were copied. |
| 400 | Invalid parameters or source path does not exist. |
| 401 / 403 | Authentication failed / missing permission. |

</details>

Rules:

- Source repositories: local, remote-repository caches and federated repositories.
- Target: a local repository, or a virtual repository with a default deployment repository, of the
  same package format as the item or of the generic format; for example a Debian file can go to a
  Debian or a generic local repository.
- Copy follows Unix `cp` semantics: copying folder `a/b/1` to a target path `a/b/1` that already
  exists produces `a/b/1/1`; to land at the same path, copy into the parent (`a/b`).
- Layout translation: between repositories with different layouts the system translates module
  paths unless suppressed. In the UI, entering a custom target path suppresses translation, which
  can leave the copies unresolvable by clients, even between repositories of the same layout.
- Format metadata (indexes, descriptors) of the affected repositories is updated when the copy
  completes, so clients see a consistent state.
- Dry run reports the errors and warnings the real operation would produce, such as items the
  target refuses by its own policies or missing permissions.
- Privilege: a user with the necessary permissions, anonymous possible where granted; which
  permissions copy checks on source and target is unspecified (move: ART-033).

UI location: file tree, context menu, "Copy content": target repository list (only eligible
repositories), optional custom target path, "Dry run" button, "Copy".

Unspecified: whether properties and statistics are copied with the item; behavior when the target
file already exists.

### ART-033 · Move items
Priority: **P1**

Same inputs, outputs, defaults, target rules, layout handling, dry run and fail-fast behavior as
ART-032, with these differences:

| Aspect | Move |
|---|---|
| Source repositories | Local repositories only. |
| Privilege | delete on the source and deploy on the target. |
| Result | The source items no longer exist; metadata of both repositories is updated. |
| UI | File tree, context menu, "Move content". |

### ART-034 · Delete items
Priority: **MVP**

Deletes a file or a folder (with everything below it) by an HTTP `DELETE` of its URL
(`<repository URL>/<path>`).

| | |
|---|---|
| Applies to | Local repositories and remote-repository caches. |
| Output | `204 No Content`. |
| Privilege | delete (anonymous possible where granted). |
| Errors | 401; 403 no delete permission; 404 item does not exist; 405 the repository behind the path refuses deletes, for example a remote repository addressed directly (as opposed to its cache) or a virtual repository. |

Behavior:

- The affected format metadata is updated, so the item disappears from client indexes and search
  results.
- With the trash can enabled, files deleted from local repositories move to the trash can instead
  of being removed (ART-036).

UI location: item details action menu "Delete", or the file tree context menu; the label varies
with the item type ("Delete" for a file, "Delete content" for a folder).

### ART-035 · Delete versions (bulk UI)
Priority: **P2**

Deletes all artifacts that share a group (path prefix) and version in one step, for example every
module of a multi-module snapshot deployment, and keeps the format metadata consistent.

UI location: file tree, context menu of a folder, "Delete versions". A dialog lists the groups and
versions found below the folder; the user selects versions and confirms.

Limits: the dialog derives versions from the first 1000 artifacts found at the selected level;
versions beyond that are reached by filtering by group ID or version in the dialog.

Edge cases: how versions are recognised for formats without a layout is unspecified.

## Trash can

### ART-036 · Trash can with retention period
Priority: **P1**

When enabled, files and paths deleted from local repositories are kept in a trash can for a
retention period instead of being removed, and can be restored (ART-037) until then. The trash can
appears as a node at the bottom of the file tree (ART-040).

<details>
<summary>Settings (general settings)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enable trash can | Deleted items from local repositories go to the trash can. | unspecified |
| Retention period (days) | How long deleted items stay before permanent removal. | unspecified |
| Empty trash can (button) | Permanently deletes the current content (ART-038). | — |

</details>

Who configures: an administrator, in the general settings. Users with manage permission are also
named as able to enable the trash can and set its retention; how such a user reaches the
system-wide settings is unspecified.

Visibility and access (the trash can is filtered per user):

| User has on the repository of origin | May |
|---|---|
| deploy or manage | See the trash can and that repository's deleted files. |
| additionally delete | Restore that repository's deleted files without an administrator. |
| nothing | Neither see nor restore anything from that repository. |

UI functions (context menu of the trash-can node):

| Function | Behavior |
|---|---|
| Search trash can | Opens the global search pre-filtered to the trash can; the user enters a term. |
| Refresh | Reloads the tree and the list of deleted items. |
| Empty trash can | ART-038, with confirmation. |
| Restore | ART-037. |

Edge cases: deleted release-set versions cannot be found or restored here; they have their own
restore operation (see [distribution-and-lifecycle.md](distribution-and-lifecycle.md)). Items
whose retention has expired are removed by garbage collection (see
[storage-and-blobs.md](storage-and-blobs.md)).

### ART-037 · Restore from the trash can
Priority: **P1**

| | |
|---|---|
| Inputs | Source in the trash can: repository key, optionally with a path; destination (required): repository key, optionally with a path; optional transaction size (number of items processed per database transaction, at least 1, e.g. 1000 *(example)*). |
| Output | `202 Accepted` with a plain-text success message. |
| Privilege | administrator, project administrator or manage permission. In addition, users with deploy (or manage) and delete on the repository of origin may restore that repository's files without an administrator (see the visibility table in ART-036). |
| Errors | permission denied answers 400 (not 403); 401; 404 item not in the trash can. |

UI location: trash-can node, context menu of a deleted item or the action menu in its details,
"Restore". The dialog asks for the target repository and optionally a custom target path.

Unspecified: behavior when the destination path already exists; default transaction size.

### ART-038 · Permanently delete from the trash can
Priority: **P1**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Empty the trash can | none | `202 Accepted` | administrator |
| Delete one item or path | Repository key, optionally with a path (e.g. `team-npm` or `team-npm/lodash/-` *(example)*) | `204 No Content` | administrator, project administrator, or manage or delete permission |

Both answer permission denial with status 400 (not 403), and failed authentication with 401.

UI location: trash-can node context menu "Empty trash can" (with confirmation) and the "Empty
trash can" button in the general settings.

### ART-039 · Purge redundant trash-can folder records
Priority: **P2**

A maintenance job: redundant metadata records attached to folders in the trash can slow down the
short, frequent garbage-collection cycle and over time degrade database performance. The operation
submits a background job that deletes those records in batches.

| Input (JSON, optional) | Meaning | Default |
|---|---|---|
| Batch size | Records fetched per database query; integer > 0. | 1000 |
| Pause between batches | Milliseconds between successive queries; integer > 0. | 1000 |
| Batch count | Maximum number of queries, so at most batch size × batch count records are deleted. | -1 (the meaning of -1 is unspecified) |

| | |
|---|---|
| Output | `202 Accepted` with an informational message that the task was submitted. |
| Privilege | administrator. |
| Errors | 400 malformed body or invalid parameters; 401; 403. |

## Browsing and views

### ART-040 · File tree browser
Priority: **MVP**

UI location: the artifacts page. A tree of the full repository hierarchy, expanding into
folders and files; selecting a node shows its details (ART-041) and context actions (deploy,
download, copy, move, delete, directory-listing browser, favorites, trash can).

| Function | Behavior | Default |
|---|---|---|
| Name filter | Narrow the tree to repositories matching the entered name. | none |
| Repository-class filter | One or more of local, remote, virtual (and others the system offers). | none |
| Package-format filter | One or more formats. | none |
| Favorites filter | Show only repositories the user marked as favorite. | off |
| Compact view | Collapse chains of single-child folders into one path node. | unspecified |
| Sort | By repository class, repository key or package format. | by class in the order virtual, local, remote, remote cache |
| Sort order by class | Administrators can configure the class order system-wide; classes left out keep their default position. | virtual, local, remote, remote cache |
| Add to favorites | Hover action, context menu, or button in the item details. | — |
| Clear | Resets all filters. | — |
| Items per page | System-wide setting (general settings): maximum number of children shown per level in one batch, in the tree and the directory-listing browser (ART-042); a "load more" entry fetches the next batch. | unspecified |

Filters are stored per browser (as cookies in one variant of the UI).

Interactions: content-browsing setting (ART-045) lets file URLs open HTML, Javadoc, JSON and
similar files in place.

### ART-041 · Item details pane
Priority: **MVP**

Shown next to the file tree for the selected repository, folder or file. Tabs are context
dependent.

<details>
<summary>Tabs</summary>

| Tab | Content | Entry |
|---|---|---|
| General | Item information (ART-027) incl. checksums, dependency declaration snippets for build tools, the virtual repositories that include the item's repository, the "filtered" switch (ART-020), and clickable file URL when content browsing is enabled. | ART-027 |
| Effective permissions | Users, groups and permission policies with access at this path. | ART-031 |
| Properties | Property table with add, add-recursively, delete and delete-recursively. | ART-021 – ART-026 |
| Followers | Users watching the item. | ART-046 |
| Builds | Builds that produced or consumed the item. | [builds-and-ci.md](builds-and-ci.md) |
| Scan results | Latest vulnerability scan status, findings by severity, software bill of materials, and the option to ignore a violation that blocks download. | [security-scanning-and-curation.md](security-scanning-and-curation.md) |
| Evidence | Evidence files attached to the item. | [distribution-and-lifecycle.md](distribution-and-lifecycle.md) |

</details>

### ART-042 · HTML directory-listing browser
Priority: **P1**

A fast, read-only HTML listing of a repository, folder by folder, in the style of an HTTP server's
directory index, usable outside the UI. Reachable from the file tree (context menu or item-details
action "directory listing").

- Lists folders and files with links; checksum files (`.md5`, `.sha1`, `.sha256`) appear next to
  artifacts under the conditions of ART-008.
- With content browsing enabled on the repository (ART-045), clicking an HTML, Javadoc, JSON or
  similar file shows it in the browser.

Interactions: public repository view (ART-043).

### ART-043 · Public repository view under a path prefix
Priority: **P2**

The directory-listing browser of a repository can be mounted under a well-known path prefix
(`<base URL>/<prefix>/<repository key>`). An administrator can then expose only that prefix
through a separate virtual host to the public, keeping write access and the resource-intensive UI
and API for internal use.

Unspecified: how prefixes are configured and whether the view is strictly read-only.

### ART-044 · Browsing uncached remote content
Priority: **P2**

For a remote repository whose upstream is another instance of the same system, the tree can list
upstream folders and files that are not cached locally. Enabled by the remote repository's
"list remote folder items" setting (see [remote-repositories.md](remote-repositories.md)). Listings
are cached for the remote repository's retrieval cache period; the first listing can be slow,
especially through a virtual repository with several remotes.

### ART-045 · Content browsing of files and archives
Priority: **P2**

A per-repository setting (advanced settings, "allow content browsing"). When on, users can open
the internal entries of archives (for example single pages from a Javadoc archive) and view HTML,
Javadoc, JSON and similar files directly from their URL in the tree and the directory-listing browser.

Security: with this setting on, an uploader can place content that attacks other users, for
example cross-site scripting through uploaded HTML; repositories with content browsing need strict
moderation of what gets uploaded.

Interactions: archive entry download (ART-014) uses the same archive-entry addressing.

### ART-046 · Followers (change notifications)
Priority: **P2**

Users can follow an artifact, folder or repository and receive email about storage events on it:
create, delete, modify. Following a folder covers all of its descendants.

- Notifications are batched roughly once per minute into a single email.
- A follower is only told about items they are allowed to read.
- Administrators and users with manage permission see and manage the followers of an item in the
  Followers tab (ART-041).

Unspecified: how a user starts and stops following, and the email content.

### ART-047 · WebDAV access
Priority: **P2**

Local repositories and remote caches can be mounted as WebDAV shares at the repository's URL and
used from WebDAV-capable file managers.

- The WebDAV `LOCK` method is not supported: a deploy from a client with file locking enabled makes
  the server log an unsupported-request-method message. Disabling locking in the client before
  mounting usually helps (for davfs2 a configuration switch); some clients have locking off by
  default, others cannot turn it off.
- Clients that do not authenticate preemptively (davfs2) first get a 401 challenge; when anonymous
  access is enabled there is no challenge and the client silently works with anonymous rights.
  Configuring the client to always send an `Authorization: Basic` header avoids both.
- Windows' built-in WebDAV client: browsing and listing work; copy and move are not supported.

### ART-048 · Package list
Priority: **MVP**

UI location: "Packages" page. Lists the packages stored in all repositories, sorted
lexicographically on first display, with summary information on each package's latest version.

<details>
<summary>Filters and views</summary>

| Function | Behavior | Default / limit |
|---|---|---|
| Package format filter | One or more formats. | none |
| Package name filter | Substring match (wildcards applied before and after the input). | none |
| Description filter | Text in the version's metadata description. | none |
| Keyword filter | One keyword from the package metadata. | at most one keyword |
| Created filter | Last 7, 30 or 90 days, or a custom range; uses the creation date of the package's *first* version. | none |
| Applied indicator | Each active filter option is marked. | — |
| Custom views | Save the current filters as a named view shown as a tab; rename, delete, make default, update. Created only from the "all packages" tab. Stored in the browser, not on the server. | at most 8 |
| Recently viewed | Tab with the most recently opened packages. | 10 |

</details>

Interactions: details on ART-049; search across packages on
[search-and-query.md](search-and-query.md).

### ART-049 · Package details and install commands
Priority: **MVP**

Opening a package shows its latest version or tag.

<details>
<summary>Fields and tabs</summary>

| Field | Meaning |
|---|---|
| Version / tag | Version or tag currently shown. |
| Published | Publish date of the version. |
| Version downloads | Download count of the version or tag (ART-029). |
| Vulnerabilities | Summary of the security scan, if the version was scanned; hovering shows a legend. |
| Licenses | Licenses declared for the package. |

| Tab | Content |
|---|---|
| Info | General version information; for Docker, Helm-on-OCI and OCI the manifest or manifest list. |
| Readme | The version's readme, if any. |
| Repositories | All repositories containing the version, each with "show in file tree". |
| Builds | "Produced by" and "used by" build lists with name, number, creation date and a link. |
| Release sets | Release sets containing the version, their distribution status and last update. |
| Evidence | Evidence files of the version. |
| Scan data | Security data of the version. |

</details>

Version selection: a quick selector (pick or type a version) and an "all versions" list with main
details; both offer a "release versions only" filter when the package has release versions.

Install commands: for each supported client of the format (for Docker also Podman) the page shows
a copyable install command for the selected version; if the version exists in several
repositories, the user picks the repository. Multi-line commands show their first line with an
expander. Formats whose clients need explicit credentials first ask the user to generate a token
(after re-entering the password) and embed it in the command (see
[access-tokens.md](access-tokens.md)).

Download: for some formats the version can be downloaded from the UI (context menu of the version
in the file tree, or the download icon in the details).

## Other operations of the artifact API

### ART-050 · Invalidate cached metadata in a remote repository
Priority: **P1**

Marks all cached *metadata* files below a path of a remote-repository cache as stale (for example
index and version-list files fetched from npmjs.org, PyPI or Maven Central). Immutable content such
as package binaries is not touched.

| | |
|---|---|
| Inputs | Repository path: remote-repository cache key, optionally with a folder path. |
| Behavior | The next request for an invalidated metadata file refreshes it from the upstream first; if the upstream is unreachable, the stale copy is served. |
| Output | `200 OK` with a JSON message confirming the path that was invalidated. |
| Privilege | manage or delete on the remote repository. |
| Errors | 401; 403. |

Use: repairing corrupt or inconsistent cached metadata. Clients needing refreshed metadata may see
slower responses right after invalidation. See [remote-repositories.md](remote-repositories.md)
for metadata caching periods.

### ART-051 · List background tasks
Priority: **P2**

Returns the background tasks currently scheduled or running (for example index calculation for
virtual repositories, binary garbage collection).

| | |
|---|---|
| Output | List of tasks, each with an ID, a type, a state (scheduled, running, stopped, cancelled), a description, the node it runs on (in a high-availability cluster) and, when running, its start time. |
| Privilege | administrator. |
| Errors | 401; 403. |

See [operations-and-monitoring.md](operations-and-monitoring.md).

### ART-052 · RubyGems version list
Priority: **P2**

Returns, for a RubyGems repository and a gem name, the list of versions with their metadata in the
shape of the RubyGems.org versions API: authors, build date, creation date, description, download
count, free-form metadata, version number, summary, platform, required Ruby version, pre-release
flag, licenses, requirements and SHA-256. JSON or YAML. Requires an authenticated user; 404 for an
unknown gem. Format details: [formats/rubygems.md](formats/rubygems.md).

### ART-053 · Push to a discontinued external distribution service
Priority: **Out of scope**

A deprecated operation that pushed artifacts to an external, hosted package distribution
service (since shut down) and recorded them in a distribution repository, with options for
publishing, overwriting, signing, asynchronous execution and dry run. Superseded by release-set
distribution ([distribution-and-lifecycle.md](distribution-and-lifecycle.md)).

## Notes for implementers

- **Sizes are strings.** Item information, deploy responses and file lists serialise the size as
  a decimal string, and file lists use `-1` for folders (ART-002, ART-027, ART-028). Clients parse
  these values; changing them to JSON numbers breaks them.
- **One read mode per call.** Item information modes (properties, permissions, statistics, last
  modified, list) are mutually exclusive; a combination is a 400, not a merged answer (ART-027).
- **Creation can postdate modification.** Last-modified marks the start of an upload, creation its
  commit; for large uploads a creation time later than the last-modified time is normal and must
  not be "fixed" (ART-027).
- **Folder property changes cascade by default.** Setting or deleting properties on a folder is
  recursive unless the client turns it off; a single call can touch a whole subtree, hence the
  optional item cap (ART-023, ART-025).
- **Property strings have two escaping layers.** `|` or `;` separate properties, `,` separates
  values, and literal `, \ | =` need a URL-encoded backslash (`%5C`). Matrix parameters use `;`
  inside the path at the same time, so the parser must keep path and query handling apart
  (ART-003, ART-023).
- **Deploy by checksum must check read access.** Reuse existing content only when the caller can
  read some item holding it; otherwise answer 404 so the client uploads. Skipping the check turns a
  known hash into a read grant (ART-004).
- **Checksum files are conditional.** Build tools such as Maven fetch `.sha1`/`.md5` next to every
  artifact; whether they exist depends on client-supplied checksums or the repository's checksum
  policy, and a mismatch is a 409 on the checksum file (ART-008, ART-009).
- **Copy nests into existing folders.** Copying a folder onto an existing folder of the same name
  creates a nested folder (Unix `cp` semantics); scripts that mirror trees copy into the parent
  (ART-032). The API suppresses layout translation by default, the UI only when a custom path is
  given.
- **Status codes in the trash-can operations are irregular.** Permission denial answers 400 rather
  than 403, empty and restore answer 202, single delete 204 (ART-037, ART-038). A compatible
  implementation reproduces them or documents the deviation.
- **Templated downloads widen the annotate permission.** Marking a file filtered needs only
  annotate, yet the template runs on every download with the caller's security context and
  request; treat the "filtered" switch as a privileged action and sandbox the template engine
  (ART-020).
