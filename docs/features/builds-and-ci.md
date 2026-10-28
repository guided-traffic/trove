# Builds and CI integration

This page covers build metadata: the record a CI run publishes to say which job, tools, modules,
dependencies, environment and source revision produced a set of artifacts. It describes the record
model, where records are stored and who may touch them, the server operations on records (publish,
list, retrieve, compare, append, aggregate, promote, rename, delete, retention), the CI integrations
that produce records, and the capabilities of a companion command-line client (spec-file transfers,
local metadata collection, package-manager wrapping, configuration commands, extension plugins).
Scheduled build cleanup policies are on
[retention-and-cleanup.md](retention-and-cleanup.md). The builds screens are on
[ui-inventory.md](ui-inventory.md). The CI token exchange is on
[oidc-ci-integration.md](oidc-ci-integration.md).

## Build record model

### BLD-001 · Build record and run identity
Priority: **P1**

A build record (build metadata) describes one run of a CI job. It is not an artifact. Stored files
sit in ordinary repositories and tell you what exists; the record ties those files to the run that
made them. Every build feature on this page (inspection, promotion, retention, build-scoped
search) reads the record. Browsing repository folders cannot replace it.

| Identity element | Meaning | Default |
|---|---|---|
| Build name | Names a stream of runs. Clients usually derive it from the CI project, workflow or job name (BLD-028). | none (required) |
| Build number | Names one run within the stream. Free-form string, for example a CI run number or an epoch timestamp. | none (required) |
| Start time | Timestamp of the run. Retrieval and append accept it as an optional extra key that selects one record when several records share name and number. | unspecified |
| Project | Optional scope of the record (BLD-011). | global scope |

Behavior:

- Name and number are always given together. Clients reject one without the other.
- The companion client has an explicit "overwrite every existing record with this name and number"
  publish option, which is off by default (BLD-039). This means a second publication of the same
  name and number is normally kept as a *further* record next to the first, told apart by start
  time, and does not replace it. On the server, replacing an existing record also requires the
  delete action (BLD-012). The exact key the server uses to decide "existing" (name and number, or
  name, number and start time) is unspecified.
- Allowed characters in names and numbers: unspecified. Build numbers with special characters
  (including commas) exist; the body form of the delete operation exists for them (BLD-023).

UI location: builds list and run details (UI-028, UI-029 in [ui-inventory.md](ui-inventory.md)).

Interactions: build permissions (IAM-025 in [identity-and-access.md](identity-and-access.md));
build events (EVT-007 in [events-and-extensibility.md](events-and-extensibility.md)); build search
(SRCH-055 in [search-and-query.md](search-and-query.md)).

### BLD-002 · Run-level information
Priority: **P1**

Besides its identity, a record carries information about the run as a whole. The record format
marks no field as mandatory. Which fields the server enforces beyond name and number is unspecified.

<details><summary>Run-level fields</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Record format version | Version of the record schema the client wrote | unspecified |
| Build name, build number | BLD-001 | required |
| Build type | One of Maven, Gradle, Ant, Ivy, generic | unspecified |
| Build tool | Name (type) and version of the build tool | unspecified |
| CI server | Name (type) and version of the CI server | unspecified |
| Start time | Instant the run started, format `yyyy-MM-dd'T'HH:mm:ss.SSSZ` (numeric zone offset, for example `+0000`) | unspecified |
| Publishing client version | Version of the plugin or extractor that produced the record | unspecified |
| Duration | Run duration in milliseconds (64-bit integer) | unspecified |
| Principal | The repository-manager user used for deployment | unspecified |
| CI build URL | Link back to the CI run | unspecified |
| Version-control entries | BLD-007 | empty |
| License-check request | BLD-009 | absent |
| Retention criteria | BLD-024 | absent |
| Modules | BLD-003 | empty |
| Issues | BLD-008 | absent |
| Properties | Environment variables and system properties (BLD-006) | empty |

</details>

UI location: the header of the run details shows start time, duration, build tool, CI server and
the CI build link (UI-029). The raw record is viewable as JSON there.

Edge cases: a record without modules is valid. It shows a run without artifacts, which usually means
the client published metadata without having uploaded anything.

### BLD-003 · Modules
Priority: **P1**

A module is a unit inside a run: typically one Maven or Gradle coordinate, one npm or NuGet package,
one container image, or a module name the client chose. One run can have several modules (for
example a backend and a frontend built in the same job).

| Module field | Meaning | Default |
|---|---|---|
| Module ID | Identifier of the module, for example a coordinate or a client-chosen name | unspecified |
| Module type | Package ecosystem of the module (values below) | unspecified |
| Module properties | Free key/value map | empty |
| Artifacts | Files the module produced (BLD-004) | empty |
| Dependencies | Files the module consumed (BLD-005) | empty |

Known module type values: generic, maven, gradle, ivy, docker, helm, npm, go, python
(also written pypi), uv, conan, terraform, nix, gem, apk, cargo, nuget, cpp, build, and
`huggingfaceml-model` / `huggingfaceml-dataset` for model-hub models and datasets. Clients set the
type themselves: generic for plain uploads and downloads, the tool's own type for wrapped package
commands (BLD-040). Helm charts kept in OCI repositories use the type helm, not an OCI or docker
type. A module that references another published run has the ID
`<referenced build name>/<referenced build number>` (BLD-019). Whether that reference module uses
the type "build" is unspecified.

UI location: the run details' published-modules tab lists module ID, artifact count, checksum
columns and, per module, the dependency list with scopes (UI-029).

Edge cases: when a client gives no module name, the default naming of modules is mostly
unspecified. The one specified default is the model-hub client's, which names the module
`<build name>-<build number>:<repository type>`. When two modules share an ID, the outcome is
unspecified.

### BLD-004 · Module artifacts and artifact linking
Priority: **P1**

Each module lists the files it produced.

| Artifact field | Meaning | Default |
|---|---|---|
| Type | Kind of file (for example the extension) | unspecified |
| Name | File name | unspecified |
| Path | Path of the file in its repository | unspecified |
| SHA-1, SHA-256, MD5 | Checksums of the file | unspecified |
| Original deployment repository | Key of the repository the file was first deployed to | unspecified |

Linking a listed artifact to a stored file:

- The record alone does not move or create files. The client deploys the files first, then
  publishes the record.
- For the system to link a listed artifact to the stored file, the stored item must carry the
  build-name and build-number item properties (ART-021 in [artifacts.md](artifacts.md), usually
  set at deploy time through ART-003). The SHA-1 and MD5 in the record must also match the stored
  file. Without these properties the run view shows the artifact with "no path found". Publication
  still succeeds.
- When the record carries the original repository and path, that location is used. Otherwise
  stored files are found by checksum plus the build properties. Copies that promotion made in
  other repositories also count as the run's artifacts. Both rules are stated for cleanup in RET-023
  ([retention-and-cleanup.md](retention-and-cleanup.md)).

Consumers of the link: build artifacts search (SRCH-019), downloading a build's artifacts as an
archive (ART-016), the client's "only artifacts of build `<name>/<number>`" filter (BLD-036),
promotion (BLD-020), deletion with artifacts (BLD-023), retention with artifacts (BLD-024, RET-037).

### BLD-005 · Module dependencies
Priority: **P1**

Each module lists the files it consumed.

| Dependency field | Meaning | Default |
|---|---|---|
| Dependency ID | Identifier, for example a coordinate or package name and version | unspecified |
| Type | Kind of file | unspecified |
| SHA-1, SHA-256, MD5 | Checksums of the consumed file | unspecified |
| Scopes | List of scopes in which the dependency is used, for example compile, runtime, test, provided | empty |
| Requested-by chains | List of chains (each a list of dependency IDs) that show through which parents the dependency was pulled in | empty |

Behavior:

- "Builds that use this file as a dependency" is answered by SHA-1 (SRCH-014 in
  [search-and-query.md](search-and-query.md)).
- Promotion can include dependencies, filtered by scope (BLD-020).
- The client can drop dependency scopes before publishing, for example test and provided (BLD-039).

Edge cases: when a client cannot determine a dependency's checksum, it leaves the dependency out of
the record by default. For npm, regular-dependency misses are logged as warnings, while peer and
bundled misses are logged only at debug level. The command fails only on request (npm, BLD-040).
How the server treats a dependency without checksums: unspecified.

### BLD-006 · Environment and system variables
Priority: **P1**

The record's property map carries environment variables of the CI process and system properties of
the build runtime (for Java builds, JVM system properties). The run view shows them in two groups,
"environment variables" and "system variables". How the record tells the two groups apart is
unspecified.

Collection happens in the client and is off by default in typical clients (the Maven plugin and the
companion client). An empty environment tab ("0 environment variables, 0 system variables") is
therefore normal and does not mean the publication failed.

| Client setting | Meaning | Default |
|---|---|---|
| Collect environment | Capture variables at publish time | off |
| Include patterns | Semicolon-separated wildcard patterns of variable names to keep | `*` |
| Exclude patterns | Case-insensitive, semicolon-separated patterns of names to drop | `*password*;*psw*;*secret*;*key*;*token*;*auth*` |

Security: this filtering runs in the client before publication. The record format accepts any
property map, and reading a run needs only read on the build (BLD-012), so every reader of the
build sees whatever the client sent. Server-side redaction is unspecified.

UI location: run details, environment tab (UI-029).

### BLD-007 · Version-control information
Priority: **P1**

The record carries a list of version-control entries.

| Field | Meaning | Default |
|---|---|---|
| Revision | Commit identifier | unspecified |
| Message | Commit message | unspecified |
| Branch | Branch name | unspecified |
| URL | Remote repository URL | unspecified |

Behavior: the client reads these values from the local Git checkout (BLD-039). It takes the
directory *containing* the Git metadata folder, and optionally a Git configuration file for unusual
layouts. If the folder is not found, the client logs a warning, leaves out version-control data and
still exits successfully.

Edge cases: when two collectors run in one job (for example an automatic post-step and an explicit
"add Git information" step), the record contains duplicate identical entries. The server does not
deduplicate them.

UI location: run details, VCS tab (UI-029).

### BLD-008 · Issue-tracker references
Priority: **P2**

The record can name the work items (issues) a run relates to.

| Field | Meaning | Default |
|---|---|---|
| Tracker name and version | Issue tracker type (free text, for example JIRA) and version | unspecified |
| Aggregate issues | Whether issues of earlier runs are included | unspecified |
| Aggregation status | When aggregating, how far back: up to the last run that carries this promotion status (for example released) | unspecified |
| Affected issues | List of issue key, URL, summary, and an "aggregated" flag that marks issues already seen in earlier runs | empty |

Client-side collection: the client reads the commit messages from the local Git log and matches each
against a configured regular expression with two capture groups (issue key and summary).

| Collection setting | Meaning | Default |
|---|---|---|
| Tracker name | Free text | unspecified |
| Pattern | Regular expression for commit messages, for example `(.+-[0-9]+)\s-\s(.+)` *(example)* | unspecified |
| Key group index | Capture group with the issue key | unspecified |
| Summary group index | Capture group with the summary | unspecified |
| Tracker URL | Base used to build issue links in the UI | unspecified |
| Aggregate | Include issues of earlier runs | unspecified |
| Aggregation status | Aggregate back to the last run with this status | unspecified |
| Connection profile | Server queried for earlier published runs | the client's default profile |

The client fetches details of earlier published runs of the build from the server (BLD-015,
BLD-016). The purpose is unspecified.

UI location: run details, issues tab (UI-029).

### BLD-009 · License-check request
Priority: **Later**

A record can carry a request for license checks after publication: run checks (on/off), include the
published artifacts in addition to dependencies, auto-discover licenses, a space-separated list of
dependency scopes to check, and a space-separated list of e-mail recipients for violations. The
behavior depends on a scanning subsystem
([security-scanning-and-curation.md](security-scanning-and-curation.md)). Defaults: unspecified.

## Storage and access control

### BLD-010 · Build metadata repository
Priority: **P1**

Records are stored in a dedicated internal repository, the build metadata repository. A default one
exists for the global scope. Project builds are kept in a build metadata repository associated with
the project (BLD-011). How project repositories are named and created is unspecified, and the append
operation can name the repository explicitly (BLD-018). The builds list shows the repository per
build as "build repository".

Behavior:

- Clients publish records only through the build operations (BLD-013). They do not deploy them as
  files, and the repository is not a package repository for JARs, images or other packages.
- Access goes through build permissions (IAM-025 in
  [identity-and-access.md](identity-and-access.md)). The repository cannot be selected in
  repository permission policies. A build grant gives access to the matching path in it. The pattern
  example `apache**/**` (every build whose name starts with `apache`) shows that the build name is
  the leading path segment. The rest of the internal layout is unspecified.
- Its content belongs to the system, not to users. If a generic cleanup, replication or permission
  change reaches it, publishers can be locked out, history can disappear, and (with artifact
  deletion) more files can go than intended. Path-based cleanup must leave it out (RET-037).
- In instance-to-instance transfer, at most 8 worker threads apply to build metadata repositories,
  regardless of the configured thread count (BLD-044).

UI location: builds list, "build repository" column (UI-028).

### BLD-011 · Project scoping of builds
Priority: **P2**

Every build belongs either to the global scope (default build metadata repository) or to a project
(BLD-010; projects:
[identity-and-access.md](identity-and-access.md); repository assignment REPO-013 in
[repositories.md](repositories.md)).

Behavior:

- The build operations take an optional project key. Without it, the global repository is used.
  The body form of delete (BLD-023) calls this the default project. Exceptions: the query form of
  delete has no project input, and rename requires the key for project builds (BLD-022).
- List operations filter by project. A project mismatch between publisher and viewer looks like a
  missing build.
- The project must exist before a record is published into it.
- Rename requires the project key when the build belongs to a project (BLD-022).
- The build picker of permission policies lists only global builds. Project builds are granted
  through project roles.

### BLD-012 · Privileges per build operation
Priority: **P1**

Build actions (read, annotate, deploy, delete, manage, manage scan metadata) are defined in IAM-025. They
apply per build name pattern, are independent of repository actions, and are needed in addition to
repository actions where files are touched.

| Operation | Required privilege |
|---|---|
| List builds (BLD-014), list runs (BLD-015) | Read on the build, or a general read privilege whose exact meaning is unspecified. Anonymous access works where anonymous holds it. |
| Retrieve a run (BLD-016) | Read on the build |
| Publish (BLD-013) | Deploy on the build; additionally delete when an existing record is replaced |
| Append modules (BLD-018) | Deploy and delete on the build |
| Aggregate by reference (BLD-019) | Read on each referenced build; deploy on the aggregating build to publish it |
| Promote (BLD-020) | Deploy on the build; read on the source content; deploy on the target repository. Whether a move also needs delete on the source: unspecified |
| Rename (BLD-022) | Deploy and delete on the build |
| Delete runs (BLD-023) | Delete on the build. Removing the artifacts as well is a broader scope; the repository privilege it needs is unspecified |
| Retention (BLD-024, RET-037) | Delete on the build |
| Promote a container image (BLD-025) | An authorized ("privileged") caller; exact action unspecified |

UI location: build permissions in the permission policy editor (IAM-025). Effective permissions per
build are shown in the run details (IAM-030, UI-029).

## Build operations

### BLD-013 · Publish a build run
Priority: **P1**

Stores a complete record.

| Input | Meaning | Default |
|---|---|---|
| Record | The full record (BLD-001 to BLD-009, BLD-024) as JSON | required |
| Project | Target project (BLD-011) | global scope |

Output: a success status; the response body is unspecified. Errors: malformed body (bad request),
missing or invalid credentials (unauthorized), missing deploy, or missing delete when an existing
record is replaced (forbidden). Privilege: BLD-012.

Behavior:

- The record is written to the build metadata repository of its scope (BLD-010).
- A "build uploaded" event fires (EVT-007). Before- and after-save hooks may run (EVT-046 in
  [events-and-extensibility.md](events-and-extensibility.md)).
- Retention criteria may travel with the record. When the server applies them is unspecified
  (BLD-024).
- Artifacts are linked as in BLD-004.

Typical order in a job: resolve dependencies, deploy artifacts with the build properties, then
publish the record (BLD-027).

Edge cases:

- Publishing without uploading anything gives a run without artifacts.
- Uploading artifacts without publishing gives no run.
- Combining a CI wrapper's automatic publication with an explicit one is reported to publish the
  run twice (BLD-030).

UI location: none; publication is API-only. The result appears in the builds list (UI-028).

### BLD-014 · List builds
Priority: **P1**

| Input | Meaning | Default |
|---|---|---|
| Project | Only builds of this project | global scope (whether all scopes are included is unspecified) |

Output: a link to the build collection and a list of builds. Each entry has a link relative to the
collection (`/<build name>`) and the start time of the latest run (ISO 8601; open: either the
pattern `yyyy-MM-dd'T'HH:mm:ss.SSSZ` or values ending in a literal `Z`, for example
`2024-01-15T10:30:00.000Z` *(example)*; the spec decides). Errors: unauthorized, forbidden.
Privilege: BLD-012.

Edge cases: paging and ordering are unspecified. Whether the API list leaves out builds the caller
cannot read is unspecified. The builds page shows only readable builds (UI-028).

UI location: builds list (UI-028).

### BLD-015 · List runs of a build
Priority: **P1**

| Input | Meaning | Default |
|---|---|---|
| Build name | The build | required |
| Project | Scope | global scope |

Output: a link to the build and a list of runs. Each entry has a link relative to the build
(`/<build number>`) and its start time. Errors: unauthorized, forbidden, not found (unknown build
name). Privilege: BLD-012.

Edge cases: how several records with the same number are listed is unspecified.

UI location: builds list, select a build name (UI-028).

### BLD-016 · Retrieve a build run
Priority: **P1**

| Input | Meaning | Default |
|---|---|---|
| Build name, build number | The run | required |
| Start time | Selects one record when several share name and number; format `yyyy-MM-dd'T'HH:mm:ss.SSSZ` | none |
| Compare with | Number of an older run to compare with (BLD-017) | none |
| Project | Scope | global scope |

Output: a link to the run and the full record. Errors: unauthorized, forbidden (no read), not found.
Privilege: BLD-012.

Edge cases: which record is returned when several share name and number and no start time is
given is unspecified.

UI location: run details, record view (UI-029).

### BLD-017 · Compare two runs
Priority: **P2**

Retrieving a run with "compare with" set to the number of an older run of the same build returns a
comparison of the two runs' contents. Which parts of the record are compared and the output format
are unspecified. The run details have a diff tab whose content is also unspecified (UI-029).

### BLD-018 · Append modules to a stored run
Priority: **P2**

Adds modules to an existing record in place.

| Input | Meaning | Default |
|---|---|---|
| Build name, build number | The parent run that receives the modules | required |
| Start time | Selects the parent record; ISO 8601 with a numeric zone offset and no `Z` suffix, for example `2024-01-01T00:00:00.000+0000` *(example)* | none |
| Build metadata repository | Key of the repository that holds the parent record | default for the scope |
| Project | Scope of the parent run | global scope |
| Modules | Array of modules (BLD-003) | required |

Output: success without content. Errors: bad request, unauthorized, forbidden (deploy and delete
missing), not found (parent record unknown). Privilege: BLD-012.

Edge cases: what happens when an appended module ID already exists in the record is unspecified.

UI location: unspecified.

### BLD-019 · Aggregate published runs by reference
Priority: **P2**

For pipelines whose steps run on several machines or at different times, each part is published as
its own run. A new aggregating run then references them.

Behavior:

- The client adds one reference module per referenced run to the not-yet-published aggregating run.
  The reference module's ID is `<referenced name>/<referenced number>`.
- The referenced runs must already be published. The client checks this against the server before
  adding the reference.
- Afterwards the aggregating run is published (BLD-013).
- Retrieving the aggregating run's artifacts (for example "download all artifacts of build
  `aggregating/10`") includes the artifacts of the referenced runs. Whether the server or the client
  resolves the references is unspecified.
- Separate jobs on ephemeral runners are not combined automatically. Aggregation is the way to
  combine them.

| Client option | Meaning | Default |
|---|---|---|
| Connection profile | Server to check references against | default profile |
| Project | Scope | global scope |
| CI build URL | Recorded in the aggregating run | none |
| Dry run | Preview without publishing | off |
| Environment include / exclude | As in BLD-006 | `*` / default exclusion list |
| Skip TLS verification | | off |

Privilege: read on each referenced build (BLD-012). This differs from BLD-018, which edits a
stored record in place.

### BLD-020 · Promote a build run
Priority: **P2**

Changes the status of a run and optionally copies or moves its artifacts (and dependencies) to a
target repository while setting properties on them. This is repository promotion of a run. Release
sets and lifecycle stages are a separate subsystem
([distribution-and-lifecycle.md](distribution-and-lifecycle.md)).

<details><summary>Promotion request</summary>

| Input | Meaning | Default |
|---|---|---|
| Build name, build number | The run | required |
| Project | Scope; without it the default build metadata repository is used | global scope |
| Status | New status of the run, for example released *(example)*. The key input for a status-only promotion | none |
| Comment | Reason for the promotion | none |
| CI user | User that triggered the promotion in the CI server | required |
| Timestamp | When the promotion command was received, format `yyyy-MM-dd'T'HH:mm:ss.SSSZ` | required |
| Dry run | Simulate without changing anything | off |
| Source repository | Only take artifacts from this repository | resolved automatically |
| Target repository | Destination; may be a virtual repository; may be omitted for a status-only promotion | none |
| Copy | On: copy. Off: move | off (move) |
| Artifacts | Include the run's artifacts | on |
| Dependencies | Include the run's dependencies | off |
| Scopes | Dependency scopes to include; scopes are combined with OR; only relevant when dependencies are included | all scopes |
| Properties | Key/value pairs set on the run's artifacts, whether or not a target is given | none |
| Fail fast | Abort on the first error | on |

The published request schema lists CI user, timestamp, copy, artifacts, dependencies and fail fast
as required, yet also gives defaults for the four booleans. A compatible server applies the
defaults when the booleans are missing; whether it rejects a request without CI user or timestamp
is unspecified.

</details>

Output: a list of messages, each with a level (error, warning, info) and a text. Errors:
unauthorized, forbidden (no deploy on the build), not found (unknown run). Privilege: BLD-012.

Behavior: status, comment, user and timestamp feed the run's promotion history (BLD-021). A "build
promoted" event fires (EVT-007).

Edge cases:

- With a virtual target, the member that receives the files is unspecified. The default deployment
  repository (REPO-038 in [repositories.md](repositories.md)) is the natural candidate.
- Moving an artifact that other runs also list: unspecified.
- With fail fast off, the operation continues past errors and reports them as messages.
- Typical refusal causes: source read or target deploy missing. A wrong copy/move choice or a
  missing source restriction affects more files than expected, so a dry run is the recommended
  first step.

The companion client exposes the same inputs (status, comment, source repository, include
dependencies, copy, properties as `key1=value1;key2=value2`, dry run, fail fast) with the same
defaults. It takes the target repository as a mandatory argument. By default it prints nothing;
on request it prints a JSON result (status code and message), and it has no table output.

UI location: unspecified; no UI action is defined.

### BLD-021 · Promotion status and history
Priority: **P2**

A promotion request carries a status, a comment, the CI user and a timestamp (BLD-020). Other
features depend on the system keeping these per run. How the history is stored is unspecified.

- Build artifacts search can pick "the latest run that carries status X" (SRCH-019 in
  [search-and-query.md](search-and-query.md)).
- Issue aggregation stops at the last run with a given status (BLD-008).
- The run details have a release-history tab whose exact content is unspecified (UI-029).

Statuses are free text (example: released). Allowed values and case sensitivity are unspecified.

### BLD-022 · Rename a build
Priority: **P2**

Renames a build, typically to follow a renamed CI job.

| Input | Meaning | Default |
|---|---|---|
| Current build name | | required |
| New build name | | required |
| Project | Required when the build belongs to a project | none |

Output: a text message saying the renaming has started. The operation runs asynchronously.
Errors: bad request, unauthorized, forbidden (deploy and delete missing), not found. Privilege:
BLD-012.

Edge cases: the operation addresses a build name, not a single run. How it treats individual runs
is unspecified. Whether the build-name properties on linked artifacts are updated is unspecified.
The outcome when the new name already exists is unspecified.

### BLD-023 · Delete build runs
Priority: **P1**

Removes records, optionally together with the artifacts they list. Two operation forms exist.

Form A, build name plus query parameters:

| Input | Meaning | Default |
|---|---|---|
| Build name | | required |
| Build numbers | Comma-separated list of numbers to delete | none |
| Delete artifacts | 0 or 1: also delete the runs' artifacts | 0 |
| Delete all | 0 or 1: delete every run of the build | 0 |

Output: a plain-text confirmation that lists the deleted runs as `<name>#<number>`. Errors: bad
request (no numbers given), unauthorized, forbidden (no delete on the build), not found. This form
takes no project input; how it reaches builds that belong to a project is unspecified.

Form B, request body, for numbers containing special characters:

| Input | Meaning | Default |
|---|---|---|
| Project | Scope | the default project |
| Build name | | required |
| Build numbers | Array of numbers; may be omitted when delete all is on | none |
| Delete artifacts | Also delete the runs' artifacts | off |
| Delete all | Delete every run of the build | off |

Output: plain-text confirmation. Errors: bad request, unauthorized, forbidden.

Behavior: a "build deleted" event fires (EVT-007). Deleting records never deletes artifacts
unless the flag is set. With the flag, the artifacts are found as in BLD-004. Whether deleted
records or artifacts pass through the trash can is unspecified (policy-based cleanup: RET-038).

Privilege: BLD-012. UI location: unspecified; no UI action is defined.

### BLD-024 · Retention criteria carried in a published run
Priority: **P1**

A published record can carry retention criteria for its build name. Build-tool plugins expose them as
retention settings of the build information.

| Criterion | Meaning | Default |
|---|---|---|
| Maximum count | Number of runs of this build name to keep | unspecified |
| Earliest build date | Runs older than this are discarded; a 64-bit integer instant (unit unspecified) | unspecified |
| Protected build numbers | Numbers never discarded | empty |
| Delete artifacts | Also delete the artifacts of discarded runs | unspecified |

Behavior: when the server applies criteria received with a record (at publication or later) is
unspecified. The standalone "set build retention" operation, the client's
"discard old runs" command and their asynchronous behavior are defined in RET-037. Scheduled
build cleanup policies are defined in RET-023
([retention-and-cleanup.md](retention-and-cleanup.md)).

Edge cases: the standalone operation takes the earliest date as an ISO 8601 date-time, while the
record uses an integer. An implementation must accept both forms in their own places. Deleting
artifacts with retention can affect other consumers of those artifacts.

### BLD-025 · Promote a container image
Priority: **P2**

Moves or copies a container image (or a Helm chart stored as an OCI artifact) from one repository to
another, independently of build records. The source may be a local or a federated repository.

| Input | Meaning | Default |
|---|---|---|
| Source repository | Key of the repository the image is in | required |
| Target repository | Destination repository | required |
| Image name | Image (repository path) to promote | required |
| Target image name | Name in the target | same as the image name |
| Tag | Tag to promote | none: all tags of the image are promoted |
| Target tag | Tag in the target | same as the tag |
| Copy | On: copy. Off: move | off (move) |

Output: plain-text success. Errors: bad request, unauthorized, forbidden, not found (unknown
repository or image). Privilege: BLD-012. The companion client offers the same operation.

Interactions: container format behavior on [formats/README.md](formats/README.md).

### BLD-026 · Promotion and staging extensions
Priority: **Later**

Server-side extensions can define named promotions and named staging strategies. A named promotion
runs for a given run (build name and number) with parameters. A staging strategy is queried for a
build name with parameters by a build server before a release build, and returns versioning,
version-control and promotion defaults. Details, parameter encoding and privileges are on EVT-067
([events-and-extensibility.md](events-and-extensibility.md)).

## CI integration

### BLD-027 · One build identity per CI job
Priority: **P2**

The contract all integrations follow. Within a single build name and number, a finished job has:

1. fetched its dependencies from the system (only where the job uses it as its package source);
2. stored its outputs in repositories whose format matches the outputs;
3. sent one record describing the run.

A job that stored files but sent no record, or sent a record but stored nothing, is incomplete.

Integrations come in three layers that can be combined:

- Build-tool plugins (BLD-033) collect modules, artifacts and dependencies inside the build.
- The companion client (BLD-035 to BLD-045) runs commands, including generic uploads and the
  publication step.
- CI wrappers (BLD-029 to BLD-032) install or expose the client and usually supply the credentials
  and the build identity (BLD-028).

Edge cases:

- Collected data accumulates in the client until publication. When the work is split over several
  jobs on throwaway runners, each runner keeps its own data and nothing merges it; aggregation
  (BLD-019) is the way to join them.
- Mixing collectors from two layers without a plan duplicates Git metadata (BLD-007) or loses
  environment data.
- A build is missing from the list when publication did not run, when name, number or project
  differ between steps, or when the viewer lacks read.

### BLD-028 · Build identity defaults derived from CI variables
Priority: **P2**

CI wrappers set the client's build name, number and URL from the CI system's own variables, so the
commands in a job need no explicit arguments. Explicit arguments win over these defaults.

| CI system | Build name | Build number | Build URL |
|---|---|---|---|
| GitLab CI (include template) | `$CI_PROJECT_PATH_SLUG-$CI_COMMIT_REF_NAME` | `$CI_PIPELINE_ID` | unspecified |
| GitHub Actions (setup action) | Workflow name | Run number | unspecified |
| Jenkins (plugin) | `JOB_NAME` | `BUILD_NUMBER` | `BUILD_URL` |
| Azure Pipelines (publish task, example) | `$(Build.DefinitionName)` | `$(Build.BuildNumber)` | unspecified |
| Bitbucket Pipelines (setup step) | `$BITBUCKET_REPO_OWNER-$BITBUCKET_REPO_SLUG-$BITBUCKET_BRANCH` | `$BITBUCKET_BUILD_NUMBER` | Pipelines result URL |
| Bamboo (task) | Plan name | Bamboo build number | Bamboo result URL |
| Maven build plugin (no CI value) | The project's artifactId | A build timestamp | unspecified |

Edge cases: Bamboo deployment projects have no plan metadata, so name and number must be passed
explicitly. A wrapper may also set a project key for build commands (BLD-011).

### BLD-029 · GitLab CI include template
Priority: **P2**

A set of ready-made GitLab CI jobs that a pipeline pulls in through `include:` (remote URL, a vendored
copy via `local`, or another GitLab project via `project` and `file`). There are variants for Linux
and macOS runners and for Windows runners.

Behavior:

- The include provides a hidden setup job and a hidden cleanup job. A job references the setup
  job's script (`!reference [<setup job>, script]`) before its first client command and the
  cleanup job's script in `after_script`. Setup and cleanup must be in the same job.
- Setup installs the companion client, configures the connection from CI/CD variables and exports
  the build identity (BLD-028).
- What cleanup does in detail is unspecified. Removing the stored connection profile at the end of
  a CI job is the recommended practice for the client (BLD-035).
- Package templates exist for .NET, Go, Gradle, Maven, npm, NuGet, pip, Pipenv and Yarn Berry. A
  "build" template configures repositories and runs the package command. The Maven example then
  publishes the record. An "audit" template prepares the dependency graph for a
  dependency audit by the scanning subsystem.

<details><summary>Template variables</summary>

| Setting | Meaning | Default |
|---|---|---|
| Base URL | URL of the repository manager | required |
| User | User for basic authentication; also needed for container registry login with a token | none |
| Password | Password for basic authentication | none |
| Access token | Access token | none |
| Container registry | Replaces GitLab's own registry variables with a registry of the repository manager | none |
| Client download repository | Key of a generic remote repository that proxies the client's release site, for runners without internet access | none |
| Project key | Project for build commands (BLD-011) | none |
| Client version | Client version to install, with a minimum supported version | unspecified |

</details>

Credential combinations: URL only (anonymous); URL, user and password; URL and access token. Store
credentials as masked and protected CI/CD variables. Token exchange from a GitLab ID token is
described in CIOIDC-025 ([oidc-ci-integration.md](oidc-ci-integration.md)).

Edge cases: "command not found" means setup did not run in this job. Authentication fails when
protected variables are unavailable on an unprotected branch. Download failures are fixed with the
client download repository.

### BLD-030 · GitHub Actions setup action
Priority: **P2**

A GitHub Action that installs and configures the companion client for the rest of the job.

<details><summary>Action inputs</summary>

| Input | Meaning | Default |
|---|---|---|
| Client version | Version to install; pin it for reproducibility | a pinned version of the action |
| Download repository | Remote repository that proxies the client's releases, for isolated networks | none |
| OIDC provider name | Must exactly match the provider configuration (CIOIDC-003) | none |
| OIDC audience | Audience requested for the GitHub ID token | the repository-owner URL |
| Disable job summary | Suppress the job summary (BLD-031) | off |
| Disable automatic publication | Suppress the post-step publication | off |
| Disable automatic evidence collection | Suppress evidence collection | off |
| Connection profile ID | ID of the client's connection profile | generated per run |
| Enable package aliases | Install command interception (BLD-042) | off |
| Alias tools | Comma-separated tools to alias | all supported tools |

</details>

Behavior:

- Base URL comes from a GitHub *variable* (not a secret, otherwise the job-summary links are masked).
  Credentials are an access token, a user and password, or OIDC.
- With OIDC the workflow needs `permissions: id-token: write`. The action performs the token
  exchange (CIOIDC-018) and, on success, exposes the issued token and the user name as step
  outputs. The token output is a secret.
- Package aliases (BLD-042) need a minimum client version. If installing them fails, the action
  only warns and later steps run the plain tools. A tool installed after the action can push the
  alias directory down in `PATH`; the alias directory then has to be put first again.
- Build name defaults to the workflow name and build number to the run number (BLD-028).
- A post-step publishes the pending record at the end of a successful workflow, including Git
  metadata. Publishing explicitly in the job disables the automatic publication for that build.
- Running an explicit "add Git information" step while relying on the post-step yields duplicate
  version-control entries (BLD-007).

Edge cases: a stored access token expires even though a developer's local client keeps refreshing
its own. Short-lived OIDC tokens avoid that.

### BLD-031 · CI job summary
Priority: **Later**

At the end of a successful CI run, a Markdown summary is added to the run page. It links to the
artifacts stored in the repository manager, the published record, and security findings. It is
built from the client's command summaries (BLD-043) and generated only for successful runs.

### BLD-047 · Package links between the CI platform and the package view
Priority: **Later**

For GitHub Actions, packages a workflow uploads show up in the CI platform's own artifact view with
a short summary. Each entry links to the package details in the system's UI (ART-049 in
[artifacts.md](artifacts.md)), and the package details link back to the CI run that produced the
version.

Preconditions:

- The CI platform's app for the system is installed (CIOIDC-026 in
  [oidc-ci-integration.md](oidc-ci-integration.md)) and the integration is switched on in the
  system.
- The workflow sets up the companion client with OIDC (BLD-030), uploads through a package-manager
  command (BLD-040) and publishes the run (BLD-013).
- Optional: the image is attested on the CI side, with a fully qualified image name (no `oci://`
  prefix, no tag) and the pushed digest as subject.

Unspecified: which operation delivers the summary to the CI platform, and where the link back to
the CI run comes from. The published run carries the CI build URL (BLD-002), which is the likely
source.

### BLD-032 · Other CI system integrations
Priority: **Later**

| CI system | Integration shape |
|---|---|
| Jenkins | Plugin that installs the client as a Jenkins tool (download from the release site, through a generic remote repository, or a manually installed binary) and adds a pipeline step that runs client commands. Supports several configured instances selected by profile ID, configuration as code with credentials from the Jenkins credential store, and a freestyle build step. A build action is attached to the Jenkins run when publication output is valid. |
| Azure DevOps | Extension with service-connection types for the platform, the repository manager, the scanning subsystem and distribution. OIDC can be enabled per connection with a provider name (CIOIDC-004). Task families: build and resolve per format, build lifecycle (collect issues, publish record, promote, discard), security and distribution, a tools installer for restricted agents (client and build-tool extractors through remote repositories), and a generic client task whose lines must start with the client command. |
| Bitbucket Pipelines | A pipe copies a setup script into the step. Sourcing it installs the client, imports exported connection profiles from secured variables, optionally configures OIDC (generic OIDC provider type, CIOIDC-002) from the step's token, and exports the build identity (BLD-028). It does not publish anything by itself, and the script deletes itself afterwards. It always downloads the client from the public release site and has no mirror setting, so fully isolated runners cannot use it. |
| Bamboo | Task that selects a configured connection and runs a client command against it. Build-plan tasks export plan name, build number and result URL. |
| TeamCity and others | Client in ordinary build steps. No current plugin. |

All wrappers require deploy and publish privileges, matching repositories and one build identity
(BLD-027).

### BLD-033 · Build-tool plugins for Maven and Gradle
Priority: **Later**

Plugins inside the build that deploy artifacts and publish the record themselves.

<details><summary>Maven plugin</summary>

- The plugin swaps in its own deployer during validate, Maven's first lifecycle phase. Maven's log
  therefore says it skipped deployment; that line is normal, and the plugin's upload lines follow
  it. Nothing is sent until every module of the reactor has finished, and then files and record go
  out together. The build must reach the deploy phase; stopping at install deploys nothing.

| Setting group | Setting | Meaning | Default |
|---|---|---|---|
| Publisher | Release repository / snapshot repository | Deployment targets | none |
| Publisher | Publish artifacts | Deploy artifacts | on |
| Publisher | Publish build record | Publish the record | on |
| Publisher | Filter excluded artifacts | Leave excluded artifacts out of the record | off |
| Publisher | Record all dependencies | Include implicit and build-time dependencies | off |
| Publisher | Minimum checksum-deploy size | Files from this size on are first offered by checksum (ART-004); 0 disables | 10 KB |
| Resolver | Release / snapshot repositories | Resolution sources | none |
| Build information | Name, number, URL, project, principal, retention (BLD-024) | Record identity | name: artifactId; number: timestamp |
| Environment | Collect environment, exclude patterns | BLD-006 | off |
| Deploy properties | Properties set on every deployed artifact | | none |
| Proxy | HTTP(S) proxy | | Maven's active proxy |

</details>

<details><summary>Gradle plugin</summary>

- Needs a publishing plugin (maven-publish or ivy-publish) and at least one publication. Each
  project gets a publish task, and a deploy task on the root project deploys artifacts and the
  record.

| Setting | Meaning | Default |
|---|---|---|
| Base URL | Repository manager URL | none |
| Repository / release repository / snapshot repository | Deployment targets | none |
| User, password | Credentials (password or access token) | none |
| Publish build record | Publish the record | on |
| Parallel deploy workers | Concurrent deployment threads | 3 |
| Publications | Which publications to deploy; an "all publications" keyword exists | known default publications |
| Skip | Skip a project | off |
| Publish artifacts / POM / Ivy descriptor | What to deploy | on |
| Module type | Module type in the record (BLD-003) | gradle |
| Build name, number, project | Record identity | unspecified |

- A CI driver can supply the settings through a properties file whose path comes from an
  environment variable.

</details>

### BLD-034 · Legacy CI plugins
Priority: **Out of scope**

Older CI-specific plugins (Jenkins, Bamboo, TeamCity, Visual Studio Team Services, an older Gradle
plugin generation, Ant/Ivy) remain only as migration references, and new work uses the current
wrappers. The migration path: run the new integration side by side, compare artifact paths, names,
numbers and published records, then retire the old job.

## Companion CLI

### BLD-035 · Connection profiles and CLI authentication
Priority: **P2**

The client keeps named connection profiles and uses one as the default.

<details><summary>Profile settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Profile ID | Local alias; `delete`, `use`, `show`, `clear` are reserved | required |
| Base URL | Must include the protocol; a trailing slash is optional | required |
| Access token | Token (JWT or reference token, TOK-004 in [access-tokens.md](access-tokens.md)) | none |
| User, password | Basic credentials | none |
| Client certificate and key | PEM files for mutual TLS | none |
| SSH key path | Key file | none |
| Encrypt password | Store a server-issued encrypted password instead of the clear one (IAM-035) | on |
| Basic authentication only | Send user and password directly instead of an auto-refreshed access token | off |
| Read password / token from standard input | Keep secrets off the command line | off |
| Disable token refresh | Do not refresh access tokens automatically | off (refresh on) |
| Skip TLS verification | Testing only | off |
| Interactive | Prompt for missing values | on |
| Overwrite | Replace a profile with the same ID | off |

</details>

Behavior:

- Profiles can be added, edited, shown, removed and set as default. A per-command flag or an
  environment variable overrides the default profile.
- A profile can be exported as a portable secret string and imported elsewhere. Importing an
  empty or malformed string can overwrite existing profiles.
- Stored secrets can be encrypted with a master key of exactly 32 characters, taken from a key file
  or an environment variable. Only secret fields (passwords, tokens) are encrypted; IDs and URLs
  stay readable. The key must be in place before the first profile is added: setting it on an
  existing unencrypted store makes the stored profiles unreadable, so they have to be added again.
  Losing the key later has the same effect. There is no command to turn encryption off.
- Authentication methods: access token; user and password (the client then either obtains an
  access token or stores an encrypted password); browser login for interactive
  SSO (not usable in CI); OIDC token exchange in CI (CIOIDC-025); one-shot URL and token flags
  without a stored profile.
- The client does not read URL or token environment variables on its own. CI wrappers read them and
  create the profile.
- Standard `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are honored. Setting `CI=true` disables
  prompts and progress bars.
- Servers whose TLS certificate comes from a private or self-signed CA: PEM CA certificates placed
  in a certificates folder inside the client's home directory are trusted in addition to the
  system store; with several files the folder needs OpenSSL hash links. Skipping TLS verification
  is the fallback, for testing only.
- Errors can be emitted as JSON (status code, status text, response body) on standard output for
  scripts. A generic "call any API path with the stored credentials" command exists.

### BLD-036 · Generic file operations
Priority: **P2**

Commands: upload, download, copy, move, delete, search, set properties, delete properties, and a
connectivity check (ping).

<details><summary>Behavior and options</summary>

| Option / behavior | Meaning | Default |
|---|---|---|
| Source pattern | Upload: local path with wildcards, a regular expression (regex mode) or an Ant pattern (Ant mode). Download and copy/move/delete/search: `repository/path` with wildcards | required |
| Target | `repository/path` or a local path. A trailing `/` appends the source name | download: current directory |
| Recursive | Include sub-folders | on |
| Flat | Ignore the source hierarchy | off |
| Target properties | `key1=value1;key2=value2` set on uploaded files (ART-023 in [artifacts.md](artifacts.md)) | none |
| Property filter / property exclusion | Only files with all / none of the given properties | none |
| Build filter | Only artifacts of run `<name>/<number>` (BLD-004). The variants "also its dependencies" and "only its dependencies" exist as spec-file fields (BLD-037), not as inline options | none |
| Release-set filter | Only files of a release set `<name>/<version>` | none |
| Debian coordinates | Upload only: distribution, component and architecture as `d/c/a` for `.deb` files | none |
| Include directories | Upload/download: take the directory path itself into account; search and property commands: also match folders | off |
| Repository only | Set and delete properties: act on the repository named in the first pattern segment without walking the full path pattern | off |
| Detailed summary | List every affected file in the command summary | off |
| Sort, order, limit, offset | Sort by item fields (for example created, modified, name, size) | order ascending |
| Threads | Parallel files | 3 |
| Retries / wait between retries | Per HTTP attempt within one run | 3 / 0s |
| Dry run | Show what would happen | off |
| Fail when nothing matched | Exit code 2 when no file is affected | off |
| Sync deletes | After upload, delete everything under a remote path that was not uploaded (or the local equivalent after download) | off |
| Archive | Pack the upload into a zip (target must be a file path). When exclusions remove every file, no archive is uploaded unless an "upload empty archive" switch is on | none; empty archive switch off |
| Explode | Extract zip, tar, tar.gz or tgz after upload or download | off |
| Symbolic links | Preserve links (Linux and macOS) | off |
| Multi-part upload | From 200 MiB in 5 parts of 20 MiB; needs object storage (ART-007); 0 parts disables | on |
| Multi-part download | From 5120 KB in 3 parts; 0 disables | on |
| Checksum upload | Before uploading, offer the file by checksum and skip the transfer when the content already exists (ART-004); from 10 KB | on |
| Download skip | Skip files whose local checksum matches | on |
| Skip checksum verification on download | | off |
| Transitive download | Through a virtual repository, also search its first five remote members | off |
| Delete confirmation | Prompt unless quiet; quiet is on when `CI` is set | prompt |
| Search output | Selectable fields (name, repository, path, checksums, size, created/modified and their users, properties); count only | all fields |
| Output format | JSON or table | search: JSON; other commands: backward-compatible text |

</details>

Edge cases:

- Download finds files through the server's index. A file in a remote repository that has never
  been cached is not found (0 downloads) even though a plain HTTP GET would fetch it.
- Retries do not resume an interrupted process. Re-running the command skips files that are already
  complete through the checksum logic.
- A pattern that matches nothing exits successfully unless "fail when nothing matched" is set.
- Symbolic links: without the switch, upload follows a link and stores the target file's content.
  With it, the link is stored as a zero-byte file carrying two item properties, the path the link
  points to and, when the target is a regular file, the SHA-1 of the target's content. Validation
  on download (spec field in BLD-037) checks that each target exists and still matches the recorded
  checksum; it applies to files, not directories. Both switches work on Linux and macOS and have no
  effect on Windows.

Server capabilities used: query-based search ([search-and-query.md](search-and-query.md)), deploy by
checksum, multi-part upload, item properties ([artifacts.md](artifacts.md)).

### BLD-037 · Spec files
Priority: **P2**

A JSON document with a list of file groups that replaces the inline arguments of upload, download,
copy, move, delete, search, set properties, delete properties, "add files as dependencies"
(BLD-039) and release-set creation. Inline options override the spec.

<details><summary>File-group settings</summary>

| Setting | Applies to | Meaning | Default |
|---|---|---|---|
| Pattern | all | Local path (upload, add dependencies) or `repository/path` (others); exclusive with a query | required unless a query is given |
| Query | server-side commands | A query-language query selecting items ([search-and-query.md](search-and-query.md)); exclusive with a pattern | none |
| Target | upload, download, copy, move | Destination; may use placeholders (BLD-038) | download: `./` |
| Target properties | upload, release-set creation | Properties to set | none |
| Property filter / exclusion | server-side commands | As in BLD-036 | none |
| Recursive | all | | on |
| Flat | upload, download, copy, move | | off |
| Exclusions | all | Patterns to leave out (with a pattern only) | none |
| Build filter | server-side commands | As in BLD-036 | none |
| Include dependencies / exclude artifacts | copy, move, download, search, set properties, delete | With a build filter: also match the run's dependencies / match only its dependencies | off / off |
| Release-set filter | server-side commands | | none |
| Sort fields, sort order, limit, offset | server-side commands | | order ascending |
| Regex, Ant | upload, add dependencies | Pattern syntax | off |
| Archive (zip), explode, symbolic links, include directories | upload | | none / off / off / off |
| Explode, skip archive inspection, validate symbolic links, include directories, transitive | download | | off |
| Validate symbolic links | copy, move | Check by SHA-1 that links point to existing, unchanged files | off |
| Path mapping | release-set creation | Regex input and output to place files at the destination | none |
| Signing key | download of release sets | Public key to validate downloaded release-set files | none |
| From server | add dependencies | Search the server instead of the local disk | off |
| Project | all | Project key | none |

</details>

Behavior: `${name}` placeholders in a spec are replaced from a semicolon-separated `name=value` list
given on the command line, so one spec serves several environments. A spec entry naming a
missing local file is handled like a pattern without hits: the command still exits successfully
unless "fail when nothing matched" is set. That check looks at the command's total, so one group
that transferred files is enough for success even when another group found nothing.

### BLD-038 · Path placeholders
Priority: **P2**

In upload, download, copy and move (inline or in specs), parts of the source pattern wrapped in
parentheses are captured and inserted into the target as `{1}`, `{2}`, and so on, numbered left to
right and usable in any order or not at all. In regex mode the regular expression's capture groups
are used instead.

Examples:

- `(*).tgz` → `repo/{1}/` puts each archive into a folder named after it.
- `(*).(*)` → `repo/{2}/{1}.{2}` sorts by extension. The wildcard is greedy, so only the last dot
  separates the groups.

A dry run prints the resolved targets without transferring anything.

### BLD-039 · Local build metadata collection and publication
Priority: **P2**

The client records build metadata locally and publishes it in one step.

Behavior:

- Every command that accepts a build name and number (both required together) records into a local
  temporary area keyed by name and number: upload records artifacts, download records dependencies,
  and package-manager commands record both (BLD-040). Repeated commands accumulate into one run
  until it is published. An optional module name splits a run into modules (BLD-003). An optional
  project key scopes it (BLD-011).
- Name, number, CI build URL and project can come from environment variables instead of arguments
  (BLD-028).
- Nothing reaches the server until publication. A forgotten publication step leaves the data only
  on the build machine.

| Client command | Effect | Notes |
|---|---|---|
| Add files as dependencies | Adds local files (or, from server, stored files) matching a pattern or spec as dependencies | Wildcards or regex (regex not with "from server"), exclusions, module, dry run, recursive on |
| Collect environment | Attaches environment variables (BLD-006) | Superseded by the publish-time option |
| Add Git information | Attaches revision, branch, remote URL and optionally issues (BLD-007, BLD-008) | Optional path to the directory containing the Git folder, an issue-collection configuration, a profile |
| Publish | Sends the record (BLD-013) | Options below |
| Append a published run | Adds a reference module (BLD-019) | Referenced run must exist on the server |
| Promote | BLD-020 | Output JSON only |
| Discard old runs | Retention by maximum days, maximum runs, protected numbers (comma-separated), delete artifacts (RET-037) | At least one criterion, otherwise the call succeeds and removes nothing. The client waits for completion unless its asynchronous switch is on (default off), unlike the server operation, whose asynchronous flag defaults to on |
| Clean | Deletes the locally collected data for a name and number | |

<details><summary>Publish options</summary>

| Option | Meaning | Default |
|---|---|---|
| Connection profile | Target server | default profile |
| Project | Scope | none |
| CI build URL | Recorded in the run; also from an environment variable | none |
| Collect environment | BLD-006 | off |
| Environment include / exclude | BLD-006 | `*` / `*password*;*psw*;*secret*;*key*;*token*;*auth*` |
| Excluded dependency scopes | Semicolon-separated scopes dropped from the record (formats with scopes, for example Maven and npm) | none |
| Collect Git information | BLD-007 | off |
| Git directory / Git configuration file | BLD-007 | search upwards from the working directory |
| Dry run | Print the full record that would be sent, without contacting the server | off |
| Overwrite | Replace every stored record with this name and number (artifacts are kept) | off |
| Detailed summary | Report details of the published record | off |
| Skip TLS verification | | off |
| Output format | JSON or table | backward-compatible text |

</details>

Typical CI sequence: build-tool commands with name and number, then publish with collect
environment and collect Git information. Separate collect and add-Git steps followed by publish are
the older, equivalent form.

### BLD-040 · Package-manager wrapping
Priority: **P2**

The client runs a package manager on the user's behalf. It routes resolution and deployment to
configured repositories and records dependencies and artifacts into the local run (BLD-039).

Behavior:

- A one-time per-project configuration step stores the resolution repository, the deployment
  repository and the connection profile for each tool. A global variant exists. The step does not
  check that the repositories exist or have the right format, so a typo only shows up at the first
  install or publish, as an HTTP 404, 401 or 403.
- Native arguments are passed through unchanged. For tools that need an explicit publish target
  (Gradle publications, Maven's deploy phase, npm publish), forgetting it yields a run with metadata
  but no artifacts.

| Tool | Dependencies recorded by | Artifacts recorded by | Notes |
|---|---|---|---|
| npm | install, ci | publish | Wrapped publish packs and uploads, so npm's publish lifecycle scripts do not run (rename them to the pack equivalents). Workspaces can be published together. |
| NuGet client | restore, install, update | push, pack (`.nupkg`, `.snupkg`) | Wrapped mode injects a temporary NuGet configuration file into each call unless the user passes their own source or configuration file. Restore is the main supported subcommand there; other subcommands work only if NuGet accepts a configuration-file option for them. The recording split shown is the one for native mode (BLD-041). A NuGet v2 protocol switch exists, and an insecure-connection switch for testing only. |
| .NET CLI | restore, build, add | pack, `nuget push` | test, run and publish record nothing. For test, restore is suppressed, so restore first with build flags. |
| Maven | build | install phase or later | Open: either the wrapped client deploys during install, or the deploy phase must run because install only writes to the local Maven repository; the spec decides. |
| Gradle | build | publish tasks | |
| Container client | pull, build and push collect metadata; image layers are recorded as dependencies | how pushed images appear as artifacts is unspecified | Registry login through the stored profile, run automatically before build, push and pull unless skipped. A Podman variant exists for pull and push. Images pushed by other tools are added with BLD-045 |
| Others with build metadata | Yarn, pnpm, pip, Pipenv, Poetry, Twine, uv, Go, Helm, Conan, Cargo, Terraform, Nix, RubyGems/Bundler, Alpine apk, the model-hub client (models and datasets), an agent-package manager | | APT records nothing; Debian packages get their coordinates through the generic upload (BLD-036) |

"Fail on uncollected dependencies" (npm install and ci): by default, dependencies whose integrity or
checksum cannot be determined are left out with a warning. The option fails the command instead, for
`all` or a comma-separated subset of `regular`, `peer`, `optional`, `bundle` (case-sensitive; `all`
cannot be combined; empty tokens are rejected). It requires build name and number.

### BLD-041 · Native execution mode
Priority: **P2**

Package-manager wrapping (BLD-040) has two modes.

| Aspect | Wrapped (default) | Native (opt-in) |
|---|---|---|
| Repository configuration | Injected by the client from its per-project configuration | The user's own tool configuration, typically from the client setup instructions (UI-021 in [ui-inventory.md](ui-inventory.md)) |
| Project and lock files | May be modified | Never modified |
| Per-tool configuration step | Required | Not needed |
| Build metadata | Collected | Collected for Maven, Gradle, npm, Poetry, Docker, Yarn 4 and later, NuGet, .NET, uv, Helm, Conan, Cargo, Nix, Ruby when run through the client with name and number |

Behavior:

- Native mode is switched on by an environment variable for the whole shell session (default off).
- Some tools always run natively: Helm, Conan, uv, Ruby, Cargo, Nix. Some only support wrapped mode:
  pip, Pipenv, Twine, Go, Terraform.
- When a tool's wrapped per-project configuration file exists (for NuGet and .NET also a global
  one), native mode is ignored for that tool and the command runs wrapped; for Maven this happens
  without a message. With Yarn before version 4, native mode logs a warning and falls back too.
- In native mode, Maven metadata is collected after the build from its output (multi-module
  supported). If the dependency tree cannot be read, only the root project is recorded.
- In native mode, NuGet and .NET accept per-command resolution and deployment repository options
  that add a source for that call only and leave the project's own NuGet configuration untouched.
  In wrapped mode the repositories come from the configuration step instead.
- Running the plain tool without the client records nothing.

### BLD-042 · Transparent command interception
Priority: **Later**

Shim executables for supported tools are installed into a directory that must come first on `PATH`.
When an interception environment variable is on (values: on, an audit mode that only logs, off), a
plain `npm install`, `mvn ...`, `docker push ...` and similar run through the client's wrapper
(BLD-040), including build name and number arguments.

Behavior:

- Supported tools: docker, mvn, gradle, npm, yarn, pnpm, pip, pipenv, poetry, dotnet, nuget, gem,
  bundle, go, apk.
- For docker, only login, build (including `buildx build`), push and pull are intercepted. Other
  subcommands pass through.
- The Maven wrapper script, apt and uv are not intercepted.
- Each tool has a mode (route through the client, or pass through). A status command shows the mode
  and whether alias and real binary exist.
- Installing or removing aliases takes a configuration lock with a timeout (default 5 s). If
  installing aliases fails in a CI action, the action only warns.

### BLD-043 · Command summaries
Priority: **Later**

When an output directory is configured, supported commands (upload, publish build record, container
push and pull, scans, evidence) write data files into per-category subdirectories. A "generate
summary" command aggregates them into Markdown per category plus an overall file. The files are
never removed automatically. Generating without any recorded data succeeds and produces nothing.
CI job summaries (BLD-031) are built from this output.

### BLD-044 · Instance-to-instance transfer
Priority: **Later**

The client can push all files of selected repositories from a source instance to a target instance,
and copy or merge configuration, for example for migration. The configuration copy takes include
and exclude repository lists, pre-checks, and a force switch that allows a non-empty target.

| Option | Meaning | Default |
|---|---|---|
| Include / exclude repositories | Semicolon-separated names, wildcards allowed | all |
| Include files | Path patterns | all |
| Ignore saved state | Restart from scratch instead of resuming | off |
| Proxy key | An outbound proxy configured on the source instance | none |
| Status / stop | Show or stop a running transfer | off |
| Filestore check | Check whether binaries already exist in the target's storage | off |
| Pre-checks | Validate connectivity, repository existence and compatibility, privileges, extension installation | off |
| Worker threads | Parallel transfers, changeable while running | 8; at most 8 for build metadata repositories |

Behavior and limits:

- It requires a server-side transfer extension on the source instance (installed on the primary
  node from the release site or local files).
- Target repositories must exist with the same name and type.
- The source pushes directly to the target, so it needs network access to it.
- Remote-repository caches are not transferred. Deletions on the source after the start are not
  propagated, and neither are property-only changes.
- Each repository goes through three passes: all files, then files created or changed after the
  first pass began, then files that failed before. Once the first pass is done for a repository,
  later runs only do the other two.
- The transfer resumes from saved state on the client machine. Failed files are listed in a CSV
  error summary and retried on the next run, and re-running picks up files created or modified
  meanwhile.
- Stop exits non-zero when nothing is running.
- Configuration copy is meant for a new, empty target. It takes over repositories, users, groups,
  permissions and the remaining configuration, and it always wipes the target's existing
  configuration and content first. The force switch only allows a non-empty target; it protects
  nothing and does not skip the confirmation prompt, so unattended runs have to answer it.
- Configuration merge is the variant for a target that already holds data. It adds repositories
  and projects from the source and never overwrites: a repository or project whose key already
  exists on the target is skipped. It takes include and exclude lists for both (semicolon-separated,
  wildcards allowed). Source and target must be different instances.

Interactions: replication and federation as continuous alternatives
([replication-and-federation.md](replication-and-federation.md)); import and export
([operations-and-monitoring.md](operations-and-monitoring.md)).

### BLD-045 · Attach externally pushed container images to a run
Priority: **P2**

Some image builders push to the registry themselves (in-cluster builders such as Kaniko, Docker
Buildx with `--push` for multi-platform images). The companion client can then add such an image,
already stored in a container repository, to the local run (BLD-039) after the fact.

| Input | Meaning | Default |
|---|---|---|
| Repository | Container repository that holds the pushed image | required |
| Image file | Path to a local file with the image reference `<image>:<tag>@sha256:<manifest digest>`; several tags of the same digest are given comma-separated | required |
| Build name, number, module, project | Run that receives the image (BLD-039) | none |

Behavior:

- The image is recorded in the run as a module of type docker (BLD-003). How the client resolves
  the reference against the repository is unspecified.
- A variant for OpenShift builds starts the cluster build itself (or reuses a finished one), waits
  for it, reads tag and digest from the result, and records the image the same way, so no image
  file is needed. Only the start-build operation is wrapped.
- Nothing reaches the server until the run is published (BLD-013).

Edge cases: a reference without a digest, or in another format, makes the command fail. Which
image files (manifest, layers) become artifacts and which become dependencies of the module:
unspecified.

Interactions: container format behavior on [formats/README.md](formats/README.md); image promotion
(BLD-025).

### BLD-046 · Configuration commands for repositories, replication and access
Priority: **P2**

The companion client wraps the server's configuration operations, so scripts and CI jobs can
create and remove repositories, replication jobs, users, groups and permission policies. Every
command takes a connection profile (BLD-035) or one-shot URL and credentials, and most need
administrator rights on the server.

<details><summary>Commands</summary>

| Command | What it does | Server operation |
|---|---|---|
| Repository template | Interactive wizard that writes a JSON document for one repository: purpose (create or update), key, class (local, remote, virtual, federated), format, then any optional settings of the repository configuration | none, local file |
| Create / update repository | Sends a template document | REPO-053, REPO-054 in [repositories.md](repositories.md) |
| Delete repositories | Deletes repositories with all content; the key may contain wildcards to hit several repositories | REPO-063 |
| Replication template | Wizard: job type (push or pull), source and target repository keys, target connection profile, cron expression, optional settings | none, local file |
| Create replication job | Sends a template. The same command serves updates; an existing job is replaced | REPL-011 in [replication-and-federation.md](replication-and-federation.md) |
| Delete replication jobs | Removes every replication job of one repository | REPL-011 |
| Create user | One user with name, password and e-mail; optional administrator flag, groups to join, replace switch | IAM-009 in [identity-and-access.md](identity-and-access.md) |
| Create users in bulk | Users from a CSV file whose header row names user name, password and e-mail; optional per-row columns for the administrator flag and groups; an option adds every new user to the same groups | IAM-009 |
| Delete users | By a comma-separated list of names, or from a CSV file with a user-name column (other columns are ignored) | IAM-009 |
| Create group / add users to group / delete group | Group by name; members as a comma-separated list of existing users | IAM-018 |
| Permission policy template | Wizard: policy name, then one or more sections (repositories, builds, release sets). The repository section takes repository keys or the any / any local / any remote wildcards (IAM-020), include patterns (default `**`) and exclude patterns (default none). Then, per user and per group, a set of actions from read, write, annotate, delete, manage, manage scan metadata and distribute (IAM-022, IAM-026) | none, local file |
| Create / update / delete permission policy | Sends a template, or deletes by name | IAM-031 |

</details>

Behavior:

- Templates may contain `${name}` placeholders. They are filled at apply time from a
  semicolon-separated `name=value` list, so one template can produce many entities. Substitution
  is single-valued. The permission policy wizard writes literal values, so placeholders are added
  by editing the file afterwards.
- The client creates one repository per call. Bulk creation is a shell loop over a reusable
  template; the server's batch operations (REPO-061) are not used.
- Creation is not idempotent: creating an existing repository or permission policy fails (the
  policy answers 409), and repeatable pipelines use update after the first run. Updating a
  permission policy requires it to exist and replaces its whole definition.
- Bulk user creation aborts as soon as it meets an existing user instead of skipping it and
  continuing with the rest. With the replace switch, password and e-mail of existing users are overwritten.
  Groups named for new users must exist beforehand. Creating an existing group fails unless the
  replace switch is set.
- Deletions prompt for confirmation unless quiet; quiet defaults to on when `CI` is set. Deleting
  a permission policy that does not exist exits with code 1 and the server's 404.
- The wizards need an interactive terminal and exit with an error without one. In CI the JSON is
  written by hand.
- In a permission policy template the repository list is one comma-separated string; a JSON list
  is rejected. The build section's repository value is ignored: the client always sends the build
  metadata repository (BLD-010).
- Output: JSON on request. Without it, create and update print nothing on success.

### BLD-048 · Extension plugins for the companion client
Priority: **Later**

The companion client can be extended with plugins: separately built executables that add
commands. Public plugins are listed in a public registry. An organization can run a private
registry in a local generic repository of the system (REPO-003 in
[repositories.md](repositories.md)).

| Setting (client side) | Meaning | Default |
|---|---|---|
| Plugin server | Connection profile (BLD-035) of the instance that holds the private registry | none: install uses the public registry; publish requires it |
| Plugin repository | Key of the generic local repository used as the registry; only valid together with the plugin server | a fixed default key |

Behavior:

- Install takes a name and an optional version (`name@version`). The version string is passed as
  published, for example `1.0.0` or `v1.0.0`; without one the latest version is installed. Each
  plugin is installed into its own folder (binary plus optional resources) under the client's home
  directory. Offline hosts get the copied folder. There is no list command.
- Installing over an installed plugin can destroy the installation; uninstall first. Uninstall
  removes the folder and asks for confirmation unless `CI=true`.
- Publish works only against a private registry. It compiles the plugin source (a Go toolchain is
  required) for Linux (amd64, arm64, arm, 386, s390x, ppc64, ppc64le), macOS (amd64, arm64) and
  Windows (amd64). It checks that the version is new and equals the version compiled into the
  plugin, uploads every binary, and copies the version to a "latest" location used by installs
  without a version. Versions follow semantic versioning.
- Server side, the registry needs nothing beyond a generic repository with read access for
  installers and deploy access for publishers. Copying to "latest" replaces existing files, so
  publishers presumably also need overwrite (inferred). The exact folder layout is unspecified.

Interactions: the federation engine migration tool (REPL-072) and the push-replication conversion
tool (REPL-050) are distributed as such plugins.

### BLD-049 · Instance statistics and shell conveniences
Priority: **Later**

- A statistics command prints an instance summary: number of projects, number and total size of
  distinct binaries, number and total size of artifacts, the storage backend type, repository
  counts per class (local, remote, virtual, federated), registered sites and release sets. Output
  is text (default), table or JSON. Full output needs administrator rights. For other users the
  site and project sections report an error, but the command still exits 0. Server data used:
  storage summary (STOR-039 in [storage-and-blobs.md](storage-and-blobs.md)), repository list
  (REPO-057), site registry (REPL-051).
- A command prints every environment variable the client reads, with its default and a
  description.
- Completion scripts for bash, zsh and fish come with package-manager installs and can also be
  generated on demand.

## Notes for implementers

- Name and number do not identify a run uniquely. The client's default-off overwrite option implies
  that publishing the same pair twice adds a record, and the start time tells the records apart.
  Retrieval and append accept the start time. Replacing requires the delete action (BLD-001,
  BLD-013).
- Timestamps are specified as `yyyy-MM-dd'T'HH:mm:ss.SSSZ`, which in the pattern language it comes
  from means a numeric offset such as `+0000`, and the append operation explicitly asks for that
  form. The list examples, however, end in a literal `Z`. Accept both on input; emit the numeric
  offset unless a client is known to need `Z` (unspecified). The earliest-build-date retention
  criterion is an integer inside a record but an ISO date-time in the standalone retention
  operation (BLD-024, RET-037).
- The list operations return entries as links relative to the collection (`/<name>`, `/<number>`).
  Clients concatenate them with the collection link.
- A run is linked to stored files through item properties (build name and number) plus matching
  checksums written at deploy time, or through a recorded original repository and path.
  Publication succeeds without either, but the run then shows its artifacts with "no path found"
  (BLD-004).
- Promotion moves by default (both the API and the client). It leaves dependencies out unless asked,
  applies properties even when no target is given, and fails fast unless told otherwise (BLD-020).
- Rename always completes in the background. The standalone retention operation does too by
  default. Callers must not assume the effect is visible when the call returns (BLD-022, RET-037).
- Deleting runs whose numbers contain commas needs the body form. The query form splits on commas
  (BLD-023).
- The build metadata repository cannot be chosen in repository permission policies, and build grants
  alone govern access to it. Generic cleanup or replication rules *can* include it, and then they
  break publishing or remove history, so an implementation has to exclude it or guard it
  (BLD-010).
- Environment data is filtered only by the client. Anything a client sends is stored and visible to
  every reader of the build, so treat records as potentially sensitive (BLD-006).
- Empty environment, VCS or issue tabs are normal outcomes of client defaults and not errors.
  Duplicate VCS entries come from two collectors and are stored as sent (BLD-006, BLD-007).
