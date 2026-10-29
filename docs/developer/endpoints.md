# Endpoints

Several client listeners ("endpoints") on one binary: the default endpoint on `:8080` and any number of further
ones, each with its own port, hosts, certificates, surface and chain. Requirements: R-EP-001 to R-EP-006,
R-EP-008 and R-EP-009 of [spec/endpoints.md](../spec/endpoints.md) (M0), and R-EP-010 to R-EP-016 with the API side
of R-EP-017 (M1, WP1.9): the credential rules per endpoint, endpoint-bound tokens, exposure rules as the ceiling of the
decision point, the rate-limit class, and the endpoint and exposure-rule operations. Not built yet: the endpoint screen
(the UI wave), metadata filtering and the cache-fill refusal of package formats (R-EP-018, R-EP-019, with npm in M2;
`cache_fill` is carried only), and the replay of the journaled exposure changes (R-EP-020, M5; the events carry their
journal entries from now on). The security view is [security/endpoints-and-exposure.md](../security/endpoints-and-exposure.md).

## What to have in your head

- **The endpoint is the listener.** `httpserver.EndpointFrom(ctx)` returns the endpoint whose `net.Listener`
  accepted the connection. It is placed in the context by the recovery stage (step 1) of that listener's
  chain and by nothing else; no request header, `Host`, server name or path selects it (R-EP-002).
  `withEndpoint` is unexported. `internal/archtest` ([endpoint.go](../../internal/archtest/endpoint.go), typed over
  `go/packages`) fails when a package other than `internal/app` and `internal/httpserver` uses `httpserver.NewEndpoint`
  or `httpserver.Chain`, or makes an `Endpoint` value (literal, `new`, a variable of the value type), however it spells
  it: import alias, function value or a local type alias (R-EP-002; fixture
  [testdata/endpoint](../../internal/archtest/testdata/endpoint)).
- **Request data only picks among the endpoint's own hosts.** A host outside the set answers `421` with the
  problem code `misdirected_request` at step 5, after the in-flight limit and before authentication and routing.
- **One chain, one router, one TLS block per endpoint.** The default endpoint is not special code: it is the
  endpoint named `default`, with the listener `server.listen`, `server.tls` and `server.max_in_flight`.
- **The surface is applied when routes are registered.** A path of a family the endpoint does not serve falls
  through to the catch-all `404`, byte-identical to an unknown path.
- **The endpoint reaches the decision point through the request context.** Stage 7 of every endpoint's chain calls
  `authz.WithEndpoint(ctx, name)`; `Decide`, `DecideOn`, `Readable` and `Check` take the endpoint from there and
  ignore what a coordinate names, so a content route cannot forget the ceiling or pick another endpoint's
  (Q-EP-20). A decision without a request context (a job) has no ceiling.
- **Exposure is part of the grant index.** The rules are read in the index build's snapshot and compiled with it; a
  rule change notifies the change area `authz` and follows the bound, the stale wait and `Trove-Authz-Min-Epoch` of a
  policy change. No rule is read from Valkey, and a management `GET` reads the rules from PostgreSQL.

## Configuration

`server.endpoints` is one map key ([endpoints.go](../../internal/config/endpoints.go)); in the environment it is JSON
(`TROVE_SERVER_ENDPOINTS`). `Endpoint` keeps "unset" apart from a legitimate zero (`*bool`, `*string`, `nil` lists),
because a further endpoint and the default endpoint have different defaults. `Server.ResolveEndpoints()` applies
them and returns `[]ResolvedEndpoint` (default first, the rest by name); the loader's `Finalize` writes the same
values back into the entries, so `trove admin config validate` prints them.

```yaml
server:
  listen: ":8080"                      # default: the default endpoint
  endpoints:                           # default: {}
    customers:                         # example: name, ^[a-z][a-z0-9-]{0,19}$, at most 16 further endpoints
      listen: ":8443"                  # example; required; a port of its own
      base_url: https://packages.example.com   # example; required; an origin
      hosts: []                        # default: only the host of base_url; example [pkgs.example.org]
      wildcard_host: ""                # default: none; example "*.registry.example.com"
      host_roots: {}                   # default; example {pkgs.example.org: docker-hub}: reserved, no route uses it
      wildcard_root: none              # default: none | label
      trusted_proxies: []              # default: server.trusted_proxies
      pool: ""                         # default: served by the main web Deployment; example ep-customers
      max_in_flight: 1024              # default, per replica and endpoint
      tls:
        certificates: []               # default: the pair(s) of server.tls
        client_ca_file: ""             # default: no client certificates; set = require and verify (never inherited)
      surface: [packages]              # default for a further endpoint; ui is refused
      auth: {token_types: [sat], bound_tokens_only: true, anonymous: false}   # default for a further endpoint (R-EP-010)
      exposure: rules                  # default for a further endpoint: rules | all (R-EP-012)
      cache_fill: false                # default for a further endpoint; carried, not enforced yet (R-EP-019, M2)
      rate_limit_class: external       # default for a further endpoint; "" = no class (R-EP-015)
      replica_header: never            # default for a further endpoint: authenticated | always | never
    default:                           # optional; hosts, wildcard_host, host_roots, wildcard_root, surface, auth,
      hosts: [trove.example.com]       #   exposure, cache_fill, rate_limit_class, replica_header only
  endpoint_rate_limits:                # default: the class below
    external: {requests_per_second: 50, burst: 200, key: principal}   # default
```

`Server.validateEndpoints` reports every violation in one pass, each naming the key: a name outside the syntax, more
than 16 further endpoints, a key the default entry may not set (the message names the `server` key that owns it), a
missing `listen` or `base_url`, two listeners on one port (among `server.listen`, `server.ops_listen` and every
`listen`; port `0` is exempt), a `base_url` that is not an origin, a host in two endpoints, an exact host that
another endpoint's wildcard matches, one wildcard host on two endpoints, a host equal to the host of
`server.content_base_url`, an empty or unknown `surface`, `ui` on a further endpoint (`surface_not_supported`), a
`rate_limit_class` without an entry, a malformed wildcard host (`*.<domain>` with at least two labels), a
`host_roots` key that is not an exact host of the endpoint or a value that is not a repository key, `wildcard_root:
label` without `wildcard_host`, a `pool` outside the name syntax, an empty `auth.token_types` list (the
authentication stage reads an empty list as no restriction, so it is refused rather than accepted, Q-EP-23), and the
TLS block rules of `server.tls` applied to the effective block.

The effective TLS block of a further endpoint is `server.tls` with the endpoint's `certificates` (if any) and, when
`client_ca_file` is set, `client_auth: require`; `min_version` and `reload_interval` are `server.tls`'s.

## Packages and files

| File | Responsibility |
|---|---|
| [internal/config/endpoints.go](../../internal/config/endpoints.go) | `Endpoint`, `ResolvedEndpoint`, `ResolveEndpoints`, `WildcardLabel`, `validateEndpoints`, `fillEndpointDefaults` |
| [internal/httpserver/endpoint.go](../../internal/httpserver/endpoint.go) | `Endpoint` (the immutable snapshot), `NewEndpoint`, `EndpointFrom`, `RootRepository`, host set matching, response base URL, host-root lookup |
| [internal/httpserver/surface.go](../../internal/httpserver/surface.go) | `FamilyOf(pattern)`, `CheckSurface` |
| [internal/httpserver/router.go](../../internal/httpserver/router.go) | `NewEndpointRouter(e)`: registers only the routes of the surface; `Dropped()` |
| [internal/httpserver/clientaddr.go](../../internal/httpserver/clientaddr.go) | `ForEndpoint`: effective host, host check, response base URL, binding (`Resolver.resolveHost`) |
| [internal/httpserver/chain.go](../../internal/httpserver/chain.go) | `Chain.Endpoint`, `Chain.Events`; the 421 answer in `addrStage` |
| [internal/httpserver/routetable.go](../../internal/httpserver/routetable.go) | `CheckRouteTable`, now also reserving `/.well-known/` |
| [internal/app/wire_endpoints.go](../../internal/app/wire_endpoints.go) | Which endpoints this process serves, per-endpoint TLS, chain and listener, `EndpointAddr` |
| [internal/app/m1_convert.go](../../internal/app/m1_convert.go) | `endpointRules`: the configured credential rules of an endpoint for the authentication stage (R-EP-010) |
| [internal/app/wire_exposure.go](../../internal/app/wire_exposure.go) | `exposureCeiling` (the modes for the decision point), `wireEndpoints` (rule service, inert-rule warning, rate-limit classes), `endpointStage` (stage 7 per endpoint), `credentialRefused` (metric and `token.rejected`), `endpointServices`, `Authorizer` |
| [internal/auth/auth.go](../../internal/auth/auth.go), [stage.go](../../internal/auth/stage.go) | `EndpointRules` with `Refused`, `Refusal`; the stage applies type, binding and anonymous rules and reports each refusal |
| [internal/authz/ceiling.go](../../internal/authz/ceiling.go) | `Ceiling` (evaluated on the request's index), `WithEndpoint`, `EndpointOf` |
| [internal/authz/exposure.go](../../internal/authz/exposure.go) | The rule model, `ExposureFormat` and `ExposureVersioning` (the format seam of R-EP-018), compilation into the index, matching, the explanation of the dry run, the fingerprint, `ExposureCeiling` |
| [internal/authz/exposure_service.go](../../internal/authz/exposure_service.go) | `ExposureService`: validation, create in the caller's transaction, replace, delete, get, list, counts, `Check`; the `endpoint.exposure.*` events with their journal entries |
| [internal/audit/events_endpoints.go](../../internal/audit/events_endpoints.go) | The three event types; updated and deleted carry the revocation-journal flag |
| [internal/api/v1/endpoints_api.go](../../internal/api/v1/endpoints_api.go) | The nine operations of R-EP-016; `EndpointServices` |
| [internal/api/v1/endpoint_ratelimit.go](../../internal/api/v1/endpoint_ratelimit.go) | `NewEndpointRateLimit`, `EndpointLimiterName` (R-EP-015) |
| [internal/meta/postgres/queries/endpoints.sql](../../internal/meta/postgres/queries/endpoints.sql) | The build query of the index, the rule reads and writes, the counts, the serving-replica count |
| [internal/observability](../../internal/observability) | `WithEndpoint` (request log option), `WithEndpointNames` (metrics option), `MiddlewareFor` (span attribute), the `trove_endpoint_*` families, `EndpointCredentialsRefused` |

## Request path of an endpoint

The chain of [architecture.md](architecture.md#request-path-client-listener) is built once per endpoint. What differs:

| Step | Per endpoint |
|---|---|
| 1 recovery | places `*httpserver.Endpoint` in the context |
| 2/3 trace, log, metrics | `Tracer.MiddlewareFor(name)` adds `trove.endpoint`; `RequestLog(…, WithEndpoint(name))` sets the `endpoint` label and log field and counts `trove_http_requests_in_flight` per endpoint |
| 4 in-flight limit | `Endpoint.MaxInFlight()`: `server.max_in_flight` for `default`, `max_in_flight` otherwise; a busy endpoint answers `503 server_busy` while another is unaffected |
| 5 address and host | the endpoint's `Resolver` (its `trusted_proxies`): client address as before, then the effective host, the host check, the response base URL, the host-root binding |
| 6 authentication | `ReplicaHeader` is the endpoint's; the stage applies the endpoint's credential rules (`endpointRules`, below) |
| 7 rate limit | `endpointStage`: the endpoint's name into the context for the decision point, then the endpoint's rate-limit class, then the management API's stage (CORS and the classes of R-API-016) |

**Effective host** (`Resolver.resolveHost`): the request's `Host` (`:authority`), lower-cased, without port. When the
TCP peer is a trusted proxy of the endpoint, a forwarded host that lies in the endpoint's host set replaces it; one
outside the set is ignored and counted in `trove_endpoint_forwarded_host_ignored_total`. Only the header family
that `server.forwarded_header` selects is read (R-ARCH-006 holds: there is no fallback to the other), which narrows
the "`Forwarded`, else `X-Forwarded-Host`" of the spec. An endpoint that checks hosts answers `421` for a host that is
not in its set, an IP literal, or no host. A further endpoint always checks; the default endpoint checks only when
`hosts` or `wildcard_host` is configured, and keeps R-OPS-005 otherwise (including the fallback of an unlisted host
to `server.base_url`). Forwarded hosts are not read on an endpoint that does not check.

**Response base URL** (`Endpoint.baseURLFor`): the host of `base_url` gives `base_url`; a host of an
`additional_base_urls` entry (default endpoint) gives that entry; another exact host or a wildcard match gives the
scheme and port of `base_url` on that host. `RequestInfo.BaseURL` carries it; no handler reads a host itself.

**Host-root binding** (R-EP-006): `RequestInfo.RootRepository` (`httpserver.RootRepository(ctx)`) is `host_roots[host]`,
else the host label under `wildcard_root: label`. Nothing reads it yet. A format that needs host roots declares
literal bound-host patterns; the route-table rule reserves `/v2/` and `/.well-known/` on every endpoint, so until a
format registers them every path below answers the `404` problem on a bound and an unbound host alike.

## Credentials per endpoint (R-EP-010, R-EP-011)

`endpointRules(rt)` turns the resolved configuration into `auth.EndpointRules`: `AcceptsSession` only where the surface
holds `ui` (the default endpoint), `TokenTypes` from `auth.token_types`, `BoundTokensOnly` and `Anonymous`. The stage
([identity.md](identity.md#request-path)) applies them in this order, each with the `401` of an unknown token or of a
missing credential:

| Rule | Where | Lookup |
|---|---|---|
| a type outside `token_types` (a PAT on `customers`) | `token.Check` gives the type from the prefix | none |
| a token whose scope names other endpoints, or an unbound one with `bound_tokens_only` | after the validation | one, or the cache |
| no credential and `anonymous: false` (a session cookie without `ui` counts as none) | `noCredential` | none |

Each refusal calls `EndpointRules.Refused`; `credentialRefused` counts it in
`trove_endpoint_credentials_refused_total{endpoint,reason}` (`token_type`, `token_binding`, `anonymous`) and records a
refused token as `token.rejected` with reason `endpoint_not_accepted` (asynchronous, deduplicated per token ID, or per
client address for a type refusal, which has no ID). Type and binding refusals do not count towards the failed-credential
throttle. The binding itself is the token scope's `endpoints` list of the identity area: it is validated against the
configured names at creation, kept on rotation, and narrowed by the derivation rule (`token.Scope.Within`).

## Exposure (R-EP-012 to R-EP-014)

```text
rule write ─► ExposureService (one transaction): validate ─► row ─► Changes.Notify("authz") ─► audit event (after the epoch)
index build ─► Source.Snapshot reads EndpointsExposureBuild in the same REPEATABLE READ snapshot ─► buildExposure
request ─► stage 7: authz.WithEndpoint ─► Decide / Readable ─► ceilingRefuses (first) ─► the rest of R-AUTHZ-015
```

- **Mode.** `ExposureCeiling` holds the modes of the configured endpoints: `all` admits everything; `rules` admits a
  read that an enabled rule of the *addressed* repository admits (`Coordinate.Addressed`, else `Repository`, Q-EP-11) and
  refuses every other action as read-only. An endpoint name the configuration does not hold is treated as `rules` with no
  rule; a coordinate without an endpoint has no ceiling.
- **Before everything that depends on the repository.** `evaluate` runs the ceiling right after the anonymous gate and
  before the repository lookup, the `requireAuthentication` check and the virtual gate, for every action; a write is
  refused as read-only before a read is refused as hidden (Q-EP-21). So a missing repository, a hidden one and one that
  requires authentication give the same `404`, for every subject, `content:bypass` included.
- **Matching** (`compiledRule.evaluate`): package, path, version globs, range, pre-release and tag, in that order; a
  repository-level coordinate passes any enabled rule; an exclude wins inside a rule only; a version without the format's
  `ExposureVersioning` is refused (fail closed). Package patterns fold case for NuGet like the grants.
- **Formats.** `DefaultExposureFormats`: generic repositories are exposable through `paths` alone; every other format is
  not exposable (`422 format_not_exposable`) until its page states its filter and it supplies an `ExposureFormat` with
  package names, versions, tags and `ExposureVersioning` (R-EP-018, M2).
- **Listings.** `Readable` drops a repository without an enabled rule on the request's endpoint and counts it in
  `trove_exposure_hidden_total{kind="item"}`; a refused `Decide` counts `kind="request"`. `trove_exposure_rules{endpoint}`
  is set from every installed index.
- **The fingerprint** (`ExposureCeiling.ExposureFingerprint`) is computed from the index without a query, for the
  validators of filtered documents in M2; the dry run computes the same value from the stored rows.
- **Deletion of a repository.** A repository in `lifecycle = 'deleting'` drops out of the build query at once; its rows go
  with the repository's row through the foreign key. That cascade writes no `endpoint.exposure.deleted` (H-164).

## Rate-limit class (R-EP-015)

One cluster limiter per class that an endpoint of the process names, `endpoint_<class>` with hyphens as underscores
(`EndpointLimiterName`, Q-EP-22), built with `LimiterSet.NewLimiterShared`: while Valkey cannot be used, the per-replica
share divides by `startServingReplicaCount`, the live replicas of this replica's pool (web replicas with the same
`TROVE_POOL_NAME`, and replicas of the role `all`), refreshed every heartbeat. The bucket is
`<endpoint>\x00token:<id>` (or the principal, or `addr:<client>` for an anonymous request and for `key: address`), so two
endpoints of one class have separate budgets. Every answer of a limited endpoint carries `RateLimit-Policy` and
`RateLimit` with the class `endpoint:<class>`; the management API's classes add their members to the same fields. A
refusal is `429 rate_limited` with `Retry-After` as problem+json; the format's error shape on package protocols comes
with the first format that has routes (M2).

## Management API (R-EP-016, R-EP-017)

| Operation | Path | Access |
|---|---|---|
| `listEndpoints` | `GET /api/v1/endpoints` | `endpoints:read`; configured endpoints (default first), then inert names with `configuration: null` |
| `getEndpoint` | `GET /api/v1/endpoints/{endpoint}` | `endpoints:read`; 404 for a name neither configured nor holding rules |
| `listExposureRules` | `GET /api/v1/endpoints/{endpoint}/exposure-rules` | `endpoints:read`; filters `repository` (repeatable), `package`, `enabled`; sort `name`/`-name` |
| `createExposureRule` | `POST …/exposure-rules` | `endpoints:write`; optional `Idempotency-Key`; 201 with `Location`, `ETag`, `Trove-Authz-Epoch` |
| `getExposureRule` | `GET …/exposure-rules/{name}` | `endpoints:read`; `ETag` is the quoted `revision`, 304 |
| `replaceExposureRule` | `PUT …/exposure-rules/{name}` | `endpoints:write`; `If-Match` required (428, 412 with `currentEtag`) |
| `patchExposureRule` | `PATCH …/exposure-rules/{name}` | as PUT, a merge patch on the stored rule (Q-EP-18) |
| `deleteExposureRule` | `DELETE …/exposure-rules/{name}` | `endpoints:write`; `If-Match` honoured; 204 with `Trove-Authz-Epoch`; works on an inert endpoint |
| `checkExposure` | `POST …/check-exposure` | `endpoints:read` and `read` on the repository (decided in the handler, 404 like a missing repository) |

- `name` and `repository` are immutable (`422 validation_failed` with the pointer). An inert endpoint takes no create or
  replace (`409 endpoint_not_configured`); at most 1,000 rules per endpoint (`409 exposure_rule_limit_reached`, counted
  under a row lock of the endpoint's rules).
- The rule list reads the endpoint's rules (bounded by the limit) in one query and pages over `(name, id)` in the handler,
  because the `package` filter is a pattern match.
- `x-trove-recent-auth: true` is declared on the writes; nothing in this build enforces the extension yet (H-165).
- The idempotent create learns its epoch in the commit hook; the first answer carries `Trove-Authz-Epoch`, a replay of the
  stored outcome does not.
- **For the UI wave (endpoint screen, rule editor and preview):** the list is `listEndpoints` (inert names flagged,
  `ruleCount`, `configuration` for the read-only panel); the rule table is `listExposureRules` with `enabled` from the
  representation and `patchExposureRule {"enabled": …}` for a toggle; the editor loads with `getExposureRule` (keep the
  `ETag`), saves with `replaceExposureRule` and `If-Match`, creates with `createExposureRule` (send an `Idempotency-Key`),
  and deletes with `deleteExposureRule`; the preview panel posts `checkExposure` with the unsaved rule as `rule` and the
  tester's input as `paths` (generic) or `package` and `versions` (formats with versions), debounced; the banner for mode
  `all` is the warning `endpoint_not_in_rules_mode` of the rule and of the check; the client address is the endpoint's
  `configuration.baseUrl` plus the format's `ProtocolBasePath` from `GET /api/v1/formats`.

## Surface

`FamilyOf(pattern)` classifies a ServeMux pattern: `ui` (`GET /{$}`, `/ui/`, `/auth/`), `ci_exchange`
(`/api/v1/auth/oidc/token`, `…/-/npm/v1/oidc/…`), `api` (`/api/v1/…`), `packages` (`/api/<format>/…`, `/v2/`,
`/.well-known/`), `files` (`/files/`), anything else `other` (test fixtures; default endpoint only).
`NewEndpointRouter` drops patterns of a family outside the surface, and registers the fixed `GET /{$}` redirect
only with `ui`. `CheckSurface` is the independent proof: the composition root runs it with `CheckRouteTable` over
every endpoint's patterns, and the unit test fails on a `files` pattern registered on an endpoint without `files`.
`Document.CheckRoutes` (API routes against the OpenAPI document) runs only for endpoints that serve `api`, over the
registered and the dropped patterns (`checkAPIRoutes`): the ci_exchange route of a surface without it is not missing.
`CheckRouteTable` also fails a pattern of another family that can match a path of the API or of the exchange
(`/api/{format}/{repoKey}/…` matches `/api/v1/…`; checked by trying the pattern in a ServeMux against probe paths).
The router's fallback answers a path of a family outside the surface with the plain 404, never a 405 with `Allow`.
A wildcard host matches one DNS label (`config.WildcardLabel`), `hostOnly` returns "" for a byte outside printable
ASCII, and a host-checking endpoint's `base_url` host must be a DNS name (no IP literal, no trailing dot).

## Which process opens which listener

`servesEndpoint` ([wire_endpoints.go](../../internal/app/wire_endpoints.go)): role `all` opens every endpoint; a
`web` process opens the endpoints whose `pool` equals `TROVE_POOL_NAME` (empty for the main Deployment); a process
in a pool that no endpoint names serves the endpoints without a pool; a `worker` opens none. Every listener binds
before the replica reports ready (the bind happens in `startClient`, step 9); a listener that cannot bind stops
start-up and the error starts with `endpoint <name>`. Shutdown drains every client listener in parallel with the
same drain delay and deadline; `trove_endpoint_listener_up{endpoint}` is 1 from the bind and 0 at the start of the
shutdown.

## TLS

Each further endpoint gets its own `httpserver.ListenerTLS`, built by `startEndpointTLS` from the effective block
with the reloader label `listener="client:<name>"` (the default endpoint stays `client`). The certificate of a
handshake is chosen by `tlsfiles.Reloader.Certificate`: the pair whose names hold the SNI name exactly, else the
first pair a wildcard covers, else the first pair; a pair of another endpoint is never in the set. The `tls`
readiness check covers every endpoint listener. Rotation and the failed-rotation counter are those of
[tls.md](tls.md); the position of a pair in `tls.certificates` is the last path segment in the chart's mount
(`…/endpoints/<name>/<n>/tls.crt`), and `name` in the TLS series is that path.

## Observability

| Signal | Content |
|---|---|
| `trove_http_requests_total`, `…_duration_seconds`, `…_in_flight` | label `endpoint`, bounded by the configured names (`WithEndpointNames`); a name outside folds into `_other`; ops series carry the empty value |
| `trove_endpoint_listener_up`, `…_misdirected_total`, `…_forwarded_host_ignored_total` | per `endpoint`, exported at 0 from the start |
| request log | `endpoint` (null on the ops listener) and `host` (the effective host, at most 255 bytes) |
| spans | `trove.endpoint` |
| `trove_endpoint_credentials_refused_total` | per `endpoint` and `reason` (`token_type`, `token_binding`, `anonymous`), exported at 0 |
| `trove_exposure_hidden_total` | per `endpoint` and `kind` (`request`, `item`); the decision point's registry, bounded by the configured names |
| `trove_exposure_rules` | enabled rules per configured endpoint in the installed grant index; aggregate with `max` |
| `trove_ratelimit_decisions_total`, `trove_ratelimit_fallback` | the limiter `endpoint_<class>` of each class (R-ARCH-034) |
| audit | the column `endpoint` of every event recorded during a request: `authz.denied` (reason `deny.exposure`, `deny.endpoint_read_only`), `token.rejected` (reason `endpoint_not_accepted`), `endpoint.exposure.*` |

`trove_endpoint_cache_fill_refused_total` belongs to the cache-fill policy of the package formats (R-EP-019, M2) and does
not exist yet.

## Chart

See [helm-chart.md](helm-chart.md#endpoints).

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [endpoint_test.go](../../internal/httpserver/endpoint_test.go), [endpoints_test.go](../../internal/config/endpoints_test.go) | identity table (every combination of listener, `Host`, forwarded host, peer), 421, wildcard, base URL, per-endpoint in-flight bulkhead, surface and route table per endpoint, `/.well-known/`, host roots, SNI, rotation, client CA |
| app | [endpoints_test.go](../../internal/app/endpoints_test.go) | two endpoints on one replica, log and metrics endpoint, pools and roles, bind failure names the endpoint, SNI and rotation on a running app |
| integration | [test/integration/server/endpoints_test.go](../../test/integration/server/endpoints_test.go) | the real binary on the TLS stack: both listeners, 421, forwarded hosts, log and metrics, surface bodies, host roots, SNI incl. wildcard, rotation within `reload_interval` + 1 s and a broken rotation |
| chart | [test/chart/endpoints_test.go](../../test/chart/endpoints_test.go) | Services, Ingresses, NetworkPolicies, pool, mounts, config block, validation failures, notes |
| architecture | [internal/archtest](../../internal/archtest) | only `internal/httpserver` sets the endpoint |
| unit | [exposure_test.go](../../internal/authz/exposure_test.go) | rule matching (paths, exclude inside a rule, rules add up, disabled, repository level, the R-EP-012 version fixture on a test versioning), the reasons of the dry run, the fingerprint, the ceiling in `Decide` for every subject (administrator, break-glass, anonymous, missing and `requireAuthentication` repositories, virtual and addressed members, writes, an unconfigured endpoint), the context endpoint, mode `all` equal to default, `Readable`, the metrics, a rule change through the index, validation codes and journal entries, the p99 of 1,000 rules |
| unit | [endpoint_rules_test.go](../../internal/auth/endpoint_rules_test.go), [exposure_test.go](../../internal/app/exposure_test.go), [endpoints_auth_test.go](../../internal/config/endpoints_auth_test.go) | type refusal without a lookup, binding refusals after one, the refusal hook, the cookie without `ui`; the rules from the configuration; stage 7 puts the endpoint into the context; an empty `token_types` stops start-up |
| unit | [endpoint_ratelimit_test.go](../../internal/api/v1/endpoint_ratelimit_test.go) | the limiter name, 429 with the class fields, separate budgets per token and endpoint, key `address`, the fallback share |
| integration | [test/integration/endpoints](../../test/integration/endpoints) | E1.14 with three in-process replicas behind a dispatcher, Valkey running and unreachable: a PAT refused while `api_token` is locked (no query), a bound SAT on `customers` and 401 on default, R-EP-005 with a credential, a rule created, narrowed and deleted through the API on every replica within the bound and at once with `Trove-Authz-Min-Epoch`, hidden equal to missing, the bound administrator, the audit events with their journal rows, 1,000 requests of one token; the convention suite (`RunContract`) on the rules, the endpoint list with an inert name, filters, validation, the dry run with a draft, an auditor, every answer validated against the document |

The `/files/` routes belong to the files area; the integration tests register a stand-in route through `app.Options.Extra`
that decides exactly as the contract says (`v1.SubjectFrom`, `Decide` on the request's context, `authz.NotFound` for a
missing file). Once the files area is merged, the same assertions run against its route.

## Extension checklists

**A new route family.** Add the constant to `config.Surfaces`, the case to `FamilyOf`, the row to the table in
[spec/endpoints.md](../spec/endpoints.md) R-EP-005, and the family to `values.schema.json` (`surface` enum).

**A new per-endpoint setting.** Add the key to `config.Endpoint` (a pointer when zero is a valid value), its default to
`resolveEndpoint`, its check to `validateEndpoints`, its field to `httpserver.Endpoint`/`NewEndpoint`, the chart key to
`trove.endpoint` and `trove.endpointsConfig` (and `values.schema.json`), and regenerate the schema with `go generate
./internal/config/...`.

**A format that needs a host root.** Register its literal bound-host patterns through the exempt list of
`CheckRouteTable`, read `httpserver.RootRepository(ctx)`, and fix the repository from it before authorization.

**A content route (files, a format).** Build the subject with `v1.SubjectFrom(httpserver.PrincipalFrom(ctx))` and decide
with the request's context, so the endpoint's ceiling applies without the route naming it; put the addressed repository
into `Coordinate.Addressed` when deciding a member of a virtual repository, the version into `Coordinate.Version`; answer
a missing item with `authz.NotFound()`; list with `Readable` on the request's context.

**A format that can be served under exposure rules.** Give it an `authz.ExposureFormat` (package names, versions, tags,
`ExposureVersioning` for its ranges and pre-releases) through the formats function of `NewExposureCeiling` and
`ExposureServiceOptions.Ceiling` (`exposureCeiling` in [wire_exposure.go](../../internal/app/wire_exposure.go)), filter its
documents with the rules of the index (R-EP-018), and list a package's stored versions in the dry run.
