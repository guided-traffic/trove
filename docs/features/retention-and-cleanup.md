# Retention, cleanup and archiving

This page covers everything the system does to bound the growth of stored content over time:
policy-driven cleanup of stale packages, builds and release sets; policy-driven archiving of stale
packages to a separate, cheaper archive tier and their search and restore; the older artifact-level
archive policies with expiry inside the archive; the shared machinery all of these use (scope
patterns, time, property and version conditions, cron schedules, run limits, dry runs, run history
and CSV reports); and the per-repository and per-publish retention mechanisms that act without a
policy (maximum unique snapshots and tags, kept revisions of overwritten tags, unused-cache expiry
on remote repositories, build retention parameters per build name). Deleting a single item by hand, the trash can itself and garbage
collection of unreferenced binaries are described in [artifacts.md](artifacts.md) and
[storage-and-blobs.md](storage-and-blobs.md); this page describes only how retention interacts with
them.

## Policy framework

### RET-001 · Retention policy model
Priority: **P1**

A retention policy is a named, stored rule set that a background job evaluates against the
system's content and that, on each run, either deletes the matching items (cleanup policy) or moves
them to an archive tier (archive policy). Three cleanup policy kinds exist, distinguished by the
item type they act on, and one archive policy kind:

| Policy kind | Acts on | Unit of evaluation |
|---|---|---|
| Package cleanup policy | Packages in local and federated repositories | One package version, judged by its lead file (RET-009) |
| Build cleanup policy | Build records, optionally with their artifacts | One build run (name + number) |
| Release-set cleanup policy | Release sets | One release-set version |
| Package archive policy | Packages in local and federated repositories | One package version, judged by its lead file |

An older, artifact-level archive policy with its own model is described in RET-034.

Every policy has the same lifecycle: it is created **disabled**; it can be dry-run while disabled
(RET-015); it must be enabled (RET-013) before it runs on its schedule (RET-011) or can be started
manually (RET-014); a running policy can be stopped (RET-016) but not edited or deleted (RET-019).

Common settings of every policy kind:

| Setting | Meaning | Default |
|---|---|---|
| Policy key | Identifier of the policy, chosen by the creator; at least three characters from letters, digits, underscore and hyphen. Unique; creating a second policy with an existing key is rejected as a conflict. | — (mandatory) |
| Description | Free text explaining why the policy exists; up to 256 characters for cleanup policies. | empty |
| Schedule | Cron expression (RET-011). Empty means the policy only runs manually. | empty |
| Maximum run duration | Upper bound for one run in minutes (RET-012). | unspecified |
| Enabled | Whether scheduled and manual runs are allowed. Must be false on creation. | false |
| Skip trash can | Delete permanently instead of moving deleted items to the trash can (RET-038). Not offered for release-set cleanup. | unspecified (off for the older archive, RET-034) |
| Owning project | Set for a project-level policy; empty for a global policy (RET-002). | empty (global) |

Each kind adds its own scope and condition settings (RET-022, RET-023, RET-024, RET-027).

Item-type filter values used by list and view operations: package, build, release set.

Cleanup and archive policies run only on full instances; an edge instance (see
[distribution-and-lifecycle.md](distribution-and-lifecycle.md)) cannot host them and has its own,
separate retention model for received release sets (RET-039).

UI location: administration area, "retention policies" page, with a "create policy" menu offering
cleanup policy (then package, release set or build) and archive policy; the page has a cleanup
section and an archive section, each with a policies tab and a runs tab (the archive section adds a
restored tab).

Interactions: evidence attached to a cleaned package, build or release set is deleted with it
(evidence: see [distribution-and-lifecycle.md](distribution-and-lifecycle.md)); deletions in a
local repository that push-replicates are replicated to the targets (RET-026).

### RET-002 · Global and project-scoped policies
Priority: **P2**

Policies exist at two levels:

- **Global policy**: created by a system administrator (see IAM-002 in
  [identity-and-access.md](identity-and-access.md)); may span several or all projects and
  repositories that belong to no project. Only a system administrator manages it.
- **Project policy**: created by a project administrator (or a system administrator) for one
  project; it can select only that project's resources.

Project selection settings (visible only when a system administrator creates a global policy;
mandatory when visible):

| Setting | Meaning | Default |
|---|---|---|
| Include all projects | Apply to every project. | false |
| Included projects | Explicit project keys. A reserved key denotes "repositories not assigned to any project". | empty |

Stage selection settings (package cleanup policies; the archive policy form shows them too, but the
archive operations carry no stage fields):

| Setting | Meaning | Default |
|---|---|---|
| Include all stages | Apply to repositories in every lifecycle stage. When true, the stage list must be empty. | unspecified |
| Included stages | Explicit stages; when "include all stages" is false at least one stage is required. | unspecified |

Visibility and authorization rules:

- A project administrator sees the global policies that affect the project but cannot change them.
- Run results of global policies are visible only to system administrators.
- A project administrator can search only inside the project context.
- Listing policies: a system administrator may filter by project; without the filter the result
  contains global and project policies. A project administrator must pass the project filter.
- "Disable all policies" and "stop all runs" are available only to system administrators in the
  global context (RET-016, RET-017).
- Users who are neither system nor project administrators have no access to any retention function.
- A system administrator delegates retention management by making a user an administrator of the
  project (project settings, project administrators).

Interactions: project assignment and stages of repositories, see REPO-013 in
[repositories.md](repositories.md).

### RET-003 · Repository and path scope
Priority: **P1**

Package cleanup and package archive policies select repositories and, optionally, folders:

| Setting | Meaning | Default |
|---|---|---|
| Package types | One or more package formats the policy acts on. At least one is required. | — (mandatory) |
| Included repositories | One or more repository keys or patterns; an "all repositories" choice exists. At least one entry is required. | — (mandatory) |
| Excluded repositories | Keys or patterns removed from the included set. | empty |
| Included paths | Folder paths inside the selected repositories; narrows the scope. | empty (whole repository) |
| Excluded paths | Folder paths whose packages are left alone. | empty |

Path scope is only partly specified. Both the cleanup form and the archive form list included and
excluded path patterns. Neither the create operation for cleanup policies nor the one for archive
policies specifies a path field, though. The archive form also says, next to the repository
selector, that no path inside a repository can be given. The only specified behavior for path
patterns is the last-folder rule below, and it applies to cleanup. Whether archive policies
honor path patterns is unspecified. A compatible implementation offers path patterns for package
cleanup in both the UI and the API.

Pattern syntax: an explicit name, or a `*` wildcard at the end (any suffix) or at the start (any
prefix). Regular expressions are not supported.

Limits and edge cases:

- Only **local and federated** repositories are eligible. Remote and virtual repositories are never
  processed (remote caches have their own expiry, RET-036).
- An included path that names only the last folder (the folder directly holding the files) matches
  nothing, even if the full path contains that folder name; at least one parent folder must be part
  of the pattern.
- The UI offers auto-completion of repository names filtered by project and package type.
- A repository with its own retention settings (maximum unique snapshots or tags, RET-035) can lose
  packages that those settings would have kept if a policy also selects it.

### RET-004 · Package name and version selectors
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Included package name | Exactly one package name or name pattern; a match-everything value selects all packages. | — (mandatory) |
| Excluded package names | Explicit package names only; patterns are not accepted. | empty |
| Included package version | Exactly one version or version pattern (prefix or suffix wildcard). | all versions |
| Excluded package versions | Versions to keep out of the policy. | empty |

Pattern syntax as in RET-003. Edge case: excluded versions are either explicit versions only (as in
the UI) or also patterns such as `1.*` and `*.1` (as in the operation). A compatible implementation
accepts both explicit values and prefix or suffix patterns.

### RET-005 · Age and usage conditions
Priority: **P1**

Two time-based conditions select stale package versions:

| Condition | Meaning | Default |
|---|---|---|
| Created before (age) | The package version was created more than the given period ago, regardless of downloads. | unspecified |
| Not downloaded since (usage) | The last download of the package version is older than the given period. A version nobody ever downloaded qualifies only when it was created longer ago than the period. | 2 years (UI) |

Period units are days, weeks, months and years. One day is 24 hours counted back from the moment
the run executes; one week is 7 days, one month 30 days, one year 365 days. The UI normalizes
input to the largest exact unit (7 days is shown as 1 week). Stored values are in days; an older
month-based representation exists for compatibility and is superseded by the day-based one.

The usage condition is the recommended one for avoiding deletion of packages still in use. The
download timestamp is the one kept by download statistics (see ART-029 in
[artifacts.md](artifacts.md)); for federated repositories the run consults the last-download state
on every federation member, which makes runs slower the more members exist (RET-025).

Of the time conditions, build cleanup and release-set cleanup support only the age condition
(RET-023, RET-024).

### RET-006 · Property conditions
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Included property | Key and one or more values; only package versions whose lead file carries the property qualify. | none |
| Excluded property | Key and one or more values; package versions whose lead file carries the property are never selected. | none |

Properties are evaluated on the lead file only (RET-009), not on every file of the package. Property
model: see ART-021 in [artifacts.md](artifacts.md).

### RET-007 · Keep last N versions
Priority: **P1**

Shields the newest N versions of every package from the policy.

| Setting | Meaning | Default |
|---|---|---|
| Versions to keep | Number of latest versions per package that are never selected. | unspecified (condition off) |

Behavior:

- "Most recent" is determined by creation date, not by version-number ordering.
- Evaluated independently per package. Included paths merely pick the packages under
  consideration; they do not pool the versions of several packages into one count. Newer versions of a sibling
  package under the same folder never count against another package.
- The latest version of a package is always kept, whatever the value.
- Combined with other conditions it acts as a safety rail: a version among the last N is kept even
  if it matches every time and property condition.
- Not available for the generic format (RET-009).

Edge cases:

- Helm 3.18 and later push two manifest nodes per chart version (one for the tag, one for the
  digest) into any OCI-compliant repository. Because each manifest node counts as a version, the
  kept set can end up holding digest entries rather than the tags that consumers pull by.
- Debian packages uploaded to the repository root: the version condition may select all of them;
  the age condition is unaffected. Uploading to a subfolder avoids it.

### RET-008 · Combining conditions
Priority: **P1**

A package policy takes one or more conditions from {created before, not downloaded since,
property, keep last N}. All selected conditions combine with logical AND:

1. A version must satisfy every selected time condition and every included property.
2. It must not carry an excluded property.
3. It must fall outside the per-package kept set of the "keep last N" condition.

Examples: with "created before" and "not downloaded since" both selected, a version that was never
downloaded is selected only once its age exceeds the "created before" period. A policy with
"created before 90 days", the property `lifecycle=deprecated` and "keep last 3" selects a version
only if all three hold: it is more than 90 days old, it carries `lifecycle=deprecated`, and it is
not one of the three newest versions of its package.

### RET-009 · Lead file and format support
Priority: **P1**

Many formats store a package version as several files. A package policy judges the whole version by
one designated **lead file**: its creation time, last download time and properties decide, and if
the lead file qualifies the whole package version is deleted or archived. For a Maven version, for
example, the POM is the lead file.

<details>
<summary>Format support matrix for package cleanup and package archive policies</summary>

All listed formats support the age and usage conditions under both policy kinds. "Keep last N" is
supported by every listed format except generic, under both policy kinds. The archive policy
supports the same formats as cleanup except Chef and Puppet. Where no lead file is specified, the
cell says unspecified.

| Format | Lead file | Keep last N | Archive policy |
|---|---|---|---|
| Agent plug-ins | `.zip` | yes | yes |
| Alpine | `.tar.gz`, `.apk` | yes | yes |
| Ansible | `.tar.gz` | yes | yes |
| Bower | unspecified | yes | yes |
| Cargo | `.crate` | yes | yes |
| Chef | unspecified | yes | not listed |
| CocoaPods | `.tar.gz`, `.tgz` | yes | yes |
| Composer | unspecified | yes | yes |
| Conan | `conanfile.py` | yes | yes |
| Conda | `.tar.bz2` | yes | yes |
| CRAN | `.tar.gz` | yes | yes |
| Debian | `.deb`, `.ddeb` | yes | yes |
| Docker | image manifest | yes (see RET-010) | yes |
| RubyGems | `.gem` | yes | yes |
| Generic | unspecified | **no** | yes |
| Go | `.mod` | yes | yes |
| Gradle | `.pom` | yes | yes |
| Helm | `.tgz` | yes | yes |
| Helm OCI | image manifest | yes (see RET-010) | yes |
| Hex | unspecified | yes | yes |
| Hugging Face models and datasets | a generated model-info or dataset-info file | yes | yes |
| Machine-learning models | model manifest | yes | yes |
| Maven | `.pom` | yes | yes |
| Nix | `narinfo` | yes | yes |
| npm | `.tgz` | yes | yes |
| NuGet | `.nupkg` | yes | yes |
| OCI | image manifest | yes (see RET-010) | yes |
| Puppet | unspecified | yes | not listed |
| PyPI | `.tar.gz`, `.tgz`, `.gz`, `.whl`, `.egg`, `.bz2` | yes | yes |
| RPM | `.rpm` | yes | yes |
| sbt | `.pom`, `.xml` | yes | yes |
| Swift | unspecified | yes | yes |
| Terraform | `.zip` | yes | yes |
| Terraform state backend | `.json` | yes | yes |
| Vagrant | unspecified | yes | yes |

</details>

Edge cases: sbt packages uploaded with the plain default sbt layout instead of the recommended
layout are not resolved by cleanup or archive runs, so policies never select them. In archive run
reports, a package without version information (generic and similar) is reported with its creation
time (epoch) as version.

Interactions: format matrix in [formats/README.md](formats/README.md); layouts in
[repositories.md](repositories.md).

### RET-010 · Container images under package policies
Priority: **P1**

Applies to Docker, OCI and Helm OCI repositories.

- **Time conditions** treat every manifest and every manifest list independently, without
  regard to their hierarchy; each is judged by its own state. The usage condition is recommended
  so that images in use stay.
- **Keep last N** respects the image structure: a manifest list is deleted (or archived) together
  with all its sub-manifests, or not at all, so tags and multi-platform images never end up
  half-removed. It counts versions per image.
- Keep last N may be combined with time and property conditions in one policy; the last N are never
  touched.
- Separate policies with conflicting criteria on the same repository (one with keep last N, another
  with not-downloaded-since) are to be avoided; combine the conditions in one policy instead.
- With per-repository tag revision retention active (RET-035: kept copies of overwritten tags),
  the kept revisions of a tag go (are deleted or archived) only together with the tag's current
  revision.
- With a per-repository maximum of unique tags active (RET-035), the policy's keep-last-N value
  wins: keep last 2 and a maximum of 5 unique tags leave 2 tags of the image after a run.
- The usage condition depends on per-tag download data. Clients that resolve a tag once and then
  pull by digest credit the download to one tag path only, so sibling tags on the same digest look
  unused. An optional system-wide switch for Docker, OCI and Helm OCI copies download count, last
  download time and last downloader to every tag that shares the pulled digest. It applies only to
  pulls after it is enabled (no backfill), runs asynchronously, and updates only tags the puller may
  read. Default: unspecified.

Interactions: REPO-027 in [repositories.md](repositories.md).

## Scheduling and execution

### RET-011 · Cron schedule
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Schedule | Cron expression in the Quartz scheduler dialect: five or six fields; the optional leading field is seconds, followed by minutes, hours, day of month, month and day of week; `?` stands for "no specific value". Example: `0 30 3 * * ?` fires every day at 03:30. | empty (manual only) |

Rules:

- The minimum interval between two scheduled runs is 6 hours. An expression that fires more often is
  rejected when the policy is saved.
- An empty expression means no automatic runs; the policy runs only when started manually.
- Scheduled runs happen only while the policy is enabled.
- Open: the day of week is numbered either 0–6 with Sunday as 0, or 1–7 with Sunday as 1 as in the
  Quartz dialect (which also accepts day names); the spec decides. Accepting day names avoids the
  ambiguity.
- Open: either hourly schedules are allowed, or the 6-hour minimum applies; this page follows the
  minimum, which is the rule enforced on save.
- Off-hours schedules are recommended. Combined with the maximum run duration (RET-012), a schedule
  confines runs to weekends or maintenance windows.

### RET-012 · Maximum run duration and incremental processing
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Maximum run duration | Minutes after which a run stops even if not finished. At most 300 minutes (5 hours). | unspecified |

A run that reaches the limit stops; whatever it processed stays processed. Pending items are picked
up by the next scheduled run and the following ones, so a large backlog is worked off incrementally.

### RET-013 · Enable and disable a policy
Priority: **P1**

New policies are disabled. For package cleanup and package archive policies, a create request
that sets the enabled flag to true fails with an error. Build cleanup policies must be created
inactive; what a build cleanup create request that sets the flag returns is unspecified. The
release-set cleanup create operation has no enabled field. Enabling and disabling is a separate
operation.

- Enabled: scheduled runs and manual runs are allowed.
- Disabled: dry runs, editing and deleting are allowed; scheduled and manual runs are not.

Operation: set enabled state — input: policy key, enabled flag; output: success; privilege: system
administrator, or project administrator for a policy of that project. One such operation exists per
policy kind (package cleanup, build cleanup, release-set cleanup, package archive).

UI location: toggle in the "active" column of the policies tab. A dry run before enabling is the
recommended practice.

### RET-014 · Manual run
Priority: **P1**

Starts a run immediately, independent of the schedule ("run now"). The policy must be enabled.
The run appears in the run history with a flag that marks it as manually triggered and the user who
triggered it (RET-020).

UI location: policies tab, actions menu of a policy, "run".

Operation (one per policy kind; manual run and dry run share it):

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Run policy | Policy key; dry-run flag (default false) | Trigger ID of the started run; the run proceeds asynchronously (package cleanup answers "accepted", package archive answers "OK") | System administrator; project administrator for the project's policies |

The trigger ID identifies the run in the stop operation (RET-016), the run lookup (RET-020), the
report download (RET-021) and the run summary (RET-021).

### RET-015 · Dry run
Priority: **P1**

A dry run evaluates the policy exactly like a real run but changes nothing. Its result is a run
history entry flagged as dry run and a CSV report listing every item that a real run would delete or
archive (RET-021).

- Allowed on disabled policies; this is the intended way to validate a new policy before enabling it.
- The result reflects the moment of the dry run; a later real run may select different items.
- If the dry run selects nothing, no report is produced.

UI location: policies tab, actions menu of a policy, "dry run"; afterwards "download last report".

### RET-016 · Stop a run and stop all runs
Priority: **P1**

- **Stop one run**: stops the currently running execution of one policy. The run's status becomes
  "stopping" and then "stopped". A stopped run cannot be resumed; the next run starts from the
  beginning (already processed items stay processed).
- **Stop all runs**: stops every running cleanup run (packages, builds and release sets) in one
  operation; a separate operation stops all running archive runs. Global context only, system
  administrator only.

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Stop a cleanup run | Run ID (the trigger ID returned when the run was started, RET-014); an action value "stop" in the body | Accepted (asynchronous) | System administrator; project administrator for the project's policies |
| Stop all cleanup runs | none | Accepted (asynchronous) | System administrator |
| Stop a package archive run | Run ID (trigger ID); action value "stop" | Accepted | As for a cleanup run |
| Stop all package archive runs | none | Accepted | System administrator |
| Stop an older-archive run (RET-034) | Policy key | Accepted | System administrator |

UI location: actions menu of a policy ("stop"); policies tab and runs tab, "global stop/disable"
menu, "stop all runs" with a confirmation dialog.

Edge case: if a node shuts down unexpectedly during a run, the run is not marked as finished, and
the next run picks up the items that were left. For a planned restart, running policies are to be
stopped first. Both statements are made only for the older archive policies (RET-034); for the
other kinds they are unspecified.

### RET-017 · Disable all policies
Priority: **P1**

Sets every cleanup policy of all three kinds to disabled in one operation; a separate operation does
the same for archive policies. Afterwards policies are re-enabled one by one.

Cleanup operation input: an optional project key; with it, only that project's policies are
disabled, without it all global-level policies are disabled. The archive operation takes no input.
Privilege: system administrator; the UI offers the action only in the global context ("global
stop/disable" menu, confirmation dialog).

### RET-018 · Run concurrency, queuing and limits
Priority: **P1**

- Policies run concurrently up to a maximum; further triggered runs are queued; when the queue is
  full, further triggers are skipped. Queued and skipped runs do not appear in the run history; a
  skipped run is only logged (with a message naming the policy, the run number and the number of
  runs already in progress). The maximum and the queue capacity are unspecified; an example log
  message shows three concurrent runs.
- More than 10 simultaneous policy runs are not advisable.
- A run does not start while a mandatory maintenance task, for example garbage collection, is in
  progress; a manual run or dry run has to be retried later.
- One run deletes or archives at most 100,000 packages.
- No progress notifications are sent during a run.
- Progress and errors are written to the service log.

<details>
<summary>Tuning parameters of the cleanup engine</summary>

Node-level configuration, not per policy.

| Parameter | Meaning | Default |
|---|---|---|
| Records per report file | Maximum rows in one CSV report file; larger reports are split into several files in the archive. | 100,000 |
| Run-detail retention | Days that the per-item records of a run, used to build reports, are kept. Also bounds how far back run history reaches. | 30 |
| Run-detail purge interval | Days between runs of the job that purges expired per-item records. | 10 |
| Threads per run | Maximum worker threads one policy run uses. | 5 |
| Batch size | Maximum packages requested for processing at once. | 10,000 |
| Partition size | Items per group into which one batch is split during processing. | 1,000 |
| Error threshold | Percentage of failed items in one run after which the run is stopped. | 10 |
| Packages per run | Maximum packages one run processes. | 100,000 |

</details>

### RET-019 · Edit and delete a policy
Priority: **P1**

- A running policy can be neither updated nor deleted; the caller has to wait for the run to finish
  or stop it first.
- An update replaces the configuration of the policy identified by its key (operation inputs and
  outputs: as for create, RET-022 to RET-024). Whether the key itself can be changed is unspecified.
- Delete removes the policy; whether its run history and stored reports are removed is unspecified.
- Deletions selected by an edited policy in a push-replicating local repository are replicated
  (RET-026).

UI location: actions menu of a policy, "edit" (opens the policy form) and "delete" (confirmation
dialog).

## Run history and reports

### RET-020 · Policy list and run history
Priority: **P1**

**Policy list** (policies tab) columns: policy name, item type, global flag, project ("unassigned"
when the policy targets repositories without a project), last run status, last run time, next run
time, enabled. Actions per row: run now, dry run, edit, stop, download last report, delete. The
list can be searched by policy name, refreshed, and its columns chosen and reordered.

**Run history** (runs tab) columns: run ID, global flag, project, item type, policy name, start time,
processed and cleaned items with physical size, status, report availability. Newest run first.
Filters: policy name, project, start time range, status, and further options. The list can be
refreshed.

Run statuses:

| Status | Meaning |
|---|---|
| In progress | Running, no errors so far. |
| In progress with errors | Running, some items failed. |
| Completed | Finished without errors (also shown for dry runs, marked as dry run). |
| Completed with errors | Finished; some items failed; details in the log (also for dry runs). |
| Stopping | A user stopped the run; stopping is in progress. |
| Stopped | A user stopped the run. |
| Failed | The run failed. |

The run-history operation additionally knows three restore-related states (retrieval from the
archive tier pending, partially retrieved, retrieved); their exact meaning is unspecified.

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Policy overview | Item types (mandatory, one or more of package, build, release set; no duplicates), optional policy key, optional project key | For each policy: item type, key, next run time, enabled, project display name, included projects, and last-run data (status, time, dry-run flag, run ID, whether the report was downloaded, total items) | System or project administrator |
| List build cleanup policies | Optional project key | Same overview entries, build policies only | System or project administrator |
| Run history | Optional item types, time range (from, to), policy key, project key, list of project keys, statuses, "exclude runs without processed items", page, page size, sort field, sort order (ascending, descending) | Run entries (run ID, item type, policy key, project display name, start and end time, total items, cleaned items, cleaned size, status, triggered by, manual flag, dry-run flag, task ID, included projects) and pagination (page size, current page, last page, total) | System or project administrator |
| Run by trigger | Policy key, trigger ID | One run entry | System or project administrator |
| Archive policy overview | Optional item type (package), optional partial or full policy key | For each archive policy: item type, name, enabled, next run time, last-run data | System or project administrator |

Times are epoch milliseconds.

### RET-021 · Run reports
Priority: **P1**

Every run that touches at least one item, real or dry, produces a CSV report; a run that selects
nothing produces none. Reports are stored in a dedicated internal repository; deleting that
repository loses the report history, so it must not be removed. The download is a ZIP archive with
one or more CSV files (split at the per-file record limit, RET-018).

Operations (one set for cleanup runs, one for archive runs):

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Download run report | Policy key, run ID (both mandatory) | Binary ZIP archive; not found when no report exists | System administrator for any policy; project administrator for the project's policies (the archive operation checks whether the caller administers the policy's project; the UI grants project administrators report downloads for cleanup runs too) |
| Run summary | Trigger ID and policy key (both mandatory for cleanup runs; trigger ID only for archive runs) | Structured summary: policy (name, run ID, dry-run flag, status, triggered by); execution (start, finish, duration in milliseconds); processed items (item type, total, processed, failed, physical bytes, virtual bytes); run configuration (created and modified at and by, schedule with cron expression and maximum run duration in milliseconds, description, skip-trash-can flag, target with included and excluded repositories, package types, included and excluded packages, all-projects flag, included projects, and criteria with a human-readable age such as "30 days", usage in days, keep-last-N) | System administrator for any policy; project administrator for the project's policies for archive runs; unspecified for cleanup runs |

Timestamps in the run summary are date-time strings; elsewhere in run views they are epoch
milliseconds.

UI location: actions menu of a policy, "download last report"; runs tab, report icon per run
(states: available, selected, unavailable).

<details>
<summary>Report content per policy kind</summary>

All reports start with **run details** (policy name and run status, dry-run flag, start time,
duration) and a **configuration snapshot** of the policy as it was for this run (key, description,
schedule, maximum run duration, skip-trash-can flag where applicable, created by and at, last
modified by and at, project, included projects).

Package cleanup and package archive reports:

| Section | Content |
|---|---|
| Summary | Items deleted or archived; items processed; virtual size; physical size |
| Configuration | Package types; included and excluded repositories; included package name pattern; excluded package names; age, usage and keep-last-N values |
| Per item | Package name; version; size on disk; repository; repository type (local, federated); status; status message; path; created; modified; last downloaded |

Package status messages: successfully cleaned; cleanup failure (see log); package not found;
archived; dry run processed; dry run failed; restore states (restore not initialized, retrieved from the
archive tier, restored). Archive reports also explain skips (for example, the package changed since it was
archived, or it has already been restored).

Build cleanup report:

| Section | Content |
|---|---|
| Summary | Builds deleted; builds processed; virtual size; physical size |
| Configuration | Selected projects or all projects; included build names; age value; delete-artifacts flag |
| Per item | Build name; build number; created; created by; artifact count; artifact virtual size; associated release sets; status; status message |

Build status messages: deleted; partially deleted (X of Y artifacts deleted); error; skipped
(including "skipped because a release set references the build; set a release-set retention policy
first"); dry run completed; dry run failed; dry run error.

Release-set cleanup report:

| Section | Content |
|---|---|
| Summary | Release-set versions deleted; versions processed |
| Configuration | Included release-set names; age value; not-promoted-to environments |
| Per item | Name; version; created; created by; promoted-to environments; status; status message |

Release-set status messages: successfully cleaned; cleanup failure; dry run processed or failed;
distribution service not installed; never distributed (deleted); distribution attempted but always
failed (treated as never distributed); distributed successfully (not deleted); distribution state
could not be validated.

</details>

## Cleanup policies

### RET-022 · Package cleanup policy
Priority: **P1**

Deletes package versions in local and federated repositories that match the policy's scope and
conditions. Deleted versions go to the trash can unless the policy skips it (RET-038); metadata
derived from them is removed with them.

<details>
<summary>All settings of a package cleanup policy</summary>

| Setting | Meaning | Default |
|---|---|---|
| Policy key | RET-001 | — (mandatory) |
| Description | RET-001 | empty |
| Item type | Package. | package |
| Owning project | RET-002 | empty (global) |
| Include all stages / included stages | RET-002 | unspecified |
| Include all projects / included projects | RET-002 | false / empty |
| Package types | RET-003 | — (mandatory) |
| Included / excluded repositories | RET-003 | — (mandatory) / empty |
| Included / excluded paths | RET-003 | empty |
| Included package name / excluded package names | RET-004 | — (mandatory) / empty |
| Included package version / excluded package versions | RET-004 | all / empty |
| Created before | RET-005 | condition off |
| Not downloaded since | RET-005 | 2 years in the UI when selected |
| Included property / excluded property | RET-006 | none |
| Keep last N versions | RET-007 | condition off |
| Schedule | RET-011 | empty |
| Maximum run duration | RET-012 | unspecified |
| Skip trash can | RET-038 | unspecified |
| Enabled | RET-013; must be false on create | false |

</details>

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Create | Policy key; full configuration | Created policy (201); conflict if the key exists; bad request on invalid input | System administrator; project administrator for the own project |
| Get | Policy key; optional "check repository availability" flag (default false) | Policy configuration | Same |
| List | Optional project key (mandatory for project administrators) | All matching policies | Same |
| Update | Policy key; configuration | Updated policy; refused while running | Same |
| Delete | Policy key | Confirmation; refused while running | Same |
| Set enabled state | RET-013 | | Same |
| Run or dry run | RET-014, RET-015 | | Same |

UI location: retention policies page, create policy, cleanup policy, package.

Limits: RET-018. Edge cases: no check whether a package belongs to a build or a release set, so a
package cleanup can break later promotion of builds and release sets that reference it.

### RET-023 · Build cleanup policy
Priority: **P1**

Deletes build runs older than a given age, together with their build record and, by default,
their artifacts.

| Setting | Meaning | Default |
|---|---|---|
| Include all projects / included projects | RET-002 (mandatory in the operation) | — |
| Included builds | Build names, each with its project key (empty for global); `*` allowed only at the end; a leading `*` is invalid; an "all builds" choice exists. | — (mandatory) |
| Exclude builds referenced by release sets | Builds that a release set contains are not deleted. | unspecified (operation only) |
| Builds older than | Age by creation date, in days (an older month-based value exists). Units and 24-hour day definition as in RET-005. | — |
| Delete build artifacts | Also delete the artifacts the build produced. | on |
| Skip trash can | RET-038 | unspecified |
| Schedule, maximum run duration, enabled | RET-011 to RET-013 | — |

Artifact resolution when "delete build artifacts" is on:

1. If the build record carries the full repository path of each artifact, the artifacts are deleted
   at that original location.
2. Otherwise artifacts are found by checksum plus the build name, build number and build timestamp
   properties attached to them. Copies of those artifacts that were promoted or copied to other
   repositories are deleted as well.
3. Some CI tools write a build timestamp property that differs from the start time in the build
   record. Then the run checks whether the same build name and number exist in another project; if
   yes, the artifacts are not deleted; if no, they are deleted by checksum, build name and build
   number.

Operations: create, get, list (overview format, RET-020), update, delete, set enabled state, run or
dry run; inputs, outputs and privileges as in RET-022.

UI location: retention policies page, create policy, cleanup policy, build.

Interactions: build records in [builds-and-ci.md](builds-and-ci.md); retention parameters per
build name (RET-037); a build referenced by a release set is skipped and reported as such (RET-021).

### RET-024 · Release-set cleanup policy
Priority: **Later**

Deletes release-set versions older than a given age that were not promoted to selected
environments.

| Setting | Meaning | Default |
|---|---|---|
| Include all projects / included projects | RET-002 (both mandatory in the operation) | — |
| Included release sets | Release-set names, each with its project key (empty for global); a match-everything value selects all. | — (mandatory) |
| Release-set versions older than | Age by creation date in days, weeks, months or years (an older month-based value exists). | — |
| Not promoted to environments | Environments; a version promoted to any of them is excluded from deletion. A match-everything value excludes versions promoted anywhere. | — (mandatory list) |
| Exclude versions promoted to any environment | Boolean form of the previous setting. | unspecified |
| Schedule, maximum run duration, enabled | RET-011 to RET-013 | — |

No skip-trash-can setting exists for this kind.

Limits: release sets that have been distributed are never deleted; the sources of a release set
(builds, artifacts) are not deleted. A deleted release-set version cannot be found by trash-can
search and cannot be restored through the trash-can UI; it is restored through a dedicated
operation.

Operations: create, get, list, update, delete, set enabled state, run or dry run; as in RET-022.

Interactions: release sets in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### RET-025 · Cleanup and archiving on federated repositories
Priority: **Later**

Federated repositories are eligible for package cleanup and package archive policies. The run
determines the last download of each package across all federation members, so dry and real runs
take noticeably longer than on local repositories, growing with the number of members. Recommended:
narrower conditions and more frequent runs.

Behavior across federation members:

| Aspect | Behavior |
|---|---|
| Deletions propagate | Items a policy deletes in a federated repository are deleted on every member, not only where the policy ran. |
| Policies are local | Policy definitions are not synchronized between members; each member has its own. Only the resulting deletions propagate. |
| Run on one member | A policy for a federated repository is meant to run on exactly one member; running the same policy on several members at once produces duplicate deletion events. |
| Receive-only members | Members that only receive changes apply propagated deletions but do not run policies whose deletions would propagate outward. |
| Dry run is local | A dry run sees only what exists on the executing member; items not yet synchronized from other members are missing from it. |
| Recovery | Once propagated, a deletion can be undone only if the member that ran the policy has its trash can enabled (RET-038). |
| Trash can | Moving an item into the trash can counts as a deletion for federation. Emptying the trash can sends deletion events to all members. The trash-can switch is set per instance and is not synchronized. |

The older artifact-level archive (RET-034) does not synchronize archive and restore events with
federation members, and federation re-creates items it deleted; it is not to be used with
federated repositories.

Open: federated repositories either get a simplified policy form (age in days, a maximum number of
versions or tags, path include and exclude patterns, a dry-run option) with operation names of its
own, or use the policy operations in RET-022 to RET-027. The simplified conditions map onto RET-003,
RET-005 and RET-007. This page treats the policy operations as authoritative.

Interactions: federation in [replication-and-federation.md](replication-and-federation.md).

### RET-026 · Retention and push replication
Priority: **P2**

A cleanup or archive policy may select a local repository that push-replicates to other instances.
Such repositories are not excluded automatically, and the deletions the policy performs are
replicated to the replication targets. The UI warns about this on create and edit.

Interactions: replication in [replication-and-federation.md](replication-and-federation.md);
replicating download statistics (see that page) prevents the targets' own usage-based policies from
cleaning items that are in use on the source.

## Archive tier

### RET-027 · Package archive policy
Priority: **Later**

Moves stale package versions from the active instance to a paired archive instance (RET-028) backed
by low-cost storage, instead of deleting them. Settings, conditions, scheduling, limits, dry run,
stop, run history and reports are those of the package cleanup policy (RET-003 to RET-021) with
these differences:

- The form shows stage selection and path patterns. The operations have no stage fields, and path
  scope is unspecified for archiving (RET-002, RET-003).
- On a run, every selected package version is copied with its metadata (and, in current
  versions, its evidence) to the archive instance, then deleted from the active instance. If
  copying fails, nothing is deleted on the active side and the failure is logged.
- The skip-trash-can setting decides whether the deletion on the active side goes through the trash
  can (RET-038).
- On the archive instance, dedicated archive repositories are created on demand.
- The keep-last-N safety rail keeps the newest N versions on the active instance.

Operations: create (must be created disabled), get, list (optional project key, mandatory for
project administrators), update (refused while running), delete (refused while running), set enabled
state, run or dry run, stop one, stop all, disable all, download run report, run summary; privileges
as for cleanup policies.

UI location: retention policies page, create policy, archive policy; archive section with policies,
runs and restored tabs.

### RET-028 · Archive instance pairing
Priority: **Later**

The archive tier is a second, reduced instance of the system dedicated to holding archived content.

- Topology: exactly one active instance pairs with exactly one archive instance; several active
  instances sharing one archive instance is not supported, so every cluster that archives needs
  its own archive instance. The active instance must be a full instance, not an edge instance.
- Both must run with the same cloud provider and in the same region.
- Pairing: on the archive instance an administrator generates a one-time pairing token (valid 300
  seconds by default); on the active instance an administrator registers the archive instance and
  creates the binding with that token.
- Re-establishing trust, for instance after a pairing was revoked by accident, means running the
  pairing again. For the older archive (RET-034), trust is bound to a namespace that the archive
  instance generated at pairing. The repeated pairing request names that namespace, and an
  operation returns it.
- Unbinding is open: either unbinding an archive instance is not supported, or a binding can be
  revoked from the active instance; the spec decides. Either way a binding cannot be removed while
  the archive instance is unavailable.
  An archive instance cannot be converted into another instance kind.
- For the older archive, the archive instance's own UI is limited to browsing, searching, creating
  generic local repositories, managing administrators and groups, and monitoring storage.

UI location: administration, topology, deployment bindings, archive tab.

Interactions: pairing tokens in [access-tokens.md](access-tokens.md); multi-site topology in
[replication-and-federation.md](replication-and-federation.md).

### RET-029 · Archive search
Priority: **Later**

From the active instance, administrators search the archive for content to restore. Results show
each item's original repository.

| Criterion (packages) | Meaning |
|---|---|
| Original repository | Repository the package was archived from; wildcards allowed. |
| Archiving time | Time range in which the package was archived. |
| Package name pattern | Wildcards allowed. |
| Package version | Wildcards allowed. |
| Package type | One format. |

Free-text search by package type is also offered. For the older artifact-level archive (RET-034),
artifact search criteria are: original repository (wildcards), archiving time, artifact name pattern
(wildcards), checksum (MD5, SHA-1 or SHA-256, exact value, no wildcards), path, and properties.

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Search archived packages | Package type, repositories and name pattern (all mandatory), version, archived-from and archived-to (epoch milliseconds); optional project key (without it, the global scope) | Results (lead manifest file name, package type, package name, version, original repository, archiving time in epoch milliseconds) and a range (start position, end position, total) | System administrator; project administrator within the project |
| Search archived artifacts (older archive) | A query in the system's query language (see SRCH-024 in [search-and-query.md](search-and-query.md)); only the item domain is supported, not builds, archive entries, promotions or releases | Results and a range (start, end, total) | System administrator |
| Convert criteria to query (older archive) | Structured criteria form | Generated query text | System administrator |

UI location: global search, scope "archive", with an advanced filter switching between artifacts
and packages.

### RET-030 · Restore from the archive
Priority: **Later**

Restores archived items into the active instance. Restore is non-destructive: the archive copy
stays.

| Setting | Meaning | Default |
|---|---|---|
| Original location | Restore each item into the repository it was archived from. | — |
| Fallback location | Mandatory with "original location"; used when the original repository no longer exists. | — |
| New location | Restore every item into one given repository. | — |

Rules:

- The target path must exist before the restore; a target of the same package type as the
  restored package is recommended.
- One item or many selected items can be restored at once.
- The restore runs in the background and can take minutes to hours.
- An error fails the restore and is logged.
- Package archive tier: by default at most 10,000 artifacts (counted across the selected packages)
  per restore. A selection above the limit is rejected as a whole: nothing is restored, and the
  reason is logged.
- Older archive (RET-034): at most 30,000 artifacts per restore by default (configurable). A larger
  restore proceeds up to the limit, then terminates, and the reason is logged.
- Older archive: restoring by folder path is not supported in the UI (select the files); a
  restore fails when the path it generates exceeds 1,024 characters; repository names are limited
  to 64 characters.
- The older archive mails a summary to the administrator on completion if a mail server is
  configured.

Operations:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Restore archived packages | Optional project key (project-level restore); restore-to-original flag (required unless a new location is given); fallback location (must name at least a repository key); new location (required unless restore-to-original is set); list of package manifest paths to restore (mandatory, as returned by the package search) | Run summary ID and a message; the restore continues in the background | System administrator; project administrator within the project |
| Restore archived artifacts (older archive) | Restore kind (package or artifact; default artifact); restore-to-original flag; fallback location (required with restore-to-original; at least a repository key); central destination repository (required otherwise; the original path hierarchy is kept inside it); namespace flag (default true: item paths are given without the archive namespace); items to restore as `repository/path` entries, folders ending in `/`, files not | Accepted; message and restore operation ID | System administrator |

Edge case: the older restore operation accepts folder entries, while "restore by folder path" is
unsupported in the UI. A compatible implementation accepts folder entries in the operation and
expands them to files.

UI location: archive search results, restore action per row or for the selection, dialog
"select restore destination".

### RET-031 · Restore runs and reports
Priority: **Later**

Restored tab columns: run ID, project, start time, restored items with physical size, status,
report availability. Filters: run ID, start time range, status.

Restore report: run details (status, start time, duration); summary (restored packages, processed
packages, virtual size, physical size); restore request (origin projects, package type, fallback
destination path, new destination path, restore-to-original flag); per item (name, version, a
lead-file reference where applicable, size on disk, source repository and its type, target
repository, status such as "restored", message, path, created, modified). The set of restore run
statuses and part of the per-item field list are unspecified.

Operation (older archive): restore status — inputs: time range; output per restore: state (pending,
in progress, in progress with errors, complete, complete with errors, failed), counts of completed,
failed and total artifacts, progress percentage, start and finish time, total, succeeded and failed
bytes, destination and destination kind (original or central location, or fallback location).

### RET-032 · Archive storage overview
Priority: **Later**

Global-context view of the archive tier:

| Figure | Meaning |
|---|---|
| Archive size | Total size held in the archive. |
| Optimization | Space saved on the active instance: (initial storage − archived storage) / initial storage × 100. |
| Virtual | Number of artifacts and their total size without deduplication. |
| Physical | Number of binaries and their total size after deduplication. |

The older archive's monitoring page shows binaries (count and size), artifacts (count and size) and
the ratio between them, plus archiving and restoring tabs with policy filter, "hide runs that
archived nothing", execution ID, date range, start time and status.

UI location: retention policies page, archive section, "archive storage info" link (system
administrators only).

### RET-033 · Archive and restore logging
Priority: **Later**

Archive runs log on the active instance; restores log on the archive instance. Logged events:
policy creation; start of a run; end of a run with start and end time, success percentage, processed,
archived and failed counts; per-item status. A restore logs its start with size, item count and an
estimated end time, its end, and the status of every restored item. In the older archive
(RET-034), each operation identifier starts with a prefix for artifact archive, package archive,
artifact restore or package restore. Archive identifiers then carry the policy key and a random
suffix, and restore identifiers carry a random suffix only.

### RET-034 · Artifact-level archive policy (older archive)
Priority: **Later**

The older archive model, still supported on self-hosted installations: a policy selects artifacts
(not packages) or packages of one type and moves them to a paired archive instance; the archive can
purge them after a period.

<details>
<summary>Settings of the artifact-level archive policy</summary>

| Setting | Meaning | Default |
|---|---|---|
| Policy key | Unique key. | — (mandatory) |
| Enabled | When disabled, execution is blocked. Mandatory in the operation. | — |
| Schedule | Cron expression; mandatory in the operation. | — |
| Description | Free text. | empty |
| Package mode | Whether the policy acts on packages (true) or artifacts (false). | false |
| Included / excluded repositories | Artifact mode; combinable with an AND/OR switch. | — |
| Included / excluded path patterns | Artifact mode; AND/OR switch. | empty |
| Included / excluded artifact name patterns | Artifact mode; AND/OR switch. | empty |
| Included / excluded property name and value | Artifact mode; AND/OR switch. | empty |
| Deployed before | Age condition. | off |
| Last downloaded before | Usage condition. | off |
| Query | A query in the query language instead of the structured criteria; artifact mode only. Converting to a query is one-way: afterwards the structured fields can no longer be edited in the UI. | empty |
| Package type | Package mode: one format (Conan, Debian, Docker, RubyGems, generic, Go, Gradle, Helm, Maven, npm, NuGet, PyPI, RPM). | — |
| Package repositories | Package mode: local repositories, mandatory. | — |
| Package name / version | Package mode; wildcards allowed. | all |
| Created before / downloaded before | Package mode: absolute instants in epoch milliseconds. | — |
| Maximum run duration | Minutes. | unspecified |
| Purge from archive after | Months after which archived items are deleted from the archive instance; the UI offers "never" or "after" a period. | unspecified |
| Skip trash can | Archived items are deleted on the active instance without passing through the trash can and their binaries become eligible for the next full garbage collection (RET-038). | false |

</details>

Behavior and limits:

- Preview: an estimate of how many items a policy would archive, for a stored policy (by key) or an
  unsaved configuration; result is the estimated item count and total size in bytes. The UI offers
  it as "preview" before saving.
- Status per policy: last run state (pending, in progress, in progress with errors, complete,
  complete with errors, failed), completed, failed and total counts, progress percentage, start and
  finish time, total, succeeded and failed bytes, triggered by; next execution time.
- UI statuses: pending (never run), triggered (manual), running, running with errors, complete,
  complete with errors, stopping, stopped.
- Archived repositories on the archive side carry a random namespace prefix.
- An email summary goes to the administrator after each run if a mail server is configured.
- Only system administrators create and run these policies.
- For multi-file formats other than Docker, Maven, npm and Gradle, the safe procedure is:
  mark unused packages with a property, move them to a dedicated repository, then archive by
  property, repository and age.
- Defects to avoid: cron validation that always flags expressions as invalid, age settings that
  appear not to persist in the form, structured criteria that cannot be saved as a query from the
  UI.

Operations:

| Operation | Inputs | Output |
|---|---|---|
| Create | Policy key; configuration (key, enabled and schedule mandatory; either a query or structured criteria for artifact mode) | Created policy with last-run status and next execution time; conflict if the key exists |
| Update | Policy key; configuration | Updated policy |
| Get; list | Policy key; none | Policy or all policies, each with last-run status and next execution time |
| Delete | Policy key | Confirmation text |
| Run | Policy key | Accepted; the run starts in the background |
| Stop | Policy key | Accepted |
| Preview by key; preview by configuration | Policy key; unsaved configuration | Estimated count and total bytes |
| Archive status | Time range (epoch), optional policy filter | Per run: policy key, state, completed, failed and total counts, progress percentage, start and finish time, total, succeeded and failed bytes |
| Restore status | Time range (epoch) | Same structure, plus destination and destination kind (original or fallback) |
| Get archive namespace | none | Namespace identifier generated at pairing; needed to re-pair (RET-028) and for restore requests that give item paths including the namespace (RET-030) |

Search, convert-to-query and restore operations: RET-029 and RET-030. All operations of the older
archive require a system administrator.

<details>
<summary>Tuning parameters of the older archive</summary>

Active instance:

| Parameter | Meaning | Default |
|---|---|---|
| Archive threads per policy | Worker threads that archive items. | 5 |
| Restore threads | Worker threads that restore items. | 5 |
| Connections to archive instance | Maximum HTTP connections used to deploy to the archive instance. | 50 |
| Connections per route | Maximum HTTP connections per route. | 50 |
| Socket timeout | HTTP socket timeout (unit unspecified, presumably milliseconds). | 15,000 |
| Restore pull interval | Seconds between attempts to pull restored items from the archive instance. | 120 (1,800 recommended) |
| Restore batch size | Restored items pulled per batch. | 1,000 |
| Restore bandwidth cap | Approximate MB cap for all restores; meaning of 0 unspecified (presumably unlimited). | 0 |
| Restore error threshold | Percentage of errors that stops a restore. | 10 |
| Archive batch size | Items read from the database per batch during archiving. | 10,000 |
| Archive bandwidth cap | Approximate MB cap for one archive run; meaning of 0 unspecified. | 0 |
| Archive error threshold | Percentage of errors that stops an archive run. | 10 |
| Packages per archive batch | Maximum packages archived per batch. | 10,000 |
| Retry count | Retries after a communication failure with the archive instance. | 30 |
| Retry delay | Seconds between retries. | 2 |

Archive instance:

| Parameter | Meaning | Default |
|---|---|---|
| Restore processing threads | Threads that process a restore request. | 5 |
| Restore status check interval | Seconds between checks of the restore state of archived items; a longer interval lowers cost on low-cost object storage. | 120 (10,800 recommended) |
| Restore cleanup interval | Seconds between runs of the restore cleanup job. | 300 |
| Restore artifact limit | Maximum artifacts per restore. | 30,000 |

Changing these values is discouraged.

</details>

## Repository-level and per-build retention

### RET-035 · Maximum unique snapshots, unique tags and tag revisions
Priority: **P1**

Without any policy, a repository can cap how many variants of one thing it keeps:

| Setting | Formats | Effect | Default |
|---|---|---|---|
| Maximum unique snapshots | Maven, Gradle, Ivy, sbt and NuGet local repositories; also in the Maven-family and VCS remote forms and the federated forms | Caps the unique snapshots kept per artifact; the oldest snapshots beyond the cap are removed. At least 2 must be kept to avoid concurrency issues. | 0 or blank: no limit, nothing removed |
| Maximum unique tags | Docker, OCI and Helm OCI local repositories (also in the federated form) | Caps the unique tag names kept per image; first in, first out: pushing a new tag name beyond the cap removes the oldest tag together with all its kept revisions. The `latest` tag gets no exemption. | 0 or blank: no limit |
| Tag revision retention | Docker, OCI and Helm OCI local repositories | Number of revisions of the same tag name to keep when a tag is overwritten. Older revisions stay under a name derived from their digest; beyond the limit the oldest revision is removed (first in, first out). | 1: overwritten revisions are not kept |

Behavior:

- None of these runs on a schedule over existing content. A push triggers the evaluation, for the
  pushed item only.
- Snapshots: on deploy, surplus old snapshots are marked, and a background job running every 5
  minutes deletes the marked ones.
- Tags: the check runs lazily, per push, only for the image that was pushed; images not pushed
  since the limit was set keep their excess tags.
- When both tag settings are active, the unique-tag cap takes precedence: if it removes a tag, all
  kept revisions of that tag go with it. A manifest list and its sub-manifests count as one tag
  context; this relationship is tracked only for tags pushed after the feature existed.
- Tags removed by these settings go to the trash can when it is enabled (RET-038); for removed
  snapshots this is unspecified.
- Open: in the federated form, tag revision retention is either a number of days after which unused
  tags become eligible, or a count as everywhere else; this page follows the count semantics.

Interaction with policies: RET-010 (keep last N wins over the tag cap; kept revisions go with
their tag) and RET-003 (a policy can remove what these settings would keep).

Settings and placement: REPO-025 and REPO-027 in [repositories.md](repositories.md).

### RET-036 · Unused-cache expiry on remote repositories
Priority: **P1**

Remote repositories are never touched by retention policies (RET-003). Their cache is bounded by a
two-step mechanism instead:

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Unused artifacts cleanup period | Per remote repository | Hours an item cached by this repository may stay unused before it is marked as a removal candidate. Setting the period does not remove anything by itself. | 0 (empty or 0: cached items are kept indefinitely) |
| Unused cached artifacts cleanup schedule | System-wide | Cron expression of the maintenance job that removes the marked items. A "run now" action starts the job immediately. The job is resource-intensive; running it at most once or twice a day, outside working hours, is recommended. | unspecified; example every 12 hours |

Behavior:

1. An item that has not been used for longer than its repository's period becomes a candidate.
   Which timestamp "unused" is measured from (last download from the cache, or caching time for
   never-downloaded items) is unspecified.
2. The next run of the system-wide cleanup job removes all candidates.

Related mechanisms that are not retention:

- Invalidating ("zapping") a remote cache only marks cached metadata as expired so it is fetched
  again on the next request; it deletes nothing (see
  [remote-repositories.md](remote-repositories.md)).
- Items can be deleted from a remote cache by hand; nothing can be deployed into it.
- A separate system-wide job with its own cron expression deletes aggregated descriptor files that
  virtual repositories cache, once they are older than 168 hours (one week); see REPO-037 in
  [repositories.md](repositories.md).

UI location: remote repository settings, advanced tab, cache settings section (period);
administration, maintenance page (schedule).

Interactions: cache settings and pull-through behavior in
[remote-repositories.md](remote-repositories.md). Apart from manual deletion, this is the only
mechanism that bounds the size of a remote repository's cache.

### RET-037 · Retention parameters per build name
Priority: **P1**

Retention parameters can be attached to a build name, so that older runs of that build are
discarded without a cleanup policy. The companion CI client offers the same parameters from the
command line: the number of newest runs to keep, a maximum age in days, excluded build numbers, and
artifact deletion. Without any keep rule, that client request succeeds and removes nothing.

| Setting | Meaning | Default |
|---|---|---|
| Maximum builds | Number of runs of this build name to keep; older runs are discarded. | unspecified |
| Earliest build date | Runs started before this instant are discarded (ISO 8601 date-time with milliseconds and zone offset). The client command expresses it as a maximum age. | unspecified |
| Protected build numbers | Build numbers that are never discarded. | empty |
| Delete build artifacts | Also delete the artifacts of discarded runs. | unspecified |

Operation: set build retention — inputs: build name, retention parameters, asynchronous flag
(default true: the call returns immediately and discarding proceeds in the background; false: the
call waits until done, unless a node-level setting forces asynchronous processing); output: success;
errors: malformed request, missing delete permission on the build, unknown build. Privilege: the
delete action on the build (see IAM-025 in [identity-and-access.md](identity-and-access.md)).

Edge cases:

- Open: either setting retention does not immediately delete builds, or the synchronous mode waits
  for completion. A compatible implementation applies the parameters immediately and lets the flag
  only decide whether the call waits.
- Discarding removes build records; with "delete build artifacts" it also removes artifacts, which
  can affect other consumers of those artifacts.
- Build records live in a dedicated internal repository. A path- or repository-based cleanup that
  includes it removes build history and breaks publishing; it has to be excluded from generic
  cleanup.

Interactions: deleting individual builds and build records in [builds-and-ci.md](builds-and-ci.md);
policy-based build cleanup RET-023.

### RET-038 · Trash can interaction
Priority: **P1**

Unless a policy skips it, a policy deletion goes to the trash can like a manual delete, so the trash
can decides whether the effects of retention are reversible.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Trash can enabled | Per instance (not synchronized between federation members) | Deleted items from local repositories are kept in the trash can instead of being removed. | unspecified |
| Trash retention period | Per instance | Days a deleted item stays in the trash can before it is removed for good. | unspecified |
| Skip trash can | Per policy (package cleanup, build cleanup, package archive, older archive) | The policy's deletions bypass the trash can and are permanent at once. | off for the older archive; unspecified for the others |

Rules:

- Without the system-wide trash can, every policy deletion is permanent, whatever the policy says.
- Release-set cleanup has no skip setting. A deleted release-set version can be neither found by
  trash-can search nor restored from the trash-can UI; a dedicated restore operation exists (see
  [distribution-and-lifecycle.md](distribution-and-lifecycle.md)).
- Garbage collection removes trash-can items whose retention period has expired; it runs on a cron
  schedule (by default every 4 hours). A binary is deleted only when no other path references its
  checksum. Binaries that are no longer referenced at all (including items removed from the trash can
  by hand) are deleted by a full garbage collection that runs every 20 collection cycles by default.
- Trash-can visibility follows repository permissions: deploy or manage permission on the original
  repository shows its items; delete permission also allows restoring them.
- Emptying the trash can removes everything in it permanently.

Interactions: trash can and restore in [artifacts.md](artifacts.md); trash search SRCH-053 in
[search-and-query.md](search-and-query.md); garbage collection in
[storage-and-blobs.md](storage-and-blobs.md); federated propagation RET-025.

## Release-set retention

### RET-039 · Release-set retention policies
Priority: **Later**

Edge instances that receive release sets through distribution have their own retention model,
separate from the release-set cleanup policy (RET-024).

| Setting | Meaning | Default |
|---|---|---|
| Retention enabled | System-wide switch. While off, any received release set can be deleted and "keep" markers are ignored. Must be on before rules, candidate reports and the cleanup history can be used. | unspecified |
| Automatic deletion | Whether the background job deletes versions automatically. | unspecified |
| Maximum versions per release set | Versions of one release set kept on the edge instance. | unspecified |
| Maximum age | Days since a version was released. | unspecified |
| Schedule | Cron expression of the background deletion job. | unspecified |
| Maximum deletions per background run | Upper bound of versions one background run deletes. | unspecified |

Behavior:

- Several retention rules can exist; a version that matches one or more rules is marked.
- Automatic deletion removes a version only when **both** the version count is exceeded **and**
  the version is older than the maximum age. Versions that meet only one criterion appear only in
  the candidate report for manual deletion.
- A rule always combines both values; updating only one keeps the other's last value.
- Each received release set carries a keep / don't-keep marker. Kept versions are never deleted
  while retention is enabled.
- Manual cleanup: a candidate report, viewable by age or by version count and searchable by name
  with wildcards (kept items shown by default), lets an administrator toggle markers and delete
  selected don't-keep versions. Deletion is asynchronous and permanent; versions that can no longer
  be deleted are reported in a failure dialog. An artifact contained in another remaining release
  set stays.
- A cleanup history lists marker changes and deletions, sortable and paginated; deletions
  initiated from the source through the distribution service are not listed.
- The mechanism is designed for PostgreSQL and Apache Derby only.

Operations (privilege: system administrator; the candidate, history and delete operations answer
"not available" on instance configurations without this model):

| Operation | Inputs | Output |
|---|---|---|
| Get / set retention switch | Enabled flag (set) | Enabled flag (get) |
| Get / set retention rules | Automatic flag, versions per release set, maximum age in days, schedule, maximum deletions per run; partial updates allowed, but a value cannot be unset, only overwritten | Current rules |
| List deletion candidates | Rule type (by version count or by age, mandatory), include kept versions; offset (default 0), limit (default 100), sort field (name, version, distribution time (default), keep marker), direction (default ascending), case-insensitive name or version pattern with `*` (default `*`) | Total and entries (name, version, creation time, distribution time, keep marker) |
| Cleanup history | Offset (default 0), limit (default 100), sort field (name, version, operation, time (default), user), direction (default descending) | Total and entries (name, version, operation: delete, keep or unkeep; time; user) |
| Delete received release-set versions | Name and version; dry-run flag (default false) for validation only | Success |

UI location: distribution area, release sets, received tab, "cleanup" button (administrators only),
with "by days", "by versions" and "cleanup history" tabs.

Related: a system-wide setting declares a partially distributed release set of the older
generation "incomplete" after a number of hours and makes it eligible for cleanup from a temporary
folder; 0 disables this cleanup.

Interactions: distribution and edge instances in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

## Custom cleanup

### RET-042 · Search-driven and scripted custom cleanup
Priority: **P2**

Retention needs that the policy model does not cover can be built from general operations; no
dedicated feature exists for them.

- Search by usage and by creation date: one search operation finds artifacts not downloaded since
  a given instant, another finds artifacts created within a date range. A client script deletes
  the results through the normal delete operation, which uses the trash can when it is enabled
  (RET-038). See
  [search-and-query.md](search-and-query.md).
- Server-side scripting hooks can run cleanup logic inside the system, for example deleting
  artifacts not downloaded for a number of months. See
  [events-and-extensibility.md](events-and-extensibility.md).
- Manual deletion of a complete package version: [artifacts.md](artifacts.md).

Such scripts bypass everything the policy framework provides (dry run, run limits, run history,
reports, lead-file judgment). They act per file, not per package version.

## Out of scope

### RET-040 · License-tier gating of retention features
Priority: **Out of scope**

Tying package, build and release-set cleanup, the package archive tier and the older archive to paid
license tiers and minimum product versions. A self-hosted product has no such gating; every
retention feature is either implemented or not.

### RET-041 · Portal provisioning of hosted archive instances
Priority: **Out of scope**

Ordering and provisioning a hosted archive instance through an operator-run customer portal
(instance naming rules, provider and region choice, credentials, activation e-mails) is
hosted-service plumbing without meaning for a self-hosted product; the pairing itself is RET-028.

## Notes for implementers

- Policies are created disabled; for package cleanup and archive policies a create request that
  asks for "enabled" fails. Enabling is always a second call, so a compatible implementation keeps
  the rejection for every kind rather than silently accepting an enabled policy.
- Path patterns appear in the forms but not in the create operations; trove defines them
  in both for package cleanup and must decide explicitly whether archive policies honor them
  (RET-003).
- Repository-level caps (maximum unique snapshots and tags, tag revisions) act lazily on push, not
  as a sweep; lowering a cap does not shrink images or artifacts that are not pushed again
  (RET-035).
- Dry runs are allowed on disabled policies; manual and scheduled runs are not. Both kinds of run
  return a trigger ID immediately and continue asynchronously.
- Every package condition is judged on the lead file only. Properties on other files of the package
  and per-file download times do not count.
- "Not downloaded since" falls back to the creation date for never-downloaded versions; without
  that rule, a usage-only policy would delete freshly published packages nobody has pulled yet.
- "Keep last N" ranks by creation date, per package, and always keeps the newest version; it is a
  veto over every other condition, not an additional selector.
- Time units are fixed lengths counted from the run's start (day 24 h, month 30 days, year 365
  days), not calendar arithmetic.
- Runs are bounded (300 minutes, 100,000 packages) and resume implicitly: the next run re-evaluates
  from scratch and picks up what is left. Selection must therefore be idempotent and safe to
  interrupt at any item.
- Runs do not start while garbage collection or another mandatory maintenance task runs; queued and
  skipped runs are invisible in the run history and only logged.
- Run reports live in an internal repository and are not produced for runs that selected nothing.
  The internal repositories for reports and for build records must be shielded from generic
  cleanup.
- Deletions made by a policy follow the normal delete path: they are replicated to push-replication
  targets, propagated to all federation members, and are reversible only through the trash can.
