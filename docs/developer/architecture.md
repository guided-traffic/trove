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
| [internal/httpserver](../../internal/httpserver) | Listeners, `Router` (ServeMux wrapper), the middleware `Chain`, client address, in-flight limit, route-table rules, graceful shutdown |
| [internal/observability/health](../../internal/observability/health) | Readiness checks with debounce, the liveness watchdog, the probe handlers |
| [internal/api/v1](../../internal/api/v1) | Management API handlers and the OpenAPI document, see [http-api.md](http-api.md) |

| File in `internal/app` | Content |
|---|---|
| [app.go](../../internal/app/app.go) | `Options`, `Run`, `RunWith`, `New`, `Start`, `bootstrap`, `Shutdown` |
| [component.go](../../internal/app/component.go) | `Component` and the optional shutdown phases `Drainer`, `Settler`, `Quiescer`, `Flusher` |
| [roles.go](../../internal/app/roles.go) | `SubsystemsFor(role)`: the table of R-ARCH-001 as data |
| [identity.go](../../internal/app/identity.go) | Instance name (`TROVE_POD_NAME`, `POD_NAME`, `HOSTNAME` in Kubernetes, else `hostname-pid`), random instance UID, node name |
| [wire_observability.go](../../internal/app/wire_observability.go), [wire_postgres.go](../../internal/app/wire_postgres.go), [wire_valkey.go](../../internal/app/wire_valkey.go) | One file per infrastructure client; `WIRING POINT` comments in `bootstrap` mark what later work packages add |
| [access.go](../../internal/app/access.go) | M0 stand-ins for authentication and authorization (see below) |
| [facts.go](../../internal/app/facts.go) | System information from the configuration; the replica list (this replica only until the registry exists) |
| [fixture_on.go](../../internal/app/fixture_on.go) / [fixture_off.go](../../internal/app/fixture_off.go) | The `drainfixture` test build |

## What runs where

`SubsystemsFor` encodes the table of R-ARCH-001. `web`: client and ops listener, main pool, Valkey,
change poll, registry. `worker`: no client listener, plus the lease pool and the job loops. `all` is both
and logs a warning. A `worker` has no socket on `:8080`; a test proves it
(`R-ARCH-001 a worker replica opens no client socket`).

## Request path (client listener)

```text
net/http server ── track (stall deadlines, active-request registry for the shutdown)
  └─ 1 recovery        panic -> 500 problem "internal"; logger into context; r.Pattern resolved; Connection: close while draining
     └─ 2 trace        observability Tracer.Middleware (traceparent, trace ID)   [injected]
        └─ 2 request ID  X-Request-Id = trace ID (minted if no trace stage); X-Trove-Replica for replica_header=always; OnRequest hook names the handler
           └─ 3 request log + metrics   observability.RequestLog               [injected]
              └─ 4 in-flight limit      503 server_busy + Retry-After: 1
                 └─ 5 client address    Resolver: peer, trusted proxies, base URL; Annotate hook
                    └─ 6 authentication principal or anonymous; invalid credential -> 401; X-Trove-Replica for authenticated callers
                       └─ 7 rate limits [injected, none in M0]
                          └─ 8 Router (ServeMux): API operations, /ui/, ... ; unmatched -> 404 problem, wrong method -> 405 + Allow
```

[chain.go](../../internal/httpserver/chain.go) builds this in one function; a test records the order
(`R-ARCH-005 stages run in the fixed order`). The route label of the log and the metrics is
`r.Pattern`, resolved by the outermost stage with `Router.Pattern`, because every stage that copies the
request would otherwise hide the mux's own pattern from the log stage.

The ops listener ([ops.go](../../internal/httpserver/ops.go)) serves `GET /healthz`, `/readyz`,
`/metrics` through the same recovery and request-ID stages and an optional request log. It has no
in-flight limit, so a probe is never refused because the request pool is busy.

Every route of a listener is registered through `httpserver.Router`. The generated API operations, the
UI and later the format dispatcher all use `Router.Handle`, so `Router.Patterns()` is the whole route
table. At start-up the composition root runs two checks over it: `Document.CheckRoutes` (API routes and
`api/openapi.yaml` agree in both directions) and `httpserver.CheckRouteTable` (nothing but the OCI format
below `/v2/`, no wildcard first segment, R-HA-041). A violation stops start-up and names the pattern.

### Client address and base URL

[clientaddr.go](../../internal/httpserver/clientaddr.go) is the only place that reads `Forwarded`,
`X-Forwarded-*` and `Host` (R-ARCH-006). The client is the TCP peer unless the peer lies in
`server.trusted_proxies`; then it is the right-most address of `Forwarded: for=` (or `X-Forwarded-For`)
that is not itself trusted. An entry that is not an address stops the walk and leaves the proxy as the
client. Forwarded scheme and host change the base URL only from a trusted peer and only when the origin is
one of `server.additional_base_urls`. Handlers read `httpserver.InfoFrom(ctx)`.

## Start-up

`App.Start` returns after step 2; `bootstrap` runs the rest in the background so a replica that cannot
reach PostgreSQL stays alive and unready.

| Step (R-ARCH-024) | Where |
|---|---|
| 1 configuration | `config.Load` in `serve.go` |
| 2 logging, metrics, tracing; ops listener; readiness 503, liveness 200 | `Start` |
| 3 PostgreSQL pools, schema gate, `system_state` facts (an unreachable database or a schema that is behind is retried with backoff, readiness stays 503; a configuration error, a failed preflight or a schema floor above this binary ends the process) | `postgresComponent` ([wire_postgres.go](../../internal/app/wire_postgres.go)) |
| 4 `audit_maintain_partitions()` | `WIRING POINT` in `bootstrap` (the audit package does not exist yet) |
| 5 key ring; `outbound.New` with its `Err()` as a first-readiness check | `bootstrap` |
| 6 object store: `s3.NewFromFactory`, `blob.NewGuard`, `blob.New`, `blob.AwaitStartupCheck` (a transient failure keeps the replica unready, a configuration error ends the process), the `objectStore` check (`first = !health.require_object_store`), the garbage collector on `worker` and `all` | `startBlob` ([wire_blob.go](../../internal/app/wire_blob.go)) |
| 7 Valkey in the background with `cluster.WithKeyspaceID`; signals; change log and cache registry (`invalidation` check); replica registry; on `worker` and `all` the lease manager and the job worker (worker first, then leases), on `web` only the `jobs.Queue` | `valkeyComponent`, `startCoordination` ([wire_coordination.go](../../internal/app/wire_coordination.go)) |
| 8 snapshots and caches of the domain packages | `WIRING POINT` (register on `App.Caches()`) |
| 9 client listener (`web`, `all`) | `startClient` |
| 10 `startup` check passes | `bootstrap` sets the flag and refreshes the checks |

Before serving, `freezeRegistries` freezes the sealed columns, the revision columns and the table registry; the job
registry ([wire_jobs.go](../../internal/app/wire_jobs.go)) is frozen where it is built: the kinds other packages put in
`jobs.Default`, the coordination kinds, and `gc` / `gc-full` with their tasks (schedules from `maintenance.tasks`,
fallback `*/30 * * * *` and `0 2 * * 0`, dedup key `gc`). It is a registry per replica, not `jobs.Default` itself, so
several replicas in one test process do not collide.

Readiness: `startup`, `database`, `schema`, `invalidation`, `outbound` gate; `objectStore` gates the first readiness only
unless `health.require_object_store`; `valkey` is reported and holds back the first readiness only on a configuration
error (Q-HA-13). Checks are cached and debounced (`health.failure_threshold`, `health.success_threshold`); probes never
run a check. A loop registers with the watchdog through `health.Loop(name, bound)`, calls `Beat()` per iteration and
`Stop()` when it ends on purpose.

Metrics beyond the observability package: [wire_gauges.go](../../internal/app/wire_gauges.go) exports
`trove_db_pool_connections`, `trove_db_pool_max_connections`, `trove_db_pool_empty_acquires_total` (read from the pools at
scrape time) and the background gauge `trove_jobs_queued{kind}`. The remaining families of R-OPS-007 (acquire duration
histogram, canceled acquires, inventory and backup gauges) are not exported yet.

## Shutdown

`App.Shutdown` (R-ARCH-024):

1. `SetDraining`: `/readyz` turns 503 at once, `Drain` of every component (a worker stops claiming).
2. `web`/`all`: keep serving for `health.shutdown_drain_delay`; responses carry `Connection: close`.
   `worker`: no delay.
3. `Server.Shutdown` with `server.shutdown_timeout`: the listener closes, in-flight requests finish;
   `Settle` of every component runs alongside.
4. `shutdown_timeout - 5 s`: `Quiesce` cancels what still runs.
5. At the deadline the remaining requests are cut by closing their connections and counted in
   `trove_http_transfers_cut_total{direction}`. A request whose body arrived completely runs on for at
   most `httpserver.DefaultCommitGrace` (4 s).
6. `Flush` then `Stop` in reverse start order (3 s budget): job worker, then leases (in one component), replica row,
   change log, signals, Valkey, pools; then the ops listener, then the tracer. `Registry.Drain` marks the row `draining` in step 1.

## Extending

- A new infrastructure client: add `wire_<name>.go` with a `Component`, call `startComponent` at its
  step in `bootstrap`, register its checks with `AddReadiness` / `AddReported`.
- A background loop: `health.Loop`, `Beat` per iteration, a deadline on every call shorter than
  `health.watchdog_timeout`.
- Work that must finish or be released at shutdown: implement `Drainer`/`Settler`/`Quiescer`/`Flusher`.

## Integration test

[test/integration/server](../../test/integration/server) builds the binary and runs `migrate up`, `serve --role all`, `web` and
`worker` side by side against the integration stack, then SIGTERM under load. Run it with
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/server/...`.

## Test builds

`go build -tags drainfixture` starts the HTTP shell without PostgreSQL and Valkey and adds
`/test-fixture/hold` and `/test-fixture/stream`. [serve_process_test.go](../../cmd/trove/serve_process_test.go)
compiles it and sends a real `SIGTERM` while a request is in flight. No release build contains it.

## Known gaps

- Authentication and authorization are stand-ins ([access.go](../../internal/app/access.go)): any
  presented credential is a 401, and `system.read` is held by nobody unless
  `system.bootstrap_anonymous_read: true`.
- The audit partitions (step 4), the key-ring canary and the cache snapshots (step 8) are `WIRING POINT`s in
  `bootstrap`; their packages do not exist yet. The trash purge and the upload-session expiry that R-STOR-025 puts at the
  start of a collection run are not part of the `gc` handler yet.
- A shell started without infrastructure (`SkipInfrastructure`, the drain fixture) lists only itself as replica.
- The UI locales are constants in [misc.go](../../internal/app/misc.go) until a `ui` configuration section exists.
