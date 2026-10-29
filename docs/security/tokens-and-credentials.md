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
| 1 | `Authorization: Bearer <token>` or `Basic base64(<any>:<token>)` | The value must be a trove token (prefix, length 57, base62, CRC-32). A JWT, an identity-provider token or a password, the break-glass password included, is refused at the syntax step without a database query. The Basic username is recorded and ignored. `auth.clients.basic.enabled: false` refuses Basic. |
| 2 | Session cookie `__Host-trv_session` | Only where the endpoint's surface holds `ui` and the path allows it (`/api/v1/` all methods, `/files/` GET HEAD PUT DELETE, `/api/<format>/` GET HEAD); CSRF is the session validator's. A replica without a session validator ignores the cookie. |
| 3 | none | Anonymous only if the endpoint, `auth.anonymous.allowed` and the runtime switch `anonymousAccess` allow it, and never an unsafe method on `/api/v1/`; or a route that is always open or declared public. |

The NuGet API-key source and the GitLab job-token extension point are not evaluated; no credential is read from a query
parameter or a path. An exchange route (`POST` of the CI exchange and the two npm exchanges) receives the ID token in
`Authorization` and the stage does not read it.

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

## Throttle, markers and rate limits

The failed-credential throttle counts every rejected credential per client address (`auth.rate_limit.failures_per_address`
per minute, the address after the trusted-proxy rule). It is consulted only for a credential that is not in the replica's
cache. In throttled mode a credential validated from PostgreSQL on any replica within `auth.rate_limit.recent_token_window`
carries a Valkey marker (`tokseen:v1:<hash of the secret hash>`, constant value, mandatory expiry) and is still validated;
anything else gets 429 with `Retry-After`. A marker only lets the lookup run: validation still reads PostgreSQL or the
replica's cache, so a forged or stale marker authenticates nobody. Without Valkey only the replica's own cache exempts a
credential, and the limiter is expected to fall back to per-replica shares. Addresses in `auth.rate_limit.exempt_cidrs` are
never throttled.

## Principals, groups and the login seam

Names are validated by the application and by `CHECK` constraints and triggers of migration 00007 (the built-in principals
`anonymous` and `authenticated` cannot be changed or deleted, a membership, token or binding naming a principal of the wrong
kind is refused). The provisioner trusts the login flow for the identity (`iss`, `sub`), the mapped username and the
group names; it never matches by username or e-mail: a taken username is `username_taken`, a pending link is completed once
by a conditional update, two first logins on two replicas end with one user. A user disabled for any reason but dormancy is
refused; deleting a user ends sessions and deletes tokens and memberships, and a later login of the same identity is a new
principal. Group membership is resolved when a request is authenticated, not when a token was issued.

## What this does not cover

### H-81 · The failed-credential throttle counts per replica until the cluster limiter is connected

Component: [wire_identity.go](../../internal/app/wire_identity.go), `auth.LocalLimiter`. Subject: the budget of
`auth.rate_limit.failures_per_address`. Live in a build that has not connected `a.m1.failureLimiter` to the cluster-wide
limiter of R-ARCH-034: each replica counts alone, so the cluster-wide budget is the configured value times the number of
`web` replicas, and an address spread over replicas gets a fresh budget on each. What makes it real: a guesser that can
send well-formed fake tokens at a rate the database can absorb. The unit tests show that a shared counter and per-replica
shares both keep the bound of 300; the wiring is what is missing.

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

### H-84 · Last use of a token is not recorded

Component: `api_token.last_used_at` (R-AUTH-031, P1). Subject: leaked tokens. Live in M1: the column stays null, so
`GET /api/v1/tokens?lastUsedBefore=` cannot find stale tokens and the owner cannot see whether a leaked token was used.
[H-19](../spec/identity-and-authentication.md#h-19--tokens-are-bearer-credentials) names last-used data among the limits of a
bearer token; until it is written, the audit trail (`token.rejected`, and the access events of the content areas) is the only
trace of a use.

### H-85 · Group names from the login flow are applied as given

Component: `principal.PGProvisioner`, subject: group memberships of an OIDC user. Live for every provider. The provisioner
takes `LoginClaims.Groups` as the trove group names that the provider's mapping rules produced and creates a missing group
with origin `oidc` when `CreateGroups` is set; it cannot tell a mapped name from a name the claim controls. A group that
carries an administrator role binding therefore gives that role to whoever the identity provider puts into the mapped
group, as [H-16](../spec/identity-and-authentication.md#h-16--idp-side-deactivation-without-scim) and the security notes of
the specification describe. What makes it real: a provider whose users can create or name groups themselves, with a rule
that maps such names onto a bound group.
