# High availability and Helm deployment

Status: specification — not implemented

This page specifies how trove stays available, scales out and is deployed. It covers:

- The replica model: identical, interchangeable replicas per role, with `web` and `worker` as
  separate Deployments from the start and `backup` as a CronJob (INV-2), and how replicas
  coordinate through PostgreSQL and Valkey without talking to each other (INV-3, INV-12, INV-18).
- The failure contract of every component: pod crash, node drain and node loss, PostgreSQL failover
  with reconnect and retry, Valkey failover through Sentinel and Valkey outage, object-storage
  outage, spool exhaustion and a failed migration.
- Leader-less scheduling and singleton work under advisory-lock leases.
- Node drains without outage: the capacity rule, disruption budgets, spreading and the drain
  acceptance test under npm and NuGet load.
- Rolling upgrades with expand/contract migrations and the migration Job.
- PostgreSQL requirements: version floor, server settings, roles and privileges, TLS, connection
  budget, the optional transaction-mode pooler and credential rotation.
- Valkey requirements: discovery through Sentinel, credentials and TLS, supported topologies and
  the degraded mode.
- Backup, restore and disaster recovery from the deployment side: the backup CronJob, the restore
  Job, the platform layer (point-in-time recovery, bucket versioning) and why a restored database
  never points at a deleted blob. Backup format and procedure belong to
  [backup-and-restore.md](backup-and-restore.md).
- The Helm chart `deploy/helm/trove`: every resource it renders, every volume it mounts (none
  ReadWriteMany, none persistent) and its complete `values.yaml`.
- Edge-proxy requirements for large uploads, sizing guidance, the local development profile
  (kind, CloudNativePG, Valkey with Sentinel, SeaweedFS) and the failure-injection suite that
  proves all of the above.

Semantics owned elsewhere are referenced, not repeated. The probe endpoints and replica registry
are in [operations-and-observability.md](operations-and-observability.md). Blob commit, garbage
collection and the object-store client are in [storage.md](storage.md). Secret sealing and
outbound TLS are in [repositories-and-proxy.md](repositories-and-proxy.md). The break-glass account
is in [identity-and-authentication.md](identity-and-authentication.md).

Covers: the catalog pages [ha-and-deployment.md](../features/ha-and-deployment.md) (`HA`, all MVP
and P1 entries), the upgrade and backup entries of
[operations-and-monitoring.md](../features/operations-and-monitoring.md) (OPS-032 and OPS-033 on
the chart side, OPS-072, OPS-073, OPS-074, OPS-076), and the deployment side of
[storage-and-blobs.md](../features/storage-and-blobs.md) (spool volume, object-store credentials
through workload identity). The storage entries themselves belong to [storage.md](storage.md).

## Where this page hands over

| Topic | Owner |
|---|---|
| `/healthz`, `/readyz` semantics, check list (the `valkey` check included), `health.*` keys, drain delay | [operations-and-observability.md](operations-and-observability.md) R-OPS-001 |
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
| Cross-replica fetch collapse and its Valkey lock (INV-11) | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 |
| What Valkey holds and what it never holds, its key layout and keyspace, the change log and signals, the Valkey client library and its command list | [architecture.md](architecture.md) R-ARCH-018, R-ARCH-030, R-ARCH-031, R-ARCH-033, R-ARCH-034 |
| Backup format, `trove backup create` and `trove backup restore`, consistency with garbage collection, retention semantics, what "empty installation" means | [backup-and-restore.md](backup-and-restore.md) |
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
                                    │ HTTP, Service <fullname>:8080, no session affinity
            ┌───────────────────────┼───────────────────────┐
     ┌──────▼──────┐         ┌──────▼──────┐         ┌──────▼──────┐
     │ web pod     │         │ web pod     │   ...   │ web pod     │  Deployment <fullname>-web
     │ :8080 :8081 │         │ :8080 :8081 │         │ :8080 :8081 │  spool: emptyDir
     └─────────────┘         └─────────────┘         └─────────────┘
     ┌─────────────┐         ┌─────────────┐
     │ worker pod  │   ...   │ worker pod  │  Deployment <fullname>-worker: jobs, :8081 only
     └─────────────┘         └─────────────┘
     ┌─────────────┐
     │ backup pod  │  CronJob <fullname>-backup ──S3──▶ backup target bucket (R-HA-060)
     └─────────────┘

  every pod ──SQL───▶ PostgreSQL primary ──streaming──▶ standbys (CloudNativePG, Patroni, RDS)
                      optional: request pool through a transaction-mode pooler, control
                      connection through a pool of its own (R-HA-058, Q-HA-20)
  web, worker ─RESP─▶ Valkey primary ──replication──▶ replica; ≥ 3 Sentinels name the primary
                      (backup, restore and migration pods never connect to Valkey)
  every pod ──S3────▶ blob bucket, versioning recommended (AWS S3, MinIO, Ceph RGW, SeaweedFS)

  Helm hook before the Deployments: the migration Job. With restore.enabled: no migration Job,
  both roles at zero, and the restore Job as an ordinary resource (R-HA-060, Q-HA-18)
```

Replicas never open connections to each other. Every cross-replica signal goes through PostgreSQL
(job claims, leases, the replica registry, the change log that carries invalidations) or Valkey
(fetch collapse, the negative cache of remote repositories, rate-limit counters, change messages
and signals); [architecture.md](architecture.md) R-ARCH-030 assigns each mechanism (INV-12). The
pod network therefore needs no replica-to-replica rule and no peer discovery. Everything a pod
keeps outside these three services is scratch space for one request or one job (INV-18).

## Replica model and coordination

### R-HA-001 · Active-active replicas without a primary node
Priority: **MVP** · Covers: HA-001, HA-002, HA-010

- Every `web` replica MUST be able to serve every client request and every management request, and
  every `worker` replica every job kind it implements. No replica holds state that another replica
  needs to answer a request (INV-2, INV-18): consecutive requests of one client, the requests of a
  multi-request protocol included, may reach different replicas. The only local state is the spool
  of the request or job that is running (R-HA-035) and in-process caches, which are invalidated via
  R-HA-004.
- There MUST be no primary, leader or seed replica in configuration. Work that must run once per
  cluster runs under leases that any `worker` replica can take (R-HA-005, R-HA-006).
- **Roles** (INV-2, [architecture.md](architecture.md) R-ARCH-001). A process has one role,
  `process.role: web|worker`, which has no default. The chart starts each Deployment with
  `trove serve --role <role>`, the flag form that overrides `process.role` (R-HA-002), so the
  shared configuration file names no role:
  - `web` serves the client listener and the operations listener, and never claims jobs or leases;
  - `worker` claims jobs and leases, and serves only the operations listener;
  - `all` combines both. It exists only for the local profile, for a binary started outside the
    chart during development, and the chart never renders it.

  The `backup` role is the `trove backup` subcommand. The backup CronJob and the restore Job run it
  (R-HA-060), and it serves no listener.
- A production deployment MUST run at least two replicas of each role, on different nodes (INV-12).
  The chart enforces this unless `highAvailability.enforce` is false (R-HA-029). One replica of a
  role is supported for development only. It runs the same code path against the same dependencies
  (external PostgreSQL, Valkey and S3), so scaling is a change of a replica count and nothing else.
  The catalog's "single node with embedded database and local file storage" topology does not exist
  in trove.
- Load is shared by adding replicas of a role (INV-12). Capacity planning assumes that the peak load
  needs more than one `web` replica. R-HA-059 states how many replicas a node drain needs on top.
- Adding a replica needs no configuration besides the replica count. A new `worker` replica takes
  jobs as soon as it is ready. Removing a replica needs no handover: its leases end with its
  database sessions (R-HA-006), its job leases expire (R-HA-005), and its Valkey locks expire
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026).

Acceptance criteria:

- With three `web` replicas behind the Service, `for i in $(seq 1 300); do curl -s -o /dev/null -w '%{http_code}\n' https://trove.example.com/api/v1/system/ping; done | sort | uniq -c`
  prints only `200`, and the `X-Trove-Replica` header of authenticated calls shows all three pod
  names.
- `kubectl scale deploy/trove-web --replicas=4` adds a replica that serves traffic and appears in
  `GET /api/v1/system/replicas` without any other change.
- `helm upgrade … --set web.replicaCount=1 --set worker.replicaCount=1 --set highAvailability.enforce=false`
  yields a working installation with one replica per role against the same database, Valkey and
  bucket.
- A test proxy in front of the `web` pod IPs sends every request to a different replica in strict
  rotation. Through it, these flows complete (INV-18): an npm web login with its polling requests,
  `dotnet nuget push` followed by a restore of the pushed version, `npm publish` followed by
  `npm view`, and a paged API listing followed to its last page.

### R-HA-002 · Role split into web and worker Deployments
Priority: **MVP** · Covers: HA-015

The role that serves clients is named `web` (INV-2), and
the split is no longer optional: it is an owner constraint since 2026-10-28.

- The chart MUST always render two Deployments: `<fullname>-web`, whose container runs
  `trove serve --role web`, and `<fullname>-worker`, whose container runs
  `trove serve --role worker`. Each has its own replica count, resources, autoscaler, disruption
  budget, termination grace period, spool size and scheduling settings (R-HA-031, R-HA-032). There
  is no single-Deployment mode, and no value renders `--role all`.
- The client Service selects only `web` pods. `worker` pods are reached only on the operations port
  (probes and metrics).
- `worker.replicaCount` (or `worker.autoscaling.minReplicas`) MUST be at least 1, otherwise no
  scheduled work runs. The chart refuses to render (R-HA-029). With `highAvailability.enforce` the
  minimum is 2 for both roles, so that GC and other singleton work survive a worker pod loss without
  waiting for a reschedule.
- `web` and `worker` pods run the same image and the same configuration file; only the `--role`
  argument differs. No component runs in a separate image or as a sidecar.
- Why from the start: background work (GC, retention runs, reindexing, cache fills of prefetch jobs,
  webhook delivery) never competes with request latency for CPU and memory; `web` scales on request
  load and `worker` on job load; a drain or rollout of one role does not touch the other; and a
  further split of either role (R-HA-062) is a change of values, not of code.

Acceptance criteria:

- With default values, `kubectl get deploy -l app.kubernetes.io/instance=trove` lists `trove-web`
  and `trove-worker`, and `GET /api/v1/system/replicas` shows roles `["web"]` and `["worker"]`.
- A manual GC run (`POST /api/v1/system/tasks/gc/run`) sent to a `web` pod is executed on a
  `worker` pod (the worker's instance ID appears in `job.locked_by` of the gc job), and the
  instance ID of no `web` pod ever appears in `job.locked_by`.
- `helm template … --set worker.replicaCount=0` fails and names `worker.replicaCount`.
- For every value set in `ci/`, the rendered containers carry the argument `--role web` or
  `--role worker`, never `all`.

### R-HA-062 · Further splits of a role into several Deployments
Priority: **P1** · Covers: HA-015

The owner allows several worker types and a split service (INV-2). Because every replica of a role
can serve everything its role serves, a further split is routing and scheduling only.

- **Web pools.** Each entry of `web.pools` renders a Deployment `<fullname>-web-<name>` with
  `--role web`, its own Service `<fullname>-web-<name>` (port 8080, no session affinity), PDB,
  autoscaler and the keys of `web` as overrides. The Ingress and the HTTPRoute route the entry's
  `paths` (path prefixes inside the same host, for example `/api/npm/` and `/api/nuget/`) to that
  Service; every other path stays on `<fullname>`. Use: a surge of package traffic cannot starve
  the UI and the management API. The host is still routed whole (R-HA-037): the pools split it by
  prefix, never by rewriting.
- **Worker pools.** Each entry of `worker.pools` renders a Deployment `<fullname>-worker-<name>`
  with `--role worker` that claims only the job queues listed in `queues`; the queue names and the
  setting that restricts a process to them belong to [architecture.md](architecture.md) R-ARCH-016.
  The main `worker` Deployment claims every queue that no pool lists, so a queue never stays
  unclaimed. Use: indexing or garbage collection on nodes of their own.
- Every pool follows the rules of its role: at least two replicas with `highAvailability.enforce`,
  spreading, PDB, the drain rule of R-HA-059, and the connection budget of R-HA-016, which counts
  pools like any other Deployment. A pool's pods carry `trove/pool: <name>`, which keeps its
  selectors apart from the main Deployment's (R-HA-028) and becomes the target label `pool` of its
  series (R-HA-045).
- **Endpoint pools are MVP.** The pool of a further endpoint (`endpoints.<name>.pool`,
  [endpoints.md](endpoints.md) R-EP-008, Q-EP-3) is a web pool named `ep-<name>` that follows every
  rule above, but is routed by its own port, Service and edge route instead of by path prefix, and
  its pods open only the listeners of their endpoints (R-EP-002). The chart renders it from the MVP
  on; the path pools of `web.pools` and the worker pools stay P1, and their names may not start with
  `ep-`.

```yaml
web:
  pools: []                    # default (proposed); example:
  # - name: packages           # example
  #   paths: [/api/npm/, /api/nuget/, /files/]   # example
  #   replicaCount: 4          # example; every key of web may be overridden here
worker:
  pools: []                    # default (proposed); example:
  # - name: indexing           # example
  #   queues: [index]          # example; queue names from architecture.md R-ARCH-016
  #   replicaCount: 2          # example
```

Acceptance criteria: with one web pool for `/api/npm/` and one worker pool for the index queue,
R-HA-059's drain test passes; npm requests appear only in the pool's access logs; jobs of the index
queue run only on the pool's pods; `helm template` fails when two pools list the same path prefix
or the same queue.

### R-HA-063 · Chart TLS wiring
Priority: **MVP** · Covers: HA-018

The chart wires TLS through trove's listeners and the platform's connections. `tls.enabled` defaults to `true`
(Q-HA-32).
- `tls.existingSecret` names a Secret that the chart never creates. It holds `tls.crt`, `tls.key` and `ca.crt`: the
  keys of a cert-manager TLS Secret with its CA. Rendering fails with `tls.enabled: true` and no such Secret, naming
  it, as the encryption key and the backup target do (R-HA-029). `tls.enabled: false` renders no TLS wiring, and H-38
  applies.
- The Secret is mounted read-only at `/var/run/secrets/trove/tls/`. The chart sets `server.tls.cert_file`,
  `server.tls.key_file` and, when mutual TLS is configured, `server.tls.client_ca_file` (R-ARCH-035). A rotated Secret
  replaces the mounted files, and the reloader of R-ARCH-035 applies them.
- A further endpoint serves the same pair unless its `tls.existingSecret` or `tls.extraSecrets` name pairs of its own,
  wildcard certificates included; each is mounted under `/var/run/secrets/trove/endpoints/<name>/<n>/`, rendered as
  that endpoint's `tls.certificates`, chosen by SNI and reloaded the same way ([endpoints.md](endpoints.md) R-EP-004,
  R-EP-008). Its Service port declares `appProtocol: https`, and its Ingress or HTTPRoute connects to it over TLS as
  above.
- Probes use `scheme: HTTPS` on the ops port (R-HA-034). The kubelet does not verify the served certificate for
  probes, so they need no CA.
- The client and ops Service ports declare `appProtocol: https`.
- The ServiceMonitor sets `tlsConfig` with `ca` from `ca.crt` and `serverName` equal to the Service name, so
  Prometheus verifies the certificate.
- The Ingress or Gateway connects to the backend over TLS and verifies it against `ca.crt`. The chart renders the
  backend-protocol setting of each supported controller, and the chart documentation names the annotation per
  controller. A Gateway route uses a `BackendTLSPolicy`.
- The chart test renders `tls.enabled: true` and checks the HTTPS probes, the `appProtocol` and the Secret mount, and
  checks that rendering without the Secret fails.

Acceptance criteria:
- `helm template` with `tls.enabled: true` and no Secret fails and names the Secret; with `tls.enabled: false` no probe
  uses `scheme: HTTPS`.
- Every request through the dev profile's edge is served over TLS and verified against the dev CA (R-HA-051).
- Prometheus scrapes the ops port over TLS with the chart's ServiceMonitor, and the target is up.

### R-HA-003 · Replica registry as the coordination view
Priority: **MVP** · Covers: HA-006, HA-009

The replica registry of R-OPS-003 (table `replica`, heartbeat every `system.heartbeat_interval`,
stale after `system.heartbeat_stale_after`) is the single source of truth for "which replicas are
alive, with which version and roles, on which node". This page uses it for four HA mechanisms. The
table, the heartbeat, the pruning of stale rows and the read-only API `GET /api/v1/system/replicas`
are therefore MVP (Q-HA-12). The node comes from `TROVE_NODE_NAME`, which the chart sets from the
downward API (R-HA-030); the registry also records how many Sentinels each replica reaches
(`valkey_sentinels_reachable`, R-HA-057). The UI status screen is MVP as well
([api-and-ui.md](api-and-ui.md) R-UI-038, Q-API-8, Q-UI-7): it groups the replicas by role, states
per role whether two ready replicas run on different nodes, summarises the replicas' Valkey views
and shows the backup summary of R-API-050. It is the only UI view of the replicas, the scheduled
tasks and the key ring. R-HA-001 and R-HA-020 verify rollouts through the replica API.

| Use | Rule |
|---|---|
| Contract-migration guard | A contract step runs only when no live replica runs a version that still uses the dropped object (R-HA-018). |
| Cluster minimum version | `min(version)` over live replicas gates features and background migrations whose data the previous release cannot interpret (R-HA-020, R-HA-022). |
| Connection budget check | The migration Job compares `max_connections` with the budget derived from the expected replica counts per role (R-HA-016). |
| Post-restore cleanup | `trove admin post-restore` empties the table, because every row in a restored database describes a pod that no longer exists (R-HA-025). |

- "Live" means `heartbeat_at > now() - system.heartbeat_stale_after`, evaluated with PostgreSQL's
  clock. Replica clocks are never compared with each other (R-HA-005).
- A replica in state `draining` still counts as live for the contract guard: it may still run old
  code for up to its grace period.
- The migration Job, the backup CronJob's pods and the restore Job do not insert a `replica` row.
  They are not serving replicas and must not appear in the minimum-version computation.

Acceptance criteria:

- During a rolling upgrade from 0.3.0 to 0.4.0, `GET /api/v1/system/replicas` shows both versions;
  after `kubectl rollout status deploy/trove-web` and `deploy/trove-worker` return, only 0.4.0 is
  listed within `system.heartbeat_stale_after`.
- `kubectl delete pod --grace-period=0 --force <pod>` makes the row stale within 30 s; the
  contract guard of a migration started afterwards ignores it.

### R-HA-004 · Cross-replica invalidation through the change log and Valkey
Priority: **MVP** · Covers: HA-006

Since 2026-10-28 trove issues no `LISTEN` and no `NOTIFY`,
and no replica holds a coordination session for invalidations ([architecture.md](architecture.md)
R-ARCH-018, Q-ARCH-15). A change reaches the other replicas through a change log in PostgreSQL,
which carries correctness, and through a Valkey Pub/Sub message, which carries speed. The writing
transaction raises the area's row in `cluster_epoch` and inserts the change into `cluster_change`;
every `web` and `worker` replica polls both tables every `cluster.epoch_poll_interval` on its
control connection. This requirement fixes the HA rules that mechanism MUST satisfy.

- **Delivery of a message is never a correctness condition.** Every cache that a change keeps
  fresh is self-validating or carries a maximum entry age; its owner sets the age, and this page
  requires one to exist (R-ARCH-018, ADR-031). The change-log poll applies every committed change
  within `cluster.epoch_poll_interval`, whatever Valkey does, and only the poll advances a
  replica's applied epoch of an area. A Valkey message only evicts early and never advances the
  applied epoch, so a lost, late, duplicated or forged message can neither suppress nor reorder a
  change.
- **Only committed changes are announced.** The change-log row commits with the change, and the
  Valkey message is published only after commit.
- **Valkey loss degrades, PostgreSQL loss gates.** While Valkey is unavailable, changes take effect
  at the next poll, typically within a second, and the replica stays ready (INV-12, Q-HA-13). A
  missing Valkey delivery only reports `valkey` as degraded (R-HA-008); it never fails a check that
  gates readiness. The `invalidation` readiness check of R-ARCH-018 passes while the last
  successful poll is younger than `cluster.epoch_max_staleness`. A replica that cannot poll the
  change log fails it after that, because it can then no longer bound how stale its revocations
  are; R-HA-008 debounces the check so that it never undercuts the `database` check.
- **No connection outside the main pool.** The poll runs on the replica's control connection, a
  reserved connection of the main class ([architecture.md](architecture.md) R-ARCH-019), so it
  works through a transaction-mode pooler (R-HA-058) and needs no session connection. A `web`
  replica therefore holds no PostgreSQL connection outside its main pool (R-HA-016). Behind a
  pooler, the control connection uses a server pool that request traffic cannot exhaust
  (Q-HA-20), so a saturated request pool cannot stall the poll.
- **Rolling upgrades.** Payloads are versioned. A replica that reads a payload version it does not
  know flushes the caches of that area instead of ignoring the row (R-HA-020: during a rolling
  upgrade, the newer release may write rows the older one cannot parse).
- **Failover and restore.** After a PostgreSQL failover that lost committed transactions
  (asynchronous replication, R-HA-013) or after a point-in-time restore, an area's epoch in
  PostgreSQL can be lower than the epoch a replica last applied, or later writes can already have
  raised it again to the same or a higher number, which then stands for different changes. The
  replica detects both: with its applied epoch it keeps the `changed_at` it read, which equals the
  `created_at` of the change row at that epoch, because both come from the writing transaction's
  `now()` ([architecture.md](architecture.md) R-ARCH-018, [authorization.md](authorization.md)
  R-AUTHZ-028, "A reused epoch"). An area whose epoch went backwards, whose epoch equals the
  applied one with a different `changed_at`, or whose change row at the applied epoch carries a
  different `created_at` MUST be flushed, and the replica takes the database's current epoch as its
  new starting point, so that later changes are not skipped as already applied. Two transactions
  separated by a failover never share that timestamp. A replica started after the restore starts
  from the current epochs anyway.
- Payloads stay below 1 KiB (R-ARCH-018). A change that touches many objects sends one "flush"
  entry per cache, not one entry per object.

Acceptance criteria:

- With Valkey's Pub/Sub connection of one replica killed (`CLIENT KILL TYPE pubsub`), a permission
  change made through another replica takes effect on it within `cluster.epoch_poll_interval` plus
  1 s, and `/readyz` keeps answering `200`.
- With Valkey stopped, a token revoked through one replica is rejected on every replica within
  `cluster.epoch_poll_interval` plus 1 s.
- A NetworkPolicy that denies one replica's egress to PostgreSQL makes it fail `invalidation` (and
  `database`) and leave the Service; after removal it applies every change it missed before it is
  ready again.
- A test lowers an area's epoch below the epochs the replicas have applied, as a failover with lost
  transactions would. Every replica flushes that area's caches, and the next change of the area
  takes effect on every replica.
- A test reuses an epoch: with Valkey stopped and one replica's poll paused by a test hook, it
  deletes the newest `cluster_change` row of an area, resets the area's `cluster_epoch` row to the
  previous epoch and `changed_at`, and commits a different change through another replica, which
  takes the same epoch number again. When the hook is lifted, the paused replica flushes the area
  (`trove_cache_flushes_total` rises) within `cluster.epoch_poll_interval` plus 1 s and serves the
  new change; a further change that raises the epoch above the reused one before the hook is
  lifted is detected the same way through the change row's `created_at`.

### R-HA-005 · Leader-less scheduling and job leases
Priority: **MVP** · Covers: HA-002, HA-007

Background work runs through the PostgreSQL job queue ([architecture.md](architecture.md) owns
the table and the worker loop). Schedules (cron expressions) belong to the subsystems that own the
work. This requirement fixes the HA guarantees both must give.

**Scheduling without a fixed leader.**

- One scheduler loop runs on each `worker` replica. Each tick (`jobs.schedule_tick`)
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
- The next due slot is computed from the task's cron expression and its last inserted slot
  (`job_task_slot`, Q-ARCH-29), not from the replica clock. Runs
  missed during an outage are coalesced into one run, never replayed one by one.

**Job leases.**

- A worker claims queued jobs with `FOR UPDATE SKIP LOCKED` in a short transaction that sets
  `state = 'running'`, `locked_by` (the replica's `instance_uid`) and
  `locked_until = now() + jobs.lease_duration`, then commits. No transaction stays open while the
  job runs.
- The worker extends the lease every `jobs.heartbeat_interval`. A row that another replica has taken
  cancels the job's context at once. A failed extension (database unreachable) cancels it only once
  the lease could already have expired, measured on the worker's monotonic clock from its last
  confirmed extension (Q-HA-31). Until then the handler keeps running, so a database blip shorter
  than the lease does not cancel the work. A cancelled handler stops changing shared state for the job.
- A reaper (part of the scheduler tick) returns jobs whose `locked_until < now()` to `queued`. It does
  not increment `attempt`: the claim that starts the next attempt does. A job whose attempts are used
  up ends `failed` with `attempts_exhausted`.
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

-- No schedule table. Each task's slots are job rows; the unique index job_task_slot on
-- (task, scheduled_for) keeps one job per slot (architecture.md R-ARCH-016, Q-ARCH-29).
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

Work that must not run twice at the same time (the scheduler tick, secret re-encryption,
background migrations) runs under an *advisory-lock lease* (INV-12). Two kinds of work are not on
this list:

- **Storage maintenance runs.** Garbage collection, the orphan sweep, retention runs, the scrub and
  remote-cache cleanup are kept apart by exclusive job keys in the job table
  ([architecture.md](architecture.md) R-ARCH-016), not by a lease; cap enforcement claims its
  outbox rows with `FOR UPDATE SKIP LOCKED` ([storage.md](storage.md) R-STOR-044). Their safety
  rests on row locks and, for S3 deletes, on the hand-over rule of [storage.md](storage.md)
  R-STOR-024: two overlapping runs cannot delete a referenced blob, so exclusion is an efficiency
  property there ([storage.md](storage.md) Q-STOR-13). They write through the main pool and need no
  session connection.
- **Reindexing.** A manual reindex and an automatic index rebuild are jobs with the dedup key
  `index:<repository id>` (R-ARCH-016), which keeps two runs for one repository apart without a
  lease, because leases carry registered numbers only (R-ARCH-017).

A lease works as follows:

1. The worker takes a connection from the dedicated lease pool (`database.lease_pool_size`) and
   calls `pg_try_advisory_lock(<class>, <key>)`, a session-level lock
   ([advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)).
   Failure to acquire means another replica holds the lease, so the worker skips the work.
2. **Fencing.** Every state change the singleton makes in PostgreSQL MUST be executed on that same
   session. The lease and the writes share one session. If the session has died, the lock is gone,
   *and* the write fails, so a replica that lost its lease cannot commit anything. Actions outside
   PostgreSQL happen only after the database change that authorizes them has committed on that
   session. (The S3 deletes of storage maintenance follow R-STOR-024's hand-over rule instead,
   because no database fence can stop a request that was already sent.)
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
5. Session-level locks need a session that stays on one server connection, which
   transaction-mode and statement-mode poolers break. Only three things therefore connect to the
   *session host* (`database.session_host`, R-HA-058): the lease pool of `worker` replicas,
   `trove migrate` (the migration lock) and `trove backup` (the backup's snapshot and export
   connections, and the restore's writes). The session host is PostgreSQL directly, or a
   session-mode pooler such as PgBouncer with `pool_mode = session`. `web` replicas hold no
   coordination session and no session connection at all: their only class is the main pool,
   which MAY go through a transaction-mode pooler (Q-HA-6). A session connection that lands on a
   transaction-mode pooler is refused before its first use ([architecture.md](architecture.md)
   R-ARCH-017). That is a guard, not a proof; the supported setups are the ones named here.

**Lock key registry.** Every advisory lock uses the two-argument form with a registered category as
`classid`, as fixed by the registry of [architecture.md](architecture.md) R-ARCH-017, so `pg_locks`
names every lock (ADR-077). This page uses two of its categories and keeps the retired one listed:

| `classid` | Category | Key (`objid`) | Used by |
|---|---|---|---|
| 1 | `lease` | registered lease number of the task (`internal/cluster/locks.go`; `scheduler` = 1) | singleton leases of this requirement and R-HA-005 |
| 2 | `fetch` (retired, reserved) | — | Nothing since 2026-10-28: the fetch lock lives in Valkey ([architecture.md](architecture.md) R-ARCH-033, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026). The number stays reserved and is never reused |
| 3 | `migration` | 0 | schema migration, exclusive (R-HA-019); the database phase of a backup, shared (R-HA-060) |

Configuration:

```yaml
database:
  lease_pool_size: 4        # default (proposed): concurrent singleton leases per replica
  lease_check_interval: 5s  # default (proposed)
```

Acceptance criteria:

- With the `scheduler` lease held by worker A,
  `SELECT classid, objid, pid FROM pg_locks WHERE locktype = 'advisory' AND classid = 1 AND objid = 1`
  shows one row; terminating that backend (`pg_terminate_backend`) stops A's scheduler tick within
  `lease_check_interval` + 1 s, A logs the lease loss (`trove_lease_lost_total{task="scheduler"}`
  rises), no further `schedule` update or job insert from A's scheduler appears, and another worker
  holds the lease within `cluster.lease_retry_interval` + 1 s.
- A fault-injection test blocks A's traffic to PostgreSQL (NetworkPolicy) while A holds the
  `scheduler` lease and a schedule `* * * * *` is due. A commits no `schedule` update and no job
  insert after its session is gone, B takes the lease after the server releases the session, and
  every minute's occurrence is enqueued exactly once.
- With three workers and no other singleton work running, a GC run and a retention run in progress
  hold no advisory lock of category 1 (`pg_locks` shows only the `scheduler` lease), and their job
  rows carry an exclusive dedup key (Q-STOR-13).

## Failure modes

### R-HA-007 · Failure-mode contract
Priority: **MVP** · Covers: HA-003, HA-004

trove MUST behave as in this table. Each row is exercised by the failure-injection suite
(R-HA-052). "Client effect" assumes at least two replicas.

| Failure | Detection | trove behavior | Client effect | Recovery |
|---|---|---|---|---|
| Container crash (panic, OOM kill) | kubelet; session end releases locks | In-flight requests of that pod end; nothing half-committed (a `201` means durable, R-STOR-004); its job leases expire; its advisory locks are released immediately; its Valkey locks expire after their TTL | Requests on that pod fail with connection reset or `502` from the proxy; npm (`fetch-retries`) and NuGet clients retry | kubelet restarts the container; the spool directory is emptied at start (R-HA-012) |
| Pod eviction, node drain, rolling restart, autoscaler scale-down | `SIGTERM` | Drain (R-HA-011): readiness fails at once, in-flight requests finish within the grace period, jobs are released | None for requests shorter than the grace period; longer transfers are cut (tolerance in R-HA-059) | PDB keeps all but one replica per role serving (R-HA-032); the capacity rule of R-HA-059 keeps the rest from overload |
| Node loss (no `SIGTERM`) | Node controller marks the node not ready | Endpoints of the lost pods are removed when their Ready condition flips; job leases expire; advisory locks are released by the server's TCP keepalive, which trove sets on its lock-holding sessions (R-HA-013, R-HA-015); Valkey locks expire | Requests routed to the lost pods fail until the endpoints are removed (tens of seconds) | Pods are recreated after `nodeFailureTolerationSeconds` of their role (R-HA-032) |
| One replica cut off from PostgreSQL | `database` check (R-OPS-001) | That replica answers `503` and turns not ready once the cut has lasted the check threshold and `health.database_outage_grace` (Q-OPS-31); its singleton work stops (R-HA-006) | Traffic moves to the other replicas | Automatic when the network heals |
| PostgreSQL switchover or failover | Connection errors, SQLSTATE `57P01`, `25006`, class `08` | Retry with backoff within `database.retry.max_elapsed`, pool reset, the change-log poll resumes (an area whose epoch went backwards or was reused is flushed, R-HA-004), singleton work aborts and resumes on the next run (R-HA-009) | Requests stall for up to the retry budget; those that exceed it get `503` with `Retry-After` | Automatic |
| PostgreSQL unavailable beyond the retry budget | `database` check on every replica | Every replica answers `503` and turns not ready; liveness stays `200`, so nothing restarts; authorization decides nothing on a grant index unconfirmed for longer than `authz.cache.max_staleness` (10 s); such requests wait within the retry budget like database calls, then get `503` `authz_stale` ([authorization.md](authorization.md) R-AUTHZ-028, Q-AUTHZ-13) | `503` for everything (the Service has no ready endpoints) | Readiness returns when PostgreSQL does; no manual step |
| PostgreSQL connection limit reached (`53300`) | Connect error | Backoff and retry inside the budget; metric `trove_db_retries_total{kind="too_many_connections"}` | Latency, then `503` | Size the budget (R-HA-016) |
| PostgreSQL saturated: every request connection, or every server connection of the request pooler, busy | `trove_db_pool_acquire_duration_seconds`, alert `TroveDatabasePoolSaturated` | Requests wait for a connection within their deadline; those that exceed it get `503` with `Retry-After`. The control connection keeps the change-log poll, the `database` check and the job heartbeats off the request pool: directly on its own backend, behind a pooler on a pool of its own (R-HA-008, Q-HA-20). Replicas stay ready and keep their jobs | Higher latency, some `503`; no replica leaves the Service because of the load | Scale the database, or lower the load |
| Instance of a transaction-mode pooler stopped or lost (request or control pooler, R-HA-058) | Connection errors of class `08` on the main pool | Retry with backoff (R-HA-009); the new connection reaches another instance of the same pooler through its Service; session-host connections are unaffected | A graceful stop that outlasts trove's connection lifetime: none. A lost instance: only requests whose `COMMIT` was in flight get `503` with `Retry-After` | Automatic |
| Object storage unreachable from all replicas, for any duration | Circuit breaker (R-STOR-033), `objectStore` check, alert `TroveObjectStoreUnreachable` | Behavior per [storage.md](storage.md) R-STOR-036: new uploads `503`, downloads of stored content `503`, metadata, listings, deletes, deduplicated uploads and the API keep working. Running replicas stay ready (R-HA-008, Q-HA-1). A replica that starts during the outage stays not ready until its startup check (R-STOR-035) succeeds; it retries without exiting, so it becomes ready as soon as the store returns | Degraded: reads of metadata, the UI, logins and CI token exchange succeed; blob transfers fail with retryable `503` | Automatic; no blob is marked missing because of errors |
| Object storage unreachable from one replica only | Its circuit opens; peers' do not; alert `TroveObjectStoreUnreachableSingleReplica` | That replica's blob requests fail fast with `503` and `Retry-After`; it stays ready | Blob requests that hit it fail; clients' retries are balanced by the edge proxy and usually reach another replica | Automatic when its network path heals; otherwise the responder deletes the pod |
| S3 throttling (`503 SlowDown`) | SDK retryer | Retries with backoff (R-STOR-033) | Higher latency | The content-addressed key layout spreads load across prefixes (R-HA-050) |
| S3 credentials fail (expired web identity, revoked key) | `AccessDenied`, refresh error | Counted as object-store failure; logged with the credential source, never the secret | As the outage rows | Fix the credential; static key files are re-read (R-STOR-031) |
| Valkey primary lost or switched over (Sentinel failover) | Command errors, timeouts, `READONLY`, closed connections; Sentinel's `switch-master` event as an early hint | Each Valkey use falls back for that call (R-HA-057); the client re-resolves the primary through Sentinel and checks its `ROLE` (R-HA-056); after `valkey.circuit_breaker.failure_threshold` failures the circuit skips Valkey for `open_duration`; replicas stay ready (Q-HA-13) | None; latency rises; a concurrent cache miss may be fetched upstream once per replica instead of once | Automatic, within Sentinel's `down-after-milliseconds` plus the failover and one reconnect backoff |
| Sentinel quorum lost (fewer than a majority of Sentinels reachable) | Sentinel queries fail or time out; `trove_valkey_sentinels_reachable`, the number of configured Sentinels that answered the last probe of all of them (R-HA-056 step 4, R-HA-057) | Existing connections to the primary keep working; a reconnect needs one reachable Sentinel; no failover can happen meanwhile | None while the primary is healthy; if the primary fails too, as the next row | Restore the Sentinels; alert `TroveValkeySentinelsDegraded` |
| Valkey unavailable for every replica, for any duration | Circuit open on every replica, `valkey` check, alert `TroveValkeyUnavailable` | Degraded mode (R-HA-057): fetch collapse and negative caching per replica, rate limits approximated per replica, changes applied at the next change-log poll (R-HA-004); replicas stay ready; a replica started during the outage becomes ready (Q-HA-13) | None; upstream load rises, changes take up to `cluster.epoch_poll_interval` | Automatic; nothing to repair in Valkey |
| Valkey unreachable from one replica only | Its circuit opens; peers' do not; alert `TroveValkeyUnavailableSingleReplica` | That replica runs in degraded mode; it stays ready | None | Automatic when its path heals; otherwise the responder deletes the pod |
| Valkey content lost (flush, restart without persistence, a failover that drops writes not yet replicated) | Not detected, and not needed | A lost fetch lock causes a duplicate upstream fetch, which content addressing makes harmless (INV-11); negative-cache entries are fetched again; rate-limit counters restart; an entry that an older primary brings back is ignored when a newer invalidation covers it (R-ARCH-030) | None | Automatic |
| Valkey rejects trove's credentials or certificate (rotation mistake) | `WRONGPASS`, `NOAUTH`, TLS verification error | Running replicas: as "Valkey unavailable". New replicas: not ready until fixed, because a configuration error gates the first readiness (Q-HA-13), so a rollout stalls instead of degrading every replica | None from running replicas | Fix the Secret or the Valkey user |
| Backup pod evicted, crashed or cut off during a run | Pod failure; Job controller | A drain eviction is not counted against `backoffLimit` (R-HA-060); the Job starts a new pod; an incomplete run is never listed as a backup ([backup-and-restore.md](backup-and-restore.md)) | None; serving replicas are not involved | Automatic; `TroveBackupFailed` when a run ends `failed` or `abandoned` (an `interrupted` run does not count), `TroveBackupStale` when no run succeeds within 1.5 × `backup.expected_interval` (R-HA-045) |
| Restore pod evicted, crashed or cut off during a restore | Pod failure; Job controller | A drain eviction is not counted against `restore.backoffLimit`; the Job's next pod resumes from the restore anchor ([backup-and-restore.md](backup-and-restore.md) R-BAK-021); `trove migrate` refuses the anchored database meanwhile (R-HA-019) | None beyond the restore's own downtime; both roles are at zero | Automatic; a Job that failed for good is deleted and created again (R-HA-060) |
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
- **Valkey never gates readiness after the first one** (INV-12, Q-HA-13). The `valkey` check runs
  every `valkey.health_check_interval` and is reported in verbose `/readyz`
  (`"degraded":["valkey"]`), in the replica registry and as `trove_valkey_available`. Before a
  replica is ready for the first time, only a *configuration error* holds it back: Valkey or a
  Sentinel rejects the credentials (`WRONGPASS`, `NOAUTH`, an ACL denial), TLS verification fails,
  or every reachable Sentinel answers that it does not know `valkey.sentinel.primary_name`.
  Unreachable Sentinels or data nodes never hold a replica back, so a replica started during a
  Valkey outage becomes ready in degraded mode, and the autoscaler and a drain can still add
  capacity. A rollout with a mistyped Valkey setting stalls with `maxUnavailable: 0` instead of
  degrading every replica.
- **Load never fails a check.** The `database` check ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-001) and the change-log poll behind the `invalidation` check run on the replica's control
  connection ([architecture.md](architecture.md) R-ARCH-019), never on the request pool. A request
  pool that is exhausted under load therefore makes requests wait or fail with `503`, but never
  takes replicas out of the Service one after another, which would push their load onto the rest
  and turn overload into an outage. Behind a transaction-mode pooler, the control connection uses
  a server pool of its own for the same reason (R-HA-058, Q-HA-20).
- **A switchover does not empty the Service.** The `database` and `schema` checks tolerate a
  PostgreSQL outage shorter than `health.database_outage_grace` (default 20 s, always below
  `database.retry.max_elapsed`) once a replica has been ready, and show `degraded` meanwhile
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-001, Q-OPS-31). A
  CloudNativePG switchover of up to 15 s therefore never flips readiness, while an outage of 30 s
  does, about 20 to 25 s in.
- **No check undercuts the PostgreSQL window.** The `invalidation` check of
  [architecture.md](architecture.md) R-ARCH-018 also depends on PostgreSQL, through the change-log
  poll. It MUST NOT turn a replica unready sooner than the `database` check does. Its evaluations
  start failing only once the poll has been stale for `cluster.epoch_max_staleness` (default 5 s),
  and it is debounced with the same thresholds as `database` (`health.failure_threshold`,
  `health.success_threshold`, [operations-and-observability.md](operations-and-observability.md)
  R-OPS-001). For one outage, `invalidation` therefore turns a replica unready no earlier than
  `database`, which keeps architecture.md's 5 s default. Otherwise a switchover of a few seconds
  would empty the Service through `invalidation`, although requests ride it out in their retry loop
  (R-HA-009). Raising `cluster.epoch_max_staleness` to the readiness window (about 15 s) would be
  the alternative, at the cost of a slower readiness signal for a replica cut off from the change
  log. Code paths that must fail closed sooner, such as authorization
  ([authorization.md](authorization.md) R-AUTHZ-028), read the change-log freshness directly
  instead of relying on readiness.
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
- A test endpoint of the test build holds every request connection of every `web` replica in
  `SELECT pg_sleep(4)` transactions for 60 s, once with direct connections and once with the
  pooler profile (R-HA-058), where it also occupies every server connection of the request pooler.
  `/readyz` answers `200` on every replica throughout, and no `worker` job returns to `queued`
  because a heartbeat was late.

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
- With a transaction-mode pooler (R-HA-058), `database.host` names the pooler, and the pooler
  follows the primary. A failover then reaches trove as errors on pooled connections (class `08`,
  `25006` from a server connection that the pooler opened before the switch), which the table below
  retries. The session host follows the rules above.

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

- The change-log poll resumes on the control connection, a reserved connection of the main class.
  An area whose epoch went backwards because the failover lost committed transactions, or whose
  epoch number was reused by later writes, is flushed (R-HA-004).
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

- The chart sets `terminationGracePeriodSeconds` per role (default 120 s for `web`, 300 s for
  `worker`). The server shutdown timeout ([architecture.md](architecture.md)) MUST be derived
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
- A fetch that the replica leads for other waiters (R-REPO-026) finishes within the shutdown timeout
  or is abandoned; either way the replica releases its Valkey fetch lock explicitly (delete only if
  the lock still carries this replica's token), so waiters on other replicas take over at once
  instead of after the lock's TTL. From `SIGTERM` on, the replica holds no new cross-replica
  flight; a request on it joins a flight held elsewhere or fetches under its in-process
  single-flight only ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026, Q-REPO-7).
- Leases are released explicitly, and the lease connections, the Valkey connections and the pools
  are closed last (R-ARCH-024). One exception: the `scheduler` lease is released at `SIGTERM`
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-001 shutdown step 1,
  R-OPS-019), not after the running jobs, so a worker drain hands over scheduling within
  `cluster.lease_retry_interval` instead of after the worker grace period. Leases that fence a
  running singleton job stay with that job until it ends or is released.
- A transfer that outlasts `server.shutdown_timeout` is cut at the deadline
  ([architecture.md](architecture.md) R-ARCH-024 step 5, Q-ARCH-18). A download is detectable as
  cut (`Content-Length`) and resumes with `Range` and `If-Range` on any replica; an upload whose
  body was still arriving leaves nothing committed and is retried by the client. An upload whose
  body has fully arrived is not cut before its status line: it may run on into the 5 s margin of
  the grace period for at most 3 s, so a commit whose answer is lost never turns the client's retry
  into a version conflict, and the process still exits before `SIGKILL`. Operators size the grace
  period to their p99 transfer duration (R-HA-050); R-HA-059 states the tolerance that the drain
  test applies.

Acceptance criteria:

- `kubectl rollout restart deploy/trove-web` during a loop of `curl -T 500mb.bin …/files/generic-local/<n>.bin`
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
| Lease locks of a dead `worker` process (the only advisory locks a replica holds; `web` replicas hold none) | Released when its lease session ends; on node loss after the server's TCP keepalive, which trove sets per session (R-HA-013, R-HA-015) |
| A `replica` row with a stale heartbeat | Shown as `lost`, pruned after 24 h (R-OPS-003) |
| Remote-fetch collapse locks in Valkey | Expire with their Valkey TTL (`cluster.flight_lock_ttl`, 30 s), or are released at graceful shutdown (R-HA-011); waiters fetch themselves after their wait bound ([architecture.md](architecture.md) R-ARCH-033, R-REPO-026) |
| Negative-cache entries and counters the dead process wrote to Valkey | Nothing to remove: every key carries a TTL, and readers validate what they read (R-ARCH-030) |
| Partial output of a backup run | Never listed as a backup; removed by the next run or by retention ([backup-and-restore.md](backup-and-restore.md)) |

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

### R-HA-059 · Node drain without outage
Priority: **MVP** · Covers: HA-009, HA-047

A node drain (`kubectl drain`, a node-pool upgrade, a cluster-autoscaler scale-down) MUST cause no
failed client request when the installation follows the rules below (INV-12). The rules define
what "sized for it" means.

**Rules.**

1. **Capacity is N + 1 per role.** For each role, and each pool of R-HA-062:
   `minimum replicas ≥ ceil(peak load ÷ capacity of one replica) + maxUnavailable of its PDB`.
   The capacity of one replica comes from the reference load test (R-HA-050). Kubernetes evicts
   before the replacement exists, so the remaining replicas carry the peak alone until the
   replacement is ready. The autoscaler may add replicas above the minimum, but the rule does not
   count on it reacting during a drain. The defaults (three `web`, two `worker`, Q-HA-16) satisfy
   the rule for a peak that two `web` replicas carry.
2. **One pod per role at a time.** Each role's PDB allows one unavailable pod (`maxUnavailable: 1`,
   R-HA-032). `kubectl drain` retries a refused eviction, so a node that hosts several pods of one
   role is drained one pod after the other, each after the previous replacement is ready.
3. **Replacements can schedule.** Both roles spread over nodes with `DoNotSchedule` and
   `nodeTaintsPolicy: Honor` (R-HA-032). A cordoned node carries the
   `node.kubernetes.io/unschedulable` taint. With the default `nodeTaintsPolicy: Ignore` it would
   still count as a spreading domain holding zero pods, and the replacement of a role with one pod
   on each of three nodes would stay `Pending`
   ([topology spread constraints](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/#spread-constraint-definition):
   `nodeTaintsPolicy` defaults to `Ignore`, is on by default since Kubernetes 1.26 and GA since
   1.33). The remaining nodes need free resources for one pod of each role that is being drained.
4. **The endpoint is gone before the listener closes.** The replica fails readiness at `SIGTERM` and
   keeps serving for `health.shutdown_drain_delay` (R-HA-011). The delay MUST exceed the time the
   edge proxy needs to stop sending to an endpoint whose readiness flipped; otherwise the edge sends
   new requests to a closed listener, and clients see `502`. The drain test measures that time for
   each edge of R-HA-039 and fails when the delay is shorter.
5. **Dependencies drain by their own rules.** trove's guarantee holds only when the services on the
   drained node are drained safely as well:

   | Service on the node | What makes its drain safe |
   |---|---|
   | PostgreSQL instance | CloudNativePG switches the primary over before it drains the primary's node ([Kubernetes upgrade and maintenance](https://cloudnative-pg.io/docs/1.30/kubernetes_upgrade)); with `dataDurability: required` at least three instances, so a drained standby leaves a synchronous standby (R-HA-013). Other platforms: their maintenance procedure |
   | Pooler instance (R-HA-058), request or control pooler | At least two instances of each pooler on different nodes, a PDB, PgBouncer 1.23 or later, whose `SIGTERM` waits for clients to disconnect ([signals](https://www.pgbouncer.org/usage.html)), and a grace period longer than trove's pooled connection lifetime |
   | Valkey primary | A replica and a Sentinel majority on other nodes. Sentinel fails over after `down-after-milliseconds`, and trove runs degraded meanwhile (R-HA-057). Running `SENTINEL FAILOVER` before the primary stops shortens that window |
   | Valkey replica, Sentinel | A PDB with `maxUnavailable: 1` from whoever runs Valkey; three Sentinels on three nodes keep a majority |
   | S3-compatible store, edge proxy | The platform's; both must run highly available |
   | A running backup pod | Evicted without a disruption budget; the Job starts the run again (R-HA-060) |

6. **Long transfers.** A transfer on an evicted replica that lasts longer than
   `server.shutdown_timeout` (grace period − drain delay − 5 s, R-HA-011) is cut, and its client
   sees a connection reset. This is the one client effect a drain may have. Downloads resume with a
   `Range` request; uploads are repeated by the client. Installations that cannot accept it raise
   `terminationGracePeriodSeconds` to their p99 transfer duration (R-HA-050).

**Acceptance test.** Nightly in CI on the kind profile of R-HA-051 with three worker nodes; `web`
runs 3 replicas, the chart default, set with `--set web.replicaCount=3` on top of
`values-dev.yaml`, and `worker` 2. PostgreSQL and Valkey run on nodes other than the drained one,
except where a variant says otherwise. The load starts 5 minutes before the drain and runs until
10 minutes after `kubectl drain` returns:

| Load | Rate |
|---|---|
| `npm ci --cache "$(mktemp -d)"` of a fixture with 400 locked dependencies through the virtual repository `npm-all` (remote and local members, cache warm) | 8 parallel loops |
| `dotnet restore --no-cache --force` of a fixture with 60 packages through `nuget-all` | 4 parallel loops |
| `npm publish` of a new version to `npm-local`; `dotnet nuget push` of a new version to `nuget-local` | one each every 10 s |
| k6 `constant-arrival-rate` against the edge: 50 % npm packument GETs, 30 % npm tarball GETs of 1–5 MB, 20 % NuGet registration and flat-container GETs | 150 requests/s |
| Long-transfer probes: `curl --limit-rate 2M --retry 5 --retry-all-errors -C - -o big.bin …/files/generic-local/1gib.bin` (about 512 s, longer than the `web` grace period) | 2 parallel loops |

The drain is `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data --timeout=20m` of a
node that hosts at least one `web` and one `worker` pod. Pass conditions:

- every `npm` and `dotnet` command exits `0` with the clients' default retries;
- k6 records no response other than `2xx` or `304` and no connection error (k6 does not retry);
- the p99 latency of k6 requests from the first eviction until 2 minutes after the drain is at most
  twice the p99 of the warm-up;
- every acknowledged publish installs afterwards;
- a long-transfer probe is cut at most once per evicted `web` pod that served it, completes through
  resume, and its SHA-256 matches; a cut on a pod that was not evicted fails the test;
- the kubelet events show no `SIGKILL` of a trove container;
- every job that ran on an evicted `worker` pod finishes on another one with `attempt` unchanged
  (R-HA-011);
- no trove pod stays `Pending` for longer than 60 s, and `kubectl drain` returns within 10 minutes.

Variants, with the same load and pass conditions:

| Variant | The drained node also hosts | Additional pass condition | Runs |
|---|---|---|---|
| A | nothing else | — | every change to `main` (2 minutes of load before and 3 after) and nightly |
| B | the Valkey primary and one Sentinel | `trove_valkey_primary_changes_total` rises on every replica; no replica turns unready | nightly |
| C | the PostgreSQL primary | CloudNativePG switches over before the eviction | nightly |
| D | an instance of the transaction-mode pooler (pooler profile, R-HA-058) | — | nightly |
| E | a running backup pod (R-HA-060) | the backup Job completes, and the eviction is not counted against `backoffLimit` | nightly |

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
| Synchronous replication | Recommended, with one synchronous standby | Without it, a failover can lose transactions that were acknowledged as `201`. With CloudNativePG: `postgresql.synchronous: {method: any, number: 1, dataDurability: required}` ([replication](https://cloudnative-pg.io/docs/1.30/replication)); `required` pauses writes when no standby is available, `preferred` keeps writing and risks that loss. With `required`, run at least three instances, so that a drained or lost standby still leaves a synchronous standby and a node drain never pauses writes (R-HA-059) |
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
- The runtime role needs no privilege for advisory locks.
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
  - when `database.backup_role` is set (migration Job only, after the grants above), that the role
    exists and holds `SELECT` on every table and sequence of the schema, `INSERT`, `UPDATE` and
    `DELETE` on `backup_run` and `backup_blob_pin`, and `INSERT` on `audit_event`. `trove backup
    create` checks the same grants at start and exits `2` naming a missing one (R-BAK-015).
- Managed services: on Amazon RDS the administrator account is not a true superuser, and the SQL
  above runs as that account unchanged. With CloudNativePG, `bootstrap.initdb.owner` creates the
  owner; the two runtime roles are added through the cluster's managed roles or with the SQL above.
- **Backup and restore credentials (R-HA-060).** The backup CronJob uses `secrets.databaseBackup`
  when it is set, otherwise the runtime credentials. The restore Job uses the migration credentials
  (the runtime credentials in single-role mode), because a restore creates schema objects and
  disables triggers. The privileges each command needs are listed in
  [backup-and-restore.md](backup-and-restore.md) R-BAK-015. A dedicated backup login is
  recommended: the backup pod also holds the backup target's credentials, and with its own login it
  cannot change trove's data beyond its own run records. That login is not read-only, because a run
  records itself, pins blobs and audits its outcome. The database administrator creates it once,
  with connect and schema usage only:

  ```sql
  CREATE ROLE trove_backup LOGIN PASSWORD :'backup_password';
  GRANT CONNECT ON DATABASE trove TO trove_backup;
  GRANT USAGE ON SCHEMA trove TO trove_backup;
  ```

  When `database.backup_role` names it, `trove migrate` (as the owner) grants the rest after
  applying the steps, idempotently on every run, so tables added by later migrations are covered:

  ```sql
  GRANT SELECT ON ALL TABLES IN SCHEMA trove TO trove_backup;
  GRANT SELECT ON ALL SEQUENCES IN SCHEMA trove TO trove_backup;
  GRANT INSERT, UPDATE, DELETE ON backup_run, backup_blob_pin TO trove_backup;
  GRANT INSERT ON audit_event TO trove_backup;
  ALTER DEFAULT PRIVILEGES IN SCHEMA trove GRANT SELECT ON TABLES TO trove_backup;
  ```

  The default privileges cover tables the owner creates between two migration runs, such as the
  monthly `audit_event` partitions of `audit_maintain_partitions()`, so the export's `LOCK TABLE`
  never meets a partition the backup role cannot read.
- **Role defaults with a transaction-mode pooler (R-HA-058).** Per-session settings cannot travel
  as startup parameters through the pooler. The administrator sets them on each runtime login, not
  on the group role `trove_app`, because role-level settings are not inherited by members
  ([ALTER ROLE](https://www.postgresql.org/docs/current/sql-alterrole.html)):

  ```sql
  ALTER ROLE trove_app_a IN DATABASE trove SET statement_timeout = '30s';                   -- example: database.session.statement_timeout
  ALTER ROLE trove_app_a IN DATABASE trove SET lock_timeout = '5s';                         -- example
  ALTER ROLE trove_app_a IN DATABASE trove SET idle_in_transaction_session_timeout = '60s'; -- example
  ALTER ROLE trove_app_a IN DATABASE trove SET search_path = trove;                         -- example: database.schema
  -- the same four statements for trove_app_b
  ```

Configuration:

```yaml
database:
  schema: trove               # default (proposed)
  backup_role: ""             # default (proposed): empty = no backup grants; example: trove_backup (R-BAK-015)
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
- With `database.backup_role: trove_backup`, after `trove migrate` a backup with the
  `trove_backup` credential completes, and `psql -U trove_backup -c 'DELETE FROM trove.job'` fails
  with `42501`. After `REVOKE INSERT ON trove.audit_event FROM trove_backup` by hand, the next
  migration Job grants it again.

### R-HA-015 · Connection settings and TLS
Priority: **MVP** · Covers: HA-061, HA-063

**TLS material reload (R-ARCH-035).** The PostgreSQL root CA, client certificate and key named by `ssl_root_cert_file`,
`ssl_cert_file` and `ssl_key_file` are read again for each new connection, so a rotated file applies to the next
connection without a restart. A file that fails to load keeps the connections that already use the old material and
is reported as a reload failure.

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
- `application_name` is `trove:<pod name>:<class>` with `<class>` in `main`, `lease`, `migrate`
  and `backup` (the restore Job uses `backup` as well), truncated to 63 bytes
  ([architecture.md](architecture.md) R-ARCH-019 owns the classes). This makes every session
  attributable in `pg_stat_activity`.
- Per-session settings, sent as startup parameters on direct and session-host connections. Through
  a transaction-mode pooler the main pool sends only `application_name` and `TimeZone`, which
  PgBouncer tracks; PgBouncer refuses other startup parameters
  ([`track_extra_parameters`](https://www.pgbouncer.org/config.html)). The other settings then come
  from role defaults (R-HA-014), and preflight verifies them (R-HA-058):

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
  replica, and release its advisory locks (R-HA-006), within `idle + interval × count` (about
  2 minutes with the defaults), the bound R-HA-013 recommends for the server setting. They are sent
  on every session that connects to the session host, which includes the lease pool that holds
  session-level locks.

Configuration:

```yaml
database:
  host: trove-db-rw.trove.svc     # example; comma-separated list allowed (R-HA-009); the pooler with R-HA-058
  port: 5432                          # default (proposed)
  session_host: ""                    # default (proposed): empty = host; the direct host with R-HA-058
  session_port: 0                     # default (proposed): 0 = port
  control_host: ""                    # default (proposed): empty = host; the control pooler with R-HA-058 (Q-HA-20)
  control_port: 0                     # default (proposed): 0 = port
  control_name: ""                    # default (proposed): empty = name; a PgBouncer database entry of its own
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

- `SELECT application_name, ssl FROM pg_stat_activity JOIN pg_stat_ssl USING (pid) WHERE application_name LIKE 'trove:%'`
  shows `ssl = true` for every trove session.
- Against a server whose `tcp_keepalives_*` settings are left at `0` (operating-system default),
  R-HA-006's partition test passes, and the other replica takes the lease within 150 s of the
  partition.
- With `sslMode: verify-full` and a host name not in the server certificate, startup fails with a
  TLS verification error naming the host.
- `kubectl exec <pod> -- ls /var/run/secrets/trove/database-ca/` lists `ca.crt` only.

### R-HA-016 · Connection budget and pool sizing
Priority: **MVP** · Covers: HA-062

A replica opens PostgreSQL connections from these sources. [architecture.md](architecture.md)
R-ARCH-019 owns the pools; this requirement owns the budget.

| Source | Key | Default (proposed) | Roles | Connects to |
|---|---|---|---|---|
| Main pool (class `main`) | `database.pool.max_conns` | 20 | `web`, `worker` | `database.host`, the pooler with R-HA-058 |
| Lease pool (class `lease`, R-HA-006) | `database.lease_pool_size` | 4 | `worker` | session host |

There is no listener session and no fetch-collapse lock pool any more: invalidations travel through
the change log on the main pool (R-HA-004), and fetch collapse takes a Valkey lock since INV-11
changed ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026,
[architecture.md](architecture.md) Q-ARCH-14). There is no coordination session either, so a `web`
replica opens nothing but its main pool. Jobs add the migration Job (2 connections) and the
backup Job (`backup_conns` = 2 + `backup.database.parallel`, 4 with defaults,
[backup-and-restore.md](backup-and-restore.md) R-BAK-005), counted once per cluster because one
backup runs at a time (R-BAK-010), both on the session host. The restore Job uses 2 connections and
is not counted, because no replica runs while it does (R-HA-060). The main class includes one
reserved control connection per replica inside `database.pool.max_conns`
([architecture.md](architecture.md) R-ARCH-019), so the formula below does not change.
`database.pool.max_conns` must therefore be at least 2: the replica refuses a smaller value at
start-up and names the key (R-ARCH-019), and the `config` schema carries the same minimum, so
`helm template` refuses it first (R-HA-029). Behind a transaction-mode pooler the control
connection reaches the server through the control pool of R-HA-058, whose server connections
`database.pooler.server_connections` includes.

The PostgreSQL connection limit MUST satisfy:

```text
web_max      = Σ over web Deployments (autoscaling.enabled ? maxReplicas : replicaCount) + rollingUpdate.maxSurge
worker_max   = Σ over worker Deployments, the same terms
session      = worker_max × database.lease_pool_size
pooled       = database.pooler.mode == transaction
                 ? database.pooler.server_connections           (declared, R-HA-058: request and control pools)
                 : (web_max + worker_max) × database.pool.max_conns
budget       = pooled + session
               + 2                                              (migration Job)
               + backup_conns                                   (backup Job: 2 + backup.database.parallel, once per cluster)
               + database.preflight.reserved_connections        (operators, tools, monitoring)
               + superuser_reserved_connections                 (server setting, default 3)
max_connections ≥ budget
```

Examples:

- Defaults, direct connections, `web` 3 and `worker` 2 replicas with `maxSurge: 1`
  (`web_max` 4, `worker_max` 3): 7 × 20 + 3 × 4 + 2 + 4 + 10 + 3 = **171**, with no `web_max × 1`
  term, because `web` replicas hold no session connection. The PostgreSQL default of 100 is too
  small, so the chart's NOTES print the computed budget.
- Large profile (R-HA-050), `web` up to 16 and `worker` up to 6, `max_conns: 30`: without a pooler
  24 × 30 + 7 × 4 + 19 = **767**; with a transaction-mode pooler declaring 100 server connections,
  100 + 28 + 19 = **147**.

**Growth with the replica count (INV-12).** Without a pooler, every added replica adds its whole
main pool. With the pooler, the main pools add nothing at the server: an added `web` replica adds
no server connection at all, and an added `worker` replica adds its lease pool. The server-side
count then depends only on the number of `worker` replicas, which follow job load and stay small
(Q-HA-6).

- The chart passes `web_max` and `worker_max` to the migration Job
  (`TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS`, `TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS`).
  The Job compares the budget with `SHOW max_connections`. With
  `database.preflight.connection_budget: enforce` (default) a shortfall fails the Job, and with it
  `helm upgrade`, before the larger deployment exists. With `warn` it logs and continues.
- trove cannot read the pooler's limits. With the pooler, `NOTES.txt` prints the client connections
  each pooler must accept, `(web_max + worker_max) × database.pool.max_conns` for the request pool
  and `web_max + worker_max` for the control pool, next to the budget, and R-HA-058 lists the
  pooler settings that must match.
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

- Against a server with `max_connections = 100` and the default values, `helm upgrade --install`
  fails in the migration Job with a message that names 171 required and 100 available.
- Under the reference load test (R-HA-050, medium profile),
  `max(trove_db_pool_connections{state="acquired"})` stays below `max_conns`, and
  `SELECT count(*) FROM pg_stat_activity WHERE usename LIKE 'trove%'` never exceeds the computed
  budget.
- With the pooler profile (R-HA-058), scaling `web` from 2 to 12 replicas leaves
  `SELECT count(*) FROM pg_stat_activity WHERE usename LIKE 'trove%'` at or below the declared
  `database.pooler.server_connections` plus the `worker` leases and the one-off connections.

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
   Existing connections end within `max_conn_lifetime` + jitter. Lease connections are recycled on
   the same schedule, but only between singleton runs.
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

### R-HA-058 · Transaction-mode pooler for the main pool
Priority: **MVP** · Covers: HA-062

The main pool MAY reach PostgreSQL through a transaction-mode pooler, so that the server-side
connections of the main pools stay fixed however many replicas run (INV-12, Q-HA-6). Direct
connections stay supported and remain the default.

- `database.pooler.mode` says what `database.host` is: `none` (default) for PostgreSQL itself,
  `session` for a session-mode pooler (trove behaves as with a direct connection), `transaction`
  for a transaction-mode pooler. Statement-mode poolers are not supported for any class
  ([architecture.md](architecture.md) R-ARCH-019): they refuse transactions of more than one
  statement, and trove's write paths run exactly such transactions.
- With `transaction`, every connection that needs session state connects to the *session host*
  (`database.session_host`, `database.session_port`): the lease pool of `worker` replicas, the
  migration Job, the backup Job and the restore Job (the session classes of R-ARCH-019). There is
  no coordination session; `web` replicas never connect to the session host. The
  session host is PostgreSQL or a session-mode pooler. An empty `database.session_host` is refused
  at startup and by the chart (R-HA-029), because session work would then land on the pooler.
- **The control connection has a server pool of its own** (Q-HA-20). The reserved control
  connection of [architecture.md](architecture.md) R-ARCH-019 carries the change-log poll, the
  `database` check, the registry heartbeat and the job heartbeats. Its client-side reservation does
  not reach the server through a shared pooler: when every server connection of the request
  pooler is busy, the control queries of every replica would queue behind request transactions,
  every replica would fail `database` and `invalidation` at once, and workers would lose job
  leases. With `transaction`, the control connection therefore connects to the *control endpoint*
  (`database.control_host`, `database.control_port`, `database.control_name`): a second pooler
  whose server pool serves only control connections (with CloudNativePG, a second `Pooler`
  resource), or a database entry of a self-managed PgBouncer with its own `pool_size`. The chart
  refuses a control endpoint equal to the request endpoint (R-HA-029). Its load is one poll per
  replica per `cluster.epoch_poll_interval` plus the heartbeats and checks, each a short statement,
  so a few server connections serve hundreds of replicas. Empty control keys mean the request endpoint,
  which is correct for `none` and `session`, where the control connection has a server backend of
  its own.
- **Nothing on the main pool keeps session state:** no `SET` without `LOCAL`, no session-level
  advisory lock, no `LISTEN`, no temporary table, no `WITH HOLD` cursor, no SQL-level `PREPARE`.
  [architecture.md](architecture.md) R-ARCH-019 enforces this with a test over the queries.
  Transaction-scoped advisory locks (`pg_advisory_xact_lock`) work through the pooler.
- **Settings.** Through the pooler, the main pool sends only `application_name` and `TimeZone` as
  startup parameters (R-HA-015). `statement_timeout`, `lock_timeout`,
  `idle_in_transaction_session_timeout` and `search_path` come from role defaults on each runtime
  login (R-HA-014), and request transactions still set `SET LOCAL statement_timeout`
  (R-ARCH-019). At startup, preflight reads the four settings on a pooled connection of the request
  endpoint and of the control endpoint, and refuses to start, naming the login and the setting,
  when one differs from the configured value.
- **Prepared statements.** pgx caches protocol-level prepared statements per connection. PgBouncer
  supports them in transaction mode while `max_prepared_statements` is above 0, which its
  documentation gives as default 200
  ([`max_prepared_statements`](https://www.pgbouncer.org/config.html)). With a pooler that does not,
  the main pool's exec mode is switched as [architecture.md](architecture.md) R-ARCH-019 states.
- **The pooler is highly available.** Each pooler (request and control) runs at least two instances
  on different nodes behind one Service, with a PodDisruptionBudget that the operator adds
  (CloudNativePG's pooling page names none, checked 2026-10-28), and PgBouncer 1.23 or later, whose
  `SIGTERM` waits for clients to disconnect ([signals](https://www.pgbouncer.org/usage.html)). With
  the pooler, trove's main pool SHOULD use `database.pool.max_conn_lifetime: 5m` and
  `max_conn_lifetime_jitter: 1m`, and the pooler pods' termination grace period MUST exceed their
  sum. A draining pooler instance then loses no client before trove has moved its connections to
  another instance (R-HA-059).
- **Pooler limits.** Each request-pooler instance accepts `max_client_conn ≥ (web_max + worker_max)
  × database.pool.max_conns`, because one instance may receive every client while the other
  drains; each control-pooler instance accepts `max_client_conn ≥ web_max + worker_max`.
  `database.pooler.server_connections` declares the sum over the instances of both poolers of
  `default_pool_size` plus `reserve_pool_size`; R-HA-016 counts it.
- TLS from trove to the pooler follows R-HA-015 (`verify-full` against the pooler's certificate).
  A PostgreSQL failover behind the pooler is handled by R-HA-009.

CloudNativePG example ([connection pooling](https://cloudnative-pg.io/docs/1.30/connection_pooling);
the `template` needs `containers: []` when no container is overridden):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata:
  name: trove-db-pooler-rw                # example
spec:
  cluster:
    name: trove-db                        # example
  instances: 2                            # example; at least 2, on different nodes
  type: rw
  pgbouncer:
    poolMode: transaction
    parameters:
      default_pool_size: "40"             # example; 2 × (40 + 5) = 90 server connections
      reserve_pool_size: "5"              # example
      max_client_conn: "1000"             # example; ≥ (web_max + worker_max) × max_conns
      max_prepared_statements: "200"      # example
  template:
    metadata:
      labels:
        app: trove-db-pooler              # example
    spec:
      containers: []
      terminationGracePeriodSeconds: 420  # example; > max_conn_lifetime + jitter (360 s)
      topologySpreadConstraints:
        - maxSkew: 1                      # example
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app: trove-db-pooler        # example
---
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata:
  name: trove-db-pooler-control           # example; the control pool (Q-HA-20)
spec:
  cluster:
    name: trove-db                        # example
  instances: 2                            # example; at least 2, on different nodes
  type: rw
  pgbouncer:
    poolMode: transaction
    parameters:
      default_pool_size: "5"              # example; 2 × 5 = 10 server connections
      reserve_pool_size: "0"              # example
      max_client_conn: "200"              # example; ≥ web_max + worker_max
      max_prepared_statements: "200"      # example
  template:
    metadata:
      labels:
        app: trove-db-pooler-control      # example
    spec:
      containers: []
      terminationGracePeriodSeconds: 420  # example
      topologySpreadConstraints:
        - maxSkew: 1                      # example
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app: trove-db-pooler-control  # example
```

trove configuration:

```yaml
database:
  host: trove-db-pooler-rw.trove.svc            # example: the request pooler
  control_host: trove-db-pooler-control.trove.svc  # example: the control pooler (Q-HA-20)
  session_host: trove-db-rw.trove.svc           # example: PostgreSQL directly
  pooler:
    mode: transaction                           # example; default (proposed): none
    server_connections: 100                     # example: 90 + 10; default (proposed): 0
  pool:
    max_conn_lifetime: 5m                       # example; recommended with the pooler
    max_conn_lifetime_jitter: 1m                # example
```

Acceptance criteria (pooler profile of R-HA-051, nightly):

- The full R-HA-052 suite passes.
- R-HA-016's growth criterion holds: 10 more `web` replicas add no server connection.
- `kubectl delete pod <pooler pod>` during an `npm ci` loop: no failed iteration. With
  `--grace-period=0 --force`, only requests whose `COMMIT` was in flight answer `503` with
  `Retry-After`, and every client iteration succeeds after its own retries.
- A login without the role defaults makes the replica exit with a message naming the login and the
  setting.
- With `pooler.mode: transaction` and an empty `session_host`, `helm template` fails and names
  `config.database.session_host`; with none of the `control_*` keys set, it fails and names
  `config.database.control_host`.
- R-HA-008's saturation criterion passes in this profile: with every server connection of the
  request pooler held by `pg_sleep(4)` transactions for 60 s, every replica stays ready, and
  `SHOW POOLS` on the control pooler shows no client waiting longer than 1 s.

## Valkey

### R-HA-056 · Valkey with Sentinel: connection and supported topologies
Priority: **MVP** · Covers: trove-only

**TLS material reload (R-ARCH-035).** The CA, client certificate and key of the data nodes and of the Sentinels
(`valkey.tls.*`, `valkey.sentinel.tls`) are read again for each new handshake. A rotated certificate applies to the
next handshake without a restart; a failed read keeps the material that works and is reported.

Valkey holds trove's shared ephemeral state (INV-3); [architecture.md](architecture.md) R-ARCH-030
decides what goes there, and R-ARCH-031 specifies the client. This requirement owns the `valkey`
configuration keys, the supported topologies, the server settings and what the chart wires.
Valkey with Sentinel is an owner constraint (INV-12).

Three settings weaken the connection and are refused unless named: `valkey.sentinel.tls: false`
(plaintext Sentinel connections) and `valkey.tls.mode: disable` each require `valkey.allow_insecure: true`,
and `valkey.key_prefix` must end in `:` (Q-HA-30).

**Supported topologies** (Q-HA-14):

| Topology | Status |
|---|---|
| A Valkey primary with at least one replica and at least three Sentinels, each on a different node; run by an operator, a Helm chart or the platform team, in the cluster or on virtual machines | Supported; the reference topology, which the local profile runs (R-HA-051) |
| A managed Valkey service that exposes Sentinel | Supported, with the same configuration |
| A managed service that offers a primary endpoint whose DNS name follows failover, without Sentinel (for example Amazon ElastiCache with Multi-AZ, [auto failover](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/AutoFailover.html)) | `valkey.mode: endpoint`, P1 (Deferred) |
| Valkey Cluster (sharded) | Not supported (Deferred) |
| One Valkey instance | `valkey.mode: standalone`, development only; the chart refuses it with `highAvailability.enforce` (R-HA-029) |

- **The chart bundles no Valkey and no Sentinel** (R-HA-028), as it bundles no PostgreSQL and no
  object store: production needs them highly available, and their upgrades, failover drills and
  memory sizing belong to whoever runs them. Ways to run them, as of 2026-10-28: the official
  `valkey-io/valkey-helm` chart has Sentinel settings on its main branch but not in its latest
  release, 0.12.1 ([repository](https://github.com/valkey-io/valkey-helm), checked 2026-10-28);
  third-party operators; managed services. The local profile uses manifests of its own (Q-HA-15).
- **Version.** Valkey 9.0 or later, because trove's lock release uses `DELIFEQ`, which exists since
  9.0 ([architecture.md](architecture.md) Q-ARCH-13, R-ARCH-031). CI tests the newest patch of 9.0
  and 9.1. Maintained lines on 2026-10-28: 9.1, 9.0, 8.1, 8.0 and 7.2
  ([download page](https://valkey.io/download/)).

**Server settings.**

| Setting | Requirement | Reason |
|---|---|---|
| `maxmemory` | Set, sized per R-HA-050 | Without it Valkey grows until the container is OOM-killed |
| `maxmemory-policy` | `allkeys-lru` recommended; trove works under every policy, and a refused write counts as unavailable for that call (R-ARCH-030) | Every trove key has a TTL and may be lost (INV-3); under memory pressure the least recently used entries go first. Evicting a rate-limit counter resets that limit early |
| Persistence | Not needed: `save ""` and `appendonly no` are fine | Valkey never holds the only copy (INV-3), so its pods need no PersistentVolumeClaim (INV-13). A primary that restarts empty before Sentinel notices makes its replicas resynchronize to an empty data set, which costs only cache warmth |
| `min-replicas-to-write` | `0` (Valkey's default) | With `1` and a single replica, draining the replica's node stops all writes on the primary |
| Sentinels | At least three, on three nodes, quorum 2 | A majority must authorize a failover ([Sentinel](https://valkey.io/topics/sentinel/)); one drained node leaves a majority |
| Sentinel `down-after-milliseconds` | 5000 recommended | trove runs degraded for about this long when the primary dies (R-HA-057); a much shorter value risks failovers on short stalls |
| ACL | A user for trove, limited to keys `~<key_prefix>*`, channels `&<key_prefix>*` and the command list of [architecture.md](architecture.md) R-ARCH-031, which includes `ROLE` for the Sentinel client protocol and `DELIFEQ` for the script-free lock release, and no scripting command | A leaked trove credential cannot read or flush other applications' data and cannot run scripts or administrative commands ([ACL](https://valkey.io/topics/acl/)) |
| TLS | On, with `tls-replication yes`, so that Sentinels speak TLS as well | Valkey enables a Sentinel's `tls-port` only together with `tls-replication`, and requires client certificates by default (`tls-auth-clients yes`): mount one (`secrets.valkeyClientCert`) or set `tls-auth-clients optional` ([TLS](https://valkey.io/topics/tls/)) |

**Discovery and failover.** trove's Valkey client MUST follow the Sentinel client protocol
([Sentinel clients](https://valkey.io/topics/sentinel-clients/)); the library is
[architecture.md](architecture.md)'s choice and MUST support these steps, TLS and separate
credentials for Sentinels:

1. Ask the configured Sentinels in order, each within `valkey.sentinel_query_timeout`, for
   `SENTINEL get-master-addr-by-name <valkey.sentinel.primary_name>`. The first Sentinel that
   answers moves to the front of the list; a null answer moves on to the next Sentinel.
2. Connect to the returned address, authenticate, and check `ROLE`. Anything other than a primary
   sends the client back to step 1 after a short wait.
3. Re-resolve after every timeout, connection error, closed connection and `READONLY` error. When
   the primary moved, close every pooled connection to the old address, the Pub/Sub subscriber
   included, which subscribes again on the new primary and triggers one change-log poll
   ([architecture.md](architecture.md) R-ARCH-018). Sentinel's `switch-master`
   message is an early trigger only, because messages can be missed; since Valkey 9, coordinated
   failover relies on clients that follow the whole protocol
   ([Sentinel](https://valkey.io/topics/sentinel/)).
4. Reconcile with the Sentinels every `valkey.sentinel.refresh_interval` as well: a demoted primary
   can keep its connections open, and a client that missed `switch-master` would otherwise stay
   bound to it (R-ARCH-031). In the same round, the replica probes every configured Sentinel, each
   within `valkey.sentinel_query_timeout` and concurrently, and sets
   `trove_valkey_sentinels_reachable` to the number that answered. Step 1 stops at the first
   Sentinel that answers, so its count would stay at 1 and could not show a lost majority.
5. Sentinels learned through `SENTINEL sentinels` MAY be added for the lifetime of the process; the
   configured list is never shortened.
6. While no Sentinel answers, connections to a primary that still answers `ROLE` as primary stay in
   use; new connections wait for a Sentinel.

**Connection rules.**

- Every Valkey call carries `valkey.command_timeout`. No Valkey call runs inside a PostgreSQL
  transaction, and no request waits for Valkey longer than that timeout (R-HA-057).
- Every key and every channel name starts with `valkey.key_prefix`, and every key carries a TTL.
  The prefix lets one Valkey serve several installations under ACL key patterns. Names follow the
  shape `<key_prefix><ks>:<mechanism>:v1:…`, with the default prefix `trove:<ks>:<mechanism>:v1:…`,
  where `<ks>` is the installation's keyspace and `v1` the encoding version
  ([architecture.md](architecture.md) R-ARCH-030).
- Each connection carries the client name `trove:<pod name>:<purpose>` (`CLIENT SETNAME`), so
  `CLIENT LIST` attributes it, like `application_name` in PostgreSQL (R-HA-015). A replica holds at
  most six Valkey connections, whatever its request load (R-ARCH-031).
- Credentials come only from files (INV-15): `valkey.username_file` and `valkey.password_file` for
  the data nodes, `valkey.sentinel.username_file` and `valkey.sentinel.password_file` for the
  Sentinels. The chart mounts them from existing Secrets (R-HA-030). Files are re-read for every new
  connection, and connections are recycled after `valkey.max_conn_lifetime` with up to 10 % jitter,
  so a rotated Secret applies without a restart. A rotation without a gap uses an ACL user's
  several passwords: add the new password, update the Secret, wait one connection lifetime, remove
  the old password.
- TLS: `valkey.tls.mode` is `verify-full` (default), `verify-ca` or `disable`; `disable` needs
  `valkey.allow_insecure: true`, otherwise the replica refuses to start. `valkey.sentinel.tls`
  applies the same TLS settings to the Sentinels.

Configuration:

```yaml
valkey:
  mode: sentinel                     # default (proposed): sentinel | standalone (development only); endpoint is P1
  sentinel:
    addresses: []                    # required in sentinel mode; example: [trove-valkey-sentinel-0.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-1.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-2.trove-valkey-sentinel.trove.svc:26379]
    primary_name: ""                 # required in sentinel mode; example: trove (the name in "sentinel monitor")
    username_file: ""                # default (proposed): empty = Sentinels without ACL user; set by the chart
    password_file: ""                # default (proposed): empty = Sentinels without password; set by the chart
    tls: true                        # default (proposed): use valkey.tls for Sentinel connections too
    refresh_interval: 5s             # default (proposed): periodic reconciliation with the Sentinels (R-ARCH-031)
  address: ""                        # standalone mode only; example: localhost:6379
  database: 0                        # default (proposed)
  key_prefix: "trove:"               # default (proposed)
  username_file: ""                  # default (proposed): empty = the "default" user; set by the chart
  password_file: ""                  # set by the chart from secrets.valkey
  tls:
    mode: verify-full                # default (proposed): verify-full | verify-ca | disable
    ca_file: ""                      # default (proposed): empty = system roots plus trustedCA; set by the chart from secrets.valkeyCA
    cert_file: ""                    # default (proposed): no client certificate; set by the chart
    key_file: ""                     # default (proposed)
    server_name: ""                  # default (proposed): the host name of each address
  allow_insecure: false              # default (proposed); required for tls.mode disable
  connect_timeout: 2s                # default (proposed)
  command_timeout: 500ms             # default (proposed)
  sentinel_query_timeout: 300ms      # default (proposed), per Sentinel tried
  max_conn_lifetime: 30m             # default (proposed)
  health_check_interval: 5s          # default (proposed)
  reconnect:
    initial_backoff: 100ms           # default (proposed)
    max_backoff: 5s                  # default (proposed)
  circuit_breaker:
    failure_threshold: 5             # default (proposed): consecutive failed calls that open the circuit
    open_duration: 5s                # default (proposed)
```

Acceptance criteria:

- `CLIENT LIST` on the Valkey primary of the local profile lists connections named
  `trove:<pod>:…` from every `web` and `worker` pod, over the TLS port only (the profile's
  plaintext port is 0).
- A new replica with a wrong `primary_name` stays not ready and logs that no Sentinel knows the
  name; with a wrong password it stays not ready and logs `WRONGPASS`; with every Sentinel
  unreachable at its first start it becomes ready with `"degraded":["valkey"]` (Q-HA-13).
- With `valkey.tls.mode: disable` and `allow_insecure: false`, the replica exits and names both
  keys.
- After a full R-HA-052 run, `valkey-cli --scan` lists no key outside `key_prefix`, and `TTL` of
  1,000 sampled keys never answers `-1`.
- The password rotation above, run during an `npm ci` loop, causes no failed iteration and no pod
  restart.

### R-HA-057 · Valkey failover, outage and degraded mode
Priority: **MVP** · Covers: trove-only

Losing Valkey, or Valkey's contents, costs performance only, never correctness or data, and never
readiness (INV-3, INV-12). [architecture.md](architecture.md) R-ARCH-030 assigns every mechanism
and its fallback; this requirement fixes the contract they share.

1. **A Valkey failure never fails a client request by itself.** Every use of Valkey has a fallback
   that works without it, and nothing that trove serves to a client, and no positive
   authentication or authorization result, is read from Valkey (R-ARCH-030, Q-ARCH-16). The uses
   and their fallbacks:

   | Use | Owner | Fallback while Valkey is unavailable | Cost |
   |---|---|---|---|
   | Fetch collapse across replicas | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026, [architecture.md](architecture.md) R-ARCH-033 | In-process single-flight only | Up to one upstream fetch per replica and object |
   | Negative cache of remote repositories | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-028 | Negative cache: per-replica L1 only ([architecture.md](architecture.md) R-ARCH-030) | More upstream requests for missing paths |
   | Rate-limit counters with cluster scope | [architecture.md](architecture.md) R-ARCH-034 | Per-replica buckets at the configured values divided by the live replicas of the role | Limits hold approximately, not exactly |
   | Change messages | [architecture.md](architecture.md) R-ARCH-018 | The change-log poll ([architecture.md](architecture.md) R-ARCH-018, R-HA-004), bound `cluster.epoch_poll_interval` | Changes take effect within `cluster.epoch_poll_interval` instead of milliseconds |
   | Signals (job wake-ups, metadata refreshes, outbox wake-up) | [architecture.md](architecture.md) R-ARCH-018 | Consumers poll | Up to one poll interval of extra delay |

2. **What Valkey returns is validated.** Valkey can come back with old content: after an outage, or
   after a failover to a replica that missed the last writes, because Valkey replicates
   asynchronously and Sentinel does not guarantee that acknowledged writes survive a failover
   ([Sentinel](https://valkey.io/topics/sentinel/)). A reader therefore checks every value before
   use: a negative-cache entry counts only inside its time window and only if no newer
   invalidation covers it, and every key expires on its own (R-ARCH-030). Deleting a Valkey key is
   never the only invalidation.
3. **Circuit breaker per replica.** After `valkey.circuit_breaker.failure_threshold` consecutive
   failed calls, the replica skips Valkey for `open_duration` and then lets one probe through. While
   the circuit is open, every use runs its fallback at once instead of waiting for a timeout.
4. **Readiness.** As R-HA-008 and Q-HA-13 state: degraded, alerted, never unready after the first
   readiness.
5. **Headroom.** While Valkey is unavailable, remote repositories may fetch an object once per
   replica, negative caching is per replica, and change propagation falls back to the poll. The
   reference load test (R-HA-050) runs once with Valkey stopped and records upstream request rates
   and PostgreSQL load; the profiles leave room for that run.
6. **Nothing to repair afterwards.** When Valkey answers again, the circuit closes after a
   successful probe. No flush is needed, because of rule 2.
7. **Restores move to a new keyspace; nothing is flushed.** After a restore, Valkey's keys describe
   an installation state that no longer exists. A restore assigns a new `system_state.keyspace_id`,
   and every key and channel carries it ([backup-and-restore.md](backup-and-restore.md) R-BAK-022,
   R-ARCH-030), so the restored installation never reads an old key, also on a shared Valkey, and
   needs no access to Valkey to restore. No key is scanned or deleted; the old keys expire by their
   TTLs. The point-in-time restore of R-HA-025 sets a new keyspace ID the same way, because a PITR
   brings back the old ID.

Metrics (R-OPS-007 conventions): `trove_valkey_available` (gauge per replica),
`trove_valkey_circuit_open`, `trove_valkey_sentinels_reachable` (the number of configured Sentinels
that answered the replica's last probe of all of them, run every
`valkey.sentinel.refresh_interval`, R-HA-056 step 4), `trove_valkey_primary_changes_total`,
`trove_valkey_errors_total{kind}` with `kind` in `timeout`, `connection`, `readonly`, `auth`, `tls`,
`oom`, `no_primary` (a Sentinel query named no primary for `valkey.sentinel.primary_name`).

Acceptance criteria. All run under R-HA-052's load on the local profile, whose Sentinels use
`down-after-milliseconds 5000`; "zero failed" means every `npm` and `dotnet` command exits `0` and
k6 records only `2xx` and `304`:

- **Planned failover:** `SENTINEL FAILOVER trove` on one Sentinel. Zero failed;
  `trove_valkey_primary_changes_total` rises by one on every replica; every replica writes to the
  new primary within 10 s.
- **Primary loss:** `kubectl delete pod <Valkey primary> --grace-period=0 --force`. Zero failed;
  every replica reports `valkey` degraded for at most `down-after-milliseconds` plus 15 s;
  `/readyz` answers `200` throughout.
- **Outage:** the Valkey pods and the Sentinels scaled to 0 for 5 minutes, with one `web` pod
  deleted meanwhile. Zero failed; the replacement pod becomes ready in degraded mode;
  `TroveValkeyUnavailable` fires; after the return, `degraded` clears within `open_duration` plus
  `health_check_interval` plus 5 s.
- **Sentinel quorum loss:** with all three Sentinels up, `trove_valkey_sentinels_reachable` is 3 on
  every replica and `TroveValkeySentinelsDegraded` stays silent. The Sentinels are then scaled to 1,
  then to 0, while the primary stays healthy. Zero failed; running replicas do not degrade; the
  gauge drops to 1 and then 0 within `valkey.sentinel.refresh_interval` plus 1 s;
  `TroveValkeySentinelsDegraded` fires.
- **Content loss:** `FLUSHALL` on the primary with a test credential. Zero failed; a counting
  upstream stub records at most one fetch per replica for each object that was in flight.
- **Stale content:** a negative-cache entry written straight into Valkey with a creation time in
  the future, and another older than a covering invalidation, are both ignored, and the request
  reaches the upstream (R-ARCH-030).
- **Slow Valkey:** a fault proxy delays every Valkey command by 2 s. The circuits open, and the p99
  of the k6 load stays below its baseline plus twice `valkey.command_timeout`.

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
  (R-HA-030). With `restore.enabled` the chart renders no migration Job, because the restore
  applies the migrations itself (R-HA-060).
- **Backups and migrations exclude each other.** A backup's database phase holds a long
  transaction whose table locks would block the migration's DDL, which would then exhaust
  `migrate.ddl_lock_retries` and fail the upgrade. The backup therefore holds
  `pg_advisory_lock_shared(3, 0)` during its database phase (R-HA-060), and `trove migrate` takes
  `pg_advisory_lock(3, 0)` exclusively (step 2 below). A migration waits for a running backup's
  database phase up to `migrate.advisory_lock_wait`, then fails with a message that names the
  backup; a backup waits for a running migration. The upgrade preparation (R-HA-021) runs the
  pre-upgrade backup to completion first.
- **Never on a half-restored database.** `trove migrate up` refuses, with exit status `3` and
  without applying anything, while the schema carries a restore anchor or `system_state` holds
  `restore_in_progress` ([backup-and-restore.md](backup-and-restore.md) R-BAK-021), and names the
  backup ID. A restore that was interrupted, for example because an operator turned
  `restore.enabled` off before the restore Job completed, which deletes the Job, is then resumed
  instead of being overtaken: migrations applied beyond the backup's step before the load would make
  the resumed load fail its column comparison. The restore holds the migration lock while it runs,
  so the check runs after step 2 below.
- The Job connects to the session host (`database.session_host`, R-HA-058), because the migration
  lock is a session-level lock.
- The hook ConfigMap carries the same `trove.yaml` as `<fullname>-config` (R-HA-030), so
  `trove migrate` reads `audit.retention_days` and the `database` and `migrate` sections from the
  same configuration file as the replicas.
- The Job runs `trove migrate up`, trove's own runner on native pgx (R-ARCH-026), which does the
  following in order:
  1. preflight (R-HA-013, R-HA-014, R-HA-016);
  2. `pg_advisory_lock(3, 0)` (category `migration`, R-ARCH-017), waiting up to
     `migrate.advisory_lock_wait`, so concurrent Jobs serialize and the second finds nothing to do.
     Every later step runs on that same connection;
  3. refuse a half-restored database (above), then verify the checksums of applied steps;
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
  `ttlSecondsAfterFinished` 86400, so the logs stay readable for a day. `restartPolicy: Never` with
  a `podFailurePolicy`: pod condition `DisruptionTarget` → `Ignore`, so a node drain during
  `helm upgrade` does not use up the retries (the evicted pod's session ends, which releases the
  migration lock and rolls back its open step); exit status `3` → `FailJob`, because a refusal of
  the contract guard or of a half-restored database does not change on retry.
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
- Against a database whose restore was stopped during its blob copy (anchor set), the migration
  Job exits `3`, applies nothing, names the backup ID and ends `Failed` without a retry; the
  restore Job created again afterwards resumes and completes (R-BAK-021).
- Draining the node of a running migration Job pod ends with the Job `Complete` and the
  `helm upgrade` succeeding, also with `migrations.backoffLimit: 0`.

### R-HA-020 · Rolling upgrade and mixed-version operation
Priority: **MVP** · Covers: HA-069, OPS-073

- Both Deployments use `RollingUpdate` with `maxSurge: 1` and `maxUnavailable: 0`, plus
  `minReadySeconds: 10`. Serving capacity never drops below the replica count during an upgrade,
  so the replica counts of the drain rule (R-HA-059) suffice for a zero-downtime upgrade. The
  catalog's recommendation of three nodes comes from upgrades that take a node out of service.
- **Order.** Helm runs the migration hook first (R-HA-019) and then applies the `web` and `worker`
  Deployments together; each rolls on its own.
  No order between them is needed, because both roles tolerate the other role in release N−1
  (below). The backup CronJob takes the new image for its next run; a run already in progress
  finishes on the old one, which the expand-only window allows.
- During the mixed-version window, release N and N−1 serve the same clients alternately, and N−1
  workers may run jobs that N web replicas enqueued. Therefore:
  - Anything a client carries between requests MUST be understood by both releases: pagination
    cursors, `ETag` values, session cookies, CSRF tokens, upload session identifiers.
  - Job payloads and change-log payloads are versioned. Older workers skip unknown job kinds
    (R-HA-005), and older replicas flush the area on an unknown change-log payload (R-HA-004).
  - Valkey keys and channels are versioned the same way: an incompatible change of an encoding
    uses a new version segment in the key (R-ARCH-030), so N and N−1 never read each other's
    incompatible entries; they only miss them for the duration of the rollout.
  - A feature that writes data N−1 would misinterpret MUST stay inactive until the cluster minimum
    version (R-HA-003) is ≥ N. Until then the API answers requests to that feature with `503`
    `cluster_upgrade_in_progress`.
- The rollout pauses on its own when a new pod does not become ready (incompatible schema,
  configuration error, failed key check), because `maxUnavailable: 0` keeps all old pods.
  `kubectl rollout undo` or `helm rollback` returns to the previous release.
- Upgrade verification:
  - `kubectl rollout status deploy/trove-web` and `kubectl rollout status deploy/trove-worker`;
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
| Skipping minors (x.y → x.y+2 or later) | Stepwise through each minor with zero downtime, or in one step with downtime: scale both roles to zero, wait `system.heartbeat_stale_after`, then `helm upgrade`. The contract guard enforces this (R-HA-018, Q-HA-5) |
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
  2. Take a backup and wait for it to complete:
     `kubectl -n trove create job --from=cronjob/trove-backup trove-backup-pre-upgrade` (R-HA-060).
     Where the platform keeps point-in-time recovery, also note the time as a restore point
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
  the message of R-HA-018. After `kubectl scale deploy/trove-web deploy/trove-worker --replicas=0`,
  a 30 s wait and `helm upgrade`, it succeeds without any override.
- A `helm upgrade` started while a backup's database phase runs waits for it; with the phase
  artificially held longer than `migrate.advisory_lock_wait`, the migration Job fails with a message
  that names the backup, and the old pods keep serving.

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

### R-HA-060 · Backup CronJob and restore Job
Priority: **MVP** · Covers: OPS-032, OPS-033

`trove backup create` writes a consistent backup of the service, a transactionally consistent
snapshot of trove's PostgreSQL data with every blob it references, to a configurable S3 target, and
`trove backup restore` restores one into an empty installation (INV-19).
[backup-and-restore.md](backup-and-restore.md) owns the format, the consistency with garbage
collection, the configuration semantics, the exit codes, the retention and the restore procedure
(R-BAK-009 to R-BAK-013, R-BAK-020, R-BAK-021). This requirement is the chart side; R-BAK-012
describes the same objects from the backup's side.

**Backup CronJob.** With `backup.enabled: true` (default, Q-HA-17) the chart renders the CronJob
`<fullname>-backup`:

| Field | Value |
|---|---|
| `schedule`, `timeZone` | `backup.schedule` (default `"0 2 * * *"`) and `backup.timeZone` (default `Etc/UTC`; [time zones](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#time-zones), stable since Kubernetes 1.27) |
| `concurrencyPolicy` | `Forbid`, fixed and not a value: when the previous run is still going, the controller skips the due run ([concurrency policy](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#concurrency-policy)). The database decides in the end (R-BAK-010), because a manual Job bypasses the CronJob |
| `startingDeadlineSeconds` | `backup.startingDeadlineSeconds` (default 3600): a run missed by more than an hour is skipped, not started late |
| `successfulJobsHistoryLimit`, `failedJobsHistoryLimit` | 3 and 3 (defaults) |
| `suspend` | `backup.suspend` (default `false`); rendered `true` while `restore.enabled` is set, and set by R-HA-025's procedure |
| Job `backoffLimit`, `activeDeadlineSeconds` | `backup.backoffLimit` (2) and `backup.activeDeadlineSeconds` (0 = not rendered, because a first backup of a large installation may take many hours; `TroveBackupRunningLong` reports a run that hangs) |
| Job `podFailurePolicy` | Pod condition `DisruptionTarget` → `Ignore`, so an eviction by a drain or preemption is not counted against `backoffLimit`; exit codes 2, 3, 4 and 6 of R-BAK-013 → `FailJob`, because a retry cannot help. `restartPolicy: Never`, as the policy requires ([pod failure policy](https://kubernetes.io/docs/concepts/workloads/controllers/job/#pod-failure-policy), stable since Kubernetes 1.31) |

- **Pod.** The trove image runs `trove backup create --trigger schedule` (plus `--adopt-target`
  while `backup.adoptTarget` is set, R-BAK-002) with the configuration file `<fullname>-config`,
  the security contexts of R-HA-033, `backup.resources` and
  `terminationGracePeriodSeconds: backup.terminationGracePeriodSeconds` (60). The backup streams
  table files and blobs, so the pod mounts no volume but `/tmp`, an `emptyDir` with
  `backup.tmpSizeLimit` (R-HA-061). It has no listener for clients and no probes.
- **Identity.** The pod runs as ServiceAccount `<fullname>-backup` (`backup.serviceAccount.*`),
  separate from the replicas' account, so workload identity maps the backup to a cloud role of its
  own (R-BAK-015). `automountServiceAccountToken: false`.
- **Credentials.** PostgreSQL with `secrets.databaseBackup` (otherwise the runtime credentials,
  R-HA-014) on the session host (R-HA-058). The blob store with read access, through
  `config.storage.s3` with the backup account's identity or `secrets.s3`. The target through
  `config.backup.target.credentials.source`: `default` uses the backup account's workload
  identity, `static` mounts `secrets.backupTarget`. Target credentials are rendered into backup and
  restore pods only, never into `web` or `worker` pods (R-BAK-011). The encryption key Secret is not
  mounted: a backup copies sealed values as stored (R-HA-027). Valkey is not used (INV-19).
- **Drains.** No PodDisruptionBudget selects backup pods, so a drain never waits for a backup
  (R-HA-059). On `SIGTERM` the run ends itself within the grace period, and the Job starts a new pod
  whose run starts over with a new snapshot and skips what is already copied (R-BAK-012,
  R-BAK-013). The pod carries `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, as
  R-BAK-012 requires: only the cluster autoscaler's scale-down reads it, so consolidation does not
  restart a multi-hour run over and over, while `kubectl drain` and the Eviction API ignore it.
- **Concurrency.** `Forbid` serializes scheduled runs. A manual run
  (`kubectl -n trove create job --from=cronjob/trove-backup trove-backup-manual-$(date +%s)`) and
  a scheduled one are serialized by the backup's run row (R-BAK-010). The database phase also holds
  the shared migration lock, so backups and migrations exclude each other (R-HA-019). A hung run
  cancels itself after `backup.stall_timeout` and exits `75` (R-BAK-013, Q-BAK-11), so `Forbid` is
  never held by a run that makes no progress, without a wall-clock limit that would also end a
  healthy first backup of many hours.
- **Configuration.** The chart renders `config.backup` into the shared configuration file; its keys
  are R-BAK-011's, and R-HA-031 shows all of them. It also renders the chart-managed keys
  `backup.manual_run.namespace` (the release namespace) and `backup.manual_run.cronjob`
  (`<fullname>-backup`), so the API's backup summary and the UI can show the exact manual-run
  command ([api-and-ui.md](api-and-ui.md) R-API-050, Q-API-10); no API endpoint starts a backup.
  The backup alerts read `backup.expected_interval` through the gauge
  `trove_backup_expected_interval_seconds`, so the chart needs no threshold value of its own
  (R-HA-045).

**Where the target may live** (R-BAK-002, R-BAK-015):

- The target MUST NOT share the blob store's endpoint and bucket unless
  `config.backup.target.allow_same_bucket: true`, and even then neither prefix may contain the
  other: the orphan sweep deletes every object under `storage.s3.prefix` that no row references
  (R-STOR-026), backups included. The chart refuses other values (R-HA-029), and
  `trove backup create` exits `2` on them as well, so a configuration set outside the chart is
  caught too (R-BAK-002). The setting exists for single-bucket evaluation setups and logs a warning
  on every run.
- A bucket of its own, in another account or project, is recommended: the replicas' credential has
  no access to it, and the backup's credential cannot delete in the blob bucket. Object Lock on the
  target keeps a stolen backup credential from destroying backups within the lock period
  (R-BAK-015, Security notes).

**Restore Job** (Q-HA-18). With `restore.enabled: true` and `restore.backupId` set, the chart
renders the Job `<fullname>-restore` and changes two other things while the value is set:

- The Job runs `trove backup restore <restore.backupId>`, plus `--skip-revocation-journal` while
  `restore.skipRevocationJournal` is set (R-BAK-024) and `restore.extraArgs`. It is an ordinary
  resource, not a Helm hook, so Helm does not wait for it; the operator waits with
  `kubectl wait --for=condition=complete job/<fullname>-restore`, and a restore that takes many
  hours meets no Helm or GitOps timeout.
- The migration hook Job is not rendered, because the restore applies the migrations itself, up to
  the backup's step before the load and the rest after it (R-BAK-021, R-BAK-023).
- The backup CronJob is rendered suspended, so no backup runs against a half-restored
  installation.
- The chart refuses `restore.enabled` while `web` or `worker` has a replica count above 0 or
  autoscaling enabled (R-HA-029): nothing may write to the installation during the restore. The
  restore itself refuses a non-empty database or blob prefix (R-BAK-020).
- **Identity.** The Job runs as ServiceAccount `<fullname>-restore` (`restore.serviceAccount.*`),
  `automountServiceAccountToken: false`. Its cloud identity holds R-BAK-015's target reader, blob
  writer and journal reader rows. Neither existing account fits: the backup account may only read
  the blob bucket, and the replicas' account has no access to the target (R-BAK-015). The Job uses
  the migration credentials (R-HA-014) on the session host, `secrets.s3` and `secrets.backupTarget`
  where the blob store or the target use static keys, and the encryption key Secret, because the
  restore checks before it loads
  anything that the key ring opens every key ID of the backup (R-BAK-020, R-HA-027). It never
  touches Valkey, mounts no Valkey Secret and has no Valkey egress: the restored installation gets
  a new keyspace ID, and nothing is flushed (R-BAK-022, R-HA-057 rule 7).
- **Interruptions resume** ([backup-and-restore.md](backup-and-restore.md) R-BAK-021, Q-BAK-10).
  The Job takes the backup Job's failure handling: `restartPolicy: Never`, `podFailurePolicy` with
  `DisruptionTarget` → `Ignore` and exit codes 2, 3, 4 and 6 → `FailJob`, and
  `backoffLimit: restore.backoffLimit` (2). A pod evicted by a drain, killed or cut off by a
  PostgreSQL failover exits or disappears, and the Job's next pod resumes the restore from its
  anchor without a flag. No PodDisruptionBudget selects the pod, so a drain never waits for a
  restore either; the pod carries `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` like
  the backup pod. `activeDeadlineSeconds` comes from `restore.activeDeadlineSeconds` (0 = not
  rendered).
- **A Job that failed for good** (its retries used up, or a `FailJob` exit) is continued after the
  cause is fixed by deleting it and running the same `helm upgrade` with the restore values again:
  `kubectl -n trove delete job trove-restore`. Helm then creates the Job anew, and its pod resumes.
  Turning `restore.enabled` off instead deletes the Job and renders the migration hook again, which
  refuses the anchored database (R-HA-019) and fails the upgrade, so an unfinished restore is never
  overtaken.

The operator's sequence (R-BAK-021):

```shell
helm install trove oci://ghcr.io/guided-traffic/charts/trove -n trove -f values.yaml \
  --set web.replicaCount=0 --set web.autoscaling.enabled=false \
  --set worker.replicaCount=0 --set worker.autoscaling.enabled=false \
  --set highAvailability.enforce=false \
  --set restore.enabled=true --set restore.backupId=20261028T020000Z-7f3a   # example ID
kubectl -n trove wait --for=condition=complete job/trove-restore --timeout=24h
helm upgrade trove oci://ghcr.io/guided-traffic/charts/trove -n trove -f values.yaml   # normal values
```

The final `helm upgrade` renders the migration Job again, which finds nothing to apply, and starts
both roles.

Acceptance criteria:

- `helm template` renders the CronJob with `concurrencyPolicy: Forbid`, the pod failure policy
  above, `restartPolicy: Never` and its own ServiceAccount, and no PodDisruptionBudget selects its
  pods. No `web` or `worker` container has a file or variable with target credentials.
- `kubectl create job --from=cronjob/trove-backup b1` during R-HA-052's load completes, with zero
  failed client commands.
- With `backup.backoffLimit: 0` for the test, draining the node of a running backup pod still ends
  with the Job complete on another node.
- `helm template --set restore.enabled=true --set restore.backupId=x` with both roles at zero
  renders the restore Job with its own ServiceAccount and the pod failure policy above, no
  migration Job and a suspended CronJob; with `--set web.replicaCount=2` it fails and names
  `restore.enabled` and `web.replicaCount`.
- Draining the node of the restore pod during the blob copy ends with the restore Job `Complete`,
  also with `restore.backoffLimit: 0`, and the restored installation matches the backup
  (R-BAK-012).
- The sequence above, run on the local profile against a backup taken there, brings up an
  installation whose `npm view` output matches the backup (R-HA-051 `dev-restore`).
- With `config.storage.s3.bucket: trove-blobs` and prefix `trove`, `helm template … --set config.backup.target.bucket=trove-blobs --set config.backup.target.prefix=trove/backups --set config.backup.target.allow_same_bucket=true`
  fails and names both locations; without `allow_same_bucket` any prefix in `trove-blobs` fails.

### R-HA-023 · Backup of PostgreSQL, object storage and keys
Priority: **MVP** · Covers: OPS-033

trove's own backup (INV-19, R-HA-060) is the backup every installation has. This requirement is
the platform layer under it: continuous protection that gives a recovery point of minutes instead
of one backup interval, and the rules that keep a compromised replica from destroying history.
The platform layer is recommended, not required; its checks and rules are MVP (Q-HA-19).

| State | Protection | Requirement |
|---|---|---|
| Everything in PostgreSQL and every referenced blob, as of one snapshot | trove's backup CronJob, to its own target | R-HA-060, [backup-and-restore.md](backup-and-restore.md) |
| PostgreSQL (all metadata, configuration, users, tokens, audit) | Continuous WAL archiving plus base backups, enabling point-in-time recovery ([continuous archiving](https://www.postgresql.org/docs/current/continuous-archiving.html)) | Recovery window ≥ `backup.recovery_window`. With CloudNativePG through the Barman Cloud plugin ([backup](https://cloudnative-pg.io/docs/1.30/backup), [plugin](https://cloudnative-pg.io/plugin-barman-cloud/docs/intro/)); on managed services their PITR. The archive goes to a bucket *other than* the blob bucket, with different credentials |
| Blob bucket | Bucket versioning with noncurrent-version expiry ≥ `backup.recovery_window`, plus the multipart lifecycle rule (R-HA-012); optionally replication to a second site (R-HA-026) | [S3 versioning](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Versioning.html) |
| Encryption keys (R-HA-027), Helm values | Kept outside the cluster in the operator's secret store; Helm values in Git; a trove backup does not contain the keys | Without the keys, sealed secrets in a restored database cannot be decrypted |
| Valkey | None: nothing in it is the only copy (INV-3, INV-19) | — |

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

  The result is exported as the gauge `trove_backup_check_ok{check}` by the daily task
  `backup-check` (default schedule `40 5 * * *`,
  [operations-and-observability.md](operations-and-observability.md) R-OPS-019).

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
- **Restores of trove backups.** A trove backup copies every blob its snapshot references
  (INV-19), so restoring it does not depend on the grace period or on versioning for those blobs.
  The orderings above still matter for that backup's own consistency
  ([backup-and-restore.md](backup-and-restore.md) owns it) and for every point-in-time restore of
  the platform layer (R-HA-025).

Acceptance criteria:

- Test with a fake clock: publish package P at *t₀*, delete it at *t₁*, run GC at
  *t₁* + grace + 1 min (object deleted), restore the database to *t₀* + 1 min. With versioning on,
  `trove admin storage verify` reports P's blob missing, and after `trove admin storage
  restore-versions --before <t₀+1min>` (R-HA-025) `npm install P` succeeds. Without versioning and
  with a restore point inside the grace period, `npm install P` succeeds without any repair.
- `backup.recovery_window: 168h`, `storage.gc.grace_period: 24h`, `objectVersioning: false`: the
  replica exits with a message naming the three keys.

### R-HA-025 · Restore procedure
Priority: **MVP** · Covers: OPS-033

Restores are whole-installation, point-in-time operations. Restoring a single repository is
Deferred (repository export and import, OPS-036 and OPS-037, are P2). This procedure restores the
platform layer (R-HA-023) to a time *t*. A trove backup is restored with the restore Job of
R-HA-060 and the procedure of [backup-and-restore.md](backup-and-restore.md); that procedure
runs the cleanup of step 4 as well (R-BAK-022, R-BAK-024), except the deletion pause, which is not
needed with a blob prefix that starts empty and ends holding exactly the referenced blobs
(Q-HA-19), and except a Valkey flush, which neither path performs: both replace it by a new
keyspace.

1. **Stop writers and backups.** `helm upgrade … --set web.replicaCount=0 --set worker.replicaCount=0 --set web.autoscaling.enabled=false --set worker.autoscaling.enabled=false --set highAvailability.enforce=false --set backup.suspend=true`.
   Wait `system.heartbeat_stale_after`. A backup run that is already going is deleted
   (`kubectl delete job -l app.kubernetes.io/component=backup`), so it cannot record a
   half-restored state.
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
   - runs two fix-ups of [backup-and-restore.md](backup-and-restore.md) R-BAK-022 in one
     transaction: a new random `system_state.keyspace_id`, so that no replica reads a lock,
     negative-cache entry or counter written after *t* (R-HA-057, rule 7), and the offset of 2^32
     on every column registered with `meta.RegisterRevisionColumn` (`packages.rev_seq`,
     `repositories.config_version`, `cluster_epoch.epoch` and the others R-BAK-022 lists). A PITR
     brings back the old keyspace ID and the old revision counters exactly as a backup restore
     does. A Valkey flush instead would leave the revision counters uncovered: a client holding an
     `ETag` from after *t* would get `304` for different content, and an `If-Match` would match it;
   - sets the `system_state` key `restored_at` to the current time; single-use CI issuers refuse
     subject tokens issued up to `clock_skew` after it ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)
     R-CI-018);
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
6. **Start.** `helm upgrade` with the normal values, which also resumes the backup CronJob. The
   migration Job brings an older restored schema forward.
7. **Resume deletions** after verification: `trove admin maintenance resume-deletions`.

Deletion pause (sketch; `system_state` is defined in R-OPS-024):

```sql
ALTER TABLE system_state
  ADD COLUMN deletions_paused_at     timestamptz,
  ADD COLUMN deletions_paused_reason text;
```

- GC, the orphan sweep, retention runs, cap enforcement ([storage.md](storage.md) R-STOR-044) and
  remote-cache cleanup (R-STOR-045) MUST check the pause before every batch of deletions, and skip
  the batch while it is set. A paused collection or sweep run ends with `result.paused: true`; a
  paused retention run ends `stopped` with `error.code = deletions_paused`; cap enforcement claims
  nothing while the pause is set (R-STOR-024, R-STOR-026, R-STOR-040, R-STOR-044, R-STOR-045).
- `trove admin maintenance pause-deletions --reason …` sets the pause by hand, for example
  before a planned restore drill.

Acceptance criteria (point-in-time restore drill, part of R-HA-052, run weekly in CI):

- Steps 1–7 against the local profile restore a database to *t* = 10 minutes ago.
  - `npm view` shows exactly the versions published before *t*.
  - A package published after *t* is absent, and its object is not deleted while the pause is set.
  - After `resume-deletions` and an orphan sweep with the test `min_age`, that object is removed.
  - A token revoked after *t* is rejected after step 4 (R-HA-054). With
    `backup.revocation_journal.enabled: false` it works again, which documents H-37.
  - After step 4, `system_state.keyspace_id` differs from its value before the restore, and the
    replicas write only keys of the new keyspace.
  - An npm client that fetched a package document after *t* from the old database keeps its
    `ETag`; after the restore and one publish to the same package, the same conditional request
    answers `200`, not `304` (revision offset of R-BAK-022).

### R-HA-026 · Cross-site disaster recovery
Priority: **P1** · Covers: OPS-033

- **Topology.** The primary site runs trove. The recovery site keeps:
  - a CloudNativePG replica cluster fed from the primary's WAL archive or by streaming
    ([replica clusters](https://cloudnative-pg.io/docs/1.30/replica_cluster)), or a managed
    cross-region read replica;
  - a bucket filled by one-way replication of the blob bucket
    ([S3 replication](https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication.html));
  - an installed Helm release with both roles at replica count 0, the backup CronJob suspended,
    the same encryption key Secret, and values that differ only in database host, bucket, region
    and Valkey addresses;
  - a Valkey with Sentinel of its own (R-HA-056). Nothing is replicated into it: its content is
    ephemeral (INV-3), and the recovery site starts with an empty keyspace.

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
  3. Run `trove admin post-restore --restored-to <last replayed transaction time>` while both roles
     are still at zero. It pauses deletions, reports blobs whose objects have not arrived yet and
     gives the installation a new keyspace ID (R-HA-025 step 4).
  4. Scale both roles up, and resume the backup CronJob with a target at the recovery site.

  The order matters: a replica reads the keyspace ID once, at start-up, after any restore has
  finished ([architecture.md](architecture.md) R-ARCH-030, R-ARCH-024), so replicas started before
  `post-restore` would keep the old keyspace until they restart. Operators who scaled up first
  restart both roles after `post-restore` (`kubectl rollout restart`). The CI restore cut-off
  `restored_at` does not depend on the order, because the token exchange reads it in its own
  transaction ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-018).
- The trove backups of R-HA-060 are a second way back when both the database replica and the
  bucket replica are unusable: their target SHOULD be in a third location, or at least not at the
  primary site.
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

- **Same keys on every replica.** All Deployments, the migration Job and the restore Job mount the
  same Secret (`secrets.encryption.existingSecret`). The backup CronJob does not mount it
  (R-HA-060). `secrets.encryption.keys` is the ordered list of key files
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
Priority: **MVP** · Covers: trove-only

A database restore to time *t* undoes every access removal made after *t* (H-37), and every
withdrawal or reservation of a package version made after *t*. trove therefore records every
change to an access-bearing object, and every change that withdraws or reserves a version number
where a format page asks for it, a second time, outside PostgreSQL, and `trove admin post-restore`
re-applies the removals and reservations among the entries newer than *t* (R-HA-025, step 4;
Q-HA-9). A restore of a trove backup replays the journal the same way, with *t* the backup's
snapshot time ([backup-and-restore.md](backup-and-restore.md), R-HA-060). Replay never grants
access and never deletes content.

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
| `endpoint.exposure.deleted`; `endpoint.exposure.updated` | [endpoints.md](endpoints.md) R-EP-020 | Delete the rule; the update rule below with the exposure comparator of R-EP-020, whose suspension disables the rule |
| `npm.unpublish` | [formats/npm.md](formats/npm.md) R-NPM-017, R-NPM-047 | Withhold the version: tombstone it and apply the dist-tag rule, leaving its items stored and unserved, when the restored repository holds it live with the journaled integrity (`applied`); `already in effect` when it is tombstoned; otherwise `subject unknown`. Nothing is deleted; `trove admin npm reinstate` reverses it |
| `package.published`, `package.deleted` (from P1 also the copy and move events of R-STOR-019) of local NuGet repositories | [formats/nuget.md](formats/nuget.md) R-NUGET-033 | Addition of an absent version: write a `reserved` tombstone for the journaled SHA-512. Removal: an absent version's tombstone becomes or is written as `removed`; a present version with the journaled SHA-512 is reported `revived`; never delete |

**Dispatch.** An entry of a format event is dispatched to that format's replay handler, which the
format page specifies (the two rows above). An entry of a format without a replay handler is
reported `skipped`. Access entries are replayed by the rules below.

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
- Shipping is triggered by a signal ([architecture.md](architecture.md) R-ARCH-018) and runs at
  least every `backup.revocation_journal.ship_interval`.
- A change never waits for object storage. During an object-store outage the outbox grows and ships
  afterwards. The gauge `trove_revocation_journal_pending` shows the backlog.

**Replay run.**

- `trove admin post-restore --restored-to <t>` lists the entries with
  `at > t − backup.revocation_journal.replay_margin` and sorts them by `at` and `id`. It applies
  them in that order, each to the state its predecessors left.
- It prints one line per entry (`applied`, `already in effect`, `subject unknown`, `skipped`,
  `suspended`, `revived`) and writes the audit event `system.restore.journal_replayed` with the
  count of each outcome. `revived` marks a version that a format handler found present although the
  journal recorded its removal, and that it leaves in place because replay never deletes
  ([formats/nuget.md](formats/nuget.md) R-NUGET-033).
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
- There is no bundled database, no bundled Valkey and no bundled object store, because production
  needs all three to be highly available (INV-12). The local development profile (R-HA-051)
  installs them separately.
- **The chart never creates or generates a Secret.** Every credential is a reference to an
  existing Secret (R-HA-030). This removes a whole class of upgrade failures: no generated value
  can change on re-render.

Rendered resources (`<fullname>` is the release name, or `<release>-trove` when the release name
does not contain `trove`, overridable by `fullnameOverride`):

| Kind | Name | Rendered when |
|---|---|---|
| Deployment | `<fullname>-web`, `<fullname>-worker` | always (R-HA-002) |
| Deployment, Service | `<fullname>-web-<pool>`, `<fullname>-worker-<pool>` (Deployment only) | per entry of `web.pools` and `worker.pools` (P1, R-HA-062) |
| Service (ClusterIP) | `<fullname>`, port `http` 8080 → `web` pods, `sessionAffinity: None` | always |
| Service (ClusterIP) | `<fullname>-ops`, port `ops` 8081, `web` and `worker` pods | always (probes from the kubelet do not need it; ServiceMonitor does) |
| ConfigMap | `<fullname>-config` (`trove.yaml`) | always |
| ServiceAccount | `<fullname>` | `serviceAccount.create` |
| ConfigMap, ServiceAccount (hooks, weight −10) | `<fullname>-migrate` | `migrations.mode: hook` and not `restore.enabled` |
| Job (hook, weight 0) | `<fullname>-migrate` | `migrations.mode: hook` and not `restore.enabled` |
| Job | `<fullname>-restore` | `restore.enabled` (R-HA-060) |
| ServiceAccount | `<fullname>-restore` | `restore.enabled` and `restore.serviceAccount.create` |
| Job (hook) | `<fullname>-post-restore` | `postRestore.enabled` |
| CronJob | `<fullname>-backup` | `backup.enabled` (R-HA-060) |
| ServiceAccount | `<fullname>-backup` | `backup.enabled` and `backup.serviceAccount.create` |
| PodDisruptionBudget | per Deployment, none for Jobs | `podDisruptionBudget.enabled` of the role |
| HorizontalPodAutoscaler (`autoscaling/v2`) | per Deployment | `autoscaling.enabled` of the role |
| Ingress | `<fullname>` | `ingress.enabled` |
| HTTPRoute | `<fullname>` | `gateway.enabled` |
| Gateway | `<fullname>-gateway` | `gateway.enabled` and `gateway.create.enabled` (P1, R-HA-053) |
| NetworkPolicy | `<fullname>` (`web` and `worker`), `<fullname>-migrate`, `<fullname>-restore`, `<fullname>-backup` | `networkPolicy.enabled` |
| Service, Ingress, HTTPRoute, NetworkPolicy | `<fullname>-ep-<name>` per further endpoint: its Service and port, its own Ingress or HTTPRoute with its class, hosts (a wildcard host included) and TLS entries, and a NetworkPolicy for its port ([endpoints.md](endpoints.md) R-EP-008) | per entry of `endpoints`; the Ingress with its `ingress.enabled`, the HTTPRoute with its `gateway.enabled` |
| Deployment, PodDisruptionBudget, HorizontalPodAutoscaler | `<fullname>-web-ep-<name>`, the optional pool of an endpoint ([endpoints.md](endpoints.md) R-EP-008) | `endpoints.<name>.pool.enabled` |
| ServiceMonitor, PrometheusRule | `<fullname>` | `metrics.serviceMonitor.enabled`, `metrics.prometheusRule.enabled` |
| anything | from `extraObjects` | listed |

- The chart renders no StatefulSet, no PersistentVolumeClaim and no Valkey, PostgreSQL or object
  store (R-HA-061).
- Labels on every object: `app.kubernetes.io/name: trove`, `app.kubernetes.io/instance`,
  `app.kubernetes.io/version`, `app.kubernetes.io/managed-by`, `helm.sh/chart`, and
  `app.kubernetes.io/component` (`web`, `worker`, `migrate`, `restore`, `backup`). `web` and
  `worker` pods also carry `trove/pool` (name proposed): `<name>` for the pods of a pool of R-HA-062,
  and the empty value for the main Deployments. The ServiceMonitor sets the target label `pool`
  from it, and Prometheus drops an empty label, so the main Deployments' series carry no `pool`
  (R-HA-045). Selectors use name, instance, component and, for `web` and `worker`, `trove/pool`,
  from the MVP on, because selector labels are immutable: adding a pool later then never overlaps
  the main Deployment's or the client Service's selector and never changes an immutable one.
- **Ports (HA-021).** Containers listen on 8080 (`http`, the default endpoint's client listener),
  8081 (`ops`) and the port of each further endpoint they serve ([endpoints.md](endpoints.md)
  R-EP-002, R-EP-008), and nothing else (INV-6). Every port is unprivileged, so no capability is
  needed. Replicas need no port open towards each other.
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
| `highAvailability.enforce` and fewer than 2 replicas (`replicaCount`, or `autoscaling.minReplicas`) for `web`, `worker` or a pool, a disabled PDB, or a missing host spread constraint, one that is not `DoNotSchedule`, or one without `nodeTaintsPolicy: Honor` (R-HA-059 rule 3) | the Deployment and the value |
| `worker.replicaCount` (or `worker.autoscaling.minReplicas`) < 1 | `worker.replicaCount` |
| `config.storage.upload.max_size` > the spool budget derived from the `web` spool size (R-HA-035) | both |
| `terminationGracePeriodSeconds` of a role < `config.health.shutdown_drain_delay` + 15 s | both |
| `config.valkey.mode: sentinel` with `config.valkey.sentinel.addresses` or `config.valkey.sentinel.primary_name` empty | the value |
| `highAvailability.enforce` and `config.valkey.mode` other than `sentinel`, or fewer than 3 Sentinel addresses | `config.valkey.mode` or `config.valkey.sentinel.addresses` |
| `secrets.valkey.existingSecret` empty | the value |
| `config.valkey.tls.mode: disable` without `config.valkey.allow_insecure: true` | both |
| `config.database.pooler.mode: transaction` with `config.database.session_host` empty or equal to `config.database.host` | `config.database.session_host` |
| `config.database.pooler.mode: transaction` with a control endpoint (`control_host`, `control_port`, `control_name`, each defaulting to its main counterpart) equal to the request endpoint (Q-HA-20) | `config.database.control_host` |
| `backup.enabled` and `config.backup.target.bucket` empty | both |
| `backup.schedule` in a form that trove's cron parser rejects: six fields, `?`, day-of-week `7`, `@every`, `TZ=` or `CRON_TZ=` ([operations-and-observability.md](operations-and-observability.md) Q-OPS-7, [storage.md](storage.md) R-STOR-025) | `backup.schedule` |
| The backup target on the blob store's endpoint and bucket without `config.backup.target.allow_same_bucket: true`, or with one prefix containing the other (R-HA-060; `trove backup create` exits `2` on the same rule, R-BAK-002) | both locations |
| `config.backup.target.credentials.source: static` without `secrets.backupTarget.existingSecret` | both |
| `restore.enabled` without `restore.backupId` | `restore.backupId` |
| `restore.enabled` with a `web` or `worker` replica count above 0 or autoscaling enabled | `restore.enabled` and the value |
| Two pools with the same name, path prefix or queue (P1, R-HA-062), or a path pool whose name starts with `ep-` | the pools |
| An endpoint whose `port` equals 8080, 8081 or another endpoint's port; an invalid endpoint name or one for which `<fullname>-ep-<name>` exceeds 63 characters; a missing `baseUrl`; a host in two endpoints; `extraSecrets` or `clientCaSecret` with `tls.enabled: false`; an endpoint pool below two replicas with `highAvailability.enforce` ([endpoints.md](endpoints.md) R-EP-008) | the endpoint and the value |
| `gateway.enabled` without `gateway.parentRefs` and without `gateway.create.enabled` | `gateway.parentRefs` |
| `gateway.enabled` and `.Capabilities.APIVersions` lacks `gateway.networking.k8s.io/v1/HTTPRoute` | the missing API |
| `metrics.serviceMonitor.enabled` (or `prometheusRule`) and the `monitoring.coreos.com/v1` API missing | the missing API |
| A chart-managed configuration key set inside `config` (secret file paths, among them those of `valkey` and `backup.target`; `storage.spool.dir`; `server.content_base_url`; `server.endpoints`; `backup.manual_run`) | the key |
| `contentOrigin.host` equal to the host of `config.server.base_url` or of a `config.server.additional_base_urls` entry (P1, R-HA-055) | `contentOrigin.host` |
| `podDisruptionBudget` with both `minAvailable` and `maxUnavailable` set | both |

- `values.schema.json` validates every chart-owned value (types, enums, `additionalProperties:
  false`); Helm checks it on install, upgrade, lint and template
  ([schema files](https://helm.sh/docs/topics/charts/#schema-files)). It uses JSON Schema
  draft-07 keywords only. Its enums admit only `ReadWriteOnce` and `ReadWriteOncePod` as access
  modes of a generic ephemeral volume (R-HA-061).
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
  keys. One file serves every Deployment and Job; what differs per Deployment, the role, is the
  container argument `--role` (R-HA-002), not a key in the file. The chart-managed keys:
  - secret file paths;
  - `storage.spool.dir`;
  - `server.content_base_url`, rendered as `https://<contentOrigin.host>` when `contentOrigin.host`
    is set (P1, R-HA-055);
  - `backup.manual_run.namespace` (the release namespace) and `backup.manual_run.cronjob`
    (`<fullname>-backup`), which the API's backup summary and the UI read to show the manual-run
    command ([api-and-ui.md](api-and-ui.md) R-API-050, Q-API-10; R-HA-060);
  - `server.endpoints`, rendered from the `endpoints` values, with the certificate file paths of
    their mounted Secrets ([endpoints.md](endpoints.md) R-EP-008). The rate-limit classes
    `server.endpoint_rate_limits` stay ordinary `config` keys.
- Precedence follows INV-15: environment variable `TROVE_<SECTION>_<KEY>` (nested keys joined
  with `_`, e.g. `TROVE_DATABASE_POOL_MAX_CONNS`) > file > built-in default. `extraEnv` can
  therefore override any file value. The chart itself sets only:
  - `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` and `TROVE_RUNTIME_TERMINATION_GRACE_PERIOD` per
    Deployment, and `TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS` and
    `TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS` for the migration Job, the variables of the
    schema keys `runtime.memory_limit_bytes`, `runtime.termination_grace_period`,
    `database.preflight.expected_web_replicas` and `database.preflight.expected_worker_replicas`
    (R-OPS-028 mapping; the chart is their only source);
  - `TROVE_STORAGE_SPOOL_MAX_BYTES` per Deployment, derived from the role's spool size when
    `config.storage.spool.max_bytes` is unset (R-HA-035);
  - `GOMAXPROCS` (R-HA-036);
  - in every trove pod (`web`, `worker`, backup, restore, migration and post-restore), from the
    downward API: `TROVE_POD_NAME` (`fieldRef: metadata.name`), `TROVE_POD_NAMESPACE`
    (`metadata.namespace`) and `TROVE_NODE_NAME` (`spec.nodeName`). R-OPS-028 reserves these
    names. They feed the replica registry, including its `node` column that the status screen
    groups by ([api-and-ui.md](api-and-ui.md) R-UI-038, Q-UI-7), the `replica` field of every log
    record and the trace resource (R-OPS-003, R-OPS-009, R-OPS-018). `spec.nodeName` is offered
    by the downward API as an environment variable only, not as a volume file.
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
end up in crash reports, and do not follow Secret updates. Each Secret is mounted only into the
pods whose process reads it; the first column names the pods where the set is narrower than every
trove pod.

**Secret propagation time.** A rotated Secret reaches the mounted files of every pod within
2 minutes (the kubelet's sync period plus its cache delay). [identity-and-authentication.md](identity-and-authentication.md)
R-AUTH-014's `auth.break_glass.rotation_hold` default (2 min) equals this time; if this time
changes, that default follows.

| Value | Keys (default names) | Mount path | Configuration key set by the chart |
|---|---|---|---|
| `secrets.database` | `username`, `password` | `/var/run/secrets/trove/database/` | `database.username_file`, `database.password_file` |
| `secrets.databaseMigration` (migration and restore Jobs only) | `username`, `password` | same path in the Jobs | same |
| `secrets.databaseBackup` (backup pods only; empty = `secrets.database`) | `username`, `password` | same path in the backup pod | same |
| `secrets.databaseCA` | `ca.crt` | `/var/run/secrets/trove/database-ca/` | `database.ssl_root_cert_file` |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/database-client/` | `database.ssl_cert_file`, `database.ssl_key_file` |
| `secrets.s3` (only with `source: static`) | `access-key-id`, `secret-access-key` | `/var/run/secrets/trove/s3/` | `storage.s3.credentials.access_key_id_file`, `…secret_access_key_file` |
| `secrets.valkey` (`web`, `worker` only; not in backup, restore, migration or post-restore pods, R-BAK-012, R-BAK-022) | `username` (optional), `password` | `/var/run/secrets/trove/valkey/` | `valkey.username_file`, `valkey.password_file` |
| `secrets.valkeySentinel` (same pods) | `username` (optional), `password` | `/var/run/secrets/trove/valkey-sentinel/` | `valkey.sentinel.username_file`, `valkey.sentinel.password_file` |
| `secrets.valkeyCA` (same pods) | `ca.crt` | `/var/run/secrets/trove/valkey-ca/` | `valkey.tls.ca_file` |
| `secrets.valkeyClientCert` (same pods) | `tls.crt`, `tls.key` | `/var/run/secrets/trove/valkey-client/` | `valkey.tls.cert_file`, `valkey.tls.key_file` |
| `secrets.backupTarget` (backup and restore pods; only with `source: static`) | `access-key-id`, `secret-access-key` | `/var/run/secrets/trove/backup-target/` | `backup.target.credentials.access_key_id_file`, `…secret_access_key_file` |
| `secrets.oidc[]` (one entry per provider ID; `web` pods only, not worker, backup, migration or restore pods, R-AUTH-042) | `client-secret` | `/var/run/secrets/trove/oidc-<id>/` | `client_secret_file` of the provider with that `id` in `config.auth.oidc.providers` |
| `secrets.breakGlass` (`web` pods only, as `secrets.oidc[]`, R-AUTH-014, R-AUTH-042) | `password` | `/var/run/secrets/trove/break-glass/` | `auth.break_glass.password_file`; empty value → `auth.break_glass.enabled: false` |
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
  Further bootstrapping is `kubectl exec deploy/trove-web -- trove admin …` (R-AUTH-015). The chart
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
Keys marked P1 (`gateway.create`, `contentOrigin`, `bootstrap`, `web.pools`, `worker.pools`) enter
the chart with the release that implements them: the `config` subtree is validated against the
schema the binary emits (R-HA-029), so a key the binary does not know yet would fail
`helm install`. The keys under `config.backup` are [backup-and-restore.md](backup-and-restore.md)
R-BAK-011's, shown here with every key and default.

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
  enforce: true                         # default (proposed): ≥ 2 replicas, a PDB and DoNotSchedule host spreading per role; Sentinel mode with ≥ 3 Sentinels (R-HA-029)

# ── web Deployment: trove serve --role web (R-HA-002) ───────────────────────
web:
  replicaCount: 3                       # default (proposed) (Q-HA-16); ignored when autoscaling.enabled
  minReadySeconds: 10                   # default (proposed)
  revisionHistoryLimit: 5               # default (proposed)
  strategy:
    type: RollingUpdate                 # default (proposed)
    rollingUpdate:
      maxSurge: 1                       # default (proposed)
      maxUnavailable: 0                 # default (proposed)
  terminationGracePeriodSeconds: 120    # default (proposed) (R-HA-011)
  spool:
    sizeLimit: ""                       # default (proposed): empty = spool.sizeLimit (R-HA-035)
  resources:
    requests:
      cpu: "1"                          # default (proposed)
      memory: 1Gi                       # default (proposed)
      ephemeral-storage: 2Gi            # default (proposed)
    limits:
      memory: 2Gi                       # default (proposed); no CPU limit by default (R-HA-036)
  autoscaling:
    enabled: false                      # default (proposed)
    minReplicas: 3                      # default (proposed) (Q-HA-16, R-HA-059)
    maxReplicas: 8                      # default (proposed)
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
    nodeTaintsPolicy: Honor             # default (proposed): cordoned nodes do not count as domains (R-HA-059)
    zone:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: ScheduleAnyway # default (proposed)
    host:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: DoNotSchedule  # default (proposed); required with highAvailability.enforce
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
  pools: []                             # P1 (R-HA-062); example: [{name: packages, paths: [/api/npm/, /api/nuget/], replicaCount: 4}]

# ── worker Deployment: trove serve --role worker (R-HA-002) ─────────────────
worker:
  replicaCount: 2                       # default (proposed) (Q-HA-16)
  minReadySeconds: 10                   # default (proposed)
  revisionHistoryLimit: 5               # default (proposed)
  strategy:
    type: RollingUpdate                 # default (proposed)
    rollingUpdate:
      maxSurge: 1                       # default (proposed)
      maxUnavailable: 0                 # default (proposed)
  terminationGracePeriodSeconds: 300    # default (proposed): jobs persist their cursor before exit
  spool:
    sizeLimit: 5Gi                      # default (proposed); workers spool only cache fills of jobs and, from P1, the nuget-symbol-index job's one extracted PDB at a time (formats/nuget.md R-NUGET-021)
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
    nodeTaintsPolicy: Honor             # default (proposed)
    zone:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: ScheduleAnyway # default (proposed)
    host:
      maxSkew: 1                        # default (proposed)
      whenUnsatisfiable: DoNotSchedule  # default (proposed): workers spread evenly; with nodes ≥ replicas no two share a node (INV-12)
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
  pools: []                             # P1 (R-HA-062); example: [{name: indexing, queues: [index], replicaCount: 2}]

# ── trove configuration (rendered to /etc/trove/trove.yaml) ───────────
# Chart-managed keys (secret file paths, storage.spool.dir, server.content_base_url) must not be set here (R-HA-029).
# One file serves every Deployment and Job; the role is the container argument --role (R-HA-002).
# The hook ConfigMap of the migration Job carries the same file (R-HA-019); the restore Job mounts <fullname>-config.
config:
  server:                               # operations-and-observability.md R-OPS-005
    base_url: ""                        # required; example: https://trove.example.com
    trusted_proxies: []                 # default (proposed); example: ["10.244.0.0/16"] (ingress controller pod CIDR)
    additional_base_urls: []            # default (proposed)
  health:                               # operations-and-observability.md R-OPS-001
    require_object_store: false         # default (proposed): object storage gates only the first readiness (R-HA-008, Q-HA-1)
    shutdown_drain_delay: 5s            # default (proposed); must exceed the edge's endpoint removal time (R-HA-059)
  metrics:                              # operations-and-observability.md R-OPS-006
    enabled: true                       # default (proposed)
    auth: none                          # default (proposed): none | static (needs secrets.metricsToken)
  log:
    level: info                         # default (proposed); operations-and-observability.md
  database:                             # this page, R-HA-009 to R-HA-016, R-HA-058
    host: ""                            # required; example: trove-db-rw.trove.svc, or the pooler with R-HA-058
    port: 5432                          # default (proposed)
    session_host: ""                    # default (proposed): empty = host; required with pooler.mode transaction; example: trove-db-rw.trove.svc
    session_port: 0                     # default (proposed): 0 = port
    control_host: ""                    # default (proposed): empty = host; required with pooler.mode transaction (R-HA-058, Q-HA-20); example: trove-db-pooler-control.trove.svc
    control_port: 0                     # default (proposed): 0 = port
    control_name: ""                    # default (proposed): empty = name
    name: trove                       # example
    schema: trove                     # default (proposed)
    backup_role: ""                     # default (proposed): empty = trove migrate grants no backup privileges; example: trove_backup (R-HA-014, R-BAK-015)
    ssl_mode: verify-full               # default (proposed)
    target_session_attrs: read-write    # default (proposed)
    connect_timeout: 5s                 # default (proposed)
    pooler:
      mode: none                        # default (proposed): none | session | transaction (R-HA-058, Q-HA-6)
      server_connections: 0             # default (proposed); with transaction: the server-side total of the request and control poolers (R-HA-016); example: 100
    pool:
      max_conns: 20                     # default (proposed); at least 2, one is the control connection (R-HA-016)
      min_conns: 2                      # default (proposed)
      max_conn_lifetime: 30m            # default (proposed); 5m recommended with a transaction-mode pooler
      max_conn_lifetime_jitter: 5m      # default (proposed); 1m recommended with a transaction-mode pooler
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
  valkey:                               # this page, R-HA-056, R-HA-057
    mode: sentinel                      # default (proposed): sentinel | standalone (development only)
    sentinel:
      addresses: []                     # required; example: [trove-valkey-sentinel-0.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-1.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-2.trove-valkey-sentinel.trove.svc:26379]
      primary_name: ""                  # required; example: trove
      tls: true                         # default (proposed)
      refresh_interval: 5s              # default (proposed)
    address: ""                         # default (proposed); standalone mode only; example: trove-valkey.trove.svc:6379
    database: 0                         # default (proposed)
    key_prefix: "trove:"                # default (proposed)
    tls:
      mode: verify-full                 # default (proposed): verify-full | verify-ca | disable
      server_name: ""                   # default (proposed): host name of each address
    allow_insecure: false               # default (proposed)
    connect_timeout: 2s                 # default (proposed)
    command_timeout: 500ms              # default (proposed)
    sentinel_query_timeout: 300ms       # default (proposed)
    max_conn_lifetime: 30m              # default (proposed)
    health_check_interval: 5s           # default (proposed)
    reconnect:
      initial_backoff: 100ms            # default (proposed)
      max_backoff: 5s                   # default (proposed)
    circuit_breaker:
      failure_threshold: 5              # default (proposed)
      open_duration: 5s                 # default (proposed)
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
  jobs:
    schedule_tick: 10s                  # default (proposed)
    lease_duration: 60s                 # default (proposed)
    heartbeat_interval: 15s             # default (proposed)
    background_migrations:
      batch_size: 1000                  # default (proposed)
      pause_between_batches: 100ms      # default (proposed)
  migrate:
    advisory_lock_wait: 5m              # default (proposed); also how long a migration waits for a backup's database phase (R-HA-019)
    ddl_lock_timeout: 5s                # default (proposed)
    ddl_lock_retries: 20                # default (proposed)
  backup:                               # backup-and-restore.md R-BAK-011 owns these keys; manual_run.* is chart-managed (R-HA-030)
    target:
      endpoint: ""                      # default (proposed): AWS resolution; example: https://s3.eu-central-1.amazonaws.com
      region: ""                        # example: eu-central-1
      bucket: ""                        # required with backup.enabled; example: trove-backups
      prefix: trove                     # default (proposed)
      force_path_style: false           # default (proposed)
      allow_insecure_endpoint: false    # default (proposed)
      ca_file: ""                       # default (proposed): system trust store
      storage_class: ""                 # default (proposed): bucket default; example: STANDARD_IA
      allow_same_bucket: false          # default (proposed); R-BAK-002, R-HA-060
      credentials:                      # same keys as storage.s3.credentials (R-STOR-031); the *_file keys are chart-managed (secrets.backupTarget, workload identity), not set here
        source: default                 # default (proposed): default | webIdentity | static (secrets.backupTarget)
        role_arn: ""                    # default (proposed); example: arn:aws:iam::210987654321:role/trove-backup-writer
        sts_endpoint: ""                # default (proposed)
      sse:
        mode: s3                        # default (proposed): s3 | kms | none (R-BAK-026)
        kms_key_id: ""                  # default (proposed); example: arn:aws:kms:eu-central-1:210987654321:key/9a8b…
        bucket_key: true                # default (proposed)
    database:
      parallel: 2                       # default (proposed); backup connections = 2 + parallel (R-HA-016)
      lock_timeout: 60s                 # default (proposed)
      migration_wait: 15m               # default (proposed); below stall_timeout
      part_size: 16MiB                  # default (proposed)
    copy:
      concurrency: 16                   # default (proposed)
      server_side: auto                 # default (proposed): auto | always | never
      part_size: 256MiB                 # default (proposed)
      check_existing: auto              # default (proposed): auto | always | never
      bytes_per_second: 0               # default (proposed): 0 = unlimited
    compression_level: 3                # default (proposed); zstd level 1–19
    exclude:
      remote_caches: false              # default (proposed); R-BAK-008
    retention:
      keep_last: 7                      # default (proposed); ≥ 1
      keep_within: 0s                   # default (proposed): count only; example: 720h
      sweep_interval: 168h              # default (proposed); full pool sweep (R-BAK-017)
    expected_interval: 24h              # default (proposed); drives TroveBackupStale through its gauge (R-HA-045, R-BAK-014)
    lease_duration: 5m                  # default (proposed)
    heartbeat_interval: 15s             # default (proposed); × 4 ≤ lease_duration
    stall_timeout: 30m                  # default (proposed); a run without progress cancels itself (R-BAK-013)
    finalize_timeout: 30s               # default (proposed); × 2 < lease_duration
    verify_sample: 1000                 # default (proposed)
    run_history: 2160h                  # default (proposed); backup_run rows kept 90 days
    recovery_window: 0s                 # default (proposed): undeclared; example: 168h (R-HA-024)
    object_versioning: false            # default (proposed)
    revocation_journal:                 # R-HA-054 (MVP, Q-HA-9)
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
  databaseBackup:
    existingSecret: ""                  # default (proposed): empty = secrets.database (R-HA-014, R-HA-060); example: trove-db-backup
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
  valkey:
    existingSecret: ""                  # required (R-HA-029); example: trove-valkey
    usernameKey: username               # default (proposed); a missing key = the "default" user
    passwordKey: password               # default (proposed)
  valkeySentinel:
    existingSecret: ""                  # default (proposed): Sentinels without authentication; example: trove-valkey-sentinel
    usernameKey: username               # default (proposed)
    passwordKey: password               # default (proposed)
  valkeyCA:
    existingSecret: ""                  # default (proposed): empty = system roots plus trustedCA; example: trove-valkey-ca
    key: ca.crt                         # default (proposed)
  valkeyClientCert:
    existingSecret: ""                  # default (proposed): no client certificate; needed while Valkey keeps tls-auth-clients yes
    certKey: tls.crt                    # default (proposed)
    keyKey: tls.key                     # default (proposed)
  backupTarget:
    existingSecret: ""                  # default (proposed); required with config.backup.target.credentials.source static
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

# ── Volumes (R-HA-035, R-HA-061: nothing ReadWriteMany, nothing persistent) ─
spool:
  type: emptyDir                        # default (proposed): emptyDir | ephemeral
  sizeLimit: 20Gi                       # default (proposed) (R-HA-035); web.spool and worker.spool override it
  headroom: 2GiB                        # default (proposed): sizeLimit − headroom = storage.spool.max_bytes (R-HA-035, R-STOR-004)
  medium: ""                            # default (proposed): node disk; "Memory" counts against the memory limit
  ephemeral:
    storageClassName: ""                # default (proposed): cluster default
    size: 50Gi                          # default (proposed); used with type ephemeral
    accessModes: [ReadWriteOnce]        # default (proposed): ReadWriteOnce | ReadWriteOncePod; nothing else validates

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
  # No sessionAffinity value: the client Service always renders sessionAffinity: None (R-HA-037).
  annotations: {}                       # default (proposed)
  labels: {}                            # default (proposed)
  loadBalancerSourceRanges: []          # default (proposed)
  externalTrafficPolicy: ""             # default (proposed): Kubernetes default; example: Local
  ipFamilyPolicy: ""                    # default (proposed): cluster default; example: PreferDualStack (R-HA-044)
  ipFamilies: []                        # default (proposed); example: [IPv6, IPv4]

ingress:
  enabled: false                        # default (proposed)
  className: ""                         # default (proposed): cluster default IngressClass; example: traefik
  annotations: {}                       # default (proposed); controller-specific settings (R-HA-039); never cookie affinity (R-HA-037)
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

endpoints: {}                           # default (proposed): only the default endpoint; every key in endpoints.md R-EP-008

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
    valkey:
      to: []                            # default (proposed); example: [{podSelector: {matchLabels: {app.kubernetes.io/name: trove-valkey}}}]
      ports: [6379, 26379]              # default (proposed): data nodes and Sentinels
    objectStorage:
      to: []                            # default (proposed); example: [{ipBlock: {cidr: 0.0.0.0/0}}] or a MinIO podSelector
      ports: [443]                      # default (proposed)
    backupTarget:
      to: []                            # default (proposed); backup and restore pods only; example: [{ipBlock: {cidr: 0.0.0.0/0}}]
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
    relabelings: []                     # default (proposed); appended after the fixed role and pool relabelings (R-HA-045)
    metricRelabelings: []               # default (proposed)
  prometheusRule:
    enabled: false                      # default (proposed)
    labels: {}                          # default (proposed)
    disabledAlerts: []                  # default (proposed); example: [TroveSpoolNearlyFull]
    extraRules: []                      # default (proposed)

# ── Migrations and restore (R-HA-019, R-HA-025, R-HA-060) ──────────────────
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

# ── Backup CronJob and restore Job (R-HA-060, backup-and-restore.md R-BAK-012) ──
backup:
  enabled: true                         # default (proposed) (Q-HA-17); needs config.backup.target.bucket
  schedule: "0 2 * * *"                 # default (proposed)
  timeZone: Etc/UTC                     # default (proposed)
  suspend: false                        # default (proposed); rendered true while restore.enabled
  adoptTarget: false                    # default (proposed); adds --adopt-target after a restore (R-BAK-002)
  startingDeadlineSeconds: 3600         # default (proposed)
  successfulJobsHistoryLimit: 3         # default (proposed)
  failedJobsHistoryLimit: 3             # default (proposed)
  backoffLimit: 2                       # default (proposed); evictions are not counted (podFailurePolicy)
  activeDeadlineSeconds: 0              # default (proposed): 0 = not rendered
  terminationGracePeriodSeconds: 60     # default (proposed)
  tmpSizeLimit: 256Mi                   # default (proposed); the only volume (R-HA-061); the commands use at most 64 MiB (R-BAK-012), the rest keeps the kubelet's sizeLimit eviction away
  resources:
    requests:
      cpu: 500m                         # default (proposed)
      memory: 256Mi                     # default (proposed)
    limits:
      memory: 1Gi                       # default (proposed)
  serviceAccount:
    create: true                        # default (proposed)
    name: ""                            # default (proposed): <fullname>-backup
    annotations: {}                     # default (proposed); example (IRSA, read on the blob bucket, write on the target):
    #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-backup  # example
  nodeSelector: {}                      # default (proposed)
  tolerations: []                       # default (proposed)
  affinity: {}                          # default (proposed)
  priorityClassName: ""                 # default (proposed)
  podAnnotations: {}                    # default (proposed); cluster-autoscaler.kubernetes.io/safe-to-evict: "false" is always added (R-BAK-012)
  podLabels: {}                         # default (proposed)
  extraEnv: []                          # default (proposed)

restore:
  enabled: false                        # default (proposed); renders the restore Job, no migration Job, a suspended CronJob (Q-HA-18)
  backupId: ""                          # required with enabled; example: 20261028T020000Z-7f3a
  skipRevocationJournal: false          # default (proposed); only when the source bucket is gone (R-BAK-024, H-37)
  extraArgs: []                         # default (proposed); example: [--check] for a dry run of the preconditions (R-BAK-020)
  backoffLimit: 2                       # default (proposed); evictions are not counted, every retry resumes (R-BAK-021)
  activeDeadlineSeconds: 0              # default (proposed): 0 = not rendered
  serviceAccount:
    create: true                        # default (proposed)
    name: ""                            # default (proposed): <fullname>-restore
    annotations: {}                     # default (proposed); example (IRSA, read on the target, write on the new blob prefix, read on the source journal):
    #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-restore  # example
  resources:
    requests:
      cpu: "1"                          # default (proposed)
      memory: 1Gi                       # default (proposed)
    limits:
      memory: 2Gi                       # default (proposed)
  podAnnotations: {}                    # default (proposed); safe-to-evict: "false" is always added, as for backup pods
  extraEnv: []                          # default (proposed)

# ── Extension points (R-HA-046) ────────────────────────────────────────────
extraEnv: []                            # default (proposed); example: [{name: TROVE_LOG_LEVEL, value: debug}]
extraEnvFrom: []                        # default (proposed)
extraVolumes: []                        # default (proposed); the operator's: R-HA-061 cannot see a claim's access mode
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
  `config.database.host`, `config.storage.s3.bucket`, `config.valkey.sentinel.addresses`,
  `config.valkey.sentinel.primary_name`, `config.backup.target.bucket`,
  `secrets.database.existingSecret`, `secrets.valkey.existingSecret`,
  `secrets.encryption.existingSecret`, `secrets.encryption.keys`) renders without error.

### R-HA-032 · Replicas, disruption budget, spreading and autoscaling
Priority: **MVP** · Covers: HA-009, HA-032, HA-043, HA-044

- **Replicas.** Defaults: 3 for `web` and 2 for `worker` (Q-HA-16); at least 2 per role with
  `highAvailability.enforce` (INV-12). The drain rule of R-HA-059 sets the real minimum: the
  replicas that carry the peak plus the PDB's `maxUnavailable`. When autoscaling is enabled, the
  Deployment's `replicas` field is omitted, so `helm upgrade` does not reset the autoscaler's
  choice, and `minReplicas` follows the drain rule.
- **Disruption budget.** One `policy/v1` PodDisruptionBudget per Deployment with
  `maxUnavailable: 1` and `unhealthyPodEvictionPolicy: AlwaysAllow` (R-HA-008). `maxUnavailable`
  scales with the replica count, whereas `minAvailable: 1` with one replica would block every
  drain. Backup and restore pods have none (R-HA-060).
- **Spreading.** Two topology spread constraints per Deployment, rendered unless replaced
  ([topology spread](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)):
  - `topology.kubernetes.io/zone`, `maxSkew: 1`, `ScheduleAnyway`;
  - `kubernetes.io/hostname`, `maxSkew: 1`, `DoNotSchedule`, for both roles, so that a role's
    replicas spread evenly over the nodes: the replica counts of any two eligible nodes differ by
    at most one, so with at least as many schedulable nodes as replicas no two share a node, and with at least
    two nodes one node loss never takes a whole role (INV-12).

  Both use `matchLabelKeys: [pod-template-hash]`, so the skew is computed per ReplicaSet and a
  rollout does not pile new pods onto the nodes the old ones leave. Both use
  `nodeTaintsPolicy: Honor`, so a cordoned node does not count as a domain and the replacement of
  an evicted pod can schedule (R-HA-059). Single-node development clusters set
  `host.whenUnsatisfiable: ScheduleAnyway` and `highAvailability.enforce: false`.
- **Node loss.** Tolerations for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable`
  with `tolerationSeconds` from the role's `nodeFailureTolerationSeconds` (60) replace the default
  of 300 s
  ([taint-based evictions](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#taint-based-evictions)).
  Replicas are stateless, so recreating them early costs nothing.
- **Autoscaling.** `autoscaling/v2` per Deployment: `web` on CPU (default target 70 %), `worker` on
  CPU (80 %), optional memory and custom metrics. Scale-down is slow (one pod per 2 minutes after a
  5-minute stabilization window), because every removed pod drains its uploads, as in a node drain.
  Scaling raises the database connection demand: the budget (R-HA-016) uses `maxReplicas`, so it
  must be raised before `maxReplicas` is, unless the transaction-mode pooler carries the main pool
  (R-HA-058).
- **Placement controls.** `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`,
  `schedulerName` are passed through unchanged, per role.

Acceptance criteria:

- On a three-node kind cluster with three zones labeled, three `web` replicas land on three nodes
  and two `worker` replicas on two. `kubectl drain` of one node keeps at least two `web` replicas
  serving at all times, and the replacement of every evicted pod becomes ready on the remaining
  nodes. The same drain with `nodeTaintsPolicy: Ignore` leaves the `web` replacement `Pending`,
  which documents why the value is set.
- With autoscaling enabled, a CPU load test raises the `web` replica count to `maxReplicas`, and a
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
- Writable paths are only the spool (`/var/lib/trove/spool`, `web` and `worker`) and `/tmp` (an
  `emptyDir` with `tmp.sizeLimit`, in backup and restore pods `backup.tmpSizeLimit`). `TMPDIR`
  points to `/tmp`. The backup CronJob, the restore Job and the migration Job run with the same
  security contexts as the Deployments.
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
- Probes use HTTPS on the operations port when `tls.enabled` is `true` (R-HA-063); plain HTTP only when it is `false`. The port is not exposed through Ingress or Gateway
  (R-HA-037), and NetworkPolicy restricts it (R-HA-042).

Acceptance criteria: the probe settings render as in the table. R-HA-008's tests pass with them.

### R-HA-035 · Spool and temporary volumes
Priority: **MVP** · Covers: HA-005

- The spool (R-STOR-004) is an `emptyDir` at `/var/lib/trove/spool` with `sizeLimit` from the
  role's `spool.sizeLimit` (`web.spool.sizeLimit`, `worker.spool.sizeLimit`, falling back to
  `spool.sizeLimit`). With `spool.type: ephemeral` it is a generic ephemeral volume (a per-pod
  PersistentVolumeClaim deleted with the pod) of `spool.ephemeral.size`
  ([ephemeral volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/)), for nodes
  whose local disk is too small. Its access mode is `ReadWriteOnce` or `ReadWriteOncePod`, never
  shared (R-HA-061).
- The spool is scratch space for the request or job that is running and nothing else (INV-18).
  Nothing in it outlives that request: a multi-request upload keeps its parts in S3 multipart
  uploads and its state in PostgreSQL ([storage.md](storage.md)), so the next request of the same
  upload may reach another replica, and a lost pod loses no acknowledged data.
- The kubelet evicts a pod whose `emptyDir` exceeds its `sizeLimit`. The process must reject uploads
  before that point. The chart therefore sets `storage.spool.max_bytes = <role's sizeLimit> −
  spool.headroom` per Deployment, as the environment variable `TROVE_STORAGE_SPOOL_MAX_BYTES`,
  unless `config.storage.spool.max_bytes` is set. R-STOR-004's admission keeps the spool below its
  budget, so eviction never happens through uploads.
- Sizing: `spool.sizeLimit ≥ concurrent spooled uploads per pod × p95 upload size + headroom`, and
  at least `storage.upload.max_size + headroom` (validated, R-HA-029). Remote-cache fills use the
  spool too (R-STOR-004). Example: 20 concurrent uploads of 500 MB, plus 2 GiB, needs about 12 GiB.
  A `worker` pod spools only the cache fills of jobs and, from P1, the `nuget-symbol-index` job,
  which spools one extracted PDB at a time ([formats/nuget.md](formats/nuget.md) R-NUGET-021), so
  its default is smaller.
- `resources.requests.ephemeral-storage` should cover the expected steady use, not the limit, so
  pods schedule on ordinary nodes. The `sizeLimit` caps the worst case.
- `medium: Memory` is allowed but counts against the memory limit. It is meant for small,
  latency-critical installations only.

Acceptance criteria:

- With `spool.sizeLimit: 3Gi` and `headroom: 1GiB`, 3 concurrent 1 GiB uploads: two succeed and the
  third receives `503` with `Retry-After` (or succeeds after a retry). The pod is never evicted
  (`kubectl get events` shows no eviction).
- `helm template … --set spool.sizeLimit=5Gi --set config.storage.upload.max_size=10GiB` fails.

### R-HA-061 · Volumes: nothing shared, nothing persistent
Priority: **MVP** · Covers: HA-005

No ReadWriteMany storage class can be assumed (owner constraint, INV-13). trove needs none: no pod
mounts a volume that another pod writes, and no pod keeps data on a volume beyond its own life.
Every volume the chart renders:

| Pods | Mount | Volume | Access | Lifetime |
|---|---|---|---|---|
| `web`, `worker` | `/var/lib/trove/spool` | `emptyDir` (default) or a generic ephemeral volume (R-HA-035) | node-local, or one ReadWriteOnce (or ReadWriteOncePod) claim per pod | the pod |
| every trove pod | `/tmp` | `emptyDir` with `tmp.sizeLimit` (backup and restore pods: `backup.tmpSizeLimit`, because they stream, R-BAK-012) | node-local | the pod |
| every trove pod | `/etc/trove/trove.yaml` | ConfigMap | read-only projection | — |
| every trove pod | `/var/run/secrets/trove/…`, `/etc/trove/ca/` | Secret or ConfigMap projections (R-HA-030) | read-only projection | — |

- The chart renders no PersistentVolumeClaim, no StatefulSet and no `volumeClaimTemplates`. A
  generic ephemeral volume is a claim that Kubernetes creates for one pod and deletes with it
  ([generic ephemeral volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes)).
  `values.schema.json` admits only `ReadWriteOnce` and `ReadWriteOncePod` as its access modes
  (R-HA-029).
- Nothing the services need is on a trove volume: PostgreSQL holds metadata, S3 holds blobs,
  backups and the revocation journal, Valkey holds ephemeral state (INV-3, INV-4, INV-18).
- Dependencies outside the chart: PostgreSQL instances use one ReadWriteOnce claim each, owned by
  their operator (CloudNativePG: one per instance); Valkey and its Sentinels need no claim, because
  Valkey's content may be lost (R-HA-056); the S3 store is the platform's. Draining a node that
  holds a PostgreSQL instance follows R-HA-059.
- `extraVolumes` belong to the operator. The chart cannot see the access mode of a claim they
  reference, so this requirement covers the volumes the chart renders.

Acceptance criteria:

- For every value set in `ci/`, `helm template` output contains no `ReadWriteMany` or
  `ReadOnlyMany`, no `kind: PersistentVolumeClaim`, no `kind: StatefulSet` and no
  `volumeClaimTemplates` (checked with `yq` in CI).
- `helm template … --set spool.type=ephemeral --set 'spool.ephemeral.accessModes={ReadWriteMany}'`
  fails schema validation.
- The local profile's kind cluster offers only its default storage class, which provisions
  ReadWriteOnce volumes on the node, and the whole R-HA-052 suite passes there.

### R-HA-036 · Resources and Go runtime limits
Priority: **MVP** · Covers: HA-052

- Defaults: requests 1 CPU and 1 GiB memory, memory limit 2 GiB, no CPU limit (`web`). `worker`
  and backup pods: 500m and 512 MiB, memory limit 1 GiB. Requests are set, never left empty, so the
  scheduler and the autoscaler have a basis.
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

- **One entry point per endpoint (HA-012).** All client traffic of the default endpoint (UI, API,
  package protocols, raw files, login) uses one port and one Service (`<fullname>:8080`), which
  selects the `web` pods. The Ingress has one rule per host, path `/`, `pathType: Prefix`, to that
  Service; web pools of R-HA-062 (P1) add more specific prefixes of the same host. Each further
  endpoint has its own port, Service `<fullname>-ep-<name>` and its own Ingress or HTTPRoute, with
  its own class, hosts, optional wildcard host and TLS Secret ([endpoints.md](endpoints.md)
  R-EP-008); its rules follow this requirement in the same way. There is no second route that
  bypasses anything, because no internal component sits behind the replica. The operations port is
  never routed by the Ingress or the Gateway.
- **No session affinity anywhere (INV-12, INV-18, [architecture.md](architecture.md) R-ARCH-032).**
  Every Service the chart renders, the client Service and the Services of web pools, keeps
  `sessionAffinity: None`, fixed and not a value, and the edge route sets no affinity cookie. The
  chart renders no cookie or client-IP affinity on the Ingress or the HTTPRoute, and its examples
  (R-HA-039) contain none; an operator who adds
  one through annotations or `extraObjects` gains nothing, because any `web` replica can answer any
  request (R-HA-001). The edge proxy balances requests over the ready `web` endpoints.
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
  a Gateway listener, or a cloud load balancer. Traffic from the edge to the pods is HTTPS when
  `tls.enabled` is `true` (R-HA-063): the edge verifies the trove certificate against the chart's CA. With
  `tls.enabled: false` it is plain HTTP inside the cluster (H-38). A service mesh with mutual TLS (Istio, Linkerd) or CNI encryption works transparently
  and closes H-38 at the platform level.
- `ingress.hosts` defaults to the host of `config.server.base_url`, so the URL clients use and the
  URL trove writes into metadata cannot diverge. A further endpoint's Ingress takes its hosts from
  that endpoint's `baseUrl`, `hosts` and `wildcardHost` the same way; a wildcard host needs a
  wildcard DNS record and a wildcard certificate in its TLS Secret ([endpoints.md](endpoints.md)
  R-EP-003, R-EP-004).
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
- Subdomain-per-repository routing (`docker-all.registry.example.com`) needs a wildcard host and a
  wildcard certificate. It is an endpoint with `wildcardHost: "*.registry.example.com"` and
  `wildcardRoot: label` ([endpoints.md](endpoints.md) R-EP-006, R-EP-007): the chart renders the
  wildcard host into that endpoint's Ingress and HTTPRoute, and the binding maps the host label to
  the repository. A Docker Engine mirror is an endpoint host bound to one repository the same way.
  The OCI page specifies the registry side.
- The chart renders no rewrite rules or snippets. This avoids the catalog's mismatch between edge
  rewrites and the server's routing method, where pushes break while pulls work.

Acceptance criteria (when the OCI format exists):
`docker login trove.example.com && docker push trove.example.com/docker-local/test:1` through
the chart's Ingress succeeds without any Ingress annotation beyond R-HA-039's.

### R-HA-042 · Network policies
Priority: **MVP** · Covers: HA-054

**Status.** The ingress policy is shipped and enabled by default: the `web` pods admit port 8080 from `http.from`
(anywhere when empty), port 8081 from `ops.from` (the monitoring namespace by default) and `probeCIDRs`; the `worker`
pods admit 8081 only; the migration Job admits nothing. The egress policy is not implemented, which is H-41. Enforcement
on the cluster's CNI is not verified in this build.

With `networkPolicy.enabled: true` (default) the chart renders, for the `web` and `worker` pods:

- **Ingress.**
  - Port 8080 from `networkPolicy.ingress.http.from`; empty means from anywhere, so the ingress
    controller works wherever it runs. `worker` pods do not listen on 8080 (R-HA-001).
  - The port of each further endpoint, on the pods that serve it, only from that endpoint's
    `networkPolicy.from` (policy `<fullname>-ep-<name>`, [endpoints.md](endpoints.md) R-EP-008), so
    each port admits only its own edge. Pods of an endpoint pool admit neither 8080 nor another
    endpoint's port.
  - Port 8081 only from `networkPolicy.ingress.ops.from` (default: namespace `monitoring`) and
    `probeCIDRs`.
  - Nothing else; replicas do not talk to each other.
- **Egress** (only with `networkPolicy.egress.enabled: true`):
  - DNS;
  - PostgreSQL (`database.to`, port 5432), which includes a pooler (R-HA-058);
  - Valkey data nodes and Sentinels (`valkey.to`, ports 6379 and 26379);
  - object storage (`objectStorage.to`);
  - optionally the EKS Pod Identity agent (`169.254.170.23:80`);
  - optionally the internet on 443/80 except private and link-local ranges (upstream registries,
    OIDC issuers, STS);
  - `extra` rules.

  This is the second layer behind the egress address policy of R-REPO-038.
- The migration Job gets the policy `<fullname>-migrate`, the restore Job `<fullname>-restore` and
  the backup pods `<fullname>-backup`. None of them accepts any ingress. Their egress, with egress
  enabled: the migration Job to DNS and PostgreSQL only; the backup pods to DNS, PostgreSQL, object
  storage and the backup target (`backupTarget.to`); the restore Job to DNS, PostgreSQL, object
  storage, the backup target, and the source blob bucket for the revocation journal
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-024), which `objectStorage.to` covers when
  both buckets are on the same store. None of the three reaches Valkey: the backup does not use it
  and the restore never touches it (R-BAK-022, R-HA-057 rule 7).
- **Valkey and the Sentinels admit only trove's pods** ([architecture.md](architecture.md) H-49).
  Whoever can write under trove's key prefix can deny service, so the Valkey data nodes and the
  Sentinels MUST be reachable only from trove's `web` and `worker` pods, from each other and from
  their own operator. The chart renders no Valkey (R-HA-028), so this ingress policy belongs to
  whoever runs Valkey; with Valkey in the release namespace it selects trove's pods by their
  labels:

  ```yaml
  apiVersion: networking.k8s.io/v1
  kind: NetworkPolicy
  metadata:
    name: trove-valkey-ingress                    # example
  spec:
    podSelector:
      matchLabels:
        app.kubernetes.io/name: trove-valkey      # example: the Valkey data nodes and Sentinels
    policyTypes: [Ingress]
    ingress:
      - from:
          - podSelector:
              matchExpressions:
                - {key: app.kubernetes.io/name, operator: In, values: [trove]}
                - {key: app.kubernetes.io/instance, operator: In, values: [trove]}   # example: the release
                - {key: app.kubernetes.io/component, operator: In, values: [web, worker]}
          - podSelector:
              matchLabels:
                app.kubernetes.io/name: trove-valkey  # example: replication and Sentinel traffic
        ports:
          - {port: 6379}
          - {port: 26379}
  ```

  The local profile applies this policy to its Valkey (R-HA-051), and the egress policy above
  limits trove's side to `valkey.to`.
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
- In the local profile, a connection to the Valkey and Sentinel ports from a pod in namespace
  `default`, and from the restore Job's pod, times out, while every `web` and `worker` pod connects.

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
Priority: **MVP** · Covers: HA-055

MVP since 2026-10-28. INV-12 requires that a Valkey outage is alerted on, the backup alerts of
[backup-and-restore.md](backup-and-restore.md) R-BAK-014 are MVP, and MVP acceptance criteria
(R-HA-010, R-HA-057) expect these alerts to fire. The ServiceMonitor and the PrometheusRule are
templates over metrics the MVP already exports, so the change adds no backend work.

- **Logs.** trove writes JSON lines to stdout only (INV-16). The cluster's log collector ships them.
  The chart renders no logging sidecars. A sidecar that needs to (e.g. a SIEM forwarder) goes into
  `sidecars`.
- **ServiceMonitor.** It scrapes the `ops` port of `<fullname>-ops`, every pod, path `/metrics`
  (R-OPS-006). When `config.metrics.auth: static`, the bearer token comes from
  `secrets.metricsToken`. Its endpoint always renders two relabelings that set the target labels
  `role` and `pool` from the pod's labels
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-006, Q-OPS-13):
  `role` from `app.kubernetes.io/component` (`web` or `worker`), and `pool` from the label
  `trove/pool` (R-HA-028), which carries the pool's name on the pods of each entry of `web.pools`
  and `worker.pools` (R-HA-062) and is empty on the main Deployments, so their series carry no
  `pool`. `metrics.serviceMonitor.relabelings` is appended after these two:

  ```yaml
  relabelings:
    - sourceLabels: [__meta_kubernetes_pod_label_app_kubernetes_io_component]
      targetLabel: role
    - sourceLabels: [__meta_kubernetes_pod_label_trove_pool]
      targetLabel: pool
  ```
- **PrometheusRule.** Default alerts; names are stable and individually disabled with
  `disabledAlerts`:

| Alert | Expression (sketch) | For |
|---|---|---|
| `TroveReplicasNotReady` | `min by (deployment) (kube_deployment_status_replicas_available{deployment=~"<fullname>-(web\|worker).*"}) < 2`, per Deployment, so one role without replicas fires while the other is healthy (needs kube-state-metrics) | 5m |
| `TroveDatabaseUnreachable` | `sum(rate(trove_db_retries_total{kind="connection"}[5m])) > 0 and sum(rate(trove_http_requests_total{code="503"}[5m])) > 0` | 2m |
| `TroveObjectStoreUnreachable` | `min(trove_storage_s3_circuit_open) == 1` (all replicas) | 2m |
| `TroveObjectStoreUnreachableSingleReplica` | `max(trove_storage_s3_circuit_open) == 1 and min(trove_storage_s3_circuit_open) == 0` | 5m |
| `TroveDatabasePoolSaturated` | `histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{db_pool="request"}[5m])) > 0.1`; the control connection's waits are the next row's ([operations-and-observability.md](operations-and-observability.md) R-OPS-046) | 10m |
| `TroveControlConnectionWaits` | `histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{db_pool="control"}[5m])) > 1`: readiness-critical loops wait for the control connection (Q-HA-20), before `invalidation` and `database` start failing on every replica (R-OPS-046) | 5m |
| `TroveJobsStuck` | `max(trove_jobs_oldest_queued_age_seconds) > 900` | 5m |
| `TroveLeaseLost` | `increase(trove_lease_lost_total[15m]) > 3` | 0m |
| `TroveMixedVersions` | `count(count by (version) (trove_build_info)) > 1` | 2h |
| `TroveSpoolNearlyFull` | `max(trove_spool_used_bytes / (trove_spool_used_bytes + trove_spool_free_bytes)) > 0.9` | 10m |
| `TroveBlobMissing` | `increase(trove_storage_blobs_damaged_total{status="missing"}[1h]) > 0` | 0m |
| `TroveGCStale` | `time() - max(trove_gc_last_success_timestamp_seconds) > 172800` | 1h |
| `TroveBackupCheckFailed` | `min(trove_backup_check_ok) == 0` | 1h |
| `TroveRevocationJournalBacklog` | `max(trove_revocation_journal_pending) > 0` (R-HA-054) | 15m |
| `TroveValkeyUnavailable` | `max(trove_valkey_available) == 0` (every replica degraded, R-HA-057) | 2m |
| `TroveValkeyUnavailableSingleReplica` | `min(trove_valkey_available) == 0 and max(trove_valkey_available) == 1` | 5m |
| `TroveValkeySentinelsDegraded` | `min(trove_valkey_sentinels_reachable) < 2` (no majority of three, R-HA-056; the gauge counts the answers to a probe of every configured Sentinel, R-HA-057) | 5m |
| `TroveValkeyFailoverFlapping` | `max(increase(trove_valkey_primary_changes_total[1h])) > 2` (failovers repeating, [operations-and-observability.md](operations-and-observability.md) Q-OPS-15) | 0m |
| `TroveValkeyPrimaryDisagreement` | `max(trove_valkey_primary_views) > 1`: replicas name different primaries, for example one still bound to a demoted primary (Q-OPS-15, R-ARCH-031) | 5m |
| `TroveBackupStale` | `time() - max(trove_backup_last_success_timestamp_seconds) > 1.5 * max(trove_backup_expected_interval_seconds)`, or `trove_backup_last_success_timestamp_seconds` absent for longer than `backup.expected_interval` ([backup-and-restore.md](backup-and-restore.md) R-BAK-014). Renamed from `TroveBackupTooOld` | 0m |
| `TroveBackupFailed` | `max(trove_backup_last_failure_timestamp_seconds) > max(trove_backup_last_success_timestamp_seconds)` (R-BAK-014). Not `kube_job_status_failed > 0`, which keeps firing for as long as `failedJobsHistoryLimit` keeps a failed Job, also after later runs succeeded; an `interrupted` run does not count | 0m |
| `TroveBackupRunningLong` | `max(trove_backup_running_seconds) > 0.5 * max(trove_backup_expected_interval_seconds)` (R-BAK-014) | 0m |
| `TroveBackupGlobalHoldLong` | `max(trove_backup_gc_hold{mode="all"}) == 1` (R-BAK-014) | 30m |
| `TroveBackupSourceDamage` | `max(trove_backup_source_blobs_damaged) > 0` (R-BAK-014) | 0m |
| `TroveBackupVerifyFindings` | `max(trove_backup_last_verify_findings) > 0` (R-BAK-014) | 0m |

Metric names come from [operations-and-observability.md](operations-and-observability.md) R-OPS-008
and [storage.md](storage.md); `trove_storage_blobs_damaged_total{status}` is storage.md's
(R-STOR-027), counted by every detection of a missing or corrupt blob. This page adds
`trove_db_retries_total`, `trove_db_pool_resets_total`, `trove_db_commit_unknown_total`,
`trove_lease_lost_total`, `trove_backup_check_ok`, `trove_revocation_journal_pending` and the
Valkey metrics of R-HA-057. The `trove_backup_*` gauges are
[backup-and-restore.md](backup-and-restore.md)'s (R-BAK-014); every `web` and `worker` replica
reads them from the run records in PostgreSQL, because a finished backup pod cannot be scraped. The
backup thresholds come from `backup.expected_interval` through the gauge
`trove_backup_expected_interval_seconds`, the same value that drives the API's `expectedInterval`
and the UI's overdue mark, so the chart has no threshold value of its own.
`trove_valkey_primary_views` and the `db_pool` label are
[operations-and-observability.md](operations-and-observability.md)'s (R-OPS-008). The alert
mapping to the owner's constraints is R-OPS-046's.
Expressions are sketches; the rule file is tested with `promtool test rules` in CI.

Acceptance criteria:

- `promtool check rules` and `promtool test rules` pass, with one rule test per alert that fires on
  its failure input and stays silent on a healthy one.
- With the S3 store stopped in the local profile, `TroveObjectStoreUnreachable` fires in a
  Prometheus scraping through the ServiceMonitor; with Valkey stopped, `TroveValkeyUnavailable`
  fires.
- With `config.backup.expected_interval: 1h` and the backup CronJob suspended,
  `TroveBackupStale` fires 90 minutes (1.5 × `expected_interval`) after the last success.
- A backup run that ends `interrupted` by a drain, followed by a successful retry, never makes
  `TroveBackupFailed` fire; a run that ends `failed` makes it fire until the next success.
- Every series scraped from a pod of a web pool carries `role="web"` and `pool="<name>"`; series of
  the main `web` Deployment carry `role="web"` and no `pool`.

### R-HA-046 · Extension points
Priority: **P1** · Covers: HA-048, HA-049, HA-050, HA-051

- `extraEnv` and `extraEnvFrom` (all trove containers), `web.extraEnv`, `worker.extraEnv`,
  `backup.extraEnv`, `restore.extraEnv` and `migrations.extraEnv` (per workload), `extraVolumes`
  and `extraVolumeMounts` (trove container), `initContainers`,
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
  `imagePullSecrets` apply to all pods, including the hook Jobs and the backup CronJob.
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
  The chart pulls no other image. No init container image, no bundled database or Valkey, no proxy
  image.
  Signatures and attestations are copied with `cosign copy`.

Acceptance criteria:

- `helm template … | grep -E 'image:' | sort -u` lists exactly one image.
- `cosign verify` of a release image succeeds with the documented identity.
- An installation from a mirror with `global.imageRegistry: registry.internal.example` pulls
  nothing from the public registry (verified with a deny-all egress policy on the nodes' public
  registry access in the test).

### R-HA-048 · Uninstall and data removal
Priority: **MVP** · Covers: HA-071

- `helm uninstall trove -n trove` removes every release-managed resource, the backup CronJob and
  its Jobs included. The chart renders no StatefulSet, no PersistentVolumeClaim and no resource
  with `helm.sh/resource-policy: keep`, so uninstall leaves no claim behind that would have to be
  either kept or deleted by label (HA-071, R-HA-061).
  - With `spool.type: emptyDir` (default) there is no claim at all.
  - With `spool.type: ephemeral` each pod's claim is a generic ephemeral volume owned by its pod.
    Kubernetes deletes the claim with the pod
    ([generic ephemeral volumes](https://kubernetes.io/docs/concepts/storage/ephemeral-volumes/#generic-ephemeral-volumes)).
    The claim template carries the release's labels, so the claims are visible while pods run.
    Whether the underlying volume is deleted follows the storage class's reclaim policy.
    The storage class of a generic ephemeral volume SHOULD have `reclaimPolicy: Delete`; with
    `Retain`, released volumes holding transient upload data accumulate and must be deleted by
    hand.
- Hook resources are not part of the release
  ([chart hooks](https://helm.sh/docs/topics/charts_hooks/)). The migration Job deletes itself
  through `ttlSecondsAfterFinished`. The hook ConfigMap and ServiceAccount are removed with
  `kubectl -n trove delete configmap,serviceaccount,job -l app.kubernetes.io/instance=trove,app.kubernetes.io/component=migrate`
  (printed by `NOTES.txt` and by `helm uninstall`'s documentation). The restore Job is an ordinary
  resource and goes with the release.
- **Data removal is separate, manual and irreversible.** Uninstall touches neither the database nor
  the buckets nor Valkey nor the Secrets. Removing data:
  1. `DROP DATABASE trove;` and the roles of R-HA-014;
  2. empty the blob bucket including all object versions (a lifecycle rule that expires current
     and noncurrent versions, or the store's tooling), then delete it;
  3. delete the trove backups in the backup target (R-HA-060) and the PostgreSQL backups of the
     platform layer (R-HA-023);
  4. delete trove's Valkey keys (`valkey.key_prefix`) and the Valkey user, or let them expire;
  5. last, the encryption key Secret.

  Deleting the keys first would make the sealed secrets in the backups undecryptable while the
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
| Kubernetes | 1.34 to 1.37 tested (the range Helm 4.3 supports, verified 2026-10-28 on [Helm version skew](https://helm.sh/docs/topics/version_skew/)). The chart uses no field newer than 1.31: PDB `unhealthyPodEvictionPolicy` and Job `podFailurePolicy` are stable since 1.31, `nodeTaintsPolicy` is on by default since 1.26 and GA since 1.33, CronJob `timeZone` stable since 1.27. 1.31–1.33 are expected to work untested |
| Helm | 4.x. Helm 3 (latest 3.x) is supported until its security support ends on 2027-02-10 ([Helm 3 end of life](https://helm.sh/blog/helm-v3-end-of-life)) |
| Gateway API (optional) | CRDs v1.2 or later, Standard channel |
| Prometheus Operator CRDs (optional) | `monitoring.coreos.com/v1` for ServiceMonitor and PrometheusRule |
| PostgreSQL | 16+ (R-HA-013), highly available (CloudNativePG, Patroni, a managed service) |
| Connection pooler (optional) | PgBouncer 1.23 or later in transaction mode for the main pool: a request pooler and a control pooler, at least two instances each (R-HA-058, Q-HA-20) |
| Valkey | 9.0 or later, highly available with Sentinel: a primary, at least one replica, at least three Sentinels on different nodes (R-HA-056) |
| Object storage | S3 API meeting R-STOR-035, versioning recommended (R-HA-023) |
| Node architectures | `amd64`, `arm64` |
| Ephemeral storage | Nodes with enough local disk for the spool, or a ReadWriteOnce StorageClass for `spool.type: ephemeral`. No ReadWriteMany class is needed (R-HA-061) |
| OpenShift | 4.x with `restricted-v2` (R-HA-033) |

- **Network locality (HA-008).** Replicas, PostgreSQL, Valkey and the bucket MUST be in one
  region. Spreading replicas over availability zones of that region is recommended (R-HA-032), with
  the PostgreSQL instances, the Valkey nodes and the Sentinels spread the same way. The backup
  target SHOULD be in another failure domain than the blob bucket (R-HA-060). One cluster stretched across regions is not supported.
  Separate sites use R-HA-026.

Acceptance criteria: the CI matrix installs the chart on kind with Kubernetes 1.34 and 1.37 using
Helm 4 and the latest Helm 3, and runs R-HA-052's smoke subset.

### R-HA-050 · Sizing guidance and profiles
Priority: **MVP** · Covers: HA-067, HA-052

Starting points. They are not measured: trove does not exist yet. The reference load test
(npm and NuGet clients replaying a recorded mix of installs, restores and publishes against remote,
local and virtual repositories) replaces these numbers before the beta, and CI publishes its results per
release.

| Profile | Typical use | `web` replicas (HPA min–max) | `worker` replicas (HPA min–max) | `web` requests CPU / memory | `web` memory limit | `database.pool.max_conns` | `web` spool | PostgreSQL | Pooler (R-HA-058) | `max_connections` (R-HA-016) | Valkey `maxmemory` |
|---|---|---|---|---|---|---|---|---|---|---|---|
| small | ≤ 50 concurrent clients, ≤ 500 GB of blobs | 3 (no HPA) | 2 (no HPA) | 500m / 512Mi | 1Gi | 10 | 10Gi | 2 vCPU, 4 GiB, 3 instances | none | 150 | 256 MiB |
| medium | ≤ 300 concurrent clients, CI-heavy | 4 (4–8) | 2 (2–4) | 1 / 1Gi | 2Gi | 20 | 20Gi | 4 vCPU, 16 GiB, 3 instances | none | 400 | 512 MiB |
| large | ≤ 1,000 concurrent clients, several TB | 6 (6–16) | 3 (3–6) | 2 / 2Gi | 4Gi | 30 | 50Gi (`ephemeral`) | 8 vCPU, 32 GiB, 3 instances | transaction mode: request and control poolers, 2 instances each, 100 server connections in total | 200 | 1 GiB |

- `max_connections` follows the R-HA-016 formula with `maxReplicas`, `maxSurge: 1`, the default
  lease pool, a backup Job of 4 connections (R-BAK-005) and 15 more (2 migration, 10 reserved, 3
  superuser). Small: 7 × 10 + 3 × 4 + 19 = 101 → 150. Medium: 14 × 20 + 5 × 4 + 19 = 319 → 400.
  Large, through the pooler: 100 + 7 × 4 + 19 = 147 → 200; without it the same profile would need
  767.
- The minimum replica counts follow the drain rule of R-HA-059: each profile's peak is assumed to
  fit into one `web` replica fewer than its minimum. The reference load test replaces that
  assumption with the measured capacity of one replica.
- Valkey: memory holds fetch locks, negative-cache entries of remote repositories and rate-limit
  counters, each under 1 KiB ([architecture.md](architecture.md) R-ARCH-030); CPU is negligible at
  these sizes. The numbers are estimates for `maxmemory`; the container limit adds about 25 % for
  Valkey's own overhead. Sentinels need a few tens of MiB each.
- While Valkey is unavailable, upstreams receive more requests and changes take up to a poll
  interval (R-HA-057, rule 5). The reference load test runs every profile once with Valkey stopped
  and records upstream request rates and PostgreSQL load.
- CPU goes mostly into TLS-free HTTP streaming, the four hashes per upload (R-STOR-004) and JSON
  metadata rendering. npm proxying of large packuments is the most memory-intensive path. Raise the
  memory limit first when remote npm repositories dominate.
- PostgreSQL storage: metadata only, roughly 1–3 KB per file item including indexes, plus audit
  events (R-OPS-017 retention). 10 million files ≈ 10–30 GB plus audit (estimate).
- S3: content addressing spreads keys over 65,536 two-level prefixes. AWS S3 scales request rates
  per prefix ([performance guidelines](https://docs.aws.amazon.com/AmazonS3/latest/userguide/optimizing-performance.html)),
  so throttling is unlikely below thousands of requests per second.
- Grace period: `terminationGracePeriodSeconds` ≥ p99 transfer duration (uploads and downloads) +
  `shutdown_drain_delay` + 15 s; longer transfers are cut at the deadline and resumed or retried
  ([architecture.md](architecture.md) Q-ARCH-18, R-HA-011).

Acceptance criteria: the profile files exist and render (R-HA-029). The reference load test runs
against each profile in CI before the beta, and this table is updated from its results in the same change.

### R-HA-051 · Local development profile
Priority: **MVP** · Covers: trove-only

**TLS in the local profile (R-ARCH-035, R-HA-063).** The dev CA issues trove's listener certificate. Traefik
serves the client edge over HTTPS on `TROVE_DEV_HTTP_PORT` and connects to trove over HTTPS, verifying the dev
CA; `base_url` is then an `https` origin of localhost. SeaweedFS serves S3 over TLS. PostgreSQL (CloudNativePG,
`verify-full`) and Valkey (TLS only) are unchanged. The integration stack runs PostgreSQL, PgBouncer and S3 over
TLS and its tests use `verify-full`. The plain-HTTP addresses named elsewhere in this requirement are superseded
by this paragraph.

**A second endpoint in the local profile ([endpoints.md](endpoints.md) R-EP-008).** `values-dev.yaml` renders a
further endpoint `customers` on port 8443 with base URL `https://packages.trove.localhost:<TROVE_DEV_HTTP_PORT>`, a
Traefik route of its own for that host and a certificate from the dev CA, so E0.16 runs on the profile. Tests reach
the host with `curl --resolve`, because the resolution of `*.localhost` differs between developer machines.

A developer brings up an HA-shaped installation on a laptop with one command: the real chart with
two `web` and two `worker` replicas, PostgreSQL with a standby, Valkey with a replica and three
Sentinels, an S3 store and the backup CronJob. Failover of every dependency can therefore be tested
locally and in CI with the same files. Files live in `deploy/dev/`. Tool versions (kind, kubectl,
Helm, openssl, CloudNativePG operator chart, PostgreSQL image, Valkey image, SeaweedFS image) are
pinned once in the repository's tool pins and read by the task runner and CI alike. Images are
pinned by digest.

| File | Content |
|---|---|
| `deploy/dev/kind.yaml` | One control-plane and three worker nodes, labeled with three zones, so spreading, drains and node loss can be exercised with two replicas per role (R-HA-059) |
| `deploy/dev/postgres.yaml` | CloudNativePG `Cluster` `trove-db`: 2 instances, synchronous replication with `dataDurability: preferred` (a stopped standby does not pause writes on a laptop), the settings of R-HA-013, `bootstrap.initdb` with database `trove` and owner `trove` (single-role mode) |
| `deploy/dev/pooler.yaml` | Optional pooler overlay (`dev-up POOLER=1`): CloudNativePG `Pooler` resources `trove-db-pooler-rw` and `trove-db-pooler-control` in transaction mode with 2 instances each, as in R-HA-058, and the values that point `database.host`, `database.control_host` and `database.session_host` at them |
| `deploy/dev/valkey.yaml` | Valkey with Sentinel (Q-HA-15), from the official `valkey/valkey` image: StatefulSet `trove-valkey` with 2 pods (pod 0 starts as primary, pod 1 as its replica) and StatefulSet `trove-valkey-sentinel` with 3 pods, both behind headless Services and spread over the three nodes, each with an `emptyDir` and no claim. Sentinels monitor the primary as `trove` with quorum 2, `down-after-milliseconds 5000`, `failover-timeout 60000`, `resolve-hostnames yes` and `announce-hostnames yes`, so stable pod host names survive restarts. An init container asks the running Sentinels for the current primary before a Sentinel or Valkey pod starts, so a restarted pod does not announce pod 0 as primary after a failover. TLS only (`port 0`, `tls-port 6379`, Sentinel `tls-port 26379`, `tls-replication yes`, `tls-auth-clients optional`), the ACL user `trove` with the example rule of [architecture.md](architecture.md) R-ARCH-031, a Sentinel password, `maxmemory 256mb`, `maxmemory-policy allkeys-lru`, `save ""`, `appendonly no`, a PDB with `maxUnavailable: 1` for each StatefulSet, and the ingress NetworkPolicy of R-HA-042 that admits only trove's `web` and `worker` pods and the Valkey pods themselves (H-49) |
| `deploy/dev/s3.yaml` | Single-process SeaweedFS Deployment (master, volume server, filer and S3 gateway in one `weed` process) with an `emptyDir`, Service `trove-s3` on port 9000 mapped to the S3 gateway, plus a Job that creates bucket `trove-blobs` with versioning enabled, bucket `trove-pg-archive` for the WAL archive (Q-HA-3) and bucket `trove-backups` as backup target (R-HA-060) |
| `deploy/dev/values-dev.yaml` | Chart values: `web` and `worker` 2 replicas each, `highAvailability.enforce: true`, `spool.sizeLimit: 4Gi`, `forcePathStyle: true`, static S3 credentials, `base_url: http://localhost:8080`, the CloudNativePG Secrets, the three Sentinel addresses with `primary_name: trove`, the Valkey Secrets, and the backup target `trove-backups` on the same SeaweedFS endpoint with a schedule every 6 hours |

Steps behind the task-runner target `dev-up` (GNU Make, the root `Makefile`; Q-HA-21):

```shell
kind create cluster --name trove --config deploy/dev/kind.yaml
helm upgrade --install cnpg cloudnative-pg --repo https://cloudnative-pg.github.io/charts \
  -n cnpg-system --create-namespace --version "$CNPG_CHART_VERSION" --wait
kubectl create namespace trove
deploy/dev/valkey-certs.sh                   # openssl: a dev CA and one certificate for the Valkey and Sentinel host names
kubectl -n trove create secret generic trove-valkey-ca --from-file=ca.crt=.dev/valkey/ca.crt
kubectl -n trove create secret tls trove-valkey-tls --cert=.dev/valkey/tls.crt --key=.dev/valkey/tls.key
kubectl -n trove create secret generic trove-valkey \
  --from-literal=username=trove --from-literal=password="$(head -c 24 /dev/urandom | base64)"
kubectl -n trove create secret generic trove-valkey-sentinel \
  --from-literal=password="$(head -c 24 /dev/urandom | base64)"
kubectl -n trove apply -f deploy/dev/postgres.yaml -f deploy/dev/valkey.yaml -f deploy/dev/s3.yaml
kubectl -n trove wait --for=condition=Ready cluster/trove-db --timeout=5m
kubectl -n trove rollout status statefulset/trove-valkey --timeout=5m
kubectl -n trove rollout status statefulset/trove-valkey-sentinel --timeout=5m
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
- Valkey runs TLS-only with `verify-full` as well; the dev CA lives in `.dev/`, which is ignored by
  Git and recreated by `dev-up`.
- Further targets:
  - `dev-down` deletes the kind cluster;
  - `dev-failover` runs `kubectl cnpg promote trove-db <standby>`;
  - `dev-valkey-failover` runs `SENTINEL FAILOVER trove` on a Sentinel; `dev-valkey-kill`
    force-deletes the Valkey primary pod; `dev-valkey-outage` scales Valkey and the Sentinels to 0
    for a given duration;
  - `dev-s3-outage` scales the S3 store to 0 for a given duration;
  - `dev-drain` runs variant A of R-HA-059's drain test against a node the target picks;
  - `dev-backup` creates a Job from the backup CronJob and waits for it; `dev-restore` installs a
    second release into an empty namespace from the latest backup (R-HA-060);
  - `dev-chaos` runs the R-HA-052 suite.
- **S3 store (Q-HA-3).** The profile and the per-change CI run SeaweedFS
  ([repository](https://github.com/seaweedfs/seaweedfs), Apache-2.0, maintained, images published).
  Its documentation lists bucket versioning, version listing, delete markers and deletes by version
  ID, which the restore drill (R-HA-025) needs
  ([S3 object versioning](https://github.com/seaweedfs/seaweedfs/wiki/S3-Object-Versioning)). It
  also states that version-specific lifecycle rules are not fully implemented; the profile
  therefore relies on the orphan sweep (R-HA-012), not on lifecycle rules. The profile counts as
  valid only while `trove admin storage check` (R-STOR-035) and the restore drill pass on it.
- **Valkey (Q-HA-15).** The profile's Valkey counts as valid only while the failover scenarios of
  R-HA-057 pass on it. When the official Valkey Helm chart releases Sentinel support, the profile
  moves to it.
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
- Running the binary outside the cluster (`trove serve --role all` from an IDE, R-HA-001) uses the
  same dependencies through `kubectl port-forward`; it is a convenience, not a tested profile.

Acceptance criteria:

- On a machine with Docker and the pinned tools, `dev-up` completes in under 15 minutes and
  `npm ping --registry http://localhost:8080/api/npm/npm-all/` succeeds.
- `dev-failover` and `dev-valkey-failover` during an `npm ci` loop cause no failed iteration.
  `dev-s3-outage 60s` behaves as R-HA-010 states, `dev-valkey-outage 60s` as R-HA-057 states.
- `dev-backup` followed by `dev-restore` yields a second installation whose `npm view` output
  matches the first.
- CI runs the same profile, so "works in dev" and "works in CI" use one configuration.

### R-HA-052 · Failure-injection test suite
Priority: **MVP** · Covers: trove-only

Every row of R-HA-007 and every HA acceptance criterion on this page runs as an automated scenario
against the local profile (R-HA-051) in CI. Load comes from real clients in parallel loops:

- `npm ci` against a virtual repository (remote and local members);
- `dotnet restore --no-cache` against a NuGet virtual repository;
- `npm publish` of unique versions to a local repository;
- `curl -T` of 200 MB files;
- for the drain and Valkey scenarios, the k6 load of R-HA-059 as well.

| Scenario | Injection | Pass condition |
|---|---|---|
| Pod crash | `kubectl delete pod --grace-period=0 --force` of a random `web` or `worker` replica every 2 min for 20 min | No client iteration fails after its own retries; no item row without a durable object (`trove admin storage verify`) |
| Rolling restart | `kubectl rollout restart` of `deploy/trove-web`, then of `deploy/trove-worker` | Zero failed iterations |
| Rolling upgrade | Previous minor → current with the migration Job | Zero failed iterations; R-HA-020 verification passes |
| Rollback | `helm rollback` after the upgrade | Zero failed iterations |
| PostgreSQL switchover | `kubectl cnpg promote` | Zero failed iterations |
| PostgreSQL primary loss | Force-delete the primary pod | Failures only within `retry.maxElapsed`, all `503`; recovery without intervention |
| PostgreSQL partition of one replica | NetworkPolicy denying egress to PostgreSQL for one pod | That pod unready within 15 s; no failed iteration overall; its leases move |
| PostgreSQL outage | Scale the database to 0 instances for 2 min (hibernation) | No container restart; full recovery within 30 s of return |
| Object-store outage | The S3 store scaled to 0 for 30 s and for 5 min; during the 5-minute run one replica is deleted | Metadata, UI and API succeed throughout; blob transfers answer `503` with `Retry-After` (R-STOR-036); no running replica turns unready (R-HA-008); the replacement pod started during the outage stays not ready with its container restart count unchanged, because the startup check retries without exiting (R-STOR-035), and becomes ready within 60 s after the store returns; full recovery within `storage.s3.circuit_breaker.open_duration` + 10 s |
| Node drain | R-HA-059's drain test, variants A to E | R-HA-059's pass conditions |
| Node loss | `docker stop` of a kind worker node | Replacement pods ready within `nodeFailureTolerationSeconds` + 60 s; no duplicate singleton work (the lease sessions of R-HA-006) |
| Change propagation without Valkey | `CLIENT KILL TYPE pubsub` for one replica, then Valkey stopped | R-HA-004: changes take effect within `cluster.epoch_poll_interval` + 1 s; replicas stay ready |
| Change log unreachable for one replica | NetworkPolicy denying its egress to PostgreSQL | R-HA-004: `invalidation` fails, the replica leaves the Service and catches up before it returns |
| Valkey planned failover | `SENTINEL FAILOVER trove` | R-HA-057 |
| Valkey primary loss | Force-delete the Valkey primary pod | R-HA-057 |
| Valkey outage | Valkey and Sentinels scaled to 0 for 5 min; one `web` pod deleted meanwhile | R-HA-057 |
| Sentinel quorum loss | Sentinels scaled to 1, then to 0 | R-HA-057 |
| Valkey content loss | `FLUSHALL` on the primary | R-HA-057 |
| Slow Valkey | Fault proxy adds 2 s to every Valkey command | R-HA-057 |
| Valkey failover flapping | Three `SENTINEL FAILOVER trove` within one hour | `TroveValkeyFailoverFlapping` fires ([operations-and-observability.md](operations-and-observability.md) R-OPS-046); zero failed iterations |
| Replica bound to a stale primary | A failover forced while one replica's Sentinel traffic is blocked and the old primary keeps that replica's connections, with `valkey.sentinel.refresh_interval` raised for the test | `TroveValkeyPrimaryDisagreement` fires after 5 minutes; after the block is lifted, the replica moves to the new primary within `valkey.sentinel.refresh_interval` + 1 s and the alert resolves |
| Control-pool saturation | A test build holds the control connection of every replica (direct profile), or every server connection of the control pooler (pooler profile), with back-to-back `pg_sleep(2)` statements for 6 minutes | `TroveControlConnectionWaits` fires; `TroveDatabasePoolSaturated` does not, because it selects `db_pool="request"`; replicas stay ready, because each wait stays below `cluster.epoch_max_staleness` |
| Maintenance stays quiet | R-HA-059 variants A to E and the planned failover of R-HA-057, each with a Prometheus scraping through the ServiceMonitor | No alert of R-HA-045 fires (R-OPS-046) |
| Pooler instance loss (pooler profile) | Delete a pooler pod, gracefully and forced | R-HA-058 |
| Lease holder loss | Kill the worker holding the `scheduler` lease; separately, kill the worker running a GC job mid-run | Another worker holds the `scheduler` lease within `cluster.lease_retry_interval` + 1 s and no occurrence is enqueued twice (R-HA-005, R-HA-006); the GC job is completed by another worker (its exclusive job key, Q-STOR-13); no S3 delete without a committed `deleting` row |
| Spool exhaustion | Concurrent uploads above the budget | `503` with `Retry-After`; no eviction |
| Migration failure | A migration that fails on purpose | Old pods keep serving; `helm upgrade` fails |
| Backup under load | `kubectl create job --from=cronjob/trove-backup` during the load | Zero failed iterations; the backup completes (R-HA-060) |
| Backup during a drain | Drain the node of a running backup pod | R-HA-059 variant E |
| Database saturation | Every request connection held by `pg_sleep(4)` transactions for 60 s, direct and in the pooler profile | R-HA-008: every replica stays ready, no job returns to `queued` |
| Migration during a drain | Drain the node of the running migration Job pod during `helm upgrade` | R-HA-019: the Job completes, the upgrade succeeds |
| Restore during a drain | Drain the node of the restore pod during the blob copy of the trove-backup restore drill | R-HA-060: the restore Job completes, the result matches the backup |
| Restore drill, trove backup | `dev-restore` from the latest backup into an empty namespace (R-HA-060) | As [backup-and-restore.md](backup-and-restore.md) states; a token revoked after the backup stays rejected (R-HA-054) |
| Restore drill, point in time | R-HA-025 steps 1–7 | As R-HA-025 |

- The suite records client-side errors per scenario and fails on any deviation from the pass
  condition. Results are published as a CI artifact per run.
- The pod-crash, switchover, rolling-upgrade, object-store and Valkey-primary-loss scenarios and
  variant A of the drain test run on every change to `main`. The full suite, the pooler profile and
  the trove-backup restore drill run nightly; the point-in-time restore drill runs weekly.

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
| trove-only | P2 | Read-only queries routed to PostgreSQL standbys |
| trove-only | P2 | Worker autoscaling on queue depth (`trove_jobs_oldest_queued_age_seconds` through an external metrics adapter) |
| trove-only | P1 | Valkey endpoint mode for managed services that offer a failover-following primary endpoint but no Sentinel (Q-HA-14) |
| trove-only | P2 | Valkey Cluster (sharded) as a supported topology (Q-HA-14) |
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
  - the break-glass password;
  - the Valkey password, which allows deleting fetch locks, resetting rate-limit counters and
    publishing forged change messages within the ACL's key pattern, all of which cost
    availability only (R-ARCH-030);
  - the backup target's static keys, if used, which reach every backup.

  Restrict `get`/`list` on Secrets in that namespace to the platform team. Enable encryption at
  rest for Secrets in etcd. Prefer an Argon2id hash for the break-glass Secret (R-AUTH-014).
- **Least privilege in the database.** The runtime role is not a superuser and, in two-role mode,
  has no DDL rights (R-HA-014). It can neither change nor delete audit rows, nor shorten their
  retention, so a compromised replica or a SQL injection cannot erase its traces in the database
  (R-HA-014, R-OPS-015); the owner and superusers still can, which external log shipping covers.
  DDL credentials exist only in the short-lived migration and restore Job pods. A dedicated backup
  login (`database.backup_role`) reads every table but writes only its own run records, blob pins
  and audit events, so the backup pod cannot change trove's data (R-HA-014, R-BAK-015).
- **Least privilege in the bucket.** The runtime credential cannot delete object versions or change
  versioning, lifecycle or bucket policy. A compromised replica cannot destroy the history that
  restores depend on (R-HA-023). The archive bucket for PostgreSQL backups uses different
  credentials.
- **Backups are a second copy of everything except the keys.** A trove backup holds every
  metadata row, token hashes and sealed secrets included, and every referenced blob (INV-19). It
  belongs in a bucket of its own whose credential the replicas do not hold; the backup's
  credential reads the blob bucket and writes the target, nothing more, through its own
  ServiceAccount (R-HA-060). The restore Job has an identity of its own that reads the target and
  the source's journal and writes the new blob prefix; its ServiceAccount exists only while
  `restore.enabled` is set. Retention needs delete rights on the target, so a stolen backup
  credential can delete backups; bucket versioning or object lock on the target keeps them
  recoverable. The encryption keys are never in a backup (R-HA-027).
- **Valkey is reached with authentication and TLS.** The chart refuses an installation without a
  Valkey password, and plaintext needs an explicit `allow_insecure` (R-HA-029, R-HA-056). Nothing
  trove serves to a client and no positive authentication or authorization result is read from
  Valkey, so whoever can write trove's keys can cost availability (duplicate fetches, reopened
  rate-limit budgets, early cache evictions), never access or integrity
  ([architecture.md](architecture.md) R-ARCH-030, Q-ARCH-16). A dedicated ACL user limited to
  trove's key and channel prefix and to the commands of R-ARCH-031 keeps a leaked trove credential
  away from other applications' data, and the egress policy lists the Valkey pods (R-HA-042). An
  ingress NetworkPolicy on the Valkey data nodes and Sentinels that admits only trove's `web` and
  `worker` pods, which whoever runs Valkey adds, keeps other workloads from writing under the
  prefix ([architecture.md](architecture.md) H-49, R-HA-042); backup, restore and migration pods
  hold no Valkey credential and reach no Valkey port.
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
- **Restore is a security event.** It reverts revocations (H-37), so post-restore and the restore
  of a trove backup record an audit event, print the warning and re-apply the revocation journal
  (R-HA-025, R-HA-054, R-HA-060). Replay applies removals and only those updates that grant nothing
  beyond the object's current state, so whoever can write to the bucket can at worst revoke access
  through the journal, never grant any. A restore also moves the installation to a new Valkey
  keyspace, so no lock, negative-cache entry or rate-limit counter of the lost state is read again
  (R-HA-057, rule 7).
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
| (a) 15 | Support 15, 16, 17, 18 | Widest reach | End of life 2027-11-11, probably within a year of trove's first release, which forces a floor bump soon after the beta; four versions in the CI matrix before 19 arrives |
| (b) 16 | Support 16, 17, 18; 19 after its general availability | End of life 2028-11-09; nothing in the design needs a newer version | Installations on 15 must upgrade PostgreSQL first |
| (c) 17 | Support 17 and 18 | Fewest versions to test | Excludes 16, which the community supports for two more years, without a feature that needs 17 |

**Choice:** (b) — PostgreSQL 16 is the floor; the floor moves only after a version's community end
of life, announced one minor release ahead.

**Rationale:** A floor that expires within months of the first release would force a breaking bump
right after the beta, and a higher floor would exclude supported servers for no gain. The spec was
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

Revisited on 2026-10-29 with the endpoints of [endpoints.md](endpoints.md): the choice is unchanged. Options (b) and
(d) are no longer per-installation burdens but optional endpoint configurations: a host bound to one repository is
the dedicated registry host of (d) and serves Docker Engine mirrors, and a wildcard host with a label binding is the
subdomain mode of (b) ([endpoints.md](endpoints.md) R-EP-006, R-EP-007, Q-EP-6). On a bound host the repository
comes from the binding, not from the first segment after `/v2/`.

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

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice moves from (a) to (b), and (b) moves from P2 to MVP. INV-12 now requires that PostgreSQL connections do not grow without bound with the replica count, while the load is expected to need many `web` replicas; and the fetch-collapse lock pool, one of the session-dependent pools, moved to Valkey (INV-11). Option (d) was added.

The lease pool, the migration lock and, before the revisit, the listener session need
session-level state, which PgBouncer in transaction mode breaks.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Not supported | Direct connections or a session-mode pooler only | Advisory-lock fencing (R-HA-006) works as designed; nothing to build | Every replica brings its whole main pool (20 by default) to the server, so the database's connection limit caps how far `web` scales: the large profile would need 767 connections (R-HA-016), against INV-12 |
| (b) Split connections | The main pool goes through a transaction-mode pooler; the lease pool, the migration Job and the backup and restore Jobs use a second, direct host (`database.session_host`) | Main-pool connections at the server stay at the pooler's pool size however many replicas run (147 instead of 767 for the large profile); fencing is unchanged; direct connections stay possible | Two hosts to configure and secure; per-session settings become role defaults; prepared statements need a pooler that tracks them; the pooler is one more HA component to run and drain |
| (c) Table-based leases | Replace session-level locks with lease rows | Works through any pooler | Gives up immediate release on session end, which R-HA-006's fencing builds on; `LISTEN` would still need a direct session |
| (d) No PostgreSQL session at all | Leases and every notification move to Valkey; every connection goes through the pooler | No direct connection per replica | Leases whose loss would corrupt data must stay in PostgreSQL (INV-3); a Valkey failover can lose a lock, and fencing on a Valkey lock does not protect PostgreSQL writes |

**Choice:** (b) — supported from the MVP on, and optional: `database.pooler.mode: transaction`
sends the main pool through a transaction-mode pooler, and every session-dependent connection uses
`database.session_host`. The session host carries only the lease pool of `worker` replicas,
`trove migrate` and `trove backup`; `web` replicas have no coordination session and never connect
to it. Direct connections stay supported and remain the default.

**Rationale:** The per-replica cost under (a) is the main pool, and (b) moves exactly that behind
a fixed server-side pool while keeping fencing for singleton work on direct sessions.
[architecture.md](architecture.md) replaced `LISTEN` with a change log polled on each replica's
control connection, a reserved connection of the main class (Q-ARCH-15), and gave `web` replicas
no coordination session and no session connection (Q-ARCH-14), so with (b) the server-side
count no longer depends on the number of `web` replicas at all; only the `worker` leases remain,
and workers follow job load. (c) and (d) both give up fencing or move data-protecting mutual
exclusion out of PostgreSQL, which INV-3 forbids. Building (b) at MVP costs a second host key, a preflight check of the role defaults
and the pooler profile in CI; the protocol work is the pooler's. The development agent revisits
this if PgBouncer's prepared-statement tracking causes failures in the conformance suite, or if
the number of `worker` replicas, whose leases bypass the pooler, ever approaches the database's
connection limit.

**Applied in the spec:** new R-HA-058 (pooler, session host, role defaults, pooler HA, CloudNativePG
example). R-HA-006 item 5: only the lease pool of `worker` replicas, `trove migrate` and
`trove backup` use the session host; `web` replicas hold no session connection; the main pool may
use the pooler. R-HA-004: invalidations need no connection outside the main pool. R-HA-014: role
defaults per runtime login. R-HA-015: `application_name` form and
startup parameters through the pooler; `session_host` and `session_port`. R-HA-016: the budget per
role, with and without the pooler, and the growth rule. R-HA-007, R-HA-009: pooler loss and
failover behind the pooler. R-HA-031: `database.session_host`, `database.session_port`,
`database.pooler.*`. R-HA-050: the large profile uses the pooler. R-HA-052, R-HA-059: pooler
scenarios. The Deferred row for (b) is removed. [architecture.md](architecture.md) R-ARCH-017 and
R-ARCH-019 use the same session host and main-pool rules (Q-ARCH-14), so no cross-page edit
remains.

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

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the journal moves from P1 to MVP. INV-19 makes `trove backup restore` an MVP path, and a restore from a scheduled backup undoes up to one backup interval of revocations, a day with the default schedule, instead of the minutes a point-in-time restore loses. That is the case this decision's revisit condition named: production restores in the beta.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Document the gap | The operator replays revocations from the audit log stream by hand | Nothing to build | Depends on someone doing it correctly under restore pressure; a leaked token revoked after *t* is valid until then |
| (b) Revocation journal in object storage | Every change to an access-bearing object is also written, through an outbox that the audit writer fills in the same transaction, to a write-once journal under the blob bucket; `post-restore` re-applies the removals among the entries newer than *t* and never grants | Automatic; reuses the versioned bucket restores already depend on and the transactional audit events that already exist; a forged entry can deny access but never grant it | New write path (outbox, shipping job) and per-object replay rules; removals made in the last seconds before the old database stopped can be missed; an object whose history replay cannot reproduce exactly is suspended rather than restored |
| (c) Invalidate all tokens on restore | `post-restore` revokes every token | Closes the gap completely for tokens | Breaks every CI pipeline and service account at once, does not cover permission removals, and punishes the restore instead of the attacker |
| (d) Replay from the audit stream | `post-restore --audit-log <file>` re-applies revocations from an export of the `log: audit` stream | No new write path in trove | Depends on the operator's log pipeline keeping complete, untruncated events for the window; anyone who can write to the pipeline can inject events |

**Choice:** (b) at MVP — a revocation journal in object storage that `post-restore` and the
restore of a trove backup replay.

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

Until the revisit, the journal was P1 because the restore procedure (R-HA-025) was the only way
back and restores to a point minutes before the incident lose few revocations. With the backup job
of INV-19 every installation restores from backups taken on a schedule, and the window of undone
revocations grows to the backup interval. A leaked token revoked in that window would come back in
every restore, so the journal ships with the backup job. The cost is the new write path and the
per-object replay rules on the owning pages, which already exist as specification. The development
agent revisits this if suspensions after restore drills turn out to be frequent; the replay rule
then moves to exact per-field narrowing where the owning page can define it.

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
Security notes describe what stays live. After the revisit: R-HA-054 is MVP and is also replayed by
the restore of a trove backup; R-HA-031 drops the P1 mark; H-37 and the Security notes describe the
MVP state; R-HA-052 adds the trove-backup restore drill with a revoked token. Cross-page:
[authorization.md](authorization.md) R-AUTHZ-031 to MVP; the journal entries of
[identity-and-authentication.md](identity-and-authentication.md) and
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) to MVP; [backup-and-restore.md](backup-and-restore.md)
replays the journal; the roadmap moves the journal from M11 to M5.

### Q-HA-10 · Default of the egress NetworkPolicy

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Off by default | Ingress policy on; egress opt-in, with a warning in `NOTES.txt` and an example values file | An installation never breaks because the chart guessed the network wrong; R-REPO-038 already checks trove's own outbound addresses | A compromised process can reach everything the pod network reaches until the operator enables egress |
| (b) On with a generic allow-list | DNS, port 5432 and port 443 to anywhere except link-local ranges | Blocks cloud metadata endpoints by default | Breaks NodeLocal DNSCache (link-local address), GKE workload identity (metadata server on 169.254.169.254), EKS Pod Identity (169.254.170.23), S3 stores and upstreams on other ports; for AWS S3 the rule cannot be narrower than "any address on 443", because NetworkPolicy has no DNS names or prefix lists |
| (c) On, explicit targets required | Rendering fails unless database and object-storage targets are given | No guessing; forces a deliberate egress design | Breaks the minimal install of R-HA-031; managed databases and AWS S3 have no stable pod selectors or narrow CIDRs to give |
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
leave a row forever. The status screen renders from endpoints that are MVP (this API, R-OPS-004,
the task and job API of R-OPS-019, R-API-049, and the backup summary of R-API-050), and it is the
only UI view of the replicas, the scheduled tasks and the key ring. It no longer adds no backend
work: since the role split and the drain constraint, it groups replicas by role and states drain
safety and the Valkey view per replica ([api-and-ui.md](api-and-ui.md) R-UI-038, Q-UI-7), which
needs two more registry columns, `node` (from `TROVE_NODE_NAME`, R-HA-030) and
`valkey_sentinels_reachable` (R-HA-057), and the backup summary endpoint of R-API-050, all MVP.
Each costs a column per heartbeat or one read query. The development agent revisits this only if
R-OPS-003's API grows beyond a read-only list.

**Applied in the spec:** R-HA-003: states which parts of the registry are MVP, that the registry
records the node and the Sentinels each replica reaches, that the UI status screen (R-UI-038) is
MVP as well, and that R-HA-001 and R-HA-020 verify through the API. R-HA-030: the chart sets
`TROVE_NODE_NAME`. [operations-and-observability.md](operations-and-observability.md) R-OPS-003
states the same (registry with `node` and `valkey_sentinels_reachable`, API and status screen MVP),
so no cross-page edit remains.

### Q-HA-13 · Valkey and readiness

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-12 states that Valkey being unavailable degrades trove and does not make replicas unready. The
open question is whether any Valkey condition may hold back a replica's first readiness, as the
object-store startup check does (Q-HA-1).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Never gate | Valkey is reported and alerted on, never part of readiness | Simplest; INV-12 word for word | A release with a mistyped Sentinel name, password or CA rolls out and degrades every replica at once: fetch collapse and negative caching per replica, rate limits approximated per replica, changes at poll speed. Only an alert notices |
| (b) Gate continuously | Readiness fails while Valkey is unreachable | Removes a replica whose own path to Valkey is broken | Contradicts INV-12: a Valkey outage would empty the Service, although every replica still serves correctly |
| (c) Gate the first readiness on any Valkey failure | As Q-HA-1 does for object storage | Stalls every rollout whose Valkey does not answer | A replica started during a Valkey outage cannot become ready, so drains, crashes and the autoscaler lose capacity while PostgreSQL carries the extra load (R-HA-057, rule 5); contradicts INV-12 for new replicas |
| (d) Gate the first readiness on configuration errors only | A rejected credential (`WRONGPASS`, `NOAUTH`, `NOPERM`), a failed TLS verification, or a primary name that no reachable Sentinel knows holds a new replica back; unreachability never does | Stalls a misconfigured rollout with `maxUnavailable: 0`, as Q-HA-1's gate does; a replica started during an outage still becomes ready; INV-12 holds for every outage | The error classes are code to test; a Sentinel that lost its configuration answers like a wrong primary name, so a broken Valkey deployment can stall a trove rollout |

**Choice:** (d) — only configuration errors hold back the first readiness; Valkey outages never
affect readiness.

**Rationale:** A configuration error is part of the release being rolled out, so stalling that
rollout keeps the working replicas, which is the protection Q-HA-1 gives against a wrong bucket. An
outage is in a shared dependency, so holding back new replicas would only remove capacity. (a)
would let a typo keep every replica in degraded mode for good, with rate limits that hold only
approximately ([architecture.md](architecture.md) R-ARCH-034), a quiet loss of what Valkey was
added for. (b) and (c) contradict INV-12. The classifier needs three error classes that Valkey and Sentinel report
distinctly. The development agent revisits this if a Sentinel that lost its state after a restart
ever stalls a rollout; the null-answer case would then need confirmation over a longer period
before it counts as a configuration error.

**Applied in the spec:** R-HA-008: the Valkey bullet. R-HA-007: the credential row. R-HA-056:
acceptance criteria for a wrong primary name, a wrong password and unreachable Sentinels. R-HA-057:
rule 4. Cross-page: [operations-and-observability.md](operations-and-observability.md) R-OPS-001
gets a `valkey` check that is reported, not gating, except for configuration errors before the
first readiness.

### Q-HA-14 · Valkey topologies trove supports

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Sentinel only | The reference topology and nothing else | Exactly the owner's constraint; one discovery path | Managed services without Sentinel, such as a primary endpoint that follows failover through DNS, stay out for good |
| (b) Sentinel at MVP, a primary-endpoint mode at P1 | `valkey.mode: endpoint` later connects to one DNS name that the provider moves on failover; trove re-resolves and checks `ROLE` after errors | Opens managed services later without weakening the reference topology | A second discovery path to test; failover time then depends on the provider's DNS |
| (c) Sentinel and Valkey Cluster | Also the sharded Cluster protocol | Valkey memory beyond one node | trove's Valkey data is small and ephemeral (R-HA-050: 1 GiB for the large profile); multi-key operations and publish/subscribe behave differently under Cluster; the owner asked for Sentinel |
| (d) Standalone allowed in production | One instance | Simplest | Contradicts INV-12 |

**Choice:** (b) — Sentinel is the only production topology at MVP; a standalone instance exists for
development; the endpoint mode is P1; Valkey Cluster is deferred.

**Rationale:** The owner fixed Sentinel, and the MVP supports exactly that, tested by the local
profile and CI. Managed services are a real deployment target, and some of them offer a primary
endpoint instead of Sentinel, so the endpoint mode is the cheapest extension: the client already
re-resolves and checks `ROLE` after every error (R-HA-056). Cluster mode solves a memory problem
that trove's data does not have. The development agent revisits this if an installation needs more
Valkey memory than one node offers, which calls for (c).

**Applied in the spec:** R-HA-056: the topology table and the configuration's `mode`. R-HA-029:
Sentinel mode with at least three addresses under `highAvailability.enforce`. Deferred: the endpoint
mode (P1) and Valkey Cluster (P2).

### Q-HA-15 · Valkey with Sentinel in the local profile and CI

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The official Valkey Helm chart, released version | `valkey-io/valkey-helm` at its latest release | One upstream to follow | Its latest release, 0.12.1, has no Sentinel settings (checked 2026-10-28) |
| (b) The official chart at a pinned commit of its main branch | Sentinel settings exist there | Upstream manifests | An unreleased chart pinned to a commit; its values may change before the release |
| (c) A third-party operator | For example an operator that manages Valkey with Sentinel through custom resources | Closer to how some installations run Valkey | One more operator with its own CRDs and release cadence in a laptop profile; not what most production installations run either |
| (d) Manifests of trove's own | `deploy/dev/valkey.yaml`: two StatefulSets from the official `valkey/valkey` image, `emptyDir` only, TLS, an ACL user and Sentinel timings set for tests | Full control over TLS, ACL, timings and failure injection; the official image is the only dependency | trove maintains the manifests and the Sentinel bootstrap init container |

**Choice:** (d) — the local profile and CI run Valkey and three Sentinels from manifests in
`deploy/dev/valkey.yaml`.

**Rationale:** The profile exists to exercise failover deterministically: kill the primary, flush
it, stop the Sentinels, delay commands. Own manifests make each of these a known operation and keep
the dependency at the official image, pinned by digest. (b) pins unreleased code, and (c) brings an
operator into every developer's laptop for a component whose production form trove does not
prescribe. The development agent revisits this when the official chart releases Sentinel support:
the profile then moves to (a), provided the failover scenarios of R-HA-057 pass on it.

**Applied in the spec:** R-HA-051: the `valkey.yaml` row, the certificate and Secret steps, the
Valkey targets and the validity bullet. R-HA-056: the bullet on what the chart does not bundle.

### Q-HA-16 · Default replica counts and the capacity rule for drains

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-12 requires at least two replicas per role and states that the load exceeds one replica; a
node drain must cause no outage when the setup is sized for it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two per role; the rule documented | Defaults stay at INV-12's minimum | Smallest footprint | With a load above one replica, draining one of two `web` replicas leaves less capacity than the load needs, so the default installation is not sized for a drain |
| (b) `web` 3, `worker` 2; the rule documented and tested | `web` defaults to one replica more than a peak of two needs; `worker` stays at two, because job load waits in the queue instead of failing requests | The default installation survives a drain at the load the owner expects; the drain test runs with the defaults | One more `web` pod than (a) |
| (c) A capacity value the chart validates | `web.capacity.peakReplicas`; the chart refuses a minimum below peak plus `maxUnavailable` | The rule cannot be forgotten | A number nobody knows before the reference load test; a wrong value gives false confidence |
| (d) A PDB with `minAvailable` equal to the peak | The budget refuses evictions below the peak | Protects capacity directly | Blocks a drain whenever the replica count equals the peak, so maintenance stalls; with an autoscaler the right number moves |

**Choice:** (b) — defaults of three `web` and two `worker` replicas, with the N + 1 rule of
R-HA-059 documented and tested.

**Rationale:** The owner's statement fixes the smallest sized installation: more than one replica
carries the peak, so a drain needs at least one more. Three `web` replicas is the smallest default
that fits; `worker` load is queued work, and a drain that removes one of two workers for a minute
delays jobs without failing a request. (c) turns an unmeasured number into a validation. (d) trades
a slower drain for capacity, but a drain that never finishes is an operational failure of its own.
The development agent revisits this when the reference load test publishes the capacity of one
replica, which makes (c) worth its value.

**Applied in the spec:** R-HA-031: `web.replicaCount: 3` and `web.autoscaling.minReplicas: 3`.
R-HA-032: the replicas bullet. R-HA-059: rule 1. R-HA-050: profile minimums.

### Q-HA-17 · The backup CronJob is on by default

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

This is the same question as [Q-BAK-9](backup-and-restore.md#q-bak-9--backup-cronjob-on-by-default),
seen from the chart; both pages choose the CronJob on by default with a required target.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) On by default, target required | `backup.enabled: true`; rendering fails without `config.backup.target.bucket` | Every installation has the backup the owner requires from its first day; the error names the missing value; `backup.enabled: false` is a visible opt-out | One more required value in the minimal install |
| (b) Off by default, `NOTES.txt` warns | The operator enables it | Smaller minimal install | An installation without backups is the outcome of doing nothing |
| (c) On by default, target inside the blob bucket | Backups under a prefix of the blob bucket | Nothing to configure | Same failure domain and same credential as the data the backup protects; under the blob prefix the orphan sweep would delete the backups (R-STOR-026); a compromised replica could delete them |

**Choice:** (a) — the backup CronJob is rendered by default, and the chart fails without a backup
target.

**Rationale:** The backup job is an owner requirement, and a fail-fast error that already names the
fix costs one value, the same trade as Q-HA-8. (b) makes the unsafe state the default. (c) puts the
backup where the failures it should survive happen. The development agent revisits this if target
installations commonly have no second bucket; a validated location in the same bucket but outside
the blob prefix, with credentials of its own, would then be the fallback.

**Applied in the spec:** R-HA-060: the CronJob and the target rules. R-HA-029: the target rows.
R-HA-031: the `backup` values and the required-value list.

### Q-HA-18 · How a restore runs in the chart

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`trove backup restore` restores into an empty installation and applies the migrations itself, up
to the backup's step before the load and the rest after it
([backup-and-restore.md](backup-and-restore.md) R-BAK-021, R-BAK-023). A restore copies every
referenced blob, so it can take hours.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A hook Job before the migration | `restore.enabled` renders a pre-install hook that runs before the migration hook; one `helm install` restores and starts | One command into an empty namespace | Helm and Argo CD wait for hooks up to their timeouts, which a multi-hour restore exceeds; the migration hook would run after a restore that already migrated |
| (b) An ordinary Job with both roles at zero | While `restore.enabled`, the chart renders the restore Job, no migration Job and a suspended backup CronJob, and refuses replica counts above zero; a second `helm upgrade` with normal values starts the roles | No Helm or GitOps timeout limits the restore; nothing writes during the restore; the restore owns the migrations, as R-BAK-021 requires; an evicted or failed restore resumes in the Job's next pod, without a flag (R-BAK-021, Q-BAK-10) | Two Helm commands and a `kubectl wait`; turning `restore.enabled` off before the Job completes must not let a migration overtake the restore, which R-HA-019's anchor check prevents |
| (c) A documented `kubectl run`, no chart support | The operator starts the restore by hand | Nothing in the chart | Mounts, credentials and service accounts are reproduced by hand at the worst moment |
| (d) A Job created from the backup CronJob | `kubectl create job --from=cronjob/…` with an edited pod spec | Reuses the CronJob's pod template | The pod spec has to be edited after creation; the backup pod has no migration credentials, by design |

**Choice:** (b) — `restore.enabled` renders the ordinary Job `<fullname>-restore` with both roles at
zero, suppresses the migration Job and suspends the backup CronJob.

**Rationale:** A restore is rare and stressful, and the one thing it must not meet is a timeout
halfway through a copy of terabytes. (b) keeps Helm out of the restore's duration, keeps every
writer away while it runs, and leaves the migrations to the command that knows the backup's
schema step. The second command is the same `helm upgrade` an operator runs anyway.
[backup-and-restore.md](backup-and-restore.md) R-BAK-012 and R-BAK-021 still describe (a)'s hook
and change to this sequence as a cross-page consequence, so both pages state one procedure; the
automatic resumption they specify (Q-BAK-10) works the same in an ordinary Job. The development
agent revisits this if restores turn out to be
short enough for Helm's timeouts in practice, which would make (a)'s single command worth its
coupling.

**Applied in the spec:** R-HA-060: the restore Job, its mounts and the operator's sequence; after
review, its own ServiceAccount `<fullname>-restore` (the backup account may not write blobs), the
backup Job's pod failure policy with `restore.backoffLimit: 2` and automatic resumption instead of
`backoffLimit: 0` and a `--resume` argument, which R-BAK-021 does not have, and the way to continue
a Job that failed for good. R-HA-019: no migration Job while `restore.enabled`, and `trove migrate`
refuses an anchored database. R-HA-028: the resource table. R-HA-029: the `restore.backupId` and
replica-count rows. R-HA-031: the `restore` values. R-HA-042: the restore Job's policy. R-HA-052:
the restore-during-a-drain scenario. Cross-page: [backup-and-restore.md](backup-and-restore.md)
R-BAK-012 and R-BAK-021 describe the restore Job as a pre-install hook and change to this
ordinary Job and sequence.

### Q-HA-19 · Priority of the platform backup layer and the point-in-time restore

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-HA-023 (platform layer: point-in-time recovery, bucket versioning, least privilege) and R-HA-025
(point-in-time restore with the post-restore cleanup) carried the catalog's P1 for OPS-033, and the
roadmap built both for the beta anyway
([roadmap Q-ROAD-2](../coverage/roadmap.md#q-road-2--backup-and-restore-in-the-beta), option (a)).
INV-19 adds trove's own backup at MVP.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep both P1 | Labels as before; the roadmap keeps its named exception | No label change | The cleanup of R-HA-025's step 4 (replica rows, jobs, deletion pause, sessions, Valkey keyspace, journal replay) is, apart from the deletion pause, what a restore of a trove backup performs at MVP (R-BAK-022, R-BAK-024), so one cleanup would carry two priorities depending on the restore path |
| (b) R-HA-025 MVP, R-HA-023 P1 | Only the restore procedure moves | The MVP restore path rests on MVP requirements only | R-HA-024, an MVP requirement, already reads R-HA-023's keys, and R-HA-023's least-privilege rules protect the source of every backup |
| (c) Both MVP | Both are MVP requirements; the platform layer stays recommended, not required, for installations | Labels match what the beta ships; no part of the MVP restore path is P1; the roadmap's exception disappears | Two more MVP requirements to count, with no extra work, since both were already scheduled for the beta |
| (d) Drop the point-in-time path | trove backups only | One restore path | The recovery point becomes one backup interval for every installation, also where the platform offers minutes |

**Choice:** (c) — R-HA-023 and R-HA-025 are MVP; the platform layer remains a recommendation for
installations, and its checks and rules are part of the beta.

**Rationale:** Q-ROAD-2 kept the labels because nothing in the design had changed. INV-19 changed
the design: restore is now an MVP path; it performs R-HA-025's cleanup in its own form and rests
on R-HA-023's least-privilege rules. Raising both labels costs nothing in schedule, because both
were in the the beta milestone already. (d) would throw away a recovery point of minutes that many
platforms provide.
The development agent revisits this if the platform layer turns out to be rarely available in
target installations; R-HA-023 could then return to P1 with its least-privilege rules moved into
R-HA-060.

**Applied in the spec:** R-HA-023: priority, introduction and the trove-backup row. R-HA-025:
priority, introduction, step 1 (backups suspended), step 4 (Valkey keyspace), step 6, and the drill
cadence. Cross-page: the roadmap's Q-ROAD-2 and M5 lists.

### Q-HA-20 · Control connection behind a transaction-mode pooler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

[architecture.md](architecture.md) R-ARCH-019 reserves one control connection per replica for the
change-log poll, the heartbeats and, after this review, the `database` check, so that an exhausted
request pool cannot take replicas out of the Service one after another. With direct connections
the reservation is real: the control connection has a server backend of its own. Behind a
transaction-mode pooler (R-HA-058, Q-HA-6) it is not: the pooler hands every client, the control
connection included, a server connection from one shared pool. When the database is saturated and
every server connection of the pooler is busy, the control queries of every replica wait behind
request transactions, and all replicas can fail `database` and `invalidation` at the same moment.
The load expected by the owner makes saturation a planned case, and the pooler is the setup
recommended when `web` scales out.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Share the request pooler | The control connection goes through the same pooler pool as requests | Nothing to configure | Under saturation, overload empties the Service instead of slowing it; job heartbeats wait as well, and workers can lose their leases. The guarantee of R-ARCH-019 holds only without a pooler |
| (b) Control connection to the session host | Every replica opens its control connection directly | A real server backend per replica, as without a pooler; no new component | One server connection per `web` replica again, so the server-side count grows with the replica count, against INV-12 and the reason for Q-HA-6 |
| (c) A control endpoint with a server pool of its own | `database.control_host`, `control_port` and `control_name` name a second pool: a second pooler (CloudNativePG: a second `Pooler`), or a database entry with its own `pool_size` in a self-managed PgBouncer | Request traffic cannot exhaust it; the server-side count stays fixed (for example 2 × 5); the control loops are short statements, so a small pool serves hundreds of replicas | One more pooler deployment to run, spread and drain; three more keys |
| (d) A separate login for control traffic | The control connection logs in as its own role through the same pooler; PgBouncer keeps one pool per database and user | No second pooler deployment | A further login with its own grants and rotation pair (R-HA-017); the per-user pool is bounded only by `default_pool_size` per instance; a managed pooler that admits one login only cannot offer it |

**Choice:** (c) — with `database.pooler.mode: transaction`, the control connection connects to a
control endpoint whose server pool serves only control connections, and the chart refuses a
control endpoint equal to the request endpoint. Without a transaction-mode pooler the control keys
stay empty and nothing changes.

**Rationale:** The reserved control connection exists so that load can slow trove down but never
switch it off, and the owner expects load beyond one replica; (a) loses that property in exactly
the setup that serves the most replicas. (b) restores it by giving back what Q-HA-6 gained. (c)
keeps both: the server-side count stays fixed, and the control loops get capacity that requests
cannot take. Its cost is one more pooler of a few connections, deployed the same way as the
request pooler. (d) avoids the deployment but adds a role and a rotation pair, and its isolation
depends on the pooler's per-user pools, which a managed pooler may not expose. The development
agent revisits this if a pooler offers reserved capacity for named clients, which would make (a)
safe, or if the saturation scenario of R-HA-052 shows control waits far below
`cluster.epoch_max_staleness` with a shared pool, which would make the second pool unnecessary.

**Applied in the spec:** R-HA-058: the control pool, its limits, the CloudNativePG example with a
second `Pooler`, the trove configuration and two acceptance criteria. R-HA-004, R-HA-007 (a
saturation row; the pooler row covers both poolers), R-HA-008 (the `database` check runs on the
control connection, and a saturation criterion), R-HA-015 and R-HA-031 (`control_host`,
`control_port`, `control_name`), R-HA-016 (the control pool inside `server_connections`), R-HA-029
(the refusal), R-HA-049, R-HA-050, R-HA-051 (the second pooler) and R-HA-052 (the saturation
scenario). Cross-page: [architecture.md](architecture.md) R-ARCH-019 (the control connection's
endpoint and the `database` check on it) and
[operations-and-observability.md](operations-and-observability.md) R-OPS-001 (the `database` check
runs on the control connection).

### Q-HA-21 · Task runner and tool pin file

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) GNU Make, root `Makefile` (chosen) | Targets for build, test, chart, dev profile and integration stack | Preinstalled on macOS and Linux runners and on the developer machines; no extra pin | Windows needs a Make port or WSL |
| (b) Taskfile | A YAML task runner | Readable task definitions | One more tool to pin and install in CI and on every laptop |
| (c) mage | Targets written in Go | No shell | Another pin and a bootstrap binary before the first target runs |
| (d) just | A command runner | Simple syntax | One more tool to pin, the same cost as (b) without a file format of its own |

Pin file (chosen with (a)): `tools/versions.env`, `KEY=value` lines only. The Makefile reads it with `include`
and exports it to the scripts; CI reads it through the `load-pins` action (`.github/actions/load-pins/action.yml`)
and appends it to `$GITHUB_ENV`. Go is pinned by the `go` line of `go.mod`, sqlc and oapi-codegen by the `tool`
directives, Angular by `web/package-lock.json`, Node by `web/.nvmrc`. `make versions-check` compares the installed
tools with the pins. Helm and kubectl in CI come from `azure/setup-*` actions pinned by commit SHA, with their
versions from the same file.

**Choice:** (a) GNU Make, and one pin file for every tool that is not a Go module or an npm package.

**Rationale:** The task runner is one more thing that every contributor must install, and Make is already on
the machines that build the project. One file for the pins means a version change touches one line, which
both the local build and CI read. The development agent would revisit the runner if Windows contributors must
build without WSL, which would make (b) the choice with the smallest cost.

**Applied in the spec:** R-HA-051 names GNU Make as the task runner (the sentence that called the target names
an example is replaced). Decision record: [ADR-125](../decisions.md#adr-125--gnu-make-and-one-pin-file-for-every-tool).

### Q-HA-22 · Upload limit against the spool of each role

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Raise the worker spool to 12 GiB so that the largest upload fits everywhere | Every role's spool holds the largest upload | One rule, no role exception | Workers hold 12 GiB of scratch for no upload they ever accept |
| (b) Drop the rule for workers, keep the 5 GiB worker spool and check nothing | The chart does not compare the two sizes for `worker` | Smallest surface | The chart cannot warn when a worker is given a larger `upload.max_size` than its spool |
| (c) Role-aware rule: `upload.max_size` must fit `spool.max_bytes` on the roles `web` and `all`; the worker spool is 5 GiB (chosen) | The binary checks the rule for `web` and `all` only; the chart renders the same rule and the worker default is 5 GiB | Workers accept no uploads, so their spool needs no upload budget; the check is exact for the roles that take uploads | Two rules to explain |

**Choice:** (c). The worker default is `5Gi` in the chart (`values.yaml`, `worker.spool.sizeLimit`).

**Rationale:** The worker role spools only job output and cache fills, never an upload (R-STOR-004). A rule
that applies to a role which accepts no uploads forces a 12 GiB default on the role that needs the least
scratch, so the rule belongs to the roles that take uploads. The development agent would revisit this if a
worker role starts accepting uploads.

**Applied in the spec:** R-HA-031 already states the worker default of `5Gi` and the rule's scope; no requirement
text changed in this round.

### Q-HA-23 · Chart validation of the `config` subtree until the binary emits its schema

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Mirror every key of `internal/config` in `values.schema.json` now | The chart holds a second copy of the configuration schema | Typed errors at `helm lint` for every key | Two copies to keep in step, and the copy is wrong the first time a key changes |
| (b) The `config` object is free-form in `values.schema.json`; the chart types the keys it owns (chosen) | `values.schema.json` types the chart-owned keys and describes the `config` object as rendered to `/etc/trove/trove.yaml` | No second copy; the chart cannot drift from the binary | Typos below `config` are caught only by the binary |
| (c) No chart validation of `config` | The binary validates at start-up only | Smallest chart | A bad value is rendered and found at runtime |

**Choice:** (b), until the binary emits `internal/config/schema.json` for the chart to consume.

**Rationale:** The binary's schema is the single source of the configuration keys (R-ARCH-020). Until it can
be read by the chart, a typed copy would be a second source. The chart keeps the keys it owns typed, so the
rejections of R-HA-029 still hold. The development agent would revisit this when the binary's schema can be
consumed; the chart test planned for that point would then run `trove admin config validate` over every
rendered value set. That test is not in `test/chart` yet.

**Applied in the spec:** R-HA-029 and R-HA-031 are consistent with this choice; no requirement text changed.

### Q-HA-24 · Edge controller, failover mechanism and the scatter profile of the local profile

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Traefik as the Ingress controller, pinned by chart and image, on `localhost` port `TROVE_DEV_HTTP_PORT` (default 8080) (chosen) | The edge is an Ingress controller, as the chart's Ingress expects | Same Ingress path as production; one pin set | One more chart and image to pin |
| (b) A plain port mapping to a Service without an Ingress | Ports are exposed without the Ingress | Fewest components | The Ingress of the chart is not exercised |
| (c) A Gateway API controller | The HTTPRoute of R-HA-038 | Matches the Gateway route | Not the priority route for the MVP (Q-HA-7) |

Scatter profile (`TROVE_PROFILE=scatter`, chosen): three `web` replicas, one per worker node, and Traefik
with `--serversTransport.maxIdleConnsPerHost=-1`, so no idle connection to a backend is kept and each request
is balanced on its own. The default profile keeps keep-alive.

Failover (chosen): `deploy/dev/failover.sh` patches `status.targetPrimary` of the CloudNativePG cluster, which is
what `kubectl cnpg promote` does, so the profile needs no plugin pin.

**Choice:** (a) for the edge; the scatter profile and the patch as stated.

**Rationale:** The local profile must exercise the Ingress path that production uses, and the scatter profile
is the only way to see load shared per request. The patch is the operator's own mechanism, so no kubectl plugin
is pinned. The development agent would revisit the edge if the Ingress path cannot be tested through a Gateway.

**Applied in the spec:** R-HA-051 and the local profile describe the same mechanisms; no requirement text changed.

### Q-HA-25 · AWS leg of the nightly run when no AWS account is configured

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The nightly run fails when the account is absent | Red nightly until the owner configures AWS | Forces the setup | A missing secret blocks every nightly signal |
| (b) The AWS job is dropped silently | No record | Quiet | A reader cannot tell that AWS was never tested |
| (c) A gate job writes an explicit SKIPPED summary; the AWS job is skipped through `needs` and shown grey, never green (chosen) | The gate reads the `AWS_CI_ROLE_ARN` secret, writes the summary and the notice, and the AWS job does not run | The skip is visible in the summary and never looks like a pass | A grey job in every nightly until the owner provides the account |

**Choice:** (c).

**Rationale:** A skipped leg must never count as a passed one. The gate is needed because a job-level `if` cannot
read secrets. The development agent would revisit this when the owner configures the AWS account and the role.

**Applied in the spec:** R-HA-051 and E0.5 already keep AWS S3 listed as skipped until the owner provides the
account; no requirement text changed.

### Q-HA-26 · Release image: how the binary gets into the image

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Multi-stage Dockerfile with the Go and Node builds inside the image | Hermetic image build | No host toolchain | A Node and Go build in every image build; slow |
| (b) The host builds the static binary with the embedded web build; the image wraps it in the pinned distroless base (chosen) | `make image` runs `make web`, a static Go build with `CGO_ENABLED=0`, then `docker buildx build` with `RUNTIME_BASE_IMAGE` from `tools/versions.env` | Fast, reproducible from the pins; no Node in the image | The image depends on the host build; the release pipeline (phase 6) must build it in CI |
| (c) Pre-built binaries from a release workflow only | No local image | Simple local | No local image for development |

**Choice:** (b).

**Rationale:** The base image and the Go toolchain are pinned, so the same inputs give the same image. The
development agent would revisit this when the release pipeline builds in CI, which would move the build into a
job with the same pins.

**Applied in the spec:** R-HA-047 and R-HA-051 describe the image; no requirement text changed.

### Q-HA-27 · Which replicas the contract guard counts as live

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every row of the replica registry | A replica that stopped long ago still counts | Conservative | A stale replica blocks an upgrade indefinitely |
| (b) A replica counts when its heartbeat is younger than `system.heartbeat_stale_after` (30 s by default) (chosen) | The registry's heartbeat decides | Stale replicas do not block | A replica whose heartbeat is late for a short time is not counted |
| (c) Only replicas that answer a probe | Live probe per replica | Exact | Probes add a path that a failover can break |

**Choice:** (b).

**Rationale:** The contract guard decides whether an older release may still run beside the migrated schema
(R-ARCH-026). The heartbeat is the one live signal every replica writes. The development agent would revisit
this if a heartbeat can be late for longer than the stale window during normal operation.

**Applied in the spec:** R-ARCH-026 states the gate; the heartbeat source as the liveness signal is the choice of this block.

### Q-HA-28 · Who creates the database schema at migration

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The operator creates the schema; `trove migrate` refuses when it is missing | The migration never creates objects that the operator did not name | Explicit ownership | An extra manual step on every installation |
| (b) `trove migrate` creates `database.schema` when it is missing and the role may create it; it never creates roles or databases, then checks the owner (chosen) | The schema is created by the migration role, and the owner check follows | One command for a new installation; roles and databases stay under the operator's control | The migration role needs `CREATE` on the database |

**Choice:** (b). When the role may not create the schema, the error names the `CREATE SCHEMA` statement to run as the
migration role (`internal/meta/postgres/migrate.go`).

**Rationale:** R-HA-014 keeps roles and databases with the operator. The schema is trove's own object, so creating it
is the natural step of the migration. The development agent would revisit this if an installation cannot grant
`CREATE` on the database to the migration role.

**Applied in the spec:** R-HA-014 keeps the operator's role and database responsibilities; no requirement text changed.

### Q-HA-29 · Where the garbage-collection pause is stored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A table with the columns of the sketch in R-HA-025 | A dedicated row shape | Typed columns | A new table for one flag |
| (b) A key-value row `deletions_paused_at` in `system_state` (chosen) | The pause is the row `system_state` with the key `deletions_paused_at`; a null value means not paused | No new table | The shape of R-HA-025's sketch is replaced |

**Choice:** (b), as built. R-HA-025's column sketch is to be settled in M5 after the restore work.

**Rationale:** The pause is one flag, and `system_state` already holds the installation's other key-value facts. The
development agent would revisit this at M5 when the restore procedure is written.

**Applied in the spec:** no requirement text changed in this round; the sketch of R-HA-025 is open for M5.

### Q-HA-30 · Plaintext switches of the Valkey connection

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No plaintext setting at all | TLS is mandatory for Valkey and for the Sentinels | Nothing to misconfigure | Local development without TLS is impossible |
| (b) Plaintext requires `valkey.allow_insecure: true` for each switch (chosen) | `valkey.sentinel.tls: false` and `valkey.tls.mode: disable` are refused unless `allow_insecure` is set; `valkey.key_prefix` must end in `:` | A plaintext connection is an explicit decision in the file | A second switch to read |
| (c) Plaintext allowed silently | The setting alone enables plaintext | Simplest | The switch is hidden from review |

**Choice:** (b).

**Rationale:** Plaintext to Valkey exposes the ACL password and the data of the request path (INV-3, H-49). The
explicit switch makes the choice visible in the configuration. The trailing colon of the prefix keeps keys of two
installations apart. The development agent would revisit this when a plaintext local profile is no longer needed.

**Applied in the spec:** R-HA-056 states the two switches and the prefix rule (added in this revision).

### Q-HA-31 · Job heartbeat, attempt counting and the slot record

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Cancel the handler on any failed extension (R-HA-005 as first written) | A single failed extension ends the work | Simplest rule | A database blip shorter than the lease cancels long jobs |
| (b) Cancel at once when another replica has taken the row; after a failed extension, cancel only once the lease could already have expired on the monotonic clock (chosen) | Bounded by the lease, not by the blip | Work survives a short outage without a second owner | The handler may run a little past the database's view of the lease |
| (c) Never cancel on a failed extension | The handler runs to its end | No cancellation on outages | A handler can outlive its lease and race a new owner |

Attempt counting (chosen with (b)): the claim that starts an attempt increments `attempt`; the reaper does not.
Slot record (chosen with (b)): the unique index `job_task_slot` on `(task, scheduled_for)` replaces a separate
schedule table.

**Choice:** (b), with the attempt and slot rules above.

**Rationale:** A database failover can take longer than a heartbeat without the lease being lost, and cancelling the
work for it wastes the attempt. The bound keeps a second owner from starting while the first still writes, because
the handler stops once the lease could have expired, and the settle statements of the job table, which name the
worker and the attempt as their owner, reject a stale attempt (`internal/jobs/worker.go`). The development agent would revisit (b) if a handler shows a write after its lease
was taken over.

**Applied in the spec:** R-HA-005: the heartbeat rule (cancel at once when the row is taken; bounded cancel after a failed
extension), the reaper rule (no increment), the schedule sketch replaced by `job_task_slot`.

### Q-HA-32 · Default of the chart's TLS wiring

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `tls.enabled` defaults to `false`; TLS is opt-in | Plain HTTP inside the cluster unless an installation sets `true` | Works without a certificate on first install | The owner constraint of 2026-10-29 would be met only by installations that opt in, so H-38 stays the default |
| (b) `tls.enabled` defaults to `true`; rendering fails without the Secret unless `tls.enabled: false` (chosen) | The chart needs a certificate Secret for every install that does not explicitly opt out | Every installation is encrypted by default; the missing Secret is found at render time, not at run time | First install needs a Secret before it renders |
| (c) TLS rendered only when the Secret exists | Silent fallback to plain HTTP when the Secret is absent | No failure on first install | A missing Secret means plaintext without any signal |

**Choice:** (b), in the pattern the chart already uses for the encryption key and the backup target (Q-HA-8, Q-BAK-9).

**Rationale:** The owner requires that every connection can run encrypted, and a default that is off would make the
encrypted state the exception. Failing at render time is the same fail-fast rule the chart applies to other required
Secrets, so no replica starts without the setting that the operator chose. The development agent would revisit the
default if a supported platform cannot provide a certificate at first install, which would make (a) the right
default for that platform only.

**Applied in the spec:** R-HA-063 states the default and the failure; H-38 is limited to `tls.enabled: false`.

## What this does not cover

### H-37 · A database restore revives revoked credentials and removed grants

Restoring PostgreSQL to time *t*, from the platform layer (R-HA-025) or from a trove backup taken at
*t* (R-HA-060), restores every token, session, user and permission policy as it was at *t*. Token
revocations, user and service-account disables, group-membership and role-binding removals, policy
narrowings and CI mapping changes made after *t* are undone. A token revoked because it leaked
becomes valid again. With scheduled backups, *t* can lie up to one backup interval in the past.
Both restore paths end all UI sessions and print a warning, but the database alone cannot know
what happened after *t*: those facts were only in the lost part of the database, in the audit log
stream and in the revocation journal.

- **Mitigated in the MVP** by the revocation journal (R-HA-054, Q-HA-9): both restore paths
  re-apply the journal kept in object storage. What stays live: removals whose outbox rows had not
  been shipped when the old database stopped (normally less than one shipping interval, default
  5 s), every removal made while `backup.revocation_journal.enabled` was false, the whole journal
  when the blob bucket that holds it is lost together with the database, and, without bucket
  versioning, journal objects deleted with the runtime S3 credential. Access granted after *t* is
  lost rather than revived, and objects whose history replay cannot reproduce exactly are
  suspended; both fail closed.
- The manual fallback for what stays live: read the `log: audit` stream from *t* to the restore,
  and repeat each revocation and removal through `trove admin token revoke` and the API.

### H-38 · Plain HTTP between the edge proxy and the replicas

This gap applies only to an installation that sets `tls.enabled: false` (R-HA-063). With the default
`tls.enabled: true`, the edge connects to the pods over TLS and trove serves it (R-ARCH-035), so there is no
plain-HTTP path between the edge and the replicas. With `tls.enabled: false`, requests travel to the pods
unencrypted, including `Authorization` headers with bearer tokens, Basic credentials and session cookies.
Anyone who can capture pod-network traffic can read them: a compromised node, a privileged pod with host
networking, or a CNI-level tap. The mitigation for such an installation is at the platform level: a service
mesh with mutual TLS, or CNI encryption (WireGuard or IPsec modes of common CNIs). The chart does not enforce
either.
