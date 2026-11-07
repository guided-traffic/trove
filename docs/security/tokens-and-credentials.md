# Tokens and credentials

Perspective: how a caller proves who it is on the client listener, how a token is made, stored, checked and ended, and what
a replica remembers about it. The code is [internal/auth](../../internal/auth) (the stage),
[internal/auth/token](../../internal/auth/token) and [internal/auth/principal](../../internal/auth/principal); the developer
view is [identity.md](../developer/identity.md). Sessions, the OIDC login and the break-glass account belong to the login
area and are not described here.

## Credential sources and precedence

The stage ([stage.go](../../internal/auth/stage.go)) evaluates exactly one credential, in this order, and a present but
invalid one is a 401 that never falls through to a lower source or to anonymous:

| Order | Source | Rule |
|---|---|---|
| 1 | `X-NuGet-ApiKey: <token>` or `<any>:<token>` | Only on a route that declares the API key (`Route.APIKey`: NuGet's push, delete, relist and symbol push) and only while `auth.clients.nuget_api_key_header` is on. The value, or its part after the last `:`, must pass the syntax step to be a credential; any other value (the placeholder `az`) is ignored and order 2 applies. A token there decides over `Authorization`, even of another principal. |
| 2 | `Authorization: Bearer <token>` or `Basic base64(<any>:<token>)` | The value must be a trove token (prefix, length 57, base62, CRC-32). A JWT, an identity-provider token or a password, the break-glass password included, is refused at the syntax step without a database query. The Basic username is recorded and ignored. `auth.clients.basic.enabled: false` refuses Basic. |
| 3 | Session cookie `__Host-trv_session` | Only where the endpoint's surface holds `ui` and the path allows it (`/api/v1/` all methods, `/files/` GET HEAD PUT DELETE, `/api/<format>/` GET HEAD), and never on a route that declares the API key, whatever its method; CSRF is the session validator's. A replica without a session validator ignores the cookie. |
| 4 | none | Anonymous only if the endpoint, `auth.anonymous.allowed` and the runtime switch `anonymousAccess` allow it, and never an unsafe method on `/api/v1/`; or a route that is always open or declared public. |

The path rules read two forms of the path (`rulePaths` in [stage.go](../../internal/auth/stage.go)). The rules that limit a
request - the hard limit on an unsafe anonymous request to `/api/v1/`, the cookie's path rule of order 3 and the challenge -
read the unescaped path, the one `ServeMux` routes by: it unescapes each segment, so `POST /api/v%31/projects` reaches the
management API and is refused like `POST /api/v1/projects`. The rules that open a route - the always-open routes, the npm login
start and the exchange routes - read the escaped path as received, so `POST /api/v1/tokens/revok%65` is not open, and a `%` in
a named segment of a repository address is no address, as the dispatcher reads it.

The stage runs before the dispatcher, so it learns whether a request reaches an API-key route from the dispatcher's own match
(`EndpointRules.APIKeyRoute`, wired to `Dispatcher.APIKeyRoute` in [wire_identity.go](../../internal/app/wire_identity.go),
Q-AUTH-186): the endpoint's surface, the prefix, the key shape, the clean path and the format's `Match`. The header is therefore
not a credential on a download, a document, the management API, `/files/`, or an endpoint without the package surface. A token
in the header passes the endpoint's token rules, the throttle and the last-use record exactly like a Bearer token; the
principal's method is `api_key` in the audit event and `nuget_apikey` in the request log and `trove_auth_attempts_total`. The
cookie rule makes a cross-site `POST` relist from a signed-in browser unauthenticated (Q-AUTH-187). A page cannot send
`X-NuGet-ApiKey` across sites either: the header needs a CORS preflight, and trove answers preflights only under `/api/v1/` and
never allows that header ([cors.go](../../internal/api/v1/cors.go)); a page would also need the token itself.

The GitLab job-token extension point is not evaluated; no credential is read from a query parameter or a path. An exchange
route (`POST` of the CI exchange and the two npm exchanges) receives the ID token in `Authorization` and the stage does not
read it.

A token type the endpoint does not accept, and a token bound to other endpoints or an unbound one on an endpoint that
accepts only bound tokens, is `invalid_token`. The response never says whether a token was unknown, revoked or expired
(`invalid_token`); a disabled owner is `principal_disabled`; the distinction between revoked and expired is in the audit event
`token.rejected`.

## Token format and storage

- 32 random bytes from `crypto/rand`, base62 in 43 digits, a CRC-32 checksum in 6 digits, 57 characters
  ([format.go](../../internal/auth/token/format.go)). The checksum filters look-alikes offline; it is no security mechanism.
- The database stores the SHA-256 of the full string in `api_token.secret_sha256` (unique, 32 bytes) and a public `hint` of the
  prefix and 4 random characters. The value is returned once, by the call that creates or rotates the token.
  [test/integration/identity/tokens_test.go](../../test/integration/identity/tokens_test.go) scans every table in text form
  and finds no token value or token material, and checks that no audit event carries one.
- A token carries a scope (a ceiling on the owner's rights, evaluated by authorization), an expiry (a SAT only may be
  non-expiring, by an administrator, only while `auth.tokens.sat.allow_non_expiring`) and no group list. A token created
  with a token must not outlive or outrank it and is revoked with it (`derived_from`).
- The break-glass account and the built-in principals hold no token; a service account has no session.

## Validation, caches and revocation

1. Syntax without database access (counted as a rejected credential).
2. The replica's cache `auth_tokens` while the freshness gate is open. A cached entry is a live token with its owner's
   status; its expiry is checked on every hit.
3. For a credential that is not cached: the throttle (below), then one indexed query joined with the owner. Revoked, expired
   or owner-disabled results are never cached.

Every revocation, rotation, disable, delete, membership change and settings change records itself in the change area `auth`
in its own transaction. The poll of the change log applies it on every replica; Valkey only evicts earlier. The poll is the
guarantee: a lost, delayed or forged Valkey message cannot hide or delay a revocation, and a forged message cannot cause an
acceptance, because no result is ever read from Valkey. A replica whose last completed poll started more than
`auth.revocation_bound` (2 s, validated at start against `cluster.epoch_poll_interval`) ago closes the gate: every lookup
goes to PostgreSQL and none is cached, and a lookup PostgreSQL does not answer is a 503. The integration tests measure the
bound on two replicas with Valkey running (about 15 ms) and with Valkey stopped (under 2 s).

The payload of a change names a token by ID and by the SHA-256 of its secret; it can confirm a guess of a secret that
already is a full 256-bit value, which nobody can make, and cannot authenticate anyone.

## Last use of a token

Every successful authentication by a token, from the cache or from PostgreSQL, notes the token ID, the time and the client address
in a per-replica map ([lastused.go](../../internal/auth/token/lastused.go)); nothing on the request path reads it, so it cannot
change an answer. The map holds no token value or hash. A component flushes it every 60 s and at shutdown with one statement that
changes a token's row only when it has no last use or the new one is `auth.tokens.last_used_granularity` (5 m by default) newer, so
the write load follows the number of tokens, not of requests. The use is noted by the authentication stage once the whole authentication has
succeeded, so a bound token that is refused on the wrong endpoint, or whose owner's groups cannot be read, leaves no trace in the column
(Q-AUTH-180). The time is capped at the database's clock, so a replica with a fast clock cannot freeze the column; the shutdown waits for the
batch the loop is writing before the final flush. The client address is the one the trusted-proxy rule of the chain
resolved (a forwarded header from a peer outside `server.trusted_proxies` is not believed), with an IPv6 zone removed because the
`inet` column cannot hold it. A failed flush is dropped and counted in `trove_auth_lastused_flush_failures_total`; a replica killed
without a drain loses the uses of its last minute, so the time and address are approximate evidence of a use, which is what
[H-19](../spec/identity-and-authentication.md#h-19--tokens-are-bearer-credentials) asks of it, and not an audit record: the audit trail
holds the rejections and the content events, not every successful use. The filter `lastUsedBefore` and the token screens read
the columns.

**Personal data.** `last_used_addr` is the client address of the latest recorded use of a token and is tied to the token's owner, so it is
personal data about that principal. It is read by the owner (their own tokens; through a token whose scope does not inherit, only that
token's line, Q-AUTH-199) and by the holders of `tokens:read` (`admin`, `security-admin`,
`auditor`; [tokens_api.go](../../internal/api/v1/tokens_api.go)), in the token lists and on a token, and by nobody else; it is not part of an
audit event. It is kept as long as the token row: the next recorded use overwrites it (at most once per `auth.tokens.last_used_granularity`),
and the row is deleted by the `token-cleanup` task `auth.tokens.retention_after_end` after the token expired or was revoked
([identity.sql](../../internal/meta/postgres/queries/identity.sql), `IdentityDeleteExpiredTokens`). A service-account token without an expiry that is
never revoked therefore keeps its last address indefinitely; revoke it, or delete the service account, to remove it. Deleting the owner deletes
the row (`ON DELETE CASCADE`).

## Throttle, markers and rate limits

The failed-credential throttle counts every rejected credential per client address (`auth.rate_limit.failures_per_address`
per minute, the address after the trusted-proxy rule), cluster-wide: the counter is the limiter `auth.credential_failures` of
the cluster limiter set, registered by `wireFailureLimiter` ([wire_api.go](../../internal/app/wire_api.go)) and counted in
Valkey. It is consulted only for a credential that is not in the replica's
cache. In throttled mode a credential validated from PostgreSQL on any replica within `auth.rate_limit.recent_token_window`
carries a Valkey marker (`tokseen:v1:<hash of the secret hash>`, constant value, mandatory expiry) and is still validated;
anything else gets 429 with `Retry-After`. A marker only lets the lookup run: validation still reads PostgreSQL or the
replica's cache, so a forged or stale marker authenticates nobody. Without Valkey only the replica's own cache exempts a
credential, and the limiter falls back to per-replica shares (the burst divided by the live replicas of the role). Addresses in `auth.rate_limit.exempt_cidrs` are
never throttled.

## Principals, groups and the login seam

Names are validated by the application and by `CHECK` constraints and triggers of migration 00007 (the built-in principals
`anonymous` and `authenticated` cannot be changed or deleted, a membership, token or binding naming a principal of the wrong
kind is refused). The provisioner trusts the login flow for the identity (`iss`, `sub`), the mapped username and the
group names; it never matches by username or e-mail: a taken username is `username_taken`, a pending link is completed once
by a conditional update, two first logins on two replicas end with one user. A user disabled for any reason but dormancy is
refused; deleting a user ends sessions and deletes tokens and memberships, and a later login of the same identity is a new
principal. Group membership is resolved when a request is authenticated, not when a token was issued.

## The management API over tokens

The operations are in [tokens_api.go](../../internal/api/v1/tokens_api.go) and [users_api.go](../../internal/api/v1/users_api.go);
the declarations are in `api/openapi.yaml` (Q-AUTH-43).

- **A token value leaves the server once.** `POST /api/v1/tokens`, `POST /api/v1/service-accounts/{name}/tokens`, `POST /api/v1/projects/{projectKey}/service-accounts/{name}/tokens` and the two rotations
  answer `201` with `Cache-Control: no-store`. The outcome of such a create carries `Outcome.SecretID`, so the idempotency row stores the
  status and the token ID but no body, and a retry with the same key answers `409 token_already_issued`. No read operation returns the value
  or its hash. [api_flows_test.go](../../test/integration/identity/api_flows_test.go) scans every table and the audit trail for the value
  of a token created through the API.
- **No impersonation.** A PAT is created for the caller (an `owner` naming anyone else is `403 forbidden`, administrators included), and
  another user's PAT is never rotated, only revoked. A service-account token is created by an administrator, or by a token of that account
  under the derivation rule; the answer for anyone else is the same `403` whether the account exists or not.
- **A session that mints a credential proves a recent sign-in.** Creating a PAT or SAT, rotating through a session and changing the
  authentication settings declare `x-trove-recent-auth: true`; the shared authorization step answers `401 reauthentication_required` when
  `authenticated_at` is older than `auth.sessions.recent_auth_window`, after the permission was decided (R-AUTH-011, R-API-007,
  Q-API-40). A token caller is not asked: its limits are the derivation rule's.
- **A narrowed token does not act on its owner's account.** A scope is a ceiling on the owner's rights (R-AUTH-024) and names no right
  over the owner's profile. The operations on the caller's own account declare `x-trove-owner-rights: required` (`setAvatar`,
  `removeAvatar`, `updatePreferences`, `listOwnSessions`, `endOwnSession`, `endOwnSessions`, `revokeOwnTokens`); the shared authorization
  step answers `403 forbidden` for a token whose scope has `inherit: false` (also with `admin: true`) before the handler runs, and a session
  and a token with `inherit: true` pass (Q-API-214, [authorize.go](../../internal/api/v1/authorize.go)). A test walks the document, so an
  unsafe `authenticated: true` operation that declares neither this nor a reason in the exempt list fails it
  ([owner_rights_test.go](../../internal/api/v1/owner_rights_test.go)), and so does an authenticated read that is neither flagged nor
  classified. The reads of the own profile (`getCurrentUser`, `getPreferences`, `getWhoami`, `getAuthzMe`) are open to every token on
  purpose. The rule takes away no capability a scope names: a token with `admin: true` of an administrator, or a scope that names
  `identities:read`, `identities:write` or `tokens:revoke` with an owner whose roles grant it, still lists and ends the owner's sessions,
  removes the owner's avatar and revokes all the owner's tokens through `/api/v1/users/{its own username}/…`, as it does for every user:
  by the capability, not by an escalation.
- **A narrowed token sees and acts on only its own line of tokens.** The token list, `GET /api/v1/tokens/{id}`,
  `DELETE /api/v1/tokens/{id}` and `POST /api/v1/tokens/{id}/rotate` admit the owner of a token. When the caller presents a token whose
  scope does not inherit the owner's rights, the owner path reaches only that token and its line, the tokens whose `derived_from`
  chain leads to it or to one of its rotation predecessors: what it, and the token it replaced, created (`inLine` in `visible` of
  [tokens_api.go](../../internal/api/v1/tokens_api.go), the query `IdentityTokenDescendsFrom`, and the `line_root` filter of the list).
  A token of the line rotated through an inheriting token leaves it (the successor is derived from the rotating token); rotated
  through a session it stays (there is no presenting token, so the successor keeps the predecessor's `derived_from`). When an
  inheriting token rotates a token of the line and the successor goes to another holder, the predecessor's children are reachable
  by both, through the line and through the successor's predecessor: they keep their scope, so this widens who holds them, not what
  they can do. A chain the retention task cut ends there. Any other token of the owner, a sibling, the parent or an
  inheriting token, is not listed and answers `404` on the read, the revocation and the rotation, like a token that does not exist, for
  a PAT and for a SAT alike (R-AUTH-028 to R-AUTH-030, Q-AUTH-199): the description, scope and last-use address of the owner's other
  credentials stay hidden. A narrowed token whose scope holds `tokens:read` sees, and one whose scope holds `tokens:revoke` revokes, as
  any holder of the capability does, so the answers differ by capability: with `tokens:revoke` and without `tokens:read`, another token
  of the owner answers `404` on `GET` and `204` on `DELETE`; with `tokens:read` and without `tokens:revoke`, `200` on `GET`, `404` on
  `DELETE` and `403 forbidden` on a rotation outside the line; with neither, `404` on all three. A session and an inheriting token reach every token of the owner. So a leaked token made
  for one repository without these capabilities can end itself and what it or its rotation predecessors minted, never read or end
  the owner's other credentials.
  One signal remains in the time, not in the answer: the ID of a token of the owner outside the line costs one lineage query
  (`DescendsFrom`) before its `404`, an unknown ID only the row lookup; both answer the same `404`.
- **A narrowed token does not stop its owner's jobs.** A job records the principal that started it, not the credential.
  `POST /api/v1/system/jobs/{id}/cancel` admits the starter through a session or an inheriting token; a token whose scope does not
  inherit cancels only as a holder of `settings:write`, which the decision point decides with its scope (`mayCancel` in
  [jobs_api.go](../../internal/api/v1/jobs_api.go), R-API-015, Q-API-215). Reading the job stays open to it, so a pipeline polls the
  run it started.
- **Two administrators do not overwrite each other.** A user, a group, a service account and the authentication settings carry a
  revision, the `ETag`; their `PATCH` requires `If-Match` (`428` without it, `412` with the current tag for a stale one), compared
  under the row lock of the principal or in the conditional update of the settings row (Q-AUTH-50). A restore raises the revision
  columns, so a tag taken before it never matches different content (R-BAK-022).
- **A token shows where it is bound.** A token bound to endpoints that the configuration no longer holds lists the names under
  `unknownEndpoints`; it is valid on none of them (R-EP-011, Q-EP-24).
- **Hidden existence.** A token the caller may neither see nor revoke answers `404`, so token IDs cannot be enumerated; a list shows the
  caller's own tokens (through a token whose scope does not inherit, only its line) unless it holds `tokens:read`.
- **Revocation by value is open.** `POST /api/v1/tokens/revoke` takes no credential (Q-AUTH-6). It answers `202` for a live token, a dead one and
  an unknown well-formed value alike; only an administrator learns the owner (`200`). Each call counts in the limiter `auth.flow_starts`
  of the client address (an IPv6 address as its `/64`, Q-AUTH-90), cluster-wide.
- **A token never escapes the token it came from.** A derived token carries `derived_from`, and a rotation keeps it in the successor
  (or derives the successor from the presenting token), capped at the expiry of every token on the chain, so revoking a parent ends every
  successor of its children ([rotate.go](../../internal/auth/token/rotate.go), Q-AUTH-89). A derived token may rotate itself only if its
  parent may. A rotation counts against the owner's active-token limit, applies `pat.allowed_groups` to a PAT, and gives a token at most one
  successor (`TestR_AUTH_030_RotationLimits`, `TestR_AUTH_030_ConcurrentRotationsLeaveOneSuccessor`).
- **Lineage changes are serialised.** A create or rotation locks the tokens it hangs on with their `derived_from` chains and refuses when one
  of them is no longer live (401 `invalid_token` for the presenting token, also while a replica's cache still accepts it); a revocation locks
  the token's tree in one statement and runs the cascade in the next, which sees every child that committed while it waited
  ([service.go](../../internal/auth/token/service.go) `lineageCheck`, [revoke.go](../../internal/auth/token/revoke.go) `revokeTree`;
  `TestR_AUTH_029_ARevocationAndADerivationAtTheSameTime`, `TestR_AUTH_027_ARevokedTokenStillCachedMintsNothing`). Revoking all tokens of a
  principal locks the principal row a create locks.

## Tokens in the browser: the client setup page

The page [client-setup.ts](../../web/src/app/features/setup/client-setup.ts) (R-UI-016, Q-UI-74) turns the placeholder `{{token}}` of a
setup answer into a token, which is the one place where the UI handles a token value it did not just receive from the creating call.

- **The server never fills a token.** The setup answer carries `{{token}}`, `${<ui.client_setup.token_env_var>}` or no authentication line, by the request's
  `credentials` (R-API-030). The templates have no field a token could come from (`formats.SetupData`), a `package`, `version` or `scope` in the
  shape of a token is refused with `400` instead of being echoed, and every snippet is checked for `trv_(pat|sat|cit)_` before it is sent
  ([packages_api.go](../../internal/api/v1/packages_api.go); `TestR_API_030_ClientSetupNeverHoldsAToken`, 144 answers over every client, mode and
  caller). The addresses are built on the configured base URL, never on the `Host` of the request: without a base URL the operation answers `503`. The browser replaces the placeholder in the component (`fillToken`, a literal replacement) and nowhere else.
- **One owner, no copy.** A created or pasted value is a private signal of the page component. It is in no service, no URL and no
  `localStorage`, `sessionStorage` or IndexedDB (`client-setup.spec.ts` scans both storage areas); choosing another repository or another
  way of signing in, and leaving the route, set it to `null`.
- **A pasted value is never sent.** It is checked in the browser for prefix, length, alphabet and the CRC-32 of the token format
  ([token-shape.ts](../../web/src/app/features/setup/token-shape.ts), the test vector of R-AUTH-022); a value that fails the check does not
  reach the snippets and no request is made for it. The check is no security measure: it catches a mistyped paste.
- **A created token is scoped and short-lived.** The page asks for a personal token with `read` on the chosen repository, optionally
  `read` and `deploy` on the setup answer's `publishTarget`, and the lifetime `ui.client_setup.token_ttl` (the answer's `tokenTtlSeconds`),
  through the ordinary token operation: the recent-authentication rule above applies, and the value is shown once.
  For a virtual repository the scope names only that repository, by its key; it reads the members' content through it by the rule of
  [permissions.md](permissions.md#a-token-scope-on-a-virtual-repository) (Q-AUTHZ-163, Q-AUTHZ-164), bounded by the owner's rights. The members are not
  written into the scope, so a reader without `repositories:read-config` can create a working token and no copied list goes stale. What that
  reach means for later membership changes, for cache fills and for the member keys a token learns is [H-326](permissions.md#h-326--a-token-scoped-to-a-virtual-repository-by-its-key-reaches-the-members-the-repository-has-when-it-is-used).
- **A file with a token leaves through a Blob URL** that is revoked right after the click.
- **The install command panel (R-UI-009) asks for `credentials=none`**, so a command copied from a package page cannot contain a token.
- **A guide never holds a token** (R-UI-049, R-API-053, Q-API-176). Its snippets are rendered by the setup's code with `credentials=env`, or
  `none` for an anonymous reader, so they hold the variable's reference or no authentication line; the reference in a section's text is
  rewritten the same way, every answer is checked for `trv_(pat|sat|cit)_` before it is sent (`TestR_API_053_NoGuideHoldsAToken`), and
  kit check 8 refuses a format whose guide spells a credential in a code block. The guide tells readers never to commit a file that holds a
  token; that a downloaded file with a token can still be committed is [H-319](../spec/api-and-ui.md#h-319--a-configuration-file-that-holds-a-token-can-be-committed).
- **README HTML is bound, not trusted.** The README component ([package-readme.ts](../../web/src/app/features/packages/package-readme.ts),
  Q-UI-71) binds server HTML with `[innerHTML]` under Angular's sanitizer and the document's Trusted Types policy, removes a link target that
  is not a fragment, `http`, `https` or `mailto`, removes an image source that is not `https` or a path of this origin, and puts
  `rel="nofollow noopener noreferrer"` on every link that leaves trove (`package-readme.spec.ts` carries the vectors). The server-side sanitizer
  of R-UI-008 is the first layer ([render.go](../../internal/packages/readme/render.go): goldmark, bluemonday with the allowlist, a pass for links, images,
  checkboxes and heading anchors; `TestR_UI_008_XSSCorpusRendersNothingExecutable`); this page describes the browser's part.
  One exception, for the binary's own text only: the manual (R-API-054) is rendered with `RenderLinked`, which turns its relative `.md`
  links into `help/<section>/<page>`, and the help page binds it with `pageLinks="help/"`, under which the component keeps a link that is a
  plain relative path of letters, digits, `/`, `_` and `-` with an optional fragment, and opens it with the router. READMEs and guides go
  through `Render` and are bound without `pageLinks`, so their relative links are still removed (`TestR_API_054_LinkedRenderingResolvesRelativeLinks`).

## Tokens of project service accounts

An Owner of a project issues, sees and revokes the tokens of the service accounts of its project, which are addressed `sa:<projectKey>/<name>` (the principal name is stored qualified, so two projects and the instance may each hold a `partner`; [R-AUTHZ-026](../spec/authorization.md#r-authz-026--projects-for-delegated-administration),
[Q-AUTHZ-177](../spec/authorization.md#q-authz-177--what-a-project-administrator-may-do-with-service-accounts-and-their-tokens)).
The credential it uses is a session or a token with `admin: true`; the token it issues is bounded by the token service itself, so the API,
the command and a test have the same rule (Q-AUTH-190):

| Property | Rule | Where |
|---|---|---|
| lifetime | never non-expiring (`Admin` stays false), at most the SAT maximum | `resolveLifetime` |
| scope | no `system` item and `admin: false`, else 422 `invalid_scope`; repository grants, `inherit` and an `endpoints` binding are allowed | `checkDelegated` in `prepareCreate` |
| rotation by itself | never: `selfRotatable: true` is 422 at `/selfRotatable`, because each self-rotation gives the successor the original lifetime and the partner would renew the token for good (Q-AUTH-192) | `checkDelegated` |
| owner | an account of a project the caller owns; a project the caller is no member of answers 404, a member who is no Owner 403, and an account of another project or an instance-wide one the plain 403 whether it exists or not | the project route; `principal.GetProjectServiceAccount` |
| visibility | `GET /tokens`, `GET /tokens/{id}`, `DELETE /tokens/{id}` and `DELETE /projects/{projectKey}/service-accounts/{name}/tokens` reach the tokens of the project's accounts and no others; a token elsewhere answers 404; a rotation is not delegated | the token handlers; `Manager.ServiceAccountByRef` for the owner filter |
| audit | `token.created` and `token.revoked` carry `project` | `insertTx`, `recordRevoked` |

**Owner filter of the token list (verified, no leak).** `GET /tokens?owner=sa:<name>` or `sa:<projectKey>/<name>` by a caller without `tokens:read` is answered with the
caller's own tokens, never the named account's, unless the caller owns the account's project (`listOwner`). A user whose list for an
account it does not administer shows its own personal tokens is the documented behaviour (R-AUTH-028, Q-AUTH-190), not a leak: it was
reported once because a test expected an empty list. The behaviour is unchanged since before projects (`87b0e2e`: the filter is forced to
the caller when `tokens:read` is missing). Who sees which tokens: a holder of `tokens:read` (`admin`, `security-admin`) every token, filtered by
`owner`; an Owner of a project its own tokens and those of the accounts of that project; any other user its own tokens only; and a
caller through a token whose scope does not inherit only that token's line among its own tokens (Q-AUTH-199). The
pinning test is `TestR_AUTHZ_026_OwnerServiceAccountsAndTokens` (the list for another account is the caller's own, the administrator's
list is the account's alone, and another account's token answers 404 to the project Owner).

A project service account has no group, no role and only its project's policies, so what its token can reach is what the project's policies grant it
([Q-AUTHZ-175](../spec/authorization.md#q-authz-175--a-project-service-account-is-granted-by-its-projects-policies-only)).

## CI tokens

A CI token (`trv_cit_`) has the format, the hashing, the validation and the cache of every token; what differs is where it comes from and
what it carries ([R-CI-017](../spec/gitlab-ci-oidc.md#r-ci-017--ci-token-identity-presentation-and-revocation)):

| Property | Rule | Where |
|---|---|---|
| issuance | only `IssueCITx`, in the exchange's transaction: the row with `created_via` `ci-exchange`, the owner a mapping principal (kind `ci_mapping`, active, read `FOR SHARE`), the expiry the database clock plus the computed lifetime (at least 1 s, at most `auth.tokens.cit.max_ttl`), `token.created` with `ciIssuer`, `ciMappings` and the claim snapshot | [cit.go](../../internal/auth/token/cit.go) |
| lists | in no user's token list, because no user owns it, and with no per-principal limit; `GET /api/v1/tokens?type=cit` shows them to holders of `tokens:read`, the issuance records to the same holders | `IssueCITx`; [ci_issuances_api.go](../../internal/api/v1/ci_issuances_api.go) |
| the bundle | `ci_context` holds the frozen grants; the validator decodes it with `claims.DecodeFrozen` into the cache entry, and a missing, unknown-version or inconsistent bundle fails the request as a storage fault ([Q-CI-17](../spec/gitlab-ci-oidc.md#q-ci-17--how-a-ci-token-reaches-the-authorization-evaluator-at-use-time)) | `decodeCIContext` |
| presentation | `Authorization: Bearer`, HTTP Basic with any user name, `X-NuGet-ApiKey` on a push, as every token | the authentication stage |
| management API | refused with `403 ci_token_restricted` except `GET` and `DELETE /api/v1/tokens/current` and the repository-content operations; it cannot create a token (`derivation` refuses a CI parent too) | `ciGate`, `checkDerivation` |
| revocation | five causes, each in its transaction with the change of the area `auth`, so within `auth.revocation_bound` on every replica; `ci.token.revoked` with the cause for each, `token.revoked` as well for one by ID and for the token itself | [ci-federation.md](ci-federation.md#revocation) |

What a CI token may do with its grants, and how it owns package names, is [ci-federation.md](ci-federation.md#what-a-ci-token-can-do).

## Bastion tokens

A Bastion token (`trv_bst_`) has the format, the hashing and the cache of every token and belongs to a Bastion registration, a principal that
holds no grant ([R-BAS-004](../spec/bastion.md#r-bas-004--bastion-registrations-and-the-bastion-token-on-the-main-installation),
[R-BAS-005](../spec/bastion.md#r-bas-005--what-the-bastion-token-can-do)). The whole page of the Bastion is [bastion.md](bastion.md); what
differs from the tokens above:

| Property | Rule | Where |
|---|---|---|
| presentation | refused on every route but the Bastion interface, as `Authorization: Bearer`, as the Basic password and as `X-NuGet-ApiKey`: `401 invalid_token` of an unknown token, counted against the client address, before the cache and the token table are asked (`TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute` shows that no database call happens) | `Service.Check` ([validate.go](../../internal/auth/token/validate.go)) |
| `auth.token_types` | `bst` stops start-up and names the key: no list of accepted types can widen the rule | [endpoints.go](../../internal/config/endpoints.go) |
| issuance | an administrator holding `endpoints:write`, through `POST /api/v1/bastions/{name}/tokens` or `trove admin bastion token create`; always expires (`ttlSeconds` from 60 up to `auth.tokens.bst.max_ttl`, else `400 invalid_ttl`); at most `auth.tokens.max_active_per_principal.bst` unrevoked tokens per registration, counted under the lock of the registration's principal row; the value only in the answer | [bastion.go](../../internal/auth/token/bastion.go) |
| scope | none: what the token may do is fixed by its type; its scope column holds the default document and is never evaluated | `AuthenticateBastion` |
| management API | the token API neither lists nor reads nor revokes it (`404`); `/bastions/{name}/tokens` does, and `tokens:revoke` alone suffices to revoke one | [identity.sql](../../internal/meta/postgres/queries/identity.sql), [bastion_registrations_api.go](../../internal/api/v1/bastion_registrations_api.go) |
| validation on the interface | the replica's `auth_tokens` cache while the change log is fresh, then, unless the sending address is in throttled mode (asked after the cache, R-AUTH-038), `BastionTokenBySecret`; a rejected token counts against the address that sent it; a revoked or expired token logs `token.rejected` once an hour with the request's source; the last use records the Bastion's own address; a disabled or deleted registration and a revocation are in force within `auth.revocation_bound` | `AuthenticateBastion`; [H-436](../spec/bastion.md#h-436--deleting-a-registration-deletes-its-token-rows-so-a-later-use-of-a-deleted-token-is-unknown-and-not-logged), [H-437](../spec/bastion.md#h-437--a-bastion-tokens-last-use-is-one-value-per-token-at-the-granularity-of-the-last-used-buffer) |
| events | `token.created` and `token.revoked` with `resource_type` `bastion` (the token as the resource, the registration and the token ID in the data), no value and no hash; `token.revoked` is journaled (R-HA-054) | [events_identity.go](../../internal/audit/events_identity.go) |

## What this does not cover

### H-82 · Rejected credentials of one address are counted together

Component: the stage and the throttle. Subject: users behind a shared address (NAT gateway, build farm). Dormant until one
client keeps sending a revoked, expired or malformed token. Every rejection counts toward the address's budget, so a fleet of
CI jobs with a stale token can put the shared address into throttled mode; the markers exempt only credentials validated
within `recent_token_window`, so colleagues whose tokens are not cached on the replica they reach and were not validated in
that window get 429 until the count falls. What makes it real: more than `failures_per_address` rejections per minute from
one address, for example a misconfigured pipeline, together with other users on that address.

### H-83 · Anonymous requests reach the management API handlers

Component: `EndpointRules.PublicRoute` ([wire_identity.go](../../internal/app/wire_identity.go)). Subject: the management
API on the default endpoint. The stage lets a request without credential reach an operation declared public, and, while
`system.bootstrap_anonymous_read` is on, every safe request under `/api/v1/` (the bridge of the M0 authorization stand-in,
see H-63 in [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships)).
The per-operation check of `authorizingMux` then decides. Live only with the bridge on; with it off only public operations
are reachable. What makes it real: an operation declared public that should not be, or the bridge enabled on an
installation that is reachable from untrusted networks (the configuration check refuses it for an https origin other than
localhost).

### H-85 · Group names from the login flow are applied as given

Component: `principal.PGProvisioner`, subject: group memberships of an OIDC user. Live for every provider. The provisioner
takes `LoginClaims.Groups` as the trove group names that the provider's mapping rules produced and creates a missing group
with origin `oidc` when `CreateGroups` is set; it cannot tell a mapped name from a name the claim controls. A group that
carries an administrator role binding therefore gives that role to whoever the identity provider puts into the mapped
group, as [H-16](../spec/identity-and-authentication.md#h-16--idp-side-deactivation-without-scim) and the security notes of
the specification describe. What makes it real: a provider whose users can create or name groups themselves, with a rule
that maps such names onto a bound group.

### H-134 · Revocation by value and the login routes share one budget per address

Component: the limiter `auth.flow_starts` (R-AUTH-038). Subject: the sign-in of everyone behind one client address. Live in the MVP
whenever the address is shared (a NAT, a proxy that is not trusted). `POST /api/v1/tokens/revoke` is open to unauthenticated callers and
counts in the same per-address budget (an IPv6 address as its `/64`, Q-AUTH-90) as `/auth/oidc/*/start` and the local login. Someone who sends well-formed values from the
address of a company's egress fills the budget, and the sign-ins from that address get `429` until the minute ends. The attacker needs
no token and gains nothing but the denial. A trusted-proxy list with the real client address, and `starts_per_address` sized for the
largest office, are the mitigations; a separate budget for the revocation would remove the coupling.

### H-297 · A token on the setup page is readable by anything that runs in the page until the page is left

A created token is shown in the page (once, with a copy button) and a pasted one sits in a password field; both are in the document
until the user leaves the route or changes the repository. The protection is the document's policy (no inline script, Trusted Types,
`script-src 'self'`, [internal/webui/csp.go](../../internal/webui/csp.go)) and the component-local handling above. A browser extension,
a script that the policy lets through, and a shoulder surfer are outside it. Creating the token with the narrow scope and the short
lifetime of the page limits what such a reader gets.

### H-332 · A broken token in the API-key header falls back to the source credentials

Component: the API-key rule of the stage (`apiKeyToken` in [stage.go](../../internal/auth/stage.go), R-AUTH-033). Subject: NuGet pushes,
deletes and relists from a client configured with an API key and with source credentials of another principal, for example a CI job
whose `--api-key` is a deploy-only service-account token while its `nuget.config` holds a personal token for restores. Dormant until
such a client sends an API key that only looks like a token: truncated, mistyped, or with a checksum that does not match. R-AUTH-033
treats every value without a well-formed token as a placeholder like `az`, so the stage ignores it and the Basic credentials decide:
the push succeeds as the other principal, with that principal's grants; the first push of an ID claims it for the owner that principal's grant designates
([authorization.md](../spec/authorization.md) R-AUTHZ-018), and the audit event names that principal with
method `basic`. Nothing is refused and nothing is counted, so the client sees no error. The caller gains no right it did not already
hold, since both credentials are its own; what it loses is the separation it configured. What makes it real: two credentials of
different principals in one client and a damaged API key. An audit query for pushes with method `basic` from a pipeline that is meant
to push with an API key shows it; one credential per client avoids it.

### H-400 · A project Owner issues tokens up to the installation's limits

Component: `createServiceAccountToken` for a project service account. Subject: the active-token limit and the lifetime of the tokens of one
project. Live for every project that has an Owner. A token issued by a project Owner is bounded by the installation's
`auth.tokens.sat` maximum lifetime and `max_active` count for the account, as every service-account token is; there is no limit per
project, no approval and no notice to an administrator of trove beyond the `token.created` event (which carries `project`). A project
Owner can therefore keep an account's credentials alive for the full maximum by issuing a new token before the old one ends, as
long as it is an Owner. What makes it real: a project whose Owner is not trusted to renew credentials. Taking the Owner role away stops the issue; a
project service account is deleted or disabled by an Owner or by an administrator of trove.

### H-401 · The repository grants of a delegated token are not checked against the project

Component: `checkDelegated` and the scope of a token a project Owner issues. Subject: the repository part of such a scope. Live for
every such token. The scope names repositories as an instance context does (`<project>/<key>`, `<projectGlob>/<keyGlob>`, [R-AUTH-024](../spec/identity-and-authentication.md)), and the scope validation does not know the project; a grant that names
a repository outside the project is accepted and does nothing, because the service account holds rights only through its project's
policies, whose selectors never reach outside it ([Q-AUTHZ-179](../spec/authorization.md#q-authz-179--a-repository-that-leaves-a-project-leaves-its-policies-inert)).
The effect is a scope that reads wider than the rights behind it, in the token list and in the audit event. What makes it real: a reviewer
who reads the scope of such a token as the access it has; the access is the intersection with the project's policies.
