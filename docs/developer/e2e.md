# End-to-end tests on the dev cluster

The suite proves the exit criteria of M0 that need Kubernetes: E0.3, E0.4 (chart part), E0.6, E0.7,
E0.11, E0.12 (dev-profile parts), E0.13, E0.14, E0.15 and E0.16 of the [roadmap](../coverage/roadmap.md), and the
end-to-end criteria of the access core of M1: E1.1, E1.8, E1.11 (the API part and the browser journeys), E1.13 and E1.14. It runs
the real release image on the [dev profile](local-profile.md), over TLS on every connection, and injects the failures of
[R-HA-051 and R-HA-052](../spec/ha-and-helm.md). This is work package WP0.11 (and its TLS part, WP0.12); the M1 tests are
[described below](#m1-tests).

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
- **The M1 tests start from a fresh installation's own tools.** The administrator they seed with is made the way
  [identity.md](identity.md#bootstrap-commands) says (`trove admin service-account create --admin`, `trove admin token
  create` through `kubectl exec` into a worker pod); everything else is the management API. They run in the `scatter`
  profile (three `web` replicas, every request to another one) and `TestMain` returns the cluster to the default profile at
  the end.
- **A failing test here is a finding.** When a criterion fails because of the product, the test stays red
  and [Findings of the suite](#findings-of-the-suite) says what was measured. No threshold is moved to make it pass.

## Layout

```
deploy/dev/e2e.sh           what `make e2e` calls: builds the driver, starts its pod, runs go test
deploy/dev/drain.sh         E0.14 as a script (`make dev-drain`), and the drain alone for the test
deploy/dev/pg-kill.sh       force-delete the PostgreSQL primary, wait for the recovery
deploy/dev/pg-stop.sh       stop PostgreSQL for a time by fencing the CloudNativePG cluster
deploy/dev/valkey-stop.sh   stop Valkey and the Sentinels until valkey-start.sh (the M1 tests hold the outage themselves)
deploy/dev/valkey-cli.sh    valkey-cli against the current primary (FLUSHALL of E1.11, CLIENT PAUSE of E1.8)
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
  m1_env_test.go            the M1 harness: edge client with a cookie jar, bootstrap administrator, browser login
                            against the test IdP, port-forwards to single pods, the Valkey outage, the scatter profile
  e1_01_login_test.go       E1.1: sign-in with Keycloak, JIT user, groups, logout, break-glass
  e1_08_ratelimit_test.go   E1.8: R-AUTH-038 thresholds, R-API-016 classes, R-ARCH-034 cluster-wide counting
  e1_11_scatter_test.go     E1.11: list walks, Idempotency-Key, read after write, R-AUTH-042 (TestE1_11Scatter), and
                            TestE1_11Journeys, which runs the Playwright journeys of R-UI-047 (web/e2e, below)
  e1_13_transfer_test.go    E1.13: curl -T during a rollout, downloads during a rollout and a pod deletion, GC on a worker
  e1_14_exposure_test.go    E1.14: exposure rules on the endpoint `customers`, bound tokens, R-EP-015
  e99_scatter_test.go       E0.11, last because it switches the profile
  monitor_test.go           /healthz and /readyz of every replica, once a second
  k6/load.js                the load: 100 requests/s on /system/ping and /system/replicas
  k6/transfer.js            the transfer loop of E1.13: PUT, GET and SHA-256 of 256 KiB bodies on generic-local
  cmd/driver/               the driver (main.go: connections, probe and job commands; load.go: the load loop)
  Dockerfile                the driver image: a static binary on the release image's base
  npm_clients_test.go       the smoke test of the npm client pods (R-NPM-045), and npmClientExec, the helper the matrix builds on
  clients/                  the npm client matrix image (below): package.json + package-lock.json, bin/ wrappers, fwd/ forwarder,
                            Dockerfile, build.sh, check-pins.mjs
web/e2e/                    the Playwright journeys of R-UI-047 (TypeScript, in the web workspace's lockfile)
  playwright.config.ts      Chromium, one worker, no retries, the edge URL and the IdP mapping from the environment
  global-setup.ts           verifies the TLS chains against the dev CA, removes alice and bob (JIT users of an earlier run)
  support.ts                the `page`, `admin` and `replicas` fixtures, the CSP watch, `expectAccessible` (axe), the sign-ins
  tests/01-...07-*.spec.ts  the journeys, in the order they run (Browser journeys, below)
```

## Running it

```shell
make image VERSION=dev          # IMAGE defaults to guidedtraffic/trove:dev
make dev-up                  # TROVE_DEV_HTTP_PORT=28080 make dev-up when 8080 is taken
make e2e SET=push            # E0.3, E0.4, E0.6, E0.7, E0.12, E0.14, E0.15, E0.16, E1.1, E1.14, then E0.11 (CI, every change)
make e2e SET=failover        # E0.4, E0.6, the E0.13 failovers and the long M1 tests E1.8, E1.11, E1.13 (CI, every change)
make e2e SET=access          # the M1 tests alone: E1.1, E1.8, E1.11, E1.13, E1.14
make e2e SET=full            # everything, in the order of the milestone
make e2e SET=drain           # E0.14 only (with the transfer loop of E1.13)
make e2e SET='^TestE06'      # any go test -run expression
make e2e SET=clients         # build the npm clients image, start the client pods, run the smoke test (the pods stay up)
make e2e SET=clients-down    # remove the client pods
make npm-clients-image       # only build the image into the local Docker
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
| E0.12 | `TestE12Valkey` | `CLIENT LIST` on the primary; three new replicas started from the web template with a wrong `primary_name`, a wrong password and an unreachable Sentinel; each is deleted with its normal grace period and the test waits until the registry no longer lists it, because a web-role row left to go stale counts towards the `n` that every fallback rate-limit share divides by (R-ARCH-034) and refused the transfer loop of E0.14 in the test that follows |
| E0.13 | `TestE13Failover` | `faultRun`: k6 + driver `load` + a monitor of `/healthz` and `/readyz` of every replica run while the fault is injected: `failover.sh`, `pg-kill.sh`, `pg-stop.sh 30s`, `valkey-failover.sh`, `valkey-kill.sh`, `valkey-outage.sh 60s` |
| E0.14 | `TestE14Drain` | see below |
| E0.15 | `TestE15TLS` | see below |
| E0.16 | `TestE16Endpoint` | see below |
| E1.1 | `TestE1_01Login` | see [M1 tests](#m1-tests) |
| E1.8 | `TestE1_08RateLimits` | see [M1 tests](#m1-tests) |
| E1.11 | `TestE1_11Scatter`, `TestE1_11Journeys` | see [M1 tests](#m1-tests) |
| E1.13 | `TestE1_13Transfers`, and the loop in `TestE14Drain` | see [M1 tests](#m1-tests) |
| E1.14 | `TestE1_14Exposure` | see [M1 tests](#m1-tests) |

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

## M1 tests

The tests of the access core run in the `scatter` profile. `useScatter` switches to it once per process (`up.sh` with
`TROVE_PROFILE=scatter`, three ready `web` pods) and `TestMain` switches back at the end, as `TestE99Scatter` does.
Subtest names carry the requirement IDs and the criterion, as the M0 tests do.

**The harness** ([m1_env_test.go](../../test/e2e/m1_env_test.go)):

| Piece | What it does |
|---|---|
| `api` | an HTTP client of the edge that sends a new connection per request (so the scatter profile's round robin shows in `X-Trove-Replica`), with a Bearer token or a cookie jar and the CSRF header; `rt` routes names to the edge's published port, to a port-forward for the test IdP's Service name and, for the per-address tests, to the web pods in turn |
| `adminToken` | `trove admin service-account create e2e-admin --admin` and `trove admin token create` through `kubectl exec` into a worker pod: the bootstrap of a fresh installation. The account stays between runs, every run mints a token and revokes those of earlier runs |
| `browser.oidcLogin` | the authorization code flow with PKCE: GET `/auth/oidc/keycloak/start`, the redirects followed by hand, the IdP's login form (found by its form id) posted with the user's password from the Secret `trove-keycloak`, the callback, the redirect into the UI. The trace records every hop and the answers of the start and the callback, so a test sees on which replicas they ran. It is an HTTP client, not a browser ([Q-AUTH-79](../spec/identity-and-authentication.md#q-auth-79--how-the-end-to-end-tests-drive-the-browser-login-of-e11-and-disable-the-break-glass-account)) |
| `podForwards` | a `kubectl port-forward` to every ready web pod; a client that sends its requests to them in turn reaches the three replicas without the edge, from the address 127.0.0.1, a bucket of its own for the per-address limits of R-AUTH-038. Every test that exceeds such a limit uses it, so the edge address (the host, which every other test shares) keeps its budget. A forward is supervised: `kubectl port-forward` ends for good on the first connection through it that fails upstream (`lost connection to pod`) or when its stream to the kubelet breaks, (measured: a client that resets a connection through the forward, or a refused connection inside the pod, makes `kubectl` print `error: lost connection to pod` and exit; the listener is then gone for the next client), so the harness starts it again on the same local port, writes the end and its stderr to `.out/e2e/portforward-*.log`, and a client that finds the port closed in between (connection refused, nothing of its request sent) waits up to 30 s for the new process, then fails with that stderr |
| `withValkeyStopped` | `valkey-stop.sh`, the criterion, `valkey-start.sh` (also after a failure) |
| `walkRetry` | a list read page by page; a 429 is waited out, because a walk with `limit=1` is more requests than the read class admits in a burst |

**Pacing.** The limits are the product's defaults (`api.rate_limits` read 50/s burst 200 and write 10/s burst 50,
`auth.rate_limit` 300 failures and 30 flow starts per minute and address). The dev profile raises only the anonymous class,
which no M1 test uses: the read and write classes are tested with tokens, the anonymous class has its bound in
`test/integration/api`. A test that spends a budget mints its own tokens; the 50 browser logins of R-AUTH-042 are paced at
2.2 s so they stay under the flow-start limit of the host's address.

| Criterion | Test | What it checks |
|---|---|---|
| E1.1 | `TestE1_01Login` | alice signs in through the Ingress with the code flow and PKCE (the authorization request carries `code_challenge_method=S256`, `state`, `nonce` and the redirect URI of the edge; the realm enforces S256, so a completed exchange proves the verifier); created just in time (`origin: jit`, deleted before the test so the creation is observable); groups `team-web` and `platform-admins` from the claim `groups` through the rule `trove-(.+)`, bob only `team-web`. Logout: the answer names the IdP's end-session endpoint with `id_token_hint`, `client_id` and the post-logout URI; the browser follows it; the cookie of the ended session is refused on every replica within `auth.revocation_bound`; a second sign-in in the same browser needs no password before the logout (the control) and asks for it again after (R-AUTH-013: the IdP session ended). Break-glass: wrong password `401`, the generated password `204`, `isAdmin` via `break-glass`; `TROVE_AUTH_BREAK_GLASS_ENABLED=false` on the web Deployment (R-AUTH-014: disabled): nine attempts over the three replicas get `403 local_login_disabled`, the earlier session `401 session_expired`; enabled again, the login works |
| E1.8 | `TestE1_08RateLimits` | R-API-016: 400 reads with one token get `429` with `Retry-After`, `rate_limited` and the `RateLimit` fields of class `read` after the burst of 200, a second token is not slowed; the write class likewise at 50, and a token whose write budget is gone still reads. R-ARCH-034: 1000 requests with one token round robin over three replicas are admitted 200 times, not three bursts, and the same on one replica; with Valkey stopped the same bound holds (each replica's share, `trove_ratelimit_fallback{limiter="api_read"}` is 1 on all three), and sequential reads add no delay with the circuit open. The latency injection: `CLIENT PAUSE 4000 ALL` on the primary holds every check for `valkey.command_timeout`; exactly the 15 checks of `failure_threshold` 5 times three replicas are held, then thousands of fast requests. R-AUTH-038: 400 random well-formed tokens, Bearer and Basic alternating, are validated (401) 300 times and answer 429 after that, both kinds; a PAT used once before still works on all three replicas during the burst; 40 starts of the login flow get 429 from the 31st on; with Valkey stopped at most 300 validations and the 40 starts admit about 30 (each replica's share of 10), and `trove_ratelimit_fallback{limiter="auth.credential_failures"}` is 1 on each replica |
| E1.11 | `TestE1_11Scatter` | every list operation (repositories, users, groups, service accounts, tokens, policies, role bindings, audit events, exposure rules, jobs) walked with `limit=1` returns what one page of 500 returns (read before and after, no duplicate, served by at least two replicas); an `Idempotency-Key` replayed 15 times is `201` with `Idempotency-Replayed: true`, the stored body and `Location`, from all three replicas, the same key with another body is `422 idempotency_key_reused` (R-API-051); a write on one replica is read back at once on the others, 24 times, and a new repository takes uploads on all three replicas at once (R-API-052). R-AUTH-042, in three modes (Valkey up with 50 browser logins, Valkey stopped for its whole duration with 25 and every pod `degraded: valkey`, and `FLUSHALL` at the lockout, after the revocation and in the middle of 25 logins): break-glass lockout (five wrong passwords one after another over the three replicas, the correct one `429 break_glass_locked` with `Retry-After` on each, still locked after `FLUSHALL`, unlock through the API; five at once lock it as well), PAT revocation (the token worked on all three replicas, then 401 on all three within the 2 s of `auth.revocation_bound`, and after `FLUSHALL` too), re-authentication (the session's `authenticated_at` set back two hours: `401 reauthentication_required` on any replica, the silent round trip `purpose=reauth` needs no password and rotates the session, the old cookie is dead), logout, and the browser logins on varying replicas |
| E1.11 journeys | `TestE1_11Journeys` | runs `npx playwright test` in `web/e2e` against the scatter profile and fails on any failure of any journey, including an axe violation of impact serious or critical on a visited screen; one subtest per journey, named by its title. A missing `npm ci` or browser is a failure that names the command to run, not a skip. See [Browser journeys](#browser-journeys) |
| E1.13 | `TestE1_13Transfers` | R-HA-011: two loops of `curl -T` of 500 MB at 50 MB/s during `kubectl rollout restart deploy/trove-web`: none fails, uploads overlapped the rollout, and what arrived has the SHA-256 that was sent. R-ARCH-024, download 1: a 400 MiB file of `generic-local` is downloaded during a rollout restart and matches ([Q-ARCH-76](../spec/architecture.md#q-arch-76--the-pace-of-the-download-that-a-rollout-restart-must-not-break): at 6 MB/s, so that the serving pod is drained while it transfers, which the test asserts). Download 2: at 2 MB/s (200 s, longer than the grace period) with the serving pod deleted after 3 s: the bytes stop arriving about 110 s later (the shutdown timeout; the pod's log says `requests cut at the shutdown deadline`), curl ends with an error, `curl -C -` resumes with `206` from another replica and the SHA-256 matches. Download 1 was red in about half of the runs until [finding 5](#findings-of-the-suite) was fixed; the cut download must end for curl within 15 s of its last bytes. R-HA-002: `POST /api/v1/system/tasks/gc/run` through a web pod is executed by a worker (the job's `replica`, and `job.ran_by` resolved through the replica table), and no job was ever run by a web replica. E0.14: `TestE14Drain` runs `k6/transfer.js` next to the ping load (4 iterations/s: PUT, GET, SHA-256 compared) and fails on any transfer that is not 200/201, a wrong checksum or a dropped iteration |
| E1.14 | `TestE1_14Exposure` | on `customers` (through its own Ingress): without a rule nothing is exposed (`404`) and a write is `403 endpoint_read_only`, a rule with `packages` on a generic repository is `422 dimension_not_supported`; a rule `paths.include: ["releases/**"]` makes `releases/1.0/app.zip` downloadable with a service-account token bound to `customers` while `internal/x.bin` and several missing paths answer the same 404 (problem body without `instance` and `requestId`, same content type and cache control); the same token is `401 invalid_token` on the default endpoint; a PAT is `401 invalid_token` on `customers` with no query of the token table (`api_token` is locked `ACCESS EXCLUSIVE` for 14 s: 41 PATs are answered in milliseconds, while an unknown PAT on the default endpoint, the control, waits for the lock and ends `503` after the statement timeout; the statistics views are no evidence, an idle backend flushes its counters up to a minute late) and `trove_endpoint_credentials_refused_total{reason="token_type"}` +41; an unbound SAT is refused with `reason="token_binding"`; no credential and a valid session cookie are `401 credentials_required`; an administrator token bound to `customers` reads the exposed file, gets the 404 for the internal one, lists only `generic-local` among the repositories and no `internal` folder. The rule lifecycle (created, narrowed with a merge patch, deleted) is read on every replica through a port-forward to its endpoint listener within the bound of R-AUTHZ-028 (Valkey up and stopped), and each change is one audit event (`endpoint.exposure.created`, `.updated`, `.deleted`). R-EP-015: 400 reads with one token get `429` after the burst with `Retry-After` and the policy `endpoint:external`, a second bound token is not slowed, the same token is not held on the default endpoint; 1000 requests with one token over the three replicas are admitted at most burst plus decay, with Valkey stopped too, where `trove_ratelimit_fallback{limiter="endpoint_external"}` is 1 on every replica |

## Browser journeys

`TestE1_11Journeys` is the second half of E1.11: the Playwright journeys of [R-UI-047](../spec/api-and-ui.md) in
[web/e2e](../../web/e2e), run in Chromium against the chart-installed build in the `scatter` profile (three `web`
replicas, every request to another one). The unit and component tiers stay in `make web-test`
([web-ui.md](web-ui.md#test-tiers-r-ui-047)); this is the tier that sees the real layout, the real CSP, the real IdP page
and, with `@axe-core/playwright`, `color-contrast` and the document-level rules (Q-UI-11, Q-UI-66).

### What a journey is

| File | Journey | Requirements | What it does |
|---|---|---|---|
| `01-sign-in.spec.ts` | sign-in through the IdP, and the local form | R-UI-004, R-AUTH-009, R-AUTH-013, R-AUTH-022 | alice signs in at the test IdP (the dev profile sets `autoRedirect`), the session survives reloads on other replicas, sign-out ends the IdP session too (the next protected route asks for the password); the break-glass administrator signs in with `?local=1`, axe on the local form |
| `02-repository-and-files.spec.ts` | wizard, upload, browse, download | R-UI-010 to R-UI-012 | the administrator creates a repository with the five wizard steps, uploads `hello.txt` in the browser, finds it in the tree, downloads it and compares the bytes |
| `03-access.spec.ts` | group, membership, policy editor, the effect for bob | R-UI-025, R-UI-026 | bob signs in (JIT) and sees no repository; the administrator creates a group, adds bob, writes a policy in the editor (the preview reports problems while the draft is empty, then the covered repository, and "Try it" explains the draft); bob sees the repository and its file on several requests; removing the membership takes it away again |
| `04-tokens.spec.ts` | personal token create and revoke | R-UI-029 | alice creates a token, sees the value once, uses it six times (every replica), revokes it, and the token is refused six times after the bound of R-AUTH-042 |
| `05-exposure.spec.ts` | exposure rule with the preview | R-UI-039, R-EP-017 | on the endpoint `customers`: the preview's verdict per path as the rule is typed, create, read back on another replica |
| `06-audit-and-system.spec.ts` | audit log, system status | R-UI-036 to R-UI-038 | one entry for each action the journeys before it did (repository, group, membership, policy, token created and revoked, exposure rule), an event opened; the status screen shows three ready `web` replicas and "Answered by" names one of them |
| `07-german-dark.spec.ts` | German, dark theme | R-UI-044, R-UI-045 | the break-glass sign-in at `/ui/de/...`, five screens in German with axe in the dark theme, the language menu back to English, sign-out |

The files run in this order, in one worker: the audit journey reads what the journeys before it did. A journey prepares
its data through the API with the administrator token (INV-17: `beforeAll` creates repositories and files, `afterAll`
deletes them) and does through the UI only what it tests. Every name carries a base-36 timestamp, so a rerun needs no
cleanup first.

### Every journey is scattered, and every page is checked

- **Replicas.** The `replicas` fixture collects the `X-Trove-Replica` of every response the page received; a journey
  asserts at least two. (The status screen's "Answered by" is checked to name one of the three, not to change: a screen
  makes a fixed number of API calls, and with the round robin of the edge pod that holds the browser's connection a
  given call can reach the same replica on every load; 8 reloads in a row did.) The scatter profile's
  round robin is per request, so a journey that signs in, writes and reads back has used the session, the change log
  and the caches across replicas (R-ARCH-032, R-API-052).
- **Accessibility.** `expectAccessible(page, screen)` waits for finite animations (a dialog still fading in has a
  blended colour), runs axe on the page and fails on a violation of impact serious or critical. It runs on each screen
  a journey visits, in English with the light theme and, in the German journey, in the dark theme.
- **CSP.** Every page of every journey is watched for `securitypolicyviolation` events; one fails the test (the
  security tier of R-UI-047).
- **TLS.** Chromium does not read `NODE_EXTRA_CA_CERTS`, and the dev CA is in no system store, so the browser contexts
  ignore certificate errors. `global-setup.ts` makes up for it: before any journey it opens a verified TLS connection
  (dev CA, name checked) to the edge and to the IdP and fails the run on a wrong or expired certificate.

### How the browser reaches the IdP

The issuer is the Service name `trove-keycloak.trove.svc` ([local-profile.md](local-profile.md#the-test-idp)), which
no resolver on the host knows. `TestE1_11Journeys` opens a `kubectl port-forward` to the Service (the one the Go
tests use, `keycloakAddr`) and passes its address as `TROVE_E2E_KEYCLOAK_ADDR`; `playwright.config.ts` adds
`--host-resolver-rules=MAP trove-keycloak.trove.svc <address>` to Chromium, so the page keeps the issuer's name (and its
certificate check) and no hosts entry is needed. Without that variable the name must resolve on its own.

### Environment

| Variable | Set by | Meaning |
|---|---|---|
| `TROVE_E2E_EDGE_URL` | `e2e.sh` | the edge, `https://localhost:<port>` |
| `NODE_EXTRA_CA_CERTS` | `TestE1_11Journeys` (from `TROVE_E2E_CA_FILE`) | the dev CA for `global-setup.ts` and the Node side of the suite |
| `TROVE_E2E_KEYCLOAK_ADDR` | `TestE1_11Journeys` | `host:port` of the port-forward to the test IdP |
| `TROVE_E2E_ALICE_PASSWORD`, `TROVE_E2E_BOB_PASSWORD`, `TROVE_E2E_BREAKGLASS_PASSWORD` | `TestE1_11Journeys` | read from the Secrets `trove-keycloak` and `trove-break-glass` |
| `TROVE_E2E_ADMIN_TOKEN` | `TestE1_11Journeys` | the token of the account `e2e-admin` (`adminToken`) |
| `PLAYWRIGHT_JSON_OUTPUT_NAME` | `TestE1_11Journeys` | `.out/playwright/results.json`, which the test turns into one subtest per journey |

`TestE1_11Journeys` also unlocks the break-glass user first (a lockout left by `TestE1_11Scatter` would fail the first
sign-in for a reason that is not the UI). `global-setup.ts` deletes the users `alice` and `bob`, as `TestE1_01Login`
does: a user left by an earlier run is bound to the subject of an IdP that may since have been rebuilt, and the sign-in
refuses it with `username_taken`.

### Installing and running

```shell
cd web && npm ci                                   # @playwright/test and @axe-core/playwright, pinned exactly in package-lock.json
npx playwright install --with-deps chromium       # the browser build of the locked Playwright version (--with-deps: apt on Linux)
make e2e SET=access                                # or: make e2e SET='^TestE1_11Journeys$'
```

The browser binaries live outside the repository (`~/.cache/ms-playwright`, on macOS `~/Library/Caches/ms-playwright`)
and are named by the Playwright version, so they follow the lockfile: after a Playwright bump run the install again.

**CI.** The `e2e` job of [release.yml](../../.github/workflows/release.yml) sets up Node from `web/.nvmrc`, runs `npm ci` in
`web/` and `npx playwright install --with-deps chromium` before the dev profile comes up, and uploads
`.out/playwright/` (HTML report, traces and screenshots of failed journeys) as `e2e-playwright-<set>`. The `--with-deps`
step uses `sudo apt-get`, which the job already does for the build tools; a self-hosted runner that is not a Debian
derivative needs the libraries Playwright lists installed by hand. This has not run on a GitHub runner yet; locally the
same commands ran on macOS (the browser download without `--with-deps`).

**After a new image** the deployment does not restart on its own (the chart does not change, the tag is `dev`):
`kubectl -n trove rollout restart deploy/trove-web deploy/trove-worker`, then wait for the rollout.

By hand against a running profile (after `TROVE_PROFILE=scatter deploy/dev/up.sh`): export the variables above, forward
the IdP and run `npx playwright test` in `web/e2e` (`--ui` or `--headed` to watch). The HTML report is
`.out/playwright/report/index.html`; `npx playwright show-report .out/playwright/report` opens it.

### What the browser tier found

Both are fixed in the same change; the journeys fail without the fix (axe, serious or critical).

1. **The label of every `mat-form-field` was `display: none`.** The density of -2 sets
   `--mat-form-field-filled-label-display: none`, so the sign-in form, the filters of the identity screens and the
   fields of the policy and token forms showed empty boxes, and axe reported `label` and `select-name` as critical (the
   label is hidden). jsdom has no layout and could not see it. Fix: `--mat-form-field-filled-label-display: block` in
   [main.scss](../../web/src/styles/main.scss) (Q-UI-67).
2. **The user menu button's accessible name ("User menu") did not contain its visible text (the user's name).**
   WCAG 2.5.3, axe `label-content-name-mismatch`, on every screen. Fix: no `aria-label`; the name is the visible user
   name followed by a visually hidden "User menu" ([app.html](../../web/src/app/app.html), Q-UI-67).

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

The M1 tests, same machine, measured in `make e2e SET=access` and the re-runs that followed it: E1.1 128 s, E1.8 69 s, E1.11 343 s
(with the 50 browser logins at 2.2 s, then 25 and 25 in the Valkey-stopped and FLUSHALL modes), E1.13 about 400 s (upload
under a restart 65 s, download under a restart 80 to 170 s, cut download and resume 220 s), E1.14 80 s; `TestE14Drain` with
the transfer loop 174 s. Numbers the tests print:

| What | Result |
|---|---|
| E1.8, 1000 requests with one token over three replicas | `read` class: 200 admitted (burst 200) in 0.9 s, 66 to 73 per replica; one replica alone: 200; Valkey stopped: 325 in 2.7 s (each replica's share plus the decay), `trove_ratelimit_fallback{limiter="api_read"}` 1 on all three |
| E1.8, `CLIENT PAUSE 4000 ALL` on the Valkey primary | 15 of 2975 requests held (400 ms or more), the first at +0.4 s and the last at +2.5 s: `failure_threshold` 5 times three replicas; 2847 fast requests after the last held one |
| E1.8, 400 random well-formed tokens, Bearer and Basic alternating | 300 validated (401), 100 throttled (429), 50 of each kind; with Valkey stopped 300 validations in all and 40 starts of the login flow admit 30 |
| E1.11, revocation of a PAT | last `200` 0 s (Valkey up) and 0.2 s (stopped) after the revocation answered, `401` on all three replicas |
| E1.13, 500 MB at 50 MB/s during `kubectl rollout restart deploy/trove-web` | 8 uploads, 0 failed, 8 overlapped the rollout (48 s) |
| E1.13, 400 MiB download | 6 MB/s during a rollout restart: the serving pod drained 25 s in, curl exit 0, SHA-256 matches. 2 MB/s with the serving pod deleted: the pod logs the cut and stops after 1 min 55 s, the resume is `206` from another replica, SHA-256 matches |
| E1.14, rule lifecycle on each replica | created, narrowed and deleted rule visible on every replica within 10 ms to 1 s with Valkey, 0.1 to 1 s without (bounds 1 s and 2 s) |
| E1.14, 1000 requests with one token on `customers` | 200 admitted with Valkey; 264 to 280 with Valkey stopped, `trove_ratelimit_fallback{limiter="endpoint_external"}` 1 on all three |
| E0.14 with the transfer loop | 1362 transfer requests (681 PUT, 681 GET), 0 failures, 0 wrong checksums, 0 dropped iterations; k6 17190 pings, only `200` |

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

5. **Fixed in trove: the end of a download was lost when the replica that served it exited right after its last response**
   (`TestE1_13Transfers`, the download under a rollout restart; red in 4 of 8 runs, 2 of 2 on the day of the fix). The 400 MiB
   file is downloaded at 6 MB/s through the edge while `kubectl rollout restart` replaces the web pods. trove logged the
   request as `200` with `response_bytes` 419430400 and stopped 0.2 to 0.5 s later; curl had 396 to 399 MiB, got nothing more
   and ended after 2 min 33 s with `HTTP/2 stream 1 was not closed cleanly: PROTOCOL_ERROR` (exit 92). Cause: when the
   handler returned, the last 2 to 4 MiB were in the replica's kernel send queue, because the reader (curl at 6 MB/s through
   HAProxy) had not taken them. `net/http` closed the connection and the process exited; the pod's network namespace went
   with its containers, and a socket that `close()` leaves behind with unsent bytes dies there without a `FIN` or an `RST`.
   The edge lost the tail and was never told. The same silence was the cut download's 77 s: the replica stopped at the shutdown
   deadline and curl heard of it when the edge's own timeout fired. It was not the edge: HAProxy relayed what it received
   and had no way to learn of bytes that were never sent. Fix ([Q-ARCH-187](../spec/architecture.md#q-arch-187--a-connection-is-held-until-the-peer-has-taken-its-last-bytes-and-a-cut-is-a-reset),
   [internal/httpserver/linger.go](../../internal/httpserver/linger.go)): during the shutdown a connection that closes with
   unacknowledged bytes (`SIOCOUTQ`) is held open until the peer has acknowledged them, at most until the commit bound of
   `planShutdown`; a connection that is cut at the deadline or still unacknowledged at that bound is closed with `SO_LINGER 0`,
   so the peer receives an `RST`. The pod's log says what was held: `connections held open until the peer acknowledged their
   last bytes` with `unacknowledged_bytes` (2.0 to 4.1 MB in each of the 11 runs after the fix) and `waited` (up to 0.84 s).
   After the fix: the download under a rollout restart ran 10 times in a row green (curl exit 0, SHA-256 matches, the serving
   pod drained 13 to 37 s in), and the cut download ends for curl 1 s after the last bytes (exit 92), not 77 s; the test now
   asserts that bound (15 s). Off Linux (a developer's `go test` on macOS) the hold and the reset are off (no `SIOCOUTQ`; a streaming connection's `RST` was observed not to reach the peer on macOS loopback), so the three unit tests of `linger_test.go` run on Linux only (CI, or a Linux container). The `h2` stream the edge opens to curl is unchanged; no edge setting was touched.
6. **Open: the access log's `principal`, `principal_type` and `auth_method` stay `anonymous` and `none` for authenticated
   requests.** Seen in the `"log":"request"` line of a `GET /api/v1/auth/whoami` with a valid service-account token
   (`status` 200, `principal":"anonymous"`) and of every `PUT /files/…` of E1.13. The stage authenticates (the answer
   names the service account); the field is not filled. Not asserted by the suite, which reads the answers and the audit
   events; the access log is a finding for E1.5's "appears in the log stream".
7. **Fixed in the profile: the object store did not survive E1.13.** Two 500 MB uploads at once were OOM-killed at the
   1 GiB limit of SeaweedFS (`OOMKilled`, exit 137; trove answered `504` and `500`), and the drain of E0.14 with the
   transfer loop evicted the node that hosted it: every blob lives on its `emptyDir`, so every download after that was
   `503 content_unavailable`. `deploy/dev/s3.yaml` now has a 4 GiB limit and runs on the control-plane node, like the
   edge. A consequence for tests: the metadata of a blob outlives the store's data (an upload of bytes that already have
   a blob row writes no object), so the bytes the M1 tests upload depend on the store instance (`storeNonce`, the UID of
   its pod).
8. **Fixed in the profile: the Keycloak client's redirect URI was `https://localhost:8080/…` whatever port the cluster
   was created with.** `up.sh` now sets `KC_TROVE_BASE_URL` from the edge port.

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

## The npm client matrix

The npm acceptance matrix of [R-NPM-045](../spec/formats/npm.md#r-npm-045--client-acceptance-matrix) runs eight real
clients against the `scatter` profile: npm 10, 11, 12, pnpm 10, 11, 12, Yarn 4 (Berry) and Yarn 1.22 (Classic). This page
describes how the suite gets them; the scenarios are described with the tests that use them. The decisions are
[Q-NPM-44](../spec/formats/npm.md#q-npm-44--how-the-end-to-end-suite-gets-the-eight-clients) and
[Q-NPM-45](../spec/formats/npm.md#q-npm-45--the-nodejs-the-clients-run-on).

```
test/e2e/clients/
  package.json         one npm alias per row, exact version ("npm12": "npm:npm@12.2.1", "yarn4": "npm:@yarnpkg/cli-dist@4.18.1", ...)
  package-lock.json    the integrity of every package; generated by npm, not edited
  bin/<row>            the wrapper of a row: npm10 npm11 npm12 pnpm10 pnpm11 pnpm12 yarn4 yarn1
  fwd/                 the TCP forwarder (Go, unit-tested without a build tag)
  Dockerfile           FROM the pinned Node image, npm ci --ignore-scripts, then the install script of pnpm 12 only
  build.sh             builds fwd for linux/<arch> and the image (make npm-clients-image, deploy/dev/e2e.sh)
  check-pins.mjs       what make versions-check verifies about all of the above
```

- **The pins live once.** The versions are `package.json` and its lockfile; the Node.js image is
  `E2E_CLIENTS_NODE_IMAGE` in [tools/versions.env](../../tools/versions.env), a digest-pinned tag that starts with the
  version of `web/.nvmrc`. `make versions-check` runs `check-pins.mjs`: the tag follows `web/.nvmrc`, every alias is
  `npm:<package>@x.y.z` (no range), the lockfile resolves each alias to exactly that package and version, and every
  alias has a wrapper and every wrapper a pin. Renovate groups the aliases as "npm clients" and opens no major for them
  ([renovate.json](../../renovate.json)); changing the rows is an edit of the spec table and of `package.json`.
- **No second client in the image.** The `npm`, `npx` and `corepack` of the Node image are removed, so a test that
  types `npm` fails instead of running an unpinned npm; Corepack would download clients at run time and is not used.
  Yarn 4 is `@yarnpkg/cli-dist`, the package that carries the Yarn 4 CLI itself, which needs no `yarnPath` and no download.
- **One Node.js for all rows.** The image runs Node.js 24.22.2. The `engines` ranges (read with `npm view` on
  2026-10-30) accept it for every row: npm 12.2.1 needs `^22.22.2 || ^24.15.0 || >=26.0.0`, npm 11.21.1
  `^20.17.0 || >=22.9.0`, npm 10.9.9 `^18.17.0 || >=20.5.0`, pnpm 11.28.9 `>=22.13`, pnpm 10.34.9 `>=18.12`, Yarn 4.18.1
  `>=18.12.0`, Yarn 1.22.22 `>=4.0.0`. pnpm 12.16.1 (`>=18`) does not run on Node.js at all: its install script replaces the
  launcher with the native binary of its platform package, so the Dockerfile runs `npm rebuild pnpm12` (all other scripts
  stay off); with the script skipped the launcher falls back to Node.js and may download a binary at first use.
  Verified: `<row> --version` prints the pinned version for all eight rows in the built image, and `pnpm12` also runs with
  no `node` on `PATH`. Not verified yet: the full scenario list of each row on Node.js 24; the matrix tests show it.
- **The base URL is the real one.** Each client pod runs `fwd -listen 127.0.0.1:<edge port> -to <target>` as its only
  process. The clients therefore use `https://localhost:<edge port>` exactly as the setup snippets
  ([R-NPM-041](../spec/formats/npm.md#r-npm-041--client-setup-instructions)) print it, and TLS verifies `localhost`
  against the dev CA (the certificates of the edge and the listener both carry `localhost`,
  [deploy/dev/pki.yaml](../../deploy/dev/pki.yaml)). `fwd` relays bytes and terminates nothing. Its default target is
  the in-cluster HAProxy Ingress HTTPS port (`TROVE_E2E_NPM_CLIENTS_TARGET`); a later test that pins a client to one `web`
  pod starts its pod with that pod's address instead.
- **The CA is mounted, not baked.** `deploy/dev/e2e.sh` writes the dev CA into the ConfigMap `trove-e2e-npm-ca`, the pod
  mounts it at `/etc/trove-ca/ca.crt` and sets `NODE_EXTRA_CA_CERTS`, which Node.js itself reads (npm, pnpm 10 and 11,
  both Yarn lines). Open, not verified: whether the native pnpm 12 reads that variable; if it does not, its scenarios pass
  the CA through pnpm's `ca`/`cafile` setting. Neither has been run against trove yet; the second smoke subtest below
  proves the path with plain Node.js. The pod follows the restricted Pod Security profile
  (non-root, read-only root filesystem, `/work` as `HOME`, `/tmp`, both emptyDir, no service account token) and runs on
  the control-plane node like the driver.
- **Running a client.** `kubectl -n trove exec trove-e2e-npm-0 -- npm12 --version`. The Go side is
  `npmClientExec(t, pod, argv...)` in `npm_clients_test.go`; `TROVE_E2E_NPM_CLIENTS_PODS` (default 1) sets how many pods
  `e2e.sh clients` starts, `trove-e2e-npm-0` to `-<n-1>`.
- **The smoke test.** `TestNPMClients` has two subtests: `R-NPM-045 every gate row runs --version` compares every
  wrapper's output with the version in `package.json`, and `R-NPM-045 the base URL reaches the cluster with a verified
  certificate` requests `/api/v1/system/ping` from the pod through the forwarder with a plain Node.js `https.get`.

## Extension checklist

- **A new M1 check.** Use `adminAPI` for the seed and `newSAToken` for a token of your own: the shared administrator
  token has a read budget (R-API-016) that a list walk spends. A test that exceeds a per-address limit goes through
  `podForwards`; one that needs Valkey down goes through `withValkeyStopped`.

- **A new browser journey.** A file `web/e2e/tests/NN-<topic>.spec.ts`, importing `test` and `expect` from `../support`
  (never from `@playwright/test`, so the CSP watch and the replica collection apply), a title that starts with the
  requirement IDs, data prepared through the `admin` fixture, `expectAccessible` on every screen it visits and an
  assertion on `replicas`. Names carry `unique(...)`. Selectors are roles and labels, scoped (`getByRole('dialog')`,
  `getByRole('group', { name })`), because the page behind a dialog keeps its fields.
- **A new client row.** Edit the table of R-NPM-045, add the alias to `test/e2e/clients/package.json`, regenerate the
  lockfile with the pinned Node image (`docker run --rm -v "$PWD/test/e2e/clients:/w" -w /w <E2E_CLIENTS_NODE_IMAGE>
  npm install --package-lock-only --ignore-scripts`), add `bin/<alias>`, add it to `npmGateRows`, run `make versions-check`
  and `make npm-clients-image`.
- **A new e2e check.** Put it in the file of its exit criterion, name the subtest `R-… E0.x …`, and fail
  with a message that says what was measured. Use `faultRun` for a fault under load.
- **A new fault.** A script in `deploy/dev/` that injects, waits for the recovery and prints how long
  it took; a `make dev-…` target; a case in `TestE13Failover`.
- **A later milestone's client load.** Extend `k6/load.js` (WP2.8); the Go side only reads the console lines.
- **A new job kind for a test.** Register it in the release image only when the test cannot be written
  without it, and say so here.
