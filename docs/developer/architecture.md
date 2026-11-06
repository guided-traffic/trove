# Architecture of a replica

What runs where, how a request travels through the HTTP shell, and in which order a replica starts and
stops. Requirements: [R-ARCH-001](../spec/architecture.md), [R-ARCH-005](../spec/architecture.md),
[R-ARCH-006](../spec/architecture.md), [R-ARCH-024](../spec/architecture.md),
[R-OPS-001](../spec/operations-and-observability.md), [R-OPS-002](../spec/operations-and-observability.md).

## Packages of the shell

| Package | Responsibility |
|---|---|
| [cmd/trove/serve.go](../../cmd/trove/serve.go) | Loads the configuration, turns `SIGTERM`/`SIGINT` into a context, calls `app.RunWith`. A second signal ends the process at once |
| [internal/app](../../internal/app) | Composition root: role subsystems, start order, shutdown sequence, wiring of every infrastructure client |
| [internal/httpserver](../../internal/httpserver) | Listeners and their TLS (`ListenerTLS`), `Router` (ServeMux wrapper), the middleware `Chain`, client address, in-flight limit and memory guard (`memory.go`), route-table rules, graceful shutdown (`server.go`; `linger*.go` holds a closing connection until the peer has acknowledged its last bytes and resets a cut one, Q-ARCH-187), and the endpoint model (`Endpoint`, host check, surface), see [endpoints.md](endpoints.md) |
| [internal/tlsfiles](../../internal/tlsfiles) | Reloadable TLS material of every listener and client (leaf package), see [tls.md](tls.md) |
| [internal/observability/health](../../internal/observability/health) | Readiness checks with debounce, the liveness watchdog, the probe handlers |
| [internal/api/v1](../../internal/api/v1) | Management API handlers and the OpenAPI document, see [http-api.md](http-api.md) |

| File in `internal/app` | Content |
|---|---|
| [app.go](../../internal/app/app.go) | `Options`, `Run`, `RunWith`, `New`, `Start`, `bootstrap`, `Shutdown` |
| [shutdown.go](../../internal/app/shutdown.go) | `shutdownBudget` and `planShutdown`: the absolute times of every shutdown phase |
| [warnings.go](../../internal/app/warnings.go) | The standing configuration warnings (unset `server.base_url`, `system.bootstrap_anonymous_read`): at start and every hour |
| [component.go](../../internal/app/component.go) | `Component` and the optional shutdown phases `Drainer`, `Settler`, `Quiescer`, `Flusher` |
| [roles.go](../../internal/app/roles.go) | `SubsystemsFor(role)`: the table of R-ARCH-001 as data; a role's wiring `Area`s are the steps of the list that run in it (`areasFor`); the explicit shell profile (`Subsystems.shell`) |
| [steps.go](../../internal/app/steps.go) | The wiring framework: `step`, `newStep`, `wiringSteps` (the list, in start order, with the roles each step runs in), `planSteps` (checked in `New`), `runSteps`, `part[T]`, `requireInputs`, `late[T]` |
| [areas.go](../../internal/app/areas.go) | The typed results of the steps: `keysArea`, `postgresArea`, `auditArea`, `blobArea`, `valkeyArea`, `coordArea`, the bundle `infra` that every domain step takes, and the domain areas (`repositoryArea`, `identityArea`, `authzArea`, `filesArea`, ...); an area added later declares its own result type in its own file |
| [identity.go](../../internal/app/identity.go) | Instance name (`TROVE_POD_NAME`, `POD_NAME`, `HOSTNAME` in Kubernetes, else `hostname-pid`), random instance UID, node name |
| [wire_observability.go](../../internal/app/wire_observability.go), [wire_keys.go](../../internal/app/wire_keys.go), [wire_postgres.go](../../internal/app/wire_postgres.go), [wire_audit.go](../../internal/app/wire_audit.go), [wire_blob.go](../../internal/app/wire_blob.go), [wire_valkey.go](../../internal/app/wire_valkey.go), [wire_coordination.go](../../internal/app/wire_coordination.go) | One file per infrastructure area: the step (`wireKeys`, `wirePostgres`, `wireAudit`, `wireBlob`, `wireValkey`, `wireCoordination`, `wireInfra`) and its component |
| [wire_tls.go](../../internal/app/wire_tls.go) | `startTLS`: one `ListenerTLS` per listener of the role (every endpoint listener included), the `tls` first-readiness check |
| [wire_endpoints.go](../../internal/app/wire_endpoints.go) | The endpoints this process serves, their TLS, chain and listener; see [endpoints.md](endpoints.md) |
| [access.go](../../internal/app/access.go) | `bridgeAccess`: the bridge of the M1 decision point. It opens `settings:read` for anonymous callers under `system.bootstrap_anonymous_read` and the two bootstrap operations (Q-AUTHZ-16, Q-AUTHZ-71); every other check asks `internal/authz` |
| [facts.go](../../internal/app/facts.go) | System information from the configuration (the format list is the catalog's); the replica list from the replica registry |
| [fixture_on.go](../../internal/app/fixture_on.go) / [fixture_off.go](../../internal/app/fixture_off.go) | The `drainfixture` test build |

## The role bastion

`trove serve --role bastion` does not go through `app.New`: [cmd/trove/serve.go](../../cmd/trove/serve.go) chooses it before the main
tree is loaded and calls [`app.RunBastion`](../../internal/app/bastion.go), which runs [internal/bastion/server](../../internal/bastion/server).
The server builds the same shell as the other roles (the TLS reloaders of `internal/httpserver`, the request log and metrics of
`internal/observability`, the health registry, the client address resolver) around two listeners, `:8443` and `:8081`, and enables none of the main
installation's subsystems: no PostgreSQL, no object store of the main installation, no jobs, no formats, no API, no UI. The client
listener's chain is stages 1 to 5, then the engine for every path (`Router.SetFallback`, so the engine sees the path as received); there is no
authentication stage and no rate-limit stage, because the engine's guard owns both. A path outside the mirrored families
is `404 bastion_not_served`; the server builds and runs the link, the cache, the shared state, the guard and the download buffer around the engine. [bastion.md](bastion.md) has the packages, the seams and the flows. On the main installation the chain gets a stage
between the client address and the authentication (`Chain.Bastion`, only on the default endpoint) that recognizes a Bastion request and marks its response;
for a recognized request it resolves the client address and the base URL again with `bastions.trusted_proxies` trusted (`httpserver.WithResolved`),
and it refuses a forward outside `/api/<format>/` and `/files/` before the authentication.

## What runs where

`SubsystemsFor` ([roles.go](../../internal/app/roles.go)) encodes the table of R-ARCH-001 for the roles `api`, `packages`, `worker`,
`web` and `all`. `api`, `packages` and `web` have a client and an ops listener, the main pool, Valkey, the change poll and the registry;
`worker` has no client listener, plus the lease pool and the job loops; `all` is both and logs a warning, `web` (the bridge of the
upgrade across the cut) logs one too. A `worker` has no socket on `:8080`; a test proves it (`R-ARCH-001 a worker replica opens no
client socket`). `Subsystems.Families` is the list of route families the role serves (`config.RoleFamilies`).

A role also has wiring **areas** (`Subsystems.Areas`), which are the steps of the list that run in it; the step list gives each step a
role set (`roleSet` in [steps.go](../../internal/app/steps.go): `onAPI`, `onPackages`, `onWeb`, `onAll`, `onWorker`, `onShell` and the unions
`everyRole`, `withListener`, `onFilling`, `onManagement`). Every serving role has the infrastructure areas (`keys`, `postgres`, `audit`,
`blob`, `valkey`, `coordination`, `infra`) and the domain areas of its jobs (`keyring`, `formatports` (the data ports of the formats and the
view adapters over them, before `exposure` because the rule service takes the adapters as its source of stored versions, Q-ARCH-253),
`exposure`, `repository`, `virtual`, `invalidation`, `proxy`, `identity`, `authz`, `ownership`, `ci`, `vuln`, `formats`, `files`). The areas
of the client listener are split by route family:

| Role | Route families | Listener areas it builds | Listener areas it does not build |
|---|---|---|---|
| `api` | `ui`, `api`, `ci_exchange` | `limits`, `virtual-engine` (the explanation only, over `proxy.View`, a read-only view of the remote members), `packages-api`, `vuln-api`, `sessions`, `login`, `auth` (with sessions), `endpoints`, `api`, `ci_exchange` | `proxy-engine`, `formathost` |
| `packages` | `packages`, `files` | `limits`, `proxy-engine`, `virtual-engine`, `formathost`, `auth` (tokens only), `endpoints`, `api` | `sessions`, `login`, `ci_exchange`, `packages-api`, `vuln-api` |
| `web`, `all` | all | the union | none |
| `worker` | none | none | all |

The area `mirror` (the mirror's counters and gauges and, at the first job of a process that claims one, the engine, [mirror.md](mirror.md)) is on every role: its gauges are what the alerts read (Q-OPS-171). The area `mirror-engine` (the remote engine and the format host behind `mirror.Host`: the client path's in `all`, an engine of its own in `worker`) is built on `worker` and `all` only, the roles with job loops (Q-ARCH-316).

A `worker` therefore runs the deletion, token and fold jobs on the same services an `api` replica has, and builds no session store, login
or limiter. The test `TestR_ARCH_001_RoleTableIsData` ([roles_table_test.go](../../internal/app/roles_table_test.go)) holds the table as
data and compares `SubsystemsFor` with it. The **shell profile** (`Options.SkipInfrastructure`, the `drainfixture` build) is
`Subsystems.shell()`: the listeners, the `keys` area and the `shell` area (the authentication stage over an empty token store), no
PostgreSQL, Valkey or object store; it keeps the role's families. It is a named profile, not what is left when a step finds a nil component
(Q-ARCH-209). `New` checks the step list against the role (`planSteps`), so a role whose steps cannot be satisfied does not start.

**Optional inputs** (Q-ARCH-300). A step may mark an input optional with `withOptional(zero)` in the step list. `auth` takes `sessionsArea`
optionally (`packages` has no `sessions` area: the stage is built over tokens only and ignores a session cookie), `virtual-engine` takes
`proxyEngineArea` optionally (`api` has no `proxy-engine` area: the engine is built over the read-only view). The step requires the part
exactly when the role enables its area (`a.sub.Has(...)`), so the role decides, not a nil check; `planSteps` keeps the order check for a
role that enables both. `requireServing` (step 9) asks for the parts of the areas the role enables, not for every part.

**What `api` has instead of a format host** (Q-ARCH-301). Only the explanation of a resolution reaches an engine on `api`. The `files` area
builds, on a role with a client listener and without the `formathost` area, a `formathost.Host` without remote engine, resolver and
dispatcher (`wireServeCheckHost`), which the format's `ServeCheck` reads its stored marks through. **Unserved families** (Q-ARCH-302,
Q-ARCH-306): every path rule of a chain stage asks `httpserver.Endpoint.ServesRequest` first, so a request of a family the process does not
serve is the request to `/does-not-exist`, before and after authentication. The authentication stage is given `EndpointRules.Served` and
its rules read the path of `rulePath` ([stage.go](../../internal/auth/stage.go)): `""` for an unserved family, which no rule matches (no
exchange, no always-open route, no public operation, no session-cookie path rule, no hard limit of `/api/v1`, the challenge of an unknown
path). The preflight and stage 7 ask `familyServed` ([ratelimit.go](../../internal/api/v1/ratelimit.go)): no header of the management API,
no CORS answer, no rate-limit class. A new path rule in a stage asks the same question; `TestR_ARCH_001_AFamilyOfTheOtherRoleIsAnUnknownPath`
([roles_test.go](../../test/integration/roles/roles_test.go)) compares status, header and body of every unserved family with
`/does-not-exist` and fails on a rule that does not.

A client listener exists per **endpoint** (R-EP-002): the default endpoint on `server.listen` and every entry of
`server.endpoints` that belongs to the process's pool (`TROVE_POOL_NAME`) and whose surface holds a family of the role
(`servesEndpoint`, [wire_endpoints.go](../../internal/app/wire_endpoints.go)). A process in a pool no endpoint names opens those without a
pool, `all` opens every one, a `worker` none. The endpoint's effective surface on the process is its configured surface intersected with
the role's families (`httpserver.ForRoleFamilies`, Q-ARCH-298). Each endpoint has its own `http.Server`, router (built from its surface),
chain instance and TLS block; the sections below describe the chain once, [endpoints.md](endpoints.md) says what differs per endpoint.

The limiters' fallback count (`replicaCount`) is the live replicas of the role in the pool plus the `all` replicas (Q-ARCH-303). The fetch share does not use it: the proxy engine divides by `cluster.FillCount`, the live replicas that fill client misses
(`packages`, `web`, `all`, pools included; [wire_proxy_engine.go](../../internal/app/wire_proxy_engine.go), Q-ARCH-297, Q-OPS-165).
The content routes of `api` (`/api/v1/repositories/{repoKey}/content/{path}`, R-API-025, Q-UI-106) are registered with `/files/`
in [wire_files.go](../../internal/app/wire_files.go) and dropped by family from the router of `packages`; the files service
behind them reaches the format's serve check through the host of `wireServeCheckHost`.

A `worker` claims the queues of `jobs.queues`, or all but `jobs.exclude_queues` (R-ARCH-016, built:
[coordination.md](coordination.md#queues-and-the-queues-a-process-claims)); the advisory sync runs on the queue `vuln`.

## Request path (client listener)

```text
net/http server ── track (stall deadlines, active-request registry for the shutdown)
  └─ 1 recovery        panic -> 500 problem "internal"; logger into context; r.Pattern resolved; Connection: close while draining
     └─ 2 trace        observability Tracer.Middleware (traceparent, trace ID)   [injected]
        └─ 2 request ID  X-Request-Id = trace ID (minted if no trace stage); X-Trove-Replica for replica_header=always; OnRequest hook names the handler
           └─ 3 request log + metrics   observability.RequestLog               [injected]
              └─ 4 in-flight limit, then the replica's memory guard (slots, heap gate)   503 server_busy + Retry-After: 1
                 └─ 5 client address    Resolver: peer, trusted proxies, base URL; Annotate hook; per endpoint: effective host, host check (421), binding
                    └─ preflight  CORS preflight of an allowed origin, answered before authentication (Q-API-138)
                       └─ 6 authentication principal or anonymous (bearer token or session cookie); invalid credential -> 401; X-Trove-Replica for authenticated callers
                       └─ 7 rate limits: api_read, api_write, api_anonymous classes (cluster-wide in Valkey, per-replica share as fallback; ADR-102)
                          └─ 8 Router (ServeMux): API operations, /ui/, ... ; unmatched -> 404 problem, wrong method -> 405 + Allow
                                                  (404 where the route hides its methods from the caller, Router.HideMethods)
```

[chain.go](../../internal/httpserver/chain.go) builds this in one function; a test records the order
(`R-ARCH-005 stages run in the fixed order`). The route label of the log and the metrics is
`r.Pattern`, resolved by the outermost stage with `Router.Pattern`, because every stage that copies the
request would otherwise hide the mux's own pattern from the log stage.

The ops listener ([ops.go](../../internal/httpserver/ops.go)) serves `GET /healthz`, `/readyz`,
`/metrics` through the same recovery and request-ID stages and an optional request log. It has no
in-flight limit and no memory guard, so a probe is never refused because the request pool is busy.

### Stage 4 and the memory guard

[memory.go](../../internal/httpserver/memory.go) holds `MemoryGuard`, which every endpoint's chain of a replica shares
(`Chain.Memory`); [internal/app/memory.go](../../internal/app/memory.go) builds it at `Start` (R-ARCH-005,
[Q-ARCH-240](../spec/architecture.md#q-arch-240--a-replica-admits-what-its-memory-holds-memory-slots-and-a-heap-gate-in-step-4)).

1. `applyMemoryLimit` sets the Go memory limit of R-HA-036: `runtime.memory_limit_ratio` × `runtime.memory_limit_bytes`, unless
   `GOMEMLIMIT` is in the environment. No limit known means no guard (`NewMemoryGuard` returns nil, which admits everything), and a
   warning.
2. `requestMemory` is what a download of a large object holds, the request bytes: `blob.DownloadBufferBytes` (the verified
   download's buffer, rounded to its pool class) + `storage.s3.http.receive_window` + `requestOverhead` (128 KiB; 100 downloads of
   1, 2 and 5 MiB held open measured 636 to 659 KiB each on macOS, `TestHeldDownloadCost`, which measures 2, 5 and 8 MiB because
   a Linux loopback send buffer takes a whole 1 MiB file). The guard counts bytes
   ([Q-ARCH-328](../spec/architecture.md#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets)):
   `Admit` adds the request bytes to `used` in a compare-and-swap loop and refuses with `memory_slots` when `used` would pass
   0.8 × limit less the caches' bytes (`App.cacheBytes`, the sum of `cache.Registry.Stats()` read with every heap sample), unless
   nothing else is held. The caches' byte budgets (`cacheBudgets`, Q-ARCH-243) are no longer subtracted; they are the floor that
   the chart checks and what the start logs an error about (budgets at 80 % of the limit).
   `Admit` returns a `Ticket`; `Chain.inflightStage` puts it in the request's context. `blob.Store.Open` (option
   `blob.WithMemory(httpserver.SettleMemory)`) calls `Ticket.Settle(blob.DownloadCost(size))`, which lowers the charge to the
   overhead plus the buffer class of that size plus `min(size, window)`; a second `Settle` of the same request adds. A charge
   never grows after the admission, so nothing is refused half way. `Slots()` is (0.8 × limit − caches) ÷ request bytes and `InUse()`
   the charges in the same unit, rounded up: the two gauges keep their names.
3. `Run` reads `/gc/heap/live:bytes` + `/memory/classes/heap/stacks:bytes` every 50 ms. At 80 % of the limit the gate closes
   (`Admit` refuses with `heap`) until the value is below 70 %. Why not the allocated heap: near the Go limit it is high by design.
   A closed gate that sees `/gc/cycles/total:gc-cycles` unchanged for a second calls `runtime.GC` (Q-ARCH-242): an idle replica
   would otherwise keep the last cycle's high value for two minutes.
4. `Chain.shed` answers every refusal the same way, `503 server_busy` with `Retry-After: 1`, rendered through the path's error
   shape, and calls `OnShed(reason)`, which the composition root counts in `trove_http_requests_shed_total{endpoint,reason}`.

The endpoint's `max_in_flight` is asked first and its slot released if the guard refuses. The charges are what makes a burst safe:
admission happens before the request allocates, so the heap gate alone would admit a whole burst inside one GC cycle. What they
do not see is a cache that grows after the admission (H-539): that is the heap gate's.

Every route of a listener is registered through `httpserver.Router`. The generated API operations, the
UI and later the format dispatcher all use `Router.Handle`, so `Router.Patterns()` is the whole route
table. At start-up the composition root runs two checks over it: `Document.CheckRoutes` (API routes and
`api/openapi.yaml` agree in both directions) and `httpserver.CheckRouteTable` (nothing but the OCI format
below `/v2/`, no wildcard first segment, R-HA-041). A violation stops start-up and names the pattern. Every endpoint has its own router and the checks run over each: `CheckSurface`
(no route of a family outside the endpoint's surface) and `CheckRouteTable`, which now also reserves `/.well-known/`
(R-EP-006). The `Document.CheckRoutes` comparison runs for the endpoints that serve `api`.

### Client address and base URL

[clientaddr.go](../../internal/httpserver/clientaddr.go) is the only place that reads `Forwarded`,
`X-Forwarded-*` and `Host` (R-ARCH-006). The client is the TCP peer unless the peer lies in
`server.trusted_proxies`; then it is the right-most address of the forwarding header that is not itself
trusted. An entry that is not an address stops the walk and leaves the proxy as the client.

`server.forwarded_header` (`x-forwarded-for` # default, or `forwarded`) names the **one** header family
the edge rewrites. The resolver reads that family for the client address, the scheme and the host, and
**never falls back to the other**: a client can send both, and the edge appends its observation to
only one, so a fallback (or a preference for the other) lets a client choose its own address. The
`forwarded` family is `Forwarded: for=`, `proto=`, `host=` of RFC 7239; the `x-forwarded-for` family is
`X-Forwarded-For`, `-Proto`, `-Host`, `-Port`. A prefix that covers every address (`0.0.0.0/0`, `::/0`) is
refused in `server.trusted_proxies`, by the config loader and again by `NewResolver`. Forwarded scheme and
host change the base URL only from a trusted peer and only when the origin is one of
`server.additional_base_urls` (or `server.base_url` itself). Handlers read `httpserver.InfoFrom(ctx)`.

Every response of both listeners carries `X-Content-Type-Options: nosniff`, set once by the outermost
stage of the chain ([chain.go](../../internal/httpserver/chain.go)).

## TLS on the listeners

Both listeners serve TLS when their block has certificates (R-ARCH-035): the client listener from `server.tls`, the
ops listener from `server.ops_tls` (`inherit: true`, the default, takes `server.tls`'s pairs, `min_version` and
`reload_interval` without client certificates; `inherit: false` is its own block). Without certificates a listener is
plain HTTP; a TLS client listener beside a plain ops listener is refused by the configuration.

```text
startTLS (step 2)                        per handshake
  NewListenerTLS("ops", OpsListenerTLS)    GetConfigForClient ── one *tls.Config per material generation
  NewListenerTLS("client", server.tls)       GetCertificate ── SNI: exact name, wildcard, else the first pair
  AddReadiness("tls", Loaded…, first)        ClientAuth + ClientCAs (client_auth, client_ca_file)
Serve
  ListenerTLS.Run ── poll every reload_interval, swap on content change, keep the old pair on a bad one
```

`httpserver.NewListenerTLS` never fails on a file: a pair that does not load at start lets the listener bind and refuse
handshakes, and the `tls` check holds the first readiness until it loads (no restart). A rotation that does not load
keeps the previous pair, counts `trove_tls_reload_errors_total` and logs an error; readiness does not change. TLS 1.2
offers ECDHE AEAD suites only, ALPN offers `h2` and `http/1.1`. Each further endpoint listener takes its own block
the same way, with the reloader label `client:<endpoint>` (R-EP-004, [endpoints.md](endpoints.md#tls)). Details, rotation and
tests: [tls.md](tls.md).

## Start-up

`App.Start` returns after step 2; `bootstrap` runs the rest in the background so a replica that cannot
reach PostgreSQL stays alive and unready.

| Step (R-ARCH-024) | Where |
|---|---|
| 1 configuration | `config.Load` in `serve.go` |
| 2 logging, metrics (with the TLS families), tracing; the listeners' TLS and the `tls` check (`startTLS`); ops listener; readiness 503, liveness 200 | `Start`, [wire_tls.go](../../internal/app/wire_tls.go) |
| 3 PostgreSQL pools, schema gate, `system_state` facts (an unreachable database or a schema that is behind is retried with backoff, readiness stays 503; a configuration error, a failed preflight or a schema floor above this binary ends the process; the component closes the pools it opened on every error or cancel return, because `startComponent` records a component only after `Start` succeeded) | `wirePostgres`, `postgresComponent` ([wire_postgres.go](../../internal/app/wire_postgres.go)) |
| 4 audit: retention check, `audit_maintain_partitions()` until the current and next month exist, the recorder | `wireAudit` ([wire_audit.go](../../internal/app/wire_audit.go)) |
| 5 key ring; `outbound.New` with its `Err()` as a first-readiness check (the first step of the list, a configuration check) | `wireKeys` ([wire_keys.go](../../internal/app/wire_keys.go)) |
| 6 object store: wait while `outbound.Err()` reports a problem (a CA file of `proxy.outbound.tls.ca_files` that loads later clears it without a restart), `s3.NewFromFactory` (a `storage.s3.ca_file` that does not load is a configuration error), `blob.NewGuard`, `blob.New`, `blob.AwaitStartupCheck` (a transient failure keeps the replica unready, a configuration error ends the process), the `objectStore` check (`first = !health.require_object_store`), the garbage collector on `worker` and `all` (handed to the `gc` job kinds through `late[*blob.Collector]`) | `wireBlob` ([wire_blob.go](../../internal/app/wire_blob.go)) |
| 7 Valkey in the background with `cluster.WithKeyspaceID`; signals; change log and cache registry (`invalidation` check); replica registry; on `worker` and `all` the lease manager and the job worker (worker first, then leases), on `web` only the `jobs.Queue` | `wireValkey`, `wireCoordination` ([wire_valkey.go](../../internal/app/wire_valkey.go), [wire_coordination.go](../../internal/app/wire_coordination.go)); `wireInfra` ([wire_keys.go](../../internal/app/wire_keys.go)) bundles the results into the `infra` value and refuses a nil member by name |
| 8 the domain areas, in the order of the list: the key-ring canary (`wireKeyRing`), the exposure ceiling and rule service (`wireExposure`), the repository snapshot and service (`wireRepository`), the routing of virtual repositories (`wireVirtual`), the cache invalidation (`wireInvalidation`), the upstream clients with the binding of the remote settings (`wireProxy`), the identity stores (`wireIdentity`), the grant index (`wireAuthz`), package ownership (`wireOwnership`), the CI issuer and key service (`wireCI`), the format contract (`wireFormats`: data-port bindings, storage hooks and the files seam), the item store and files service (`wireFiles`); on a replica with a client listener the proxy and virtual engines (`wireProxyEngine`, `wireVirtualEngine`, which takes the item store from `filesArea`), the format Host and dispatcher (`wireFormatHost`, which requires the virtual engine), the package screens' services (`wirePackagesAPI`), the limiter set (`wireLimits`), the sessions (`wireSessions`), the OIDC and break-glass login (`wireLogin`), the authentication stage (`wireAuth`), the endpoints' rate-limit classes (`wireEndpoints`), the API stage (`wireAPI`) and the CI token exchange over the limiters of `wireLimits` (`wireCIExchange`); the shell profile has `wireShell` instead. Then `requireServing` refuses to open the client listener when an area it serves is missing | [steps.go](../../internal/app/steps.go) (`wiringSteps`), [wire_repository.go](../../internal/app/wire_repository.go), [wire_virtual.go](../../internal/app/wire_virtual.go), [wire_invalidation.go](../../internal/app/wire_invalidation.go), [wire_proxy.go](../../internal/app/wire_proxy.go), [wire_identity.go](../../internal/app/wire_identity.go), [wire_login.go](../../internal/app/wire_login.go), [wire_api.go](../../internal/app/wire_api.go), [wire_authz.go](../../internal/app/wire_authz.go), [wire_files.go](../../internal/app/wire_files.go), [wire_ownership.go](../../internal/app/wire_ownership.go), [wire_ci.go](../../internal/app/wire_ci.go), [wire_formats.go](../../internal/app/wire_formats.go), [wire_exposure.go](../../internal/app/wire_exposure.go) |
| 9 client listeners (`web`, `all`): one per endpoint of the process, all bound before the replica can be ready | `startClient`, [wire_endpoints.go](../../internal/app/wire_endpoints.go) |
| 10 `startup` check passes | `bootstrap` sets the flag and refreshes the checks |

At start, and again every hour while the replica runs, `standingWarnings` logs at WARN level: `server.base_url`
unset on a replica with a client listener (R-OPS-005: URLs then come from the request's `Host`), and
`system.bootstrap_anonymous_read: true`. The config loader refuses the latter together with an https
`server.base_url` whose host is not `localhost`, `127.0.0.1` or `::1`
([finalize.go](../../internal/config/finalize.go)).

Before serving, `freezeRegistries` freezes the sealed columns, the revision columns and the table registry; the job
registry ([wire_jobs.go](../../internal/app/wire_jobs.go)) is frozen where it is built: the kinds other packages put in
`jobs.Default`, the coordination kinds, and `gc` / `gc-full` with their tasks (schedules from `maintenance.tasks`,
fallback `*/30 * * * *` and `0 2 * * 0`, dedup key `gc`). It is a registry per replica, not `jobs.Default` itself, so
several replicas in one test process do not collide.

Readiness: `startup`, `database`, `schema`, `invalidation`, `outbound` gate; `tls` (when a listener serves TLS) gates the
first readiness only: every listener pair and client CA has loaded once (until then the ops listener answers with a
temporary certificate, `/healthz` 200 and `/readyz` 503, so HTTPS probes do not restart the container, Q-ARCH-36); `objectStore` gates the first readiness only
unless `health.require_object_store`; `valkey` is reported and holds back the first readiness only on a configuration
error (Q-HA-13). `/readyz?verbose=1` shows Valkey only through the fixed reason codes `connecting`,
`no_sentinel`, `timeout`, `auth_failed`, `circuit_open`, `probe_failed`, `unsupported`, `config_error`
([wire_valkey.go](../../internal/app/wire_valkey.go), `valkeyReasonCode`); the free text with addresses
stays in the log and in the authenticated replica list. Checks are cached and debounced (`health.failure_threshold`, `health.success_threshold`); probes never
run a check. `database` and `schema` carry `health.WithGrace(health.database_outage_grace)` and wrap their error with
`markOutage` ([wire_postgres.go](../../internal/app/wire_postgres.go)): once the replica has been ready, a failure
that is `health.Transient` (a connectivity failure, `postgres.IsOutage`; a check that timed out counts) shows as
`degraded` and does not gate until it has lasted the grace (Q-OPS-31); a schema that is behind gates after the debounce.
The run of failures starts at its first failing answer and ends only when the check passes again; one answer that is
not transient ends the grace for the rest of the run (CR2-1), and the first readiness never gets a grace. `invalidation`
carries the same grace: `Changes.Check` reports a stale poll whose last error is an outage as transient, so it cannot gate
before `database` does (Q-OPS-34). With `system.bootstrap_anonymous_read`, `bridgeAccess` ([access.go](../../internal/app/access.go)) opens `settings:read` for the system info and the replica list only (Q-AUTHZ-71) to anonymous callers on the default endpoint only; every other decision is the authz engine's.
`Health.Serving()` is what `GET /api/v1/system/ping` answers from: no gating check other than `draining` fails, and a
draining replica had been ready (Q-OPS-30), so the drain delay answers every request; `Ready()` stays the readiness. A loop registers with the watchdog through `health.Loop(name, bound)`, calls `Beat()` per iteration and
`Stop()` when it ends on purpose.

Metrics beyond the observability package: [wire_gauges.go](../../internal/app/wire_gauges.go) exports
`trove_db_pool_connections`, `trove_db_pool_max_connections`, `trove_db_pool_empty_acquires_total` (read from the pools at
scrape time) and the background gauge `trove_jobs_queued{kind}`. The remaining families of R-OPS-007 (acquire duration
histogram, canceled acquires, inventory and backup gauges) are not exported yet.

## Shutdown

`App.Shutdown` (R-ARCH-024). `server.shutdown_timeout` is derived by the config loader when it is
unset (`0s`): `runtime.termination_grace_period - health.shutdown_drain_delay - 5 s` (R-HA-011), or
25 s when the grace period is unknown. The chart passes the grace period as
`TROVE_RUNTIME_TERMINATION_GRACE_PERIOD`, so a worker with 300 s and a 15 s drain gets 280 s. The source of the
value shows as `derived` in `trove admin config validate`. An explicit value that does not fit
(`drain + timeout + 5 s > grace`), or a grace period that leaves less than 1 s, is a start-up error
([finalize.go](../../internal/config/finalize.go)).

All phases work against one plan, computed once at the start of `Shutdown`
([shutdown.go](../../internal/app/shutdown.go), `planShutdown`). The times are absolute, so a phase that
runs long shortens the ones after it and never moves the end:

```text
start ──drain──▶ ReleaseAt ─▶ QuiesceAt ─▶ ServerBy ──commit grace──▶ CommitBy ──▶ CloseBy ──ops──▶ ExitBy < start + grace
        (web only)  CloseBy-15 s  ServerBy-5 s  start+drain+timeout    ≤ 3 s          flush+stop   0.5 s   ServerBy + 5 s - 0.5 s
```

1. `SetDraining`: `/readyz` turns 503 at once, `Drain` of every component (a worker stops claiming).
2. `web`/`all`: keep serving for `health.shutdown_drain_delay`; responses carry `Connection: close`.
   `worker`: no delay.
3. `Server.ShutdownBy` until `ServerBy`: the listener closes, in-flight requests finish; `Settle` of
   every component runs alongside.
4. `ReleaseAt` (`CloseBy - jobs.SettleDeadline`, 15 s, so `ServerBy - 11 s`; not before the drain ends, not after `QuiesceAt`): the components that
   quiesce first (the job worker) are cancelled, and their handlers' jobs get their release writes with the whole settle deadline
   ([Q-ARCH-329](../spec/architecture.md#q-arch-329--the-job-worker-is-cancelled-early-enough-that-the-release-writes-of-its-jobs-get-their-whole-settle-deadline)).
   `QuiesceAt` (`ServerBy - 5 s`, later when the worker is still returning): `Quiesce` of the others cancels what still runs.
5. At `ServerBy` the remaining requests are cut by closing their connections and counted in
   `trove_http_transfers_cut_total{direction}`, with one exception: a request that is **committing** runs
   on until `CommitBy`. A handler marks itself with `httpserver.BeginCommit(ctx)` right before the step
   that must not be interrupted; the arrival of the complete request body is the fallback signal. The cut waits for the answer, not only the handler: a committing request whose handler has returned keeps its connection in a flushing set until `net/http` has written the response (`ConnState` idle or closed), so a commit that went through is never reported to the client as a broken connection. The
   grace is `min(4 s, margin - close reserve - ops reserve - slack)`, i.e. 3 s with the default 5 s margin
   (the spec text says 4 s: the margin cannot carry 4 s plus the close budget).
6. `Flush` (reverse start order), then `Stop`: **signals first** (pending signals are published before the
   leases go), then job worker and leases, replica row, change log, Valkey, pools; until `CloseBy`. A component
   that ignores its context is abandoned 250 ms after `CloseBy` and reported (`close phase overran`).
   Then the ops listener and the tracer until `ExitBy`. Waiting for background goroutines is bounded by
   `ExitBy` too. `Registry.Drain` marks the row `draining` in step 1.

The budget constants (`defaultBudget`: margin 5 s from `config.ShutdownMargin`, slack 0.5 s, close reserve
1 s, ops reserve 0.5 s) are a struct on the `App` so tests scale them down
(`R-HA-011 a worker with the derived timeout quiesces its jobs ...`).

## Wiring steps

The wiring of steps 3 to 8 is a list of typed steps (`wiringSteps` in [steps.go](../../internal/app/steps.go)). A step
is a method of `App` with the shape `func (a *App) wireX(ctx context.Context, in1 T1, in2 T2) (XArea, error)`: its
inputs are the typed results of earlier steps, its result is the typed result of its area. `newStep(area, roles, method)`
reads the types off the method, so the list needs no repeated signature, and panics on any other shape; `roles` says which
roles run the step (`everyRole`, `withListener`, `onShell`). The list is the only place that says which role has which
area: `SubsystemsFor` asks it (`areasFor`).

- **Checked in `New`.** `planSteps` takes the list and the role's `Areas` and refuses the role, with an error that
  names the step, the missing type, the area that builds it and the role, when a step the role runs needs a type that no
  enabled step builds, that is built later in the list, or that two steps build. A web replica without the authz area is
  refused because the files area needs a decision point (`area files needs authzArea from area authz, which role web does
  not enable`). The shell profile runs `keys` and `shell` only.
- **Checked when it runs.** `requireInputs(area, given("database", db), ...)` returns `area: no database, no blob store`
  for every nil member, so a step that was handed an incomplete bundle stops the start-up by name instead of building a
  part that answers `503` later (`files: no authorizer`).
- **Checked before the listener opens.** `requireServing` names the areas a client listener serves (the authentication
  stage, the decision point, the repository reader, the files service, the login handler, the exposure rule service, the
  API stage, the audit store, the invalidation set, the proxy and virtual engines, the package services). A role that plans without one of them, because no other step needs
  its result, stops here with `client listener: no files service`.
- **Job handlers.** The job registry is built, and frozen, before the areas are wired. A handler therefore reads its service
  through a `late[T]` that the step sets once (`a.gc`, `a.auditStore`, `a.repoSvc`, `a.itemStore`, `a.tokenSvc`, `a.folder`,
  `a.reseal`): a job claimed before the step ran fails with a retryable error that names what is missing; one claimed
  after finds the value. A step that needs a result of another reads it as a typed input, never from a `late` (the virtual engine
  takes the item store from `filesArea`). The one request-time reader of a `late` is the Host that the files seam asks (`a.host`),
  because the Host is built from the item store, which is built with the seam (Q-ARCH-231).
- **Reading a result.** Everything that serves a request reads the results through `part[T](a.parts)`; the zero value of
  an area has nil members, which is how code reads an area the role does not build. A nil pointer must not become a
  non-nil interface: `decider()`, `Repositories()` and `Authorizer()` answer an untyped nil.

### How to add a wiring step

One new file and one line in the list.

1. Write `wire_foo.go`. In it: the area's name (`const AreaFoo Area = "foo"`), its result type (`type fooArea struct{ ... }`,
   by value), and the step `func (a *App) wireFoo(ctx context.Context, in infra, ...) (fooArea, error)`. Take `infra` and
   the areas you build on as inputs, start `Component`s with `a.startComponent`, register readiness checks, and call
   `requireInputs(AreaFoo, given("...", x), ...)` first so a nil part fails the start-up by name. A part that is
   only unreachable is retried, as steps 3 to 7 are; a part the wiring does not provide is an error. A binding the
   area owes another one (the data port of a format, the remote settings of the repository service) is made in the
   step and fails the step when it cannot be made; a missing or wrong binding is a start-up error, never a nil that a
   request finds.
2. Add `newStep(AreaFoo, everyRole, (*App).wireFoo),` to `wiringSteps` after the steps it needs. The role set says where
   it runs: `everyRole` (`api`, `packages`, `worker`, `web`, `all`), `withListener` (all but `worker`), `onFilling` (`packages`,
   `web`, `all`: the remote engine and the format host), `onManagement` (`api`, `web`, `all`: sessions, login, the CI exchange and
   the services of the management API). A part of another area that a role may leave out is an optional input:
   `newStep(..., withOptional(otherArea{}))` (Q-ARCH-300), and the step requires it with `a.sub.Has(AreaOther)`. Add the area to the
   column of the right roles in `roleTable()` of [roles_table_test.go](../../internal/app/roles_table_test.go) and to the table of
   R-ARCH-001. That is the only edit outside the new file.
3. A job kind of the area registers in the job registry before the step runs: keep the service in a `late[*foo.Service]`
   field of `App`, `set` it in the step, and build the handler with `handler(what, fn)`. Registering the kind is a line
   in `jobRegistry` ([wire_jobs.go](../../internal/app/wire_jobs.go)).
4. What the client listener needs of the area (a route, a `v1.Options` member) is read with `part[fooArea](a.parts)` in
   `startClient`; add the area to `requireServing` when a listener cannot serve without it.
5. Test it in the style of `TestR_ARCH_024_AMissingPrerequisiteIsAStartupError`: a role without the area it needs is
   refused by name, and the step refuses its nil inputs.

## Extending

- A new infrastructure client: a wiring step, see "How to add a wiring step"; register its checks with `AddReadiness` /
  `AddReported`.
- A background loop: `health.Loop`, `Beat` per iteration, a deadline on every call shorter than
  `health.watchdog_timeout`.
- Work that must finish or be released at shutdown: implement `Drainer`/`Settler`/`Quiescer`/`Flusher`.

## Integration test

[test/integration/server](../../test/integration/server) builds the binary and runs `migrate up`, `serve --role all`, `web` and
`worker` side by side against the integration stack, then SIGTERM under load. Run it with
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/server/...`.
[test/integration/tls](../../test/integration/tls) builds the binary as well and runs `serve --role all` with TLS on both
listeners, rotating the certificate under load (E0.15 at process level, [tls.md](tls.md#how-to-test)).
[test/integration/overload](../../test/integration/overload) seeds an installation through `testenv.NewStack` and runs the binary
(`Stack.Environ`) with `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` of 256 MiB: 1,000 downloads whose clients read nothing, the memory
guard's slots and `503`s, and the memory the runtime holds, sampled from `/metrics` ([Stage 4 and the memory guard](#stage-4-and-the-memory-guard)).
`TestBurstBesideEmptyCaches` offers 300 held downloads to a 1 GiB replica with the default caches (none is shed, where the
budgets would have left 77 slots), and `TestHeldDownloadCost` measures what a held download of 2, 5 and 8 MiB costs (heap in use,
stacks, resident set per download, logged with `-v`): the basis of the request bytes in Q-ARCH-328.
A memory limit is per process, which is why this suite cannot use the in-process harness.

## Test builds

`go build -tags drainfixture` starts the HTTP shell without PostgreSQL and Valkey and adds
`/test-fixture/hold` and `/test-fixture/stream`. [serve_process_test.go](../../cmd/trove/serve_process_test.go)
compiles it and sends a real `SIGTERM` while a request is in flight. No release build contains it.

## Known gaps

- The registration of each cache on `App.Caches()` is not checked at step 8. The trash purge and the upload-session expiry that
  R-STOR-025 puts at the start of a collection run are not part of the `gc` handler yet.
- A shell started without infrastructure (`SkipInfrastructure`, the drain fixture) lists only itself as replica.
- The UI locales are constants in [misc.go](../../internal/app/misc.go) until a `ui` configuration section exists.
