# End-to-end tests on the dev cluster

The suite proves the exit criteria of M0 that need Kubernetes: E0.3, E0.4 (chart part), E0.6, E0.7,
E0.11, E0.12 (dev-profile parts), E0.13 and E0.14 of the [roadmap](../coverage/roadmap.md). It runs
the real release image on the [dev profile](local-profile.md) and injects the failures of
[R-HA-051 and R-HA-052](../spec/ha-and-helm.md). This is work package WP0.11.

## What has to be in your head first

- **The release image is what is tested.** The tests start nothing test-only inside the replicas. The
  one thing the release image needed for E0.14 is a diagnostic job kind (below).
- **M0 has no API that writes a change or enqueues a job.** A *driver* pod does what a `web` replica would
  do, with the same packages and the same configuration: it writes a change through `internal/cluster`
  and enqueues a job through `internal/jobs`. The workers of the release run the jobs.
- **A failed request is counted by the client.** k6 runs at 100 requests/s through the Ingress, and every
  response becomes a line the Go test parses. Nothing is inferred from server metrics.
- **A failing test here is a finding.** When a criterion fails because of the product, the test stays red
  and [Open findings](#open-findings) says what was measured. No threshold is moved to make it pass.

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
  e99_scatter_test.go       E0.11, last because it switches the profile
  monitor_test.go           /healthz and /readyz of every replica, once a second
  k6/load.js                the load: 100 requests/s on /system/ping and /system/replicas
  cmd/driver/               the driver (main.go: connections, probe and job commands; load.go: the load loop)
  Dockerfile                the driver image: a static binary on the release image's base
```

## Running it

```shell
make image IMAGE=ghcr.io/guided-traffic/trove:dev VERSION=dev
make dev-up                  # TROVE_DEV_HTTP_PORT=28080 make dev-up when 8080 is taken
make e2e SET=push            # E0.3, E0.4, E0.6, E0.7, E0.12, E0.14, then E0.11 (what CI runs on every push to main)
make e2e SET=failover        # E0.4, E0.6 and the E0.13 failovers (CI runs it nightly)
make e2e SET=full            # everything, in the order of the milestone
make e2e SET=drain           # E0.14 only
make e2e SET='^TestE06'      # any go test -run expression
```

Needs Docker, kind, kubectl, Helm (pinned, checked by `up.sh`), `jq` and Go. k6 is a container: `K6_IMAGE`
in [tools/versions.env](../../tools/versions.env), pinned by digest, run on the Docker network `kind`, so
no host install and no host port is involved. `e2e.sh` refuses a `K6_IMAGE` whose tag differs from
`K6_VERSION`.

`VERSION=dev` matters. `make image` takes the version from `git describe`, which in a checkout without
tags is a commit hash, and the binary refuses a version that is neither `major.minor.patch` nor `dev`:
the replicas wait for the schema forever and never become ready ([Open findings](#open-findings)).

Output: the machine-readable report is `.out/test-e2e.json` (`go test -json`, test names carry the
requirement IDs), the artifacts are in `.out/e2e/`: per k6 run the console log (one line per request:
`R <unix ms> <status> <replica> <ms> <path>`) and k6's own `summary.json`, the driver's JSON per run, the
pod watch and the events of the drain, the logs of the drained pods, and the result files `e13-*.json`,
`e14-drain.json`, `e06-*.json`.

| Variable | Default | Effect |
|---|---|---|
| `TROVE_DEV_SCHEME` | `http` | scheme of the edge for the host client and k6 |
| `TROVE_DEV_OPS_SCHEME` | the edge's scheme | scheme of the pods' `:8081` as the API server proxy reaches it |
| `TROVE_DEV_CA_FILE` | unset | PEM bundle that signs the edge's certificate: used by the Go client and mounted into the k6 container (`SSL_CERT_FILE`) |
| `TROVE_E2E_K6_TARGET` | `<scheme>://trove-control-plane:30080` | what k6 requests; the NodePort of the edge on the Docker network |
| `TROVE_E2E_K6_HOSTS_ADDR` | unset | `host:port` the edge's host name resolves to inside the k6 container (a TLS edge whose certificate names the host) |
| `TROVE_E2E_OUT` | `.out/e2e` | artifact directory |
| `TROVE_E2E_TIMEOUT` | `90m` | go test timeout |
| `TROVE_E2E_DRIVER_BIN` | unset | a prebuilt linux driver binary instead of `go build` |
| `TROVE_E2E_SCATTER_RETURN` | `1` | `0` leaves the cluster in the `scatter` profile after E0.11 |

The scheme, the CA and the probe scheme are parameters so that the dev profile can move to TLS without a
rewrite of the suite; nothing in the suite hard-codes `http`. What a TLS switch has to change is listed in
[What a TLS switch changes](#what-a-tls-switch-changes).

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
minutes. The edge is not part of the system under test: Traefik runs two replicas on the control-plane node, which
no test drains. With one replica on a worker node, the drain reset the client connections of the evicted
Traefik pod (25 connection errors in the first measured run).

## Measured on the dev cluster

Apple-silicon laptop, 20 CPUs, 17 GB for Docker, kind v0.32.0, edge on `localhost:28080`. Release image
built from the working tree of 2026-10-29 plus the `system.probe` kind.

| What | Result |
|---|---|
| `make dev-down`, then `make dev-up` (images pulled by the new nodes, the trove image built beforehand) | 141 s (E0.3 budget: 15 min) |
| `make e2e SET=full` | 868 s (14.5 min): E0.13 352 s, E0.14 187 s, E0.11 141 s including two profile switches, E0.12 98 s, E0.6 68 s, E0.4 8 s |
| `SET=push` / `SET=failover` (sum of the same tests) | about 9 min / about 7 min |

The measured numbers of each criterion are in the files under `.out/e2e/` after a run and in the test
log. The tests print them with `t.Logf`.

## Open findings

What the suite found in the product. Each is reproducible with the command given; none is hidden by the
tests.

1. **`GET /api/v1/system/replicas` is not retried and answers 500 during a PostgreSQL failover**
   (`TestE13Failover`, switchover, primary kill and stop). `replicaSource.Replicas`
   ([internal/app/facts.go](../../internal/app/facts.go)) calls `cluster.ListReplicas` with the raw pool
   `db.Main()`: it bypasses the retry and the mapping to `database_unavailable`/503 that `postgres.Tx` has.
   Measured: switchover, 40 of 4555 requests `500` (`FATAL: terminating connection due to administrator
   command (SQLSTATE 57P01)`, which `classify` treats as retryable elsewhere), and `failed to connect …
   dial error: timeout` after 5 s; primary kill, 2 `500`; 30 s stop, 575 `500` next to 2667 `503`. The ping
   path never touches PostgreSQL and answers only 200/503.
   Fix: a `DB.Read(ctx, func(q.DBTX) error)` on the retrier of `internal/meta/postgres` that every handler
   read of the main pool uses, with the same error mapping as `Tx`.
2. **`GET /api/v1/system/ping` answers 503 as soon as a `web` replica drains**, intermittently
   (`TestE14Drain`): with the edge off the drained node, 1 of 16938 requests in one run, 3 of 16502 in the
   next, 0 of 16401 in the last, always from the drained replica, 100 to 240 ms after the drain started. `ping` answers from
   readiness ([R-OPS-002](../spec/operations-and-observability.md)), readiness flips at SIGTERM (R-ARCH-024),
   and the edge keeps routing to the pod until its endpoint is removed. `shutdown_drain_delay` exists to
   cover exactly this window, but it only helps if the replica keeps *serving* in it. Everything else
   survived the drain: no connection error, all jobs resumed. Fix: `ping` answers 200 while the replica
   is only draining (readiness false for no other reason). This changes R-OPS-002's "503 when not ready"
   for the draining case, so it is a spec decision.
3. **Readiness can flip during a short PostgreSQL outage** (first switchover run: 75 `503`, about 4 s on both
   replicas; the second run: none). `health.check_interval` 5 s times `failure_threshold` 2 is shorter
   than a switchover (10 to 15 s), while queries retry for `database.retry.max_elapsed` (30 s). A
   readiness that tolerates an outage shorter than the retry budget would keep the replicas in the
   Service while their queries wait; for the 30 s stop of E0.13 `/readyz` is meant to turn 503, which
   holds either way.
4. **`make image` produces a binary that never becomes ready in a checkout without tags.** `VERSION` falls
   back to `git describe --tags --always --dirty`, a commit hash; `parseVersion`
   ([internal/meta/postgres/version.go](../../internal/meta/postgres/version.go)) accepts only
   `major.minor.patch` or `dev`, and the replicas log `waiting for the schema … is not major.minor.patch`
   at every retry, for ever. Fix: the Makefile default `VERSION` is `dev` when `git describe` is not a version.
   CI and this page pass `VERSION=dev` until then.

## What a TLS switch changes

The suite takes the scheme, the CA and the probe scheme from the environment ([Running it](#running-it)).
When trove's listeners and the edge's backend connection move to TLS:

- `deploy/dev/e2e.sh` runs with `TROVE_DEV_SCHEME=https`, `TROVE_DEV_CA_FILE=<dev CA>` and, when the
  certificate names `localhost` while k6 reaches the node by name, `TROVE_E2E_K6_HOSTS_ADDR` and
  `TROVE_E2E_K6_TARGET=https://localhost:<port>`. The edge URL for the host stays `localhost:<port>`.
- `TROVE_DEV_OPS_SCHEME=https` makes the API-server proxy address `https:<pod>:8081`. That proxy does
  not verify the pod's certificate; a test that must check the certificate has to connect differently.
- `test/e2e/e03_cluster_test.go` (`TestE07NetworkPolicy`) uses `wget` over plain HTTP against the pod IP.
  It needs `https://` and the CA, or `--no-check-certificate`, and a `wget` that does TLS (the Valkey
  image's busybox does).
- `test/e2e/e12_valkey_test.go` clones the web pod; the clones inherit whatever TLS volumes the web
  Deployment has.
- `deploy/dev/up.sh` / `values-dev.yaml`: the Ingress backend scheme, `config.server.base_url` and
  `probeCIDRs` stay as they are.
- `pg-stop.sh`, `pg-kill.sh` and the Valkey scripts do not depend on the scheme.

## Extension checklist

- **A new e2e check.** Put it in the file of its exit criterion, name the subtest `R-… E0.x …`, and fail
  with a message that says what was measured. Use `faultRun` for a fault under load.
- **A new fault.** A script in `deploy/dev/` that injects, waits for the recovery and prints how long
  it took; a `make dev-…` target; a case in `TestE13Failover`.
- **A later milestone's client load.** Extend `k6/load.js` (WP2.8); the Go side only reads the console lines.
- **A new job kind for a test.** Register it in the release image only when the test cannot be written
  without it, and say so here.
