# Distribution and release lifecycle

This page covers how a repository manager turns a selection of stored artifacts into an immutable,
signed, versioned **release set**; how a release set (and, one level up, an **application
version**) moves through ordered **lifecycle stages** guarded by policy gates; how it is shipped to
remote read-only **edge instances** or carried across an air gap as an archive; and how signed
**evidence** (attestations) is attached to artifacts, packages, builds, release sets and
application versions. There are two generations of release sets: a *legacy*
generation that a separate distribution service assembles from queries and signs on request, and
a *current* generation that the repository manager creates itself, signs at creation and can
promote through stages. Both are described; where behavior differs, the entry says so. Unless an
entry says otherwise, "release set" means the current generation.

## Release sets

### DIST-001 · Release set model
Priority: **Later**

A release set is the bill of materials of one software release: the files and packages that make
up the release plus their metadata, frozen as one unit so the release stays coherent.

- **Identity.** A release set has a name and one or more versions. Name and version are unique per
  storage repository (DIST-002); because there is one storage repository per project, they are
  unique per project. A release set belongs either to the global scope or to exactly one project.
  A release set created from a build that belongs to a project is associated with that project,
  even when the UI is in the all-projects context.
- **Content.** Every version contains at least one artifact. Content may only come from local and
  federated repositories; build dependencies may additionally come from a remote repository's
  cache (they must already be cached). Two candidate artifacts with the same path and name from
  different repositories (a *path collision*) cannot both be included (see DIST-005).
- **Immutability.** Once a version is created (or a draft finalized, DIST-006), no file can be
  added, changed or removed. Later changes to properties of the source artifacts are not reflected
  in the version. Any content change requires a new version.
- **Manifest.** Each version has a JSON specification stored in the storage repository under a
  path built from name and version. It is signed and stored as a DSSE envelope whose base64
  payload is an in-toto statement; every included file appears as a subject with its URI and
  SHA-256 digest (see DIST-012).
- **Snapshot.** The system keeps its own copy of every included artifact (and its metadata as of
  creation) in the storage repository. Deleting or changing a source artifact afterwards does not
  affect the release set; promotion rollback (DIST-021) restores from this snapshot.
- **Container images.** When a source contains a container image manifest or a manifest list,
  the system resolves it and includes the manifest(s) and all layers. Resolution can be switched
  off per request, separately for Docker-style and OCI manifests.
- **Origin.** A version is either created locally or *received* (by distribution, DIST-034, or
  by archive import, DIST-043).
- **Audit.** Creation, promotion, distribution, export and deletion are recorded on the version
  timeline (DIST-010) and produce signed internal evidence (DIST-051).

<details>
<summary>Identity settings</summary>

| Setting | Meaning | Default / limit |
|---|---|---|
| Name | Release set name. Starts with a letter, digit or underscore; then letters, digits, underscores, periods, hyphens. | Max 255 characters through the API; the UI limits input to 128. |
| Version | Version string. Same rules as the name, plus `+`. | Max 255 through the API; UI limit 32. |
| Project | Project scope; selects the storage repository. | Global scope when omitted. |
| Tag | Optional free label (DIST-008). | Max 128 characters. |
| Signing key | Key pair used to sign the manifest (DIST-004). | Default key chain, see DIST-004. |

</details>

**Legacy generation.** A legacy release set is created and signed by the separate distribution
service (DIST-038). Its artifacts must all come from one source instance; queries run with the
requesting user's privileges. Names start with an alphanumeric character followed by
alphanumerics or `-`, `_`, `:` (max 255); versions max 255. A version may hold up to about 3,000
artifacts — a recommendation, not an enforced limit. Legacy release sets have no project support
and are listed separately from current ones; the two generations are not interchangeable. Once a
file is part of a distributed legacy release set it cannot be deleted on the target, although it
may be removed on the source.

**Interactions.** Sources are builds (see [builds-and-ci.md](builds-and-ci.md)), query-language
results (see [search-and-query.md](search-and-query.md)), artifacts and package versions (see
[artifacts.md](artifacts.md)). Federation replicates release sets (DIST-016).

### DIST-002 · Release set storage repositories
Priority: **Later**

Release sets live in dedicated storage repositories that users cannot write to directly.

- **Current generation.** A read-only system repository with its own package type holds the
  manifests and the artifact snapshots. It is created automatically when the first release set in
  a scope is created: one global repository, plus one per project whose key carries the project
  key as prefix. A storage-repository key whose prefix does not match the project is rejected.
  Every release-set operation addresses the storage repository either by project key or by
  repository key; with neither, the global storage repository is used. The storage repository is
  not recreated automatically (restore fails if it was removed, DIST-013).
- **Legacy generation.** A default storage repository exists out of the box. Administrators can
  create further ones through the normal create-repository dialog by choosing the release-set
  repository class; they carry only the common settings (key, public description, internal notes,
  include/exclude patterns). Content is laid out as `<name>/<version>/<original path>`, cannot be
  edited or removed, and survives deletion of the source artifacts. Signing a legacy release set
  copies its content into the storage repository. The UI only creates legacy release sets in the
  default storage repository; the API can name another.

**UI.** Visible in the repository tree; evidence of a release set is shown when the release set
is selected in the tree (DIST-052).

**Interactions.** Permission policies select release sets through their storage repository
(DIST-014). Repository classes in general: see [repositories.md](repositories.md).

### DIST-003 · Create a release set version from sources
Priority: **Later**

Creates a new version from one or more sources, signs its manifest and stores the snapshot.

<details>
<summary>Source types</summary>

| Source type | Inputs | Notes |
|---|---|---|
| Builds | Build name; build number; optional build start time (latest run of that number when omitted); optional build metadata repository (system default when omitted; BLD-010 in [builds-and-ci.md](builds-and-ci.md)); include dependencies (default off). | Dependencies must be in local or federated repositories, or already cached in a remote repository. |
| Release set versions | Name and version, plus project key or storage-repository key. | Combines several release sets into one (for example, one per microservice into one product release). |
| Query | A query-language expression selecting artifacts. | API and CLI only. |
| Artifacts | List of repository paths, each with optional SHA-256. | API and CLI only. |
| Package versions | Package type, name, version, repository. | API and CLI only. |

</details>

One request may combine several sources of different types; each source may switch off container
manifest resolution individually. An application version cannot be a source (validation error).

<details>
<summary>Request options</summary>

| Setting | Meaning | Default |
|---|---|---|
| Asynchronous | Return as soon as the request is accepted ("submitted") instead of after creation ("created"). | Asynchronous |
| Validation mode | Fail on first error, or collect validation errors (DIST-005). | Fail on first error |
| Conflict resolution | How to pick between candidates with equal checksum and relative path in different repositories (DIST-005). | Automatic |
| Dry run | Validate without persisting (DIST-005). | Off |
| Draft | Create a mutable draft (DIST-006). | Off |
| Filters | Include/exclude filters (DIST-007). | None |
| Skip container manifest resolution | See DIST-001. | Off |
| Project / storage repository | Target scope (DIST-002). | Global |
| Tag | DIST-008. | None |
| Signing key name | DIST-004. | Default key chain |

</details>

**Operation.** Create release set version — inputs: the above; output: storage repository key,
name, version, creation timestamp, tag (omitted when empty); success is "submitted" for
asynchronous and "created" for synchronous calls. Privilege: read on all source paths, plus read
and create on the release set. An existing name+version yields a conflict error.

**UI.** Release lifecycle area: actions "create version from builds" and "create version from
release sets" (wizard: details tab with name, version, optional signing key; then selection of
builds — with an *include build dependencies* checkbox — or of existing release set versions).
From the builds table: create from one or more selected builds, or from a single build after
reviewing its modules. Creating from a build that already produced a release set pre-fills the
name and signing key; a *create new name* option starts a new release set instead (not offered
for release sets created from several builds or from other release sets). A new version of an
existing release set is created from its page with a new version number. The result appears on
the stage board as *unassigned*, on the timeline, and in the builds table's release-set column.

**Limits and edge cases.** A creation that stays in the started state longer than the creation
timeout (default 360 minutes) is marked failed. An expired signing key fails the request.

**Interactions.** Creation start, completion and failure are webhook events (see
[events-and-extensibility.md](events-and-extensibility.md)). Creation produces internal evidence
(DIST-051); a security scan may follow (DIST-023).

### DIST-004 · Release set signing keys
Priority: **Later**

Every version is signed when it is created; promotion may re-sign with a different key.

- **Key selection.** The request names a key pair. Without one, the per-project default key name
  applies, then the global default key name, then a key the system generates automatically.
- **Key types.** GPG key pairs (for example RSA 2048) managed in the key management area of the
  administration UI; a passphrase is optional. Release-set signing (creation and promotion) is one
  of the GPG use cases.
- **Validity.** An expired key fails creation, promotion and distribution.
- **Changing keys.** A different key can be chosen when creating a new version of an existing
  release set or when promoting a version (for example after a key compromise or a change of
  ownership); the key pair itself can also be updated through the key-pair API. Distribution
  always uses the key that signed the version at creation.
- **Propagation.** The public key must be propagated to edge instances before distribution and
  to the source's own trusted-public-key list before an air-gap export (DIST-026).

| Setting | Meaning | Default |
|---|---|---|
| Global default signing key name | Key used when a request names none. | None |
| Per-project default signing key names | Overrides the global default for a project. | None |

**Legacy generation.** The distribution service holds key pairs (private and public); the source
instance and every edge hold the public key. Keys must be at least 2,048 bits. Several key pairs
can be managed, each with a mandatory alias (an alias derived from a timestamp is generated when
missing) and one default; a sign request may name an alias, otherwise the default is used. When
the key has a passphrase, signing prompts for it.

<details>
<summary>Legacy key operations (distribution service)</summary>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Set signing key pair | Private and public key, optional passphrase | — | unspecified |
| Get default public signing key | — | Public key | unspecified |
| Upload and propagate key pairs | Key pair(s), alias, default flag, propagate-to-edges flag (propagates once) | unspecified | unspecified |
| Propagate public keys to edges | Key alias or all keys | unspecified | unspecified |
| List key aliases | — | Aliases | unspecified |
| Get public key by alias | Alias | Public key | unspecified |
| Rename alias | Alias, new alias | — | unspecified |
| Delete key by alias | Alias | — | unspecified |
| Get public signing key by name | Key name | Key content | Administrator |

</details>

**Interactions.** Key management UI and other signing uses (repository metadata signing): see
[identity-and-access.md](identity-and-access.md). In a federation every member needs the keys
(DIST-016). Evidence uses separate key pairs (DIST-046).

### DIST-005 · Creation validation, conflict resolution and dry run
Priority: **Later**

- **Validation mode.** By default creation stops at the first validation error. In collect mode
  the system gathers validation errors and returns them together after the failed attempt — at
  most 50 by default (configurable). Representative grouped errors:

  | Status | Cause |
  |---|---|
  | 400 | Source artifact not found; invalid source (checksum mismatch, prohibited package type, several paths for the same package, several binaries on the same path); source repository is neither local nor federated; application version used as a source |
  | 403 | No read permission on a release set, build or artifact path |
  | 404 | Repository path not found |
  | 422 | Build artifact cannot be resolved |

  Never grouped, whatever the mode: 403 for missing permissions or an expired signing key, and
  409 when the name and version already exist.
- **Conflict resolution.** When candidates with the same checksum and relative path exist in
  different repositories: *automatic* (default) lets the system choose; *manual* leaves the choice
  to the caller, who resolves it with filters (DIST-007). For application versions automatic
  picks the newest by creation date; for release sets the rule is unspecified. Creation also fails
  when the same artifact exists at the same location in several repositories; how this interacts
  with automatic resolution is unspecified.
- **Dry run.** Runs the full server-side validation without persisting anything; success returns
  200 instead of 201. Available on creation and on draft update.

### DIST-006 · Draft release set versions
Priority: **Later**

A draft is an unlocked version that a pipeline can fill over time and lock at the end.

- Created with the *draft* option on DIST-003.
- **Update draft** — inputs: additional sources (any source type), include/exclude filters,
  asynchronous, validation mode, dry run, conflict resolution, scope, signing key; output: storage
  repository, name, version, still-draft flag, update timestamp, number of artifacts added;
  privilege: read and create on the release set, read on the sources. Only drafts can be updated.
- **Finalize draft** — locks the version and makes it immutable; idempotent (succeeds on an
  already final version).
- Promoting a draft finalizes and locks it first, then promotes.

**Limits.** No evidence can be attached to a draft. Concurrent updates of the same draft are
serialized. Evidence and properties added to artifacts after they were added to the draft, and
changes to a build after it was added, do not reach the final version. Drafts are not scanned;
scanning starts after finalization.

**Legacy generation.** A legacy release set is a draft until signed: the UI offers *create*
(editable draft) and *create and sign* (final). Only unsigned versions can be edited. *Clone
version* copies the specification (name and queries) into a new creation form.

### DIST-007 · Derive a version with include and exclude filters
Priority: **Later**

Creates a new version from an existing version (or other sources) by stating only the changes —
for example a new version identical to the previous one except that PNG files are dropped, or
restricted to certain package types. Filters apply on creation (DIST-003) and draft update
(DIST-006).

| Filter field | Meaning | Limit |
|---|---|---|
| Package type | e.g. docker, npm, maven, generic | 32 characters |
| Package name | Package name | 255 |
| Package version | Package version | 255 |
| Path | Artifact path pattern (e.g. `**/*.png`) | 1,350 |
| SHA-256 | Exact checksum | 64 |

Each filter list (*included*, *excluded*) holds any number of filter objects. How several fields
in one filter object combine, and how *included* and *excluded* interact when both match, is
unspecified.

### DIST-008 · Tags on release set versions
Priority: **Later**

A version can carry one short text tag (for example "release-candidate"). The tag shows on the
version card of the stage board, in the version details sidebar and in lists of received
versions. The stage board can be filtered to the versions with a given tag, and the
list-versions operation (DIST-010) accepts a tag filter with a trailing wildcard and several
comma-separated values.

- **Rules.** At most 128 characters; letters, digits, hyphen, underscore and period only (no
  spaces). The UI and the companion CLI additionally require the first and last character to be a
  letter or digit.
- **Operations.** *Assign tag* replaces the current tag; assigning an empty or null tag clears
  it. *Remove tag* deletes it. Both need the annotate action on the release set (DIST-014).
- The tag can also be set at creation (DIST-003), from the details sidebar, and with the
  companion CLI's annotate command, which sets tag and properties in one call.

### DIST-009 · Properties on release set versions
Priority: **Later**

Key/value properties and property sets can be added to a version (UI: the version's properties
tab); they are placed on the version's manifest file. Received versions show the
properties they arrived with.

Through the companion CLI's annotate command: property keys up to 255 characters, values up to
2,400 characters; setting an existing key replaces its values; a key can be removed with all its
values, or its values emptied while the key stays; a recursive option (default on) decides
whether content in sub-folders is affected as well.

Related property behavior:

- Promotion can add properties to every promoted artifact (DIST-020); a configurable list of
  property keys is stripped from artifacts during promotion (default: none).
- Legacy generation: properties already on the source artifacts are taken into the release set;
  additional properties can be supplied at creation and are transferred to the edges; a list of
  *additional properties* is attached to every artifact during distribution.

**Interactions.** Properties and property sets in general: see [artifacts.md](artifacts.md).

### DIST-010 · Release set lists, version details and timeline
Priority: **Later**

**UI (release lifecycle area).** A list of release sets with name, project (if any), origin
(created on this instance or received through distribution), number of versions and latest
version. Per release set a *versions* tab (version, current lifecycle status, tag, time of the
most recent activity such as a promotion), a *stage board* (one column per lifecycle stage plus
*unassigned*, DIST-020) and a *distributions* tab (DIST-029). Per version:

- **Timeline** — every event in reverse chronological order, the first always being creation;
  further events for promotion, distribution (one entry per target), export, deletion, internal
  evidence (visible to users with evidence access) and external evidence. Each event has a status:
  in progress, failed, success, deleting, deleted, canceled (distribution or export). Removed
  promotions and deleted exports stay visible but crossed out. A version selector switches between
  versions. The *actions* menu offers promote, roll back, distribute, delete from target, export,
  delete version, download all evidence.
- **Content** — a table of the release items (packages and standalone artifacts) with name,
  version, the source they came from (build or release set) and whether they carry evidence;
  plus a content graph showing builds, packages and related evidence (DIST-052).
- **Spec** — opened from the creation event: the version's JSON definition; offered as a
  download when it is too large to display.
- **Evidence**, **properties** and a **scan** tab.
- **Sidebar** — version details, tag (editable) and *instances*: the stages where the version is
  currently present (after a copy promotion both source and target stage).

<details>
<summary>Query operations</summary>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List release set names | Scope | Names mapped to storage repository (and project) | Read; unreadable release sets are omitted |
| List release set groups | Scope | Name, storage repository, latest version, number of versions | Read on each |
| List versions of a release set | Name; filters (version, creator, tag, status, stage reached, release status); ordering; page size (default 25, max 255); optional creation messages and the caller's permissions | Versions in every status, including failed creation attempts | Read |
| Get latest version | Name | Manifest descriptor; scan checksum when available | Read |
| Get version details | Name, version | Creation time, creator, artifacts; stored as a DSSE envelope | Read |
| Get version status | Name, version | Status | Read |
| Get version signature | Name, version | DSSE envelope (DIST-012) | Read |

</details>

| Setting | Meaning | Default |
|---|---|---|
| UI hints | Suggest previously used signing key names and build names in creation forms. | On |
| Name list limit | Maximum release set names shown in the UI. | 1,000 |
| Stage board limit | Maximum versions shown on the stage board. | 200 |

**Legacy generation.** A *distributable* tab (create, distribute, track) and a *received* tab on
the source; edges show only received. Search by name or latest version; totals for both tabs. The
version page shows general information (version, creation date, status, delivery summary, size)
and tabs: *actions tracking* (per distribution action: ID — repeated once per target —, target,
action, start, finish, duration, status, artifact progress in percent with number of attempts,
details), *content* (artifacts, builds, metadata; link to the source path; a per-artifact scan
status — an artifact blocked by the scanner prevents distribution), *release notes*, *scan data*,
*spec* (source instance and queries, with the generated query on request) and *effective
permissions*. The source's release-set list shows name, latest version, and the last three
distribution jobs with ID, start, status and progress.

### DIST-011 · Find release set versions containing an artifact
Priority: **Later**

Operation *list versions by artifact* — inputs: release set name filter, artifact path, SHA-256,
pagination; output: matching versions; privilege: read on the release sets. A companion operation
lists promotions that contain a given artifact (DIST-022). For legacy release sets on edges, the
query language offers domains for release sets and their artifacts (see
[search-and-query.md](search-and-query.md)); a platform search also finds distributable and
received legacy release sets within a time window.

### DIST-012 · Independent verification of a release set version
Priority: **Later**

Anyone holding the version manifest can check it without trusting the transport (for example on a
deployment server):

1. Fetch the signature: a DSSE envelope with base64 payload, payload type
   `application/vnd.in-toto+json` and a signature list (key ID, signature).
2. Fetch the public key from the key-pair API using the key ID.
3. Build the DSSE pre-authentication encoding
   `DSSEv1 <len(type)> <type> <len(payload)> <payload>` and verify the signature (GPG, or RSA with
   SHA-256).
4. Decode the payload and compare each subject's SHA-256 with the digest of the downloaded file.

**Interactions.** The same envelope format is used for evidence (DIST-045).

### DIST-013 · Delete and restore release set versions
Priority: **Later**

- **Delete version** — synchronous or asynchronous; privilege: delete on the release set. The
  version leaves the storage repository and goes to the trash can by default; all its promotions
  are removed and the promoted copies are deleted from the stage repositories; all its evidence is
  deleted. Edges are not touched (use DIST-032). UI: version timeline, *actions → delete version*,
  with confirmation.
- **List deleted versions** — versions in the trash can that can still be restored (read on each;
  unreadable ones omitted).
- **Restore version** — an undo, not a re-creation: the same artifacts return under the same
  manifest with the original creation timestamp; evidence is restored too. The version returns as
  *unassigned*; promotion history is lost permanently. Privilege: create on the release set.
  Errors: storage repository missing (400, not recreated), nothing in the trash for these
  coordinates (404), a live version already exists (409). Restoring through the generic trash-can
  UI does not work for release sets; only this operation does.

**Legacy generation.** Delete removes the version from selected edges and, optionally, from the
distribution service itself; a separate operation deletes all versions of a release set. Empty
folders left behind in target and storage repositories are removed (default on).

**Interactions.** Automatic deletion by cleanup policies: see
[retention-and-cleanup.md](retention-and-cleanup.md) — such policies never delete distributed
release sets and never delete the source builds and artifacts, but do delete evidence. Trash can:
see [artifacts.md](artifacts.md). In a federation a delete is replicated (DIST-016).

### DIST-014 · Release set permissions
Priority: **Later**

Permission policies can target release sets as a resource kind (both generations).

- **Selection.** By storage repository, narrowed by include/exclude patterns (wildcards `*`, `**`,
  `?`; each pattern at most 1,024 characters), globally or per storage repository. Patterns also
  match release sets that do not exist yet. An *any release set* option covers all existing and
  future ones; selecting a release set by name covers all its versions.

| Action | Grants |
|---|---|
| Read | View and download release-set content and see release sets in the UI. Needed on every instance where the release set is viewed, not only the source. |
| Annotate | Add properties and metadata to release-set content; assign and remove tags (DIST-008). |
| Create | Create release sets (read + create on a name pattern allows creating release sets matching it). |
| Delete | Delete release sets. |
| Distribute | Distribute release sets (together with destination permissions, DIST-035). |
| Promote | Promote and roll back (named by the promotion operations). |
| Manage scan data | Trigger scans; manage custom issues and licenses. |
| Manage | Change other principals' rights within this policy, without adding or removing resources. |

**Interactions.** The general permission model: see
[identity-and-access.md](identity-and-access.md).

### DIST-015 · Release set background settings and metrics
Priority: **Later**

Server-side settings for current release sets:

| Setting | Meaning | Default |
|---|---|---|
| Stale record cleanup schedule | When stale creation and promotion records and action messages are purged. | Weekly, Sunday 00:00 |
| Stale record age | Age after which such a record may be purged. | 2,880 minutes (2 days) |
| Creation timeout | Started creation that exceeds it is marked failed. | 360 minutes |
| Promotion timeout | Processing promotion that exceeds it is marked failed. | 360 minutes |
| Metrics collection interval | Interval for release-set statistics exposed as metrics. | 1 hour |
| Scan hand-over retry schedule | Retry for versions not yet delivered to the scanner for indexing. | Every 6 hours (random offset) |
| Scan hand-over retry window | How long such delivery is retried. | 4,320 minutes (72 hours) |
| Promotion hand-over retry schedule | Retry for promotions not yet delivered to the scanner. | Every 30 minutes (random offset) |
| Promotion hand-over retry window | How long such delivery is retried. | 4,320 minutes |

Release-set performance metrics are exposed in OpenMetrics format for scraping by monitoring tools;
the individual metric names are unspecified here (see
[operations-and-monitoring.md](operations-and-monitoring.md)).

### DIST-016 · Release sets across federated sites
Priority: **Later**

Release sets can be used where federated repositories join several sites into one logical system
(disaster recovery, active/active).

**Prerequisites on every member:** signing keys; the projects of project-scoped release sets
(converting a project-scoped local repository to federated fails if the project is missing on a
member); stages and their repository assignments; distribution targets (the same edge may be
configured on several members); project roles and destination permissions.

**Behavior.**
- A version can be created on any member and is replicated to all members.
- Promotion and distribution can be run on any member but are not replicated; the other members
  only get a timeline entry when the operation has finished.
- If several members promote the same version independently, update events for artifacts with an
  equal checksum are skipped on the receiving side to avoid blocking the queue.
- If members share a distribution target and one has already distributed a version there, a
  second member's attempt reports that the version already exists.
- A delete on any member is replicated. Deleting from a shared target affects every member using
  that target.
- Evidence public keys must be uploaded on each member; evidence verifies only where the key
  exists (DIST-046).

**Limits.** Before forming a federation, the repositories must not contain release sets with the
same name and version on different members; otherwise behavior is unpredictable and artifacts in
those release sets may be overwritten. Non-clashing release sets federate normally.

**Interactions.** Federated repositories: see
[replication-and-federation.md](replication-and-federation.md).

## Lifecycle stages and promotion

### DIST-017 · Lifecycle stages
Priority: **Later**

A stage is a named step of the software development lifecycle (for example DEV, QA, STAGING) that
a release candidate passes before release. Each stage has its own repositories (DIST-018) and
permissions. Stages replace the older *environments* (a repository grouping with the default
environments DEV and PROD that roles can be bound to); environments remain supported, and
promotion requests still accept an environment name as a deprecated synonym of the target stage.

- **Scope.** *Global* stages can be used by every project; *project* stages only by their
  project. A default release stage named PROD exists.
- **Name.** Case-insensitive, unique within its scope (project or whole system).
- **Category.** *Promote* (part of the promotion path) or *none*; responses also report a *code*
  category for system stages before promotion (pull request, commit). The category can only be
  changed while the stage is not used in a lifecycle; repository assignment applies to promote
  stages only.

<details>
<summary>Stage operations</summary>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Create stage | Name; project key (absent = global); category (default promote); repository keys | Name, scope (global/project), project key, category, repositories, projects whose lifecycle uses it, creator, creation and modification time | unspecified; duplicate → 409 |
| List stages | Filters: project key, scope, category | Stages | Without project key: global stages, platform administrators only. With project key: global plus that project's stages, project administrators |
| Get stage | Name, project key | Stage including scope and repositories | unspecified |
| Update stage | Name, project key; new name, category, repositories | Stage | unspecified |
| Delete stage | Name, project key | — | unspecified; rejected while a lifecycle uses the stage |

</details>

**UI.** Administration, *stages and lifecycle* page: available stages in the upper half (create
global stage / create project stage, depending on the project selector), the lifecycle in the
lower half (DIST-019).

**Interactions.** Environments as a role-scoping mechanism: see
[identity-and-access.md](identity-and-access.md). Cleanup policies can exclude release sets
promoted to given stages (see [retention-and-cleanup.md](retention-and-cleanup.md)). Webhooks for
promotion events are filtered by target stage (see
[events-and-extensibility.md](events-and-extensibility.md)).

### DIST-018 · Assign repositories to stages
Priority: **Later**

A repository can belong to one or more stages; promotion copies or moves content into the
repositories of the target stage. Assignment is part of the repository's basic settings (local,
remote and virtual repositories) as a multi-select of stages, and is also possible through the
stage operations (DIST-017), the repository create/update API and a batch repository update.
Repositories the user may not edit are not selectable. Within a project, a stage (environment) is
mandatory when creating a repository; the default is DEV.

### DIST-019 · Lifecycle definition
Priority: **Later**

The lifecycle is the ordered list of stages a version passes. There is one global lifecycle, and
each project can define its own.

- The lifecycle consists of system stages before promotion (code category: pull request, commit),
  the user-chosen promote stages in order, and the fixed release stage (PROD) which is always last
  and cannot be moved or modified.
- **Edit.** Attach and detach global and project stages (detaching asks for confirmation) and
  reorder them by drag and drop.
- **Operations.** *Get lifecycle* (global, or for a project): stages grouped by category, each
  with name and scope. *Update lifecycle*: replaces the ordered list of promote stages of a
  project; system stages must not be included. The operation can answer 422; the cause is
  unspecified.

**UI.** Lower half of the stages and lifecycle page; *edit* → *attach/detach stages*.

### DIST-020 · Promote a release set version
Priority: **Later**

Promotion advances a version one step in its lifecycle by placing its content in the repositories
of the target stage, and attaches signed promotion evidence (who, when, where).

<details>
<summary>Promotion settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Target stage | Stage to promote into. | required |
| Operation | *Copy* (content stays in the source stage) or *move* (content is removed from the repositories of the last promotion). | Copy |
| Included repositories | Restrict the target repositories; when empty all target-stage repositories except excluded ones are used. | All |
| Excluded repositories | Target repositories to skip. | None |
| Included / excluded source paths | Restrict which content is promoted. | All |
| Overwrite strategy | What to do when the promotion would overwrite a file in the target that is not part of the release set: *disabled* (fail), *all* (overwrite), *latest* (overwrite only artifacts tagged `latest`). | Disabled |
| Additional properties | Key/values added to every promoted artifact. | None |
| Signing key | Optional re-signing key (DIST-004). | Current key |
| Authorization type | Whether the promotion is authorized by application governance (DIST-057). | Undefined |
| Asynchronous | Return on submission. | Asynchronous |

</details>

**Operation.** Promote version — inputs: name, version, scope, the settings above; output:
storage repository, name, version, target stage, included/excluded repositories, added
properties, creation timestamp (ISO 8601 and milliseconds), source stage; "submitted" (async) or
"promoted" (sync). Privilege: read and promote on the release set; deploy (or cache) on the target
repository paths.

**Rules and edge cases.**
- Target repositories may be local, federated, or virtual if the virtual repository contains at
  least one local repository in that stage. At least one target repository per package type in
  the version is required.
- Promotion from *unassigned* and promotion into the stage the version already occupies are always
  copies, even when move was requested.
- If an artifact already exists in the target with a different checksum, the promotion stops with
  a conflict error. A second promotion requested while one is still running for the same version
  is also rejected as a conflict.
- A draft is finalized before promotion (DIST-006).
- A promotion in processing state longer than the promotion timeout (default 360 minutes) is
  marked failed. While a security scan is pending, the promotion stays in processing; a blocking
  scan policy fails it (DIST-023).

**UI.** Stage board of the release set: drag a card to a stage, or *actions → promote* on the
timeline. Dialog step 1: optional signing key change, copy/move, target stage; step 2: target
repositories per package type (deselect to exclude). The card moves, shows an icon for the
promotion type, and the timeline records the event.

**Interactions.** Promotion start/completion/failure webhooks filtered by stage (see
[events-and-extensibility.md](events-and-extensibility.md)). Comparable build promotion: see
[builds-and-ci.md](builds-and-ci.md).

### DIST-021 · Promotion rollback
Priority: **Later**

Undoes the latest promotion of a version and returns it to its previous stage.

- The promotion record and its evidence are deleted; the audit trail keeps it — the timeline shows
  the promotion crossed out plus a rollback entry.
- **After a copy promotion:** the artifacts are deleted from the target stage's repositories; the
  previous stage still holds them with unchanged properties and evidence.
- **After a move promotion:** the artifacts are deleted from the current stage's repositories, then
  copied with their evidence and properties from the storage-repository snapshot (state at
  creation) into the previous stage's repositories.
- Only the last promotion is reversed, even if the version was promoted several times into the
  same stage.

| Setting | Meaning | Default |
|---|---|---|
| Current stage | Stage the rollback starts from. | required |
| Overwrite strategy | For files in the previous stage that are not part of the release set: *skip*, *all*, *latest*. | Skip |
| Asynchronous | Return on submission. | Asynchronous |

**Operation.** Roll back promotion — output: storage repository, name, version, stage rolled back
from and to. Privilege: read and promote on the release set, deploy on target paths. UI: version
timeline, *actions → roll back version*, with confirmation.

**Edge cases.** A rollback that overlaps a still-running promotion of that version has undefined
results; the promotion has to complete first.

### DIST-022 · Promotion records and queries
Priority: **Later**

Every promotion attempt, including failed ones, is recorded.

| Operation | Inputs | Output |
|---|---|---|
| List versions in a stage | Stage | Versions currently promoted there |
| List promotions of a version | Name, version | All promotion records |
| List promotions of a release set | Name | All promotion records across versions |
| Get promotion details | Name, version, promotion | Specification with a snapshot of the promoted items |
| Get promotion status | Name, version, promotion | Status |
| List promotions containing an artifact | Artifact | Matching promotions |

A promotion is addressed by its creation timestamp in milliseconds. Each record carries its
status (started, submitted, pending, completed, failed, rejected, deleting), source and target
stage, the instance that ran it, the initiating user, creation time, the hand-over status to the
scanner and any error messages. The list operations page, sort (by time, user, version or stage;
newest first by default) and filter by version, target stage and user. Privilege: read on the
release set.

### DIST-023 · Security scan gate for release sets
Priority: **Later**

When a scanning engine is present, release sets can be indexed and scanned like other resources.
A scan policy with a blocking action can stop promotion (current generation) and distribution or
export (both generations).

- Scanning starts once the version is an indexed resource; drafts are scanned only after
  finalization.
- Promotion and distribution fetch the scan status; the operation stays in processing until the
  scan completes. If indexing takes longer than a configured timeout, the operation fails.
- A block requires: scanner available, release set indexed, and a scan scope that binds the
  release set to a policy with a blocking action. An optional scanner setting lets operations proceed unscanned when
  the scanner is enabled but unreachable.
- A scan of a new version produces two pieces of evidence: an SBOM of the content and a
  vulnerability list, CycloneDX by default, SPDX optionally (DIST-051).
- Results appear on the version's scan tab; blocked legacy distributions show as failed in
  actions tracking.

**Interactions.** Everything about the scanner itself: see
[security-scanning-and-curation.md](security-scanning-and-curation.md).

## Distribution to edge instances

### DIST-024 · Distribution topology and edge instances
Priority: **Later**

Distribution ships a signed release set from a **source** instance to one or more **targets**.

- **Edge instance.** A reduced, read-only deployment profile of the repository manager whose
  purpose is to serve released content close to its consumers (data center, point of sale, device
  fleet). All content on an edge arrives as release sets. Edges offer public or authenticated
  download, role-based access control, download restrictions and an audit trail. Third parties
  get read-only access to an edge.
- **Other targets.** A full repository-manager instance can also be a distribution target; its
  received versions are listed together with its own release sets (DIST-034).
- **Distribution service.** A separate service (installed on its own server) coordinates
  distribution: it obtains the list of registered targets from the platform's topology registry,
  checks target availability before every distribution, sends each target the release-set
  information (file checksums and the public key), triggers the transfer of the files from the
  source, and closes the transaction. The target then verifies the signature with the public key,
  verifies every file checksum, and places the files at their target paths (by default the same
  repositories as on the source).
- **Registration.** An edge has to be installed and registered in the source side's topology
  registry before it is offered as a target. When an edge is renamed later, the new name appears
  in distribution views, while timeline events recorded earlier keep the old name.
- **Consumption.** Clients download distributed content from the edge with their native package
  clients (Docker, npm, …) or through the companion CLI by release set name and version. Downloads
  from a target keep the original repository paths; downloading a legacy release set from the
  source yields `<name>/<version>/<repository>/<path>`.

**Edge cases.** "No distribution targets available" means: no edge installed, edge not
registered, no permission to see targets, or no connectivity. An offline target cannot receive a
distribution (critical error).

**Interactions.** What an edge instance can and cannot do, and how one is set up: DIST-068.
Installation methods: [ha-and-deployment.md](ha-and-deployment.md).

### DIST-025 · Trust between source and edge instances
Priority: **Later**

Source and targets must trust each other before distribution. Two methods exist:

- **Pairing token (default).** An administrator generates a short-lived, single-purpose pairing
  token on one side (lifetime 300 seconds by default) and hands it to the other; the pairing
  exchanges it for a long-lived service-to-service access token granting what the use case needs.
  Revoking that token revokes the trust.
- **Mutual root-certificate trust (legacy).** Each side installs the other side's root
  certificate as trusted.

Hybrid set-ups (a cloud-hosted source distributing to self-managed edges) need inbound
connectivity from the source to the edge, typically through a DMZ or VPN.

**Interactions.** Pairing tokens: see [access-tokens.md](access-tokens.md).

### DIST-026 · Signing key propagation to targets
Priority: **Later**

Targets verify release sets with the signer's public key, which therefore has to be present on
every target.

- **Propagate public signing key** — input: key name; effect: adds the public key of the
  release-set signing key to the trusted public keys of all connected edges, and to the source's
  own trusted list (required before an air-gap export, DIST-042); output: a per-target
  propagation report (overall status, and per edge its name, ID, key alias and status).
  Privilege: administrator. Not available in the UI.
- Trusted public keys can also be added manually per instance (key management, *public keys*:
  alias plus key text, or the trusted-key API).
- Edges added later need a separate propagation.

**Edge cases.** With the current transfer mode, do not propagate keys with identical content under
different names to the same target.

### DIST-027 · Distribute a release set version
Priority: **Later**

<details>
<summary>Distribution settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Distribution rules | List of target filters; each rule has a site-name wildcard, a city-name wildcard and a list of country codes. A target matching any rule is included. | Site `*`, city `*`, countries `[*]` |
| Auto-create missing repositories | Create target repositories that do not exist (DIST-028). | Off |
| Path mappings | Regex input/output pairs that relocate content on the target (DIST-028). | None |
| Default mapping from last promotion | Use the repositories of the most recent promotion as the mapping (DIST-028). | Off (system setting; per-request value overrides) |
| Distribute attached evidence | Ship all evidence of the version and its content (current transfer mode). | unspecified (UI checkbox) |
| Priority | Queue priority, base and per rule (DIST-031). | Medium |

</details>

**Operation.** Distribute version — inputs: name, version, scope, settings; output: distribution
ID and the selected targets (name, service ID, type). Privilege: distribute on the release set and
distribute on each destination (DIST-035). The distribution service's own distribute operations
(for both generations; legacy release sets must be signed first) also offer a *dry run* that
simulates without distributing.

**UI.** Version timeline, *actions → distribute*: step 1 selects targets (a grayed, checked target
already holds this version — to redistribute it there, delete it from that target first); a
troubleshooting link appears when no targets are listed. Step 2 *additional settings*:
auto-create missing repositories, distribute attached evidence, per-package target path changes
(current mode) or regex mappings with templates (legacy mode). The timeline gets one entry per
target. Legacy release sets: *distributable* list, distribute icon, target list filtered by the
user's permissions (search, select all), auto-create option.

**Edge cases.**
- Partial success: targets that received the version serve it even when others failed.
- Redistribution of failed targets: individually or in a batch (legacy UI, with a failed-only
  filter).
- Older versions can be distributed at any time.
- The current transfer mode requires the source and all targets to run a version that supports
  it; mixing upgraded and older targets in one distribution fails. Versions distributed before the
  mode was switched on (or off again) need a new version before they can be redistributed.
- With a scanner, a blocking policy stops distribution (DIST-023).
- The companion CLI can wait synchronously for completion (default maximum 60 minutes) and
  offers a client-side dry run.

**Interactions.** Distribution start/complete/abort/failure and per-target receipt are webhook
events (see [events-and-extensibility.md](events-and-extensibility.md)). Each distribution
creates internal evidence (DIST-051).

### DIST-028 · Target path mapping and repository auto-creation
Priority: **Later**

**Path mappings** relocate content on the target. Each mapping has an *input* regular expression
matched against the source path and an *output* template that may use capture groups (`$1`).
Templates offered by the UI: *change repository* (all files of one source repository go to
another target repository), *change folder* (all files go to one target folder), *rename folder*
(files of one source folder go to a differently named target folder).

| Rule | Legacy transfer mode | Current transfer mode |
|---|---|---|
| When a mapping can be set | Only at the first distribution or export of a version | Once per distribution operation |
| Different mappings per target | No; all targets share one mapping | Yes, through separate distribution operations |
| Changing the mapping for a target later | Never | Not possible; delete the version from that target first |
| Redistribution to a target that already has the version | — | API: allowed if the mapping is unchanged. UI: always delete first. |
| Applies to air-gap exports | Yes | No (mapping is an import-side option, DIST-043) |

*Default mapping from last promotion*: when on and the version has a successful promotion, that
promotion's target repositories are proposed as the mapping (the user may keep, replace or clear
it); otherwise target repositories equal source repositories.

**Auto-creation of missing repositories** (off by default; without it a missing target repository
fails the distribution):

| Situation | Without mapping | With mapping |
|---|---|---|
| One source repository | Created with the same name and type as the source | One-to-one mapping: created with the source's type |
| Several source repositories | Each created with the same name and type | All of one type mapped to one target: that type, with the mapped name. Mixed types mapped to one target: a generic repository. |

Advanced mappings (beyond these patterns) disable auto-creation unless a system setting allows
it. Repositories auto-created before an abort stay in place (DIST-030).

### DIST-029 · Distribution tracking and monitoring
Priority: **Later**

Every distribution and remote deletion is tracked per target.

- **Distributions tab of a release set** — a matrix of versions (rows; with the number of
  attempted targets) by targets (columns); failed attempts in red with the error on hover; default
  time range the last 7 days; search and filters by target and status. Clicking an entry opens the
  distribution details: content (artifacts), evidence, and the path mapping used.
- **Monitoring page (administration)** — filter by target; *active* tab: release set name,
  version, project, action (distribute or delete), target (one row per target), duration, status
  (storing release set, in queue, pending scan results, in progress, finalizing distribution, not
  distributed, abort in progress), size (artifact count and total size); *history* tab (default
  last 24 hours): name, version, action, target, start, duration, final status (completed, failed,
  aborted, partially aborted; failure reason on hover). Row details: initiating user and the
  artifacts (name, type, target location, size, SHA-256).

<details>
<summary>Tracking operations</summary>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| List distributions of a version | Name, version | Distributions with status and priority | Distribute |
| Get distribution details | Name, version, distribution | Details per target | Distribute |
| List distribution trackers (service) | Name, version | Trackers with overall status | unspecified |
| Get tracker status | Tracker ID | Progress per target | unspecified |
| Legacy: distribution statuses | All / by name / by version / by tracker | Status per target | unspecified |
| Legacy: list edge targets | — | Registered targets | unspecified |

</details>

### DIST-030 · Abort a distribution
Priority: **Later**

Stops an in-progress distribution. Targets that already hold the full version keep it; only the
others are affected; repositories already auto-created are not removed. Output: accepted (202) or nothing
to abort (204). Privilege: distribute. UI: open the in-progress timeline entry, stop icon, confirm
*stop distribution*; the entry shows *abort in progress* and finally *canceled*. The distribution
service aborts by signalling every target still receiving.

### DIST-031 · Distribution priority queue
Priority: **Later**

Distribution and remote-deletion tasks are processed first-in, first-out within three priority
levels: low, medium (default), high.

- A request sets a base priority for all its targets and/or per-target rules (by site name or
  country codes) with their own priority. A rule overrides the base; when several rules match a
  target, the last one wins.
- **Update priority of a running distribution** (administrator) — inputs: release set, version,
  tracker, base priority, rules; output per target: previous and new priority and a result —
  *applied* (target still created or queued; all its artifact tasks use the new priority),
  *recorded* (target already in progress or later; stored and used only on retry), *skipped*
  (target already final).
- The current priority is shown in the distribution list (DIST-029).

Privilege to set priority on submission: distribute or remote-delete permission.

### DIST-032 · Remote deletion from targets
Priority: **Later**

Removes a distributed version from one or more targets without deleting it on the source.

- **Operation** — inputs: name, version, distribution rules (same filter shape as DIST-027),
  priority; the distribution service variants also have a dry run, and the legacy one an option
  to delete the release set from the distribution service afterwards. Output: accepted, with the
  operation ID and the selected targets. Privilege: delete on the release set plus the destination
  permission for the targets (DIST-035).
- On the target, artifacts are removed from the target repositories unless another release set
  still uses them.
- **UI.** Version timeline *actions → delete from target* with a multi-select of targets (one
  timeline event per target), or the trash icon in the details of a single distribution.
- Current release sets are managed by the repository manager, so the distribution service cannot
  delete them on the source.

**Interactions.** Deletion events per target are webhooks (see
[events-and-extensibility.md](events-and-extensibility.md)). Local deletions on a target are
reported back (DIST-033).

### DIST-033 · Reporting of deletions made on targets
Priority: **Later**

Targets cannot open connections to the source, so the distribution service periodically collects
records of versions deleted locally on targets and hands them to the source, which adds them as
timeline events.

| Setting | Meaning | Default |
|---|---|---|
| Batch size | Records fetched per request. | 200 |
| Collection interval | Time between collection runs. | 900 seconds |
| Handled-record cleanup schedule | When the source tells targets to drop handled deletion records. | Daily 02:00 |
| Leftover record retention | When remaining records are dropped anyway. | 720 hours (30 days) |

Versions distributed before both sides supported this feature are never reported.

### DIST-034 · Received release sets on targets
Priority: **Later**

On a target, received versions are listed and managed separately from the source.

- **Lists.** Release sets received (name, number of received versions, latest version), then
  versions (version, tag, received date); search by name or wildcard; sort by name, latest version
  or creation date.
- **Version page.** Timeline (actions on this target), content (release items), evidence (if evidence
  was distributed with it — otherwise a pointer to the source), properties.
- **Actions.** *Download all evidence* as one JSON file (internal and external evidence);
  *delete received version* (removes its artifacts and evidence).
- On a full instance acting as target, received versions appear in the same list as local ones.
- Viewing received release sets on an edge requires administrator rights (legacy view).

<details>
<summary>Received-version operations</summary>

| Operation | Inputs | Output |
|---|---|---|
| List received versions | Filters and ordering by name, source project or date received | Per release set: name, latest received version, source project, number of received versions, date of the latest receipt; total count |
| List received versions of a name | Name | Versions |
| List artifacts of a received version | Name, version | Artifacts |
| Get received version status | Name, version | Status and messages |
| Delete received version | Name, version | — (also used by remote deletion and cleanup) |

</details>

### DIST-035 · Destination permissions
Priority: **Later**

Distribution targets (destinations) are a permission resource of their own, offered once at least
one release set exists.

- **Selection.** By target name, or by rules: instance-name wildcard pattern, country codes, city
  wildcard pattern (each pattern up to 1,024 characters); any combination; *include all
  destinations* covers existing and future targets; a preview lists matching targets.
- **Actions.** *Distribute* (distribute to the selected targets), *delete* (remove release sets
  from them), *manage* (change other principals' rights in this policy).
- The distribution service also keeps destination permissions of its own: list, get by name,
  create or update by name (destinations required), delete — administrator only.

**Interactions.** See DIST-014 and [identity-and-access.md](identity-and-access.md).

### DIST-036 · Distribution task maintenance
Priority: **Later**

- **List stuck distributions** — distributions in a non-final state.
- **Maintenance actions** — *unlock stuck* (release all stuck tasks so they can be retried or
  cleaned up) and *stop all* running tasks. Execution is two-step: generate an encrypted execution
  token (valid for 3 minutes), then execute the action with that token.
- **Automatic recovery** of stuck tasks (configurable).
- **Redistribute recent release sets** — redistributes every release set distributed within a
  window (a date and a number of days back) to a given target; used to rebuild a target.

Privilege: administrator, for every operation above.

### DIST-037 · Distribution service operations
Priority: **Later**

| Operation | Behavior |
|---|---|
| Ping | Healthy when the service and the services it depends on (the repository manager, the identity and token service) are reachable; may allow unauthenticated calls depending on configuration, otherwise 401. |
| System info | Version, service ID, health stable/unstable (with a message when dependencies are down). |
| Get / update system settings | Administrator only; the setting catalog is unspecified. |
| Metrics | OpenMetrics text format; needs the permission to read system metrics. Catalog below. |
| Create or refresh token | Form-encoded; client-credentials grant creates, refresh-token grant refreshes. |
| Token info / revoke token | Administrator; revocation by token value or ID following RFC 7009. |

<details>
<summary>Distribution service metric families (descriptive; names are the implementer's choice)</summary>

| Family | What is counted or measured | Type |
|---|---|---|
| Build information | Service version and revision | Labelled gauge |
| Queues | Tasks in in-memory queues, per queue type and per priority (an extra internal priority above *high* is used for closing transfers); in-progress distributions per priority and the age of the oldest one per priority | Gauge |
| API usage | Processed API requests per endpoint | Counter |
| Release-set size | Artifacts per release-set version, overall and per package type | Histogram |
| Creation | Versions created, per request origin (API or UI); queries used to assemble legacy versions; queries with additional properties or path mappings | Counter |
| Distribution outcome | Distribution starts per request origin, per generation and within a project; completed and failed distributions per generation; export operations, overall and per outcome | Counter |
| Transaction failures | Failures while opening a transfer (signature or checksum conflict, signature validation failure, signing key missing on the target, target repository missing, source repository not local or missing, authorization or proxy-authentication errors, trust problems) and while closing it (bad request, conflict, forbidden, timeout, failed or unexpected status), each per cause | Counter |
| Retries | Retries per issue type when opening and closing transfers, transferring artifacts and deleting remotely | Counter |
| Remote deletion | Deleted versions; deletion failures per cause; delete actions per generation and type | Counter |
| Scan gate | Distributions and exports approved, blocked, rejected after a failed scan, or rejected because the scanner was unreachable, per generation; failed scan triggers | Counter |
| Path mapping | Path and repository changes applied; mappings taken from the last promotion | Counter |
| Stuck work | Stuck distributions per phase (up to 50) and stuck scans (counters); stuck distributions per status (gauge) | Counter, gauge |
| Conflicts | Versions whose name and version clash with an existing one under a different project | Counter |
| UI usage | Views of the active and history tabs of the monitoring page (DIST-029) | Counter |

</details>

Other notable settings: release-note sanitization (strips HTML from Markdown
release notes, default on, to prevent cross-site scripting); maximum query length for legacy
creation (default 6,000 characters, may be raised or disabled).

### DIST-038 · Legacy release sets assembled by the distribution service
Priority: **Later**

The legacy generation is built from queries rather than explicit sources.

- **Form.** *General*: name, version, description. *Spec*: the source instance and one or more
  named queries — built with a query builder (repository names, build names and numbers, property
  values, combined with and/or; the generated query is viewable) or written directly in the
  query language. *Preview artifacts* shows the result. *Release notes*: Markdown, AsciiDoc or
  plain text, with edit/preview. *Additional details*: additional properties for all artifacts and
  path mappings (DIST-028).
- **Container images.** A query matching an image's manifest file includes all its layers; a
  query matching a manifest list includes every referenced image.
- **Sign.** Finalizes the version (status *signed*, no further edits), copies the content into the
  storage repository (DIST-002), lets the user pick a signing key (default otherwise) and asks for
  the key passphrase when set.
- **Operations.** Create (signs immediately when a passphrase is given), update (unsigned only),
  sign, get all release sets, versions by name, latest version, version details (alternative
  signed response formats), SHA-256 of a version, delete one version, delete all versions, trigger
  a scan. Privilege for creation: create (write) on release sets.
- **Companion CLI** commands mirror create, update, sign, distribute, delete; options include
  release-notes file and syntax, exclusion patterns, target path with placeholders, target
  properties, choice of storage repository.

### DIST-039 · One-step create, sign and distribute
Priority: **Later**

For legacy release sets, a single request creates a version from a specification, signs it
(passphrase optional) and distributes it to the targets selected by distribution rules. How name
and version are determined is unspecified; the distribution then behaves as in DIST-027.

### DIST-040 · Retention of received release sets on targets
Priority: **Later**

Targets accumulate versions; a retention mechanism deletes received versions (both generations)
on a target.

- **Automatic deletion** happens only when both hold: the release set has reached its allowed
  maximum number of versions, **and** the version's release date is older than the allowed maximum
  number of days.
- **Cleanup report** lists candidates that meet one or both criteria, for manual deletion.
- Several policies may exist; a version matching any is marked. A policy always combines both
  values; changing one keeps the other at its last value.
- **Keep markers.** A version can be marked *keep* (never deleted) or *don't keep*. When the
  retention feature is disabled, keep markers are ignored and any version can be deleted.
- Purposes: save storage, remove redundant releases, allow distributing
  different content under the same path (mutable tags such as `latest`), force re-certification by
  requiring redistribution.

<details>
<summary>Retention operations (administrator)</summary>

| Operation | Behavior |
|---|---|
| Get retention status / enable or disable | Feature switch. |
| Get / set retention configuration | Partial updates allowed. |
| List candidates | By configuration type (days or versions). |
| Retention history | Past retention operations. |
| Delete release sets | Delete the given received versions. |
| Mark keep / don't keep | Set the keep marker. |

</details>

**UI** (administrators, feature enabled): *received* list → *cleanup* opens candidates in two
equivalent tabs, *by days* and *by versions*, with search; a lock icon per row shows and toggles
keep (kept rows shown by default); only *don't keep* rows can be selected for deletion; deletion
is permanent, asynchronous, confirmed in a dialog, and may report items that could no longer be
deleted. A *cleanup history* tab records deletions and keep changes (sortable, paginated; legacy
deletions made on the source are not listed). Artifacts shared with another release set remain.
Deleting in smaller batches performs better.

**Limits.** Supported only with PostgreSQL and one embedded database.

**Interactions.** Cleanup policies for release sets on the source: see
[retention-and-cleanup.md](retention-and-cleanup.md).

### DIST-041 · Hosted-service edges and pooled edge licenses
Priority: **Out of scope**

Edge instances run as a hosted service, a portal to set them up, and pooled licenses for
self-hosted edges attached to a hosted source belong to the operation, billing and license
management of a hosted service and have no meaning for a self-hosted system. Self-hosted edges
reached from a self-hosted source are covered by DIST-024 to DIST-025.

### DIST-068 · Edge instance capability profile and setup
Priority: **Later**

An edge instance runs the same software as a full repository manager and is installed the same
way (any method of [ha-and-deployment.md](ha-and-deployment.md)); a deployment profile chosen at
installation turns an installation into an edge. Its feature set is cut down to the purpose of
serving released content.

| Capability | On an edge instance |
|---|---|
| Content arriving through distribution (DIST-027) | Yes; this is how content normally arrives |
| Direct upload | Only generic files, through the UI or the API, into one upload repository that the edge creates automatically and that cannot be deleted |
| Other repositories | Read-only; release sets are deployed only into the upload repository named above |
| Remote repositories | Only remote repositories whose upstream is another instance of the same system (RMT-040 in [remote-repositories.md](remote-repositories.md)), and only towards instances of a full or edge kind; a remote repository pointing at a public registry is not possible |
| Pull replication | No |
| Replication, federated repositories, builds, projects, scanning integration, storage sharding, server-side extension scripts | No |
| Downloads, storage back ends | Yes |
| High availability | Unspecified |

**Setting up an edge as a distribution target**, in this order:

1. Establish trust between source and edge (DIST-025).
2. Register the edge in the central instance registry, so that the distribution service sees it as
   a target (REPL-051 in [replication-and-federation.md](replication-and-federation.md)).
3. Add the edge as a target of identity federation from the source, so that users, groups,
   permissions and tokens are synchronized to it (REPL-053).
4. Propagate the release-set signing keys to the new edge (DIST-026).

**Interactions.** Permissions for distributing to it: DIST-035. Received release sets: DIST-034.

## Air-gapped transfer

### DIST-042 · Export a release set version as an archive
Priority: **Later**

For instances without a network connection between them: export the version to a ZIP archive,
carry it on removable media, import it on the destination (DIST-043).

- **Prerequisite.** The signing key must have been propagated to the source's own trusted public
  keys (DIST-026).
- **Export (current transfer mode)** — runs on the source instance (not available on an edge);
  builds a signed, self-contained ZIP asynchronously. Repeating the call is harmless: while an
  export runs, or once its archive exists, it answers "accepted" with the same export ID
  (`<storage repository>/<name>/<version>`) and starts nothing new. No path mapping happens on
  this side; mapping is chosen at import (DIST-043). Privilege: distribute on the release set.
  Errors: version not in an exportable status (400), version missing (404).
- **Export status** — states: started, in progress, completed, failed, not triggered (no archive
  and no running job); poll until completed, then the response carries the download URL
  (absolute and repository-relative); a failure carries a message. Privilege: read on the release
  set. Not available on an edge.
- **Delete export archive** — succeeds when no archive exists; rejected while an export is
  started or in progress. Privilege: delete on the release set. Also allowed on an edge.
- **Export via the distribution service (older mode)** — export, status and delete-archive
  operations on the service (delete requires administrator); path mappings from DIST-028 apply.
- **UI.** Version timeline *actions → export*, optional regex path mappings with templates and the
  last-promotion default (older mode); the export appears on the timeline; its dialog lists the
  archived artifacts and offers delete archive (adds a delete event and crosses out the export),
  download archive and copy link.

**Legacy generation.** Only signed versions in *ready* state can be exported; exactly one archive
per version; requires distribute permission; export, status (download URL when completed),
download and delete-archive operations on the distribution service.

### DIST-043 · Import a release set archive
Priority: **Later**

Uploads an exported ZIP on the destination, verifies the DSSE envelope and recreates the version
with origin *received*.

<details>
<summary>Import settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Archive | The exported ZIP (multipart file part). | required |
| Mode | *Path mapping*: copy the artifacts into repositories after creation. *Unpromoted*: create only, no copy. | Path mapping |
| Path mappings | Regex input matched against the artifact path in the archive, output path template (e.g. `releases-local/$1`). Empty: artifacts go back to the repositories they were exported from. | None |
| Auto-create missing repositories | Create each missing target as a local repository with the package type recorded on the artifacts and assign it to the import project (ignored in unpromoted mode). | Off |
| Project | Target project. | The default project |

</details>

- The options part is a JSON object of at most 1 MiB, parsed as JSON whatever content type it is
  sent with; absent, blank or `{}` means defaults; unknown properties are ignored.
- Creation and the optional copy are synchronous; a failed copy fails the request.
- **On an edge** only path-mapping mode and the default project are allowed, and mappings must not
  target repositories outside the default project.
- **Output.** Name, version, project, applied mode.
- **Privilege.** Administrator, plus write on the target release-set path.
- **Errors.** 400: missing file, invalid or oversized options, invalid mode, malformed ZIP,
  unsupported archive schema, missing target repository without auto-create, conflicting or
  missing package type for an auto-created repository. 404: auto-create with a non-existent
  non-default project. 409: the version already exists (only a previously failed import may be
  retried).

**Legacy generation and UI.** An import operation plus an import-status operation on the
destination (serving both generations in the older mode). UI: *received* list → *import*, from a
local file or from a server-side import folder (the file is deleted there after a successful
import; in a cluster, any node's import folder works); a progress bar; the result appears under
*received*. Requires administrator.

**Interactions.** Instance-level export and import: see
[operations-and-monitoring.md](operations-and-monitoring.md).

## Evidence

### DIST-044 · Evidence model
Priority: **Later**

Evidence is signed metadata — an attestation — stating that something happened to a **subject**:
for example that tests passed, a scan ran, a change was approved, or a promotion took place.

- **Subjects.** Artifact, package version, build, release set version, application version, and
  application (as non-artifact entity), plus arbitrary non-artifact entities such as a VCS commit
  (DIST-049). Each evidence file has exactly one subject.
- **Sources.** Internal evidence generated by the system (DIST-051) and external evidence created
  by CI systems, tools and partners (DIST-047, DIST-048).
- **Immutability.** An evidence file is stored as a standalone file immutably linked with its
  subject; the file name is generated.
- **Recommended practice.** Before an artifact, package or build is included in a release set or
  application version, evidence goes onto that item; afterwards it goes onto the version.
- **Governance.** Lifecycle policies evaluate evidence; missing required evidence is a policy
  violation (DIST-058).

**Interactions.** A graph query API returns evidence connected with release sets and packages in
one query and only returns what the caller may read (see
[events-and-extensibility.md](events-and-extensibility.md)).

### DIST-045 · Evidence file format
Priority: **Later**

Three nested layers, based on the in-toto attestation framework and DSSE:

1. **Envelope (DSSE).** Base64 payload; payload type, always `application/vnd.in-toto+json`;
   signature list (key ID = name of the signing key, signature computed per DSSE); optional
   attachment reference (repository, path, optional SHA-256).
2. **Payload (in-toto statement v1).** Statement type `https://in-toto.io/Statement/v1`; one
   subject as a resource descriptor whose digest is the subject's SHA-256 (for non-artifact
   entities the digest key is the entity type, DIST-049); predicate type (a URI acting as schema
   and category, used for querying and policies); predicate; plus extensions: creation time (when
   the evidence was created, not deployed), creator, optional Markdown rendering of the predicate,
   optional attachment list (name, SHA-256, MIME type). The attachment list is part of the signed
   payload.
3. **Predicate.** User-defined JSON claims, shaped by the predicate type; the source of truth for
   policies and queries.

**Limits.** Payload at most 16 MB; larger material goes into an attachment (DIST-050).

**Edge cases.** The creator field written by the companion CLI comes from the client's
configured user name; the identity behind the upload token plays no part.

### DIST-046 · Evidence signing keys and server-side verification
Priority: **Later**

- The private key signs evidence on the client; the server never needs it.
- Supported key types: RSA (example 2,048 bits), EC on the P-256 curve, Ed25519. The companion CLI
  can generate a P-256 pair and upload the public key in one step.
- Uploading the public key (optional, recommended) lets the server verify evidence: key
  management, *public keys* tab (alias plus key text), or the trusted-key API, which returns key
  ID, fingerprint, key, issued-on, issued-by and valid-until. Evidence whose key ID matches an
  uploaded key is marked *verified*.
- Signature algorithms accepted for REST-created evidence: RS256 (PKCS#1 v1.5 padding) and PS256
  (PSS padding); PS256 must be declared by appending `;PS256` to the key ID.
- In a federation the public key must be uploaded on every member; verification only happens on
  members that hold it.

### DIST-047 · Deploy evidence to a subject
Priority: **Later**

Uploads a finished, signed evidence envelope for a subject addressed by its repository path.

- **Inputs.** Subject repository path; optional provider ID (the source system, e.g. a code
  scanner or CI service); body: a DSSE envelope with optional attachment. Two other body shapes
  are auto-detected: a Sigstore bundle (which embeds its own DSSE envelope) and a bundle carrying a
  DSSE envelope plus the public key.
- **Validation order.** Token → envelope structure → payload decoding → signature → payload
  structure → subject repository must be local or federated → subject exists at the path → subject
  resolved by full path → subject digest compared (when a SHA-256 is given).
- **Output (evidence record).** ID (SHA-256 based), subject repository and path, subject name and
  version, evidence file name, URI, SHA-256 of the evidence file, predicate category (e.g.
  security, quality, workflow), predicate type, predicate slug (readable short form), creation time
  and creator, verified flag, signing key alias, provider ID, stage, attachments (name, SHA-256,
  MIME type, download path); signatures and predicate on request.
- **Privilege.** Read and annotate on the subject path.
- **Errors.** 400 invalid path or body; 404 subject missing; 409 evidence already exists; 412
  attachment not supported by the server version; 413 body too large.
- **Clients.** The companion CLI builds the statement, wraps and signs it and deploys it; the
  server verifies on deploy when the public key is present. Hand-built envelopes work the same way.

### DIST-048 · Server-assisted evidence preparation
Priority: **Later**

A two-step flow for clients without the companion CLI (for example a plain CI script):

1. **Prepare** — inputs: predicate, predicate type, optional Markdown, optional provider ID,
   subject (type artifact, build, package, release set, application version or entity, with the
   identifying fields of that type; optional subject SHA-256), optional attachment (repository and
   path; the server resolves the SHA-256), scope fields for entities and project-scoped subjects;
   option to include the pre-authentication encoding. Output: the URL to post the signed envelope
   to, the base64 payload, its payload type, optionally the PAE string, resolved attachments. 404
   when the subject cannot be resolved.
2. The client signs the PAE with its private key, builds the DSSE envelope (payload, payload type,
   signatures, attachments) and posts it to the returned URL (DIST-047 or DIST-049).

### DIST-049 · Evidence on entities
Priority: **Later**

Evidence can be addressed by *entity type* and *ID* instead of a repository path.

- **Non-artifact entities** (every entity type that is not one of the reserved kinds below, e.g.
  a VCS commit or an ML model; IDs from `[-_.A-Za-z0-9]`, usually one segment): the system creates a placeholder in an
  entity repository chosen by scope — per type by default, or per project, per application, or an
  explicit repository (at most one scope). The signed subject digest uses the entity type as key
  and the ID as value.
- **Reserved, artifact-backed entities** resolve to an existing lead file; the subject digest is
  that file's SHA-256, identical to evidence attached through the subject path:

  | Entity | ID format | Scope |
  |---|---|---|
  | Artifact | Repository-relative path (may contain `/`; no leading/trailing `/`, empty, `.` or `..` segments) | Repository required |
  | Package version | `<name>/versions/<version>`, split at the last `/versions/`; name shape per format (Maven `group:artifact`, npm `@scope/name`, OCI `library/nginx`, Go module path, …) | Repository required (gives the package type) |
  | Application version | `<application key>/<version>` | Optional project |
  | Release set version | `<name>/<version>` | Optional project |
  | Build version | `<name>/<number>` or `<name>/<number>/<timestamp>` (digits, ≤ 64); without timestamp the latest matching build | Optional project |

- IDs may span several URL path segments; `:` and `@` are URL-encoded. For get and delete by
  entity, the last segment is the evidence name.
- **Operations.** Deploy, get and delete evidence by entity.

### DIST-050 · Evidence attachments
Priority: **Later**

An evidence file can carry one unstructured attachment (PDF, penetration-test report, signed
contract, diagram), typically when content exceeds the 16 MB payload limit.

- The file is first uploaded to a repository; prepare and deploy reference it by repository and
  path; its name, SHA-256, MIME type (and entity ID) are embedded in the signed statement. The
  SHA-256 is optional at prepare and required at final deploy so the server can check it against
  the signed statement.
- After creation the attachment is stored with the evidence and downloadable through the
  evidence record's download path.
- The companion CLI can upload a local file to a configured temporary repository path first and
  move it into place when the evidence is created.
- At most one attachment per evidence.

### DIST-051 · Internally generated evidence
Priority: **Later**

The system attests its own actions with signed evidence:

- Release-set creation, each promotion (signed and locked, recording when, where and by whom) and
  each distribution; application-version promotion and release.
- A scanner adds an SBOM (CycloneDX by default, SPDX optional) and a vulnerability report on
  release-set creation and application promotion.
- Internal evidence uses predicate types in the system's own namespace for promotion and
  distribution, plus CycloneDX and SPDX types for SBOMs; it is also rendered as Markdown.
- Events for internal evidence appear on the version timeline for users with evidence access.

### DIST-052 · View, search and download evidence
Priority: **Later**

- **Evidence tables** on an artifact (repository tree; also for a release set selected in the
  tree), a package version (per repository) and a build version: verified mark, evidence type with
  provider icon, creation time, creator, actions *view* and *download*. The release-set and
  application-version tables add the subject (e.g. a specific package).
- **Viewer.** Shows the predicate: Markdown in a *content* tab and JSON in a *spec* tab when both
  exist; at most 3,000 lines, beyond that only download.
- **Content graph** of a release set or application version: builds, packages and all related
  evidence; clicking an evidence card shows its predicate.
- **Download all evidence** of a release set version as one JSON file.
- **Operations.** Get evidence by subject path, get by ID, search (query filters, paginated),
  get by entity (DIST-049). The graph query API returns only what the caller may read.

### DIST-053 · Evidence lifecycle
Priority: **Later**

- Evidence follows its subject when the subject is copied or moved.
- Deleting a subject first deletes its evidence asynchronously, then the subject.
- **Delete evidence** — explicit deletion of an evidence file and its record (also by entity).
- Deleting a release set version deletes its evidence; restoring it restores the evidence
  (DIST-013). Promotion rollback deletes the promotion's evidence (DIST-021).
- Cleanup policies delete the evidence of what they delete; archiving keeps evidence with the
  archived package (see [retention-and-cleanup.md](retention-and-cleanup.md)).

### DIST-066 · Client-side evidence verification
Priority: **Later**

A consumer can check evidence on its own machine instead of relying on the server's *verified*
flag (DIST-046): the companion CLI fetches the evidence of one subject and checks, per evidence
file, that the subject digest matches the stored artifact and that the signature validates.

- **Subjects.** Artifact (repository path), package version (name, version, optional repository),
  build (name, number) and release set version (name, version, optional project).
- **Keys.** Either a list of public keys given by the caller (RSA, ECDSA, Ed25519) or, on request
  (off by default), the public keys uploaded to the server.
- **Output.** Human-readable by default, JSON on request.
- In a federation the keys must be present on the member that is asked (DIST-016).

**Interactions.** Server-side verification: DIST-046. The same envelope format as release set
verification: DIST-012.

### DIST-069 · Pull-request merge evidence collected at promotion
Priority: **Later**

Proves for an audit that every code change in an application version went through a reviewed
and merged pull request, without manual work.

- **Trigger.** A project administrator (or higher) chooses the lifecycle stages at which
  collection happens. When an application version is promoted or released into such a stage and
  the promotion succeeds, collection runs as part of the successful promotion; it neither blocks
  nor delays the promotion.
- **What is collected.** The system determines the commits that went into the version since the
  previous released version (when one exists), maps them to merged pull requests of the hosting
  service and attaches one signed evidence record (DIST-044) to the application version.
- **Record content.** Source data (repository and commit range per package); merge details (who
  merged, when); the approvers; problem indicators: commits that could not be linked to a merged
  pull request (shown as unresolved), stale approvals and unverified commits.
- **Prerequisites.** The hosting service's app integration with the system; a CI
  step that records a signed merge attestation whenever a pull request is merged; packages
  deployed by CI together with build provenance attestations (DIST-070); application versions
  that follow semantic versioning.
- **Visibility.** The evidence tab of the application version (DIST-052) and one activity-log
  event per successful collection (DIST-062).

The flow as specified here covers GitHub only. For trove the equivalent unit is a GitLab merge
request: merge commit, merger, approvals and pipeline status are available from the GitLab API
and, for the pipeline that built a package, from its ID-token claims
([oidc-ci-integration.md](oidc-ci-integration.md)).

**Interactions.** Lifecycle policies can require this evidence at a gate (DIST-058, DIST-059);
capability-level summary of governance integrations: DIST-064.

### DIST-070 · Evidence from CI attestations and third-party tools
Priority: **Later**

Besides evidence a client builds and signs itself (DIST-047, DIST-048), there are three ways in
which other tools' results become evidence:

- **CI build-provenance attestations.** A CI workflow that pushes an artifact or image and then
  creates a [SLSA](https://slsa.dev/) build-provenance attestation for it with the CI platform's
  own attestation step gets that attestation stored as evidence on the artifact; it then follows
  the artifact through copies and moves (DIST-053) and can be required by a lifecycle policy. The
  example platform is GitHub Actions; the subject is named by the fully qualified image name
  without tag, identified by its digest. How the attestation travels from the CI platform into the
  system is unspecified (the setup action of BLD-030 in
  [builds-and-ci.md](builds-and-ci.md) is involved; the job summary shows the result).
- **Conversion by the companion CLI.** The CLI can turn a code-quality tool's analysis result into
  signed evidence on any subject kind (artifact, package, build, release set). For SonarQube it
  reads the scanner's report-task file from the build tool's default output location, polls the
  analysis server for the result, and signs it with the given private key and key alias. Settings
  come from environment variables or from an evidence configuration file found by searching
  upward from the working directory (then the user's home); environment variables win:

  | Setting | Meaning | Default |
  |---|---|---|
  | Server URL | Analysis server | taken from the report-task file, else the public hosted service |
  | Report-task file | Location of the scanner's output | per build tool (Maven, Gradle, scanner CLI, MSBuild) |
  | Polling retries / interval | How often and how fast the result is polled | unspecified |
  | Access token | Credential for the analysis server | required (environment variable) |

- **Partner integrations and reference designs.** Vendors of security testing, test management,
  infrastructure-as-code scanning, build analytics, change management and deployment tools push
  signed evidence directly; tools without a native integration are covered by example workflows
  that serve as reference designs (for example SBOMs and vulnerability reports from
  [Syft](https://github.com/anchore/syft) and [Grype](https://github.com/anchore/grype), JUnit
  test results, Jenkins pipeline results, OpenSSF Scorecard reports, GitLab CI SBOM and SLSA
  provenance, issue-tracker ticket links found in commit messages). Each produces an ordinary
  signed DSSE envelope (DIST-045).

For trove, accepting a standard in-toto/DSSE envelope (DIST-047) covers all three paths without
tool-specific server code; the GitLab CI path is the reference design for SBOM and SLSA
provenance named above.

**Interactions.** DIST-044 to DIST-053; DIST-064.

## Application release governance

### DIST-054 · Applications
Priority: **Later**

An application is a business-level entity inside a project that owns packages, container images
and builds, and carries business context: key, name, owners, labels, business criticality and
maturity level. It is the system of record for its software assets and the parent of application
versions (DIST-056). Applications are first-party (own), second-party (another team of the same
organization) or third-party (external, including open source).

**Operations.** List, create (also in batch), get, update, delete applications; list an
application's evaluations (DIST-059). UI: governance area, *applications* table with per-row
actions (e.g. copy application key) and per-application pages (lifecycle/stage board, versions,
resources, evaluations, activity).

**Webhook events.** Application create, change and delete (each started, completed, failed);
application version create, promote and release (each started, completed, failed); entry and
exit gate evaluations (started, passed, failed). See
[events-and-extensibility.md](events-and-extensibility.md).

### DIST-055 · Package binding
Priority: **Later**

Package versions can be bound to an application so that every package has an owning application
— for example to route a vulnerability found after release to the right owners.

- **Automatic binding** through the CI OIDC integration: the pipeline declares the application key
  in a file in its source repository, and packages produced by that pipeline are bound
  automatically.
- **Manual binding** — bind and unbind a package version; list bindings and bound versions.

**Interactions.** CI OIDC federation: see [oidc-ci-integration.md](oidc-ci-integration.md).

### DIST-056 · Application versions
Priority: **Later**

An application version is a signed, immutable bill of materials of an application at one point in
time; it inherits the application's business context and is the unit that passes the lifecycle.

<details>
<summary>Creation settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Version | Version identifier; SemVer recommended. | required |
| Sources | Query, artifacts (path, SHA-256), builds (name, number, repository, start time, include dependencies), package versions, release set versions, other application versions (of this or other applications). At least one. | required |
| Filters | Include/exclude by package name, type, version, path, SHA-256 (as DIST-007). | None |
| Skip container manifest resolution | As DIST-001. | Off |
| Draft | Iteratively changeable: content can be added and the version renamed; no scans until finalized; promotion finalizes (locks and signs) first. | Off |
| Auto-assign to first stage | After creation, promote automatically to the first lifecycle stage if all source artifacts are already in repositories of that stage; otherwise the version stays unassigned. | Off |
| Tag | One label, max 128 characters, starting and ending alphanumeric, `-` `_` `.` inside; typically the branch. | None |
| Signing key | As DIST-004. | Default key |
| Conflict resolution | Automatic (newest by creation date) or manual (caller resolves with filters). | Automatic |
| Dry run / asynchronous | Validate only / return on start. | Off / asynchronous |

</details>

**Output.** Application key, version, creation time, creator, dry-run flag, status, tag, and a
message (for example why automatic assignment did not happen; synchronous only). Conflict → 409.

**Usage patterns.** Each microservice team releases its own application version
internally; a platform-level "application of applications" version references the certified
microservice versions for the external release.

**Further operations.** List versions, get content, get status, update (draft), delete, post an
integration event, list evaluations, get one evaluation; tag a version; stage board filters by
time (7, 30, 90 days, custom, any — default any) and by version or tag; version timeline, content,
evidence and risk views.

### DIST-057 · Application promotion, release and rollback
Priority: **Later**

- **Promotion** moves a version to the next stage of the lifecycle (DIST-019); entry and exit gates
  of the stages evaluate policies first (DIST-059). If the target stage has repositories, content
  is placed there; stages without repositories are allowed.
- **Release** is the promotion into the final release stage, guarded by a single *release* gate;
  a version that passes it becomes a certified release, receives a signed attestation, and remains
  under vulnerability monitoring afterwards. Without a release gate, completing the release makes
  the version released.

<details>
<summary>Promotion settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Target stage | Stage to promote into (release uses its own operation). | required |
| Promotion type | *Copy* (no physical copy when source and target repository are the same), *move*, *keep* (stage changes, artifacts stay), *dry run*. | Copy |
| Included repositories | If set, only these are used — excluded repositories are then ignored. | All |
| Excluded repositories | Repositories to skip. | None |
| Path mappings | Input/output/package-type triples applied to relative paths (repository keys unchanged). | None |
| Overwrite strategy | For write-protected artifacts already in the target (mainly mutable container tags): *latest* (only if the tag is exactly `latest`), *all*, *disabled* (fail with 409). | Latest |
| Additional properties | Added to each promoted artifact; overrides an existing value with the same key. | None |
| Signing key | Optional change. | Current key |
| Asynchronous | Return on start. | Asynchronous |

</details>

**Output.** Source and target stage, included/excluded repositories, properties, status, message,
and the gate evaluations (entry, exit, release: decision, evaluation ID, explanation, number of
policies, stage).

**Privilege.** A role with the *promote* action for promotion and the *release* action for release.

**UI.** Application stage board: columns per stage, gates shown as boxes left (entry) and right
(exit) of each stage title; new versions start in *unassigned* or the first stage depending on
configuration; cards show version, latest promotion and time; failed promotions carry an error
banner with the failing policy and a link to the full evaluation. Promote or release by drag and
drop or from the timeline's actions (dialog as in DIST-020).

**Rollback.** Undo the latest promotion (also a release): the record is removed and the audit log
keeps the rollback. What happens to content depends on how the version got there: after a copy,
the copies in the stage being left are deleted; after a move, the content goes back into the
previous stage's repositories and is removed from the stage being left; after a keep, only the
stage assignment changes. UI: timeline *actions → rollback version*.

**Distribution.** Application versions reach edge targets and air-gapped sites too (DIST-065).

### DIST-058 · Lifecycle policies, rules and templates
Priority: **Later**

- A **policy** combines a scope (organization, project, application; global policies may span
  several), an operation (e.g. the exit gate of QA, the entry gate of a stage, the release gate, a
  copy), a rule and an action: *fail* (block) or *warn*.
- A **rule** is a condition with concrete parameter values, for example "evidence of QA exit
  certification is present" or "no component with a CVE whose CVSS score is 9.0–10.0". A rule can
  be used by several policies. Built-in rules exist.
- A **template** holds a rule's logic and parameters without values (e.g. "at least X % of tests
  pass"); several rules can use one template with different values. Templates are written in Rego
  (Open Policy Agent), are versioned and categorized (security, legal, operational, quality,
  audit, workflow); built-in templates exist; a playground lets users view, write, edit and test
  templates (load sample input, dry run); the set of allowed Rego operations is restricted. An
  AI assistant in the playground can draft template code from a natural-language description
  (for example "check a code-quality report for every package"); it asks clarifying questions,
  generates only on request, and the result stays editable.
- **Operations.** CRUD for policies, rules and templates.
- **Privilege.** Actions *read policy* and *manage policy* in global or project roles; only
  project administrators or higher decide which stages carry gates.

### DIST-059 · Gate evaluations
Priority: **Later**

When a promotion reaches a gate, all policies at that gate are evaluated against the evidence
attached to the version, producing one overall decision:

| Decision | Effect |
|---|---|
| Pass | All policies satisfied; the version may advance. |
| Fail | A blocking violation; the version cannot advance. |
| Warning | An issue is raised, the version may advance. |
| Error | A technical error stopped the evaluation; the version cannot advance. |

Views: all evaluations of an application (time, decision, action, stage, resource type, scanner
types), the policies evaluated in one evaluation, and the issues triggered. Operations: list
evaluations (globally and per application or version), get one evaluation. Gate evaluation start,
pass and fail are webhook events.

### DIST-060 · Waivers
Priority: **Later**

A waiver lets a version pass despite failing policies, for a limited time and a chosen scope —
for example all policies of one stage gate for one application, or specific findings, policies or
rules. A waiver needs a justification and approval: requesters (role with *read policy*) create
and cancel requests, also directly from a failed promotion; approvers (role with *manage policy*)
approve, reject and revoke.

### DIST-061 · Compliance frameworks
Priority: **Later**

Maps external standards (e.g. EU Cyber Resilience Act, NIST SSDF) onto automated rules.

- Terms: **framework** (a standard), **control** (a legal condition), **requirement** (its
  technical restatement), **rule** (the automated check), **scope** (applications of a project the
  framework applies to), **enforcement action** at the release gate (*warning* or *fail*).
- Setup in order: a security administrator activates a framework in the compliance catalog and
  enables (all start disabled) or replaces the mapped rules; each project administrator selects
  the applications in scope and the enforcement per rule and application; application owners
  attach the required evidence documents with the matching predicate types. Nothing is enforced
  before both configuration steps are done.

### DIST-062 · Governance activity log
Priority: **Later**

A searchable record of governance events on applications, versions, packages, policies, stages
and the lifecycle — at system, project and application level.

- **Columns.** Time, event, event ID, category (e.g. application management, lifecycle policy
  management, stage management), subject, subject ID, project (in the all-projects view), result
  (in progress, success, failure, blocked), performed by.
- **Details.** For a promotion: source and target stage, gates passed and their evaluation results,
  link to the evidence; for a policy update: the changed fields.
- **Search and filters.** Event name, subject type or application name; time range, event type,
  subject name and type (application, application version, policy, stage, package), application,
  category, result, project, performer, subject ID.
- **Retention.** 90 to 180 days online; every 90 days the 91–180-day block is archived as a ZIP
  into a system repository and a webhook announces the archive.
- **Operation.** Get activity log (filters as above).

### DIST-063 · Governance insights
Priority: **Later**

Dashboards per application and organization: security metrics, delivery metrics (deployment
frequency, lead time for changes), application health, lifecycle status, recent activity, and
*coverage* — open findings by severity and age plus whether the expected scanners run at each
lifecycle point (curation, pull-request merge, commit, build, production, distribution, runtime).
A metrics operation exposes the underlying numbers; the metric catalog is unspecified.

### DIST-064 · External governance integrations
Priority: **Later**

At capability level: issue-tracker integration for a development promotion process,
change-management integration (authentication, global and project configuration, application
synchronization, completing a change request), and pull-request-merge evidence from a VCS
hosting service; partners push signed evidence directly. Integration configurations can be saved,
removed and unsynchronized through the API. Infrastructure-as-code providers can manage applications and
policies. The detailed behavior of each integration is unspecified.

### DIST-065 · Distribution and air-gapped transfer of application versions
Priority: **Later**

Application versions (DIST-056) travel to edge targets the same way release sets do.

- **Distribute** — inputs: application key, version, distribution rules. A rule selects targets
  by site, city, country or target group; a wildcard covers every value of one field; several
  rules widen the selection; a rule may carry its own priority (as in DIST-031). Privilege: read
  and execute on the application version, plus distribute on at least one selected destination
  (DIST-035). Destinations the caller may not use are dropped; if nothing remains, the request
  fails with 400. The operation can also answer with a conflict; the cause is unspecified.
- **Remote delete** — removes the version from selected targets and keeps it on the source;
  targets chosen by rules (site, city, country codes) or a rules file; a dry run simulates it.
  Privilege: delete on the application version and on the targets. Answers "accepted" and runs
  asynchronously.
- **Air-gapped transfer** — the companion CLI exports a version as an archive to the local file
  system (a target ending in `/` is treated as a directory, otherwise as the file name; large
  downloads are split into ranges) and imports such an archive on another instance or an edge,
  optionally into a given project, with an optional path mapping (source pattern with
  placeholders mapped to a target path) or *unpromoted* (no repository placement, off by
  default). Server-side operations for this are unspecified; release-set archives (DIST-042,
  DIST-043) are the comparable mechanism.

**Interactions.** Distribution mechanics: DIST-027 to DIST-032.

### DIST-067 · Runtime status of released application versions
Priority: **Later**

When a runtime sensor reports which images run where (for example in a Kubernetes cluster), each
application version gets a runtime status:

| Status | Meaning |
|---|---|
| Running | Deployed in at least one connected environment, no known integrity problem. |
| Integrity violation | Deployed, but at least one problem was detected, for example the running image's checksum no longer matches the stored version, or a container was altered after start. |
| (none) | Not running in any connected environment; no indicator is shown. |

The status shows on the release stage of the application stage board and in the version details
operation as two flags (detected running, integrity violated). Prerequisite: a runtime monitoring
component with at least one connected sensor; that component itself is outside this catalog's
scope.

**Interactions.** Governance insights list runtime as a coverage point (DIST-063).

## Notes for implementers

- **Immutability is a snapshot, not a reference.** A release set copies its artifacts into its
  storage repository at creation; rollback of a move promotion and restore from trash rely on that
  copy. Deduplicated blob storage makes this cheap — implement it as new references to the same
  blobs.
- **Signing happens at creation, re-signing at promotion; distribution always uses the creation
  key.** Targets need the public key before the first distribution, and the source needs its own
  key in its trusted list before an air-gap export; without that propagation step distribution
  and export cannot succeed, so the UI and API should surface a missing key explicitly.
- **Default asynchronous behavior.** Creation, promotion, rollback and draft updates are
  asynchronous unless the caller asks otherwise; asynchronous success is "submitted" (200),
  synchronous success is "created" (201); a dry run also returns 200. Clients must poll status.
- **Copy is forced in two cases**: promotion from *unassigned* and promotion into the stage the
  version already occupies, even if move was requested.
- **Overwrite defaults differ**: release-set promotion fails by default when it would overwrite a
  foreign file; application promotion overwrites by default when the tag is exactly `latest`;
  rollback skips by default.
- **Path mappings are sticky per target** and, in the current transfer mode, not applied at
  air-gap export but at import; the UI demands deleting from a target before redistributing even
  when the API would allow it.
- **Evidence creator is client-asserted.** The creator field inside the signed payload comes from
  the client configuration, not from the authenticated identity. Policies that depend on who
  created evidence must not trust that field alone.
- **Evidence verification is optional.** Unverified evidence (no uploaded public key) is accepted
  and flagged; policies that need trust must check the verified flag.
- **Remote deletion keeps shared artifacts.** Deleting a version from a target leaves artifacts that
  another release set on that target still uses; the same holds for retention cleanup.
- **Two promotion paths share one stage model.** Release-set promotion (DIST-020) and
  application promotion (DIST-057) use the same stages and lifecycle but differ in defaults and
  gates; creation, properties, evidence and distribution of release sets do not depend on stages,
  so they can be built before either promotion path.
