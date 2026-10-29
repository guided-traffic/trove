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
| [internal/httpserver](../../internal/httpserver) | Listeners and their TLS (`ListenerTLS`), `Router` (ServeMux wrapper), the middleware `Chain`, client address, in-flight limit, route-table rules, graceful shutdown, and the endpoint model (`Endpoint`, host check, surface), see [endpoints.md](endpoints.md) |
| [internal/tlsfiles](../../internal/tlsfiles) | Reloadable TLS material of every listener and client (leaf package), see [tls.md](tls.md) |
| [internal/observability/health](../../internal/observability/health) | Readiness checks with debounce, the liveness watchdog, the probe handlers |
| [internal/api/v1](../../internal/api/v1) | Management API handlers and the OpenAPI document, see [http-api.md](http-api.md) |

| File in `internal/app` | Content |
|---|---|
| [app.go](../../internal/app/app.go) | `Options`, `Run`, `RunWith`, `New`, `Start`, `bootstrap`, `Shutdown` |
| [shutdown.go](../../internal/app/shutdown.go) | `shutdownBudget` and `planShutdown`: the absolute times of every shutdown phase |
| [warnings.go](../../internal/app/warnings.go) | The standing configuration warnings (unset `server.base_url`, `system.bootstrap_anonymous_read`): at start and every hour |
| [component.go](../../internal/app/component.go) | `Component` and the optional shutdown phases `Drainer`, `Settler`, `Quiescer`, `Flusher` |
| [roles.go](../../internal/app/roles.go) | `SubsystemsFor(role)`: the table of R-ARCH-001 as data |
| [identity.go](../../internal/app/identity.go) | Instance name (`TROVE_POD_NAME`, `POD_NAME`, `HOSTNAME` in Kubernetes, else `hostname-pid`), random instance UID, node name |
| [wire_observability.go](../../internal/app/wire_observability.go), [wire_postgres.go](../../internal/app/wire_postgres.go), [wire_valkey.go](../../internal/app/wire_valkey.go) | One file per infrastructure client; `WIRING POINT` comments in `bootstrap` mark what later work packages add |
| [wire_tls.go](../../internal/app/wire_tls.go) | `startTLS`: one `ListenerTLS` per listener of the role (every endpoint listener included), the `tls` first-readiness check |
| [wire_endpoints.go](../../internal/app/wire_endpoints.go) | The endpoints this process serves, their TLS, chain and listener; see [endpoints.md](endpoints.md) |
| [access.go](../../internal/app/access.go) | M0 stand-ins for authentication and authorization (see below) |
| [facts.go](../../internal/app/facts.go) | System information from the configuration; the replica list (this replica only until the registry exists) |
| [fixture_on.go](../../internal/app/fixture_on.go) / [fixture_off.go](../../internal/app/fixture_off.go) | The `drainfixture` test build |

## What runs where

`SubsystemsFor` encodes the table of R-ARCH-001. `web`: client and ops listener, main pool, Valkey,
change poll, registry. `worker`: no client listener, plus the lease pool and the job loops. `all` is both
and logs a warning. A `worker` has no socket on `:8080`; a test proves it
(`R-ARCH-001 a worker replica opens no client socket`).

A client listener exists per **endpoint** (R-EP-002): the default endpoint on `server.listen` and every entry of
`server.endpoints` that belongs to the process's pool (`TROVE_POOL_NAME`). `web` opens the endpoints of its pool, a
process in a pool no endpoint names opens those without a pool, `all` opens every one, a `worker` none. Each
endpoint has its own `http.Server`, router (built from its surface), chain instance and TLS block; the sections
below describe the chain once, [endpoints.md](endpoints.md) says what differs per endpoint.

## Request path (client listener)

```text
net/http server ── track (stall deadlines, active-request registry for the shutdown)
  └─ 1 recovery        panic -> 500 problem "internal"; logger into context; r.Pattern resolved; Connection: close while draining
     └─ 2 trace        observability Tracer.Middleware (traceparent, trace ID)   [injected]
        └─ 2 request ID  X-Request-Id = trace ID (minted if no trace stage); X-Trove-Replica for replica_header=always; OnRequest hook names the handler
           └─ 3 request log + metrics   observability.RequestLog               [injected]
              └─ 4 in-flight limit      503 server_busy + Retry-After: 1
                 └─ 5 client address    Resolver: peer, trusted proxies, base URL; Annotate hook; per endpoint: effective host, host check (421), binding
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
| 3 PostgreSQL pools, schema gate, `system_state` facts (an unreachable database or a schema that is behind is retried with backoff, readiness stays 503; a configuration error, a failed preflight or a schema floor above this binary ends the process; the component closes the pools it opened on every error or cancel return, because `startComponent` records a component only after `Start` succeeded) | `postgresComponent` ([wire_postgres.go](../../internal/app/wire_postgres.go)) |
| 4 audit: retention check, `audit_maintain_partitions()` until the current and next month exist, the recorder | `App.startAudit` ([wire_audit.go](../../internal/app/wire_audit.go)) |
| 5 key ring; `outbound.New` with its `Err()` as a first-readiness check | `bootstrap` |
| 6 object store: wait while `outbound.Err()` reports a problem (a CA file of `proxy.outbound.tls.ca_files` that loads later clears it without a restart), `s3.NewFromFactory` (a `storage.s3.ca_file` that does not load is a configuration error), `blob.NewGuard`, `blob.New`, `blob.AwaitStartupCheck` (a transient failure keeps the replica unready, a configuration error ends the process), the `objectStore` check (`first = !health.require_object_store`), the garbage collector on `worker` and `all` | `startBlob` ([wire_blob.go](../../internal/app/wire_blob.go)) |
| 7 Valkey in the background with `cluster.WithKeyspaceID`; signals; change log and cache registry (`invalidation` check); replica registry; on `worker` and `all` the lease manager and the job worker (worker first, then leases), on `web` only the `jobs.Queue` | `valkeyComponent`, `startCoordination` ([wire_coordination.go](../../internal/app/wire_coordination.go)) |
| 8 snapshots and caches of the domain packages | `WIRING POINT` (register on `App.Caches()`) |
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
before `database` does (Q-OPS-34). With `system.bootstrap_anonymous_read`, `bridgeAccess` ([access.go](../../internal/app/access.go)) opens the capability `settings:read` to anonymous callers on the default endpoint only; every other decision is the authz engine's.
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
start ──drain──▶ QuiesceAt ─────▶ ServerBy ──commit grace──▶ CommitBy ──▶ CloseBy ──ops──▶ ExitBy < start + grace
        (web only)  ServerBy-5 s   start+drain+timeout    ≤ 3 s          flush+stop   0.5 s   ServerBy + 5 s - 0.5 s
```

1. `SetDraining`: `/readyz` turns 503 at once, `Drain` of every component (a worker stops claiming).
2. `web`/`all`: keep serving for `health.shutdown_drain_delay`; responses carry `Connection: close`.
   `worker`: no delay.
3. `Server.ShutdownBy` until `ServerBy`: the listener closes, in-flight requests finish; `Settle` of
   every component runs alongside.
4. `QuiesceAt` (`ServerBy - 5 s`): `Quiesce` cancels what still runs.
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
[test/integration/tls](../../test/integration/tls) builds the binary as well and runs `serve --role all` with TLS on both
listeners, rotating the certificate under load (E0.15 at process level, [tls.md](tls.md#how-to-test)).

## Test builds

`go build -tags drainfixture` starts the HTTP shell without PostgreSQL and Valkey and adds
`/test-fixture/hold` and `/test-fixture/stream`. [serve_process_test.go](../../cmd/trove/serve_process_test.go)
compiles it and sends a real `SIGTERM` while a request is in flight. No release build contains it.

## Known gaps

- The key-ring canary and the cache snapshots (step 8) are `WIRING POINT`s in
  `bootstrap`; their packages do not exist yet. The trash purge and the upload-session expiry that R-STOR-025 puts at the
  start of a collection run are not part of the `gc` handler yet.
- A shell started without infrastructure (`SkipInfrastructure`, the drain fixture) lists only itself as replica.
- The UI locales are constants in [misc.go](../../internal/app/misc.go) until a `ui` configuration section exists.
