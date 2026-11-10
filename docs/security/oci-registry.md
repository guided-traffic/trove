# OCI registry: the token service and registry tokens

Status: built (phase T-16, work packages of the token service and of the registry root): the registry token, its keys, its
verification in the authentication stage, the scope grammar, the grant, the token service at `/v2/_token`, the enforcement point of the
grant below `/v2/`, and the mount of `/v2/` that puts them on a route: the two patterns on one handler, the dispatcher's handler, the version check, the
catalog, the bound host, the challenge's scope per route and the route-table checks. Not built yet: the handlers of the repository
routes (the OCI format answers 501 until their work packages land) and the catalog's page in the format's data port (the catalog answers
501 until it exists).

Perspective: what a container client presents to the registry, what the token service gives it in return, what a registry token can do
and for how long, which key signs it, and what a stolen one is worth. The trove tokens themselves are on
[tokens-and-credentials.md](tokens-and-credentials.md), the decision of every request on [permissions.md](permissions.md), the key ring
on [data-and-secret-flow.md](data-and-secret-flow.md). The requirements are R-OCI-004 to R-OCI-006 and R-OCI-034 in
[spec/formats/oci.md](../spec/formats/oci.md); the code is [internal/auth/registrytoken](../../internal/auth/registrytoken), the stage
[internal/auth/stage.go](../../internal/auth/stage.go), the token service [internal/api/registry/token.go](../../internal/api/registry/token.go)
the enforcement point [internal/api/registry/enforce.go](../../internal/api/registry/enforce.go), and the mount
[register.go](../../internal/api/registry/register.go) with the dispatcher's handler
[registry.go](../../internal/api/dispatch/registry.go).

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
  behind the check in one call; for a subtree pattern it also registers the prefix for the paths ServeMux would redirect
  (`Router.HandleRegistryUncleaned`). The start-up check of the route table (`httpserver.CheckRouter`, R-ARCH-005, Q-ARCH-390) takes the
  enforcer's `Patterns` and `UncleanedPrefixes` as its only exemptions below `/v2/` ([app.go](../../internal/app/app.go) `startClient`),
  so a handler registered on the router directly, or before the enforcer exists, stops the start-up. It also probes every other
  pattern in a scratch ServeMux (the escaped spelling `/v%32/{rest...}`, a host-only pattern), refuses a `HandleUncleaned` prefix at
  or below `/v2` (it panics at registration) or one that encloses it, and refuses a fallback handler. Its exemptions below
  `/.well-known/` are a list of their own, so neither list opens the other prefix. The literal segments of a pattern are unescaped
  before the test (`LiteralPatternPath`), the probe adds the pattern's own method, the router refuses an uncleaned prefix registered
  twice (the first match wins, so a second one could shadow the enforcer's), and an architecture rule (`CheckRegistryUncleaned`)
  allows `HandleRegistryUncleaned` in `internal/api/registry` only.
- **One match.** Which route a request reaches is the dispatcher's own match, `dispatch.Dispatcher.registryMatch`
  ([registry_target.go](../../internal/api/dispatch/registry_target.go)), and the enforcement point and the dispatcher's `/v2/`
  handler both go through it: `RegistryTarget` is that match reduced to the target the grant is checked on, and the handler serves
  the route the same function names. The request is matched once: `Enforcer`'s wrapper puts a memo into the request's context before
  it checks and the handler reuses the answer, so the bound-host lookup runs once and cannot differ between the check and the service.
  There is no second parse that could read the bound host, the cache suffix or the method
  differently. The vocabulary both share is the leaf `internal/api/registry/route`, and an architecture rule keeps the dispatcher
  from depending on the registry root or the authentication stage.
- **Fail closed.** For a request that carries a registry token it answers 401 with `error="insufficient_scope"`, the scope to ask for
  and the code `oci_insufficient_scope` (so logs and metrics tell it from a rejected credential) when the route has no entry or the match
  cannot read the address; when a repository route's action has no token action (such a route also cannot be mounted); when the grant
  lacks the route's action on the image name of the repository (the bound repository on a bound host, the base repository of a cache
  view); when the catalog is asked for without `registry:catalog:*`; and when a request matched to the version check is not `/v2/`
  itself.
- **What the refusal says (Q-OCI-220).** The text of that 401 names the scope the request needs (the same value as the challenge's
  `scope`, which carries the name the client typed) and, for a personal or service-account token whose scope is `inherit` without
  `admin`, adds that such a token acts as a non-administrator also for an owner who is an administrator and has to be created with
  administrator rights (`insufficientText` in [enforce.go](../../internal/api/registry/enforce.go)). It is built from the request's
  address and the caller's own token scope, never from a lookup of the repository, so an existing repository, one the caller may not
  read and one that does not exist are answered with the same text; it does not say which right the owner lacks, because that would
  need the decision point's answer for the repository. A trove token presented directly keeps the 403 of the decision point, which
  names the action and the target the caller sent, and the 404 `NAME_UNKNOWN` that is byte-identical for a hidden and a missing
  repository. The test is `TestTheRefusalOfATokenWithoutRightsSaysWhyAndNothingAboutTheRepository` in
  [refusal_test.go](../../test/integration/registry/refusal_test.go).
- **What `Mount` refuses.** A pattern outside `/v2/`, a pattern mounted twice, a route name twice, a route without its example request,
  a repository route under a core route's name, a second version check, and a version check other than `GET` or `HEAD` of `/v2/`.

The version check passes any registry token on `/v2/` itself. A request with a trove token presented directly, or without a credential,
is the decision point's alone. `Enforcer.Also` checks a further coordinate, the source of a cross-repository mount, action by action, with
a cache view as its base repository. `registrytest.Walk` ([walk.go](../../internal/api/registry/registrytest/walk.go)) proves the wiring through the endpoint's whole handler
with registry tokens in the `Authorization` header (the integration test mints them at the token service, so the proof includes it):
the registered names are exactly the format's routes and the three core routes, every `/v2/` pattern of the router was mounted through
the enforcer, the format's own `Match` of each route's example names that route, the route that served an example request (the
dispatcher's label of the request) is that route, an empty grant is refused everywhere but at the version check and the token service,
and per repository route every token action but the route's is refused while exactly the route's passes. With `Bound` set it runs the
same on a host bound to a repository, so the target is resolved inside the chain, where the host stage places the binding. Then the
decision point decides every request again (R-AUTHZ-015).

## The mount of /v2/

- **One handler, one match.** Everything below `/v2/` is the patterns `/v2` and `/v2/{rest...}` on one handler ([register.go](../../internal/api/registry/register.go),
  Q-OCI-100). The handler matches the request itself and serves the route the match names, so the escaped spelling of a core route
  (`/v2/%5Fcatalog`) reaches no handler its match did not name. A registry token on a path that matches no route is refused by the
  enforcement point; a direct trove token or no credential meets the handler's 404 `NAME_UNKNOWN`.
- **The same 404 for what does not exist and what the caller cannot see** (R-AUTHZ-016, R-OCI-001): a path with fewer than three
  segments, a project or repository that does not exist, one of another format and one the decision point hides answer the same body
  (`TestNameUnknownIsTheSameForWhatDoesNotExistAndWhatTheCallerCannotSee` compares the bytes). The decision point is asked
  before the repository is looked up.
- **The challenge of a caller without a credential.** Where anonymous access is on, such a caller meets the decision point's bare `Bearer`
  challenge for a private repository (R-AUTHZ-016); below `/v2/` the enforcement point replaces it by the registry's (realm, service and the
  route's scope, [challenge_writer.go](../../internal/api/registry/challenge_writer.go)), so the client learns where to ask for a token and
  the answer is the one for a repository that does not exist.
- **The version check** answers 401 with the challenge to a caller that is anonymous and carries no registry token; the stage lets such
  a caller through where anonymous access is on, so the handler builds the challenge itself ([ping.go](../../internal/api/registry/ping.go)).
  An invalid credential never arrives there.
- **A bound host** (R-OCI-029): the binding counts only for a repository of the format, read per request by
  `Dispatcher.BoundRepository`, which the match, the token service and the catalog all call (Q-EP-80). A binding to a repository of
  another format or a missing one behaves as no binding, for the token service's scope grammar too, so a token minted on such a host
  names full names, and the match reads full names. A failed lookup answers the request with the error (503 for `Unavailable`); a registry token gets the same 503 from the enforcement point, not a missing scope. The challenge's scope for a bound host tells an unauthenticated caller whether the host's bound repository exists and is of the format (the scope names the image only then): a minor existence signal of the host's own configuration.
- **The catalog** ([catalog.go](../../internal/api/registry/catalog.go), Q-OCI-101) lists names after the decision point has said they
  are readable, one name at a time; a hidden repository and a hidden name appear nowhere, not in the order, not in the `Link`.
- **The token service's caller** is recorded for the request log (`recordPrincipal` in [wire_registry.go](../../internal/app/wire_registry.go)),
  and the minimum epoch header is read after the stage accepted the credential (`withMinEpoch`).

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

## Tests

| Test | What it pins |
|---|---|
| `TestImagesArePushedAndPulledThroughV2`, `TestR_OCI_004_TheLoginSequencesOfDockerAndContainerd`, `TestR_OCI_004_AnonymousPullThroughV2AndPushWithoutLoginFails`, `TestLinksOnABoundHostAreTheShortForm`, `TestR_OCI_005_RemovingTheDeployGrantStopsThePush`, `TestPathScopedGrantsAreDecidedPerImageOnV2` ([flows_test.go](../../test/integration/registry/flows_test.go)) | Push (blobs seeded), pull, tags, the raw-write 409, the login sequences of Docker and containerd, the refusal after the deploy grant is removed, the per-image decision of the handlers with a package-scoped token |
| `TestTheWalkThroughTheRealHandler` ([registry_test.go](../../test/integration/registry/registry_test.go)) | The walk through the real handler of the default endpoint and of a bound-host endpoint, with registry tokens from the token service; the route table holds the one pattern |
| `TestTheRouteServedIsTheRouteMatched` ([registry_test.go](../../internal/api/dispatch/registry_test.go)) | For every request of a list, the route the handler labels is the route `RegistryTarget` names, and a request the match refuses reaches none |
| `TestRouteTableProbesWhatThePatternServes`, `TestRouteTableChecksTheWaysAroundThePatterns` ([routecheck_test.go](../../internal/httpserver/routecheck_test.go)) | The probe of the patterns, the uncleaned prefixes, the fallback |
| `TestABoundHostIsTheDistributionAPIOfItsRepository` (both packages) | The bound host fixes the repository; a binding to another format or a missing repository is no binding; the token service and the challenge name the image without a prefix |
| `TestR_OCI_017_TheCatalogListsWhatTheCallerMayRead`, `TestTheCatalogPagesOverRepositoriesInByteOrder` | The catalog on the real decision point with a package-scoped token, and the paging over repositories |

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-579](../spec/formats/oci.md#h-579--a-holder-of-the-stored-secret-key-ring-can-mint-registry-tokens): a holder of the stored-secret key ring and of a live trove token's hash can mint registry tokens.

### H-595 · A registry token is a bearer credential for its whole lifetime

Stage: the verification of a registry token in the authentication stage ([stage.go](../../internal/auth/stage.go) `byRegistryToken`) and
the token service that issues it ([token.go](../../internal/api/registry/token.go)).

Subject: every registry token, of every principal and the anonymous one; the repositories and actions of its grant.

Live since the registry root mounts `/v2/`. A registry token is bound to an endpoint, a principal, a trove token and
a grant, not to the client that requested it: whoever holds it can present it from any address until it expires or its trove token is
revoked. Docker and containerd keep it in memory, but a client's debug log, a proxy that records `Authorization` headers or a crash dump
can carry it.

What makes it real: a leaked registry token of a principal with `push` or `delete` on a repository. Within `oci.token_lifetime` (300 s by
default, up to 3600 s) its holder can do what the grant names and the principal may still do, without the trove token. Revoking the trove
token ends it within the revocation bound; nothing ends one registry token alone, and a lower `oci.token_lifetime` is the lever for a
shorter window.

### H-648 · The catalog reads names it does not list for a caller whose grants hide most of them

Stage: the catalog handler ([catalog.go](../../internal/api/registry/catalog.go)) over the data port's page of names.

Subject: any caller that may read at least one repository of the format, including a registry token with `registry:catalog:*` and an
anonymous caller where anonymous access is on and a grant covers a repository.

Live. A page is bounded by `n` (at most 1,000) in what it returns, not in what it reads: the handler fetches 500 names of a repository
at a time and decides each, so a caller whose grants (a package pattern, a narrow token scope) admit one name in a repository of a
million reads the million names, in pages of 500, for one answer. The decision per name is in memory; the reads are statements on the
index of the repository's manifest names.

What makes it real: a repository with very many image names and a caller with a narrow package grant that asks for the catalog in a
loop. The request is subject to the rate limits of its class (R-ARCH-005 step 7), the statement timeout bounds each read, and nothing
bounds the number of reads of one request.

### H-649 · A registry token with the catalog grant lists names beyond the other entries of its grant

Stage: the catalog handler and the enforcement point's check of `registry:catalog:*`.

Subject: the holder of a registry token whose grant has `registry:catalog:*` and pull entries for fewer images than the trove token may
read.

Live. The grant is a ceiling for the routes that name an image; for the catalog the ceiling is the route itself, and the names are
those the trove token's principal may read (R-OCI-017). A registry token requested for one image and the catalog therefore lists every
image the principal can read, though it cannot pull them with that token.

What makes it real: a registry token handed to a component that should see one image (a build step) and a principal who reads many.
The token cannot read the images it lists, only learn their names; a scope on the trove token itself narrows the list.

### H-650 · Token requests count in the bucket of the client address, whatever the principal's rate-limit class

Stage: the chain's rate-limit stage and the token service, which authenticates its credential itself (Q-OCI-51).

Subject: a rate-limit class keyed by principal on an endpoint that serves the registry; every client of the token service behind one
address.

Live. The chain does not authenticate `/v2/_token`, so the rate-limit stage sees every token request as anonymous and counts it in the
bucket of the client address. A class that limits by principal limits the other routes of the registry per principal and the token
requests per address: many clients behind one NAT or one proxy share the bucket, and one principal's loop of token requests uses the
bucket of everyone behind its address. The throttle of failed credentials (R-AUTH-038) still counts a wrong secret against the address.

What makes it real: a CI farm behind one egress address that pulls with many service accounts, on an endpoint whose class is tuned for
per-principal limits.

### H-750 · The OCI client pods of the e2e tier are privileged, and the dev CA is added to a node's containerd

Stage: the dev and CI clusters of the e2e sets `oci*` ([oci-clients.yaml](../../deploy/dev/oci-clients.yaml), `oci_node_setup` in [e2e.sh](../../deploy/dev/e2e.sh)). Not an installation of trove.

Live wherever those sets run. dockerd and Podman need a privileged container, so the pods `trove-e2e-oci-<i>` run privileged and as root in the namespace `trove-e2e-clients`, whose Pod Security level is `privileged` (Q-OCI-21); a container escape reaches the kind node, and with it every other pod of the cluster. The Kubernetes pull row also copies a forwarder and the dev CA with a `hosts.toml` into the control-plane node (`docker exec`, `docker cp`), so that node's containerd trusts the dev CA for `localhost:<edge port>`. The pods mount no ServiceAccount token and hold no credential of their own (the tests hand each command the token it needs), the namespace holds nothing else, and a dev cluster holds no data of value: the cluster is a throwaway of the runner or the laptop.

What makes it real: running the sets on a cluster that is not a throwaway (nothing in the repository does), or a malicious image in the pinned digests. The pins are digests in [tools/versions.env](../../tools/versions.env) that Renovate moves, so a changed pin is a reviewed pull request.
