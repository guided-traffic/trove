# Operation catalog: administration API

This page lists the administration operations a full-featured repository manager exposes over its
management API: identities (users, groups, roles, projects), permission policies, tokens,
authentication providers (OIDC, SAML, LDAP, SCIM and others), security settings, system
configuration and diagnostics, retention policies, scripting hooks and multi-site management.
Each entry names the operations, their inputs and outputs, status codes and the required
privilege, described generically and without wire-level paths; the capability behind an
operation is described on the feature page the entry links to. Content operations (artifacts,
repositories, search, builds, webhooks) are in [api-content-operations.md](api-content-operations.md).
The priority is trove's, per the rubric in [README.md](README.md).
A "UI:" line names the screen where an operation has one; entries without it have no UI screen
specified.

## Conventions shared by the administration operations

### ADMAPI-001 · Callers and privilege tiers
Priority: **MVP**

Every administration operation states which caller may invoke it. The tiers used throughout this
page:

| Tier | Who | Typical operations |
|---|---|---|
| Unauthenticated | Anyone, including anonymous callers | Health probes (ADMAPI-069), version (ADMAPI-070), OIDC token exchange (ADMAPI-031; the presented ID token is the credential) |
| Authenticated user, self-service | Any logged-in user acting on their own account | Create identity tokens, list and revoke own tokens (ADMAPI-039, ADMAPI-043), change own password, own legacy key |
| Anonymous where anonymous access is on | Anonymous callers if the system allows anonymous access | Public verification keys (ADMAPI-058, ADMAPI-059, ADMAPI-060) |
| Scoped token | A token carrying a system or project read scope instead of administrator rights | The read operations of ADMAPI-041; SCIM operations with identity read, write and delete scopes (ADMAPI-035) |
| Delegated manager | Users or groups with a delegated role (webhook manager, resource manager) or the manage action on a permission policy | Webhook administration (ADMAPI-027); editing the action maps of a policy in the older interface (ADMAPI-025) |
| Project administrator | Member holding the project-administrator role, narrowed by the project's administrator switches (manage members, manage resources, per-repository-class create and delete) | Project members and roles (ADMAPI-064, ADMAPI-066), project environments, project-scoped identity mappings (ADMAPI-030), project tokens, hooks of the project |
| System administrator | Administrator flag on the user or one of its groups | Everything else |

Credentials: the current interfaces take a bearer token; the token-creation operation also takes
Basic credentials when the security settings allow it (IAM-037); several older operations also
accept Basic and the legacy API key. Interactions: TOK-033, TOK-034, IAM-002, IAM-007.

### ADMAPI-002 · Status codes and response bodies
Priority: **MVP**

The administration operations follow common HTTP conventions, with exceptions that compatible
clients rely on:

| Situation | Usual answer | Exceptions |
|---|---|---|
| Created | 201 with the stored record | OIDC provider and identity mapping create answer 200; custom global role create answers 201 with a status object (code, message, detail); key-pair create answers 201 with an empty body; older "create or replace" operations answer 200 with a confirmation text |
| Read | 200 with JSON | Several older operations answer plain text (configuration document XML, system information, service ID, encrypted password, logs of import and export) |
| Updated | 200 with the stored record | Password policy, SAML switch and some security settings answer 204 without body; instance registry update answers 204 |
| Deleted | 204 | Older interfaces answer 200 with a confirmation; cleanup and archive policy deletes answer 200 (the older archive delete with a text message); tunnel delete is idempotent (204 even if absent) |
| Long-running work accepted | 202 | Policy runs and stops, asynchronous re-index, identity configuration export, full federation broadcast (which uses 202 for "already running") |
| Invalid input | 400 | An unknown user or group is 400 instead of 404 in a few operations (cloud IAM binding read and remove) |
| Not authenticated / not allowed | 401 / 403 | Some operations specify no 403 (SCIM) |
| Unknown resource | 404 | — |
| Duplicate name or key | 409 | Also used for "operation already in progress" (tunnel pairing, support bundle on the tunnel client, portal actions) and for a topology whose cleanup failed |
| Feature not active | 503 / 405 | Lock diagnostics without native advisory locks (503); offline registration without offline mode (405) |

Interactions: [api-content-operations.md](api-content-operations.md) (same conventions for
content operations).

### ADMAPI-003 · Paging styles
Priority: **MVP**

Four paging styles coexist; most other list operations return the full list.

| Style | Used by | Inputs | Outputs |
|---|---|---|---|
| Opaque cursor | Users (ADMAPI-006), groups (ADMAPI-015), projects using a role (ADMAPI-063) | page size (default 1000, minimum 1), cursor, descending order | entries plus the cursor for the next page |
| Name cursor | Permission policies (ADMAPI-021) | cursor = a policy name to start from; page size 1 to 99,999 (open: whether both bounds are inclusive or exclusive) | entries plus the name of the last entry; empty when nothing follows |
| Page number | Cleanup runs (ADMAPI-088) | page, page size, sort field, sort order | runs plus page size, current page, last page, total |
| Opaque page token | Topology entity monitoring (ADMAPI-098) | page token | entities, next page token (empty when done), total count |

SCIM lists (ADMAPI-035) use the SCIM start index and count instead. How a cursor behaves when
entries are added or removed between pages is unspecified.

## Users

The user model is described in [identity-and-access.md](identity-and-access.md) (IAM-001 to
IAM-012). The current interface addresses users by username.

### ADMAPI-004 · Create user
Priority: **MVP**

Creates an internal user (the realm is always the internal one).

<details>
<summary>Inputs</summary>

| Input | Meaning | Default |
|---|---|---|
| Username | Unique login name (required) | — |
| Password | Initial password; not required when the internal password is disabled | — |
| Email | Email address (required) | — |
| Groups | Groups to join | none |
| Administrator | System administrator flag (IAM-002) | off |
| Profile updatable | The user may edit their own profile | on |
| Internal password disabled | The user cannot log in with an internal password (external login only) | off |
| UI access disabled | API-only user (IAM-003) | off |
| Scan watch manager, policy viewer, policy manager, reports manager | Scan-related delegated roles (IAM-008) | unspecified |
| Resources manager | Delegated role (IAM-007) | unspecified |
| Webhook manager | Delegated role (IAM-007) | off |
| Platform auditor | Read-only auditor role (IAM-006) | off |

</details>

Output (201): the user record of ADMAPI-005. Errors: 400 invalid input; 401; 403; 409 user
exists. Privilege: administrator.

Interactions: IAM-009. For trove, INV-7 limits internal users to the break-glass administrator
and service accounts; OIDC users are created just in time (AUTHN-005).

UI: administration area, user management, users list and user dialog (IAM-001).

### ADMAPI-005 · Get user
Priority: **MVP**

Input: username. Output: username, email, administrator flag, effective-administrator flag (true
also when administrator rights come from a group, IAM-016), profile-updatable, UI-access-disabled,
internal-password-disabled, last login (date-time), realm, groups, status (invited, enabled,
disabled, locked) and the delegated role flags of ADMAPI-004. Errors: 400, 401, 403,
404. Privilege: administrator, or a token with the identities-read system scope.

An older read operation for the same record (administrator only, addressed by username) is
deprecated in favor of this one. It returns name, email, last login, realm, administrator
flag, profile-updatable, internal-password-disabled, groups, UI-access-disabled, the scan-related
role flags, a multi-factor status (none, enabled, disabled) and an account status (enabled,
disabled, locked); it has no invited status and no webhook, resource-manager or auditor flags.

UI: administration area, user management, users list (IAM-001).

### ADMAPI-006 · List users
Priority: **MVP**

<details>
<summary>Inputs (all optional)</summary>

| Input | Meaning | Default |
|---|---|---|
| Status | Only users in this status | all |
| Username | Substring match on the username | — |
| Administrators only | Only administrators; not combinable with a repository name | off |
| Role | Only users holding this role; when given, the username and administrators-only filters are ignored | — |
| Resource type | Required when filtering by a repository role | — |
| Repository name | Narrow the role filter to one repository; not combinable with administrators-only | — |
| Project key | Search within one project | — |
| Descending order | Sort descending | unspecified (ascending implied) |
| Page size | Number of results | 1000; minimum 1 |
| Cursor | Continue after this cursor | — |

</details>

Output: entries of username, URI, realm and status, plus a cursor for the next page. Errors: 400,
401, 403. Privilege: administrator, or the identities-read system scope. Paging: ADMAPI-003.

UI: administration area, user management, users list (IAM-001).

### ADMAPI-007 · Update user (partial)
Priority: **MVP**

Input: username plus any subset of: email, password, administrator, profile updatable, internal
password disabled, UI access disabled, and the delegated role flags of ADMAPI-004.
Omitted fields keep their value. When the internal password is switched from disabled back to
enabled, a password is mandatory in the same request. Username, realm and groups cannot be
changed here (groups: ADMAPI-009).

Output (200): the full user record of ADMAPI-005. Errors: 400, 401, 403, 404. Privilege:
administrator.

UI: user dialog in the users list (IAM-001).

### ADMAPI-008 · Delete user
Priority: **MVP**

Input: username. Output: 204. Errors: 401, 403, 404. Privilege: administrator. What happens to the
user's tokens, group memberships and permission grants on deletion is unspecified in the
operation description (see IAM-009, AUTHN-030).

UI: users list (IAM-001).

### ADMAPI-009 · Add or remove groups of a user
Priority: **MVP**

Input: username; a list of groups to join and a list of groups to leave (both optional). Output
(200): the user's resulting group list. Errors: 400, 401, 403, 404 (unknown user). Privilege:
administrator. The group-side counterpart is ADMAPI-018; both change the same
membership relation.

UI: user dialog and group dialog (IAM-001, IAM-013).

### ADMAPI-010 · Change a user's password
Priority: **P2**

Input: username; new password (required). Output: 204. Errors: 400 invalid input, 401, 403, 404.
Privilege: administrator, or the user themself. Whether the password policy (ADMAPI-054)
is enforced here is unspecified. Interactions: AUTHN-003, AUTHN-042 (self-service password
change).

### ADMAPI-011 · Password expiry operations
Priority: **P2**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Expire one user's password | Username | 204; 404 | administrator |
| Expire all users' passwords | none | 200 with a plain-text body | administrator |
| Unexpire all expired passwords | none | 200 with a plain-text body | administrator |

An expired password must be changed at the next login. The older interface has list-based and
single-user variants (ADMAPI-051). Interactions: IAM-041.

UI: user-management settings, password expiration policy (IAM-041).

### ADMAPI-012 · Unlock a user
Priority: **P1**

Input: username. Output: 204. Errors: 401, 403, 404. Privilege: administrator. Lifts a lock caused
by repeated failed logins (IAM-042); the older interface offers list and all-user variants
(ADMAPI-050).

UI: user management (IAM-042).

### ADMAPI-114 · Deprecated identity operations of the repository service
Priority: **Later**

Before identity management moved into the separate identity service, the repository service
offered its own user, group, password and security-configuration operations. They are still
available, marked deprecated, and scheduled for removal: on hosted deployments by a fixed date,
on self-managed installations first switched off behind a flag for a 90-day grace period and then
removed in a later release. Automation written against the older generation still calls them.

<details>
<summary>Operations and their successors</summary>

| Operation | Behavior of the deprecated form | Successor |
|---|---|---|
| List users | Bare array of name, absolute URI and realm; no paging | ADMAPI-006 (cursor paging, relative URIs, status per entry) |
| Get user | User record with a last-login timestamp, an offline-mode flag and a multi-factor status, which the successor drops | ADMAPI-005 |
| Create or replace user | One call creates or overwrites; fields not sent are reset to their defaults | ADMAPI-004 (create only) and ADMAPI-007 |
| Update user | Fields not sent are reset to their defaults; group membership can be set in the same call | ADMAPI-007 (true partial update) and ADMAPI-009 (groups) |
| Delete user | 200 with a plain-text confirmation | ADMAPI-008 (204) |
| Expire one user's password | — | ADMAPI-011 (204) |
| Expire all users' passwords | — | ADMAPI-011 |
| Change password | Username, old password and the new password twice in the body; an administrator may change any password, every non-anonymous user their own; plain-text answer | ADMAPI-010 (username in the address, new password only, 204) |
| Unlock one user | Plain-text answer | ADMAPI-012 (204) |
| List groups | Bare array; no paging | ADMAPI-015 |
| Get group | Optional switch to include the member usernames (default off) | ADMAPI-014 (members always returned) |
| Create or replace group | 201 with an empty body; fields not sent are reset to their defaults; members can be given | ADMAPI-013 (create only, full record in the answer) and ADMAPI-016 |
| Update group | 200 without body; members can be set in the same call | ADMAPI-016 (partial) and ADMAPI-018 (members) |
| Delete group | 200 with a plain-text confirmation | ADMAPI-017 (204) |
| Read the security configuration | The whole security configuration as one XML document; its contents are not enumerated | ADMAPI-084 (export to a file on the server instead of a response body) |
| Save the security configuration | Replaces the whole security configuration from such a document; only a document of the same release is accepted | ADMAPI-084 (import from a fixed file location) |
| Read and set the password expiration policy | Enabled flag, maximum age, notify-by-email flag | The account-lockout policy object of the identity service carries the maximum age; an age above zero means enabled; the e-mail flag is not exposed (ADMAPI-050, ADMAPI-051) |

</details>

Privilege: administrator for all of them, except the self-service password change. Names in the
request address follow the "+"-for-space rule of ADMAPI-025. An optional server switch rejects
usernames with upper-case letters when users are added to a group (default off). The deprecated
token and permission-policy operations of the same generation are TOK-046 and ADMAPI-026.

For trove: no client that trove targets (npm, NuGet, the CI token exchange) needs these
operations; they only matter for migrating automation written against the older generation.

## Groups

The group model is described in IAM-013 to IAM-018 in
[identity-and-access.md](identity-and-access.md). Groups are addressed by name.

### ADMAPI-013 · Create group
Priority: **MVP**

Creates a group in the internal realm, or imports one directory group.

<details>
<summary>Inputs</summary>

| Input | Meaning | Default |
|---|---|---|
| Name | Group name (required); for an import, the group's name in the directory | — |
| Description | Free text | unspecified |
| Auto-join | New users join the group automatically (IAM-015) | off |
| Administrator privileges | Members are system administrators (IAM-016) | off |
| External ID | Identifier assigned by a SCIM client | unspecified |
| Import from directory group setting | Name of a directory group setting (ADMAPI-034) to import this group from | — |
| Members | Initial member usernames; not combinable with an import | none |
| Reports manager, scan watch manager, policy manager, policy viewer | Scan-related delegated roles for members (IAM-008) | off |
| Resource management in the default scope | Members manage resources outside projects (IAM-007) | off |
| Webhook manager | Members manage webhooks (IAM-007) | off |

</details>

Directory import: the given name is matched against the name attribute of the groups found
through the named setting, with whatever comparison rule the directory applies to that attribute
(standard name attributes such as the common name usually ignore case; a directory that compares
exactly needs the exact spelling). The match must resolve to exactly one group. The stored group
belongs to the directory realm and takes the directory's name lower-cased, so the created name can
differ from the request (the response carries it); this equals what an import from the
directory-groups screen produces. Membership is then maintained by directory logins, which is why
an initial member list is refused for an import.

Output (201): the group record of ADMAPI-014. Errors: 400; 401; 403; 404 when a member,
the directory group setting or the directory group does not exist; 409 group exists. Privilege:
administrator. Interactions: IAM-018, AUTHN-026 (bulk import through ADMAPI-034 refresh).

UI: administration area, user management, groups list, "new group" dialog (IAM-013); directory imports also from the directory group settings (AUTHN-025).

### ADMAPI-014 · Get group
Priority: **MVP**

Input: group name. Output: name, description, auto-join, administrator privileges, realm, realm
attributes, external ID, members, and the delegated role flags of ADMAPI-013. Errors: 400,
401, 403, 404. Privilege: administrator, or the identities-read system scope.

UI: groups list (IAM-013).

### ADMAPI-015 · List groups
Priority: **MVP**

<details>
<summary>Inputs (all optional)</summary>

| Input | Meaning | Default |
|---|---|---|
| Page size | Number of results | 1000; minimum 1 |
| Descending order | Descending lexical order | off |
| Group name | Name pattern filter | — |
| Cursor | Continue after this cursor | — |
| Role, resource type, repository name | Only groups holding this role on this repository; the three must be given together | — |
| Project key | Only groups of this project | — |

</details>

Output: entries of group name and URI, plus a cursor. Errors: 400, 401, 403. Privilege:
administrator, or the identities-read system scope. Paging: ADMAPI-003.

UI: groups list (IAM-013).

### ADMAPI-016 · Update group
Priority: **MVP**

Partial update. Input: group name plus any subset of description, auto-join, administrator
privileges, external ID and the delegated role flags. Omitted fields keep their value; an empty
body succeeds with 200. Realm and realm attributes cannot be changed; members are changed with
ADMAPI-018. Output (200): the full group record. Errors: 400, 401, 403, 404. Privilege:
administrator.

UI: group dialog (IAM-013).

### ADMAPI-017 · Delete group
Priority: **MVP**

Input: group name. Output: 204. Errors: 401, 403, 404. Privilege: administrator.

UI: groups list (IAM-013).

### ADMAPI-018 · Add or remove group members
Priority: **MVP**

Input: group name; a list of usernames to add and a list to remove. Output (200): the resulting
member list. Errors: 401, 403, 404 (unknown group). Privilege: administrator. Behavior for unknown
usernames in the lists is unspecified.

UI: group dialog (IAM-013).

## Global roles

Roles are named bundles of actions used in projects (ADMAPI-066). A global role is
defined once and is available in every project; project roles are defined per project.

### ADMAPI-019 · Global role operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create custom global role | Role record; the type must be "custom global" | 201 with a status object (code, message, optional detail); 400; 409 role exists | administrator |
| List global roles | none | All global roles: the project-administrator role, the predefined roles (for example a viewer role) and custom global roles | administrator or project administrator |
| Get global role | Role name | The role; 404 | administrator |
| Update global role | Role name; role record whose name matches the path and whose type equals the current type; description, environments and actions optional | 200; 400; 404 | administrator |
| Delete custom global role | Role name | 204; 400 invalid request; 404 | administrator |

<details>
<summary>Role record</summary>

| Field (descriptive) | Meaning |
|---|---|
| Name | Role name (required) |
| Description | Free text |
| Type | Project administrator role, predefined role, or custom global role. An update must send the role's current type; type changes are rejected. |
| Environments | Environments (ADMAPI-068) the role's actions apply to |
| Actions | Granted actions as named constants, for example "read repository" or "manage members" |

</details>

Only custom global roles can be created; which roles can be deleted beyond custom ones is
unspecified (the operation is named for custom roles only). Interactions: ADMAPI-066,
TOK-011.

## Permission policies

The permission model (resources, include and exclude patterns, actions, evaluation) is described
in IAM-019 to IAM-034 in [identity-and-access.md](identity-and-access.md). The current interface
models a policy as a name, a description and up to three resource sections; two older interfaces
remain for compatibility (ADMAPI-025, ADMAPI-026).

### ADMAPI-020 · Create permission policy
Priority: **MVP**

<details>
<summary>Policy document</summary>

| Element (descriptive) | Meaning |
|---|---|
| Name | Unique policy name (required) |
| Description | Free text |
| Repository-content section | Grants on repositories (IAM-020 to IAM-024) |
| Build section | Grants on build information (IAM-025) |
| Release-set section | Grants on release sets (IAM-026) |
| Section: user actions | Map of username to the list of granted actions |
| Section: group actions | Map of group name to the list of granted actions |
| Section: targets | Map of target name to its include-pattern list and exclude-pattern list; the operation does not define what a target name denotes per resource type (the older interface, ADMAPI-025, uses repository keys and repository wildcards) |

</details>

Output (201): the stored policy. Errors: 400, 401, 403, 409 (name exists). Privilege:
administrator. Interactions: IAM-019, IAM-031.

UI: administration area, user management, permissions, "new permission" wizard (IAM-019).

### ADMAPI-021 · List permission policies
Priority: **MVP**

<details>
<summary>Inputs (all optional)</summary>

| Input | Meaning | Default / limit |
|---|---|---|
| Cursor | Policy name to start listing from | — |
| Page size | Number of results | unspecified; 1 to 99,999 (open: whether both bounds are inclusive or exclusive) |
| Users | Comma-separated usernames; a policy is returned when it grants any action to one or more of them | — |
| Groups | Comma-separated group names; same rule | — |
| Include inherited groups | With a user filter, also match policies granted to the user's groups | on |
| Expand | With a user or group filter, return full policies instead of names; rejected (400) without a filter | off |

</details>

Output: entries of policy name and URI, plus a cursor (the name of the last policy on the page;
empty when nothing follows). With expand: full policies whose action maps are narrowed to the
requested principals, so actions of other principals in the same policy are omitted. Username
filters ignore case; group-name filters compare exactly. Naming a user or group that does not
exist yields 404. Policies
generated from project roles are never listed; stored policies that belong to a project are.

Privilege: administrator, or the permissions-read system scope. The user and group filters
replace the per-user and per-group lookups of the older interface (ADMAPI-025).

UI: permissions list (IAM-019).

### ADMAPI-022 · Get permission policy
Priority: **MVP**

Input: policy name. Output: the full policy document. Errors: 400, 401, 403, 404. Privilege:
administrator, or the permissions-read system scope.

UI: permissions list (IAM-019).

### ADMAPI-023 · Delete permission policy
Priority: **MVP**

Input: policy name. Output: 204. Errors: 400, 401, 403, 404. Privilege: administrator.

UI: permissions list (IAM-019).

### ADMAPI-024 · Operate on one resource section of a policy
Priority: **MVP**

Four operations address one section of a policy by policy name and resource type (repository
content, build, release set):

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get section | Name, resource type | The section (user actions, group actions, targets); 404 if policy or section missing | administrator |
| Replace section | Name, resource type, full section | 200 with the stored section; 400; 404 if the policy is missing | administrator |
| Update section | Name, resource type, partial section | 200 with the stored section; 400; 404 if the policy is missing | administrator |
| Delete section | Name, resource type | 204; 404 if policy or section missing | administrator |

Replace overwrites the section with the sent action maps and targets. Update is a partial
change; whether its maps merge key by key or replace whole maps is unspecified. These operations
let a client change one resource type of a policy without resending the others, which avoids the
replace-drops-sections trap of ADMAPI-025.

UI: the resource tabs of the permission wizard (IAM-019).

### ADMAPI-025 · Older permission interface with resource sections
Priority: **P2**

The previous generation of the policy interface (IAM-033). A policy has a name (at most 64
characters) and three sections — repositories, builds, release sets — each with include patterns,
exclude patterns, a repository list and user and group action maps.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List policies | none | Array of name and URI | administrator, or a user with the manage action on a policy |
| Get policy | Name | Full policy; 404 | administrator, or manage action on it |
| Existence check (HEAD) | Name | 200 if it exists; 404 otherwise | administrator, or manage action on it |
| Create policy | Name; policy | 201 | administrator |
| Create or replace policy | Name; policy | 200 | administrator, or a user with the manage action (who may change only the action maps) |
| Delete policy | Name | 200; 404 | administrator |
| Policies of a user | Username | Full policies that apply to the user | administrator, or manage action |
| Policies of a group | Group name | Full policies that apply to the group | administrator, or manage action |

<details>
<summary>Section defaults and rules</summary>

| Element | Rule | Default |
|---|---|---|
| Include patterns | Paths included | repository section: everything (`**`); build and release-set sections: empty |
| Exclude patterns | Paths excluded | empty |
| Repositories | Repository keys, or wildcards for all repositories, all local or all remote repositories; virtual repositories are not supported | build section: the built-in build-information repository, not changeable; release-set section: the built-in release-set repository |
| Actions | read, write, annotate, delete, manage, scan-metadata management, distribute | — |

</details>

Replace semantics: the body replaces the stored policy; a section missing from the body is
deleted, so updating a policy that has repository and build sections with only a repository
section drops the build section. Missing values take the defaults of the section type.
Release-set sections on edge instances accept only the built-in release-set repository; with an
empty repository list that repository is filled in.

Name encoding: a "+" in a user, group or policy name in the request path stands for a space by
default; a server setting turns this translation off so that "+" is literal.

UI: none; API only.

### ADMAPI-026 · Oldest repository-only permission interface
Priority: **P2**

The oldest interface manages repository permissions only (no build or release-set sections).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get policy | Name | Name, include pattern (default everything), exclude pattern (default none), repository list (local and remote only; required), user and group action maps | administrator |
| Create or replace policy | Name; same document | 200 with a confirmation message | administrator |

Actions are encoded as short letter codes: one letter each for manage, delete, deploy, annotate,
read and distribute, and a three-letter code for scan-metadata management. Missing values take defaults. The same "+"-for-space rule as in
ADMAPI-025 applies. Interactions: IAM-033.

UI: none; API only.

## Webhook administration

Webhook subscriptions, handlers, delivery logs and their operations are catalogued in
[api-content-operations.md](api-content-operations.md) and described in EVT-018 to EVT-034 in
[events-and-extensibility.md](events-and-extensibility.md). This entry pins only the
administration-side controls that the identity operations on this page carry.

### ADMAPI-027 · Webhook management rights and webhook token scope
Priority: **P1**

Who may manage webhooks is decided by three administration-side controls:

| Control | Where it is set | Effect |
|---|---|---|
| Webhook manager flag on a user | ADMAPI-004, ADMAPI-007 (default off) | The user may manage webhooks without being an administrator (IAM-007) |
| Webhook manager flag on a group | ADMAPI-013, ADMAPI-016 (default off) | All members may manage webhooks |
| Webhook token scope | ADMAPI-039 scope input | A token limited to webhook operations, see below |

The webhook token scope carries a project key, an optional event domain and a set of
actions: read, write, delete, execute. The domain narrows the token to one category of event
types (for example artifact events or curation events). Write on a domain allows creating
subscriptions for that domain's events. With read alone a token may list subscriptions, read one
subscription and stream or fetch delivery troubleshooting data (ADMAPI-041). What
"execute" permits is unspecified.

Interactions: EVT-030 (webhook management privileges), EVT-022 (project-scoped subscriptions),
TOK-014.

UI: role flags in the user and group dialogs (IAM-007); webhooks page (EVT-034).

## Authentication providers

Configuration operations for the ways users and machines authenticate. Login behavior is
described in [authentication-providers.md](authentication-providers.md); CI token exchange in
[oidc-ci-integration.md](oidc-ci-integration.md).

### ADMAPI-028 · OIDC provider configuration operations
Priority: **MVP**

Create, list, read, update and delete the provider configurations that CI token exchange trusts
(CIOIDC-001). A configuration is addressed by its provider name.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create provider | Provider record (below) | 200 (response body unspecified); 400 on invalid input | administrator |
| List providers | none | All provider records | administrator or project administrator |
| Get provider | Provider name | The record; 404 if unknown | administrator or project administrator |
| Update provider | Provider name; full record | 200 (response body unspecified); 400; no 404 is specified | administrator |
| Delete provider | Provider name | 204; 404 if unknown | administrator |

<details>
<summary>Provider record</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Provider name | Unique handle; clients send it on exchange | — |
| Issuer URL | The provider's issuer URL | — |
| Audience | Expected audience of incoming tokens | unspecified |
| Description | Free text | unspecified |
| Provider type | For example generic OpenID Connect or GitHub | unspecified |
| Token issuer | Labelled "token issuer (organization)"; for the GitHub type it holds the GitHub organization name. Its meaning for other types is unspecified | unspecified |
| Permissive configuration | GitHub type only: accept tokens without the organization restriction (not recommended) | unspecified |
| Use default outbound proxy | Route calls to the provider through the system-wide proxy | unspecified |
| Azure application ID | Azure type only: needed when the application signs with custom keys because of claims mapping | unspecified |

</details>

The operation schema marks no field as required; which fields are effectively mandatory follows
CIOIDC-001. Create answers 200, not 201. Whether update can rename a provider, and what happens
to its identity mappings on delete, is unspecified.

UI: administration area, integrations, OpenID Connect (CIOIDC-001). Interactions: CIOIDC-005.

### ADMAPI-029 · Identity mapping operations
Priority: **MVP**

Identity mappings belong to one provider configuration and decide which token an exchange yields
(CIOIDC-007). They are addressed by provider name and mapping name.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create mapping | Provider name; mapping record | 200 with only the derived project key; 400 | administrator, or project administrator for a project-scoped mapping |
| List mappings of a provider | Provider name; optional project key filter | All mapping records; 404 if the provider is unknown | administrator or project administrator |
| Get mapping | Provider name, mapping name | The record; 404 | administrator or project administrator |
| Update mapping | Provider name, mapping name; full record | 200 with only the derived project key; 400; 404 | administrator, or project administrator for a project-scoped mapping |
| Delete mapping | Provider name, mapping name | 204; 404 | administrator, or project administrator for a project-scoped mapping |

<details>
<summary>Mapping record</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Mapping name | Unique within the provider | — |
| Description | Free text | unspecified |
| Claims filter | JSON object of claim names to expected values; all must match (CIOIDC-008) | unspecified |
| Priority | Evaluation order; lower numbers first (CIOIDC-009) | unspecified |
| Token specification: username | Fixed subject of the issued token | unspecified |
| Token specification: username pattern | Subject derived from claims (CIOIDC-011, CIOIDC-012) | unspecified |
| Token specification: groups pattern | Group names derived from claims | unspecified |
| Token specification: scope | Scope of the issued token, same grammar as ADMAPI-040 | unspecified |
| Token specification: audience | Audience of the issued token | unspecified |
| Token specification: expiry in seconds | Lifetime of the issued token | unspecified |
| Token specification: self-revocable | Whether the holder may revoke the issued token | unspecified |
| Project key (output only) | Derived from the scope, see ADMAPI-030 | — |

</details>

No field is marked as required. Interactions: CIOIDC-007 to CIOIDC-017.

UI: integrations page, row menu of a provider, "add identity mapping" (CIOIDC-007).

### ADMAPI-030 · Project-scoped identity mappings
Priority: **P1**

A mapping becomes project-scoped solely through its scope: if the token scope grants project
roles of one project, that project's key is the mapping's project key. A group- or user-scope
mapping is global. There is no project-key input on create or update; the key in responses is
derived. Only one project per mapping is allowed; roles of two projects in one scope are
rejected. An update must keep the same derived project; changing it is rejected with 400.

| Operation | System administrator | Project administrator with resource-management right | Project administrator without it |
|---|---|---|---|
| Create, update, delete a project-scoped mapping | yes, any project | own projects only | no (403) |
| Create, update, delete a global mapping | yes | no (403) | no (403) |
| List project-scoped mappings | all projects | own projects | own projects |
| List global mappings | yes | depends on the visibility switch (IAM-040) | depends on the switch |
| Token exchange | no authentication | no authentication | no authentication |

Selection order during an exchange that names a project: that project's mappings first, then
global ones; inside each tier, lower priority number first; equal priority, earlier creation
first; the first mapping whose claims filter matches wins. Without a project in the exchange,
all mappings compete by priority and creation time alone. A project mapping with priority 5
therefore beats a global mapping with priority 1 when the exchange names the project.

Interactions: CIOIDC-015, CIOIDC-016, CIOIDC-009, ADMAPI-066.

UI: project selected in the project selector, integrations page (CIOIDC-015).

### ADMAPI-031 · OIDC token exchange
Priority: **MVP**

Callable without credentials: the presented ID token is the credential. The body is JSON (not the form encoding of
[RFC 8693](https://www.rfc-editor.org/rfc/rfc8693)) and uses the RFC 8693 parameter names
`grant_type` (token-exchange URN), `subject_token_type` (ID-token URN) and `subject_token`, plus
non-standard inputs: provider name, project key, application key, identity mapping name, include
reference token, provider type, and CI run metadata (job ID, run ID, repository, revision,
branch). None is marked as required.

Output (200): access token, token type, lifetime in seconds, issued token type, the username the
token represents, and a reference token when requested. Errors: 400 invalid input; 401 invalid
credentials. Which of the two an unknown provider name or a claim set that matches no mapping
produces is unspecified.

Interactions: CIOIDC-018 to CIOIDC-023; ADMAPI-030 for precedence.

UI: none; machine-to-machine endpoint.

### ADMAPI-032 · SAML configuration operations
Priority: **P2**

Named SAML service-provider configurations plus one global on/off switch (AUTHN-015 to
AUTHN-021).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create configuration | Configuration record | 201 with the record; 400 | administrator |
| List configurations | none | All records | administrator |
| Get configuration | Name | The record; 404 | administrator |
| Update configuration | Name; record | 200 with the record; 400 | administrator |
| Delete configuration | Name | 204 | administrator |
| Get general settings | none | SAML login enabled flag; whether a certificate is present | administrator |
| Enable or disable SAML login | Enabled flag (required) | 204; 400 | administrator |

<details>
<summary>Configuration record</summary>

| Field (descriptive) | Meaning | Required |
|---|---|---|
| Name | Unique configuration name | yes |
| Integration enabled | Whether this configuration is active | no |
| Verify audience restriction | Check the assertion's audience restriction | no |
| Login URL | Identity provider's single sign-on URL | yes |
| Logout URL | Identity provider's logout URL | yes |
| Certificate | Identity provider's signing certificate | yes |
| Service provider name | Entity ID the system presents | yes |
| Auto-create users | Create users on first login (AUTHN-005) | no |
| Allow profile access | Externally created users may open their profile (AUTHN-006) | no |
| Use encrypted assertion | Expect encrypted assertions (AUTHN-017) | no |
| Auto-redirect | Send unauthenticated browsers straight to the identity provider (AUTHN-019) | no |
| Synchronize groups | Take group membership from an assertion attribute (AUTHN-018) | no |
| Group attribute | Assertion attribute carrying group names | no |
| Email attribute | Assertion attribute carrying the email address | no |
| Name ID attribute | Attribute used as username instead of the subject name ID | no |
| Directory group settings | Directory group settings (ADMAPI-034) used to look up groups for SAML users (AUTHN-021) | no |

</details>

Defaults of the boolean fields are unspecified. Delete specifies no 404.

UI: administration, authentication section, SAML page (AUTHN-016).

### ADMAPI-033 · Directory (LDAP) connection operations
Priority: **P2**

Named directory connection settings, addressed by key (AUTHN-022 to AUTHN-024).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create settings | Settings record | 201 with key, enabled flag and directory URL; 400 | administrator |
| List settings | none | All records | administrator |
| Get settings | Key | The record; 404 | administrator |
| Update settings | Settings record (the key is in the body, not the path) | 200 (response body unspecified); 400 | administrator |
| Delete settings | Key | 204; 404 | administrator |
| Test connection | A full settings record (need not be saved) | 204 on success; 400 when the test fails | administrator |

<details>
<summary>Settings record</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Key | Unique name (required) | — |
| Enabled | Whether the connection is used for login | on |
| Directory URL | LDAP or LDAPS URL of the directory (required); see AUTHN-022 for the base-DN convention | — |
| User DN pattern | Pattern that builds a user's DN from the username | unspecified |
| Search filter | Filter used to find a user | unspecified |
| Search base | Base for the user search | unspecified |
| Search subtree | Search below the base recursively | unspecified |
| Manager DN | Bind DN used for searches | unspecified |
| Manager password | Bind password; returned by read operations | unspecified |
| Auto-create users | Create users on first login | unspecified |
| Email attribute | Directory attribute holding the email | unspecified |
| Poisoning protection | Directory poisoning protection; exact checks unspecified (AUTHN-022) | unspecified |
| Allow profile access | Externally created users may open their profile | unspecified |
| Paging support | Use paged search results | unspecified |

</details>

Security: the read operations include the manager password field; whether its value is masked is
unspecified. An implementation should never return the secret.

UI: administration, authentication section, directory (LDAP) page (AUTHN-022).

### ADMAPI-034 · Directory group settings, search and import operations
Priority: **P2**

Group settings describe where and how groups are found in a directory and map them to local
groups (AUTHN-025, AUTHN-026).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create group settings | Group settings record | 201 with the record; 400 | administrator |
| List group settings | none | All records | administrator |
| Get group settings | Name | The record; 404 | administrator |
| Update group settings | Record (name in the body) | 200 (response body unspecified); 400 | administrator |
| Delete group settings | Name | 204; 404 | administrator |
| Refresh | Settings name; mode: update existing groups, import new groups, or both (default both); optional username to limit to one user's groups (default all users) | 200 with a plain-text message stating how many groups were imported; 400; 404 | administrator |
| Search directory groups | Group settings record; optional username | Found groups: name, description, DN, and whether the local copy needs an update | administrator |
| Read mapping strategy | Strategy name | Group member attribute, group-name attribute and description for that strategy | administrator |
| Import selected groups | Group settings record plus the list of groups to import (as returned by search) | 204; 400 | administrator |

<details>
<summary>Group settings record</summary>

| Field (descriptive) | Meaning |
|---|---|
| Name | Unique name (required) |
| Directory connection | Key of the connection settings (ADMAPI-033) to use (required) |
| Group base DN | Where groups are searched |
| Group name attribute | Attribute holding the group name |
| Group member attribute | Attribute listing members |
| Subtree | Search recursively |
| Force attribute search | Always search group attributes |
| Filter | Group search filter |
| Description attribute | Attribute used as description |
| Strategy | Mapping strategy: static (group entries list members), dynamic (user entries list groups) or hierarchy (groups encoded in the user DN), see AUTHN-025 |

</details>

Single-group import is also possible through ADMAPI-013. Defaults are unspecified.

UI: directory (LDAP) page, group settings list (AUTHN-025).

### ADMAPI-035 · SCIM 2.0 provisioning endpoint
Priority: **P1**

A SCIM 2.0 service provider ([RFC 7643](https://www.rfc-editor.org/rfc/rfc7643),
[RFC 7644](https://www.rfc-editor.org/rfc/rfc7644)) for identity providers that push users and
groups (AUTHN-028 to AUTHN-030). Bodies use the `application/scim+json` media type and the SCIM
core schemas; list responses use the SCIM list-response envelope (total results, items per page,
start index, resources).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Resource types | none | The supported resource types (user, group) | administrator or identities-read scope |
| Schemas | none | All supported schema definitions | administrator or identities-read scope |
| Schema by ID | Schema ID | One schema; 404 | administrator or identities-read scope |
| Service provider configuration | none | Supported SCIM features and authentication schemes | administrator or identities-read scope |
| List or filter users | Optional SCIM filter (equality on the user name is supported), start index, count, excluded attributes | List response; each user with ID, user name, active flag, emails (value, primary), groups, schemas, resource type | administrator or identities-read scope |
| Create user | SCIM user | 201 with the user; 400; 409 exists | administrator or identities-write scope |
| Get user | User name; optional excluded attributes | The user; 404 | administrator or identities-read scope |
| Replace user | User name; full SCIM user | 200 with the user; 400; 404 | administrator or identities-write scope |
| Patch user (also used to disable by setting active to false) | User name; SCIM patch operations | 200 with the user; 400; 404 | administrator or identities-write scope |
| Delete user | User name | 204; 404 | administrator or identities-delete scope |
| List or filter groups | Optional SCIM filter (equality on the display name is supported), start index, count, excluded attributes | List response; each group with display name, ID, description, members, schemas, resource type | administrator or identities-read scope |
| Create group | SCIM group | 201; 400; 409 exists | administrator or identities-write scope |
| Get group | Group name; optional excluded attributes | The group; 404 | administrator or identities-read scope |
| Replace group | Group name; full SCIM group | 200; 400; 404 | administrator or identities-write scope |
| Patch group | Group name; SCIM patch operations | 200 with the group, or 204 without body; 400; 404 | administrator or identities-write scope |
| Delete group | Group name | 204; 404 | administrator or identities-delete scope |

Edge cases: resources are addressed by user name and group name, and the SCIM ID equals the user
name or group name in example responses; whether that always holds is unspecified. A list page of
20 items is an example value; the default page size itself is unspecified. The SCIM operations
specify no 403. Which filter operators beyond equality are supported is unspecified. Identity
write and delete scopes appear only here; they extend the identities-read system scope of TOK-014.

UI: none; called by identity providers.

### ADMAPI-036 · Trusted reverse-proxy login configuration
Priority: **P2**

One singleton configuration for logins where a fronting proxy has already authenticated the user
and passes the identity in a request variable (AUTHN-031). Self-hosted only.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get configuration | none | The record | administrator |
| Create or update configuration | The record | 200 (response body unspecified); 400 | administrator |

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Proxied login enabled | Trust the identity supplied by the proxy | unspecified |
| Auto-create users | Create users on first login | unspecified |
| Allow profile access | Externally created users may open their profile | unspecified |
| Remote-user request variable | Name of the header or request attribute carrying the username | unspecified |
| Synchronize directory groups | Look up the user's groups in the directory (ADMAPI-034) | unspecified |

Security: enabling this trusts whatever sends the variable, so a deployment must make sure that
only the authenticating proxy can set it (AUTHN-031).

UI: administration, authentication section, HTTP SSO page (AUTHN-031).

### ADMAPI-037 · Crowd connector configuration
Priority: **Later**

One singleton configuration for delegating login to an Atlassian Crowd or Jira user directory
(AUTHN-032, AUTHN-033).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get configuration | none | The record (including the application password) | administrator |
| Create or update configuration | The record | 200 (response body unspecified); 400 | administrator |

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Integration enabled | Use the connector | unspecified |
| Server URL | Crowd server URL | unspecified |
| Application name | Name the system authenticates as | unspecified |
| Application password | Credential of that application | unspecified |
| Session validation interval | How often a Crowd session is revalidated (unit unspecified, integer) | unspecified |
| Use default outbound proxy | Route calls through the system-wide proxy | unspecified |
| Auto-create users | Create users on first login | unspecified |
| Allow profile access | Externally created users may open their profile | unspecified |
| Custom cookie token key | Custom name of the single-sign-on cookie | unspecified |
| Direct authentication | Authenticate API requests directly against Crowd | unspecified |
| Override all groups on login | Replace the user's groups with Crowd's on each login | unspecified |

### ADMAPI-038 · Cloud IAM role binding operations
Priority: **P2**

Binds an AWS IAM role (by ARN) to a user; the binding is what the signed cloud caller-identity
exchange resolves to a user (CIOIDC-027, CIOIDC-028).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Bind role | Username (required); role ARN (required) | 204; 400; 404 unknown user; 409 the role is already bound to another user | administrator |
| Get binding | Username | Username and role ARN; 400 unknown user; 404 no role bound | administrator |
| Remove binding | Username | 204; 400 unknown user | administrator |

A role can be bound to at most one user. Note the inconsistent status codes: an unknown user is
404 on bind but 400 on read and remove.

## Access tokens

Semantics of the token model (types, scopes, lifetimes, revocation) live in
[access-tokens.md](access-tokens.md). The entries below pin the operations.

### ADMAPI-039 · Create or refresh an access token
Priority: **MVP**

One operation both issues new tokens and refreshes existing ones; a grant-type input selects the
mode. The body is accepted either form-encoded or as JSON with identical fields.

<details>
<summary>Inputs</summary>

| Input | Meaning | Default / limit |
|---|---|---|
| Grant type | "client credentials" issues a new token; "refresh token" refreshes one | client credentials |
| Refresh token | The refresh secret of the token to refresh; required in refresh mode | — |
| Username | Subject the token is issued for | unspecified; at most 255 characters |
| Scope | Space-separated list of scope expressions (TOK-007) | the user-permissions scope (TOK-008); at most 500 characters |
| Expiry in seconds | Lifetime; 0 means non-expiring and is reserved to administrators | the default expiry (ADMAPI-042); minimum 0 |
| Refreshable | Issue a refresh secret with the token | off |
| Description | Free text | at most 1024 characters |
| Audience | Space-separated list of service identifiers that accept the token | all services; at most 255 characters |
| Include reference token | Also return a short reference token (TOK-004) | off |
| Force revocable | Make the token revocable even when it would otherwise not be (TOK-022) | off |

</details>

Outputs (200): the token secret, the token type (normally "Bearer"), the lifetime in seconds, the
granted scope, the refresh secret (only when refreshable) and the reference token (only when
requested).

Errors: 400 for an invalid expiry (for example not a number); 401 when unauthenticated; 403 when the expiry
exceeds the administrator-defined maximum or the caller may not request the scope.

Privileges:

- Any authenticated user may create a token for themself with the user-permissions scope (an
  identity token, TOK-005). Only administrators may request any other scope or a non-expiring
  token; an administrator-scope token may even name a username that does not exist (TOK-009,
  TOK-015).
- A project administrator may create tokens tied to projects they administer (TOK-011).
- Authenticating this call with username and password (Basic) works only for internal users,
  only when token creation over Basic authentication is enabled in the security settings
  (IAM-037), and never for a user with multi-factor authentication enabled.
- Refresh: the caller must be the same user as the subject of the token being refreshed.

UI: the token management screen (TOK-026) and the user profile page (AUTHN-039) call this
operation. Interactions: TOK-025, TOK-024 (refresh), TOK-017 to TOK-019 (lifetime rules),
CIOIDC-018 (tokens issued by exchange instead).

### ADMAPI-040 · Scope expressions accepted by token creation
Priority: **MVP**

The scope input of ADMAPI-039 is a space-separated list (at most 500 characters) of the
expressions defined in [access-tokens.md](access-tokens.md):

| Family | Expression parts | Who may request it |
|---|---|---|
| Applied user permissions (TOK-008) | — | any user, for themself (default) |
| Applied administrator permissions (TOK-009) | — | administrators |
| Applied group permissions (TOK-010) | one or more group names; names containing spaces or commas are quoted | administrators |
| Applied project-role permissions (TOK-011) | project key plus one or more role names; multi-word role names quoted | administrators; project administrators for their projects |
| Resource permission (TOK-012) | resource type, target (exact name or pattern), optional sub-resource (Ant-style path pattern), comma-separated action letters | administrators |
| Project membership read (TOK-013) | project key plus members (users, groups, or a wildcard) or roles; read only | administrators |
| System scopes (TOK-014) | see ADMAPI-041 | administrators |

Resource permission scopes need no pre-existing permission policy. The only resource types are
"repository content" (target: repository key, sub-resource: artifact path; actions read, write
— deploy or cache —, delete or overwrite, annotate, scan, manage; "*" for all) and "project"
(target: project key, sub-resource: members; action read). The full action alphabet is read,
write, delete, annotate, execute, scan and manage; which resource type accepts "execute" is
unspecified. A resource scope on the content type with target "*" and read access lets the
holder list repositories.

Interactions: TOK-007 to TOK-014; CIOIDC-007 (identity mappings carry a scope of the same
grammar).

UI: token management screen (TOK-026).

### ADMAPI-041 · Read operations open to non-administrators through system scopes
Priority: **P1**

Several administration read operations accept, besides an administrator, a token that carries a
matching system scope (TOK-014), so read-only automation needs no administrator rights.

| Read operation | Scope that opens it |
|---|---|
| Installed licenses (ADMAPI-103) | system scope "licenses read" |
| Storage summary ([storage-and-blobs.md](storage-and-blobs.md)) | system scope "storage information read" |
| List repositories ([api-content-operations.md](api-content-operations.md)) | resource scope on all repositories with read, or system scope "information read" |
| Repository configuration | repository-configuration read scope for that repository |
| List users, get user (ADMAPI-006, ADMAPI-005) | system scope "identities read" |
| List groups, get group (ADMAPI-015, ADMAPI-014) | system scope "identities read" |
| List and get permission policies (ADMAPI-021, ADMAPI-022) | system scope "permissions read" |
| Project users, groups, roles (ADMAPI-064, ADMAPI-065, ADMAPI-066) | project membership read scope of that project |
| List, get, troubleshoot webhook subscriptions | webhook scope with read, optionally narrowed to a project and an event domain (ADMAPI-027) |
| System metrics, live logs ([operations-and-monitoring.md](operations-and-monitoring.md)) | system scopes "metrics read" and "live logs read" |
| Metrics of the network tunnel client (ADMAPI-105) | system scope "metrics read" |

Interactions: TOK-014, TOK-013, IAM-007.

UI: none; API only.

### ADMAPI-042 · Token lifetime policy settings
Priority: **MVP**

Three server settings shape what ADMAPI-039 accepts. They live in the identity
service's configuration file, not in an API document.

| Setting | Meaning | Default |
|---|---|---|
| Default expiry | Lifetime applied when a request gives no expiry. 0 means no default is forced. Non-negative; must not exceed the maximum expiry when that is set. | 1 year (31,536,000 seconds) |
| Maximum expiry for non-administrators | Upper bound for tokens of non-administrator users. 0 or unset means no bound: administrators may then create non-expiring tokens and users may pick any lifetime. Any positive value forbids non-expiring user tokens and caps user lifetimes. Non-negative; must be greater than or equal to the default expiry. | 0 |
| Allow refreshable user tokens | When off, every user token is created non-refreshable regardless of the request. | on |

When an expiry is forced and the request omits one, the default applies to user tokens.

Interactions: TOK-018, TOK-019, TOK-024, IAM-038; ADMAPI-047 exposes the default expiry
over the API.

UI: global security settings, token expiration section (TOK-018).

### ADMAPI-043 · List tokens
Priority: **MVP**

Returns the tokens visible to the caller: administrators see every token, other users only their
own. Reference tokens are included.

Output: a list of token records, each with token ID, subject, expiry (Unix seconds), issued-at
(Unix seconds), issuer, description, refreshable flag, scope, and last-used time (Unix seconds;
present only when last-used tracking is enabled on the server, TOK-029). The token secret is never
returned. Errors: 401, 403. Filters: open; the operation may take optional filters, but none is
specified.

Privilege: any authenticated caller. Interactions: TOK-027, TOK-029, TOK-026 (UI).

UI: token management screen (TOK-026).

### ADMAPI-044 · Get token details
Priority: **MVP**

Input: a token ID, or the alias "me" for the token that authenticates the call. Output: the same
record as in ADMAPI-043. Errors: 401, 403, 404 (unknown ID).

Privilege: administrators read any token; other users only tokens whose subject is their own
username. Interactions: TOK-028.

UI: token management screen (TOK-026).

### ADMAPI-045 · Revoke a token by ID
Priority: **MVP**

Input: a token ID, or "me" to revoke the token that authenticates the call. Output: 200 (body
unspecified). Errors: 401, 403, 404.

Privilege: users revoke their own tokens; administrators revoke any token. Interactions: TOK-030,
TOK-032.

UI: token management screen (TOK-026).

### ADMAPI-046 · Revoke a token by value
Priority: **MVP**

Input: the token secret, either a reference token or a self-contained token. A self-contained
token is revocable only if it was created force-revocable or refreshable. Output: 200 with the ID
of the revoked token. Errors: 401, 403, 404 (unknown or not revocable).

Privilege: administrator. Interactions: TOK-031, TOK-020, TOK-022.

### ADMAPI-047 · Read and set the default token expiry
Priority: **MVP**

Two operations read and replace the system-wide default token expiration in seconds. Input of the
write operation: the new value (required, integer). Outputs: the current value (read); 200 with no
body (write). Errors: 401, 403.

Privilege: administrator. Whether this value is the same setting as the configuration-file
default expiry in ADMAPI-042 is unspecified; both describe "the default expiry". Interactions:
TOK-018.

UI: global security settings, token expiration section (TOK-018).

### ADMAPI-048 · Create a pairing token
Priority: **Later**

Issues a pairing token (a JWT) with which two services establish trust. Input: a use case from a closed list — registration with the central multi-site
console (ADMAPI-096), pairing with the network tunnel service (ADMAPI-105), or
cross-instance federation of curation policies (ADMAPI-101). Which use cases exist depends on the services
installed. Output: the pairing token. Errors: 401, 403, 404 (unknown or unsupported use case).

Privilege: administrator. Interactions: TOK-042.

UI: token management screen, pairing-token tab (TOK-042).

### ADMAPI-049 · Legacy API key operations
Priority: **Later**

A deprecated per-user static key (TOK-044, AUTHN-043). Each user has at most one.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create own key | none | 200 with the key; 400 if a key already exists (regenerate instead) | authenticated user |
| Regenerate own key | none | 200 with the new key | authenticated user |
| Get own key | none | 200 with the key; 404 if none exists | authenticated user |
| Revoke own key, or all keys | Optional "delete all" switch (0 or 1) | 200 with a confirmation message | own key: authenticated user; all keys: administrator |
| Revoke another user's key | Username | 200 with a confirmation message; 404 unknown user | administrator |

The key is returned in clear on every read, unlike tokens (TOK-006). The priority reflects that
trove's own tokens (INV-8) cover the use case.

## Security settings

Login protection, password rules, secret encryption, signing keys and trusted certificates.
Feature semantics: IAM-035 to IAM-049 in [identity-and-access.md](identity-and-access.md).

### ADMAPI-050 · Account lockout policy and locked users
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get lockout policy | none | Enabled flag; number of failed login attempts that locks an account | administrator |
| Set lockout policy | Enabled flag; failed-attempt threshold | 200 with a plain-text confirmation; 400 invalid policy | administrator |
| List locked-out users | none | Array of usernames locked by repeated failed logins (only meaningful while the policy is enabled) | administrator |
| Unlock listed users | Array of usernames | 200 with a plain-text confirmation; 400 | administrator |
| Unlock all users | none | 200 with a plain-text confirmation | administrator |

Example values: enabled, five attempts; the default values are unspecified.
Single-user unlock in the current interface: ADMAPI-012. Interactions: IAM-042, EVT-008
(lockout event).

UI: user-management settings and users list (IAM-042).

### ADMAPI-051 · Password expiration policy and bulk expiry
Priority: **P2**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get password expiration policy | none | Enabled flag; maximum password age (unit unspecified in the operation; IAM-041); notify-by-email flag (the older interface returns all three as strings) | administrator |
| Expire passwords of listed users | Array of usernames | 200 with a status and message | administrator |
| Unexpire one user's password | Username | 200 with a plain-text confirmation; 404 | administrator |

Example values: maximum age 60, email notification on; defaults are unspecified. The
single-user unexpire operation is deprecated and is removed together with its screen once the
older interface generation is retired. The current interface's single-user and
all-user operations are in ADMAPI-011. Interactions: IAM-041.

UI: user-management settings, password expiration policy (IAM-041).

### ADMAPI-052 · Encrypted client password
Priority: **P2**

Returns an encrypted form of the caller's own password, for use in client configuration files
instead of the clear password (IAM-035). The call must be authenticated with the clear-text
password; when it is authenticated with a legacy API key, the encrypted API key is returned
instead. Output: the encrypted value as plain text. Errors: 401, 403. Privilege: "a privileged
user"; what that means is unspecified.

### ADMAPI-053 · Activate and deactivate secret encryption
Priority: **P2**

Two operations of the older interface. Activate creates a new master encryption key and switches key
encryption on; deactivate removes the current key and switches it off (IAM-045 describes what is
encrypted). The decrypted state is temporary: the next configuration change encrypts again. Both
answer 200 with a fixed plain-text completion marker. Privilege: administrator. Interactions:
IAM-045.

### ADMAPI-054 · Password complexity policy
Priority: **P2**

Rules for internal passwords. Two write operations exist: a full replace (fields not sent are
reset to their defaults) and a partial update (fields not sent keep their value). Both answer 204;
400 on invalid input. Privilege: administrator. Whether a read operation exists is unspecified.

| Setting | Meaning | Default |
|---|---|---|
| Minimum uppercase characters | — | unspecified |
| Minimum lowercase characters | — | unspecified |
| Minimum digits | — | unspecified |
| Minimum special characters | — | unspecified |
| Minimum length | — | unspecified |
| Must differ from the previous password | — | unspecified |

Interactions: AUTHN-003.

### ADMAPI-055 · Basic-authentication switch
Priority: **P1**

Read and set whether HTTP Basic authentication is accepted at all. Read output: enabled flag.
Write input: enabled flag (required); output 200; 400. Privilege: administrator. Note for trove:
INV-8 accepts tokens inside Basic credentials for clients that speak only Basic (NuGet), so
disabling Basic must not cut those clients off. Interactions: IAM-036, AUTHN-003, TOK-034.

### ADMAPI-056 · Trust-root certificates of the token issuer
Priority: **P2**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get root certificate | Optional "formatted" switch (default off; on returns PEM with header, footer and line breaks) | The certificate as plain text | unspecified (only 401 is specified) |
| Get CA public certificate | none | The certificate as plain text | administrator |

Interactions: TOK-036 to TOK-039 (token signing and cross-instance trust).

### ADMAPI-057 · External secret-manager connections
Priority: **P2**

Connections to a HashiCorp Vault server from which signing keys can be read (IAM-047). Requires
TLS on the system.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create or update connection | Connection key (path); connection record | 200 with the record; 400; 415 non-JSON body | administrator |
| List connections | none | All records | administrator |
| Test connection | Connection record | 204 on success; 400; 415; 500 | administrator |
| Delete connection | Connection key | 204; 404 | administrator |

<details>
<summary>Connection record</summary>

| Field (descriptive) | Meaning |
|---|---|
| Key | Identifier of the connection |
| Type | Secret-manager type (required) |
| Server URL | Vault address |
| Authentication | One of: AppRole (role ID, secret ID); TLS certificate (client certificate and private key in PEM); agent (no further fields) |
| Mounts | Secret-engine mounts, each with a path and a key-value engine version (version 1 or 2) |

</details>

Security: list and create responses carry the AppRole secret and the client private key; an
implementation should mask them. Signing key pairs reference a Vault key and
path (ADMAPI-059).

UI: security, Vault connectors list (IAM-047).

### ADMAPI-058 · Default package-signing key (older interface)
Priority: **P2**

One system-wide default GPG key pair used to sign Debian, RPM and opkg (ipk) packages; clients
verify with the public key.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Set public key | Armored public key as the request body | 200 with a confirmation; 400 invalid key | administrator |
| Get public key | none | The public key as plain text; 404 if none is set | any authenticated user, or anonymous when anonymous access is enabled |
| Set private key | Armored private key as the request body | 200; 400 | administrator |
| Set passphrase | Passphrase in a request header | 200; 400 | administrator |

Compare the named key pairs of ADMAPI-059 and their per-repository assignment in
ADMAPI-060.
Interactions: IAM-046.

UI: security, keys management, signing keys tab (IAM-046).

### ADMAPI-059 · Signing key pairs
Priority: **P2**

Named signing key pairs, RSA or GPG (IAM-046).

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create or replace key pair | Key-pair record | 201 with an empty body; 400 | administrator |
| Update key pair | Key-pair record | 200 with alias, name, public key, type; 400 | administrator |
| List key pairs | none | Alias, name, public key and type of each pair | administrator |
| Get key pair | Pair name | Alias, name, public key, type; 404 | any authenticated user, or anonymous when anonymous access is enabled |
| Delete key pair | Pair name | 200 with a plain-text confirmation; 404 | administrator |
| Verify key pair | Key-pair record | 200 with a plain-text verdict; 400 when verification fails | administrator |

<details>
<summary>Key-pair record</summary>

| Field (descriptive) | Meaning | Required |
|---|---|---|
| Pair name | Unique name | yes |
| Pair type | RSA or GPG | yes |
| Alias | Alias of the pair | yes |
| Private key | PEM | yes |
| Public key | PEM | yes |
| Passphrase | Passphrase of the private key | no |
| Vault key | Secret-manager connection to read the key from (ADMAPI-057) | no |
| Vault public key | Path and key of the public key in the secret manager | no |

</details>

Read operations return no private key.

UI: security, keys management, signing keys tab (IAM-046).

### ADMAPI-060 · Key-pair assignment to repositories
Priority: **P2**

A repository may hold a primary and a secondary signing key; the secondary is the fallback when
the primary is removed and can be promoted.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Assign keys | Repository key; key name as plain-text body | 200; 400 invalid key name | administrator |
| Set primary key | Repository key; key name | 200; 400 | administrator |
| Set secondary key | Repository key; key name | 200; 400 | administrator |
| Promote secondary to primary | Repository key | 200; 400 when no secondary exists; 404 | administrator |
| Remove a key | Repository key, key name | 200; 404 | administrator |
| Delete primary key / delete secondary key | Repository key | 200; 404 when none is set | administrator |
| Download primary / secondary public key | Repository key | Armored public key as plain text; 404 when none is set | administrator |
| Public key of a repository | Repository key | The public key as a plain-text attachment with a file name; 404 | any authenticated user, or anonymous when anonymous access is enabled |

Interactions:
REPO-042, [formats/README.md](formats/README.md).

### ADMAPI-061 · Trusted public keys of other services
Priority: **Later**

Uploads the public GPG key of a distribution service under an alias, so that this service may
upload release sets to this instance (IAM-049). Input: alias and armored public key (both required). Output:
key ID, fingerprint, the key, issue time, issuer and validity end. Privilege: administrator.

UI: security, keys management, public keys tab (IAM-049).

### ADMAPI-062 · Installed TLS certificates
Priority: **P2**

Installed TLS certificates, addressed by alias. What the certificates are used for is
unspecified; the related capability is client certificates for upstream connections.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List certificates | none | Per certificate: alias, issued to, issued by, issued on, valid until, fingerprint | administrator |
| Add certificate | Alias; PEM content as body | 200 with a status and messages; 400 invalid format | administrator |
| Delete certificate | Alias | 200 with a status and message; 404 | administrator |

Which PEM parts (certificate chain, private key) the body must contain is unspecified.
Interactions: [remote-repositories.md](remote-repositories.md).

## Projects and environments

A project is a named tenant that owns repositories and other resources, has members (users and
groups) with project roles, and may have its own environments. Projects are addressed by project
key. Capability descriptions: REPO-013 to REPO-015 and REPO-045 in
[repositories.md](repositories.md), TOK-011 and TOK-013 in [access-tokens.md](access-tokens.md).

### ADMAPI-063 · Project operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create project | Project record (below) | 201 with the record; 400; 409 key exists | administrator |
| List projects | none | Project records the caller administers (all projects for an administrator) | administrator or project administrator; no fine-grained token scope applies |
| Get project | Project key | The record; 404 | administrator or project administrator |
| Update project | Project key; display name, description, administrator switches, storage quota (all optional) | 200 with the record; 400; 404 | administrator |
| Delete project | Project key | 204; 404 | administrator |
| Projects using a global role | Global role name (required); page size (default 1000, minimum 1); descending order by name; cursor | Entries of project display name and project key, plus a cursor; 404 | administrator |

<details>
<summary>Project record</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Project key | Unique key (required) | — |
| Display name | Human-readable name (required) | — |
| Description | Free text | unspecified |
| Project administrator may manage members | Add, change and remove members and roles | on |
| Project administrator may manage resources (coarse switch) | Older switch covering create and delete of local and virtual repositories | on |
| Project administrator may index resources | Allow indexing of the project's resources (meaning otherwise unspecified) | on |
| Project administrator may manage remote repositories (coarse switch) | Older switch covering create and delete of remote repositories | on |
| Create / delete local repositories | Fine-grained switch | on, see rule below |
| Create / delete remote repositories | Fine-grained switch | on, see rule below |
| Create / delete virtual repositories | Fine-grained switch | on, see rule below |
| Storage quota in bytes | Upper bound for the project's stored bytes | unspecified |
| Soft limit (output) | Whether the quota is a soft limit | unspecified |
| Quota email notification (output) | Whether exceeding the quota sends email | unspecified |

</details>

Rule for the administrator switches: if none of the six fine-grained repository switches is sent,
the two coarse switches decide them (resources → local and virtual, remote → remote). As soon as
one fine-grained switch is present, the coarse switches are ignored. A fine-grained switch that is
not sent defaults to on when the project is created and keeps its current value on update.

Interactions: REPO-013; storage quotas in [storage-and-blobs.md](storage-and-blobs.md).

### ADMAPI-064 · Project user membership operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List project users | Project key | Members, each with username and role names; 404 project unknown | administrator, project administrator with member management, or the project's users-read scope |
| Get project user | Project key, username | Username and roles; 404 | administrator or project administrator with member management |
| Add or update project user | Project key, username; role names | 200 with username and roles; 400; 404 | administrator or project administrator with member management |
| Remove project user | Project key, username | 204; 404 | administrator or project administrator with member management |

Add and update are one operation keyed by username; whether the sent role list replaces or extends
the user's existing roles is unspecified. Interactions: TOK-013.

### ADMAPI-065 · Project group membership operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List project groups | Project key | Members, each with group name and role names; 404 | administrator, project administrator with member management, or the project's groups-read scope |
| Get project group | Project key, group name | Group name and roles; 404 | administrator or project administrator with member management |
| Add or update project group | Project key, group name; role names | 200 with group name and roles; 400; 404 | administrator or project administrator with member management |
| Remove project group | Project key, group name | 204; 404 | administrator or project administrator with member management |

### ADMAPI-066 · Project role operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List roles of a project | Project key | Roles: name, description, type (predefined, project custom, global custom), environments, actions; 404 | administrator, project administrator, or the project's roles-read scope |
| Create project role | Project key; name (required), description, type (required), environments, actions | 201 with the role; 400; 404 | administrator or project administrator with member management |
| Get project role | Project key, role name | The role; 404 | administrator or project administrator |
| Update project role | Project key, role name; type (required; must be project custom, as this operation edits custom roles only), description, environments, actions | 200 with the role; 400; 404 | administrator or project administrator with member management |
| Delete project role | Project key, role name | 204; 404 | administrator or project administrator with member management |

A project's role list can contain global custom roles (ADMAPI-019), marked with the global
custom type; whether every global role is listed in every project is unspecified (ADMAPI-063
offers a separate lookup of the projects that use a given global role).
Interactions: TOK-011, ADMAPI-030.

### ADMAPI-067 · Repository assignment and sharing operations
Priority: **P1**

Assigning gives a project ownership of a repository; sharing grants another project's members
access according to their roles there, without ownership.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Assign or move repository | Repository key, target project key; optional force switch (needed when the repository already belongs to another project) | 204; 400; 404 | administrator |
| Unassign repository | Repository key | 204; 404 | administrator |
| Repository project status | Repository key | Repository key, owning project, environments, projects it is shared with, shared-with-all flag, shared-read-only flag; 404 | administrator |
| Share with all projects | Repository key (local or remote) | 204; 400 (for example the repository does not exist); 404 | administrator |
| Share with one project | Repository key, target project key; optional read-only switch | 204; 400; 404 | administrator |
| Unshare from all projects | Repository key | 204; 404 | administrator |
| Unshare from one project | Repository key, target project key | 204; 404 | administrator |

Unsharing never deletes the repository; it stays assigned to its owning project. A read-only
share prevents the target project from modifying content. Interactions: REPO-013, REPO-014,
REPO-015 (priority of sharing there: P2).

### ADMAPI-068 · Environment operations
Priority: **P1**

Environments are named labels attached to repositories and referenced by roles. They exist
globally and per project. Environments are superseded by lifecycle stages (ADMAPI-111) but still
supported.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create global environment | Name | 201 with the name; 400; 409 exists | administrator |
| List global environments | none | Names | administrator or project administrator |
| Rename global environment | Current name; new name (required) | 200 with the new name; 400; 404 | administrator |
| Delete global environment | Name | 204; 404 | administrator |
| Create project environment | Project key; name | 201; 400; 404 project unknown; 409 exists | administrator or project administrator |
| List project environments | Project key | Names; 404 | administrator or project administrator |
| Rename project environment | Project key, current name; new name (required) | 200 with the new name; 400; 404 | administrator or project administrator |
| Delete project environment | Project key, name | 204; 404 | administrator or project administrator |

Limits: names at most 32 characters, case-insensitive, unique within their scope (global names
unique across the whole system, project names within the project). At most 10 global and 10
environments per project by default; the limit is a server setting; at most 30 is recommended.

## System and configuration

Health, identity of the instance, the central configuration document and maintenance tasks.
Capability descriptions: [operations-and-monitoring.md](operations-and-monitoring.md) and
[ha-and-deployment.md](ha-and-deployment.md).

### ADMAPI-069 · Health probes
Priority: **MVP**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Ping (main service) | none | 200 with a short plain-text body when the service works; 500 otherwise | none (anonymous allowed) |
| Readiness | none | 200 with a status-code object ("OK") when ready; 500 with a reason otherwise | none |
| Liveness | none | 200 with a status-code object when alive; 500 otherwise | none |
| Ping (identity service) | none | 200 with plain text | none |

The readiness probe follows Kubernetes probe semantics and is suggested as a latency metric. Each
auxiliary service (hook service ADMAPI-095, tunnel client ADMAPI-105, entitlement
connector ADMAPI-106, central console ADMAPI-102) has its own probes. For trove the
probes live on the operations listener (INV-6). Interactions:
[operations-and-monitoring.md](operations-and-monitoring.md).

### ADMAPI-070 · Version and system information
Priority: **MVP**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Version | none | Version string, revision, list of installed add-ons | any caller, anonymous allowed |
| System information | none | General system information as plain text (content unspecified) | administrator |
| Service ID | none | The instance's service identifier as plain text (a service-type prefix, "@", and a unique ID) | administrator |

The version operation is reachable anonymously. The service ID is the identifier that token
audiences name (TOK-016).

### ADMAPI-071 · External base URL
Priority: **MVP**

Sets the custom base URL, the externally visible address of the system. Input: the URL as a
plain-text body. Output: 200 with plain text; 400 invalid URL. Privilege: administrator.
Self-hosted only. Whether a dedicated read operation exists is unspecified. Interactions:
AUTHN-002 (single sign-on requires it); in trove the base URL also drives the metadata URL
rewriting of INV-11.

### ADMAPI-072 · Read and replace the whole configuration document
Priority: **P1**

The system keeps its general configuration in one document.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get configuration | none | The complete configuration document (for example XML) | administrator |
| Save configuration | The complete document | 200; 400 invalid document | administrator |

Saving replaces everything; it is an advanced feature, and the document should be verified
first. Self-hosted only. Interactions: configuration as code in
[operations-and-monitoring.md](operations-and-monitoring.md); INV-15.

### ADMAPI-073 · Apply partial configuration changes from YAML
Priority: **P1**

Applies a YAML document that creates, updates or deletes any elements of the configuration in one
call; only the elements in the document change. Input: YAML body. Output: 200 with a plain-text
message; 400. Privilege: administrator. Self-hosted only. A careless patch can overwrite critical
values (for example a repository's package type), so a backup of the configuration first is
recommended. This is the declarative counterpart to the per-resource operations.
Interactions: configuration as code (P1 in the rubric).

### ADMAPI-074 · Verify an outbound connection
Priority: **P2**

The system calls a given URL and reports whether it answered with 200. Inputs: endpoint URL
(required); optional username and password. Output: 200 with plain text when the endpoint answered
200; 400 when the connection failed. Privilege: administrator. Self-hosted only. Security: this is
a server-side request to an arbitrary URL; an implementation should apply the same private-network
protection as for webhooks (EVT-029).

### ADMAPI-075 · Reverse-proxy configuration generator
Priority: **P2**

Stores the parameters of a fronting web server and renders a configuration snippet for it.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get reverse-proxy settings | none | The settings below | administrator; other users receive only the routing method, ports and the repositories they may see |
| Update reverse-proxy settings | The settings | 200; 400 | administrator |
| Get configuration snippet | none | Plain-text web-server configuration (for example nginx) | administrator |

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Key | Name of this configuration | unspecified |
| Web server type | nginx or Apache HTTP Server | unspecified |
| Internal application context path | Path the service listens on behind the proxy | unspecified |
| Public application context path | Path exposed to clients | unspecified |
| Server name | Public host name | unspecified |
| Server name expression | Wildcard host pattern (for per-repository subdomains) | unspecified |
| Internal server name and port | Where the proxy forwards to | unspecified |
| TLS certificate and key paths | Files on the proxy host | unspecified |
| Container registry routing method | Subdomain per repository, repository path prefix, or port per repository | unspecified |
| Use HTTPS / use HTTP | Which listeners to render | unspecified |
| HTTPS port / HTTP port | Listener ports | unspecified |

</details>

Self-hosted only. In a Kubernetes deployment the Ingress or Gateway route of the Helm chart
(INV-13) plays this role. Interactions: [repositories.md](repositories.md) (container registry
addressing).

### ADMAPI-076 · SHA-256 migration task
Priority: **Later**

Starts and stops a background SHA-256 migration task over existing stored data; what it does
beyond its name is unspecified. Start inputs (all optional): batch threshold (default 10),
query limit (default 100), wait interval for the cluster in milliseconds (default 5000), sleep interval in
milliseconds (default 5000), and a node ID to force the task onto. Stop input: the sleep interval
(default 5000), which should match the start value. Outputs: 200 with plain text; start answers
400 where the environment does not support the task. The same migration can alternatively be
driven by server properties. Privilege: administrator. Self-hosted only. trove computes SHA-256 on every upload (INV-4), so it has no
such legacy data; the entry is kept for completeness.

### ADMAPI-077 · Re-index paths in the package metadata service
Priority: **P2**

Re-sends metadata events for repositories or directories to the separate package metadata service
when it is out of sync, recursively under each given path. Inputs: list of repository keys or
directory paths (required); asynchronous switch (default on); delete-first switch (default off:
existing data for the paths is not removed before re-indexing). Outputs: 200, or 202 when accepted
asynchronously; 400 invalid paths. Privilege: administrator. A plain-text accept header is
rejected with 406. Interactions: REPO-009 (recalculate repository index).

### ADMAPI-078 · Cluster lock diagnostics
Priority: **P1**

Lists the database advisory locks held across the cluster, to diagnose stuck or long-held locks
without database access.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Cluster locks | Optional category filter (exact match) | Snapshot metadata (query time, responding node, whether clustering is on, number of peers that responded and that were unreachable), summary (total locks, nodes reporting, nodes unreachable), warnings (code and message, omitted when empty), and per node: node ID, status (reachable or not), locks (category, key such as a repository name, held-since time in epoch milliseconds and as a timestamp, holding thread ID, holding thread name) | administrator |
| Node locks | Optional category filter | The same per-node block for the receiving node only, without fan-out | administrator |

Behavior: the receiving node reports its own locks and, in a cluster, asks the other running
members; a member that does not answer is reported unreachable with an empty lock list. The result
is a best-effort, non-atomic snapshot. Stopped or stale members are left out rather than marked
unreachable, so counts can be low during a rolling restart. Unknown held-since times are reported
as 0 (epoch start) and an unknown thread ID as 0; the thread name is empty when the reporting
node's version does not supply it, which does not mean the lock is free. A held-since time resets
when the node's database session is renewed. Because the locks belong to database sessions, they
vanish on their own when a node dies. In one clustered authentication mode the fan-out is skipped
and a warning says so, while the clustering flag stays on and zero peers are counted. Locks of the
older table-based mechanism that still synchronizes background jobs never appear here. Without
native advisory locking the operations answer 503. The node operation mainly serves the fan-out
but may be called directly. Self-hosted only.

This maps directly onto trove's coordination model (INV-12: PostgreSQL advisory-lock leases for
singleton schedulers and pull-through collapse). Interactions: [ha-and-deployment.md](ha-and-deployment.md).

### ADMAPI-079 · Upstream curation availability setting
Priority: **Later**

Read and set one switch: whether package downloads are allowed while the curation service is
unavailable (fail open) or blocked (fail closed). Read output and write input: that flag. Write
output: 200; 400. Privilege: administrator. Default unspecified. Interactions:
[security-scanning-and-curation.md](security-scanning-and-curation.md).

## Diagnostics, backup and migration

### ADMAPI-080 · Temporary logger levels
Priority: **P1**

Raises the log level of named loggers for a limited time without editing the logging
configuration; afterwards the previous levels return by themselves.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Set temporary levels | Map of logger name to level (required; at most 100 loggers); duration in seconds (required) | 200 with a confirmation string; 400 when no logger is given, more than 100 are given, or the duration is negative | administrator |
| Reset levels | none | 200 with a confirmation string; the previous levels are restored at once | administrator |
| Get level of one logger | Logger name | The current level as a string; for an unknown name, the root logger's default level | administrator |

Behavior and edge cases:

- In a cluster the set and reset operations propagate to every node; the read returns the level
  of whichever node answers.
- A second set while a timer is still running is not allowed: reset first, then set again.
- Unknown logger names do not fail the set operation; to correct a typo, reset (or wait for the
  timer) and set again.
- Self-hosted only.

For trove (INV-16, `log/slog`) the logger names map to component names. Interactions:
[operations-and-monitoring.md](operations-and-monitoring.md).

### ADMAPI-081 · Support bundles
Priority: **P1**

A support bundle is an archive of diagnostic data collected on demand.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create bundle | All optional: name, description, collection parameters (below) | 200 with the bundle ID and, per service, its service ID and a URL where the bundle is stored; 400 when not supported in the environment | administrator |
| List bundles | none | Bundles (ID, name, description, creation time, status) and a count | administrator |
| Get bundle metadata | Bundle ID | Name, description, ID, storage location, the collection parameters used, creation time, status; 404 | administrator |
| Download bundle | Bundle ID | The archive as a binary stream; 400 when the download fails; 404 | administrator |
| Delete bundle | Bundle ID | 204; 404 | administrator |

<details>
<summary>Collection parameters</summary>

| Parameter | Meaning | Default |
|---|---|---|
| Configuration | Include configuration files | on |
| System information | Include storage, system properties, CPU and runtime information | on |
| Slow database queries | Include the 10 slowest database queries (PostgreSQL and other supported databases) | off |
| Logs | Include logs | on |
| Log start date | First day of logs (YYYY-MM-DD) | the day before the end date |
| Log end date | Last day of logs (YYYY-MM-DD) | today |
| Thread dump count | Number of thread dumps | 1 |
| Thread dump interval | Milliseconds between thread dumps | 0 |

</details>

Self-hosted only. The bundle is stored inside the system itself, in a dedicated internal location
reachable by URL. The network tunnel client has its own bundle operation that collects from all
services and allows only one bundle at a time (409 otherwise) (ADMAPI-105). For trove the
thread-dump parameters correspond to Go goroutine dumps. Interactions:
[operations-and-monitoring.md](operations-and-monitoring.md).

### ADMAPI-082 · Support bundles of the distribution service
Priority: **Later**

The distribution service (release sets to edge instances, [distribution-and-lifecycle.md](distribution-and-lifecycle.md))
offers the same bundle operations with the same collection parameters except the slow-query
switch: create with a generated ID, create
with a caller-chosen ID, read metadata, download as ZIP, delete, list (count plus ID, description,
name, creation date). Creating with a chosen ID answers 400 when the storage repository is missing
(the bundle is then kept on each edge instance's file system) and 404 when the authentication
provider cannot be found. Privilege: administrator.

### ADMAPI-083 · System and repository import and export
Priority: **P2**

File-system based migration and backup of the whole system or single repositories, on the
server's local disk.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Import repository content | Server path (a single repository's folder, or the parent folder holding all repositories); optional target repository (empty means all); include metadata (default on); verbose (default off) | 200 with a plain-text import log | administrator |
| Default import settings | none | Template of the system import settings | administrator |
| Full system import | Import path (required); include metadata; verbose; fail on error; fail if empty | 200 with a plain-text log; 406 unsupported content type | administrator |
| Default export settings | none | Template of the system export settings; 400 where not supported | administrator |
| Full system export | Export path (required); include metadata; create archive; bypass filtering; verbose; fail on error; fail if empty; Maven 2 layout; incremental; exclude content | 200 with a plain-text log | administrator |
| Export one repository with metadata | Repository key (required); target server path (required); exclude metadata (default off); Maven 2 layout; verbose | 200 with a plain-text log; writes a data directory and a metadata directory | administrator |

The template values are: import — include metadata on, verbose off, fail on error on, fail if
empty on; export — include metadata on, create archive off, bypass filtering off, verbose off,
fail on error on, fail if empty on, Maven 2 off, incremental off, exclude content off. A system
import maintains security entities (users, groups, permissions, tokens); whether existing entities
are kept or overwritten is unspecified. The
single-repository export does not use the directory structure the repository import expects.
trove backups rely on PostgreSQL and S3 (INV-3, INV-4); a file-system export is a migration
aid. Interactions: [operations-and-monitoring.md](operations-and-monitoring.md).

### ADMAPI-084 · Identity configuration export and import
Priority: **P1**

Backup of the identity service's configuration (the service that holds users, groups, permissions
and tokens), separately from content.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Export | none | 202; the export is written asynchronously as a timestamped JSON file into a backup folder on the server | administrator |
| Import | none (the file must be placed at a fixed bootstrap location on the server beforehand) | 200; 400 | administrator |

Self-hosted only. The import reads only that one file at that one location. Which entities the
export contains is unspecified. Interactions: IAM-010 (bootstrap and
recovery).

## Retention, cleanup and archive policies

Policy semantics (selectors, conditions, schedules, dry runs, reports) are described in
[retention-and-cleanup.md](retention-and-cleanup.md). The entries below pin the operations. All
policies are addressed by a policy key of at least three characters (letters, digits, underscore,
hyphen); the key appears both in the path and in the body.

### ADMAPI-085 · Package cleanup policy operations
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create policy | Policy key; policy record | 201 with the record; 400; 409 key exists | unspecified, see below |
| Update policy | Policy key; policy record | 200 with the record; 404; rejected while the policy runs | as create |
| Get policy | Policy key; optional switch to check repository availability (default off) | The record; 404 | as create |
| List policies | Project key (optional for administrators, who otherwise get global and project policies; mandatory for project administrators) | Records | as create |
| Delete policy | Policy key | 200; 404; rejected while the policy runs | as create |
| Enable or disable | Policy key; enabled flag (required) | 200; 404 | as create |
| Run now | Policy key; dry-run switch (default off) | 202 with a trigger ID used by the run operations (ADMAPI-088); 404 | as create |

<details>
<summary>Policy record</summary>

| Field (descriptive) | Meaning | Default / limit |
|---|---|---|
| Key | Policy key (required) | at least 3 characters |
| Description | Free text | at most 256 characters |
| Schedule | Cron expression; empty means manual runs only | empty |
| Project key | Set only for project-level policies | — |
| Item type | Package | — |
| Maximum duration | Minutes after which a run stops even if incomplete | unspecified |
| Enabled | Must be off (or omitted) on create; on is rejected | off |
| Skip trash can | Delete permanently instead of moving to the trash can | unspecified |
| Package types | At least one (required) | — |
| Repositories | One or more repository name patterns or names (required) | — |
| Excluded repositories | Patterns or names | none |
| Included packages | Package name pattern or name (required); all packages: `**` | — |
| Excluded packages | Explicit names only, no patterns | none |
| Include all projects | Global level only | off |
| Included projects | Project keys; the key of the default (no project) scope includes unassigned repositories | — |
| Include all stages / included stages | Lifecycle stages or environments to cover; with "all" on the list must be empty, otherwise at least one stage is needed | unspecified |
| Created before (days) | Age since creation; a day is 24 hours counted from the run start; an older months-based variant exists | — |
| Last downloaded before (days) | Age since last download; an older months-based variant exists | — |
| Keep last N versions | Versions to keep; the newest version is never removed | — |
| Included properties / excluded properties | Key-value pairs on the package's lead file | — |
| Included versions / excluded versions | Explicit versions or patterns with a leading or trailing wildcard | — |

</details>

Privileges: the cleanup operations name no required privilege; they specify 401 and 403 only.
A project administrator must pass a project key when listing, and the include-all-projects switch
is a global-level setting for administrators. Anything further
about project administrators' rights on cleanup policies is unspecified. A dry run is possible
while the policy is disabled. Interactions: RET-001 to
RET-022.

UI: administration area, retention policies page (RET-001).

### ADMAPI-086 · Build cleanup policy operations
Priority: **P1**

The same operation set as ADMAPI-085 (create inactive, update, get, list with project
key, delete, enable or disable, run now with dry run → trigger ID). Record differences: item type
"build" (required); include all projects (required) and included projects (required); included
builds, each with a build-name pattern (trailing wildcard) and a project key (empty for global);
exclude builds that are part of a release set; created-before age in days (older months variant);
delete the builds' artifacts too; skip trash can. Interactions: RET-023.

UI: retention policies page, create policy, build cleanup (RET-023).

### ADMAPI-087 · Release-set cleanup policy operations
Priority: **Later**

The same operation set for release sets. Record differences: item type "release set" (required);
include all projects and included projects (required); release sets to consider, each a name
(`**` for all) and a project key (empty for global); environments whose promoted versions are
protected (required; `**` for all) or an exclude-all-promoted switch; created-before age in days
(older months variant). Interactions: RET-024.

UI: retention policies page (RET-024).

### ADMAPI-088 · Cleanup run control, views and reports
Priority: **P1**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Stop a run | Run ID (the trigger ID); action "stop" (required) | 202; a stopped run cannot be resumed | unspecified |
| Stop all runs | none | 202; stops running package, build and release-set policies at global level | unspecified |
| Disable all policies | Optional project key (otherwise all global-level policies) | 200 | unspecified |
| Policies overview | Item types (required, unique); optional policy key and project key | Per policy: item type, key, next run time, enabled, project display name, included projects, last run (status, time, dry-run flag, run ID, whether the report was downloaded, items processed) | unspecified |
| Runs overview | Optional: item types, time range, policy key, project key or list of project keys, run status list, exclude runs that processed nothing; page, page size, sort field, sort order | Runs (run ID, item type, policy key, project, start and end, total, cleaned items and bytes, status, triggered by, manual flag, dry-run flag, task ID, included projects) and pagination (page size, current page, last page, total) | unspecified |
| Run details | Policy key, trigger ID | One run as in the runs overview; 404 | unspecified |
| Download run report | Policy key, run ID (both required) | CSV report as a binary stream; 404 | unspecified |
| Run summary report | Trigger ID, policy key (both required) | Policy summary (name, run ID, dry run, status, triggered by), execution summary (start, finish, duration), processed items (type, total, processed, failed, physical and virtual bytes), and the configuration used (created and modified by and at, schedule, maximum duration, description, skip trash can, target selectors, cleanup criteria) | administrator, for global and project-level policies |

Run statuses: in progress, in progress with errors, completed, completed with errors, stopped,
stopping, failed, plus three warm-up states (pending warm, partially warmed, warmed) whose meaning
is unspecified. Like the policy operations (ADMAPI-085), the run operations name no required
privilege apart from the summary report, which administrators may read for global and
project-level policies; whether project administrators may use any of them is unspecified for
cleanup (the archive counterparts in ADMAPI-089 state it for reports). Interactions: RET-014 to
RET-021.

UI: retention policies page, policies tab and runs tab (RET-016, RET-020, RET-021).

### ADMAPI-089 · Package archive policy operations
Priority: **Later**

Package archive policies move old package versions to a separate archive instance (RET-027 to
RET-031). The record is the package cleanup record of ADMAPI-085 without stages, item type and the
older months-based ages; the included-packages list takes a single pattern or name; its maximum
duration is capped at 300 minutes; it must be created disabled.

| Operation | Inputs | Outputs |
|---|---|---|
| Create, update (not while running), get, list (project key mandatory for project administrators), delete (not while running), enable or disable | as for cleanup policies | 201, 200, 404, 409 as for cleanup policies |
| Disable all (global) | none | 200 |
| Policies overview | Item type filter (package); optional key substring | Per policy: type, name, enabled, next run, last run (status, time, dry run) |
| Run now | Policy key; dry-run switch (default off) | 200 with a trigger ID |
| Stop a run / stop all runs | Run ID with action "stop" / none | 202 |
| Download run report / run summary report | Policy key and run ID / trigger ID | CSV stream / summary as in ADMAPI-088 |
| Restore archived packages | Optional project key; manifest paths (required); restore to the original repository, or to a new destination path (one of the two is required); fallback destination (at least a repository key) when the original no longer exists | 200 with a run summary ID and a message |

Privileges: administrators for everything; project administrators must pass their project key when
listing and may read run reports and summaries of their projects' policies.

UI: retention policies page, archive section (RET-027).

### ADMAPI-090 · Older artifact-level archive operations
Priority: **Later**

The previous archive generation (RET-034): policies select artifacts by query or search form, or
packages by a package search form, and move them to the archive instance.

| Operation | Inputs | Outputs |
|---|---|---|
| Create, update, get, list, delete policy | Policy record: key, enabled, cron schedule (required), description, package mode switch (default off), a query-language expression or a search form (one is required; the query is not allowed in package mode), a package search form (package type, name and version with wildcards, repositories, created-before and downloaded-before in epoch milliseconds), maximum duration in minutes, months until archived items are purged from the archive instance, skip trash can (default off) | The record plus the last run status (state, artifact counts, progress, start and finish, byte counts, triggered by) and the next execution time; create 201, 409 on duplicates; delete 200 with a text message |
| Run | Policy key | 202, runs in the background |
| Stop | Policy key | 202 |
| Preview by key / by record | Policy key / policy record | Estimated candidate count and total size in bytes |
| Convert search form to query | Search form as text | The equivalent query-language expression |
| Archive namespace | none | The namespace ID generated for the archived repositories; needed for restores |
| Run status / restore status | Optional time range (epoch); optional policy filter for run status | Per run: policy key, state (pending, in progress, in progress with errors, complete, complete with errors, failed), counts, progress, times, byte counts, destination and destination mode for restores |
| Search archived artifacts | Query-language expression | Results and a range (start, end, total) |
| Search archived packages | Package type, repositories, name pattern (all required); version; archive time range; optional project key | Results (manifest name, type, name, version, original repository, archive time) and a range |
| Restore | Restore type (package or artifact; default artifact); restore to original location (with a required fallback path) or to a central destination repository keeping the original path hierarchy; namespace switch (default on: items are given without the namespace); items (repository paths; folders end with "/") | 202 with a message and a restore operation ID |

A restore copies items back to the main instance; the archive instance keeps its copy.
Privilege: administrator.

## Scripting hooks

Server-side scripts that run on system events, on a schedule or on an HTTP call. The capability
is described in [events-and-extensibility.md](events-and-extensibility.md) (EVT-038 onward); the
entries below pin its management operations. The hook service runs as its own component with its
own health probes.

### ADMAPI-091 · Hook definition operations
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Create hook | Hook record (below); project key as a query input for project administrators, which must match the project in the body | 201; 400 | administrator, or project administrator for their project |
| List hooks | none | All hook records | administrator |
| Get hook | Hook key | The record; 404 | administrator |
| Update hook | Hook record (the key is part of the body) | 200; 400; 404 | administrator |
| Delete hook | Hook key | 204; 404 | administrator |

<details>
<summary>Hook record</summary>

| Field (descriptive) | Meaning | Default |
|---|---|---|
| Key | Unique hook key (required) | — |
| Description | Free text | unspecified |
| Enabled | Whether the hook runs (required on create) | on |
| Source code | TypeScript or JavaScript, as plain text or as Base64 with a fixed prefix marker; the UI always sends Base64 (required) | — |
| Shared | Lets users who are neither system nor project administrators execute the hook where the event kind allows it; HTTP-triggered hooks support it | unspecified |
| Debug | Record successful runs in the troubleshooting view; failed runs are always recorded | unspecified |
| Event kind | The event the hook is bound to (required); see ADMAPI-092 | — |
| Filter criteria | Which events trigger the hook; shape depends on the event kind (ADMAPI-094) | — |
| Filter: repository keys | Repositories whose events trigger the hook; at least one unless an "any" switch is on | — |
| Filter: include path patterns | Paths that trigger; empty includes all | empty list |
| Filter: exclude path patterns | Paths that never trigger; empty excludes none | empty list |
| Filter: any local / any remote / any federated repository | Match all repositories of that class; combined with the key list by OR | off |

</details>

When the event kind's metadata marks a filter as mandatory, the filter of the advertised shape
must be present. Interactions: EVT-040, EVT-041.

UI: hook management UI (EVT-061).

### ADMAPI-092 · Hook event kinds
Priority: **Later**

The closed list of event kinds a hook may bind to:

| Group | Event kinds (descriptive) |
|---|---|
| Token lifecycle | before token creation; before token revocation; before token expiry |
| Content (pre-operation) | before download request; before download; before upload request; before upload; before create; before delete; before move; before copy; before property create; before property delete; before build-information save |
| Content (post-operation) | after download; after download error; after create; after delete; after move; after copy; after save; after property create; after property delete; after build-information save |
| Remote (pull-through) flow | before remote information request; before remote download; after remote download; alternate remote path; alternate remote content |
| Response rewriting | alternate response; alternate response for all requests |
| Replication | before file, directory, delete, property and statistics replication |
| Repository configuration | before and after repository create, update and delete (listed by the create operation, absent from the separate event list) |
| Runtime | after workload state change (the create operation names it as a state-change request) |
| HTTP-triggered | generic event (executed by ADMAPI-093) |

Scheduled execution is also an event kind, used with a schedule filter, yet it is missing from
both enumerations (create operation and event list); whether it belongs in the closed list is
open. Interactions: EVT-039, EVT-043 to EVT-052.

### ADMAPI-093 · Execute, test and rerun hooks
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Execute an HTTP-triggered hook | Hook key; JSON payload whose shape depends on the event kind | 200 with the value the script returned and an execution status (unspecified, failed, succeeded); 404 | administrator, project administrator of the hook's project, or any authenticated user when the hook is shared |
| Test a stored hook | Hook key; event payload | 200 with the test result; 404 | administrator |
| Test without a hook key | Event payload only; how the code under test is supplied is unspecified | 200 with the test result | administrator |
| Rerun a past execution | Hook key; ID of the execution-history entry; optional "force record" switch (default off) | 200; 404 | as for execute |
| Execution history | Optional: maximum number of entries; start and end timestamps (Unix) bounding the execution start | History entries (shape unspecified) | administrator |

Interactions: EVT-051, EVT-056, EVT-057.

UI: hook management UI, run history (EVT-057).

### ADMAPI-094 · Hook discovery metadata
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List available event kinds | none | Event-kind names | administrator |
| Event-kind metadata | Project key (required for project administrators; filters to kinds usable in projects) | Per kind: owning application and name, description, sample payload (JSON string), sample code, type definitions (TypeScript interfaces of request and response), whether project administrators may use it, filter shape, whether a filter is mandatory, documentation link, whether it runs asynchronously, a JSON schema of the request when available, the request type name | administrator or project administrator |
| Script compiler settings | Project key (required for project administrators) | The TypeScript compiler options, include globs and exclude paths used to transpile hook code | administrator or project administrator |

Clients build execution payloads from the sample payload and type definitions. Interactions:
EVT-058, EVT-062.

### ADMAPI-095 · Hook service health probes
Priority: **Later**

Unauthenticated liveness and readiness probes of the hook service; each answers 200 with a plain
text body when healthy. Interactions: ADMAPI-069.

## Multi-site management

Operations of a central console that registers several instances, groups them into topologies,
federates identities and curation policies between them and monitors synchronization. Capability
descriptions: [replication-and-federation.md](replication-and-federation.md). Trust between
instances starts with a pairing token (ADMAPI-048) created on the instance that joins.

### ADMAPI-096 · Central instance registry
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List registered instances | Optional filters: name, tag, URL, status (online, offline, partial, unauthorized) | ID, name, URL, status, tags; 400 for an invalid status | administrator |
| List available instances (topology view) | Optional filters with `*` wildcards: name, tag, URL; status | ID, name, public URL, internal base URL for server-to-server calls, whether it is the local instance, status code and message, location (city, country code), license types | administrator |
| Get instance | Instance ID | ID, name, URL, status, tags; 404 | administrator |
| Register instance | Name, URL, location (city name, two-letter ISO 3166-1 country code, latitude, longitude; all required); a pairing token or older join key, or, for older services only, username and password; tags | 201 with ID, name, URL, status, tags; 400 | administrator |
| Update instance | Instance ID; same record | 204; 404 | administrator |
| Remove instance | Instance ID | 204; 404 | administrator |

Registration: create a pairing token on the joining instance for the central-console use case,
pass it here; the console validates it by calling that instance.

### ADMAPI-097 · Topology operations
Priority: **Later**

A topology is a named set of sites (registered instances) between which entities are federated.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List topologies | none | Topologies and their total | administrator |
| Get topology | Topology key | The topology; 404 | administrator |
| Create topology | Name (required; the stable key is derived from it), description, sites (each a site name that must match a registered instance; required) | 201 with the topology whenever it was stored; 400 when validation fails (nothing stored; may list per-site trust or health failures); 409 key exists | administrator |
| Update topology | Topology key; any of name, description, sites | 200 with the topology; 400 (nothing changed); 404 | administrator |
| Delete topology | Topology key | 204; 409 with the current topology when federation cleanup failed (the topology is kept; retry the delete); 404 | administrator |

Topology record: key, name, description, created and modified times, sites (site name, whether
it is the instance serving the request, location, status connected or disconnected, and a status
description explaining a disconnect). Federation set-up runs after the topology is stored, so a
201 or 200 can still carry disconnected sites.

### ADMAPI-098 · Entity synchronization monitoring
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Summary | Topology key; optional modified-after and modified-before times (RFC 3339); include project-scoped entity types (default off) | Per entity type: counts of entities in failure, in progress and success; aggregation time; 400 invalid window; 404 | administrator |
| List entities | Topology key; entity type (required); optional single status filter (failure, success, in progress); opaque page token | Entities (ID, type, optional per-target details), next page token (empty when done), total count; 400 for an unknown type, status or token | administrator |
| Entity detail | Topology key, entity type, entity ID | Per target: source and target instance IDs and site names, status (success, failure, in progress, pending, sent), failure description, last change time; 404 | administrator |

Monitored entity types: users, groups, permissions, tokens, projects, environments, roles, and
the project-scoped types project users, project groups and project resources. Counting rule:
an entity with any failed target counts as failure; otherwise any pending or sent target counts
as in progress; only all-success counts as success.

### ADMAPI-099 · Identity federation targets on one instance
Priority: **Later**

Instance-local management of where users, groups, permissions and tokens are pushed. The central
console variant is ADMAPI-100.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| List targets | none | All target records | administrator |
| Register target | Target record | 201 with the record; 400 (for example a missing name or URL); 409 name exists | administrator |
| Get target | Server name | The record; 400 blank name; 404 | administrator |
| Update target | Server name; partial record (JSON or JSON merge patch) | 200 with the record; 400 when name or URL are changed; 404 | administrator |
| Delete target | Server name | 204; 404 | administrator |
| Validate a target | Target URL | 204 when it can join the trust circle; 400 | administrator |
| Full broadcast | Server name | 204 started; 202 already running; 304 target not eligible; 400; 404 | administrator |

| Target field | Meaning | Default |
|---|---|---|
| Name | Server name of the target (required, immutable) | — |
| URL | Target URL (required, immutable) | — |
| Entities | Which of users, groups, permissions, tokens to federate | all four |
| Permission filters | Include and exclude name patterns for permissions | none |
| Group filters | Include and exclude name patterns for groups | none |
| Active | Whether the target receives updates | on |
| Force override | Replace an existing target with the same name on register | off |

Interactions: TOK-039, TOK-040.

### ADMAPI-100 · Central identity federation configuration
Priority: **Later**

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Get configuration of one instance | Instance ID | Entity types federated from it, and outbound targets (target ID, service code, URL, optional per-target entity override, optional permission include and exclude patterns); 404 | administrator |
| Get configuration of all instances | Include instances without configuration (default off) | Per source instance: the configuration or an error message | administrator |
| Federation candidates | none | Unspecified shape | administrator |
| Configure one instance | Instance ID; entity types and targets (both required) | 200 with per-step results (label, status OK, warning or error, message); 400 with set-up errors; 404 | administrator |

The configuration is a star: one source instance pushes to several targets. The central console
is the recommended way to manage identity federation across instances.

### ADMAPI-101 · Curation policy federation
Priority: **Later**

A controller instance propagates upstream curation policies to follower instances.

| Operation | Inputs | Outputs | Required privilege |
|---|---|---|---|
| Add follower | Follower ID (a unique label), URL, pairing token created on the follower for the cross-instance federation use case | 201 with ID and URL; 400; 409 ID exists | administrator |
| List followers | none | ID and URL of each | administrator |
| Get follower | Follower ID | URL; 404 | administrator |
| Update follower | Follower ID; URL and a new pairing token (both required) — used to rotate the token or move the follower | 204; 400; 404 | administrator |
| Remove follower | Follower ID | 204; 404 | administrator |
| Create federation | Unique name; description; one connection group from the controller to a list of follower IDs; the fixed list of entity types (custom package labels, curation conditions, curation package types, curation policies), which must be sent exactly | 201 with the generated ID and the record; 400 (including an invalid entity type); 409 name exists | administrator |
| List federations | none | ID and name of each | administrator |
| Get federation | Federation ID | The record; 404 | administrator |
| Delete federation | Federation ID | 204; synchronization stops at once; 404 | administrator |

Followers must be added before a federation references them, and must be taken out of every
federation before they are removed. Interactions:
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### ADMAPI-102 · Central console system operations
Priority: **Later**

Unauthenticated ping (200 plain text); export of the console's configuration (binary stream;
administrator); import of previously exported console data, a JSON document protected by the
master key, after the export file has been renamed to the import file name the console expects
(204; 400 wrong format; 500 import error; privilege administrator). Export and import are
self-hosted only.

## Licensing and hosted-service plumbing

Operations that exist only for license management or for an operator-run hosted service. One
entry each, so the inventory is complete; none is planned for trove.

### ADMAPI-103 · License installation and inspection
Priority: **Out of scope**

Read the installed license (type, valid-through date, licensee); install or replace a license
key; for clusters, list the installed licenses (type, validity, licensee, license hash, node ID,
node URL, expired flag — readable with the licenses-read system scope), install several keys at
once (per-key result map) and delete keys by a comma-separated list of hashes. Self-hosted only;
administrator.

### ADMAPI-104 · License pools in the central console
Priority: **Out of scope**

Upload an encrypted license pool (by download URL or file upload, plus decryption key; 409 for a
duplicate name or URL), list pools (identifier, name, size, license type), read a pool's usage
report, attach licenses from a pool to a registered instance, acquire licenses under a free-text
label, release them again (the instance must be offline first), and delete a pool.

### ADMAPI-105 · Network tunnel between a hosted and a self-hosted instance
Priority: **Out of scope**

An operator-run tunnel: a client service on the self-hosted side keeps outbound TCP connections
(opened with an HTTP upgrade) to a server service on the hosted side, so the hosted side can reach
the self-hosted instance through firewalls. Server side: reset a tunnel's connections (only on the
answering node in a cluster), block and unblock a tunnel, list blocked tunnels. Client side:
liveness and readiness probes (unauthenticated), define a tunnel (ID, server URL, local URL,
pairing token), establish or renew pairing (409 while another pairing runs), read the
configuration (token redacted), modify it (partial; server URL, insecure switch, proxy settings,
local URL, anonymous endpoint patterns, dial timeout, minimum and maximum tunnels, usage
thresholds, tunnel-creation interval, cron for closing tunnels and whether used tunnels may be
closed), delete (idempotent; 400 for system-configured tunnels), stop, restart, reset connections,
debug snapshot (per tunnel: configuration, active connections with stream counts and times),
Prometheus metrics (administrator or metrics-read scope; 404 when metrics are disabled), and a
support bundle (one at a time).

### ADMAPI-106 · Entitlement connector and offline registration
Priority: **Out of scope**

A connector service that fetches license entitlements from the license issuer. Operations: start
offline registration on an air-gapped instance (405 when offline mode is not configured), submit
the offline request token to the issuer's entitlement service from a connected machine (the
response body holds the entitlements, a response header the offline response token), apply
entitlements plus response token on the air-gapped instance, read the instance ID and service ID,
and unauthenticated liveness and readiness probes (503 when not ready).

### ADMAPI-107 · Hosted account portal operations
Priority: **Out of scope**

Operations of an operator-run hosted service: private network links per server (list by server
or by link, add, replace a failed link, remove; statuses connected, connecting, removing, failed),
custom-domain TLS certificates (list, create, renew, validate, delete, attach or detach domains of
type base URL or container-registry subdomain; 409 while another action runs), IP allowlist per
server (read, add, remove; at most 2,500 entries per request and 4,500 per account; private
RFC 1918 ranges rejected; allowed tenants of the same service), the service's outbound NAT address
ranges and available regions (unauthenticated), the list of servers in the account, and a
CSV usage export (traffic only; daily or monthly partitions; rate-limited).

### ADMAPI-108 · Hosted log collection
Priority: **Out of scope**

A hosted-only switch that starts collecting audit and request logs into a dedicated internal
repository (created on the first call; collection cycles of up to 24 hours; storage and transfer
billed). Input: enabled flag. Privilege: administrator. Self-hosted trove ships logs through its
own pipeline (INV-16).

## Governance and lifecycle operation groups

Recorded at index level only: the operation groups and what each covers, without per-operation
inputs. Capability descriptions live in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md) and
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### ADMAPI-109 · Application lifecycle service operations
Priority: **Later**

Groups: activity log; applications (list, create, batch create, read, update, delete, list
evaluations, detach an external integration); package versions bound to an application (list
bindings, bind, list bound versions, unbind); application versions (list, create, update, delete,
read content, list and read evaluations, post an integration event, read status); distribution
of an application version and remote deletion; promotion (promote, list, read); release;
rollback; integration configuration (save, remove); metrics; multi-site readiness; per-project
lifecycle stages with their gates, stage list and release stage; service health (liveness,
readiness, ping, service ID) and a service support bundle.

### ADMAPI-110 · Policy, rule, template and evaluation operations
Priority: **Later**

Create, list, read, update and delete for policies, rules and rule templates of a central policy
service, plus listing of policy evaluations. Interactions: [security-scanning-and-curation.md](security-scanning-and-curation.md).

### ADMAPI-111 · Lifecycle stage operations
Priority: **Later**

Create, list, read, update and delete lifecycle stages; read and update the ordered lifecycle
(the sequence of stages). Interactions: ADMAPI-068.

### ADMAPI-112 · Evidence operations
Priority: **Later**

Deploy evidence for a subject (an artifact or another entity), read by subject, by ID or by
entity, search, delete (by ID or by entity), and prepare evidence for client-side signing.

### ADMAPI-113 · Release set operations
Priority: **Later**

Release set versions: create, update a draft, finalize a draft, assign and delete tags, list
names, groups and versions, read the latest version, details, status and signature, find
versions containing an artifact, list deleted versions, delete, restore; received versions (list,
read artifacts and status, delete). Promotion: promote, list promotions per version, per
environment and per artifact, read details and status, roll back. Distribution: distribute, abort,
list and read distributions, delete remotely, propagate the public signing key, export a version
as an archive file, read export status, delete the export. Air-gapped transfer: trigger an export,
read its status, delete it, import an archive. Interactions: RET-024, IAM-026.

## Notes for implementers

- Two generations of identity and permission interfaces coexist (ADMAPI-021 versus
  ADMAPI-025 and ADMAPI-026, ADMAPI-005 versus the older user and lock
  operations). Existing scripts and CI plugins call both; the older "create or replace" of a
  policy silently deletes every resource section missing from the body.
- Partial and full updates are not uniform: users, groups, topologies and identity federation
  targets take partial updates (omitted fields keep their value, an empty group update succeeds);
  password-policy create, SCIM replace, the older policy replace and the configuration-document
  save reset everything not sent.
- Cleanup and archive policies must be created disabled; a create with the enabled flag on is
  rejected, and enabling is a separate call (ADMAPI-085). Policies cannot be updated or
  deleted while they run.
- The token ID "me" addresses the calling token for read and revoke (ADMAPI-044,
  ADMAPI-045). Token secrets appear only in the create response; the legacy API key, by
  contrast, is returned on every read (ADMAPI-049).
- An identity mapping's project is derived from its token scope, never sent separately, and
  cannot change on update (400). Naming a project in the exchange puts that project's mappings
  ahead of all global ones regardless of priority numbers (ADMAPI-030).
- Sending a single fine-grained project-administrator switch on project create switches the other
  unsent fine-grained switches on and disables the coarse switches' mapping (ADMAPI-063).
- Name matching differs per operation: usernames are case-insensitive and group names exact in
  the policy filter (ADMAPI-021); directory group imports store the name in lower case
  (ADMAPI-013); in older path segments "+" stands for a space by default.
- Read schemas of several configuration objects include secrets (directory manager password,
  Crowd application password, secret-manager credentials). A compatible implementation should
  accept them on write and mask them on read; clients must not depend on reading them back.
- Cluster-wide operations behave differently from node-local ones: logger level set and reset
  propagate to all nodes while a read reflects one node (ADMAPI-080); lock diagnostics are
  a best-effort snapshot (ADMAPI-078); tunnel resets act only on the answering node.
- Health probes, version and the OIDC exchange are unauthenticated, and public verification keys
  are readable anonymously when anonymous access is on; everything else needs an authenticated
  caller. Without administrator rights, identity and policy data is readable only through the
  system read scopes (ADMAPI-041), the project membership scopes and project-administrator rights
  for one's own projects (ADMAPI-064 to ADMAPI-066), and the manage action on a policy in the
  older interface (ADMAPI-025).
