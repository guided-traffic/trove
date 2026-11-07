# High availability and Helm deployment

Status: specification — not implemented

This page specifies how trove stays available, scales out and is deployed. It covers:

- The replica model: identical, interchangeable replicas per role, with `api`, `packages` and `worker` as
  separate Deployments (the task areas of ADR-153 and ADR-155, specified for phase 6a; built today: `web` and `worker`),
  pools of a role by path prefix or queue, `backup` as a CronJob (INV-2), and how replicas
  coordinate through PostgreSQL and Valkey without talking to each other (INV-3, INV-12, INV-18).
- The failure contract of every component: pod crash, node drain and node loss, PostgreSQL failover
  with reconnect and retry, Valkey failover through Sentinel and Valkey outage, object-storage
  outage, spool exhaustion and a failed migration.
- Leader-less scheduling and singleton work under advisory-lock leases.
- Node drains without outage: the capacity rule, disruption budgets, spreading and the drain
  acceptance test under npm and NuGet load.
- Rolling upgrades with expand/contract migrations (promised from the first stable release on, [Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)),
  and the migration Job.
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

The cut of phase 6a (ADR-153, ADR-155; the chart renders it). The releases before it ran one Deployment `<fullname>-web`
behind the Service `<fullname>` that served every path, which is the shape the upgrade bridge of R-HA-064 keeps for one step.

```text
             clients: npm, pnpm, Yarn, dotnet, nuget.exe, browsers, GitLab jobs
                                    │ HTTPS
                   ┌────────────────▼─────────────────┐
                   │  Ingress controller or Gateway   │  TLS ends here; one host split by path prefix (R-HA-037)
                   └───────┬─────────────────┬────────┘
     /, /ui, /auth, /api/v1 │                 │ /api, /files, /v2, /.well-known
     Service <fullname>-api │                 │ Service <fullname>-packages        (HTTPS :8080, no session affinity)
            ┌───────────────┤                 ├─────────────────────────┐
     ┌──────▼──────┐ ┌──────▼──────┐   ┌──────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
     │ api pod     │ │ api pod     │   │ packages    │ │ packages    │ │ packages    │  Deployments <fullname>-api and
     │ :8080 :8081 │ │ :8080 :8081 │   │ :8080 :8081 │ │ :8080 :8081 │ │ :8080 :8081 │  <fullname>-packages; spool: emptyDir
     └─────────────┘ └─────────────┘   └─────────────┘ └─────────────┘ └─────────────┘
       UI, management API, sessions,     package protocols, raw files, tokens only,
       OIDC and break-glass secrets      the fills of client misses
     ┌─────────────┐         ┌─────────────┐
     │ worker pod  │   ...   │ worker pod  │  Deployment <fullname>-worker: jobs, :8081 only;
     └─────────────┘         └─────────────┘  optional pools by queue <fullname>-worker-<pool> (R-HA-062)
     ┌─────────────┐
     │ backup pod  │  CronJob <fullname>-backup ──S3──▶ backup target bucket (R-HA-060)
     └─────────────┘

  every pod ──SQL───▶ PostgreSQL primary ──streaming──▶ standbys (CloudNativePG, Patroni, RDS)
                      optional: request pool through a transaction-mode pooler, control
                      connection through a pool of its own (R-HA-058, Q-HA-20)
  api, packages, worker ─RESP─▶ Valkey primary ──replication──▶ replica; ≥ 3 Sentinels name the primary
                      (backup, restore and migration pods never connect to Valkey)
  every pod ──S3────▶ blob bucket, versioning recommended (AWS S3, MinIO, Ceph RGW, SeaweedFS)

  Helm hook before the Deployments: the migration Job. With restore.enabled: no migration Job,
  every role at zero, and the restore Job as an ordinary resource (R-HA-060, Q-HA-18)
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

- Every replica of a client-serving role MUST be able to serve every request of the route families of its role (`api`: the
  UI and every management request; `packages`: every package and raw-file request), and every `worker` replica every job
  kind of the queues it claims (R-ARCH-016). No replica holds state that another replica
  needs to answer a request (INV-2, INV-18): consecutive requests of one client, the requests of a
  multi-request protocol included, may reach different replicas. The only local state is the spool
  of the request or job that is running (R-HA-035) and in-process caches, which are invalidated via
  R-HA-004.
- There MUST be no primary, leader or seed replica in configuration. Work that must run once per
  cluster runs under leases that any `worker` replica can take (R-HA-005, R-HA-006).
- **Roles** (INV-2, [architecture.md](architecture.md) R-ARCH-001). A process has one role,
  `process.role: api|packages|worker|web|all`, which has no default. The chart starts each Deployment with
  `trove serve --role <role>`, the flag form that overrides `process.role` (R-HA-002), so the
  shared configuration file names no role:
  - `api` serves the route families `ui`, `api` and `ci_exchange` on the client listener and the operations listener, and
    never claims jobs or leases;
  - `packages` serves the route families `packages` and `files` on the client listener and the operations listener, with
    tokens only, and never claims jobs or leases;
  - `worker` claims jobs and leases, and serves only the operations listener;
  - `web` is `api` and `packages` in one process, rendered only by the upgrade bridge of R-HA-064;
  - `all` combines every role. It exists only for the local profile, for a binary started outside the
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
  needs more than one `packages` replica; the UI and management load fits one `api` replica. R-HA-059 states how many
  replicas a node drain needs on top.
- Adding a replica needs no configuration besides the replica count. A new `worker` replica takes
  jobs as soon as it is ready. Removing a replica needs no handover: its leases end with its
  database sessions (R-HA-006), its job leases expire (R-HA-005), and its Valkey locks expire
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026).

Acceptance criteria:

- With two `api` replicas behind the edge, `for i in $(seq 1 300); do curl -s -o /dev/null -w '%{http_code}\n' https://trove.example.com/api/v1/system/ping; done | sort | uniq -c`
  prints only `200`, and the `X-Trove-Replica` header of authenticated calls shows both `api` pod
  names and no other; the same loop over `GET /api/npm/web/npm-all/lodash` with a token shows the three `packages` pod names.
- `kubectl scale deploy/trove-packages --replicas=4` adds a replica that serves traffic and appears in
  `GET /api/v1/system/replicas` without any other change.
- `helm upgrade … --set api.replicaCount=1 --set packages.replicaCount=1 --set worker.replicaCount=1 --set highAvailability.enforce=false`
  yields a working installation with one replica per role against the same database, Valkey and
  bucket.
- A test proxy in front of the `api` and `packages` pod IPs, routing by the path prefixes of R-HA-037, sends every request
  to a different replica of its role in strict rotation. Through it, these flows complete (INV-18): an npm web login with its polling requests,
  `dotnet nuget push` followed by a restore of the pushed version, `npm publish` followed by
  `npm view`, and a paged API listing followed to its last page.

### R-HA-002 · Role split into api, packages and worker Deployments
Priority: **MVP** · Covers: HA-015

The split is no longer optional: it is an owner constraint since 2026-10-28, and the task areas `api`, `packages` and
`worker` are the owner's cut of 2026-11-04 (INV-2,
[ADR-153](../decisions.md#adr-153--trove-is-cut-into-task-areas-roles-of-one-binary-network-seams-only-at-trust-or-load-boundaries),
[ADR-155](../decisions.md#adr-155--three-roles-api-packages-and-worker-variants-are-pools)).

**Status.** Built (phase 6a, WP6a.3): the chart renders `<fullname>-api`, `<fullname>-packages` and `<fullname>-worker`
from the list of Deployments in [_units.tpl](../../deploy/helm/trove/templates/_units.tpl); `web.enabled: true` renders
`<fullname>-web` (`--role web`) beside them for the first two steps of R-HA-064.

- The chart MUST always render three Deployments: `<fullname>-api`, whose container runs `trove serve --role api`,
  `<fullname>-packages`, whose container runs `trove serve --role packages`, and `<fullname>-worker`, whose container runs
  `trove serve --role worker`. Each has its own replica count, resources, autoscaler, disruption budget, termination grace
  period, spool size, scheduling settings and ingress policy (R-HA-031, R-HA-032, R-HA-042). There is no single-Deployment
  mode, and no value renders `--role all`. `--role web` is rendered only by the bridge of R-HA-064 (`web.enabled: true`).
- **Services.** `<fullname>-api` (port `http` 8080) selects only the `api` pods of the main Deployment, and
  `<fullname>-packages` (port `http` 8080) only the `packages` pods of the main Deployment; the edge routes each host's paths
  to them by prefix (R-HA-037). `<fullname>-ops` selects every serving pod on 8081. `worker` pods are reached only on the
  operations port (probes and metrics). The Service `<fullname>` of the releases before the cut is rendered only by the bridge
  of R-HA-064 (Q-HA-74).
- **In-cluster callers** that address trove without the edge, such as a Bastion calling the main installation by its
  Service name ([bastion.md](bastion.md) R-BAS-008) or CI runners in the cluster, address the Service of the role whose
  families they call: `<fullname>-api` for the UI, the management API and the CI exchange, `<fullname>-packages` for the
  package protocols and raw files. A request that reaches the other role's Service answers the `404` of an unknown path
  ([architecture.md](architecture.md) R-ARCH-001). A caller that needs both families in one base URL goes through the edge.
- `worker.replicaCount` (or `worker.autoscaling.minReplicas`) MUST be at least 1, otherwise no
  scheduled work runs, and `api.replicaCount` and `packages.replicaCount` MUST be at least 1, otherwise the edge has no
  backend for their families. The chart refuses to render (R-HA-029). With `highAvailability.enforce` the minimum is 2 for
  every role and every pool, so that GC and other singleton work survive a worker pod loss without waiting for a reschedule
  and no family loses its last replica in a drain.
- The roles' pods run the same image and the same configuration file; only the `--role` argument and the per-Deployment
  environment of R-HA-062 (`TROVE_POOL_NAME`, `TROVE_JOBS_QUEUES`, `TROVE_JOBS_EXCLUDE_QUEUES`) differ. No component runs in a
  separate image or as a sidecar.
- Why: background work (GC, retention runs, reindexing, the mirror's passes, the advisory sync, webhook delivery) never
  competes with request latency for CPU and memory; a surge of package traffic cannot starve the UI and the management API;
  `packages` scales on package load, `api` on UI and API load and `worker` on job load; a drain or rollout of one role does
  not touch the others; the login secrets live only in `api` pods (R-HA-030); and a further split of a role (R-HA-062) is a
  change of values, not of code.

Acceptance criteria:

- With default values, `kubectl get deploy -l app.kubernetes.io/instance=trove` lists `trove-api`, `trove-packages`
  and `trove-worker`, and `GET /api/v1/system/replicas` shows roles `["api"]`, `["packages"]` and `["worker"]`.
- A manual GC run (`POST /api/v1/system/tasks/gc/run`) sent to an `api` pod is executed on a
  `worker` pod (the worker's instance ID appears in `job.locked_by` of the gc job), and the
  instance ID of no `api` or `packages` pod ever appears in `job.locked_by`.
- `helm template … --set worker.replicaCount=0` fails and names `worker.replicaCount`; `--set packages.replicaCount=0`
  fails and names `packages.replicaCount`.
- For every value set in `ci/`, the rendered containers carry the argument `--role api`, `--role packages` or
  `--role worker`, `--role web` only where the value set enables the bridge, and never `all`.
- The chart test of each role (`test/chart`): a Deployment, a Service where the role serves clients, a PodDisruptionBudget,
  a HorizontalPodAutoscaler with `autoscaling.enabled`, a NetworkPolicy with the ports of R-HA-042, and the edge rules of
  R-HA-037 that point at its Service.

### R-HA-062 · Further splits of a role into several Deployments
Priority: **MVP** · Covers: HA-015

**Status.** Built (phase 6a, WP6a.3): path pools, worker pools and the endpoint pools per role
([_units.tpl](../../deploy/helm/trove/templates/_units.tpl)). The environment form of the queues is a JSON list (Q-HA-82), what a
pool inherits is Q-HA-84, and the ports a path pool opens for further endpoints are Q-HA-83.

The owner allows several worker types and a split service (INV-2); a variant of a role by path prefix or by queue is a
pool of that role, rendered from values without code (ADR-155). Because every replica of a role can serve everything its
role serves, a pool is routing and scheduling only.

- **Path pools of `api` and `packages`.** Each entry of `api.pools` or `packages.pools` renders a Deployment
  `<fullname>-<role>-<name>` with `--role <role>`, its own Service `<fullname>-<role>-<name>` (port 8080, no session
  affinity), disruption budget, autoscaler, ingress policy and the keys of its role as overrides. The Ingress and the
  HTTPRoute route the entry's `paths` (path prefixes inside the same host, for example `/api/npm` and `/api/nuget` in
  `packages.pools`) to that Service; every other path of the role stays on `<fullname>-<role>`. A pool's paths MUST lie
  inside the families of its role (R-HA-037's prefix table: below `/api` but not `/api/v1`, or `/files`, `/v2`,
  `/.well-known` for `packages`; `/ui`, `/auth` or below `/api/v1` for `api`), MUST NOT equal one of the role's own prefixes
  of R-HA-037, and two pools MUST NOT list the same prefix. Use: a surge of npm traffic cannot starve NuGet clients; a heavy
  part of the management API on pods of its own.
- **Worker pools.** Each entry of `worker.pools` renders a Deployment `<fullname>-worker-<name>` with `--role worker` and
  `TROVE_JOBS_QUEUES` set to the entry's `queues` as one JSON list (`["vuln"]`, the loader's form for a list-valued variable,
  [architecture.md](architecture.md) Q-ARCH-304), so it claims only those queues (the queue names and the setting:
  [architecture.md](architecture.md) R-ARCH-016, Q-ARCH-296). The main `worker` Deployment gets `TROVE_JOBS_EXCLUDE_QUEUES`
  set, in the same form, to the union of every pool's queues and claims every other queue, so a queue never stays unclaimed, a queue a later
  release adds is claimed by the main Deployment, and no queue is claimed by two pools. Use: the advisory sync (`vuln`),
  the mirror's passes (`mirror`), indexing (`index`) or garbage collection (`maintenance`) on pods of their own. A pool's
  `jobs.workers.<queue>` slots are those of the configuration unless its `extraEnv` sets them.
- **Endpoint pools.** The pool of a further endpoint (`endpoints.<name>.pool`, [endpoints.md](endpoints.md) R-EP-008,
  Q-EP-3) is named `ep-<name>` and is a pool of the role that serves the endpoint's surface (Q-EP-70): a pool of `packages`
  for a surface within `packages` and `files`, of `api` for one within `api` and `ci_exchange`, and one of each for a surface
  that holds families of both roles. It follows every rule below, but is routed by its own port, Services and edge route
  instead of by path prefix, and its pods open only the listeners of their endpoints (R-EP-002). Endpoint pools are built
  since the MVP as pools of `web`; the cut makes them pools of the role of their surface.
- Every pool follows the rules of its role: at least two replicas with `highAvailability.enforce`, spreading, disruption
  budget, the drain rule of R-HA-059, its own ingress policy (R-HA-042), and the connection budget of R-HA-016, which counts
  pools like any other Deployment. A pool's pods carry `trove/pool: <name>`, which keeps its selectors apart from the main
  Deployment's (R-HA-028) and becomes the target label `pool` of its series (R-HA-045). The names of path and worker pools
  match `^[a-z][a-z0-9-]{0,19}$` and may not start with `ep-`.
- **Adding a pool without a gap (Q-HA-90).** A path pool's route is rendered in the same apply that creates its Deployment, so
  the prefix would answer `503` until the pods are ready. A path pool therefore takes the key `routed` (default `true`): with
  `routed: false` the pool's Deployment, Service, budget and policy render and its prefix stays on `<fullname>-<role>`. The
  addition takes two applies: the pool with `routed: false`, then, once its pods are ready, without the key. The same
  holds for a pool whose `paths` change. An endpoint pool has no such key: enabling `endpoints.<name>.pool` on a running
  endpoint repoints that endpoint's Service in the apply that creates the pods, so the endpoint answers `503` for the start
  time of the pool's pods; a new endpoint has no earlier traffic and is not affected.
- **Refused combinations.** The chart refuses to render: worker pools that together claim every queue (the main worker's
  exclude list would leave it none, and the binary refuses to start, [architecture.md](architecture.md) R-ARCH-016); a worker
  pool with no replica (`replicaCount: 0`, or an autoscaler whose `minReplicas` is below 1), whose queues nobody would claim
  because the main worker excludes them; and an `endpoints.default.surface` that holds no family of a role (`api`, `ci_exchange`,
  `ui` for `api`; `packages`, `files` for `packages`) while no further endpoint is served by that role, so that the role's
  Deployments would serve nothing.

```yaml
api:
  pools: []                    # default (proposed); example:
  # - name: admin              # example
  #   paths: [/api/v1/projects]   # example; prefixes inside the families of api
  #   replicaCount: 2          # example; every key of api may be overridden here
packages:
  pools: []                    # default (proposed); example:
  # - name: npm                # example
  #   paths: [/api/npm]        # example; prefixes inside the families of packages
  #   replicaCount: 4          # example; every key of packages may be overridden here
worker:
  pools: []                    # default (proposed); example:
  # - name: vuln               # example
  #   queues: [vuln]           # example; queue names from architecture.md R-ARCH-016
  #   replicaCount: 2          # example; every key of worker may be overridden here
```

Acceptance criteria: with one `packages` pool for `/api/npm` and one worker pool for the queue `vuln`, R-HA-059's drain test
passes; npm requests appear only in the pool's access logs and NuGet requests only in those of `<fullname>-packages`; jobs of
the queue `vuln` run only on the pool's pods and every other kind only on `<fullname>-worker`; `helm template` fails when two
pools list the same path prefix or the same queue, when a `packages` pool lists `/api/v1/system` or an `api` pool lists
`/files`, when a worker pool lists a queue that does not exist, and when a pool's name starts with `ep-`.

### R-HA-063 · Chart TLS wiring
Priority: **MVP** · Covers: HA-018

The chart wires TLS through trove's listeners and the platform's connections. `tls.enabled` defaults to `true`
(Q-HA-32).
- `tls.secretName` names a Secret that the chart never creates. It holds `tls.crt`, `tls.key` and `ca.crt`: the
  keys of a cert-manager TLS Secret with its CA. Rendering fails with `tls.enabled: true` and no `tls.secretName`,
  naming it, as the encryption key and the backup target do (R-HA-029). `tls.enabled: false` renders no TLS wiring,
  and H-38 applies.
- The Secret is mounted read-only at `/var/run/secrets/trove/tls/`, as a directory volume with items and without
  `subPath`, mode `0440`, with only `tls.crt` and `tls.key` projected. `ca.crt` is not mounted into the process: the
  listener never reads it, and the Secret still carries it for the edge and for monitoring. The chart sets
  `server.tls.certificates` to that one pair and, when mutual TLS is configured, `server.tls.client_ca_file` with
  `client_auth` from `tls.clientCA.auth` (R-ARCH-035). A rotated Secret reaches the pod through the kubelet's swap and
  the reloader of R-ARCH-035 applies it. The checksum annotation follows the settings and the paths, not the Secret's
  content, so a rotation does not roll the pods.
- `server.ops_tls` is explicit when `tls.clientCA` is set, or when `tls.ops.secretName` names its own pair. The
  explicit block has `inherit: false`, the same pair as the client listener, and no client CA: probes and scrapers
  present no client certificate, so mutual TLS on `:8080` never extends to `:8081`. With `tls.ops.secretName` the
  block takes that Secret's pair instead.
- A further endpoint serves the same pair unless its `tls.existingSecret` or `tls.extraSecrets` name pairs of its own,
  wildcard certificates included; each is mounted under `/var/run/secrets/trove/endpoints/<name>/<n>/`, rendered as
  that endpoint's `tls.certificates`, chosen by SNI and reloaded the same way ([endpoints.md](endpoints.md) R-EP-004,
  R-EP-008). Its Service port declares `appProtocol: https`, and its Ingress or HTTPRoute connects to it over TLS as
  above.
- Probes use `scheme: HTTPS` on the ops port (R-HA-034). The kubelet does not verify the served certificate for
  probes, so they need no CA.
- The client and ops Service ports declare `appProtocol: https`.
- With `metrics.serviceMonitor.enabled` the ServiceMonitor carries a `tlsConfig` (`ca` from `ca.crt` of the Secret of the ops
  listener, read from the ServiceMonitor's namespace, which is therefore the release namespace while `tls.enabled`; `serverName`
  `<fullname>-ops.<namespace>.svc`, a SAN the operator's certificate must carry).
- The Ingress connects to the backend over TLS. `ingress.backendTLS.controller` selects the controller-specific
  settings: `haproxy` (the default, the primary target: `haproxy-ingress.github.io/secure-backends`,
  `secure-verify-ca-secret` with the CA of `ingress.backendTLS.caSecret`, `secure-sni` set to `<fullname>.<namespace>.svc`,
  and `secure-crt-secret` for `clientCertSecret`), `traefik` (the `serversscheme` and `serverstransport` of the Service),
  `ingress-nginx` (`backend-protocol`, `proxy-ssl-*`) or `manual` (the settings are the operator's, in
  `ingress.annotations`). `ingress.backendTLS.verify` defaults to `true`: the controller verifies the trove certificate,
  and the render fails when `caSecret` is missing for `haproxy` and `ingress-nginx`, or `serversTransport` for
  `traefik`. `verify: false` encrypts the connection without verifying it, and the choice is explicit.
- A Gateway route uses a `BackendTLSPolicy`, rendered only with `tls.backendTLSPolicy.enabled`, which takes its CA from
  a ConfigMap (`caConfigMapName`) because the Gateway API takes no Secret there; `tls.clientCA`, `tls.ops` and
  `tls.backendTLSPolicy` need `tls.enabled: true`.
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
  after `kubectl rollout status` of `deploy/trove-api`, `deploy/trove-packages` and `deploy/trove-worker` returns, only 0.4.0 is
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
   the remaining window. A *silent* holder, whose process stopped while its kernel keeps the
   connection (a paused container, a stopped process), answers every keepalive probe, so the
   keepalive never ends its session. The lease sessions therefore also carry
   `idle_session_timeout` at the same bound (`idle + interval × count` of
   `database.server_keepalive`, R-HA-015): the server ends a session that sent no statement for that
   long, and the lock with it. A live holder sends `SELECT 1` every `database.lease_check_interval`
   (3), and the configuration refuses a bound below three of those (Q-HA-127).
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
- A holder falls silent with its connection open (its traffic relayed through a TCP relay that stops
  moving bytes and closing sockets): the server ends its session within `idle + interval × count` of
  `database.server_keepalive`, another worker holds the lease within that bound plus
  `cluster.lease_retry_interval`, and at no instant do two sessions hold it
  (`TestLeases_R_HA_006_FrozenHolder`).
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
| Processes stopped with their connections open (a paused node or container, `SIGSTOP`) | Their kernel keeps answering TCP keepalives, so the keepalive sees nothing; the node controller marks a paused node not ready | Job leases expire; the server ends a lock-holding session after `idle + interval × count` of `database.server_keepalive` without a statement (`idle_session_timeout` of the lease sessions, R-HA-006, Q-HA-127), which releases its advisory locks; when the processes run again their lease sessions are gone, so they commit nothing under a lease | As for node loss while the node is marked not ready | Automatic; the pods come back with the node, or are replaced after `nodeFailureTolerationSeconds` |
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

- An upload that sends `Expect: 100-continue` and is rejected because the circuit is open MUST be
  rejected *before* the body is read, after authentication and authorization (`503` with `Retry-After`), so clients that send it
  (`dotnet nuget push`, `curl -T`) do not transfer the body at all. A write without `Expect` is read
  and a deduplicated upload succeeds ([Q-STOR-149](storage.md#q-stor-149--an-upload-that-waits-for-100-continue-is-refused-before-its-body-while-the-circuit-is-open)).
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
  `curl -T big.bin https://trove.example.com/files/web/generic-local/x.bin -H 'Expect: 100-continue'`
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
  persist their cursor. A handler still running 11 s before the deadline is cancelled and its job
  lease released explicitly (`state = 'queued'`, `attempt` unchanged): the release write has the
  whole settle deadline of the worker, 15 s with its retries, before the process stops waiting for
  it ([architecture.md](architecture.md) R-ARCH-024 step 4, Q-ARCH-329), so a release during a
  PostgreSQL switchover is not left to the lease's expiry and the reaper. The advisory-lock
  sessions are closed at the end.
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

- `kubectl rollout restart deploy/trove-packages` (before phase 6a: `deploy/trove-web`) during a loop of `curl -T 500mb.bin …/files/web/generic-local/<n>.bin`
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
   count on it reacting during a drain. The defaults (three `packages`, two `api`, two `worker`, Q-HA-16, Q-HA-75)
   satisfy the rule for a peak that two `packages` replicas and one `api` replica carry.
2. **One pod per role at a time.** Each role's PDB allows one unavailable pod (`maxUnavailable: 1`,
   R-HA-032). `kubectl drain` retries a refused eviction, so a node that hosts several pods of one
   role is drained one pod after the other, each after the previous replacement is ready.
3. **Replacements can schedule.** Every role and pool spreads over nodes with `DoNotSchedule` and
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
   each edge of R-HA-039 and fails when the delay is shorter. It measures it from the log of every
   evicted `api` and `packages` pod, under the load of the test: the time from the line "shutting down"
   (the readiness flip) to the arrival of the last request of a client listener that came after it,
   both stamped by the same pod's clock ([Q-HA-110](#q-ha-110--how-the-drain-test-measures-the-edges-endpoint-removal-time)).
   The delay must exceed the longest time measured by at least 1 s of silence
   ([Q-HA-113](#q-ha-113--what-the-drain-test-fails-on)); the test fails with both numbers otherwise. The default of 5 s
   ([Q-HA-111](#q-ha-111--the-default-of-healthshutdown_drain_delay)) is almost three times the longest time measured on the
   dev profile's HAProxy Ingress on the CI runners, and the drain test is how an installation finds out that its own edge needs
   more. The dev profile runs 15 s ([Q-HA-112](#q-ha-112--whether-the-dev-profile-keeps-its-drain-delay-of-15-s)).
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

**Acceptance test.** In a green full run of Test and Release on the commit that closes the phase, on the kind profile of R-HA-051 with three worker nodes; `packages`
runs 3 replicas, the chart default, set with `--set packages.replicaCount=3` on top of
`values-dev.yaml`, `api` 2 and `worker` 2 (before phase 6a: `web` 3 and `worker` 2). PostgreSQL and Valkey run on nodes other than the drained one,
except where a variant says otherwise. The load starts 5 minutes before the drain and runs until
10 minutes after `kubectl drain` returns:

| Load | Rate |
|---|---|
| `npm ci --cache "$(mktemp -d)"` of a fixture with 400 locked dependencies through the virtual repository `npm-all` (remote and local members, cache warm) | 8 parallel loops |
| `dotnet restore --no-cache --force` of a fixture with 60 packages through `nuget-all` | 4 parallel loops |
| `npm publish` of a new version to `npm-local`; `dotnet nuget push` of a new version to `nuget-local` | one each every 10 s |
| k6 `constant-arrival-rate` against the edge: 50 % npm packument GETs, 30 % npm tarball GETs of 1–5 MB, 20 % NuGet registration and flat-container GETs | 150 requests/s |
| Long-transfer probes: `curl --limit-rate 2M --retry 5 --retry-all-errors -C - -o big.bin …/files/web/generic-local/1gib.bin` (about 512 s, longer than the `web` grace period) | 2 parallel loops |

The drain is `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data --timeout=20m` of a
node that hosts at least one `api`, one `packages` and one `worker` pod. Pass conditions:

- every `npm` and `dotnet` command exits `0` with the clients' default retries;
- k6 records no response other than `2xx` or `304` and no connection error (k6 does not retry);
- the p99 latency of k6 requests from the first eviction until 2 minutes after the drain is at most
  twice the p99 of the warm-up;
- every acknowledged publish installs afterwards;
- a long-transfer probe is cut at most once per evicted `packages` pod that served it, completes through
  resume, and its SHA-256 matches; a cut on a pod that was not evicted fails the test;
- the kubelet events show no `SIGKILL` of a trove container;
- every job that ran on an evicted `worker` pod finishes on another one with `attempt` unchanged
  (R-HA-011);
- no trove pod stays `Pending` for longer than 60 s, and `kubectl drain` returns within 10 minutes.

Variants, with the same load and pass conditions:

| Variant | The drained node also hosts | Additional pass condition | Runs |
|---|---|---|---|
| A | nothing else | — | every night and on dispatch, in HA and Performance (2 minutes of load before and 3 after; every change to `main` until the owner's decision of 2026-11-01, [Q-HA-67](#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)) |
| B | the Valkey primary and one Sentinel | `trove_valkey_primary_changes_total` rises on every replica; no replica turns unready | a green full run of Test and Release on the commit that closes the phase |
| C | the PostgreSQL primary | CloudNativePG switches over before the eviction | a green full run of Test and Release on the commit that closes the phase |
| D | an instance of the transaction-mode pooler (pooler profile, R-HA-058) | — | a green full run of Test and Release on the commit that closes the phase |
| E | a running backup pod (R-HA-060) | the backup Job completes, and the eviction is not counted against `backoffLimit` | a green full run of Test and Release on the commit that closes the phase |

**A drain per role** (phase 6a). For each of `api`, `packages` and `worker` (and a worker pool of the queue `vuln` when the
run configures one), the pods of that role alone are evicted one after the other through the eviction API, each after the
previous replacement is ready. What it adds to a node drain, which takes one pod per role, is that every replica of a role is
replaced: per-replica state such as the rendition cache ([architecture.md](architecture.md) R-ARCH-012) is cold on every
`packages` replica afterwards. Its load ([Q-HA-94](#q-ha-94--the-load-of-the-drain-per-role)) is the npm part of the table
above, as the npm tests run it
([Q-HA-65](#q-ha-65--the-npm-part-of-r-ha-059s-load-before-nuget-exists-and-how-its-commands-are-counted)), beside a load of
its own:

| Load | Rate |
|---|---|
| `npm ci --cache "$(mktemp -d)"` of a fixture with at least 400 locked dependencies through `npm-all` (remote and local members, cache warm) | 8 parallel loops |
| `npm publish` of a new version to `npm-local` | one every 10 s |
| k6 `constant-arrival-rate` against the edge: the npm package document of every package the fixture locks, in the form `npm install` asks for, and npm tarballs of 1–5 MB, five to three | 120 requests/s |
| k6 against the edge: `GET /api/v1/system/ping` and `GET /api/v1/system/replicas` | 100 requests/s |
| k6 against the edge: an upload of a file to `generic-local`, its download and a comparison of its SHA-256 | 4 iterations/s |
| A management API call with a token (`GET /api/v1/auth/whoami`) | one every 250 ms |
| A change written and a job enqueued by the test's driver; before the first eviction of the main `worker`, four probe jobs that run for 400 s | one each per second |

`npm ci` of a lockfile asks for tarballs only ([formats/npm.md](formats/npm.md) R-NPM-046), so the documents are k6's. The load
starts 3.5 minutes before the first eviction: its first 90 s fill the replicas' caches with k6's documents, and the warm-up is the
2 minutes after them. It runs until 3 minutes after the last replacement is ready. Pass conditions:

- every `npm` command exits `0` with npm's default retries;
- k6 records no response other than `2xx` or `304` for the npm requests and `200` for the ping and replica requests, and no
  connection error;
- the p99 latency of k6's npm requests from the first eviction until 2 minutes after the last replacement is ready is at most
  twice the p99 of the warm-up, and so is the p99 of k6's document requests on their own against the warm-up's documents (the
  tarballs of 1–5 MB set the p99 of the mix);
- every acknowledged publish installs afterwards;
- no transfer and no management call fails, and no download returns other bytes than were uploaded;
- the pods of the other roles keep their names and restart counts while a role is drained;
- the kubelet events show no `SIGKILL` of a trove container, and no trove pod stays `Pending` for longer than 60 s;
- every probe job finishes with `attempt` unchanged, and one that ran on an evicted `worker` pod resumes from its checkpoint on
  another one (R-HA-011).

Not part of it (Q-HA-94): the `dotnet` loops, `dotnet nuget push` and the NuGet share of k6, which join with the drain under
NuGet load; the long-transfer probes; a loop of a browser journey; and a full advisory sync per pod of the pool `vuln`, whose resume
from its checkpoint after `SIGTERM` the node drain of `TestVulnLong_R_VULN_004_FullSyncDrainedMidRun` covers.

The leg that runs it is the set `drain-roles` of the nightly workflow, on dispatch and locally ([Q-HA-88](#q-ha-88--which-legs-run-the-drain-per-role));
`TestE14Drain` (variant A, one node) stays in the set `push`. The set runs on the scatter profile of the npm tests (three `api`, three
`packages`, two `worker` and two pods of the pool `vuln`, an edge that balances every request on its own without a kept-alive
connection to the pods), with the npm client pods and the counting test upstream.

On the local profile of R-HA-051, whose three worker nodes each host a Sentinel and most a Valkey pod or a PostgreSQL instance,
variant A drains a node that hosts neither the PostgreSQL primary nor the Valkey primary; a Sentinel, a Valkey replica or the
standby on it leaves by rule 5, and a primary that is in the way is moved before the load starts
([Q-HA-68](#q-ha-68--the-node-variant-a-drains-on-the-three-node-local-profile)).

## PostgreSQL

### R-HA-013 · Supported PostgreSQL versions and server settings
Priority: **MVP** · Covers: HA-058, HA-003

**Versions.** trove MUST support PostgreSQL 16, 17 and 18 and MUST refuse to start against an
older server (`server_version_num < 160000`) with a message naming the version found. The floor is
16 (Q-HA-2). Support dates verified 2026-10-28 on the
[PostgreSQL versioning page](https://www.postgresql.org/support/versioning/): 14 ends 2026-11-12,
15 on 2027-11-11, 16 on 2028-11-09, 17 on 2029-11-08, 18 on 2030-11-14.

- **Version under test (Q-ARCH-219).** The integration tier of CI and the dev profile run PostgreSQL
  18.6 (`tools/versions.env`, [compose.yaml](../../test/integration/compose.yaml)). No CI run starts a
  real server of 16 or 17, so the floor and the line after it are not verified against a server, and
  the refusal below `server_version_num` 160000 (`internal/meta/postgres/preflight.go`) has no test in the
  repository. The CI matrix over every supported version is not built yet: until it is, support of 16 and
  17 is what this page states, not what CI has run. Next step: a 16 leg of the integration tier and a test
  of the refusal, decided with the owner (Q-ARCH-219 option (c)).
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
- Text columns that carry indexed keys (project and repository keys, package names, paths) are declared with
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
  - After migration 5 has applied (it revokes `INSERT`, `UPDATE`, `DELETE` and `TRUNCATE` on `schema_migration` and
    `schema_state` from the runtime role), that the runtime login cannot write either table. A login that can is refused
    at start, and the error names the `REVOKE` that fixes it. `system_state` stays writable: the runtime sets the deletion
    pause and later keys of its own. A superuser and the owner of the tables are skipped, and so is a database that has
    not reached migration 5 yet.
  - Implemented in M0: the server version, the encoding, the superuser refusal, the bookkeeping check of the item above and,
    behind a transaction-mode pooler, the session settings that the role's defaults must carry. Not implemented in M0: the
    audit-object and backup-role checks, because the tables they name do not exist yet
    ([security/privilege-footprint.md](../security/privilege-footprint.md), H-75).
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
| `idle_session_timeout` (lease sessions only) | `idle + interval × count` of `database.server_keepalive`, in the whole seconds above (120 s with the defaults) |

- The server-side keepalive parameters are user-settable (context `user`), so they need no
  privilege and no server configuration. They make the server end the session of a vanished
  replica, and release its advisory locks (R-HA-006), within `idle + interval × count` (about
  2 minutes with the defaults), the bound R-HA-013 recommends for the server setting. They are sent
  on every session that connects to the session host, which includes the lease pool that holds
  session-level locks.
- `idle_session_timeout` (context `user` as well) ends a lease session that sent no statement for
  the same bound: a holder whose process stopped while its kernel keeps answering the keepalive
  probes (R-HA-006 item 4, Q-HA-127). Only the lease pool sends it; a lease holder pings every
  `database.lease_check_interval`, and a keepalive bound below three of those is refused at
  startup naming `database.server_keepalive`.

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
| Main pool (class `main`) | `database.pool.max_conns` | 20 | `api`, `packages`, `worker` (and `web` in the bridge of R-HA-064) | `database.host`, the pooler with R-HA-058 |
| Lease pool (class `lease`, R-HA-006) | `database.lease_pool_size` | 4 | `worker` | session host |

There is no listener session and no fetch-collapse lock pool any more: invalidations travel through
the change log on the main pool (R-HA-004), and fetch collapse takes a Valkey lock since INV-11
changed ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026,
[architecture.md](architecture.md) Q-ARCH-14). There is no coordination session either, so an `api` or `packages`
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
serving_max  = Σ over the Deployments of api and packages, their path pools, the endpoint pools
               and, with web.enabled (R-HA-064), the web Deployment and its endpoint pools,
               of (autoscaling.enabled ? maxReplicas : replicaCount) + rollingUpdate.maxSurge
worker_max   = Σ over the worker Deployment and its pools, the same terms
session      = worker_max × database.lease_pool_size
pooled       = database.pooler.mode == transaction
                 ? database.pooler.server_connections           (declared, R-HA-058: request and control pools)
                 : (serving_max + worker_max) × database.pool.max_conns
budget       = pooled + session
               + 2                                              (migration Job)
               + backup_conns                                   (backup Job: 2 + backup.database.parallel, once per cluster)
               + database.preflight.reserved_connections        (operators, tools, monitoring)
               + superuser_reserved_connections                 (server setting, default 3)
max_connections ≥ budget
```

Examples:

- Defaults of the cut (Q-HA-75), direct connections, `api` 2, `packages` 3 and `worker` 2 replicas with `maxSurge: 1`
  (`serving_max` 3 + 4 = 7, `worker_max` 3): 10 × 20 + 3 × 4 + 2 + 4 + 10 + 3 = **231**, with no `serving_max × 1`
  term, because `api` and `packages` replicas hold no session connection. Before the cut, with `web` 3 and `worker` 2, it was
  171. The PostgreSQL default of 100 is too small, so the chart's NOTES print the computed budget.
- During the first two steps of the upgrade across the cut (R-HA-064), the `web` Deployment runs beside `api` and `packages`:
  with the defaults `serving_max` is 4 + 3 + 4 = 11, and the budget **311**. The migration Job of that step checks it, so a
  server sized only for the target fails the step before anything changes.
- Large profile (R-HA-050), `packages` up to 16, `api` up to 4 and `worker` up to 6, `max_conns: 30`: without a pooler
  (17 + 5 + 7) × 30 + 7 × 4 + 19 = **917**; with a transaction-mode pooler declaring 100 server connections,
  100 + 28 + 19 = **147**.

**Growth with the replica count (INV-12).** Without a pooler, every added replica adds its whole
main pool. With the pooler, the main pools add nothing at the server: an added `api` or `packages` replica adds
no server connection at all, and an added `worker` replica adds its lease pool. The server-side
count then depends only on the number of `worker` replicas, which follow job load and stay small
(Q-HA-6).

- The chart passes `serving_max` and `worker_max` to the migration Job
  (`TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS`, `TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS`; Q-HA-77). The
  key `database.preflight.expected_serving_replicas` replaces `expected_web_replicas`, which stays a deprecated alias with a
  start-up warning (R-ARCH-025); the chart renders only the new name.
  The Job compares the budget with `SHOW max_connections`. With
  `database.preflight.connection_budget: enforce` (default) a shortfall fails the Job, and with it
  `helm upgrade`, before the larger deployment exists. With `warn` it logs and continues.
- trove cannot read the pooler's limits. With the pooler, `NOTES.txt` prints the client connections
  each pooler must accept, `(serving_max + worker_max) × database.pool.max_conns` for the request pool
  and `serving_max + worker_max` for the control pool, next to the budget, and R-HA-058 lists the
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
  fails in the migration Job with a message that names 231 required and 100 available.
- Under the reference load test (R-HA-050, medium profile),
  `max(trove_db_pool_connections{state="acquired"})` stays below `max_conns`, and
  `SELECT count(*) FROM pg_stat_activity WHERE usename LIKE 'trove%'` never exceeds the computed
  budget.
- With the pooler profile (R-HA-058), scaling `packages` from 2 to 12 replicas leaves
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

Acceptance criteria (pooler profile of R-HA-051, a green full run of Test and Release on the commit that closes the phase):

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
- `verify-ca` requires `valkey.tls.ca_file`: the configuration check refuses it without, and the client
  refuses to dial with it. Without a CA file the system roots would verify the chain and no host name would
  be checked, so any publicly issued certificate would pass.
- Sentinel mode warns once at start, and the warnings do not stop the replica: `valkey.sentinel.password_file`
  is empty (anyone who reaches a Sentinel can name another primary), or `valkey.tls.mode` is not `disable` and
  `valkey.tls.ca_file` is empty (the primary is verified against the system roots). No configuration switch makes
  either an error yet ([Q-HA-33](#q-ha-33--sentinel-trust-settings-warn-or-refuse-at-start)); the trust
  consequences are in [security/transport-encryption.md](../security/transport-encryption.md), H-64.

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

**Before the first stable release the compatibility promises of this requirement do not apply**
(owner, 2026-11-06, `CLAUDE.md`, [Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)). N and N−1 need not run side by side against
one schema, and an upgrade may need a new database; no rollback is supported. The rules of the
migration files are switched off, ready to be switched on at the first stable release
([the switches](../developer/releasing.md#switched-off-until-the-first-stable-release)): with `EnforceMigrationPhases = false` in
`internal/meta/postgres/steps.go` every file header is optional, a step's phase does not restrict
its statements, and `requiredSchema` is the highest migration number the binary embeds; with the job
`compat` switched off, a migration file may be edited. The runner, the checksum check, the gate and
the contract guard of a step that declares itself a contract step stay on, as described below.

**Status.** `GET /api/v1/system/migrations` is built (`getMigrationStatus`, finding F-59 closed on 2026-11-06): the `api` role
answers it with the report of `trove migrate status`, from the same code on the replica's pool, and the background migrations of
[Q-HA-114](#q-ha-114--which-background-migrations-the-migration-report-lists-before-r-ha-022s-table-exists). Tested by
`TestGetMigrationStatus` ([migrations_api_test.go](../../internal/api/v1/migrations_api_test.go)), `TestMigrationStatus`
([migrations_test.go](../../test/integration/api/migrations_test.go), an `api` replica against PostgreSQL, the answer against the
document, `settings:read`) and `TestR_HA_021_UpgradePreparation`
([upgrade_preparation_test.go](../../test/integration/postgres/upgrade_preparation_test.go)). The runner, the headers and the
guard are tested by the tests named R-HA-018 in [migrate_test.go](../../test/integration/postgres/migrate_test.go),
[steps_test.go](../../internal/meta/postgres/steps_test.go) and [gate_test.go](../../internal/meta/postgres/gate_test.go).

From the first stable release on, release N and release N−1 run side by side against one schema
during every rolling upgrade (INV-3), and every migration step is classified by these rules:

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
  | `-- trove:phase expand` or `-- trove:phase contract` | The step's class (table above); every file has exactly one. While the rules are switched off, at most one, and a file without it is an expand step |
  | `-- trove:no-transaction` | The step cannot run in a transaction block; the file holds exactly one statement |
  | `-- trove:requires-app >= <version>` | Contract steps only, and required there: the oldest release allowed to serve once the step is applied |
  | `-- trove:requires-background <name>` | Contract steps only, optional, once per migration: a background data migration (R-HA-022) that must have `succeeded` before the guard lets the step run ([Q-HA-115](#q-ha-115--how-a-contract-step-declares-the-background-migration-it-depends-on-and-what-the-guard-does-with-it)) |

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

- Each binary embeds `requiredSchema`, the number of its newest expand step. While the rules are
  switched off ([Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)), it is the number of its newest migration, a contract step included.
- Each contract step declares `requires-app` in its header: the oldest release allowed to serve
  once the step is applied. The release before it is the step's `lastUsedBy`, the newest release
  whose code still reads or writes the object the step removes. `requires-app` is the declared
  value; `lastUsedBy` is only a name for the release before it.
- The database records `compat_floor` = the highest `requires-app` of all applied contract steps,
  or `0.0.0` while none is applied. No version arithmetic is involved: the floor is a declared
  version, compared as a semantic version.
- A binary of version V can serve iff `applied ≥ requiredSchema` and `compat_floor ≤ V`. The
  window has no upper bound on the schema number. From the first stable release on, newer expand
  steps do not make an older binary incompatible, which keeps N−1 ready while N's expand steps are
  applied; before it nothing promises that an older binary works against a newer schema. The
  `schema` readiness check of R-OPS-001 MUST implement exactly this rule: below `requiredSchema`
  the replica waits not ready (R-HA-008), and with `compat_floor` above V it exits with
  `schema requires trove >= <compat_floor>` (R-ARCH-026).

**Contract guard.** Before applying a contract step, `trove migrate` reads the live replicas
(R-HA-003). If any of them runs a version below the step's `requires-app` (that is, ≤ its
`lastUsedBy`), `trove migrate` MUST exit with status `3` without applying anything, which fails the
Job. The error lists those replicas and the two ways forward: upgrade one minor release at a time,
or stop all replicas and upgrade with downtime (R-HA-021). There is no override flag. The downtime
path scales the replicas to zero, so the registry lists none and the guard passes; an override
while old replicas are live would let the step break them (R-ARCH-026, Q-ARCH-3). The guard also
refuses a contract step, with status `3` and without applying anything, while a background migration
it names in `-- trove:requires-background` has not `succeeded`; the error names the migration and its
state (R-HA-021 step 4, R-HA-022, [Q-HA-115](#q-ha-115--how-a-contract-step-declares-the-background-migration-it-depends-on-and-what-the-guard-does-with-it)).
A step that names a migration its binary does not declare fails the run with status `1`.

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
- From the first stable release on, a migration file is immutable once it is on `main`
  (R-ARCH-026, Q-ARCH-283), and a defect in a step is fixed forward with a new step of the class
  the change needs: expand for an addition, contract one minor later for a removal (the table
  above). Until then the check of this rule is switched off ([Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)): a file may be edited,
  and a database that applied the earlier version fails the checksum check above and is recreated.
- There are no down migrations. From the first stable release on, rolling back one release is
  supported without a database restore (R-HA-019); before it no rollback is supported.

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

- Each `pending` entry carries `version`, `name`, `class`, `requiresApp` (`null` for an expand step) and
  `requiresBackground` (empty unless a contract step names background migrations): what the guard checks
  before the step.
- `background` lists the background data migrations the answering replica's binary declares, with
  `name`, `release`, `state`, `processed`, `estimatedTotal` and `startedAt` (`null` while unknown); until
  R-HA-022's table exists the state is read from each migration's completion record and newest job
  ([Q-HA-114](#q-ha-114--which-background-migrations-the-migration-report-lists-before-r-ha-022s-table-exists)).
- The report is the answering replica's, for its own binary: during a rolling upgrade both releases
  answer, each with its `binary`. The `api` role serves it ([ADR-155](../decisions.md#adr-155--three-roles-api-packages-and-worker-variants-are-pools)).
- `trove migrate status` prints the same report from the same code (`postgres.Report`), plus `servable`
  and `verdict`, the gate's answer for its binary; a replica that answers the endpoint has passed the gate,
  so the endpoint leaves them out.

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
  file without `-- trove:requires-app` (R-ARCH-026). While the rules are switched off
  ([Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)), the unit tests hold both modes: `TestParseSteps` with the rules on,
  `TestParseStepsRelaxed` with them off, where a file without any header and those statements in any
  step pass.
- From the first stable release on, an integration test: the previous release's binary passes its
  full API suite against a schema with the current release's expand steps applied.
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
- **Rollback.** Before the first stable release no rollback is supported ([Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)): N's
  Job may have applied steps that N−1 does not work against, and the way back is the backup taken
  before the upgrade (R-HA-021) or a new database. From the first stable release on, `helm rollback`
  to release N−1 after an upgrade to N is supported. N's Job applied only expand steps that N−1
  tolerates, and contract steps whose objects N−1 no longer uses, which declare `requires-app` N−1
  or lower (R-HA-021). `compat_floor` is therefore ≤ N−1. Rolling back further than one release
  requires a database restore.

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

**Before the first stable release this requirement is not a promise** (owner, 2026-11-06,
`CLAUDE.md`, [Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)): N and N−1 need not serve side by side, and an upgrade may need
downtime or a new database. The chart still rolls the Deployments as described below; the rules of
the mixed-version window are promised from the first stable release on, and the status names the
test that exists today. The nightly run skips its set `upgrade` until then; a dispatch with
`sets=upgrade` runs it ([the switches](../developer/releasing.md#switched-off-until-the-first-stable-release)).

**Status.** The rolling upgrade and its rollback are tested by `TestUpgrade_R_HA_020_RollingFromThePreviousRelease`
([upgrade_test.go](../../test/e2e/upgrade_test.go), the set `upgrade`): the latest release before the working tree is installed
from the published chart and image and upgraded with `helm upgrade --wait` under `npm ci`, `npm publish`, `dotnet restore` and
browser loops ([Q-HA-95](#q-ha-95--how-the-upgrade-tests-get-and-install-the-older-release),
[Q-HA-97](#q-ha-97--the-loops-of-the-upgrade-tests)). The verification reads `GET /api/v1/system/migrations` from a replica of the
release under test, waits for the background migrations to succeed and checks it against `trove migrate status`
([Q-HA-116](#q-ha-116--the-upgrade-tests-read-get-apiv1systemmigrations-from-the-release-under-test), which supersedes Q-HA-96); the
cut test runs the same check after its step 2. Both upgrade tests passed with that read in the dispatched nightly run 42230022283. The gate on the cluster minimum version
(the second acceptance criterion) has no feature to gate yet and is not tested. The UI's hashed files across the window are
tested by `TestUpgrade_R_HA_020_AcrossTwoBundles` ([upgrade_bundles_test.go](../../test/e2e/upgrade_bundles_test.go), the set
`upgrade`): the working tree with its second UI bundle is upgraded to the image under test under the same loops, strict
([Q-HA-124](#q-ha-124--how-the-upgrade-tests-prove-an-upgrade-between-two-bundles)); and by `TestR_HA_020_SharedUIAssets` and
`TestR_HA_020_SharedUIAssetsWithoutTheObjectStore` ([test/integration/uiassets](../../test/integration/uiassets/uiassets_test.go)):
two replicas with different builds on one database and object store, each file of one's `index.html` loaded from the other, an
unknown name `404`, no unhashed path from the store, the renewal and the retention, and `503` for a file of the other build while
the object store is away.

- Every Deployment uses `RollingUpdate` with `maxSurge: 1` and `maxUnavailable: 0`, plus
  `minReadySeconds: 10`. Serving capacity never drops below the replica count during an upgrade,
  so the replica counts of the drain rule (R-HA-059) suffice for a zero-downtime upgrade. The
  catalog's recommendation of three nodes comes from upgrades that take a node out of service.
- **Order.** Helm runs the migration hook first (R-HA-019) and then applies the Deployments of every role and pool
  together; each rolls on its own.
  No order between them is needed, because every role tolerates the other roles in release N−1
  (below). The upgrade from a release before the cut into roles takes the three steps of R-HA-064 instead, because a
  Deployment cannot be renamed. The backup CronJob takes the new image for its next run; a run already in progress
  finishes on the old one, which the expand-only window allows.
- During the mixed-version window, release N and N−1 serve the same clients alternately, and N−1
  workers may run jobs that N web replicas enqueued. Therefore:
  - Anything a client carries between requests MUST be understood by both releases: pagination
    cursors, `ETag` values, session cookies, CSRF tokens, upload session identifiers.
  - The UI's hashed files are named after their content (R-ARCH-004, R-UI-001), so a replica of release N lacks those of N−1
    and the reverse. Every replica that serves the UI publishes the hashed files of its build to the shared asset store before
    it turns ready, renews them while it runs, and answers a hashed file its own build lacks from the store
    ([Q-HA-120](#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release) to
    [Q-HA-123](#q-ha-123--when-a-replica-publishes-its-build)): a page whose `index.html` came from either release loads on the
    replicas of both, and so do the lazy chunks of a tab that stays open after the upgrade, for 7 days after the last replica of
    its release stopped. This holds between two releases that both have the store; an upgrade from a release before it can
    still meet a `404` for a hashed file once ([Q-HA-125](#q-ha-125--the-rolling-upgrade-from-a-release-without-the-store-stays-strict)),
    and the cut from v0.16.0 counts it as a finding ([Q-HA-99](#q-ha-99--the-uis-hashed-assets-across-an-upgrade)).
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
  - `kubectl rollout status` of `deploy/trove-api`, `deploy/trove-packages` and `deploy/trove-worker` (and of each pool);
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

**Before the first stable release the supported paths of the table do not apply** (owner,
2026-11-06, `CLAUDE.md`, [Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)): any upgrade may need downtime or a new database, and no
rollback is supported. The preparation below still applies; the backup it takes is the way back.

**Status.** Step 4 of the preparation is built: `GET /api/v1/system/migrations` lists the background migrations with their states
([Q-HA-114](#q-ha-114--which-background-migrations-the-migration-report-lists-before-r-ha-022s-table-exists)), and the contract guard
refuses a contract step that names one in `-- trove:requires-background` until it has `succeeded`
([Q-HA-115](#q-ha-115--how-a-contract-step-declares-the-background-migration-it-depends-on-and-what-the-guard-does-with-it)).
`TestR_HA_021_UpgradePreparation` ([upgrade_preparation_test.go](../../test/integration/postgres/upgrade_preparation_test.go)) pins
against PostgreSQL step 4 (for each state of the records the report and the guard agree) and the runner's part of both acceptance
criteria: the refusal with status `3` and R-HA-018's message while a replica of an older release is live, the success without an
override once the replicas' heartbeats are stale, and the wait for a backup's database phase and the failure that names the backup
after `migrate.advisory_lock_wait`. Not tested at the level of the criteria: a real `helm upgrade` across a contract step with
`kubectl scale` (no pair of published releases has a contract step between them), and the old pods serving through a refusal; the
chart test checks that exit status `3` fails the Job without a retry (R-HA-019), and `TestE04MigrationFailure` that a Job that
fails (on its credential) leaves the replicas serving.

| Upgrade | Supported path |
|---|---|
| Patch (x.y.z → x.y.z+1) | Rolling, zero downtime; patch releases contain no contract steps |
| Next minor (x.y → x.y+1) | Rolling, zero downtime (R-HA-020) |
| From a release before the cut into roles to the first release with `api` and `packages` | Three rolling steps with zero downtime: `web.enabled: true`, then `web.routed: false`, then `web.enabled: false` (R-HA-064); the release notes name them |
| Skipping minors (x.y → x.y+2 or later) | Stepwise through each minor with zero downtime, or in one step with downtime: scale every role to zero, wait `system.heartbeat_stale_after`, then `helm upgrade`. The contract guard enforces this (R-HA-018, Q-HA-5) |
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
  the message of R-HA-018. After `kubectl scale deploy/trove-api deploy/trove-packages deploy/trove-worker --replicas=0`,
  a 30 s wait and `helm upgrade`, it succeeds without any override.
- A `helm upgrade` started while a backup's database phase runs waits for it; with the phase
  artificially held longer than `migrate.advisory_lock_wait`, the migration Job fails with a message
  that names the backup, and the old pods keep serving.

### R-HA-064 · Upgrade across the cut into roles
Priority: **MVP** · Covers: HA-069, OPS-073

**Before the first stable release its promise of no downtime does not apply** (owner, 2026-11-06,
`CLAUDE.md`, [Q-ARCH-340](architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release)): it rests on the mixed-version operation of R-HA-020.

**Status.** Built (phase 6a, WP6a.3; the third step since F-64, Q-HA-130): the bridge, its edge, step 2's value `web.routed`
and the guard ([_validate_roles.tpl](../../deploy/helm/trove/templates/_validate_roles.tpl)). The guard reads the cluster
(`trove.bridgeFacts`) and decides on what it read (`trove.bridgeGuard`); the chart test `TestRolesBridge` checks the decision
with given facts (Q-HA-132), and the reads are verified by the upgrade e2e test (Q-HA-86): `TestUpgrade_R_HA_064_AcrossTheCut`
([upgrade_test.go](../../test/e2e/upgrade_test.go), the set `upgrade`, [Q-HA-95](#q-ha-95--how-the-upgrade-tests-get-and-install-the-older-release))
installs v0.16.0, checks both refusals and that `helm template` renders, and runs the three steps under client and browser
loops, with the edge's access lines read for answers without a backend (Q-HA-131). The chart test `TestRolesBridge` checks
that each step changes either the edge or the backends. While the bridge runs, its `web` pods hold the secrets of both
roles ([H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut)).

The releases before the cut run `<fullname>-web` behind the Service `<fullname>` and route every host whole to it. A
Deployment cannot be renamed, and Helm applies a release in one pass: it creates the new Deployments, repoints the edge and
deletes `<fullname>-web` before a new pod is ready. One `helm upgrade` from the old shape to the cut would therefore leave the
edge without a ready backend for the start time of the new pods. An edge also applies a removed backend before a moved route:
HAProxy Ingress empties the backend of a deleted Service at once and takes a changed Ingress with its next reload, about 1.5 s
later (F-64), so an apply that both moves a route and removes the backend it pointed at answers `503` in between. The upgrade
therefore takes three steps, each of which changes either the edge or the set of backends, never both, joined by the bridge
values `web.enabled` and `web.routed` (Q-ARCH-295, Q-HA-73, Q-HA-130).

- **`web.enabled: true` (the bridge).** The chart renders, besides everything of R-HA-002 and R-HA-062:
  - `<fullname>-web` with `--role web` from the `web` values, which keep every key of the releases before the cut, its
    disruption budget, autoscaler and ingress policy, and the endpoint pools of the old shape (`<fullname>-web-ep-<name>`,
    `--role web`, `TROVE_POOL_NAME=ep-<name>`);
  - the endpoint pools of the target shape (`<fullname>-packages-ep-<name>`, `<fullname>-api-ep-<name>`) beside them, so that
    step 2 switches the endpoint's Service onto pods that are ready (Q-HA-90); for a surface with families of both roles the
    Service `<fullname>-api-ep-<name>` already selects the `api` pods, without a route, so that step 2 routes to a Service
    whose endpoints exist;
  - the Service `<fullname>` (port 8080, selecting the `web` pods of the main Deployment);
  - with `web.routed: true` (the default), the edge of the old shape: every rendered Ingress routes each host whole (`/`,
    `pathType: Prefix`) to `<fullname>`, and each further endpoint's Service selects the `web` pods that served it before
    (the main `web` Deployment or `<fullname>-web-ep-<name>`); with `web.routed: false`, the edge of the target shape
    (R-HA-037, R-EP-008) while everything above keeps running;
  - one BackendTLSPolicy (R-HA-063) for `<fullname>` and one for each Service of the target shape, for the whole bridge: a
    route kept outside the chart (a Gateway API HTTPRoute) moves between them at a time the chart does not know.

  The Deployments of `api`, `packages` and `worker`, their path pools and endpoint pools, and their Services run beside it;
  until step 2, `api` and `packages` receive no edge traffic, so their share of the fetch budget is unused during step 1
  (below). `NOTES.txt` names the step the release is at and the next one.
- **Step 1.** `helm upgrade` with `--set web.enabled=true` and the values otherwise unchanged: `<fullname>-web` rolls to the
  new release by R-HA-020 with no gap, because the edge keeps its Service, and the new Deployments become ready meanwhile.
  Between steps 1 and 3, in-cluster callers move to `<fullname>-api` and `<fullname>-packages` (R-HA-002), and the values gain
  the `api.*` and `packages.*` sizing the installation wants. Before step 1, policies outside the chart that admit trove's pods by
  `app.kubernetes.io/component` (an ingress policy of PostgreSQL, Valkey or the Sentinels) must admit `api` and `packages` beside
  `web`, and the labels cluster-wide policies key on move from `web.podLabels` to `api.podLabels` and `packages.podLabels`; otherwise
  the new pods cannot reach their dependencies and stay not ready, which the guard of step 2 then reports. The upgrade guide
  ([docs/manual/admin/upgrades.md](../manual/admin/upgrades.md)) lists this as the first check (seen on the owner's cluster on
  2026-11-06, where the policies of the database and Valkey admitted `web`, `worker` and `migrate` only).
- **Step 2.** `helm upgrade` with `--set web.enabled=true --set web.routed=false`: the edge switches to the path routing of
  R-HA-037 onto Services whose pods have been ready since step 1, and each further endpoint's Service onto the pods of its role.
  Nothing is deleted: `<fullname>-web`, its endpoint pools and the Service `<fullname>` keep running, so a request the edge
  still sends by the old route until it has applied the new one is served. An edge outside the chart (an HTTPRoute) is moved to
  the role Services now.
- **Step 3.** `helm upgrade` with `web.enabled: false` (the default): Helm deletes `<fullname>-web`, its endpoint pools and the
  Service `<fullname>`, which no route names any more; their pods drain by R-HA-011. No route changes.
- **Guard.** Where the chart can read the cluster (`helm upgrade`, `helm install --dry-run=server`; Helm's `lookup`), the
  release's namespace holds a Deployment `<fullname>-web` and the rendered edge is the target shape's (step 2 or step 3), it
  reads where the edge points: the edge is still on the bridge while the chart's Ingress `<fullname>` routes a path to the
  Service `<fullname>`, or a further endpoint's Service `<fullname>-ep-<name>` selects the `web` pods. It refuses to render
  ([_validate_roles.tpl](../../deploy/helm/trove/templates/_validate_roles.tpl): `trove.bridgeFacts` reads, `trove.bridgeGuard`
  decides):
  - the upgrade that moves the edge (the edge is still on the bridge) while a serving Deployment of the target shape
    (`<fullname>-api`, `<fullname>-packages`, their path pools and their endpoint pools) does not exist or has fewer ready
    replicas than its `spec.replicas`; the message names what still routes to the bridge, the Deployment and the three steps.
    Once the edge has moved, an upgrade at step 2 or 3 passes whatever the readiness of the target shape: it moves nothing,
    and a refusal would leave a crash-looping `api` or a rollout in progress only the way back to step 1 (Q-HA-132);
  - `web.enabled: false` while the edge is still on the bridge, which is step 3 without step 2; the message names what
    still routes to the bridge and the three steps;
  - where it reads no edge at all (no Ingress `<fullname>`, no endpoint Service), the target shape while `<fullname>-api` or
    `<fullname>-packages` does not exist: step 1 has not run.

  Under `helm template` and other renderers without cluster access the guard has nothing to read, and the release notes carry
  the three steps. An edge outside the chart is not read: an operator who skips step 2 there meets the gap of F-64.
- **Mixed versions.** During step 1 the replicas of the release before the cut (role `web`) and the new release (roles
  `web`, `api`, `packages`, `worker`) run side by side, and R-HA-020's rules hold unchanged: the registry and the status
  screen list the `web` replicas of both releases under the role `web`, and the new `api` and `packages` replicas as new rows
  with the roles `["api"]` and `["packages"]` (R-OPS-003, R-UI-038); the `web` replicas of the releases before the cut do not
  list them. An older worker does not claim the queue `vuln` (R-ARCH-016).
- **Fetch share during step 1.** The fetch share of a remote repository (R-REPO-040) counts the live replicas of `packages`,
  `web` and `all`. During step 1 it therefore counts the `packages` pods of the main Deployment, which receive no edge traffic,
  and the `packages` pods of endpoint pools, which fill nothing when the endpoint has `cacheFill: false`. Each pod that serves
  holds fewer slots than it would without them: the budget is never exceeded, and the throughput of cache fills is lower until
  step 2.
- **Left on.** The guard acts on steps 2 and 3 only. An installation that stays at step 1 keeps every host on the `web` pods,
  and one that stays at step 2 keeps them running beside the roles; both hold the login secrets
  ([H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut)),
  and the alert `TroveBridgeLeftOn` (Q-HA-92) reports a `web` replica registered for 24 hours.
- **The way back** is a rollback of a release (R-ARCH-025). Each step's rollback is the reverse of a step that changes one
  kind of thing: rolling back step 3 recreates `<fullname>-web` and the Service `<fullname>` without a route to them, rolling
  back step 2 moves the edge back onto `<fullname>`, whose pods still run, and rolling back step 1 is a rollback of
  `<fullname>-web` alone. None of them leaves a route without a ready backend, so a rollback goes one step at a time: from
  step 3 to step 2, then from step 2 to step 1. `helm rollback` applies a stored release without rendering it, so no guard
  can refuse a rollback from step 3 straight to a revision of step 1: it recreates `<fullname>-web` and points the Ingress
  at `<fullname>` in one apply, and the host answers `503` until the restored `web` pods are ready.
- A fresh installation never renders `web`: `web.enabled` defaults to `false`, `web.routed` has no effect without it, and the
  chart's `ci/` value sets render the bridge only in the value set that tests it.

Acceptance criteria:

- The upgrade e2e test installs the release before the cut, runs `npm ci` and `dotnet restore` loops and a browser journey of
  the UI against it, runs steps 1, 2 and 3 with `helm upgrade --wait`, and no iteration fails and the edge answers no request
  with `503` for want of a backend (Q-HA-131); between steps 1 and 3, `GET /api/v1/system/replicas` lists the roles `web`,
  `api`, `packages` and `worker`, after step 2 the Ingress `trove` routes no path to the Service `trove` while `trove-web` and
  the Service `trove` still exist, and after step 3 the registry lists no `web`.
- `helm upgrade` with `web.enabled: false` against a namespace that holds `trove-web` and no ready `trove-api` fails before
  anything changes, names `web.enabled` and the three steps; the same values under `helm template` render.
- `helm upgrade` with `web.enabled: false` after step 1, while the Ingress `trove` routes `/` to the Service `trove`, fails
  before anything changes and names step 2.
- After step 2, `helm upgrade` with the values of step 2 or 3 renders while a Deployment of the target shape is not ready
  (Q-HA-132).
- `helm template … --set web.enabled=true` renders `trove-web` with `--role web`, the Service `trove` selecting its pods, an
  Ingress whose every rule is `/` to `trove`, and the Deployments and Services of `api`, `packages` and `worker`; with
  `--set web.routed=false` the same Deployments and Services and the Ingress of the target shape.

### R-HA-022 · Background data migrations
Priority: **MVP** · Covers: OPS-074

**Status.** Partly built. One background migration exists, `data_migration_nuget_semver2`
([formats/nuget.md](formats/nuget.md) Q-NUGET-93): a job on the queue `maintenance`, a singleton through `coalesce`, in keyset
batches with a checkpoint, started only once no replica of the release before it is live. The table `background_migration`, the
pause and resume operations and a shared runner are not built. Until they are, each migration is declared by its package with the
`system_state` key of its completion, and the report of R-HA-018 and the guard read its state from that record and its newest job
([Q-HA-114](#q-ha-114--which-background-migrations-the-migration-report-lists-before-r-ha-022s-table-exists)); a contract step
declares its dependency with `-- trove:requires-background <name>`
([Q-HA-115](#q-ha-115--how-a-contract-step-declares-the-background-migration-it-depends-on-and-what-the-guard-does-with-it)). The
acceptance criteria are not tested in their form (a test migration over 1 million rows with three worker restarts); the wait for
the previous release is tested for the NuGet recomputation ([semver2_test.go](../../test/integration/nuget/semver2_test.go)).

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

**Status.** The chart side is built (phase 7), the post-restore Job of R-HA-025 as well: [backup.yaml](../../deploy/helm/trove/templates/backup.yaml) (ServiceAccount and
CronJob), [restore.yaml](../../deploy/helm/trove/templates/restore.yaml) (ServiceAccount and Job), the pod template and failure
policy in [_backup.tpl](../../deploy/helm/trove/templates/_backup.tpl), the validation in
[_validate_backup.tpl](../../deploy/helm/trove/templates/_validate_backup.tpl), checked by `test/chart/backup_test.go`. The Jobs
take only their own `extraEnv` ([Q-HA-149](#q-ha-149--the-backup-and-restore-jobs-take-the-environment-of-their-own-values-only)),
the restore Job has a grace period of its own ([Q-HA-147](#q-ha-147--the-restore-job-has-a-termination-grace-period-of-its-own)),
and the target's CA is the Secret `secrets.backupTargetCA`
([Q-HA-146](#q-ha-146--the-ca-of-the-backup-target-is-a-secret-of-the-chart)). The acceptance criteria that need a cluster (a
drain of the node of a running pod, `kubectl create job --from=cronjob/…` under load, the restore sequence on the local profile)
are the end-to-end suite's.

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
  rendered) and `terminationGracePeriodSeconds` from `restore.terminationGracePeriodSeconds` (60, at least 30, Q-HA-147).
- **A Job that failed for good** (its retries used up, or a `FailJob` exit) is continued after the
  cause is fixed by deleting it and running the same `helm upgrade` with the restore values again:
  `kubectl -n trove delete job trove-restore`. Helm then creates the Job anew, and its pod resumes.
  Turning `restore.enabled` off instead deletes the Job and renders the migration hook again, which
  refuses the anchored database (R-HA-019) and fails the upgrade, so an unfinished restore is never
  overtaken.

The operator's sequence (R-BAK-021):

```shell
helm repo add trove https://guided-traffic.github.io/trove/
# <v>: the chart version to install; example 0.1.0
helm install trove trove/trove -n trove -f values.yaml --version <v> \
  --set web.replicaCount=0 --set web.autoscaling.enabled=false \
  --set worker.replicaCount=0 --set worker.autoscaling.enabled=false \
  --set highAvailability.enforce=false \
  --set restore.enabled=true --set restore.backupId=20261028T020000Z-7f3a   # example ID
kubectl -n trove wait --for=condition=complete job/trove-restore --timeout=24h
helm repo update trove
helm upgrade trove trove/trove -n trove -f values.yaml --version <v>   # normal values
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

**Status.** Built (phase 7): the task `backup-check` (`40 5 * * *`, kind `backup-check` in the `maintenance` queue), the command
`trove admin backup check` and the gauge, in [internal/backup/platform](../../internal/backup/platform); the startup rule below is
`validateRecoveryWindow` in [internal/config/backup.go](../../internal/config/backup.go). What the check verifies and why only what is
declared: [Q-HA-151](#q-ha-151--the-platform-check-verifies-what-the-operator-declared-and-nothing-else),
[Q-HA-152](#q-ha-152--backuprecovery_window-and-backupobject_versioning-are-config-keys-with-the-startup-rule-of-r-ha-024),
[Q-HA-153](#q-ha-153--trove-admin-backup-check-runs-the-checks-without-storing-them-and-exits-5-on-a-failed-one). The check names the
results `archive_mode`, `object_versioning`, `noncurrent_expiry` and `grace_period`. Its gap is [H-564](#h-564--the-platform-check-reads-settings-not-outcomes).

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
  `content_status = 'missing'` per R-STOR-027 (the read of `blob.Store.Open`, which looks for the object once more after
  the mark, so that a concurrent repair stays, [storage.md](storage.md) Q-STOR-151; the alert `TroveBlobMissing` of
  R-HA-045 reads the counter). The mark heals by itself: once it is 30 s old a read asks the store again, and an
  object that came back is served ([storage.md](storage.md) R-STOR-012, Q-STOR-153). A remote repository treats it as a cache miss and
  fetches again (R-STOR-027; [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-025 "Lost content",
  Q-REPO-202), and answers `503 content_unavailable` while it cannot.
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

**Status.** Step 4 is built: `trove admin post-restore` ([internal/backup/postrestore](../../internal/backup/postrestore)) and the
hook Job of the chart ([post-restore.yaml](../../deploy/helm/trove/templates/post-restore.yaml), `postRestore.*`,
[Q-HA-150](#q-ha-150--the-post-restore-job-is-a-post-upgrade-hook-with-a-credentials-file-the-chart-renders)); the Job passes
`--credentials-file` for step 5's credential, so the journal replay lists object versions. The drill against the local profile is the
end-to-end suite's.

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
| `authz.project.deleted`, `authz.project.member_removed` | authorization R-AUTHZ-026, R-AUTHZ-031 | Delete the project (when empty), remove the membership |
| `repository.moved` | authorization R-AUTHZ-031, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-060 | Disable the repository when the restored database holds it in the source project, and report it `suspended` |
| `ci.issuer.*` and `ci.mapping.*` disables and deletions | gitlab-ci R-CI-002, R-CI-019, R-CI-020 | Disable the issuer or mapping. Disabling a mapping that was enabled also suspends every later enabled mapping of its tier, so no job falls through to a mapping that grants more ([Q-CI-11](gitlab-ci-oidc.md#q-ci-11--replaying-a-mapping-disable-under-first-match)) |
| `authz.policy.updated`, `authz.project.member_changed`; `ci.issuer.*` and `ci.mapping.*` updates; `settings.auth.changed` (the anonymous-access switch, R-AUTH-037) and `authz.settings.changed` | the pages above | Update rule below |
| `endpoint.exposure.deleted`; `endpoint.exposure.updated` | [endpoints.md](endpoints.md) R-EP-020 | Delete the rule; the update rule below with the exposure comparator of R-EP-020, whose suspension disables the rule |
| `npm.unpublish` | [formats/npm.md](formats/npm.md) R-NPM-017, R-NPM-047 | Withhold the version: tombstone it and apply the dist-tag rule, leaving its items stored and unserved, when the restored repository holds it live with the journaled integrity (`applied`); `already in effect` when it is tombstoned; otherwise `subject unknown`. Nothing is deleted; `trove admin npm reinstate` reverses it |
| `vulnerability.finding.status_changed`, `vulnerability.finding.reopened` when the change sets a download block (`block: set`) | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-013, Q-VULN-48 | Set `blocked` on the restored finding of the journaled id when it is `confirmed`; `already in effect` when it is blocked; `subject unknown` when it is missing. The entry is optional: a change that lifts a block or sets none is not journaled |
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

Journal objects (examples, one object per line). The envelope is `v`, `id`, `at` and `event`; `entry` is the object the
producer passed with the audit event, verbatim (Q-HA-140). The names inside `entry` are the owning page's:

```json
{"v": 1, "id": "0192f0c4-7a1e-7c3b-9d2e-5b8f1a2c3d4e", "at": "2026-10-28T12:00:03.114Z", "event": "token.revoked", "entry": {"tokenId": "0192f0a1-…", "reason": "admin"}}
{"v": 1, "id": "0192f0d2-…", "at": "2026-10-28T12:04:41.902Z", "event": "authz.policy.updated", "entry": {"policyId": "0191aa07-…", "name": "team-web-publish", "before": {"entries": [{"principal": "group:team-web", "actions": ["deploy", "read"]}]}, "after": {"entries": [{"principal": "group:team-web", "actions": ["read"]}]}}}
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
| Deployment | `<fullname>-api`, `<fullname>-packages`, `<fullname>-worker` | always (R-HA-002) |
| Deployment, Service | `<fullname>-api-<pool>`, `<fullname>-packages-<pool>`, `<fullname>-worker-<pool>` (Deployment only) | per entry of `api.pools`, `packages.pools` and `worker.pools` (R-HA-062) |
| Service (ClusterIP) | `<fullname>-api` and `<fullname>-packages`, port `http` 8080 → the main Deployment's pods of that role, `sessionAffinity: None` | always |
| Deployment, Service (ClusterIP) | `<fullname>-web` (`--role web`) and `<fullname>`, port `http` 8080 → `web` pods; `<fullname>-web-ep-<name>` for the endpoint pools of the old shape | `web.enabled`, the bridge of the upgrade across the cut (R-HA-064) |
| Service (ClusterIP) | `<fullname>-ops`, port `ops` 8081, every serving pod (`api`, `packages`, `worker`, `web`) | always (probes from the kubelet do not need it; ServiceMonitor does) |
| ConfigMap | `<fullname>-config` (`trove.yaml`) | always |
| ServiceAccount | `<fullname>` | `serviceAccount.create` |
| ConfigMap, ServiceAccount (hooks, weight −10) | `<fullname>-migrate` | `migrations.mode: hook` and not `restore.enabled` |
| Job (hook, weight 0) | `<fullname>-migrate` | `migrations.mode: hook` and not `restore.enabled` |
| Job | `<fullname>-restore` | `restore.enabled` (R-HA-060) |
| ServiceAccount | `<fullname>-restore` | `restore.enabled` and `restore.serviceAccount.create` |
| Job (hook) | `<fullname>-post-restore` | `postRestore.enabled` |
| CronJob | `<fullname>-backup` | `backup.enabled` (R-HA-060) |
| ServiceAccount | `<fullname>-backup` | `backup.enabled` and `backup.serviceAccount.create` |
| PodDisruptionBudget | per Deployment, none for Jobs | `podDisruptionBudget.enabled` of the role or pool |
| HorizontalPodAutoscaler (`autoscaling/v2`) | per Deployment | `autoscaling.enabled` of the role or pool |
| Ingress | `<fullname>`: per host the path rules of R-HA-037 to the role Services | `ingress.enabled` |
| HTTPRoute | `<fullname>`: the same path rules | `gateway.enabled` |
| Gateway | `<fullname>-gateway` | `gateway.enabled` and `gateway.create.enabled` (P1, R-HA-053) |
| NetworkPolicy | one per Deployment, named as the Deployment (`<fullname>-api`, `<fullname>-packages`, `<fullname>-worker`, each pool, each endpoint pool, `<fullname>-web` in the bridge), and `<fullname>-migrate`, `<fullname>-restore`, `<fullname>-backup`; ingress rules only, each with its port (R-HA-042) | `networkPolicy.enabled` |
| Service, Ingress, HTTPRoute | `<fullname>-ep-<name>` per further endpoint, and `<fullname>-api-ep-<name>` when its surface holds families of both roles: its Services and port, its own Ingress or HTTPRoute with its class, hosts (a wildcard host included), TLS entries and the path rules of its surface ([endpoints.md](endpoints.md) R-EP-008, Q-EP-71) | per entry of `endpoints`; the Ingress with its `ingress.enabled`, the HTTPRoute with its `gateway.enabled` |
| Deployment, PodDisruptionBudget, HorizontalPodAutoscaler | `<fullname>-packages-ep-<name>` and `<fullname>-api-ep-<name>`, the optional pool of an endpoint, one per role its surface holds ([endpoints.md](endpoints.md) R-EP-008, Q-EP-70) | `endpoints.<name>.pool.enabled` |
| ServiceMonitor, PrometheusRule | `<fullname>` | `metrics.serviceMonitor.enabled`, `metrics.prometheusRule.enabled` |
| anything | from `extraObjects` | listed |

- The chart renders no StatefulSet, no PersistentVolumeClaim and no Valkey, PostgreSQL or object
  store (R-HA-061).
- Labels on every object: `app.kubernetes.io/name: trove`, `app.kubernetes.io/instance`,
  `app.kubernetes.io/version`, `app.kubernetes.io/managed-by`, `helm.sh/chart`, and
  `app.kubernetes.io/component` (`api`, `packages`, `worker`, `web` in the bridge, `migrate`, `restore`, `backup`). The
  pods of `api`, `packages`, `worker` and `web` also carry `trove/pool` (name proposed): `<name>` for the pods of a pool of
  R-HA-062 (`ep-<name>` for an endpoint pool), and the empty value for the main Deployments. The ServiceMonitor sets the target label `pool`
  from it, and Prometheus drops an empty label, so the main Deployments' series carry no `pool`
  (R-HA-045). Selectors use name, instance, component and, for the serving roles, `trove/pool`,
  from the MVP on, because selector labels are immutable: adding a pool later then never overlaps
  the main Deployment's or the role Service's selector and never changes an immutable one. The cut adds Deployments
  under new names with new component values, so no immutable selector of the releases before it changes (R-HA-064).
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
| `highAvailability.enforce` and fewer than 2 replicas (`replicaCount`, or `autoscaling.minReplicas`) for `api`, `packages`, `worker`, `web` with `web.enabled`, or a pool, a disabled PDB, or a missing host spread constraint, one that is not `DoNotSchedule`, or one without `nodeTaintsPolicy: Honor` (R-HA-059 rule 3) | the Deployment and the value |
| `worker.replicaCount`, `api.replicaCount` or `packages.replicaCount` (or its `autoscaling.minReplicas`) < 1 | the value |
| `config.storage.upload.max_size` > the spool budget derived from the `packages` spool size (and the `web` spool size with `web.enabled`); the upload limit of the UI's content route (`config.ui.deploy.max_upload_bytes`, or `config.storage.upload.max_size` when it is 0) > the spool budget of `api` (R-HA-035, Q-UI-106) | both |
| `terminationGracePeriodSeconds` of a role < `config.health.shutdown_drain_delay` + 15 s | both |
| `api`, `packages`, `web` with `web.enabled`, a path pool or an endpoint pool without `resources.limits.memory`, or with one that admits fewer than 64 requests: ⌊0.8 × `config.runtime.memory_limit_ratio` × the limit ÷ the request bytes of R-ARCH-005⌋, from `config.storage.download.buffer_size`, `hold_back` and `config.storage.s3.http.receive_window` (R-HA-036, Q-HA-70) | the limit, the Deployment or the slot count and the values it comes from |
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
| `restore.enabled` with a replica count above 0 or autoscaling enabled for any role or pool | `restore.enabled` and the value |
| Two pools of a role with the same name, two pools with the same path prefix or queue, a pool path outside the families of its role or equal to one of the role's own prefixes of R-HA-037, a worker pool queue that does not exist, or a pool whose name starts with `ep-` (R-HA-062) | the pools |
| The edge of the target shape (`web.enabled: false`, or `web.routed: false`) while the namespace holds `<fullname>-web`, the edge is still on the bridge (the chart's Ingress `<fullname>` routes a path to the Service `<fullname>`, or a `<fullname>-ep-<name>` selects the `web` pods) and a serving Deployment of the target shape (api, packages, path pools, endpoint pools) is missing or not fully ready, read through `lookup` where the chart can read the cluster (R-HA-064, Q-HA-132) | what still routes to the bridge, the Deployment and the three steps |
| `web.enabled: false` while the namespace holds `<fullname>-web` and the edge is still on the bridge, read through `lookup` (R-HA-064, Q-HA-130) | what still routes to the bridge and the three steps |
| The edge of the target shape while the namespace holds `<fullname>-web`, the chart reads no edge (no Ingress `<fullname>`, no endpoint Service) and `<fullname>-api` or `<fullname>-packages` does not exist: step 1 has not run (R-HA-064, Q-HA-132) | the Deployment and the three steps |
| Worker pools that claim every queue; a worker pool without a replica; an `endpoints.default.surface` without a family of a role that no further endpoint is served by (R-HA-062) | the pool or the surface |
| A provider of `config.auth.oidc.providers` with a `client_auth_method` other than `none` and no entry in `secrets.oidc` (R-HA-030, Q-HA-93) | the provider |
| An endpoint whose `port` equals 8080, 8081 or another endpoint's port; an invalid endpoint name or one for which `<fullname>-api-ep-<name>` (a surface with families of both roles) or `<fullname>-packages-ep-<name>` (a pool) exceeds 63 characters; a missing `baseUrl`; a host in two endpoints; `extraSecrets` or `clientCaSecret` with `tls.enabled: false`; an endpoint pool below two replicas with `highAvailability.enforce` ([endpoints.md](endpoints.md) R-EP-008) | the endpoint and the value |
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
    Deployment, and `TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS` (`..._WEB_REPLICAS` before phase 6a) and
    `TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS` for the migration Job, the variables of the
    schema keys `runtime.memory_limit_bytes`, `runtime.termination_grace_period`,
    `database.preflight.expected_serving_replicas` and `database.preflight.expected_worker_replicas`
    (R-OPS-028 mapping; the chart is their only source; R-HA-016);
  - `TROVE_POOL_NAME` on the Deployment of each pool (R-HA-062, R-OPS-003), and on worker Deployments
    `TROVE_JOBS_QUEUES` (a pool) or `TROVE_JOBS_EXCLUDE_QUEUES` (the main `worker` beside pools), the variables of
    `jobs.queues` and `jobs.exclude_queues` ([architecture.md](architecture.md) R-ARCH-016);
  - `TROVE_STORAGE_SPOOL_MAX_BYTES` per Deployment, derived from the role's spool size when
    `config.storage.spool.max_bytes` is unset (R-HA-035);
  - `GOMAXPROCS` (R-HA-036);
  - in every trove pod (`api`, `packages`, `worker`, `web`, backup, restore, migration and post-restore), from the
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
| `secrets.valkey` (`api`, `packages`, `worker`, `web` only; not in backup, restore, migration or post-restore pods, R-BAK-012, R-BAK-022) | `username` (optional), `password` | `/var/run/secrets/trove/valkey/` | `valkey.username_file`, `valkey.password_file` |
| `secrets.valkeySentinel` (same pods) | `username` (optional), `password` | `/var/run/secrets/trove/valkey-sentinel/` | `valkey.sentinel.username_file`, `valkey.sentinel.password_file` |
| `secrets.valkeyCA` (same pods) | `ca.crt` | `/var/run/secrets/trove/valkey-ca/` | `valkey.tls.ca_file` |
| `secrets.valkeyClientCert` (same pods) | `tls.crt`, `tls.key` | `/var/run/secrets/trove/valkey-client/` | `valkey.tls.cert_file`, `valkey.tls.key_file` |
| `secrets.backupTarget` (backup and restore pods; only with `source: static`) | `access-key-id`, `secret-access-key`, `session-token` (only with `sessionTokenKey`) | `/var/run/secrets/trove/backup-target/` | `backup.target.credentials.access_key_id_file`, `…secret_access_key_file`, `…session_token_file` |
| `secrets.backupTargetCA` (backup and restore pods; Q-HA-146) | `ca.crt` | `/var/run/secrets/trove/backup-target-ca/` | `backup.target.ca_file` |
| `secrets.oidc[]` (one entry per provider ID; only the pods of role `api` (its main Deployment, its pools and its endpoint pools) and `web` with `web.enabled`; never a pod of `packages`, worker, backup, migration or restore pods, R-AUTH-042, Q-HA-79) | `client-secret` | `/var/run/secrets/trove/oidc-<id>/` | `client_secret_file` of the provider with that `id` in `config.auth.oidc.providers` |
| `secrets.breakGlass` (the same pods as `secrets.oidc[]`, R-AUTH-014, R-AUTH-042) | `password` | `/var/run/secrets/trove/break-glass/` | `auth.break_glass.password_file`; empty value → `auth.break_glass.enabled: false` |
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
  Further bootstrapping is `kubectl exec deploy/trove-api -- trove admin …` (R-AUTH-015), in the role that mounts the
  break-glass secret (`deploy/trove-web` before phase 6a). The chart
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

Every value of the chart with its default. In the block below, as in the chart's `values.yaml`, every value is its default
and is commented only where its name does not say its meaning, unit, range or consequence, with an `example:` where the
default is empty (owner, 2026-11-04: no content-free comments). Keys under
`config` belong to the pages named in the comments; this page only fixes how the chart wires them.
Keys marked P1 (`gateway.create`, `contentOrigin`, `bootstrap`) enter the chart with the release that implements them, and
the keys of phase 6a (`api`, `packages`, `web.enabled`, the pools of R-HA-062) with phase 6a: the `config` subtree is validated against the
schema the binary emits (R-HA-029), so a key the binary does not know yet would fail
`helm install`. The keys under `config.backup` are [backup-and-restore.md](backup-and-restore.md)
R-BAK-011's, shown here with every key and default.

<details>
<summary>deploy/helm/trove/values.yaml</summary>

```yaml
# ── Global ──────────────────────────────────────────────────────────────────
global:
  imageRegistry: ""                     # empty = image.registry
  imagePullSecrets: []                  # example: [{name: registry-pull}]

nameOverride: ""
fullnameOverride: ""
commonLabels: {}
commonAnnotations: {}

image:
  registry: docker.io                   # Docker Hub (build.yml publishes there)
  repository: guidedtraffic/trove       # the image of the release pipeline
  tag: ""                               # empty = chart appVersion
  digest: ""                            # example: sha256:<64 hex>; wins over tag
  pullPolicy: IfNotPresent

highAvailability:
  enforce: true                         # >= 2 replicas, a PDB and DoNotSchedule host spreading per role; Sentinel mode with >= 3 Sentinels

# ── api Deployment: trove serve --role api ───────────────────────
# The UI, the management API, the sign-in and the CI exchange; the login secrets mount here only.
api:
  replicaCount: 2                       # ignored when autoscaling.enabled
  minReadySeconds: 10
  revisionHistoryLimit: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  terminationGracePeriodSeconds: 120    # the UI's uploads and the audit exports
  spool:
    sizeLimit: 5Gi                      # holds the UI's uploads through the content route, up to config.ui.deploy.max_upload_bytes
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
      ephemeral-storage: 1Gi
    limits:
      memory: 1Gi                       # no CPU limit by default
  autoscaling:
    enabled: false
    minReplicas: 2
    maxReplicas: 4
    targetCPUUtilizationPercentage: 70
    targetMemoryUtilizationPercentage: 0  # 0 = no memory metric
    metrics: []                         # additional autoscaling/v2 metric specs
    behavior:
      scaleUp:
        stabilizationWindowSeconds: 0
        policies:
          - type: Pods
            value: 2
            periodSeconds: 60
      scaleDown:
        stabilizationWindowSeconds: 300
        policies:
          - type: Pods
            value: 1
            periodSeconds: 120
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
    minAvailable: null                  # mutually exclusive with maxUnavailable
    unhealthyPodEvictionPolicy: AlwaysAllow
  topologySpread:
    enabled: true                       # render the two constraints below
    nodeTaintsPolicy: Honor             # cordoned nodes do not count as domains
    zone:
      maxSkew: 1
      whenUnsatisfiable: ScheduleAnyway
    host:
      maxSkew: 1
      whenUnsatisfiable: DoNotSchedule  # required with highAvailability.enforce
  topologySpreadConstraints: []         # non-empty replaces the generated constraints
  nodeFailureTolerationSeconds: 60      # not-ready/unreachable tolerations
  affinity: {}
  nodeSelector: {}                      # example: {kubernetes.io/arch: arm64}
  tolerations: []
  priorityClassName: ""                 # example: business-critical
  schedulerName: ""
  podAnnotations: {}
  podLabels: {}
  extraEnv: []                          # appended after the shared extraEnv
  pools: []                             # example: [{name: admin, paths: [/api/v1/projects], replicaCount: 2}]

# ── packages Deployment: trove serve --role packages ─────────────
# The package protocols and the raw file transfer, tokens only; no login secret mounts here.
packages:
  replicaCount: 3                       # ignored when autoscaling.enabled
  minReadySeconds: 10
  revisionHistoryLimit: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  terminationGracePeriodSeconds: 120
  spool:
    sizeLimit: ""                       # empty = spool.sizeLimit
  resources:
    requests:
      cpu: "1"
      memory: 1Gi
      ephemeral-storage: 2Gi
    limits:
      memory: 2Gi                       # no CPU limit by default
  autoscaling:
    enabled: false
    minReplicas: 3
    maxReplicas: 8
    targetCPUUtilizationPercentage: 70
    targetMemoryUtilizationPercentage: 0  # 0 = no memory metric
    metrics: []                         # additional autoscaling/v2 metric specs
    behavior:
      scaleUp:
        stabilizationWindowSeconds: 0
        policies:
          - type: Pods
            value: 2
            periodSeconds: 60
      scaleDown:
        stabilizationWindowSeconds: 300
        policies:
          - type: Pods
            value: 1
            periodSeconds: 120
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
    minAvailable: null                  # mutually exclusive with maxUnavailable
    unhealthyPodEvictionPolicy: AlwaysAllow
  topologySpread:
    enabled: true                       # render the two constraints below
    nodeTaintsPolicy: Honor             # cordoned nodes do not count as domains
    zone:
      maxSkew: 1
      whenUnsatisfiable: ScheduleAnyway
    host:
      maxSkew: 1
      whenUnsatisfiable: DoNotSchedule  # required with highAvailability.enforce
  topologySpreadConstraints: []         # non-empty replaces the generated constraints
  nodeFailureTolerationSeconds: 60      # not-ready/unreachable tolerations
  affinity: {}
  nodeSelector: {}                      # example: {kubernetes.io/arch: arm64}
  tolerations: []
  priorityClassName: ""                 # example: business-critical
  schedulerName: ""
  podAnnotations: {}
  podLabels: {}
  extraEnv: []                          # appended after the shared extraEnv
  pools: []                             # example: [{name: npm, paths: [/api/npm], replicaCount: 4}]

# ── web Deployment: the bridge of the upgrade across the cut ─────
# Rendered only with web.enabled: trove serve --role web, the Service <fullname> and the edge of the releases
# before the cut. Every other key keeps its meaning and default of those releases; a fresh installation never sets it.
web:
  enabled: false                        # true for steps 1 and 2 of the cut (R-HA-064)
  routed: true                          # false in step 2: the edge moves to api and packages while the bridge still runs
  replicaCount: 3                       # ignored when autoscaling.enabled
  minReadySeconds: 10
  revisionHistoryLimit: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  terminationGracePeriodSeconds: 120
  spool:
    sizeLimit: ""                       # empty = spool.sizeLimit
  resources:
    requests:
      cpu: "1"
      memory: 1Gi
      ephemeral-storage: 2Gi
    limits:
      memory: 2Gi                       # no CPU limit by default
  autoscaling:
    enabled: false
    minReplicas: 3
    maxReplicas: 8
    targetCPUUtilizationPercentage: 70
    targetMemoryUtilizationPercentage: 0  # 0 = no memory metric
    metrics: []                         # additional autoscaling/v2 metric specs
    behavior:
      scaleUp:
        stabilizationWindowSeconds: 0
        policies:
          - type: Pods
            value: 2
            periodSeconds: 60
      scaleDown:
        stabilizationWindowSeconds: 300
        policies:
          - type: Pods
            value: 1
            periodSeconds: 120
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
    minAvailable: null                  # mutually exclusive with maxUnavailable
    unhealthyPodEvictionPolicy: AlwaysAllow
  topologySpread:
    enabled: true                       # render the two constraints below
    nodeTaintsPolicy: Honor             # cordoned nodes do not count as domains
    zone:
      maxSkew: 1
      whenUnsatisfiable: ScheduleAnyway
    host:
      maxSkew: 1
      whenUnsatisfiable: DoNotSchedule  # required with highAvailability.enforce
  topologySpreadConstraints: []         # non-empty replaces the generated constraints
  nodeFailureTolerationSeconds: 60      # not-ready/unreachable tolerations
  affinity: {}
  nodeSelector: {}                      # example: {kubernetes.io/arch: arm64}
  tolerations: []
  priorityClassName: ""                 # example: business-critical
  schedulerName: ""
  podAnnotations: {}
  podLabels: {}
  extraEnv: []                          # appended after the shared extraEnv
  pools: []                             # kept for the releases before the cut; ignored, the bridge renders no path pool

# ── worker Deployment: trove serve --role worker ─────────────────
worker:
  replicaCount: 2
  minReadySeconds: 10
  revisionHistoryLimit: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  terminationGracePeriodSeconds: 300    # jobs persist their cursor before exit
  spool:
    sizeLimit: 5Gi                      # workers spool only cache fills of jobs and, from P1, the nuget-symbol-index job's one extracted PDB at a time (formats/nuget.md)
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
      ephemeral-storage: 1Gi
    limits:
      memory: 1Gi
  autoscaling:
    enabled: false
    minReplicas: 2
    maxReplicas: 4
    targetCPUUtilizationPercentage: 80
    targetMemoryUtilizationPercentage: 0
    metrics: []
    behavior: {}                        # Kubernetes defaults
  podDisruptionBudget:
    enabled: true
    maxUnavailable: 1
    minAvailable: null
    unhealthyPodEvictionPolicy: AlwaysAllow
  topologySpread:
    enabled: true
    nodeTaintsPolicy: Honor
    zone:
      maxSkew: 1
      whenUnsatisfiable: ScheduleAnyway
    host:
      maxSkew: 1
      whenUnsatisfiable: DoNotSchedule  # workers spread evenly; with nodes ≥ replicas no two share a node (INV-12)
  topologySpreadConstraints: []
  nodeFailureTolerationSeconds: 60
  affinity: {}
  nodeSelector: {}
  tolerations: []
  priorityClassName: ""
  schedulerName: ""
  podAnnotations: {}
  podLabels: {}
  extraEnv: []
  pools: []                             # example: [{name: vuln, queues: [vuln], replicaCount: 2}]; the main worker claims every queue no pool lists

# ── trove configuration (rendered to /etc/trove/trove.yaml) ─────────────────
# Chart-managed keys (secret file paths, storage.spool.dir, server.content_base_url) must not be set here (R-HA-029).
# One file serves every Deployment and Job; the role is the container argument --role.
# The hook ConfigMap of the migration Job carries the same file (R-HA-019); the restore Job mounts <fullname>-config.
config:
  server:                               # operations-and-observability.md
    base_url: ""                        # required; example: https://trove.example.com
    trusted_proxies: []                 # example: ["10.244.3.17", "10.244.5.9"] (the addresses of the ingress controller pods, never the whole pod CIDR; a prefix of length 0 is refused)
    forwarded_header: x-forwarded-for   # the one header the edge rewrites, x-forwarded-for | forwarded; the other is never read
    additional_base_urls: []
  health:                               # operations-and-observability.md
    require_object_store: false         # object storage gates only the first readiness (R-HA-008, Q-HA-1)
    shutdown_drain_delay: 5s            # default; must exceed the edge's endpoint removal time, measured at 0.2 to 1.8 s on the dev edge (Q-HA-111)
  metrics:                              # operations-and-observability.md
    enabled: true
    auth: none                          # none | static (needs secrets.metricsToken)
  log:
    level: info                         # operations-and-observability.md
  database:                             # this page
    host: ""                            # required; example: trove-db-rw.trove.svc, or the request pooler
    port: 5432
    session_host: ""                    # empty = host; required with pooler.mode transaction; example: trove-db-rw.trove.svc
    session_port: 0                     # 0 = port
    control_host: ""                    # empty = host; required with pooler.mode transaction (R-HA-058, Q-HA-20); example: trove-db-pooler-control.trove.svc
    control_port: 0                     # 0 = port
    control_name: ""                    # empty = name
    name: trove
    schema: trove
    backup_role: ""                     # empty = trove migrate grants no backup privileges; example: trove_backup (R-HA-014, R-BAK-015)
    ssl_mode: verify-full
    target_session_attrs: read-write
    connect_timeout: 5s
    pooler:
      mode: none                        # none | session | transaction (R-HA-058, Q-HA-6)
      server_connections: 0             # with transaction: the server-side total of the request and control poolers (R-HA-016); example: 100
    pool:
      max_conns: 20                     # at least 2, one is the control connection (R-HA-016)
      min_conns: 2
      max_conn_lifetime: 30m            # 5m recommended with a transaction-mode pooler
      max_conn_lifetime_jitter: 5m      # 1m recommended with a transaction-mode pooler
      max_conn_idle_time: 5m
      health_check_period: 30s
    lease_pool_size: 4
    lease_check_interval: 5s
    session:
      statement_timeout: 30s
      lock_timeout: 5s
      idle_in_transaction_session_timeout: 60s
    retry:
      initial_backoff: 100ms
      max_backoff: 2s
      max_elapsed: 30s
    keepalive:
      idle: 15s
      interval: 5s
      count: 3
      user_timeout: 30s
    server_keepalive:                   # sent as tcp_keepalives_* startup parameters (R-HA-015)
      idle: 60s
      interval: 10s
      count: 6
    preflight:
      connection_budget: enforce        # enforce | warn
      reserved_connections: 10
      allow_superuser: false
  valkey:                               # this page
    mode: sentinel                      # sentinel | standalone (development only)
    sentinel:
      addresses: []                     # required; example: [trove-valkey-sentinel-0.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-1.trove-valkey-sentinel.trove.svc:26379, trove-valkey-sentinel-2.trove-valkey-sentinel.trove.svc:26379]
      primary_name: ""                  # required; example: trove
      tls: true
      refresh_interval: 5s
    address: ""                         # standalone mode only; example: trove-valkey.trove.svc:6379
    database: 0
    key_prefix: "trove:"
    tls:
      mode: verify-full                 # verify-full | verify-ca | disable
      server_name: ""                   # host name of each address
    allow_insecure: false
    connect_timeout: 2s
    command_timeout: 500ms
    sentinel_query_timeout: 300ms
    max_conn_lifetime: 30m
    health_check_interval: 5s
    reconnect:
      initial_backoff: 100ms
      max_backoff: 5s
    circuit_breaker:
      failure_threshold: 5
      open_duration: 5s
  storage:                              # storage.md
    s3:
      bucket: ""                        # required; example: trove-blobs
      prefix: trove                   # the default of storage.md
      region: ""                        # example: eu-central-1
      endpoint: ""                      # AWS resolution; example: https://minio.trove.svc:9000
      force_path_style: false           # true for MinIO, SeaweedFS and Ceph RGW
      credentials:
        source: default                 # default | webIdentity | static
        role_arn: ""
    upload:
      max_size: 10GiB                   # must fit the spool
    gc:
      grace_period: 24h                 # the default of storage.md; see R-HA-024
  jobs:
    schedule_tick: 10s
    lease_duration: 60s
    heartbeat_interval: 15s
    background_migrations:
      batch_size: 1000
      pause_between_batches: 100ms
  migrate:
    advisory_lock_wait: 5m              # also how long a migration waits for a backup's database phase (R-HA-019)
    ddl_lock_timeout: 5s
    ddl_lock_retries: 20
  backup:                               # backup-and-restore.md R-BAK-011 owns these keys; manual_run.* is chart-managed (R-HA-030)
    target:
      endpoint: ""                      # AWS resolution; example: https://s3.eu-central-1.amazonaws.com
      region: ""                        # example: eu-central-1
      bucket: ""                        # required with backup.enabled; example: trove-backups
      prefix: trove
      force_path_style: false
      allow_insecure_endpoint: false
      ca_file: ""                       # system trust store
      storage_class: ""                 # bucket default; example: STANDARD_IA
      allow_same_bucket: false          # R-BAK-002, R-HA-060
      credentials:                      # same keys as storage.s3.credentials (R-STOR-031); the *_file keys are chart-managed (secrets.backupTarget, workload identity), not set here
        source: default                 # default | webIdentity | static (secrets.backupTarget)
        role_arn: ""                    # example: arn:aws:iam::210987654321:role/trove-backup-writer
        sts_endpoint: ""
      sse:
        mode: s3                        # s3 | kms | none (R-BAK-026)
        kms_key_id: ""                  # example: arn:aws:kms:eu-central-1:210987654321:key/9a8b…
        bucket_key: true
    database:
      parallel: 2                       # backup connections = 2 + parallel (R-HA-016)
      lock_timeout: 60s
      migration_wait: 15m               # below stall_timeout
      part_size: 16MiB
    copy:
      concurrency: 16
      server_side: auto                 # auto | always | never
      part_size: 256MiB
      check_existing: auto              # auto | always | never
      bytes_per_second: 0               # 0 = unlimited
    compression_level: 3                # zstd level 1–19
    exclude:
      remote_caches: false              # R-BAK-008
    retention:
      keep_last: 7                      # ≥ 1
      keep_within: 0s                   # count only; example: 720h
      sweep_interval: 168h              # full pool sweep (R-BAK-017)
    expected_interval: 24h              # drives TroveBackupStale through its gauge (R-HA-045, R-BAK-014)
    lease_duration: 5m
    heartbeat_interval: 15s             # × 4 ≤ lease_duration
    stall_timeout: 30m                  # a run without progress cancels itself (R-BAK-013)
    finalize_timeout: 30s               # × 2 < lease_duration
    verify_sample: 1000
    run_history: 2160h                  # backup_run rows kept 90 days
    recovery_window: 0s                 # undeclared; example: 168h (R-HA-024)
    object_versioning: false
    revocation_journal:                 # R-HA-054 (MVP, Q-HA-9)
      enabled: true
      ship_interval: 5s
      replay_margin: 5m
  runtime:
    memory_limit_ratio: 0.85            # R-HA-036
  audit:                                # operations-and-observability.md R-OPS-015, R-OPS-017
    retention_days: 365                 # written to audit_policy by the migration Job (R-HA-014)
  auth:                                 # identity-and-authentication.md (OIDC providers, breakGlass.username, …)
    login_page:
      auto_redirect: true               # true: with exactly one OIDC provider the sign-in page goes straight to it; /ui/login?auto_sign_in=false still shows the page with the break-glass form. false: the page lists the providers
  authz: {}                             # empty; authorization.md
  ci: {}                                # empty; gitlab-ci-oidc.md

# ── TLS on trove's own listeners ────────────────────
# The chart never creates the Secret. It holds the keys of a cert-manager TLS Secret (tls.crt,
# tls.key and, for the edge and for Prometheus, ca.crt). Mounted as a directory volume without
# subPath, so a rotated Secret reaches the pod through the kubelet's atomic swap and the process
# reloads the files (tls.reloadInterval). tls.* configures the default client listener :8080 and
# the operations listener :8081; each entry of endpoints carries its own tls block.
tls:
  enabled: true                         # false = plain HTTP inside the cluster
  secretName: ""                        # required with tls.enabled; example: trove-tls (cert-manager Certificate secretName)
  certKey: tls.crt                      # key of the certificate chain in the Secret
  keyKey: tls.key                       # key of the private key in the Secret
  minVersion: "1.2"                     # "1.2" | "1.3" (server.tls.min_version)
  reloadInterval: 10s                   # content poll of the mounted files (server.tls.reload_interval)
  clientCA:                             # optional mutual TLS: the edge must present a client certificate
    secretName: ""                      # empty = no client certificates requested; example: trove-edge-ca
    key: ca.crt                         # key of the CA bundle in that Secret
    auth: require                       # require | verify_if_given (server.tls.client_auth)
  ops:                                  # optional separate certificate for:8081 (probes, metrics); empty = same material as:8080. Recommended with endpoint pools and workers: only then the listener private key of:8080 stays out of the pods that do not serve it
    secretName: ""                      # example: trove-ops-tls
    certKey: tls.crt
    keyKey: tls.key
  backendTLSPolicy:                     # Gateway API: how the Gateway verifies trove (needs the BackendTLSPolicy CRD)
    enabled: false
    caConfigMapName: ""                 # required when enabled; example: trove-tls-ca (a ConfigMap with ca.crt: Gateway API takes no Secret here)
    hostname: ""                        # empty = <fullname>.<namespace>.svc; must be a SAN of the certificate

# ── Existing Secrets (the chart never creates one) ──────────────────────────
secrets:
  database:
    existingSecret: ""                  # required; example: trove-db-app (CloudNativePG application Secret)
    usernameKey: username
    passwordKey: password
  databaseMigration:
    existingSecret: ""                  # empty = single-role mode; example: trove-db-owner
    usernameKey: username
    passwordKey: password
  databaseBackup:
    existingSecret: ""                  # empty = secrets.database (R-HA-014, R-HA-060); example: trove-db-backup
    usernameKey: username
    passwordKey: password
  databaseCA:
    existingSecret: ""                  # example: trove-db-ca (only ca.crt is projected)
    key: ca.crt
  databaseClientCert:
    existingSecret: ""                  # no client certificate
    certKey: tls.crt
    keyKey: tls.key
  s3:
    existingSecret: ""                  # required with credentials.source static; also the Secret of an S3 provisioner when a *Key below is set
    accessKeyIdKey: access-key-id       # example (provisioner Secret): AWS_ACCESS_KEY_ID
    secretAccessKeyKey: secret-access-key  # example (provisioner Secret): AWS_SECRET_ACCESS_KEY
    bucketKey: ""                       # the bucket comes from config.storage.s3.bucket; example (provisioner Secret): S3_BUCKET; set = TROVE_STORAGE_S3_BUCKET reads this key of the Secret
    regionKey: ""                       # config.storage.s3.region; example (provisioner Secret): S3_REGION; set = TROVE_STORAGE_S3_REGION reads this key
    endpointKey: ""                     # config.storage.s3.endpoint; example (provisioner Secret): S3_ENDPOINT (a host without scheme, which trove reaches over https); set = TROVE_STORAGE_S3_ENDPOINT reads this key
    sessionTokenKey: ""
  valkey:
    existingSecret: ""                  # required; example: trove-valkey
    usernameKey: username               # a missing key = the "default" user
    passwordKey: password
  valkeySentinel:
    existingSecret: ""                  # Sentinels without authentication; example: trove-valkey-sentinel
    usernameKey: ""                     # empty = no Sentinel user, the file is not mounted
    passwordKey: password
  s3CA:
    existingSecret: ""                  # empty = the outbound trust store (system roots plus config.proxy.outbound.tls.ca_files); example: trove-s3-tls. Renders storage.s3.ca_file
    key: ca.crt
  valkeyCA:
    existingSecret: ""                  # empty = system roots plus trustedCA; example: trove-valkey-ca
    key: ca.crt
  valkeyClientCert:
    existingSecret: ""                  # no client certificate; needed while Valkey keeps tls-auth-clients yes
    certKey: tls.crt
    keyKey: tls.key
  backupTarget:
    existingSecret: ""                  # required with config.backup.target.credentials.source static
    accessKeyIdKey: access-key-id
    secretAccessKeyKey: secret-access-key
    sessionTokenKey: ""                 # empty = no session token
  backupSource:
    existingSecret: ""                  # required with config.storage.s3.credentials.source static and backup.enabled; the backup pods only. A login that may only read the blob bucket (s3:GetObject, s3:ListBucket), so a compromised backup pod cannot write the blobs or the journal (R-BAK-015, Q-HA-160)
    accessKeyIdKey: access-key-id
    secretAccessKeyKey: secret-access-key
  journalReader:
    existingSecret: ""                  # required with journalCredentials.source static of postRestore or restore; those Jobs only. A login that may list the object versions of the blob bucket (s3:ListBucketVersions), which the runtime credential deliberately lacks (R-HA-023)
    accessKeyIdKey: access-key-id
    secretAccessKeyKey: secret-access-key
  backupTargetCA:
    existingSecret: ""                  # empty = the system trust store; the backup and restore pods only; example: trove-backup-target-tls. Renders backup.target.ca_file (Q-HA-146)
    key: ca.crt
  oidc: []                              # example:
  #  - providerId: corp                 # example; matches config.auth.oidc.providers[].id
  #    existingSecret: trove-oidc-corp  # example
  #    key: client-secret               # example
  breakGlass:
    existingSecret: ""                  # empty disables the break-glass account; example: trove-break-glass
    key: password                       # plaintext or Argon2id PHC hash
  encryption:
    existingSecret: ""                  # required; example: trove-encryption-keys
    keys: []                            # required; ordered, first = active; example: [2026-10.key, 2026-04.key]
  metricsToken:
    existingSecret: ""                  # needed with config.metrics.auth static
    key: token

trustedCA:
  existingSecret: ""                    # example: corp-ca-bundle
  existingConfigMap: ""                 # example: corp-ca-bundle (trust-manager target)
  keys: [ca-bundle.crt]
  useForObjectStore: false              # also set storage.s3.caFile

bootstrap:
  existingConfigMap: ""                 # configuration documents (P1)

# ── Pod identity and security ──────────────────────────────────────────────
serviceAccount:
  create: true
  name: ""                              # <fullname>
  annotations: {}                       # example (IRSA):
  #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-s3  # example
  automountServiceAccountToken: false   # trove never calls the Kubernetes API

podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532                      # null on OpenShift (restricted-v2 assigns the UID)
  runAsGroup: 65532                     # null on OpenShift
  fsGroup: 65532                        # null on OpenShift
  fsGroupChangePolicy: OnRootMismatch
  seccompProfile:
    type: RuntimeDefault

securityContext:
  allowPrivilegeEscalation: false
  privileged: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  capabilities:
    drop: [ALL]
  seccompProfile:
    type: RuntimeDefault

# ── Volumes: nothing ReadWriteMany, nothing persistent ─────────────────────
spool:
  type: emptyDir                        # emptyDir | ephemeral
  sizeLimit: 20Gi                       # web.spool and worker.spool override it
  headroom: 2GiB                        # sizeLimit - headroom = storage.spool.max_bytes
  medium: ""                            # node disk; "Memory" counts against the memory limit
  ephemeral:
    storageClassName: ""                # cluster default
    size: 50Gi                          # used with type ephemeral
    accessModes: [ReadWriteOnce]        # ReadWriteOnce | ReadWriteOncePod; nothing else validates

tmp:
  sizeLimit: 1Gi                        # /tmp under readOnlyRootFilesystem

# ── Probes ────────────────────────────────
probes:
  startup:
    enabled: true
    path: /healthz
    periodSeconds: 2
    timeoutSeconds: 2
    failureThreshold: 60                # 2 minutes to bind the listeners
    custom: {}                          # replaces the generated probe
  liveness:
    enabled: true
    path: /healthz
    periodSeconds: 10
    timeoutSeconds: 3
    failureThreshold: 6
    custom: {}
  readiness:
    enabled: true
    path: /readyz
    periodSeconds: 5
    timeoutSeconds: 3
    failureThreshold: 1                 # debouncing happens in the process (health.failure_threshold)
    successThreshold: 1
    custom: {}

# ── Exposure ───────────────────────────────────────────────────────────────
service:
  type: ClusterIP                       # ClusterIP | LoadBalancer | NodePort
  port: 8080
  opsPort: 8081
  # No sessionAffinity value: the client Service always renders sessionAffinity: None (R-HA-037).
  annotations: {}
  labels: {}
  loadBalancerSourceRanges: []
  externalTrafficPolicy: ""             # Kubernetes default; example: Local
  ipFamilyPolicy: ""                    # cluster default; example: PreferDualStack (R-HA-044)
  ipFamilies: []                        # example: [IPv6, IPv4]

ingress:
  enabled: false
  className: ""                         # cluster default IngressClass; example: traefik
  annotations: {}                       # controller-specific settings; never cookie affinity
  labels: {}
  hosts: []                             # empty = host of config.server.base_url
  # No path value: every host gets the fixed path rules of R-HA-037 (Prefix rules per route family onto <fullname>-api and
  # <fullname>-packages; / onto <fullname> with web.enabled).
  tls: []                               # example: [{secretName: trove-tls, hosts: [trove.example.com]}]
  backendTLS:                           # how the controller reaches trove when tls.enabled; trove speaks HTTPS, so the controller must too
    controller: haproxy                 # haproxy (HAProxy Ingress, the primary target) | traefik | ingress-nginx | manual (you set the backend protocol in ingress.annotations / service.annotations)
    verify: true                        # the controller verifies the trove certificate; needs caSecret (haproxy, ingress-nginx) or serversTransport (traefik); false = encrypted but unverified
    caSecret: ""                        # required with verify on haproxy and ingress-nginx; Secret in the release namespace with ca.crt; example: trove-tls (a cert-manager Secret carries ca.crt)
    clientCertSecret: ""                # haproxy: Secret with tls.crt and tls.key that HAProxy Ingress presents to trove (required with tls.clientCA); example: trove-edge-client
    serversTransport: ""                # traefik: ServersTransport reference <namespace>-<name>@kubernetescrd that verifies trove; example: trove-trove-tls@kubernetescrd

gateway:
  enabled: false
  parentRefs: []                        # example: [{name: shared, namespace: gateway-system, sectionName: https}]
  hostnames: []                         # empty = host of config.server.base_url
  annotations: {}
  labels: {}
  timeouts:
    request: 3600s                      # R-HA-039
    backendRequest: 3600s
  filters: []                           # HTTPRoute filters on every path rule of R-HA-037
  extraRules: []
  create:                               # P1 (R-HA-053)
    enabled: false                      # attach to an existing Gateway
    gatewayClassName: ""                # example: envoy-gateway
    listeners: []                       # example: [{name: https, protocol: HTTPS, port: 443, tls: {certificateRefs: [{name: trove-tls}]}}]

endpoints: {}                           # only the default endpoint; every key in endpoints.md

contentOrigin:                          # P1 (R-HA-055); renders config.server.content_base_url (R-ARCH-029)
  host: ""                              # empty = one origin (R-ARCH-007); example: trove-content.example.net
  tls:
    secretName: ""                      # no TLS entry of its own; example: trove-content-tls

# ── Network policy: ingress rules only, one policy per Deployment, each rule with its port ─
networkPolicy:
  enabled: true
  ingress:
    http:
      from: []                          # empty = from anywhere; example: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ingress}}}]
    ops:
      from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
    probeCIDRs: []                      # node CIDRs, for CNIs that apply policy to kubelet probes
  # No egress value: the chart renders ingress rules only, each with its port (R-HA-042, ADR-154).

# ── Monitoring ─────────────────────────────────────────────────────────────
metrics:
  serviceMonitor:
    enabled: false
    namespace: ""                       # release namespace
    interval: 30s
    scrapeTimeout: 10s
    labels: {}                          # example: {release: kube-prometheus-stack}
    relabelings: []                     # appended after the fixed role and pool relabelings (R-HA-045)
    metricRelabelings: []
  prometheusRule:
    enabled: false
    labels: {}
    disabledAlerts: []                  # example: [TroveSpoolNearlyFull]
    extraRules: []

# ── Migrations and restore ─────────────────────────────────────────────────
migrations:
  mode: hook                            # hook | disabled
  backoffLimit: 2
  activeDeadlineSeconds: 1800
  ttlSecondsAfterFinished: 86400
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      memory: 512Mi
  podAnnotations: {}
  extraEnv: []

postRestore:
  enabled: false                        # renders the post-restore hook Job once
  restoredTo: ""                        # required with enabled, the time t the platform restore went to, RFC 3339; example: "2026-10-28T09:15:00Z"
  skipRevocationJournal: false          # only when the source's bucket is gone (R-BAK-024)
  journalCredentials:
    source: runtime                     # runtime (the blob store's credential, a plain listing) | default (the identity of the Job's ServiceAccount lists object versions) | static (secrets.journalReader) (Q-HA-150)
  backoffLimit: 2                       # a retry applies the clean-up again; its steps are safe to repeat
  activeDeadlineSeconds: 0              # 0 = not rendered
  terminationGracePeriodSeconds: 60     # at least 30
  serviceAccount:
    create: true
    name: ""                            # <fullname>-post-restore
    annotations: {}                     # example (IRSA, with s3:ListBucketVersions on the blob bucket when journalCredentials.source is default): {eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/trove-post-restore"}
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      memory: 512Mi
  podAnnotations: {}                    # safe-to-evict: "false" is always added
  extraEnv: []

# ── Backup CronJob and restore Job (R-HA-060, backup-and-restore.md R-BAK-012) ──
backup:
  enabled: true                         # needs config.backup.target.bucket (Q-HA-17)
  schedule: "0 2 * * *"
  timeZone: Etc/UTC
  suspend: false                        # rendered true while restore.enabled
  adoptTarget: false                    # adds --adopt-target after a restore (R-BAK-002)
  startingDeadlineSeconds: 3600
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  backoffLimit: 2                       # evictions are not counted (podFailurePolicy)
  activeDeadlineSeconds: 0              # 0 = not rendered
  terminationGracePeriodSeconds: 60
  tmpSizeLimit: 256Mi                   # the only volume (R-HA-061); the commands use at most 64 MiB (R-BAK-012), the rest keeps the kubelet's sizeLimit eviction away
  resources:
    requests:
      cpu: 500m
      memory: 256Mi
    limits:
      memory: 1Gi
  serviceAccount:
    create: true
    name: ""                            # <fullname>-backup
    annotations: {}                     # example (IRSA, read on the blob bucket, write on the target):
    #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-backup  # example
  nodeSelector: {}
  tolerations: []
  affinity: {}
  priorityClassName: ""
  podAnnotations: {}                    # cluster-autoscaler.kubernetes.io/safe-to-evict: "false" is always added (R-BAK-012)
  podLabels: {}
  extraEnv: []

restore:
  enabled: false                        # renders the restore Job, no migration Job, a suspended CronJob (Q-HA-18)
  backupId: ""                          # required with enabled; example: 20261028T020000Z-7f3a
  skipRevocationJournal: false          # only when the source bucket is gone (R-BAK-024, H-37)
  extraArgs: []                         # example: [--check] for a dry run of the preconditions (R-BAK-020)
  journalCredentials:
    source: runtime                     # runtime (the blob store's credential, a plain listing) | default (the identity of the Job's ServiceAccount lists object versions) | static (secrets.journalReader); anything but runtime keeps a deletion with the runtime credential from hiding a journal entry (R-BAK-015, Q-HA-159)
  backoffLimit: 2                       # evictions are not counted, every retry resumes (R-BAK-021)
  activeDeadlineSeconds: 0              # 0 = not rendered
  terminationGracePeriodSeconds: 60     # at least 30; time to stop at the next safe point (Q-HA-147)
  serviceAccount:
    create: true
    name: ""                            # <fullname>-restore
    annotations: {}                     # example (IRSA, read on the target, write on the new blob prefix, read on the source journal):
    #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-restore  # example
  resources:
    requests:
      cpu: "1"
      memory: 1Gi
    limits:
      memory: 2Gi
  podAnnotations: {}                    # safe-to-evict: "false" is always added, as for backup pods
  extraEnv: []

# ── Extension points ────────────────────────────────────────────
extraEnv: []                            # example: [{name: TROVE_LOG_LEVEL, value: debug}]
extraEnvFrom: []
extraVolumes: []                        # the operator's; the chart's volume check cannot see a claim's access mode
extraVolumeMounts: []
initContainers: []                      # run before trove starts
sidecars: []                            # native sidecars (restartPolicy: Always) allowed
lifecycle: {}                           # none; trove drains itself (R-HA-011)
extraObjects: []                        # extra manifests rendered with tpl
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

- **Replicas.** Defaults: 2 for `api`, 3 for `packages` and 2 for `worker` (Q-HA-16, Q-HA-75; before phase 6a 3 for
  `web`); at least 2 per role and pool with `highAvailability.enforce` (INV-12). The drain rule of R-HA-059 sets the real minimum: the
  replicas that carry the peak plus the PDB's `maxUnavailable`. When autoscaling is enabled, the
  Deployment's `replicas` field is omitted, so `helm upgrade` does not reset the autoscaler's
  choice, and `minReplicas` follows the drain rule.
- **Disruption budget.** One `policy/v1` PodDisruptionBudget per Deployment (every role, every pool) with
  `maxUnavailable: 1` and `unhealthyPodEvictionPolicy: AlwaysAllow` (R-HA-008). `maxUnavailable`
  scales with the replica count, whereas `minAvailable: 1` with one replica would block every
  drain. Backup and restore pods have none (R-HA-060).
- **Spreading.** Two topology spread constraints per Deployment (every role, every pool), rendered unless replaced
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
- **Autoscaling.** `autoscaling/v2` per Deployment: `api` and `packages` on CPU (default target 70 %; `minReplicas` 2 and
  3, `maxReplicas` 4 and 8), `worker` and each worker pool on CPU (80 %), optional memory and custom metrics. A pool has
  an autoscaler of its own from its overrides. Scale-down is slow (one pod per 2 minutes after a
  5-minute stabilization window), because every removed pod drains its uploads, as in a node drain.
  Scaling raises the database connection demand: the budget (R-HA-016) uses `maxReplicas`, so it
  must be raised before `maxReplicas` is, unless the transaction-mode pooler carries the main pool
  (R-HA-058).
- **Placement controls.** `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`,
  `schedulerName` are passed through unchanged, per role.

Acceptance criteria:

- On a three-node kind cluster with three zones labeled, three `packages` replicas land on three nodes
  and two `api` and two `worker` replicas on two each. `kubectl drain` of one node keeps at least two `packages` replicas
  and one `api` replica serving at all times, and the replacement of every evicted pod becomes ready on the remaining
  nodes. The same drain with `nodeTaintsPolicy: Ignore` leaves the `packages` replacement `Pending`,
  which documents why the value is set.
- With autoscaling enabled, a CPU load test of package downloads raises the `packages` replica count to `maxReplicas` while
  the `api` replica count stays at its minimum, and a following `helm upgrade` with unchanged values resets neither.

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
  role's or pool's `spool.sizeLimit` (`api.spool.sizeLimit`, `packages.spool.sizeLimit`, `worker.spool.sizeLimit`,
  `web.spool.sizeLimit` with `web.enabled`, falling back to `spool.sizeLimit`). With `spool.type: ephemeral` it is a generic ephemeral volume (a per-pod
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
  at least `storage.upload.max_size + headroom` for `packages` (validated, R-HA-029). An `api` pod spools only the UI's
  uploads through the content route of [api-and-ui.md](api-and-ui.md) R-API-025, bounded by
  `ui.deploy.max_upload_bytes` (Q-UI-106), so its spool needs that bound plus headroom. Remote-cache fills use the
  spool too (R-STOR-004). Example: 20 concurrent uploads of 500 MB, plus 2 GiB, needs about 12 GiB.
  A `worker` pod spools only the cache fills of jobs and, from P1, the `nuget-symbol-index` job,
  which spools one extracted PDB at a time ([formats/nuget.md](formats/nuget.md) R-NUGET-021), so
  its default is smaller.
- A `worker` spool also holds one advisory archive while a sync runs ([vulnerability-scanning.md](vulnerability-scanning.md)
  R-VULN-025), and so does the spool of a worker pool that claims the queue `vuln`: the chart refuses such a spool budget (size minus `spool.headroom`) below
  `config.vulnerabilities.sources.osv.max_archive_size` while the scanning is enabled and the OSV source is `online` or
  `repository`, and the failure names both values.
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
  The rest is headroom for non-heap memory. `GOMEMLIMIT=off` is an explicit setting too: no limit.
- **Admission follows the memory.** The same limit sizes what a `web` replica admits: memory slots
  and a heap gate in step 4 of the chain ([architecture.md](architecture.md) R-ARCH-005,
  Q-ARCH-240, Q-ARCH-328). A replica that the CPU cannot keep up with answers the excess `503 server_busy`
  instead of collecting requests until the memory runs out. The admission counts bytes: a request
  holds a download of a large object until its download knows its size and settles to what it holds,
  and the caches count by what they hold, so empty caches reserve nothing. `server.max_in_flight`
  stays an upper bound; with the chart's 2 GiB and the default caches full the 1,191 memory slots bind first
  (Q-ARCH-243, Q-ARCH-270), 2,228 with empty ones. The chart refuses a `web` pod
  without a memory limit and one whose limit, with the caches full, admits fewer than 64 requests (R-HA-029, Q-HA-70).
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
- With `TROVE_RUNTIME_MEMORY_LIMIT_BYTES` of 256 MiB, `go_gc_gomemlimit_bytes` is 85 % of it, and
  1,000 slow downloads held open leave the memory the runtime holds below 256 MiB
  (`test/integration/overload`, R-ARCH-005).
- On the dev profile, P-8's 1,000 requests/s of a 50 KiB tarball against one `web` pod whose CPU
  cannot serve them end without a restart of the pod; what it cannot serve is answered
  `503 server_busy` ([coverage/roadmap.md](../coverage/roadmap.md) Q-ROAD-10).

### R-HA-037 · Service, Ingress and TLS at the edge
Priority: **MVP** · Covers: HA-012, HA-022, HA-038, HA-039, HA-042

**Status.** Built (phase 6a, WP6a.3) for the Ingress ([ingress.yaml](../../deploy/helm/trove/templates/ingress.yaml) over the table of
[_edge.tpl](../../deploy/helm/trove/templates/_edge.tpl)); the bridge of R-HA-064 keeps one rule `/` to `<fullname>` for one step.
The HTTPRoute of R-HA-038 reads the same table and is not rendered yet (Q-HA-81).

- **One host, split by path prefix (HA-012, Q-HA-71).** All client traffic of the default endpoint (UI, API, package
  protocols, raw files, login) uses one host, one port 8080 and one certificate. The edge splits each host by path prefix
  between the Services of the roles that serve the route families ([endpoints.md](endpoints.md) R-EP-005,
  [architecture.md](architecture.md) R-ARCH-001). The rules are fixed, not values, and every one has `pathType: Prefix`,
  which Kubernetes and the Gateway API match by whole path elements (`/api` matches `/api` and `/api/npm/…`, never
  `/apix`), and the longest matching prefix wins:

  | Path prefix | Families | Service |
  |---|---|---|
  | `/` | `ui` (`GET /`, `/ui/…`, `/auth/…`), the npm exchange at the host root (`/-/npm/v1/oidc/…`, `ci_exchange`, P1), and every path no other rule matches | `<fullname>-api` |
  | `/api/v1` | `api`, and `ci_exchange`'s `POST /api/v1/auth/oidc/token` | `<fullname>-api` |
  | `/api` | `packages`: `/api/<format>/<projectKey>/<repoKey>/…` | `<fullname>-packages` |
  | `/files` | `files` | `<fullname>-packages` |
  | `/v2`, `/.well-known` | `packages`: the host-root routes reserved by INV-6 and the host-root bindings of [endpoints.md](endpoints.md) R-EP-006 | `<fullname>-packages` |

  The path pools of R-HA-062 add their more specific prefixes onto their own Services. A path the edge matches against a
  rule of the other role (an encoded segment that the router decodes into another family, for example) reaches a role that
  does not register its family and answers the `404` of an unknown path: the edge shares load, each role's router decides
  what it serves. One family is nested inside another's prefix: the npm trusted-publishing exchange below a registry URL
  (`/api/npm/<projectKey>/<repoKey>/-/npm/v1/oidc/token/exchange/package/<name>`, [formats/npm.md](formats/npm.md) R-NPM-022, P1) belongs
  to `ci_exchange` but matches `/api`. The chart renders a rule of its own for it when R-NPM-022 is built (Q-HA-72); until
  then the route does not exist, and its path answers `404` on either role.
- **Further endpoints.** Each further endpoint has its own port, its own Ingress or HTTPRoute with its own class, hosts,
  optional wildcard host and TLS Secret, and Services by role: `<fullname>-ep-<name>` and, when its surface holds families
  of both roles, `<fullname>-api-ep-<name>` ([endpoints.md](endpoints.md) R-EP-008, Q-EP-71). Its rules are the rows of the
  table above whose families its surface holds, each to the Service of the role that serves it; an endpoint whose surface
  lies within one role is routed whole (`/`) to that Service. `ui` is never on a further endpoint.
- There is no second route that bypasses anything, because no internal component sits behind the replicas. The operations
  port is never routed by the Ingress or the Gateway.
- **No session affinity anywhere (INV-12, INV-18, [architecture.md](architecture.md) R-ARCH-032).**
  Every Service the chart renders, the role Services, the Services of pools and of endpoints, keeps
  `sessionAffinity: None`, fixed and not a value, and the edge route sets no affinity cookie. The
  chart renders no cookie or client-IP affinity on the Ingress or the HTTPRoute, and its examples
  (R-HA-039) contain none; an operator who adds
  one through annotations or `extraObjects` gains nothing, because any replica of a role can answer any
  request of its families (R-HA-001). The edge proxy balances requests over the ready endpoints of each Service.
- **The whole host, never a sub-path.** trove owns the root of every host it is served on.
  Clients fix some routes at the host root (npm's `/-/npm/v1/` exchange prefix,
  [formats/npm.md](formats/npm.md) Q-NPM-1; `/v2/` for OCI, Q-HA-4), which INV-6 reserves next to
  `/auth/` and `/ui/`; the session cookie carries the `__Host-` prefix with `Path=/`; and
  `server.base_url` is an origin without a path
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-005). The split by prefix happens inside
  the host and never rewrites a path, so the rules of the table and the HTTPRoute matches (R-HA-038) are fixed and not
  values. `values.schema.json` declares `ingress` and `gateway` with `additionalProperties: false`, so
  `--set ingress.path=/trove` fails schema validation before anything renders (R-HA-029).
- **Load balancer (HA-022).** An Ingress controller or a Gateway API implementation (R-HA-038) is
  the load balancer. The Services are `ClusterIP` by default. `service.type: LoadBalancer` is allowed only together with the
  bridge of R-HA-064 or for a further endpoint whose surface lies within one role, because a cloud load balancer in front of
  one Service cannot split a host by path; clusters without an Ingress controller or Gateway route through one of the two.
  Plain HTTP to the internet is not a supported production setup.
- **TLS (HA-042).** TLS terminates at the edge: an Ingress TLS Secret, cert-manager through an
  annotation (`cert-manager.io/cluster-issuer`, [cert-manager Ingress](https://cert-manager.io/docs/usage/ingress/)),
  a Gateway listener, or a cloud load balancer. Traffic from the edge to the pods is HTTPS when
  `tls.enabled` is `true` (R-HA-063): the edge verifies the trove certificate against the chart's CA under the name
  `<fullname>.<namespace>.svc` for both role Services, the name the listener certificate carries (and that no Service
  needs to have). With `tls.enabled: false` it is plain HTTP inside the cluster (H-38). A service mesh with mutual TLS
  (Istio, Linkerd) or CNI encryption works transparently and closes H-38 at the platform level.
- `ingress.hosts` defaults to the host of `config.server.base_url`, so the URL clients use and the
  URL trove writes into metadata cannot diverge. A further endpoint's Ingress takes its hosts from
  that endpoint's `baseUrl`, `hosts` and `wildcardHost` the same way; a wildcard host needs a
  wildcard DNS record and a wildcard certificate in its TLS Secret ([endpoints.md](endpoints.md)
  R-EP-003, R-EP-004).
- ingress-nginx, the most common controller, was retired by the Kubernetes project in March 2026
  and receives no further fixes
  ([retirement announcement](https://kubernetes.io/blog/2025/11/11/ingress-nginx-retirement/)).
  The chart's Ingress is controller-neutral: it uses only `pathType: Prefix`, which every conformant controller supports.
  The chart renders an HTTPRoute from the MVP on (R-HA-038, Q-HA-7), and new installations should prefer it.

Acceptance criteria:

- With `ingress.enabled: true`, `className: haproxy` and a cert-manager issuer, `curl -sI https://trove.example.com/api/v1/system/ping`
  returns `200` with a certificate for the host and an `X-Trove-Replica` of an `api` pod, and
  `curl -sI -H "Authorization: Bearer $T" https://trove.example.com/api/npm/web/npm-all/lodash` one of a `packages` pod.
- `curl -s https://trove.example.com:8081/metrics` and any Ingress path to the operations port
  fail. The Ingress manifest contains no rule for port 8081.
- `helm template … --set ingress.enabled=true --set ingress.path=/trove` fails schema validation
  and names `path`. Every rendered Ingress rule has `pathType: Prefix`, the rules of each host are exactly the rows of the
  table (plus the prefixes of the path pools), and each names the Service of its row; the chart test asserts the same for
  the HTTPRoute.
- During the e2e set with every role deployed, the access log of no `api` pod records a request below `/api/<format>/` or
  `/files/`, and the access log of no `packages` pod records one below `/ui/`, `/auth/` or `/api/v1/`.

### R-HA-038 · Gateway API exposure
Priority: **MVP** · Covers: HA-040

**Status.** Rendered for the default endpoint ([httproute.yaml](../../deploy/helm/trove/templates/httproute.yaml), render test
`TestR_HA_038_GatewayAPIExposure`), reading the table the Ingress reads ([_edge.tpl](../../deploy/helm/trove/templates/_edge.tpl)); the BackendTLSPolicies of
`tls.backendTLSPolicy` name one Service each (Q-HA-81, Q-HA-85). Not rendered: the HTTPRoute of further endpoints (R-EP-008) and the
Gateway of `gateway.create` (R-HA-053). `gateway.parentRefs` is required with `gateway.enabled`. The criteria with Envoy Gateway in kind are
not covered by the render test.

- With `gateway.enabled: true` the chart renders an `HTTPRoute` (`gateway.networking.k8s.io/v1`).
  It attaches to `gateway.parentRefs`, with `hostnames` defaulting to the base URL's host (plus the
  content host of R-HA-055 when set), one rule per row of R-HA-037's prefix table with a `PathPrefix` match to the Service
  of its role (fixed, not values; one rule `PathPrefix /` to `<fullname>` with the bridge of R-HA-064), the path pools'
  rules of R-HA-062, and on every rule `timeouts.request` / `timeouts.backendRequest` and `gateway.filters` from the values.
  The Gateway API orders `PathPrefix` matches by length, so the more specific prefix wins as in an Ingress. With
  `tls.backendTLSPolicy.enabled`, one BackendTLSPolicy per Service the HTTPRoute names. HTTPRoute timeouts are part of the Standard channel since Gateway API v1.2
  ([GEP-1742](https://gateway-api.sigs.k8s.io/geps/gep-1742/)), so v1.2 CRDs are the minimum.
  `backendRequest` MUST NOT exceed `request`.
- `gateway.enabled: true` with an empty `gateway.parentRefs` is refused at render time (R-HA-029): an HTTPRoute without a parent attaches
  to no Gateway and would route nothing, so the chart names the missing value instead of rendering an object that does nothing
  (`trove: gateway.parentRefs is required with gateway.enabled: the Gateway the HTTPRoute attaches to`).
- The HTTPRoute is MVP (Q-HA-7). It attaches to an existing, platform-owned Gateway. A Gateway
  rendered by the chart is P1 (R-HA-053).
- Ingress and HTTPRoute may be enabled at the same time, for migrating between them with a DNS
  switch and an instant way back.
- Body size, request buffering and slash handling are not part of the Gateway API. They are set
  through implementation-specific policies (R-HA-039) that the chart does not render. `extraObjects`
  can carry them.

Acceptance criteria:

- With Envoy Gateway in kind and the policies of R-HA-039, `npm publish` of a 200 MB package and
  `npm view @acme/ui` (scoped name, encoded slash) work through the HTTPRoute, reaching `packages` pods, and the UI loads
  from `api` pods.
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

- `curl -T 6gib.bin https://trove.example.com/files/web/generic-local/big.bin` succeeds. 6 GiB is
  above the 5 GiB single-request limit of S3, so the multipart path is covered as well.
- `curl --limit-rate 2M -o /dev/null https://trove.example.com/files/web/generic-local/big.bin` runs
  longer than 15 minutes and completes.
- `curl -s https://trove.example.com/api/npm/web/npm-all/@acme%2fui` returns the packument.
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
  - adds a rule for that host to the Ingress (path `/`, `pathType: Prefix`, R-HA-037) and an HTTPRoute rule for the
    host (R-HA-038), both to `<fullname>-packages:8080`, because the content host serves only the families `packages` and
    `files` (`<fullname>:8080` with the bridge of R-HA-064). There is no further Service, port or Deployment: the replica
    tells the hosts apart (R-ARCH-029, with the host resolved by R-ARCH-006);
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
  renders one Ingress with the path rules of R-HA-037 for the main host and one rule `/` to `<fullname>-packages:8080` for
  the content host, a TLS entry with
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
  rules. The project key and the repository key are the first two path segments after `/v2/`
  (`docker pull trove.example.com/web/docker-all/library/alpine:3`), as below `/api/<format>/`
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-002). The OCI format page specifies this.
- Subdomain-per-repository routing (`docker-all.registry.example.com`) is not offered: a host label cannot name a
  project and a repository key, and the binding of a host label to a repository is gone
  ([endpoints.md](endpoints.md) [Q-EP-76](endpoints.md#q-ep-76--a-host-root-binds-a-repository-by-project-and-key-and-the-label-binding-goes)).
  A Docker Engine mirror is an endpoint host bound to one repository by its project and key
  (`host_roots: {mirror.example.com: web/docker-hub}`, [endpoints.md](endpoints.md) R-EP-006, R-EP-007). The OCI page
  specifies the registry side.
- The chart renders no rewrite rules or snippets. This avoids the catalog's mismatch between edge
  rewrites and the server's routing method, where pushes break while pulls work.

Acceptance criteria (when the OCI format exists):
`docker login trove.example.com && docker push trove.example.com/web/docker-local/test:1` through
the chart's Ingress succeeds without any Ingress annotation beyond R-HA-039's.

### R-HA-042 · Network policies
Priority: **MVP** · Covers: HA-054

**Status.** Built (phase 6a, WP6a.3): one policy per Deployment of the cut and the migration Job
([networkpolicy.yaml](../../deploy/helm/trove/templates/networkpolicy.yaml)). Enforcement on the cluster's CNI is verified on
the kind profile's CNI only (H-41).

The owner's rule ([ADR-154](../decisions.md#adr-154--network-policies-are-ingress-rules-between-the-charts-pods-each-with-its-port),
2026-11-04): trove writes only ingress rules, only for the pods the chart deploys and between them, and every rule names its
specific port. The chart renders no egress rule and no policy for pods it does not deploy.

With `networkPolicy.enabled: true` (default) the chart renders one NetworkPolicy per Deployment, named as the Deployment and
selecting its pods by the selector labels of R-HA-028, with `policyTypes: [Ingress]` and one rule per port the pods open:

| Policy | Port 8080 | Port of a further endpoint | Port 8081 |
|---|---|---|---|
| `<fullname>-api`, `<fullname>-packages`, each of their path pools | from `networkPolicy.ingress.http.from` (empty means from anywhere, so the edge works wherever it runs) | from that endpoint's `networkPolicy.from`, for each further endpoint without a pool whose surface holds a family of the role | from `networkPolicy.ingress.ops.from` and `networkPolicy.probeCIDRs` |
| each endpoint pool (`<fullname>-packages-ep-<name>`, `<fullname>-api-ep-<name>`) | none: an endpoint pool opens no 8080 (R-EP-002) | from the endpoint's `networkPolicy.from` | as above |
| `<fullname>-worker`, each worker pool | none: a worker listens on no client port (R-HA-001) | none | as above |
| `<fullname>-web` and `<fullname>-web-ep-<name>` (bridge of R-HA-064) | as `api` and `packages` | as `api` and `packages` | as above |
| `<fullname>-migrate`, `<fullname>-backup`, `<fullname>-restore` | none | none | none: `ingress: []` |

- Every rule names its port, and every rule with a port carries the `from` of its row. A row whose `from` list is empty
  for 8081 (neither an `ops.from` peer nor a `probeCIDRs` entry) leaves the 8081 rule out rather than rendering an empty
  `from`, which Kubernetes reads as "from anywhere". `networkPolicy.ingress.http.from` and an endpoint's
  `networkPolicy.from` keep their meaning: empty admits the port from anywhere, so the edge works without a value.
- No rule admits one trove pod from another: replicas never open connections to each other (Topology), and the cut adds no
  seam between the roles ([ADR-153](../decisions.md#adr-153--trove-is-cut-into-task-areas-roles-of-one-binary-network-seams-only-at-trust-or-load-boundaries)).
  A seam that a later decision adds is a rule of the receiving role's policy that admits the sending role on the seam's
  port (ADR-154).
- In-cluster callers that address a role's Service directly (R-HA-002), such as a Bastion in another namespace, reach
  port 8080 only when `networkPolicy.ingress.http.from` admits them; with a restricted list, their namespace or pods are
  added to it.
- **Egress.** None. The chart renders no egress rule, has no `networkPolicy.egress` values and no example file for one. What
  separates trove's outbound connections is the address policy of
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-038, which every role applies in code, and beyond it the
  cluster operator's own policy; the gap is [H-516](../security/egress.md#h-516--the-chart-restricts-no-egress-of-its-pods).
  An operator who restricts egress at the cluster lets each role reach what it uses: every serving role and the migration
  Job DNS and PostgreSQL (`database.host`, `session_host`, the pooler of R-HA-058); every serving role Valkey and its
  Sentinels and the object store; `packages` the upstreams of its remote repositories and the shadowing probes; `api` the
  OIDC issuers and the upstreams it tests (R-REPO-024); `worker` the upstreams of mirrored remotes, the advisory sources of
  [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-025 and the CI issuers' key endpoints; backup and restore
  the backup target.
- **Valkey and the Sentinels admit only trove's pods** ([architecture.md](architecture.md) H-49).
  Whoever can write under trove's key prefix can deny service, so the Valkey data nodes and the
  Sentinels MUST be reachable only from trove's serving pods (`api`, `packages`, `worker`, `web`), from each other and from
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
                - {key: app.kubernetes.io/component, operator: In, values: [api, packages, worker, web]}
          - podSelector:
              matchLabels:
                app.kubernetes.io/name: trove-valkey  # example: replication and Sentinel traffic
        ports:
          - {port: 6379}
          - {port: 26379}
  ```

  The local profile applies this policy to its Valkey (R-HA-051).
- Kubelet probes: most CNIs exempt node-to-pod probe traffic from policies. Where one does not,
  `probeCIDRs` adds the node CIDRs to the 8081 rule.

Acceptance criteria:

- The chart test renders, for the default values, exactly the policies `trove-api`, `trove-packages`, `trove-worker` and
  `trove-migrate`; `trove-api` and `trove-packages` have one rule for TCP 8080 without `from` and one for TCP 8081 from the
  namespace `monitoring`; `trove-worker` only the 8081 rule; `trove-migrate` `ingress: []`; no policy has
  `policyTypes: [Egress]` or an `egress` key, and every rule names a port. With the `customers` endpoint of R-EP-008
  (surface `[packages, files, api]`, no pool), `trove-packages` and `trove-api` each gain a rule for 8443 from
  `endpoints.customers.networkPolicy.from`; with its pool enabled, `trove-packages-ep-customers` and `trove-api-ep-customers`
  carry the 8443 and 8081 rules and no 8080 rule. With a worker pool `vuln`, `trove-worker-vuln` carries the 8081 rule only.
- From a pod in namespace `default`, `curl https://trove-ops.trove:8081/metrics` times out, and
  `curl https://trove-api.trove:8080/api/v1/system/ping` succeeds; a connection to a `worker` pod's 8080 is refused or times
  out.
- In the local profile, a connection to the Valkey and Sentinel ports from a pod in namespace
  `default`, and from the restore Job's pod, times out, while every `api`, `packages` and `worker` pod connects.

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

**Status.** The PrometheusRule is rendered by the main chart with `metrics.prometheusRule.{enabled,labels,disabledAlerts,extraRules}`
(`disabledAlerts` refuses a name that is no alert), checked by `promtool check rules` and tested by `promtool test rules`
([deploy/helm/trove/tests/rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml), `make chart-test`), one test in which the alert fires and one in
which it stays silent per alert. It carries the three alerts of the vulnerability scanning ([vulnerability-scanning.md](vulnerability-scanning.md)
R-VULN-024, Q-VULN-55): `TroveVulnerabilityDataStale`, `TroveVulnerabilitySyncFailing` and `TroveVulnerabilityClaimedNameMalicious`; the two of the
mirror mode (R-OPS-046); `TroveBridgeLeftOn` (R-HA-064); `TroveBlobMissing` (severity `critical`, summed over the replicas, [storage.md](storage.md)
Q-STOR-152); and these sixteen rows of the table below: `TroveReplicasNotReady`, `TroveDatabaseUnreachable`, `TroveObjectStoreUnreachable`,
`TroveObjectStoreUnreachableSingleReplica`, `TroveDatabasePoolSaturated`, `TroveControlConnectionWaits`, `TroveJobsStuck`, `TroveLeaseLost`,
`TroveMixedVersions`, `TroveSpoolNearlyFull`, `TroveGCStale`, `TroveValkeyUnavailable`, `TroveValkeyUnavailableSingleReplica`,
`TroveValkeySentinelsDegraded`, `TroveValkeyFailoverFlapping` and `TroveValkeyPrimaryDisagreement`. The ServiceMonitor is rendered
([servicemonitor.yaml](../../deploy/helm/trove/templates/servicemonitor.yaml), render test `TestServiceMonitor`). The rule scopes its selectors by
`namespace`, which every scrape through the Prometheus operator carries. The binary exports every metric these rows read under the name of their
expression (Q-HA-135).

Every alert of the table is rendered and tested (phase 7): `TroveBackupCheckFailed` (R-HA-023, the stored report of the task `backup-check`), `TroveRevocationJournalBacklog` (R-HA-054) and the six alerts of the backup runs, `TroveBackupStale`, `TroveBackupFailed`,
`TroveBackupRunningLong`, `TroveBackupGlobalHoldLong`, `TroveBackupSourceDamage` and `TroveBackupVerifyFindings`
([backup-and-restore.md](backup-and-restore.md) R-BAK-014), each with a rule test in which it fires and one in which it stays silent. The
two gauges that have no series until the event happened (Q-OPS-180) are handled in the expressions: `TroveBackupFailed` reads the last success
as `or vector(0)`, and `TroveBackupStale` has a clause for a backup that never succeeded ([Q-OPS-182](operations-and-observability.md#q-ops-182--the-clause-of-trovebackupstale-for-a-backup-that-never-succeeded-reads-its-window-from-the-chart)).

`TroveBackupCheckFailed` reads `trove_backup_check_ok{check}`, which every replica exports from the report that the task `backup-check` stored in
`system_state` ([Q-OPS-185](operations-and-observability.md#q-ops-185--the-gauge-of-the-platform-check-is-read-from-the-stored-report-by-every-replica));
a check that is not declared or not readable has no series. The acceptance criteria
below that need a Prometheus scraping a running profile (object store stopped, Valkey stopped, the suspended backup, the interrupted backup run)
are not covered by a test: the rule tests prove the expressions against given series, not that a stopped component produces those series.

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
  `role` from `app.kubernetes.io/component` (`api`, `packages` or `worker`, and `web` in the bridge of R-HA-064;
  Q-OPS-161), and `pool` from the label
  `trove/pool` (R-HA-028), which carries the pool's name on the pods of each entry of `api.pools`, `packages.pools`
  and `worker.pools` (R-HA-062), `ep-<name>` on the pods of an endpoint pool, and is empty on the main Deployments, so their
  series carry no `pool`. `metrics.serviceMonitor.relabelings` is appended after these two:

  ```yaml
  relabelings:
    - sourceLabels: [__meta_kubernetes_pod_label_app_kubernetes_io_component]
      targetLabel: role
    - sourceLabels: [__meta_kubernetes_pod_label_trove_pool]
      targetLabel: pool
  ```
- **PrometheusRule.** Default alerts; names are stable and individually disabled with
  `disabledAlerts`:

| Alert | Expression (sketch) | For | Severity |
|---|---|---|---|
| `TroveReplicasNotReady` | `min by (deployment) (kube_deployment_status_replicas_available{deployment=~"<fullname>-(api\|packages\|worker\|web)(-.*)?"}) < 2`, per Deployment, so one role or pool without replicas fires while the others are healthy (the Deployments are `<fullname>-<role>` and `<fullname>-<role>-<pool>` for the roles `api`, `packages`, `worker` and the bridge `web`, R-HA-062, R-HA-064; needs kube-state-metrics) | 5m | critical |
| `TroveDatabaseUnreachable` | `sum(rate(trove_db_retries_total{kind="connection"}[5m])) > 0 and sum(rate(trove_http_requests_total{code="503"}[5m])) > 0` | 2m | critical |
| `TroveObjectStoreUnreachable` | `min(trove_storage_s3_circuit_open) == 1` (all replicas) | 2m | critical |
| `TroveObjectStoreUnreachableSingleReplica` | `max(trove_storage_s3_circuit_open) == 1 and min(trove_storage_s3_circuit_open) == 0` | 5m | warning |
| `TroveDatabasePoolSaturated` | `histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{db_pool="request"}[5m])) > 0.1`; the control connection's waits are the next row's ([operations-and-observability.md](operations-and-observability.md) R-OPS-046) | 10m | warning |
| `TroveControlConnectionWaits` | `histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{db_pool="control"}[5m])) > 1`: readiness-critical loops wait for the control connection (Q-HA-20), before `invalidation` and `database` start failing on every replica (R-OPS-046) | 5m | warning |
| `TroveJobsStuck` | `max(trove_jobs_oldest_queued_age_seconds) > 900` | 5m | warning |
| `TroveLeaseLost` | `increase(trove_lease_lost_total[15m]) > 3` | 0m | warning |
| `TroveMixedVersions` | `count(count by (version) (trove_build_info)) > 1` | 2h | warning |
| `TroveSpoolNearlyFull` | `max(trove_spool_used_bytes / (trove_spool_used_bytes + trove_spool_free_bytes)) > 0.9` | 10m | warning |
| `TroveBlobMissing` | `increase(trove_storage_blobs_damaged_total{status="missing"}[1h]) > 0` | 0m | critical |
| `TroveGCStale` | `time() - max(trove_gc_last_success_timestamp_seconds) > 172800` | 1h | warning |
| `TroveBackupCheckFailed` | `min(trove_backup_check_ok) == 0` | 1h | critical |
| `TroveRevocationJournalBacklog` | `max(trove_revocation_journal_pending) > 0` (R-HA-054) | 15m | warning |
| `TroveValkeyUnavailable` | `max(trove_valkey_available) == 0` (every replica degraded, R-HA-057) | 2m | critical |
| `TroveValkeyUnavailableSingleReplica` | `min(trove_valkey_available) == 0 and max(trove_valkey_available) == 1` | 5m | warning |
| `TroveValkeySentinelsDegraded` | `min(trove_valkey_sentinels_reachable) < 2` (no majority of three, R-HA-056; the gauge counts the answers to a probe of every configured Sentinel, R-HA-057) | 5m | warning |
| `TroveValkeyFailoverFlapping` | `max(increase(trove_valkey_primary_changes_total[1h])) > 2` (failovers repeating, [operations-and-observability.md](operations-and-observability.md) Q-OPS-15) | 0m | warning |
| `TroveValkeyPrimaryDisagreement` | `max(trove_valkey_primary_views) > 1`: replicas name different primaries, for example one still bound to a demoted primary (Q-OPS-15, R-ARCH-031) | 5m | critical |
| `TroveBackupStale` | `time() - max(trove_backup_last_success_timestamp_seconds) > 1.5 * max(trove_backup_expected_interval_seconds)`, or `trove_backup_last_success_timestamp_seconds` absent for longer than `backup.expected_interval` ([backup-and-restore.md](backup-and-restore.md) R-BAK-014). Renamed from `TroveBackupTooOld` | 0m | critical |
| `TroveBackupFailed` | `max(trove_backup_last_failure_timestamp_seconds) > max(trove_backup_last_success_timestamp_seconds)` (R-BAK-014). Not `kube_job_status_failed > 0`, which keeps firing for as long as `failedJobsHistoryLimit` keeps a failed Job, also after later runs succeeded; an `interrupted` run does not count | 0m | critical |
| `TroveBackupRunningLong` | `max(trove_backup_running_seconds) > 0.5 * max(trove_backup_expected_interval_seconds)` (R-BAK-014) | 0m | warning |
| `TroveBackupGlobalHoldLong` | `max(trove_backup_gc_hold{mode="all"}) == 1` (R-BAK-014) | 30m | warning |
| `TroveBackupSourceDamage` | `max(trove_backup_source_blobs_damaged) > 0` (R-BAK-014) | 0m | critical |
| `TroveBackupVerifyFindings` | `max(trove_backup_last_verify_findings) > 0` (R-BAK-014) | 0m | critical |

Metric names come from [operations-and-observability.md](operations-and-observability.md) R-OPS-008
and [storage.md](storage.md); `trove_storage_blobs_damaged_total{status}` is storage.md's
(R-STOR-027), counted by every detection of a missing or corrupt blob. The spool gauges are `trove_spool_used_bytes` (the budget that
running uploads hold, R-STOR-004) and `trove_spool_free_bytes` (the free space of the spool volume, read at every reservation and release);
`trove_gc_last_success_timestamp_seconds` has no series on a replica until a collection run there ended without error, neither paused nor
a dry run, so a new installation does not report a time of 0 and `TroveGCStale` stays silent until the first run. This page adds
`trove_db_retries_total`, `trove_db_pool_resets_total`, `trove_db_commit_unknown_total`,
`trove_lease_lost_total`, `trove_backup_check_ok`, `trove_revocation_journal_pending` and the
Valkey metrics of R-HA-057. The `trove_backup_*` gauges are
[backup-and-restore.md](backup-and-restore.md)'s (R-BAK-014); every `web` and `worker` replica
reads them from the run records in PostgreSQL, because a finished backup pod cannot be scraped. The
backup thresholds come from `backup.expected_interval` through the gauge
`trove_backup_expected_interval_seconds`, the same value that drives the API's `expectedIntervalSeconds`
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
- Every series scraped from a pod of a `packages` pool carries `role="packages"` and `pool="<name>"`; series of
  the main `packages` Deployment carry `role="packages"` and no `pool`; series of the main `api` Deployment carry
  `role="api"`, and of a worker pool `role="worker"` and its `pool`.

### R-HA-046 · Extension points
Priority: **P1** · Covers: HA-048, HA-049, HA-050, HA-051

- `extraEnv` and `extraEnvFrom` (the containers of the Deployments; the backup and restore Jobs take only their own
  `backup.extraEnv` and `restore.extraEnv`, like the migration Job, Q-HA-149), `web.extraEnv`, `worker.extraEnv`,
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

### R-HA-047 · Images, private registries, air-gapped installation and attestations
Priority: **P1** · Covers: HA-056

- Images are referenced as `<global.imageRegistry or image.registry>/<image.repository>` with
  `@<digest>` when set, otherwise `:<tag or appVersion>`. `global.imagePullSecrets` and
  `imagePullSecrets` apply to all pods, including the hook Jobs and the backup CronJob.
- Images are multi-architecture (`linux/amd64`, `linux/arm64`).
- **Publishing location (Q-HA-11, owner decision of 2026-10-29).** The release pipeline,
  [build.yml](../../.github/workflows/build.yml) ("Release Docker & Helm"), runs on every published
  release and publishes to two places:
  - image `docker.io/guidedtraffic/trove` on Docker Hub, with the tags `<version>`, `<major>.<minor>`,
    `<major>` and the commit's `sha` tag of the release (`latest` belongs to pushes to `main`);
  - chart in the chart repository `https://guided-traffic.github.io/trove/`, served from the
    `gh-pages` branch: the release adds its package `trove-<version>.tgz` and the merged `index.yaml`.

  Install the chart from its repository (`<v>` is the chart version):

  ```shell
  helm repo add trove https://guided-traffic.github.io/trove/
  helm install trove trove/trove -n trove -f values.yaml --version <v>   # <v>: example 0.1.0
  ```

  The chart's defaults are `image.registry: docker.io` and `image.repository: guidedtraffic/trove`
  ([values.yaml](../../deploy/helm/trove/values.yaml)). In the packaged chart the release job sets
  `image.tag` to the release version. Anonymous pulls from Docker Hub are subject to its rate limits
  (Q-HA-11, option (d)). Mirrors are the operator's (air-gapped, below).
- **Attestations, no signatures (owner decision of 2026-10-29).** The image index of each release
  carries a BuildKit SLSA provenance attestation and an SBOM attestation (`provenance: true` and
  `sbom: true` in build.yml). The release also carries the image's SPDX SBOM as an asset,
  `guidedtraffic-trove_*.spdx.json`. Docker Scout scans the image; its report goes to the job summary
  and to `scout-report.sarif`. The workflow sets no `exit-code` input; the action's default for it is not
  verified here.
  The chart has neither a signature nor an attestation: `helm package` runs without `--sign`, and the
  pipeline makes no chart SBOM or provenance. Nothing is signed, and nothing in the cluster checks an
  attestation. To read the provenance of a release:

  ```shell
  docker buildx imagetools inspect guidedtraffic/trove:<v> --format '{{ json .Provenance }}'   # <v>: example 0.1.0
  ```

  An attestation states what the build claims. It is not a signature, so it does not show who made the
  claim. See [H-79](#h-79--artifacts-are-unsigned-and-nothing-checks-their-origin).
- **Air-gapped.** Mirror the chart (`helm pull trove/trove --version <v>`) and the image by digest
  (`crane copy`, `skopeo copy --all`) into the internal registry, then set `global.imageRegistry`.
  The chart pulls no other image. No init container image, no bundled database or Valkey, no proxy
  image. Copy the image index as a whole, so that its attestation manifests come along. Whether a
  given mirror keeps them is not verified here.

Acceptance criteria:

- `helm template … | grep -E 'image:' | sort -u` lists exactly one image.
- The image index of a release carries a provenance and an SBOM attestation, and the release has its
  SPDX file `guidedtraffic-trove_*.spdx.json` attached.
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

- **After the cut into roles** (phase 6a, Q-HA-75) the `web` columns are those of the `packages` Deployment, and every
  profile adds `api` with 2 replicas (HPA 2–4 in medium and large), 500m / 512Mi requests, a 1Gi memory limit and a 5Gi
  spool; `worker` keeps its column, and a worker pool for the queue `vuln` (R-HA-062) is recommended from medium on.
- `max_connections` follows the R-HA-016 formula with `maxReplicas`, `maxSurge: 1`, the default
  lease pool, a backup Job of 4 connections (R-BAK-005) and 15 more (2 migration, 10 reserved, 3
  superuser). Before the cut: small 7 × 10 + 3 × 4 + 19 = 101 → 150; medium 14 × 20 + 5 × 4 + 19 = 319 → 400; large,
  through the pooler, 100 + 7 × 4 + 19 = 147 → 200, without it 767. With `api` (phase 6a): small (4 + 3 + 3) × 10 + 12 + 19
  = 131 → 150; medium (9 + 5 + 5) × 20 + 20 + 19 = 419 → **500**; large through the pooler unchanged at 147 → 200, without it
  917.
- The minimum replica counts follow the drain rule of R-HA-059: each profile's peak is assumed to
  fit into one `packages` (before the cut: `web`) replica fewer than its minimum, and the UI and management load into one
  `api` replica. The reference load test replaces that
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
two `api`, three `packages` and two `worker` replicas and a worker pool of the queue `vuln` with two (before phase 6a: two `web` and two `worker`; [Q-HA-87](#q-ha-87--the-dev-profiles-sizing-of-the-roles-and-its-postgresql-connection-budget)), PostgreSQL with a
standby, Valkey with a replica and three
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
| `deploy/dev/values-dev.yaml` | Chart values: `web` and `worker` 2 replicas each (phase 6a: `api` 2, `packages` 3, `worker` 2 and the pool `vuln` 2, Q-HA-87), `highAvailability.enforce: true`, `spool.sizeLimit: 4Gi`, `forcePathStyle: true`, static S3 credentials, `base_url: http://localhost:8080`, the CloudNativePG Secrets, the three Sentinel addresses with `primary_name: trove`, the Valkey Secrets, and the backup target `trove-backups` on the same SeaweedFS endpoint with a schedule every 6 hours |

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
kubectl -n trove port-forward svc/trove-api 8080:8080   # phase 6a; svc/trove before it; package clients use the edge or svc/trove-packages
```

- The CloudNativePG application Secret `trove-db-app` and the CA Secret of the cluster are used
  directly (`secrets.database`, `secrets.databaseCA`), so the dev profile exercises TLS with
  `verify-full` like production. The host is the cluster's read-write Service
  ([quickstart](https://cloudnative-pg.io/docs/1.30/quickstart), [bootstrap](https://cloudnative-pg.io/docs/1.30/bootstrap)).
- Valkey runs TLS-only with `verify-full` as well; the dev CA lives in `.dev/`, which is ignored by
  Git and recreated by `dev-up`.
- Further targets:
  - `dev-down` deletes the kind cluster;
  - `dev-failover` does what `kubectl cnpg promote trove-db <standby>` does (it sets `status.targetPrimary`), once the operator holds
    its lease, the cluster is settled and a ready standby runs on a schedulable node, and waits until the new primary serves
    `trove-db-rw` (Q-HA-126);
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
  The MinIO leg of that check is the job `s3-conformance` of
  [Test and Release](../../.github/workflows/release.yml). It builds MinIO from that source at the
  commit `MINIO_COMMIT` of [tools/versions.env](../../tools/versions.env), serves it over TLS only, and
  runs on every change (owner decision of 2026-10-29; the leg ran nightly before). The build receives
  no security fixes, and the job runs on the organisation's self-hosted runners like every other job;
  [H-80](#h-80--self-hosted-runners-build-the-code-of-every-pull-request) records what that means.
- **AWS S3.** The AWS S3 leg is not in the pipeline. It needs an AWS account and a CI role, which the
  repository does not have. Until the owner provides them, no job runs R-STOR-035 against AWS S3, and
  no run reports AWS S3 as passed ([Q-HA-25](#q-ha-25--aws-leg-of-the-nightly-run-when-no-aws-account-is-configured)).
- The break-glass password is printed once by the target (`kubectl get secret … | base64 -d`).
  OIDC is optional in dev. An optional Dex overlay belongs to
  [identity-and-authentication.md](identity-and-authentication.md).
- Running the binary outside the cluster (`trove serve --role all` from an IDE, R-HA-001) uses the
  same dependencies through `kubectl port-forward`; it is a convenience, not a tested profile.

Acceptance criteria:

- On a machine with Docker and the pinned tools, `dev-up` completes in under 15 minutes and
  `npm ping --registry http://localhost:8080/api/npm/web/npm-all/` succeeds.
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
| Pod crash | `kubectl delete pod --grace-period=0 --force` of a random `web` or `worker` replica every 2 min: 10 times (20 min) nightly and in the closing run, 3 times in `make e2e SET=npm-ha-crash` | No client iteration fails after its own retries; no item row without a durable object (`trove admin storage verify`; until that command exists, every version the scenario's publishes left is downloaded against its integrity, [Q-HA-69](#q-ha-69--the-storage-check-of-the-pod-crash-before-trove-admin-storage-verify-exists)) |
| Rolling restart | `kubectl rollout restart` of `deploy/trove-api`, `deploy/trove-packages`, then of `deploy/trove-worker` (before phase 6a: `deploy/trove-web`) | Zero failed iterations |
| Rolling upgrade | Previous minor → current with the migration Job | Zero failed iterations; R-HA-020 verification passes |
| Rollback | `helm rollback` after the upgrade | Zero failed iterations |
| PostgreSQL switchover | `kubectl cnpg promote` | Zero failed iterations |
| PostgreSQL primary loss | Force-delete the primary pod | Failures only within `retry.maxElapsed`, all `503`; recovery without intervention |
| PostgreSQL partition of one replica | NetworkPolicy denying egress to PostgreSQL for one pod | That pod unready within 15 s; no failed iteration overall; its leases move |
| PostgreSQL outage | Scale the database to 0 instances for 2 min (hibernation) | No container restart; full recovery within 30 s of return |
| Object-store outage | The S3 store scaled to 0 and kept stopped for 30 s and for 5 min, counted from the return of the stop ([Q-STOR-150](storage.md#q-stor-150--how-long-the-outage-test-keeps-the-store-stopped-and-what-a-k6-failure-inside-it-is)); during the 5-minute run one replica is deleted | Metadata, UI and API succeed throughout; blob transfers answer `503` with `Retry-After` (R-STOR-036); no running replica turns unready (R-HA-008); the replacement pod started during the outage stays not ready with its container restart count unchanged, because the startup check retries without exiting (R-STOR-035), and becomes ready within 60 s after the store returns; full recovery within `storage.s3.circuit_breaker.open_duration` + 10 s. The store keeps its objects over the outage ([Q-STOR-147](storage.md#q-stor-147--the-dev-profiles-object-store-keeps-its-objects-over-an-outage)); an `npm` command that overlaps the outage may fail, with the `503`, and every other one exits `0` ([Q-STOR-148](storage.md#q-stor-148--what-the-npm-loops-may-see-of-an-object-store-outage)) |
| Node drain | R-HA-059's drain test, variants A to E | R-HA-059's pass conditions |
| Node loss | `docker stop` of a kind worker node that hosts an `api`, a `packages` and a `worker` pod and no primary and no operator ([Q-HA-104](#q-ha-104--the-node-of-the-node-loss-scenario)) | Replacement pods ready within `nodeFailureTolerationSeconds` + 60 s of the node being declared lost ([Q-HA-103](#q-ha-103--the-node-loss-bound-is-counted-from-the-moment-the-cluster-declares-the-node-lost)); no duplicate singleton work (the lease sessions of R-HA-006); a client command that was running at the loss may fail with a network error, every later one exits `0` ([Q-HA-105](#q-ha-105--what-the-npm-loops-may-see-of-a-lost-node)) |
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
  variant A of the drain test run every night and on dispatch in the workflow HA and Performance, not on every change to
  `main` (owner, 2026-11-01): the pod crash in its 20-minute form, the rest as before
  ([Q-HA-67](#q-ha-67--where-and-how-often-the-scenarios-under-npm-load-run)). A red night is recorded as an open finding and
  does not hold a release. The full suite, the pooler profile and
  the trove-backup restore drill run in a green full run of Test and Release on the commit that closes
  the phase. The point-in-time restore drill has no run in the pipeline.

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
  away from other applications' data. An
  ingress NetworkPolicy on the Valkey data nodes and Sentinels that admits only trove's `api`, `packages` and
  `worker` pods (`web` in the bridge), which whoever runs Valkey adds, keeps other workloads from writing under the
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
- **Supply chain.** The image carries provenance and SBOM attestations and is not signed; the chart
  carries neither. Installing by digest (`image.digest`) is the operator's choice in production, because
  the chart defaults to a tag. Nothing verifies either artifact in the cluster (R-HA-047, H-79).
- **Restore is a security event.** It reverts revocations (H-37), so post-restore and the restore
  of a trove backup record an audit event, print the warning and re-apply the revocation journal
  (R-HA-025, R-HA-054, R-HA-060). Replay applies removals and only those updates that grant nothing
  beyond the object's current state, so whoever can write to the bucket can at worst revoke access
  through the journal, never grant any. A restore also moves the installation to a new Valkey
  keyspace, so no lock, negative-cache entry or rate-limit counter of the lost state is read again
  (R-HA-057, rule 7).
- **The chart restricts no egress.** By the owner's rule (ADR-154) it renders ingress rules only, so a compromised
  trove process can reach every address the pod network reaches, including cloud metadata
  endpoints and in-cluster services, unless the cluster operator's own policy restricts it. trove's own outbound
  connections are checked by the address policy of R-REPO-038 in every role either way (R-HA-042,
  [H-516](../security/egress.md#h-516--the-chart-restricts-no-egress-of-its-pods)).
- **The cut into roles keeps the login secrets out of `packages`** (phase 6a, specified): the OIDC client secrets and the
  break-glass password mount only into `api` pods (R-HA-030), and `packages` evaluates no session cookie. It does not
  narrow the database, object-store or Valkey credentials or the key ring, which every role holds
  ([architecture.md](architecture.md) H-517).

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

**Choice:** (e) — SeaweedFS runs in the local profile and per-change CI; MinIO (pinned source build) runs on
every change, and AWS S3 once an AWS account exists (owner decision of 2026-10-29, below).

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

**Owner decision (2026-10-29).** The MinIO leg runs in [Test and Release](../../.github/workflows/release.yml)
(job `s3-conformance`) on every change, not nightly, at the commit of `MINIO_COMMIT`. No nightly
workflow exists. The AWS S3 leg does not run until an AWS account and a CI role exist
([Q-HA-25](#q-ha-25--aws-leg-of-the-nightly-run-when-no-aws-account-is-configured)). Choice (e) stands
for the rest: SeaweedFS is the store of the local profile and of the integration tier. The rationale above rests on
MinIO running on isolated runners, which is no longer the case; see [H-80](#h-80--self-hosted-runners-build-the-code-of-every-pull-request).

### Q-HA-4 · Reserve `/v2/` on the client listener for OCI

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded in part by [Q-EP-76](endpoints.md#q-ep-76--a-host-root-binds-a-repository-by-project-and-key-and-the-label-binding-goes) and [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared): the first two segments after `/v2/` are the project key and the repository key, and the subdomain mode through a label binding is gone.

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

Status: **Superseded** by the owner's rule of 2026-11-04 ([ADR-154](../decisions.md#adr-154--network-policies-are-ingress-rules-between-the-charts-pods-each-with-its-port)):
the chart renders ingress rules only and no egress policy at all, so there is no default to choose. The development agent had
weighed four defaults for an optional egress policy (off; on with a generic allow-list; on with explicit targets required; on
with everything but the metadata address) and chosen (a), off by default with a `NOTES.txt` warning and an example values file
`deploy/helm/trove/examples/egress.yaml`; neither was built. What remains is recorded as
[H-516](../security/egress.md#h-516--the-chart-restricts-no-egress-of-its-pods): the address policy of R-REPO-038 in code and the
cluster operator's own policy.

**Applied in the spec:** R-HA-042 (no egress part, no `networkPolicy.egress` values), R-HA-031, Security notes.

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

**Owner decision (2026-10-29).** The owner overrides the choice of option (a) above. The image is
published to Docker Hub as `docker.io/guidedtraffic/trove`, and the chart to its repository
`https://guided-traffic.github.io/trove/` (GitHub Pages, from the `gh-pages` branch), as the
organisation's other repositories do ([ADR-130](../decisions.md#adr-130--the-organisations-ci-and-release-standard)).
Keyless cosign signatures are dropped in favour of provenance and SBOM attestations. The option's
argument that no registry credential is stored no longer holds: the release job logs in to Docker
Hub with the organisation's access token (secret `DOCKERHUB_PAT`), and the same token pulls the base
images in CI. R-HA-031 and R-HA-047 carry the new values.

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

**Owner decision, 2026-10-29 (revisits this question).** The owner's standard is the guided-traffic
[valkey-operator](https://github.com/guided-traffic/valkey-operator) (chart 1.14.12, custom resource `Valkey`
of `vko.gtrfc.com/v1`, checked on 2026-10-29). Option (c) is therefore the owner's constraint, not the development
agent's choice, and the local profile moves from `deploy/dev/valkey.yaml` to the operator's resource with
cert-manager as its certificate source. The operator gives Sentinel mode, TLS only, no persistence, PodDisruptionBudgets
and zone anti-affinity, and it lacks three things this page requires: ACL users (R-ARCH-031, R-HA-056), a Sentinel
password separate from the data nodes' and `maxmemory`. The owner was asked whether to extend the operator; until
the owner decides, the dev profile runs trove as the operator's `default` user, and the requirements of R-ARCH-031 and
R-HA-056 stay as written for production. Those gaps are recorded where their mechanisms are:
[H-72](../security/privilege-footprint.md#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist) and
[H-64](../security/transport-encryption.md#h-64--sentinel-trust-with-the-chart-defaults). The dev profile's switch is pending
(see [local-profile.md](../developer/local-profile.md)); this paragraph supersedes choice (d) for the dev profile only.

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
tools with the pins. Helm and kubectl in CI come from `azure/setup-helm@v5` and `azure/setup-kubectl@v5`, pinned by
major version tag as the organisation's other repositories pin their actions, with their versions from the same file.
Renovate updates the action tags ([renovate.json](../../renovate.json), the `github-actions` manager).

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

**Owner decision, 2026-10-29 (revisits this question).** The edge of the local profile is the owner's controller, HAProxy
Ingress: the chart `haproxy-ingress` at the owner's pin in the owner's own cluster base. The owner moved that pin from 0.15.3 to 0.16.3 on 2026-10-30, so the dev edge keeps mirroring production; the controller image of 0.16.3 runs HAProxy 2.8.31 (0.15.3 ran 2.6.25; both read from the images), and `helm template` of the 0.16.3 chart with the dev values (Helm 4.3.1) keeps the two replicas, the PodDisruptionBudget with `minAvailable: 1`, the NodePorts 30080 and 30443, the control-plane placement and the default IngressClass, while the pod no longer automounts the ServiceAccount token into the pod: it is mounted only into the controller container (a projected volume), as the chart documents. The controller, which watches Ingresses through the API, still has its token. The annotation prefix
`haproxy-ingress.github.io/`. Choice (a) therefore names HAProxy Ingress instead of Traefik, and the Traefik pins of the
profile are withdrawn when the profile switches. The scatter profile keeps its purpose (one request, one backend) and moves
to HAProxy's keys: a round-robin balance, and no upstream keep-alive through `option http-server-close` in a backend
configuration snippet. That mechanism is not verified in this repository yet, so R-HA-051 does not state it as done. The
failover choice (patching `status.targetPrimary`) is unchanged, because the owner's PostgreSQL operator is the same.
The edge's TLS to the backend is described in [security/transport-encryption.md](../security/transport-encryption.md).

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

**Owner decision (2026-10-29).** The pipeline has no nightly run and no AWS S3 leg until an AWS account and a
CI role exist. Option (c) is therefore not built: there is no gate job and no AWS job, and no grey job appears
in any run. A run therefore never shows AWS S3 as passed, because no run includes it. R-STOR-035 is not checked
against AWS S3 by any job until the owner provides the account; E0.5 of the [roadmap](../coverage/roadmap.md)
says the same.

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

**Revisited 2026-10-29.** The condition of the rationale is met: the release pipeline builds the image in CI, from
the same Containerfile and pins ([build.yml](../../.github/workflows/build.yml), and the `image` job of
[Test and Release](../../.github/workflows/release.yml)). The Containerfile holds the Angular build, the Go build
and the base image in its own stages, and `make image` is one `docker buildx build` of it
([Makefile](../../Makefile)). The host no longer runs `make web` for the image. The choice (b) stands; its
description of the host steps is superseded.

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

### Q-HA-33 · Sentinel trust settings: warn or refuse at start

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Two settings weaken the Sentinel trust without being plaintext: a Sentinel without a password, and data nodes verified
against the system roots without a CA file. The chart defaults to both (its Valkey Sentinel Secret and its Valkey CA
Secret are empty), and the owner's operator cannot yet provide a Sentinel password.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse at start, unless a switch names the weakness | the replica exits with the names of the keys | the weakness cannot be left by accident | the chart's defaults and the owner's operator would stop every install until the operator gains the password |
| (b) Warn once at start; never refuse | one warning per weakness, naming the fix | an install starts and shows the gap in its logs | a warning is easy to miss |
| (c) Warn and add a switch that makes each an error | the operator opts in to strictness | the strict setup is one key | a second switch to read; the default stays lenient |

**Choice:** (b), without a strictness switch yet.

**Rationale:** The chart's defaults are the owner's current deployment, and refusing them would stop it without a
safety gain that the warning does not already give. The gap is recorded as an open item on the transport page
(H-64) and the warning names the fix. The development agent would revisit this when the valkey-operator provides a
Sentinel password: then the missing password becomes an error (a) for the production values, and the choice becomes (c).

**Applied in the spec:** [R-HA-056](#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies) (the warnings of the
connection bullet); the code is `warnWeakTrust` in [internal/cluster/valkey.go](../../internal/cluster/valkey.go).

### Q-HA-64 · How the end-to-end tests of the access core hold Valkey down, reach a replica directly and get the login configuration into the dev profile

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E1.8, E1.11 and E1.14 test "with Valkey stopped" for the whole of a criterion, E1.8 tests per-address limits whose
bucket every other test of the run shares (the address of the host behind the edge), and E1.1 needs the OIDC provider and the
break-glass password in the dev values while the chart has no value for either.

| Question | Options | Choice |
|---|---|---|
| Holding Valkey down for a criterion | (a) `valkey-outage.sh` with a duration; (b) a stop script and a start script, the test holds the outage; (c) a toxiproxy in front of Valkey | (b) `valkey-stop.sh` and `valkey-start.sh`, with `valkey-outage.sh` as stop, sleep, start. (a) cannot hold an outage for a criterion of unknown length; (c) is a new component in the profile |
| The latency injection of R-ARCH-034 | (a) a proxy that delays; (b) `CLIENT PAUSE ... ALL` on the primary for 4 s | (b), through `valkey-cli.sh`. The calls neither succeed nor fail at once, which is what a late Valkey does; 4 s stays below the Sentinels' 5 s `down-after-milliseconds`, so no failover is provoked |
| Per-address limits (R-AUTH-038) | (a) the edge address, with long waits between tests; (b) port-forwards to the web pods | (b) for the threshold tests: the replicas see 127.0.0.1, a bucket of its own, and the requests are sent to the three pods in turn, which is the criterion's "round robin over three replicas" without the edge in between |
| Login configuration of the dev profile | (a) wait for chart values `secrets.oidc` and `secrets.breakGlass`; (b) `extraVolumes` and `extraVolumeMounts` with one projected volume | (b) in values-dev.yaml: the chart is not touched, and the paths in `config.auth` are those the chart's values will render |

**Rationale:** Each choice adds no component to the profile and keeps the tests close to what an operator does. The revisit
conditions: the chart gains the two secret values (then the projected volume goes), or a criterion needs a delay finer than
`CLIENT PAUSE` (then a proxy).

**Revisit applied:** the chart has the values `secrets.oidc` and `secrets.breakGlass` (built); values-dev.yaml uses them and no longer
projects the client secret and the break-glass password. The Keycloak CA stays a volume of `extraVolumes` until `trustedCA` (R-HA-043) is built.

**Applied in the spec:** none; [../developer/local-profile.md](../developer/local-profile.md) and
[../developer/e2e.md](../developer/e2e.md#m1-tests).

### Q-HA-65 · The npm part of R-HA-059's load before NuGet exists, and how its commands are counted

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap E2.11 runs variant A of [R-HA-059](#r-ha-059--node-drain-without-outage) in M2 "with the npm part of its load". The
load table has three npm rows (eight `npm ci` loops of a fixture with 400 locked dependencies through `npm-all`, an `npm publish` every
10 s, k6 at 150 requests/s of which 50 % are npm documents and 30 % npm tarballs of 1 to 5 MB) and NuGet rows that M3 brings. The
corpus of the counting test upstream has no tarball near 1 MB, and the package classes of R-API-016 budget 200 requests/s per token.
The pass condition is that every `npm` command exits `0` with npm's default retries, while the clients run in pods that the test
reaches through `kubectl exec`, which runs through the API server while a node is drained or a database switches over.

| Question | Options | Choice |
|---|---|---|
| The k6 rate and mix | (a) the npm rows at their own rates: 120 requests/s, documents and tarballs 5 to 3; (b) 150 requests/s of npm alone, 62.5 % documents and 37.5 % tarballs; (c) 150 requests/s with NuGet's share as `GET /api/v1/system/ping` | (a). (b) is a mix the spec does not name, with 25 % more large tarballs than its load; (c) adds requests the load table does not name, which make the warm-up's p99 smaller and the latency condition easier |
| The fixtures | (a) built at test time: nine versions of a package with 1 to 5 MiB of random bytes in `npm-local` for k6's tarballs; a lockfile resolved through `npm-all` from corpus roots whose closure has at least 397 names plus three `npm-local` packages; (b) committed fixtures | (a): the lockfile locks 475 packages (472 through `npm-remote`, 3 from `npm-local`), and its `npm install` fills trove's cache, so the loops start warm; nothing to keep in step with the corpus |
| Tokens | (a) one per load source; (b) two per `npm ci` loop, alternated per command, three for k6 taken in turn, one for the publisher | (b). A loop asks for 475 tarballs every 2.5 s; on one token per loop four in ten commands met the `package_read` budget of 1,000 in 5 s (measured: `429`, then npm's retry after 10 s), so the load measured the limiter more than the drain. k6's 120 requests/s are spread over three tokens since 2026-11-01: a PostgreSQL switchover held the replicas for 19 s, the edge queued the load and handed it on at once, and one token met its burst (48 answers `429` in Actions run 41249697928). 20 tokens, all an account may hold |
| How a command is counted | (a) a shell loop per load source detached in its client pod (`setsid`), one record per command with its exit status, the output of a failed one kept, stopped through a file; (b) one `kubectl exec` per command from the test; (c) a Go client that sends npm's requests | (a): the exit status is the client's own, and an exec that breaks during a fault is never a failed command. (b) cannot tell a broken exec stream's status from npm's; (c) is not npm, whose retries and timeouts the criterion names. A loop that writes no record fails `loopsSpan` |

The long-transfer probes of R-HA-059 are no npm load and join the drain test in E5.9.

**Rationale:** E2.11 names the npm rows, and their rates are the spec's. The development agent revisits this when M3 adds the NuGet
rows: k6 then reaches 150 requests/s with the spec's mix, and the tokens become two per loop of both clients (another account, because
of the 20-token bound); and the counting when the clients run as Jobs of their own, whose exit status Kubernetes records.

**Applied in the spec:** none; [../developer/e2e.md](../developer/e2e.md#drain-and-failover-under-npm-load) and
[test/e2e/npm_load_test.go](../../test/e2e/npm_load_test.go).

### Q-HA-66 · The rate-limit window of a request that a database outage held

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the rate limits of R-API-016 and R-EP-015 run after the authentication stage, because their bucket is the token
([architecture.md](architecture.md) R-ARCH-034). A token lookup that misses the replica's cache (its `auth.tokens.cache_ttl` of 15 s
ends) goes to PostgreSQL, and during a switchover that read is held for up to `database.retry.max_elapsed` (R-HA-015). Every request of
that token on that replica waits behind it and reaches the limiter when the database returns. The limiter counted a request in the
window of its check, so a client that never exceeded its rate was charged its held requests and its new ones in one window. Measured
in the switchover under npm load (E2.11): k6 at 120 requests/s on one token, below its budget of 200/s and 1,000 per 5 s, arriving at
exactly 120 per second throughout; the switchover held the requests for 6 s; 4 to 5 s after the release, 153 requests of the client
were answered `429` on all three replicas (the counter is cluster-wide). A window that receives 600 arrivals can exceed 1,000 only if
it is also charged requests that arrived earlier, so the requests were counted after their wait; the authentication is the one stage
before the limiter that reads the database. It happened in one switchover of three, which fits the cache of at least two of the three
replicas expiring during a 6 s hold. Variant C of R-HA-059, a drain of the PostgreSQL primary's node under k6, would meet it in E5.9.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Charge a request to the window it arrived in | Step 5 of the chain records `Received`; the limiter counts the request in, and decides it against, the window of that time; the per-replica fallback decides at the time of the check | A client is charged by what it sent and when; a client above its rate is still refused, each window of arrival admits its burst; no new key, no new round trip | The `RateLimit` fields of a held request describe its arrival window |
| (b) Serve the expired cache entry while the database is unreachable | The authentication does not wait | No hold before the limiter | A revoked or disabled token would be accepted past R-AUTH-042's bound for as long as the outage lasts; a security regression |
| (c) Exempt a request from the limit when it waited | A request that waited longer than a bound skips the limiter | Simple | An outage would open the limit for every client at once, the abusive ones too |
| (d) Keep it | Clients retry a `429` | No change | A non-retrying client fails on a switchover it did not cause; R-HA-052's zero failed iterations and variant C's k6 condition fail in about one switchover in three |

**Choice:** (a). `Limiter.TakeAt` in [internal/cluster/limiter.go](../../internal/cluster/limiter.go), called by the stages of
[ratelimit.go](../../internal/api/v1/ratelimit.go) and [endpoint_ratelimit.go](../../internal/api/v1/endpoint_ratelimit.go) with
`RequestInfo.Received` ([internal/httpserver/chain.go](../../internal/httpserver/chain.go), step 5). An arrival time in the future or
zero is the time of the check. The login limiters (R-AUTH-038) take no token and run before any wait; they keep `Take`.

**Rationale:** The budget is a statement about the client's rate, which the arrival time measures and the time of the check does not.
What the limit defends stays: the held requests of a client above its rate are refused by the windows they arrived in (unit test
`TestLimiterCluster`, "requests a stage held are charged to the windows they arrived in"). What it does not defend: the backlog of a
client within its budget is served at once when the database returns, which adds to the load of the recovery; that load was sent at
an admitted rate, and the in-flight limit of the chain (step 4) still bounds it. One more bound is weaker than before: the counters
of a window live two windows from their first hit (`EXPIRE … NX`), so a request held longer than that finds its window's counter gone
and starts it again, and that window can admit one more burst than its limit, at most twice the limit in all. Only a stage before the
limiter holds a request that long (the token lookup during a database outage); a client cannot make it happen. The development agent revisits this when the
token lookup no longer waits for the database during an outage without weakening R-AUTH-042, which would remove the hold itself.

**Applied in the spec:** [architecture.md](architecture.md) R-ARCH-034, the cluster-scope bullets: the window of a request is the window
of its arrival.

### Q-HA-67 · Where and how often the scenarios under npm load run

Status: **Decided by the owner on 2026-11-01** — option (e). It replaces the development agent's choice (a), which made the
leg `npm-ha` the longest job of Test and Release (48 minutes, 22 of them the pod crash).

**Amended by the owner later on 2026-11-01:** the legs under faults leave the per-change pipeline altogether. Performance,
failover and similar long tests run occasionally: locally (`make e2e SET=failover|npm-ha|npm-ha-crash|npm-ha-soak`) and in a
separate CI run, the workflow HA and Performance ([performance.yml](../../.github/workflows/performance.yml), every night and on
dispatch), with the jobs `e2e` (set `failover`), `e2e-npm` (set `npm-ha`), `npm-ha-soak` and `latency`. Test and Release keeps
the sets `push`, `access-long` and `npm`. The three-force-delete set `npm-ha-crash` runs by hand; the nightly soak runs the same
test with ten. A red long leg becomes an open finding and does not hold a release. Reason: the per-change pipeline had
blocked feature work for more than a day, failing on CPU-throttled runners rather than on trove; the scenarios keep their pass
conditions.

Context: R-HA-052 runs the pod-crash and switchover scenarios and variant A of the drain test on every change to `main`, and the
Valkey scenarios in the full suite; E2.11 pairs the drain with `dev-failover` and `dev-valkey-failover` during an `npm ci` loop. The
scenarios need the client pods, the counting test upstream and the scatter profile of the npm tests. The pod crash alone takes
20 minutes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A leg `npm-ha` of the job `e2e-npm` | `make e2e SET=npm-ha` on every change: drain variant A, PostgreSQL switchover, Valkey failover, pod crash, and the load scripts of R-ARCH-027 as a report; a closing dispatch runs the leg as `npm-ha-closing`, which adds the 5-minute Valkey outage | R-HA-052's split as written, the Valkey failover of E2.11 on every change; its own cluster, so the matrix of the leg `npm` does not load the drain and the drain does not slow the matrix | One more kind cluster per change; about 40 minutes of scenarios |
| (b) Everything on every change | The outage in `npm-ha` too | One set | 10 more minutes per change for a scenario R-HA-052 keeps for the full suite |
| (c) The scenarios in the set `npm` | One cluster for all npm tests | No extra cluster | The matrix leg grows by 40 minutes; a failure of either blocks the other's evidence |
| (d) In the job `e2e` beside E0.14 | The push set drains under npm load | One drain per change | That job has no client pods and no test upstream, and runs two `web` replicas, not R-HA-059's three |
| (e) Two legs per change, a shorter pod crash, the 20 minutes nightly | Leg `npm-ha`: drain variant A, PostgreSQL switchover, Valkey failover. Leg `npm-ha-crash`: the pod crash with three force-deletes every 2 minutes. The ten force-deletes over 20 minutes and the load scripts of R-ARCH-027 run nightly (`performance.yml`, set `npm-ha-soak`) and in the closing run (`npm-ha-closing`, one leg with the outage) | Every scenario still runs on every change; the longest npm leg drops from about 48 to about 22 minutes | One more kind cluster per change than (a); a crash race that needs more than three force-deletes shows up nightly, not on the change that caused it |

**Choice:** (e), with the step budget of 80 minutes for the `npm-ha` legs (the job 120) kept for the closing leg.

**Rationale:** the owner's goal of 2026-11-01 is a shorter round trip of Test and Release; (a) put 48 minutes on its critical
path. Three force-deletes keep the 2-minute recovery window of the full scenario, so each kill is judged as before; the
20-minute form keeps its daily run. The load scripts of R-ARCH-027 are a report, and R-ARCH-027 asks for them in the closing
run only.

**Applied in the spec:** R-HA-052 (the pod-crash row and the frequencies); [release.yml](../../.github/workflows/release.yml)
(job `e2e-npm`), [performance.yml](../../.github/workflows/performance.yml) (job `npm-ha-soak`),
[deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) (sets `npm-ha`, `npm-ha-crash`, `npm-ha-soak` and `npm-ha-closing`),
[npm_ha_test.go](../../test/e2e/npm_ha_test.go) (`TestNPMHACrash`).

### Q-HA-68 · The node variant A drains on the three-node local profile

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: variant A of R-HA-059 drains a node that hosts a `web` and a `worker` pod and "nothing else", with PostgreSQL and Valkey on
other nodes. The local profile of R-HA-051 spreads three Sentinels and two Valkey pods one per zone over its three worker nodes, and the
two PostgreSQL instances over two of them: every node hosts a Sentinel, and most host a Valkey pod or a PostgreSQL instance.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A node with neither primary | The node hosts a `web` and a `worker` pod and neither the PostgreSQL primary nor the Valkey primary; its Sentinel, a Valkey replica or the PostgreSQL standby drain by their own disruption budgets. A primary in the way is moved before the load starts (`valkey-failover.sh`, `failover.sh`) | Variant A's subject, trove's own pods, is what is drained; the primaries stay put, so A does not turn into B or C; no change to the profile | The drained node is not empty of dependencies; a Sentinel and a standby leave with it |
| (b) A fourth worker node for trove alone | `kind.yaml` gains a node that only trove pods may use | Variant A as written | A fourth node on every laptop and runner; spreading over three zones for the dependencies and four nodes for trove makes the profile unlike a production cluster |
| (c) Pin the dependencies to two nodes | Valkey, the Sentinels and PostgreSQL leave the third node free | Variant A as written on three nodes | Two Sentinels on one node; a node loss takes the Sentinel majority, which R-HA-059's rule 5 forbids |

**Choice:** (a). `drainNodeVariantA` in [npm_ha_test.go](../../test/e2e/npm_ha_test.go) picks the node.

**Rationale:** A drained Sentinel or standby is rule 5 of R-HA-059 at work, which every variant relies on; a moved primary is what
variants B and C test. The development agent revisits this when the local profile gains a node for other reasons.

**Applied in the spec:** R-HA-059, the variants table: the note on variant A in the local profile.

### Q-HA-69 · The storage check of the pod crash before `trove admin storage verify` exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the pod-crash row of R-HA-052 passes when "no item row [is] without a durable object (`trove admin storage verify`)". The
command belongs to M5 (roadmap E5.2); WP2.8 runs the scenario in M2.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read every listed version | After the scenario, every version that `npm-local` lists for the package the publish loop wrote is downloaded through the API and compared with its `dist.integrity` | An item row without its object answers an error; the check reads what a client reads | Only the items the scenario wrote, and only through npm |
| (b) Wait for the command | The scenario runs without the storage condition until M5 | No interim check | The condition that makes a crash during a publish dangerous goes unchecked until M5 |
| (c) Compare the item table with a bucket listing | SQL over the item rows, the S3 listing of `trove-blobs` | Every item | A second reading of the storage layout in a test, which the command will own |

**Choice:** (a), `committedTarballsReadable` in [npm_load_test.go](../../test/e2e/npm_load_test.go). Every acknowledged publish must
be among the listed versions.

**Rationale:** The writes of the scenario are the only items a crash in it can leave without an object, and a download is the reading
that matters to a client. The development agent revisits this when `trove admin storage verify` exists: the scenario then runs the
command as R-HA-052 states.

**Applied in the spec:** R-HA-052, the pod-crash row: the interim check.

### Q-HA-70 · The chart refuses a `web` pod whose memory cannot hold what it admits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a `web` replica sizes its admission from its memory limit ([architecture.md](architecture.md) R-ARCH-005,
Q-ARCH-240, Q-ARCH-243): ⌊(0.8 × `L` − the caches' byte budgets) ÷ request bytes⌋ memory slots, with `L` = `runtime.memory_limit_ratio` × the container limit. The
chart passes `limits.memory` through the downward API; without a limit Kubernetes passes the node's allocatable memory
instead, and the replica would size its slots from memory it does not have to itself. A limit that admits a handful of
requests cannot serve the parallel downloads of even a few clients (npm opens up to 15 connections per install).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Require the limit and at least 64 slots | `trove.validate` computes the slots with the binary's formula for `web` and every endpoint pool and refuses a missing limit or fewer than 64, naming the values | A combination that cannot fit fails at `helm install`, before a pod sheds every request | The chart repeats a formula of the binary (one template, `trove.requestBytes`, tested against the binary's numbers) |
| (b) Refuse an explicit `config.server.max_in_flight` above the slots | The operator's promise of concurrency must fit the memory | Catches the literal mismatch | The binary already admits the smaller of the two; refusing a value that is harmless at runtime only moves the arithmetic to the operator |
| (c) A floor on `limits.memory` alone, such as 256 MiB | One number | Simple | Misses a large `buffer_size` on a large pod, which is the combination that does not fit |
| (d) No chart rule | The binary logs its slots at start | Nothing to keep in sync | A pod that admits almost nothing is found under load |

**Choice:** (a). The limit is read as a Kubernetes quantity with a binary or a decimal unit (`2Gi`, `2G`); a fraction or an
exponent is refused with a message that says so.

**Rationale:** The slots are the contract between the pod's memory and its admission; the chart is where both are chosen, so it
is where a pair that cannot work is refused. 64 is four clients at npm's parallelism; with the defaults it is any limit of
59 MiB or more, so it only catches a buffer configuration that is out of proportion to its pod. The development agent revisits the
floor when the sizing profiles of R-HA-050 exist and say what a small pod is expected to serve.

**Applied in the spec:** R-HA-029 (the row), R-HA-036 (admission follows the memory). Code:
[_validate.tpl](../../deploy/helm/trove/templates/_validate.tpl), `trove.quantityBytes` and `trove.requestBytes` in
[_helpers.tpl](../../deploy/helm/trove/templates/_helpers.tpl).

### Q-HA-71 · How the edge splits one host between `api` and `packages`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: ADR-155 splits one host by path prefix between the two roles; R-HA-037 routed a host whole. The route families map
onto path prefixes ([internal/httpserver/surface.go](../../internal/httpserver/surface.go), `familyOfPath`): `ui` is `/ui`,
`/auth` and `GET /`; `api` is `/api/v1`; `ci_exchange` is `POST /api/v1/auth/oidc/token` and the npm exchange (P1);
`packages` is the rest of `/api` and the host-root prefixes `/v2` and `/.well-known`; `files` is `/files`. Both Kubernetes
`Prefix` and the Gateway API's `PathPrefix` match whole path elements and prefer the longest match.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Six fixed `Prefix` rules per host, `/` and `/api/v1` to `api`, `/api`, `/files`, `/v2`, `/.well-known` to `packages` (chosen) | Every path no rule names, scanners' paths and the UI's deep links included, reaches `api` | Portable to every conformant controller and to the Gateway API; the UI and the sign-in need no rule of their own; a path the edge sends to the wrong role is a `404`, never another family's answer | Unknown paths cost `api` a `404` each |
| (b) The catch-all `/` to `packages` and explicit rules for `/ui`, `/auth` and `GET /` | Unknown paths reach `packages` | The bulk role absorbs scanners | `GET /` exactly needs an `Exact` rule; a UI path added later needs a chart change |
| (c) Regular-expression rules per family | One rule per family | Mirrors `familyOfPath` | Not portable (implementation-specific in the Gateway API, an annotation in HAProxy Ingress) and ordered differently by each controller |
| (d) A host per role (`api.trove.example.com`) | No path split | Simplest edge | Breaks the one-origin model of R-ARCH-007 and the `__Host-` cookie on the base URL; every client URL changes |

**Choice:** (a).

**Rationale:** prefix rules are the one routing feature every Ingress controller and Gateway implementation agrees on, and the
families are prefixes, with one P1 exception (Q-HA-72). Sending what no rule names to `api` keeps the UI's paths free of chart
rules. The edge is load sharing, not a trust boundary: each role's own router registers only its families (Q-ARCH-298), so a
decoded or unusual path never reaches a handler of the other role. The development agent would revisit this if scanner traffic
measurably loads `api`; then (b) with an `Exact` rule for `/`.

**Applied in the spec:** R-HA-037 (the prefix table and its criteria), R-HA-038 (the HTTPRoute rules), R-HA-055 (the content
host to `packages`), R-HA-062 (path pools inside their role's prefixes).

### Q-HA-72 · How the npm exchange below a registry URL reaches `api`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded in part by [Q-REPO-212](repositories-and-proxy.md#q-repo-212--a-repository-is-addressed-inside-its-project-and-no-repository-is-shared): the registry URL holds the project key and the repository key, so the rule's path is `^/api/npm/[^/]+/[^/]+/-/npm/v1/oidc/`.

Context: R-NPM-022 (P1, not built: the path is one of npm's deferred routes in
[internal/formats/npm/routes.go](../../internal/formats/npm/routes.go)) serves the trusted-publishing exchange at the host root
(npm) and below every registry URL (pnpm from 11.28.7 and 12.15.0, Yarn 4): `/api/npm/<repoKey>/-/npm/v1/oidc/token/exchange/package/<name>`.
It belongs to `ci_exchange`, so to `api` (ADR-155), but matches the `/api` prefix of `packages`, and a repository key in the
middle of the path cannot be named by a prefix rule.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A rule of its own when R-NPM-022 is built: a regular-expression path `^/api/npm/[^/]+/-/npm/v1/oidc/` to `<fullname>-api` where the edge supports it (an HTTPRoute `RegularExpression` match; HAProxy Ingress's regular-expression path type on an Ingress of its own), with a chart test per supported edge; until then nothing (chosen) | The family table holds; the edge carries one exception | ADR-155's family assignment is kept; no secret or area moves | Regular-expression matches are implementation-specific, and their precedence over a prefix differs between controllers; on an edge that ranks them below prefixes the exchange keeps reaching `packages` |
| (b) `packages` serves npm's exchange routes as an exception to the family table | The exchange is built on `packages` | No edge rule; works on every edge | `packages` would build the CI exchange area and issue tokens against identity-provider tokens, which ADR-155 put in `api`; an owner decision to change |
| (c) Only the host-root form | Registry-URL clients use the host root | No nested rule | pnpm since 11.28.7 and Yarn 4 post below the registry URL and cannot be configured otherwise |

**Choice:** (a), scheduled with R-NPM-022 (P1).

**Rationale:** the owner put the exchange in `api`; the edge is where a nested path can be told apart without moving an area.
The development agent would revisit this when R-NPM-022 is built and a supported edge cannot rank the rule above `/api`: then
(b), for the owner to decide, since it changes ADR-155's table.

**Applied in the spec:** R-HA-037 (the nested family), [formats/npm.md](formats/npm.md) R-NPM-022 (a note on the edge rule).

### Q-HA-73 · How an installation upgrades across the cut into roles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-020 promises a zero-downtime upgrade from the previous minor; Q-ARCH-295 keeps the role `web` as the bridge.
Helm applies a release in one pass and does not wait for a new pod before it repoints the edge and deletes a removed
Deployment. A Service's selector can change in place; a Deployment's cannot.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two steps joined by `web.enabled`: step 1 keeps the old Service and edge in front of `<fullname>-web` while `api` and `packages` start; step 2 switches the edge and removes `web`; a `lookup` guard refuses step 2 while the new Deployments are not ready (chosen) | R-HA-064 | Zero downtime with plain Helm; the guard catches a one-step upgrade wherever the chart can read the cluster | Two `helm upgrade` runs; renderers without cluster access (`helm template`, some GitOps tools) get no guard and rely on the release notes |
| (b) Automatic: the chart decides the shape from `lookup` (the bridge while `<fullname>-web` exists and the new roles are not ready) | One value-free command, run twice | No value to set | The rendered output depends on the cluster, so `helm template` and GitOps renderers produce the target shape at once and cause the gap; a reviewer cannot read the shape from the values |
| (c) One step, accepting the gap | Release notes warn | Nothing to build | Breaks R-HA-020 for this upgrade: every family answers `503` for the start time of the new pods |
| (d) A pre-upgrade hook that creates and waits for `api` and `packages` | Hook-managed Deployments | One command | Hook resources are not release resources: they escape `helm rollback` and `helm uninstall` and duplicate the Deployment template outside the release |

**Choice:** (a).

**Rationale:** only (a) and (d) keep the edge on ready pods, and (a) does it with release resources and an explicit value whose
effect a reviewer can see. The guard is validation only: it never changes what renders. The way back from step 2 is a rollback
and has the start-time gap; R-ARCH-025 promises a working rollback, not a gap-free one. The development agent would revisit
this if Helm gains a wait between applying and deleting resources, which would make one step safe.

**Applied in the spec:** R-HA-064 (new), R-HA-020 (order), R-HA-021 (the row of the upgrade path), R-HA-029 (the guard),
R-HA-031 (`web.enabled`).

**Revised** by [Q-HA-130](#q-ha-130--the-cut-moves-the-edge-and-removes-the-bridge-in-two-separate-upgrades) (2026-11-06): the
second step left the edge without a backend for about 1.5 s and is split into two.

### Q-HA-74 · Which client Services exist after the cut, and how in-cluster callers reach trove

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: before the cut one Service `<fullname>` served every family, and callers inside the cluster use it: the dev profile's
Bastions call `https://trove.trove.svc:8080` for their interface (`/api/v1/bastion/…`, `api`) and their forwards (`packages`,
`files`) ([deploy/dev/bastion-chart-common.yaml](../../deploy/dev/bastion-chart-common.yaml)). A Service cannot split by path.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One Service per role, `<fullname>-api` and `<fullname>-packages`; `<fullname>` only in the bridge; an in-cluster caller uses the Service of the family it calls, and the Bastion gains an optional `bastion.main.packages_url` for its forwards (chosen) | Names say what they serve | Each Service answers every request it receives; a caller that addresses the wrong one gets a `404`, not a silent partial service | In-cluster callers and certificates (SANs) change once; the Bastion's configuration gains a key |
| (b) Keep `<fullname>` as the `packages` Service | The most common in-cluster callers (package clients) keep their URL | Fewer changes for package clients | The name hides that the management API, the CI exchange and the Bastion interface are gone from it; a caller of both families breaks silently |
| (c) Keep `<fullname>` selecting both roles' pods | One name for everything | No caller changes | A request lands on a pod of the other role half of the time and answers `404`: random failures |

**Choice:** (a). The edge keeps verifying the listener certificate under `<fullname>.<namespace>.svc` for both role Services
(R-HA-037), so the certificate needs that name and, for in-cluster callers, the names of the role Services.

**Rationale:** with path routing only at the edge, the only Services that answer every request they receive are role
Services. The Bastion is the one in-cluster caller that needs both families; one optional key (default: `bastion.main.url`)
keeps a Bastion behind the edge unchanged and lets one that calls Services name both. The development agent would revisit
this if a common client fixes a single in-cluster URL for both families; then an edge inside the cluster is the answer, not a
Service.

**Applied in the spec:** R-HA-002 (Services and in-cluster callers), R-HA-028 (the resources), R-HA-037 (the verification
name), R-HA-064 (`<fullname>` in the bridge); [bastion.md](bastion.md) (the key `bastion.main.packages_url`).

### Q-HA-75 · Default replica counts, resources and drain capacity per role

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [ADR-105](../decisions.md#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role) and Q-HA-16 set
three `web` and two `worker` replicas by the N + 1 rule of R-HA-059: a peak that two `web` replicas carry, plus one for the
drain. The cut divides the `web` load: the package protocols and raw files carry almost all of it (the npm load of phase 7: 57 %
of a `web` pod's CPU in the virtual tarball path), the UI and the management API little.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `api` 2, `packages` 3, `worker` 2; `api` with half the requests of `web` (500m, 512Mi, a 1Gi limit) and a 5Gi spool; `packages` with the values of `web` (chosen) | `packages` carries the old peak; `api` a peak of one replica plus one | The default installation survives a drain at the load the owner expects; the package path keeps its sizing | Seven pods instead of five; the PostgreSQL budget grows from 171 to 231 |
| (b) Two of each | INV-12's minimum | Six pods | A drain of one of two `packages` replicas leaves less capacity than the peak needs |
| (c) `api` 3, `packages` 3 | Symmetric | One rule for both | A third `api` pod for a load that fits one |

**Choice:** (a). Autoscaling: `api` 2–4 and `packages` 3–8 on CPU (70 %), `worker` as before.

**Rationale:** the rule of ADR-105 applies per role, and the peak of each role is what it carries after the cut. The development
agent would revisit the numbers when the reference load test (R-HA-050) publishes the capacity of one replica of each role.

**Applied in the spec:** R-HA-031 (the `api` and `packages` blocks), R-HA-032, R-HA-016 (the examples), R-HA-050, R-HA-059 rule
1; [ADR-105](../decisions.md#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role) (a "Changed on" paragraph).

### Q-HA-76 · How the network policies are cut after the cut into roles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: ADR-154 asks for ingress rules only, between the chart's pods, each with its port, and "one policy per role (and per
pool)". Built today: one policy each for `web` and `worker`, and one per further endpoint that admits its port on the pods that
serve it (`endpoint-networkpolicy.yaml`, since merged into [networkpolicy.yaml](../../deploy/helm/trove/templates/networkpolicy.yaml)), whose pool policy
also admits 8081.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One policy per Deployment, named as the Deployment, with a rule per port its pods open: 8080, each further endpoint's port, 8081 (chosen) | The endpoint policies fold into the policy of the Deployment that serves the port | One place per pod type shows everything it admits; an endpoint's port follows the pods that open it, whichever role they are | A further endpoint without a pool appears in two policies when its surface spans both roles |
| (b) Per role, plus one per endpoint as today | The endpoint policies stay | Smaller change | A pod type's admissions are spread over several policies; an endpoint policy must select the pods of two roles |

**Choice:** (a).

**Rationale:** ADR-154 asks for a policy per role and pool; a Deployment is exactly a role or a pool, and naming the policy as
the Deployment makes the chart test a table. The development agent would revisit this if a seam between roles is ever added,
which then is one more rule in the receiving Deployment's policy.

**Applied in the spec:** R-HA-042, R-HA-028; [endpoints.md](endpoints.md) R-EP-008 (the NetworkPolicy row).

### Q-HA-77 · The connection-budget key of the serving roles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the migration Job compares the budget of R-HA-016 with `max_connections` from two counts it gets from the chart,
`database.preflight.expected_web_replicas` and `expected_worker_replicas` ([internal/config/schema.json](../../internal/config/schema.json)).
The formula distinguishes only processes with a lease pool from those without.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rename to `expected_serving_replicas`, the old key a deprecated alias with a start-up warning (chosen) | One count for every Deployment without a lease pool | The name says what it counts; R-ARCH-025's alias rule is kept | A key to rename in the binary and the chart |
| (b) Keep `expected_web_replicas` with the new meaning | No rename | No change | The name names a role that no longer counts alone |
| (c) One key per role | `expected_api_replicas`, `expected_packages_replicas` | Mirrors the values | The formula does not tell them apart; more keys for one sum |

**Choice:** (a). The chart renders only the new name.

**Rationale:** the budget needs exactly two numbers, and naming them by what they count keeps them right for pools and the
bridge. The development agent would revisit this if the roles get different pool sizes (`database.pool.max_conns` per role),
which would make (c) a formula input.

**Applied in the spec:** R-HA-016, R-HA-030.

### Q-HA-78 · Whether the path pools of `api` and `packages` come with phase 6a

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-062 had web and worker pools at P1; ADR-155 says variants by path prefix or queue are pools of a role,
"available as values under this decision", and BETA.md's phase 6a lists the worker pools by queue.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Both path pools and worker pools in phase 6a (chosen) | `api.pools`, `packages.pools` and `worker.pools` | The path rules of R-HA-037 are generated from data anyway, and a pool is one more row with its own Service; the owner's decision offers both | More chart tests and an e2e criterion in phase 6a |
| (b) Worker pools in phase 6a, path pools at P1 | As BETA.md lists | A smaller phase | The values of ADR-155 stay unavailable for formats until after the beta |

**Choice:** (a).

**Rationale:** once the edge rules are rendered per role from a table, a path pool adds rows and a Deployment the template
already renders; leaving it out saves little. The development agent would revisit this if the chart work of WP6a.3 runs late, in
which case the path pools move to P1 without changing any other part.

**Applied in the spec:** R-HA-062 (priority and values), R-HA-028, R-HA-029, R-HA-031.

### Q-HA-79 · Which pods mount the login secrets

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: today `secrets.oidc[]` and `secrets.breakGlass` mount into every pod of role `web`, endpoint pools included
([_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl)). After the cut, `api` builds the login area on every one of
its pods; the configuration file, shared by every pod, names the secret files.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every pod of role `api` (main, pools, endpoint pools) and `web` in the bridge; never `packages` (chosen) | The role holds its secrets | A role is one set of areas and secrets (ADR-155); the login area finds its files wherever it is built | An `api` endpoint pool, which serves no `ui`, holds the OIDC client secrets |
| (b) Only the pods that serve `ui` (the main `api` Deployment) | The smallest set | Endpoint pools of `api` hold no login secret | The login area would have to start without its files on some pods of the same role: two behaviours of one role |

**Choice:** (a).

**Rationale:** the trust line ADR-155 draws runs between `api` and `packages`, and (a) keeps it a line between roles. An
endpoint with only `api` families is rare and token-only. The development agent would revisit this if such endpoints become
common: then a login area that is built only where the surface holds `ui`.

**Applied in the spec:** R-HA-030 (the secret rows); [endpoints.md](endpoints.md) H-57.

### Q-HA-81 · Whether the chart renders the HTTPRoute with the path rules of the cut

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Built:** the
HTTPRoute is rendered ([httproute.yaml](../../deploy/helm/trove/templates/httproute.yaml), render test `TestR_HA_038_GatewayAPIExposure`) from the
same `trove.edgeRules` table as the Ingress, so the deferral of option (a) has ended; R-HA-038 states the values and the refusals.

Context: R-HA-038 makes the HTTPRoute MVP, but the chart renders none today: there is no `gateway` value, no check of the
Gateway API in `.Capabilities`, and no timeouts or filters. Work package WP6a.3 builds the edge's path rules, which the
HTTPRoute would share ([_edge.tpl](../../deploy/helm/trove/templates/_edge.tpl)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The path rules are one template (`trove.edgeRules`) that the Ingress and the BackendTLSPolicies read; the HTTPRoute with its `gateway.*` values, timeouts, filters and capability check comes with the chart completion of phase 7 (chosen) | The cut does not wait for a feature it does not need | The phase 6a exit criteria (E6a.4) name the Ingress rules; the HTTPRoute adds a template over the same table, not a second table | The R-HA-037 criterion "the chart test asserts the same for the HTTPRoute" is open until phase 7 |
| (b) Build the HTTPRoute in WP6a.3 | Both edges at the cut | One review of the edge | Adds the values `gateway.*`, the capability check, the timeouts rule (`backendRequest` ≤ `request`) and a Gateway API schema to a package that is already the largest of the phase |

**Choice:** (a).

**Rationale:** the table is data, so the HTTPRoute is a loop over it, and the risk of the cut is in the rules, not in the
second renderer. The development agent would revisit this if an installation needs the Gateway API before phase 7; then (b)
as its own package.

**Applied in the spec:** R-HA-038 (a status note), [roadmap](../coverage/roadmap.md) WP6a.3.

### Q-HA-82 · How the chart passes the queues of a worker pool

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-030 names `TROVE_JOBS_QUEUES` and `TROVE_JOBS_EXCLUDE_QUEUES`; the configuration loader reads a list-valued key from
its environment variable as one JSON (or YAML flow) list and refuses a bare `index` or `index,vuln`
([architecture.md](architecture.md) R-ARCH-016, Q-ARCH-304).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `toJson` of the pool's `queues`, set on the Deployment after `TROVE_POOL_NAME` and before `extraEnv`, so `extraEnv` can still override (chosen) | `TROVE_JOBS_QUEUES='["vuln"]'` | The form the loader accepts; one value per variable | None |
| (b) A comma-separated string | `vuln,index` | Readable | The loader refuses it |

**Choice:** (a). The main `worker` gets `TROVE_JOBS_EXCLUDE_QUEUES` set to the sorted union of every pool's queues when at least
one pool lists one, and no variable otherwise, so a queue a later release adds is claimed by the main Deployment.

**Rationale:** the form is the loader's. The development agent would revisit this if the loader gains a second list form.

**Applied in the spec:** R-HA-062 (the environment form).

### Q-HA-83 · Which Services and ports a path pool takes for the further endpoints

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-EP-002 lets a process in a pool that no endpoint names (a path pool) serve the endpoints without a pool, like the main
Deployment of its role. R-HA-062 routes a path pool's `paths` inside the host of the default endpoint. A further endpoint is
routed whole, or by the rows of its families, to its own Services.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A path pool opens the ports of the endpoints without a pool whose surface holds a family of its role, and its NetworkPolicy admits them; the Services and the Ingress of a further endpoint select the main Deployment only, and the pool's paths apply to the default endpoint only (chosen) | The pods do what R-EP-002 says; the edge routes only what R-HA-062 names | Matches both pages; an endpoint's routing needs no pool table | A path pool's pods hold an endpoint's certificate and port without receiving its traffic |
| (b) A path pool opens no endpoint port | Smaller policies | Fewer open ports | Contradicts R-EP-002: the process opens them anyway |
| (c) The pool's paths also route inside every endpoint's host | Pools work on every endpoint | Symmetric | Pool Services would need the endpoint's port; unspecified |

**Choice:** (a).

**Rationale:** the policy has to admit what the process listens on. The development agent would revisit this if a pool's paths
are wanted on an endpoint; then (c) with a per-endpoint pool Service.

**Applied in the spec:** R-HA-062 (a sentence), R-HA-042 (the table rows).

### Q-HA-84 · What a pool inherits from its role

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-062 says a pool takes "the keys of its role as overrides"; R-HA-032 gives a pool "an autoscaler of its own from its overrides".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every key of the role, `autoscaling` included, merged key by key under the pool's own keys; an enabled autoscaler of the role is therefore the pool's too, with the pool's `replicaCount` ignored, and `autoscaling.enabled: false` in the entry switches it off (chosen) | One rule for every key | Nothing is special; the HA rules see the merged values | A pool that sets `replicaCount` under an inheriting autoscaler is scaled by the autoscaler |
| (b) `autoscaling` is never inherited | A pool scales only when it says so | No surprise on `replicaCount` | A second rule next to "every key of the role" |

**Choice:** (a). The check of `highAvailability.enforce` and the connection counts read the merged values.

**Rationale:** one merge rule is checkable by a table. The development agent would revisit this if operators set `replicaCount`
on a pool and are surprised.

**Applied in the spec:** R-HA-062 (a sentence).

### Q-HA-85 · The component label of the Ingress and the policy objects of the edge

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-028 lists the component values of the pods and jobs. The Ingress and the BackendTLSPolicies belong to no role since the cut,
and a selector label with `trove/pool` belongs to the serving roles only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `app.kubernetes.io/component: ingress` on the Ingress objects and the BackendTLSPolicies, one BackendTLSPolicy per Service the default endpoint's edge names, named as the Service (chosen) | Honest labels; no pool label | The policy's `targetRefs` name one Service | One more component value |
| (b) `api` on every edge object | No new value | Fewer values | A selector on `component=api` would select an Ingress |

**Choice:** (a).

**Rationale:** the component label also feeds the `role` target label of the ServiceMonitor (R-HA-045), which pods carry and edge objects do not.
The development agent would revisit this if the ServiceMonitor selects by Service.

**Applied in the spec:** R-HA-028 (the component list), R-HA-038 (the BackendTLSPolicy per Service).

### Q-HA-86 · Where the connection budget is computed and what the guard can prove offline

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-016 asks the chart to pass `serving_max` and `worker_max` to the migration Job and to print the budget in `NOTES.txt`.
The guard of R-HA-064 reads the cluster through `lookup`, which returns nothing under `helm template` and under the chart tests.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The chart sums `maxReplicas` (or `replicaCount`) plus `maxSurge` over every Deployment (`trove.connectionCounts`), passes the two counts and prints the budget in the notes; the guard is plain template code whose cluster-dependent half (`lookup`) is verified by the upgrade e2e test and `helm upgrade --dry-run=server`, and whose offline half (inert, same output) by the chart test (chosen) | One source of the counts | The Job and the notes cannot disagree with the Deployments | The refusal itself is not exercised by `make chart-test` |
| (b) A test seam that fakes `lookup` | The refusal is tested offline | Coverage | A value in the shipped chart whose only use is a test |

**Choice:** (a).

**Rationale:** a seam in the shipped chart that changes what the guard reads is a second code path in the one place that must not
have one. The development agent would revisit this if Helm offers a way to inject cluster state into `helm template`.

**Applied in the spec:** R-HA-016 (the notes), R-HA-064 (the guard's verification).

### Q-HA-87 · The dev profile's sizing of the roles and its PostgreSQL connection budget

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-051 named two replicas of each role for the profile after the cut, while E6a.5 asks for `api` 2, `packages` 3 and
`worker` 2 with a worker pool for `vuln` and random distribution by the Services, and R-HA-059 drains each Deployment pod by pod. The
profile's PostgreSQL has `max_connections` 200, and the migration Job refuses a smaller server than the budget of R-HA-016.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `api` 2, `packages` 3, `worker` 2 and the pool `vuln` 2 (every Deployment at least two replicas for `highAvailability.enforce`); `max_connections` 400 on the profile's CloudNativePG cluster (chosen) | The chart's defaults for the serving roles plus the smallest pool the enforcement accepts | Random distribution is visible over three `packages` pods; the pool has its own drain; the budget is 303 with a surge of one per Deployment, 383 during the bridge, both below 400 | Nine pods instead of four; more requested CPU on the kind nodes |
| (b) Two `packages` replicas and no pool | The earlier text of R-HA-051 | Fewer pods | No third replica to tell random from alternating; no pool to drain; the vuln queue shares the main worker |
| (c) `max_connections` 231 and 311, the budget of the defaults without a pool | The numbers of R-HA-016's examples | Matches the examples | The pool's three more replicas at most (two and a surge) raise the chart's budget to 303 and 383, and the migration Job of the profile would refuse the cluster |

**Choice:** (a).

**Rationale:** the examples of R-HA-016 hold for the chart's defaults; the profile adds a pool, and the chart prints the number it
needs (`helm install --dry-run` prints 303, and 383 with `web.enabled`). The development agent would revisit this when the pool's
replica count or `database.pool.max_conns` of the profile changes.

**Applied in the spec:** R-HA-051 (the sentence on the replicas and the row of `values-dev.yaml`); [deploy/dev/values-dev.yaml](../../deploy/dev/values-dev.yaml),
[deploy/dev/postgres.yaml](../../deploy/dev/postgres.yaml).

### Q-HA-88 · Which legs run the drain per role

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. The load
of the leg is the owner's decision of 2026-11-05, [Q-HA-94](#q-ha-94--the-load-of-the-drain-per-role): the npm load of `TestNPMHA`
runs beside the load named in (a).

Context: R-HA-059 "A drain per role" evicts every pod of `api`, `packages`, the main `worker` and the pool `vuln` one after the other,
and the owner moved the drain, the failovers and the soak out of the release path on 2026-11-01 (Q-HA-67). The test of variant
A (`TestE14Drain`) stays in the set `push` with its reduced load.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A set `drain-roles` (`TestDrainRoles`) in the nightly workflow, on dispatch and locally, with the ping load, the upload and download loop, a loop of management calls with a token and the jobs of the driver (chosen) | Nine evictions, each waiting for its replacement | Fits the rule of 2026-11-01; failures are findings, not blockers | The full load of the spec's table (npm and dotnet loops, the UI journey) is not part of it yet |
| (b) The same test in the set `push` | Every change | Earlier feedback | Twenty minutes or more on a CPU-limited runner, against the rule of 2026-11-01 |
| (c) Three drains of whole nodes | Reuses `drain.sh` | No new code | Does not drain a role alone: the other roles' pods leave with it |

**Choice:** (a).

**Rationale:** the long legs run where the owner put them. The development agent would revisit the load of the test when the npm
and NuGet client loops of `TestNPMHA` can run beside it without the node drain.

**Applied in the spec:** R-HA-059 ("A drain per role": the leg that runs it); [docs/developer/e2e.md](../developer/e2e.md).

### Q-HA-89 · How the end-to-end harness addresses the roles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the helpers of the suite took the pods of the one `web` Deployment and forwarded to all of them. After the cut a package
request sent to an `api` pod answers the `404` of an unknown path (R-ARCH-001), so a helper over both roles breaks tests that look
as if they pass.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A selector and a forward helper per role (`selAPI`, `selPackages`, `packagesForwards`, `apiForwards`); a test names the role whose pods its requests belong to; a revocation waits for `api` through the edge and for every `packages` pod through its forward (chosen) | The test says which role it exercises | A request cannot reach the wrong role unnoticed | Each test names its role |
| (b) One helper over the pods of both roles | Few edits | Small diff | Half of the forwards answer `404` to the other family |
| (c) Send every request through the edge | No forwards | Exercises path routing everywhere | The per-address limits of R-AUTH-038 cannot be tested from a bucket of their own |

**Choice:** (a).

**Rationale:** the roles are different pods with different families; a test that does not say which one it talks to proves little.
The development agent would revisit this if a third serving role appears.

**Applied in the spec:** none; [docs/developer/e2e.md](../developer/e2e.md).

### Q-HA-90 · How a new pool or a new endpoint pool gets its traffic without a gap

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Helm applies a release in one pass. A path pool's route and an endpoint pool's Service selector are rendered in the same apply that creates the pool's Deployment, so the prefix or the endpoint answers `503` until the new pods are ready. In the bridge of R-HA-064 the same class showed up for the endpoint pools of the roles, which were first created in step 2 by the apply that repoints the endpoint's Service at them.

| Option | For | Against |
|---|---|---|
| (a) Render a pool's route only when `lookup` finds its Deployment ready | No new value; the second apply is implicit | `lookup` returns nothing under `helm template`, so GitOps renderers (Argo CD, Flux) would never render the route and the chart tests could not see it |
| (b) A documented two-step procedure and no chart support | Nothing to build | Nothing prevents the one-step apply; the pool cannot be added without routing it |
| (c) A key `routed` on a path pool (default `true`): `false` renders the pool without its route; the bridge renders the endpoint pools of the roles beside the web pools; an endpoint pool enabled on a running endpoint stays documented as a short gap (chosen) | Deterministic, the same under every renderer, testable offline; the guard of R-HA-064 can name every serving Deployment of the target shape | One more key; enabling an endpoint pool on a running endpoint is not covered |

**Choice:** (c).

**Rationale:** `lookup` is inert exactly where the answer matters most for GitOps, and a rule that depends on cluster state cannot be reviewed from the rendered output. `routed` moves the decision into values, where the two applies are two commits. An endpoint pool's Service selector, its listeners and the `pool` key of the configuration move together, so a pool that is rendered but not selected would need a second configuration shape; a new endpoint has no earlier traffic, and enabling a pool on a running endpoint is rare enough to document. The development agent would revisit this if an installation reports the gap of an endpoint pool, and add `pool.routed` then.

**Applied in the spec:** R-HA-062 (path pool routing, the refused combinations), R-HA-064 (endpoint pools of the roles in the bridge, the guard over every serving unit), [values.schema.json](../../deploy/helm/trove/values.schema.json) (`routed`).

### Q-HA-91 · The replicas of a Deployment when its autoscaler is switched on

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: with `autoscaling.enabled` the chart leaves `spec.replicas` out so that `helm upgrade` does not reset the autoscaler's choice (R-HA-032). On the upgrade that switches the autoscaler on, the field disappears from the manifest, the API server defaults it to 1 and the Deployment scales down before the autoscaler acts.

| Option | For | Against |
|---|---|---|
| (a) Read the live replicas with `lookup` and render them while the autoscaler is on (chosen) | The field keeps the live value, so neither the transition nor later upgrades change it; no value, no step | `lookup` returns nothing under `helm template` and on the first install; the field is then left out as before, and a GitOps renderer applies its own server-side merge |
| (b) Document a step: raise `replicaCount` to the expected load, then enable the autoscaler | Nothing to build | A step an operator forgets; the failure is a scale-down in production |

**Choice:** (a).

**Rationale:** The only moment the field matters is the transition, and the cluster holds the number the transition has to keep. Where `lookup` has nothing to read the chart renders what it rendered before. The development agent would revisit this if the field has to be set by a renderer without cluster access.

**Applied in the spec:** [_deployment.tpl](../../deploy/helm/trove/templates/_deployment.tpl) (`spec.replicas`), R-HA-032.

### Q-HA-92 · How an installation that stays on the bridge is reminded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the guard of R-HA-064 runs only when `web.enabled` is false. An operator who stops after step 1 keeps every host on the `web` pods, which hold the login secrets ([H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut)); only `NOTES.txt` says so.

| Option | For | Against |
|---|---|---|
| (a) A refusal after a time | Enforces the end of the bridge | The chart has no clock; a refusal on a later unrelated upgrade would block it |
| (b) An alert in the chart's PrometheusRule on `count(trove_build_info{namespace, role="web"}) > 0` for 24 hours, `TroveBridgeLeftOn`, severity warning, disableable like every alert (chosen) | Uses a series the binary already exports, scoped to the release namespace; the reminder reaches whoever watches the alerts | Needs `metrics.prometheusRule.enabled` and a scraping Prometheus; a clock of 24 hours is fixed |
| (c) Only the text of H-518 | Nothing to build | No signal |

**Choice:** (b).

**Rationale:** The bridge is meant to last for one rollout; 24 hours is longer than any rollout and shorter than an unnoticed week. The development agent would revisit the time if an installation needs a longer step 1, and make it a value then.

**Applied in the spec:** [_rules.tpl](../../deploy/helm/trove/templates/_rules.tpl) (`TroveBridgeLeftOn`), [rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml), R-HA-064, [H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut).

### Q-HA-93 · A provider with a client secret must name its Secret in secrets.oidc

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the login secrets belong to the api pods and, in the bridge, the web pods (R-HA-030). A provider whose `client_auth_method` is not `none` and that has no entry in `secrets.oidc` can get its secret only through the global `extraEnv`, `extraEnvFrom`, `extraVolumes` or `extraVolumeMounts`, which every Deployment receives; the trust split would break silently.

| Option | For | Against |
|---|---|---|
| (a) Fail the render, naming the provider (chosen) | The split cannot be broken by omission; the message names the two ways out | A provider whose secret an operator deliberately mounts everywhere is refused too |
| (b) A warning in `NOTES.txt` | No refusal | A warning does not stop the spread |

**Choice:** (a).

**Rationale:** The chart cannot tell a deliberate global mount from an omission, and the cost of the refusal is one list entry. The development agent would revisit this if per-role extra mounts are added, because the global lists would then no longer reach every Deployment.

**Applied in the spec:** [_validate.tpl](../../deploy/helm/trove/templates/_validate.tpl) (the login secrets), [_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl) (the comment on who mounts them), R-HA-030.

### Q-HA-94 · The load of the drain per role

Status: **Decided by the owner on 2026-11-05** — option (c), on finding F-49.

Context: R-HA-059 "A drain per role" asked for the load of the acceptance test's table plus a loop of a browser journey, and for
a full advisory sync started before the first eviction of the pool `vuln` that ends on another worker from its checkpoint. The
test of the set `drain-roles` (`TestDrainRoles`, [Q-HA-88](#q-ha-88--which-legs-run-the-drain-per-role)) ran the ping load through
the edge, the upload and download loop on `generic-local`, a loop of management calls with a token and the driver's changes and jobs,
and passed so in the nightly run 41902592855 (513 s). The npm load of R-HA-059 is built and runs in `TestNPMHA`
([Q-HA-65](#q-ha-65--the-npm-part-of-r-ha-059s-load-before-nuget-exists-and-how-its-commands-are-counted)); its k6 mix is
npm's share of the table (documents and tarballs, 120 requests/s), without the NuGet requests.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The full load of R-HA-059 | The npm and `dotnet` client loops, an npm and a NuGet publish every 10 s, k6 with its NuGet share, the long-transfer probes of 1 GiB, a loop of a browser journey, and a full advisory sync started before the first eviction of the pool `vuln` | The criterion as written | No browser journey loop exists as a test; the `dotnet` loops and the NuGet share of k6 run in no drain test yet (the drain part of E3.6 is phase 7's since 2026-11-01, [nuget_closing_test.go](../../test/e2e/nuget_closing_test.go)); a sync per pod repeats the `SIGTERM` and checkpoint path that the node drain of `TestVulnLong_R_VULN_004_FullSyncDrainedMidRun` covers |
| (b) Narrow the criterion to the load the test ran | Ping, the transfer loop, management calls, the driver's jobs | Nothing to build; green | No package document is requested, so the per-replica state the drain per role takes away is never measured; no client command runs |
| (c) Add the npm load of `TestNPMHA` and narrow the rest (chosen) | Eight `npm ci` loops, a publish every 10 s and k6's npm documents and tarballs at 120 requests/s beside the test's own load, with R-HA-059's pass conditions for them: every `npm` command exits `0` with npm's default retries; k6 only `2xx` and `304` and no connection error; the p99 of k6's requests from the first eviction until 2 minutes after the last is at most twice the warm-up's; every acknowledged publish installs afterwards. How it is applied is below the rationale. Not part of it: the `dotnet` loops and the NuGet rows, the long-transfer probes, the browser journey loop and the advisory sync per pod | Document load across the drain of every `packages` replica; the load and its checks are built and were green in `TestNPMHA`'s drain variant A (nightly run 41902592855) | NuGet registrations are not requested until the drain under NuGet load exists; the set runs on the npm tests' scatter profile, whose edge keeps no connection to a pod alive |

**Choice:** (c).

**Rationale:** the drain per role replaces every pod of a role one after the other; what it adds over the node drain, which takes one
pod per role, is per-replica state. trove keeps rendered documents (packuments, NuGet registrations) in a per-replica cache
(`RenditionCache` in [internal/formats/host.go](../../internal/formats/host.go), `cache.rendition_bytes`), so after the drain of
`packages` every replica's cache is cold, while in the node drain two of three stay warm. Only document load shows whether the p99
stays within twice the warm-up's; the `generic-local` loop has no renditions. The npm load is already built and was green in
`TestNPMHA`'s drain variant A. The `dotnet` loops stay with phase 7, where the owner moved the drain part of E3.6 on 2026-11-01; the
browser journey loop has no test; the advisory sync's resume from its checkpoint after `SIGTERM` is covered by the node drain of
`TestVulnLong_R_VULN_004_FullSyncDrainedMidRun`, the same `SIGTERM` and checkpoint path. The long-transfer probes are no part of
`TestNPMHA`'s load and stay with the acceptance test's full load.

How (c) is applied (the development agent, 2026-11-05, after the first local run):

- Which documents a replaced replica has to render again: the npm document of a local repository goes through the rendition cache
  in `serveRendering` of [handlers_document.go](../../internal/formats/npm/handlers_document.go), the documents of a remote and of a
  virtual repository, full, abbreviated and per version, in `serveFull` of
  [handlers_remote.go](../../internal/formats/npm/handlers_remote.go); NuGet's registration index, pages and leaves and its
  flat-container version lists go through it for local, remote and virtual repositories. A document of `npm-all` on a replaced
  replica also misses the merged documents of the virtual repository (`proxy.merged_metadata_cache_bytes`) and the remote's stored
  documents (`proxy.metadata_cache_bytes`), both per replica.
- `npm ci` asks for tarballs only (R-NPM-046), so the documents of the load are k6's. With `TestNPMHA`'s set of 22 documents a
  replaced replica rendered 22 documents again, and the tarballs set the p99 of the mix: in the first local run the documents' p99
  was 402 ms in the warm-up and 400 ms in the drain window, the tarballs' 1,063 and 1,108 ms, the mix's 834 and 838 ms. The drain
  per role therefore asks for the document of every package the fixture locks (`npmLoadOpts.allDocuments`; `TestNPMHA` keeps its
  set), holds the documents' p99 to twice the warm-up's on its own, and checks that every replacement `packages` replica answered
  at least half of them with `200`, which shows that the cold caches were exercised.
- k6 cycles through its 406 documents in order at 75 documents/s, so the warm-up starts after 90 s of load, 16 cycles in which a
  replica has missed a given document with a chance of about (2/3)^16 (0.15 %): a warm-up that still rendered every document once
  would raise the bound.

What follows: the set `drain-roles` starts the npm client pods and the counting npm test upstream, and the test switches to the scatter
profile of the npm tests (three `api` replicas instead of two); the load runs 3.5 minutes before the first eviction (90 s to fill the
caches, 2 minutes of warm-up) and 3 minutes after the last replacement is ready. The edge with kept-alive connections to the pods stays
drained by `TestE14Drain` in the set `push`. The numbers of the local runs are in
[e2e.md](../developer/e2e.md#the-cut-into-roles-phase-6a). Revisited when the drain under NuGet load exists: its `dotnet` loops and
k6's NuGet share then join the drain per role.

**Applied in the spec:** R-HA-059 ("A drain per role"); [e14_drain_roles_test.go](../../test/e2e/e14_drain_roles_test.go),
[deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) (the set `drain-roles`), [docs/developer/e2e.md](../developer/e2e.md).

### Q-HA-95 · How the upgrade tests get and install the older release

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-020's and R-HA-064's acceptance criteria upgrade from a release that was published, not from a build of the working
tree, so the test needs that release's chart, its image and the dev values it was installed with. Releases publish the chart to
`https://guided-traffic.github.io/trove/` and the image to Docker Hub as `guidedtraffic/trove:<x.y.z>`; the release tags are in
git, with `deploy/dev/values-dev.yaml` of that commit. The dev cluster holds a database that the working tree migrated; the
upgrade is to run the migration Job on the schema of the older release, and there are no down migrations (R-HA-019).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The published artifacts, a reset installation (chosen) | `helm install trove trove --repo <repository> --version <x.y.z>` with the image `guidedtraffic/trove:<x.y.z>` (pulled by the kind nodes) and the dev values of the tag (`git show <tag>:deploy/dev/...`); before it the release is removed and the database and Valkey are emptied | The upgrade starts from what a user runs: the published chart, the published image; no build of an old tree; the old release is installed with the values its own repository used | Needs the tags in the checkout, network access to two registries, and a reset of the dev installation, whose data the set does not keep |
| (b) Build the old release from its tag | `git worktree` of the tag, `make image`, `helm install` from the directory | Works offline | Builds a second tree with the toolchain of today (one version of every tool), and tests an artifact no user has |
| (c) A second kind cluster or namespace for the old release | The old release in a namespace of its own, with its own database | Leaves the dev installation alone | The dependencies (PostgreSQL, Valkey, S3, the IdP, the edge) are cluster-wide singletons of the dev profile; a second set doubles the 17 GB the machine has, and the edge and the client pods would address two installations |
| (d) Migrate the database back | Roll the schema back to the old release's | Keeps the data | There are no down migrations (R-HA-019) |

**Choice:** (a).

**Rationale:** the criterion is about the transition a user performs, so its input is the published release. The reset costs a few
minutes and only the dev installation's rows, which every set builds again from its fixtures (they are idempotent); the object
store keeps its blobs, which nothing references. The set ends by returning the cluster to the current dev release
(`restoreCurrentRelease`), so the sets after it find what they expect. Revisited when a release stops being installable by the
dev stack (a dependency that its dev values need and the stack no longer provides): then the oldest start is the oldest release
the stack serves, and the cut test needs its own stack.

**Applied in the spec:** R-HA-064 and R-HA-020 (the status lines). Code: [upgrade_env_test.go](../../test/e2e/upgrade_env_test.go),
[release-values.sh](../../deploy/dev/release-values.sh) and `release_values` in [lib.sh](../../deploy/dev/lib.sh) (the values of
up.sh, shared), [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) (the set `upgrade`), [docs/developer/e2e.md](../developer/e2e.md#the-upgrade-tests).

### Q-HA-96 · The migration check of the upgrade tests until the endpoint exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.
**Superseded** on 2026-11-06 by [Q-HA-116](#q-ha-116--the-upgrade-tests-read-get-apiv1systemmigrations-from-the-release-under-test):
the endpoint is built, and the tests read it; the CLI stays as the cross-check and for the rollback, as the revisit condition below
foresaw.

Context: R-HA-020's upgrade verification reads `GET /api/v1/system/migrations`. The endpoint is specified (R-HA-018) and its
document is the type `StatusReport` of [migrate.go](../../internal/meta/postgres/migrate.go), but no route answers it
(`grep -rn system/migrations internal api cmd` finds the type's comment only), so the request answers `404 not_found`. The same
document is what `trove migrate status` prints.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read `trove migrate status` in a worker pod (chosen) | The test runs the command in `deploy/trove-worker` and checks `pending`, `servable`, `binary.version` and that the schema the binary requires is applied | The same document, from the same code; no product change in a test task | It is the CLI, not the endpoint; the endpoint's authorization (`system.read`) is not exercised |
| (b) Build the endpoint | A handler in the `api` role, the OpenAPI entry, the permission, tests | The criterion as written | A product feature with its own tests, API contract check and security review, outside the scope of an e2e work package |
| (c) Leave the check out | The verification names two of three steps | Nothing to build | A pending step after an upgrade would go unseen |

**Choice:** (a).

**Rationale:** the check's value is the answer (no pending step, the binary can serve the schema), and the CLI gives it from the same
code. The endpoint is reported as unbuilt in the status line of R-HA-020 so that it is not taken for built. Revisited when the
endpoint is built: the test then reads it through the administrator's token and the CLI check stays as the cross-check.

**Applied in the spec:** R-HA-020 (the status line). Code: `upMigrateStatus` in [upgrade_env_test.go](../../test/e2e/upgrade_env_test.go).

### Q-HA-97 · The loops of the upgrade tests

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-020's criterion names `npm ci` and `dotnet restore` loops against a remote and a local repository; E6a.7 adds the UI
journey, R-HA-052 "Rolling upgrade" the load of its table. The drain tests already run the npm load of R-HA-059 with k6 and
eight loops ([Q-HA-94](#q-ha-94--the-load-of-the-drain-per-role)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The full load of R-HA-059 | Eight `npm ci` loops, `dotnet` loops, publishes, k6 at 150 requests/s, the 1 GiB probes | The strongest statement | The upgrade replaces every pod of an installation that has to carry that load on a laptop (the bridge doubles the pods); the loops would fail for lack of CPU, which is not the upgrade |
| (b) Client loops through one virtual repository over a remote and a local member, the browser journey, no k6 (chosen) | Three `npm ci` loops of the 400-package lockfile, one `npm publish` every 10 s, two `dotnet restore` loops, one browser journey per iteration; every iteration is recorded and must exit 0 with the client's own retries | Each iteration reads a local and a remote package; the write path (publish) and the session across releases (the browser) are covered; the number of loops fits the dev cluster during the bridge | Not the load of the acceptance table; the throughput and latency bounds are the drain tests' |
| (c) Loops against the remote and against the local repository separately | Two repositories per loop kind | Literal reading of the criterion | A lockfile names its registry in every `resolved` URL, so a second registry needs a second lockfile; the virtual repository makes the same requests of both members |

**Choice:** (b).

**Rationale:** the question an upgrade asks is whether a client command or a page load meets a moment without a backend, a session
or a document the other release cannot read; that needs clients of every kind running through the whole window and every
iteration counted, not a high rate. The drain tests carry the rate. Each loop must have an iteration inside each step's window, so a
loop that stalled outside the window cannot pass the step. Revisited when the upgrade runs on a larger machine in CI: the
`allDocuments` k6 load of the drain per role can join.

**Applied in the spec:** R-HA-020 and R-HA-052 "Rolling upgrade" (the status line). Code: [upgrade_loops_test.go](../../test/e2e/upgrade_loops_test.go),
[web/e2e/upgrade](../../web/e2e/upgrade), [web/e2e/playwright.upgrade.config.ts](../../web/e2e/playwright.upgrade.config.ts).

### Q-HA-98 · The connection budget of the bridge in the scatter profile

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first run of the upgrade test failed at step 1: the migration Job of `helm upgrade --set web.enabled=true` refused with
`the connection budget needs 403 connections but the server allows 400 (max_connections)` (R-HA-016: main pools 360 for the 18
Deployments' pods at their surge, lease pools 24, migration 2, backup 4, reserved 10, superuser reserved 3). The upgrade tests run in
the `scatter` profile, which has `api` 3 (a third replica to tell the scatter from an alternation), so the bridge there holds `web` 3,
`api` 3, `packages` 3, `worker` 2 and the pool `vuln` 2, each with a surge of 1. [Q-HA-87](#q-ha-87--the-dev-profiles-sizing-of-the-roles-and-its-postgresql-connection-budget)
sized the server for `api` 2: 383 with the bridge.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `max_connections` 450 on the profile's CloudNativePG cluster (chosen) | The server allows the 403 of the bridge in the scatter profile and 47 more | The refusal is the chart's own check, working as specified; the profile's values stay the ones of every other set | A change of the parameter restarts the instances once on an existing cluster (`make dev-up` applies it; a new cluster starts with it) |
| (b) Run the bridge steps with `api.replicaCount=2` | One pod fewer | No change of the server | The steps would run with values that differ from the profile's, and the third `api` replica of the scatter profile is gone for the part of the test that needs it |
| (c) Turn the check off for the test (`database.preflight.connection_budget: warn`) | The Job logs the shortfall | No restart | The test would stop exercising the check that a user meets; a shortfall that is real would go unseen |

**Choice:** (a).

**Rationale:** the profile's server was sized for the pods it runs at their surge; the scatter profile with the bridge needs three
more pods' budget than the number of Q-HA-87 counted. The check refused for the right reason, and the fix is the number it printed.
Revisited when the sizing of a role or `database.pool.max_conns` of the profile changes.

**Applied in the spec:** [deploy/dev/postgres.yaml](../../deploy/dev/postgres.yaml) (`max_connections: "450"`),
[local-profile.md](../developer/local-profile.md), [e2e.md](../developer/e2e.md).

### Q-HA-99 · The UI's hashed assets across an upgrade

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first complete run of `TestUpgrade_R_HA_064_AcrossTheCut` (E6a.7) failed two iterations of the browser journey, both
inside step 1: the login page stayed blank. The trace shows why: `index.html` came from a pod of one release and the requests for
`main-<hash>.js`, `polyfills-<hash>.js` and `chunk-<hash>.js` went, one by one, to pods of both releases
(`trove-web-59876cdf84-*` of v0.16.0 and `trove-web-5bb78c9659-*` of the image under test) and answered `404` on the pods of the
other one, because Angular names every asset after its content and the two releases built different bundles. R-ARCH-004 serves them
`immutable` and `index.html` `no-cache`, which is right for one release and says nothing about two that run side by side. Every
other loop of the test (141 `npm ci`, 27 publishes, 186 `dotnet restore`) passed, and the rolling upgrade from v0.19.0, whose bundle
is the working tree's, ran 71 journey iterations without a failure.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the criterion and the test strict | Any failed journey iteration fails the test | The criterion as written | It cannot pass for the cut: the pods of v0.16.0 cannot be changed, and a page that starts on a new `index.html` meets them for as long as the window lasts; the test would be red for every release that changes the bundle |
| (b) Count the failure of the asset skew as a finding, gate every other failure (chosen) | The journey prints `ASSET-SKEW <url> <replica>` for each `404` of a hashed asset under `/ui/<locale>/`; an iteration that failed with such a line is logged and counted, one that failed without it fails the test | The finding stays visible in every run (a count and the URLs); sign-in, session, the management API and the screens are still gated across the releases | A real failure of the user's page load is let through the test |
| (c) Make the product serve the other release's assets | The image carries the previous bundle, or the edge pins a browser to one release (cookie affinity) | Removes the failure for a user | Needs a release pipeline step, or an edge feature that not every ingress controller has, and it cannot reach the pods of releases that are already published; an owner decision about the UI's serving (R-ARCH-004), outside this work package |
| (d) Retry the page load in the journey | The journey reloads on a blank page | Green | Hides the finding and tests a journey no user has |

**Choice:** (b) for the cut from v0.16.0 alone, whose pods cannot be changed any more; (a) for every other upgrade test, the
rolling upgrade of R-HA-020 among them, with (c) as the product's open work (finding F-58 in
[promt.md](../projektplan/promt.md#8--open-findings)). `requireClean` takes the release whose skew it counts; the rolling upgrade
passes none, so a skew there fails the test.

**Rationale:** the test's job is to find what an upgrade breaks, and it found this; hiding it (d) or leaving a test red that nothing
in this repository can turn green (a) would both cost more than counting it. What does not change is every other pass condition:
sign-in across releases, the replica registry, the local and the remote repositories, publishes. A user who meets the skew presses
reload, which fetches a consistent set from whichever pods answer. The tolerance is not extended to the rolling upgrade: between two
releases that are both built from this repository the skew is a product defect that (c) can remove for every later upgrade, and the
v0.19.0 run passed only because its bundle equals the working tree's, so the first release that changes the bundle makes that test
red until (c) is built. Revisited when (c) is built: the cut test keeps (b) for v0.16.0 only.

**Applied in the spec:** R-HA-020 (the sentence on the UI in the mixed-version list), R-HA-064 and the roadmap's E6a.7. Code:
`upCmd.Skew` and `requireClean` in [upgrade_loops_test.go](../../test/e2e/upgrade_loops_test.go),
[web/e2e/upgrade/16-upgrade.spec.ts](../../web/e2e/upgrade/16-upgrade.spec.ts).

**Built (2026-11-06):** option (c) is the shared asset store of
[Q-HA-120](#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release) to
[Q-HA-123](#q-ha-123--when-a-replica-publishes-its-build), between releases that both have it. As decided above, the cut test keeps
(b) for v0.16.0 only, whose pods cannot be changed; every other upgrade test stays strict, the upgrade across two bundles of
[Q-HA-124](#q-ha-124--how-the-upgrade-tests-prove-an-upgrade-between-two-bundles) and the rolling upgrade from a release without the
store ([Q-HA-125](#q-ha-125--the-rolling-upgrade-from-a-release-without-the-store-stays-strict)) among them.

### Q-HA-100 · The NuGet part of R-HA-059's load and how it runs beside npm's

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-059's table has two `dotnet` rows (four `dotnet restore --no-cache --force` loops of a fixture with 60 packages through
`nuget-all`; a `dotnet nuget push` of a new version to `nuget-local` every 10 s) and a k6 row of 150 requests/s of which 50 % are npm
package documents, 30 % npm tarballs and 20 % NuGet registration and flat-container requests. The npm part runs in `TestNPMHA`
([Q-HA-65](#q-ha-65--the-npm-part-of-r-ha-059s-load-before-nuget-exists-and-how-its-commands-are-counted)): 120 requests/s of
[k6/npm.js](../../test/e2e/k6/npm.js), which `TestDrainRoles` and `TestNPMPerf` also use. Phase 7 builds the NuGet part
([Q-NUGET-73](formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs)).

| Question | Options | Choice |
|---|---|---|
| How k6 reaches the 150 requests/s with the 50/30/20 mix | (a) a second script, [k6/nuget.js](../../test/e2e/k6/nuget.js), at 30 requests/s beside `npm.js` at 120 (four in ten registration indexes, three flat-container version lists, three package downloads; every second round conditional with the ETag the test read); (b) one script with the three-way mix, replacing `npm.js`; (c) a switch in `npm.js` that adds the NuGet requests | (a). (b) and (c) change a script that `TestDrainRoles`, `TestNPMPerf` and the npm tests parse line by line, for a mix that two processes at fixed rates reproduce exactly (arrival rates add up). Each script keeps its own VU pool, so a slow NuGet request cannot starve the npm tarballs of VUs |
| The p99 bound of R-HA-059 | (a) the p99 of all k6 requests of both scripts together (the spec's "k6 requests"), the NuGet requests' own p99 reported; (b) additionally gate the NuGet requests' own p99 at twice their warm-up's | (a). The documents of NuGet are small and their p99 is a few milliseconds, so twice the warm-up is a bound on scheduling noise, not on the drain; the tarballs of 1 to 5 MiB set the p99 of the mix, as [Q-HA-94](#q-ha-94--the-load-of-the-drain-per-role) found. Revisit with (b) if a drain regression ever shows in the NuGet requests alone |
| How a `dotnet` command is counted | (a) as an `npm` command: a shell loop per load source detached in its client pod, one record per command with its exit status, the output of a failed one kept; a restore that exits `0` but extracted another number of packages than the fixture has counts as failed (exit status 96), and a push that exits `0` without printing "Your package was pushed." too (97); (b) the exit status alone | (a). `dotnet` exits `0` on some failures ([e2e.md](../developer/e2e.md#the-nuget-clients)) |
| Why a restore reaches the server | (a) `--no-cache --force` and a new, empty `NUGET_PACKAGES` and `NUGET_HTTP_CACHE_PATH` per iteration, removed afterwards; (b) the flags alone | (a). With the flags alone the global packages folder of the loop's first iteration satisfies the later ones and a restore asks for the registrations only |
| Tokens | (a) another service account, `e2e-nuget-ha`, with two tokens per restore loop (alternated per command), three for k6, one for the publisher; (b) the npm account's | (a). The account holds at most 20 active tokens (`auth.tokens.max_active_per_principal`); the npm load uses 20 |

**Rationale:** the pass conditions of R-HA-059 are the clients' and k6's, whichever format the request is; the second k6 process and the
second set of loops reproduce the table's rates without touching the scripts the other tests share. The development agent revisits the
split when the long-transfer probes join the drain (they are a third load and need a third script) or when k6's NuGet requests must
follow the corpus of a customer endpoint.

**Applied in the spec:** none; R-HA-059 already states the table. [../developer/e2e.md](../developer/e2e.md#drain-and-failover-under-nuget-load),
[test/e2e/nuget_load_test.go](../../test/e2e/nuget_load_test.go), [test/e2e/nuget_ha_test.go](../../test/e2e/nuget_ha_test.go).

### Q-HA-101 · How R-ARCH-024's drain criterion is counted

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first acceptance criterion of R-ARCH-024 asks that `kubectl drain` runs "during a loop of 100 `npm ci`, 20 `npm publish`,
100 `dotnet restore` and 20 `dotnet nuget push`" through the Service, all with default retries, with no failed operation and every
published version installable afterwards. R-HA-059's drain test runs a load of parallel loops of the same four commands for 5 minutes
and more; its variant A is the run that satisfies the criterion ([Q-NUGET-73](formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commands the load of the drain run completed count; the test asserts at least 100, 20, 100 and 20 (chosen) | `fullHAResult.conditions` in [nuget_ha_test.go](../../test/e2e/nuget_ha_test.go), in the drain subtest that names R-ARCH-024 | One run, one set of pass conditions; a load that falls behind (a runner too small for eight `npm ci` and four `dotnet restore` loops) fails the criterion visibly instead of passing with fewer commands | The commands before and after the drain count too |
| (b) Only the commands that overlap the drain count | Count by the interval of `kubectl drain` | Closer to "during" | `drain.sh` returns in 40 to 60 s; 100 `dotnet restore` in that interval needs a rate the criterion's loop does not imply |
| (c) A test of its own with fixed counts | A sequential loop of exactly the four counts around a drain | The numbers literally | A second drain test with a load that R-HA-059 already covers with more clients in parallel |

**Choice:** (a). **Rationale:** "a loop of 100 commands" describes the length of the load the drain runs in; the run of variant A is that
load, and its pass conditions (every command exits `0`, every acknowledged publish installs) are the criterion's. The development agent
revisits this if the owner wants the count restricted to the interval of the drain.

**Applied in the spec:** R-ARCH-024's first acceptance criterion names the test; [../developer/e2e.md](../developer/e2e.md#drain-and-failover-under-nuget-load).

### Q-HA-102 · The CPU of the NuGet drain and where its leg runs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the leg `nuget-ha` (job `e2e-nuget` of the nightly workflow) runs `TestNuGetHA`, which carries the whole load of R-HA-059's table for
both clients. The job ran on GitHub-hosted `ubuntu-24.04` with 4 vCPUs since 2026-11-05, because the kind legs that load the NuGet clients image
were evicted from `large` for disk (`kind load` copied the 2.3 GB image into four nodes). Measured on the local profile
([e2e.md](../developer/e2e.md#drain-and-failover-under-nuget-load)): the load used 16.1 cores on average over the drain (peak 17.1), 10.3 of
them on the control-plane node that hosts the client pods and the edge, 5.0 on the workers, 0.8 in k6; Docker had 20 CPUs, and two runs
failed from starvation.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the leg on `ubuntu-24.04` (4 vCPUs) | No change | Nothing to move | A quarter of the CPU the scenario used: the clients alone would run for minutes per command, the p99 condition would measure the runner, and the leg would be red every night for capacity (the situation Q-NUGET-73 avoided) |
| (b) Move the leg to `large` (limit 16 CPUs, 20 GiB) and load the clients images into the control-plane node only (chosen) | `runs-on: large` for `e2e-nuget (nuget-ha)`; `deploy/dev/e2e.sh` runs `kind load docker-image --nodes <cluster>-control-plane` for both clients images, so the 2.3 GB image occupies one copy of the runner's Docker disk (30 GiB) instead of four | The scenario's load fits the limit of `large` at the edge of what was measured; the disk reason for leaving `large` no longer applies | 16 CPUs is the measured mean, the peak is above it, and the measurement is from an arm64 laptop, not from the runner; the first nightly on `large` is the evidence |
| (c) Scale the load down on the small runner (fewer loops, a lower rate) | A reduced R-HA-059 table | Fits 4 vCPUs | It is not the acceptance test of R-HA-059 any more; the conditions would pass on a load the spec does not name |

**Choice:** (b). **Rationale:** the load is the spec's and cannot shrink; the one reason the NuGet legs left `large` is removed by loading the image
into the one node that uses it (verified locally: `make e2e SET=nuget-clients` 43 s instead of 4.5 minutes, the pods start). Not verified: the
scenario on a runner of either class. The development agent revisits this when the first nightly runs on `large` (a red leg for capacity would
mean a larger class, not a smaller load).

**Applied in the spec:** none. [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) (`--nodes` for the two clients images). The job's `runs-on` in
[performance.yml](../../.github/workflows/performance.yml) is the CI change of the same phase, not made in this decision's commit.

### Q-HA-103 · The node-loss bound is counted from the moment the cluster declares the node lost

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the node-loss row of R-HA-052 requires "replacement pods ready within `nodeFailureTolerationSeconds` + 60 s". The chart sets the
`node.kubernetes.io/unreachable` toleration of every pod to `nodeFailureTolerationSeconds` (60 s), and Kubernetes starts that clock when the
node controller puts the taint on the node, which it does after `node-monitor-grace-period` of the controller manager (about 40 s plus
the controller's period; 53 s in the local runs). Between `docker stop` and the taint nothing in the chart acts.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) From `docker stop` | The bound includes the cluster's detection time | The client's view | The grace period is a setting of the cluster, not of the chart; 120 s cannot hold with the 53 s of detection, 60 s of toleration and the start of a pod |
| (b) From the taint `node.kubernetes.io/unreachable:NoExecute` (chosen) | The `timeAdded` of the taint is the start | It is the moment the toleration clock starts, so the bound measures what the chart configures plus the start of the replacement | The detection time is reported beside it |
| (c) From `docker stop`, plus the grace period of the controller manager | The bound is 120 s plus the cluster's | Includes everything | The test would read a setting of the node image's control plane |

**Choice:** (b). The test reports both durations (`docker stop` to the taint, taint to the last replacement ready).

**Rationale:** the row asks what the chart's tolerations buy; detection is the cluster's. The development agent would revisit it if the chart ever sets
a kubelet or controller-manager value.

**Applied in the spec:** R-HA-052, the node-loss row.

### Q-HA-104 · The node of the node-loss scenario

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first run of `TestHASuite_NodeLoss` used the node of the drain's variant A (`api`, `packages` and `worker` pod, not the PostgreSQL or
Valkey primary). The node also carried the CloudNativePG operator, which the local profile runs once, and the PostgreSQL standby. The primary's
commits wait for the synchronous standby (`dataDurability: preferred`) until the operator drops it from the set; with the operator gone
with the node that took until Kubernetes had replaced the operator after its own default toleration of 300 s (the operator pod was 4 minutes
old when the run ended). In that time every replica's `/readyz` answered `503`, no replacement pod could be ready and 34 of 458 `npm` commands failed
with the `503`. That is the loss of a dependency's operator, which the PostgreSQL rows of R-HA-052 cover, not of trove's pods.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The node of variant A | `drainNodeVariantA` | One rule for the drain and the node loss | Takes the operators with it |
| (b) A node with an `api`, a `packages` and a `worker` pod and neither a primary, nor an operator, nor the standby where one exists (chosen) | `nodeLossNode`: an operator or a primary in the way is moved first, before the load starts; the node with the standby is taken only if no other qualifies and the test then says so | The scenario measures trove's replacement pods, the lease and the clients; no other row's failure is mixed in | The standby is not lost together with the pods, which the PostgreSQL rows cover |
| (c) Run the operators with two replicas in the local profile | Change the dev profile | Realistic for production | The chart under test would not change; the operators are not trove |

**Choice:** (b).

**Rationale:** R-HA-052's rows are single failures. The development agent would revisit it if the local profile runs its operators with two replicas.

**Applied in the spec:** R-HA-052, the node-loss row. The way the node is found was changed by [Q-HA-106](#q-ha-106--the-node-of-the-node-loss-scenario-is-chosen-first-and-cleared-not-searched-for); the precondition is the same.

### Q-HA-105 · What the npm loops may see of a lost node

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the node-loss row of R-HA-052 states no client condition, and the other scenarios under npm load require that every `npm` command exits `0` with npm's
default retries. On the loss of a node that hosts two of the three `packages` pods, three of 445 `npm ci` commands (`ci0`, `ci2`, `ci4`) failed with `ECONNRESET`
after 37 to 40 seconds; each had been running when the node stopped (started one second before), no command that started later failed, and the k6 load, which does
not retry, saw `502` and connections without an answer (781 of 39,642 requests), none after the replacements were ready. Why: the node's network is gone at once, so a
request in flight on one of its pods is never answered and never reset; the controller manager declares the node lost only after `node-monitor-grace-period` (44 s in
the run), and until HAProxy's health check (`check inter 2s`) takes the pod out of rotation a second attempt may go to a dead pod again. npm's two
retries (waits of 10 s and up to 60 s, requests that hang until the edge's `timeout server` of 50 s) are used up in under 40 s. Measured: the three commands, their duration, the
detection time and that no later command failed. Not verified: which attempt of each command hung on a dead pod and which was refused (the edge's log of the failed
requests was not read), and that the edge keeps sessions of a pod it has marked down. That is the behavior of the cluster and the edge, not a defect of trove's pods; the
chart sets only the toleration (Q-HA-103).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every command exits `0` | The rule of the other scenarios | One rule | Fails on a cluster property that a trove release cannot change |
| (b) A command that was running when the node stopped may fail with a network error; every command that started later exits `0` (chosen) | `nodeLossCommands` | States what the clients see and bounds it to the commands the loss met; a failure of another kind or of a later command is a finding | The window of the failure is the cluster's detection time, not a number of the chart |
| (c) Shorten the cluster's detection | A `node-monitor-grace-period` of the kind control plane, `on-marked-down shutdown-sessions` on the edge | Fewer failed commands | A setting of the test cluster and not of the product; the scenario would test the tuned cluster |

**Choice:** (b). The test logs each failed command with its duration.

**Rationale:** R-HA-052's pass condition is about the replacement pods and the singleton work; the clients' part is what Kubernetes' failure detection leaves. The development
agent would revisit it if the edge's handling of sessions to a pod that was marked down is configured (`on-marked-down shutdown-sessions` was not tried).

**Applied in the spec:** R-HA-052, the node-loss row.

### Q-HA-106 · The node of the node-loss scenario is chosen first and cleared, not searched for

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `TestHASuite_NodeLoss` failed in the nightly run 42192042297 after 42 s with "no node hosts an api, a packages and a worker pod without the
PostgreSQL and Valkey primaries and the operators", on the same runner class on which it had passed the run before. Q-HA-104 searched for a clean node and moved the
blocker it met first (the operators' pods, a Valkey failover or a PostgreSQL switchover), four attempts at most. On the three workers of the kind cluster the
four blockers (two primaries, two operators) can leave no clean node, and every attempt re-places them: the log shows the Valkey failover, the PostgreSQL switchover, the
deletion of the operators' pods (the scheduler put them on the same two nodes again) and a second PostgreSQL switchover, and then the fourth attempt was used up.
Whether a clean node exists after one attempt depends on where the scheduler puts the operators, which the test did not control.
A clean node is not enough: the first run of the new search chose the node of the standby (three blockers moved), and the survivors' `/readyz` answered non-200
two or three times each (R-HA-008) and k6 saw 4,771 failures, because the primary's log shows `canceling wait for synchronous replication` for every commit from 10 s after the
stop, until the operator dropped the standby. The standby is the primary's synchronous partner (`dataDurability: preferred`), so a node with an instance is not a node whose loss
only costs trove its pods.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) More attempts | Raise the 4 | One line | Still a search whose outcome the scheduler decides; each attempt is a switchover and a rollout of about 30 s |
| (b) Choose the node first, then clear it (chosen) | The node without a PostgreSQL instance that hosts an `api` and a `packages` pod (three workers, two instances: there is one), else the candidate with the fewest blockers. A `worker` pod is brought there if it has none (the other workers are cordoned, one worker pod is deleted). The node is cordoned; the operators' pods are deleted (the scheduler cannot put them back), the Valkey primary is failed over, the PostgreSQL primary switched over; the node is uncordoned and the result is checked | Terminates in one pass; the pods land where the rest of the cluster is; a node without an instance costs the database nothing | The cordons are a cluster change during the run, undone before the load starts |
| (c) Run the operators with two replicas in the dev profile | Q-HA-104 (c) | Realistic | The chart under test does not change |

**Choice:** (b). `clearNode` in `test/e2e/node_loss_test.go` serves this scenario and the node-loss subtests of the mirror's failure suite ([Q-REPO-200](repositories-and-proxy.md#q-repo-200--the-node-lost-under-a-pass-is-cleared-of-the-operators-and-the-primaries-first)).

**Rationale:** R-HA-052's rows are single failures, so the precondition has to be made, not hoped for. The development agent would revisit it if the dev profile runs
its operators with two replicas.

**Applied in the spec:** R-HA-052, the node-loss row (unchanged text: the node still hosts no primary and no operator). The order of
the clearance was changed by [Q-HA-126](#q-ha-126--the-node-clearance-moves-the-primaries-before-the-cordon-and-failoversh-waits-for-what-a-switchover-needs):
the primaries are moved before the node is cordoned.

### Q-HA-110 · How the drain test measures the edge's endpoint-removal time

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-059 rule 4 asks the drain test to measure, for each edge, the time the edge needs to stop sending requests to a pod whose
readiness flipped, and to fail when `health.shutdown_drain_delay` is shorter. The origin is the moment the pod flips (`SIGTERM`, the
log line "shutting down"); the end is the last request that reaches the pod afterwards. The test runs in `TestE14Drain` (set `push`,
every change), `TestDrainRoles` (nightly) and the drain of `TestNPMHA`, on a cluster whose edge, nodes and test process need not share
a clock.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The pod's own log (chosen) | The line "shutting down" is the origin; the end is the latest arrival (the stamp of a `"log":"request"` line of a client listener minus its `duration_ms`) after it. Both come from one process, so one clock | No clock to align; needs nothing of the edge; counts exactly the requests that reached the pod, whichever edge pod or connection carried them; the kubelet's delay between the deletion and `SIGTERM` is excluded, as it is from the drain delay | Needs the pod's log to outlive the pod (a `kubectl logs -f` stream per victim, started before the eviction); a pod that gets no request after the flip yields no sample |
| (b) The edge's access log | The last line that names the pod's backend server after the pod's deletion | Names the edge's own view | HAProxy Ingress names the server by an index (`srv001`), not by the pod; the stamps of the edge and of the pods must be aligned; an edge that is not HAProxy (R-HA-039) logs another format |
| (c) The pod watch's deletion timestamp as origin | Requests in the pods' logs after the API server's `deletionTimestamp` | One origin for all pods | The origin is the API server's clock and precedes `SIGTERM` by the kubelet's delay, so the measured time is longer than the one the drain delay has to cover; needs the clocks of the test process, the API server and the pods to agree |
| (d) A scrape of the request counter | Poll the pod's request metric every second | No log stream | A pod that is drained answers its scrape endpoint for the whole delay; a resolution of 1 s is as long as the margin it is supposed to check |

**Choice:** (a).

**Rationale:** it is the only option that needs no alignment of clocks, so it cannot fail falsely for a skew between the runner, the
node and the edge. The streams are in the shared drain helpers ([drain_measure_test.go](../../test/e2e/drain_measure_test.go)), so
the three drain tests measure alike. The ops listener is excluded (the kubelet's probes). Revisit when an edge other than HAProxy
Ingress is part of the drain test (R-HA-039): its time is measured in the same way, since it only needs the pods' logs.

**Applied in the spec:** R-HA-059 rule 4; [drain_measure_test.go](../../test/e2e/drain_measure_test.go),
[e14_drain_test.go](../../test/e2e/e14_drain_test.go), [e14_drain_roles_test.go](../../test/e2e/e14_drain_roles_test.go),
[npm_ha_test.go](../../test/e2e/npm_ha_test.go), [docs/developer/e2e.md](../developer/e2e.md).

### Q-HA-111 · The default of health.shutdown_drain_delay

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the default was 5 s, proposed, and the dev profile raised it to 15 s after Actions run 41241903588 (the HAProxy Ingress of
the kind cluster on a CPU-limited runner kept sending requests to a drained pod for more than 5 s and four documents and four
tarballs got `502`). The drain tests now measure the time per evicted pod (Q-HA-110). The longest time between the readiness flip and
the last request that reached the pod, on the dev profile's HAProxy Ingress under the load of the drain tests:

| Where | Pods | Longest |
|---|---|---|
| Actions, `e2e-artifacts-push` of the runs 42057547730, 42116230650, 42121742101 and 42131510743 (`TestE14Drain`, 15 s delay) | 4 `api`, 4 `packages` | `api` 0.15 to 0.18 s; `packages` 1.79 s (9 requests after the flip), in the other three no request after it |
| Actions, `e2e-npm-artifacts-npm-ha` of the dispatch 42126196109 (drain variant A under the npm load: 8 `npm ci` loops, k6) | 1 `api`, 1 `packages` | `api` no request after the flip; `packages` 0.98 s (54 requests after the flip) |
| Local kind cluster, an idle host (`make e2e SET=drain`, 2026-11-06) | 1 `api`, 1 `packages` | `api` 0.14 s; `packages` no request after the flip |
| Local kind cluster on a CPU-starved host: load average 16 to 27 on 20 CPUs, the API server answering `TLS handshake timeout` (`make e2e SET=drain-roles`, 2026-11-06, first eviction of `api`) | 2 `api` | 13.4 s (a flow of about 100 requests/s for 10 s, then a burst of 9 requests at 13.4 s); the second pod of the run 1.3 s |

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep 5 s (chosen) | The default stays; it is 2.8 times the longest time measured on the CI runners (1.79 s) | A drained pod waits 5 s only; the shutdown budget of R-HA-011 (grace period minus delay minus 5 s) loses little | A host that starves its edge (the last row) needs more: there the drain test fails and names both numbers |
| (b) 15 s | The dev profile's value becomes the default | Covers the starved case | Every drain of every installation waits 15 s for an edge that needs under 2 s, and the shutdown budget shrinks by 10 s |
| (c) 10 s | Between the two | Over five times the longest time on the runners | Neither measured nor derived: the starved case still exceeds it |
| (d) Derive from the edge | The chart sets the delay from the Ingress class | Fits the edge | The chart cannot know an edge's propagation time; the measured times are of one controller |

**Choice:** (a), 5 s.

**Rationale:** the delay has to cover the edge's removal time, not the worst load a host can put on it, and the drain test is how an
installation finds out that its own edge needs more: it fails with both numbers (Q-HA-113). The value is the longest time measured
on the CI runners (1.79 s) with a margin of almost three, the margin an installation has before it must measure its own edge. The
starved case is a property of a host without CPU for its edge, not of the edge, and the guidance for it is to raise the delay (the
manual's limitations page says so). Revisit when a nightly run on a runner that is not starved measures more than 4 s (the bound of
Q-HA-113 on 5 s), or when the owner's production controller is measured.

**Applied in the spec:** R-HA-059 rule 4; the values of R-HA-031 and of the configuration in [operations-and-observability.md](operations-and-observability.md)
(the "proposed" mark removed); [internal/config/health.go](../../internal/config/health.go), [values.yaml](../../deploy/helm/trove/values.yaml) and
[values.schema.json](../../deploy/helm/trove/values.schema.json) keep 5 s and name the measurement; the manual's
[limitations page](../manual/admin/limitations.md).

### Q-HA-112 · Whether the dev profile keeps its drain delay of 15 s

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [values-dev.yaml](../../deploy/dev/values-dev.yaml) sets `health.shutdown_drain_delay: 15s` since run 41241903588. With the default
at 5 s (Q-HA-111) the override is the only difference between the dev profile and the default on this value. The profile is used by
CI and by a developer's laptop that also builds, runs the integration stack and other clusters.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep 15 s, say why (chosen) | The comment names the measurements instead of the run | The starved host of Q-HA-111 measured 13.4 s, and 15 s with the test's second of silence was just enough; the push leg does not turn red on a busy host | The dev profile does not run the default; the drain test cannot show the default failing |
| (b) Remove the override | The dev profile runs 5 s | The profile tests the default; the drain test fails when the edge is slower than 4 s | A starved CI runner or laptop fails `TestE14Drain` (set `push`, every change) for a cause that is the host's |
| (c) 10 s | Between | Halves the wait | A starved host measured 13.4 s: it would fail; chosen by feel |

**Choice:** (a).

**Rationale:** what the dev profile tests is that a drain under load loses no request; it needs a delay that holds on the hosts it
runs on, and one of them was measured at 13.4 s. The default's own sufficiency is shown by the measurement, which every drain test
logs and writes to `<name>-endpoint-removal.json` whatever the delay (the profile's 15 s does not change what is measured, only
how long the edge may take). Revisit when the owner wants the profile to run the default: change the one line and read the
`*-endpoint-removal.json` of several nightly runs first.

**Applied in the spec:** [values-dev.yaml](../../deploy/dev/values-dev.yaml) (comment), [docs/developer/e2e.md](../developer/e2e.md).

### Q-HA-113 · What the drain test fails on

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-059 rule 4 says the test fails when the delay is shorter than the measured time. The pod's listener accepts until the
delay ends, so a delay that is too short shows as requests until its last moment, and a measured time can hardly exceed the delay.
`TestE14Drain` is in the set that runs on every change.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Delay at least the measured maximum plus 1 s of silence; no sample is no failure, no sample at all is one (chosen) | Fails when the last request after the flip came less than 1 s before the delay ended | Can fail for a too-short delay, which "last request before the delay" cannot; a pod that no request reached after the flip (a low load on `packages`) is not a failure; a run that measured nothing is | 1 s is a fixed bound; a low load can understate the time |
| (b) Last request later than the delay | Fails only when a request came after the delay | The literal rule | A request after the delay hits a closed listener or never arrives: the measure is capped by the delay and cannot fail |
| (c) Every drained pod must show a sample | A missing sample fails | Strict | `packages` receives 4 requests/s in `TestE14Drain` and none after the flip in three of four CI runs: false failures in a per-change set |

**Choice:** (a).

**Rationale:** it can fail for the cause the rule names and not for the load being thin. The silence of 1 s is the smallest bound
that separates "the edge stopped" from "the listener closed on the last request"; the measured times are 0.2 to 1.8 s. Revisit when
the load of `TestE14Drain` on `packages` is raised so that every pod gets a sample.

**Applied in the spec:** R-HA-059 rule 4; [drain_measure_test.go](../../test/e2e/drain_measure_test.go) (`drainSilence`).

### Q-HA-114 · Which background migrations the migration report lists before R-HA-022's table exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the report of R-HA-018 (`GET /api/v1/system/migrations`, `trove migrate status`) has a member `background`, the rows of
R-HA-022's table `background_migration`; R-HA-020's verification ("the expected background migrations") and step 4 of R-HA-021 read
it. Neither the table nor its pause and resume operations are built. One background migration runs: the recomputation of NuGet's
SemVer 2 flag (`data_migration_nuget_semver2`, Q-NUGET-93), which records its completion in the `system_state` key
`nuget_semver2_recomputed` and its progress in its job rows. The table needs a migration step and moves the state of a migration
that installations have already run (finding F-59 is the endpoint, not the table).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An empty list until the table exists | `background: []` always | Nothing to build | The migration that runs is invisible; R-HA-020's "expected background migrations" cannot be checked, and the guard of R-HA-021 step 4 has nothing to read |
| (b) Each migration declared by its package, its state read from its completion record and its newest job (chosen) | `postgres.BackgroundMigration` (name, release, the `system_state` key of the completion, optionally the count of a completed run from that record); `app.BackgroundMigrations()` lists the binary's; the record makes it `succeeded`, a newest job `running` or `cancelling` makes it `running`, `failed` makes it `failed`, anything else (no job, queued, cancelled, a run that ended waiting for older replicas) is `pending` | The report shows today's migration from records that already exist; no schema change; the report and the guard read one function, so they cannot disagree; the document keeps R-HA-018's shape, so the table can replace the reader later | The state is derived: `paused` cannot occur until the pause operation exists, and once `jobs.retention` has removed the job rows `startedAt` is `null` and `processed` comes from the completion record or is 0 |
| (c) Build R-HA-022's table now | `background_migration` with its states, the NuGet recomputation moved onto it, pause and resume | The specification as written | A migration step, a move of a completed migration's state and the operations with their own tests and review; more than the finding needs, and Q-NUGET-93 names it as its own revisit condition |
| (d) The job rows of the kinds `data_migration_*` | Every kind with that prefix, its newest job's state | Nothing to declare | A completed migration vanishes or turns `pending` when `jobs.retention` removes its rows; the release that introduced it is in no job row |

**Choice:** (b).

**Rationale:** the report answers whether the background work an upgrade needs is done; (b) answers it right from the records the
migration keeps, and it keeps the report and the guard on one reader ([background.go](../../internal/meta/postgres/background.go)).
What a record cannot say (`paused`) belongs to operations that are not built. Revisited when R-HA-022's table is built (the reader
then reads its row, the document stays) or when a migration needs a state the records cannot express.

**Applied in the spec:** R-HA-018 (the members of the report), R-HA-021 and R-HA-022 (the status lines). Code:
[background.go](../../internal/meta/postgres/background.go) (`BackgroundMigration`, `ReadBackground`), `BackgroundMigrationJob` in
[migration.sql](../../internal/meta/postgres/queries/migration.sql), `Report` in [migrate.go](../../internal/meta/postgres/migrate.go),
`Semver2Background` in [semver2.go](../../internal/formatdata/nuget/semver2.go), [migration_report.go](../../internal/app/migration_report.go).
Tests: `TestBackgroundState`, `TestR_HA_022_TheRecomputationIsDeclaredAsABackgroundMigration`, `TestBackgroundMigrations`,
`TestR_HA_021_UpgradePreparation`, `TestMigrationStatus`.

### Q-HA-115 · How a contract step declares the background migration it depends on, and what the guard does with it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-022 says that a contract step that depends on a background migration's completion declares it and that the guard
refuses the step before the migration has `succeeded`; step 4 of R-HA-021 says the guard checks it. R-HA-018's header table and
R-ARCH-026 named no such declaration, and the guard read the replica registry only. No embedded step depends on a background
migration today; the first contract step that removes what a backfill converts needs the declaration.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A header `-- trove:requires-background <name>`, once per migration, contract steps only; the guard refuses with status `3` while one has not `succeeded` (chosen) | Parsed with the other headers; the guard reads the state with the report's reader (Q-HA-114); a name the binary does not declare fails the run with status `1`, and a unit test over the embedded steps keeps such a step out of a release | The dependency sits in the file whose SQL needs it, is checksummed with it and seen in review; the refusal and the report read one function; status `3` is the refusal that a retry cannot change (`FailJob`, R-HA-019) | One more header in R-ARCH-026's syntax |
| (b) A table in Go that maps step numbers to migrations | The guard looks the step up | No header | The dependency lives apart from the step; nothing stops a step from shipping without its entry |
| (c) Leave it to the operator's step 4 | The report shows the state, nothing enforces it | Nothing to build | R-HA-021 and R-HA-022 say the guard checks it; a skipped check removes data a backfill has not converted |
| (d) Refuse with status `1` | The Job retries | A short backfill may finish during the retries | A backfill runs on the workers of the running release for minutes to hours; three tries within 30 minutes are noise |

**Choice:** (a).

**Rationale:** a contract step that removes an old form is safe only after the backfill converted every row, and the place that
knows the dependency is the step's own file. Reading the state with the report's reader means an operator who sees `succeeded` in
step 4 meets no refusal, and one who sees `running` meets it with the same word. Revisited when R-HA-022's table exists (the guard
reads its row; the header stays).

**Applied in the spec:** R-HA-018 (the header table, the guard), [architecture.md](architecture.md) R-ARCH-026 (the header list,
the gate), R-HA-021 and R-HA-022 (the status lines). Code: [steps.go](../../internal/meta/postgres/steps.go) (`RequiresBackground`),
`backgroundGuard` and `CheckBackgroundDependencies` in [background.go](../../internal/meta/postgres/background.go), `contractGuard`
in [migrate.go](../../internal/meta/postgres/migrate.go), [cmd/trove/migrate.go](../../cmd/trove/migrate.go). Tests: the R-HA-022
cases of `TestParseSteps`, `TestCheckBackgroundDependencies`, `TestBackgroundMigrations`, `TestR_HA_021_UpgradePreparation`.

### Q-HA-116 · The upgrade tests read GET /api/v1/system/migrations from the release under test

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [Q-HA-96](#q-ha-96--the-migration-check-of-the-upgrade-tests-until-the-endpoint-exists) had the upgrade tests read `trove
migrate status` in a worker pod until the endpoint existed, and it exists now. The releases before it do not have the route, so
their pods answer `404`; right after a rolling upgrade the edge can still reach a pod of the release before while it drains
(R-HA-011). The rollback test ends on the release before, which has no route at all.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The endpoint with the administrator's token through the edge, after the upgrade, accepted only from a replica of the release under test; a `404` or another `binary.version` is read again for up to 90 s; `trove migrate status` in the worker as the cross-check; the rollback read with the command (chosen) | `upMigrations` and `requireMigrationsDone` | The criterion as written; the route on the `api` role, the edge and `settings:read` are exercised; the cross-check pins that the endpoint and the command agree | A read can take more than one request while the edge drops a pod of the release before |
| (b) The endpoint, failing on any `404` | One request | Strict | A draining pod of the release before answers `404` for a route its release never had; that is no defect of the release under test |
| (c) Keep the command alone (Q-HA-96) | No change | Proven | The endpoint is not exercised end to end |
| (d) A port-forward to one `api` pod of the new ReplicaSet | No edge | One request | Bypasses the edge and the routing by role that an operator's read meets |

**Choice:** (a).

**Rationale:** only an answer that is provably not from the release under test is read again; every answer of that release is
gated, and every other status fails the test. R-HA-020's "the expected background migrations" is read as every migration the
binary declares reaching `succeeded` within 3 minutes, a `failed` one failing at once. The cut test runs the same check after
its step 2. Revisited when every release an upgrade test starts from has the route (the `404` branch then never matches).

**Applied in the spec:** R-HA-020 (the status line), Q-HA-96 (superseded). Code: `upMigrations` and `requireMigrationsDone` in
[upgrade_env_test.go](../../test/e2e/upgrade_env_test.go), [upgrade_test.go](../../test/e2e/upgrade_test.go),

### Q-HA-120 · How a replica serves a hashed UI file of the other release

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Builds
option (c) of [Q-HA-99](#q-ha-99--the-uis-hashed-assets-across-an-upgrade).

Context: finding F-58. During a rolling upgrade a page takes its `index.html` from a replica of one release and its scripts,
stylesheets and fonts, one request each, from replicas of both. Every one of them is named after its content (R-UI-001), so the
replicas of the other release answer `404` and the page stays blank. R-HA-020 asks that what a client carries between requests is
understood by both releases; the names an `index.html` hands the browser are such a thing. A tab opened before the upgrade loads the
lazy chunk of a route it opens later from the replicas that run then.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A shared, content-addressed asset store (chosen) | Every replica that serves the UI (`api`, `web` in the bridge, `all`) publishes the hashed files of its embedded build at start-up; a request for a hashed file its own build lacks is answered from the store with the headers of its own files | Both directions of the window, any number of releases inside the retention, the lazy chunks of a tab left open, independent of the edge | A table, a data path and a write at every start; a read of the store per foreign file; works only between releases that both have it ([Q-HA-125](#q-ha-125--the-rolling-upgrade-from-a-release-without-the-store-stays-strict)) |
| (b) The image carries the previous release's bundle | The release pipeline adds the last release's build next to its own | No shared state | One direction only: the pods of N serve the files of N−1, the pods of N−1 still answer `404` for N's; a skipped release or a rollback is not covered; the pipeline fetches and embeds an older build |
| (c) The edge keeps a browser on one release | Cookie affinity at the ingress controller | No change in trove | A feature of some controllers and not of others; a rolling upgrade deletes every pod, so the pinned pod goes away in the middle of a page and the next request lands on the other release anyway |
| (d) A script that reloads on a failed load | `index.html` reloads once when a module fails to load | Small | A retry, not a fix: the reload can meet the same mix again; an inline script needs its own nonce in `script-src` (R-UI-001 allows `'self'` only); a lazy chunk of an open tab is not covered |

**Choice:** (a).

**Rationale:** it is the only option that removes the failure in both directions of the window and for the open tabs, and it needs
nothing outside trove. The rest of the design follows in Q-HA-121 to Q-HA-123, the names in
[Q-UI-140](api-and-ui.md#q-ui-140--which-file-names-of-the-ui-build-are-hashed) and the answers in
[Q-UI-141](api-and-ui.md#q-ui-141--what-a-replica-answers-for-a-hashed-file-of-another-release). Revisit if the store's cost at start-up
(Q-HA-123) is found to delay the readiness of an upgrade.

**Applied in the spec:** R-HA-020 (the UI's files in the mixed-version list), R-UI-001 (serving), the area `ui-assets` of
[architecture.md](architecture.md) (Q-ARCH-332). Code: [internal/webui/shared.go](../../internal/webui/shared.go),
[internal/webui/shared/](../../internal/webui/shared/), [internal/app/wire_ui_assets.go](../../internal/app/wire_ui_assets.go).
Tests: `TestSharedAssets` (internal/webui), `TestR_HA_020_SharedUIAssets` and `TestR_HA_020_SharedUIAssetsWithoutTheObjectStore`
([test/integration/uiassets](../../test/integration/uiassets/uiassets_test.go)), `TestUpgrade_R_HA_020_AcrossTwoBundles`.

### Q-HA-121 · Where the shared UI files live

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [Q-HA-120](#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release) needs a store that every replica reads and
writes. A build has 135 hashed files per locale, 2.4 MB per locale, 4.8 MB for the two (a build of 2026-11-06); the scripts of
the two locales share their names and differ in content where a translation is inlined.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The blob store, named by a table (chosen) | The content is a blob (R-STOR-002, spooled, hashed, committed by the protocol of R-STOR-005); a row of `ui_assets` (locale, path, blob, size, publisher, `seen_at`) names it and holds the reference that keeps the collector away (R-STOR-024); a read verifies the digest (R-STOR-013) | INV-4 holds; the files of both locales and of every release are stored once by content; deleting a row hands the blob to the collector; the replicas keep what they read in memory (Q-UI-141) | A file of the other release costs an object read on the first request a replica gets for it; while the object store is unavailable those files answer `503` |
| (b) PostgreSQL rows with the content | A `bytea` column | One store, transactional | Contradicts INV-4 ("No binary content is stored in PostgreSQL") |
| (c) Valkey | Keys with the content | Fast | INV-3: nothing trove serves to a client is read from Valkey, and losing Valkey must cost performance only |

**Choice:** (a).

**Rationale:** the blob store is where trove keeps content, with the deduplication, the collection and the verification it needs
already; the table adds only the names. The table's export class is transient (R-BAK-004): a restore leaves it empty and the replicas
publish again (Q-HA-123). Revisit if the object read of a foreign file is measured to slow a page during an upgrade.

**Applied in the spec:** R-HA-020. Code: migration `00043_ui_assets.sql`, [tables_ui.go](../../internal/meta/postgres/tables_ui.go),
[queries/ui_assets.sql](../../internal/meta/postgres/queries/ui_assets.sql), [shared.go](../../internal/webui/shared/shared.go).

### Q-HA-122 · How long the files of a release stay in the store

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the store must hold the files of every release whose pages may still be open, and must not grow with every build: a build is
about 5 MB, and the dev profile builds several a day.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Renewed by the replicas that carry a file, removed 7 days after the last renewal (chosen) | Every replica renews the rows that hold its build's own bytes at start-up and every hour (`seen_at`, rows renewed in the last half hour are left alone; a row of the same name with other bytes is not renewed, [Q-UI-142](api-and-ui.md#q-ui-142--which-bytes-the-shared-store-keeps-for-a-name-that-two-builds-fill-differently)); every replica deletes the rows no replica renewed for 7 days, and the delete trigger queues their blobs | A release's files stay while one of its replicas runs and a week after; no build identity or ordering is needed; a file both releases have with the same bytes is kept by either | A file stays up to a week after it is needed; 7 days is a constant |
| (b) The files of the last N builds | A build table ordered by publish time | A bounded count | Needs a build identity; a build whose replicas still run (a pool that rolls later, a rollback) loses its files once N newer builds were published |
| (c) The versions of the replica registry | Keep the files of the versions the registry lists | Uses what exists | A version does not name a build (every dev build is `dev`), and a row leaves the registry with its replica, so the open tabs of that release lose their lazy chunks at once |
| (d) Never removed | Write-once forever | Simple | Grows by a build at every upgrade, without bound |

**Choice:** (a), with the retention of 7 days.

**Rationale:** the renewal ties a file's life to the replicas that carry it, which is what an open page depends on, and a week covers
a tab left open over a weekend. The constants are `shared.Retention` and `shared.RenewEvery`. Revisit if an operator needs another
retention (a configuration key) or the store's size is a concern.

**Applied in the spec:** R-HA-020. Code: [keeper.go](../../internal/webui/shared/keeper.go) (`Keeper`), the statements `TouchUIAssets`
and `PruneUIAssets`. Test: `TestR_HA_020_SharedUIAssets` (renewal, a restore's empty table, the retention and the queued blobs).

### Q-HA-123 · When a replica publishes its build

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a new replica's `index.html` names files that only its own build has. The replicas of the other release can serve them only
once they are in the store.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Before the replica turns ready, for at most a minute (chosen) | The wiring step `ui-assets` (R-ARCH-024 step 8) publishes before the client listener opens; a store that does not take the build within `shared.PublishWait` (1 minute) is logged and the replica starts without it; the keeper tries again every minute | A page from a new replica finds every file it names on the old replicas from its first answer; a store that refuses writes does not take the UI and the management API down | The first replica of a build commits its files at start-up (on the dev cluster on 2026-11-06: 244 new files of 270 in 1.2 s, the next replica of the build 7 ms); a store that fails delays the readiness by a minute |
| (b) In the background after the replica is ready | No cost at start-up | Fast start | The first pages of a new replica can meet the `404`, and the old replicas remember the miss for 10 s (Q-UI-141) |
| (c) Before ready, without a bound | The replica waits until it has published | The skew can never happen | An object store that passes the start-up check and refuses writes keeps every new `api` replica unready: an upgrade pauses, and a restart of all of them (a node drain) loses the UI and the management API for a convenience of the UI |
| (d) In the migration Job | Once per upgrade | No cost per replica | The Job has no spool or blob store; a restore, a rollback and the role `all` would publish nothing |

**Choice:** (a).

**Rationale:** reliability first: the store serves the UI's pages of the other release; it must not decide whether trove serves at all.
The object store's start-up check (R-STOR-035) runs before, so a publish that fails after it is the exception. Revisit if the
publish is measured to take a large part of an upgrade.

**Applied in the spec:** R-HA-020; the area table of [architecture.md](architecture.md) (`ui-assets`). Code: `wireUIAssets`,
`Keeper.PublishAtStart`.

### Q-HA-124 · How the upgrade tests prove an upgrade between two bundles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: F-58 is done when a rolling upgrade passes across two releases with different bundles. The releases that exist do not have
the store, and the rolling upgrade of R-HA-020 starts from a published release.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A second image of the working tree with another bundle (chosen) | `make image-marked` builds the image with `UI_BUNDLE_MARK=1`: `build:embed` keeps the libraries' licence comments in the scripts (`--extract-licenses=false`), which renames 122 of the 135 hashed files of a locale (a build of 2026-11-06) and changes no behaviour; `TestUpgrade_R_HA_020_AcrossTwoBundles` installs the tree's chart with it and upgrades to the image under test, every loop strict, and a sampler reads the `index.html` through the edge and loads the main script it names | Two bundles with the store on both sides, now; the window and the bundles are measured, not assumed | A second image build in the leg (about 1.5 min); the variant differs in comments, not in code |
| (b) Wait for the next release that changes the bundle | The rolling test from that release | No extra image | The proof comes a release later, and the test is red until then |
| (c) A build-time constant or a replaced source file | `define` or `fileReplacements` in `angular.json` | A code change | A constant changes nothing unless `web/src` reads it; a replaced file is a copy that the UI work changes under it |
| (d) Rename the files after the build | Rewrite the names | No second build | The names are referenced from `index.html` and from every chunk: a second bundler |

**Choice:** (a).

**Rationale:** it tests what F-58 asks with the code under test on both sides of the window, and nothing in the build or the product
behaves differently. Both images report the version `dev`, so the test reads the bundles, not the registry's versions, to see the
window. Revisit when the published releases have the store: the rolling test from the previous release then proves the same on
every release that changes the bundle.

**Applied in the spec:** R-HA-020 (status). Code: [build-embed.mjs](../../web/scripts/build-embed.mjs), the `Containerfile` (`UI_BUNDLE_MARK`),
`make image-marked`, [upgrade_bundles_test.go](../../test/e2e/upgrade_bundles_test.go), the step `make image-marked` of the leg
`e2e-upgrade`.

### Q-HA-125 · The rolling upgrade from a release without the store stays strict

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the first release with the store cannot serve the files of a release before it, which never published them, and that release's
replicas cannot serve the new ones. `TestUpgrade_R_HA_020_RollingFromThePreviousRelease` upgrades from the latest published release,
which until the next release has no store: a change of the bundle on `main` before this change is released makes it red.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Strict, as it is (chosen) | A `404` for a hashed file fails the test | The test shows what that one upgrade does to a user; it is green with every bundle change once a release with the store is published | A run between a bundle change on `main` and the release of this change is red |
| (b) Count the skew against a release without the store, as Q-HA-99 does for v0.16.0 | A tolerance keyed on the migration `00043` being newer than the release | Green at once | Widens a pass condition for a case that ends with the next release, and the tolerance has to be taken out again |
| (c) The first release with the store also carries the previous bundle | [Q-HA-120](#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release) (b) for one release | No skew at all | A pipeline change for one release |

**Choice:** (a).

**Rationale:** the tolerance of Q-HA-99 exists because the pods of v0.16.0 can never be changed; a release without the store is
followed by one with it at the next release. The order that keeps the leg green: this change is released before, or with, the next
change of the bundle. Operators read the one-time limit on the manual's upgrade page. Revisit if a release with a bundle change is cut
without the store.

**Applied in the spec:** R-HA-020; [docs/manual/admin/upgrades.md](../manual/admin/upgrades.md).

### Q-HA-126 · The node clearance moves the primaries before the cordon, and failover.sh waits for what a switchover needs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-65. In the dispatched nightly run 42211913572 (leg `mirror-long`) `clearNode` ([Q-HA-106](#q-ha-106--the-node-of-the-node-loss-scenario-is-chosen-first-and-cleared-not-searched-for))
cordoned the node of the pass's process, which hosted the PostgreSQL primary `trove-db-1` and the CloudNativePG operator, deleted the
operators' pods, and 14 s after the cordon ran `failover.sh`, which logged `switchover trove-db: trove-db-2 -> trove-db-1` and exited 1
three minutes later: "switchover did not finish in 3 minutes (primary trove-db-2, ready 2, phase 'Cluster in healthy state')". The
cluster's event "SwitchingOver: Current primary is running on unschedulable node …, switching over from trove-db-1 to trove-db-2" names
the cause: CloudNativePG switches a primary off a node that is cordoned (its handling of a drain, `reconcileTargetPrimaryFromPods` of
the operator 1.30). When the script read the status the primary had already moved, so "the first standby" it picked was `trove-db-1`,
the old primary on the cordoned node; the operator moved the primary off that node again, and the script's wait for `trove-db-1` as
primary could not end. The operator was there: its replacement had taken the lease. Reproduced on the dev cluster: the same order on the
node of the primary and the operator ended with the same error; a watch of the cluster's status shows the operator's switchover 1.3 s
after the cordon (started by the old operator pod, finished by its replacement), the script's switchover back onto the cordoned node
13 s later and the operator's second switchover 15 s after that.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Run `failover.sh` again when it fails | A retry | One line | Races the operator again and makes the primary bounce; a blind retry hides what the switchover lacked |
| (b) Keep the order and wait for the operator's own switchover | After the cordon, wait until the primary is off the node and the cluster is healthy | The path of a real drain | Two actors at once (the operator switches over while its own pod is replaced); the scenario's precondition rests on the operator's internal conditions for that switchover (every instance ready, the candidate streaming) |
| (c) The primaries first, then the cordon; `failover.sh` waits for its preconditions (chosen) | `clearNode` fails the Valkey primary over and switches the PostgreSQL primary over while the node is schedulable and the operators run, then cordons the node, deletes the operators' pods and waits until each replacement is ready on another node and holds its operator's lease (`replaceOperators`; `rollout status` reads a Deployment status that can still count the deleted pod). `failover.sh`, for every caller: waits up to 180 s until a ready pod that is not being deleted holds the lease `db9c8771.cnpg.io` and the cluster is settled (target primary = current primary, every instance ready, phase "Cluster in healthy state"); takes a ready standby on a schedulable node and refuses one on a cordoned node; asks with a JSON patch that tests the primary it read, so a switchover that started in between refuses the request; ends when the new primary also carries the label `cnpg.io/instanceRole=primary` that `trove-db-rw` selects. Each wait fails with the precondition it did not see met | One actor at a time; every failure names its precondition; no script can ask for a primary on a cordoned node; the operator that drops a lost standby from the synchronous set is in charge before the node goes | The clearance's order differs from a drain, where the cordon comes first and the operator moves the primary itself; the drain is its own scenario (`drain.sh`, R-HA-059) |

**Choice:** (c).

**Rationale:** the clearance makes a precondition of a single-failure scenario, so it must not depend on a race; the hardened script
also protects the other callers (`avoidPrimary`, the switchover scenarios), which were safe only because nothing else moved the primary
at the time. The development agent would revisit it if the dev profile runs its operators with two replicas (Q-HA-104 (c)), which makes
the operators' replacement unnecessary, or if CloudNativePG stops switching a primary off a cordoned node.

**Applied in the spec:** R-HA-051, the target `dev-failover`. Code: [failover.sh](../../deploy/dev/failover.sh),
[node_loss_test.go](../../test/e2e/node_loss_test.go) (`clearNode`, `replaceOperators`, `operatorProblem`).

### Q-HA-127 · A lease session that stays silent is ended by the server within the keepalive's bound

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the same nightly run 42211913572 failed the frozen-node subtest of the mirror's failure suite: after `docker pause` of the
node of the pass's process (it hosted the PostgreSQL standby, so the clearance moved nothing), the pass did not continue in another process
for 15 minutes. The test upstream's log has no request of the pass from the pause (11:55:28) to the unpause of the test's cleanup
(12:10:36), and the pass went on 0.2 s after it: the paused process itself continued, nobody had taken the pass over. A pass moves after a
lost process through the reaper (on every worker) and the next `mirror-dispatch`, which only the holder of the `scheduler` lease enqueues
(R-HA-005). The lease is a session-level advisory lock (R-HA-006); a paused container's processes stop, but its kernel keeps the TCP
connection and answers every keepalive probe, so the server never ends the session and the lock stays for as long as the pause lasts. The
run's artifacts cannot name the holder (the worker pods were replaced before the cluster state was taken); the other runs' frozen subtests
progressed after about a minute. Reproduced in the integration tier: a holder whose lease pool runs through a TCP relay that stops moving
bytes and closing sockets kept the lock for 34 s against a server keepalive bound of 4 s, and no other replica got the lease
(`TestLeases_R_HA_006_FrozenHolder` before the fix). Reproduced on the dev cluster: `docker pause` of the node of the worker that held the
`scheduler` lease left its session in `pg_locks`, idle, for the 172 s of the pause (the keepalive bound is 120 s); the lease moved only
after the unpause. With the fix the same experiment ended the session between 115 and 120 s of idleness (a sample every 5 s), and another
worker held the lease in the sample 16 s later (`cluster.lease_retry_interval` is 15 s).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `idle_session_timeout` on the lease sessions at the keepalive's bound (chosen) | A startup parameter of the lease pool only, `idle + interval × count` of `database.server_keepalive` (120 s with the defaults); the configuration refuses a bound below three `database.lease_check_interval` | The silent holder is released within the bound a vanished one already has; no new key; the server decides, whatever the holder's process does; fencing stays "the writes run on the lease's session", so the thawed holder can write nothing | A holder that is alive but sends nothing for 2 minutes (a stall longer than any measured: 40 s on a starved runner) loses its lease and restarts its work from the cursor |
| (b) A separate, shorter timeout | e.g. three lease checks (15 s) | Faster takeover | A new key; a starved but live holder loses its lease often (stalls of 40 s were measured) |
| (c) Leases as rows with an expiry, like the job leases | Renewed by the holder, taken over after expiry | No dependence on the session | A redesign of R-HA-006: fencing by the session goes, every singleton write would need a token check |
| (d) Keep the holder of the `scheduler` lease off the frozen node in the test | `clearNode` moves it too | No product change | Hides the gap: R-REPO-053's criterion names a frozen node, and the scheduler of a production cluster would stop for as long as a stopped holder lasts |
| (e) `tcp_user_timeout` on the server | A bound on unacknowledged data | Server-side | The paused kernel acknowledges: nothing is ever unacknowledged |

**Choice:** (a).

**Rationale:** a stopped process with a live kernel is the one case of a gone holder the keepalive cannot see; the lease check makes
"no statement for the keepalive's bound" a reliable sign of it, and the bound is the one the specification already gives for a vanished
holder. With it, the frozen-node subtest's path is: the lease free after at most 120 s, the next worker holds it within
`cluster.lease_retry_interval`, the scheduler enqueues the due `mirror-dispatch` at once, inside R-REPO-053's 5 minutes. The
development agent would revisit it if `trove_lease_lost_total` shows live holders losing their leases under load, or if leases move to rows.

**Applied in the spec:** R-HA-006 item 4 and an acceptance criterion, R-HA-015 (the parameter table). Code: `runtimeParams` and
`ServerReleaseBound` in `internal/meta/postgres/connect.go`, `Database.Validate` in `internal/config/database.go`;
[lease_frozen_test.go](../../test/integration/coordination/lease_frozen_test.go).

### Q-HA-130 · The cut moves the edge and removes the bridge in two separate upgrades

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: F-64. In the scheduled nightly run 42194413882 one browser journey of `TestUpgrade_R_HA_064_AcrossTheCut` failed at
its first step: `GET /ui/en/login?local=1` at 10:03:15.255 was answered `503` by the edge (`trove_trove_http/<NOSRV>`,
termination state `SC--`) during step 2 of the two-step cut (Q-HA-73), and the sign-in form never came. The edge's access lines
of the cluster-state artifact show 509 such answers between 10:03:13.849 and 10:03:15.255, 508 of them to the npm client pods,
which repeated them, so every `npm ci` passed. The controller's log names the order: Helm deleted the Service `trove` (13.144)
and updated the Ingress `trove` (13.303) in one apply; HAProxy Ingress applied the empty backend at once as a dynamic update
(13.85 to 13.90 on its two pods) and the moved routes with its next reload (15.35 and 15.47). An edge applies endpoint changes
and route changes on separate paths, so an apply that both moves a route and removes the backend it pointed at leaves that route
without a server until the route moves; draining the deleted pods (R-HA-011) does not help, because the Service went with them.
The same holds for a new Service that a moved route names in the same apply (`<fullname>-api-ep-<name>` of a two-role endpoint).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Three upgrades: step 2 (`web.enabled: true`, `web.routed: false`) moves the edge while the bridge keeps running, step 3 (`web.enabled: false`) removes the bridge, which no route names; the bridge renders every Service a later step routes to; the guard refuses step 3 while the chart's Ingress still routes to `<fullname>` (chosen) | Each apply changes either the edge or the set of backends | No gap whatever order or delay the edge applies changes in; every step's rollback is gap-free too; deterministic under every renderer; the value follows the pools' `routed` (Q-HA-90) | One more `helm upgrade` (or GitOps commit); an edge outside the chart is not read by the guard |
| (b) Two upgrades; the bridge's Deployment and Service survive step 2 through `helm.sh/resource-policy: keep` and are deleted by hand | No new value | One command less | Leaves objects outside the release that `helm rollback` and `helm uninstall` do not manage; a manual deletion the guard cannot check |
| (c) Two upgrades; step 2 keeps rendering the bridge while `lookup` finds the edge on it, and a later upgrade removes it | No new value | The operator runs the same command twice | The rendering depends on the cluster: `helm template` and GitOps renderers produce the gap (the argument of Q-HA-73 (b) and Q-HA-90 (a)) |
| (d) Two upgrades; the edge keeps terminating endpoints (HAProxy Ingress `drain-support`) or a delay is inserted between the steps' changes | Controller settings | No chart change | Controller-specific (the chart supports HAProxy Ingress, ingress-nginx and Traefik); the deleted Service takes its endpoints with it, terminating or not |
| (e) Two upgrades, the gap documented | Release notes | Nothing to build | Breaks R-HA-021's "zero downtime" for this path: a browser does not repeat a `503`, and F-64 is the evidence |

**Choice:** (a).

**Rationale:** the gap comes from one apply carrying two kinds of change that an edge applies at different times, so the fix is to
never put both into one apply: expand (step 1), switch (step 2), contract (step 3). It does not depend on the controller's timing,
which (d) would have to know for each controller. The value is in the values, so the three applies are three reviewable renders,
which (c) gives up. The BackendTLSPolicies of both sets render for the whole bridge, because a Gateway API route is the
operator's and switches whenever the operator moves it. The development agent would revisit this if Helm gains a wait between
applying and deleting resources and the supported edges apply a Service deletion only after the routes that name it, which
would make two steps safe.

**Applied in the spec:** R-HA-064 (the steps, the bridge's Services and policies, the guard, the way back, the acceptance
criteria), R-HA-020 (order), R-HA-021 (the row of the upgrade path), R-HA-029 (the guard's two refusals),
[values.yaml](../../deploy/helm/trove/values.yaml) and [values.schema.json](../../deploy/helm/trove/values.schema.json)
(`web.routed`), [_edge.tpl](../../deploy/helm/trove/templates/_edge.tpl) (`trove.bridgeRouted`, `trove.edgeBackendServices`),
[endpoint-services.yaml](../../deploy/helm/trove/templates/endpoint-services.yaml),
[_validate_roles.tpl](../../deploy/helm/trove/templates/_validate_roles.tpl), [_notes.tpl](../../deploy/helm/trove/templates/_notes.tpl),
[_rules.tpl](../../deploy/helm/trove/templates/_rules.tpl) (`TroveBridgeLeftOn`), the upgrade guide
([docs/manual/admin/upgrades.md](../manual/admin/upgrades.md)), `TestRolesBridge` and `TestUpgrade_R_HA_064_AcrossTheCut`.

### Q-HA-131 · The upgrade tests read the edge's access lines for answers without a backend

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: F-64's gap of 1.4 s answered 509 requests with `503`; the npm loops repeated every one and passed, and the browser
journey met the gap in one iteration of 14 by chance. The loops alone detect an edge gap only when a client that does not repeat
happens to fall into it, so the zero-downtime promise of R-HA-020 and R-HA-064 was checked by chance. Since F-50 the dev
profile's edge writes one access line per request with its backend, server and termination state.

| Option | For | Against |
|---|---|---|
| (a) After the loops stop, read the edge pods' lines of the loops' span and fail on any line `<backend>/<NOSRV> … 503` (chosen) | Counts every request the edge could not route, also those a client repeated; independent of chance; names the backend and the time | Bound to the access line format of the dev profile's edge (HAProxy Ingress) |
| (b) Count the `503` answers in the clients' logs | Client-side | npm logs a repeated `503` only at verbose levels, the NuGet client not at all |
| (c) Run the browser journey more often | No new check | Still chance: a journey every 20 s against a gap of 1.5 s |

**Choice:** (a).

**Rationale:** the edge is the one place every request passes, and its line distinguishes an answer without a server from a
`503` of trove itself (which names its server) and from a request the client gave up on (status `-1`). Both upgrade tests use it
(`requireEdgeServed` in [upgrade_loops_test.go](../../test/e2e/upgrade_loops_test.go)), the cut and the rolling upgrade with its
rollback. The development agent would revisit the format if the dev profile changes its edge.

**Applied in the spec:** R-HA-064 (acceptance criteria), [upgrade_test.go](../../test/e2e/upgrade_test.go),
[docs/developer/e2e.md](../developer/e2e.md#the-upgrade-tests).

### Q-HA-132 · The bridge guard checks readiness only on the upgrade that moves the edge

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a review of Q-HA-130 found that the readiness refusal ran on every upgrade with the values of step 2, because
`<fullname>-web` exists for the whole of step 2. An `api` that crash-loops at step 2, an autoscaler scaling up or a rollout in
progress made the upgrade that carries the fix fail, and the only way through was back to step 1, which moves the edge back to
the `web` pods. The refusal exists for one transition: the apply that moves the edge onto the target shape. Where the edge
points is in the cluster: the chart's Ingress `<fullname>` routes to the Service `<fullname>`, and each further endpoint's
Service `<fullname>-ep-<name>` selects the `web` pods, until step 2 has been applied. An installation without the chart's
Ingress (an edge of its own) and without further endpoints shows the chart no edge.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read where the edge points (the Ingress and the endpoint Services); check readiness only while it is still on the bridge; with no edge to read, refuse only a missing main `api` or `packages` Deployment, which says step 1 has not run (chosen) | The refusal follows the transition, not the values | An upgrade at step 2 with the edge moved passes whatever the readiness; the endpoint Services give a signal without the chart's Ingress; a one-step upgrade from the old shape is still refused | Without a readable edge, a target that exists but is not ready is not refused when the operator's own route moves |
| (b) Read the chart's Ingress only; no Ingress, no guard | The review's minimum | Simplest | An installation with `ingress.enabled: false` loses the refusal of the one-step upgrade from the old shape, and an endpoint whose Service moves in step 2 is not read |
| (c) Keep the readiness check on every upgrade at step 2 or 3 while `<fullname>-web` exists | No change | Strict | The defect of the review: a fix cannot be applied at step 2 while a target Deployment is not ready |
| (d) A value that skips the guard | `web.skipGuard` | An escape | A switch that turns the safety off is set once and forgotten; the facts are in the cluster |

**Choice:** (a).

**Rationale:** the refusal protects the moment the edge moves, so it reads whether the edge has moved; every other upgrade at
step 2 or 3 moves nothing and must be able to repair a role. The decision is a template over facts (`trove.bridgeGuard` over
what `trove.bridgeFacts` reads with `lookup`), so the chart test checks it with given facts and an API server checks the reads
(the cases of the decision: the edge on the bridge with `api` not ready, step 3 before step 2, the edge moved with `api` not
ready, no readable edge with and without `api`, an endpoint Service on the `web` pods). The development agent would revisit
the fallback without a readable edge if an installation with its own edge reports a refusal it needs or a gap it met.

**Applied in the spec:** R-HA-064 (the guard, the way back), R-HA-029 (the rows of the guard),
[_validate_roles.tpl](../../deploy/helm/trove/templates/_validate_roles.tpl), `TestRolesBridge`, the upgrade guide
([docs/manual/admin/upgrades.md](../manual/admin/upgrades.md)).

### Q-HA-135 · The names of the specification win, and the binary is renamed where it drifted

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: building the PrometheusRule of R-HA-045 showed that its expressions read metrics the binary did not export, and names that differed
from the specification. Not exported: `trove_db_retries_total`, `trove_db_pool_acquire_duration_seconds`, `trove_jobs_oldest_queued_age_seconds`,
`trove_spool_used_bytes`, `trove_gc_last_success_timestamp_seconds`, `trove_revocation_journal_pending`, `trove_valkey_primary_views` and every
`trove_backup_*`. Drifted: the binary had `trove_storage_spool_free_bytes` and `trove_storage_spool_reserved_bytes`, where
[operations-and-observability.md](operations-and-observability.md) R-OPS-008 and the expression of `TroveSpoolNearlyFull` name
`trove_spool_used_bytes` and `trove_spool_free_bytes`; `trove_db_pool_empty_acquires_total` carried the number of all acquisitions
(pgxpool `AcquireCount`) under a name whose catalogue row says "acquisitions that had to wait" (`EmptyAcquireCount`). The role regex of
`TroveReplicasNotReady` named `web` and `worker`, the Deployments of an earlier shape.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The names of the specification win; the binary is renamed where it drifted, and exports the missing metrics where their mechanism exists (chosen) | The two spool gauges, the pool counter and the role regex are fixed in code and chart; the metrics with a mechanism are added; the rows whose mechanism is not built stay out | One catalogue, one name per metric; the expressions are the ones the specification was reviewed with; pre-alpha has no dashboard or scrape that depends on an old name | Every place that names an old name changes in the same commit |
| (b) The names of the binary win; the specification and the expressions are edited | `trove_storage_spool_*` stays | No code change | R-OPS-008 and storage.md disagree with each other (storage.md names `trove_storage_spool_*`, R-OPS-008 `trove_spool_*`), so one page would still be wrong; `trove_db_pool_empty_acquires_total` would keep a name that does not say what it counts |
| (c) Export both names for a while | An alias per renamed metric | Nothing breaks for a scrape of the old name | The repository has no installation and no dashboard; two series for one fact double the cardinality and the rule "no aliases before the first stable release" forbids it |

**Choice:** (a).

**Rationale:** the specification is the source of truth, and its two pages already agreed on the `trove_spool_*` names; the binary was the
outlier. `trove_spool_used_bytes` is the budget that running uploads hold, which is what the spool admits against (R-STOR-004); the
free bytes are read at every reservation and release, so they are current when the spool is idle, not only at the last reservation. The
backup gauges and the revocation journal gauge are not invented: their jobs are phase 7 (BETA.md), and a metric nobody writes would only
make an alert that never fires look like one that works. The development agent would revisit this when the backup is built (the seven backup
alerts and `TroveRevocationJournalBacklog` join the rule then) or when an installation scrapes the old spool names.

**Applied in the spec:** R-HA-045 (status, severity column, the role regex, the spool and GC gauges), R-HA-038 (the refusal of an empty
`gateway.parentRefs`), Q-HA-81 (status), [storage.md](storage.md) (spool metric names),
[operations-and-observability.md](operations-and-observability.md) R-OPS-008 (rows for the spool, GC, pool and Valkey gauges). Code:
`internal/blob/metrics.go`, `internal/meta/postgres/metrics.go`, `internal/app/wire_gauges.go`; chart: `templates/_rules.tpl`.

### Q-HA-136 · The dev edge keeps an idle client connection longer than npm's longest retry delay

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: in the scheduled nightly run 42356771989 one `npm publish` of the 300 s object-store outage exited with `ECONNRESET` (`socket hang up`)
instead of the registry's `503`, which R-HA-059's client condition and F-50 count as a failure. The command made three attempts through the
forwarder of the clients pod to the edge: at 09:01:05.5 and 09:01:15.6 on one connection (both answered `503` and logged by the edge), and the
third 60 s after the second, at 09:02:15.6 (the command ended at 09:02:15.638), which has no access line at the edge. npm waits
`fetch-retry-maxtimeout` = 60 s before its last attempt, and HAProxy Ingress closes an idle client connection after `timeout-keep-alive`, 1 m by
the controller's documentation (not verified in the pinned image). The client reused a connection in the instant the edge closed it. The three
other commands of the run that retried through the whole outage (the `publish` commands 9, 10 and 11) made their third attempt 60.01 to 60.03 s
after the answer to the second, every time on a new connection: the same race, lost the other way, so the edge had closed the idle connection by
then and the client had seen it. No `packages` pod was involved: the replica deleted at 09:02:25 was deleted 10 s after the command had ended.
The absence of an access line is weak evidence alone: the first attempt of command 10, on a connection that had just carried a request, has none
either, so a missing line can also be a lost line (the access lines pass the controller's syslog socket); that is not known.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The dev edge's `timeout-keep-alive` is 90 s, longer than the 60 s of npm's last retry delay (chosen) | One key in `deploy/dev/haproxy-values.yaml` | The npm client with its default retries (R-HA-059's condition) never reuses a connection at the edge's idle limit; no test changes; the edge is not the system under test | A client that waits exactly the edge's limit still races; the key is the dev profile's, a production edge has its own |
| (b) The publish loop runs `npm publish --fetch-retry-maxtimeout=30000` | The client waits less | Same effect without touching the edge | The condition is "npm's default retries" (R-HA-059); a changed client no longer shows what a user's client sees |
| (c) `fetch-retries=0` in the loops, the test repeats | No retry in the client | No idle wait | The retry of the client is what the 503 contract (`retry`) is for; the outage tests would no longer show that npm survives the outage |
| (d) The forwarder resets or reconnects an idle connection | The test hides the race | None | The forwarder is a byte relay on purpose; a client could not tell a closed connection from a live one |

**Choice:** (a), and the forwarder writes a closing line for every connection that the target closed first (idle time, whether the client's bytes
came after the close).

**Rationale:** the race belongs to HTTP keep-alive and not to trove: any client that reuses an idle connection exactly at the server's limit can
lose a request, and only an idempotent request is safe to repeat blindly. A production edge sets its own limit (the owner's controller); trove
asks nothing of it. For the dev profile the limit is chosen so that the longest wait of the tested clients stays inside it. Not shown directly:
which side closed first, because neither the controller nor the forwarder logged a close of an idle connection. The forwarder's line makes the
next occurrence name itself. The development agent would revisit this when the closing line shows a reset with an idle time other than the
edge's limit (then the cause is elsewhere) or when a client waits longer than 90 s between attempts.

**Applied:** `deploy/dev/haproxy-values.yaml` (`timeout-keep-alive: 90s`), `test/e2e/clients/fwd/main.go` (closing line, tests in `main_test.go`),
[developer/e2e.md](../developer/e2e.md) (forwarder), F-70 in [projektplan/promt.md](../projektplan/promt.md). No requirement text changed.

### Q-HA-159 · The restore Job has a journal reader of its own, switched on by a credentials file

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decouple the listing of versions from the credentials file in `journal/replay/open.go` | The restore lists versions whenever the bucket is versioned | No chart value | The listing needs `s3:ListBucketVersions`, which the runtime credential lacks on purpose, so a restore would fail on a bucket that is not versioned or on a runtime credential; the file is the operator's statement that the credential may list |
| (b) `restore.journalCredentials.source` (`runtime`, `default`, `static`) mirroring `postRestore`, rendering the file for `--journal-credentials-file` (chosen) | A workload identity of the restore ServiceAccount (`default`) or `secrets.journalReader` (`static`) lists versions | `open.go` is unchanged: a file with `source: default` already means "list versions with the identity of the pod"; one mechanism for both Jobs | The default stays `runtime`, so an operator must choose |

**Choice:** (b). `secrets.journalReader` serves both Jobs (renamed from `postRestoreJournal`).

**Rationale:** The runtime credential needs `DeleteObject` for garbage collection, so its holder can remove a journal entry; only a reader that lists versions sees through that (R-BAK-015, R-BAK-024). The development agent would revisit this when the restore command can detect a versioned bucket and refuse `runtime` itself.

**Applied:** R-HA-060, R-BAK-024; `templates/restore.yaml`, `templates/_backup.tpl`, `templates/_secrets.tpl`, `templates/_validate_backup.tpl`.

### Q-HA-160 · The backup pod reads the blob bucket with a login of its own when the blob credentials are static

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The backup pod mounts `secrets.s3` | As before | No new value | The runtime credential can write and delete in the bucket and the journal: a compromised backup pod could rewrite journal entries (R-BAK-015: the backup never writes or deletes there) |
| (b) `secrets.backupSource`, required with static blob credentials and `backup.enabled`; the chart renders `backup.source` (chosen) | The backup scope mounts the read-only login instead; the restore Job and the replicas keep `secrets.s3` | The backup holds what R-BAK-015 says; a workload identity needs nothing | An installation with static keys needs a second login (the dev profile names the same Secret) |

**Choice:** (b), with the config key of [Q-BAK-49](backup-and-restore.md#q-bak-49--the-backup-reads-the-blob-bucket-with-backupsourcecredentials-when-separate_credentials-is-set).

**Rationale:** No fallback to `secrets.s3`: a missing Secret fails the render naming both values. The development agent would revisit this when the bucket policy of the profile can express a read-only login.

**Applied:** R-HA-030 (Secrets table), R-HA-031, R-BAK-015; `templates/_secrets.tpl`, `templates/_helpers.tpl`, `templates/_validate_backup.tpl`.

### Q-HA-161 · The post-restore Job is refused beside replicas and named after its restore point

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As before: a hook with a fixed name, replicas allowed | Operators stop the roles by hand | No rule | A hook runs after the Deployments are applied, so `api` and `packages` roll onto the restored database and serve revoked access before the replay |
| (b) The replica rule of `restore.enabled` for `postRestore.enabled`, and the Job named `<fullname>-post-restore-<hash of restoredTo>` with the annotation `trove/restored-to` (chosen) | Roles at zero, every kind of Deployment; a repeat of the same point is a visible Job of the same name; the command turns it into a no-op | A second upgrade with the flag left on cannot rotate the keyspace under running pods | Helm replaces a hook of the same name, so the no-op is the command's (it stores the restore point it cleaned up for) |

**Choice:** (b). The command is expected to answer a `--restored-to` equal to the one it already cleaned up for with exit 0 and a line saying so, and to run again for another time; the chart cannot make a Job run once.

**Applied:** R-HA-025 step 1 and 4, R-HA-029; `templates/post-restore.yaml`, `templates/_validate_backup.tpl`, `templates/_validate_roles.tpl`, `templates/_notes.tpl`.

### Q-HA-150 · The post-restore Job is a post-upgrade hook with a credentials file the chart renders

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only the one-off pod of R-HA-025 (`kubectl run … -- trove admin post-restore …`) | The chart renders nothing | No chart code | The pod needs the release's configuration, the owner login, the blob store's CA and the journal credential, which the operator would assemble by hand and get wrong under the pressure of a restore |
| (b) A hook Job `<fullname>-post-restore` with `postRestore.enabled` and `restoredTo`, `post-install,post-upgrade`, Secrets of its scope, and `postRestore.journalCredentials.source` (`runtime`, `default`, `static`) that renders the YAML file of `--credentials-file` (chosen) | Helm waits for it and fails the upgrade when it fails; the owner login of the migration Job, the blob store and, for the journal, the credential that lists object versions reach this Job only; `static` mounts `secrets.journalReader`, `default` uses the Job's workload identity | The credentials file is chart-written, not the operator's | Four values more than the spec's two |
| (c) Run it inside the restore Job | One Job | The restore Job already does it for a backup restore (R-BAK-021) | A platform restore has no restore Job, and the clean-up must run while the roles are at zero, before the upgrade that starts them |

**Choice:** (b), with `backoffLimit` 2, because the fix-ups are one transaction and a repeated replay reports its entries as already in effect.

**Rationale:** R-HA-025 step 4 names the hook Job, and step 5 names the separate credential that only this command may receive. The development agent would revisit this if the journal reader should read the credential from the operator's file directly.

**Applied:** R-HA-025 step 4, R-HA-028, R-HA-030 (Secrets table), R-HA-031 (values block), R-HA-029 (rows of `postRestore.enabled`); `templates/post-restore.yaml`, `templates/_backup.tpl`, `templates/_secrets.tpl`, `templates/_validate_backup.tpl`.

### Q-HA-151 · The platform check verifies what the operator declared and nothing else

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Check everything always and export every result | `archive_mode`, versioning, lifecycle and grace period on every installation | Simple to explain | An installation that relies on trove's own backup alone would alarm `TroveBackupCheckFailed` for a protection it never claimed |
| (b) A check per declaration: `archive_mode` and `grace_period` with `backup.recovery_window`, `object_versioning` with `backup.object_versioning`, `noncurrent_expiry` with both (chosen) | Nothing declared, no series | The alert means "a protection you declared is missing"; the defaults stay silent | The operator must declare to be checked |

**Choice:** (b). A setting the credential may not read is unknown and has no series (R-HA-023 "otherwise unknown").

**Rationale:** R-HA-023 calls the platform layer "recommended, not required". The development agent would revisit this if an installation without any declaration should be told so.

**Applied:** R-HA-023, R-HA-045; `internal/backup/platform`.

### Q-HA-152 · `backup.recovery_window` and `backup.object_versioning` are config keys with the startup rule of R-HA-024

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keys for the check only | The check reads them; no startup rule | Smaller change | R-HA-024's rule (refuse to start) stays unbuilt although the keys and the check hold the same inputs |
| (b) Keys, the startup rule and the check (chosen) | A replica and every backup command refuse a window above `storage.gc.grace_period` without `object_versioning`, naming the three keys | One definition of the three-key rule | The rule reaches the backup commands too |

**Choice:** (b).

**Rationale:** The check's `grace_period` row and the startup rule are the same inequality; building one without the other would let them disagree. The development agent would revisit this if a restore Job must run on an installation that violates the rule.

**Applied:** R-HA-023, R-HA-024; `internal/config/backup.go`, `internal/config/schema.json`.

### Q-HA-153 · `trove admin backup check` runs the checks without storing them and exits 5 on a failed one

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The CLI reads the stored report | It prints the last run of the task | No S3 access of its own | It shows yesterday's state, and the acceptance criterion asks for a report after `kubectl cnpg backup` |
| (b) The CLI runs the checks (chosen) | The same `platform.Checker`; it stores nothing; exit 5 when a declared protection does not hold | Current state; usable in a drill script | The pod running it needs the bucket's settings rights to be more than "unknown" |

**Choice:** (b).

**Rationale:** R-HA-023: "reports, without changing anything". The development agent would revisit this if the stored report should be printable too.

**Applied:** R-HA-023; `cmd/trove/admin_backupcheck.go`.

### Q-HA-146 · The CA of the backup target is a Secret of the chart

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `config.backup.target.ca_file` stays an ordinary key | The operator mounts the file with `extraVolumes` | No new value | `extraVolumes` reach the Deployments and not the Jobs (Q-HA-149), so the key would name a file no backup pod has; a private CA, which the local profile uses on every link, could not be trusted |
| (b) `secrets.backupTargetCA` (`existingSecret`, `key`), mounted in the backup and restore pods, renders `backup.target.ca_file` (chosen) | The CA mounts as a directory volume like every other Secret of the chart, so a rotated CA reaches the pod without a restart | The target's trust is its own, as the target's credentials are; replicas never mount it; the key moves from the operator's keys to the chart-managed ones (R-HA-029 refuses it inside `config`) | One value more |
| (c) Reuse `secrets.s3CA` | One CA for the blob store and the target | No new value | A target in another account or provider has its own CA; a backup pod would trust the blob store's CA for it |

**Choice:** (b).

**Rationale:** The rule of the owner's stack is that every link is TLS-capable and certificates rotate without a restart (2026-10-29). The chart's way to give a pod a CA is a Secret mounted without `subPath`, which (b) follows. The development agent would revisit this when the chart gets the `trustedCA` values of R-HA-043, which could carry the target's CA as well.

**Applied:** R-HA-030 (Secrets table), R-HA-031 (values block), R-HA-029 (managed keys); `templates/_secrets.tpl`, `templates/_helpers.tpl`, `templates/_validate.tpl`, `values.yaml`, `values.schema.json`, `deploy/dev/values-dev.yaml`.

### Q-HA-147 · The restore Job has a termination grace period of its own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The restore Job reads `backup.terminationGracePeriodSeconds` | No new value | One value | Switching the backup off or tuning it for a lighter run would change how a restore ends; a restore at a safe point can need other time than a backup (R-BAK-012 asks only for at least 30 seconds of both) |
| (b) `restore.terminationGracePeriodSeconds`, default 60, at least 30 (chosen) | Each Job has its own | The value sits with the object it configures, like `backoffLimit` and `activeDeadlineSeconds` | One value more |

**Choice:** (b).

**Rationale:** R-BAK-012 demands the grace period of both Jobs and R-HA-060 only named it for the backup. The development agent would revisit this if the restore's time to a safe point turns out to be much longer than a backup's.

**Applied:** R-HA-060, R-HA-031 (values block); `values.yaml`, `values.schema.json`.

### Q-HA-148 · While `restore.enabled` is set, the replica minimums give way to the zero rule

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the minimums and add the zero rule | `api`, `packages` and `worker` need at least one replica, with `highAvailability.enforce` at least two, and `restore.enabled` needs none | Nothing to change | Cannot be satisfied together: no value passes both |
| (b) The checks "at least 1" and "at least 2 with `highAvailability.enforce`" do not apply while `restore.enabled` is set; the zero rule replaces them (chosen) | Every Deployment, pool and endpoint pool must have `replicaCount: 0` and no autoscaler; the other rules (disruption budget, host spread, memory limits) stay | The sequence of R-HA-060 passes with its own values; a replica that would write during the restore is refused with the value named | `highAvailability.enforce` does not stop an installation without replicas while the restore Job exists |
| (c) No validation while `restore.enabled` is set | Every rule off | Simplest | A typo in the other values would surface in the final upgrade, after hours of restore |

**Choice:** (b).

**Rationale:** An installation under restore is not served on purpose, and the operator's sequence (R-HA-060) sets `highAvailability.enforce=false` for the same reason; the final `helm upgrade` without `restore.enabled` applies every minimum again. The development agent would revisit this if a restore should run beside replicas that only read.

**Applied:** R-HA-029 (rows of `restore.enabled`), R-HA-060; `templates/_validate_roles.tpl`, `templates/_validate_backup.tpl`.

### Q-HA-149 · The backup and restore Jobs take the environment of their own values only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The global `extraEnv`, `extraEnvFrom`, `extraVolumes` and `extraVolumeMounts` reach the Jobs as they reach the Deployments | R-HA-046 read literally | An outbound proxy variable set once reaches every pod | A variable or volume that serves the replicas (a Valkey or login Secret an operator adds, the dev profile's CA volume) would reach the pods that hold the target's credentials, and the rule that the Jobs mount no Valkey Secret (R-BAK-012) could not be checked from the chart |
| (b) The Jobs take `backup.extraEnv` and `restore.extraEnv` and nothing global (chosen) | As the migration Job does with `migrations.extraEnv` | What a Job mounts is what its values and the Secrets of its scope say; the chart test can assert it | An operator sets a proxy variable three times |

**Choice:** (b).

**Rationale:** The Jobs are the pods with the highest privilege on the data, and the chart's rule is that each Secret is mounted only into the pods whose process reads it (R-HA-030). The development agent would revisit this if operators ask for a shared list for all Jobs.

**Applied:** R-HA-046, R-HA-060; `templates/_backup.tpl`.

### Q-HA-140 · A journal object is an envelope around the producer's entry

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The producer's entry is the object | The entry carries `v`, `id`, `at`, `event` itself | One level | Every producer would have to repeat the four envelope members and could get one wrong; the entries in the code (`internal/auth/token`, `internal/auth/principal`, `internal/project`, `internal/formatdata/*`) are already shaped by their owners without them |
| (b) `{"v", "id", "at", "event", "entry"}` with the producer's object verbatim in `entry` (chosen) | `journal.NewObject` builds it from the `revocation_outbox` row; the shipper never interprets `entry` | The envelope is one function; the same row always produces the same bytes (a repeated shipment is harmless); a new producer needs no change here | The examples of this requirement showed `subject` and `before`/`after` at the top level, and change |

**Choice:** (b).

**Rationale:** The audit writer already stores the producer's entry in `revocation_outbox.entry`, and the outbox row has the other four values. Wrapping them keeps the producers independent of the journal's format and gives the reader a version to refuse (`ErrUnknownVersion`). The development agent would revisit this if an entry needs a value that the outbox row does not have.

**Applied:** R-HA-054 examples; `internal/journal/object.go`.

### Q-HA-141 · Replay handlers are registered per event name by the domain that owns the event

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One replay function with a switch on the event name | A package `replay` knows every domain | Everything in one place | Identity, authorization, CI, exposure, npm and NuGet owners would edit one file; the import graph would make the replay depend on every domain |
| (b) `journal.Register(Handler{Domain, Events, Apply})` in a file of the owning domain, and `journal.ApplyUpdate` for the update rule (chosen) | A handler applies one entry in its own transaction as the owner credential and returns an outcome; `ApplyUpdate[T]` is the three-case rule of this requirement over the owner's `Narrower[T]` | The update rule exists once and is tested once; a domain adds its events without touching another's; an event type without a handler is `skipped` | A registry (frozen at start-up, R-ARCH-032) |

**Choice:** (b).

**Rationale:** The comparator is the owning page's (R-AUTHZ-031, R-EP-020, R-CI-020), and the rule that chooses between applying, skipping and suspending is this page's. Splitting them along the two owners is what the generic function does. The development agent would revisit this if a replay needs several entries at once (a handler for a batch).

**Applied:** R-HA-054 "Dispatch" and "Replay rules"; `internal/journal/replay.go`.

### Q-HA-142 · The replay reads the journal by day prefixes and reports what it cannot read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) List the whole journal | One listing of `_journal/revocations/` | Simple | The journal is kept for as long as the operator keeps it; a restore lists years of keys for the entries of one interval |
| (b) List the UTC days from `snapshotAt - replay_margin` to now, read each object, report an object that does not decode (chosen) | `journal.DayPrefixes`; an object that is unreadable or newer than this binary reads is returned as `Unreadable` and printed `skipped`; it never stops the replay | Bounded work; one forged or damaged object cannot block the replay of the others; the output shows what was lost (H-553) | A day boundary is a prefix boundary; the margin is always rounded to whole days |

**Choice:** (b).

**Rationale:** The key layout of this requirement already names the day. The margin covers transactions that started before the snapshot and committed after it, which a day prefix includes. The development agent would revisit this if a store lists a prefix of millions of keys too slowly.

**Applied:** R-HA-054 "Replay run"; `internal/journal/object.go` (`DayPrefixes`), `internal/journal/ports.go` (`Reader`).

### Q-HA-145 · The schema check reads the restore marker after the schema state

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The marker first | A restore that commits between the two reads is missed | Simple | A replica can find the final schema step and no marker, and serve a half-restored database |
| (b) The schema state first, then the marker, in `CheckSchema` and in the report of `trove migrate status` (chosen) | The marker is committed with the step that makes the schema servable (R-BAK-021 step 2), so a state read first cannot be followed by a missing marker | No window; one extra read on the control connection every check | Two statements instead of one |

**Choice:** (b). `postgres.ErrRestoreInProgress` is a plain failure of the `schema` check (R-OPS-001), not an outage: the replica stays not ready.
**Rationale:** The order is the whole proof; a single statement would not need it but would cost every readiness check a join. Revisit if the check moves into one query.
**Applied:** R-OPS-001 `schema` check, R-ARCH-024 step 3; `internal/meta/postgres/gate.go`, `migrate.go` (`Report`).
### Q-HA-144 · How the shipper is triggered, and what `--credentials-file` holds

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | Behaviour | Cost |
|---|---|---|
| (a) A job kind that a timer on every job-running replica enqueues while the outbox holds a row; `enabled: false` deletes the rows unshipped; `--credentials-file` is a strict YAML document of the keys of `storage.s3.credentials` | One code path; bypasses the one-minute cron resolution; the audit writer stays free of Valkey | A change waits up to `ship_interval` instead of being shipped on a signal |
| (b) A signal published by the audit writer after commit | Ships at once | The audit writer gains a Valkey dependency it must not have (INV-3) |
| (c) A periodic task, and an AWS shared credentials file | Registry-only | The finest cron schedule is one minute; profiles and fallbacks are ambiguous |

**Choice:** (a). `revocation-journal-ship` is `Coalesce` in the `maintenance` queue; `internal/app/wire_journal.go` starts the timer. With `enabled: false` the same job deletes the rows without writing objects. A credentials file also switches the journal reader to listing object versions; an unknown key in it is refused, so a typo cannot fall back to the runtime credential.
**Rationale:** The interval alone meets the acceptance criterion (the object appears within `ship_interval` after the store returns). The runtime credential lacks `s3:ListBucketVersions` (R-HA-023), so a file is the way to give a restore one. Revisit when the 5 s latency matters for the recovery point, or when the restore Job's identity replaces files.
**Applied:** R-HA-054 "Outbox and shipping" is read as triggered by the interval; `internal/jobs/journal.go`, `internal/journal/ship`, `internal/app/wire_journal.go`, `internal/journal/replay/open.go`.

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

### H-79 · Artifacts are unsigned and nothing checks their origin

The release pipeline ([build.yml](../../.github/workflows/build.yml)) publishes the image
`docker.io/guidedtraffic/trove` and the chart at `https://guided-traffic.github.io/trove/`, and no
consumer receives a signature to check them against. The image carries a provenance and an SBOM
attestation in its image index. The chart carries none: `helm package` runs without `--sign`, so there is
no `.prov` file, and the pipeline makes no chart SBOM. The attestations are unsigned, so they name the build
but not the party that made the claim.

- **Component and subject:** the release pipeline and the two places it writes to. The subject is every
  consumer that pulls the image or the chart, and any admission policy of the cluster that runs them. The
  chart renders no admission policy and no signature check.
- **Live or dormant:** dormant until the first release of the pipeline, because the workflows have not run
  on GitHub yet ([releasing.md](../developer/releasing.md#not-in-place-yet)). From that release on, live.
- **What makes it real:** whoever holds the Docker Hub access token of the account `guidedtraffic` (the
  secret `DOCKERHUB_PAT`) can push a different image under an existing tag. The chart pulls by tag by default,
  because `image.digest` is empty in [values.yaml](../../deploy/helm/trove/values.yaml). Whoever can write to the
  `gh-pages` branch can change a published chart, and the release job writes that branch with
  `git push --force`. Docker Scout reports CVEs without checking origin. The keyless signatures of the earlier
  pipeline are not part of the design since 2026-10-29 ([Q-HA-11](#q-ha-11--publishing-location-of-images-and-chart)).
  OPS-076 ("Signed release artifacts", P1) is therefore open: no signature is delivered, and R-HA-047 no longer
  covers it.

### H-80 · Self-hosted runners build the code of every pull request

[release.yml](../../.github/workflows/release.yml) runs every job on the organisation's self-hosted runners, on every push and
every pull request to `main` (`runs-on: self-hosted` and `runs-on: large`), except the kind legs that load the NuGet clients
image, which run on GitHub-hosted runners (owner, 2026-11-05, [releasing.md](../developer/releasing.md#ci)). The MinIO leg of R-STOR-035 builds MinIO from source on those runners (job `s3-conformance`). R-HA-051
and Q-HA-3 had described that build on isolated runners, so the isolation those pages describe is not in the
design.

- **Component and subject:** the self-hosted runners and the pipeline. The subject is the organisation's secrets
  that the jobs of a pull request receive, and the host and network of a runner while a job runs.
- **Live or dormant:** live for every pull request from a branch of this repository. Those runs receive the
  repository's secrets: the jobs `image`, `container-scan`, `integration`, `s3-conformance`, `e2e` and `e2e-npm` log in to
  Docker Hub with `secrets.DOCKERHUB_PAT`. GitHub passes repository secrets to pull requests from the same
  repository, not to pull requests from forks. Whether a fork's pull request reaches these runners at all depends
  on the repository's Actions settings, which are not in the repository and are not verified here. For the jobs on
  GitHub-hosted runners the host-and-network part is dormant (a fresh virtual machine per job that GitHub discards
  afterwards); their secret part stays live.
- **What makes it real:** a pull request can change the workflow file or the Makefile of its own branch, and the
  changed file runs with the secrets. Whoever can push a branch to this repository can therefore read
  `DOCKERHUB_PAT` from a job. The runners are ephemeral pods ([releasing.md](../developer/releasing.md#ci)), which
  bounds what a build leaves behind. What a running job can reach from the pod's network is not verified here.

### H-564 · The platform check reads settings, not outcomes

Component: the task `backup-check` and `trove admin backup check` (R-HA-023). Subject: whether a point-in-time restore would work. The check reads `archive_mode`, the bucket's versioning and lifecycle rules and the grace period. `archive_mode = on` says PostgreSQL hands finished WAL segments to its archive command; it does not say that the command succeeds, that a base backup exists or that a restore has ever been tried. A noncurrent-version rule that matches only a prefix of the bucket counts as one that matches the bucket.

Live whenever `backup.recovery_window` is declared. What makes it real: an archive command that fails (an expired credential, a deleted bucket) while `archive_mode` stays `on`, so `trove_backup_check_ok{check="archive_mode"}` stays 1 and `TroveBackupCheckFailed` stays silent. The restore drill of R-HA-025 is the only check of the outcome, and it is not run by the installation.
