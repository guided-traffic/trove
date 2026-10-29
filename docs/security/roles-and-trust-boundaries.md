# Roles and trust boundaries

Status: implemented in M0 for the listeners, the client-address rule, the trace trust rule and the Valkey data path. No
login, identity provider or repository exists yet, so no principal is authenticated. The sections that would need one say
what is not there.

This page says which process plays which role, which party each boundary trusts and for what, and where a request or a
value crosses a boundary. The code that implements each rule is linked. Open gaps are at the end of the page.

## Roles

| Role | Process | Serves | Background work and leases | Chart |
|---|---|---|---|---|
| `web` | `trove serve --role web` | the client listener of the default endpoint and of the further endpoints its pool serves; the ops listener | none: no job loop, no lease pool ([roles.go](../../internal/app/roles.go), `SubsystemsFor`) | Deployment `<fullname>-web` and the pools of further endpoints |
| `worker` | `trove serve --role worker` | the ops listener only | the job claim loop, scheduler and reaper; the lease pool | Deployment `<fullname>-worker` |
| `all` | `trove serve --role all` | both listeners | both sets | not rendered by the chart; for a local run outside the chart ([spec INV-2](../spec/README.md#design-invariants)) |
| migration | `trove migrate up` | nothing: no listener | none | Job `<fullname>-migrate` ([migrate.yaml](../../deploy/helm/trove/templates/migrate.yaml)) |

`trove backup` is reserved and answers "not implemented yet". Its role has no code yet.

## Trust boundaries

```text
            clients, package managers, browsers
                          |
             +------------v-------------+
             | edge (ingress controller)|  trusted to set ONE forwarding header
             | TLS, backend verification|  family (server.forwarded_header) and
             +------------+-------------+  only from its own addresses
                          | HTTPS (tls.enabled), verified by the edge
   +----------------------v-----------------------+
   | web pod                                       |
   |  client listener :8080 (default endpoint)     |   host outside the endpoint: 421
   |  further endpoint listeners (own port each)   |
   |  ops listener :8081  (probes, metrics)  <-----+---- kubelet, monitoring
   +---+-----------+-----------+-------------------+      unauthenticated; the
       |           |           |                          NetworkPolicy is the fence
       v           v           v
   PostgreSQL   Valkey      object store    trace collector (OTLP)
   verify-full  TLS, data   https, no       SDK transport, not the
   + file creds nodes and   egress policy   outbound factory (H-53)
                Sentinels
```

| Boundary | Trusted party and for what | What it cannot do | Configured by | Code |
|---|---|---|---|---|
| Edge to client listener | the edge reports the client address and the forwarded host and scheme, in one header family | make a client's own entries count as the edge's | `server.trusted_proxies`, `server.forwarded_header`, per endpoint `trusted_proxies` | [clientaddr.go](../../internal/httpserver/clientaddr.go) |
| Client to an endpoint | the listener picks the endpoint; `Host` picks a host of that endpoint | reach another endpoint's content by a header | the listener of the endpoint, `endpoints.<name>.hosts` | [endpoint.go](../../internal/httpserver/endpoint.go) |
| Kubelet and monitoring to the ops listener | nothing: no authentication on `:8081` | (not enforced by trove) | `server.ops_tls`, `metrics.auth`, the chart's NetworkPolicy | [ops.go](../../internal/httpserver/ops.go) |
| Client to trace context | a peer named in `tracing.trusted_peers` may continue its trace | a client choose the trace ID | `tracing.incoming_context`, `tracing.trusted_peers` | [tracing.go](../../internal/observability/tracing.go) |
| Replicas to PostgreSQL, Valkey, object store | the server's certificate, checked by the client | a plaintext connection unless named with `allow_insecure` | `database.ssl_mode`, `valkey.tls`, `storage.s3` | [transport-encryption.md](transport-encryption.md) |
| Replicas to Valkey data | the value a Valkey read returns | grant access or supply served content (INV-3) | the key families of [valkey.go](../../internal/cluster/valkey_keys.go) | this page, below |
| Replicas to identity provider, upstream registries | not yet built (M1, M2) | not applicable in M0 | not applicable | [identity-and-authentication.md](../spec/identity-and-authentication.md), [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |

## Client address

The client address is the TCP peer, unless the peer is one of `server.trusted_proxies`. Then it is the right-most entry of
the one header family that `server.forwarded_header` selects (`x-forwarded-for`, the default, or `forwarded`, RFC 7239)
and that is not itself a trusted proxy. The other family is never read, not even as a fallback, because a client can
send both and only the edge's own family is the edge's ([clientaddr.go](../../internal/httpserver/clientaddr.go), `Resolve`
and `forwardedFor`).

- An entry that is not an address (`unknown`, an obfuscated identifier) stops the walk; the peer stays the client, so a
  client cannot make the log show an address of its choosing through a value the edge did not write.
- A prefix of length 0 (`0.0.0.0/0`, `::/0`) is refused at load and at start: it would trust every peer
  ([config/server.go](../../internal/config/server.go), `Validate`; [clientaddr.go](../../internal/httpserver/clientaddr.go),
  `NewResolver`).
- The forwarded scheme and host count only when the peer is trusted, and then only for an origin listed in
  `server.additional_base_urls` or equal to `server.base_url`. Every other case generates URLs from `server.base_url`.
- On an endpoint that checks hosts, a forwarded host counts only when it lies in the endpoint's host set. Otherwise it is
  ignored and counted in `trove_endpoint_forwarded_host_ignored_total`
  ([clientaddr.go](../../internal/httpserver/clientaddr.go), `resolveHost`).

The rule gives the right answer only when the edge appends the address it sees to the selected family. See
[H-62](#h-62--client-address-follows-the-header-the-edge-sends).

## Endpoint identification

The endpoint of a request is the one whose listener accepted the connection. Only the middleware chain of that listener
places it into the request context ([endpoint.go](../../internal/httpserver/endpoint.go), `EndpointFrom`). `Host` and the
forwarded host choose among that endpoint's own hosts. A host outside the set answers `421 Misdirected Request` with the
problem code `misdirected_request` and is counted in `trove_endpoint_misdirected_total`
([errs/codes.go](../../internal/errs/codes.go), [errs.go](../../internal/errs/errs.go)). The decision is
[ADR-128](../decisions.md#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling)
and [spec/endpoints.md](../spec/endpoints.md) Q-EP-1.

## Trace trust

`tracing.incoming_context` takes `trusted_peers` (the default), `all` or `none`
([config/trace.go](../../internal/config/trace.go)). With `trusted_peers`, an incoming `traceparent` is continued only when
the TCP peer lies in `tracing.trusted_peers`, which is empty by default; then no incoming context is continued
([tracing.go](../../internal/observability/tracing.go), `peerTrusted` and `continues`). A context that is not continued
becomes a span link, so the trace ID the client receives in `X-Request-Id` and the request log carry the trove-generated ID.

- `tracing.trusted_peers` refuses a catch-all prefix (`0.0.0.0/0`, `::/0`) at load
  ([config/trace.go](../../internal/config/trace.go), `Validate`).
- `server.trusted_proxies` grants no trace trust. The edge forwards the client's own `traceparent` unchanged, so trusting
  the edge for trace context would let every client choose the ID that `X-Request-Id` and the logs carry (the
  rationale in the comment of [config/trace.go](../../internal/config/trace.go)). The decision is
  [spec/operations-and-observability.md](../spec/operations-and-observability.md) Q-OPS-32.

## Operations listener

The ops listener serves `/healthz`, `/readyz` and `/metrics` without authentication. `metrics.auth: static` protects
`/metrics` alone, and it reads its bearer token from a file on each check
([metrics.go](../../internal/observability/metrics.go), `authorized`). The readiness answer names checks and fixed reason
codes (`no_sentinel`, `circuit_open`, `probe_failed` and the others of [valkey_reason.go](../../internal/cluster/valkey_reason.go)),
never an address or a raw error text. Reachability of the listener is the one fence, and it is described under
[H-41](../spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only).

## Installation facts before identity ships

Until the identity work of M1 nobody holds `system.read`. The switch `system.bootstrap_anonymous_read` (default `false`)
lets an anonymous caller read `/api/v1/system/info` and `/api/v1/system/replicas` on the default endpoint's client
listener, and only there: the bridge answers for a request only when the endpoint it arrived on is the default endpoint and
admits anonymous requests (`server.endpoints.default.auth.anonymous`, [access.go](../../internal/app/access.go), `Require`); a
further endpoint refuses the same request as unauthenticated ([config/system.go](../../internal/config/system.go), [app.go](../../internal/app/app.go), `bootstrapAccess`;
[access.go](../../internal/app/access.go)). Startup logs a warning while it is set
([warnings.go](../../internal/app/warnings.go)) and refuses it when `server.base_url` or the `base_url` of any further endpoint
is an `https` origin other than localhost ([config/finalize.go](../../internal/config/finalize.go), `Validate`). The dev profile sets it
([values-dev.yaml](../../deploy/dev/values-dev.yaml)). See [H-63](#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships).

## Valkey is a boundary of its own

Valkey holds shared ephemeral state: the coordination keys, the negative cache of remote repositories and, later, rate
limits ([spec INV-3](../spec/README.md#design-invariants), [architecture.md](../spec/architecture.md) Q-ARCH-16). Nothing
trove serves is read from it, and no positive authentication or authorization result is. A value read from Valkey can make
trove refuse a request or fetch from an upstream again; it cannot grant access or supply content. Change messages on the
Valkey channel only evict caches early and never advance the applied epoch ([changelog.go](../../internal/cluster/changelog.go),
`handleMessage`), so a forged or lost message cannot suppress a change, because the poll applies every change again.

The write path of Valkey is therefore a denial-of-service boundary. Its exposure is recorded as
[H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service). The credential side of the boundary is on
[privilege-footprint.md](privilege-footprint.md).

## What this does not cover

### H-62 · Client address follows the header the edge sends

Stage: the client address of every request, which the request log records
([clientaddr.go](../../internal/httpserver/clientaddr.go), `Resolve`).

Live when an entry of `server.trusted_proxies` (or of an endpoint's `trusted_proxies`) is a proxy that does not append the
peer's address to the selected header family. The walk then takes the client's own entry, so the client chooses its logged
address. The chart default `trusted_proxies: []` keeps the rule at the TCP peer, which makes the gap dormant until an
operator names an edge. Subject: the client address in the request log, which the rate limits and the audit trail of
the spec will also use once they exist.

What makes it real: an edge that forwards a client's header unchanged, or sets none, listed as a trusted proxy. The edge's
configuration is outside trove, and trove cannot check it.

### H-63 · Anonymous read of installation facts and the replica list until identity ships

Stage: `GET /api/v1/system/info` and `GET /api/v1/system/replicas` on the default endpoint's client listener, reached
through the edge ([access.go](../../internal/app/access.go), `bridgeAccess.Require`). These two and no other: the bridge
matches the request path (`bootstrapPath`), so the other operations of `settings:read` (the scheduled tasks, the encryption
status with its key IDs, the authentication settings) answer `401` to an anonymous caller with the switch on (Q-AUTHZ-71,
`TestBootstrapAnonymousReadOnlyOnDefaultEndpoint`).

Live in the dev profile today, because [values-dev.yaml](../../deploy/dev/values-dev.yaml) sets
`system.bootstrap_anonymous_read: true`. Dormant on an installation that leaves the default `false`. The startup guard covers
only `https` origins other than localhost (the default endpoint's and every further endpoint's), so an `http` base URL with
the switch on starts with a warning. A further endpoint is not reached by the bridge, whatever its surface lists.

Subject: the bucket name, the object-store endpoint, the Valkey mode and the topology of the replicas (their names, nodes
and versions, lease names, and the check results of [replica registry](../spec/ha-and-helm.md)). Anyone who reaches the default
endpoint's listener (`:8080`) reads them. The switch ([config/system.go](../../internal/config/system.go)) outlived the
identity work of M1; Q-AUTHZ-16 removes it together with `bridgeAccess` once the dev profile signs in.

What makes it real: the switch set on an installation that is reachable from clients. The dev profile is not, but its edge
is on `localhost`.
</content>
