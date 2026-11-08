# OCI registry: the token service and registry tokens

Status: built (phase T-16, work package of the token service): the registry token, its keys, its verification in the authentication
stage, the scope grammar, the grant, the token service at `/v2/_token` and the enforcement point of the grant below `/v2/`. Not built
yet: the mount of `/v2/` that puts the enforcement point and the token service on a route, the registry root's own routes (the version
check, the catalog, the challenge's scope per route) and the handlers of the repository routes. Until the registry root mounts `/v2/` no
request reaches the token service and no registry token is presented; this page describes the mechanisms that will then run.

Perspective: what a container client presents to the registry, what the token service gives it in return, what a registry token can do
and for how long, which key signs it, and what a stolen one is worth. The trove tokens themselves are on
[tokens-and-credentials.md](tokens-and-credentials.md), the decision of every request on [permissions.md](permissions.md), the key ring
on [data-and-secret-flow.md](data-and-secret-flow.md). The requirements are R-OCI-004 to R-OCI-006 and R-OCI-034 in
[spec/formats/oci.md](../spec/formats/oci.md); the code is [internal/auth/registrytoken](../../internal/auth/registrytoken), the stage
[internal/auth/stage.go](../../internal/auth/stage.go), the token service [internal/api/registry/token.go](../../internal/api/registry/token.go)
and the enforcement point [internal/api/registry/enforce.go](../../internal/api/registry/enforce.go).

## What a client presents where

| Route | Credentials the stage accepts | Session cookie | Challenge of a 401 |
|---|---|---|---|
| `/v2/_token` (`GET`, `POST`) | Not evaluated by the chain. The token service takes the one credential of the request - the `Authorization` header of a `GET` (a trove token as `Basic` with any user name or as `Bearer`), or the `password` of the OAuth 2.0 form of a `POST` - and hands it to `Stage.AuthenticateTokenRequest`, which applies every rule of R-AUTH-001 and of the endpoint to it. A `POST` with a header and a form is refused (400). | ignored | `Basic realm="trove"` |
| every other path below `/v2/` | A trove token as `Basic` or `Bearer` (direct credentials, R-OCI-004), or a registry token as `Bearer`, when both the escaped and the unescaped path lie below `/v2/`. | ignored, for every method (`sessionAllowed`) | `Bearer realm="<base>/v2/_token",service="<host>"`, with `error="invalid_token"` after a rejected credential |
| every path outside `/v2/` | Unchanged; a value with the prefix `trv_rgt_` is no trove token and is refused at the syntax step without a query. | unchanged | unchanged |

The handler never reads the presented secret: it holds an `auth.Presented` value that the stage made from the header or the form, and
asks it only for the SHA-256 of the secret once the stage accepted it. `Presented` renders its kind only (`String`, `GoString`,
`LogValue`), and the secret sits behind a pointer, so a reflective print of a struct that holds one shows an address. A registry token
presented to the token service fails the syntax step of a trove token, so a registry token cannot be exchanged for a new one: the only
way to extend access past a registry token's expiry is to present the trove token again.

`auth.clients.basic.enabled: false` refuses the form's password as it refuses `Basic`, so with Basic off no registry client gets a token
(Q-OCI-57); the configuration reference and the switch's description say so.

The realm and the service of the challenge come from the request's response base URL (R-EP-003). Without `server.base_url` that base URL
follows the request's `Host` header ([clientaddr.go](../../internal/httpserver/clientaddr.go)), so a client can then make the challenge
name a realm of its choosing for itself; the documentation calls that mode development only, and nothing enforces it.

## The registry token

A registry token is `trv_rgt_` + base64url(`1:<key id>:<claims as JSON>`) + `.` + base64url(HMAC-SHA-256(key, the part before the dot)),
at most 4 KiB ([token.go](../../internal/auth/registrytoken/token.go)). The claims are:

| Claim | Content | Why |
|---|---|---|
| principal kind and ID | the owner of the trove token, none for anonymous | the stage compares it with the cache entry of the trove token |
| binding | the trove token's ID, type and the SHA-256 of its secret | the key of the token cache of R-AUTH-023, so the registry token is checked through the same cache, gate, throttle and invalidation as its trove token (Q-OCI-50, ADR-163); the stage compares the cached ID, type and owner with the claims |
| endpoint | the name of the endpoint the token request arrived on | a token is refused on another endpoint (R-EP-002) |
| issue and expiry | Unix seconds; the expiry is at most `oci.token_lifetime` after the issue and never after the trove token's expiry | the lifetime of a stolen token |
| grant | at most 16 entries of repository, image name and actions (`pull`, `push`, `delete`), and the catalog | a ceiling the enforcement point checks before the decision point |

The claims carry no secret: the hash of a 256-bit random secret allows no guess of it (R-AUTH-023), and it is what the database holds. The
token is not a reference token: it is stored nowhere, so a database dump or a backup holds none. A restore brings back the trove tokens
of the snapshot; the restore replays the revocation journal before the installation serves (R-BAK-024, R-HA-054), so a trove token revoked
after the snapshot is revoked again and its registry tokens stop with it.

A registry token is bound to an endpoint name, not to a host or an installation: it is valid on every host its endpoint serves, and on
another installation that holds the same key ring and the same token row (a staging installation restored from production's backup with
production's key ring accepts production's registry tokens while that token row is live there).

## Keys

The MAC key is derived with HKDF-SHA-256 from each key of the stored-secret key ring with the info `trove registry token v1`
(`secrets.KeyRing.Derive` in [keyring.go](../../internal/secrets/keyring.go), `NewSigner` in [token.go](../../internal/auth/registrytoken/token.go)).
The key files themselves never leave the `secrets` package, and no other purpose shares the derived key (the fingerprints use `trove
fingerprint v1`). The active key's derivation signs; every key's derivation verifies.

A rotation of the ring (R-ARCH-021) therefore needs nothing of its own: during step 1 (the new key in every ring, not active) nothing
changes; after step 2 (the new key active) new tokens name the new key and old ones verify until they expire; after step 4 (the old key
removed) a token of the old key is refused. A token signed by a replica whose ring holds a key that another replica lacks is refused on
the other one, which is why R-ARCH-021 adds a key everywhere before any replica uses it.

## Verification

`Stage.byRegistryToken` ([stage.go](../../internal/auth/stage.go)) runs in this order and stops at the first refusal:

| Step | Check | Cost | Refusal |
|---|---|---|---|
| 1 | Length at most 4 KiB, the prefix, one dot, strict base64url, version `1`, a key ID the ring holds | none | 401 `invalid_token` |
| 2 | The MAC with the named key, compared in constant time | one HMAC | 401 `invalid_token` |
| 3 | The claims parse strictly (no unknown member, no trailing data) and keep the rules of the issuer | a JSON decode of authenticated bytes | 401 `invalid_token` |
| 4 | The expiry against the replica's clock; the issue time at most one minute ahead of it; the span at most `oci.token_lifetime`; the endpoint of the request | none | 401 `invalid_token` |
| 5 | The endpoint's token types for the trove token's type (R-EP-010) | none | 401 `invalid_token` |
| 6 | The trove token through the token cache with the hash (`token.Service.CheckSecretHash`) | a query only on a cache miss or while the freshness gate bypasses the cache | revoked, expired, unknown: 401 `invalid_token`; owner disabled: 401 `principal_disabled`; throttled address: 429; PostgreSQL unreachable: 503 |
| 7 | The cache entry names the token ID, the type and the principal of the claims | none | 401 `invalid_token` |
| 8 | The endpoint bindings of the trove token's scope (R-EP-011), shared with a trove token presented itself (`Stage.accept`) | none | 401 `invalid_token` |

The reason of a refusal goes to `trove_auth_attempts_total`, not to the client. A forged, tampered, expired or foreign token is refused at
steps 1 to 4 and costs no database query (`TestRegistryTokensAgainstTheRealStores` in [registrytoken_test.go](../../test/integration/identity/registrytoken_test.go),
with a statement counter on the replica's pools); it counts as a failed credential of its address (R-AUTH-038). An anonymous registry
token stops at step 4 and passes only while the endpoint admits anonymous requests and the runtime switch is on, so switching anonymous
access off ends every anonymous registry token within the revocation bound.

The result is the trove token's principal with the trove token's scope as its ceiling (R-AUTH-024) and the grant as a further ceiling.

## The enforcement point

The grant is a ceiling the decision point does not see, so one place enforces it ([enforce.go](../../internal/api/registry/enforce.go),
Q-OCI-56), and nothing can be mounted below `/v2/` around it:

- **One way to mount.** `Enforcer.Mount(router, pattern, handler, routes...)` registers the routes a handler serves and puts the handler
  behind the check in one call. The start-up check of the route table (`httpserver.CheckRouteTable`, R-ARCH-005) takes the enforcer's
  `Patterns` as its only exemptions below `/v2/` ([app.go](../../internal/app/app.go) `startClient`), so a handler registered on the
  router directly, or before the enforcer exists, stops the start-up. Its exemptions below `/.well-known/` are a list of their own,
  so neither list opens the other prefix.
- **One match.** Which route a request reaches is the dispatcher's own match (`dispatch.Dispatcher.RegistryTarget`,
  [registry_target.go](../../internal/api/dispatch/registry_target.go)), the same function the dispatcher routes by, so the route whose
  action is checked is the route that serves the request. The vocabulary both share is the leaf `internal/api/registry/route`, and an
  architecture rule keeps the dispatcher from depending on the registry root or the authentication stage.
- **Fail closed.** For a request that carries a registry token it answers 401 with `error="insufficient_scope"`, the scope to ask for
  and the code `oci_insufficient_scope` (so logs and metrics tell it from a rejected credential) when the route has no entry or the match
  cannot read the address; when a repository route's action has no token action (such a route also cannot be mounted); when the grant
  lacks the route's action on the image name of the repository (the bound repository on a bound host, the base repository of a cache
  view); when the catalog is asked for without `registry:catalog:*`; and when a request matched to the version check is not `/v2/`
  itself.
- **What `Mount` refuses.** A pattern outside `/v2/`, a pattern mounted twice, a route name twice, a route without its example request,
  a repository route under a core route's name, a second version check, and a version check other than `GET` or `HEAD` of `/v2/`.

The version check passes any registry token on `/v2/` itself. A request with a trove token presented directly, or without a credential,
is the decision point's alone. `Enforcer.Also` checks a further coordinate, the source of a cross-repository mount, action by action, with
a cache view as its base repository. `registrytest.Walk` ([walk.go](../../internal/api/registry/registrytest/walk.go)) proves the wiring
through the endpoint's whole handler with registry tokens minted by the signer in the `Authorization` header: the registered names are
exactly the format's routes and the three core routes, every `/v2/` pattern of the router was mounted through the enforcer, the
dispatcher matches each route's example to that route, an empty grant is refused everywhere but at the version check and the token
service, and per repository route every token action but the route's is refused while exactly the route's passes. Then the decision point
decides every request again (R-AUTHZ-015).

## Revocation and lifetime

| Event | Effect on a registry token |
|---|---|
| The trove token is revoked, expires, or its owner is disabled | Refused on every replica within `auth.revocation_bound` (2 s), with or without Valkey, because step 6 is the trove token's own lookup (`TestRegistryTokensAgainstTheRealStores`, both Valkey states) |
| A grant of the principal is removed | Requests that need it are refused by the decision point (403 or 404) once the replica's grant index holds the change (R-AUTHZ-015) |
| Anonymous access is switched off | Every anonymous registry token is refused within the revocation bound |
| The token's key leaves the key ring | Refused |
| Nothing | Valid until its expiry: `oci.token_lifetime`, 300 s by default, at most 3600 s, never after the trove token's expiry |

## The grant

The token service reads at most 16 scope entries ([scope.go](../../internal/auth/registrytoken/scope.go); more are 400) and grants per entry
by the coarse check of R-OCI-006 ([grantor.go](../../internal/api/registry/grantor.go)): `pull` asks `read`, `push` asks `deploy`,
`delete` asks `delete`, each on the named repository with the image name as the package, as a question on the request's index that writes
no audit event (Q-OCI-55). A repository the caller cannot see, that does not exist, is of another format, disabled or being deleted grants
nothing, and the answer is the same 200 with a smaller grant in every case, so the token service tells a caller nothing it could not learn
from a request. An entry the grammar does not admit is skipped. On a host whose root is bound to a repository, an entry names the image
without the repository's prefix; an entry for a cache view (`<key>~cache`) is granted under the base repository.

The grant is a ceiling: `push` covers `overwrite`, and the decision point still requires `overwrite` for a moved tag (Q-OCI-6).

## Audit and metrics

A token request the stage refuses with 401 writes `auth.registry_token.denied` (reason and how the credential was presented in the
details, the endpoint in the event's column; never the credential), deduplicated per replica, client address, endpoint, reason and form of
the credential within `audit.token_rejection_dedup_window` ([events_registry_token.go](../../internal/audit/events_registry_token.go),
[dedup.go](../../internal/audit/dedup.go), Q-OCI-53). A disabled owner's token writes no `token.rejected`, so this event is its trail;
a revoked or expired trove token also writes its `token.rejected`. An issued token writes no event. `trove_oci_registry_tokens_total{outcome}`
counts `issued`, `anonymous` and `denied` ([metrics.go](../../internal/auth/registrytoken/metrics.go)).

The `Authorization` header is redacted in every log (R-AUTH-001). In any other logged text, a query string included, the scrubber's token
pattern ([redact.go](../../internal/observability/redact.go)) cuts the claims of a registry token to its prefix and at most four characters;
the pattern stops at the dot, so the 43 characters of the MAC stay in the line. The MAC is worthless without the claims it signs
(`TestTheLogScrubberHidesARegistryToken`).

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-579](../spec/formats/oci.md#h-579--a-holder-of-the-stored-secret-key-ring-can-mint-registry-tokens): a holder of the stored-secret key ring and of a live trove token's hash can mint registry tokens.

### H-595 · A registry token is a bearer credential for its whole lifetime

Stage: the verification of a registry token in the authentication stage ([stage.go](../../internal/auth/stage.go) `byRegistryToken`) and
the token service that issues it ([token.go](../../internal/api/registry/token.go)).

Subject: every registry token, of every principal and the anonymous one; the repositories and actions of its grant.

Dormant until the registry root mounts `/v2/`; live from then on. A registry token is bound to an endpoint, a principal, a trove token and
a grant, not to the client that requested it: whoever holds it can present it from any address until it expires or its trove token is
revoked. Docker and containerd keep it in memory, but a client's debug log, a proxy that records `Authorization` headers or a crash dump
can carry it.

What makes it real: a leaked registry token of a principal with `push` or `delete` on a repository. Within `oci.token_lifetime` (300 s by
default, up to 3600 s) its holder can do what the grant names and the principal may still do, without the trove token. Revoking the trove
token ends it within the revocation bound; nothing ends one registry token alone, and a lower `oci.token_lifetime` is the lever for a
shorter window.
