# OIDC federation for CI systems

This page covers workload identity federation: a CI job (or another non-human workload) proves
its identity with a short-lived JSON Web Token issued by its own platform, and the system
exchanges that token for a short-lived access token of its own. No long-lived secret is stored in
the CI system. The page describes trusted provider configurations, identity mappings (the rules
that turn claims into permissions), the exchange operation, the client-side helpers around it, an
OIDC-free variant for cloud IAM roles, and, in the last section, the GitLab facts an implementation
needs to write correct mappings for GitLab CI. Browser single sign-on for humans over OpenID
Connect is a different feature and lives in [authentication-providers.md](authentication-providers.md);
the tokens the exchange issues are described in [access-tokens.md](access-tokens.md).

## Provider configurations

### CIOIDC-001 · OIDC provider configuration
Priority: **MVP**

A provider configuration declares one external token issuer the system trusts for token
exchange. It is a named object; the name is the handle that CI jobs send with every exchange
request (see CIOIDC-018) and the key under which administrators manage it (see CIOIDC-005). One
configuration owns any number of identity mappings (see CIOIDC-007); without at least one
matching mapping, a valid token from the issuer yields nothing.

Behavior:

- The provider URL identifies the issuer. Incoming tokens are verified against the issuer's
  published signing keys and against the expected issuer and audience (see CIOIDC-019).
- The expected issuer defaults to the provider URL. A separate token-issuer setting overrides it
  when the `iss` claim of the provider's tokens differs from the provider URL.
- The audience setting, when filled, must equal the audience the CI platform writes into its
  tokens.
- Several configurations can point at the same issuer (the GitHub App mode in CIOIDC-026 creates
  one per repository); clients pick one by name.

<details>
<summary>Settings of a provider configuration</summary>

| Setting | Meaning | Required | Default |
|---|---|---|---|
| Provider name | Unique name of the configuration. Clients send it in the exchange request and it must match exactly; it also addresses the configuration in management operations. | Yes | — |
| Provider type | GitHub, Azure, or generic OpenID Connect (see CIOIDC-002 to CIOIDC-004). | Yes | unspecified |
| Description | Free text to identify the configuration. | No | empty |
| Provider URL | Issuer URL of the provider. For a generic provider it must match the issuer the provider itself publishes. For the GitHub type it is filled in automatically with `https://token.actions.githubusercontent.com`. | Yes | GitHub type: the GitHub Actions issuer; otherwise none |
| Audience | Expected audience of incoming tokens; must match the audience configured on the provider side. | No | unspecified (empty) |
| Token issuer | Expected `iss` value when it differs from the provider URL. | No | empty, meaning the provider URL is the expected issuer |
| Azure application ID | Needed for the Azure type when the Azure application signs with custom keys because it uses claims mapping (see CIOIDC-004). | No (Azure only) | — |
| Organization | GitHub organization whose tokens are accepted; case-sensitive (see CIOIDC-003). | Yes for the GitHub type, unless permissive mode is on | — |
| Permissive mode | GitHub type only: accept tokens without an organization restriction. Discouraged. | No | unspecified (off is implied, because the organization is otherwise mandatory) |
| Use default proxy | Route the system's outbound calls to the provider through the system-wide default outbound proxy. Which calls (discovery, key retrieval) is unspecified. Exposed in the management API; whether the UI offers a field is unspecified. | No | unspecified |

</details>

UI location: administration area, integrations page, OpenID Connect tab. The list shows per
configuration: name, description, provider type, provider URL, number of identity mappings, Azure
application ID, organization, and the permissive flag. A "new integration → OpenID Connect" dialog
creates a configuration; it offers "save" and "save and continue" (the latter opens the
identity-mapping dialog). System administrators edit and delete configurations; project
administrators only add mappings (see CIOIDC-015).

Limits and edge cases:

- A mismatch between the provider name in the CI job and the configured name makes the exchange
  fail as an authentication error (401), not as "not found".
- What happens to a configuration's identity mappings when the configuration is deleted is
  unspecified.
- Whether the provider name can be changed through an update is unspecified.
- In the management API the token-issuer field is annotated as "organization" and the schema has
  no separate organization field. This suggests that, for the GitHub type, the organization
  restriction is carried in the token-issuer field over the API; this reading is unconfirmed.

Interactions: the outbound proxy setting uses the system-wide outbound proxy described in
[remote-repositories.md](remote-repositories.md). Delegation to projects: CIOIDC-006 and
CIOIDC-015.

### CIOIDC-002 · Generic OpenID Connect provider type
Priority: **MVP**

The generic type accepts tokens from any standards-compliant OIDC issuer, for example Okta,
Jenkins, GitLab and GitHub Enterprise (edition unspecified); AWS Cognito user pools are a further
issuer used through this type. Settings are those of CIOIDC-001 without the GitHub and Azure
specifics.

GitLab through the generic type:

- Provider URL: `https://gitlab.com` for GitLab.com, or the base URL of a self-managed GitLab
  instance.
- Audience: optional; if set, the job's ID token must carry the same `aud` (see CIOIDC-030).
- Token issuer: only needed when the token's `iss` differs from the provider URL.
- Typical mapping: a claims filter on `sub`, for example
  `{"sub": "project_path:my-group/my-project:ref_type:branch:ref:main"}` (see CIOIDC-034), with
  priority 1 for the strongest permission.

AWS Cognito through the generic type (client-credentials flow):

- Provider URL and token issuer: `https://cognito-idp.<region>.amazonaws.com/<user-pool-id>`; the
  discovery document under `/.well-known/openid-configuration` of that URL must be reachable.
- Audience: left empty or set to the app client ID. Cognito access tokens name the app client in
  their `client_id` claim rather than in `aud`, so the mapping filters on `iss` and
  `client_id`.
- Cognito-side prerequisites: a user pool, a user pool domain (for the token endpoint), an app
  client with the `client_credentials` grant and a client secret, and a resource server with at
  least one scope that the token request asks for.
- The CI side obtains an access token from Cognito's OAuth token endpoint with the
  `client_credentials` grant and sends it to the exchange with the ID-token subject-token type.
  So the exchange accepts at least this kind of OAuth access token under the ID-token type; whether
  it checks the declared token type or the JWT kind at all is unspecified. Inferred from the setup:
  an empty audience setting skips the audience check.

Limits: those of CIOIDC-001; further limits unspecified.

### CIOIDC-003 · GitHub Actions provider type with organization restriction
Priority: **P1**

The GitHub type is tailored to the GitHub Actions OIDC issuer.

- Choosing this type fills the provider URL with `https://token.actions.githubusercontent.com`.
  Whether the field stays editable afterwards is unspecified.
- Organization: the GitHub organization whose workflows may exchange tokens. Required and
  case-sensitive, unless permissive mode is on. Which token claim is compared with the
  organization is unspecified; GitHub's `repository_owner` claim is the natural candidate.
- Permissive mode: accept tokens of any organization. Discouraged, because it can grant broader
  access than intended; identity-mapping claims are then the only restriction.
- Claims commonly used in mappings for GitHub tokens: `iss` (set to the GitHub issuer to pin
  the origin), `enterprise`, `repository`, `repository_owner`, `repository_visibility`,
  `environment`, `actor`, `runner_environment`, `workflow`, `sub`. See GitHub's
  [OIDC claims reference](https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/about-security-hardening-with-openid-connect).

GitHub workflow requirements (GitHub facts, for compatibility testing):

- The job or workflow needs `permissions: id-token: write`; without it GitHub refuses to issue the
  token.
- The token is requested from GitHub's runtime endpoint (`ACTIONS_ID_TOKEN_REQUEST_URL`, with an
  `audience` query parameter, authenticated by `ACTIONS_ID_TOKEN_REQUEST_TOKEN`) or through
  `getIDToken()` of the Actions toolkit.
- Reusable (called) workflows do not inherit the caller's OIDC setup. If the called workflow
  performs the exchange without its own setup, the exchange fails with a 400 bad-request error
  about unparsable JSON input. The setup step must be repeated in both workflows.

UI location: same dialog as CIOIDC-001. Choosing the GitHub type fills the provider URL and shows
the organization and permissive fields. A snippet generator is available for this type only (see
CIOIDC-024).

Interactions: CIOIDC-026 creates GitHub-type configurations automatically.

### CIOIDC-004 · Azure DevOps provider type
Priority: **P1**

The Azure type accepts ID tokens issued to Azure DevOps service connections and Microsoft Entra
applications.

- Azure DevOps service connection tokens have this shape:
  `iss = https://vstoken.dev.azure.com/<organization-GUID>`,
  `sub = sc://<organization>/<project>/<service-connection>`,
  `aud = api://AzureADTokenExchange`, plus `jti`.
- Configuration: provider URL = the `vstoken` issuer URL with the organization GUID; audience =
  `api://AzureADTokenExchange`; token issuer left empty unless a custom issuer is used.
- The issuer, subject and audience values are easiest to obtain by running a pipeline once with
  the service connection; its log prints them. Logging claims such as `sub`, `aud` and `iss` is
  safe; logging the full ID token or the resulting access token is not.
- Mappings filter on `sub`, using wildcards where needed: `sc://MyOrg/MyProject/MyConnection`
  admits one project; `sc://MyOrg/*/MyConnection` admits every project that uses that
  connection.
- Azure application ID: when the Entra application uses claims mapping and therefore custom
  signing keys, its application ID must be configured so the right keys are used for
  verification. How the ID is used is unspecified; Microsoft documents that such applications
  publish their keys under an application-specific key URL
  ([signature validation](https://learn.microsoft.com/en-us/entra/identity-platform/access-tokens#validate-the-signature)).

CI side: the Azure DevOps service connection type offered for the system has an "OpenID Connect
integration" authentication method with server URL, provider name and platform URL fields. The
pipeline tasks export the exchanged username and token as output variables for later steps. A
minimum extension version applies; its value is unspecified.

Limits: unspecified.

### CIOIDC-005 · Provider configuration management operations
Priority: **MVP**

Management API for provider configurations. Every call authenticates with a bearer access token.
The representation holds the settings of CIOIDC-001: name, issuer URL, audience, description,
provider type, token issuer, permissive flag, default-proxy flag, Azure application ID.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create provider configuration | Full representation | Stored representation, HTTP 200 (not 201); 400 on invalid input | System administrator |
| List provider configurations | — | Array of representations | System administrator or project administrator |
| Get provider configuration | Provider name | Representation; 404 if unknown | System administrator or project administrator |
| Update provider configuration | Provider name; representation (sent with PUT; whether omitted fields are cleared or kept is unspecified) | Updated representation, 200; 400 on invalid input; whether an unknown name yields 404 is unspecified | System administrator |
| Delete provider configuration | Provider name | 204 without body; 404 if unknown | System administrator |

All operations return 401 for bad credentials and 403 for insufficient privilege. Partial-update
semantics, validation rules and the exact provider-type literals accepted by the API are
unspecified. Open: the server accepts either the lower-case literals `generic` and `github`, or the
display names "Generic OpenID Connect" and "GitHub"; the companion CLI uses yet another spelling
for the exchange (`GitHub`, `Azure`, `GenericOidc`, see CIOIDC-025). The spec decides which
literals the server accepts.

Interactions: the operation catalog in [api-admin-operations.md](api-admin-operations.md) lists
these operations with the rest of the administration API.

### CIOIDC-006 · Project association of provider configurations
Priority: **P1**

Provider configurations can be associated with a project (a tenant-like grouping of
repositories, members and roles, see [identity-and-access.md](identity-and-access.md)). In the UI,
selecting a specific project in the project selector shows only the configurations associated with
that project; "all projects" shows all of them. A create request may carry a project key in its
body, but the management schema has no such field, so how the association is created, changed or
removed is unspecified. Scoping OIDC at project level is also a capability of the GitHub
integration (CIOIDC-026).

Interactions: project-scoped identity mappings (CIOIDC-015) are the precise form of project
delegation.

## Identity mappings

### CIOIDC-007 · Identity mapping
Priority: **MVP**

An identity mapping is a rule inside one provider configuration. It pairs a claims filter (which
tokens it applies to) with a token specification (what the exchange issues). During an exchange
the system selects one mapping (see CIOIDC-009) and issues a token according to its specification.
A reference token is also created per mapping for the external service; see CIOIDC-020.

<details>
<summary>Settings of an identity mapping</summary>

| Setting | Meaning | Required | Default / limits |
|---|---|---|---|
| Name | Identifies the mapping within its provider configuration; management operations address it by provider name + mapping name. | Yes | — |
| Description | Free text; recommended to name the source repository and the mapped identity. | No | empty |
| Priority | Integer. Lower number = evaluated earlier (1 = highest). Recommended: priority 1 for the most powerful grant, the weakest grant last. | Marked mandatory in the UI, yet an omitted value is accepted | Omitted → lowest priority |
| Claims filter | JSON object of claim name → expected value(s) (see CIOIDC-008). Entered as key/value rows or as raw JSON; a toggle switches the two editors. | Marked mandatory in the UI | An empty filter matches every token of the provider (see edge cases) |
| Token scope kind | What the issued token represents: admin, user, group(s), group pattern (dynamic), resource scope (dynamic), or project roles (project tab only). Combinations: CIOIDC-010. | Yes | — |
| Username | Fixed user the token is issued for (user or admin kind); appears in the system's logs when the workload authenticates. | For user/admin kinds | Blank → a synthetic subject is used (format unspecified) |
| Groups | Group names whose permissions the token carries (group kind). | For group kind | — |
| Username pattern | Dynamic user subject derived from a claim (see CIOIDC-011, CIOIDC-012). | For dynamic user | — |
| Groups pattern | Dynamic group list derived from a claim. | For dynamic groups | — |
| Resource scope pattern | Dynamic resource grant derived from a claim (see CIOIDC-013). | For resource kind | — |
| Project roles | One or more roles of one project (see CIOIDC-015). | For project kind | — |
| Services (audience) | Which of the system's services the issued token is valid for: all, or a specific service. | Yes (UI) | All services pre-selected in the GitHub App flow (CIOIDC-026); otherwise unspecified |
| Expiration time | Lifetime of the issued token. The UI takes minutes, the API takes seconds. | No | Maximum 24 hours (1,440 minutes); default unspecified (example: 60 minutes) |
| Self-revocable | Whether the issued token may revoke itself. API only; semantics beyond the flag are unspecified. | No | unspecified |

</details>

API representation: name, description, claims filter (free-form JSON object), priority, and a
token specification. The token specification holds username, scope expression, audience, lifetime
in seconds, username pattern, groups pattern, and the self-revocable flag. Responses add the
project key derived from the scope (see CIOIDC-015). The schema has no field for a resource scope
pattern (CIOIDC-013); how such a mapping is expressed over the API is unspecified.

UI location: integrations page, row menu of a provider configuration → "add identity mapping".
The mapping dialog has a Global tab and a Project tab. Global offers token scope kinds admin, user
and group plus a username field. Project offers project roles. Both have services and expiration
time. Each mapping row has a menu for edit and delete, and an "eye" icon opens a read-only detail
view.

Limits and edge cases:

- An empty claims filter lets any holder of a valid token from that issuer obtain the mapped
  token. For a public issuer such as GitLab.com or GitHub
  Actions, that is anyone with an account.
- Whether a lifetime above 24 hours is rejected or clamped by the API is unspecified.
- The mapping's lifetime has to cover the longest job that uses the exchanged token; for example,
  a security scan in a pull-request workflow fails midway when the token expires, and a longer
  mapping lifetime is the remedy.
- Whether a global and a project-scoped mapping of the same provider may share a name is
  unspecified.

Security: an admin-kind mapping with a weak claims filter hands full administrative rights to
every workload that matches. An admin-scoped token can be issued for a username that does not
exist (this holds for scoped tokens in general).

Interactions: the scope expression uses the same scope language as access tokens created through
the token API ([access-tokens.md](access-tokens.md)). The resulting permissions come from the
permission policies, groups and roles in [identity-and-access.md](identity-and-access.md).

### CIOIDC-008 · Claims filter matching
Priority: **MVP**

The claims filter decides whether a mapping applies to an incoming token.

- Shape: a JSON object. Each key names a claim of the incoming token, each value states what the
  claim must contain. Additional keys act as additional restrictions, so all listed claims must
  match. This conjunctive reading is inferred from the examples, not an explicit rule; the spec
  decides.
- Exact value: `{"repository": "octo-org/octo-repo"}`.
- Several allowed values: an array admits any of the listed values,
  `{"repository": ["octo-org/app", "octo-org/lib", "octo-org/tools"]}`.
- Wildcards: `*` and `**` are supported, e.g. `{"repository": "octo-org/*"}` or
  `{"repository": "octo-org/app/**"}`; Azure example `sc://MyOrg/*/MyConnection`. Explicit and
  implied patterns are distinguished, but neither is defined. The exact difference
  between `*` and `**` (single segment versus any depth, as in Ant-style patterns) is unspecified.
- Nested claims: a dot-separated path addresses a value inside nested objects, e.g.
  `custom_claims.department.group` matches `{"custom_claims": {"department": {"group": "engineering"}}}`.
  Several nesting levels are allowed. Nested claims also work in dynamic mappings (CIOIDC-011).
  Case sensitivity of claim paths is unspecified: whether `Custom_claims.department.group`
  (capitalized first segment) addresses the same claim is open.
- Standard claims (`iss`, `sub`, `aud`, …) can be part of the filter. For GitHub, setting `iss`
  to the GitHub Actions issuer in the filter confirms where the claims came from.
- At least one claim per mapping is strongly recommended; without any, everyone who can obtain a
  token from the issuer qualifies (CIOIDC-007).

Unspecified: how a filter value is compared with a token claim that is itself an array (e.g. a
multi-valued `aud` or a group list); whether JSON types are compared strictly (string `"true"`
versus boolean `true`, string `"20"` versus number `20`); whether wildcards are allowed inside
array elements; whether regular expressions are allowed in the filter (they are defined only for
dynamic mappings).

Limits: wildcards add processing cost. Filters should stay small and simple for exchange
latency.

### CIOIDC-009 · Mapping evaluation order
Priority: **MVP**

Exactly one mapping is selected per exchange:

1. If the exchange request carries a project key, the project-scoped mappings of that project are
   evaluated before all global mappings, regardless of numeric priority.
2. Within a tier (project or global), lower numeric priority comes first.
3. Equal priority: the mapping created earlier comes first.
4. The first mapping whose claims filter matches wins; later mappings are not consulted.

Without a project key in the request, global and project-scoped mappings compete in one list,
ordered by priority and then creation time.

Example: a global mapping with priority 1 and a project mapping with priority 5 both match. With
the project key, the project mapping wins; without it, the global mapping wins.

Edge cases:

- When a project key is supplied, whether mappings of other projects are skipped or evaluated
  after the global tier is unspecified.
- Because project-scoped mappings compete globally when no project key is sent, a project
  administrator's mapping with a low priority number and a broad filter can shadow global mappings
  for requests without a project key. This follows from the rules above.
- If the selected mapping's dynamic pattern fails (missing claim, non-matching template), the
  whole request fails (CIOIDC-011). Whether evaluation falls through to the next mapping is
  unspecified.

Limits: putting frequently matched mappings at the top shortens evaluation. Very large mapping
lists should be split across provider configurations or projects (performance guidance).

### CIOIDC-010 · Mapped token subject kinds and their combinations
Priority: **MVP**

One mapping's token may combine several subject kinds, for example user and group permissions
together. The allowed combinations are fixed:

| Kind | Admin | User | Groups | Dynamic user | Dynamic groups | Dynamic resource |
|---|---|---|---|---|---|---|
| Admin | — | no | no | no | no | no |
| User | no | — | yes | no | yes | yes |
| Groups | no | yes | — | yes | no | yes |
| Dynamic user | no | no | yes | — | yes | yes |
| Dynamic groups | no | yes | no | yes | — | yes |
| Dynamic resource | no | yes | yes | yes | yes | — |

Rules behind the table: admin stands alone. A fixed and a dynamic form of the same subject (user
with dynamic user, groups with dynamic groups) exclude each other. Everything else combines.

Subject kinds and their effect:

- Admin: administrative permissions, associated with the configured username.
- User: the permissions of the named user. For a non-existent user see CIOIDC-014.
- Groups: the permissions of the listed groups only.
- Project roles: the permissions of the listed roles in one project (CIOIDC-015).
- Dynamic user, dynamic groups, dynamic resource: see CIOIDC-012 and CIOIDC-013.

Interactions: the scope syntax that encodes these kinds is shared with scoped access tokens; see
[access-tokens.md](access-tokens.md).

### CIOIDC-011 · Dynamic mapping pattern language
Priority: **MVP**

A dynamic mapping derives a username, a group list or a resource scope from a claim of the
incoming token, instead of using a fixed value. The pattern extracts a claim, can validate it, and
can rewrite it. If the referenced claim is absent, the exchange fails.

Syntax: a pipeline of stages separated by `|`, evaluated left to right on a current value.
Whitespace around `|` appears in some examples and not in others.

| Stage | Form | Effect |
|---|---|---|
| Claim template (first stage) | Literal text with one `{{claim}}` placeholder, e.g. `{{actor}}` or `ci{{actor}}ci` | Reads the claim. Literal text around the placeholder must match the value, otherwise the request fails. The placeholder is capture group 1. |
| Regular expression | e.g. `^([^@]+)@example\.com$` | Matches the current value and defines new capture groups. |
| Replacement | e.g. `$1`, `$0@example.com`, `pre$0post`, `$1_$2_ci` | Builds the new current value. `$0` = whole match, `$n` = capture group n, mixed with literal text. |
| Operator | `op:replaceAll("a","b")`, `op:replaceFirst("a","b")`, `op:replaceLast("a","b")` | String replacement on the current value: all, first, or last occurrence. |

Examples:

```text
{{actor}}                         "alice"                 -> "alice"
{{actor}} | $0@example.com        "alice"                 -> "alice@example.com"
({{actor}})@example.com|$1        "alice@example.com"     -> "alice"
ci{{mail}}example.com|$0          "ci-bot@example.com"    -> "ci-bot@example.com"
                                  "dev@example.org"       -> request fails (no "ci" prefix)
ci{{actor}}ci|$1                  "ciuser-12ci"           -> "user-12"
{{actor}} | hub$0ci               "user"                  -> "hubuserci"
{{group}} | $0-ci                 groups a, b, c          -> "a-ci", "b-ci", "c-ci"
{{email}} | ^([^@]+)@example\.com$ | $1_com | op:replaceAll(".","-")
                                  "first.m.last@example.com" -> "first-m-last_com"
{{actor}} | op:replaceAll("-","_") | ^([^_]+)_([^_]+)_([^_]+)_mac$ | $1_$2_$3_ci
                                  "Ab-cd-ef_mac"          -> "Ab_cd_ef_ci"
{{data}} | ^([^-]+)-([^-]+)$ | op:replaceFirst("a","A") | op:replaceLast("t","T") | $1_$2
                                  "team-project"          -> "teAm_projecT"
```

Evaluation model, derived from the examples above: a replacement resolves
`$n` against the most recent match stage (template or regular expression), re-applied to the
current value. Only this reading explains the last example: operators run between the regular
expression and the replacement, yet the replacement sees the altered text. Implementations that
want compatibility with existing patterns should reproduce it.

Applied to a list claim (groups), the pattern runs once per element. Open: the group claim
arrives either as a JSON array or as a comma-separated string ("gitgroup, gitgroup2, …"), or
both forms are accepted; the spec decides.

Unspecified: whether literal text in a claim template is matched literally or as a regular
expression (one example wraps the placeholder in parentheses); how a non-matching regular
expression stage behaves (failure is likely, by analogy with the template); escaping of `|`, `"`
and `$` inside stages; the regular-expression dialect.

Limits: the advanced stages (regular expression, operators) extend the basic template form. The
performance guidance covers claims filters only (CIOIDC-008); the evaluation cost of patterns is
unspecified.

### CIOIDC-012 · Dynamic user and group subjects
Priority: **P1**

With a dynamic user subject, the issued token's username comes from a claim through a pattern
(CIOIDC-011), e.g. GitHub's `actor` claim with a domain suffix appended to match an existing
account naming scheme. With dynamic groups, each value of a group claim is rewritten by the
pattern and the result becomes the token's group list. In the UI this is the "group mapping" token
scope with a pattern field. In the API it is the username pattern and groups pattern of the token
specification.

Edge cases: a missing claim fails the exchange. A derived username that does not exist is treated
as described in CIOIDC-014. Derived group names that do not exist: unspecified.

### CIOIDC-013 · Dynamic resource scope from a claim
Priority: **MVP**

One identity mapping can grant repository-level access derived from the token itself, without one
mapping per repository and without pre-existing permission policies. The token scope kind is
"resource scope" with a pattern (CIOIDC-011). The pattern yields a resource scope expression: a
repository key (exact or pattern), an optional path pattern (Ant-style), and a set of actions.

- A dedicated custom claim, whose name the system fixes, can carry a complete resource scope
  expression; the pattern `{{<that claim>}}` then applies it unchanged. Example: read on
  `docker-local/image/1.0/**`.
- Normalization: when the derived value is a bare repository key, the system expands it to read
  access on all paths of that repository.
- Any claim can feed the pattern, not only the dedicated one:

```text
prefix-{{repo}}|$1                                "prefix-docker-prod"           -> read on docker-prod/**
{{path}} | ^([^/]+)/.*$ | $1                      "docker-prod/viewer/1.2"       -> read on docker-prod/**
{{repo}} | op:replaceAll(".","-") | $1            "my.docker.repo"               -> read on my-docker-repo/**
{{email_path}} | op:replaceAll(".","-") | ^([^@]+)@.*$ | $1
                                                  "team.a@company.com/docker-prod" -> read on team-a/**
```

- The companion CLI offers a repository filter that narrows the resulting resource scope at
  exchange time (CIOIDC-025). Which exchange parameter carries it is unspecified; the exchange
  schema has a parameter described only as "repository" (CIOIDC-023).

Resource scope actions (from the scoped-token model): read, write (deploy or cache), delete or
overwrite, annotate, scan, manage, plus a wildcard for all. Normalized values always get read
only. The only defined way to express write access through a derived value is the dedicated
claim's full expression.

Interactions: GitLab documents a fixed claim set and no way for a job to add claims of its own
(see CIOIDC-033). For GitLab CI, a resource scope must therefore come from the documented claims
such as `project_path`.
Example: derive the repository or package path from the project path to grant "write the
packages this project produces". The resource scope syntax and action set are defined in
[access-tokens.md](access-tokens.md).

### CIOIDC-014 · Transient users from user-subject mappings
Priority: **P1**

When a mapping resolves a username (fixed or dynamic), the system issues a user-subject token
without checking that the user exists. When the token is used and the user does not exist, the
request runs as a transient user: a temporary identity that also receives every group flagged to
add new users automatically ("auto-join" groups). Auto-join groups are resolved when the token is
used and are not recorded in the token.

| Mapping subject | Effective permissions |
|---|---|
| User only | Auto-join groups (plus the user's own grants if the user exists) |
| Groups only | Exactly the listed groups |
| User + groups | Listed groups + auto-join groups |

Recommendation: leave the username empty when the token must be restricted to
the listed groups. The token then gets a synthetic subject, and auto-join expansion does not
happen.

Security: an auto-join group with broad permissions silently widens every user-subject CI token.
Interactions: auto-join groups are defined in [identity-and-access.md](identity-and-access.md).

### CIOIDC-015 · Project-scoped identity mappings
Priority: **P1**

Identity mappings can belong to one project instead of being global. Project administrators can
then onboard their own CI pipelines without a system administrator.

| Aspect | Global mapping | Project-scoped mapping |
|---|---|---|
| Who creates, updates, deletes | System administrator | System administrator, or project administrator whose role includes the resource-management action, for their own project |
| Token subject | User, groups, admin, dynamic kinds | Roles of the one project |
| Visibility | Everyone with access; can be hidden from project administrators (CIOIDC-016) | System administrators and that project's administrators |
| Precedence | Second tier when the exchange names a project | First tier when the exchange names this project (CIOIDC-009) |
| Typical use | CI access shared by several projects | CI access dedicated to one project |

Rules:

- No separate project field exists on create or update. The project is derived from the scope
  expression: a project-role scope names the project key and the roles. A user or group scope
  makes the mapping global. Responses report the derived project key.
- The role list holds one or more roles defined in that project, comma-separated, with names
  containing spaces in double quotes. Predefined roles (e.g. developer, project administrator,
  release manager) and custom roles are allowed.
- A scope may name only one project.
- The project of an existing mapping cannot change; an update whose scope implies another project
  returns 400.
- The exchange request names the project through its project key parameter (CIOIDC-018) to give
  project mappings precedence.

Operation permissions:

| Operation | System administrator | Project admin with resource-management | Project admin without it |
|---|---|---|---|
| Create, update, delete project-scoped mapping | Yes | Own project only | No (403) |
| Create, update, delete global mapping | Yes | No (403) | No (403) |
| List project-scoped mappings | All projects | Own project | Own project |
| List global mappings | Yes | Depends on CIOIDC-016 | Depends on CIOIDC-016 |
| Token exchange | No authentication | No authentication | No authentication |

UI location: select the project in the project selector, open the integrations page, add a
mapping on the Project tab and pick one or more project roles.

Interactions: project roles and the resource-management action are defined in
[identity-and-access.md](identity-and-access.md).

### CIOIDC-016 · Hiding global identity mappings from project administrators
Priority: **P2**

A system-wide security setting hides global identity mappings from project administrators, so
they only see the mappings of projects they administer. UI location: system security settings
(general page), "OIDC settings" section, a checkbox restricting global mapping visibility. Only a
system administrator working in the "all projects" context can change it. Default: unspecified.
Management API for the flag: unspecified.

### CIOIDC-017 · Identity mapping management operations
Priority: **MVP**

Management API for identity mappings, nested under a provider configuration. Every call
authenticates with a bearer access token. The representation is that of CIOIDC-007; responses add
the derived project key.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create mapping | Provider name; mapping representation | Stored mapping with project key, HTTP 200; 400 on invalid input | System administrator; project administrator with resource-management for a mapping in their own project |
| List mappings of a provider | Provider name; optional project key filter | Array of mappings; 404 if the provider is unknown | System administrator or project administrator (visibility per CIOIDC-015/016) |
| Get mapping | Provider name; mapping name | Mapping; 404 if unknown | System administrator or project administrator |
| Update mapping | Provider name; mapping name; full representation | Updated mapping, 200; 400 on invalid input or project change; 404 if unknown | As for create |
| Delete mapping | Provider name; mapping name | 204 without body; 404 if unknown | As for create; global mappings need a system administrator |

401 for bad credentials and 403 for insufficient privilege apply to all operations.

Limits: load guidance keeps mapping create/update below 500 requests per second and throttles
near 1,000. Measured on a three-replica deployment with 6 CPU cores and 3 GB memory per
authentication-service container; other setups differ.

## Token exchange

### CIOIDC-018 · OIDC token exchange operation
Priority: **MVP**

The token endpoint takes a CI provider's JWT and returns a short-lived access token of the system.
Its parameter names follow OAuth 2.0 Token Exchange
([RFC 8693](https://www.rfc-editor.org/rfc/rfc8693)). The request body is a JSON object, not the
form encoding RFC 8693 prescribes; the operation schema declares only a JSON body. Whether the
server requires a JSON content-type header or also accepts the JSON body without any content-type
header is unspecified. The endpoint requires no authentication
of its own: the subject token is the only credential.

Request parameters:

| Parameter | Meaning | Required |
|---|---|---|
| `grant_type` | `urn:ietf:params:oauth:grant-type:token-exchange` | Not marked; always sent |
| `subject_token_type` | `urn:ietf:params:oauth:token-type:id_token`, also for a Cognito access token | Not marked; always sent |
| `subject_token` | The CI provider's JWT | Effectively yes |
| Provider name (non-standard) | Name of the provider configuration (CIOIDC-001) | Effectively yes (the CLI requires it) |
| Project key (non-standard) | Gives that project's mappings precedence (CIOIDC-009, CIOIDC-015) | No |
| Application key (non-standard) | See CIOIDC-022 | No |
| Identity mapping name (non-standard) | See CIOIDC-021 | No |
| Include reference token (non-standard, boolean) | See CIOIDC-020 | No |
| Provider type (non-standard) | Provider type; purpose unspecified. The companion CLI has a provider-type option (CIOIDC-025) that presumably fills it. | No |
| Job ID, run ID, repository, revision, branch (non-standard) | See CIOIDC-023 | No |

The operation schema marks no parameter as required.

Response (HTTP 200):

| Field | Meaning |
|---|---|
| `access_token` | The issued access token |
| `token_type` | `Bearer` |
| `expires_in` | Lifetime in seconds |
| `issued_token_type` | `urn:ietf:params:oauth:token-type:access_token` |
| Username (non-standard) | The username the token represents, e.g. a fixed or derived user |
| Reference token (non-standard) | Present when requested (CIOIDC-020) |
| `scope` | Present in the companion CLI's output (e.g. a user-subject scope). Not listed in the operation schema. |

Errors: 400 for invalid input (for example a body the server cannot parse as JSON, seen
when a reusable workflow lacks its OIDC setup, CIOIDC-003); 401 for an invalid or
expired token or a provider name that matches no configuration (described together as
"authentication required"). The response when the token is valid but no mapping matches is
unspecified.

Behavior:

- The issued token lasts as long as the selected mapping's expiration time (CIOIDC-007) and
  carries the mapping's subject, scope and audience.
- The token works like an API key and is used as a bearer token. Pairing it with the returned
  username as HTTP Basic credentials, for clients that only speak Basic or need a username
  (container registry login), is implied by the username field; whether it is supported is
  unspecified.
- The exchange yields a short-lived token usable in the workflow and with the companion CLI, not a
  temporary username/password for package managers. Using the returned username plus the token as
  Basic credentials is the implied route; no separate password is issued.

Limits: load guidance caps exchanges at roughly 40 per second (same reference deployment as
CIOIDC-017); clients beyond that should be throttled. Error spikes of 409, 502 or 503
signal resource contention.

Security: the endpoint is reachable without credentials, so the claims filters (CIOIDC-008) are
the entire access control. Rate limiting and audit of exchange attempts are the operator's
concern. Whether the token's `jti` is tracked against replay is unspecified.

Interactions: the issued tokens are access tokens of the system with the scope expression of
[access-tokens.md](access-tokens.md). Whether they can be revoked like other access tokens (beyond
the self-revocable flag of CIOIDC-007) and whether they appear in token listings is unspecified.

### CIOIDC-019 · ID-token verification during exchange
Priority: **MVP**

Before mapping, the system verifies the subject token:

- Signature: checked against the provider's JSON Web Key Set. How the key set is located is
  unspecified; the Cognito setup (the discovery document must be reachable, CIOIDC-002) points to
  [OpenID Connect Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html) from the
  provider URL.
- Issuer: the `iss` claim must equal the token-issuer setting, or the provider URL when that is
  empty.
- Audience: the `aud` claim must match the configured audience when one is set; inferred: no
  check when none is set (CIOIDC-002). Open: either this holds, or the provider's audience
  setting does not validate the `aud` claim and only claims listed in a mapping's claims filter
  (CIOIDC-008) are checked, so an `aud` restriction belongs in the filter (the setup of the
  pull-request scanning bot assumes this second reading). The spec decides; an implementation
  should check the configured audience and also honor an `aud` entry in the filter.
- Validity: `exp`, `nbf` and `iat` bound a token's validity; the issued token is short-lived.
  Clock-skew rules are unspecified.
- GitHub type: the organization restriction (CIOIDC-003). Azure type: application-specific keys
  (CIOIDC-004).

Unspecified: key caching and refresh on key rotation, allowed signing algorithms, clock-skew
tolerance, handling of an array-valued `aud`, replay protection through `jti`.

### CIOIDC-020 · Reference token in the exchange response
Priority: **P2**

The exchange request can ask for a reference token in addition to the access token. A reference
token is a short opaque handle that stands for the full access token
([access-tokens.md](access-tokens.md)); the response then contains it. Every identity mapping
also gets a reference token, meant for the calling service to authenticate with (CIOIDC-007); how
this relates to the per-request flag is unspecified. Default of the flag: unspecified.

### CIOIDC-021 · Explicit identity-mapping selection on exchange
Priority: **P2**

The exchange request accepts an identity mapping name. Semantics (whether it bypasses priority
ordering, whether the named mapping's claims filter must still match, error when it does not) are
unspecified. A compatible implementation should at least require that the named mapping's filter
matches.

### CIOIDC-022 · Application-scoped exchange
Priority: **Later**

The exchange request accepts an application key. The companion CLI requires it when provider
configurations are scoped to an application. The OIDC feature does not define that entity; the
catalog's application entity is DIST-054 in
[distribution-and-lifecycle.md](distribution-and-lifecycle.md). How configurations are scoped to an
application, and how the key affects mapping selection, is unspecified.

### CIOIDC-023 · CI run metadata on exchange
Priority: **P2**

The exchange request accepts optional CI run metadata: job ID, run ID, repository, revision and
branch. Their purpose is unspecified. Plausible uses are audit records and linking the issued
token to build metadata ([builds-and-ci.md](builds-and-ci.md)). Whether the
values are checked against token claims is unspecified; since the caller can choose them freely,
they must not be trusted for authorization.

## Client-side integration

### CIOIDC-024 · Workflow snippet generator
Priority: **P2**

Once a GitHub-type provider configuration has been filled in, the configuration page offers a
"show snippet" action that renders a ready-to-paste workflow snippet with the base URL, the
provider name and the audience. Whether the configuration must be saved first is unspecified. There are
two variants: one using the companion CLI's GitHub Actions setup step, and one using plain `curl`
against the exchange operation. A copy button puts it on the clipboard. Available for the GitHub
type only.

Interactions: client setup instructions in [ui-inventory.md](ui-inventory.md). A GitLab CI variant
(an `id_tokens` block plus the exchange call) would be the counterpart for GitLab-first
deployments; none is offered.

### CIOIDC-025 · Companion CLI token exchange
Priority: **P2**

The companion CLI has a command that exchanges a CI ID token for an access token. It is meant to
run inside CI jobs, because the ID tokens exist only there.

| Input | Meaning | Default |
|---|---|---|
| Provider name (first positional argument) | Provider configuration name | Required |
| ID token (second positional argument, a flag, or an environment variable) | The CI provider's JWT | — |
| Base URL | The system's base URL | The CLI's default configured server |
| Provider type | `GitHub`, `Azure` or generic OIDC | GitHub |
| Audience | Audience to request or expect | — |
| Application key | CIOIDC-022 | — |
| Project key | CIOIDC-015 | — |
| Repository | Narrows the resulting resource scope (CIOIDC-013) | — |
| Output format | `json` or `table` | `json` |

Output: access token, username, issued token type, scope, token type, printed to standard output.
The command does not persist the token; the user stores it with the CLI's separate
server-configuration command for later commands. The separate path in which the
CLI performs the exchange itself is the OIDC-enabled server configuration (see the GitLab pattern
below).

Errors as surfaced by the CLI: 401 (invalid/expired token or provider-name mismatch), 404 (wrong
base URL), unsupported provider type, missing positional arguments.

CI patterns:

- GitLab: the job declares its ID token under the environment-variable name the CLI reads (via
  `id_tokens:`, CIOIDC-030) and configures the server with the provider name. The CLI then
  exchanges the token without explicit arguments.
- GitHub Actions: the CLI's setup action takes the provider name and audience as inputs and
  performs the exchange. In reusable workflows it must be repeated (CIOIDC-003).

Interactions: other CLI capabilities in [builds-and-ci.md](builds-and-ci.md).

### CIOIDC-026 · GitHub App onboarding mode
Priority: **P2**

An automatic alternative to manual configuration ("advanced mode") for GitHub, based on a GitHub
App published for the system:

| Aspect | Automatic mode (GitHub App) | Advanced mode (manual) |
|---|---|---|
| Use case | Quick onboarding of individual repositories | Shared or more elaborate configurations |
| Repository mapping | One provider configuration per GitHub repository | One configuration for many repositories |
| Token kinds | User, admin, group | Project roles, user, admin, group |

Flow: installing the app from GitHub Marketplace on selected repositories opens an installer that
asks for the system's URL and for consent. In the system's integration setup, the administrator
picks repositories, chooses the token type and services (all services pre-selected), and can
let the system open a pull request in each repository. The pull request contains a workflow with
`id-token: write` and the setup step bound to the generated provider configuration (named, for
example, `<owner>/<repository>@github`). Later actions: add repositories, edit a
repository's identity mapping, disconnect a repository (with confirmation). UI location:
integrations page, a "Git repositories" tab. Must be started from the "all projects" context.

Edge cases: reinstalling the app after removal can fail on GitHub's side because a marketplace
subscription is still active. The app must be uninstalled and the marketplace plan cancelled
first.

## Cloud-role identity federation

### CIOIDC-027 · Cloud IAM role binding to a user
Priority: **P2**

Instead of OIDC, AWS workloads can authenticate with their IAM role (CIOIDC-028). An administrator
binds one AWS IAM role ARN to one existing user of the system. The binding is one-to-one: a role
can be bound to only one user.

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Bind role to user | Username; IAM role ARN (both required) | 204 without body; 404 if the user does not exist; 409 if the role is already bound to another user; 400 on invalid input | System administrator |
| Get binding | Username | Username and role ARN; 400 if the user does not exist; 404 if no role is bound | System administrator |
| Remove binding | Username | 204 without body; 400 if the user does not exist | System administrator |

Recommended setup: a dedicated user for the workload with only the needed repository
permissions. Whether a user can hold several roles is unspecified (the API takes one role per
call and addresses bindings by username). Behavior when binding a second role to the same user:
unspecified (replace or reject).

### CIOIDC-028 · Signed cloud caller-identity token exchange
Priority: **P2**

A dedicated exchange operation, separate from the OIDC one and with no provider configuration. The
client signs an AWS STS `GetCallerIdentity` request with its current AWS credentials (AWS
Signature Version 4) and sends the signature headers, not the request itself, to the system. The
system validates the caller through STS `GetCallerIdentity`, finds the user bound to the returned
IAM role (CIOIDC-027), and issues an access token for that user. The AWS secret key never leaves
the workload.

| Header | Content | Required |
|---|---|---|
| `Authorization` | SigV4 signature starting with `AWS4-HMAC-SHA256` or `AWS4-ECDSA-P256-SHA256`; credential scope ending in `/sts/aws4_request` | Yes |
| `X-Amz-Date` | UTC timestamp `yyyyMMdd'T'HHmmss'Z'`; rejected if it lies in the future or outside a 12-hour window | Yes |
| `X-Amz-Security-Token` | Session token of temporary credentials (assumed role, instance profile, environment) | Yes |
| `X-Amz-Region-Set` | AWS region (e.g. `us-east-1`) or `aws-global` | Yes |

Body (JSON): requested lifetime in seconds and a description (example: 3600 seconds, "CI pipeline
token"). Response: an access token; exact fields unspecified. Defaults and maximum lifetime:
unspecified. Because the session-token header is mandatory, the operation is built for temporary
AWS credentials; long-term access keys without a session token do not fit (inferred from the
header table).

Prerequisites: the role may call `sts:GetCallerIdentity`. AWS grants this by default, but
restrictive permission boundaries can remove it.

Unspecified: how an assumed-role session ARN returned by STS is normalized to the bound role ARN;
which STS endpoint the system calls; whether the signed request's age is checked beyond the
12-hour window.

Fits Jenkins on AWS, AWS CodeBuild, EKS pods, and any workload that already has an IAM role. The
OIDC route (CIOIDC-002, e.g. Cognito) fits when claims-based mappings are wanted.

### CIOIDC-029 · Passwordless image pulls for Kubernetes workloads
Priority: **P2**

Kubernetes clusters can pull and push container images without stored registry secrets:

- A secret-rotating operator in the cluster gets AWS credentials through IRSA (IAM roles for
  service accounts: the cluster's OIDC provider, a service-account annotation,
  `AssumeRoleWithWebIdentity`) or through EKS Pod Identity (no annotation, an EKS API
  association). It turns them into short-lived registry credentials and keeps those in Kubernetes
  image-pull secrets, rotating them. The operator signs with SigV4A, never sends the AWS secret
  key, and relies on the IAM role binding (CIOIDC-027); that it calls the operation of CIOIDC-028
  is likely but unconfirmed.
- Alternatively, a kubelet credential provider plugin (targeting EKS, AKS and GKE) supplies
  credentials to the kubelet for image pulls directly. For EKS it can use either the Cognito OIDC
  route (CIOIDC-002) or the IAM role route (CIOIDC-028).

System-side setup: a dedicated user with the repository permissions, a scoped token for it if
needed, and the IAM role binding (CIOIDC-027). AWS-side setup: an IAM role whose trust policy
admits the cluster's service account, and a policy allowing `sts:GetCallerIdentity` (and
`iam:GetRole`) on that role. The operator and the credential provider are separate client
components; their installation is out of this page's scope (see
[ha-and-deployment.md](ha-and-deployment.md) for deployment topics).

## GitLab CI identity facts

These entries describe GitLab, not the system. They record what an implementation must know to
validate GitLab ID tokens and write safe mappings. Source: GitLab documentation as published on
2026-10-28 (GitLab 19.x). Priorities state how urgently the system needs the fact.

### CIOIDC-030 · `id_tokens` keyword
Priority: **MVP**

Source: [GitLab CI/CD YAML: `id_tokens`](https://docs.gitlab.com/ci/yaml/#id_tokens),
[ID token authentication](https://docs.gitlab.com/ci/secrets/id_token_authentication/).

- Job keyword, also allowed under `default:`. A job's own `id_tokens` replaces the default
  completely; the two are not merged.
- Value: a map of variable name → token definition. Each definition has the required sub-key
  `aud`, which takes a single string, an array of strings, or CI/CD variables.
- Each entry yields one JWT, exposed to the job as a CI/CD variable of that name and usable in
  `script`, `before_script` and `after_script`.
- Several tokens with different audiences per job are allowed; the relying party should reject
  tokens whose `aud` is not its own.

```yaml
publish:
  id_tokens:
    REGISTRY_ID_TOKEN:
      aud: https://packages.example.com
  script:
    - ./exchange-and-publish.sh "$REGISTRY_ID_TOKEN"
```

- The predecessors `CI_JOB_JWT` and `CI_JOB_JWT_V2` were removed in GitLab 17.0
  ([cloud services](https://docs.gitlab.com/ci/cloud_services/)). Using them, a missing
  `id_tokens:` block, a mismatched audience, or a mismatched provider name are the documented
  causes of 401 errors at relying parties.
- GitLab ID tokens contain no `preferred_username` claim by default.

### CIOIDC-031 · ID-token signing, issuer, keys and lifetime
Priority: **MVP**

Source: [ID token authentication](https://docs.gitlab.com/ci/secrets/id_token_authentication/),
[Connect to cloud services](https://docs.gitlab.com/ci/cloud_services/).

- Signature: RS256, with a dedicated private key of the GitLab instance.
- Issuer (`iss`): the URL of the GitLab instance, e.g. `https://gitlab.example.com`.
- Public keys of GitLab.com: `https://gitlab.com/oauth/discovery/keys`. Verified on 2026-10-28 by
  fetching `https://gitlab.com/.well-known/openid-configuration`: issuer `https://gitlab.com`,
  `jwks_uri` as above, ID-token signing algorithm RS256. Self-managed instances publish the same
  paths under their own base URL; inferred from the discovery convention.
- Lifetime: the job's timeout if one is set, otherwise 5 minutes. In the documented example token
  `nbf` lies 5 seconds before `iat` and `exp` one hour after it, so verifiers need a small clock
  tolerance.
- Default audience: the GitLab instance's domain, when not set by `id_tokens`. The YAML keyword
  itself requires `aud`.
- Failure modes on GitLab's side: `400: missing token` when ID-token prerequisites are missing; an
  instance without a signing key cannot issue tokens; issuance is refused when the `sub` claim
  contains a project path that another project used before (CIOIDC-034).

### CIOIDC-032 · Standard JWT claims in GitLab ID tokens
Priority: **MVP**

Source: [ID token authentication, token payload](https://docs.gitlab.com/ci/secrets/id_token_authentication/#token-payload).

| Claim | Meaning |
|---|---|
| `iss` | Issuer: GitLab calls it the instance's domain; the example payload and the GitLab.com discovery document use the full HTTPS URL (`https://gitlab.example.com`, `https://gitlab.com`) |
| `sub` | Subject; default `project_path:{group}/{project}:ref_type:{type}:ref:{branch_name}`, configurable per project (CIOIDC-034) |
| `aud` | Audience from `id_tokens`; instance domain by default |
| `exp` | Expiration time |
| `nbf` | Not valid before |
| `iat` | Issued at |
| `jti` | Unique token ID |

### CIOIDC-033 · GitLab-specific ID-token claims
Priority: **MVP**

Source: [ID token authentication, token payload](https://docs.gitlab.com/ci/secrets/id_token_authentication/#token-payload).
GitLab calls these its custom claims; the set is defined by GitLab, and GitLab's documentation
offers no way for a job to add claims of its own.

<details>
<summary>All GitLab custom claims</summary>

| Claim | Present | Meaning |
|---|---|---|
| `project_id` | Always | ID of the project running the job; in a merge request pipeline, the source project |
| `project_path` | Always | Path of that project; in a merge request pipeline, the source project |
| `namespace_id` | Always | Namespace ID of that project; source project in merge request pipelines |
| `namespace_path` | Always | Namespace path of that project; source project in merge request pipelines |
| `job_project_id` | Always (GitLab 18.4+) | ID of the project that actually runs the job |
| `job_project_path` | Always (18.4+) | Path of the project that runs the job |
| `job_namespace_id` | Always (18.4+) | Namespace ID of the project that runs the job |
| `job_namespace_path` | Always (18.4+) | Namespace path of the project that runs the job |
| `source_project_id` | Always (19.5+) | ID of the project that holds the source code; the merge request's source project |
| `source_project_path` | Always (19.5+) | Path of that project |
| `source_namespace_id` | Always (19.5+) | Namespace ID of that project |
| `source_namespace_path` | Always (19.5+) | Namespace path of that project |
| `user_id` | Always | ID of the user executing the job |
| `user_login` | Always | Username of that user (changeable by the user) |
| `user_email` | Always | Email of that user (changeable by the user) |
| `user_access_level` | Always | Access level of that user |
| `user_identities` | When the user opts in through a preference | The user's external identities (list of provider + external UID) |
| `pipeline_id` | Always | Pipeline ID |
| `pipeline_source` | Always | How the pipeline was triggered (CIOIDC-035) |
| `job_id` | Always | Job ID |
| `ref` | Always | Git ref name; in a merge request pipeline, the source branch |
| `ref_type` | Always | `branch` or `tag` |
| `ref_path` | Always | Fully qualified ref, e.g. `refs/heads/main`; source branch in merge request pipelines |
| `ref_protected` | Always | `true` if the ref is protected (CIOIDC-037) |
| `groups_direct` | User directly in 0–200 groups and feature flag `ci_jwt_groups_direct` enabled (off by default; GitLab 17.3+) | Paths of the user's direct groups; omitted above 200 groups |
| `environment` | Job specifies an environment | Environment name |
| `environment_protected` | Job specifies an environment | `true` if that environment is protected |
| `deployment_tier` | Job specifies an environment | `production`, `staging`, `testing`, `development` or `other` |
| `environment_action` | Job specifies an environment | `start` (default), `prepare`, `stop`, `verify` or `access` |
| `runner_id` | Always | ID of the runner |
| `runner_environment` | Always | `gitlab-hosted` or `self-hosted` |
| `sha` | Always | Commit SHA |
| `ci_config_ref_uri` | Always; `null` unless the pipeline definition lives in the same project | Ref path of the top-level pipeline definition, e.g. `gitlab.example.com/my-group/my-project//.gitlab-ci.yml@refs/heads/main` |
| `ci_config_sha` | Always; `null` under the same condition | Commit SHA of that definition |
| `project_visibility` | Always | `internal`, `private` or `public` |
| `job_source` | Always (18.9+) | Job source |
| `job_config` | Job triggered by a policy (18.9+) | Origin metadata of policy jobs: `sha` and `url` of the policy configuration |

</details>

Value types: in GitLab's documented example payload, IDs and booleans are JSON strings
(`"project_id": "20"`, `"ref_protected": "false"`), while `runner_id`, `iat`, `nbf` and `exp` are
numbers. `user_identities` is an array of objects, `groups_direct` an array of strings,
`job_config` an object.

Trust guidance from GitLab: prefer immutable identifiers (`project_id`, which never changes;
`namespace_id`, stable while the project stays in its namespace) next to path claims. Paths change
on rename or transfer. Never rely on `user_login` or `user_email` alone.

### CIOIDC-034 · `sub` claim format and customization
Priority: **MVP**

Source: [Connect to cloud services](https://docs.gitlab.com/ci/cloud_services/),
[Projects API, update a project](https://docs.gitlab.com/api/projects/#update-a-project).

- Default format: `project_path:{group}/{project}:ref_type:{type}:ref:{branch_name}`, i.e. pairs
  of component name and value joined by `:`.
- Per-project customization through the project setting `ci_id_token_sub_claim_components`, set
  through the projects API (GitLab documents no UI for it). Default `["project_path", "ref_type", "ref"]`. The first element must be
  one of `project_path`, `project_id`, `job_project_path`, `job_project_id`,
  `source_project_path`, `source_project_id`. Further elements may be `ref_type`, `ref`,
  `ref_protected`, `environment_protected`, `deployment_tier`. History: setting since 17.10;
  `environment_protected` and `deployment_tier` since 18.7; `project_id` first since 19.1; `job_*`
  and `source_*` first since 19.5.
- Example with the project ID: `project_id:{id}:ref_type:{type}:ref:{branch_name}`.
- Wildcard filters GitLab documents for cloud trust policies:

| Filter | `sub` pattern |
|---|---|
| Any branch of one project | `project_path:mygroup/myproject:ref_type:branch:ref:*` |
| One project, main branch | `project_path:mygroup/myproject:ref_type:branch:ref:main` |
| All projects of a group, main branch | `project_path:mygroup/*:ref_type:branch:ref:main` |
| A tag | `project_path:mygroup/*:ref_type:tag:ref:1.0` |

- Path reuse protection: GitLab refuses to issue ID tokens ("ID token issuance is disabled") when
  the configured `sub` contains a `project_path`, `job_project_path` or `source_project_path` that
  another project used before. A new project therefore cannot inherit trust meant for an old one.
  The fix is to start `sub` with the matching ID component and update the relying party's
  mappings.
- Escaping of `:` inside values (branch names may contain it) is not documented by GitLab.

### CIOIDC-035 · Pipeline source values
Priority: **MVP**

Source: [`CI_PIPELINE_SOURCE` values](https://docs.gitlab.com/ci/jobs/job_rules/#ci_pipeline_source-predefined-variable).
The `pipeline_source` claim takes the same values:

| Value | Pipeline created by |
|---|---|
| `api` | The pipelines API |
| `chat` | A ChatOps command |
| `external` | A CI service other than GitLab |
| `external_pull_request_event` | An external pull request on GitHub |
| `merge_request_event` | A merge request created or updated (merge request pipelines, merged results, merge trains) |
| `ondemand_dast_scan` | An on-demand DAST scan |
| `ondemand_dast_validation` | DAST site-profile validation |
| `parent_pipeline` | A parent pipeline (child pipeline) |
| `pipeline` | A multi-project pipeline |
| `push` | A Git push (branches and tags) |
| `schedule` | A pipeline schedule |
| `security_orchestration_policy` | A scheduled scan execution policy |
| `trigger` | A trigger token |
| `web` | "New pipeline" in the GitLab UI |
| `webide` | The Web IDE |

### CIOIDC-036 · Fork and merge request pipelines
Priority: **MVP**

Source: [Merge request pipelines, forks](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/#use-with-forked-projects),
[cloud services, project that runs the job](https://docs.gitlab.com/ci/cloud_services/#use-the-project-that-runs-the-job-as-the-subject),
[token payload](https://docs.gitlab.com/ci/secrets/id_token_authentication/#token-payload).

- Merge request pipelines have `pipeline_source = merge_request_event`. `ref` and `ref_path` name
  the source branch, not the target branch.
- Merge request from a fork, default: the pipeline is created and runs in the fork, with the
  fork's CI configuration, variables and runners. All project claims name the fork. To a relying
  party this looks like any pipeline of the fork's project.
- Merge request from a fork, run in the parent project (triggered by a parent-project member):
  the pipeline runs in the parent project with the CI configuration from the fork's branch, the
  parent's settings, variables and runners, and the triggering member's permissions. Claims:
  `project_path`/`project_id` (and `source_project_*`) name the fork; `job_project_*` name the
  parent; `ref` is the fork's branch name, which can be `main`; `ref_protected` is `false`
  whenever source and target projects differ.
- A `sub` built from `job_project_*` hides that the code comes from a fork. GitLab recommends
  adding `ref_protected` to the `sub` components and requiring `ref_protected:true` to exclude
  such pipelines.
- Fork merge request pipelines never get protected variables or protected runners. Same-project
  merge request pipelines get them only if both branches are protected, the triggering user may
  push/merge to the target branch, and the project allows it (setting since GitLab 18.1).
- Running fork pipelines in the parent project can be disabled per project
  (`ci_allow_fork_pipelines_to_run_in_parent_project`, API only). Pipelines created earlier keep
  their original context, including on retry.

### CIOIDC-037 · Protected refs and protected environments
Priority: **MVP**

Source: [Pipeline security on protected branches](https://docs.gitlab.com/ci/pipelines/#pipeline-security-on-protected-branches),
[Protected environments](https://docs.gitlab.com/ci/environments/protected_environments/),
[deployment tier](https://docs.gitlab.com/ci/yaml/#environmentdeployment_tier),
[environment actions](https://docs.gitlab.com/ci/environments/#access-an-environment-for-preparation-or-verification-purposes).

- `ref_protected` is `true` when the job's branch or tag is protected. On protected branches only
  users allowed to push or merge to that branch can run manual pipelines, scheduled pipelines,
  trigger-token pipelines, manual jobs, and job retries or cancels. Protected variables and
  protected runners are available only to pipelines of protected refs.
- `environment_protected` is `true` when the job's environment is protected. Only users and groups
  on the environment's "allowed to deploy" list can run deployments to it. Protecting an
  environment requires the Maintainer role; GitLab administrators can use every environment.
  Protected environments are a GitLab Premium and Ultimate feature; on the Free tier a filter on
  `environment_protected = true` cannot match (inferred).
- Jobs with `environment_action` `prepare`, `verify` or `access` use an environment without
  creating a deployment; GitLab presents this as a way to give build jobs the environment's
  variables and protection. The claim therefore tells a relying party whether a job is an actual
  deployment (`start`) or only touches the environment.
- `deployment_tier` comes from `environment:deployment_tier` or is guessed from the environment
  name. It cannot be set in the UI.
- Consequence for mappings: a grant that writes release packages should require
  `ref_protected = true` (and, for deployments, `environment_protected = true`) together with a
  stable project identifier. Branch-name filters alone are forgeable by anyone who can push a
  branch (see CIOIDC-036).

### CIOIDC-038 · `CI_JOB_TOKEN` properties
Priority: **P1**

Source: [CI/CD job token](https://docs.gitlab.com/ci/jobs/ci_job_token/),
[token prefixes](https://docs.gitlab.com/security/tokens/#token-prefixes),
[predefined variables](https://docs.gitlab.com/ci/variables/predefined_variables/).

- Created for each job just before it runs and exposed as the predefined variable `CI_JOB_TOKEN`.
  It is valid only while the job runs and is revoked when the job finishes. It also becomes
  invalid when the job is erased or the project is being deleted.
- Same value as `CI_REGISTRY_PASSWORD`. `CI_REPOSITORY_URL` embeds it for cloning as user
  `gitlab-ci-token`.
- Permissions: the access level of the user who triggered the job (push, manual run, schedule
  owner), limited to a fixed set of resources (CIOIDC-039) and to the allowlist (CIOIDC-040).
- Format: JWT by default since GitLab 19.0. A top-level group can switch back to the legacy
  format until GitLab 20.0. GitLab's prefix table lists `glcbt-` for CI/CD job tokens. The claims
  of the JWT format and whether third parties can verify it offline are not documented; the
  documented way for a third party to identify a job token is the Jobs API (CIOIDC-039).
- GitLab masks the token in job logs. `base64` without `-w0` wraps JWT-format tokens and breaks
  them.
- It is not an ID token requested for a specific audience; GitLab documents no audience binding
  for it. A relying party that accepts it must call GitLab to resolve it, and nothing restricts
  the token to that relying party.

### CIOIDC-039 · API endpoints that accept `CI_JOB_TOKEN`
Priority: **P1**

Source: [CI/CD job token, job token access](https://docs.gitlab.com/ci/jobs/ci_job_token/#job-token-access),
[Jobs API, retrieve a job by job token](https://docs.gitlab.com/api/jobs/#retrieve-a-job-by-job-token).

Presentation: header `JOB-TOKEN: <token>` (recommended), form field `token`, data field
`job_token`, or query `?job_token=` (discouraged). The Jobs API's `GET /job` also accepts
`Authorization: Bearer <token>`. Git over HTTPS uses user `gitlab-ci-token` with the token as
password, even when HTTPS cloning is otherwise disabled. Job tokens cannot authenticate GraphQL.

Resolving a token (relevant for a relying party): `GET /api/v4/job` returns the job the token
belongs to. The response includes job ID, name, status, source, ref, tag flag, stage, pipeline
(ID, project ID, ref, SHA, status), commit, and the triggering user (ID, username, name, state).
The project appears only as a numeric ID inside the pipeline object, without its path, and the call
works only while the job runs (the token is revoked afterwards, CIOIDC-038).
`GET /api/v4/job/allowed_agents` returns the job, pipeline, project with its group IDs, user, and
the Kubernetes agents allowed for it.

<details>
<summary>Resources reachable with a job token</summary>

| Resource | Access |
|---|---|
| Badges API | All endpoints |
| Branches API | `GET /projects/:id/repository/branches` |
| Commits API | `GET /projects/:id/repository/commits/:sha`, `…/:sha/merge_requests`, `…/:sha/refs` |
| Container registry | As `CI_REGISTRY_PASSWORD`, for the job's own project registry |
| Container registry API | Only the job's own project registry |
| Package registry | Authentication to the registry |
| Terraform module registry | Authentication to the registry |
| Secure files | Through the `glab securefile` command |
| Deployments API | All endpoints |
| Environments API | All endpoints |
| Files API | `GET /projects/:id/repository/files/:file_path/raw` |
| Jobs API | Only `GET /job` |
| Job artifacts API | Download endpoints only |
| Merge requests API | `GET /projects/:id/merge_requests`, `GET /projects/:id/merge_requests/:merge_request_iid` |
| Notes API | Read notes of a merge request (list and single) |
| Packages API | All endpoints |
| Pipeline trigger tokens API | Only `POST /projects/:id/trigger/pipeline` |
| Pipelines API | Only `PUT /projects/:id/pipelines/:pipeline_id/metadata` |
| Release links API, Releases API | All endpoints |
| Repositories API | `GET /projects/:id/repository/archive`, `GET /projects/:id/repository/changelog` |
| Tags API | `GET /projects/:id/repository/tags`, `GET /projects/:id/repository/tags/:tag_name` |

</details>

### CIOIDC-040 · Job-token allowlist and scope
Priority: **P1**

Source: [Control job token access](https://docs.gitlab.com/ci/jobs/ci_job_token/#control-job-token-access-to-your-project),
[Job token scope API](https://docs.gitlab.com/api/project_job_token_scopes/),
[fine-grained permissions](https://docs.gitlab.com/ci/jobs/fine_grained_permissions/).

- Inbound allowlist per target project. By default only the project's own jobs may use their job
  tokens against it. Other projects or groups must be added. A group entry covers all its
  subgroups and projects at any depth, evaluated at use time.
- Allowlisting grants nothing by itself: the triggering user must be a member of the target
  project with sufficient permissions.
- Limits: at most 200 groups and 200 projects per allowlist, counted separately. Managing it
  requires Maintainer or Owner.
- Public and internal projects: jobs of non-allowlisted projects can still fetch artifacts, use
  the container and package registries, read releases, deployments and environments, and access
  the repository. Setting those features to "only project members" removes this.
- Self-managed and Dedicated: administrators can enforce the allowlist instance-wide. Without
  enforcement a project can allow all projects (discouraged).
- Fine-grained permissions (generally available since GitLab 18.3) restrict an allowlist entry to
  named REST endpoints (read or read-and-write per resource).
- Git push with the job token: off by default, own project only. Cross-project push from
  allowlisted projects is possible since 19.1 under extra conditions. Pushes by job token trigger
  no pipelines.
- An authentication log lists other projects that used job tokens against the project: up to 100
  events in the UI, more via CSV download, with up to 5 minutes delay.
- API: read and update the scope setting, and list, add and remove allowlisted projects and groups
  under `/projects/:id/job_token_scope`. Disabling the scope fails with 400 when the instance
  enforces it.
- Relevance for the system: the allowlist governs access to GitLab's own resources. It does not
  limit which external service a leaked job token can be presented to.

## Notes for implementers

- The exchange endpoint is unauthenticated, so claims filters are the whole security boundary.
  For public issuers (GitLab.com, GitHub Actions) anyone with an account can mint a valid token
  with any audience. An empty or path-only filter is an open door; pin `namespace_id` and
  `project_id` (or the GitHub organization) in every mapping.
- Accept a JSON request body: existing CI scripts send JSON with the RFC 8693 parameter names
  plus a non-standard provider-name parameter. Supporting the form encoding of RFC 8693 as well
  costs little.
- A provider-name mismatch surfaces as 401, the same status as an invalid token. The status for
  "no mapping matched" is unspecified. Logs need to distinguish "unknown provider", "token
  invalid" and "no mapping matched", or operators cannot debug onboarding.
- The lifetime is entered in minutes in the UI and in seconds over the API; the cap is 24 hours.
  Mixing units silently produces tokens 60× too long or too short.
- GitLab serializes most ID-token claim values as strings (`"false"`, `"20"`), and `aud` may be an
  array. Define the comparison rules for claims filters explicitly; a strict boolean
  `ref_protected: true` filter never matches a GitLab token.
- Mapping order is "project tier (only if a project key is sent) → priority → creation time →
  first match". Without a project key, project-scoped mappings compete with global ones and can
  shadow them.
- A user-subject mapping whose user does not exist still yields a working token that inherits all
  auto-join groups. Group-only mappings must leave the username empty to stay restricted.
- Dynamic patterns fail the whole exchange when a claim is missing or a template does not match.
  Compatibility with existing patterns depends on the `$n` semantics derived in CIOIDC-011.
- GitLab merge request pipelines carry the source branch in `ref`. Fork pipelines run in the
  parent project report the fork in `project_path` and the parent in `job_project_path`, with
  `ref_protected = false`. Write grants should require `ref_protected = true`.
- `CI_JOB_TOKEN` is not an audience-bound OIDC token. Accepting it means calling GitLab's
  `GET /job` per request (or caching the result for the job's lifetime) and trusting a credential
  that every GitLab endpoint in CIOIDC-039 also accepts. Keep that path optional and off by
  default.
