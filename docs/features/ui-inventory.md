# UI inventory

This page lists every screen, dialog and navigation element a full-featured repository manager
offers in its web UI, and the client setup instructions it generates for package-manager clients.
It describes what each screen shows, which controls it has, which states it can be in and how the
viewer's permissions change it. The behavior behind a screen (repository semantics, token
semantics, search semantics) is specified on the page that owns the feature; entries here name
that feature's ID and add only what is specific to the UI.

## Shell and navigation

### UI-001 · Application shell and header
Priority: **MVP**

Every page after login shares one frame:

| Element | Position | Behavior |
|---|---|---|
| Project scope selector | header | Chooses the project context of the whole UI, or "all projects" (UI-003). |
| Global search box | header, always present | An object-type selector on its left, a query field, and an advanced-search toggle on its right (UI-019). The preselected object type follows the current page; on the packages page it is package search. |
| User menu | header, top right, behind the profile icon | Personal entries such as the profile and the client setup instructions (UI-004). |
| Area switcher | unspecified | Switches between the user area and the administration area (UI-002). |
| Section navigation | unspecified | The sections and screens of the current area (UI-002). |
| Content pane | remaining space | The current screen. |

The header also carries a help icon that links to help and training material (for trove: its own
documentation), an AI assistant button on hosted instances (UI-060) and, on hosted instances, a
site launcher (UI-059). Unspecified: the position of the area switcher and the section navigation,
a notification center, a theme switch, and behavior on narrow screens. Branding is configurable
(UI-044).

Interactions: everything the frame shows is filtered by the viewer's permissions (UI-005).

### UI-002 · Areas and navigation map
Priority: **MVP**

The UI is split into two top-level areas:

- **User area**: the day-to-day work with content: browsing artifacts, packages and builds, and
  self-service integrations such as webhooks.
- **Administration area**: configuration of repositories, identities, permissions, security,
  authentication providers and system settings. It is used by administrators, project
  administrators and holders of delegated roles; the screen map and each screen's entry state who
  uses which screen (see also UI-005).

<details>
<summary>Screen map</summary>

| Area | Section | Screen | Who uses it | Entry |
|---|---|---|---|---|
| User | Artifact management | Packages list and package details | any user with read access | UI-008, UI-009 |
| User | Artifact management | Artifact browser (tree, details, search) | any user with read access | UI-011, UI-020 |
| User | Artifact management | Builds list and build run details | users with build read access | UI-028, UI-029 |
| User | Artifact management | Release sets and distribution | Later | [distribution-and-lifecycle.md](distribution-and-lifecycle.md) |
| User | Integrations | Webhooks | administrators and webhook managers (IAM-007); not limited to administrators | UI-046 – UI-048 |
| User menu | — | Profile, client setup instructions, quick repository setup | every logged-in user (quick setup: unspecified) | UI-004, UI-053 |
| Administration | Repositories | Repositories list, create and edit forms | administrators, project administrators | UI-030 – UI-033 |
| Administration | Repository settings | Property sets | administrators | UI-034 |
| Administration | Repository settings | General repository settings (UI upload limit, browser page size, global offline mode, global replication blocking, archive indexing, folder download, trash can) | administrators | UI-045 |
| Administration | General management | General settings (server name, base URL, date format, branding, banner, login notice) | administrators | UI-044 |
| Administration | Monitoring | Federation status dashboard | administrators; Later | UI-054 |
| Administration | User management | Users, groups, permissions, access tokens, settings | administrators; managers for their own policies | UI-035 – UI-042 |
| Administration | Security | General security settings | administrators | UI-043 |
| Administration | Security | Keys management (signing keys, keystores, trusted public keys, SSH keys), secret-manager connectors | administrators | [identity-and-access.md](identity-and-access.md) IAM-046 – IAM-050 |
| Administration | Authentication providers | OAuth/OIDC SSO, SAML SSO, LDAP, reverse-proxy SSO, SCIM | administrators | [authentication-providers.md](authentication-providers.md) |
| Administration | Integrations | OpenID Connect provider configurations and identity mappings for CI systems | administrators; project administrators add mappings | UI-049 |
| Administration | Projects (first tab) | All-projects or project overview, project settings, storage quota | administrators, project administrators | UI-050 – UI-052 |
| Administration | User management (one project selected) | Project members, project roles | project administrators with the manage-members privilege, administrators | UI-056, UI-057 |
| Administration | User management ("all projects" selected) | Global roles | administrators | UI-057 |
| Administration | Topology | Connected instances and trust bindings | Later | [replication-and-federation.md](replication-and-federation.md) |
| Administration | Monitoring | Service status per node; system log viewer; storage summary | administrators | OPS-008, OPS-027 in [operations-and-monitoring.md](operations-and-monitoring.md); STOR-039 in [storage-and-blobs.md](storage-and-blobs.md) |
| Administration | Support | Support bundles | administrators | OPS-031 |
| Administration | Services and import/export | Scheduled backups; system and repository export and import (self-managed) | administrators | OPS-032, OPS-034, OPS-036 |
| Administration | General management | Mail server; application integrations | administrators | OPS-060, OPS-049 |
| Administration | Advanced | Editor for the central configuration descriptor | administrators | OPS-042 |
| Administration | Repository settings | Maintenance (garbage collection, unreferenced-data pruning, disk-space thresholds, scheduled jobs) | administrators | STOR-037, STOR-038, STOR-040, OPS-065 |
| Administration | System settings | Repository layouts; format normalization migration; system-wide repository search order | administrators | REPO-050, REPO-067, REPO-068 in [repositories.md](repositories.md) |
| Administration | Proxies; Security | Outbound proxy definitions; client TLS certificates for upstreams | administrators | RMT-048, RMT-007 in [remote-repositories.md](remote-repositories.md) |
| Administration | Retention policies | Cleanup and archive policies, their runs and reports; archive search | administrators; project administrators for their projects' policies | RET-001, RET-027, RET-029 in [retention-and-cleanup.md](retention-and-cleanup.md) |
| Administration | Scripting hooks | Hook list, editor and run history | administrators; Later | EVT-061, EVT-057 in [events-and-extensibility.md](events-and-extensibility.md) |
| Repository forms | Replication tab | Push and pull replication of one repository | administrators; P2 | REPL-002, REPL-005, REPL-006 |
| Administration | Scanner and upstream curation | Indexing scope, scan policies, curation settings | Later | SEC-007, SEC-042, SEC-050 in [security-scanning-and-curation.md](security-scanning-and-curation.md) |

The rows below the topology row point at screens whose fields and behavior are specified on the
page that owns the feature; this page does not repeat them.

</details>

Deep links: stable routes are specified only for a handful of administration screens: the
federation status dashboard and its per-repository connection view, the "new local repository"
form, and the edit forms of local and federated repositories (route built from the class and the
repository key). Whether the repositories list, the forms of the other classes and every other
screen and object (package version, build run, artifact path) have a bookmarkable address is
unspecified; "show in artifact tree" links (UI-011) imply at least an addressable tree selection.

### UI-003 · Project scope selector
Priority: **P1**

A project drop-down in the header defines the scope of what the content pane shows.

| Selection | Effect |
|---|---|
| A specific project | Screens show that project's resources; some screens additionally show instance-wide information. The first tab of the administration area becomes the **project overview** (UI-050) with "edit" and "delete" controls for the project. |
| All projects | Screens show the resources of every project. The first tab of the administration area becomes the **all-projects overview** (UI-050). |

Scope-dependent behavior:

- Projects act as filters: users see only the resources tied to the projects and roles they hold.
- Webhooks: "all projects" lists every webhook with a project column; a project lists only its
  own webhooks, and webhooks created there are limited to artifact, container-image,
  artifact-property and build events (UI-047).
- Repositories: some row actions exist only inside a project (UI-030); inside a project the
  environment field of a new repository is mandatory (UI-031).
- Client setup and the "all projects" views list only the virtual repositories relevant to the
  viewer's projects.
- Some global settings are only reachable with "all projects" selected (UI-043).

Unspecified: which projects a non-member sees in the drop-down, whether the selection persists
across sessions, and the default selection after login.

Interactions: project assignment of repositories (REPO-013 in [repositories.md](repositories.md));
project roles in [identity-and-access.md](identity-and-access.md).

### UI-004 · User menu
Priority: **MVP**

Opened by clicking the user's initials or profile icon at the top right of the header. Known
entries:

| Entry | Opens |
|---|---|
| Edit profile | The profile page (UI-053) |
| Client setup instructions | The format picker of the client setup dialog (UI-021) |
| Quick repository creation | The quick setup wizard (UI-033) |

Unspecified: where the logout control sits (under single sign-on users log out of the UI manually,
but the control has no defined place), and whether the quick-setup entry is shown to users who
cannot create repositories.

### UI-005 · Permission-driven rendering
Priority: **MVP**

Authorization is enforced by the operations behind each screen (see
[identity-and-access.md](identity-and-access.md)); hiding a control is never the protection. On
top of that, the UI adapts what it shows to the viewer:

<details>
<summary>Rendering rules</summary>

| Situation | UI behavior | Entry |
|---|---|---|
| Content the viewer cannot read | Repositories, folders and files without read permission are absent from the tree, the search results and the package list; the trash can lists only items the viewer has permissions for. | UI-011, UI-018 |
| Actions on an item | The action menu offers only actions the viewer's permissions allow; the set also depends on the item kind (repository, folder, file). | UI-012 |
| Followers tab | Shown to administrators and to users with the manage action on the item. | ART-046 |
| Trash can contents and restore | The manage action covers restoring from the trash can. In addition, a user with deploy or manage on a repository sees the trash can and the deleted items that came from that repository; with delete on it as well, the user can restore them. Items from other repositories stay invisible. Administrators see and restore everything. | UI-018, UI-045 |
| Account with UI access disabled | Cannot use the UI at all; only the API accepts the account. | IAM-003 |
| Auditor role | Sees every screen but cannot create, change or delete anything. In the user editor the auditor role excludes every other role: ticking it greys out the others and vice versa. | IAM-006, UI-036 |
| Delegated roles (manage resources, manage webhooks) | Unlock the corresponding administration screens without full administration. A webhook manager is limited to artifact, artifact-property and container-image events; can edit a webhook only with read access to the repository it covers and only if it does not use the "any local" / "any remote" selectors; cannot use include or exclude patterns. | IAM-007, UI-047 |
| Permission policy editor | A user cannot edit the actions granted to themselves (protects against self-lockout). A user holding the manage action can grant other principals only actions they hold themselves, and never manage; an operator switch can loosen this, which is not advised. In the older editor variant, administrators cannot be added as principals (they hold every action anyway), and only actions of installed subsystems are listed. | UI-040 |
| Anonymous viewer | With anonymous access on, a visitor who is not logged in sees exactly what the anonymous user may read. An option on the anonymous user routes every anonymous visitor to the login page instead. | UI-038 |
| Project scope | Some global settings are reachable only with "all projects" selected (for example hiding global identity mappings); some repository actions (connecting to a shared repository, creating another internal remote repository) are unavailable in the "all projects" scope. | UI-003, UI-030, UI-043 |
| Missing data or subsystem | On the build run page a detail view is absent when its data was not collected, the viewer lacks permission, or the subsystem is not enabled; the environment tab instead stays and shows zero counts. Search types and editor tabs of subsystems that are not installed are not offered. | UI-019, UI-029, UI-040 |
| Tokens with credentials | Install commands that need credentials ask for the password again before generating a token; client setup offers "generate token" or "use your own token". | UI-010, UI-022 |

</details>

Unspecified: whether denied actions are hidden or shown disabled (both patterns occur: own
permissions are disabled; menu actions "vary" by permission), and the empty-state text shown to a
user who can read nothing.

### UI-006 · Login screen
Priority: **MVP**

The entry screen for users who are not logged in. Elements: one button per enabled single
sign-on provider, the username/password form (unless hidden by the security settings), a
"remember me" option, and a link below the provider buttons that reveals the username/password
form when it is hidden. With the "hide providers" option the user types the provider name instead
of picking it from a list. Users with a second factor get the one-time-password prompt of
AUTHN-035.

Further elements:

- **Forgot password**: a link on the login form opens a dialog asking for the username; "submit"
  sends an email with a password-reset link to the address stored for that user (AUTHN-042).
  Behavior for externally authenticated users is unspecified.
- **Login notice**: when an administrator enables the custom login dialog (UI-044), a message
  window with title, icon type and text appears before login, and users must acknowledge it to
  continue.
- **Custom banner**: the custom message of UI-044 appears at the top of the screens (whether also
  on the login screen is unspecified).

Behavior and settings are specified in AUTHN-037 (presentation), AUTHN-038 (remember me) and
AUTHN-035 (second factor) in [authentication-providers.md](authentication-providers.md).

Interactions: anonymous start page (UI-038); bootstrap administrator login (IAM-010).

### UI-007 · First-run onboarding wizard
Priority: **P2**

Self-hosted installations only. On the first login after installation a wizard opens
automatically ("get started") and walks through the minimum configuration:

| Step | Content | Rules |
|---|---|---|
| 1. Welcome | "Get started" | — |
| 2. Administrator password | Replace the initial administrator password | At least 8 characters with an uppercase letter, a lowercase letter and a digit; the wizard does not advance otherwise |
| 3. Base URL | The externally visible base URL, entered as a full URL with protocol (`https://host.example`) | Validation fails when the host does not resolve from the browser's network or the format is wrong |
| 4. Outbound proxy | Optional default proxy for remote access | Skippable; if used, key, host and port are all required |
| 5. Default repositories | Pick the formats for which default repositories are created | — |
| Extra steps | One per installed subsystem, e.g. choosing repositories for scan indexing | Present only with that subsystem |
| Finish | "Finish" | — |

Behavior:

- The wizard runs once; it does not reappear on later logins. All its settings remain editable in
  the administration area (UI-044 for the base URL).
- When a new subsystem is added to an existing installation, the wizard reappears to configure
  that subsystem.
- Optional steps can be skipped without blocking completion.
- If it does not appear on first login, a stale browser session is the usual cause.

Unspecified: the names and upstreams of the default repositories created in step 5 (compare
UI-033), and what happens when the wizard is abandoned midway.

Interactions: base URL used by client setup snippets (UI-023); bootstrap administrator (IAM-010 in
[identity-and-access.md](identity-and-access.md)).

### UI-058 · Supported browsers
Priority: **MVP**

The UI is tested against the latest versions of four browsers: Chrome, Firefox, Safari on macOS,
and Edge in its Chromium-based form. Older versions, other browsers and mobile browsers are not
covered; whether they work is unspecified, as are a minimum version and a policy for how long a
browser version stays supported.

For trove: the Angular UI targets the same evergreen set; the supported-browser list belongs in
the user documentation so that support requests from other browsers can be answered against it.

## Packages and artifacts

### UI-008 · Packages list screen
Priority: **MVP**

UI location: user area, artifact management, packages. Behavior and filter semantics: ART-048 in
[artifacts.md](artifacts.md).

Layout:

- **Tabs** across the top: "recently viewed" (the last 10 packages whose details the viewer
  opened), "all packages", and up to 8 custom views. Custom views can only be created from the
  "all packages" tab.
- **Filters panel** on the "all packages" tab: format (multi-select), name (substring),
  description text, keyword (exactly one), creation period (last 7, 30 or 90 days, or a custom
  range; measured on the package's first version). Each applied option is marked with an
  indicator dot.
- **Save new view** in the filters panel opens a small dialog asking for a view name (keep it
  short; the tab has little room). "Update view" overwrites the filters of the open custom view.
- **Custom view menu**: hovering a custom view's tab reveals an actions button with rename,
  delete and "make default".
- **List**: one row per package with summary information on its latest version; the first display
  lists every package sorted lexicographically by name. Clicking anywhere on a row opens the
  package details (UI-009). For some formats the list also offers a download and a copyable install
  command.

Edge cases:

- Custom views live in the browser's storage only: they are lost when the browser data is
  cleared and do not follow the user to another browser.
- Which summary columns the list shows and the empty-state text are unspecified.

Backing operations: unspecified (a package listing with the filters above).

### UI-009 · Package details screen
Priority: **MVP**

Opened from the packages list or from the global search (package type). Shows the
latest version or tag by default. Field and tab semantics: ART-049 in [artifacts.md](artifacts.md).

| Region | Content |
|---|---|
| Header | Version or tag shown, publish date, download count of this version or tag, scan summary by severity (hover shows a legend), licenses |
| Version selector | Drop-down list of versions with a free-text field; a "release versions only" switch where the format distinguishes pre-releases |
| "All versions" view | Table of all versions with their main details and the same "release versions only" switch; clicking a row selects that version |
| Tabs | Info (for container and OCI formats the manifest or manifest list), readme, repositories (each with "show in artifact tree"), builds (produced by / used by, each build linking to its build page), release sets, evidence, scan data |
| Install panel | UI-010 |

For container images the download count is per tag; with shared-digest statistics enabled, a pull
of any tag or digest sharing a manifest updates the counts of every readable tag that references
that digest in the same repository and image (see ART-029).

Adding a package to "recently viewed" happens when its details page is opened.

Unspecified: behavior when the package has no readable version left, and the order of the
versions in the selector.

### UI-010 · Install command panel
Priority: **MVP**

Part of the package details (UI-009).

- A **client** picker lists every supported client of the format (for container images both
  Docker and Podman).
- A **repository** picker appears when the selected version exists in more than one repository.
- The panel renders the install command for client, repository and version; a copy button copies
  it. Multi-line commands show their first line and a "show more" expander.
- For formats whose clients need explicit credentials in the command, the panel first shows a
  **generate token** prompt: the user re-enters their password and confirms; the panel then shows
  the command with the generated credential embedded. Which kind of token is generated and its
  expiry are unspecified (the client setup dialog generates identity tokens, UI-022; see AUTHN-040
  in [authentication-providers.md](authentication-providers.md)).

Edge cases: users who log in through single sign-on have no internal password to re-enter; how
the prompt behaves for them is unspecified.

Interactions: the client setup dialog (UI-021) produces the client configuration these commands
assume; snippet notation in UI-023.

### UI-011 · Artifact browser screen
Priority: **MVP**

UI location: user area, artifact management, artifacts.

| Region | Content | Entry |
|---|---|---|
| Toolbar / action menu | "Deploy" (opens UI-013) and actions for the selected node | UI-012 |
| Tree | All readable repositories, expandable into folders and files; repositories sorted by class (virtual, local, remote, remote cache) unless re-sorted; the trash can as the last node when enabled | ART-040, UI-018 |
| Tree filters | Repository name, class, format, favorites only, compact view (collapse single-child folder chains), sort by class, key or format, clear | ART-040, SRCH-062 |
| Details pane | Tabs for the selected node: general, effective permissions, properties, followers, builds, scan results, evidence — each only where it applies | ART-041 |
| Search field | Searches repositories, folders and files across the tree; switches the content pane to a results table | UI-020 |

Navigation: other screens deep-link into the tree with "show in artifact tree" (from the
repositories list, the package repositories tab and search results); from search results the
tree opens filtered to the item's repository. The tree remembers its filters per browser (as
cookies in one variant).

Further tree behavior:

- **Large folders**: each level is loaded in batches of a configurable size; a "load more" entry
  at the end of the list fetches the next batch (UI-045).
- **Uncached remote content**: for a remote repository whose upstream is another instance of the
  same kind with remote folder listing switched on, the tree also lists items that are not cached
  yet; the first listing can be slow and is then cached for the remote's retrieval cache period
  (ART-044).
- **General tab sections**: item information, a dependency declaration snippet, the virtual
  repositories that include the item, and checksums. For large uploads the "created" timestamp can
  be later than "last modified", because the latter records the start of the upload and the
  former its commit.

Unspecified: the default batch size for large folders, and the empty-state text of an
installation without repositories.

### UI-012 · Item actions and their required permissions
Priority: **MVP**

Actions are reached by right-clicking a tree node, through the action menu in the details pane,
or on a search-result row. The offered set depends on the node kind and on the viewer's
permissions; the label can vary with the node kind (for example "delete" for a file, "delete
content" for a folder).

<details>
<summary>Action catalog</summary>

| Action | Node kinds | Required permission | Entry |
|---|---|---|---|
| Deploy (upload) | toolbar; target chosen in the dialog | deploy on the target | UI-013, ART-006 |
| Download | file (tree, details); search-result rows | read | — |
| Download folder as archive | folder (when enabled system-wide) | read | UI-015 |
| Copy content | folder, file | unspecified | UI-014, ART-032 |
| Move content | folder, file | delete on the source and deploy on the target | UI-014, ART-033 |
| Delete / delete content | folder, file | delete | UI-016, ART-034 |
| Delete versions | folder | delete | UI-017 |
| Recalculate index | repository (tree context menu; also a row action of the repositories list) | unspecified | REPO-009 in [repositories.md](repositories.md) |
| Copy path | folder, file | unspecified beyond seeing the node (read) | — |
| Invalidate cache (marks the cached metadata stale so the next request refreshes it from the upstream; content is not deleted) | remote repository cache (context menu or action menu) | manage or delete on the remote repository | ART-050, [remote-repositories.md](remote-repositories.md) |
| Directory-listing browser | repository, folder, file | unspecified beyond seeing the node (read) | ART-042 |
| Client setup instructions | repository | unspecified beyond seeing the node (read) | UI-021 |
| Add to / remove from favorites | repository | unspecified beyond seeing the node (read) | SRCH-062 |
| Show in artifact tree | search result, package repository | unspecified beyond seeing the node (read) | UI-011 |
| Add property, add recursively, delete property, delete recursively | repository, folder, file (properties tab) | annotate | ART-021 – ART-025 |
| Manage followers | repository, folder, file (followers tab) | manage, or administrator | ART-046 |
| Search, refresh, empty trash can; restore | trash can node; items in it | restore: manage, or deploy/manage plus delete on the origin repository (UI-005); search and refresh: seeing the node; empty: unspecified | UI-018, ART-036 – ART-038 |
| Ignore violation | file (scan results tab) | unspecified | [security-scanning-and-curation.md](security-scanning-and-curation.md) |

</details>

### UI-013 · Deploy dialog
Priority: **P1**

Opened from the artifact browser's action menu. Fields: target repository, a "single file"
tab (default) and a "multiple files" tab, a drop zone / file picker, an optional target path
(also accepts matrix parameters), and file-type dependent options (deploy according to layout,
deploy as bundle archive, deploy as Maven artifact with editable coordinates, generate default
POM or use the JAR's embedded POM). A "deploy" button submits.

Behavior, options, limits (forbidden characters in names; a per-file size limit for UI uploads,
100 MB and adjustable by an administrator in UI-045, where 0 means unlimited) and the causes of
failure are specified in ART-006 in [artifacts.md](artifacts.md). Deploying to a virtual
repository needs a default deployment repository (REPO-038 in
[repositories.md](repositories.md)).

### UI-014 · Copy and move dialog
Priority: **P1**

Opened by "copy content" or "move content" on a folder or file. Semantics: ART-032 and ART-033 in
[artifacts.md](artifacts.md).

| Field | Meaning | Default |
|---|---|---|
| Target repository | Picker limited to repositories of the item's format or of the generic format: local ones, plus virtual ones that forward deploys to a local member (REPO-038) | none |
| Custom path | Checkbox; when set, a target path field appears | off (the item lands in the target repository's root) |
| Target path | Folder in the target repository | unspecified |

Buttons: "dry run" simulates the operation and lists errors and warnings without changing
anything; "copy" / "move" executes it. Metadata of affected packages is recalculated after the
operation.

Edge cases:

- Copy follows Unix semantics: copying folder `a/b/1` to `a/b/1` in the target yields
  `a/b/1/1`; to keep the same path, copy into the parent folder.
- A custom path suppresses translation between different repository layouts, so clients may not
  resolve the result even between repositories of the same layout.
- Typical dry-run findings: the target's policies reject some items; missing permissions.

### UI-015 · Folder download dialog
Priority: **P2**

Opened by "download" on a folder (tree context menu or the details pane) when an administrator
has enabled folder download in the general repository settings (UI-045); anonymous viewers get
it only when the separate anonymous switch there is on as well. Semantics: ART-015 in
[artifacts.md](artifacts.md).

| Field | Meaning | Default |
|---|---|---|
| Archive type | `zip`, `tar`, `tar.gz` or `tgz` | unspecified |
| Include checksum files | Adds `.md5`, `.sha1`, `.sha256` files under the same conditions under which they appear in the directory listing; for remote repository caches the files are always added | unspecified |

Limits (set in the general repository settings, UI-045): maximum total size in MB, maximum number
of files (folder and all subfolders), maximum concurrent folder downloads. Defaults: unspecified.

Edge case: if any file in the folder is blocked for download (for example by a scan policy), the
whole download fails with 403.

### UI-016 · Delete item action
Priority: **MVP**

"Delete" on a file or "delete content" on a folder, from the tree context menu or the details
action menu. The item is removed and the package metadata of the repository is updated so that
the item no longer appears in searches or client metadata. When the trash can is enabled, items
deleted from local repositories go there (UI-018). Semantics: ART-034 in
[artifacts.md](artifacts.md).

Unspecified: the wording and form of the confirmation step.

### UI-017 · Delete-versions dialog
Priority: **P2**

"Delete versions" on a folder opens a dialog listing every group/version combination found below
the folder, each with a checkbox, and a "delete selected" button. It removes all files of the
chosen versions in one step and keeps package metadata consistent. See ART-035 in
[artifacts.md](artifacts.md).

Limit: the dialog collects versions from the first 1,000 artifacts found at the selected level; a
filter by group ID or version narrows the scan when the wanted version is missing.

### UI-018 · Trash can in the tree
Priority: **P1**

When an administrator (or a user with the manage action) has enabled the trash can, a "trash can"
node appears as the last node of the tree. It holds items deleted from local repositories for the
configured retention period. The switch, the retention period in days and a second "empty trash
can" button live in the general repository settings (UI-045). Semantics: ART-036 – ART-038 in
[artifacts.md](artifacts.md).

| Action | Where | Behavior |
|---|---|---|
| Search trash can | context menu of the node | Switches the global search to the trash search type with the query field prefilled (SRCH-053) |
| Empty trash can | context menu of the node | Confirmation, then permanent deletion of everything in it |
| Refresh | context menu of the node | Reloads the node's content |
| Restore | context menu of an item, or the details action menu | Opens the restore dialog |

Restore dialog: target repository (picker), "restore to a custom path" checkbox with a target
path field, "restore" button.

The trash can's content is filtered by the viewer's permissions: a user with deploy or manage on a
repository sees the items deleted from it, and with delete on it as well can restore them; the
manage action also covers restoring (UI-005). Release-set versions in the trash can can neither be
searched nor restored from the UI (API only).

Unspecified: the permission needed to empty the trash can from the tree node, and the restore
dialog's preselected target.

### UI-019 · Global search box
Priority: **P1**

Header element available on every page (UI-001). The object-type selector offers packages,
artifacts, builds, release sets and scanned resources (the last two only with those subsystems);
the advanced-search forms additionally cover archived items where cold storage is in use.
Typing a query and pressing Enter opens the result list of that object type; clicking a result
opens the object's page. The advanced-search toggle opens a form for the selected type; each form
field writes the corresponding keyword into the query field, so form and keyword syntax stay in
sync.

Result-list details:

- Artifact results carry a per-row actions control at the far right; which actions appear depends
  on the item.
- After a package-type artifact search, a "show query" button displays the query-language
  expression the search ran, with a copy button (SRCH-054; the query language itself is
  SRCH-023 and later, P2).

Semantics, keywords, per-type forms and limits: SRCH-048 – SRCH-057 in
[search-and-query.md](search-and-query.md).

### UI-020 · Artifact search on the browser page
Priority: **P1**

A search field on the artifact browser that searches repositories, folders and files by name and
path prefix, with scope keywords for repository, path and checksum, suggestions (at most five)
after four characters, recent searches, an "exact matches only" switch, a results table with
selectable columns, filters and per-row actions (show in tree, favorites, download, delete), and
keyboard shortcuts. Selecting a location in the tree first puts a "find in" scope chip into the
search field. The result order is fixed (item kind, then modification time) and cannot be changed.
A breadcrumb or back arrow returns to the tree.

This search exists in one deployment variant only; the other variant offers just the tree filters
of UI-011.

Semantics and limits: SRCH-058 – SRCH-063 in [search-and-query.md](search-and-query.md).

## Client setup instructions

### UI-021 · Client setup dialog
Priority: **MVP**

A dialog that turns "which repository do I use, and how do I configure my client" into
ready-to-paste configuration and commands, with the viewer's server address, repository and
credentials already filled in. Prerequisite: a repository of the wanted format exists (UI-031,
UI-033).

Entry points:

| Entry point | Starts with |
|---|---|
| User menu → client setup instructions | A package-type selection pane listing formats and clients |
| Repositories screen → row action (UI-030) | The row's repository |
| Artifact browser → repository node → client setup button (UI-011) | The selected repository |
| Federated repository (UI-027) | The federated repository |

Flow:

1. Pick the format or client (when opened from the user menu).
2. **Repository** field: pick one of the repositories of that format.
3. **Client** drop-down: only for formats with several clients (for example npm, pnpm and Yarn;
   NuGet CLI, .NET CLI, PowerShell and Chocolatey).
4. **Configure tab**: choose how credentials enter the snippets (UI-022), then press "generate
   token and create instructions" or "get instructions".
5. The configure tab shows the configuration steps for the client; further tabs (for example
   **publish** and **install**) show snippets for those tasks. The set of tabs, their names, the
   buttons and the content vary by format and client.
6. "Select a different package type" returns to step 1.

Edge cases:

- A virtual repository yields deploy/publish instructions only when it has a default deployment
  repository (REPO-038 in [repositories.md](repositories.md)); deploys sent to the virtual
  repository then land in that local repository.
- Unspecified: the empty state when no repository of the chosen format is readable, and whether
  the repository list contains only repositories the viewer can read.

Interactions: the install commands on the package details (UI-010) are the per-version
counterpart; the CI workflow snippet of the OIDC integration screen (UI-049, CIOIDC-024) is a
separate generator.

### UI-022 · Credential step and token embedding
Priority: **MVP**

The configure tab offers two choices:

| Choice | Behavior |
|---|---|
| Generate token | Creates a new identity token for the viewer (their own permissions) and embeds it in every snippet. See AUTHN-040 in [authentication-providers.md](authentication-providers.md) and TOK-005 in [access-tokens.md](access-tokens.md). |
| Use your own token | A field accepts an existing token, for example pasted from a password manager; the dialog embeds it as given. Whether it is validated is unspecified. |

Security: the rendered snippets contain a live credential. The install command panel (UI-010)
asks for the password again before it generates a token; no such step is specified here.

Unspecified: whether "generate token" asks for re-authentication, which description and expiry the
generated token receives, whether it then appears in the profile's identity-token list, and whether
anonymous-readable repositories get snippets without credentials. Requirement of this catalog: the
generated token is an ordinary identity token, so it gets the default identity-token expiry
(UI-043, TOK-018) and is listed and revocable in the profile (UI-053) like every other identity
token.

### UI-023 · Snippet templating
Priority: **MVP**

Snippets are templates over a small set of values. The filled-in values are specified only as
"environment and authentication information"; this page uses the following notation for them:

| Placeholder | Value |
|---|---|
| `<base-url>` | The system's externally visible base URL (general settings, UI-044) |
| `<repo-key>` | Key of the chosen repository |
| `<endpoint-url>` | The chosen repository's address for the format's client protocol (REPO-005 in [repositories.md](repositories.md)) |
| `<username>` | The viewer's username |
| `<token>` | The token from the credential step (UI-022) |
| `<package>`, `<version>` | Package coordinates (install commands only, UI-010) |

Rules:

- Commands are copied with a copy button; long multi-line commands are collapsed to their first
  line with an expander (specified for install commands, UI-010).
- Deploy snippets appear only where deployment is possible (UI-021).
- Consistency requirement of this catalog: the dialog and the install
  commands render from one template set, so they cannot disagree.

### UI-024 · npm client instructions
Priority: **MVP**

Clients offered: npm, pnpm and Yarn (modern, version 2 and later, and classic, version 1).
Pointing clients at a virtual repository as the default registry is recommended. In the
snippets below `<endpoint-url>` is the repository's npm registry URL with a trailing slash and
`<endpoint-path>` the same URL without the scheme (`//host/…/`), as npm expects for per-registry
credentials.

<details>
<summary>Configure tab</summary>

npm, interactive login against the default registry or a specific one:

```shell
npm config set registry <endpoint-url>
npm login --auth-type=web
npm login --registry=<endpoint-url> --auth-type=web
npm login --registry=<endpoint-url> --auth-type=web --scope=@<scope>
```

npm and pnpm, credentials written to `.npmrc` (for automation; unscoped and scoped variants):

```ini
registry=<endpoint-url>
<endpoint-path>:_authToken=<token>

@<scope>:registry=<endpoint-url>
<endpoint-path>:_authToken=<token>
```

pnpm, interactive:

```shell
pnpm config set registry <endpoint-url>
pnpm login --auth-type=web
pnpm login --registry=<endpoint-url> --scope=@<scope>
```

Yarn modern:

```shell
yarn config set --home npmRegistryServer "<endpoint-url>"
yarn config set --home 'npmRegistries["<endpoint-url>"].npmAlwaysAuth' true
yarn config set --home 'npmRegistries["<endpoint-url>"].npmAuthToken' <token>
yarn config set unsafeHttpWhitelist --home --json '["<host>"]'   # only for plain-HTTP servers
```

Yarn classic:

```shell
npm config set registry <endpoint-url>
npm login --auth-type=web
echo "always-auth=true" >> ~/.npmrc
```

</details>

<details>
<summary>Install and publish tabs</summary>

| Client | Install | Publish |
|---|---|---|
| npm | `npm install <package>`; `npm install <package> --registry <endpoint-url>` | `"publishConfig": {"registry": "<endpoint-url>"}` in `package.json`, then `npm publish`; or `npm publish --registry <endpoint-url>` |
| pnpm | `pnpm add <package>`; `pnpm add <package>@<version>` | same `publishConfig`, then `pnpm publish` |
| Yarn modern | `yarn add <package>` (optionally `@<version>`) | `yarn npm publish` |
| Yarn classic | `yarn add <package>` | `publishConfig` in `package.json`, then `yarn publish`; or `yarn publish --registry <endpoint-url>` |

Without a `publishConfig`, `npm publish` and `pnpm publish` publish to the default registry from
`.npmrc`. Scoped package names include the `@<scope>/` prefix.

</details>

Edge cases:

- Publish snippets make sense only for local repositories and virtual repositories with a default
  deployment repository (UI-021).
- npm metadata requests through a virtual repository need read access to the metadata paths, not
  only to the tarballs (see UI-040).
- npm format behavior: [formats/README.md](formats/README.md) and the npm format page.

### UI-025 · NuGet client instructions
Priority: **MVP**

Clients offered: NuGet CLI, .NET CLI, Visual Studio, PowerShell and Chocolatey. The tabs of the
NuGet variant are **configure** and **deploy**, plus install snippets. Placeholders:
`<v3-index-url>` is the repository's NuGet v3 service index URL, `<v2-url>` its NuGet v2 feed URL,
`<source>` a local name for the package source (the snippets use a fixed example name).

<details>
<summary>Configure tab</summary>

NuGet CLI (v3, and v2 for repositories configured for the v2 protocol):

```shell
nuget sources Add -Name <source> -Source <v3-index-url> -username <username> -password <token>
nuget setapikey <username>:<token> -Source <source>
nuget sources Add -Name <source> -Source <v2-url> -username <username> -password <token>
```

.NET CLI:

```shell
dotnet nuget add source <v3-index-url> --name <source> --username <username> --password <token>
dotnet nuget add source <v2-url> --name <source> --username <username> --password <token>
```

Visual Studio: Tools → NuGet Package Manager → Package Manager Settings → Package Sources; add a
source with a name and the repository URL. Symbol server: Tools → Options → Debugging → Symbols;
add the repository's symbol URL.

PowerShell:

```powershell
$credential = New-Object PSCredential('<username>', $(ConvertTo-SecureString '<token>' -AsPlainText -Force))
Register-PSRepository -Name <source> -SourceLocation "<v2-url>" -PublishLocation "<v2-url>" -InstallationPolicy Trusted -Credential $credential
Register-PSRepository -Name <source> -ScriptSourceLocation "<v2-url>" -ScriptPublishLocation "<v2-url>" -InstallationPolicy Trusted -Credential $credential
```

Chocolatey (v2 recommended; v3 via the service index URL):

```shell
choco source add -n <source> -s="<v2-url>" --priority 1 --bypass-proxy --allow-self-service
choco apikey -s "<v2-url>" -k <username>:<token>
```

</details>

<details>
<summary>Install and deploy tabs</summary>

| Client | Install | Deploy (push) |
|---|---|---|
| NuGet CLI | `nuget install <package>`; `nuget install <package> -Source <source>` | `nuget push <package>.nupkg -Source <source>`; symbols alone: `nuget push <package>.snupkg -Source <source>`; a `.snupkg` lying next to the `.nupkg` is pushed together with it |
| .NET CLI | `dotnet restore` | `dotnet nuget push <package>.nupkg --api-key <api-key> --source <source>` (the key value is unspecified) |
| PowerShell | `Install-Module -Name <module> -Repository <source> -Credential $credential`; `Install-Script` likewise | `Publish-Module -Path <path> -Repository <source> -NuGetApiKey <username>:<token>`; `Publish-Script` likewise |
| Chocolatey | `choco install <package> -Source <source>` | `choco push <package> -s <source>` |

</details>

Edge cases:

- Deploy snippets apply to local and virtual repositories, never to remote repositories.
- To make a client resolve from this repository first, the source must be first in the client's
  configuration or be named explicitly with `-Source`.
- With anonymous access enabled, the server does not challenge NuGet clients for credentials, so
  they send none; the repository option "force authentication" (REPO-028 in
  [repositories.md](repositories.md)) makes the server challenge first.
- For repositories with a custom layout, the push snippet appends a layout path (for
  example a release folder) to the repository URL, so the package lands where the layout expects
  it.
- NuGet CLI 3.5 and later need no separate symbol source.

Format behavior: REPO-030 and REPO-031 in [repositories.md](repositories.md) and the NuGet format
page under [formats/](formats/README.md).

### UI-026 · Instructions for other formats
Priority: **P1**

The dialog covers every supported format, among them Maven, npm and Docker, and offers Docker and
Podman as clients of container repositories. Each format's snippets are the
native client configuration documented on its format page under
[formats/](formats/README.md), rendered with the placeholders of UI-023. Formats that need
explicit credentials in commands embed the token from the credential step.

### UI-027 · Federated repository instructions
Priority: **Later**

Opened from the artifact browser with a federated repository selected: pick the build tool and
copy the generated configuration. The generated address always points at the local member of the
federation, so federation stays invisible to clients.

Permission rule for this variant: read permission yields resolve snippets, deploy
permission additionally yields deploy snippets. Whether the same rule applies to other repository
classes is unspecified.

See [replication-and-federation.md](replication-and-federation.md).

## Builds

### UI-028 · Builds list
Priority: **P1**

UI location: user area, artifact management, builds. One row per build name the viewer can read.

| Column | Meaning |
|---|---|
| Build name | Name of the build; opens the list of its runs |
| Last build number | Number of the most recent published run |
| Published modules | Modules published by the last run (whether shown as a count or a list is unspecified) |
| Project | Project the build belongs to |
| Build repository | Repository holding the build records |

Opening a build name lists its runs; selecting a build number opens the run details (UI-029).

Empty state: a build is missing from the list when its record was not published, when it was
published in another project context, or when the viewer lacks read access to it. The UI shows no
specific hint (unspecified).

Interactions: build permissions (IAM-025 in [identity-and-access.md](identity-and-access.md)),
build metadata in [builds-and-ci.md](builds-and-ci.md), build search (SRCH-055).

### UI-029 · Build run details
Priority: **P1**

| Region | Content |
|---|---|
| Header | Start time, duration, build tool, CI server, link to the CI run |
| Published modules tab | Per module: ID, number of artifacts, checksum columns; per module the dependency list with scopes |
| Environment tab | Environment variables and system variables captured at build time |
| VCS tab | Git metadata, when the publisher collected it |
| Issues tab | Linked work items when collected |
| Diff tab | Content unspecified |
| Release history tab | Content unspecified |
| Scan data tab | Security data of the build (only with a scanning subsystem) |
| Evidence tab | Evidence attached to the run |
| Effective permissions | Who can access the build (IAM-030) |
| Record view | The raw build record as JSON |

Empty states: when the publisher did not collect environment data the tab shows "0 environment
variables" and "0 system variables"; that is not an error. A tab for data that was not collected,
or for a subsystem that is not enabled, is absent.

Operations behind the screen (append, promote, discard, delete) are described in
[builds-and-ci.md](builds-and-ci.md) as command-line and API operations, not as UI actions;
whether the screen offers them is unspecified.

## Administration: repositories

### UI-030 · Repositories screen
Priority: **MVP**

UI location: administration area, repositories. Views, filters and row actions: REPO-007 in
[repositories.md](repositories.md). UI specifics:

- A "create a repository" button opens a menu of repository classes (UI-031).
- Views reached through tiles: all repositories, recently viewed (20), inactive, shared.
- The filter panel is available only in the all-repositories view and has a "clear all" control.
- Row actions appear behind a three-dot button that shows when hovering the right end of a row.
- Clicking a repository key opens its edit form; opening a form in edit mode counts as "viewed"
  even if nothing is saved.
- Recalculate index is marked as heavy for large repositories.
- Where federation is enabled on local repositories, a "multi-site" column shows federation,
  replication or both; the create menu and the class filter then omit "federated" (federation is
  switched on in a local repository's multi-site tab, UI-031), and a row action opens the
  repository's federation status (UI-054).
- One variant of the screen groups the list in tabs per repository class.

### UI-031 · Create and edit repository form
Priority: **MVP**

Flow:

1. "Create a repository" → choose the class (local, remote, virtual; in one variant also
   federated).
2. A format picker shows one icon per supported format; picking one opens the form. The format
   cannot be changed later (REPO-002).
3. The form has tabs: **basic** (key, environment/stage, include/exclude patterns and the other
   mandatory settings; for virtual repositories the member selector, UI-032), **advanced**
   (property sets, priority resolution, download redirect, content browsing, format-specific
   settings) and, for local and remote repositories, **replications** (an event-replication
   checkbox plus a list of replication targets). Virtual repositories have only the basic and
   advanced tabs. Where federation is offered, local repositories get a **multi-site** tab for
   federation members, and existing federated repositories open on a **federation** tab.
4. A class-specific submit button ("create local repository", "create virtual repository", …).

Edit mode opens the same tabs for an existing repository with a "save" button.

Remote repositories: the basic tab starts with a choice between an **external** upstream and an
**internal** one (only inside a project that received a shared repository of the same format
with project-scoped connections, REPO-015); the client-certificate field has a **test** button
that verifies the certificate.

Field semantics: [repositories.md](repositories.md) (local REPO-016 – REPO-032, virtual REPO-033 –
REPO-045) and [remote-repositories.md](remote-repositories.md).

Edge cases:

- The include pattern defaults to "include everything"; removing that row in the UI only hides
  it — the default stays in effect.
- Inside a project the environment field is mandatory and preselects the development environment.

Validation shown in the form: key rules of REPO-004 (length per class, forbidden characters,
reserved names and suffixes).

### UI-032 · Virtual repository member selector
Priority: **MVP**

Part of the basic tab of a virtual repository.

- Two lists: **available** repositories (same format, local, remote and virtual) and **selected**
  repositories. Items move by selection plus arrow buttons or by drag and drop.
- The selected list can be reordered by drag and drop; the order is the resolution order inside
  each class (local first, then remote caches, then remote upstreams; REPO-035).
- An **included repositories** view shows the effective list: nested virtual repositories are
  expanded into their local and remote members, and the view updates when a nested virtual
  repository changes.
- A **default deployment repository** picker lists the selected local repositories (REPO-038).
- If a member is neither assigned to nor shared with the virtual repository's project, a warning
  appears with a "view" control listing the conflicting repositories and an export of that list as
  CSV.
- Invalid nesting (a loop) produces a warning that the repository cannot be resolved.

### UI-033 · Quick repository setup wizard
Priority: **P1**

UI location: user menu (UI-004). Step 1: pick one or more formats; "next". Step 2: enter a key
prefix of at most 20 characters; "create". The wizard then creates a local, a remote and a
virtual repository per selected format. See REPO-008 in [repositories.md](repositories.md) for
what is unspecified (generated keys, upstreams, privileges).

### UI-034 · Property sets screen
Priority: **P2**

UI location: administration area, repository settings, property sets (administrators only).

- List of property sets; "new property set" opens a dialog with the set's name and a property
  table.
- "New record" in that dialog opens a property dialog: property name, value type (any value,
  single select, multi select), an optional default value and, for the select types, a list of
  predefined values built with an "add" button.
- Clicking a set's name opens it for editing; "save" stores it. "Delete" removes a set, but not
  while any repository still uses it.
- Property sets are attached to repositories in the repository form's advanced tab through a
  two-list selector (drag and drop, checkbox plus arrow, or a double arrow that moves everything).

Semantics: ART-026 in [artifacts.md](artifacts.md). Values defined in a set guide the UI (pickers in
the properties tab) but are not enforced.

## Administration: identity and access

### UI-035 · Users screen
Priority: **MVP**

UI location: administration area, user management, users.

| Element | Behavior |
|---|---|
| Table | One row per user; columns include username, email (with the invitation state where invitations exist) and status (enabled, disabled or locked, kept in sync with provisioning); further columns are sortable |
| Search | Partial match on name or email |
| "New user" button | Opens the user editor (UI-036); only administrators can create users |
| Row click | Opens the user editor for that user |
| Bulk unlock | Select locked users and unlock them (IAM-042) |
| Legacy-key banner | Hosted variant only: a banner offers a CSV download of the users who still authenticate with legacy API keys (IAM-044); self-hosted installations find them in the access log instead |
| Invitation state | Invited users appear with an "invited" email status and a "resend invitation" action on hover (hosted variant only; Out of scope, IAM-011) |

Backing operations: user management operations IAM-009 in
[identity-and-access.md](identity-and-access.md).

### UI-036 · User editor
Priority: **MVP**

A dialog for "add new user" and "edit user".

<details>
<summary>Fields</summary>

| Field | Meaning | Default |
|---|---|---|
| Username | Unique login name | — |
| Email address | Contact and notification address | — |
| Password | Internal password; optional when the internal password is disabled (IAM-003) | — |
| Role: system administrator | Full administrative rights (IAM-002) | off (IAM-001) |
| Role: manage resources | Create, edit and delete permissions on every resource type (IAM-007) | off (IAM-001) |
| Role: auditor | Read-only access to every screen; mutually exclusive with all other roles. Offered only when an operator switch enables the role (off by default) (IAM-006) | off (IAM-001) |
| Role: manage webhooks | Create and manage webhooks with the limits of UI-005 (IAM-007) | off (IAM-001) |
| Scan roles | Manage or read scan policies, manage watches, manage reports (IAM-008) | off (IAM-001) |
| Option: can update profile | User may edit their own profile, except the password | on (IAM-003) |
| Option: disable UI access | API-only account | off (IAM-003) |
| Option: disable internal password | No fallback to the internal password when external authentication is on | off (IAM-003) |
| Related groups | Two tabs: "all" (every group with a checkbox; shift-click selects a range) and "selected" (current memberships). Older variant: available and selected groups in one two-list view | none, plus auto-join groups (IAM-015) |
| Reset second-factor enrollment | Button in edit mode; the user must enroll again (IAM-039) | — |

</details>

Field semantics: IAM-001 – IAM-008 and IAM-014 in [identity-and-access.md](identity-and-access.md).

### UI-037 · Groups screen and group editor
Priority: **MVP**

UI location: administration area, user management, groups. A table of groups with a "new group"
button; clicking a group opens the editor.

| Field | Meaning | Default |
|---|---|---|
| Name | Unique; the characters `/`, `\`, `:`, `;`, `\|`, `?`, `<`, `>` and `,` are rejected | — |
| Description | Free text | empty (IAM-013) |
| External ID | Identifier used to match the group in an external directory | empty (IAM-013) |
| Role: administer the system | Members become administrators (IAM-016) | off (IAM-013) |
| Role: manage resources | As in the user editor | off (IAM-013) |
| Automatically join new users | New users of every origin, including transient users, become members (IAM-015) | off (IAM-013) |
| Users | Members of the group | none (IAM-013) |

Edge case: when the administrator role is ticked, "automatically join new users" is disabled, so
new users never become administrators by default.

### UI-038 · Anonymous user page
Priority: **P1**

The predefined anonymous user has its own user page with the option "make the login page the
start page", which sends every visitor who is not logged in to the login page.
Its permissions are granted through permission policies like any other user's; anonymous access
itself is switched on in the general security settings (UI-043). See AUTHN-036 in
[authentication-providers.md](authentication-providers.md) and IAM-005 in
[identity-and-access.md](identity-and-access.md).

### UI-039 · Permissions screen
Priority: **MVP**

UI location: administration area, user management, permissions. An overview of all permission
policies with a "new permission" button; clicking a policy opens it in the editor (UI-040). Users
holding the manage action on a policy see and edit that policy (IAM-029).

Unspecified: the overview's columns and filters.

### UI-040 · Permission policy editor
Priority: **MVP**

A page with a name field and three tabs.

1. **Name**: unique, chosen to be recognizable (for example by team or region).
2. **Resources tab**: one "add" button per resource type (repositories, builds, release sets,
   distribution targets, CI pipeline sources — the last three only with those subsystems).
   - *Repositories*: a searchable list of repositories plus the selectors "any local", "any
     remote" and "any distribution repository" (cover existing and future repositories of that
     class; virtual repositories cannot be chosen). "Set patterns (optional)" opens include and
     exclude pattern lists, either for all selected repositories or per repository; a pattern is
     added with "+" or Enter. Patterns are limited to 1,024 characters (per pattern or in total:
     unspecified).
   - *Builds*: pick by name, or "by pattern" with "include all builds" and exclude patterns; a
     preview table lists the matching builds. The list offers only builds of the default build
     repository (instance-wide scope); build access inside a project is granted through project
     roles (UI-057).
   - *Release sets*: pick release-set repositories, then optional include/exclude patterns, for
     all selected repositories or per repository, with auto-completion from existing release
     sets. *Distribution targets*: by name, or by rules (target-name pattern, country codes,
     city-name pattern) with a preview. *Pipeline sources*: by name or pattern with a preview.
     All three are Later; see IAM-026 – IAM-028.
3. **Users tab** and **groups tab**: a "+" button opens a two-list selection dialog (arrow
   buttons or drag and drop, "OK"). Only after principals are added can their actions be ticked;
   each resource type has its own action checkboxes (read, annotate, deploy/cache,
   delete/overwrite, manage, and subsystem-specific actions). An older variant of the editor
   refuses to create a policy without at least one user or group.
4. "Create" (or "save") stores the policy.

Rules shown by the editor: see UI-005 (no self-editing; a manager grants only actions they hold
and never manage; in the older variant no administrators as principals). For npm repositories the
patterns should include the package metadata paths, or metadata requests through a virtual
repository fail although the tarball is readable (see IAM-021 in
[identity-and-access.md](identity-and-access.md)).

Semantics: IAM-019 – IAM-029 in [identity-and-access.md](identity-and-access.md).

### UI-041 · Access tokens screen
Priority: **MVP**

UI location: administration area, user management, access tokens. Layout, generate dialog
variants (administrator, user, group, project administrator), the result window with one-time
display and copy buttons, revoke, and the pairing-token tab: TOK-026 in
[access-tokens.md](access-tokens.md).

UI specifics:

- The list has a scope column and a project-key column (empty for tokens not created by project
  administrators).
- The expiration control offers preset durations and a custom value in hours; for project
  administrators the maximum is 24 hours.
- The service selector has an "all" checkbox; clearing it reveals a list of individual services.
- The result window closes irreversibly: the token cannot be shown again.
- The pairing-token tab: a "generate pairing token for" picker naming the purpose, "generate", and
  a result window with expiry (300 seconds by default), token ID and token.

### UI-042 · User management settings screen
Priority: **P1**

UI location: administration area, user management, settings.

| Section | Controls | Entry |
|---|---|---|
| Multi-factor authentication | "Enable authenticator app" switch | IAM-039 |
| Account lockout | "Lock user after exceeding max failed login attempts" checkbox, "max failed login attempts" field, "unlock all users" button; temporary login suspension controls | IAM-042 |
| Password expiration | Enable switch, "password expires every (days)", "send mail notification before expiration", "force password expiration for all users", "unexpire expired passwords for all users" | IAM-041 |
| Password encryption policy | supported / unsupported / required | IAM-035 |

Defaults: see the referenced entries; unspecified where they say so.

Placement: multi-factor authentication, account lockout with "unlock all users", and the password
encryption policy belong on this screen. The password expiration controls and the
temporary-login-suspension controls are UI settings without a fixed screen; they are placed here
because these security settings belong to the user management settings, not to the security area.

### UI-043 · General security settings screen
Priority: **MVP**

UI location: administration area, security, general.

<details>
<summary>Sections</summary>

| Section | Controls | Entry |
|---|---|---|
| Anonymous access | "Allow anonymous access"; "allow non-administrator managers to assign the anonymous user" with the list of actions they may grant | IAM-005, IAM-034 |
| Resource visibility | "Hide existence of unauthorized resources": a request without permission gets 404 instead of 403 (the anonymous 401 case: IAM-032) | IAM-032 |
| Basic authentication | "Disable basic authentication for internal users"; "internal users allowed to use basic authentication" (user picker); "hide basic authentication on the login page"; "hide SSO providers" (only with the previous option) | IAM-036, AUTHN-037 |
| Token generation | "Enable token generation through the API" for basic-authenticated users | IAM-037 |
| Legacy API keys | "Disable API key usage" (hosted variant; off by default; self-hosted installations use a server-level switch instead) | IAM-044 |
| Inter-service trust | "Show join key" icon revealing the shared inter-service secret | IAM-043 |
| Token expiration | Default token expiration (seconds; the value prefilled when identity tokens are created; at least 0 and below the maximum; default 31,536,000 = 365 days), maximum token expiration for tokens created by project administrators (seconds; default 0 = unlimited, any positive value forbids non-expiring tokens), "enable email notifications for token expiration", notice period (days), time of the daily expiry check (converted to UTC), notification frequency (once or daily) | IAM-038, TOK-018 – TOK-023 |
| OIDC settings | "Hide global identity mappings from project administrators" — only reachable with "all projects" selected | IAM-040, CIOIDC-016 |

</details>

A "save" button stores the screen. A separate repository-security settings page offers "remove
API keys for all users" (IAM-044).

## Administration: system

### UI-044 · General settings screen
Priority: **MVP**

UI location: administration area, general management, settings. Some fields exist only on
self-hosted installations.

<details>
<summary>Fields</summary>

| Section | Field | Meaning | Default |
|---|---|---|---|
| General | Server name | Name shown in the title of every page | unspecified |
| General | Custom base URL (self-hosted) | Base for links and redirects the system generates and for addresses in non-request contexts such as emails; needed behind a reverse proxy. A request header can override it per request so the system answers under several host names. Also settable through the API | derived from the address of the incoming request |
| General | Date format | Format of dates in the UI; mandatory | unspecified |
| General | Enable help component | Context-sensitive help at the top right of the UI | unspecified |
| Direct cloud-storage download (self-hosted) | Minimum size | Smallest artifact size for which downloads are redirected to the object store (see [storage-and-blobs.md](storage-and-blobs.md)) | unspecified |
| Look and feel | Logo file | Uploaded image shown at the top left of every screen; at most 100 MB | unspecified |
| Look and feel | Logo URL | Alternative: an externally hosted image | unspecified |
| Custom message | Enabled, title, title color, message | Banner text at the top of the screens; links are written as a bracketed pair of URL and link text | unspecified |
| Custom login dialog | Enabled, title, type (info, warning, error, success), message | Notice shown before login that users must acknowledge (UI-006) | unspecified |

</details>

Interactions: the base URL feeds client setup snippets (UI-023) and is a prerequisite for single
sign-on (AUTHN-002 in [authentication-providers.md](authentication-providers.md)).

### UI-045 · General repository settings screen
Priority: **P1**

UI location: administration area, repository settings, general settings (a page separate from
UI-044). Administrators only; one "save" button stores the whole screen.

<details>
<summary>Fields</summary>

| Section | Field | Meaning | Default |
|---|---|---|---|
| General | Max upload size in the UI (MB) | Largest file the deploy dialog (UI-013) accepts; 0 means unlimited | unspecified (100 MB in examples and in ART-006) |
| General | Items per browser page | How many items (folders and files) the tree browser and the directory-listing browser show per batch at one level; a "load more" entry fetches the next batch | unspecified |
| General | Global offline mode | The system behaves as if it had no external network: no remote repository is queried, whatever its own offline flag; only local and cached content is served | unspecified (`false` in configuration examples) |
| Global replication blocking | Block replications, block push replications, block pull replications | When on, push and/or pull replication stops regardless of each repository's replication configuration | disabled |
| General | Archive indexing for search | Index the contents of newly deployed archives so the archive search type (UI-019) finds entries inside them; storage-heavy, so it should stay off when unused | unspecified (off on the hosted variant) |
| Folder download | Enable folder download | Allows "download" on folders in the artifact browser (UI-015); applies to all users | unspecified |
| Folder download | Enable folder download for anonymous access | Extends folder download to anonymous viewers | unspecified |
| Folder download | Max size (MB) | Maximum total size of one folder download | unspecified |
| Folder download | Max number of files | Maximum number of files in one folder download, subfolders included | unspecified |
| Folder download | Max parallel folder downloads | Maximum concurrent folder downloads | unspecified |
| Release-set distribution | Incomplete-release-set cleanup period (hours) | Hours after which a partially distributed release set counts as incomplete and its temporary files may be cleaned up; 0 disables the cleanup (Later) | unspecified |
| Trash can | Enable trash can | Deleted items from local repositories go to the trash can (UI-018) | unspecified |
| Trash can | Retention period (days) | Days an item stays in the trash can before it is deleted permanently | unspecified (14 in configuration examples) |
| Trash can | Empty trash can (button) | Deletes everything in the trash can permanently | — |

</details>

Who may see and restore trash-can items: UI-005. Users with the manage action can also enable the
trash can and set its retention; on which screen they do that is unspecified, since this screen is
administrator-only.

Interactions: global offline mode and remote repositories ([remote-repositories.md](remote-repositories.md));
replication blocking ([replication-and-federation.md](replication-and-federation.md)); trash can
(ART-036 in [artifacts.md](artifacts.md)); configuration as code
([operations-and-monitoring.md](operations-and-monitoring.md)).

## Integrations

### UI-046 · Webhooks screen
Priority: **P1**

UI location: user area, integrations, webhooks. Not limited to administrators (UI-005).

- With "all projects" selected, the list shows every webhook and a project column.
- With a project selected, the list shows only that project's webhooks.
- A "new webhook" button opens the editor (UI-047); clicking a webhook opens it for editing.
- A second tab, "troubleshooting", shows delivery records (UI-048).

Backing operations and event semantics: [events-and-extensibility.md](events-and-extensibility.md).

### UI-047 · Webhook editor
Priority: **P1**

A toggle at the top selects **predefined** (fixed payload format) or **custom** (own method,
headers and payload template).

<details>
<summary>Fields</summary>

| Field | Predefined | Custom | Meaning | Default |
|---|---|---|---|---|
| Name, description | yes | yes | Identification | — |
| URL | yes | yes | Target of the request | — |
| HTTP method | — (always POST) | yes | POST, PUT, GET, PATCH or DELETE | POST |
| Use proxy | yes | yes | One of the configured outbound proxies (self-hosted only) | unspecified |
| Record successful deliveries | yes | yes | Also store successful deliveries for the troubleshooting tab | unspecified |
| Events | yes | yes | Drop-down grouped by resource type; hovering a type reveals its events with checkboxes. In a project context only artifact, container-image, artifact-property and build events are offered | — |
| Resources | yes | yes | Per resource type a dialog: e.g. repositories (any local, any remote, a list, or include/exclude patterns), builds (all new builds, a list, or patterns) | — |
| Secrets | yes | yes | Named encrypted values, injected into headers or payload with a template reference to the secret's name; "add secret" adds more | none (optional) |
| Signing secret | yes | unspecified | Shared secret to sign the payload (HMAC-SHA256) so the receiver can verify it; when signing is not selected, the secret is sent in a request header | none (optional) |
| Headers | yes | yes | Name/value pairs; "add header" adds more | none (optional) |
| Payload | — | yes | Template with dotted references to event data, secrets and the triggering user, plus `json` and `yaml` conversion functions | — |

</details>

Buttons: **test** sends a dummy request to the URL to check connectivity (no real event);
**create** / **save** stores the webhook.

Edge cases: selecting "any local" or "any remote" also covers repositories created later; targets
in private networks are refused unless an operator lifts that restriction.

Semantics: [events-and-extensibility.md](events-and-extensibility.md).

### UI-048 · Webhook troubleshooting tab
Priority: **P2**

- The tab lists the last delivery of each webhook; clicking a webhook lists all its stored
  deliveries; a project column sorts by project; a status filter offers only the statuses that
  occur in the current list.
- Clicking a delivery opens a side pane with tabs: **request** (default; method, URL, headers,
  payload), **response** (headers, payload and status returned by the receiver), **event** (the
  triggering event with raw data), **error** (error messages).
- Stored are failed deliveries, plus successful ones for webhooks with "record successful
  deliveries" on (debug mode).

Edge case: on self-hosted installations the tab works only when an operator has switched
troubleshooting on with a key-value store (Redis) as its storage; with the alternative log-file
storage the records go to a log file on each node and the tab stays empty (unspecified how the UI
signals that).

### UI-049 · CI identity integration screen
Priority: **MVP**

UI location: administration area, integrations, OpenID Connect tab. Lists provider
configurations, offers "new integration → OpenID Connect", "save" and "save and continue" (the
latter opens the identity-mapping dialog), a row menu with "add identity mapping", and for the
GitHub provider type a workflow snippet generator. Specified in CIOIDC-001, CIOIDC-007,
CIOIDC-015 and CIOIDC-024 in [oidc-ci-integration.md](oidc-ci-integration.md).

Gap: there is no snippet for GitLab CI; a GitLab-first product needs one (an
`id_tokens` block plus the exchange call), see CIOIDC-024.

## Projects

### UI-050 · Projects overview
Priority: **P1**

UI location: administration area, first tab; content follows the project selector (UI-003).

| Selection | Tab name | Content and controls |
|---|---|---|
| All projects | All-projects overview | All projects in the installation; controls at the top right: switch between list and dashboard view, search, "create new" (UI-051) |
| One project | Project overview | Details of the selected project, including a members-and-roles area with "add members" (UI-056); controls at the top right: "edit settings" and delete |

Dashboard view (system administrators): one entry per project with its project administrators,
the storage-quota status and quota alerts. See UI-052 for the quota thresholds.

Edge case: only an empty project can be deleted (its repositories, builds and other resources
removed or reassigned first).

### UI-051 · Create and edit project dialog
Priority: **P1**

Only system administrators create projects and edit their basic settings ("edit settings" on the
project overview).

<details>
<summary>Fields and tabs</summary>

| Tab / area | Field | Meaning | Default / limit |
|---|---|---|---|
| Configure project | Project name | Display name | — |
| Configure project | Project key | Unique key; prefix of resources created in the project | mandatory; 2–32 characters, lowercase letters, digits and hyphens, starting with a letter |
| Configure project | Description | Free text | — |
| Configure project | Storage quota | Unlimited, or "notify when reaching" with a limit in GB on the total virtual size of the project's repositories | unlimited |
| Configure project | Project administrator privileges: manage resources | Repositories: create/update and delete, set separately for local, remote and virtual; others: builds, OIDC identity mappings, retention policies (always on together with manage resources) | unspecified |
| Configure project | Project administrator privileges: manage members | Manage the users and groups of the project | unspecified |
| Configure project | Index resources | Let project administrators choose repositories, builds and release sets for scan indexing | unspecified |
| Project admins | Users and groups | Receive the project administrator role; system administrators are tagged "admin" in the list | — |
| Assign repositories | Repositories | Optional assignment of existing repositories (REPO-013) | none (optional) |
| Add destinations | Distribution targets | Optional; Later | none (optional) |

</details>

Buttons: "create"; in edit mode "save".

Unspecified: whether the project key can be changed after creation.

### UI-052 · Project storage quota indicators
Priority: **P2**

The projects list shows each project's storage usage in its quota section; the dashboard view
(UI-050) adds the quota status and alerts per project.

| Usage of the quota | Indicator |
|---|---|
| below 75 % | green bar |
| 75 % to 95 % | orange bar with a warning message |
| 100 % | red bar with a critical warning message |

Inside a project, a storage tab in the left pane lists the artifact size of each of the project's
repositories.

Open: enforcement either stops deploys once the quota is exceeded, as the project-creation dialog
says, or treats the limit as informational ("deploy beyond limit"), as a hosted variant does; the
spec decides (see IAM-061 in [identity-and-access.md](identity-and-access.md)).

Unspecified: the indicator between 95 % and 100 %, and how "virtual size" is computed (see
[storage-and-blobs.md](storage-and-blobs.md)).

### UI-056 · Project members screen
Priority: **P2**

UI location: administration area with one project selected, user management, members; the
project overview (UI-050) reaches the same dialog through an "add members" control at the bottom
of its members-and-roles area. Used by project administrators and system administrators.

- **Add members** opens a dialog in which the viewer picks users or groups and assigns each one
  or more roles (global or project roles, UI-057); "done" adds them.
- A member must always hold at least one role. Removing a member means clearing their role
  assignment; the last role removed takes the member out of the project. Groups are removed the
  same way.

Unspecified: the columns of the members list, filters, and whether removal asks for confirmation.

Semantics: IAM-053 in [identity-and-access.md](identity-and-access.md).

### UI-057 · Global roles and project roles screens
Priority: **P2**

The roles area of user management changes with the project selector (UI-003):

| Selection | Screen | Who edits | Content |
|---|---|---|---|
| "All projects" | Global roles | system administrators | A table of the global roles split into two tabs (software-delivery roles and machine-learning roles), showing which actions each role enables; an edit icon at the end of each row; a "create global role" button for custom roles |
| One project | Roles | project administrators, system administrators | A project-roles tab with "create project role" and a second tab listing the global roles |

Role form (global and project): a unique name; environments (for global roles from the global
environments list; for project roles optional, from global and project environments); actions in
one of two tabs, **basic** (actions bundled into categories) and **advanced** (each resource type
and action on its own). Switching from advanced back to basic discards the advanced selection,
including unsaved changes. "Create" stores the role.

Limits shown by the forms: at most 30 custom global roles and at most 30 roles per project; a
project role must grant at least one action.

Semantics: IAM-054 – IAM-056 in [identity-and-access.md](identity-and-access.md).

## User profile

### UI-053 · Profile page
Priority: **MVP**

UI location: user menu → edit profile (UI-004). Behavior of the self-service features: AUTHN-039
and AUTHN-040 in [authentication-providers.md](authentication-providers.md).

- **Locked state**: the page opens locked. Entering the current password and pressing "unlock"
  makes every section editable; "save" stores changes. Users of external authentication (OAuth or
  SAML single sign-on) can be given access to their API key and SSH keys by an administrator
  without unlocking (AUTHN-006).

<details>
<summary>Sections</summary>

| Section | Controls | Entry |
|---|---|---|
| Identity tokens | "Generate an identity token" opens a dialog: optional description → "next" → the token with a copy button → "close". The list shows each token with its description; an "x" per row and a confirming "revoke" remove it | AUTHN-040 |
| Email | Editable after unlocking, if the account may update its profile | IAM-003 |
| Password | "Change password": new password and confirmation. Unavailable when the account authenticates externally without an internal user | AUTHN-042, AUTHN-003 |
| Encrypted password | The current password in encrypted form for use in insecure environments; masked, with a "view" and a "copy" button | IAM-035 |
| SSH public keys | "Add new SSH key": alias and public key, "add"; several keys per user; each with copy and delete | AUTHN-041, IAM-050 |
| Bound OAuth accounts | Per configured OAuth provider a "click to bind" action linking the provider identity to this account | AUTHN-012 |
| Legacy API key | Shown while legacy keys are still enabled; creating new keys is no longer possible | AUTHN-043 (Later) |

</details>

Edge cases:

- Open: who may change a password. The profile page offers "change password", while the "can
  update profile" option excludes the password; the spec decides (same open question as
  IAM-003).
- Users from a hosted single-sign-on setup with an email attribute may be asked on first login to
  verify their email ("verify email" notice); unspecified for self-hosted installations.

## Federation

### UI-054 · Federation status dashboard
Priority: **Later**

UI location: administration area, monitoring, federation status (system administrators only).
Two levels:

- **Federations list**: summary tiles per status (healthy, delayed, error, pending full
  synchronization, full synchronization running) that filter the table on click (click again to
  clear); a filter panel with all statuses; a free-text search over keys and status text. Table
  columns: priority flag (default sort, prioritized first, then by status severity), repository
  key (opens level 2), number of members (opens a pop-up listing each member's key, URL and
  status), overall status (the most severe member status plus how many members have it).
- **Repository detail**: a member selector (with "all members"), in-progress and failed binary
  transfer counts for the selected member (N/A when unreachable or unsupported), and a connections
  table with source, target, status, latency (relative time since the last successfully handled
  event, N/A when unknown) and queued events.

Status severity, highest first: error, delayed, pending full synchronization, full
synchronization running, disabled, unsupported, healthy, not applicable. A connection can also
show "receiving only" (target in receive-only mode); like disabled, unsupported and not applicable
it has no summary tile of its own.

Priority flag: toggled by hovering the priority cell and clicking the flag; at most 10 repositories
or 20 % of all repositories, whichever is lower.

Also reachable from the repositories list row action "federation status" (UI-030). Semantics: see
[replication-and-federation.md](replication-and-federation.md).

### UI-061 · Federation monitoring screens of the older embedded synchronization mode
Priority: **Later**

When federation runs in the older embedded mode instead of the separate synchronization service,
UI-054 is replaced by two screens with a different cut: per repository and per remote instance
rather than per repository connection. System administrators only.

- **Repository federation status window**, opened from the row action "federation status" of a
  federated repository in the repositories list (UI-030). Summary counts: artifacts fully
  replicated (metadata and binary), artifacts with metadata only, binary transfer tasks in
  progress and failed (in-progress far below metadata-only signals a high failure rate). A table
  of target members: repository key, target URL, number of queued events, mirror latency (now
  minus the time of the oldest unhandled event), next registration time, and a synchronized or
  out-of-sync flag; out of sync is decided by a fixed, system-level error-event threshold that
  cannot be changed. Selecting a row breaks the queued events down into create, update, delete,
  property and error counts.
- **Federation sync status table**, under administration, monitoring: one row per remote
  instance with its URL, binaries still to fetch from it, fetch failures, a status ("federated"
  when no local federated repository exceeds the configured delay threshold towards that instance,
  "delayed" otherwise), and the longest delay ("synced" without lag, "N/A" when unknown). A
  refresh button reloads the table; clicking a row opens the ten mirror pairs with the longest
  delay (source repository, target repository, delay).

The data comes from the status operations of API-079 in
[api-content-operations.md](api-content-operations.md); the embedded mode itself is described in
[replication-and-federation.md](replication-and-federation.md).

## Hosted-only elements

### UI-055 · Hosted account portal dashboard
Priority: **Out of scope**

A separate, operator-run account portal of a hosted service shows subscription status and plan,
licensed entitlements, usage of connected installations, and a documentation search. It has no
meaning for a self-hosted repository manager.

### UI-059 · Site launcher across hosted sites and subscriptions
Priority: **Out of scope**

On hosted offerings an administrator opens a launcher from the header that lists the
organization's subscriptions and, per subscription, the active sites registered in the account
portal's topology; a check mark marks the current site. Choosing another site opens it in the same
browser tab, possibly after a new login. Depends on the operator-run account portal and its
topology of hosted servers (UI-055; [operations-and-monitoring.md](operations-and-monitoring.md)
OPS-085); a self-hosted instance has no such registry.

### UI-060 · In-product AI assistant panel
Priority: **Out of scope**

On hosted instances a header button opens a chat panel backed by an operator-run language model.
It answers how-to questions from the system's documentation, navigates to the matching screens, and
in a second mode drafts policy rules in the policy language of the release-governance subsystem
([distribution-and-lifecycle.md](distribution-and-lifecycle.md)). The account portal's primary
administrator must accept additional terms to switch it on, for all instances or per instance;
each user gets a weekly usage quota. Planned extensions (answering questions about the user's own
data, acting on the user's behalf after approval) are unspecified. Out of scope as an operator-run
hosted service; the AI tool server for external assistants is a separate feature (EVT-077
in [events-and-extensibility.md](events-and-extensibility.md)).

## Notes for implementers

- **"Viewed" is not "edited".** Opening a repository's edit form puts it on the recently-viewed
  list even without saving; opening a package's details page puts it on the package
  recently-viewed list. Record a view event, not a save event.
- **Some UI state never reaches the server.** Package custom views and tree filters live in the
  browser (storage or cookies); users lose them with their browser data. Moving them server-side
  is a deviation the catalog does not require.
- **A hidden default is still a default.** Removing the "include everything" row from a
  repository's include patterns hides it in the form but keeps it in effect; the form must not
  save an empty list as "include nothing".
- **Secrets are shown once.** Token result windows, profile tokens and generated client setup
  snippets carry live credentials; the result cannot be reopened, and rendered snippets must not
  be cached or logged.
- **Deploy snippets follow the repository class.** A virtual repository offers deploy snippets and
  accepts UI deploys only with a default deployment repository. Rendering setup snippets and
  install commands from one template set keeps the dialog and the package page consistent.
- **Copy uses Unix semantics.** Copying a folder onto an existing folder of the same name nests
  it; a custom target path suppresses layout translation. Showing the resulting path before
  execution (dry run) avoids most surprises.
- **Self-lockout guards are part of the UI contract.** Users cannot edit their own permissions,
  administrators cannot be added to permission policies, ticking the administrator role on a
  group disables auto-join, and the auditor role excludes every other role.
- **Empty is not failed.** A build run with no captured environment shows zero counts; a missing
  detail tab can mean "not collected", "no permission" or "subsystem off". The UI does not tell
  these apart, so support material should.
- **The UI upload path is size-limited.** Deploys through the UI are capped per file by an
  administrator setting (UI-045; 100 MB in examples, 0 for unlimited); larger files must go
  through native clients or multi-part upload, and the dialog should say so before the upload
  starts.
- **Project scope changes what a screen means.** The same screen (webhooks, repositories) lists
  everything with "all projects" and only one project's resources otherwise, and some actions or
  settings exist only in one of the two scopes.
