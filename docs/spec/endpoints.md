# Endpoints

Status: specification; the M0 parts are implemented: listeners and identification by listener, the host set with `421`, the
surface per listener, TLS per endpoint, the chart rendering, and the metrics, logs and spans that carry the endpoint. The M1
parts are implemented: the credential rules per endpoint, endpoint-bound tokens, exposure rules as the ceiling of the
decision point, the rate-limit class and the management API of R-EP-016 (the screen of R-EP-017 is the UI wave's).
Metadata filtering and the cache-fill policy (R-EP-018, R-EP-019) arrive with npm, the journal replay (R-EP-020) with the
restore.

This page specifies endpoints: the named network entry points through which clients reach trove,
and what each of them may serve. It covers:

- the endpoint model and its configuration: one client listener per endpoint, an optional web pool,
  hosts (exact names and at most one wildcard), a base URL for URL generation, and TLS certificates
  chosen by SNI, wildcard certificates included;
- how the endpoint of a request is identified (by the listener, never by `Host`), the host
  allowlist with `421 Misdirected Request`, and forwarded hosts;
- the route families an endpoint serves (its surface), the credentials it accepts, and tokens bound
  to endpoints;
- host-root binding of a host to one repository, the mechanism for clients that cannot use a path
  (Docker Engine mirrors, Terraform discovery), reserved in the MVP and used by later formats, and
  the rule that path-based routing stays the default;
- exposure rules: which repositories, packages and versions an endpoint makes retrievable, stored
  in PostgreSQL and applied as a hard ceiling in the one authorization decision point, with their
  API and screen;
- package metadata on an endpoint: filtered documents, URLs on the endpoint's base URL, cache keys,
  and cache fill from upstreams;
- rate limits, observability, chart rendering, and what a restore does to exposure changes.

The default endpoint is the client listener on port 8080 that every other page describes (INV-6).
An installation that configures no further endpoint behaves exactly as those pages specify; every
rule on this page that changes behaviour names the setting that switches it on.

| Topic | Owner |
|---|---|
| Listener mechanics, the middleware chain, client address, TLS reloader, format contract | [architecture.md](architecture.md) R-ARCH-005, R-ARCH-006, R-ARCH-008, R-ARCH-035 |
| Base URL rules of the default endpoint, metric and log conventions, audit storage, configuration loading | [operations-and-observability.md](operations-and-observability.md) R-OPS-005, R-OPS-007, R-OPS-010, R-OPS-015, R-OPS-028 |
| Credential stage, token scope structure, token creation | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-001, R-AUTH-024, R-AUTH-027 |
| Decision point, grants, scope ceiling, denial responses, the pattern syntax | [authorization.md](authorization.md) R-AUTHZ-005, R-AUTHZ-013, R-AUTHZ-015, R-AUTHZ-016; [storage.md](storage.md) Q-STOR-9 |
| Virtual resolution, cache fill, URL rewriting, merged-metadata cache | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-019, R-REPO-020, R-REPO-025, R-REPO-043 |
| Filtering of npm and NuGet documents | [formats/npm.md](formats/npm.md) R-NPM-048, [formats/nuget.md](formats/nuget.md) R-NUGET-034 |
| Chart resources, TLS wiring, NetworkPolicies, pools, the local profile | [ha-and-helm.md](ha-and-helm.md) R-HA-028, R-HA-037, R-HA-042, R-HA-051, R-HA-062, R-HA-063 |
| Revocation journal | [ha-and-helm.md](ha-and-helm.md) R-HA-054, [authorization.md](authorization.md) R-AUTHZ-031 |
| Management API conventions, UI shell and route map | [api-and-ui.md](api-and-ui.md) |

Covers: this page draws on [ha-and-deployment.md](../features/ha-and-deployment.md) (HA-021,
HA-025, HA-027, HA-038 to HA-040), [operations-and-monitoring.md](../features/operations-and-monitoring.md)
(OPS-012, OPS-019, OPS-051, OPS-052), [artifacts.md](../features/artifacts.md) (ART-043),
[identity-and-access.md](../features/identity-and-access.md) (IAM-032),
[access-tokens.md](../features/access-tokens.md) (TOK-007),
[beyond-baseline.md](../features/beyond-baseline.md) (BEYOND-030) and
[formats/docker.md](../features/formats/docker.md) (PKG-DOCKER-007). Exposure rules per endpoint
have no catalog entry; those requirements are `trove-only`.

<details>
<summary>Facts this page relies on, and how they were verified (2026-10-29)</summary>

| Fact | Verified by |
|---|---|
| `421 Misdirected Request` tells a client that the server is not configured to answer for the request's target and that it may retry on a new connection; HTTP/2 lets a client reuse one connection for several hosts that its certificate covers. | [RFC 9110 §15.5.20](https://www.rfc-editor.org/rfc/rfc9110#section-15.5.20), [RFC 9113 §9.1.1](https://www.rfc-editor.org/rfc/rfc9113#section-9.1.1) |
| A wildcard name `*.example.com` in a certificate matches exactly one left-most label, never the bare domain and never two labels. | [RFC 6125 §6.4.3](https://www.rfc-editor.org/rfc/rfc6125#section-6.4.3) |
| A Kubernetes Ingress rule may carry a wildcard host `*.example.com` that matches one DNS label; an HTTPRoute hostname may carry a leading wildcard label. | [Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/#hostname-wildcards), [HTTPRoute hostnames](https://gateway-api.sigs.k8s.io/reference/spec/#gateway.networking.k8s.io/v1.HTTPRouteSpec) |
| Go's `tls.Config.GetCertificate` is called per handshake with the client's SNI value, so a listener can choose among several certificates without a restart. | `go doc crypto/tls.Config` with go1.27.3 |
| The spec already reserves `/v2/` for OCI with the repository key as first segment (Q-HA-4), lists subdomain routing as an OCI option (R-HA-041), and plans a second origin for content (R-ARCH-029). | [ha-and-helm.md](ha-and-helm.md), [architecture.md](architecture.md) |

Not verified on this page, and this is the gap: the client behaviour in the table of
[Host roots and wildcard hosts](#host-roots-and-wildcard-hosts) for formats other than npm and
NuGet (Docker Engine refusing a mirror URL with a path, containerd's `override_path`, Terraform's
discovery at the host root, path support of the other clients) comes from the owner request's
analysis and the clients' documentation it cites; each format page re-checks it before it relies on
it. Name resolution of `*.localhost` on developer machines is not assumed: the local profile's tests
pass `--resolve`.

</details>

## Terms

| Term | Meaning on this page |
|---|---|
| Endpoint | A named client entry point: one listener with its hosts, base URL, certificates, surface, accepted credentials, exposure mode, cache-fill policy and rate-limit class (R-EP-001). |
| Default endpoint | The endpoint `default`: the listener `server.listen` with `server.base_url`. It exists in every installation. |
| Further endpoint | Every endpoint other than `default`. |
| Effective host | The host a request is checked and answered for: its own `Host` or `:authority`, or a forwarded host within the endpoint's host set (R-EP-003). |
| Response base URL | The origin every absolute URL of one response is built from (R-EP-003). |
| Surface | The route families an endpoint serves (R-EP-005). |
| Exposure mode | `all`: no ceiling. `rules`: deny by default; only what an exposure rule admits is retrievable (R-EP-012). |
| Exposure rule | A row that admits content of one repository on one endpoint, narrowed by package, path, version and tag patterns (R-EP-012). |
| Addressed repository | The repository a client names in its request URL, or the one a host-root binding names, whatever members resolution consults afterwards. |
| Exposure fingerprint | SHA-256 over the endpoint's mode and the ID, revision and `enabled` flag of every rule of that endpoint for the addressed repository; a constant for mode `all` (R-EP-018). |
| Host-root binding | A host of an endpoint whose root belongs to one repository (R-EP-006). |
| Host label | The label a wildcard host matched (`docker-all` in `docker-all.registry.example.com`). |

```text
      internal clients, browsers, CI jobs                        customers
               │ https://trove.example.com                         │ https://packages.example.com
     ┌─────────▼──────────┐                              ┌─────────▼──────────┐
     │ Ingress <fullname> │  ingress class A             │ Ingress            │  ingress class B, own hosts,
     │                    │                              │ <fullname>-ep-     │  own TLS Secret, wildcard
     └─────────┬──────────┘                              │ customers          │  host optional
               │ Service <fullname>:8080                 └─────────┬──────────┘
               │                                                   │ Service <fullname>-ep-customers:8443
     ┌─────────▼──────────────────────┐                  ┌─────────▼──────────────────────┐
     │ web pods, listener :8080       │                  │ pool pods (optional), :8443    │
     │ endpoint default               │                  │ endpoint customers             │
     │ surface: ui api ci_exchange    │                  │ surface: packages              │
     │          packages files        │                  │ credentials: bound SAT tokens  │
     │ exposure: all, cache fill: on  │                  │ exposure: rules, cache fill:   │
     └─────────┬──────────────────────┘                  │ off, rate-limit class external │
               │                                         └─────────┬──────────────────────┘
               └────────────── one decision point (R-AUTHZ-015) ───┘
                  effective = grants ∩ token scope ∩ exposure(endpoint of the listener)
```

## Endpoint model

### R-EP-001 · Endpoints and their configuration
Priority: **MVP** · Covers: HA-021

An endpoint is named and configured in the file (INV-15, Q-EP-2). Changing an endpoint is a
configuration change and therefore a rolling restart (R-OPS-028); exposure rules, which change at
runtime, live in PostgreSQL (R-EP-012).

- **Names** match `^[a-z][a-z0-9-]{0,19}$`. The name is a metric label value, a token binding, the
  key of exposure rules and part of chart object names (R-EP-008). `default` names the default
  endpoint. At most 16 further endpoints may be configured, so the `endpoint` label stays bounded.
- **The default endpoint always exists.** Its listener is `server.listen`, its base URL
  `server.base_url` with `server.additional_base_urls` (R-OPS-005), its in-flight limit
  `server.max_in_flight` and its certificate pair `server.tls` (R-ARCH-035). The entry
  `server.endpoints.default` is optional and may set only `hosts`, `wildcard_host`, `host_roots`,
  `wildcard_root`, `surface`, `auth`, `exposure`, `cache_fill`, `rate_limit_class` and
  `replica_header`. Any other key there stops startup with an error that names the `server` key
  that owns the value.
- **A further endpoint** MUST set `listen` and `base_url`. Every other key has the default shown
  below, which differs from the default endpoint's where a further endpoint is meant for an
  audience outside the organization (Q-EP-5, Q-EP-10).
- **Startup validation.** Each of these stops startup with an error that names the keys, and every
  violation is reported in one pass (R-OPS-028):
  - two listeners on the same port, among `server.listen`, `server.ops_listen` and every endpoint's
    `listen`;
  - a `base_url` that breaks the origin rule of R-OPS-005 (a scheme, a host and an optional port,
    nothing else); a further endpoint with an `http` base URL on a host other than a loopback name
    starts with a warning;
  - a host in the host sets of two endpoints, an exact host of one endpoint that the wildcard host
    of another matches, or two endpoints with the same wildcard host (R-EP-003);
  - an endpoint host equal to the host of `server.content_base_url`
    ([architecture.md](architecture.md) R-ARCH-029);
  - a `surface` that is empty, names an unknown family, or lists `ui` on a further endpoint
    (R-EP-005);
  - a `rate_limit_class` that names no entry of `server.endpoint_rate_limits` (R-EP-015);
  - an empty `auth.token_types` list (R-EP-010,
    [Q-EP-23](#q-ep-23--an-empty-list-of-accepted-token-types));
  - an invalid wildcard host or host-root binding (R-EP-003, R-EP-006);
  - a `pool` that does not match the endpoint name syntax.

```yaml
server:
  listen: ":8080"                         # default (proposed): the default endpoint's listener (INV-6)
  endpoints: {}                           # default (proposed): only the default endpoint
  # endpoints:
  #   customers:                          # example: endpoint name
  #     listen: ":8443"                   # example; required; a port of its own
  #     base_url: https://packages.example.com   # example; required; an origin (R-OPS-005)
  #     hosts: []                         # default (proposed): only the host of base_url (R-EP-003); example [packages.example.com, pkgs.example.org]
  #     wildcard_host: ""                 # default (proposed): none; example "*.packages.example.com" (R-EP-003)
  #     host_roots: {}                    # default (proposed); example {mirror.example.com: docker-hub} (R-EP-006)
  #     wildcard_root: none               # default (proposed): none | label (R-EP-006)
  #     trusted_proxies: []               # default (proposed): [] = server.trusted_proxies; example ["10.43.0.0/16"] (R-EP-003)
  #     pool: ""                          # default (proposed): "" = served by the main web Deployment; example ep-customers (R-EP-002)
  #     max_in_flight: 1024               # default (proposed), per replica (R-EP-002)
  #     tls:
  #       certificates: []                # default (proposed): [] = the pair of server.tls (R-EP-004)
  #       # - {cert_file: /var/run/secrets/trove/endpoints/customers/0/tls.crt, key_file: /var/run/secrets/trove/endpoints/customers/0/tls.key}   # example
  #       client_ca_file: ""              # default (proposed): no client certificates (R-EP-004)
  #     surface: [packages]               # default (proposed) for a further endpoint (R-EP-005)
  #     auth:
  #       token_types: [sat]              # default (proposed) for a further endpoint (R-EP-010)
  #       bound_tokens_only: true         # default (proposed) for a further endpoint (R-EP-010, R-EP-011)
  #       anonymous: false                # default (proposed) for a further endpoint (R-EP-010)
  #     exposure: rules                   # default (proposed) for a further endpoint: rules | all (R-EP-012)
  #     cache_fill: false                 # default (proposed) for a further endpoint (R-EP-019)
  #     rate_limit_class: external        # default (proposed) for a further endpoint (R-EP-015)
  #     replica_header: never             # default (proposed) for a further endpoint: authenticated | always | never (R-EP-009)
  #   default:                            # optional; only the keys below
  #     hosts: []                         # default (proposed): no host check, R-OPS-005 unchanged
  #     wildcard_host: ""                 # default (proposed)
  #     host_roots: {}                    # default (proposed)
  #     wildcard_root: none               # default (proposed)
  #     surface: [ui, api, ci_exchange, packages, files]   # default (proposed)
  #     auth: {token_types: [pat, sat, cit], bound_tokens_only: false, anonymous: true}   # default (proposed); anonymous: true defers to R-AUTH-037
  #     exposure: all                     # default (proposed)
  #     cache_fill: true                  # default (proposed)
  #     rate_limit_class: ""              # default (proposed): no endpoint class
  #     replica_header: authenticated     # default (proposed): the value of server.replica_header (R-OPS-002)
  endpoint_rate_limits:                   # R-EP-015
    external: {requests_per_second: 50, burst: 200, key: principal}   # default (proposed)
```

The keys belong to the section `server` and are owned by this page
([operations-and-observability.md](operations-and-observability.md) R-OPS-028). Lists and maps
arrive through the environment as JSON (`TROVE_SERVER_ENDPOINTS`), like every other list.

Acceptance criteria:

- With the `customers` example set, `trove admin config validate` prints `server.endpoints` as one row whose value is the
  map as one JSON value, with its source; the defaults of a further endpoint are in that value
  ([Q-EP-16](#q-ep-16--rendering-of-the-endpoint-map-and-of-the-pool-values)).
- `listen: ":8080"` for `customers` stops startup and names `server.endpoints.customers.listen` and
  `server.listen`. `server.endpoints.default.listen` stops startup and names `server.listen`.
- A host listed by `customers` and by `partners`, or `packages.example.com` listed by `customers`
  while `partners` has `wildcard_host: "*.example.com"`, stops startup and names both endpoints.
- `server.endpoints.Customers`, a 17th further endpoint, and `rate_limit_class: nope` each stop
  startup with an error that names the key.

### R-EP-002 · Endpoint identity comes from the listener
Priority: **MVP** · Covers: trove-only

- **One listener per endpoint.** Every endpoint has its own `net.Listener` and `http.Server`, its own
  ServeMux built from its surface (R-EP-005) through the one registration wrapper of R-ARCH-005,
  and its own instance of the middleware chain, constructed with the endpoint bound. Middleware
  step 1 places the endpoint in the request context (`endpoint.From(ctx)`: an immutable snapshot of
  name, hosts, surface, credential rules, exposure mode, cache-fill policy and rate-limit class).
  Nothing else sets or changes it.
- **Never from request data.** The endpoint is the one whose listener accepted the TCP connection.
  It MUST NOT be derived from `Host`, `:authority`, the TLS server name, `Forwarded`,
  `X-Forwarded-*`, a path prefix or any other part of the request (Q-EP-1). Request data only
  selects among the endpoint's own hosts (R-EP-003). A client that reaches a listener therefore
  gets that endpoint's surface, credential rules and exposure, whatever headers it sends.
- **Connection reuse.** An HTTP/2 client may send a request for another host on a connection it
  holds. The endpoint stays the connection's, and the host check answers `421` (R-EP-003), so the
  client opens a new connection to the right endpoint.
- **Which process opens which listener.** A `web` process opens the client listeners of the
  endpoints of its pool: the endpoints whose `pool` equals the process's pool name, read from the
  reserved variable `TROVE_POOL_NAME` ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-028), which is empty for the main `web` Deployment. A process in a pool that no endpoint
  names (a path pool of [ha-and-helm.md](ha-and-helm.md) R-HA-062, P1) serves the endpoints without
  a pool, like the main Deployment. A `worker` process opens no client listener. The development
  role `all` opens every listener. Every process opens the ops listener as before.
- **Lifecycle.** A `web` process binds every listener of its pool before it reports ready; a
  listener that cannot bind stops startup and names the endpoint. Shutdown drains every client
  listener with the same readiness flip, drain delay and in-flight completion
  ([architecture.md](architecture.md) R-ARCH-024).
- **A bulkhead per endpoint.** The in-flight limit of R-ARCH-005 step 4 counts per endpoint:
  `server.max_in_flight` for the default endpoint, `max_in_flight` for each further endpoint. Load
  on one endpoint cannot use up the slots of another on the same replica.
- Handlers, formats and the core read the endpoint only from the request context. Background jobs
  have no endpoint; a job that builds a URL uses the default endpoint's base URL.
- The route-table test of R-ARCH-005 runs over the ServeMux of every endpoint.

Acceptance criteria:

- Endpoints `default` (`:8080`) and `customers` (`:8443`, base URL `https://packages.example.com`).
  `curl --resolve packages.example.com:8080:127.0.0.1 https://packages.example.com:8080/api/v1/system/ping`
  is logged with `"endpoint":"default"`, never with `customers`.
  `curl --resolve trove.example.com:8443:127.0.0.1 https://trove.example.com:8443/api/npm/npm-all/`
  answers `421` from `customers`.
- A table test sends every combination of listener, `Host`, server name and forwarded host of a
  fixture to the server and asserts that the endpoint in the request context is always the
  listener's. R-ARCH-006's source check (no `Host` or forwarded header read outside
  `internal/httpserver`) still passes, and an architecture test fails when a package other than
  `internal/httpserver` constructs an endpoint snapshot.
- A process with `TROVE_POOL_NAME=ep-customers` and `customers` assigned to that pool listens on
  `:8443` and `:8081` only; `ss -ltn` in its pod shows no `:8080`. A main `web` pod shows `:8080`
  and `:8081` only.
- With `max_in_flight: 1` on `customers` and one slow request holding it, a second request on
  `customers` answers `503` `server_busy`, while a request on `:8080` answers `200`.

### R-EP-003 · Hosts of an endpoint: allowlist, 421 and the response base URL
Priority: **MVP** · Covers: OPS-051, OPS-052, HA-025

- **Host set.** An endpoint's host set is the host of its `base_url`, every entry of `hosts` (DNS
  names, compared lower-cased, without a port) and its `wildcard_host`. The default endpoint's set
  also holds the hosts of `server.additional_base_urls`.
- **Wildcard host (Q-EP-6).** At most one per endpoint, in the form `*.<domain>` where `<domain>`
  has at least two labels. It matches a host with exactly one more left-most label, as a wildcard
  certificate does: `*.registry.example.com` matches `docker-all.registry.example.com`, not
  `registry.example.com` and not `a.b.registry.example.com`. The matched label is the request's
  host label, which R-EP-006 may bind to a repository.
- **Effective host.** The request's `:authority` or `Host`, lower-cased and without its port. When
  the TCP peer lies in the endpoint's trusted proxies (`trusted_proxies`, else
  `server.trusted_proxies`) and the request carries a forwarded host in the one header family that
  `server.forwarded_header` selects (`host=` of `Forwarded`, [RFC 7239](https://www.rfc-editor.org/rfc/rfc7239), or
  `X-Forwarded-Host`; [Q-EP-15](#q-ep-15--which-forwarded-host-counts-on-an-endpoint)), the forwarded
  host is the effective host if it lies in the endpoint's host set. The other family is never read,
  not even as a fallback. A forwarded host outside the set is ignored for the check and for URL
  generation, and counted in `trove_endpoint_forwarded_host_ignored_total{endpoint}`.
- **Host check.** A further endpoint always checks; the default endpoint checks only when its
  `hosts` or `wildcard_host` is configured. A checking endpoint answers a request whose effective
  host is not in its host set, an empty host or an IP literal with `421 Misdirected Request`
  ([RFC 9110 §15.5.20](https://www.rfc-editor.org/rfc/rfc9110#section-15.5.20)) and the problem
  code `misdirected_request`. The check runs at middleware step 5 of R-ARCH-005, after the
  in-flight limit and before authentication, rate limits and routing. Its answer does not depend
  on the path, so it reveals nothing about content.
- **Default endpoint without configured hosts.** No check: R-OPS-005 applies unchanged, including
  its fallback of an unlisted host to `server.base_url`.
- **Response base URL.** For a request on endpoint E with effective host h in E's set:
  - h is the host of E's `base_url`: that base URL;
  - h is the host of a `server.additional_base_urls` entry (default endpoint): that entry;
  - h is another exact host of E, or matches E's wildcard host: `<scheme of E's base_url>://h`,
    with the port of E's `base_url` when it has one.

  For the default endpoint without configured hosts the rule of R-OPS-005 applies. Middleware
  step 5 places the response base URL in the request context once, and `LinkBuilder`
  ([architecture.md](architecture.md) R-ARCH-009) builds every absolute URL of the response from
  it. No handler reads a host itself (R-ARCH-006).
- **URLs that leave the request** stay on the default endpoint: the OIDC redirect and post-logout
  URIs (R-AUTH-003), links in mail, and the webhook `source` (R-OPS-036) are built from
  `server.base_url`. No response on one endpoint contains an absolute URL on another endpoint's
  host.

Acceptance criteria:

- `customers` with base URL `https://packages.example.com` and `hosts: [pkgs.example.org]`:
  - `GET https://pkgs.example.org:8443/api/npm/npm-customers/@acme%2fsdk` answers a document whose
    tarball URLs start with `https://pkgs.example.org/api/npm/npm-customers/`;
  - `Host: trove.example.com` answers `421` with code `misdirected_request`, and so do
    `Host: 10.0.0.5` and an HTTP/1.0 request without `Host`;
  - `Host: evil.example` with `X-Forwarded-Host: packages.example.com` from a peer outside the
    trusted proxies answers `421`; the same from a trusted proxy answers `200` with URLs on
    `packages.example.com`;
  - `Host: packages.example.com` with `X-Forwarded-Host: evil.example` from a trusted proxy answers
    `200` with URLs on `packages.example.com`, and the ignored-host counter increments.
- With `wildcard_host: "*.registry.example.com"`, `docker-all.registry.example.com` passes the
  check with host label `docker-all`; `registry.example.com` (not in the set) and
  `a.b.registry.example.com` answer `421`.
- Without `server.endpoints.default`, R-OPS-005's acceptance criteria pass unchanged. With
  `server.endpoints.default.hosts: [trove.example.com]`, a request on `:8080` with
  `Host: packages.example.com` answers `421`.

### R-EP-004 · TLS on endpoint listeners: certificates by SNI, wildcard certificates, reload
Priority: **MVP** · Covers: trove-only

Every endpoint listener uses the TLS machinery of [architecture.md](architecture.md) R-ARCH-035
(INV-20); this requirement adds the certificate set.

- **When TLS is served.** An endpoint listener serves TLS when it has a certificate: the pairs of
  its `tls.certificates`, or, when that list is empty, the pair of `server.tls`. Without either it
  serves plain HTTP, which is the installation choice H-38 records.
- **Selection by SNI.** `tls.Config.GetCertificate` chooses among the endpoint's own pairs only:
  the pair whose DNS names cover the TLS server name, an exact name before a wildcard name, and
  the first pair of the list when the client sends no server name or no pair covers it. A pair of
  another endpoint is never served on this listener. A wildcard name in a certificate covers one
  label, by the rule of R-EP-003.
- **The server name selects a certificate, never the endpoint**, and it need not equal the
  effective host: an edge that re-encrypts to trove sends the Service name as server name
  ([ha-and-helm.md](ha-and-helm.md) R-HA-063), while `Host` carries the public name.
- **Reload.** Every pair is checked by the reloader of R-ARCH-035 every `server.tls.reload_interval`
  and swapped atomically for new handshakes; a pair that does not load keeps the previous one in
  service. The families `trove_tls_reload_errors_total` and `trove_tls_certificate_expiry_timestamp_seconds`
  carry `kind="listener"`, `listener="client:customers"` and `name`, the configured file of the pair (its path
  names the position in `tls.certificates`; [Q-EP-14](#q-ep-14--labels-of-the-tls-series-and-of-the-ops-series)).
  A broken pair at start holds the first readiness and does not stop the process; the listener serves once
  the pair loads (R-ARCH-035).
- **Client certificates.** `tls.client_ca_file` of a further endpoint, when set, requires and
  verifies a client certificate on that listener ([architecture.md](architecture.md) Q-ARCH-32).
  A further endpoint does not inherit `server.tls.client_ca_file`, because its clients are usually
  not the proxy that the default endpoint pins.
- TLS 1.2 is the minimum, TLS 1.3 is preferred, and HTTP/2 is negotiated with ALPN, as on the
  default endpoint.

Acceptance criteria:

- `customers` with the pairs `packages.example.com` (position 0) and `*.packages.example.com`
  (position 1): `openssl s_client -connect :8443 -servername packages.example.com` receives pair 0,
  `-servername a.packages.example.com` receives pair 1, and `-servername other.example` and a
  handshake without a server name receive pair 0. The same names on `:8080` receive the pair of
  `server.tls`.
- Rotating pair 1 under E0.13's k6 load: k6 records only `200`, the new certificate is served within
  `server.tls.reload_interval` + 1 s, and the process start time does not change.
- A rotated pair 1 whose key does not match keeps the old pair in service and increments
  `trove_tls_reload_errors_total{kind="listener",listener="client:customers",name="/var/run/secrets/trove/endpoints/customers/1/tls.crt"}`
  (the name is the certificate path; example).
- With `tls.client_ca_file` set on `customers` only, a handshake on `:8443` without a client
  certificate fails before any HTTP request is read, and `:8080` is unaffected.

### R-EP-005 · Surface per endpoint
Priority: **MVP** · Covers: ART-043

An endpoint serves only the route families of its `surface`:

| Family | Routes ([architecture.md](architecture.md) R-ARCH-005) | Default endpoint | Further endpoint |
|---|---|---|---|
| `ui` | `/ui/…`, `GET /{$}`, `/auth/…` (browser sign-in, session, logout) | on | not allowed (Q-EP-13) |
| `api` | `/api/v1/…` except the CI exchange | on | off by default |
| `ci_exchange` | `POST /api/v1/auth/oidc/token`; from P1 the npm trusted-publishing exchange at the host root and below every registry URL ([formats/npm.md](formats/npm.md) R-NPM-022) | on | off by default |
| `packages` | `/api/<format>/<repoKey>/…` other than the exchange routes, the format host-root routes other than the exchange (`/v2/` once OCI exists), and host-root bindings (R-EP-006) | on | on by default |
| `files` | `/files/…` | on | off by default |

- Each endpoint's ServeMux registers only the routes of its families. A path of a family the
  endpoint does not serve falls through to the `404` problem of R-ARCH-005, byte-identical to an
  unknown path apart from `X-Request-Id`. It never answers `403` or `405`, so one endpoint does not
  reveal what another serves.
- **`ui` belongs to the default endpoint.** The OIDC redirect URIs, the post-logout URI and the
  `__Host-` cookies are bound to `server.base_url` (R-AUTH-003, Q-AUTH-2), so a sign-in started on
  another host could not complete there. A further endpoint that lists `ui` stops startup with
  `surface_not_supported`. Session cookies are not evaluated on any endpoint without `ui`
  (R-EP-010).
- **`api` on a further endpoint** serves the management API to token callers. Its content
  operations are subject to the endpoint's exposure (R-EP-014); its capability operations need the
  capability as everywhere. Startup logs a warning that names the endpoint when `api` or
  `ci_exchange` is enabled on an endpoint with `exposure: rules`.
- The npm exchange routes belong to `ci_exchange`, not to `packages`, so an endpoint can serve npm
  packages without accepting identity-provider tokens on any route.

Acceptance criteria:

- On `customers` with the default surface, `GET /ui/`, `GET /`, `GET /auth/oidc/corp/start`,
  `GET /api/v1/repositories`, `POST /api/v1/auth/oidc/token` and `GET /files/generic-local/x`
  answer the same `404` problem body as `GET /does-not-exist`; `GET /api/npm/npm-customers/`
  answers `200`.
- `surface: [ui, packages]` on `customers` stops startup and names
  `server.endpoints.customers.surface`.
- The route-table test lists, per endpoint, exactly the registered patterns of its families, and
  fails on a fixture that registers a `files` pattern on an endpoint without `files`.

## Host roots and wildcard hosts

This section is normative for every format page.

- **Path-based is the default.** A format serves its protocol below `/api/<format>/<repoKey>/` on
  every endpoint (INV-6). A format page MAY use a host root, through a host-root route
  ([architecture.md](architecture.md) R-ARCH-005) or a host-root binding (R-EP-006), only where a
  client fixes the path at the host root and cannot be configured with a path. One host, one
  certificate and one edge route then serve every repository of every format.
- **No wildcard host is required by the MVP.** npm and NuGet clients take a registry URL with a
  path. An installation needs a wildcard host only for the optional subdomain mode of OCI (P1).
- **Where host roots are unavoidable** (from the owner request's analysis; each format page
  re-checks the client behaviour before relying on it):

| Client or protocol | Registry URL with a path possible | Needs | Mechanism |
|---|---|---|---|
| npm, pnpm, Yarn; NuGet; PyPI; Maven and Gradle; Cargo (sparse index); Go module proxy; Helm (index and OCI path mode); Ansible Galaxy; Composer; Conan; Debian and RPM; RubyGems; pub; Swift; Conda; Bazel | yes | nothing | `/api/<format>/<repoKey>/` |
| OCI clients (docker, Podman, containerd, CRI-O, ORAS) with image names that carry the repository key (`trove.example.com/docker-all/alpine:3`) | the API root is fixed at `/v2/` | the host-root prefix `/v2/`, reserved since the MVP ([ha-and-helm.md](ha-and-helm.md) Q-HA-4) | host-root route |
| containerd (`hosts.toml` with a path and `override_path`) and CRI-O (a mirror location with a path) as pull-through mirrors | yes | nothing beyond `/v2/` | host-root route |
| Docker Engine `registry-mirrors` | no: the mirror URL may not carry a path, and pulls arrive as `/v2/library/<name>/…` without a repository segment | a host whose `/v2/` belongs to one repository | host-root binding (R-EP-007) |
| Terraform and OpenTofu registry protocol | no: service discovery reads `/.well-known/terraform.json` at the host root | one host per Terraform registry; several Terraform repositories need several hosts | host-root binding (R-EP-007) |
| OCI subdomain mode (`docker-all.registry.example.com/alpine:3`, image names without the repository segment) | optional | a wildcard host, a wildcard DNS record and a wildcard certificate | wildcard host with `wildcard_root: label` (R-EP-007) |

- **Bindings belong to hosts, not to repositories.** A repository may be bound on several hosts
  and reached by path on every endpoint at the same time; a binding never moves a repository off
  `/api/<format>/<repoKey>/`.
- **npm and NuGet never use a binding.** npm's host-root exchange (R-NPM-022, P1) is a host-root
  route of the `ci_exchange` family on every endpoint that serves it.

### R-EP-006 · Host-root binding: the reserved mechanism
Priority: **MVP** · Covers: trove-only

The MVP builds the mechanism; no MVP format uses it (Q-EP-6). R-EP-007 lists its first consumers.

- **Configuration.** `host_roots` maps exact hosts of the endpoint's host set to repository keys.
  `wildcard_root: label` binds every host that `wildcard_host` matches to the repository whose key
  equals the host label; `none` binds none. At startup, a `host_roots` key that is not an exact host
  of the endpoint, a value that is not a valid repository key (INV-5), or `wildcard_root: label`
  without a `wildcard_host` stops startup and names the key. Whether the repository exists is
  decided per request, because repositories live in PostgreSQL: a binding to a key that does not
  exist, or to a repository whose format serves no bound-host route, behaves as no binding.
- **Request context.** Middleware step 5 places the binding of the effective host in the request
  context (`endpoint.RootRepository(ctx)`), next to the response base URL.
- **Dispatch.** A format that needs host roots declares literal bound-host patterns relative to the
  host root (`/v2/`, `/.well-known/terraform.json`). The core registers them on every endpoint with
  `packages` in its surface. A request on a bound host whose path matches one of them goes to the
  bound repository's format with the repository fixed, and authorization and exposure run on that
  repository exactly as for `/api/<format>/<repoKey>/`. On an unbound host the same path keeps its
  unbound meaning (`/v2/<repoKey>/…`, Q-HA-4) or answers `404`.
- **Reserved prefix.** INV-6 reserves `/.well-known/` at the host root for discovery documents that
  clients fetch there ([RFC 8615](https://www.rfc-editor.org/rfc/rfc8615)), the first being
  Terraform's on a bound host. No other route may use it, and until a format registers one, every
  path below it answers the `404` problem. The route-table test of R-ARCH-005 fails on any other
  pattern under `/.well-known/`, as it does under `/v2/`.
- In the MVP, a request on a bound host is routed exactly like one on an unbound host.

Acceptance criteria:

- `host_roots: {mirror.example.com: docker-hub}` with no OCI format: `GET /v2/` and
  `GET /.well-known/terraform.json` on `mirror.example.com` answer the `404` problem; a unit test of
  middleware step 5 finds `docker-hub` as the binding for `mirror.example.com` and none for
  `registry.example.com`.
- A `host_roots` key outside the endpoint's hosts, and `wildcard_root: label` without
  `wildcard_host`, each stop startup and name the key.
- The route-table test fails on a fixture plug-in that registers `GET /.well-known/x`, on every
  endpoint, and passes once the fixture is removed.

### R-EP-007 · Single-repository hosts for Docker Engine mirrors, OCI subdomains and Terraform discovery
Priority: **P1** · Covers: HA-027, PKG-DOCKER-007

The format pages own the protocols; this requirement fixes that they reach host roots through
R-EP-006 and through nothing else.

- **Docker Engine mirror (OCI format, F4).** On a host bound to an OCI repository R, `/v2/…` is the
  distribution API of R, with the image name taken as sent: `GET /v2/library/alpine/manifests/3`
  reads `library/alpine` in R. A Docker Engine configured with
  `registry-mirrors: ["https://mirror.example.com"]` pulls through R. The token challenge's realm
  is built from the response base URL of that host.
- **OCI subdomain mode (F4).** With `wildcard_host: "*.registry.example.com"` and
  `wildcard_root: label`, `docker pull docker-all.registry.example.com/alpine:3` reads `alpine` in
  `docker-all`. It needs a wildcard DNS record and a certificate for `*.registry.example.com` at the
  edge and, when the edge passes TLS through, on the listener (R-EP-004). This is the subdomain
  routing of [ha-and-helm.md](ha-and-helm.md) R-HA-041.
- **Terraform discovery (the Terraform format page).** On a host bound to a Terraform repository,
  `GET /.well-known/terraform.json` answers the discovery document, whose `modules.v1` and
  `providers.v1` point below `/api/terraform/<repoKey>/` on the same host. Each Terraform registry
  needs its own host, or the wildcard mode.
- No format derives a repository from a host by any other means.

Acceptance criteria (when the formats exist):

- A Docker Engine with `registry-mirrors` set to a host bound to the remote `docker-hub` pulls
  `alpine:3`, and the counting upstream records one manifest request.
- `docker pull docker-all.registry.example.com/alpine:3` succeeds through the chart's Ingress with a
  wildcard certificate.
- `terraform init` of a module `tf.example.com/acme/net/aws` resolves through the discovery
  document of the host bound to `terraform-modules`.

## Deployment and observability

### R-EP-008 · Chart rendering of endpoints
Priority: **MVP** · Covers: HA-038, HA-039, HA-040

The chart renders each entry of `endpoints` into `trove.yaml` and into the Kubernetes objects that
carry its traffic. The default endpoint keeps the objects of [ha-and-helm.md](ha-and-helm.md)
R-HA-037 and R-HA-038 (`<fullname>`, `ingress.*`, `gateway.*`).

```yaml
endpoints: {}                             # default (proposed): only the default endpoint; example:
#  default:                               # optional; only the keys the default endpoint may set (R-EP-001)
#    hosts: []                            # default (proposed): with further endpoints, the hosts of config.server.base_url and additional_base_urls
#  customers:
#    port: 8443                           # example; required; container port and Service port
#    baseUrl: https://packages.example.com   # example; required
#    hosts: []                            # default (proposed); example [pkgs.example.org]
#    wildcardHost: ""                     # default (proposed); example "*.packages.example.com"
#    hostRoots: {}                        # default (proposed)
#    wildcardRoot: none                   # default (proposed)
#    surface: [packages]                  # default (proposed)
#    auth: {tokenTypes: [sat], boundTokensOnly: true, anonymous: false}   # default (proposed)
#    exposure: rules                      # default (proposed)
#    cacheFill: false                     # default (proposed)
#    rateLimitClass: external             # default (proposed)
#    replicaHeader: never                 # default (proposed)
#    maxInFlight: 1024                    # default (proposed)
#    trustedProxies: []                   # default (proposed): config.server.trusted_proxies
#    tls:
#      existingSecret: ""                 # default (proposed): "" = the listener pair of tls.secretName (R-HA-063)
#      extraSecrets: []                   # default (proposed); example [trove-packages-wildcard-tls]: further pairs, chosen by SNI
#      clientCaSecret: ""                 # default (proposed): no client certificates
#    pool:
#      enabled: false                     # default (proposed): served by <fullname>-web (Q-EP-3)
#      replicaCount: 2                    # default (proposed) when enabled; every key of web may be overridden here, merged key by key over the web values ([Q-EP-16](#q-ep-16--rendering-of-the-endpoint-map-and-of-the-pool-values))
#    service:
#      type: ClusterIP                    # default (proposed): ClusterIP | LoadBalancer | NodePort
#      annotations: {}                    # default (proposed)
#      loadBalancerSourceRanges: []       # default (proposed)
#    ingress:
#      enabled: false                     # default (proposed)
#      className: ""                      # default (proposed): cluster default IngressClass; example traefik-external
#      annotations: {}                    # default (proposed); controller settings of R-HA-039
#      tls: []                            # default (proposed); example [{secretName: packages-tls, hosts: [packages.example.com, "*.packages.example.com"]}]
#    gateway:
#      enabled: false                     # default (proposed)
#      parentRefs: []                     # default (proposed); example [{name: external, namespace: gateway-system, sectionName: https}]
#    networkPolicy:
#      from: []                           # default (proposed): empty = from anywhere; example [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ingress-external}}}]
```

| Kind | Name | Rendered when |
|---|---|---|
| Configuration | `server.endpoints.<name>` in `trove.yaml`; chart-managed ([ha-and-helm.md](ha-and-helm.md) R-HA-030) | per entry |
| Service | `<fullname>-ep-<name>`, one port `<port>` with `appProtocol: https` under `tls.enabled`, `sessionAffinity: None`, selecting the pods that serve the endpoint | per further endpoint |
| Deployment, PodDisruptionBudget, HorizontalPodAutoscaler | `<fullname>-web-ep-<name>` with `--role web`, `TROVE_POOL_NAME=ep-<name>`, pod label `trove/pool: ep-<name>`; the rendered `pool` of the endpoint is `ep-<name>` | `pool.enabled` |
| Ingress | `<fullname>-ep-<name>`: `ingressClassName`, one rule per host of the endpoint (the base URL's host, every exact host, the wildcard host), path `/` with `pathType: Prefix`, backend `<fullname>-ep-<name>:<port>` over TLS as R-HA-063 wires it, with the endpoint's own `ingress.backendTLS` keys overriding the global block key by key ([Q-EP-17](#q-ep-17--backend-tls-of-a-further-endpoints-ingress)), and the `tls` entries | `ingress.enabled` |
| HTTPRoute, BackendTLSPolicy | `<fullname>-ep-<name>`: `parentRefs`, `hostnames` with the same hosts (a wildcard as a leading `*.` label), one rule `PathPrefix /` | `gateway.enabled` |
| NetworkPolicy | `<fullname>-ep-<name>`: admits `<port>` on the pods that serve the endpoint, from `networkPolicy.from` only | `networkPolicy.enabled` |
| Secret mounts | `tls.existingSecret` and each of `extraSecrets` at `/var/run/secrets/trove/endpoints/<name>/<n>/` (keys `tls.crt`, `tls.key`), rendered as `tls.certificates`; `clientCaSecret` (`ca.crt`) as `tls.client_ca_file` | per entry that names them |

- **Pods and ports.** The pods that serve an endpoint (the main `web` Deployment, or the endpoint's
  pool) get its container port; no other pod listens on it (R-EP-002). An endpoint pool follows
  the rules of a web pool of R-HA-062: at least two replicas with `highAvailability.enforce`,
  spreading, a disruption budget, the drain rule of R-HA-059, and a share of the connection budget
  of R-HA-016. Endpoint pools are MVP; the path pools of R-HA-062 stay P1, and their names may not
  start with `ep-`.
- **Default endpoint hosts.** When at least one further endpoint is configured, the chart renders
  `server.endpoints.default.hosts` from the hosts of `config.server.base_url` and
  `config.server.additional_base_urls`, unless `endpoints.default.hosts` is set. Traffic that an
  edge sends to port 8080 for a customer host then answers `421` instead of reaching the default
  endpoint (H-59).
- **One edge per audience.** `ingress.className` and `gateway.parentRefs` let a further endpoint use
  another ingress controller or Gateway than the default endpoint, so external traffic can enter
  through its own load balancer.
- **Validation** ([ha-and-helm.md](ha-and-helm.md) R-HA-029): rendering fails and names the value
  for a `port` equal to 8080, 8081 or another endpoint's port; an invalid name or a name for which
  `<fullname>-ep-<name>` exceeds 63 characters; a missing `baseUrl`; a host in two endpoints;
  `config.server.endpoints` set inside `config`; `extraSecrets` or `clientCaSecret` with
  `tls.enabled: false`; and an endpoint pool below two replicas with `highAvailability.enforce`.
- **NOTES.txt** lists each endpoint with its Service and hosts, and warns when an endpoint with
  `exposure: rules` has no pool or an empty `networkPolicy.from`.
- **Local profile** ([ha-and-helm.md](ha-and-helm.md) R-HA-051): `values-dev.yaml` renders an
  endpoint `customers` on port 8443 with base URL `https://packages.trove.localhost:<port>`, its own
  Traefik route and a certificate from the dev CA.

Acceptance criteria:

- `helm template` with the `customers` example, `ingress.enabled: true`,
  `className: traefik-external` and `tls: [{secretName: packages-tls, hosts: [packages.example.com,
  "*.packages.example.com"]}]` renders: Service `trove-ep-customers` with port 8443 selecting the
  main `web` pods; Ingress `trove-ep-customers` with that class, rules for `packages.example.com`
  and `*.packages.example.com`, the TLS entry and the backend `trove-ep-customers:8443`;
  NetworkPolicy `trove-ep-customers` admitting 8443 only from `networkPolicy.from`; and
  `trove.yaml` with `server.endpoints.customers` and
  `server.endpoints.default.hosts: [trove.example.com]`.
- With `pool.enabled: true`: Deployment `trove-web-ep-customers` with `TROVE_POOL_NAME=ep-customers`,
  its disruption budget, and the Service selecting only its pods; the main `web` pods carry no
  container port 8443.
- `--set endpoints.customers.port=8080` and `--set config.server.endpoints.x.listen=:9000` each fail
  and name the value.

### R-EP-009 · Endpoint observability
Priority: **MVP** · Covers: OPS-012, OPS-019

- **Metric label.** `endpoint` takes the configured endpoint names and `default`, bounded by
  configuration to 17 values ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-007). The client-listener series of `trove_http_requests_total`,
  `trove_http_request_duration_seconds` and `trove_http_requests_in_flight` carry it (R-OPS-008);
  ops-listener series do not.
- **Families of this page:**

| Family | Type | Labels | Meaning |
|---|---|---|---|
| `trove_endpoint_listener_up` | gauge | `endpoint` | 1 while this replica's listener of the endpoint accepts connections |
| `trove_endpoint_misdirected_total` | counter | `endpoint` | `421` answers (R-EP-003) |
| `trove_endpoint_forwarded_host_ignored_total` | counter | `endpoint` | Forwarded hosts outside the endpoint's host set (R-EP-003) |
| `trove_endpoint_credentials_refused_total` | counter | `endpoint`, `reason` (`token_type`, `token_binding`, `anonymous`) | Credentials the endpoint does not accept (R-EP-010) |
| `trove_endpoint_cache_fill_refused_total` | counter | `endpoint` | Upstream contacts the endpoint's cache-fill policy prevented (R-EP-019) |
| `trove_exposure_hidden_total` | counter | `endpoint`, `kind` (`request`, `item`) | Requests answered `404` or `403` by exposure, and items removed from documents and listings (R-EP-013, R-EP-018) |
| `trove_exposure_rules` | gauge | `endpoint` | Enabled rules per endpoint, read from PostgreSQL; aggregate with `max` (R-OPS-007) |

  The TLS families of R-ARCH-035 carry `endpoint` and `certificate` (R-EP-004).
- **Request log** ([operations-and-observability.md](operations-and-observability.md) R-OPS-010):
  the fields `endpoint` (the name) and `host` (the effective host, at most 255 bytes) are set on
  every client-listener record.
- **Audit** (R-OPS-015): every event recorded during a request carries `endpoint`; events of jobs
  carry `null`. A read refused by exposure is `authz.denied` with reason `deny.exposure`, a refused
  write reason `deny.endpoint_read_only`; a refused credential is the `token.rejected` event of
  R-OPS-016 and R-AUTH-041 with reason `endpoint_not_accepted`.
- **Traces** (R-OPS-018): every server span carries `trove.endpoint`.
- **Identification headers** (R-OPS-002): `replica_header` applies per endpoint, `never` by default
  on further endpoints, so pod names do not leave the organization.

Acceptance criteria:

- After one request on each endpoint, `trove_http_requests_total` has series with
  `endpoint="default"` and `endpoint="customers"`, the ops-listener series have no `endpoint`, and
  R-OPS-007's label test accepts the label.
- A `421` on `customers` increments `trove_endpoint_misdirected_total{endpoint="customers"}` and is
  logged with `"endpoint":"customers"` and `"status":421`.
- The audit event of a read refused by exposure on `customers` has `endpoint = 'customers'` and
  reason `deny.exposure`. An authenticated response on `customers` carries no `X-Trove-Replica`.

## Credentials

### R-EP-010 · Accepted credentials per endpoint
Priority: **MVP** · Covers: trove-only

The authentication stage ([identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-001) applies the endpoint's rules after it selects the credential, and before every database
lookup that a rule makes unnecessary:

1. **Session cookie.** Evaluated only on an endpoint whose surface holds `ui`, which is the default
   endpoint. Elsewhere it is ignored as if absent: browsers do not isolate cookies by port, so a
   further endpoint on the default endpoint's host name would otherwise receive the session.
2. **Token type.** The prefix gives the type (R-AUTH-022). A type outside `auth.token_types` is
   rejected with `401` `invalid_token`, without a database lookup. This covers the NuGet API key.
3. **Binding.** A token whose scope binds it to endpoints (R-EP-011) is rejected with `401`
   `invalid_token` on every endpoint it does not name. With `auth.bound_tokens_only: true`, an
   unbound token is rejected the same way.
4. **Anonymous.** With `auth.anonymous: false`, no request on the endpoint becomes anonymous: a
   request without a credential gets `401` with the challenge of R-AUTH-002. With `true`,
   R-AUTH-037 decides as on the default endpoint.

- A rejection never falls through to a lower-ranked credential or to anonymous (R-AUTH-001). Its
  answer is the `401` `invalid_token` of an unknown token, so it does not tell that the token is
  valid on another endpoint; the audit event and `trove_endpoint_credentials_refused_total` name
  the reason.
- Type and binding refusals do not count towards the failed-credential throttle of R-AUTH-038: the
  token is known, and the type refusal happens before any lookup.
- The endpoint never adds a principal and never changes a principal set (R-AUTHZ-002).
- Defaults: the default endpoint accepts sessions, PATs, SATs and CI tokens, unbound, and defers to
  R-AUTH-037 for anonymous access, so R-AUTH-001 is unchanged there. A further endpoint accepts only
  service-account tokens bound to it, and no anonymous request (Q-EP-10).

Acceptance criteria:

- On `customers` with the defaults: a PAT gets `401` `invalid_token` and the token table records no
  query (query counter); an unbound SAT gets `401` after one lookup; a SAT bound to `customers`
  reaches authorization; the same bound SAT gets `401` on `default`.
- `curl -b '__Host-trv_session=<valid session>' https://packages.example.com:8443/api/npm/npm-customers/@acme%2fsdk`
  answers `401` `credentials_required`, because the cookie is ignored and anonymous requests are
  off.
- Anonymous access on (R-AUTH-037) and `anonymous` granted `read` on `npm-customers`: an anonymous
  `npm view @acme/sdk` succeeds on `default` and gets `E401` on `customers`.
- Each case increments `trove_endpoint_credentials_refused_total` with its reason.

### R-EP-011 · Endpoint-bound tokens
Priority: **MVP** · Covers: TOK-007

- The token scope ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-024)
  gains the field `endpoints`: a list of at most 16 endpoint names. Absent or empty, the token is
  unbound. Non-empty, the token is valid only on the endpoints it names (R-EP-010).
- `endpoints` is a binding, not a resource type: it grants nothing and changes nothing but where
  the token is accepted. The closed list of resource types of R-AUTHZ-013 is unchanged.
- **Creation.** Every name must be a configured endpoint; otherwise `400` `invalid_scope` names
  it. Scopes stay immutable, and rotation keeps the binding (R-AUTH-030).
- **Derivation rule** (R-AUTH-027). A token created under the derivation rule from a bound token
  must be bound to a non-empty subset of its endpoints; otherwise `403`
  `derivation_exceeds_parent`. An unbound token may create a bound or an unbound one.
- **CI tokens** are unbound in the MVP: the exchange (R-CI-007) takes no binding, so an endpoint
  with `bound_tokens_only: true` refuses them.
- **An endpoint removed from the configuration** leaves its bound tokens unchanged; they are valid
  on no endpoint that no longer exists, and token listings show the names under
  `"unknownEndpoints"`.

```json
{
  "description": "Acme Corp, package downloads",
  "ttlSeconds": 31536000,
  "scope": {"inherit": true, "endpoints": ["customers"]}
}
```

*(example request of `POST /api/v1/service-accounts/customer-acme/tokens`)*

Acceptance criteria:

- The request above answers `201` and the token reads back with `"endpoints": ["customers"]`; the
  same request with `"endpoints": ["nope"]` answers `400` `invalid_scope`.
- A token created under the derivation rule from that token without `endpoints` answers `403`
  `derivation_exceeds_parent`.
- The bound token reads `@acme/sdk` through `customers` and gets `401` on `default`.

## Exposure rules

### R-EP-012 · Exposure rules: model, validation and storage
Priority: **MVP** · Covers: trove-only

- **Mode.** An endpoint with `exposure: all` has no ceiling. An endpoint with `exposure: rules`
  exposes nothing until a rule admits it (deny by default). Rules of an endpoint in mode `all` are
  stored and have no effect.
- **A rule** belongs to one endpoint and one repository, named by key and stored by ID (Q-EP-8), and
  narrows by these dimensions:

| Dimension | Syntax | Default |
|---|---|---|
| `packages` | include and exclude name globs of R-AUTHZ-005 (the one matcher of [storage.md](storage.md) Q-STOR-9), with the format's case rule | unrestricted |
| `paths` | include and exclude path patterns of R-AUTHZ-005 | unrestricted |
| `versions` | include and exclude name globs over the normalized version (Q-STOR-9), and `range`: one version range in the format's native syntax (npm: `>=2.0.0 <4`; NuGet: `[2.0,4.0)`), validated by the format (R-EP-018) | unrestricted, no range |
| `prereleases` | `true` exposes pre-release versions (a SemVer pre-release identifier; NuGet's pre-release label) | `false` |
| `distTags` | include and exclude globs over tag names, for formats with tags (npm) | every tag |
| `enabled` | `false` makes the rule admit nothing | `true` |

- **Matching.** An item is exposed on endpoint E when at least one enabled rule of E for the
  addressed repository admits it. A rule admits it when the package, path and version dimensions
  all match: an empty include list leaves a dimension unrestricted, an exclude match removes the
  item, the version lies in `range` when one is set, and a pre-release only with `prereleases`.
  Exclude wins inside a rule and nowhere else, so rules add up like grants (R-AUTHZ-001).
  - A coordinate without a version (a package document, a dist-tag document) is admitted when a
    rule admits its package; its versions are then filtered inside the document (R-EP-018).
  - A repository-level coordinate (a service index, a search, the registry root) is admitted when
    E has an enabled rule for the repository; listed items are filtered per item.
  - A tag is visible when a rule that admits its package admits the tag name and the version the
    tag points to is exposed; npm re-points `latest` ([formats/npm.md](formats/npm.md) R-NPM-048).
- **Example rule** *(example)*:

```json
{
  "name": "acme-sdk",
  "description": "SDK releases for customers",
  "repository": "npm-customers",
  "enabled": true,
  "packages": {"include": ["@acme/sdk", "@acme/sdk-*"], "exclude": ["@acme/sdk-internal"]},
  "paths": {"include": [], "exclude": []},
  "versions": {"include": [], "exclude": ["2.0.1"], "range": ">=2.0.0"},
  "prereleases": false,
  "distTags": {"include": ["latest", "lts"], "exclude": []}
}
```

- **Data model.**

```sql
CREATE TABLE endpoint_exposure_rule (
  id           uuid        PRIMARY KEY,                       -- UUIDv7
  endpoint     text        NOT NULL CHECK (endpoint ~ '^[a-z][a-z0-9-]{0,19}$'),
  name         text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  description  text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  repo_id      bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  enabled      boolean     NOT NULL DEFAULT true,
  packages     jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
  paths        jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
  versions     jsonb       NOT NULL DEFAULT '{"include": [], "exclude": [], "range": null}',
  prereleases  boolean     NOT NULL DEFAULT false,
  dist_tags    jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
  revision     bigint      NOT NULL DEFAULT 1,                -- ETag (R-API-012)
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid        REFERENCES principal (id) ON DELETE SET NULL,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid        REFERENCES principal (id) ON DELETE SET NULL,
  UNIQUE (endpoint, name)
);
CREATE INDEX endpoint_exposure_rule_repo ON endpoint_exposure_rule (repo_id);
```

  - Export class `data` ([backup-and-restore.md](backup-and-restore.md) R-BAK-004). `revision` is a
    client-visible ETag and is registered with `meta.RegisterRevisionColumn`, so a restore raises it
    (R-BAK-022).
  - Every write records itself in the change area `authz` ([architecture.md](architecture.md)
    R-ARCH-018), and the compiled grant index of R-AUTHZ-028 holds the rules. A rule change
    therefore follows the same propagation bound, the same fail-closed wait on a stale index and the
    same `Trove-Authz-Min-Epoch` header as a policy change (R-AUTHZ-032). No rule is read from
    Valkey.
  - Deleting a repository deletes its rules through the foreign key, audited as
    `endpoint.exposure.deleted` with cause `repository_deleted`; a repository created later under
    the same key is not exposed.
  - Rules whose endpoint is not configured stay stored and inert: the API reports them with
    `"inert": true`, and a replica logs a warning per such endpoint name with its rule count at
    startup (H-58).
- **Limits.** At most 1,000 rules per endpoint; pattern lists as R-AUTHZ-005 (each pattern 1 to
  1,024 characters, at most 256 per list); a range of at most 256 characters.
- **Validation** (`422`): `unknown_repository`; `invalid_pattern`; `invalid_version_range` (the
  format refuses the range); `dimension_not_supported` (a non-empty `versions` or `distTags` for a
  format without versions or tags, or a non-empty `packages` for a format without package names);
  `format_not_exposable` (a format without exposure support, R-EP-018). Saved with a warning:
  `path_patterns_on_package_format`, as for policies (R-AUTHZ-005), and `endpoint_not_in_rules_mode`
  when the endpoint's mode is `all`.

Acceptance criteria:

- Fixture: `npm-customers` (virtual over `npm-internal`) holds `@acme/sdk` 1.9.0, 2.0.0, 2.0.1,
  2.1.0-rc.1 and 2.1.0, `@acme/sdk-internal` 1.0.0 and `@acme/billing` 3.0.0, with the example rule
  on `customers`. Exposed: `@acme/sdk` 2.0.0 and 2.1.0. Hidden: 1.9.0 (range), 2.0.1 (exclude),
  2.1.0-rc.1 (pre-release), `@acme/sdk-internal` (package exclude) and `@acme/billing` (no rule).
- A rule with `"range": ">=2.0.0 <"` answers `422` `invalid_version_range`; a rule with
  `distTags.include` on a NuGet repository answers `422` `dimension_not_supported`.
- Deleting `npm-customers` removes its rules; a re-created `npm-customers` exposes nothing.
- A rule written through replica A takes effect on replica B within the bound of R-AUTHZ-028, also
  with Valkey stopped, and at once for a request that sends the returned epoch in
  `Trove-Authz-Min-Epoch`.

### R-EP-013 · Exposure as a ceiling in the decision point
Priority: **MVP** · Covers: IAM-032

- **The formula.** On endpoint E, the effective right is the principal's grants ∩ the token scope
  (∩ the CI caps and the triggering-user bound) ∩ the exposure of E. Exposure only narrows: an
  exposed item still needs a grant. It binds every subject, administrators and `content:bypass`
  included (Q-EP-12), and the anonymous principal.
- **Placement** ([authorization.md](authorization.md) R-AUTHZ-015). `Allowed` checks the ceiling
  first, before `content:bypass`, against the addressed repository of the coordinate, which
  `WithRepo` does not change (Q-EP-11):

```go
func Allowed(sub Subject, idx *Index, a Action, c Coordinate) bool {
    if !idx.Exposure(c.Endpoint).Admits(a, c.Addressed, c) {      // R-EP-013: every subject
        return false
    }
    if sub.Capabilities().Has(ContentBypass) {
        return true
    }
    // … unchanged (R-AUTHZ-015)
}
```

  The virtual gate, member selection (`MemberUsable`), the populate check and the commit-time
  re-check therefore all see the ceiling. The decision point applies it to every action of a
  coordinate right after the anonymous gate and before the repository lookup, a write's read-only
  refusal before a read's hidden one
  ([Q-EP-21](#q-ep-21--where-the-ceiling-sits-in-the-evaluation-order)), for the endpoint the
  request arrived on, which the chain puts into the request context and which replaces a
  coordinate's `Endpoint` ([Q-EP-20](#q-ep-20--how-the-endpoint-of-a-request-reaches-the-decision-point)).
- **Reads only** (Q-EP-9). On an endpoint with `exposure: rules`, `Admits` refuses every action
  other than `read`.
- **Answers** ([authorization.md](authorization.md) R-AUTHZ-016):
  - a read-type request that exposure does not admit answers `404`, byte-identical to the `404` for
    a missing resource on the same endpoint; exposure is decided before the existence lookup, so
    neither the body nor the work done depends on existence (Q-EP-4);
  - a write-type request on an endpoint with `exposure: rules` answers `403` `endpoint_read_only`,
    whether or not its target exists;
  - a request without a credential that the endpoint requires gets `401` first, as R-AUTHZ-016
    orders it;
  - `hideExistence: false` (R-AUTHZ-017, P1) never applies to exposure: a refused read stays `404`.
- `reasonFor` of R-AUTHZ-015 returns `deny.exposure` and `deny.endpoint_read_only`. The decision
  explanation (R-AUTHZ-024, P1) names the endpoint and the rules it considered.
- **Performance.** With 1,000 rules on an endpoint, the 1 ms p99 target of R-AUTHZ-015 holds for
  one `Decide` call including the ceiling.

Acceptance criteria (fixture of R-EP-012):

- On `customers`, with a SAT bound to `customers` whose service account holds `admin`
  (`{"inherit": true, "admin": true, "endpoints": ["customers"]}`): `npm view @acme/sdk@2.1.0`
  succeeds; `npm view @acme/sdk@1.9.0` and `npm view @acme/billing` get `E404`. Through `default`,
  an unbound administrator token sees every version.
- `GET …/@acme/sdk/-/sdk-1.9.0.tgz` (hidden) and `GET …/@acme/sdk/-/sdk-9.9.9.tgz` (missing) on
  `customers` answer byte-identical bodies apart from `X-Request-Id`, and the item lookup counter
  records no lookup for either.
- `npm publish` on `customers` with a bound token whose service account holds `deploy` answers
  `E403` with `endpoint_read_only`.
- A bound SAT of a service account without a grant on `npm-customers` gets `E404` for
  `@acme/sdk@2.1.0`: exposure never grants.
- Every row of R-AUTHZ-015's worked trace, repeated on an endpoint with `exposure: all`, gives the
  same result as on `default`.

### R-EP-014 · Exposure on raw files, listings and the management API
Priority: **MVP** · Covers: trove-only

- **Raw files.** On an endpoint with `files`, `/files/<repoKey>/<path>` passes the ceiling with the
  coordinate the format plug-in gives the file: package name and version for npm and NuGet
  repositories, the path alone for generic ones. A folder listing (R-STOR-017, P1) lists only
  exposed entries, and a folder without an exposed entry answers `404`.
- **Generic repositories** are exposed through `paths`; a rule with a non-empty `packages`,
  `versions` or `distTags` for one answers `422` `dimension_not_supported`.
- **Management API.** On an endpoint with `api`, every listing, search result, count and item read
  that R-AUTHZ-016 filters per item is filtered by exposure as well: the repository list shows only
  repositories with an enabled rule, and the packages, items and search operations (R-API-022,
  R-API-025, R-API-028, R-API-029) list only exposed packages, versions and files. Counts and totals
  are computed after the filter. A holder of `repositories:read-config`, who lists and reads every
  repository without a read grant, is bound the same way (Q-EP-12): the list shows the exposed
  repositories only, and reading another one answers the `404` of a missing one. Capability operations (users, policies, settings) are not content;
  they need their capability, and the endpoint's credential rules decide who can present one
  (R-EP-010).

Acceptance criteria:

- `generic-local` with a rule `paths.include: ["releases/**"]` on `customers` (surface
  `[packages, files]`): `GET /files/generic-local/releases/1.0/app.zip` answers `200`, and
  `GET /files/generic-local/internal/x.bin` answers a `404` byte-identical to a missing file.
- With `api` added to `customers`, `GET /api/v1/repositories` lists only `npm-customers` and
  `generic-local`, and `GET /api/v1/packages?repository=npm-customers` lists `@acme/sdk` with the
  versions 2.0.0 and 2.1.0 only.
- A rule with `packages.include` on `generic-local` answers `422` `dimension_not_supported`.

### R-EP-015 · Rate-limit class per endpoint
Priority: **MVP** · Covers: BEYOND-030

- `server.endpoint_rate_limits` defines classes: `requests_per_second`, `burst` and `key`, which is
  `principal` (the token ID, or the client address for an anonymous request) or `address` (the client
  address of R-ARCH-006). An endpoint's `rate_limit_class` names one; empty means none.
- The endpoint limiter runs at middleware step 7 of R-ARCH-005 for every request on the endpoint,
  package protocols included, before the classes of R-API-016 and R-AUTH-038, which still apply.
- Each class is a limiter of [architecture.md](architecture.md) R-ARCH-034 named
  `endpoint_<class>`, with scope `cluster`; a hyphen of the class name is an underscore in the
  limiter name ([Q-EP-22](#q-ep-22--limiter-name-and-fallback-share-of-an-endpoint-class)). The
  bucket key includes the endpoint name, so two endpoints that share a class have separate budgets.
  While Valkey is unavailable, the per-replica share divides by the live replicas that serve the
  endpoint (the replica registry's `pool`, R-OPS-003): the web replicas of the replica's own pool
  and the replicas of the role `all`, at least 1.
- An exhausted bucket answers `429` `rate_limited` with `Retry-After` and the `RateLimit-Policy` and
  `RateLimit` fields of R-API-016 with the class name `endpoint:<class>`; on package protocols the
  body is the format's error shape (R-ARCH-023).

Acceptance criteria:

- With the default class `external`, a loop of 400 `npm view` with one bound token on `customers`
  receives `429` answers after the burst, each with `Retry-After`; a second bound token is not slowed
  down, and the first token's requests to another endpoint that accepts it are not either.
- In the `scatter` profile, 1,000 requests with one token on `customers` within 2 s are admitted at
  most 300 times, also with Valkey stopped, and `trove_ratelimit_fallback{limiter="endpoint_external"}`
  is 1 on every replica that serves `customers` while Valkey is stopped.

### R-EP-016 · Endpoint and exposure management API
Priority: **MVP** · Covers: trove-only

| Method | Path | Purpose | Permission |
|---|---|---|---|
| GET | `/api/v1/endpoints` | The configured endpoints (name, port, base URL, hosts, wildcard host, host-root bindings, pool, surface, credential rules, exposure mode, cache-fill policy, rate-limit class) with their rule counts, plus the names of inert endpoints that still have rules | `endpoints:read` |
| GET | `/api/v1/endpoints/{endpoint}` | One endpoint | `endpoints:read` |
| GET | `/api/v1/endpoints/{endpoint}/exposure-rules` | Rules; filters `repository`, `package` (rules whose patterns admit the name) and `enabled`; cursor paging (R-API-010) | `endpoints:read` |
| POST | `/api/v1/endpoints/{endpoint}/exposure-rules` | Create a rule | `endpoints:write`; recent authentication (R-API-007) |
| GET | `/api/v1/endpoints/{endpoint}/exposure-rules/{name}` | Read, with `ETag` | `endpoints:read` |
| PUT | `/api/v1/endpoints/{endpoint}/exposure-rules/{name}` | Replace; `If-Match` required (`428` without it); `name` and `repository` are immutable | `endpoints:write`; recent authentication |
| PATCH | `/api/v1/endpoints/{endpoint}/exposure-rules/{name}` | JSON merge patch on the stored rule, with the rules of PUT ([Q-EP-18](#q-ep-18--writing-an-exposure-rule-put-merge-patch-and-the-immutable-repository)) | `endpoints:write`; recent authentication |
| DELETE | `/api/v1/endpoints/{endpoint}/exposure-rules/{name}` | Delete | `endpoints:write`; recent authentication |
| POST | `/api/v1/endpoints/{endpoint}/check-exposure` | Dry run for one repository and package, or for paths of a generic repository: exposed or not, the admitting rule or the reason, the exposed and hidden versions and paths and the visible tags; an optional draft `rule` is evaluated in place of the stored rule of its name ([Q-EP-19](#q-ep-19--what-check-exposure-checks-paths-and-the-editors-draft)) | `endpoints:read` and `read` on the repository |

- Endpoints themselves are file configuration (INV-15) and have no write operation. `{endpoint}` is
  a configured endpoint or an inert name that still has rules, whose rules can be read and deleted.
- Every successful write answers `Trove-Authz-Epoch` (R-API-052, R-AUTHZ-032).
- `check-exposure` transfers no content and contacts no upstream; for a remote repository it reports
  the cached versions only. Reasons: `exposed`, `no_rule`, `rule_disabled`, `package_excluded`,
  `path_excluded`, `version_excluded`, `out_of_range`, `prerelease`, `tag_excluded`.
- **Capabilities.** `endpoints:read` and `endpoints:write` join the capability table of
  [authorization.md](authorization.md) R-AUTHZ-010: `admin` and `security-admin` hold both,
  `auditor` and `repository-admin` hold `endpoints:read`. Exposure and grants are separate keys: a
  holder of `policies:write` alone cannot open content to an endpoint, and a holder of
  `endpoints:write` alone cannot give anyone a right.
- **Audit** (R-OPS-016): `endpoint.exposure.created`, `endpoint.exposure.updated` (with
  `details.changes` and the before and after states) and `endpoint.exposure.deleted`, transactional;
  the update and delete events carry the revocation-journal flag (R-EP-020).

```json
{"repository": "npm-customers", "package": "@acme/sdk", "versions": ["1.9.0", "2.1.0"]}
```

*(example request of `check-exposure`)*

```json
{
  "endpoint": "customers",
  "repository": "npm-customers",
  "package": "@acme/sdk",
  "packageExposed": true,
  "versions": [
    {"version": "1.9.0", "exposed": false, "reason": "out_of_range"},
    {"version": "2.1.0", "exposed": true, "rule": "acme-sdk"}
  ],
  "distTags": [{"tag": "latest", "version": "2.1.0", "visible": true}],
  "exposureFingerprint": "sha256:5b1e…"
}
```

*(example response)*

Acceptance criteria:

- Create, read, replace and delete of the rule of R-EP-012 round-trip with ETags; a `PUT` without
  `If-Match` answers `428`; an `auditor` reads and gets `403` on `POST`.
- After a `DELETE` through replica A, `npm view @acme/sdk` on `customers` gets `E404` through every
  replica within the bound of R-AUTHZ-028.
- `check-exposure` without `versions` lists, for the fixture of R-EP-012, 1.9.0 as `out_of_range`,
  2.0.1 as `version_excluded`, 2.1.0-rc.1 as `prerelease`, and 2.0.0 and 2.1.0 as exposed.

### R-EP-017 · Endpoint administration screen
Priority: **MVP** · Covers: trove-only

- **Routes** ([api-and-ui.md](api-and-ui.md) R-UI-003): `/admin/endpoints` (the list: name, base URL,
  hosts, surface, exposure mode, rule count, pool; inert names flagged), `/admin/endpoints/{endpoint}`
  (the configuration read-only, marked as coming from the configuration file, and the rule table:
  name, repository, packages, version summary, pre-releases, enabled),
  `/admin/endpoints/{endpoint}/rules/new` and `/admin/endpoints/{endpoint}/rules/{name}`.
- **Rule editor.** One page like the policy editor ([api-and-ui.md](api-and-ui.md) R-UI-027, Q-UI-4):
  a repository picker of keys, include and exclude lists with syntax help, the version range checked
  through the API, the pre-release switch, tag lists only for formats with tags, and `enabled`. A
  preview panel fed by `check-exposure` (debounced 500 ms) shows, for a package typed into the
  tester, its exposed and hidden versions with their reasons. Saving shows a diff against the loaded
  revision and sends `PUT` with `If-Match`; recent authentication follows R-UI-042.
- For each repository a rule exposes, the screen shows its client address on that endpoint
  (`<base URL>/api/<format>/<key>/`, with a copy button) for handing to the consumers.
- A banner warns when the endpoint's mode is `all`, because its rules have no effect there.
- The navigation entry appears for holders of `endpoints:read` (R-UI-005). English and German
  (R-UI-045), accessibility as R-UI-044.

Acceptance criteria:

- Creating the rule of R-EP-012 through the editor stores the same document as creating it through
  the API, and the preview for `@acme/sdk` lists the versions and reasons that `check-exposure`
  returns.
- The automated accessibility check reports no serious violation on the three screens.

## Package metadata and remote repositories

### R-EP-018 · Package metadata on an endpoint: filtering, URLs and cache keys
Priority: **MVP** · Covers: trove-only

- **Format contract** ([architecture.md](architecture.md) R-ARCH-008). A format that can be served on
  an endpoint with `exposure: rules` implements `Exposure() ExposureAdapter`:

```go
type ExposureAdapter interface {
    // ValidateRange checks one version range in the format's native syntax (R-EP-012).
    ValidateRange(expr string) error
    // Version parses a normalized version and says whether it is a pre-release.
    Version(v string) (ver Version, ok bool)
    // FilterDocument removes every version, file and tag the view does not expose from a
    // rendered, merged or rewritten document, and fixes the fields that refer to them.
    FilterDocument(ctx context.Context, p Path, doc []byte, view ExposureView) ([]byte, error)
}

// ExposureView answers from the compiled rules of one endpoint and one addressed repository.
type ExposureView interface {
    Package(name string) bool
    Version(name, version string) bool
    Tag(name, tag, version string) bool
}
```

  A format without the adapter is not exposable: on an endpoint with `exposure: rules` every one of
  its routes answers `404`, and rules for its repositories are refused with `422`
  `format_not_exposable` (fail closed). npm and NuGet implement it
  ([formats/npm.md](formats/npm.md) R-NPM-048, [formats/nuget.md](formats/nuget.md) R-NUGET-034);
  a later format page states its filter before its repositories can be exposed.
- **What is filtered.** Every document that lists versions, files or tags: package documents,
  version lists, registrations, single-version and tag documents, search and autocomplete results.
  Counts (`totalHits`, `count`) count exposed items only. Every version a filtered document lists is
  downloadable by the same caller on the same endpoint, and a version no document lists is refused
  as an artifact (R-EP-013).
- **Order.** Filtering runs on every response, after the merge of a virtual repository and after
  the rewrite of a remote document, and before compression. Stored upstream documents stay
  unchanged.
- **URLs.** Every URL in a served document is built at serve time from the response base URL of the
  request's endpoint and host (R-EP-003, [repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-043).
- **Only the addressed repository is named.** On an endpoint with `exposure: rules`, responses carry
  no `Trove-Resolved-From` (R-REPO-019), and no problem body names a repository other than the
  addressed one: a member's failure is reported as the addressed repository's.
- **Cache keys and validators.**
  - The keys of the rendition cache ([architecture.md](architecture.md) R-ARCH-018) and of the
    merged-metadata cache (R-REPO-020) include the endpoint name, because the endpoint's cache-fill
    policy changes what members contribute (R-EP-019). A cached filtered document is keyed
    additionally by the exposure fingerprint.
  - The ETag of every rendered, merged or filtered document includes the endpoint name and the
    exposure fingerprint (R-NPM-013, R-NUGET-002), so a rule change changes the validator, and a
    `304` never confirms a copy that lists a version hidden since.
  - The exposure fingerprint is computed from the compiled index without a query.
  - Caches of unfiltered bytes keyed by content or by upstream request (the metadata bytes by digest
    of R-REPO-025, the forwarded NuGet search responses of R-NUGET-016) stay shared, because
    filtering and rewriting run on every use.
- Nothing of this is cached in Valkey ([architecture.md](architecture.md) Q-ARCH-16).

Acceptance criteria (fixture of R-EP-012):

- `npm view @acme/sdk versions` on `customers` prints `[ '2.0.0', '2.1.0' ]`, and through `default`
  every version.
- The package document's `ETag` on `customers` differs from the one on `default`. After a rule
  update that hides 2.0.0, a conditional request with the earlier `ETag` answers `200` with a
  document without 2.0.0, on every replica.
- A test format without the adapter answers `404` on every route on `customers`, and a rule for its
  repository answers `422` `format_not_exposable`.
- No response body or header captured on `customers` during the e2e run contains `trove.example.com`
  or the key of a member of `npm-customers`.

### R-EP-019 · Cache-fill policy per endpoint
Priority: **MVP** · Covers: trove-only

- With `cache_fill: false` (the default of a further endpoint), no request on the endpoint makes
  trove contact an upstream (Q-EP-5). Every subject fails the cache-fill check there
  ([authorization.md](authorization.md) R-AUTHZ-008, the `MemberUsable` step of R-AUTHZ-015), so a
  request is served exactly as for a caller who may read but not populate: what the cache holds,
  expired metadata served stale without a refresh, an uncached exposed artifact on a remote
  repository `403` `populate_not_permitted`, and through a virtual repository a miss
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-025 step 9, R-REPO-019). npm's
  rule for cached tarballs whose document is stale applies as for such a caller
  ([formats/npm.md](formats/npm.md) R-NPM-025).
- Forwarded search (R-NUGET-016) is not forwarded on such an endpoint; the remote answers with its
  cache-only search.
- With `cache_fill: true`, the subject's populate check decides, as on the default endpoint.
- A refused upstream contact increments `trove_endpoint_cache_fill_refused_total{endpoint}` and
  writes no negative entry (R-REPO-025 step 9).

Acceptance criteria:

- `npm-customers` gains the remote member `npmjs`, and a rule exposes `left-pad`. With
  `left-pad@1.3.0` not cached, `npm view left-pad@1.3.0` on `customers` gets `E404`, and the counting
  upstream records no request. After an internal `npm install left-pad@1.3.0` through `default`, the
  same command on `customers` succeeds.
- With `cache_fill: true` on `customers` and a bound token whose service account may populate
  `npmjs`, the first command fetches from the upstream.

## Restores

### R-EP-020 · Exposure changes in the revocation journal
Priority: **MVP** · Covers: trove-only

A database restore to time *t* undoes the exposure narrowings made after *t*, which would show
consumers versions hidden since ([ha-and-helm.md](ha-and-helm.md) H-37). The journal of R-HA-054
re-applies them.

| Audit event, flagged `journal: revocation` | Entry carries | Replay |
|---|---|---|
| `endpoint.exposure.deleted` | Rule ID, endpoint, name, repository ID | Delete the rule |
| `endpoint.exposure.updated` | Rule ID, endpoint, name, repository ID; the state before and after the change: `enabled`, `packages`, `paths`, `versions`, `prereleases`, `distTags` | Update rule of R-HA-054 with the comparator below |

- **Comparator.** `narrows(current, after)` answers true only when all of the following hold, and
  false otherwise:
  1. endpoint and repository ID are equal;
  2. `after.enabled` implies `current.enabled`, and `after.prereleases` implies
     `current.prereleases`;
  3. in each of `packages`, `paths`, the version globs and `distTags`, the exclude list of `after`
     holds every exclude pattern of `current`, and when the include list of `current` is non-empty,
     the include list of `after` is non-empty and a subset of it; patterns are compared as the
     matcher compares them, without glob containment (as R-AUTHZ-031);
  4. the ranges are equal, or `current` has none.
- **Suspension** sets `enabled: false`. A disabled rule exposes nothing, and an administrator
  re-enables it from the after-state that the replay prints. Replay notifies the change area `authz`
  and writes its audit event like every rule change.
- Rules created after *t* are lost with the restore, which fails closed. Endpoint modes are file
  configuration and are not restored, so they are not journaled.

Acceptance criteria:

- A trove backup is taken; then `acme-sdk` is narrowed to `"range": ">=2.1.0"` and a rule `beta` is
  deleted. `trove backup restore` of that backup into an empty installation reports both entries
  `applied`; afterwards `npm view @acme/sdk@2.0.0` on `customers` gets `E404` on every replica, and
  `beta` does not exist.
- A journaled update that widens `packages.include` from `@acme/sdk` to `@acme/*` is reported
  `skipped`; one that replaces `@acme/sdk` by `@acme/sdk-*` is reported `suspended`, and the rule
  reads back with `"enabled": false`.

## Worked example: a customer endpoint

The owner's case: internal npm and NuGet packages offered to customers through an external endpoint
that lists only what is released to them.

1. **Configuration.** The chart values of R-EP-008 with `customers` on port 8443, base URL
   `https://packages.example.com`, its own IngressClass `traefik-external`, a TLS Secret, and a pool.
   The rendered `trove.yaml` carries `server.endpoints.customers` and
   `server.endpoints.default.hosts: [trove.example.com]`.
2. **Repositories.** Virtual repositories `npm-customers` and `nuget-customers` over the internal
   local repositories; no remote member, so customers resolve public dependencies from the public
   registries.
3. **Exposure.** The rule `acme-sdk` of R-EP-012 on `customers`, and a NuGet rule exposing
   `Acme.Sdk` from 2.0.0, without pre-releases.
4. **Principal.** A service account `customer-acme` with a policy that grants `read` on
   `npm-customers`, `nuget-customers` and their members, and the bound token of R-EP-011.
5. **Customer configuration** *(example)*:

```ini
# .npmrc: only the @acme scope points at trove
@acme:registry=https://packages.example.com/api/npm/npm-customers/
//packages.example.com/api/npm/npm-customers/:_authToken=${ACME_TROVE_TOKEN}
```

```xml
<!-- nuget.config -->
<packageSources>
  <add key="acme" value="https://packages.example.com/api/nuget/nuget-customers/v3/index.json" />
</packageSources>
<packageSourceCredentials>
  <acme>
    <add key="Username" value="acme" />
    <add key="ClearTextPassword" value="%ACME_TROVE_TOKEN%" />
  </acme>
</packageSourceCredentials>
```

| # | Request | Steps | Result |
|---|---|---|---|
| 1 | `npm view @acme/sdk versions` with the customer token | endpoint `customers` from the listener; host in the set; SAT bound to `customers`; grant `read`; rule `acme-sdk`; document filtered | `[ '2.0.0', '2.1.0' ]`, tarball URLs on `packages.example.com` |
| 2 | `npm install @acme/sdk@1.9.0` | exposure: out of range | `E404`, as for a missing version |
| 3 | `npm view @acme/billing` | no rule | `E404` |
| 4 | The customer token against `https://trove.example.com/api/npm/npm-customers/` | default endpoint: the token is bound to `customers` | `E401` |
| 5 | An employee's PAT against `packages.example.com` | token type not accepted | `E401` |
| 6 | `npm publish` with the customer token | endpoint is read only | `E403` `endpoint_read_only` |
| 7 | `curl -H 'Host: trove.example.com'` to the customer Ingress address | effective host not in the set | `421` |
| 8 | `dotnet restore` of `Acme.Sdk 2.1.0` | as row 1 for NuGet (R-NUGET-034) | restored; the version list holds only exposed versions |

## Deferred

| Item | Note |
|---|---|
| Client setup snippets for a chosen endpoint | R-API-030 renders snippets for the default endpoint; an `endpoint` parameter that renders them on another endpoint's base URL is P1. Until then the endpoint screen shows the client address (R-EP-017). |
| Channel exposure | Exposing a version only while a named tag points to it would make versions disappear from lockfiles when the tag moves; not specified. |
| Writes through an endpoint with exposure rules | Read only in the MVP (Q-EP-9). |
| Sign-in and the UI on further endpoints | Default endpoint only (Q-EP-13). |
| Exposure rules in configuration documents | They join R-OPS-029 as an object kind when configuration as code ships (P1, M9). |
| A binding chosen at the CI exchange | CI tokens are unbound in the MVP (R-EP-011). |

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-EP-1 · How the endpoint of a request is identified

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The owner wants an external endpoint through which customers retrieve selected internal packages.
Whatever identifies the endpoint is the security boundary between what customers and what employees
can reach.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The listener the connection arrived on | Each endpoint has its own port; `Host` only selects among the endpoint's own hosts | No request header can choose the endpoint; the edge route and the NetworkPolicy of each port decide who reaches which endpoint; a customer who sends another `Host` gets `421`, not the internal surface | One port, Service, edge route and NetworkPolicy per endpoint |
| (b) The `Host` header on one listener | Virtual hosts | One port | `Host` is client data: any client that reaches the listener picks the endpoint, so the boundary would rest on every edge never passing a foreign host; HTTP/2 reuses connections across hosts |
| (c) The TLS server name | The endpoint per connection | Fixed before the first request | Absent with plain HTTP; an edge that re-encrypts sends the Service name (R-HA-063); client data as well |
| (d) A path prefix | `/ext/api/npm/…` | One host | Changes INV-6 for every format; host-root routes cannot carry the prefix |

**Choice:** (a) — the endpoint is the listener's, and request data only selects among its hosts.

**Rationale:** The exposure ceiling is only as strong as the identification it hangs on. A port is
selected by the network path, which Kubernetes Services, edge routes and NetworkPolicies control;
every header is selected by the client. The cost, one more Service and route per endpoint, is
rendered by the chart (R-EP-008). The development agent would revisit this if a supported platform
could give trove only one port, and would then fall back to (b) with an edge that is the only path
to the listener and rewrites `Host`.

**Applied in the spec:** R-EP-002 and R-EP-003 state the rule; [architecture.md](architecture.md)
R-ARCH-005 serves several client listeners and R-ARCH-006 limits forwarded hosts to the endpoint's
set; cross-page: INV-6 in [README.md](README.md) now names one client listener per endpoint and
identification by listener; [ADR-128](../decisions.md#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling).

### Q-EP-2 · Where endpoint configuration and exposure rules live

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Everything in the file | Endpoints and their rules in `trove.yaml` | One place; GitOps | Every change of what customers see is a rolling restart; replicas disagree during the rollout; no audit trail and no ETag per change |
| (b) Everything in PostgreSQL | Endpoints and rules through the API | Runtime changes | A listener must be known before the database is reachable; a port or certificate change needs a restart and new Kubernetes objects anyway, which the chart renders from values |
| (c) Network binding in the file, exposure rules in PostgreSQL | Listener, hosts, base URL, certificates, pool, surface and credential rules in the file; rules through API and UI, audited | Each value lives where its lifecycle is, by the rule of R-ARCH-020; rules change at runtime, identically on every replica, with the bounds of the grant index | Two places to look; rules can outlive their endpoint name (H-58) |

**Choice:** (c), with the rules compiled into the grant index of R-AUTHZ-028.

**Rationale:** Which ports and hosts exist is a deployment fact that the chart must render as
Services and edge routes, so it belongs with the chart values. Which versions a customer may see is
a release decision that changes daily and must be audited and identical on every replica within a
bound. Compiling the rules into the grant index gives them the propagation, staleness and epoch
rules that policies already have, without a second mechanism. The development agent would revisit
this if exposure rules had to be managed as code before configuration documents ship (P1), which
would add them to R-OPS-029 earlier.

**Applied in the spec:** R-EP-001, R-EP-012; cross-page: INV-15 in [README.md](README.md) now
places endpoints in the file and exposure rules in PostgreSQL.

### Q-EP-3 · Separate pool or shared pods for a further endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Always the main `web` pods | Every listener on every `web` pod | No extra pods | External load competes with internal load; every pod opens the internal listener |
| (b) Always a pool of its own | A Deployment per further endpoint | Bulkhead and no internal listener on external pods, by construction | At least two more pods per endpoint under the HA rule, also for an internal second endpoint |
| (c) An optional pool per endpoint, off by default, recommended for endpoints with exposure rules | `pool.enabled` in the chart; NOTES.txt warns | Each installation chooses; the in-flight bulkhead (R-EP-002) and the NetworkPolicy per port hold either way | A small installation may run an external endpoint on shared pods |

**Choice:** (c).

**Rationale:** A pool buys availability isolation and a smaller set of open listeners, but not
smaller credentials: its process holds what every `web` replica holds (H-57). That benefit does not
justify two more pods for every endpoint, for example an internal one for CI runners. The per-endpoint
in-flight limit and the per-port NetworkPolicy are on in both layouts. The development agent would
make the pool the default for endpoints with exposure rules once a reduced-privilege role for pools
exists, or once an installation shows internal requests starved by external load.

**Applied in the spec:** R-EP-002 (listeners per pool, the bulkhead), R-EP-008 (the pool values);
cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-062 makes endpoint pools MVP.

### Q-EP-4 · The answer for content an endpoint does not expose

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `404`, identical to a missing resource; writes `403` `endpoint_read_only` | The rule of R-AUTHZ-016 for refused reads | No existence oracle: a customer cannot list the names of internal packages by probing; clients treat it as "not there" | Troubleshooting needs `check-exposure` |
| (b) `403` `not_exposed` | A distinct status | Easy to diagnose | Reveals which names exist internally |
| (c) `404` with a distinct body | Same status, other text | Easier logs | The body is an oracle as well |

**Choice:** (a).

**Rationale:** The point of an external endpoint is that its audience learns nothing about what it
is not given, and R-AUTHZ-016 already hides refused reads the same way. Writes are refused with
`403` whatever exists, so the refusal reveals nothing either. Administrators see the reason through
`check-exposure` and the audit event. The development agent would revisit this only if a package
client turned out to need a distinct status, and none is known.

**Applied in the spec:** R-EP-013; cross-page: [authorization.md](authorization.md) R-AUTHZ-016
gains both rows.

### Q-EP-5 · Cache fill from upstreams on further endpoints

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As on the default endpoint | The subject's populate action decides (R-AUTHZ-008) | Customers always get the newest upstream metadata | Customers decide what trove downloads, spend the upstream's rate limits, and can probe upstreams through trove |
| (b) Off by default, `cache_fill` per endpoint | Further endpoints serve what the cache holds unless configured otherwise | No upstream contact from outside by default; an internal second endpoint can switch it on | Customers see upstream content only after internal traffic fetched it |
| (c) Never on further endpoints | No setting | Simplest | Blocks a second internal endpoint, for example for CI runners |

**Choice:** (b).

**Rationale:** Which third-party packages enter the organization is an internal decision, and an
external audience should not be able to make trove fetch or refresh anything. The setting keeps (a)
available where an endpoint serves internal clients. The development agent would revisit the default
if the owner's customers depend on upstream packages that internal traffic never fetches.

**Applied in the spec:** R-EP-019; cross-page: [authorization.md](authorization.md) R-AUTHZ-015's
`MemberUsable` and [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-025 step 9.

### Q-EP-6 · How far wildcard hosts and host-root bindings go

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The owner suspected that some registries cannot work without a wildcard host. The analysis in
[Host roots and wildcard hosts](#host-roots-and-wildcard-hosts) shows that no MVP format needs one,
that Docker Engine mirrors and Terraform discovery need a host whose root belongs to one repository,
and that only the OCI subdomain mode needs a wildcard.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exact hosts only, no bindings | Path-based everywhere | Smallest | Docker Engine mirrors and Terraform cannot be served later without a redesign |
| (b) Exact hosts, at most one single-label wildcard per endpoint, and host-root bindings of exact hosts or of the wildcard label; the mechanism in the MVP, its consumers P1 | R-EP-003, R-EP-006, R-EP-007 | Covers every known host-root case; matches what certificates can express (one label); nothing to move when OCI and Terraform arrive | A mechanism without a consumer until P1 |
| (c) Several wildcards or patterns per endpoint | Regular expressions over hosts | Most flexible | Ambiguous matches, overlaps between endpoints, certificates that cannot cover them |

**Choice:** (b).

**Rationale:** The listener, the host check and the certificate selection are built in M0 anyway,
and a binding is one more context value on the same step; adding it later would revisit the
middleware, the route-table test and INV-6. One single-label wildcard is what a wildcard certificate
covers, so a broader pattern would only produce hosts that cannot get a valid certificate. The
development agent would revisit this if a client needed a multi-label wildcard.

**Applied in the spec:** R-EP-003, R-EP-006, R-EP-007; cross-page: INV-6 in [README.md](README.md)
reserves `/.well-known/` and names host-root bindings; [ha-and-helm.md](ha-and-helm.md) R-HA-041 and
Q-HA-4 now point at endpoint bindings for the subdomain mode and the dedicated registry host.

### Q-EP-7 · Endpoints in the beta

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The owner was asked whether multiple endpoints belong in the beta. Until he answers or objects at the
walkthrough, the choice below stands.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) MVP, split over M0 to M3 | Foundation (listeners, hosts, TLS, surface, bindings, chart) in M0; exposure rules, credentials, API and UI in M1; npm filtering and cache-fill policy in M2; NuGet filtering in M3; journal entries in M5 | Each part lands with the subsystem it changes: listeners with the server shell, the ceiling with the decision point, filtering with each format | 21 more MVP requirements |
| (b) P1 after the beta | Built in the platform lane | A smaller beta | The decision point, the middleware chain and both format renderers would be revisited after they shipped |
| (c) The M0 foundation in the beta, exposure after it | Listeners and chart now | The expensive retrofit of the server shell is avoided | The owner's use case, customers seeing selected packages, is not usable in the beta |

**Choice:** (a).

**Rationale:** The parts that are expensive to add later are the ones this choice builds first: a
second listener touches the server shell, the chart and the TLS reloader of M0, and the ceiling
touches the one decision point of M1, whose tests every later milestone runs. Filtering is per
format and costs least when each format is built. The development agent would move R-EP-010 to
R-EP-020, R-NPM-048 and R-NUGET-034 to P1 and keep only the M0 foundation, option (c), if the owner
says the beta does not need customer endpoints.

**Applied in the spec:** the priorities on this page; cross-page:
[coverage/roadmap.md](../coverage/roadmap.md) M0 to M3 and M5, and
[projektplan/BETA.md](../projektplan/BETA.md).

### Q-EP-8 · How exposure rules select repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One repository per rule, named by key and stored by ID | As for anonymous grants (R-AUTHZ-003) | A repository created later is never exposed by accident; a re-created key is not exposed; version ranges have one syntax per rule, because a rule has one format | One rule per repository |
| (b) The selectors of R-AUTHZ-004 | Keys, key patterns and classes | Fewer rules | `npm-*` would expose every future npm repository to customers; mixed formats make a range ambiguous |

**Choice:** (a).

**Rationale:** An external audience is the case R-AUTHZ-003 already decided for anonymous access:
dynamic selectors are refused, because their failure mode is silent exposure. Customers are normally
offered a few virtual repositories, so the rule count stays small. The development agent would
revisit this if an installation needed rules over hundreds of repositories.

**Applied in the spec:** R-EP-012.

### Q-EP-9 · Writes through an endpoint with exposure rules

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read only | Every action other than `read` is refused with `403` `endpoint_read_only` | The owner asked for retrieval; ownership, shadowing checks and quotas were designed for internal principals | No partner upload through an external endpoint |
| (b) Actions per rule | A rule lists the actions it admits | Partner upload endpoints | Ownership domains, the shadowing check and quotas would need a review for external principals first |

**Choice:** (a).

**Rationale:** Retrieval is the request, and every write path carries checks whose threat model
assumes internal publishers. An endpoint with `exposure: all` still accepts writes, so an internal
second endpoint is not limited. The development agent would revisit this when the owner names a
partner upload case.

**Applied in the spec:** R-EP-013.

### Q-EP-10 · Credentials a further endpoint accepts by default

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The same as the default endpoint | Sessions, PATs, SATs, CI tokens, anonymous per R-AUTH-037 | No configuration | Every employee token works from outside; a leaked customer token works inside |
| (b) Service-account tokens bound to the endpoint; no anonymous; no session | R-EP-010 defaults | Customer tokens work only on their endpoint, internal tokens do not work there, and both are machine credentials with an owner | An administrator issues a bound token per customer |
| (c) Anonymous by default | A public mirror | Zero setup for public content | Wrong default for selected internal content |

**Choice:** (b).

**Rationale:** Binding in both directions makes a leaked token useful only where it was meant to be
used. Service accounts are the existing way to give a machine an identity with its own grants
(R-AUTH-020). A public endpoint stays possible by setting `anonymous: true` and granting `anonymous`
explicitly. The development agent would revisit this if customers had to sign in interactively,
which needs Q-EP-13 first.

**Applied in the spec:** R-EP-010, R-EP-011; cross-page:
[identity-and-authentication.md](identity-and-authentication.md) R-AUTH-001, R-AUTH-024, R-AUTH-027.

### Q-EP-11 · Exposure on virtual repositories: the addressed repository or its members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decided on the addressed repository | A rule on `npm-customers` decides what is retrievable through it, whichever member holds it | The operator writes what customers see through the address they use; members stay decided by grants (R-AUTHZ-009) | A new member holding names that match a rule widens what the rule exposes, as far as grants allow (H-61) |
| (b) Decided on the addressed repository and on every member | Rules on both | A member change exposes nothing new | Twice the rules; a member without a rule silently hides content, which is hard to diagnose |

**Choice:** (a).

**Rationale:** Exposure answers "what may come out of this door", and the door is the address. The
widening in (a) needs two independent changes, a member and a matching grant, and rules that name
packages exactly avoid it. The development agent would revisit this if H-61 is observed, and would
then add an optional member list to a rule.

**Applied in the spec:** R-EP-012, R-EP-013 (`c.Addressed`).

### Q-EP-12 · Whether administrators pass the exposure ceiling

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No: exposure binds every subject, `content:bypass` included | The ceiling is checked before the bypass | The endpoint is a property of the network path, not of the person; an administrator credential used from outside cannot see more than the endpoint shows | An administrator checks content through the default endpoint |
| (b) `content:bypass` passes exposure | As for grants | Administrators see everything everywhere | A leaked administrator token turns the customer endpoint into the internal one |

**Choice:** (a).

**Rationale:** The ceiling describes what may leave through an entry point, so it must not depend on
who asks. Administrators keep every right on the default endpoint. The development agent sees no
case for revisiting this; an emergency is handled through the default endpoint.

**Applied in the spec:** R-EP-013; cross-page: INV-9 in [README.md](README.md) intersects with the
endpoint's exposure, and [authorization.md](authorization.md) R-AUTHZ-001 and R-AUTHZ-015 name the
ceiling.

### Q-EP-13 · Sign-in and the UI on further endpoints

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The default endpoint only | `ui` is refused on further endpoints, and session cookies are evaluated nowhere else | Redirect URIs, post-logout URI and `__Host-` cookies stay bound to `server.base_url` (R-AUTH-003, Q-AUTH-2) | No browser portal for customers |
| (b) Per-endpoint redirect URIs and sessions | An OIDC client registration and a session per endpoint | A customer portal | Every provider registration, the cookie rules and CSRF would have to be specified per endpoint |

**Choice:** (a).

**Rationale:** The owner's case is package retrieval by clients, which needs no browser. (b) would
reopen the login design for a use case nobody asked for. The development agent would revisit this
when a customer portal is requested.

**Applied in the spec:** R-EP-005, R-EP-010.

### Q-EP-14 · Labels of the TLS series and of the ops series

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the TLS families name the material of every listener, the endpoint listeners included, and the request families already
carry an `endpoint` label.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `kind`, `listener`, `name`; the listener is `client:<endpoint>` for a further endpoint | one family set for every listener | bounded by the configuration; one set of alerts; the endpoint is named where its listener is | the endpoint is part of the listener label, not its own label |
| (b) one family set per endpoint | a family name carries the endpoint | a dashboard selects one endpoint by name | the family set multiplies with the endpoints; names are not bounded by one rule |
| (c) an `endpoint` label on each TLS family | the same label as the request families | one label for every endpoint-scoped series | a second label that repeats `listener` for the same listener |

**Choice:** (a). The ops listener's series carry `endpoint=""`, the empty value the request families use on the ops listener.

**Rationale:** The listener label is already bounded by the configuration (at most sixteen further endpoints), and one family
set keeps the expiry and failed-rotation alerts identical for every listener. The development agent would revisit this when an
alert must select one endpoint across kinds of material.

**Applied in the spec:** [R-OPS-007](operations-and-observability.md) (label set of `trove_tls_*`), [R-EP-004](#r-ep-004--tls-on-endpoint-listeners-certificates-by-sni-wildcard-certificates-reload),
[R-EP-009](#r-ep-009--endpoint-observability) (the empty `endpoint` value on the ops listener).

### Q-EP-15 · Which forwarded host counts on an endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only the header family `server.forwarded_header` selects; no fallback | `Forwarded` `host=` or `X-Forwarded-Host`, one of them | the client cannot choose the host the edge did not write; the rule is one line | an edge that can only send the other family needs its setting changed |
| (b) `Forwarded` first, then `X-Forwarded-Host` | the earlier wording of this page | works with either edge | a client can send both, and the fallback picks the one the client chose when the edge sent none |
| (c) The request's `Host` only | no forwarded host | no header to trust | an edge that terminates on another host cannot route to an endpoint |

**Choice:** (a).

**Rationale:** The forwarded-host and the forwarded-address rules have one trust rule (R-ARCH-006): read the one family the edge
writes, never the other. An audit finding showed the fallback let a client choose the address that the edge had not written; the
same reasoning applies to the host. The development agent would revisit this only if the edge could not write its own family, and
then changes the setting, not the rule.

**Applied in the spec:** [R-ARCH-006](architecture.md#r-arch-006--client-address-and-trusted-proxies) (one family for address,
scheme and host), [R-EP-003](#r-ep-003--hosts-of-an-endpoint-allowlist-421-and-the-response-base-url) (forwarded host only from
the configured family).

### Q-EP-16 · Rendering of the endpoint map and of the pool values

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `admin config validate` prints `server.endpoints` as one JSON row; a pool's values are merged key by key over the web values | one value per map, the merge per key | the table stays readable; a value a pool does not set is kept | a JSON cell is less readable than a row per endpoint |
| (b) One row per endpoint key | a flat listing | every key is greppable | the listing grows with the endpoints and loses the map's shape |
| (c) A pool's values replace the web values as a whole | the pool is a complete copy | simple to read | a key that is not repeated in the pool is silently lost |

**Choice:** (a). The chart's merge (`trove.mergeValues`) takes each key of the pool over the web values, recursively, and a key set
to `false`, `0` or empty wins.

**Rationale:** The effective view is for the operator's review, and a map is one value in the environment as well (JSON in
`TROVE_SERVER_ENDPOINTS`), so one row matches what the operator writes. A key-wise merge is the only choice that keeps every web
setting a pool does not state. The development agent would revisit this when the effective view gets a structured form.

**Applied in the spec:** [R-EP-001](#r-ep-001--endpoints-and-their-configuration) (the effective view),
[R-EP-008](#r-ep-008--chart-rendering-of-endpoints) (pool values).

### Q-EP-17 · Backend TLS of a further endpoint's Ingress

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each key of the endpoint's `ingress.backendTLS` overrides the global block for that endpoint | a per-key override | a customer endpoint changes only the CA or the server name it needs | a reader must merge two blocks in the head |
| (b) A complete block per endpoint | each endpoint states every key | nothing is inherited | the same controller setting is repeated per endpoint |
| (c) The global block only | one setting for every Ingress | simplest | an endpoint that needs another CA cannot be served |

**Choice:** (a).

**Rationale:** Most of the backend settings (the controller, the verification on) are the same for every endpoint, and a
customer endpoint usually differs in one key. The merge is the one of Q-EP-16. The development agent would revisit this when an
endpoint needs a different controller in the same release.

**Applied in the spec:** [R-EP-008](#r-ep-008--chart-rendering-of-endpoints) (the Ingress of a further endpoint),
[R-HA-063](ha-and-helm.md#r-ha-063--chart-tls-wiring) (the backend settings of the Ingress).

### Q-EP-18 · Writing an exposure rule: PUT, merge patch and the immutable repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-EP-016 lists `PUT` only. The API conventions offer merge patch on every keyed resource (R-API-013), their
contract suite tests it, and the rule table of R-EP-017 toggles `enabled` per row. The journal comparator of R-EP-020
treats a rule whose repository changed as one that does not narrow.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PUT` only; the repository may change | As the table says | One write form | A toggle sends the whole rule; a repository change turns a narrowing edit into a suspension at a restore; the convention suite does not apply |
| (b) `PUT` and `PATCH` (merge patch); `name` and `repository` immutable | Both forms, one validation | The table can toggle `enabled` with a one-member patch and `If-Match`; the rule keeps one format and one range syntax; the convention suite runs on it unchanged | One more operation |
| (c) `PATCH` only | Partial updates | Small bodies | The editor's save is a whole document; a `PUT` of a `GET` body is the R-API-003 round trip |

**Choice:** (b). A different `repository` answers `422 validation_failed` with the pointer `/repository`; to move a rule,
create one for the other repository and delete the old one.

**Rationale:** Exposure is a ceiling, so the cost of a mistaken write is content shown to an outside audience; one validation
for both forms and a fixed repository keep every write reviewable against the rule it replaces, and the journal entry
compares like with like. The development agent would revisit the immutable repository if operators move rules between
repositories often, and would then journal a move as a deletion and a creation.

**Applied in the spec:** R-EP-016 (the `PATCH` row; `name` and `repository` immutable on `PUT`); cross-page:
[api-and-ui.md](api-and-ui.md) R-API-007 lists the exposure-rule writes.

### Q-EP-19 · What check-exposure checks: paths and the editor's draft

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the dry run of R-EP-016 takes a repository, a package and versions. Generic repositories have no packages and no
versions; they are exposed through `paths` (R-EP-014). The preview of the rule editor (R-EP-017) shows the effect of the
rule being edited, before it is saved.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stored rules, packages and versions only | As written | Smallest | Nothing to check for the only format of M1; the preview shows the saved state, not the edit |
| (b) `paths` for the path dimension, and an optional draft `rule` evaluated in place of the stored rule of its name (or in addition) | The check answers for paths too; the draft is validated like a write and stored nowhere | The preview shows what the edit will do, with the same matcher the decision point uses; generic repositories can be checked | A larger request body |
| (c) A separate preview operation for drafts | Two operations | The stored-rule check stays as written | Two operations with one matcher; the UI calls both |

**Choice:** (b). `paths` items answer `path`, `exposed`, `rule` and `reason`; a draft with another repository than the check's
answers `422`; the exposure fingerprint is that of the stored rules, unaffected by the draft. Without `versions`, a format
with versions lists the package's stored versions; no format of M1 has versions.

**Rationale:** The editor exists to change what customers see, and a preview of the saved state does not show the change. One
operation keeps one matcher behind the answer and behind the ceiling. The development agent would revisit this if drafts made
the check slow enough to need its own limits.

**Applied in the spec:** R-EP-016 (the `check-exposure` row).

### Q-EP-20 · How the endpoint of a request reaches the decision point

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the coordinate carries the endpoint (R-AUTHZ-015), but `Readable` of a listing takes no coordinate, and every content
route would have to fill the field. A route that forgets it would decide without the ceiling: exposure would fail open.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each caller sets `Coordinate.Endpoint` | As the pseudo-code reads | Explicit | Fails open where a caller forgets it; `Readable` needs a new parameter |
| (b) The principal carries the endpoint and the subject copies it | One more field through the conversions | Every caller that builds its subject the contract's way gets it | A subject built by hand has none; the principal is about who, not where |
| (c) The chain puts the endpoint into the request context at stage 7; the decision point reads it there and ignores the coordinate's | A context value of the decision point | Every decision of a request applies its endpoint's ceiling, listings included; a caller can neither forget it nor pick another endpoint's | A decision on another context than the request's has no ceiling |

**Choice:** (c). `authz.WithEndpoint` is set by the composition root for every request of an endpoint's chain; a coordinate's
`Endpoint` counts only without a request endpoint (a job, a command, a test).

**Rationale:** The endpoint is a property of the listener, so it travels with the request and not with the caller's code. The
remaining risk, a decision on a detached context, is narrower and is named in H-166. The development agent would revisit this
if content routes started deciding in background work for a request, which would need the endpoint passed on explicitly.

**Applied in the spec:** R-EP-013 (the placement); cross-page: none, R-AUTHZ-015's pseudo-code reads `c.Endpoint`, which the
decision point fills from the request.

### Q-EP-21 · Where the ceiling sits in the evaluation order

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-EP-013 places the ceiling first in `Allowed`. In R-AUTHZ-015, `Decide` looks the repository up and refuses an
anonymous request for a repository with `requireAuthentication` before `Allowed` runs, so on an endpoint with anonymous access
and exposure rules a missing repository (`404`) and a repository that requires authentication (`401`) would answer
differently whatever the rules say.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) First in `Allowed` | As written | The pseudo-code unchanged | The repository's existence and settings show through the answer before exposure is decided |
| (b) First in `Decide`, after the anonymous gate and before the repository lookup, for every action; read-only before hidden | The ceiling sees the addressed repository by key | A missing, a hidden and a protected repository give the same `404` on every endpoint with rules; a write is `403 endpoint_read_only` whether or not its target exists | The ceiling runs once more per coordinate than in (a)'s virtual gate |

**Choice:** (b). The anonymous gate of R-AUTH-037 stays first, so a request without a credential that the endpoint requires is
`401` as R-AUTHZ-016 orders it.

**Rationale:** Q-EP-4 says exposure is decided before the existence lookup so that neither the body nor the work depends on
existence; the repository's own settings are part of what must not show. The development agent sees no case for revisiting
this; on an endpoint in mode `all` the order makes no difference.

**Applied in the spec:** R-EP-013 (placement before the repository lookup).

### Q-EP-22 · Limiter name and fallback share of an endpoint class

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a class name may hold a hyphen (`^[a-z][a-z0-9-]{0,19}$`), a limiter name may not (R-ARCH-034). The share of
R-ARCH-034 divides by the live replicas of the role; an endpoint is served by the replicas of its pool.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `endpoint_<class>` with hyphens as underscores; the share counts the web replicas of the replica's own pool and the replicas of role `all` | One limiter per class; a per-limiter share | Class names stay as configured; the share is the set that serves the replica's endpoints, which all replicas of one pool share | A web pool that no endpoint names is counted apart from the main Deployment it serves with (H-167) |
| (b) Refuse hyphens in class names | Validation | Literal names | Breaks configurations the M0 validation accepts |
| (c) One limiter per endpoint | `endpoint_<class>_<endpoint>` | Exact share per endpoint | The metric label the spec names no longer exists; the limiter set grows with the endpoints |

**Choice:** (a). The limiter takes its share from a count of its own (`LimiterSet.NewLimiterShared`,
[architecture.md](architecture.md) Q-ARCH-45).

**Rationale:** Every endpoint a replica serves has the same serving replicas, so one count per replica is exact in the layouts
the chart renders. The development agent would revisit this if pools that serve the no-pool endpoints were rendered on purpose.

**Applied in the spec:** R-EP-015 (the name and the share).

### Q-EP-23 · An empty list of accepted token types

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `auth.token_types: []` is valid YAML. The authentication stage reads an empty list of accepted types as no
restriction, which is right for a caller that sets no rules and wrong for an operator who meant "no token".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Empty accepts every type | As the stage reads it | No change | An operator's "accept nothing" silently accepts every token, PATs included |
| (b) Empty accepts no token | A sentinel in the rules | Literal | A new field in the stage's contract; an anonymous-only endpoint is possible with `anonymous: true` anyway, where tokens do no harm |
| (c) Startup refuses an empty list | Validation names the key | No silent reading either way; one line of validation | An endpoint cannot be set to accept no token at all |

**Choice:** (c).

**Rationale:** A credential setting that fails open is the case R-OPS-028's validation exists for, and no use case needs an
endpoint that refuses every token while serving packages. The development agent would revisit this when such an endpoint is
asked for, and would then add (b).

**Applied in the spec:** R-EP-001 (the startup validation list).

### Q-EP-24 · Token listings show the bound endpoints that are not configured

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-EP-011 says that an endpoint removed from the configuration leaves its bound tokens unchanged and that token listings show the names under `"unknownEndpoints"`; the token representation had no such member.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Listings only | The member is on list items | Where the spec says it | `GET /tokens/{id}` and `/tokens/current` differ from a list item |
| (b) The token representation (chosen) | `unknownEndpoints` is a required array member of `Token` (list, inspection, current) | One representation | A member on an answer that creates a token would be empty by construction |

**Choice:** (b), not on the answer that creates a token (`TokenCreated`: its endpoints were validated a moment ago). The member holds the names of `scope.endpoints` that no configured endpoint carries, `[]` for an unbound token and when every name is configured. The API server asks the configuration through `AuthServices.EndpointConfigured` (the predicate of the scope check, `TokenScopeOptions`); without one nothing is unknown.

**Rationale:** the member is derived from the configuration at read time, so adding the endpoint back removes the name from it. The development agent would revisit this if a listing must filter on it.

**Applied in the spec:** none.

### Q-EP-25 · A deleted repository records the exposure rules it takes with it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`endpoint_exposure_rule.repo_id` deletes with the repository row, so the rules vanished without `endpoint.exposure.deleted` (R-EP-012 asks the event with cause `repository_deleted`, H-164).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A trigger on the row | The database writes the audit row | Cannot be bypassed | The audit writer, its journal entry and the epoch are the application's |
| (b) A delete hook of the repository service (chosen) | `repository.Hooks.OnDelete` runs in the transaction that removes the row, just before it goes; `authz.ExposureHook` reads the rules of the repository under a row lock, notifies the change area `authz` and records one event per rule with `cause: repository_deleted`, the journal entry of R-EP-020 and the actor stored with the deletion job | The same transaction as the removal; the epoch and the journal are the ones of an API delete | Every path to the row must call the hook |
| (c) The job records after the fact | Read the rules, delete, then record | No hook | A crash between the two loses the events |

**Choice:** (b). Both ways to the row call it: the job (`finishDelete`) and the immediate removal of a virtual repository (`deleteNow`). `repository.MultiHooks` composes it with the settings hook; the composition root builds one `ExposureService` before the repository wiring for both. An error of the hook fails the transaction, so a deletion never happens without its events: the integration test makes the recorder fail and sees the repository and its rules stay until the retry succeeds.

**Rationale:** the rules stop exposing when the repository is marked `deleting` (the grant index leaves them out), so the event completes the trail and the revocation journal. The development agent would revisit this if a second kind of row refers to a repository by a cascading key and needs the same.

**Applied in the spec:** none; R-EP-012 reads unchanged. Closes H-164.

### Q-EP-26 · A rule patch is written conditional on the revision it was computed on

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`PATCH` of an exposure rule computed the merge on the stored rule and wrote it with the revision of `If-Match`; for `If-Match: *` the write had no revision condition, so a change between the read and the write was merged over silently. The policy `PATCH` already writes conditional on exactly the version it read (Q-AUTHZ-26).

**Choice:** the handler compares `If-Match` with the revision it read (412 with the current tag), merges, and writes with that revision, also for `*`. A concurrent change is a 412, never a lost update. `PUT` keeps the blind write of `*`, which is its meaning: the caller supplies the whole document.

**Rationale:** a merge is computed from what was read, so the write must be valid only against what was read. The development agent would revisit this if clients retry a 412 of a `*` patch automatically and the retry storm matters.

**Applied in the spec:** none.

### Q-EP-54 · The surface of the dev profile's endpoint `customers`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E0.16 introduced `customers` with `surface: [packages]`, which at the time was the only family with routes of its
own. E1.14 needs raw files under exposure (R-EP-014: `GET /files/generic-local/releases/1.0/app.zip` on `customers`) and the
listings of the management API under exposure (an administrator token bound to `customers` sees no more than the rules
expose), so the endpoint must serve the families `files` and `api`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `[packages, files, api]` on `customers` | The dev endpoint serves all three families a customer endpoint can | One endpoint carries E0.16 and E1.14; E0.16's checks (every path answers the one `401 credentials_required` without a credential) are unchanged | The dev endpoint is no longer the minimal package-only endpoint |
| (b) A third endpoint for E1.14 | A second further endpoint with its own host, certificates and Ingress | E0.16's endpoint stays minimal | Another certificate pair, Ingress and NetworkPolicy in the dev profile for a change of one list; the criteria name `customers` |
| (c) Switch the surface during the test | `helm upgrade` in the test | No change to the profile | Two rollouts per run for a value the endpoint can simply carry |

**Choice:** (a).

**Rationale:** The criteria name the endpoint `customers`; a surface is a value, not a mechanism. The revisit condition is an
E1.14 variant that needs an endpoint without `api`, which would be (b).

**Applied in the spec:** none; [deploy/dev/values-dev.yaml](../../deploy/dev/values-dev.yaml) and
[../developer/local-profile.md](../developer/local-profile.md#the-second-endpoint).

## What this does not cover

### H-57 · A further endpoint runs with every credential of a `web` replica

Stage: every request on a further endpoint (R-EP-002), served by the main `web` pods or by the
endpoint's pool (R-EP-008).

Live for every installation that configures a further endpoint, most of all one reachable from the
internet. Subject: the database, object-store and Valkey credentials, the stored-secret key ring
and the private key of the listener pair the pod loads (its own endpoint pair, and the pair of
`tls.secretName` when the operations listener inherits it or an endpoint has no pair of its own)
of the process that serves it. A pool keeps external load off internal pods and keeps the internal
listener closed on its pods, but its process holds the same credentials as every `web` replica, so
a flaw reachable through the external endpoint (request parsing, format code, H-1) reaches internal
data as from any other request. No reduced-privilege role exists for endpoint pools. The chart
mounts the private key of the default endpoint's pair (`tls.secretName`) only into pods that load it:
with `tls.ops.secretName` set and pairs of their own on the pool's endpoints, an endpoint pool and
the workers hold no copy of it; without `tls.ops`, every pod holds it, because its operations
listener inherits the pair. Set `tls.ops` when you run pools. What makes it real: an exploitable flaw in a code path that the
endpoint's surface exposes.

### H-58 · Exposure rules are bound to an endpoint name

Stage: `endpoint_exposure_rule.endpoint` (R-EP-012).

Live when an operator removes an endpoint from the configuration while its rules remain, and later
configures an endpoint under the same name for another audience: the old rules apply to the new
endpoint at once. Subject: the content those rules expose. Mitigation: inert rules are flagged by the
API, the screen and a startup warning, and can be deleted. What makes it real: reusing an endpoint
name without reviewing its rules.

### H-59 · Traffic routed to the default endpoint's port bypasses exposure

Stage: the default endpoint, port 8080, with `exposure: all` (R-EP-002, R-EP-003).

Live where the default endpoint checks no hosts (a binary run outside the chart, or chart values
that clear `endpoints.default.hosts`) and an edge route, a LoadBalancer Service or a hand-written
Ingress sends an external host to port 8080. Identification by listener then gives those requests
the default endpoint's surface and no ceiling. Bound customer tokens are refused there (R-EP-010),
so the exposure is limited to what anonymous access and unbound credentials reach on the default
endpoint. The chart renders the default endpoint's hosts whenever further endpoints exist, which
turns such traffic into `421`, and the NetworkPolicy of port 8080 limits who reaches it. What makes it
real: an edge misconfiguration together with anonymous access or an unbound token on the external
side.

### H-60 · Package-level metadata reveals activity of hidden versions

Stage: npm package documents and their `Last-Modified` on endpoints with `exposure: rules`
([formats/npm.md](formats/npm.md) R-NPM-048).

Live for every npm package that an endpoint exposes in part. `time.modified` and `Last-Modified`
keep the source document's value, because they must never move backwards for downstream proxies
(R-NPM-012), so a consumer can tell that a package changed (a hidden version was published or a tag
moved), though not what changed. NuGet documents carry per-version times only. What makes it real:
release timing that is itself confidential.

### H-61 · Exposure follows the members of an addressed virtual repository

Stage: rules on virtual repositories (R-EP-012, Q-EP-11).

Live when a virtual repository addressed through an endpoint gains a member that holds packages
matching one of the rule's patterns, and the endpoint's principals hold `read` on that member, for
example through a class selector. Their versions become retrievable without any change to an
exposure rule. Subject: the packages of the new member. Mitigation: rules that name packages exactly,
and grants for customer service accounts by repository key. What makes it real: broad name patterns
together with broad grants.
