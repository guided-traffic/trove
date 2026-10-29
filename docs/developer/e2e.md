# End-to-end tests on the dev cluster

The suite proves the exit criteria of M0 that need Kubernetes: E0.3, E0.4 (chart part), E0.6, E0.7,
E0.11, E0.12 (dev-profile parts), E0.13, E0.14, E0.15 and E0.16 of the [roadmap](../coverage/roadmap.md). It runs
the real release image on the [dev profile](local-profile.md), over TLS on every connection, and injects the failures of
[R-HA-051 and R-HA-052](../spec/ha-and-helm.md). This is work package WP0.11 (and its TLS part, WP0.12).

## What has to be in your head first

- **The release image is what is tested.** The tests start nothing test-only inside the replicas. The
  one thing the release image needed for E0.14 is a diagnostic job kind (below).
- **M0 has no API that writes a change or enqueues a job.** A *driver* pod does what a `web` replica would
  do, with the same packages and the same configuration: it writes a change through `internal/cluster`
  and enqueues a job through `internal/jobs`. The workers of the release run the jobs.
- **A failed request is counted by the client.** k6 runs at 100 requests/s through the Ingress, and every
  response becomes a line the Go test parses. Nothing is inferred from server metrics.
- **The harness's own stop is not a failure.** The tests end k6 with SIGINT, which cancels the requests in flight at
  that moment (measured: one run in four at 100 requests/s, k6 error text `context canceled`; it caused the single
  `status 0` of a CI run of E0.11). `parseK6` sets aside exactly those: status 0, text `context canceled`, started in
  the last two seconds of the run, and reports their number in the log line. A connection error, a reset or any other
  status 0 stays a failure, wherever it happens. A request without a response is logged as `<code>:<text>` in the
  replica column.
- **A failing test here is a finding.** When a criterion fails because of the product, the test stays red
  and [Findings of the suite](#findings-of-the-suite) says what was measured. No threshold is moved to make it pass.

## Layout

```
deploy/dev/e2e.sh           what `make e2e` calls: builds the driver, starts its pod, runs go test
deploy/dev/drain.sh         E0.14 as a script (`make dev-drain`), and the drain alone for the test
deploy/dev/pg-kill.sh       force-delete the PostgreSQL primary, wait for the recovery
deploy/dev/pg-stop.sh       stop PostgreSQL for a time by fencing the CloudNativePG cluster
test/e2e/                   the Go tests, build tag `e2e`, package e2e
  env_test.go               kubectl, the API-server proxy to :8081, the driver and k6 helpers
  e03_cluster_test.go       E0.3, E0.7 (Pod Security, NetworkPolicy enforcement)
  e04_migration_test.go     E0.4 chart part
  e06_lease_test.go         E0.6, P-13 and P-14 with Valkey
  e12_valkey_test.go        E0.12
  e13_failover_test.go      E0.13
  e14_drain_test.go         E0.14
  e15_tls_test.go           E0.15: plaintext refused, the listener certificate renewed under load
  e16_endpoint_test.go      E0.16: the second endpoint `customers`
  e99_scatter_test.go       E0.11, last because it switches the profile
  monitor_test.go           /healthz and /readyz of every replica, once a second
  k6/load.js                the load: 100 requests/s on /system/ping and /system/replicas
  cmd/driver/               the driver (main.go: connections, probe and job commands; load.go: the load loop)
  Dockerfile                the driver image: a static binary on the release image's base
```

## Running it

```shell
make image VERSION=dev          # IMAGE defaults to guidedtraffic/trove:dev
make dev-up                  # TROVE_DEV_HTTP_PORT=28080 make dev-up when 8080 is taken
make e2e SET=push            # E0.3, E0.4, E0.6, E0.7, E0.12, E0.14, then E0.11 (what CI runs on every push to main)
make e2e SET=failover        # E0.4, E0.6 and the E0.13 failovers (CI runs it on every change too)
make e2e SET=full            # everything, in the order of the milestone
make e2e SET=drain           # E0.14 only
make e2e SET='^TestE06'      # any go test -run expression
```

Needs Docker, kind, kubectl, Helm (pinned, checked by `up.sh`), `jq` and Go. k6 is a container: `K6_IMAGE`
in [tools/versions.env](../../tools/versions.env), pinned by digest, run on the Docker network `kind`, so
no host install and no host port is involved. `e2e.sh` refuses a `K6_IMAGE` whose tag differs from
`K6_VERSION`.

`VERSION` defaults to `dev`, or to the number of a `v0.*` tag on `HEAD`. The binary refuses a version that is
neither `major.minor.patch` nor `dev`: with a commit hash the replicas would wait for the schema forever and never
become ready, and it now logs an ERROR at start saying so ([Findings of the suite](#findings-of-the-suite)).

Output: the machine-readable report is `.out/test-e2e.json` (`go test -json`, test names carry the
requirement IDs), the artifacts are in `.out/e2e/`: per k6 run the console log (one line per request:
`R <unix ms> <status> <replica> <ms> <path>`) and k6's own `summary.json`, the driver's JSON per run, the
pod watch and the events of the drain, the logs of the drained pods, and the result files `e13-*.json`,
`e14-drain.json`, `e06-*.json`.

| Variable | Default | Effect |
|---|---|---|
| `TROVE_DEV_SCHEME` | `https` | scheme of the edge for the host client and k6 |
| `TROVE_DEV_OPS_SCHEME` | `https` | scheme of the pods' `:8081` as the API server proxy reaches it |
| `TROVE_DEV_CA_FILE` | `.dev/pki/ca.crt` (written by `up.sh`) | PEM bundle that signs the edge's certificate: used by the Go client and mounted into the k6 container (`SSL_CERT_FILE`) |
| `TROVE_E2E_K6_TARGET` | `https://localhost:30443` | what k6 requests: the edge's HTTPS NodePort, by the name its certificate carries |
| `TROVE_E2E_K6_HOSTS_ADDR` | `<control-plane node address on the Docker network kind>:30443` | `host:port` that name resolves to inside the k6 container (so the certificate is verified for `localhost` while k6 reaches the node's address) |
| `TROVE_E2E_OUT` | `.out/e2e` | artifact directory |
| `TROVE_E2E_TIMEOUT` | `90m` | go test timeout |
| `TROVE_E2E_DRIVER_BIN` | unset | a prebuilt linux driver binary instead of `go build` |
| `TROVE_E2E_SCATTER_RETURN` | `1` | `0` leaves the cluster in the `scatter` profile after E0.11 |

The suite runs over TLS by default: the scheme, the CA and the probe scheme are parameters (the defaults above),
and nothing in the suite hard-codes `http`. What the move to TLS changed is listed in
[TLS in the suite](#tls-in-the-suite).

## The driver

[test/e2e/cmd/driver](../../test/e2e/cmd/driver) runs in the pod `trove-e2e-driver`, which `e2e.sh`
creates from the worker Deployment's pod template: the same ConfigMap (`/etc/trove/trove.yaml`), the same
Secrets, the same security contexts, no probes, no ports. It carries `app.kubernetes.io/name=trove` and
`trove/serve=true` because the Valkey NetworkPolicy admits exactly those pods, and a component label
that no Service, PodDisruptionBudget or spread constraint selects. It runs on the control-plane node, so a
drain of a worker node never evicts it. Its connections are named `trove:e2e-writer-…`, `…e2e-follower-…`
and `…e2e-probe-…` with role `web`: it claims no job and holds no lease.

| Command | What it does |
|---|---|
| `driver load -duration d` | each second: write a change to the area `cache` through peer A and enqueue a `system.probe` job (one step). Peer B follows the change log like a second `web` replica and measures when a cached value that the change names is evicted (**P-13**). The pick-up of the jobs (**P-14**) is the database's `started_at` minus `created_at`. The summary splits the samples by whether Valkey was up at the start and at the end |
| `driver stop` | ends a running `load` after its tick |
| `driver probe -steps n -interval-ms m` | enqueues one long `system.probe` job and prints its id |
| `driver job <id>` | prints the job row: state, attempt, checkpoint, result |
| `driver info` | prints `cluster.epoch_poll_interval`, `jobs.poll_interval` and `database.retry.max_elapsed` |

### The `system.probe` job kind

A job that persists its cursor every second has to exist in the release image: workers only run kinds
they know. [internal/jobs/probe.go](../../internal/jobs/probe.go) registers `system.probe` (default queue,
three attempts): it counts `steps` steps of `interval_ms`, saves a checkpoint and the progress after every
step, and records in the checkpoint which workers carried it (`hops`) and the step each later worker
resumed from (`resumes`). It touches no table but its own job row. Nothing enqueues it except a caller
with write access to the job table. The driver declares the same kind and args on its side of the wire,
without importing the handler.

## The tests

Each subtest name carries the requirement IDs (the coverage generator reads them from `go test -json`) and
the exit criterion.

| Criterion | Test | What it checks |
|---|---|---|
| E0.3 | `TestE03DevProfile` | two `web` and two `worker` pods Ready, every pod's `:8081/readyz` 200 (through the API-server proxy), `/system/replicas` lists two `web` and two `worker` and each pod under its role, `/system/ping` is `OK` through the Ingress; two PostgreSQL instances, two Valkey pods, three Sentinels. The 15-minute budget is measured by `make dev-up` itself (below) |
| E0.4 | `TestE04MigrationFailure` | `helm upgrade` with a migration credential whose password is wrong. The Job fails for that real reason (SASL authentication, `28P01`); `helm upgrade` fails naming the Job; ping answers 200 throughout; no pod is replaced and the web Deployment's generation is unchanged; the release is `failed`; the repairing upgrade succeeds and the checks of E0.3 pass again |
| E0.6 | `TestE06Lease` | the scheduler lease is read from `pg_locks` (class 1, object 1) joined to `pg_stat_activity` (`trove:<pod>:lease`), sampled every 200 ms from one psql session. One holder, a worker; no lease connection and no job lock of a `web` replica. Then two faults: force-delete, and SIGKILL of the process |
| E0.7 | `TestE07PodSecurity`, `TestE07NetworkPolicy` | the namespace enforces `restricted`, the pods run, a privileged pod is refused; from another namespace `:8080` of a web pod answers and `:8081` does not |
| E0.11 | `TestE99Scatter` | switches to `scatter`, runs the E0.3 checks and the Valkey connection check, 60 sequential requests reach three replicas 20 times each, k6 at 100 requests/s records only 200 spread evenly over the three |
| E0.12 | `TestE12Valkey` | `CLIENT LIST` on the primary; three new replicas started from the web template with a wrong `primary_name`, a wrong password and an unreachable Sentinel |
| E0.13 | `TestE13Failover` | `faultRun`: k6 + driver `load` + a monitor of `/healthz` and `/readyz` of every replica run while the fault is injected: `failover.sh`, `pg-kill.sh`, `pg-stop.sh 30s`, `valkey-failover.sh`, `valkey-kill.sh`, `valkey-outage.sh 60s` |
| E0.14 | `TestE14Drain` | see below |
| E0.15 | `TestE15TLS` | see below |
| E0.16 | `TestE16Endpoint` | see below |

### E0.6: the two faults

`kubectl delete pod --grace-period=0 --force` does not crash the process: the kubelet still delivers
SIGTERM, the replica shuts down cleanly and **deletes its own row** ([R-OPS-003](../spec/operations-and-observability.md):
"on a clean shutdown the replica deletes its own row"). The roadmap's wording, that the registry shows
the deleted pod `"lost": true`, can therefore only be observed after a process dies without cleanup. The
test runs both: the force-delete (the lease moves, the row is gone, the replacement is listed) and a
SIGKILL of the container's process through the container runtime of the node (`crictl inspect`, `kill -9`),
after which the kubelet restarts the container in the same pod. The registry then shows the dead
instance `lost` once its heartbeat is older than `system.heartbeat_stale_after` (30 s), and the new
instance beside it. The lease goes to whoever asks first: the other worker, or the restarted or
replacement pod. The test asserts that it moves, to a live worker, with never two holders in 200 ms
samples, and logs who took it.

### E0.13: pass and fail

The criterion counts the client's view. `only200` fails on any non-200, on fewer than 95 % of the
expected requests, and on `dropped_iterations` (k6 could not keep its rate). k6 waits 45 s for an answer, longer than
`database.retry.max_elapsed` (30 s), so a request the server holds ends as the server's answer and not as
a client timeout. For the PostgreSQL cases the test checks the status set (503 only), the time of the
last failure against `retry.max_elapsed` and that no failure follows the recovery. For Valkey it checks
that `/readyz` is 200 on every replica at every sample, that every change reached the follower and every
job ran, and that the p99 of the propagation and of the pick-up stay under `epoch_poll_interval + 1 s` and
`jobs.poll_interval + 1 s`, which are the bounds without Valkey.

The monitor reads the replicas through the API server (`/api/v1/namespaces/trove/pods/<pod>:8081/proxy/…`),
so the answer is the pod's own, not a Service's.

### E0.14

1. Pick the node ([drain.sh --pick](../../deploy/dev/drain.sh)): a worker node with a `web` and a `worker`
   pod, not the PostgreSQL primary's (its PodDisruptionBudget refuses the eviction). When the only such
   node is the primary's, `failover.sh` moves the primary first.
2. Start a watch on the trove pods (every transition with its arrival time), stream the logs of the pods
   that will be drained, start the driver `load` and k6.
3. Enqueue six `system.probe` jobs of 150 steps at 1 s, so that both workers run some.
4. `drain.sh <node>`: `kubectl drain --ignore-daemonsets --delete-emptydir-data`, wait for the rollouts,
   uncordon.
5. Every job must end `succeeded` with `attempt` 1 and 150 steps. A job that has more than one hop must
   have resumed at a step strictly inside the run. At least one job must have moved.
6. k6: only 200, no connection error, no dropped iteration. No container with exit code 137 or
   `OOMKilled` since the watch started. The kubelet's events for the drained pods hold no SIGKILL, no
   `FailedKillPod`. No trove pod Pending longer than 60 s.

The dev profile gives the worker a `terminationGracePeriodSeconds` of 60 (the chart default is 300), so a
worker whose job outlasts the shutdown budget releases it after about 45 s and the test does not wait 5
minutes. The edge is not part of the system under test: HAProxy Ingress runs two replicas on the control-plane node, which
no test drains. With one replica on a worker node, the drain reset the client connections of the evicted
edge pod (25 connection errors in the first measured run, with the previous controller).

### E0.15

`TestE15TLS` has two subtests.

1. **Plaintext is refused.** A pod of the namespace `default` sends a plaintext `GET` to `:8080` and `:8081` of a web
   pod and to the object store (`http://trove-s3.trove.svc:9000/`). The listeners answer `400` (net/http's "Client sent
   an HTTP request to an HTTPS server") or nothing; any `200` fails the test. (Valkey's plaintext port is 0, checked by
   E0.12. PostgreSQL's plaintext refusal is a CloudNativePG default that this suite does not probe.)
2. **The listener certificate is renewed under load.**
   - Per trove pod (two web, two worker) an *ephemeral container* with the pinned openssl image
     (`OPENSSL_IMAGE`, same user as trove, `--target=trove`) polls every 100 ms: the serial of
     `/var/run/secrets/trove/tls/tls.crt` as the kubelet mounts it into the trove container (read through
     `/proc/1/root`), and the serial `openssl s_client` receives from `127.0.0.1:8080` and `:8081` of that pod. It logs
     a line with the kernel uptime whenever one of the three changes, so *file arrival* and *served* are timed by the
     same clock inside one pod, which is what `reload_interval` bounds. (A worker serves `:8081` only.)
   - k6 runs at 100 requests/s through the edge; after 20 s the test triggers the renewal the way `cmctl renew`
     does: it adds the `Issuing` condition to the status of the `Certificate` `trove-tls` (a JSON patch on the status
     subresource; `privateKey.rotationPolicy: Always` makes it a new key too).
   - Asserted: k6 only `200` (and the rate held); on every pod the new serial is served within
     `server.tls.reload_interval + 1 s` (2 s + 1 s from the dev values) of the file changing, on both listeners;
     `trove_tls_reloads_total{kind="listener"}` rose and `trove_tls_reload_errors_total` did not; pod UID, restart
     count and `process_start_time_seconds` of every pod are unchanged.
   - The kubelet needs up to about a minute to refresh a Secret volume; that part is not bounded by trove and not
     asserted, only logged (`everywhere_after_s`).

### E0.16

`TestE16Endpoint`: the second endpoint `customers` (listener `:8443`, host `packages.trove.localhost`, wildcard
`*.packages.trove.localhost`, surface `packages`, own certificate and wildcard pair, own Ingress; see
[local-profile.md](local-profile.md#the-second-endpoint)).

- Through the edge (`TLS` to the published port, name resolution bypassed, verified against the dev CA for the real
  name): `packages.trove.localhost` and a host under the wildcard get the endpoint's edge certificate by SNI, and
  `localhost` the default one; `/ui/`, `/`, `/files/…`, `/api/v1/system/ping`, `/api/v1/system/replicas` and an unknown
  `/api/v1/` path answer, on both hosts, one identical `401` `credentials_required` (an endpoint admits no anonymous
  request, R-EP-010, Q-EP-10; the authentication stage runs before the routing, so the surface is not visible without a
  credential); the default endpoint's ping still answers `200`.
- On the endpoint's Service, from a pod in `default` (no trusted proxy): `Host: localhost` answers `421`, also with
  `X-Forwarded-Host: packages.trove.localhost`, and an IP literal as host answers `421`; the endpoint's own hosts
  answer the same `401` `credentials_required` with identical bodies on `/ui/` and a management API path; the TLS listener of the pod hands out the exact pair for the exact host and the
  wildcard pair for a host under the wildcard (serials compared with the two Secrets).
  The `421` is tested here and not through the Ingress because HAProxy Ingress routes by `Host`: a request with the
  default endpoint's host goes to the default backend.
- `trove_http_requests_total` carries both `endpoint` values and `trove_endpoint_listener_up{endpoint="customers"}` is 1.

## Measured on the dev cluster

Apple-silicon laptop (arm64), 20 CPUs, 16 GB for Docker, kind v0.32.0, edge on `https://localhost:28080`, the operators'
amd64 images under qemu. Release image built from the working tree of 2026-10-29 (`make image VERSION=dev`), TLS on
every connection.

| What | Result |
|---|---|
| `make dev-down`, then `make dev-up` (images pulled by the new nodes, the trove image built beforehand) | 156 s for both; `dev-up` itself 2 min 33 s (E0.3 budget: 15 min). Per task: HAProxy Ingress 57 s, PKI and secrets 64 s, Valkey operator 64 s, PostgreSQL, Valkey and SeaweedFS 2 min 22 s |
| `make e2e SET=push` | 561 s: E0.14 171 s, E0.15 107 s, E0.12 98 s, E0.11 87 s, E0.6 68 s, E0.7 11 s, E0.4 9 s, E0.16 5 s |
| `make e2e SET=failover` | 453 s: E0.13 364 s, E0.6 68 s, E0.4 9 s |
| E0.15, certificate renewal under load (10 K requests) | k6 only `200` (9694 requests, 0 dropped); cert-manager issued 0.1 s after the trigger; on the four pods the new certificate was served 0.0 to 1.8 s after the mounted file changed (bound 3 s); the kubelet needed up to 65 s to refresh the Secret volume of the last pod; `trove_tls_reloads_total` +2 on a web pod (client and ops listener), +1 on a worker; reload errors 0; no restart, process start time unchanged |
| E0.14, drain of `trove-worker2` | k6 16994 requests, only `200`, 0 dropped; drain 46.5 s; 2 jobs moved to the other worker, all `succeeded` with `attempt` 1 |
| E0.13, PostgreSQL switchover | 4890 requests, only `200`, 0 dropped, slowest request 9.5 s (held in the retry) |
| E0.13, PostgreSQL primary force-deleted | 6290 requests, only `200`, 0 dropped, slowest 17.9 s |
| E0.13, PostgreSQL stopped 30 s | 6991 requests, `200` 6145 and `503` 846 (503 only), 0 dropped; `/readyz` 503 on 10 to 15 one-second samples per pod; `/healthz` never; no restart |
| E0.13, Valkey failover / kill / outage 60 s | only `200` (3391 / 4294 / 10495 requests), slowest request 21 ms, `/readyz` 200 on every replica throughout; the faults took 2 s / 10 s / 72 s |
| E0.11 scatter | 60 sequential requests reached the three web replicas 20 times each; 100 requests/s through the edge, only `200` |

The k6 load pre-allocates 2000 virtual users. A request that the server holds while it retries the database occupies
one for up to `database.retry.max_elapsed`, and a user started on demand is slow (with TLS it includes a handshake),
so with the earlier 100 pre-allocated users k6 dropped 177 to 540 iterations in the PostgreSQL cases and
`only200` rightly failed on "the load fell behind its rate". The check is unchanged; the load generator now holds
its rate.

The measured numbers of each criterion are in the files under `.out/e2e/` after a run and in the test
log. The tests print them with `t.Logf`.

## Findings of the suite

What the suite found in the product, and what fixed each. Every one has a regression test that fails without the
fix. The numbers stay, because commits and test output refer to them. The suite was re-run on the kind
profile after the fixes (`SET=push` and `SET=failover`, above): the measurements below no longer occur.

1. **Fixed: `GET /api/v1/system/replicas` was not retried and answered 500 during a PostgreSQL failover**
   (`TestE13Failover`, switchover, primary kill and stop). `replicaSource.Replicas`
   ([internal/app/facts.go](../../internal/app/facts.go)) called `cluster.ListReplicas` with the raw pool
   `db.Main()`: it bypassed the retry and the mapping to `database_unavailable`/503 that `postgres.InTx` has.
   Measured: switchover, 40 of 4555 requests `500` (`FATAL: terminating connection due to administrator
   command (SQLSTATE 57P01)`), and `failed to connect … dial error: timeout` after 5 s; primary kill, 2 `500`;
   30 s stop, 575 `500` next to 2667 `503`. Fix: `DB.Read(ctx, func(q.DBTX) error)` in
   [internal/meta/postgres/tx.go](../../internal/meta/postgres/tx.go), with the retry, the bound and the error
   mapping of `InTx`; `replicaSource.Replicas`, `SystemFacts`, `ServerVersion` and `SchemaState` use it
   ([database.md](database.md)). Regression: `TestRead` in
   [test/integration/postgres/read_test.go](../../test/integration/postgres/read_test.go), subtests
   "Read repeats a read that a failover ended", "reads that run while every backend is terminated all succeed"
   (150 reads, backends terminated every 120 ms, none fails), "a read that keeps failing ends as
   database_unavailable within retry.max_elapsed" and "the system facts reads go through Read".
2. **Fixed: `GET /api/v1/system/ping` answered 503 as soon as a `web` replica drained**, intermittently
   (`TestE14Drain`): 1 of 16938 requests in one run, 3 of 16502 in the next, always from the drained replica,
   100 to 240 ms after the drain started. Ping followed readiness, readiness flips at SIGTERM (R-ARCH-024), and the
   edge keeps routing to the pod until its endpoint is removed; the drain delay exists to cover that window and
   only helps if the replica serves in it. Decision Q-OPS-30: ping answers from `Health.Serving()` (`200` while the
   replica is ready or draining after it was ready; `503` when a gating check other than `draining` fails, or it
   never was ready). Regression: `TestGracefulShutdown` in
   [internal/app/lifecycle_test.go](../../internal/app/lifecycle_test.go) (ping during the drain delay is 200),
   `TestServeProcess` in [cmd/trove/serve_process_test.go](../../cmd/trove/serve_process_test.go) (the process,
   SIGTERM, ping five times in the drain delay), `TestServing` in
   [internal/observability/health](../../internal/observability/health/health_test.go).
3. **Fixed: readiness could flip during a short PostgreSQL outage** (first switchover run: 75 `503`, about 4 s on
   both replicas; the second run: none). `health.check_interval` 5 s times `failure_threshold` 2 is shorter than a
   switchover (10 to 15 s), while queries retry for `database.retry.max_elapsed` (30 s). Decision Q-OPS-31: the
   `database` and `schema` checks have an outage grace, `health.database_outage_grace` (20 s, validated below
   `database.retry.max_elapsed`). Once a replica has been ready, a connectivity failure keeps the check `degraded` and
   `/readyz` at 200 for that long; then it fails. Regression: `TestOutageGrace` (fake clock) in
   [internal/observability/health](../../internal/observability/health/health_test.go), `TestMarkOutage` and
   `TestDatabaseOutageGrace` in
   [test/integration/server/outage_test.go](../../test/integration/server/outage_test.go) (a TCP proxy in front of
   PostgreSQL: down for 1.2 s, `/readyz` stays 200 and lists `database` as degraded; down past the 2.5 s grace of
   that test, 503 not earlier than 2 s, and 200 again when the proxy is back). The 30 s stop of E0.13 still turns
   `/readyz` 503, now 20 to 25 s into the outage instead of 10.
4. **Fixed: `make image` produced a binary that never became ready in a checkout without tags.** `VERSION` fell back
   to `git describe --tags --always --dirty`, a commit hash, which `parseVersion`
   ([internal/meta/postgres/version.go](../../internal/meta/postgres/version.go)) refuses, so the replicas logged
   `waiting for the schema … is not major.minor.patch` for ever. Now the Makefile's `VERSION` is the number of a
   `v0.*` tag on `HEAD` (the `v` stripped) and `dev` otherwise; an explicit `VERSION=` (CI) still wins. The binary
   also logs one ERROR at start when its own version cannot be compared with the schema (`TestCheckBinaryVersion`
   in [internal/app/version_check_test.go](../../internal/app/version_check_test.go)): the message names the version
   and the way out. CI and this page still pass `VERSION=dev`, which is now the default and no longer needed.

## TLS in the suite

The suite and the profile are TLS on every connection ([local-profile.md](local-profile.md)). What that meant for the
harness:

- `deploy/dev/e2e.sh` defaults to `TROVE_DEV_SCHEME=https`, `TROVE_DEV_OPS_SCHEME=https` and
  `TROVE_DEV_CA_FILE=.dev/pki/ca.crt`. k6 requests `https://localhost:30443` (the edge's HTTPS NodePort; the
  certificate names `localhost`) and resolves that name to the control-plane node's address on the Docker network
  (`TROVE_E2E_K6_HOSTS_ADDR`); the CA is mounted into the k6 container as `SSL_CERT_FILE`. The edge URL for the host
  stays `https://localhost:<port>`.
- The API-server proxy to `:8081` is addressed as `https:<pod>:8081`; it does not verify the pod's certificate. The
  checks that matter for the certificate (E0.15, E0.16) connect differently, as described above.
- `TestE07NetworkPolicy` completes a TLS handshake with `openssl s_client` from a pod in `default` and reads the status
  line; the `Host` is the default endpoint's (`localhost`), because the chart gives the default endpoint a host check
  once a further endpoint exists.
- `TestE12Valkey` uses the Valkey operator's names: port 16379, password from `$VALKEY_PASSWORD` in the pod, user
  `default`. The wrong-password case creates a Secret with a `password` key only, because the profile's chart values
  use the `default` user.
- The Valkey outage cannot scale the StatefulSets alone (the operator restores them), see
  [local-profile.md](local-profile.md#valkey-and-sentinel).

## Extension checklist

- **A new e2e check.** Put it in the file of its exit criterion, name the subtest `R-… E0.x …`, and fail
  with a message that says what was measured. Use `faultRun` for a fault under load.
- **A new fault.** A script in `deploy/dev/` that injects, waits for the recovery and prints how long
  it took; a `make dev-…` target; a case in `TestE13Failover`.
- **A later milestone's client load.** Extend `k6/load.js` (WP2.8); the Go side only reads the console lines.
- **A new job kind for a test.** Register it in the release image only when the test cannot be written
  without it, and say so here.
