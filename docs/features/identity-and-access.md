# Identity and access

This page catalogs who can do what in a full-featured binary repository manager: user accounts
and their flags, groups, the permission-policy model (resources, include/exclude patterns,
actions and how they apply to local, remote and virtual repositories), anonymous access,
system-wide roles, projects with their members, roles and environments, the security settings
that govern login behavior, and the management of keys and stored secrets. How a person or a CI
job proves its identity is covered in [authentication-providers.md](authentication-providers.md),
[access-tokens.md](access-tokens.md) and [oidc-ci-integration.md](oidc-ci-integration.md); this
page covers what that identity is then allowed to do.

## Users

### IAM-001 · User accounts
Priority: **MVP**

A user is a named principal that can authenticate and to which permissions are granted, either
directly or through groups. Users are created by an administrator (UI or API), automatically on
first login through an external identity provider (see
[authentication-providers.md](authentication-providers.md)), through provisioning (SCIM), or
transiently during an OIDC token exchange (see IAM-015). Passwords of internal users are stored
only as hashes or encrypted hashes.

Every user record carries an authentication realm. Users created through the management API are
always placed in the internal realm; users created by an external provider carry that provider's
realm (for example a directory realm).

<details>
<summary>User attributes</summary>

| Attribute | Meaning | Default |
|---|---|---|
| Username | Unique login name; the key of the record | required |
| Email address | Contact address, used for notifications | required on create |
| Password | Internal password; optional when the internal password is disabled (IAM-003) | none |
| Groups | Groups the user belongs to | none, plus every auto-join group (IAM-015) |
| System administrator | See IAM-002 | off |
| Effective administrator | Read-only; true when the user is an administrator directly or through an administrator group | derived |
| Profile updatable | See IAM-003 | on |
| UI access disabled | See IAM-003 | off |
| Internal password disabled | See IAM-003 | off |
| Delegated system roles | Resource manager, webhook manager, auditor, scan-related roles (IAM-006 to IAM-008) | off |
| Realm | Authentication realm the user belongs to | internal for API-created users |
| Status | Invited, enabled, disabled or locked (IAM-004) | enabled |
| Last login | Timestamp of the most recent login | read-only |
| MFA enrollment status | None, enabled or disabled (older interface only, see IAM-039) | read-only |

</details>

UI location: administration area, user management, users list (sortable columns, partial search
by name or email, a status column). Creating or editing opens a user dialog with username, email,
password, a roles block, an options block and a related-groups block.

Edge cases: creating a user whose name exists fails with a conflict. Only administrators can
create users.

Interactions: groups (IAM-013), permission policies (IAM-019), tokens issued to the user (see
[access-tokens.md](access-tokens.md)).

### IAM-002 · System administrator
Priority: **MVP**

A system administrator is the equivalent of a root account: it is not subject to any permission
check, can manage every resource and every setting, and holds every action on every resource.
Administrator status comes either from the user's own administrator flag or from membership in
an administrator group (IAM-016); the API reports both the flag and the derived "effective
administrator" value.

Behavior:

- Because administrators implicitly hold everything, the UI does not offer administrators (users
  or groups with administrator privileges) as principals when editing a permission policy.
- The user-list operation can filter to administrators only.
- Recommended practice: keep the number of administrators minimal.

Self-hosted installations ship with one default administrator account with a well-known initial
password; operators should change it right after the first login (whether the system forces the
change is unspecified). Hosted (SaaS) instances have no such account. Recovery
when no administrator can log in is IAM-010.

Interactions: IAM-016 (administrator groups), IAM-036 (basic-authentication lock-out risk),
IAM-039 (MFA recovery).

### IAM-003 · Per-user options
Priority: **MVP**

Three per-user switches shape how a user may use the system.

| Option | Meaning | Default |
|---|---|---|
| Profile updatable | The user may edit their own profile details, except the password. Open: either only an administrator changes a password, or the user may change it too, as the change-password operation (IAM-009) accepts; the spec decides. Leaving the option off suits shared departmental accounts. | on |
| UI access disabled | The user is limited to the API; the web UI refuses the account. Suited to automation accounts. | off |
| Internal password disabled | Turns off the fallback to the internal password when an external authentication method (for example a directory) is active. When set at creation, no password is required. | off |

Edge case: when the internal-password switch is turned back off on a user that had it on, the
update must include a new password.

### IAM-004 · User status and disabling
Priority: **MVP**

Each user has a status: **enabled**, **disabled**, **locked** (after too many failed logins, see
IAM-042) or **invited** (hosted trial instances only, see IAM-011). The status is shown in the
users list and returned by the API; the user list can be filtered by status. The effect of
"disabled" is specified mainly for tokens: a disabled user can no longer exchange a refresh token
for a new access token; other effects are unspecified. When users are provisioned through SCIM,
the status column reflects the provisioned state (see
[authentication-providers.md](authentication-providers.md)). The older user interface reports
only enabled, disabled and locked.

Interactions: tokens of a disabled user and refresh behavior are described in
[access-tokens.md](access-tokens.md); SCIM is recommended so that a user disabled at the identity
provider cannot keep refreshing tokens.

### IAM-005 · Anonymous user
Priority: **MVP**

The system ships with a predefined anonymous user that represents every request without
credentials. It only takes effect when anonymous access is switched on globally (security
settings, general; default unspecified). Once on, the anonymous user can be
granted actions in permission policies like any other user (for example reading cached
artifacts and populating remote caches).

Behavior and settings:

| Setting | Meaning | Default |
|---|---|---|
| Allow anonymous access | Global switch; the anonymous user must be active before its permissions take effect | unspecified |
| Login page as start page | On the anonymous user's settings: routes anonymous UI visitors to the login page | unspecified |

Edge cases and hardening:

- Newer releases remove the anonymous user from the default broad policies that cover all
  repositories and all remote repositories. The recommended practice is a dedicated policy that
  grants the anonymous user only the read action on selected repositories.
- Granting anonymous access to a wildcard (IAM-020) also grants it on future repositories of that
  kind, which can expose sensitive content.
- Adding the anonymous user to projects is blocked by default (IAM-062).
- Which non-administrators may grant actions to the anonymous user is controlled by IAM-034.
- Without credentials, a request for a protected resource is answered with 401. Whether IAM-032
  also replaces that 401 with 404 is unspecified (IAM-032 specifies only the 403 case).

Interactions: IAM-020, IAM-023 (anonymous cache population needs the deploy/cache action),
IAM-032, IAM-034.

### IAM-006 · Auditor role
Priority: **P1**

A user-level role that can view the entire web UI but cannot create, modify or delete anything,
intended for audit and compliance. Assigning the role to an existing user keeps that user's API
permissions as they were.

Behavior:

- In the UI, the auditor role is mutually exclusive with every other role: selecting it disables
  the others and vice versa.
- The role sits behind a feature switch that is off by default.

UI location: user dialog, roles block. API: a boolean on the user record (IAM-009).

### IAM-007 · Delegated system roles: resource manager and webhook manager
Priority: **P1**

Two system-wide roles let non-administrators handle parts of the administration. Both can be set
on a user or on a group.

| Role | What it allows | Default |
|---|---|---|
| Resource manager | Lets the principal administer resources of every type (create, change, remove), CI pipeline resources included. For groups, the API limits the description to resources in the default (non-project) scope. | off |
| Webhook manager | Create and manage webhooks, with limits: only events of the artifact, artifact-property and container-image domains; to edit a webhook the user needs read on the repository it covers and the webhook must not target the "any local" or "any remote" wildcards; include/exclude patterns on webhooks stay administrator-only. | off |

Interactions: webhooks in [events-and-extensibility.md](events-and-extensibility.md); permission
policies (IAM-019).

### IAM-008 · Scan-related roles
Priority: **Later**

Role flags set on users or groups that only matter when a vulnerability-scanning engine is
present: manage security and compliance policies, view policies only, manage scan scopes (the
sets of resources a scanner watches), and create and manage scan reports. They are global roles
on the principal, not actions inside a permission policy.

| Role | Meaning | Default |
|---|---|---|
| Policy manager | Create, edit and delete scan policies | off |
| Policy viewer | View scan policies only | off |
| Scan-scope manager | Add, edit and delete scan scopes on repositories | off |
| Report manager | Create, generate and manage scan reports | off |

Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md); the
repository action "manage scan metadata" (IAM-022) is separate from these roles.

### IAM-009 · User management operations
Priority: **MVP**

The management API offers the full user lifecycle. Unless stated otherwise every operation
requires an administrator.

<details>
<summary>Operations</summary>

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create user | Username and email (required); password (required unless the internal password is disabled); groups; administrator flag; profile-updatable flag; UI-access-disabled flag; internal-password-disabled flag; delegated role flags (IAM-006 to IAM-008) | 201 with the user record; 409 if the user exists | administrator |
| Get user | Username | User record (attributes of IAM-001, including effective administrator, realm, status, last login, groups, role flags); 404 if unknown | administrator, or a token scoped to read identities |
| List users | Optional filters: status; username (substring match); administrators only; role (when a role filter is given, the administrator and username filters are ignored); resource type (required when filtering by a repository role); repository name (not combinable with administrators-only); project key; descending order; page size (minimum 1, default 1000); cursor | List of entries (username, URI, realm, status) plus a cursor for the next page | administrator, or a token scoped to read identities |
| Update user (partial) | Username plus any of: email, password, administrator flag, profile-updatable flag, internal-password-disabled flag, UI-access-disabled flag, delegated role flags; omitted attributes stay unchanged; a password is mandatory when the internal password is re-enabled | Updated record; 404 if unknown | administrator |
| Delete user | Username | 204; 404 if unknown | administrator |
| Add or remove groups of a user | Username, groups to add, groups to remove | The user's resulting group list | administrator |
| Change password | Username, new password | 204 | administrator, or the user themself |
| Expire a user's password | Username | 204 | administrator |
| Expire all passwords / unexpire all passwords | none | 200 | administrator |
| Unlock a user | Username | 204; 404 if unknown | administrator |

</details>

The password- and lock-related operations are described with their policies in IAM-041,
IAM-042 and IAM-069.

### IAM-010 · Administrator recovery by bootstrap credentials
Priority: **MVP**

When no administrator can log in, an operator with access to the server's filesystem can recreate
one or more administrators: a credentials file is placed in the identity service's configuration
directory, one line per administrator, each line holding a username, a host qualifier (a
wildcard) and a new password. The file must have mode exactly 0600 (neither more nor less
permissive works) and be owned by the service account. The identity service picks it up on
restart. In older clustered releases this had to happen on the primary node followed by a
rolling restart.

The same procedure is the way out when an administrator loses their second factor (IAM-039).
Whether it also remedies the basic-authentication lock-out of IAM-036 is unspecified; that
lock-out has its own recovery path.

### IAM-011 · User invitation by email
Priority: **Out of scope**

On hosted trial instances an administrator can invite a user by email instead of creating the
account: the invite carries roles and options, the user then sets a password or signs in through
a personal social login provider. Invited users show the status "invited"; an invite can be
resent, which invalidates the earlier link; only one open invitation per email address. Tied to
hosted trial instances; no meaning for a self-hosted installation.

### IAM-012 · Persistent login ("remember me")
Priority: **P2**

The login screen offers a "remember me" checkbox. Ticking it stores a browser cookie that signs
the user in automatically for 7 days; once the cookie has expired, credentials are needed again.
An administrator can switch the feature off for the whole instance so that credentials are asked
for on every login (a server setting that takes effect after a restart).

Interactions: UI sessions in [authentication-providers.md](authentication-providers.md).

## Groups

### IAM-013 · Groups
Priority: **MVP**

A group bundles users so that permissions and roles are granted once to the group instead of to
every member; a group is the carrier of a job function. A user's effective
permissions are the union of their own grants and the grants of all their groups (permissions are
additive, IAM-019).

<details>
<summary>Group attributes</summary>

| Attribute | Meaning | Default |
|---|---|---|
| Name | Unique name. Not allowed: `/`, `\`, `:`, `;`, `\|`, `?`, `<`, `>`, `,` | required |
| Description | Free text | empty |
| External ID | Identifier that links the group to its counterpart in an external identity provider; the API ties it to SCIM provisioning | empty |
| Auto-join | See IAM-015 | off |
| Administrator privileges | See IAM-016 | off |
| Resource manager, webhook manager | See IAM-007 | off |
| Scan-related roles | See IAM-008 | off |
| Members | Usernames | empty |
| Realm, realm attributes | Internal, or the external realm the group comes from (IAM-017) | internal |

</details>

UI location: administration area, user management, groups list; "new group" dialog with name,
description, external ID, roles (administer the system, manage resources), the auto-join checkbox
and a users block.

### IAM-014 · Group membership management
Priority: **MVP**

Membership can be managed from either side:

- On the user dialog, a related-groups block assigns or removes groups. For large installations
  the block has an "all" tab (with multi-select by shift-click over a range) and a "selected" tab.
- On the group dialog, a users block assigns or removes members.

In both dialogs permissions for the user or group can be adjusted on the same screen.

API: add or remove members of a group (lists of usernames to add and to remove; administrator);
add or remove groups of a user (IAM-009).

Edge case: creating a group with initial members fails with 404 when any listed user does not
exist.

### IAM-015 · Auto-join groups
Priority: **P1**

A group flagged auto-join receives every newly created user automatically. This covers accounts
made by hand (UI, API), by invitation or by SCIM provisioning; accounts the system creates on
first login through a directory, SAML or OAuth/OIDC provider; and short-lived users that only
exist while one authentication is processed, for example when an OIDC token exchange maps to a
username that has no account.

Edge cases:

- Because such short-lived users pick up the auto-join groups at the moment they are created, a
  token issued through OIDC can end up with more rights than the identity mapping grants: the
  result is the union of the mapped groups and every auto-join group. Administrators
  should review auto-join groups for this reason (privilege escalation). An OIDC identity mapping
  without a username uses a synthetic subject and therefore avoids the auto-join expansion.
- An administrator group cannot be auto-join (IAM-016).

Interactions: OIDC identity mappings in [oidc-ci-integration.md](oidc-ci-integration.md).

### IAM-016 · Administrator groups
Priority: **MVP**

A group with administrator privileges turns every member into a system administrator (IAM-002).
To prevent new users from becoming administrators automatically, the auto-join option is
disabled while administrator privileges are set.

This is how an external identity provider's group can carry administrator rights: the
lock-out guidance (IAM-036) is to create a group for externally authenticated users with
administrator privileges before disabling internal logins.

### IAM-017 · Groups from external realms
Priority: **P2**

Groups can originate outside the internal realm. A group can be created by importing it from a
configured directory group setting: the request names the setting and the group's name in the
directory; the directory's own comparison rule decides the match (case-insensitive for the common
name attributes); the name must identify exactly one directory group; the group is created in the
directory realm with its name in lowercase (so the created name can differ from the requested
one, and the response returns the created name). Members cannot be supplied together with an
import, because directory login keeps membership in sync. Importing all groups of a directory
setting at once is a separate operation of the directory integration.

Groups may also carry an external ID used by SCIM provisioning to correlate them with the
identity provider's groups.

Interactions: directory, SAML and SCIM integration in
[authentication-providers.md](authentication-providers.md).

### IAM-018 · Group management operations
Priority: **MVP**

<details>
<summary>Operations</summary>

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create group | Name (required); description; auto-join flag; administrator flag; external ID; directory-import setting name; initial members; delegated role flags | 201 with the group record; 404 if a member, the directory setting or the directory group is missing; 409 if the group exists | administrator |
| Get group | Name | Group record (attributes of IAM-013 including realm and realm attributes); 404 if unknown | administrator, or a token scoped to read identities |
| List groups | Optional: name pattern; role, resource type and repository name (only together); project key; descending lexical order (default off); page size (default 1000); cursor | Entries (group name, URI) plus a cursor | administrator, or a token scoped to read identities |
| Update group (partial) | Name plus any of: description, auto-join flag, administrator flag, external ID, delegated role flags; omitted fields stay unchanged and an empty request succeeds; realm and realm attributes cannot be changed; membership is not changed here | Updated record; 404 if unknown | administrator |
| Add or remove members | Name, usernames to add, usernames to remove | The group's resulting member list; 404 if the group is unknown | administrator |
| Delete group | Name | 204; 404 if unknown | administrator |

</details>

## Permission policies

### IAM-019 · Permission policy model
Priority: **MVP**

A permission policy is a named object that binds resources to principals and actions. It
consists of:

- a unique name (up to 64 characters in the older interface) and an optional description;
- per resource type (repositories, builds, release sets, distribution targets, CI pipeline
  sources), a selection of resources narrowed by include/exclude patterns (IAM-020, IAM-021,
  IAM-025 to IAM-028);
- per resource type, a map of users to granted actions and a map of groups to granted actions.

Evaluation rules:

- Deny by default. Permissions are purely additive: an action is granted only where some policy
  explicitly grants it to the user or to one of the user's groups. There are no deny rules;
  exclusion happens only through exclude patterns that narrow a policy's resources.
- Each resource type has its own action set (IAM-022, IAM-025 to IAM-028). Actions offered in the
  UI depend on which subsystems are installed.
- Administrators bypass evaluation (IAM-002).
- A token presented by a client can narrow the user's permissions further (scopes; see
  [access-tokens.md](access-tokens.md)).
- Denied requests answer 401 to anonymous callers and 403 to authenticated callers; with IAM-032
  on, the 403 becomes 404 (the effect on the anonymous 401 is unspecified).

Permissions can be managed centrally (this page) or per project through roles (IAM-054 to
IAM-056).
The policy list (IAM-031) leaves out the grants that project roles produce.

UI location: administration area, user management, permissions; "new permission" wizard with a
name, a resources tab, a users tab and a groups tab; principals must be added before actions can
be ticked. A permissions overview lists all policies, and a policy can be exported in its
machine-readable form (the same structure the companion CLI's template uses, IAM-033).

### IAM-020 · Repository resources and repository wildcards
Priority: **MVP**

The repository part of a policy selects repositories explicitly by key and/or through wildcards
that cover every existing and future repository of a class:

| Selector | Covers |
|---|---|
| Explicit keys | The listed local, remote (and, where available, distribution) repositories |
| Any local repository | All current and future local repositories |
| Any remote repository | All current and future remote repositories |
| Any distribution repository | All current and future distribution repositories (release-set storage) |
| Any repository | All repositories (offered by the older interface and the companion CLI template, IAM-033; not offered as a UI choice) |

Virtual repositories cannot be selected: a virtual repository has no permissions of its own
(IAM-024). The older interface requires the repository list to be present.

Edge cases:

- Wildcards are dynamic: a repository created later is covered immediately, which matters for
  anonymous grants (IAM-005).
- The build-information repository is never selected here; it is governed by build permissions
  (IAM-025).

### IAM-021 · Include and exclude patterns
Priority: **MVP**

Within the selected repositories, include and exclude patterns restrict a policy to paths.

- Syntax: Ant-style path patterns: `*` matches within one path segment, `**` matches any number of
  segments, `?` matches one character. Example: include `org/apache/**` limits a policy on a
  Maven repository to that subtree.
- Several patterns can be given. The combination rule is not spelled out; the include/exclude
  naming implies that a path is covered when it matches at least one include pattern and no
  exclude pattern.
- Patterns can apply to all selected repositories at once, or be set per repository.
- Each include and each exclude pattern field is limited to 1024 characters.
- Defaults: include `**` (everything) for repositories; exclude empty (nothing).

Edge cases:

- For npm, metadata lives under paths separate from the tarballs; a policy must include those
  metadata paths as well, otherwise metadata requests through a virtual
  repository can fail even though the user may read the tarball. A path-pattern model that serves
  npm must therefore either document this or map package names to all their paths.
- The same pattern syntax is used for builds, release sets, distribution targets and pipeline
  sources (IAM-025 to IAM-028), always limited to 1024 characters.

Interactions: the per-format path layout in [formats/README.md](formats/README.md).

### IAM-022 · Repository actions
Priority: **MVP**

Actions a policy can grant on repository content:

| Action | Semantics |
|---|---|
| Read | Download artifacts and read their metadata. On a remote repository only items already in its cache (IAM-023). |
| Annotate | Add, change and remove properties (metadata) on artifacts and folders. |
| Deploy/cache | Upload artifacts to local repositories; on remote repositories, fetch from the upstream into the cache (IAM-023). |
| Delete/overwrite | Delete artifacts or replace existing ones. Leaving it out protects a deployed release or a unique (timestamped) snapshot from being replaced; Maven snapshots of the non-unique kind stay replaceable for anyone holding deploy. |
| Manage scan metadata | Trigger rescans of already scanned artifacts and create or delete custom issues and licenses (only with a scanning engine). |
| Manage | Change the grants of other users and groups within this policy (IAM-029), plus administrative chores in the artifact browser, for example editing who follows an item or restoring items from the trash can. Adding or removing resources of the policy stays out of reach. |

Edge cases:

- Actions are independent check boxes; whether any action implies another for repositories (for
  example that deploy implies read) is unspecified.
  (When older distribution permissions were migrated, each non-read action was mapped together
  with read, which hints at the intended pairing but is not a rule.)
- Overwrite is gated by the delete/overwrite action, not by deploy alone.

Interactions: trash can and properties in [artifacts.md](artifacts.md); deployment through
virtual repositories (IAM-024).

### IAM-023 · Permissions on remote repositories
Priority: **MVP**

For remote (pull-through) repositories the two lowest actions split the work:

- **Read** allows downloading only what is already in the remote repository's cache. A request
  for an item not yet cached is not fetched from the upstream for a user who holds only read.
- **Deploy/cache** allows the request to fetch the item from the upstream and store it in the
  cache.

Consequence: a user (or the anonymous user) who should transparently pull new upstream packages
needs both read and deploy/cache on the remote repository. Policies that grant only read on
remotes make the remote a read-only mirror of whatever others already pulled.

Interactions: remote-repository caching in [remote-repositories.md](remote-repositories.md);
IAM-024 for access through virtual repositories.

### IAM-024 · Permissions through virtual repositories
Priority: **MVP**

A virtual repository has no permission policy of its own and cannot be selected in one. Access
through it is governed by the permissions the caller already holds on its member local and remote
repositories. The per-member mechanics (whether an unreadable member is silently skipped during
resolution, and whether an upstream fetch through a remote member requires deploy/cache there as
in IAM-023) are unspecified, although both follow naturally from the rule.

Edge cases:

- Open: either a policy's repository list may hold a virtual repository key, as the per-group
  lookup of the older interface returns, or virtual repositories are not supported there, as that
  same interface states; the spec decides.
- npm metadata through a virtual repository needs read on the metadata paths of the members
  (IAM-021).
- Hiding unauthorized resources can be set per virtual repository (IAM-032).
- Which permission a deployment through a virtual repository needs on its deployment target is
  unspecified; see [repositories.md](repositories.md) for the deployment
  target itself.

### IAM-025 · Build resources and actions
Priority: **P1**

Build permissions control access to recorded build information and to the matching paths in the
build-information repository (always the built-in one; it cannot be changed).

Selection: by build name from the list of existing builds, by include/exclude patterns, or
"include all builds" (existing and future), optionally narrowed by exclude patterns. Patterns
apply to current and future builds. Example: include `apache**/**` covers every build whose name
starts with `apache`, whether or not it exists yet. A preview lists the builds that match. The
picker lists only builds of the default build-information repository (the "all projects" scope); builds
belonging to a project are permitted through project roles (IAM-055, IAM-060). Defaults: include
and exclude empty.

| Action | Semantics |
|---|---|
| Read | View and download build-information files and see the build on the builds page |
| Annotate | Add properties to build-information files and folders |
| Deploy | Upload and promote build information |
| Delete | Delete build-information files |
| Manage scan metadata | Trigger scans of builds; manage custom issues and licenses (scanning engine only) |
| Manage | Change other principals' build grants in this policy (IAM-029) |

Example: read plus deploy on `apache**/**` lets a CI identity publish build information whose
build name starts with `apache` and see those builds.

Interactions: [builds-and-ci.md](builds-and-ci.md).

### IAM-026 · Release-set resources and actions
Priority: **Later**

Release sets (immutable, signed collections of artifacts that make up a release) are permitted
per release-set repository, narrowed by include/exclude patterns on release-set names, either for
all selected repositories or per release-set repository. Name autocompletion is offered while
typing a pattern. Selection modes: any release set (existing and future), by name (all versions
of the named set), by pattern. When the repository list is empty, the default release-set
repository is used; on edge instances only that default repository is allowed.

| Action | Semantics |
|---|---|
| Read | View and download release-set content and see the sets on the distribution page; needed on every instance where the set should be visible |
| Annotate | Add properties to release-set files and folders |
| Create | Create release sets (with read and deploy on a pattern such as `apache**/**`, a user may create sets whose names match) |
| Delete | Delete release sets |
| Distribute | Distribute release sets |
| Manage scan metadata | Trigger scans of release sets (scanning engine only) |
| Manage | Change other principals' grants in this policy (IAM-029) |

Interactions: [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### IAM-027 · Distribution-target resources and actions
Priority: **Later**

Distribution targets (edge instances that receive release sets) are selectable once at least one
release set exists. Selection: by target name; "include all destinations" (existing and future);
or by rules combining an instance-name pattern (`*` for all), a list of country codes (`*` for
all) and a city-name pattern (`**` for all). Several rules can be added; a preview shows the
matching targets.

| Action | Semantics |
|---|---|
| Distribute | Distribute release sets to the selected targets |
| Delete | Delete release sets from the selected targets |
| Manage | Add and remove principals who may distribute to these targets (IAM-029) |

### IAM-028 · CI pipeline-source resources and actions
Priority: **Out of scope**

Where an integrated CI pipeline engine exists, its pipeline sources (Git repositories holding
pipeline definitions) are a resource type: selected by name, by include/exclude pattern, or
"include all". Actions: read (view the sources), trigger (manually run steps), manage (create and
edit sources). It depends on a bundled CI engine, which is not a repository-manager function.

### IAM-029 · Delegated policy management (manage action)
Priority: **P1**

A principal holding the manage action on a resource type of a policy becomes a delegated manager
of that policy: it may add, change and remove the grants of other users and groups for that
resource type, but may not add or remove resources.

Rules:

- Default mode: a delegated manager can grant only actions it holds itself, and never the manage
  action. Example: a manager holding read, deploy and manage can grant read and deploy.
- The UI does not let the logged-in user edit their own grants, to prevent accidental
  self-lock-out (not a security boundary).
- An instance-level setting changes the mode: (a) grant only own actions, without manage
  (default); (b) grant any action except manage; (c) grant any action including manage. The
  older behavior, where managers could also grant manage, was treated as a security issue.
- Through the older interface, a delegated manager can list and read policies it manages and can
  update only the actions of a policy, not its resources.
- Whether a delegated manager may grant actions to the anonymous user is controlled by IAM-034.

### IAM-030 · Effective permissions view
Priority: **P1**

For an artifact path, a build or a release set, the UI shows an effective-permissions tab listing
every user and group that holds actions on it and the policies that grant them. The API offers
the complementary lookups by principal: the filtered policy list (IAM-031) and the older
per-user and per-group lookups (IAM-033).

Interactions: artifact browser in [ui-inventory.md](ui-inventory.md).

### IAM-031 · Permission policy operations
Priority: **MVP**

The current management interface models a policy as a name, a description and one section per
resource type (repository content, build, release set). Each section has an action map for
users, an action map for groups, and a map from target name (a repository key, or for builds and
release sets the corresponding target) to its own include and exclude pattern lists. How the
"any local" and "any remote" selectors are spelled in this map is unspecified; the older interface
uses reserved names (IAM-033).

<details>
<summary>Operations</summary>

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create policy | Name (required); description; resource sections | 201 with the policy; 409 if the name exists | administrator |
| List policies | Optional: cursor (a policy name to start from); page size (between 1 and 99,999; open: whether the bounds are inclusive or exclusive, the spec decides; default unspecified); user names and/or group names (comma-separated) to keep only policies granting anything to at least one of them; include inherited groups (default on: a user filter also matches policies granted to the user's groups); expand (default off; only with a user or group filter) | Names and URIs plus a cursor (the name of the last policy of the page; empty when there are no more). With expand: the full policies, but with actions narrowed to the requested principals. 400 for expand without a filter; 404 for an unknown user or group. | administrator, or a token scoped to read permissions |
| Get policy | Name | Full policy; 404 if unknown | administrator, or a token scoped to read permissions |
| Delete policy | Name | 204; 404 if unknown | administrator |
| Get one resource section | Name, resource type (repository content, build, release set) | The section | administrator |
| Replace one resource section | Name, resource type, full section (action maps and targets) | 200 with the section; 404 if the policy is unknown | administrator |
| Update one resource section | Name, resource type, partial section | 200 with the resulting section; 404 if the policy is unknown; merge rules for partial maps unspecified | administrator |
| Delete one resource section | Name, resource type | 204; 404 if the policy or section is missing | administrator |

</details>

Filter rules: user filters ignore case, group filters compare exactly. Grants that come from
project roles never appear in the list; stored policies owned by a project do.

Interactions: declarative configuration in
[operations-and-monitoring.md](operations-and-monitoring.md); operation catalog in
[api-admin-operations.md](api-admin-operations.md).

### IAM-032 · Hide existence of unauthorized resources
Priority: **P1**

By default the system reveals that a protected resource exists: an anonymous request gets 401
(authentication required) and an authenticated request without permission gets 403. With this
setting on, the 403 is replaced by 404, so an authenticated caller cannot tell a forbidden
resource from a missing one. Whether the anonymous 401 also becomes 404 is unspecified; an
implementation has to decide it, keeping in mind that package clients only send credentials after
a 401.

| Setting | Scope | Default |
|---|---|---|
| Hide existence of unauthorized resources | Global (all virtual repositories) | off |
| Same setting per virtual repository | One virtual repository | off |

UI location: security settings, general.

Interactions: package clients that interpret 401 as "send credentials" behave differently when
they get 404; see [formats/README.md](formats/README.md).

### IAM-033 · Older permission interfaces
Priority: **P2**

An older generation of the management interface is still documented and used by existing
automation:

- A repository-only form: name, one include pattern string and one exclude pattern string,
  a required repository list (local and remote only), and principal maps whose actions are given
  as one-letter codes. Administrator only. Builds and release sets are not covered.
- A second form with sections for repositories, builds and release sets; each section has
  include and exclude pattern lists (defaults: include `**` for repositories, empty for builds
  and release sets; exclude empty), a repository list (wildcards for any, any local and any
  remote), and user and group action maps using full action words (read, write/deploy, annotate,
  delete, manage, manage scan metadata, distribute). The build section's repository list is fixed
  to the build-information repository; the release-set section defaults to the default release-set
  repository.
- Create-or-replace semantics: the request replaces the policy; a section missing from the
  request is deleted. A delegated manager (IAM-029) may call it but only changes actions.
- Names with spaces are written with `+` in request paths by default (an instance setting can
  switch this off).
- Per-user and per-group lookups return every policy that applies, with the principal's actions
  flattened into a single list per section.
- An existence check for a policy name (200 if it exists, 404 otherwise).
- The companion CLI builds on this interface: an interactive wizard (terminal only) writes a
  policy template. The template has optional sections for repositories, builds and release sets;
  for the repository section the wizard asks for a comma-separated repository list (keys or the
  reserved "any", "any local", "any remote" names), include patterns (default `**`) and exclude
  patterns (default empty); for every section it then asks for users and groups and their actions
  (read, write, annotate, delete, manage, manage scan metadata, distribute). The repository list
  is a single comma-separated string, not a list. Create fails with 409 when the name exists;
  update requires the policy to exist and replaces it entirely; delete asks for confirmation
  unless run quietly and removes access immediately. Templates may contain placeholders that are
  substituted at apply time (one value per placeholder). The build section's repository list is
  ignored and always sent as the build-information repository. All three commands need an
  administrator. See [builds-and-ci.md](builds-and-ci.md) for the CLI.

Operations: list (names and URIs), get, create, create-or-replace, delete, per-user lookup,
per-group lookup, existence check; administrator or delegated manager for reads, administrator
for create and delete.

## Security settings

### IAM-034 · Control over anonymous grants by non-administrators
Priority: **P2**

Who may put the anonymous user into permission policies, and with which actions:

| Configuration | System administrators | Project administrators and delegated managers |
|---|---|---|
| Anonymous access on, non-administrator assignment allowed | Full control over the anonymous user's grants, any action | May put the anonymous user into a policy, change or drop its grant, but only with actions on the administrator's approved list |
| Anonymous access on, non-administrator assignment not allowed | Full control, any action | No changes to the anonymous user's grants at all |
| Anonymous access off | May only change or drop existing anonymous grants; adding the anonymous user to any policy is refused | No changes to the anonymous user's grants at all |

The check does not apply when a whole policy is deleted.

UI location: security settings, general (switch plus the approved-actions list). Default of the
switch: unspecified.

### IAM-035 · Encrypted client passwords
Priority: **P2**

Some clients keep credentials in plain-text files (for example Maven's settings
file). The system can issue each user an encrypted form of their password, generated from a
server-held secret, that clients store and send instead of the clear password. A central policy
decides how the server treats it:

| Policy value | Behavior | Default |
|---|---|---|
| Supported | Accept encrypted and clear passwords | default |
| Required | Every authenticated request must use the encrypted password | |
| Unsupported | Reject requests that use the encrypted password | |

Operation: a user retrieves their own encrypted password; the request must be authenticated with
the clear-text password (if made with a legacy API key, the encrypted API key is returned).

UI location: user-management settings, password encryption policy.

### IAM-036 · Basic-authentication controls
Priority: **P1**

Settings that restrict username/password login for internal users once an external provider is
configured:

| Setting | Meaning | Default |
|---|---|---|
| Disable basic authentication for internal users | Turns off username/password authentication for the UI and the API, including scripts and plugins that rely on it | in newer releases on (basic authentication disabled) when external authentication methods are configured; otherwise unspecified |
| Basic-authentication exemptions | Named internal users who keep username/password login while the switch above is on (failsafe accounts) | empty |
| Hide basic authentication on the login page | Removes the username/password form from the login page; it stays reachable through a link below the provider buttons | unspecified |
| Hide SSO providers | Hides the list of configured providers; users type the provider name instead. Only available when basic authentication is hidden | unspecified |

Lock-out guard: before disabling, configure an external method, create a group of external users
with administrator privileges (IAM-016), and optionally keep one or more exempt internal users.
If the switch is turned on without that preparation the instance can end up without any
administrator. Self-hosted recovery: place a configuration-import file for the identity service
that sets basic authentication back to enabled, then restart. On a hosted instance only the
operator's support can recover.

Operations (administrator): read whether basic authentication is enabled; enable or disable it.

Interactions: [authentication-providers.md](authentication-providers.md).

### IAM-037 · Token generation by basic-authenticated users
Priority: **P1**

A switch that allows users authenticated with username and password to create tokens through the
API, intended for automation accounts. Default unspecified.

Interactions: [access-tokens.md](access-tokens.md).

### IAM-038 · Token lifetime limits and expiry notifications
Priority: **MVP**

Instance-wide token settings in the security settings:

| Setting | Meaning | Default |
|---|---|---|
| Default token expiry | Lifetime pre-filled when creating new identity tokens; at least 0 and below the maximum | 31,536,000 seconds (365 days) |
| Maximum token expiry | Upper bound for tokens created by project administrators; 0 removes the limit and allows non-expiring tokens; any positive value forbids non-expiring tokens | 0 (unlimited) |
| Email notification before expiry | Sends users an email before their tokens expire | unspecified |
| Notice period | Days before expiry when the notification is sent | unspecified |
| Daily check time | Time of the daily scan for tokens expiring within 24 hours after the notice period (converted to UTC) | unspecified |
| Notification frequency | A single email, or one email per day until the token expires | unspecified |

Edge case: when tokens are synchronized between several instances, notifications should be
enabled on only one of them to avoid duplicates.

Interactions: [access-tokens.md](access-tokens.md) owns token semantics.

### IAM-039 · Multi-factor authentication (TOTP)
Priority: **P2**

An instance-wide switch requires a time-based one-time password from an authenticator app in
addition to the username and password, for all users.

- Enrollment: at the first login after the switch is on, the user logs in with credentials,
  scans a QR code with the authenticator app and confirms with a generated code.
- Login: credentials, then the current code.
- Reset: an administrator resets a user's enrollment from the user dialog; the user enrolls again.
- Administrator recovery: bootstrap a new administrator (IAM-010) and reset the enrollment.

UI location: user-management settings, multi-factor authentication. Default: unspecified. The
user record shows the enrollment status (older interface).

Interactions: identity-provider MFA in [authentication-providers.md](authentication-providers.md).

### IAM-040 · Hide global identity mappings from project administrators
Priority: **P2**

A switch in the OIDC part of the security settings: when on, project administrators see only the
OIDC identity mappings of the projects they administer, not the global ones. Set by a system
administrator in the all-projects scope. Default unspecified.

Interactions: [oidc-ci-integration.md](oidc-ci-integration.md), projects (IAM-051).

### IAM-041 · Password expiration policy
Priority: **P2**

Makes internal passwords age out: once a password is older than the configured number of days,
the account is locked until its owner sets a new password.

| Setting | Meaning | Default |
|---|---|---|
| Enable password expiration policy | Turns the policy on | unspecified |
| Password expires every (days) | Maximum password age | unspecified |
| Email notification before expiry | Users get an email a few days before expiry | unspecified |
| Force password expiration for all users | Action: every user must change the password at the next login | — |
| Unexpire expired passwords for all users | Action: clears the expired state for everyone | — |

Operations: get the policy (enabled, maximum age, notify flag); expire one user, a list of users,
or all users; unexpire all users; unexpire a single user (announced for removal). All
administrator.

UI location: user-management settings, password expiration policy.

### IAM-069 · Password strength policy
Priority: **P1**

Rules that every new internal password must satisfy. The policy is one instance-wide object:

| Setting | Meaning | Default |
|---|---|---|
| Minimum length | Fewest characters a password may have | unspecified |
| Minimum uppercase letters | Required count of uppercase characters | unspecified |
| Minimum lowercase letters | Required count of lowercase characters | unspecified |
| Minimum digits | Required count of digits | unspecified |
| Minimum special characters | Required count of non-alphanumeric characters | unspecified |
| Must differ from previous | Rejects a new password equal to the one it replaces | unspecified |

Operations (administrator):

- Set the whole policy: replaces every rule; a rule left out of the request falls back to its
  default (the defaults themselves are unspecified). Answers 204.
- Update the policy partially: only the rules in the request change. Answers 204.

Reading the policy back and the UI location are unspecified. Interactions: the policy matters for
internal users only (IAM-001); externally authenticated users are governed by their identity
provider. Password ageing is IAM-041, brute-force defenses are IAM-042.

### IAM-042 · Temporary login suspension and account lockout
Priority: **P1**

Two independent brute-force defenses:

**Temporary login suspension.** After a failed login, further attempts for that user are ignored
(answered with 403) for a short delay that grows with each consecutive failure, up to a maximum
of 60 seconds. Both the number of incorrect attempts before the delay starts and the maximum delay
are configurable, and the mechanism can be disabled. In a cluster each node counts separately: with
a threshold of 100 and three nodes, every node accepts 99 bad attempts before suspending.

**Account lockout.** When enabled, a user is locked after the configured number of failed logins
and stays locked until an administrator unlocks them.

| Setting | Meaning | Default |
|---|---|---|
| Account lockout enabled | Switches the lockout on | unspecified |
| Failed-login limit | Number of failed logins that locks the account | unspecified |
| Suspension threshold, maximum suspension delay | Temporary suspension tuning | maximum delay 60 seconds; threshold unspecified |

Operations (administrator): get and set the lock policy (enabled, attempts); list locked users;
unlock one user, a list of users, or all locked users. UI: a bulk unlock action next to the lock
settings, and unlock for one or several selected users in the users list.

Interactions: IAM-004 (locked status).

### IAM-043 · Inter-service join key
Priority: **Out of scope**

A shared AES-128 secret that establishes trust between the separately deployed services of one
installation; administrators can display it in the security settings. A single-binary product has
no internal service mesh to join.

### IAM-044 · Legacy API keys
Priority: **Out of scope**

Deprecated per-user static API keys: create, regenerate, view and revoke the own key; an
administrator can revoke another user's key or all keys in the system at once (after which every
script using them fails). Superseded by access tokens ([access-tokens.md](access-tokens.md)), which
are the recommended mechanism.

## Keys and secrets

### IAM-045 · Encryption of stored configuration secrets
Priority: **MVP**

Secrets that the system stores to reach other systems (directory bind passwords, upstream
credentials of remote repositories and similar) are encrypted at rest with a system-generated
master encryption key and decrypted only when used.

Behavior:

- Active by default: existing secrets in the stored configuration are encrypted and new or
  changed ones are encrypted automatically.
- Deactivate (administrator operation): decrypts all secrets, reloads the configuration and
  removes the key. Open: either new secrets are stored unencrypted afterwards, or deactivation is
  short-lived and the next configuration change switches encryption back on; the spec decides.
- Activate (administrator operation): creates a new key and encrypts; repeated activation while
  active is ignored.
- Backup and full export include the key when it is in its default location; a full import
  restores it and activates encryption with it.

Interactions: backup and export in [operations-and-monitoring.md](operations-and-monitoring.md);
remote-repository credentials in [remote-repositories.md](remote-repositories.md).

### IAM-046 · Signing key pairs
Priority: **P2**

A central key store for key pairs used to sign repository metadata and release sets. The system
signs repository metadata, not individual packages.

| Key type | Used for |
|---|---|
| GPG | Repository metadata of Debian, Opkg and RPM repositories; Terraform provider registry signing; signing release sets on creation or promotion, with propagation to the distribution subsystem |
| RSA | Signing and verifying the Alpine package index (local, federated and virtual repositories); signing and encrypting Hex content exchanged with the Mix client |
| ECDSA | npm package metadata signatures on local repositories, so that `npm audit signatures` can verify them |
| Ed25519 | Nix binary-cache metadata signatures on local repositories |

Fields when adding a key: name, alias (short nickname), private key, public key, optional
passphrase. The keys dashboard shows the number of keys, type, name, alias, primary and secondary
resource and the source (uploaded, or which secret-manager connector holds it; "no connector
configured" otherwise).

The operations also cover: create or replace, list, get, update, delete and
verify a key pair; fetch the public key a repository uses; set and remove a repository's keys;
download a repository's primary key and secondary public key. Each repository can carry a primary
and a secondary key: the secondary is the fallback once the primary has been removed, and a
separate operation promotes the secondary to primary (it fails when no secondary exists). All
key-pair operations require an administrator; further details are unspecified on this page.

UI location: security, keys management, signing keys tab.

Interactions: per-format signing in [formats/README.md](formats/README.md); IAM-047.

### IAM-047 · External secret-manager connectors (HashiCorp Vault)
Priority: **P2**

Signing keys can live in HashiCorp Vault instead of being uploaded. An administrator configures
one or more connectors and references key material by path and ID.

| Connector setting | Meaning | Default |
|---|---|---|
| URL | Base URL of the Vault server | required |
| Authentication | AppRole (role ID and secret ID), TLS certificate (certificate and private key), or agent auto-auth (a local Vault agent; self-hosted only) | required |
| Secret engine path | Mount path of the secret engine (the API accepts a list of mounts) | required |
| Secret engine type | KV version 1 or KV version 2 | required |

Behavior:

- "Test configuration" verifies a connector before saving.
- New GPG or RSA keys can be created as Vault-backed (pick the connector, enter details, test,
  save). Only RSA and GPG key pairs are specified for Vault; other key types are unspecified.
- An uploaded key can be changed to a Vault-backed key; the uploaded copy is deleted permanently.
- HashiCorp's hosted Vault service is not supported.
- Operations (administrator): set, list, test and delete connector configurations; key-pair
  operations accept a Vault reference instead of inline key material. The set operation notes that
  TLS must be enabled (whether on the server or towards Vault is unspecified).

UI location: security, Vault connectors list.

### IAM-048 · JAR signing keystore for virtual repositories
Priority: **P2**

A keystore of key pairs used to sign JAR files on download from a virtual repository (for Java
Web Start, which requires signed JARs). Workflow: set the keystore password first (needed for any
later change), upload a keystore file and unlock it with its password, add key pairs, remove key
pairs. A virtual repository's advanced settings select the key pair to sign with.

UI location: security, keys management, Java keystore tab.

Interactions: virtual repository settings in [repositories.md](repositories.md).

### IAM-049 · Trusted public keys
Priority: **Later**

A list of public GPG keys (alias plus key text or file) that establish trust between the
distribution subsystem, the source instance and edge instances, used to verify release-set
integrity.

UI location: security, keys management, public keys tab.

Interactions: [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### IAM-050 · SSH authentication
Priority: **P2**

Clients that speak SSH (Git LFS and the companion CLI) can authenticate with RSA key pairs.

| Server setting | Meaning | Default |
|---|---|---|
| Enable SSH authentication | Turns the SSH endpoint on | unspecified |
| Port | Port of the SSH listener | unspecified |
| Custom URL base | The instance's public base URL, shared with the general settings | unspecified |
| Server public and private key | Key pair the server uses to authenticate itself to clients | unspecified |

Users add their public key in their profile. Self-hosted administrators can restrict accepted
cipher, key-exchange, MAC and signature algorithms through include-only lists and exclude lists
per algorithm category; empty by default, in which case these defaults apply:

<details>
<summary>Default algorithm sets</summary>

| Category | Loaded by default | Deprecated (only when configured by name) |
|---|---|---|
| Ciphers | aes128-ctr, aes192-ctr, aes256-ctr, aes128-gcm@openssh.com, aes256-gcm@openssh.com, aes128-cbc, aes192-cbc, aes256-cbc | arcfour128, arcfour256, blowfish-cbc, 3des-cbc |
| Key exchange | diffie-hellman-group-exchange-sha256, diffie-hellman-group18-sha512, diffie-hellman-group17-sha512, diffie-hellman-group16-sha512, diffie-hellman-group15-sha512, diffie-hellman-group14-sha256 | diffie-hellman-group14-sha1, diffie-hellman-group-exchange-sha1, diffie-hellman-group1-sha1 |
| MAC | hmac-sha2-256-etm@openssh.com, hmac-sha2-512-etm@openssh.com, hmac-sha1-etm@openssh.com, hmac-sha2-256, hmac-sha2-512, hmac-sha1 | hmac-md5, hmac-md5-96, hmac-sha1-96 |
| Signatures | ecdsa-sha2-nistp256/384/521 and their cert-v01 variants, sk-ecdsa-sha2-nistp256@openssh.com, rsa-sha2-512, rsa-sha2-256 and their cert-v01 variants, ssh-rsa | ssh-dss, ssh-dss-cert-v01@openssh.com, ssh-rsa-cert-v01@openssh.com |

</details>

Not available on the hosted service.

UI location: security, keys management, SSH keys tab.

## Projects, roles and environments

### IAM-051 · Projects
Priority: **P2**

A project is a management unit that owns resources (repositories, builds, release sets, CI
pipeline resources) and has members who act on those resources through roles. Projects serve as a
filter: members see and act on the resources of their projects according to their roles. They
let system administrators delegate day-to-day administration to project administrators
(IAM-052).

<details>
<summary>Project settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Project key | Unique, immutable identifier; 2 to 32 characters, lowercase letters, digits and hyphens, starting with a letter (older releases required at least 3). Prefixed to the key of every repository created inside the project (IAM-057) and to the build-information repository of the project (IAM-060). | required |
| Display name | Human-readable name. The characters `: \| ? < > " * / @` are not allowed. Open: whether that restriction applies to the display name or to the project key; the spec decides. | required |
| Description | Free text | empty |
| Storage quota | See IAM-061 | unlimited |
| Project administrator privileges | See IAM-052 | see IAM-052 |
| Project administrators | Users and groups that receive the project administrator role | none |
| Repositories, distribution targets | Optionally assigned while creating the project | none |

</details>

Behavior:

- Only system administrators create, edit and delete projects, set quotas and assign project
  administrators. System administrators are implicitly project administrators of every project.
- A project can be deleted only when it holds no assigned or available resources; repositories
  must first be unassigned or moved to another project.
- Listing returns all projects to system administrators and only the administered projects to
  project administrators.
- When the project feature is introduced into an existing installation, all existing resources
  start as "unassigned" and can be assigned in bulk (IAM-057).
- Projects are not available on edge instances.
- Project context also filters views: in the all-projects scope and in the client setup
  instructions, a developer is shown only the virtual repositories of projects that concern them.

UI location: a project selector in the top bar switches between one project and "all projects".
With one project selected the administration area opens a project overview (edit and delete
controls); with all projects selected it opens an all-projects overview with a list view, a
dashboard view (per project: its administrators and quota status with alerts), search, and a
"create new" button. The creation dialog has tabs for project settings, repository assignment
and distribution targets.

Interactions: IAM-019 (project roles are the intended replacement for central
permission policies, which remain available for installations that predate projects); OIDC
identity mappings can be project-scoped ([oidc-ci-integration.md](oidc-ci-integration.md),
IAM-040); webhooks can be project-scoped for builds and release sets
([events-and-extensibility.md](events-and-extensibility.md)); retention policies can be
managed per project ([retention-and-cleanup.md](retention-and-cleanup.md)).

### IAM-052 · Project administrators and delegated privileges
Priority: **P2**

The project administrator role (a predefined global role, IAM-054) is the project-level
equivalent of a system administrator. Its powers are bounded by privileges a system administrator
sets per project:

| Privilege | Meaning | Default |
|---|---|---|
| Manage members | Add, change and remove project members and their roles; create, edit and delete project roles | on |
| Manage resources: repositories | Create/update and delete local, remote and virtual repositories, each repository class and each of create/update and delete set separately (six switches) | on |
| Manage resources: other resources | Covers the project's non-repository resources: builds, OIDC identity mappings, and retention policies for cleanup and archiving. It comes bundled with manage resources and has no switch of its own. Keeping it while clearing every repository switch yields a project administrator who handles retention but cannot create or delete repositories. | on |
| Index resources | Select which repositories, builds and release sets the scanning engine indexes | on |
| Scanning privileges | Manage project security assets (scan policies, scopes, reports) and ignore violations raised by global scan scopes, or create ignore rules | unspecified |

API compatibility: older clients send two coarse switches (manage resources, which drives the
local and virtual create/delete switches; manage remote repositories, which drives the remote
ones). If a request sets any of the six fine-grained switches, the coarse switches are ignored;
on creation omitted fine-grained switches default to on, on update they keep their current values.

Other rules:

- Project administrators can only create new repositories in their project; assigning existing
  repositories is reserved to system administrators.
- The project administrator role's actions cannot be modified; custom global environments are
  added to it automatically (IAM-064).
- UI: the project administrators list marks users or groups that are system administrators.

### IAM-053 · Project members
Priority: **P2**

Membership is a consequence of role assignment: holding at least one role (global or project role)
in a project makes a user or group a member, and a member can never be left without a role. Removing a member's last role
assignment removes the member. Each member may hold several roles; roles are cumulative (the
union of their actions), and when a global and a project-level role clash, the project-level role
takes precedence. Open: how a "clash" can arise between allow-only roles is
unspecified; the spec decides whether the precedence rule has any effect.

Example: a global role grants read and write in DEV and PROD; a project role
grants read, write and delete in DEV; a member holding both can read and write in both and delete
in DEV only.

UI location: project administration, user management, members, "add members" (choose users or
groups and their roles); also from the project overview's members-and-roles area.

Interactions: IAM-062 (anonymous user in projects); the user and group lists can be filtered by
project (IAM-009, IAM-018).

### IAM-054 · Global roles
Priority: **P2**

Global roles are role definitions maintained at system level and usable in every project. A
system administrator sets, per role, the environments it applies to and its actions (IAM-056).

Predefined global roles: project administrator, developer, contributor, viewer, release manager,
security manager and application administrator; a separate tab holds two machine-learning roles
(model governor, model developer). Predefined roles cannot be renamed or deleted, but their
actions and environments can be edited, except the project administrator role.

Custom global roles can be created, renamed, edited and deleted by a system administrator; at most
30 custom global roles. Each has a unique name, one or more global environments, and actions
chosen in basic or advanced mode.

A role has one of these types: project-administrator role, predefined, custom global, custom
project (IAM-055). Updates must keep the role's type.

UI location: administration area with "all projects" selected, user management, global roles
(edit icon at the end of each row; "create global role").

Operations: see IAM-068.

### IAM-055 · Project roles
Priority: **P2**

Project roles are defined inside one project by a project administrator (with the manage-members
privilege) or a system administrator, and combine with global roles to refine access.

Rules:

- A project role must grant at least one action; empty roles are not allowed.
- At most 30 roles per project.
- Environments are optional; both global and project environments can be selected (IAM-064).
- Actions granted through a role apply to all project resources of the matching type in the
  role's environments; roles have repository-level granularity and no path patterns.
- Basic and advanced action modes as in IAM-056.

Examples: automation engineer, contributor, annotator, QA tester, lead
developer.

UI location: project selected, user management, roles, project roles tab ("create project
role"); a second tab shows the global roles.

### IAM-056 · Role action catalog
Priority: **P2**

Roles (global and project) grant actions chosen in one of two modes. Basic mode bundles
fine-grained actions into categories; advanced mode exposes each resource type and action
separately. Switching from advanced back to basic discards the advanced choices. In the API,
actions are identified by constant names (for example a read-repository action and a
manage-members action).

<details>
<summary>Basic actions</summary>

| Category | Action | Expands to |
|---|---|---|
| Artifacts | Read | Read repository, read release set, read build, read pipeline |
| Artifacts | Write | Annotate and deploy/cache repository content, annotate and create release sets, annotate and deploy builds |
| Artifacts | Delete | Delete/overwrite repository content, delete release sets |
| Artifacts | Delete build | Delete builds. Open: whether it also covers deleting or overwriting artifacts; the spec decides. |
| Applications | Read, write, delete, promote application | Application entities of a release-governance subsystem: read applications and versions; create/update, bind/unbind resources, create and annotate versions; delete applications and versions; promote and release versions |
| Release-governance policies | Read, manage, delete | Read policies and evaluations; create/update; delete |
| Release sets | Promote | Promote release sets between stages |
| Integrated CI pipelines (IAM-028) | Trigger (and, in the global-role list, manage pipelines) | Manually trigger steps; create and edit pipeline sources |
| Scanning | Trigger security scan; manage scan data; manage scan scopes and policies; manage reports; read policies; ignore global violations | Scanning-engine functions |
| Distribution targets | Distribute | Distribute release sets to targets |
| Machine learning | Models, feature sets, data sources, secrets, runtime environments (read, manage, build, invoke, allow, deploy, query) | Machine-learning platform functions |

</details>

<details>
<summary>Advanced actions</summary>

| Resource type | Actions |
|---|---|
| Repositories | Read, annotate, deploy/cache, delete/overwrite, manage scan metadata (same semantics as IAM-022) |
| Builds | Read, annotate, deploy (upload and promote), delete, manage scan data (as IAM-025) |
| Release sets | Read, annotate, create, delete (including attached evidence), promote, distribute (project roles), manage scan metadata |
| Applications | Read, create/update, delete, bind/unbind |
| Application versions | Read, create, delete, annotate, promote/release |
| Release-governance policies | Read, create/update, delete |
| Integrated CI pipelines (IAM-028) | Read, trigger |
| Scanning | Read policies, manage scan scopes, manage policies, manage reports, ignore global violations |
| Distribution targets | Distribute, delete (remote deletion from targets) |
| Machine learning | Per resource: read, create, delete, query (online/offline), build, invoke, allow, deploy, log data, manual execution |

</details>

Edge cases: role actions contain no "manage" action for permission delegation; management inside
a project comes from the project administrator role and its privileges (IAM-052). Actions offered
depend on installed subsystems.

Interactions: IAM-022 and IAM-025 to IAM-027 define the per-resource semantics.

### IAM-057 · Repository ownership by projects
Priority: **P2**

A repository belongs to at most one project (it is "assigned" to it); unassigned repositories
belong to no project.

Behavior:

- Repositories created inside a project get the project key as a prefix of their key, and an
  environment must be chosen at creation (DEV by default) (IAM-064).
- Assign: a system administrator assigns an unassigned repository to a project, individually or
  in bulk from the unassigned-repositories view (tabs for local, remote, virtual, release-set and
  federated repositories). Federated repositories are not created inside a project; they are
  created first and then assigned to a project by a system administrator.
- Move: assigning an already assigned repository to another project moves it (ownership changes,
  the old assignment is removed); the assign operation has an optional force switch for moving a
  repository that already belongs to a project (what happens without it is unspecified).
- Unassign: removes the assignment; if the repository is shared, the administrator chooses
  whether the sharing stays.
- Status: for any repository the system reports the owning project, its environments, the
  projects it is shared with, whether it is shared with all projects, and whether the sharing is
  read-only.
- Ownership versus sharing: the owning project manages the repository; projects it is shared with
  (IAM-058) do not own it. Only the owner can delete a shared repository.

UI location: administration, repositories; the project column offers assign, move and unassign.

Interactions: [repositories.md](repositories.md) for repository keys.

### IAM-058 · Sharing repositories across projects
Priority: **P2**

A system administrator can open a local or remote repository to other projects: a single one,
a chosen set, or every project (the "every project" option also reaches projects created later).
Members of
a receiving project act on the shared repository according to their roles in the receiving
project; the receiving project does not own it.

| Setting | Meaning | Default |
|---|---|---|
| Sharing permission | Read only, or read and write: the ceiling for what receiving members can do | unspecified (the per-project share operation has an optional read-only switch; the share-with-all operation has none) |
| Receiving projects | Selected projects, or all projects including future ones | none |

Rules:

- Effective access of a member is the most restrictive of the sharing ceiling, the member's
  project role, and the member's system-level permissions.
- Unsharing (from one project or from all) leaves the repository assigned to its owner.
- Virtual repositories: the recommended practice is that every receiving project wraps the shared
  repository in a virtual repository of its own, so that permissions do not cross project lines;
  using a directly shared repository inside virtual repositories is only partly supported.
- Best practice: consumers of a shared repository get read-only access,
  whichever sharing method is used, so ownership stays with the producer.
- An alternative "pull" pattern is a remote repository in the consuming project that points at
  the producer's repository URL with credentials, which keeps a cached copy even if the producer
  deletes the original (see [remote-repositories.md](remote-repositories.md)).

UI location: administration, repositories, actions menu, project sharing (tabs: sharing
permissions, receiving projects).

### IAM-059 · Project-aware sharing through internal remote repositories
Priority: **Later**

A sharing mode in which every request from a receiving project carries that project's identity,
so each receiving project can apply its own upstream-curation and scanning policies and downloads
can be attributed per project. Direct sharing and project-aware sharing can coexist in one
instance, but not for the same repository.

Behavior:

- The receiving project administrator connects once by creating an "internal remote repository":
  a remote repository pointing at the shared repository of the same instance instead of at an
  external upstream. Its default key joins the receiving project key and the shared repository key
  with a hyphen. It can be added to virtual repositories and given lifecycle stages during
  creation; it can also be set up through the ordinary remote-repository form by anyone who may
  add remote repositories there.
- A receiving project that bypasses the internal remote repository and calls the shared
  repository directly is refused.
- Read-only share: only content that is already cached, either in the internal remote repository
  or in the shared repository, is served; the shared repository makes no upstream fetch on behalf
  of the receiver, and a miss ends in "not found".
- Read-and-write share: a member who may write to the internal remote repository can cause the
  shared repository to fetch from its external upstream. Because access always goes through a
  remote repository, uploading into a shared local repository is impossible in this mode.
- Unconnected shares remain reviewable for 60 days (a banner in the receiving project); later they
  can be connected from a shared-repositories view.
- Connecting is not possible from the all-projects scope or from the owning project.
- After unsharing, the receiving project's internal remote repositories are refused by the source;
  they are not removed automatically and stay in place until someone deletes or repoints them, and
  the UI warns about remotes whose share can no longer be connected.
- Switching an existing direct share to project-aware sends the receiving projects through the
  same review-and-connect step as a new share.
- Works only within one instance, not across instances or federation.

### IAM-060 · Builds in projects
Priority: **P2**

Every project gets its own build-information repository, created automatically when the project is
created and named after the project key with a fixed suffix; neither system nor project
administrators can delete it. Build information uploaded in a project's context is stored there
and governed by the project's roles (build actions, IAM-056). A single build belongs to one
project at a time. System administrators can upload builds to a project.

Interactions: [builds-and-ci.md](builds-and-ci.md); IAM-025 (central build permissions only
cover the default build-information repository).

### IAM-061 · Project storage quotas
Priority: **P2**

A system administrator can set a storage quota per project; it applies to the total virtual size
of the repositories assigned to the project.

| Setting | Meaning | Default |
|---|---|---|
| Storage quota | Limit, entered in GB in the UI and in bytes in the API | unlimited |
| Soft limit | Whether the quota only warns (reported by the API) | unspecified |
| Email notification | Whether an email is sent when the quota is exceeded (reported by the API) | unspecified |

Thresholds: below 75% the usage bar is green; between 75% and 95% orange with a warning; at
100% red with a critical warning. Open: on a self-hosted installation the quota either blocks
(project users cannot deploy once it is exceeded) or is a notification trigger that never blocks;
the spec decides. On a hosted instance the limit is informational with a "deploy beyond limit"
option. The project's storage tab lists the artifact size per repository.

Interactions: [storage-and-blobs.md](storage-and-blobs.md).

### IAM-062 · Anonymous access in projects
Priority: **P2**

The anonymous user is kept out of projects as a hardening measure: project administrators manage
ordinary users and groups as members, but the system refuses the anonymous user as a member of a
new project. An instance-level configuration switch (off by default) lifts the restriction.

Interactions: IAM-005, IAM-034.

### IAM-063 · Environments
Priority: **P2**

Environments group resources (repositories, pipeline sources and similar) so that roles can be
scoped by maturity, for example development versus production.

| Aspect | Rule |
|---|---|
| Predefined environments | DEV and PROD, global, cannot be deleted |
| Global custom environments | Created by a system administrator; available to all projects |
| Project custom environments | Created by a project administrator (or a system administrator) for one project; the project name is put in front of the environment name, which is why two projects may use the same environment name |
| Name | At most 32 characters; case-insensitive. Open: either names are unique in the instance, or a global name is unique instance-wide and a project name unique within its project; the spec decides |
| Limits | Open: the default is either 30 global and 30 per project, configurable at instance level, or 10, extendable to 30 as the recommended maximum; the spec decides. 30 is the upper recommendation either way |
| Ordering | Environments can be reordered by drag and drop |
| Deletion | Only when no role and no repository references the environment; DEV and PROD can never be deleted |

Operations: create, list, rename and delete global environments (system administrator; listing
also for project administrators); create, list, rename and delete project environments (project or
system administrator). Create answers 201 and 409 when the name exists; rename takes the new name
(same 32-character limit); delete answers 204, or 404 for an unknown environment or project.
Environments are attached to roles through the role operations and to repositories through the
repository configuration (IAM-064).

UI location: administration, environments, with a scope selector (all, or one project); "create
new environment" / "create new project environment"; actions menu, delete.

Edge case: lifecycle stages are the successor of environments, with environments still supported
(see [distribution-and-lifecycle.md](distribution-and-lifecycle.md)). Newer terminology says
"stage" where older terminology says "environment".

### IAM-064 · Environments on roles and repositories
Priority: **P2**

Environments connect roles to repositories: a role grants its actions on the project resources
that are in the role's environments.

- Roles: a system administrator assigns global environments to global roles; a project
  administrator assigns global and project environments to project roles. The project
  administrator role cannot be edited; custom global environments are added to it automatically.
- Repositories: each repository is assigned to one or more environments (open: whether a
  repository is limited to a single environment instead; the spec decides). A repository created in
  a project must have an environment; DEV is preselected.
- Effect: a member's action on a project repository is granted when one of the member's roles
  holds that action and lists an environment the repository is assigned to.
- A recommended pattern: an "external" environment for remote repositories in which developers
  hold read and write (so they can populate caches) while internal DEV and PROD repositories stay
  read-only for them.

UI location: user management, global roles or roles, edit a role, pick environments and actions;
repository settings, basic tab, environments.

Interactions: IAM-023 (write/cache on remote repositories), IAM-054, IAM-055.

### IAM-065 · Project-scoped scan policies, scopes and reports
Priority: **Later**

With a scanning engine, policies, scan scopes and reports exist at global level (usable across
projects; created by system administrators, security managers or users with the matching role)
and at project level (created by project administrators, security managers or users with the
matching role). Violations from global scopes appear in whichever project holds the resource and
can only be ignored by holders of the "ignore global violations" privilege; violations from
project scopes are visible only to project members and can be ignored by project-level managers.
Global scopes contain only global policies.

Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md).

### IAM-066 · Migration from permission policies to projects
Priority: **Later**

A wizard (a beta feature, system administrators only) converts existing permission policies into
a project structure without touching existing repositories:

1. Define the project: how projects map to the organization (per team, per application, other);
   the users and groups to include; name, key and storage quota.
2. Select the existing permission policies that apply to the chosen members (searchable, filterable
   by user or group). Policies built on the "any local" and "any remote" wildcards are never mapped
   automatically.
3. Repository structure: one new project repository per selected policy with the source
   repository's configuration, plus a virtual repository that also includes the source
   repository, so existing clients keep working; editable names, a stage per repository, a
   conflicts-and-alerts panel and the virtual resolution order.
4. Role mapping, first match wins: policy includes manage → project administrator; read only →
   viewer; read, annotate and write without delete → contributor; read, write, delete and
   annotate without scan → developer; read and scan → security manager; anything else → no role
   (set manually).
5. Review and start.

Migrations are listed with a status (not started; in progress, with resume and edit; migrated,
with view and edit). New repositories are created because a repository can belong to only one
project; roles are stage-based; existing policies may be path-based while roles apply per
repository; the source stays untouched. Legacy repositories are never decommissioned
automatically.

### IAM-067 · Project count limits per subscription
Priority: **Out of scope**

Capping the number of projects an installation may create by subscription, with purchasable
increments. Billing and license management of a hosted service; no meaning for a self-hosted
repository manager.

### IAM-068 · Project and role management operations
Priority: **P2**

<details>
<summary>Operations</summary>

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create project | Key and display name (required); description; administrator privileges (IAM-052); storage quota in bytes | 201 with the project; 409 if the key exists | system administrator |
| List projects | none | Projects with key, display name, description, privileges, quota, soft-limit and notification flags | system administrator (all) or project administrator (own) |
| Get project | Key | Project; 404 if unknown | system or project administrator |
| Update project | Key; display name, description, privileges, quota | Updated project | system administrator |
| Delete project | Key | 204; 404 if unknown | system administrator |
| List projects using a global role | Role name (global roles only); page size (minimum 1, default 1000); descending order; cursor | Project names and keys plus a cursor | system administrator |
| List / get project users | Key (and username) | Members with their role names | system administrator, project administrator with manage members, or (list only) a token scoped to read the project's user members |
| Add or update a project user | Key, username, role names | Member with roles | system administrator or project administrator with manage members |
| Remove a project user | Key, username | 204 | same |
| List / get / add-or-update / remove project groups | As for users, by group name | As for users | same (list also with a token scoped to read the project's group members) |
| List project roles | Key | Roles with name, description, type, environments, actions | system or project administrator, or a token scoped to read the project's roles |
| Create / get / update / delete project role | Key, role name; type (only custom project roles can be updated), description, environments, actions | Role | create, update and delete: system administrator or project administrator with manage members; get: system or project administrator |
| Create custom global role | Name, description, type (custom global), environments, actions | 201; 409 if it exists | system administrator |
| List global roles | none | Predefined and custom roles with type, environments, actions | system administrator or project administrator |
| Get / update / delete global role | Role name; on update name, description, unchanged type, environments, actions | Role; delete only for custom roles | system administrator |
| Assign or move repository to project | Repository key, target project key, optional force switch for a repository that already belongs to a project | 204; 404 for an unknown repository or project | system administrator |
| Unassign repository | Repository key | 204 | system administrator |
| Get repository project status | Repository key | Owner project, environments, shared-with projects, shared-with-all flag, read-only flag | system administrator |
| Share with a project | Repository key (local or remote), target project key, optional read-only switch | 204 | system administrator |
| Share with all projects | Repository key (local or remote) | 204 | system administrator |
| Unshare from a project / from all projects | Repository key, target project key (or none); the repository stays assigned to its owner | 204 | system administrator |
| Create / list / rename / delete global environment | Environment name; new name for rename | 201 (409 if it exists) / list of names / renamed name / 204 | system administrator; listing also project administrator |
| Create / list / rename / delete project environment | Project key, environment name; new name for rename | 201 (409 if it exists) / list of names / renamed name / 204 | project or system administrator |

</details>

## Notes for implementers

- Deny by default and allow-only: no permission policy or role can subtract access. Exclusion
  exists only as exclude patterns inside one policy, so a broad policy elsewhere still grants the
  path (IAM-019, IAM-021).
- On remote repositories, read alone serves only cached content; clients that are expected to
  pull new upstream packages (including anonymous ones) need deploy/cache as well. This is the
  most common cause of "works for one user, 404 for another" on proxies (IAM-023).
- Virtual repositories carry no permissions; every decision is made against the member
  repository that actually serves the content. npm metadata paths must be covered too, or
  metadata through a virtual repository fails while tarballs work (IAM-021, IAM-024).
- "Any local" and "any remote" selectors are dynamic and cover repositories created later;
  combined with the anonymous user they silently expose new repositories (IAM-005, IAM-020).
- Status codes are part of the contract: 401 for anonymous, 403 for authenticated callers, and 404
  instead of 403 when existence is hidden; what hiding does to the anonymous 401 is unspecified
  and must be decided deliberately, because package clients only send credentials after a 401.
  Whatever is chosen must hold on every protocol endpoint (IAM-032).
- Overwriting an existing artifact requires delete/overwrite in addition to deploy; immutability of
  releases rests on withholding it (IAM-022).
- Delegated managers can by default grant only what they hold and never "manage"; an instance
  switch relaxes this, and the UI blocks self-edits only to prevent lock-out (IAM-029).
- Auto-join groups also apply to the short-lived users created during an OIDC token exchange, which
  widens CI tokens beyond the identity mapping unless the mapping names no user (IAM-015).
- Older permission interfaces replace the whole policy on update: a section missing from the
  request is deleted. Tools built on them (including the companion CLI's template commands)
  depend on that full-replace behavior (IAM-033).
- Login suspension counts failures per cluster node, not cluster-wide; a shared counter is needed
  if the threshold must hold across replicas (IAM-042).
