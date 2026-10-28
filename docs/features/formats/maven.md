# Maven package format

This page catalogs what a repository manager does to act as a Maven repository: hosting artifacts
under the Maven 2 path layout in local repositories, proxying and caching upstream Maven
repositories in remote repositories, aggregating both behind one URL in virtual repositories, the
format-specific repository settings (release and snapshot handling, snapshot naming, maximum
unique snapshots, checksum policies, POM consistency), calculation of `maven-metadata.xml`, the
Maven index for IDE integrations, client setup and the build integrations that sit on top. The
path layout and the metadata format are defined by the Apache Maven project, not by any repository
manager; facts taken from Maven's own documentation link it
([repository layout](https://maven.apache.org/repository/layout.html),
[repository metadata](https://maven.apache.org/repositories/metadata.html),
[metadata model](https://maven.apache.org/ref/3.9.9/maven-repository-metadata/repository-metadata.html),
[checksums](https://maven.apache.org/resolver/about-checksums.html)). Gradle, Ivy, sbt and
Eclipse P2 are on [jvm-other.md](jvm-other.md). Repository classes and layouts in general are
described in [../repositories.md](../repositories.md), pull-through caching in general in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/maven/<repo-key>/` stands for the base URL of one
repository (the path prefix is the implementation's choice, see REPO-005 in
[../repositories.md](../repositories.md)); `maven-local`, `maven-snapshots-local`, `maven-remote`
and `maven-virtual` are example repository keys; `${REPO_TOKEN}` is an access token.

## Repositories and addressing

### PKG-MAVEN-001 · Maven repositories in all repository classes
Priority: **P1**

Maven is a format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)); a federated variant exists as well (see
[../replication-and-federation.md](../replication-and-federation.md)).

- Local repositories hold the organization's own releases and snapshots; the system computes their
  Maven metadata (PKG-MAVEN-014).
- Remote repositories proxy and cache an upstream Maven repository such as Maven Central
  (PKG-MAVEN-017). Example upstream URLs: `https://repo.maven.apache.org/maven2` in an API
  request, `https://repo1.maven.org/maven2/` in a configuration-as-code file. The create form
  shows a pre-filled upstream URL for the user to confirm or change; the pre-filled value is
  unspecified.
- Virtual repositories aggregate local and remote members under one URL (PKG-MAVEN-022) and are
  the recommended target for client configuration.
- Maven, Gradle, Ivy and sbt are four separate format choices that share one block of
  format-specific settings, the "Maven family" (PKG-JVM-001 in [jvm-other.md](jvm-other.md)).
- The format is fixed at creation (REPO-002). The layout defaults to the Maven 2 layout
  (REPO-012, PKG-MAVEN-002).

Creating a repository requires the system administrator or a project administrator; for others
the action is not offered. UI location: administration, repositories, create repository, choose
the class, then the Maven format. Local: enter the repository key. Remote: confirm the upstream
URL. Virtual: select one or more local or remote members.

<details>
<summary>Maven-family settings per repository class</summary>

| Setting | Local | Remote | Virtual | Default | Entry |
|---|---|---|---|---|---|
| Handle releases | yes | yes | — | on | PKG-MAVEN-003 |
| Handle snapshots | yes | yes | — | on | PKG-MAVEN-003 |
| Snapshot naming behavior | yes (also federated) | — | — | unique | PKG-MAVEN-010 |
| Maximum unique snapshots | yes | yes | — | 0 (no limit) | PKG-MAVEN-011 |
| Deployment checksum policy | yes (also federated) | — | — | verify against client checksums | PKG-MAVEN-006 |
| Upstream checksum policy | — | yes | — | generate if absent | PKG-MAVEN-018 |
| Suppress POM consistency check | yes | yes | — | off | PKG-MAVEN-005 |
| Eagerly fetch jars | — | yes | — | off | PKG-MAVEN-019 |
| Eagerly fetch sources | — | yes | — | off | PKG-MAVEN-019 |
| List remote artifacts | — | yes (Maven only) | — | off | PKG-MAVEN-020 |
| Repository layout | yes (mandatory) | yes (cache, mandatory) | optional | Maven 2 layout | PKG-MAVEN-002 |
| Remote layout mapping | — | yes | — | none | PKG-MAVEN-002 |
| POM repository-reference cleanup | — | — | yes | discard active references | PKG-MAVEN-023 |
| Force authentication | — | — | yes (Maven only) | off | PKG-MAVEN-025 |
| Signing key pair | — | — | yes (the configuration-as-code reference: Maven only) | none | PKG-MAVEN-026 |

Generic settings that also apply (include/exclude patterns, priority resolution, disable
resolution, cache periods, default deployment repository and so on) are in
[../repositories.md](../repositories.md) and [../remote-repositories.md](../remote-repositories.md).

</details>

Interactions: Maven coordinate search, version list and latest-version search (SRCH-006, SRCH-016,
SRCH-017 in [../search-and-query.md](../search-and-query.md)); package retention policies use the
POM as the lead file of a Maven version (RET-009 in
[../retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-MAVEN-002 · Repository URL and Maven 2 path layout
Priority: **P1**

Every Maven repository answers the Maven repository protocol under its own base URL. A Maven
client uses that URL as the `<url>` of a repository, plugin repository, mirror or distribution
target, for example `https://repo.example.com/maven/maven-virtual/`.

Files are stored under the Maven 2 layout (PKG-MAVEN-036): the group ID with dots turned into
slashes, then artifact ID, then version, then `<artifactId>-<version>[-<classifier>].<extension>`;
the descriptor is the `.pom` file in the same folder. Example (own):
`com/example/http-client/4.2.0/http-client-4.2.0.jar` next to
`com/example/http-client/4.2.0/http-client-4.2.0.pom`.

The layout is a named layout (REPO-046 to REPO-052 in [../repositories.md](../repositories.md))
from which the system derives module information:

| Module field | Maven meaning | Example |
|---|---|---|
| Organization | group ID (stored as path) | `com/example` |
| Module | artifact ID | `http-client` |
| Base revision | version without the snapshot part | `4.2` for `4.2-SNAPSHOT` |
| Folder integration revision | the constant `SNAPSHOT` in the version folder | `SNAPSHOT` |
| File integration revision | `SNAPSHOT` or a timestamp with build number in the file name | `20240115.093012-7` |
| Classifier | classifier | `sources` |
| Extension | file extension | `jar` |

Maven 2 layout definition in descriptive notation (REPO-048):

| Part | Value |
|---|---|
| Artifact pattern | `<org-path>/<module>/<base-rev>(-<folder-integ-rev>)/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Descriptor pattern | the artifact pattern with the literal extension `.pom` |
| Folder integration revision expression | `SNAPSHOT` |
| File integration revision expression | `SNAPSHOT\|(?:(?:\d{8}.\d{6})-(?:\d+))` (see the notes) |

- Bundled layouts relevant here: Maven 2/3 and Maven 1 (REPO-047).
- A remote repository can store its cache in the Maven 2 layout while its upstream uses the Maven 1
  layout; outgoing requests are then translated into Maven 1 paths (remote layout mapping, RMT-035
  in [../remote-repositories.md](../remote-repositories.md); REPO-051).
- A virtual repository with a layout translates requests into the layouts of its members; without
  a layout, or when module information cannot be built from the path, the path passes through
  unchanged (REPO-051).
- Copy and move between local repositories with different layouts convert paths (REPO-052).
- Repositories created before custom layouts existed were configured with the Maven 2 layout
  automatically.

Edge cases: a file whose path does not match the pattern gets no module information, so
layout-based functions (snapshot cleanup, version searches, package retention) do not see it.
No strict layout enforcement is specified for Maven; for repositories in general, content of the
wrong format is accepted but neither indexed nor reflected in the metadata (REPO-002). Whether a
Maven repository rejects a file outside the layout is therefore unspecified.

### PKG-MAVEN-003 · Release and snapshot handling
Priority: **P1**

The system classifies every path as release or snapshot (integration) through the layout's
integration revision expressions (PKG-MAVEN-002): `1.0-SNAPSHOT/...-1.0-SNAPSHOT.jar` and
`1.0-SNAPSHOT/...-1.0-20240115.093012-7.jar` are snapshots, `1.0/...-1.0.jar` is a release.

| Setting | Meaning | Default |
|---|---|---|
| Handle releases | Local: release artifacts may be deployed. Remote: release artifacts may be deployed or cached. | on |
| Handle snapshots | Local: snapshot artifacts may be deployed. Remote: snapshot artifacts may be deployed or cached. | on |

- A deployment of a kind the repository does not handle fails; a conflict with the repository's
  release/snapshot handling is one cause of failed deployments. The status
  code is unspecified.
- Resolution: the order in which repositories are searched is affected
  by their release and snapshot handling (REPO-035). The exact rule (for example that a member
  not handling snapshots is skipped for a snapshot path) is unspecified.
- Typical setup: one local repository for releases (handle snapshots off), one for snapshots
  (handle releases off), both aggregated with a remote repository in one virtual repository.
- Present on local, remote and federated configurations; shown for the Maven family (REPO-023,
  RMT-036).

UI location: repository settings, basic tab, Maven-family section.

Interactions: build-tool plugins pick the release or snapshot target by whether the version ends
in `-SNAPSHOT` (PKG-MAVEN-035).

## Deployment

### PKG-MAVEN-004 · Deploy through the Maven protocol
Priority: **P1**

Maven deploys with one HTTP `PUT` per file to the file's layout path below the repository URL
(PKG-MAVEN-036). A typical module deployment sends the main artifact, the POM, attached classified
artifacts, checksum files for each, optional `.asc` signatures, and `maven-metadata.xml`, which
Maven deploys with checksums like an artifact
([Maven metadata](https://maven.apache.org/repositories/metadata.html)).

Client configuration in the project's `pom.xml`:

```xml
<distributionManagement>
  <repository>
    <id>releases</id>
    <name>Internal releases</name>
    <url>https://repo.example.com/maven/maven-local/</url>
  </repository>
  <snapshotRepository>
    <id>snapshots</id>
    <name>Internal snapshots</name>
    <url>https://repo.example.com/maven/maven-snapshots-local/</url>
  </snapshotRepository>
</distributionManagement>
```

- Each `<id>` must match a `<server>` entry with credentials in the client's `settings.xml`
  (PKG-MAVEN-027). The `<name>` is free text for the user's own orientation. Both targets may be
  the same local repository.
- Target: a local repository, or a virtual repository that has a default deployment repository;
  the deployment then lands in that local repository (REPO-038).
- Properties at deploy time: matrix parameters appended to the deployment URL become properties of
  every deployed file; Maven substitutes project properties such as a build number or revision
  into the URL before deploying (ART-003 in [../artifacts.md](../artifacts.md)).
- Required permission: deploy on the target path; overwriting an existing file additionally needs
  the delete/overwrite action, except for non-unique snapshots (PKG-MAVEN-012).
- Checks on arrival, in unspecified order: include/exclude patterns (REPO-011),
  release/snapshot handling (PKG-MAVEN-003), POM consistency (PKG-MAVEN-005), deployment checksum
  policy (PKG-MAVEN-006), snapshot naming (PKG-MAVEN-010), maximum unique snapshots
  (PKG-MAVEN-011).
- Metadata: with unique or non-unique snapshot naming the system generates the Maven metadata
  itself; with deployer naming it does not (PKG-MAVEN-010, PKG-MAVEN-014). What happens to a
  `maven-metadata.xml` uploaded by the client in the first two modes (stored, replaced or ignored)
  is unspecified.
- Status codes: 401 and 403 for missing credentials or permissions; 409
  for a POM whose coordinates do not match its path (PKG-MAVEN-005).

Interactions: deploy by checksum (ART-004) is used by the build-tool plugin for files above a size
threshold (PKG-MAVEN-035).

### PKG-MAVEN-005 · POM consistency check
Priority: **P1**

On deployment the system compares `groupId:artifactId:version` declared in a POM with the path the
POM is deployed to. On a mismatch it rejects the deployment with `409 Conflict`, which keeps the
repository's metadata consistent with its content.

| Setting | Meaning | Default |
|---|---|---|
| Suppress POM consistency check | Accept POMs whose coordinates do not match the path. | off (check active) |

- Local repositories: applies to deployments. Remote repositories: the same switch makes the cache
  accept POMs with mismatching coordinates (RMT-036). Present on federated configurations too
  (REPO-026).
- Shown for the Maven family.

UI location: repository settings, basic tab, Maven-family section.

Edge cases, all unspecified: whether coordinates inherited from a parent POM
(a POM without its own `groupId` or `version`) are resolved before comparing; whether packaging
or classifier take part; how a POM that cannot be parsed is treated.

### PKG-MAVEN-006 · Deployment checksum policy
Priority: **P1**

Decides how checksums supplied by the deploying client are treated (ART-009 in
[../artifacts.md](../artifacts.md), REPO-022).

| Option | Behavior | Default |
|---|---|---|
| Verify against client checksums | If the client sent no valid checksum for a deployed file, a request for that file's checksum file answers `404`. If the client's checksum differs from the one the server computed, the checksum file answers `409` until a correct checksum is deployed. | yes |
| Trust server-generated checksums | Client checksums are not verified; the server's own checksums are used and the file is usable immediately. Integrity is not verified end to end. | — |

- Clients supply checksums either as checksum headers on the upload (ART-002) or, the Maven way,
  by deploying `<file>.sha1` and `<file>.md5` after the file.
- Applies to local and federated repositories of the Maven family.
- Files deployed without a valid client checksum can be found with the bad-or-missing-checksum
  search (SRCH-009 in [../search-and-query.md](../search-and-query.md)).

UI location: repository settings, basic tab, Maven-family section, "checksum policy".

Edge case: the interplay of an immediate `409` on an upload with a mismatching checksum header and
the `409` on the checksum file under this policy is unspecified (see ART-009).

### PKG-MAVEN-007 · Checksum files next to artifacts
Priority: **P1**

Maven verifies downloads against checksum files stored next to every file, named
`<file>.<algorithm>`. Maven Resolver supports MD5, SHA-1, SHA-256 and SHA-512 and by default
produces and checks SHA-1 and MD5
([Maven checksums](https://maven.apache.org/resolver/about-checksums.html)).

- In the system, checksum files (`.sha1`, `.sha256`, `.md5`) are listed and downloadable through
  the repository's browsing endpoint when the file was uploaded with its checksum value, or when
  the repository's checksum policy is "trust server-generated checksums" (PKG-MAVEN-006).
- Folder download with checksum files included (ART-015) follows the same rule for local
  repositories; for remote repository caches checksum files are always added.
- Checksum files of cached upstream content follow the upstream checksum policy (PKG-MAVEN-018).
- Whether `.sha512` files are served is unspecified (ART-008).
- Checksums give transport integrity only; tamper protection comes from `.asc` signature files,
  which Maven treats as optional ordinary files next to the artifact. Whether the system does
  anything with `.asc` files beyond storing them is unspecified.

### PKG-MAVEN-008 · Deploy dialog for Maven artifacts
Priority: **P2**

The UI deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md), ART-006 in
[../artifacts.md](../artifacts.md)) offers Maven-specific options when the file is a Maven
artifact:

| Option | Meaning | Default |
|---|---|---|
| Deploy as Maven artifact | Exposes the Maven coordinates (group ID, version, classifier and type), pre-filled by parsing the file name; edits change the target path. | unspecified |
| Generate default POM / deploy the JAR's internal POM | For a target without a POM: deploy the POM embedded in the JAR, or a generated default POM. A POM edited by hand must stay valid. | unspecified |

- The alternative "deploy according to layout" exposes the layout tokens instead (ART-006).
- UI uploads are limited by the system-wide UI upload size (ART-006).
- Whether an artifact ID field is editable is unspecified.

### PKG-MAVEN-009 · Generate a POM for a stored JAR
Priority: **P2**

The API counterpart of the dialog option: creates a POM for a JAR already stored in a repository,
taken from the JAR's embedded POM or, if there is none, a default POM.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Generate POM | repository key; path of the JAR | success status, body unspecified | unspecified; the operation's responses include `401` and `403` |

Unspecified: where the POM is written (presumably next to the JAR), the content of the default POM
(for example coordinates derived from the path), behavior when a POM already exists, and which
repository classes are allowed.

## Snapshots

### PKG-MAVEN-010 · Snapshot naming behavior
Priority: **P1**

Central control over how snapshots are named in a repository, independent of client settings, so
that an organization gets one consistent format (REPO-024).

| Option | Behavior | Default |
|---|---|---|
| Unique | Snapshots get a unique, time-based version in the file name (`<artifactId>-<base>-<yyyyMMdd.HHmmss>-<n>.<ext>`, PKG-MAVEN-038). | yes |
| Non-unique | Snapshots use the self-overwriting name `<artifactId>-<version>-SNAPSHOT.<ext>`. | — |
| As deployed | The name the deploying client sends is kept unchanged. The system generates no Maven metadata. | — |

- The default was non-unique in older releases and changed to unique; existing repositories may
  still carry non-unique.
- Maven 3 neither resolves nor deploys non-unique snapshots. Repositories still on non-unique
  should switch to unique and remove the old non-unique snapshots.
- "As deployed" must not be used on repositories that are replicated: each instance has to
  generate its metadata locally, which this mode does not do.
- A timestamp chosen by the client says nothing about the source revision it was built from; record
  the revision in the artifact itself or in build metadata instead
  ([../builds-and-ci.md](../builds-and-ci.md)).
- Applies to local and federated repositories.

UI location: repository settings, basic tab, Maven-family section.

Unspecified: how the system converts a deployment that arrives in the other form (a
`-SNAPSHOT` file into a unique repository, or a timestamped file into a non-unique one): which
timestamp and build number it assigns, or whether it rejects instead.

### PKG-MAVEN-011 · Maximum unique snapshots
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Maximum unique snapshots | Upper bound on the stored unique snapshots of one artifact. A snapshot that arrives once the bound is reached makes the system delete the oldest one on its own. | 0 or blank: no limit |

- Acts when a snapshot arrives, not on a schedule (RET-035 in
  [../retention-and-cleanup.md](../retention-and-cleanup.md)).
- Present on local, remote and federated configurations of the Maven family (REPO-025, RMT-036).
- Relies on module identification by layout (PKG-MAVEN-002); files the layout cannot parse are
  never counted.

UI location: repository settings, basic tab, Maven-family section.

Unspecified: what "the same artifact" covers (module and base version, or each classifier
separately), whether removed snapshots go to the trash can (ART-036), and whether the version
metadata is updated in the same step.

### PKG-MAVEN-012 · Overwrite rules for releases and snapshots
Priority: **P1**

- Overwriting a deployed release or a unique snapshot requires the delete/overwrite action on the
  path in addition to deploy (IAM-022 in [../identity-and-access.md](../identity-and-access.md)).
  Withholding that action makes releases immutable for the user or group.
- Non-unique snapshots can always be overwritten by anyone allowed to deploy.
- No repository-level switch that forbids overwriting releases altogether is specified.

### PKG-MAVEN-013 · Newest snapshot and release by symbolic version
Priority: **P2**

A download path may name a version symbolically (ART-013 in [../artifacts.md](../artifacts.md)):

| Case | Version position contains | Resolves to |
|---|---|---|
| Latest snapshot | Maven's own `<base>-SNAPSHOT` form, for example `1.2-SNAPSHOT/http-client-1.2-SNAPSHOT.jar` | the newest unique snapshot of that base version |
| Latest release | a reserved latest-release token | the newest release |

- Only local repositories, remote caches and virtual repositories take part; upstreams are not
  queried for this.
- How the Maven case orders versions to find the newest is unspecified; the alphabetical order
  and the system-wide switch to creation time that ART-013 describes are stated for the
  non-Maven tokens.
- The latest-version search (SRCH-017) answers the same question as an API call, ordering versions
  with Maven's version comparison.

## Maven metadata

### PKG-MAVEN-014 · Metadata calculation for local repositories
Priority: **P1**

The system computes `maven-metadata.xml` (PKG-MAVEN-037) for artifacts hosted in local
repositories.

- When: on deployment, when the snapshot naming is unique or non-unique (not in "as deployed"
  mode, PKG-MAVEN-010); on deletion (PKG-MAVEN-016); on copy and move, where
  the affected metadata is brought up to date as soon as the operation finishes (ART-032,
  ART-033); on explicit recalculation (PKG-MAVEN-015, REPO-009).
- Remote repositories serve the upstream's metadata from their cache (PKG-MAVEN-017); virtual
  repositories aggregate (PKG-MAVEN-022).
- Replicated repositories compute their metadata on each instance.
- A POM must exist for a version to take part in the calculation (stated for the recalculation
  operation).

Unspecified: whether the calculation after a deployment runs inside the request or asynchronously; which
metadata levels are produced (artifact level with the version list, snapshot version level, group
level with plugin prefixes); how `latest` and `release` are chosen and how the version list is
ordered; where `lastUpdated` comes from; how concurrent deployments of the same module are
serialized.

### PKG-MAVEN-015 · Recalculate Maven metadata
Priority: **P1**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate Maven metadata | repository key (local repositories only); folder path; optional flag that limits the calculation to the given folder instead of descending into subfolders | plain-text message | unspecified; the operation's responses include `401` and `403` |

- Default: recursive. Open: the flag either has the non-recursive meaning its name suggests, or
  it controls recursive calculation and defaults to false; the spec decides. This page uses the
  reading that matches the name.
- A POM file must exist in the repository for the calculation to succeed.
- The repository-wide "recalculate index" row action (REPO-009) covers the whole repository; it is
  a heavy operation.

### PKG-MAVEN-016 · Metadata kept consistent on deletion
Priority: **P1**

- Deleting a file or folder updates the corresponding metadata, so the deleted item no longer
  appears to clients or in searches (ART-034 in [../artifacts.md](../artifacts.md)).
- "Delete versions" (ART-035) removes all artifacts that share a group and version in one step,
  typically every module of a multi-module snapshot deployment, and keeps the metadata
  descriptors up to date. The dialog derives versions from the first 1000 artifacts at the
  selected folder level; further versions are reached by filtering by group ID or version.
- Whether removals by maximum unique snapshots (PKG-MAVEN-011) or by retention policies update the
  metadata immediately is unspecified.

## Remote repositories

### PKG-MAVEN-017 · Proxying a Maven upstream
Priority: **P1**

A remote Maven repository proxies one upstream URL and caches what clients request (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

- Release artifacts are treated as immutable once cached; cache expiry concerns only snapshots and
  metadata files such as `maven-metadata.xml`.

| Setting | Effect for Maven | Default |
|---|---|---|
| Metadata retrieval cache period (RMT-018) | How long a cached `maven-metadata.xml` (and snapshot content) is served before the upstream is asked again | 7,200 s; minimum 30 s |
| Metadata retrieval timeout (RMT-019) | If the refresh takes longer, the previously cached metadata is served | 60 s |
| Negative cache period (RMT-020) | How long an upstream `404` is remembered | 1,800 s |
| Cache invalidation (RMT-026) | Marks all cached metadata (for example metadata fetched from Maven Central) as expired; binaries are untouched; each item is refreshed on its next request, and a stale copy is served when the upstream fails | — |

- The cache uses the Maven 2 layout; an upstream in another layout can be mapped (PKG-MAVEN-002,
  RMT-035).
- Handle releases/snapshots, maximum unique snapshots and the POM consistency switch apply to the
  cache (PKG-MAVEN-003, PKG-MAVEN-005, PKG-MAVEN-011).
- The version list search with remote lookup reads the upstream's `maven-metadata.xml`
  (SRCH-016).

Unspecified: whether cached `maven-metadata.xml` is served byte for byte or rewritten, and how a
snapshot request is resolved when the upstream's snapshot metadata has changed since caching.

### PKG-MAVEN-018 · Upstream checksum policy
Priority: **P1**

What happens when an upstream checksum is missing or does not match the checksum computed while
fetching (RMT-022 in [../remote-repositories.md](../remote-repositories.md)):

| Option | Behavior | Default |
|---|---|---|
| Generate if absent | Fetch the upstream checksum; if there is none, compute one and cache the item; if it does not match, do not cache and fail the download. | yes |
| Fail | Missing or mismatching upstream checksum: do not cache, fail the download. | — |
| Ignore and generate | Ignore upstream checksums, use only the computed one. Retrieval never fails on checksums; integrity is not verified. | — |
| Ignore and pass through | Store and serve upstream checksums even when they do not match; compute one when missing. Retrieval never fails, but Maven's own client-side checksum verification then fails on mismatching files. | — |

- The UI shows it for Maven-family remotes; configuration as code treats it as Maven-only; the
  repository configuration schema lists all four options.

UI location: remote repository settings, basic tab, Maven-family section.

### PKG-MAVEN-019 · Eager fetching of jars and sources
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Eagerly fetch jars | When a POM is requested, the matching jar is fetched from the upstream in the background. | off |
| Eagerly fetch sources | When a binary jar is requested, the matching sources jar is fetched in the background. | off |

The background fetch shortens the first access to the companion file (RMT-025). Configuration as
code treats the jar option as Maven-only; the UI shows both for the Maven family. How "matching"
is derived (same coordinates, packaging `jar`, classifier `sources`) is unspecified.

UI location: remote repository settings, basic tab, Maven-family section.

### PKG-MAVEN-020 · Browsing upstream content that is not cached
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| List remote artifacts | Users can navigate the upstream's content in the artifact browser even where nothing is cached yet. Offered for Maven only within the Maven family. | off |

- Uses the remote folder listing mechanism (RMT-038): listings are cached for the metadata
  retrieval cache period; items not cached yet are marked as such. Maven Central supports
  browsing.
- Maven clients themselves never need folder listings (PKG-MAVEN-036).

### PKG-MAVEN-021 · Upstreams that need cookies and cross-host credentials
Priority: **P2**

Some Maven upstreams authenticate across hosts. One example, the Oracle Maven
repository, works only with all three of:

- upstream credentials set on the remote repository (RMT-005);
- credentials forwarded to the hosts the upstream redirects to (RMT-012);
- cookie management on (RMT-013).

Security: forwarding hands the upstream credential to whatever host the upstream redirects to
(RMT-012).

## Virtual repositories

### PKG-MAVEN-022 · Aggregated resolution through a virtual repository
Priority: **P1**

- Clients point at one virtual URL; members are resolved in three tiers, local repositories, then
  remote caches, then upstreams, each tier in list order (REPO-035). Permissions, include/exclude
  patterns, the members' release/snapshot handling and priority resolution (REPO-018, supported
  for Maven) shape the order.
- A request for the latest version consults every member including the upstreams, even when a
  local repository or cache already holds a version; a newer upstream version is downloaded and
  cached (REPO-036).
- Virtual repositories answer with metadata aggregated from their members (REPO-037), and
  `maven-metadata.xml` is one of the expiring metadata files. The
  algorithm for merging Maven metadata is unspecified.
- Virtual repositories keep an internal cache of aggregated metadata such as POM files
  (PKG-MAVEN-023, PKG-MAVEN-024).
- Deploying through the virtual URL requires a default deployment repository (REPO-038).
- Requests from other instances may or may not reach the members' upstreams (REPO-039).
- The client setup instructions for Maven offer only virtual repositories (PKG-MAVEN-029).

Interactions: permissions through virtual repositories, IAM-024 in
[../identity-and-access.md](../identity-and-access.md).

### PKG-MAVEN-023 · Repository references removed from served POMs
Priority: **P1**

A Maven-family virtual repository can strip repository declarations from the POMs it serves, so
that clients resolve only through the system and not from repositories a third-party POM names
(REPO-041).

| Option | Behavior | Default |
|---|---|---|
| Discard active references | Strips repository declarations at project level and those in profiles of the same POM that are switched on by default; declarations in other profiles stay. | yes |
| Discard any reference | Strips every repository declaration, wherever in the POM it appears. | — |
| Nothing | Serves the POM unchanged. | — |

UI location: virtual repository settings, Maven-family section.

Unspecified: whether plugin repository elements are removed as well, and how checksums are served
for a POM the system has changed (see the notes).

### PKG-MAVEN-024 · Cleanup of cached POMs in virtual repositories
Priority: **P2**

A system maintenance job deletes POM files from the virtual repositories' internal metadata cache
once they are older than 168 hours (one week).

| Setting | Meaning | Default |
|---|---|---|
| Virtual repository cleanup schedule | Cron expression (Quartz syntax with seconds, for example `0 0 /12 * * ?` for every 12 hours) for the job | unspecified |

UI location: administration, maintenance. Whether a "run now" action exists for this job is
unspecified. See also [../operations-and-monitoring.md](../operations-and-monitoring.md).

### PKG-MAVEN-025 · Force authentication on virtual Maven repositories
Priority: **P1**

When anonymous access is enabled (IAM-005 in
[../identity-and-access.md](../identity-and-access.md)), the system by default does not ask a
Maven client for credentials. Consequence (inferred): a client that
sends credentials only after an authentication challenge never sends them and is served with
anonymous rights.

| Setting | Meaning | Default |
|---|---|---|
| Force authentication | The system requests credentials from the Maven client before giving access to this repository (HTTP basic authentication). | off |

- Offered for Maven virtual repositories only within the Maven family (REPO-028).

UI location: virtual repository settings (new or edit dialog), Maven-family section.

### PKG-MAVEN-026 · JAR signing on download through a virtual repository
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Signing key pair | Named key pair the virtual repository uses to sign artifacts automatically; the configuration reference names Java Web Start and JAR signing as the purpose. | none |

The key pairs come from the JAR signing keystore (IAM-048 in
[../identity-and-access.md](../identity-and-access.md); REPO-042). Unspecified: which files are
signed, whether signed output is cached, and which checksums are served for a signed file.

## Authentication

### PKG-MAVEN-027 · Credentials in Maven client configuration
Priority: **P1**

- Maven authenticates with `<server>` entries in `settings.xml`. Every `<repository>`,
  `<pluginRepository>`, `<mirror>` and distribution target that needs credentials must have a
  `<server>` with the same `<id>` holding username and password.
- Authentication is needed when anonymous access is off, when repositories are restricted to some
  users, and for deployment.
- Recommended: a token (identity token, TOK-005 in
  [../access-tokens.md](../access-tokens.md)) in the password field instead of the user's
  password. A reference token (TOK-004) must be sent together with the username, because Maven's
  HTTP client uses basic authentication.
- Encrypted passwords: generated settings files can carry a server-issued encrypted form of the
  user's password instead of clear text (IAM-035). Maven's own client-side password encryption
  does not solve the problem centrally: the decrypted password is in memory and on the wire, the
  master key lies in clear text on disk, and its use cannot be mandated.
- Client behavior to expect: when several repository definitions use the same URL, Maven takes the
  credentials of the first one it encounters and uses them for all of them for the whole build;
  different credentials for the same URL therefore produce `401` errors.
- Settings file locations: per user `~/.m2/settings.xml`, per installation
  `<maven home>/conf/settings.xml`, or a file passed with `mvn -s <file>`.

```xml
<servers>
  <server>
    <id>releases</id>
    <username>jdoe</username>
    <password>${REPO_TOKEN}</password>
  </server>
</servers>
```

### PKG-MAVEN-028 · Existence check before authentication
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Check existence before authenticating (system-wide) | For requests to Maven local, remote and virtual repositories that require authentication, the system first checks whether the requested item exists. If not, it answers `404` without authenticating; if it does, it authenticates and authorizes as usual. | off |

- Self-hosted only; changing the setting requires a restart.
- Purpose: Maven probes many repositories for every dependency; misses no longer need an
  authentication round trip.

Security: the difference between `404` (absent) and `401` (present) tells unauthenticated callers
which paths exist, which conflicts with hiding unauthorized resources (IAM-032 in
[../identity-and-access.md](../identity-and-access.md)).

## Client setup

### PKG-MAVEN-029 · Maven settings in the client setup instructions
Priority: **P1**

The client setup dialog (UI-021 and UI-022 in [../ui-inventory.md](../ui-inventory.md)) generates
a complete `settings.xml` for Maven with the system's URL and the user's credentials or a newly
generated token filled in. It supports virtual Maven repositories only.

- The user picks four repositories: releases, snapshots, plugin releases, plugin snapshots.
- The generated file overrides Maven's built-in `central` and snapshot repositories: two
  `<server>` entries (`central`, `snapshots`) with username and token; one profile, activated
  through `<activeProfiles>`, with a `central` repository (snapshots disabled) and a `snapshots`
  repository, plus the same pair as plugin repositories.
- Option "mirror all repositories": adds a mirror with `<mirrorOf>*</mirrorOf>` pointing at a virtual
  repository, so that requests to repositories declared inside third-party POMs also go through
  the system. It does not separate releases from snapshots; the one repository must resolve both.
  See [Maven mirror settings](https://maven.apache.org/guides/mini/guide-mirror-settings.html).
- The Maven instructions include the `distributionManagement` snippet (PKG-MAVEN-004) pointing at
  a local repository; whether the dialog shows it on a separate tab is unspecified.

<details>
<summary>Shape of the generated settings file (own example)</summary>

```xml
<settings xmlns="http://maven.apache.org/SETTINGS/1.2.0">
  <servers>
    <server><id>central</id><username>jdoe</username><password>${REPO_TOKEN}</password></server>
    <server><id>snapshots</id><username>jdoe</username><password>${REPO_TOKEN}</password></server>
  </servers>
  <mirrors><!-- only with "mirror all repositories" -->
    <mirror><id>central</id><mirrorOf>*</mirrorOf><url>https://repo.example.com/maven/maven-virtual/</url></mirror>
  </mirrors>
  <profiles>
    <profile>
      <id>repo-manager</id>
      <repositories>
        <repository><id>central</id><snapshots><enabled>false</enabled></snapshots>
          <url>https://repo.example.com/maven/maven-virtual/</url></repository>
        <repository><id>snapshots</id><snapshots/>
          <url>https://repo.example.com/maven/maven-snapshots-virtual/</url></repository>
      </repositories>
      <pluginRepositories>
        <pluginRepository><id>central</id><snapshots><enabled>false</enabled></snapshots>
          <url>https://repo.example.com/maven/maven-virtual/</url></pluginRepository>
        <pluginRepository><id>snapshots</id><snapshots/>
          <url>https://repo.example.com/maven/maven-snapshots-virtual/</url></pluginRepository>
      </pluginRepositories>
    </profile>
  </profiles>
  <activeProfiles><activeProfile>repo-manager</activeProfile></activeProfiles>
</settings>
```

</details>

- The generated profile carries a fixed ID chosen by the system; Maven only needs
  `<activeProfiles>` to name the same ID.
- The mirror entry uses the ID `central`, so Maven applies the credentials of the `central`
  `<server>` entry to it; no separate server entry is generated for the mirror.

### PKG-MAVEN-030 · Personalised settings files
Priority: **P2**

An administrator can publish one settings file that each user downloads in a personalised form
(filtered resources, ART-020 in [../artifacts.md](../artifacts.md)):

1. open the client setup dialog for a Maven (or Gradle, or Ivy) repository and generate settings;
2. download the generated snippet and replace user-specific values with template placeholders;
3. deploy the file as an artifact and mark it as filtered.

Each download is rendered for the caller, for example with the caller's own credentials.
Requires administrator rights.

## Maven index for IDE integrations

### PKG-MAVEN-031 · Maven index for IDE integrations
Priority: **P2**

IDE integrations (IntelliJ IDEA, NetBeans, Eclipse) read Maven indexes to offer artifact search.
The system exposes such indexes:

- Remote repositories: the index is fetched from the upstream when it provides one; otherwise the
  system calculates one from the artifacts already cached.
- Local and virtual repositories: the system calculates the index.
- Maven indexes have nothing to do with the system's own search: they are rebuilt on a schedule,
  carry only a subset of the data, and are deliberately not protected by permissions.
- In Maven's layout the index lives in `.index/` at the repository root, as a full index
  `nexus-maven-repository-index.gz` and incremental chunks
  `nexus-maven-repository-index.<n>.gz` with a `nexus-maven-repository-index.properties` file
  ([Maven repository layout](https://maven.apache.org/repository/layout.html)). Whether the
  system serves the index at exactly these paths is unspecified.

Security: anyone who can read a repository's index learns its whole content, regardless of
path-level permissions inside the repository. Repositories with confidential paths should not be
indexed.

### PKG-MAVEN-032 · Scheduled index calculation
Priority: **P2**

<details>
<summary>Index schedule settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Index calculation runs on the schedule. | unspecified (a configuration example shows off) |
| Cron expression | Quartz-style cron expression for recalculating the indexes of the included repositories. | unspecified |
| Next indexing time | Read-only: next scheduled run. | — |
| Run indexing now | Action: start a run immediately. | — |
| Included repositories | Repositories indexed on the next run. | unspecified |
| Excluded repositories | Repositories not indexed on the next run. | unspecified |
| Clear all | Action: remove every repository from the included list. | — |

</details>

- Calculation is resource-intensive, especially for large local repositories and for virtual
  repositories with many members; repositories that need no index should be excluded.
- The configuration-as-code form carries enabled, cron expression and included repositories.

UI location: administration, system settings, Maven indexer.

### PKG-MAVEN-033 · Calculate the Maven index on demand
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Calculate Maven index | comma-separated list of repository keys; "force" flag (0 or 1) | plain-text message | unspecified; the operation's responses include `401` and `403` |

- Calculates and caches the index for the listed repositories.
- For a virtual repository, the caller lists all member repositories that the aggregated index
  should include.
- Unspecified: what "force" changes (presumably recalculation even when an index is current), and
  whether the call is synchronous.

## Build integration

### PKG-MAVEN-034 · Wrapping Maven with the companion CLI
Priority: **P2**

The companion command-line client can run Maven on the user's behalf, route resolution and
deployment to configured repositories and collect a build metadata record (BLD-039 to BLD-041 in
[../builds-and-ci.md](../builds-and-ci.md)). Maven-specific parts:

<details>
<summary>Per-project Maven configuration of the wrapper</summary>

| Setting | Meaning | Default |
|---|---|---|
| Global | Apply to all projects on the machine instead of the current project | off |
| Resolution connection, deployment connection | Stored server profiles to resolve from and deploy to | none |
| Release resolution repository, snapshot resolution repository | Repositories for release and snapshot dependencies | none |
| Release deployment repository, snapshot deployment repository | Repositories that receive releases and snapshots; without them nothing is deployed | none |
| Include patterns, exclude patterns | Wildcard filters on the artifacts to deploy | none |
| Use Maven wrapper | Run the project's `mvnw` in wrapped mode | off |
| Disable snapshot resolution | Do not resolve snapshots | off |
| Snapshot update policy | How often Maven checks for newer snapshots: `always`, `daily`, `interval:<minutes>`, `never` (Maven's own update policy) | `daily` |

</details>

- Run options: build name and build number (both or neither), project, module, upload threads
  (default 3; parallel artifact uploads, not Maven's `-T`), skip TLS verification, scan before
  upload with a choice of output format.
- The wrapped run deploys artifacts from the `install` phase on; earlier phases deploy nothing.
  BLD-040 records an open question about install versus deploy.
- A separate wrapper command runs the project's `mvnw`/`mvnw.cmd`: in native mode it searches the
  current and parent directories and fails without falling back to `mvn`; with a per-project
  configuration present it behaves like the plain wrapper.
- Native mode (BLD-041) uses the user's own `settings.xml` from the client setup instructions
  (PKG-MAVEN-029) and still collects build metadata, also for multi-module projects.
- On first use the wrapper downloads a helper library from a public distribution site; in isolated
  networks a remote Maven repository of the system pointed at that site serves it.
- Requires a Java runtime on the path. With a reference token, a username must be configured,
  because Maven's HTTP client uses basic authentication (PKG-MAVEN-027).

Server operations it relies on: the Maven protocol (PKG-MAVEN-004, PKG-MAVEN-036) and build
publication (BLD-013).

### PKG-MAVEN-035 · Build-tool plugin for Maven
Priority: **Later**

A Maven plugin that replaces Maven's deployer, deploys release and snapshot artifacts and publishes
the build metadata record from inside the build (BLD-033 in
[../builds-and-ci.md](../builds-and-ci.md)).

- Requires Maven 3.8.1 or later and Java 8 or later; credentials come from settings, CI secrets or
  protected properties, never from the POM.
- Targets are Maven local repositories for releases and snapshots; the deploying identity needs
  deploy permission and permission to publish build metadata.
- Its goal binds to the `validate` phase; Maven then logs that it skips its own deployment, which
  is expected; uploads start after all modules have completed. `mvn deploy` must still be run.
- The snapshot target is used when the project version ends in `-SNAPSHOT`.
- Files from a configurable size on (default 10 KB, 0 disables) are first offered by checksum
  (ART-004), so the server must support deploy by checksum.
- Resolution through the system is configured separately from deployment; without it, Maven keeps
  resolving from Maven Central.

## Protocol reference

This section summarizes, from Maven's own documentation, what a Maven-compatible repository
serves. Paths are relative to the repository URL (PKG-MAVEN-002).

### PKG-MAVEN-036 · Repository paths and HTTP semantics
Priority: **P1**

From the [Maven repository layout](https://maven.apache.org/repository/layout.html):

```text
<repository root>/
  archetype-catalog.xml
  <groupId as path>/
    maven-metadata.xml            (+ .<checksum>)   group level, plugin groups only
    <artifactId>/
      maven-metadata.xml          (+ .<checksum>)   version list
      <version>/
        <artifactId>-<version>.pom                   (+ .asc, + .<checksum>)
        <artifactId>-<version>.<extension>           (+ .asc, + .<checksum>)
        <artifactId>-<version>-<classifier>.<extension> (+ .asc, + .<checksum>)
```

- `<groupId as path>` replaces `.` with `/` (`org.example.tools` → `org/example/tools`);
  `<checksum>` is one file per algorithm, `md5` and `sha1` by default; `.asc` is an optional
  detached PGP signature.
- Extension and classifier come from the producing plugin at publication time and from the
  dependency's `type` (default `jar`) and `classifier` at consumption time; one type maps to an
  extension and a default classifier.
- Transport: plain file operations over HTTP(S), `GET` to read and `PUT` to write. Clients never
  need directory listings. HTTP/1.1 reason phrases were used to carry extended error messages when
  access is refused; this usage is legacy and does not exist in HTTP/2.

### PKG-MAVEN-037 · `maven-metadata.xml` documents
Priority: **P1**

From [Maven metadata](https://maven.apache.org/repositories/metadata.html) and the
[metadata model](https://maven.apache.org/ref/3.9.9/maven-repository-metadata/repository-metadata.html).
All levels share one XML schema; one file can carry two levels at once when an artifact ID folder
of one module is the group folder of another.

| Level | Location | Content (metadata model) | Present in |
|---|---|---|---|
| Group | `<groupId as path>/` | `plugins/plugin` entries: plugin prefix, name, artifact ID; only for `maven-plugin` packaging, used to resolve prefixes such as `jar:jar` | release and snapshot repositories |
| Artifact | `<groupId as path>/<artifactId>/` | `groupId`, `artifactId`, `versioning/latest` (last version added, release or snapshot), `versioning/release` (last release added), `versioning/versions/version` (all versions), `versioning/lastUpdated` | release and snapshot repositories |
| Snapshot version | `<groupId as path>/<artifactId>/<base>-SNAPSHOT/` | `groupId`, `artifactId`, `version` (base version), `versioning/snapshot` (`timestamp`, `buildNumber`, `localCopy`), `versioning/snapshotVersions/snapshotVersion` (`classifier`, `extension`, `value`, `updated`) | snapshot repositories only |

- `lastUpdated` and `updated` are UTC `yyyyMMddHHmmss`; the snapshot `timestamp` is UTC
  `yyyyMMdd.HHmmss`; `buildNumber` defaults to 0, `localCopy` to false.
- `snapshotVersions` holds only the newest snapshot per classifier/extension pair; each pair
  appears once.
- Metadata files carry checksum files like artifacts.

<details>
<summary>Example documents (own)</summary>

```xml
<!-- com/example/http-client/maven-metadata.xml -->
<metadata modelVersion="1.1.0">
  <groupId>com.example</groupId>
  <artifactId>http-client</artifactId>
  <versioning>
    <latest>4.3-SNAPSHOT</latest>
    <release>4.2.0</release>
    <versions>
      <version>4.1.0</version>
      <version>4.2.0</version>
      <version>4.3-SNAPSHOT</version>
    </versions>
    <lastUpdated>20240115093012</lastUpdated>
  </versioning>
</metadata>
```

```xml
<!-- com/example/http-client/4.3-SNAPSHOT/maven-metadata.xml -->
<metadata modelVersion="1.1.0">
  <groupId>com.example</groupId>
  <artifactId>http-client</artifactId>
  <version>4.3-SNAPSHOT</version>
  <versioning>
    <snapshot>
      <timestamp>20240115.093012</timestamp>
      <buildNumber>7</buildNumber>
    </snapshot>
    <lastUpdated>20240115093012</lastUpdated>
    <snapshotVersions>
      <snapshotVersion>
        <extension>jar</extension>
        <value>4.3-20240115.093012-7</value>
        <updated>20240115093012</updated>
      </snapshotVersion>
      <snapshotVersion>
        <extension>pom</extension>
        <value>4.3-20240115.093012-7</value>
        <updated>20240115093012</updated>
      </snapshotVersion>
      <snapshotVersion>
        <classifier>sources</classifier>
        <extension>jar</extension>
        <value>4.3-20240115.093012-7</value>
        <updated>20240115093012</updated>
      </snapshotVersion>
    </snapshotVersions>
  </versioning>
</metadata>
```

</details>

### PKG-MAVEN-038 · Snapshot file naming
Priority: **P1**

From the [Maven repository layout](https://maven.apache.org/repository/layout.html):

- The version folder of a snapshot always uses the base version, for example `3.8.4-SNAPSHOT/`.
- File names take one of two forms:
  - base-version form: `<artifactId>-3.8.4-SNAPSHOT[-<classifier>].<ext>`; every deployment
    replaces the previous file (the system's "non-unique" naming, PKG-MAVEN-010);
  - timestamped form: `SNAPSHOT` replaced by `<yyyyMMdd.HHmmss>-<counter>`, for example
    `<artifactId>-3.8.4-20240115.093012-7.jar`; every deployment adds files (the system's "unique"
    naming).
- With the timestamped form, clients find the current files through the snapshot version metadata
  (PKG-MAVEN-037).

Maven's documentation calls the base-version form "unique SNAPSHOT artifacts", the opposite of the
system's use of "unique"; see the notes.

### PKG-MAVEN-039 · Repository-root auxiliary files
Priority: **P2**

Maven's layout documentation lists further files at the repository root
([Maven repository layout](https://maven.apache.org/repository/layout.html)):

| Path | Content |
|---|---|
| `archetype-catalog.xml` | Catalog of the Maven archetypes offered by the repository |
| `.index/` | Maven Indexer output (PKG-MAVEN-031) |
| `.meta/prefixes.txt`, `.meta/repository-metadata.xml` | Repository-level metadata files |

Whether the system generates, aggregates or proxies any of these is unspecified, apart from the
index (PKG-MAVEN-031).

## Notes for implementers

- **"Unique" means opposite things.** The repository setting "unique" stands for timestamped file
  names, Maven's layout page calls the base-version form "unique SNAPSHOT artifacts". Map the
  setting to file names (PKG-MAVEN-010, PKG-MAVEN-038), not to the word.
- **Every served file needs matching checksum files.** Maven fetches `.sha1`/`.md5` next to every
  artifact, POM and `maven-metadata.xml`. Anything the server rewrites (POMs with repository
  references removed, aggregated metadata in virtual repositories, signed JARs) must be served with
  checksums of the rewritten bytes, or Maven's checksum verification fails (PKG-MAVEN-007,
  PKG-MAVEN-023, PKG-MAVEN-026).
- **The Maven file-integration expression has an unescaped dot.** `\d{8}.\d{6}` matches any
  character between date and time; use `\d{8}\.\d{6}` in an implementation, and keep the folder
  and file expressions free of capturing groups (REPO-049).
- **Releases are immutable in caches, metadata is not.** Only snapshots and `maven-metadata.xml`
  expire in a remote cache; a stale copy is served when the upstream is slow or down
  (PKG-MAVEN-017). An unexpired negative cache entry hides a version that was just published
  upstream.
- **Anonymous access can hide a user's own rights.** With anonymous read enabled, the system does
  not ask Maven for credentials, so a client that waits for a challenge is served as anonymous
  unless the repository forces authentication (PKG-MAVEN-025); the opposite switch, existence
  check before authentication, discloses which paths exist (PKG-MAVEN-028).
- **One credential per URL on the client.** Maven reuses the first `<server>` credential for every
  repository definition with the same URL, so generated settings must not use different
  credentials for one URL (PKG-MAVEN-027).
- **POM consistency must cope with inheritance.** Many POMs inherit `groupId` and `version` from
  their parent; how the check handles that is unspecified, but comparing only the
  POM's own elements rejects valid deployments (PKG-MAVEN-005).
- **Concurrent deployments race on metadata.** Parallel CI jobs, or several cluster nodes, deploy
  modules of the same group and the same snapshot version at once; the version list and the
  snapshot metadata must be recomputed so that no deployment's update is lost (PKG-MAVEN-014).
- **Snapshot metadata must match the stored files.** `snapshot`, `snapshotVersions` and the
  timestamped files must agree after every deployment, removal by maximum unique snapshots and
  deletion; in a virtual repository the snapshot version metadata and the files it names should
  come from the same member (PKG-MAVEN-011, PKG-MAVEN-016, PKG-MAVEN-022).
- **A Maven index leaks the repository's content.** Never index a repository whose permissions
  differ by path; index calculation is also expensive, so only included repositories should be
  processed (PKG-MAVEN-031, PKG-MAVEN-032).
