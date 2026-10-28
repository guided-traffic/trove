# Repositories

This page catalogs the repository model of a full-featured binary repository manager: the
repository classes and what each one means, repository keys and their naming rules, every setting
of local and virtual repositories, repository layouts and their path tokens, the summary of which
repository classes each package format supports, project and environment assignment, and the API
operations that create, read, update and delete repository configurations one at a time or in
batches. Remote (pull-through) repositories are covered in
[remote-repositories.md](remote-repositories.md); federated repositories and replication in
[replication-and-federation.md](replication-and-federation.md); deploying, downloading, copying and
moving content in [artifacts.md](artifacts.md), including the artifact and package object model
(ART-001) and the package list and package details (ART-048, ART-049); the full per-format matrix
in [formats/README.md](formats/README.md); the client setup instructions in
[ui-inventory.md](ui-inventory.md).

## Repository model

### REPO-001 · Repository classes
Priority: **MVP**

A repository is a named container that groups artifacts by application, team or purpose. Every
repository has exactly one class, chosen at creation. The configuration update operation (REPO-054)
cannot change it; whether such an attempt is rejected or silently ignored is unspecified. The only
class change is the dedicated conversion of a local repository into a federated one
and back (UI row action "convert to federated", REPO-007; details in
[replication-and-federation.md](replication-and-federation.md)).

| Class | Stores content | Meaning |
|---|---|---|
| Local | yes | Hosts artifacts that the organization uploads or produces itself (first- and second-party content such as build outputs and internal packages). |
| Remote | yes, as a cache | Proxies and caches an upstream addressed by URL, for example a public package registry or a repository on another instance of the system. See RMT-001 in [remote-repositories.md](remote-repositories.md). |
| Virtual | no | One URL in front of an ordered set of local, remote and nested virtual repositories that share its format; it bounds where artifacts are searched and resolved. See REPO-033. |
| Federated | yes | A local repository whose content and metadata are synchronized with member repositories at other sites. See [replication-and-federation.md](replication-and-federation.md). |
| Release-set storage | yes, immutable | Holds signed, unmodifiable copies of the artifacts of a release set for distribution. See REPO-066. |

- A virtual repository can aggregate any number of members; an upper bound is unspecified.
- The listing operation (REPO-057) additionally accepts a class value for release distribution, and
  the "all configurations" operation (REPO-058) groups configurations under local, remote,
  virtual, federated and release-set storage.
- A new federation starts from a local repository to which member repositories are added; that
  procedure belongs to [replication-and-federation.md](replication-and-federation.md).

Interactions: format binding REPO-002; naming REPO-004; format support per class REPO-006.

### REPO-002 · One package format per repository
Priority: **MVP**

- A format (package type) is chosen when the repository is created and cannot be changed later.
- The format decides which metadata the system parses, which indexes it computes on every upload
  and which client protocol the repository speaks.
- A virtual repository can only aggregate repositories of its own format. The exception is a
  generic virtual repository (REPO-003).
- When the format is omitted on API creation, the configuration schema defaults it to generic.
  For virtual repositories the same schema marks the format as mandatory; treat it as required
  for virtual repositories.
- The set of accepted formats differs per class (REPO-006).

Edge cases: uploading content of another format is not blocked. That content is neither indexed
nor reflected in repository metadata. Non-conforming content (images, text files and other
resources not wrapped in the format's package structure) in a format-specific repository degrades
indexing and performance; such content belongs in a generic repository.

Interactions: artifacts versus packages and how an upload is classified, see ART-001 in
[artifacts.md](artifacts.md).

### REPO-003 · Generic repositories
Priority: **P1**

- A generic repository accepts files of any kind (installers, documents, media, formats the
  system does not support natively) and can also proxy unsupported formats as a remote repository.
- It maintains no format-specific package index. A native client of a specific format needs a
  repository of that format.
- A generic virtual repository may aggregate repositories of any format, but maintains no
  metadata of its own.

### REPO-004 · Repository key naming rules
Priority: **MVP**

The repository key is the repository's name and its identifier in URLs, configuration and API
calls.

| Rule | Value |
|---|---|
| Empty key | not allowed |
| Uniqueness | unique across the whole instance, across all classes |
| Maximum length: local, federated and virtual repositories | 64 characters |
| Maximum length: remote repositories | 58 characters |
| Maximum length: container-image formats (Docker, OCI, Helm charts stored as OCI) for local and federated repositories | 63 characters |
| Forbidden characters | `/` `\` `:` `\|` `?` `*` `"` `<` `>` |
| Container-image formats (Docker, OCI, Helm as OCI) | no upper-case letters, no underscore |
| Reserved exact keys | `.`, `..`, `&`, and a fixed list of keys taken by internal system repositories (internal logs, usage and billing logs, build metadata, pipeline information, the automatic trash can, support bundles, in-transit storage, edge-instance uploads, release-set storage) |
| Reserved prefixes | two system-reserved prefixes for internal repositories |
| Reserved suffix | a key must not end in `-cache` |

- The UI states a stricter rule: the key must not begin with a digit and must not contain spaces
  or special characters. The API rule set above does not include the leading-digit restriction.
- The reason for the reserved `-cache` suffix is unspecified. The remote limit (58) is exactly the
  general limit (64) minus the length of that suffix, which suggests that a remote repository's
  cache is addressed by appending it to the key; this is an inference, not a specified fact.
- Recommended (not enforced): lower-case letters and hyphens only, no underscores or other special
  characters, because those cause URL-encoding problems in reverse proxies and load balancers. A
  four-part convention `<team>-<technology>-<maturity>-<locator>` (for example
  `billing-npm-dev-local`) for local repositories; virtual repositories drop the locator part
  (`billing-npm-dev`) because they span locations.
- Recommended granularity: several smaller repositories per team or business unit rather than a
  few large ones; this gives clear security boundaries without folder-level permissions, avoids
  cross-team interference and performs better.

### REPO-005 · Repository addressing
Priority: **MVP**

- Artifacts of a local repository are addressed under the system's base URL by the repository
  key followed by the path inside the repository: `<base URL>/<repository key>/<artifact path>`.
  A virtual repository is likewise reached through one logical URL; the scheme is specified only
  for local repositories. Package-format protocol endpoints are described per format
  in [formats/README.md](formats/README.md).
- The repository listing (REPO-057) returns a URL per repository. For a local repository the URL
  is the repository's own access URL; for a project-scoped remote repository it is empty
  (REPO-015). What the URL holds for other classes is unspecified.

### REPO-006 · Format support per repository class
Priority: **MVP**

Not every format supports every class. The summary below states, per format, whether local,
remote and virtual repositories exist. The full matrix with clients, protocol versions and the
priority of each format lives in [formats/README.md](formats/README.md); this entry is MVP for npm
and NuGet and inherits each other format's priority from that page.

<details>
<summary>Class support per format</summary>

| Format | Local | Remote | Virtual |
|---|---|---|---|
| AI agent plugins | yes | no | no |
| AI agent packages (skills, prompts, hooks, MCP servers) | yes | no | no |
| AI editor extensions (marketplace proxy) | no | yes | no |
| Alpine Linux | yes | yes | yes |
| Ansible | yes | yes | yes |
| Bazel modules | no | yes | no |
| Bower | yes | yes | yes |
| Cargo | yes | yes | no |
| Chef | yes | yes | yes |
| CocoaPods | yes | yes (only through the CocoaPods CDN) | yes |
| PHP Composer | yes | yes | yes |
| Conan | yes | yes | yes |
| Conda | yes | yes | yes |
| CRAN | yes | yes | yes |
| Debian | yes | yes | yes |
| Docker | yes | yes | yes |
| Generic | yes | yes | yes |
| Git LFS | yes | yes | yes |
| Go modules | yes | yes | yes |
| Gradle | yes | yes | yes |
| Helm charts | yes | yes | yes |
| Hugging Face | yes | yes | yes |
| Ivy | yes | yes | yes |
| JetBrains plugins | no | yes | no |
| LuaRocks | yes | yes | yes |
| Maven | yes | yes | yes |
| npm | yes | yes | yes |
| NuGet | yes | yes | yes |
| OCI | yes | yes | yes |
| Opkg | yes | yes | no |
| Eclipse P2 | no | yes | yes |
| Dart/Flutter pub | yes | yes | yes |
| Puppet | yes | yes | yes |
| PyPI | yes | yes | yes |
| RPM (YUM) | yes | yes | yes |
| RubyGems | yes | yes | yes |
| sbt | yes | yes | yes |
| Swift | yes | yes | yes |
| Terraform/OpenTofu registry | yes | yes | yes |
| Terraform state backend | yes | no | no |
| Vagrant | yes | no | no |
| Version-control archives (VCS) | no | yes | no |

</details>

- The API accepts a separate format set per class. It agrees with the table on most rows: the
  remote set lacks the Terraform state backend and Vagrant; the virtual set lacks Cargo, Opkg, the
  Terraform state backend and Vagrant; the federated set is its own subset.
- The API distinguishes two Helm format values, classic Helm chart repositories and Helm charts
  stored as OCI artifacts, for every class; the table has a single Helm row. The naming limits of
  REPO-004 treat the OCI variant as a container format.
- Open, for the spec to decide, where the API sets and the table disagree: the API's virtual set
  also omits CocoaPods, although the table lists CocoaPods virtual repositories; no API set lists
  Eclipse P2, LuaRocks, JetBrains plugins, version-control archives or the AI formats, although
  the table lists them (P2 as remote and virtual, LuaRocks for all three classes, JetBrains
  plugins, version-control archives and AI editor extensions as remote, the AI agent formats as
  local); Bazel modules are either accepted for no class at all or for local, virtual and
  federated but not remote repositories, the exact opposite of the table.
- The repository listing's format filter (REPO-057) has yet another value set: it includes P2
  and a separate YUM value but omits npm, OCI, Alpine, Conda, Ansible and Hugging Face. Treat
  that enumeration as incomplete, not as a rule.
- Some formats (for example Hex, Nix, model-serving container images) are absent from the class
  table; see [formats/README.md](formats/README.md).

## Repository administration

### REPO-007 · Repository administration list
Priority: **MVP**

UI location: administration area, repositories section.

- A table of every repository with summary data, including **last activity** (the most recent
  upload to or download from the repository) and **project** (the project the repository is
  assigned to, and whether other projects share it).
- Views:

| View | Content |
|---|---|
| All repositories | every repository; the starting point for filtering |
| Recently viewed | the 20 repositories most recently created or opened in edit mode (opening counts even when nothing is saved) |
| Inactive | local repositories with resolution disabled (REPO-017), remote repositories set offline, federated repositories whose synchronization is paused because their configuration is out of sync |
| Shared | repositories shared from this project to others or from other projects to this one, with sharing date (last change of the sharing permission), sharing permission (read only, or read and write) and the state of the internal connection (pending, connected, not applicable when shared directly); see REPO-014 |

- Filters (all-repositories view, combinable, with a clear-all control): class (any combination),
  format (any combination), upstream URL (remote repositories only), project (assigned to, shared
  with, both, or neither assigned nor shared), environment (stage), and "has replication" (local
  and remote repositories only).
- Row actions:

| Action | Applies to |
|---|---|
| Show in the artifact browser | all |
| Open client setup instructions for this repository | all |
| Recalculate index (REPO-009) | formats that support it |
| Assign to a project (REPO-013) | all |
| Share with projects (REPO-014) | all |
| Delete (REPO-063) | all |
| Convert to federated | local |
| View aggregated repositories | virtual |
| Push configuration to out-of-sync members; show federation status | federated |
| Connect to the shared repository (when pending); view connected internal remote repositories; create another internal remote repository | shared repositories; the connect and create actions are unavailable in the all-projects scope |

Interactions: client setup instructions and the artifact browser, see
[ui-inventory.md](ui-inventory.md); federation actions, see
[replication-and-federation.md](replication-and-federation.md).

### REPO-008 · Quick repository setup wizard
Priority: **P1**

UI location: user menu, quick setup entry.

- The user picks one or more formats and enters a key prefix; the wizard then creates a local, a
  remote and a virtual repository for each selected format in one step.
- Limit: the prefix is at most 20 characters.
- Unspecified: the exact keys generated from the prefix, the upstream URLs of the created remote
  repositories, the membership and order of the created virtual repositories, and the privilege
  needed to run the wizard.

### REPO-009 · Recalculate repository index
Priority: **P1**

- A repository row action that recomputes all indexes and format metadata of the repository.
- Availability depends on the format; not every format offers it.
- It is a heavy operation and can degrade system performance on large repositories.
- The format-specific recalculation operations of the API are listed per format in
  [formats/README.md](formats/README.md) and [api-content-operations.md](api-content-operations.md).

## Settings shared by local and virtual repositories

### REPO-010 · Public description and internal notes
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Public description | Free text on content and purpose; visible to every user who can access the repository. | empty |
| Internal notes | Free text for administrators only. | empty |

Both settings exist on local, remote, virtual, federated and release-set storage repositories.

### REPO-011 · Include and exclude patterns
Priority: **MVP**

Two lists of Ant-style path patterns restrict which artifact paths a repository takes part in
when the system resolves a request.

| Setting | Meaning | Default |
|---|---|---|
| Include patterns | Paths the repository is consulted for; comma-separated list. | `**/*` (everything) |
| Exclude patterns | Paths subtracted from the included set; comma-separated list. | empty (nothing) |

- Effective set = paths matching an include pattern minus paths matching an exclude pattern.
- An empty include value also means "everything". In the UI, removing the include-everything row
  hides it from the form but it stays in effect.
- Example: include `com/example/**,org/vendorlib/**`, exclude `com/example/sandbox/**`. A request
  for `org/vendorlib/core/2.1/core-2.1.pom` or `com/example/api/1.0/api-1.0.jar` is looked up in
  the repository; `com/example/sandbox/tool/0.3/tool-0.3.jar` is not.
- The patterns apply to regular users only. Administrators see all items regardless of them.
- On a virtual repository the patterns steer requests, typically with nesting: a virtual
  repository that aggregates only remote repositories can exclude the organization's own
  namespace, so requests for internal artifacts are refused there and can only be served by a
  local repository; a virtual repository can also hide artifacts from a group of consumers by
  excluding them by namespace, name or version.
- Unspecified: whether the patterns also block deployment, how they match format-specific names
  that are not paths (for example scoped npm package names), and any length limit. (The roughly
  1,500-character limit on combined patterns belongs to replication settings, see
  [replication-and-federation.md](replication-and-federation.md).)

Interactions: resolution order REPO-035. These repository patterns are a separate mechanism from
the include and exclude patterns of a permission policy (IAM-021 in
[identity-and-access.md](identity-and-access.md)).

### REPO-012 · Repository layout reference
Priority: **P1**

- Each repository references one named layout (REPO-046 to REPO-052) that tells the system how
  module information is encoded in artifact paths.
- When a repository is created in the UI, the system suggests the layout recommended for the
  chosen format.
- API default when the layout is omitted: the Maven 2 layout, "where applicable". An example
  configuration of a generic repository carries a flat "simple" layout instead.
- Local repositories: a layout is mandatory. Remote repositories: a layout is mandatory for the
  cache, and a different layout of the upstream can be declared in addition (REPO-051). Virtual
  repositories: optional.
- For Terraform local repositories the registry type (module or provider, REPO-032) selects the
  layout.
- Repositories that existed before configurable layouts were introduced were assigned the Maven 2
  layout automatically.

### REPO-013 · Project assignment and environments
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Project | The project the repository is assigned to. | none (example: an unassigned repository's configuration shows the project value `default`) |
| Environments (stages) | One or more environments the repository belongs to; environments group project resources such as repositories for management and policies. | UI: DEV when creating inside a project; API: an empty list for an unassigned repository (example) |

- Assigning a repository to a project requires at least one environment: the UI makes the stage
  mandatory when a repository is created inside a project, and the configuration schema marks
  the environment list as mandatory whenever a project is set (the exact error is unspecified).
- Applies to local, remote, virtual and federated repositories.
- Assignment is changed through the "assign to a project" row action (REPO-007) or by updating the
  configuration (REPO-054).
- The repository list can be filtered by project and by environment (REPO-007).

Interactions: projects, environments and project roles, see
[identity-and-access.md](identity-and-access.md); virtual repositories in projects REPO-045.

### REPO-014 · Sharing a repository with other projects
Priority: **P2**

- A repository assigned to a project can be shared with one or more other projects. Sharing
  grants the receiving project either read-only or read-and-write permission on the repository.
- Optional project-aware download requests: the receiving project connects to the shared
  repository through an internal remote repository of its own (REPO-015), so that downloads are
  attributed to the consuming project and that project's own security policies apply.
- The shared view (REPO-007) shows the sharing date, the sharing permission and the internal
  connection state (pending: shared with project-aware requests but not yet connected; connected:
  at least one internal remote repository is connected; not applicable: shared directly).
- UI location: repository row actions "share with projects", "connect to shared repository",
  "view internal remote repositories", "create another internal remote repository".
- The sharing operations of the API are unspecified here; see
  [identity-and-access.md](identity-and-access.md).

### REPO-015 · Project-scoped connection to a shared repository
Priority: **P2**

A remote repository in one project can point at a repository of another project that is shared
with it, instead of at an upstream URL.

| Setting | Meaning | Default |
|---|---|---|
| Project-scoped upstream | When on, the remote repository connects to a shared repository of another project rather than to a URL. | off |
| Upstream repository key | The shared repository to connect to; mandatory when the setting above is on; must be one of the project's connectable repositories. | none |
| Upstream project key | The project that owns the shared repository; returned by the system, read-only. | — |

- The upstream URL is not required for such a repository and is returned empty.
- An update can convert in both directions: switch the setting on together with an upstream
  repository key, or switch it off together with a URL. Switching it off without a URL is
  rejected; an upstream key outside the connectable set is rejected.
- The repository listing (REPO-057) returns the upstream repository key for such repositories and
  null for all others; the partial configuration returned to non-administrators (REPO-055) also
  contains it.

Operations:

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List connectable repositories of a project | project key; optional filters: target format, key search text, maximum number of results | list of repository keys usable as upstream repository key | project administrator of that project, or platform administrator |
| List project-scoped consumers of a repository | repository key; optional filters: consuming project key, maximum number of results | per connected project-scoped remote repository: its key, owning project key and project display name, a creation timestamp in milliseconds since the epoch, and its environments | platform administrator, or project administrator of the given project when the project filter is used |

- Both operations can answer 400 for an invalid request and "service unavailable, retry later"
  (503).
- Whether the consumer timestamp records the moment of sharing or the creation of the consuming
  remote repository is unspecified; the field is only named a creation time.

Interactions: the full behavior of project-scoped remote repositories (read-and-write shares,
unsharing, deletion of the shared repository) is RMT-047 in
[remote-repositories.md](remote-repositories.md); this entry covers only the settings and
operations as they appear in repository configuration.

## Local repositories

### REPO-016 · Local repository
Priority: **MVP**

- Stores content uploaded by users, clients and CI; serves it at the repository address
  (REPO-005); can be a member of virtual repositories and the default deployment target of a
  virtual repository (REPO-038); can be replicated or turned into a federated repository (see
  [replication-and-federation.md](replication-and-federation.md)).
- UI location: administration area, repositories section, "create a repository", class local,
  then the format. The form has a basic tab, an advanced tab and a replication tab (optional;
  enables event-based replication and adds replication targets).
- Settings common to every format are listed below; format-specific settings follow in REPO-022
  to REPO-032.
- The configuration also carries a per-repository signed-URL validity value (example: 90); its
  unit and meaning are unspecified (signed URLs: [artifacts.md](artifacts.md)).

<details>
<summary>Common local repository settings</summary>

| Setting | Meaning | Default | Entry |
|---|---|---|---|
| Format | Package type; fixed at creation. | generic (API) | REPO-002 |
| Repository key | Unique identifier. | — (mandatory) | REPO-004 |
| Project, environments | Project assignment and stages. | none; DEV in UI inside a project | REPO-013 |
| Layout | Layout used to store and identify modules. | Maven 2 (API), format-specific suggestion (UI) | REPO-012 |
| Public description | Visible to users with access. | empty | REPO-010 |
| Internal notes | Visible to administrators. | empty | REPO-010 |
| Include patterns | Paths the repository serves. | `**/*` | REPO-011 |
| Exclude patterns | Paths subtracted from the included set. | empty | REPO-011 |
| Property sets | Property sets available to artifacts of this repository. | empty | ART-026 in [artifacts.md](artifacts.md) |
| Priority resolution | Precedence in virtual resolution and metadata merge. | off | REPO-018 |
| Disable resolution | Repository ignored for resolution, download and deployment. | off | REPO-017 |
| Archive content browsing | Browse inside archives; risk of cross-site scripting through served content. | unspecified (example: off) | ART-045 in [artifacts.md](artifacts.md) |
| Download redirect | Redirect downloads (semantics in storage page). | off | REPO-019 |
| CDN download redirect | Redirect downloads to a CDN. | off | REPO-020 |
| Scanner indexing | Index content for vulnerability and license scanning. | off | REPO-021 |
| Checksum policy | Handling of client-supplied checksums on deploy. | verify against client checksums | REPO-022 |
| Handle releases | Accept release deployments. | on | REPO-023 |
| Handle snapshots | Accept snapshot deployments. | on | REPO-023 |
| Snapshot naming behavior | Naming of deployed snapshots. | unique | REPO-024 |
| Maximum unique snapshots | Cap on stored snapshots per artifact. | 0 (unlimited) | REPO-025 |
| Suppress descriptor consistency check | Skip the coordinate-versus-path check. | off | REPO-026 |
| Maximum unique tags | Cap on tags per container image. | 0 (unlimited) | REPO-027 |

</details>

### REPO-017 · Disable resolution
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Disable resolution | When on, the system skips this repository when resolving artifacts, and the repository accepts neither downloads nor deployments. | off |

- A local repository with this setting on is listed as inactive (REPO-007).
- Not offered for these formats: Hugging Face models, Debian, Terraform registry, Terraform state
  backend, Chef, Git LFS, Puppet, Vagrant.
- The same setting exists on remote and federated repositories; for remote repositories see
  [remote-repositories.md](remote-repositories.md).

### REPO-018 · Priority resolution
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Priority resolution | When on, the repository takes precedence over the configured order when a virtual repository resolves a request; and package metadata is merged only from repositories with this setting on. | off |

- Merge rule: if the requested package exists in one or more repositories with priority
  resolution, the virtual repository merges metadata only from those. If the package is found in
  none of them, metadata is merged from the repositories without the setting. As a consequence,
  an internally hosted package shadows a same-named package of a lower-priority (for example
  upstream) member in merged metadata.
- Applies to local, remote and federated repositories.
- Docker: priority resolution requires the virtual repository's "resolve tags by latest
  timestamp" setting to be on (REPO-043).
- Supported formats, open between two candidate sets (the spec decides):
  - Per the repository form: not offered for Cargo, Chef, Debian, Git LFS, Ivy, NuGet, Opkg, pub,
    Puppet, Swift, Terraform, Vagrant and Hugging Face models.
  - Per the configuration schema: applies to all formats except CocoaPods, Git LFS, NuGet v2
    (OData), Opkg, Cargo, Vagrant and version-control archives (for federated repositories also
    RPM).
  - Whether NuGet v3 supports the setting is therefore open.

Interactions: virtual resolution order REPO-035; metadata merge REPO-037.

### REPO-019 · Download redirect
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Download redirect | Per-repository switch to answer downloads with a redirect instead of streaming the content. | off |

The semantics (redirect target, conditions) are unspecified; see direct
download from object storage in [storage-and-blobs.md](storage-and-blobs.md). Exists on local,
remote and federated repositories.

### REPO-020 · CDN download redirect
Priority: **Out of scope**

A per-repository switch that redirects clients to download through a CDN operated as part of a
hosted (SaaS) offering; default off; not available for many formats. It has no meaning for a
self-hosted system.

### REPO-021 · Scanner indexing
Priority: **Later**

| Setting | Meaning | Default |
|---|---|---|
| Scanner indexing | Content of the repository is indexed by the vulnerability and license scanner. | off |

- Formats that can be indexed: Alpine, Bower, Cargo, Composer, Conan, Conda, CRAN, Debian,
  Docker, RubyGems, generic, Go, Gradle, Hugging Face, Ivy, Maven, npm, NuGet, PyPI, RPM, sbt,
  Terraform state backend.
- Formats that cannot: Chef, CocoaPods, Git LFS, Helm, Opkg, pub, Puppet, Swift, Terraform
  registry, Vagrant.
- Exists on local, remote and federated repositories. The configuration also carries a
  retention period for scan data next to this setting (example: 90); its unit and meaning are
  unspecified.

Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md).

### REPO-022 · Deployment checksum policy
Priority: **P1**

Applies to Maven, Gradle, Ivy and sbt local repositories in the UI; the configuration carries it
on every local and federated repository.

| Option | Behavior |
|---|---|
| Verify against client checksums (default) | The client's checksum for a deployed artifact is checked. If the client sent no valid checksum, a request for that checksum file is answered with 404. If the client checksum differs from the checksum the server computed, requests are answered with 409 until a correct checksum is deployed. |
| Trust server-generated checksums | Client checksums are not checked; the server's own checksums are used and the artifact is usable immediately, at the cost of integrity assurance. |

The remote-repository checksum policy (four options) is a different setting; see
[remote-repositories.md](remote-repositories.md). Checksums in general:
[artifacts.md](artifacts.md).

### REPO-023 · Release and snapshot handling
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Handle releases | Release artifacts may be deployed to this repository. | on |
| Handle snapshots | Snapshot (integration) artifacts may be deployed to this repository. | on |

- Shown for Maven, Gradle, Ivy and sbt; present on local, remote and federated configurations.
- The release and snapshot handling of the repositories also influences which repositories are
  searched for a request (REPO-035).

### REPO-024 · Snapshot naming behavior
Priority: **P1**

Central control over how snapshots are named when deployed, independent of client settings.

| Option | Behavior |
|---|---|
| Unique (default) | Snapshots get a unique, time-based version number. |
| Non-unique | Snapshots use the self-overwriting form `<artifact>-<version>-SNAPSHOT.<ext>`. |
| Deployer | The name sent by the deploying client is kept as is. No metadata is generated; do not use with replication, because each instance must generate its metadata locally. |

- The default changed from non-unique to unique in a past release, for local and federated
  repositories alike; whether repositories created before that change were converted is
  unspecified.
- Maven 3 clients no longer resolve or deploy non-unique snapshots; repositories still using them
  should switch to unique and remove old non-unique snapshots.
- Applies to local and federated repositories.

### REPO-025 · Maximum unique snapshots
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Maximum unique snapshots | Upper bound on how many distinct snapshot builds of one artifact the repository retains; an upload beyond the bound evicts the oldest stored snapshot without user action. | UI: blank; configuration schema: 0; both mean no limit |

Offered for Maven, Gradle, Ivy and sbt local repositories and for NuGet local repositories (where
0 also means "no limit, no cleanup"). Present on remote and federated configurations too.

Interactions: retention policies in [retention-and-cleanup.md](retention-and-cleanup.md); module
identification by layout REPO-046.

### REPO-026 · Descriptor consistency check
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Suppress descriptor consistency check | On deployment the system checks that the group, artifact and version declared in the POM match the deployment path and rejects the deployment on a mismatch; this setting turns the check off. | off (check active) |

Maven, Gradle, Ivy and sbt local repositories; present on remote and federated configurations.

### REPO-027 · Container tag limits and retention
Priority: **P1**

| Setting | Formats | Meaning | Default |
|---|---|---|---|
| Container API version | Docker | Docker registry API version 1 or 2 in the UI; OCI repositories do not support version 1, and the configuration API accepts only version 2. | version 2 |
| Maximum unique tags | Docker, OCI | Per image, the number of distinct tags kept in the repository; when exceeded, the oldest tags are removed. | blank or 0: all tags kept |
| Tag retention | Docker, OCI | How many earlier revisions of a tag are kept when the tag is overwritten by a new push. | OCI: 1, meaning overwritten revisions are not kept; Docker: unspecified |
| Block image manifest v2 schema 1 | Docker | Rejects pushes of images with the legacy schema-1 manifest. | unspecified (example: off) |

Full container behavior: [formats/README.md](formats/README.md).

### REPO-028 · Force authentication per repository
Priority: **MVP**

| Setting | Formats and classes | Meaning | Default |
|---|---|---|---|
| Force authentication | NuGet (local, virtual), Conan (local, virtual), Eclipse P2 (virtual), Maven (virtual only); the configuration schema additionally carries the NuGet, Conan and P2 switches on local and remote repositories | Every use of the repository requires HTTP basic authentication credentials. | off |
| Allow anonymous download and search | Cargo (local) | Cargo clients send no credentials when downloading or searching crates; this setting allows anonymous access to exactly those resources, overriding the global anonymous-access setting. | unspecified |

Interactions: anonymous access and client authentication, see
[identity-and-access.md](identity-and-access.md) and [access-tokens.md](access-tokens.md).

### REPO-029 · npm local repository settings
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Primary signing key | Name of the primary key pair of the repository; npm repositories use elliptic-curve (ECDSA) key pairs. | unspecified |
| Secondary signing key | Name of the secondary key pair. | unspecified |
| Enforce strict layout | Packages are stored and resolved by path; uploads that do not comply are stored but not indexed. | unspecified |

The npm protocol and the effect of strict layout are described in
[formats/README.md](formats/README.md).

### REPO-030 · NuGet local and virtual repository settings
Priority: **MVP**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Maximum unique snapshots | local | REPO-025. | 0 (no limit) |
| Force authentication | local, virtual | REPO-028. | off |
| Enforce layout (version normalization) | local, virtual | The repository refuses duplicate package versions under the NuGet version normalization rules (REPO-031), aligning the repository with the public NuGet gallery. | unspecified |

- Enforce layout can only be chosen when the repository is created; it cannot be switched on or
  off afterwards. Existing repositories are converted with the normalization migration
  (REPO-067).
- The NuGet v3 package content resource (the flat container that current NuGet clients restore
  from) is available only on repositories with enforced layout. Clients that speak only NuGet v2
  (for example Chocolatey or PowerShellGet) do not need it and can keep using non-normalized
  repositories.

### REPO-031 · NuGet version normalization rules
Priority: **MVP**

The NuGet client considers `2`, `2.0`, `2.0.0` and `2.0.0.0` equal. A normalized repository
therefore admits exactly one canonical spelling per release, which keeps a release from landing
under several file names. The canonical spelling has three or more dot-separated numeric parts
(`Major.Minor.Patch`), carries no leading zeros, drops a fourth part when it is `0`, and has no
build metadata.

| Input | Normalized form |
|---|---|
| `3.1.4` | `3.1.4` (unchanged) |
| `3.1.4.0` | `3.1.4` (zero fourth segment dropped) |
| `3.1.4.2` | `3.1.4.2` (non-zero fourth segment kept) |
| `3.1` | `3.1.0` |
| `03.01.04` | `3.1.4` (leading zeros dropped) |
| `3.1.4+ci.7` | `3.1.4` (build metadata dropped) |

- A valid semantic version is rewritten into normalized form. A package whose version is not a
  semantic version at all is rejected by a normalized repository.
- Build metadata is stripped, not rejected: a package file named with `+<metadata>` is stored
  under the name without it.
- Two versions that differ only in build metadata normalize to the same version; the second one
  deployed is rejected as a conflict.
- A non-normalized package cannot be uploaded, copied or moved into a normalized repository.
- Membership rule for virtual repositories: every member of a normalized virtual repository must
  itself be normalized, whereas a non-normalized virtual repository may mix both kinds.
- Paired repositories (replication source and target, federation members, a chained remote
  repository and its upstream on another instance) must share the setting: both normalized or both
  not. A normalized chained remote repository can only point at a normalized upstream, and it
  must be configured with administrator credentials so the system can verify the upstream's
  state.
- Restoring an archived NuGet repository into a repository with a different setting can lead to
  rejected packages.

Interactions: NuGet protocol, see [formats/README.md](formats/README.md); remote repositories, see
[remote-repositories.md](remote-repositories.md).

### REPO-032 · Other format-specific local repository settings
Priority: **P2**

Each row's real priority follows the priority of its format in
[formats/README.md](formats/README.md).

<details>
<summary>Format-specific local settings</summary>

| Format | Setting | Meaning | Default |
|---|---|---|---|
| Alpine | Signing key pair | RSA key pair that signs the Alpine index file. | unspecified |
| Cargo | Allow anonymous download and search | REPO-028. | unspecified |
| Cargo | Sparse index support | Serves the internal index following the Cargo sparse-index specification; the Git-based index is not supported. | unspecified |
| CocoaPods | URL base | Read-only display of the fixed URL prefix used to compute relative URLs. | read-only |
| Conan | Force authentication | REPO-028. | off |
| Debian | Primary and secondary GPG key | Names of the GPG keys the repository signs with. | unspecified |
| Debian | Legacy trivial layout | Uses the deprecated flat ("trivial") Debian layout; not supported for virtual Debian repositories. | off (example) |
| Debian | Index debug symbols | Also indexes debug-symbol packages (`.ddeb`). | unspecified |
| Debian | Additional index compression formats | Index formats produced besides gzip, which is always produced and cannot be turned off; values such as bzip2, lzma, xz. | none |
| Docker, OCI | REPO-027 | | |
| Hex | Signing key pair | RSA key pair that signs and encrypts content exchanged with the Mix client. | unspecified |
| Maven, Gradle, Ivy, sbt | REPO-022 to REPO-026 | | |
| npm | REPO-029 | | |
| NuGet | REPO-030 | | |
| Opkg | Primary and secondary GPG key | Signing keys. | unspecified |
| PHP Composer | Composer v1 indexing | Uses the v1 index; new repositories use the faster v2 index. | off (v2) |
| RPM | Primary and secondary GPG key | Signing keys. | unspecified |
| RPM | Metadata folder depth | Directory level below which the system looks for RPMs and writes the `repodata` directory. 0 is the repository root (whole repository indexed, `repodata` at the root). Depth 1 with a layout `<name>/<version>/<arch>/<file>` yields one `repodata` per package name, i.e. several YUM repositories inside one repository. Changing the depth of an existing repository can require re-indexing or moving packages and reconfiguring clients. | 0 |
| RPM | Automatic metadata calculation | Recomputes RPM metadata automatically after relevant changes at the configured depth. | off (example) |
| RPM | File-list indexing | Also produces the `filelists.xml` metadata. | off |
| RPM | Group file names | Comma-separated list of YUM group files; each `repodata` directory holds at most one group file, several groups are tags inside it. | none |
| Terraform registry | Registry type | Module or provider; selects the matching layout. | unspecified |
| Terraform state backend (inferred) | State encryption | Stored states are encrypted. The configuration schema lists this switch on every local repository without naming its format; the state backend is the only format that stores states. | on |

</details>

## Virtual repositories

### REPO-033 · Virtual repository
Priority: **MVP**

- A virtual repository gives clients one stable URL in front of an ordered list of members:
  local, remote and further virtual repositories, all of its own format. Members and their access
  rules can be swapped behind that URL without touching client configuration.
- It stores no artifacts of its own.
- UI location: administration area, repositories section, "create a repository", class virtual,
  then the format (fixed afterwards). Basic tab (mandatory settings and the member selection) and
  advanced tab. Members are moved from an "available" list to a "selected" list; the selected list
  can be reordered by drag and drop and shows the effective, flattened member list (REPO-034).
- Recommended practice: point build tools and package managers at a virtual repository rather
  than at a local one; order local repositories first and remote repositories last.

<details>
<summary>Common virtual repository settings</summary>

| Setting | Meaning | Default | Entry |
|---|---|---|---|
| Format | Package type; fixed at creation. | mandatory | REPO-002 |
| Repository key | Unique identifier. | — (mandatory) | REPO-004 |
| Project, environments | Project assignment and stages. | none; DEV in UI inside a project | REPO-013 |
| Layout | Layout for requests made against the virtual repository. | unspecified (optional) | REPO-051 |
| Public description, internal notes | Descriptions. | empty | REPO-010 |
| Include and exclude patterns | Paths the virtual repository answers for. | `**/*`, empty | REPO-011 |
| Included repositories | Ordered list of member keys. | — (open: mandatory or optional in the API, the spec decides; the UI requires a selection) | REPO-035 |
| Default deployment repository | Local repository that receives deployments. | none | REPO-038 |
| Requests from other instances may fetch remote content | Members' upstreams may be contacted for requests from other instances. | off | REPO-039 |
| Format-specific settings | | | REPO-037, REPO-040 to REPO-044 |

</details>

Interactions: remote members, see [remote-repositories.md](remote-repositories.md); deployment
through a virtual repository, see [artifacts.md](artifacts.md).

### REPO-034 · Nested virtual repositories
Priority: **P1**

- A virtual repository can include other virtual repositories.
- The effective member list resolves nested virtual repositories into their local and remote
  members; it updates automatically whenever a nested virtual repository changes.
- The system analyzes the composition and warns when a virtual repository cannot be resolved
  because of invalid nesting, such as a cycle. Whether a cycle is refused at save time is
  unspecified; only the warning is specified.

### REPO-035 · Virtual resolution order
Priority: **MVP**

- A request to a virtual repository is resolved in three tiers, regardless of where a member sits
  in the list:
  1. local repositories,
  2. caches of remote repositories,
  3. the remote repositories' upstreams.
- Inside each tier, members are tried in the order of the included-repositories list.
- The effective order is shown in the included-repositories list on the basic tab, nested
  members flattened.
- The order is further shaped by: the caller's permissions, the include and exclude patterns of
  the virtual repository and of each member (REPO-011), the members' release and snapshot handling
  (REPO-023), priority resolution (REPO-018, which takes precedence over the list order), and
  disabled members (REPO-017).
- Permission policies attach to local and remote repositories; virtual repositories cannot be
  named in a permission policy. What a caller can resolve through a virtual repository is
  therefore decided on its members (IAM-024).
- The in-tier order is also tied to the system-wide search order (REPO-068); how the virtual
  repository's own member list and that global order interact when they disagree is unspecified.

Interactions: permissions through virtual repositories, see IAM-024 in
[identity-and-access.md](identity-and-access.md).

### REPO-036 · Latest-version resolution across members
Priority: **MVP**

A "latest version" request is the exception to tier-first resolution (REPO-035): finding some
version in a local repository or a remote cache does not end the lookup. All members are asked,
the remote upstreams included, so the answer is the newest version anywhere. When the newest one
sits upstream, it is fetched into that remote repository's cache and served from there.

### REPO-037 · Merged metadata and virtual metadata cache period
Priority: **MVP**

- For formats with index or metadata documents, the virtual repository answers with metadata
  merged from its members (subject to priority resolution, REPO-018). A generic virtual repository
  keeps no metadata.

| Setting | Formats | Meaning | Default |
|---|---|---|---|
| Metadata cache period (seconds) | npm, Alpine, Chef, Conan, Conda, CRAN, OCI, Debian | How long merged metadata is cached before the members are checked for newer versions. 0 disables caching (for Debian: always check). | 7200; Debian: unspecified |

- The period applies to files that expire, i.e. metadata documents (for example Maven metadata,
  npm package documents, container manifests); release artifacts that never change are not
  affected. This scope is specified only for the Debian field; that it holds for the other
  formats is an assumption.
- The setting appears in the UI but not in the API's configuration schema.
- The merge algorithm per format is described in [formats/README.md](formats/README.md).

### REPO-038 · Default deployment repository
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Default deployment repository | The repository that receives artifacts deployed to the virtual repository's URL. | none |

Unspecified here: whether it must be a member, which classes are allowed, and what happens on a
deployment when no default is set. Deployment through virtual repositories is described in
[artifacts.md](artifacts.md).

### REPO-039 · Remote retrieval for requests from other instances
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Requests from other instances may fetch remote content | When on, other instances of the system that request through this virtual repository may cause it to fetch artifacts from its remote members' upstreams even if not cached; when off, such requests are served from caches only. | off |

Not applicable to Swift, Terraform and PHP Composer. Chained instances: see
[remote-repositories.md](remote-repositories.md).

### REPO-040 · External dependency rewrite
Priority: **P1**

| Setting | Formats | Meaning | Default |
|---|---|---|---|
| Rewrite external dependencies | npm, Bower | Dependencies that point to external sources are rewritten so the system fetches and caches them. | off |
| Remote repository for external dependencies | npm, Bower | The member remote repository in which fetched external dependencies are cached. | empty |
| External dependency allow list | npm, Bower, Go | Ant-style patterns of sources external dependencies may be fetched from; for example restrict to one host by replacing the default with that host name. | `**` (any source) |
| Follow go-import meta tags | Go | The system follows version-control roots announced in `go-import` meta tags to fetch remote modules. | on |

- In the API the rewrite switch and the allow list apply to npm, Bower and Go; the cache
  repository applies to npm and Bower only.
- The exact rewritten form of a dependency is unspecified. Format details:
  [formats/README.md](formats/README.md).

### REPO-041 · Descriptor repository-reference cleanup
Priority: **P1**

Maven, Gradle, Ivy and sbt virtual repositories can strip repository declarations from served POM
files, so that clients resolve only through the system.

| Option | Behavior |
|---|---|
| Discard active references (default) | Strips repository declarations at the POM's top level and inside profiles of that POM marked active by default; declarations in other profiles stay. |
| Discard any reference | Strips every repository declaration, regardless of the profile it sits in. |
| Nothing | The POM is served unchanged. |

### REPO-042 · Signing keys on virtual repositories
Priority: **P2**

| Setting | Formats | Meaning | Default |
|---|---|---|---|
| Signing key pair | Maven, Gradle, Ivy, sbt | Named key pair used to sign artifacts automatically. | unspecified |
| Primary and secondary key | Debian (and present in the general virtual configuration) | Names of the GPG keys the repository uses. | unspecified |

Interactions: the key stores these settings select from, see IAM-046 and IAM-048 in
[identity-and-access.md](identity-and-access.md).

### REPO-043 · Container tag resolution by latest timestamp
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Resolve tags by latest timestamp | When the same Docker tag exists in several members, the virtual repository returns the one with the newest timestamp. | unspecified |

Required for priority resolution on Docker (REPO-018).

### REPO-044 · Other format-specific virtual repository settings
Priority: **P2**

Each row's real priority follows the priority of its format in
[formats/README.md](formats/README.md).

<details>
<summary>Format-specific virtual settings</summary>

| Format | Setting | Meaning | Default |
|---|---|---|---|
| Alpine, Chef, Conan, Conda, CRAN, npm, OCI | Metadata cache period | REPO-037. | 7200 s |
| Conan | Force authentication | REPO-028. | off |
| Debian | Indexed remote architectures | Architectures indexed for the remote members; limiting them speeds up the first indexing. | amd64, i386 (example API value: `arm64,amd64`) |
| Debian | Additional index compression formats | As for local Debian repositories (REPO-032). | none |
| Debian | Index debug symbols | As for local Debian repositories. | unspecified |
| Debian | Legacy trivial layout | Not supported on virtual Debian repositories. | — |
| Debian | Primary and secondary GPG key | REPO-042. | unspecified |
| Docker | Resolve tags by latest timestamp | REPO-043. | unspecified |
| Go | go-import meta tags, allow list | REPO-040. | on, `**` |
| Maven, Gradle, Ivy, sbt | Descriptor reference cleanup | REPO-041. | discard active references |
| Maven, Gradle, Ivy, sbt | Signing key pair | REPO-042. | unspecified |
| Maven | Force authentication | REPO-028. | off |
| npm, Bower | External dependency rewrite | REPO-040. | off |
| NuGet | Force authentication, enforce layout | REPO-028, REPO-030. | off, unspecified |
| Eclipse P2 | Member selection | P2 virtual repositories choose members differently: a local repository (usually Maven or generic) plus upstream P2 URLs. | — |
| Eclipse P2 | Local repository | The local repository included in the virtual repository. | unspecified |
| Eclipse P2 | Path suffix | Optional folder within the chosen local repository that holds the P2 metadata files (content, artifacts, composite content and so on); empty means the repository root; for a repository deployed as one archive, the archive root followed by `!`, e.g. `site.zip!`. | empty |
| Eclipse P2 | Upstream P2 URL | URL of a remote P2 repository; the system analyzes its metadata and determines which remote repositories to create, because one P2 site can aggregate content from several hosts. | none |
| Eclipse P2 | Force authentication | REPO-028. | off |

</details>

### REPO-045 · Virtual repositories in projects
Priority: **P1**

- A virtual repository is assigned to a project and shared like any other repository (REPO-013,
  REPO-014).
- If members are neither assigned to nor shared with the virtual repository's project, the UI
  shows a warning with a list of the conflicting members, exportable as CSV.
- Which members are in scope is decided in the owning project of the virtual repository, not in
  the caller's project. A user from another project is therefore not recognized while members are
  resolved, even if each member is shared with that user's project, and a virtual repository
  shared across projects runs into permission conflicts. Recommended instead: every project
  defines its own virtual repository over the shared remote repositories.

## Repository layouts

### REPO-046 · Module identification from paths
Priority: **P1**

A layout lets the system derive module information from the path of a stored file, for any build
technology. Module information is assembled from these fields:

| Field | Meaning | Example | Mandatory |
|---|---|---|---|
| Organization | Literals that identify the publishing organization | `com.example` | yes |
| Module | Literals that identify the module | `http-client` | yes |
| Base revision | The version without any integration (snapshot) part | `4.2.0`; for `4.2-SNAPSHOT` the base revision is `4.2` | yes |
| Folder integration revision | The integration part of the version as it appears in folder names | for `4.2-SNAPSHOT`: `SNAPSHOT` | no |
| File integration revision | The integration part of the version as it appears in the file name | for `4.2-20240115.093012-7`: `20240115.093012-7` | no |
| Classifier | Literals that identify a variant of the artifact | `sources` | no |
| Extension | The file extension | `jar` | no |
| Type | The artifact type, for when the extension cannot serve as type | `java-source` | no |

### REPO-047 · Bundled layouts
Priority: **P1**

- The system ships predefined layouts that need no configuration: Ansible, Bower, a "build"
  layout (purpose unspecified), Cargo, Composer, Conan, Go, Gradle (the Gradle cache layout),
  Ivy (its default layout), Maven 1, Maven 2/3, npm, an npm test layout, NuGet, Puppet;
  Terraform module and provider layouts (REPO-032); and a flat "simple" layout.
- Layouts are defined at system level; any layout can be reused by any number of repositories.

### REPO-048 · Path pattern syntax
Priority: **P2**

A layout defines an artifact path pattern and, optionally, a descriptor path pattern. A pattern is
built from tokens, path separators (`/`), literals (such as `.` and `-`) and optional groups in
parentheses.

| Token (descriptive) | Stands for |
|---|---|
| organization, dotted | Organization with levels separated by dots, Ivy style (`com.example`) |
| organization, as path | Organization with levels separated by `/`, Maven style (`com/example`) |
| module | Module |
| base revision | Base revision |
| folder integration revision | Folder integration revision |
| file integration revision | File integration revision |
| classifier | Classifier |
| extension | Extension |
| type | Type |
| custom token | A named token defined by a regular expression, for cases the built-in tokens do not cover; for example a token that matches the word `BUILD` followed by a dash and one or more digits |

- The token syntax is modeled on Apache Ivy patterns (tokens in square brackets; custom tokens
  carry their regular expression inside the brackets).
- Optional groups: a sequence in parentheses may be absent. For example `module(-classifier)`
  matches both `http-client-sources` and `http-client`; `base revision(-file integration
  revision)` matches both `4.2-SNAPSHOT` and `4.2`.
- Any number of custom tokens is allowed. If the same custom token name appears several times,
  only the first occurrence's regular expression is used; later occurrences become a
  back-reference to the first match, whatever expression they carry.
- A layout with custom tokens should start with organization-as-path followed by module; without
  a module token some API operations do not work.
- The three mandatory fields (organization, module, base revision) should also appear in the
  directory part of the pattern, not only in the file name, for reliable matching.
- The descriptor path pattern is optional; without it, descriptor files (POM, `ivy.xml`) are
  matched with the artifact pattern. It is recommended when descriptors have a distinctive name.

Examples in descriptive notation (`<…>` is a token, `( … )` optional):

| Convention | Pattern |
|---|---|
| Maven artifact | `<org-path>/<module>/<base-rev>(-<folder-integ-rev>)/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Maven descriptor | same as the artifact pattern with the literal extension `.pom` |
| Ivy default artifact | `<org-dotted>/<module>/<base-rev>(-<folder-integ-rev>)/<type>s/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Gradle artifact | `<org-dotted>/<module>/<base-rev>(-<folder-integ-rev>)/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>` |
| Gradle descriptor | `<org-dotted>/<module>/ivy-<base-rev>(-<file-integ-rev>).xml` |

### REPO-049 · Integration revision expressions
Priority: **P2**

| Setting | Meaning |
|---|---|
| Folder integration revision expression | Regular expression that matches exactly the integration part of a version as used in folder names. |
| File integration revision expression | Regular expression that matches exactly the integration part of a version as used in file names. |

- The system wraps each expression into one capturing group; the expression itself must not
  contain capturing groups (use non-capturing groups), otherwise matching becomes unreliable.
- Examples: Maven folder revision `SNAPSHOT`; Maven file revision, either the constant or a
  timestamp plus build number such as `20240115.093012-7`:
  `SNAPSHOT|(?:(?:\d{8}.\d{6})-(?:\d+))`; Ivy default for both, a 14-digit timestamp: `\d{14}`.

### REPO-050 · Layout management
Priority: **P2**

- UI location: administration area, system settings, repository layouts.
- Operations: create a new layout; duplicate an existing layout as a starting point; edit; test a
  layout against a sample path, which shows the module fields the system extracts from it.
- Layout settings: name, artifact path pattern, descriptor path pattern (optional), folder and
  file integration revision expressions (REPO-048, REPO-049).
- API operations for layouts are unspecified; for configuration import and export, see
  [operations-and-monitoring.md](operations-and-monitoring.md).

### REPO-051 · Layouts per repository class and path translation
Priority: **P2**

| Class | Layout rule |
|---|---|
| Local | A layout is mandatory; it defines how artifacts are stored. |
| Remote | The cache layout is mandatory. Optionally the upstream's own layout can be mapped: if it differs from the cache layout, every request to the upstream is translated from the cache layout path to the upstream layout path. Example: an upstream in Maven 1 convention behind a cache in Maven 2 convention. |
| Virtual | Optional. When set, requests can be made in the virtual repository's layout; the system builds module information from the request path and translates it into each member's layout. |

The request path passes through to members unchanged when module information cannot be built from
it, when it cannot be mapped to a member (fields missing on either side), or when the virtual
repository or the member has no layout.

Interactions: remote repository settings, see RMT-001 in
[remote-repositories.md](remote-repositories.md).

### REPO-052 · Layout-based maintenance functions
Priority: **P2**

Module information from layouts enables:

- automatic cleanup of snapshot and integration versions (REPO-025);
- deletion of old versions;
- path conversion between a remote layout and a local layout;
- path conversion between two local layouts when copying or moving content (copy and move can
  suppress this translation, see [artifacts.md](artifacts.md));
- translation of virtual requests into member layouts (REPO-051);
- latest-version search based on layouts, see SRCH-017 in [search-and-query.md](search-and-query.md).

## Repository operations

All operations exchange JSON. Repository configurations use one document shape per class (local,
remote, virtual, federated); class-specific media types and plain JSON carry the same shape.
Errors are returned as a list of entries, each with an HTTP status and a message. Clients are told
to tolerate unknown properties, because new settings may be added at any time. The full operation
catalog is in [api-content-operations.md](api-content-operations.md).

### REPO-053 · Create repository
Priority: **MVP**

| | |
|---|---|
| Inputs | Repository key (in the address); configuration document with class (mandatory), format and any settings of REPO-010 to REPO-045 and of [remote-repositories.md](remote-repositories.md). |
| Outputs | Success with a plain-text confirmation (not JSON). |
| Privilege | Administrator. |
| Errors | 400: a repository with the key already exists, invalid configuration, or an invalid upstream for a project-scoped remote repository (REPO-015); 401: invalid credentials; 403: not an administrator; 500. |

- Settings missing from the document take their defaults.
- There is no upsert: creating an existing key fails.
- A local configuration document that carries a non-empty list of federation members creates a
  federated repository while the document's class stays local; see
  [replication-and-federation.md](replication-and-federation.md).
- A remote repository that proxies another instance, and the CDN switch (REPO-020), are also set
  through this operation; see [remote-repositories.md](remote-repositories.md).

### REPO-054 · Update repository configuration
Priority: **MVP**

| | |
|---|---|
| Inputs | Repository key; a partial or full configuration document. |
| Outputs | Success with a plain-text confirmation. |
| Privilege | Administrator. |
| Errors | 400: invalid configuration, invalid project-scoped upstream, or project-scoped mode switched off without a URL; 401; 403; 404: repository not found; 500. |

- Only the supplied settings change; optional settings may be left out.
- The class cannot be changed. The format is fixed as well (REPO-002).
- Converts a remote repository to or from project-scoped mode (REPO-015).

### REPO-055 · Get repository configuration
Priority: **MVP**

| | |
|---|---|
| Inputs | Repository key. |
| Outputs | The configuration document of the repository's class, including every setting the system knows, also those irrelevant to the repository's format. |
| Privilege | Administrators receive the complete configuration; other users with access receive a partial configuration (which settings it contains is unspecified, except that it includes the upstream key of a project-scoped remote repository). |
| Errors | 401; 403: no access to this configuration; 404; 500. |

For a project-scoped remote repository the URL is empty and the upstream repository key is set.

### REPO-056 · Get format-filtered repository configuration
Priority: **P1**

Same inputs, privileges and errors as REPO-055, but the output contains only the settings that are
relevant to the repository's format.

### REPO-057 · List repositories
Priority: **MVP**

| | |
|---|---|
| Inputs | Optional filters: class (local, remote, virtual, federated, release distribution), format, project key. Without filters all repositories are listed. |
| Outputs | A list of minimal records: key, class, description, URL (REPO-005), format, and the upstream repository key of project-scoped remote repositories (null otherwise). |
| Privilege | Any caller, including anonymous where anonymous access is enabled. Whether the list is narrowed to repositories the caller can read is unspecified. |
| Errors | 401; 403; 500. |

- The output writes the class in capitals and the format name with a leading capital, unlike the
  lower-case values in configuration documents.
- The specified set of format filter values lacks npm and several other formats (REPO-006);
  whether the filter accepts them anyway is unspecified.

### REPO-058 · Get all repository configurations
Priority: **P1**

| | |
|---|---|
| Inputs | Optional filters: format and class, each accepting several comma-separated values. |
| Outputs | One object with a list of full configuration documents per class: local, remote, virtual, federated, release-set storage. |
| Privilege | Administrator. |
| Errors | 401; 403; 500. |

### REPO-059 · Get repository configurations by key
Priority: **P1**

| | |
|---|---|
| Inputs | A list of repository keys (mandatory). |
| Outputs | A map from repository key to its configuration document. |
| Privilege | Administrators receive complete configurations; other users partial ones. |
| Limits | 100 keys per request by default; the limit is configurable. |
| Errors | 400: no keys given, or the limit exceeded; 401; 403; 500. |

### REPO-060 · Check repository existence
Priority: **P2**

| | |
|---|---|
| Inputs | One or more classes (repeatable); optional project key. Without a project key all repositories of all projects are checked. |
| Outputs | Whether any repository of the given classes exists in the given scope; the classes that were evaluated; the project scope used. |
| Privilege | Administrator. |
| Errors | 401; 403; 500. |

### REPO-061 · Create repositories in a batch
Priority: **P1**

| | |
|---|---|
| Inputs | A list of configuration documents; every entry must carry its key; classes and formats may be mixed; each entry follows REPO-053 for its class, including project-scoped remote repositories. |
| Outputs | Created (201) with one plain-text line per repository. |
| Privilege | Administrator. |
| Errors | 400: batch size limit exceeded, a key missing, an invalid configuration or an invalid project-scoped upstream; 401; 403; 409: key conflict; 500. |

- All or nothing: one invalid entry fails the whole batch.
- The batch size limit is unspecified.

### REPO-062 · Update repositories in a batch
Priority: **P1**

| | |
|---|---|
| Inputs | A list of partial or full configuration documents, each with its key; mixed classes and formats allowed; each entry follows REPO-054. |
| Outputs | Success, possibly with a plain-text confirmation. |
| Privilege | Administrator. |
| Errors | 400: batch size limit exceeded, a key missing, invalid project-scoped settings; 401; 403; 404: no repositories found for the given keys; 500. |

- All or nothing: one invalid entry fails the whole batch.
- Unspecified: the batch size limit, and whether a single unknown key among known keys yields 404
  or fails validation otherwise.

### REPO-063 · Delete repository
Priority: **MVP**

Removes the repository configuration together with all of its content. Applies to local, remote,
virtual and federated repositories.

| | |
|---|---|
| Inputs | Repository key. |
| Outputs | A deletion report: repository key, status message, number of artifacts deleted in this attempt, success flag; failure reports add the total number of failed artifact deletions and a sample of failures (each with an HTTP status and a message). |
| Privilege | Administrator. |

| Status | Meaning |
|---|---|
| 200 | Configuration removed; every artifact deleted. |
| 202 | A deletion of this repository is already running (deletion lock held); success-shaped report; retry later. |
| 207 | Some artifacts could not be deleted; the configuration is kept; delete the remaining artifacts and retry. |
| 400 | Invalid key: blank, or a protected internal repository (trash can, support bundles). |
| 403 | No permission, a validation failure, or every artifact deletion failed. |
| 404 | No such repository. The body is success-shaped with the success flag set; rely on the status code. |
| 409 | An import into the repository is still running, or a server-side extension hook vetoed the deletion (before-repository-delete event). |
| 424 | Detaching a federated repository from its members on other instances failed. |
| 500 | Unexpected failure, or the deletion lock could not be acquired. |

- The failure sample is capped (50 entries by default, configurable); the total count is always
  exact.
- Validation and policy failures carry an empty failure sample; per-artifact failures fill it.
- A typical per-artifact failure is an artifact protected against deletion by a marker property;
  each such artifact appears in the sample with status 403.

Interactions: extension hooks, see [events-and-extensibility.md](events-and-extensibility.md);
trash can, see [artifacts.md](artifacts.md); federation, see
[replication-and-federation.md](replication-and-federation.md).

### REPO-064 · Delete repositories in a batch
Priority: **P1**

| | |
|---|---|
| Inputs | A non-empty list of non-empty repository keys; order preserved. |
| Outputs | An overall status message and one REPO-063 report per repository in request order; or, on fail-fast validation, only the overall status message. |
| Privilege | Administrator. |

| Status | Meaning |
|---|---|
| 200 | Every repository removed, including the case where every listed configuration was already missing (per-repository success reports). |
| 207 | Mixed results. |
| 400 | Empty or missing key list, or the batch was rejected by early validation before any repository was touched (status message only). |
| 401 | Authentication failed (ordinary error list, not a report). |
| 403, 409, 424, 500 | Every repository failed; the status is taken from the failures. A 403 can also be a fail-fast rejection (for example when the list contains the build-metadata repository) with a status message only. |

- Best effort per repository: one failure does not stop the others.
- Fail-fast validation covers, for example, an empty list, the trash can, the support-bundle
  repository and the build-metadata repository.

### REPO-065 · Shared remote configuration (deprecated)
Priority: **P2**

| | |
|---|---|
| Inputs | Key of a remote repository. |
| Outputs | The shareable configuration of the remote repository (at least key and URL); values are sanitized for non-administrators. |
| Privilege | Any valid user for a "shared" remote repository, administrator otherwise. Presumably "shared" means the remote setting that shares its configuration (default off; see [remote-repositories.md](remote-repositories.md)); that link is an assumption. |
| Errors | 401; 403; 404; 500. |

Deprecated in favor of REPO-055; kept for clients that still call it.

## Special-purpose repositories and tools

### REPO-066 · Release-set storage repository
Priority: **Later**

- When a release set is created and signed, its artifacts are copied automatically into a
  separate release-set storage repository, where they cannot be edited or deleted. They stay
  available for distribution even when the originals are removed from their source repositories.
- Storage path convention: `<release set name>/<release set version>/<target path>`.
- One such repository exists by default and is created automatically; administrators can create
  more, with their own naming. The UI creates release sets only in the default repository; others
  are used through the API.
- Settings: repository key, public description, internal notes, include and exclude patterns
  (as REPO-004, REPO-010, REPO-011).
- UI location: administration area, repositories section, "create a repository", release-set
  class.

Interactions: [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### REPO-067 · Format normalization migration
Priority: **P2**

A guided migration that brings an existing NuGet repository created without enforced layout
(REPO-030) into the normalized state (REPO-031). NuGet is the only migration type today; others
may follow.

- UI location: administration area, system settings, migration tool. A table lists migrations,
  filterable by all, in progress (running dry runs, finished dry runs, running migrations) and
  migrated; row actions: migrate, run a new dry run, view the saved configuration, download the
  last report; further column and filter customization.
- Privilege: administrator or project administrator.
- Flow: create a migration and pick the source repository (only repositories of the eligible
  format that are not already part of another migration are offered) → the migration is saved and
  a dry run starts (asynchronous; the user can leave and return through the table) → review the
  dry-run counts and download the full report → optionally fix packages and repeat the dry run →
  set up the migration → confirm twice.

| Class | Dry run reports | Migration behavior |
|---|---|---|
| Local | total packages, already normalized, failing normalization | Non-compliant packages are moved, unchanged and under their original names, into an isolation repository: either created automatically or an existing one chosen by the user. If every package is non-compliant, the repository ends up empty. |
| Remote | as local | Completes immediately: the repository is backed up, then its cache is cleared. No isolation. |
| Chained remote (upstream is a repository on another instance) | whether the upstream is already normalized | The upstream must be migrated first; the migration can be saved for later and resumed; then completes immediately. Needs the administrator credentials configured on the remote repository. |
| Virtual | counts of member repositories: total, normalized, not normalized | Moves no packages. Marks the virtual repository normalized only when every member is normalized; otherwise nothing changes and the report names the members to migrate first. Per member: create a migration, view an existing one, download its last report. |

- While a migration runs, the repository is unavailable to clients, CI pipelines and virtual
  repositories; builds that depend on it fail.
- After a successful migration the NuGet v3 package content resource is enabled and the
  normalized layout is enforced.
- Duplicates (two versions that normalize to the same string) are not detected during migration;
  both are moved to isolation and collide only on re-upload. Suggested resolution order: prefer
  first-party over third-party packages, then the package of the repository's primary owning
  team, then verified publishers, then the most recently published, else keep both in isolation.
- Paired repositories (replication, federation, chained remote) must be migrated together or
  unpaired first (REPO-031).

## System-wide settings

### REPO-068 · System-wide repository search order
Priority: **P2**

- UI location: administration area, the system-wide repositories configuration screen. The
  administrator moves local, remote and virtual repositories into a selected list and arranges
  them; the list position is the sequence in which repositories are consulted when artifacts are
  resolved.
- The effective order is further shaped by the caller's permissions, include and exclude
  patterns (REPO-011) and release and snapshot handling (REPO-023), as for virtual repositories
  (REPO-035).
- Unspecified: which requests consult this order (it is referred to only for the order inside a
  virtual repository's tiers, REPO-035), whether repositories absent from the selected
  list are consulted at all, and whether an API operation exposes it.

## Notes for implementers

- The update operation cannot change a repository's class, and the format is fixed at creation;
  the only class change is the dedicated local-to-federated conversion and back. Create is never
  an upsert (an existing key fails with 400), and create and update answer with plain text, not
  JSON.
- An empty include pattern means "everything", and deleting the include-everything row in the UI
  does not remove it. Administrators bypass include and exclude patterns entirely.
- Virtual resolution is tier-first (locals, then remote caches, then upstreams); the member list
  orders only within a tier. Latest-version requests always consult the upstreams.
- Priority resolution changes metadata merge, not only lookup order: flagged members alone
  contribute metadata when they hold the package.
- Batch create and batch update are all-or-nothing; batch delete is best-effort per repository.
  A single delete with partial artifact failures keeps the configuration (207), and a delete of a
  missing repository returns 404 with a success-shaped body.
- Several boolean settings are written as the strings `"true"`/`"false"` in the configuration
  schema and typed inconsistently (string in one class, boolean in another); accept both.
  Clients expect unknown properties to be tolerated.
- The list operation spells class and format in different letter case than configuration
  documents.
- Key limits differ per class (remote 58, container formats 63, others 64); container formats
  forbid upper case and underscores; the `-cache` suffix is reserved.
- NuGet enforced layout is a creation-time decision; build metadata is silently stripped,
  non-semantic versions are rejected, and normalized and non-normalized repositories must not be
  paired.
- A virtual repository resolves in the context of its owning project, and permission policies
  never name virtual repositories: authorization is decided on the members.
