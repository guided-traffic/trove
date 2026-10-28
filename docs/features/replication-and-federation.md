# Replication, federation and multi-site topology

This page covers everything that keeps several independent installations of the system (here
called *sites*) consistent with each other: one-way repository replication, run on a schedule or
driven by events (push from local repositories, pull into remote repositories); federated
repositories, where identically named repositories on up to ten sites form a peer mesh with
event-driven bidirectional or receive-only synchronization; the synchronization service,
monitoring and recovery that federation depends on; the federation of identities, projects and
upstream-curation policies across sites; and an outbound tunnel that lets a site behind a firewall
take part. High availability inside one site is in [ha-and-deployment.md](ha-and-deployment.md),
pull-through caching in [remote-repositories.md](remote-repositories.md), trust and pairing
tokens in [access-tokens.md](access-tokens.md).

## Repository replication

### REPL-001 · Repository replication between instances
Priority: **P2**

Replication copies the content of a repository to a repository on another instance of the same
kind of system. It runs in one of two directions:

| Direction | Configured on | What happens |
|---|---|---|
| Push | A local repository (REPO-016 in [repositories.md](repositories.md)) on the source instance | The source sends new and changed items to a local repository on the target instance |
| Pull | A remote repository (RMT-001 in [remote-repositories.md](remote-repositories.md)) whose upstream is a repository on another instance of the system | The remote repository fills its cache ahead of demand instead of waiting for the first client request |

Each direction can run on a schedule (REPL-002), on events (REPL-003, REPL-007), or both. Typical
goals: every site sees the same versions of remote content, build outputs are shared between
sites, clients are insulated from slow or unstable links, and caches are filled before they are
needed.

Choosing a direction:

| | Push | Pull |
|---|---|---|
| Strengths | Asynchronous and fast; shortest out-of-sync window; in a chain (A→B→C) the first instance only feeds the next one | Many targets can pull from one source; each item travels one hop; targets do not have to relay content |
| Fits when | The far side must reflect every change almost immediately | There are many targets, or the source sits behind a proxy or firewall that prevents it from pushing (for example a hosted source and an on-premises target) |

Rules and limits:

- Pull replication works only against another instance of the system. Third-party registries are
  cached through ordinary pull-through behavior (RMT-001).
- Both instances should run the same version. A temporarily mixed pair usually keeps working; the
  older side should be upgraded soon.
- Replication cycles must be avoided: A→B together with B→A, or A→B→C→A. They can lose data or
  grow storage without bound. Whether the system detects or prevents cycles is unspecified.
- Running replication on top of cross-region replication of the binary store at the
  object-storage layer is discouraged, because the two cause synchronization problems. The
  supported way to let the storage layer move binaries is the metadata-only mode (REPL-016),
  where the source explicitly checks the target store before sending content.
- On a local repository, replication and federation are mutually exclusive (REPL-018).
- Replication is meant for continuous synchronization; very large one-time migrations should use
  dedicated migration means instead.
- All replication activity is written to the main service log.

Interactions: deletions performed by retention policies on a push-replicated repository are
replicated to the targets (RET-026 in [retention-and-cleanup.md](retention-and-cleanup.md));
blocking replication hooks can veto single items (EVT-047 in
[events-and-extensibility.md](events-and-extensibility.md)); a push-replicated repository can be
converted into a receive-only federation (REPL-050).

### REPL-002 · Scheduled replication
Priority: **P2**

A scheduled replication job synchronizes the whole repository (or the configured path prefix,
REPL-010) at times given by a Quartz-style cron expression (seconds field first, e.g.
`0 30 2 * * ?` # example). The UI shows the computed next run time next to the expression.

- One schedule per repository: all targets of a push repository run at the same moment. When the
  configuration is supplied as a list of targets, the cron expression and the event flag of the
  first list element apply to the repository (REPL-011).
- A scheduled run reconciles all items, not only recent changes. It is therefore the safety net
  for event-based replication: changes whose events failed, or that happened while a server was
  down or the network was partitioned, are picked up by the next scheduled run. Running a schedule
  on top of event replication is the recommended setup.
- Default schedule: unspecified.

UI location: local repository settings, replication tab (push); remote repository settings,
replication tab (pull). The replication tab also offers an action to run the configured
replication immediately (REPL-012).

### REPL-003 · Event-based push replication
Priority: **P2**

With event replication enabled for a push target, every create, copy, move, delete and property
change on the source is sent to the target almost immediately. Only the items touched by the event
are transferred. The flag is set per target (REPL-005) and applies to both push and pull
configurations in the configuration model. Default: unspecified.

Edge case: how often a failed event is retried is unspecified; the next scheduled run (REPL-002)
closes the gap. Without a schedule, nothing repairs such a gap except a manual run (REPL-012).

### REPL-004 · Multi-target push replication
Priority: **P2**

One local repository pushes to several target repositories on several instances at the same time.
Each target has its own settings (REPL-005) and its own status (REPL-013); schedule and event flag
are shared by all targets of the repository.

Operations (administrator only, local repositories only):

| Operation | Inputs | Output |
|---|---|---|
| Create or replace the multi-target configuration | Repository key; cron expression (required); event-replication flag (required); list of target definitions (required) | Success or validation error |
| Update the multi-target configuration | Same as above | Success, validation error |
| Delete one or all targets | Repository key; optional target URL | Without a URL every target definition of the repository is removed |

The maximum number of targets per repository is unspecified.

### REPL-005 · Push replication target settings
Priority: **P2**

<details>
<summary>All settings of a push target</summary>

| Setting | Meaning | Default |
|---|---|---|
| Active | The target replicates once the configuration is saved | unspecified |
| Target URL | Full URL of the target local repository on the other instance (instance base URL plus repository path) | required |
| Username | User on the target instance | none |
| Credential | Password or identity token of that user | none |
| Outbound proxy | Proxy definition used to reach the target (RMT-048) | unspecified |
| No proxy | Bypass every proxy for this target | unspecified |
| Socket timeout | Network timeout for remote operations, in milliseconds | unspecified |
| Event replication | Replicate each change as it happens (REPL-003) | unspecified |
| Synchronize deletes | Delete on the target what was deleted on the source (REPL-008) | off |
| Synchronize properties | Copy item properties (REPL-009) | on |
| Synchronize download statistics | Copy download counters (REPL-009) | unspecified |
| Include path prefix pattern | Replicate only matching paths (REPL-010) | none |
| Exclude path prefix pattern | Skip matching paths (REPL-010) | none |
| Check binary existence in target store | Send metadata only when the target's store already holds the binary (REPL-016) | off |
| Replication key | Identifier generated per repository–target pair; returned by the system, not supplied | generated |

</details>

UI location: local repository settings, replication tab. The tab lists every target with its URL,
active flag and event flag; an add/edit dialog holds the settings above.

Security: the target credential grants write access on another instance; the response of the
read operation (REPL-011) contains the credential field, and whether it is masked is unspecified.

### REPL-006 · Pull replication
Priority: **P2**

Pull replication is configured on a remote repository whose upstream is a repository on another
instance of the system. Its source may be a local, remote or virtual repository there.

| Setting | Meaning | Default |
|---|---|---|
| Active | Pull replication enabled once saved | unspecified |
| Source URL | URL of the source repository. For some formats the URL must address the format's API path of the repository (npm is one example), for others the plain repository path (Maven is one example) — the same rule as for chained remote repositories (RMT-040) | required |
| Event replication | Keep a live event channel open and pull each change as it happens (REPL-007) | unspecified |
| Synchronize deletes | Delete cached items that no longer exist at the source (REPL-008) | off |
| Synchronize properties | Copy item properties (REPL-009) | on |
| Path prefix | Replicate only a sub-path of the source repository (REPL-010) | none |
| Schedule | Cron expression (REPL-002) | unspecified |

Behavior:

- Credentials are not part of the replication settings: the remote repository's upstream
  credentials (RMT-005) are used for both the scheduled file listing and the event channel. What
  is pulled is limited by the effective read permissions of that user on the source instance.
- With event replication, a file uploaded at the source becomes available on the pulling instance
  right away.
- Container registries: when a container remote repository uses both scheduled and event-based
  pull, registry bearer-token authentication must be switched off on the remote repository. The
  scheduled listing uses the source's storage listing with HTTP Basic authentication and fails with
  403 otherwise; username and credential remain required.

UI location: remote repository settings, replication tab.

### REPL-007 · Event channel for event-based pull replication
Priority: **P2**

For event-based pull, the pulling instance opens a long-lived event stream to the source. Three
peer-to-peer operations implement it; clients never call them directly:

| Peer operation | Purpose |
|---|---|
| Open channel | Establish the event stream from source to subscriber |
| Close channel | Tear the stream down |
| Deliver events | Receive replication events from the peer |

- All three reject anonymous callers with 401 and callers that are not permitted replication peers
  with 403. For event delivery, the sender also names its originating server, and the receiver
  checks that this identifier belongs to the authenticated service identity of the caller.
- The pulling side authenticates with the remote repository's upstream credentials: a user that
  exists on the source and can read the source repository. A connection test in the remote
  repository's advanced settings verifies them; 401 means missing or wrong credentials. Setups that
  relied on anonymous read access at the source need a dedicated service user instead.

Administrative operations on the source instance (administrator only):

| Operation | Inputs | Output |
|---|---|---|
| List subscribers of a local repository | Repository key | List of identifiers of the instances and repositories subscribed for event-based pull |
| Release all channels | none | Every subscribed channel is closed; subscribers then try to reconnect |

### REPL-008 · Delete synchronization
Priority: **P2**

When enabled, items deleted on the source are also deleted on the target, including their
property metadata. The flag defaults to off in the configuration model and in the run-now
operation (REPL-012).

Edge cases:

- Deletion is driven by absence: the target loses every item that does not exist on the source.
  An empty source repository therefore purges the target repository.
- Replicating everything and then deleting unwanted items is discouraged when the target is
  external or untrusted, because the content is exposed in between.

### REPL-009 · Property and download-statistics synchronization
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Synchronize properties | Properties (ART-021 in [artifacts.md](artifacts.md)) of replicated items are copied with them | on |
| Synchronize download statistics | Download counters (ART-029) are copied, so that cleanup on a disaster-recovery target does not treat items as unused | unspecified |

The pull form in the UI does not offer download statistics; the configuration model carries the
field for both directions.

### REPL-010 · Path filters
Priority: **P2**

The UI offers one optional path prefix: only items below that sub-path are replicated. The
configuration model offers an include path prefix pattern and an exclude path prefix pattern
(pattern syntax unspecified). Unset filters impose no restriction.

Limit: the include and exclude patterns of a replication job are stored together in a column of
2048 characters, of which 200 are reserved; the combined pattern text may therefore not exceed
1848 characters. A longer definition is rejected with a validation error and not saved.

### REPL-011 · Replication configuration operations
Priority: **P2**

Administrator only. Local and remote repositories unless stated.

| Operation | Inputs | Output / behavior |
|---|---|---|
| Get configuration | Repository key | One target definition, or a list for multi-target push; each with every field of REPL-005 plus repository key and replication key |
| Set (add or replace) | Repository key; one target definition or a list | Replaces the configuration. With a list, the cron expression and event flag of the first element apply to the repository |
| Update | Repository key; target definition | 404 when no configuration exists |
| Delete | Repository key; optional target URL | Without URL all target definitions are deleted. Supported for local repositories and remote-repository caches |
| Create, replace, update multi-target | See REPL-004 | Local repositories only |

Errors: 400 for a missing repository or malformed body, 401, 403 for non-administrators, 404 for
an unknown repository or configuration.

### REPL-012 · Run replication now
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Run replication immediately | Repository path: a local repository triggers push, a remote-repository cache triggers pull; optional sub-path; optional strategy parameter (values unspecified); optional list of per-target overrides | 200 when scheduled |

Per-target overrides: target URL, username, credential and proxy name (these four are used by
push only), no-proxy flag, synchronize properties (default on), synchronize deletes (default off),
include and exclude path prefix patterns, binary-existence check. With an empty list every configured replication of the
repository runs. The UI offers the same action on the replication tab. Required privilege:
unspecified.

### REPL-013 · Replication status
Priority: **P2**

After each run the system records the start time, the finish time and the outcome (ok or failure)
as properties on the source repository that was replicated. The properties are keyed by source repository for a
single target and by source repository plus target URL for multi-target push, and are visible in
the properties of the repository root in the tree browser.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get replication status | Repository path (local, remote-repository cache or remote repository) | Overall status; last completion time (ISO 8601, or null if never completed); list of targets with URL, repository key, status, last completion; map of repositories to status and last completion | Read permission on the path |

Status values: unknown, never run, incomplete, error, warning, ok, inconsistent, partial failure.
The precise meaning of each value is unspecified.

### REPL-014 · Enable or disable replications in bulk
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Enable or disable many replications | Action (enable or disable); include patterns and exclude patterns matched against target repository URLs | All matching replication definitions switch state |

Use: switch off every replication towards one target instance or one target repository in one
call. Pattern syntax and required privilege: unspecified.

### REPL-015 · Global replication block
Priority: **P2**

A system-wide switch that stops replication from being triggered at all, separately for push and
pull. Administrator only.

| Operation | Inputs | Output |
|---|---|---|
| Get global replication state | none | Push blocked (yes/no), pull blocked (yes/no) |
| Block replications | Push flag (default true), pull flag (default true) | Confirmation; while blocked no replication of that kind is triggered |
| Unblock replications | Push flag (default true), pull flag (default true) | Confirmation |

What happens to a run that is already in progress when the block is set: unspecified.

### REPL-016 · Metadata-only replication over storage-level copy
Priority: **P2**

When the binary stores of two instances are already synchronized at the storage layer (for
example by object-storage replication), replication can transfer metadata only and leave the
binary content to the storage layer.

1. The operator synchronizes the binary stores of source and target.
2. On the source, the binary-existence check is enabled in the replication configuration or in a
   run-now call (REPL-005, REPL-012).
3. On the target, binary-existence checks are allowed through the checksum-replication setting.

During a run the source asks the target whether its store already holds each binary; if so, only
metadata is sent. Copying the data at the storage level remains the operator's responsibility.
Off by default; self-managed sites only; administrator only.

| Setting (target side) | Meaning | Default |
|---|---|---|
| Allow binary-existence check | Lets sources use the check against this instance | required field; default unspecified |
| Days to trust | Number of days during which full checksums from the source are trusted, for migrations | 0; maximum 30 |

| Operation | Inputs | Output |
|---|---|---|
| Configure checksum replication | Allow flag; days to trust | Success |
| Get checksum replication | none | Allow flag; date until which trust is granted |

Interaction: content-addressed blob storage in [storage-and-blobs.md](storage-and-blobs.md).

### REPL-017 · Replication monitoring across sites
Priority: **P2**

With the central multi-site management service (REPL-051), administrators see every replication in
which a selected site takes part.

- Topology map, replication view: arrows between sites show replication direction; the list can
  be filtered by source or destination repository; a bottom panel shows the replication status of
  the selected site (succeeded, failed, not configured, not run).
- History table, per replication run:

| Field | Meaning |
|---|---|
| Source repository | Replicated repository |
| Target site | Site holding the target repository |
| Target repository | Destination repository |
| Run date | When the run took place |
| Status | Outcome |
| Duration | Duration of the last run in seconds |
| Size | Size of the last run |
| Successful | Number of source items replicated |
| Failed | Number of items that failed at the destination |
| Rate | Transfer rate in kbit/s |

## Federated repositories: model and membership

### REPL-018 · Federated repository
Priority: **Later**

A federation is a set of repositories with the same purpose on up to ten sites (*members*) that
behave as one repository: every member accepts deployments and serves downloads locally, and
every change is propagated asynchronously to the other members. There is no primary member. Use
cases: teams at several sites, production environments at remote locations, and higher
availability of artifacts across sites (also disaster-recovery copies and regional distribution).

Repository model:

- Federation is a mode of a local repository; its class stays local. The local repository form has
  a multi-site tab offering federation (preselected for new repositories) or replication.
- A repository uses either federation or replication, never both at once. Switching to federation
  deletes the replication definitions; switching to replication requires removing all members
  first. A repository with replication and no members opens the tab in replication mode.
- Older model: a separate *federated* repository class. Repositories stored that way keep working
  and are edited on a federation tab; no migration is required. Explicit conversions exist
  (REPL-024).
- The repository creation dialog lists only local, remote and virtual. The repository list has a
  multi-site column showing federation, replication or both (when "both" can occur is unspecified).

Operations (administrator only):

| Operation | Inputs | Output |
|---|---|---|
| Create a federated repository | Ordinary repository creation with class local and a member list (each member: URL, enabled flag, optional mode, REPL-020) | Repository; a non-empty member list enables federation |
| Update members | Repository update with a new member list | Updated federation; may trigger initial full sync (REPL-035) |
| List repositories with federation flag | Optional filter: federated yes / no / both | Each repository with its class, a federated flag and its member list (URL, enabled); legacy federated repositories appear with class local |

For developers a federated repository is a local repository: browsing and download need read
permission, deployment needs deploy permission, and client setup instructions always point at the
local member (UI-027 in [ui-inventory.md](ui-inventory.md)). A deployment is available locally at
once and on other members after synchronization; downloads are served from the local copy with
local latency.

### REPL-019 · Members and mesh topology
Priority: **Later**

- Topology is a full mesh of up to 10 members; the federation tab shows the count as *n/10*.
- A member is addressed by a URL made of the member site's base URL (or its dedicated federation
  URL, REPL-047) and the repository key; the key is at most 64 characters.
- The repository key is used as the repository name on members the system creates. The key itself
  is not part of the synchronized configuration (REPL-033); whether a member added by URL may carry
  a different key is unspecified.
- Members are independent: there is no ordering or priority among members for metadata, and a
  slow or overloaded member does not hold back the others.
- In projects (IAM-051 in [identity-and-access.md](identity-and-access.md)) all members of a
  federation must belong to the same project key.
- Only system administrators create federations and add members (REPL-044).

Member card on the federation tab:

| Element | Meaning |
|---|---|
| URL | Full URL of the member repository (read-only) |
| Repository key | Key on the member site (read-only) |
| Current badge | Marks the member on this site |
| Synchronization toggle | Pause or resume (REPL-022); offered for the local member, disabled while it is receive-only |
| Receive-only checkbox | Sets a remote member to receive-only (REPL-020); not offered for the local member |
| Remove | Removes a remote member (see below); not offered for the local member |

Member addition and removal:

- **Add.** Two ways: pick a registered site (REPL-051) and an existing repository there, or create
  a new one with the same key on that site; or enter the member URL by hand, in which case a
  missing repository is created on the remote site automatically. Sites appear in the picker only
  when trust has been established (TOK-039, TOK-040 in [access-tokens.md](access-tokens.md)). The
  add button is disabled while repository key or format are unset and while the local member is
  receive-only. A new member starts in the *pending full sync* state (REPL-039) and receives an
  initial full synchronization. The site performing the addition copies its security-scanning
  configuration to the new member once (REPL-033).
- **Remove.** An administrator of any member may remove its own or any other member, including the
  repository the federation was started from; the remaining members continue. With the separate
  synchronization service (REPL-045), removal is refused while any other member is offline, and
  offline members must be removed first. The older embedded mode allows the removal, but the
  offline member never learns of it and must be corrected by hand on its site. Removing members
  never changes the repository class. To disconnect temporarily, pause instead (REPL-022).

| Operation | Inputs | Output |
|---|---|---|
| Remove a member from all federations | Base URL of the member site | Member removed from every federation it belongs to; 400 for an invalid URL, an unknown member, or when the remote site's configuration cannot be fetched |

### REPL-020 · Mirror modes: bidirectional, receive-only, send-only
Priority: **Later**

| Mode | Sends | Receives | How it is set |
|---|---|---|---|
| Bidirectional | yes | yes | Default for every member |
| Receive-only | no | yes | Checkbox on a remote member's card, or a receive-only mode on that member's entry in the member list of the repository API, at creation or later |
| Send-only | yes | no | Never set directly; it is the automatic counterpart of a member marked receive-only |

Bidirectional and receive-only members can be mixed in one federation. Receive-only gives a
hub-and-spoke layout with one owner of the content and no traffic or storage back towards the hub.

Behavior of a receive-only member:

- It never sends events; it does not start full synchronizations; configuration synchronization is
  skipped for it.
- Changes to the federation configuration (adding, removing or updating members) made on the
  receive-only member are rejected with 409 and a message that the local member is receive-only.
  The federation is managed from the sending member.
- Local writes are *not* blocked. Uploads, deletions, property changes, moves into the repository
  and promotions succeed locally but are never propagated, monitoring still reports the member as
  healthy, and the next full synchronization from the sender removes locally added items and
  restores locally deleted ones. Write access should be removed through permissions.
- Copying items out is safe; moving them out deletes them from the receive-only member.
- Container tag overwrites replicated from the sender trigger the usual orphan-layer cleanup on the
  receive-only member as well.

Receive-only differs from push replication: a push-replication target accepts local writes by
design and keeps them; a receive-only member does not keep them reliably. Spokes that need local
uploads should use bidirectional membership with restrictive permissions, or a separate local
repository.

On create or update with a receive-only member, an initial full synchronization from the sender
starts automatically; the reverse direction is impossible. Setup is complete when the member's
connection shows receive-only and healthy (REPL-041).

### REPL-021 · Receive-only trust enforced at site level
Priority: **Later**

Trust between two sites for federation is set up with a pairing token (TOK-042). Two variants:

| Variant | Effect |
|---|---|
| Read-only federation pairing (recommended for governance) | The target receives a receive-only long-lived trust token. Every federation built over this pairing is receive-only on the target, and the target's administrators cannot create federations that send back to the source |
| Full federation pairing | A full trust token; direction is decided per repository by the member mode only, so bidirectional and receive-only federations between the same two sites are both possible |

Pairing operations (administrator):

| Operation | Inputs | Output |
|---|---|---|
| Generate pairing token | Use case (bidirectional federation, receive-only federation, and other use cases such as archive storage) | Short-lived pairing token, for example with a lifetime of 300 seconds |
| Register (exchange) pairing token | Same use case; pairing token | Confirmation message |
| Unregister trust | Use case; base URL of the peer; force flag (default off) | Confirmation message |

Errors: 404 for an unknown use case.

### REPL-022 · Pause and resume a member
Priority: **Later**

A synchronization toggle per member pauses and resumes synchronization with it.

- While paused, events for the member are queued locally but not sent, and the other members do
  not synchronize items and metadata with it. The member stays in the topology.
- Paused is not a member state but an *enabled* flag; it is shown as *disabled* on the dashboard
  and can be combined with any state.
- After a long pause a full synchronization may be needed, because queued events expire (REPL-029).
- The toggle is unavailable while the member is receive-only.

Open: the toggle is offered either next to every member or only on the local member; the spec
decides.

### REPL-023 · Federation priority
Priority: **Later**

| Level | Effect |
|---|---|
| Standard | Default processing order |
| High | Larger event batches (up to 400 events) and processing before standard repositories |
| Urgent | Highest urgency, batches up to 400 events; not settable from the UI |

- Limits against priority inflation: at most 10 high-priority repositories per member and at most
  20 % of all federated repositories; the lower value applies. Exceeding them fails with a
  prioritization error.
- Priority is set per member; to prioritize a whole federation, set it on every member.
- Changes take effect immediately.
- UI location: federation status dashboard (REPL-040), priority column, flag icon toggles high and
  standard.

| Operation | Inputs | Output |
|---|---|---|
| Set priority | Repository key; level | 200; 404 for an unknown repository |

Open, for the spec to decide: the standard batch size is either 200 events or 100 events (worker
batch); whether the set-priority operation accepts *urgent* or only *high* and *standard*, although
urgent is meant to be API-only; and whether the limit is a flat site-wide maximum of 10 prioritized
repositories or the combined rule above.

### REPL-024 · Repository class conversions
Priority: **Later**

| Conversion | How | Constraints |
|---|---|---|
| Replication ↔ federation on a local repository | Multi-site tab (REPL-018) | Switching to federation deletes replication definitions; switching back requires removing members |
| Local → legacy federated class | Operation with repository key | Within a project, all members share the project key |
| Legacy federated class → local | Operation with a list of repository keys | Only when the repository has no other members; response maps each failed key to a reason |
| Build-information repository → federated | Operation with the repository key, then add members by repository update | Irreversible |

Removing all members does not convert a legacy federated repository back to local.

### REPL-025 · Delete a federated repository
Priority: **Later**

Deletion removes the local repository and its items irreversibly. On self-managed sites the other
members continue on their own; their membership lists should then be cleaned up. On hosted
deployments deletion may propagate to other members depending on deployment policy.

Open: on self-managed sites, either a repository-deletion event carries a deletion instruction to
remote members (REPL-028), or remote members continue independently as described above; the spec
decides.

### REPL-026 · Format support and exclusions
Priority: **Later**

| Format | Federation support |
|---|---|
| All formats in general (Maven, npm, NuGet, Docker/OCI, PyPI, Go, Helm, generic, Conan, RPM, Debian, Terraform registry, build information, …) | Supported |
| Terraform state backend | Excluded by a configurable exclusion list whose default contains only this format |
| Cargo | Federation (and virtual repositories) not supported without a workaround (workaround unspecified here) |
| Machine-learning model repositories | Cannot be federated with repositories using the older model-hub layout; layouts are incompatible |
| NuGet | All members must use the same NuGet layout variant |

Format pages: [formats/README.md](formats/README.md).

### REPL-027 · Members on different versions
Priority: **Later**

Once every member has reached a baseline version, members may run different versions and be
upgraded one site at a time.

- When a member with a newer version brings configuration fields or values that older members do
  not know, the UI reports the conflict. Resolution: disable the new fields on the newer member or
  remove it from the federation; for enhanced fields with new values, pick a value the older
  versions support.
- When repositories are created through the API, the system resolves this itself, so that
  automation such as disaster recovery does not break: features older members lack are disabled;
  enhanced features get a value every member supports.
- The member heartbeat reports *version conflict* when versions are incompatible (REPL-039).
- Known limitation: an error response from an older member may not be understood by the newer
  synchronization service, blocking the affected event batch; no workaround.

## Federation synchronization

### REPL-028 · Event-driven synchronization
Priority: **Later**

Every operation on a federated repository produces an event that is propagated to the other
members according to their mirror modes.

| Item event | Trigger | Carries |
|---|---|---|
| Create | New item deployed | Path, checksums, size, uploader, timestamps; a binary task for the content |
| Update | Existing item overwritten | Changed metadata; a binary task for the new content |
| Delete | Item deleted, including when the trash can is emptied | Deletion instruction; removed on all members |
| Properties | Properties added, changed or removed on an item or folder | Property key/value pairs |

| Repository event | Trigger | Carries |
|---|---|---|
| Repository created | Federated repository created with members | Configuration and member list |
| Repository updated | Settings changed (description, include/exclude patterns, layout, …) | Updated configuration |
| Repository deleted | Federated repository deleted | Deletion instruction to remote members |
| Configuration sync | Push configuration or scheduled configuration sync (REPL-034) | Current configuration |
| Member status change | Member enabled, disabled or otherwise changed | New member status |

Lifecycle:

1. The operation creates an event with type, path, metadata and checksums.
2. The event is stored in a durable queue held in the database, so it survives restarts.
3. Workers of the synchronization service take events in batches (REPL-029).
4. Redundant events are removed (REPL-029).
5. The batch is sent to every member whose mode allows receiving.
6. The target applies the metadata. If a create or update refers to content it does not hold, it
   creates a binary task (REPL-030).
7. The event is marked done; failed events are retried (REPL-029).

Metadata travels quickly in bulk and separately from binary content: properties cannot be stored
before their file exists, and container manifests cannot be stored before their layers. A target
member cannot serve a file until its metadata has arrived; the file's appearance in the target's
tree browser shows that it has.

### REPL-029 · Event queue processing, deduplication and retention
Priority: **Later**

<details>
<summary>Queue and worker settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Worker batch size | Events taken per worker cycle for standard priority | 100 |
| Maximum events per batch | Upper bound per cycle, standard priority | 200 |
| Maximum events per batch, high/urgent | Upper bound per cycle for prioritized repositories | 400 |
| Stream allocation size | Events loaded per stream read | 50 |
| Worker retry count | Retries per worker | 2 |
| Minimum retry backoff | Between worker retries | 2 s |
| Event request timeout | Per propagation request | 30 s |
| Maximum error retries | Attempts before a member's queue counts as exhausted | 6 |
| Retry backoff factor | Growth factor of retry delays | 4 |
| Maximum retry backoff | Upper bound of a retry delay | 30 h |
| Same-path grace period | Minimum time between processing successive events for the same path, against races | 60 s |
| Create-then-delete elimination | A create and a later delete of the same item that have not been sent yet are both dropped | active (switch to skip it: off) |
| Delete-event deduplication | Redundant delete events are merged | active (switch to skip it: off) |
| Event retention | Processed events are kept this long, then removed by a daily cleanup at 01:00 | 3 days |
| Stalled-stream recovery | Job that restarts event streams stopped by transient errors | every 10 s |
| Lag collection | Job that measures lag per stream | every 2 min |
| Delayed threshold | Lag above which a connection is shown as delayed | 20 s |

</details>

- Deduplication keeps only what matters: an update followed by a delete sends only the delete.
- Lag is the time between now and the oldest event not yet processed.
- Retention edge case: if a member is unreachable longer than the retention period, unsent events
  can be removed by cleanup. The member then becomes *out of sync* and only a full synchronization
  (REPL-035) recovers it. The retention must be the same on every member; raising it grows the
  database considerably.
- Queue states per member: healthy, pending full sync, error, stopped (by an administrator),
  disabled by the system (persistent errors).

### REPL-030 · Binary transfer tasks
Priority: **Later**

When an event refers to a checksum the target does not hold, a binary task is created; a handler
downloads the content from the source and stores it on the target. Content already present on the
target is not transferred again. With the separate synchronization service (REPL-045, recent
versions only), a member that is offline does not block binary transfer: the others obtain
missing binaries from the remaining members. The older embedded mode lacks this.

| Task state | Meaning | Next step |
|---|---|---|
| Queued | Waiting for a handler | Picked up automatically |
| In progress | Downloading from the source or storing on the target | Completes or returns to queued on a transient failure |
| Completed | Content transferred | Marked for deletion, then cleaned up |
| Exhausted (submission) | Retries for storing into the repository used up | Replay (REPL-031) |
| Exhausted (storage) | Retries for the storage operation used up | Replay (REPL-031) |
| Rejected | Target refused the content by validation or policy | Final; see logs |

<details>
<summary>Binary task settings</summary>

| Setting | Default |
|---|---|
| Maximum retries per task | 10 |
| Handler thread pool | 100 |
| Tasks fetched per handler run | 500 |
| Insert batch size for new tasks | 500 |
| Handler start delay | 1 min |
| Handler run interval | 1 s |
| Penalty throttling for failing tenants | on; 1 s penalty, 1 s recovery |
| Lag thresholds for pending and executing tasks (metrics) | 5 min, collected every 1 min |
| Binary import workers on the repository side | 6 |

</details>

Individual binaries can also be prioritized on demand; the mechanism is unspecified.

### REPL-031 · Failed binary task management
Priority: **Later**

Administrator only. Fix the root cause first; replay is pointless otherwise.

| Operation | Inputs | Output |
|---|---|---|
| List failed binary tasks | Repository key (required); page size (default 1000); page number (from 1) | List of tasks: SHA-1, repository key, creation time, failure count |
| Replay failed tasks | Repository key; optional SHA-1 (all failed tasks of the repository if omitted) | Number of tasks re-queued; retry counters reset |
| Delete failed tasks | Repository key; optional SHA-1 | Number of tasks deleted, without retry |

Auto-healing (REPL-037) replays exhausted tasks periodically; manual replay is for disabled
auto-healing, immediate recovery or a targeted repository or binary. After a replay the
dashboard's in-progress counter rises and the failed counter falls.

### REPL-032 · Conflict resolution
Priority: **Later**

When the same item is changed on two members at nearly the same time, the event that arrives last
wins and overwrites the others; there is no merge. Every member applies the most recent action on
an item, whichever user performed it within their own permissions. Results depend on event
timestamps, so the server clocks of all members must be synchronized; unsynchronized clocks let
the wrong change win. Ordering between a delete and a concurrent update beyond last-wins:
unspecified.

### REPL-033 · Synchronized metadata and site-local settings
Priority: **Later**

| Synchronized | Items |
|---|---|
| Folders | Size in bytes, properties |
| Files | Checksum, properties |
| Files, opt-in | Uploader, last-modified time, deployment time; off by default and must be switched on at source and target |
| Files, during full sync, opt-in | Creation time; an operation with a required enabled flag switches it on |

Repository settings that stay local on each member (not synchronized):

| Local setting | Note |
|---|---|
| Outbound proxy, maximum download threads | Per site |
| Modification date | Per site |
| Offline (blacked-out) flag | Per site |
| Repository layout | Per site |
| Repository key | See REPL-019 |
| Primary and secondary signing key pairs | Per site |
| Security-scanning configuration | Copied once from the site performing an add-member operation to the new member, also for members the system creates; later changes are never propagated |
| CDN redirect configuration and flags, download redirect | Per site |
| Property-set definitions | Property values on items are synchronized; definitions (ART-026) must be created on every member, ideally with identical predefined values, before federating |

Beyond repository settings, the following are also site-local: general system settings (including
trash-can enablement and global offline mode), retention policy definitions (REPL-049), and
server-side extension scripts (REPL-049).

### REPL-034 · Configuration synchronization and drift detection
Priority: **Later**

Federation settings entered in the repository form (members, modes, synchronization toggles) are
not kept in the repository server's own database, nor in the synchronization service's database.
The repository server hands them to the synchronization service, which writes them to a
distributed key-value configuration store shared by the site's services and fans them out to
every remote member.

- **Push configuration** forces immediate propagation for one repository, e.g. after a network
  failure. UI: repository list, row action. After editing a federated repository, administrators
  should push the configuration and check the dashboard.
- **Configuration sync job** (hourly) does the same continuously and checks for drift: (1) the set
  of federated repositories known to the synchronization service matches the repository server,
  importing missing configurations and flagging mismatches as out of sync; (2) enabled flags and
  base URLs of local members match the repository server and identity service; (3) configuration
  of remote members matches the local one. A second integrity check runs hourly at half past.

| Operation | Inputs | Output |
|---|---|---|
| Push configuration | Repository key | Success |
| List inconsistent federated repositories | none | Keys of local federated repositories whose configuration differs from at least one remote member |

### REPL-035 · Full synchronization
Priority: **Later**

A full synchronization compares the complete file lists of two members and copies what is
missing. It runs when a member joins, when queued events were lost (REPL-029) and when recovery
(REPL-037) is not enough; it is costly for large repositories, comparable to restarting the
federation.

Mechanics:

1. The initiating member asks the remote member to compile its file list.
2. It polls every 30 s until the list is ready, so no connection stays open for hours.
3. The lists are compared (phases: prepare local list, poll remote list, compare, propagate
   batches).
4. Missing files are copied from the initiating member to the remote member in batches.
5. On success the member returns to healthy; on failure it is flagged out of sync.

Direction and deletes: deletions are not synchronized. An item deleted on A whose delete event was
lost still exists on B, and a full synchronization started on B copies it back to A. Reconciling a
whole federation requires a full synchronization started on every member.

A receive-only member never initiates a full synchronization (REPL-020).

| Operation | Inputs | Output |
|---|---|---|
| Full synchronization | Repository key; optional member URL (all remote members if omitted); dry-run flag (default off) | Message; list of result paths |
| Forced full synchronization | Repository key; optional member URL | Interrupts a running (e.g. stuck) full synchronization and starts a new one; same output |
| Propagate creation time during full sync | Enabled flag (required) | Switches creation-time propagation on or off |

Dry run: compares without copying and writes a JSON report listing each missing item (name, type,
full path) into a system repository; the response contains the report's path.

<details>
<summary>Full synchronization settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Full synchronization available | on |
| Excluded repositories | Repository keys excluded from standard full sync | none |
| Artifacts per batch | Synchronization service / repository server | 2000 / 1000 |
| Dry-run batch size | | 2000 |
| Members per coordinator run | | 50 |
| Coordinator interval | Job that processes members pending full sync | 120 s, first run after 1 min |
| Minimum interval per member | Between two full syncs of the same member | 60 min |
| Backoff between full syncs | | 0 min |
| Batch retries | | 5, with 5 s quiet period |
| Long-running threshold | Full syncs longer than this are logged | 30 min |
| Heartbeat staleness | A running full sync without heartbeat for this long counts as stale | 30 min |
| Lock duration / stale lock release | | 24 h / 60 min |
| Stuck full sync reset | Job that resets and restarts a stuck full sync, e.g. after a restart | every 15 min, first after 1 min |
| Polling interval / polling limit | Waiting for the remote file list | 30 s / 24 h |
| Concurrent file-list tasks | | 30 |
| File-list heartbeat expiry | | 15 min |
| Published file-list lifetime | An interrupted full sync resumes with a published list younger than this; older lists are discarded and rebuilt; 0 disables reuse | 30 min |
| File list built by query language | Otherwise by SQL paging | on |
| SQL paging threshold / page size / pages per temporary file | Only without query language | 400 000 / 100 000 rows / 4 |
| In-memory sorting | Sort file lists in memory (by depth, path, name) instead of in the database | on in the synchronization service, off in the older embedded mode |
| Entries per sorted chunk | | 400 000 |
| Temporary directory cleanup | | every 10 min |

</details>

Trap: database collation and in-memory sort order differ for some names. If members use
different in-memory sorting settings, matching files are treated as missing and copied again; the
setting must be identical on every member.

### REPL-036 · Full synchronization from a pre-generated file list
Priority: **Later**

For very large repositories the remote file list can be produced offline instead of over the
network:

1. On the initiating member, exclude the repository from standard full synchronization and enable
   in-memory sorting (restart required).
2. On the remote member, call *create full-sync file* with the remote repository key, a target
   repository and a path; the system writes a file named after the repository key there. The file
   must not be renamed.
3. Copy the file to the initiating member's synchronization service working directory, named after
   the remote repository key and the host (and port) of the remote member's URL.
4. Run a forced full synchronization (REPL-035), optionally limited to that member. The service
   uses the file as the remote list and skips fetching and polling; a missing or misnamed file or
   disabled in-memory sorting makes the run fail or fall back to the network.

### REPL-037 · Auto-healing and manual recovery
Priority: **Later**

Auto-healing runs periodically and repairs what can be repaired without a full synchronization:

- **Exhausted event queues** (more failures than the retry limit, REPL-029): failed events are
  reset and sending resumes.
- **Exhausted binary tasks** (REPL-030): tasks are replayed.
- States *error (generic)* and *error (unreachable)* recover this way; *out of sync* normally
  needs a full synchronization.
- Every administrator registered for e-mail notification receives a message naming source and
  target when an exhausted member is detected and recovery starts.

| Setting | Default |
|---|---|
| Auto-healing enabled | on |
| Auto-healing interval | 20 s, first run after 1 min |
| Delay between queue trigger attempts | 1 min |
| Health check that reconnects a broken connection from repository server to synchronization service and re-registers the queue | every 80 s |

| Operation | Inputs | Output |
|---|---|---|
| Recover | Optional repository key (all federated repositories if omitted); force flag (default off) | Clears errors of exhausted queues so sending is retried |
| Reconnect to the synchronization service | none | For a repository server that could not connect at startup; 424 when the dependency is unavailable |

Escalation order for an out-of-sync member: push configuration (REPL-034), auto-healing or
recovery, then full synchronization (REPL-035).

### REPL-038 · Bulk mirroring and ordered parallel apply
Priority: **Later**

Events are grouped into per-repository bulks and sent to the target, which assigns a thread pool
to each bulk; the threads store items in parallel while keeping the order that matters (for
container images: all layers before all manifests). This avoids sending one event at a time over
long-distance links and also speeds up full synchronization.

| Setting | Meaning | Default |
|---|---|---|
| Formats excluded from bulk mode | List of formats, or all | none |
| Executor pools on the target | Parallel bulks | 4 (maximum 5) |
| Threads per pool | Parallel items per repository | 4 (maximum 5) |
| Events per message | Events sent with one message | 100 |
| Wait when all pools are busy | | 2 min |
| Full-sync executor threads | Dedicated executor, takes precedence | 10 |
| Full-sync executor wait | | 2 min |
| Bulk property fetching | Read event properties from the database in bulk | off, 500 per batch |
| Metadata workers on the source | Parallel senders; each handles one repository at a time; restart required | open: either 10 or derived from the CPU count; the spec decides |

Rule: executor pools on the target must be at least the number of metadata workers on the source,
otherwise the target becomes the bottleneck. Heavier loads may need longer wait timeouts and a
longer socket timeout for large repositories.

## Federation health, monitoring and operations

### REPL-039 · Member states and health
Priority: **Later**

<details>
<summary>Member states</summary>

| State | Severity | Shown as | Meaning | Recovers automatically |
|---|---|---|---|---|
| Receive-only | 0 | Receiving only | Receives, never sends; federation configuration changes rejected locally | not applicable (expected) |
| Healthy | 1 | Healthy | Fully synchronized | — |
| Import in progress | 2 | Import in progress | An import is running on the member | — |
| Full sync running | 3 | Full sync running | Full synchronization in progress | yes, when stale |
| Full sync preparing | 3 | Full sync running | File lists being prepared | yes, when stale |
| Pending full sync | 4 | Pending full sync | Waiting for a full synchronization to start | no; started by the coordinator |
| Error (generic) | 5 | Error | General synchronization error | yes |
| Error (unreachable) | 5 | Error | Member inactive or unreachable | yes, when connectivity returns |
| Error (out of sync) | 6 | Error | Full synchronization failed, or queued events were deleted before delivery | open: either it recovers by auto-healing, or it typically needs a full synchronization (REPL-037); the spec decides |
| Marked for removal | 7 | Error | Member is being removed | no |

</details>

Transitions: new member → pending full sync → preparing → running → healthy; healthy → one of the
error states; generic and unreachable errors → healthy after recovery; out of sync → pending full
sync; healthy ↔ receive-only when the mode changes.

Labels that are not states:

- *Delayed* is a healthy member whose lag exceeds the delayed threshold (REPL-029).
- *Disabled* is the paused flag (REPL-022), independent of the state.
- *Unsupported* is a member whose version cannot report status; *N/A* means status cannot be
  determined.

Heartbeat (every 20 s; stale after 600 s; a member counts as inactive after 24 h without
heartbeat): results *ok*, *version conflict*, *mirror disabled on the remote side*, *repository
not found on the remote side*.

### REPL-040 · Federation status dashboard
Priority: **Later**

UI location: administration, monitoring, federation status; system administrators only (UI-054 in
[ui-inventory.md](ui-inventory.md)).

Level 1, federations list:

- Summary tiles with counts of federations per status: healthy (no member in error, full sync or
  delay), delayed, error, pending full sync, full sync running. Clicking a tile filters the
  table; clicking again clears the filter.
- Table columns: priority flag (default sort, high first), repository key (link to level 2),
  member count (opens a popup listing each member's key, URL and its most severe outgoing
  status), status (most severe status among the members, with the number of members in it).
- Aggregate severity order: error > delayed > pending full sync > full sync running > disabled >
  unsupported > healthy > N/A. Disabled, unsupported, receive-only and N/A have no tile.
- Filter sidebar with every status, free-text search over keys and status text, priority toggle
  (REPL-023).

Level 2, one federation:

- Member selector; counters of in-progress and failed binary tasks for the selected member (N/A
  when unreachable or unsupported).
- Connection table, one row per source→target pair: source (key and site URL), target, status,
  latency (time since the last successfully handled event, shown as a relative duration), queued
  events.
- Connection status values: healthy, delayed, error, pending full sync, full sync running,
  disabled, receiving only, unsupported, N/A.

### REPL-041 · Federation monitoring operations
Priority: **Later**

Administrator only. They answer 405 when the synchronization service is not enabled.

| Operation | Inputs | Output |
|---|---|---|
| Connection details | Repository key | Per connection: local key and site URL, remote key and site URL, status, lag in ms, queued events, disabled flag, supported flag |
| State details | Optional comma-separated status filter | Per federation: local key, priority (high or standard), members with URL, key, aggregated status, supported, disabled and lagging flags |
| State summary | none | Number of members per status: healthy, not available, delayed, pending full sync, full sync running, error, disabled |
| Binary task counters | Repository key; member URL | In-progress and failed counts |
| Local federation URL | none | The URL this site publishes to members (REPL-047) |
| Synchronization service active | none | Whether the dashboard can be used |

### REPL-042 · Detailed per-repository and per-site status
Priority: **Later**

A second, more detailed status view per repository and per peer site (it belongs to the older
embedded synchronization mode; its operations answer 405 when the separate synchronization service
is enabled).

- Per repository: items fully replicated (metadata and content), items with metadata only,
  binary tasks in progress and failed (in progress far below metadata-only signals a high failure
  rate).
- Per target member: key, URL, pending events with a breakdown into create, update, delete,
  property and error events, latency (now minus the time of the next unhandled event), next
  registration time, synchronized yes/no (based on a fixed number of error events).
- Per peer site: binaries still to fetch, fetch failures, *federated* or *delayed*, most delayed
  repository; refresh; popup with the ten most lagging repository pairs.

| Operation | Inputs | Output |
|---|---|---|
| Repository status (two variants, the second with more member statuses) | Repository key | Local key; binary task counts; per-mirror event status; artifact status. Expensive for the database |
| Synchronization state of all repositories | none | Per repository: key, status (syncing, full sync running, disabled, out of sync), members (remote key, remote URL) |
| Mirror lag | Optional threshold in ms | Per pair: local key, remote URL, remote key, lag in ms, event registration time |
| Unavailable mirrors | none | Members not yet synchronized: newly added, during local→federated conversion, or past an error threshold; with local key, remote URL, remote key, status |

### REPL-043 · Federation metrics
Priority: **Later**

The synchronization service exposes OpenMetrics/Prometheus metrics on an authenticated endpoint
(an access token is required). By default metrics are aggregated per tenant; some switch to
repository or member granularity when thresholds are exceeded, which costs performance.

| Metric family | Content |
|---|---|
| Lag | Aggregate worker lag in seconds; per-repository lag for the most lagging repositories; queue lag and events left |
| Events | Events received from the repository server, removed by deduplication, received from remote members, sent to remote members; per-source and per-target processing failures |
| Full synchronization | Count and duration per member, file-list size, compile, sort and propagation rates, failures |
| Member states | Members per state; disabled members; members recovered by auto-healing |
| Binary tasks | Count by state; pending and executing tasks beyond the lag threshold; handler duration |
| Configuration integrity | Repositories inconsistent with remote members and with the local repository server; enabled-flag mismatches; missing peer configuration or trust tokens |
| Infrastructure | HTTP client connections available, leased, pending; failed database queries and query durations; errors between repository server and synchronization service; database partition counts and intervals |

An additional, older metric set (off by default) reports: pairs whose lag exceeds a threshold
(minimum and default 20 000 ms), the maximum lag, the number of members and of repositories over
the threshold, disabled members, and repositories per status (healthy, pending full sync, full
sync running, out of sync, exhausted, disabled). Measurement interval 30 s (never below 5 s);
disabled members excluded from lag by default and counted by default. Lag metrics cover only
healthy federations.

The catalog lists metric families only; names are implementation-defined. General metrics
conventions: [operations-and-monitoring.md](operations-and-monitoring.md).

### REPL-044 · Permissions and service-to-service authorization
Priority: **Later**

| Operation | Required privilege |
|---|---|
| Create, edit, delete federated repositories; add and remove members; full synchronization; priority; push configuration; federation status | System administrator |
| Browse and download; client setup instructions for resolution | Read |
| Deploy; client setup instructions for deployment | Deploy |

Between sites, each site holds a long-lived trust token obtained when trust was established
(REPL-021, TOK-039, TOK-040). For each remote call the synchronization service exchanges it for a
short-lived token restricted to the scopes the operation needs:

| Internal scope | Purpose |
|---|---|
| Validate member | Check reachability and configuration of a remote member |
| Full sync | Authorize full synchronization |
| Service | General synchronization-service operations |
| Configuration sync | Synchronize configuration |
| Metadata | Query and send metadata |
| Connection states | Query connection state |

| Setting | Default |
|---|---|
| Restrict exchanged tokens to required scopes | on |
| Lifetime of exchanged tokens | 1 h |
| Trust-token refresh job | hourly |
| Refresh before expiry | 7 days |
| Expiry margin (tokens this close to expiry are not used) | 900 s |
| Cache for remote tokens | 600 s |

Failure classes: token exchange failed (stale or invalid trust: re-establish trust), trust token
not found.

### REPL-045 · Synchronization service
Priority: **Later**

Federation is run by a separate, horizontally scalable synchronization service next to the
repository server. An older mode embedded in the repository server also exists; new capabilities
such as receive-only members exist only in the separate service.

- **Datastore:** PostgreSQL only, for event queue, message payloads, member state, binary tasks,
  file-list tasks and stream positions. It can share the repository server's PostgreSQL database
  or use its own (PostgreSQL 13 or later; schema created on first start). Data cannot be moved from
  a shared to a separate database later. Peers do not need the same database engine.
- **Partitioning:** event and payload tables are partitioned automatically: a job every 30 min
  creates a new partition when the current one reaches 50 % of its capacity (sub-partition chunk
  size 7 000 000 records); sub-partitions are monitored daily.
- **Sizing:**

| Scale | Federated repositories | Database size | Database resources | Service replicas, CPU, memory |
|---|---|---|---|---|
| Small | < 10 | < 5 GB | 2 vCPU, 4 GB | 1; 2–4 CPU; 4–8 Gi |
| Medium | 10–100 | 5–50 GB | 4 vCPU, 8 GB | 1–2; 4–6 CPU; 8–12 Gi |
| Large | > 100 | > 50 GB | 8+ vCPU, 16+ GB | 2+ with autoscaling; 4–6 CPU and 8–12 Gi each |

- **Kubernetes deployment (Helm):** one replica by default; horizontal autoscaling off by default
  (minimum 1, maximum 11 when enabled); liveness and readiness endpoints (ready = database
  connected and configuration loaded; probe timeout 5 s); metrics scraping on by default every
  60 s; settings are passed as string values (unquoted numbers or booleans break chart
  rendering); self-signed and custom-CA certificates supported.
- **High availability of the repository server:** enabling or disabling the synchronization
  service is applied only on the node that handles the request; other nodes do not follow and need
  a restart. See [ha-and-deployment.md](ha-and-deployment.md).

<details>
<summary>Background jobs</summary>

| Job | Default schedule |
|---|---|
| Member heartbeat | every 20 s |
| Stalled-stream recovery | every 10 s |
| Lag collection | every 2 min |
| File-list tasks | every 30 s |
| Readiness check | every 15 s |
| Partition management | every 30 min |
| Sub-partition monitoring | daily, midnight |
| Event cleanup | daily, 01:00 |
| Full-sync directory cleanup | every 10 min |
| Metrics collection / export | about every minute / every 60 s |
| Orphan cleanup (e.g. state rows of deleted repositories) | daily, 02:00 |
| Configuration sync / integrity check | hourly / hourly at :30 |
| Full-sync coordinator | every 120 s |
| Auto-healing | every 20 s |
| Worker runner, binary task handler | continuous |

</details>

<details>
<summary>Thread pools, HTTP client and caches</summary>

| Setting | Default |
|---|---|
| General job pool / tenant pool / heartbeat pool / tenant heartbeat pool | 100 / 100 / 100 / 200 |
| Full-sync pool / request tunneling pool / support bundle pool | 30 / 30 / 5 |
| Tenant configuration-sync pool / repository event pool | 100 / 100 |
| HTTP connect, response, read and write timeouts | 120 s each |
| HTTP maximum connections | 50 |
| HTTP connection idle time / lifetime / eviction interval | 20 s / 30 s / 30 s |
| HTTP in-memory buffer | 5 MB |
| HTTP retries | 5, 100 ms backoff, on closed, reset, refused or timed-out connections |
| HTTP connection timeout / socket timeout | 5000 ms / 300 s |
| Internal RPC maximum payload | 4 MiB |
| Site discovery cache / repository management cache | 15 min / 40 min |
| Database lock pool | 3 |

Guideline: with more than 100 federated repositories set the general job pool to about 1.5 times
the number of federated repositories.

</details>

### REPL-046 · Network paths and outbound proxy
Priority: **Later**

| From | To | Purpose |
|---|---|---|
| Repository server | Synchronization service (same site) | Event submission, validation, state queries, full sync, request tunneling (internal RPC) |
| Synchronization service | Repository server (same site) | Batch delivery, binary transfer, metadata queries (HTTP) |
| Synchronization service, site A | Synchronization service, site B | Events, file-list exchange, heartbeats, binary tasks (HTTPS) |
| Repository server, site A | Repository server, site B | Binary transfer, validation responses (HTTPS) |

- Firewall: every site must reach every other site over HTTPS. Through the tunnel (REPL-062) only
  outbound HTTPS from the on-premises site is needed.
- Outbound proxy per federated repository (repository settings, advanced tab): pick a proxy
  definition (RMT-048), or tick *no proxy* to connect directly. With neither, the system default
  proxy is used if one exists (RMT-049). A proxy must be defined before it can be selected. Hidden
  on hosted deployments.
- Distant members may need longer HTTP timeouts (REPL-045).

### REPL-047 · Base URL and dedicated federation URL
Priority: **Later**

- Members find and address each other by the site's custom base URL, which must be set on
  self-managed sites before federating.
- A site can define a dedicated *federation URL*; it then publishes this address to members
  instead of the base URL. It applies to federation traffic only, not to redirects or metadata.
- Changing either URL on an active federation (all members on a recent enough version): change
  the URL. When both source and target run the separate synchronization service, nothing else is
  needed. When either side runs the older embedded mode, a new pairing token is generated on the
  source and the target is paired again; when the source runs the embedded mode, a *replace URL*
  call on the target (old URL, new URL) follows. Then check the dashboard and revoke the obsolete
  trust tokens. Last resort: dissolve the federation, change the URL and rebuild every federated
  repository.
- The federation URL is edited in the global configuration, not in the general settings screen.

### REPL-048 · Geo-synchronized topology behind a geographic load balancer
Priority: **Later**

Several sites sit behind one geographically routing DNS name and are meant to carry exactly the
same configuration (repository keys, users, groups, permission policies). How the security
entities are kept identical is unspecified; identity federation (REPL-053) is the mechanism this
catalog offers for it. Clients use one URL and do not change their tool
configuration when routed elsewhere.

- Every site uses the shared address as base URL and a unique per-site federation URL (REPL-047),
  so federation traffic reaches the right site behind the balancer.
- Synchronization is asynchronous: a client should stay on one site; after switching sites,
  recent changes may not have arrived yet.
- Converting existing federations: disconnect all federated repositories on one site (one by one or
  in the global configuration), wait and verify, add the federation URL, reconnect, verify on the
  member site, check logs, upload on one site and download on the other; run a full
  synchronization if anything changed meanwhile. On clustered sites only one node per site should
  be running during this setup.

### REPL-049 · Interaction of federation with other features
Priority: **Later**

| Feature | Behavior in a federation |
|---|---|
| Retention and cleanup policies (RET-025) | Deletions propagate to all members and are irreversible unless the executing member's trash can is on. Run a policy on one member only; parallel runs create duplicate deletes. Receive-only members receive deletions but do not run policies or initiate outward deletions. Policy definitions are local; dry runs see only the local member's items |
| Trash can (ART-036) | Emptying the trash can produces delete events for all members; trash-can enablement is per site |
| Global offline mode (RMT-031) | The site stops talking to remote members; events queue locally until the mode is lifted |
| Disable resolution | Downloads are refused, but the repository still accepts deployments and replicates |
| Import into a federated repository | Imported items produce events and spread to all members; import into one member only. For large imports pause the other members and run a full sync afterwards |
| Export | Contains content and metadata but no membership; membership must be re-created after an import |
| Seeding a new member | Import existing content before adding the member to reduce the initial full synchronization |
| Changes below the API (storage-level copies, manual migrations) | Produce no events; run a full synchronization afterwards |
| Server-side extension scripts (EVT-063) | Installed and run per site, never distributed. Event scripts fire again on every member when the item arrives there. Changes made by a script produce new events (risk of event storms and loops). Scheduled scripts run on every member independently |
| Property sets (ART-026) | Values synchronized, definitions local (REPL-033) |
| Container images | Tag overwrites trigger orphan-layer cleanup on every member whose manifest changed |

### REPL-050 · Converting push replication into receive-only federation
Priority: **Later**

A migration tool (a plugin of the companion command-line client) converts push-replicated local
repositories into federations with the target as a receive-only member.

| Option | Meaning | Default |
|---|---|---|
| Repositories | Comma-separated keys, or a file with one key per line (`#` comments and blank lines ignored); mutually exclusive | required |
| Allow multiple targets | Also convert a repository whose replication definitions name more than one target | off |
| Bidirectional | Create bidirectional instead of receive-only membership | off |
| Report file | Path of the per-repository result report | timestamped file in the working directory |
| Retry interval | Wait before retrying member assignment after 400 or 409 | 2000 ms |

Per repository: check that the source is local; create the target from the source configuration
if missing; convert both sides; configure membership; remove the replication definitions; the
initial full synchronization starts automatically.

Skipped: missing or non-local source; existing non-local target; multiple targets unless allowed
(then only the named target is configured, and the replication definitions to all other targets
are removed). The tool does not check that the repository actually replicates to the named target.
Pull replication is not covered. Report columns: key, status (success, failed, skipped), details;
a failed repository can be half-converted. Prerequisites: synchronization service on both sites,
established trust, no full synchronization running.

## Multi-site management and identity federation

### REPL-051 · Site registry
Priority: **Later**

A central multi-site management service keeps a registry of the sites it oversees and the services
they run.

| Field | Meaning |
|---|---|
| Name | Unique logical name |
| URL | Address of the site |
| Location | City, ISO 3166-1 alpha-2 country code, latitude, longitude |
| Credential | Pairing token generated on the site (TOK-042), or an older shared join key, or username and password for older services |
| Tags | Free labels such as location or purpose (development, test, production), used for filtering |

- Registration validates the pairing token by connecting to the site.
- The site hosting the management service is registered automatically as the *home* site; only
  its location can be edited and it cannot be unregistered.
- Unregistering only stops monitoring; the site itself is untouched.
- Site status: online, offline, partial, unauthorized.

| Operation | Inputs | Output |
|---|---|---|
| List sites | Filters on name, tag, URL (wildcard `*`), status | Sites with id, name, public URL, internal base URL, local flag, status code and message, location |
| Get site | Site id | Site |
| Register site | Fields above | 201 with id, name, URL, status, tags |
| Update site | Site id; all fields | 204 |
| Unregister site | Site id | 204 |

UI location: administration, topology, site services. Administrator only.

### REPL-052 · Site topology and health map
Priority: **Later**

A map shows the registered sites at their locations with health markers: several sites at one
location, all services online, some services offline, all services offline, site offline, and
identity federation status unavailable. The map switches between a replication view (REPL-017)
and an identity federation view (REPL-054) and can be filtered by tags. A panel per site shows
service status, storage usage, replication status and the installed services. UI location:
dashboard, topology.

### REPL-053 · Identity federation
Priority: **Later**

Identity federation keeps security entities identical across sites so that users can authenticate
against any site and permissions apply everywhere.

| Entity | Synchronized content |
|---|---|
| Users | Accounts, credentials, profile |
| Groups | Definitions and memberships |
| Permission policies | Policies controlling read, deploy and manage access (IAM-019) |
| Access tokens | Platform tokens for services and CI |

- It is independent of repository federation; neither configures the other. Set up identity
  federation first if permissions should behave the same on every site.
- Prerequisites: base URL set, administrator access, the central management service, registered
  sites, and certificate-based trust between the sites (TOK-039).
- Implicit rules: synchronizing a group also synchronizes its users; synchronizing a permission
  policy also synchronizes its users and groups.
- Skipped entities: a user whose group does not exist on the target, and a permission policy whose
  user or group does not exist there, fail to synchronize.
- Users from external directories (LDAP, SAML, …) are synchronized as users only, never the
  provider configuration, and only after they exist (have logged in) on the source.
- By default a permission policy only maps to repositories of the same class on the target
  (local→local, remote→remote); two system switches allow local permissions on remote repositories
  and the reverse.
- Signed download URLs (ART-018) are not supported with identity federation.

### REPL-054 · Identity federation topologies
Priority: **Later**

| Topology | Behavior | Trust setup |
|---|---|---|
| Mesh | Every site sends and receives changes; concurrent changes to the same entity need conflict handling (REPL-056) | Every site trusts the root certificates of all others |
| Star | A hub sends to spokes; changes on spokes are not sent back | Spokes trust the hub's root certificate |
| Chain (A→B→C) | B forwards what it received from A only when inbound forward propagation is enabled on B | default off |

Wizard (administration, topology, identity federation; on the site running the central
management service): choose *apply topology* mesh or star, move sites from available to included,
choose entity types, review summary, finish; a dialog shows the resulting sync activity. In star
mode entity types can be applied to all targets or assigned per target, and permission policies can
be filtered per target with include and exclude regular expressions. The UI displays a mesh as a
set of one-way relationships; the underlying synchronization is still two-way.

A status map shows arrows for direction and state of each relationship.

### REPL-055 · Identity federation synchronization settings
Priority: **Later**

Configured per identity service in its configuration file (outbound on the source, inbound on the
target); a restart is needed after changes.

<details>
<summary>Outbound and inbound settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Entity types | Which of users, groups, permissions, tokens are sent | all four |
| Excluded users | Users never sent | none |
| Buffer wait | Time between synchronization attempts (best effort) | 30 000 ms |
| Buffer size | Accumulated changes that trigger an attempt | 500 |
| Stale after | A target that fails or does not answer this long is considered stale (REPL-057) | 168 h |
| Future-time tolerance | See REPL-056 | 60 000 ms |
| Acknowledgement timeout | Wait for the target's acknowledgement before retrying | 3000 ms |
| Retries | Attempts before a change counts as failed | 3 |
| Stored events limit | Maximum events kept in the database; -1 = unlimited | -1 |
| Automatic full sync of recovered targets | Revive stale targets automatically | off |
| Targets | Logical name, URL, optional permission include/exclude regular expressions | none |
| Service-ID mapping (inbound) | Maps the source's service identifiers (by service type with wildcard) to the local services; normally generated automatically | generated |
| Partial entity sync (inbound) | Delta merge conflict handling (REPL-056) | off |
| Forward propagation (inbound) | Pass received changes on in a chain (REPL-054) | off |

</details>

- One identity service serves at most one service of each type; a replaced service's identifier
  must be removed manually before a new one of the same type can be added; identifiers of very old
  versions use a different format and must be mapped by hand.
- An invalid configuration file stops permission synchronization without any error message.
- Removing a registered target: the configuration-file procedure is to delete it from the file,
  reload the file and restart the identity service. The identity service's target operations
  (REPL-071) also offer an unregister call; how the two paths interact is unspecified.

### REPL-056 · Identity federation conflict handling
Priority: **Later**

- Default: the change with the newest timestamp wins. To absorb clock skew, a site ignores an
  incoming change whose timestamp is less than the future-time tolerance (default 60 s) after its
  own last change of that entity. Example: a local password change at 09:02:00.000 wins over an
  incoming change stamped between 09:02:00.000 and 09:02:59.999; an incoming change stamped
  09:03:00.000 or later wins.
- Optional delta merge (*partial entity sync*): sites exchange only the changed parts of an entity.
  On receipt, a site compares modification timestamps; if the entity changed locally in the
  meantime, it reloads it, merges both changes and stores the result, then propagates it. Applies to
  group membership, password and permission changes, so a user added to group A on one site and to
  group B on another ends up in both. Not applied to deletions: a delete on one site racing with an
  update on another remains a race.

### REPL-057 · Full broadcast and stale-target recovery
Priority: **Later**

A *full broadcast* sends all selected entities from a source to a target. It is used when a
federation is first set up and to revive a stale target (one that has not answered for longer than
the stale period, REPL-055); the source stops sending to stale targets until revived.

| Way | Details |
|---|---|
| UI | Identity federation page, three-dot menu of a source, broadcast, confirm |
| Operation on the source identity service | Target server name; authenticated as the identity service's own administrator, not a repository-server administrator. Outcomes: started; already in progress for this target; target not eligible; unknown target |
| Automatic | Setting *automatic full sync of recovered targets* (REPL-055) |

Full broadcasts are database-heavy; repeated broadcasts should be avoided.

### REPL-058 · Central identity federation configuration
Priority: **Later**

The central management service configures identity federation on registered sites. Administrator
only.

| Operation | Inputs | Output |
|---|---|---|
| Get configuration of one site | Site id | Entity types; targets, each with site id, identity-service identifier, URL, optional entity-type override, optional permission include/exclude patterns |
| Get configuration of all sites | Include sites without configuration (default off) | Per source site: configuration or error message |
| List candidate sites | none | Sites that can take part |
| Configure a site | Site id; entity types; targets (star from this source) | Per-step results with status ok, warning or error and a message; 400 on setup errors, 404 for an unknown site |

### REPL-071 · Identity federation target operations on a single site
Priority: **Later**

Besides the configuration file (REPL-055) and the central management service (REPL-058), each
identity service offers operations to manage its own outbound targets directly. The central
service is recommended for multi-site setups, these operations for managing one site.
Administrator only.

| Operation | Inputs | Output |
|---|---|---|
| List targets | none | Every target with name, URL, entity types, permission filters, group filters, active flag |
| Register target | Name (required), URL (required), entity types (default: users, groups, permissions, tokens), permission include/exclude patterns, group include/exclude patterns, active flag (default on), force-override flag (default off) | 201 with the stored target; 400 for missing name or URL; 409 when a target with that name exists |
| Get target | Target name | Target; 404 |
| Update target | Target name; any of entity types, permission filters, group filters, active flag (partial update) | Updated target; 400 when name or URL would change (both are immutable); 404 |
| Unregister target | Target name | 204; 404 |
| Validate target | Target URL | 204 when the target is usable for certificate-based trust (TOK-039); 400 for an empty or invalid request |
| Full broadcast | Target name | See REPL-057 |

Group filters (include and exclude patterns on group names) exist only in these operations; the
configuration file and the wizard describe permission filters only. Pattern syntax: regular
expressions in the configuration file; unspecified for the operations.

### REPL-059 · Workspace federation of projects and access settings
Priority: **Later**

A group of full sites (not edge instances) that keeps projects, project members and roles, global
and project lifecycle stages, users, groups, permission policies and access tokens identical on all
member sites. These become *global entities*: a change on any member reaches every member.

- Topology is always a mesh of peers; no star or chain. All entity types are synchronized; there
  is no selection, filtering or mapping.
- Candidate further entity types: curation entities and repositories.
- Use cases: the same working environment for a project on any site; failover between an active
  and a dormant site; sites dedicated to lifecycle stages sharing one set of projects and users.
- A site belongs to at most one group. The group is defined on a main site running the central
  management service; the main site is always a member, cannot be removed, and has no special role
  in synchronization (no single point of failure).
- All members need certificate-based trust (TOK-039); sites operated under a different account are
  registered with a pairing token (REPL-051).
- Coexistence: repository federation and upstream-curation federation (REPL-061) continue normally.
  Identity federation overlaps: a group must not contain several sites of the same identity
  federation; groups and identity federations should cover disjoint sites; a member that also
  belongs to an identity federation exchanges access entities with that federation's sites outside
  the group.
- Creation checks every site for health and connectivity, trust and conflicts with identity
  federation, and proceeds only if all pass. The initial synchronization compares all
  global entities and copies the newest value of each to every site (last write wins); older values
  are lost. Later changes are copied as they happen.
- A removed site keeps its last values and stops sending and receiving updates; non-members are
  never touched.
- When a site is offline the others continue; retries, queuing and self-inventory recover it
  without manual resynchronization.

UI location: administration, topology, workspace federation: create or edit with name, description
and site selection (eligible registered sites); a review step lists the global entities that stop
being maintained locally. The group key is derived from the name and immutable.

| Operation | Inputs | Output |
|---|---|---|
| List groups | none | Groups with key, name, description, created, modified, sites (name, current-site flag, location, connected or disconnected with a reason), total |
| Get group | Key | Group; 404 |
| Create group | Name (required), description, sites (registered site names, required) | 201 whenever stored, with per-site status for problems after storing; 400 when not stored (per-site trust or health details); 409 for an existing key |
| Update group | Key; any of name, description, sites | 200 with per-site status |
| Delete group | Key | 204; 409 when cleanup failed — the group is kept and the call must be repeated |

### REPL-060 · Workspace federation monitoring
Priority: **Later**

Each synchronization event copies one entity from one source site to one target site; a change
usually needs several events.

- UI: workspace federation, monitoring tab: per entity type a status bar over a selectable time
  window (hover for counts) and a failure column; a row opens the failed events; search by entity
  key; filter by source or target site; a toggle adds system entity types.

| Operation | Inputs | Output |
|---|---|---|
| Summary | Group key; time window (modified after/before, RFC 3339); include system entities (default off) | Per entity type (users, groups, permissions, tokens, projects, environments, roles; system: project users, project groups, project resources) counts of failure (any target failed), in progress (pending or sent, no failure), success (all targets); timestamp |
| List entities | Group key; entity type (required); optional single status; page token | Entities with per-target status (success, failure, in progress, pending, sent), failure description, last change; next page token; total |
| Entity detail | Group key; entity type; entity id | Per-target status with failure descriptions |

### REPL-061 · Upstream-curation policy federation
Priority: **Later**

Keeps upstream-curation policies (see [security-scanning-and-curation.md](security-scanning-and-curation.md))
consistent across sites. Star topology: one controller site sends to follower sites; the controller
may be hosted or self-managed and serves both kinds of followers; a self-managed follower can be
attached to a hosted controller through the tunnel (REPL-062).

Synchronized entity types (fixed set, all required): custom package labels, curation conditions,
curation format settings, curation policies.

Setup is API-only (no UI): enable the federation components on self-managed sites (a
self-managed site needs a recent version, and its repository server must run on Linux or
Kubernetes; a Windows installation cannot take part); on each follower generate a pairing token
for the workspace-federation use case (TOK-042); on the controller register each follower;
create the federation; trigger a full synchronization.
Afterwards the curation screens show the controller, and background synchronization appears in the
audit pages.

| Operation (on the controller, administrator) | Inputs | Output |
|---|---|---|
| Add follower | Unique follower id, URL, pairing token | 201 with id and URL; 409 for a duplicate id |
| List / get follower | — / follower id | Ids and URLs / URL; 404 |
| Update follower | Follower id; new URL; new pairing token (token rotation) | 204 |
| Delete follower | Follower id | 204; the follower leaves all federations (remove it from connection groups first) |
| Create federation | Unique name; description; one source mapped to the list of follower ids; entity types | 201 with generated id; 409 for a duplicate name |
| List / get / delete federation | — / id / id | Id and name / full definition / 204, synchronization stops at once |
| Full synchronization | Federation id | Starts synchronization across the federation |

## Hybrid connectivity tunnel

### REPL-062 · Outbound tunnel for sites behind a firewall
Priority: **Later**

Multi-site features need the hosted side to reach on-premises sites, which firewalls usually
forbid. The tunnel reverses the connection: a tunnel client on the on-premises site opens
persistent outbound TCP connections (HTTP Upgrade) to a tunnel server on the hosted site; services
then talk through it as if connected directly.

- Only outbound HTTPS from the on-premises site; no inbound ports, VPN or private links.
- The tunnel carries only traffic that originates on the server (hosted) side; traffic originating
  on the on-premises site does not use it.
- Authentication: a single-use pairing token secures creation; forwarded requests carry signed
  tokens; the on-premises site's own CA certificates are used.
- One server serves many clients; both sides can run several nodes for high availability.
- Uses: identity federation (REPL-053), repository federation (REPL-067), distribution to
  on-premises edge instances ([distribution-and-lifecycle.md](distribution-and-lifecycle.md)),
  upstream-curation federation (REPL-061); chained remote repositories (RMT-040) through a tunnel
  support only some authentication methods.
- Proxy support: the tunnel inherits the site's default proxy; each tunnel can override it.

### REPL-063 · Tunnel creation and pairing
Priority: **Later**

1. Check that the tunnel client service is active on the on-premises site.
2. On the hosted site: tunnels, server tab, generate pairing token.
3. On the on-premises site: tunnels, client tab, new tunnel:

| Field | Meaning |
|---|---|
| Tunnel ID | Lower-case letters, digits and hyphens; not `server` or `client`; unique |
| Client URL | Address of the on-premises site; the server routes tunnel traffic there |
| Server URL | Address of the hosted site used to open the tunnel |
| Pairing token | Single-use token from step 2 |

4. On the hosted site, register the on-premises site in the site registry (REPL-051) under the
   local tunnel endpoint address that contains the tunnel ID.
5. Test the connection from the server side.

- Certificate-based trust between the sites should exist before tunnels are created; otherwise
  the tunnels must be restarted after trust is set up.
- Tunnels written directly into configuration files appear in the list but cannot be managed by UI
  or API.
- A custom TLS root CA certificate can be set per tunnel (all nodes of that tunnel use it) or for all
  tunnels of the site, e.g. when the site uses a self-signed certificate.

| Operation (client side, administrator) | Inputs | Output |
|---|---|---|
| Define tunnel | Tunnel ID; server URL; client URL; pairing token | 201, tunnel active |
| Establish trust | Tunnel ID; pairing token | Confirmation; 409 while another pairing runs; 404 |

### REPL-064 · Tunnel lifecycle controls
Priority: **Later**

| Side | Action | Effect |
|---|---|---|
| Server | Test connection | Tries to reach the client |
| Server | Block / unblock | Terminates connections and refuses new ones without deleting the tunnel / accepts again; blocked tunnels are listed |
| Server | Clean up | Drops inactive or offline tunnels and removes them from the list |
| Server | Reset connections | Terminates the tunnel's connections on the handling node only |
| Client | Edit | Basic and advanced parameters (REPL-065) |
| Client | Stop / restart | Suspends the tunnel (connections closed, shown offline) / re-establishes it |
| Client | Reset connections | Terminates connections on the handling node only |
| Client | Delete | Removes the tunnel; idempotent; refused for system-configured tunnels |
| Client | Get configuration | Configuration with the server token redacted, stopped flag, last update time |

Block (server) and stop (client) are independent and the other side does not learn of them;
offline diagnostics reflect what the viewing side observes. UI location: administration, topology,
tunnels; a row shows node and connection details. UI status refreshes every 10–15 s.

### REPL-065 · Tunnel scaling and advanced parameters
Priority: **Later**

The client adds and closes parallel tunnel connections per node according to traffic, within the
configured range.

| Parameter | Default |
|---|---|
| Minimum tunnels per node | 2 |
| Maximum tunnels per node | 10 |
| Target usage low / high | 1 / 5 (exact semantics unspecified) |
| Tunnel creation interval | 1 min |
| Tunnel closing schedule | daily at midnight (`0 0 * * *`) |
| Allow closing tunnels in use | on |
| Anonymous endpoints | regular expression matching the system ping, readiness and liveness paths |
| Local dial timeout | 5 s |
| Graceful shutdown period | unspecified |
| Proxy enabled | on |
| Proxy cache expiration | 3600 s |
| Proxy reference | the site's default proxy |
| Proxy scheme override | none (http, https or empty) |
| Insecure connection | off |
| TLS root CA certificate | none |

Modify operation: partial update of any of these values and of the pairing token; 204; 404.

### REPL-066 · Tunnel diagnostics
Priority: **Later**

| Diagnostic | Details |
|---|---|
| Liveness, readiness | Unauthenticated health checks of the tunnel client |
| Debug snapshot | Per tunnel: configuration (token redacted), active connections with id, active streams, creation and last dial time; tunnel creation time. Per node; suitable as a CI pre-check |
| Metrics | Prometheus format; administrator or a token with metrics-read scope; 404 when collection is off |
| Support bundle | Configuration (default on), logs with optional date range (default on), system information (default on), thread dumps (count 1, interval 0); one at a time (409 otherwise) |
| Logs | Server service log (availability), server request log (requests entering), client service log (connectivity, handshakes, stability), client request log (requests leaving, without content) |

Troubleshooting: allow outbound HTTPS to the hosted site; check CA rotations; make sure internal
proxies do not intercept the HTTP Upgrade; confirm the handshake with the debug snapshot; check the
target service's request log for final delivery.

### REPL-067 · Repository federation over the tunnel
Priority: **Later**

- On the on-premises site, two switches (repository server and synchronization service) enable
  tunnel addresses, and the federation URL (REPL-047) is set to the local tunnel endpoint. This
  affects every federation of that site. Such a site can federate only with the hosted side;
  on-premises to on-premises federation over the tunnel is not supported.
- Trust needs two pairings: a bidirectional federation pairing token generated on the hosted site
  and registered on the on-premises site, and one generated on the on-premises site (with its base
  URL overridden to the tunnel address) and registered on the hosted site.
- On the hosted side the on-premises member is added by URL (tunnel endpoint plus repository key);
  picking it from the site list is not supported. Adding a hosted member from the on-premises side
  is the normal flow.
- Errors: "members from the same host" (switches off), 400 "service not fully operational" during
  start-up (wait a few minutes), 404 at the tunnel endpoint (switches or URL), token errors during
  pairing.
- Expect higher latency and lower throughput than direct federation, depending on the distance
  between the sites and the artifact sizes; very large artifacts may need longer timeouts.
- Status: experimental.

## Licensing and hosted-service plumbing

### REPL-068 · License gating of multi-site features
Priority: **Out of scope**

License tiers that gate multi-target push, event-based pull, metadata-only replication,
federation, identity federation and the tunnel, and limit a lower tier to one push target per
repository. A self-hosted product has no such tiers.

### REPL-069 · Hosted tenant relocation with federation state
Priority: **Out of scope**

When the operator of a hosted service moves a tenant between regions, federation state (repository
configurations, members, modes, enabled flags, member states) is exported after a grace period for
in-flight events (default 10 min) and imported in the new region (status: not started, in
progress, completed, failed); members then pass through *pending full sync*.

### REPL-070 · Usage telemetry from the synchronization service
Priority: **Out of scope**

A daily job (03:00) collects aggregated usage metrics for the hosted-service operator's analytics.

### REPL-072 · Migration from the embedded synchronization mode to the separate service
Priority: **Out of scope**

Federation has two engines, the older one embedded in the repository server and the separate
synchronization service (REPL-045); the embedded one is being retired. A migration tool, shipped
as an extension plugin of the companion client (BLD-048), moves federation configuration,
member states and queued events into the service, and a reverse command moves them back.

- Preconditions: a PostgreSQL database for the service, the service installed and switched on, and
  no full synchronization (REPL-035) running or preparing. An administrator runs the tool with a
  scoped access token. Its options include forcing the switch without draining queued events,
  parallel queue migration, batch size, resuming repositories an earlier run skipped, and HTTP
  client tuning.
- Granularity is the site: every federated repository of one site uses the same engine, while
  members on different sites may run different engines during the transition. Federation keeps
  working while the migration runs. Neither direction needs downtime, and a single server needs no
  restart.
- Status values: started, migrating events into the service, migrating events back (rollback),
  completed, completed with errors. A periodic job (every 4 h by default) marks the status as
  completed on a site that switches the service on before it has any federated repository.
  Flipping the engine flag by hand instead of using the tool leaves configuration and queue state
  inconsistent.
- In a cluster the node that handles the switch applies it and notifies the others. When the
  notification fails, switching back to the embedded engine requires restarting the other nodes.
- Monitoring changes with the engine: the older status and lag operations stop answering, and
  metric names and log locations differ.

A product with a single synchronization implementation has nothing to migrate.

## Notes for implementers

- A federated member serves a file only after its metadata has arrived; content arrives later
  through binary tasks. Clients that deploy on one site and immediately resolve on another must
  expect misses for a while.
- Last event received wins, without merge (REPL-032). Clock skew between sites decides conflicts;
  NTP-grade time synchronization is a hard requirement, not a recommendation.
- Event retention (3 days) is the real recovery horizon: any outage longer than that turns into an
  out-of-sync member and a full synchronization. Keep the value identical on all members.
- Full synchronization copies missing items and never deletes; a lost delete reappears when a full
  sync runs from a member that still has the item (REPL-035).
- In-memory sorting of full-sync file lists must be identical on every member, or matching files
  are copied again on every full sync.
- Receive-only does not mean read-only: local writes succeed, are never propagated, monitoring
  stays healthy, and the next full sync silently reverts them. Enforce it with permissions.
- Federation configuration changes are rejected (409) on a receive-only member; all management
  happens on the sending side.
- Sync deletes in replication is absence-based: an empty or wrongly filtered source purges the
  target (REPL-008). Replication cycles are not detected.
- Combined include/exclude path patterns of a replication job are capped at 1848 characters; a
  longer definition is rejected at save time.
- Event-based pull channels refuse anonymous peers; a pulling remote repository needs upstream
  credentials of a user with read access at the source, and container remotes doing scheduled
  pull must use Basic rather than registry-token authentication.
