# Events and extensibility

This page catalogs how a full-featured binary repository manager tells other systems what
happened and how it lets operators change its behavior without changing its code: the event
catalog and webhook subscriptions (standard and templated payloads, signing, secrets, proxies,
test delivery, delivery log), server-side scripting hooks that run synchronously inside upload,
download, storage, repository, token and replication flows (plus scheduled and HTTP-triggered
scripts), the legacy in-process extension scripts those hooks replace, the machine-readable API
description, the GraphQL query layer, the tool server for AI assistants, client SDKs, and chat and
incident-tool integrations. Operation catalogs for the same subsystems live in
[api-content-operations.md](api-content-operations.md) and
[api-admin-operations.md](api-admin-operations.md); roles that may manage webhooks are defined in
[identity-and-access.md](identity-and-access.md).

## Event model

### EVT-001 · Asynchronous event dispatch to webhooks
Priority: **P1**

When something happens in the system, such as an artifact being deployed, a build being uploaded
or a property being removed, the system emits an event. A dedicated event dispatcher matches the
event against all enabled webhook subscriptions (EVT-018) and sends one outbound HTTP request per
matching subscription. Delivery is asynchronous: the user action that caused the event is neither
delayed nor failed by the webhook, and the webhook cannot change its outcome. This is the main
difference from scripting hooks (EVT-038), which run synchronously and can block or modify an
operation.

| Property | Webhooks | Scripting hooks |
|---|---|---|
| Purpose | Notification, auditing, triggering external automation | Controlling and customizing the operation itself |
| Timing | Asynchronous | Synchronous |
| Effect on the triggering request | None | Can allow, stop, warn or modify; adds latency |
| Where the logic runs | On the receiver's infrastructure | Inside the repository manager |
| Work per event | Exactly one HTTP request | A script that may compute and send several requests |

Limits and edge cases:

- Retry policy (count, backoff) for failed deliveries: unspecified. Delivery records carry a
  "retries attempted" counter (EVT-032), so retries exist, but their rules are unspecified.
- Request timeout for a delivery: unspecified.
- Ordering and exactly-once guarantees: unspecified. Scan status events explicitly allow the
  same occurrence to be delivered more than once (EVT-015).
- Only enabled subscriptions receive events; a disabled subscription keeps its configuration.

Interactions: subscription model EVT-018; delivery log EVT-032; hooks EVT-038. An HTTP-triggered
hook can be called from a templated webhook to react to events that have no hook of their own
(EVT-051).

### EVT-002 · Common event envelope
Priority: **P1**

Every event of the repository-centric domains is delivered in one JSON envelope with these
parts:

| Part | Meaning |
|---|---|
| Domain | The subsystem that emitted the event (artifact, artifact property, container image, build, release set, distribution, user, scan status, application lifecycle, and so on). |
| Event type | The event name within the domain. |
| Event data | A domain-specific object; its contents are listed per event in EVT-004 to EVT-016. |
| Subscription key | The key of the subscription that matched (EVT-018). |
| Origin | The base URL of the instance that produced the event. |
| Source | An identifier of the emitting service instance. |
| Actor context | Who caused the event (EVT-003). |

Edge cases:

- Open: upstream curation events (EVT-014) either use the envelope or arrive as a flat domain
  object without it, and application lifecycle events (EVT-016) either carry the origin URL or
  omit it; the spec decides.
- The standard handler sends the envelope as the request body with a JSON content type (the
  delivery record shows that header). Templated handlers send whatever their template renders
  (EVT-025).

### EVT-003 · Actor context in events
Priority: **P1**

Each event carries an actor object identifying who triggered the underlying action, for audit and
for actor-aware integrations (for example, "who deployed this artifact"):

| Field (descriptive) | Meaning |
|---|---|
| Identity | The username; for token-authenticated requests, the token subject. |
| Token flag | True when the request was authenticated with an access token, false for a direct user login. |
| Realm | Which authentication realm admitted the user (internal, LDAP and so on). |

The same three facts are available to payload templates (EVT-026) and to scripting hooks, whose
request payloads carry the same actor object (EVT-053).

## Event catalog

### EVT-004 · Artifact events
Priority: **P1**

| Event | Fires when | Filter applies to |
|---|---|---|
| Deployed | An artifact is deployed to a repository. | Target repository and path |
| Deleted | An artifact is deleted from a repository. | Repository and path |
| Moved | An artifact is moved to another repository. | The source repository |
| Copied | An artifact is copied to another repository. | The source repository |
| Cached | An artifact is fetched from upstream and stored in a remote repository's cache, for example on `npm install`; pull replication into a remote repository also triggers it. | The remote repository |

Event data: repository key, path within the repository, file name, SHA-256 checksum, size in
bytes. Moved and copied add the source and the target location.

Edge cases:

- Open: the "source" and "target" fields of move and copy events carry either bare repository
  keys or full repository paths; the spec decides.
- Folder operations (moving or deleting a folder): whether one event per contained file or one
  event per folder is emitted is unspecified.

Interactions: filter EVT-019; remote caching in [remote-repositories.md](remote-repositories.md)
(RMT-001); deploy, move, copy and delete operations in [artifacts.md](artifacts.md); per-user
email notifications on storage changes are a separate mechanism (ART-046).

### EVT-005 · Artifact property events
Priority: **P1**

| Event | Fires when |
|---|---|
| Property added | A property is set on a file, a folder or the repository root. |
| Property deleted | A property is removed from a file, a folder or the repository root. |

Event data: everything from EVT-004 (repository key, path, name, SHA-256, size) plus the
property key and its list of values.

Edge cases: when a property with several values is set in one call, the event carries one key
and all its values; whether setting several keys in one call yields one event per key is
unspecified. Property operations: ART-021 to ART-024 in [artifacts.md](artifacts.md).

### EVT-006 · Container image tag events
Priority: **P1**

Events of Docker and OCI repositories, at tag level:

| Event | Fires when | Filter applies to |
|---|---|---|
| Tag pushed | A Docker or OCI image is pushed under a tag that did not exist before. | Target repository and path |
| Tag deleted | A tag is deleted. | Repository and path |
| Tag promoted | A tag is promoted to another repository. | The source repository |

Event data: repository key, path of the image manifest file, file name (the manifest), SHA-256,
size, image name, tag, list of platforms (architecture and operating system per entry), and the
image kind (Docker or OCI). Promotion adds the target repository and the target tag.

Interactions: container image promotion is part of the build and promotion features in
[builds-and-ci.md](builds-and-ci.md); format behavior in [formats/README.md](formats/README.md).

### EVT-007 · Build events
Priority: **P1**

| Event | Fires when |
|---|---|
| Build uploaded | Build metadata for a new build run is uploaded. |
| Build deleted | A build run is deleted. |
| Build promoted | A build run is promoted. |

Event data: build name, build number, build start timestamp, key of the repository that stores
build metadata.

Scope rules when projects are used (EVT-022):

- A subscription created in the global scope receives events for every build in the system
  matching its name or pattern, including builds that belong to projects; "any build" in the
  global scope means every build in the system.
- A subscription created inside a project receives events only for that project's builds.

Interactions: build metadata, promotion and retention in [builds-and-ci.md](builds-and-ci.md);
build filter EVT-020.

### EVT-008 · User lockout event
Priority: **P2**

Fires every time a user account is locked after the configured number of failed logins. There is
no filter for this domain; every lockout is delivered.

Event data: the user's administrator flag, UI-access-disabled flag, email address, group names,
internal-password-disabled flag, last login timestamp, profile-updatable flag, realm, status
(locked) and username.

Interactions: account lockout itself is IAM-042 in [identity-and-access.md](identity-and-access.md).

### EVT-009 · First-generation release set events
Priority: **Later**

| Event | Fires when |
|---|---|
| Created | A release set (a signed, immutable collection of artifacts prepared for distribution) is created. |
| Signed | A release set is signed. |
| Deleted | A release set is deleted. |

Event data: release set name, version and size. Filter: release set names or name patterns.

Interactions: release sets in [distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### EVT-010 · Release set creation events
Priority: **Later**

For the current generation of release sets, three events report the creation process: creation
started, creation failed, creation completed.

Event data: key of the repository that stores the release set, release set name, version,
creation timestamp.

Filter: all release sets, or selected ones (a map from release set name to selected versions,
and a list of registered names); optionally scoped to a project. Only a system administrator can
create subscriptions for release sets inside a specific project; project administrators cannot.

### EVT-011 · Release set promotion events
Priority: **Later**

Three events report promoting a release set version to a lifecycle environment: promotion
started, promotion failed, promotion completed.

Event data: as EVT-010 plus the target environment, and the creation time as both a timestamp
and milliseconds since the epoch.

Filter: the target environments are mandatory; the subscription fires only for promotions into
the selected environments. Project restriction as in EVT-010. Lifecycle environments:
[distribution-and-lifecycle.md](distribution-and-lifecycle.md).

### EVT-012 · Distribution events
Priority: **Later**

Events on the distributing side when a release set version is sent to, or removed from, one or
more edge instances:

| Event | Fires when |
|---|---|
| Distribution started / completed / aborted / failed | The distribution of a release set version changes state. |
| Deletion started / completed / failed | Removal of a release set version from edge instances changes state. |

Event data: release set name, version and size; the list of edge instances involved (name and
address per instance); a status message; a numeric transaction identifier.

Filter: release set names or patterns, and the edge instances involved. Edge case: the names of
the deletion events are not settled; an implementation must pick one stable name per event and
use it both as the display name and as the payload identifier (see Notes).

### EVT-013 · Distribution destination events
Priority: **Later**

Events on an edge instance when its copy of a release set changes: received, deletion started,
deletion completed, deletion failed. Event data: release set name, version, status message.
Filter: as EVT-012.

### EVT-014 · Upstream curation events
Priority: **Later**

| Event | Fires when | Event data |
|---|---|---|
| Package blocked | Curation refuses a package requested through a remote repository. | Package type, name, version and download URL; block reason; curated repository and its project; requesting user's name and email; origin repository, server and project; public registry URL and name; the matched policies (name, id, dry-run flag, result, condition name, condition category); an event id. |
| Waiver request created | A user asks for an exception to a blocking policy. | The request: id, creation and closing time, repository, package type, name, version, status, decision owners (group names), requesters (user, email, time, justification), affected policies (id, name, scope, action, condition id, name and category, whether the caller can approve, package types, decision owners); a link to the package page. |
| Waiver request updated | A waiver request is decided or changed. | As above, plus the decision (who, when, justification, status) and the lists of decided and still-pending policies. |
| Policy changed | A curation policy or its condition is modified. | A change kind plus the full policy before and after: id, audit fields, enabled flag, name, scope, action, condition (id, custom flag, risk type, supported package types, template id, name, parameter values), waivers, waiver-request setting. |

There is no filter for this domain; every curation event of the instance is delivered.
Interactions: upstream curation in
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### EVT-015 · Scan status events
Priority: **Later**

Fires when the vulnerability scan of a resource reaches a terminal overall status. The event
type mirrors the status: done, failed, partial, not supported. The subscription selects one or
more of these outcomes; project-scoped subscriptions are supported.

Event data:

- Resource coordinates: resource kind (artifact, build, release set of either generation,
  application version) with the identifying fields for that kind, for example repository, path
  and name for an artifact, or name, version, project and repository for a release set.
- Overall status: terminal status, last update time, optional error message.
- Per-service details: status, update time and optional error for software composition
  analysis, policy violations, exposures (with per-category sub-statuses when present) and
  contextual analysis.
- Time at which the terminal status was reached (RFC 3339).

Edge case: the same resource can produce more than one event while its overall status stays
terminal, for example when one scan part finishes late. Consumers must treat events as
notifications and de-duplicate. These events are distinct from the scanner's own policy
violation notifications. Interactions:
[security-scanning-and-curation.md](security-scanning-and-curation.md).

### EVT-016 · Application lifecycle events
Priority: **Later**

Events of an application-governance layer that groups builds and packages into applications
and versions and moves versions through stages guarded by entry and exit gates.

| Category | Events | Event data |
|---|---|---|
| Gate evaluation | Entry gate evaluation started / passed / failed; exit gate evaluation started / passed / failed | Application key, application version, stage, evaluation id; passed and failed add the decision and an explanation text. |
| Application | Application creation, update and deletion, each started / completed / failed | Application key, project key, display name, description, criticality, maturity level, labels (key/value), owners (name and kind), timestamp in milliseconds. |
| Application version | Version creation started / completed / failed; promotion started / completed / failed; release started / completed / failed | Application key and version; promotion and release add the stage. |

Filter (all optional, combined with AND): project key (exact match), list of application keys,
list of stages. Setting a stage list suppresses all application-category events, because they
carry no stage; to receive both, create two subscriptions.

### EVT-017 · Event catalog discovery
Priority: **P1**

The system can list its event catalog: every domain with the event types it offers. The UI's
event picker shows the same catalog (hover over a domain, tick the events), and the AI tool
server exposes it as a tool (EVT-079); the REST API has no operation for it.
The catalog is the same regardless of the project context; a project parameter only affects
authorization.

Unspecified: whether the catalog also states which filter shape each domain takes and which
event types support project scope (EVT-022).

## Webhook subscriptions

### EVT-018 · Webhook subscription
Priority: **P1**

A webhook subscription binds one event domain, a set of event types in that domain, an optional
domain-specific filter and exactly one handler (the outbound request). A subscription covers one
domain only; subscribing to artifact and build events needs two subscriptions.

<details>
<summary>Subscription settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Key | Unique identifier, also the address of the subscription in the API. Starts with a letter, continues with letters, digits, underscore or hyphen; at least two characters, at most 500. Fixed after creation. | required |
| Description | Free text. | empty |
| Enabled | Whether the subscription receives events. The create operation lists it as a required field. | schema default off; the AI tool server's create tool also defaults to off; the UI default is unspecified |
| Domain | The event domain (EVT-004 to EVT-016). | required |
| Event types | At least one event type of the domain. | required |
| Filter criteria | Domain-specific narrowing (EVT-019 to EVT-021). | depends on the domain |
| Project | Project the subscription belongs to (EVT-022). Fixed after creation. | none (global) |
| Handler | Exactly one handler, standard (EVT-023) or templated (EVT-025). | required |
| Record successful deliveries | Also store successful deliveries in the delivery log; failed deliveries are always stored (EVT-032). | off |

</details>

Operations:

<details>
<summary>Subscription operations</summary>

| Operation | Inputs | Output | Required privilege (EVT-030) |
|---|---|---|---|
| List subscriptions | none | Array of subscriptions the caller can read | Webhook read |
| Get subscription | Key | The subscription; 404 if unknown | Webhook read |
| Create subscription | Full definition | 201 with the stored subscription; 400 on invalid input | Webhook write |
| Replace subscription | Key, full definition without the key | 204; 404 if unknown | Webhook write |
| Delete subscription | Key | 204; 404 if unknown | Webhook delete |
| Test a definition | Full definition (same shape as create) | 200 when the test request succeeded (EVT-031) | Webhook execute |
| Set one secret value | Key, secret name, new value as plain text | unspecified | Webhook write |
| List event domains and types | Optional project (authorization context only) | The event catalog (EVT-017); available only as an AI tool server tool | unspecified |
| Read or stream delivery records | Filters (EVT-033) | Delivery records | Webhook read |

</details>

Replace semantics: the update is a full replacement of description, enabled flag, filter,
handler and recording flag. The project, if sent, must equal the stored project. For a templated
handler, the secret list on update is the complete list: a secret whose name is missing is
removed, a secret sent without a value keeps its stored value, a secret sent with a value is
added or rotated, and a secret sent with an empty value is wiped. For a standard handler, reads
return the signing secret as encrypted ciphertext; sending that ciphertext back unchanged keeps
the secret, sending a new value rotates it.

Edge cases:

- Read responses never contain secret values in clear text (EVT-027).
- The AI tool server's create and update tools offer an option to "skip handler URL
  verification only", which implies that the server verifies the target URL when a
  subscription is stored. What the verification does is unspecified.

UI location: the integrations area, webhooks page (EVT-034).

### EVT-019 · Repository and path filter
Priority: **P1**

Applies to the artifact, artifact property and container image domains. The filter selects the
repositories and paths whose events trigger the subscription.

| Setting | Meaning | Default |
|---|---|---|
| Any local | Every local repository, including ones created later. | off |
| Any remote | Every remote repository, including ones created later. | off |
| Any federated | Every federated repository, including ones created later. | off |
| Repository keys | Explicitly selected repositories. | empty |
| Include patterns | Path patterns an event's path must match. | empty (all paths) |
| Exclude patterns | Path patterns that suppress an event. | empty |

Behavior: repository keys and the "any" switches combine with OR. Include and exclude patterns
then narrow by path. In the UI the user either picks repositories from a list or enters
include/exclude patterns. The pattern syntax for webhooks is unspecified; the scripting hook
filter, which has the same shape, uses Ant-style patterns (`?`, `*`, `**`, EVT-041).

Edge cases: for moved, copied and promoted events the filter is matched against the source
repository (EVT-004, EVT-006). A webhook-manager role may be limited in which of these settings
it may use (IAM-007 in [identity-and-access.md](identity-and-access.md)).

### EVT-020 · Build filter
Priority: **P1**

Applies to the build domain.

| Setting | Meaning | Default |
|---|---|---|
| Any build | Every build, including builds uploaded later. | off |
| Selected builds | Explicit build names. | empty |
| Include patterns / exclude patterns | Build name patterns. | empty |

Scope interaction with projects: EVT-007.

### EVT-021 · Release, environment and application filters
Priority: **Later**

Filter shapes of the remaining domains:

| Domain | Filter |
|---|---|
| First-generation release sets | Any release set, or names and name patterns. |
| Current-generation release sets | Any release set, or a map of release set name to selected versions; optional project. |
| Release set promotion | Selected target environments (mandatory). |
| Distribution and destination | Release set names or patterns, and the edge instances involved. |
| Upstream curation, user lockout | No filter; everything is delivered. |
| Scan status | Selected terminal outcomes; optional project. |
| Application lifecycle | Project key (exact), application keys, stages (EVT-016). |

The filter object accepts additional, domain-specific members beyond the ones listed here.

### EVT-022 · Project-scoped subscriptions
Priority: **P2**

A subscription can belong to a project. Inside a project:

- Only the artifact, container image, artifact property and build domains are offered in the
  UI. Release set subscriptions also accept a project, but only system administrators may
  create them (EVT-010, EVT-011); scan status subscriptions support project scope (EVT-015).
- Repository selection covers the project's repositories; build events cover only the project's
  builds (EVT-007).
- The project of a subscription cannot be changed after creation.

The UI lists webhooks either for all projects (with a project column) or for the selected
project. Open: a project is either required whenever all chosen event types support project
scope, or allowed only in that case; the spec decides.

Interactions: projects in [identity-and-access.md](identity-and-access.md).

### EVT-023 · Standard webhook handler
Priority: **P1**

A handler with a fixed payload: the event envelope (EVT-002) is sent unchanged as an HTTP POST
to the target URL. It cannot be adapted to a specific receiver, but the target URL and extra
headers can be chosen.

| Setting | Meaning | Default |
|---|---|---|
| Target URL | Absolute URL that receives the POST. | required |
| Proxy | Outbound proxy to use, referenced by its configured key, not by URL (EVT-028). | none |
| Secret | Shared secret, sent or used for signing (EVT-024). | none |
| Sign with secret | Use the secret to sign the payload instead of sending it. | off |
| Custom headers | List of name/value pairs added to the request. | empty |

### EVT-024 · Payload signing and shared-secret header
Priority: **P1**

For standard handlers (EVT-023) the optional secret authenticates deliveries in one of two ways:

- **Signing on.** The secret is not transmitted. The system computes an HMAC with SHA-256 over
  the exact payload bytes, keyed with the secret, so the receiver can verify origin and
  integrity. The receiver verifies by computing a hexadecimal HMAC-SHA256 digest of the raw
  body and comparing it with the received value.
- **Signing off.** The secret itself is sent in a dedicated request header on every delivery.

Unspecified: the header that carries the signature, any prefix on the signature value, and
whether a timestamp is included to prevent replay. trove must define its own header names.

Security note: with signing off, the secret travels in clear text inside the TLS connection on
every request; signing should be preferred.

### EVT-025 · Templated webhook handler
Priority: **P1**

A handler whose request is fully shaped by the user, so it can call any receiver's own API
(GitHub Actions, GitLab pipeline triggers, Jenkins jobs, Slack, Microsoft Teams, Jira and
others; recipes EVT-035, EVT-036).

| Setting | Meaning | Default |
|---|---|---|
| Target URL | Absolute URL of the receiver. | required |
| HTTP method | One of POST, PUT, GET, PATCH, DELETE. | POST |
| Proxy | As EVT-023. | none |
| Headers | Name/value pairs; values may contain template expressions, typically to inject a secret (`Authorization: Bearer <secret>`). | empty |
| Payload template | Text template rendered per event into the request body (EVT-026). The body need not be JSON; form-encoded bodies are possible. | empty |
| Secrets | Named secrets usable in headers and payload (EVT-027). | empty |

Edge case: whether template expressions are evaluated inside the target URL is unspecified.

### EVT-026 · Payload template language
Priority: **P1**

Templated handlers use a deliberately small subset of Go's `text/template` syntax:

| Construct | Effect |
|---|---|
| `{{ .<path> }}` | Renders the scalar at the dotted path as text. |
| `{{ json .<path> }}` | Serializes the object at the path as JSON. |
| `{{ yaml .<path> }}` | Serializes the object at the path as YAML. |

Available roots:

| Root (descriptive) | Content |
|---|---|
| Event data | The event's data object (EVT-004 to EVT-016). |
| Secrets | The handler's named secrets (EVT-027). |
| Actor | The actor context (EVT-003). |

Edge cases:

- Beyond these three roots, templates also read top-level envelope members, the event type and
  the origin instance URL. The template root is therefore the whole envelope (EVT-002) plus the
  secrets.
- Open: other functions, conditionals and loops are either rejected or supported; the spec
  decides.
- A scalar is rendered without escaping. A value inserted into a JSON string literal is not
  JSON-escaped unless the `json` function is used; template authors are responsible for valid
  output.
- Behavior when a path does not exist: unspecified.

### EVT-027 · Handler secrets
Priority: **P1**

Templated handlers carry a list of named secrets (tokens, passwords) for use in headers and
payload.

- A secret name starts with a letter or underscore and continues with letters, digits or
  underscores.
- Values are stored encrypted. They are write-only: reads return names only.
- A single secret value can be replaced without resending the whole subscription (operation
  "set one secret value" in EVT-018), the intended path for rotating an expired token or a
  changed password.
- Full-replacement semantics for the secret list: EVT-018.

Interactions: encryption of stored configuration secrets, IAM-045 in
[identity-and-access.md](identity-and-access.md).

### EVT-028 · Outbound proxy for delivery
Priority: **P1**

A handler can route its request through one of the outbound proxies configured in the system,
selected by proxy key. The setting exists only for self-hosted installations; a hosted
deployment omits it. Proxy definitions are shared with remote repositories
([remote-repositories.md](remote-repositories.md)).

### EVT-029 · Private-network target protection
Priority: **P1**

By default the system refuses webhook targets in private networks, to prevent the webhook
feature from being used to reach internal services (server-side request forgery). An
installation-level switch allows private targets.

Edge cases: open whether the switch, when on, allows private targets or blocks them (opposite
polarity); the spec decides. The exact semantics (which ranges count as private, whether
link-local and loopback are included, whether names are re-resolved at delivery time) are
unspecified. The scripting hook runtime has a comparable guard for its outbound HTTP client
(EVT-053).

### EVT-030 · Webhook management privileges
Priority: **P1**

| Principal | Allowed |
|---|---|
| System administrator | Everything. |
| Project administrator | List, read, create, update, delete, test, read delivery log; creates subscriptions only for the artifact, artifact property, container image and build domains. |
| User with the webhook-manager role | Manage webhooks within the limits of IAM-007 in [identity-and-access.md](identity-and-access.md). |
| Holder of a scoped access token | A token scope dedicated to webhooks with four actions: read (list, get, delivery log), write (create, replace, set secret), delete, execute (test). The scope can be bound to a project. |

These roles let non-administrators manage webhooks; without them, webhook management is limited
to system administrators. Interactions: token scopes in
[access-tokens.md](access-tokens.md).

### EVT-031 · Test delivery
Priority: **P1**

From the create/edit dialog (Test button) or through the API, the system sends a dummy event to
the configured target to check connectivity and receiver configuration. The test operation
accepts a complete, not yet stored subscription definition, so a webhook can be tested before
it is saved. The recipe for calling an HTTP-triggered hook from a templated webhook (EVT-051)
uses this test to confirm the hook is reached, so headers built from secrets are rendered for
the test request.

Unspecified: the content of the dummy event data, whether the payload template is rendered
against it, and whether the test result (status code, response body) is returned to the caller
or only success/failure.

### EVT-032 · Delivery log
Priority: **P1**

The system stores one record per handled delivery (retries are counted inside that record, not
stored as separate records), so failed integrations can be diagnosed:

- Failed deliveries are always recorded.
- Successful deliveries are recorded only when the subscription's "record successful
  deliveries" switch is on.

<details>
<summary>Delivery record contents</summary>

| Part | Content |
|---|---|
| Timestamp | When handling of the event started (milliseconds since the epoch). |
| Elapsed time | Duration of the handling in milliseconds. |
| Errors | List of error messages, for example "unexpected HTTP response status". |
| Request | Method, URL, headers, body as sent, number of retries attempted. |
| Response | Status code, headers, body returned by the receiver. |
| Event | Event id, subscription key, domain, event type, raw event data, source. |

</details>

UI: a delivery log tab on the webhooks page shows the latest delivery per webhook; selecting a
webhook lists all its recorded events; the list can be sorted by project and filtered by status
(only statuses that occurred are offered); selecting an event opens a pane with request,
response, event and error tabs.

Security note: the stored request includes headers and body as sent; whether rendered secrets
(for example an `Authorization` header built from a secret) are masked in the record is
unspecified. trove must mask them.

### EVT-033 · Delivery log storage and streaming
Priority: **P1**

Storage (self-hosted): recording is off until configured. Two back ends:

| Back end | Behavior |
|---|---|
| Log file | One file per node with the failed deliveries handled by that node; log rotation settings are available (example values: compress, keep one file uncompressed, 25 MB per file, 365 days, 10 files). The UI tab and the API are not available with this back end. |
| Key-value stream store (Redis 7.2) | Shared store; enables the UI tab and the API. Keeps the newest 10,000 records; older records are trimmed every 30 seconds. Both numbers are configurable. |

In a hosted deployment, availability of the delivery log depends on the instance.

Read and stream operation (privilege: webhook read, EVT-030):

| Parameter | Meaning |
|---|---|
| Subscription | Only records of this subscription key. |
| Target | Only records whose target URL matches, for example a host name (match semantics unspecified). |
| Start | Records reported at or after this Unix timestamp; `0` means from the earliest stored record. |
| End | Records reported before this timestamp; only together with Start. |
| Count | Maximum number of records; only together with Start. |

Without Start, or with Start but neither End nor Count, the response is a live stream that
delivers new records as they arrive. The streaming wire format is unspecified.

Additional read functions exposed through the AI tool server (EVT-079): whether recording and
the data API are available, the latest attempt per subscription, and the number of records and
errors since a start position.

### EVT-034 · Webhook management UI
Priority: **P1**

In the integrations area, the webhooks page lists subscriptions for all projects (with a project
column) or for the selected project, with a delivery log tab (EVT-032). "New webhook" opens a
form with a standard/templated toggle:

1. Name and description.
2. Target URL; for templated handlers, the HTTP method.
3. Proxy (self-hosted only).
4. Whether to show successful deliveries in the delivery log.
5. Events: a picker grouped by domain; inside a project only the domains of EVT-022.
6. Resources: a dialog per domain (repositories by list or include/exclude patterns, builds by
   list or patterns, release sets, environments, applications and stages).
7. Secrets: for templated handlers, named secrets ("add secret"); for standard handlers, the
   signing secret and the signing switch.
8. Headers ("add header").
9. Payload template (templated only).
10. Test (EVT-031) and Create.

### EVT-035 · Recipes: triggering CI pipelines
Priority: **P1**

Templated webhooks (EVT-025) are the way to start CI pipelines from repository events. Worked
recipes for common receivers follow; the receiver-side requirements are those of the receiving
system:

| Receiver | Request shape |
|---|---|
| GitHub Actions | POST to the repository's workflow-dispatch (or repository-dispatch) endpoint; the workflow must declare `workflow_dispatch`, optionally with inputs filled from the event (for example the artifact's repository and path); JSON body with the git ref and inputs; `Authorization: Bearer` with a token that has `actions:write` on the repository; GitHub's JSON accept header. |
| GitLab | POST to the project's pipeline trigger endpoint with a form-encoded body containing the trigger token and pipeline variables (for example the artifact's SHA-256); a `PRIVATE-TOKEN` header from a secret. |
| Jenkins | POST to a parameterized job's build endpoint with a form-encoded body (job token and parameters) and HTTP Basic authentication from a secret. |
| Kubernetes deployment via GitHub | A repository-dispatch event whose client payload carries the pushed image tag (EVT-006), consumed by a deployment workflow. |
| Scripting hook | POST to the HTTP-triggered hook execution endpoint with a bearer token from a secret (EVT-051). |

Interactions: CI integrations in [builds-and-ci.md](builds-and-ci.md); GitLab CI is the owner's
primary CI system ([oidc-ci-integration.md](oidc-ci-integration.md)).

### EVT-036 · Recipes: chat and incident tools
Priority: **P2**

The same mechanism covers notifications to collaboration and incident tools and replaces
dedicated chat and issue-tracker applications (EVT-037, EVT-081):

| Receiver | Request shape |
|---|---|
| Slack | POST of a JSON message to an incoming-webhook URL. |
| Microsoft Teams | POST of an Adaptive Card message to the URL generated by a Teams Workflows "webhook request received" trigger; the URL itself is the secret. The older Office 365 connector webhooks are retired and must not be used. |
| Jira Software Cloud | POST to the builds API with build name, number and start time from a build event; requires a token from a Jira app registered as build-information provider (a personal API token does not work); issue keys cannot be derived from the event and must be fixed in the template. |
| PagerDuty | The PagerDuty service's integration URL as the target of a standard webhook (EVT-023); each repository, property, image, build or release set event becomes a change event on the service, grouped by PagerDuty into existing change events where applicable. Scan violations can likewise be sent as incidents through the scanner's own webhooks. |

### EVT-037 · Interactive chat application
Priority: **Out of scope**

A hosted chat application (Slack; a Microsoft Teams variant is retired) that connects
to an instance through a registered client integration (id and secret generated in the
instance's integration settings), lets administrators create notification rules from chat
(events by domain, repositories or builds, target channel, private channels and direct
messages), pause a rule while keeping its underlying webhook, delete a rule together with its
webhook, list rules, and act on scan violations from interactive cards; one instance per chat
workspace. It is built entirely on webhooks (EVT-018) plus an operator-run bot, which has no
meaning for a self-hosted product; EVT-036 covers the notifications.

### EVT-081 · Retired issue-tracker applications
Priority: **Out of scope**

Two hosted applications for Jira Software Cloud, both retired: one linked build runs to the Jira
issues they contained and showed each build's promotion status on the issue, so a team could
see where an issue stood in the release flow; the other streamed scan violations, for resources
picked from the scanner's monitoring rules, into Jira projects. Templated webhooks replace both
(the Jira recipe in EVT-036). Nothing of this is needed in a self-hosted product beyond EVT-025
and EVT-036.

## Server-side scripting hooks

### EVT-038 · Scripting hook service
Priority: **Later**

A built-in, sandboxed runtime executes short user-written TypeScript (or JavaScript) functions
inside the system's own flows. A hook can intercept an operation before it happens and allow,
stop or modify it, react after it happened, run on a schedule or be called over HTTP. It is the
successor of the in-process extension scripts (EVT-063) and the recommended extension
mechanism.

- Only enabled hooks run.
- Hooks for the artifact store, for token management and for runtime workload monitoring share
  one runtime.
- Hooks call the system's own API through a pre-authenticated client (EVT-053), so they need no
  stored credentials for internal calls.
- In self-hosted installations the hook service is a separate service deployed next to the
  repository manager.

Edge case: combining legacy extension scripts and hooks on the same repository can lead to
unexpected behavior; hooks should be used only on repositories without extension scripts.

Comparison with webhooks: EVT-001. Comparison with extension scripts: hooks use TypeScript, run
isolated per execution, have built-in error handling, logging and access control; extension
scripts use Groovy, run inside the server process and can change behavior at any point of the
lifecycle.

### EVT-039 · Hook kinds
Priority: **Later**

| Kind | Runs when | How it is selected |
|---|---|---|
| Event-driven | A selected lifecycle point is reached (EVT-043 to EVT-050). | The event (action) identifier. |
| HTTP-triggered | A client calls the execution endpoint (EVT-051). | A dedicated generic action. |
| Scheduled | A cron schedule fires (EVT-052). | A dedicated scheduled action. |

All three kinds use the same runtime and can be created with every management channel: UI,
REST API, AI tool server, local tooling and declarative infrastructure-as-code (EVT-062).

### EVT-040 · Hook definition
Priority: **Later**

<details>
<summary>Hook settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Key | Unique identifier and address of the hook. | required |
| Description | Free text. | empty |
| Enabled | Whether the hook runs. The create operation lists it as a required field. | schema default on; the AI tool server always creates hooks disabled |
| Source code | TypeScript or JavaScript, sent as plain text or as Base64 behind a fixed prefix marker; the UI always sends Base64. | required |
| Action | The event or kind the hook is bound to (EVT-039). | required |
| Application | The service that owns the action (artifact store, token management, runtime monitoring). Local tooling guesses it from the action when omitted and fails when nothing matches; the AI tool server requires it; the REST create schema does not list it. | derived |
| Filter criteria | Which occurrences trigger the hook (EVT-041). | none |
| Secrets | Name/value pairs, stored encrypted, never returned in clear text, masked in all logs; read in code by name. | empty |
| Shared | Lets users who are neither system nor project administrators execute the hook where the action allows it; supported by HTTP-triggered hooks. | off |
| Record successful runs | Also store successful executions in the run history; failures are always stored. | off |
| Project | Project the hook belongs to. | none |
| Properties | Key/value pairs (exposed only by the AI tool server; purpose unspecified). | empty |

</details>

<details>
<summary>Hook operations</summary>

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create | Definition; project key as query parameter for project administrators (must match the body) | 201 | System administrator, or project administrator of that project |
| List | none | Array of hook definitions | System administrator |
| Get | Key | Definition; 404 if unknown | System administrator |
| Update | Definition (key in the body) | 200; 404 if unknown | System administrator |
| Delete | Key | 204; 404 if unknown | System administrator |
| Test, ad hoc / for a hook | Payload (EVT-056) | Execution result | System administrator |
| Execute HTTP-triggered hook | Key, payload | Return value and execution status (EVT-051) | EVT-051 |
| Rerun | Key, history entry (EVT-057) | Execution result | As execute |
| Execution history | Filters (EVT-057) | Array of executions | System administrator |
| List actions / action metadata | Optional project | EVT-058 | System administrator; metadata also project administrator |
| Compiler configuration | Optional project | The TypeScript compiler settings used to transpile hooks | System or project administrator |
| Liveness / readiness | none | Plain text "OK" | none (unauthenticated) |

</details>

Edge cases: hook source size limit (EVT-055). Read responses include the source code. Whether
the update operation can change the action of an existing hook is unspecified.

### EVT-041 · Hook trigger filter
Priority: **Later**

The filter shape depends on the action; the action metadata (EVT-058) states which filter type
applies and whether a filter is mandatory. When a filter is mandatory and the hook is enabled,
the filter must be provided.

Repository/path filter (artifact and repository actions):

| Setting | Meaning | Default |
|---|---|---|
| Repository keys | Repositories whose events trigger the hook; at least one, unless an "any" switch is on. | empty |
| Include patterns | Paths that trigger the hook; empty means all paths. | empty |
| Exclude patterns | Paths that never trigger the hook; empty means none. | empty |
| Any local / any remote / any federated | All repositories of that class. | off |

Repository keys and "any" switches combine with inclusive OR. Patterns follow Ant-style
matching: `?` one character, `*` any characters within a path segment, `**` any number of
segments (examples: `com/t?st.zip`, `com/*.zip`, `com/**/test.zip`).

Schedule filter (scheduled hooks): cron expression and time zone (EVT-052).

Edge cases: a hook created inside a project can select only repositories that belong to the
project, not repositories shared with it. The UI requires repositories to be selected before an
event-driven hook can be enabled; whether the same requirement applies to scheduled and
HTTP-triggered hooks is unspecified. Repository configuration hooks require a repository filter
(EVT-048).

### EVT-042 · Hook decision protocol
Priority: **Later**

Blocking ("before") hooks return a decision and a message:

| Decision | Effect |
|---|---|
| Proceed | The operation continues. |
| Stop | The operation is refused; for downloads and uploads it is aborted. |
| Warn | The operation continues and the message is written to the system log as a warning. |
| Unspecified | The initial value of a new response; effect when returned is unspecified. |

- The message is mandatory in every response. On an execution error the hook service
  overwrites it with the error.
- Informational ("after") hooks return only a message; on error it is logged as a warning. The
  response never affects the operation.
- Some hooks return extra fields: a modified repository path (upload, download request, remote
  path), extra request headers for the upstream (remote info, remote download), alternate
  response content (EVT-044).
- When a hook fails (uncaught exception, timeout, resource limit), whether the operation
  proceeds or is refused is unspecified. For errors a hook catches itself, the usual pattern is
  that blocking hooks set "stop" in their error handler (for example before upload, before
  download, before create, before create token), and "warn" only when an internal call succeeds
  with an unexpected status.

### EVT-043 · Upload and storage pre-operation hooks
Priority: **Later**

Blocking hooks that run before a storage change:

| Hook | Runs before | Typical use |
|---|---|---|
| Before upload | An artifact upload is processed. Can redirect the upload by returning a modified repository path. | Enforce path conventions, divert uploads to a quarantine repository. |
| Before create | An item is created in storage. | Prevent duplicates or unwanted content. |
| Before delete | An item is deleted. | Protect items carrying a production marker. |
| Before move | An item is moved. | Protect production items. |
| Before copy | An item is copied. | Refuse copying deprecated items. |
| Before property create | A property is set. | Restrict who may set which property. |
| Before property delete | A property is removed. | Restrict removal of significant properties. |

Request payload contents (descriptive):

- Upload: target repository key and path, combined identifier, root and folder flags, content
  length, last-modified time, the trust-server-checksums flag, the base URL, a skip-indexing
  flag, a disable-redirect flag, repository class (numeric); request headers; actor context;
  the properties attached at deploy time.
- Create: the same path facts, repository class and last-modified time, plus the item (name,
  created and last-modified time) and the actor context. Unlike the upload payload, it does not
  carry content length, the trust-server-checksums flag, the base URL or the skip-indexing and
  disable-redirect flags.
- Delete: the item (path, name, created and last-modified time) and deletion flags: whether
  metadata recalculation is triggered, whether asynchronous deletion is allowed, whether the
  trash can is skipped, whether the deletion comes from garbage collection, whether it is part
  of a move.
- Move and copy: source metadata and item, target repository path, properties of the item.
- Property create/delete: the item, the property name and, for create, the values.

Edge case: the action enumeration of the REST create operation also lists a "before upload
request" action (the counterpart of the extension point in EVT-064) and an "after save" action;
their payloads and semantics are unspecified.

Interactions: deploy and storage operations in [artifacts.md](artifacts.md); trash can and
garbage collection in [storage-and-blobs.md](storage-and-blobs.md).

### EVT-044 · Download-flow hooks
Priority: **Later**

A download passes several interception points in this order:

| Stage | Hook | Capability |
|---|---|---|
| Initial request (after authentication and other request filters) | Before download request | Allow or stop the request; can return a modified repository path. |
| Remote repository, before resolution | Alternate remote path | Redirect the upstream fetch to another path in the same remote repository. |
| Remote repository, before availability check | Before remote info | Add request headers to the upstream metadata request; allow or stop. |
| Remote repository, resource found | Alternate all responses | Replace response headers, status code, message or content. |
| Remote repository, before fetch | Before remote download | Add request headers to the upstream download; allow or stop. |
| Remote repository, after fetch | After remote download | Notification (asynchronous); can update metadata. |
| Remote repository, content received | Alternate remote content | Replace the fetched content with an alternate stream. |
| Delivery | Before download | Last check before content is delivered; allow, warn or stop. |
| Failure | After download error | Customize the error returned to the client (status, message, content). |
| Delivery started | After download | Notification once delivery has started (not completed); cannot change the download. |

An "alternate response" hook is deprecated in favor of "alternate all responses".

Request payload contents for download hooks (descriptive): repository path; the original path
when a virtual repository resolved the request; file name; flags for HEAD-only, checksum
request, recursive request, directory request, metadata request and archive-entry request (with
the entry path); modification time, last-modified and if-modified-since values; base URL;
request URI; client address; whether HEAD is replaced by GET; repository class; request or
response headers; actor context; for "before download", the physical repository path where the
item was found.

Interactions: remote repository behavior in [remote-repositories.md](remote-repositories.md);
virtual resolution in [repositories.md](repositories.md). Performance note: calling external
services from a "before download" hook slows every download; the recommended
pattern is asynchronous scanning that sets a property, which the download hook then only reads.

### EVT-045 · Post-operation notification hooks
Priority: **Later**

Informational hooks after a successful storage change: after create (for example, add default
properties to every new artifact), after delete, after move, after copy (if the target folder
does not exist it is created first), after property create, after property delete. Payloads
mirror the corresponding "before" hook (EVT-043). The response is a mandatory message only.

### EVT-046 · Build-information hooks
Priority: **Later**

| Hook | Kind | Typical use |
|---|---|---|
| Before build information save | Blocking (EVT-042) | Require mandatory metadata before a build run is stored. |
| After build information save | Informational | Start a pipeline right after a build run is stored. |

Payload: the build run with name, number, start time, build agent and client agent, duration,
initiating principal and system principal, URL, parent build name and number, build-information
repository, modules (id; artifacts with name, type, SHA-1, SHA-256, MD5, remote path and
properties; dependencies with id, scopes and requesting module), release status and promotion
history (status, comment, repository, timestamp, user, CI user). Whether the "after" hook's
payload includes the artifact checksums is unspecified.

Interactions: build metadata in [builds-and-ci.md](builds-and-ci.md).

### EVT-047 · Replication hooks
Priority: **Later**

Five blocking hooks run before something is replicated to another instance, one each for a
file, a directory, a deletion, properties and download statistics. Stop skips the replication
of that item. Payload: repository path and
class, actor context, and the target (target instance URL and target repository key).

Interactions: replication in [replication-and-federation.md](replication-and-federation.md).

### EVT-048 · Repository configuration hooks
Priority: **Later**

| Hook | Kind | Payload |
|---|---|---|
| Before repository create | Blocking | Actor; repository key, class, package type, federated flag, project, shared-with-projects flag, replication flag, environments; trigger source (for example UI); the full configuration to be stored. |
| Before repository update | Blocking | As above, but instead of the full configuration a list of changed attributes with old and new value. |
| Before repository delete | Blocking | As above, without configuration or changes. |
| After repository create | Informational | As "before create", with the stored configuration; sensitive remote-repository fields (password, custom headers) masked. |
| After repository update | Informational | Changed attributes; sensitive values masked. |
| After repository delete | Informational | Repository facts plus the operation's status code and a flag for partial content deletion. |

Rules: a repository filter is mandatory for every one of these hooks, and repositories that the
system manages for itself never trigger them. In bulk
create, update and delete operations a stop for any repository aborts the whole batch. Typical
uses: naming conventions, allowed package types, mandatory project assignment, preventing a
change of package type, protecting repositories with replication.

Interactions: repository configuration in [repositories.md](repositories.md).

### EVT-049 · Token lifecycle hooks
Priority: **Later**

| Hook | Kind | Payload | Typical use |
|---|---|---|---|
| Before token create | Blocking | Requested token: subject, owner, scopes, audiences, lifetime in seconds, refreshable flag, extension data, description, whether a reference token is requested; actor. | Refuse administrator-scoped tokens with a lifetime above the default. |
| Before token revoke | Blocking | The token: id, subject, owner, scope, audience, expiry and creation time, kind (session or generic), username, description, project; actor. | Refuse revoking the last token marked as protected. |
| Before token expiry | Informational | List of tokens about to expire (fields as above); actor. | Notify owners before expiry. |

Edge cases: when one request revokes several tokens, the revoke hook runs once per token, so a
"last protected token" check can be defeated within one batch. The lead time of the expiry hook
is unspecified (notifying 24 hours ahead is a typical use case, not a setting). Open: whether an
"after create token" hook exists; the spec decides.

Interactions: [access-tokens.md](access-tokens.md).

### EVT-050 · Runtime workload hook
Priority: **Later**

An informational hook of a runtime workload monitoring module: fires when the state of a
deployed workload changes. Payload: change kind; workload (name, namespace, cluster, nodes,
risks, vulnerability count); image tags (name, registry, repository path, architecture, tag,
SHA-256, risks, vulnerabilities with identifiers, severity, CVSS scores and affected
components, malicious packages, deploying user, build information). The response carries a
continue flag. Use case: alert on vulnerable images running in a Kubernetes cluster.

### EVT-051 · HTTP-triggered hooks
Priority: **Later**

A hook bound to the generic action runs on demand when a client posts a JSON payload to its
execution endpoint. The payload shape is defined by the hook author. The response contains the
hook's return value and an execution status (unspecified, fail, success).

Privileges: system administrator, project administrator of the hook's project, or any
authenticated user when the hook is shared (EVT-040). The UI setting is "allow other users to
execute". This lets administrators publish controlled operations that ordinary users may run,
for example reading a repository's configuration that otherwise requires administrator rights.

Calling an HTTP-triggered hook from a templated webhook (EVT-025, EVT-035) makes any webhook
event a hook trigger, for example a container tag push, which has no hook of its own.

### EVT-052 · Scheduled hooks
Priority: **Later**

A hook bound to the scheduled action runs on a cron schedule.

| Setting | Meaning | Default |
|---|---|---|
| Cron expression | Standard five-field cron with minute resolution; seconds are not supported. | required |
| Time zone | Zone in which the expression is evaluated. | UTC |

Payload: a trigger identifier. Response: a message. Typical uses: periodic synchronization,
cleanup, scanning. Long batch jobs must respect the execution limits (EVT-055); the recommended
pattern is processing in bounded chunks and remembering progress in the state store (EVT-054).

Edge case: in a cluster, how a single execution per schedule is guaranteed is unspecified
(trove: advisory-lock lease per INV-12).

### EVT-053 · Script runtime API
Priority: **Later**

A hook is a module whose default export is an asynchronous function receiving two injected
arguments, a context object and the event payload, and returning the response for its action.
Typed request and response definitions exist per action (EVT-058).

Language and sandbox rules:

- TypeScript, compiled for a modern ECMAScript target (open: either ES2022 or ES2020; the spec
  decides). JavaScript is also accepted.
- No import statements; built-in JavaScript objects are available.
- Node.js APIs are disabled, including the timer functions; use the context's wait function.
- Every promise must be awaited; a "floating" promise fails the execution.

<details>
<summary>Context object</summary>

| Member (descriptive) | Function |
|---|---|
| Base URL | The system's base URL. |
| Platform token | An access token the hook can use for API calls. |
| Internal HTTP client | GET, POST, PUT, PATCH, DELETE and a generic request method against the system's own API; host and authentication are set automatically; endpoints are relative paths. Returns status, headers and parsed body; throws an error object with message and status for responses with status 400 or higher. The DELETE shortcut takes no body; the generic request method can send one. |
| External HTTP client | An Axios instance for calls to external services, plus fire-and-forget variants of GET, PUT, POST, DELETE, HEAD, PATCH and a generic request. Fire-and-forget schedules the request and returns immediately; success means only "scheduled". |
| Wait | Returns a promise that resolves after the given milliseconds. |
| Remaining execution time | Milliseconds left before the execution times out. |
| State | Persistent key-value store (EVT-054). |
| Cryptography | Hash (SHA-256, SHA-512), HMAC (SHA-256, SHA-512) with hexadecimal (default) or Base64 output, cryptographically secure random bytes (at most 4096 per call). Unsupported algorithms or encodings, non-positive sizes and sizes above 4096 raise errors. |
| Secrets | Read a hook secret by name. |

</details>

Logging: console debug, info, warn and error lines are captured and shown in the execution logs;
logs are kept even when an execution is killed for exceeding a limit. Logging large objects
costs memory because they are serialized to JSON.

Outbound network guard (on by default, can be switched off in self-hosted installations): the
external client may not call localhost, "unicast IP addresses" (which ranges are meant is
unspecified) or the system's own domain names.
Self-hosted installations can also whitelist which request options hooks may override, configure
a DNS cache (default on, 5 s maximum TTL, 1 s for errors) and add trusted CA files. Certain
output fields (for example a modified repository path) are excluded from output obfuscation.

### EVT-054 · Per-hook state store
Priority: **Later**

Each hook has a small key-value state that persists between executions: get, set, has, delete,
clear and get-all. Values are stored as strings; non-string values are serialized to JSON.

| Limit | Value |
|---|---|
| Items per hook | 10 by default; configurable in self-hosted installations |
| Key length | 40 characters |
| Value length | 250 characters |

Edge cases: the state is not saved when the execution fails; concurrent executions of the same
hook are not supported by the state store (last writer behavior unspecified).

### EVT-055 · Execution limits and failure backoff
Priority: **Later**

<details>
<summary>Limits</summary>

| Limit | Hosted deployment | Self-hosted default |
|---|---|---|
| Wall-clock execution time | 20 s | 5,000 ms call time |
| CPU time | 1 s | 1,000 ms |
| Memory | 64 MB | 64 MB per execution; 128 MB maximum heap per runtime process |
| Source size | 100,000 characters | 100,000 characters |
| Execution rate | 1,200 per second burst; 120 per second sustained over one minute | unspecified |
| Call depth | unspecified | 100 |
| Queue wait before an execution is killed | unspecified | 30,000 ms |
| Request payload size | unspecified | 100 KB |
| Runtime processes in the pool | unspecified | 10 |
| Hooks per action | unspecified | 10 |
| Limit check interval | unspecified | 100 ms |
| Cached hook definitions | unspecified | 200, kept 300,000 ms |

</details>

An execution that exceeds a limit fails with a specific error: too much memory, too much CPU
time, execution timeout, waited too long in the queue, pool closing, killed unexpectedly, or
floating promises.

Failure backoff:

- Failed executions put the hook into exponential backoff, up to about 17 minutes (2^10 time
  units in the self-hosted settings).
- Ten failures inside a ten-minute window block the hook.
- Backoff exponent and failure counters reset every 24 hours.
- Failures caused by queue wait time and test runs from the edit page do not count.
- Hooks in backoff or with other warnings show a warning icon in the hook list; the reason is
  shown on hover; warnings can be dismissed; several warnings open in a dialog. Executions
  skipped because of backoff appear in the run history.

Edge case: open whether the failure budget is 10 failures per 24-hour cleaning interval (the
self-hosted settings) or 10 failures per 10 minutes (hosted deployments); the spec decides.

### EVT-056 · Hook testing
Priority: **Later**

- Test a stored hook with a supplied payload, or run an ad-hoc test without a stored hook.
- The UI edit page has a test pane: edit a simulated JSON payload, run, and inspect the result,
  the logs and metrics (run time, memory, CPU).
- Local tooling can run the local, not yet deployed code against a payload (EVT-062).

Result contents: hook key, triggering action, HTTP status, execution status, warnings, errors,
debug output, the hook's return value, and execution metadata such as logs and timing. Test runs
are excluded from backoff and can be included in or excluded from the history.

### EVT-057 · Execution history and rerun
Priority: **Later**

Failed executions are always recorded; successful ones only with "record successful runs". A
hosted deployment records by default; self-hosted installations must enable recording
(EVT-060).

History entry: hook key, action, project, execution status, start and end time, who or what
triggered it, test-run flag, executed hook version, trace id; plus logs and returned data.

| Query parameter | Meaning |
|---|---|
| Maximum items | Limit the number of entries. |
| Start | Exclude executions that started before this time. |
| End | Exclude executions that started after this time. |
| Hook, project, include test runs | Further filters (local tooling and AI tool server). |

UI: per hook, a run history with hook name, project, status, event, duration, last triggered
by, last triggered time; customizable columns; filters by hook type, status and application
(EVT-040); detail view with summary, logs and state.

Rerun: an execution from the history can be run again with its recorded payload, for successful
and failed runs. Input: hook key and history entry id; option to force recording of the rerun
(default off). In the UI the payload is shown for review, "save successful rerun results" can be
ticked, and the rerun continues in the background. Privilege: as for executing (EVT-051).

### EVT-058 · Action discovery metadata
Priority: **Later**

Two operations describe the available actions at runtime:

- List action names.
- Action metadata, optionally restricted to the actions available in a project.

| Metadata item | Meaning |
|---|---|
| Action | Owning application and action name. |
| Description | Human-readable description. |
| Sample payload | Example request as JSON. |
| Sample code | Starter script. |
| Type definitions | TypeScript interfaces for request and response. |
| Supports projects | Whether project administrators can create hooks for the action. |
| Filter type | Which filter shape applies (EVT-041). |
| Mandatory filter | Whether a filter must be set. |
| Documentation link | Link to the action's documentation. |
| Asynchronous | Whether the action runs asynchronously (informational hooks). |
| Request schema | JSON schema of the execution request, when available. |
| Request type name | Name of the request type within the type definitions. |

Local tooling also shows, per action, the supported filter criteria (repository key, repository
path).

### EVT-059 · Hook versions
Priority: **Later**

A deployment can carry a version identifier, a change description and a source-control commit
id. The system keeps a number of versions per hook and records which version ran in each
history entry; a deployment returns the hook key and the new version number.

| Limit | Self-hosted default |
|---|---|
| Version identifier length | 64 characters |
| Change description length | 256 characters |
| Commit id length | 128 characters |
| Versions kept per hook | 50 |

Unspecified: operations to list versions or roll back to an earlier one.

### EVT-060 · Hook service settings
Priority: **Later**

Self-hosted installations configure the hook service in their node configuration. Settings not
already listed in EVT-053, EVT-054, EVT-055 and EVT-059:

<details>
<summary>Execution history storage</summary>

| Setting | Meaning | Default |
|---|---|---|
| Recording enabled | Store execution history. | off |
| Storage URL | `redis://` or `rediss://` (Redis or Valkey, TLS for the latter) or `file://`; several comma-separated URIs with the same protocol. | file storage in the log directory |
| Connection parameters | Semicolon-separated: username, password, mode (single or cluster; mandatory for Redis/Valkey), command timeout, connection timeout. | command timeout 2,000 ms, connection timeout 10,000 ms; username and password default to literal placeholder values |
| Maximum entry size | Entries larger than this are truncated (mainly logs and returned data). | 130 KB |
| Trim interval | How often history is trimmed. | 60,000 ms |
| Entries per hook | Maximum history entries kept per hook. | 100 |
| Retention | How long entries live in Redis/Valkey (not used for file storage). | 1,296,000 s (15 days) |

</details>

File storage is discouraged: when the file reaches its limit all entries are erased instead of
truncated, and with several nodes each node keeps its own history.

Other settings: failure backoff (maximum exponent 10, reset interval 24 h, failure budget 10);
internal RPC connections used by event producers (maximum connection age 300,000 ms, maximum
idle time 30,000 ms); code samples gallery source (enabled, a source
repository on GitHub, polling every 24 hours); log throttling (50 entries per 250 ms) and log
rotation (24 h, 25 MB, 10 files).

### EVT-061 · Hook management UI
Priority: **Later**

In the administration area, a hooks page:

- Tiles to create the first hook of each kind; afterwards "new hook" with the kind.
- For event-driven hooks, a source selector (artifact store, token management, runtime) and the
  list of events with "add".
- Start from an empty skeleton or from a code gallery of ready-made samples (reference only).
- Editor with TypeScript autocompletion; name field; settings dialog (enable, description,
  repositories by list or pattern, secrets, record successful runs; HTTP-triggered: allow other
  users; scheduled: cron and time zone).
- Test pane (EVT-056); save or discard.
- List of configured hooks: enabled state, name, description, project, type, run mode (cron,
  automatic, HTTP); edit and delete actions; customizable columns; filters by application and by
  source; search; warning icons (EVT-055); per-project or all-projects view.
- Run history tab (EVT-057).

### EVT-062 · Local development tooling and declarative management
Priority: **Later**

A companion command-line tool supports developing hooks as code:

| Capability | Behavior |
|---|---|
| Scaffold | Create a project for a chosen action: a manifest (name, code location, secrets, filter, schedule), a development package description (not used at runtime), the script, a unit-test file, compiler settings, generated types. Options: overwrite, skip tests, application, project. |
| Local test run | Run the local code against a JSON payload given inline, from standard input or from a file; JSON or table output. |
| Deploy | Push definition and code; optional Base64 encoding; optional version id, change description and commit id (EVT-059). |
| Secrets | Add or edit secrets in the manifest, encrypted locally with a master password that deploy and test run ask for; option to deploy or test without secrets. |
| Remove | Undeploy a hook by key or from the manifest. |
| Execute | Run an HTTP-triggered hook with a payload. |
| List events / list hooks / history | Read operations with table or JSON output. |
| Edit schedule | Set cron and time zone in the manifest (minute resolution; time zone default UTC); applied on the next deploy. |

Client request timeout default: 5,000 ms. The scaffolded unit tests use Jest with mocked context
and payload objects.

Declarative management: an infrastructure-as-code provider (Terraform) offers a hook resource
with key, enabled flag, description, source code, action, filter criteria and secrets, so hooks
can be managed next to repositories. Changing such a hook in the UI causes drift on the next
plan. Interactions: configuration as code in
[operations-and-monitoring.md](operations-and-monitoring.md).

## Legacy in-process extension scripts

### EVT-063 · Extension scripts: deployment and reload
Priority: **Later**

Older extension mechanism: Groovy scripts (Groovy 4.0) loaded into the server process, wrapping
user code in closures at well-known extension points (EVT-064). Superseded by scripting hooks
(EVT-038).

- Every file with the Groovy extension in the extension directory is loaded at startup, in
  alphabetical order; callbacks run in load order.
- Additional libraries go into a library subdirectory.
- In a cluster, scripts are placed on the primary node and propagated to all nodes.
- Reloading after startup is off by default. A refresh interval in seconds (0 = off) makes the
  server detect changed and new files and reload them; removals are not detected, and deleting
  or renaming files while auto-reload is on requires a restart. Auto-reload should be off in
  production.
- A reload operation reloads all scripts changed since the last reload, independent of the
  interval; it returns a plain-text list of loaded files.
- A script is removed by renaming it with a dedicated deletion suffix; in a cluster the removal
  propagates and the file is deleted on every node.

Interactions: running both extension scripts and hooks on one repository is discouraged
(EVT-038).

### EVT-064 · Extension point catalog
Priority: **Later**

| Group | Extension points | Capability |
|---|---|---|
| Upload | Before upload request | Runs after authentication, before the repositories handle the upload; can force a different storage path. |
| Download | Alternate response; alternate all responses; alternate remote path; alternate remote content; after download error; before remote download; after remote download; before download; after download; before download request | As the corresponding hooks in EVT-044. Before remote download can add upstream request headers, except headers the system always sets itself (its origin-identification headers and `Accept-Encoding`). Before download request can mark a cached resource as expired (forcing a re-fetch; costs a database update and an upstream request) or force a different cache path. |
| Storage | Before/after create, delete, move, copy, property create, property delete | Before-callbacks abort the operation by throwing a cancel exception carrying a message and an HTTP status. |
| Jobs | Named job with interval (ms) and initial delay (ms), or a cron expression | Interval and cron are mutually exclusive; the delay does not apply to cron. A cron expression can carry a seconds field (Quartz style). |
| Executions | Named operation invoked over REST (EVT-066) | |
| Realms | Named authentication realm (EVT-068) | |
| Build | Before save, after save of build information | The build model is partially mutable. |
| Promotions | Named build promotion invoked over REST (EVT-067) | |
| Staging | Named staging strategy queried by build servers (EVT-067) | |
| Replication | Before file, directory, delete, property and statistics replication | A skip flag (default off) suppresses replication of the item; the target server's details are available. |

Download-point details: the alternate-response points expose a status (unset by default for
the plain variant; the original response status for the "all responses" variant), a message, a
replacement stream with its size (unset: -1) and extra response headers; they are not called
when the response is already committed. The plain variant runs only for GET requests
of found resources; the "all responses" variant for GET and HEAD of found or not-modified
resources. Checksum requests for an altered response still return the original resource's
checksum unless the script handles them. The download-request point does not know the actual
repository when a virtual repository is requested, so it does not work with container-image
virtual repositories.

Storage caveat: callbacks run inside a transaction; a script may still read the previous state
in rare cases.

Globally available helpers: a logger (named after the script file), repository operations,
security context (current user and permissions), searches (including the query language,
SRCH-024 in [search-and-query.md](search-and-query.md)), and build operations. Only the
documented public API is supported, although the full server classpath is reachable. Variables
declared without a local declaration become global across executions.

### EVT-065 · Execution identity and elevation
Priority: **Later**

Download, storage, execution and build extension points run with the identity of the user
whose request triggered them. A script can wrap code in a run-as-system block, which runs
without any authorization checks and can therefore do what the original user may not.

Security note: this is a privilege-escalation primitive by design; anyone who can deploy
extension scripts is effectively a system administrator.

### EVT-066 · REST-invoked executions
Priority: **Later**

A named execution is invoked over the API:

| Input | Meaning | Default |
|---|---|---|
| Execution name | The closure to run. | required |
| Parameters | Name=value pairs in one query parameter: pairs separated by a pipe, several values of one name separated by commas; a semicolon (or an encoded pipe) is the recommended value separator. | none |
| Asynchronous | 0 or 1. | 0 (synchronous) |

Declaration options per execution: version, description, HTTP method (GET, PUT, DELETE, POST;
default POST), default parameters, users and groups allowed to query or invoke it. The closure
receives a read-only parameter map (each entry a list of values) and, when declared, the raw
request body stream. It can set the response status (unset by default) and a message replacing
the response body; both have no effect for asynchronous runs.

Responses: 200 with plain text ("OK" by default); 404 when no execution scripts are loaded or
the name is unknown; 401, 403, 500. Authentication: basic, legacy API key or bearer token.

### EVT-067 · Build promotion and staging strategies
Priority: **Later**

- **Promotion:** a named promotion is invoked for a build name and number with parameters; it
  can set status and message; users and groups restrict who may run it.
- **Staging strategy:** a named strategy is queried with a build name and parameters by build
  servers before a release build. It returns the default module version (module id, next release
  version, next development version), version-control settings (use a release branch and its
  name, create a tag and its name or URL, tag comment, comment for the next development
  version) and promotion settings (target repository, comment, status).

Interactions: build promotion in [builds-and-ci.md](builds-and-ci.md).

### EVT-068 · Extension authentication realms
Priority: **Later**

A script can define named authentication realms. They are consulted before all built-in realms
(internal, LDAP and others), in definition order. A realm implements "authenticate" (username,
password → true or false; may assign groups and user details) and "user exists".

| Option | Meaning | Default |
|---|---|---|
| Auto-create users | Create a user record on successful login; when off, the user is transient and gets the permissions of auto-join groups. | on |
| Additive policy | Run only for users already authenticated by another realm, to enrich them (for example with groups). | off |

Script realms are used only after an explicit switch in the access configuration is set.
Interactions: [authentication-providers.md](authentication-providers.md).

### EVT-069 · Extension inventory, source retrieval, logging and UI
Priority: **Later**

| Operation | Output | Privilege |
|---|---|---|
| List extension information | Executions and staging strategies visible to the caller: name, version, description, allowed users and groups, parameters | filtered by the caller's permissions |
| List by type | Same, for executions or staging only | as above |
| Retrieve script source | The Groovy source of a named script | unspecified |
| Reload | EVT-063 | unspecified |

Logging: the default log level for extension scripts is warn; it is set per script in the
logging configuration, with the script file name (without extension) as logger name; levels
error, warn, info, debug, trace.

UI (administrators only): a page in the administration area lists scripts and their status,
copies a script's content and deletes scripts. IDE support files for Groovy editors exist;
community example scripts are published on GitHub.

## API description and query layers

### EVT-070 · Machine-readable API description
Priority: **MVP**

The management API is published as OpenAPI 3 documents, one per API area (artifacts and
storage, repositories, builds, searches, trash, replication, security, webhooks, hooks,
projects, administration, and so on), rendered as reference pages with request/response schemas
and, where enabled, an interactive "try it" console. Each operation states its authentication
(bearer token, basic, API key) and required privilege.

Edge cases: open whether the documents declare OpenAPI 3.0 or 3.1.0; the spec decides.
Deprecated operations get no OpenAPI description (EVT-072).
Interactions: trove's OpenAPI 3.1 document is the source of truth for its API (INV-6 in
[../spec/README.md](../spec/README.md)); operation catalogs in
[api-content-operations.md](api-content-operations.md) and
[api-admin-operations.md](api-admin-operations.md).

### EVT-071 · Typed JSON configuration documents
Priority: **P2**

Request and response bodies of configuration operations are typed JSON documents, each with its
own vendor-specific media type, for: local, remote, virtual and federated repository
configuration; replication configuration; users, groups and permission policies (two
generations); import and export settings; version information; and the build
artifacts request used by build-artifact search and archive download. A legend marks each
element as mandatory in create/replace (optional in partial update), optional, read-only, or
with its default value.

Interactions: the documents' contents belong to the pages that own the resources
([repositories.md](repositories.md), [replication-and-federation.md](replication-and-federation.md),
[identity-and-access.md](identity-and-access.md), [builds-and-ci.md](builds-and-ci.md)).

### EVT-072 · API deprecation lifecycle
Priority: **P2**

Deprecated operations are kept in an archive section until they are decommissioned, each with a
migration note naming the replacement and listing the differences (for example: list results
wrapped in an object with a pagination cursor instead of a bare array, renamed keys, relative
instead of absolute URIs, an added status field, cursor and limit parameters).

Retirement procedure: announcement with a fixed removal date for hosted deployments; in
self-hosted releases the operations are first disabled behind a feature flag, which gives a
90-day grace period, and removed in a later release. Some deprecated operations have no removal
date. Example: a "download on the server without sending the content to the client" operation
(useful to pre-fill a remote cache) becomes disabled by default at its end of life.

Edge case: older user, group and permission operations encode spaces in names as `+` by default,
switchable by a system setting.

### EVT-073 · GraphQL query gateway
Priority: **P2**

A single GraphQL endpoint federates several domains (package metadata, evidence, release
lifecycle, application governance, public package catalog, multi-site topology), so related data
from different subsystems can be fetched in one query with exactly the needed fields.

| Aspect | Behavior |
|---|---|
| Request | POST, `Content-Type: application/json`, body with the query and optional variables. Only POST with JSON is supported. |
| Authentication | Access token (bearer) or basic authentication; the token's audience must be the wildcard audience. |
| Authorization | Enforced per query and per record; only data the caller may read is returned. |
| Structure | Each domain is a namespace field on the root query; inside, list queries carry a "search" name prefix and single-item queries a "get" prefix (the latter return a not-found error when absent). |
| Filtering and ordering | A filter argument and an ordering argument, on the queries that support them. |
| Pagination | Relay-style cursor connections: `first`/`after` forward, `last`/`before` backward; `first` and `last` are mutually exclusive; page-size defaults per query. |
| Time values | Fields ending in "At" are ISO-8601 timestamps in UTC; a directive selects another format. |
| User values | Fields ending in "By" contain the acting user. |
| Errors | HTTP 200 with an errors array; each error has an extension code for not found, access denied or invalid input (for example invalid cursor or filter combination). |
| Evolution | Directives mark experimental fields (may break) and deprecated fields (will be removed). |

Further operations of the gateway: retrieve the schema (any authenticated principal), ping
(plain text when the service and its dependencies are ready; authentication optional, but an
invalid bearer token is rejected), liveness, readiness, service metrics in OpenMetrics text
(requires a metrics-read token scope) and router metrics when enabled.

Interactions: the query language for stored items, SRCH-023 to SRCH-036 in
[search-and-query.md](search-and-query.md); metrics in
[operations-and-monitoring.md](operations-and-monitoring.md).

### EVT-074 · Schema introspection and interactive console
Priority: **P2**

- GraphQL introspection is enabled by default for authenticated callers, so standard GraphQL
  tools can autocomplete, validate and generate documentation. Introspection describes the
  schema only; it neither returns data nor relaxes permissions.
- Self-hosted installations can disable introspection with a configuration switch.
- The UI's integrations area contains an interactive query console: editor with
  autocompletion, browsable list of queries, mutations and types, run button, JSON result pane.

### EVT-075 · Package metadata GraphQL domain
Priority: **P2**

A GraphQL domain over the package metadata of stored packages (packages, versions, the
repository locations of a version, and files).

| Operation | Purpose |
|---|---|
| Get package | One package by name plus package type (or repository package type); optional project scope. |
| Search packages | Filter, order, paginate packages; page size 1 to 50. |
| Search package versions | Filter by version attributes and by parent package; order; paginate. |
| Search package files | Filter by checksum, name, size, MIME type and by parent version or location; no ordering. |
| Get version location | One version location by package name, version, repository key and type. |

Entities: package (name, type, description, latest version, version count, timestamps, tags,
qualifiers, statistics); version (version string, size, timestamps, parent package, locations,
files, tags, qualifiers, statistics); version location (repository key, repository class, path
of the lead file, files at that location, statistics); file (name, checksums including SHA-256,
size, MIME type, qualifiers). Qualifiers are key/value metadata; statistics are download counts.
Filter inputs support equality, ranges, string matching (for example name prefix), and/or/not,
and edge filters on related entities ("version has package with…", "file has version
with…").

Rules and edge cases:

- Self-hosted installations must enable the domain explicitly.
- The token audience must be the wildcard audience or the metadata service's audience.
- Results are limited to repositories the caller can read (including project scope); searches
  return empty connections when nothing is readable.
- Get package returns null with a GraphQL error when the package is missing or unreadable; with
  a project given and the package outside that project, it returns null without an error, so
  aliased multi-package queries do not fail as a whole.
- A request without valid credentials is rejected with HTTP 403, whereas a caller who is not a
  member of the requested project gets a GraphQL error inside an HTTP 200 response.
- License and vulnerability summary fields on packages and versions are deprecated (that data
  moves to the scanning API).

Interactions: package views in [artifacts.md](artifacts.md) (ART-048, ART-049).

### EVT-076 · Legacy package metadata GraphQL
Priority: **Later**

An older, deprecated GraphQL endpoint of the metadata service, with its own in-browser GraphiQL
console and scoped-token authentication. Three queries: packages, versions, files; each takes a
mandatory filter, `first`, `after` and an order argument, and returns a connection with edges,
cursors and page information.

| Filter target | Dimensions |
|---|---|
| Packages | Name pattern (case-sensitive), list of package types, created and modified time ranges, latest-version pattern, version-count range. |
| Versions | Name pattern, created range, size range, license patterns, tag patterns, property name/value patterns, vulnerability level with minimum count. |
| Files | Name pattern, size range, MD5, SHA-1 and SHA-256 patterns. |

Entities expose package data (type, timestamps, version count, VCS and issue URLs, description,
latest version, licenses, properties, qualifiers, versions, sources, tags, download and follower
statistics, vulnerability counts), version data (size, timestamps, licenses, files, repositories
with lead file path, tags, statistics, qualifiers, properties, contributors, vulnerability counts
per severity) and file data (checksums, lead flag, length, architecture, distribution, compiler
and version, MIME type, qualifiers). Successor: EVT-075.

## AI-assistant tool server

### EVT-077 · Remote tool server for AI assistants
Priority: **P2**

The system exposes a remote server speaking the Model Context Protocol (MCP) over HTTP at a fixed
path below its base URL, so AI assistants in IDEs and chat clients (Visual Studio Code, Cursor,
Claude, Kiro, Codex and other MCP hosts) can call system operations as tools.

- A system administrator enables the server for all users in the integrations area; setup shows
  the endpoint URL. Disabling it (by deleting the integration application) makes it stop
  answering.
- Users add the URL to their MCP client configuration and complete an OAuth authorization in
  the client; hosts that support OAuth for MCP can connect.
- Tools act as the connected user (for example, the subscription list returns only what the
  caller may read); the exact authorization model is unspecified. Tools that create webhooks or
  hooks create them disabled.
- Tools are versioned with the services that provide them.

Security notes: operators remain responsible for third-party AI agents;
clients should run with least privilege and with human approval before any tool that creates,
updates or deletes resources.

### EVT-078 · Experimental tool gating and approval
Priority: **P2**

- An administrator switch exposes experimental ("beta") tools, which may change behavior, inputs
  or outputs; it can be set at setup and changed later. Without it only generally available
  tools are offered.
- Some tools are marked as requiring user approval before execution: updating a hook, replacing
  a webhook subscription and replacing evidence categories.
- Experimental tools: token creation and listing, GraphQL introspection and query execution,
  the approval-required tools above, executing an HTTP-triggered hook.

### EVT-079 · Tool catalog
Priority: **P2**

<details>
<summary>Toolsets</summary>

| Toolset | Tools (capability level) | trove relevance |
|---|---|---|
| Tokens | Create a token (grant type, refresh token, user, scope, lifetime, refreshable, audience, description, reference token, revocable, project); list tokens with filters and ordering | [access-tokens.md](access-tokens.md) |
| OIDC providers | Create an OIDC identity provider configuration (name, issuer, audience, description, provider type, token issuer, permissive validation, default proxy) | [oidc-ci-integration.md](oidc-ci-integration.md) |
| Projects, roles, stages, users, groups | Create, read, update projects; project roles; membership of users and groups (role lists replace existing roles); move a repository into a project; share a repository with all projects; global roles; list stages; list and look up users and groups (default limit 1000, cursor pagination) | [identity-and-access.md](identity-and-access.md) |
| Repositories | Create, get, list (by type, package type, project), update repository configuration | [repositories.md](repositories.md) |
| Packages | List versions of a stored package (order by semantic version, version, modified time, downloads; exclude pre-releases); find repositories holding a version; native-client install instructions for a version | ART-049 in [artifacts.md](artifacts.md) |
| Builds | List builds and build runs through the query language; get build information with optional diff against an older run | [builds-and-ci.md](builds-and-ci.md) |
| Storage | Item information, flat or deep listing with depth, statistics, last modified, properties, effective permissions | [artifacts.md](artifacts.md) |
| Webhooks | Event catalog, list/get/create/replace subscriptions, delivery log status, latest attempts and counts | EVT-017, EVT-018, EVT-033 |
| Hooks | List actions, code-generation instructions for an action, create, update, get, list, execute, execution history | EVT-040, EVT-057, EVT-058 |
| GraphQL | Introspect entities, execute a query with variables | EVT-073 |
| Federation status, multi-site topology, distribution, scanning and curation, evidence, application governance, AI asset catalog | Health and conflicts of federated repositories; topology and entity sync; distributing release sets and signing keys; scan summaries, violations, impacted resources, curation compliance, audit events and waivers; evidence search and creation; application versions, promotion, release and rollback; approved AI skills, agent plugins and tool servers per project | Later-priority subsystems ([replication-and-federation.md](replication-and-federation.md), [distribution-and-lifecycle.md](distribution-and-lifecycle.md), [security-scanning-and-curation.md](security-scanning-and-curation.md)) |

</details>

### EVT-082 · In-product AI assistant
Priority: **Later**

A chat panel opened from the top bar of the UI. In its default mode it answers how-to questions
from the system's documentation, navigates the user to the right page or workflow for a stated
intent, and gives contextual guidance; a second mode drafts lifecycle-policy code in Rego from a
natural-language description (the playground assistant of DIST-058 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md)). Answers about the user's own
data, and actions taken on the user's behalf after approval, are possible later extensions.

- **Enablement.** Off until the account owner opts in; the owner can opt out at any time and can
  additionally require activation per installation, which an administrator then confirms on
  first use there.
- **Limits.** Each user has a weekly token budget; when it is used up, AI features stop until the
  budget resets.

The assistant depends on a hosted language-model service. For a self-hosted product the
equivalent would call an operator-configured model endpoint; the documentation-search and
navigation part needs no access to repository data, which keeps its permission surface small.
Actions on the user's behalf would go through the same authorization as the tool server
(EVT-077).

**Interactions.** EVT-077; DIST-058.

## Client SDKs

### EVT-080 · Client SDKs
Priority: **P2**

Two client libraries wrap the API:

| SDK | Coverage | Notes |
|---|---|---|
| Java | Upload and download; file, folder and repository information; searches; builds; item management; repository, user, group and permission management; system configuration. | Published to Maven Central; builder-style client with URL, credentials and custom trust settings; Apache License 2.0; integration tests run against a live instance. |
| Go | Services for the artifact store, token management, distribution, scanning, CI pipelines and release lifecycle, each created through a service manager. | Used as a Go module; its APIs change frequently between releases; tests need a running instance and create and empty a test repository. |

For trove a Go client is cheapest generated from the OpenAPI description (EVT-070).

## Notes for implementers

- **Webhooks never block, hooks always can.** Webhook delivery (EVT-001) must run outside the
  request path; a slow or failing receiver must not slow or fail a deploy. Blocking hooks
  (EVT-042) sit inside the request path and need a defined failure mode (proceed or refuse on
  hook error), which is unspecified so far.
- **Exactly one handler per subscription, full-replace updates.** Clients rely on the secret
  list semantics of EVT-018: missing name removes, missing value keeps, empty value wipes. A
  partial-update shortcut would silently delete secrets of existing clients.
- **Enabled defaults differ between webhooks and hooks.** Both create operations list the
  enabled flag as required, but the declared default is off for subscriptions (EVT-018) and on
  for hooks (EVT-040), and the AI tool server creates both disabled. trove should require the
  flag explicitly on create rather than inherit either default silently.
- **"Any local/remote" is dynamic and move/copy match on the source.** New repositories join
  "any" filters automatically (EVT-019); moved, copied and promoted events are matched against
  the repository they leave (EVT-004, EVT-006).
- **Signatures are over the raw body bytes.** Receivers verify HMAC-SHA256 over exactly the
  bytes sent (EVT-024); re-serializing JSON before signing or sending breaks verification.
  trove must define and document its own signature header and encoding, which EVT-024 leaves
  unspecified.
- **Templates are not escaped.** Payload templates (EVT-026) insert raw scalars; an artifact
  path containing a quote breaks a JSON body unless the `json` function is used. The template
  root is the envelope plus secrets, not only the three listed roots.
- **Delivery logs and hook histories hold secrets in flight.** Recorded requests include
  rendered headers and bodies (EVT-032); rendered secrets must be masked before storage.
- **Outbound requests are an SSRF surface.** Both webhook targets (EVT-029) and the hook
  runtime's external client (EVT-053) must refuse loopback, private and own-service addresses
  by default, re-checked at delivery time.
- **Bulk operations and per-item hooks interact.** A stop in any repository of a bulk
  repository operation aborts the batch (EVT-048), while token revocation runs the hook per
  token (EVT-049); duplicate scan status events (EVT-015) require idempotent consumers.
- **Event identifiers are a public contract.** Domain names, event type names, filter keys and
  payload field names are what integrations parse; trove defines its own names once, keeps
  them stable, and settles the open naming questions (EVT-012) before the first release.
