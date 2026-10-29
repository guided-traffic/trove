# Endpoints

Several client listeners ("endpoints") on one binary: the default endpoint on `:8080` and any number of further
ones, each with its own port, hosts, certificates, surface and chain. Requirements: R-EP-001 to R-EP-006,
R-EP-008 and R-EP-009 of [spec/endpoints.md](../spec/endpoints.md) (M0). Not built yet, and carried only as
configuration and as fields of the endpoint snapshot: exposure rules, endpoint-bound tokens, credential
restrictions, the rate-limit class, cache-fill refusal and metadata filtering (R-EP-010 to R-EP-019, M1 and later).

## What to have in your head

- **The endpoint is the listener.** `httpserver.EndpointFrom(ctx)` returns the endpoint whose `net.Listener`
  accepted the connection. It is placed in the context by the recovery stage (step 1) of that listener's
  chain and by nothing else; no request header, `Host`, server name or path selects it (R-EP-002).
  `internal/archtest` fails when a package other than `internal/httpserver` calls `httpserver.WithEndpoint` or
  writes an `httpserver.Endpoint{}` literal (R-EP-002 source rule in [source.go](../../internal/archtest/source.go)).
- **Request data only picks among the endpoint's own hosts.** A host outside the set answers `421` with the
  problem code `misdirected_request` at step 5, after the in-flight limit and before authentication and routing.
- **One chain, one router, one TLS block per endpoint.** The default endpoint is not special code: it is the
  endpoint named `default`, with the listener `server.listen`, `server.tls` and `server.max_in_flight`.
- **The surface is applied when routes are registered.** A path of a family the endpoint does not serve falls
  through to the catch-all `404`, byte-identical to an unknown path.

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
      auth: {token_types: [sat], bound_tokens_only: true, anonymous: false}   # default; carried, not enforced yet
      exposure: rules                  # default for a further endpoint: rules | all; carried, not enforced yet
      cache_fill: false                # default for a further endpoint; carried, not enforced yet
      rate_limit_class: external       # default for a further endpoint; carried, not enforced yet
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
label` without `wildcard_host`, a `pool` outside the name syntax, and the TLS block rules of `server.tls` applied
to the effective block.

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
| [internal/observability](../../internal/observability) | `WithEndpoint` (request log option), `WithEndpointNames` (metrics option), `MiddlewareFor` (span attribute), the `trove_endpoint_*` families |

## Request path of an endpoint

The chain of [architecture.md](architecture.md#request-path-client-listener) is built once per endpoint. What differs:

| Step | Per endpoint |
|---|---|
| 1 recovery | places `*httpserver.Endpoint` in the context |
| 2/3 trace, log, metrics | `Tracer.MiddlewareFor(name)` adds `trove.endpoint`; `RequestLog(…, WithEndpoint(name))` sets the `endpoint` label and log field and counts `trove_http_requests_in_flight` per endpoint |
| 4 in-flight limit | `Endpoint.MaxInFlight()`: `server.max_in_flight` for `default`, `max_in_flight` otherwise; a busy endpoint answers `503 server_busy` while another is unaffected |
| 5 address and host | the endpoint's `Resolver` (its `trusted_proxies`): client address as before, then the effective host, the host check, the response base URL, the host-root binding |
| 6 authentication | `ReplicaHeader` is the endpoint's; the endpoint's credential rules are in the snapshot for the identity milestone |

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

## Surface

`FamilyOf(pattern)` classifies a ServeMux pattern: `ui` (`GET /{$}`, `/ui/`, `/auth/`), `ci_exchange`
(`/api/v1/auth/oidc/token`, `…/-/npm/v1/oidc/…`), `api` (`/api/v1/…`), `packages` (`/api/<format>/…`, `/v2/`,
`/.well-known/`), `files` (`/files/`), anything else `other` (test fixtures; default endpoint only).
`NewEndpointRouter` drops patterns of a family outside the surface, and registers the fixed `GET /{$}` redirect
only with `ui`. `CheckSurface` is the independent proof: the composition root runs it with `CheckRouteTable` over
every endpoint's patterns, and the unit test fails on a `files` pattern registered on an endpoint without `files`.
`Document.CheckRoutes` (API routes against the OpenAPI document) runs only for endpoints that serve `api`.

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
| audit | the column `endpoint` is reserved; there is no audit writer yet |

`trove_endpoint_credentials_refused_total`, `…_cache_fill_refused_total`, `trove_exposure_hidden_total` and
`trove_exposure_rules` belong to the credential and exposure work (M1) and do not exist yet.

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

## Extension checklists

**A new route family.** Add the constant to `config.Surfaces`, the case to `FamilyOf`, the row to the table in
[spec/endpoints.md](../spec/endpoints.md) R-EP-005, and the family to `values.schema.json` (`surface` enum).

**A new per-endpoint setting.** Add the key to `config.Endpoint` (a pointer when zero is a valid value), its default to
`resolveEndpoint`, its check to `validateEndpoints`, its field to `httpserver.Endpoint`/`NewEndpoint`, the chart key to
`trove.endpoint` and `trove.endpointsConfig` (and `values.schema.json`), and regenerate the schema with `go generate
./internal/config/...`.

**A format that needs a host root.** Register its literal bound-host patterns through the exempt list of
`CheckRouteTable`, read `httpserver.RootRepository(ctx)`, and fix the repository from it before authorization.
