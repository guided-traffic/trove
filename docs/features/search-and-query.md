# Search and query language

This page catalogs how a full-featured repository manager lets people and tools find stored
content: the single-purpose search operations of the management API, the query language for
complex cross-domain queries (including its bulk delete and property-update actions and the
operational controls around it), and the search features of the web UI. Search endpoints that a
package manager itself speaks (for example `npm search` or the NuGet search query service) are
protocol features and live on the format pages under [formats/](formats/README.md). Browsing the
repository tree, item details, properties and the trash can live in [artifacts.md](artifacts.md);
the full screen inventory lives in [ui-inventory.md](ui-inventory.md).

## Search model and shared conventions

### SRCH-001 · Two search sources: stored items and extracted package metadata
Priority: **P1**

The system keeps two separately searchable bodies of data:

- **Item store.** Every repository, folder and file with its path, checksums, timestamps,
  properties and download statistics. Artifact searches, the search operations of this page and
  the query language read from here.
- **Package-metadata store.** Fields extracted from a package's manifest or metadata file at the
  moment the package is uploaded (name, version, description, keywords, format-specific fields).
  The package search of the global search box (SRCH-051) reads from here.

A search that targets one object type only consults the store that belongs to it. The extracted
package record is written at upload time and is never refreshed when files of the package are
later changed by hand.

Edge case: when a file is renamed by hand and uploaded again, an artifact search finds it under
its new name, while a package search keeps finding only the name that was extracted from the
original upload. The UI shows the file under the new name in the tree and under the old name in
the package details, which confuses users; documentation should tell them to fall back to the
artifact search.

Interactions: package search (SRCH-051), artifact browser search (SRCH-058), format-specific
package fields (SRCH-050). Metadata extraction per format is described on the format pages under
[formats/](formats/README.md).

### SRCH-002 · Common conventions of the search operations
Priority: **P1**

The single-purpose search operations (SRCH-004 to SRCH-022) share these conventions:

- **Repository restriction.** An optional, comma-separated list of repository keys narrows the
  search. Which repository classes an operation accepts differs per operation (table below);
  several operations refuse virtual repositories and must be pointed at the repository that
  physically holds the item.
- **Result form.** Most operations return a JSON object holding a list of results; each result
  carries a link to the item's metadata resource (the item-info operation, see
  [artifacts.md](artifacts.md)). A few return direct download links instead (build artifacts
  search, the reduced mode of the coordinate search, latest version by properties).
- **Extended result detail.** An optional request header asks the server to enrich each result
  with the item's full metadata, with its properties, or with both (three accepted values). The
  name, coordinate, property and checksum searches accept it.
- **Media types.** Responses are JSON, except the layout-based latest-version search
  (SRCH-017), which answers with plain text. Most of the operations also advertise an
  operation-specific JSON media type with the same body as plain JSON; the container listings
  (SRCH-020 to SRCH-022) and the property-based latest-version search (SRCH-018) only use plain
  JSON.
- **Authentication.** HTTP Basic (user name with password or with an API key), an API key
  header, or a bearer access token; see [access-tokens.md](access-tokens.md). Some operations
  allow anonymous callers, some do not (column "Anonymous" below).
- **Errors.** A JSON envelope holding a list of error objects, each with the HTTP status and a
  human-readable message. Status codes in use: 400 (missing or invalid parameters), 401 (bad
  credentials), 403 (insufficient rights, for example "not an administrator"), 404 (on the
  operations noted in their entries), 500.
- **Concurrency.** The global concurrency limit of SRCH-045 counts these operations as well as
  query-language calls.

<details>
<summary>Repository classes and anonymous access per search operation</summary>

| Operation | Repository restriction accepts | Anonymous |
|---|---|---|
| Name search (SRCH-004) | local, remote cache (virtual refused) | allowed |
| Archive entry search (SRCH-005) | local, remote cache | allowed |
| Maven coordinate search (SRCH-006) | local, remote cache; virtual and remote in reduced mode | allowed |
| Property search (SRCH-007) | local, remote cache, virtual | allowed |
| Checksum search (SRCH-008) | local, remote cache, virtual | allowed |
| Bad checksum search (SRCH-009) | local, remote cache, virtual | allowed |
| Not-downloaded-since search (SRCH-010) | local, remote cache | not allowed |
| Date-range search (SRCH-011) | local, remote cache | not allowed |
| Created-in-range search (SRCH-012) | local, remote cache | not allowed |
| Path pattern search (SRCH-013) | one repository, named inside the pattern | not allowed |
| Builds for dependency (SRCH-014) | no restriction parameter | allowed |
| License search (SRCH-015) | local, remote cache, virtual | administrators only |
| Version list (SRCH-016) | local, remote, virtual | allowed |
| Latest version by layout (SRCH-017) | local, remote cache, virtual | allowed |
| Latest version by properties (SRCH-018) | one repository or all, in the request path | not allowed |
| Build artifacts search (SRCH-019) | list in the request body | allowed |
| Container catalog / tags / parent lists (SRCH-020 to SRCH-022) | one container repository, in the path | read permission required |

"Allowed" means the operation is open to a privileged caller, anonymous
included; whether results are filtered item by item against the caller's read permission is not
stated for these operations (it is stated for the query language, SRCH-036).

</details>

Interactions: result cap (SRCH-003), item metadata and download operations in
[artifacts.md](artifacts.md), operation catalog in
[api-content-operations.md](api-content-operations.md).

### SRCH-003 · Result cap on search operations
Priority: **P1**

For the callers it covers, a simple search operation stops after a fixed number of results; the
UI searches that do the same job share this cap. It covers the name, archive
entry, Maven coordinate, property, checksum, not-downloaded-since, date-range and
created-in-range searches.

| Setting | Meaning | Default |
|---|---|---|
| Limit anonymous callers only | When on, only anonymous callers are capped; when off, every caller is capped | on |
| Maximum results per search | Number of results returned before truncation | 1000 |

Edge cases: the capped population is also described as "internal and anonymous users"; which
callers count as internal is unspecified. Whether a truncated response signals the
truncation is unspecified. The checksum search is described as capped by the UI's maximum result
count, which is the same setting.

Interactions: query-language hard cap (SRCH-042) is a separate mechanism; system settings in
[operations-and-monitoring.md](operations-and-monitoring.md).

## Search operations

### SRCH-004 · Name search
Priority: **P1**

Finds files by a fragment of their file name across the searchable repositories.

| Input | Meaning | Default |
|---|---|---|
| Name fragment | Part of the file name; required, at least one character. The UI form of this search accepts `*` (any string) and `?` (one character) | none (required) |
| Repository restriction | Keys of local repositories or remote caches | all searchable repositories |
| Extended result detail | Header, see SRCH-002 | none |

Output: list of item-info links. Privilege: privileged caller, anonymous allowed.

Limits and edge cases: virtual repositories are not supported; the search must name the local
repository or the remote repository (its cache) that actually holds the file. Results are capped
per SRCH-003.

UI location: global search box, object type "artifacts", advanced search, type "name"
(SRCH-052).

Interactions: SRCH-002, SRCH-003; the artifact browser search (SRCH-058) is the newer UI path for
the same need.

### SRCH-005 · Archive entry search
Priority: **Later**

Searches inside archives (for example JAR files) for entries such as class files or other
resources, and reports which archives contain each matching entry.

| Input | Meaning | Default |
|---|---|---|
| Entry name pattern | Name of the entry, with wildcards (for example `*Parser.class`) | unspecified |
| Repository restriction | Local repositories or remote caches | all searchable repositories |

Output: list of records, each with the fully qualified entry name inside the archive and the list
of item-info links of the archives that contain it. Privilege: privileged caller, anonymous
allowed. May answer 404.

UI form (global search, artifacts, type "archive entries"): path (free text), "class resources
only" (default off), "exclude inner classes" (default off), "treat the term as a checksum"
(default off).

Edge cases: the operation is deprecated and scheduled for removal; the corresponding query
language domain has already been removed and any query that uses it fails at parse time
(SRCH-024). Results are capped per SRCH-003. How and when archive contents are indexed is
unspecified.

Interactions: SRCH-024 (removed archive domain).

### SRCH-006 · Maven coordinate search
Priority: **P1**

Finds artifacts by [Maven](https://maven.apache.org/) coordinates: group ID, artifact ID, version
and classifier.

| Input | Meaning | Default |
|---|---|---|
| Group ID | Maven group ID | none |
| Artifact ID | Maven artifact ID | none |
| Version | Maven version | none |
| Classifier | Maven classifier | none |
| Repository restriction | Local repositories and remote caches (standard mode) | all searchable repositories |
| Reduced output mode | When on, also searches virtual and remote repositories and returns only download link, file extension, version and classifier per result | off |
| Extended result detail | Header, see SRCH-002 | none |

At least one of the four coordinates must be given. Output: list of item-info links (standard
mode) or of reduced records (reduced mode; in the reference example the classifier appears only
on classified files). Privilege: privileged caller, anonymous allowed.

Limits and edge cases: works only on repositories whose layout knows Maven-style coordinates
(Maven, Gradle, Ivy, SBT layouts). Searching remote repositories requires the exact group ID and
artifact ID (no partial values). Results are capped per SRCH-003.

Interactions: repository layouts in [repositories.md](repositories.md); Maven format page under
[formats/](formats/README.md); version searches SRCH-016 and SRCH-017.

### SRCH-007 · Property search
Priority: **P1**

Finds items by the properties (key/value annotations) attached to them.

| Input | Meaning | Default |
|---|---|---|
| Property filters | Arbitrary request parameters: the parameter name is the property key, the parameter value is a comma-separated list of property values | a key without a value matches any value (`*`) |
| Repository restriction | Local repositories, remote caches, virtual repositories | all searchable repositories |
| Extended result detail | Header, see SRCH-002 | none |

Output: list of item-info links. Privilege: privileged caller, anonymous allowed.

Edge cases: how several comma-separated values for one key combine (all
versus any) is unspecified for the API; the UI form combines every key/value row and every value
of one key with AND (SRCH-052). Results are capped per SRCH-003.

UI location: global search, artifacts, advanced search, type "property".

Interactions: properties in [artifacts.md](artifacts.md); query-language property criteria
(SRCH-027).

### SRCH-008 · Checksum search
Priority: **P1**

Finds every stored file with a given checksum, regardless of its name or location; the typical use
is to locate a file that was renamed or to check whether content is already stored.

| Input | Meaning | Default |
|---|---|---|
| MD5 | MD5 digest to look for | none |
| SHA-1 | SHA-1 digest to look for | none |
| SHA-256 | SHA-256 digest to look for | none |
| Repository restriction | Local repositories, remote caches, virtual repositories | all searchable repositories |
| Extended result detail | Header, see SRCH-002 | none |

Output: list of item-info links. Privilege: privileged caller, anonymous allowed.

Edge cases: the checksum must be complete; no wildcards and no partial matches. Behavior when
more than one checksum parameter is sent is unspecified. Results are capped per SRCH-003.

UI location: global search, artifacts, type "checksum" (SRCH-052); checksum keyword of the
artifact browser search (SRCH-059).

Interactions: checksums and checksum-based deploy in [artifacts.md](artifacts.md);
content-addressed storage in [storage-and-blobs.md](storage-and-blobs.md).

### SRCH-009 · Bad or missing client checksum search
Priority: **P2**

Lists files whose checksum supplied by the client at upload is missing or differs from the
checksum the server computed.

| Input | Meaning | Default |
|---|---|---|
| Checksum type | MD5 or SHA-1; required | none (required) |
| Repository restriction | Local repositories, remote caches, virtual repositories | all searchable repositories |

Output: list of records with the item-info link, the server-computed checksum and the
client-supplied checksum; a missing client checksum is reported as an empty string. Privilege:
privileged caller, anonymous allowed.

Limits: at most 10,000 results. The response shape is defined for the MD5 pair; the shape for a
SHA-1 search is unspecified.

Interactions: checksum policies and the original-versus-actual checksum fields (SRCH-025) in
[artifacts.md](artifacts.md).

### SRCH-010 · Not-downloaded-since search
Priority: **P1**

Lists files that have not been downloaded since a given moment; the basis for usage-driven
cleanup.

| Input | Meaning | Default |
|---|---|---|
| Not used since | Point in time, milliseconds since the Unix epoch; required | none (required) |
| Created before | Only files created before this point in time (epoch milliseconds) | the "not used since" value |
| Repository restriction | Local repositories or remote caches | all searchable repositories |

Output: list of records with the item-info link and the last-download timestamp (ISO 8601).
Privilege: privileged, non-anonymous caller.

Edge cases: whether files that were never downloaded are included is unspecified (download
statistics of zero are stored as "no value", see SRCH-030). Results are capped per SRCH-003.

Interactions: retention and cleanup policies in
[retention-and-cleanup.md](retention-and-cleanup.md); statistic fields (SRCH-025).

### SRCH-011 · Date-range search on selectable date fields
Priority: **P1**

Lists files for which at least one chosen date field falls inside a time window.

| Input | Meaning | Default |
|---|---|---|
| From | Start of the window: epoch milliseconds or an ISO 8601 string; required | none (required) |
| To | End of the window, same formats | now |
| Date fields | Comma-separated choice of: created, last modified, last downloaded; required | none (required) |
| Repository restriction | Local repositories or remote caches | all searchable repositories |

Output: list of records with the item-info link plus the requested date fields. A file matches
when any of the chosen fields lies inside the window. Privilege: privileged, non-anonymous caller.

Edge cases: omitting both bounds answers 400. The "created" timestamp can be later than
"last modified" for large uploads, because "last modified" records when the upload started and
"created" when it was committed. Results are capped per SRCH-003.

Interactions: SRCH-012; retention in [retention-and-cleanup.md](retention-and-cleanup.md).

### SRCH-012 · Created-in-date-range search
Priority: **P1**

Lists files created inside a time window.

| Input | Meaning | Default |
|---|---|---|
| From | Start, epoch milliseconds; required | none (required) |
| To | End, epoch milliseconds | now |
| Repository restriction | Local repositories or remote caches | all searchable repositories |

Output: list of records with the item-info link and the creation timestamp (ISO 8601). Privilege:
privileged, non-anonymous caller. May answer 404. Results are capped per SRCH-003.

Edge case: the bounds are documented as epoch milliseconds only; whether ISO 8601 strings are
also accepted, as they are in SRCH-011, is unspecified.

### SRCH-013 · Path pattern search
Priority: **P1**

Lists files in one repository whose path matches an
[Apache Ant](https://ant.apache.org/manual/dirtasks.html#patterns)-style pattern.

| Input | Meaning | Default |
|---|---|---|
| Pattern | `<repository key>:<path pattern>`, for example `libs-local:org/*/tool/*/*.war`; required | none (required) |

Output: one object with the repository's base link, the pattern as received, and the list of
matching file paths relative to the repository root. Privilege: privileged, non-anonymous caller.

Limits: the recursive `**` wildcard is not supported, so that result sets stay small; whether a
pattern containing it is refused with an error or handled otherwise is unspecified. `*` matches
within one path segment (Ant semantics).

Interactions: include/exclude patterns in [repositories.md](repositories.md) use the same pattern
family.

### SRCH-014 · Builds for a dependency
Priority: **P2**

Lists the builds whose build metadata names a given file as a dependency.

| Input | Meaning | Default |
|---|---|---|
| SHA-1 | SHA-1 of the dependency file; required | none (required) |

Output: list of links to build records (build name and build number). Privilege: privileged
caller, anonymous allowed.

Interactions: build metadata in [builds-and-ci.md](builds-and-ci.md); the item detail view's
list of producing and consuming builds (artifact information, [artifacts.md](artifacts.md)).

### SRCH-015 · License search
Priority: **Later**

Lists files that already carry license information, with their license and its approval status.

| Input | Meaning | Default |
|---|---|---|
| Include unapproved | Include files whose license is unapproved | on |
| Include unknown | Include files whose license is unknown | on |
| Include not found | Include files for which no license was found | off |
| Include neutral | Include files whose license is neutral | off |
| Include approved | Include files whose license is approved | off |
| Auto-find | Try to discover new license information during the search and report it | off |
| Repository restriction | Local repositories, remote caches, virtual repositories | all searchable repositories |

Output: list of records with the item-info link, the tagged license, the auto-found license (when
auto-find is on) and the status (approved, unapproved, neutral, unknown). Privilege:
administrators only.

Edge cases: only files already tagged with license information are searched, even with auto-find
on; auto-find can slow the search down considerably. To search for a specific license value, use
the property search (SRCH-007) on the system's reserved license property.

Interactions: license analysis and policies in
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### SRCH-016 · Version list search
Priority: **P2**

Lists all available versions of a module identified by group ID and artifact ID, and marks each
as release or integration (snapshot) version.

| Input | Meaning | Default |
|---|---|---|
| Group ID | Group or organization of the module | none |
| Artifact ID | Module name | none |
| Version | Version filter; accepts `*` and `?` | none |
| Remote lookup | Also ask the upstreams of remote repositories | off |
| Repository restriction | Local, remote and virtual repositories | local repositories and remote caches |

Output: list of records with the version string and an "integration" flag. Privilege: privileged
caller, anonymous allowed.

Behavior:

- Without a version, release and integration versions are both returned. With a version such as
  `1.0-SNAPSHOT`, only integration versions are returned; wildcards in the version filter the
  final list.
- Remote lookup: for Maven repositories the upstream `maven-metadata.xml` is read; for other
  layouts the remote folder listing is used, but only on remote repositories that have remote
  folder listing enabled. Remote lookup can slow the search down considerably.
- Integration versions are recognized through the repository layout: its artifact path pattern
  must contain the base-revision token followed by the file-integration-revision token, with only
  literal characters between them.

Edge case: result ordering is unspecified.

Interactions: repository layouts in [repositories.md](repositories.md); remote folder listing in
[remote-repositories.md](remote-repositories.md).

### SRCH-017 · Latest version by layout
Priority: **P2**

Returns the newest version of a module identified by group ID and artifact ID, interpreting paths
through the repository layout and ordering versions with Maven's version comparison.

| Input | Meaning | Default |
|---|---|---|
| Group ID | Group or organization of the module | none |
| Artifact ID | Module name | none |
| Version | Base version or filter; accepts `*` and `?` | none |
| Remote lookup | Also ask the upstreams of remote repositories | off |
| Repository restriction | Local repositories, remote caches, virtual repositories | local repositories and remote caches |

Output: the version string as plain text (an integration version can be a timestamped
version). Privilege: privileged caller, anonymous allowed.

Behavior: without a version, the latest release version is returned; with a version such as
`1.0-SNAPSHOT`, the latest integration version of that base version is returned. A version with
wildcards restricts the answer to versions that match the pattern.

Limits: the repository layout's artifact path pattern must contain the organization and module
tokens. The answer when nothing matches is unspecified. Remote lookup can be slow.

Interactions: SRCH-016; repository layouts in [repositories.md](repositories.md).

### SRCH-018 · Latest version by properties
Priority: **P2**

Returns the newest value of the version property among files that match a repository, a path and
optional property filters; optionally lists the files carrying that version.

| Input | Meaning | Default |
|---|---|---|
| Repository | Repository key, or a reserved placeholder meaning "any repository"; part of the request path | none (required) |
| Path | Path, or a reserved placeholder meaning "any path"; part of the request path | none (required) |
| List files | Off: only the version. On: the version and the files carrying it | off |
| Property filters | Any further request parameter is a property key with the required value, for example `os=linux` | none |

Output: an object with the latest version and a list of download links (empty unless "list
files" is on). Privilege: an authenticated, non-anonymous caller with read permission on the
repository of each file considered. May answer 404.

Edge cases: only files whose version property key is spelled exactly in lower case are
considered. Versions are ordered with a Maven-style version comparison.

Interactions: properties in [artifacts.md](artifacts.md); SRCH-007.

### SRCH-019 · Build artifacts search
Priority: **P1**

Returns download links for the files produced by a given build, optionally narrowed.

| Input (JSON body) | Meaning | Default |
|---|---|---|
| Build name | Required, at least one character | none (required) |
| Build number | Required; a reserved keyword selects the latest build number | none (required) |
| Build status | Pick the latest build that carries this promotion status (for example "Released") | none |
| Repositories | Only files in these repositories | all repositories |
| Mappings | List of regular expressions that narrow the result; what they are matched against is unspecified (reference examples select sources and javadoc JARs by file-name suffix) | none |

Output: list of download links. Privilege: privileged caller, anonymous allowed.

Edge cases: how the build number keyword and the build status interact is unspecified beyond
"latest build with that status". The regular-expression dialect is unspecified.

Interactions: build metadata and promotion in [builds-and-ci.md](builds-and-ci.md).

### SRCH-020 · Container image catalog listing
Priority: **P1**

Lists the image names hosted in one container repository, following the catalog operation of
the [OCI distribution specification](https://github.com/opencontainers/distribution-spec) /
Docker registry API.

| Input | Meaning | Default |
|---|---|---|
| Container repository key | Part of the path; required | none (required) |
| Page size | Number of names to return (registry protocol pagination) | unspecified |
| Last seen | Name from the previous page; the listing continues after it | none |

Output: an object with the list of image names. Privilege: read permission. May answer 404.

| Setting | Meaning | Default |
|---|---|---|
| Fall back to remote cache for catalog and tags | Serve catalog and tag listings of a remote container repository from the cache | off |
| Upstream registry for cached catalog and tags | URL of the upstream registry whose catalog and tags are cached (for example Docker Hub) | none |
| Refresh interval | Governed by the remote repository's metadata retrieval cache period | per remote repository |

Interactions: container format page under [formats/](formats/README.md); metadata retrieval
cache period in [remote-repositories.md](remote-repositories.md); SRCH-021.

### SRCH-021 · Container tag listing
Priority: **P1**

Lists the tags of one image in a container repository (registry API tag list).

| Input | Meaning | Default |
|---|---|---|
| Container repository key | Part of the path; required | none (required) |
| Image name | Part of the path; required | none (required) |
| Page size | Number of tags to return | unspecified |
| Last seen | Tag from the previous page | none |

Output: an object with the image name and its list of tags. Privilege: privileged caller (the
exact permission is unspecified; the sibling catalog listing requires read). May answer 404.

| Setting | Meaning | Default |
|---|---|---|
| Filter non-conforming tag entries | Return only entries that conform to the OCI and Docker tag naming rules, so that digest references stored like tags are hidden | on |

Interactions: SRCH-020 (cache fallback settings apply to both listings).

### SRCH-022 · Parent manifest list lookup
Priority: **P2**

Finds every multi-platform manifest list (image index) in a container repository that references
a given image manifest.

| Input | Meaning | Default |
|---|---|---|
| Container repository key | Part of the path; required | none (required) |
| Image name | JSON body; required | none (required) |
| Tag | JSON body; required | none (required) |

Output: list of parent manifest lists, each with image name, digest and its tags. Privilege: read
permission on the container repository. May answer 404.

Interactions: container format page under [formats/](formats/README.md); deleting a manifest
that a list still references (cleanup) in [retention-and-cleanup.md](retention-and-cleanup.md).

## Query language

### SRCH-023 · Query execution operation
Priority: **P2**

A single management API operation runs a query written in the query language. The query is the
plain-text request body of a POST request; the answer is JSON. The server streams rows from the
database into the response to keep memory use low.

Output: an object with a list of result records and range metadata: start position, end position,
total count, the applied limit (when it is present is unspecified) and a notification text when
the hard cap was hit (SRCH-042).

The query language only sees data held by this instance: local repositories, remote caches and
virtual repositories (resolved to their members, SRCH-035). Content that exists only upstream is
reachable only through transitive search (SRCH-039).

| Status | When |
|---|---|
| 200 | Query executed |
| 400 | Empty query, query text over the size limit (SRCH-043), syntax or validation error |
| 401 | Bad credentials |
| 403 | Anonymous caller; non-administrator using an administrator-only domain |
| 408 | Query exceeded its timeout and the database cancelled it (SRCH-044) |
| 429 | Concurrency limit reached (SRCH-045) |
| 500 | Unexpected error |

Privilege: any authenticated caller; anonymous callers are always refused. Authentication: HTTP
Basic, API key header, bearer token. Domain and output restrictions per SRCH-036.

Other consumers: the UI runs queries internally (with its own timeout, SRCH-044), and client
tooling can embed a query in a declarative upload/download specification instead of a path
pattern (companion CLI, see [builds-and-ci.md](builds-and-ci.md)).

Interactions: SRCH-024 to SRCH-047 and SRCH-064; the query language is also exposed for plugins
and scripts in
[events-and-extensibility.md](events-and-extensibility.md).

### SRCH-024 · Query structure and primary domains
Priority: **P2**

A query names exactly one primary domain, calls the find method with a criteria object, and may
append modifiers in this order: output field selection (SRCH-032), sort, offset, limit
(SRCH-033), distinct (SRCH-034), and the transitive marker (SRCH-039). The criteria object may be
empty. Instead of find, two actions exist: delete (SRCH-040) and property update (SRCH-041).

Syntax is JSON-like: field names and string values are in double quotes (unquoted field names and
single quotes are listed as mistakes), numbers may be quoted or unquoted, lists are JSON arrays,
comparison and logical operators are keys that start with a reserved sigil, property keys carry a
marker prefix (SRCH-027), path separators are forward slashes (a backslash path does not match),
and field names are case-sensitive (a field name in another case does not match). A range needs
an explicit comparison operator; a value such as `>1000` is not read as a comparison.

| Primary domain | Content | Restriction |
|---|---|---|
| Items | Files and folders | none beyond SRCH-036 |
| Builds | Build metadata records | administrators, or a token with build read scope |
| Build promotions | Promotion records of builds | administrators |
| Release sets | Older-generation release sets created by the distribution component | administrators |
| Release set artifacts | Files that belong to such release sets | administrators |
| Item infos | Metadata about property changes on items | administrators (non-administrators may only use items, SRCH-036) |
| Item properties | Only as target of the property update action (SRCH-041) | unspecified |

Related, non-primary domains: item properties, statistics, build artifacts, modules, module
properties, dependencies, build properties. They are reached through relation paths (SRCH-026).

Edge cases: an archive-entry domain existed formerly; it has been removed, and any reference to
it (as primary domain, field, or output selection) is a parse error. The syntax reference lists
only items, builds, build promotions, release sets and release set artifacts as primary domains;
the field reference additionally allows item infos as primary domain.

Interactions: release sets in [distribution-and-lifecycle.md](distribution-and-lifecycle.md);
builds and promotions in [builds-and-ci.md](builds-and-ci.md).

### SRCH-025 · Domain fields
Priority: **P2**

Each domain exposes a fixed set of typed fields usable in criteria and output selection. Types are
string, date, integer, long and enumeration.

<details>
<summary>Fields per domain</summary>

**Items**

| Field | Type | Meaning |
|---|---|---|
| Repository key | string | Repository that stores the item |
| Path | string | Path of the folder that contains the item, relative to the repository root |
| Name | string | Item name |
| Created | date | When the item was created |
| Modified | date | File-system timestamp of the last modification |
| Updated | date | Time of the item's most recent upload into a repository |
| Created by | string | Owner of the item |
| Modified by | string | Last user who modified the item |
| Type | enum | file, folder or any; a query without a type criterion searches files only |
| Depth | integer | Depth of the item below the repository root |
| Original MD5 | string | MD5 as recorded when the item was first uploaded |
| Actual MD5 | string | Current MD5 |
| Original SHA-1 | string | SHA-1 as recorded when the item was first uploaded |
| Actual SHA-1 | string | Current SHA-1 |
| SHA-256 | string | SHA-256 |
| Size | long | Size on disk |
| Containing virtual repositories | string | Virtual repositories that include the item's repository (SRCH-035) |

**Builds**

| Field | Type | Meaning |
|---|---|---|
| URL | string | Link to the build on the CI server |
| Name | string | Build name |
| Number | string | Build number |
| Created | date | File-system timestamp of creation |
| Created by | string | User who created the build record |
| Modified | date | File-system timestamp of the last modification |
| Modified by | string | Last user who modified it |
| Started | date | Start time recorded in the build metadata; immutable, unchanged by promotion or replication |
| Repository | string | Repository associated with the build record |

**Build promotions**

| Field | Type | Meaning |
|---|---|---|
| Created | date | Time of the promotion |
| Created by | string | System user who promoted it |
| Status | string | Promotion status |
| Repository | string | Target repository of the promotion |
| Comment | string | Free-text comment |
| User | string | CI-server user who triggered the promotion |

**Properties** (items, modules and builds each have their own)

| Field | Type | Meaning |
|---|---|---|
| Key | string | Property key |
| Value | string | Property value |

**Statistics**

| Field | Type | Meaning |
|---|---|---|
| Last downloaded | date | Time of the last download |
| Downloads | integer | Total download count |
| Last downloaded by | string | User of the last download |
| Remote downloads | integer | Downloads through a chained remote repository on another instance that proxies this local repository |
| Remote last downloaded | date | Last download through such a chained remote |
| Remote last downloaded by | string | User of that last remote download |
| Remote origin | string | Address of the downstream instance along the chain |
| Remote path | string | Full path along the chain |

**Build artifacts**

| Field | Type | Meaning |
|---|---|---|
| Name | string | Artifact name |
| Type | string | Artifact type |
| SHA-1 | string | SHA-1 |
| MD5 | string | MD5 |

**Modules**

| Field | Type | Meaning |
|---|---|---|
| Name | string | Module name |

**Dependencies**

| Field | Type | Meaning |
|---|---|---|
| Name | string | Dependency name |
| Scope | string | Dependency scope |
| Type | string | Dependency type |
| SHA-1 | string | SHA-1 |
| MD5 | string | MD5 |

**Release sets** (older generation)

| Field | Type | Meaning |
|---|---|---|
| Name | string | Release set name |
| Version | string | Release set version |
| Status | string | Status |
| Created | string (not date) | Creation date |
| Signature | string | Signature |
| Type | string | Type |
| Storing repository | string | Repository that stores it |
| Retention | integer | Retention setting |
| Source service ID | string | Identifier of the originating service |

**Release set artifacts**

| Field | Type | Meaning |
|---|---|---|
| Path | string | Full repository path of the file |

**Item infos**

| Field | Type | Meaning |
|---|---|---|
| Item ID | long | Internal ID of the item |
| Properties modified | date | Last change of the item's properties |
| Properties modified by | string | User of that change |
| Properties MD5 | string | MD5 over the item's properties |

</details>

Edge cases: the rule for how a folder's path versus a file's path is written in criteria (with
or without a trailing wildcard segment) is ambiguous; behaviour for folder
criteria is therefore unspecified beyond the type field.

Interactions: original-versus-actual checksums feed SRCH-009; statistics are the data behind
SRCH-010; chained remote repositories in [remote-repositories.md](remote-repositories.md).

### SRCH-026 · Cross-domain relation paths
Priority: **P2**

Fields of the primary domain are referenced by their bare name. A field (or property) of any
other domain needs the full chain of relations leading to it from the primary domain, in criteria
as well as in output selection.

Known relation paths:

| From | Path | Typical use |
|---|---|---|
| Item | → statistics | Items by download count or last download |
| Item | → properties | Items by property |
| Item | → build artifact → module → build (→ build properties) | Items produced by a named build or by builds run on a given OS |
| Build | → module → build artifact → item (→ item properties) | Builds that produced a given file or files with a given license |
| Build | → module → dependency → item (→ item properties) | Builds that used a snapshot or a dependency under a given license |
| Item properties | → item | Item criteria inside the property update action (SRCH-041) |

Output shape: when output selection includes related-domain fields, related records are nested
as lists under the primary record (for example build → modules → artifacts → items), and the
primary record's own fields are qualified with the domain name.

Edge cases: other relation paths may exist; the complete relation graph is unspecified. Build
promotions are queried as their own primary domain; whether a builds query can reach promotion
fields through a relation path is unspecified. Deep paths mean many joins and slow queries
(SRCH-064).

### SRCH-027 · Field criteria and property criteria
Priority: **P2**

A criterion compares one field or one property with a value.

- **Field criterion, general form:** field → (operator → value).
- **Field criterion, short form:** field → value means "equals".
- **Property criterion:** the property key, written with the property marker prefix, → (operator
  → value), or in short form → value meaning "equals". Properties can be searched on items,
  modules and builds; a property of a non-primary domain needs the relation path in front of the
  marker.
- **Any key:** the marker followed by `*` as key matches a property with any key.
- **Any value:** `*` as the complete short-form value matches any value of that key.

Edge cases:

- `*` is a wildcard only as the complete short-form value; a short-form value that merely
  contains `*` (for example `*GPL`) is taken literally. Wildcards inside values need the match
  operators (SRCH-030).
- The short forms are equivalent to a conjunction over the property domain's key and value
  fields; both notations must give the same result.

Interactions: SRCH-028, SRCH-029, SRCH-030; properties in [artifacts.md](artifacts.md).

### SRCH-028 · Logical composition of criteria
Priority: **P2**

Criteria combine with AND and OR operators, each taking a list of criteria. Nesting is allowed to
any depth. Criteria written side by side in one object are combined with AND (the implicit
default).

Edge cases: reference examples place two criteria with the same field key in one object, and
pass two criteria objects as separate arguments to find; how duplicate keys and multiple
argument objects are interpreted is unspecified.

### SRCH-029 · Single-property match group
Priority: **P2**

An item usually carries many properties, possibly several values under the same key. In a
normal find, a negative match criterion on a key rules the item out as soon as any one of its
values for that key matches the excluded pattern, even when another value of the same key would
satisfy every criterion. Example: with the criteria "license matches `*GPL*`" and "license does
not match `LGPL-2.1*`", an item whose only license value is `GPL-3.0` is returned, but an item
with the two values `LGPL-2.1` and `LGPL-3.0` is not, because one of its values matches the
excluded pattern.

A single-property match group (a dedicated operator taking a list of property criteria) returns
an item only if at least one single property value satisfies every criterion in the group; in the
example both items are returned (`GPL-3.0` and `LGPL-3.0` each satisfy both criteria).

The group works on every domain that has properties: items, modules and builds.

### SRCH-030 · Comparison operators and wildcards
Priority: **P2**

| Operator | Applies to | Meaning |
|---|---|---|
| Not equal | string, date, integer, long | Field differs from value |
| Equal | string, date, integer, long | Field equals value |
| Greater than | string, date, integer, long | Field > value |
| Greater or equal | string, date, integer, long | Field ≥ value |
| Less than | string, date, integer, long | Field < value |
| Less or equal | string, date, integer, long | Field ≤ value |
| Match | string | Field matches the wildcard pattern |
| Not match | string | Field does not match the wildcard pattern |

Wildcards: in match and not-match, `*` stands for any string and `?` for exactly one character.
With every other operator `*` and `?` are literal characters.

Edge cases:

- Statistic fields with value zero are stored as "no value": "never downloaded" is expressed as
  "downloads equals null", and a comparison with `0` does not find those items.
- A query whose only criterion is a wildcard match on the repository key can turn into an overly
  broad database query. Its result can then contain internal items of layered formats (for
  example container image layers) that belong to the repository even though their repository key
  does not match the pattern. Combine such criteria with type, path or name.
- A leading wildcard (`*keyword*`) forces a full scan; a prefix pattern is the recommended
  alternative (SRCH-064).

### SRCH-031 · Absolute and relative time criteria
Priority: **P2**

Absolute dates follow the [W3C profile of ISO 8601](https://www.w3.org/TR/NOTE-datetime): the
full form is `YYYY-MM-DDThh:mm:ss.sTZD` (for example `2024-03-05T08:15:30.45+01:00`), and partial
precision is accepted (year only, year and month, a full date, and so on). Absolute dates are
compared with the ordinary comparison operators.

Two relative-time operators evaluate a period against the moment the query runs, so a saved query
needs no editing:

| Operator | Meaning |
|---|---|
| Within the last | From (now minus period) until now |
| Before | Everything up to (now minus period) |

A period is a number followed by a unit suffix. Units: milliseconds, seconds, minutes, days,
weeks, months, years; every unit has a long suffix and all except minutes also a short one (for
example `5d` for five days, `2w` for two weeks).

### SRCH-032 · Output field selection
Priority: **P2**

Every primary domain has a default output field set; an optional selection modifier replaces or
extends it.

| Selection | Result |
|---|---|
| None | Default fields of the primary domain |
| `*` | All fields of the primary domain |
| Specific primary-domain fields | Only those primary-domain fields |
| Only related-domain fields (for example a statistic field) | Default primary fields plus the named related fields |
| A domain name alone (for example statistics, properties) | Default primary fields plus all fields of that domain |
| Properties wildcard | All properties of each result |
| One property key | Only that property, and items without it are dropped from the result |

Default fields: items return repository key, path, name, type, size, created, created by,
modified, modified by and updated. Builds return created, created by, name, number and URL (from
a reference example). Defaults of the other domains are unspecified.

Edge cases: selecting a property key is also a filter (items lacking the key disappear).
Selecting related-domain fields disables sort, offset and limit (SRCH-033). For non-administrators
the selection must contain the permission-relevant fields (SRCH-036).

### SRCH-033 · Sorting, offset and limit
Priority: **P2**

- **Sort:** ascending or descending over a list of fields; the fields must be part of the output
  (by default or by selection) and must belong to the primary domain. A default sort order exists
  but is unspecified.
- **Offset:** skip the first N results.
- **Limit:** return at most N results; without limit all matches are returned, subject to the
  hard cap (SRCH-042).

Limits and edge cases:

- Sort, offset and limit take effect only when the query has no output selection or selects
  primary-domain fields only; with related-domain fields in the selection they are ignored (the
  query still runs). Consequently results cannot be sorted by a property.
- The limit is applied before permission filtering, so a caller can receive fewer than N rows
  (SRCH-037). An item with several properties can also yield fewer distinct items than N.
- Large offsets are slow; narrowing criteria or a cursor-style approach is preferred (SRCH-064).
- Not available on transitive queries except limit (SRCH-039), and not on the delete action
  (SRCH-040).

### SRCH-034 · Distinct results toggle
Priority: **P2**

A modifier switches duplicate elimination on or off. Default: on (unique results). Turning it off
trades possible duplicate rows for speed.

### SRCH-035 · Virtual repository targeting
Priority: **P2**

A repository-key criterion that names a virtual repository searches the local repositories and
the caches of the remote repositories that the virtual repository aggregates.

- The "containing virtual repositories" field is added to the output automatically when the query
  targets a virtual repository; otherwise it appears only when selected explicitly. It requires
  repository key, path and name to be in the output too.
- An item is found through a virtual repository only if the virtual repository can actually
  reach it; a member repository's include/exclude patterns that hide the item from the virtual
  repository also hide it from the query (SRCH-038).

Interactions: virtual repositories in [repositories.md](repositories.md); SRCH-039 for content of
chained remote instances.

### SRCH-036 · Query permission model
Priority: **P2**

| Caller | Allowed primary domains | Output requirements | Results |
|---|---|---|---|
| Anonymous | none (403) | — | — |
| Administrator | all | none | everything, regardless of include/exclude patterns |
| Other user, ordinary credentials | items only | output must contain repository key, path and name | only items the caller may read |
| Token scoped to read on artifact resources | items | as above | items inside the token's repository/path scope |
| Token scoped to read on build resources | builds | output must contain build name, number and repository | builds matching the token's build scope |
| Token with both scopes | items and builds | as above | both |

Once the required fields are present, a non-administrator may select any other field they can
access, from any domain. A query that lacks the required fields fails or produces repeated
warnings in the server log. A build-scoped token is
the intended way to let CI tools query builds without administrator rights.

Interactions: administrator role and anonymous access in
[identity-and-access.md](identity-and-access.md); token scopes in
[access-tokens.md](access-tokens.md); SRCH-037, SRCH-038.

### SRCH-037 · Limit before permission filter and scan multiplier
Priority: **P2**

With a limit N, the server fetches N candidate rows first and checks read permission
afterwards. Candidates in unreadable repositories are dropped, so the caller can get fewer than N
results, or none, while readable matches lie beyond the first N candidates; running the query
without a limit returns them.

To compensate, a scan multiplier makes the server fetch N × factor candidates and stop after N
readable results:

| Setting | Meaning | Default |
|---|---|---|
| Per-request scan multiplier | Request header carrying an integer greater than 1; overrides the system setting for that request | none |
| System scan multiplier | Factor used when the request carries no multiplier above 1; applies to every caller, administrators included | 1 (no extra scanning) |

Limits and edge cases: applies only when the query has a limit greater than 0; the limit should
stay below the hard cap (SRCH-042); not effective when the system runs on its embedded
database. A higher factor means more database load. The hosted variant only supports the
per-request header.
Alternative without extra load: restrict the criteria to repositories the caller can read.

### SRCH-038 · Include/exclude patterns filter query results
Priority: **P2**

Query results respect the include and exclude path patterns of local, remote and virtual
repositories: for non-administrators, items whose path is excluded (or not included) by their
repository are removed from the result. Administrators see all matching items regardless of
patterns.

Interactions: include/exclude patterns in [repositories.md](repositories.md); SRCH-035.

### SRCH-039 · Transitive search into chained remote instances
Priority: **Later**

A transitive marker appended to a query extends the search to content that lives on another
instance of the same system, reached through a chained remote repository (a remote repository
whose upstream is a repository on another instance, configured with credentials for it). Without
the marker, only cached copies are searched.

Rules:

- The upstream must be an instance of the same system; other upstreams are not searchable.
- The query must target exactly one repository with an equality criterion on the repository key.
- Sort and offset are not allowed; limit is.
- Primary domain must be items; output selection may only use the item and item-property domains.
- Targeting a virtual repository searches its first N remote members.

| Setting | Meaning | Default |
|---|---|---|
| Remote members searched per virtual repository | How many remote repositories of a virtual repository a transitive query visits | 5 |

Edge cases: in the reference output, cached content is reported under the remote repository's
cache key and content found on the upstream instance under the remote repository's own key; some
records in the same reference output lack selected fields, so the exact record shape is
unspecified. More members and larger results increase query time; clients sort and paginate
locally.

Interactions: chained remote repositories and remote folder listing in
[remote-repositories.md](remote-repositories.md); multi-site topology in
[replication-and-federation.md](replication-and-federation.md).

### SRCH-040 · Bulk delete action with dry run
Priority: **P2**

Instead of find, a delete action removes every item matched by the criteria (same criteria syntax
as find). Available on the items domain only. A dry-run flag is part of the action: "true"
simulates the deletion and shows which items would be affected; "false" deletes.

Limits: the delete action accepts none of the sort, offset or limit modifiers.

Unspecified: the required privilege (delete permission per item or administrator), the response
format of a dry run and of a real run, and whether deleted items go to the trash can.

Interactions: trash can and delete in [artifacts.md](artifacts.md); cleanup policies in
[retention-and-cleanup.md](retention-and-cleanup.md).

### SRCH-041 · Bulk property update action with dry run
Priority: **P2**

An update action on the item-property domain sets a new value on matching properties. Inputs: the
criteria (item fields are addressed through the relation path from property to item), one or more
property keys (required), the new value (required), and the dry-run flag ("true" simulates,
"false" executes).

Limits: only item properties; build and module properties cannot be updated this way.

Unspecified: the required privilege (annotate permission per item or administrator), the response
format, and whether a key that an item does not yet carry is created or skipped.

Interactions: properties in [artifacts.md](artifacts.md); property change events
([events-and-extensibility.md](events-and-extensibility.md)).

### SRCH-042 · Result-set hard cap
Priority: **P2**

A server-wide maximum number of result rows protects the database. When a query reaches it, the
result is trimmed and the range metadata carries a notification text saying the hard limit was
reached and results were trimmed.

| Setting | Meaning | Default |
|---|---|---|
| Maximum result rows | Hard cap on rows per query | self-hosted: none; hosted variant: fixed at 500,000 |

Clients that see the notification narrow the criteria or paginate.

Interactions: SRCH-037 (keep limits below the cap); the separate cap on simple searches
(SRCH-003).

### SRCH-043 · Query text size limit
Priority: **P2**

The request body of a query may not exceed a maximum number of characters. An over-long query is
not executed; the answer is 400 with an error message that the query is too long and names the
configured limit.

| Setting | Meaning | Default |
|---|---|---|
| Maximum query length | Characters per query request body | 6,000 |

Edge cases: raising the limit is discouraged; even around 8,000 characters large or
complex queries can fail with 500. The hosted variant does not allow changing it. Before the
limit existed, the same over-long queries could fail with 500 instead of 400, so clients that
upgrade can see previously working queries rejected. Clients split long OR lists into several
queries and merge results.

### SRCH-044 · Query timeouts
Priority: **P2**

Queries run under a database timeout; on expiry the database cancels the query and the API
answers 408.

| Setting | Meaning | Default |
|---|---|---|
| API query timeout | Maximum run time of queries sent through the API | 900 s |
| UI query timeout | Maximum run time of queries the UI triggers | 120 s |
| Streamed query timeout | Maximum run time in streaming mode (SRCH-046) | 7,200 s |
| Timeout override | When set, replaces all three values | not set |

### SRCH-045 · Concurrency limiting for queries and searches
Priority: **P2**

The server bounds how many query-language and search API calls run in parallel, globally and per
user. A request that cannot start waits up to a timeout and is then refused with 429 Too Many
Requests; clients retry after a short delay.

| Setting | Meaning | Default |
|---|---|---|
| Concurrency limiting enabled | Switches the mechanism on or off | off (it was on by default in the release that introduced it) |
| Exempt user agents | Comma-separated client user agents that are never limited | the system's own internal service clients |
| Global concurrency | Maximum parallel query-language and search API calls | 3 |
| Per-user share | Percentage of the global concurrency one user may occupy | 100 |
| Pending wait timeout | How long a request waits for a free slot before 429 | 10,000 ms |

Per-user share semantics: 0 or below disables the per-user limit (only the global one applies);
1 to 99 gives that percentage of the global value, rounded down, at least 1; 100 lets one user
use the whole global limit; above 100 is treated as 100. Example: global 4 and share 50 gives
each user 2 parallel queries while others can still use the remaining slots. The per-user limit
only applies while limiting is enabled, and exempt user agents are never limited. One wait
timeout covers both limits together: a request that waited for a per-user slot has that much
less time left to obtain a global slot. The hosted variant fixes the per-user share at 100.

### SRCH-046 · Streaming mode for large result sets
Priority: **P2**

A request header switches a query to streaming mode: instead of materializing the whole result,
the server reads rows through a database cursor, 50,000 rows per batch by default, which lowers
server memory use. Streamed queries have their own, longer timeout
(SRCH-044).

### SRCH-047 · Compact output
Priority: **P2**

A query parameter on the query operation selects minified JSON without whitespace. Default
output is pretty-printed JSON.

### SRCH-064 · Index-friendly criteria and query cost guidance
Priority: **P2**

The reference behavior comes with a cost model that a compatible server has to honor through
its indexes, and that clients rely on when they shape queries.

| Field | Expected index support | Use in criteria |
|---|---|---|
| Repository key | indexed | narrow by it first |
| Path | indexed | high selectivity |
| Name | indexed | high selectivity |
| SHA-256 | indexed | point lookups |
| Type | indexed | medium |
| Created, modified | varies | medium |
| Property keys | varies | lowest; combine with item fields |

<details>
<summary>Costly query shapes and the documented alternative</summary>

| Shape | Why it is costly | Alternative |
|---|---|---|
| Match pattern with a leading wildcard | full scan | prefix pattern |
| Find with empty criteria | unbounded | add a repository-key criterion |
| Many OR-ed wildcard patterns | expensive | several separate queries |
| Deep relation paths (SRCH-026) | many joins | shorter paths |
| Large offset | slow skip | cursor-style paging on a sorted key |
| All fields plus several related domains in the output | wide rows | select only the needed fields |

</details>

Guidance values for the limit (SRCH-033), stated as recommendations, not enforced: interactive
use 50 to 200 rows; batch jobs 1,000 to 10,000; export or audit around 50,000 with pagination.
Many single-item lookups are cheaper as one query with an OR list than as one query each, as long
as the query text stays under the size limit (SRCH-043).

Interactions: SRCH-030 (wildcards), SRCH-033 (paging), SRCH-042 (hard cap), SRCH-043 (size
limit).

## UI search

### SRCH-048 · Global search box
Priority: **P1**

A search box in the top bar is available on every page. A selector next to it chooses the object
type to search: packages, artifacts, builds, release sets, scanned resources, and archived
(cold-storage) items. The preselected type follows the current page (on the packages page,
package search is active).

Flow: choose the type, type a term, press Enter; the results list appears; clicking a result opens
the item with its details.

Syntax: predefined keywords (SRCH-049), wildcards `*` (any string) and `?` (one character, for
example `reac?` finds `react`), and free text with one or more words and Boolean expressions
(the Boolean syntax is unspecified). Matching is described as case-sensitive.

An advanced-search button opens a filter panel specific to the chosen object type; every filter
set there writes the corresponding keyword into the search field (for example choosing a package
type adds a package-type keyword).

Edge cases: release set search covers only the older generation of release sets (SRCH-056).
Release set search and scanned-resource search depend on subsystems the system may not have
installed.

Interactions: SRCH-049 to SRCH-057; archived items search in
[retention-and-cleanup.md](retention-and-cleanup.md); screen inventory in
[ui-inventory.md](ui-inventory.md).

### SRCH-049 · Search keywords
Priority: **P1**

Keywords narrow a global search and can be combined with free text; the advanced filter panel is
built on the same keywords.

| Keyword (descriptive) | Meaning | Example value |
|---|---|---|
| Package type | Only this package format | npm |
| Repository | Only this repository; described as filtering by repository type (local, remote, virtual, distribution), while its example value is a repository key | containers-hosted |
| Item type | Kind of result (shown in an example only) | package |
| Checksum | Packages or files with this checksum | 9c1e04d7ab |
| Properties | Comma-separated key=value pairs that must all be present | approved=true,team=web |
| Created before | Created before a date | 2024-06-30 |
| Created after | Created after a date | 2024-01-01 |
| Vulnerability ID | Resources affected by this CVE | CVE-2024-0001 |
| License | Resources containing this license | Apache-2.0 |
| Severity | Resources with violations of these severities (comma-separated) | high,low |
| Artifact search type | Sub-type of artifact search: packages, archive entries, properties, checksum, trash | Checksum |
| Version | Version, for all package formats (artifact search of type "package") | 1.2.3 |

Format-specific keywords are listed in SRCH-050; archive-entry options in SRCH-005.

Edge cases: the vulnerability, license and severity keywords need the scanning subsystem
([security-scanning-and-curation.md](security-scanning-and-curation.md)); without it they cannot
match.

### SRCH-050 · Format-specific package metadata search fields
Priority: **P1**

The artifact search of type "package" offers, per package format, input fields for that format's
metadata. Under the hood it matches against the standard properties the system attaches to every
package on upload (for example package ID and version for NuGet); for most formats the search is
executed as a query-language query (SRCH-054). All fields accept `*` and `?`.

<details>
<summary>Format-specific fields</summary>

| Field | Formats |
|---|---|
| Version | all formats |
| Category | Chef |
| Platform | Chef, Conda |
| User | Conan |
| Channel | Conan |
| OS | Conan |
| Architecture | Conan, Conda, Debian, Opkg, RPM |
| Build type | Conan |
| Compiler | Conan |
| Priority | Debian, Opkg |
| Maintainer | Debian, Opkg |
| Distribution | Debian |
| Component | Debian |
| Digest | Docker |
| A further Docker field (meaning unspecified) | Docker |
| App version | Helm |
| Group ID | Maven |
| Artifact ID | Maven |
| Classifier | Maven |
| Scope | npm |
| Keywords | npm |
| Box provider | Vagrant |

The package name field and the format selector are always present; the per-format field set
beyond this table is unspecified.

</details>

Limits and edge cases: does not work on remote caches of RubyGems, Debian and PHP Composer
repositories. When restricted to specific repositories, only repositories of the matching format
can be chosen; repositories with a wrong layout give unreliable results.

Interactions: SRCH-001 (properties versus package-metadata store), SRCH-054; format pages under
[formats/](formats/README.md).

### SRCH-051 · Package search
Priority: **P1**

With object type "packages", the global search queries the package-metadata store (SRCH-001).
Advanced filters: package type (list), package name, description (keywords in the package
description, for formats that have one) and metadata keywords (called tags or labels by some
formats).

Interactions: package views in [ui-inventory.md](ui-inventory.md); SRCH-001 for the rename
mismatch.

### SRCH-052 · Artifact advanced search types
Priority: **P1**

With object type "artifacts", the advanced panel offers a type selector; each type has its own
input fields:

| Type | Inputs | Behavior |
|---|---|---|
| Name | Name with `*` and `?`; optional selection of repositories | Same as SRCH-004 |
| Package | Format selector plus format fields (SRCH-050); optional repositories | Property-based package search |
| Property | Rows of key (typed or picked from a list of known keys) and value; a control appends further rows; values accept `*` and `?` | All rows are combined with AND, including several values given for the same key |
| Checksum | One checksum; a repository to search in | No wildcards; the term must be a complete, valid checksum |
| Trash | See SRCH-053 | Searches the trash can |

Results appear in a list; an action menu at the end of each row offers actions that depend on
the resource type (the set is unspecified for this view; compare SRCH-061).

Edge cases: two descriptions of the checksum type conflict: one accepts MD5, SHA-1 or SHA-256, the
other requires a valid MD5 or SHA-1 value; the API counterpart (SRCH-008) accepts all three.

Interactions: SRCH-004, SRCH-007, SRCH-008, SRCH-050, SRCH-053.

### SRCH-053 · Trash search
Priority: **P2**

An artifact search type that searches the contents of the trash can. Input: a query on the item
name, or, with a checksum toggle, a checksum. Results list trashed items.

Interactions: trash can (restore, purge) in [artifacts.md](artifacts.md).

### SRCH-054 · Show the generated query
Priority: **P2**

After a package-type artifact search (SRCH-050) that was executed as a query-language query, a
button reveals the query that was run; a copy icon puts it on the clipboard. Users can then refine
it and run it through the API (SRCH-023).

### SRCH-055 · Build search
Priority: **P1**

With object type "builds", the advanced panel filters by build name and by a creation window
(created after, created before), meaning the time the build record was deployed.

Interactions: build views in [builds-and-ci.md](builds-and-ci.md).

### SRCH-056 · Release set search
Priority: **Later**

With object type "release sets", the advanced panel filters by release set name and a creation
window (created after, created before), and searches both distributable release sets and release
sets received from another instance. Only the older generation of release sets is covered; the
newer generation is searched in its own views.

Interactions: [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### SRCH-057 · Scanned resource search
Priority: **Later**

In the security scan list, an advanced search finds resources (packages, builds, release sets,
artifacts) that the scanning subsystem has scanned.

| Filter | Meaning |
|---|---|
| Name | Issue name |
| License | A specific license, or unknown licenses |
| Severity | High, Medium, Low or Unknown |
| Date range | Restrict to a time window |

Results are grouped by resource type in tabs, each showing how many scanned resources of that
type carry an issue; clicking a resource opens its issue page.

Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md).

### SRCH-058 · Artifact browser search
Priority: **P1**

The artifact browser (the page with the repository tree) has a search field that searches all
repositories at once and finds repositories, folders and files.

Matching rules:

- **Prefix matching by default.** A term matches names that start with it; no explicit wildcard is
  needed. A term never matches in the middle of a word.
- **Single term.** Finds items whose name starts with the term, and items below a matching
  repository or folder.
- **Path term.** A term containing `/` matches items at that path; it may start at a repository or
  at any folder.
- **Several terms** separated by spaces find items whose path matches all terms, in any order.
- **Exact-match toggle.** When switched on, only items whose name equals the term are returned,
  not items whose name merely contains it.
- **Wildcard `*`.** Allowed only at the end of a term (equivalent to the plain term) or as a whole
  path segment; as a whole segment it matches across folder levels, including `/`. Leading
  wildcards or wildcards in the middle of a word return nothing.
- **Tokenization.** A hyphen splits a term into separate tokens, each matched as a prefix (so
  `web-ui` also finds `web-uikit`); a dot does not split (`bundle.tgz` is one token).

<details>
<summary>Worked examples (item <code>npm-hosted/team-a/widget-2.1.0.tgz</code>)</summary>

| Term | Result |
|---|---|
| `npm-hosted/team-a/widget-2.1.0.tgz` | the file |
| `npm-hosted/team-a` | the folder and the file |
| `team-a/widget-2.1.0.tgz` | the file |
| `npm-hosted/*` | the folder and the file |
| `npm-hosted/*/widget` | the file |
| `team-a/*` | the file |
| `npm*wid` | nothing (wildcard inside a term) |
| `*team*wid` | nothing (leading wildcard) |
| `npm-hosted team-a widget` | the repository, the folder and the file |
| `npm team wid` | the repository, the folder and the file |
| `npm-hosted` | the repository, the folder and the file |
| `team-a` | the folder and the file |
| `widget` | the file |
| `npm*` | the repository only |
| `tea*` | the folder only |
| `wid*` | the file |

</details>

Limits:

- The result order is fixed: by item type (repositories, then folders, then files), then by
  modification time; filters can narrow but not reorder.
- Files inside archives are not found.
- Without a repository scope, items are shown in the repository that physically holds them
  (local, federated, or the cache of a remote repository); virtual repositories are searched only
  through the repository scope keyword (SRCH-059).
- Selecting a location in the tree scopes the search to it; the search field then shows a scope
  indicator naming that location.

Edge cases: the reference behavior is inconsistent about trailing wildcards: a trailing `*` is
described as equivalent to the plain term, yet a plain repository name returns the repository
and its content while a prefix with `*` returns only the repository. The examples above reproduce
the reference results.

Interactions: SRCH-059 to SRCH-063; tree and item details in [artifacts.md](artifacts.md).

### SRCH-059 · Artifact browser scope keywords
Priority: **P1**

Three keywords narrow the artifact browser search and can be combined with each other and with
free terms:

| Keyword (descriptive) | Meaning | Rules |
|---|---|---|
| Repository scope | Only this repository (local, remote cache, virtual or federated) | Exactly one repository; the only way to search inside a virtual repository |
| Path scope | Only below this folder | Allowed only after a repository scope |
| Checksum | Files with this checksum | Complete checksum only; equality, no partial match |

Examples (same item as SRCH-058): repository scope `npm-hosted` alone returns the folder and the
file; repository scope plus the term `widget` returns the file; repository scope plus a checksum
returns the file; repository scope plus path scope `team-a` returns the file; a checksum alone
returns the file.

Pressing Space after a repository or path locks it in the search field as a scope chip.

### SRCH-060 · Search suggestions and recent searches
Priority: **P2**

While typing in the artifact browser search, suggestions appear once the term has at least four
characters, showing up to five items. Choosing a suggestion fills the field and reveals the item
in the tree; Enter, or the action that expands the suggestions to the complete list, opens the
full results table. The search field also lists
recent queries and recently visited results; the user can remove entries from that list.

### SRCH-061 · Artifact search results table
Priority: **P1**

Full results of the artifact browser search appear in a table.

- **Columns:** the user picks which columns are shown.
- **Filters:** item type, repository type, package type, created or modified time. Filtering by
  virtual repository type is not offered; virtual repositories are searched with the repository
  scope (SRCH-059).
- **Row actions** (depending on item type and the user's permissions): reveal in the tree (closes
  the table, opens the tree with only the item's repository shown and selects the item); mark or
  unmark a repository as favorite; download; delete (folders and files only).
- **Return:** an arrow next to the results heading or the breadcrumb leads back to the tree.

Interactions: download and delete in [artifacts.md](artifacts.md); permissions in
[identity-and-access.md](identity-and-access.md); favorites in SRCH-062.

### SRCH-062 · Repository tree filters, sort order and favorites
Priority: **P1**

Filters reduce what the repository tree shows:

| Filter | Meaning | Default |
|---|---|---|
| Repository name | Text filter on repository keys | unspecified |
| Repository type | One or more repository classes | unspecified |
| Package type | One or more package formats | unspecified |
| Compact view | Collapse chains of folders that each contain a single child into one path | unspecified |
| Sort | Sort key for the repository list: class, key or package format | unspecified |
| Favorites only | Show only repositories the user marked as favorite | unspecified |

A user marks a repository as favorite from the tree (hover action or context menu) or from the
repository's details. One UI variant stores the filter state in browser cookies and offers a
"clear" button that resets the tree.

| Setting | Meaning | Default |
|---|---|---|
| Repository class order in the tree | Order of repository classes when the tree is sorted by type | virtual, local, remote, cached |

A class left out of a configured order falls back to its default position.

Interactions: favorites are also used by SRCH-061; tree browsing in [artifacts.md](artifacts.md).

### SRCH-063 · Search keyboard shortcuts
Priority: **P2**

| Shortcut | Function |
|---|---|
| Cmd+K / Ctrl+K | Focus the search field |
| Enter | Run the search and show results, or pick the highlighted suggestion |
| Arrow keys | Move through suggestions |
| Space | Lock the current repository or path as scope |
| Esc | Close suggestions without choosing; leave the search field |

## Notes for implementers

- **Limits act before permission checks.** A limited query fetches N rows and then drops
  unreadable ones; clients that page through results must not treat a short page as the end.
  Either scan further (SRCH-037) or state the behavior to users; silently empty pages are the
  classic support case.
- **Zero is null in statistics.** "Never downloaded" is "no statistics value", not a count of
  zero; usage-based cleanup scripts query for the missing value (SRCH-030, SRCH-010).
- **Files only by default.** A query without a type criterion returns files only; folder results
  need an explicit type (SRCH-025).
- **Wildcards are operator-bound.** `*` and `?` are wildcards only in the match operators and as
  the complete "any value" short form; everywhere else they are literal characters (SRCH-027,
  SRCH-030). An implementation that treats them as wildcards everywhere breaks queries that rely
  on literal matches.
- **Non-administrator queries must carry the permission fields.** Repository key, path and name
  (builds: name, number, repository) must be in the output; tools written for administrators
  break when run with a normal user (SRCH-036).
- **Property criteria are evaluated per value, not per item.** Positive and negative criteria on
  the same key behave differently from the single-property match group (SRCH-029); clients use
  the group for "any license except X" queries.
- **Virtual repositories are not uniformly searchable.** Name search refuses them, coordinate
  search accepts them only in reduced mode, property and checksum search accept them, and the UI
  searches them only through the repository scope (SRCH-002, SRCH-059).
- **Package search and artifact search can disagree.** Package metadata is frozen at upload,
  artifacts reflect the current state (SRCH-001); tests should cover a hand-renamed file.
- **Query size is a hard 400.** Long generated OR lists must be split client-side; a compatible
  server rejects before executing and names the limit in the message (SRCH-043).
- **The cost model assumes indexes.** Clients are told to narrow by repository key, path, name
  and SHA-256 first; a server without indexes on those columns turns the recommended query shapes
  into full scans (SRCH-064).
- **Exact identifiers are left to the spec.** This page names query-language fields, operators,
  parameters and headers by meaning only; trove's own identifiers are defined in
  [docs/spec/](../spec/README.md).
