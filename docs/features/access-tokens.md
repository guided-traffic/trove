# Access tokens

This page catalogs the credentials the system issues itself: the signed access token and its
short reference form, identity tokens, refresh tokens, the bootstrap administrator token, pairing
and trust tokens for links between services and instances, cross-instance token trust, the
deprecated per-user API key and older token interfaces. It covers token attributes and claims,
the scope grammar, audience, lifetime rules, refresh, listing, revocation, the ways clients
present a token, and the signing keys behind it. How a human logs in is on
[authentication-providers.md](authentication-providers.md);
exchanging a CI-issued OIDC token for a token is on [oidc-ci-integration.md](oidc-ci-integration.md);
the permission model that scopes refer to (users, groups, permission policies, projects, roles) is
on [identity-and-access.md](identity-and-access.md); the operation catalog is on
[api-admin-operations.md](api-admin-operations.md).

## Token model

### TOK-001 · Access token as the system's own credential
Priority: **MVP**

Every successful authentication, whichever method produced it (internal password, OAuth/OIDC,
SAML, LDAP, CI OIDC exchange), ends in a credential the system issues itself: an access token.
That token is the bearer credential for all following UI, API and command-line requests, and it is
also the credential handed to automation. Tokens are not tied to human users only: a token can be
issued for a non-user subject such as a CI job (TOK-010, TOK-015).

Properties the rest of this page builds on:

- Scoped: each token carries a scope that bounds what it can do (TOK-007).
- Time-bound: each token has an expiry or is explicitly non-expiring (TOK-017).
- Independent: one identity may hold any number of tokens, each with its own scope and lifetime.
  Issuing a new token never invalidates earlier ones; a token ends only by expiring or by explicit
  revocation (TOK-032).
- Shown once: the secret is displayed only at creation (TOK-006).

Interactions: the login side of this model is AUTHN-001 in
[authentication-providers.md](authentication-providers.md); CI identities obtain tokens through
the exchange on [oidc-ci-integration.md](oidc-ci-integration.md).

### TOK-002 · Token attributes and claims
Priority: **MVP**

Every token has the following attributes. In the self-contained form (TOK-003) they are JWT
claims ([RFC 7519](https://www.rfc-editor.org/rfc/rfc7519)); the registered claim used for each is
given where one is defined.

| Attribute | Meaning | Default |
|---|---|---|
| Token ID | Unique identifier of the token (`jti`); key for lookup, listing and revocation | generated |
| Subject | The user the token belongs to (`sub`). Administrators may name any subject; other users only themselves. A subject that does not exist as a user is evaluated as a transient user (TOK-015) | the creating user |
| Scope | Space-separated list of scope items (TOK-007); carried in a scope claim | user-permissions scope |
| Audience | Services and instances that accept the token (`aud`, TOK-016) | any service on any instance |
| Issuer | Identifier of the instance or cluster that created the token (`iss`) | the issuing instance |
| Issued at | Creation time (`iat`), epoch seconds | creation time |
| Expiry | Expiry time (`exp`), epoch seconds; absent for non-expiring tokens (TOK-017) | default expiry (TOK-018) |
| Description | Free text to recognise the token, at most 1,024 characters | empty |
| Refreshable | Whether a refresh token was issued (TOK-024) | false |
| Project | Project key; present only on tokens a project administrator created (TOK-011) | absent |
| Extension data | Additional signed data: the force-revocable flag (TOK-022); for pairing tokens the base, exchange and pairing URLs (TOK-042) | absent |
| Last used | Epoch seconds of the most recent use, when last-used tracking is on (TOK-029) | absent |

Edge cases:

- Groups named in the scope travel inside the token payload. Long or many group names enlarge
  the token and can exceed the reference-token payload limit (TOK-004).
- Auto-join groups applied to a transient subject are not in the token (TOK-015).

### TOK-003 · Self-contained signed token format
Priority: **P2**

The full access token is a JSON Web Token signed with the instance's token-signing private key
(TOK-036). Example header: `{"typ":"JWT","alg":"RS256"}`, i.e. an RSA signature with SHA-256.
Because the token is self-describing, any party holding the issuer's public certificate can verify signature and claims without a database lookup; this is
what makes cross-instance trust (TOK-039) possible. Whether the database is consulted on
validation depends on the persistence and revocability thresholds (TOK-020, TOK-021).

Limits and edge cases:

- JWTs are long. Some clients cannot handle a credential of that length, and some do not support
  bearer authentication at all; the reference token (TOK-004) exists for them.
- A token signed by a key that is no longer trusted fails validation; replacing the signing pair
  therefore invalidates all outstanding tokens (TOK-038).

### TOK-004 · Reference tokens
Priority: **MVP**

A reference token is a short opaque alias for a full access token. The system stores it hashed in
its database and resolves it on every request, so it always needs a database lookup on the
issuing instance.

- Format: a fixed, recognisable prefix followed by alphanumeric characters, 64 characters in
  total, so secret scanners can match it with a regular expression. Open: either 64 characters,
  as the regular expression describes, or a 128-character key; the spec decides.
- Issued in addition to the full token when the creator asks for it (TOK-025, default: not
  requested). The UI generation dialog has a "create reference token" option (TOK-026). The
  self-service identity-token flow in the user profile always issues the reference form
  (AUTHN-040 in [authentication-providers.md](authentication-providers.md)).
- Revocable individually, by ID or by value (TOK-030, TOK-031). Listing includes reference tokens
  (TOK-027).
- Not retrievable after creation: neither UI, API nor database yields it again.
- Usable as a bearer token (TOK-033), as the password in Basic authentication (TOK-034), and in
  the legacy API-key header (TOK-044).

Limits and edge cases:

- The payload behind a reference token may not exceed 4,000 characters. The number and length of
  the groups in the token count against it. For users whose groups come from SAML, newer releases
  keep the group association in the system instead of the payload, so it no longer counts.
- Not accepted across instances that trust each other's signing certificates (TOK-039): the
  receiving instance cannot resolve another instance's reference.

### TOK-005 · Identity tokens
Priority: **MVP**

An identity token is an access token whose scope is the user-permissions scope (TOK-008): it
grants exactly what its subject is allowed to do, evaluated at use time. Any user can create
identity tokens for themselves; it is the only token kind a user without administrator or
project-administrator rights can create (TOK-007). Identity tokens are revoked when their user is
deleted or disabled.

UI: self-service generation, listing and revocation in the user profile (AUTHN-040 in
[authentication-providers.md](authentication-providers.md)); administrators see and revoke them on
the token management screen (TOK-026). API: the general create operation with no scope or the
user-permissions scope (TOK-025).

Interactions: refresh is blocked for a de-provisioned user only when de-provisioning reaches the
system (AUTHN-030 in [authentication-providers.md](authentication-providers.md)); the
administrator can restrict whether identity tokens may be refreshable (TOK-024).

### TOK-006 · One-time display of token secrets
Priority: **MVP**

Token values (full token, reference token, refresh token, pairing token) are shown only in the
response or dialog that created them. The UI result window offers a copy action and warns that
the value is gone once the window is closed. The system keeps reference tokens and identity tokens
only in hashed form, so a lost token cannot be recovered; the user creates a new one. The
companion command-line client prints the value once as well, and in its table output truncates
the token to 40 characters (TOK-035).

## Scopes

### TOK-007 · Scope expression
Priority: **MVP**

A token's scope is a space-separated list of scope items, at most 500 characters in total. Each
item belongs to one of three families:

| Family | What it grants | Entries |
|---|---|---|
| Applied permissions | Permissions already defined in the system, applied through the subject, named groups, administrator status or project roles | TOK-008 … TOK-011 |
| Resource permissions | Direct grants on named resources, independent of any defined permission policy | TOK-012, TOK-013 |
| System permissions | Narrow read or management access to system resources without administrator rights | TOK-014 |

Rules:

- Every token may call the system's REST API at all; the scope decides which permissions apply on
  top of that baseline.
- No scope given: the token gets the user-permissions scope (TOK-008).
- Administrators may request any scope. Users without administrator rights may only request the
  user-permissions scope. Project administrators may additionally request project-role scopes for
  projects they administer (TOK-011).
- Requesting a scope the caller is not entitled to is refused (forbidden, TOK-025).
- Quoting: a name that contains spaces or commas is enclosed in double quotes (TOK-010, TOK-011).

UI: the token list shows each token's scope in its own column (TOK-026).

### TOK-008 · User-permissions scope
Priority: **MVP**

The token acts with the subject's own permissions, including those of the subject's groups. The
scope names the subject rather than a list of permissions, so the rights in force at use time
apply, in particular for project roles (TOK-011) and for auto-join groups (TOK-015). It identifies
the user to the system and grants nothing beyond what the user has.
This is the default scope and the scope of identity tokens (TOK-005). If the subject does not
exist as a user, the token is evaluated as a transient user that also receives the auto-join
groups (TOK-015).

### TOK-009 · Administrator scope
Priority: **MVP**

The token carries full administrator rights. Only administrators can create it. It may be
assigned to any subject name, including a name with no matching user, and still has administrator
rights.

- Purpose: companion services that need administrator access (central
  management, scanning) obtain an administrator token when they connect, instead of authenticating
  with username and password.
- UI: the token management screen offers an "administrator" token type with subject name,
  service selection (all services or a chosen subset, which sets the audience, TOK-016) and
  expiry; only services present in the deployment are listed (TOK-026).
- Command-line client: an explicit "grant administrator" option (TOK-035).

Interactions: within a set of instances that trust each other's signing certificates, an
administrator token from one member is an administrator token on all (TOK-039).

### TOK-010 · Group scope
Priority: **MVP**

The token carries the permissions of one or more named groups. The subject does not have to exist
as a user: the token authenticates as a transient subject whose rights are exactly those of the
listed groups, and only until the token lapses (TOK-015). Only administrators can create group
tokens, because other users can only name themselves as subject.

- Purpose: authenticate automation (a CI server, one token per job) without maintaining
  placeholder user accounts.
- Syntax: a comma-separated list of group names. Names containing spaces or commas are each
  enclosed in double quotes. Open: whether enclosing the whole list in one pair of quotes is
  accepted as well; the spec decides.
- UI: "group" token type with a group picker (type-ahead), service selection and expiry
  (TOK-026).

Edge cases:

- The groups must exist on every instance that validates the token (TOK-039).
- Whether a singular form of the group keyword is accepted as an alias is unspecified.

### TOK-011 · Project-role scope
Priority: **P2**

The token carries the permissions of one or more roles within one project, as those roles are
defined at use time. It reaches every resource the roles cover in that project (repositories,
artifacts, build records).

- Syntax: project key, then a comma-separated list of role names; role names with more than one
  word are enclosed in double quotes.
- Who: system administrators, and project administrators for the projects they administer.
- Listing: tokens created by a project administrator carry the project key; the token list shows
  project key and role, and the key is empty for all other tokens (TOK-026, TOK-027).
- UI (project administrator): "generate token" dialog with description, token roles (global and
  project roles are offered), service selection and expiry. The dialog caps the expiry at 24 hours.

Interactions: projects and roles are defined on [identity-and-access.md](identity-and-access.md).

### TOK-012 · Resource permission scope
Priority: **MVP**

A resource scope item grants actions on named resources directly, without any permission policy,
project or role being defined for them. Structure:

```text
<resource-type> ":" <target> [ "/" <sub-resource> ] ":" <actions>
```

| Part | Meaning | Example |
|---|---|---|
| Resource type | From a closed list; for repository content the artifact type | artifact |
| Target | Exact name or pattern; for artifacts the repository key | `npm-local` |
| Sub-resource | Optional exact name or pattern below the target; for artifacts the path. Omitted: the whole target | `org/**` |
| Actions | Comma-separated single-letter action codes, or `*` for all actions the type supports | `r,w` |

Patterns use Ant-style syntax (`*`, `**`). Action codes across all resource types: `r` read,
`w` write, `d` delete, `a` annotate, `x` execute, `s` scan, `m` manage permissions on the
resource. Each resource type accepts a subset:

| Resource type | Target | Sub-resource | Actions |
|---|---|---|---|
| Artifact | Repository key | Path in the repository | read; write (deploy and cache); delete and overwrite; annotate; scan; manage |
| Project | Project key | Members | read (TOK-013) |

Examples (illustrative notation): read and write on everything under `org/` in repository
`npm-local`; read on all repositories with target `*`, which also lets the token list
repositories.

Edge cases: since the grant does not depend on policies, only administrators can mint such tokens
(TOK-007); whether a resource scope is intersected with the subject's own permissions is
unspecified. Open: the closed list of resource types either holds the artifact type only, or also
the project type (as in the table above) and the further types the system scopes (TOK-014) use,
such as a per-repository configuration type; the spec decides.

Interactions: the actions mirror the repository actions of the permission model on
[identity-and-access.md](identity-and-access.md); CI exchange tokens use the same idea of narrowly
scoped grants ([oidc-ci-integration.md](oidc-ci-integration.md)).

### TOK-013 · Project membership read scopes
Priority: **P2**

Resource scope items on the project type let a token read a project's membership without
administrator or project-administrator rights:

| Capability | Action |
|---|---|
| List the users of a project (users below the members sub-resource) | read |
| List the groups of a project (groups below the members sub-resource) | read |
| List the roles of a project (a roles sub-resource of its own, not below members) | read |
| List all members (users and groups) through a wildcard below the members sub-resource | read |

### TOK-014 · System scopes
Priority: **P1**

System scope items give access to system resources without administrator rights. They follow the
same three-part shape (resource, target, actions).

| Capability | Actions | Operations it unlocks |
|---|---|---|
| System metrics | read | Read metrics |
| Live logs | read | Read the live log stream |
| Identities (users and groups) | read; write and delete are used by the SCIM provisioning endpoints | List users and groups, read user or group details; SCIM create, update, delete |
| Permission policies | read | List permission policies, read one by name |
| Installed licenses | read | List installed licenses |
| Storage information | read | Read the storage summary |
| System information | read | List repositories (a read resource scope on all artifact targets works too, TOK-012) |
| Repository configuration | read, per repository | Read one repository's configuration |
| Webhook subscriptions | read, write, delete, execute; bound to a project and optionally to one event domain (for example artifact events) | List, read, create and manage subscriptions; read webhook troubleshooting data |

Interactions: SCIM connector tokens (AUTHN-029 in
[authentication-providers.md](authentication-providers.md)); metrics and logs on
[operations-and-monitoring.md](operations-and-monitoring.md); webhooks on
[events-and-extensibility.md](events-and-extensibility.md).

### TOK-015 · Transient subjects and automatic group membership
Priority: **MVP**

When a token's subject does not exist as a user, the system builds an in-memory transient user
while authenticating the request; it is never written to the database and lives only for that
request or session. What the transient user receives depends on the scope:

| Token | Effective permissions |
|---|---|
| Group scope only, no user-permissions scope | Exactly the listed groups |
| With the user-permissions scope | The listed groups plus every local group flagged to be joined automatically by new users |

- The auto-join groups are not in the token; they are applied on the validating instance at
  authentication time. The same token can therefore carry different rights on different instances,
  depending on each instance's auto-join configuration.
- A CI OIDC exchange whose identity mapping includes a username adds the user-permissions scope
  automatically, so such tokens pick up the auto-join groups. Recommended: audit auto-join groups
  regularly, or map without a username, to keep CI permissions bounded.

Interactions: AUTHN-005 in [authentication-providers.md](authentication-providers.md);
identity mappings on [oidc-ci-integration.md](oidc-ci-integration.md).

## Audience

### TOK-016 · Audience restriction
Priority: **P2**

The audience lists the services that accept the token, as a space-separated list of service
identifiers (at most 255 characters). A service identifier has the form
`<service-type>@<instance-id>`; either part can be the wildcard `*`, so the default `*@*` means any
service type on any instance. The instance identifier is generated internally per service or
cluster. The repository service returns its own service identifier as plain text through an
administrator-only "get service ID" operation; an auxiliary connectivity component of self-managed
deployments offers a second operation that lists the identifiers of the deployment's services
without authentication.

| Setting | Meaning | Default |
|---|---|---|
| Audience | Services that accept the token | any service type on any instance |

- UI: a "service" field with an "all" checkbox; clearing it shows the list of services present in
  the deployment to choose from (TOK-026).
- The bootstrap administrator token (TOK-041) is limited to the access-management service.
- Example (command-line client): a token whose audience is the repository service on any
  instance.

## Expiry and revocability

### TOK-017 · Token lifetime and non-expiring tokens
Priority: **MVP**

The creator sets the lifetime in seconds. The token is valid until then and lapses automatically.
A lifetime of zero creates a non-expiring token, which stays valid until it is revoked.

- Non-expiring tokens are reserved to administrators: the create operation marks lifetime zero
  as administrator-only, and the maximum-expiry setting (TOK-019) leaves the restriction for other
  users untouched. Open: a maximum above zero either forbids non-expiring tokens in the whole
  environment, administrators included, or applies to non-administrators only and lets
  administrators set any lifetime; the spec decides.
- No lifetime given: the default expiry applies (TOK-018).
- UI: an expiration field with preset choices and a custom value in hours.
- Validation: an invalid lifetime is rejected as invalid input. Open: the error condition is
  either "not a number or non-positive", which contradicts zero meaning non-expiring, or leaves
  zero valid; the spec decides. A lifetime above the caller's limit is refused as forbidden (TOK-025).
- Recommended: the shortest lifetime that still covers the task, and lifetimes below the
  persistence threshold (TOK-021) to avoid database checks on every request.

Interactions: the lifetime also decides revocability (TOK-020) and persistence (TOK-021).

### TOK-018 · Default token expiry
Priority: **MVP**

A system-wide default lifetime applies when a creation request names none. It is also the value
pre-filled when creating identity tokens.

| Setting | Meaning | Default |
|---|---|---|
| Default expiry | Lifetime in seconds applied when none is requested. Zero means no default is forced. Must be non-negative and not above the maximum expiry when a maximum is set | 31,536,000 s (365 days) |

Operations (administrator): read the default expiry (returns seconds); set the default expiry
(seconds, required). UI: global security settings, token expiration section.

Edge cases:

- Open: the default for tokens created without a lifetime is either one year (the table value) or
  3,600 seconds (one hour, which an example command-line response also carries); the spec decides.
  Internal tokens have a separate default of 3,600 s (TOK-048); whether the one-hour value refers
  to that is unspecified.
- With a forced default and no lifetime in the request, users receive the default; how an
  administrator request without a lifetime is treated is unspecified.

### TOK-019 · Maximum expiry for non-administrators
Priority: **MVP**

Administrators may set any lifetime. Other creators are capped:

| Setting | Meaning | Default |
|---|---|---|
| Maximum expiry | Upper limit in seconds for tokens created by users without administrator rights. Zero means no limit. Any value above zero also forbids non-expiring tokens for these users. Must not be below the default expiry | 0 (unlimited) |

UI: global security settings, token expiration section. Recommended: set a maximum.

Edge cases:

- Open: the cap applies either to users without administrator rights, or only to tokens generated
  by project administrators; a third variant caps users without administrator rights at the
  default expiry instead. The spec decides.
- The project-administrator dialog applies its own 24-hour cap (TOK-011).
- A request above the cap is refused as forbidden (TOK-025).

### TOK-020 · Revocability threshold
Priority: **P2**

The lifetime decides whether a token can be revoked. A token whose lifetime is shorter than the
revocability threshold cannot be revoked and simply runs out; a token at or above the threshold
can be revoked at any time before expiry.

| Setting | Meaning | Default |
|---|---|---|
| Revocability threshold | Shortest lifetime, in seconds, for which tokens are revocable | 21,600 s (6 hours) |

- Non-expiring tokens are always revocable, whatever the threshold.
- Zero makes every token revocable; every validation then consults the database, which is costly.
- A negative value (for example -1) makes every token that has an expiry non-revocable; this is a
  security risk.
- Values below 2 hours are discouraged for performance.
- Must be at least the persistence threshold (TOK-021).

Edge cases: default installations make every token revocable, while revoke by value treats a JWT
as revocable only if it was created refreshable or with the force-revocable flag (TOK-022,
TOK-031). Open: how these two rules combine with the threshold; the spec decides.

### TOK-021 · Persistence threshold
Priority: **P2**

The persistence threshold decides which tokens are stored in the database. A token whose lifetime
(expiry minus issue time) is below the threshold is not stored and is validated from its
signature alone.

| Setting | Meaning | Default |
|---|---|---|
| Persistence threshold | Minimum lifetime in seconds for a token to be stored | 10,800 s (3 hours) |

- Zero or negative: every token is stored and every validation, including internal service
  tokens, consults the database; values below 2 hours are discouraged.
- Constraint: persistence threshold ≤ revocability threshold, because a token that is not stored
  cannot be revoked. If both are positive and the constraint is violated at startup, the system
  logs an error and lowers the persistence threshold to the revocability threshold.
- Lowering the threshold on a running system has a side effect: tokens with a lifetime under the
  previous value but over the new revocability threshold are in effect revoked. The mechanism is
  unspecified.

### TOK-022 · Force-revocable flag
Priority: **P2**

A creation option marks a token as revocable regardless of the thresholds; the flag is recorded
in the token's extension data.

| Setting | Meaning | Default |
|---|---|---|
| Force revocable (per request) | Marks this token revocable | false |
| Force revocable by default (system) | Default for the per-request option | false |

The system default stays false so that tokens remain usable across instances that trust each
other (TOK-039): a revocable token can only be checked against the issuing instance's database.

### TOK-023 · Token expiry notifications
Priority: **P2**

The system can e-mail token owners before their tokens expire.

<details>
<summary>Settings (global security settings, token expiration section)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enable e-mail notifications for token expiration | Turns the notifications on | off |
| Notice period | Days before expiry at which notification starts | 1 day |
| Time of daily check | Time of day (24-hour clock) for the once-daily scan; the UI converts the entered time to UTC. The scan looks for tokens expiring within a 24-hour window after the notice period | 08:00 UTC |
| Notification frequency | Notify once, or daily throughout the notice period | unspecified |

</details>

Edge cases: when token records are federated between instances, notifications should be enabled
on one instance only, otherwise owners receive duplicates. Requires a configured mail server
([operations-and-monitoring.md](operations-and-monitoring.md)).

## Refresh

### TOK-024 · Refreshable tokens and the refresh grant
Priority: **P1**

A token created as refreshable comes with a refresh token. Presenting the refresh token yields a
replacement access token that carries the original's privileges unchanged, so the holder, not the
issuer, decides when to renew. The exchange follows the OAuth 2.0 refresh grant
([RFC 6749 §6](https://www.rfc-editor.org/rfc/rfc6749#section-6)) on the same operation that
creates tokens (TOK-025).

| Setting | Meaning | Default |
|---|---|---|
| Refreshable (per request) | Issue a refresh token with this token | false |
| Allow refreshable tokens (system) | When off, user tokens are created non-refreshable; open whether this covers every user token or only user identity tokens | on |
| Refresh grace period (system) | How long after expiry a refreshable token can still be refreshed, in seconds; must be positive | 86,400 s (24 hours) |

Rules:

- The caller of the refresh must be the same user as the token being refreshed.
- Only the instance or cluster that issued the token can refresh it, unless token records are
  shared through identity federation ([replication-and-federation.md](replication-and-federation.md)).
- On the operation described here, the refresh token alone identifies the token being refreshed.
  The distribution service's own token operation differs: it also requires the current access
  token and accepts a new lifetime and refreshable flag on refresh (TOK-047).
- The command-line client refreshes its stored tokens automatically unless told not to (TOK-035).

Edge cases: an external user removed at the identity provider keeps refreshing unless
de-provisioning reaches the system (SCIM); see AUTHN-030 in
[authentication-providers.md](authentication-providers.md).

## Token operations and UI

### TOK-025 · Create a token
Priority: **MVP**

One operation creates a token or refreshes one. It accepts form-encoded or JSON input and follows
the OAuth 2.0 token-endpoint conventions
([RFC 6749 §4.4, §5.1](https://www.rfc-editor.org/rfc/rfc6749#section-4.4)): grant type
`client_credentials` creates, `refresh_token` refreshes.

<details>
<summary>Inputs</summary>

| Input | Meaning | Default | Limit |
|---|---|---|---|
| Grant type | Create or refresh | create | – |
| Refresh token | Required when refreshing | – | – |
| Subject username | User the token is for; administrators only for names other than their own | the caller | 255 characters |
| Scope | Scope list (TOK-007) | user-permissions scope | 500 characters |
| Lifetime | Seconds; 0 = non-expiring (administrators only, TOK-017) | default expiry (TOK-018) | ≥ 0; maximum expiry for non-administrators (TOK-019) |
| Refreshable | Issue a refresh token (TOK-024) | false | – |
| Description | Free text | empty | 1,024 characters |
| Audience | Service identifiers (TOK-016) | any service on any instance | 255 characters |
| Include reference token | Also return the reference form (TOK-004) | false | – |
| Force revocable | Mark revocable regardless of thresholds (TOK-022) | false | – |
| Project | Not a separate input in the operation schema: the project key is part of the project-role scope item (TOK-011). The command-line client has its own project option (TOK-035) | – | – |

</details>

Outputs: the access token, token type (`Bearer`), lifetime in seconds, granted scope, the refresh
token when refreshable, the reference token when requested. The UI and the command-line output
also show the token ID, audience and refreshable flag; the operation schema does not list them.

Responses: success; invalid input (malformed lifetime, TOK-017); unauthenticated; forbidden
(lifetime above the caller's limit, scope not permitted for the caller, subject other than self
for a non-administrator).

Required privilege: any authenticated caller for a token for themselves with the
user-permissions scope; project administrator for project-role tokens; administrator for
everything else.

Authentication of the create call:

- With an existing token (bearer).
- With username and password (Basic) only for internal-realm users, only while the "allow token
  creation through the API for basic-authenticated users" switch is on (default: off; AUTHN-003 in
  [authentication-providers.md](authentication-providers.md)), and never for a user with
  multi-factor authentication enabled.

### TOK-026 · Token management screen
Priority: **MVP**

Administration area, user management, access tokens. The screen lists tokens and generates new
ones.

- List: one row per token with its scope in a dedicated column; tokens created by project
  administrators show a project key and role, others an empty project key. Selecting a row and
  choosing revoke revokes it (TOK-030).
- Generate (administrator): a token-type selector with administrator, user and group:
  - administrator: subject username, service selection (all, or a chosen subset), expiration
    (presets or custom hours);
  - user: user picker with type-ahead, service selection, expiration;
  - group: group picker with type-ahead, service selection, expiration;
  - a "create reference token" checkbox (its initial state is unspecified).
- Generate (project administrator): description, token roles, service selection, expiration
  capped at 24 hours (TOK-011).
- Result window: it lists the subject username, scope, audience, expiry, token ID and token value,
  plus the reference token when requested, each with a copy action; the window warns that the
  values are not stored (TOK-006).
- A separate pairing-token tab generates pairing tokens (TOK-042).

Interactions: self-service identity tokens live in the user profile (AUTHN-040 in
[authentication-providers.md](authentication-providers.md)); screen inventory on
[ui-inventory.md](ui-inventory.md).

### TOK-027 · List tokens
Priority: **MVP**

Returns the tokens visible to the caller. Administrators see all tokens; other users see only
their own. Reference tokens are included.

Output per token: token ID, subject, expiry (epoch seconds; absent for non-expiring tokens),
issued-at, issuer, description, refreshable flag, scope, project key (only for tokens created by a
project administrator), last-used time (only with TOK-029). Token values are never returned.

Required privilege: any valid token. The operation supports filtering and sorting through query
parameters; which parameters exist is unspecified. The deprecated list operation it replaces had no
filtering (TOK-046).

### TOK-028 · Get token details
Priority: **MVP**

Returns one token's details (same fields as TOK-027) by token ID. A reserved alias in place of the
ID returns the details of the token that authenticates the request itself, which lets a client
inspect its own credential (expiry, scope) without knowing its ID.

Required privilege: any valid token; administrators may read any token, other users only tokens
whose subject is their own username. Responses: success, unauthenticated, forbidden, not found.

### TOK-029 · Last-used tracking
Priority: **P1**

When enabled, the system records when each token was last used and returns it as epoch seconds in
list and detail responses (TOK-027, TOK-028).

| Setting | Meaning | Default |
|---|---|---|
| Record last use | Turns tracking on | off (must be enabled) |
| Recording threshold | Coarsening interval for updates; example value 900, unit unspecified | unspecified |

Edge cases: whether use by reference token, bearer or Basic authentication is tracked alike is
unspecified.

### TOK-030 · Revoke a token by ID
Priority: **MVP**

Revokes the token with the given ID. The same reserved alias as in TOK-028 revokes the token that
authenticates the request (self-logout of a credential).

- Privilege: users revoke their own tokens; administrators any token.
- Responses: success, unauthenticated, forbidden, not found.
- Applies only to revocable tokens (TOK-020) and only on the issuing instance, unless token
  records are federated ([replication-and-federation.md](replication-and-federation.md)).
- UI: revoke action on the token management screen (TOK-026) and in the user profile (AUTHN-040).

### TOK-031 · Revoke a token by value
Priority: **MVP**

Revokes a token given its value, either a full JWT or a reference token, without knowing its ID;
suited to responding to a leaked secret.

- Input: the token value (required).
- Output: the ID of the revoked token.
- Privilege: administrator.
- Fails with "not found" when the token is unknown or not revocable. A JWT counts as revocable
  here only if it was created refreshable or with the force-revocable flag (TOK-022).

### TOK-032 · Revocation semantics
Priority: **MVP**

- The system keeps a list of revoked token IDs and checks it on every validation of a token that
  is subject to revocation checks (TOK-020, TOK-021).
- Expiry ends a token automatically; a non-expiring token ends only by revocation.
- Creating a new token never revokes existing ones; a compromised token has to be revoked
  explicitly.
- Deleting or disabling a user revokes that user's identity tokens (AUTHN-030 in
  [authentication-providers.md](authentication-providers.md)). The related configuration switch
  "delete a user's tokens together with the user" defaults to on.
- A scheduled job cleans up expired tokens. It runs hourly by default; when the default schedule
  is kept, its minute and second are chosen at random per installation, and the job can be
  switched off.
- Validation results are cached: by default an entry is dropped 60 s after it was written and the
  cache holds at most 1,000 tokens. Whether a revocation takes effect before a cached entry
  expires is unspecified.
- An optional switch (default: off) ends a user's earlier UI session tokens when a new session
  token is created for that user (AUTHN-038 in
  [authentication-providers.md](authentication-providers.md)).
- Removing tokens by subject matches the subject name only: tokens of different users that share
  the same subject name are removed together, which is intended.
- Replacing or resetting the signing key pair invalidates every token signed with the old key at
  once (TOK-038).
- Pairing and trust tokens are revoked like any other token; revoking a trust token ends the
  trust relationship (TOK-042).

## Presenting tokens

### TOK-033 · Bearer authentication
Priority: **MVP**

A client puts either form of the token, full or reference, into the standard header
`Authorization: Bearer <token>` ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750)). This works
for UI, API and command-line requests. Because a group-scoped token (TOK-010) needs no user
account, CI servers can authenticate this way without placeholder users.

### TOK-034 · Token as the password in Basic authentication
Priority: **MVP**

A reference token can replace the password in HTTP Basic authentication
([RFC 7617](https://www.rfc-editor.org/rfc/rfc7617)), for package clients that only speak Basic.
The username sent must be the subject username the token was created for.

Edge cases: whether a full JWT is accepted as a Basic password is unspecified; the reference token
is the form defined for this purpose. Whether the switch that disables Basic authentication for
internal passwords (AUTHN-003 in
[authentication-providers.md](authentication-providers.md)) also blocks tokens sent as Basic
passwords is unspecified.

### TOK-035 · Command-line client token handling
Priority: **P2**

The companion command-line client works with tokens as follows:

- Server configuration accepts a token directly (also for reference tokens) or username and
  password. With username and password, the client obtains an access token itself instead of
  storing the password as a credential, and refreshes it automatically; an option disables the
  automatic refresh, another uses username and password (or a legacy API key) directly without any
  token. Tokens can be fed through standard input to keep them off the command line. The client
  does not read credentials from environment variables by itself. A stored server configuration
  can be exported as one portable encoded string and imported on another machine. Besides tokens
  and passwords the client can authenticate with an SSH key pair or a TLS client certificate
  (AUTHN-041, AUTHN-034 in [authentication-providers.md](authentication-providers.md)); package
  managers driven through the client support neither.
- A token-creation command creates a token for the current user, or for a named user when run by
  an administrator. Options: project key; grant administrator, groups and scope (administrator
  only); lifetime in seconds (default: the system default; 0 for non-expiring, administrator
  only); refreshable (default off); description (up to 1,024 characters); audience; reference
  token (default off); output format JSON (default) or table. The command refuses to run against a
  server configured with username and password.
- An OIDC exchange command and a browser-login command obtain tokens without a stored secret
  ([oidc-ci-integration.md](oidc-ci-integration.md), AUTHN-044 in
  [authentication-providers.md](authentication-providers.md)).

Interactions: companion CLI capabilities on [builds-and-ci.md](builds-and-ci.md).

## Signing keys and cross-instance trust

### TOK-036 · Token-signing key pair
Priority: **P2**

Self-contained tokens (TOK-003) are signed with a private key and verified with the matching
public certificate (the "root" certificate of the token issuer). The system generates the pair
itself unless the operator supplies one (TOK-037); at which point of the lifecycle it does so is
unspecified. Certificates placed in the trusted-certificate directory are moved into the database
once loaded (TOK-039). In a high-availability cluster the pair is propagated between the nodes
automatically.

Interactions: [ha-and-deployment.md](ha-and-deployment.md). The at-rest encryption key of the
database plays no role in token validation (Notes for implementers).

### TOK-037 · Custom signing certificates
Priority: **P2**

Operators can supply their own certificate authority and token-issuer key pair instead of the
generated ones. At certificate initialization the system reads, if present, four PEM files from
its bootstrap key directory: the CA certificate, the CA private key, the token-issuer certificate
and the token-issuer private key.

Requirements:

- Certificate and private key must belong together.
- The certificate must remain valid for at least 7 more days.
- A CA certificate must carry the critical basic constraint CA:TRUE and the critical key usages
  digital signature and certificate signing. Example: a 2,048-bit RSA key and a SHA-256
  self-signed certificate valid for 365 days.

Edge cases: loading a new pair invalidates all previously issued tokens unless the old public
certificate is kept as trusted (TOK-038).

Related: the system can also act as certificate authority for TLS between its services, sign
server certificates with an operator-supplied CA, or use pre-signed server certificates; TLS
itself is on
[ha-and-deployment.md](ha-and-deployment.md).

### TOK-038 · Signing key reset and rotation
Priority: **P2**

An administrator forces a new signing pair by placing a reset trigger file in the bootstrap key
directory and restarting the service. All tokens signed with the old pair stop validating. To keep
them valid, the old public certificate is copied into the trusted-certificate directory first.

A signing-pair rotation is the way to invalidate every outstanding token at once, for example after
a suspected compromise; individual compromised tokens are revoked instead (TOK-030, TOK-031).

### TOK-039 · Cross-instance token trust
Priority: **Later**

Several instances can accept each other's tokens by sharing public signing certificates: each
instance verifies a token's signature against its own key and against every certificate in its
trusted set. A single build agent can then use one token on several instances.

- Setup: copy an instance's public token certificate into the other instance's
  trusted-certificate directory, or fetch it through a "get root certificate" operation
  (administrator; an option returns it as PEM with header, footer and line breaks, default off).
  A separate administrator-only operation returns the public certificate of the system's
  certificate authority (TOK-037). Name each
  copied certificate after its source instance. The file must belong to the service's operating
  system user and group. Helm installations have their own procedure for adding trusted
  certificates; its details are unspecified here.
- Trust may be one-way or mutual. The trusted set is watched and reloaded when a token is
  verified, and a background task looks for newly added trusted certificates every 30 s by
  default; adding or removing a certificate needs no restart. A loaded certificate moves from the
  directory into the database.
- Members of the set effectively hold administrator rights on each other, since administrator
  tokens from one are valid on all.

Restrictions:

- Users and groups named in a token's scope must exist on every member.
- Revocable tokens are checked against the issuing instance's database, which other members
  cannot reach. Either share token records through identity federation
  ([replication-and-federation.md](replication-and-federation.md)), or use tokens shorter than the
  revocability threshold (TOK-020) and set the same threshold on all members.
- Reference tokens, and therefore tokens created in the user profile, do not work across members.
- Refresh stays with the instance that issued the token; other members cannot perform it.

### TOK-040 · Centrally managed trust links between instances
Priority: **Later**

A trust link is a token-based, mutual trust relationship between two managed instances with a
narrower reach than full certificate sharing (TOK-039); it also lets administrators of hosted
instances set up such links themselves. A central multi-instance management service creates the
link tokens and renews them; that service must be enabled and the instances must be registered
with it.

- UI: administration area, deployments, trust links. Add a link for a purpose (federated
  repository), select source and target instances, apply (repeatable to add more), create; the
  results are listed per pair. A failed link is retried after checking that the source instance is
  reachable.
- Remove: select one or more links, delete, confirm; removal ends trust in both directions.
- For federated repositories, the root and intermediate certificates must be placed in the
  instance's trusted-certificate locations; on which side is unspecified.

Interactions: [replication-and-federation.md](replication-and-federation.md),
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

## Service-to-service tokens

### TOK-041 · Bootstrap administrator token
Priority: **MVP**

An operator can obtain the first administrator-scoped token without the UI, without basic
credentials and without a prior token, so an installation can be configured fully by automation.

- Trigger: a token-request file placed in the bootstrap key directory before startup. When the
  service starts and finds it, it generates a token, writes it to a token file in the key
  directory, and deletes the request file. The content of the request file is unspecified.
- The token file is deleted after a configurable delay.
- Container installations must mount the bootstrap directory.

| Property or setting | Value |
|---|---|
| Scope | administrator |
| Subject | the default administrator username, whether or not that user exists |
| Audience | the access-management service only |
| Lifetime | 15 minutes; the system revokes it afterwards |
| Token-file deletion delay | 1 minute (default, configurable in minutes) |

Interactions: deployment automation on [ha-and-deployment.md](ha-and-deployment.md).

### TOK-042 · Pairing tokens
Priority: **Later**

A pairing token bootstraps trust between two services or instances for one named purpose. It is
an access token with deliberately limited reach: short-lived, revocable, and meant to be used at
most once (it is revoked after the first pairing). The receiving side exchanges it for a
long-lived trust token, then the two sides communicate with ordinary token authentication.

Token content:

- Subject: the principal that requested the pairing token.
- Extension data: the base URL of the issuer (mandatory); the exchange URL (mandatory, trusted
  because the token is signed); a pairing URL (optional, used to establish trust in both
  directions).

Purposes, split between two services:

| Issued by | Purpose |
|---|---|
| Access-management service | Registering an instance or edge instance with a central multi-instance management service |
| Access-management service | Pairing the two ends of an outbound tunnel that links a hosted instance with a self-managed instance behind a firewall; only one pairing may run per tunnel at a time (a second attempt answers "conflict") |
| Access-management service | Entity federation from a controller instance to follower instances; also used to rotate a follower's pairing token |
| Repository service | Federated repositories with trust in both directions |
| Repository service | Federated repositories with one-way, receive-only trust |
| Repository service | Linking an instance to an archive (cold-storage) instance, either as a whole or for one namespace |

| Setting | Meaning | Default |
|---|---|---|
| Pairing-token lifetime | Validity of a new pairing token | 300 s (5 minutes) |

Operations:

- Create a pairing token for a purpose (administrator). The purpose is part of the request
  address. Output: the pairing token (a signed token). An unknown or unavailable purpose answers
  "not found". Which purposes are offered depends on the services present. The repository service
  has its own create operation for its purposes, with the same behavior.
- Exchange (repository service): input the purpose and the pairing token (required); output a
  status message. Responses: success, invalid request, unauthenticated, forbidden, unknown purpose
  ("not found"), server error.
- Unregister (repository service): cancels the trust with one service. Inputs: the purpose, the
  base URL of the peer whose trust ends (required) and a force flag (default: false); output a
  status message. Same responses as the exchange. What the force flag skips is unspecified.
- Required privilege of the repository-service exchange and unregister operations is
  unspecified; they accept Basic credentials, bearer tokens and the legacy API-key header
  (TOK-044).

Trust token: the result of a successful pairing. It grants the requesting service everything the
purpose requires on the issuing service, is typically powerful and long-lived, and an
administrator ends the trust by revoking it (TOK-030).

UI: token management screen, pairing-token tab: choose the purpose, generate; the window shows the
lifetime in seconds, the token ID and the token with a copy action.

Pairing tokens replace a shared secret for links across deployments; inside one deployment the
shared inter-service secret remains (TOK-043).

Interactions: [replication-and-federation.md](replication-and-federation.md),
[distribution-and-lifecycle.md](distribution-and-lifecycle.md),
[retention-and-cleanup.md](retention-and-cleanup.md) (archive).

### TOK-043 · Shared inter-service secret
Priority: **Out of scope**

In a deployment made of several cooperating services, the services trust each other through one
shared secret: each signs its service tokens with HMAC-SHA256 (JWS HS256) using that secret.
Every service must hold the identical value; with a mismatch, calls between them fail. The secret
is generated on first start unless the operator provides one (example: 32 random bytes,
hex-encoded), is viewable in the UI after re-entering the password, and can be rotated by
distributing a new value to all services within one maintenance window. Open: the mechanism is
either HMAC-SHA256 signing, as above, or symmetric encryption with a 128- or 256-bit AES key; the
spec decides. Reason for the priority: trove is a single binary whose replicas coordinate through PostgreSQL only, so there is no
service-to-service hop to protect.

### TOK-048 · Lifetimes and cleanup of internal service credentials
Priority: **Out of scope**

Timings for the credentials that the services of one deployment issue to and hold for each other:

| Setting | Meaning | Default |
|---|---|---|
| Default lifetime of internal tokens | Lifetime applied when an internal token is requested without one; must be positive | 3,600 s |
| Event-client token lifetime | Lifetime of the tokens generated for the internal event-publishing client; allowed range 60 s to 604,800 s (7 days) | 600 s |
| Stale-service check interval | How often a job looks for registered services that stopped sending heartbeats | 3,600 s |
| Stale-service age | Time since the last heartbeat after which a registered service counts as stale | 604,800 s (7 days) |
| Excluded service types | Service types the stale-service job skips | the central multi-instance management service |

The stale-service job is named after revoking administrator credentials of stale services; what
it revokes beyond what that name says is unspecified. Reason for the priority: as for TOK-043,
trove has no separate services that hold credentials for each other.

## Legacy and hosted credentials

### TOK-044 · Legacy API keys
Priority: **Later**

The predecessor of identity and reference tokens: one secret per user, created in the profile
after unlocking it, presented either in a dedicated request header or as the password in
Basic authentication. The reference token is its replacement; reference tokens are also accepted
in that header.

| | Legacy API key | Reference token (TOK-004) |
|---|---|---|
| Per user | at most one, shared by all of a user's clients | any number |
| Expiry | none | default and maximum expiry apply |
| Storage | retrievable through UI and API | hashed, not retrievable |
| Administrator oversight | none | listing and revocation |
| Length | 73 characters with a fixed prefix | 64 characters with a fixed prefix |

Operations (all deprecated):

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Create own key | – | the key; error if one already exists | authenticated user |
| Regenerate own key | – | the new key | authenticated user |
| Get own key | – | the key; not found if none | authenticated user |
| Revoke own key, or all keys | flag selecting "all" | confirmation | user; administrator for "all" |
| Revoke another user's key | username | confirmation; not found for unknown user | administrator |

Deprecation state: declared end of life; creation is blocked by default in current releases; key
usage can additionally be blocked by a switch (default off). Administrators find remaining users
through the authentication method recorded in login log entries. Keys generated by very old
releases must be regenerated before they work as Basic passwords. Profile UI and switches:
AUTHN-043 in [authentication-providers.md](authentication-providers.md).

### TOK-045 · Hosted account-portal token
Priority: **Out of scope**

The operator-run account portal of a hosted service issues one API token for managing the hosted
environment (custom domains, private connectivity, IP allowlists, server topology). Only the
primary administrator can generate or regenerate it; the other administrators and technical
members of the account can view it. It expires
after one year; regenerating it ends the previous token immediately; expiry notices go out 30, 10,
5 and 1 days before and on the day of expiry. Without meaning for a self-hosted system.

### TOK-046 · Deprecated token operations of the repository service
Priority: **Later**

Before token management moved to the access-management service, the repository service had token
operations of its own. Two of them still exist, marked deprecated with a removal date;
self-managed releases first turn them off behind a switch for a 90-day grace period and remove
them in a later release.

- List tokens (administrator): every token the instance issued, with token ID, issuer (the issuing
  instance's service identifier, TOK-016), subject, issued-at and expiry (epoch seconds) and the
  refreshable flag. No filtering. Subjects may appear in an older format,
  `<service-id>/users/<username>`.
- Revoke (administrator, form-encoded input): either the token value or the token ID. Answers
  success also when the token was already revoked or never existed, and "invalid request" when
  another instance issued the token, which cannot be revoked here. It accepted the token-type
  hint of [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009); the replacements have no
  equivalent.

Replacements: list (TOK-027), revoke by ID (TOK-030) and revoke by value (TOK-031). Unlike the old
revoke, the replacements answer "not found" for unknown tokens. An older create call on the
repository service also exists; its behavior is unspecified.

### TOK-047 · Token operations of the distribution service
Priority: **Later**

The service that distributes release sets to edge instances
([distribution-and-lifecycle.md](distribution-and-lifecycle.md)) exposes token operations of its
own in OAuth 2.0 style, all with form-encoded input:

- Create or refresh, selected by the grant type (default: create).
  - Create: the username (required), optionally scope, lifetime and refreshable flag. Only two
    scope values exist, administrator and groups. Users without administrator rights may create
    tokens only for themselves and never with the administrator scope. A lifetime of zero, or
    none, means the default lifetime; in TOK-025 zero means non-expiring instead.
  - Refresh: the refresh token and the current access token (both required), optionally a new
    lifetime and the refreshable flag.
  - Answers 201 for a new token and 200 for a refresh; the response carries the access token,
    lifetime, granted scope, token type and, when refreshable, the refresh token. Refused
    creations answer "forbidden"; bad parameters or grant type "invalid request".
- List (administrator): token ID, issuer, subject, issued-at, expiry and refreshable flag.
- Revoke (administrator), modeled on [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009): either a
  token value, which may be an access token or a refresh token, or a token ID. Sending both is an
  invalid request; an unknown token answers "not found".

## Notes for implementers

- Clients rely on the reference form: package managers that only speak Basic, or that choke on
  long secrets, need a short token usable as a password with the subject username (TOK-004,
  TOK-034).
- A token's effective rights are evaluated at use time: user-permissions and project-role scopes
  follow later changes to the user, groups and roles, and transient subjects pick up the local
  auto-join groups, which are not in the token (TOK-008, TOK-011, TOK-015).
- Default auto-join groups silently widen every CI token minted with a username; group-only
  tokens avoid that (TOK-015).
- With self-contained tokens, short-lived tokens are neither stored nor revocable (TOK-020,
  TOK-021); an implementation that stores and checks every token must still accept the same
  creation inputs and lifetimes.
- Lifetime zero means non-expiring, not "use the default"; an omitted lifetime means the default
  (TOK-017, TOK-018). The default itself is open (one hour versus one year). The
  distribution service's own token operation reads zero as "use the default" (TOK-047), so the
  meaning of zero has to be fixed per interface.
- Revocation and caching interact: validation results are cached for up to 60 s by default, and
  whether a revocation clears the cache is unspecified (TOK-032). An implementation has
  to decide and document its revocation latency.
- The reserved "current token" alias on the get and revoke operations lets clients inspect or
  revoke their own credential without knowing its ID (TOK-028, TOK-030).
- Revocation by subject matches on the name only; tokens of distinct users sharing a name go
  together (TOK-032).
- Refresh must be rejected for a user that no longer exists or is disabled, otherwise refreshable
  tokens outlive de-provisioning (TOK-024, AUTHN-030).
- Rotating the signing key pair is the designated "revoke everything" switch; the at-rest
  encryption key is unrelated to token validation, so rotating it neither helps nor is needed
  after a token compromise (TOK-038).
- The bootstrap administrator token and its request file are secrets on disk; they are deleted
  after one minute and the token expires after 15 minutes (TOK-041).
