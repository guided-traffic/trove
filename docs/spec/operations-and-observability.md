# Operations and observability

Status: specification — not implemented

This page specifies how trove is run, watched and automated from outside. It covers:

- Health and readiness semantics per process role (`web`, `worker`) on the operations listener,
  including the degraded state while Valkey or object storage is unavailable; ping, version and
  system information.
- The Prometheus metrics catalog, with its naming and cardinality rules, the metrics of Valkey, the
  change log and the connection pools, and how a series names the role it comes from.
- The operational signals and alerts behind the owner constraints on availability, horizontal
  scale and backup.
- The structured log stream: application, request, upstream and audit records, plus secret
  redaction.
- The audit event catalog, with its storage, query, export and retention.
- OpenTelemetry tracing.
- Administrative operations: scheduled maintenance tasks, the garbage-collection trigger, remote
  cache invalidation, the installation-wide cache flush, metadata reindex, the storage summary,
  read-only maintenance mode, the system banner, lock diagnostics and support bundles.
- The configuration file and how environment variables map onto it.
- Configuration as code: declarative documents with diff and apply semantics.
- Outgoing mail and content types for raw downloads.
- Webhooks: the event outbox, envelope, event types, subscriptions, signing, templated requests,
  retries through the job queue on `worker` replicas, outbound address protection, test delivery
  and the delivery log.

Backup and restore belong to [backup-and-restore.md](backup-and-restore.md) (format, commands, run
visibility) and [ha-and-helm.md](ha-and-helm.md) (the CronJob, the restore Job and the platform
protection). This page carries the backup's metrics and audit events in its catalogs and lists, in
[Backup and restore](#backup-and-restore), only what it adds to them.

Covers: features from [operations-and-monitoring.md](../features/operations-and-monitoring.md) and
[events-and-extensibility.md](../features/events-and-extensibility.md). From
[retention-and-cleanup.md](../features/retention-and-cleanup.md) it takes only the operational side
(scheduling, run visibility, audit). The retention policies themselves are specified in
[storage.md](storage.md). It also covers the related operation entries of
[api-content-operations.md](../features/api-content-operations.md) and
[api-admin-operations.md](../features/api-admin-operations.md), and single entries from
[ha-and-deployment.md](../features/ha-and-deployment.md),
[storage-and-blobs.md](../features/storage-and-blobs.md) and
[artifacts.md](../features/artifacts.md).

## Conventions used on this page

- **API.** Every `/api/v1` operation follows the conventions of [api-and-ui.md](api-and-ui.md):
  JSON bodies, [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem details for errors,
  cursor paging, and the credentials of INV-8. The JSON shown here is a sketch. The OpenAPI document
  is authoritative (INV-6).
- **Time.** Records and responses carry RFC 3339 timestamps in UTC with millisecond precision.
  Durations in configuration are Go duration strings (`10s`, `15m`, `24h`). Retention periods are
  whole days, in keys that end in `_days`.
- **Schedules.** Five-field cron expressions evaluated in UTC, with the fields, names and macros
  of [Kubernetes CronJobs](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#schedule-syntax)
  (`@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`, `@midnight`, `@hourly`). Every
  schedule in trove goes through one parser (Q-OPS-7). The normative dialect, its parser tests and
  the rejected forms are in [storage.md](storage.md) R-STOR-025: among others six-field
  expressions, `?`, the day-of-week value `7`, `@every`, and `TZ=` or `CRON_TZ=` prefixes, each
  rejected with an error that names the key. The backup CronJob's schedule is the one that
  Kubernetes evaluates, in `backup.timeZone` (default `Etc/UTC`, [ha-and-helm.md](ha-and-helm.md)
  R-HA-060); the chart rejects the same forms in it (Q-OPS-7).
- **Roles.** `web` replicas serve the client listener; `worker` replicas run the job queue, the
  scheduler and webhook delivery; both serve the operations listener (INV-2,
  [architecture.md](architecture.md) R-ARCH-001). "Every replica" on this page means every `web`
  and `worker` replica. `backup` pods, the restore Job and the migration Job are not replicas: they
  have no probes and no registry row ([ha-and-helm.md](ha-and-helm.md) R-HA-003, R-HA-060).
- **Cross-replica propagation** follows [architecture.md](architecture.md) R-ARCH-018 (INV-3,
  INV-12). trove issues no `LISTEN` and no `NOTIFY`. A change that other replicas must apply within
  a bound goes to a *change area*: the writing transaction advances the area's epoch in PostgreSQL,
  every replica polls the change log every `cluster.epoch_poll_interval`, and a Valkey message sent
  after commit only makes it faster. A wake-up that nothing depends on goes to a *signal area*,
  published to Valkey only. This page owns the change areas `system_state`, `support` and
  `webhooks` and the signal areas `events` and `deliveries`. "In force on every replica" means
  within `cluster.epoch_poll_interval` plus 1 s whether Valkey is available or not (P-13 of
  [architecture.md](architecture.md) R-ARCH-027).
- **Permissions** used on this page. [authorization.md](authorization.md) defines them; the names
  are proposals. Token scopes that narrow them (TOK-014) belong to
  [identity-and-authentication.md](identity-and-authentication.md).

| Permission (proposed name) | Grants |
|---|---|
| system administrator | Every operation on this page |
| `system.read`: the capability `settings:read` of [authorization.md](authorization.md) R-AUTHZ-010 | Read replicas, system information, tasks, jobs, locks, effective configuration, log levels, backup runs ([backup-and-restore.md](backup-and-restore.md) R-BAK-014) |
| `audit.read` | Query and export audit events (the auditor role, IAM-006) |
| `storage.read` | Read the storage summary (`GET /api/v1/storage/summary`) and the damaged-blob list (`GET /api/v1/storage/damaged-blobs`); both read paths are owned by [storage.md](storage.md) (R-STOR-027, R-STOR-028) |
| `config.read` | Run configuration diff and export |
| `webhooks.read`, `webhooks.manage` | Webhook subscriptions and delivery log (R-OPS-039) |
| repository action `manage` (INV-9) | Cache invalidation and reindex of that repository |

## Health and system information

### R-OPS-001 · Liveness and readiness probes
Priority: **MVP** · Covers: OPS-002, OPS-003, OPS-004, ADMAPI-069, HA-023

Every `web` and `worker` replica MUST serve `GET /healthz` (liveness) and `GET /readyz`
(readiness) on the operations listener (INV-6) without authentication. Neither path exists on the
client listener, and a `worker` replica has no client listener at all
([architecture.md](architecture.md) R-ARCH-001). `backup` pods have no probes
([ha-and-helm.md](ha-and-helm.md) R-HA-060).

A probe request MUST answer from cached check results. It MUST NOT open a database connection, call
object storage or send a Valkey command itself, so a probe storm cannot load the dependencies. A
background loop runs every check every `health.check_interval`, each bounded by
`health.check_timeout`; the `valkey` check runs every `valkey.health_check_interval`
([ha-and-helm.md](ha-and-helm.md) R-HA-056).

**Liveness** reflects the process only (Q-OPS-1). It returns `200` while the HTTP server answers
and every watched loop has finished an iteration within its bound; otherwise `503`. It never reads
a readiness check result and is never coupled to readiness, so no outage of PostgreSQL, Valkey,
object storage, the OIDC provider or an upstream can fail it. Restarting replicas does not repair a
dependency outage, and coupling liveness to one would restart every replica at the same moment.

- **Watched loops.** The health check loop, with a bound of three `health.check_interval`s. With a
  bound of `health.watchdog_timeout` each, under the names that [architecture.md](architecture.md)
  R-ARCH-016 registers:

  | Loop | `web` | `worker` |
  |---|---|---|
  | `cluster.changes`: the change-log poll (R-ARCH-018) | yes | yes |
  | `cluster.signals`: the Valkey subscriber (R-ARCH-018, R-ARCH-031) | yes | yes |
  | `cluster.leases`: the lease check (R-ARCH-017) | — | yes |
  | `jobs.claim` and `jobs.reaper` (R-ARCH-016) | — | yes |
  | `jobs.scheduler`, while this replica holds the `scheduler` lease | — | yes |
  | `webhooks.dispatch`: the outbox dispatcher, while webhooks are enabled (R-OPS-035, P1) | — | yes |

  The development role `all` watches both columns.
- **Progress, not success.** A loop records progress at the end of every iteration, including
  iterations that did no work or failed against a dependency. A loop that keeps retrying an
  unreachable PostgreSQL is alive, and so is the Valkey subscriber while Valkey is unreachable.
  Waiting for a free worker slot is not part of an iteration: the claim loop records progress
  while every worker is busy, and while an unready `worker` claims nothing (Q-OPS-12), so a long
  job (a full garbage collection, for example) never fails liveness. Only a loop that has not
  finished an iteration within its bound, because of a deadlock or a call without a deadline,
  fails liveness.
- **Deadlines.** Every call a watched loop makes MUST carry a context deadline shorter than
  `health.watchdog_timeout`. A blackholed dependency then produces failed iterations, not a stuck
  loop. A unit test per loop asserts this.
- **Draining.** A loop that stops on purpose during shutdown leaves the watchdog first.
- **Evidence before the restart.** When a loop is first reported stuck, the replica writes one
  `ERROR` record that names the loop and carries a goroutine dump
  ([`runtime/pprof`](https://pkg.go.dev/runtime/pprof) profile `goroutine`), truncated to 256 KiB.
  The restart would otherwise erase the only trace of the deadlock.
- **Restart.** The chart's liveness probe ([ha-and-helm.md](ha-and-helm.md) R-HA-034) restarts the
  pod once `/healthz` has failed for its threshold.

**Readiness** means "may take work", for both roles (Q-OPS-12): a ready `web` replica is an
endpoint of the client Service, and a ready `worker` replica claims jobs; an unready `worker` keeps
running its loops but claims nothing. Readiness also drives the rollout of each Deployment and the
disruption budget, which counts only ready pods as healthy
([PDB healthiness](https://kubernetes.io/docs/tasks/run-application/configure-pdb/#healthiness-of-a-pod)).
`/readyz` returns `200` only while every gating check below passes. A check counts as failed after
`health.failure_threshold` consecutive failures and as passed again after
`health.success_threshold` consecutive successes; reported checks use the same thresholds for
their `degraded` state.

| Check | Passes when | Kind | Why |
|---|---|---|---|
| `startup` | The start-up steps of [architecture.md](architecture.md) R-ARCH-024 have completed: the ops listener is bound; on `web` the client listener is bound, on `worker` the claim loop, the reaper and the scheduler loop run; in-process caches are initialised; audit partitions for the current and the next month exist (R-OPS-015) | gating | No half-started replica receives traffic or claims a job |
| `schema` | The applied schema version is inside the window this binary supports (expand/contract, INV-3), and `system_state` holds no `restore_in_progress` marker ([backup-and-restore.md](backup-and-restore.md) R-BAK-021) | gating; a connectivity failure gets the outage grace below | A replica never serves against a schema it does not understand, or against a half-restored database |
| `database` | `SELECT 1` on the replica's control connection ([architecture.md](architecture.md) R-ARCH-019) succeeds within the timeout, and the server is a primary | gating, with the outage grace below | Nothing works without PostgreSQL (INV-3). The control connection keeps an exhausted request pool from failing the check, so load never takes replicas out of the Service one after another ([ha-and-helm.md](ha-and-helm.md) R-HA-008, Q-HA-20) |
| `invalidation` | The last successful change-log poll is younger than `cluster.epoch_max_staleness` ([architecture.md](architecture.md) R-ARCH-018) | gating | A replica that cannot poll the change log cannot bound how stale its revocations and policies are (INV-8, INV-9). The check depends on PostgreSQL only, never on Valkey. The poll runs on the replica's reserved control connection ([architecture.md](architecture.md) R-ARCH-019), so a saturated request pool does not fail it. Its evaluations start failing only once the poll has been stale for `cluster.epoch_max_staleness`, and it is debounced like `database`, so for the same outage it never turns a replica unready sooner than `database` does ([ha-and-helm.md](ha-and-helm.md) R-HA-008) |
| `objectStore` | A `HEAD` request on the bucket (or on a sentinel object under the configured prefix) succeeds within the timeout | gating until the replica is first ready, then reported; gating throughout with `health.require_object_store: true` (Q-OPS-2) | The first readiness also requires the object-store startup check of [storage.md](storage.md) R-STOR-035, so a release with a wrong bucket never replaces working pods |
| `valkey` | The Valkey health probe ([architecture.md](architecture.md) R-ARCH-030: a `SET` of the replica's probe key with its expiry) succeeds within `valkey.command_timeout`, and the replica's Valkey circuit is closed ([ha-and-helm.md](ha-and-helm.md) R-HA-057) | reported; before the replica is first ready it gates on a *configuration error* only (Q-HA-13) | Valkey holds shared ephemeral state only (INV-3). While it is unavailable every mechanism runs its fallback, so the replica still answers correctly and stays in the Service (INV-12) |
| `configBootstrap` | Startup configuration documents were applied (R-OPS-031); passes trivially when none are configured | gating | An invalid document stalls a rollout instead of breaking the service |
| `draining` | No termination signal was received | gating | Removes a `web` replica from the Service and stops a `worker` from claiming before either stops |

**Database outage grace** (Q-OPS-31). The `database` and `schema` checks read PostgreSQL, and a switchover takes
10 to 15 s while queries retry for `database.retry.max_elapsed` (R-ARCH-019). Once the replica has been ready, a failure
of one of them that is a connectivity failure (a refused, reset or timed-out connection, SQLSTATE `57P01`, `57P02`,
`57P03`, `53300` or class `08`, a server in recovery, a check that exceeded `health.check_timeout`) does not make the
replica unready until it has lasted `health.database_outage_grace`. Until then the check shows as `degraded` in
`/readyz?verbose=1` and in the registry, `/readyz` answers `200`, and the replica stays in the Service while its
requests wait within their retry. The grace is measured from the first failed evaluation of an unbroken run, and a
single passing evaluation does not restart it: only the check being `pass` again (`health.success_threshold`)
does. After the grace the check fails as any gating check does. Three things the grace does not do: a failure that is
not an outage (a schema that is behind, a rejected credential, a half-restored database) gates after the usual
debounce; the first readiness of a replica gets no grace; `0` switches it off. The value MUST be below
`database.retry.max_elapsed`, and start-up refuses a configuration that is not, naming the key: a replica has to turn
unready before its queries give up.

A Valkey *configuration error* is what [ha-and-helm.md](ha-and-helm.md) R-HA-008 lists: Valkey or a
Sentinel rejects the credentials (`WRONGPASS`, `NOAUTH`, an ACL denial), TLS verification fails, or
every reachable Sentinel answers that it does not know `valkey.sentinel.primary_name`.
Unreachable Sentinels or data nodes are never a configuration error. After the first readiness, a
configuration error (a rotation mistake) counts as unavailability and is reported like it
([ha-and-helm.md](ha-and-helm.md) R-HA-007). The running replicas stay ready, but every pod that
starts while the error lasts stays unready: a drain's replacement pods and the autoscaler's new
pods add no capacity, so `kubectl drain` waits on the disruption budget instead of causing an
outage, until the Secret or the ACL is corrected. `TroveValkeyUnavailable` (R-OPS-046) fires,
because every running replica reports the error as `degraded`.

**Gating and reported checks** (Q-OPS-2, [ha-and-helm.md](ha-and-helm.md) Q-HA-13). Once the
replica has been ready, two checks are reported instead of gating: `objectStore` (unless
`health.require_object_store: true`) and `valkey` (always). While one of them fails:

- `/readyz` answers `200` with the check under `degraded`, for example `"degraded":["valkey"]`;
- the replica registry (R-OPS-003) and `trove_health_check_passing{name}` show the failure, and so
  does `trove_valkey_available` for Valkey (R-OPS-045); the alerts of R-OPS-046 fire;
- `objectStore`: blob requests fail one by one with a retryable `503` through the circuit breaker
  ([storage.md](storage.md) R-STOR-033, R-STOR-036);
- `valkey`: no request fails because of it. Fetch collapse and the negative cache work per replica,
  cluster-scoped rate limits fall back to per-replica shares, changes take effect at the next
  change-log poll, and signal consumers wait for their own polls
  ([ha-and-helm.md](ha-and-helm.md) R-HA-057, [architecture.md](architecture.md) R-ARCH-030).

A shared object-store outage therefore leaves logins, CI token exchange, metadata, deletes, the UI
and the API working, and a Valkey outage costs upstream load and latency, instead of removing every
replica from the Service. A release with a wrong bucket, endpoint or credential, or with a wrong
Valkey password, primary name or CA, still never becomes ready, so it cannot replace working pods.
With `health.require_object_store: true` the object-store check gates readiness continuously, with
the same thresholds as every other check (`health.failure_threshold`, `health.success_threshold`);
storage has no readiness settings of its own ([storage.md](storage.md) R-STOR-036). Valkey has no
such switch: INV-12 forbids gating on it.

**Shutdown.** On `SIGTERM` the replica follows the order of [architecture.md](architecture.md)
R-ARCH-024. This page owns what probes, the registry and the records show:

1. Fail the `draining` check, so readiness answers `503` at once, and set the registry row to
   `draining` (R-OPS-003). A support-bundle part this replica is still collecting (R-OPS-027) is
   abandoned. A `worker` that holds the `scheduler` lease stops its scheduler tick and releases
   that lease now, not at the end of its drain (R-OPS-019).
2. `web`: keep serving for `health.shutdown_drain_delay`, so that requests a proxy still routes to
   the replica succeed; responses completed from now on carry `Connection: close` on HTTP/1.1. The
   delay must exceed the edge's endpoint-removal time ([ha-and-helm.md](ha-and-helm.md) R-HA-059
   rule 4). `worker`: no delay, because no Service routes to it; it stops claiming at once.
3. `web`: stop accepting connections and let in-flight requests finish within
   `server.shutdown_timeout`. Streams that never become idle on their own, the delivery-log follow
   stream of R-OPS-044, are ended at this point through `http.Server.RegisterOnShutdown`, because
   `Shutdown` waits for active connections to become idle and does not end such streams itself
   ([`net/http`](https://pkg.go.dev/net/http#Server.Shutdown)); their clients reconnect to another
   replica ([api-and-ui.md](api-and-ui.md) R-API-051). `worker`: running jobs finish, or are
   released without counting the attempt 5 s before the deadline
   ([architecture.md](architecture.md) R-ARCH-016).
4. Write the open audit deduplication summaries and flush the asynchronous audit buffer
   (R-OPS-015), so that a drain loses no audit event while PostgreSQL is reachable.
5. Delete the registry row and exit.

Liveness stays `200` while the replica drains: every loop that stops leaves the watchdog first.
[ha-and-helm.md](ha-and-helm.md) R-HA-011 sets each role's termination grace period and derives
`server.shutdown_timeout` from it.

**API (operations listener, no authentication):**

```http
GET /readyz              → 200 {"status":"ready"}
GET /readyz              → 200 {"status":"ready","degraded":["valkey"]}
GET /readyz              → 200 {"status":"ready","degraded":["objectStore","valkey"]}
GET /readyz              → 503 {"status":"unready","failing":["database","invalidation"]}
GET /readyz?verbose=1    → 200 | 503
GET /healthz             → 200 {"status":"alive"} | 503 {"status":"stuck","stuck":["jobs.claim"]}
```

```json
{
  "status": "ready",
  "replica": "trove-web-6f9c7d8b5-x2v4q",
  "role": "web",
  "checkedAt": "2026-10-28T12:00:05.120Z",
  "degraded": ["valkey"],
  "checks": {
    "startup": {"status": "pass"},
    "schema": {"status": "pass", "applied": 42, "supported": [41, 42]},
    "database": {"status": "pass", "latencyMs": 2},
    "invalidation": {"status": "pass", "lastPollAgeMs": 412},
    "objectStore": {"status": "pass", "latencyMs": 11},
    "valkey": {"status": "degraded", "reason": "connection", "since": "2026-10-28T11:58:40.002Z"},
    "configBootstrap": {"status": "pass"},
    "draining": {"status": "pass"}
  }
}
```

A check's `status` is `pass`, `fail` (gating) or `degraded` (reported). The `valkey` check's
`reason` uses the vocabulary of the registry's Valkey view (R-OPS-003): an error kind of
`trove_valkey_errors_total` (`timeout`, `connection`, `readonly`, `auth`, `tls`, `oom`),
`no_primary` (every reachable Sentinel answers that it does not know the primary name) or
`unsupported_version`. A configuration error is `auth`, `tls` or `no_primary` before the replica
was first ready, with `status: fail` and `"configError": true`; afterwards the same reasons are
reported as `degraded`.

Failure text in verbose output MUST be sanitised. It never contains a connection string,
credentials or a presigned URL (R-OPS-013), and it names no host, address or user name: the
operations listener is unauthenticated (H-41). Addresses are in system information (R-OPS-004),
behind `system.read`.

**Configuration:**

```yaml
health:
  check_interval: 5s              # default (proposed)
  check_timeout: 2s               # default (proposed)
  failure_threshold: 2            # default (proposed)
  success_threshold: 1            # default (proposed)
  require_object_store: false     # default (proposed); true = object storage gates readiness continuously (Q-OPS-2)
  watchdog_timeout: 2m            # default (proposed); bound for the watched loops of liveness (Q-OPS-1)
  shutdown_drain_delay: 5s        # default (proposed)
  database_outage_grace: 20s      # default (proposed); a ready replica tolerates a PostgreSQL outage this short (Q-OPS-31); 0 = off; below database.retry.max_elapsed
```

**Acceptance criteria:**

- `kubectl -n trove port-forward pod/<web replica> 8081:8081` followed by
  `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/readyz` prints `200`. The same
  path on port 8080 returns `404`. On a `worker` pod, `/readyz` on 8081 prints `200` and nothing
  listens on 8080.
- Switch PostgreSQL over (`kubectl cnpg promote`, a 10 to 15 s outage). `/readyz` answers `200` on every
  replica throughout, with `"degraded"` containing `database` while the outage lasts, and no replica
  leaves the Service.
- Apply a NetworkPolicy that denies egress from the trove pods to PostgreSQL. Once the outage has lasted
  `health.database_outage_grace` (20 s) plus one check interval, `/readyz` answers `503` on `web` and
  `worker` pods with `"failing"` containing `database`, while `/healthz` keeps answering `200`. While the policy is in
  place, no `worker` replica's instance ID appears in `job.locked_by` of a newly claimed job.
  Remove the policy, and `/readyz` returns to `200` within 5 s.
- Run `npm ci --registry https://trove.example.com/api/npm/npm-all/` in a loop while
  `kubectl -n trove rollout restart deploy/trove-web deploy/trove-worker` runs. No iteration fails.
- Kill the Valkey Pub/Sub connection of one replica (`CLIENT KILL TYPE pubsub` on the primary),
  then revoke a token through another replica. The token is rejected on the first replica within
  `cluster.epoch_poll_interval` plus 1 s, and its `/readyz` answers `200` throughout.
- Scale the Valkey data nodes and Sentinels to 0 for 3 minutes, and delete one `web` pod meanwhile.
  Every replica answers `/readyz` with `200` and `"degraded":["valkey"]`, no pod leaves the
  Service's endpoints, `trove_health_check_passing{name="valkey"}` is `0`, the replacement pod
  becomes ready, and the e2e client suite passes. After Valkey returns, `degraded` clears within
  `valkey.circuit_breaker.open_duration` plus `valkey.health_check_interval` plus 5 s.
- A new `web` pod started with a wrong Valkey password stays unready with `"failing":["valkey"]`,
  reason `auth` and `"configError": true`, and the running replicas stay ready (Q-HA-13).
- A replica started while `system_state` holds `restore_in_progress` stays unready with
  `"failing":["schema"]` and claims no job.
- 1,000 parallel requests to `/readyz` cause no additional PostgreSQL sessions
  (`pg_stat_activity` count unchanged) and no Valkey command (`INFO commandstats` on the primary
  unchanged apart from the probe interval).
- A `web` pod that holds an open delivery-log follow stream (R-OPS-044) is deleted. The stream
  ends within `health.shutdown_drain_delay` plus 1 s, the client reconnects with `Last-Event-ID`
  to another replica, and it misses no delivery event.
- 1,000 denied pushes are sent at random over three `web` replicas, and one of them is deleted
  during the burst. Afterwards the sum of `details.count` over the `authz.denied` events of that
  principal is 1,000 (R-OPS-015).
- Apply a NetworkPolicy that denies egress from every trove pod to the object store for 3 minutes.
  `/readyz` keeps answering `200` with `"degraded":["objectStore"]`, no pod leaves the Service's
  endpoints, `npm view @acme/ui --registry …/npm-local/` succeeds, and downloading its tarball
  answers `503` with `Retry-After`. A replica started while the policy is in place does not become
  ready.
- The same policy against PostgreSQL for 5 minutes leaves every watched loop progressing:
  `/healthz` stays `200` and no restart count increases.
- A fault-injection build that blocks the job claim loop makes `/healthz` answer `503` with
  `"stuck":["jobs.claim"]` within `health.watchdog_timeout`, and the log holds one `ERROR` record
  with a goroutine dump that names the loop.
- With `jobs.workers.maintenance: 1`, a job that runs for three times `health.watchdog_timeout`
  leaves `/healthz` at `200` throughout.

### R-OPS-002 · Ping, version and identification headers
Priority: **MVP** · Covers: OPS-001, OPS-005, OPS-006, ADMAPI-070

`GET /api/v1/system/ping` on the client listener (`web` replicas) answers `200 text/plain` with
`OK` while the replica serves, and `503` with `UNAVAILABLE` otherwise (Q-OPS-30). A replica serves while
it is ready, a degraded replica included (R-OPS-001), and while it drains after it had been ready: the drain
delay (R-ARCH-024 step 2) exists so that requests the edge still routes to the pod succeed, a ping among them.
It does not serve, and ping answers `503`, while a gating check other than `draining` fails (the database,
the schema, the invalidation check) and while the replica has never been ready. A Valkey outage therefore
never fails a ping. Anonymous callers MUST be
accepted even when anonymous repository access is off (AUTHN-036). Credentials that are presented MUST be
validated, so tooling can check base URL and token in one call. An invalid, expired or revoked token
yields `401`.

`GET /api/v1/system/version` returns build facts. By default it requires an authenticated caller
of any kind (Q-OPS-3). With `system.public_version: true` anonymous callers are accepted as well.
Invalid credentials yield `401` in either mode.

```json
{
  "version": "0.3.0",
  "revision": "4f1c2a9e0d7b…",
  "buildTime": "2026-10-20T09:12:00Z",
  "goVersion": "go1.27.3",
  "formats": ["npm", "nuget"]
}
```

Every client-listener response MUST carry `X-Request-Id` with the request's trace ID
(R-OPS-018). `X-Trove-Replica` with the replica name is sent according to
`server.replica_header`: `authenticated` (default) sends it only to authenticated callers,
`always` sends it to everyone, `never` suppresses it. A further endpoint has its own
`replica_header`, `never` by default ([endpoints.md](endpoints.md) R-EP-009). No response carries
the product version in a header.

```yaml
system:
  public_version: false           # default (proposed)
server:
  replica_header: authenticated   # default (proposed); authenticated | always | never
```

**Acceptance criteria:**

- `curl -s https://trove.example.com/api/v1/system/ping` prints `OK`.
- `curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer trv_pat_<revoked>' …/ping`
  prints `401`.
- Send `SIGTERM` to a `web` replica and call `/api/v1/system/ping` on it every 20 ms during
  `health.shutdown_drain_delay`: every answer is `200 OK` while `/readyz` on the ops listener answers
  `503` with `"failing":["draining"]`.
- An anonymous call to `/api/v1/system/version` gets `401`. The same call with a valid token gets
  `200`, and the response has an `X-Trove-Replica` header that the anonymous response lacks.

### R-OPS-003 · Replica registry and service status
Priority: **MVP** · Covers: OPS-008

The table, the heartbeat, the pruning of stale rows and the read-only API below are MVP, because
the HA mechanisms of [ha-and-helm.md](ha-and-helm.md) build on them (Q-HA-12 there). The UI
status screen is MVP as well ([api-and-ui.md](api-and-ui.md) R-UI-038, Q-API-8): it renders only
MVP endpoints and is the only view of the replicas, the scheduled tasks and the key ring.

Every `web` and `worker` replica MUST register itself in PostgreSQL at startup, with its role, and
refresh its row every `system.heartbeat_interval` on its control connection
([architecture.md](architecture.md) R-ARCH-019), so that a request pool exhausted under load never
makes a live replica look lost. A row whose heartbeat is older than `system.heartbeat_stale_after`
is shown as `lost`. The `replica-registry-prune` task (R-OPS-019) deletes rows that have been stale
for 24 h. On a clean shutdown the replica deletes its own row. Backup pods, the restore Job and the
migration Job never register ([ha-and-helm.md](ha-and-helm.md) R-HA-003).

Besides the HA mechanisms of [ha-and-helm.md](ha-and-helm.md) R-HA-003, four things on other
pages count the live replicas of a role here: the per-replica fallback of cluster-scoped rate
limits ([architecture.md](architecture.md) R-ARCH-034), the support bundle, which waits for one
part per live replica (R-OPS-027), the gauge `trove_valkey_primary_views` (R-OPS-045), and the
proxy's fetch shares ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-040), which
divide a remote repository's `maxConcurrentFetches` by the count of live client-serving replicas
(`web`, or `all` in the local profile), refreshed with the heartbeat. None of them needs Valkey.

**Node.** Each row names the Kubernetes node the replica runs on: `spec.nodeName`, which the
downward API offers only as an environment variable, passed by the chart as `TROVE_NODE_NAME`
(R-OPS-028); `NULL` outside Kubernetes. The instance name, which is not the node name, comes from `TROVE_POD_NAME`, then `POD_NAME`, then
`HOSTNAME` inside Kubernetes, and otherwise `<hostname>-<pid>` (Q-OPS-28). The
number of distinct nodes per role tells whether a role survives the drain of one node (INV-12,
[ha-and-helm.md](ha-and-helm.md) R-HA-059); system information reports it (R-OPS-004).

**Valkey view.** Each heartbeat also writes the replica's view of Valkey. This page owns its shape,
and [api-and-ui.md](api-and-ui.md) R-API-038 publishes it unchanged (cross-page): `state`
(`available` or `degraded`), `reason` (`null`, an error kind of `trove_valkey_errors_total` or
`unsupported_version`, as in the `valkey` check of R-OPS-001), `circuit`, the `primary` address
its client currently uses (host and port; `null` while it has none), `sentinelsReachable`,
`sentinelsConfigured`, `serverVersion` and `lastPrimaryChangeAt`. No credential, ACL user name or key appears. A replica
whose client stayed bound to a demoted primary ([architecture.md](architecture.md) R-ARCH-031)
shows up here as a disagreement with the others. `sentinelsReachable` is the replica's
`trove_valkey_sentinels_reachable` (R-OPS-008) at the heartbeat, refreshed with every heartbeat
like the rest of the view. The fields that [api-and-ui.md](api-and-ui.md) R-API-038 and R-UI-038
name `valkeyPrimary`, `valkeyPrimarySince` and `valkeySentinelsReachable` are `valkey.primary`,
`valkey.lastPrimaryChangeAt` and `valkey.sentinelsReachable` of this view (cross-page).

**Checks.** `checks` holds every check of R-OPS-001, gating and reported, as an object with its
`status` (`pass`, `fail` or `degraded`); the `valkey` check also carries its `reason` in the
vocabulary of R-OPS-001, so the status screen can name the cause (a timeout, a rejected
credential, a missing primary) without a second call; the circuit state is `valkey.circuit` of
the view above.

**Pool.** With the endpoint pools of [endpoints.md](endpoints.md) R-EP-008 (MVP) and the further
splits of [ha-and-helm.md](ha-and-helm.md) R-HA-062 (P1), the column `pool` names the pool of the
replica's Deployment, from the variable `TROVE_POOL_NAME` that the
chart sets on each pool Deployment (R-OPS-028); it is `NULL` for a role's main Deployment and
outside the chart (cross-page to R-HA-030 and R-HA-062).

**Key IDs of the stored-secret key ring.** Each row carries the key IDs of the replica's ring
(`secrets.encryption_key_files`) and its `secrets.active_key_id`
([architecture.md](architecture.md) R-ARCH-021), written at registration and refreshed with every
heartbeat. They are key IDs only: never key material, fingerprints or ciphertext. The key-ring
status of [api-and-ui.md](api-and-ui.md) R-API-049 reads them from here to decide whether the
rings of all live replicas are compatible, and the `secrets-reencrypt` task (R-OPS-019) refuses to
start while they are not.

```sql
CREATE TABLE replica (
  instance_uid  uuid        PRIMARY KEY,          -- random per process start
  name          text        NOT NULL,             -- pod name (TROVE_POD_NAME), else host name
  version       text        NOT NULL,
  revision      text        NOT NULL,
  roles         text[]      NOT NULL,             -- {web}, {worker}, or {all} for the development role (INV-2)
  state         text        NOT NULL CHECK (state IN ('starting','ready','unready','draining')),
  checks        jsonb       NOT NULL DEFAULT '{}',-- {"<check>":{"status":…,"reason":…}}: last results, gating and reported, sanitised
  leases        jsonb       NOT NULL DEFAULT '[]',-- [{"name":"scheduler","since":"…"}]; worker role only
  secret_key_ids       text[] NOT NULL,           -- key IDs of the replica's stored-secret key ring (R-ARCH-021); IDs only
  active_secret_key_id text   NOT NULL,           -- the replica's secrets.active_key_id; ID only
  node          text,                             -- Kubernetes node name (TROVE_NODE_NAME); NULL outside Kubernetes
  valkey        jsonb       NOT NULL DEFAULT '{}',-- the replica's view of Valkey (shape above), sentinelsReachable included; no credentials
  pool          text,                             -- endpoint pool (MVP) or R-HA-062 pool (P1), from TROVE_POOL_NAME; NULL for a role's main Deployment
  started_at    timestamptz NOT NULL,
  heartbeat_at  timestamptz NOT NULL
);
```

The table has the backup export class `transient` ([backup-and-restore.md](backup-and-restore.md)
R-BAK-004): a restored installation starts with an empty registry.

API: `GET /api/v1/system/replicas` → `200`. Requires `system.read`.

```json
{
  "items": [
    {
      "name": "trove-web-6f9c7d8b5-x2v4q",
      "instanceUid": "8a0b…",
      "version": "0.3.0",
      "revision": "4f1c2a9e0d7b…",
      "roles": ["web"],
      "state": "ready",
      "lost": false,
      "startedAt": "2026-10-28T08:01:12.004Z",
      "heartbeatAt": "2026-10-28T12:00:10.311Z",
      "leases": [],
      "node": "pool-a-7x2k",
      "checks": {"database": {"status": "pass"}, "invalidation": {"status": "pass"}, "objectStore": {"status": "pass"},
                 "valkey": {"status": "degraded", "reason": "connection"}},
      "degraded": ["valkey"],
      "valkey": {"state": "degraded", "reason": "connection", "circuit": "open", "primary": null,
                 "sentinelsReachable": 3, "sentinelsConfigured": 3, "serverVersion": "9.0.7",
                 "lastPrimaryChangeAt": "2026-10-28T11:20:03.511Z"}
    },
    {
      "name": "trove-worker-7c4d9f6b8-m3k8p",
      "instanceUid": "c19e…",
      "version": "0.3.0",
      "revision": "4f1c2a9e0d7b…",
      "roles": ["worker"],
      "state": "ready",
      "lost": false,
      "startedAt": "2026-10-28T08:01:14.870Z",
      "heartbeatAt": "2026-10-28T12:00:09.002Z",
      "leases": [{"name": "scheduler", "since": "2026-10-28T08:01:15.880Z"}],
      "node": "pool-a-9m4q",
      "checks": {"database": {"status": "pass"}, "invalidation": {"status": "pass"}, "objectStore": {"status": "pass"},
                 "valkey": {"status": "pass"}},
      "degraded": [],
      "valkey": {"state": "available", "reason": null, "circuit": "closed", "primary": "10.42.3.17:6379",
                 "sentinelsReachable": 3, "sentinelsConfigured": 3, "serverVersion": "9.0.7",
                 "lastPrimaryChangeAt": "2026-10-28T11:20:03.498Z"}
    }
  ]
}
```

*(example values)* The UI shows this list as the service status screen
([api-and-ui.md](api-and-ui.md) R-UI-038). Mixed versions during a rolling upgrade, the replicas of
each role and degraded replicas are visible here.

```yaml
system:
  heartbeat_interval: 10s         # default (proposed)
  heartbeat_stale_after: 30s      # default (proposed)
```

**Acceptance criteria:**

- With three `web` and two `worker` replicas, the list shows five `ready` entries, three with roles
  `["web"]` and two with `["worker"]`, each with the node it runs on; only a `worker` entry lists a
  lease. After
  `kubectl delete pod --grace-period=0 --force <replica>`, that entry shows `"lost": true` within
  30 s and is replaced by the new pod's entry.
- With Valkey stopped, every entry stays `ready`, lists `valkey` under `degraded`, and carries
  `checks.valkey` with `"status": "degraded"` and a reason.
- With three Sentinels configured and two of them stopped, every entry's
  `valkey.sentinelsReachable` is 1 within `valkey.sentinel.refresh_interval` plus
  `system.heartbeat_interval`, while `checks.valkey.status` stays `pass`.
- After `SENTINEL FAILOVER trove`, every entry's `valkey.primary` names the new primary within
  `valkey.sentinel.refresh_interval` plus `system.heartbeat_interval`.
- While a backup Job and the migration Job run, the list contains no entry for their pods.

### R-OPS-004 · System information
Priority: **MVP** · Covers: ADMAPI-070, OPS-007

`GET /api/v1/system/info` → `200`. Requires `system.read`. It returns the facts an operator needs to
describe the deployment, with every secret omitted. Cluster-wide facts come from PostgreSQL;
`answeredBy` and the pools describe the `web` replica that answered,
because requests reach replicas at random and replicas never ask each other (R-ARCH-002). The
replica registry (R-OPS-003) holds the per-replica view of every replica.

```json
{
  "installationId": "0b7c3f5e-…",
  "incarnation": "5d10e2a4-…",
  "answeredBy": {"replica": "trove-web-6f9c7d8b5-x2v4q", "role": "web"},
  "version": {"version": "0.3.0", "revision": "4f1c2a9e0d7b…", "goVersion": "go1.27.3"},
  "baseUrl": "https://trove.example.com",
  "baseUrlSource": "config",
  "maintenanceMode": "normal",
  "replicas": {
    "live": 5,
    "ready": 5,
    "versions": {"0.3.0": 5},
    "roles": {
      "web": {"live": 3, "ready": 3, "degraded": 0, "nodes": 3},
      "worker": {"live": 2, "ready": 2, "degraded": 0, "nodes": 2}
    }
  },
  "database": {
    "serverVersion": "17.6",
    "schemaVersion": 42,
    "supportedSchemaVersions": [41, 42],
    "poolerMode": "transaction",
    "pools": {
      "request": {"max": 19, "inUse": 3, "idle": 5},
      "control": {"max": 1, "inUse": 0, "idle": 1}
    }
  },
  "valkey": {
    "mode": "sentinel",
    "primaryName": "trove",
    "sentinels": 3,
    "tls": "verify-full",
    "keyPrefix": "trove:",
    "keyspace": "5f2a9c0b13de",
    "degradedReplicas": 0
  },
  "objectStore": {
    "endpoint": "https://s3.eu-central-1.amazonaws.com",
    "bucket": "trove-prod",
    "prefix": "blobs",
    "credentialSource": "workload-identity"
  },
  "backup": {
    "expectedIntervalSeconds": 86400,
    "lastSuccessAt": "2026-10-28T02:41:17.000Z",
    "overdue": false,
    "lastRunState": "completed"
  },
  "features": {"tracing": false, "webhooks": true, "configAsCode": true},
  "formats": ["npm", "nuget"],
  "runtime": {"goos": "linux", "goarch": "amd64", "gomaxprocs": 4, "memoryLimitBytes": 2147483648}
}
```

*(example values)*

- `installationId` is a UUID generated by the first migration and stored in `system_state`
  (R-OPS-024). Support bundles use it, and it is the stable identifier of the installation in
  exported data. A restore keeps it and writes a new `incarnation`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).
- `valkey.keyspace` is the keyspace segment of every key and channel
  ([architecture.md](architecture.md) R-ARCH-030). It changes with every restore, so an operator
  can tell the keys of the running installation from stale ones on a shared Valkey.
- `backup` is read from `backup_run` (R-BAK-014). The replicas never hold the backup target's
  credentials, so the block shows runs, not the target's contents. `expectedIntervalSeconds` is
  `backup.expected_interval` in seconds; the unit is part of the name, as
  [api-and-ui.md](api-and-ui.md) R-API-003 requires for durations. `overdue` follows the rule of
  [api-and-ui.md](api-and-ui.md) R-API-050: it is true when `lastSuccessAt` is older than
  1.5 × `backup.expected_interval`, the condition on which `TroveBackupStale` fires (R-OPS-046),
  so this block, the backup summary and the alert agree. With no successful run at all,
  `lastSuccessAt` and `overdue` are `null`.
- `database.pools` are the answering replica's. The Valkey block holds installation facts only;
  each replica's view of Valkey (its primary, circuit and Sentinels) is in the replica registry
  (R-OPS-003), and no Sentinel address appears in either.

**Acceptance criteria:** the response contains no field whose value is a password, key, token or
presigned URL. `grep -iE 'secret|password|trv_'` over the response finds only key names, never
values and no Sentinel address. With Valkey stopped, the call answers `200` and
`valkey.degradedReplicas` equals `replicas.live`. With three `web` replicas on one node,
`replicas.roles.web.nodes` is `1`.

### R-OPS-005 · External base URL and forwarded hosts
Priority: **MVP** · Covers: OPS-051, OPS-052, ADMAPI-071

`server.base_url` is the canonical external URL. Every absolute URL that trove generates MUST be
built from it: npm tarball URLs, NuGet service-index resources, the OIDC redirect URI, UI links,
`Location` headers, webhook `source` (R-OPS-036) and links in mail.

- **An origin, never a path below one.** `server.base_url` MUST be an origin: scheme `http` or
  `https`, a host and an optional port. Its path is empty or `/`, and it has no query, no fragment
  and no user information. Every `server.additional_base_urls` entry follows the same rule. A value
  that breaks it stops startup with an error that names the key and the offending part. trove owns
  the host it runs on: INV-6's host-root routes (the npm token exchange of
  [formats/npm.md](formats/npm.md) Q-NPM-1, `/v2/` reserved for OCI), the `__Host-trv_session`
  cookie with `Path=/` ([identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-2)
  and `GET /{$}` all assume it. Below a path prefix of a shared host, npm would post GitLab ID
  tokens to that host's root, and the session cookie would reach every service on it.
- **Unset.** Allowed for development only. URLs are then derived from the request's `Host` and
  scheme, a warning is logged at startup and every hour, and system information reports
  `baseUrlSource: request`. The Helm chart MUST require the value
  ([ha-and-helm.md](ha-and-helm.md)).
- **Per-request override.** The forwarded scheme and host from an RFC 7239 `Forwarded` header, or
  from `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-Port`, are used only when both
  conditions hold:
  - the TCP peer lies in `server.trusted_proxies`;
  - the forwarded origin equals one entry of `server.additional_base_urls`.

  In every other case forwarded headers are ignored for URL generation. The same trust rule
  derives the client address used in logs and audit: the right-most address in `X-Forwarded-For`
  that is not a trusted proxy.
- **The content host (P1).** With `server.content_base_url` set
  ([architecture.md](architecture.md) R-ARCH-029), its host is a known host under these rules as
  well: the per-request override selects it under the same two conditions, with
  `content_base_url` in place of a list entry. A request resolved to it is a content-host request.
  R-ARCH-029 decides what is served there, and URLs generated for such a response are built from
  `content_base_url`. It is never a base URL for UI, API, OIDC, mail or webhook links, so it MUST
  NOT appear in `server.additional_base_urls`: an entry with the content host's host name stops
  startup, as R-ARCH-029's origin rules require.
- **Endpoints.** These rules describe the default endpoint. A further endpoint has its own
  `base_url`, host set and trusted proxies; a request on it is answered with the base URL of its
  matched host, a host outside its set answers `421`, and a forwarded host counts only inside the
  set ([endpoints.md](endpoints.md) R-EP-003). With `server.endpoints.default.hosts` configured,
  the default endpoint checks its hosts the same way. Absolute URLs that leave a request (OIDC,
  mail, webhooks) are always built from `server.base_url`.
- **No runtime write operation.** The base URL is configuration (GitOps). It is shown in system
  information, and it cannot be changed through the API.
- **Cached metadata.** Metadata that embeds URLs MUST be cached in a form that does not depend on
  the base URL, and rewritten per response (INV-11, [repositories-and-proxy.md](repositories-and-proxy.md)).
  Otherwise one host name's URLs would leak into another host name's responses.

```yaml
server:
  base_url: https://trove.example.com              # example; required in production
  trusted_proxies: []                                # default (proposed); example ["10.0.0.0/8"]
  additional_base_urls: []                           # default (proposed); example ["https://npm.example.com"]
```

**Acceptance criteria:**

- With `base_url` set and no trusted proxies, `curl -H 'X-Forwarded-Host: evil.example' …/api/npm/npm-all/lodash`
  returns tarball URLs on `trove.example.com`.
- With the ingress controller's pod CIDR trusted and `https://npm.example.com` listed,
  `npm view lodash dist.tarball --registry https://npm.example.com/api/npm/npm-all/` prints a URL on
  `npm.example.com`. An unlisted host falls back to `base_url`.
- Each of `base_url: https://shared.example.com/trove`, `base_url: https://trove.example.com/?x=1`,
  `base_url: https://user@trove.example.com` and
  `additional_base_urls: ["https://npm.example.com/registry"]` stops `trove serve` with a non-zero
  exit and an error that names the key. `base_url: https://trove.example.com/` and
  `base_url: https://trove.example.com:8443` start.
- (P1) With `content_base_url: https://trove-content.example.net`,
  `additional_base_urls: ["https://trove-content.example.net"]` stops startup.

## Metrics

### R-OPS-006 · Metrics endpoint
Priority: **MVP** · Covers: OPS-011, API-033

Every replica MUST expose `GET /metrics` on the operations listener in the
[Prometheus text format 0.0.4](https://prometheus.io/docs/instrumenting/exposition_formats/). When
the request's `Accept` header asks for `application/openmetrics-text`, it uses the
[OpenMetrics 1.0](https://openmetrics.io/) format, which carries exemplars (R-OPS-018).

- Each replica exposes only its own process. Prometheus scrapes every `web` and `worker` pod,
  through a ServiceMonitor rendered by the chart ([ha-and-helm.md](ha-and-helm.md) R-HA-045), never
  through the client Service or the Ingress. A `backup` pod exposes `/metrics` only while its run
  lasts ([backup-and-restore.md](backup-and-restore.md) R-BAK-014); every backup fact that alerts
  need is also a gauge on the replicas, read from PostgreSQL, because a finished Job cannot be
  scraped.
- **Role and pool of a series** (Q-OPS-13). The process reports its role in
  `trove_build_info{role}`. Every other series gets `role` and, for the pools of
  [ha-and-helm.md](ha-and-helm.md) R-HA-062, `pool` as *target labels*: the chart's ServiceMonitor
  copies them from the pod's labels by relabeling. The process does not add them itself. On
  `trove_build_info` the exposed `role` and the target label collide; with `honor_labels: false`,
  the ServiceMonitor default, Prometheus keeps the target label and stores the exposed one as
  `exported_role` ([scrape configuration](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#scrape_config)).
  Both carry the same value on chart-deployed pods.
- Metrics stay readable without PostgreSQL and without Valkey: a scrape touches neither
  (Q-OPS-18).
- `metrics.auth: none` (default) relies on the NetworkPolicy of INV-13 to keep the listener
  cluster-internal (H-41). `metrics.auth: static` requires `Authorization: Bearer <value>`, where the
  value is read from `metrics.bearer_token_file` and compared in constant time.
- Scraping deliberately does not use trove tokens (TOK-014). Validating one needs PostgreSQL, and
  metrics must stay readable during exactly the database outage they are needed for.
- Metrics are not offered on the client listener.
- **On by default.** With the shipped defaults (`metrics.enabled: true`) every family of R-OPS-008
  is exposed, the database-pool families included; there is no second switch for database metrics.
  Only the per-query histograms are opt-in (`metrics.db_query_histograms`). With
  `metrics.enabled: false`, `/metrics` answers `404`.
- **Plain exposition.** Samples carry no timestamps, and the output contains no comment lines
  other than those of the format (`# HELP`, `# TYPE`, and in OpenMetrics `# UNIT` and `# EOF`).
- The default Go runtime and process collectors (`go_*`, `process_*`) are exposed unchanged. These
  are the only families without the `trove_` prefix.

```yaml
metrics:
  enabled: true                   # default (proposed)
  auth: none                      # default (proposed); none | static
  bearer_token_file: ""           # example /var/run/secrets/trove/metrics-token
```

**Acceptance criteria:**

- `curl -s http://127.0.0.1:8081/metrics | promtool check metrics` exits `0`. `promtool` is
  Prometheus' own linter.
- `curl -s -H 'Accept: application/openmetrics-text; version=1.0.0' http://127.0.0.1:8081/metrics | tail -1`
  prints `# EOF`.
- With `auth: static`, a scrape without the bearer value gets `401`. Scrapes keep succeeding while
  PostgreSQL is unreachable, and while Valkey is unreachable.
- In a Prometheus scraping through the chart's ServiceMonitor, `count by (role) (trove_build_info)`
  returns the replica count of each Deployment, and `trove_db_pool_connections` carries the target
  label `role` with the values `web` and `worker`.

### R-OPS-007 · Metric naming, labels and cardinality
Priority: **MVP** · Covers: OPS-012

Rules for every metric family, including families that format pages and other spec pages add:

- **Names** follow the [Prometheus naming practices](https://prometheus.io/docs/practices/naming/):
  - prefix `trove_`, then the subsystem;
  - base units in the name (`_seconds`, `_bytes`);
  - `_total` on counters;
  - format-specific families are named `trove_<format>_…`.
- **Types** match semantics. Counters are monotonic. Values read from PostgreSQL are gauges.
  Durations are histograms. A total never becomes a gauge and a median never becomes a counter.
- **Allowed labels.** These are the only labels permitted. Each has a bounded value set, defined by
  code or by configuration.

  | Label | Values |
  |---|---|
  | `listener` | `client`, `ops` |
  | `endpoint` | Configured endpoint names and `default`, at most 17 ([endpoints.md](endpoints.md) R-EP-001, R-EP-009); bounded by configuration; only on client-listener series |
  | `certificate` | 0-based position of a pair in an endpoint's certificate set ([endpoints.md](endpoints.md) R-EP-004); bounded by configuration |
  | `handler` | Registered handler names (`npm.packument`, `nuget.push`, `files.get`, `api.repositories.list`, `unmatched`, …) |
  | `method` | Standard HTTP methods; anything else becomes `OTHER` |
  | `code` | HTTP status code, on counters |
  | `code_class` | `1xx` to `5xx`, on histograms |
  | `format` | Registered format names |
  | `class` | `local`, `remote`, `virtual` |
  | `repo` | Repository key; capped, see below. Format pages that write `repository` in a label set (for example [formats/nuget.md](formats/nuget.md) R-NUGET-028) mean this label (cross-page) |
  | `subscription` | Webhook subscription key; capped, see below |
  | `issuer` | Configured CI issuer names ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-002); bounded by configuration |
  | `operation`, `kind`, `result`, `outcome`, `task`, `cache`, `state`, `status`, `direction`, `auth_method`, `query`, `name`, `reason`, `mode`, `family`, `label`, `source`, `check`, `action`, `trigger`, `via`, `limit` | Enumerations fixed in code; `action` takes the repository actions and capabilities of [authorization.md](authorization.md) (INV-9, R-AUTHZ-010) |
  | `area` | Change and signal areas registered in `internal/cluster/areas.go` ([architecture.md](architecture.md) R-ARCH-018) |
  | `db_pool` | `request`, `control`, `lease`; `backup` on backup pods ([architecture.md](architecture.md) R-ARCH-019) |
  | `command` | Valkey commands of the allowed command list ([architecture.md](architecture.md) R-ARCH-031) |
  | `mechanism` | Valkey uses of [architecture.md](architecture.md) R-ARCH-030 (`flight_lock`, `negative_cache`, `rate_limit`, `change_message`, `signal`, `probe`) |
  | `limiter` | Registered rate limiters ([architecture.md](architecture.md) R-ARCH-034) |
  | `key_id` | Key IDs of the stored-secret key ring and key IDs that sealed values still reference ([architecture.md](architecture.md) R-ARCH-021); bounded by configuration |
  | `version`, `revision`, `goversion`, `role` | Only on `trove_build_info` |

  Target labels that the scraper attaches (`role`, `pool`, `namespace`, `pod`, Q-OPS-13) are
  outside this list: the process never emits them on other families.
- **Forbidden labels.** User names, principal or token IDs, IP addresses and host names, request
  paths, package names, versions, trace IDs and error messages MUST NOT appear as label values.
  Valkey and PostgreSQL addresses are therefore never labels; the registry and system information
  show them (R-OPS-003, R-OPS-004).
- **Repository cap** (Q-OPS-10). A family with a `repo` label gives at most
  `metrics.label_limits.repository` distinct keys their own series per replica. Which keys get one
  is decided from the repository snapshot ([architecture.md](architecture.md) R-ARCH-018), so every
  replica decides the same: keys matching `metrics.repository_label_include` first, in the order of
  the patterns, then the other repositories in creation order (ascending repository ID). Every
  further key is reported as `repo="_other"` on every replica. The selection follows the snapshot,
  so a deleted repository frees its place on every replica within the change-area bound. The same
  rule applies to `subscription`, in subscription creation order. Each overflow increments
  `trove_metrics_label_overflow_total{family,label}`. Requests reach replicas at random
  (INV-12), so a selection made per replica, for example by first observation, would give each
  replica a different set and make `sum by (repo)` across replicas wrong.
- **Budget.** With default settings, 500 repositories and 50 subscriptions, one replica SHOULD
  expose fewer than 25,000 series. The series a Prometheus server stores grow with the replica
  count; an installation that scales `web` far out lowers `metrics.label_limits.repository` or
  narrows `repository_label_include` (Q-OPS-10).
- **Values read from PostgreSQL** (Q-OPS-18). Inventory, queue, backup-run and registry gauges, and
  the CI gauges `trove_ci_tokens_active{issuer}` and `trove_ci_signing_keys_age_seconds{issuer}`
  ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-020), are
  read from PostgreSQL by every `web` and `worker` replica, so every replica reports the same value.
  Dashboards and alerts aggregate them with `max`, not `sum`, and their help text says so. A replica
  evaluates them in the background once per `metrics.db_gauge_interval`, on its request pool and
  with a deadline, and a scrape returns the last evaluated values without touching PostgreSQL.
  `trove_metrics_db_gauges_last_success_timestamp_seconds` tells their age. The database load of
  these gauges thus depends on the replica count and the interval, never on the scrape rate or on
  how many Prometheus servers scrape.
- **Absent, not zero.** A family that describes a subsystem a role does not run
  ([architecture.md](architecture.md) R-ARCH-001) is absent on that role: the client-listener
  families on `worker`; on `web`, the families that count this process's own job work
  (`trove_jobs_running`, `trove_jobs_completed_total`, `trove_jobs_duration_seconds`,
  `trove_lease_held`, `trove_lease_lost_total`, `trove_webhook_deliveries_total` and the two
  webhook duration histograms). Gauges read from PostgreSQL
  describe the installation, not the process, and both roles export them.

```yaml
metrics:
  label_limits:
    repository: 500               # default (proposed)
    subscription: 100             # default (proposed)
  repository_label_include: []    # default (proposed); example ["npm-*", "nuget-*"]
  db_query_histograms: false      # default (proposed); see R-OPS-008
  native_histograms: false        # default (proposed); also expose native histograms
  db_gauge_interval: 30s          # default (proposed); evaluation interval of gauges read from PostgreSQL (Q-OPS-18)
```

**Acceptance criteria:**

- A load test against 600 repositories, sent at random over three `web` replicas, yields on every
  replica the same 500 distinct `repo` values plus `_other` in `trove_repository_requests_total`,
  and a non-zero overflow counter.
- With a scrape every second for 5 minutes from two Prometheus servers, `pg_stat_statements` shows
  the gauge queries of each replica at most once per `metrics.db_gauge_interval`.
- `curl -s …/metrics | grep -c '^trove_'` stays below 25,000 in the reference load test.
- A unit test over the registry rejects any family with a label outside the allowed list.

### R-OPS-008 · Metrics catalog
Priority: **MVP** · Covers: OPS-012, OPS-013

trove MUST expose the families below. Other spec pages MAY add families under R-OPS-007, and
SHOULD list them on their own pages.

HTTP duration histograms use the buckets 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
30, 60 and 300 s. The long tail exists because the duration includes body transfer.

<details>
<summary>Metric families</summary>

| Family | Type | Labels | Meaning |
|---|---|---|---|
| `trove_build_info` | gauge (=1) | `version`, `revision`, `goversion`, `role` | Build and process role (`web`, `worker`, `all`) of the running binary (Q-OPS-13) |
| `trove_ready` | gauge | — | 1 while `/readyz` answers `200`, degraded included (R-OPS-001) |
| `trove_http_requests_total` | counter | `listener`, `endpoint` (client listener), `handler`, `method`, `code` | Completed requests |
| `trove_http_request_duration_seconds` | histogram | `listener`, `endpoint` (client listener), `handler`, `method`, `code_class` | Time from first byte received to last byte sent |
| `trove_http_requests_in_flight` | gauge | `listener`, `endpoint` (client listener) | Requests in progress |
| `trove_http_received_bytes_total` | counter | `handler` | Request body bytes |
| `trove_http_sent_bytes_total` | counter | `handler` | Response body bytes |
| `trove_http_transfers_cut_total` | counter | `direction` | Transfers cut at the shutdown deadline ([architecture.md](architecture.md) R-ARCH-024, Q-ARCH-18) |
| `trove_repository_requests_total` | counter | `repo`, `format`, `class`, `operation` (download, upload, metadata, delete, search), `outcome` (success, not_found, denied, error) | Package-protocol and raw-file operations per repository |
| `trove_repository_transferred_bytes_total` | counter | `repo`, `format`, `class`, `direction` (in, out) | Bytes per repository |
| `trove_remote_cache_lookups_total` | counter | `repo`, `kind` (metadata, artifact), `result` (hit, miss, stale, negative_hit) | Pull-through cache outcomes (INV-11) |
| `trove_remote_upstream_requests_total` | counter | `repo`, `code` (status code or `error`) | Requests sent to an upstream |
| `trove_remote_upstream_request_duration_seconds` | histogram | `repo` | Upstream response time including body |
| `trove_remote_upstream_up` | gauge | `repo` | 1 while the upstream counts as reachable, 0 while it is treated as unreachable ([repositories-and-proxy.md](repositories-and-proxy.md)) |
| `trove_remote_collapsed_requests_total` | counter | `repo` | Cache misses that waited for an in-flight fetch instead of fetching themselves, whether the fetch ran on this replica or on another one (INV-11) |
| `trove_remote_source_network_unavailable_total` | counter | `repo` | Fetches answered `502` `source_network_unavailable` because this replica has no interface address inside the remote's `sourceNetwork` prefix ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-024, R-REPO-047, Q-REPO-9); a non-zero rate on some replicas only means their pods sit outside the prefix |
| `trove_flight_lock_bypass_total` | counter | `reason` (`valkey_unavailable`, `wait_timeout`, `draining`) | Fetches that went ahead without the cross-replica lock ([architecture.md](architecture.md) R-ARCH-033, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026, R-REPO-047) |
| `trove_flight_lock_lost_total` | counter | — | Holds whose lock key was gone or taken when extended (R-ARCH-033) |
| `trove_shadow_checks_total` | counter | `repo` (the local repository), `outcome` (`no-exposure`, `clear`, `shadowing-approved`, `refused`, `undetermined-approved`, `unavailable`) | Shadowing checks on first publishes of a name ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048) |
| `trove_blobstore_operations_total` | counter | `operation` (put, get, head, delete, list, multipart), `outcome` | Object-storage calls |
| `trove_blobstore_operation_duration_seconds` | histogram | `operation` | Object-storage latency |
| `trove_blobstore_transferred_bytes_total` | counter | `direction` | Bytes to and from object storage |
| `trove_blobstore_dedup_hits_total` | counter | — | Uploads whose content address already existed (INV-4) |
| `trove_spool_used_bytes`, `trove_spool_free_bytes` | gauge | — | Local temporary upload spool |
| `trove_storage_blobs`, `trove_storage_blob_bytes`, `trove_storage_logical_bytes` | gauge | — | From the storage summary (R-OPS-023); same value on every replica |
| `trove_repository_files`, `trove_repository_bytes` | gauge | `repo`, `format`, `class` | From the storage summary |
| `trove_repositories` | gauge | `format`, `class` | Number of repositories |
| `trove_storage_summary_age_seconds` | gauge | — | Age of the cached storage summary |
| `trove_storage_blobs_damaged_total` | counter | `status` (`missing`, `corrupt`) | Damaged blobs detected by the scrub or by download verification ([storage.md](storage.md) R-STOR-013, R-STOR-027) |
| `trove_storage_blobs_damaged` | gauge | `status` (`missing`, `corrupt`) | Current number of damaged blobs, from the latest storage summary (R-OPS-023, [storage.md](storage.md) R-STOR-028); same value on every replica |
| `trove_gc_runs_total` | counter | `outcome` | Garbage-collection runs of the tasks `gc` and `gc-full` ([storage.md](storage.md) R-STOR-025) |
| `trove_gc_deleted_blobs_total`, `trove_gc_reclaimed_bytes_total` | counter | — | Garbage-collection effect |
| `trove_gc_candidates_kept_total` | counter | — | Candidates kept because the reference check found a reference ([storage.md](storage.md) R-STOR-024) |
| `trove_gc_deleting_rows` | gauge | — | Blob rows in state `deleting` (from PostgreSQL); a value that does not fall back to zero means interrupted deletions are not finishing (R-STOR-024) |
| `trove_gc_last_success_timestamp_seconds` | gauge | — | End of the last successful run |
| `trove_gc_candidates_held_total` | counter | — | Collection candidates kept by a backup's hold ([backup-and-restore.md](backup-and-restore.md) R-BAK-007) |
| `trove_secrets_sealed_values` | gauge | `key_id` | Sealed values per key ID over every sealed column (from PostgreSQL; [api-and-ui.md](api-and-ui.md) R-API-049); a non-zero value under a retired key ID means re-encryption has not finished |
| `trove_secrets_key_rings_compatible` | gauge | — | 1 while every live replica's ring holds every key ID that is active on any live replica or referenced by a sealed value, else 0 (from the replica registry, R-OPS-003; R-API-049) |
| `trove_db_pool_connections` | gauge | `db_pool`, `state` (acquired, idle, constructing) | Connection state per pool of this replica; `db_pool` is `request` and `control` on every replica and `lease` on `worker` ([architecture.md](architecture.md) R-ARCH-019). Summed by the target label `role`, it shows the connections each role holds (Q-OPS-13) |
| `trove_db_pool_max_connections` | gauge | `db_pool` | Pool size |
| `trove_db_pool_acquire_duration_seconds` | histogram | `db_pool` | Wait for a connection; for `control` it is the headroom of the readiness-critical loops (Q-HA-20) |
| `trove_db_pool_empty_acquires_total` | counter | `db_pool` | Acquisitions that had to wait (pgxpool `EmptyAcquireCount`) |
| `trove_db_pool_canceled_acquires_total` | counter | `db_pool` | Acquisitions given up because the caller's deadline ended (pgxpool `CanceledAcquireCount`); the precursor of `503` `database_timeout` |
| `trove_db_pool_connections_opened_total` | counter | `db_pool` | New connections (pgxpool `NewConnsCount`); a jump after a failover or a credential rotation is a reconnect storm |
| `trove_db_retries_total` | counter | `kind` | Retried transactions ([ha-and-helm.md](ha-and-helm.md) R-HA-009) |
| `trove_db_pool_resets_total`, `trove_db_commit_unknown_total` | counter | — | Pool resets and commits with unknown outcome (R-HA-009) |
| `trove_db_slow_queries_total` | counter | `query` (name of the query in code) | Queries slower than `metrics.slow_query_threshold` |
| `trove_db_query_duration_seconds` | histogram | `query` | Only with `metrics.db_query_histograms: true` |
| `trove_jobs_queued` | gauge | `kind` | Queued jobs (from PostgreSQL) |
| `trove_jobs_oldest_queued_age_seconds` | gauge | `kind` | Age of the oldest queued job; the primary alerting signal for a stuck queue. Read from PostgreSQL by `web` replicas too, so it keeps reporting while no `worker` runs |
| `trove_jobs_running` | gauge | `kind` | Jobs this replica executes (`worker`) |
| `trove_jobs_completed_total` | counter | `kind`, `outcome` (succeeded, failed, retried, cancelled, released) | Job executions; `released` counts jobs handed back at shutdown without counting the attempt (R-ARCH-016) |
| `trove_jobs_duration_seconds` | histogram | `kind` | Job execution time |
| `trove_task_last_success_timestamp_seconds` | gauge | `task` | Last successful run of a scheduled task (R-OPS-019) |
| `trove_lease_held` | gauge | `name` | 1 on the `worker` replica that holds a singleton lease (INV-12) |
| `trove_lease_lost_total` | counter | `task` | Leases lost while held ([ha-and-helm.md](ha-and-helm.md) R-HA-006) |
| `trove_health_check_passing` | gauge | `name` (check name) | 1 while the check passes, gating or reported (R-OPS-001); `valkey` and `objectStore` at 0 mean degraded, not unready |
| `trove_cluster_poll_last_success_timestamp_seconds` | gauge | — | End of the last successful change-log poll; the `invalidation` check compares its age with `cluster.epoch_max_staleness` ([architecture.md](architecture.md) R-ARCH-018) |
| `trove_cluster_changes_applied_total` | counter | `area`, `source` (`poll`, `message`) | Changes applied from the change log, and early evictions from Valkey messages; a `message` count near the `poll` count shows the fast path working |
| `trove_cluster_signals_published_total`, `trove_cluster_signals_dropped_total` | counter | `area` | Signals sent to Valkey after the coalescer, and signals dropped while Valkey is unavailable (R-ARCH-018) |
| `trove_cache_requests_total` | counter | `cache`, `result` (hit, miss) | In-process caches ([architecture.md](architecture.md)) |
| `trove_cache_entries` | gauge | `cache` | Entries per in-process cache |
| `trove_cache_flushes_total` | counter | `cache`, `reason` (`change`, `change_log_gap`, `unknown_payload`, `epoch_regressed`, `admin`, `size`) | Cache flushes: a change-area entry, a gap in the change log, a payload version this binary does not know, an epoch that went backwards after a failover or restore (R-ARCH-018, [ha-and-helm.md](ha-and-helm.md) R-HA-004), the cache flush of R-OPS-047, the size bound |
| `trove_valkey_available` | gauge | — | 1 while the `valkey` check passes on this replica ([ha-and-helm.md](ha-and-helm.md) R-HA-057, R-OPS-045) |
| `trove_valkey_circuit_open` | gauge | — | 1 while this replica's Valkey circuit is open (R-HA-057) |
| `trove_valkey_sentinels_reachable` | gauge | — | Sentinels that answered this replica's last query (R-HA-057) |
| `trove_valkey_primary_changes_total` | counter | — | Primary changes this replica's client followed: failovers seen (R-HA-057) |
| `trove_valkey_pubsub_subscribed` | gauge | — | 1 while the Pub/Sub connection is subscribed on the current primary (R-OPS-045) |
| `trove_valkey_primary_views` | gauge | — | Distinct non-null `valkey.primary` values over the live replicas of the registry (from PostgreSQL, R-OPS-003); above 1 for longer than a reconciliation means replicas write to different primaries (R-OPS-045) |
| `trove_valkey_command_duration_seconds` | histogram | `command` | Round trip of a Valkey command or pipeline ([architecture.md](architecture.md) R-ARCH-031) |
| `trove_valkey_errors_total` | counter | `kind` (`timeout`, `connection`, `readonly`, `auth`, `tls`, `oom`, `no_primary`) | Failed Valkey calls (R-HA-057); `no_primary` counts Sentinel queries that named no primary for `valkey.sentinel.primary_name` (cross-page to R-HA-057) |
| `trove_valkey_degraded_total` | counter | `mechanism` | Calls that ran their fallback instead of Valkey (R-ARCH-031) |
| `trove_valkey_cache_lookups_total` | counter | `cache` (`negative`), `result` (`hit`, `miss`, `rejected`, `unavailable`) | Lookups in Valkey after an L1 miss; `rejected` counts entries that reader validation refused ([architecture.md](architecture.md) R-ARCH-030). The L1 is counted in `trove_cache_requests_total{cache="negative_l1"}` (R-OPS-045) |
| `trove_ratelimit_decisions_total` | counter | `limiter`, `mode` (`cluster`, `fallback`, `replica`), `outcome` (`admitted`, `refused`) | Rate-limit checks of the limiter of [architecture.md](architecture.md) R-ARCH-034 |
| `trove_ratelimit_fallback` | gauge | `limiter` | 1 while a cluster-scoped limiter answers from per-replica shares (R-ARCH-034) |
| `trove_auth_attempts_total` | counter | `auth_method` (bearer, basic, nuget_apikey, session, oidc_login, ci_exchange), `outcome` (success, invalid, expired, revoked, denied) | Authentication outcomes |
| `trove_auth_cache_bypassed_total` | counter | `reason` (`changes_stale`) | Credential lookups sent to PostgreSQL instead of the in-process caches because the replica's last change-log poll is older than `auth.revocation_bound` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023); a rate without a database incident means a replica's poll lags |
| `trove_auth_throttle_exempt_total` | counter | `via` (`cache`, `marker`) | Requests of recently validated credentials exempted from the credential-failure throttle, by the replica's own cache or by a Valkey exemption marker ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038); a marker only lets a credential be validated, never authenticates it |
| `trove_auth_session_touch_failures_total` | counter | — | Session `last_seen_at` writes that failed; they never fail the request ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-010, R-AUTH-041) |
| `trove_auth_ratelimited_total` | counter | `limit` (`credential_failures`, `flow_starts`, `device_code_failures`, `break_glass_lockout`, `break_glass_verifications`, `pending_flows`) | Authentication requests refused by one of the limits of [identity-and-authentication.md](identity-and-authentication.md) (R-AUTH-014, R-AUTH-038) |
| `trove_authz_decisions_total` | counter | `outcome` (allow, deny), `reason` (the reason codes of [authorization.md](authorization.md) R-AUTHZ-029), `action` | Authorization decisions (INV-9); every answer counts, also those whose audit event is deduplicated |
| `trove_authz_epoch` | gauge | — | `authz` epoch of the grant index this replica has installed ([authorization.md](authorization.md) R-AUTHZ-028); it never decreases on a replica |
| `trove_authz_index_staleness_seconds` | gauge | — | Time since the installed grant index was last confirmed by a change-log poll or its own build (R-AUTHZ-028); it never grows because Valkey is unavailable |
| `trove_authz_index_rebuilds_total` | counter | `trigger` (`startup`, `poll`, `message`, `flush`, `safety`, `min_epoch`) | Grant-index builds and why they ran (R-AUTHZ-028, R-AUTHZ-032); forged Valkey messages do not raise `message` |
| `trove_authz_stale_waits_total` | counter | `outcome` (`decided`, `refused`) | Decisions that waited for a fresh grant index, and those that ended `503` `authz_stale` (R-AUTHZ-028) |
| `trove_authz_min_epoch_total` | counter | `outcome` (`current`, `waited`, `unknown`, `timeout`) | Requests that named a minimum `authz` epoch, by how they were served ([authorization.md](authorization.md) R-AUTHZ-032) |
| `trove_audit_events_total` | counter | `kind` (category), `outcome` | Audit events recorded |
| `trove_audit_events_dropped_total` | counter | — | Access events lost to a full buffer (R-OPS-015) |
| `trove_webhook_deliveries_total` | counter | `subscription`, `outcome` (succeeded, failed, retrying, abandoned) | Delivery attempts, on `worker` replicas |
| `trove_webhook_delivery_duration_seconds` | histogram | — | Receiver response time |
| `trove_webhook_render_duration_seconds` | histogram | — | Time to prepare a request (template, signing) |
| `trove_webhook_pending_deliveries` | gauge | — | Deliveries waiting or retrying (from PostgreSQL) |
| `trove_events_undispatched` | gauge | — | Outbox rows not yet dispatched (R-OPS-035) |
| `trove_tracing_spans_dropped_total` | counter | — | Spans lost to a full export queue |
| `trove_outbound_requests_total` | counter | `kind` (purpose), `code` | Requests sent to other systems, by purpose and result code (Q-OPS-26) |
| `trove_outbound_request_duration_seconds` | histogram | `kind` (purpose) | Time of an outbound request including the body, by purpose |
| `trove_outbound_egress_denied_total` | counter | `kind` (purpose) | Connections refused by the egress address policy, by purpose |
| `trove_metrics_label_overflow_total` | counter | `family`, `label` | Values folded into `_other` |
| `trove_metrics_db_gauges_last_success_timestamp_seconds` | gauge | — | End of the last successful evaluation of the gauges read from PostgreSQL (Q-OPS-18) |
| `trove_maintenance_mode` | gauge | `mode` | 1 for the current mode (R-OPS-024) |
| `trove_backup_last_success_timestamp_seconds`, `trove_backup_last_failure_timestamp_seconds`, `trove_backup_expected_interval_seconds`, `trove_backup_last_duration_seconds`, `trove_backup_last_database_bytes`, `trove_backup_last_blob_bytes`, `trove_backup_last_copied_bytes`, `trove_backup_running_seconds`, `trove_backup_pinned_blobs`, `trove_backup_snapshot_open_seconds`, `trove_backup_source_blobs_damaged`, `trove_backup_last_verify_timestamp_seconds`, `trove_backup_last_verify_findings` | gauge | — | Backup runs, read from `backup_run` by every `web` and `worker` replica under the rule of R-OPS-007; [backup-and-restore.md](backup-and-restore.md) R-BAK-014 owns their semantics. `trove_backup_expected_interval_seconds` is `backup.expected_interval` from the replica's configuration, the threshold source of `TroveBackupStale` |
| `trove_backup_running` | gauge | `operation` (`create`, `delete`), `state` | 1 for the operation and state of the active run with a valid lease, if any (R-BAK-014) |
| `trove_backup_gc_hold` | gauge | `mode` (`all`, `pinned`) | 1 while a valid garbage-collection hold of that kind exists (R-BAK-007, R-BAK-014) |
| `trove_backup_copied_objects_total`, `trove_backup_copied_bytes_total` | counter | `mode` | Exported by the backup pod while its run lasts (R-BAK-014) |
| `trove_backup_check_ok` | gauge | `check` | Result of the platform backup checks ([ha-and-helm.md](ha-and-helm.md) R-HA-023), written by the `backup-check` task |
| `trove_revocation_journal_pending` | gauge | — | Revocation-journal backlog ([ha-and-helm.md](ha-and-helm.md) R-HA-054) |
| `trove_nuget_push_replayed_total` | counter | `repo` | Pushes of the identical bytes of a stored NuGet version, answered as accepted without a new event ([formats/nuget.md](formats/nuget.md) R-NUGET-006, R-NUGET-028, Q-NUGET-6; that page's label `repository` is `repo` here) |
| `trove_nuget_symbol_index_lag_seconds` (P1) | gauge | — | Age of the oldest symbol package not yet indexed (R-NUGET-021, R-NUGET-028) |
| `trove_nuget_symbol_index_failures_total` (P1) | counter | `repo`, `reason` | Symbol packages whose indexing ended with an error (R-NUGET-021, R-NUGET-028) |

</details>

**Acceptance criteria:**

- After a cold `npm install express --registry https://trove.example.com/api/npm/npm-all/`,
  `trove_remote_cache_lookups_total{repo="npm-remote",kind="artifact",result="miss"}` is greater
  than zero.
- After `npm cache clean --force` and a second install in a fresh directory, the `result="hit"`
  series of the same family grows instead.
- `trove_jobs_oldest_queued_age_seconds` rises on the `web` replicas when the `worker` Deployment is
  scaled to 0 and a task is triggered.
- With Valkey stopped, `trove_valkey_available` is 0, `trove_ratelimit_fallback` is 1 for every
  cluster-scoped limiter and `trove_valkey_degraded_total` grows on every replica, while
  `trove_ready` stays 1.
- A unit test over the registry fails on a family of this table that a role exports although the
  role does not run the subsystem (R-OPS-007, "absent, not zero").

### R-OPS-045 · Valkey and coordination observability
Priority: **MVP** · Covers: trove-only

Valkey is part of every production topology (INV-12), and its outage must be visible although it
never makes a replica unready. [ha-and-helm.md](ha-and-helm.md) R-HA-057 owns the degraded-mode
contract and [architecture.md](architecture.md) R-ARCH-031 the client; this requirement fixes what
an operator sees of them. The families are in R-OPS-008.

| Question | Signal |
|---|---|
| Can this replica use Valkey? | `trove_valkey_available`, `trove_valkey_circuit_open`, the `valkey` check (R-OPS-001) |
| How fast is it? | `trove_valkey_command_duration_seconds{command}`, with the buckets 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25 and 0.5 s, because no call waits longer than `valkey.command_timeout` (500 ms by default) |
| What fails? | `trove_valkey_errors_total{kind}` |
| Which failovers did it follow? | `trove_valkey_primary_changes_total`; the registry's `valkey.primary` and `trove_valkey_primary_views` (R-OPS-003) |
| Is the fast path of the change log up? | `trove_valkey_pubsub_subscribed`, `trove_cluster_changes_applied_total{source="message"}` |
| What does the degradation cost? | `trove_valkey_degraded_total{mechanism}`, `trove_flight_lock_bypass_total`, `trove_ratelimit_fallback`, `trove_cluster_signals_dropped_total`, and the upstream request rate in `trove_remote_upstream_requests_total` |
| Does the shared negative cache pay off? | `trove_valkey_cache_lookups_total{cache="negative"}` |
| Can the Sentinels still fail over? | `trove_valkey_sentinels_reachable` |

- **Hit ratio.**
  `sum(rate(trove_valkey_cache_lookups_total{cache="negative",result="hit"}[5m])) / sum(rate(trove_valkey_cache_lookups_total{cache="negative",result=~"hit|miss"}[5m]))`
  is the share of L1 misses that Valkey answered. `rejected` counts entries that reader validation
  refused: a creation time outside the window, or a newer covering invalidation
  ([architecture.md](architecture.md) R-ARCH-030). A steady `rejected` rate without recent
  invalidations points at writes from outside trove (H-49).
- **Transitions are logged, failed calls are counted.** One `WARN` record (`component: cluster`)
  when the `valkey` check turns degraded, with the reason, and one `INFO` record when it passes
  again, with the duration. One `INFO` record per primary change, with the old and the new address,
  so that a failover shows up in the log stream next to the requests it slowed. One `ERROR` record
  for a configuration error (R-OPS-001). A failed call writes no record, so an outage adds two
  records per replica to the log, not one per request.
- **No audit events.** Valkey availability is operational state, not an actor's action.
- **The server side is not trove's view.** trove's ACL user may run only the commands of
  [architecture.md](architecture.md) R-ARCH-031, which exclude `INFO`, so trove reports what its
  client sees. Memory, evictions, replication offsets and Sentinel state come from an exporter
  that whoever runs Valkey runs next to it, for example the Prometheus exporter for Valkey and Redis
  ([redis_exporter](https://github.com/oliver006/redis_exporter)).
  [ha-and-helm.md](ha-and-helm.md) R-HA-056 names the server settings worth watching (`maxmemory`,
  the eviction policy).
- **Deleting trove's keys is always safe.** Every key under `valkey.key_prefix` is disposable
  (INV-3, R-ARCH-030). An operator who suspects poisoned entries may delete the keys of the current
  keyspace (`<key_prefix><keyspace>:*`, the keyspace from R-OPS-004) with an administrative
  credential, or flush the database where no other application or installation shares it
  (R-ARCH-030's shared prefixes); trove only loses cache warmth. trove itself never deletes keys by
  pattern (R-OPS-047, Q-OPS-16).

Acceptance criteria, under the load of [ha-and-helm.md](ha-and-helm.md) R-HA-052 on the local
profile:

- `SENTINEL FAILOVER trove`: `trove_valkey_primary_changes_total` rises by one on every replica;
  each replica writes one `INFO` record that names the old and the new primary;
  `trove_valkey_pubsub_subscribed` is 1 again within `valkey.sentinel.refresh_interval` plus 1 s;
  `trove_valkey_primary_views` is back to 1 within `system.heartbeat_interval` plus
  `metrics.db_gauge_interval`.
- Valkey scaled to 0 for 5 minutes: `trove_valkey_available` is 0 on every replica,
  `trove_valkey_degraded_total` grows for `flight_lock`, `negative_cache` and `rate_limit`, and each
  replica writes exactly one `WARN` record and, after the return, one `INFO` record for the
  transition.
- A fault proxy delays every Valkey command by 2 s: `trove_valkey_errors_total{kind="timeout"}`
  grows, the circuit opens, and no observation of `trove_valkey_command_duration_seconds` exceeds
  `valkey.command_timeout` plus 50 ms.
- After an upstream `404` for one path of a remote repository, requests for that path on the other
  replicas increment `trove_valkey_cache_lookups_total{result="hit"}`. A negative entry written into
  Valkey with a creation time in the future increments `result="rejected"`.
- `valkey-cli --scan --pattern 'trove:<keyspace>:*' | xargs valkey-cli del`, run with an
  administrative credential in the middle of the load, makes no client request fail.

### R-OPS-046 · Operational signals for availability, horizontal scale and backup
Priority: **MVP** · Covers: trove-only

The owner's constraints (HA PostgreSQL, HA Valkey with Sentinel and S3; load beyond one replica;
node drains without outage; a backup job) each name a way trove can fail. This requirement maps
each of them to the signal that shows it and the alert that reports it.
[ha-and-helm.md](ha-and-helm.md) R-HA-045 renders the alerts as a PrometheusRule; this page owns
the metrics they read, and [backup-and-restore.md](backup-and-restore.md) R-BAK-014 owns the
backup alerts' expressions.

| Constraint | What goes wrong | Signal | Alert (R-HA-045) |
|---|---|---|---|
| HA PostgreSQL | Primary unreachable beyond the retry budget | `trove_db_retries_total{kind="connection"}`, `trove_health_check_passing{name="database"}` | `TroveDatabaseUnreachable` |
| HA PostgreSQL | Change log stalled on a replica | `trove_cluster_poll_last_success_timestamp_seconds`, the `invalidation` check | `TroveReplicasNotReady` once a role is short |
| Load beyond one replica | Request pool saturated | `trove_db_pool_acquire_duration_seconds{db_pool="request"}`, `trove_db_pool_canceled_acquires_total` | `TroveDatabasePoolSaturated`; its expression selects `db_pool="request"`, so the control connection's waits stay with the next row (cross-page R-HA-045) |
| Load beyond one replica | Readiness-critical loops wait for the control connection ([ha-and-helm.md](ha-and-helm.md) Q-HA-20) | `trove_db_pool_acquire_duration_seconds{db_pool="control"}` | `TroveControlConnectionWaits` (new): p95 above 1 s for 5 minutes, before the `invalidation` and `database` checks start failing on every replica |
| Load beyond one replica | Connections growing with the replica count (INV-12) | `sum by (role, db_pool) (trove_db_pool_connections)` against the budget of [ha-and-helm.md](ha-and-helm.md) R-HA-016 | none: the migration Job's budget check refuses a deployment that would exceed it |
| Load beyond one replica | Job queue falling behind | `trove_jobs_oldest_queued_age_seconds` | `TroveJobsStuck` |
| HA Valkey with Sentinel | Valkey unavailable for every replica | `trove_valkey_available` | `TroveValkeyUnavailable` |
| HA Valkey with Sentinel | Valkey unavailable for one replica | `trove_valkey_available` | `TroveValkeyUnavailableSingleReplica` |
| HA Valkey with Sentinel | Sentinel majority lost | `trove_valkey_sentinels_reachable` | `TroveValkeySentinelsDegraded` |
| HA Valkey with Sentinel | Failovers repeating (Q-OPS-15) | `trove_valkey_primary_changes_total` | `TroveValkeyFailoverFlapping` (new): `max(increase(trove_valkey_primary_changes_total[1h])) > 2` |
| HA Valkey with Sentinel | Replicas writing to different primaries (Q-OPS-15) | `trove_valkey_primary_views` | `TroveValkeyPrimaryDisagreement` (new): `max(trove_valkey_primary_views) > 1` for 5 minutes |
| HA S3 | Store unreachable | `trove_storage_s3_circuit_open` | `TroveObjectStoreUnreachable`, `TroveObjectStoreUnreachableSingleReplica` |
| Drain without outage | A role below two ready replicas | kube-state-metrics, `trove_ready` | `TroveReplicasNotReady` |
| Drain without outage | Transfers cut at shutdown | `trove_http_transfers_cut_total` | none: the revisit trigger of [architecture.md](architecture.md) Q-ARCH-18 |
| Drain without outage | Jobs handed back at shutdown | `trove_jobs_completed_total{outcome="released"}` | none: a release is the designed drain path |
| Backup job | Last run failed | `trove_backup_last_failure_timestamp_seconds` | `TroveBackupFailed` |
| Backup job | Backup overdue: its age exceeds the expected interval | `time() - max(trove_backup_last_success_timestamp_seconds)` against `trove_backup_expected_interval_seconds` | `TroveBackupStale` (R-BAK-014) |
| Backup job | A run hangs | `trove_backup_running_seconds` | `TroveBackupRunningLong` |
| Backup job | Garbage collection held by a run | `trove_backup_gc_hold{mode="all"}` | `TroveBackupGlobalHoldLong` |
| Backup job | Platform protection missing | `trove_backup_check_ok` | `TroveBackupCheckFailed` |
| Backup job | A backup is complete but not whole: its snapshot references blobs that were missing or corrupt in the source | `trove_backup_source_blobs_damaged` | `TroveBackupSourceDamage` (R-BAK-014) |
| Backup job | A stored backup no longer matches its manifest | `trove_backup_last_verify_findings` | `TroveBackupVerifyFindings` (R-BAK-014) |

- **Metrics only.** Every alert reads families of R-OPS-008, families of the owning pages (the
  object-store circuit `trove_storage_s3_circuit_open` of [storage.md](storage.md)), or
  kube-state-metrics where the row says so. No alert parses logs.
- **`max` over gauges read from PostgreSQL** (R-OPS-007), so the alert does not change with the
  replica count.
- **Maintenance stays quiet.** The designed maintenance paths fire no alert: a node drain of
  [ha-and-helm.md](ha-and-helm.md) R-HA-059 (variants A to E), a planned Valkey failover, a backup
  run interrupted by a drain (R-BAK-012), and a rolling upgrade. The `for` durations are chosen for
  that; Valkey degradation during an unplanned failover lasts `down-after-milliseconds` plus a few
  seconds, below `TroveValkeyUnavailable`'s 2 minutes.
- **One name and one threshold for the backup age.** This page uses R-BAK-014's alert
  `TroveBackupStale` with its threshold of 1.5 × `backup.expected_interval`, and
  [ha-and-helm.md](ha-and-helm.md) R-HA-045 lists it the same way. Its earlier name
  `TroveBackupTooOld`, with 1.1 ×, is retired.

Acceptance criteria:

- `promtool test rules` holds one test per row that names an alert: the alert fires on the
  row's failure input and stays silent on a healthy input.
- The drain test of R-HA-059, variants A to E, and the planned failover of R-HA-057 end without a
  firing alert in the Prometheus that scrapes the profile.
- Each failure scenario of R-HA-052 that a row names fires that row's alert: PostgreSQL outage,
  Valkey outage, one replica's Valkey path blocked, three Valkey failovers within an hour, a backup
  run that exits `1`, and the backup CronJob suspended with `backup.expected_interval: 1h`.

## Logs

### R-OPS-009 · Log stream and record format
Priority: **MVP** · Covers: OPS-017, OPS-018

trove MUST write logs only to standard output, one JSON object per line, through Go's
[`log/slog`](https://pkg.go.dev/log/slog) (INV-16). It writes no log files, so it does no rotation
and no archiving: collecting, rotating and shipping logs is the platform's job (HA-055,
[ha-and-helm.md](ha-and-helm.md)). The field `log` separates four record streams in the one
output.

| `log` | Content | Requirement |
|---|---|---|
| `app` | Application messages of every component | this one |
| `request` | One record per completed HTTP request | R-OPS-010 |
| `upstream` | One record per outbound request: remote repositories, webhook deliveries, OIDC discovery and JWKS fetches | R-OPS-011 |
| `audit` | A copy of every recorded audit event | R-OPS-015 |

Fields present in every record:

| Field | Content |
|---|---|
| `time` | RFC 3339, UTC, millisecond precision |
| `level` | `DEBUG`, `INFO`, `WARN`, `ERROR` |
| `msg` | Message text; a constant per call site, with variable data in attributes |
| `log` | Stream, see above |
| `component` | Registered component name (R-OPS-012) |
| `replica` | Pod name: the replica, or the pod of a backup, restore or migration Job |
| `role` | `web`, `worker` or `all` for `trove serve`; `backup` for `trove backup`; `migrate` and `admin` for those commands |
| `trace_id`, `span_id` | Present whenever the record belongs to a request, a job or a backup run (R-OPS-018) |
| `error` | Error text, when the record reports an error |
| `source` | `file:line`, only with `log.add_source: true` |

- **Attribute keys** are `snake_case` and stable. A renamed key is a breaking change for log
  pipelines, and the release notes MUST name it.
- **`log.format: text`** switches to slog's text handler for local development. Field names stay
  the same.
- **The ring buffer.** Every replica keeps its last `log.ring_buffer_records` records in memory,
  after redaction. Support bundles (R-OPS-027) take their logs from it, because trove itself
  holds no log files. The buffer dies with its pod; a drained or deleted replica's records exist
  only in the platform's log stack from then on.

```yaml
log:
  format: json                    # default (proposed); json | text
  level: info                     # default (proposed); debug | info | warn | error
  levels: {}                      # default (proposed); example {proxy: debug, auth: warn}
  add_source: false               # default (proposed)
  ring_buffer_records: 5000       # default (proposed)
```

**Acceptance criteria:** `kubectl -n trove logs deploy/trove-web | jq -e 'has("time") and has("level") and has("log") and has("replica") and has("role")'`
succeeds for every line, and so does the same check over `deploy/trove-worker` and over the pod of
a completed backup Job, whose records carry `"role":"backup"`.

### R-OPS-010 · Request log
Priority: **MVP** · Covers: OPS-019

Every completed request on the client listener MUST produce one `log: request` record at `INFO`.
The field set is fixed. A field that does not apply is `null`, and an unknown length is `-1`, so
consumers can rely on every key being present.

<details>
<summary>Fields of the request log record (25)</summary>

| Field | Content |
|---|---|
| `time` | Completion time |
| `trace_id` | Trace ID; also sent to the client as `X-Request-Id` |
| `listener` | `client` or `ops` |
| `endpoint` | Name of the endpoint whose listener accepted the connection ([endpoints.md](endpoints.md) R-EP-002); `null` on the ops listener |
| `host` | Effective host of the request, at most 255 bytes ([endpoints.md](endpoints.md) R-EP-003); `null` on the ops listener |
| `remote_addr` | Client address after the trusted-proxy rule (R-OPS-005) |
| `peer_addr` | TCP peer address |
| `method` | HTTP method |
| `path` | URL path, plus a query string in which every parameter of `log.request.redact_query_params` is redacted |
| `handler` | Registered handler name (the same values as the metrics label) |
| `status` | Response status |
| `request_bytes` | Body bytes received, `-1` if unknown |
| `response_bytes` | Body bytes sent, `-1` if unknown |
| `duration_ms` | Processing time, with microsecond fraction |
| `user_agent` | Client user agent |
| `principal` | User name, service-account name, `ci:<CI issuer name>/<mapping name>` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-009) or `anonymous` |
| `principal_type` | `user`, `service_account`, `ci`, `breakglass`, `anonymous` |
| `principal_id` | Opaque principal ID, `null` for anonymous callers |
| `token_id` | ID of the token used (never the token), `null` if none |
| `auth_method` | `bearer`, `basic`, `nuget_apikey`, `session`, `none` |
| `repo`, `format` | Repository key and format, if the request addressed one |
| `cache` | `hit`, `miss`, `stale`, `negative_hit` or `null` (remote and virtual reads) |
| `deny_reason` | Short code for `401`/`403` responses (`no_credentials`, `invalid_token`, `not_permitted`, …), else `null` |
| `limiter` | For a `429`: the name of the rate limiter that refused the request ([architecture.md](architecture.md) R-ARCH-034); else `null` |
| `limit_mode` | For a `429`: `cluster` (the cluster-wide counter in Valkey), `fallback` (the per-replica share while Valkey is unavailable) or `replica` (a limit that protects this replica); else `null` |

</details>

Requests reach replicas at random (INV-12), so one client's requests are spread over the records
of several replicas; `trace_id`, `principal_id` and `token_id` join them, never `replica`.

Requests on the operations listener are logged only with `log.request.ops_listener: true`. Request
and response bodies and headers other than `User-Agent` are never logged (R-OPS-013).

```yaml
log:
  request:
    enabled: true                 # default (proposed)
    ops_listener: false           # default (proposed)
    redact_query_params: [token, access_token, password, secret, sig, signature, X-Amz-Signature, X-Amz-Credential, X-Amz-Security-Token]   # default (proposed)
```

**Acceptance criteria:**

- `npm view express version --registry https://trove.example.com/api/npm/npm-all/` produces a
  record with `"handler":"npm.packument"`, `"repo":"npm-all"` and `"status":200`.
- A `dotnet nuget push … --api-key <token>` produces a record whose `token_id` is set and which
  does not contain the token.

### R-OPS-011 · Upstream request log
Priority: **MVP** · Covers: OPS-020

Every request trove sends to another system MUST produce one `log: upstream` record when it
completes or fails. The same conventions as R-OPS-010 apply.

| Field | Content |
|---|---|
| `time`, `trace_id` | Completion time; trace of the request or job that caused the call |
| `purpose` | `remote` (pull-through fetch, or the probe of a shadowing check), `webhook`, `oidc_discovery`, `jwks`, `mail` |
| `repo` | Remote repository key (`remote`), else `null` |
| `subscription` | Webhook subscription key (`webhook`), else `null` |
| `method` | HTTP method |
| `url` | Target URL with user information removed and query parameters redacted (R-OPS-013) |
| `status` | Upstream status, `0` on a transport error |
| `error` | Transport or TLS error text, else `null` |
| `request_bytes`, `response_bytes` | `-1` if unknown |
| `duration_ms` | Total time including body |
| `attempt` | Attempt number for retried calls, else `1` |
| `on_behalf_of` | Principal whose request caused the call, or `system` for jobs |
| `collapsed_waiters` | Number of concurrent misses on this replica that this fetch served (INV-11). Waiters on other replicas wait through the Valkey flight lock ([architecture.md](architecture.md) R-ARCH-033) and are not counted here; they send no upstream request, answer with the `collapsed` parameter of `Cache-Status` ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-047) and count in `trove_remote_collapsed_requests_total` on their own replica |

[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-047 adds the fields `flight_role`
and `cache_status` to records with `purpose: remote`. `flight_role` is `leader`, `follower` or
`waiter` for pull-through fetches, and `probe` for the upstream request of a shadowing check
(R-REPO-048), which stores nothing.

```yaml
log:
  upstream:
    enabled: true                 # default (proposed)
```

**Acceptance criteria:** a cold `npm install left-pad --registry …/npm-all/` produces at least one
`"purpose":"remote"` record with `"repo":"npm-remote"`, and the `trace_id` of the client request
that caused it.

### R-OPS-012 · Log levels and temporary overrides
Priority: **P1** · Covers: OPS-025, OPS-026, ADMAPI-080

Every log call belongs to a registered component. The registry covers at least `api`, `audit`,
`auth`, `authz`, `config`, `db`, `gc`, `http`, `jobs`, `metrics`, `npm`, `nuget`, `proxy`,
`storage`, `tracing` and `webhooks`; other pages add their own. Each component has a level made of
three parts:

1. `log.levels[<component>]` if set, else `log.level`;
2. raised or lowered by an active temporary override;
3. evaluated per record through a `slog.LevelVar` per component, so a change needs no restart.

API:

| Method and path | Request | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/log-levels` | — | `200` configured levels, active override, component list | `system.read` |
| `PUT /api/v1/system/log-levels/override` | `{"levels":{"auth":"debug","proxy":"debug"},"duration":"15m"}` | `200` stored override with `expiresAt` | system administrator |
| `DELETE /api/v1/system/log-levels/override` | — | `204` | system administrator |

- **One override at a time.** A new override replaces the active one. Unlike the catalog
  behaviour, trove does not require clearing it first.
- **Validation.** An unknown component or level yields `400`, so a typo is never accepted
  silently. The duration must lie between `1m` and `log.override_max_duration`.
- **Propagation.** The override is stored in `system_state` (R-OPS-024) and written to the change
  area `system_state` ([architecture.md](architecture.md) R-ARCH-018), so it is in force on every
  `web` and `worker` replica within `cluster.epoch_poll_interval` plus 1 s, with or without Valkey.
  A replica that starts later reads it at start-up, and each replica reverts it locally at
  `expiresAt`, so no timer needs coordinating. `trove backup` and `trove migrate` use the configured
  levels.
- **Audit.** Each set and clear writes `system.log_levels_changed`.
- **Redaction.** Debug output follows R-OPS-013 like every other level.

```yaml
log:
  override_max_duration: 24h      # default (proposed)
```

**Acceptance criteria:** with three `web` and two `worker` replicas, `PUT …/override` with
`{"levels":{"proxy":"debug"},"duration":"2m"}` makes `"component":"proxy","level":"DEBUG"` records
appear on all five within 5 s, also with Valkey stopped. They stop after 2 min without further
calls.

### R-OPS-013 · Secret redaction
Priority: **MVP** · Covers: trove-only

No secret may leave the process through a log record, an audit record, a span, a support bundle, a
delivery-log entry or an error message. These rules are mandatory, and code review checks them
([docs/developer/](../developer/)).

1. **Secret-typed values.** Every secret held in memory (tokens, passwords, client secrets, webhook
   secrets, the object-store secret key, the Valkey and Sentinel passwords, the backup target's
   keys) uses a dedicated type whose `LogValue()`
   ([`slog.LogValuer`](https://pkg.go.dev/log/slog#LogValuer)), `String()` and `MarshalJSON()`
   return `[REDACTED]`.
2. **Key-based redaction.** A handler-level `ReplaceAttr` replaces the value of any attribute whose
   key contains `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, `apikey`,
   `private_key` or `credential` (case-insensitive). The ID fields `token_id` and `token_type` are
   exempt.
3. **Pattern scrubbing** of string values:
   - trove token formats (`trv_pat_`, `trv_sat_`, `trv_cit_` followed by the token body,
     INV-8) become `trv_<type>_[REDACTED]`;
   - `Bearer <value>` becomes `Bearer [REDACTED]`;
   - user information in URLs becomes `user:[REDACTED]@`;
   - query parameters on the redaction list are redacted;
   - the path segment after `/auth/cli/` (the poll secret of the npm web login,
     [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-034) becomes
     `[REDACTED]`.
4. **Bodies and headers.** Request and response bodies are never logged. Of the headers, only
   `User-Agent`, `Content-Type`, `Content-Length` and `Accept` may be logged.
5. **Spans.** Span attributes follow rules 1 to 3. Database spans carry statement text with
   placeholders only, never bound values.
6. **Errors passed on to clients** go through the same scrubbing. A pull-through fetch error never
   returns upstream credentials embedded in a URL.

**Acceptance criteria:** a test suite logs in, publishes with `npm publish` and with
`dotnet nuget push --api-key "$TOKEN"`, creates a templated webhook with a secret, and creates a
support bundle. Afterwards, `grep -E 'trv_(pat|sat|cit)_[0-9A-Za-z]{10,}'` over the collected log
output, the audit export, the exported spans, the support bundle and the delivery log finds
nothing.

## Audit

### R-OPS-014 · Event registry
Priority: **MVP** · Covers: trove-only

Audit events and webhook event types share one registry in code. Every entry declares:

- **name**: `<domain>.<event>` or `<domain>.<object>.<event>`, each segment lower snake case and
  the last in past tense, for example `repository.created`, `package.published` or
  `system.backup.completed`;
- **category**: `auth`, `token`, `identity`, `authz`, `repository`, `content`, `system`, `config`,
  `webhook`;
- **audit mode**: `transactional`, `async` or `none` (R-OPS-015);
- **webhook**: whether subscriptions may select the event (R-OPS-037);
- **journal**: `revocation` or absent (see "Journal flag" below);
- **data schema**: a JSON Schema of the event data, published through R-OPS-037.

Names are a public contract: audit exports, SIEM rules and webhook receivers parse them. A name is
never reused. A rename keeps the old name as an alias, emitted next to the new one for at least one
minor release, and the release notes MUST list it. Other spec pages add their events to the
registry under these rules; [storage.md](storage.md), for example, adds `retention_policy.*`,
[repositories-and-proxy.md](repositories-and-proxy.md) adds the security event
`repository.remote_package_shadowed` (R-REPO-048), and [formats/npm.md](formats/npm.md) R-NPM-044
adds the npm events, among them `npm.unpublish` with the journal flag and the transactional
`npm.reinstate` without it. The MVP ones among these are listed in R-OPS-016.

**Bounded arrays.** Every array in a data schema declares `maxItems` of at most 1,000, and the
producer of an event keeps to it. Templated webhooks rely on that bound (R-OPS-041).

**Journal flag** (Q-HA-9 on [ha-and-helm.md](ha-and-helm.md)). MVP since 2026-10-28: the
revocation journal of [ha-and-helm.md](ha-and-helm.md) R-HA-054 is MVP, because every restore of a
trove backup replays it ([backup-and-restore.md](backup-and-restore.md) R-BAK-024, INV-19), and
NuGet relies on it for version numbers from the MVP on ([formats/nuget.md](formats/nuget.md)
R-NUGET-033). An entry declares `journal: revocation` when its event does one of these:

- removes access or updates an access-bearing object (R-HA-054);
- withdraws published content that a restore would otherwise bring back (npm: `npm.unpublish`,
  [formats/npm.md](formats/npm.md) Q-NPM-12);
- reserves or withdraws version numbers (NuGet: `package.published` and `package.deleted` of local
  repositories, [formats/nuget.md](formats/nuget.md) Q-NUGET-10, R-NUGET-033).

Every other entry leaves the attribute out. Only an entry with audit mode `transactional` may
declare it, because the journal row is written in the transaction of the change (R-OPS-015).
Without the journal, a database restore revives what such an event removed
([ha-and-helm.md](ha-and-helm.md) H-37). R-HA-054 lists the access events that MUST carry the flag
and replays them after a restore. The owning page sets the flag and defines what the entry
carries. An event type added later joins the journal through the flag alone, without new emission
code.

- **The flag is per event type; the entry comes from the producer.** The flag means "journaled
  when the producer supplies an entry". The audit writer journals an event of a flagged type when
  its producer passes a journal entry with it, and writes no `revocation_outbox` row without one
  (R-OPS-015).
- **Required or optional entry.** A flagged entry declares `journal_entry: required` (the
  default) or `optional`. Every type that one page owns alone is `required`: an event of it
  written without an entry fails its transaction, so a producer bug can never drop a revocation
  silently. `optional` is reserved for event types that several producers share and not all of
  them journal: `package.published` and `package.deleted` carry `journal: revocation` with
  `journal_entry: optional`, because npm journals neither (it journals `npm.unpublish` instead,
  Q-NPM-12) and NuGet supplies the entry of R-NUGET-033 for its local repositories.

**Acceptance criteria:** a test fails when a registry entry has no data schema, when two entries
share a name, or when an array in a data schema has no `maxItems` or one above 1,000. It also
fails when an entry declares `journal` with a value other than `revocation` or with an audit mode
other than `transactional`, when an entry declares `journal_entry` without `journal`, when an
entry outside the list of shared types in the registry code (today `package.published` and
`package.deleted`) declares `journal_entry: optional`, or when an event listed in
R-HA-054 lacks the flag, is optional or is missing from the registry. An integration test writes
`token.revoked` without an entry through a test hook and finds the revocation rolled back; an npm
publish writes `package.published` and no `revocation_outbox` row, and a NuGet push to a local
repository writes both.

### R-OPS-015 · Audit recording
Priority: **MVP** · Covers: OPS-022, OPS-023

Audit events are stored in PostgreSQL and copied to the log stream (INV-16).

- **Transactional events** record state changes: configuration, identities, tokens, permissions and
  content writes. They MUST be inserted in the same database transaction as the change. When the
  insert fails, the change rolls back. No change exists without its audit record.
- **Asynchronous events** record access: logins, denials, rejected tokens, failed CI exchanges.
  They go through a bounded in-process buffer (`audit.async_buffer`), written in batches every
  second or every 500 events. When the buffer is full, the event is still written to the log
  stream, and `trove_audit_events_dropped_total` is incremented. A burst of denials can
  therefore never block request handling. On `SIGTERM` the replica flushes the buffer before it
  exits (step 4 of the shutdown in R-OPS-001, which is step 6 of
  [architecture.md](architecture.md) R-ARCH-024), so a drain or a rollout loses no event while
  PostgreSQL is reachable.
- **Log copy.** After the commit, each event is also written as a `log: audit` record with the same
  fields. The database copy is authoritative. A crash between commit and log write can lose the log
  copy, not the record.
- **Deduplication without updates.** Audit rows are never updated (Q-OPS-4). `authz.denied` is
  deduplicated per replica, principal, action, repository and path within
  `audit.denial_dedup_window`. `auth.token_rejected` is deduplicated per replica and token ID within
  `audit.token_rejection_dedup_window`.
  - The first occurrence is written at once, with `details.count` = 1.
  - Further occurrences within the window are counted in memory. When the window closes they are
    written as one summary event, with `details.count` = the number of repeats and
    `details.repeat_of` = the ID of the first event.
  - A replica that shuts down writes its open summaries before it exits.
  - Requests reach replicas at random (INV-12). With n replicas, one burst therefore yields up to
    n first events and n summaries per key and window, and the sum of `details.count` over them is
    exact (Q-OPS-14). Deduplication state never lives in Valkey: an audit record must not depend
    on shared ephemeral state (INV-3).
- **Anonymous denials** are recorded only with `audit.record_anonymous_denials: true`. Otherwise
  they appear only in the request log and the metrics, so anonymous scanning cannot flood the
  table. When they are recorded, they are deduplicated per replica and client address (after
  trusted-proxy processing, R-OPS-005), not per principal, action, repository and path, so a
  scanner that probes many paths still collapses into one key. With n `web` replicas, one address
  yields at most n first events and n summaries per window, and the sum of `details.count` stays
  exact ([authorization.md](authorization.md) R-AUTHZ-029).
- **Successful downloads** are recorded only with `audit.record_downloads: true`, as asynchronous
  `artifact.downloaded` events. The request log holds them in every case.
- **Allow decisions.** Every authorization decision can be traced (INV-9). With the `authz`
  component at `debug` level, allow decisions are logged, though never stored as audit events.
- **Change details.** For an update, `details.changes` lists the changed fields as
  `{"field": "<path in the API representation>", "from": …, "to": …}`. A secret field appears only
  as `{"field": "…", "change": "set" | "changed" | "cleared"}`.
- **Revocation journal** (MVP with R-HA-054). When the audit writer inserts a transactional event
  whose registry entry declares `journal: revocation` (R-OPS-014) and whose producer supplied a
  journal entry, it also inserts a `revocation_outbox` row ([ha-and-helm.md](ha-and-helm.md)
  R-HA-054) in the same transaction.
  - No entry, no row. For a type with `journal_entry: required`, a missing entry fails the
    transaction instead, like a failed audit insert. For a type with `journal_entry: optional`
    (`package.published`, `package.deleted`), a missing entry means the producer does not journal
    it: an npm publish writes no row, a push to a local NuGet repository writes one with the entry
    of [formats/nuget.md](formats/nuget.md) R-NUGET-033.
  - The row takes the event's `id`, its `occurred_at` as `at` and its registry name as `event`.
  - When that insert fails, the change rolls back, exactly as for a failed audit insert. A
    committed change therefore always has its journal row, and a rolled-back change never has one.
  - For an update event, the entry carries the object's access-bearing state before and after the
    change; the owning page defines which fields that state holds. Secrets are redacted as in
    `details.changes`: a secret field carries its value in neither state, only `set`, `changed` or
    `cleared`, and the entry holds no token hashes.
  - Asynchronous events never write a journal row, because R-OPS-014 allows the flag only on
    transactional events.

```sql
CREATE TABLE audit_event (
  id            uuid        NOT NULL,          -- UUIDv7
  occurred_at   timestamptz NOT NULL,
  action        text        NOT NULL,          -- registry name, e.g. 'repository.created'
  category      text        NOT NULL,
  outcome       text        NOT NULL CHECK (outcome IN ('success','denied','failure')),
  actor_type    text        NOT NULL,          -- user | service_account | ci | breakglass | system | anonymous
  actor_id      uuid,
  actor_name    text,
  token_id      uuid,
  auth_method   text,
  source_ip     inet,
  user_agent    text,
  trace_id      text,
  replica       text,
  endpoint      text,                          -- endpoint of the request (endpoints.md R-EP-009); NULL for jobs
  resource_type text,                          -- repository | user | group | token | permission_policy | …
  resource_id   text,
  repository    text,
  path          text,
  reason        text,                          -- denial or failure code
  details       jsonb       NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);            -- one partition per calendar month (UTC)

CREATE INDEX audit_event_actor_idx      ON audit_event (actor_id, occurred_at DESC);
CREATE INDEX audit_event_action_idx     ON audit_event (action, occurred_at DESC);
CREATE INDEX audit_event_repository_idx ON audit_event (repository, occurred_at DESC) WHERE repository IS NOT NULL;
CREATE INDEX audit_event_trace_idx      ON audit_event (trace_id) WHERE trace_id IS NOT NULL;
```

Partitions use [declarative partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html).

**Append-only rows and partition maintenance** (Q-OPS-4). trove's runtime role can insert and read
audit events, but it can neither change nor delete them, and it cannot shorten their retention.

- **Privileges.** The migration revokes `UPDATE`, `DELETE` and `TRUNCATE` on `audit_event` from the
  runtime grant role of [ha-and-helm.md](ha-and-helm.md) R-HA-014, so the schema's default
  privileges do not reach this table. The runtime role holds no privilege on the partitions
  themselves and reaches them only through the parent table.
- **One maintenance function.** Partitions are created and dropped only by
  `audit_maintain_partitions()`. The migration creates it as the schema owner, with
  `SECURITY DEFINER` and a fixed `search_path`
  ([guidance](https://www.postgresql.org/docs/current/sql-createfunction.html#SQL-CREATEFUNCTION-SECURITY)).
  The runtime role holds only `EXECUTE` on it.
  - It creates the partitions for the current month and the next three months, and revokes the
    runtime role's privileges on each new partition.
  - It drops partitions whose upper bound is older than `audit_policy.retention_days`. `0` keeps
    every partition.
  - Concurrent calls from several replicas are serialised by locking the `audit_policy` row
    (`SELECT … FOR UPDATE`), so no advisory-lock category is needed.
- **Partitions for a given time range.** A restore loads audit events of months that the current
  month's window does not cover ([backup-and-restore.md](backup-and-restore.md) R-BAK-021 step 4).
  The owner's companion function `audit_create_partitions(range_from, range_to)` creates every
  missing monthly partition that overlaps the range, with the same attach procedure, lock timeout
  and privilege revocation as `audit_maintain_partitions()`, and drops nothing, so a restore never
  loses a loaded month to the retention step. It refuses a range longer than 1,200 months or one
  whose end lies before its start. The runtime role holds no `EXECUTE` on it; the restore Job runs
  it as the schema owner for the time range of the audit rows it loads.
- **Short locks.** Every transactional write inserts into `audit_event`, so partition DDL must
  never queue writes behind it ([partition maintenance](https://www.postgresql.org/docs/current/ddl-partitioning.html)).
  - The function sets `lock_timeout` to 2 s through its own `SET` clause, which applies only
    while it runs. A session-level `SET` is forbidden on the main pool, because that pool may run
    through a transaction-mode pooler ([architecture.md](architecture.md) R-ARCH-019).
  - It creates each new partition as a standalone table and attaches it with `ATTACH PARTITION`,
    which takes only a `SHARE UPDATE EXCLUSIVE` lock on the parent; that lock does not conflict
    with inserts or reads. `CREATE TABLE … PARTITION OF` would take `ACCESS EXCLUSIVE`.
  - Dropping an expired partition still takes `ACCESS EXCLUSIVE` on the parent. When a lock is
    not granted within the timeout, for example behind the snapshot transaction of a running
    backup, which holds `ACCESS SHARE` on every table it exports
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-005), the function catches
    the `lock_not_available` error (`55P03`) for that partition, skips it, and the next call
    retries it.
  - Writes therefore wait at most the timeout, and a skipped call costs nothing, because
    partitions exist three months ahead.
  - An audit export is not such a reader: it reads in short keyset batches and holds no
    transaction between them (R-OPS-017), so the backup's snapshot transaction is the only long
    reader the function has to wait out.
- **Retention is owner data.** `audit_policy` is owned by the schema owner and only readable by the
  runtime role. `trove migrate` writes `audit.retention_days` into it on every run, so a
  compromised replica cannot shorten retention. A replica whose configured value differs from the
  stored one logs a warning that names `trove migrate`.
- **When it runs.** `trove migrate` calls the function after writing `audit_policy`. Every replica
  calls it at startup, and the `startup` readiness check (R-OPS-001) passes only when partitions
  for the current and the next month exist. The `audit-retention` task calls it daily
  (R-OPS-019). An insert therefore never finds a missing partition, even when the scheduler has
  not run for weeks.
- **Single-role mode.** In the database-role setup of [ha-and-helm.md](ha-and-helm.md) R-HA-014
  that uses one login for everything (not a process role of INV-2), the runtime role owns the
  schema, so these protections do not apply, and the replica writes `audit_policy` itself at
  startup (H-39).
- **Writers.** `web` and `worker` replicas write events of the requests and jobs they run; the
  `backup` role writes its own run events with the grant of
  [backup-and-restore.md](backup-and-restore.md) R-BAK-015 (`INSERT` on `audit_event` only).

```sql
CREATE TABLE audit_policy (
  id              smallint    PRIMARY KEY CHECK (id = 1),
  retention_days  integer     NOT NULL CHECK (retention_days >= 0),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Run by the migration as the schema owner. Schema and role names: R-HA-014 (example values).
CREATE FUNCTION audit_maintain_partitions() RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = trove, pg_temp SET lock_timeout = '2s'
  AS $$ … $$;
REVOKE ALL ON FUNCTION audit_maintain_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_maintain_partitions() TO trove_app;
-- Owner only: creates the partitions a restore needs (R-BAK-021 step 4); drops nothing.
CREATE FUNCTION audit_create_partitions(range_from timestamptz, range_to timestamptz) RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = trove, pg_temp SET lock_timeout = '2s'
  AS $$ … $$;
REVOKE ALL ON FUNCTION audit_create_partitions(timestamptz, timestamptz) FROM PUBLIC;
REVOKE UPDATE, DELETE, TRUNCATE ON audit_event FROM trove_app;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON audit_policy FROM trove_app;
```

```yaml
audit:
  async_buffer: 10000                    # default (proposed)
  denial_dedup_window: 1m                # default (proposed)
  token_rejection_dedup_window: 5m       # default (proposed)
  record_anonymous_denials: false        # default (proposed)
  record_downloads: false                # default (proposed)
```

**Acceptance criteria:**

- `dotnet nuget push Acme.Lib.1.0.0.nupkg --source https://trove.example.com/api/nuget/nuget-local/v3/index.json --api-key "$TOKEN"`
  yields one `package.published` event with outcome `success`, the token's `token_id` and
  repository `nuget-local`. The same event appears in `kubectl logs` as `"log":"audit"`.
- A repository update whose audit insert is forced to fail (fault injection) leaves the repository
  unchanged.
- Revoking a token commits one `revocation_outbox` row whose `id` and `at` equal the `id` and
  `occurred_at` of its `token.revoked` event. With the outbox insert forced to fail (fault
  injection), the revocation rolls back: the token still authenticates and no `token.revoked` event
  exists. Removing `deploy` from a policy entry yields an `authz.policy.updated` row whose entry
  holds that policy entry's actions before (`deploy`, `read`) and after (`read`).
- Ten pushes with a read-only token within one minute, all through one replica, yield two
  `authz.denied` events: the first with `details.count` = 1, then one summary with
  `details.count` = 9 and `details.repeat_of` naming the first.
- The same ten pushes sent at random over three `web` replicas yield at most six `authz.denied`
  events, and their `details.count` values sum to 10.
- In two-role mode, `psql -U trove_app_a -c 'DELETE FROM trove.audit_event'` and
  `psql -U trove_app_a -c "UPDATE trove.audit_event SET outcome = 'success'"` fail with `42501`
  (insufficient privilege), and so does the same `DELETE` against one partition. So does an
  `UPDATE` of `trove.audit_policy`.
- After a replica starts, `pg_inherits` lists partitions of `audit_event` for the current month and
  the next three months.
- While a second session holds an open transaction that has read `audit_event` (as the snapshot
  transaction of a running backup does), a call of `audit_maintain_partitions()` that has an
  expired partition to drop returns within 3 s without dropping it, and repository updates issued
  meanwhile commit without waiting for it.
- `SELECT audit_create_partitions('2025-01-15Z', '2025-04-02Z')` as the schema owner creates the
  partitions for January to April 2025 and drops none; the same call as `trove_app_a` fails with
  `42501`.
- A long audit export read with `curl --limit-rate` leaves no `idle in transaction` session, and a
  partition drop due meanwhile succeeds on its first call.
- With `audit.record_anonymous_denials: true`, 1,000 anonymous requests from one client address
  to 1,000 different paths, sent at random over three `web` replicas, yield at most six denial
  events with `actor_type` `anonymous` for that address, and their `details.count` values sum to
  1,000.

### R-OPS-016 · Audit event catalog
Priority: **MVP** · Covers: OPS-022, OPS-023

The MVP registry MUST contain at least the events below. Mode is **T** for transactional, **A** for
asynchronous. "Hook" marks events that webhooks may select (R-OPS-037).

<details>
<summary>Audit events</summary>

| Event | Mode | Hook | Recorded when | Key fields in `details` |
|---|---|---|---|---|
| `auth.login_succeeded` | A | — | Browser OIDC login or break-glass login succeeded (INV-7) | `method` (`oidc`, `breakglass`), `issuer`, `session_id` |
| `auth.login_failed` | A | — | Login failed | `reason` (`oidc_error`, `state_mismatch`, `user_disabled`, `breakglass_disabled`, `bad_credentials`) |
| `auth.logout` | A | — | Session ended by the user | `session_id` |
| `auth.ci_exchange_denied` | A | — | A CI OIDC exchange was refused (INV-10) | `issuer`, `reason` (`signature`, `audience`, `expired`, `no_mapping`), selected claims (`project_path`, `ref`, `pipeline_id`) |
| `auth.token_rejected` | A | — | A well-formed trove token (valid checksum) was unknown, revoked or expired | `token_id` if known, `token_type`, `reason`, `count`, `repeat_of` (summaries only) |
| `token.created` | T | — | Any trove token was issued, including CI exchanges | `token_type`, `scope`, `expires_at`, `ci_mapping`, `ci_claims` |
| `token.revoked` | T | — | A token was revoked | `token_type`, `revoked_by` |
| `session.revoked` | T | — | An administrator ended another user's session | `session_id` |
| `user.created` | T | — | A user was created (just in time from ID-token claims, or by an administrator) | `source` (`jit`, `api`) |
| `user.updated` | T | — | Profile or group membership changed, including changes from claims at login | `changes` |
| `user.disabled`, `user.enabled`, `user.deleted` | T | — | User state change | — |
| `group.created`, `group.updated`, `group.deleted` | T | — | Group record change ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-018). Claim-to-group rules are part of the login provider in the configuration file and change with a rollout, not through an event (Q-OPS-6) | `changes` |
| `serviceaccount.created`, `serviceaccount.updated`, `serviceaccount.disabled`, `serviceaccount.deleted` | T | — | Service-account change; names as [identity-and-authentication.md](identity-and-authentication.md) registers them | `changes` |
| `admin_role.granted`, `admin_role.revoked` | T | — | System administration role or delegated system permission changed | `permission` |
| `authz.policy.created`, `authz.policy.updated`, `authz.policy.deleted` | T | — | Permission policy change (INV-9); names as [authorization.md](authorization.md) registers them | `changes` |
| `endpoint.exposure.created`, `endpoint.exposure.updated`, `endpoint.exposure.deleted` | T | — | Exposure rule change of an endpoint ([endpoints.md](endpoints.md) R-EP-016); updates and deletions carry the journal flag (R-EP-020) | `changes`, `endpoint`, `before`, `after` |
| `authz.denied` | A | — | An authenticated principal was refused an action; with `audit.record_anonymous_denials`, also an anonymous caller, deduplicated per client address (R-OPS-015) | `operation`, `required_action`, `count`, `repeat_of` (summaries only) |
| `oidc_provider.created`, `oidc_provider.updated`, `oidc_provider.deleted` | T | — | Trusted issuer change | `changes` |
| `identity_mapping.created`, `identity_mapping.updated`, `identity_mapping.deleted` | T | — | CI identity mapping change | `changes` |
| `repository.created`, `repository.updated`, `repository.deleted` | T | — | Repository configuration change | `changes` |
| `repository.cache_invalidated` | T | — | R-OPS-021 | `path`, `scope`, `count` |
| `repository.remote_package_shadowed` | T | — | Security event: a caller holding `manage` (or `content:bypass`) gave a local repository its first version of a name that a remote member of a containing virtual repository serves ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048); written in the commit transaction. The local repository is the `repository` column | `package`, `exposure` (the pairs `{virtual, remote}`) |
| `package.published` | T | yes | A package version was accepted (npm publish, NuGet push). Journal flag with `journal_entry: optional` (R-OPS-014): journaled for local NuGet repositories only ([formats/nuget.md](formats/nuget.md) R-NUGET-033) | `package`, `version`, `files` |
| `package.deleted` | T | yes | A package version was deleted or unpublished. Journal flag with `journal_entry: optional`, as for `package.published` | `package`, `version` |
| `npm.reinstate` | T | — | `trove admin npm reinstate` put back a version that a journal replay had withheld ([formats/npm.md](formats/npm.md) R-NPM-044, R-NPM-047); not journaled, because it restores access rather than removing it | `package`, `version`, `integrity`, `journal_entry` (the entry that withheld it) |
| `package.metadata_changed` | T | yes | Deprecate, undeprecate, unlist, relist, dist-tag set or removed | `package`, `version`, `change` |
| `artifact.deployed` | T | yes | A file was stored in a local repository by any upload path | `path`, `sha256`, `size`, `overwritten` |
| `artifact.deleted` | T | yes | A file was removed | `path`, `sha256`, `size` |
| `artifact.downloaded` | A | — | Only with `audit.record_downloads` | `path`, `sha256` |
| `system.config_applied` | T | — | Configuration document applied (R-OPS-030, R-OPS-031) | `document`, `digest`, `summary`, `source` |
| `system.config_exported` | A | — | R-OPS-032 | `kinds` |
| `system.maintenance_mode_changed` | T | — | R-OPS-024 | `from`, `to`, `message` |
| `system.banner_changed` | T | — | R-OPS-025 | `changes` |
| `system.log_levels_changed` | T | — | R-OPS-012 | `levels`, `expires_at` |
| `system.task_triggered` | T | — | Manual task run, garbage collection, reindex, storage-summary refresh | `task`, `job_id`, `parameters` |
| `system.support_bundle_created`, `system.support_bundle_downloaded`, `system.support_bundle_deleted` | T | — | R-OPS-027 | `bundle_id`, `include` |
| `system.caches_flushed` | T | — | R-OPS-047 (P1) | `inProcess`, `negativeRepositories` |
| `system.backup.completed` | T | — | A `trove backup create` run ended `completed` or `completed_with_warnings` ([backup-and-restore.md](backup-and-restore.md) R-BAK-014); actor `system:backup`, `replica` = the backup pod | `backupId`, `outcome`, `databaseBytes`, `blobsCopied`, `damaged` |
| `system.backup.failed` | T | — | A create run ended `failed` (R-BAK-014); an `interrupted` run writes none; actor `system:backup` | `backupId`, `code` |
| `system.backup.deleted` | T | — | A backup was deleted by retention or by `trove backup delete` (R-BAK-016); actor `system:backup` | `backupId`, `reason` (`retention`, `manual`) |
| `system.backup.target_adopted` | T | — | A run adopted a target of an earlier incarnation (R-BAK-002); actor `system:backup` | `previousIncarnation` |
| `system.restored` | T | — | A restore finished; written into the restored database ([backup-and-restore.md](backup-and-restore.md) R-BAK-021, [ha-and-helm.md](ha-and-helm.md) R-HA-025 for a point-in-time restore) | `source` (`backup`, `pitr`), `backupId`, `snapshotAt`, `journal` (`replayed`, or `skipped` with `--skip-revocation-journal`) |
| `system.restore.journal_replayed` | T | — | The revocation journal was replayed after a restore (R-BAK-024, [ha-and-helm.md](ha-and-helm.md) R-HA-054) | counts per outcome |
| `audit.exported` | T | — | R-OPS-017 | `from`, `to`, `filters`, `format` |
| `webhook.created`, `webhook.updated`, `webhook.deleted` | T | — | R-OPS-038 | `changes` |
| `webhook.secret_rotated`, `webhook.tested`, `webhook.redelivered` | T | — | R-OPS-040, R-OPS-041, R-OPS-043, R-OPS-044 | `secret_name` |
| `webhook.disabled_by_receiver`, `webhook.suspended` | T | — | R-OPS-042, R-OPS-039 | `status`, `reason` |

</details>

P1 features add their events when they ship, under R-OPS-014: `artifact.cached` (webhook only, no
audit), `artifact.copied`, `artifact.moved`, `artifact.property_set`, `artifact.property_deleted`,
`container.tag_pushed`, `container.tag_deleted`, `build.uploaded`, `build.deleted`,
`build.promoted`, and the retention events of [storage.md](storage.md).

**Acceptance criteria:** for every row, an integration test performs the triggering action and
finds exactly one matching event, with `actor_type`, `outcome` and the listed keys filled in.

### R-OPS-017 · Audit query, export and retention
Priority: **MVP** · Covers: OPS-023

API (requires `audit.read`; a system administrator holds it implicitly):

| Method and path | Parameters | Response |
|---|---|---|
| `GET /api/v1/audit/events` | `from`, `to` (default: the last 24 h), `action` (exact, or a prefix ending in `.*`), `category`, `outcome`, `actor` (ID or name), `repository`, `resourceType`, `resourceId`, `traceId`, `limit` (≤ 1000, default 100), `cursor` | `200 {"items":[…],"nextCursor":"…"}`, newest first |
| `GET /api/v1/audit/events/{id}` | — | `200` one event; `404` |
| `GET /api/v1/audit/events/export` | `from`, `to` (both required, range ≤ 400 days), the same filters, `format=jsonl` (default) or `csv` | `200` streamed as [JSON Lines](https://jsonlines.org/) (`application/x-ndjson`) or CSV, oldest first; `400` for a missing or oversized range |

```json
{
  "id": "01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11",
  "occurredAt": "2026-10-28T11:58:02.417Z",
  "action": "package.published",
  "category": "content",
  "outcome": "success",
  "actor": {"type": "ci", "id": "5f0e…", "name": "ci:gitlab-internal/acme-publish", "tokenId": "c41d…", "authMethod": "bearer"},
  "source": {"ip": "10.12.4.7", "userAgent": "npm/11.6.0 node/v24.11.0 linux x64 workspaces/false"},
  "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "replica": "trove-web-6f9c7d8b5-x2v4q",
  "resource": {"type": "package", "id": "npm-local/@acme/ui@2.4.0"},
  "repository": "npm-local",
  "details": {"package": "@acme/ui", "version": "2.4.0", "files": [{"path": "@acme/ui/-/ui-2.4.0.tgz", "sha256": "…", "size": 48213}]}
}
```

- **Exports are audited.** Every export writes `audit.exported`.
- **Long exports and drains** follow [api-and-ui.md](api-and-ui.md) R-API-017 and the streamed
  downloads of R-API-051. The export is ordered by `(occurred_at, id)`, which the primary key of
  `audit_event` already indexes, and read in keyset batches. Each batch is one short query on a
  main-pool connection, and the connection is returned before the batch is written to the client
  ([architecture.md](architecture.md) R-ARCH-019). No export therefore holds a connection or a
  long transaction while the client reads, concurrent exports add no connections (INV-12), and
  partition maintenance never waits behind an export (R-OPS-015). The answer is sent with chunked
  transfer coding (or as an HTTP/2 stream), so a cut export is visibly incomplete. A drain cuts an
  export at most once per evicted replica; the client resumes on any replica with an inclusive
  `from` set to the `occurredAt` of its last complete line and drops the lines whose `id` it
  already has. Each batch is its own snapshot: an event whose transaction commits after the batch
  that covers its time is not in that export, so a range that ends further back than the longest
  write transaction is complete (R-API-051).
- **Push to a SIEM** goes through the `log: audit` stream and the platform's log shipping. The
  export endpoint covers pulls and backfills.
- **Retention.** The `audit-retention` task calls `audit_maintain_partitions()` (R-OPS-015), which
  drops whole partitions whose newest possible timestamp is older than the stored
  `audit.retention_days`. `0` keeps events for ever. Values below 90 log a warning at startup. A
  changed value takes effect after the next `trove migrate`, which the chart runs on every upgrade.
- **Restore.** Audit events live in PostgreSQL with the backup export class `data`, so a trove
  backup and a point-in-time restore bring them back as of their snapshot
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-001, [ha-and-helm.md](ha-and-helm.md)
  R-HA-025). Events written after the snapshot exist only in the `log: audit` stream, which is one
  more reason to ship it outside trove (H-39).
- **UI.** The UI offers an audit view backed by this API ([api-and-ui.md](api-and-ui.md)).

```yaml
audit:
  retention_days: 365             # default (proposed); 0 = keep for ever
```

**Acceptance criteria:**

- `curl -s -H "Authorization: Bearer $AUDITOR_TOKEN" 'https://trove.example.com/api/v1/audit/events/export?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z' | jq -c .action | sort | uniq -c`
  lists the month's actions. The export itself appears as `audit.exported`.
- An export of 200,000 events read with `curl --limit-rate 200K` is cut by deleting the serving
  pod. A second request with `from` set to the last complete line's `occurredAt`, with the IDs
  already received dropped, completes it, and the union holds each event of the range exactly
  once. Meanwhile `pg_stat_activity` shows no connection of the pod `idle in transaction` and none
  held for longer than one batch query.
- With `retention_days: 30` and a test clock advanced by 62 days, the oldest month's partition is
  gone after the task runs.
- A token without `audit.read` gets `403`.

## Tracing

### R-OPS-018 · Trace context and OpenTelemetry export
Priority: **MVP** · Covers: trove-only

**Trace IDs always exist.** Every request and every job has a
[W3C Trace Context](https://www.w3.org/TR/trace-context/) trace ID, whether or not export is
enabled. The ID correlates request log, audit events, upstream log and spans, and the client
receives it as `X-Request-Id`.

**Incoming context.** An incoming `traceparent` is continued according to
`tracing.incoming_context`:

- `trusted_proxies` (default): continued only from peers in `server.trusted_proxies`. From any other
  client trove starts a new trace and attaches the incoming context as a span link, so clients
  cannot choose the trace IDs written into the audit trail.
- `all`: continued from every client.
- `none`: never continued.

**Export** (`tracing.enabled: true`). Spans go over OTLP (gRPC or HTTP/protobuf) through a bounded
queue of the process (Q-OPS-25). When the trove keys are unset, the standard `OTEL_EXPORTER_OTLP_*` variables of the
[OTLP exporter specification](https://opentelemetry.io/docs/specs/otel/protocol/exporter/) apply.
Resource attributes:

- `service.name=trove`;
- `service.version`;
- `service.instance.id` = pod name;
- `trove.role` = the process role (`web`, `worker`, `all`, `backup`);
- Kubernetes attributes, where the downward API provides them.

**Spans:**

- One server span per request, named and attributed after the
  [HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/), with
  `http.route` set to the handler template.
- Child spans for authentication, authorization, database queries (statement with placeholders
  only), object-storage calls, Valkey commands and upstream fetches.
- **Valkey spans.** One client span per command or pipeline, with `db.operation.name` set to the
  command and `db.system.name` set to `valkey`. The registry of the database conventions lists
  `redis` but no `valkey` value (checked 2026-10-28,
  [database attributes](https://opentelemetry.io/docs/specs/semconv/registry/attributes/db/)), and
  Valkey is not Redis. Keys, values and channel names are never attributes. While the circuit is
  open no span is created; the fallback is recorded as the span event `valkey.fallback`.
- **Collapsed cache misses.** A waiter on the same replica adds a span link to the leader's trace.
  A waiter on another replica adds one when the flight's outcome message carries the leader's trace
  context ([architecture.md](architecture.md) R-ARCH-033, cross-page); it always carries the
  attribute `trove.flight.role`.
- Job spans link to the trace that enqueued the job, whose `traceparent` is stored with the job, so a
  job run on a `worker` replica links to the `web` request that started it.
- Webhook deliveries get a span per attempt.
- A `trove backup` run is one trace, with the run ID as `trove.backup.run_id`.

**Attributes.** trove-specific attributes use the `trove.` namespace:
`trove.repository.key`, `trove.repository.format`, `trove.repository.class`,
`trove.cache.result`, `trove.principal.type`. User names, e-mail addresses and tokens are never
span attributes (R-OPS-013).

**Outgoing context.** Context is not propagated to remote upstreams or webhook receivers unless
`tracing.propagate_to_upstreams: true`. Their trace IDs would otherwise reveal internal request
correlation to third parties.

**Sampling.** Parent-based, with a trace-ID ratio (`tracing.sample_ratio`). Errors cannot be
favoured in head sampling; tail sampling belongs in the collector.

**Exemplars.** Histogram observations made inside a sampled span carry the trace ID as an
OpenMetrics exemplar.

**Dropped spans.** A full export queue drops spans and increments
`trove_tracing_spans_dropped_total`. Request handling never waits for export.

```yaml
tracing:
  enabled: false                         # default (proposed)
  endpoint: ""                           # example http://otel-collector.observability:4318
  protocol: http/protobuf                # default (proposed); grpc | http/protobuf
  ca_file: ""                            # example /etc/trove/tls/otel-ca.pem
  headers_file: ""                       # example /var/run/secrets/trove/otel-headers (one "Name: value" per line)
  sample_ratio: 0.1                      # default (proposed)
  incoming_context: trusted_proxies      # default (proposed); trusted_proxies | all | none
  propagate_to_upstreams: false          # default (proposed)
  queue_size: 2048                       # default (proposed)
```

**Acceptance criteria:**

- With an OpenTelemetry Collector using the `debug` exporter, `TROVE_TRACING_ENABLED=true`,
  `TROVE_TRACING_ENDPOINT=http://otel-collector:4318` and `TROVE_TRACING_SAMPLE_RATIO=1`, a
  cold `npm install lodash --registry …/npm-all/` yields one trace containing:
  - a server span with `http.route`;
  - database spans;
  - a `PutObject` span;
  - a Valkey span for the flight lock (`SET`), without a key attribute;
  - a client span to the npm upstream.
- The trace ID equals the response's `X-Request-Id` and the request log's `trace_id`.
- No exported attribute matches `trv_`.
- A request sent directly from outside the trusted proxies with a forged `traceparent` gets a new
  trace ID.

## Maintenance and administrative operations

### R-OPS-019 · Scheduled maintenance tasks and job visibility
Priority: **MVP** · Covers: OPS-065, ART-051

**Why MVP** (Q-OPS-9). The queue and the scheduler are MVP ([architecture.md](architecture.md)
R-ARCH-016), and MVP operations return job references that must be pollable from the first
release: garbage collection (R-OPS-020) and every long-running API operation
([api-and-ui.md](api-and-ui.md) R-API-015). A task exists from the release that ships its owning
requirement: the MVP registers `gc`, `gc-full`, `audit-retention`, `replica-registry-prune`,
`secrets-reencrypt`, `cluster-change-prune`, `backup-check` and the MVP tasks of other pages
(among them `session-cleanup`, `token-cleanup` and `token-expiry-notice` of
[identity-and-authentication.md](identity-and-authentication.md) R-AUTH-043, and
`ci-keys-refresh`, `ci-replay-cleanup` and `ci-issuance-retention` of
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-030); P1 requirements add theirs when they ship.

**Tasks and leases.** A *task* is a named maintenance job, recurring or manual-only. A singleton
scheduler enqueues due tasks into the job queue. The scheduler runs on the `worker` replica that
holds the `scheduler` lease, a session-level advisory lock on its lease pool (INV-12,
[architecture.md](architecture.md) R-ARCH-017), and any ready `worker` replica executes the job
([architecture.md](architecture.md) R-ARCH-016 owns the queue). The trigger API runs on `web`
replicas and only enqueues. On `SIGTERM` the holder stops its scheduler tick and releases the
`scheduler` lease at once (R-OPS-001, shutdown step 1), not after its running jobs, which may take
the whole `worker` grace period. A drain of the scheduler's node therefore moves the lease to
another `worker` within `cluster.lease_retry_interval`. Leases that fence a running singleton job
stay with that job until it ends or is released ([ha-and-helm.md](ha-and-helm.md) R-HA-006). The
`(task, scheduled_for)` key below keeps the hand-over from enqueuing a slot twice.

**Backups are not a task.** `trove backup create` runs as the Kubernetes CronJob of
[ha-and-helm.md](ha-and-helm.md) R-HA-060, in the `backup` role: it needs the backup target's
credentials, which `web` and `worker` replicas never hold
([backup-and-restore.md](backup-and-restore.md) R-BAK-015), and its schedule is evaluated by
Kubernetes (Q-OPS-7). Its runs appear in `GET /api/v1/system/backups` (R-BAK-014), not in the task
or job lists.

**Manual-only tasks.** A task registered as manual-only has no schedule: it has no
`maintenance.tasks.<name>` entry, the scheduler never enqueues it, and it runs only through
`POST /api/v1/system/tasks/{name}/run`. A `maintenance.tasks.<name>` entry for such a task stops
startup with an error that names the key (R-OPS-028). `secrets-reencrypt` is the first one. A
manual-only task may declare a precondition that the trigger checks before it enqueues the job; a
failed precondition answers `409` with a problem that names it, and nothing is enqueued.

**Exactly-once enqueue.** Each due run carries the key `(task, scheduled_for)`. A unique constraint
on that key means a lease hand-over can never enqueue the same run twice.

**Missed runs** after a full outage are not caught up. Only the latest missed slot runs, once.

**Mutual exclusion.** A task does not start while another run of the same task, or of a task that
shares its exclusion key, is queued or running. [storage.md](storage.md)'s `gc` and `gc-full`
share the exclusive dedup key `gc` (Q-STOR-10, R-STOR-025), through the `exclusive` enqueue mode
of R-ARCH-016, so at most one collection run is queued or running, whichever task started it. A
manual trigger in that state returns the existing job with `409`. A scheduled slot refused by the
shared key is offered again on the next scheduler tick, so it is postponed, never lost
(R-STOR-025).

| Task | Semantics owned by | Default schedule |
|---|---|---|
| `gc` | [storage.md](storage.md) R-STOR-024 and R-STOR-025: incremental collection (R-OPS-020 is the trigger); dedup key `gc` | `*/30 * * * *` (proposed, storage.md) |
| `gc-full` | [storage.md](storage.md) R-STOR-025: full sweep (R-OPS-020 is the trigger); shares the dedup key `gc` | `0 2 * * 0` (proposed, storage.md) |
| `orphan-sweep` | [storage.md](storage.md) R-STOR-026 | `0 5 * * 6` (proposed, storage.md) |
| `scrub` | [storage.md](storage.md) R-STOR-027 | `0 4 * * *` (proposed, storage.md) |
| `storage-summary` | R-OPS-023 | `17 * * * *` (proposed) |
| `audit-retention` | R-OPS-015 and R-OPS-017: calls `audit_maintain_partitions()`, which creates partitions ahead and drops expired ones | `30 3 * * *` (proposed) |
| `webhook-retention` | R-OPS-044 and the outbox of R-OPS-035 | `*/15 * * * *` (proposed) |
| `support-bundle-expiry` | R-OPS-027 | `0 4 * * *` (proposed) |
| `replica-registry-prune` | R-OPS-003 | `*/10 * * * *` (proposed) |
| `remote-cache-cleanup` | [storage.md](storage.md) R-STOR-045; the per-repository expiry setting belongs to [repositories-and-proxy.md](repositories-and-proxy.md) (RET-036, RMT-021) | `0 3 * * *` (proposed, storage.md) |
| `token-cleanup`, `session-cleanup` | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-043 (R-AUTH-023; R-AUTH-005, R-AUTH-010, R-AUTH-034) | `40 3 * * *` and `*/10 * * * *` (proposed, identity-and-authentication.md) |
| `token-expiry-notice` | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-043, R-AUTH-032: writes `token.expiring` for tokens entering the notice period | `0 6 * * *` (proposed, identity-and-authentication.md) |
| `user-dormancy` (P1) | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-043, R-AUTH-021: disables dormant users and emits the dormancy warnings | `30 4 * * *` (proposed, identity-and-authentication.md) |
| `ci-keys-refresh` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-030, R-CI-003 | `*/5 * * * *` (proposed, gitlab-ci-oidc.md) |
| `ci-replay-cleanup` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-030, R-CI-018 | `7 * * * *` (proposed, gitlab-ci-oidc.md) |
| `ci-issuance-retention` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-030, R-CI-020 | `40 3 * * *` (proposed, gitlab-ci-oidc.md) |
| `secrets-reencrypt` | [architecture.md](architecture.md) R-ARCH-021: re-seals every stored secret with the active key; its job kind has the same name ([api-and-ui.md](api-and-ui.md) R-API-049) | manual only; precondition: the key rings of all live replicas are compatible (R-OPS-003), else `409` `key_ring_incompatible` |
| `cluster-change-prune` | [architecture.md](architecture.md) R-ARCH-016 and R-ARCH-018: deletes change-log rows older than `cluster.change_retention` | `*/10 * * * *` (proposed, architecture.md) |
| `backup-check` | [ha-and-helm.md](ha-and-helm.md) R-HA-023: the checks of `trove admin backup check`, exported as `trove_backup_check_ok{check}`; the task name is proposed here | `40 5 * * *` (proposed) |

Retention policies ([storage.md](storage.md)) carry their own schedules and appear in the job list,
not in the task list.

**Schedules are configuration.** They live in `maintenance.tasks.<name>`, not in the database. The
API shows them and can trigger a run, but it cannot edit them. A task's default schedule applies
when its key is absent; the owning page of a task sets that default, and the table above repeats
it.

API:

| Method and path | Request | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/tasks` | — | `200` per task: `name`, `schedule`, `enabled`, `nextRunAt`, `running`, `lastRun{jobId,startedAt,finishedAt,outcome,replica}`; `schedule` and `nextRunAt` are `null` for a manual-only task | `system.read` |
| `POST /api/v1/system/tasks/{name}/run` | optional task parameters, e.g. `{"dryRun":true}` | `202 {"job":{…}}` with `Location` of the job ([api-and-ui.md](api-and-ui.md) R-API-015); `409 {"job":{…}}` if a run of the task, or of a task sharing its dedup key, is already queued or running; `409` problem when the task's precondition fails (`key_ring_incompatible` for `secrets-reencrypt`); `404` unknown task; `503` in read-only mode for mutating tasks (R-OPS-024) | system administrator |
| `GET /api/v1/system/jobs` | `kind`, `state` (the job states of R-ARCH-016: `queued`, `running`, `succeeded`, `failed`, `cancelling`, `cancelled`), `limit`, `cursor` | `200` jobs with `id`, `kind`, `state`, `replica`, `enqueuedAt`, `startedAt`, `finishedAt`, `attempt`, `progress`, `error`, `triggeredBy` | `system.read`; any other caller sees only the jobs it started (R-API-015) |
| `GET /api/v1/system/jobs/{id}` | — | `200` one job, including its result summary | `system.read`, or the caller started the job (R-API-015) |
| `POST /api/v1/system/jobs/{id}/cancel` | — | `202`; the job turns `cancelling` and stops at its next checkpoint; `409` if finished | system administrator, or the caller started the job (R-API-015) |

A job reference returned by any trigger on this page has this shape:

```json
{"job": {"id": "01933b2a-…", "kind": "gc", "state": "queued", "href": "/api/v1/system/jobs/01933b2a-…"}}
```

```yaml
maintenance:
  tasks:
    gc: {enabled: true, schedule: "*/30 * * * *"}                  # default (proposed); owned by storage.md R-STOR-025
    gc-full: {enabled: true, schedule: "0 2 * * 0"}                # default (proposed); owned by storage.md R-STOR-025
    orphan-sweep: {enabled: true, schedule: "0 5 * * 6"}           # default (proposed); owned by storage.md R-STOR-026 (P1)
    scrub: {enabled: true, schedule: "0 4 * * *"}                  # default (proposed); owned by storage.md R-STOR-027 (P1)
    remote-cache-cleanup: {enabled: true, schedule: "0 3 * * *"}   # default (proposed); owned by storage.md R-STOR-045 (P1)
    storage-summary: {enabled: true, schedule: "17 * * * *"}       # default (proposed)
    audit-retention: {enabled: true, schedule: "30 3 * * *"}       # default (proposed)
    webhook-retention: {enabled: true, schedule: "*/15 * * * *"}   # default (proposed)
    support-bundle-expiry: {enabled: true, schedule: "0 4 * * *"}  # default (proposed)
    replica-registry-prune: {enabled: true, schedule: "*/10 * * * *"}  # default (proposed)
    cluster-change-prune: {enabled: true, schedule: "*/10 * * * *"}    # default (proposed); owned by architecture.md R-ARCH-018
    backup-check: {enabled: true, schedule: "40 5 * * *"}              # default (proposed); owned by ha-and-helm.md R-HA-023
    session-cleanup: {enabled: true, schedule: "*/10 * * * *"}         # default (proposed); owned by identity-and-authentication.md R-AUTH-043
    token-cleanup: {enabled: true, schedule: "40 3 * * *"}             # default (proposed); owned by identity-and-authentication.md R-AUTH-043
    token-expiry-notice: {enabled: true, schedule: "0 6 * * *"}        # default (proposed); owned by identity-and-authentication.md R-AUTH-043
    user-dormancy: {enabled: true, schedule: "30 4 * * *"}             # default (proposed); owned by identity-and-authentication.md R-AUTH-043 (P1)
    ci-keys-refresh: {enabled: true, schedule: "*/5 * * * *"}          # default (proposed); owned by gitlab-ci-oidc.md R-CI-030
    ci-replay-cleanup: {enabled: true, schedule: "7 * * * *"}          # default (proposed); owned by gitlab-ci-oidc.md R-CI-030
    ci-issuance-retention: {enabled: true, schedule: "40 3 * * *"}     # default (proposed); owned by gitlab-ci-oidc.md R-CI-030
    # secrets-reencrypt is manual-only: no entry; one here stops startup
```

**Acceptance criteria:**

- With three `web` and two `worker` replicas, each scheduled run of `storage-summary` appears
  exactly once in the job list over 24 h, and its `replica` is always a `worker` pod. A
  `POST …/tasks/storage-summary/run` answered by a `web` replica is executed by a `worker` replica.
- During a `kubectl drain` of the node of the `worker` that holds the `scheduler` lease, every
  slot of a task scheduled `* * * * *` is enqueued exactly once.
- `GET /api/v1/system/tasks` lists `secrets-reencrypt` with `"schedule": null` and
  `"nextRunAt": null`. Setting `maintenance.tasks.secrets-reencrypt` stops startup with an error
  that names the key. While `trove_secrets_key_rings_compatible` is `0`,
  `POST …/tasks/secrets-reencrypt/run` answers `409` with code `key_ring_incompatible` and no job is
  enqueued.
- Deleting the pod that holds the `scheduler` lease moves `trove_lease_held{name="scheduler"}`
  to another `worker` replica within `cluster.lease_retry_interval` plus 1 s, with no duplicate
  runs.
- `POST …/tasks/storage-summary/run` twice in a row returns `202`, then `409` with the same job ID.

### R-OPS-020 · Garbage-collection trigger
Priority: **MVP** · Covers: API-113

Garbage collection itself is specified in [storage.md](storage.md) (R-STOR-024, R-STOR-025). This
page specifies how it is started and observed.

- **Two tasks.** `POST /api/v1/system/tasks/gc/run` (R-OPS-019) starts an incremental run.
  `POST /api/v1/system/tasks/gc-full/run` starts the full sweep, which also finds unreferenced
  blobs that never reached the collection queue. Both take the body `{"dryRun": false}` (default).
  A dry run reports what it would delete and changes nothing, the trash purge included.
- **One run at a time.** Both tasks share the exclusive dedup key `gc`, so a trigger of either
  while a run of either is queued or running answers `409` with that job.
- **Result.** The job result carries the fields of [storage.md](storage.md) R-STOR-025:
  `dryRun`, `trashPurged`, `candidates`, `kept`, `deleted`, `bytesReclaimed` and `errors`. In a
  dry run, `deleted` and `bytesReclaimed` count what a real run would delete. These figures feed
  the `trove_gc_*` families (R-OPS-008).
- **Observation and stop.** Runs are listed through `GET /api/v1/system/jobs?kind=gc` and
  `?kind=gc-full` and stopped through `POST /api/v1/system/jobs/{id}/cancel`, after the current
  batch.
- Starting a run writes `system.task_triggered`.
- Permission: system administrator.

**Acceptance criteria:**

- With the trash can disabled (P1, [storage.md](storage.md) R-STOR-022), delete a package version,
  wait for the grace period ([storage.md](storage.md)), then run `POST …/tasks/gc/run` with
  `{"dryRun":true}`. The job result shows `"dryRun": true` and counts the blob in `deleted`, and
  the object still exists in the bucket (`aws s3api head-object --bucket … --key <prefix>/sha256/…`
  succeeds). A real run then removes it, and `trove_gc_deleted_blobs_total` grows.
- `POST …/tasks/gc-full/run` while a `gc` run is queued or running answers `409` with the `gc`
  job's ID.

### R-OPS-021 · Remote cache invalidation
Priority: **MVP** · Covers: API-028

MVP because [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-045, which specifies the
semantics, is MVP and names this operation as its surface, and because the negative cache now lives
in Valkey (INV-3, [architecture.md](architecture.md) R-ARCH-030): this call is the one way to cut
a wrong or forged negative entry short without a Valkey credential (H-49).

`POST /api/v1/repositories/{key}/cache/invalidate`

```json
{"path": "left-pad", "scope": "metadata"}
```

- `path` is a path or package-name prefix within the repository. Empty means the whole repository.
- `scope` is one of:
  - `metadata` (default): cached metadata expires now, and the next request revalidates it against
    the upstream. The stale copy is still served if the upstream fails (INV-11).
  - `negative`: cached misses no longer count. They live in Valkey, are not deleted and expire on
    their own ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-028).
  - `all`: both of the above.
- Cached binaries are content-addressed and immutable, so invalidation never removes them.
  Removing them is a delete ([repositories-and-proxy.md](repositories-and-proxy.md)).
- On a **virtual** repository, the merged metadata cached for the path prefix is dropped. Its
  remote members are not touched.
- On a **local** repository, the call returns `400`.
- **Mechanism.** The call inserts one invalidation row in PostgreSQL (R-REPO-045), and the
  inserting transaction records the change in the change area `cache_invalidation`
  ([architecture.md](architecture.md) R-ARCH-018). It is in force on every replica within
  `cluster.epoch_poll_interval` plus 1 s (2 s with the defaults), with or without Valkey, and
  within milliseconds while Valkey is available. No `NOTIFY` is involved.
- **Negative entries in Valkey are not deleted.** Their keys are hashes and cannot be found by
  prefix, and a deleted key could come back from a primary that missed the delete (R-HA-057 rule 2).
  Readers ignore every negative entry older than a covering invalidation (R-ARCH-030), so the call
  works while Valkey is unavailable and needs no Valkey permission beyond trove's own.
- Response: `200 {"invalidated": 12}`. The count covers metadata entries only: the covered
  metadata entries in PostgreSQL at the time of the call. Negative entries in Valkey are not
  counted, because counting them would need a scan of the keyspace.
- Permission: repository action `manage` on `{key}`, or system administrator.
- Audit: `repository.cache_invalidated`.
- Allowed in read-only mode (R-OPS-024).

**Acceptance criteria:**

- After
  `curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"path":"left-pad"}' https://trove.example.com/api/v1/repositories/npm-remote/cache/invalidate`,
  the next `npm view left-pad --registry …/npm-remote/` produces a `"purpose":"remote"` upstream
  record. A second view within the metadata cache period produces none.
- With Valkey stopped, an invalidation with scope `negative` through one replica makes a request
  for a negatively cached path reach the upstream on every replica within
  `cluster.epoch_poll_interval` plus 1 s. After Valkey returns with the old negative entry still
  present, the entry is ignored (`trove_valkey_cache_lookups_total{result="rejected"}` grows).

### R-OPS-047 · Installation-wide cache flush
Priority: **P1** · Covers: trove-only

An administrator drops every cache that could hold a wrong answer, on every replica, in one call:
after a direct database repair, while diagnosing a suspected cache bug, or after an operator
closed a path that let someone write into Valkey (H-49). It never deletes Valkey keys (Q-OPS-16).

`POST /api/v1/system/caches/flush`

```json
{"inProcess": true, "negative": true}
```

- **`inProcess`** (default `true`): writes a flush entry to the change area `cache`
  ([architecture.md](architecture.md) R-ARCH-018). Every `web` and `worker` replica flushes every
  registered in-process cache through its `OnFlush` hook within `cluster.epoch_poll_interval` plus
  1 s, with or without Valkey, and counts `trove_cache_flushes_total{reason="admin"}`.
- **`negative`** (default `true`): inserts, in one transaction, one invalidation with an empty prefix
  and scope `negative` for every remote repository ([repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-045). Readers then ignore every negative entry written before it (R-ARCH-030); the keys
  expire on their own.
- **Not touched:** flight locks and rate-limit counters, which end within their TTL and window
  (R-ARCH-033, R-ARCH-034); stored remote metadata and cached binaries, which R-OPS-021 and the
  repository delete handle.
- **Cost.** Every replica refills its caches from PostgreSQL, like a start-up (P-15 of
  [architecture.md](architecture.md) R-ARCH-027). The response says so.
- Response: `200 {"inProcess": true, "negativeRepositories": 12}`. Permission: system
  administrator. Audit: `system.caches_flushed`. Allowed in read-only mode (R-OPS-024).

**Acceptance criteria:**

- With three `web` and two `worker` replicas, a flush through one replica raises
  `trove_cache_flushes_total{reason="admin"}` on all five within `cluster.epoch_poll_interval`
  plus 1 s, also with Valkey stopped.
- A negative entry for `left-pad` is written into Valkey with a valid creation time before the
  flush. After the flush, `npm view left-pad --registry …/npm-remote/` reaches the upstream, and
  `valkey-cli EXISTS` still finds the key: trove deleted nothing in Valkey.
- `MONITOR` on the Valkey primary during the flush shows no `SCAN`, `KEYS` or `DEL`.

### R-OPS-022 · Metadata reindex
Priority: **P1** · Covers: API-041, OPS-070

`POST /api/v1/repositories/{key}/reindex` rebuilds the derived metadata of a local repository from
its stored package records, through the format plug-in interface of
[architecture.md](architecture.md). Examples: npm packument documents, NuGet registration pages and
flat-container version lists.

```json
{"path": "@acme/", "mode": "derived", "dryRun": false}
```

- **`mode: derived`** (default) rebuilds from PostgreSQL rows only.
- **`mode: deep`** first re-reads the package manifests from the stored blobs (`package.json`
  inside the tarball, `.nuspec` inside the `.nupkg`) and corrects the stored records.
- **Dry run.** A dry run reports the differences it would correct.
- **Repository class.** Remote and virtual repositories hold no authoritative metadata, so the call
  returns `400` there and points to R-OPS-021.
- **Response and permission.** `202 {"job":{…}}`; `409` while a reindex of the same repository is
  queued or running; `503` in read-only mode. Permission: repository action `manage`, or system
  administrator. Audit: `system.task_triggered`.

**Acceptance criteria:**

- Corrupt the derived packument of `@acme/ui` in a test database, then run a reindex with
  `{"path":"@acme/ui"}`.
- `npm view @acme/ui versions --registry …/npm-local/` lists every stored version again.
- The job result reports one corrected document.

### R-OPS-023 · Storage summary
Priority: **P1** · Covers: API-031, API-032, STOR-039

The `storage-summary` task computes aggregates from PostgreSQL and stores the result. Readers
always get the cached result, never a live computation. This requirement owns the task, its
schedule, the snapshot table and the envelope (`computedAt`, `durationMs`).
[storage.md](storage.md) R-STOR-028 owns the figures and the read path (Q-STOR-10), and the task
is started like every other task, through R-OPS-019 (Q-OPS-9, [api-and-ui.md](api-and-ui.md)
R-API-020: one path per function).

- **Source of the counts.** The aggregates come from the metadata schema of
  [storage.md](storage.md): the task reads the folded counters of R-STOR-003 and the partial
  indexes on `blobs` instead of scanning items (R-STOR-028).
- **Response format.** Numbers are plain integers and ratios. They are not formatted strings,
  unlike the catalog's behaviour.
- **What is counted.** The figures cover what PostgreSQL references. `blobs` already includes
  content referenced only from the trash can and content awaiting collection. The bucket's billed
  usage still differs: it also holds objects of rows in state `deleting`, orphans, and versions
  kept by bucket versioning. S3-compatible stores report no capacity, so `quota` shows only the
  capacity an administrator configured (R-STOR-029), never a measured one.

```sql
CREATE TABLE storage_summary (
  id           smallint    PRIMARY KEY CHECK (id = 1),
  computed_at  timestamptz NOT NULL,
  duration_ms  integer     NOT NULL,
  summary      jsonb       NOT NULL
);
```

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/storage/summary` | `200` the latest snapshot (below; the figures and their definitions are owned by [storage.md](storage.md) R-STOR-028); `404` before the first computation; reads never compute | `storage.read` |
| `POST /api/v1/system/tasks/storage-summary/run` | `202 {"job":{…}}`, or `409` with the running job (R-OPS-019). There is no second refresh path | system administrator |

```json
{
  "computedAt": "2026-10-28T09:17:00.412Z",
  "durationMs": 412,
  "blobs": { "count": 412093, "bytes": 1893341184512 },
  "logical": { "files": 1203381, "folders": 98112, "bytes": 4120938391040 },
  "deduplicationRatio": 0.4594,
  "trash": { "items": 3210, "bytes": 9123012345 },
  "pendingCollection": { "blobs": 812, "bytes": 1029381233 },
  "damaged": { "missing": 0, "corrupt": 0 },
  "quota": { "enabled": true, "capacityBytes": 5497558138880, "usedRatio": 0.3444, "warnPercent": 80, "limitPercent": 95 },
  "repositories": [
    { "key": "npm-local", "class": "local", "format": "npm", "files": 52011, "folders": 8812, "bytes": 81233920123, "packages": 812, "versions": 9120, "shareOfLogicalBytes": 0.0197 }
  ]
}
```

*(example values)* `computedAt` and `durationMs` are this requirement's envelope. Every other
member is defined in R-STOR-028: `deduplicationRatio` is `blobs.bytes / logical.bytes`,
`quota.usedRatio` is `blobs.bytes / capacityBytes` (`null` without a configured capacity), and
`shareOfLogicalBytes` is a repository's share of `logical.bytes` as a ratio from 0 to 1. The
`damaged` counts also feed the gauge `trove_storage_blobs_damaged{status}` (R-OPS-008).

**Acceptance criteria:** after publishing a 1 MiB package to `npm-local` and running
`POST /api/v1/system/tasks/storage-summary/run`, the `npm-local` entry's `bytes` in
`GET /api/v1/storage/summary` grows by the tarball size. `trove_storage_summary_age_seconds`
drops below 60, and a token without `storage.read` gets `403`. The OpenAPI document contains
neither `/api/v1/system/storage/summary` nor a `…/summary/refresh` path.

### R-OPS-024 · Read-only maintenance mode
Priority: **P1** · Covers: trove-only

A system-wide mode stops content and configuration writes while reads keep working, for example
during a storage migration, an incident, or a contract migration (INV-3).

The mode is stored in the key-value table `system_state`, which this requirement defines. The
table itself is MVP, whatever this requirement's priority: the first migration creates it, because
MVP requirements keep keys in it from the first release:

- `installation_id` (R-OPS-004), seeded by the first migration with a random UUID and kept by
  every restore;
- `keyspace_id` ([architecture.md](architecture.md) R-ARCH-030) and `incarnation`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-002), each seeded by a migration with a
  random UUID (`gen_random_uuid()`), so every installation has both before its first replica
  starts; a restore replaces both with new random UUIDs (R-BAK-022);
- `restore_in_progress`, present only while a restore runs, and `restored_from`, written by a
  restore ([backup-and-restore.md](backup-and-restore.md) R-BAK-021, R-BAK-022);
- `restored_at` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-018).

The keys `maintenance_mode`, `banner` and `log_level_override` arrive with their P1 requirements.
The owning page of a key defines its value.

```sql
CREATE TABLE system_state (
  key         text        PRIMARY KEY,   -- e.g. 'installation_id', 'keyspace_id', 'incarnation', 'restore_in_progress', 'restored_from', 'maintenance_mode', 'banner', 'log_level_override'
  value       jsonb       NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid
);
```

API:

| Method and path | Request / response | Permission |
|---|---|---|
| `GET /api/v1/system/maintenance` | `200 {"mode":"read_only","cacheFill":true,"message":"Storage migration until 14:00 UTC","since":"…","by":"…"}` | any authenticated caller |
| `PUT /api/v1/system/maintenance` | `{"mode":"read_only" \| "normal","cacheFill":true,"message":"…","retryAfterSeconds":300}` → `200` | system administrator |

In `read_only` mode:

- **Rejected with `503`.** These requests get `503` with `Retry-After` and the message, as RFC 9457
  problem details on the management API and as the format's own error body on package protocols
  ([formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md)):
  - publish, push, unpublish, delete, deprecate, unlist and dist-tag changes;
  - writes to `/files/`;
  - every mutating `/api/v1` operation except those listed under "still allowed";
  - configuration-as-code apply.
- **Still allowed.** These continue, because people must keep authenticating and must be able to
  revoke a leaked credential during maintenance:
  - downloads and metadata reads;
  - logins, sessions and CI token exchange (INV-10);
  - just-in-time user creation;
  - token creation and revocation;
  - this endpoint, log-level overrides, support bundles, remote cache invalidation (R-OPS-021) and
    the cache flush (R-OPS-047);
  - audit writes and webhook delivery state.
- **Backups continue.** `trove backup create` runs normally: it changes no content, its writes are
  its own run records, pins and audit events ([backup-and-restore.md](backup-and-restore.md)
  R-BAK-009), and a backup right before a maintenance window is a common reason to switch the
  mode on.
- **Pull-through caching** continues while `cacheFill: true`. With `cacheFill: false`, remote
  repositories serve only what they already cache, as in global offline mode
  ([repositories-and-proxy.md](repositories-and-proxy.md), RMT-031).
- **Background work.** The scheduler does not enqueue mutating tasks (`gc`, `gc-full`,
  `orphan-sweep`, retention, reindex, `remote-cache-cleanup`). Runs already in progress finish.
- **Readiness** is unaffected.
- **UI.** The UI shows the mode as a banner (R-OPS-025).

The mode is stored under `system_state` key `maintenance_mode` and written to the change area
`system_state` ([architecture.md](architecture.md) R-ARCH-018), so it is in force on every `web`
and `worker` replica within `cluster.epoch_poll_interval` plus 1 s, whether Valkey is available or
not. Its in-process copy is a registered cache with a `SafetyTTL` of 30 s. Each change writes
`system.maintenance_mode_changed`. `system_state` has the backup export class `data`
([backup-and-restore.md](backup-and-restore.md) R-BAK-004), and a restore rewrites only the keys
that R-BAK-022 lists, so a restore brings back the mode stored in its snapshot. A backup taken in
`read_only` mode, the common case before a maintenance window, therefore restores an installation
in `read_only` mode, and the operator switches it back with `PUT /api/v1/system/maintenance`
after verifying the restore. The restore's own verification runs before any replica starts.

**Acceptance criteria:**

- In `read_only` mode, `npm publish --registry …/npm-local/` fails with `503` and shows the
  message, and `dotnet nuget push …` fails with `503`.
- `npm install` through `npm-all` succeeds, a new personal access token can be created, and an
  existing one revoked.
- All replicas enforce the mode within 2 s of the `PUT`, also with Valkey stopped.
- A `trove backup create` started in `read_only` mode completes.

### R-OPS-025 · System banner
Priority: **P1** · Covers: OPS-055

An administrator sets a message that the UI shows at the top of every screen.

- **Shape.** `{"enabled": true, "severity": "info" | "warning" | "critical", "title": "…", "message": "…"}`.
- **Message format.** The message is plain text with links in Markdown inline syntax
  `[text](https://…)`. The UI renders only text and `https` links, never HTML.
- **Storage.** It is stored under `system_state` key `banner` and propagated through the change area
  `system_state`, like the maintenance mode (R-OPS-024).
- **Maintenance mode.** While read-only mode is on, the UI also shows the maintenance message with
  severity `warning`, whether or not a banner is set.

| Method and path | Response | Permission |
|---|---|---|
| `GET /api/v1/system/banner` | `200` banner, or `{"enabled":false}`; also served to anonymous callers, because the login page shows it | none |
| `PUT /api/v1/system/banner` | `200` | system administrator |

Audit: `system.banner_changed`.

**Acceptance criteria:**

- A banner with message `see [status](javascript:alert(1))` is stored, but the UI renders the link
  as plain text.
- A banner set through one replica is returned by every replica within 2 s, also with Valkey stopped.

### R-OPS-026 · Coordination lock diagnostics
Priority: **P1** · Covers: ADMAPI-078, OPS-009

`GET /api/v1/system/locks?category=` (requires `system.read`) shows the PostgreSQL
[advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) held
or awaited by trove, so stuck leases, a waiting migration or a contended configuration apply can be
diagnosed without database access. Since 2026-10-28 these are mutual exclusions whose failure
would corrupt data (INV-3); the cross-replica fetch lock lives in Valkey and is not part of this
view (Q-OPS-17).

- **How it is read.** The handler queries
  [`pg_locks`](https://www.postgresql.org/docs/current/view-pg-locks.html) (`locktype = 'advisory'`)
  joined with `pg_stat_activity`, in one transaction on the request pool, so it works through a
  transaction-mode pooler. It does not fan out to other replicas.
- **Requirements on [architecture.md](architecture.md)** (R-ARCH-017, R-ARCH-019), already
  specified there:
  - every trove connection sets `application_name` to `trove:<replica>:<class>`, which maps a
    lock to its replica and connection class;
  - advisory locks use the two-key form, with the first key naming a registered category
    (`lease`, `migration`, `config`). Category 2 (`fetch`) is retired and its number stays
    reserved; no lock of it appears in `pg_locks`, because flight locks are Valkey keys
    ([architecture.md](architecture.md) R-ARCH-017, R-ARCH-033), and the view never shows them.
- **What it shows:**
  - category;
  - key: the registered name for leases and configuration objects, the step for migrations;
  - replica, connection class, backend PID, whether the lock is granted;
  - `waitStart` for waiters;
  - `heldSince` for leases, taken from the replica registry (R-OPS-003); `null` where PostgreSQL
    does not record it.
- Leases are held only by `worker` replicas (on `lease` connections), the migration lock by the
  migration Job, the restore Job or a backup run in its database phase
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-005), and `config` locks only for the
  length of one transaction.
- **Collapsed fetches** are observed through `trove_remote_collapsed_requests_total`, the
  `trove_flight_lock_*` families (R-OPS-008), the leader's upstream record with `flight_role` and
  `collapsed_waiters` and its trace (R-OPS-011), and the `collapsed` parameter of `Cache-Status`
  on the waiters' responses ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-047).
- **Limits.** The result is a point-in-time snapshot and not atomic.

```json
{
  "snapshotAt": "2026-10-28T12:01:00.000Z",
  "items": [
    {"category": "lease", "key": "scheduler", "replica": "trove-worker-7c4d9f6b8-m3k8p", "class": "lease", "pid": 4211, "granted": true, "heldSince": "2026-10-28T08:01:15.880Z", "waitStart": null},
    {"category": "migration", "key": "0", "replica": "trove-backup-29361600-q7x2d", "class": "backup", "pid": 4310, "granted": true, "heldSince": null, "waitStart": null}
  ]
}
```

**Acceptance criteria:**

- The `scheduler` lease appears exactly once, with the `worker` replica that `trove_lease_held`
  names, and no item names a `web` replica with class `lease`.
- While a backup run is in its database phase, its shared migration lock appears with class
  `backup`.
- Ten parallel cold `npm install` runs of the same package show no `fetch` item; the upstream log
  holds one record with `"flight_role":"leader"` for the tarball, and
  `trove_remote_collapsed_requests_total` grows.

### R-OPS-027 · Support bundle
Priority: **P1** · Covers: OPS-031, ADMAPI-081

A support bundle collects diagnostics from every live replica into one downloadable archive.
trove has no log files, so logs come from each replica's ring buffer (R-OPS-009). Anything older
must be fetched from the platform's log stack.

| Method and path | Request / response | Permission |
|---|---|---|
| `POST /api/v1/system/support-bundles` | parameters below → `202 {"id":"…","job":{…}}` | system administrator |
| `GET /api/v1/system/support-bundles` | `200` list: `id`, `name`, `description`, `state`, `createdAt`, `createdBy`, `expiresAt`, `sizeBytes` | system administrator |
| `GET /api/v1/system/support-bundles/{id}` | `200` metadata, parameters and part list; `404` | system administrator |
| `GET /api/v1/system/support-bundles/{id}/download` | `200 application/x-tar` stream; `409` while collecting; `404` | system administrator |
| `DELETE /api/v1/system/support-bundles/{id}` | `204`; `404` | system administrator |

```json
{
  "name": "slow-npm-installs",
  "description": "Case 2026-10-28",
  "include": {
    "systemInfo": true,
    "effectiveConfig": true,
    "configExport": true,
    "logs": true,
    "goroutineDumps": 1,
    "goroutineDumpInterval": "0s",
    "heapProfile": true,
    "cpuProfileSeconds": 0,
    "metrics": true,
    "database": true,
    "slowQueries": false,
    "jobs": true
  }
}
```

**Collection.**

1. The request creates the bundle row and enqueues a collection job in one transaction, which also
   writes the change area `support` ([architecture.md](architecture.md) R-ARCH-018). The row
   records the expected parts: one `replica/<name>` per replica that the registry shows as live in
   that transaction (R-OPS-003). The set lives in PostgreSQL, so a collection job that a drain
   hands to another `worker` knows it too (INV-18).
2. Every live `web` and `worker` replica learns of the bundle through that area, within
   `cluster.epoch_poll_interval` plus 1 s, writes its own part and uploads it to the blob store:
   ring-buffer logs, goroutine dumps and profiles from
   [`runtime/pprof`](https://pkg.go.dev/runtime/pprof), a metrics snapshot, readiness details, its
   pool statistics and its Valkey client state (current primary, circuit state, Sentinels
   reachable, the last errors by kind; never credentials). Only the replica itself can capture
   these, so on a `web` replica this is background work that outlasts the request that started
   it: bounded by `support.max_cpu_profile_seconds` plus the upload and never past
   `support.collect_timeout`, abandoned at `SIGTERM` (R-OPS-001), and listed as a bounded
   exception of [architecture.md](architecture.md) R-ARCH-001 (cross-page). It holds no database
   connection while it captures or uploads.
3. The collection job, on a `worker` replica, adds the cluster part. The job is idempotent: a job
   released at a drain resumes on another `worker` and replaces the cluster part (an upsert on
   `(bundle_id, name)`). The cluster part holds:
   - system information;
   - effective configuration (R-OPS-028);
   - a configuration export without secrets (R-OPS-032);
   - replicas, tasks, the last 100 failed jobs, locks;
   - the backup runs of the last 30 days from `backup_run`, with their target as stored (bucket,
     prefix and endpoint host, no credentials; [backup-and-restore.md](backup-and-restore.md)
     R-BAK-009);
   - schema version, table sizes and trove's sessions from `pg_stat_activity`;
   - with `slowQueries`, the top 20 statements from
     [`pg_stat_statements`](https://www.postgresql.org/docs/current/pgstatstatements.html) where
     that extension is installed.
4. The bundle turns `ready` when every expected part and the cluster part exist, or `partial`
   after `support.collect_timeout`. A replica that is drained during the collection makes the
   bundle `partial`; its logs are in the platform's log stack.

Any `web` replica serves the download, because the parts are blobs (INV-18).

**Parts as blobs.** Parts are content-addressed blobs (INV-4). Their references live in
`support_bundle_part`, and storage.md's garbage collection counts them. Deleting or expiring a
bundle drops the references, and the blobs follow.

**Manifest.** Every bundle carries a `MANIFEST.json` listing every included item, so an operator
can review the contents before handing them on.

**Never included:** audit events, user and group lists, token records, package content, request
bodies. Everything included passes through R-OPS-013.

```sql
CREATE TABLE support_bundle (
  id            uuid        PRIMARY KEY,
  name          text        NOT NULL,
  description   text        NOT NULL DEFAULT '',
  parameters    jsonb       NOT NULL,
  expected_parts text[]     NOT NULL,   -- 'replica/<name>' of every replica live at creation (R-OPS-003)
  state         text        NOT NULL CHECK (state IN ('collecting','ready','partial','failed')),
  created_by    uuid        NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  completed_at  timestamptz,
  expires_at    timestamptz NOT NULL
);
CREATE TABLE support_bundle_part (
  bundle_id     uuid        NOT NULL REFERENCES support_bundle (id) ON DELETE CASCADE,
  name          text        NOT NULL,   -- 'cluster' or 'replica/<name>'
  blob_sha256   bytea       NOT NULL,   -- blob reference (INV-4), counted by garbage collection
  size_bytes    bigint      NOT NULL,
  PRIMARY KEY (bundle_id, name)
);
```

```yaml
support:
  bundle_retention_days: 7        # default (proposed)
  collect_timeout: 2m             # default (proposed)
  max_cpu_profile_seconds: 60     # default (proposed)
```

**Acceptance criteria:**

- With three `web` and two `worker` replicas, a bundle reaches `ready` and its tar contains
  `MANIFEST.json`, `cluster.tar.gz` and five `replica/<name>.tar.gz` parts, also with Valkey
  stopped.
- `tar -xOf bundle.tar | grep -aE 'trv_(pat|sat|cit)_[0-9A-Za-z]{10,}'` finds nothing.
- Eight days later the bundle is gone, and after the grace period its blobs are gone too.

## Configuration

### R-OPS-028 · Configuration file and environment mapping
Priority: **MVP** · Covers: OPS-040, HA-036

trove reads one YAML file at startup (INV-15). The default path is `/etc/trove/trove.yaml`.
The `--config` flag or the reserved variable `TROVE_CONFIG_FILE` overrides it.

**Precedence**, from lowest to highest:

1. built-in defaults;
2. the file;
3. environment variables;
4. the few command-line flags (`--config`, `--log-level`, and `--role` of `trove serve`, which
   overrides `process.role`; [architecture.md](architecture.md) R-ARCH-001).

One file serves every role: the chart renders one configuration for the `web` and `worker`
Deployments, the backup CronJob and the restore Job. Only the invocation differs: `--role` of
`trove serve` for the Deployments, the `trove backup` subcommand for the Jobs
([ha-and-helm.md](ha-and-helm.md) R-HA-002, R-HA-060). Every role validates the syntax of every key, but
opens only the secret files of the keys it uses: `web` and `worker` replicas never open the backup
target's credential files, which the chart mounts into backup and restore pods only
([backup-and-restore.md](backup-and-restore.md) R-BAK-011).

**Environment mapping.** Every key has exactly one variable: `TROVE_` followed by the key path,
upper-cased, with path segments joined by `_`. Example: `log.request.enabled` maps to
`TROVE_LOG_REQUEST_ENABLED`.

- **Ambiguity.** Keys contain underscores themselves, so a variable is never split naively. The
  names are generated from the configuration schema and matched exactly.
- **Value types.** Lists and maps in variables are JSON (`TROVE_LOG_LEVELS='{"proxy":"debug"}'`),
  booleans are `true`/`false`, durations are Go durations.
- **Values that differ per Deployment or Job.** The file is shared, so the few values that differ
  between the `web` and `worker` Deployments, their pools and the Jobs arrive as ordinary mapped
  variables that the chart sets ([ha-and-helm.md](ha-and-helm.md) R-HA-030): per Deployment
  `TROVE_RUNTIME_MEMORY_LIMIT_BYTES`, `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD` and
  `TROVE_STORAGE_SPOOL_MAX_BYTES` (`storage.spool.max_bytes`, derived from the role's spool size
  unless `config.storage.spool.max_bytes` is set, R-HA-035); for the migration Job
  `TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS` and
  `TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS` (`database.preflight.expected_web_replicas`
  and `database.preflight.expected_worker_replicas`, the connection budget check of
  [ha-and-helm.md](ha-and-helm.md) R-HA-016). They are schema keys like any other and pass strict
  validation.

**Strict validation.** An unknown key in the file or an unknown `TROVE_` variable stops startup
with an error that names it and the closest valid key. So does a type or range error. Every error
is reported in one pass. A mistyped override therefore fails loudly instead of being dropped. The
only exempt variables are the reserved ones: `TROVE_CONFIG_FILE`; `TROVE_CAC_*` for
configuration-as-code secret references (R-OPS-029); `TROVE_POD_NAME`, `TROVE_POD_NAMESPACE`
and `TROVE_NODE_NAME`, which the chart sets in every trove pod from the downward API
(`metadata.name`, `metadata.namespace`, `spec.nodeName`) for the replica registry, the `replica`
field of every log record and the trace resource (R-OPS-003, R-OPS-009, R-OPS-018); and
`TROVE_POOL_NAME`, which the chart sets on each pool Deployment: from the MVP on the endpoint
pools of [endpoints.md](endpoints.md) R-EP-008, whose processes open only the listeners of their
endpoints (R-EP-002), and from P1 the pools of [ha-and-helm.md](ha-and-helm.md) R-HA-062; it also
fills the registry's `pool` column (R-OPS-003). Without
the reservation, strict validation would stop every pod that carries them.

**Secrets.** Every secret-typed key `<name>` has a companion `<name>_file`, and both can be set
through the environment. A secret value written inline into the YAML file MUST stop startup with an
error, because the file is typically a ConfigMap (INV-15). Setting both the value and `_file` is an
error. A section owner MAY declare a secret *file-only*: it then has only the `<name>_file` key,
which is read again on every use, and no value key. The Valkey and Sentinel credentials are
file-only, so a rotated Secret applies without a restart ([ha-and-helm.md](ha-and-helm.md)
R-HA-056); the database credential follows R-HA-017.

**Changes take effect on restart.** The only live-adjustable item is the log-level override
(R-OPS-012). The chart rolls the pods of both Deployments when the rendered file changes
([ha-and-helm.md](ha-and-helm.md)); each Deployment rolls under its own disruption budget, so a
configuration change is a rolling restart without outage (R-HA-059).

**Commands and API:**

- `trove admin config validate [--config <file>]` validates the file plus the environment and
  prints the effective configuration, redacted, with each value's source.
- `GET /api/v1/system/config` returns the same data (`system.read`):
  `{"values": {"log.level": {"value": "info", "source": "default"}, "mail.password": {"value": "[REDACTED]", "source": "file:/var/run/secrets/trove/smtp"}}}`.

**Sections and their owners.** Each owner page lists its own keys. This page lists the keys of the
sections it owns, in the requirements above and below.

| Section | Owner |
|---|---|
| `server` | [architecture.md](architecture.md) (listeners, timeouts, shutdown, the content origin `content_base_url` and `content_url_ttl` of R-ARCH-029); this page (`base_url`, `trusted_proxies`, `additional_base_urls`, `replica_header`); [endpoints.md](endpoints.md) (`endpoints`, `endpoint_rate_limits`) |
| `database`, `migrate`, `runtime` | [ha-and-helm.md](ha-and-helm.md) |
| `valkey` | [ha-and-helm.md](ha-and-helm.md) R-HA-056; [architecture.md](architecture.md) R-ARCH-031 adds `valkey.sentinel.refresh_interval` |
| `backup` | [backup-and-restore.md](backup-and-restore.md) R-BAK-011; [ha-and-helm.md](ha-and-helm.md) adds `backup.recovery_window` and `backup.object_versioning` (R-HA-023) and `backup.revocation_journal` (R-HA-054) |
| `storage`, `retention` | [storage.md](storage.md) |
| `auth` | [identity-and-authentication.md](identity-and-authentication.md) |
| `authz` | [authorization.md](authorization.md) |
| `ci` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `proxy` (including `proxy.outbound`), `repositories` | [repositories-and-proxy.md](repositories-and-proxy.md) |
| `npm`, `nuget` | [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| `api`, `ui` | [api-and-ui.md](api-and-ui.md) |
| `process`, `jobs`, `cluster`, `cache`, `secrets` | [architecture.md](architecture.md); [ha-and-helm.md](ha-and-helm.md) adds `jobs.background_migrations` |
| `system`, `health`, `log`, `metrics`, `tracing`, `audit`, `maintenance`, `support`, `config_as_code`, `webhooks`, `mail`, `content_types` | this page |

<details>
<summary>Example file (keys of this page; other sections abbreviated)</summary>

```yaml
server:
  base_url: https://trove.example.com            # example
  trusted_proxies: ["10.42.0.0/16"]                # example
  additional_base_urls: []                         # default (proposed)
  replica_header: authenticated                    # default (proposed)
system:
  public_version: false                            # default (proposed)
  heartbeat_interval: 10s                          # default (proposed)
  heartbeat_stale_after: 30s                       # default (proposed)
health:
  check_interval: 5s                               # default (proposed)
  check_timeout: 2s                                # default (proposed)
  failure_threshold: 2                             # default (proposed)
  success_threshold: 1                             # default (proposed)
  require_object_store: false                      # default (proposed)
  watchdog_timeout: 2m                             # default (proposed)
  shutdown_drain_delay: 5s                         # default (proposed)
  database_outage_grace: 20s                       # default (proposed)
log:
  format: json                                     # default (proposed)
  level: info                                      # default (proposed)
  levels: {}                                       # default (proposed)
  add_source: false                                # default (proposed)
  ring_buffer_records: 5000                        # default (proposed)
  override_max_duration: 24h                       # default (proposed)
  request: {enabled: true, ops_listener: false}    # default (proposed)
  upstream: {enabled: true}                        # default (proposed)
metrics:
  enabled: true                                    # default (proposed)
  auth: none                                       # default (proposed)
  bearer_token_file: ""                            # default (proposed)
  label_limits: {repository: 500, subscription: 100}  # default (proposed)
  repository_label_include: []                     # default (proposed)
  slow_query_threshold: 500ms                      # default (proposed)
  db_query_histograms: false                       # default (proposed)
  native_histograms: false                         # default (proposed)
  db_gauge_interval: 30s                           # default (proposed)
tracing:
  enabled: false                                   # default (proposed)
  endpoint: ""                                     # default (proposed)
  protocol: http/protobuf                          # default (proposed)
  sample_ratio: 0.1                                # default (proposed)
  incoming_context: trusted_proxies                # default (proposed)
  propagate_to_upstreams: false                    # default (proposed)
audit:
  retention_days: 365                              # default (proposed)
  async_buffer: 10000                              # default (proposed)
  denial_dedup_window: 1m                          # default (proposed)
  token_rejection_dedup_window: 5m                 # default (proposed)
  record_anonymous_denials: false                  # default (proposed)
  record_downloads: false                          # default (proposed)
maintenance:
  tasks:
    storage-summary: {enabled: true, schedule: "17 * * * *"}   # default (proposed)
support:
  bundle_retention_days: 7                         # default (proposed)
  collect_timeout: 2m                              # default (proposed)
config_as_code:
  files: []                                        # default (proposed); example ["/etc/trove/config.d/*.yaml"]
  mode: apply                                      # default (proposed); apply | create_only
  secrets_dir: /etc/trove/cac-secrets            # default (proposed)
  lock_managed_objects: true                       # default (proposed)
webhooks:
  enabled: true                                    # default (proposed); see R-OPS-042 for the rest
mail:
  enabled: false                                   # default (proposed)
  host: smtp.example.com                           # example
  password_file: /var/run/secrets/trove/smtp     # example
content_types:
  overrides: {}                                    # default (proposed)
```

</details>

**Acceptance criteria:**

- `TROVE_LOG_LEVL=debug trove serve` exits non-zero with a message naming `TROVE_LOG_LEVL`
  and suggesting `TROVE_LOG_LEVEL`.
- `mail.password: hunter2` in the file stops startup with an error that names `mail.password_file`
  and `TROVE_MAIL_PASSWORD`.
- `TROVE_METRICS_LABEL_LIMITS_REPOSITORY=50` is reported by `GET /api/v1/system/config` with
  source `env`.

### R-OPS-029 · Configuration-as-code document
Priority: **P1** · Covers: OPS-043, OPS-044, OPS-045

A configuration document declares the desired state of database-held configuration (INV-15). It
can be applied through the API (R-OPS-030), through the CLI, or at startup (R-OPS-031).

```yaml
apiVersion: trove/v1
kind: Configuration
metadata:
  name: platform-baseline          # document identity; owns every object it declares
  prune: true                      # delete owned objects that are no longer declared
spec:
  ciIssuers:                       # trusted issuers for the CI exchange (gitlab-ci-oidc.md); not login providers
    - name: gitlab
      issuer: https://gitlab.example.com
      audiences: ["https://trove.example.com"]
  groups:                          # group records only; claim-to-group rules live in the configuration file
    - name: developers
      description: All developers
  serviceAccounts:
    - name: renovate
      description: Dependency update bot
  repositories:
    - key: npm-remote
      format: npm
      class: remote
      remote:
        url: https://registry.npmjs.org/
    - key: npm-local
      format: npm
      class: local
    - key: npm-all
      format: npm
      class: virtual
      virtual:
        members: [npm-local, npm-remote]
        deployTarget: npm-local
    - key: nuget-remote
      format: nuget
      class: remote
      remote:
        url: https://nuget.partner.example.com/v3/index.json
        password: {secretRef: {file: nuget-upstream-token}}
  permissionPolicies:
    - name: developers-read
      principals: {groups: [developers]}
      resources: [{repositories: ["npm-*", "nuget-*"]}]
      actions: [read]
  ciMappings:
    - name: acme-publish
      issuer: gitlab               # the CI issuer the mapping belongs to (path parameter of its API)
      match: {namespace_path: acme, ref_protected: "true"}
      grants:
        - repositories: [npm-local]
          packages: ["@acme/*"]
          actions: [read, deploy]
  webhooks: []                     # R-OPS-038; secrets as secretRef
```

**Object bodies.** Every object in `spec` is the request representation of the matching create
operation in the OpenAPI document. Field names, validation and defaults are those of the owning
page:

| `spec` key | Owner |
|---|---|
| `repositories` | [repositories-and-proxy.md](repositories-and-proxy.md) |
| `permissionPolicies` | [authorization.md](authorization.md) |
| `groups`, `serviceAccounts` | [identity-and-authentication.md](identity-and-authentication.md) |
| `ciIssuers`, `ciMappings` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `retentionPolicies` | [storage.md](storage.md) |
| `webhooks` | R-OPS-038 |

So the document needs no second schema: the OpenAPI document generates its JSON Schema.

**Login providers are not a document kind** (Q-OPS-6). The browser-login OIDC clients (INV-7) live
in the configuration file ([identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-004), so login works before any document is applied and a broken apply cannot lock
administrators out. That covers the whole provider entry, including its claim mapping and its
claim-to-group rules (R-AUTH-008): a document declares group records, never which claim values
lead into them. A document that contains a `loginProviders` key, or a group with a claim-mapping
field, is rejected with `422`. The CI issuers of `spec.ciIssuers` are a different thing: they are
trusted for the CI token exchange only (INV-10) and never log anyone in.

**An object is the complete desired state of that object.** Omitted fields take their defaults and
are not merged with the stored values. This makes the result independent of history, and it is
what a Git review shows. There is no partial-patch form, and there is no `null`-means-delete
convention: an object is deleted by removing it from a document that has `prune: true`.

**Ownership.** Each object is owned by at most one document, recorded in
`config_managed_object`.

- **Conflicts.** A document that declares an object owned by another document, or an existing
  unmanaged object with the same key, has a conflict (R-OPS-030), unless the apply request sets
  `adopt: true`.
- **Locking.** With `config_as_code.lock_managed_objects: true` (the default), the API and UI
  reject changes to a managed object's configuration with `409` and name the owning document
  (Q-OPS-8). The way to change a locked object, including in an emergency, is to apply a corrected
  document through the API or the CLI (R-OPS-030); the CLI's database mode works when no
  administrator can log in. `adopt: true` moves an object to another document.
- **Content is unaffected.** Publishing into a managed repository is not a configuration change and
  is not affected.

**Secrets.** A secret field takes `{"secretRef": {"file": "<name>"}}` or
`{"secretRef": {"env": "TROVE_CAC_<NAME>"}}`, resolved on the server.

- **Where files may live.** A file must lie directly under `config_as_code.secrets_dir`. No path
  separators and no `..`.
- **Which variables may be read.** Only those with the `TROVE_CAC_` prefix. trove's own secrets
  (database password, object-store key) can therefore never be routed into, say, a remote
  repository's upstream credentials and sent to a host of the author's choice.
- **Inline values.** A literal `{"value": "…"}` is accepted only through the API (R-OPS-030) and
  rejected in startup files.

```sql
CREATE TABLE config_document (
  name         text        PRIMARY KEY,
  digest       bytea       NOT NULL,     -- SHA-256 of the canonical document (secrets as fingerprints)
  applied_at   timestamptz NOT NULL,
  applied_by   uuid,                     -- null for startup and CLI-with-database applies
  source       text        NOT NULL CHECK (source IN ('api','startup','cli'))
);
CREATE TABLE config_managed_object (
  kind         text        NOT NULL,     -- 'repository', 'permissionPolicy', …
  object_key   text        NOT NULL,
  document     text        NOT NULL REFERENCES config_document (name),
  fingerprint  bytea       NOT NULL,     -- HMAC-SHA-256 of the applied canonical object
  PRIMARY KEY (kind, object_key)
);
```

**Secret fingerprints.** Secret values are never compared in clear text. A diff compares an
HMAC-SHA-256 fingerprint, keyed with the installation's stored-secret key (IAM-045,
[architecture.md](architecture.md)), and reports only "secret changed". An unkeyed hash would let
anyone with database read access guess short secrets offline.

**Acceptance criteria:** the document above validates against the JSON Schema generated from the
OpenAPI document. A document with `secretRef: {file: ../db-password}` or
`secretRef: {env: TROVE_DATABASE_PASSWORD}` is rejected with `422`, and the error names the
field. So is a document with a `spec.loginProviders` key, or with `claimValues` on a group.
With the default lock, `PUT /api/v1/repositories/npm-local` with the current `If-Match` on a
repository owned by `platform-baseline` answers `409`, and the problem names the document.

### R-OPS-030 · Diff and apply
Priority: **P1** · Covers: OPS-043, OPS-045, ADMAPI-073

| Method and path | Request | Response | Permission |
|---|---|---|---|
| `POST /api/v1/config/diff` | the document (`application/yaml` or `application/json`); query `adopt`, `allowDestructive` | `200` plan; `422` validation errors with JSON pointers | `config.read` |
| `POST /api/v1/config/apply` | the document; query `adopt`, `allowDestructive`, `expectedPlanHash` | `200` applied plan; `409` plan has conflicts or blocked changes (nothing applied); `412` `expectedPlanHash` differs (fresh plan in body); `422` invalid; `503` read-only mode | system administrator |

```json
{
  "document": "platform-baseline",
  "planHash": "sha256:5d1f…",
  "changes": [
    {"kind": "repository", "key": "npm-remote", "action": "create"},
    {"kind": "repository", "key": "npm-all", "action": "update",
     "fields": [{"path": "/virtual/members", "from": ["npm-local"], "to": ["npm-local", "npm-remote"]}]},
    {"kind": "repository", "key": "nuget-remote", "action": "update",
     "fields": [{"path": "/remote/password", "change": "secret changed"}]},
    {"kind": "permissionPolicy", "key": "old-readers", "action": "delete"},
    {"kind": "repository", "key": "legacy-npm", "action": "delete", "blocked": "repository holds 1204 files; requires allowDestructive"},
    {"kind": "repository", "key": "npm-local", "action": "conflict", "reason": "exists and is not managed by this document; requires adopt"}
  ],
  "summary": {"create": 1, "update": 2, "delete": 2, "unchanged": 14, "conflict": 1, "blocked": 1}
}
```

**Validation.** The whole document is validated before any change: schemas, references between
objects (in the document or in the database), and cross-object rules such as virtual members of the
same format.

**The plan hash** is SHA-256 over the canonical list of changes. A pipeline can run `diff` in
review and `apply` with `expectedPlanHash` after approval. If anything changed in between, the apply
returns `412` instead of applying an unreviewed plan.

**Atomic apply.**

- Apply runs in one PostgreSQL transaction that first takes the transaction-scoped advisory lock of
  the category `config` with the object number registered for configuration documents
  (`pg_advisory_xact_lock(4, <n>)`, [architecture.md](architecture.md) R-ARCH-017, cross-page). Every
  change commits, or none does. The lock ends with the transaction, so the apply works through a
  transaction-mode pooler and holds no session connection.
- Creates and updates run in dependency order: `ciIssuers`, `groups`, `serviceAccounts`, local
  and remote `repositories`, virtual `repositories`, `ciMappings`, `permissionPolicies`,
  `webhooks`, `retentionPolicies`. Deletes run in reverse order.
- Other replicas learn of the changes through the change areas of the changed objects
  ([architecture.md](architecture.md) R-ARCH-018), within `cluster.epoch_poll_interval` plus 1 s,
  with or without Valkey.

**Destructive safety.**

- Deleting a repository that holds content, or deleting a permission policy or identity mapping
  that is in use (granted in the last 30 days), is `blocked` unless `allowDestructive=true`.
- A document whose `spec` is empty while `prune: true` would delete everything the document owns.
  It is always `blocked` without `allowDestructive=true`.
- An empty or truncated document rendered by broken automation can therefore never wipe the
  configuration.

**Audit.** Every apply writes one `system.config_applied` event with the summary and digest, plus
the per-object events of R-OPS-016.

**CLI.** `trove admin config diff -f <file>` and `trove admin config apply -f <file> [--adopt]
[--allow-destructive] [--plan-hash <hash>]` work in two modes:

- with `--server <url> --token-file <path>`, through this API;
- with the database settings of the configuration file, directly against PostgreSQL. This mode is
  for bootstrap and recovery when no administrator can log in (INV-2), and it records `source: cli`.
  It writes the same change areas as the API, so running replicas apply the result within the same
  bound; it needs no Valkey.

**Acceptance criteria:**

- `curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/yaml' --data-binary @baseline.yaml https://trove.example.com/api/v1/config/diff | jq .summary`
  shows the expected counts. Applying with the returned `planHash` succeeds, and a second diff
  shows only `unchanged`.
- Diff and apply with `adopt=true`. Changing, through the UI, a repository that the document
  adopts (it is not managed yet, so it is not locked, Q-OPS-8) between diff and apply makes the
  apply return `412`.
- Applying a document with an empty `spec` and `prune: true` returns `409` and changes nothing.
- A fault injected in the middle of an apply leaves the database without any of its changes.

### R-OPS-031 · Startup bootstrap from configuration documents
Priority: **P1** · Covers: OPS-046, HA-037

When `config_as_code.files` names files (glob patterns, applied in lexical order), each `web` and
`worker` replica does the following at startup, after the schema check and before it reports
ready:

1. **Lock and apply.** It applies each document in a transaction that takes the `config` lock of
   R-OPS-030 first and computes the diff under it. Replicas of both roles that start together
   therefore apply once, and the rest see an empty diff.
2. **Mode.**
   - `mode: apply` reconciles: create, update, and prune per document. Changes that need
     `allowDestructive` are never applied at startup.
   - `mode: create_only` creates missing objects and never updates or deletes. Objects created this
     way are not managed, which gives a seed for a fresh installation that is then maintained
     through the UI.
3. **Failure.** When a document is invalid, has conflicts or contains blocked changes, the replica
   logs the plan, keeps the `configBootstrap` readiness check failing, and retries every minute. In
   a rolling update the new pods of both Deployments never become ready, so both rollouts stall
   while the old replicas keep serving and keep claiming jobs. The operator resolves the problem
   with the CLI or API (R-OPS-030).

Documents come from a ConfigMap and secret references from a Secret mounted at `secrets_dir`
([ha-and-helm.md](ha-and-helm.md)). The break-glass administrator (INV-7) is not part of a
document. Its credential comes from its own Kubernetes Secret
([identity-and-authentication.md](identity-and-authentication.md)).

**Acceptance criteria:**

- `helm install` with a ConfigMap holding the baseline document produces the repositories before
  the first pod reports ready, and `npm install --registry …/npm-all/` works without a manual step.
- Upgrading with a document that has a typo in a repository format leaves the old pods serving, and
  `kubectl rollout status` does not complete.

### R-OPS-032 · Configuration export
Priority: **P1** · Covers: ADMAPI-072, ADMAPI-084

`GET /api/v1/config/export` (requires `config.read`) returns the current database-held
configuration as a document (R-OPS-029). It is a logical configuration backup, and the starting
point for moving an existing installation to configuration as code.

Query parameters:

- `kinds`: comma-separated; default all;
- `managedBy=<document>`: only that document's objects;
- `unmanaged=true`: only unmanaged objects.

The response:

- is `application/yaml` (or JSON through `Accept`);
- has `metadata.name: export-<timestamp>` and `prune: false`;
- replaces every secret with `{"secretRef": {"file": "<kind>-<key>-<field>"}}`; secret values are
  never exported.

Audit: `system.config_exported`.

**Acceptance criteria:**

- Export from an installation configured through the UI, then apply the export to an empty
  installation with matching secret files and `adopt=true`.
- A diff between the two installations' exports is empty.
- `grep -E 'trv_|BEGIN .*PRIVATE KEY'` over the export finds nothing.

### R-OPS-033 · Outgoing mail
Priority: **P1** · Covers: OPS-060

trove sends mail through one SMTP relay for features that declare mail notifications. Token
expiry notices (TOK-023) are one example.

- **TLS.** Connections use STARTTLS by default. `tls: none` is accepted only together with
  `allow_insecure: true`, and logs a warning at startup.
- **Links.** Every link in a mail is built from `server.base_url`.
- **Test.** `POST /api/v1/system/mail/test` with `{"to":"ops@example.com"}` sends a test message.
  It answers `200 {"delivered":true}`, or `502` with the SMTP error, redacted. Permission: system
  administrator.

```yaml
mail:
  enabled: false                  # default (proposed)
  host: smtp.example.com          # example
  port: 587                       # default (proposed)
  tls: starttls                   # default (proposed); starttls | implicit | none
  allow_insecure: false           # default (proposed)
  username: trove               # example
  password_file: /var/run/secrets/trove/smtp   # example (or TROVE_MAIL_PASSWORD)
  from: "trove <trove@example.com>"          # example
  subject_prefix: "[trove] "    # default (proposed)
```

**Acceptance criteria:** against a test SMTP server (for example Mailpit) with STARTTLS, the test
endpoint delivers one message whose `From` and subject prefix match the configuration. With
`tls: none` and no `allow_insecure`, startup fails.

### R-OPS-034 · Content types for raw downloads
Priority: **P1** · Covers: OPS-050

Raw file downloads (`/files/<repoKey>/<path>`, INV-6) and the UI's file viewer take their
`Content-Type` from one table.

**Building the table.** The table is built in this order:

1. Go's [`mime`](https://pkg.go.dev/mime) package built-ins;
2. trove's additions (`.tgz` → `application/gzip`, `.nupkg` and `.snupkg` → `application/zip`,
   `.nuspec` → `application/xml`, …);
3. `content_types.overrides` from the configuration file (restart required).

**Matching.** The longest matching suffix wins, so `.tar.gz` beats `.gz`. An unknown extension
gets `application/octet-stream`.

**Response headers.** Package content is uploaded by users and served from the same origin as the
UI, so every raw download MUST carry, whatever its content type:

- `X-Content-Type-Options: nosniff`;
- `Content-Security-Policy: sandbox; default-src 'none'`;
- `Cross-Origin-Resource-Policy: same-site`;
- `Content-Disposition: attachment; filename*=UTF-8''<percent-encoded file name>`, on every
  `/files/` response, not only for types a browser would render ([storage.md](storage.md)
  R-STOR-012).

These are the content headers of [architecture.md](architecture.md) R-ARCH-007. They are required
from the MVP by R-ARCH-007 and R-STOR-012, whatever this requirement's priority. The core sets them
through a response wrapper that no handler can weaken, and an override in this table cannot remove
them. With the separate content origin of R-ARCH-029 (P1), responses on the content host carry
`Cross-Origin-Resource-Policy: cross-origin` instead of `same-site` and add
`Referrer-Policy: no-referrer`; R-ARCH-029 owns that difference.

Package-protocol endpoints set the content types their clients expect (format pages), not this
table. They carry the content headers of R-ARCH-007 as well.

```yaml
content_types:
  overrides: {}                   # default (proposed); example {".mjs": "text/javascript", ".sbom.json": "application/json"}
```

**Acceptance criteria:**

- `curl -sI …/files/generic-local/docs/index.html` shows `Content-Disposition: attachment`,
  `Content-Security-Policy: sandbox; default-src 'none'`, `X-Content-Type-Options: nosniff` and
  `Cross-Origin-Resource-Policy: same-site`.
- `curl -sI …/files/npm-local/@acme/ui/-/ui-2.4.0.tgz` shows `Content-Type: application/gzip` and
  `Content-Disposition: attachment; filename*=UTF-8''ui-2.4.0.tgz`.
- With `content_types.overrides: {".txt": "text/plain"}`, `curl -sI …/files/generic-local/notes.txt`
  still shows `Content-Disposition: attachment` and the sandbox CSP.

## Webhooks

Webhooks tell other systems what happened. They never influence the operation that caused the
event: delivery is asynchronous, a slow or failing receiver cannot delay or fail a publish, and a
receiver cannot change the outcome. Synchronous hooks that can block an operation are not planned
(see Deferred).

### R-OPS-035 · Event outbox and dispatch
Priority: **P1** · Covers: EVT-001

**Outbox row.** A state change whose registry entry is webhook-capable (R-OPS-014) MUST insert one
`event_outbox` row in the same transaction as the change. A rolled-back change therefore never
produces an event, and a committed change always does. Remote cache fills (`artifact.cached`)
write their row in the transaction that records the cached file.

**No subscriber, no row.** Each replica keeps the set of domains with an enabled subscription in
memory, as a registered cache of the change area `webhooks` ([architecture.md](architecture.md)
R-ARCH-018). When no subscription covers a domain, no outbox row is written, so webhooks cost
nothing until someone uses them. Outbox rows are written by the replica that commits the change:
`web` replicas for publishes and cache fills, `worker` replicas for changes their jobs make.

**When a new subscription starts.** A subscription receives events committed after it is enabled,
plus the propagation delay of the change area (`cluster.epoch_poll_interval` plus 1 s, with or
without Valkey). Earlier events are not delivered.

**Dispatch.**

- The dispatcher runs only on `worker` replicas ([architecture.md](architecture.md) R-ARCH-001),
  under the watched loop `webhooks.dispatch` (R-OPS-001). Any ready `worker` dispatches; no
  `worker` is special.
- After an outbox row commits, the writer sends a signal on the signal area `events`: it leaves
  through the coalescer to Valkey (R-ARCH-018), because publish and cache-fill transactions write
  outbox rows at client-traffic rates. Delivery is at most once and absent while Valkey is
  unavailable, so a five-second poll covers lost signals; while Valkey is unavailable, dispatch
  starts within that poll.
- The dispatcher claims undispatched rows with `FOR UPDATE SKIP LOCKED` in batches of up to 100, on
  the main pool in one short transaction (it works through a transaction-mode pooler).
- For each event it evaluates every enabled subscription (filter and owner permission, R-OPS-038
  and R-OPS-039).
- It inserts one `webhook_delivery` row and one delivery job per match, then marks the event
  dispatched, all in one transaction.
- A `worker` that is drained in the middle of a batch rolls its transaction back, so the rows stay
  undispatched and another `worker` claims them.

**Guarantees.** Delivery is at least once. Ordering is not guaranteed, not even per subscription.
Each event has a stable ID, carried in the envelope and in the `webhook-id` header (R-OPS-040), so
receivers can deduplicate.

**Retention.** Dispatched outbox rows are kept for `webhooks.outbox_retention_days` (for redelivery
and diagnosis), then deleted by `webhook-retention`.

```sql
CREATE TABLE event_outbox (
  id             uuid        PRIMARY KEY,       -- UUIDv7; envelope id and webhook-id
  type           text        NOT NULL,          -- registry name, e.g. 'package.published'
  domain         text        NOT NULL,
  occurred_at    timestamptz NOT NULL,
  repository     text,
  subject        text,                          -- path or package name used by filters
  envelope       jsonb       NOT NULL,          -- R-OPS-036
  traceparent    text,
  dispatched_at  timestamptz
);
CREATE INDEX event_outbox_pending_idx ON event_outbox (occurred_at) WHERE dispatched_at IS NULL;
```

**Acceptance criteria:**

- With no subscriptions, publishing 1,000 npm versions writes no `event_outbox` rows.
- With one subscription, scaling the `worker` Deployment to 0 for five minutes and back delivers
  every event published meanwhile exactly once or more, and never loses one.
- With Valkey stopped, an event is dispatched within 5 s plus one claim of its commit.
- A publish that fails after the outbox insert (fault injection) produces no delivery.

### R-OPS-036 · Event envelope
Priority: **P1** · Covers: EVT-002, EVT-003

The event body is a [CloudEvents 1.0](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md)
event in the
[JSON format](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/formats/json-format.md),
structured mode, with media type `application/cloudevents+json`. Receivers can use any CloudEvents
SDK.

```json
{
  "specversion": "1.0",
  "id": "01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11",
  "source": "https://trove.example.com",
  "type": "trove.package.published",
  "subject": "npm-local/@acme/ui@2.4.0",
  "time": "2026-10-28T11:58:02.417Z",
  "datacontenttype": "application/json",
  "dataschema": "https://trove.example.com/api/v1/webhooks/event-types/package.published/schema",
  "trovesubscription": "gitlab-trigger-npm",
  "trovereplica": "trove-web-6f9c7d8b5-x2v4q",
  "trovetest": false,
  "data": {
    "repository": {"key": "npm-local", "format": "npm", "class": "local"},
    "package": {"name": "@acme/ui", "version": "2.4.0"},
    "files": [{"path": "@acme/ui/-/ui-2.4.0.tgz", "sha256": "9b74c9897bac770ffc029102a200c5de…", "size": 48213}],
    "actor": {
      "type": "ci",
      "id": "5f0e…",
      "name": "ci:gitlab-internal/acme-publish",
      "tokenAuthenticated": true,
      "authMethod": "bearer",
      "ci": {"issuer": "https://gitlab.example.com", "project_path": "acme/ui", "ref": "main", "pipeline_id": "184467", "job_id": "9921034"}
    }
  }
}
```

- **One envelope for every domain.** Every event type of every domain is delivered in this
  envelope, with `source` set. This includes the domains that later features add (curation,
  scanning, lifecycle and so on). No domain is delivered as a bare data object, and no domain
  omits `source`.
- **Standard attributes.**
  - `source` is `server.base_url`.
  - `type` is `trove.` followed by the registry name. The prefix is fixed from the first release
    that ships webhooks (Q-OPS-5).
  - `subject` is `<repository>/<path or package@version>`.
- **Extension attributes** (lower-case, as CloudEvents requires):
  - `trovesubscription`: the matching subscription;
  - `trovereplica`: the replica that committed the change, not the `worker` that delivers it;
  - `trovetest`: `true` only for test deliveries (R-OPS-043).
- **Actor.** `data.actor` names who caused the event: principal type, ID, name, whether a token
  was used, and the authentication method. For CI principals it adds the issuer and selected,
  non-secret claims of the exchanged ID token (INV-10). E-mail addresses and token IDs are not
  included.

**Acceptance criteria:**

- A standard-handler delivery parses with the CloudEvents Go SDK
  (`github.com/cloudevents/sdk-go/v2`) without errors.
- Its `data` validates against the schema at `dataschema`.

### R-OPS-037 · Webhook event types and discovery
Priority: **P1** · Covers: EVT-004, EVT-005, EVT-006, EVT-007, EVT-017

Subscriptions may select these types. Event types are always listed explicitly: there is no "all
types" wildcard, so high-volume types such as `artifact.cached` are only delivered when someone
selects them.

| Type | Fires when | Filter subject | `data` beyond repository and actor | Available |
|---|---|---|---|---|
| `package.published` | A package version is accepted (npm publish, NuGet push); one event per version | package name | `package{name,version}`, `files[{path,sha256,size}]` | P1 |
| `package.deleted` | A version is deleted or unpublished | package name | `package{name,version}` | P1 |
| `package.metadata_changed` | Deprecate or undeprecate, unlist or relist, dist-tag set or removed | package name | `package{name,version}`, `change{kind,…}` (`deprecated`, `undeprecated`, `unlisted`, `relisted`, `tag_set`, `tag_removed`) | P1 |
| `artifact.deployed` | A file is stored in a local repository by any upload path; one event per file | path | `path`, `name`, `sha256`, `size`, `overwritten` | P1 |
| `artifact.deleted` | A file is removed | path | `path`, `name`, `sha256`, `size` | P1 |
| `artifact.cached` | A remote repository stores a file fetched from its upstream | path | `path`, `name`, `sha256`, `size`, `upstreamUrl` (without credentials) | P1 |
| `artifact.copied`, `artifact.moved` | A file is copied or moved (ART-032, ART-033) | source path | `source{repository,path}`, `target{repository,path}`, `sha256`, `size` | with copy and move |
| `artifact.property_set`, `artifact.property_deleted` | A property is set or removed (ART-021 to ART-025) | path | `path`, `property{key,values}` | with properties |
| `container.tag_pushed`, `container.tag_deleted` | A Docker or OCI tag is created or deleted | image name | `image`, `tag`, `digest`, `mediaType`, `platforms[{os,architecture,variant}]` | with Docker and OCI |
| `build.uploaded`, `build.deleted`, `build.promoted` | Build information is stored, deleted or promoted | build name | `build{name,number,startedAt}`, promotion target | with build information |

- **Move and copy match the source.** Moved and copied files are matched against the source
  repository and path.
- **Location fields.** `source` and `target` are objects that hold the bare repository key and the
  path inside that repository, for example
  `{"repository": "npm-local", "path": "@acme/ui/-/ui-2.4.0.tgz"}`. They are never one joined
  string.
- **Folders.** Deleting, moving or copying a folder emits one event per file it contains. There is
  no folder-level event. One npm publish emits one `package.published` and one
  `artifact.deployed`.
- **Discovery.** `GET /api/v1/webhooks/event-types` (requires `webhooks.read`) lists the domains, the
  types available in this installation, the filter shape per domain, and a schema link per type.
  `GET /api/v1/webhooks/event-types/{type}/schema` returns the JSON Schema of `data` and is public,
  because receivers need it.

```json
{
  "domains": [
    {
      "domain": "package",
      "filter": "repository",
      "types": [
        {"type": "package.published", "cloudEventsType": "trove.package.published", "schema": "/api/v1/webhooks/event-types/package.published/schema"}
      ]
    }
  ]
}
```

**Acceptance criteria:**

- `npm publish` emits exactly one `package.published` and one `artifact.deployed`.
- `npm dist-tag add @acme/ui@2.4.0 beta` emits one `package.metadata_changed` with `change.kind` =
  `tag_set`.
- `dotnet nuget delete Acme.Lib 1.0.0 --source … --api-key …` on a repository configured to unlist
  emits `package.metadata_changed` with `unlisted`.
- The discovery list contains no type of a format or feature that is not installed.

### R-OPS-038 · Webhook subscriptions and filters
Priority: **P1** · Covers: EVT-018, EVT-019, EVT-020, API-148, API-149, API-150, API-151, API-152, API-153

A subscription binds one domain, one or more event types of that domain, a filter and exactly one
handler.

```json
{
  "key": "gitlab-trigger-npm",
  "description": "Start the integration pipeline for new @acme packages",
  "enabled": true,
  "domain": "package",
  "eventTypes": ["package.published"],
  "filter": {
    "repositories": ["npm-local"],
    "anyLocal": false,
    "anyRemote": false,
    "include": ["@acme/*"],
    "exclude": ["@acme/internal-*"]
  },
  "handler": {"kind": "templated", "…": "R-OPS-041"},
  "recordSuccessfulDeliveries": false,
  "owner": {"type": "user", "id": "5f0e…", "name": "alice"},
  "state": "active"
}
```

- **Key.** Matches `^[a-z][a-z0-9_-]{1,99}$`, is unique, and is fixed after creation.
- **Reserved keys.** `test`, `deliveries` and `event-types` are reserved names of the collection
  ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7), because `/api/v1/webhooks/test`,
  `/api/v1/webhooks/deliveries` and `/api/v1/webhooks/event-types` sit in the `{key}` position. A
  create with one of them, through the API or a configuration document (R-OPS-029), is rejected
  with `422` `reserved_name`. The key schema in the OpenAPI document lists them as
  `x-trove-reserved-names`, so R-API-004's CI check covers any literal sibling added later.
- **`enabled`** MUST be present on create, because subscriptions have no implicit default.
- **Filter.**
  - Repository selection is the union of `repositories` and the `anyLocal` / `anyRemote`
    switches. The switches include repositories created later. An empty selection is invalid.
  - `include` and `exclude` match the event's filter subject (R-OPS-037): a path for `artifact.*`,
    a package name for `package.*`, an image name for `container.*`. They use the pattern syntax of
    [authorization.md](authorization.md), so permission policies and webhooks read patterns the
    same way.
  - An empty `include` means every subject. `exclude` wins over `include`.
  - The build domain's filter is `{"builds": [...], "anyBuild": false, "include": [...], "exclude": [...]}`.
- **Owner and state.**
  - `owner` is the principal that created the subscription. Only a system administrator can
    reassign it.
  - `state` is `active`, `suspended` (R-OPS-039) or `disabled_by_receiver` (R-OPS-042). It is
    independent of `enabled`. An administrator returns a subscription to `active` with
    `POST /api/v1/webhooks/{key}/reactivate`.

API (permissions per R-OPS-039; secret handling per R-OPS-040 and R-OPS-041):

| Method and path | Response |
|---|---|
| `GET /api/v1/webhooks` | `200` subscriptions the caller may read, without secret values |
| `POST /api/v1/webhooks` | `201` stored subscription (with a generated signing secret, once); `400`/`422` invalid; `409` key exists |
| `GET /api/v1/webhooks/{key}` | `200` with `ETag: "<revision>"`; `404` |
| `PUT /api/v1/webhooks/{key}` | Full replacement of everything except `key` and `owner`; `If-Match` required ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2; `If-Match: *` for a deliberate blind write); `200` with the new `ETag`; `404`; `412` `precondition_failed` on a revision mismatch; `428` `precondition_required` without `If-Match` |
| `DELETE /api/v1/webhooks/{key}` | `204`; pending deliveries are abandoned; `If-Match` optional |
| `POST /api/v1/webhooks/{key}/reactivate` | `200`; system administrator |

```sql
CREATE TABLE webhook_subscription (
  key              text        PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_-]{1,99}$'),
  description      text        NOT NULL DEFAULT '',
  enabled          boolean     NOT NULL,
  domain           text        NOT NULL,
  event_types      text[]      NOT NULL CHECK (cardinality(event_types) > 0),
  filter           jsonb       NOT NULL,
  handler_kind     text        NOT NULL CHECK (handler_kind IN ('standard','templated')),
  handler          jsonb       NOT NULL,      -- url or url secret name, method, headers, template, proxy key; never secret values
  record_success   boolean     NOT NULL DEFAULT false,
  owner_id         uuid        NOT NULL,
  state            text        NOT NULL DEFAULT 'active' CHECK (state IN ('active','suspended','disabled_by_receiver')),
  revision         bigint      NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
```

Subscriptions can also be declared in configuration documents (`spec.webhooks`, R-OPS-029), with
secrets given as `secretRef`.

**Acceptance criteria:**

- A subscription with `anyLocal: true` receives `artifact.deployed` from a local repository created
  after it.
- `exclude: ["@acme/internal-*"]` suppresses `@acme/internal-tools`.
- A `POST` without `enabled` returns `422`.
- A `POST /api/v1/webhooks` with key `deliveries`, `test` or `event-types` returns `422` with code
  `reserved_name`.
- A `PUT /api/v1/webhooks/gitlab-trigger-npm` without `If-Match` returns `428` and changes
  nothing; with the `ETag` of a preceding `GET` it returns `200`, and a second `PUT` with the same
  tag returns `412`. Single-secret writes (`PUT …/secrets/{name}`, R-OPS-041) need no `If-Match`:
  they are write-only slots, exempt under R-API-012.

### R-OPS-039 · Webhook authorization
Priority: **P1** · Covers: EVT-030, ADMAPI-027

Event data reveals repository paths and package names. A subscription must therefore never become a
way to observe content its owner cannot read.

| Principal | May |
|---|---|
| System administrator | Everything, including `anyLocal`, `anyRemote` and reassigning owners |
| `webhooks.manage` (delegated system permission, IAM-007, [authorization.md](authorization.md)) | Create, update, delete and test subscriptions it owns. Every repository named in `repositories` must be one on which it holds `read`. `anyLocal` and `anyRemote` are refused (`403`) |
| `webhooks.read` | List and read every subscription and the delivery log, without secret values |
| Token with a webhook scope ([identity-and-authentication.md](identity-and-authentication.md), TOK-014) | As its principal, narrowed by the scope (INV-9) |

**The owner's permissions bound delivery.** At dispatch, an event is delivered only when the
subscription's owner currently holds `read` on the event's repository and on the event's path or
package (INV-9 evaluation, cached). Administrator owners always pass.

**Suspension.** When the owner is disabled or deleted, the subscription moves to `suspended`:
deliveries stop, and `webhook.suspended` is written. An administrator reassigns the owner and
reactivates it.

Changing a permission policy needs no webhook update: the next dispatch evaluates the owner again.

**Acceptance criteria:**

- A user with `webhooks.manage` and `read` on `npm-local` only gets `403` for a subscription
  naming `npm-secret`.
- After that user's `read` on `npm-local` is removed by a policy change, their existing
  subscription stops receiving `npm-local` events within the authorization cache period.
- Disabling the user suspends the subscription.

### R-OPS-040 · Request signing
Priority: **P1** · Covers: EVT-023, EVT-024

The **standard handler** POSTs the envelope unchanged:

```json
"handler": {
  "kind": "standard",
  "url": "https://hooks.example.com/trove",
  "proxy": null,
  "headers": [{"name": "X-Team", "value": "platform"}],
  "signing": {"enabled": true}
}
```

**Signing** follows the [Standard Webhooks](https://www.standardwebhooks.com/) specification for
both handler kinds, so receivers can use its reference libraries.

- **Headers.** Every signed attempt carries `webhook-id` (the event ID, the same on every attempt),
  `webhook-timestamp` (Unix seconds of this attempt) and `webhook-signature`.
- **Signature value.** `v1,` followed by the base64 HMAC-SHA-256 ([RFC 2104](https://www.rfc-editor.org/rfc/rfc2104))
  of `<webhook-id>.<webhook-timestamp>.<raw body bytes>`.
- **Byte-exact body.** The body is signed exactly as sent and never re-serialised after signing.
- **Replay.** The timestamp lets receivers reject replays. Five minutes of tolerance is the
  recommended receiver setting.
- **Secret format.** A signing secret is 32 random bytes, base64-encoded, prefixed `whsec_`.
- **Creation.** On create with `signing.enabled` and no supplied secret, trove generates one and
  returns it once, in the `201` response (`"signingSecret": "whsec_…"`). Later reads show only
  `{"signing": {"enabled": true, "secretSet": true, "rotatedAt": "…"}}`.
- **Rotation.** `POST /api/v1/webhooks/{key}/signing-secret/rotate` returns a new secret once. For
  `webhooks.signing_rotation_overlap`, deliveries carry two signatures separated by a space (new
  and old), so receivers can switch without downtime.

Unlike the catalog's unsigned mode, trove never sends a shared secret in a header by itself. A
receiver that needs a static token gets it through a templated header from a handler secret
(R-OPS-041).

**Headers trove always sets:**

- `Content-Type: application/cloudevents+json; charset=utf-8` (standard handler);
- `User-Agent: trove-webhooks/<version>`;
- the three signature headers when signing is on.

**Reserved headers.** Custom headers cannot set `Host`, `Content-Length`, `Transfer-Encoding`,
`Connection`, `traceparent` or any `webhook-*` header (`422`).

**Acceptance criteria:**

- A receiver built on the Standard Webhooks reference library verifies a delivery, and rejects it
  after one byte of the body is changed.
- After a rotation, a receiver that still holds the old secret keeps verifying during the overlap.
- A `GET` of the subscription never contains `whsec_`.

### R-OPS-041 · Templated requests and handler secrets
Priority: **P1** · Covers: EVT-025, EVT-026, EVT-027, EVT-035

The **templated handler** shapes method, headers and body for a receiver's own API.

```json
"handler": {
  "kind": "templated",
  "url": "https://gitlab.example.com/api/v4/projects/42/trigger/pipeline",
  "method": "POST",
  "proxy": null,
  "headers": [{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}],
  "template": "token={{ .secrets.trigger_token | urlquery }}&ref=main&variables[PACKAGE]={{ .data.package.name | urlquery }}&variables[VERSION]={{ .data.package.version | urlquery }}",
  "secrets": [{"name": "trigger_token", "value": "glptt-…"}],
  "signing": {"enabled": false}
}
```

**Template language** (Q-OPS-11). Go [`text/template`](https://pkg.go.dev/text/template),
executed with `missingkey=error`, restricted so that one render does a bounded amount of work.

- **Root fields.**
  - `.event`: the whole envelope (R-OPS-036);
  - `.data`: shortcut to `.event.data`;
  - `.actor`: shortcut to `.event.data.actor`;
  - `.secrets.<name>`: the handler secrets.
- **Template data.** The envelope is passed as decoded JSON, with numbers as
  [`json.Number`](https://pkg.go.dev/encoding/json#Number). Sizes therefore print exactly
  (`1073741824`, not `1.073741824e+09`), and no data value is an integer that a `range` could
  count up to. Every field that the type's data schema declares is present, `null` where it does
  not apply, so `missingkey=error` fires only for names outside the schema.
- **Allowed actions.**
  - text and `{{ pipeline }}` output, comments;
  - `if`, `else`, `else if`, `with`;
  - variables (`{{ $f := index .data.files 0 }}`);
  - `range` over a field path of the data, for example `{{ range .data.files }}`, with `break` and
    `continue`.
- **Rejected when the template is stored (`422`).**
  - `define`, `template` and `block`, so templates cannot recurse;
  - a `range` inside another `range`;
  - more than 16 `range` actions in one template;
  - a `range` whose pipeline is not a field path rooted at `.` or `$`, which refuses literals
    (`{{ range 1000000000 }}`), function results and user variables;
  - the `call` function.
- **Functions.** The `text/template` built-ins except `call` (`and`, `or`, `not`, `eq`, `ne`, `lt`,
  `le`, `gt`, `ge`, `len`, `index`, `slice`, `print`, `printf`, `println`, `urlquery`, `html`,
  `js`), plus the functions below.
  - **Number-aware comparisons.** `eq`, `ne`, `lt`, `le`, `gt` and `ge` are replaced (the
    template's function map overrides built-ins) by versions that compare two numbers numerically,
    whether each is a `json.Number` from the data or a numeric literal. The built-ins would compare
    two `json.Number` values as strings (`9` greater than `10`) and refuse a number against a
    literal. Comparing a number with a string is a render error.
  - `json`: encodes any value as JSON, including correct string escaping;
  - `yaml`;
  - `base64`;
  - `default`: `{{ .data.package | default "none" }}` returns the fallback for a `null` or empty
    value.

  No function reads files, the environment, the clock or the network. Template authors use `json`
  for values placed in JSON bodies. An unescaped scalar inside a JSON string is the author's error,
  and the test delivery shows it.
- **Bounded work.** Arrays in event data hold at most 1,000 items (R-OPS-014), loops cannot nest,
  and a template holds at most 16 of them, so one render runs at most 16,000 loop iterations. One
  `printf` produces at most about 10 MB, because Go's `fmt` refuses larger widths. Rendering stops
  with a render error at the first write past the body limit below.
- **Missing values.** A name outside the schema is a render error. Validation on save finds it
  against the sample event. At delivery it fails the attempt without a retry (R-OPS-042). A field
  that is `null` renders as `<no value>` when printed directly, which is `text/template`'s own
  output; templates print such fields through `default` or `json`.
- **Limits.**
  - template source ≤ 64 KiB;
  - rendered body ≤ 1 MiB;
  - header values are templates too: they are rendered with the same data, under the same
    restrictions, and their `range` actions count toward the 16.
  - The target URL is not templated. A URL that is itself a secret (incoming-webhook URLs of chat
    tools) is given as `"urlSecret": "<name>"` instead of `url`.
- **Methods.** `POST` (default), `PUT`, `PATCH`, `GET`, `DELETE`.
- **Validation on save.** A template that references an unknown secret is rejected. So is one that
  fails to render against the sample event of each subscribed type.

**Handler secrets.**

- **Names** match `^[A-Za-z_][A-Za-z0-9_]{0,63}$`. A handler holds at most 20 secrets, each up to
  8 KiB.
- **Encryption.** Values are encrypted at rest with the stored-secret key (IAM-045,
  [architecture.md](architecture.md)).
- **Write-only.** Reads return `{"name": "…", "set": true, "updatedAt": "…"}`, never the value.
- **On `PUT` of the subscription**, the secret list is complete:
  - an entry with `value` sets or rotates that secret;
  - an entry without `value` keeps the stored one;
  - a secret missing from the list is removed;
  - an empty string is rejected (`422`), so nothing is wiped by accident.
- **Single-secret operations.** `PUT /api/v1/webhooks/{key}/secrets/{name}` with `{"value":"…"}`
  sets or rotates one secret (`204`). `DELETE` removes it, or answers `409` while the template,
  headers or `urlSecret` still reference it.
- **Audit:** `webhook.secret_rotated`.

```sql
CREATE TABLE webhook_secret (
  subscription_key     text        NOT NULL REFERENCES webhook_subscription (key) ON DELETE CASCADE,
  name                 text        NOT NULL,     -- '_signing' holds the signing secret
  ciphertext           bytea       NOT NULL,
  previous_ciphertext  bytea,                    -- signing rotation overlap only
  previous_expires_at  timestamptz,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subscription_key, name)
);
```

**Recipes (acceptance criteria).** The UI and documentation ship these as templates.

- **GitLab pipeline trigger**
  ([pipeline trigger tokens](https://docs.gitlab.com/ci/triggers/)). The handler above starts a
  pipeline on `main` with `PACKAGE` and `VERSION` set after `npm publish` of `@acme/ui`.
- **GitHub Actions** through
  [`repository_dispatch`](https://docs.github.com/en/rest/repos/repos#create-a-repository-dispatch-event).
  `POST https://api.github.com/repos/acme/deploy/dispatches` with headers
  `Accept: application/vnd.github+json` and `Authorization: Bearer {{ .secrets.gh_token }}`, and the
  body `{"event_type":"trove-package","client_payload":{"package":{{ json .data.package }}}}`,
  starts a workflow that declares `on: repository_dispatch`.
- **Chat incoming webhook.** With `urlSecret`, a Slack-compatible incoming-webhook URL receives
  `{"text": {{ printf "%s published %s" .actor.name .event.subject | json }}}`.

**Further acceptance criteria:**

- Storing a template with `{{ range 1000000000 }}{{ end }}`, with a `range` nested in another, or
  with `{{ define "x" }}` is refused with `422`, and the error names the construct.
- `{{ range .data.files }}{{ .path }} {{ .size }};{{ end }}` renders
  `@acme/ui/-/ui-2.4.0.tgz 48213;` for the `package.published` event shown in R-OPS-036.
- A template that references `.data.pakage` is refused on save, because the sample event has no
  such field.
- For a file of size `9` and a literal `10`, `{{ if lt (index .data.files 0).size 10 }}small{{ end }}`
  renders `small`, and `{{ if gt .data.package.name 1 }}{{ end }}` fails with a render error.
- A template with 17 `range` actions is refused with `422`.

### R-OPS-042 · Delivery, retries and outbound protection
Priority: **P1** · Covers: EVT-001, EVT-028, EVT-029

**Jobs.** Each `webhook_delivery` row is executed by a job of kind `webhook.deliver` on the queue
`delivery` of the job framework ([architecture.md](architecture.md) R-ARCH-016), so only `worker`
replicas send deliveries; the test delivery of R-OPS-043 is the one request a `web` replica sends
to a receiver. A retry reschedules the same job with a later `run_at`.

**One attempt:**

1. Load the subscription, its secrets and the event.
2. Render (R-OPS-041) and sign (R-OPS-040).
3. Resolve and check the target address (below).
4. Send with `webhooks.timeout` as the whole-request limit. Redirects are not followed: a `3xx` is
   a failure.
5. Read at most `webhooks.delivery_log.max_body_bytes` of the response.
6. Record the attempt (R-OPS-044).

**Outcomes:**

| Result | Next step |
|---|---|
| `2xx` | `succeeded` |
| Transport error, timeout, `408`, `425`, `429`, `5xx` | `retrying`. The backoff is `min(max_backoff, initial_backoff × 3^(attempt−1))` with ±20 % jitter. A `Retry-After` on `429`/`503` raises it, up to `max_backoff` ([RFC 9110 §10.2.3](https://www.rfc-editor.org/rfc/rfc9110#section-10.2.3)) |
| `410 Gone` | `failed`, and the subscription becomes `disabled_by_receiver` (`webhook.disabled_by_receiver`) |
| Other `4xx`, `3xx`, render error, blocked address | `failed`, no retry |
| `max_attempts` reached, or the event is older than `max_delivery_age` | `abandoned` |

With the defaults, attempts are spread over roughly 21 h.

**Concurrency.** Each `worker` replica runs at most `jobs.workers.delivery` deliveries
([architecture.md](architecture.md) R-ARCH-016); delivery throughput grows with the `worker`
replica count. Each subscription has at most `webhooks.max_in_flight_per_subscription` deliveries
in flight across all replicas (Q-OPS-19). A count inside the claim query alone would not hold it:
under `READ COMMITTED`, two workers claiming at the same moment do not see each other's
uncommitted claims, and both take the last slot. The claim of a `webhook.deliver` job therefore
runs, in one short transaction on the main pool:

1. `SELECT … FROM webhook_subscription WHERE key = $1 FOR UPDATE SKIP LOCKED`. A subscription whose
   row another worker holds is skipped in this round and offered again in the next.
2. Count the subscription's `webhook_delivery` rows in state `in_flight` whose job is still
   `running`. This statement starts after the row lock is granted, so it sees every claim
   committed before it.
3. Below the limit, claim the job (R-ARCH-016) and set its delivery to `in_flight`; at the limit,
   leave the job queued.

The row lock serialises the claims of one subscription across every replica, so the limit holds
whatever the replica count. It works through a transaction-mode pooler and needs no Valkey. A
delivery of a lost worker stops counting when the reaper returns its job to `queued`
(R-ARCH-016). A slow receiver therefore delays only its own subscription.

**Drains.** An attempt in flight when its `worker` receives `SIGTERM` finishes within
`webhooks.timeout`, far inside the `worker` grace period ([ha-and-helm.md](ha-and-helm.md)
R-HA-011), and is recorded normally. A delivery job not yet started is released to another
`worker` without counting an attempt (R-ARCH-016). If a `worker` dies between the receiver's answer
and the record, the job is retried and the receiver sees the same `webhook-id` again, which
at-least-once delivery allows (R-OPS-035).

**Outbound address protection** (server-side request forgery):

- **Blocked ranges.** The target's addresses are checked inside the dialer's `Control` hook, on
  every connection, so a DNS answer that changes between validation and connection (DNS
  rebinding) is caught. These ranges are refused unless they lie in
  `webhooks.allowed_private_cidrs`:
  - loopback;
  - unspecified;
  - private ([RFC 1918](https://www.rfc-editor.org/rfc/rfc1918));
  - shared address space (100.64.0.0/10);
  - link-local, including the 169.254.169.254 cloud metadata endpoints;
  - unique-local IPv6 (fc00::/7) and link-local IPv6 (fe80::/10);
  - multicast and broadcast;
  - IPv4-mapped IPv6 forms of all of these.

  The ranges follow the IANA
  [IPv4](https://www.iana.org/assignments/iana-ipv4-special-registry/) and
  [IPv6](https://www.iana.org/assignments/iana-ipv6-special-registry/) special-purpose registries.
- **Blanket override.** `webhooks.allow_private_targets: true` lifts the check entirely. It is
  discouraged, and it logs a warning at startup.
- **HTTPS.** `webhooks.require_https: true` refuses `http` targets.
- **TLS.** Certificates are always verified, against the system roots plus `webhooks.ca_file`.
  There is no per-subscription switch to skip verification.
- **Checks on save.** URL syntax, scheme, and the current resolution are checked when a
  subscription is stored. A blocked target is refused with `422`.
- **Outbound proxy.** A handler MAY name an outbound proxy defined in
  [repositories-and-proxy.md](repositories-and-proxy.md) (`"proxy": "<proxy key>"`). Through a
  proxy, trove checks the target name's resolution itself before sending, but the proxy connects
  on its own (H-40).

**Read-only mode.** Deliveries continue in read-only mode (R-OPS-024).

```yaml
webhooks:
  enabled: true                         # default (proposed)
  # per-replica concurrency is jobs.workers.delivery (architecture.md R-ARCH-016); webhooks.workers no longer exists
  timeout: 10s                          # default (proposed)
  max_attempts: 10                      # default (proposed)
  initial_backoff: 30s                  # default (proposed)
  max_backoff: 6h                       # default (proposed)
  max_delivery_age: 72h                 # default (proposed)
  max_in_flight_per_subscription: 4     # default (proposed)
  require_https: true                   # default (proposed)
  allow_private_targets: false          # default (proposed)
  allowed_private_cidrs: []             # default (proposed); example ["10.20.30.0/24"]
  ca_file: ""                           # example /etc/trove/tls/internal-ca.pem
  signing_rotation_overlap: 24h         # default (proposed)
  outbox_retention_days: 7              # default (proposed)
```

**Acceptance criteria:**

- A subscription to `https://127.0.0.1/` or `https://169.254.169.254/` is refused with `422`.
- A name that resolves to a public address at save time and to `10.0.0.5` at delivery time fails
  the attempt with a blocked-address error.
- A receiver answering `503` with `Retry-After: 120` is retried no earlier than 120 s later.
- A receiver answering `410` disables the subscription after one attempt.
- A receiver that hangs for 60 s fails the attempt after 10 s, and other subscriptions' deliveries
  are not delayed.
- With two `worker` replicas and `max_in_flight_per_subscription: 4`, a receiver that holds every
  request open never sees more than four concurrent requests.
- A `worker` replica is deleted while it delivers to a receiver that answers after 3 s: the attempt
  is recorded as `succeeded`, and no job of kind `webhook.deliver` counts an attempt for the
  shutdown.

### R-OPS-043 · Test delivery
Priority: **P1** · Covers: EVT-031, API-154

| Method and path | Request | Permission |
|---|---|---|
| `POST /api/v1/webhooks/{key}/test` | optional `{"eventType":"package.published"}` | owner with `webhooks.manage`, or system administrator |
| `POST /api/v1/webhooks/test` | a full, unsaved subscription definition (secrets inline), plus optional `eventType` | `webhooks.manage` within R-OPS-039 limits |

**The test event.** trove builds a synthetic event of the given type (default: the first
subscribed type) from the type's sample data, with `trovetest: true`. It renders, signs and sends
the event once, with the same address checks and timeout as a real delivery and without retries.

**The response** is `200`, whatever the receiver answered:

```json
{
  "success": false,
  "durationMs": 132,
  "request": {"method": "POST", "url": "https://gitlab.example.com/api/v4/projects/42/trigger/pipeline", "headers": {"Content-Type": "application/x-www-form-urlencoded"}, "body": "token=«redacted:trigger_token»&ref=main&…"},
  "response": {"status": 404, "headers": {"Content-Type": "application/json"}, "body": "{\"message\":\"404 Not Found\"}"},
  "error": "unexpected status 404"
}
```

**Other rules.**

- A render error yields `success: false` with the error and no request.
- Tests are recorded in the delivery log with `test: true` and written as `webhook.tested`.
- Each principal may run 10 tests per minute across the installation; beyond that the call returns
  `429` with `Retry-After`. The limit is the cluster-scoped limiter `webhook_test` of
  [architecture.md](architecture.md) R-ARCH-034, keyed by principal ID, so it does not grow with the
  number of `web` replicas; while Valkey is unavailable it falls back to per-replica shares.

**Acceptance criteria:**

- Testing the GitLab recipe against a project with a valid trigger token returns
  `"success": true`, and a pipeline appears.
- The recorded request shows `«redacted:trigger_token»` instead of the token.

### R-OPS-044 · Delivery log
Priority: **P1** · Covers: EVT-032, EVT-033, API-155

Delivery records live in PostgreSQL (INV-3). Valkey carries only the wake-up of the follow stream
below, so its loss delays the stream by one poll and loses nothing.

**Recording.**

- Failed and abandoned deliveries are always kept.
- Successful deliveries are kept only when the subscription has `recordSuccessfulDeliveries`.
  Otherwise the record is reduced to a *tombstone* when it succeeds (Q-OPS-20): state
  `succeeded`, `recorded = false`, request, response and attempt list cleared, change position
  advanced, so the follow stream below sends its final state. `webhook-retention` deletes
  tombstones after `webhooks.delivery_log.tombstone_retention`. Tombstones exist for the follow
  stream only: the list, detail, redeliver and status endpoints treat them as absent (`404` for
  the detail).
- One record per delivery. Retries update the record in place and append to its attempt list (at
  most the last 10 attempts).
- **Change position.** Because records change in place, every insert and every update of a
  record takes a new value of the sequence `webhook_delivery_change_seq` into `change_seq`. A
  `BEFORE INSERT OR UPDATE` trigger sets it, so no write path can forget it. The sequence is part
  of a backup (sequence values are restored, [backup-and-restore.md](backup-and-restore.md)
  R-BAK-001), so positions never repeat after a restore.

**Masking.** Before storage, every value of the subscription's secrets in the URL, headers and
body is replaced with `«redacted:<name>»`. So are its base64 and URL-encoded forms. The values of
`Authorization`, `Cookie` and signature headers are replaced entirely (R-OPS-013, H-42).

**Retention.** `webhook-retention` deletes:

- failed and abandoned records after `failed_retention_days`;
- successful records after `success_retention_days`;
- tombstones of unrecorded successes after `tombstone_retention`;
- the oldest records beyond `max_records_per_subscription`.

```sql
CREATE TABLE webhook_delivery (
  id                uuid        PRIMARY KEY,
  subscription_key  text        NOT NULL REFERENCES webhook_subscription (key) ON DELETE CASCADE,
  event_id          uuid        NOT NULL,
  event_type        text        NOT NULL,
  test              boolean     NOT NULL DEFAULT false,
  state             text        NOT NULL CHECK (state IN ('pending','in_flight','retrying','succeeded','failed','abandoned')),
  attempts          smallint    NOT NULL DEFAULT 0,
  next_attempt_at   timestamptz,
  job_id            uuid,       -- the webhook.deliver job (R-ARCH-016); the in-flight count of R-OPS-042 joins it
  recorded          boolean     NOT NULL DEFAULT true,  -- false: tombstone of an unrecorded success (Q-OPS-20)
  change_seq        bigint      NOT NULL,  -- nextval('webhook_delivery_change_seq') on every insert and update (trigger); the follow stream's position
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),  -- set by every write; bounds the follow stream's re-read window
  completed_at      timestamptz,
  last_status       smallint,
  last_error        text,
  request           jsonb,      -- last attempt: method, url, headers, body (masked, truncated)
  response          jsonb,      -- last attempt: status, headers, body (truncated)
  attempt_log       jsonb       NOT NULL DEFAULT '[]',  -- [{startedAt, durationMs, status, error}]
  UNIQUE (subscription_key, event_id, test)
);
CREATE INDEX webhook_delivery_sub_idx   ON webhook_delivery (subscription_key, created_at DESC);
CREATE INDEX webhook_delivery_state_idx ON webhook_delivery (state, next_attempt_at);
CREATE INDEX webhook_delivery_inflight_idx ON webhook_delivery (subscription_key) WHERE state = 'in_flight';
CREATE INDEX webhook_delivery_change_idx  ON webhook_delivery (change_seq);
CREATE INDEX webhook_delivery_updated_idx ON webhook_delivery (updated_at);
CREATE INDEX webhook_delivery_tombstone_idx ON webhook_delivery (updated_at) WHERE NOT recorded;
```

API (requires `webhooks.read`, or ownership of the subscription):

| Method and path | Parameters / response |
|---|---|
| `GET /api/v1/webhooks/deliveries` | `subscription`, `state`, `eventType`, `target` (host), `from`, `to`, `limit`, `cursor` → `200` summaries, newest first |
| `GET /api/v1/webhooks/deliveries?follow=true` | Same filters. `200 text/event-stream` ([Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)): one `delivery` event per new or updated record, with a comment line every 15 s to keep the connection open. Details below |
| `GET /api/v1/webhooks/deliveries/{id}` | `200` full record with request, response, attempts and the event envelope |
| `POST /api/v1/webhooks/deliveries/{id}/redeliver` | `202`; resets a `failed` or `abandoned` record to `pending` and queues a new attempt series for the same event, with the attempt list continuing; `409` while the record is pending or in flight; requires the right to manage the subscription; writes `webhook.redelivered` |
| `GET /api/v1/webhooks/{key}/status` | `200 {"lastDelivery":{…},"pending":3,"failedLast24h":1}` |

**The follow stream** is one long request on a `web` replica, written for random distribution and
drains (INV-18), under the event-stream rules of [api-and-ui.md](api-and-ui.md) R-API-051:

- **Position and source.** The stream's order is `change_seq`, and its cursor is the highest
  `change_seq` it has sent. Each query reads, from PostgreSQL, the matching records with
  `change_seq` above the cursor, plus the matching records at or below it whose `updated_at` lies
  within `webhooks.delivery_log.follow_reread_window` before the query. The second part exists
  because a sequence value is drawn before commit: two transactions can commit out of sequence
  order, so a record can become visible after a higher position was already sent
  ([architecture.md](architecture.md) Q-ARCH-15, option (d)). Delivery records are written in
  short transactions that never span a receiver call ([architecture.md](architecture.md)
  R-ARCH-019), so the window covers every late commit and no committed change is skipped. The
  stream drops a record whose record ID and `change_seq` (its version) it has already sent on this
  connection, and sends records in `change_seq` order.
- **Events.** One `delivery` event per new version of a matching record. Its data carries the
  record's summary with `id` and `changeSeq`, so record and version identify it; its `id:` line
  carries the stream's cursor after this event (never lower than the previous one, also for a
  record that committed late), together with a prefix of `system_state.incarnation`, so a cursor
  from before a restore is recognised and the stream starts afresh instead of skipping.
- **Resumable on any replica.** A client that reconnects sends the last `id` back as
  `Last-Event-ID`, as `EventSource` does on its own, and any replica continues after that cursor,
  re-reading the window behind it. Nothing about the stream lives in the replica that served it.
  Versions of that window that the client already received can arrive again after a reconnect;
  a client drops them by `id` and `changeSeq`.
- **Polling, no held connection.** The stream queries PostgreSQL at least every
  `webhooks.delivery_log.follow_poll_interval`, which may not exceed 5 s (a larger value stops
  startup with an error that names the key), takes a main-pool connection for each query only and
  holds none between queries (R-ARCH-019), so open streams never add database connections.
- **Early wake-up only.** A `worker` that creates or updates a delivery record sends a signal on
  this page's signal area `deliveries` after commit ([architecture.md](architecture.md)
  R-ARCH-018), carrying the record IDs; streams on every `web` replica query at once. Signals are
  delivered at most once and absent while Valkey is unavailable, so the poll alone meets the
  stream's contract and the stream works without Valkey. The area is an early wake-up and nothing
  else; it is listed in the area table of [architecture.md](architecture.md) R-ARCH-018.
- **Bounded load.** A stream runs at most one query per second, however many wake-ups arrive, so
  a busy delivery queue cannot multiply into one query per signal and stream. Each `web` replica
  serves at most `webhooks.delivery_log.max_follow_streams` streams; one more answers `503` with
  `Retry-After: 5`, and the retry may reach another replica.
- **How a stream ends.** Once `health.shutdown_drain_delay` has passed at shutdown (R-OPS-001
  shutdown step 3, between steps 2 and 3 of [architecture.md](architecture.md) R-ARCH-024), and
  after `api.event_stream_max_age` ([api-and-ui.md](api-and-ui.md) R-API-051), the replica sends
  `retry: 1000` and ends the stream, so the client reconnects at once, to another replica after a
  drain, and streams spread onto replicas added since they opened.

```yaml
webhooks:
  delivery_log:
    failed_retention_days: 14           # default (proposed)
    success_retention_days: 3           # default (proposed)
    max_records_per_subscription: 10000 # default (proposed)
    max_body_bytes: 65536               # default (proposed)
    follow_poll_interval: 2s            # default (proposed); at most 5s; bound of the follow stream without Valkey
    follow_reread_window: 5s            # default (proposed); re-read behind the cursor for late commits
    max_follow_streams: 100             # default (proposed); open follow streams per web replica
    tombstone_retention: 15m            # default (proposed); must exceed api.event_stream_max_age plus follow_reread_window (Q-OPS-20)
```

**Acceptance criteria:**

- A failing receiver produces one record with an attempt list that grows per retry, and every
  attempt raises its `change_seq`.
- `curl -N -H "Authorization: Bearer $TOKEN" 'https://trove.example.com/api/v1/webhooks/deliveries?follow=true&subscription=gitlab-trigger-npm'`
  shows a `delivery` event within 2 s of a publish, and within
  `webhooks.delivery_log.follow_poll_interval` plus 2 s with Valkey stopped.
- A test hook holds one delivery-record transaction open for 2 s after it drew its `change_seq`,
  while later records commit and are sent. When it commits, the stream sends it within one poll.
- A follow stream cut by deleting its `web` pod and resumed with the last `id` as `Last-Event-ID`
  on another replica receives every version of every matching record at least once across both
  connections; versions it receives twice all lie within `follow_reread_window` before the cut.
- A subscription without `recordSuccessfulDeliveries` delivers one event successfully. The follow
  stream shows the record `succeeded`; `GET /api/v1/webhooks/deliveries/{id}` answers `404`; after
  `tombstone_retention` the row is gone.
- During a slow follow, `pg_stat_activity` shows no connection of the serving pod between queries.
- `redeliver` of an abandoned record produces another attempt with the same `webhook-id`, on the same record.

## Backup and restore

trove's own backup (INV-19) belongs to [backup-and-restore.md](backup-and-restore.md): the
`backup` role's commands, the format in S3, consistency with garbage collection, restore into an
empty installation and the run visibility of R-BAK-014. [ha-and-helm.md](ha-and-helm.md) renders
the CronJob and the restore Job (R-HA-060) and owns the platform layer under it: point-in-time
recovery, bucket versioning and cross-site recovery (R-HA-023 to R-HA-026). This page adds the
following.

- **Observability of backups.** The backup's gauges, counters and alerts are in this page's
  catalog (R-OPS-008) and signal map (R-OPS-046); its audit events are in R-OPS-016; its runs are
  in system information (R-OPS-004) and in support bundles (R-OPS-027). The backup pod logs in the
  format of R-OPS-009 with `"role":"backup"`, and a run is one trace (R-OPS-018). It is not a
  maintenance task (R-OPS-019).
- **What a backup holds of this page.** A backup is one consistent PostgreSQL snapshot plus every
  blob it references, restorable on its own ([backup-and-restore.md](backup-and-restore.md)
  R-BAK-001). Everything this page stores lives in PostgreSQL. With the export classes of
  [backup-and-restore.md](backup-and-restore.md) R-BAK-004, `replica`, `support_bundle` and
  `support_bundle_part` are `transient`: a restored installation starts with an empty registry and
  no support bundles. Everything else of this page is `data` and is restored as of the snapshot:
  audit events, `system_state` (maintenance mode, banner, log-level override), configuration
  documents and ownership, webhook subscriptions and secrets (sealed), outbox, delivery log and the
  storage summary, which the next `storage-summary` run recomputes.
- **Valkey is not backed up** (INV-19). Nothing this page keeps in Valkey (signals, the follow
  stream's wake-up, rate-limit counters of R-OPS-043) is needed after a restore.
- **The stored-secret key.** Sealed values (webhook secrets, upstream credentials) can only be read
  with that key (IAM-045). A backup holds sealed values only; the key ring comes from the
  operator's escrow ([backup-and-restore.md](backup-and-restore.md) R-BAK-028).
- **Revocations after the snapshot.** The audit writer journals every flagged event in the
  transaction of its change (R-OPS-014, R-OPS-015), outside the database, and every restore
  replays that journal ([backup-and-restore.md](backup-and-restore.md) R-BAK-024), so a token
  revoked or a policy narrowed after the snapshot stays revoked or narrowed, an npm version
  unpublished after it stays withheld, and a NuGet version number used after it stays reserved.
  This is why the journal flag is MVP.
- **After a restore.** The restore creates the audit partitions its rows need through the owner's
  `audit_create_partitions()` (R-OPS-015), applies the migrations, verifies that every referenced
  blob was copied and finishes before any replica starts; while it runs, `system_state` holds
  `restore_in_progress` and the `schema` check keeps every replica unready (R-OPS-001,
  [backup-and-restore.md](backup-and-restore.md) R-BAK-021). Webhook events that were undispatched or in flight at the snapshot are delivered
  again, which at-least-once delivery allows (R-OPS-035); receivers deduplicate by `webhook-id`.
  An expired log-level override is ignored by its `expiresAt`, and a read-only mode stored in the
  snapshot is still on (R-OPS-024).
- **Configuration as code.** Documents kept in Git (R-OPS-029) and exports (R-OPS-032) are a
  logical backup of the database-held configuration that survives the loss of the database. The
  configuration file and Kubernetes Secrets are part of the Helm release.

## Handed over to other pages

MVP and P1 features of the catalog pages above that this page does not specify:

| Catalog ID | Feature | Owner |
|---|---|---|
| OPS-032, OPS-034, OPS-035, OPS-039 | Scheduled backups, system export and import as the backup format and restore, instance migration by restore; MVP by INV-19 | [backup-and-restore.md](backup-and-restore.md); this page carries their metrics and audit events (R-OPS-008, R-OPS-016) |
| OPS-033 | Component backup and disaster recovery | Split: [backup-and-restore.md](backup-and-restore.md) owns trove's own backup and restore (R-BAK-001 to R-BAK-029); [ha-and-helm.md](ha-and-helm.md) owns point-in-time recovery, bucket versioning and cross-site disaster recovery (R-HA-023 to R-HA-026) and the chart side, the backup CronJob and the restore Job (R-HA-060) |
| OPS-057 | General repository-manager settings: global offline mode (RMT-031) to [repositories-and-proxy.md](repositories-and-proxy.md); trash can to [storage.md](storage.md); UI upload size limit and items per page to [api-and-ui.md](api-and-ui.md) | split as listed |
| OPS-062 | Container registry addressing method | The Docker/OCI format page (P1), with [ha-and-helm.md](ha-and-helm.md) for ingress routing |
| OPS-063 | Reverse proxy deployment rules | [ha-and-helm.md](ha-and-helm.md); this page contributes R-OPS-005 |
| OPS-066 | Storage quota | [storage.md](storage.md) |
| OPS-067 | Prune unreferenced data | [storage.md](storage.md) |
| OPS-072, OPS-073, OPS-074 | Upgrade policy, rolling upgrade, schema migrations | [ha-and-helm.md](ha-and-helm.md) (INV-3); this page contributes the `schema` readiness check and the replica list |
| OPS-076 | Signed release artifacts (container image and chart signatures) | [ha-and-helm.md](ha-and-helm.md) and the release process in [docs/developer/](../developer/) |
| EVT-034 | Webhook management UI | [api-and-ui.md](api-and-ui.md), on top of R-OPS-037 to R-OPS-044 |
| EVT-070 | Machine-readable API description | [api-and-ui.md](api-and-ui.md) (INV-6) |
| RET-001 to RET-038 (MVP/P1) | Retention and cleanup policies, runs, reports | [storage.md](storage.md); runs appear in the job list (R-OPS-019), and policies can be declared in configuration documents (R-OPS-029) |
| HA-046, HA-047, HA-055 | Chart probes, termination, log and metrics integration | [ha-and-helm.md](ha-and-helm.md); it consumes R-OPS-001, R-OPS-006, R-OPS-009 and the signal map of R-OPS-046 |

## Deferred

<details>
<summary>Deferred catalog entries (28)</summary>

| Catalog ID | Priority | One line |
|---|---|---|
| OPS-010 | P2 | Administrative outbound connection test; remote repositories have their own test ([repositories-and-proxy.md](repositories-and-proxy.md)) |
| OPS-014 | Later | Federation metric families, with federation itself |
| OPS-015 | P2 | Metrics written to log files; not planned, Prometheus scraping covers it |
| OPS-021 | P2 | Front router access log; trove has no internal router, and the ingress controller logs its own access |
| OPS-024 | P2 | Log rotation and archiving; not applicable, logs go to standard output |
| OPS-027, OPS-028 | P2 | Log viewer screen and live log API; the platform log stack and the support-bundle ring buffer cover the need |
| OPS-029, OPS-030 | P2 | Syslog and log analytics integrations; done by the platform's log shipper on standard output |
| OPS-036 to OPS-038 | P2 | Repository export and import, identity-service export ([backup-and-restore.md](backup-and-restore.md) Deferred); scheduled backups, system export and import and instance migration are MVP there (INV-19) |
| OPS-041, OPS-042 | P2 | Advanced runtime properties and a whole-configuration descriptor; the configuration file and configuration documents replace both |
| OPS-047, OPS-048, OPS-049 | P2 | Authentication-provider bootstrap file, identity-service configuration documents, OAuth application integrations |
| OPS-053, OPS-054, OPS-056 | P2 | Server name and date format, custom logo, login notice |
| OPS-058, OPS-059 | P2 | Folder download limits, global replication blocking |
| OPS-061 | P2 | Reverse proxy configuration generator |
| OPS-064 | P2 | Load protection with per-activity throttling (`429`); cluster-wide rate limits per caller exist from the MVP ([architecture.md](architecture.md) R-ARCH-034) |
| OPS-069 | P2 | SHA-256 backfill; not needed, SHA-256 is computed at upload (INV-4) |
| OPS-071 | P2 | Maven index calculation, with the Maven format |
| OPS-075 | P2 | Migration tool from another repository manager |
| OPS-098 | P2 | Global read access to build information |
| EVT-008 | P2 | User lockout event; trove has no passwords, so a `user.disabled` event type would be the equivalent |
| EVT-009 to EVT-016, EVT-021 | Later | Release-set, distribution, curation, scan-status and application-lifecycle events and their filters |
| EVT-022 | P2 | Project-scoped subscriptions, with projects |
| EVT-036 | P2 | Chat and incident-tool recipes beyond the one in R-OPS-041 |
| EVT-038 to EVT-069 | Later | Synchronous server-side hooks and in-process extension scripts |
| EVT-071, EVT-072 | P2 | Typed configuration media types, API deprecation lifecycle ([api-and-ui.md](api-and-ui.md)) |
| EVT-073 to EVT-076 | P2/Later | GraphQL query layer |
| EVT-077 to EVT-079 | P2 | Tool server for AI assistants |
| EVT-080 | P2 | Client SDKs, generated from the OpenAPI document |
| OPS-016, OPS-068, OPS-077 to OPS-097, EVT-037, EVT-081 | Out of scope | Runtime management beans, embedded database, license-based feature gating, billing, subscriptions, license management and portal functions of a hosted service, chat and issue-tracker applications |

</details>

## Security notes

- **Operations listener.** It is unauthenticated by default, so its protection is network-level:
  the chart's NetworkPolicy admits only the kubelet probes and the Prometheus scraper (INV-13,
  [ha-and-helm.md](ha-and-helm.md)). Metrics carry repository keys and traffic volumes, and verbose
  readiness reveals dependency latency and whether Valkey is reachable, but no host, address or user
  name (R-OPS-001). `metrics.auth: static` adds a bearer check that keeps working during a database
  outage (H-41).
- **Revocation depends on the change log, never on Valkey.** Prompt token revocation (INV-8)
  relies on cross-replica invalidation. Every replica polls the PostgreSQL change log; a replica
  whose poll is stale fails `invalidation` and leaves the Service (R-OPS-001), and authorization
  reads the freshness directly where it must fail closed ([authorization.md](authorization.md)
  R-AUTHZ-028). A Valkey message only evicts early: a Valkey outage, a lost message or a forged one
  can neither delay a revocation beyond the poll bound nor suppress it
  ([architecture.md](architecture.md) R-ARCH-018, H-49).
- **Valkey state is not trusted for anything this page reports as fact.** Audit deduplication,
  the delivery log and the replica registry live in PostgreSQL (R-OPS-003, R-OPS-015, R-OPS-044);
  Valkey carries only wake-ups and counters whose loss costs performance (INV-3). An attacker with
  write access to Valkey can raise or lift the test-delivery limit of R-OPS-043 and produce extra
  wake-ups (H-49), not change an audit record or a delivery.
- **Addresses only behind `system.read`.** The Valkey primary address, the keyspace and the backup
  target's bucket and prefix appear only in system information, the replica registry and support
  bundles (R-OPS-003, R-OPS-004, R-OPS-027), never as metric labels and never on the
  unauthenticated operations listener.
- **Backup credentials stay out of the replicas.** Backup observability on `web` and `worker`
  replicas reads `backup_run` only; the target's credentials exist in backup and restore pods only
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-015), so a compromised replica can see that
  backups run but cannot read or delete them.
- **Forwarded headers.** They are believed only from `server.trusted_proxies`. That rule governs
  URL generation (R-OPS-005), client addresses in logs and audit (R-OPS-010, R-OPS-015) and the
  continuation of trace context (R-OPS-018). A too-wide `trusted_proxies` lets clients forge their
  audit address.
- **Audit integrity.** Every state change and its audit record are atomic (R-OPS-015). Access
  events can be dropped under overload, which the dropped counter makes visible. In two-role mode
  the runtime role can only insert and read audit rows, and retention is owner data, so a
  compromised replica cannot erase its traces. The records are still not tamper-evident against
  the schema owner or a superuser (H-39).
- **Configuration documents can name secrets only inside an allow-list.** Files under
  `secrets_dir`, variables prefixed `TROVE_CAC_`. Without that limit, an administrator's document
  could copy the database password into a remote repository's credentials and send it to any host.
- **Webhooks:**
  - they are bounded by the owner's current read permission (R-OPS-039);
  - they refuse private and metadata addresses at connection time (R-OPS-042);
  - they never follow redirects;
  - they sign with a timestamp against replays (R-OPS-040);
  - they mask secrets in the delivery log (R-OPS-044).

  Event data such as package names is publisher-controlled. Templates must use `json` or
  `urlquery` to keep it from breaking the receiver's syntax (R-OPS-041). Templates cannot recurse,
  nest loops or count to a number of their choosing, so a stored template cannot tie up a worker
  (Q-OPS-11).
- **Raw downloads** are served with `nosniff`, a sandboxing CSP,
  `Cross-Origin-Resource-Policy: same-site` and `Content-Disposition: attachment` on every
  response, set by the core so no handler or content-type override can drop them. Uploaded HTML
  or SVG therefore never runs in the UI's origin (R-OPS-034, R-ARCH-007).
- **Support bundles** are administrator-only and audited. Their manifest is meant to be reviewed
  before sharing. They exclude identity data, tokens, audit events and package content
  (R-OPS-027).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-OPS-1 · Couple liveness to prolonged unreadiness?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Never couple; liveness checks only that the HTTP server answers and the check loop runs | The previous default, without any coupling knob | Simple; no dependency outage can restart a replica | A deadlocked worker, scheduler or `LISTEN` loop leaves a replica alive but useless until a human notices |
| (b) Couple after a grace period for every check | Liveness fails once readiness has failed for a set time; the catalog's default (OPS-004), and the previous draft's opt-in knob `health.liveness_unready_grace` | Self-heals a replica that stays unready | A shared outage of PostgreSQL restarts every replica together; contradicts R-HA-008's MUST; restarts discard caches and add connection load at the worst moment |
| (c) Couple only for internal checks (`startup`, `notify`) after a grace period | A per-check coupling policy | Restarts a replica whose `LISTEN` session never recovers | `notify` also fails for shared causes (connection exhaustion, a transaction-mode pooler), so it still restarts all replicas together; per-check policy for little gain |
| (d) Never couple to check results; liveness is a watchdog over trove's own loops | Each long-running loop records progress per iteration, failed iterations included; `/healthz` fails only when a loop has not finished an iteration within its bound, and the chart's liveness probe restarts the pod | Detects deadlocks and hung calls, the faults a restart actually repairs; a dependency outage only produces failed iterations, never a restart; the restart goes through Kubernetes, so the pod drains and the probe thresholds of R-HA-034 apply | Every call in a watched loop needs a deadline, which a test per loop has to enforce |
| (e) Same watchdog, but the process exits itself | A stalled loop makes trove write a goroutine dump and call `os.Exit`; Kubernetes restarts the container | No dependence on probe settings; the evidence is written at the moment of the stall | An exit drops every in-flight request and upload at once, where a failed liveness probe still sends `SIGTERM` and lets the replica drain; operators can neither tune nor disable it through the chart's probe settings |

**Choice:** (d) — liveness never reads readiness or any check result, and fails only when one of trove's own loops stops making progress; (e)'s goroutine dump is adopted, its self-exit is not.

**Rationale:** The deciding question is what a restart repairs. It repairs process-local faults (a deadlock, a goroutine blocked on a call without a deadline), and it never repairs a shared dependency. (b) and (c) infer a local fault from dependency symptoms, so during a shared outage they restart every replica at once, which R-HA-008 forbids. The previous opt-in knob put that failure one setting away, so it is removed rather than kept as an option. (a) is safe but blind to the stuck-loop case. (d) measures progress directly: a loop that keeps retrying an unreachable PostgreSQL is progressing, and the deadline rule turns a blackholed network into failed iterations rather than a hang. (e) detects the same faults but restarts without draining. Its one real advantage, evidence of the deadlock, is taken over: the watchdog logs a goroutine dump when it first reports a loop stuck. One trap of a watchdog is excluded explicitly: the claim loop counts as progressing while all workers are busy, otherwise one long garbage-collection job on the single maintenance worker would restart the replica. The extra cost is one timestamp per loop and the deadline test. The development agent would revisit this if production shows replicas stuck unready while every loop progresses; that symptom would get a targeted rule, not a generic coupling.

**Applied in the spec:** R-OPS-001: liveness rewritten as a watchdog over named loops, with the progress rule (busy workers count as progress), the deadline rule, the draining exemption, a goroutine dump in the log when a loop is first reported stuck, and a `stuck` list in the `503` body. `health.liveness_unready_grace` removed and `health.watchdog_timeout: 2m` added. Acceptance criteria: a 5-minute PostgreSQL block causes no restart; a blocked claim loop fails liveness and logs the dump; a job running for three watchdog periods on a single worker leaves liveness at `200`. The example file in R-OPS-028 follows. After the owner's constraints of 2026-10-28 the choice is unchanged: a Valkey outage is a shared dependency outage like a PostgreSQL one, so it never fails liveness either. R-OPS-001 now lists the watched loops per role, with the change-log poll, the Valkey subscriber and the lease check in place of the retired coordination session; option (c)'s `notify` check no longer exists (`invalidation` replaces it).

### Q-OPS-2 · Should object-storage reachability gate readiness?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep INV-12 as written | An object-store failure makes replicas unready, softened by a failure threshold | Literal invariant; a replica that loses its own path to S3 leaves the Service | One bucket is shared, so an outage removes every replica. Logins, CI token exchange, metadata, deletes, the UI and the API go down with the blob traffic, including the screens operators diagnose with |
| (b) Never gate on object storage | Readiness reflects the process and PostgreSQL only | Everything that needs no blob keeps working | A release with a wrong bucket, endpoint, credential or CA becomes ready and replaces working pods |
| (c) Configurable, gating by default | The previous draft | Operators can choose | The default is (a)'s full outage |
| (d) Gate the first readiness only, then report; continuous gating as opt-in | A replica becomes ready only after its object-store checks passed once (R-STOR-035 startup check). Afterwards a failing check is reported as `degraded`, and blob requests fail one by one through the circuit breaker. `health.require_object_store: true` restores continuous gating | A misconfigured release stalls its rollout instead of breaking the service. A shared outage degrades instead of taking everything down. Operators whose replicas reach S3 over separate network paths can opt into gating | One more boolean to test. A replica that loses only its own S3 path keeps receiving traffic and answers blob requests with a retryable `503` until the single-replica alert is handled. A pod replaced during a shared outage (eviction, crash) cannot become ready until the store returns, so a long outage can shrink capacity |
| (e) Relative gating | A replica turns unready only when its check fails while other replicas pass, compared through the replica registry | Precise in both cases | Cross-replica coordination for a rare case that the circuit breaker and the single-replica alert already cover; flaps while replicas briefly disagree |

**Choice:** (d) — object storage gates only the first readiness of a replica, is reported and degrades blob requests afterwards, and gates continuously only when the operator opts in.

**Rationale:** Readiness exists to route traffic from a broken replica to healthy ones. With one shared bucket there is no healthy replica to route to, so continuous gating turns a partial outage into a total one. The first-readiness gate keeps the one thing gating does well: it stops a misconfigured release before it replaces working pods. (b) gives that up, (c) keeps (a)'s total outage as the default, and (e) buys precision with coordination that the existing alerts make unnecessary. A request that hits a degraded replica gets a retryable `503`, and a retry is likely to land on another replica. The price is paid only during a long shared outage: pods replaced meanwhile stay unready (R-STOR-035's startup check keeps retrying and never exits on a transient outage), but the running replicas are not touched, and the PodDisruptionBudget keeps voluntary evictions of ready pods within its budget. Under (a) the same outage removes every replica at once, so for a shared outage (d) is never worse than (a). This matches the rule recorded in [ha-and-helm.md](ha-and-helm.md) R-HA-008 (Q-HA-1) and [storage.md](storage.md) R-STOR-036 (Q-STOR-1). It changes INV-12, which ties readiness to object-storage reachability. The development agent would revisit (e) if replicas losing only their own S3 path turn out to be frequent in production.

**Applied in the spec:** R-OPS-001: the `objectStore` row states first-readiness gating. A new "Gating and reported checks" paragraph adds the `degraded` response and the opt-in. `health.require_object_store` now defaults to `false`. An acceptance criterion with a 3-minute object-store block was added. R-OPS-008: new gauge `trove_health_check_passing{name}`. R-OPS-028: the example file follows. INV-12 in [README.md](README.md) needs the edit listed as a cross-page consequence.

### Q-OPS-3 · Anonymous access to the version endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Anonymous by default | The catalog's behavior (OPS-005) | Tooling can read the version without credentials | Version, revision and Go version tell an attacker which known vulnerabilities apply. No npm or NuGet client calls the endpoint |
| (b) Authenticated by default, `system.public_version: true` as opt-in | Any valid credential reads it; operators may open it | Secure default; operators who want a public status page can open it; `ping` stays anonymous for reachability checks | Anonymous monitoring needs a token or the opt-in |
| (c) Anonymous with reduced content | Only the API level and the format list, without version, revision or Go version | Capability discovery without credentials | A second response shape to maintain for a consumer that does not exist; it can be added later without breaking anything |

**Choice:** (b) — the version endpoint needs a credential unless the operator sets `system.public_version: true`.

**Rationale:** No supported client needs anonymous version data, and `ping` already answers reachability anonymously. Exposing the exact build to unauthenticated callers only helps someone matching the installation against vulnerability lists. (c) solves a problem nobody has yet. The development agent would revisit this if a client or integration needs anonymous capability discovery; it would then add (c)'s reduced shape rather than open the full version.

**Applied in the spec:** No change; R-OPS-002 already follows this option.

### Q-OPS-4 · Tamper evidence for audit records

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) External shipping only | trove's runtime role keeps full rights on audit rows. Integrity comes from shipping the `log: audit` stream to an append-only store | No extra mechanism | A SQL injection or a compromised replica can rewrite or delete history. Under two-role mode the runtime role cannot even drop partitions, so retention as drafted would not work there |
| (b) Hash-chain every row in PostgreSQL and verify on export | Each row stores the hash of its predecessor | Detects edits in the middle of the chain | Serialises every transactional write on one chain head. The role that writes the chain can recompute it, so without an external anchor it proves nothing against that role |
| (c) Append-only for the runtime role, plus external shipping | The runtime role may only `INSERT` and `SELECT`. Partitions are created and dropped by an owner function (`SECURITY DEFINER`) that applies a retention the owner stores. Shipping covers the owner and superusers | Stops a compromised replica from erasing its traces. Fixes partition maintenance under two-role mode. Costs a few lines of migration SQL and no write contention | No protection in single-role mode or against the owner. Deduplicated denials can no longer be counted up in place |
| (d) (c) plus sealed checkpoints | A job hashes each new batch of rows, chains it to the previous seal and writes the seal to the log stream as an external anchor; the export verifies | Makes edits by the owner detectable, given the shipped seals | A sealing job, a verification path and seal storage, for a threat that log shipping already covers when it is in place |

**Choice:** (c) — audit rows are append-only for trove's runtime role, and partitions and retention are handled by an owner-held function.

**Rationale:** The realistic attacker is a compromised replica or a SQL injection, both acting as the runtime role. (c) is the cheapest option that stops that attacker from rewriting history. It also repairs a real defect: under the two-role setup of [ha-and-helm.md](ha-and-helm.md) R-HA-014, the runtime role has no DDL rights, so the drafted retention task could neither create nor drop partitions. After two months every transactional write would have failed for want of a partition. (b) costs write throughput and still trusts the writer. (d) adds a verification system whose value depends on the same external log shipping that (c) already relies on. Deduplication now writes a first event and one summary event, so no row is ever updated. Moving partition DDL into a function that every replica calls creates one hazard of its own, which the spec closes: `CREATE TABLE … PARTITION OF` and `DROP TABLE` take `ACCESS EXCLUSIVE` on the parent, so a call that waits for a long reader of `audit_event` to finish, such as a backup's snapshot transaction (exports read in short batches, R-OPS-017), would make every transactional write queue up behind it. The function therefore attaches new partitions (`SHARE UPDATE EXCLUSIVE`, which inserts do not wait for), runs under a 2-second `lock_timeout`, and leaves a drop it cannot lock to the next call. The development agent would revisit (d) when an installation needs verifiable completeness of the database copy itself, for example for an external audit.

**Applied in the spec:** R-OPS-015: new section "Append-only rows and partition maintenance" with privileges, `audit_maintain_partitions()` (serialised by a row lock on `audit_policy`, short locks through `ATTACH PARTITION` and `lock_timeout`), the owner-written `audit_policy`, calls from `trove migrate`, at startup and daily, and single-role behavior. Deduplication rewritten without updates (`repeat_of`). New acceptance criteria for `42501` on update and delete, for the partitions present after startup, for the denial summary, and for a maintenance call that does not block writes behind an open reader of `audit_event` (since 2026-10-28 a backup's snapshot transaction; exports hold no transaction between batches, R-OPS-017). R-OPS-016: `repeat_of` added to `authz.denied` and `auth.token_rejected`. R-OPS-017: retention runs through the function and changes take effect with `trove migrate`. R-OPS-001: the `startup` check requires partitions for the current and the next month. R-OPS-019: `audit-retention` row. Security notes and H-39 narrowed to the owner, superusers and single-role mode.

### Q-OPS-5 · CloudEvents `type` prefix

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `trove.` | `trove.package.published` | Short, readable, identical on every installation, already specified | Deviates from the CloudEvents recommendation that `type` SHOULD carry a reverse-DNS prefix ([spec, `type`](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md#type)) |
| (b) Reverse DNS of a domain the project registers | For example `org.<project-domain>.trove.` | Follows the recommendation | No such domain exists today. A later change of domain breaks every receiver's filters |
| (c) Reverse DNS of the code-hosting namespace | For example `io.github.<org>.trove.` | Follows the recommendation with a name that exists today | Ties a permanent public contract to a hosting account and its name, which the project may rename or leave; long and noisy in filters |
| (d) Per-installation prefix from the base URL host | For example `com.example.trove.` | Unique per producer | Receivers become installation-specific, and one receiver cannot serve two installations with the same filter |

**Choice:** (a) — `type` is `trove.` followed by the registry name, frozen from the first release that ships webhooks.

**Rationale:** Receivers match `type` literally, so it must be short, stable and the same on every installation. (c) and (d) fail stability or uniformity, and (b) depends on a domain that does not exist. The reverse-DNS recommendation is a SHOULD that exists to name who defines the event's semantics. The registry names already do that within trove, and `source` (the base URL) identifies the producing installation. The development agent would revisit this only if the project registers a domain before the first release that ships webhooks (P1); afterwards the prefix is frozen.

**Applied in the spec:** R-OPS-036: the `type` bullet states that the prefix is fixed from the first release that ships webhooks.

### Q-OPS-6 · The human-login OIDC client in configuration documents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Configuration file only | Login providers are keys of `trove.yaml`, the client secret comes from a file | Login works before any administrator or document exists. A bad document apply cannot lock everyone out. The client secret never enters the database | Changing a provider needs a rollout |
| (b) Also a `spec.loginProviders` kind in configuration documents | Login providers held in PostgreSQL, declared like repositories | One GitOps path for all configuration | A broken apply can remove the only working login. The client secret must be stored encrypted. Two places would define the same thing |
| (c) Connection and secret in the file, claim-to-group rules in documents | Documents declare which claim values lead into which group (`claimValues` on a group); the file keeps issuer, client and secret | The part that changes when teams change is applied without a rollout, and documents in Git are reviewed too | One provider is defined in two places. An apply that drops the administrator group's rule locks every OIDC administrator out, and documents can also be applied through the API without a Git review. Contradicts Q-AUTH-1, which keeps group rules in the file |

**Choice:** (a) — login providers, including their claim mapping and claim-to-group rules, stay in the configuration file, and configuration documents declare neither.

**Rationale:** Login is bootstrap-critical. A provider that only a database apply can create cannot be used to recover from a bad apply. The file already serves GitOps through the Helm values and keeps the client secret out of the database. (c) is the strongest alternative, because a Git-reviewed document is not the unreviewed UI edit that [identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-1 argues against. It still fails on the lock-out case, since one apply can strip the rule that makes someone an administrator, and on the API apply path that bypasses review. New teams need no rule change anyway: `match_regex` rules with `$1` expansion (R-AUTH-008) cover them. Choosing (a) also follows the decision of [identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-1 (a) and R-AUTH-004, which exclude configuration documents from changing a provider. The page's own example contradicted this by giving a group `claimValues`; the example now declares the group record only, and the CI issuer kind is renamed from `oidcProviders` to `ciIssuers` so that no reader takes it for a login provider. The development agent would revisit this if Q-AUTH-1 moves group rules into PostgreSQL; a document kind for them would then follow.

**Applied in the spec:** R-OPS-029: new paragraph "Login providers are not a document kind", covering claim mapping and claim-to-group rules, with a `422` for a `loginProviders` key or a claim-mapping field on a group; the example document declares `groups` without `claimValues`; the CI kinds are renamed `ciIssuers` and `ciMappings` (with `issuer` instead of `provider`) in the example, the owner table and R-OPS-030's apply order; a new acceptance criterion for the `422`. R-OPS-016: the `group.*` row no longer claims to record claim-to-group mappings.

### Q-OPS-7 · One cron syntax for the whole system

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice now states its scope. Every schedule that trove's scheduler evaluates stays a five-field UTC expression (a). The backup job is a Kubernetes CronJob ([ha-and-helm.md](ha-and-helm.md) R-HA-060), whose schedule Kubernetes evaluates in the CronJob's `timeZone`; option (e) was added for it and chosen: the chart validates the backup schedule against the same rejected forms, and `backup.timeZone` defaults to `Etc/UTC`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Five-field Kubernetes CronJob syntax with its macros, in UTC, other dialects' forms rejected | `30 3 * * *`, `@daily`; `?`, `7` and six-field forms fail loudly | Operators know it from Kubernetes. No trove task needs second precision. UTC has no daylight-saving gaps or repeats. A pasted expression from a dialect that counts Sunday as 1 fails instead of running on the wrong day | Maintenance windows in local time have to be converted to UTC by hand |
| (b) Six-field syntax with seconds | The catalog's syntax (OPS-044) | Matches the reference behavior | No task needs seconds. Six fields read differently from every Kubernetes schedule in the same chart, a certain source of off-by-one-field errors |
| (c) Five-field syntax plus an optional IANA time zone per schedule | Like the `timeZone` field of Kubernetes CronJobs | Local maintenance windows | Jobs at 02:30 local time are skipped or doubled on daylight-saving change days; more test surface for a need nobody has stated |
| (d) Accept five and six fields, dialect chosen by field count | Kubernetes syntax for five fields, the catalog's dialect for six | Expressions from either world paste unchanged | The dialects number weekdays differently (1 is Monday in one and Sunday in the other), so a pasted expression silently runs on the wrong day; two parsers to test |
| (e) (a) for trove's scheduler; the backup CronJob validated against (a)'s rules, with the CronJob's own `timeZone` as a chart value defaulting to `Etc/UTC` | Kubernetes evaluates the backup schedule; `helm template` fails on `?`, `7`, six fields, `@every`, `TZ=` and `CRON_TZ=` in `backup.schedule` | One syntax for every schedule an operator writes. The default keeps UTC everywhere. A local-time backup is possible where the CronJob API offers it ([time zones](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#time-zones)) | A backup scheduled in a local zone between 01:00 and 03:00 can be skipped or run twice on a daylight-saving change day |
| (f) (a) for trove's scheduler; the backup CronJob fixed to UTC | No `backup.timeZone` value | Exactly one time base | The CronJob field exists for this and costs nothing to pass through; operators convert by hand |

**Choice:** (a) for every schedule trove's scheduler evaluates — a five-field expression with the Kubernetes CronJob fields and macros, evaluated in UTC, through the one parser that [storage.md](storage.md) R-STOR-025 specifies — and (e) for the backup CronJob: the same rejected forms, checked by the chart, and a `timeZone` that defaults to `Etc/UTC`.

**Rationale:** One syntax across the product and its chart removes a class of configuration errors, and UTC removes daylight-saving edge cases from the scheduler's exactly-once guarantee (R-ARCH-016). Kubernetes itself rejects `TZ=` and `CRON_TZ=` inside a schedule ([CronJob docs](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#unsupported-timezone-specification)), so trove rejects them too. Kubernetes accepts `?` as `*`, but trove rejects it, together with `7` and six-field forms, so that an expression from a dialect that counts Sunday as 1 fails loudly (the same decision as Q-STOR-4 in storage.md). That is also why (d)'s convenience is rejected: it turns exactly this case into a cleanup on the wrong day. The backup job cannot be a task of trove's scheduler, because it needs credentials that the replicas never hold ([backup-and-restore.md](backup-and-restore.md) R-BAK-015); its schedule is therefore Kubernetes', and the only lever left is the chart's validation, which (e) uses. A duplicated backup run on a change day is serialized by the run row (R-BAK-010) and harmless; a skipped one is caught by `TroveBackupStale` (R-OPS-046). (f) removes a field that costs nothing and helps operators who plan backups in local time. The development agent would revisit this if operators ask for local-time windows for trove's own tasks; an optional time-zone field (c) can be added later without breaking existing schedules.

**Applied in the spec:** Conventions: the "Schedules" bullet lists the Kubernetes macro set, names the main rejected forms, points to the normative dialect in [storage.md](storage.md) R-STOR-025, which every schedule of trove's scheduler uses, and names the backup CronJob as the schedule Kubernetes evaluates. R-OPS-019: "Backups are not a task". Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-029 rejects those forms in `backup.schedule`.

### Q-OPS-8 · Locking objects managed by configuration documents

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Locked by default, one global switch | API and UI answer `409` naming the owning document (`lock_managed_objects: true`) | No silent drift that the next apply reverts. One rule to explain | Teams that mix UI edits and documents must turn the switch off globally |
| (b) Unlocked; drift shows up in the next diff | Anyone with rights edits managed objects | No friction for UI users | An emergency UI fix is silently reverted by the next pipeline run, possibly re-opening the incident |
| (c) Per-document setting | Each document declares whether its objects are locked | Fine-grained | A second place to look for why an edit fails; no team has asked for mixed styles |
| (d) Global default with per-document override | (a) plus an override in `metadata` | Covers mixed setups | Same cost as (c), for the same unstated need |
| (e) Locked, with an audited administrator override per edit | A `force` flag lets an administrator change a managed object anyway; the object is marked drifted until the next apply | A UI fix in an emergency without touching the document | The next apply reverts the fix exactly as in (b), unless apply also refuses drifted objects, which then blocks the pipeline until someone edits the document: more states for the same end result as editing the document first |

**Choice:** (a) — managed objects are locked against API and UI changes by default, controlled by one global switch.

**Rationale:** A clear `409` that names the document tells the operator where the source of truth is, and beats a fix that silently disappears at the next apply. The emergency path stays open and leads through that source: correct the document and apply it through the API, or through the CLI's database mode when nobody can log in. (e) looks like a softer lock but only moves (b)'s revert problem one step later. A per-document override, (c) or (d), can be added later without changing the default. The development agent would revisit this when an installation needs both styles side by side.

**Applied in the spec:** R-OPS-029: the "Locking" bullet states the default and the emergency path (corrected document through API or CLI, `adopt` to move ownership); a new acceptance criterion checks the `409` that names the document. R-OPS-030: the `412` acceptance criterion now changes an adopted, not yet managed repository, because a managed one is locked.

### Q-OPS-9 · Ownership of the job API

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) This page keeps the job read and cancel API; [architecture.md](architecture.md) keeps the model | The API sits next to the task list and the triggers that return job references | Job visibility is an operator concern; the API is a thin view over the model; [api-and-ui.md](api-and-ui.md) R-API-015 already links here | Model and API live on two pages |
| (b) Move model and API to architecture.md | One page for jobs | One place | Puts operator-facing endpoints on a page about internals; R-OPS-019's tasks would still live here |
| (c) Move the API to api-and-ui.md next to R-API-015 | API conventions and job API in one place | Close to the long-running-operation rules | Splits the task list from the job list, which share one model and one screen |

**Choice:** (a) — the job API stays in R-OPS-019, the model in R-ARCH-016, and R-OPS-019 is raised to MVP.

**Rationale:** The split already matches how the pages link to each other. Reviewing it exposed a priority conflict: R-OPS-019 was P1, but MVP requirements return job references whose `href` points at its API (R-OPS-020's garbage-collection trigger and every long-running operation of R-API-015). An MVP client would get links to an endpoint that does not exist. The queue and the scheduler are MVP anyway, so the API on top of them is a small addition. Keeping the API here also means applying [api-and-ui.md](api-and-ui.md) Q-API-9 and [storage.md](storage.md) Q-STOR-10 on this page: every singleton task starts only through the task trigger, and the owning page keeps the read path of its results. R-OPS-023 still had its own `…/storage/summary/refresh` path and a `/api/v1/system/storage/summary` read path, both of which R-API-020's acceptance criterion forbids. The development agent would revisit this if the job API grows domain-specific views; those would go to their owning pages, as R-API-015 already allows.

**Applied in the spec:** R-OPS-019: priority P1 → MVP, with a "Why MVP" paragraph and the rule that a task exists from the release that ships its owner. The job read and cancel permissions now include the job's creator, as R-API-015 requires. The trigger's `202` carries `Location` (R-API-015); the state filter lists R-ARCH-016's states including `cancelling`; mutual exclusion covers tasks that share an exclusion key (`gc` and `gc-full`). R-OPS-023: the read path is `GET /api/v1/storage/summary` (owned by R-STOR-028), the refresh path is removed in favour of `POST /api/v1/system/tasks/storage-summary/run`, and an acceptance criterion checks that neither old path is in the OpenAPI document.

### Q-OPS-10 · Default cap for the `repo` metric label

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the cap stays 500, but which keys keep their label is now decided from the repository snapshot, the same on every replica (include patterns first, then creation order), instead of by first observation per replica. With requests spread at random over n replicas, per-replica selection gave every replica a different set beyond the cap and made `sum by (repo)` across replicas wrong. Option (d) was added and is part of the choice.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 500 repositories per family and replica | Keys beyond the cap fold into `repo="_other"`; the overflow counter shows it | Most installations stay below the cap, so dashboards work without configuration; within the 25,000-series budget of R-OPS-007 | Large installations need `repository_label_include` to choose which keys keep a label |
| (b) 100 | A tighter cap | Smaller scrapes | Medium installations fold real repositories into `_other` out of the box |
| (c) No `repo` label by default; only repositories that match `repository_label_include` | Opt-in per repository | Smallest series count | Every installation has to configure something before per-repository dashboards show anything |
| (d) (a), with the labelled keys chosen from the repository snapshot | Include patterns first, then ascending repository ID; recomputed when the snapshot changes | Every replica labels the same keys, so per-repository sums across replicas are exact; a deleted repository frees its place everywhere | A repository created after the cap was reached stays in `_other` until it is listed in `repository_label_include`, however busy it is |

**Choice:** (a) with (d) — a cap of 500 distinct repository keys per family and replica, the same keys on every replica, chosen from the repository snapshot, with `_other` and the overflow counter beyond it.

**Rationale:** The cap keeps scrape size bounded while working without configuration for the common case. The overflow counter makes outgrowing it visible, and the include list then makes the labelling deterministic. (b) folds real repositories of medium installations into `_other` before anyone has asked for a smaller scrape. (c) makes every installation configure something before per-repository dashboards show anything. An estimate from the families of R-OPS-008, not a measurement: a local repository costs up to about 24 `repo` series (operation and outcome combinations, bytes, inventory), and a remote one about 50, because the upstream duration histogram alone adds 17 (14 buckets, `+Inf`, `_sum`, `_count`). A mix of 400 local and 100 remote keys keeps the `repo`-labelled series near 15,000, which leaves room for the other families inside R-OPS-007's 25,000 budget. 500 remote keys alone would reach about 25,000 `repo`-labelled series; such an installation sets `repository_label_include` or lowers the cap. The budget is per replica, and a Prometheus server stores it once per replica, so an installation that scales `web` far out multiplies the stored series by that count; the same two settings are its levers. (d) costs a "busy but new" repository its own series once the cap is full; first observation would have labelled it on some replicas and folded it on others, which is worse than a consistent `_other` plus the overflow counter that says the include list needs an entry. The development agent would revisit this if the reference load test of R-OPS-007 exceeds the series budget with the default, or if installations regularly hit the cap with busy new repositories; ranking by traffic from the storage summary would then replace creation order.

**Applied in the spec:** R-OPS-007: the "Repository cap" bullet chooses the labelled keys from the repository snapshot, the same on every replica, for `repo` and `subscription`; the "Budget" bullet names the growth with the replica count; the acceptance criterion spreads the load test over three `web` replicas and expects the same 500 keys on each.

### Q-OPS-11 · Template language: conditionals, loops and functions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Minimal subset | Field substitution and pipelines into a fixed function set (`json`, `yaml`, `urlquery`, `base64`, `default`, `printf`); no `if`, no `range` | Trivially bounded; can be extended later without breaking templates | No conditional text (for example on `overwritten` or `trovetest`) and no per-file lines in human-readable messages; the validator still has to walk the parse tree to reject the rest |
| (b) Restricted `text/template` | `if`, `else`, `with`, variables and non-nested `range` over event fields (at most 16 per template); the built-ins except `call`, with number-aware comparisons, plus `json`, `yaml`, `base64`, `default`. Rejected on save: `define`, `template`, `block`, nested `range`, `range` over literals, function results or variables. Data is passed as JSON with `json.Number` numbers | Covers conditional text and file lists. No new dependency (the YAML encoder is the configuration loader's). Work per render is bounded by schema-capped arrays and the loop count. Numbers print exactly | A parse-tree validator, the `json.Number` decoding and six replacement comparison functions to implement and test |
| (c) `text/template` with a large third-party function library | String, date, crypto and collection helpers | Rich templates | Larger contract and attack surface: such libraries include functions that read the process environment, which would expose trove's own secrets; unbounded loops stay possible |
| (d) A different language (CEL expressions inside a JSON skeleton, or Jsonnet) | Cost-limited evaluation | Built-in evaluation limits | Unfamiliar to receiver authors; awkward for form-encoded and plain-text bodies; departs from the template model the subscription API exposes |

**Choice:** (b) — templates use `text/template` with conditionals, at most 16 non-nested `range` actions over event data and a fixed function set with number-aware comparisons, and everything that could recurse or count without bound is rejected when the template is stored.

**Rationale:** This settles the catalog's open point on templated handlers (EVT-026): which functions, conditionals and loops are supported, and what a missing path does. Go 1.27's `text/template` can range over integers, so an unrestricted template such as `{{ range 1000000000 }}{{ range 1000000000 }}{{ end }}{{ end }}` spins without writing anything. The body limit never triggers for a loop that writes nothing, and a goroutine cannot be stopped from outside. Passing the data as JSON with `json.Number` removes integers from the data: a `range` over a number fails. The save-time check removes literal, computed and nested loops, and the cap of 16 loops, with arrays capped at 1,000 items by R-OPS-014, bounds one render to 16,000 iterations; without that cap the 64 KiB source could hold about 2,000 sequential loops. `json.Number` has one side effect that the first draft missed: the built-in comparisons treat it as a string, so `gt` of two sizes compares them as text (`9` greater than `10`) and a comparison with a numeric literal fails. Replacing the six comparison functions through the template's function map fixes that without giving up exact printing. Verified with a Go 1.27.3 test program in this review: `range` over a literal integer iterates, `range` over a `json.Number` or a string fails, two `json.Number` values compare as text under the built-in `gt`, a number against an integer literal fails with "incompatible types", the function map overrides a built-in, a `null` field prints `<no value>`, and `fmt` refuses widths above about 10^7. (a) would need almost the same validator while giving up useful conditionals. (c) adds functions that can leak secrets. (d) changes the user-facing model for limits that (b) already provides. Fields that the schema declares are always present (as `null`), so `missingkey=error` catches only real typos, and save-time validation against the sample event catches those first. The development agent would revisit this if later event types (for example build events with long module lists) need nested loops; a per-render iteration budget would then replace the nesting rule.

**Applied in the spec:** R-OPS-041: "Template language" rewritten with template data (`json.Number`, schema fields always present), allowed actions, constructs rejected on save (including more than 16 `range` actions), the function list without `call` and with number-aware comparisons, bounded work, `null` printing as `<no value>`, and header templates under the same restrictions. Five acceptance criteria added. R-OPS-014: every array in a data schema declares `maxItems` ≤ 1,000, checked by the registry test. Security notes: the webhook bullet states that templates cannot tie up a worker.

### Q-OPS-12 · What readiness means on a `worker` replica

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-2 splits `web` and `worker` from the start. No Service routes to a `worker` replica, so
readiness cannot mean "receives traffic" there. It still drives the Deployment's rollout and the
disruption budget, which counts only ready pods as healthy
([PDB healthiness](https://kubernetes.io/docs/tasks/run-application/configure-pdb/#healthiness-of-a-pod)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No readiness probe on `worker` | Liveness only; a running container counts as ready | Nothing to define | A new `worker` release with a wrong bucket or schema replaces working workers, because the rollout cannot see that it is broken; the PDB counts broken workers as healthy |
| (b) The same checks as `web`; readiness drives rollout and PDB only | The claim loop ignores readiness | Rollouts stall on a broken release | A `worker` whose change-log poll is stale keeps running jobs with stale caches: webhook dispatch checks the owner's current `read` permission from the cached grants (R-OPS-039), and cleanup jobs read the repository snapshot |
| (c) The same checks as `web`, and a `worker` claims only while ready | Readiness means "may take work" for both roles: Service endpoints for `web`, job claims for `worker` | One rule for both roles; `draining`, which already stops claims at `SIGTERM`, becomes a case of it; a stale or misconfigured worker leaves the work to the others; rollouts and the PDB see the same state | A `worker` that is unready only because of a check that does not matter for one job kind stops that kind too |
| (d) Worker-specific checks, for example "the claim query succeeds" | A separate readiness definition per role | Precise per role | Two definitions to keep in step; the claim query depends on exactly what `database` checks |

**Choice:** (c) — `worker` replicas run the same checks as `web`, and the claim loop claims only while the replica is ready.

**Rationale:** The question readiness answers is the same for both roles: may this process take
work that other replicas could take instead? (c) answers it once. The concrete risk of (b) is a
permission check against stale grants: a webhook delivered to a subscription owner whose `read` was
just revoked, which the `invalidation` check exists to prevent on `web`. (a) gives up the rollout
protection that Q-OPS-2 and Q-HA-13 rely on for every role. (d) duplicates the `database` check.
The cost of (c) is small: the claim loop already stops at `SIGTERM`, keeps its liveness progress
while it does not claim, and the reported checks (`objectStore`, `valkey`) never stop it. The
development agent would revisit this if a job kind turns out to need claiming while a gating check
fails, for example a repair job during a long object-store outage with continuous gating on.

**Applied in the spec:** R-OPS-001: readiness defined as "may take work" for both roles, the check
table with role-independent checks, the shutdown steps per role, and acceptance criteria for a
`worker` that claims nothing while PostgreSQL is cut off and for a replica that waits during a
restore. Cross-page: [architecture.md](architecture.md) R-ARCH-016 (the claim loop claims only
while the replica is ready, and records progress while it does not).

### Q-OPS-13 · How a series names its role and pool

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

With `web` and `worker` Deployments, and pools of either ([ha-and-helm.md](ha-and-helm.md)
R-HA-062), dashboards and alerts need the role behind a series, for example the PostgreSQL
connections each role holds.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Constant `role` label on every series, set by the process | The process knows its role and adds it everywhere | Works with any scraper | The pool is a chart concept the process does not know; Prometheus' guidance for exporters says not to apply the same label to every metric ([writing exporters](https://prometheus.io/docs/instrumenting/writing_exporters/#target-labels-not-static-scraped-labels)); a scrape configuration that also adds `role` collides |
| (b) Target labels from the scrape, plus the role in the info metric | The ServiceMonitor copies `role` and `pool` from the pod's labels by relabeling; the process reports its role only in `trove_build_info{role}` | Follows the guidance, which allows machine roles as target labels; the pool comes from where it is defined; every series of a pod carries both | A scraper outside the chart gets the labels only if it relabels the same way; otherwise it joins `trove_build_info` |
| (c) Info metric only | Queries join `* on (instance) group_left (role) trove_build_info` | No scrape configuration | Every dashboard and alert needs the join, and a forgotten join silently mixes roles |
| (d) Metric names per role | `trove_web_…`, `trove_worker_…` | No labels at all | Twice the catalog; a further split of a role (R-HA-062) renames series |

**Choice:** (b) — `role` and `pool` are target labels that the chart's ServiceMonitor attaches from the pod's labels, and the process reports its role in `trove_build_info{role}` for scrapers that do not relabel.

**Rationale:** The role and the pool describe where a process runs, which is what target labels
are for, and the pool exists only in the chart's values. (a) would need the process to learn a
chart concept and still collide with scrapers that label by pod. (c) moves the burden into every
query. (d) breaks with every split the owner allows. The info metric keeps (c) available as the
fallback, so nothing is lost for other scrapers. The prometheus-operator's ServiceMonitor copies pod
labels onto series ([OpenShift's ServiceMonitor API reference](https://docs.redhat.com/en/documentation/openshift_container_platform/4.17/html/monitoring_apis/servicemonitor-monitoring-coreos-com-v1),
which mirrors the upstream API, describes `podTargetLabels` that way); relabeling a pod label to
the name `role` is plain Prometheus relabeling. The development agent would revisit this if a
supported scrape path can neither relabel nor join, which would justify (a) behind a switch.

**Applied in the spec:** R-OPS-006: the "Role and pool of a series" bullet and an acceptance
criterion. R-OPS-007: `role` only on `trove_build_info`; target labels are outside the label list.
R-OPS-008: `trove_build_info{role}`. Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-045 (the
ServiceMonitor's relabelings for `role` and `pool`).

### Q-OPS-14 · Audit deduplication across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-OPS-015 deduplicates `authz.denied` and `auth.token_rejected` per replica. Requests now reach
replicas at random (INV-12), so one burst is spread over all of them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica | Each replica writes a first event and one summary per key and window | No shared state in the audit path; the sum of counts stays exact; no event is hidden, because every replica writes its first occurrence at once | Up to n first events and n summaries per key and window with n replicas |
| (b) Cluster-wide through Valkey | `SET NX` decides the first occurrence, `INCR` counts, one replica writes the summary | One first event and one summary per window | The count of an audit record would depend on shared ephemeral state; a Valkey loss or failover loses counts or the summary, against INV-3 ("never correctness or data"); a forged key suppresses the first event |
| (c) Cluster-wide counter rows in PostgreSQL | Upsert a count per key and window | Exact, one row | Rows are updated, which Q-OPS-4's append-only grant forbids; one hot row per key under a denial flood |
| (d) No deduplication | Every denial is a row | Simplest | A denial flood writes one row per request |

**Choice:** (a) — deduplication stays per replica; the bound is n first events and n summaries per key and window, and the sum of `details.count` is exact.

**Rationale:** An audit record is evidence, so its content must not depend on a store whose loss
the design accepts (INV-3); (b) fails that test, and a forged first-occurrence key would even hide a
denial. (c) breaks append-only. The cost of (a) grows linearly with the replica count, not with the
request rate, and the monthly partitions absorb it. INV-18 is unaffected, because no response
depends on the deduplication state. The development agent would revisit this if denial floods with
many replicas dominate the audit volume; a longer window per replica would then come before any
shared state.

**Applied in the spec:** R-OPS-015: the bullet on random distribution and an acceptance criterion
over three replicas. R-OPS-001: shutdown step 4 writes open summaries and flushes the buffer, with a
drain criterion. Cross-page: [architecture.md](architecture.md) R-ARCH-032 lists the audit buffer
and the deduplication windows as per-replica state, and R-ARCH-024 shutdown step 6 flushes them.

### Q-OPS-15 · Alerting on Valkey failovers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A Sentinel failover is the designed way Valkey stays available. trove runs degraded for about
`down-after-milliseconds` and recovers by itself (R-HA-057). The question is which part of it
deserves an alert.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No failover alert | Failovers on dashboards only; outages are covered by `TroveValkeyUnavailable` | No noise | A Valkey that fails over every few minutes, through a flapping network or a `down-after-milliseconds` that is too short, stays unnoticed while every failover costs collapse and rate-limit precision |
| (b) An alert on every failover | `increase(trove_valkey_primary_changes_total[5m]) > 0` | Every failover is seen | Fires on every planned failover and every drain of the primary's node (R-HA-059 variant B), which teaches people to ignore it |
| (c) Alerts on repetition and on disagreement | `TroveValkeyFailoverFlapping`: more than two primary changes seen by any replica within an hour; `TroveValkeyPrimaryDisagreement`: replicas name different primaries for 5 minutes | Silent during maintenance; catches the two failure modes a single failover hides: flapping, and a replica that stayed bound to a demoted primary ([architecture.md](architecture.md) R-ARCH-031) | Needs the registry's primary view and one gauge read from PostgreSQL |
| (d) Alert on Sentinel events | trove subscribes to `+sdown`, `+odown` and `+switch-master` and alerts on them | Earliest signal | Sentinel's own state belongs to the exporter run with Valkey; trove would alert on the infrastructure instead of on what it experiences |

**Choice:** (c) — no alert for a single failover; alerts for repeated failovers and for replicas that disagree about the primary.

**Rationale:** A single failover is normal operation and stays visible in the counter and in one
log record per replica (R-OPS-045). What harms trove is a failover that keeps happening, or one that
some replicas did not follow: Sentinel does not guarantee that acknowledged writes survive a
failover ([Sentinel](https://valkey.io/topics/sentinel/)), and a replica that writes to the old
primary loses its flight locks and counters for as long as it stays there. Both are invisible in
(a) and drowned in (b). (d) duplicates the server-side exporter. The thresholds keep R-HA-059's
drain test and R-HA-057's planned failover silent. The development agent would revisit this if the
disagreement alert fires during normal failovers in the nightly runs, which would mean the
reconciliation of R-ARCH-031 takes longer than its bound.

**Applied in the spec:** R-OPS-003: the replica's view of Valkey in the registry, including its primary.
R-OPS-008: `trove_valkey_primary_views`. R-OPS-045: the transition and failover log records.
R-OPS-046: the two alert rows. Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-045 (the two
alerts).

### Q-OPS-16 · Flushing caches across the installation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Since Valkey holds the negative cache (INV-3, [architecture.md](architecture.md) Q-ARCH-16), a
"flush the caches" operation has to say what it does in Valkey. The change area `cache` of
R-ARCH-018 already carries flush requests, but no operation produced them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No operation; document that deleting trove's keys is safe | Operators flush Valkey with an administrative credential; in-process caches end by `SafetyTTL` | No new endpoint | Needs a Valkey administrator during an incident; does not reach in-process caches before their TTL |
| (b) An endpoint that flushes in-process caches through the change area and invalidates negative entries in PostgreSQL | `POST /api/v1/system/caches/flush`; Valkey keys stay and are ignored by reader validation | Works while Valkey is unavailable; needs no extra Valkey command; a resurrected key from a stale primary is ignored too (R-HA-057 rule 2) | Flushed negative entries take memory until they expire |
| (c) (b) plus deleting the keyspace's keys | `SCAN` and `DEL` over `trove:<ks>:*` | Memory freed at once | Widens the ACL of [architecture.md](architecture.md) R-ARCH-031 by `SCAN` and `DEL`, which a stolen credential could then use; a key deleted on a primary that loses the write comes back anyway, so the reader validation is needed regardless |
| (d) Rotate the keyspace at runtime | A new `keyspace_id`, as a restore writes | Every key orphaned in one step | Contradicts R-ARCH-030, under which a replica reads the keyspace once at start-up; replicas would disagree on the keyspace during the switch, which splits flight locks and counters |

**Choice:** (b) as P1 (R-OPS-047), and (a)'s statement as an MVP fact of R-OPS-045: deleting trove's keys is always safe.

**Rationale:** The flush must work in exactly the situations where Valkey is suspect, so it cannot
depend on deleting Valkey keys; reader validation against an invalidation time in PostgreSQL makes
that unnecessary, and it is already how R-OPS-021 works. (c) buys memory at the price of least
privilege. (d) breaks an invariant of the keyspace for a gain that (b) already provides. P1,
because nothing in the MVP depends on it: R-OPS-021 (MVP) cuts a wrong negative entry short per
repository, Valkey content loss is harmless by design, and in-process caches have their
`SafetyTTL`. The development agent would revisit the priority if incident reviews of the prototype
show a need for an installation-wide flush before P1.

**Applied in the spec:** New R-OPS-047. R-OPS-045: "Deleting trove's keys is always safe".
R-OPS-016: `system.caches_flushed`. R-OPS-024: allowed in read-only mode. R-OPS-008: the `admin`
reason of `trove_cache_flushes_total`.

### Q-OPS-17 · Flight locks in the lock diagnostics

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The cross-replica fetch lock moved from a PostgreSQL advisory lock to Valkey
([architecture.md](architecture.md) Q-ARCH-6, Q-ARCH-7), and R-ARCH-017 retired the `fetch`
category. Architecture left open whether R-OPS-026 shows flight locks from Valkey or not at all.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Not at all | The view shows PostgreSQL advisory locks only; collapsed fetches are observed through metrics and the upstream log | No new access path; the view keeps one source and one meaning: exclusions whose failure would corrupt data | No list of fetches in progress |
| (b) `SCAN` the keyspace | List `trove:<ks>:lock:fetch:v1:*` with their TTLs | A live list | Needs `SCAN` in the ACL; keys are hashes ([architecture.md](architecture.md) R-ARCH-030), so the list cannot name a package anyway; O(keyspace) per call |
| (c) Each replica publishes its held flights in its registry row | A bounded list per heartbeat | No Valkey access | Writes per heartbeat for a diagnostic that is stale by the time it is read; the flights are seconds long |
| (d) Flights in the support bundle | Each replica's part lists its current and recent flights | Available when someone investigates | Same staleness; already covered by the upstream log in the ring buffer |

**Choice:** (a) — the lock view shows PostgreSQL advisory locks only; collapsed fetches are diagnosed through `trove_remote_collapsed_requests_total`, the `trove_flight_lock_*` families, the leader's upstream record and the `collapsed` parameter of `Cache-Status`.

**Rationale:** The view's question was "what is everyone waiting for". For fetches, the upstream
log answers it better: the leader's record carries its trace and its `flight_role` (R-OPS-011),
and the waiters' responses carry the `collapsed` parameter of `Cache-Status` and count in
`trove_remote_collapsed_requests_total` (R-REPO-047). (b) would widen the ACL to
list hashes that name nothing. (c) and (d) record state that lives for seconds. Keeping the view to
PostgreSQL also keeps its meaning sharp: what it shows are the exclusions INV-3 keeps in
PostgreSQL. The development agent would revisit this if operators need to see a stuck flight that
neither the metrics nor the log explain; (d) is then the cheapest addition.

**Applied in the spec:** R-OPS-026: the introduction, the `fetch` category removed, the connection
class in each item, the example and the acceptance criteria.

### Q-OPS-18 · Gauges read from PostgreSQL on n replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Inventory, queue, backup-run and registry gauges are read from PostgreSQL. With the load beyond one
replica and `web` autoscaled, the number of replicas that read them grows, and a highly available
Prometheus pair scrapes every replica twice.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every replica queries on every scrape | The earlier, unstated behaviour | Values are fresh at scrape time | Database load grows with replicas × scrape rate × Prometheus servers; a scrape waits for PostgreSQL and fails during exactly the outage it should report |
| (b) Every `web` and `worker` replica evaluates in the background once per interval | `metrics.db_gauge_interval` (30 s); a scrape returns the last values; an age gauge tells their age | Load depends on replicas and interval only; scrapes never touch PostgreSQL; the same 30 s rule as [backup-and-restore.md](backup-and-restore.md) R-BAK-014 | Values up to one interval old; still n queries per interval |
| (c) Only `worker` replicas export them | Fewer readers | Load independent of the `web` count | The series vanish when no `worker` runs, exactly when `TroveJobsStuck` and `TroveBackupStale` must fire |
| (d) One replica under a lease exports them | A `metrics` lease | One reader | A lease for metrics; series jump between pods on every hand-over; a new singleton to drain |

**Choice:** (b) — every `web` and `worker` replica evaluates the gauges read from PostgreSQL in the background once per `metrics.db_gauge_interval`, and scrapes return the last values.

**Rationale:** The alerts that matter most read these gauges, so they must survive the loss of a
whole role, which rules out (c) and makes (d) a single point of reporting. (a) ties database load to
how Prometheus is deployed, which trove does not control. Under (b), 100 replicas at 30 s run each
gauge query about three times per second, a few dozen small indexed queries per second for the
whole set, independent of scrapers. The values are at most one interval old,
which no alert threshold notices. The development agent would revisit this if the gauge queries show
up in `pg_stat_statements` at the reference scale; the interval would then grow for the inventory
gauges first.

**Applied in the spec:** R-OPS-007: the "Values read from PostgreSQL" bullet, the "Absent, not zero"
bullet, `metrics.db_gauge_interval` and an acceptance criterion with two Prometheus servers.
R-OPS-008: `trove_metrics_db_gauges_last_success_timestamp_seconds`. R-OPS-006: scrapes touch
neither PostgreSQL nor Valkey. R-OPS-028: the example file.

### Q-OPS-19 · Per-subscription delivery limit across `worker` replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-OPS-042 promises that a receiver never sees more than
`webhooks.max_in_flight_per_subscription` concurrent deliveries of one subscription. With
deliveries spread over n `worker` replicas, the limit needs a mechanism that holds across them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Count in the claim query | The claim skips a subscription whose running deliveries reach the limit | No extra step | Not a limit: under `READ COMMITTED` concurrent claimers do not see each other's uncommitted claims, so n workers can each take the last slot and the receiver sees up to limit + n − 1 |
| (b) Lock the subscription row in the claim transaction, then count | `FOR UPDATE SKIP LOCKED` on `webhook_subscription`, then count the subscription's `in_flight` deliveries whose job is `running`, then claim | Exact across replicas; transaction-scoped, so it works through a transaction-mode pooler; no Valkey; a lost worker's delivery stops counting when the reaper requeues its job | One more row lock per delivery claim; claims of one subscription serialise, which is the point of the limit |
| (c) Slot rows per subscription | N slot rows, each claimed with `SKIP LOCKED` and held through lease columns for the length of the attempt | Exact; no serialised count | A second lease mechanism next to the job lease of R-ARCH-016, with its own expiry and cleanup, for the same guarantee |
| (d) A Valkey semaphore | `INCR` with a TTL per subscription, decremented after the attempt | Fast; no PostgreSQL work | A failover or flush loses the count and the receiver is overrun; a crashed holder leaks a slot until the TTL; the fallback while Valkey is unavailable needs (b) anyway (INV-3) |
| (e) A per-replica share | Each worker allows limit ÷ n, with n from the replica registry | No coordination | Approximate only: n changes with every scale-out and drain, and the share rounds to 0 or overshoots the limit; [architecture.md](architecture.md) R-ARCH-034 accepts such shares only as a degraded mode |

**Choice:** (b) — the claim of a `webhook.deliver` job locks its subscription's row with `SKIP LOCKED`, counts the running deliveries, and claims only below the limit.

**Rationale:** The limit exists to protect a receiver, so it must hold exactly whatever the
`worker` replica count, and it must not depend on state whose loss the design accepts. (a) is what
the page said before and is not a limit under concurrency. (d) loses the count in exactly the
failover the owner requires trove to survive. (c) is exact but duplicates the job lease. (e) is
the approximation R-ARCH-034 tolerates only while Valkey is down, made permanent. (b) reuses what
the job framework already has, a short transaction on the main pool, and the subscription row is
the natural point of serialisation; `SKIP LOCKED` keeps a busy subscription from making other
claims wait. The development agent would revisit this if the subscription row lock shows up as
contention in `pg_stat_statements` at the reference load, which would point to (c).

**Applied in the spec:** R-OPS-042: the "Concurrency" paragraph with the three claim steps.
R-OPS-044: `webhook_delivery.job_id`, the index on `in_flight` deliveries. Cross-page:
[architecture.md](architecture.md) R-ARCH-016 (the kind-specific claim step for
`webhook.deliver`).

### Q-OPS-20 · Final event for a successful delivery that is not recorded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Without `recordSuccessfulDeliveries`, R-OPS-044 used to delete a delivery's row when it
succeeded. The follow stream of [api-and-ui.md](api-and-ui.md) R-API-051 reads delivery records
from PostgreSQL only, so a deleted row produces no event: a client that follows the stream sees the
delivery `in_flight` and never learns that it ended.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No final event | The row is deleted at success, as before | No extra write | Every unrecorded success stays `in_flight` in every follower's view; the webhook screen cannot show a delivery finishing; the stream misses a change, which R-API-051 counts as an event |
| (b) A short-lived tombstone | At success the row becomes a stripped `succeeded` version (`recorded = false`, no request, response or attempt list) with a new `change_seq`; `webhook-retention` deletes it after `tombstone_retention` | The final event comes from PostgreSQL through the same query and the same position as every other event, so it survives a reconnect to another replica and a Valkey outage; nothing of the delivery's content is kept, so "not recorded" still holds | An update instead of a delete, and a batched delete later; a row per success lives for up to 30 minutes; the list, detail and status endpoints have to filter tombstones |
| (c) A separate tombstone table | The deleting transaction inserts `(id, subscription, change_seq)` into a second table that the stream reads as well | The delivery table holds recorded rows only | A second source for one ordered stream, with the same sequence and the same re-read window to keep in step |
| (d) The final state in the signal | The `deliveries` signal carries the final state, and the stream forwards it | No database write | Signals are delivered at most once and are absent while Valkey is unavailable; R-API-051 forbids a stream that depends on them, and a reconnect loses the event |

**Choice:** (b) — an unrecorded success leaves a tombstone row with its final state for `webhooks.delivery_log.tombstone_retention` (15 minutes), which only the follow stream shows.

**Rationale:** The stream's contract is "every change of a matching record, from PostgreSQL", and a
delivery that ends is the change a follower waits for. (b) satisfies it with the mechanism the
stream already has, so the event survives a drain, a reconnect to another replica and a Valkey
outage alike; (d) fails all three. (c) gets the same result with a second source to merge. (a)
keeps a false `in_flight` on screen. The tombstone keeps no request, response or attempt data, so
the subscription's choice not to record successes still decides what is stored; the row exists
only so the outcome can be streamed. The retention must exceed `api.event_stream_max_age` plus the
re-read window, so a client that reconnects at the latest moment still finds it; 15 minutes covers
the defaults (10 minutes plus 5 s). The cost is one row per success for at most two retention
runs. The development agent would revisit this if tombstones dominate the table at the reference
delivery rate, which would point to (c).

**Applied in the spec:** R-OPS-044: "Recording" (tombstones, their visibility), the columns
`recorded` and `change_seq`, the partial index on tombstones, the retention list,
`tombstone_retention` with its validation rule, and an acceptance criterion. R-OPS-019: the
`webhook-retention` task covers tombstones through R-OPS-044.

### Q-OPS-21 · Position of the delivery-log follow stream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Retries update a delivery record in place, and requests reach `web` replicas at random. The follow
stream therefore needs a position in PostgreSQL that orders every version of every record, never
skips a committed one, and lets any replica resume after it ([api-and-ui.md](api-and-ui.md)
R-API-051).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `updated_at` and record ID | The previous draft: order by the write time, re-read 5 s behind the cursor | No new column | Write times are taken at transaction start and are not ordered by commit; equal timestamps tie; a PostgreSQL failover to a host whose clock is behind can move `now()` backwards, so a cursor can pass over later writes |
| (b) `change_seq` from a sequence, plus a re-read window | Every insert and update takes `nextval` through a trigger; each query reads above the cursor and re-reads the records written within `follow_reread_window` behind it; a connection drops versions it has already sent | Monotonic across failovers, and across restores because sequence values are restored (R-BAK-001); two plain indexes; the window covers out-of-order commits of short transactions; works through a transaction-mode pooler | After a reconnect, versions written within the window before the cut can arrive twice; each event carries record ID and version, so the client drops them |
| (c) A commit-ordered counter row | Each delivery write ends with `UPDATE … SET seq = seq + 1` on one row, which orders positions by commit ([architecture.md](architecture.md) Q-ARCH-15) | Exact: no window and no duplicates | Every delivery write of every `worker` serialises on one row for its commit, which caps delivery throughput at the commit latency, exactly under the load the owner expects to exceed one replica |
| (d) Transaction IDs and snapshots | Each write stores `pg_current_xact_id()`; the event id carries the reader's snapshot, and the next reader emits only rows not visible in it | Exact without a window | The id grows with the number of transactions in progress; transaction IDs do not survive a logical restore or a logical-replication upgrade of the database, so an old id can silently skip events |

**Choice:** (b) — the stream's position is `change_seq`, taken from a sequence on every insert and update, with a re-read window behind the cursor and duplicates dropped by record and version.

**Rationale:** R-API-051's hard rule is "never skip a committed event"; (b) meets it as long as a
delivery-record transaction is shorter than the window, which R-ARCH-019 already requires of every
transaction on the main pool, and an acceptance criterion holds one open for 2 s. (a) cannot
promise it across a failover. (c) is exact, but turns the delivery path into a single serial
point, which the owner's load constraint rules out. (d) is exact in steady state and fragile in
exactly the platform operations (restores, logical upgrades) the owner requires trove to survive.
The price of (b) is bounded duplicates after a reconnect, identifiable by record and version.
R-API-051's acceptance criterion expects no duplicate per record and version after a reconnect;
under (b) that holds once the client drops repeated versions, as the event data allows
(cross-page). The development agent would revisit this if a consumer appears that cannot drop
duplicates, which would make (d), guarded by the incarnation prefix of the event id, worth its
complexity.

**Applied in the spec:** R-OPS-044: "Change position", the column `change_seq` and its trigger,
the indexes, the stream's "Position and source", "Events" and "Resumable on any replica" rules,
`follow_reread_window`, the 5 s ceiling of `follow_poll_interval`, and acceptance criteria for a
late commit, a resumed stream and a stream that holds no connection between queries.

### Q-OPS-22 · Journal flag on event types that several producers share

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The journal flag of R-OPS-014 is declared per event type. `package.published` and
`package.deleted` are shared: NuGet journals both for local repositories, so that a restore keeps
version numbers reserved ([formats/nuget.md](formats/nuget.md) Q-NUGET-10, R-NUGET-033), while npm
journals neither and journals `npm.unpublish` instead ([formats/npm.md](formats/npm.md) Q-NPM-12).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The flag always journals | Every `package.published` and `package.deleted` is journaled, for every format | One rule | npm would journal every publish, which Q-NPM-12 rejects (a replay must never add content), and npm defines no entry for it |
| (b) The flag journals when the producer supplies an entry | For every flagged type, an event without an entry writes no journal row | Shared types work without a second mechanism | A producer bug that omits the entry of an access removal silently drops it from the journal, and a restore revives what it removed (H-37); nothing notices at run time |
| (c) Format-specific event types | NuGet emits `nuget.version_published` and `nuget.version_deleted` with an unconditional flag | One rule; no optional entries | Two events per push, and a second name for a change that webhooks and SIEM rules already parse as `package.*`; the registry's naming contract (R-OPS-014) makes such additions permanent |
| (d) (b), with a declared entry requirement | `journal_entry: required` by default, so an event of such a type without an entry fails its transaction; `optional` only for allow-listed shared types | Shared types work, and the guarantee for revocations stays as strict as before | One more registry attribute and its test |

**Choice:** (d) — the flag means "journaled when the producer supplies an entry"; every flagged type requires an entry unless it is one of the allow-listed shared types, today `package.published` and `package.deleted`.

**Rationale:** (b) is what the shared types need, but applied to every type it weakens the one
guarantee the journal exists for: that a committed revocation always has its journal row. (d) keeps
that guarantee for every type one page owns, because a missing entry rolls the change back just as
a failed audit insert does, and confines the optional form to an allow-list that a test checks.
(a) contradicts npm's decision, and (c) duplicates a public event name for a storage concern. The
development agent would revisit this if a third format needs an optional entry on another shared
type; the allow-list then grows by review, not by default.

**Applied in the spec:** R-OPS-014: the widened flag definition (content withdrawals, version
numbers), "The flag is per event type; the entry comes from the producer", `journal_entry`, and the
acceptance criteria. R-OPS-015: "No entry, no row" in the revocation journal bullet. R-OPS-016:
the journal note on `package.published` and `package.deleted`.

### Q-OPS-23 · Configuration file that is not there

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A missing file is always an error | Start-up stops unless `/etc/trove/trove.yaml` exists | One rule | A replica that runs on defaults and the environment (the normal development case) cannot start |
| (b) The default file may be absent; a file named by `--config` or `TROVE_CONFIG_FILE` must exist (chosen) | The default path is looked up and skipped when missing. An explicit path that is missing fails the command. | Defaults and environment are enough to run; a typo in an explicit path is never silent | Two rules for one file setting |
| (c) Environment only, no file | The file is not read at all | Smallest surface | The chart's ConfigMap cannot be used |

**Choice:** (b).

**Rationale:** The configuration must work from the environment alone, so the default path cannot be
mandatory. An explicit path names a file the operator meant to load, so its absence is an error. The
development agent would revisit this if the default path had to be mandatory for an audit reason.

**Applied in the spec:** this block records the rule of the configuration loader (`internal/config`,
`load.go`); R-OPS-028 is unchanged.

### Q-OPS-24 · Required keys per command, reserved variable names and source labels

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One required set for every command | `migrate` and `admin config validate` need what `serve` needs | One rule | A migration job needs a Valkey address and a bucket it never uses |
| (b) Required keys per command (chosen) | `config.Command` names the keys each command requires. `migrate` needs the database only. `admin config validate` applies the `serve` checks except that the role may be unset. | Each command fails for what it uses | A rule per command to keep in step |
| (c) No command-dependent checks | Each command loads what it is given | Simple | A bad configuration reaches the first use |

Reserved names (chosen with (b)): environment variables with the prefixes `TROVE_CAC_` and `TROVE_TEST_`,
and `TROVE_CONFIG_FILE`, `TROVE_POD_NAME`, `TROVE_POD_NAMESPACE`, `TROVE_NODE_NAME` and `TROVE_POOL_NAME`,
are not configuration keys, so the strict environment validation ignores them. Source labels that
`admin config validate` prints for each value: `default`, `file`, `env`, `flag:--role` (or the flag
name), and `file:<secret path>` for a value read from a secret file.

**Choice:** (b), with the reserved names and labels above.

**Rationale:** A command should fail for the keys it uses and no others. The reserved names let the
test suite and the chart set their own variables without the validator rejecting them. The labels let an
operator see where each value came from. The development agent would revisit the per-command rules if a
command starts to use a key that another command validates differently.

**Applied in the spec:** this block records the rules of `internal/config` (`load.go`, `admin.go`); R-OPS-028
is unchanged.

### Q-OPS-25 · OTLP span export: own queue, transport and endpoint rule

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The SDK's batch span processor | Spans are batched and dropped without a count | Standard component | A dropped span is invisible; the drop counter of R-OPS-018 cannot be kept |
| (b) An own bounded queue in front of the SDK's OTLP exporter (chosen) | `queueProcessor` holds up to a fixed number of ended spans. A full queue drops the span and increments `trove_tracing_spans_dropped_total`. Request handling never waits. | The drop is counted, and the queue size is fixed | Own code that the SDK's processor would otherwise provide |
| (c) Synchronous export per span | Each request waits for its spans | No queue | Request latency depends on the collector |

Transport (chosen with (b)): the OTLP exporter of the OpenTelemetry SDK sends the spans. It does not go
through `internal/outbound`, so the egress address policy does not apply to it ([H-53](#h-53--span-export-bypasses-the-egress-address-policy)).
Endpoint (chosen with (b)): `tracing.endpoint` is not required when `OTEL_EXPORTER_OTLP_ENDPOINT` or
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is set.

**Choice:** (b), with the transport and endpoint rules above.

**Rationale:** Counting drops is the only way an operator learns that traces are lost, and a request must never
wait for an exporter. The development agent would revisit the own queue if the SDK's processor gained a
drop counter, which would let the code go back to the standard component.

**Applied in the spec:** R-OPS-018: the export paragraph names the bounded queue of the process
(Q-OPS-25); the metrics table has `trove_tracing_spans_dropped_total` as before.

### Q-OPS-26 · Metric label names of the outbound and the object-store metrics

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `purpose` on the outbound families | `trove_outbound_*{purpose}` | Names what the value is | Differs from the object-store family's label; a rename of a family that is not released yet |
| (b) `kind` for the purpose on the outbound families (chosen) | `trove_outbound_requests_total{kind,code}`, `trove_outbound_request_duration_seconds{kind}`, `trove_outbound_egress_denied_total{kind}` | Matches the code; the families are in R-OPS-008 | The name `kind` is generic |
| (c) `operation` for the object-store families, `op` as in R-STOR-033 | `trove_storage_s3_requests_total{op,...}` | Matches the text of R-STOR-033 | `op` is not the label R-OPS-007 allows; the code uses `operation` |

**Choice:** (b) for the outbound families, and `operation` (not `op`) for the object-store families.

**Rationale:** R-OPS-007 lists `operation` among the allowed labels, and the code already uses it. The
outbound families name their purpose `kind` in code and in the catalog. The development agent would
revisit the outbound name if an operator dashboard needs one label name across both families.

**Applied in the spec:** R-OPS-008 lists the three `trove_outbound_*` families; R-STOR-033 names the label
`operation` (see [storage.md](storage.md)).

### Q-OPS-27 · Secret values in configuration dumps and logs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Redaction in the dump code only | The effective-configuration view and the logs mask values by rule | Nothing changes in the type | Any other serializer of the type leaks the value |
| (b) `config.Secret` implements `MarshalJSON` and `LogValue` (chosen) | The value never serialises and never logs; `String` and `LogValue` return the redaction marker | The type is safe wherever it is printed | One type to keep in step |
| (c) Secret values loaded only as files and never as strings | Every secret is a path | No value in memory as text | Environment-variable secrets lose their only form |

**Choice:** (b).

**Rationale:** A secret that a serializer can reach will eventually be serialized, so the protection belongs in
the type. Closing it in the type also closes the gap that would otherwise be recorded as an open item. The
development agent would revisit this when a second secret type appears that does not use `config.Secret`.

**Applied in the spec:** R-OPS-013 and the effective-configuration view are consistent; no requirement text changed.

### Q-OPS-28 · Instance name source

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host name only | `os.Hostname()` | Simple | In Kubernetes every pod of a Deployment may share one name on a node, and two processes on one machine collide |
| (b) `TROVE_POD_NAME`, then `POD_NAME`, then `HOSTNAME` inside Kubernetes (`KUBERNETES_SERVICE_HOST` set), otherwise `<hostname>-<pid>` (chosen) | The name is the pod name where one exists, and unique otherwise | Names unique per process and readable in `/api/v1/system/replicas` | Two sources to document |
| (c) A random name | A UUID | Always unique | Unreadable in the replica registry |

**Choice:** (b).

**Rationale:** The replica registry shows the instance name to operators, so it must name the pod. Outside
Kubernetes, the process ID keeps two processes apart. Open item in the code, not in this page: `trove migrate`
has its own instance-name function (`cmd/trove/migrate.go`) that reads `TROVE_POD_NAME` and then falls back to
`<hostname>-<pid>`; the order of (b) is not applied there yet. The development agent would revisit this if the
instance name must be stable across restarts of one pod.

**Applied in the spec:** R-OPS-003 states the order (sentence on the pod name); the column comment in its table
still says "pod name (`TROVE_POD_NAME`), else host name".

### Q-OPS-29 · A replica started on a database that is not yet migrated

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse to start | The process exits when the schema is behind | Clear failure | A rolling upgrade whose migration job is late crash-loops every replica |
| (b) Wait, not ready, until the schema check passes; a schema below the compatibility floor fails start-up (chosen) | The replica runs its wait loop with readiness false. It retries the schema check with a backoff of up to 15 s. A schema below the floor (`FloorError`) returns an error and the process stops. | The pre-upgrade Job may finish late without a restart storm; a too-old schema is still refused | A replica sits not-ready until someone migrates |
| (c) Serve with the old schema | The replica answers requests against whatever schema exists | Available during the gap | Violates the expand/contract guarantee of [architecture.md](architecture.md) R-ARCH-026 |

**Choice:** (b).

**Rationale:** A migration is normally run first by the chart's pre-upgrade Job, so a replica that starts
early waits instead of failing. The compatibility floor is the one condition that never heals by waiting.
The development agent would revisit this if a rolling upgrade needs readiness to be true before the schema
check succeeds.

**Applied in the spec:** this block records the start-up behaviour of `internal/app` (`wire_postgres.go`);
R-OPS-001 is unchanged.

### Q-OPS-30 · Ping while the replica drains

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ping follows readiness | `503` as soon as `draining` is set | One rule: ping equals `/readyz` | The drain delay exists so that requests an edge still routes to the pod succeed (R-ARCH-024 step 2); the one request that is a ping gets `503` in that window. Measured: 1 of 16938 and 3 of 16502 pings during a node drain |
| (b) Ping answers `200` while the replica serves, the drain phase included; `503` when it is unready for any other reason or never became ready (chosen) | Ping reads "no gating check other than `draining` fails, and a draining replica had been ready before" | The replica keeps its promise to serve everything during the delay; a pod whose database check fails still answers `503` | Ping and `/readyz` differ during a drain, by design: readiness leaves the Service, ping tells a client whether the replica it reached works |
| (c) Ping always `200` | Ping proves the HTTP server only | Simplest | Ping stops being a check of base URL, token and a working replica (R-OPS-002) |

**Choice:** (b).

**Rationale:** Readiness decides who receives new traffic; ping answers a request that already arrived. A request that
arrives during the drain delay is one the replica has promised to serve, so it must not be answered `503`. A replica that
drains before it was ever ready never served and keeps answering `503`. The development agent would revisit this if an
edge health-checks through the ping path instead of using the pod's readiness.

**Applied in the spec:** R-OPS-002 states the rule and gains an acceptance criterion; R-ARCH-024 step 2 names ping.

### Q-OPS-31 · Database readiness during a short outage

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As before: `check_interval` × `failure_threshold` | The check fails after about 10 s | One rule | A CloudNativePG switchover takes 10 to 15 s. Replicas leave the Service for it, although their queries retry for `database.retry.max_elapsed` and would have succeeded |
| (b) Raise `failure_threshold` | 4 failures at 5 s | No new key | Every check is slower, the object store and the invalidation check too; a 15 s switchover still sits at the edge |
| (c) A grace on the checks that read PostgreSQL: `health.database_outage_grace`, default 20 s (chosen) | Once the replica has been ready, a failure of `database` or `schema` that is a connectivity failure keeps the check `degraded` and the replica ready until it has lasted the grace; then it fails like today | A switchover of up to 15 s never flips readiness, a 30 s outage does (about 20 to 25 s after it began); a schema that is behind or a rejected credential still fails at once | One more key, validated to stay below `database.retry.max_elapsed` |

**Choice:** (c), default `20s`. `0` switches the grace off.

**Rationale:** The grace must exceed the longest switchover that is routine (15 s) and stay below the point where
queries give up (`database.retry.max_elapsed`, 30 s), so a replica turns unready before it answers `503` to every request.
A failure counts as an outage when it is a connectivity failure (a refused or timed-out connection, SQLSTATE `57P01`,
`57P02`, `57P03`, `53300`, class `08`, a standby, a check that timed out); anything else gates at once. The grace runs from
the first failed evaluation of an unbroken run and restarts only after the check has passed again
(`health.success_threshold`). The first readiness gets no grace, so a replica never becomes ready on an unreachable
database. The `invalidation` check keeps its own window (`cluster.epoch_max_staleness`, at least 15 s, plus its debounce),
which for one outage never ends before the `database` grace does. The development agent would revisit the default when a
platform's switchover is measured above 15 s.

**Applied in the spec:** R-OPS-001 (`database` and `schema` rows, the paragraph on the outage grace, the configuration
blocks and the first acceptance criterion); ha-and-helm.md R-HA-008 and its failure table.

## What this does not cover

### H-53 · Span export bypasses the egress address policy

Stage: the OTLP exporter of `tracing.endpoint` (R-OPS-018, Q-OPS-25), which sends spans through the
OpenTelemetry SDK's own transport.

Live only when `tracing.enabled` is `true`. Subject: the destinations that a replica can send span data to;
span attributes are scrubbed of secrets before export (R-OPS-013), so the exposure is metadata about requests
and not credentials.

What makes it real: a collector endpoint that resolves to an address the egress policy would refuse, set in
the configuration by someone who can write it. The policy that refuses the same address for upstreams does
not apply to the exporter.

### H-39 · Audit records are not tamper-evident

In two-role mode, trove's runtime role can only insert and read audit events, and it cannot shorten
their retention (R-OPS-015). Nothing makes the records tamper-evident, though:

- the schema owner and any superuser can alter or delete rows;
- in single-role mode, trove's own role is the schema owner, so a compromised replica can too;
- the runtime role can insert forged events in either mode.

Integrity against those holders rests on shipping the `log: audit` stream to a store outside
trove's control, for example object storage with object lock or a SIEM. Neither trove nor the
chart enforces that (Q-OPS-4). The gap is live from the first release for every installation that
does not ship logs externally. For single-role installations it also covers a compromised replica.
It becomes real for anyone who obtains the owner credential of the migration Job, a superuser
login, or, in single-role mode, the runtime credential.

### H-40 · Outbound address checks are weaker through an outbound proxy

When a webhook handler names an outbound proxy (R-OPS-042), the proxy resolves and connects to the
target. trove can only check its own resolution of the name before sending, so a name that
resolves differently for the proxy, or changes in between, reaches whatever address the proxy
chooses. Proxy-side egress rules are the only full control. The gap is live only for subscriptions
that use a proxy.

### H-41 · Egress is unrestricted, and the ingress policy's enforcement is unverified

With the default `metrics.auth: none`, any pod that can reach port 8081 of a `web` or `worker`
replica reads metrics: repository keys, request and byte volumes, queue depth, backup run state. It
also reads verbose readiness: dependency latency, schema version, whether Valkey is reachable. The chart ships an
ingress NetworkPolicy, enabled by default, that admits port 8081 only from the monitoring namespace and the probe
CIDRs (R-HA-042). Its enforcement on the cluster's CNI is not verified in this build. Clusters whose CNI does not
enforce NetworkPolicy, or installations that switch it off, expose this data cluster-wide unless
`metrics.auth: static` is set. Separately, the replicas' egress is not restricted: the chart renders no egress
policy, so a compromised replica can reach any address (R-HA-042, H-6).

### H-42 · Secret masking in the delivery log matches known encodings only

The delivery log masks secret values in plain, base64 and URL-encoded form, and replaces the values
of authorization-type headers entirely (R-OPS-044). A template that transforms a secret further,
for example by hashing it or embedding it in a JWT, puts a derived value into the stored request
that masking does not recognise. That derived value is readable by holders of `webhooks.read`. The
gap is live for templated handlers with such templates. Review templates, and grant `webhooks.read`
narrowly.
