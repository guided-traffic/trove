# High availability and Helm deployment

Status: specification — not implemented

This page specifies how trove stays available and how it is deployed. It covers:

- The replica model: identical replicas, the optional `api`/`worker` role split, and how replicas
  coordinate only through PostgreSQL (INV-2, INV-12).
- The failure contract of every component: pod crash, node drain and node loss, PostgreSQL failover
  with reconnect and retry, object-storage outage, spool exhaustion and a failed migration.
- Leader-less scheduling and singleton work under advisory-lock leases.
- Rolling upgrades with expand/contract migrations and the migration Job.
- PostgreSQL requirements: version floor, server settings, roles and privileges, TLS, connection
  budget and credential rotation.
- Backup, restore and disaster recovery, including why a restored database never points at a
  deleted blob.
- The Helm chart `deploy/helm/trove`: every resource it renders and its complete `values.yaml`.
- Edge-proxy requirements for large uploads, sizing guidance, the local development profile
  (kind, CloudNativePG, SeaweedFS) and the failure-injection suite that proves all of the above.

Semantics owned elsewhere are referenced, not repeated. The probe endpoints and replica registry
are in [operations-and-observability.md](operations-and-observability.md). Blob commit, garbage
collection and the object-store client are in [storage.md](storage.md). Secret sealing and
outbound TLS are in [repositories-and-proxy.md](repositories-and-proxy.md). The break-glass account
is in [identity-and-authentication.md](identity-and-authentication.md).

Covers: the catalog pages [ha-and-deployment.md](../features/ha-and-deployment.md) (`HA`, all MVP
and P1 entries), the upgrade and backup entries of
[operations-and-monitoring.md](../features/operations-and-monitoring.md) (OPS-033, OPS-072,
OPS-073, OPS-074, OPS-076), and the deployment side of
[storage-and-blobs.md](../features/storage-and-blobs.md) (spool volume, object-store credentials
through workload identity). The storage entries themselves belong to [storage.md](storage.md).

## Where this page hands over

| Topic | Owner |
|---|---|
| `/healthz`, `/readyz` semantics, check list, `health.*` keys, drain delay | [operations-and-observability.md](operations-and-observability.md) R-OPS-001 |
| Replica registry table `replica`, heartbeat keys, `GET /api/v1/system/replicas` | [operations-and-observability.md](operations-and-observability.md) R-OPS-003 |
| `server.base_url`, trusted proxies, forwarded headers | [operations-and-observability.md](operations-and-observability.md) R-OPS-005 |
| Metric names and the metrics catalog | [operations-and-observability.md](operations-and-observability.md) R-OPS-006 to R-OPS-008 |
| Spool admission, commit protocol, garbage collection, orphan sweep, object-store client, outage table | [storage.md](storage.md) R-STOR-004, R-STOR-005, R-STOR-024 to R-STOR-026, R-STOR-030 to R-STOR-036 |
| Secret sealing format, key ring and the `secrets-reencrypt` task | [architecture.md](architecture.md) R-ARCH-021 |
| Key-ring status `GET /api/v1/system/encryption` and the re-encryption trigger | [api-and-ui.md](api-and-ui.md) R-API-049 |
| Migration files, their header comments and the migration runner | [architecture.md](architecture.md) R-ARCH-026 |
| Content origin semantics (`server.content_base_url`) | [architecture.md](architecture.md) R-ARCH-029 |
| Append-only audit rows, `audit_policy`, `audit_maintain_partitions()` | [operations-and-observability.md](operations-and-observability.md) R-OPS-015 |
| Outbound CA bundles, egress address policy | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-035, R-REPO-038 |
| Cross-replica fetch collapse and its lock pool | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 |
| Break-glass account, `trove admin` bootstrap commands, session table | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-014, R-AUTH-015, R-AUTH-010 |
| Authorization cache staleness and fail-closed behavior | [authorization.md](authorization.md) |
| Job queue schema, worker loop, in-process caches, server shutdown timeout | [architecture.md](architecture.md) |
| Scheduled task catalog | [operations-and-observability.md](operations-and-observability.md) |

## Topology

```text
            clients: npm, pnpm, Yarn, dotnet, nuget.exe, browsers, GitLab jobs
                                   │ HTTPS
                  ┌────────────────▼─────────────────┐
                  │  Ingress controller or Gateway   │  TLS ends here (R-HA-037)
                  └────────────────┬─────────────────┘
                                   │ HTTP, Service <fullname>:8080
         ┌─────────────────────────┼─────────────────────────┐
  ┌──────▼───────┐          ┌──────▼───────┐          ┌──────▼───────┐
  │ trove pod  │          │ trove pod  │   ...    │ trove pod  │  roles [api, worker]
  │ :8080  :8081 │          │ :8080  :8081 │          │ :8080  :8081 │  spool: emptyDir
  └──┬────────┬──┘          └──┬────────┬──┘          └──┬────────┬──┘
     │ SQL    │ S3 API         │        │                │        │
     ▼        ▼                ▼        ▼                ▼        ▼
  ┌──────────────────────┐                       ┌──────────────────────────┐
  │ PostgreSQL primary   │──streaming──▶ standby  │ S3 bucket, versioning on │
  │ (CloudNativePG, RDS) │                        │ (AWS S3, MinIO, Ceph RGW) │
  └──────────────────────┘                       └──────────────────────────┘
     ▲
     └── migration Job (Helm pre-install/pre-upgrade hook): trove migrate up
```

Replicas never open connections to each other. Every cross-replica signal (cache invalidation,
leases, job claims, membership) goes through PostgreSQL, so the pod network needs no
replica-to-replica rule and no peer discovery.

## Replica model and coordination

### R-HA-001 · Active-active replicas without a primary node
Priority: **MVP** · Covers: HA-001, HA-002, HA-010

- Every replica MUST be able to serve every client request and every management request. No
  replica holds state that another replica needs to answer a request (INV-2). The only local state
  is the spool (R-HA-035) and in-process caches, which are invalidated via R-HA-004.
- There MUST be no primary, leader or seed replica in configuration. Work that must run once per
  cluster runs under leases that any eligible replica can take (R-HA-005, R-HA-006).
- A replica is *eligible for background work* when its `roles` include `worker`. A replica with
  `roles: [api]` serves HTTP and never claims jobs or leases. A replica with `roles: [worker]`
  serves only the operations listener. The default is `roles: [api, worker]` (INV-2 "all").
- A production deployment MUST run at least two replicas (INV-12). The chart enforces this unless
  `highAvailability.enforce` is false (R-HA-029). A single replica is supported for development
  only. It has the same code path and the same dependencies (external PostgreSQL and S3), so
  moving from one replica to several is a change of `replicaCount` and nothing else. The catalog's
  "single node with embedded database and local file storage" topology does not exist in trove.
- Adding a replica needs no configuration besides the replica count. The new replica becomes
  eligible for background work as soon as it is ready. Removing a replica needs no handover: its
  leases end with its database sessions (R-HA-006) and its job leases expire (R-HA-005).

Configuration (trove; the chart sets it per Deployment, R-HA-030):

```yaml
roles: [api, worker]   # default (proposed); [api] or [worker] with the role split (R-HA-002)
```

Acceptance criteria:

- With three replicas behind the Service, `for i in $(seq 1 300); do curl -s -o /dev/null -w '%{http_code}\n' https://trove.example.com/api/v1/system/ping; done | sort | uniq -c`
  prints only `200`, and the `X-Trove-Replica` header of authenticated calls shows all three pod
  names.
- `kubectl scale deploy/trove --replicas=4` adds a replica that serves traffic and appears in
  `GET /api/v1/system/replicas` without any other change.
- `helm upgrade … --set server.replicaCount=1 --set highAvailability.enforce=false` yields a
  working single-replica installation against the same database and bucket.

### R-HA-002 · Role split into api and worker Deployments
Priority: **P1** · Covers: HA-015

- With `roleSplit.enabled: true` the chart MUST render two Deployments: `<fullname>-api` with
  `roles: [api]` and `<fullname>-worker` with `roles: [worker]`. Each has its own replica count,
  resources, autoscaler, disruption budget and scheduling settings. Without the split, the chart
  renders one Deployment `<fullname>` with `roles: [api, worker]`.
- The client Service selects only pods with the `api` role. Worker pods are reached only on the
  operations port (probes and metrics).
- With the split enabled, `worker.replicaCount` (or `worker.autoscaling.minReplicas`) MUST be at
  least 1, otherwise no scheduled work runs. The chart refuses to render (R-HA-029). With
  `highAvailability.enforce` the minimum is 2, so that GC and other singleton work survive a worker
  pod loss without waiting for a reschedule.
- Workers and API pods run the same image and the same configuration file; only `roles` differs.
  No component runs in a separate image or as a sidecar.
- Reasons to split: background work (GC, retention runs, reindexing, cache fills of prefetch jobs)
  stops competing with request latency for CPU and memory, and API pods can scale on CPU without
  multiplying workers.

Acceptance criteria:

- With the split and `server.replicaCount: 2`, `worker.replicaCount: 2`,
  `kubectl get deploy -l app.kubernetes.io/instance=trove` lists `trove-api` and
  `trove-worker`; `GET /api/v1/system/replicas` shows roles `["api"]` and `["worker"]`.
- A manual GC run (`POST /api/v1/system/tasks/gc/run`) through the API pods is executed on a
  worker pod (its `trove_lease_held` series is 1 during the run).
- `helm template … --set roleSplit.enabled=true --set worker.replicaCount=0` fails and names
  `worker.replicaCount`.

### R-HA-003 · Replica registry as the coordination view
Priority: **MVP** · Covers: HA-006, HA-009

The replica registry of R-OPS-003 (table `replica`, heartbeat every `system.heartbeat_interval`,
stale after `system.heartbeat_stale_after`) is the single source of truth for "which replicas are
alive, with which version and roles". This page uses it for four HA mechanisms. The table, the
heartbeat, the pruning of stale rows and the read-only API `GET /api/v1/system/replicas` are
therefore MVP (Q-HA-12). The UI status screen is MVP as well ([api-and-ui.md](api-and-ui.md)
R-UI-038, Q-API-8): it renders only from MVP endpoints and is the only UI view of the replicas, the
scheduled tasks and the key ring. R-HA-001 and R-HA-020 verify rollouts through the replica API.

| Use | Rule |
|---|---|
| Contract-migration guard | A contract step runs only when no live replica runs a version that still uses the dropped object (R-HA-018). |
| Cluster minimum version | `min(version)` over live replicas gates features and background migrations whose data the previous release cannot interpret (R-HA-020, R-HA-022). |
| Connection budget check | The migration Job compares `max_connections` with the budget derived from the expected replica counts (R-HA-016). |
| Post-restore cleanup | `trove admin post-restore` empties the table, because every row in a restored database describes a pod that no longer exists (R-HA-025). |

- "Live" means `heartbeat_at > now() - system.heartbeat_stale_after`, evaluated with PostgreSQL's
  clock. Replica clocks are never compared with each other (R-HA-005).
- A replica in state `draining` still counts as live for the contract guard: it may still run old
  code for up to its grace period.
- The migration Job does not insert a `replica` row. It is not a serving replica and must not appear
  in the minimum-version computation.

Acceptance criteria:

- During a rolling upgrade from 0.3.0 to 0.4.0, `GET /api/v1/system/replicas` shows both versions;
  after `kubectl rollout status deploy/trove` returns, only 0.4.0 is listed within
  `system.heartbeat_stale_after`.
- `kubectl delete pod --grace-period=0 --force <pod>` makes the row stale within 30 s; the
  contract guard of a migration started afterwards ignores it.

### R-HA-004 · Cross-replica invalidation over LISTEN/NOTIFY
Priority: **MVP** · Covers: HA-006

- Every replica MUST hold exactly one dedicated `LISTEN` session, outside the connection pool, for
  all invalidation channels. Channel names and payloads belong to the subsystems that send them
  ([architecture.md](architecture.md)). A `NOTIFY` is sent in the same transaction as the change
  it announces, so it is delivered only after commit
  ([NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html)).
- Notifications sent while a replica is disconnected are lost. Therefore, after every
  (re)connection of the `LISTEN` session, a replica MUST drop every in-process cache that
  notifications keep fresh *before* it reports `notify` as passing (R-OPS-001), then re-subscribe.
- Every such cache MUST also carry a maximum entry age (its owner sets it; this page requires one to
  exist), so a missed notification cannot keep a stale entry alive indefinitely.
- Payloads are versioned. A replica that receives a payload version it does not know MUST flush the
  whole cache of that channel instead of ignoring the message (R-HA-020: during a rolling upgrade,
  the newer release may send messages the older one cannot parse).
- When the listener connection is recycled (credential rotation, R-HA-017), the replica MUST open
  the new session and `LISTEN` on it before it closes the old one, so recycling causes no gap and
  no cache flush.
- Payloads stay below PostgreSQL's 8,000-byte limit. A change that touches many objects sends one
  "flush" message per cache, not one message per object.
- **Delivery probe.** After every (re)connection of the `LISTEN` session, the replica sends a probe
  notification on its own replica channel through the main pool. It reports `notify` as passing
  only after that probe has arrived on the listener session. The probe proves the whole path, and
  it is the guard against an unsupported transaction-mode pooler (R-HA-006, Q-HA-6), under which
  the listener is expected never to receive it. A missing probe keeps the replica not ready and
  logs, once per minute, that `LISTEN` delivery failed and that transaction-mode poolers are not
  supported.

Acceptance criteria:

- `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name LIKE 'trove/%/listen'`
  on one replica increments `trove_cache_flushes_total{reason="reconnect"}` there, and a
  permission change made through another replica afterwards takes effect on it.
- With the listener recycled by a credential rotation, `trove_cache_flushes_total{reason="reconnect"}`
  does not increase.
- A replica connected through PgBouncer with `pool_mode = transaction` (test fixture) stays not
  ready with `"failing":["notify"]` and logs the pooler message; the same replica through
  `pool_mode = session` becomes ready.

### R-HA-005 · Leader-less scheduling and job leases
Priority: **MVP** · Covers: HA-002, HA-007

Background work runs through the PostgreSQL job queue ([architecture.md](architecture.md) owns
the table and the worker loop). Schedules (cron expressions) belong to the subsystems that own the
work. This requirement fixes the HA guarantees both must give.

**Scheduling without a fixed leader.**

- One scheduler loop runs on each worker-eligible replica. Each tick (`jobs.schedule_tick`)
  tries to take the `scheduler` lease (an advisory-lock lease, R-HA-006). Only the holder enqueues
  due work. When the holder dies, its session ends, PostgreSQL releases the lock, and the next tick
  of another replica takes over. No replica is configured as scheduler.
- Enqueuing MUST also be safe without the lease. A due schedule is claimed with a conditional
  update in the same transaction that inserts the job:

  ```sql
  UPDATE schedule
     SET next_run_at = $next, last_enqueued_at = now()
   WHERE name = $1 AND enabled AND next_run_at <= now()
  RETURNING name;
  -- zero rows: another replica enqueued it first; do nothing
  ```

  Under READ COMMITTED, a second concurrent `UPDATE` waits for the first and re-evaluates the
  `WHERE` clause against the committed row, so the same occurrence is never enqueued twice, even if
  two replicas briefly both believe they hold the lease
  ([UPDATE under READ COMMITTED](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)).
- `$next` is computed from the schedule's previous `next_run_at`, not from the replica clock. Runs
  missed during an outage are coalesced into one run, never replayed one by one.

**Job leases.**

- A worker claims queued jobs with `FOR UPDATE SKIP LOCKED` in a short transaction that sets
  `state = 'running'`, `locked_by` (the replica's `instance_uid`) and
  `locked_until = now() + jobs.lease_duration`, then commits. No transaction stays open while the
  job runs.
- The worker extends the lease every `jobs.heartbeat_interval`. If an extension fails (database
  unreachable, row no longer owned), the worker MUST cancel the job's context and stop changing
  shared state for it.
- A reaper (part of the scheduler tick) returns jobs whose `locked_until < now()` to `queued`,
  increments `attempt`, and moves jobs over `max_attempts` to `failed`.
- All lease and heartbeat timestamps use PostgreSQL's `now()`. Replica clocks never decide whether a
  lease has expired.
- During a rolling upgrade a worker claims only job kinds it implements (`WHERE kind = ANY($known)`,
  [architecture.md](architecture.md) R-ARCH-016). A kind whose arguments change incompatibly gets a
  new kind name, so older workers leave it to newer ones.

HA-relevant columns of the job table (sketch; [architecture.md](architecture.md) owns the full
definition):

```sql
CREATE TABLE job (                           -- excerpt; full definition in architecture.md R-ARCH-016
  id               uuid        PRIMARY KEY,
  kind             text        NOT NULL,
  state            job_state   NOT NULL DEFAULT 'queued',
  run_at           timestamptz NOT NULL DEFAULT now(),
  attempt          integer     NOT NULL DEFAULT 0,
  max_attempts     integer     NOT NULL DEFAULT 10,
  locked_by        uuid,                       -- replica.instance_uid
  locked_until     timestamptz,                -- lease, extended by heartbeat
  checkpoint       jsonb                       -- persisted cursor of a restartable job
);
CREATE INDEX job_fetch ON job (queue, priority, run_at) WHERE state = 'queued';
CREATE INDEX job_lease ON job (locked_until) WHERE state IN ('running', 'cancelling');

CREATE TABLE schedule (
  name             text        PRIMARY KEY,    -- e.g. 'storage.gc.incremental'
  cron             text        NOT NULL,       -- 5 fields, UTC
  enabled          boolean     NOT NULL DEFAULT true,
  next_run_at      timestamptz NOT NULL,
  last_enqueued_at timestamptz
);
```

Configuration:

```yaml
jobs:                       # section owned by architecture.md (R-ARCH-016)
  schedule_tick: 10s        # default (proposed)
  lease_duration: 60s       # default (proposed); must be ≥ 3 × heartbeat_interval
  heartbeat_interval: 15s   # default (proposed)
```

Acceptance criteria:

- With three worker replicas and a schedule `* * * * *` for a test job that records its start, 60
  minutes produce exactly 60 runs, also while one replica is restarted every 5 minutes.
- `kubectl delete pod --grace-period=0 --force` of the replica that runs a long test job makes
  another replica run it again after at most `lease_duration` plus one tick; `attempt` is 2.
- Setting a replica's system clock 10 minutes ahead (test build with a fake clock) changes neither
  the number of runs nor any lease decision.

### R-HA-006 · Singleton work under advisory-lock leases with fencing
Priority: **MVP** · Covers: HA-007

Work that must not run twice at the same time (the scheduler tick, garbage collection, the orphan
sweep, retention runs, secret re-encryption, background migrations) runs under an
*advisory-lock lease* (INV-12). Reindexing is not on this list: a manual reindex and an automatic
index rebuild are jobs with the dedup key `index:<repository id>` ([architecture.md](architecture.md)
R-ARCH-016), which keeps two runs for one repository apart without a lease, because leases carry
registered numbers only (R-ARCH-017). A lease works as follows:

1. The worker takes a connection from the dedicated lease pool (`database.lease_pool_size`) and
   calls `pg_try_advisory_lock(<class>, <key>)`, a session-level lock
   ([advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)).
   Failure to acquire means another replica holds the lease, so the worker skips the work.
2. **Fencing.** Every state change the singleton makes in PostgreSQL MUST be executed on that same
   session. The lease and the writes share one session. If the session has died, the lock is gone,
   *and* the write fails, so a replica that lost its lease cannot commit anything. Actions outside
   PostgreSQL (S3 deletes) happen only after the database change that authorizes them has committed
   on that session ([storage.md](storage.md) R-STOR-024 follows this order).
3. The worker checks the session every `database.lease_check_interval` (`SELECT 1`). On failure it
   cancels the work, records the loss (`trove_lease_lost_total{task}`), and does not try to
   re-acquire in the same run. The next scheduled run starts fresh. All singleton jobs MUST be
   restartable from their persisted cursor.
4. A partitioned replica detects the loss of its session through TCP keepalive and user timeout
   (`database.keepalive.*`, R-HA-015) before the server releases the lock through its own keepalive
   (R-HA-013). trove sends the server-side keepalive settings as startup parameters on every
   session that holds advisory locks (R-HA-015), so the server-side bound holds even where the
   server's own settings are left at the operating-system default. Client-side detection
   (default ≤ 30 s) is shorter than server-side release (default ≤ 120 s), and fencing (2) covers
   the remaining window.
5. Transaction-mode and statement-mode connection poolers break session-level locks, `LISTEN` and
   the fetch-collapse locks of R-REPO-026. trove MUST NOT be connected through one (Q-HA-6). Direct
   connections and session-mode poolers (for example PgBouncer with `pool_mode = session`) are
   supported. The delivery probe of R-HA-004 keeps a replica not ready in the usual
   misconfiguration. It is a guard, not a proof; the supported setups are the ones named here.
   Splitting the main pool onto a transaction-mode pooler while session work connects directly is
   P2 (Deferred).

**Lock key registry.** Every advisory lock uses the two-argument form with a registered category as
`classid`, as fixed by the registry of [architecture.md](architecture.md) R-ARCH-017, so `pg_locks`
names every lock (ADR-077). This page uses three of its categories:

| `classid` | Category | Key (`objid`) | Used by |
|---|---|---|---|
| 1 | `lease` | registered lease number of the task (`internal/cluster/locks.go`) | singleton leases of this requirement and R-HA-005 |
| 2 | `fetch` | 32-bit hash of the flight key | fetch collapse of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 |
| 3 | `migration` | 0 | schema migration (R-HA-019) |

Configuration:

```yaml
database:
  lease_pool_size: 4        # default (proposed): concurrent singleton leases per replica
  lease_check_interval: 5s  # default (proposed)
```

Acceptance criteria:

- With GC running on replica A, `SELECT classid, objid, pid FROM pg_locks WHERE locktype = 'advisory' AND classid = 1`
  shows one row; terminating that backend (`pg_terminate_backend`) stops the run on A within
  `lease_check_interval` + 1 s, A logs the lease loss, and no further GC writes from A appear.
- A fault-injection test blocks A's traffic to PostgreSQL (NetworkPolicy) while A is in a GC batch.
  A issues no S3 `DeleteObject` for a row whose `deleting` transition did not commit, and B takes
  the lease after the server releases the session.

## Failure modes

### R-HA-007 · Failure-mode contract
Priority: **MVP** · Covers: HA-003, HA-004

trove MUST behave as in this table. Each row is exercised by the failure-injection suite
(R-HA-052). "Client effect" assumes at least two replicas.

| Failure | Detection | trove behavior | Client effect | Recovery |
|---|---|---|---|---|
| Container crash (panic, OOM kill) | kubelet; session end releases locks | In-flight requests of that pod end; nothing half-committed (a `201` means durable, R-STOR-004); its job leases expire; its advisory locks are released immediately | Requests on that pod fail with connection reset or `502` from the proxy; npm (`fetch-retries`) and NuGet clients retry | kubelet restarts the container; the spool directory is emptied at start (R-HA-012) |
| Pod eviction, node drain, rolling restart | `SIGTERM` | Drain (R-HA-011): readiness fails at once, in-flight requests finish within the grace period, jobs are released | None for requests shorter than the grace period; longer transfers are cut | PDB keeps all but one replica serving (R-HA-032) |
| Node loss (no `SIGTERM`) | Node controller marks the node not ready | Endpoints of the lost pods are removed when their Ready condition flips; job leases expire; advisory locks are released by the server's TCP keepalive, which trove sets on its lock-holding sessions (R-HA-013, R-HA-015) | Requests routed to the lost pods fail until the endpoints are removed (tens of seconds) | Pods are recreated after `server.nodeFailureTolerationSeconds` (R-HA-032) |
| One replica cut off from PostgreSQL | `database` check (R-OPS-001) | That replica answers `503` and turns not ready after the check threshold; its singleton work stops (R-HA-006) | Traffic moves to the other replicas | Automatic when the network heals |
| PostgreSQL switchover or failover | Connection errors, SQLSTATE `57P01`, `25006`, class `08` | Retry with backoff within `database.retry.max_elapsed`, pool reset, listener reconnect with cache flush, singleton work aborts and resumes on the next run (R-HA-009) | Requests stall for up to the retry budget; those that exceed it get `503` with `Retry-After` | Automatic |
| PostgreSQL unavailable beyond the retry budget | `database` check on every replica | Every replica answers `503` and turns not ready; liveness stays `200`, so nothing restarts; authorization fails closed after `authz.cache.max_staleness` ([authorization.md](authorization.md)) | `503` for everything (the Service has no ready endpoints) | Readiness returns when PostgreSQL does; no manual step |
| PostgreSQL connection limit reached (`53300`) | Connect error | Backoff and retry inside the budget; metric `trove_db_retries_total{kind="too_many_connections"}` | Latency, then `503` | Size the budget (R-HA-016) |
| Object storage unreachable from all replicas, for any duration | Circuit breaker (R-STOR-033), `objectStore` check, alert `TroveObjectStoreUnreachable` | Behavior per [storage.md](storage.md) R-STOR-036: new uploads `503`, downloads of stored content `503`, metadata, listings, deletes, deduplicated uploads and the API keep working. Running replicas stay ready (R-HA-008, Q-HA-1). A replica that starts during the outage stays not ready until its startup check (R-STOR-035) succeeds; it retries without exiting, so it becomes ready as soon as the store returns | Degraded: reads of metadata, the UI, logins and CI token exchange succeed; blob transfers fail with retryable `503` | Automatic; no blob is marked missing because of errors |
| Object storage unreachable from one replica only | Its circuit opens; peers' do not; alert `TroveObjectStoreUnreachableSingleReplica` | That replica's blob requests fail fast with `503` and `Retry-After`; it stays ready | Blob requests that hit it fail; clients' retries are balanced by the edge proxy and usually reach another replica | Automatic when its network path heals; otherwise the responder deletes the pod |
| S3 throttling (`503 SlowDown`) | SDK retryer | Retries with backoff (R-STOR-033) | Higher latency | The content-addressed key layout spreads load across prefixes (R-HA-050) |
| S3 credentials fail (expired web identity, revoked key) | `AccessDenied`, refresh error | Counted as object-store failure; logged with the credential source, never the secret | As the outage rows | Fix the credential; static key files are re-read (R-STOR-031) |
| Spool volume full | Spool budget (R-STOR-004) | New uploads `503` with `Retry-After`; uploads larger than the whole budget `413` | Retry succeeds when space frees | R-HA-035 sizing |
| Migration Job fails | Helm sees the hook Job fail | Old pods keep running on the unchanged or expanded schema; new pods are never created (pre-upgrade runs before the new manifests are applied) | None | Fix the cause, re-run `helm upgrade` (R-HA-019) |
| Encryption key set differs on a new replica | Startup check (R-HA-027) | The new replica refuses to start | None (old replicas keep serving; the rollout stalls) | Fix the Secret |
| OIDC provider unreachable | Login fails | Browser logins fail; sessions, PATs, service-account and CI tokens keep working, because trove validates its own tokens (INV-8) | Only new browser logins fail | Automatic |
| GitLab JWKS unreachable | Exchange fails or uses cached keys | Per [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | CI token exchange may fail | Automatic |
| Upstream registry unreachable | Upstream circuit | Stale metadata is served (INV-11, [repositories-and-proxy.md](repositories-and-proxy.md)) | Cached content keeps working | Automatic |
| Edge proxy pod loss | Platform | Not trove's; the controller must run highly available | Depends on the controller | Platform |

Acceptance criteria: every row is a scenario of R-HA-052 with the stated client effect as its pass
condition.

### R-HA-008 · Readiness and liveness under dependency failure
Priority: **MVP** · Covers: HA-023

R-OPS-001 defines the probe endpoints and the check list. This requirement fixes the HA rules that
the check thresholds and the chart's probe settings (R-HA-034) must satisfy together.

- Liveness MUST NOT depend on PostgreSQL, object storage, the OIDC provider or any upstream. A
  dependency outage must never restart replicas, because restarts do not repair a shared
  dependency and they discard warm caches and in-flight work. The chart's liveness probe uses
  `/healthz` only.
- Readiness MUST become false within about 15 s (`health.check_interval` × `health.failure_threshold`
  + one probe period) when the replica cannot reach PostgreSQL, and MUST become true again without
  a restart.
- Readiness and the retry budget interact. During a PostgreSQL switchover shorter than the
  readiness window, replicas stay ready and requests ride out the switchover in their retry loop
  (R-HA-009). A longer outage removes all replicas from the Service, so the edge proxy answers
  `503` at once instead of letting requests queue.
- Object storage gates readiness only until a replica is ready for the first time (Q-HA-1). A
  replica becomes ready only after its object-store startup check (R-STOR-035,
  `storage.s3.startup_check: true`) has passed, so a release with a wrong bucket, endpoint,
  credential or CA never replaces working pods: the rollout stalls with `maxUnavailable: 0`.
  After that, the `objectStore` check keeps running and is reported (verbose `/readyz`, the
  replica registry, metrics), but its failure MUST NOT make the replica unready. Blob requests
  degrade one by one through the circuit breaker (R-STOR-033, R-STOR-036), and the two alerts of
  R-HA-045 tell a shared outage from a single replica's problem.
- `health.require_object_store` defaults to `false` and implements this rule. `true` makes the
  `objectStore` check gate readiness continuously, debounced by R-OPS-001's
  `health.failure_threshold` and `health.success_threshold`. It is meant for an installation behind
  an outer load balancer that can fail over to a second installation. The chart passes
  `config.health.require_object_store` through unchanged.
- A replica whose schema is behind its `requiredSchema` (R-HA-018) stays not ready and logs the
  required schema version once per minute. It does not exit, so the rollout stalls visibly with
  `maxUnavailable: 0` (R-HA-020) instead of crash-looping, and the replica becomes ready as soon as
  the migration has run. A binary older than `compat_floor` can never serve against that schema and
  exits instead ([architecture.md](architecture.md) R-ARCH-026).
- Because a PostgreSQL outage (and, with `health.require_object_store: true`, an object-storage
  outage) makes every replica unready at once, the PodDisruptionBudget uses
  `unhealthyPodEvictionPolicy: AlwaysAllow` (R-HA-032). Otherwise a node drain during the outage
  would be blocked, since the default policy treats unready pods as already disrupted
  ([PDB unhealthy pod eviction policy](https://kubernetes.io/docs/tasks/run-application/configure-pdb/#unhealthy-pod-eviction-policy),
  stable since Kubernetes 1.31).

Acceptance criteria:

- With a NetworkPolicy that denies egress to PostgreSQL applied to all pods for 3 minutes, no
  container restarts (`kubectl get pods` restart count unchanged), every `/readyz` answers `503`,
  and all pods return to ready within 15 s after removal.
- With that policy applied, `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data`
  evicts the trove pod on that node instead of waiting.

### R-HA-009 · PostgreSQL failover: reconnect and retry
Priority: **MVP** · Covers: HA-064

The pgx driver ([pgconn](https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn),
[pgxpool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)) does not retry statements itself.
trove MUST implement the following.

**Finding the primary.**

- `database.host` MAY list several hosts separated by commas (libpq style; pgx tries them in
  order). With `database.target_session_attrs: read-write` (default) a connection is accepted only by
  a server that accepts writes, so a demoted former primary is skipped.
- With CloudNativePG the single host is the cluster's read-write Service
  ([service management](https://cloudnative-pg.io/docs/1.30/service_management)), which always
  points at the current primary. With Amazon RDS it is the instance or cluster endpoint whose DNS
  record moves on failover. Go's resolver does not cache DNS answers, so each new connection
  resolves the name again.

**Detecting dead connections.**

- The dialer MUST enable TCP keepalive (`database.keepalive.idle`, `interval`, `count`) and, on
  Linux, `TCP_USER_TIMEOUT` (`database.keepalive.user_timeout`), so a connection to a vanished
  server (node loss, no RST) fails within about 30 s instead of the operating system default of
  hours.
- Every statement runs with a context deadline (request deadline or `database.session.statement_timeout`).

**Retry classification.** A whole transaction is the unit of retry. Transaction bodies MUST be
written as functions that can run again from the start: no side effects outside PostgreSQL inside
the function. S3 writes happen before the transaction and are idempotent by content address
(R-STOR-005).

| Error | Retry? | Extra action |
|---|---|---|
| Error before anything was sent (`pgconn.SafeToRetry`) | yes | — |
| Connection failure while acquiring or dialing, SQLSTATE class `08` | yes | — |
| `57P01` admin_shutdown, `57P02` crash_shutdown, `57P03` cannot_connect_now | yes | Discard the connection |
| `25006` read_only_sql_transaction | yes | Discard all pooled connections: the pool reached a demoted primary |
| `40001` serialization_failure, `40P01` deadlock_detected | yes | — |
| `53300` too_many_connections | yes, with the backoff | Metric |
| Connection lost after `COMMIT` was sent | no (outcome unknown) | Answer `503` with `Retry-After`; see below |
| Anything else | no | — |

- Backoff: exponential from `database.retry.initial_backoff` to `database.retry.max_backoff` with
  full jitter, bounded by `database.retry.max_elapsed` and by the request's own deadline.
- **Unknown commit outcome.** When the connection drops after `COMMIT` was sent, the transaction
  may or may not have committed. trove MUST NOT retry it blindly. Writes that a client may
  repeat MUST be idempotent at the protocol level: re-uploading identical content to the same path
  is a no-op success (R-STOR-005, overwrite rule). So the client's own retry after the `503` is
  safe. Format pages decide how a repeated publish of an existing version answers when the content
  is identical.
- Background jobs retry through their own attempt counter (R-HA-005) in addition to the per
  transaction retry.

**After a failover.**

- The `LISTEN` session reconnects with backoff and flushes caches (R-HA-004).
- Singleton leases are lost with their sessions. The work aborts and resumes from its cursor on the
  next scheduled run (R-HA-006).
- The pool refills on demand. `database.pool.min_conns` connections are re-established in the
  background.

Configuration:

```yaml
database:
  target_session_attrs: read-write # default (proposed): read-write | primary | any
  retry:
    initial_backoff: 100ms         # default (proposed)
    max_backoff: 2s                # default (proposed)
    max_elapsed: 30s               # default (proposed); request deadlines may cut it shorter
  keepalive:
    idle: 15s                      # default (proposed)
    interval: 5s                   # default (proposed)
    count: 3                       # default (proposed)
    user_timeout: 30s              # default (proposed); Linux TCP_USER_TIMEOUT
```

Metrics (R-OPS-007 conventions): `trove_db_retries_total{kind}` with `kind` in `connection`,
`shutdown`, `read_only`, `serialization`, `too_many_connections`;
`trove_db_pool_resets_total`; `trove_db_commit_unknown_total`.

Acceptance criteria:

- Two replicas, CloudNativePG with two instances. While a loop runs `npm ci` (npm's default
  `fetch-retries`) and `dotnet restore --no-cache` against trove, `kubectl cnpg promote trove-db 2`
  ([kubectl plugin](https://cloudnative-pg.io/docs/1.30/kubectl-plugin)) completes. No client
  iteration fails, and `trove_db_retries_total` increases.
- `kubectl delete pod trove-db-1 --grace-period=0 --force` (unplanned failover of the primary):
  at most the requests in flight during the first `retry.maxElapsed` fail, all with `503` and
  `Retry-After`; the next loop iteration succeeds.
- A unit test with an injected connection drop after `COMMIT` returns `503` and does not run the
  transaction function a second time.

### R-HA-010 · Object-storage outage
Priority: **MVP** · Covers: HA-004

[storage.md](storage.md) R-STOR-036 is the normative table for every operation during an
object-storage outage, and R-STOR-033 specifies the per-replica circuit breaker. On top of that,
this page requires the following:

- An upload rejected because the circuit is open MUST be rejected *before* the body is read
  (`503` with `Retry-After`), so clients that send `Expect: 100-continue` (`dotnet nuget push`,
  `curl -T`) do not transfer the body at all.
- `Retry-After` on these `503` responses is `storage.s3.circuit_breaker.open_duration` rounded up to
  whole seconds, at least 5.
- The circuit state is a per-replica gauge (`trove_storage_s3_circuit_open`). The chart's alert
  rules distinguish a shared outage (all replicas open) from a single replica's network problem
  (one replica open), because the two need different responders (R-HA-045).
- While the circuit is open, garbage collection and the orphan sweep pause their S3 phase. This is
  already required by R-STOR-036, and it means an outage never turns into lost blobs.

Acceptance criteria (in addition to R-STOR-036's):

- With the local profile's S3 store scaled to 0 for 5 minutes during an `npm ci` loop against a
  virtual repository whose tarballs are cached: `npm view <pkg> versions` keeps succeeding for the
  whole period, every replica's `/readyz` keeps answering `200`, while
  `curl -T big.bin https://trove.example.com/files/generic-local/x.bin -H 'Expect: 100-continue'`
  receives `503` without sending the body. Within `storage.s3.circuit_breaker.open_duration`
  plus 10 s of the store returning, both work.
- A pod deleted during that outage is replaced, and the replacement stays not ready until the store
  returns. Its container restart count stays unchanged throughout, because the startup check
  retries transient errors without exiting (R-STOR-035), and it becomes ready within 60 s after the
  store returns.
- The alert `TroveObjectStoreUnreachable` fires during the outage, and
  `TroveObjectStoreUnreachableSingleReplica` does not.

### R-HA-011 · Graceful termination and connection draining
Priority: **MVP** · Covers: HA-047

On `SIGTERM` a replica follows R-OPS-001: it fails readiness at once, keeps serving for
`health.shutdown_drain_delay` so every proxy notices the endpoint removal, then stops accepting
connections and waits for in-flight requests. This page fixes the time budget and what happens to
background work.

- The chart sets `terminationGracePeriodSeconds` (default 120 s for API pods, 300 s for worker pods
  with the split). The server shutdown timeout ([architecture.md](architecture.md)) MUST be derived
  from it: `server.shutdown_timeout = terminationGracePeriodSeconds − health.shutdown_drain_delay − 5 s`.
  The chart passes the grace period to the process as `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD`, so
  the two cannot drift apart. The process exits before the kubelet's `SIGKILL`.
- No `preStop` hook is needed or rendered. The delay is implemented in the process, because the
  image contains no shell or `sleep` binary.
- Responses completed during the drain carry `Connection: close`, so clients reconnect through the
  Service to a remaining replica.
- Workers stop claiming jobs at `SIGTERM`. Running jobs get the shutdown timeout to finish or to
  persist their cursor. At the deadline their job leases are released explicitly
  (`state = 'queued'`, `attempt` unchanged), and their advisory-lock sessions are closed.
- The `LISTEN` session and the pools are closed last.
- A transfer that outlasts the grace period is cut. The client sees a connection reset and must
  retry. Operators size the grace period to their p99 upload duration (R-HA-050).

Acceptance criteria:

- `kubectl rollout restart deploy/trove` during a loop of `curl -T 500mb.bin …/files/generic-local/<n>.bin`
  over a link limited to 50 MB/s: no upload fails.
- `kubectl delete pod <worker>` during a test job that persists its cursor every second: the job
  continues on another replica from the persisted cursor with `attempt` unchanged.
- The pod's termination takes less than `terminationGracePeriodSeconds` (no `SIGKILL` in the
  kubelet events).

### R-HA-012 · Crash and node-loss recovery
Priority: **MVP** · Covers: HA-005

A crash never loses acknowledged data, because a `201` means durable in S3 and committed in
PostgreSQL (R-STOR-004). What a crash leaves behind, and what removes it:

| Leftover | Removed by |
|---|---|
| Spool files (`emptyDir` survives container restarts within the same pod) | Deleted at process start (R-STOR-004, step 6) |
| Objects written to S3 whose metadata commit never happened | Orphan sweep after `storage.orphans.min_age` (R-STOR-026) |
| Incomplete multipart uploads | Orphan sweep (`storage.orphans.multipart_max_age`), plus a bucket lifecycle rule `AbortIncompleteMultipartUpload` after 1 day as a second line ([lifecycle rule](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html)) |
| Jobs in `running` with a dead owner | Lease expiry and the reaper (R-HA-005) |
| Advisory locks of the dead process | Released when its session ends; on node loss after the server's TCP keepalive, which trove sets per session (R-HA-013, R-HA-015) |
| A `replica` row with a stale heartbeat | Shown as `lost`, pruned after 24 h (R-OPS-003) |
| Remote-fetch collapse locks | Released with the session (R-REPO-026) |

- The node-local volume of the catalog (HA-005: persistent per-node storage for data in transit)
  does not exist in trove. Uploads are never acknowledged before they reach S3, and there is no
  asynchronous upload queue, so the spool can be ephemeral. This removes the catalog's
  PersistentVolumeClaim-per-pod problems (manual cleanup after scale-down, claims reattached after
  reinstall).

Acceptance criteria:

- `kubectl exec <pod> -- kill -9 1` during a 2 GiB upload: the client gets an error, no item row
  exists, and after a back-dated orphan sweep (`minAge: 0s` in the test profile) no object and no
  multipart upload remains under the prefix
  (`aws s3api list-multipart-uploads --bucket trove-blobs --prefix trove/` is empty).
- After the restart, the spool directory of that pod is empty.

## PostgreSQL

### R-HA-013 · Supported PostgreSQL versions and server settings
Priority: **MVP** · Covers: HA-058, HA-003

**Versions.** trove MUST support PostgreSQL 16, 17 and 18 and MUST refuse to start against an
older server (`server_version_num < 160000`) with a message naming the version found. The floor is
16 (Q-HA-2). Support dates verified 2026-10-28 on the
[PostgreSQL versioning page](https://www.postgresql.org/support/versioning/): 14 ends 2026-11-12,
15 on 2027-11-11, 16 on 2028-11-09, 17 on 2029-11-08, 18 on 2030-11-14.

- A new major version joins the support list once it is generally available and the CI matrix
  passes on it. PostgreSQL 19 is at release candidate stage; its general availability is planned
  for 2026-11-19 ([RC 1 announcement](https://www.postgresql.org/about/news/postgresql-19-rc-1-released-3409/),
  verified 2026-10-28).
- The floor moves only with the community support window: the first trove minor release published
  after a major version's end of life MAY drop that version, and the release notes of the minor
  release before it MUST announce the drop. A schema or query feature that needs a newer version
  than the floor is not used until the floor reaches it.

trove uses only the primary. Read replicas are not used for queries, because every node must see
one consistent view (HA-003) and replica lag would show stale permissions and stale package lists.
Routing reads to replicas is Deferred.

**Server settings.**

| Setting | Requirement | Reason |
|---|---|---|
| `server_encoding` | `UTF8` (checked at startup) | Package names and metadata are Unicode |
| `max_connections` | ≥ the budget of R-HA-016 (checked by the migration Job) | Scaling out without it exhausts connections |
| `tcp_keepalives_idle`, `tcp_keepalives_interval`, `tcp_keepalives_count` | Recommended `60`, `10`, `6` | The server ends the session of a vanished replica, and releases its advisory locks, within about 2 minutes instead of the OS default ([connection settings](https://www.postgresql.org/docs/current/runtime-config-connection.html)). trove sends the same values as startup parameters on every session it opens, the lock-holding ones included (R-HA-015), so the advisory-lock bound holds without this server setting; the server setting still covers other clients |
| `client_connection_check_interval` | Recommended `10s` | Detects a vanished client during a long query |
| WAL archiving | Required for point-in-time recovery (R-HA-023) | Restore to a point in time |
| Synchronous replication | Recommended, with one synchronous standby | Without it, a failover can lose transactions that were acknowledged as `201`. With CloudNativePG: `postgresql.synchronous: {method: any, number: 1, dataDurability: required}` ([replication](https://cloudnative-pg.io/docs/1.30/replication)); `required` pauses writes when no standby is available, `preferred` keeps writing and risks that loss |
| `default_tablespace` | Empty or a tablespace created for the purpose | Partitioned tables cannot be created in `pg_global` |
| Extensions | None required | Any page that needs one MUST add it to this table |
| `idle_in_transaction_session_timeout`, `statement_timeout`, `lock_timeout` | Not required at server level; trove sets them per session (R-HA-015) | Works on managed services without parameter-group changes |

- trove sets `TimeZone=UTC` and its `search_path` per session and stores time as `timestamptz`, so
  the server's time zone setting does not matter.
- Text columns that carry indexed keys (repository keys, package names, paths) are declared with
  `COLLATE "C"`. Byte-wise ordering is stable, so an operating-system collation update on the
  database server cannot corrupt these indexes. Case-insensitive matching (NuGet IDs) uses
  normalized columns computed in Go, not database collations.
- **Network locality (HA-008).** The database MUST be in the same region as the replicas. Each
  request makes several sequential round trips, so the round-trip time to the primary adds
  directly to request latency. Below 2 ms is recommended. Cross-region primaries are not
  supported. Multi-site setups use a disaster-recovery replica instead (R-HA-026).

Configuration (CloudNativePG example):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: trove-db                         # example
spec:
  instances: 3                             # example: primary + 2 standbys
  postgresql:
    parameters:
      max_connections: "300"               # example; derive from R-HA-016
      tcp_keepalives_idle: "60"            # default (proposed)
      tcp_keepalives_interval: "10"        # default (proposed)
      tcp_keepalives_count: "6"            # default (proposed)
      client_connection_check_interval: "10s"  # default (proposed)
    synchronous:
      method: any                          # default (proposed)
      number: 1                            # default (proposed)
      dataDurability: required             # default (proposed)
  bootstrap:
    initdb:
      database: trove                    # example
      owner: trove_owner                 # example
  storage:
    size: 50Gi                             # example; see R-HA-050
```

Acceptance criteria:

- Starting against PostgreSQL 15 exits with status 1 and the log line names `15.x` and the minimum
  `16`.
- The CI matrix runs the integration suite against every supported major version (16, 17 and 18;
  19 once it is added).
- `SHOW tcp_keepalives_idle` on the test cluster returns `60`; R-HA-006's partition test passes.

### R-HA-014 · Database provisioning, roles and privileges
Priority: **MVP** · Covers: HA-060

trove MUST NOT need superuser rights, and MUST NOT create databases or roles. Two credential
modes exist:

| Mode | Roles | Used when |
|---|---|---|
| Two-role (recommended for production) | `trove_owner` owns the schema and runs migrations; runtime logins are members of `trove_app`, which holds only DML grants | `secrets.databaseMigration.existingSecret` is set |
| Single-role | One login owns the schema and also serves requests | Development, or a managed database that provides one role (the CloudNativePG `app` owner) |

In single-role mode a replica logs a warning at startup: the runtime role can change the schema,
so a SQL injection would have DDL rights.

Provisioning, run once by a database administrator (PostgreSQL 16+):

```sql
CREATE ROLE trove_owner LOGIN PASSWORD :'owner_password';   -- migration Job only
CREATE ROLE trove_app NOLOGIN;                              -- grant holder
CREATE ROLE trove_app_a LOGIN PASSWORD :'app_password' IN ROLE trove_app;
CREATE ROLE trove_app_b LOGIN IN ROLE trove_app;          -- rotation partner (R-HA-017), no password yet
CREATE DATABASE trove OWNER trove_owner ENCODING 'UTF8' TEMPLATE template0;
\connect trove
REVOKE ALL ON DATABASE trove FROM PUBLIC;
GRANT CONNECT ON DATABASE trove TO trove_app;
CREATE SCHEMA trove AUTHORIZATION trove_owner;
GRANT USAGE ON SCHEMA trove TO trove_app;
```

The first migration (run as `trove_owner`) sets the default privileges, so every later table is
reachable for the runtime role without further grants
([ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html)):

```sql
ALTER DEFAULT PRIVILEGES IN SCHEMA trove
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO trove_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA trove
  GRANT USAGE, SELECT ON SEQUENCES TO trove_app;
```

**Audit objects are the exception** ([operations-and-observability.md](operations-and-observability.md)
R-OPS-015, Q-OPS-4). Audit rows are append-only for the runtime role, and partition maintenance
and retention belong to the owner. The migration that creates the audit objects (run as
`trove_owner`) therefore narrows the defaults above:

```sql
-- audit_event: INSERT and SELECT only
REVOKE UPDATE, DELETE, TRUNCATE ON audit_event FROM trove_app;
-- audit_policy: owner-written retention, readable but not writable by the runtime role
CREATE TABLE audit_policy (…);                        -- columns: R-OPS-015
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON audit_policy FROM trove_app;
-- the only path to partition DDL
CREATE FUNCTION audit_maintain_partitions() RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = trove, pg_temp
  AS $$ … $$;                                         -- body: R-OPS-015
REVOKE ALL ON FUNCTION audit_maintain_partitions() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_maintain_partitions() TO trove_app;
```

- Partitions of `audit_event` carry no grant for `trove_app`; the runtime role reaches them only
  through the parent table. `audit_maintain_partitions()` runs as the owner, so the default
  privileges above would grant DML on every partition it creates. The function therefore revokes
  them (`REVOKE ALL ON <partition> FROM trove_app`) on every partition it creates.
- The migration Job reads `audit.retention_days` from the same configuration file as the replicas
  (R-HA-019, R-HA-030). After applying the steps, `trove migrate` writes it into `audit_policy` and
  then calls `audit_maintain_partitions()`. Retention is therefore owner data that a compromised
  replica cannot shorten, and the partitions exist before the first replica of the release writes
  (R-OPS-015, R-OPS-017).
- In single-role mode the runtime role owns these objects, so none of this applies there (R-OPS-015,
  H-39).
- The runtime role needs no privilege for advisory locks, `LISTEN` or `NOTIFY`.
- Preflight, run by the migration Job and by every replica at startup, MUST check:
  - the server version (R-HA-013);
  - `server_encoding = 'UTF8'`;
  - that the runtime role is not a superuser (`rolsuper`); a superuser runtime role is refused
    unless `database.preflight.allow_superuser: true`;
  - that schema `trove` exists and is owned by the migration role;
  - after migrations, that the runtime role holds exactly the expected privileges
    (`has_table_privilege`, `has_function_privilege`):
    - `SELECT`, `INSERT`, `UPDATE`, `DELETE` on every table of the schema except the audit
      objects below (the defaults above);
    - `SELECT` and `INSERT` on `audit_event`, and none of `UPDATE`, `DELETE`, `TRUNCATE`;
    - `SELECT` on `audit_policy`, and none of `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`;
    - no privilege on any partition of `audit_event`;
    - `EXECUTE` on `audit_maintain_partitions()`.

    A missing grant and a surplus grant on the audit objects both fail the check. In single-role
    mode the runtime role owns every object, so the check is skipped there.
- Managed services: on Amazon RDS the administrator account is not a true superuser, and the SQL
  above runs as that account unchanged. With CloudNativePG, `bootstrap.initdb.owner` creates the
  owner; the two runtime roles are added through the cluster's managed roles or with the SQL above.

Configuration:

```yaml
database:
  schema: trove               # default (proposed)
  preflight:
    allow_superuser: false      # default (proposed)
```

Acceptance criteria:

- With the two-role setup, `psql -U trove_app_a -c 'DROP TABLE trove.job'` fails with
  `42501` (insufficient privilege), and the whole integration suite passes.
- With the two-role setup, `psql -U trove_app_a -c 'DELETE FROM trove.audit_event'` fails with
  `42501`.
- After `GRANT UPDATE ON trove.audit_event TO trove_app` by hand, a replica's preflight fails and
  names `audit_event` and `UPDATE`.
- A runtime role with `SUPERUSER` makes the replica exit with a message naming
  `database.preflight.allow_superuser`.

### R-HA-015 · Connection settings and TLS
Priority: **MVP** · Covers: HA-061, HA-063

- Credentials come only from files mounted from Kubernetes Secrets (`database.username_file`,
  `database.password_file`; INV-15). The password file is read again for every new connection
  (pgx `BeforeConnect`), so a rotated Secret applies without a restart (R-HA-017). A connection
  URL with an inline password is rejected at startup.
- TLS: `database.ssl_mode` defaults to `verify-full`. `database.ssl_root_cert_file` names the CA
  certificate. Certificate authentication uses `database.ssl_cert_file` and `database.ssl_key_file`.
  `disable`, `allow` and `prefer` are rejected unless `database.allow_insecure: true`, which the
  local development profile does not need either. CloudNativePG serves TLS by default with its own
  CA ([certificates](https://cloudnative-pg.io/docs/1.30/certificates)).
- The CloudNativePG CA Secret contains the CA's private key (`ca.key`) next to `ca.crt`. The chart
  MUST project only the configured key (`ca.crt`) into the pod (R-HA-030), so the replica never
  holds a key that can mint server certificates.
- `application_name` is `trove/<pod name>/<pool>` with `<pool>` in `main`, `lease`, `lock`,
  `listen`, `migrate`, truncated to 63 bytes. This makes every session attributable in
  `pg_stat_activity`.
- Per-session settings, sent as startup parameters:

| Parameter | Value |
|---|---|
| `statement_timeout` | `database.session.statement_timeout` (migrations: 0) |
| `lock_timeout` | `database.session.lock_timeout` |
| `idle_in_transaction_session_timeout` | `database.session.idle_in_transaction_session_timeout` |
| `TimeZone` | `UTC` |
| `search_path` | `database.schema` |
| `tcp_keepalives_idle`, `tcp_keepalives_interval`, `tcp_keepalives_count` | `database.server_keepalive.idle`, `.interval` (whole seconds) and `.count` |

- The server-side keepalive parameters are user-settable (context `user`), so they need no
  privilege and no server configuration. They make the server end the session of a vanished
  replica, and release its advisory locks (R-HA-006, R-REPO-026), within
  `idle + interval × count` (about 2 minutes with the defaults), the bound R-HA-013 recommends for
  the server setting. They are sent on every session, which includes the listener (coordination)
  session, the lease pool and the fetch-collapse lock pool that hold session-level locks.

Configuration:

```yaml
database:
  host: trove-db-rw.trove.svc     # example; comma-separated list allowed (R-HA-009)
  port: 5432                          # default (proposed)
  name: trove                       # example
  username_file: /var/run/secrets/trove/database/username  # default (proposed); set by the chart
  password_file: /var/run/secrets/trove/database/password  # default (proposed); set by the chart
  ssl_mode: verify-full               # default (proposed): verify-full | verify-ca | require
  ssl_root_cert_file: /var/run/secrets/trove/database-ca/ca.crt  # example; set by the chart
  ssl_cert_file: ""                   # default (proposed)
  ssl_key_file: ""                    # default (proposed)
  allow_insecure: false               # default (proposed)
  connect_timeout: 5s                 # default (proposed)
  session:
    statement_timeout: 30s            # default (proposed)
    lock_timeout: 5s                  # default (proposed)
    idle_in_transaction_session_timeout: 60s  # default (proposed)
  server_keepalive:                   # sent as tcp_keepalives_* startup parameters
    idle: 60s                         # default (proposed)
    interval: 10s                     # default (proposed)
    count: 6                          # default (proposed)
```

Acceptance criteria:

- `SELECT application_name, ssl FROM pg_stat_activity JOIN pg_stat_ssl USING (pid) WHERE application_name LIKE 'trove/%'`
  shows `ssl = true` for every trove session.
- Against a server whose `tcp_keepalives_*` settings are left at `0` (operating-system default),
  R-HA-006's partition test passes, and the other replica takes the lease within 150 s of the
  partition.
- With `sslMode: verify-full` and a host name not in the server certificate, startup fails with a
  TLS verification error naming the host.
- `kubectl exec <pod> -- ls /var/run/secrets/trove/database-ca/` lists `ca.crt` only.

### R-HA-016 · Connection budget and pool sizing
Priority: **MVP** · Covers: HA-062

Every replica opens connections from four sources:

| Source | Key | Default (proposed) |
|---|---|---|
| Main pool | `database.pool.max_conns` | 20 |
| Lease pool (R-HA-006) | `database.lease_pool_size` | 4 |
| Fetch-collapse lock pool ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026) | `proxy.lock_pool_size` | 16 |
| Listener (R-HA-004) | — | 1 |

The PostgreSQL connection limit MUST satisfy:

```text
per_replica  = database.pool.max_conns + database.lease_pool_size + proxy.lock_pool_size + 1
replicas_max = Σ over Deployments (autoscaling.enabled ? maxReplicas : replicaCount)
               + Σ over Deployments rollingUpdate.maxSurge
budget       = replicas_max × per_replica
               + 2                                  (migration Job)
               + database.preflight.reserved_connections   (operators, backup tools, monitoring)
               + superuser_reserved_connections     (server setting, default 3)
max_connections ≥ budget
```

Example with defaults, two replicas, `maxSurge: 1`: 3 × (20 + 4 + 16 + 1) + 2 + 10 + 3 = **138**.
The PostgreSQL default of 100 is too small, so the chart's NOTES print the computed budget.

- The chart passes `replicas_max` to the migration Job (`TROVE_DATABASE_PREFLIGHT_EXPECTED_REPLICAS`).
  The Job compares the budget with `SHOW max_connections`. With
  `database.preflight.connection_budget: enforce` (default) a shortfall fails the Job, and with it
  `helm upgrade`, before the larger deployment exists. With `warn` it logs and continues.
- Sizing the main pool: database time per request is a few milliseconds, so 20 connections serve
  several thousand short transactions per second per replica. More connections per replica rarely
  help. The total number of *active* connections should stay near 2–4 × the database server's
  vCPUs. If `trove_db_pool_acquire_duration_seconds` shows waits while the database CPU is idle,
  raise `max_conns`. If the database CPU is saturated, scale the database, not the pool.
- Pool settings with jittered lifetimes spread reconnections over time, so a credential rotation or
  a failover does not cause a reconnect storm:

Configuration:

```yaml
database:
  pool:
    max_conns: 20                 # default (proposed)
    min_conns: 2                  # default (proposed)
    max_conn_lifetime: 30m        # default (proposed)
    max_conn_lifetime_jitter: 5m  # default (proposed)
    max_conn_idle_time: 5m        # default (proposed)
    health_check_period: 30s      # default (proposed)
  preflight:
    connection_budget: enforce    # default (proposed): enforce | warn
    reserved_connections: 10      # default (proposed)
```

Acceptance criteria:

- Against a server with `max_connections = 100` and the default values with three replicas,
  `helm upgrade --install` fails in the migration Job with a message that names 179 required and
  100 available.
- Under the reference load test (R-HA-050, medium profile),
  `max(trove_db_pool_connections{state="acquired"})` stays below `max_conns`, and
  `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove/%'` never exceeds
  the computed budget.

### R-HA-017 · Database credential rotation without downtime
Priority: **P1** · Covers: HA-065

PostgreSQL holds one password per role, so rotating a role's password in place opens a window
where new connections fail. trove rotates by alternating between the two runtime logins of
R-HA-014 (`trove_app_a`, `trove_app_b`), which share all grants through `trove_app`:

1. Set a new password on the inactive login: `ALTER ROLE trove_app_b PASSWORD '…'`.
2. Update the Kubernetes Secret named by `secrets.database.existingSecret` with both `username`
   and `password` of `trove_app_b` in one update. The kubelet replaces all keys of a Secret
   volume in one atomic swap, so a replica never reads a mismatched pair.
3. Within the kubelet sync period (about a minute) every new connection uses `trove_app_b`.
   Existing connections end within `max_conn_lifetime` + jitter. The listener and lease sessions are
   recycled on the same schedule (the listener without a gap, R-HA-004; lease sessions only between
   runs).
4. When `SELECT count(*) FROM pg_stat_activity WHERE usename = 'trove_app_a'` returns 0, disable
   the old login: `ALTER ROLE trove_app_a PASSWORD NULL`.

The next rotation runs the same steps in the opposite direction. The owner role used by the
migration Job is rotated between upgrades, because it is only used while the Job runs.

- Secret volumes MUST be mounted without `subPath` (R-HA-030); otherwise the kubelet never updates
  the files ([Secrets as files](https://kubernetes.io/docs/concepts/configuration/secret/#using-secrets-as-files-from-a-pod)).
- CloudNativePG's managed roles can automate steps 1 and 4.

Acceptance criteria:

- During an `npm ci` loop, steps 1–4 complete without a failed iteration and without a pod
  restart; afterwards `pg_stat_activity` shows only `trove_app_b` sessions.

## Upgrades and migrations

### R-HA-018 · Expand/contract schema migrations
Priority: **MVP** · Covers: HA-066, OPS-074

Release N and release N−1 run side by side against one schema during every rolling upgrade
(INV-3). Every migration step is classified, and the classes have rules:

| Class | Allowed changes | Applied |
|---|---|---|
| **expand** | New tables, new nullable columns or columns with a constant default, new indexes (built `CONCURRENTLY`), new constraints added `NOT VALID` and validated in a later step, new views, new functions | By the migration Job of the release that ships it, before any of its pods start |
| **contract** | Dropping tables, columns, indexes, constraints and functions; `SET NOT NULL`; renames (only as add-copy-drop over several releases, never `RENAME` of an object an older release reads) | Only when no live replica runs a release that still uses the object (guard below) |

- **Runner.** Steps are applied by trove's own runner on the native pgx API
  ([architecture.md](architecture.md) R-ARCH-026, Q-ARCH-3); no migration library and no
  `database/sql` are involved. A transactional step and its `schema_migration` row commit in one
  transaction, so a crash leaves neither.
- **File headers.** Each step is an embedded SQL file `internal/meta/postgres/migrations/NNNNN_<name>.sql`
  whose header comments declare what this requirement needs (R-ARCH-026 owns the syntax):

  | Header | Meaning |
  |---|---|
  | `-- trove:phase expand` or `-- trove:phase contract` | The step's class (table above); every file has exactly one |
  | `-- trove:no-transaction` | The step cannot run in a transaction block; the file holds exactly one statement |
  | `-- trove:requires-app >= <version>` | Contract steps only, and required there: the oldest release allowed to serve once the step is applied |

- Data changes are not migrations. Backfills run as background data migrations (R-HA-022), never
  inside the migration Job, so the Job's duration does not grow with the data.
- Every step runs with `lock_timeout` = `migrate.ddl_lock_timeout` and is retried up to
  `migrate.ddl_lock_retries` times with backoff. A DDL statement that waits for an exclusive lock
  blocks every later query on that table, so it must give up quickly rather than queue behind a
  long transaction.
- `CREATE INDEX CONCURRENTLY` cannot run in a transaction block
  ([CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)).
  Such steps carry `-- trove:no-transaction`, hold a single statement and are idempotent. Before
  retrying, the runner drops an `INVALID` index left by a failed attempt.
- `SET NOT NULL` on a large table goes through `CHECK (col IS NOT NULL) NOT VALID`,
  `VALIDATE CONSTRAINT`, then `SET NOT NULL`, which uses the validated check instead of a full scan
  under an exclusive lock.

**Schema compatibility window.**

- Each binary embeds `requiredSchema`, the number of its newest expand step.
- Each contract step declares `requires-app` in its header: the oldest release allowed to serve
  once the step is applied. The release before it is the step's `lastUsedBy`, the newest release
  whose code still reads or writes the object the step removes. `requires-app` is the declared
  value; `lastUsedBy` is only a name for the release before it.
- The database records `compat_floor` = the highest `requires-app` of all applied contract steps,
  or `0.0.0` while none is applied. No version arithmetic is involved: the floor is a declared
  version, compared as a semantic version.
- A binary of version V can serve iff `applied ≥ requiredSchema` and `compat_floor ≤ V`. Newer
  expand steps do not make an older binary incompatible. The window has no upper bound on the
  schema number, which keeps N−1 ready while N's expand steps are applied. The `schema` readiness
  check of R-OPS-001 MUST implement exactly this rule: below `requiredSchema` the replica waits not
  ready (R-HA-008), and with `compat_floor` above V it exits with
  `schema requires trove >= <compat_floor>` (R-ARCH-026).

**Contract guard.** Before applying a contract step, `trove migrate` reads the live replicas
(R-HA-003). If any of them runs a version below the step's `requires-app` (that is, ≤ its
`lastUsedBy`), `trove migrate` MUST exit with status `3` without applying anything, which fails the
Job. The error lists those replicas and the two ways forward: upgrade one minor release at a time,
or stop all replicas and upgrade with downtime (R-HA-021). There is no override flag. The downtime
path scales the replicas to zero, so the registry lists none and the guard passes; an override
while old replicas are live would let the step break them (R-ARCH-026, Q-ARCH-3).

```sql
CREATE TABLE schema_migration (
  version        integer     PRIMARY KEY,
  name           text        NOT NULL,             -- e.g. '00057_package_tags'
  class          text        NOT NULL CHECK (class IN ('expand','contract')),  -- trove:phase
  release        text        NOT NULL,             -- trove version that ships the step
  requires_app   text,                             -- trove:requires-app; contract only
  transactional  boolean     NOT NULL,             -- false with trove:no-transaction
  checksum       bytea       NOT NULL,             -- SHA-256 of the embedded SQL
  applied_at     timestamptz NOT NULL DEFAULT now(),
  duration_ms    integer     NOT NULL
);

CREATE TABLE schema_state (
  id             boolean     PRIMARY KEY DEFAULT true CHECK (id),
  applied        integer     NOT NULL,
  compat_floor   text        NOT NULL              -- highest applied requires_app; '0.0.0' if none
);
```

- A checksum mismatch between an applied step and the embedded SQL (an edited migration) fails the
  Job.
- There are no down migrations. Rolling back one release is supported without a database restore
  (R-HA-019).

API (permission: `system.read`, [operations-and-observability.md](operations-and-observability.md)):

`GET /api/v1/system/migrations` → `200`

```json
{
  "schema": {"applied": 57, "compatFloor": "0.3.0"},
  "binary": {"version": "0.4.0", "requiredSchema": 57},
  "pending": [],
  "applied": [
    {"version": 57, "name": "00057_package_tags", "class": "expand", "release": "0.4.0",
     "appliedAt": "2026-10-28T12:00:03.114Z", "durationMs": 412}
  ],
  "background": [
    {"name": "backfill_package_tags", "release": "0.4.0", "state": "running",
     "processed": 120000, "estimatedTotal": 480000, "startedAt": "2026-10-28T12:05:00.000Z"}
  ]
}
```

`401` without credentials, `403` without `system.read`.

Configuration (`trove migrate`):

```yaml
migrate:
  advisory_lock_wait: 5m     # default (proposed): how long to wait for another migrator
  ddl_lock_timeout: 5s       # default (proposed)
  ddl_lock_retries: 20       # default (proposed)
```

Acceptance criteria:

- A CI check fails a change that adds a contract-class statement (`DROP`, `RENAME`, `SET NOT NULL`)
  to a file headed `-- trove:phase expand`, a file without a valid `phase` header, and a contract
  file without `-- trove:requires-app` (R-ARCH-026).
- Integration test: the previous release's binary passes its full API suite against a schema with
  the current release's expand steps applied.
- With a 0.3.x replica alive, a migration plan that contains a contract step with
  `-- trove:requires-app >= 0.4.0` makes `trove migrate` exit with status `3`, apply nothing and
  name that replica.
- After that step is applied with no older replica alive, `GET /api/v1/system/migrations` reports
  `compatFloor: "0.4.0"`.
- A migration whose DDL waits behind a transaction held open for 2 minutes does not block reads on
  the table for more than `ddl_lock_timeout` per attempt (measured with a concurrent `SELECT` loop).

### R-HA-019 · Migration Job
Priority: **MVP** · Covers: HA-066, HA-069

- With `migrations.mode: hook` (default) the chart renders a Job annotated
  `helm.sh/hook: pre-install,pre-upgrade`, `helm.sh/hook-weight: "0"` and
  `helm.sh/hook-delete-policy: before-hook-creation`
  ([chart hooks](https://helm.sh/docs/topics/charts_hooks/)). Helm waits for the Job to complete
  before it applies the release's other manifests. A failed Job fails the release, and the
  previous Deployment keeps running unchanged.
- Hooks run before the release's ordinary resources exist on a first install. The Job therefore
  MUST NOT depend on chart-rendered ordinary resources. Its configuration ConfigMap and its
  ServiceAccount are rendered as hook resources with weight `-10`. Secrets are pre-existing
  (R-HA-030).
- The hook ConfigMap carries the same `trove.yaml` as `<fullname>-config` (R-HA-030), so
  `trove migrate` reads `audit.retention_days` and the `database` and `migrate` sections from the
  same configuration file as the replicas.
- The Job runs `trove migrate up`, trove's own runner on native pgx (R-ARCH-026), which does the
  following in order:
  1. preflight (R-HA-013, R-HA-014, R-HA-016);
  2. `pg_advisory_lock(3, 0)` (category `migration`, R-ARCH-017), waiting up to
     `migrate.advisory_lock_wait`, so concurrent Jobs serialize and the second finds nothing to do.
     Every later step runs on that same connection;
  3. verify the checksums of applied steps;
  4. compute the plan, run the contract guard (exit status `3` when it refuses, R-HA-018), apply
     the steps, update `schema_state`;
  5. write `audit.retention_days` into `audit_policy`, then call `audit_maintain_partitions()`
     (R-HA-014, [operations-and-observability.md](operations-and-observability.md) R-OPS-015);
  6. release the lock and print a JSON summary to stdout;
  7. write an audit event `system.schema.migrated` with the applied versions.
- The Job uses the migration credentials (`secrets.databaseMigration`), or the runtime credentials
  in single-role mode. It mounts no S3 credential and no service-account token, and it has the same
  pod and container security context as the replicas.
- Job settings: `backoffLimit` 2 (steps are idempotent), `activeDeadlineSeconds` 1800 and
  `ttlSecondsAfterFinished` 86400, so the logs stay readable for a day.
- Helm's default `--timeout` (5 m) is shorter than the Job deadline. The documented command is
  `helm upgrade --install … --wait --timeout 30m`.
- GitOps: Argo CD maps both `pre-install` and `pre-upgrade` to its `PreSync` phase and honors
  `before-hook-creation` ([Argo CD Helm hooks](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/)).
  The Job then runs on every sync, which is harmless because it is idempotent and finds nothing to
  do.
- `migrations.mode: disabled` renders no Job. The operator runs `trove migrate up` by other
  means, for example `kubectl -n trove run trove-migrate --rm -i --restart=Never --image=… -- trove migrate up`
  with the same mounts. New pods stay not ready until the schema is sufficient (R-HA-008).
- **Rollback.** `helm rollback` to release N−1 after an upgrade to N is supported. N's Job applied
  only expand steps that N−1 tolerates, and contract steps whose objects N−1 no longer uses, which
  declare `requires-app` N−1 or lower (R-HA-021). `compat_floor` is therefore ≤ N−1. Rolling back
  further than one release requires a database restore.

Acceptance criteria:

- `helm install` into an empty namespace (with the pre-created Secrets) succeeds on the first try.
  The hook ConfigMap and ServiceAccount exist before the Job pod starts.
- Two `helm upgrade` commands started at the same time: one Job applies the steps, the other logs
  that it waited for the lock and applied nothing; both succeed.
- A migration made to fail on purpose leaves the old pods serving (no failed `npm ci` iteration),
  and `helm history trove` shows the release as failed.
- `helm rollback trove <previous revision> --wait` after an upgrade with expand steps brings back
  the previous version, and its readiness passes.

### R-HA-020 · Rolling upgrade and mixed-version operation
Priority: **MVP** · Covers: HA-069, OPS-073

- Deployments use `RollingUpdate` with `maxSurge: 1` and `maxUnavailable: 0`, plus
  `minReadySeconds: 10`. Serving capacity never drops below the replica count during an upgrade,
  so two replicas suffice for a zero-downtime upgrade. The catalog's recommendation of three nodes
  comes from upgrades that take a node out of service.
- During the mixed-version window, release N and N−1 serve the same clients alternately. Therefore:
  - Anything a client carries between requests MUST be understood by both releases: pagination
    cursors, `ETag` values, session cookies, CSRF tokens, upload session identifiers.
  - Job payloads and `NOTIFY` payloads are versioned. Older workers skip unknown job kinds
    (R-HA-005), and older listeners flush on unknown payloads (R-HA-004).
  - A feature that writes data N−1 would misinterpret MUST stay inactive until the cluster minimum
    version (R-HA-003) is ≥ N. Until then the API answers requests to that feature with `503`
    `cluster_upgrade_in_progress`.
- The rollout pauses on its own when a new pod does not become ready (incompatible schema,
  configuration error, failed key check), because `maxUnavailable: 0` keeps all old pods.
  `kubectl rollout undo` or `helm rollback` returns to the previous release.
- Upgrade verification:
  - `kubectl rollout status deploy/trove`;
  - `GET /api/v1/system/replicas` lists only the new version;
  - `GET /api/v1/system/migrations` shows no pending step and the expected background migrations.

Acceptance criteria:

- Upgrade from the previous minor release to the current one with `helm upgrade --wait` while
  `npm ci` and `dotnet restore` loops run against both a remote and a local repository: no
  iteration fails.
- A test feature gated on the cluster minimum version answers `503 cluster_upgrade_in_progress`
  while a 0.3.x replica is alive, and works after it is gone.

### R-HA-021 · Upgrade policy and preparation
Priority: **MVP** · Covers: OPS-072, HA-070

| Upgrade | Supported path |
|---|---|
| Patch (x.y.z → x.y.z+1) | Rolling, zero downtime; patch releases contain no contract steps |
| Next minor (x.y → x.y+1) | Rolling, zero downtime (R-HA-020) |
| Skipping minors (x.y → x.y+2 or later) | Stepwise through each minor with zero downtime, or in one step with downtime: scale to zero, wait `system.heartbeat_stale_after`, then `helm upgrade`. The contract guard enforces this (R-HA-018, Q-HA-5) |
| Major | As stated in the release notes; skipping a major is not supported |

- The Helm chart is the only supported installation and upgrade method (INV-13). The chart version
  and the application version are released together. `appVersion` is the default image tag.
- The compatibility window is one minor release (Q-HA-5). A contract step shipped in minor release
  N may remove only objects that N−1 no longer uses, so its header declares
  `-- trove:requires-app >= N−1` or lower ([architecture.md](architecture.md) R-ARCH-026), and its
  `lastUsedBy`, the release before, is N−2 or older. This is R-ARCH-026's rule that a contract step
  ships at the earliest one minor after the release that stopped using the old objects. Every
  release's notes name the oldest release it can be upgraded from without downtime and, when
  contract steps are included, the stepwise path from older minors. The contract guard's error
  names the same path.
- Preparation:
  1. Read the release notes for breaking changes and new required values.
  2. Confirm that the PostgreSQL backup is current, and note the time as a restore point
     (R-HA-023).
  3. Confirm that the encryption key Secret contains all keys listed in
     `secrets.encryption.keys` (R-HA-027).
  4. Check `GET /api/v1/system/migrations`: a background migration that a contract step of the
     target release depends on must be `succeeded`. The guard also checks this.
  5. Run `helm upgrade --install … --dry-run=server` (or `helm diff`), so fail-fast validation
     (R-HA-029) runs before anything changes.
- Unlike the catalog's chart, there are no cluster keys to read back from a running node, and no
  bundled database to pin: the chart generates no values (R-HA-029).

Acceptance criteria:

- Upgrading from 0.2.x directly to 0.4.x with a contract step headed
  `-- trove:requires-app >= 0.3.0` fails in the Job (`trove migrate` exits with status `3`) with
  the message of R-HA-018. After `kubectl scale deploy/trove --replicas=0`, a 30 s wait and
  `helm upgrade`, it succeeds without any override.

### R-HA-022 · Background data migrations
Priority: **MVP** · Covers: OPS-074

- A data change too large for the migration Job (backfills, re-hashing, re-indexing metadata) is a
  named background migration. It runs as a job of kind `data_migration_<name>` on the job queue
  ([architecture.md](architecture.md) R-ARCH-016, R-ARCH-026), as a singleton under a lease
  (R-HA-006), in keyset batches of `jobs.background_migrations.batch_size` rows, with
  `pause_between_batches` between them. It persists its cursor after every batch, so an
  interruption resumes where it stopped.
- Its runs are visible in the job API (`GET /api/v1/system/jobs?kind=data_migration_<name>`).
  `background_migration` below keeps the durable state that the contract guard and
  `GET /api/v1/system/migrations` read, also after the job rows are deleted (`jobs.retention`).
- During a rolling upgrade, an N−1 worker never claims a `data_migration_<name>` kind introduced
  in N, because workers claim only the kinds they know (R-HA-005).
- A background migration starts only when the cluster minimum version (R-HA-003) is ≥ the release
  that introduced it. Otherwise an older replica could write rows in the old shape behind the
  backfill.
- While a background migration runs, code that depends on its result MUST handle unconverted rows
  (dual read). A contract step that depends on its completion declares it, and the guard
  (R-HA-018) refuses to run that step before the migration has `succeeded`.
- `POST /api/v1/system/migrations/background/{name}/pause` and `…/resume` → `202`; `409` when the
  migration is not in a state that allows the transition. Permission: system administrator.

```sql
CREATE TABLE background_migration (
  name             text        PRIMARY KEY,
  release          text        NOT NULL,
  state            text        NOT NULL CHECK (state IN ('pending','running','paused','succeeded','failed')),
  cursor           jsonb,
  processed        bigint      NOT NULL DEFAULT 0,
  estimated_total  bigint,
  started_at       timestamptz,
  finished_at      timestamptz,
  last_error       text,
  updated_at       timestamptz NOT NULL DEFAULT now()
);
```

Configuration:

```yaml
jobs:
  background_migrations:
    batch_size: 1000             # default (proposed)
    pause_between_batches: 100ms # default (proposed)
```

Acceptance criteria:

- A test background migration over 1 million rows survives three worker restarts and finishes with
  `processed = 1000000`, each row converted exactly once (verified by a per-row counter column).
- It does not start while a replica of the previous release is alive.

## Backup, restore and disaster recovery

### R-HA-023 · Backup of PostgreSQL, object storage and keys
Priority: **P1** · Covers: OPS-033

trove's state lives in three places. Each needs its own protection, and none is backed up by
trove itself (an application-level export is Deferred, OPS-032):

| State | Protection | Requirement |
|---|---|---|
| PostgreSQL (all metadata, configuration, users, tokens, audit) | Continuous WAL archiving plus base backups, enabling point-in-time recovery ([continuous archiving](https://www.postgresql.org/docs/current/continuous-archiving.html)) | Recovery window ≥ `backup.recovery_window`. With CloudNativePG through the Barman Cloud plugin ([backup](https://cloudnative-pg.io/docs/1.30/backup), [plugin](https://cloudnative-pg.io/plugin-barman-cloud/docs/intro/)); on managed services their PITR. The archive goes to a bucket *other than* the blob bucket, with different credentials |
| Blob bucket | Bucket versioning with noncurrent-version expiry ≥ `backup.recovery_window`, plus the multipart lifecycle rule (R-HA-012); optionally replication to a second site (R-HA-026) | [S3 versioning](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Versioning.html) |
| Encryption keys (R-HA-027), Helm values | Kept outside the cluster in the operator's secret store; Helm values in Git | Without the keys, sealed secrets in a restored database cannot be decrypted |

- **Recovery point.** CloudNativePG archives a WAL segment at least every `archive_timeout`
  (default 5 min, [PostgreSQL configuration](https://cloudnative-pg.io/docs/1.30/postgresql_conf)),
  which bounds data loss on a full restore. Synchronous replication (R-HA-013) bounds it to zero
  for a failover, but not for a restore.
- **Least privilege against purges.** The runtime S3 credential (R-STOR-031) MUST NOT hold
  `s3:DeleteObjectVersion`, `s3:PutBucketVersioning`, `s3:PutLifecycleConfiguration` or
  `s3:PutBucketPolicy`. A compromised replica can then create delete markers but cannot destroy
  history, and versioning can restore the objects.
- `trove admin backup check` reports, without changing anything:
  - bucket versioning (`GetBucketVersioning`) and lifecycle rules (`GetLifecycleConfiguration`),
    when the credential may read them; otherwise "unknown";
  - `SHOW archive_mode` on the database;
  - whether `storage.gc.grace_period` covers `backup.recovery_window` (R-HA-024).

  The result is exported as the gauge `trove_backup_check_ok{check}` by a daily task.

Configuration:

```yaml
backup:
  recovery_window: 168h       # example; declares the PITR window the platform keeps; 0 = undeclared
  object_versioning: false    # default (proposed); true asserts bucket versioning with noncurrent expiry ≥ recovery_window
```

Acceptance criteria:

- In the local profile with the Barman Cloud plugin configured, `kubectl cnpg backup trove-db`
  succeeds, and `trove admin backup check` reports versioning `Enabled` and `archive_mode = on`.
- With an IAM policy that follows this requirement,
  `aws s3api delete-object --bucket trove-blobs --key <key> --version-id <id>` with the runtime
  credential fails with `AccessDenied`.

### R-HA-024 · Restore consistency between metadata and blobs
Priority: **MVP** · Covers: OPS-033

A PostgreSQL restore to time *t* MUST never produce a database that references a blob whose object
is gone. Three orderings guarantee it. The first two are [storage.md](storage.md)'s; this page
states why they are sufficient and adds the third.

1. **Write before reference.** A blob row becomes `live`, and a reference to it can commit, only
   after its object write succeeded (R-STOR-005, R-STOR-024 rule 4). A database restored to any
   *t* therefore references only objects written before *t*.
2. **Delete only after the grace period.** An object is deleted no earlier than
   `storage.gc.grace_period` after its last reference disappeared (R-STOR-024).
3. **Grace period ≥ recovery window, or versioning.** Let the last reference to blob *B*
   disappear at *u*. Its object is deleted at *d* ≥ *u* + grace. A database restored to *t*
   references *B* only if *t* < *u*. So the restored database misses *B* only if *t* < *u* ≤
   *d* − grace < now − grace, that is, only for restore points older than the grace period.
   Restore points within `recovery_window` are therefore safe when grace ≥ `recovery_window`, or when
   the bucket keeps deleted objects as noncurrent versions for at least `recovery_window`, in which
   case R-HA-025 restores them.

Startup validation: when `backup.recovery_window` is declared (> 0) and exceeds
`storage.gc.grace_period` (default 24 h), a replica MUST refuse to start unless
`backup.object_versioning: true`. The message names all three keys. With the defaults (grace 24 h,
recovery window undeclared), nothing is checked. Declaring the window is the operator's statement
that restores to older points must work.

- **Reads of a missing object.** If an object is nevertheless missing (restore outside the window,
  out-of-band deletion, disaster-recovery lag), reading it MUST answer `503` with `Retry-After`,
  never `404`. The item exists, and its content may come back through replication or version
  restore. It also counts `trove_storage_blobs_damaged_total{status="missing"}` and marks the blob
  `content_status = 'missing'` per R-STOR-027. A remote repository treats it as a cache miss and
  fetches again (R-STOR-027).
- **Orphans after restore.** Objects written after *t* have no row in the restored database. The
  orphan sweep removes them after `storage.orphans.min_age` (R-STOR-026), unless deletions are paused
  (R-HA-025).

Acceptance criteria:

- Test with a fake clock: publish package P at *t₀*, delete it at *t₁*, run GC at
  *t₁* + grace + 1 min (object deleted), restore the database to *t₀* + 1 min. With versioning on,
  `trove admin storage verify` reports P's blob missing, and after `trove admin storage
  restore-versions --before <t₀+1min>` (R-HA-025) `npm install P` succeeds. Without versioning and
  with a restore point inside the grace period, `npm install P` succeeds without any repair.
- `backup.recovery_window: 168h`, `storage.gc.grace_period: 24h`, `objectVersioning: false`: the
  replica exits with a message naming the three keys.

### R-HA-025 · Restore procedure
Priority: **P1** · Covers: OPS-033

Restores are whole-installation, point-in-time operations. Restoring a single repository is
Deferred (repository export and import, OPS-036 and OPS-037, are P2).

1. **Stop writers.** `helm upgrade … --set server.replicaCount=0 --set worker.replicaCount=0 --set server.autoscaling.enabled=false --set highAvailability.enforce=false`.
   Wait `system.heartbeat_stale_after`.
2. **Restore PostgreSQL** into a new cluster. With CloudNativePG, a new `Cluster` with
   `bootstrap.recovery.source` naming the backup's external cluster and
   `recoveryTarget.targetTime` set to *t* ([recovery](https://cloudnative-pg.io/docs/1.30/recovery));
   CloudNativePG never restores in place. On managed services, their PITR to a new instance.
3. **Point the release at it.** Change `config.database.host` (and the credentials Secret, if the
   new cluster has new ones). The encryption key Secret stays the same.
4. **Run `trove admin post-restore --restored-to <t>`** as a one-off pod with the release's
   configuration (`kubectl run … -- trove admin post-restore …`, or
   `helm upgrade … --set postRestore.enabled=true`, which renders a hook Job). It:
   - deletes all rows of `replica` (they describe pods that no longer exist);
   - returns every `running` job to `queued` without incrementing `attempt`;
   - sets the deletion pause (below), so GC and the orphan sweep delete nothing until an operator
     resumes them;
   - ends every UI session (`trove admin sessions end --all`, R-AUTH-015);
   - runs `trove admin storage verify --since <t − storage.gc.grace_period>` and prints the
     number of referenced blobs whose objects are missing;
   - re-applies the revocation journal entries newer than *t* (R-HA-054) and prints one line per
     entry, including every object it suspended;
   - writes the audit event `system.restored` with *t*;
   - prints a reminder that access granted after *t* is gone and that revocations newer than the
     last shipped journal entry are undone (H-37).
5. **Repair missing blobs** if step 4 reported any and the bucket is versioned:
   `trove admin storage restore-versions --before <t>` removes the delete markers of missing
   referenced objects. This needs a separate credential with `s3:ListBucketVersions` and
   `s3:DeleteObjectVersion`, passed only to this command (`--credentials-file`). The runtime
   credential deliberately lacks these permissions (R-HA-023).
6. **Start.** `helm upgrade` with the normal values. The migration Job brings an older restored
   schema forward.
7. **Resume deletions** after verification: `trove admin maintenance resume-deletions`.

Deletion pause (sketch; `system_state` is defined in R-OPS-024):

```sql
ALTER TABLE system_state
  ADD COLUMN deletions_paused_at     timestamptz,
  ADD COLUMN deletions_paused_reason text;
```

- GC, the orphan sweep and retention runs ([storage.md](storage.md)) MUST check the pause before
  every batch of deletions, and skip the batch while it is set.
- `trove admin maintenance pause-deletions --reason …` sets the pause by hand, for example
  before a planned restore drill.

Acceptance criteria (restore drill, part of R-HA-052, run monthly in CI):

- Steps 1–7 against the local profile restore a database to *t* = 10 minutes ago.
  - `npm view` shows exactly the versions published before *t*.
  - A package published after *t* is absent, and its object is not deleted while the pause is set.
  - After `resume-deletions` and an orphan sweep with the test `min_age`, that object is removed.
  - A token revoked after *t* is rejected after step 4 (R-HA-054). With
    `backup.revocation_journal.enabled: false` it works again, which documents H-37.

### R-HA-026 · Cross-site disaster recovery
Priority: **P1** · Covers: OPS-033

- **Topology.** The primary site runs trove. The recovery site keeps:
  - a CloudNativePG replica cluster fed from the primary's WAL archive or by streaming
    ([replica clusters](https://cloudnative-pg.io/docs/1.30/replica_cluster)), or a managed
    cross-region read replica;
  - a bucket filled by one-way replication of the blob bucket
    ([S3 replication](https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication.html));
  - an installed Helm release with replica count 0, the same encryption key Secret, and values
    that differ only in database host, bucket and region.

  Active-active operation across sites is not supported. R-STOR-035 assumption A4 allows only one
  writing site, and federation is Later.
- **Replicate deletes or not.** Delete-marker replication SHOULD be off. The recovery bucket then
  only grows, and a GC delete on the primary can never remove an object that the (lagging) replica
  database still references. After a failover, the orphan sweep at the recovery site removes the
  surplus.
- **Failover.**
  1. Promote the replica cluster.
  2. Move the DNS name of `server.base_url` to the recovery site's edge. Keeping the same base URL
     keeps clients' configuration, tokens and every URL embedded in cached metadata valid.
  3. Scale the release up.
  4. Run `trove admin post-restore --restored-to <last replayed transaction time>`. It pauses
     deletions and reports blobs whose objects have not arrived yet.
- **Replication lag.** The database replica can be ahead of bucket replication. A blob committed on
  the primary shortly before the disaster may be referenced but not yet copied. Its reads answer
  `503` (R-HA-024) until the object arrives, if it ever does. Remote-repository content heals by
  re-fetching. Local content written in the lag window is lost if the primary site's bucket is lost
  too. AWS S3 Replication Time Control bounds that window.
- **Recovery point and time:**
  - RPO = max(database replication lag, bucket replication lag);
  - RTO = promotion + DNS TTL + pod start + post-restore, typically minutes.

  Both are measured in the drill, not promised.

Acceptance criteria:

- Drill in CI with two kind clusters (two "sites"): fail over while an `npm publish` loop runs.
  - Every package acknowledged at least `max lag` before the cut-off installs at the recovery site.
  - Packages in the lag window either install or answer `503`, never `404`.
  - Measured RPO and RTO are reported.

### R-HA-027 · Encryption keys for sealed secrets in a replicated deployment
Priority: **MVP** · Covers: HA-016, HA-031

Secrets stored in PostgreSQL (upstream credentials, webhook secrets, …) are sealed with AES-256-GCM
under the key ring of [architecture.md](architecture.md) R-ARCH-021: `secrets.encryption_key_files`
lists the key files, the file name without `.key` is the key ID, `secrets.active_key_id` names the
sealing key, and the `secret_key_canary` table stops a replica whose ring cannot open a key ID that
sealed rows still reference. This requirement covers what HA and the chart add:

- **Same keys on every replica.** All Deployments and the migration Job mount the same Secret
  (`secrets.encryption.existingSecret`). `secrets.encryption.keys` is the ordered list of key files
  of that Secret. The chart renders `secrets.encryption_key_files` from the whole list and
  `secrets.active_key_id` from the first entry.
- **Never generated by the chart.** The chart MUST NOT create the key. It fails rendering when no
  Secret is named (R-HA-029). A key generated at install time is lost or regenerated on the next
  render wherever Helm's `lookup` is unavailable (`helm template`, Argo CD). A regenerated key
  silently makes every sealed value undecryptable. R-ARCH-021 likewise has no default and no
  generated key. The operator creates the Secret before the first install (Q-HA-8), by hand as
  below or through the cluster's secret tooling (an external-secrets operator, a vault injector,
  sealed secrets). When `secrets.encryption.existingSecret` or `secrets.encryption.keys` is empty,
  the rendering error (R-HA-029) prints this command:

  ```shell
  kubectl -n trove create secret generic trove-encryption-keys \
    --from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)
  ```

  The file name without `.key` is the key ID, here `2026-10`, the form R-ARCH-021 uses.
- **Missing keys.** The canary check of R-ARCH-021 stops a replica whose ring lacks a key ID that
  sealed rows reference; it logs the expected and found key IDs, never key material. Old and new
  replicas with different active keys during step 2 of a rotation are expected and harmless,
  because by then every ring holds both keys.
- **Rotation without a decryption gap.** A plain "new key first, roll out" would let new pods seal
  with a key that old pods do not have yet. Rotation therefore takes two rollouts before the
  re-encryption of R-ARCH-021. Each step can be checked without a shell in a pod through
  `GET /api/v1/system/encryption` ([api-and-ui.md](api-and-ui.md) R-API-049, permission
  `system.read`):
  1. Add the new key to the Secret and *append* it to `secrets.encryption.keys` (decrypt-only
     everywhere). `helm upgrade`.
  2. Before this step, check that `GET /api/v1/system/encryption` lists the new key ID in every
     replica's `keyIds` and reports `ringsCompatible: true`. Then move the key to the first
     position, which makes it `secrets.active_key_id`. `helm upgrade`. Every replica already holds
     it, so values sealed by new pods are readable by old pods.
  3. Start the re-encryption with `POST /api/v1/system/tasks/secrets-reencrypt/run` (R-API-049;
     the `secrets-reencrypt` task of R-ARCH-021 and R-ARCH-016). It answers `409`
     `key_ring_incompatible` while a live replica's ring lacks a key ID that is active somewhere or
     still referenced, and `409` with the running job while one runs.
  4. When `trove admin secrets status` or `GET /api/v1/system/encryption` (R-API-049) reports no
     values under the old key ID, remove it from the list, `helm upgrade`, then remove it from the
     Secret.

  The chart adds a checksum of `secrets.encryption.keys` (names only) to the pod template, so a
  change to the list rolls the pods.

Acceptance criteria:

- `helm template` without `secrets.encryption.existingSecret` fails and names the value.
- Rotation steps 1–4 during an `npm ci` loop against a remote repository with Basic credentials to
  a private upstream: no iteration fails with `502 upstream_secret_unavailable`.
- A pod started with a different Secret content exits with the key-ring mismatch message of
  R-ARCH-021, while the others keep serving.

### R-HA-054 · Revocation journal in object storage
Priority: **P1** · Covers: trove-only

A database restore to time *t* undoes every access removal made after *t* (H-37). trove therefore
records every change to an access-bearing object a second time, outside PostgreSQL, and
`trove admin post-restore` re-applies the removals among the entries newer than *t* (R-HA-025,
step 4; Q-HA-9). Replay never grants access.

**What is journaled.** Transactional audit events (R-OPS-015) whose entry in the event registry
(R-OPS-014) declares `journal: revocation`. When the audit writer inserts such an event, it inserts
a `revocation_outbox` row in the same transaction. A committed change therefore always has its
journal row, and a rolled-back change never has one. The owning pages flag their events and define
what an entry carries. An event type added later is covered by setting its flag, without a change
to this mechanism. At least these events are flagged:

| Audit event | Owner | Replay |
|---|---|---|
| `token.revoked`, `ci.token.revoked` | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-029, [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-017 | Revoke the token, with the cascade of R-AUTH-029 |
| `token.rotated` | identity R-AUTH-030 | Set the predecessor's `expires_at` to the earlier of its current and its journaled value; revoke it when the overlap was 0 |
| `user.disabled`, `user.deleted` | identity R-AUTH-016, R-AUTH-021, R-AUTH-039 | Disable the user |
| `serviceaccount.disabled`, `serviceaccount.deleted` | identity R-AUTH-020 | Disable the service account |
| `group.deleted` | identity R-AUTH-018 | Delete the group |
| `group.members.changed`, `user.groups.synced` | identity R-AUTH-018, R-AUTH-008, R-AUTH-039 | Remove each removed membership with its source; added members are ignored |
| `authz.role.unbound` | [authorization.md](authorization.md) R-AUTHZ-022 | Remove the role binding |
| `authz.policy.deleted` | authorization R-AUTHZ-021 | Delete the policy |
| `authz.ownership.removed`, `authz.ownership.released` | authorization R-AUTHZ-019 | Remove the owner |
| `ci.issuer.*` and `ci.mapping.*` disables and deletions | gitlab-ci R-CI-002, R-CI-019, R-CI-020 | Disable the issuer or mapping. Disabling a mapping that was enabled also suspends every later enabled mapping of its tier, so no job falls through to a mapping that grants more ([Q-CI-11](gitlab-ci-oidc.md#q-ci-11--replaying-a-mapping-disable-under-first-match)) |
| `authz.policy.updated`; `ci.issuer.*` and `ci.mapping.*` updates; `settings.auth.changed` (the anonymous-access switch, R-AUTH-037) and `authz.settings.changed` | the pages above | Update rule below |

The break-glass account's `enabled` state lives in the configuration file, not in PostgreSQL, so a
restore does not revive it and it is not journaled.

**Replay rules.**

- Removals (revoke, disable, delete, remove) are applied as recorded. Each is idempotent: a subject
  already in the recorded state is reported `already in effect`, one missing from the restored
  database `subject unknown`. A deleted user, service account, CI issuer or mapping is disabled
  rather than deleted, so a mistaken or forged entry stays reversible; disabling already makes
  its tokens fail (R-AUTH-016, R-AUTH-020, R-CI-017).
- **Update rule.** An update entry carries the object's state before and after the change, without
  secrets. Replay compares the object's *current* state with the journaled after-state through the
  owning page's comparator `narrows(current, after)`. The comparator answers true only when the
  after-state grants nothing that the current state does not, and false whenever it cannot tell.
  1. True: the access-bearing fields are set to the after-state (`applied`). Replay never changes
     identifying fields (names, keys).
  2. False, and the journaled before-state is contained in the after-state (the change only added
     access): the entry is skipped (`skipped`). Access granted after *t* is not restored.
  3. False otherwise: the object is suspended (`suspended`). A policy loses all its entries, an
     issuer or mapping is disabled, an access setting takes its most restrictive value. The output
     prints the journaled after-state, so an administrator can review and re-apply it.

  Case 3 is a narrowing on top of a broadening that the restored database lacks. Applying it to the
  older state could keep access the narrowing removed, so replay fails closed instead. Comparators
  MUST be conservative: an emptied include list, for example, is unrestricted under R-AUTHZ-005, so
  removing the last include pattern never counts as narrowing.
- **Forged entries cannot escalate.** Whoever holds the runtime S3 credential can write journal
  objects. A removal only removes. Where removing one object can widen what another grants, the
  owning page adds a fail-closed step: a disabled CI mapping would let its jobs fall through to the
  next mapping of its tier, so replay suspends those mappings too (R-CI-020, Q-CI-11). An update is
  applied only when it grants nothing beyond the
  object's current state, and the forgeable before-state only chooses between skipping and
  suspending. An entry of an unknown event type is reported and skipped.
- Within the replay margin (below), an update that is already reflected in the restored database
  can undo a broadening made shortly before *t*. Replay then fails closed, too.

**Outbox and shipping.**

- A worker job ships the rows in `id` order to
  `<storage.s3.prefix>/_journal/revocations/<yyyy>/<mm>/<dd>/<id>.json`, one object per entry, and
  deletes each row after its object was written. A row whose object was written but whose deletion
  did not commit is shipped again under the same key with identical content, which is harmless.
  trove never deletes a journal object. `_journal/` is a key family of
  [storage.md](storage.md) R-STOR-001, so the orphan sweep leaves it alone.
- Shipping is triggered through `NOTIFY` and runs at least every
  `backup.revocation_journal.ship_interval`.
- A change never waits for object storage. During an object-store outage the outbox grows and ships
  afterwards. The gauge `trove_revocation_journal_pending` shows the backlog.

**Replay run.**

- `trove admin post-restore --restored-to <t>` lists the entries with
  `at > t − backup.revocation_journal.replay_margin` and sorts them by `at` and `id`. It applies
  them in that order, each to the state its predecessors left.
- It prints one line per entry (`applied`, `already in effect`, `subject unknown`, `skipped`,
  `suspended`) and writes the audit event `system.restore.journal_replayed` with the counts.
- The margin covers transactions that started before *t* and committed after it (`at` is the
  transaction's `now()`).
- With bucket versioning and the restore credential of R-HA-025 step 5 (`--credentials-file`),
  replay lists object versions as well, so a delete marker set with the runtime credential cannot
  hide an entry. Without versioning, a holder of the runtime credential can delete journal objects
  and so revive what they recorded.
- If replay leaves no `admin` role binding, the output says so. The break-glass account
  (R-AUTH-014) is the way back in.

**Residual gap.** Entries not yet shipped when the old database stopped (normally less than one
shipping interval). Access granted after *t*, including reactivations, is absent from the restored
database and never replayed; an administrator grants it again. Objects suspended under case 3 of
the update rule grant nothing until an administrator reviews them.

**Retention.** Journal objects are kept until the operator expires them, for example with a bucket
lifecycle rule on the `_journal/` prefix that is longer than `backup.recovery_window`. An entry is a
few hundred bytes, an update entry a few kilobytes.

```sql
CREATE TABLE revocation_outbox (
  id     uuid        PRIMARY KEY,        -- the audit event's ID (UUIDv7)
  at     timestamptz NOT NULL,           -- the audit event's occurred_at, the transaction's now()
  event  text        NOT NULL,           -- registry name, e.g. 'token.revoked'
  entry  jsonb       NOT NULL            -- the journal object body; no secrets, no token hashes
);
```

Journal objects (examples, one object per line):

```json
{"v": 1, "id": "0192f0c4-7a1e-7c3b-9d2e-5b8f1a2c3d4e", "at": "2026-10-28T12:00:03.114Z", "event": "token.revoked", "subject": {"type": "token", "id": "0192f0a1-…"}, "actor": "user:alice"}
{"v": 1, "id": "0192f0d2-…", "at": "2026-10-28T12:04:41.902Z", "event": "authz.policy.updated", "subject": {"type": "permission_policy", "id": "0191aa07-…"}, "before": {"entries": [{"principal": "group:team-web", "actions": ["deploy", "read"]}]}, "after": {"entries": [{"principal": "group:team-web", "actions": ["read"]}]}, "actor": "user:sec-alice"}
```

Configuration:

```yaml
backup:
  revocation_journal:
    enabled: true          # default (proposed)
    ship_interval: 5s      # default (proposed)
    replay_margin: 5m      # default (proposed); longer than any transaction (R-HA-015 timeouts)
```

Acceptance criteria:

- Revoke a personal access token, wait `ship_interval`, restore the database to a point before the
  revocation and run `post-restore`: the token is rejected with `401`, and the output lists
  `token.revoked … applied`.
- Remove `deploy` from a policy entry and unbind `admin` from a group, then restore to a point
  before both changes and run `post-restore`: the entry holds only `read`, and the group's members
  are not administrators. A policy that was first broadened and then narrowed after *t* ends
  `suspended` and grants nothing.
- With the S3 store stopped, revoking a token takes effect at once; its journal object appears
  within `ship_interval` after the store returns.
- Three hand-written journal objects (one with an unknown event type, one whose after-state adds an
  action to a policy, one whose before-state is forged so that the addition looks like a
  narrowing) are reported `skipped` or `suspended`, and no principal gains access.

## Helm chart

### R-HA-028 · Chart scope, rendered resources and naming
Priority: **MVP** · Covers: HA-029, HA-032, HA-038, HA-021

- The chart `trove` lives in `deploy/helm/trove`, `apiVersion: v2`, and follows the
  [Helm chart best practices](https://helm.sh/docs/chart_best_practices/): camelCase values,
  standard `app.kubernetes.io/*` labels, no CRDs shipped, no subcharts. It is released with
  `version` = `appVersion` = trove's version.
- There is no bundled database and no bundled object store, because production needs both to be
  highly available (INV-12). The local development profile (R-HA-051) installs them separately.
- **The chart never creates or generates a Secret.** Every credential is a reference to an
  existing Secret (R-HA-030). This removes a whole class of upgrade failures: no generated value
  can change on re-render.

Rendered resources (`<fullname>` is the release name, or `<release>-trove` when the release name
does not contain `trove`, overridable by `fullnameOverride`):

| Kind | Name | Rendered when |
|---|---|---|
| Deployment | `<fullname>`, or `<fullname>-api` and `<fullname>-worker` | always; two with `roleSplit.enabled` |
| Service (ClusterIP) | `<fullname>`, port `http` 8080 → container `http` | always |
| Service (ClusterIP) | `<fullname>-ops`, port `ops` 8081, all roles | always (probes from the kubelet do not need it; ServiceMonitor does) |
| ConfigMap | `<fullname>-config` (`trove.yaml`) | always |
| ServiceAccount | `<fullname>` | `serviceAccount.create` |
| ConfigMap, ServiceAccount (hooks, weight −10) | `<fullname>-migrate` | `migrations.mode: hook` |
| Job (hook) | `<fullname>-migrate` | `migrations.mode: hook` |
| Job (hook) | `<fullname>-post-restore` | `postRestore.enabled` |
| PodDisruptionBudget | per Deployment | `podDisruptionBudget.enabled` |
| HorizontalPodAutoscaler (`autoscaling/v2`) | per Deployment | `autoscaling.enabled` |
| Ingress | `<fullname>` | `ingress.enabled` |
| HTTPRoute | `<fullname>` | `gateway.enabled` |
| Gateway | `<fullname>-gateway` | `gateway.enabled` and `gateway.create.enabled` (P1, R-HA-053) |
| NetworkPolicy | `<fullname>` (and `<fullname>-migrate` for the Job) | `networkPolicy.enabled` |
| ServiceMonitor, PrometheusRule | `<fullname>` | `metrics.serviceMonitor.enabled`, `metrics.prometheusRule.enabled` |
| anything | from `extraObjects` | listed |

- Labels on every object: `app.kubernetes.io/name: trove`, `app.kubernetes.io/instance`,
  `app.kubernetes.io/version`, `app.kubernetes.io/managed-by`, `helm.sh/chart`, and
  `app.kubernetes.io/component` (`server`, `api`, `worker`, `migrate`). Selectors use name,
  instance and component only, because selector labels are immutable.
- **Ports (HA-021).** Containers listen on 8080 (`http`, client listener) and 8081 (`ops`) and
  nothing else (INV-6). Both ports are unprivileged, so no capability is needed. Replicas need no
  port open towards each other.
- The pod template carries `checksum/config` (hash of the rendered ConfigMap), so every
  configuration change rolls the pods. Secret contents are not hashed, because the chart does not
  read them. Rotated Secret files are re-read by their consumers where the owning page says so
  (database password, OIDC client secret, CA bundles, break-glass password). Other changes need
  `kubectl rollout restart`.

Acceptance criteria:

- `helm lint deploy/helm/trove` and `helm template` with `ci/*.yaml` value sets pass in CI. A
  kubeconform run validates the output against the Kubernetes and Gateway API schemas of the
  supported versions.
- `helm template … | grep -c '^kind: Secret$'` prints `0` for every CI value set.

### R-HA-029 · Fail-fast chart validation
Priority: **MVP** · Covers: HA-030

The chart MUST refuse to render, naming the offending value, before anything is applied:

| Condition | Message names |
|---|---|
| `config.server.base_url` empty | `config.server.base_url` |
| `config.database.host` or `secrets.database.existingSecret` empty | the value |
| `config.storage.s3.bucket` empty | the value |
| `config.storage.s3.credentials.source: static` without `secrets.s3.existingSecret` | both |
| `secrets.encryption.existingSecret` or `secrets.encryption.keys` empty | the value, and the key-creation command of R-HA-027 |
| `highAvailability.enforce` and fewer than 2 replicas (`replicaCount`, or `autoscaling.minReplicas`) for any Deployment, or a disabled PDB | the Deployment |
| `roleSplit.enabled` and `worker.replicaCount` (or minimum) < 1 | `worker.replicaCount` |
| `config.storage.upload.max_size` > the spool budget derived from `spool.sizeLimit` (R-HA-035) | both |
| `terminationGracePeriodSeconds` < `config.health.shutdown_drain_delay` + 15 s | both |
| `gateway.enabled` without `gateway.parentRefs` and without `gateway.create.enabled` | `gateway.parentRefs` |
| `gateway.enabled` and `.Capabilities.APIVersions` lacks `gateway.networking.k8s.io/v1/HTTPRoute` | the missing API |
| `metrics.serviceMonitor.enabled` (or `prometheusRule`) and the `monitoring.coreos.com/v1` API missing | the missing API |
| A chart-managed configuration key set inside `config` (secret file paths, `roles`, `storage.spool.dir`, `server.content_base_url`) | the key |
| `contentOrigin.host` equal to the host of `config.server.base_url` or of a `config.server.additional_base_urls` entry (P1, R-HA-055) | `contentOrigin.host` |
| `podDisruptionBudget` with both `minAvailable` and `maxUnavailable` set | both |

- `values.schema.json` validates every chart-owned value (types, enums, `additionalProperties:
  false`); Helm checks it on install, upgrade, lint and template
  ([schema files](https://helm.sh/docs/topics/charts/#schema-files)). It uses JSON Schema
  draft-07 keywords only.
- The `config` subtree is validated against the schema the binary emits (`trove config schema`).
  CI regenerates it from the Go configuration types and fails on drift. A typo in a configuration
  key therefore fails `helm install`, not silently at runtime (catalog trap HA-036). The binary
  rejects unknown keys at startup as a second line.
- Offline rendering (`helm template`) has no cluster capabilities. CI passes
  `--api-versions gateway.networking.k8s.io/v1/HTTPRoute --api-versions monitoring.coreos.com/v1`
  where needed.

Acceptance criteria: a CI test renders one value set per row of the table and asserts the failure
and the named value. Another renders `values-dev.yaml` and the three profiles successfully.

### R-HA-030 · Configuration rendering and secret wiring
Priority: **MVP** · Covers: HA-033, HA-035, HA-036, HA-037, HA-053

**Configuration file.**

- The chart renders `config` into `/etc/trove/trove.yaml` (INV-15), merged with chart-managed
  keys:
  - secret file paths;
  - `roles` per Deployment;
  - `storage.spool.dir`;
  - `storage.spool.max_bytes`, derived from the volume when unset (R-HA-035);
  - `server.content_base_url`, rendered as `https://<contentOrigin.host>` when `contentOrigin.host`
    is set (P1, R-HA-055).
- Precedence follows INV-15: environment variable `TROVE_<SECTION>_<KEY>` (nested keys joined
  with `_`, e.g. `TROVE_DATABASE_POOL_MAX_CONNS`) > file > built-in default. `extraEnv` can
  therefore override any file value. The chart itself sets only:
  - `TROVE_RUNTIME_MEMORY_LIMIT_BYTES`, `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD` and
    `TROVE_DATABASE_PREFLIGHT_EXPECTED_REPLICAS`, the variables of the schema keys
    `runtime.memory_limit_bytes`, `runtime.termination_grace_period` and
    `database.preflight.expected_replicas` (R-OPS-028 mapping; the chart is their only source);
  - `GOMAXPROCS` (R-HA-036);
  - the pod name and namespace through the downward API.
- There is no "complete configuration file from a Secret" mode. It would let an operator replace
  chart-managed paths and lose the validation of R-HA-029. Everything that is not a secret goes
  through `config`.
- `enableServiceLinks: false` on every pod. Kubernetes otherwise injects variables named after
  Services in the namespace. A Service named `trove-database` would inject
  `TROVE_DATABASE_PORT=tcp://…`, which collides with trove's own variable prefix and fails
  strict validation
  ([Service environment variables](https://kubernetes.io/docs/concepts/services-networking/service/#environment-variables)).

**Secrets.** Every Secret is pre-created by the operator and mounted as a read-only volume with
`items` (only the named keys), `defaultMode: 0440`, and no `subPath`, so rotations reach the files.
Secrets never become environment variables. Environment variables are readable through `/proc`,
end up in crash reports, and do not follow Secret updates.

| Value | Keys (default names) | Mount path | Configuration key set by the chart |
|---|---|---|---|
| `secrets.database` | `username`, `password` | `/var/run/secrets/trove/database/` | `database.username_file`, `database.password_file` |
| `secrets.databaseMigration` (Job only) | `username`, `password` | same path in the Job | same |
| `secrets.databaseCA` | `ca.crt` | `/var/run/secrets/trove/database-ca/` | `database.ssl_root_cert_file` |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/database-client/` | `database.ssl_cert_file`, `database.ssl_key_file` |
| `secrets.s3` (only with `source: static`) | `access-key-id`, `secret-access-key` | `/var/run/secrets/trove/s3/` | `storage.s3.credentials.access_key_id_file`, `…secret_access_key_file` |
| `secrets.oidc[]` (one entry per provider ID) | `client-secret` | `/var/run/secrets/trove/oidc-<id>/` | `client_secret_file` of the provider with that `id` in `config.auth.oidc.providers` |
| `secrets.breakGlass` | `password` | `/var/run/secrets/trove/break-glass/` | `auth.break_glass.password_file`; empty value → `auth.break_glass.enabled: false` |
| `secrets.encryption` | `secrets.encryption.keys` | `/var/run/secrets/trove/encryption/` | `secrets.encryption_key_files` (ordered) and `secrets.active_key_id` (the first entry) |
| `secrets.metricsToken` | `token` | `/var/run/secrets/trove/metrics/` | `metrics.bearer_token_file` (R-OPS-006) |
| `trustedCA.*` (Secret or ConfigMap) | listed keys | `/etc/trove/ca/` | `proxy.outbound.tls.ca_files` (R-REPO-035); optionally `storage.s3.ca_file` |
| `bootstrap.existingConfigMap` (P1) | any | `/etc/trove/bootstrap/` | configuration documents of [operations-and-observability.md](operations-and-observability.md) |

- **External database (HA-033).** Only an external PostgreSQL is supported. The host goes in
  `config.database.host`, credentials in `secrets.database`. No "wait for database" init container
  is rendered: the replica retries the connection at startup while not ready, and the migration Job
  retries its first connection within `activeDeadlineSeconds`.
- **Object storage (HA-053).** S3 is the only back end (INV-4), so there is no unsafe "local file
  system" default that silently breaks with a second replica. Workload identity is the default
  (`credentials.source: default`). On EKS the chart's ServiceAccount carries the role annotation
  ([IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)),
  or a Pod Identity association grants the role without one
  ([EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html)).
  Static keys use `secrets.s3`.
- **Bootstrap (HA-037).** The break-glass administrator is the first-start credential (R-AUTH-014).
  Further bootstrapping is `kubectl exec deploy/trove -- trove admin …` (R-AUTH-015). The chart
  renders no Job that writes tokens into Secrets: such a Job would need `create` on Secrets in the
  namespace, a permission the chart otherwise never needs. Operators store the printed token with
  their own secret tooling.

Acceptance criteria:

- `kubectl exec <pod> -- env | grep -iE 'password|secret|token'` prints nothing.
- Replacing the database Secret's password (R-HA-017) reaches `/var/run/secrets/trove/database/password`
  within 2 minutes without a restart.
- With a Service `trove-database` in the namespace, the pods start (no injected variables).

### R-HA-031 · Values reference
Priority: **MVP** · Covers: HA-029

The complete `values.yaml`. Every value carries `# default (proposed)` or `# example`. Keys under
`config` belong to the pages named in the comments; this page only fixes how the chart wires them.
Keys marked P1 (`gateway.create`, `contentOrigin`, `config.backup.revocation_journal`, `bootstrap`) enter the chart
with the release that implements them: the `config` subtree is validated against the schema the
binary emits (R-HA-029), so a key the binary does not know yet would fail `helm install`.

<details>
<summary>deploy/helm/trove/values.yaml</summary>

```yaml
# ── Global ──────────────────────────────────────────────────────────────────
global:
  imageRegistry: ""                     # default (proposed): empty = image.registry
  imagePullSecrets: []                  # default (proposed); example: [{name: registry-pull}]

nameOverride: ""                        # default (proposed)
fullnameOverride: ""                    # default (proposed)
commonLabels: {}                        # default (proposed)
commonAnnotations: {}                   # default (proposed)

image:
  registry: ghcr.io                     # default (proposed) (Q-HA-11)
  repository: guided-traffic/trove      # default (proposed)
  tag: ""                               # default (proposed): empty = chart appVersion
  digest: ""                            # default (proposed); example: sha256:<64 hex>; wins over tag
  pullPolicy: IfNotPresent              # default (proposed)

highAvailability:
  enforce: true                         # default (proposed): ≥ 2 replicas and a PDB per Deployment (R-HA-029)

roleSplit:
  enabled: false                        # default (proposed): one Deployment with roles [api, worker] (R-HA-002)

# ── Server Deployment: roles [api, worker], or [api] with roleSplit ──────────
server:
  replicaCount: 2                       # default (proposed); ignored when autoscaling.enabled
  minReadySeconds: 10                   # default (proposed)
  revisionHistoryLimit: 5               # default (proposed)
  strategy:
    type: RollingUpdate                 # default (proposed)
    rollingUpdate:
      maxSurge: 1                       # default (proposed)
      maxUnavailable: 0                 # default (proposed)
  terminationGracePeriodSeconds: 120    # default (proposed) (R-HA-011)
  resources:
    requests:
      cpu: "1"                          # default (proposed)
      memory: 1Gi                       # default (proposed)
      ephemeral-storage: 2Gi            # default (proposed)
    limits:
      memory: 2Gi                       # default (proposed); no CPU limit by default (R-HA-036)
  autoscaling:
    enabled: false                      # default (proposed)
    minReplicas: 2                      # default (proposed)
    maxReplicas: 6                      # default (proposed)
    targetCPUUtilizationPercentage: 70  # default (proposed)
    targetMemoryUtilizationPercentage: 0  # default (proposed): 0 = no memory metric
    metrics: []                         # default (proposed); additional autoscaling/v2 metric specs
    behavior:
      scaleUp:
        stabilizationWindowSeconds: 0   # default (proposed)
        policies:
          - type: Pods                  # default (proposed)
            value: 2                    # default (proposed)
            periodSeconds: 60           # default (proposed)
      scaleDown:
        stabilizationWindowSeconds: 300 # default (proposed)
        policies:
          - type: Pods                  # default (proposed)
            value: 1                    # default (proposed)
            periodSeconds: 120          # default (proposed)
  podDisruptionBudget:
    enabled: true                       # default (proposed)
    maxUnavailable: 1                   # default (proposed)
    minAvailable: null                  # default (proposed); mutually exclusive with maxUnavailable
    unhealthyPodEvictionPolicy: AlwaysAllow  # default (proposed) (R-HA-008)
  topologySpread:
    enabled: true                       # default (proposed): render the two constraints below
    zone:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: ScheduleAnyway # default (proposed)
    host:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: DoNotSchedule  # default (proposed)
  topologySpreadConstraints: []         # default (proposed); non-empty replaces the generated constraints
  nodeFailureTolerationSeconds: 60      # default (proposed); not-ready/unreachable tolerations (R-HA-032)
  affinity: {}                          # default (proposed)
  nodeSelector: {}                      # default (proposed); example: {kubernetes.io/arch: arm64}
  tolerations: []                       # default (proposed)
  priorityClassName: ""                 # default (proposed); example: business-critical
  schedulerName: ""                     # default (proposed)
  podAnnotations: {}                    # default (proposed)
  podLabels: {}                         # default (proposed)
  extraEnv: []                          # default (proposed); appended after the shared extraEnv

# ── Worker Deployment: roles [worker]; rendered only with roleSplit.enabled ─
worker:
  replicaCount: 2                       # default (proposed)
  minReadySeconds: 10                   # default (proposed)
  revisionHistoryLimit: 5               # default (proposed)
  strategy:
    type: RollingUpdate                 # default (proposed)
    rollingUpdate:
      maxSurge: 1                       # default (proposed)
      maxUnavailable: 0                 # default (proposed)
  terminationGracePeriodSeconds: 300    # default (proposed): jobs persist their cursor before exit
  spoolSizeLimit: 5Gi                   # default (proposed); workers spool only cache fills of jobs
  resources:
    requests:
      cpu: 500m                         # default (proposed)
      memory: 512Mi                     # default (proposed)
      ephemeral-storage: 1Gi            # default (proposed)
    limits:
      memory: 1Gi                       # default (proposed)
  autoscaling:
    enabled: false                      # default (proposed)
    minReplicas: 2                      # default (proposed)
    maxReplicas: 4                      # default (proposed)
    targetCPUUtilizationPercentage: 80  # default (proposed)
    targetMemoryUtilizationPercentage: 0  # default (proposed)
    metrics: []                         # default (proposed)
    behavior: {}                        # default (proposed): Kubernetes defaults
  podDisruptionBudget:
    enabled: true                       # default (proposed)
    maxUnavailable: 1                   # default (proposed)
    minAvailable: null                  # default (proposed)
    unhealthyPodEvictionPolicy: AlwaysAllow  # default (proposed)
  topologySpread:
    enabled: true                       # default (proposed)
    zone:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: ScheduleAnyway # default (proposed)
    host:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: ScheduleAnyway # default (proposed)
  topologySpreadConstraints: []         # default (proposed)
  nodeFailureTolerationSeconds: 60      # default (proposed)
  affinity: {}                          # default (proposed)
  nodeSelector: {}                      # default (proposed)
  tolerations: []                       # default (proposed)
  priorityClassName: ""                 # default (proposed)
  schedulerName: ""                     # default (proposed)
  podAnnotations: {}                    # default (proposed)
  podLabels: {}                         # default (proposed)
  extraEnv: []                          # default (proposed)

# ── trove configuration (rendered to /etc/trove/trove.yaml) ───────────
# Chart-managed keys (secret file paths, roles, storage.spool.dir, server.content_base_url) must not be set here (R-HA-029).
# The migration Job's hook ConfigMap carries the same file (R-HA-019).
config:
  server:                               # operations-and-observability.md R-OPS-005
    base_url: ""                        # required; example: https://trove.example.com
    trusted_proxies: []                 # default (proposed); example: ["10.244.0.0/16"] (ingress controller pod CIDR)
    additional_base_urls: []            # default (proposed)
  health:                               # operations-and-observability.md R-OPS-001
    require_object_store: false         # default (proposed): object storage gates only the first readiness (R-HA-008, Q-HA-1)
    shutdown_drain_delay: 5s            # default (proposed)
  metrics:                              # operations-and-observability.md R-OPS-006
    enabled: true                       # default (proposed)
    auth: none                          # default (proposed): none | static (needs secrets.metricsToken)
  log:
    level: info                         # default (proposed); operations-and-observability.md
  database:                             # this page, R-HA-009 to R-HA-016
    host: ""                            # required; example: trove-db-rw.trove.svc
    port: 5432                          # default (proposed)
    name: trove                       # example
    schema: trove                     # default (proposed)
    ssl_mode: verify-full               # default (proposed)
    target_session_attrs: read-write    # default (proposed)
    connect_timeout: 5s                 # default (proposed)
    pool:
      max_conns: 20                     # default (proposed)
      min_conns: 2                      # default (proposed)
      max_conn_lifetime: 30m            # default (proposed)
      max_conn_lifetime_jitter: 5m      # default (proposed)
      max_conn_idle_time: 5m            # default (proposed)
      health_check_period: 30s          # default (proposed)
    lease_pool_size: 4                  # default (proposed)
    lease_check_interval: 5s            # default (proposed)
    session:
      statement_timeout: 30s            # default (proposed)
      lock_timeout: 5s                  # default (proposed)
      idle_in_transaction_session_timeout: 60s  # default (proposed)
    retry:
      initial_backoff: 100ms            # default (proposed)
      max_backoff: 2s                   # default (proposed)
      max_elapsed: 30s                  # default (proposed)
    keepalive:
      idle: 15s                         # default (proposed)
      interval: 5s                      # default (proposed)
      count: 3                          # default (proposed)
      user_timeout: 30s                 # default (proposed)
    server_keepalive:                   # sent as tcp_keepalives_* startup parameters (R-HA-015)
      idle: 60s                         # default (proposed)
      interval: 10s                     # default (proposed)
      count: 6                          # default (proposed)
    preflight:
      connection_budget: enforce        # default (proposed): enforce | warn
      reserved_connections: 10          # default (proposed)
      allow_superuser: false            # default (proposed)
  storage:                              # storage.md R-STOR-024 to R-STOR-036
    s3:
      bucket: ""                        # required; example: trove-blobs
      prefix: trove                   # default (proposed) in storage.md
      region: ""                        # example: eu-central-1
      endpoint: ""                      # default (proposed): AWS resolution; example: https://minio.trove.svc:9000
      force_path_style: false           # default (proposed); true for MinIO, SeaweedFS and Ceph RGW
      credentials:
        source: default                 # default (proposed): default | webIdentity | static
        role_arn: ""                    # default (proposed)
    upload:
      max_size: 10GiB                   # default (proposed) in storage.md; must fit the spool (R-HA-035)
    gc:
      grace_period: 24h                 # default (proposed) in storage.md; see R-HA-024
  proxy:
    lock_pool_size: 16                  # default (proposed) in repositories-and-proxy.md; counts toward R-HA-016
  jobs:
    schedule_tick: 10s                  # default (proposed)
    lease_duration: 60s                 # default (proposed)
    heartbeat_interval: 15s             # default (proposed)
    background_migrations:
      batch_size: 1000                  # default (proposed)
      pause_between_batches: 100ms      # default (proposed)
  migrate:
    advisory_lock_wait: 5m              # default (proposed)
    ddl_lock_timeout: 5s                # default (proposed)
    ddl_lock_retries: 20                # default (proposed)
  backup:
    recovery_window: 0s                 # default (proposed): undeclared; example: 168h (R-HA-024)
    object_versioning: false            # default (proposed)
    revocation_journal:                 # P1 (R-HA-054)
      enabled: true                     # default (proposed)
      ship_interval: 5s                 # default (proposed)
      replay_margin: 5m                 # default (proposed)
  runtime:
    memory_limit_ratio: 0.85            # default (proposed) (R-HA-036)
  audit:                                # operations-and-observability.md R-OPS-015, R-OPS-017
    retention_days: 365                 # default (proposed); written to audit_policy by the migration Job (R-HA-014)
  auth: {}                              # default (proposed): empty; identity-and-authentication.md (OIDC providers, breakGlass.username, …)
  authz: {}                             # default (proposed): empty; authorization.md
  ci: {}                                # default (proposed): empty; gitlab-ci-oidc.md

# ── Existing Secrets (the chart never creates one) ──────────────────────────
secrets:
  database:
    existingSecret: ""                  # required; example: trove-db-app (CloudNativePG application Secret)
    usernameKey: username               # default (proposed)
    passwordKey: password               # default (proposed)
  databaseMigration:
    existingSecret: ""                  # default (proposed): empty = single-role mode (R-HA-014); example: trove-db-owner
    usernameKey: username               # default (proposed)
    passwordKey: password               # default (proposed)
  databaseCA:
    existingSecret: ""                  # example: trove-db-ca (only ca.crt is projected)
    key: ca.crt                         # default (proposed)
  databaseClientCert:
    existingSecret: ""                  # default (proposed): no client certificate
    certKey: tls.crt                    # default (proposed)
    keyKey: tls.key                     # default (proposed)
  s3:
    existingSecret: ""                  # default (proposed); required with credentials.source static
    accessKeyIdKey: access-key-id       # default (proposed)
    secretAccessKeyKey: secret-access-key  # default (proposed)
    sessionTokenKey: ""                 # default (proposed)
  oidc: []                              # default (proposed); example:
  #  - providerId: corp                 # example; matches config.auth.oidc.providers[].id
  #    existingSecret: trove-oidc-corp  # example
  #    key: client-secret               # example
  breakGlass:
    existingSecret: ""                  # default (proposed): empty disables the break-glass account; example: trove-break-glass
    key: password                       # default (proposed); plaintext or Argon2id PHC hash (R-AUTH-014)
  encryption:
    existingSecret: ""                  # required; example: trove-encryption-keys
    keys: []                            # required; ordered, first = active; example: [2026-10.key, 2026-04.key]
  metricsToken:
    existingSecret: ""                  # default (proposed); needed with config.metrics.auth static
    key: token                          # default (proposed)

trustedCA:
  existingSecret: ""                    # default (proposed); example: corp-ca-bundle
  existingConfigMap: ""                 # default (proposed); example: corp-ca-bundle (trust-manager target)
  keys: [ca-bundle.crt]                 # default (proposed)
  useForObjectStore: false              # default (proposed); also set storage.s3.caFile

bootstrap:
  existingConfigMap: ""                 # default (proposed); configuration documents (P1)

# ── Pod identity and security ──────────────────────────────────────────────
serviceAccount:
  create: true                          # default (proposed)
  name: ""                              # default (proposed): <fullname>
  annotations: {}                       # default (proposed); example (IRSA):
  #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-s3  # example
  automountServiceAccountToken: false   # default (proposed): trove never calls the Kubernetes API

podSecurityContext:
  runAsNonRoot: true                    # default (proposed)
  runAsUser: 65532                      # default (proposed); null on OpenShift (restricted-v2 assigns the UID)
  runAsGroup: 65532                     # default (proposed); null on OpenShift
  fsGroup: 65532                        # default (proposed); null on OpenShift
  fsGroupChangePolicy: OnRootMismatch   # default (proposed)
  seccompProfile:
    type: RuntimeDefault                # default (proposed)

securityContext:
  allowPrivilegeEscalation: false       # default (proposed)
  privileged: false                     # default (proposed)
  readOnlyRootFilesystem: true          # default (proposed)
  runAsNonRoot: true                    # default (proposed)
  capabilities:
    drop: [ALL]                         # default (proposed)
  seccompProfile:
    type: RuntimeDefault                # default (proposed)

# ── Volumes ────────────────────────────────────────────────────────────────
spool:
  type: emptyDir                        # default (proposed): emptyDir | ephemeral
  sizeLimit: 20Gi                       # default (proposed) (R-HA-035)
  headroom: 2GiB                        # default (proposed): sizeLimit − headroom = storage.spool.maxBytes
  medium: ""                            # default (proposed): node disk; "Memory" counts against the memory limit
  ephemeral:
    storageClassName: ""                # default (proposed): cluster default
    size: 50Gi                          # default (proposed); used with type ephemeral
    accessModes: [ReadWriteOnce]        # default (proposed)

tmp:
  sizeLimit: 1Gi                        # default (proposed); /tmp under readOnlyRootFilesystem

# ── Probes (R-HA-034; paths from R-OPS-001) ────────────────────────────────
probes:
  startup:
    enabled: true                       # default (proposed)
    path: /healthz                      # default (proposed)
    periodSeconds: 2                    # default (proposed)
    timeoutSeconds: 2                   # default (proposed)
    failureThreshold: 60                # default (proposed): 2 minutes to bind the listeners
    custom: {}                          # default (proposed); replaces the generated probe
  liveness:
    enabled: true                       # default (proposed)
    path: /healthz                      # default (proposed)
    periodSeconds: 10                   # default (proposed)
    timeoutSeconds: 3                   # default (proposed)
    failureThreshold: 6                 # default (proposed)
    custom: {}                          # default (proposed)
  readiness:
    enabled: true                       # default (proposed)
    path: /readyz                       # default (proposed)
    periodSeconds: 5                    # default (proposed)
    timeoutSeconds: 3                   # default (proposed)
    failureThreshold: 1                 # default (proposed): debouncing happens in the process (health.failure_threshold)
    successThreshold: 1                 # default (proposed)
    custom: {}                          # default (proposed)

# ── Exposure ───────────────────────────────────────────────────────────────
service:
  type: ClusterIP                       # default (proposed): ClusterIP | LoadBalancer | NodePort
  port: 8080                            # default (proposed)
  opsPort: 8081                         # default (proposed)
  annotations: {}                       # default (proposed)
  labels: {}                            # default (proposed)
  loadBalancerSourceRanges: []          # default (proposed)
  externalTrafficPolicy: ""             # default (proposed): Kubernetes default; example: Local
  ipFamilyPolicy: ""                    # default (proposed): cluster default; example: PreferDualStack (R-HA-044)
  ipFamilies: []                        # default (proposed); example: [IPv6, IPv4]

ingress:
  enabled: false                        # default (proposed)
  className: ""                         # default (proposed): cluster default IngressClass; example: traefik
  annotations: {}                       # default (proposed); controller-specific settings (R-HA-039)
  labels: {}                            # default (proposed)
  hosts: []                             # default (proposed): empty = host of config.server.base_url
  # No path value: every host is routed whole, path / with pathType Prefix (R-HA-037).
  tls: []                               # default (proposed); example: [{secretName: trove-tls, hosts: [trove.example.com]}]

gateway:
  enabled: false                        # default (proposed)
  parentRefs: []                        # default (proposed); example: [{name: shared, namespace: gateway-system, sectionName: https}]
  hostnames: []                         # default (proposed): empty = host of config.server.base_url
  annotations: {}                       # default (proposed)
  labels: {}                            # default (proposed)
  timeouts:
    request: 3600s                      # default (proposed) (R-HA-039)
    backendRequest: 3600s               # default (proposed)
  filters: []                           # default (proposed); HTTPRoute filters on the main rule
  extraRules: []                        # default (proposed)
  create:                               # P1 (R-HA-053)
    enabled: false                      # default (proposed): attach to an existing Gateway
    gatewayClassName: ""                # default (proposed); example: envoy-gateway
    listeners: []                       # default (proposed); example: [{name: https, protocol: HTTPS, port: 443, tls: {certificateRefs: [{name: trove-tls}]}}]

contentOrigin:                          # P1 (R-HA-055); renders config.server.content_base_url (R-ARCH-029)
  host: ""                              # default (proposed): empty = one origin (R-ARCH-007); example: trove-content.example.net
  tls:
    secretName: ""                      # default (proposed): no TLS entry of its own; example: trove-content-tls

# ── Network policy (R-HA-042) ──────────────────────────────────────────────
networkPolicy:
  enabled: true                         # default (proposed)
  ingress:
    http:
      from: []                          # default (proposed): empty = from anywhere; example: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ingress}}}]
    ops:
      from:                             # default (proposed)
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring  # default (proposed)
    probeCIDRs: []                      # default (proposed); node CIDRs, for CNIs that apply policy to kubelet probes
  egress:
    enabled: false                      # default (proposed): no egress restriction rendered
    dns:
      to:                               # default (proposed)
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system  # default (proposed)
          podSelector:
            matchLabels:
              k8s-app: kube-dns         # default (proposed)
    database:
      to: []                            # default (proposed); example: [{podSelector: {matchLabels: {cnpg.io/cluster: trove-db}}}]
      ports: [5432]                     # default (proposed)
    objectStorage:
      to: []                            # default (proposed); example: [{ipBlock: {cidr: 0.0.0.0/0}}] or a MinIO podSelector
      ports: [443]                      # default (proposed)
    podIdentityAgent: false             # default (proposed); true allows 169.254.170.23:80 (EKS Pod Identity)
    internet:
      enabled: true                     # default (proposed): upstreams, OIDC issuers, STS
      ports: [443, 80]                  # default (proposed)
      exceptCIDRs: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16]  # default (proposed)
    extra: []                           # default (proposed); additional egress rules

# ── Monitoring (R-HA-045) ──────────────────────────────────────────────────
metrics:
  serviceMonitor:
    enabled: false                      # default (proposed)
    namespace: ""                       # default (proposed): release namespace
    interval: 30s                       # default (proposed)
    scrapeTimeout: 10s                  # default (proposed)
    labels: {}                          # default (proposed); example: {release: kube-prometheus-stack}
    relabelings: []                     # default (proposed)
    metricRelabelings: []               # default (proposed)
  prometheusRule:
    enabled: false                      # default (proposed)
    labels: {}                          # default (proposed)
    disabledAlerts: []                  # default (proposed); example: [TroveSpoolNearlyFull]
    extraRules: []                      # default (proposed)

# ── Migrations and restore (R-HA-019, R-HA-025) ────────────────────────────
migrations:
  mode: hook                            # default (proposed): hook | disabled
  backoffLimit: 2                       # default (proposed)
  activeDeadlineSeconds: 1800           # default (proposed)
  ttlSecondsAfterFinished: 86400        # default (proposed)
  resources:
    requests:
      cpu: 100m                         # default (proposed)
      memory: 128Mi                     # default (proposed)
    limits:
      memory: 512Mi                     # default (proposed)
  podAnnotations: {}                    # default (proposed)
  extraEnv: []                          # default (proposed)

postRestore:
  enabled: false                        # default (proposed); renders the post-restore hook Job once
  restoredTo: ""                        # example: "2026-10-28T09:15:00Z"

# ── Extension points (R-HA-046) ────────────────────────────────────────────
extraEnv: []                            # default (proposed); example: [{name: TROVE_LOG_LEVEL, value: debug}]
extraEnvFrom: []                        # default (proposed)
extraVolumes: []                        # default (proposed)
extraVolumeMounts: []                   # default (proposed)
initContainers: []                      # default (proposed); run before trove starts
sidecars: []                            # default (proposed); native sidecars (restartPolicy: Always) allowed
lifecycle: {}                           # default (proposed): none; trove drains itself (R-HA-011)
extraObjects: []                        # default (proposed); extra manifests rendered with tpl
```

</details>

Acceptance criteria:

- `helm show values deploy/helm/trove` equals this block, except for formatting. A CI test
  compares the key sets.
- `helm template` with only the required values set (`config.server.base_url`,
  `config.database.host`, `config.storage.s3.bucket`, `secrets.database.existingSecret`,
  `secrets.encryption.existingSecret`, `secrets.encryption.keys`) renders without error.

### R-HA-032 · Replicas, disruption budget, spreading and autoscaling
Priority: **MVP** · Covers: HA-009, HA-032, HA-043, HA-044

- **Replicas.** Default 2 per Deployment (INV-12). When autoscaling is enabled, the Deployment's
  `replicas` field is omitted, so `helm upgrade` does not reset the autoscaler's choice.
- **Disruption budget.** `policy/v1` PodDisruptionBudget with `maxUnavailable: 1` and
  `unhealthyPodEvictionPolicy: AlwaysAllow` (R-HA-008). `maxUnavailable` scales with the replica
  count, whereas `minAvailable: 1` with one replica would block every drain.
- **Spreading.** Two topology spread constraints, rendered unless replaced
  ([topology spread](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)):
  - `topology.kubernetes.io/zone`, `maxSkew: 1`, `ScheduleAnyway`;
  - `kubernetes.io/hostname`, `maxSkew: 1`, `DoNotSchedule` (server) or `ScheduleAnyway`
    (worker).

  Both use `matchLabelKeys: [pod-template-hash]`, so the skew is computed per ReplicaSet and a
  rollout does not pile new pods onto the nodes the old ones leave. Single-node development
  clusters set `host.whenUnsatisfiable: ScheduleAnyway` (R-HA-051).
- **Node loss.** Tolerations for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable`
  with `tolerationSeconds: server.nodeFailureTolerationSeconds` (60) replace the default of 300 s
  ([taint-based evictions](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#taint-based-evictions)).
  Replicas are stateless, so recreating them early costs nothing.
- **Autoscaling.** `autoscaling/v2` on CPU (default target 70 %), optional memory and custom
  metrics. Scale-down is slow (one pod per 2 minutes after a 5-minute stabilization window), because
  every removed pod drains its uploads. Scaling raises the database connection demand: the budget
  (R-HA-016) uses `maxReplicas`, so it must be raised before `maxReplicas` is.
- **Placement controls.** `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`,
  `schedulerName` are passed through unchanged.

Acceptance criteria:

- On a three-node kind cluster with three zones labeled, three replicas land on three nodes.
  `kubectl drain` of one node keeps two replicas serving at all times.
- With autoscaling enabled, a CPU load test raises the replica count to `maxReplicas`, and a
  following `helm upgrade` with unchanged values does not reset it.

### R-HA-033 · Pod and container security
Priority: **MVP** · Covers: HA-045

- Pods run with the values' `podSecurityContext` and `securityContext`:
  - non-root UID/GID 65532;
  - `readOnlyRootFilesystem: true`;
  - no privilege escalation;
  - all capabilities dropped;
  - seccomp `RuntimeDefault`.

  This satisfies the Kubernetes *restricted* Pod Security Standard
  ([Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)).
  The namespace can therefore enforce `pod-security.kubernetes.io/enforce: restricted`.
- Writable paths are only the spool (`/var/lib/trove/spool`) and `/tmp` (an `emptyDir` with
  `tmp.sizeLimit`). `TMPDIR` points to `/tmp`.
- The image is a minimal non-root base with CA certificates and time-zone data, no shell and no
  package manager. Debugging uses ephemeral debug containers. `trove admin` subcommands run with
  `kubectl exec` (no shell needed).
- `automountServiceAccountToken: false`: trove never calls the Kubernetes API. Workload identity
  for S3 does not depend on the token mount. Both the IRSA webhook and EKS Pod Identity inject their
  own projected token volume.
- No RBAC Role or RoleBinding is rendered.
- OpenShift: the `restricted-v2` security context constraint assigns UIDs from a namespace range.
  There, `runAsUser`, `runAsGroup` and `fsGroup` are set to `null`, and everything else stays.
  The image works with an arbitrary UID in group 0 for its files.
- `extraObjects`, `initContainers` and `sidecars` may run with their own security contexts. The
  chart does not weaken trove's container for them.

Acceptance criteria:

- `kubectl label ns trove pod-security.kubernetes.io/enforce=restricted` followed by
  `helm install` succeeds, and all pods start.
- `kubectl exec <pod> -- touch /x` fails with a read-only file system error.

### R-HA-034 · Probes
Priority: **MVP** · Covers: HA-046

| Probe | Endpoint (R-OPS-001) | Period / timeout / threshold | Rationale |
|---|---|---|---|
| startup | `GET :8081/healthz` | 2 s / 2 s / 60 | Holds off liveness until the listeners are bound. Startup work that takes longer (waiting for the schema) keeps the process alive and reports not ready instead |
| liveness | `GET :8081/healthz` | 10 s / 3 s / 6 | One minute of a stuck process before a restart; never tied to dependencies (R-HA-008) |
| readiness | `GET :8081/readyz` | 5 s / 3 s / 1 | The process debounces its checks (`health.failure_threshold`), so the probe removes a pod as soon as the process says so, which matters most for the drain |

- Each probe can be disabled or replaced (`probes.<name>.custom`).
- Probes use plain HTTP on the operations port. The port is not exposed through Ingress or Gateway
  (R-HA-037), and NetworkPolicy restricts it (R-HA-042).

Acceptance criteria: the probe settings render as in the table. R-HA-008's tests pass with them.

### R-HA-035 · Spool and temporary volumes
Priority: **MVP** · Covers: HA-005

- The spool (R-STOR-004) is an `emptyDir` at `/var/lib/trove/spool` with `sizeLimit:
  spool.sizeLimit`. With `spool.type: ephemeral` it is a generic ephemeral volume (a per-pod
  PersistentVolumeClaim deleted with the pod) of `spool.ephemeral.size`
  ([ephemeral volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/)), for nodes
  whose local disk is too small.
- The kubelet evicts a pod whose `emptyDir` exceeds its `sizeLimit`. The process must reject uploads
  before that point. The chart therefore sets `storage.spool.max_bytes = spool.sizeLimit −
  spool.headroom` unless `config.storage.spool.max_bytes` is set. R-STOR-004's admission keeps the
  spool below its budget, so eviction never happens through uploads.
- Sizing: `spool.sizeLimit ≥ concurrent spooled uploads per pod × p95 upload size + headroom`, and
  at least `storage.upload.max_size + headroom` (validated, R-HA-029). Remote-cache fills use the
  spool too (R-STOR-004). Example: 20 concurrent uploads of 500 MB, plus 2 GiB, needs about 12 GiB.
- `resources.requests.ephemeral-storage` should cover the expected steady use, not the limit, so
  pods schedule on ordinary nodes. The `sizeLimit` caps the worst case.
- `medium: Memory` is allowed but counts against the memory limit. It is meant for small,
  latency-critical installations only.

Acceptance criteria:

- With `spool.sizeLimit: 3Gi` and `headroom: 1GiB`, 3 concurrent 1 GiB uploads: two succeed and the
  third receives `503` with `Retry-After` (or succeeds after a retry). The pod is never evicted
  (`kubectl get events` shows no eviction).
- `helm template … --set spool.sizeLimit=5Gi --set config.storage.upload.max_size=10GiB` fails.

### R-HA-036 · Resources and Go runtime limits
Priority: **MVP** · Covers: HA-052

- Defaults: requests 1 CPU and 1 GiB memory, memory limit 2 GiB, no CPU limit (server). Workers:
  500m and 512 MiB, memory limit 1 GiB. Requests are set, never left empty, so the scheduler and
  the autoscaler have a basis.
- **CPU.** Since Go 1.25 the runtime sets `GOMAXPROCS` from the cgroup CPU *limit* and ignores
  requests ([Go 1.25 release notes](https://go.dev/doc/go1.25#container-aware-gomaxprocs)).
  Without a CPU limit it would use every CPU of the node. When `resources.limits.cpu` is unset, the
  chart therefore sets `GOMAXPROCS` from `requests.cpu` through the downward API
  (`resourceFieldRef`, divisor 1, rounded up). When a CPU limit is set, the chart sets nothing and
  the runtime's container-aware default applies.
- **Memory.** The chart passes `limits.memory` as `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` (downward API).
  At startup, unless `GOMEMLIMIT` is set explicitly, the process calls `debug.SetMemoryLimit` with
  `runtime.memory_limit_ratio` × that value ([SetMemoryLimit](https://pkg.go.dev/runtime/debug#SetMemoryLimit)).
  The garbage collector then works harder near the limit instead of the container being OOM-killed.
  The rest is headroom for non-heap memory.
- Buffers are bounded so that memory does not scale with transfer size: streaming bodies, spool
  instead of memory above `storage.spool.memory_threshold`, bounded metadata documents (format pages
  set limits for npm packuments and NuGet registration pages).
- Sizing profiles live in `deploy/helm/trove/profiles/{small,medium,large}.yaml` (R-HA-050). They
  are layered before the operator's values file: `helm upgrade -f profiles/medium.yaml -f my-values.yaml`.

Acceptance criteria:

- With requests 2 CPU and no limit on a 16-CPU node, `GET /api/v1/system/info` reports
  `gomaxprocs: 2`. With a limit of 3, it reports 3.
- With a 1 GiB memory limit, a load test that holds 200 concurrent large-packument requests ends
  without an OOM kill, and `go_gc_cycles_total` rises instead.

### R-HA-037 · Service, Ingress and TLS at the edge
Priority: **MVP** · Covers: HA-012, HA-022, HA-038, HA-039, HA-042

- **One entry point (HA-012).** All client traffic (UI, API, package protocols, raw files, login)
  uses one port and one Service (`<fullname>:8080`). The Ingress has one rule per host, path `/`,
  `pathType: Prefix`, to that Service. There is no second route that bypasses anything, because no
  internal component sits behind the replica. The operations port is never routed by the Ingress or
  the Gateway.
- **The whole host, never a sub-path.** trove owns the root of every host it is served on.
  Clients fix some routes at the host root (npm's `/-/npm/v1/` exchange prefix,
  [formats/npm.md](formats/npm.md) Q-NPM-1; `/v2/` for OCI, Q-HA-4), which INV-6 reserves next to
  `/auth/` and `/ui/`; the session cookie carries the `__Host-` prefix with `Path=/`; and
  `server.base_url` is an origin without a path
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-005). The Ingress path
  and the HTTPRoute match (`PathPrefix /`, R-HA-038) are therefore fixed and not values.
  `values.schema.json` declares `ingress` and `gateway` with `additionalProperties: false`, so
  `--set ingress.path=/trove` fails schema validation before anything renders (R-HA-029).
- **Load balancer (HA-022).** An Ingress controller or a Gateway API implementation (R-HA-038) is
  the load balancer. The Service is `ClusterIP` by default. `service.type: LoadBalancer` is allowed
  for clusters without either, with TLS terminated at the cloud load balancer through its
  annotations. Plain HTTP to the internet is not a supported production setup.
- **TLS (HA-042).** TLS terminates at the edge: an Ingress TLS Secret, cert-manager through an
  annotation (`cert-manager.io/cluster-issuer`, [cert-manager Ingress](https://cert-manager.io/docs/usage/ingress/)),
  a Gateway listener, or a cloud load balancer. Traffic from the edge to the pods is plain HTTP
  inside the cluster (H-38). trove has no TLS listener in the MVP. Internal TLS is Deferred
  (HA-018). A service mesh with mutual TLS (Istio, Linkerd) or CNI encryption works transparently
  and closes H-38 at the platform level.
- `ingress.hosts` defaults to the host of `config.server.base_url`, so the URL clients use and the
  URL trove writes into metadata cannot diverge.
- ingress-nginx, the most common controller, was retired by the Kubernetes project in March 2026
  and receives no further fixes
  ([retirement announcement](https://kubernetes.io/blog/2025/11/11/ingress-nginx-retirement/)).
  The chart's Ingress is controller-neutral. The chart renders an HTTPRoute from the MVP on
  (R-HA-038, Q-HA-7), and new installations should prefer it.

Acceptance criteria:

- With `ingress.enabled: true`, `className: traefik` and a cert-manager issuer, `curl -sI https://trove.example.com/api/v1/system/ping`
  returns `200` with a certificate for the host.
- `curl -s https://trove.example.com:8081/metrics` and any Ingress path to the operations port
  fail. The Ingress manifest contains no rule for port 8081.
- `helm template … --set ingress.enabled=true --set ingress.path=/trove` fails schema validation
  and names `path`. Every rendered Ingress rule has path `/` and `pathType: Prefix`.

### R-HA-038 · Gateway API exposure
Priority: **MVP** · Covers: HA-040

- With `gateway.enabled: true` the chart renders an `HTTPRoute` (`gateway.networking.k8s.io/v1`).
  It attaches to `gateway.parentRefs`, with `hostnames` defaulting to the base URL's host (plus the
  content host of R-HA-055 when set), one rule with `PathPrefix /` to the Service (fixed, not a
  value, R-HA-037), and `timeouts.request` / `timeouts.backendRequest` from the values. HTTPRoute timeouts are part of the Standard channel since Gateway API v1.2
  ([GEP-1742](https://gateway-api.sigs.k8s.io/geps/gep-1742/)), so v1.2 CRDs are the minimum.
  `backendRequest` MUST NOT exceed `request`.
- The HTTPRoute is MVP (Q-HA-7). It attaches to an existing, platform-owned Gateway. A Gateway
  rendered by the chart is P1 (R-HA-053).
- Ingress and HTTPRoute may be enabled at the same time, for migrating between them with a DNS
  switch and an instant way back.
- Body size, request buffering and slash handling are not part of the Gateway API. They are set
  through implementation-specific policies (R-HA-039) that the chart does not render. `extraObjects`
  can carry them.

Acceptance criteria:

- With Envoy Gateway in kind and the policies of R-HA-039, `npm publish` of a 200 MB package and
  `npm view @acme/ui` (scoped name, encoded slash) work through the HTTPRoute.
- Rendering with `gateway.enabled: true` and no Gateway API in the cluster fails before anything is
  created (R-HA-029).

### R-HA-053 · Chart-created Gateway
Priority: **P1** · Covers: HA-040

- `gateway.create.enabled: true` renders a Gateway named `<fullname>-gateway` with
  `gateway.create.gatewayClassName` and `gateway.create.listeners`, and attaches the HTTPRoute of
  R-HA-038 to it. The suffix avoids clashes with Services that some implementations create. It is
  meant for single-tenant clusters; shared clusters attach to a platform-owned Gateway.
- The chart does not create a TLS Secret for the listener. Certificates come from an existing Secret
  or from cert-manager's Gateway support.

Acceptance criteria: with Envoy Gateway in kind and `gateway.create.enabled: true`, R-HA-038's
criteria pass through the chart-created Gateway, and `helm uninstall` removes it.

### R-HA-039 · Proxy requirements for large and long transfers
Priority: **MVP** · Covers: HA-024

Every hop in front of trove (cloud load balancer, Ingress controller, Gateway, corporate reverse
proxy) MUST be configured as follows. The defaults of common controllers violate most rows, and
the failures appear only later.

| Requirement | Value | Failure if violated |
|---|---|---|
| Request body size | Unlimited, or ≥ `storage.upload.max_size` | `413` on large publishes |
| Request buffering | Off | The proxy spools the upload to its own disk and forwards it late; clients time out, and `Expect: 100-continue` rejection (R-HA-010) stops working |
| Response buffering | Off | Large downloads are written to the proxy's disk first |
| Read/send timeouts | ≥ 900 s (≥ 3600 s for multi-GB artifacts on slow links) | Long uploads and downloads cut at 60 s |
| Encoded slashes | Passed through unchanged (`%2f`, `%2F`) | Scoped npm packages (`@scope%2fname`) fail ([formats/npm.md](formats/npm.md)) |
| Path normalization, slash merging | Off | Same |
| Response caching | Off | Stale metadata, broken publishes; caching is trove's job (remote repositories) |
| Upstream protocol | HTTP/1.1 with keep-alive | Connection churn |
| Forwarded headers | `X-Forwarded-For`, `X-Forwarded-Proto` set by the last hop, from a CIDR listed in `server.trusted_proxies` (R-OPS-005) | Wrong client IP in the audit log |

Examples for common controllers (verified 2026-10-28 against their documentation; the controller's
documentation wins):

<details>
<summary>ingress-nginx (retired; for existing installations only)</summary>

```yaml
ingress:
  className: nginx                                            # example
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "0"          # example: unlimited
    nginx.ingress.kubernetes.io/proxy-request-buffering: "off"   # example
    nginx.ingress.kubernetes.io/proxy-buffering: "off"           # example
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"       # example
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"       # example
```

</details>

<details>
<summary>Traefik</summary>

Traefik's entry points limit the time to read a whole request, including its body, to 60 s by
default (`transport.respondingTimeouts.readTimeout`,
[entry points](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/)). The
setting applies per entry point, not per route:

```yaml
# Traefik static configuration (Helm values of the Traefik chart)
ports:
  websecure:
    transport:
      respondingTimeouts:
        readTimeout: 3600s    # example
        writeTimeout: 0s      # example (Traefik default)
        idleTimeout: 180s     # example (Traefik default)
```

Traefik does not limit the body size and does not buffer unless a buffering middleware is attached.

</details>

<details>
<summary>Envoy Gateway (Gateway API)</summary>

Envoy Gateway unescapes encoded slashes and redirects by default, which breaks `@scope%2fname`.
Keep them unchanged with a `ClientTrafficPolicy` on the Gateway, created in the Gateway's
namespace ([Envoy Gateway](https://gateway.envoyproxy.io/)):

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: ClientTrafficPolicy
metadata:
  name: trove-paths               # example
  namespace: gateway-system         # example: namespace of the Gateway
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: shared                  # example
  path:
    escapedSlashesAction: KeepUnchanged   # example
    disableMergeSlashes: true             # example
```

Route timeouts come from the HTTPRoute (R-HA-038).

</details>

Acceptance criteria (run through the chart's Ingress and through the HTTPRoute in CI):

- `curl -T 6gib.bin https://trove.example.com/files/generic-local/big.bin` succeeds. 6 GiB is
  above the 5 GiB single-request limit of S3, so the multipart path is covered as well.
- `curl --limit-rate 2M -o /dev/null https://trove.example.com/files/generic-local/big.bin` runs
  longer than 15 minutes and completes.
- `curl -s https://trove.example.com/api/npm/npm-all/@acme%2fui` returns the packument.
- `dotnet nuget push big.nupkg --source …` of a 1 GiB package succeeds.

### R-HA-040 · Public base URL and forwarded headers in the chart
Priority: **MVP** · Covers: HA-025

R-OPS-005 specifies URL generation and the trust rules. The chart side:

- `config.server.base_url` is required (R-HA-029). The Ingress host and the HTTPRoute host name
  default to its host (R-HA-037), so the published name and the generated URLs agree.
- `config.server.trusted_proxies` SHOULD list exactly the pod CIDR (or the node CIDR with
  `externalTrafficPolicy: Local`) of the edge proxy. A broader list lets any pod in the cluster set
  the client address that the audit log records (Security notes).
- The edge proxy does not need to forward `Host` or `X-Forwarded-Host` for trove to generate
  correct URLs, because they come from `base_url`. This removes the catalog's class of
  "reads work, multi-step writes fail" errors caused by rewritten `Host` headers.

Acceptance criteria: R-OPS-005's criteria pass through the chart's Ingress with
`trusted_proxies` set to the ingress controller's pod CIDR.

### R-HA-055 · Optional content host at the edge
Priority: **P1** · Covers: trove-only

[architecture.md](architecture.md) R-ARCH-029 specifies the optional content origin: package
content a browser could render is served from a second host name that never receives the UI's
session cookie (Q-ARCH-10). This requirement is the chart side.

- `contentOrigin.host` names the content host. When it is set, the chart:
  - renders `server.content_base_url: https://<contentOrigin.host>` into `trove.yaml`, a
    chart-managed key (R-HA-030), so the published host and the URLs trove generates cannot
    diverge, as for `base_url` (R-HA-040);
  - adds a rule for that host to the Ingress (path `/`, `pathType: Prefix`, R-HA-037) and adds the
    host to the HTTPRoute's `hostnames` (R-HA-038). Both route to the same Service
    `<fullname>:8080`. There is no second Service, port or Deployment: the replica tells the hosts
    apart (R-ARCH-029, with the host resolved by R-ARCH-006);
  - with `contentOrigin.tls.secretName`, adds a TLS entry of its own to the Ingress
    (`{secretName: <contentOrigin.tls.secretName>, hosts: [<contentOrigin.host>]}`), so the content
    host has its own certificate; with cert-manager's Ingress annotation (R-HA-037), cert-manager
    issues it. With a chart-created Gateway (R-HA-053), it adds an HTTPS listener `content` on port
    443 for that host with that certificate. On a platform-owned Gateway, a listener of the
    platform must cover the host.
- Host rules, checked before anything is applied:
  - `values.schema.json` accepts only a DNS host name for `contentOrigin.host`, without scheme,
    port or path. A content origin on the UI's host with another port, which R-ARCH-029 refuses
    because browsers do not isolate cookies by port, therefore cannot be configured.
  - Rendering fails when `contentOrigin.host` equals the host of `config.server.base_url` or of a
    `config.server.additional_base_urls` entry (R-HA-029).
  - A subdomain of the UI's registrable domain renders, and the replica logs R-ARCH-029's warning at
    start-up. A separate registrable domain, such as `trove-content.example.net`, is recommended.
  - The start-up checks of R-ARCH-029 stay the second line, also for installations that run the
    binary without the chart.
- The edge requirements of R-HA-039 apply to the content host unchanged. NetworkPolicies
  (R-HA-042) need no change, because the traffic reaches the same port.
- With `contentOrigin.host` empty (default) nothing extra is rendered: one origin with the content
  headers of R-ARCH-007.

Acceptance criteria:

- `helm template … --set ingress.enabled=true --set contentOrigin.host=trove-content.example.net --set contentOrigin.tls.secretName=trove-content-tls`
  renders one Ingress with two rules, both to `<fullname>:8080`, a TLS entry with
  `trove-content-tls` for the content host, and `server.content_base_url: https://trove-content.example.net`
  in `trove.yaml`. With `gateway.enabled: true`, the HTTPRoute lists both host names.
- With `config.server.base_url: https://trove.example.com`, `--set contentOrigin.host=trove.example.com`
  fails and names `contentOrigin.host`; `--set contentOrigin.host=trove.example.com:8443` fails
  schema validation; `--set config.server.content_base_url=https://x.example.net` fails as a
  chart-managed key.
- R-ARCH-029's acceptance criteria pass through the chart's Ingress with Traefik and a cert-manager
  issuer, as in R-HA-037's criteria.

### R-HA-041 · Container-registry routing at the edge
Priority: **P1** · Covers: HA-027

Docker and OCI clients address a registry at the host root (`https://<host>/v2/…`). A path prefix
is not possible, so the OCI format needs `/v2/` on the client listener. INV-6 reserves `/v2/` for
the OCI distribution API (Q-HA-4): no other route may use it, from the MVP on, so the OCI format
can be added later without moving anything.

- **The reservation is MVP; the routing below is P1.** Until the OCI format exists, `/v2/` and
  everything below it answer `404` through the listener's "anything else" row
  ([architecture.md](architecture.md) R-ARCH-005). A route-table test fails when any registered
  client-listener pattern other than the OCI format's starts with `/v2/`. ServeMux's own conflict
  check is not enough, because a more specific pattern such as `/v2/x` does not conflict with
  `/v2/`.
- The chart routes the whole host to trove (R-HA-037). `/v2/` reaches trove without rewrite
  rules. The repository is the first path segment after `/v2/`
  (`docker pull trove.example.com/docker-all/library/alpine:3`). The OCI format page specifies
  this.
- Subdomain-per-repository routing (`docker-all.trove.example.com`) needs a wildcard host and a
  wildcard certificate. The chart supports it by adding `*.<base host>` to `ingress.hosts` and
  `gateway.hostnames` when `config.oci.subdomainRouting` is enabled. Mapping the host to a
  repository is the OCI page's.
- The chart renders no rewrite rules or snippets. This avoids the catalog's mismatch between edge
  rewrites and the server's routing method, where pushes break while pulls work.

Acceptance criteria (when the OCI format exists):
`docker login trove.example.com && docker push trove.example.com/docker-local/test:1` through
the chart's Ingress succeeds without any Ingress annotation beyond R-HA-039's.

### R-HA-042 · Network policies
Priority: **MVP** · Covers: HA-054

With `networkPolicy.enabled: true` (default) the chart renders, for all trove pods:

- **Ingress.**
  - Port 8080 from `networkPolicy.ingress.http.from`; empty means from anywhere, so the ingress
    controller works wherever it runs.
  - Port 8081 only from `networkPolicy.ingress.ops.from` (default: namespace `monitoring`) and
    `probeCIDRs`.
  - Nothing else; replicas do not talk to each other.
- **Egress** (only with `networkPolicy.egress.enabled: true`):
  - DNS;
  - PostgreSQL (`database.to`, port 5432);
  - object storage (`objectStorage.to`);
  - optionally the EKS Pod Identity agent (`169.254.170.23:80`);
  - optionally the internet on 443/80 except private and link-local ranges (upstream registries,
    OIDC issuers, STS);
  - `extra` rules.

  This is the second layer behind the egress address policy of R-REPO-038.
- The migration Job gets its own policy: egress to DNS and PostgreSQL only.
- Kubelet probes: most CNIs exempt node-to-pod probe traffic from policies. Where one does not,
  `probeCIDRs` adds the node CIDRs to the 8081 rule.

Egress stays off by default (Q-HA-10). A wrong guess about where PostgreSQL and S3 live breaks the
installation, while a missing egress policy only removes the second layer. To keep the choice
visible:

- `NOTES.txt` states, when `networkPolicy.egress.enabled` is false, that egress from the pods is
  unrestricted, and points to the production example.
- `deploy/helm/trove/examples/egress.yaml` is a values file with an egress block for a typical
  production setup (CloudNativePG in the namespace, AWS S3, internet upstreams). Every value in it is
  marked `# example`, and a CI value set renders it.

Acceptance criteria:

- From a pod in namespace `default`, `curl http://trove-ops.trove:8081/metrics` times out, and
  `curl http://trove.trove:8080/api/v1/system/ping` succeeds.
- With egress enabled and configured for the local profile, all R-HA-052 scenarios pass, and
  a connection from a trove pod's network namespace to `169.254.169.254:80` (`kubectl debug -it <pod>
  --image=<pinned curl image> --target=trove -- curl -m 5 http://169.254.169.254/`) times out.

### R-HA-043 · Trusted CA bundles
Priority: **MVP** · Covers: HA-019

- `trustedCA.existingSecret` or `trustedCA.existingConfigMap` (for example a trust-manager `Bundle`
  target, [trust-manager](https://cert-manager.io/docs/trust/trust-manager/)) is mounted at
  `/etc/trove/ca/`. Its files populate `proxy.outbound.tls.ca_files` (R-REPO-035). They are added to the system
  roots for every outbound TLS connection: upstreams, OIDC issuers, GitLab, webhooks. They are
  reloaded within 60 s without a restart (R-REPO-035).
- With `trustedCA.useForObjectStore: true` the same bundle becomes `storage.s3.ca_file`, for an
  S3-compatible store with a private CA.
- PostgreSQL uses its own CA (`secrets.databaseCA`), never the general bundle. The database
  connection then trusts exactly the database's CA.
- Every replica mounts the same source, so there is no per-node trust store to keep in sync
  (catalog HA-006, HA-019).

Acceptance criteria: R-REPO-035's criterion passes with the bundle supplied through
`trustedCA.existingConfigMap`, and an S3-compatible store with a private CA (the local profile's
store behind a TLS endpoint) works with `useForObjectStore: true`.

### R-HA-044 · IPv6 and dual-stack
Priority: **P1** · Covers: HA-020

- Listeners bind to `[::]` and accept IPv4 and IPv6. The binary contains no IPv4-only assumption
  (address parsing, CIDR lists, `X-Forwarded-For`, log fields).
- `service.ipFamilyPolicy` and `service.ipFamilies` pass through to both Services
  ([dual-stack](https://kubernetes.io/docs/concepts/services-networking/dual-stack/)).
- `server.trusted_proxies`, `proxy.outbound.address_policy` and NetworkPolicy CIDR values accept IPv6 notation.
- Host names are used everywhere a URL is configured. IPv6 literals in `server.base_url` are allowed
  in bracket notation, but they are not recommended because certificates rarely cover them.

Acceptance criteria: on a dual-stack kind cluster (`networking.ipFamily: dual`) with
`PreferDualStack`, `curl -6` and `curl -4` against the Service both succeed. The audit log records
the IPv6 client address of the `curl -6` request.

### R-HA-045 · Monitoring integration
Priority: **P1** · Covers: HA-055

- **Logs.** trove writes JSON lines to stdout only (INV-16). The cluster's log collector ships them.
  The chart renders no logging sidecars. A sidecar that needs to (e.g. a SIEM forwarder) goes into
  `sidecars`.
- **ServiceMonitor.** It scrapes the `ops` port of `<fullname>-ops`, every pod, path `/metrics`
  (R-OPS-006). When `config.metrics.auth: static`, the bearer token comes from
  `secrets.metricsToken`.
- **PrometheusRule.** Default alerts; names are stable and individually disabled with
  `disabledAlerts`:

| Alert | Expression (sketch) | For |
|---|---|---|
| `TroveReplicasNotReady` | `sum(kube_deployment_status_replicas_available{deployment=~"trove.*"}) < 2` (needs kube-state-metrics) | 5m |
| `TroveDatabaseUnreachable` | `sum(rate(trove_db_retries_total{kind="connection"}[5m])) > 0 and sum(rate(trove_http_requests_total{code="503"}[5m])) > 0` | 2m |
| `TroveObjectStoreUnreachable` | `min(trove_storage_s3_circuit_open) == 1` (all replicas) | 2m |
| `TroveObjectStoreUnreachableSingleReplica` | `max(trove_storage_s3_circuit_open) == 1 and min(trove_storage_s3_circuit_open) == 0` | 5m |
| `TroveDatabasePoolSaturated` | `histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket[5m])) > 0.1` | 10m |
| `TroveJobsStuck` | `max(trove_jobs_oldest_queued_age_seconds) > 900` | 5m |
| `TroveLeaseLost` | `increase(trove_lease_lost_total[15m]) > 3` | 0m |
| `TroveMixedVersions` | `count(count by (version) (trove_build_info)) > 1` | 2h |
| `TroveSpoolNearlyFull` | `max(trove_spool_used_bytes / (trove_spool_used_bytes + trove_spool_free_bytes)) > 0.9` | 10m |
| `TroveBlobMissing` | `increase(trove_storage_blobs_damaged_total{status="missing"}[1h]) > 0` | 0m |
| `TroveGCStale` | `time() - max(trove_gc_last_success_timestamp_seconds) > 172800` | 1h |
| `TroveBackupCheckFailed` | `min(trove_backup_check_ok) == 0` | 1h |
| `TroveRevocationJournalBacklog` | `max(trove_revocation_journal_pending) > 0` (R-HA-054) | 15m |

Metric names come from [operations-and-observability.md](operations-and-observability.md) R-OPS-008
and [storage.md](storage.md); `trove_storage_blobs_damaged_total{status}` is storage.md's
(R-STOR-027), counted by every detection of a missing or corrupt blob. This page adds
`trove_db_retries_total`, `trove_db_pool_resets_total`, `trove_db_commit_unknown_total`,
`trove_lease_lost_total`, `trove_backup_check_ok` and `trove_revocation_journal_pending`.
Expressions are sketches; the rule file is tested with `promtool test rules` in CI.

Acceptance criteria: `promtool check rules` and `promtool test rules` pass. With the S3 store
stopped in the local profile, `TroveObjectStoreUnreachable` fires in a Prometheus scraping through
the ServiceMonitor.

### R-HA-046 · Extension points
Priority: **P1** · Covers: HA-048, HA-049, HA-050, HA-051

- `extraEnv` and `extraEnvFrom` (all trove containers), `server.extraEnv` and `worker.extraEnv`
  (per role), `extraVolumes` and `extraVolumeMounts` (trove container), `initContainers`,
  `sidecars` (Kubernetes native sidecars with `restartPolicy: Always` are allowed), `lifecycle`
  and `extraObjects`.
- All of them are rendered with `tpl`, so they can reference values and release names.
- `extraEnv` entries may override chart-set variables (INV-15 precedence). This is the escape
  hatch, and it is documented as such.
- `lifecycle` defaults to empty. A custom `preStop` adds to the drain delay, so the grace-period
  validation (R-HA-029) counts it only if the operator raises `terminationGracePeriodSeconds`
  accordingly.
- Extension containers do not change trove's own security context (R-HA-033).

Acceptance criteria: a CI value set with one of each renders, passes kubeconform, and the pod starts
in kind with an init container that writes a file the sidecar reads.

### R-HA-047 · Images, private registries, air-gapped installation and signatures
Priority: **P1** · Covers: HA-056, OPS-076

- Images are referenced as `<global.imageRegistry or image.registry>/<image.repository>` with
  `@<digest>` when set, otherwise `:<tag or appVersion>`. `global.imagePullSecrets` and
  `imagePullSecrets` apply to all pods, including the hook Jobs.
- Images are multi-architecture (`linux/amd64`, `linux/arm64`).
- **Publishing location (Q-HA-11).** The repository is hosted on GitHub (`origin` is
  `github.com/guided-traffic/trove`), so the release workflow publishes to the GitHub Container
  Registry of that organization and nowhere else:
  - image `ghcr.io/guided-traffic/trove`;
  - chart `oci://ghcr.io/guided-traffic/charts/trove`
    ([Helm OCI registries](https://helm.sh/docs/topics/registries/)):
    `helm upgrade --install trove oci://ghcr.io/guided-traffic/charts/trove --version 0.4.0`
    (version is an example).

  Both packages are public, so anonymous pulls work. Mirrors are the operator's (air-gapped,
  below).
- **Signatures and provenance.** Every image and every chart artifact is signed with Sigstore cosign
  in keyless mode from the release workflow's GitHub Actions OIDC identity
  ([Sigstore](https://docs.sigstore.dev/)). Each carries an SPDX SBOM and a SLSA provenance
  attestation ([SLSA](https://slsa.dev/)). Verification:

  ```shell
  cosign verify ghcr.io/guided-traffic/trove@sha256:<digest> \
    --certificate-identity-regexp '^https://github\.com/guided-traffic/trove/\.github/workflows/release\.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```

  A cluster policy engine (for example a Kyverno image verification policy) can enforce this at
  pod creation.
- **Air-gapped.** Mirror the chart (`helm pull oci://… --version`) and the image by digest
  (`crane copy`, `skopeo copy --all`) into the internal registry, then set `global.imageRegistry`.
  The chart pulls no other image. No init container image, no bundled database, no proxy image.
  Signatures and attestations are copied with `cosign copy`.

Acceptance criteria:

- `helm template … | grep -E 'image:' | sort -u` lists exactly one image.
- `cosign verify` of a release image succeeds with the documented identity.
- An installation from a mirror with `global.imageRegistry: registry.internal.example` pulls
  nothing from the public registry (verified with a deny-all egress policy on the nodes' public
  registry access in the test).

### R-HA-048 · Uninstall and data removal
Priority: **MVP** · Covers: HA-071

- `helm uninstall trove -n trove` removes every release-managed resource. The chart renders no
  StatefulSet, no PersistentVolumeClaim and no resource with `helm.sh/resource-policy: keep`, so
  uninstall leaves no claim behind that would have to be either kept or deleted by label (HA-071).
  - With `spool.type: emptyDir` (default) there is no claim at all.
  - With `spool.type: ephemeral` each pod's claim is a generic ephemeral volume owned by its pod.
    Kubernetes deletes the claim with the pod
    ([generic ephemeral volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes)).
    The claim template carries the release's labels, so the claims are visible while pods run.
    Whether the underlying volume is deleted follows the storage class's reclaim policy.
    `spool.ephemeral.storageClassName` SHOULD name a class with `reclaimPolicy: Delete`; with
    `Retain`, released volumes holding transient upload data accumulate and must be deleted by
    hand.
- Hook resources are not part of the release
  ([chart hooks](https://helm.sh/docs/topics/charts_hooks/)). The migration Job deletes itself
  through `ttlSecondsAfterFinished`. The hook ConfigMap and ServiceAccount are removed with
  `kubectl -n trove delete configmap,serviceaccount,job -l app.kubernetes.io/instance=trove,app.kubernetes.io/component=migrate`
  (printed by `NOTES.txt` and by `helm uninstall`'s documentation).
- **Data removal is separate, manual and irreversible.** Uninstall touches neither the database nor
  the bucket nor the Secrets. Removing data:
  1. `DROP DATABASE trove;` and the roles of R-HA-014;
  2. empty the bucket including all object versions (a lifecycle rule that expires current and
     noncurrent versions, or the store's tooling), then delete it;
  3. delete the PostgreSQL backups;
  4. last, the encryption key Secret.

  Deleting the keys first would make the database backups' sealed secrets undecryptable while the
  backups still exist.

Acceptance criteria:

- After `helm uninstall` and the documented `kubectl delete`,
  `kubectl get all,cm,sa,pdb,hpa,ingress,httproute,networkpolicy -n trove -l app.kubernetes.io/instance=trove`
  returns nothing, and the database and bucket are unchanged.
- With `spool.type: ephemeral`, `kubectl get pvc -n trove -l app.kubernetes.io/instance=trove`
  lists one claim per pod before the uninstall and nothing within 2 minutes after it.

## Platform, sizing and environments

### R-HA-049 · Platform prerequisites and network locality
Priority: **MVP** · Covers: HA-068, HA-008

| Requirement | Value |
|---|---|
| Kubernetes | 1.34 to 1.37 tested (the range Helm 4.3 supports, verified 2026-10-28 on [Helm version skew](https://helm.sh/docs/topics/version_skew/)). The chart uses no API newer than 1.31 (PDB `unhealthyPodEvictionPolicy`), so 1.31–1.33 are expected to work untested |
| Helm | 4.x. Helm 3 (latest 3.x) is supported until its security support ends on 2027-02-10 ([Helm 3 end of life](https://helm.sh/blog/helm-v3-end-of-life)) |
| Gateway API (optional) | CRDs v1.2 or later, Standard channel |
| Prometheus Operator CRDs (optional) | `monitoring.coreos.com/v1` for ServiceMonitor and PrometheusRule |
| PostgreSQL | 16+ (R-HA-013), highly available (CloudNativePG, Patroni, a managed service) |
| Object storage | S3 API meeting R-STOR-035, versioning recommended (R-HA-023) |
| Node architectures | `amd64`, `arm64` |
| Ephemeral storage | Nodes with enough local disk for `spool.sizeLimit`, or a StorageClass for `spool.type: ephemeral` |
| OpenShift | 4.x with `restricted-v2` (R-HA-033) |

- **Network locality (HA-008).** Replicas, PostgreSQL and the bucket MUST be in one region. Spreading
  replicas over availability zones of that region is recommended (R-HA-032), with the PostgreSQL
  primary and standbys spread the same way. One cluster stretched across regions is not supported.
  Separate sites use R-HA-026.

Acceptance criteria: the CI matrix installs the chart on kind with Kubernetes 1.34 and 1.37 using
Helm 4 and the latest Helm 3, and runs R-HA-052's smoke subset.

### R-HA-050 · Sizing guidance and profiles
Priority: **MVP** · Covers: HA-067, HA-052

Starting points. They are not measured: trove does not exist yet. The reference load test
(npm and NuGet clients replaying a recorded mix of installs, restores and publishes against remote,
local and virtual repositories) replaces these numbers before 1.0, and CI publishes its results per
release.

| Profile | Typical use | Server replicas (HPA min–max) | Requests CPU / memory | Memory limit | `database.pool.max_conns` | `spool.sizeLimit` | PostgreSQL | `max_connections` (R-HA-016) |
|---|---|---|---|---|---|---|---|---|
| small | ≤ 50 concurrent clients, ≤ 500 GB of blobs | 2 (no HPA) | 500m / 512Mi | 1Gi | 10 | 10Gi | 2 vCPU, 4 GiB, 3 instances | 150 |
| medium | ≤ 300 concurrent clients, CI-heavy | 3 (3–6) | 1 / 1Gi | 2Gi | 20 | 20Gi | 4 vCPU, 16 GiB, 3 instances | 400 |
| large | ≤ 1,000 concurrent clients, several TB | 4 (4–12), role split with 2–4 workers | 2 / 2Gi | 4Gi | 30 | 50Gi (`ephemeral`) | 8 vCPU, 32 GiB, 3 instances | 1000 |

- `max_connections` follows the R-HA-016 formula with `maxReplicas`, `maxSurge: 1`, the default
  lock and lease pools, and the workers where applicable. Example for medium:
  (6 + 1) × (20 + 4 + 16 + 1) + 15 = 302 → 400 with margin. Large: (12 + 4 + 2) × (30 + 4 + 16 + 1)
  + 15 = 933 → 1000.
- CPU goes mostly into TLS-free HTTP streaming, the four hashes per upload (R-STOR-004) and JSON
  metadata rendering. npm proxying of large packuments is the most memory-intensive path. Raise the
  memory limit first when remote npm repositories dominate.
- PostgreSQL storage: metadata only, roughly 1–3 KB per file item including indexes, plus audit
  events (R-OPS-017 retention). 10 million files ≈ 10–30 GB plus audit (estimate).
- S3: content addressing spreads keys over 65,536 two-level prefixes. AWS S3 scales request rates
  per prefix ([performance guidelines](https://docs.aws.amazon.com/AmazonS3/latest/userguide/optimizing-performance.html)),
  so throttling is unlikely below thousands of requests per second.
- Grace period: `terminationGracePeriodSeconds` ≥ p99 upload duration + `shutdown_drain_delay` +
  15 s.

Acceptance criteria: the profile files exist and render (R-HA-029). The reference load test runs
against each profile in CI before 1.0, and this table is updated from its results in the same change.

### R-HA-051 · Local development profile
Priority: **MVP** · Covers: trove-only

A developer brings up a production-shaped installation (two replicas, HA PostgreSQL, S3, the real
chart) on a laptop with one command. Files live in `deploy/dev/`. Tool versions (kind, kubectl,
Helm, CloudNativePG operator chart, PostgreSQL image, SeaweedFS image) are pinned once in the
repository's tool pins and read by the task runner and CI alike. Images are pinned by digest.

| File | Content |
|---|---|
| `deploy/dev/kind.yaml` | One control-plane and two worker nodes, labeled with two zones, so spreading, drains and node loss can be exercised |
| `deploy/dev/postgres.yaml` | CloudNativePG `Cluster` `trove-db`: 2 instances, synchronous replication with `dataDurability: preferred` (a stopped standby does not pause writes on a laptop), the settings of R-HA-013, `bootstrap.initdb` with database `trove` and owner `trove` (single-role mode) |
| `deploy/dev/s3.yaml` | Single-process SeaweedFS Deployment (master, volume server, filer and S3 gateway in one `weed` process) with an `emptyDir`, Service `trove-s3` on port 9000 mapped to the S3 gateway, plus a Job that creates bucket `trove-blobs` with versioning enabled and bucket `trove-pg-archive` for the WAL archive (Q-HA-3) |
| `deploy/dev/values-dev.yaml` | Chart values: 2 replicas, `host.whenUnsatisfiable: ScheduleAnyway`, `spool.sizeLimit: 4Gi`, `forcePathStyle: true`, static S3 credentials, `base_url: http://localhost:8080`, the CloudNativePG Secrets |

Steps behind the task-runner target `dev-up` (target names are an example until the task runner is
chosen):

```shell
kind create cluster --name trove --config deploy/dev/kind.yaml
helm upgrade --install cnpg cloudnative-pg --repo https://cloudnative-pg.github.io/charts \
  -n cnpg-system --create-namespace --version "$CNPG_CHART_VERSION" --wait
kubectl create namespace trove
kubectl -n trove apply -f deploy/dev/postgres.yaml -f deploy/dev/s3.yaml
kubectl -n trove wait --for=condition=Ready cluster/trove-db --timeout=5m
kubectl -n trove create secret generic trove-encryption-keys \
  --from-file=dev.key=<(head -c 32 /dev/urandom | base64)
kubectl -n trove create secret generic trove-break-glass \
  --from-literal=password="$(head -c 24 /dev/urandom | base64)"
helm upgrade --install trove deploy/helm/trove -n trove -f deploy/dev/values-dev.yaml --wait --timeout 10m
kubectl -n trove port-forward svc/trove 8080:8080
```

- The CloudNativePG application Secret `trove-db-app` and the CA Secret of the cluster are used
  directly (`secrets.database`, `secrets.databaseCA`), so the dev profile exercises TLS with
  `verify-full` like production. The host is the cluster's read-write Service
  ([quickstart](https://cloudnative-pg.io/docs/1.30/quickstart), [bootstrap](https://cloudnative-pg.io/docs/1.30/bootstrap)).
- Further targets:
  - `dev-down` deletes the kind cluster;
  - `dev-failover` runs `kubectl cnpg promote trove-db <standby>`;
  - `dev-s3-outage` scales the S3 store to 0 for a given duration;
  - `dev-chaos` runs the R-HA-052 suite.
- **S3 store (Q-HA-3).** The profile and the per-change CI run SeaweedFS
  ([repository](https://github.com/seaweedfs/seaweedfs), Apache-2.0, maintained, images published).
  Its documentation lists bucket versioning, version listing, delete markers and deletes by version
  ID, which the restore drill (R-HA-025) needs
  ([S3 object versioning](https://github.com/seaweedfs/seaweedfs/wiki/S3-Object-Versioning)). It
  also states that version-specific lifecycle rules are not fully implemented; the profile
  therefore relies on the orphan sweep (R-HA-012), not on lifecycle rules. The profile counts as
  valid only while `trove admin storage check` (R-STOR-035) and the restore drill pass on it.
- **MinIO.** The MinIO community repository was archived on 2026-04-25 and is distributed as
  source only, without prebuilt binaries or images ([repository](https://github.com/minio/minio)).
  MinIO stays a supported production target as long as it passes R-STOR-035's conformance check.
  The nightly CI matrix keeps a MinIO job, built from that source at a pinned commit and run only
  in isolated nightly runners, because the build receives no security fixes. AWS S3 is the
  nightly reference store. Its job needs an AWS account and a CI role that the repository does not
  have yet; until the owner provides them, the nightly results list the AWS job as skipped, never
  as passed.
- The break-glass password is printed once by the target (`kubectl get secret … | base64 -d`).
  OIDC is optional in dev. An optional Dex overlay belongs to
  [identity-and-authentication.md](identity-and-authentication.md).

Acceptance criteria:

- On a machine with Docker and the pinned tools, `dev-up` completes in under 10 minutes and
  `npm ping --registry http://localhost:8080/api/npm/npm-all/` succeeds.
- `dev-failover` during an `npm ci` loop causes no failed iteration. `dev-s3-outage 60s` behaves as
  R-HA-010 states.
- CI runs the same profile, so "works in dev" and "works in CI" use one configuration.

### R-HA-052 · Failure-injection test suite
Priority: **MVP** · Covers: trove-only

Every row of R-HA-007 and every HA acceptance criterion on this page runs as an automated scenario
against the local profile (R-HA-051) in CI. Load comes from real clients in parallel loops:

- `npm ci` against a virtual repository (remote and local members);
- `dotnet restore --no-cache` against a NuGet virtual repository;
- `npm publish` of unique versions to a local repository;
- `curl -T` of 200 MB files.

| Scenario | Injection | Pass condition |
|---|---|---|
| Pod crash | `kubectl delete pod --grace-period=0 --force` of a random replica every 2 min for 20 min | No client iteration fails after its own retries; no item row without a durable object (`trove admin storage verify`) |
| Rolling restart | `kubectl rollout restart` | Zero failed iterations |
| Rolling upgrade | Previous minor → current with the migration Job | Zero failed iterations; R-HA-020 verification passes |
| Rollback | `helm rollback` after the upgrade | Zero failed iterations |
| PostgreSQL switchover | `kubectl cnpg promote` | Zero failed iterations |
| PostgreSQL primary loss | Force-delete the primary pod | Failures only within `retry.maxElapsed`, all `503`; recovery without intervention |
| PostgreSQL partition of one replica | NetworkPolicy denying egress to PostgreSQL for one pod | That pod unready within 15 s; no failed iteration overall; its leases move |
| PostgreSQL outage | Scale the database to 0 instances for 2 min (hibernation) | No container restart; full recovery within 30 s of return |
| Object-store outage | The S3 store scaled to 0 for 30 s and for 5 min; during the 5-minute run one replica is deleted | Metadata, UI and API succeed throughout; blob transfers answer `503` with `Retry-After` (R-STOR-036); no running replica turns unready (R-HA-008); the replacement pod started during the outage stays not ready with its container restart count unchanged, because the startup check retries without exiting (R-STOR-035), and becomes ready within 60 s after the store returns; full recovery within `storage.s3.circuit_breaker.open_duration` + 10 s |
| Node drain | `kubectl drain` of a worker node | Zero failed iterations; PDB respected |
| Node loss | `docker stop` of a kind worker node | Replacement pods ready within `nodeFailureTolerationSeconds` + 60 s; no duplicate singleton work (lease table) |
| Listener loss | `pg_terminate_backend` of the `listen` session | Cache flush recorded; next permission change visible on that replica |
| Lease holder loss | Kill the replica holding the GC lease mid-run | Another replica completes the next run; no S3 delete without a committed `deleting` row |
| Spool exhaustion | Concurrent uploads above the budget | `503` with `Retry-After`; no eviction |
| Migration failure | A migration that fails on purpose | Old pods keep serving; `helm upgrade` fails |
| Restore drill | R-HA-025 steps 1–7 | As R-HA-025 |

- The suite records client-side errors per scenario and fails on any deviation from the pass
  condition. Results are published as a CI artifact per run.
- The pod-crash, switchover, rolling-upgrade and object-store scenarios run on every change to `main`.
  The full suite runs nightly, the restore drill monthly.

Acceptance criteria: the suite exists, runs as described, and is green before any release.

## Deferred

| ID | Priority | Item |
|---|---|---|
| HA-011 | P2 | Active-passive warm standby on shared storage: replaced by R-HA-026's replica cluster plus bucket replication |
| HA-013 | Later | Internal service registry with health-gated routing: trove has no internal services |
| HA-014 | P2 | Availability-zone affinity for internal routing: no internal hops; zone-aware Service routing is the platform's |
| HA-018 | P2 | Internal TLS from the edge to the pods (H-38); a TLS listener with a mounted certificate and SANs for the Service name |
| HA-026 | P2 | Reverse-proxy configuration generator: R-HA-039's examples instead |
| HA-028 | P2 | Client-certificate forwarding from the edge proxy |
| HA-034 | P2 | Bundled evaluation database: the local profile installs CloudNativePG instead |
| HA-041 | P2 | Bundled edge reverse proxy: Ingress or Gateway API instead |
| HA-057 | Later | Server-side extension scripts delivered through the chart |
| HA-073 | Later | Message-broker TLS for a scanning component |
| OPS-032 | P2 | Scheduled application-level backups: platform PITR plus bucket versioning instead (R-HA-023) |
| trove-only | P2 | Read-only queries routed to PostgreSQL standbys |
| trove-only | P2 | Worker autoscaling on queue depth (`trove_jobs_oldest_queued_age_seconds` through an external metrics adapter) |
| trove-only | P2 | Split connections: the main pool through a transaction-mode pooler, listener, lease and lock sessions direct (Q-HA-6, option (b)) |
| trove-only | Later | Active-active operation across sites |

Out of scope, listed for completeness: HA-017 (service trust key between internal services; one
binary has none), HA-059 (non-PostgreSQL engines; owner constraint), HA-072 (license
management).

## Security notes

- **Secrets stay in Secrets.** Every credential is a pre-existing Kubernetes Secret, mounted as
  files with only the named keys, never as environment variables. The chart never generates one.
  The database CA Secret is projected down to `ca.crt`, so the CA private key never reaches a
  replica (R-HA-015, R-HA-030).
- **Namespace Secret readers hold the keys to everything.** Anyone who can `get` Secrets in the
  release namespace can read:
  - the database password;
  - the encryption keys, which together with database read access unseal every stored upstream
    credential;
  - the break-glass password.

  Restrict `get`/`list` on Secrets in that namespace to the platform team. Enable encryption at
  rest for Secrets in etcd. Prefer an Argon2id hash for the break-glass Secret (R-AUTH-014).
- **Least privilege in the database.** The runtime role is not a superuser and, in two-role mode,
  has no DDL rights (R-HA-014). It can neither change nor delete audit rows, nor shorten their
  retention, so a compromised replica or a SQL injection cannot erase its traces in the database
  (R-HA-014, R-OPS-015); the owner and superusers still can, which external log shipping covers.
  DDL credentials exist only in the short-lived migration Job pod.
- **Least privilege in the bucket.** The runtime credential cannot delete object versions or change
  versioning, lifecycle or bucket policy. A compromised replica cannot destroy the history that
  restores depend on (R-HA-023). The archive bucket for PostgreSQL backups uses different
  credentials.
- **Operations listener.** Port 8081 serves unauthenticated probes and, by default,
  unauthenticated metrics (R-OPS-006). It is never routed by Ingress or Gateway. The default
  NetworkPolicy limits it to the monitoring namespace (R-HA-042).
- **Client address in the audit log.** It is only as trustworthy as `server.trusted_proxies`. List
  the edge proxy's CIDR, not the whole pod network, and keep the ingress-side NetworkPolicy, so
  that other pods cannot forge `X-Forwarded-For` (R-HA-040).
- **Pod hardening.** Restricted Pod Security Standard, read-only root file system, no service
  account token, no RBAC objects, `enableServiceLinks: false` (R-HA-030, R-HA-033).
- **Supply chain.** Images and chart are signed, with SBOM and provenance, and installed by digest
  in production (R-HA-047).
- **Restore is a security event.** It reverts revocations (H-37), so post-restore records an audit
  event and prints the warning (R-HA-025). From P1 on it re-applies the revocation journal
  (R-HA-054). Replay applies removals and only those updates that grant nothing beyond the
  object's current state, so whoever can write to the bucket can at worst revoke access through the
  journal, never grant any.
- **Egress is unrestricted by default.** Without `networkPolicy.egress.enabled`, a compromised
  trove process can reach every address the pod network reaches, including cloud metadata
  endpoints and in-cluster services. trove's own outbound connections are checked by the address
  policy of R-REPO-038 either way. The egress policy is the second layer for the case where the
  process itself is no longer trustworthy (R-HA-042, Q-HA-10).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-HA-1 · Readiness on object-storage failure

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

This is the same question as Q-STOR-1 ([storage.md](storage.md)) and Q-OPS-2
([operations-and-observability.md](operations-and-observability.md)), seen from the failure-mode
contract. The three pages must agree on one rule; this page's choice is the storage page's
default-plus-opt-in with the first-readiness gate made explicit.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Gate continuously | Readiness fails whenever the `objectStore` check fails, debounced by R-OPS-001's thresholds (INV-12 as written) | Removes a replica whose own S3 path is broken; matches INV-12's wording | A shared outage empties the Service. UI, API, logins, CI token exchange, metadata and deletes go down together with blob traffic, and operators lose the UI they diagnose with |
| (b) Never gate | Readiness reflects the process and PostgreSQL only; S3 health is metrics, alerts and per-request circuit breaking | The degraded service of R-STOR-036 survives a shared outage; simplest rule | A release with a wrong bucket, endpoint, credential or CA becomes ready, replaces every working pod and breaks all blob traffic |
| (c) Gate relatively | Readiness fails only when this replica fails while a majority of the other live replicas pass, compared through `replica.checks` | Handles the shared outage and the single-replica case automatically | Readiness depends on other replicas' heartbeats; stale rows and two-replica ties add states to test; the single-replica case already fails fast per request |
| (d) Gate the first readiness only | A replica becomes ready only after its startup check (R-STOR-035) passed; afterwards S3 failures degrade blob requests one by one and raise alerts; `health.require_object_store: true` restores (a) | Keeps (b)'s degraded service and still stalls a misconfigured rollout; reuses the startup check storage.md already specifies; no cross-replica logic | A replica that loses only its own S3 path stays in the Service and answers blob requests with a fast `503` until its path heals or someone acts on the alert. Pods restarted, rescheduled or added by the autoscaler during a long shared outage cannot become ready, so capacity shrinks until the store returns |
| (e) Gate the first readiness unless the settings are proven | As (d), but a replica whose startup check fails with a transient error may still become ready when a fingerprint of its S3 settings (endpoint, region, bucket, prefix, credential source, CA) matches one that a live replica has verified, as recorded in the replica registry | Pods that start during a long shared outage rejoin, so capacity does not shrink | The fingerprint cannot see credential contents under workload identity, so a broken role or key behind unchanged settings passes, which is the misconfiguration the gate exists for; adds shared state for a rare overlap of a long outage with pod churn |

**Choice:** (d) — object storage gates only a replica's first readiness; after that, failures
degrade blob requests per request and raise alerts, and running replicas stay ready.

**Rationale:** Readiness exists to send traffic to replicas that can serve it. With one shared
bucket, a shared outage leaves no better replica, so (a) only switches off what still works:
metadata from PostgreSQL, the UI, logins, `trv_cit_` exchange, deletes and deduplicated uploads
(R-STOR-036). (b) gets the shared outage right but drops the one case where gating protects
something real: a rollout with a broken S3 configuration. Under (d) the new pods never become
ready, `maxUnavailable: 0` keeps the old ones, and the rollout stalls visibly, the same protection
R-HA-008 gives against an incompatible schema. The mechanism already exists (R-STOR-035's startup
check), so (d) costs one default (`health.require_object_store: false`) and no new code path. (c)
would automate the single-replica case, but it couples readiness to other replicas' heartbeats.
Clients already retry the fast `503` through the edge, which balances per request, and
`TroveObjectStoreUnreachableSingleReplica` names the replica. (e) removes (d)'s one real cost, lost
capacity when pods churn during a long shared outage, but it weakens the gate exactly where it
matters: a rotated or revoked S3 role leaves the settings fingerprint unchanged. That cost also
needs a long outage plus pod churn, and the shared-outage alert is already firing then; the
PodDisruptionBudget keeps a node drain from evicting more than one ready pod meanwhile. The
development agent revisits this if `TroveObjectStoreUnreachableSingleReplica` fires repeatedly in
real installations (node-specific egress failures, for example), which calls for (c)'s majority
rule on top of (d), or if capacity loss during long outages is observed, which calls for (e) with
a credential check added to the fingerprint.

This deliberately changes INV-12, which ties readiness to object-storage reachability. The edit to
[README.md](README.md) is listed as a cross-page consequence.

**Applied in the spec:** R-HA-007: the two shared-outage rows are merged into one in which running
replicas stay ready and a replica started during the outage stays not ready; the single-replica row
no longer turns the replica unready. R-HA-008: the bullet that deferred the rule to this question is replaced by
the first-readiness rule and `health.require_object_store: false`. R-HA-010: acceptance criteria
require `/readyz` to stay `200` during the outage and a replacement pod to stay not ready. R-HA-031:
`config.health.require_object_store: false`. R-HA-052: new pass condition for the object-store
scenario.

### Q-HA-2 · PostgreSQL version floor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 15 | Support 15, 16, 17, 18 | Widest reach | End of life 2027-11-11, probably within a year of trove's first release, which forces a floor bump soon after 1.0; four versions in the CI matrix before 19 arrives |
| (b) 16 | Support 16, 17, 18; 19 after its general availability | End of life 2028-11-09; nothing in the design needs a newer version | Installations on 15 must upgrade PostgreSQL first |
| (c) 17 | Support 17 and 18 | Fewest versions to test | Excludes 16, which the community supports for two more years, without a feature that needs 17 |

**Choice:** (b) — PostgreSQL 16 is the floor; the floor moves only after a version's community end
of life, announced one minor release ahead.

**Rationale:** A floor that expires within months of the first release would force a breaking bump
right after 1.0, and a higher floor would exclude supported servers for no gain. The spec was
searched for version-specific features: UUIDv7 values are generated in Go ([architecture.md](architecture.md)), not with
PostgreSQL 18's `uuidv7()`, and nothing else needs 17 or 18. Tying later bumps to the community end
of life makes the floor predictable for operators. The development agent revisits this if a
required feature exists only in 17 or later.

**Applied in the spec:** R-HA-013: the floor is stated as decided; PostgreSQL 19 joins after its
general availability (planned 2026-11-19, verified 2026-10-28) and a passing CI matrix; a rule for
dropping a version after its end of life; the CI criterion covers every supported version.

### Q-HA-3 · S3 component of the local profile after MinIO's archival

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The MinIO community repository was archived on 2026-04-25 and ships source only. Garage was
dropped as an option: its
[S3 compatibility page](https://garagehq.deuxfleurs.fr/reference_manual/s3_compatibility.html)
lists object versioning as not implemented, so the restore drill (R-HA-025) could not run on it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) MinIO, pinned | An image built from the archived source at a fixed commit, or the last published image | The store most installations know | No security fixes; running it in shared CI runners that build untrusted pull requests is a risk; no upstream for bug reports |
| (b) SeaweedFS | One `weed` process in the profile; Apache-2.0, maintained, images published | Documents bucket versioning, version listing, delete markers and deletes by version ID; light enough for kind | Its documentation says version-specific lifecycle rules are not fully implemented; less common in production than AWS S3 or MinIO |
| (c) Ceph RGW through Rook | A Ceph cluster in kind | Closest to on-premises production stores | Operator, monitors and OSDs on a laptop; hard to fit R-HA-051's 10-minute `dev-up` budget |
| (d) RustFS | Apache-2.0 store whose README lists bucket versioning as available; 1.0 line released in September 2026 | Operates much like MinIO | Weeks of release history; versioning in a single-drive dev deployment not verified |
| (e) SeaweedFS per change, MinIO and AWS S3 nightly | (b) for the profile and per-change CI; the nightly matrix adds a MinIO job built at a pinned commit and an AWS S3 job as the reference | A maintained default for every developer and pull request, plus evidence for the stores installations run | Three stores to keep green |

**Choice:** (e) — SeaweedFS runs in the local profile and per-change CI; MinIO (pinned source build,
isolated nightly runners) and AWS S3 run nightly.

**Rationale:** The profile has to exercise versioning, because the restore drill and
`restore-versions` depend on it; that rules out Garage and leaves SeaweedFS as the maintained store
with documented versioning that fits a laptop. The missing lifecycle support costs nothing, since
the orphan sweep is the primary cleanup (R-HA-012) and the lifecycle rule only a second line.
Keeping MinIO nightly protects installations that run it without exposing per-change runners to an
unpatched build. AWS S3 stays the reference for consistency claims (R-STOR-035). The AWS job needs
an AWS account and a CI role, which the repository does not have yet. The development agent
revisits this if SeaweedFS fails `trove admin storage check` or the restore drill; RustFS (d) is
then the next candidate.

**Applied in the spec:** R-HA-051: `deploy/dev/minio.yaml` is replaced by `deploy/dev/s3.yaml`
(SeaweedFS, images pinned by digest), the tool pins name SeaweedFS, and the MinIO bullet is split
into an S3-store bullet and a MinIO bullet that defines the nightly job; the AWS S3 job is reported
as skipped until the owner provides an AWS account and a CI role. R-HA-010, R-HA-043, R-HA-045
and R-HA-052: criteria refer to the local profile's S3 store instead of MinIO. The page summary
names SeaweedFS, and the `force_path_style` comment in R-HA-031 lists it.

### Q-HA-4 · Reserve `/v2/` on the client listener for OCI

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-6 puts every package protocol under `/api/<format>/<repoKey>/`. Docker and OCI clients cannot
use a path prefix before `/v2/`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Reserve `/v2/` in INV-6 | `/v2/` on the client listener belongs to the OCI distribution API, with the repository key as the first segment | One host, one certificate, one Ingress rule; the chart already routes the whole host (R-HA-037); `docker pull trove.example.com/docker-all/alpine:3` works | One more host-root path besides npm's exchange prefix (Q-NPM-1); trove must own its host root, which INV-6 already assumes |
| (b) Subdomain per repository only | `<repoKey>.trove.example.com/v2/` | Image names without a repository segment | Wildcard DNS and a wildcard certificate become mandatory for every OCI user |
| (c) Separate listener port | For example `:5000` for OCI | No path can ever collide | A second Service port, Ingress or Gateway listener, firewall rule and certificate everywhere; image references carry the port |
| (d) One dedicated registry host | A second host name (for example `registry.trove.example.com`) whose root is the OCI API; the main host keeps INV-6 unchanged | The main host's root stays free; no wildcard certificate | A second DNS name, certificate and Ingress or Gateway host for every installation that wants OCI; `server.base_url` alone no longer describes the installation, so URL generation needs a second base URL |

**Choice:** (a) — INV-6 reserves `/v2/` on the client listener for the OCI distribution API from the
MVP on; subdomain routing stays an optional mode of the OCI format page (R-HA-041).

**Rationale:** The reservation costs nothing now and prevents an MVP route from taking `/v2/` and
forcing a breaking move when OCI arrives (F4 in the roadmap). (a) needs no edge configuration
beyond R-HA-039, while (b), (c) and (d) push wildcard certificates, extra ports or a second host
and certificate onto every installation. trove already owns its host root for npm's host-root
exchange prefix (Q-NPM-1), so (a) adds no new assumption. (d) stays possible later as an optional mode of
the OCI format, next to (b)'s subdomains. The development agent revisits this only if an OCI
client turns up that cannot use repository-prefixed image names.

This deliberately amends INV-6. The edit to [README.md](README.md) is listed as a cross-page
consequence.

**Applied in the spec:** R-HA-041: states that INV-6 reserves `/v2/` and that no other route may use
it. A new bullet makes the reservation MVP: `/v2/` answers `404` until the OCI format exists, and a
route-table test rejects any other client-listener pattern under `/v2/`, because ServeMux's
conflict check does not catch a more specific pattern. The routing itself stays P1.

### Q-HA-5 · Zero-downtime upgrades that skip minor releases

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The catalog (OPS-072) allows in-place upgrades to any later minor release. Expand/contract with a
one-release compatibility window cannot keep N−2 and N serving side by side once N contracts what
N−2 used.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Adjacent minors only | Zero downtime from N−1 to N; a skip goes stepwise or with a short downtime; the contract guard enforces it | Compatibility code and dead columns live one release; the mixed-version tests cover one pair | Operators who skip releases run several `helm upgrade` commands or accept a short downtime |
| (b) Two-minor window | Contract steps declare `lastUsedBy` two releases back; N−2 to N rolls | Skipping one minor needs no downtime | Compatibility code lives twice as long, and the mixed-version tests double (N−2 and N−1 against N) |
| (c) No contract steps within a major | The schema only grows until the next major | Any minor to any later minor rolls | Dead schema accumulates for a whole major; renames and new `NOT NULL` constraints wait for a major |

**Choice:** (a) — rolling upgrades are zero-downtime between adjacent minors; skips go stepwise or
with downtime, enforced by the contract guard.

**Rationale:** Each stepwise upgrade is one rolling `helm upgrade`, so the cost of a skip is
operator time, not availability, and the guard turns a mistake into a failed Job instead of broken
replicas. (b) and (c) buy convenience with a longer life for code paths that are hard to test.
[architecture.md](architecture.md) already promises N−1 to N without downtime (R-ARCH-026). The
development agent revisits this if installations regularly lag more than one minor behind.

**Applied in the spec:** R-HA-021: the skip row refers to this decision instead of the open
question; a new bullet fixes the one-minor window (`lastUsedBy` of a contract step shipped in N is
N−2 or older, stated also in R-ARCH-026's `requires-app` header terms) and what release notes and
the guard's error must name.

### Q-HA-6 · Transaction-mode connection poolers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Leases, the listener and the fetch-collapse lock need session-level state. PgBouncer in
transaction mode breaks them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Not supported | Direct connections or a session-mode pooler only | Advisory-lock fencing (R-HA-006), `LISTEN` and fetch collapse work as designed; nothing to build | Large replica counts need a larger `max_connections` (R-HA-016) |
| (b) Split connections | The main pool goes through a transaction-mode pooler; listener, lease and lock sessions use a second, direct host | Fewer server connections at high replica counts | Two hosts to configure and secure; prepared-statement handling under the pooler; worth it only beyond the sizes of R-HA-050 |
| (c) Table-based leases | Replace session-level locks with lease rows | Works through any pooler | Gives up immediate release on session end, which R-HA-006's fencing builds on; `LISTEN` would still need a direct session |

**Choice:** (a) — transaction-mode and statement-mode poolers are not supported; (b) stays a P2
item for installations whose connection counts demand it.

**Rationale:** The spec text already followed (a) (R-HA-006 item 5 and the Deferred table), and this
decision confirms it. The connection budget of R-HA-016 stays within normal `max_connections`
values for the documented profiles (R-HA-050), so (b) solves a problem the MVP does not have, and
(c) would weaken fencing, the property that keeps a partitioned replica from committing garbage
collection decisions. What the text lacked was detection: a misconfigured pooler would have
surfaced as silent cache staleness. The `notify` check now waits for a delivery probe, which keeps
such a replica not ready. The development agent revisits this when a reference load test needs
more connections than a managed database offers.

**Applied in the spec:** R-HA-006 item 5: names transaction-mode and statement-mode poolers, states
that direct connections and session-mode poolers are supported, and points to the probe and the
P2 item. R-HA-004: new delivery-probe bullet and acceptance criterion with PgBouncer in both modes.
The Deferred row stays P2 and now names option (b) instead of the question.

### Q-HA-7 · Priority of Gateway API

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The catalog ranks Ingress MVP and Gateway API P1. ingress-nginx was retired in March 2026, and the
Kubernetes project recommends Gateway API.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Catalog priorities | Ingress MVP, Gateway API P1 | Smallest MVP chart | Clusters that offer only a Gateway implementation cannot expose the MVP without hand-written routes; R-HA-039's tests already run through an HTTPRoute |
| (b) HTTPRoute MVP, chart-created Gateway P1 | The chart attaches an HTTPRoute to an existing Gateway at MVP; rendering a Gateway comes later | Covers Gateway-only clusters with a few template lines; platform teams usually own Gateways | Two exposure paths to test from the MVP on |
| (c) All of Gateway API MVP | HTTPRoute and chart-created Gateway at MVP | Single-tenant clusters need no separate Gateway manifest | Gateway listeners carry TLS and implementation-specific settings, which is more chart surface for a less common setup |

**Choice:** (b) — the HTTPRoute is MVP; a Gateway rendered by the chart is P1.

**Rationale:** The HTTPRoute is the part every Gateway-based cluster needs, and the CI setup that
runs R-HA-039's tests through it exists anyway, so the cost is small. A chart-created Gateway mostly
helps single-tenant clusters and brings listener and certificate settings into the chart, which can
wait. The development agent revisits this if the walkthrough shows that most target clusters have no
shared Gateway.

**Applied in the spec:** R-HA-038 is now MVP and covers only the HTTPRoute; the new R-HA-053 (P1,
Covers HA-040) holds the chart-created Gateway. R-HA-028's resource table and the
`gateway.create` values block point to R-HA-053; R-HA-037 says the HTTPRoute exists from the MVP
on.

### Q-HA-8 · Who creates the first encryption key

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Operators expect `helm install` to work without preparing anything. [architecture.md](architecture.md)
R-ARCH-021 has no default and no generated key, and this page forbids chart-generated Secrets.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Chart generates it | Generated on first install and kept across upgrades with Helm's `lookup` | `helm install` needs one Secret fewer | `lookup` returns nothing under `helm template` and Argo CD, so the key is regenerated on every GitOps sync and every sealed value becomes undecryptable without an error |
| (b) Operator creates it | The chart fails rendering without it and prints the one-line command that creates a key | No generated value can change on re-render; works with every secret tool (external-secrets operators, sealed secrets); the chart keeps needing no Secret permissions | One preparation step before the first install |
| (c) Opt-in hook Job | A Job with `create` on Secrets generates the key once | Install works without preparation when enabled | Grants the release a Secret-write permission it otherwise never needs; a deleted Secret is silently regenerated with a new key |

**Choice:** (b) — the operator creates the key Secret; the chart fails without it and prints the
command that creates one.

**Rationale:** The failure mode of (a) and (c) is the worst kind: silent and irreversible loss of
every stored upstream credential, which the catalog also warns about (HA-016). (b) turns the
preparation step into a fail-fast error that already contains the fix, so it costs one command. The
development agent revisits this only if Helm gains a render-stable way to keep generated values
that also works offline.

**Applied in the spec:** R-HA-027: the key is created by the operator, by hand or through secret
tooling, and the rendering error prints the creation command. R-HA-029: the encryption row names
that command.

### Q-HA-9 · Making revocations survive a database restore (H-37)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Document the gap | The operator replays revocations from the audit log stream by hand | Nothing to build | Depends on someone doing it correctly under restore pressure; a leaked token revoked after *t* is valid until then |
| (b) Revocation journal in object storage | Every change to an access-bearing object is also written, through an outbox that the audit writer fills in the same transaction, to a write-once journal under the blob bucket; `post-restore` re-applies the removals among the entries newer than *t* and never grants | Automatic; reuses the versioned bucket restores already depend on and the transactional audit events that already exist; a forged entry can deny access but never grant it | New write path (outbox, shipping job) and per-object replay rules; removals made in the last seconds before the old database stopped can be missed; an object whose history replay cannot reproduce exactly is suspended rather than restored |
| (c) Invalidate all tokens on restore | `post-restore` revokes every token | Closes the gap completely for tokens | Breaks every CI pipeline and service account at once, does not cover permission removals, and punishes the restore instead of the attacker |
| (d) Replay from the audit stream | `post-restore --audit-log <file>` re-applies revocations from an export of the `log: audit` stream | No new write path in trove | Depends on the operator's log pipeline keeping complete, untruncated events for the window; anyone who can write to the pipeline can inject events |

**Choice:** (b) at P1 — a revocation journal in object storage that `post-restore` replays; the MVP
keeps the manual procedure of (a).

**Rationale:** A revoked credential that comes back to life is a security failure, and only (b)
fixes it without an operator in the loop and without punishing legitimate users. Two design points
keep (b) correct for one developer:

- **The audit writer fills the outbox.** R-OPS-015 already writes these events in the transaction
  of the change, so one hook there covers every owning page, and an event type added later is
  covered by a registry flag instead of new emission code in each page.
- **Replay compares against the current state.** A narrowing is applied only when the after-state
  grants nothing beyond the object's current state; otherwise it is skipped or the object is
  suspended. Trusting the journaled before-state instead would let a forged entry escalate, and
  applying narrowing diffs blindly would broaden access where an empty include list means
  "unrestricted" (R-AUTHZ-005). With this rule, the worst a writer with the runtime S3 credential
  can do is revoke access, a denial of service and not an escalation.

The journal is P1 although the restore procedure (R-HA-025) ships in 1.0
([roadmap Q-ROAD-2](../coverage/roadmap.md#q-road-2--backup-and-restore-in-10)): it is a new write
path across every access-bearing object type with per-object replay rules, and H-37 exists in
every installation whose database is restored, whether or not a procedure is documented. Until the
journal ships, H-37 stays live with the manual mitigation, and it is the first work after 1.0. The
development agent revisits this in two cases: if a 1.0 installation needs a production restore
before the journal ships, the journal is built ahead of all other work; if suspensions after
restore drills turn out to be frequent, the replay rule moves to exact per-field narrowing where
the owning page can define it.

**Applied in the spec:** New R-HA-054 (P1). It defines the registry flag `journal: revocation`, the
minimum set of flagged audit events under their existing names (token revocation and rotation,
user and service-account disables and deletions, group deletion and membership removals from every
source, role unbinding, policy deletion and updates, ownership removal, CI issuer and mapping
changes, access settings), the outbox table, the object layout, the replay rules with the
comparator and suspension, the configuration `backup.revocation_journal.*` and acceptance criteria
that include forged entries. The break-glass `enabled` state is excluded, because it lives in the
configuration file. R-HA-025 step 4 replays the journal and reports suspended objects, and the
drill criterion expects a revoked token to stay revoked. R-HA-031 adds the
`backup.revocation_journal` values, R-HA-045 adds `TroveRevocationJournalBacklog`, and H-37 and the
Security notes describe what stays live before and after P1.

### Q-HA-10 · Default of the egress NetworkPolicy

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Off by default | Ingress policy on; egress opt-in, with a warning in `NOTES.txt` and an example values file | An installation never breaks because the chart guessed the network wrong; R-REPO-038 already checks trove's own outbound addresses | A compromised process can reach everything the pod network reaches until the operator enables egress |
| (b) On with a generic allow-list | DNS, port 5432 and port 443 to anywhere except link-local ranges | Blocks cloud metadata endpoints by default | Breaks NodeLocal DNSCache (link-local address), GKE workload identity (metadata server on 169.254.169.254), EKS Pod Identity (169.254.170.23), S3 stores and upstreams on other ports; for AWS S3 the rule cannot be narrower than "any address on 443", because NetworkPolicy has no DNS names or prefix lists |
| (c) On, explicit targets required | Rendering fails unless database and object-storage targets are given | No guessing; forces a deliberate egress design | Breaks the six-value minimal install (R-HA-031); managed databases and AWS S3 have no stable pod selectors or narrow CIDRs to give |
| (d) On, everything except the metadata address | One rule: egress to `0.0.0.0/0` and `::/0` except `169.254.169.254/32` (and `fd00:ec2::254/128`) | Blocks the classic credential theft through the cloud metadata service and nothing else | GKE workload identity serves tokens from that very address, so S3 credentials break there; on CNIs whose `ipBlock` rules do not match cluster pod addresses (Cilium, for example), in-cluster PostgreSQL and S3 become unreachable |

**Choice:** (a) — egress stays off by default; `NOTES.txt` says so and points to
`deploy/helm/trove/examples/egress.yaml`.

**Rationale:** Of the four, only (a) cannot break a working cluster at first start. (b)'s
protection reduces to blocking link-local addresses, which is exactly what breaks common platform
features, and (d), the narrowest default that still blocks something, breaks GKE identity and
in-cluster dependencies on common CNIs. The real protection against outbound misuse by trove's own code is R-REPO-038, which
applies regardless. The egress policy matters when the process is compromised, so it is made
visible (NOTES, example file, Security notes) rather than silently absent. The development agent
revisits this if the chart gains a reliable way to detect the cluster's DNS and identity endpoints.

**Applied in the spec:** R-HA-042: the default is stated as decided, `NOTES.txt` warns while egress
is off, and an example values file rendered in CI is added. Security notes: new bullet on
unrestricted egress.

### Q-HA-11 · Publishing location of images and chart

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) GitHub Container Registry of the hosting organization | `ghcr.io/guided-traffic/trove` and `oci://ghcr.io/guided-traffic/charts/trove` | The repository is on GitHub (`origin` is `github.com/guided-traffic/trove`, verified with `git remote -v`); the release workflow pushes and signs with its own OIDC identity, no stored registry credential | Anonymous pulls depend on GitHub's availability; packages must be set to public once |
| (b) GitLab container registry of a GitLab group | Images and chart in a GitLab project's registry | Close to the GitLab CI users trove serves | The repository and its release workflow are not on GitLab; pushing would need a stored GitLab token and a second identity for signatures |
| (c) Both, mirrored | Publish to GitHub and GitLab | Pull from either | Two signature identities to document and verify; twice the release surface for no functional gain; operators mirror anyway |
| (d) Docker Hub | `docker.io/<org>/trove`, the chart as an OCI artifact there | Default registry of every container tool; the widest reach | Anonymous pulls are rate-limited per source address, which hits CI fleets and clusters behind one NAT address; the release workflow needs a stored registry token; an organization account must be created first |

**Choice:** (a) — images and chart are published to the GitHub Container Registry of the
`guided-traffic` organization and signed from the GitHub Actions release workflow.

**Rationale:** Publishing where the release workflow runs keeps one identity for building, pushing,
signing and provenance, which is what makes `cosign verify` meaningful, and needs no stored
registry credential, unlike (b) and (d). Air-gapped and pull-limited installations mirror by digest
anyway (R-HA-047). The development agent revisits this
if the repository moves to another forge.

**Applied in the spec:** R-HA-031: `image.registry: ghcr.io` and `image.repository:
guided-traffic/trove` are now proposed defaults. R-HA-047: publishing location, chart reference and
the exact certificate identity of the release workflow.

### Q-HA-12 · Replica registry at MVP

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The catalog ranks the replica status screen P1 (OPS-008), and R-OPS-003 was drafted as P1 as a
whole. This page needs the table and heartbeat at MVP for the contract guard, the cluster minimum
version and the post-restore cleanup.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Table and heartbeat MVP | API and UI stay P1 | Smallest MVP | R-HA-001's and R-HA-020's MVP acceptance criteria use `GET /api/v1/system/replicas`, so they could not run |
| (b) All P1, separate HA table | HA keeps its own minimal heartbeat table | R-OPS-003 unchanged | Two sources of truth about which replicas are alive |
| (c) Table, heartbeat, pruning and read-only API MVP | Everything the HA mechanisms and rollout checks read is MVP; the UI status screen is ranked by [api-and-ui.md](api-and-ui.md) R-UI-038 | MVP acceptance criteria and rollout verification work; the API is one read query | Slightly more MVP scope |

**Choice:** (c) — the table, the heartbeat, the pruning of stale rows and the read-only replica API
are MVP. The UI status screen is MVP as well: [api-and-ui.md](api-and-ui.md) R-UI-038 ranks it MVP
(Q-API-8 there), and this choice follows that ranking.

**Rationale:** The API is what operators and the failure-injection suite use to verify a rollout,
and it is a single `SELECT` behind an existing permission. Without pruning, every crashed pod would
leave a row forever. The status screen renders only from endpoints that are MVP (this API,
R-OPS-004, the task and job API of R-OPS-019, R-API-049), so it adds no backend work, and it is the
only UI view of the replicas, the scheduled tasks and the key ring. The development agent revisits
this only if R-OPS-003's API grows beyond a read-only list.

**Applied in the spec:** R-HA-003: states which parts of the registry are MVP, that the UI status
screen (R-UI-038) is MVP as well, and that R-HA-001 and R-HA-020 verify through the API.
[operations-and-observability.md](operations-and-observability.md) R-OPS-003 states the same
(registry, API and status screen MVP), so no cross-page edit remains.

## What this does not cover

### H-37 · A database restore revives revoked credentials and removed grants

Restoring PostgreSQL to time *t* restores every token, session, user and permission policy as it
was at *t*. Token revocations, user and service-account disables, group-membership and role-binding
removals, policy narrowings and CI mapping changes made after *t* are undone. A token revoked
because it leaked becomes valid again. `post-restore` ends all UI
sessions and prints a warning, but it cannot know what happened after *t*: those facts were only in
the lost part of the database and in the audit log stream.

- **Live in the MVP.** The mitigation is manual: read the `log: audit` stream from *t* to the
  restore, and repeat each revocation and removal through `trove admin token revoke` and the API.
- **From P1 on (R-HA-054, Q-HA-9),** `post-restore` re-applies the revocation journal kept in
  object storage. What stays live then: removals whose outbox rows had not been shipped when the old
  database stopped (normally less than one shipping interval, default 5 s), every removal made
  while `backup.revocation_journal.enabled` was false, and, without bucket versioning, journal
  objects deleted with the runtime S3 credential. Access granted after *t* is lost rather than
  revived, and objects whose history replay cannot reproduce exactly are suspended; both fail
  closed.

### H-38 · Plain HTTP between the edge proxy and the replicas

TLS ends at the Ingress controller or Gateway (R-HA-037). Inside the cluster, requests travel to the
pods unencrypted, including `Authorization` headers with bearer tokens, Basic credentials and
session cookies. Anyone who can capture pod-network traffic can read the following: a compromised
node, a privileged pod with host networking, or a CNI-level tap. trove has no TLS listener in the
MVP (internal TLS, HA-018, is P2). The mitigation is at the platform level: a service mesh with
mutual TLS, or CNI encryption (WireGuard or IPsec modes of common CNIs). The chart does not enforce
either.
