# Architecture

Status: specification — not implemented

This page fixes the skeleton of trove. It covers:
- the components inside the single binary, the process roles and what each role runs;
- the Go and Angular code layout;
- the order in which components handle the shared request paths: local download, remote download
  (hit, miss, upstream down), upload, and virtual resolution;
- the contract every package format implements;
- the background job framework;
- cross-replica coordination: which mechanism lives in PostgreSQL and which in Valkey, request
  collapsing, shared counters, cache invalidation, and the PostgreSQL connection model;
- configuration loading, encryption of stored secrets and the outbound HTTP client factory;
- the error model, the process lifecycle with drain-safe shutdown, versioning and compatibility,
  and proposed performance targets.

What a feature does for the user is specified on the topic pages listed in
[README.md](README.md#pages). This page specifies the mechanisms they build on, and it names the
owning page wherever a topic hands over. Where a request path below and an owning page disagree on
a detail, the owning page wins. This page only fixes the order of steps and the boundaries between
components.

Covers: this page draws on [repositories.md](../features/repositories.md),
[remote-repositories.md](../features/remote-repositories.md),
[storage-and-blobs.md](../features/storage-and-blobs.md),
[ha-and-deployment.md](../features/ha-and-deployment.md) and
[operations-and-monitoring.md](../features/operations-and-monitoring.md). The format contract also
draws on [formats/README.md](../features/formats/README.md), and the stored-secret key on
[identity-and-access.md](../features/identity-and-access.md) (IAM-045).

## Handover to other pages

These are architecture-adjacent entries of the catalog pages listed above that another spec page
owns. This page provides only the mechanism the owner builds on.

| Catalog IDs | Owner | What this page contributes |
|---|---|---|
| OPS-051, OPS-052 (public base URL, per-request host) | [operations-and-observability.md](operations-and-observability.md) | Middleware position and client-address rules (R-ARCH-006) |
| OPS-001 to OPS-004 (probes), OPS-008 (service status), OPS-009 (lock diagnostics) | [operations-and-observability.md](operations-and-observability.md) | Start-up order, shutdown timeout (R-ARCH-024); lock categories and connection naming (R-ARCH-017, R-ARCH-019) |
| OPS-050 (content types), OPS-065 and ART-051 (tasks and job API), OPS-070 (reindex) | [operations-and-observability.md](operations-and-observability.md) | Job queue and scheduler (R-ARCH-016) |
| ART-011, ART-029, STOR-003 and the upload/download pipeline | [storage.md](storage.md) | Request order (R-ARCH-011, R-ARCH-014), format storage hooks (R-ARCH-008) |
| RMT-* settings, collapsing misses, negative cache, metadata rewriting, merged-metadata cache | [repositories-and-proxy.md](repositories-and-proxy.md) | Request order (R-ARCH-013, R-ARCH-015), `FlightLock` on Valkey (R-ARCH-033), negative-cache keys (R-ARCH-030), change areas and signals (R-ARCH-018) |
| HA-022 to HA-071 except those listed below | [ha-and-helm.md](ha-and-helm.md) | Process model, roles, lifecycle, the connection classes behind the connection formula (R-ARCH-019), the Valkey client settings (R-ARCH-031) |
| Long-running operations, client setup snippets, UI baseline | [api-and-ui.md](api-and-ui.md) (R-API-015, R-API-033, R-UI-001) | Job columns (R-ARCH-016), snippet templates in the format contract (R-ARCH-008) |
| BEYOND-030 and the login and exchange limits (rate limits) | [api-and-ui.md](api-and-ui.md) R-API-016, [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038, [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-021 | Cluster-wide counters with a per-replica fallback (R-ARCH-034) |
| Backup and restore (INV-19) | [backup-and-restore.md](backup-and-restore.md) | The `backup` role and its connection class (R-ARCH-001, R-ARCH-019), the export classes of this page's tables (R-ARCH-016, R-ARCH-018), the revision registry entry of `rev_seq` (R-ARCH-009), the runner's restore mode (R-ARCH-026) |

<details>
<summary>Facts this page relies on, and how they were verified (2026-10-28)</summary>

| Fact | Verified by |
|---|---|
| `net/http.ServeMux` unescapes paths segment by segment, so `%2F` stays inside one segment. `{name...}` matches the remainder. Conflicting patterns panic at registration. Paths with `.`, `..` or `//` are redirected to a cleaned URL, while escaped `%2e`/`%2f` are kept. | `go doc net/http.ServeMux` with the local go1.27.3 toolchain |
| `http.ResponseController` sets per-request read and write deadlines; a read deadline that has already passed cannot be extended. | `go doc` with go1.27.3 |
| A NOTIFY payload must be shorter than 8000 bytes by default, and notifications are delivered only after commit. Notifying transactions serialize their commits on one heavyweight lock held until after commit. The commit's WAL flush and its wait for a synchronous standby happen while that lock is held; with `synchronous_commit = off`, neither happens. | [PostgreSQL NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html); `src/backend/commands/async.c`, `src/backend/access/transam/xact.c` and `src/include/replication/syncrep.h` on `REL_18_STABLE` |
| Advisory locks have a one-`bigint` key space and a two-`int4` key space, which do not overlap. `pg_locks` shows two-key locks with `objsubid = 2`. | [Lock functions](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS), [pg_locks](https://www.postgresql.org/docs/current/view-pg-locks.html) |
| pgx v5 has `Conn.WaitForNotification` and the `default_query_exec_mode` values `cache_statement`, `cache_describe`, `describe_exec`, `exec`, `simple_protocol`. An `Exec` without arguments always uses the simple protocol, which accepts several statements in one string and runs them inside the open transaction. | pgx v5.11.0 source (`conn.go`, `pgconn/pgconn.go`) |
| goose v3's Provider accepts `embed.FS` sources, Go migrations, a custom table name, and an optional session locker. Its version row carries only the version number (`database.InsertRequest{Version}`); a different table layout needs a custom `database.Store` with `DialectCustom`. Every SQL file must start with `-- +goose Up`. | goose v3.29.0 source (`provider_options.go`, `provider.go`, `provider_run.go`, `database/store.go`, `internal/sqlparser/parser.go`) |
| tern v2 takes `pg_advisory_lock(9628173550095224)`, a fixed single-`bigint` key, around every migration run, with no option to disable it. | tern v2.4.3 source (`migrate/migrate.go`) |
| In aws-sdk-go-v2, the `feature/s3/manager` Uploader is marked deprecated in favour of `feature/s3/transfermanager`. `service/s3` exposes `UsePathStyle`, `RequestChecksumCalculation` and `ResponseChecksumValidation`. | SDK source |
| Zoneless change detection is the default for new Angular applications since v21. | [angular.dev zoneless guide](https://angular.dev/guide/zoneless) |
| Latest versions: pgx v5.11.0, sqlc v1.31.1, goose v3.29.0, golang-migrate v4.20.1, tern v2.4.3, aws-sdk-go-v2 `service/s3` v1.114.6, `feature/s3/transfermanager` v0.5.1, minio-go v7.4.1, River v0.50.0, chi v5.5.0 | `proxy.golang.org/<module>/@latest` |
| Licences: pgx, goose, golang-migrate and tern are MIT. aws-sdk-go-v2 and minio-go are Apache-2.0. River is MPL-2.0. | LICENSE files upstream |
| The versions in the row above are still the latest on 2026-10-28, re-checked when the questions of this page were decided. | `proxy.golang.org/<module>/@latest` |
| For user content that must not render, current guidance names `nosniff`, `Content-Disposition: attachment`, CSP `sandbox` with `default-src 'none'`, and `Cross-Origin-Resource-Policy: same-site`. It treats a separate sandbox domain as necessary only when active content must render, or when renderer compromise and Spectre-class attacks are in the threat model. Only a domain on the Public Suffix List isolates subdomains from each other. | [web.dev: Securely hosting user data in modern web applications](https://web.dev/articles/securely-hosting-user-data) |
| A cookie named with the `__Host-` prefix is accepted by the browser only with `Secure`, `Path=/` and no `Domain`. A sibling or parent subdomain therefore cannot set or shadow it. "Same-site" compares scheme and registrable domain, so a subdomain content host is same-site with the UI. | [RFC 6265bis draft](https://datatracker.ietf.org/doc/draft-ietf-httpbis-rfc6265bis/), cookie prefixes and the "same-site" definition |
| valkey-go v1.0.79 (Apache-2.0) is the latest release. Its Sentinel client resolves the primary through the Sentinels, checks `ROLE` on the instance it connects to, follows `+switch-master`, and reconciles periodically when `SentinelOption.TopologyRefreshInterval` is set; its documentation states that without it a missed `+switch-master` binds the client to the old primary. `SentinelOption` carries its own `TLSConfig`, `Username` and `Password`. `ClientOption.AuthCredentialsFn` supplies credentials on every connection attempt. A Sentinel or standalone client pipelines over 2^`PipelineMultiplex` connections, 4 by default. Server-assisted client-side caching is on by default with 128 MiB per connection and is switched off with `DisableCache`. The command builder has `SET … IFEQ` and `DELIFEQ`. The module requires Go 1.25. | `proxy.golang.org`; source (`valkey.go`, `sentinel.go`, `internal/cmds/gen_string.go`, `go.mod`) |
| go-redis v9.23.0 (BSD-2-Clause) lists Redis CE 8.0 to 8.10 as its supported servers and calls other servers "not officially supported". Its `FailoverOptions` carry `SentinelUsername` and `SentinelPassword`; its compare-and-delete helper targets Redis's `DELEX`, not Valkey's `DELIFEQ`. | `proxy.golang.org`; source (`README.md`, `sentinel.go`, `string_commands.go`) |
| The Go client of Valkey GLIDE (v2.5.3) uses cgo to call its Rust core. | [GLIDE Go README](https://github.com/valkey-io/valkey-glide/blob/main/go/README.md) |
| Valkey 7.2, 8.0, 8.1, 9.0 and 9.1 all received patch releases on 2026-10-26 and 2026-10-27 (9.1.3, 9.0.7, 8.1.11); 9.2.0 is at rc1. Maintenance ends: 9.0 on 2028-10-21, 9.1 on 2029-05-19 (security support until 2031-05-19), 8.1 on 2028-03-31. | GitHub releases API for `valkey-io/valkey`; [Valkey releases](https://valkey.io/topics/releases/) |
| `SET … IFEQ <value>` exists since Valkey 8.1.0. `DELIFEQ key value` exists since 9.0.0 and deletes the key only if its value matches (ACL categories `@fast @string @write`). | [SET](https://valkey.io/commands/set/), [DELIFEQ](https://valkey.io/commands/delifeq/) |
| Valkey replication is asynchronous, so Sentinel does not guarantee that acknowledged writes survive a failover. A robust deployment needs at least three Sentinels in independent failure domains. Clients resolve the primary with `SENTINEL get-master-addr-by-name`, verify it with `ROLE`, and resolve again on every reconnection; Sentinel sends `CLIENT KILL type normal` to a reconfigured instance. Sentinel authenticates to ACL-protected instances with `sentinel auth-user` and `sentinel auth-pass`. | [Sentinel](https://valkey.io/topics/sentinel/), [Sentinel clients](https://valkey.io/topics/sentinel-clients/) |
| Pub/Sub delivery is at most once: a message is lost when the subscriber is disconnected or when no one is subscribed. | [Pub/Sub](https://valkey.io/topics/pubsub/) |
| A single-instance lock is `SET key <random> NX PX <ttl>`, released by a compare-and-delete. Failover-based setups cannot guarantee mutual exclusion, because a lock written to the primary can be lost before it reaches the promoted replica. | [Distributed locks](https://valkey.io/topics/distlock/) |
| PgBouncer 1.26.1 (2026-10-14) is current. In transaction pooling, `SET`/`RESET`, `LISTEN`, `WITH HOLD` cursors, SQL `PREPARE`/`DEALLOCATE` and session-level advisory locks never work. Protocol-level prepared statements work when `max_prepared_statements` is non-zero (default 200). `application_name` is a tracked startup parameter. | GitHub releases API for `pgbouncer/pgbouncer`; [features](https://www.pgbouncer.org/features.html), [configuration](https://www.pgbouncer.org/config.html) |
| Kubernetes starts the kubelet's graceful shutdown and the removal of the pod from EndpointSlices at the same time. A terminating endpoint has `ready: false` and exposes the `serving` condition. The default `terminationGracePeriodSeconds` is 30. A `preStop` hook that overruns the grace period gets a one-off 2 s extension. The `sleep` lifecycle handler runs in the kubelet and is stable since v1.34. | [Pod termination](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination), [container lifecycle hooks](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/), [KEP-3960](https://www.kubernetes.dev/resources/keps/3960/) |
| npm defaults: `fetch-retries` 2, `fetch-retry-mintimeout` 10 s, `fetch-retry-maxtimeout` 60 s, `fetch-timeout` 5 min. NuGet's enhanced retry defaults: 6 tries, 1,000 ms delay. | [npm config](https://docs.npmjs.com/cli/v11/using-npm/config), [NuGet environment variables](https://learn.microsoft.com/nuget/reference/cli-reference/cli-ref-environment-variables) |

Not verified: the behaviour of specific S3-compatible stores; the behaviour of managed Valkey
offerings, which mostly expose their own failover endpoint instead of Sentinel; the exact
endpoint-propagation delay of a given Ingress or Gateway controller. The performance targets in
R-ARCH-027 are proposals, not measurements.

</details>

## Process and components

### R-ARCH-001 · One binary, identical replicas, process roles
Priority: **MVP** · Covers: HA-001, HA-002, HA-009, HA-010, HA-015

trove MUST build into one statically linked binary, `trove` (`CGO_ENABLED=0`). It contains the
management API, every package-format protocol, the embedded Angular build, the schema migrations
and the background workers (INV-1, INV-2).

**Status.** The binary knows the roles `api`, `packages`, `worker`, `web` and `all` (phase 6a, WP6a.1;
[ADR-153](../decisions.md#adr-153--trove-is-cut-into-task-areas-roles-of-one-binary-network-seams-only-at-trust-or-load-boundaries),
[ADR-155](../decisions.md#adr-155--three-roles-api-packages-and-worker-variants-are-pools), Q-ARCH-294 to Q-ARCH-303): the
tables below are the data of [internal/app/roles.go](../../internal/app/roles.go) and
[steps.go](../../internal/app/steps.go), and a test holds them. The registry, the status screen, the content routes of `api` and the
fetch share's count of the replicas that fill are built (WP6a.4), as are the queue restriction of R-ARCH-016 (WP6a.2) and the chart's
Deployment per role (WP6a.3). What is left of phase 6a is its tests and the dev profile
([roadmap](../coverage/roadmap.md#phase-6a--task-areas), WP6a.5).

| Invocation | Purpose |
|---|---|
| `trove serve --role api\|packages\|worker\|web\|all` | Runs one replica of a role. The flag overrides `process.role`. |
| `trove serve --role bastion` | Runs one replica of the trove Bastion, deployed only by the chart `trove-bastion` ([bastion.md](bastion.md) R-BAS-001, phase 5b). |
| `trove migrate [up\|status]` | Applies or lists the embedded migrations behind the compatibility gate (R-ARCH-026). |
| `trove backup create\|list\|verify\|delete\|prune\|restore` | The `backup` role: creates a consistent backup into the configured S3 target, lists, verifies, deletes or prunes the backups there, or restores one into an empty installation, then exits (INV-19, [backup-and-restore.md](backup-and-restore.md) R-BAK-009, R-BAK-016, R-BAK-018 to R-BAK-021). It has no client listener and uses no Valkey. The chart runs `create` as a CronJob and `restore` as a Job. |
| `trove admin …` | Bootstrap and recovery: break-glass administrator ([identity-and-authentication.md](identity-and-authentication.md)), `admin config validate` ([operations-and-observability.md](operations-and-observability.md)), `admin secrets status` (R-ARCH-021; the same status is served by `GET /api/v1/system/encryption`). The break-glass commands run in an `api` pod, the only role that mounts the break-glass secret. |
| `trove --version` | Version, commit, build date. |

**Roles** (INV-2, Q-ARCH-294, Q-ARCH-295). A role is a set of enabled subsystems and wiring areas. Every replica of a
role runs the same ones and is interchangeable with any other replica of that role:

| Role | Serves | Holds | Deployed by the chart as |
|---|---|---|---|
| `api` | the route families `ui`, `api`, `ci_exchange` of every endpoint whose surface holds them ([endpoints.md](endpoints.md) R-EP-005) | the session store, the OIDC client secrets, the break-glass password | `<fullname>-api`, its pools and the `api` part of endpoint pools ([ha-and-helm.md](ha-and-helm.md) R-HA-002, R-HA-062) |
| `packages` | the route families `packages`, `files`; tokens only, a session cookie is ignored as absent; the fills of client misses and the shadowing probes | no session store, no login secret | `<fullname>-packages`, its pools and the `packages` part of endpoint pools |
| `worker` | the job queue, its leases and the scheduler, the mirror's passes, the advisory sync; the queues it claims are configurable (R-ARCH-016) | the lease pool | `<fullname>-worker` and its pools by queue |
| `web` | `api` and `packages` in one process: every family | everything `api` and `packages` hold | `<fullname>-web` only as the bridge of the upgrade across the cut (Q-ARCH-295, [ha-and-helm.md](ha-and-helm.md) R-HA-064) |
| `all` | every role but `bastion` in one process | everything | never; a binary started outside the chart during development |
| `backup`, `bastion` | unchanged: the `trove backup` subcommand (above), the Bastion's read flow ([bastion.md](bastion.md) R-BAS-001) | their own | the backup CronJob and restore Job; the chart `trove-bastion` |

| Subsystem | `api` | `packages` | `worker` | `backup` | `web`, `all` |
|---|---|---|---|---|---|
| Client listener `:8080` and the listeners of further endpoints (R-ARCH-005, [endpoints.md](endpoints.md) R-EP-002), each with the families of its surface that the role serves (Q-ARCH-298) | yes | yes | no | no | yes |
| Ops listener `:8081`: probes and metrics | yes | yes | yes | `/metrics` only, while a run lasts ([backup-and-restore.md](backup-and-restore.md) R-BAK-014) | yes |
| Main pool, transaction-scoped work only (R-ARCH-019) | yes | yes | yes | no | yes |
| Session-scoped PostgreSQL connections: the lease pool (R-ARCH-017) | no | no | yes | its own control, snapshot and export connections (R-ARCH-019) | `all` only |
| Job claim loop, reaper and scheduler (R-ARCH-016); outbox dispatcher (P1) | no | no | yes | no | `all` only |
| Enqueueing jobs | yes | yes | yes | no | yes |
| Valkey client (R-ARCH-031) | yes | yes | yes | no | yes |
| Change-log poll and signal subscriber (R-ARCH-018) | yes | yes | yes | no | yes |
| Replica registry row and heartbeat ([operations-and-observability.md](operations-and-observability.md) R-OPS-003) | yes | yes | yes | no | yes |

**Wiring areas** (R-ARCH-024 step 8, Q-ARCH-209, Q-ARCH-294). The areas are the steps of
[internal/app/steps.go](../../internal/app/steps.go); a role enables them as data (`SubsystemsFor` in
[internal/app/roles.go](../../internal/app/roles.go)), and `planSteps` refuses a role whose steps cannot be satisfied. Every
serving role (`api`, `packages`, `worker`, `web`, `all`) builds the infrastructure areas `keys`, `postgres`, `audit`, `blob`,
`valkey`, `coordination`, `infra` and the domain areas `keyring`, `formatports`, `exposure`, `repository`, `virtual`,
`invalidation`, `proxy`, `identity`, `authz`, `ownership`, `ci`, `vuln`, `formats` and `files`, because its jobs, its caches or its
routes read them. The areas of the client listener are split by route family:

| Area (step) | What it builds | `api` | `packages` | `worker` | `web`, `all` |
|---|---|---|---|---|---|
| `limits` | the cluster limiter set of the client listeners (R-ARCH-034) with the failure, flow-start, CI and Bastion-download limiters | yes | yes | no | yes |
| `proxy-engine` | the remote engine: fills, flights, the negative cache (R-REPO-025) | **no** | yes | no | yes |
| `virtual-engine` | the virtual engine (R-REPO-019) | the explanation only, over a read-only view of the remote members (Q-ARCH-294) | yes | no | yes |
| `formathost` | the format host and the protocol dispatcher of `/api/<format>/` | **no** | yes | no | yes |
| `packages-api` | the services of the package screens of the management API (R-API-029, R-API-030) | yes | **no** | no | yes |
| `vuln-api` | the services of the vulnerability operations of the management API | yes | **no** | no | yes |
| `sessions` | the session store (R-AUTH-001) | yes | **no** | no | yes |
| `login` | the OIDC and break-glass login with its browser routes | yes | **no** | no | yes |
| `auth` | the authentication stage of the chain | with the session credential | tokens only: no session store, a session cookie is ignored as absent | no | with the session credential |
| `endpoints` | the rate-limit classes of the endpoints (R-EP-015) | yes | yes | no | yes |
| `api` | stage 7 of the chain: CORS and the request limits of R-API-016, applied on every endpoint chain despite the name | yes | yes | no | yes |
| `ui-assets` | the shared asset store of the UI ([ha-and-helm.md](ha-and-helm.md) R-HA-020, Q-HA-120): the build's hashed files published before the replica turns ready and renewed hourly, the files of the other release read from it | yes | **no** | no | yes |
| `ci_exchange` | the CI token exchange (`POST /api/v1/auth/oidc/token`, the npm exchange of R-NPM-022 at P1) | yes | **no** | no | yes |
| `mirror` | the counters and the gauge source of R-REPO-057 (every role), the engine built at the first job of a process that claims one (R-REPO-053, Q-ARCH-312) | yes | yes | yes | yes |
| `mirror-engine` | the remote engine and the format host behind `mirror.Host`: in `all` the engine and host of the client path, in `worker` an engine of its own for background requests (Q-ARCH-316, Q-ARCH-317) | **no** | **no** | yes | `all` only |

- A `packages` replica MUST NOT build the areas `sessions`, `login`, `ci_exchange`, `packages-api` and `vuln-api`, MUST NOT
  read the OIDC client-secret or break-glass files (the chart does not mount them, [ha-and-helm.md](ha-and-helm.md) R-HA-030),
  and evaluates no session cookie on any endpoint ([endpoints.md](endpoints.md) R-EP-010). Its identity loops are the
  last-used flush of tokens only ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-031).
- An `api` replica MUST NOT build the areas `proxy-engine` and `formathost`, so no route it serves can fill a cache or
  forward to an upstream: the only upstream request it sends is the upstream test of
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-024, which takes no fetch slot (Q-ARCH-297). The
  explanation of a virtual repository (R-REPO-019) asks the remote members only what the item store, the cache entries,
  the negative store and the circuit state answer (`Cached`, `EntryState`); a read-only view gives them and refuses
  `Metadata` and `Probe` with `Unavailable`. The UI's downloads and uploads reach `api` through the session-only content
  routes of [api-and-ui.md](api-and-ui.md) R-API-025 (Q-UI-106), which go through the `files` area, never through the
  format host. The `files` area of `api` is given a Host without a remote engine, resolver, dispatcher or route, only for
  the serve check of the item store, which reads a format's stored marks through the data port (Q-ARCH-301). Of the
  operations of the management API, only the explanation of a resolution reaches an engine (Q-ARCH-301).
- `web` enables the union of the areas of `api` and `packages`, which is the list of the release before the cut; `all`
  enables every area. A replica started with `web` or `all` logs a warning that names the role.
- **Two areas take a part of another area as an optional input** (Q-ARCH-300). The `auth` area takes the session store of
  the `sessions` area, and the `virtual-engine` area the remote engine of the `proxy-engine` area. A role that enables the
  other area hands the step its result and `planSteps` insists it is listed before; a role that does not (`packages` has no
  `sessions`, `api` has no `proxy-engine`) hands the step the zero value, and the step builds the variant of its part: the
  stage over tokens only, the engine over the read-only view. The role's areas decide, not a nil check, so a role that
  enables the area and gets no result stops the start-up.
- A role also names the **wiring areas** it enables (R-ARCH-024 step 8, Q-ARCH-209): the units of
  composition that build a typed part of the replica, such as the database, the audit recorder, the
  object store, the Valkey client and the coordination layer, and the domain areas on top of them.
  The areas are data of the role, not a consequence of which component happens to be present: a
  `worker` enables the areas whose jobs it runs, an `api` or `packages` replica the areas its families serve. The
  shell profile (an HTTP shell without PostgreSQL, Valkey and the object store, used by the
  composition root's own tests and the drain fixture) is a role with the listeners and no
  infrastructure areas, and it is only reachable through the test options, never through
  configuration.
- `process.role` has no default. `trove serve` without a role exits non-zero with
  `process.role must be api, packages, worker, web or all`. The chart deploys `api`, `packages` and `worker` as separate
  Deployments, each with its own replica count, autoscaler, disruption budget, ingress policy and topology spread
  ([ha-and-helm.md](ha-and-helm.md) R-HA-002).
- **What "web" means elsewhere** (INV-2). The pages written before the cut name the client-serving role `web`. Such a
  statement applies to the role that serves the route family it concerns (`api` for `ui`, `api` and `ci_exchange`,
  `packages` for `packages` and `files`), and to both where it concerns every request, such as the listener, the
  drain, the readiness checks and the limits of the chain. A statement about the `web` Deployment, its values or its
  Service names the bridge of R-HA-064 only where it says so.
- An `api` or `packages` replica MUST NOT:
  - claim or run jobs, or hold a lease;
  - open a session-scoped PostgreSQL connection;
  - run a background loop whose iterations take longer than a request. Its loops are each bounded
    by a deadline:
    - the change-log poll and signal subscriber (R-ARCH-018), the Valkey client (R-ARCH-031) and
      the registry heartbeat;
    - the flushes of the write-only buffers of R-ARCH-032: download statistics, the activity and
      use buffers of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012 and
      R-REPO-046 (P1), and the asynchronous audit buffer
      ([operations-and-observability.md](operations-and-observability.md) R-OPS-015);
    - on `api`, the identity loops of [identity-and-authentication.md](identity-and-authentication.md)
      R-AUTH-042: the OIDC discovery refresh and JWKS refetch (R-AUTH-004), the re-read of the
      client-secret and break-glass files including the break-glass rotation check (R-AUTH-014),
      the last-used flush (R-AUTH-031) and the pending-flow count refresh (R-AUTH-038); on `packages`, the last-used
      flush;
  - leave work running after a response that a later request depends on.

  Work that a request starts and that outlasts it is enqueued as a job, and the request answers
  with the job reference ([api-and-ui.md](api-and-ui.md) R-API-015). Examples are reindexing,
  repository deletion, bulk copy or move, garbage collection and re-encryption. Three bounded
  exceptions remain, each ended at shutdown and none leaving anything half-stored
  (R-ARCH-024):
  - on `packages`, the remote fetch that collapsed waiters depend on, which runs detached from its initiating
    client ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026);
  - on `packages`, the completion of a metadata refresh after the stale copy was served (R-REPO-027);
  - on both, the replica's own support-bundle part
    ([operations-and-observability.md](operations-and-observability.md) R-OPS-027, P1). It is
    bounded by `support.max_cpu_profile_seconds` plus the upload, never runs past
    `support.collect_timeout`, holds no database connection, and is abandoned at `SIGTERM`; the
    bundle then ends `partial`.
- A request of a family that the replica's role does not serve answers the `404` problem of an unknown path
  ([endpoints.md](endpoints.md) R-EP-005), never `403`, `405` or a redirect: the edge splits a host by path prefix
  between the roles ([ha-and-helm.md](ha-and-helm.md) R-HA-037), and a request the edge sends to the wrong role, or one
  that reaches a role's Service directly, finds no route rather than another role's answer. The edge's routing is load
  sharing, not a trust boundary: each role's own router decides what it serves. The same holds for a request without a
  credential: a route that is reachable without one only where it is served (the UI shell, the login tree, the CI
  exchange, the public operations, npm's login start) is decided like an unknown path where the role does not serve it,
  so the `401` of an endpoint that admits no anonymous request, or the anonymous pass of one that does, is the same
  answer as for `GET /does-not-exist` (Q-ARCH-302). And it holds before and after authentication for every rule of a
  chain stage that a path selects: the headers of the management API ([api-and-ui.md](api-and-ui.md) R-API-017), its CORS
  answers (R-API-018) and its rate-limit classes (R-API-016), the path rule of the session cookie, the challenge and the
  hard limit on an unsafe anonymous request to `/api/v1/` ([identity-and-authentication.md](identity-and-authentication.md)
  R-AUTH-001, R-AUTH-002, R-AUTH-037) apply only to a family the process serves on the endpoint. A request of an unserved
  family is the request to `/does-not-exist` with the same method and headers, in status, header and body, apart from
  `X-Request-Id`, `Date`, the problem's `requestId` and `instance` and the `Content-Length` that follows them (Q-ARCH-306).
- A `worker` replica serves only the ops listener and is not selected by any client Service.
- Load is shared by adding replicas of a role (INV-12). No code path depends on the number of
  replicas of a role, and each role scales and drains on its own. A role is split further by path prefix (`api`,
  `packages`) or by queue (`worker`) into pools through chart values without code changes (INV-2,
  [ha-and-helm.md](ha-and-helm.md) R-HA-062).
- No replica holds state that another replica needs to answer a later request (INV-18,
  R-ARCH-032). This holds across roles too: a flow that spans both roles, such as an upload through the UI followed
  by a client download or the command-line login of ADR-049 (P1), meets in PostgreSQL and object storage only.
  - Durable state lives in PostgreSQL, the system of record, and in object storage. Shared
    ephemeral state lives in Valkey (INV-3, R-ARCH-030).
  - In-process state is limited to caches that can be rebuilt (R-ARCH-018) and to the scratch
    space of the request or job currently running ([storage.md](storage.md)).
- There is no primary node. Work that must run once per cluster runs as a job, where the queue
  guarantees one claimant (R-ARCH-016), or under a named lease (R-ARCH-017). Both live in
  PostgreSQL.
- An `api`, `packages`, `worker` or `web` replica registers itself on start in the replica registry, with its role
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-003). It takes traffic or jobs as
  soon as it is ready. No other step is needed to add or remove a replica.
- One replica and many run the same code path. Even a single replica uses external PostgreSQL,
  Valkey and S3-compatible storage (INV-3, INV-4).
  - The local profile runs PostgreSQL with a standby (CloudNativePG, two instances), Valkey with
    Sentinel in the reference topology of [ha-and-helm.md](ha-and-helm.md) R-HA-056, and
    SeaweedFS (R-HA-051), so failovers can be exercised locally.
  - Production runs HA PostgreSQL, Valkey with Sentinel and S3 (INV-12).
  - The chart bundles no production database and no production Valkey.
- Splitting roles is the trove form of separately scaled workloads (HA-015). The UI is never a
  separate workload because it is embedded (INV-1); it is served by `api`.

```yaml
process:
  role: ""            # no default: api | packages | worker | web | all; the chart sets it per Deployment (--role)
  # role: packages    # example
```

Acceptance criteria:
- On Linux, `file trove` reports a statically linked executable, and `go version -m trove`
  shows `CGO_ENABLED=0`.
- `trove serve` without `--role` and without `process.role` exits non-zero and names
  `process.role`.
- **The role table is data and the architecture test holds it** (Q-ARCH-294). A test in `internal/app` compares
  `SubsystemsFor` of every role with the tables above: the areas of each role equal its column, in step order;
  `packages` has none of `sessions`, `login`, `ci_exchange`, `packages-api`, `vuln-api`; `api` has neither
  `proxy-engine` nor `formathost`; `web` is the union of `api` and `packages`; and `planSteps` accepts every role.
- A replica started with `--role worker` has no socket on `:8080` (`ss -ltn`). The instance ID of a
  replica started with `--role api` or `--role packages` never appears in `job.locked_by`, and `pg_stat_activity`
  never shows a connection named `trove:<that replica>:lease`.
- On a `packages` replica, `GET /ui/`, `GET /`, `GET /auth/oidc/corp/start`, `GET /api/v1/system/ping` and
  `POST /api/v1/auth/oidc/token` answer the problem of `GET /does-not-exist` (the `404` with a token or with anonymous access on, the same `401` `credentials_required` without a credential while it is off, Q-ARCH-302), and a request with a valid
  `__Host-trv_session` cookie and no token to `GET /api/npm/npm-local/@acme%2fsdk` is treated as one without a
  credential (`401` `credentials_required` while anonymous access is off); on an `api` replica,
  `GET /api/npm/npm-local/@acme%2fsdk`, `GET /files/generic-local/x` and `GET /v2/` answer that problem, and a test
  double of the upstream records no request from it while the e2e suite runs, except the upstream tests it sends.
- With `api.cors.allowed_origins` set and the rate limits on, each of these requests and `OPTIONS /api/v1/system/ping` on
  `packages`, `OPTIONS /files/generic-local/x` on `api`, sent with no credential, a token, a rejected token, a live session
  cookie with and without its CSRF token, an ended session cookie, the Fetch Metadata headers of a browser or as the CORS
  preflight of an allowed origin, with anonymous access off and on, answers what the request with the same method and
  headers to `/does-not-exist` answers: the same status, the same header apart from `X-Request-Id`, `Date` and
  `Content-Length`, and the same body apart from `requestId` and `instance` (Q-ARCH-306).
- One of three `packages` pods is deleted with `kubectl delete pod --grace-period=0 --force` during a
  loop of 100 `npm ci` runs through the edge. The loop finishes with npm's default
  `fetch-retries`.

### R-ARCH-002 · Component model and dependency rules
Priority: **MVP** · Covers: HA-003, HA-004, HA-006

```mermaid
flowchart LR
  subgraph Clients
    PM["Package clients<br/>npm, pnpm, Yarn, dotnet, nuget"]
    BR["Browser<br/>Angular UI"]
    CI["GitLab CI job"]
    PR["Prometheus, kubelet probes"]
  end
  subgraph Replica["trove replica (one Go process)"]
    direction TB
    HS["httpserver<br/>:8080 clients, :8081 ops"]
    AN["auth<br/>oidc, session, token, ciexchange"]
    AZ["authz"]
    API["api/v1"]
    UI["webui<br/>embedded Angular build"]
    FM["formats<br/>npm, nuget, ..."]
    CT["formats/catalog<br/>format facts, name rule, exposure (leaf)"]
    RP["repository<br/>snapshot, routing"]
    VT["virtual<br/>per-caller resolution, merged cache"]
    PX["proxy<br/>freshness, negative cache, collapse"]
    BL["blob<br/>spool, digests, s3"]
    MT["meta<br/>postgres, migrations"]
    CL["cluster<br/>valkey, change log, signals,<br/>flight lock, counters, leases"]
    CA["cache<br/>in-process"]
    JB["jobs<br/>queue, workers, scheduler"]
    AU["audit"]
    BK["backup<br/>create, restore (backup role)"]
    OB["observability<br/>slog, metrics, traces, health"]
    OUT["outbound<br/>HTTP client factory"]
    SE["secrets<br/>stored-secret key ring"]
  end
  PG[("PostgreSQL<br/>system of record")]
  VK[("Valkey<br/>primary, replicas, Sentinel")]
  S3[("S3-compatible<br/>object storage")]
  IDP["OIDC provider"]
  GL["GitLab issuer JWKS"]
  UP["Upstream registries<br/>registry.npmjs.org, api.nuget.org"]

  PM --> HS
  BR --> HS
  CI --> HS
  PR --> HS
  HS --> AN
  AN --> AZ
  HS --> API
  HS --> UI
  HS --> FM
  FM --> RP
  FM --> PX
  FM --> VT
  VT --> RP
  VT --> PX
  FM --> CT
  RP --> CT
  AZ --> CT
  API --> RP
  API --> JB
  RP --> MT
  PX --> OUT
  PX --> BL
  OUT --> UP
  OUT --> IDP
  OUT --> GL
  BL --> S3
  MT --> PG
  CL --> PG
  CL --> VK
  PX --> CL
  HS --> CL
  JB --> CL
  CA --- CL
  JB --> MT
  AU --> MT
  SE --> MT
  BK --> MT
  BK --> BL
```

| Component | Package | Responsibility | Behaviour specified in |
|---|---|---|---|
| httpserver | `internal/httpserver` | Listeners, top-level routing, middleware, client address | R-ARCH-005, R-ARCH-006 |
| auth | `internal/auth/{oidc,session,token,ciexchange}` | Principal resolution, OIDC login, tokens, CI exchange | [identity-and-authentication.md](identity-and-authentication.md), [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| authz | `internal/authz` | Permission decisions | [authorization.md](authorization.md) |
| scope | `internal/auth/scope` | The value type of a token's scope (leaf), shared by the stage, the token service and the decision point | R-AUTH-024 |
| jws | `internal/auth/jws` | Compact-JWS parsing and verification and JWK sets (leaf), shared by the login and the subject-token validation of the CI exchange | R-AUTH-006, R-CI-004, Q-CI-19 |
| vuln | `internal/vuln`, `internal/vuln/{advisory,match,osv,metrics,store,sync,evaluate,triage,report}` | Vulnerability scanning: the domain vocabulary and ports at the root; the advisory records, the matcher, the OSV decoder and the metrics are leaves | [vulnerability-scanning.md](vulnerability-scanning.md), Q-VULN-23 |
| catalog | `internal/formats/catalog` | The format catalog (leaf): every format's facts, its package-name rule and its exposure facts; `repository` and `authz` read it | R-REPO-003, R-AUTHZ-005, Q-ARCH-188 |
| api | `internal/api/v1`, `internal/api/edge` | Management REST API; the conversions every HTTP entry point shares (the request to the `authz.Subject`, the holder of a request's index) | [api-and-ui.md](api-and-ui.md), R-AUTHZ-015 |
| dispatch | `internal/api/dispatch` | The format dispatcher: `/api/<id>/<repoKey>/…` for every format with routes, and the error shape of the prefix | R-ARCH-005, R-ARCH-011, Q-ARCH-214 |
| formathost | `internal/formathost` | The `Host` of the format contract and the adapters of the item store's hooks and the generic layer's seam; the data ports are bound by the composition root | R-ARCH-009, Q-ARCH-193, Q-ARCH-212 |
| packages | `internal/packages` | The generic package rows: the package store and the package writer that raises `packages.rev_seq` | R-ARCH-009, Q-ARCH-212 |
| webui | `internal/webui`, `internal/webui/shared` | Embedded Angular build; the shared asset store of the UI's hashed files on the blob store, which the handler takes as an interface (Q-ARCH-332) | [api-and-ui.md](api-and-ui.md) (R-UI-001), R-ARCH-004, [ha-and-helm.md](ha-and-helm.md) (R-HA-020, Q-HA-120) |
| formats | `internal/formats`, `internal/formats/<id>` | Package protocols behind the plug-in contract | R-ARCH-008 to R-ARCH-010; [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| repository | `internal/repository` | Repository model and snapshot, request classification, caller-independent routing of virtual repositories (members, claims, pin rules) | [repositories-and-proxy.md](repositories-and-proxy.md) |
| proxy | `internal/proxy`, `internal/proxy/{upstream,invalidation}` | Remote fetch engine, per-remote upstream clients, cache invalidation | [repositories-and-proxy.md](repositories-and-proxy.md) |
| virtual | `internal/virtual` | Per-caller resolution of virtual repositories, merged-metadata cache, shadowing check (Q-ARCH-192) | [repositories-and-proxy.md](repositories-and-proxy.md) |
| formatdata | `internal/formatdata/<id>` | Implementations of the store ports that formats declare for their own tables (Q-ARCH-193) | [formats/npm.md](formats/npm.md) |
| blob | `internal/blob`, `internal/blob/s3` | Spool, digests, content-addressed objects | [storage.md](storage.md) |
| meta | `internal/meta`, `internal/meta/postgres` | Transactions, generated queries, embedded migrations | [storage.md](storage.md) (schema of items and packages), R-ARCH-016, R-ARCH-019, R-ARCH-026 |
| cluster | `internal/cluster` | Valkey client and key registry, change log and signals, `FlightLock`, shared counters, leases, advisory-lock categories | R-ARCH-017, R-ARCH-018, R-ARCH-030, R-ARCH-031, R-ARCH-033, R-ARCH-034 |
| cache | `internal/cache` | Bounded in-process caches and their invalidation hooks | R-ARCH-018 |
| jobs | `internal/jobs` | Queue, workers, scheduler | R-ARCH-016 |
| audit | `internal/audit` | Audit events | [operations-and-observability.md](operations-and-observability.md) |
| backup | `internal/backup` | `trove backup` create, restore, list, verify and delete; the table registry | [backup-and-restore.md](backup-and-restore.md) |
| observability | `internal/observability` | Logs, metrics, traces, health checks | [operations-and-observability.md](operations-and-observability.md) |
| outbound | `internal/outbound` | Every outbound HTTP client | R-ARCH-022; settings in [repositories-and-proxy.md](repositories-and-proxy.md) |
| secrets | `internal/secrets` | Stored-secret encryption, sealed-column registry, key-ring status | R-ARCH-021 |
| config | `internal/config` | Configuration schema and loading | R-ARCH-020; [operations-and-observability.md](operations-and-observability.md) |
| errs | `internal/errs` | Error kinds and rendering | R-ARCH-023 |

Normative rules:
- PostgreSQL and S3-compatible object storage hold every piece of durable state (INV-3, INV-4).
  Valkey holds shared ephemeral state only, and nothing in it is the only copy of durable data
  (R-ARCH-030). There is no other stateful dependency.
- Shared binary storage (HA-004). The MVP has exactly one binary-store type: S3-compatible object
  storage that every replica reaches over the network.
  - There is no node-local file-system store, no cluster-replicated local store and no redundancy
    factor. Copies and durability are the object store's job (INV-4, INV-12).
  - The catalog leaves open which storage type a deployment gets by default. That choice does not
    arise in trove, and no default can become unsafe when the replica count is raised.
  - Any store type added later MUST be shared by every replica. A store that is safe on one node
    only (STOR-008, P2 in [storage.md](storage.md)) MUST refuse to start while the replica registry
    shows another live replica.
- Replicas never talk to each other. There are no peer addresses and no replica-to-replica HTTP.
  Configuration changes, cache invalidation, locks, counters and job hand-off go through PostgreSQL
  and Valkey, as R-ARCH-030 assigns each mechanism (INV-12). The catalog's per-node peer address
  (HA-006) therefore does not exist in trove.
- Imports form layers:
  1. Edge: `httpserver`, `api/v1`, `api/edge`, `api/dispatch`, `webui`, `formats` (the contract and the format handlers), `formathost`.
  2. Domain: `repository`, `files`, `items`, `packages`, `proxy`, `virtual`, `formatdata`, `authz`, `auth`, `jobs`, `audit`, `backup`.
  3. Infrastructure: `meta/postgres`, `blob` (the store and its spool; the S3 client is `blob/s3`), `cluster`, `outbound`, `secrets`.
  4. Leaf: `config`, `errs`, `buildinfo`, `observability`, `cache`, `meta`, `backup/tables`, `tlsfiles`, `formats/catalog`, `formats/nuget/id`, `formats/nuget/version`, `formats/nuget/nupkg`, `formats/nuget/nuspec`, `formats/<id>/rows`, `auth/scope`, `auth/jws` (the compact-JWS verifier the login and the CI exchange share, Q-CI-19), `ci/reason`, `ci/claims`, `ci/subject` (the pure parts of the CI federation, Q-CI-21). Every layer may import them, and they import only each other.
     `formats/<id>/rows` is the values of a format's data port, shared by the format and `formatdata/<id>` (Q-ARCH-193). It is a leaf by rule, not by a
     list entry: archtest's `layerOf` gives every `internal/formats/<id>/rows` the leaf layer, and `CheckLayers` fails it when it imports anything but `errs` and
     the standard library (npm and NuGet each have one).
     The four NuGet rule libraries (`id`, `version`, `nupkg`, `nuspec`) are leaves by list entry: the format, its data port and its jobs share one parser, and the layer
     rule keeps their imports to `errs`, the standard library and each other.

  No lower layer imports a higher one. Format packages reach storage only through the host interface
  (R-ARCH-009), so the diagram has no arrow from `formats` to `blob`.
- Only `meta/postgres` and `cluster` import pgx. Only `cluster` imports the Valkey client
  (R-ARCH-031). Only `blob/s3` imports the S3 SDK. Format packages import none of them
  (R-ARCH-008).
- `backup` follows the same rules: it reaches PostgreSQL through an export and load API of
  `meta/postgres` (snapshot, table export, restore load), and both the blob store and the backup
  target through clients built by `blob/s3`, one per bucket and credential
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-015).
- `internal/archtest` walks `go list -deps -json ./...` and fails on any forbidden import.

Acceptance criteria:
- Adding an import of `internal/meta/postgres` to `internal/formats/npm` makes
  `go test ./internal/archtest` fail and name the edge.
- `grep -rn "http.Client{\|net.Dial" internal --include=*.go` finds matches only in
  `internal/outbound` and in `_test.go` files.
- Adding an import of `github.com/valkey-io/valkey-go` to `internal/proxy` makes
  `go test ./internal/archtest` fail and name the edge.

### R-ARCH-032 · No state between requests on one replica (INV-18)
Priority: **MVP** · Covers: HA-001, HA-009

The Kubernetes Service distributes requests at random and without session affinity (INV-12). Two
requests of one client may therefore reach different replicas, even two requests on one keep-alive
connection through an edge proxy that balances per request.

- No request may depend on state that an earlier request left in one replica's memory or on its
  local disk. Every kind of per-replica state is listed here; a new kind needs a row in this table
  before it ships:

| Per-replica state | Lifetime | Why it does not break INV-18 |
|---|---|---|
| Registered in-process caches (R-ARCH-018): the repository snapshot; the proxy's copies of the claims and pin-rule trie, gone packages, circuit state, global offline switch and cache invalidations ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-023, R-REPO-029, R-REPO-031, R-REPO-045); the grant index; the identity caches `auth_tokens`, `auth_sessions`, `auth_principals` and `auth_settings` ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023); the rendition cache; the merged-metadata cache; the per-remote upstream HTTP clients with their connection pools (`proxy_upstream_clients`, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-049, Q-REPO-89); CI issuer keys; from M4b the advisory source state `vuln_source_state` and the rendered NuGet vulnerability page `vuln_nuget_page` ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-027) | Until evicted, invalidated or `SafetyTTL` | Each is a copy of PostgreSQL state. It is self-validating (`rev_seq`, epochs) or kept within the propagation bound of R-ARCH-018, and a fill never re-inserts a value that a change has already invalidated (R-ARCH-018), so another replica gives the same answer from PostgreSQL. An epoch that went backwards or was reused after a PostgreSQL failover flushes the area on every replica, for every change area (R-ARCH-018, change-area step 5). |
| The last read advisory data state per ecosystem, kept beside the registered cache `vuln_source_state` ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-027, Q-VULN-72) | Replaced by every successful read of the state; kept while the read fails | The answer a caller gets when the refill is slow or failed, so that no download waits for the database. A copy of PostgreSQL state that only a repository with `onMissingData: block` reads, and only when no finding blocks already; its age shows through the state's synced time (`Stale`). It outlives the cache's `SafetyTTL` on purpose: a state read that fails says nothing about the data, and the download's own lookup reads PostgreSQL anyway. |
| Metadata-bytes cache, keyed by blob digest ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-025) | Until evicted | Self-validating: which digest a request serves is always read from PostgreSQL, and the key is the content address, so every replica serves the same bytes. |
| Negative cache of unknown repository keys (R-ARCH-011 step 2) | Until evicted, at most `SafetyTTL` | A copy of a PostgreSQL miss. Every `repo_config` change evicts it on every replica within the bound of R-ARCH-018, so a new repository answers `404` on a replica only if that replica missed the key shortly before the creation, and for at most that bound ([api-and-ui.md](api-and-ui.md) R-API-052). |
| Negative-cache L1 (R-ARCH-030) | At most the checked lifetime of the Valkey entry it copies | A copy of an upstream `404` or `410`, not of PostgreSQL state. Any replica may answer from such an entry or ask the upstream within `notFoundTtlSeconds` ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-028), so both answers are the same contract; an invalidation applies to the L1 as to Valkey. |
| Caches of external documents: OIDC discovery and JWKS of the login providers ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-004) | Until their refresh | A copy of what the provider publishes. An unknown `kid` makes the replica refetch, at most once per provider and replica per 60 s, so right after a provider signs with a new key, one replica may reject a login that another accepts, for at most that throttle. A provider that publishes a key before signing with it never opens this window. The GitLab issuer keys live in PostgreSQL and are a registered cache above ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-003). On an unknown `kid`, a CI issuer re-reads its `ci_issuer_keys` row at most once per issuer and replica per `cluster.epoch_poll_interval`, then refetches under the same kind of per-replica throttle (`ci.signing_keys.unknown_kid_refresh_interval`). A key that any replica has stored is accepted on every replica within one poll interval, and a forged `kid` costs no PostgreSQL query of its own ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-003, Q-CI-12). |
| Pass-through answers of remote repositories, the registered cache `proxy_query_results` (R-ARCH-013 step 6, Q-ARCH-270): the forwarded NuGet search results ([formats/nuget.md](formats/nuget.md) R-NUGET-016) | At most the period the format asks (`nuget.search.remote_result_ttl`), and never more than one hour | A copy of an upstream answer, stored unfiltered; patterns, claims and permissions are applied on every use. Any replica may answer from its copy or ask the upstream within the period, so both answers meet the same contract. |
| Break-glass verifier and OIDC client secrets read from Secret files, and the loaded break-glass file content ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-004, R-AUTH-014) | Until the file changes | Replicas switch at different moments on a rotation. The session end of a break-glass rotation is one PostgreSQL transaction, so no replica keeps a session that another has ended (R-AUTH-014, Q-AUTH-14). |
| Pending-flow count ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038) | At most 5 s old | A copy of a PostgreSQL count that bounds a `503` decision. |
| Per session, the newest `last_seen_at` a replica has read ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-010) | While the replica holds the session | A lower bound of the row that only saves touch statements and never decides an end; the session's deadlines are decided from its row. |
| In-process single-flight groups (R-ARCH-033), and the credential lookups shared while the freshness gate is closed ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023) | One flight; a shared credential lookup at most `auth.revocation_bound` old | Only concurrent requests join; nothing outlives the flight, and a shared lookup is never cached. |
| Spool and temporary files (`emptyDir` or a generic ephemeral volume, INV-13) | One request or one job | Deleted on every path and at process start ([storage.md](storage.md) R-STOR-004). |
| Write-only buffers: download statistics (R-ARCH-011), `lastActivityAt` ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012), metadata `last_used_at` (R-REPO-046, P1), token last-used times ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-031) | Until the next batch flush (the last-used buffer: 60 s) | Requests write them and never read them. They are flushed at shutdown (R-ARCH-024 step 6); a crash loses at most one batch, which the next use records again. |
| Asynchronous audit buffer and the audit deduplication windows with their open denial summaries ([operations-and-observability.md](operations-and-observability.md) R-OPS-015) | Until the next batch or deduplication window | Written by requests, never read by one; flushed at shutdown. A crash loses what they hold, normally under one second of events, at most `audit.async_buffer`. |
| Denial aggregation buffer of the CI exchange ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-021) | One minute | Requests write it and never read it; the first denial of an address and reason in a minute is written at once, so a crash loses at most one minute of counts. |
| De-duplication of `auth.csrf_failed` events ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-041) | One minute | Bounds log volume only; no answer depends on it. |
| Log ring buffer ([operations-and-observability.md](operations-and-observability.md) R-OPS-009) | The last `log.ring_buffer_records` records | Read only by the replica's own support-bundle part; no request's answer depends on it, and the records also go to the platform's log stack. |
| Cached values of the gauges read from PostgreSQL ([operations-and-observability.md](operations-and-observability.md) R-OPS-007, Q-OPS-18) | At most `metrics.db_gauge_interval` | A copy of PostgreSQL state served to scrapes only; an age gauge states how old it is. |
| Sent set of a delivery-log follow stream ([operations-and-observability.md](operations-and-observability.md) R-OPS-044, P1) | One request | Lives and ends with its stream; a reconnect on another replica continues from `Last-Event-ID` ([api-and-ui.md](api-and-ui.md) R-API-051). |
| The replica's support-bundle part being collected ([operations-and-observability.md](operations-and-observability.md) R-OPS-027, P1) | At most `support.collect_timeout` | Uploaded as a blob when done; abandoned at `SIGTERM`, which leaves the bundle `partial` (R-ARCH-001). |
| Signal coalescer queue (R-ARCH-018) | At most one coalescing window | Best-effort wake-ups that no request depends on. |
| Fallback rate-limit buckets (R-ARCH-034) | While Valkey is unavailable | The degraded mode INV-12 names. While Valkey is available, the counters are cluster-wide. |
| In-flight request counter (`server.max_in_flight`, R-ARCH-005); the memory slots and the heap gate of R-ARCH-005's memory guard, sized from the replica's own memory limit (Q-ARCH-240); upstream fetch slots, which only fetching requests take ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-040); Argon2id verification slots ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-014) | Concurrent requests | Protect the replica itself: its connections, its upstream share and its memory. A refused request succeeds on retry elsewhere. |
| The window, waits and in-flight background fetches of a mirror's pass, and the background slot counters of a repository ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-054, R-REPO-055, R-REPO-058) | One pass attempt, in the process that runs it | The pass's durable state (cursor, pending names, fence) is in PostgreSQL; a pass continued in another process starts its window at 1, and no request reads any of it. |
| Circuit breakers for the object store and for Valkey | Seconds | They describe this replica's own network path ([storage.md](storage.md) R-STOR-033, R-ARCH-031). |

- A protocol that spans requests keeps its state in PostgreSQL and its data in S3. Valkey may hold
  such state only where its loss costs nothing but one client retry; no protocol below uses it.
  The protocols, MVP unless marked:
  - browser login: the `oidc_login_flow` row and the binding cookie
    ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-006);
  - npm web login and device login polling (P1): the `cli_login` row (R-AUTH-034, R-AUTH-036);
  - UI sessions: the session row (R-AUTH-010);
  - paged lists: cursors that encode their position and never refer to replica memory
    ([api-and-ui.md](api-and-ui.md));
  - long-running operations: job rows (R-ARCH-016);
  - chunked and resumable uploads (P1): the `upload_sessions` and `upload_session_parts` rows and
    one S3 multipart upload per session ([storage.md](storage.md) R-STOR-011, Q-STOR-11).
- Neither trove nor the chart configures session affinity: Services keep `sessionAffinity: None`,
  and the edge route sets no affinity cookie ([ha-and-helm.md](ha-and-helm.md)).
- No volume is shared between pods (INV-13). A role that needed persistent local state would break
  this requirement first, so no role has any.

Acceptance criteria:
- In the e2e profile `scatter`, the edge balances every request round robin across three `web`
  replicas, with upstream keep-alive switched off. The client suite passes in that profile: npm
  publish and then install through a virtual repository, `dotnet nuget push` and then
  `dotnet restore`, the UI login, and a paged `GET /api/v1/…` list walked to its last page. From
  P1 the same profile also runs `npm login --auth-type=web` and the device login (R-AUTH-034,
  R-AUTH-036, [formats/npm.md](formats/npm.md) R-NPM-020).
- A test in `internal/archtest` fails on a package-level variable of map, slice, pointer or
  `sync.Map` type outside `internal/cache`, `internal/cluster`, `internal/observability` and the
  registries that are frozen at start-up: `internal/formats`, `internal/jobs`, `internal/errs`,
  `internal/secrets`, `internal/meta` (the revision-column registry, R-ARCH-009) and
  `internal/backup/tables` (the table registry) and `internal/audit` (the event registry, R-OPS-014).
  A package-level `*regexp.Regexp` is exempt; generated
  files, variables with a `//go:embed` directive and variables of interface type are not checked
  (Q-ARCH-19).

## Code layout

`internal/tlsfiles` is a leaf layer, like the table registry of `internal/backup/tables`: every layer may
use it, it imports only the standard library and the Prometheus client, and it never dials. The packages that present
or trust TLS material import it: `httpserver` (listeners), `meta/postgres`, `cluster` (Valkey), `outbound` (the trust
stores, which the object-store client uses), and `app` for its metric families. The OTLP trace exporter reads its CA file
directly, once ([H-69](../security/transport-encryption.md#h-69--tracingca_file-is-read-once-at-start)). The rules of
[R-ARCH-035](#r-arch-035--tls-on-troves-listeners-and-reloadable-tls-material) apply to every importer.

### R-ARCH-003 · Go module and package layout
Priority: **MVP** · Covers: trove-only

The repository is one Go module. `go.mod` states `go 1.27.3`, which is the minimum and also the
pin CI reads. Code generators are pinned once, and local builds and CI read the same pin:
- sqlc (Q-ARCH-2) and oapi-codegen are `tool` directives in `go.mod`.
- oapi-codegen is v2.8 or later, the first line with OpenAPI 3.1 support. It runs with the
  `models` and `std-http-server` outputs and writes `internal/api/v1/gen/`
  ([api-and-ui.md](api-and-ui.md) R-API-002, Q-API-1).
- openapi-typescript 7.x is pinned in `web/package-lock.json` and writes
  `web/src/app/api/generated/` (R-ARCH-004).

```text
trove/
├── cmd/trove/main.go          # subcommands serve, migrate, backup, admin; flag parsing only
├── api/openapi.yaml             # OpenAPI 3.1 source of truth, served as /api/v1/openapi.json (INV-6)
├── internal/
│   ├── app/                     # composition root: builds components from config, start/stop order
│   ├── buildinfo/               # version, commit, build date (-ldflags)
│   ├── config/                  # schema, YAML + env loading, validation
│   ├── httpserver/              # listeners, ServeMux, middleware, client address
│   │   └── content/             # the content-header wrapper of R-ARCH-007, shared with the format contract and the kit
│   ├── api/v1/                  # management API handlers
│   │   └── gen/                 # oapi-codegen output: models, std-http-server interface (generated, committed)
│   ├── api/rawfiles/            # the /files/ handler (R-API-025); content responses through api/edge
│   ├── api/dispatch/            # the format dispatcher: /api/<id>/<repoKey>/…, the error shape of a format prefix (Q-ARCH-214)
│   ├── formathost/              # the Host of the format contract, the hook and seam adapters, the data-port bindings (Q-ARCH-212)
│   ├── packages/                # the generic package rows: store and writer, rev_seq raised once per package per transaction (Q-ARCH-212)
│   ├── api/edge/                # what every entry point shares: Subject, Access, the content response, conditional and digest headers, links, list query, JSON (Q-ARCH-205)
│   ├── auth/{principal,oidc,session,token,ciexchange}/   # principal: the principal table; the others as named
│   ├── auth/scope/              # the token scope value type (leaf layer)
│   ├── auth/jws/                # compact JWS and JWK sets, shared by the login and the CI exchange (leaf layer, Q-CI-19)
│   ├── authz/
│   ├── repository/              # model, snapshot, CleanPath, classification, routing of virtual repositories (members, claims, pins)
│   ├── items/                   # the item store: spool, the one commit, stat, open, listing rows, subtree delete, statistics; no authz (Q-ARCH-207)
│   ├── files/                   # the generic layer: decides on the request's index, resolves and admits, over items (R-STOR-007 to R-STOR-018, Q-ARCH-206)
│   ├── formats/                 # contract (Format, Host), registry
│   │   ├── all/                 # imports and registers every format
│   │   ├── catalog/             # the format catalog: facts, name rule, exposure (leaf layer, Q-ARCH-188)
│   │   ├── untrusted/           # resource limits and bounded readers for untrusted content (R-ARCH-028; leaf layer, Q-VULN-23)
│   │   ├── formattest/          # conformance kit, in-memory Host (Q-ARCH-216)
│   │   │   ├── crafted/         # generators of the hostile inputs of check 6 (standard library only)
│   │   │   └── testformat/      # the kit's own test format and its defects
│   │   ├── generic/
│   │   ├── npm/
│   │   └── nuget/
│   ├── proxy/                   # remote fetch engine
│   │   ├── upstream/            # per-remote HTTP clients, credentials, hop policy
│   │   └── invalidation/        # invalidation set, fold task
│   ├── virtual/                 # per-caller resolution, merged-metadata cache, shadowing check (Q-ARCH-192)
│   ├── vuln/                    # vulnerability scanning: statuses, the repository member, the download verdict and the ports (R-VULN-001 to R-VULN-029)
│   │   ├── advisory/            # the normalized advisory records, severities, source attribution, audit ids (leaf layer)
│   │   ├── match/               # the matcher over a format's version order (leaf layer)
│   │   ├── osv/                 # the OSV and KEV decoders (leaf layer)
│   │   └── store/ sync/ evaluate/ triage/ report/   # the tables' wrappers, the sync jobs, evaluation and lifecycle, status changes, the report
│   ├── mirror/                  # the mirror mode of remote repositories: dispatch, passes, throttling, status (R-REPO-050 to R-REPO-059)
│   │   └── discover/            # the discovery contract a format's remote adapter implements (leaf layer, ADR-152)
│   ├── formatdata/              # store ports of the formats, implemented over the generated queries (Q-ARCH-193)
│   │   └── npm/
│   ├── blob/                    # BlobStore interface, spool, digests
│   │   ├── s3/
│   │   └── blobtest/            # conformance kit and in-memory store for tests
│   ├── meta/                    # Tx helper, shared types
│   │   └── postgres/            # pgx pools, transactions, migration runner (R-ARCH-026)
│   │       ├── migrations/      # NNNNN_name.sql, embedded
│   │       ├── queries/         # *.sql inputs for sqlc
│   │       ├── q/               # sqlc output (generated, committed)
│   │       └── listq/           # dynamic list/search queries: bound values, allowlisted identifiers (R-ARCH-019)
│   ├── cluster/                 # Valkey client and key registry, change log and signal areas, FlightLock,
│   │                            # shared counters, leases, lock categories (R-ARCH-017, R-ARCH-018, R-ARCH-030 to R-ARCH-034)
│   ├── cache/
│   ├── jobs/
│   ├── audit/
│   ├── backup/                  # trove backup: create, restore (backup-and-restore.md)
│   │   └── tables/              # table registry: the export class of every table (leaf layer)
│   ├── outbound/
│   ├── secrets/
│   ├── tlsfiles/                # TLS material read from PEM files and reloaded by content (leaf layer, R-ARCH-035)
│   ├── errs/
│   ├── observability/
│   ├── webui/                   # go:embed all:dist; dist/ is filled by the web build
│   ├── manual/                  # the manual of the help area: contents, rendering, link check (R-API-054)
│   ├── tools/coverage/          # requirement coverage generator (docs/coverage, Q-ROAD-4)
│   └── archtest/
├── docs/manual/                 # the manual's Markdown and embed.go, which embeds it (R-API-054, Q-UI-89)
├── web/                         # Angular workspace (R-ARCH-004)
├── deploy/helm/trove/         # Helm chart (INV-13)
├── test/
│   ├── integration/             # real PostgreSQL, PgBouncer, Valkey with Sentinel and SeaweedFS (S3) in containers (R-HA-051)
│   └── e2e/                     # kind + Helm + real clients (npm, pnpm, yarn, dotnet)
└── perf/                        # load scripts, reference lockfiles (R-ARCH-027)
```

- All Go code lives under `internal/`, so trove exposes no public Go API. A new format is a new
  package in this repository (R-ARCH-010).
- There is one package per bounded context. `util`, `common` and `helpers` packages are not
  allowed.
- Generated code carries the `// Code generated … DO NOT EDIT.` header and is committed. CI runs
  `go generate ./... && git diff --exit-code`.
- `internal/webui/dist/` holds a committed placeholder `index.html`, so `go build` works without a
  UI build. Release builds run the web build first. Built locale directories `dist/<locale>/` are
  not committed; `internal/webui/.gitignore` ignores them (Q-ARCH-20).

Acceptance criteria:
- `go build ./cmd/trove` succeeds on a clean checkout without Node.js installed.
- `go generate ./... && git diff --exit-code` passes in CI.

### R-ARCH-004 · Angular workspace layout
Priority: **MVP** · Covers: trove-only

The technology baseline (standalone components, zoneless, `OnPush`, `httpResource()`, Signal Forms,
CSP nonce, localized builds under `/ui/{locale}/`) and state management are specified in
[api-and-ui.md](api-and-ui.md) (R-UI-001, R-UI-046). This requirement fixes where the code lives
and how the build reaches the binary.

```text
web/
├── angular.json                 # i18n locales; localized production build
├── package.json                 # exact versions (Angular 22.x, INV-1); package-lock.json committed
├── .nvmrc                       # the one Node.js version used locally and in CI
├── tsconfig.json
└── src/
    ├── index.html               # root element carries ngCspNonce (filled per response by webui)
    ├── main.ts                  # bootstrapApplication(App, appConfig)
    ├── locale/                  # translation files
    ├── styles/                  # design tokens, light and dark theme
    └── app/
        ├── app.ts               # root standalone component: shell, navigation, banner, outlet
        ├── app.config.ts        # router, HttpClient, interceptors, error handler
        ├── app.routes.ts        # lazy routes per area
        ├── api/                 # thin hand-written clients per resource family
        │   └── generated/       # openapi-typescript output (generated, committed)
        ├── core/                # SessionStore, interceptors (CSRF, problem mapping, 401), layout
        ├── shared/              # presentational components, pipes, directives
        └── features/
            ├── packages/  browse/  repositories/  setup/  tokens/
            ├── formats/<id>/    # optional format-specific panels, registered by format ID
            └── admin/           # users, groups, service accounts, policies, CI identities, system
```

- Generated API types live only in `src/app/api/generated/`, and nothing in that folder is edited
  by hand ([api-and-ui.md](api-and-ui.md) Q-API-1).
- The repository settings form is rendered from the format's settings JSON Schema (R-ARCH-008). A
  format adds a panel under `features/formats/<id>/` only for interactions a schema cannot express.
- The production build writes one directory per locale. The release build copies them to
  `internal/webui/dist/<locale>/`.
- `internal/webui` serves them under `/ui/{locale}/`. It injects the per-response CSP nonce into
  `index.html` and applies the cache and fallback rules of R-UI-001.

Acceptance criteria:
- `grep -rE "NgModule\(|provideZoneChangeDetection|zone\.js" web/src web/angular.json` finds
  nothing.
- After `npm ci && npx ng build` in `web/` and a release `go build`, `curl -s localhost:8080/ui/en/admin/jobs`
  returns `index.html` with a nonce, and two requests carry different nonces.

## HTTP edge

### R-ARCH-005 · HTTP listeners, routing and middleware chain
Priority: **MVP** · Covers: HA-012, HA-020, HA-021, HA-024, REPO-005

Listeners (INV-6; Q-ARCH-11 keeps the layout and the ports as they are):
- The client listener (`server.listen`) serves everything clients use. The ops listener
  (`server.ops_listen`) serves only `/healthz`, `/readyz` and `/metrics`
  ([operations-and-observability.md](operations-and-observability.md)). Neither serves the other's
  paths.
- **Several client listeners.** `server.listen` is the listener of the default endpoint. Every
  further endpoint of `server.endpoints` has a client listener of its own, with its own ServeMux
  built from the endpoint's surface, its own in-flight limit and its own instance of the middleware
  chain below ([endpoints.md](endpoints.md) R-EP-001, R-EP-002, R-EP-005). The endpoint of a
  request is the listener's and is placed in the request context at step 1; it is never derived
  from request data (Q-EP-1). A `web` process opens the client listeners of the endpoints of its
  pool, a `worker` process none.
- An address without a host binds every interface on IPv4 and IPv6 (HA-020).
- HTTP/1.1 is always on. Both listeners serve TLS when `server.tls` is set, and HTTP/2 is served over
  TLS (R-ARCH-035). A reverse proxy or Ingress in front may end the client's TLS and connect to trove
  over TLS again, which the chart wires (R-HA-063). TLS at the edge alone is no longer the design: plain
  HTTP to trove is a chart choice, `tls.enabled: false` (H-38).

Top-level routes on the client listener (`net/http.ServeMux`, Q-ARCH-1). The `/api/v1/` operations
are registered on the same ServeMux by the oapi-codegen `std-http-server` interface, and the
authorization declarations are looked up by the matched `http.Request.Pattern`
([api-and-ui.md](api-and-ui.md) R-API-002):

| Pattern | Handler |
|---|---|
| `/api/v1/` | Management API ([api-and-ui.md](api-and-ui.md)) |
| `/api/<id>/{repoKey}/{rest...}` (and the bare `/api/<id>/{repoKey}`), registered once per format ID whose `Routes()` is not empty | Format dispatcher (R-ARCH-008; `internal/api/dispatch`). generic has no routes, its protocol is `/files/`, so it registers no prefix (Q-GENERIC-7) |
| `/files/{repoKey}/{path...}` | Raw file access ([storage.md](storage.md)) |
| `/auth/` | Browser login flow ([identity-and-authentication.md](identity-and-authentication.md)) |
| `/ui/` | Embedded UI (R-ARCH-004) |
| `POST /-/npm/v1/oidc/token/exchange/package/{name...}` | npm plug-in: trusted-publishing token exchange at the host root ([formats/npm.md](formats/npm.md) R-NPM-022, P1; Q-NPM-1) |
| `/v2/` | Reserved for the OCI format ([ha-and-helm.md](ha-and-helm.md) R-HA-041, Q-HA-4). `404` until the OCI format exists: no pattern is registered here yet, so `/v2/` and every path below it fall through to the last row. No other pattern may start with `/v2/` (route-table test below). |
| `/.well-known/` | Reserved for discovery documents at the host root, the first being Terraform's on a host bound to one repository (P1; [endpoints.md](endpoints.md) R-EP-006). `404` until a format registers one; no other pattern may start with `/.well-known/` (route-table test below). |
| `GET /{$}` | `302` to `/ui/`; only on an endpoint whose surface holds `ui` ([endpoints.md](endpoints.md) R-EP-005) |
| anything else | `404` problem (R-ARCH-023) |

- Format IDs match `^[a-z][a-z0-9-]{1,31}$` and MUST NOT match `^v[0-9]+$`, which is reserved for API
  versions. Registering literal format segments keeps `/api/v1/` and the format routes free of
  ServeMux conflicts. A conflicting registration panics at start-up, so a collision cannot ship.
- A host-root route exists only where a client fixes the path and cannot be configured to use
  `/api/<format>/<repoKey>/`. The format's owning page adds it to INV-6 and registers it as a literal
  ServeMux pattern in the table above. It never carries a repository key and never changes the
  `/api/<format>/<repoKey>/` layout (Q-ARCH-11). The decided host-root routes:
  - **npm** ([formats/npm.md](formats/npm.md) Q-NPM-1). The only host-root route is
    `POST /-/npm/v1/oidc/token/exchange/package/{name...}`. npm builds the exchange URL against the
    host root of the registry URL. trove owns that host root, because `server.base_url` is an origin
    without a path ([operations-and-observability.md](operations-and-observability.md) R-OPS-005).
    - The registry-relative form of the same exchange,
      `/api/npm/<repoKey>/-/npm/v1/oidc/token/exchange/package/{name}`, used by Yarn 4 and current
      pnpm, is an ordinary npm route behind the format dispatcher. Both entry points run one handler.
    - Like the dispatcher, the handler takes the name from `r.URL.EscapedPath()`, so `@scope%2fname`
      stays one name.
  - Any other path below `/-/` has no route and falls through to the `404` problem. That includes
    npm's attestation path `/-/npm/v1/attestations/…`. Attestations are served later below the
    registry URL ([formats/npm.md](formats/npm.md) R-NPM-027).
  - **OCI** ([ha-and-helm.md](ha-and-helm.md) Q-HA-4, R-HA-041). `/v2/` and everything below it
    belong to the OCI distribution API, with the repository key as the first path segment after
    `/v2/`. The reservation is MVP; the OCI format itself is P1. Until it exists, nothing is
    registered under `/v2/`, and those paths answer the `404` problem.
    - A route-table test in `internal/httpserver` fails when any registered client-listener pattern
      other than the OCI format's has a path that is `/v2` or starts with `/v2/`, whatever its
      method or host prefix (`GET /v2/x`, `/v2/{name...}`). It also fails when a pattern other than
      the catch-all for "anything else" and `GET /{$}` has a wildcard as its first path segment
      (`/{seg}/…`), because such a pattern matches below `/v2/` as well.
    - ServeMux's start-up conflict check does not replace this test. A more specific pattern such as
      `/v2/x` does not conflict with `/v2/`, so it would register without a panic and take those
      paths, before or after the OCI format exists.
    - The test reads the patterns from the one wrapper through which `internal/httpserver` registers
      every client-listener route, the generated `/api/v1/` operations and the format plug-ins'
      host-root routes included. A route registered on the ServeMux around that wrapper is a defect.
  - No host-root route is served on the content host (`server.content_base_url`, R-ARCH-029).
  - The route-table test applies the same rule to `/.well-known/` and runs over the ServeMux of
    every endpoint ([endpoints.md](endpoints.md) R-EP-002, R-EP-006).
- **Error shape of a format prefix.** Every error response below `/api/<id>/` is rendered by the
  format's `WriteError`: the handler's own errors, and the refusals of the chain's stages above
  (`401` with its challenge from step 6, `421` from step 5, `503 server_busy` from step 4, `429`
  from step 7), which the chain renders through `httpserver.Problem`. The dispatcher registers the
  format's shape for the prefix with `Router.HandleErrorShape`, and `Problem` consults that table by
  path prefix (Q-ARCH-214). The `/api/v1/` paths keep `problem+json`.
- The dispatcher hands the format the remaining path exactly as received: `r.URL.EscapedPath()`
  minus `/api/<id>/<repoKey>/`. It never hands over the unescaped wildcard value. This keeps an
  encoded slash in a package name (`@scope%2fname`) apart from a separator. Repository keys contain
  nothing that needs escaping (INV-5), so the prefix looks the same escaped and unescaped.
- Every repository-relative path passes `repository.CleanPath` before authorization runs.
  `CleanPath` decodes percent-escapes exactly once and answers `400` with code `invalid_path` when a
  segment is `.`, `..` or empty, or contains NUL, `\` or a control character
  ([authorization.md](authorization.md)). ServeMux's own cleaning does not cover escaped dots
  (`%2e%2e`), so trove does not rely on it.

Middleware, outermost first:
1. Panic recovery: `500` with code `internal`, and the stack goes to the log.
2. Trace context and request ID ([operations-and-observability.md](operations-and-observability.md)).
   Every response carries `X-Request-Id`.
3. Request log and metrics. The route label is the ServeMux pattern or the format route name
   (R-ARCH-008), never the raw path. The dispatcher sets it to `Route.Name` as soon as the format's
   `Match` has resolved the route.
4. In-flight limit (`server.max_in_flight` on the default endpoint, the endpoint's `max_in_flight`
   on a further endpoint; client listeners only), then the replica's memory guard below, which
   every endpoint of the replica shares: `503` with `Retry-After: 1` and code `server_busy`.
5. Client address (R-ARCH-006), the endpoint's host check with `421` for a foreign host, the
   base URL of the response and the host-root binding
   ([operations-and-observability.md](operations-and-observability.md) R-OPS-005,
   [endpoints.md](endpoints.md) R-EP-003, R-EP-006).
6. Authentication: a principal or anonymous. Credentials that are present but invalid always give
   `401` ([identity-and-authentication.md](identity-and-authentication.md)). The one exception is a
   `POST` to an exchange endpoint, at the host root or below a registry URL. There the
   `Authorization` header carries an ID token, the stage does not evaluate it, and the request
   reaches the handler unauthenticated
   ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-001). Whether a
   package request reaches a route that declares the API-key header (`Route.APIKey`, R-ARCH-008)
   the stage learns from the format dispatcher's own match, before the dispatcher runs
   ([identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-186).
7. The endpoint's rate-limit class, on every route of a further endpoint
   ([endpoints.md](endpoints.md) R-EP-015), then the rate limits on `/api/v1/`, `/files/`, the
   package protocols under `/api/<format>/` and `/auth/` ([api-and-ui.md](api-and-ui.md)). Their
   counters are cluster-wide in Valkey wherever the owning page gives a class cluster scope, and
   fall back to per-replica buckets while Valkey is unavailable (R-ARCH-034). The check never waits
   longer than `valkey.command_timeout`.
8. The handler. Authorization runs inside the handler, as soon as the repository and the path or
   package are known ([authorization.md](authorization.md)).

Memory guard of step 4 ([Q-ARCH-240](#q-arch-240--a-replica-admits-what-its-memory-holds-memory-slots-and-a-heap-gate-in-step-4)).
A replica under overload answers the excess with `503` and is never killed for it. The concurrency
a replica admits is bounded by its memory as well as by `max_in_flight`:
- **The limit.** The guard works against the Go memory limit `L` of
  [ha-and-helm.md](ha-and-helm.md) R-HA-036: `runtime.memory_limit_ratio` ×
  `runtime.memory_limit_bytes`, or `GOMEMLIMIT` when it is set. Without a known limit there is no
  guard, only `max_in_flight`, and the replica logs a warning at start.
- **Memory slots** ([Q-ARCH-328](#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets)).
  The guard counts bytes. A request is charged the request bytes at step 4 and holds them to its
  end, and the replica admits a request while the charges plus the bytes its caches hold now stay
  below 0.8 × `L`; a request that is alone is always admitted. The request bytes are what a
  download of a large object holds (Q-ARCH-241): the buffer of a verified download
  (`storage.download.buffer_size`, at least twice `storage.download.hold_back` and 4 KiB, rounded up
  to a power of two), the object store's receive window `storage.s3.http.receive_window` and 128 KiB
  for the rest (connection, TLS state, stacks, the object-store call), 640 KiB with the defaults;
  100 downloads of 1, 2 and 5 MiB held open measured 636 to 659 KiB each.
  - **A download settles.** The blob store knows the size of the object before it asks the object
    store (`blob.Store.Open`), and tells the request's charge what the download will hold: the
    buffer class of that size plus the bytes the store can send ahead, at most the window
    (`blob.DownloadCost`; a ranged read by its length, `blob.RangeCost`). The charge falls to that
    and the 128 KiB: a 50 KiB tarball holds about 242 KiB, not 640 KiB. The charge is of the
    readers open now ([Q-ARCH-330](#q-arch-330--the-charge-of-a-request-is-the-overhead-and-the-readers-it-has-open-now)):
    closing a reader takes its bytes off again, so objects read one after the other (the ranged
    reads of a NuGet package's README, the pages of a registration index) never pass the admission
    charge, and only readers open together add. A request refuses nothing half way, and one that
    has ended, or a fetch that outlives it, settles nothing of its ticket.
  - **The caches count by what they hold.** The bytes of the replica's caches (the registry's
    weighted sizes: the rendition cache, the two metadata caches, the pass-through cache and the
    smaller ones), read with every heap sample, are subtracted from 0.8 × `L`, not their byte
    budgets (`cache.rendition_bytes`, `proxy.metadata_cache_bytes`,
    `proxy.merged_metadata_cache_bytes`, `proxy.query_cache_bytes`: 648 MiB with the defaults,
    Q-ARCH-243, Q-ARCH-270). A cache that is full takes what its budget says; one that is empty
    takes nothing. The growth of a cache after a request was admitted is the heap gate's (H-539).
  - **Slots.** `trove_http_memory_slots` is (0.8 × `L` − the caches' bytes) ÷ the request bytes,
    at least one, in downloads of a large object. With empty caches a 1 GiB container (`L` =
    870 MiB) has 1,114 slots, 256 MiB has 278, and the chart's 2 GiB 2,228; with the caches full
    (648 MiB) 1 GiB has 77 and 2 GiB 1,191, which is what the chart checks (R-HA-029, Q-HA-70).
    `trove_http_memory_slots_in_use` is the charges in the same unit, rounded up.
  The charges bound a burst that arrives faster than a GC cycle can show it. An endpoint admits
  the smaller of its `max_in_flight` and what the replica has left.
- **Heap gate.** Every 50 ms the guard reads the live heap of the last GC cycle plus the goroutine
  stacks (`runtime/metrics`). At 80 % of `L` it admits no request until they fall below 70 %. The
  gate bounds requests that hold more than a download, such as a document held in memory
  (R-ARCH-028). A closed gate that sees no GC cycle for 1 s runs one, so it opens once the load is
  gone ([Q-ARCH-242](#q-arch-242--a-closed-heap-gate-runs-a-gc-cycle-when-none-came-for-a-second)).
- **Visibility.** Every refusal of step 4 counts in `trove_http_requests_shed_total{endpoint,reason}`
  (`in_flight`, `memory_slots`, `heap`); `trove_http_memory_slots` is the number of slots in downloads of a large object
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-008). The gate logs when it
  closes and when it opens.
- Not bounded by the guard: connections that send no request (H-324), the growth of a cache after a request was admitted (H-539), memory a replica retains
  beyond the high-water mark (H-323), and slots that slow requests hold (H-322).

Transfer rules:
- A handler MUST finish authentication and the coarse authorization of its route (R-ARCH-008)
  before it reads the request body. Go sends `100 Continue` only on the first body read, so a
  rejected upload never transfers its body.
- `http.Server.ReadTimeout` and `WriteTimeout` stay at 0, because they would cut long transfers.
  Requests are bounded by `server.read_header_timeout` and by a stall timeout: after every
  successful chunk, the handler moves the read and write deadlines forward by
  `server.transfer_stall_timeout` through `http.ResponseController` (HA-024).
- Bodies are streamed. Only documents a format declares as bounded are held in memory
  (R-ARCH-028). Upload size limits belong to [storage.md](storage.md), and JSON body limits on
  `/api/v1/` to [api-and-ui.md](api-and-ui.md).

```yaml
server:
  listen: ":8080"                    # default (proposed), INV-6
  ops_listen: ":8081"                # default (proposed), INV-6
  read_header_timeout: 10s           # default (proposed)
  idle_timeout: 120s                 # default (proposed)
  transfer_stall_timeout: 60s        # default (proposed)
  max_header_bytes: 65536            # default (proposed)
  max_in_flight: 4096                # default (proposed), per replica
  shutdown_timeout: 25s              # default (proposed) outside the chart; the chart derives it from the grace period (R-ARCH-024)
  tls:
    cert_file: ""                    # default (proposed): empty = plain HTTP
    key_file: ""                     # default (proposed)
```

Acceptance criteria:
- `curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/healthz` prints `404`. The same
  request on `:8081` prints `200`.
- `curl -s http://localhost:8080/api/npm/npm-all/@types%2fnode | jq -r .name` prints `@types/node`.
  A request to `…/api/npm/npm-all/%2e%2e/x` gets `400` with code `invalid_path`.
- `curl -T 5GiB.bin --limit-rate 5M` to a generic repository (P1) completes. A client that stops
  sending for 61 s has its connection closed.
- `curl -v -T pkg.tgz -H 'Expect: 100-continue'` without credentials, against a repository that
  needs authentication, gets `401` and no `100 Continue`.
- `curl -g -s http://[::1]:8080/ui/` answers.
- `GET /-/npm/v1/attestations/left-pad@1.3.0` and `GET /-/whoami` at the host root answer `404`
  with a problem body. From P1, `POST /-/npm/v1/oidc/token/exchange/package/@acme%2Fui` with a
  GitLab ID token as `Bearer` reaches the npm exchange handler instead of being rejected with `401`
  by the authentication step.
- Until the OCI format exists, `GET /v2/` and `GET /v2/docker-all/manifests/latest` answer `404`
  with a problem body. The route-table test fails on a fixture plug-in that registers
  `GET /v2/x` as a host-root route, and passes again once the fixture is removed.
- With a further endpoint configured (R-EP-001), `:8080` and the endpoint's port each answer with
  their own route families, and a request whose host is not one of the further endpoint's hosts
  answers `421` there ([endpoints.md](endpoints.md) R-EP-002, R-EP-003, R-EP-005).
- A replica with `runtime.memory_limit_bytes` of 256 MiB and 1,000 concurrent downloads of a 1 MiB
  file whose clients do not read: at most 278 complete with their bytes once the clients read, every
  other one answers `503` `server_busy` with `Retry-After: 1`, the memory the Go runtime holds stays
  below 256 MiB, and the process does not exit. Afterwards a download answers `200` again
  (`test/integration/overload`, `TestMemoryAdmission`).
- A replica with `runtime.memory_limit_bytes` of 1 GiB and the default caches, still empty, and 300
  concurrent downloads of a 1 MiB file whose clients do not read: none is shed, although the
  caches' budgets (648 MiB) would have left 77 slots under Q-ARCH-240, and every download completes
  with its bytes (`TestBurstBesideEmptyCaches`).

### R-ARCH-006 · Client address and trusted proxies
Priority: **MVP** · Covers: HA-025

Several pages rely on this mechanism: rate limits, audit, authorization conditions, and token
exchange limits.
- The client address is the TCP peer, unless the peer lies in the trusted proxies of the request's
  endpoint (`server.trusted_proxies`, or a further endpoint's `trusted_proxies`,
  [endpoints.md](endpoints.md) R-EP-001). In that case it is the right-most address of the one header
  family that `server.forwarded_header` selects, `x-forwarded-for` (the default, `X-Forwarded-For`) or
  `forwarded` (the `for=` parameters of [RFC 7239](https://www.rfc-editor.org/rfc/rfc7239)), that is not
  itself a trusted proxy. The other family is never read, not even as a fallback
  ([Q-ARCH-35](#q-arch-35--which-forwarded-header-family-does-the-edge-rewrite)). An entry that is not an
  address stops the walk, and the peer stays the client. A prefix of length 0 is refused.
- Forwarded scheme and host are read from the same family, and evaluated only under the rules of the public base URL. Those rules,
  the `server.base_url`, `server.trusted_proxies` and `server.additional_base_urls` keys, and their
  defaults are owned by [operations-and-observability.md](operations-and-observability.md). On every
  endpoint a forwarded host counts only when it lies in that endpoint's host set; it never selects
  the endpoint ([endpoints.md](endpoints.md) R-EP-003).
- The resolved client address and base URL are placed in the request context once, by middleware
  step 5 of R-ARCH-005. No handler, format or job reads `Host`, `X-Forwarded-*` or `Forwarded`
  itself.
- Rendered metadata that contains links is cached under a key that includes the base URL and the
  endpoint (R-ARCH-018, [endpoints.md](endpoints.md) R-EP-018), so one host name never sees
  another's links and one endpoint never another's exposure.

Acceptance criteria:
- `grep -rnE 'X-Forwarded|\.Host\b|"Forwarded"' internal --include=*.go` matches only in
  `internal/httpserver` and in tests.
- With `trusted_proxies: ["10.42.0.9"]` (the edge's address, example), a request from `10.42.0.9` carrying
  `X-Forwarded-For: 198.51.100.4` is logged with client address `198.51.100.4`. The same header from
  `203.0.113.5` is logged as `203.0.113.5`.
- Spoof case: with `forwarded_header: x-forwarded-for`, a request from the trusted `10.42.0.9` carrying
  `X-Forwarded-For: 203.0.113.5` and `Forwarded: for=198.51.100.9` is logged as `203.0.113.5`; with
  `forwarded_header: forwarded` the same request is logged as `198.51.100.9`. The unselected family
  never changes the answer.

### R-ARCH-007 · One origin for UI and package content
Priority: **MVP** · Covers: trove-only

In the MVP, the UI, the management API and the package endpoints share one origin: the host name
and the listener of the default endpoint (INV-6). A further endpoint never serves the UI or the
sign-in flow ([endpoints.md](endpoints.md) R-EP-005), so it is an origin without session cookies;
the content headers below apply on every endpoint. [formats/nuget.md](formats/nuget.md) handed this
decision to this page.
An optional separate content origin is P1 (R-ARCH-029, Q-ARCH-10). With it unset, which remains the
default in P1, everything below stays the complete defence.

Consequences, normative for every component:
- Every response whose body comes from package content or package metadata carries the content
  headers:
  - `X-Content-Type-Options: nosniff`;
  - `Content-Security-Policy: sandbox; default-src 'none'`;
  - `Cross-Origin-Resource-Policy: same-site`, and `same-origin` when the session cookie
    authenticated the request (Q-ARCH-86);
  - for files, `Content-Disposition: attachment` with the file name ([storage.md](storage.md)).

  A route of kind `Metadata` or `Computed` (a package document, a service index) carries the same
  set without `Content-Disposition`: a client reads it as a response and does not save it as a file.
  This covers `/files/` and every format route of kind `Download`, `Metadata`, `Computed` or `Volatile`
  (R-ARCH-008). It also covers uploaded and cached README, license, icon and manifest files.
  One exception: the HTML directory index of a folder `GET` under `/files/` is rendered by trove,
  not stored content. For it the wrapper selects the header set of
  [storage.md](storage.md) R-STOR-017 (`nosniff`, `Cross-Origin-Resource-Policy: same-site`, the
  CSP without `sandbox`, no `Content-Disposition`); the handler cannot weaken that set either.
- **The core sets the headers, not the handler.** The format dispatcher and the `/files/` handler
  wrap the `http.ResponseWriter` of every route of those kinds before the handler runs.
  - The wrapper writes the headers and checks them again at `WriteHeader`, so a handler can change
    `Content-Type` but cannot remove or weaken them.
  - A `Volatile` route (a search) may return package-derived bytes: titles, descriptions, icon URLs. The
    wrapper puts the headers of `Metadata` on it, without `Content-Disposition`, whatever the handler does
    (Q-ARCH-258, ADR-137). A route of another kind (`Upload`, `Delete`, `Admin`) that returns package-derived
    bytes is a contract violation, which the conformance kit detects (R-ARCH-010 check 7), and so is a
    `Volatile` route that serves a stored item.
- The UI never embeds package-provided HTML. It renders README content only as sanitized output of
  its own Markdown renderer ([api-and-ui.md](api-and-ui.md)).
- **Content links from one place.** Every absolute URL to package content that a response hands to
  a browser comes from `LinkBuilder` (R-ARCH-009) or from a `downloadUrl` field of the API. Examples
  are `downloadUrl`, README images generated by the renderer, and file links in directory indexes.
  The UI never assembles such a URL from strings. This is the seam R-ARCH-029 uses to move these
  links to the content origin without touching formats or the UI.
- The conformance kit (R-ARCH-010) fails a format whose content routes lack these headers.

Acceptance criteria:
- An `evil.svg` with an embedded script, stored in a generic repository and opened from `/files/`
  in a browser, does not run the script. The response carries all four headers.
- An npm package document and a NuGet `.nuspec` response both carry `nosniff`, the sandbox CSP and
  `Cross-Origin-Resource-Policy: same-site`.
- A test format whose download handler calls `w.Header().Del("Content-Security-Policy")` before
  writing still sends the header.
- A web unit test fails on any string literal in `web/src` that starts with `/files/` or with
  `/api/<id>/`, where `<id>` is a registered format ID. Exempt are the `files` client in
  `web/src/app/api/`, which performs the UI upload (`PUT /files/…`,
  [api-and-ui.md](api-and-ui.md) R-UI-012) against the UI origin, and the generated code in
  `web/src/app/api/generated/`.

### R-ARCH-029 · Optional separate content origin
Priority: **P1** · Covers: trove-only

With `server.content_base_url` set, package content that a browser could render is served from a
second origin. That origin never sees the UI's session cookie and is not the UI's origin. The header
defence of R-ARCH-007 stays in force on both origins. The content origin adds a second, independent
layer (Q-ARCH-10).

Origin rules, checked at start-up. On a violation the process exits and names the rule:
- `server.content_base_url` is an `https` origin with no path.
- Its host name differs from the host name of `server.base_url`, of every
  `server.additional_base_urls` entry
  ([operations-and-observability.md](operations-and-observability.md)) and of every endpoint host
  ([endpoints.md](endpoints.md) R-EP-001). A different port on the same
  host is refused: browsers do not isolate cookies by port, so such an origin would receive the
  session cookie.
- Recommended form: a separate registrable domain, such as `https://trove-content.example.net`.
  - A subdomain of the UI's registrable domain is accepted. Every trove cookie carries the
    `__Host-` prefix ([identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-2), so
    it is host-only: the content host neither receives `__Host-trv_session` or `__Host-trv_csrf` nor
    can it plant or shadow them.
  - Such a host is still same-site with the UI, so `SameSite` and `Sec-Fetch-Site` do not mark its
    requests as foreign. Start-up therefore logs a warning that names the registrable domain. The
    registrable domain is determined with the Public Suffix List (`golang.org/x/net/publicsuffix`).

Routing on the client listener. The host is resolved by R-ARCH-006, never read from raw headers.
The content host counts as a known host under the base-URL rules of
[operations-and-observability.md](operations-and-observability.md):
- On the content host, only `GET` and `HEAD` of `/files/…` and of format routes of kind `Download`,
  `Metadata` or `Computed` are served. Every other path answers `404`, including `/api/v1/`,
  `/auth/`, `/ui/` and the host-root routes of R-ARCH-005, such as npm's token exchange.
- On the UI host, a `GET` of those routes is answered `303 See Other` to the same path on the
  content host when either of these holds, whatever other credential the request carries:
  - it carries the session cookie;
  - it carries `Sec-Fetch-Dest` with a value other than `empty`, which marks a browser navigation
    or subresource load.

  A request that matches neither is served on the UI host as before. That covers every package
  client and script (npm, pnpm, Yarn, dotnet, nuget.exe, curl), which send no session cookie and no
  `Sec-Fetch-Dest`, so they are not affected.

Authentication on the content host:
- The session cookie is never accepted there. Bearer and Basic tokens are accepted as on the UI
  host, and anonymous access follows the repository's grants.
- A redirect for a session-cookie request carries a signed, short-lived grant in the query:
  `trv_exp=<unix seconds>&trv_sid=<session row id>&trv_sig=v1.<key id>.<base64url HMAC-SHA-256>`.
  - The MAC covers the content host, the escaped path, `trv_exp` and `trv_sid`.
  - The MAC key is derived with HKDF-SHA-256 from the stored-secret key `<key id>` of R-ARCH-021,
    with info `trove content-url v1`. A key rotation therefore keeps outstanding URLs valid until
    they expire.
- At use, the content host:
  1. verifies the MAC and `trv_exp`;
  2. loads the session, which must still be valid;
  3. authorizes `read` for the session's user on the path, exactly as on the UI host. A grant
     removed after signing takes effect at once.
- Content responses on the content host additionally carry `Referrer-Policy: no-referrer`, so a
  signed URL does not leak through a `Referer` header. They carry
  `Cross-Origin-Resource-Policy: cross-origin` instead of `same-site`, because they hold no ambient
  credential and the UI must be able to load README images from them.

Further rules:
- The content headers of R-ARCH-007 are unchanged, `Content-Disposition: attachment` included. This
  requirement does not render active content inline. Inline browsing (ART-045, P2) builds on this
  origin and is specified with it.
- The UI does not read package content with `fetch` or XHR. Package content reaches the browser only
  as a navigation or an `img` load, so no CORS configuration is needed on the content host.
- The chart renders the second host name in the Ingress or HTTPRoute, routed to the same Service,
  with its own TLS certificate ([ha-and-helm.md](ha-and-helm.md)). The UI's CSP adds the content
  origin to `img-src` when it is set ([api-and-ui.md](api-and-ui.md) R-UI-001).

```yaml
server:
  content_base_url: ""            # default (proposed): empty = one origin (R-ARCH-007)
  # content_base_url: "https://trove-content.example.net"   # example
  content_url_ttl: 300s           # default (proposed): lifetime of a signed content URL
```

Acceptance criteria:
- Set-up: `content_base_url: https://trove-content.example.net`, and a browser logged in to
  `https://trove.example.com`.
  - Opening `https://trove.example.com/files/generic-local/evil.svg` ends on the content host with
    `trv_sig` in the URL, and the response carries the content headers.
  - The Playwright request log shows no request to the content host carrying
    `__Host-trv_session` or `__Host-trv_csrf`, and the browser's cookie store holds no cookie for
    the content host.
- `curl -H "Authorization: Bearer $T" https://trove.example.com/files/generic-local/x.bin`
  answers `200` without a redirect. `npm ci` and `dotnet restore` against the UI host make no
  request to the content host.
- A signed URL is a credential, so a failed check never falls back to anonymous access
  (R-ARCH-005 step 6):
  - used after `content_url_ttl`, it answers `401` with code `content_url_expired`;
  - used with a wrong MAC, or after the session ended (logout), it answers `401` with code
    `content_url_invalid`;
  - used after the user's `read` grant is removed, it answers `404`
    ([authorization.md](authorization.md) R-AUTHZ-016).
- `GET https://trove-content.example.net/api/v1/repositories` answers `404`.
- `content_base_url: https://trove.example.com:8443` with `base_url: https://trove.example.com`
  stops start-up with `content_base_url must use a host name other than base_url`.
- `content_base_url: https://content.trove.example.com` starts and logs a warning that the content
  host shares the registrable domain `example.com` with the UI.

## Format plug-in contract

### R-ARCH-008 · Format plug-in contract
Priority: **MVP** · Covers: FMT-001, FMT-006, FMT-008, FMT-011, REPO-002, REPO-006

Each package format is a Go package `internal/formats/<id>` that implements `formats.Format`.

A format owns:
- its route table and path grammar;
- how each path behaves in caches;
- package identity and namespaced properties;
- the storage hooks [storage.md](storage.md) needs;
- upstream request mapping and metadata rewriting;
- merging across virtual members;
- client setup templates and its guide (api-and-ui.md R-UI-049);
- its native error shape.

The core owns authentication, authorization, the upload and download pipeline, freshness, negative
caching, collapsing misses, offline state, audit, jobs and metrics. Formats reach the core only
through `Host` (R-ARCH-009). They MUST NOT open outbound connections, run SQL or touch the object
store.

```go
package formats

// ContractVersion changes whenever a method below changes meaning or leaves (R-ARCH-025). The
// contract is frozen at 1 since the end of M3 (E3.3, ADR-141): every change of it is an ADR, an
// addition keeps the version, and a breaking change raises it and moves every format in the same
// change. A format declares the literal it was written against.
const ContractVersion = 1

// The types of other packages under the names a format spells them: one type each, no mirrors
// (Q-ARCH-188, Q-ARCH-189). A format imports this package and nothing else of the core.
type (
	ID     = catalog.ID         // "npm", "nuget"; ^[a-z][a-z0-9-]{1,31}$, never ^v[0-9]+$
	Class  = catalog.Class      // "local", "remote", "virtual": a string, as stored and served
	Action = authz.Action       // consts ActionRead, ActionAnnotate, ActionDeploy, ActionOverwrite, ActionDelete, ActionManage
	Path   = repository.Cleaned // canonical repository path: Path ("" = root) and Folder
	Tx     = meta.Tx            // opaque: it has no way to run SQL
	Limits = untrusted.Limits   // R-ARCH-028
)

type Format interface {
	Descriptor() Descriptor

	// Routes lists every protocol operation below /api/<id>/<repoKey>/. It is empty for a format whose
	// protocol is /files/ alone: generic (Q-GENERIC-7).
	Routes() []Route
	// Match resolves method + repository-relative path (still percent-encoded) to a route. It returns
	// errs.NotFound or errs.MethodNotAllowed and never touches storage. Usually:
	// return formats.MatchRoutes(f.Routes(), method, escapedPath).
	Match(method, escapedPath string) (*Route, Params, error)
	// Classify says how a canonical path behaves in caches and which package it belongs to. It gives
	// a /files/ request its coordinate (R-EP-014) and every request its R-REPO-003 descriptor.
	Classify(p Path) PathInfo
	// PackageDocumentPath returns the canonical path of the package document of a normalized
	// package name: the document whose existence upstream stands for the package's existence
	// (npm: the package document; NuGet: "{lower_id}/index.json"). ok is false for a format without
	// package names. Used by R-REPO-029 (gone packages) and R-REPO-048 (shadowing check).
	PackageDocumentPath(name string) (p Path, ok bool)

	// Storage hooks required by storage.md. Every event carries the PackageWriter bound to the hook's
	// transaction and the format's data port (Q-ARCH-193, Q-ARCH-212).
	IngestOptions(rq *Request) IngestOptions                                   // expected digests, SHA-512, media type
	Inspect(ctx context.Context, in InspectInput) (*Inspection, error)         // identity and namespaced properties (FMT-008)
	OnCommit(ctx context.Context, tx Tx, c CommitEvent) error                  // c.Origin: Upload or Fill; package and version rows
	OnItemsRemoved(ctx context.Context, tx Tx, e ItemsEvent) error

	// ServeCheck runs before the core serves a stored item on any route. errs.NotFound refuses the item
	// because of format state the generic item row does not carry. It reads through h.
	ServeCheck(ctx context.Context, h Host, repo RepoRef, it ItemRef, mode ServeMode) error

	Remote() RemoteAdapter     // nil exactly when Descriptor().Classes lacks Remote
	Virtual() VirtualAdapter   // nil exactly when Descriptor().Classes lacks Virtual
	// Exposure carries the format's exposure facts; nil makes the format unservable on an endpoint with
	// exposure rules (endpoints.md R-EP-018). It is the one source of those facts.
	Exposure() ExposureAdapter
	// View is nil for a format without package screens (api-and-ui.md R-API-029). The adapter orders the
	// format's versions and reads, through the format's data port, what the
	// generic rows do not hold: description, keywords, licenses, README and format-specific metadata
	// (Q-API-166). It runs no SQL; Validate refuses a view without package names.
	View() ViewAdapter

	// SetupTemplates returns the client setup templates (text/template) used by api-and-ui.md R-API-033.
	SetupTemplates() []SetupTemplate
	// Guide returns the format's guide (api-and-ui.md R-UI-049, R-API-053): what a repository of each class is
	// and how each client uses it, as Markdown templates over GuideData. A format with clients covers every class
	// and client with the required topics (kit check 8, Q-ARCH-235).
	Guide() []GuideSection

	// WriteError renders a core error in the client's native error shape (R-ARCH-023). The dispatcher also
	// uses it for the refusals of the chain's own stages on this format's paths (Q-ARCH-214). Once the dispatcher
	// knows the repository key, r carries a Refusal (RefusalFrom(r)): the format ID, the key as addressed and the
	// response base URL, so that a refusal can name an absolute URL (Q-ARCH-263).
	WriteError(w http.ResponseWriter, r *http.Request, err error)
}

// Descriptor is what a format registers: the catalog facts (R-REPO-003) plus what only the contract needs.
type Descriptor struct {
	catalog.Facts                    // ID, DisplayName, Classes (the authoritative class matrix), Layout, ...
	ContractVersion  int             // the literal the format was written against; Register panics on a mismatch
	Names            catalog.Names   // the one package-name normaliser; nil exactly when !PackageNames
	Clients          []Client        // native clients and tested versions (FMT-002, FMT-003)
	// SettingsSchema ([]byte, JSON Schema 2020-12 of the settings member named after the format) is a catalog fact: catalog.Facts, Q-ARCH-229
	PropertyPrefix   string          // reserved property namespace, e.g. "npm." (FMT-008)
	MaxDocumentBytes int64           // bound for documents held in memory (R-ARCH-028)
	Limits           Limits          // tightenings of the defaults of R-ARCH-028
	// NotifyAreas are the signal areas the format may name in PackageWriter.Notify (catalog.FormatAreas); the writer raises the
	// signal of a changed package itself (Q-ARCH-259). Register refuses any other area.
	NotifyAreas []string
	DataPort         reflect.Type    // the interface type of the format's side-table port; nil without side tables (Q-ARCH-193)
	// VerifyAnnouncedDigests: the protocol addresses artifacts by the digests its metadata announces, so a remote fill
	// compares them whatever integrity.checksumPolicy says (R-REPO-041: npm always verifies, R-NPM-026).
	VerifyAnnouncedDigests bool
}

type Route struct {
	Name     string   // stable metric, log and audit label, e.g. "npm.tarball.get"
	Method   string
	Template string   // documentation form and default matcher, e.g. "/{package}/-/{file}"; "{rest...}" takes the tail
	Examples []string // escaped paths this route must match and no other route of its method may (check 1)
	Action   authz.Action // coarse check before the body is read
	Kind     RouteKind // Download, Metadata, Computed, Upload, Delete, Admin, Volatile
	Classes  []Class  // the classes that serve the route; empty is every class of the format
	Match    func(escapedPath string) (Params, bool) // overrides the template for a grammar it cannot state
	Handler  HandlerFunc
	APIKey   bool     // the route accepts a credential in the format's API-key header besides Authorization (NuGet X-NuGet-ApiKey, R-NUGET-012); examined only there, first (Q-ARCH-266)
}

// ServesClass reports whether the route serves repositories of a class (Classes empty: every class).
func (r *Route) ServesClass(c Class) bool

type HandlerFunc func(ctx context.Context, rq *Request, w http.ResponseWriter) error

type Request struct {
	*http.Request
	// Repo is the repository this request acts on: the one the client addressed or, on a request derived through
	// Member or WriteTarget, the member or deployment target the call is made for. Addressed names the one the client
	// addressed. The addressed key itself (the unexported field via) is set only by Member (Q-ARCH-252).
	Repo     *repository.Repository // an immutable snapshot entry
	Route    *Route
	Params   Params
	Path     Path                   // canonical repository path (R-ARCH-005)
	Escaped  string                 // the repository-relative path as received
	Subject  authz.Subject          // who acts, and on which endpoint (Subject.Endpoint, Q-EP-20)
	Index    *authz.Index           // the index the request began on; every decision of the request runs on it
	Endpoint EndpointFacts          // Name, Rules, CacheFill, ExposureFingerprint: derived from the listener's endpoint
	CacheView bool                 // the request addressed the cache view of a remote repository, "<key>~cache" (R-REPO-032)
	Host     Host                  // shadows http.Request.Host on purpose: no handler reads the Host header (R-ARCH-006)
	Log      *slog.Logger          // request-scoped; nil logs to slog's default
}

// Refusal is what the dispatcher hands WriteError through the request it receives, once the repository key is known
// (Q-ARCH-263). Registry is Base + "/api/" + Format + "/" + Key + "/".
type Refusal struct {
	Format  ID
	Key     string // as addressed: the key, or "<remote>~cache"
	BaseURL string // the response base URL without a trailing slash
}

func (f Refusal) Registry() string // BaseURL + "/api/" + Format + "/" + escaped Key + "/"
func WithRefusal(r *http.Request, f Refusal) *http.Request
func RefusalFrom(r *http.Request) (Refusal, bool)

// Addressed is the key of the repository the client addressed: the outermost virtual repository on a request derived for
// one of its members, the request's own repository otherwise. Every coordinate the Host builds carries it as
// Coordinate.Addressed (R-AUTHZ-015, Q-AUTHZ-165).
func (rq *Request) Addressed() string

// Member returns a copy of the request made for a member of rq.Repo: Repo is the member, the addressed key stays the
// outermost one. A repository that is no member is refused with an internal error.
func (rq *Request) Member(m *repository.Repository) (*Request, error)

// WriteTarget returns a copy made for the deployment target, for a read that stands for the write (R-NPM-029). It
// carries no addressed virtual repository: such a read is decided on the target, which a scope must name itself.
func (rq *Request) WriteTarget(target *repository.Repository) *Request

// RepoRef is Repo as the Host's calls name it: the numeric id of its row, key, class and format.
func (rq *Request) RepoRef() RepoRef

// Target is what a format asks the decision point inside the addressed repository. The host completes the
// coordinate (repository, class, format, the kind of the route), so a format names neither another repository
// nor another endpoint (Q-ARCH-189). That holds for Authorize. The read stores, EnsureLocked and DeleteItems take a RepoRef
// the format builds and refuse one that is not the request's or a member below it (RepositoryAllowed, Q-ARCH-260); the
// signatures stay, and rq.Repo stays the whole repository model (Q-ARCH-260 records why).
type Target struct {
	Package, Version, Path string // normalized; all empty is the repository level
	Actions                []Action
}

type PathKind uint8

const (
	Content      PathKind = iota + 1 // immutable once stored
	PathMetadata                     // upstream document that expires after the metadata cache period
	PathComputed                     // rendered by trove from stored rows, never fetched
	PathVolatile                     // passed through, never cached (search, advisory queries)
)

// RequestKind maps a path kind to the request kind of R-REPO-003; String names it.
func (k PathKind) RequestKind() RequestKind
func (k PathKind) String() string

// PathInfo is the format's R-REPO-003 descriptor of a canonical path.
type PathInfo struct {
	Kind        PathKind
	Package     string // normalized, "" when the path belongs to no package
	Version     string
	RoutingName string // for an artifact without a package, so patterns, claims and pins can protect it
}

// ServeRoute runs rq.Route.Handler with the guarantees the core owes every route (the dispatcher and the
// conformance kit both call it): the content headers of R-ARCH-007 for Download, Metadata, Computed and
// Volatile routes (Volatile without Content-Disposition, Q-ARCH-258), panic recovery that logs format and
// route (R-ARCH-028), WriteError for an error returned before the status line, and an aborted connection
// for one after it.
func ServeRoute(f Format, w http.ResponseWriter, rq *Request, sameOrigin bool)

type RemoteAdapter interface {
	// UpstreamRequest maps a repository path to the upstream request.
	UpstreamRequest(ctx context.Context, rc RemoteContext, p Path, variant string) (*http.Request, error)
	// AcceptResponse checks status, headers and declared type before a byte is stored or sent.
	AcceptResponse(p Path, resp *http.Response) error
	// PrepareFill validates a fetched metadata document before anything is stored, outside every
	// transaction (R-ARCH-019); an error keeps the previous entry and answers 502 in the format's shape.
	PrepareFill(ctx context.Context, rc RemoteContext, p Path, variant string, body io.ReaderAt, size int64) (Prepared, error)
	// OnFill runs inside a remote metadata cache-fill transaction, while the core holds the cache
	// entry's row lock and before it writes the entry. f.Replaces says whether the core's upsert
	// rule replaces the stored document or keeps a newer stored one (repositories-and-proxy.md
	// R-REPO-026 step 5). The decision is FillByFetchTime (the upsert keeps a later stored fetch),
	// FillKeepStored (keep the stored document and renew only its freshness) or FillReplaceStored
	// (the format recorded this document as the newer one by its own order, Q-REPO-120).
	OnFill(ctx context.Context, tx Tx, f FillEvent) (FillDecision, error)
	// Rewrite renders a stored upstream document for one response (repositories-and-proxy.md).
	Rewrite(ctx context.Context, rc RemoteContext, p Path, doc []byte, links LinkBuilder) ([]byte, error)
	// TracksVersionRemoval says that the format's removal marks replace the generic gone-package rule (R-REPO-029).
	TracksVersionRemoval() bool
	// ExpectsType declares the media types the format expects for a path; the engine does not block them (R-REPO-042).
	ExpectsType(p Path, mediaType string) bool
	// BeforeArtifact runs before the engine looks at or fetches an artifact (also for a remote member of a virtual
	// repository, on the request derived through Member): the format decides whether its stored marks allow it and
	// which digests its metadata announces (R-NPM-025, R-NPM-026, Q-NPM-13). Both have defaults in the embeddable
	// RemoteBase. They were optional interfaces found by type assertion; they are methods of the contract so that a
	// format cannot forget them unnoticed (Q-ARCH-254).
	BeforeArtifact(ctx context.Context, h Host, rq *Request, p Path) (Digests, error)
	// UpstreamQuery maps a pass-through Query to the upstream request (R-ARCH-013 step 6): a GET or HEAD without a
	// body, whose method and URL are the key of the cached answer. The default in RemoteBase refuses, for a format
	// with no Volatile route that is passed through.
	UpstreamQuery(ctx context.Context, rc RemoteContext, q Query) (*http.Request, error)
}

// Three optional interfaces of a RemoteAdapter, read off the adapter once, when the format registers (Q-ARCH-267,
// Q-ARCH-268, Q-ARCH-284). A capability only some formats have is an optional interface and not a method with a default
// (Q-ARCH-254), so a format that does not need it changes nothing.

// Discoverer: the format's upstream addressing follows a discovery document (R-NUGET-014). The engine resolves it
// as a metadata document of the repository before it asks UpstreamRequest for any other path, parses it with
// ParseDiscovery and gives the result to every call of that fetch as RemoteContext.Discovery. The request for the
// document itself carries none. The host resolves it the same way for UpstreamQuery (Q-ARCH-282).
type Discoverer interface {
	DiscoveryDocument() (p Path, variant string, ok bool)
	ParseDiscovery(ctx context.Context, rc RemoteContext, doc []byte) (any, error)
}

// FillInspector: the format inspects an artifact it fetched before it is stored (R-NUGET-015). InspectFill runs after
// the digest check and before the item commit, outside any transaction; no byte of an artifact with a hook reaches a
// client before it accepted it. Its result becomes CommitEvent.Inspection; its error is the answer to every request
// of the flight, 502 unless it is an *errs.Error of another class, and nothing is stored.
type FillInspector interface {
	InspectsFill(p Path) bool
	InspectFill(ctx context.Context, rc RemoteContext, p Path, sp Spooled) (*Inspection, error)
}

// MirrorLister: the packages of the format's upstream can be discovered, so a remote repository of the format can run in
// mode mirror (repositories-and-proxy.md R-REPO-050 to R-REPO-055, ADR-152). It is the leaf internal/mirror/discover's
// Lister, which the mirror engine, below the contract, takes by its type: MirrorMethods (what the upstream offers),
// MirrorNames (a page of names of a pass, from a cursor), MirrorPlan (fills a package's documents through the engine and
// returns its artifacts newest first) and MirrorHosts (discovery hosts besides the upstream's, without credentials).
type MirrorLister = discover.Lister

// FactsGate: BeforeArtifact for a caller that holds the rows the item lookup read for the cached artifact (ItemRef.Facts,
// R-ARCH-027, Q-ARCH-291). The adapter decides on them when they were read for its own key and asks the data port
// otherwise; it returns the facts the artifact's serve check is to use, which are the ones it read again after a
// revalidation. RemoteFetcher.GateWith is the host's side of the same call (Gate with the facts), for a remote member of a
// virtual repository.
type FactsGate interface {
	BeforeArtifactWith(ctx context.Context, h Host, rq *Request, p Path, have ServeFacts) (Digests, ServeFacts, error)
}

// RemoteCaps are the optional interfaces Register found on a format's remote adapter; a field is nil for one the
// adapter lacks. They are the Remote field of Capabilities, and RemoteCapabilities returns them by format ID.
type RemoteCaps struct {
	Discovery Discoverer
	Fill      FillInspector
	Mirror    MirrorLister
	Gate      FactsGate
}

func RemoteCapabilities(id ID) RemoteCaps

type VirtualAdapter interface {
	Strategy(p Path) Strategy
	// Merge combines member documents. Each part carries its member's tier and position, so the format
	// can apply the per-version source rule of repositories-and-proxy.md.
	Merge(ctx context.Context, rq *Request, p Path, parts []MemberDocument) ([]byte, error)
	// LocalDocument renders a local member's document from its rows (R-ARCH-012) for the core's
	// merged-metadata cache (R-ARCH-015 step 3), so no format code orchestrates that cache.
	LocalDocument(ctx context.Context, rq *Request, member RepoRef, p Path, variant string) (doc []byte, rev RevSeq, found bool, err error)
	// ArtifactPrefix is the prefix, with a trailing slash, below which a remote member keeps the cached artifacts of a
	// package: the directory its versions live in, which the merge lists to find the versions whose artifact is cached
	// (R-AUTHZ-008). Only the format knows it: npm's tarballs are below the document of the package, NuGet's below the
	// directory of its version list. "" says that the cached artifacts are not below one prefix per package.
	ArtifactPrefix(name string) string
}

type Strategy uint8

const (
	FirstMatch Strategy = iota + 1 // tiered lookup, first eligible member that has it wins
	Merge                          // every eligible member contributes
)

// ExposureAdapter is a format's part of endpoint exposure (endpoints.md R-EP-018): the version syntax the decision
// point compiles rules with and the dimensions rules may narrow. What an endpoint hides from a document is the format's
// own filter over Request.Endpoint.Exposure; no core code calls one (Q-ARCH-257).
type ExposureAdapter interface {
	catalog.Versioning // ValidateRange, Prerelease, InRange
	Dimensions() ExposureDimensions
}

// ExposureView is what the endpoint exposes of the addressed repository. A format whose rules also narrow tags reads them
// from the same value through an interface of its own, because what a tag is belongs to the format.
type ExposureView interface {
	Package(name string) bool
	Version(name, version string) bool
}

// ItemsRestorer is the optional restore hook: it runs in the transaction that restored items. It is not part of Format
// (the restore does not exist yet); CapabilitiesOf finds it (Q-ARCH-256).
type ItemsRestorer interface {
	OnItemsRestored(ctx context.Context, tx Tx, e ItemsEvent) error
}

// Capabilities are the optional behaviours of a format, found once when it registers. A new behaviour is an optional
// interface and a field here (R-ARCH-025).
type Capabilities struct {
	ItemsRestorer ItemsRestorer // nil for a format without a restore hook
	Remote        RemoteCaps    // the optional interfaces of the remote adapter (WP3.0a, WP3.0b)
	Vulnerability VulnerabilityAdapter // nil for a format without the vulnerability adapter (R-VULN-009, ADR-150)
	ServeFacts    ServeFactsKeyer      // nil for a format whose serve check reads nothing besides the item (Q-ARCH-291)
}

// ServeFactsKeyer names, for a path of a repository, the rows the format's ServeCheck decides on, or the zero key when it
// wants none (Q-ARCH-291). The item lookup of a download reads them in its own statement and returns them with the item as
// ItemRef.Facts; ServeCheck computes the same key and decides on the facts only when they were read for it
// (ServeFacts.For), so a route that did not pass the key stays correct and costs its own query. The types are those of the
// leaf package internal/servefacts: ServeKey{Kind, Package, Version} with the kinds ServeNPMLocal (the tombstone of a local
// npm version), ServeNPMRemote (the facts of a remote npm tarball, ServeNPMRemoteFacts) and ServeNuGetLocal (the version row
// and the tombstone of a local NuGet version); ServeKind is the type of a kind. Every kind is the rows of one statement the
// check ran before (NPMTombstoned, NPMRemoteTarballFacts, NuGetServeState).
type ServeFactsKeyer interface {
	ServeFactsKey(repo RepoRef, p Path) ServeKey
}

// VulnerabilityAdapter lets the core match stored versions against OSV advisories (vulnerability-scanning.md R-VULN-009).
type VulnerabilityAdapter interface {
	// Ecosystem is the OSV ecosystem name of the format ("npm", "NuGet").
	Ecosystem() string
	// AdvisoryName normalizes a package name of an advisory as the format normalizes stored names; ok is false for a name
	// the format refuses.
	AdvisoryName(name string) (normalized string, ok bool)
	// AdvisoryVersion normalizes a version of an advisory as package_versions.version is normalized.
	AdvisoryVersion(v string) (normalized string, ok bool)
	// CompareVersions orders two normalized versions by the format's precedence (-1, 0, +1); an error for a version that
	// does not parse.
	CompareVersions(a, b string) (int, error)
}

// VulnerabilityData is what a format's audit routes read of the scanning (R-VULN-017, R-VULN-018); Host.Vulnerabilities
// returns it. Its types are those of the leaf package internal/vuln/advisory; the matcher is the leaf package
// internal/vuln/match, which a format imports and calls itself (match.Affects, ADR-150).
type VulnerabilityData interface {
	// State is the state of the ecosystem's advisory data from the replica's cache, no query; not complete when the
	// scanning is not wired or the ecosystem was never imported.
	State(ecosystem string) advisory.DataState
	// ForNames returns the non-withdrawn advisories that name any of the names, keyed by name, in one query.
	ForNames(ctx context.Context, ecosystem string, names []string) (map[string][]advisory.Advisory, error)
	// All returns every non-withdrawn advisory of the ecosystem (the NuGet page).
	All(ctx context.Context, ecosystem string) ([]advisory.Advisory, error)
	// InternalNames reports which names are internal names of the format (R-VULN-010), which an audit withholds.
	InternalNames(ctx context.Context, format ID, names []string) (map[string]bool, error)
	// CountAudit counts one audit in trove_vuln_audit_requests_total (R-VULN-024), so that a format imports no metric package.
	CountAudit(format ID, mode, outcome string)
	// Enabled reports whether the installation's vulnerabilities.enabled is true, which an audit mode local needs (R-VULN-017).
	// The composition root sets it explicitly (formathost.VulnPorts.Enabled); it is not derived from which ports are wired.
	Enabled() bool
	// LocalByDefault reports whether the audit mode of a repository whose document does not set one is local: Enabled and the
	// OSV source is not off (R-VULN-017, R-VULN-018, Q-VULN-44).
	LocalByDefault() bool
	// PageKey is the data epoch of the ecosystem and the applied epochs of the claims and repo_config areas as text: the
	// input of the weak ETag of the NuGet vulnerability page (R-VULN-018, Q-VULN-45).
	PageKey(ecosystem string) string
	// Page returns the page cached under the key in the replica's cache vuln_nuget_page, or renders and caches it.
	Page(ctx context.Context, key string, render func(context.Context) ([]byte, error)) ([]byte, error)
}

// NoVulnerabilityData returns the VulnerabilityData of a host that has not wired the scanning: every ecosystem is not
// complete and a read answers 503 vulnerability_data_unavailable.
func NoVulnerabilityData() VulnerabilityData

// CapabilitiesOf reads the capabilities off a format: the one place that asserts to an interface of this package.
func CapabilitiesOf(f Format) Capabilities

// CapabilitiesFor returns the capabilities recorded when the format registered.
func CapabilitiesFor(id ID) Capabilities

// WrapsContent reports whether the core wraps a route of this kind in the content headers of R-ARCH-007: Download,
// Metadata, Computed and Volatile.
func (k RouteKind) WrapsContent() bool

// RepositoryAllowed refuses a repository that is neither the request's nor a member below it (Q-ARCH-260); the stores
// and the package writer of a Host call it with the context ServeRoute gave the handler.
func RepositoryAllowed(ctx context.Context, repo RepoRef) error

type SetupTemplate struct {
	ID       string // stable, e.g. "npm.npmrc.configure"
	Client   string // "npm", "pnpm", "yarn", "dotnet", "nuget"
	Section  string // "configure", "publish", "install"
	Lang     string // "shell" (POSIX), "powershell", "cmd", "ini", "xml", "yaml": the quoting of what the API substitutes
	File     string // target file when the snippet is file content, e.g. ".npmrc"
	Template string // text/template over SetupData, which has no credential field (api-and-ui.md R-API-030)
	// VersionSpec is how an install template attaches the version after the package when the request names one,
	// SetupVersionRef standing for it: "" is "@<version>" (npm), " --version <version>" is dotnet (Q-API-188).
	VersionSpec string
}

// SetupData: RegistryURL, Repository, Endpoint, Variant (the variant of a configure snippet the request asked for, in the
// words of the format: npm's scope without "@"), Username and RequiresAuth (the repository is not anonymously readable).
// A template that needs the registry URL without its scheme, the key of a keyed credential line, derives it with the
// template function schemeless (Q-ARCH-257).

type GuideSection struct {
	ID       string  // stable, e.g. "npm.remote.what"
	Topic    string  // what, configure, authenticate, install, publish, maintain, audit, lockfiles, endpoint, troubleshooting
	Title    string  // the heading, plain text
	Classes  []Class // the classes the section is written for; empty is every class
	Clients  []string // the clients it is written for; empty is every client
	Snippets string  // the setup section whose snippets the API attaches: "configure", "install", "publish" or ""
	Markdown string  // text/template over GuideData; the only credential it may spell is a token reference: SetupTokenRef, SetupTokenRefWin or SetupTokenRefPS
}

// AppliesTo reports whether the section is written for a class and a client ("" for no client).
func (g GuideSection) AppliesTo(class Class, client string) bool

// ViewAdapter is a format's part of the package screens (api-and-ui.md R-API-029) and of the dry run of the exposure
// rules (endpoints.md R-EP-016). It reads through the format's data port and runs no SQL. Whether a version is a
// pre-release is the exposure adapter's answer (Versioning.Prerelease), taken once by the host (Q-ARCH-257).
type ViewAdapter interface {
	// Compare is a total order over the versions the format stores (negative: a is older than b); a string that is no
	// version sorts before every version, and equal strings compare equal.
	Compare(a, b string) int
	Describe(ctx context.Context, ports DataPorts, vs []ViewVersion, want ViewFields, visible func(version string) bool) (map[VersionID]ViewData, error)
	Find(ctx context.Context, ports DataPorts, f ViewFind, limit int) ([]VersionID, error)
	// Withheld returns the ids of the stored versions the format does not serve (R-NPM-047).
	Withheld(ctx context.Context, ports DataPorts, repos []string) ([]VersionID, error)
	// Stored returns at most q.Limit of the versions a local or a remote repository serves of the package, newest
	// first, and the tags when q.Tags is set (Q-EP-67). "Serves" means answerable by a request for the exact version:
	// an unlisted version the repository still serves counts, a deprecated one counts, a withheld one does not.
	// "Newest first" is arrival time for a hosted repository, publish time for a remote one (unknown last), and for a
	// format with neither Compare descending. A package the repository does not hold is the empty answer.
	Stored(ctx context.Context, ports DataPorts, q StoredQuery) (Stored, error)
}

type DataPorts interface{ FormatData(id ID) any } // the part of Host the view reads format data through

// StoredQuery asks what one repository stores under one package name; Tags is true only for a format whose catalog
// entry has tags (catalog.Exposure.Tags).
type StoredQuery struct {
	Repo    RepoRef // local or remote; the host never asks for a virtual repository
	Package string  // normalized
	Limit   int
	Tags    bool
}

type Stored struct {
	Versions []string          // newest first
	Tags     map[string]string // tag to version; empty unless asked
}

type (
	ViewVersion struct {
		ID      VersionID
		Repo    RepoRef
		Package string
		Version string
	}
	ViewFind struct {
		Repos   []string
		Text    string
		Keyword string
	}
	ViewData struct {
		Description string
		Keywords    []string
		Licenses    []string
		HasReadme   bool
		Metadata    map[string]any
		Readme      string
		ReadmeFile  string
	}
	ViewFields uint8
)

const (
	ViewSummary ViewFields = 1 << iota // description and keywords
	ViewDetails                        // adds the licenses, whether a README exists and the metadata
	ViewReadme                         // adds the README text and its file name
)

// Has reports whether f includes every field of o.
func (f ViewFields) Has(o ViewFields) bool

// GuideData: SetupData plus Class, Client, PublishTarget (only when the reader may deploy there), Anonymous,
// ExposureRules (the endpoint serves under exposure rules) and TokenEnvVar (a variable name). No credential field.
// formats.ExecuteGuide parses and runs a section; the API and the kit both call it.
```

Supporting types of the blocks, named here so that the contract has no type the spec does not know
(`TestR_ARCH_008_EveryExportedTypeOfTheContractIsNamedInTheSpec`): `PackageID` is the id of a package row;
`URLMapping` (path and upstream URL) and `RemoteDocument` (a stored or fetched upstream document with its times and
`CacheStatus`) are what a remote adapter reads and hands over; `Currency` says how current a document is after
`EnsureCurrent` (Q-NPM-13); `RemovedItem` is an item a removal took away, with its version and the SHA-256 of its content (Q-NUGET-39); `ItemsEvent` is what the removal hook and the restore hook learn; `ShadowPair` names a
virtual repository and the remote member that serves a package (R-REPO-048); `RemoteBase` gives a `RemoteAdapter` the
defaults of `ExpectsType`, `BeforeArtifact` and `UpstreamQuery` to embed; `RemoteContext` carries the repository, the upstream URL, the
format's settings and, once the engine resolved the discovery document of a `Discoverer`, its parsed form in
`Discovery` (Q-ARCH-268).

The request types of the pass-through and of the fan-out over the members of a virtual repository are in `RemoteFetcher` and
`Resolver`, which the blocks leave to the code and name here (Q-ARCH-270, Q-ARCH-271, Q-ARCH-272). `RemoteFetcher.Query` takes a
`Query` (the repository path of a `Volatile` request, its already normalised parameters and the period its answer is kept) and
returns a `QueryResult` (the upstream's answer as received, shared with the cache); `RemoteFetcher.Gate` runs
`RemoteAdapter.BeforeArtifact` without fetching, for the cached tier of a remote member of a virtual repository.
`Resolver.Plan` returns a `VirtualPlan` of `PlannedMember`s (member, tier, position, whether the caller may fill it);
`Resolver.KeepsAway` answers whether a claim, pin rule or exclude pattern keeps a search term away from a member;
`Resolver.ReadScope` returns the caller's `ReadScope` for the rows of a listing, whose `ReadShape` says per repository whether
every row, none or only the rows `MayRead` allows can be read.

The blocks above are held equal to the code: `TestR_ARCH_008_TheWrittenContractEqualsTheCode`
(`internal/formats/contract_spec_test.go`) parses the Go blocks of R-ARCH-008 and R-ARCH-009 and compares every type
they declare (exported fields, interface methods, aliases), the methods the code attaches to it, the functions and
the constants with the package, and fails with one line per difference (Q-ARCH-254). The unexported field `via` of
`Request` is not part of the contract.

The functions and values of the package that the blocks leave to the code are named here, so that nothing exported
joins the contract without this page knowing it (`TestR_ARCH_008_EveryExportedFunctionAndValueOfTheContractIsNamedInTheSpec`):
`CatalogEntry` derives a format's catalog entry from its descriptor and exposure adapter, and `Descriptors` lists the
descriptors of the registered formats, the body of `GET /api/v1/formats`; `GuideTopics` lists the guide topics in serving
order (`GuideWhat`, `GuideConfigure`, `GuideAuthenticate`, `GuideInstall`, `GuidePublish`, `GuideMaintain`, `GuideAudit`,
`GuideLockfiles`, `GuideEndpoint`, `GuideTroubleshooting`) and `RequiredGuideTopics` the ones kit check 8 requires;
`GuideRepositoryPlaceholder` stands for the key in a guide that names no repository; `SetupTokenRefs` lists the
spellings of the token reference in the order the API looks for them; `SubjectName` and `SubjectPath` are the catalog's
pattern subjects and `KindArtifact`, `KindMetadata`, `KindSearch`, `KindWrite` and `KindOther` its request kinds, under
the names a format spells; `TierLocal`, `TierRemoteCached` and `TierRemote` are the tiers of a merge part; `Current`,
`Revalidated` and `Unknown` are the values of `Currency`.

**The freeze.** The contract is frozen at `ContractVersion` 1 since the end of M3 (E3.3,
[ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)).
`TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile` holds the exported surface of `internal/formats` and of
`internal/formats/catalog`, and the types of the two leaf packages the contract speaks in (`internal/vuln/advisory` with its
constants, `internal/vuln/match`; ADR-150, Q-VULN-73), equal to
[internal/formats/testdata/contract.txt](../../internal/formats/testdata/contract.txt); every change of it is an ADR, and
R-ARCH-025 says which changes keep the version (Q-ARCH-280).

**Optional behaviour.** `Format` holds what every format answers. A behaviour that only some formats have, or that
the core cannot call yet, is an optional interface of this package, found by `CapabilitiesOf` when the format registers
and recorded in the registry (`CapabilitiesFor`); the core never asks the format value (the architecture test allows
that assertion in `CapabilitiesOf` alone). Today there are five: `ItemsRestorer` on the format, `Discoverer`,
`FillInspector` and `MirrorLister` on its remote adapter (`Capabilities.Remote`, Q-ARCH-267, Q-ARCH-268, Q-ARCH-284), and
`VulnerabilityAdapter` on the format (`Capabilities.Vulnerability`, R-VULN-009,
[ADR-150](../decisions.md#adr-150--the-format-contract-gains-the-optional-vulnerability-adapter)). `MirrorLister` also
derives the catalog fact `Entry.Mirror`, as the vulnerability adapter derives `Entry.Ecosystem`
([ADR-152](../decisions.md#adr-152--the-format-contract-gains-the-optional-mirror-lister)). A behaviour that every format
answers, and whose default is a correct answer for a format without it, is a method with that default in `Base` or
`RemoteBase` instead (`ExpectsType`, `BeforeArtifact`, `UpstreamQuery`). The rule for adding to the contract is in
R-ARCH-025.

Normative rules:
- `Descriptor().Classes` is the single source of truth for the classes a format supports. Creating
  a repository with any other class is rejected ([repositories-and-proxy.md](repositories-and-proxy.md)).
  - In the catalog, two lists of formats per class disagree (FMT-001). trove settles this by keeping
    no second list.
  - The per-class format sets of the OpenAPI document, the `classes` of `GET /api/v1/formats` and
    the formats the UI offers for a chosen class are all generated from `Classes` (R-ARCH-010).
- `Classify` MUST put every path a route serves into exactly one kind. A `Content` path is never
  refreshed. A `Metadata` path is never treated as immutable.
- `PackageDocumentPath` is the only place where the core learns which document stands for a
  package. [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-003 lists it in the format
  registration. Two rules use it, and neither keeps a list per format:
  - the upstream-gone rule of R-REPO-029, which marks a package gone when this document is gone;
  - the shadowing check of R-REPO-048, which probes this document.

  Names that the format normalizes to the same name MUST yield the same path.
- `Inspect` writes properties only under `PropertyPrefix`. Who may change that namespace is
  decided in [authorization.md](authorization.md). For uploads, the name `Inspect` reads from the
  content is the name authorization checks ([authorization.md](authorization.md)).
- `Route.Action` is a coarse check that the core runs before the body is read: "may this principal
  do this action anywhere in this repository?". The handler then decides on the concrete path or
  package through `Host.Authorize`, with a `Target` that names it; the host completes the coordinate
  with the repository, its class and format and the request kind of the route.
- A format that has side tables of its own declares the interface type of their port in
  `Descriptor.DataPort`; the port is implemented by a domain package (`internal/formatdata/<id>`) and
  bound to the format ID by the composition root (Q-ARCH-193). A hook reaches it as `event.Data`, a
  route as `Host.FormatData(id)`. A declared port without a binding, a binding that does not
  implement it and a binding for no format stop the start-up.
- A format writes its generic package rows through the `PackageWriter` of the event (R-ARCH-009) and
  its side rows through its port, which takes the opaque `Tx` per call. It runs no SQL and cannot
  unwrap the `Tx`.
- `OnCommit`, `OnItemsRemoved`, `OnItemsRestored` and `RemoteAdapter.OnFill` run inside the
  storage transaction and MUST NOT perform I/O outside PostgreSQL.
- **Remote fill.** The storage hook that runs inside a remote metadata cache-fill transaction,
  `RemoteAdapter.OnFill` (R-ARCH-012 lists remote cache fill among its triggers), MUST:
  1. tell the format whether the cache-entry upsert replaces the stored document or keeps the newer
     stored one ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 step 5), and let
     the format answer that its own order outranks the fetch time (Q-REPO-120);
  2. let the format keep the stored document, with only its freshness renewed, when the format's
     own comparison against a value it recorded for the stored document says the received one is
     older.

  npm uses this for its stale-copy rule against `npm_remote_packages.doc_modified`
  ([formats/npm.md](formats/npm.md) R-NPM-024, Q-NPM-5). A format writes its side tables of remote
  documents in this hook, and only when the stored document is replaced. They then follow the
  stored document even under duplicate fills, which a lost `FlightLock` or an unavailable Valkey
  causes (R-ARCH-033).
- **Serve-time check.** The core calls `ServeCheck` before it serves a stored item on any route:
  `/files/<repo>/<path>`, a remote's cache view, virtual tier resolution and the format's own
  routes. The mode says what the route may do: `ServeStored` decides on the stored marks only and
  never contacts an upstream (`/files/`, the cache view, virtual tiers 1 and 2);
  `ServeMayRevalidate` lets the format's own route revalidate a stale document first (Q-NPM-13). The
  generic layer reaches the check through the `files.Formats` seam, implemented over the registry by
  `internal/formathost`. Through it a format refuses an item with `404` because of format state the generic item
  row does not carry. npm refuses items of tombstoned versions, including versions withheld by a
  restore's replay ([formats/npm.md](formats/npm.md) R-NPM-047, Q-NPM-12), and of versions removed
  upstream (R-NPM-026).
  - The hook reads through `Host` like every other format access, and runs no SQL itself.
  - The in-process state a format's check uses is listed in R-ARCH-032 like any other per-replica
    state. npm uses none: it reads PostgreSQL per request.
- **Vulnerability adapter (from M4b).** A format takes part in vulnerability scanning by also
  implementing the optional interface `formats.VulnerabilityAdapter` of
  [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-009 (its OSV ecosystem, the
  normalization of advisory names and versions, the format's version order). `CapabilitiesOf` finds
  it when the format registers, as a field of `Capabilities` (R-ARCH-025; no other code asserts to an
  interface of the contract, Q-ARCH-254), so the frozen methods above and `ContractVersion` stay as
  they are; its addition is an ADR like every change of the frozen contract (ADR-141). A format
  without it is `not_covered` (Q-VULN-15).

Acceptance criteria:
- The conformance kit (R-ARCH-010) passes for `npm` and `nuget`, with no check skipped
  (`TestR_ARCH_010_NoCheckIsSkipped` in each format's package).
- Creating a repository whose class is not in the format's `Classes` fails with `422`.

### R-ARCH-009 · Core services offered to formats
Priority: **MVP** · Covers: FMT-007, STOR-001, STOR-002

```go
type Host interface {
	// Begin gives a request the state the host keeps for it (the write decisions the transaction records, the spool
	// files) and returns the function that releases it. The dispatcher calls it once, before the handler, and defers
	// the result; the state is bound to rq and to every request derived from it. The host holds none itself
	// (R-ARCH-032, Q-ARCH-265).
	Begin(rq *Request) (finish func())
	// Authorize decides the target for rq.Subject on rq.Index (authz.Decider.DecideOn) and returns the
	// refusal of R-AUTHZ-016, or nil. The host completes the coordinate and applies the repository's
	// include and exclude patterns (R-REPO-005) to the package name or the path.
	Authorize(ctx context.Context, rq *Request, t Target) error
	// CheckOwnership asks the ownership rule of R-AUTHZ-018 before a body is read, read-only: nothing is
	// locked or claimed, and the commit decides again (Q-ARCH-226).
	CheckOwnership(ctx context.Context, rq *Request, t Target, w OwnershipWrite) error
	// Write runs fn in one transaction (R-ARCH-019) with the PackageWriter bound to rq. The allowed
	// write decisions of the request are recorded in it (R-AUTHZ-029).
	Write(ctx context.Context, rq *Request, fn func(tx Tx, w PackageWriter) error) error

	Ingest() Ingestor            // upload pipeline of storage.md: admission, spool, digests, commit; Commit takes the request's other files as Companions, in one transaction (Q-ARCH-228)
	Items() ItemStore            // repository paths <-> blobs, Range and conditional reads (storage.md)
	Packages() PackageStore      // package and version rows behind the package view (FMT-007)
	Remote() RemoteFetcher       // remote engine (repositories-and-proxy.md); Exchange sends one request through a remote's upstream client outside the cache (Q-NPM-62); Query passes a Volatile request through with a registered per-replica cache (Q-ARCH-270); Gate runs the artifact gate without fetching (Q-ARCH-272)
	Resolver() Resolver          // virtual resolution (repositories-and-proxy.md); Plan, KeepsAway and ReadScope serve a format that fans a request out over the members (Q-ARCH-271)
	Links(rq *Request) LinkBuilder // absolute URLs on the response base URL of rq's endpoint and host (R-ARCH-006)
	Renditions() RenditionCache  // rendered documents (R-ARCH-012), keyed by a struct the core hashes
	FormatData(id ID) any        // the data port bound to a format (Q-ARCH-193); nil without Descriptor.DataPort
	RestoredAt(ctx context.Context) (time.Time, error) // system_state restored_at; zero until a restore ran
	Vulnerabilities() VulnerabilityData // the scanning's data for the audit routes (R-VULN-017, R-VULN-018); NoVulnerabilityData until wired
}

type PackageStore interface {
	Package(ctx context.Context, repo RepoRef, name string) (PackageRow, bool, error)
	Versions(ctx context.Context, repo RepoRef, name string) ([]VersionRow, RevSeq, error)
	// RevSeqs returns packages.rev_seq per repository id; a repository without the package maps to 0.
	RevSeqs(ctx context.Context, repos []RepoRef, name string) (map[int64]RevSeq, error)
}

// PackageWriter writes the generic package rows inside a storage transaction. It is bound to the
// transaction of the hook or of Host.Write that handed it out (Q-ARCH-212).
type PackageWriter interface {
	EnsureLocked(ctx context.Context, repo RepoRef, name, displayName string) (pkg PackageRef, created bool, err error)
	Versions(ctx context.Context, pkg PackageRef) ([]VersionRow, error)
	InsertVersion(ctx context.Context, pkg PackageRef, v NewVersion) (VersionID, error)
	TrashOrDeleteVersion(ctx context.Context, v VersionID, purge bool) error
	SetPackageAttributes(ctx context.Context, pkg PackageRef, attrs json.RawMessage) error
	SetVersionAttributes(ctx context.Context, v VersionID, attrs json.RawMessage) error
	LinkItem(ctx context.Context, it ItemRef, v VersionID, lead bool) error
	SetProperties(ctx context.Context, it ItemRef, props map[string]string) error // below PropertyPrefix only
	// BumpRevision raises rev_seq (once per transaction) and returns it with clock_timestamp() read after
	// the row lock. It is for a change that writes only the format's side rows.
	BumpRevision(ctx context.Context, pkg PackageRef) (RevSeq, time.Time, error)
	// Claim checks ownership and claims in the commit transaction (R-AUTHZ-018); AutoClaim applies the
	// namespace auto claim (R-REPO-023) and returns what it did, which the audit event of the publish
	// records. Both refuse until the composition root binds the rule
	// (Q-ARCH-215). The writer attaches the signal of a changed package itself: once per package it raised in the
	// transaction, the signal area metadata_changed with catalog.PackageChanged(repoID, name), which the virtual engine
	// reads to drop merged documents early (R-REPO-020, Q-ARCH-259). Notify attaches a notification of an area the
	// format declared in Descriptor.NotifyAreas (R-ARCH-018) for a change that raised no package row. EnsureLocked and
	// DeleteItems refuse a repository other than the one the writer is bound to (Q-ARCH-260).
	Claim(ctx context.Context, t Target, w OwnershipWrite) error
	AutoClaim(ctx context.Context, name string) (AutoClaimResult, error) // Created, Pattern; or the Reason: disabled, covered, skipped, limit
	Notify(area string, payload any) error
	// DeleteItems removes the files at the paths permanently (R-STOR-018), in this transaction and as the
	// actor of the request; OnItemsRemoved runs for them in it (npm: formats/npm.md Q-NPM-27).
	DeleteItems(ctx context.Context, repo RepoRef, paths ...string) error
}
```

- Formats never see blob addresses, bucket names or SQL. Item, package and blob schemas belong to
  [storage.md](storage.md).
  *Status (milestone M3):* the host hands no format an address, and `Bind(host, *config.Config, metrics)` hands a format the whole configuration,
  which holds the bucket name; npm reads its own section only. `Bind` is not part of the frozen contract: a typed environment with the format's own section
  is added later as an optional interface (R-ARCH-025), and the format then drops `Bind` (Q-ARCH-261).
- This page requires one thing of that schema: a revision sequence `rev_seq bigint NOT NULL DEFAULT 1`
  on storage.md's generic `packages` row, not in a format's side table.
  - The core reads it for read-your-writes (R-ARCH-012) and for the merged-metadata check
    (R-ARCH-015) without format code, so a format keeps no second counter of its own.
  - It is incremented in the transaction that changes the package, by the `PackageWriter` itself on
    every call that writes the package's generic rows, once per package per transaction however many
    calls there are (Q-ARCH-212). A change that writes only a format's side rows calls
    `BumpRevision`. Conformance kit check 9 (R-ARCH-010) catches a write that forgets it.
  - It is the first entry of the revision-column registry below, so a restore raises it above
    every value the old installation can have issued as an `ETag`
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).
- **Revision-column registry.** `meta.RegisterRevisionColumn(table, column)` in `internal/meta`
  registers every `bigint` column whose values clients hold as an `ETag` or send as `If-Match`, or
  that replicas compare as an epoch. A restore raises every registered column by 2^32, so no such
  value repeats with different content after a restore
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-022, Q-BAK-5).
  - `packages.rev_seq` is registered first, by this page. The owning pages register their own:
    the repository and claims revisions and the revision of the proxy-offline switch of
    [repositories-and-proxy.md](repositories-and-proxy.md), the revision columns of
    [gitlab-ci-oidc.md](gitlab-ci-oidc.md), and `cluster_epoch.epoch` (R-ARCH-018).
  - The registry is frozen at start-up like the other registries (R-ARCH-032), and the restore
    reads it ([backup-and-restore.md](backup-and-restore.md) R-BAK-021 step 6). No second list of
    revision columns exists.
- `internal/formathost` implements `Host` over the item store, `internal/packages` (the package
  rows), the decision point and, when they exist, the remote and virtual engines; until an engine
  exists its calls answer an internal error that says so. It holds no request state (R-ARCH-032): the
  decisions that allowed a write travel with the request and are recorded in the write's transaction.
  The storage hooks of the item store reach the formats through it (`items.Hooks`).
- Copy, move and delete change rows only, never blob content (STOR-002, [storage.md](storage.md)).
- Everything one client request writes goes through one transaction: items (the lead and the companion files of one
  request, Q-ARCH-228), package rows,
  properties, audit event and job enqueue. Its notifications are attached to that transaction and
  leave it as their area's class decides (R-ARCH-018): a change area writes its change-log row
  inside the transaction, and a signal area is published to Valkey through the coalescer after
  commit. A rollback sends neither.

Acceptance criteria:
- `go test ./internal/formats/...` passes with no PostgreSQL and no object store running, using the
  in-memory `Host` in `formattest`.
- A unit test over the migrated schema fails on a `bigint` column named `rev_seq`, `*_version`,
  `revision` or `epoch` that is not registered with `meta.RegisterRevisionColumn`
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).

### R-ARCH-010 · Format registry and conformance kit
Priority: **MVP** · Covers: FMT-001, FMT-002, FMT-003

- Formats are registered at compile time: `internal/formats/all` imports every format package and
  calls `formats.Register`. There are no Go `plugin` shared objects and no out-of-process formats:
  - the `plugin` package requires identical toolchain and dependency versions and does not run on
    every platform;
  - an external process would add a network hop to every package request.
- At start-up, `Register` panics, and the process exits, on any of these:
  - a duplicate or reserved ID;
  - a `ContractVersion` mismatch (the contract is frozen at 1 since the end of M3, E3.3 and ADR-141;
    a change that raises it moves every format in the same change, R-ARCH-025);
  - a route set that fails the kit's ambiguity check, which `Register` runs itself: every route has
    a name, a method, a template, a handler, a kind, an action and at least one example, and every
    entry of `Route.Examples` is matched by its own route and by no other route of the same method;
  - a class matrix with no matching adapter, for example `Remote` listed but `Remote()` returning
    nil.
- The registry feeds these ([api-and-ui.md](api-and-ui.md)):
  - the format list of the client setup API;
  - the repository create form (settings schema);
  - the OpenAPI enum of format IDs, and one enum per class listing the formats whose `Classes`
    contain that class.

  The create form offers, for the chosen class, only those formats. No hand-maintained list of
  formats per class exists anywhere in the code or the API document. The document holds the schemas
  `FormatName`, `LocalFormatName`, `RemoteFormatName` and `VirtualFormatName`
  (`# >>> WP2.1 formats` in `api/openapi.yaml`), and `TestR_ARCH_010_PerClassFormatEnums` compares
  each with the registry.
- `Register` ends the registry's life as a free-for-all: the composition root freezes the catalog
  before the replica serves, and a registration after that panics.

The conformance kit (`internal/formats/formattest`) is table-driven. Every format runs it in its
own tests:
1. Every route matches its documented examples through the format's own `Match`, and no other route
   matches them. The kit also runs what `Register` runs (`formats.Validate` and the catalog's rules).
2. `Match` rejects `..`, `%2e%2e`, NUL, control characters and paths longer than 4 KiB. The kit runs its
   traversal inputs, and every route example mutated with them, through `Match`; it runs them for a
   format without routes as well.
3. `Classify` returns a kind for every route example. No `Content` path is `Metadata`. A format whose protocol paths
   are not its canonical paths maps each example to the canonical path it stands for (`Fixture.Canonical`); an example
   that addresses none (a service index, a search, a write) has nothing to classify and is held to its kind by checks 7
   and 9, and a `Download` route's example must address one (Q-ARCH-281).
4. `Rewrite` is deterministic and idempotent. No upstream host remains in a download link of a
   rewritten document.
5. `Merge` is deterministic and follows the per-version source rule of
   [repositories-and-proxy.md](repositories-and-proxy.md): the version entry comes from the member
   that tier resolution would choose for that version's file.
6. `Inspect` enforces the limits of R-ARCH-028 against crafted inputs that the kit ships
   (`formattest/crafted`): it answers `413` or `422` within 5 s, and the heap, sampled with
   `runtime/metrics`, stays within 512 MiB of its baseline. A format whose remote adapter inspects fills
   (`FillInspector`, Q-ARCH-267) runs every crafted input its hook takes through `InspectFill` as well, under the same
   time and heap bounds and without a panic; what the hook answers is the format's (Q-ARCH-281). A format whose remote
   adapter follows a discovery document (`Discoverer`, Q-ARCH-268) runs the kit's crafted documents (JSON nested 100,000
   deep, an array of a million values, a page of HTML, invalid UTF-8, nothing) and the fixture's own (`Discovery`, at
   least one the format reads) through `ParseDiscovery` under the same bounds: a crafted document is refused, a valid one
   read, without a panic, and the document the adapter names is a metadata path of the format. Check 6 cannot be skipped
   for a format whose remote adapter reads a discovery document or inspects fills: R-ARCH-025's kit check for those
   behaviours.
7. Responses of `Download`, `Metadata`, `Computed` and `Volatile` routes carry the content headers of
   R-ARCH-007, including when the handler tries to remove them; a `Volatile` route may return
   package-derived bytes, and none of the four serves a `Content-Disposition` but `Download`. No route of
   another kind returns a body taken from package content, and a `Volatile` route serves no stored item. The headers
   are checked on what a route serves, so each of the four needs a content case it serves (a status below 400); a route
   whose cases only refuse (an operation not built yet, a refusal by design) is listed in the fixture's `NoContent` with
   the reason, and an entry for a route that a case serves fails (Q-ARCH-281).
8. Every client in `Descriptor().Clients` has `configure` and `install` templates. No template
   contains a token placeholder that the server would fill. The format's guide (`Guide()`, Q-ARCH-236)
   covers every class of `Classes` for every client with the topics `what`, `configure`,
   `authenticate`, `install` and `troubleshooting`, each saying something for every reader the kit
   tries (a named repository and none, one who may publish and one who may not, signed in and
   anonymous, an endpoint with and without exposure rules); the `configure` and `install` topics
   attach the setup snippets of their section; every section has a known topic, a title, a unique ID,
   classes and clients of the descriptor and executes over `GuideData`; and the guide obeys the
   template rule in the code a reader copies: its fenced code blocks spell no credential except the
   option names the fixture lists and the reference `${TROVE_TOKEN}`, and no section holds the shape of
   a token. A format without clients has no guide.
9. Every storage hook, and every route of kind `Upload`, `Delete` or `Admin` that changes package
   rows, leaves the affected package's `rev_seq` higher than before (R-ARCH-009), measured on the
   in-memory `Host`. Every hook the format has runs in at least one change case: `OnCommit` and `OnItemsRemoved` for a
   format with package names, `OnItemsRestored` for one with `ItemsRestorer`; a hook no change can drive is listed in
   `NoRevision` with the reason, as a write route that changes no package row is (Q-ARCH-281).
10. For every sample package name the kit ships, `PackageDocumentPath` returns a path that `Match`
    resolves to a `GET` route, and two spellings of the same normalized name yield the same path.
11. For a format with a view adapter (`View()` is not nil): `Compare` is a total order over the fixture's sample
    versions (reflexive, antisymmetric, transitive, never equal for two different versions, and a string that is no
    version sorts before every version); `Withheld` and `Stored` answer an empty repository of the local and the remote
    class, and a package it does not hold, without an error and without a version; over a package the fixture seeds,
    `Stored` lists exactly the versions the repository serves, newest arrival first, within its limit, with tags only
    when the query asks for them, and `Withheld` lists none of them (Q-ARCH-253, Q-EP-67).
12. For a format with routes: the document of every `Metadata` and `Computed` route that a content case serves is the same
    for a user, a service account and the anonymous caller, on one seeded Host: equal status, equal bytes and equal
    `ETag`, `Content-Type`, `Content-Encoding`, `Vary` and `Cache-Control` (R-BAS-013, Q-BAS-72). A route whose document
    depends on who asks is `Volatile`. A `Metadata` or `Computed` route without a content case that serves a document and
    without a `NoContent` entry fails, as in check 7.
13. From M4b, for a format that implements `VulnerabilityAdapter`: the matching corpus of
    [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-009 (the event table test and the
    frozen real-record corpus) gives the expected results (Q-VULN-15).

A check that cannot apply to a format is skipped in the format's test with a stated reason (generic
has no routes, no clients and no package names); a skip without a reason fails the check, so a
later class cannot pass a check vacuously. The kit also refuses a skip that the format contradicts:
checks 1, 3, 7 and 12 on a format that has routes, check 4 on one with a remote adapter, check 5 with a
virtual adapter, check 6 with an `Upload` route, check 8 with clients, setup templates or a guide, check 9 with
package names or a route that writes, check 10 with package names, check 11 with a view adapter, check 13 with a
vulnerability adapter. Check 2 is never skipped. A check with nothing to run over (an empty fixture) fails unless it
is skipped. npm and NuGet skip no check (`TestR_ARCH_010_NoCheckIsSkipped`); what a check of theirs does not look at
is an entry of `NoRevision` or `NoContent`, each with its reason, in the format's `conformance_test.go`.

A format runs the kit as `formattest.Run(t, format, formattest.Fixture{…})` in its own test; one
subtest per check is named `R-ARCH-010 check N <name>`. The fixture holds what the format alone can
state: extra paths for checks 2 and 3, upstream documents for 4, member documents for 5, the crafted
uploads for 6 (`InspectTarGz`, `InspectZip`, `InspectJSON` and `InspectXML` give the standard ones),
requests for 7, changes for 9, sample names for 10 and the sample versions, a package name and a seed for 11. The kit derives the rest from the format. It
requires a content case it serves, or a reasoned `NoContent` entry, for every `Download`, `Metadata`, `Computed` and
`Volatile` route, and a change case, or a reasoned `NoRevision` entry, for every `Upload`, `Delete` and `Admin` route
and every storage hook.
`formattest.Evaluate` returns the same results without a `testing.T`; the kit's own tests use it to
assert that a defective format fails the check the acceptance criteria name (`formattest/testformat`
carries one named defect per check, and for check 8 also `NoGuide`, a class without guide sections,
and `GuideCredential`, a credential in a code block of the guide). Check 7 runs `formats.ServeRoute` with a handler that deletes
the content headers before and while it writes, and reads the headers at the status line. Check 9
runs each change against the in-memory `Host`, whose package writer raises `rev_seq` once per package
per transaction as `internal/packages` does, and compares the revision before and after. The fuzz
helpers `FuzzMatch` and `FuzzInspect` take a format and use its route examples and the kit's inputs
as seeds.

End-to-end, every listed client runs its tested versions against a local, a remote and a virtual
repository in `test/e2e` (FMT-002, FMT-003). The versions are fixed on the format pages.

Acceptance criteria:
- A format that registers `/{package}` and `/{scope}/{name}` without a disambiguating rule fails
  kit check 1.
- Registering the npm tarball route with kind `Volatile` instead of `Download` fails kit check 7 (it serves a stored
  item). An `Admin` route that answers with the bytes of a stored package fails the check that drives it (check 9, its
  change case), a `Volatile` search that answers with them passes (Q-ARCH-258).
- A test format whose tag-changing `Admin` route updates package rows without incrementing
  `rev_seq` fails kit check 9.
- A unit test compares each per-class format enum in `api/openapi.yaml` with the registry and fails
  on any difference.
- A test format whose guide has no section for one of its classes, or whose guide has a credential
  in a code block, fails kit check 8 (`TestR_ARCH_010_Defects`).
- A content route whose cases only refuse fails kit check 7 unless `NoContent` lists it with a reason, a restore hook
  no change case drives fails check 9, a fill hook that panics on a crafted input fails check 6, and a skip of check 6
  or 9 that the format contradicts fails (`TestQ_ARCH_281_*`, `TestR_ARCH_010_KitSkips`).

## Request paths

Each path below fixes the order in which components act. It names the page that owns each step and
introduces no rules of its own beyond that order.

### R-ARCH-011 · Request path: download from a local repository
Priority: **MVP** · Covers: ART-011, REPO-016, STOR-001

1. ServeMux matches `/api/<id>/{repoKey}/…` or `/files/{repoKey}/…`. Middleware resolves the
   client address, base URL and principal.
2. The repository comes from the in-process snapshot. A format segment that does not match the
   repository's format answers `404` with code `repository_not_found`. A key that the snapshot does
   not know is handled so that a repository is usable on every replica as soon as its creation has
   answered ([api-and-ui.md](api-and-ui.md) R-API-052, Q-API-12):
   - A key that fails INV-5's key pattern answers `404` `repository_not_found` without a query.
   - Any other key is looked up in PostgreSQL before the replica answers. Concurrent lookups of one
     key share one query.
   - If the key exists, the replica triggers one immediate change-log poll (R-ARCH-018,
     change-area step 3), coalesced with concurrent ones, and waits until the caches that poll
     invalidates are rebuilt, the grant index of [authorization.md](authorization.md) R-AUTHZ-028
     included. It then resolves and authorizes the request against them. The wait is bounded like
     a wait for the database ([ha-and-helm.md](ha-and-helm.md) R-HA-009); if the bound ends first,
     the request answers `503` with `Retry-After`.
   - If the key does not exist, the request answers `404` `repository_not_found`, and the miss is
     recorded in the negative cache of unknown repository keys. It is a registered cache
     (`repo_key_misses`, R-ARCH-018) bounded by `cache.unknown_repo_keys` per replica, with a
     `SafetyTTL` of 60 s, and every `repo_config` change evicts it. Repeated misses for one key are
     answered from it without a query, so a flood of requests for unknown keys costs at most one
     indexed query per distinct key and entry lifetime.
3. `Format.Match` selects the route, and `repository.CleanPath` canonicalises the path.
4. Authorization checks `read` on the repository and the path or package
   ([authorization.md](authorization.md)). Denial answers `401` with a challenge for an anonymous
   caller, and `403` or `404` for an authenticated one, as that page decides.
5. Include and exclude patterns apply ([repositories-and-proxy.md](repositories-and-proxy.md)).
6. What happens next depends on the route kind:
   - `Content`: the item lookup, then the format's serve-time check (`ServeCheck`, R-ARCH-008),
     then the `HEAD`, conditional, `Range` and streaming rules of [storage.md](storage.md).
     Conditional requests and `HEAD` never reach object storage.
     From M4b the item lookup also reads the block verdict of
     [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-015 in the same query, with the
     repository's policy from the snapshot as parameters. It is applied after `ServeCheck`, so a
     version the caller may not read or the format refuses keeps its `404`; a blocked version
     answers `403 vulnerability_blocked` for `GET`, `HEAD`, conditional and `Range` requests alike,
     never `304`.
   - `Computed`: R-ARCH-012.
7. The response carries the content headers (R-ARCH-007).
8. Download statistics are counted in memory and flushed in batches. The request path never writes
   them ([storage.md](storage.md)), and no request reads the buffer (R-ARCH-032). The buffer is
   flushed at shutdown (R-ARCH-024).

Acceptance criteria:
- `npm install @acme/lib@1.0.0 --registry https://trove.example.com/api/npm/npm-local/` succeeds.
- A conditional request with the item's ETag answers `304`, and the object-store GET counter does
  not move.
- With Valkey stopped and `cluster.epoch_poll_interval: 10s`, a repository created with
  `POST /api/v1/repositories` through replica A takes an `npm publish` through replica B at once
  after the creation answered ([api-and-ui.md](api-and-ui.md) R-API-052). 1,000 requests for one
  unknown key that matches INV-5's pattern cause one PostgreSQL lookup on a replica.

### R-ARCH-012 · Read-your-writes package metadata for local repositories
Priority: **MVP** · Covers: FMT-011

- Some formats have clients that read per-package metadata, for example the npm package document,
  and the NuGet registration and flat-container lists. For these formats, a local repository MUST
  render that metadata at request time from committed package rows. It MUST NOT serve a file that a
  background job rebuilds.
- After a publish has returned 2xx, the next metadata read on any replica MUST include the new
  version.
- The rendering is cached in the rendition cache under (repository, package, `rev_seq`, variant,
  base URL, encoding, rendering settings, render version). Each request reads the current `rev_seq`
  with one indexed query, so a cached rendering can never outlive a commit, whether or not a
  notification has arrived. [formats/npm.md](formats/npm.md) and [formats/nuget.md](formats/nuget.md)
  apply this rule.
  - The rendering settings are the format's `trove.yaml` keys that change the bytes and the serving
    repository's `config_version`. The render version is a constant of the format that a release
    increments whenever it changes the rendered bytes (npm: `npmRenderVersion`).
  - A cached rendering and the `ETag` computed for it use the same inputs, so a client never holds
    an `ETag` for bytes that a settings change or a new release would render differently
    ([formats/npm.md](formats/npm.md) R-NPM-013). *Status (milestone M3):* the format hashes the inputs of its ETag itself and keeps them in step
    with the rendition key by hand; a serving function that derives one ETag from the key is a helper added after the freeze (R-ARCH-025, Q-ARCH-261).
- Every change that reaches package rows through the storage hooks of R-ARCH-008 increments
  `rev_seq` in its own transaction. Those changes are: publish or upload, delete, trash restore,
  copy, move, import, retention removal and remote cache fill. The catalog lists deploy, delete,
  copy and move as triggers (FMT-011); every such operation in trove goes through these hooks, so
  none can bypass the metadata.
- Clients never upload derived metadata. No `Upload` route accepts a write to a path that `Classify` marks
  `Computed`, and `Register` refuses a route table that has one. A change that only edits the rows a computed path is
  rendered from, such as npm's dist-tag writes, is an `Admin` route and may name a computed path (Q-ARCH-232).
- Repository-wide indexes of later formats, such as a Helm `index.yaml` or Debian `Packages`,
  follow Q-ARCH-12:
  1. **Default: rendered per request.** The index is rendered from rows and cached under a
     per-repository revision. The commit transaction increments that revision only for
     repositories whose format declares a repository-wide index; that declaration arrives with the
     first such format as an optional interface (R-ARCH-025, Q-ARCH-256), and no field of
     `Descriptor` stands for it before. npm and NuGet declare none, so their repositories never
     contend on a per-repository row.
  2. **Exception: built by a job.** An index may be built by a job only when its format page shows
     one of two things: rendering per request cannot meet the P-4 bound of R-ARCH-027 at the
     format's reference repository size, or the index must be signed or stored in several
     compressed variants. The job then follows these rules:
     - it is a `coalesce` job of R-ARCH-016, one waiting per repository;
     - the previous index keeps being served until the new one commits in one transaction, so a
       client never reads a partial index;
     - the format page states a delay bound, and an acceptance test measures it under load.
  3. **No publish waits for an index.** A publish answers when its own commit is durable
     (R-ARCH-014), and never waits for an index rebuild.
- Through a virtual repository the same guarantee holds for local members (R-ARCH-015).

Acceptance criteria, against three replicas, 50 iterations, no sleep, zero failures:

```sh
npm version patch --no-git-tag-version
npm publish --registry https://trove.example.com/api/npm/npm-local/
npm view "@acme/lib@$(node -p 'require("./package.json").version')" version \
  --registry https://trove.example.com/api/npm/npm-all/
```

```sh
dotnet nuget push "Acme.Lib.1.0.$i.nupkg" --api-key "$TOKEN" \
  --source https://trove.example.com/api/nuget/nuget-local/v3/index.json
dotnet add package Acme.Lib --version "1.0.$i" \
  --source https://trove.example.com/api/nuget/nuget-all/v3/index.json
```

### R-ARCH-013 · Request path: download through a remote repository
Priority: **MVP** · Covers: RMT-001, RMT-018, RMT-019, RMT-020, RMT-029, RMT-033

Steps 1 to 5 are those of R-ARCH-011. The semantics of each later step, its settings and its
status codes are owned by [repositories-and-proxy.md](repositories-and-proxy.md). This requirement
fixes their order and the components involved.

6. `Format.Classify`. A `Volatile` path passes through without being stored: the format calls `Host.Remote().Query`, and the
   engine sends the request `RemoteAdapter.UpstreamQuery` builds through the repository's client, with the egress rules, trust
   store, redirect rules and credential policy of a fetch (steps 2 and 3 below), under the repository's fetch slots and
   circuit, and keeps the answer, as received, in the registered per-replica cache `proxy_query_results` for the period the
   format asks (at most one hour; Q-ARCH-270). A current copy is served to every caller without contacting the upstream. A
   repository that may not contact its upstream (offline, `proxy.global_offline`, the cache view, `only-if-cached`) or a caller
   who may not fill it (R-AUTHZ-008, R-EP-019) is served from that cache only, and a miss answers `formats.ErrOffline`, which
   the format turns into its own cache-only answer. Identical queries in flight on one replica wait for one contact. The
   answer is stored as neither a cache entry nor an item, and a missing resource at the upstream writes no negative entry.
   For a format whose addressing follows a discovery document (`Discoverer`), the host resolves it as step 2 below says,
   with the request's mode and populate check, before the adapter builds the request, and hands it over as
   `RemoteContext.Discovery`; a populate refusal for the document is the query's `formats.ErrOffline` and is not audited
   (Q-ARCH-282).
   *Status (milestone M3):* built (WP3.0c). No route of npm is `Volatile`; the first user is NuGet's search (R-NUGET-016),
   built 2026-11-02.
7. The proxy looks up the cache entry in PostgreSQL, and on a miss the negative entry in its
   in-process L1 and then in Valkey (R-ARCH-030). Without Valkey, only the L1 answers. The outcomes
   are owned by the proxy page; the codes shown are those it uses:

| Case | Owner's behaviour (summary) | Client sees |
|---|---|---|
| Hit (artifact, or metadata not expired) | Serve from the blob store; metadata through `Rewrite` and the rendition cache | `200`, `Cache-Status` hit |
| Metadata expired, upstream reachable | Conditional refresh within the metadata retrieval timeout | `200` |
| Refresh times out or fails | Serve the stored copy as stale | `200`, `Cache-Status` stale |
| Negative entry | No upstream call | `404` |
| Repository or system offline, nothing stored | No upstream call | `404` `not_cached_offline` |
| Assumed-offline circuit open, nothing stored | No upstream call | `503` `upstream_assumed_offline` with `Retry-After` |
| Caller may read but not populate | No upstream call ([authorization.md](authorization.md)) | `403` `populate_not_permitted` or stored content |
| Miss | Collapse, fetch, verify, store, serve (below) | `200` |
| Upstream `401`/`403` | Never forwarded as `401`/`403` | `502` `upstream_auth_failed` |
| Upstream unreachable / timeout | Counted toward the circuit | `502` `upstream_unreachable` / `504` `upstream_timeout` |
| Destination refused by the egress policy | No connection | `502` `egress_denied` |

**Vulnerability blocks, from M4b** ([vulnerability-scanning.md](vulnerability-scanning.md)
R-VULN-011, R-VULN-015). A hit reads the joined block verdict in its item lookup, as R-ARCH-011
step 6 does. On a miss of a path whose `PathInfo` names a package and a version, the core evaluates
that version after the population check and before step 1 below: a blocked version takes no flight
lock, is not fetched, not stored and answers `403`, and the evaluation of a version that is fetched
is reused by the commit of step 5, so a fill costs one evaluation query.

The order on a miss:
1. Single-flight within the replica, then the cross-replica flight lock in Valkey through
   `cluster.FlightLock` (R-ARCH-033), both per the collapse protocol of the proxy page (INV-11).
   While Valkey is unavailable, only the single-flight within the replica applies.
2. `RemoteAdapter.UpstreamRequest` builds the request. A client from `internal/outbound`
   (R-ARCH-022) sends it. For a format whose addressing follows a discovery document (`Discoverer`, R-NUGET-014),
   the engine first resolves that document as a metadata document of the repository, before the fetch takes its
   slot, parses it with `ParseDiscovery` and gives the result to every later call of the fetch as
   `RemoteContext.Discovery`; the request for the document itself carries none (Q-ARCH-268). The URL map
   (R-REPO-043) addresses metadata paths as it does artifacts.
3. `RemoteAdapter.AcceptResponse` checks status and declared type before any byte is stored or
   sent.
4. The body enters the upload pipeline of [storage.md](storage.md): spool and single-pass digests.
   The leader's own client MAY receive the body while it arrives, with its tail withheld until
   verified and, for a stored artifact, until the entry has committed
   ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-041). No client therefore
   completes an unverified download, or one whose bytes a duplicate flight's commit has refused.
   Waiters are served from the stored entry. A format that inspects what it fetches declares a fill hook
   (`FillInspector`, R-NUGET-015): for such an artifact no byte goes to any client, the leader's included, until
   `InspectFill` has accepted the spooled body after the digest check and before the commit. The leader is then
   served from the stored entry like a waiter, so a refusal answers all of them with 502 and leaves no blob, no item,
   no entry and no negative entry; the inspection becomes `CommitEvent.Inspection` (Q-ARCH-267).
5. One short transaction stores the blob reference and the cache entry.
6. The lock is released and the fill outcome is published on the flight's channel (R-ARCH-033).
   No database transaction is open, and no PostgreSQL connection is held, while bytes are
   transferred.

```mermaid
sequenceDiagram
    autonumber
    participant C as npm client
    participant A as Replica A
    participant PG as PostgreSQL
    participant VK as Valkey
    participant S3 as Object storage
    participant U as registry.npmjs.org
    C->>A: GET /api/npm/npm-remote/left-pad/-/left-pad-1.3.0.tgz
    A->>A: snapshot, Match, CleanPath, authorize read
    A->>PG: cache entry?
    PG-->>A: none
    A->>VK: GET trove:<ks>:neg:v1:<repo id>:<f>
    VK-->>A: nil
    A->>A: authorize population, single-flight join
    A->>VK: SET trove:<ks>:lock:fetch:v1:<f> <token> NX PX 30000
    VK-->>A: OK
    A->>PG: re-check cache entry
    A->>U: GET (outbound client, egress policy)
    U-->>A: 200, body
    A-->>C: body streamed, tail withheld until verified and, for a stored artifact, until the entry has committed
    A->>VK: SET … IFEQ <token> PX 30000 (every 10 s while transferring)
    A->>S3: HEAD, then PUT sha256/... if absent
    A->>PG: BEGIN, upsert cache entry, COMMIT
    A->>VK: DELIFEQ trove:<ks>:lock:fetch:v1:<f> <token>, PUBLISH trove:<ks>:fill:v1:<f> stored
    A-->>C: tail
```

Acceptance criteria (metric names per [operations-and-observability.md](operations-and-observability.md)):
- On an empty remote, `npm cache clean --force && npm install lodash@4.17.21 --registry https://trove.example.com/api/npm/npm-remote/`
  fetches from the upstream. A second run after `npm cache clean --force` makes no upstream request.
- After the metadata cache period has passed and with the upstream unreachable,
  `npm install lodash@4.17.21` still succeeds from stale metadata and the cached tarball.
- With Valkey stopped, both criteria above still pass.

### R-ARCH-014 · Request path: upload
Priority: **MVP** · Covers: ART-002, ART-008, STOR-004, STOR-019, HA-005, REPO-038

1. Route, principal and repository as in R-ARCH-011.
   - A remote repository answers `405`.
   - A virtual repository resolves to its default deployment target, or answers `405` with code
     `no_deploy_target` ([repositories-and-proxy.md](repositories-and-proxy.md)).
2. The coarse check (`Route.Action`) runs before the body is read (R-ARCH-005).
3. The format parses the request envelope, such as the npm publish document or a NuGet
   `multipart/form-data` body. It passes each binary part, with its `IngestOptions`, to
   `Host.Ingest()`. `IngestOptions.Length` is the declared length of the reader passed (0 or less:
   unknown); the spool never reads the length from the request, because a part is not the request
   body (Q-ARCH-264). Admission, spooling to the replica-local spool directory (HA-005), single-pass
   digests (INV-4) and size and digest verification follow [storage.md](storage.md).
4. `Format.Inspect` reads the spooled file and returns the declared identity and namespaced
   properties.
5. Fine-grained authorization runs on the declared identity: `deploy` on the repository, path and
   package, plus `overwrite` when the path exists (INV-9, [authorization.md](authorization.md)).
   Whether a format forbids replacing a version at all is decided on its format page (npm does).
   - The shadowing check of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 runs
     next, after this authorization and before step 6. It runs only for the first version of a
     name in a local repository that no claim covers. It comes after authorization because its
     outcome depends on whether the caller holds `manage`.
   - The check probes the package document (`PackageDocumentPath`, R-ARCH-008) on the exposed
     remote members. The probes run concurrently and outside any database transaction.
   - A refusal (`403 shadows_remote_package`, `503 shadow_check_unavailable`) ends the request
     before anything is written to object storage or PostgreSQL.
6. The commit protocol of [storage.md](storage.md) runs: the object is written unless it already
   exists, then one transaction holds the item rows, `Format.OnCommit` (package rows, `rev_seq` + 1),
   properties, the audit event and any job enqueue. `201`, or whatever the protocol expects, is sent
   only after the object is durable and the transaction has committed (STOR-019).
7. Notifications for caches follow the commit (R-ARCH-018), and the spool file is removed on every
   path.

Acceptance criteria:
- After `npm publish` and `dotnet nuget push`,
  `aws s3api head-object --bucket "$B" --key "$P/sha256/ab/cd/<hex>"` succeeds, and `<hex>` equals
  `sha256sum` of the uploaded file.
- `npm publish` with a token that only has `read` is rejected, and the object-store PUT counter does
  not move.
- An npm tarball whose `package.json` declares `@other/lib`, published to the URL for `@acme/lib`,
  is rejected with `400` before it is stored.
- With `npm-all` = [`npm-local`, `npm-remote`], a first `npm publish` of `left-pad` to `npm-local`
  by a caller with `deploy` but not `manage` answers `403` with `shadows_remote_package`. The
  object-store PUT counter does not move, and while the probe runs, `pg_stat_activity` shows no
  session of that replica in state `idle in transaction`.

### R-ARCH-015 · Request path: virtual resolution
Priority: **MVP** · Covers: REPO-033, REPO-035, REPO-036, REPO-037, REPO-038

The resolution algorithm, nesting rules, eligibility, pin rules, the merged-metadata cache and the
default deployment target are owned by [repositories-and-proxy.md](repositories-and-proxy.md). The
components act in this order:

1. `repository` reads the virtual repository's effective member list from the snapshot.
2. `repository` filters out ineligible members: by patterns, by state, and by the caller's
   permissions as `authz` decides them. Virtual repositories are never named in permission policies.
3. The format's `Strategy(path)` picks the mode:
   - `FirstMatch` walks the tiers: local members, then the remote members' caches, then the remote
     members' upstreams through R-ARCH-013. The first hit wins.
   - `Merge` collects each eligible member's document. Local documents come from
     R-ARCH-012, remote documents from R-ARCH-013, with stale serving and the circuit applying. The
     format then merges them under the per-version source rule.
4. The merged result is cached as the proxy page specifies. The cache key includes the eligible
   member set with the caller's cache-fill right on each remote member, and the endpoint
   (Q-REPO-110). The base URL is not part of the key: the merge holds no absolute link, the
   format rewrites links to the response base URL after the cache (step 5).
   - Each entry also records the `rev_seq` of every eligible local member, with `0` for a member
     that does not hold the package, and one `RevSeqs` query per request checks them. A local
     publish, including the first publish of a name that so far came only from a remote member, is
     then visible through the virtual repository on every replica without waiting for a
     notification. This MUST hold for every format (Q-ARCH-9). The check sits in the core's
     merged-metadata cache, not in the format's `Merge`, so a new format gets it without code of its
     own. [formats/npm.md](formats/npm.md) states the same rule for npm.
   - One virtual metadata period governs every format: `mergedMetadataTtlSeconds` of
     [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-020. No format has a default of
     its own. The catalog names two competing defaults (FMT-011, REPO-037); trove settles this with
     the one period. Local member changes bypass the period (above), and a merged entry never
     outlives the remote entries it was built from.
5. Links in the response point at the virtual repository. Writes go to the default deployment
   target through R-ARCH-014.

Acceptance criteria, with `npm-all` = [`npm-local`, `npm-remote`]:
- `npm view lodash versions --registry …/npm-all/` lists the upstream versions, and
  `npm view @acme/lib versions --registry …/npm-all/` lists the local ones.
- A user with `read` on `npm-remote` only cannot see `@acme/lib` through `npm-all`. A user with
  `read` on both can.
- With Valkey stopped, so that no signal reaches replica B, the next `npm view @acme/lib versions`
  through `npm-all` on replica B lists a version just published to `npm-local` on replica A. The
  same test passes for `nuget-all` with the registration index, and for the `formattest` fake
  format.
- With Valkey stopped and `local.autoClaim: false` on `npm-local` (so no new claim changes
  the member fingerprint), `npm view lodash versions --registry …/npm-all/` is cached on replica
  B, then `lodash@99.0.0` is published to `npm-local` on replica A by a caller with `manage`
  ([repositories-and-proxy.md](repositories-and-proxy.md) Q-REPO-6). The next `npm view` on B lists
  `99.0.0`.

## Background jobs and coordination

### R-ARCH-016 · Background job framework on PostgreSQL
Priority: **MVP** · Covers: HA-002, STOR-037, FMT-011, FMT-012, OPS-074

The queue backs three things: maintenance tasks and the job API
([operations-and-observability.md](operations-and-observability.md)), long-running API operations
([api-and-ui.md](api-and-ui.md) R-API-015), and internal work such as index rebuilds, webhook
delivery and data migrations. Its columns serve all three.

```sql
CREATE TYPE job_state AS ENUM ('queued', 'running', 'succeeded', 'failed', 'cancelling', 'cancelled');

CREATE TABLE job (
    id             uuid        PRIMARY KEY,                 -- UUIDv7, generated by trove
    kind           text        NOT NULL,                    -- 'gc', 'index_rebuild', 'repository_deletion', ...
    queue          text        NOT NULL DEFAULT 'default',
    args           jsonb       NOT NULL DEFAULT '{}',
    state          job_state   NOT NULL DEFAULT 'queued',
    priority       smallint    NOT NULL DEFAULT 100,        -- lower runs first
    run_at         timestamptz NOT NULL DEFAULT now(),
    attempt        integer     NOT NULL DEFAULT 0,
    max_attempts   integer     NOT NULL DEFAULT 10,
    dedup_key      text,                                    -- see enqueue modes
    dedup_mode     text        CHECK (dedup_mode IN ('exclusive', 'coalesce')),
    task           text,                                    -- maintenance task name for scheduled runs
    scheduled_for  timestamptz,                             -- schedule slot of a scheduled run
    target_kind    text,                                    -- R-API-015 operation target
    target_key     text,
    locked_by      uuid,                                    -- replica instance id (replica registry)
    locked_until   timestamptz,                             -- lease, extended by heartbeat
    progress_done  bigint,
    progress_total bigint,
    progress_unit  text,
    checkpoint     jsonb,                                   -- resumable handler state
    result         jsonb,
    error          jsonb,                                   -- problem object (R-ARCH-023)
    created_by     uuid,                                    -- principal; NULL for system and scheduled runs
    traceparent    text,                                    -- W3C trace context of the enqueuing request or job, written at enqueue
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz,
    CHECK ((dedup_key IS NULL) = (dedup_mode IS NULL))
);
CREATE INDEX job_fetch ON job (queue, priority, run_at) WHERE state = 'queued';
CREATE INDEX job_lease ON job (locked_until) WHERE state IN ('running', 'cancelling');
CREATE INDEX job_running_key ON job (dedup_key) WHERE state IN ('running', 'cancelling');
CREATE UNIQUE INDEX job_one_waiting ON job (dedup_key)
    WHERE dedup_key IS NOT NULL AND state = 'queued';
CREATE UNIQUE INDEX job_one_active ON job (dedup_key)
    WHERE dedup_mode = 'exclusive' AND state IN ('queued', 'running', 'cancelling');
CREATE UNIQUE INDEX job_task_slot ON job (task, scheduled_for) WHERE task IS NOT NULL;
```

Only replicas of role `worker` (or `all`) run the claim loop, the reaper and the scheduler
(R-ARCH-001). An `api` or `packages` replica enqueues jobs and never claims one. Workers claim jobs on the main
pool, in short transactions that work through a transaction-mode pooler (R-ARCH-019):

```sql
WITH next AS (
    SELECT j.id FROM job j
    WHERE j.state = 'queued' AND j.queue = ANY($1) AND j.kind = ANY($2) AND j.run_at <= now()
      AND (j.dedup_key IS NULL OR NOT EXISTS (
             SELECT 1 FROM job r
             WHERE r.dedup_key = j.dedup_key AND r.state IN ('running', 'cancelling')))
    ORDER BY j.priority, j.run_at
    FOR UPDATE SKIP LOCKED
    LIMIT $3)
UPDATE job SET state = 'running', locked_by = $4, locked_until = now() + $5::interval,
               attempt = job.attempt + 1, started_at = coalesce(job.started_at, now())
FROM next WHERE job.id = next.id
RETURNING job.*;
```

**Queues** (Q-ARCH-296, built in phase 6a's WP6a.2: [internal/config/jobs.go](../../internal/config/jobs.go) holds the queue
names, the slots and the two settings, [internal/jobs/worker.go](../../internal/jobs/worker.go) the claim over them). Every
kind belongs to one queue, and
`jobs.workers.<queue>` is the number of jobs of that queue one replica runs at a time:

| Queue | Kinds (table below) | Slots per replica (default, proposed) |
|---|---|---|
| `default` | API operations and work a request starts: retention runs, `vuln-evaluate`, `vuln-rescan` | 4 |
| `maintenance` | the scheduled housekeeping: `gc`, `gc-full`, sweeps, cleanups, prunes, `data_migration_*`, `secrets-reencrypt`, `vuln-findings-retention` | 1 |
| `index` | `nuget-symbol-index` and later index builds | 2 |
| `delivery` | webhook deliveries (P1) | 8 |
| `mirror` | `mirror-pass` (phase 6b); `mirror-dispatch` runs on `maintenance` (Q-ARCH-321) | 2 |
| `vuln` | the advisory sync: `vuln-sync-osv`, `vuln-sync-osv-full`, `vuln-sync-kev` | 1 |

- **Which queues a process claims.** `jobs.queues` lists the queues a worker claims; empty (the default) means every
  queue. `jobs.exclude_queues` lists queues a worker does not claim; empty (the default) excludes none. Setting both, or
  naming a queue that does not exist, or excluding every queue so that the process would claim nothing, stops start-up and
  names the key (a name listed twice is read once). The claim query's `$1` is the resulting list, and the slots of a queue
  the process does not claim are never started. The chart sets `jobs.queues` on each worker
  pool and `jobs.exclude_queues` on the main `worker` Deployment to the union of the pools' queues, so a queue never
  stays unclaimed and a queue added in a later release is claimed by the main Deployment without a value
  ([ha-and-helm.md](ha-and-helm.md) R-HA-062).
- **As an environment variable** a list-valued key is read the way the configuration loader reads every list (Q-ARCH-304):
  one JSON (or YAML flow) list, `TROVE_JOBS_QUEUES='["index","vuln"]'` or `TROVE_JOBS_EXCLUDE_QUEUES='["index"]'`; a bare
  name or a comma-separated text is refused, naming the variable. The chart renders the JSON form.
- A replica that claims only some queues still runs the reaper (it returns expired jobs of every queue) and may hold
  the `scheduler` lease (the scheduler enqueues into every queue), because both are cluster-wide and need no slot.
- **A kind that changes its queue** is enqueued into the new queue by the release that moves it, and a row an older
  release enqueued into the old queue is claimed by a process that claims the old queue and runs the kind; the slots it
  runs in are those of the row's queue (Q-ARCH-305). During the mixed-version window of a rolling upgrade
  ([ha-and-helm.md](ha-and-helm.md) R-HA-020), an older worker does not claim a queue it does not know, so a job of a
  new queue waits for a worker of the new release.

Normative rules:
- Delivery is at least once, so handlers MUST be idempotent. Long handlers, such as garbage
  collection and data migrations, checkpoint into `checkpoint` and resume from it.
- **Claim only while ready.** The claim loop claims only while the replica is ready, that is while
  every gating check of [operations-and-observability.md](operations-and-observability.md)
  R-OPS-001 passes. While it is not ready, for example while PostgreSQL does not answer or the
  replica drains, the loop claims nothing and still records watchdog progress, so liveness never
  fails because readiness does.
- **Kind-specific claim steps.** A kind may add a step that runs inside the claim transaction. The
  claim of kind `webhook.deliver` ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-042, P1) locks the subscription row with `FOR UPDATE SKIP LOCKED`, counts the
  subscription's `in_flight` deliveries whose job is still `running`, and claims only below
  `webhooks.max_in_flight_per_subscription`; otherwise the job stays `queued`. The row lock
  serialises the claims of one subscription across every replica, which a count inside the claim
  query alone would not do under `READ COMMITTED`.
- **Trace context.** An enqueue writes the `traceparent` of the request or job that enqueues it, so
  the job's spans link to the web request that started it
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-018). The column holds
  trace context only, never a credential.
- A replica claims only the job kinds it knows (`$2`). During a rolling upgrade, an N-1 replica
  never picks up a kind introduced in N.
- Enqueue modes:
  - `exclusive`: no new job while one with the same key is queued or running. A conflicting enqueue
    returns the existing job. Maintenance tasks use this mode, and their manual trigger answers
    `409` with that job ([operations-and-observability.md](operations-and-observability.md)).
  - `coalesce`: at most one job waits while another runs, so work that arrives during a run is not
    lost. Automatic index rebuilds (the job-built exception of R-ARCH-012) use this mode. Jobs with
    the same key never run at the same time.
  - A manual reindex ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-016) is
    `exclusive`, so its trigger answers `409` naming the running job. Both kinds use the key
    `index:<repository id>`, so a manual reindex and an automatic rebuild of one repository never
    run at the same time. Neither takes an advisory-lock lease: leases carry registered numbers
    only (R-ARCH-017).
  - If a coalesced job returns to `queued` (retry, reaper) while another waits, it ends
    `cancelled` with `error.code = superseded`.
- The heartbeat extends `locked_until` every `jobs.heartbeat_interval`. One statement per replica
  extends all of its running jobs, on the replica's control connection (R-ARCH-019), so a pool
  drained by busy handlers cannot make a live worker lose its jobs. A heartbeat that no longer
  matches its row cancels the handler's context. So does one that finds the state `cancelling`; the
  handler then ends the job `cancelled` at its next checkpoint.
- `locked_until` is the job's visibility timeout. A worker that dies without releasing its jobs
  holds them at most until `locked_until`; the heartbeat keeps it in the future while the handler
  runs.
- The reaper runs on every worker replica. Expired `running` jobs go back to `queued` with backoff
  (`run_at = now() + min(2^attempt s, 1 h)` ± 20 % jitter), or to `failed` once `max_attempts` is
  used up.
- **Release on shutdown.** On `SIGTERM` a worker stops claiming at once. Running handlers keep
  running until 5 s before the shutdown deadline (R-ARCH-024). A handler that has not finished by
  then is cancelled; it persists its checkpoint if it has one, and its job is released in one
  statement:

  ```sql
  UPDATE job SET state = 'queued', locked_by = NULL, locked_until = NULL,
                 attempt = attempt - 1, run_at = now()
  WHERE id = $1 AND locked_by = $2 AND state IN ('running', 'cancelling');
  ```

  A released job does not count the interrupted attempt. Another worker claims it at its next poll
  or wake-up and resumes from `checkpoint`. A job in `cancelling` ends `cancelled` instead of being
  released. A drain therefore costs a job its progress since the last checkpoint, never an attempt
  and never the job itself. A release counts in `trove_jobs_completed_total{outcome="released"}`
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-008).
  - Before its job is released, a handler cancels its outstanding object-store calls and waits for
    them to return, so it sends nothing after the release. It may then run one short statement that
    hands its own claims over: the garbage collector records step 5 for the rows whose delete
    succeeded and back-dates `state_changed_at` of its unfinished claims
    ([storage.md](storage.md) R-STOR-024). The hand-over rule of R-STOR-024 depends on both, so
    the run that continues the job finishes those rows after `storage.gc.delete_settle` instead of
    `storage.gc.handover_delay`.
- Workers wake on the signal area `jobs` (R-ARCH-018), published to Valkey after an enqueue
  commits, and also poll every `jobs.poll_interval`. While Valkey is unavailable, pick-up waits for
  the poll.
- The scheduler is a singleton under the `scheduler` lease (R-ARCH-017). Every
  `jobs.schedule_tick` it reads the schedules: `maintenance.tasks.<name>` from configuration, and
  the schedules other pages keep in PostgreSQL, such as cleanup policies in
  [storage.md](storage.md). For each one it inserts the latest due slot as
  `(task, scheduled_for)` with `ON CONFLICT DO NOTHING`. Missed slots are not caught up, and a slot
  is never enqueued twice, even across a lease hand-over.
- **A refused slot is offered again.** The scheduler keeps no record of the slots it has tried.
  The `ON CONFLICT DO NOTHING` names no conflict target, so a refusal by any unique index is
  silent. On every tick it offers each task's latest due slot again:
  - A slot refused by `job_task_slot`, the `(task, scheduled_for)` key, is already enqueued, so
    offering it again changes nothing.
  - A slot refused by an exclusive dedup key (`job_one_active`, or `job_one_waiting`) is not lost.
    This happens when another job with that key, of the same task or of a task that shares the key,
    is queued or running. The slot is offered on every later tick while it remains the task's latest
    due slot, and it is enqueued on the first tick after the blocking job has ended. Once a newer
    slot of the task is due, the newer one is offered instead.
  - [storage.md](storage.md) R-STOR-025 relies on this: the weekly `gc-full` slot that collides
    with a `gc` run is postponed until that run ends, not dropped.
- Finished jobs are deleted after `jobs.retention`. The default matches the 30-day operation
  retention of [api-and-ui.md](api-and-ui.md).
  - One exception: the job of each task's most recent enqueued slot is kept until a newer slot of
    that task is enqueued, or until the task no longer exists.
  - That row is what makes `job_task_slot` refuse the slot. Without it, a task scheduled less often
    than `jobs.retention` would have its latest due slot enqueued a second time once the row was
    deleted, for example a cleanup policy on `@monthly`.
- **Liveness watchdog** ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-001, Q-OPS-1). Each long-running loop registers with the watchdog through
  `cluster.WatchLoop(watchdog, name, bound)`, which returns a `*cluster.WatchedLoop`, under the loop
  name that the `stuck` list of `/healthz` shows. It calls `Beat()` at the end of every iteration,
  failed iterations included. Besides
  the health check loop, which R-OPS-001 watches itself, the watched loops are:
  - the claim loop `jobs.claim` and the reaper `jobs.reaper`, on replicas whose role runs workers;
  - the scheduler tick `jobs.scheduler`, while this replica holds the `scheduler` lease;
  - the lease loop `cluster.leases`, on replicas whose role runs workers (R-ARCH-017);
  - the usage fold `blob.usage_fold` of [storage.md](storage.md) R-STOR-003, on replicas whose role runs workers; it also beats while
    it waits for `storage.usage.fold_interval`, so that an interval above the watchdog bound is no stuck loop;
  - the change-log poll `cluster.changes` and the Valkey subscriber `cluster.signals`, on every
    `web` and `worker` replica (R-ARCH-018, R-ARCH-031). The subscriber records progress while
    Valkey is unreachable, so a Valkey outage never fails liveness;
  - the outbox dispatcher `webhooks.dispatch` of
    [operations-and-observability.md](operations-and-observability.md) R-OPS-035 (P1), while
    webhooks are enabled.

  Rules for these loops:
  - The claim loop records progress while every worker is busy. Waiting for a worker slot is
    outside the iteration, so a long job on the single `maintenance` worker never fails liveness.
    The wait for a `jobs` signal inside an iteration ends after `jobs.poll_interval`.
  - Every call a watched loop makes carries a context deadline shorter than
    `health.watchdog_timeout`: the claim query, the reaper's update, the slot insert and the
    dispatcher's batch. A blackholed PostgreSQL then produces failed iterations, not a stuck loop.
    A unit test per loop asserts the deadline.
  - A loop that stops on purpose, during shutdown or when its lease is lost, calls `Stop()` first
    (R-ARCH-024).
- Kinds are registered in code: `jobs.RegisterKind(kind, KindSpec{Handler, Queue, Timeout, MaxAttempts, DedupMode, Priority})`,
  and periodic tasks with `jobs.RegisterTask(name, TaskSpec{Kind, Schedule, Disabled})`.
  Each kind's semantics belong to its owning page. A kind that a maintenance task enqueues carries
  the task's name, because the job reference of
  [operations-and-observability.md](operations-and-observability.md) R-OPS-019 and
  `GET /api/v1/system/jobs?kind=<task>` select a task's runs by kind. Unless the table says
  otherwise, a task's dedup key is its name, in `exclusive` mode. A kind exists from the release
  that ships its owning requirement.

| Kind | Owner | Registration |
|---|---|---|
| `gc`, `gc-full` | [storage.md](storage.md) R-STOR-025 | Queue `maintenance`. Dedup key `gc`, shared by both kinds, `exclusive`: one collection run at a time, whichever task started it. `gc-full` checkpoints its keyset position. |
| `orphan-sweep` | [storage.md](storage.md) R-STOR-026 (P1) | Queue `maintenance`, `exclusive`; checkpoints after each prefix |
| `scrub` | [storage.md](storage.md) R-STOR-027 (P1) | Queue `maintenance`, `exclusive` |
| `remote-cache-cleanup` | [storage.md](storage.md) R-STOR-045 (P1) | Queue `maintenance`, `exclusive`; checkpoints after each batch |
| `build-retention` | [storage.md](storage.md) R-STOR-043 (P1) | Queue `default`. Dedup key `build-retention:<build name>`, `coalesce`: one run per build name at a time, and a build published during a run is covered by the waiting one |
| `retention-run` | [storage.md](storage.md) R-STOR-040 (P1) | Queue `default`. Dedup key `retention:<policy key>`, `exclusive`. `MaxAttempts: 1`, so the reaper fails a run whose worker was lost instead of re-queueing it. The kind name is fixed here, because storage.md names none. |
| `secrets-reencrypt` | R-ARCH-021 | Manual task without a schedule, `exclusive`. It refuses to start while the replicas' key rings are incompatible. |
| `data_migration_<name>` | R-ARCH-026 | Resumable through `checkpoint` |
| `nuget-symbol-index` | [formats/nuget.md](formats/nuget.md) R-NUGET-021 (P1) | Queue `index`, dedup key `nuget-symbol-index:<version id>`, `exclusive`: one job per symbol package, enqueued in the symbol push's commit and, for the backfill, by a data migration. The handler re-checks `indexed_at` under the `nuget_symbol_package` row lock, so a released, retried or reindex-started run commits nothing twice; a package that cannot be indexed ends with `index_error` instead of failing the job. |
| `data_migration_nuget_semver2` | [formats/nuget.md](formats/nuget.md) R-NUGET-004 ([Q-NUGET-93](formats/nuget.md#q-nuget-93--the-versions-stored-before-the-semver-2-classification-counted-dependency-ranges-are-recomputed-once-by-a-background-job-in-go)) | Queue `maintenance`, `coalesce` under its name. Queued by every `worker` and `all` replica at start-up while `system_state` has no key `nuget_semver2_recomputed`; it waits, queuing the next run a minute later, while a live replica runs 0.9.0 or older (R-HA-022), checkpoints its cursor after each batch of `jobs.background_migrations.batch_size` rows, and records the key when it reaches the end. |
| `cluster-change-prune` | R-ARCH-018 | Scheduled every 10 minutes, `exclusive`. Deletes a `cluster_change` row once the next row of its area is older than `cluster.change_retention`. The newest row of each area is therefore always kept, and a replica that polled within the retention still finds the row at its last applied epoch, which change-area step 5 of R-ARCH-018 checks. Idempotent. |
| `vuln-sync-osv`, `vuln-sync-osv-full` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-004 (from M4b) | Queue `vuln` (Q-ARCH-296). Dedup key `vuln-sync-osv`, shared by both kinds, `exclusive`, as for `gc` and `gc-full`. `MaxAttempts: 1`: a failed run waits for the next slot. A full sync checkpoints the index of its last committed batch and the archive's `ETag`. |
| `vuln-sync-kev` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-008 (from M4b) | Queue `vuln` (Q-ARCH-296). `exclusive` under its name; `MaxAttempts: 1`, as every sync run of R-VULN-004 |
| `vuln-evaluate` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-011 (from M4b) | Queue `default`. Dedup key `vuln-evaluate`, `coalesce`. Its work is read from tables, never from `args`: the advisories queued in `vuln_pending_evaluation` and the full evaluations requested in `vuln_source_state`, so an enqueue that a waiting job absorbs loses nothing (Q-VULN-18). Checkpoints after each batch. |
| `vuln-rescan` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-011 (from M4b) | Dedup key `vuln-rescan:<repository id>`, `exclusive`; its manual trigger answers `409` with the running job. A run that finds other scanning inputs of its repository at its end than at its start runs again before it ends (Q-VULN-18). |
| `vuln-findings-retention` | [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-014 (from M4b) | `exclusive` under its name; deletes in batches |
| `mirror-dispatch` | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-053 (phase 6b) | Queue `maintenance` (Q-ARCH-321), `exclusive` under its name, every minute. Ensures a `mirror_state` row per remote in mode `mirror` and enqueues the due passes. |
| `mirror-pass` | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-053 (phase 6b) | Queue `mirror`, which any process that claims jobs, or a pool of its own ([ha-and-helm.md](ha-and-helm.md) R-HA-062), claims. Dedup key `mirror-pass:<repository id>`, `exclusive`; `MaxAttempts: 1`, no timeout. The pass's state lives in `mirror_state` and `mirror_pending`, not in the job's checkpoint, so that a pass outlives its jobs: a release continues it in another process that claims the queue, and a lost lease, a failure or a long rate-limit wait end the job and the next dispatch continues it under the fence of Q-ARCH-287. |

Tasks of other pages, such as those in R-OPS-019's table, register their kinds by the same rule.
None of the storage kinds above takes an advisory-lock lease: their exclusion is the job key, and
their safety rests on row locks ([storage.md](storage.md) Q-STOR-13).

The `job` table has the backup export class `data` ([backup-and-restore.md](backup-and-restore.md)
R-BAK-004). A restore returns its `running` rows to `queued` without counting the attempt, as a
graceful release does, and turns its `cancelling` rows into `cancelled`, because their
cancellation was requested before the snapshot ([backup-and-restore.md](backup-and-restore.md)
R-BAK-022 owns this rule).

```yaml
jobs:
  workers:                         # concurrent jobs per replica and queue
    default: 4                     # default (proposed)
    maintenance: 1                 # default (proposed)
    index: 2                       # default (proposed)
    delivery: 8                    # default (proposed), webhooks (P1)
    mirror: 2                      # default (proposed), the passes of mirrored remotes and their dispatch (R-REPO-053)
    vuln: 1                        # default (proposed), the advisory sync (R-VULN-004, R-VULN-008; Q-ARCH-296)
  queues: []                       # default (proposed): every queue; example: [index] on a worker pool (Q-ARCH-296)
  exclude_queues: []               # default (proposed): none; example: [index, mirror] on the main worker beside two pools
  poll_interval: 2s                # default (proposed)
  lease_duration: 60s              # default (proposed)
  heartbeat_interval: 15s          # default (proposed)
  schedule_tick: 10s               # default (proposed)
  retention: 720h                  # default (proposed), 30 days
```

Acceptance criteria:
- `kill -9` on the replica running an index rebuild makes the job `queued` again within
  `lease_duration` plus one reaper pass, and another replica completes it.
- 100 coalesced enqueues of one repository's index rebuild within one second produce at most two
  executions.
- Three replicas with a task scheduled `*/5 * * * *` produce exactly 12 jobs in one hour. Deleting
  the scheduler pod in that hour produces no duplicate.
- A `gc` job is held running past the Sunday 02:00 slot of `gc-full`. The `gc-full` job for that
  slot, with `scheduled_for` = 02:00, is enqueued on the first scheduler tick after the `gc` job
  ends, and exactly once.
- With `jobs.retention: 1h` and a task scheduled `0 0 1 * *`, the job of the month's slot is still
  present a day later, and the slot has not been enqueued a second time.
- In a fault-injection build, the claim query is held by a `pg_sleep` longer than
  `health.watchdog_timeout`. Each claim ends with a deadline error, `jobs.claim` keeps recording
  progress, and `/healthz` stays `200`.
- `kubectl drain` of the node that runs a `gc-full` job, which checkpoints after every batch: the
  job is `queued` again before the pod exits, with `attempt` unchanged, and another worker finishes
  it from its checkpoint.
- With Valkey stopped, an enqueued job is picked up within `jobs.poll_interval` plus 1 s, and no
  worker replica turns unready.
- With `jobs.queues: [index]` on one worker and `jobs.exclude_queues: [index]` on another, 50 `nuget-symbol-index` jobs
  and 50 `gc` jobs run with every `nuget-symbol-index` job's `locked_by` naming the first and every `gc` job's the
  second; `jobs.queues: [index]` together with `jobs.exclude_queues: [mirror]`, and `jobs.queues: [indexing]`, each stop
  start-up and name the key.
- A full advisory sync (`vuln-sync-osv-full`) running on a worker with `jobs.workers.maintenance: 1` does not delay a
  `gc` job enqueued meanwhile: the `gc` job starts within `jobs.poll_interval` plus 1 s (the queues `vuln` and
  `maintenance` have their own slots).

### R-ARCH-017 · Session connections, leases and advisory-lock categories
Priority: **MVP** · Covers: HA-002, HA-006, HA-007

Mutual exclusion whose failure would corrupt data stays in PostgreSQL (INV-3): named leases, the
singleton work of [ha-and-helm.md](ha-and-helm.md) R-HA-006, the migration lock and the
transaction-scoped configuration locks. Since 2026-10-28 there is no per-replica coordination
session and no `LISTEN` (Q-ARCH-15), and only replicas that run singleton work hold session
connections (Q-ARCH-14).

- **Lease pool.** Each replica of role `worker` (or `all`) keeps the lease pool: up to
  `database.lease_pool_size` session-scoped connections named `trove:<replica>:lease`, opened
  against the session host. That is `database.session_host` when the main pool goes through a
  transaction-mode pooler, and the database host otherwise ([ha-and-helm.md](ha-and-helm.md)
  R-HA-058). A `web` replica opens none (R-ARCH-001).
  - The `scheduler` lease and every fenced singleton lease of R-HA-006 is a session-level advisory
    lock held on one of these connections. The singleton's writes run on the same connection
    (fencing, R-HA-006).
  - `trove migrate` and `trove backup` open their own session connection against the same endpoint
    (R-ARCH-019).
- The loop that checks the held leases is watched by liveness as `cluster.leases` (R-ARCH-016,
  [operations-and-observability.md](operations-and-observability.md) R-OPS-001).
  - One iteration pings each lease connection with `SELECT 1`, every
    `database.lease_check_interval`. It records progress whether the ping succeeded or failed.
  - Every ping and every reconnect attempt carries a deadline shorter than
    `health.watchdog_timeout`.
- When a lease connection is lost, the replica cancels the holder's context, records the loss
  (R-HA-006), and tries the lease again every `cluster.lease_retry_interval`. No cache is flushed,
  because no cache depends on this session (R-ARCH-018). Readiness is not affected.
- On shutdown, the replica releases its leases explicitly (`pg_advisory_unlock`) and closes the
  connections, so another worker takes a lease at its next retry instead of waiting for the
  session to end (R-ARCH-024).
- Every advisory lock uses the two-`int4` form, and the first key is a registered category. This
  lets the lock-diagnostics view of
  [operations-and-observability.md](operations-and-observability.md) name every lock:

| classid | Category | objid | Holder |
|---|---|---|---|
| 1 | `lease` | registered lease number (`scheduler` = 1) | a lease-pool connection of a `worker` replica; session lock with fencing ([ha-and-helm.md](ha-and-helm.md) R-HA-006) |
| 2 | `fetch` (retired) | — | Not used since 2026-10-28: the fetch lock lives in Valkey (R-ARCH-033, Q-ARCH-6). The number stays reserved and is never reused. |
| 3 | `migration` | 0 | Session locks on the session host: `trove migrate`, exclusive (R-ARCH-026, [ha-and-helm.md](ha-and-helm.md) R-HA-019); the database phase of `trove backup create`, shared `pg_advisory_lock_shared(3, 0)` on the snapshot connection ([backup-and-restore.md](backup-and-restore.md) R-BAK-005); `trove backup restore`, exclusive, until it finishes (R-BAK-021) |
| 4 | `config` | registered object type (virtual membership = 1, configuration documents = 2, project creation = 3, Bastion registration creation = 4) | `pg_advisory_xact_lock` inside configuration transactions; works through a transaction-mode pooler |

- No category hashes its `objid`. New categories are added in `internal/cluster/locks.go`, never as
  ad-hoc keys.
- The single-`bigint` forms of the advisory-lock functions are not used anywhere (Q-ARCH-7). That
  includes transaction-scoped locks such as virtual membership changes, which take
  `pg_advisory_xact_lock(4, 1)`, and the apply of configuration documents, which takes
  `pg_advisory_xact_lock(4, 2)` as the first statement of its transaction
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-030, R-OPS-031). Both
  end with their transaction.
- The migration lock excludes backups and migrations from each other
  ([ha-and-helm.md](ha-and-helm.md) R-HA-019): a backup's database phase holds it shared, while
  `trove migrate` and `trove backup restore` hold it exclusively. A restore holds it until it
  finishes, and the migration runner it calls runs on that same connection (R-ARCH-026). Whether
  two backup runs may overlap is decided elsewhere ([backup-and-restore.md](backup-and-restore.md)
  R-BAK-010), not by this lock.
- Not every serialisation needs a category. `audit_maintain_partitions()`
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-015) is called by every
  replica at start-up, by `trove migrate` and by the daily task. Concurrent calls serialise on a row
  lock of `audit_policy` (`SELECT … FOR UPDATE`) and take no advisory lock. The category test below
  is therefore unaffected.
- `cluster.FlightLock` uses no PostgreSQL lock; R-ARCH-033 specifies it.
- Session-scoped connections depend on session state, so a transaction-mode pooler breaks them.
  Before a `worker` replica takes its first lease, and before `trove migrate` or `trove backup`
  starts, the process runs `SELECT pg_backend_pid()` five times, outside a transaction, on its
  session connection. If the values differ, it exits with
  `session connection must keep session state; set database.session_host to a direct or session-mode endpoint`.
  The check is a guard, not a proof. A `web` replica runs no such check: it holds no session state,
  and its main pool may use a transaction-mode pooler (R-ARCH-019).
- Lease holders are shown in the replica registry
  ([operations-and-observability.md](operations-and-observability.md)).

```yaml
cluster:
  lease_retry_interval: 15s        # default (proposed)
# The session host is database.session_host and database.session_port (ha-and-helm.md R-HA-058).
# Until 2026-10-28 this section had cluster.coordination_url and cluster.ping_interval; both are gone.
```

Acceptance criteria:
- With three `worker` replicas,
  `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2 AND classid = 1 AND objid = 1`
  returns 1. After the holder pod is deleted, another worker holds the lease within 15 s.
- With six `web` and two `worker` replicas,
  `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:%:lease'` never exceeds
  2 × `database.lease_pool_size`, and no row names a `web` replica.
- With `database.session_host` pointing at PgBouncer in transaction mode, a `worker` replica and
  `trove migrate` exit non-zero with the message above. A `web` replica whose database URL points
  at the same PgBouncer starts and becomes ready.
- A test over `internal/meta/postgres/queries/*.sql` and every Go source fails on any
  `pg_advisory*` or `pg_try_advisory*` call with one argument, or with a first argument that is not
  a category constant of `internal/cluster/locks.go`, or with the retired category 2. It accepts
  the shared form `pg_advisory_lock_shared(3, 0)` in the backup export code and fails on a shared
  advisory-lock call anywhere else.
- After `SELECT pg_terminate_backend(<pid of the connection holding the scheduler lease>)`, the
  replica logs the lease loss, `/readyz` stays `200`, and a worker holds the `scheduler` lease again
  within `cluster.lease_retry_interval` plus 1 s.

### R-ARCH-018 · In-process caches and cross-replica invalidation
Priority: **MVP** · Covers: HA-006, RMT-026

Each owning page decides what it caches. This page provides the mechanism every cache uses, and
the rules for it. Since 2026-10-28 trove issues no `LISTEN` and no `NOTIFY` (Q-ARCH-15). A change
reaches the other replicas through a change log in PostgreSQL, which carries correctness, and
through Valkey, which carries speed.

Caches:
- A cache is registered with `cache.Register(registry, name, Options{MaxBytes|MaxEntries, Weigh, SafetyTTL, Areas, OnChange, OnFlush})`,
  where `registry` is the process's `*cache.Registry`.
  `SafetyTTL` is mandatory and bounds staleness if an invalidation is ever missed. Every cache exports
  `trove_cache_requests_total{cache,result}` and its size
  ([operations-and-observability.md](operations-and-observability.md)).
- Keys that make an entry self-validating are preferred over change messages: a source digest, a
  `rev_seq`, or an epoch counter like the one in [authorization.md](authorization.md). Change
  messages then only evict early.
- Caches live in the replica's memory. Valkey holds no shared copy of a cache that serves clients
  or decides access (R-ARCH-030, Q-ARCH-16).
- **A fill never re-inserts an invalidated value.** A cache filled from PostgreSQL on a miss reads,
  before its query, the applied epoch of every change area that invalidates it
  (`cluster.Applied(area)`), and inserts the result only if those epochs are unchanged after the
  query. Otherwise the result serves the current request and is not cached. Without this check, a
  fill whose query ran before a change committed, and whose insert lands after the poll applied
  that change, would keep the old value until `SafetyTTL`. For the token cache that would be a
  revoked token accepted for up to `cache_ttl` on one replica. A Valkey message needs no such check:
  it never advances the applied epoch, so the poll that follows evicts again.

Areas. Every source of invalidation is an *area*, registered in `internal/cluster/areas.go` with a
name matching `^[a-z][a-z0-9_]{1,31}$`, its owner and one of two classes. `Host.Notify` and
`cluster.Notify` attach a notification to the writing transaction, and the class decides how it
leaves:

- **Change areas** carry changes whose propagation an owning page bounds: configuration, policies,
  revocations, settings.
  1. Inside the writing transaction, `cluster.Notify` runs:

     ```sql
     UPDATE cluster_epoch SET epoch = epoch + 1, changed_at = now() WHERE area = $1 RETURNING epoch;
     INSERT INTO cluster_change (area, epoch, payload) VALUES ($1, $2, $3);
     ```

     The `UPDATE` holds the area's row until commit. A second writer of the same area gets its
     epoch only after the first has committed
     ([UPDATE under READ COMMITTED](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)),
     so each area's epochs become visible in order and without gaps. A rollback leaves neither the
     row nor the epoch.

     `meta.InTx` runs the two statements last, just before `COMMIT`, whatever point of the
     transaction called `cluster.Notify`. The row is therefore held for the commit only, and writers
     of one area serialize on their commits, not on their whole transactions. A change area is
     written by administrative actions and revocations, never once per package download, upload
     or cache fill; a change that client traffic produces at that rate uses a signal area and a
     self-validating key instead. Three writers of
     [repositories-and-proxy.md](repositories-and-proxy.md) fit this rule at rates of their own:
     - the auto claim of a name's first publish writes `claims` once per name and repository,
       never per upload, and it needs the 2 s bound of the change log for dependency-confusion
       protection (R-REPO-023);
     - the repository-delete job writes `claims` for each batch that removes claims (R-REPO-013);
     - the fold job writes `cache_invalidation` when it deletes folded invalidation rows
       (R-REPO-045).

     Both job writes happen per batch, not per client request. From M4b the advisory sync of
     [vulnerability-scanning.md](vulnerability-scanning.md) writes `vuln_data` once per sync run
     that changed records, never per request (R-VULN-004, R-VULN-027).

     A transaction that notifies several change areas updates their `cluster_epoch` rows in
     area-name order, so two such transactions cannot deadlock. A project repository assignment
     (P1), for example, notifies both `authz` and `repo_config`
     ([authorization.md](authorization.md) R-AUTHZ-028).
  2. After commit, the writer applies its own invalidation at once and publishes
     `{"a": <area>, "e": <epoch>, "v": <payload version>, "p": <payload>}` on the Valkey channel
     `trove:<ks>:inv:v1` (R-ARCH-030).
  3. Every `web` and `worker` replica polls the change log every `cluster.epoch_poll_interval`,
     on its control connection (R-ARCH-019), so a request pool that is exhausted under load cannot
     stall the poll.
     - One query reads `epoch` and `changed_at` of every area from `cluster_epoch`. The poll
       exposes both values per area to the area's owners, which compare them as
       [authorization.md](authorization.md) R-AUTHZ-028 does with its index label.
     - For each area whose epoch is higher than the replica's last applied epoch, a second query
       reads the `cluster_change` row at the last applied epoch and the rows above it, in epoch
       order. Before it applies the rows above, the replica checks the row at its last applied
       epoch against step 5.
     - The replica applies the rows and advances the area's last applied epoch. With it, it keeps
       the `created_at` of the row it applied last, which equals the `changed_at` that the same
       transaction wrote. Only this poll advances the last applied epoch.
     - Besides the interval, a request may trigger one immediate poll: R-ARCH-011 step 2 does so
       for a repository key that PostgreSQL knows and the replica's snapshot does not. Concurrent
       triggers, and a trigger that meets a running poll, coalesce into one poll on the control
       connection. The triggering request waits until the caches that poll invalidates are
       rebuilt.
  4. A message from Valkey is applied at once, as an early eviction, and never advances the last
     applied epoch. A lost, delayed, duplicated or forged message can therefore neither suppress nor
     reorder a change: the poll applies every committed change from PostgreSQL within its interval.
  5. The replica flushes the area's caches through `OnFlush` and takes the database's current
     epoch of the area, with its `changed_at`, as its new last applied epoch, even when that epoch
     is lower, in each of these cases:
     - the rows above the last applied epoch, or the row at it, are no longer there, because
       `cluster-change-prune` (R-ARCH-016) removed them
       (`trove_cache_flushes_total{reason="change_log_gap"}`). The replica cannot tell from a
       missing row whether its history still holds, so a missing row at the last applied epoch is
       handled like an epoch that went backwards;
     - a row carries a payload version the replica does not know (a rolling upgrade,
       [ha-and-helm.md](ha-and-helm.md) R-HA-020; `reason="unknown_payload"`);
     - **the epoch went backwards** (`reason="epoch_regressed"`). After a PostgreSQL failover that
       lost committed transactions (asynchronous replication, [ha-and-helm.md](ha-and-helm.md)
       R-HA-013), or after a point-in-time restore, the area's history in PostgreSQL can differ
       from the one the replica applied ([ha-and-helm.md](ha-and-helm.md) R-HA-004). It counts as
       gone backwards when the area's epoch in `cluster_epoch` is lower than the last applied
       epoch; when it is equal but its `changed_at` differs from the one the replica applied; or
       when it is higher but the `cluster_change` row at the last applied epoch carries a different
       `created_at`.

     Taking the lower epoch as the starting point keeps later changes from being skipped as
     already applied. Comparing `changed_at` and `created_at` catches a *reused* epoch: later
     writes can raise an area to the same number before the replica's first poll after the
     failover, so the epoch never looks lower, yet it stands for different changes. Without the
     check, a token revocation (area `auth`) committed at a reused epoch number would be skipped
     until `SafetyTTL`. `changed_at` is written by the same statement that raises the epoch, and two
     transactions separated by a failover never share it. A replica started after a restore starts
     from the current epochs anyway (R-ARCH-024).

  A committed change is in force on every replica within `cluster.epoch_poll_interval` plus one
  query and the cache rebuild, whether Valkey is available or not. With Valkey it is typically in
  force within milliseconds.
- **Signal areas** carry wake-ups and early evictions that client traffic produces: job enqueues,
  remote metadata refreshes, the outbox wake-up, the delivery-log wake-up and the yield of a mirror's background fetches to
  clients (`mirror_yield`, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055). They write nothing to
  PostgreSQL.
  - After commit, the signal goes to the replica's coalescer; a rollback drops it. The coalescer
    publishes at most one message per area per `cluster.signal_coalesce_window` on
    `trove:<ks>:sig:v1:<area>`, batching the payloads.
  - Delivery is at most once ([Pub/Sub](https://valkey.io/topics/pubsub/)). Every consumer of a
    signal area MUST stay correct without the signal, through self-validating keys, `SafetyTTL` or
    its own polling (`jobs.poll_interval`, the lock poll of R-ARCH-033). While Valkey is
    unavailable, signals are dropped and counted in `trove_cluster_signals_dropped_total{area}`.
  - The completion of a collapsed fetch is not an area. It uses the flight's own channel
    (R-ARCH-033).

Rules:
- A payload is versioned JSON under 1 KiB. A change that would need more becomes a coarse flush of
  the affected cache. A payload never carries a secret, or a value from which a live credential can
  be derived: it carries IDs, and hashes of credentials that have just ended.
- Only `internal/cluster` writes `cluster_epoch` and `cluster_change` and publishes to Valkey. No code
  issues `LISTEN`, `NOTIFY` or `pg_notify`.
- The `invalidation` readiness check passes while the last successful change-log poll is younger
  than `cluster.epoch_max_staleness`. It depends on PostgreSQL only; Valkey never gates readiness
  (INV-12). Because the poll has its own connection, the check fails when PostgreSQL does not
  answer, not when the replica is merely busy; under overload, replicas do not leave the Service
  one after another and push their load onto the rest.
  - **No check undercuts the PostgreSQL window** ([ha-and-helm.md](ha-and-helm.md) R-HA-008).
    `cluster.epoch_max_staleness` MUST be at least the readiness window of the `database` check
    (about 15 s), so `invalidation` never turns a replica unready sooner than `database` does. A
    PostgreSQL switchover of a few seconds, which requests ride out in their retry loop (R-HA-009),
    then never empties the Service through `invalidation`. The default is 20 s.
  - Code paths that must fail closed sooner read the change-log freshness directly instead of
    relying on readiness:

    ```go
    package cluster

    // Fresh reports whether the last successful change-log poll is younger than
    // cluster.epoch_max_staleness: the condition of the `invalidation` check.
    func (c *Changes) Fresh() bool

    // FreshWithin reports whether the replica's last completed change-log poll started
    // less than d ago, measured on the replica's monotonic clock. A poll reads every change
    // committed before it started (READ COMMITTED), so a true result proves that every change
    // older than d is applied.
    func (c *Changes) FreshWithin(d time.Duration) bool
    ```

    [authorization.md](authorization.md) R-AUTHZ-028 bounds the staleness of its grant index this
    way with `authz.cache.max_staleness`, and the freshness gate of
    [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023 calls
    `(*cluster.Changes).FreshWithin(auth.revocation_bound)`.
- After the Valkey subscriber reconnects, the replica runs one change-log poll at once. Caches of
  signal areas are not flushed, because they never rely on signals for correctness.
- Metrics ([operations-and-observability.md](operations-and-observability.md) R-OPS-008):
  `trove_cluster_poll_last_success_timestamp_seconds`, whose age the `invalidation` check compares
  with `cluster.epoch_max_staleness`; `trove_cluster_changes_applied_total{area,source}`, with
  `source` = `poll` for changes applied from the change log, `message` for early evictions from
  Valkey and `local` for changes the replica applies to itself; `trove_cluster_changes_published_total{area}`,
  `trove_cluster_changes_dropped_total{area}` and `trove_cluster_poll_errors_total` for the change
  messages and the polls; `trove_cluster_signals_published_total{area}` and
  `trove_cluster_signals_dropped_total{area}` for signals; and `trove_cache_flushes_total{cache,reason}` for
  the flushes of change-area step 5.
- A migration inserts the `cluster_epoch` row of a change area in the release that introduces the
  area (an expand step).
- Backup export classes ([backup-and-restore.md](backup-and-restore.md) R-BAK-004): `cluster_change`
  is `transient`, and `cluster_epoch` is `data`, so the area rows that migrations created survive a
  restore. `cluster_epoch.epoch` is registered with `meta.RegisterRevisionColumn` (R-BAK-022). The
  offset is harmless: no replica outlives a restore, and every replica reads the current epochs at
  start-up (R-ARCH-024).

```sql
CREATE TABLE cluster_epoch (
    area       text        PRIMARY KEY,           -- a change area of internal/cluster/areas.go
    epoch      bigint      NOT NULL DEFAULT 0,
    changed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cluster_change (
    area       text        NOT NULL REFERENCES cluster_epoch (area),
    epoch      bigint      NOT NULL,
    payload    jsonb       NOT NULL,              -- versioned, under 1 KiB
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (area, epoch)
);
```

The owning page may move one of its areas to the other class. Until it does, this table holds:

| Area | Owner | Class | Purpose |
|---|---|---|---|
| `jobs` | this page | signal | Wake the workers of a queue; the payload names the queues |
| `cache` | this page | change | System settings, and flush requests: the installation-wide cache flush of [operations-and-observability.md](operations-and-observability.md) R-OPS-047 (P1) writes the flush entries, counted as `trove_cache_flushes_total{reason="admin"}` |
| `auth` | [identity-and-authentication.md](identity-and-authentication.md) | change | Token revocation and rotation, session end, principal status (disable, enable, delete), membership changes, runtime auth settings, and the break-glass rotation end. The payload carries the token ID with the hash of the ended credential, the session ID or the principal ID (R-AUTH-023). |
| `authz` | [authorization.md](authorization.md) | change | Policies (entries, selectors, patterns), role bindings, projects, project administrators and project repository assignments. Its epoch is the authorization epoch of R-AUTHZ-028 and of the `Trove-Authz-Epoch` header (R-AUTHZ-032); the grant index also rebuilds on `repo_config`. |
| `repo_config`, `claims`, `cache_invalidation` | [repositories-and-proxy.md](repositories-and-proxy.md) | change | Repository snapshot, claims and pin rules, cache invalidation (RMT-026). `claims` is also written by the auto claim of a first publish and by the repository-delete job, `cache_invalidation` by the fold job (change-area step 1 above). |
| `upstream_state` | [repositories-and-proxy.md](repositories-and-proxy.md) | change | Circuit state, the global offline switch (R-REPO-031) and the reload of one repository's gone packages (R-REPO-029). Moved from the hot-path class: its transitions are rare, and R-REPO-029's 2 s bound then holds without Valkey. |
| `metadata_changed` | [repositories-and-proxy.md](repositories-and-proxy.md) | signal | Early merged-metadata eviction after a remote refresh; R-ARCH-015's `rev_seq` check keeps local members exact without it |
| `mirror_yield` | [repositories-and-proxy.md](repositories-and-proxy.md) (R-REPO-055) | signal | A client fetch of a remote repository waits for a fetch slot: every process that runs background fetches of it (a mirror's pass) pre-empts those no follower of its own joined and starts none for 30 s. The payload is the repository's numeric id; a replica raises it at most once a second per repository. A lost signal costs only the early yield: the pass keeps within its share (Q-ARCH-286, Q-ARCH-309). |
| `system_state`, `support`, `webhooks` | [operations-and-observability.md](operations-and-observability.md) | change | Read-only mode, support bundles, webhook configuration |
| `events` | [operations-and-observability.md](operations-and-observability.md) | signal | Outbox dispatch wake-up (R-OPS-035, P1); outbox rows are written in publish and cache-fill transactions, and a five-second poll covers lost signals |
| `deliveries` | [operations-and-observability.md](operations-and-observability.md) | signal | Wakes the delivery-log follow stream (R-OPS-044, P1); the payload carries delivery record IDs. The stream's own poll covers lost signals ([api-and-ui.md](api-and-ui.md) R-API-051). |
| `ci_issuer_keys`, `ci_mappings` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | change | Key-set reload, written only when a stored key set changes (R-CI-003). Issuer and mapping reload, written by every issuer, mapping and delegation write (R-CI-002, R-CI-009). A mapping disable revokes. The exchange does not depend on the timing of either: it re-reads the key row on an unknown `kid` (throttled per replica), and it re-reads the issuer row and checks its `version` and `mappings_version` (R-CI-007). |
| `vuln_data` | [vulnerability-scanning.md](vulnerability-scanning.md) | change | From M4b: the state of the advisory data per source and ecosystem (complete, stale, data epoch), written once per sync run that changed records. It invalidates the caches `vuln_source_state` (`SafetyTTL` 60 s) and `vuln_nuget_page`, whose key carries the `vuln_data`, `claims` and `repo_config` epochs (R-VULN-018, R-VULN-027). |

The channel `trove_proxy_fetch` of the earlier design no longer exists: fill completion uses the
flight channel of R-ARCH-033.

```yaml
cluster:
  epoch_poll_interval: 1s          # default (proposed)
  epoch_max_staleness: 20s         # default (proposed): the `invalidation` readiness check; at least the readiness window of ha-and-helm.md R-HA-008 (about 15 s)
  change_retention: 1h             # default (proposed): cluster-change-prune
  signal_coalesce_window: 50ms     # default (proposed)
cache:
  rendition_bytes: 268435456       # default (proposed), 256 MiB per replica, rendered package documents
  snapshot_safety_ttl: 5m          # default (proposed), full reload of the repository snapshot
  unknown_repo_keys: 10000         # default (proposed), per replica: negative cache of unknown repository keys (R-ARCH-011 step 2)
```

Acceptance criteria:
- A repository's include pattern is changed through the API on replica A. Within 1 s (p99 over 100
  trials), a request on replica B honours it. With Valkey stopped, the same holds within
  `cluster.epoch_poll_interval` plus 1 s.
- With Valkey stopped, a token revoked through replica A is rejected by replica B within 2 s.
- A fault-injection build drops every second message on `trove:<ks>:inv:v1` and injects forged messages
  with random epochs and payloads. Over 1,000 policy changes, no replica serves a decision older
  than `cluster.epoch_poll_interval` plus 1 s after the change committed, and the forged messages
  cause evictions only.
- 1,000 transactions per second across three replicas, each enqueueing a job, cause at most
  3 × 20 `PUBLISH` per second on `trove:<ks>:sig:v1:jobs` (Valkey `INFO commandstats`), and
  `pg_stat_statements` shows no `NOTIFY`.
- A test over `internal/meta/postgres/queries/*.sql` and every Go source fails on `LISTEN`,
  `NOTIFY` or `pg_notify`, and on a write to `cluster_epoch` or `cluster_change` outside
  `internal/cluster`.
- A replica is paused for longer than `cluster.change_retention` while changes are made. After it
  resumes, it flushes the affected areas (`trove_cache_flushes_total{reason="change_log_gap"}`) and
  serves current data.
- A test lowers the epoch of area `repo_config` in `cluster_epoch` below the epoch the replicas
  have applied, as a failover with lost transactions or a point-in-time restore would. Every
  replica flushes the area's caches (`trove_cache_flushes_total{reason="epoch_regressed"}`) and
  takes the lower epoch as its starting point, and the next change of the area takes effect on
  every replica within `cluster.epoch_poll_interval` plus 1 s, with Valkey stopped.
- A reused epoch in area `auth`: token T1 is revoked through replica A as `auth` epoch `e`, and B
  applies it. Valkey is then stopped and B's poll paused by a test hook. As a failover that lost
  the commit would, the test clears T1's revocation, deletes that `cluster_change` row and resets
  the `auth` row of `cluster_epoch` to `e − 1` with its earlier `changed_at`. Token T2, which B
  holds in its token cache, is then revoked through A, which commits as epoch `e` again. When the
  hook is lifted, B flushes its `auth` caches within `cluster.epoch_poll_interval` plus 1 s
  (`trove_cache_flushes_total{reason="epoch_regressed"}` rises), and B answers a request with T2
  with `401`.
- With `cluster.epoch_max_staleness` at its default, a PostgreSQL switchover of 10 s makes no
  replica fail the `invalidation` check, and `/readyz` answers `200` on every replica throughout
  ([ha-and-helm.md](ha-and-helm.md) R-HA-008, R-HA-059 variant C).
- A test build pauses a token-cache fill on replica B between its query and its insert. The token is
  revoked through replica A, B's poll applies the change, and the fill resumes. The next request
  with the token on B answers `401`.
- A test transaction calls `cluster.Notify` on area `cache` and then runs for another 2 s. A second
  writer of `cache` that starts within those 2 s commits within 100 ms, without waiting for the
  first.

### R-ARCH-019 · Database access, connection naming and budget
Priority: **MVP** · Covers: HA-062

- The driver is pgx v5 with its native API (Q-ARCH-2). `pgxpool` is the main pool. `database/sql`
  and `pgx/v5/stdlib` are not used anywhere, the migration runner included (Q-ARCH-3).
- Static queries, meaning every query whose SQL text does not depend on the request, are `.sql`
  files compiled by sqlc into `internal/meta/postgres/q`.
- Dynamic list and search queries are built in `internal/meta/postgres/listq`. Those are the API
  lists of [api-and-ui.md](api-and-ui.md) with optional filters, sort keys and cursors. The
  builder's rules:
  - values are always bound parameters;
  - column and sort identifiers come from a per-endpoint allowlist;
  - no other package concatenates SQL.
- Connection classes (Q-ARCH-14):

| Class (`application_name` suffix) | Opened by | Endpoint | What runs on it | Connections |
|---|---|---|---|---|
| `main` | `web`, `worker`, `all` | the database host ([ha-and-helm.md](ha-and-helm.md)); direct, session-mode or transaction-mode pooler. With a transaction-mode pooler, the control connection uses the control endpoint (below). | The request pool: every request-path transaction, job claims and completions, job handlers, the grant-index build. The control connection: the change-log poll, the `database` check, the registry heartbeat, the job heartbeats. | `database.pool.max_conns` per replica, one of them the control connection |
| `lease` | `worker`, `all` | the session host: `database.session_host` with a transaction-mode pooler, the database host otherwise (R-HA-058); direct or session-mode only | Session-level advisory locks and the fenced writes of singleton work (R-ARCH-017, R-HA-006) | `database.lease_pool_size` per `worker` replica |
| `migrate` | `trove migrate` | the session host | The migration lock and the migration steps (R-ARCH-026) | 1 |
| `backup` | `trove backup` | the session host | The control, snapshot and export connections of a backup and the writes of a restore ([backup-and-restore.md](backup-and-restore.md) R-BAK-005, R-BAK-021) | as that page sets |

- **Main-pool rules.** Work on the main pool keeps no state beyond its own transaction, so it works
  directly, through a session-mode pooler and through a transaction-mode pooler.
  - Allowed: `SET LOCAL`, `pg_advisory_xact_lock`, `SELECT … FOR UPDATE SKIP LOCKED`, `COPY`
    inside a transaction, protocol-level prepared statements.
  - Forbidden: session-level `SET` and `RESET`, `LISTEN`, session-level advisory locks, temporary
    tables, `WITH HOLD` cursors, SQL `PREPARE` (the PgBouncer feature table lists all of them as
    never working in transaction pooling).
  - pgx's default exec mode, `cache_statement`, uses protocol-level prepared statements. PgBouncer
    supports them in transaction mode when `max_prepared_statements` is non-zero (default 200).
    Behind a pooler without that support, the main pool's URL sets
    `default_query_exec_mode=exec`.
- **No connection is held across a wait.** A request or a job holds a main-pool connection for one
  query or one transaction only. It never holds one while it reads from or writes to a client, the
  object store, an upstream or Valkey. The number of concurrent transfers per replica (P-10: 500) is
  therefore independent of `database.pool.max_conns`, and a slow client never occupies a database
  connection.
- **Control connection.** One connection of each replica's main class is reserved, as a pool of
  size 1 next to the request pool, which gets `database.pool.max_conns` − 1. It carries the loops
  that readiness and job ownership depend on: the change-log poll (R-ARCH-018), the `database`
  readiness check ([operations-and-observability.md](operations-and-observability.md) R-OPS-001),
  the replica-registry heartbeat and, on `worker` replicas, the job heartbeat, one statement for
  all of the replica's running jobs (R-ARCH-016).
  - Without the reservation, a request pool exhausted under overload for longer than
    `cluster.epoch_max_staleness` would fail the `invalidation` check on one replica after another,
    and each replica leaving the Service would push its load onto the rest. Busy job handlers would
    likewise delay heartbeats until a live worker lost its jobs.
  - **Its own server pool behind a pooler** ([ha-and-helm.md](ha-and-helm.md) R-HA-058, Q-HA-20).
    The client-side reservation does not reach the server through a shared transaction-mode
    pooler: when every server connection of the request pooler is busy, the control queries of
    every replica would queue behind request transactions. With a transaction-mode pooler, the
    control connection therefore connects to `database.control_host`, `database.control_port` and
    `database.control_name`: a server pool that request traffic cannot exhaust, such as a second
    pooler or a database entry with its own pool size. Empty keys mean the main endpoint, which is
    correct without a pooler and with a session-mode pooler, where the control connection has a
    server backend of its own.
  - Only these loops run on it. Work of an owning page never does: the grant-index build of
    [authorization.md](authorization.md) R-AUTHZ-028 and its epoch reads run on the request pool,
    so authorization work never delays the poll, the checks or the heartbeats.
  - The control connection is an ordinary `main`-class connection: transaction-scoped, pooler-safe
    and named `trove:<replica>:main`. The replica's total stays `database.pool.max_conns`, so the
    budget of R-HA-016 is unchanged. `database.pool.max_conns` must be at least 2; start-up refuses
    a smaller value and names the key.
- Every connection sets `application_name` to `trove:<replica>:<class>`. `<replica>` is the pod
  name; `trove migrate` and `trove backup` use their Job's pod name. PgBouncer tracks
  `application_name` as a startup parameter, so `pg_stat_activity` shows it on the server
  connection while that connection serves trove. The lock diagnostics of
  [operations-and-observability.md](operations-and-observability.md) depend on it.
- Transactions run at `READ COMMITTED`.
  - Two exceptions run at `REPEATABLE READ READ ONLY`. The first is the backup's snapshot and
    export transactions on the `backup` class ([backup-and-restore.md](backup-and-restore.md)
    R-BAK-005). The second is the grant-index build of [authorization.md](authorization.md)
    R-AUTHZ-028: one transaction on the request pool of the `main` class, never on the control
    connection. It is transaction-scoped and therefore pooler-safe, and it runs with a context
    deadline and `SET LOCAL statement_timeout` equal to `authz.cache.max_staleness`. A read-only
    transaction never meets a serialization failure.
  - `meta.InTx` retries SQLSTATE `40001` and `40P01` up to three times.
  - **Every read of the main pool on a request path retries like a transaction.** A handler that reads
    without a transaction uses `DB.Read`, never the raw pool. It repeats the read on a failover error
    (SQLSTATE `57P01`, `57P02`, `57P03`, `25006`, `53300`, class `08`, a refused or timed-out connection)
    with the jittered backoff of `meta.InTx` until `database.retry.max_elapsed` or the request deadline,
    destroys the connection that failed so the retry finds the new primary, and ends as `503`
    `database_unavailable` (R-ARCH-023) when the bound is used up. Any other error is returned unchanged.
    A read through `DB.Read` is not one snapshot: a handler that needs several statements to agree uses
    `meta.InTx`. A read that is best effort by nature (the installation facts of `GET /api/v1/system/info`)
    bounds the whole call with its own short context, so it never waits out the retry bound.
  - No outbound HTTP, no object-store call and no Valkey call runs inside a transaction.
  - Request-path transactions set `SET LOCAL statement_timeout` (5 s, proposed). Jobs set their own
    timeout.
- **Connection count** (INV-12). A `web` replica opens `main` only. A `worker` replica opens `main`
  and `lease`. There is no listener session and no lock pool (Q-ARCH-14, Q-ARCH-6).
  - With direct connections, the server sees Σ replicas × `database.pool.max_conns` +
    Σ `worker` replicas × `database.lease_pool_size` + the one-off connections. That grows with the
    number of `web` replicas, and the chart's budget check keeps it below `max_connections`
    ([ha-and-helm.md](ha-and-helm.md) R-HA-016).
  - With a transaction-mode pooler in front of the `main` class, the server sees the pooler's server
    pool, which does not depend on the replica count, plus the worker leases and the one-off
    connections. This is the supported way to scale `web` beyond the direct budget, and the
    recommended production setup when `web` autoscales.
  - [ha-and-helm.md](ha-and-helm.md) R-HA-016 owns the formula, its keys and the chart check.
- Transaction-mode poolers are supported for the `main` class from the MVP on. The session classes
  refuse to run behind them (R-ARCH-017). Statement-mode poolers are not supported for any class:
  they refuse transactions of more than one statement, and trove's write paths run exactly such
  transactions (R-ARCH-009).

Acceptance criteria:
- Three `web` and two `worker` replicas run with the database URL pointing at PgBouncer
  (`pool_mode = transaction`, `max_prepared_statements = 200`, `default_pool_size = 40`) and with
  `database.session_host` direct. The client suite and the job suite pass. While `web` scales
  from 3 to 12 replicas under the reference load, the server connections of the `main` class never
  exceed 40.
- Under a 500-connection load test,
  `SELECT application_name, count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:%' GROUP BY 1`
  shows only the classes `main`, `lease`, `migrate` and `backup`, never more connections than the
  formula allows, and no `lease` connection of a `web` replica.
- A test over `internal/meta/postgres/queries/*.sql` and every Go source fails:
  - on a session-level `SET` or `RESET` (anything but `SET LOCAL`) and on a session-level
    advisory-lock function, outside the code of the session classes: the lease code of
    `internal/cluster`, the migration runner, and the backup export and restore load
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-005, R-BAK-021);
  - on `CREATE TEMP` outside the restore load, which fills `CycleBreak` columns from a temporary
    table (R-BAK-021);
  - on an isolation level other than `READ COMMITTED` (`REPEATABLE READ`, `SERIALIZABLE`) outside
    the backup snapshot and export code and the grant-index build of
    [authorization.md](authorization.md) R-AUTHZ-028;
  - on `LISTEN` and `WITH HOLD` anywhere.
- A load test exhausts the request pool of every `web` replica for 30 s. `/readyz` stays `200` on
  every replica throughout, and no job of a `worker` replica returns to `queued` because a
  heartbeat was late.
- In the pooler profile of [ha-and-helm.md](ha-and-helm.md) R-HA-058, a test build holds every
  server connection of the request pooler busy for 60 s. `/readyz` stays `200` on every replica
  throughout, and `pg_stat_activity` shows each replica's `trove:<replica>:main` control queries
  on the control pooler's server connections.
- A request-path query held up by an injected `pg_sleep(10)` returns `503` with code
  `database_timeout` after 5 s.
- A list request whose `sort` names a column outside the endpoint's allowlist answers `400` and
  sends no SQL. A unit test fails if any package other than `listq` builds SQL with `fmt.Sprintf`
  or with string concatenation.

## Shared ephemeral state (Valkey)

**Named exception for the catalog-driven SQL builder.** `internal/meta/postgres/blobrefs.go` builds SQL
from catalog table names by string formatting, for the discovery of R-STOR-024 rule 3. It is the one such
place outside the `listq` builder. The source rule of R-ARCH-032 names that file and no other.

### R-ARCH-030 · Valkey usage map
Priority: **MVP** · Covers: HA-006, HA-007

Valkey holds shared ephemeral state (INV-3). This requirement assigns every cross-replica mechanism
to PostgreSQL or Valkey, and fixes the keys, their lifetimes and what happens when Valkey is
unavailable or loses its data. Two rules decide where a mechanism lives:
1. State whose loss would cost correctness or data lives in PostgreSQL. That includes every mutual
   exclusion whose failure would corrupt data (INV-3).
2. No content, metadata or credential that trove serves to a client, and no positive
   authentication or authorization result, is read from Valkey (Q-ARCH-16). A value read from
   Valkey can at most make trove refuse: a negative entry answers `404` for a remote path, and a
   counter answers `429`. A throttle exemption marker can only lift the throttle for one
   credential, which then costs one PostgreSQL lookup; it never accepts a credential. Valkey's
   content can therefore cost availability, never access or integrity (H-49).

The keys are shown with the default prefix `trove:`.

| Mechanism | Lives in | Keys and channels | Lifetime | Valkey unavailable | Valkey lost its data (failover, flush, restart) | Specified in |
|---|---|---|---|---|---|---|
| Collapsing of remote fetches | Valkey, behind the in-process single-flight | `trove:<ks>:lock:fetch:v1:<f>`; channel `trove:<ks>:fill:v1:<f>` | `cluster.flight_lock_ttl` (30 s), extended while the fetch runs | Single-flight per replica only: at most one upstream fetch per replica and flight | A second replica may fetch too; content addressing and the entry upsert rules make the duplicate harmless | R-ARCH-033, [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 |
| Negative cache of remote repositories | Valkey, behind a bounded per-replica L1 (`proxy.negative_l1_entries`, 100,000 entries per replica by default) | `trove:<ks>:neg:v1:<repo id>:<f>` → `{"s": 404 or 410, "c": <time the upstream request was sent, Unix ms, replica clock>}`; written by the flight's holder before it releases the flight; a malformed value is ignored and removed with a compare-and-delete (Q-ARCH-197) | the repository's `notFoundTtlSeconds` | L1 only: more upstream requests for missing paths | More upstream requests. An entry that an older primary brings back is ignored when a newer invalidation covers it | R-REPO-028, R-REPO-045 |
| Rate-limit counters with cluster scope | Valkey | `trove:<ks>:rl:v1:<limiter>:<b>:<window>` | 2 × the window | Per-replica buckets at the configured values divided by the live replicas of the role | Counters restart: at most one extra window of budget | R-ARCH-034 |
| Throttle exemption markers of recently validated credentials | Valkey | `trove:<ks>:tokseen:v1:<t>` | `auth.rate_limit.recent_token_window` (10 min) | Only the replica's own cache exempts | Colleagues behind a throttled address may get `429` until they are revalidated | R-AUTH-038 |
| Change messages, the fast path of change areas | Valkey Pub/Sub; correctness lies in the PostgreSQL change log | channel `trove:<ks>:inv:v1` | none (Pub/Sub) | Changes apply at the next change-log poll | Lost messages: as unavailable | R-ARCH-018 |
| Signals: job wake-ups, remote metadata refreshes, outbox wake-up | Valkey Pub/Sub only | channel `trove:<ks>:sig:v1:<area>` | none | Dropped; consumers poll | Lost; consumers poll | R-ARCH-018 |
| Valkey health probe | Valkey | `trove:<ks>:probe:v1:<instance uid>` | 30 s | `valkey` reported as degraded | Recreated by the next probe | R-ARCH-031 |
| Cache entries and stale copies of remote repositories, upstream circuit state | PostgreSQL and S3 | — | — | Unaffected | Unaffected | R-REPO-026 to R-REPO-029 |
| Rendered package documents, merged metadata | In-process only | — | Self-validating keys (R-ARCH-012, R-ARCH-015) | Unaffected | Unaffected | R-ARCH-018 |
| Token, session, permission, CI-mapping and issuer-key caches | In-process only; Valkey carries only their change messages | — | The owner's `SafetyTTL` | Unaffected: the change-log poll keeps the bound | Unaffected | R-AUTH-023, R-AUTHZ-028, R-CI-003 |
| Browser login flows, npm web login and device login polling, UI sessions | PostgreSQL | — | — | Unaffected | Unaffected | R-AUTH-006, R-AUTH-010, R-AUTH-034, R-AUTH-036 |
| Job queue, scheduler slots, named and singleton leases, migration lock, configuration locks | PostgreSQL | — | — | Unaffected | Unaffected | R-ARCH-016, R-ARCH-017 |
| Replica registry, break-glass lockout and rotation hold (R-AUTH-014, R-AUTH-038), multi-request upload sessions (P1), download statistics, CI subject-token replay set, its restore cut-off and CI issuance records | PostgreSQL | — | — | Unaffected | Unaffected | R-OPS-003, R-AUTH-014, R-AUTH-038, R-STOR-011, [storage.md](storage.md), [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-018, R-CI-020 |

`<ks>` is the keyspace: the first 12 hex digits of `system_state.keyspace_id` after its dashes are removed
([backup-and-restore.md](backup-and-restore.md) R-BAK-022), read from PostgreSQL at start-up. `<f>`
is the lowercase hex SHA-256 of `repository id ‖ 0x00 ‖ canonical path ‖ 0x00 ‖ variant` (the id in decimal digits), the
flight key of R-REPO-026. `<b>` is the lowercase hex of the first 16 bytes of the SHA-256 of the
limiter's bucket key. `<t>` is the lowercase hex of the first 16 bytes of
SHA-256(`"tokseen"` ‖ 0x00 ‖ `secret_sha256`), where `secret_sha256` is the stored hash of the
credential ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038).

Normative rules:
- **Prefix.** Every key and every channel starts with `valkey.key_prefix` (default `trove:`, which
  must end in `:`), followed by the keyspace and `:`. Installations that share one Valkey use
  different prefixes and different ACL users (R-ARCH-031).
- **Keyspace.** A restore gives the installation a new keyspace ID (R-BAK-022). The restored
  installation therefore never reads a key or message of the installation it was restored from,
  even on the same Valkey, and nothing needs to be flushed; the old keys expire by their TTLs. A
  replica reads the keyspace once, at start-up, after any restore has finished (R-ARCH-024), so it
  never changes keyspace while it runs.
- **Versioned areas.** The segment after the mechanism name (`v1`) is the encoding version. A
  release that changes the encoding of a value or payload uses a new version segment, so N and N-1
  replicas never misread each other's entries during a rolling upgrade; they only miss them.
- **No raw identifiers.** Keys hold IDs and hashes only: never package names, paths, principal
  names, token values or client addresses.
- **Every key expires.** Every write sets an expiry. A key without one is a defect.
- **Readers validate.** A value read from Valkey is checked before use, and a malformed one is
  ignored and deleted.
  - A negative entry counts only while `c`, the time its upstream request was sent by the
    replica clock, lies between now minus `notFoundTtlSeconds` and now plus 5 s, and no covering
    invalidation of scope `negative` or `all` was created later than `c` minus 5 s (R-REPO-028,
    R-REPO-045). The 5 s are the clock allowance, applied in the invalidation's favour. The key's
    own expiry is not trusted. Invalidation rows of scope `negative` or `all` are therefore kept
    86,405 s, the largest `notFoundTtlSeconds` plus the allowance, so readers can apply them to
    every entry that could still count.
  - A rate-limit counter cannot block a bucket for longer than its window, because each window has
    its own key.
- **Small values.** No value or payload exceeds 1 KiB.
- **One gateway.** Only `internal/cluster` talks to Valkey. Key families and channel families are registered in
  `internal/cluster/valkey_keys.go`, and a key is formed only by `(*Valkey).Key(family, parts...)`.
  A part matches `^[A-Za-z0-9_-]{1,128}$`, so a dotted name such as `auth.credential_failures` is
  written with underscores. Other packages use `FlightLock`, the negative-cache store, the limiter
  and `Notify`.
- **No persistence assumed.** trove treats every Valkey restart as a loss of all its data. AOF and
  RDB persistence are the operator's choice. trove works under every `maxmemory-policy`: an
  eviction counts as an expiry. A Valkey that refuses a write (`noeviction` at its memory limit, a
  demoted primary) counts as unavailable for that call.
- **Primary only.** Every command goes to the primary. Replicas are never read, because their state
  lags behind the primary's (asynchronous replication).
- **Not backed up.** A backup holds no Valkey data (INV-19). A restore needs no access to Valkey:
  the new keyspace starts empty ([backup-and-restore.md](backup-and-restore.md) R-BAK-022).

Acceptance criteria:
- After the integration suite, `valkey-cli --scan` lists only keys that start with the prefix, and
  `TTL` returns a positive value for each of them. The integration stack's Valkey is shared with the Bastion's suites, whose
  keys are a Bastion's own keyspace `trove-bastion:<prefix>:` ([bastion.md](bastion.md) R-BAS-034); they are admitted by the scan
  and must carry a TTL too.
- With Valkey stopped for the whole e2e client suite, the suite passes, and every replica stays
  ready with `"degraded":["valkey"]`.
- `valkey-cli FLUSHALL` in the middle of the reference load test makes no client request fail.
- A negative entry for `left-pad` is written into Valkey with a creation time one hour in the
  future, and another with a creation time older than an invalidation of `left-pad`. Both are
  ignored, and the request reaches the upstream.

### R-ARCH-031 · Valkey client, Sentinel discovery, TLS, ACL and degradation
Priority: **MVP** · Covers: trove-only

[ha-and-helm.md](ha-and-helm.md) R-HA-056 owns the keys of the `valkey` configuration section, the
supported topologies and the server settings; R-HA-057 owns the degraded-mode contract, the circuit
breaker and the metrics. This requirement fixes the client that implements them and its behaviour
inside the process.

- **Library.** valkey-go (Q-ARCH-13), pinned once in `go.mod`. Server-assisted client-side caching
  is switched off (`DisableCache`), because trove's caches are its own (R-ARCH-018). trove uses no
  blocking command, so the client opens no blocking-pool connection. Only `internal/cluster` imports
  it (R-ARCH-002).
- **Servers.** Valkey 9.0 and every later minor line (Q-ARCH-13). While trove is in development,
  the integration tier tests the newest patch of the newest line only (9.1 today) and no older
  line is guaranteed (owner, 2026-10-29); the version check below still refuses every server
  older than 9.0, because trove needs `DELIFEQ`. At connect, trove
  reads the server version from the `HELLO` reply. Below 9.0 it logs
  `valkey <version> is not supported; trove requires 9.0 or later` and treats Valkey as
  unavailable. Valkey cluster mode and other servers are not supported.
- **Sentinel protocol.** The client follows R-HA-056's discovery steps. It asks the Sentinels of
  `valkey.sentinel.addresses` for `valkey.sentinel.primary_name`, checks `ROLE` on the instance it
  connects to, and resolves again after every reconnection, timeout, closed connection and
  `READONLY` error. In addition, it reconciles with the Sentinels every
  `valkey.sentinel.refresh_interval` (valkey-go's `TopologyRefreshInterval`). Trove's own Sentinel
  loop probes every configured Sentinel once per interval on a short connection that it closes
  again, so the replica holds one connection to the current Sentinel and no more. A demoted primary can
  keep its connections open, and valkey-go's documentation states that a client which missed
  `+switch-master` then stays bound to it; the reconciliation bounds that case.
- **Subscriptions follow the primary.** After every primary change, whether learned from
  `+switch-master`, from a failed call or from the reconciliation, the Pub/Sub connection is
  re-established on the new primary and re-subscribes every channel the replica listens on. The
  replica then runs one change-log poll (R-ARCH-018), and every flight-lock waiter re-checks its key
  (R-ARCH-033). A subscriber left on a demoted primary would otherwise hear nothing until its
  connection breaks. Correctness never depends on these messages, but P-13 and P-14 do.
- **Primary only.** Every command goes to the primary. Replicas are never read (R-ARCH-030).
- **Connections.** Per replica: four pipelined connections to the primary (valkey-go's default for
  Sentinel clients), one Pub/Sub connection and one connection to the current Sentinel, each named
  `trove:<pod name>:<purpose>` (R-HA-056). Commands are pipelined automatically, so the count does
  not grow with the request rate.
- **Calls.** Every call carries `valkey.command_timeout` as its deadline, and no call runs inside a
  PostgreSQL transaction (R-ARCH-019). Every call passes the replica's circuit breaker of R-HA-057
  (`valkey.circuit_breaker`). While the breaker is open, every call returns `cluster.ErrUnavailable`
  at once, without network I/O.
- **Degradation.** `cluster.ErrUnavailable` is never returned to a client and never fails a
  request; each mechanism degrades as R-ARCH-030 states. Valkey being unreachable never makes a
  replica unready (INV-12): `/readyz` lists `valkey` under `degraded`, and start-up does not wait
  for Valkey (R-ARCH-024). A configuration error found at a replica's first connection (an unknown
  primary name, rejected credentials, a rejected certificate) is handled as
  [ha-and-helm.md](ha-and-helm.md) Q-HA-13 decides.
- **TLS.** `valkey.tls` applies to the data nodes and, with `valkey.sentinel.tls`, to the Sentinels.
  Certificates are verified as `valkey.tls.mode` says, `verify-full` by default; `disable` needs
  `valkey.allow_insecure` (R-HA-056).
- **Credentials.** The username and password files of the data nodes and of the Sentinels
  (R-HA-056) are read in `AuthCredentialsFn` on every connection attempt, and connections are
  recycled after `valkey.max_conn_lifetime`. A rotated Secret therefore takes effect without a
  restart. Passwords never appear in the configuration file, in logs or in metrics.
- **Least privilege.** trove needs only these commands, on keys and channels under its prefix:
  `GET`, `SET`, `DELIFEQ`, `INCR`, `EXPIRE`, `MULTI`, `EXEC`, `DISCARD`, `PUBLISH`,
  `SUBSCRIBE`, `UNSUBSCRIBE`, `SUNSUBSCRIBE`, `PUNSUBSCRIBE`, `PING`, `HELLO`, `ROLE`,
  `CLIENT SETNAME` and `CLIENT SETINFO`. `DISCARD`, `SUNSUBSCRIBE` and `PUNSUBSCRIBE` are sent by
  the client library when it resets a Pub/Sub connection; trove issues no sharded or pattern
  subscription. It runs no scripts. An example ACL rule, which the integration tier and the dev
  profile use:

  ```text
  user trove on >… resetkeys ~trove:* resetchannels &trove:* -@all +get +set +delifeq +incr +expire +multi +exec +publish +subscribe +unsubscribe +sunsubscribe +punsubscribe +discard +ping +hello +role +client|setname +client|setinfo
  ```

- **Metrics.** Those of R-HA-057 (`trove_valkey_available`, `trove_valkey_circuit_open`,
  `trove_valkey_primary_changes_total`, `trove_valkey_errors_total{kind}`), plus
  `trove_valkey_command_duration_seconds{command}`, per mechanism that ran its fallback
  `trove_valkey_degraded_total{mechanism}`, `trove_valkey_pubsub_subscribed` (1 while the Pub/Sub
  connection is subscribed on the current primary) and `trove_valkey_cache_lookups_total{cache,result}`
  for lookups in Valkey after an L1 miss ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-045).
- **Logs.** The client logs one record per transition of the `valkey` check, `WARN` when it turns
  degraded and `INFO` when it recovers, and one `INFO` record per primary change with the old and
  the new address. It never logs one record per failed call; failed calls are counted in
  `trove_valkey_errors_total{kind}`.
- **Configuration.** One key joins R-HA-056's `valkey` block (cross-page):

  ```yaml
  valkey:
    sentinel:
      refresh_interval: 5s         # default (proposed): periodic reconciliation with the Sentinels
  ```

Acceptance criteria:
- Against a Valkey 8.1 server, trove logs the unsupported-version message and runs degraded.
- With the example ACL rule in place, the integration suite passes and `ACL LOG` shows no denial.
- A failover is forced while the Sentinels' Pub/Sub traffic to trove is blocked, so that no
  `+switch-master` arrives and the old primary keeps its connections. Every replica writes to the
  new primary within `valkey.sentinel.refresh_interval` plus 1 s. Within the same bound,
  `CLIENT LIST TYPE pubsub` on the new primary shows every replica's Pub/Sub connection. With
  `cluster.epoch_poll_interval` raised to 60 s for this test, a change made through replica A is
  then applied on replica B within 1 s, which only the Valkey message can achieve.
- During a client-suite run, `CLIENT LIST` on the primary shows at most six connections per
  replica, whatever the request rate.

### R-ARCH-033 · Cross-replica request collapsing through Valkey (`FlightLock`)
Priority: **MVP** · Covers: HA-007

[repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 owns the collapse protocol and
its settings. This requirement specifies `cluster.FlightLock`, the only way the proxy reaches the
cross-replica lock (INV-11, Q-ARCH-6).

```go
package cluster

// FlightKey is <f> of R-ARCH-030; the only way to form one is NewFlightKey(repoID, path, variant).
type FlightKey struct{ /* the 64 hex digits */ }

// Outcome is "stored" (an entry or a negative entry exists now) or "failed" (the holder gave
// up). Wait alone also returns "gone": the lock key is absent and no outcome was heard.
type Outcome string

type FlightLock interface {
	// Acquire tries once. ok is false while another holder has the flight.
	// An error that wraps ErrUnavailable means: go on without the cross-replica lock.
	Acquire(ctx context.Context, f FlightKey) (h *FlightHold, ok bool, err error)
	// Wait returns when the holder publishes an outcome, when the lock is gone,
	// or when ctx ends (then with ctx's error). The caller then repeats the proxy's checks
	// and may call Acquire again.
	Wait(ctx context.Context, f FlightKey) (Outcome, error)
}

// Release deletes the lock if this hold still owns it and publishes the outcome, with the
// leader's traceparent when it has one (empty: none). It may be called again and then does nothing.
func (h *FlightHold) Release(ctx context.Context, o Outcome, traceparent string) error

// Lost reports that an extension found the key gone or owned by another token.
func (h *FlightHold) Lost() bool
```

- **Acquire.** `SET trove:<ks>:lock:fetch:v1:<f> <token> NX PX <cluster.flight_lock_ttl>`. The token is
  `<instance uid>.<16 random bytes, base64url>`, unique for each acquisition.
- **Hold.** While the holder fetches, verifies and commits, the hold extends the lock every third
  of the TTL with `SET <key> <token> IFEQ <token> PX <ttl>`. If an extension finds the key gone or
  owned by another token, the holder carries on, because its fetch stays correct. It stops
  extending, counts `trove_flight_lock_lost_total`, and never deletes the other holder's key.
- **Release.** After the entry has committed, after an upstream `404` or `410` has been recorded
  as a negative entry, or after the fetch has failed, the hold runs `DELIFEQ <key> <token>` and then
  `PUBLISH trove:<ks>:fill:v1:<f> <stored|failed>`, in that order, so a woken waiter finds the lock gone. A recorded
  `404` or `410` is `stored`: the outcome says that an entry or a negative entry exists now (Q-ARCH-195). Both run even when the initiating client is gone,
  since R-REPO-026 runs the fetch detached. The outcome message may also carry the leader's
  `traceparent` after a space (`stored 00-…`), still under 1 KiB, so waiters on other replicas can add a span link
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-018). On shutdown, a
  fetch still running 5 s before the deadline is cancelled and released with `failed`
  (R-ARCH-024 step 4; [storage.md](storage.md) R-STOR-004 step 8).
- **Wait.** The waiter first subscribes to `trove:<ks>:fill:v1:<f>`, then checks that the lock key still
  exists, so a release between its failed `Acquire` and the subscription is not missed. It then
  waits for the outcome, for `proxy.lock_poll_interval_ms` (after which it checks the key again), for
  the re-subscription of the Pub/Sub connection after a primary change or a broken connection
  (R-ARCH-031; the key is checked again then too), or for its context deadline
  (`proxy.fetch_wait_timeout_seconds`). The waiters of one flight on a replica share one subscription,
  so 50 waiters cost one `SUBSCRIBE`. When `Wait` returns, the caller
  repeats the proxy's checks (the entry, the circuit and the negative entry;
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 step 4) and answers from them,
  or calls `Acquire` again.
- **Expiry instead of session end.** A holder that dies without releasing (process killed, node
  lost) frees the flight when the key expires, after at most `cluster.flight_lock_ttl`. A holder
  that shuts down gracefully releases at once.
- **Unavailable.** When the breaker is open or a call fails, `Acquire` and `Wait` return an error
  that wraps `ErrUnavailable` at once (Q-ARCH-196). A call that fails during the hold is retried at the next
  extension; the lock may expire meanwhile, and the next call that reaches Valkey then finds it gone. The proxy then fetches under its in-process single-flight only and
  counts `trove_flight_lock_bypass_total{reason="valkey_unavailable"}`.
- **Bypass reasons.** `trove_flight_lock_bypass_total{reason}` counts every fetch that goes on
  without the cross-replica lock:
  - `valkey_unavailable`: above;
  - `wait_timeout`: the wait bound `proxy.fetch_wait_timeout_seconds` ended without a valid entry,
    and the proxy fetches without the lock (R-REPO-026 step 4);
  - `draining`: the replica is shutting down and no other replica holds the flight.
- **Draining.** A replica whose readiness is `draining` never calls `FlightLock.Acquire`, so it
  leads no new cross-replica flight (R-ARCH-024 shutdown step 1). It may still call `Wait` and join
  a flight that another replica holds ([repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-026, Q-REPO-7).
- **Lost lock.** If a failover loses the key, a second replica may acquire the flight while the
  first still fetches. Both fetches are correct: blob commits are idempotent by content address,
  and the entry upsert keeps the newer metadata and refuses a different digest for an artifact
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-041). A leader withholds its
  client's tail until its entry has committed (R-REPO-041), so the losing side of a duplicate fetch
  aborts before that client completes. Correctness never relies on mutual exclusion here (INV-11).
- **No PostgreSQL connection is held** while acquiring, waiting, transferring or extending. The lock
  uses none.
- **Clock.** Valkey measures the TTL; no replica clock decides expiry.

```yaml
cluster:
  flight_lock_ttl: 30s             # default (proposed); extended every ttl/3 while the fetch runs
```

Acceptance criteria (two replicas, a counting stub upstream that delays every response by 2 s):
- 100 concurrent requests for one uncached tarball, 50 per replica: the stub records exactly one
  `GET`, and all 100 responses are `200` with identical bytes. Meanwhile `pg_stat_activity` shows
  no trove connection in state `idle in transaction`, and `pg_locks` shows no advisory lock.
- The stub streams a 200 MiB artifact slowly, and the holder's pod is killed with `SIGKILL`
  mid-transfer. A waiter on the other replica acquires the flight within `cluster.flight_lock_ttl`
  plus `proxy.lock_poll_interval_ms`, fetches, and completes every waiting request. No entry for a
  partial body exists at any time.
- The same with a graceful `kubectl delete pod`: the waiter acquires within
  `proxy.lock_poll_interval_ms`, because the holder released with `failed`.
- With Valkey stopped, all 100 requests succeed, and the stub records at most one `GET` per
  replica.
- A Sentinel failover is forced while the holder transfers, and the lock key is deleted on the new
  primary to simulate the lost write. The stub records at most two `GET`s, the responses are
  identical, and exactly one cache entry exists.
- A test over `internal/proxy` fails on an import of the Valkey client and on any `pg_advisory*`
  call.

### R-ARCH-034 · Cluster-wide rate-limit counters
Priority: **MVP** · Covers: trove-only

The owning pages define the limit classes, their bucket keys and their values
([api-and-ui.md](api-and-ui.md) R-API-016,
[identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038,
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-021). This requirement provides the limiter they use
(Q-ARCH-17).

- A limiter is registered with `LimiterSet.NewLimiter(name, scope, rate, burst)` (Q-ARCH-38). Scope `cluster`
  counts across all replicas. Scope `replica` counts in the replica's memory and is meant for limits
  that protect the replica itself.
- **Cluster scope** uses a sliding-window counter in Valkey.
  - The window is `W = burst ÷ rate` seconds, at least 1 s, and at most `burst` requests are
    admitted per window.
  - A check is one round trip: `MULTI`, `INCR` of the current window's key,
    `EXPIRE <key> <2W> NX`, `GET` of the previous window's key, `EXEC`.
  - A request is admitted when `previous × (1 − elapsed ÷ W) + current ≤ burst`. Otherwise it is
    refused, and the owning page renders the `429` with its `Retry-After`.
  - The window of a request, and its `elapsed`, are those of the time the request was received, not of
    the check: a request that a stage before the limiter held (the token lookup during a database
    switchover) is charged to the window it arrived in ([ha-and-helm.md](ha-and-helm.md) Q-HA-66). The
    per-replica fallback decides at the time of the check.
  - The `INCR` runs before the decision, so refused requests count too. A caller that keeps sending
    while refused stays refused until its rate drops below the limit; a caller that honours
    `Retry-After` gets the configured rate.
  - No script is used.
- **Count-only form.** Some limits count failures, not requests: a request is checked before it
  is processed, and only a failed one is counted afterwards. For these the limiter offers two
  calls with the same sliding-window arithmetic and the same per-replica fallback:

  ```go
  // Exceeded pipelines a GET of the current and the previous window's key, without INCR, and
  // reports whether previous × (1 − elapsed ÷ W) + current reaches burst: a limit of n failures
  // admits n and refuses the next (Q-AUTH-20).
  func (l *Limiter) Exceeded(ctx context.Context, bucket string) bool
  // Count records one failure: MULTI, INCR of the current window's key, EXPIRE <key> <2W> NX, EXEC.
  func (l *Limiter) Count(ctx context.Context, bucket string)
  ```

  [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038 registers
  `auth.credential_failures`, `auth.flow_starts` and `auth.device_code_failures`, each with burst
  = the configured value and a window of one minute, or `auth.cli_login.ttl` for wrong device
  codes; the first and the last use the count-only form.
- **Fallback.** While Valkey is unavailable, a cluster-scoped limiter answers from an in-memory token
  bucket with rate `rate ÷ n` and burst `max(1, burst ÷ n)`. `n` is the number of live replicas that
  share the replica's client traffic in the replica registry (the replicas of its role in its pool, and every `all` replica; Q-ARCH-303), at least 1, refreshed
  with the heartbeat by one component that every limiter of the client listener shares
  ([Q-ARCH-147](#q-arch-147--one-replica-count-per-pool-for-every-limiter-of-the-client-listener));
  a limiter may still count its own set (`LimiterSet.NewLimiterShared`,
  [Q-ARCH-45](#q-arch-45--a-limiter-may-divide-its-fallback-share-by-its-own-count)). The
  cluster-wide budget thus stays close to the configured one. This is the per-replica degradation
  INV-12 names; `trove_ratelimit_fallback{limiter}` is 1 while it lasts.
- A limiter never fails a request because Valkey failed, and its check waits at most
  `valkey.command_timeout` (R-ARCH-031).
- Every decision counts in `trove_ratelimit_decisions_total{limiter,mode,outcome}`, with `mode` =
  `cluster`, `fallback` or `replica` and `outcome` = `admitted` or `refused`
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-008).
- Further cluster-scoped limiters are registered by their owners, for example `webhook_test`, 10
  test deliveries per minute per principal
  ([operations-and-observability.md](operations-and-observability.md) R-OPS-043, P1).
- Bucket keys are hashed (R-ARCH-030). A counter lost in a failover restarts, which admits at most
  one extra window of requests.
- The cluster-wide break-glass lockout stays in PostgreSQL
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038). It guards a
  credential, so it must survive a loss of Valkey's data.

Acceptance criteria:
- Three `web` replicas run one limiter with cluster scope, rate 50 and burst 200. 1,000 requests
  with one token, sent round robin over the replicas within 2 s, are admitted at most 300 times.
  The same test against one replica gives the same bound.
- With Valkey stopped, the same test is admitted at most 300 times as well, and
  `trove_ratelimit_fallback` is 1 on every replica.
- With 1 s of latency injected in front of Valkey, the limiter adds at most `valkey.command_timeout`
  to at most `valkey.circuit_breaker.failure_threshold` checks per replica, and nothing once the breaker is
  open.

## Configuration, secrets and outbound connections

### R-ARCH-035 · TLS on trove's listeners and reloadable TLS material
Priority: **MVP** · Covers: HA-018

Every listener of trove serves TLS when `server.tls` is set, and every TLS material that trove presents
or trusts can be replaced without a restart (INV-20, [ADR-127](../decisions.md#adr-127--transport-encryption-on-every-connection-troves-listeners-included)).

Listener side:
- `server.tls.certificates` is a list of `{cert_file, key_file}` pairs of PEM files, leaf first. The first
  pair is the default; SNI picks the pair whose names hold the requested name exactly, else the first pair a
  wildcard covers that the client accepts, else the default. An empty list is plain HTTP. The pairs serve the
  client listener; the ops listener takes them through `server.ops_tls` (below).
- `server.ops_tls.inherit` (default `true`) gives the ops listener the client listener's pairs, `min_version` and
  `reload_interval`, and no client authentication, because the kubelet's probes present no certificate. With
  `inherit: false` the ops listener has its own `certificates`, `client_ca_file`, `client_auth`, `min_version`
  and `reload_interval` (Q-ARCH-33); with `inherit: true` a setting of the block other than `inherit` is refused.
- `server.tls.client_auth` is `none` (default: no certificate is requested), `verify_if_given` (a presented
  certificate must chain to `client_ca_file`; a client without one is admitted) or `require` (a client without a
  verifying certificate is refused in the handshake). A `client_auth` other than `none` needs `client_ca_file`,
  and `client_ca_file` with `none` is refused. Mutual TLS lets a proxy connect to trove (Q-ARCH-32); it is off by default.
- Minimum TLS 1.2 (default) or 1.3, TLS 1.3 preferred. TLS 1.2 is offered with ECDHE key exchange and AES-GCM or
  ChaCha20-Poly1305 only. HTTP/2 is negotiated over TLS with ALPN (`h2`, `http/1.1`).
- A reloader checks both files every `server.tls.reload_interval` (default `10s`). It compares the
  content, not the file times, and a change is loaded into a new `tls.Config.GetCertificate` lookup, so
  the new certificate serves every new handshake at once. Connections already open keep the certificate
  they negotiated until they close (Q-ARCH-31).
- A changed pair or bundle is compared by content, not by time, once per pass. A new pair that does not load
  (unreadable, an incomplete PEM block, a key that does not match its certificate, an expired leaf or one that is
  not valid yet) keeps the
  previous content in service, counts once per distinct content in
  `trove_tls_reload_errors_total{kind,listener,name}`, and logs an error with the file names and the reason.
  Readiness does not change, and no other piece of the material is affected.
- A pair that is missing or broken at start does not stop the process: the listener binds, the `tls` readiness
  check holds the first readiness until every listener's material has loaded, then clears without a restart. The
  client listener and an endpoint listener refuse handshakes until their pair loads. The ops listener answers with a
  temporary certificate meanwhile (in memory, ECDSA P-256, valid one hour, CN = the instance, never written to disk),
  so the kubelet's HTTPS probes, which do not verify, reach `/healthz` (200) and `/readyz` (503, check `tls`) instead
  of failing and restarting the container; `trove_tls_temporary_certificate{listener="ops"}` is 1 while it serves
  ([Q-ARCH-36](#q-arch-36--the-ops-listener-before-its-certificate-loads)). A listener that asks for client
  certificates without a `client_ca_file` is refused at construction. Client-side material that is missing at start (PostgreSQL, Valkey, outbound CA bundles)
  fails the construction of that client, and the process ends ([tls.md](../developer/tls.md)).
- The expiry of every served piece is `trove_tls_certificate_expiry_timestamp_seconds{kind,listener,name}`: the
  notAfter of a pair's leaf, or the earliest notAfter of a CA bundle, so an expiry alert needs no log parsing.
  `trove_tls_reloads_total{kind,listener,name}` counts the swaps after the first load. The label values are
  fixed in [Q-EP-14](endpoints.md#q-ep-14--labels-of-the-tls-series-and-of-the-ops-series) for endpoint listeners.
- **Endpoint listeners.** The listener of every further endpoint uses the same reloader, bound and
  failure rule for each pair of its certificate set, and chooses among its own pairs by the TLS
  server name, wildcard certificates included; with no pair of its own it serves the pair of
  `server.tls`. The TLS series of an endpoint listener carry `listener="client:<endpoint>"`
  ([endpoints.md](endpoints.md) R-EP-004, [Q-EP-14](endpoints.md#q-ep-14--labels-of-the-tls-series-and-of-the-ops-series)).

Client side: every TLS material trove presents or trusts is re-read without a restart, and an error
keeps the last good material in service:
- PostgreSQL root CA, client certificate and key: read for each new connection
  ([ha-and-helm.md](ha-and-helm.md) R-HA-015).
- Valkey and Sentinel CA, client certificate and key: read for each new handshake
  ([ha-and-helm.md](ha-and-helm.md) R-HA-056).
- Outbound CA bundles, and the object store's `storage.s3.ca_file` (supported since M0, R-STOR-030): reloaded
  within the bound of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-035 and R-ARCH-022.

Acceptance criteria:
- Rotating the listener certificate under a constant k6 load (E0.15) produces no failed request, the new
  certificate is served within `server.tls.reload_interval` + 1 s, and the process start time does not change.
- A new pair whose key does not match its certificate keeps the old pair in service, increments
  `trove_tls_reload_errors_total`, and logs an error; the next valid pair is served.
- A connection opened before a rotation keeps the old certificate until it closes; a connection opened after
  the bound sees the new certificate.
- With `client_ca_file` set, a client certificate that does not chain to it is refused in the TLS handshake,
  before any HTTP request is read.
- A rotated PostgreSQL or Valkey client certificate is used by the next new connection or handshake, with no
  restart and no failed request on the replica.

### R-ARCH-020 · Configuration schema in code and the file/database split
Priority: **MVP** · Covers: OPS-040

The file location, precedence, environment mapping, strict validation, secret companions
(`<name>_file`) and `trove admin config validate` are specified in
[operations-and-observability.md](operations-and-observability.md). This page fixes how the
schema is built and which data belongs in the file.

- The schema is one Go struct tree in `internal/config`. Each key's tags give its YAML name
  (snake_case), default, validation and secret flag. The environment variable names, the JSON
  Schema and the redacted effective-configuration view are generated from that tree. No key exists
  outside it.
- File or database: if a value is needed before the database is reachable, or is specific to a
  deployment, it goes in the file. That covers the process role, listeners, database, Valkey and
  object-store connections, OIDC provider connection, base URL, pools, caches, jobs, logging and
  schedules of maintenance tasks, and endpoints with their hosts, certificates and surfaces. If a value must be identical on every replica and can change without a restart, it goes in
  PostgreSQL. That covers repositories, permission policies, the exposure rules of endpoints, users,
  groups, tokens, CI identity mappings and runtime settings (INV-15,
  [endpoints.md](endpoints.md) Q-EP-2).
- Sections owned by this page: `process` (role), `server` (listeners, timeouts, shutdown), `jobs`,
  `cluster`, `cache`, `secrets`. The `valkey` section belongs to [ha-and-helm.md](ha-and-helm.md)
  R-HA-056, and R-ARCH-031 implements it.

Acceptance criteria:
- A unit test fails if two keys derive the same environment variable name, or if a struct field has
  no YAML tag.
- `trove admin config validate` lists every key of this page with its source.

### R-ARCH-021 · Encryption of stored secrets and the stored-secret key
Priority: **MVP** · Covers: HA-016, IAM-045

Secrets that trove stores in PostgreSQL to reach other systems are sealed by `internal/secrets`
before they are written. Examples are remote upstream credentials and webhook secrets (P1).
[identity-and-authentication.md](identity-and-authentication.md) and
[repositories-and-proxy.md](repositories-and-proxy.md) hand this key to this page.

- Algorithm: AES-256-GCM with a random 96-bit nonce.
  - Ciphertext format: `v1:<key id>:<base64(nonce ‖ ciphertext ‖ tag)>`.
  - The additional authenticated data is the number of primary-key parts, then the strings `trove/sealed/v1`,
    the table, the column and each primary-key part, every string prefixed with its length (an unsigned
    varint). A composite key is registered as `repository_id,name` and passed as `secrets.Row(parts...)`; a
    row key whose part count differs from the registered key fails with `ErrRowKey`, and two row keys that
    differ only in where a separator falls encode differently. A database writer cannot move a ciphertext to
    another row or column and have it decrypt there.
- Key ring: `secrets.encryption_key_files` lists files. Each file holds 32 random bytes, either as
  base64 text (43 or 44 characters, surrounding white space ignored) or raw (a file of exactly 32
  bytes, as a random-byte generator writes it; Q-ARCH-34), and the file name without `.key` is the
  key ID. `secrets.active_key_id` names the key used for
  encryption; every listed key decrypts. The Helm chart mounts them from a Kubernetes Secret
  ([ha-and-helm.md](ha-and-helm.md)). There is no default key and no generated key: a missing key
  ring stops start-up.
- Sealed-column registry. Every column that holds sealed values is registered with
  `internal/secrets`, with its table, primary key and column. The per-key counts of the status
  below and the `secrets-reencrypt` job walk this one registry. No second list of sealed columns
  exists.
- Key-ring status. `trove admin secrets status` and `GET /api/v1/system/encryption`
  ([api-and-ui.md](api-and-ui.md) R-API-049, Q-API-8) share one implementation in
  `internal/secrets`. It reports:
  - the key IDs;
  - the sealed values per key ID;
  - the canary state;
  - the key IDs of every live replica.

  It never reports key material, fingerprints, ciphertext or plaintext.
- Each replica publishes the key IDs of its ring and its active key ID with its heartbeat in the
  replica registry ([operations-and-observability.md](operations-and-observability.md) R-OPS-003).
  It publishes IDs only. The rings are compatible when every live replica's ring holds every key ID
  that is active on any live replica or that a sealed value references.
- Fingerprints: configuration-as-code diffs compare secrets by HMAC-SHA-256
  ([operations-and-observability.md](operations-and-observability.md)). The HMAC key is derived with
  HKDF-SHA-256 ([RFC 5869](https://www.rfc-editor.org/rfc/rfc5869)) from the active key, with info
  `trove fingerprint v1`. After a key rotation, fingerprints change once, so the next diff reports
  every secret as changed.
- Rotation. Every replica must be able to decrypt with a key before any replica seals with it.
  Rotation therefore takes two roll-outs before the re-encryption; the chart side is in
  [ha-and-helm.md](ha-and-helm.md) R-HA-027.
  1. Add the new key file to the ring, leaving `active_key_id` unchanged, and roll out. The new key
     is decrypt-only everywhere.
  2. Set `active_key_id` to the new key, then roll out.
  3. Start the manual task `secrets-reencrypt` (R-ARCH-016) with
     `POST /api/v1/system/tasks/secrets-reencrypt/run` (system administrator).
     - The job reseals every value of the sealed-column registry that is not under the active key.
       It is idempotent. It works in batches of one transaction each, and a batch whose transaction
       is run again (a deadlock, a serialization failure, a failover) neither skips its rows nor
       counts them twice (Q-ARCH-227).
     - The trigger answers `409` with code `key_ring_incompatible` while the rings are
       incompatible, that is, while a live replica's ring lacks a key ID that is active anywhere or
       that a sealed value references. Running then would seal values that replica cannot open.
     - While a run is queued or running, the trigger answers `409` with that job.
  4. When `trove admin secrets status` or `GET /api/v1/system/encryption` reports no values under
     the old ID, remove its file and roll out again.

  HA-016 describes a single key without a rotation path. Key IDs from day one avoid that problem.
- Start-up canary: for each key ID, `secret_key_canary` holds a known plaintext sealed with that key.
  A replica refuses to start when the ring cannot open the canary of a key ID that sealed rows still
  reference. The error is `key ring does not match database: missing key <id>`. The fix is to
  restore the key file.
- Sealing is always on, and no administrator operation switches it (IAM-045).
  - There is no deactivate operation. The catalog leaves open whether secrets stay unsealed after a
    deactivation or are sealed again on the next change. Neither state can arise in trove: no code
    path writes an unsealed secret value to PostgreSQL.
  - There is no activate operation and no generated key. The key ring comes from the operator, and
    sealing runs from the first start.
  - Values written before any key existed do not exist, because a missing key ring stops start-up.
- Backups and exports never contain key material.
  - A PostgreSQL backup holds sealed values only. The configuration export of
    [operations-and-observability.md](operations-and-observability.md) R-OPS-032 replaces every
    secret with a `secretRef` and never exports a value, sealed or plain.
  - The backup job of INV-19 copies sealed values only and never the key ring. The operator keeps
    the key ring Secret in escrow, outside the cluster and outside the target's credential domain,
    including every retired key that a retained backup still lists
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-028).
  - `trove backup restore` opens the backup's canary rows with the configured key ring before it
    loads anything, and refuses with `key ring does not match backup: missing key <id>`
    ([backup-and-restore.md](backup-and-restore.md) R-BAK-020 check 3). A database restored by
    other means without its key ring is refused by the start-up canary above, before any request is
    served. There is no import path that carries a key.

```sql
CREATE TABLE secret_key_canary (
    key_id     text        PRIMARY KEY,
    ciphertext text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

```yaml
secrets:
  encryption_key_files:                          # example
    - /var/run/secrets/trove/encryption/2026-10.key
  active_key_id: "2026-10"                       # example
```

Acceptance criteria:
- After a remote repository is created with an upstream password, every value in the sealed-secret
  column starts with `v1:2026-10:`.
- After the rotation sequence, `trove admin secrets status` reports `0` values under `2026-10`, and
  `GET /api/v1/system/encryption` reports `sealedValues: 0` for that key ID. After the old file is
  removed, every replica starts.
- With one replica still on a ring that lacks the newly active key,
  `POST /api/v1/system/tasks/secrets-reencrypt/run` answers `409` with `key_ring_incompatible`,
  and no `secrets-reencrypt` job is created.
- A unit test fails if a column whose values pass through `internal/secrets` sealing is missing
  from the sealed-column registry.
- During step 2 of the rotation, while old and new replicas run side by side, a secret that a new
  replica seals is opened by an old replica without error.
- `pg_dump` of a database with sealed upstream passwords contains none of the plaintext passwords.
- A replica started with an unrelated key ring exits non-zero with the mismatch message.

### R-ARCH-022 · Outbound HTTP client factory
Priority: **MVP** · Covers: HA-019, RMT-015

Every CA bundle and client certificate of an outbound client is re-read without a restart, on the reload
bound of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-035 (60 s), so the statement that the
bundle is read once at start-up does not hold; the reload is required from M0, not in P1. The reloader is the
one of R-ARCH-035. The object store's CA trust follows Q-STOR-14 until it is supported.

- Every outbound HTTP request is made by a client from `internal/outbound`. This includes upstream
  registries, OIDC discovery and JWKS, the GitLab issuer JWKS, the object store and webhooks (P1).
  The factory builds clients per purpose, and per remote repository.
- The factory applies:
  - the trust store: system roots plus configured CA bundles, with no global switch to disable
    verification;
  - client certificates;
  - outbound proxies;
  - redirect policy;
  - the egress address policy, checked in the dialer's `Control` hook at connect time, so DNS
    rebinding cannot get around it;
  - timeouts, with a remote's socket timeout (RMT-015) mapped to its connect and response-header
    timeouts;
  - the upstream request log.

  Settings and defaults are in [repositories-and-proxy.md](repositories-and-proxy.md)
  (`proxy.outbound`) and [operations-and-observability.md](operations-and-observability.md) (webhook
  delivery and the upstream log).
- Every client sends `User-Agent: trove/<version>`. No other component builds an `http.Client` or
  dials a socket (R-ARCH-002).

Acceptance criteria:
- A remote whose upstream URL is `http://169.254.169.254/latest/` answers `502` with code
  `egress_denied`, and no packet to that address leaves the pod.
- A host name that alternately resolves to a public address and to `127.0.0.1` never produces a
  connection to `127.0.0.1`.

## Errors, lifecycle and compatibility

### R-ARCH-023 · Error model
Priority: **MVP** · Covers: trove-only

The wire format on `/api/v1/`, `/auth` and `/files` is RFC 9457 problem details, as
[api-and-ui.md](api-and-ui.md) specifies (`type`, `title`, `status`, `detail`, `instance`, and the
extensions `code`, `requestId`, `errors`). Package-protocol routes keep their ecosystem's error
shape. This requirement fixes the internal model every component uses.

```go
package errs

type Kind uint8

const (
	Internal Kind = iota
	Invalid             // 400; 422 when the body parsed but failed validation
	Unauthenticated     // 401 + WWW-Authenticate
	Forbidden           // 403
	NotFound            // 404
	MethodNotAllowed    // 405 + Allow
	Conflict            // 409
	PreconditionFailed  // 412
	TooLarge            // 413
	Unsupported         // 415
	RateLimited         // 429 + Retry-After
	InsufficientStorage // 507
	Unavailable         // 503 + Retry-After
	UpstreamFailed      // 502
	UpstreamTimeout     // 504
)

type Error struct {
	Kind       Kind
	Code       string        // stable snake_case code, part of the API contract
	Detail     string        // safe for the caller
	Fields     []FieldError  // pointer, code, detail
	RetryAfter time.Duration
	Op         string        // where it happened; logged, never rendered
	Err        error         // cause; logged, never rendered
}

func E(kind Kind, code, detail string, opts ...Option) *Error
func (e *Error) Error() string
func (e *Error) Unwrap() error
```

- Only an `*errs.Error` is rendered. Any other error becomes `Internal` with code `internal` and a
  generic detail. Its cause is logged with the request ID.
- Codes are listed in `internal/errs/codes.go`, the single registry from which the problem catalog
  served under `/api/v1/problems/<code>` is generated ([api-and-ui.md](api-and-ui.md)). A code is
  never reused or renamed within an API major version (R-ARCH-025).
- Package-protocol routes render through `Format.WriteError`. The status comes from the `Kind`
  unless the protocol requires otherwise, and each format page documents its deviations.
- Upstream errors are never passed through verbatim. An upstream `401` or `403` becomes
  `UpstreamFailed` (R-ARCH-013), and an upstream body never appears in `detail`.
- Logging:
  - `5xx` responses are logged at `error`, with cause and, for panics, the stack.
  - `4xx` responses are logged at `debug`. Authentication and authorization denials also reach the
    audit stream ([operations-and-observability.md](operations-and-observability.md)).
  - A cancelled client context is not an error. It is logged with status `499`.

Acceptance criteria:
- `curl -s -D h.txt -H "Authorization: Bearer $T" https://trove.example.com/api/v1/repositories/nope`
  returns `404`, `application/problem+json` and `.code == "repository_not_found"`, and
  `.requestId` equals the `X-Request-Id` header in `h.txt`.
- With PostgreSQL unreachable, API calls return `503` with code `database_unavailable` and
  `Retry-After`, and the body contains no SQL.
- `npm view trove-nope-4711 --registry …/npm-local/` makes npm print its normal `404 Not Found`
  message.

### R-ARCH-024 · Process lifecycle: start-up, readiness, graceful shutdown
Priority: **MVP** · Covers: HA-047, OPS-002, OPS-003

The probe endpoints, the readiness checks and the drain delay are owned by
[operations-and-observability.md](operations-and-observability.md), and the pod's grace period by
[ha-and-helm.md](ha-and-helm.md) R-HA-011. This page owns the order of start-up, the order of
shutdown and the server shutdown timeout. Together they make a node drain cause no outage when
every role runs at least two replicas on different nodes (INV-12).

Start-up order:
1. Load and validate the configuration. Stop on any error.
2. Start logging, metrics and tracing, and open the ops listener. Readiness answers `503`.
   Liveness answers `200` while the listener is up and every watched loop started so far makes
   progress (R-ARCH-016, [operations-and-observability.md](operations-and-observability.md)
   R-OPS-001). Loops register with the watchdog when they start, so a step below that is still
   retrying never fails liveness.
3. Connect the main pool and run the schema compatibility check (R-ARCH-026), retrying until
   `startup` passes or the process exits. While `system_state` marks a restore in progress
   ([backup-and-restore.md](backup-and-restore.md)), the replica waits at this step: it stays not
   ready and claims no jobs.
4. Call `audit_maintain_partitions()`
   ([operations-and-observability.md](operations-and-observability.md) R-OPS-015). In single-role
   mode, the replica first writes `audit_policy` itself. The call is repeated with backoff until
   partitions for the current and the next month exist. A drop that the function cannot lock is
   left to its next call and is not a failure.
5. Run the key-ring canary (R-ARCH-021).
6. Run the object-store check ([storage.md](storage.md)).
7. Read the keyspace ID (R-ARCH-030) and start the Valkey client in the background (R-ARCH-031).
   Start-up never waits for Valkey, and an unreachable Valkey never gates readiness; a
   configuration error at the first connection is handled as [ha-and-helm.md](ha-and-helm.md)
   Q-HA-13 decides. Run the first change-log poll (R-ARCH-018). Register in the replica registry
   with the role. On a `worker` replica, open the lease pool (R-ARCH-017).
8. Load the snapshots and initialise the registered caches.
9. Open the client listeners (`api`, `packages`), each with the route families of its endpoint that the role serves (R-ARCH-001, Q-ARCH-298), or start the job workers on the queues the process claims (`worker`, R-ARCH-016), as the role allows.
10. The `startup` readiness check passes. It requires that steps 3 to 9 have completed and that the
    audit partitions for the current and the next month exist (R-OPS-001). It does not require a
    reachable Valkey.

Step 8 builds one area at a time, in the order of the list in `internal/app/steps.go`. Each area is a
step that takes the typed results of earlier steps and returns its own; the list is checked against the
role's areas when the replica is built (Q-ARCH-210). A replica MUST NOT start with an area missing that
a step it runs needs: the missing prerequisite stops the start-up with an error that names the area, the
missing part and the role, instead of a replica that is ready and answers `503` on the routes of the
area (Q-ARCH-209). Whether a prerequisite is reachable is a different matter: a database or an object
store that cannot be reached yet is retried as steps 3 to 7 say; a part that the role's wiring does not
provide at all is a defect and is refused before anything starts. On a role that serves the UI the area `ui-assets` publishes the
hashed files of the build to the shared asset store in step 8, before the listener opens, and waits for it at most a minute
([ha-and-helm.md](ha-and-helm.md) Q-HA-123).

Shutdown on `SIGTERM`. Kubernetes removes the pod from the EndpointSlices at the same time as it
sends the signal, and every proxy in front of trove (kube-proxy, the Ingress or Gateway controller)
learns of the removal with a delay. The steps cover that delay first:
1. Readiness fails at once (`draining`), and the registry row turns `draining`.
   - A `worker` stops claiming jobs at once. If it holds the `scheduler` lease, it stops its
     scheduler tick and releases that lease at once (`pg_advisory_unlock`), so another worker
     schedules from its next retry instead of after this drain
     ([operations-and-observability.md](operations-and-observability.md) R-OPS-001, R-OPS-019).
     Its other leases stay until step 6.
   - A `web` replica holds no new cross-replica flight from now on
     ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026, Q-REPO-7; R-ARCH-033).
   - A support-bundle part this replica is still collecting is abandoned (R-OPS-027, P1).
2. A `web` replica keeps serving normally for `health.shutdown_drain_delay`, so that requests a
   proxy still routes to it succeed. That includes `GET /api/v1/system/ping`, which answers `200`
   until the listener closes ([operations-and-observability.md](operations-and-observability.md)
   R-OPS-002, Q-OPS-30). Every response it completes from now on carries
   `Connection: close` on HTTP/1.1, so clients and proxies reconnect through the Service to another
   replica. The drain delay must cover the endpoint-propagation time of the edge in use; the drain
   test below measures it.
3. At the start of this step, before `http.Server.Shutdown` waits for idle connections, the
   replica sends `retry: 1000` on every open event stream and ends it, through
   `http.Server.RegisterOnShutdown` ([api-and-ui.md](api-and-ui.md) R-API-051,
   [operations-and-observability.md](operations-and-observability.md) R-OPS-001 step 3). This
   covers every long-lived stream, such as the delivery-log follow stream of R-OPS-044 (P1):
   `Shutdown` waits for connections to become idle and never ends such a stream itself, so it
   would hold the replica until the deadline and then be cut. Its client reconnects through the
   Service to another replica with `Last-Event-ID`. Then `http.Server.Shutdown` runs with deadline
   `server.shutdown_timeout`. The listeners close, idle connections close, HTTP/2 connections
   receive `GOAWAY`, and in-flight requests finish. The change-log poll and the grant-index
   rebuilds keep running through this step and until step 6, because the requests still in flight
   and the commit-time re-checks of uploads that complete now depend on them
   ([authorization.md](authorization.md) R-AUTHZ-028, "Drain").
4. Every watched loop that stops leaves the watchdog first (R-ARCH-016), so the stop does not fail
   liveness. Job handlers still running 11 s before the deadline are cancelled and their jobs
   released without counting the attempt (R-ARCH-016). The 11 s are the 15 s settle deadline of the
   job worker (`jobs.SettleDeadline`) less the 4 s between the deadline and the close deadline of
   step 6, so the release write of a job has its whole settle deadline, retries included
   ([Q-ARCH-329](#q-arch-329--the-job-worker-is-cancelled-early-enough-that-the-release-writes-of-its-jobs-get-their-whole-settle-deadline)).
   A shutdown timeout under 11 s cancels the handlers when the drain delay ends. Remote fetches
   still running 5 s before the deadline are cancelled, after the job worker has returned, and
   their flight locks released with `failed` (R-ARCH-033).
5. At the deadline, the requests still running are cut by closing their connections, with one
   exception. A request whose request body has been read completely is storing and committing an
   upload, and it is never cut between its commit and its status line. It runs on into the 5 s
   margin of the grace period, for at most 3 s, so the process still exits before the kubelet's
   `SIGKILL`. P-11 bounds that work (1 s plus size ÷ 150 MiB/s, proposed). Without the exception, a
   publish cut after its commit would be retried by its client and refused as an existing version
   (`409 npm_version_exists`, [formats/npm.md](formats/npm.md)), although it had succeeded. npm
   additionally acknowledges an identical retry by the same principal, with the same bytes and
   dist-tag, within `npm.publish.retry_window`, which also covers answers lost outside trove; and a
   revisioned unpublish or deprecation retried with a stale revision succeeds when it would change
   nothing ([formats/npm.md](formats/npm.md) R-NPM-007 step 4, R-NPM-009, Q-NPM-11).
   - A download cut this way is detectable by the client, because every response with a blob body
     carries `Content-Length`. A client that resumes sends `Range` with `If-Range: <ETag>` and is
     served `206` by whichever replica it reaches, because the ETag is the content address
     ([storage.md](storage.md) R-STOR-012). npm and NuGet clients retry the whole request instead
     (npm `fetch-retries` 2, NuGet 6 tries); their files are small against the grace period.
   - A streamed download whose length depends on the data, such as the audit export, is cut the
     same way. Its client sees an incomplete response and resumes on any replica as
     [api-and-ui.md](api-and-ui.md) R-API-051 specifies.
   - An upload cut this way, while its body was still arriving, leaves nothing committed
     ([storage.md](storage.md) R-STOR-004). The client retries it.
     - A cut request of an upload session (P1) keeps the parts it has recorded and never aborts
       the session's multipart upload. It cancels its outstanding part upload, waits for the call
       to return, and releases its writer claim in one short transaction before the process exits
       ([storage.md](storage.md) R-STOR-004 step 8, R-STOR-011). The client then resumes on
       another replica at once, instead of meeting `416` until the claim expires.
   - Each cut is counted in `trove_http_transfers_cut_total{direction}`. How long a single
     transfer may run without being cut is decided in Q-ARCH-18.
   - A request that finishes before the deadline is not finished when its handler returns: its last
     bytes may still sit in the replica's send queue, behind a slow reader. A connection that closes
     during the shutdown with unacknowledged bytes stays open until the peer has acknowledged them,
     at most until the end of the commit grace, because the sockets of an exited process die with
     the pod's network namespace without a `FIN` or an `RST`. A connection that is cut at the
     deadline, or still unacknowledged at that bound, is reset (`RST`), so that the peer learns of
     the end at once and not at its own timeout (Q-ARCH-187).
6. After the last request has finished, the write-only buffers of R-ARCH-032 are flushed before
   the pools close: download statistics, the `lastActivityAt` buffer
   ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-012), the metadata
   `last_used_at` buffer (R-REPO-046, P1) and the token last-used buffer
   ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-031). The open
   audit deduplication summaries, denial summaries included, are written and the asynchronous
   audit buffer is flushed ([operations-and-observability.md](operations-and-observability.md)
   R-OPS-015). Pending signals are published. Then the remaining leases are released (R-ARCH-017),
   the registry row is deleted, the change-log poll and the grant-index rebuilds stop, and the
   Valkey client and the pools close. The process exits `0`.

The pod's termination grace period must exceed `health.shutdown_drain_delay` plus
`server.shutdown_timeout` plus 5 s. The chart passes the grace period to the process and derives
`server.shutdown_timeout` from it ([ha-and-helm.md](ha-and-helm.md) R-HA-011), so the two cannot
drift apart. A transfer that outlasts it is cut off (HA-047).

No-outage drain needs, for each of the roles `api`, `packages` and `worker` and each pool, at least two replicas on different
nodes and a disruption budget that evicts one pod at a time (INV-12,
[ha-and-helm.md](ha-and-helm.md) R-HA-032). A replica holds nothing the others need: its requests
are retried elsewhere (R-ARCH-032), its jobs are released or expire (R-ARCH-016), its leases are
released, or end with their sessions if it is killed (R-ARCH-017), and its flight locks are
released or expire (R-ARCH-033).

The `backup` role serves no client. A `trove backup create` on a drained node exits `75`, and its
Job retries it; a retried run copies only the blobs that are still missing
([backup-and-restore.md](backup-and-restore.md) R-BAK-013). Its garbage-collection hold counts only
while the run's lease is valid, so it cannot outlive the run (R-BAK-007, R-BAK-010).

Acceptance criteria:
- With two `web` and two `worker` replicas on four nodes and the chart's disruption budgets,
  `kubectl drain` of a node that hosts one `web` and one `worker` replica runs during a loop of
  100 `npm ci`, 20 `npm publish`, 100 `dotnet restore` and 20 `dotnet nuget push` through the
  Service, all with their default retry settings. No client operation fails, and every published
  version is installable afterwards.
  Automated by the drain subtest of `TestNuGetHA` (`make e2e SET=nuget-ha`), whose load runs the four
  commands in parallel loops and is checked for at least these counts
  ([Q-HA-101](ha-and-helm.md#q-ha-101--how-r-arch-024s-drain-criterion-is-counted)).
- `kubectl rollout restart deployment/trove-web` runs during a
  `curl --limit-rate 20M -o f.bin …/files/generic-local/400MiB.bin` (about 20 s). curl exits `0`,
  and `sha256sum f.bin` matches.
- A `curl --limit-rate 2M -o f.bin …/files/generic-local/400MiB.bin` (about 200 s, longer than the
  grace period) runs while the serving pod is deleted. The transfer is cut at the deadline.
  `curl -C - --limit-rate 20M -o f.bin` with the same URL resumes with `206` from another replica,
  and `sha256sum f.bin` matches.
- A test build delays the commit of an `npm publish` by 2 s, and the serving pod is deleted so that
  the publish body completes 1 s before the shutdown deadline. The publish answers `201`, the
  version is installable through another replica, and the kubelet events show no `SIGKILL`.
- The PostgreSQL primary is stopped for 30 s. `/healthz` stays `200` and `/readyz` turns `503`.
  Afterwards `/readyz` is back to `200`, and no restart count has increased.
- A replica started while PostgreSQL is unreachable answers `/healthz` with `200` and `/readyz`
  with `503` until the database is back. When `/readyz` first answers `200`, `pg_inherits` lists
  `audit_event` partitions for the current and the next month.
- A replica started while Valkey is unreachable becomes ready, and `/readyz` answers `200` with
  `"degraded":["valkey"]`. The Valkey primary is then killed without a failover for 60 s: no
  replica turns unready and no restart count increases.

### R-ARCH-025 · Versioning and compatibility policy
Priority: **MVP** · Covers: OPS-072, EVT-072

| Surface | Version scheme | Promise |
|---|---|---|
| trove release | [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html); image tag = version | Any later minor of the same major can be installed directly. Rolling upgrades with no downtime work from the previous minor (N-1 → N) and between patches (R-ARCH-026). The two latest minors receive security fixes (to be stated in `SECURITY.md`). |
| Management API | `/api/v1` | Additive changes only within v1. Deprecation headers ([RFC 9745](https://www.rfc-editor.org/rfc/rfc9745), [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594)) and the support window are in [api-and-ui.md](api-and-ui.md). |
| Package protocols | Ecosystem-defined (npm registry API, NuGet v3) | trove follows the protocol. A client-visible change in behaviour is a breaking trove change unless it fixes non-conformance. |
| Error codes | snake_case strings | Stable within an API major |
| Configuration file | Key paths | Keys are only added within a major. A renamed key keeps its old name as a deprecated alias, with a start-up warning, for at least 2 minors. Removal happens only in a major. |
| Helm chart | Own SemVer; `appVersion` = trove version | Same alias rule for values ([ha-and-helm.md](ha-and-helm.md)) |
| Database schema | Migration sequence | Not a public interface. N and N-1 run against one schema (INV-3, R-ARCH-026). |
| Token formats | `trv_*_` prefixes (INV-8) | A prefix never changes meaning. New token types get new prefixes. |
| Metrics, log and audit fields | Names and labels | Stable within a major. A renamed metric is emitted under both names for at least 2 minors ([operations-and-observability.md](operations-and-observability.md)). |
| Bastion interface | `bastion.InterfaceVersion`, an integer of its own, carried in `Trove-Bastion-Interface` ([bastion.md](bastion.md) R-BAS-009) | Main installation and Bastion talk only at the same version; every change of the interface raises it, guarded by its surface file; a release that raises it says so in its notes |
| Format contract | `formats.ContractVersion`, frozen at 1 since the end of M3 ([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)) | Internal and compile-time checked; no external promise. Every change is an ADR; the rule for changing it is below |

**Major version 0.** trove releases stay on major version 0 until the owner decides otherwise
([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)); Semantic Release sets each version from the
conventional commits, and a phase closes with the release cut from its commits. Semantic Versioning would allow any change within 0.x; trove does
not use that latitude. The promises of the table apply between 0.x minors as they would within a
major, and a breaking change in 0.x is announced in the release notes with its migration path.

`helm rollback` from N to N-1 works by design (R-ARCH-026). Rolling back further is not supported.

**Evolution of the format contract after the freeze.** The contract froze at `ContractVersion` 1 at the end of M3 (E3.3,
[ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)), as the smaller one: what no caller in
the core could use is not in it (Q-ARCH-256). From the freeze on:
- *Every change is an ADR.* `TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile` holds the exported surface
  of `internal/formats` and of the catalog leaf `internal/formats/catalog` (every type with its exported fields,
  interface methods and methods, every function, every constant and variable with its value), and the types with their
  fields and methods of the leaves the contract exposes (`internal/vuln/advisory`, with its constants, and
  `internal/vuln/match`; their functions are not part of the contract), equal to
  [internal/formats/testdata/contract.txt](../../internal/formats/testdata/contract.txt), whose first line is
  `ContractVersion`. A change of the surface fails the test until the file is regenerated
  (`go test ./internal/formats -run TestR_ARCH_025 -update-contract`) in the change that carries its ADR. The test does
  not tell an addition from a breaking change; the ADR decides which it is, by the rules below (Q-ARCH-280).
- *A new behaviour is added, never inserted.* A behaviour whose presence the core must know, because it changes what an
  engine does (a discovery document, a fill hook, a restore hook), is an optional interface of `internal/formats` with a
  field in `Capabilities`; `CapabilitiesOf` finds it when the format registers, the registry records it, and the core
  asks the registry (`CapabilitiesFor`), never the format value. A format that has no such behaviour is not touched, and
  `ContractVersion` stays.
- *A default for the common case lives in an embeddable base* (`Base`, `RemoteBase`), as `ExpectsType`,
  `BeforeArtifact` and `UpstreamQuery` do: a behaviour every format answers, whose default is a correct answer for a
  format without it, is such a method rather than an optional interface (Q-ARCH-254). A method added to an adapter
  interface needs such a default in the same change.
- *A method that the core calls on every format* is added to `Format` only together with its default in `Base`, so that
  a format that embeds `Base` compiles unchanged.
- *New services for formats* are added to `Host`, `PackageWriter`, the stores, `RemoteFetcher` and `Resolver`: formats
  call them and implement none, so adding is compatible.
- *`ContractVersion` rises only when the meaning of an existing method changes* (an argument gains a rule, a result a
  case), a signature changes or a method or field leaves. The change is applied to every registered format in the same
  change, `Register` refuses a format written against the older number, and its ADR names what breaks. The narrowed
  signatures of Q-ARCH-260 (a format names only its own repository, H-329) are the first such change on record.
- The conformance kit gains a check for every new behaviour in the change that adds it, and the written blocks of R-ARCH-008
  and R-ARCH-009 change with the code (`TestR_ARCH_008_TheWrittenContractEqualsTheCode`). A check added to the kit later
  applies to every format in the change that adds it.

Acceptance criteria:
- A CI job compares `api/openapi.yaml` with the latest release tag using an OpenAPI breaking-change
  checker (for example `oasdiff breaking`) and fails on a breaking change within v1. The checker is oasdiff at the pin
  of `tools/versions.env` (Q-ARCH-273), the baseline is the newest `v0.*` tag whose commit is not HEAD's (Q-ARCH-274),
  and the job is the `compat` job of `release.yml`, run by `make compat-check`; the tests are `TestR_ARCH_025_*` in
  `test/compat`.
- The upgrade end-to-end test runs `helm upgrade` from N-1 to N and then `helm rollback`. Both
  versions pass the client suite against the same database.
- A change of the exported surface of the format contract fails
  `TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile` (in `internal/formats`, not `test/compat`) until
  `internal/formats/testdata/contract.txt` is regenerated, and the failure names each changed line.

### R-ARCH-026 · Schema migrations and the compatibility gate
Priority: **MVP** · Covers: OPS-073, OPS-074, HA-066, HA-069

[ha-and-helm.md](ha-and-helm.md) owns what a migration may do and how an upgrade is guarded: the
step classes, the tables `schema_migration` and `schema_state`, checksums, lock timeouts and
retries, the compatibility window and the contract guard (R-HA-018), the migration Job (R-HA-019)
and background data migrations (R-HA-022). This requirement fixes the files and the runner that
implements them (Q-ARCH-3).

Files:
- Migrations are SQL files in `internal/meta/postgres/migrations/NNNNN_<name>.sql`, embedded in the
  binary (INV-3). There are no down migrations (R-HA-018).
- A migration file is immutable once it is on `main`, released or not. It is never edited, not even
  in a comment or in whitespace (the runner checksums the bytes), and never renamed or removed. A
  defect is fixed forward with a new file of the next number, in one of the classes of R-HA-018
  (Q-ARCH-283). The compat check reads `main`'s history for this (acceptance criteria below).
- Each file starts with trove's header comments:
  - `-- trove:phase expand` or `-- trove:phase contract`, which is R-HA-018's class;
  - `-- trove:no-transaction` on a step that cannot run in a transaction block, such as
    `CREATE INDEX CONCURRENTLY`. Such a file holds exactly one statement (R-HA-018);
  - on a contract step, `-- trove:requires-app >= <version>`, the oldest release allowed to serve
    once the step is applied. R-HA-018's `lastUsedBy` is the release before it, and `compat_floor`
    is the highest `requires-app` of the applied contract steps;
  - on a contract step that removes what a background data migration converts, `-- trove:requires-background <name>`,
    once per migration ([ha-and-helm.md](ha-and-helm.md) R-HA-022, Q-HA-115).
- Expand steps never break the previous minor: new tables, nullable or defaulted columns, indexes
  built `CONCURRENTLY`, new enum values (R-HA-018 lists the classes in full). Contract steps (drop,
  rename, tighten) ship at the earliest one minor after the release that stopped using the old
  objects ([ha-and-helm.md](ha-and-helm.md) Q-HA-5).

Runner. It is trove's own code in `internal/meta/postgres` on the native pgx API; no migration
library and no `database/sql` are involved (Q-ARCH-3). `trove migrate up` runs these steps:
1. Take `pg_advisory_lock(3, 0)` (category `migration`, R-ARCH-017) and run every later step on
   that same connection. A running backup's database phase holds the lock shared, and a running
   restore holds it exclusively, so the runner waits for either up to `migrate.advisory_lock_wait`
   ([ha-and-helm.md](ha-and-helm.md) R-HA-019).
2. Refuse a half-restored database. While the schema carries a restore anchor
   (`COMMENT ON SCHEMA <database.schema> IS 'trove-restore <backup-id>'`) or `system_state` holds
   `restore_in_progress` ([backup-and-restore.md](backup-and-restore.md) R-BAK-021), the runner
   exits with status `3`, applies nothing and names the backup ID. An interrupted restore is then
   resumed instead of being overtaken: steps applied beyond the backup's step before the load
   would make the resumed load fail its column comparison. The check runs after step 1, because
   the restore holds the lock while it runs. The runner's internal restore mode (below) does not
   run this check.
3. Verify the SHA-256 of every applied step in `schema_migration` against the embedded file. On a
   mismatch, stop before applying anything (R-HA-018).
4. Run the contract guard (gates below).
5. Apply each pending file in order:
   - A transactional step runs as `BEGIN`, then `SET LOCAL lock_timeout` to
     `migrate.ddl_lock_timeout`, then the whole file as one `Exec` without arguments, then the
     step's `schema_migration` row, then `COMMIT`. pgx sends an argument-less `Exec` through the
     simple protocol, so PostgreSQL itself splits and parses the statements. A step and its row
     commit together, so a crash leaves neither.
   - A `no-transaction` step runs its single statement under the session's `lock_timeout` and then
     writes its row. Before a retry, an `INVALID` index left by the failed attempt is dropped
     (R-HA-018).
   - A lock timeout (SQLSTATE `55P03`) retries the step up to `migrate.ddl_lock_retries` times with
     backoff.
6. Update `schema_state`, release the lock, and print the summary of R-HA-019.

`trove migrate status` lists applied and pending steps and the background data migrations without taking the lock;
`GET /api/v1/system/migrations` answers the same report from the same code ([ha-and-helm.md](ha-and-helm.md) R-HA-018).

Restore mode. The runner offers an internal "migrate up to step N" mode for `trove backup restore`
alone, which loads a backup into exactly the schema it was exported from and then applies the
remaining steps ([backup-and-restore.md](backup-and-restore.md) R-BAK-021, R-BAK-023, Q-BAK-8).
`trove migrate` exposes no such flag.
- It runs on a connection that already holds `pg_advisory_lock(3, 0)`: the restore's session
  connection, which holds the lock from its first check to its finish. The runner neither takes
  nor releases the lock in this mode.
- It applies the embedded steps up to step N, or all remaining steps when the restore brings the
  schema forward after the load, with the same checksums, lock timeouts and retries as above. It
  skips the half-restored refusal of step 2, because the restore is the operation that set the
  anchor.
- It lets the caller add statements to the transaction of its final `schema_state` update. The
  restore commits `restore_in_progress` there, so a replica's schema check never finds a servable
  schema without the marker (R-BAK-021 step 2, R-ARCH-024 start-up step 3).
- During a restore no replica is registered (`replica` is a `transient` table, and the replicas
  wait at start-up step 3 of R-ARCH-024), so the contract guard has nothing to wait for.

Migration files are not squashed or removed within a major, in addition to never being edited
once applied (R-HA-018). Every later release of a major can therefore restore every earlier backup
of it. A major release that drops old steps names, in its release notes, the last release that
restores older backups (R-BAK-023).

Gates:
- **Gate in `trove migrate`** (R-HA-018's contract guard). Before a contract step is applied, the
  runner reads the replica registry ([operations-and-observability.md](operations-and-observability.md)).
  If a live replica runs a version below the step's `requires-app`, it exits `3` without applying
  anything. It lists those replicas and the two ways forward of R-HA-021. There is no override flag:
  the downtime path is to scale the replicas to zero, so that the registry shows none. It also exits
  `3` while a background migration the step names in `-- trove:requires-background` has not succeeded
  ([ha-and-helm.md](ha-and-helm.md) R-HA-021 step 4, Q-HA-115).
- **Gate in `trove serve`**, through the `schema` readiness check, with R-HA-018's rule: a binary
  of version V serves only if `schema_state.applied` is at least its `requiredSchema` and
  `compat_floor` is at most V.
  - Below `requiredSchema`, the replica stays unready. The Helm pre-upgrade Job normally migrates
    first.
  - With `compat_floor` above V, the process exits with `schema requires trove >= <compat_floor>`.
- Data migrations that take minutes are R-HA-022's background migrations. Each runs as a job of
  kind `data_migration_<name>` (R-ARCH-016), is resumable and is visible in the job API. Code
  reads both the old and the new form until it has finished.
- Because N and N-1 are schema-compatible, configuration changes are not blocked during a rolling
  upgrade (OPS-073 describes a system that blocks them). A feature that needs every replica on N is
  gated on the lowest version among the live replicas.

Acceptance criteria:
- Three replicas on N-1 run a continuous `npm ci` plus `npm publish` loop. `helm upgrade` to N
  completes without a failed client operation.
- A test contract migration whose `requires-app` is above a live replica's version makes
  `trove migrate` exit `3` and name that replica.
- An N-2 binary started against schema N exits with `schema requires trove >= …`.
- `trove migrate` killed with `SIGKILL` inside a transactional step leaves neither the step's
  objects nor its `schema_migration` row, and the next run applies the step.
- After an applied migration file is edited, `trove migrate up` exits non-zero, names the file and
  applies nothing.
- Against a database whose schema carries the restore anchor of backup `20261028T020000Z-7f3a`,
  and once more against one whose `system_state` holds `restore_in_progress`, `trove migrate up`
  exits `3`, names the backup ID and applies nothing. A restore of that backup then resumes and
  finishes (R-BAK-021).
- A CI check compares the embedded migration files with those of the latest release of the same
  major and fails when one of them is missing, renamed or changed, or when an added file does not continue the
  numbering after the release's highest number (Q-ARCH-274).
- A CI check reads the history of `main` and fails when a migration file that main has committed is changed,
  removed or renamed by a later commit, on `main` or on the checked-out branch of a pull request, with the file,
  the commit and the rule in the message. The allowlist `historicalEdits` names each historical edit by its
  commit, and an entry that names no edit of `main` fails (Q-ARCH-283). A file that only the checked-out branch
  has is not checked, because main does not hold it yet.
- The tests are `TestR_ARCH_026_*` in `test/compat`; the history check runs in the same job and target as the release check.
- A unit test fails on a migration file without a valid `phase` header, on a contract step without
  `requires-app`, and on a contract-class statement (`DROP`, `RENAME`, `SET NOT NULL`) in an expand
  file.
- `internal/archtest` fails if any trove package imports `database/sql` or
  `github.com/jackc/pgx/v5/stdlib`.

## Performance and robustness

### R-ARCH-027 · Performance targets
Priority: **MVP** · Covers: trove-only

Reference environment (proposed):
- 3 `web` replicas and 2 `worker` replicas, each limited to 2 vCPU and 4 GiB;
- PostgreSQL with 4 vCPU and 16 GiB on SSD, at most 1 ms round trip, with PgBouncer in
  transaction mode in front of the `main` class (R-ARCH-019);
- Valkey 9.1 with one primary, two replicas and three Sentinels, 1 vCPU each, in the same cluster;
- S3-compatible storage in the same region;
- the load generator inside the cluster.

The expected load exceeds what one replica can carry (INV-12). The per-replica targets below are
therefore only half of the picture; P-16 checks that capacity grows with the replica count.

Latencies are measured at the replica.

| ID | Scenario | Target (proposed) |
|---|---|---|
| P-1 | Local or cached remote binary ≤ 1 MiB, time to first byte | p50 ≤ 25 ms, p99 ≤ 100 ms (one PostgreSQL lookup plus the object GET) |
| P-2 | Same request, trove's own share without the object GET | p99 ≤ 10 ms |
| P-3 | Package document from the rendition cache (≤ 1 MiB, gzip) | p50 ≤ 5 ms, p99 ≤ 25 ms |
| P-4 | Package document, rendition miss (1 MiB) | p99 ≤ 150 ms |
| P-5 | Virtual merge, two cached members, merge not cached, 1 MiB result | p99 ≤ 200 ms |
| P-6 | Overhead of a remote miss beyond upstream time and transfer | p95 ≤ 50 ms |
| P-7 | Throughput, documents from the rendition cache | ≥ 2,000 requests/s per replica at p99 ≤ 50 ms |
| P-8 | Throughput, cached 50 KiB binaries | ≥ 1,000 requests/s per replica |
| P-9 | Streaming, files ≥ 100 MiB | ≥ 300 MiB/s per replica at ≤ 1.5 cores |
| P-10 | Concurrency | ≥ 5,000 open connections and ≥ 500 concurrent transfers per replica; ≤ 256 KiB memory per transfer |
| P-11 | Upload finish after the last byte arrives | ≤ 1 s + size ÷ 150 MiB/s |
| P-12 | Collapsing | N concurrent identical misses → 1 upstream request in the cluster while Valkey is available; at most 1 per replica while it is not |
| P-13 | Propagation of changes in change areas (R-ARCH-018), from the return of the writer's `COMMIT` until another replica no longer serves the old value (Q-ARCH-220) | p99 ≤ 100 ms while Valkey is available; enforced by the nightly performance run, per change p50 ≤ 20 ms and delivery (Q-ARCH-220); p99 ≤ `cluster.epoch_poll_interval` + 1 s while it is not |
| P-14 | Job pick-up after enqueue | p99 ≤ 1 s while Valkey is available; enforced by the nightly performance run, per change p50 ≤ 100 ms (Q-ARCH-220); p99 ≤ `jobs.poll_interval` + 1 s while it is not |
| P-15 | Process start to ready (10,000 repositories, 50,000 policy grants) | ≤ 15 s |
| P-16 | Scale-out: throughput of P-7 and P-8 with 6 `web` replicas compared with 3 | ≥ 1.8 × at the same p99 |
| P-17 | Latency added by a Valkey outage, after the breaker has opened | p99 of P-1 and P-3 within 5 % of the baseline |

Database budget per request, used to size P-1 to P-8:
- local download: 1 query, the item lookup, which also reads the rows the format's serve-time check decides on (the
  tombstone of npm and NuGet, the NuGet version row, Q-ARCH-291) and the verdict of the vulnerability scanning
  ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-015);
- remote hit: 1 query, the same lookup, which also reads whether the artifact's cache entry exists and, for npm, the facts
  of the remote version (Q-ARCH-291) that the artifact gate (R-NPM-025, R-NPM-026, Q-NPM-13) and the serve check decide on; a hit
  of a package whose document is expired or was validated before a restore revalidates the document first and costs the
  statements of that revalidation besides;
- file of a virtual repository: 1 query per member the resolution asks until one has the file (R-REPO-019), the last of them
  being the hit above, so a file of the second member costs 2 (Q-ARCH-290);
- computed local document: 1 query (`rev_seq`) when the rendition is cached;
- virtual merge: 1 query for local `rev_seq` values, plus 1 per remote member;
- token and policy checks: 0 queries while their caches are valid;
- per replica, not per request: one change-log poll per `cluster.epoch_poll_interval`;
- a mirror's pass, not a request: per package one transaction that deletes its pending row and adds to the counters, per
  artifact the fill of a remote miss and one primary-key read of the usage counter, and per discovered page one
  transaction ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-052, R-REPO-053); its fills meet the budget
  of a remote miss and run in the process that claims the queue `mirror`, outside the client path.

Valkey budget per request: local download and remote hit 0 round trips; remote miss up to 3
(negative-cache lookup, lock acquire, release with the outcome); a route with a cluster-scoped rate
limit 1. A credential that misses the replica's cache costs 1 round trip before the PostgreSQL
lookup (the throttle check, pipelined with the exemption-marker read) and 1 after the PostgreSQL
answer (the failure count or the marker refresh;
[identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038). A credential that
the replica's cache answers costs none.

Measurement:
- `perf/` holds k6 scripts and a Go driver for P-12. The full run of Test and Release on the commit that closes the phase runs them on a staging cluster
  sized like the reference, and runs P-16 with the `web` Deployment scaled from 3 to 6 replicas.
- Pull requests run Go micro-benchmarks for `Match`, `Rewrite`, `Merge` and `authz.Decide`. A
  `benchstat` regression above 10 % produces a warning.

Acceptance criteria:
- The report of that run shows P-1 to P-17. A miss of a target in that run opens a regression issue.
- The database budget per request is counted on the PostgreSQL log, per route (`test/integration/querybudget`,
  `TestR_ARCH_027_ADownloadCostsOneStatement`): an npm tarball of a local repository, its raw view, an npm tarball of a
  remote repository (hit), the same of the local and the remote member of a virtual repository, and the same five for NuGet,
  each with the statements it ran and the budget above.

### R-ARCH-028 · Resource limits for untrusted package content
Priority: **MVP** · Covers: trove-only

- Every upload and every upstream response is untrusted input.
- `Inspect` reads only the archive entries it needs, in memory. Entry names are never used as
  file-system paths, and nothing is unpacked to disk. Defaults, which a format can only tighten:
  - 10,000 entries;
  - 64 MiB of uncompressed bytes read;
  - a compression ratio of 100:1 per entry;
  - 1 MiB per manifest entry;
  - a 30 s context deadline.
- The bounds the list leaves open have defaults of their own, which a format can only tighten
  (Q-ARCH-217): JSON nesting of 64 levels and 2,000,000 values per document, XML nesting of 64
  elements, and 2 GiB of inflated bytes for a pass that keeps nothing.
- `internal/formats/untrusted` holds the helpers that enforce these bounds, and formats read
  untrusted bytes through them: `Limit` and `ReadAll` (a byte cap that fails instead of cutting),
  `DecodeJSON` and `DecodeXML` (byte cap, then a scan for depth and values or for declarations, then
  the decoder), `NewTarGz` and `NewZip` (entry count, declared and read bytes, ratio, duplicate
  names, links, and for zip the names of the central directory against those of the local headers)
  and `CountTarGz` (the streaming pass). A bound that is crossed answers `413`; a structure the
  helpers refuse answers `422`. A format maps both to the codes of its own protocol.
- These limits bind entries whose content is read into memory. A format MAY add a streaming pass
  that only counts entries and sums their sizes without keeping content, for example to record
  npm's `dist.unpackedSize`. That pass uses the format's own finite limits, declared on its spec
  page ([formats/npm.md](formats/npm.md) R-NPM-007, [formats/nuget.md](formats/nuget.md)
  R-NUGET-005), and runs within the same deadline.
- Archive entries that two readers could resolve differently (duplicate names, link entries in
  place of a manifest, ZIP central-directory and local-header names that disagree) are rejected by
  the format's `Inspect`; the format pages define the rule per archive type.
- A document held in memory is capped by the smaller of the format's `MaxDocumentBytes` and the
  proxy's metadata size limit ([repositories-and-proxy.md](repositories-and-proxy.md)).
- JSON and XML decoders run with depth limits. XML document type declarations are rejected, and no
  entity is expanded.
- A panic in a format handler, a storage hook, `Rewrite`, `Merge` or a job handler is recovered. The
  request answers `500` and the job is retried. The process never crashes, and the log names the
  format and route.
- `GOMEMLIMIT` follows the container memory limit, as the chart sets it
  ([ha-and-helm.md](ha-and-helm.md)).

Acceptance criteria:
- The conformance kit's crafted inputs are rejected within 5 s with `413` or `422`. The replica's
  RSS stays within 512 MiB of its baseline. The inputs are a `.nupkg` that expands to 10 GiB, an npm
  tarball with a 2 GiB `package.json`, a JSON document nested 100,000 deep, and a `.nuspec` with a
  DOCTYPE.

## Deferred

| Catalog ID | Priority | One line |
|---|---|---|
| HA-011 | P2 | Active-passive warm standby: trove has only active-active replicas. |
| HA-013 | Later | Internal service registry and health-gated routing: one process per replica, nothing to route internally. |
| HA-014 | P2 | Availability-zone affinity for internal routing: replicas never call each other (R-ARCH-002). |
| HA-018 | P2 | Internal TLS from a built-in certificate authority: `server.tls` or a service mesh covers the MVP. |
| STOR-007 | P2 | Composable storage provider chain: one S3 implementation behind one interface. |
| STOR-009 | P2 | Node-local read cache: can be added as a `BlobStore` decorator without changing callers. |
| STOR-016 | P2 | Asynchronous upload to the persistent store: would change what `201` means (INV-4 writes synchronously). |
| RMT-025 | P2 | Eager fetching of companion files: a `RemoteAdapter` extension that enqueues jobs. |
| RMT-046 | P2 | Parallel chunked download of large upstream files. |
| REPO-039 | P2 | Uncached retrieval through a virtual repository for requests from other instances. |
| OPS-041 | P2 | Advanced runtime properties: every key is in the schema (R-ARCH-020); there are no hidden knobs. |
| OPS-042 | P2 | Central configuration descriptor: configuration is split between the file and PostgreSQL by rule (R-ARCH-020). |
| OPS-064 | P2 | Load protection per activity: the MVP has the per-replica in-flight limit and memory guard (R-ARCH-005), which treat every request alike (H-322), and the rate limits of [api-and-ui.md](api-and-ui.md), whose counters can be cluster-wide (R-ARCH-034). |

## Security notes

- **Shared origin.** In the MVP, the UI, its session cookie and every package file share one origin
  (R-ARCH-007).
  - The content headers stop an uploaded HTML, SVG or XML file from becoming stored XSS against
    every logged-in user. This is live on `/files/` and on every format route that serves content or
    metadata verbatim.
  - The core sets the headers in a wrapper the handler cannot bypass, and the conformance kit tests
    them.
  - From P1, an optional content origin (R-ARCH-029) moves browser-facing content off the UI's
    origin. The residual risk is H-3.
- **Signed content URLs (P1).** A signed content URL is a read capability for one path, bound to
  one session and valid for `server.content_url_ttl`. It is checked against the session and the
  current grants at every use. Content responses send no `Referer`. A link copied out of the browser
  therefore stops working at logout, at grant removal, or once `content_url_ttl` has passed (five
  minutes by default).
- **Forwarded headers.** These are evaluated only in middleware (R-ARCH-006) and only from trusted
  proxies. No handler can derive links or client addresses from attacker-supplied headers.
- **Path canonicalisation.** It happens once, before authorization (R-ARCH-005). Encoded traversal
  (`%2e%2e`) gets past ServeMux and is rejected by `CleanPath`.
- **Upload authorization before the body.** The coarse check runs before the first body read
  (R-ARCH-005). An unauthenticated caller cannot make a replica spool data.
- **Declared identity is authorized.** Fine-grained authorization runs on the identity `Inspect`
  reads from the content, not on the URL (R-ARCH-014). A token scoped to `@acme/*` cannot publish
  `@other/lib` by choosing the URL.
- **Stored secrets.** Sealed secrets are bound to their row by GCM additional data (R-ARCH-021). A
  principal with database write access cannot move one remote's credentials to another. A principal
  with read access gets no plaintext, and no secret fingerprint is unkeyed.
- **Egress at connect time.** The egress policy runs in the dialer (R-ARCH-022). A repository
  manager (`manage` can be delegated, INV-9) cannot reach the cloud metadata address through DNS
  rebinding.
- **Change messages are early eviction, not correctness.** Revocation-sensitive caches belong to
  their pages, which give them self-validating keys or short safety TTLs. Revocations use change
  areas: the PostgreSQL change log applies them on every replica within
  `cluster.epoch_poll_interval`, whatever Valkey does, and the `invalidation` readiness check takes a
  replica whose poll has stalled out of the Service (R-ARCH-018).
- **Valkey is outside the trust path.** No content, metadata or credential that trove serves, no
  positive authentication or authorization result and no key material is read from Valkey
  (R-ARCH-030, Q-ARCH-16); a Valkey value can at most make trove refuse with `404` or `429`, or,
  as a throttle exemption marker, cost one more PostgreSQL lookup for one credential. A message
  from Valkey can only evict early; it never advances a replica's applied change epoch, so a forged
  or suppressed message cannot hide a revocation (R-ARCH-018). Values read from Valkey are validated
  before use. What remains is availability: H-49.
- **Valkey credentials and transport.** trove connects to Valkey and to the Sentinels with TLS by
  default and verifies the server certificate, with its host name under the default `verify-full`
  and without it under `verify-ca`. There is no mode that encrypts without verifying; TLS can be
  switched off only as `valkey.tls.mode: disable` together with `valkey.allow_insecure: true`
  ([ha-and-helm.md](ha-and-helm.md) R-HA-056). trove authenticates as an ACL user that can reach
  only its own key and channel prefix and is denied scripting (R-ARCH-031). Passwords come from
  mounted files and are re-read on reconnection.
- **No session state on the request path.** The `main` class keeps no state beyond its transaction
  (R-ARCH-019), so a transaction-mode pooler cannot leak one request's session settings into
  another's.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-ARCH-1 · HTTP router

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `net/http.ServeMux` | Standard-library mux for every top-level route and for the `/api/v1/` operations; each format matches the escaped remainder itself | No dependency. Method patterns with automatic `405`. A panic on conflicting patterns at registration. `%2F` stays inside one segment (verified with go1.27.3). Sets `http.Request.Pattern`, which the API's authorization lookup keys on ([api-and-ui.md](api-and-ui.md) R-API-002). | No route groups, so per-group middleware is a handler wrapper |
| (b) chi v5.5.0 | A third-party `net/http`-compatible router for every route | Route groups and a mature middleware set | A dependency on the hottest path. It does not solve the ambiguity of npm scoped names, so formats still parse their paths themselves. |
| (c) Echo or Gin | A framework with its own handler and context type | Binding and validation helpers, a large ecosystem | Every handler is tied to a non-stdlib context. The stall deadlines of R-ARCH-005 via `http.ResponseController` go through the framework's writer wrapper. |
| (d) Hybrid | ServeMux at the top, with chi as a sub-router for `/api/v1/` only | Route groups where the management API has many routes | Two routing semantics in one binary and two places where conflicts are detected. The gain is syntax only. |

**Choice:** (a) — `net/http.ServeMux` serves every route, and each format resolves its own escaped path behind its literal prefix.

**Rationale:**
- The hard routing problem is in the package protocols: npm's `@scope%2fname` versus
  `/{package}/{version}`, and NuGet's lower-cased paths. No pattern router resolves those. The
  format's own `Match` on the escaped path does (R-ARCH-005, R-ARCH-008). A router library would
  therefore only ever serve `/api/v1/`.
- For `/api/v1/`, [api-and-ui.md](api-and-ui.md) Q-API-1 chose oapi-codegen's `std-http-server`
  output, which registers ServeMux patterns. R-API-002 looks up each operation's authorization
  declaration by `http.Request.Pattern`, which ServeMux sets. (a) is what those decisions already
  build on. (b) to (d) would need another generator mode and a re-check of that lookup, without
  routing anything (a) cannot.
- (a) adds no dependency, and its conflict detection at registration means a route collision cannot
  ship.
- Revisit if the generated `std-http-server` interface cannot express an operation the API needs.
  The fallback is (d), with chi below ServeMux for `/api/v1/` only, never for the protocol routes.

**Applied in the spec:**
- R-ARCH-005 states that the `/api/v1/` operations are registered on the same ServeMux through the
  generated `std-http-server` interface, and that authorization declarations are looked up by
  `http.Request.Pattern`.
- R-ARCH-003 and R-ARCH-004 show the generator outputs `internal/api/v1/gen/` and
  `web/src/app/api/generated/`, and the pins of oapi-codegen (`tool` directive) and
  openapi-typescript (lockfile). Q-API-1 handed these layout edits to this page.

### Q-ARCH-2 · PostgreSQL driver and query layer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged. trove no longer uses `LISTEN` (Q-ARCH-15), so `WaitForNotification` drops out of the rationale; pgx's exec modes now also let the `main` class run through a transaction-mode pooler (Q-ARCH-14).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) pgx + sqlc for every query | pgx v5.11.0 native API; every query is a `.sql` file compiled by sqlc v1.31.1 | SQL is checked against the migrations at generation time. Locking-sensitive SQL stays reviewable. | Optional filters need `sqlc.narg` and `col = $1 OR $1 IS NULL` patterns, which a cached generic plan cannot prune. A sort key chosen by the caller needs `CASE` in `ORDER BY`, which no index can serve for a cursor. |
| (b) pgx, handwritten | pgx native API, every query written by hand in Go | Full control | No generation-time check of the large majority of queries, which are static |
| (c) `database/sql` + ORM | GORM, ent or bun | Fast CRUD | Hides exactly the SQL this design depends on (`SKIP LOCKED`, advisory locks, `ON CONFLICT`). Loses COPY and pgx's exec modes. |
| (d) pgx + sqlc + a small list builder | sqlc for every static query; dynamic list and search queries built in one package with bound values and allowlisted identifiers | Keeps the generation-time check where it fits and plain SQL where it does not. Injection is ruled out by construction. | A second, small query path to maintain and test |

**Choice:** (d) — pgx's native API, with sqlc for every static query and one confined builder, `listq`, for the dynamic list and search queries.

**Rationale:**
- pgx provides what the design needs: `pgxpool`, COPY and the exec modes (verified in its source).
  The exec modes let the `main` class work behind a transaction-mode pooler, including one without
  prepared-statement support (`default_query_exec_mode=exec`, R-ARCH-019).
- sqlc keeps the static, locking-sensitive SQL checked and readable in review.
- The cursor-paged, filterable lists of [api-and-ui.md](api-and-ui.md) (Q-API-6) are where (a)
  breaks down: each combination of filters and sort key needs its own plan and its own index order.
  A builder confined to one package emits exactly that SQL, with bound values and allowlisted
  identifiers, without giving up sqlc anywhere else.
- A general query-builder library was considered as a variant of (d). The fragments are a handful
  of allowlisted `WHERE` and `ORDER BY` pieces per endpoint, so a library would add a second SQL
  dialect to review and save little code.
- (c) hides the SQL the HA design relies on. (b) gives up the generation-time check for the static
  majority.
- Revisit if `listq` grows beyond list and search endpoints. That would mean query logic is leaking
  out of sqlc for reasons other than optional filters.

**Applied in the spec:**
- R-ARCH-019 names pgx's native API, splits queries into static ones (sqlc,
  `internal/meta/postgres/q`) and dynamic list and search queries (`internal/meta/postgres/listq`,
  bound values, allowlisted identifiers, no SQL concatenation elsewhere), and has an acceptance
  criterion for both rules.
- R-ARCH-003's layout tree shows `listq/`.
- 2026-10-28: the rationale no longer names `WaitForNotification`, and option (c) no longer counts
  its loss; R-ARCH-019 states the exec-mode setting for poolers without prepared-statement support.

### Q-ARCH-3 · Migration runner

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) goose v3.29.0 Provider API | goose applies the embedded SQL files under trove's `migration` lock. R-HA-018's `schema_migration` table is written through a custom `database.Store`. | A tested apply loop and SQL parser. `embed.FS` sources. An *optional* locker, so trove's lock stays the only one (verified in its source). | Its version write carries only the version number (`InsertRequest{Version}`), so R-HA-018's row needs a custom `database.Store` under `DialectCustom` that follows goose's version-0 convention. Every file also needs `-- +goose Up` next to trove's headers. Brings `database/sql` and `pgx/v5/stdlib` into the binary for this one use. |
| (b) golang-migrate v4.20.1 | The widely used migration library | Many drivers and a large user base | A failed migration leaves the version marked dirty and needs a manual `force` before the next deploy. It takes its own lock, outside trove's lock categories. Its version table cannot hold R-HA-018's columns. |
| (c) tern v2.4.3 | pgx-native migrator | Same author as pgx. Go and SQL migrations, a `disable-tx` magic comment, `fs.FS` sources ([README](https://github.com/jackc/tern)). | Takes `pg_advisory_lock(9628173550095224)` on every run: a fixed single-`bigint` key that R-ARCH-017 forbids, with no switch (verified in its source). Its version table holds one number. |
| (d) A trove-owned runner on native pgx | A small runner (a few hundred lines, estimated) in `internal/meta/postgres`. Each embedded file runs as one argument-less `Exec` inside a transaction, together with its R-HA-018 row. | Writes R-HA-018's table directly, in the step's own transaction. PostgreSQL parses the SQL, because pgx sends an argument-less `Exec` through the simple protocol (verified), so trove needs no parser. One header dialect. No `database/sql`. | The apply loop is trove's own code and is exercised only by trove's tests |

**Choice:** (d) — a trove-owned runner on native pgx that applies each embedded file and its R-HA-018 `schema_migration` row in one transaction; this revises the earlier choice of goose.

**Rationale:**
- [ha-and-helm.md](ha-and-helm.md) R-HA-018 and R-HA-019 already make trove own the substance of
  the upgrade path:
  - the version table with class, release, checksum and duration;
  - checksum verification;
  - the per-step lock timeout and retries, and the clean-up of invalid indexes;
  - the compatibility floor and the contract guard.

  A library can only add the apply loop and a SQL parser.
- The parser is not needed. pgx sends an argument-less `Exec` through the simple protocol, and
  PostgreSQL parses a file of several statements itself, inside the open transaction (verified in
  pgx v5.11.0).
- The loop is where R-HA-018's row, lock timeout and retries have to go anyway. Under (a), the
  same code becomes a goose `database.Store` that receives only a version number and must follow
  goose's version-0 convention, plus a wrapper around `UpByOne`. That is as much code as (d), and
  it is coupled to a library's internals.
- (b) and (c) bring their own locks outside R-ARCH-017's categories; (c) uses a fixed
  single-`bigint` key. (b) adds a manual dirty-state repair during an outage.
- The risk of own code on the upgrade path is covered by tests that run on every change:
  - every migration against each supported PostgreSQL major (R-HA-013);
  - the N-1 compatibility test (R-HA-018);
  - the crash and checksum criteria of R-ARCH-026.
- The earlier choice of (a) assumed that goose's own version table could stand in for R-HA-018's.
  It cannot, and adapting goose to it costs what it saves.
- Revisit if trove needs Go-code migrations inside the migration Job. R-HA-018 moves data changes to
  background migrations, so none are planned.

**Applied in the spec:**
- R-ARCH-026 is rewritten:
  - it defers tables, classes, checksums, retries and the guard to R-HA-018, R-HA-019 and R-HA-022;
  - it defines the file headers `phase`, `no-transaction` and `requires-app`, and maps them to
    `lastUsedBy` and `compat_floor`;
  - it specifies the runner's steps on one connection under `pg_advisory_lock(3, 0)`;
  - it removes the `schema_compat` table, the library's version table and the `--allow-downtime`
    override. Overriding the guard while old replicas are live would let a contract step break
    them. R-HA-021's downtime path, scaling to zero, makes the guard pass without an override.
  - it adds acceptance criteria for crash atomicity, edited files, the header lint and the absence
    of `database/sql`.
- R-ARCH-019: `database/sql` and `pgx/v5/stdlib` are not used anywhere.
- R-ARCH-003: the runner lives in `internal/meta/postgres`.
- Verified-facts table: goose's version-row and annotation rules, tern's fixed lock key, and pgx's
  simple protocol for an argument-less `Exec`.

### Q-ARCH-4 · S3 client library

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) aws-sdk-go-v2 `service/s3` v1.114.6, trove-owned multipart | The AWS SDK's S3 client; trove uploads the spool file in parts through `io.SectionReader` | AWS S3 is a primary target. Workload identity through the default credential chain (INV-13). First-class presigning and KMS. A stable 1.x module. Every call [storage.md](storage.md) R-STOR-030 lists is directly available. | About 150 lines of multipart code owned by trove (estimated) |
| (b) aws-sdk-go-v2 + `feature/s3/transfermanager` v0.5.1 | The SDK's new transfer helper | Ready-made multipart upload | Pre-1.0. The previous `feature/s3/manager` Uploader is already deprecated in its favour. A helper between trove and the part-level calls R-STOR-006 needs. |
| (c) minio-go v7.4.1 | The MinIO client library | A stable major, tuned for S3-compatible stores | Implements its own credential providers. Each AWS mechanism trove relies on (workload identity, KMS) has to be re-checked against it. |
| (d) A portable blob API (Go CDK `blob` with its S3 driver) | One API over S3, GCS and Azure | Other object stores without new code | Lowest common denominator. It exposes neither per-part checksums nor the multipart lifecycle (part size, part budget, listing and aborting incomplete uploads) that R-STOR-006 and the orphan sweep of R-STOR-026 require. The owner fixed S3, and trove's `BlobStore` interface is already the seam for other stores (STOR-007, P2). |

**Choice:** (a) — the stable `service/s3` client, with a trove-owned multipart upload that reads its parts straight from the spool file.

**Rationale:**
- R-STOR-006 needs control that the helpers do not give:
  - parts read through section readers on the spool file;
  - a part size raised to fit 10,000 parts;
  - a per-replica cap on concurrent parts;
  - a SHA-256 checksum on each part;
  - `AbortMultipartUpload` on failure;
  - listing of incomplete uploads for the orphan sweep (R-STOR-026).

  `service/s3` exposes every one of these calls. (b) and (d) put a helper between trove and them,
  and (b) is pre-1.0 while its predecessor is already deprecated.
- (a) and (c) are both stable majors. (a) wins on credentials: the AWS SDK's default chain covers
  workload identity (IRSA and similar, INV-13) as AWS maintains it, while minio-go's own providers
  would have to be re-checked for each mechanism.
- S3-compatible stores need `UsePathStyle`, an explicit endpoint and region, and the checksum
  options set to "when required" (verified in the SDK source). [storage.md](storage.md) R-STOR-030
  exposes these settings.
- Revisit, by switching to (c), only if the storage integration suite of R-STOR-030, with the store
  set of [ha-and-helm.md](ha-and-helm.md) Q-HA-3, shows the AWS SDK failing on a store trove must
  support.

**Applied in the spec:** No change; the requirement text already follows this option (R-ARCH-002, [storage.md](storage.md) R-STOR-006 and R-STOR-030).

### Q-ARCH-5 · Job queue implementation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged. A Valkey-based queue was added as option (c) and rejected. Only the `worker` role claims jobs, wake-ups come from Valkey with polling as the bound, and the release of running jobs on shutdown is spelled out.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The queue of R-ARCH-016 | One `job` table, claimed with `FOR UPDATE SKIP LOCKED`; migrations in trove's single migration path | Carries the operation columns of [api-and-ui.md](api-and-ui.md) and the task slots of [operations-and-observability.md](operations-and-observability.md). Rolling-upgrade safe through claiming by known kind. Claims are short transactions that work through a transaction-mode pooler (R-ARCH-019). | trove owns the reaper, backoff and scheduler code |
| (b) River v0.50.0 | pgx-native queue library (MPL-2.0) | Mature features, an admin UI | Pre-1.0. It manages its own tables and migrations (`rivermigrate`), a second migration path next to INV-3's single one. Its schema does not hold the operation and task-slot columns. |
| (c) A queue in Valkey (Streams with consumer groups) | Jobs are stream entries; workers read them through a consumer group and acknowledge them | Pushes jobs to workers without polling; takes the claim load off PostgreSQL | INV-3 puts the durable job queue in PostgreSQL: a Sentinel failover can lose acknowledged writes ([Sentinel](https://valkey.io/topics/sentinel/)), and a lost job is lost work. An enqueue could no longer commit atomically with the change that causes it. The operation and task-slot columns would need PostgreSQL anyway. |

**Choice:** (a) — the PostgreSQL job table and claim query of R-ARCH-016, owned by trove.

**Rationale:**
- INV-3 has one migration path, and the job row is shared by three consumers: maintenance tasks,
  long-running API operations and internal work.
- (b) would split the schema and still need a side table for those columns.
- (c) breaks INV-3 for the one piece of state where loss means lost work, and it gives up the
  enqueue in the same transaction as the change.
- The queue code is small, and the claim query is given in full on this page.
- The horizontal-scale constraint does not press on the queue: `web` replicas only insert rows, and
  only the few `worker` replicas claim. `locked_until` already is the visibility timeout, and the
  heartbeat keeps it current, so a drained or lost worker gives its jobs back without coordination.
- Revisit if webhook delivery (P1) produces job volumes far above maintenance work and the claim
  query shows up in `pg_stat_statements` as a top consumer.

**Applied in the spec:**
- 2026-10-28, R-ARCH-016:
  - only `worker` (or `all`) replicas run the claim loop, the reaper and the scheduler;
  - `locked_until` is named as the visibility timeout;
  - the release statement on shutdown, which does not count the interrupted attempt;
  - wake-ups through the signal area `jobs` in Valkey, with `jobs.poll_interval` as the bound;
  - the watched loops `cluster.leases`, `cluster.changes` and `cluster.signals`;
  - the kind `cluster-change-prune`;
  - acceptance criteria for a drain during `gc-full` and for pick-up without Valkey.
- R-ARCH-001's role table lists which role runs the queue's loops.

### Q-ARCH-6 · Cross-replica collapse holds a connection for the whole upstream transfer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice changed from (a), the PostgreSQL session advisory lock on a lock pool, to (d), a lock in Valkey with a TTL that the holder extends. No PostgreSQL connection is held during a fetch any more, and the lock pool is gone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Advisory lock, bounded pool, bypass | The earlier choice: INV-11's session lock on a dedicated lock pool. When the pool stays exhausted for 100 ms, fetch without the cross-replica lock and count it. | Released by PostgreSQL as soon as the holder's connection closes. No heartbeat. | Pins one PostgreSQL session for the whole transfer. Every `web` replica needs `proxy.lock_pool_size` (16) direct session connections, so connections grow with the replica count, against INV-12, and a transaction-mode pooler cannot carry them. INV-11 now names a Valkey lock. |
| (b) Lease row with heartbeat | A `fetch_lease` row, renewed while the transfer runs; waiters watch the row | Holds no connection | A PostgreSQL write per fetch start, per heartbeat and per release: hot-path writes and row churn on the system of record, for an optimization |
| (c) Collapse within a replica only | Single-flight per process, no cross-replica lock | No shared state | n replicas cause up to n identical upstream fetches, and n grows with the load. Breaks P-12 and multiplies upstream rate-limit use. |
| (d) Valkey lock with TTL and extension | `SET … NX PX` to acquire, `SET … IFEQ` to extend every third of the TTL, `DELIFEQ` to release; waiters subscribe to the flight's channel and poll; the in-process single-flight stays in front; without Valkey, (c) | Holds no PostgreSQL connection while acquiring, waiting or transferring. Works with any pooler. A crashed holder frees the flight within the TTL. Valkey's own commands do compare-and-extend and compare-and-delete, so no script is needed. | A holder killed without shutdown delays waiters by up to the TTL, where (a) released at once on a closed socket. A failover can lose the lock (asynchronous replication, [distributed locks](https://valkey.io/topics/distlock/)); the result is a duplicate fetch. |

**Choice:** (d) — a Valkey lock per flight with a 30 s TTL that the holder extends, behind `cluster.FlightLock`, with the in-process single-flight in front and collapsing per replica as the fallback.

**Rationale:**
- Connections decide it. Under (a) every `web` replica holds up to 16 lock-pool connections plus a
  listener, all of them direct sessions: at 20 replicas that is 340 connections a pooler cannot
  absorb. (d) needs none, which is what lets the `main` class run behind a transaction-mode pooler
  (Q-ARCH-14).
- The lock is an optimization and stays one (R-REPO-026): blob commits are idempotent by content
  address, and the entry upsert keeps the newer metadata and refuses a different digest. Losing the
  lock in a failover therefore costs one duplicate upstream fetch. That is exactly the loss INV-3
  allows Valkey to cause.
- Failure timing is comparable. A graceful shutdown releases at once (R-ARCH-024). A killed holder
  frees the flight after at most 30 s. Under (a), node loss freed it only after the server's TCP
  keepalive, about 2 minutes (R-HA-013); only a process crash with a closed socket was faster.
- (b) moves the cost into PostgreSQL writes on the hot path. (c) gives up P-12 exactly when the load
  needs many replicas.
- `cluster.FlightLock` keeps the proxy independent of the mechanism, as before.
- Revisit if `trove_flight_lock_lost_total` or the bypass counter rises under normal load, or if
  container-layer remotes (P1) show waiters timing out behind killed holders; the TTL is then the
  first lever.

**Applied in the spec:**
- New R-ARCH-033 specifies `FlightLock` on Valkey: acquire, extension, release with the outcome,
  waiting through the flight channel and polling, expiry, the bypass without Valkey, the lost-lock
  case and acceptance criteria.
- R-ARCH-013 orders the miss through the Valkey lock, and its sequence diagram shows the Valkey
  calls.
- R-ARCH-017 retires the advisory-lock category 2 `fetch` and keeps its number reserved.
- R-ARCH-019 has no lock pool, and R-ARCH-030 lists the keys and the degradation.
- R-ARCH-003's layout tree still names `FlightLock` in `internal/cluster`.
- Cross-page: [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 (steps 1, 3, 4, 5
  and 6, the lock pool and `proxy.lock_pool_size`), [ha-and-helm.md](ha-and-helm.md) R-HA-006,
  R-HA-012 and R-HA-016, and INV-11 is already worded for this choice.

### Q-ARCH-7 · Lock key form for cache fills

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the fetch lock moved to Valkey (Q-ARCH-6), so its key is now the full SHA-256 of the flight key in a Valkey key (option (c)). Option (a), the two-key form with a registered category, stays the rule for every remaining PostgreSQL advisory lock, and the category `fetch` is retired.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two-key form, category `fetch` | `pg_try_advisory_lock(2, <first 4 bytes of SHA-256 of the flight key>)` | `pg_locks` shows `classid = 2`, so the lock-diagnostics view of [operations-and-observability.md](operations-and-observability.md) and any operator can name every lock. One key space for all of trove's locks. | 32-bit hash: a collision makes two unrelated fetches wait for each other. Needs the PostgreSQL lock that Q-ARCH-6 gave up. |
| (b) Single 64-bit hash | `pg_try_advisory_lock(<bigint>)` | Collisions are practically impossible | Anonymous in `pg_locks`. A second key space next to the categories, which must never be mixed up with them. |
| (c) Valkey key with the full hash | `trove:<ks>:lock:fetch:v1:<64 hex of SHA-256 of the flight key>` | No collisions to reason about; the key says what it locks; listed by `SCAN` on the prefix | Not visible in `pg_locks`; diagnostics read the Valkey prefix instead |

**Choice:** (c) for the fetch lock — the Valkey key `trove:<ks>:lock:fetch:v1:<SHA-256 hex of the flight key>`; every remaining PostgreSQL advisory lock keeps (a)'s two-key form with a registered category.

**Rationale:**
- The 32-bit hash was a concession to PostgreSQL's advisory-lock key width. A Valkey key has no such
  limit, so the full SHA-256 removes the collision case altogether.
- The categories stay valuable for the locks that remain in PostgreSQL (lease, migration, config):
  every one of them is still named in `pg_locks`, and the category test still rejects ad-hoc keys.
- Retiring category 2 without reusing its number keeps old dashboards and runbooks from misreading
  a new lock.
- Revisit if a later mechanism needs a hashed PostgreSQL advisory lock again; it then follows (a).

**Applied in the spec:**
- R-ARCH-017's category table marks category 2 `fetch` as retired and reserved. Its test also
  rejects category 2, and it states that no category hashes its `objid` any more.
- R-ARCH-030 and R-ARCH-033 define the Valkey key with the full hash.
- The two-key rule, the single-`bigint` ban and `pg_advisory_xact_lock(4, 1)` are unchanged in
  R-ARCH-017.
- Cross-page: [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 step 1,
  [ha-and-helm.md](ha-and-helm.md) R-HA-006's lock-key registry (row `fetch`), and the lock view of
  [operations-and-observability.md](operations-and-observability.md) R-OPS-026, which shows flight
  locks from Valkey or not at all.

### Q-ARCH-8 · NOTIFY on hot-path commits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice changed from (a), a post-commit coalescer that sends `NOTIFY`, to (d), the same coalescer publishing to Valkey. trove sends no `NOTIFY` at all now; administrative changes moved to the change log of Q-ARCH-15.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Post-commit coalescer with `NOTIFY` | The earlier choice. Hot-path changes are handed to a per-replica coalescer after commit; it sends at most one `NOTIFY` per channel per 50 ms. | Delivery after commit; bounded notifying commits | Needs a `LISTEN` session on every replica: one direct session connection each, which a transaction-mode pooler cannot carry and which grows with the replica count (INV-12). Every coalescer `NOTIFY` still takes PostgreSQL's notify commit lock (`async.c`). |
| (b) `NOTIFY` inside each writing transaction | The textbook pattern | Simplest; exactly one notification per change | PostgreSQL serializes the commits of notifying transactions on one lock (`async.c`), so upload and fill throughput becomes bound to that lock. Same `LISTEN` sessions as (a). |
| (c) No notification for hot-path changes | Consumers only poll | No notification traffic | Adds up to one poll interval to every job pick-up and collapsed request |
| (d) Post-commit coalescer publishing to Valkey | The coalescer of (a), but it publishes on `trove:<ks>:sig:v1:<area>`; fill completion uses the flight's own channel (R-ARCH-033) | No PostgreSQL work at all for a signal; no session connection; wake-ups in milliseconds; fan-out bounded by the coalescer | At-most-once delivery, and nothing while Valkey is unavailable. Every consumer already stays correct without the signal (self-validating keys, polling). |

**Choice:** (d) — signals that client traffic produces leave through the per-replica coalescer to Valkey Pub/Sub after commit; trove issues no `NOTIFY`.

**Rationale:**
- The reason for (a) was PostgreSQL's notify lock. (d) removes the lock from the picture entirely
  and also removes the `LISTEN` session, which Q-ARCH-14 needs gone so that `web` replicas hold no
  session connection.
- Hot-path signals were already best effort under (a): a crash between commit and flush lost them,
  and every consumer covered that with self-validating keys or polling. Valkey's at-most-once
  delivery changes nothing for them, and a Valkey outage degrades them to their polling bound, as
  INV-12 asks.
- The coalescer stays, because it bounds the fan-out: with n publishing replicas and m subscribers,
  at most n × 20 × m deliveries per second per area, whatever the commit rate.
- Fill completion leaves the shared channel. A per-flight channel (R-ARCH-033) reaches only the
  replicas that wait for that flight, instead of every replica on every fill.
- (c) adds latency that (d) avoids at almost no cost.
- Revisit if a signal consumer turns out to need delivery guarantees. It then becomes a change area
  (Q-ARCH-15) instead of a signal.

**Applied in the spec:**
- R-ARCH-018: signal areas, the coalescer publishing to Valkey, at-most-once delivery and the rule
  that every consumer stays correct without the signal. The area table replaces the channel table,
  `trove_proxy_fetch` is gone, `upstream_state` moves to the change class, and no code issues
  `LISTEN`, `NOTIFY` or `pg_notify` (with a test).
- R-ARCH-009: `Host.Notify` takes an area, and its comment and the transaction rule say how each
  class leaves.
- R-ARCH-016: workers wake on the signal area `jobs`.
- R-ARCH-015's acceptance criteria run with Valkey stopped instead of with the coalescer disabled.
- Cross-page: [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 step 5 and the
  channel names of R-REPO-029 and R-REPO-045;
  [operations-and-observability.md](operations-and-observability.md) R-OPS-035 (signal `events`);
  [ha-and-helm.md](ha-and-helm.md) R-HA-004.

### Q-ARCH-9 · Freshness of merged metadata for local members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice is unchanged and gains weight, because requests now reach replicas at random and no signal is guaranteed. R-ARCH-015's acceptance criteria run with Valkey stopped instead of with the coalescer disabled.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `rev_seq` check on every merged hit, in the core | The merged-metadata cache records each eligible local member's `rev_seq` and checks them with one `RevSeqs` query per request | A publish is visible through the virtual repository on every replica at once, notification or not. Implemented once, so every format has it. | One indexed query per request (in the P-5 budget) |
| (b) `NOTIFY` eviction only | Merged entries are dropped when the metadata-change notification arrives | No extra query | A publish-then-install CI job that hits another replica before the notification gets the old document. A lost `LISTEN` session widens the gap to the safety TTL. |

**Choice:** (a) — the core's merged-metadata cache checks the `rev_seq` of every eligible local member on each hit, mandatory for every format.

**Rationale:**
- "Publish in one job step, install in the next" is the most common CI pattern, and (b) makes it
  flaky in exactly the HA setup the owner requires.
- The check is one indexed query on (repository, name) for all local members together, and it is
  already in the database budget of R-ARCH-027.
- Putting the check in the core rather than in each `Merge` means later formats cannot forget it.
  This works only if the core can read the counter without format code. The counter therefore
  lives on the generic `packages` row and not in a format's side table, as the npm and NuGet pages
  currently have it.
- A member that does not yet hold the package is recorded with `rev_seq` 0. Otherwise the first
  local publish of a name that so far came only from a remote member would stay invisible until
  the merged entry expired.
- Revisit only if P-5 is missed and profiling shows `RevSeqs` as the cause.

**Applied in the spec:**
- R-ARCH-015:
  - raises the rule from SHOULD to MUST for every format;
  - places the check in the core's merged-metadata cache;
  - records every eligible local member, with 0 for a member without the package;
  - has acceptance criteria for npm, NuGet, the `formattest` fake format and a first local publish
    of a name a remote member serves, run without any cross-replica signal (since 2026-10-28: with
    Valkey stopped).
- R-ARCH-009 places `rev_seq` on [storage.md](storage.md)'s `packages` row, and `RevSeqs` documents
  the 0 for an absent package.
- R-ARCH-010 adds kit check 9, which fails a write that forgets to increment `rev_seq`.
- [storage.md](storage.md), [formats/npm.md](formats/npm.md) and [formats/nuget.md](formats/nuget.md)
  get the matching cross-page edits.

### Q-ARCH-10 · Separate content origin for package content a browser can render

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

H-3 records that the UI and package content share one origin. The question is whether trove should
serve browser-renderable content from a separate, configurable content host: in the prototype,
later, or never. Such content is raw files under `/files/`, and README, license, icon and other
package files.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Never | One origin for good; the content headers are the only defence | Simplest: one host name, one certificate | H-3 stays forever. Inline viewing of files (ART-045, HTML reports in generic repositories) can never be offered safely. |
| (b) Prototype, mandatory | Every installation needs a second host name; browser content is served only there | Removes the shared-origin class from day one | Every installation, evaluation included, needs a second DNS name, certificate and edge route, plus the signed-URL flow in the prototype. Package clients gain nothing. |
| (c) Prototype, optional | The content origin is built into the prototype, off by default | Hardening is available from the start | Prototype effort and a second test matrix for a switch that is off by default, while the default path already has three independent header layers against script |
| (d) MVP: one origin with core-enforced headers and a link seam; P1: optional content origin | The MVP keeps R-ARCH-007's headers, now set by the core so no handler can drop them, plus `Cross-Origin-Resource-Policy`, and builds every content link in one place. P1 adds `server.content_base_url` (R-ARCH-029); inline browsing (P2) builds on it. | No cost to installations that do not need it. The P1 switch touches neither formats nor the UI. It keeps the door open for ART-045. | Until P1, and wherever the switch stays off, the defence remains header-based (H-3) |

**Choice:** (d) — the MVP keeps one origin with content headers that the core enforces and one place that builds content links, and the optional content origin is P1 requirement R-ARCH-029.

**Rationale:**
- For content that must not render, the current guidance on hosting user data
  ([web.dev](https://web.dev/articles/securely-hosting-user-data)) recommends `nosniff`,
  `Content-Disposition: attachment`, CSP `sandbox` and `default-src 'none'`, and
  `Cross-Origin-Resource-Policy`. It calls a separate sandbox domain necessary only for content that
  has to render, or for a threat model that includes renderer compromise and Spectre-class attacks.
  trove renders no package content in the MVP.
- `attachment` and CSP `sandbox` each stop script execution on their own in current browsers. The
  first prevents rendering; the second gives a rendered document an opaque origin without script.
  `nosniff` keeps a harmless declared type from being sniffed into an active one. The remaining
  failure mode the MVP controls, a handler that omits the headers, is closed by setting them in the
  core.
- None of these headers affects package clients. npm, pnpm, Yarn, dotnet and nuget.exe ignore them,
  and the UI loads package images with `img`, which `attachment` and the CSP do not block and
  `same-site` permits.
- The residual cases need either an edge that strips response headers or a browser that ignores
  them. Those are deployment-level and are what R-ARCH-029 covers when an operator enables it.
- (b) and (c) charge every installation, or the prototype, for that residual.
- (a) would close the door on inline viewing, which needs a separate origin.
- Cookies are isolated by host name, not by port or origin. R-ARCH-029 therefore requires a content
  host name that differs from the UI's.
  - [identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-2 gave every trove
    cookie the `__Host-` prefix, so a subdomain content host can neither receive nor plant them.
  - Such a host is still same-site with the UI, so R-ARCH-029 accepts it with a start-up warning
    and recommends a separate registrable domain.
- Revisit and pull R-ARCH-029 into the MVP if any of these happens:
  - the prototype has to render a package-provided file inline;
  - a target installation's edge is known to rewrite response headers;
  - a header bypass is reported in a supported browser.

**Applied in the spec:**
- R-ARCH-007:
  - adds `Cross-Origin-Resource-Policy: same-site`;
  - makes the core, not the handler, set the content headers through a writer wrapper the handler
    cannot weaken;
  - requires every browser-facing content link to come from `LinkBuilder` or `downloadUrl`;
  - points at P1 R-ARCH-029;
  - has acceptance criteria for the wrapper and for URL-building in the UI.
- New R-ARCH-029 (P1, trove-only) specifies `server.content_base_url` and
  `server.content_url_ttl`:
  - origin checks at start-up: an `https` origin, a host name other than the UI's (a different port
    is refused), and a warning for a host on the UI's registrable domain;
  - host-based routing;
  - a `303` hand-off of browser requests that carry the session cookie or a non-`empty`
    `Sec-Fetch-Dest`, whatever other credential they carry, while package clients stay on the UI
    host;
  - session-bound HMAC URLs keyed from R-ARCH-021's key ring;
  - `Referrer-Policy: no-referrer`;
  - chart and CSP hooks;
  - acceptance criteria, with the `__Host-` cookie names of Q-AUTH-2.
- R-ARCH-010 check 7 also fails a handler that removes the headers, and a content body on a route
  of another kind.
- The security notes and H-3 are updated.

### Q-ARCH-11 · URL layout and port numbers (INV-6)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A consistency review asked whether the package path layout `/api/<format>/<repoKey>/…` and the
port pair 8080/8081 should change, so that trove does not look like existing repository managers.
The owner's white-label rule forbids naming other products; it does not forbid functional
resemblance. The question is whether trove keeps INV-6 or adopts a distinct layout and ports.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep INV-6 | `/api/<format>/<repoKey>/…` for protocols, `/api/v1/` for the management API, client port 8080, operations port 8081 | Every spec page already agrees. The format segment allows per-format routing and edge rules without reserving top-level names. A layout with a format segment migrates by prefix rewrite. | Protocols and the management API share the `/api/` prefix, so format IDs must never look like `v<n>` (enforced at registration). The layout resembles some existing tools. |
| (b) Format at the top level | `/<format>/<repoKey>/…` (`/npm/npm-all/`); `/api/` holds only the management API | `/api/` means one contract. Slightly shorter client URLs. | Format IDs share the top-level namespace with `api`, `auth`, `ui`, `files`, `v2`, `-` and `.well-known`, so every future host-root route competes with format IDs. Resembles other existing tools. About 190 references in 15 documents change. |
| (c) Format-free repository prefix | `/r/<repoKey>/…`; the format comes from the repository (INV-5 keys are unique and single-format) | Shortest URLs. No format mismatch case. A regex maps any other layout onto it. | The URL no longer shows the format, so edge rules per format are impossible. `/r/` and `/files/` overlap for generic repositories. Same churn as (b). |
| (d) Keep the paths, move the operations port | For example 9090 instead of 8081 | Removes the port resemblance | Every port number is someone's default (9090 is the Prometheus server's). 8080/8081 for application and admin listener is a generic convention. Changes 13 references in [ha-and-helm.md](ha-and-helm.md) for no functional gain. |

**Choice:** (a) — INV-6 stays as it is: protocols under `/api/<format>/<repoKey>/`, the management API under `/api/v1/`, client port 8080 and operations port 8081.

**Rationale:**
- **Client configuration.** Every MVP client (npm, pnpm, Yarn, dotnet, nuget.exe) takes a full
  registry or service-index URL with a path, and the setup snippets are generated (R-API-033). Path
  length and shape do not change what a user types.
- **Collisions with `/api/v1`.** In (a), formats are registered as literal segments, `^v[0-9]+$` is
  reserved, and ServeMux panics on a conflicting registration, so a collision cannot reach a
  release. (b) only moves the same problem to the root. There, the host-root routes that clients
  fix (npm's `/-/npm/v1/…`, Q-NPM-1; OCI's `/v2/`, Q-HA-4) and future well-known paths compete
  with format IDs.
- **Migration.** Every layout needs one edge rewrite from any other manager's layout. A source with
  a format segment maps onto (a) with a plain prefix strip. A format-free source maps onto (a) only
  with per-repository rules, and onto (c) with one regex. None of this decides it.
- **Resemblance.** It is functional, not a reference, and every candidate layout matches some
  existing tool. Distinctness cannot be a design goal here; only clarity can.
- **Ports.** 8080/8081 is the common application/admin pair. Neither collides with common sidecars
  (Istio uses 15000 to 15090,
  [ports used by Istio](https://istio.io/latest/docs/ops/deployment/application-requirements/#ports-used-by-istio)).
  The operations listener is internal anyway: it is reached only through the `-ops` Service and the
  NetworkPolicy.
- **Cost.** This argument is secondary. (b) and (c) would rewrite about 190 references in 15
  documents (counted on 2026-10-28) for a gain in clarity that (a) already gets by documenting
  `/api/v1/` as the only management API.
- Revisit before the first external client is configured against a running trove, and only for
  this reason: the owner finds the resemblance unacceptable for the white-label product at the
  walkthrough. Then (b) is the fallback. After clients exist, any change breaks every `.npmrc` and
  `nuget.config`.

**Applied in the spec:**
- No change to INV-6 from this decision. Its host-root rows come from Q-NPM-1 and Q-HA-4.
- R-ARCH-005 refers to this decision at the listeners and states that a conflicting registration
  cannot ship.
- R-ARCH-005's route table lists the host-root routes that
  [formats/npm.md](formats/npm.md) Q-NPM-1 and [ha-and-helm.md](ha-and-helm.md) Q-HA-4 decided. It
  states when a host-root route may exist at all: only where a client fixes the path. Such a route
  never carries a repository key and never changes the `/api/<format>/<repoKey>/` layout.

### Q-ARCH-12 · Repository-wide indexes: rendered per request or built by jobs, and does a publish wait?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The catalog computes format indexes asynchronously after a deploy, with delays from seconds to a
minute, and leaves open whether any format holds the publish response until its index is updated
(FMT-011). npm and NuGet are settled: their per-package metadata is rendered per request
(R-ARCH-012). Later formats have repository-wide indexes, such as Helm `index.yaml`, Debian
`Packages` and Conda `repodata.json`. This question sets trove's rule for them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Always per request | Every index of every format is rendered from rows at request time and cached by revision | Read-your-writes everywhere. No index jobs. | Signed indexes (Debian `InRelease`, RPM `repomd.xml.asc`) and several compressed variants of a large `Packages` file would be re-signed and recompressed after every change, on the request path |
| (b) Repository-wide indexes always by job | Per-package documents per request; repository-wide indexes rebuilt by a coalesced job; publish returns at once; each format page states its delay | Cheap requests. The pattern the catalog describes. | A publish-then-install CI step can miss the new version for the length of the delay, even where rendering per request would have been cheap |
| (c) Jobs, and the publish waits | Indexes by job, but the publish response is held until the rebuild commits (with a timeout) | Read-your-writes for job-built indexes | Publish latency and success are bound to worker capacity and queue health. A stuck queue makes publishes time out. Clients retry and duplicate work. |
| (d) Per request by default, jobs by exception, publish never waits | Every derived index is rendered per request and cached under a revision. A format may build an index by job only when its page shows that per-request rendering misses P-4 at its reference size, or that the index must be signed or stored in several compressed variants. Such a job is coalesced, the old index is served until the new one commits, and the delay bound is stated and tested. | Read-your-writes wherever it is affordable, and bounded staleness only where it is not. Publish latency never depends on the job queue. | Two mechanisms, and each job-built format must justify its exception |

**Choice:** (d) — derived indexes are rendered per request by default, built by a job only by justified exception, and no publish ever waits for an index.

**Rationale:**
- Rendering from rows is what makes R-ARCH-012's guarantee hold in an active-active cluster without
  coordination. It should be the norm, not an npm and NuGet special case.
- A per-repository revision is incremented only for formats that declare a repository-wide index.
  It keeps the rendition cache exact without making npm and NuGet publishes contend on a
  repository row.
- Signing and multi-variant compression are where per-request rendering really costs too much, so
  the exception is limited to that, with a measured delay instead of an open-ended one.
- (c) couples the client-facing write path to background capacity, which is exactly the failure
  coupling the HA design avoids.
- Revisit for a format whose job-built index cannot hold its stated delay bound under the R-ARCH-027
  reference load.

**Applied in the spec:**
- R-ARCH-012 now:
  - lists every change that increments `rev_seq` (publish or upload, delete, restore, copy, move,
    import, retention, cache fill), which settles the catalog's trigger list;
  - forbids client-uploaded derived metadata;
  - replaces "MAY be rebuilt by a job" with the three rules of option (d).
- R-ARCH-008's `Descriptor` gains `RepositoryIndex`, which switches the per-repository revision on.
- R-ARCH-016 separates the automatic rebuild of the exception (`coalesce`) from a manual reindex
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-016, `exclusive`, `409` while one
  runs). Both use the key `index:<repository id>`, so the two never run at the same time.
- Settled alongside, under FMT-011: R-ARCH-015 fixes one virtual metadata period for every format
  (`mergedMetadataTtlSeconds`, R-REPO-020), which settles the catalog's competing defaults.

### Q-ARCH-13 · Valkey client library and supported Valkey versions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-3 and INV-12 make Valkey with Sentinel part of every production installation. The client must
follow Sentinel failovers, speak TLS and ACL to the data nodes and to the Sentinels, and fit
R-ARCH-001's static binary without cgo. The supported server versions decide whether the flight
lock needs a script.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) valkey-go v1.0.79, Valkey 9.0 and later | The Valkey project's Go client (Apache-2.0), against servers of the 9.0 line and later | Its Sentinel client checks `ROLE`, follows `+switch-master` and reconciles periodically. TLS and an ACL user for data nodes and for Sentinels. Credentials are read on every connection attempt. Builders for `SET … IFEQ` and `DELIFEQ` (all verified in its source). From 9.0, `DELIFEQ` exists, so the lock needs no script and the ACL user needs no scripting. | Frequent patch releases (v1.0.79). Client-side caching is on by default and has to be switched off. An installation on Valkey 8.x has to upgrade. |
| (b) valkey-go, Valkey 8.1 and later | The same client, one more server line | Covers 8.1 installations (maintained until 2028-03-31) | 8.1 has `SET … IFEQ` but no `DELIFEQ`. The release then needs a script, so the ACL user must be allowed scripting, and two code paths exist until 8.1 is dropped. |
| (c) go-redis v9.23.0 | Redis Inc.'s client (BSD-2-Clause) with `FailoverClient` for Sentinel | Large user base; Sentinel with an ACL user | Its supported servers are Redis CE 8.0 to 8.10, and other servers are "not officially supported". Its compare-and-delete helper targets Redis's `DELEX`, not Valkey's `DELIFEQ`. Divergence between the servers would surface in trove's tests, not upstream's. |
| (d) Valkey GLIDE for Go v2.5.3 | The Valkey project's multi-language client on a Rust core | Official Valkey client with one core across languages | Needs cgo, which R-ARCH-001's `CGO_ENABLED=0` static binary rules out. Its Go documentation names no Sentinel mode. |

**Choice:** (a) — valkey-go, against Valkey 9.0 and every later minor line, in Sentinel mode for production.

**Rationale:**
- (d) is ruled out by the static binary. Between (a) and (c), (a) is maintained by the Valkey
  project and tested against Valkey, while (c) is tested against Redis CE, whose commands are
  diverging from Valkey's (`DELEX` against `DELIFEQ`).
- (a)'s Sentinel client does what the Valkey client guide asks for: resolve through the Sentinels,
  verify with `ROLE`, resolve again on every reconnection. Its periodic reconciliation covers a
  missed `+switch-master`, the failure mode its own documentation names.
- Reading credentials on every connection attempt lets a rotated Secret take effect without a
  restart (R-ARCH-031).
- Floor 9.0 rather than 8.1: the flight lock is the only place that needs compare-and-delete. With
  `DELIFEQ` and `SET … IFEQ`, trove runs no scripts, so its ACL user can be denied scripting, which
  narrows what a stolen trove credential can do inside Valkey. 9.0 is maintained until 2028-10-21
  and 9.1 until 2029-05-19, which covers the prototype and the first releases. Valkey enters trove
  with this change, so no installation is already bound to an older line.
- Revisit if a target installation must run Valkey 8.1 (then (b), with a script for the release
  only), if valkey-go stops following Valkey releases, or if GLIDE ships a Go client without cgo
  and with Sentinel support.

**Applied in the spec:**
- New R-ARCH-031 implements [ha-and-helm.md](ha-and-helm.md) R-HA-056 and R-HA-057 with this
  library: the server floor and its check at connect, the Sentinel protocol plus periodic
  reconciliation, primary-only commands, the connection count, the breaker path, TLS, credentials
  read per connection attempt, the least-privilege command list with an example ACL rule, and
  acceptance criteria.
- R-ARCH-002: only `cluster` imports the Valkey client, with an archtest criterion.
- The verified-facts table lists the client, server and Sentinel facts this choice rests on.
- Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-056 (version floor 9.0 instead of 8.0, CI on
  9.0 and 9.1, the new key `valkey.sentinel.refresh_interval`, the local profile on Valkey 9.1).

### Q-ARCH-14 · PostgreSQL connection model for n replicas and transaction-mode poolers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-12 says the load exceeds one replica, and PostgreSQL connections must not grow without bound
with the replica count. Until 2026-10-28 every replica opened the main pool (20), the lease pool (4),
the fetch lock pool (16) and one `LISTEN` session, all as direct or session-mode connections, and
transaction-mode poolers were excluded ([ha-and-helm.md](ha-and-helm.md) Q-HA-6). The example below
uses 20 `web` and 3 `worker` replicas plus one surge pod.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Direct or session-mode only | The earlier model: every replica opens main, lease, lock pool and listener | Nothing to change; session features everywhere | 24 × 41 = 984 server connections in the example, growing by 41 per replica. Violates INV-12 at the scale the owner expects. |
| (b) Split by class and role | The `main` class keeps no session state and may run through a transaction-mode pooler. Session connections exist only on `worker` replicas (the lease pool) and in `trove migrate` and `trove backup`. No listener (Q-ARCH-15), no lock pool (Q-ARCH-6). | With a pooler, the server sees the pooler's pool (for example 40) plus 4 × 4 lease connections plus the one-off ones, about 60 in the example, whatever the `web` count. Without a pooler, still 24 × 20 + 4 × 4 = 496 instead of 984. Fencing of singleton work is unchanged. | Two endpoints to configure where a pooler is used. Every request-path query must stay transaction-scoped, which a test enforces. |
| (c) Everything through a transaction-mode pooler | (b), and leases become table rows with fencing tokens, so no session connection remains | Zero session connections | Gives up R-HA-006's fencing on the lease's own session and the immediate release on session end. `worker` replicas are few, so it saves a dozen connections. |
| (d) (b) plus one `LISTEN` session per replica | Pooled main pool, but invalidation stays on `LISTEN/NOTIFY` | Delivery on commit as before | Still one direct session per replica, growing with the count; every `web` replica needs a direct endpoint, and readiness depends on that session |

**Choice:** (b) — the `main` class is transaction-scoped and may run through a transaction-mode pooler from the MVP on; session connections exist only for the lease pool on `worker` replicas and for `trove migrate` and `trove backup`.

**Rationale:**
- Only (b) and (c) keep server connections independent of the number of `web` replicas, which is
  the number that grows with load. (c) pays for a dozen saved connections with weaker fencing, the
  property that keeps a partitioned worker from committing the scheduler's slots and the writes of
  the singleton work that remains under a lease in [ha-and-helm.md](ha-and-helm.md) R-HA-006, such
  as background migrations. Garbage collection holds no lease ([storage.md](storage.md) Q-STOR-13).
- The main pool's work is already transaction-scoped: job claims, `InTx` with `SET LOCAL`, and
  `pg_advisory_xact_lock(4, 1)`. What needed a session was the listener, the lock pool and the
  leases. Q-ARCH-15 and Q-ARCH-6 remove the first two, and the leases move to the role that runs
  singleton work.
- PgBouncer's feature table confirms the boundary: in transaction pooling, session `SET`, `LISTEN`,
  SQL `PREPARE`, `WITH HOLD` cursors and session-level advisory locks never work, while
  protocol-level prepared statements work with `max_prepared_statements` (default 200). pgx's
  default exec mode uses exactly those, and `default_query_exec_mode=exec` covers poolers without
  them.
- Direct connections stay supported. The chart's budget check (R-HA-016) bounds them, and an
  installation whose `web` autoscaling would exceed the budget adds a pooler instead of raising
  `max_connections`.
- Moving the change-log poll from a dedicated session onto the main class must not tie readiness to
  request load. One reserved control connection inside `database.pool.max_conns` keeps the poll
  and the heartbeats independent of a saturated request pool without adding a connection
  (R-ARCH-019).
- Revisit if a managed PostgreSQL's pooler supports neither prepared statements nor `exec` mode
  without measurable latency, or if `worker` counts grow so large that their lease sessions matter;
  (c) is then the next step.

**Applied in the spec:**
- R-ARCH-019: the connection classes, the main-pool rules, the exec-mode setting, the connection
  count with and without a pooler, and acceptance criteria with PgBouncer in transaction mode and
  a source test against session state.
- R-ARCH-017: the lease pool exists only on `worker` replicas; the session check runs in the
  processes that hold sessions; a `web` replica starts behind a transaction-mode pooler.
- R-ARCH-001: the role table shows which role opens which class.
- R-ARCH-027: the reference environment has PgBouncer in transaction mode.
- R-ARCH-019, after review on 2026-10-28: one control connection per replica is reserved inside
  `database.pool.max_conns` for the change-log poll and the heartbeats, so a request pool saturated
  under overload cannot fail readiness on one replica after another, and the connection budget is
  unchanged. No connection is held across a wait on a client, the object store, an upstream or
  Valkey. Statement-mode poolers are excluded. The backup's snapshot transactions are the one
  exception to `READ COMMITTED`, and session-scoped statements are confined to the code of the
  session classes.
- After cross-page review on 2026-10-28: behind a transaction-mode pooler, the control connection
  uses a control endpoint with a server pool of its own and also carries the `database` check
  (R-ARCH-019, [ha-and-helm.md](ha-and-helm.md) Q-HA-20); the grant-index build of
  [authorization.md](authorization.md) R-AUTHZ-028 is a second `REPEATABLE READ READ ONLY`
  exception on the request pool; the first rationale bullet no longer names garbage collection as
  lease-fenced work ([storage.md](storage.md) Q-STOR-13).
- Cross-page: [ha-and-helm.md](ha-and-helm.md) Q-HA-6 (option (b) becomes supported in the MVP,
  and the P2 Deferred row goes), R-HA-006 item 5, R-HA-016 (formula without the lock pool and the
  listener, per role, with a pooler form), R-HA-004 and R-HA-017 step 3 (no listener);
  [operations-and-observability.md](operations-and-observability.md) R-OPS-001 and R-OPS-026;
  ADR-077 in [docs/decisions.md](../decisions.md).

### Q-ARCH-15 · Cross-replica invalidation without LISTEN/NOTIFY

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Caches on n replicas must learn of changes within the bounds that pages promise, for example a
revoked token rejected within 2 s (R-AUTH-023). INV-3 says that losing Valkey's data must not cost
correctness. INV-12 says that a Valkey outage must not make replicas unready. Q-ARCH-14 needs `web`
replicas without session connections.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep `LISTEN/NOTIFY` for administrative changes | One direct `LISTEN` session per replica, `NOTIFY` in the writing transaction | Delivery on commit, already specified | One direct session per replica, which a transaction-mode pooler cannot carry and which grows with the count. A lost session makes the replica unready (`notify` check). |
| (b) Valkey Pub/Sub only | Changes are published after commit; nothing in PostgreSQL | Fast; no PostgreSQL work | Delivery is at most once. A message lost in an outage or a failover leaves a revoked token valid until `SafetyTTL`. Correctness would depend on Valkey, against INV-3. |
| (c) Change log in PostgreSQL, polled, with Valkey as the fast path | Each change area has an epoch row; the writing transaction increments it and inserts a change row. Every replica polls the log on a reserved main-class connection. Valkey carries the same change at once, as an early eviction that never advances the applied epoch. | Correct from PostgreSQL alone, within the poll interval; fast with Valkey. No session connection. The area's row lock orders the commits, so epochs have no gaps. Lost, duplicated or forged messages cannot hide a change. | One small query per replica per poll interval (100 replicas at 1 s: 100 queries per second). Writers of one area serialize on its row, for their commits only, because the row is written last. Each change is applied twice, once from the message and once from the poll. |
| (d) Outbox with one global sequence, polled | Change rows numbered from one sequence, polled by "greater than the last seen" | One query for all areas | Concurrent transactions commit out of sequence order, so the poll can skip a row that commits late. That needs gap tracking or a delay window. |

**Choice:** (c) — a per-area epoch and change log in PostgreSQL, polled by every replica, with Valkey Pub/Sub as the fast path that can only evict early.

**Rationale:**
- (c) keeps correctness where INV-3 puts it and speed where INV-12 allows it. With Valkey, a change
  takes effect within milliseconds. Without Valkey, it takes effect within the poll interval, which
  also bounds revocations that R-AUTH-023 requires within 2 s.
- Ordering comes for free. The `UPDATE` of the area's epoch row holds that row until commit, so the
  next writer of the same area gets its epoch only after the previous one committed. Polling by
  "epoch greater than the last applied" therefore cannot miss a committed change. (d) needs extra
  machinery for the same guarantee.
- Because only the poll advances the applied epoch, a forged or suppressed Valkey message can never
  hide a revocation; it can at most cause an extra eviction. That keeps Valkey outside the trust
  path (Q-ARCH-16).
- The cost is small: change areas carry administrative changes only (configuration, policies,
  revocations), and the poll reads a table with one row per area. [authorization.md](authorization.md)
  R-AUTHZ-028 uses the `authz` area itself and has no epoch row or poll of its own
  ([authorization.md](authorization.md) Q-AUTHZ-11).
- Readiness keeps a guard that depends on PostgreSQL alone: the `invalidation` check fails when the
  poll has stalled.
- Revisit if the poll load matters (beyond about 200 replicas), with a longer interval for areas
  without a tight bound, or if one area's write rate makes its row contend, for example CI token
  revocations at the end of every job at thousands per second; that area then splits into shards.

**Applied in the spec:**
- R-ARCH-018 is rewritten: change and signal areas, the `cluster_epoch` and `cluster_change`
  tables, the poll, the rule that only the poll advances the applied epoch, the `invalidation`
  readiness check, the area table and acceptance criteria with Valkey stopped and with forged
  messages.
- R-ARCH-016: the watched loops `cluster.changes` and `cluster.signals`, and the job kind
  `cluster-change-prune`.
- R-ARCH-017: there is no per-replica coordination session, and a lost lease connection flushes no
  cache.
- R-ARCH-024: start-up runs the first poll; R-ARCH-027: P-13 with and without Valkey; the security
  notes.
- R-ARCH-018, after review on 2026-10-28: `cluster.Notify` writes the epoch row as the last
  statements before `COMMIT`, so writers of an area serialize on their commits only; change areas
  are never written per package request; a cache fill checks the applied epochs around its query, so
  it cannot re-insert a value the poll has just invalidated (a revoked token, for example); the poll
  runs on the control connection of R-ARCH-019; `cluster_change` is `transient` for backups and
  `cluster_epoch.epoch` is a registered revision column.
- R-ARCH-018, after cross-page review on 2026-10-28: the poll reads `changed_at` with each epoch
  and checks the row at the last applied epoch, so an epoch that went backwards or was reused
  after a failover or a point-in-time restore flushes the area, for every change area
  ([ha-and-helm.md](ha-and-helm.md) R-HA-004, [authorization.md](authorization.md) R-AUTHZ-028);
  `cluster-change-prune` keeps a row until its successor is older than the retention;
  `cluster.epoch_max_staleness` defaults to 20 s, at least the readiness window of R-HA-008, and
  fail-closed paths read `cluster.ChangesFresh()` or `cluster.ChangesFreshWithin(d)`; writers of
  several areas update them in area-name order.
- Cross-page: [operations-and-observability.md](operations-and-observability.md) R-OPS-001
  (`notify` becomes `invalidation`, and the watched loops); [ha-and-helm.md](ha-and-helm.md)
  R-HA-004 (rewritten onto the change log) and R-HA-001;
  [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023;
  [authorization.md](authorization.md) R-AUTHZ-028; [repositories-and-proxy.md](repositories-and-proxy.md)
  R-REPO-045; [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-003 and R-CI-009.

### Q-ARCH-16 · What Valkey may hold

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-3 lists shared caches among Valkey's uses. The question is which caches, given that whoever can
write to Valkey can change what a shared cache returns.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Coordination only | Locks, counters and signals in Valkey; every cache in process or in PostgreSQL | Smallest trust surface | The negative cache stays an `UNLOGGED` PostgreSQL table: an insert per upstream 404 and a cleanup job on the system of record, for state its own page calls disposable |
| (b) Coordination plus caches that serve nothing | (a) plus the negative cache. Token, session, permission, rendered and merged documents stay in process; Valkey carries only their change messages. | Disposable write load leaves PostgreSQL. Valkey's content can cost availability, never access or integrity. | A remote miss gains one Valkey round trip. Forged negative entries can hide packages for their checked lifetime (H-49). |
| (c) A shared second level for every cache | Also token validation results, permission decisions, rendered package documents and merged metadata in Valkey | Fewer database reads and less rendering per replica | Anyone who can write to Valkey could grant access or serve altered package metadata, for example a tarball URL with a matching integrity hash. Rendered documents of several MiB per entry load Valkey's network. A token check miss costs one indexed query, so the saving is small. |

**Choice:** (b) — Valkey holds coordination state and the negative cache; nothing served to a client and no positive authentication or authorization result is ever read from Valkey.

**Rationale:**
- The line follows the trust boundary. Under (b), a compromised Valkey can make trove slower or
  make packages temporarily unavailable, but it cannot grant access or change what a client
  installs. Under (c), Valkey would become part of the supply chain.
- The negative cache is the one cache whose entries are small, written on the hot path and
  disposable by their owner's own words (R-REPO-028). Moving it takes those writes off PostgreSQL,
  and its readers validate creation time and invalidations, so even a forged entry is bounded.
- The in-process caches lose little. Their entries are self-validating (`rev_seq`, epochs) or cheap
  to refill, and their invalidation stays exact through the change log (Q-ARCH-15).
- Revisit if profiling at the reference load shows rendering (P-4, P-5) as the limit on scale-out.
  A shared rendition cache would then need integrity protection, an HMAC under a key Valkey never
  sees, before it could be considered.

**Applied in the spec:**
- R-ARCH-030: the two placement rules, the usage table and the reader-validation rule.
- R-ARCH-018: caches live in process. R-ARCH-013: the negative entry is looked up in the L1, then
  in Valkey.
- The security notes and H-49.
- Cross-page: [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-028 (the negative cache
  moves to Valkey and its table and cleanup job go) and R-REPO-045 (the fold job no longer deletes
  negative entries; readers apply the invalidation time);
  [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-023 (the token cache
  stays in process by rule).

### Q-ARCH-17 · Rate-limit counters across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-API-016, R-AUTH-038 and R-CI-021 keep their buckets in each replica's memory
([api-and-ui.md](api-and-ui.md) Q-API-3), so the cluster-wide budget is the configured value times
the replica count (H-43). Under the new constraints the replica count grows with the load and
changes with autoscaling, and requests reach replicas at random. INV-12 names per-replica limits as
the degraded mode while Valkey is unavailable, which makes cluster-wide limits the normal mode.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica, in memory | The earlier model | No network round trip; the budget grows with capacity | The effective limit is the configured value times a replica count that autoscaling changes. A throttled address is unthrottled on the next replica, so the failed-credential throttle weakens with every replica, and the answer depends on the replica a request reaches (INV-18). |
| (b) Cluster scope in Valkey, per-replica fallback | A sliding-window counter per bucket in Valkey; while Valkey is unavailable, local buckets at the configured value divided by the live replicas of the role | One budget whatever the replica count; the same answer on every replica; the fallback stays close to the budget | One Valkey round trip on each limited request; the sliding window is an approximation |
| (c) Cluster scope in PostgreSQL | Counter rows | Durable | A write per request on the system of record |
| (d) Per replica at the configured value divided by the replica count | No shared state | No round trip | kube-proxy balances connections, not requests: a client with keep-alive stays on one replica and gets 1/n of its budget. Small bursts round down to nothing. |

**Choice:** (b) — the limiter of R-ARCH-034 counts cluster-wide in Valkey and falls back to per-replica shares; the owning pages choose the scope per class, and every class that limits callers uses cluster scope.

**Rationale:**
- A limit that changes with the replica count is not a limit an operator can set. With the load
  spread over many replicas by design, (a) multiplies every budget, and it hands a credential
  guesser one fresh throttle per replica.
- The fallback divides by the live replicas of the role, so a Valkey outage keeps the cluster-wide
  budget roughly where it was instead of multiplying it. That is the "per replica" INV-12 names.
- The round trip is pipelined, bounded by `valkey.command_timeout`, and skipped while the breaker is
  open (R-ARCH-031). It is budgeted in R-ARCH-027.
- Limits that protect the replica itself, such as `server.max_in_flight`, stay per replica.
- The break-glass lockout stays in PostgreSQL: it guards a credential and must survive a loss of
  Valkey's data.
- Revisit if the limiter's round trip shows up in P-2. Read classes could then check a local bucket
  first and consult Valkey only near the limit.

**Applied in the spec:**
- New R-ARCH-034: the limiter, its scopes, the sliding-window algorithm without scripts, the
  fallback, and acceptance criteria with and without Valkey.
- R-ARCH-005 step 7, R-ARCH-030's usage table, R-ARCH-032's state table and the Deferred row of
  OPS-064.
- Cross-page: [api-and-ui.md](api-and-ui.md) Q-API-3, R-API-016 and H-43 (per-replica budgets only
  while Valkey is unavailable); [identity-and-authentication.md](identity-and-authentication.md)
  R-AUTH-038 (the failed-credential throttle and the start limits count cluster-wide);
  [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-021.

### Q-ARCH-18 · Transfers that outlast the termination grace period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A node drain or a rollout sends `SIGTERM`. The grace period of [ha-and-helm.md](ha-and-helm.md)
R-HA-011 (120 s by default for `web` pods) bounds how long a replica may finish its transfers. A
slow client or a large file can need longer. The owner requires a drain without outage when the
setup is sized for it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Cut at the deadline; clients resume or retry | The grace period is sized to the p99 transfer. Every blob response carries `Content-Length`, a strong `ETag` and `Accept-Ranges`, so a cut is detectable and resumable on any replica. | Drains stay bounded. No new component. npm and NuGet retry by default, and resuming works on any replica because the ETag is the content address. | A client that neither retries nor resumes fails a transfer longer than the grace period |
| (b) A grace period as long as the longest transfer | For example one hour | No transfer is cut | Each drained pod blocks for up to the grace period. With a disruption budget of one pod at a time, a node-pool upgrade takes hours, and one stuck connection holds a node. |
| (c) Hand large downloads to the object store | Presigned redirects ([storage.md](storage.md) R-STOR-014, P2) | Transfers bypass the replicas, so a drain never cuts them | Clients and networks must reach the store directly, which private stores often prevent. A presigned URL is a bearer capability until it expires, and authorization at use and the content headers move to the store. P2 today. |
| (d) Move live connections to another replica | — | — | Not possible for TCP without a connection-migrating proxy in front, which the platform does not offer |

**Choice:** (a) — a transfer still running at the shutdown deadline is cut, and clients resume with `Range` and `If-Range` or retry; (c) remains the later path for formats with very large artifacts.

**Rationale:**
- The MVP formats move small files. At 1 MiB/s, 120 s cover 120 MiB, far above a typical npm
  tarball or `.nupkg`. npm retries twice and NuGet six times by default.
- Statelessness makes resuming work: any replica serves the `206`, because the ETag is the content
  address and the item lookup is in PostgreSQL (INV-18).
- Uploads are atomic (R-STOR-004), so a cut upload costs one retry, never a corrupt or partial
  package. An upload whose body has fully arrived is not cut before its status line, because a
  commit whose answer is lost turns the client's retry into a version conflict.
- (b) trades a bounded drain for an unbounded one. (c) has its own security trade-offs and is P2.
- Revisit when container-image or generic large-file repositories (P1) show transfers cut at
  shutdown in `trove_http_transfers_cut_total`; (c) for those formats, or a longer grace period for
  a dedicated `web` Deployment that serves them, is then the next step.

**Applied in the spec:**
- R-ARCH-024: shutdown step 5 cuts the remaining transfers at the deadline, states how downloads
  resume and uploads retry, and counts the cuts; an acceptance criterion resumes a cut download on
  another replica.
- R-ARCH-024, after review on 2026-10-28: an upload whose body has arrived completely is never cut
  between its commit and its status line. It runs on for at most 3 s of the grace period's 5 s
  margin, so a retried publish never meets its own committed version as a conflict; an acceptance
  criterion publishes across the deadline.
- Cross-page: [storage.md](storage.md) R-STOR-012 (`Content-Length` on every blob response and
  `If-Range` support); [ha-and-helm.md](ha-and-helm.md) R-HA-011 (size the grace period to the p99
  transfer, and name this decision).

### Q-ARCH-19 · Package-level state the state rule allows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No exemption | Every package-level pointer outside the allowed trees is reported | Strictest reading of R-ARCH-032 | Compiled regular expressions must move into functions or into wrappers, for no gain in safety |
| (b) Exact exemption of `*regexp.Regexp` (chosen) | A compiled regular expression is exempt by package path and type name; every other pointer is still reported | An immutable program is not state a request can leave behind. The exemption names one type, so nothing else slips through. | One more named exception to keep in the archtest list |
| (c) Exemption of every immutable type | Any type the author declares immutable is exempt | Covers future immutable values | An author decides what "immutable" means, which is the judgement the rule exists to avoid |

**Choice:** (b) — `*regexp.Regexp` is exempt, matched exactly by package path and type name.

**Rationale:** The rule protects against state that a request leaves in a replica's memory. A regular
expression compiled at start-up is shared and never written after compilation, so it cannot carry
state between requests. Generated files, variables with a `//go:embed` directive and variables of
interface type are skipped for the same reason: they are not hand-written state of these kinds. The
development agent would revisit this if a regular expression were compiled from request data, or if
the rule had to cover lazily filled package variables, which would need their own exemption with a
reason.

**Applied in the spec:** R-ARCH-032, acceptance criterion on package-level state: the frozen registries
are named exactly (`formats`, `jobs`, `errs`, `secrets`, `meta`, `backup/tables`), and the exemptions
of this block are stated.

### Q-ARCH-20 · Web build output in git and the placeholder

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Commit the built locale directories | `internal/webui/dist/<locale>/` is in git, so a clone builds a working UI without Node | No build step before `go build` | Large generated diffs on every UI change; a stale build can be committed |
| (b) No placeholder; the UI is built only in CI and releases | `go build` depends on a UI build | Nothing generated in git | `go build` needs Node.js, against R-ARCH-003's acceptance criterion |
| (c) Committed placeholder `index.html`; built locale directories ignored | `internal/webui/dist/index.html` is committed; `dist/<locale>/` is ignored by `internal/webui/.gitignore`. With no locale directory the placeholder serves every locale. With some but not all configured locales built, `webui.New` fails. (chosen) | `go build` works without Node.js; a release build runs the web build first; a half-built UI cannot start | A binary built without the web build shows a placeholder page, which the release procedure must never ship |

**Choice:** (c) — the placeholder is committed, the locale directories are not, and a partially built
UI fails at start-up.

**Rationale:** R-ARCH-003 requires `go build ./cmd/trove` on a clean checkout without Node.js, and
the release build must still embed the real UI. Ignoring the locale directories keeps the diffs
generated by the build out of git. Failing when some locales are missing is the fail-fast choice:
a UI that serves one language and 404s another is worse than a start-up error. The development agent
would revisit this when the release pipeline builds the UI in CI and no release can be produced
without it.

**Applied in the spec:** R-ARCH-003 states that built locale directories are not committed and
`internal/webui/.gitignore` ignores them; the placeholder rule is unchanged.

### Q-ARCH-21 · Setup failures of the outbound factory and the object-store exception

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `outbound.New` returns an error on a setup problem | A failed trust store stops start-up | Problems surface at start-up | Local repositories and the probes stop because of one remote setting |
| (b) `outbound.New` returns a `*Factory` whose `Err()` reports the setup problem; every request through the factory fails (chosen) | A failed trust store (`proxy.outbound.tls.ca_files`, or `use_system_roots: false` without files) is kept in the factory. Clients built by it fail every request with that error. | Replica stays up for the parts that need no outbound client; the failure is visible on every use | A misconfigured replica answers remote requests with errors until the configuration is fixed |
| (c) Silent fallback to the system roots | A failed trust store is ignored | Nothing breaks | Certificates are verified against a different trust store than configured |

For the egress policy of R-REPO-038 (the address policy of [repositories-and-proxy.md](repositories-and-proxy.md)):

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every purpose | The policy applies to the object-store client too | One rule for every outbound connection | The object-store endpoint is operator-configured and is often a private address (MinIO, SeaweedFS, Ceph RGW in the cluster), so the default policy refuses it |
| (b) Every purpose except `objectstore` (chosen) | The `objectstore` purpose sets the egress check off; its transfers keep their own stall detection | The endpoint is configuration the operator writes, not input from a client | The gap of [H-56](#h-56--the-object-store-endpoint-skips-the-egress-address-policy) |
| (c) Per-purpose allowlist in configuration | A setting lists the purposes that may reach private addresses | Explicit per deployment | One more setting to get wrong |

**Choice:** (b) for the policy, and (b) for setup failures — the factory keeps its setup error, and every request through it fails with that error.

**Rationale:** The endpoint of the object store is written by the operator in the configuration, so
its address is not untrusted input in the way an upstream's redirect is. Refusing it by default would
break every on-premises installation. The gap stays recorded as H-56 rather than hidden. For setup
failures, a replica that cannot verify upstream certificates must not contact upstreams, and it still
serves what needs no upstream. The development agent would revisit the object-store exception when
a deployment shows that the endpoint can come from a request, or when the configuration of endpoints
moves into the database.

**Applied in the spec:** H-56 records the object-store exception as an open gap. R-ARCH-022 is
unchanged.

### Q-ARCH-22 · Sealed-column registry and the envelope format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Column names passed to `Seal` as strings | Any table and column may be sealed by name | No registration step | A column can be sealed under a binding that no reader expects, and nothing checks it |
| (b) Registry values only (chosen) | `secrets.Register(table, primaryKey, column)` returns a `Column`; only the registry creates one. `Seal` on any other column fails with `ErrUnregistered`. | The binding (table, primary key, column) is fixed by the code that owns it | One call per sealed column at start-up |
| (c) Registry in the database | The list of sealed columns is a table | Editable at run time | The list would decide what is encrypted, so a row change could leave secrets in plaintext |

Envelope: `v1:<key id>:<base64(nonce ‖ ciphertext ‖ tag)>`, AES-256-GCM, additional authenticated data
`trove/sealed/v1` followed by the table, the primary key and the column name (chosen).

**Choice:** (b) for the registry; the envelope as stated above.

**Rationale:** The additional authenticated data binds a sealed value to its row and column, so a
value copied to another row or column fails authentication instead of decrypting to a foreign secret.
A version segment at the front of the envelope lets a later format be read beside this one. The
development agent would revisit the envelope when a second cipher or a key service (KMS) is needed.

**Applied in the spec:** R-ARCH-021 keeps the mechanism; the envelope and the registry rule are the
choice of this block.

### Q-ARCH-23 · Query execution mode behind a transaction-mode pooler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) pgx default `cache_statement` everywhere | Prepared statements are cached per connection | Fastest for repeated queries on direct connections | A transaction-mode pooler hands the next transaction a connection whose named statements are gone |
| (b) `QueryExecModeExec` for the pooled main class when `database.pooler.mode` is `transaction`; the default elsewhere (chosen) | Parameters are sent through the extended protocol on the unnamed statement | Works through any transaction-mode pooler, with or without `max_prepared_statements` | Slower than cached statements for hot queries, measured at M5 |
| (c) A configuration key that selects the mode | The operator chooses per deployment | Explicit | An operator choice that breaks the pooler case silently |

Refusal rule (chosen with (b)): a session-scoped connection that lands on a pooler refusing session
parameters (SQLSTATE `08P01`) fails with `ErrSessionState` and is never retried.

**Choice:** (b), with the refusal rule.

**Rationale:** Exec mode costs little on the main class and removes a whole failure class without a
setting to get wrong. The session classes cannot run behind a transaction pooler at all, so the
refusal is the only correct answer for them; retrying it would only repeat the same refusal.

**Applied in the spec:** R-ARCH-019 already states the exec mode of the transaction-mode pooler; no
requirement text changed. The session-state refusal is not yet in R-ARCH-019 and is recorded here only.

### Q-ARCH-24 · Opening the PostgreSQL pool, and a connection lost during COMMIT

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Retry every failure within `database.retry.max_elapsed` | Connection errors, version errors and privilege errors all retry | One rule | A wrong version or missing privilege waits for the full bound before it is reported |
| (b) Connection errors retry within `database.retry.max_elapsed`, then `IsUnavailable`; version, encoding and superuser failures fail at once (chosen) | At start-up and per transaction, a failover error is retried with jittered backoff until the bound or the request deadline. A COMMIT that the server rejects with a failover error did not commit and is retried. A COMMIT whose outcome is unknown (the connection closed without a server answer) is never retried and answers `503` `database_unavailable`. | Configuration errors surface at once; a failover is absorbed | An unknown COMMIT outcome leaves one case where a client retry may duplicate a committed write |
| (c) Never retry at open | The first failed connect ends the process | Simple | A replica restarts for every failover window |

**Choice:** (b).

**Rationale:** A failover is normal in an HA deployment and must not surface as an error to clients.
A configuration error will not heal by waiting, so it fails at once. The unknown COMMIT outcome cannot be
retried safely by the server, so the replica reports it. The gap is accepted: idempotent operations
(npm identical publish, NuGet identical push, idempotency keys of [api-and-ui.md](api-and-ui.md)
R-API-014) absorb a retry, and other writes are not retried by the server. The development agent would
revisit this if a write without an idempotency key shows a duplicate in the audit trail.

**Applied in the spec:** R-ARCH-019 does not yet state the unknown-COMMIT rule; this block is its record
and no requirement text changed in this round.

### Q-ARCH-25 · READONLY replies and the circuit breaker

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Count a `READONLY` reply as a failure | The breaker opens on a demotion | One rule for every error | A demoted primary keeps the replica away from the new primary for `valkey.circuit_breaker.open_duration` on top of the refresh that finds it |
| (b) Neutral for the breaker; counted as `trove_valkey_errors_total{kind="readonly"}`; the next probe or refresh marks Valkey degraded (chosen) | The client moves on at once | The demotion is visible in metrics, and the bound stays the refresh interval plus 1 s | A replica that cannot reach the new primary is not counted as failing until the probe runs |
| (c) Ignore `READONLY` entirely | No metric, no state change | Smallest code | Blind to a demotion |

**Choice:** (b).

**Rationale:** A `READONLY` reply says that the node was demoted and that the client is about to move
on. It does not say that Valkey is down. Opening the circuit for it would delay the failover that the
replica is already following. The development agent would revisit this if the metric shows
`READONLY` replies that do not end in a primary change.

**Applied in the spec:** R-ARCH-031 states the bound of the failover (refresh interval plus 1 s);
the breaker treatment is the choice of this block.

### Q-ARCH-26 · Valkey key families, expiry and keyspace

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Free-form key strings with an expiry chosen per call | Any code may write any key | No registration | A key without an expiry is possible, and nothing checks the name |
| (b) Key families registered at package initialisation with a mandatory positive maximum expiry (chosen) | `RegisterKeyFamily(mechanism, version, maxTTL)` creates the family; `(*Valkey).Key(family, parts...)` forms a key with the family's maximum expiry; `KeyTTL` may shorten it, never lengthen it | A key without expiry cannot be written through the API. Names are checked in one place. | A new mechanism needs a family registration |
| (c) One global key builder with string concatenation | A helper joins its arguments | Short code | Raw names can reach a key, which the rule against raw identifiers forbids |

Rules of the chosen option: the mechanism matches `^[a-z][a-z0-9_]*(:[a-z][a-z0-9_]*)*$`; a part matches
`^[A-Za-z0-9_-]{1,128}$`; the keyspace is the first 12 hex digits of `system_state.keyspace_id` after its
dashes are removed (the UUID is read once at start-up, `internal/app`).

**Choice:** (b).

**Rationale:** The registry makes "every key expires" a property of the code, not a review item. The part
pattern keeps raw names out of keys. Removing the dashes keeps the keyspace in a fixed hexadecimal form
for every installation. The development agent would revisit this if a mechanism needs keys that are
built from more than one family, or a part that cannot be an identifier or a hash.

**Applied in the spec:** R-ARCH-030: the registry file is `internal/cluster/valkey_keys.go`; the part
form and the `Key` call are stated; the keyspace derivation says that the dashes are removed.

### Q-ARCH-27 · Valkey connections: data, Pub/Sub and Sentinel probes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One connection for commands and subscriptions | Pub/Sub shares the data connection | Fewest connections | A subscription holds a connection that commands need; a demotion is harder to follow |
| (b) One dedicated Pub/Sub connection that follows the primary; a short probe connection per Sentinel at each refresh (chosen) | The data client is `valkey-go`; the Pub/Sub connection is named `trove:<instance>:pubsub` and re-subscribes after every primary change; trove's own Sentinel loop opens a short connection per Sentinel every `valkey.sentinel.refresh_interval` and closes it | The subscriber and the commands do not block each other; the connection count stays fixed | Two connections more than (a) |
| (c) A Pub/Sub connection per change area | Each area subscribes on its own connection | Isolation per area | The connection count grows with the areas, against the bound of R-ARCH-031 |

**Choice:** (b).

**Rationale:** A replica needs a fixed, small number of connections to Valkey, and a subscription must
never stop the commands that a request waits for. The name of the Pub/Sub connection makes it visible
in `CLIENT LIST`. The development agent would revisit this if the replica count makes the fixed set of
connections a problem at the Valkey server's connection limit.

**Applied in the spec:** R-ARCH-031: the Sentinel reconciliation paragraph states the probe connections.
The naming of the Pub/Sub connection follows from the same choice; the subscriptions paragraph is consistent with it.

### Q-ARCH-28 · Layering of the blob store, and where its garbage-collection job registers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `blob` as a domain package that formats call directly | An arrow from `formats` to `blob` | Formats can store bytes without the host | Formats hold storage logic, and the layer rule has no leaf for shared types |
| (b) `blob` as infrastructure; formats reach it only through the host interface; the GC job registers in `internal/app` through `Collector.RunJob` (chosen) | `blob` imports neither `jobs` nor the formats. `internal/app` builds the job kinds from a `JobRun` adapter. | The layer rule holds; `blob` is testable without the job framework | One adapter in the composition root |
| (c) `blob` imports `jobs` to register its own kinds | The store registers its job kinds itself | Local registration | `blob` depends on the domain layer, which R-ARCH-002 forbids |

**Choice:** (b).

**Rationale:** The layer rule of R-ARCH-002 puts `blob` below the domain. Formats must not hold storage
handles, so the host interface is the only path to storage (R-ARCH-009). The composition root is the one
place where all layers are visible, which is where the registration belongs.

**Applied in the spec:** R-ARCH-002: the layer list has the leaf layer and names `blob` as infrastructure;
the diagram has no arrow from `formats` to `blob`. The archtest rule for format packages is an
allowlist that does not admit `internal/blob` (R-ARCH-008).

### Q-ARCH-29 · What the scheduler enqueues on each tick

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Run a task only at its first scheduled time | A new installation waits for the first matching time | No surprise run at start-up | A weekly task waits up to a week after install |
| (b) Each tick enqueues the latest due slot of every enabled task (chosen) | The scheduler inserts the most recent due slot of each enabled task into `job` with the task name as its key; the unique index `job_task_slot` on `(task, scheduled_for)` refuses a second insert of the same slot | A fresh installation runs each task once; missed runs during an outage collapse into one | A task that was due long ago runs once, not once per missed slot |
| (c) Run every missed slot | Each due slot is enqueued | Complete history | A long outage replays hours of maintenance at once |

**Choice:** (b).

**Rationale:** The coalescing of missed runs is what [ha-and-helm.md](ha-and-helm.md) R-HA-005 already
requires for outages. The unique slot index makes a double insert harmless, which is also what keeps
the task single across replicas. The development agent would revisit this if a task needs every slot,
for example one that must process each day's data.

**Applied in the spec:** R-HA-005 names `job_task_slot` as the slot record (see [ha-and-helm.md](ha-and-helm.md)).

### Q-ARCH-30 · Fencing of singleton writes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No token: writes on the lease's own session (chosen) | Every state change of a singleton runs on the session that holds the lease advisory lock; if the session dies, the lock and the write fail together ([ha-and-helm.md](ha-and-helm.md) R-HA-006) | The fence is the database's own lock on the connection that writes; nothing to hand out or compare | The fence works only where the session is kept, so the session host must be direct or session-mode |
| (b) `pg_current_xact_id()` as a fencing token, checked against `pg_locks` in the write transaction | Each acquisition yields an `xid8`, which the writer presents | Looks like a classic fencing token | Not monotonic across an asynchronous failover, so a later holder can carry a lower token; and a `pg_locks` check on another session is not a fence. Rejected also because nothing in the code used it (architecture review, A7) |
| (c) Table-based leases with fencing tokens | Lease rows with a counter | Works through any pooler | Gives up the immediate release on session end that R-HA-006 builds on (Q-ARCH-14) |

**Choice:** (a).

**Rationale:** The lock and the write share one session, so a replica that lost its lease cannot commit. A token
adds a second mechanism that is weaker than the session. The development agent would revisit this only if a
singleton must write through a pooler that cannot keep a session, which Q-ARCH-14 already excludes.

**Applied in the spec:** R-HA-006 is the rule and is unchanged; R-HA-052's stale "lease table" wording is fixed.
No token is specified anywhere.

### Q-ARCH-31 · How TLS material is reloaded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Poll the file content every `server.tls.reload_interval` and compare a hash (chosen) | A timer reads both files and swaps the certificate when the content changes | Works on every volume type, including Kubernetes Secret volumes whose files are replaced by a symlink swap; no watcher to lose events | A change takes up to one interval to appear (default 10 s) |
| (b) Watch the files with a filesystem notifier (fsnotify) | The kernel reports changes | Immediate | Missed events when a Secret volume swaps a symlink, which needs a watch on the directory; a dependency for each platform |
| (c) Restart the process on a change | The pod is restarted | Simplest | Violates the owner constraint of 2026-10-29 (no restart) |

**Choice:** (a), with the interval as the one knob. The bound of R-ARCH-035 (reload_interval + 1 s) follows.

**Rationale:** The owner requires that rotation needs no restart, and Secret volumes are the mechanism the chart uses. A
polling loop is the one design that every mounted file type satisfies. The development agent would revisit this if
the interval must be below one second, which would move the check to a watcher on the directory.

**Applied in the spec:** R-ARCH-035 states the reloader and its bound.

### Q-ARCH-32 · Client certificates on trove's client listener

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Client certificates required on every connection | The listener refuses clients without a certificate | Strong authentication of the transport | Breaks every package client and browser, and the kubelet probes cannot present one |
| (b) Optional: `server.tls.client_ca_file` set means verified, off by default (chosen) | Only an installation that sets the CA requires a certificate, typically for a proxy that connects to trove | Proxies can prove themselves; clients are unaffected by default | An operator must decide per installation |
| (c) No client certificates | Verification is never offered | Smallest code | No way to pin a proxy to trove |

**Choice:** (b).

**Rationale:** The owner needs trove to accept TLS from a reverse proxy. Client certificates are a stronger tie for that
proxy, not for people, which is why the identity model (INV-7) stays with the external provider. The development agent
would revisit this when a proxy-to-trove setup needs per-client identities, which the authentication work would carry.

**Applied in the spec:** R-ARCH-035 states the switch and its default.

### Q-ARCH-33 · One certificate pair for both listeners, or one pair each

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One pair for both listeners by default; `server.ops_tls` may name its own pair (chosen) | `server.tls` serves `:8080` and `:8081`; an installation that wants a separate ops certificate sets `server.ops_tls` | One rotation covers both; the chart needs one Secret | Both listeners share one private key unless the ops pair is set |
| (b) Separate pairs always | Two Secrets, two rotations | Separation by construction | Twice the rotation and mounts for every installation |
| (c) Ops listener in plain HTTP | Probes and metrics unencrypted | Simplest probes | Violates INV-20 |

**Choice:** (a), with `server.ops_tls` as the override.

**Rationale:** The probes and the metrics live on the same pod and the same trust domain, and one pair keeps the
rotation simple. The override covers the case where the monitoring side requires a different CA. The development agent
would revisit this when an installation has a monitoring CA that must not sign client traffic.

**Applied in the spec:** R-ARCH-035 states the default pair and the override.

### Q-ARCH-34 · Key files: base64 text only, or raw bytes too

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Base64 text only | A key file is 43 or 44 base64 characters; a generator that writes random bytes needs a conversion step | One format | The owner's secret generator (internal-secrets-operator, `type: bytes`) writes the 32 bytes themselves, so its Secret cannot be mounted as it is |
| (b) A file of exactly 32 bytes is the key; anything else is base64 text (chosen) | The length decides, before anything is trimmed. Base64 text of 32 bytes is 43 or 44 characters (45 with a newline), never 32 | A generated Secret is usable as written; the existing base64 files keep working; no flag, no second key list | A 32-character text file is read as a raw key, and a raw key plus a stray newline (33 bytes) is refused with its size |
| (c) A second key list for raw files (`encryption_key_raw_files`) | The operator says which format a file has | No guessing | Two lists and a rule for their order for a distinction the length already makes |

**Choice:** (b).

**Rationale:** The two formats cannot be confused at the length of a key, so the file needs no marker. The check runs
before `TrimSpace` because a random key may begin or end with a white-space byte. The error for any other size says
"32 random bytes, either raw or base64-encoded". The development agent would revisit this if a key length other than
256 bits is ever allowed, since the length then no longer tells the formats apart.

**Applied in the spec:** R-ARCH-021 states both formats in the key-ring bullet.

### Q-ARCH-35 · Which forwarded header family does the edge rewrite?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A client can send both `X-Forwarded-For` and `Forwarded`; the edge writes one of them. An audit of the first design
found that the address resolver preferred `Forwarded` and fell back to `X-Forwarded-For`, so a client could choose the
address the log records.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One family, chosen by `server.forwarded_header` (`x-forwarded-for`, the default, or `forwarded`), no fallback | the other family is never read | a client cannot choose the address through the family the edge does not write; one rule for address, scheme and host | an edge that writes the other family needs the setting changed |
| (b) `Forwarded` first, `X-Forwarded-For` second | the first design | works with either edge | the client chooses whenever it sends the preferred family |
| (c) `X-Forwarded-For` only | no RFC 7239 support | one family, one format | an edge that writes only `Forwarded` cannot be configured |

**Choice:** (a). The chart's default is `x-forwarded-for`, which is what the ingress controllers write.

**Rationale:** The client address is the input of rate limits, audit and authorization conditions, so it is the most
exposed input of the request. One family and no fallback make the trust rule a statement about one header. The
development agent would revisit this when an edge can write only the other family; the change is the setting, not the rule.

**Applied in the spec:** [R-ARCH-006](#r-arch-006--client-address-and-trusted-proxies) (the family rule and the spoof
acceptance case), [R-OPS-005](operations-and-observability.md#r-ops-005--external-base-url-and-forwarded-hosts) and
[endpoints.md](endpoints.md) R-EP-003 ([Q-EP-15](endpoints.md#q-ep-15--which-forwarded-host-counts-on-an-endpoint)).

### Q-ARCH-36 · The ops listener before its certificate loads

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Under HTTPS probes the kubelet does not verify the certificate, but it needs a completed handshake. A listener that
refuses every handshake until its pair loads fails the startup and liveness probes, and the container is restarted in a
loop, although R-ARCH-035 says the replica stays unready and serves once the files are valid (H-68).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse handshakes (as before) | Probes fail until the pair loads | Nothing unverified is ever served | A restart loop that hides the cause (a missing secret) behind kubelet back-off |
| (b) Ops listener serves a temporary self-generated certificate until its pair loads (chosen) | In memory, ECDSA, one hour, CN = instance; `/healthz` 200, `/readyz` 503 with `tls` failing; the client listener still refuses | The replica stays up, unready, with the cause visible in `/readyz?verbose=1`, the log and a metric | An ops client that does not verify talks to an unauthenticated endpoint for that time; probes and metrics only, no secrets |
| (c) Probes over plain HTTP while the pair is missing | A second plain port | Simple | A plaintext switch the chart cannot express; against INV-20 |

**Choice:** (b). The temporary certificate is never offered by the client or an endpoint listener. A client that verifies
(a scraper with the CA) still fails, as it should. `trove_tls_temporary_certificate{listener}` and one warning log mark
the state. A pair whose leaf is not valid yet (SA10) is refused like an expired one, the old pair stays, and the same
content loads once its time has come.

**Rationale:** A restart does not fix a missing file; unready does. The ops listener carries probes and metrics, no
credentials. The development agent would revisit this if the ops listener ever carries an operator API.

**Applied in the spec:** [R-ARCH-035](#r-arch-035--tls-on-troves-listeners-and-reloadable-tls-material).

### Q-ARCH-37 · Who may build an endpoint, and what an endpoint answers outside its surface

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The text rule of R-EP-002 (a regular expression over sources) | Names `httpserver.WithEndpoint(` and `Endpoint{` | Cheap | An import alias, a function value, `new(T)` or a type alias get around it |
| (b) A typed rule over `go/packages` plus an unexported setter (chosen) | `withEndpoint` is unexported; `NewEndpoint`, `Chain` and a value of `Endpoint` appear only in `internal/app` and `internal/httpserver`; the check resolves types, so aliases and function values are found | Cannot be spelled around | Loads the module's types (about 2 s) |

**Choice:** (b), with three rules that close the same hole from the other side: a packages-family pattern that can
match a path of the API or of the token exchange (`/api/{format}/…`) fails `CheckRouteTable` (SA9); a path of a family
outside the surface answers the plain `404`, never a `405` (R-EP-005); a wildcard host matches a DNS label only and a host
with a byte outside printable ASCII is no host (SA4). `base_url` hosts of a host-checking endpoint must be DNS names: an
IP literal and a trailing dot are refused at load rather than normalised, because a trailing dot is normalised on the
request side only and a silently rewritten base URL would change the URLs in responses (SA7).

**Rationale:** The endpoint of a request is its security context; the rule must hold against the next author, not only
the last one. The development agent would revisit the trailing-dot refusal if an operator needs fully qualified names.

**Applied in the spec:** [R-ARCH-005](#r-arch-005--http-listeners-routing-and-middleware-chain) and
[endpoints.md](endpoints.md) R-EP-002, R-EP-003, R-EP-005 (text edits belong to the owner of that page).

### Q-ARCH-38 · The limiter is created from a set, not by a package function

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `LimiterSet.NewLimiter(name, scope, rate, burst)` | A set holds the Valkey client, the metrics registry and the live-replica count; limiters are created from it | No package-level state (R-ARCH-032); several replicas in one test process; the Valkey client stays private to `internal/cluster` | The signature differs from R-ARCH-034's `cluster.NewLimiter(...)` |
| (b) `cluster.NewLimiter` with a package-level default client | Matches the text | One call | A global client; tests with several replicas cannot coexist |

The verdict also gained fields (`Remaining`, `Reset`, `Window`, `Mode`, `Limit`) that the `RateLimit` fields and the fallback gauge need, and the call that counts and decides is `Take` (with `Allow` as its boolean); the count-only calls are `Exceeded` and `Count` as specified.

**Choice:** (a).

**Rationale:** The set is the unit a replica owns. The development agent would revisit this if a package needs a limiter before the composition root exists.

**Applied in the spec:** R-ARCH-034: "A limiter is registered with `LimiterSet.NewLimiter(name, scope, rate, burst)`".

### Q-ARCH-39 · The start-up canary of R-ARCH-021 is built with the key-ring status

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) At step 8, with the API wiring | `verifyKeyRingCanaries`: refuse when sealed values reference a key the ring lacks, seed the canary of each key ID, refuse a ring that cannot open an existing canary | The status of R-API-049 reports a canary state that exists; the guard of R-ARCH-021 exists before the first secret | New start-up step on every replica |
| (b) Leave the canary to the first sealed column | Status shows `absent` everywhere | Nothing to do now | R-ARCH-021 says the guard comes first, and a mismatched ring is cheapest to catch before data exists |

**Choice:** (a). The seed is `INSERT … ON CONFLICT DO NOTHING`, so two replicas starting together agree; a second ring with other key material under the same ID fails at start-up with `key ring does not match database`.

**Rationale:** The check costs one query at start. The development agent would revisit this if start-up must work without PostgreSQL write access.

**Applied in the spec:** R-ARCH-021's start-up canary is implemented as described; no text changes.

### Q-ARCH-45 · A limiter may divide its fallback share by its own count

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the endpoint rate-limit classes of [endpoints.md](endpoints.md) R-EP-015 divide the budget by the replicas that serve
the endpoint, the replicas of a pool, while the set's count is the replicas of the role.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `LimiterSet.NewLimiterShared(name, scope, rate, burst, share)`: a limiter with a count function of its own; `NewLimiter` passes none | One set, one registry of metrics, a per-limiter `n` | The fallback gauge and the decisions counter stay one family per replica; the API classes are unchanged | Two constructors |
| (b) A second limiter set per pool | A set with another count | No new constructor | Registers the metric families twice on one registry; more state per replica |

**Choice:** (a).

**Rationale:** The share is a property of what a limiter guards, not of the replica. The development agent would revisit this if
a limiter needed a share per bucket.

**Applied in the spec:** R-ARCH-034 (the fallback bullet).

### Q-ARCH-40 · The lookup of an unknown key runs before the decision, and the decision waits for the grant index

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-011 looks the repository up (step 2) before it authorizes (step 4); R-AUTHZ-016 asks for authorization before the existence lookup so that the answer and its time do not depend on existence.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Authorize first, look up after | The decision runs on the grant index of the replica | R-AUTHZ-016 to the letter | A repository created through another replica is unknown to the index until its rebuild, so a request right after the creation is refused (R-API-052 broken) |
| (b) Look an unknown key up first, discard the result, bring the index up to date, then decide (chosen) | `SnapshotReader.ByKey` finds the row in PostgreSQL and runs the change-log poll; `authz.Engine.CatchUp(key)` reads the database epochs (the coalesced read of R-AUTHZ-032) and waits for an index at least that new, only for a key the index does not hold | R-ARCH-011 and R-API-052 hold; the answer is still made by the decision alone, so hidden and missing stay the same bytes; a known key costs a map lookup | A request for an unknown key costs one indexed query per key and entry lifetime (the negative cache of R-ARCH-011) before it is refused, which R-AUTHZ-016 would rather not see |

**Choice:** (b).

**Rationale:** The extra cost exists only for a key that is not in the replica's snapshot, bounded by `cache.unknown_repo_keys`, and its result never reaches the answer. The development agent would revisit this if the response-time argument of R-AUTHZ-016 is to be tested with a timing probe.

**Applied in the spec:** R-ARCH-011 and R-AUTHZ-016 read unchanged; [docs/developer/files.md](../developer/files.md).

### Q-ARCH-41 · The content headers go on responses that carry content, not on problems

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-007 says the wrapper sets the headers before the handler runs, "on every response whose body comes from package content".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Wrap before the handler | Every answer of the route carries them | As written | A problem answer (404, 403) would carry `Content-Disposition: attachment` and the sandbox policy for a body that is not content |
| (b) Wrap when the response carries the file or the index (chosen) | `httpserver.ContentWriter` wraps the writer where the item is resolved (200, 206, 304, HEAD and the index); it applies the headers at wrap time, at `WriteHeader` and at the first `Write` | A handler cannot weaken them; a problem is a problem | The wrapper is not in the chain but in the handler |

**Choice:** (b).

**Rationale:** The criteria of R-ARCH-007 (the four headers on `evil.svg`, a handler that deletes one still sends it) hold. The development agent would revisit this if a format route kind needs the wrapper before it knows the status.

**Applied in the spec:** R-ARCH-007 reads unchanged.

### Q-ARCH-42 · Paths that ServeMux would redirect reach the files handler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-GENERIC-002 answers 400 `invalid_path` for an empty or dot segment; `net/http.ServeMux` redirects a path such as `/files/r/a//b` to its cleaned form before any pattern is tried.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Accept the redirect | The client follows to `/files/r/a/b` | Nothing to build | A path the specification refuses is silently rewritten; `a/../x` becomes `x` |
| (b) `Router.HandleUncleaned("/files/", h)` (chosen) | A request below the prefix whose path the mux would clean goes to the handler, which answers 400 | The criteria of R-GENERIC-002 hold; the prefix is dropped on an endpoint without the files surface | A second dispatch in the router for one family |

**Choice:** (b).

**Rationale:** Encoded dot segments (`%2e%2e`) never reach the mux's cleaning and are decoded once by `CleanPath`. The development agent would revisit this if a format route also needs raw paths.

**Applied in the spec:** R-ARCH-005 and R-GENERIC-002 read unchanged.

### Q-ARCH-43 · The files transfer is two packages: internal/files and internal/api/rawfiles

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-003 names no package for the items and the raw transfer.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One package at the edge | Handler and items logic together | One place | The items operations of the management API and the format dispatcher would import an edge package for the logic |
| (b) `internal/files` (domain) and `internal/api/rawfiles` (edge) (chosen) | The service decides and writes; the handler parses the request and writes the headers | The layers of R-ARCH-002 hold; the management API, the later format dispatcher and the jobs call the service | Two packages |

**Choice:** (b).

**Rationale:** The same split as repository and API. The development agent would revisit it if the format dispatcher takes over the handler.

**Applied in the spec:** R-ARCH-003 (the tree).

Superseded by [Q-ARCH-206](#q-arch-206--the-files-transfer-is-three-packages-the-item-store-the-generic-layer-and-the-raw-routes): the revisit condition was met when the formats gained storage hooks of their own.

### Q-ARCH-86 · The resource policy of content read with the session cookie

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The session cookie is `SameSite=Lax` and `/files/` accepts it for `GET`. A browser sends such a cookie with a subresource
request from any page of the same site, so a page on a sibling host (`wiki.example.com` next to `trove.example.com`) could
embed `<img src="https://trove.example.com/files/private/…">` or `<script src=…>` and load a private file with its visitor's
rights; `Cross-Origin-Resource-Policy: same-site` lets it through.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `same-site` for every response | As before | One header value | The embedding above |
| (b) `same-origin` when the session cookie authenticated the request (chosen) | `ContentWriterFor` chooses the value from the request's credential; a token or anonymous response keeps `same-site` | The policy binds no-cors subresource loads only: the UI's downloads, which are navigations, and its own image loads (same origin) are untouched (R-UI-001: package content reaches the browser only as a navigation or an `img` load); the content origin of R-ARCH-029 never sees the session cookie | Two values of one header |
| (c) Refuse the session cookie on `/files/` unless `Sec-Fetch-Dest` is `document` | The request is then anonymous | Also stops reads by `fetch` from the same site | Breaks the UI's `img` loads, and depends on how each browser labels a download |
| (d) `same-origin` for every response | One value | Simple | Blocks a same-site page from embedding public content that it may embed today, and the UI's image loads from the content origin (R-ARCH-029) |

**Choice:** (b).

**Rationale:** the credential that makes an embedding dangerous is the ambient one, and only the session cookie is ambient.
The development agent would revisit (c) if trove adds a route that serves package content to `fetch`.

**Applied in the spec:** R-ARCH-007 (the header list). R-STOR-017's directory index takes the same value.

### Q-ARCH-76 · The pace of the download that a rollout restart must not break

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-024's second criterion runs `kubectl rollout restart deployment/trove-web` during
`curl --limit-rate 20M` of a 400 MiB file (about 20 s). The rollout replaces the three web pods one after the other and takes
more than a minute, so at 20 MB/s the serving pod is replaced during the transfer in about one run of three: the criterion
passes without the drain having been exercised.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 20 MB/s as written | The criterion's number | Literal | Two runs in three do not touch the serving pod |
| (b) 6 MB/s (about 70 s) | The transfer outlasts the rollout (about 50 s) and ends well inside the 110 s shutdown timeout of a drained pod; 4 MB/s (100 s) left 10 s of margin and a slow object store made the drain cut the transfer, correctly | Every run drains the serving pod while it transfers; the test asserts that it did | Not the number in the criterion |
| (c) Repeat (a) until the serving pod is replaced | Retries | Literal pace | A flaky number of rounds, 20 s each plus a rollout |

**Choice:** (b), with the assertion that the serving pod was drained before the transfer ended.

**Rationale:** The intent of the criterion is that a restart does not break a transfer, which only a drained serving pod proves.
The revisit condition is a rollout fast enough that 20 MB/s always overlaps it.

**Applied in the spec:** none; [../developer/e2e.md](../developer/e2e.md#m1-tests).

### Q-ARCH-147 · One replica count per pool for every limiter of the client listener

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The failure throttle and the API limits divided their fallback share by the live replicas of the role, the endpoint classes by those of
the pool (Q-ARCH-45); both counted in goroutines on the bootstrap context, which ended first at shutdown.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two counters | As before | No change | Two denominators for one listener |
| (b) One component (chosen) | `replicaCount` counts the pool's serving replicas once per heartbeat interval, is a `Component`, and stops with the others; every limiter of the set uses it | One n | `NewLimiterShared` is no longer used by the composition root |

**Choice:** (b).

**Rationale:** a client request is answered by a replica of the pool, so the pool is the denominator for all of them. The development
agent would revisit this if a limiter guards a resource shared across pools.

**Applied in the spec:** R-ARCH-034 (the fallback bullet) reads "live replicas that serve the endpoint's pool".

### Q-ARCH-187 · A connection is held until the peer has taken its last bytes, and a cut is a reset

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the download of E1.13 through the edge fails in about half of the runs when the serving pod is replaced. The replica
logs a complete `200` and exits 0.2 to 0.5 s later; the client has 396 to 399 MiB and waits about 80 s for the rest. At the
moment the handler returned, the replica's send queue held 2.8 MiB (measured through the shutdown log of the fix:
2902082 unacknowledged bytes) which the edge, relaying at 6 MB/s, had not taken. `net/http` closes the connection when the
handler is done; the kernel would go on sending, but the process exits and the pod's network namespace with it, and an orphaned
socket dies there without a `FIN` or an `RST`. The edge neither gets the tail nor hears of the end. The same silence is why a
download cut at the shutdown deadline is noticed by its client only after the edge's own timeout.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Hold the connection | A connection that closes during the shutdown with unacknowledged bytes (`SIOCOUTQ`) stays open until they are acknowledged or the commit bound passes; a cut or overdue connection is closed with `SO_LINGER 0` (`RST`) | Exact: waits for the one signal that matters, costs nothing when the queue is empty, bounded by the plan; the cut becomes visible at once | `SIOCOUTQ` is Linux only (elsewhere the old behaviour); the accepted conn is wrapped, which hides `ReadFrom` (no `sendfile`, unused: bodies come from the object store) |
| (b) `CloseWrite` and read to EOF | Half-close, then read until the peer closes | Portable | An edge with keep-alive does not close; the wait is as long as its idle timeout, and TLS needs `close_notify` handling on top |
| (c) A fixed sleep before exit | Wait a second or two | Trivial | Wrong for any queue larger than the sleep times the reader's speed |
| (d) Change the edge | Shorter timeouts, no keep-alive to the backend | None for the lost tail: the bytes are gone before the edge can act | Moves the symptom, costs every request |

**Choice:** (a).

**Rationale:** The loss is in the replica's own exit, so the fix lies there. The bound is the commit bound of the shutdown plan
(`planShutdown`), inside the margin that is already reserved. Revisit if a platform without `SIOCOUTQ` runs the pods, or if a
reader that is slower than the margin allows must be served to the end (then the shutdown timeout, not this wait, is the knob).

**Applied in the spec:** R-ARCH-024 step 5; [../developer/e2e.md](../developer/e2e.md#findings-of-the-suite) finding 5.

### Q-ARCH-188 · The format catalog is a leaf package that every consumer reads, not a copy in each layer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the facts of a format (classes, layout, pattern subject, package names, priority members, pins, caps) were declared in
`formats.Descriptor` and mirrored three times: `repository.FormatInfo` with `repository.FormatCatalog` (and a second `Class` and
`PatternSubject`), the adapter `app.formatCatalog`, and a fourth copy in the files integration environment. The decision point
named NuGet's case folding, the literals `"npm"` and `"nuget"` and the exposure facts of the generic format itself. `repository` and
`authz` are domain packages and `internal/formats` is edge, so they cannot import the format contract.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/formats/catalog`, a leaf; consumers take a `*catalog.Registry`, nil is `catalog.Default()` | The format declares its facts once, `formats.Register` puts them into the catalog, `repository` and `authz` read them there; tests build a registry of their own | One declaration, no mirror; the facts sit next to the formats that declare them; a test shares no state and imports no format | A new leaf package; a nil option reaches the process registry silently |
| (b) `internal/catalog`, top level | The same, one level up | Short path | Reads as a product catalog; the formats' own tree is where a contributor looks |
| (c) A global registry only | `formats.Lookup` everywhere | No injection | The domain layer cannot import it; tests share state and depend on each other's registrations |

**Choice:** (a). The leaf holds `ID`, `Class`, `PatternSubject`, `RequestKind`, `Facts`, the one package-name normaliser (`Names`),
the exposure facts (`Exposure`, `Versioning`) and the `Registry` (`Register`, `Lookup`, `List`, `Freeze`, `NormalizeName`). It imports
nothing of this module. `formats.Register` derives the entry from the format and is the only production registration; a format
that has package names declares the normaliser, and grants, token scopes, exposure rules and repository patterns fold a name and a
pattern with it. A format the catalog does not know folds nothing and is not exposable (fail closed).

**Rationale:** the facts and the rule that folds a name are the same for every layer, so they are declared once and read, never
copied. The injection is what lets a test of the decision point name a case-insensitive format without importing one. The development
agent would revisit this if a fact needs the request types of the contract (then it stays in `internal/formats`), or if a second
process-wide registry becomes necessary (then the nil default is the first thing to remove).

**Applied in the spec:** R-ARCH-002 (the leaf list, the component table and the diagram) and R-ARCH-003 (the tree); Q-ARCH-28 loses
its stale remark. The contract sketch of R-ARCH-008 to R-ARCH-010 follows when the contract is written (WP2.1).

### Q-ARCH-189 · Shared value types are declared once in a leaf and named by alias, never mirrored

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: besides the format facts, the token scope existed three times (`token.Scope`, `httpserver.Scope` and the conversion back to
`token.Scope` in the subject), `authz.RequestKind` repeated `formats.RequestKind`, and `repository.Class` repeated `formats.Class`
(a string against a number). Every copy needed a conversion at a seam, and a field added to one was lost at the next conversion.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One declaration in a leaf, other packages alias or use it | `catalog.Class`, `PatternSubject`, `RequestKind` with aliases in `formats`, `repository` and `authz`; `internal/auth/scope` with `scope.Scope` used by the stage, `httpserver`, `token` and `authz` | No mirror, no conversion; a call site keeps its spelling through an alias | An alias hides where a type comes from |
| (b) Allow `authz` and `repository` to be imported by format packages | A format names the domain types | No alias | A format could call `NewService` or build an `Engine`; the layers meet in the contract |
| (c) Copies owned by each layer | As before | No new package | The drift this decision removes |

**Choice:** (a). The scope leaf holds `Scope`, `Grant`, `Patterns`, `Options`, `Default`, `Parse`, `ParseStored`, `Marshal` and
`Within`; `httpserver.Credential.Scope` is a `*scope.Scope` and its separate `Endpoints` field is gone (the binding is
`Scope.Endpoints`). The names `Action`, `Path` and a narrow authorization `Target` reach a format as aliases in the contract in
the same way when the contract is written (WP2.1). The token package keeps `Scope`, `ScopeGrant`, `Patterns`, `ScopeOptions`,
`DefaultScope`, `ParseScope` and `ParseStoredScope` as aliases and one-line forwards, so the identity area compiles unchanged
while it is being changed; the file is deleted when no caller spells them.

**Rationale:** a type that crosses a layer boundary is declared where both sides can import it. The development agent would
revisit this if an alias causes a wrong import in a format package, which the architecture test names.

**Applied in the spec:** R-ARCH-002 and R-ARCH-003 (the `auth/scope` leaf).

### Q-ARCH-190 · The M2 tables ship as three expand-only migrations before any package that uses them

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: milestone M2 adds the remote cache, the virtual repositories with claims and ownership, and the npm format, owned by about ten work packages that run in parallel. Each needs tables, the generated `q/models.go` changes with every schema change, and `RequiredSchema()` (R-HA-018) is the highest expand step, so each extra migration is another rolling-upgrade step.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One schema package, three files by area | `00014_remote_proxy`, `00015_virtual_claims_ownership` and `00016_npm` land first; later packages add query files only | The generated models change once; no package edits another's migration; the tables are registered and tested before any code uses them | A package that finds a column missing asks for a follow-up expand step |
| (b) Each package ships its own migration | A migration next to the code that needs it | The table and its owner arrive together | Parallel packages collide on the version numbers and on `q/models.go`; a gap in the numbering is an error (`ParseSteps`) |
| (c) One file for everything | `00014_m2.sql` | One step | One failure rolls back all of M2; the file mixes three owners |

**Choice:** (a). Every file is `-- trove:phase expand` without `no-transaction` or `requires-app`: no `DROP`, `RENAME` or `SET NOT NULL`, no backfill (no remote or virtual repository can exist before M2, Q-REPO-16), and the new indexes are on empty tables. A v0.3.x replica never reads the new tables, so it keeps serving against schema 16 during the rolling upgrade; the M2 binary requires 16. `repositories.claims_version` is the one change to an existing table, a column with a default. The tables are data tables (R-BAK-004), registered in `internal/meta/postgres/tables_m2.go` until their owning packages exist; the one new revision column is registered with `meta.RegisterRevisionColumn`.

**Rationale:** The packages that follow depend on the schema, not on each other's code, and the migration test (`TestTableRegistry`, `TestRevisionColumns`, `TestBlobReferenceRules`, `TestM2Migrations`) can cover the constraints before an implementation exists. The development agent would revisit this when a package needs more than one follow-up step: then it merges those into one `00017`.

**Applied in the spec:** none; [../developer/database.md](../developer/database.md#m2-tables) lists the tables.

### Q-ARCH-191 · Columns and tables the M2 schema leaves out or leaves without a constraint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: three sketches reach beyond M2. `package_owner.ci_issuer_id` ([authorization.md](authorization.md) R-AUTHZ-018) references `ci_issuers`, a table of M4 (R-CI-002). `npm_packages.search_vector` (R-NPM-006, R-NPM-024) serves the search of R-NPM-037, which is P1. The tables `remote_references`, `client_certificates`, `outbound_proxies` and `proxy_offline_switch` belong to P1 requirements (R-REPO-031, R-REPO-034, R-REPO-037).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Leave out what no MVP code reads; keep the column that M2 writes, without its foreign key | `ci_issuer_id` stays a plain `uuid` with the CHECKs; M4 adds the foreign key `NOT VALID` and validates it; no `search_vector`, no P1 tables | The CHECKs that bind `owner_kind` to the columns hold from the first row; every later addition is an expand step | The column is unconstrained until M4 |
| (b) Create the P1 and M4 structures now | An empty `ci_issuers`, the `tsvector`, the P1 tables | The sketches are followed to the letter | M4's table is created early without its design; an unused `tsvector` needs maintenance in every write path for no reader |
| (c) Add the owner columns in M4 | `package_owner` without the CI columns now | No unconstrained column | The CHECK that binds `owner_kind` to the columns has to be replaced, which is a `DROP CONSTRAINT` and forbidden in an expand file |

**Choice:** (a). `NOT VALID` and `VALIDATE CONSTRAINT` are allowed in expand files, and the M4 step adds the constraint on a table whose CI rows it can check. `npm_remote_packages.doc_sha256` is a digest, not a blob reference: it has no foreign key to `blobs`, so the blob reference rule does not apply; the stored document is referenced by `remote_cache_entries.blob_sha256`.

**Rationale:** A column that nothing reads is cost without value, while the constraint on a column that is written from M2 on must not wait. The development agent would revisit this if M4 needs the owner rows of CI projects to exist before `ci_issuers`.

**Applied in the spec:** [formats/npm.md](formats/npm.md) R-NPM-006 (the sketch marks `search_vector` as added by R-NPM-037); the `ci_issuer_id` line of the R-AUTHZ-018 sketch in [authorization.md](authorization.md) is the authorization work package's write-back.

### Q-ARCH-192 · The per-caller virtual engine is a domain package of its own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-003 puts "virtual resolver" into `internal/repository`. Resolution needs the caller's permissions (`authz`) and the remote engine (`proxy`) for tier checks, merged documents and the shadowing probe; `authz` already imports `repository` (the grant index reads the snapshot).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Routing in `repository`, resolution in `internal/virtual` | The caller-independent steps (members, claims, pin rules) stay in `repository`; per-caller resolution, the merged-metadata cache and the shadowing check move to a new domain package that imports `repository`, `authz` and `proxy` | No import cycle; the snapshot readers (`authz`) see the routing without importing the engine | One more package |
| (b) Everything in `repository` | As R-ARCH-003's tree says | One package | `repository` cannot import `authz` or `proxy`; every per-caller step needs a callback interface |

**Choice:** (a). `internal/virtual` is a domain-layer package (archtest `layerTable`), and the R-ARCH-033 source ban (no PostgreSQL advisory lock) covers it like `internal/proxy`.

**Rationale:** The import direction `authz → repository` is fixed by the grant index; the dependency of the engine on `authz` and `proxy` can only be satisfied by a package above both. The development agent would revisit this if the routing steps turn out to need the caller after all.

**Applied in the spec:** R-ARCH-002 (component table and diagram) and R-ARCH-003 (tree); `internal/archtest/layers.go` and `internal/archtest/source.go`.

### Q-ARCH-193 · A format reaches its own tables through a store port that a domain package implements

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: npm keeps `npm_packages`, `npm_versions`, `npm_tombstones` and the remote document records. Formats may not contain SQL or import the query package (R-ARCH-008, R-ARCH-002), yet the correctness rules of npm rest on constraints of those tables: `UNIQUE (repo_id, name_fold)` decides the look-alike race of R-NPM-007, the primary key of `npm_tombstones` refuses a republished version.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Store port in the format, implementation in `internal/formatdata/<id>` | The port is declared in the format package; a domain package implements it over the generated queries; the composition root binds it and checks at start-up that the binding implements the port | Keeps R-ARCH-008; the constraints stay in real tables | One more package per format with tables |
| (b) Facts in the generic `attributes jsonb` | No format tables | No new package | Loses the unique constraints and the tombstone key |
| (c) Formats import a leaf query package | The format calls `q` directly | Least code | Breaks R-ARCH-002 |

**Choice:** (a). `internal/formatdata` is a domain-layer package tree (archtest `layerTable`).

**Rationale:** The rules npm needs for correctness are database constraints, and only (a) keeps them without SQL in the format. The development agent would revisit this when a second format shows that the ports have a common shape worth a shared helper.

**Applied in the spec:** R-ARCH-002 (component table) and R-ARCH-003 (tree). The `Host` method that hands a format its bound store (R-ARCH-009) is the format contract's write-back.

### Q-ARCH-194 · The areas of M2: claims, cache invalidation and upstream state are change areas, the merged-metadata eviction a signal

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the table of R-ARCH-018 lists `claims`, `cache_invalidation` and `upstream_state` as change areas and `metadata_changed` as a signal. Code needs the registrations and the `cluster_epoch` rows before a package writes or watches them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Register all four with the schema package | The areas are declared in `internal/cluster/areas.go` between `>>> M2 schema` markers; the migrations seed the epoch rows of the three change areas | No package registers another's area; a replica that sees a payload version above its own flushes the area (R-ARCH-018 step 5) from the first release | The areas exist before any writer does |
| (b) Each owning package registers its area | Registration next to the code | Locality | The `cluster_epoch` row and the registration live in different changes, so a forgotten row is found only at run time |

**Choice:** (a), payload version 1 for each change area, no `cluster_epoch` row for the signal. `00014_remote_proxy` seeds `cache_invalidation` and `upstream_state`, `00015_virtual_claims_ownership` seeds `claims`. `TestM2Migrations` checks that every registered change area has its row.

**Rationale:** The areas are part of the contract of the tables, so they travel with them. The development agent would revisit this if an area needs a payload that its first writer cannot express in version 1.

**Applied in the spec:** none; the R-ARCH-018 table already lists the four areas.

### Q-ARCH-195 · What a waiter learns when a flight ends

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-033 has the holder publish `stored` or `failed` and has `Wait` return "when the holder publishes an outcome, when the lock is gone, or when ctx ends", without saying what `Wait` returns in each case, what `stored` means for an upstream `404` that became a negative entry, or how the leader's `traceparent` travels.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two outcomes | `stored` and `failed`; a lock that is gone is returned as `failed` | Smallest vocabulary | A waiter, its log line and a later metric cannot tell a holder that gave up from a lock that expired or was lost in a failover |
| (b) Three values | `stored` and `failed` are published; `gone` is returned by `Wait` alone when the key is absent and no message was heard. `stored` means "an entry or a negative entry exists now". The message is the outcome, optionally followed by a space and the `traceparent` | Distinguishes the three ways a wait ends; the message stays the literal `stored` or `failed` that R-ARCH-033 shows; a recorded `404` needs no word of its own | One more value to document; callers must treat all three alike |
| (c) A word per result | `stored`, `negative`, `failed`, `gone` | The waiter knows which of the entry or the negative entry to read | The waiter repeats the checks of R-REPO-025 steps 4 to 8 in any case (R-REPO-026 step 4), so nothing is decided by the word; one more vocabulary to keep in step between the holder and the waiter |

**Choice:** (b).

**Rationale:**
- Every waiter does the same thing for every value: repeat the proxy's checks and perhaps acquire. The vocabulary is for the log and for diagnosis, so it should tell the three endings apart and no more.
- A negative entry is written before the release (R-REPO-026 step 5), so "something is stored now" is true for it, and the waiter finds it through the negative-entry check.
- The trace context rides in the message so that no second message or key is needed. Only a short printable token (at most 256 characters, no whitespace) is sent; anything else is dropped, never an error, because the release must not fail over a header.
- `Wait` returns only the outcome today. The development agent would revisit this when waiters add the span link of R-OPS-018: `Wait` then needs a second result for the `traceparent`, an additive change to the interface.

**Applied in the spec:** R-ARCH-033 (interface block, Release bullet, the unavailable bullet).

### Q-ARCH-196 · The life of a hold, and what a failed call is

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-033 has the hold extend its lock every third of the TTL while the fetch runs, and has `Acquire` and `Wait` return `ErrUnavailable` "when the breaker is open or a call fails". Open points: who drives the extension, what a failed extension means, how long a hold may live, which errors are `ErrUnavailable`, and whether the in-process single-flight group lives in `internal/cluster`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One goroutine per hold | `Acquire` starts a ticker goroutine bound to the Valkey client's lifetime; it extends with `SET … IFEQ <token> PX <ttl>` every TTL/3, ends at `Release`, when an extension finds the key gone or foreign, or when the client closes | The caller has nothing to remember; the hold cannot be forgotten by a code path that returns early; a failed call is retried at the next tick | One goroutine per running fetch, bounded by `proxy.max_concurrent_fetches` plus the fetches waiting for a slot |
| (b) One loop per `FlightLock` | A single loop extends every live hold, in a pipeline | Fewest goroutines | A registry of holds to keep consistent with `Release`; one slow call delays every hold; no gain at the concurrency a replica has (tens of fetches) |
| (c) The caller extends | `FlightHold.Extend` is called from the fetch loop | No goroutine | A path that forgets, or a transfer that does not return to the loop for a TTL, loses the lock: the failure is silent and in the proxy |

**Choice:** (a). Every failed call, a breaker that is open and a missing keyspace included, returns an error that wraps `ErrUnavailable` (`errors.Is`), after counting `trove_valkey_degraded_total{mechanism="flight_lock"}`; a context that ended returns its own error; a flight key that was not formed with `NewFlightKey` is a programming error and is neither. A hold has no longest life of its own: the proxy ends it (a fetch that has not finished 5 s before the shutdown deadline is released with `failed`, R-ARCH-024 step 4), and closing the client ends every extension. The in-process single-flight group is not in `internal/cluster`: it shares the fetch result between requests of one replica and is part of the proxy engine (`internal/proxy`), in front of `Acquire`.

**Rationale:**
- A transient failure of an extension must not end the hold: the next tick may reach Valkey. If the key expired meanwhile, that tick finds it gone and the loss is counted once, which is the case R-ARCH-033 describes.
- One rule for the caller keeps R-REPO-026's fallback in one place: anything that is not a context error and not `ok`, `Acquire` and `Wait` alike, means "fetch under the in-process single-flight and count `valkey_unavailable`". A Valkey reply the client does not classify (a type error) is also "go on without the lock"; the request never fails because of the lock.
- The group is generic request plumbing (`golang.org/x/sync/singleflight`, R-REPO-026 step 2) whose result type belongs to the engine. The infrastructure layer would otherwise need to know entries and documents.
- The development agent would revisit (b) when the per-replica fetch count rises into the thousands, and the lack of a longest life when a leaked hold shows up in `trove_flight_lock_lost_total` or in keys that stay for the maximum.

**Applied in the spec:** R-ARCH-033 (unavailable bullet); the single-flight placement is stated in [../developer/valkey.md](../developer/valkey.md#flightlock).

### Q-ARCH-197 · The negative store checks the form of an entry; the proxy judges its validity

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-028 has readers validate an entry (`c` within the repository's current `notFoundTtlSeconds`, no later invalidation covering the path), delete a malformed one, and keep a bounded L1 in front of Valkey. `internal/cluster` knows neither the repository's period, nor the path (the key holds a hash), nor the invalidations.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Form in the store, validity in the proxy | `NegativeStore` writes and reads `{"s", "c"}` and removes a malformed value; the proxy's wrapper holds the L1 and applies the window and the invalidations | Each layer decides what it knows; the store can be tested against Valkey alone | The metric `rejected` is split between two places |
| (b) The store validates | `Lookup` takes the period and a predicate over the invalidations | One place counts and decides | `cluster` would depend on the invalidation set and on paths, against the layering (infrastructure below the domain) |
| (c) The store holds the L1 as well | One type with L1, Valkey and validation | One object for the proxy to call | The L1 is a registered cache of the proxy (R-ARCH-032, `proxy.negative_l1_entries`) and follows the cache rules of that layer |

**Choice:** (a). `Lookup` decodes strictly (exactly `s` of 404 or 410 and `c` above 0, no other field, nothing after the value); anything else is deleted with `DELIFEQ` of the value that was read, so a writer that stored a good value meanwhile keeps it, counted as `rejected` and returned as not found. `Record` with a period of zero or less stores nothing (`notFoundTtlSeconds: 0`), refuses any status but 404 and 410, and refuses a period above 24 h plus 5 s. The L1, the validity window and the invalidations are the proxy's. `hit` is counted when the store returns an entry; an entry that the proxy then refuses is counted there as a further `rejected`.

**Rationale:**
- Only the proxy knows the period, the path and the invalidations, and R-REPO-028 already puts them with the reader.
- The deletion of a value that was read, not of the key, is what the ACL allows (`DELIFEQ`, no `DEL`) and what is safe against a concurrent writer.
- A refused entry is counted twice, as `hit` and as `rejected`: the ratio `hit ÷ (hit + miss)` of R-OPS-045 then overstates the saving by the refused entries, which are rare (they exist for the seconds after an invalidation). The development agent would revisit this if the ratio is used for alerts, and give the store a way to take back a `hit`.

**Applied in the spec:** R-ARCH-030 (the negative-cache row), R-OPS-045 (`trove_valkey_cache_lookups_total` row).

### Q-ARCH-198 · Source-address binding of a remote's connections

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-024 binds a remote's upstream connections to the first interface address inside `sourceNetwork`, enumerated at start and every 60 s, and forbids a fallback to the kernel's choice. The text leaves open who enumerates (a goroutine or the dial path), what a failing enumeration means, and what a replica does when the address it enumerated has gone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Background goroutine per client | Each bound client starts a ticker that re-enumerates every 60 s | The dial path never enumerates | A goroutine to stop for every client that the upstream-client cache replaces; the lifetime of a client is not visible to the factory |
| (b) Enumeration on the dial path once the last one is 60 s old (chosen) | The option enumerates once when the client is built; a dial that finds the result 60 s old enumerates again, under a mutex | No goroutine, no lifetime; an idle client costs nothing; the interface lister is a plain injected function | One dial per minute and client pays for the enumeration |
| (c) Enumerate on every dial | No cache | Always current | A netlink call per connection, on the path that opens one connection per fetch |

Failure of the enumeration itself: the last good address is kept (a lister that fails says nothing about the address), and a client that never had one answers `ErrSourceNetworkUnavailable` with the lister's error joined. A successful enumeration without an address in the prefix drops the address at once: the address is gone, and using it would leave through an interface the upstream's firewall rejects. The prefix is masked before use, IPv4-mapped interface addresses are matched as IPv4, and the first match in interface order wins.

**Choice:** (b).

**Rationale:** The connection pool of a client outlives single requests, but a client is rebuilt whenever the remote's settings change, so a goroutine per client needs a close path that the factory does not have. The cost of (b) is bounded by the 60 s interval. Revisit if replicas show dial latency spikes at the interval, or if a lister can block for long: then (a) with an explicit `Close` on the cached client.

**Applied in the spec:** R-REPO-024 `sourceNetwork` bullets name the lazy re-enumeration and the failing-lister rule; the engine (WP2.2-B) counts `trove_remote_source_network_unavailable_total` and rate-limits the log line where it sees `outbound.ErrSourceNetworkUnavailable`.

### Q-ARCH-199 · Where the credential decision of each hop is made

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-033, R-REPO-036 and R-REPO-039 send credentials, custom headers and the client certificate only to the upstream origin and `sendCredentials` hosts, and to other origins of a redirect chain only with `forwardAuthOnRedirect`. `net/http` copies the headers of the first request to every redirect and strips only `Authorization`, `Cookie` and `WWW-Authenticate` when the domain changes; it also strips `Authorization` on a hop to another domain that is allowed to have it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The caller sets the headers and a redirect check removes them | The engine puts credentials on the request; `CheckRedirect` deletes them on a foreign hop | No new interface | The first request and the redirect are decided in two places; a hop to a `sendCredentials` host loses the `Authorization` that `net/http` stripped; a forgotten header leaks |
| (b) The client decides on every hop in its round tripper (chosen) | `WithHopPolicy(p)`: the round tripper calls `p.Decorate(req, hop)` on a copy of every request it sends, `hop` counted from the `Response` links that `net/http` sets on redirect requests. `CredentialPolicy` is the implementation for a remote: it removes `Authorization` and every key of its `Headers()` and sets them again only where the host may have them | One place; the caller never puts a credential on a request; a secret that rotates is read per request; the hop number needs no state | The caller must not set `Authorization` itself (the policy removes it) |
| (c) One client per host | A client per origin, credentials baked in | No policy object | A redirect has to be followed by the engine itself, and the limit, the scheme rule and the egress rules of the client no longer apply to the chain |

The client certificate is a connection property, not a header. A policy that also implements `ClientCertPolicy` scopes it: the round tripper puts the decision for the hop in the request context, and the client certificate callback of the TLS configuration (`GetClientCertificate`, which sees the handshake context) presents none where it is false. A connection is pooled per host and the decision depends on the host and the policy only, so a pooled connection never carries a decision of another hop. The first request to a host that is neither the origin nor a `sendCredentials` host never carries credentials, also with `forwardAuthOnRedirect`; the flag widens redirect hops only. Origins compare scheme, lower-case host and effective port; `sendCredentials` hosts match by `path.Match` on the lower-case host name, ignoring the port.

**Choice:** (b).

**Rationale:** The rule has to hold for the first request, for each redirect and for the TLS handshake, and only the round tripper sees all three. The redirect limit, the `https` to `http` refusal and the egress check stay where they were, in `CheckRedirect` and the dialer. Revisit if a format needs a credential that depends on the response of a previous hop (a token handshake): that belongs to the format plug-in's own requests, each of which passes through `Decorate` as hop 0.

**Applied in the spec:** R-REPO-039 states that the client certificate follows the same rule as the credentials and that `forwardAuthOnRedirect` widens redirect hops only.

### Q-ARCH-200 · The engine takes a format through its own Adapter and the populate check through an interface

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The proxy is a domain package and cannot import the format contract (an edge package), the plan sketched `MayFill bool` computed by the host, and the refusal at step 9 has to be audited while the stale path must not audit.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `proxy.Adapter` and `proxy.Populate` declared in the proxy; `internal/formathost/remote.go` adapts a `formats.RemoteAdapter` per request and the decision point to `Populate` (chosen) | `Populate` has `May` (a question), `Deny` (the audited refusal), `Record` (the allowed event in the fill transaction) and `Endpoint` | The engine decides when to ask, which `MayFill` could not; the layers hold; the engine is testable with a counting adapter | Two similar interfaces |
| (b) `MayFill bool` computed by the host | A boolean per request | Simple | Asks the decision point on every hit; cannot audit the refusal only when nothing is served |
| (c) The proxy imports the contract | No adapter | No bridge | An edge package imported by the domain |

**Choice:** (a). Three additions to the contract follow: `formats.Request.CacheView` (set by the dispatcher from the `~cache` address), `Descriptor.VerifyAnnouncedDigests` (npm always verifies, R-REPO-041) and `formats.CacheStatus.Header` and `Collapsed`.

**Rationale:** The development agent would revisit it when the second format needs an adapter method the bridge cannot derive.

**Applied in the spec:** R-ARCH-008 and R-ARCH-009 (the remote engine as the format sees it).

### Q-ARCH-201 · An error carries response headers, so a refusal of the engine carries its Cache-Status

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-047 wants `Cache-Status` on every response of a remote repository; an error is rendered by the format's `WriteError`, which has no handle on the engine's status.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `errs.WithHeader(name, value)`; `errs.Write` sets them after the headers of the kind (chosen) | A negative entry is a Document with `Status.Negative`, every other refusal an error with the header | One mechanism for any package that needs a header on an answer | A small addition to the error model |
| (b) The format reads the status from the context | A header sink in the request context | No change in `errs` | A hidden channel; every format must remember it |
| (c) Leave errors without the header | Document the gap | None | Violates the requirement |

**Choice:** (a). The headers `Content-Type` and `X-Content-Type-Options` cannot be replaced.

**Rationale:** The development agent would revisit it if a second header needs a rule.

**Applied in the spec:** R-ARCH-023 (the error model) and R-REPO-047.

### Q-ARCH-202 · The engine is a wiring area of its own and a component that drains and quiesces

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The plan sketched `StartDrain` and `CancelBefore(deadline)`; the composition root already has the optional phases `Drainer` and `Quiescer` (R-ARCH-024), and the engine needs the upstream clients, the invalidation set, the item store and the replica count.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Area `proxy-engine` with those four inputs; the engine implements `Drain` and `Quiesce` (chosen) | `Drain` stops cross-replica leadership; `Quiesce` cancels the running flights, which release with `failed`, and waits for them | The shutdown sequence calls it like every other component; a missing input is a start-up error | The names differ from the plan |
| (b) Build the engine in the proxy area | One area | One file less | The proxy area is built before the item store exists |
| (c) Lazy accessors | Resolve inputs when a request needs them | No ordering | A missing prerequisite becomes a nil at request time |

**Choice:** (a).

**Rationale:** The development agent would revisit it if the engine's inputs change.

**Applied in the spec:** R-ARCH-024 (the optional phases name the engine) and R-REPO-026 (the shutdown bullet).

### Q-ARCH-203 · A fill commits through the blob commit and the item commit, and the entry is written in their transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-025 and R-ARCH-013 fix the order spool, blob, one short transaction, release; R-ARCH-019 forbids a transaction across a transfer; R-REPO-041 refuses a different digest for an artifact.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A document goes through `blob.Commit` with a link function that locks the entry, runs `OnFill`, upserts the entry, records the URL map, signals and writes the populate event; an artifact goes through `items.Commit` with `Extra` that locks the entry, refuses another digest, inserts it and writes the populate and `artifact.cached` events (chosen) | Validation (`PrepareFill`) runs before, outside any transaction | Entry, rows of the format and events commit or fail together; the unique row lock serializes duplicate flights | A refused digest rolls back an item row that the commit wrote first |
| (b) Commit the blob, then a second transaction for the entry | Two steps | Smaller transaction | A crash between them leaves a blob without an entry and an item without one |
| (c) An advisory lock per entry | Serialize flights in the database | Simple | Banned in the proxy (R-ARCH-033) and holds a connection |

**Choice:** (a). `artifact.cached` has audit mode `none` and the webhook flag (R-OPS-014: webhook only); the fill transaction hands it to the recorder, which records nothing for that mode until the outbox exists.

**Rationale:** The development agent would revisit it when the outbox of R-OPS-035 is built.

**Applied in the spec:** R-REPO-025 (the data model) and R-OPS-016.

### Q-ARCH-204 · The proxy's security events: one transactional, four asynchronous and throttled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-REPO-047 lists `proxy.upstream_gone`, `proxy.upstream_digest_changed`, `proxy.integrity_mismatch`, `proxy.tls_untrusted` and `proxy.egress_denied` and does not say how they are written; four of them concern a request that stores nothing.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `proxy.upstream_gone` in the transaction that marks the document; the others through the asynchronous buffer, `tls_untrusted` and `egress_denied` at most once a minute per repository and host, `upstream_digest_changed` at most once an hour per path and announced digest (chosen) | Category `repository` | The mark and its event commit together; a broken upstream does not flood the trail | A security event can be dropped when the buffer is full (the log copy remains, R-OPS-015) |
| (b) All transactional | A transaction for each | Never dropped | There is no transaction to join where nothing is stored |
| (c) No throttle | One event per request | Complete | A failing upstream writes one per request |

**Choice:** (a).

**Rationale:** The development agent would revisit it when an alert rule needs the count of requests rather than of episodes.

**Applied in the spec:** R-OPS-016 (the event table).

### Q-ARCH-205 · One edge package for the conversions every HTTP entry point shares

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `v1.SubjectFrom` is the one place that builds an `authz.Subject`, and the raw `/files/` routes import the management API
for it, for `CallerFrom` and for the JSON helpers. The format dispatcher will need the same conversion and the holder of a request's
index (Q-AUTHZ-159).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/api/edge`: conversions, the access holder, later content responses | One package that `rawfiles`, the dispatcher and `v1` import and that imports none of them | No entry point imports another; the one place that builds a subject has a package of its own, which the architecture test names | A package to keep small |
| (b) Two packages, conversions and content | Split by kind | Smaller units | More imports, no gain |
| (c) Keep them in `httpserver` | The shell holds them | No new package | `httpserver` would import `authz` and `audit` types it does not own |

**Choice:** (a). `edge.SubjectFrom(r)` and `edge.SubjectOf(ctx)` build the subject from the caller and the listener's endpoint,
`edge.ActorFrom` the audit actor, and `edge.Access` (`Begin`, `WithAccess`, `AccessFrom`, `Resolve`) holds the subject, the index
and the decider of a request. `v1.SubjectFrom(ctx)` and `v1.ActorFrom` delegate.

**Rationale:** the packages that serve content must not depend on the management API, and the conversion that carries the
exposure ceiling must exist once. The development agent would revisit this if the package starts to hold anything that is not shared
by at least two entry points.

**Applied in the spec:** R-ARCH-002 (component table, layer list) and R-ARCH-003 (the tree).

### Q-ARCH-206 · The files transfer is three packages: the item store, the generic layer and the raw routes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `internal/files` held the authorization of every operation, the repository resolution, the admission of the path, the spool, the
commit and link protocol, the listing, the subtree delete, the delete job and the statistics. The proxy commits the artifact of a
remote fill as an item of the remote repository (R-REPO-025) and a format's publish commits an upload (R-ARCH-009 `Host.Ingest()`);
both need the storage half without the decision half, and the format's storage hooks (R-ARCH-009) must run in the transaction of both.
The decision half in turn must reach the formats' serve check later (R-EP-014) without importing the registry.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep one package and add the proxy's and the formats' entry points | `files` gains a commit for fills and the hooks | No move | The proxy would import a package that decides and imports authz; the "no authz in the storage path" rule cannot be enforced by the layers |
| (b) `internal/items` (storage, no decision) and `internal/files` (the generic layer, decisions and admission) over it, both domain, with the format seam injected (chosen) | `items` imports no authz (architecture test); `files` imports neither `blob` nor `meta/postgres` (architecture test) and reaches storage only through `items`; the hooks arrive as an `items.Hooks` value the composition root injects | The proxy and `Host.Ingest()` import `items` only; both rules are held by the import test, not by review; `files` is testable with a fake store | One more package; the callbacks (`Check`, `Extra`) that carry the generic layer's decisions into the store's transactions |
| (c) Move `files` to the edge so that it imports `formats` directly | The generic layer is an edge package | No injection for the serve check | The management API and the jobs would import an edge package for logic; the delete job's hooks would still need injection into `items` |

**Choice:** (b). `internal/items` is the item store behind `Host.Ingest()` and `Host.Items()`; `internal/files` is the generic layer (decide on the
request's index, resolve the repository, admit the path, the overwrite pre-check, the per-entry filter of a listing, `RecordAllowed` inside the
store's transaction); `internal/api/rawfiles` is the `/files/` routes and imports neither `internal/api/v1` nor a store. Three import rules hold the
cut (`moduleBans` in `internal/archtest`): `internal/items` does not import `internal/authz`, `internal/files` does not import `internal/blob` or
`internal/meta/postgres`, `internal/api/rawfiles` and `internal/api/edge` do not import `internal/api/v1`. The format seam of the generic layer
(`Classify`, `ServeCheck`, R-EP-014) is added to `files` as an injected interface when the format contract lands; the storage hooks are
`items.Hooks`, already injected.

**Rationale:** the rule "a commit is decided before it is called" is only worth something if the commit cannot call the decision point. The
development agent would revisit this if a second consumer needs the generic layer's decisions without its HTTP shape, or if the number of callbacks
the store takes grows past what a reader can hold in mind.

**Applied in the spec:** R-ARCH-003 (the tree); Q-ARCH-43 is superseded.

### Q-ARCH-207 · One commit in the item store: the item row, then the committer's write, then the format's hook, then the audit row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: three committers write an item. A client upload through the generic layer records the allowed write (R-AUTHZ-029) in the commit
transaction. A format's publish commits the upload and runs its own hook. The proxy stores a remote artifact and upserts the cache entry that
names the new item's id. All three need the same link protocol (ancestor folders, the row lock, the overwrite gate) and the same transaction
for the committer's own write, the format's hook and the audit row, so that a failing step leaves no item (R-STOR-005).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One commit function per committer | One for uploads, one for fills | Each reads simply | Three copies of the link protocol, or three wrappers of one private function |
| (b) One `Commit(CommitSpec)` with `Origin`, `UpstreamURL` and `Extra` (chosen) | Inside the one transaction: the item row (and the ancestor folders), `Extra(ctx, tx, itemRef)`, `Hooks.OnCommit(CommitEvent)`, the audit row | One protocol; `Extra` can reference the new item's id; the order is a property of one function and is tested | `Extra` is a callback that must be safe to run again, because the transaction may restart |
| (c) The committer opens the transaction and passes it | `Commit(ctx, tx, spec)` | No callback | The blob store's commit protocol owns the transaction (it holds the blob row's lock); a second owner would break R-STOR-005 |

**Choice:** (b). `items.Store.Commit(ctx, items.CommitSpec{Repo, Path, Spooled, MediaType, Declared, Origin, UpstreamURL, CreateOnly, MayOverwrite, Actor,
Source, Endpoint, Payload, Extra, BeginCommit})` returns `CommitResult{Created, Overwritten, Item}`. `Origin` is `OriginUpload` or `OriginFill`;
`items.CommitEvent` and `items.RemovedEvent` are the store's own types, which the format host turns into the contract's. An identical commit
changes no row: `Extra` runs, the hook and the audit row do not. The caller settles `MayOverwrite` before the call and outside any transaction
(Q-STOR-144); a path that holds other content when the link step runs with `MayOverwrite` false is `items.ErrOverwriteDenied` and nothing is
written. The delete path takes the same shape: `DeleteSpec.Check` (asked of every path of the subtree before anything is removed) and
`DeleteSpec.Extra` (in the removing transaction), and `Hooks.OnItemsRemoved` once per statement that removes files.

**Rationale:** one function with one order is the only way the proxy's cache entry, a format's package rows and the audit row can all be
rolled back with the item. The development agent would revisit it if a fourth committer needs a step between the hook and the audit row.

**Applied in the spec:** R-ARCH-009 (the storage hooks run in this order; the format contract writes its side of it).

### Q-ARCH-208 · A deploy decides again on a new index after its body arrived

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-015 says a request takes its index once (Q-AUTHZ-159) and, separately, that a write is evaluated again when it commits "against
the grant index that the committing replica holds at that moment". The first would answer the re-check of a deploy on the index the request
began with, which is minutes old after a large upload and misses a grant removed while the body was on the wire; the integration test
`TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits` removes the grant mid-body and expects the refusal.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The re-check uses the request's holder | One index for the request | The statement "one index per request" is literal | The commit-time re-check is then no re-check at all |
| (b) The generic layer asks the entry point for a new holder after the body arrived (chosen) | `files.Caller.Recheck` begins a new `edge.Access` on the current index; the checks that follow the body (the existing-item check, the commit-time decision, the overwrite question, the recorded decision) use it | The re-check sees what the committing replica knows now; the decisions before the body stay on one index | A write has two indexes, by design |
| (c) Decide inside the commit transaction | Re-check as late as possible | Latest possible view | A stale index makes a decision wait while the transaction holds the blob row and the item (R-ARCH-019) |

**Choice:** (b). `Recheck` is set by the entry point (`rawfiles`, the integration environment); a caller without it re-checks on its own holder, which is
right for a caller whose request is short. Only `Deploy` re-begins; reads and deletes decide once.

**Rationale:** R-AUTHZ-015 asks for both, and they do not contradict: the consistency of one index is for the decisions that shape one answer, the
re-check is for the window between the first decision and the commit. The development agent would revisit it if a second write operation reads a
long body.

**Applied in the spec:** R-AUTHZ-015 reads unchanged.

### Q-ARCH-209 · What typed wiring makes a start-up error

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the composition root grew its steps as nil-tolerant functions that return without building anything when a part they need is
absent. A `web` replica whose wiring misses the decision point or the object store therefore starts, turns ready and answers `503` on
`/files/`, and the shell of the unit tests relies on exactly that nil check. Format engines add more areas, each with prerequisites
(a data port for a format that stores rows, the proxy's flight lock), and a wrong binding must not be found by the first request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Areas are data of the role; a step takes the typed results of earlier steps; a missing input stops the start-up; the shell is an explicit profile | `SubsystemsFor` lists the areas, `planSteps` refuses a role whose steps cannot be satisfied, a step refuses a nil input by name | A misconfigured replica never becomes ready; the engines' bindings fail at start-up; the unit-test shell stays, as a named profile | A step that used to tolerate a part now needs it; the shell's tests name their profile |
| (b) Keep the nil-tolerant steps and add a readiness check per area | The replica starts and reports the area as not ready | No change to the steps | The replica stays in the Service only if it is ready, but `503` on one route is a half-working replica; the check is another place that has to know the area's prerequisites |
| (c) Both: tolerate at start, refuse at the first request | As today with a log line | None | The failure moves to the user |

**Choice:** (a). `Subsystems.Areas` is the set a role enables, `Subsystems.Shell` marks the shell profile, and `planSteps` runs in `New`,
so a role that cannot satisfy its steps never starts. Each step checks its inputs with `requireInputs`, which names every nil one
(`files: no authorizer`). A part that is only unreachable stays a retry (steps 3 to 7 of R-ARCH-024); a part the wiring does not provide
is refused. Q-ARCH-230 applies it to the four places that kept a convention.

**Rationale:** the replica that serves `503` on `/files/` is today's failure mode (the plan of WP2.0-4 names it), and the same shape would
hide a missing data-port binding of a format. The development agent would revisit this if a role needs a part to be optional on purpose;
that part then becomes an area of its own that the role enables or not, not a nil check inside a step.

**Applied in the spec:** R-ARCH-001 (the areas of a role and the shell profile) and R-ARCH-024 (step 8).

### Q-ARCH-210 · How the step list binds types

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a step takes the results of earlier steps and returns its own. The list must tell what a step needs without running it, so that
the role check can run in `New`, and adding an area has to be one file and one line.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Generic constructors by arity (`wire1`, `wire2`, ...) | The compiler checks every input type | Compile-time typing | A constructor per arity, so an area with one more input edits `steps.go`; unused arities fail the linter |
| (b) Read the types off the method with reflection (`newStep(area, (*App).wireX)`) | The method's signature is the declaration; the shape is checked when the list is built | One line per area, any number of inputs; the method is ordinary typed Go | The call is not checked by the compiler; a wrong shape panics at construction, which every test of the package hits |
| (c) A struct of all areas that every step reads from | Plain field access | No reflection | Every step sees every area; the dependency order is implicit again |

**Choice:** (b). `newStep` accepts `func(*App, context.Context, In...) (Out, error)`, records the input and output types and panics on any
other shape. `planSteps` proves, before anything starts, that every input is built by a step of an enabled area listed before the step,
that each area has one step and each type one builder. Job kinds are registered before the areas are wired, so a handler reads its
service through `late[T]`, set once by the step: a job claimed before it is retried, in one place instead of one nil check per kind.
`late[T]` is for job handlers; the one reader at request time is the Host that the files seam reads (Q-ARCH-231).

**Rationale:** the list is short and read by people adding an area, so one line per area and no edit of the framework matters more than
a compile-time check of a call that the shape check and the tests cover. The development agent would revisit this if the list grows past
a dozen areas or a step needs a conditional input; a typed builder would then pay for itself.

The step list also says which roles run a step, so the areas of a role are not a second table to keep in step with it: adding an
area is its file and one line in the list.

**Applied in the spec:** R-ARCH-024 (step 8) names the list; the mechanism is described in docs/developer/architecture.md.

### Q-ARCH-211 · The contract's names are made unambiguous, and `Match` stays one line in each format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the sketch of R-ARCH-008 spells `Delete` as a route kind and as an action, and `Metadata`, `Computed` and `Volatile` as route kinds and
as path kinds. It cannot compile as written. It also gives `Base` a `Match` over `Routes()`, which an embedded struct cannot do: it does not see
the type that embeds it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Prefix where two meanings meet | Route kinds keep the names the spec pages use (`Download`, `Metadata`, `Computed`, `Upload`, `Delete`, `Admin`, `Volatile`); actions are `ActionRead` to `ActionManage`; path kinds are `Content`, `PathMetadata`, `PathComputed`, `PathVolatile`; a format writes `Match` as one call of `MatchRoutes` | The format pages and the kit checks keep their words (kind `Download`, kind `Delete`) | Two spellings of "metadata" |
| (b) Sub-namespaces | `formats.Routes.Download`, `formats.Paths.Content` as struct values | No prefixes | Not constants: no `switch` over them without a helper |
| (c) Rename the route kinds | `KindDownload` and so on | One flat set | Every format page and R-ARCH-007 name kinds `Download`, `Metadata`, `Computed`, `Upload`, `Delete`, `Admin`, `Volatile` |

**Choice:** (a). `Base` offers the hooks, the nil adapters, `ServeCheck`, `SetupTemplates` and the `problem+json` `WriteError`; `Descriptor`, `Routes`, `Match`,
`Classify` and `PackageDocumentPath` stay the format's own.

**Rationale:** the route kinds are normative words on four pages and the action constants are a convenience for a format's code, so the convenience gives
way. The development agent would revisit this if a third meeting of two meanings appears.

**Applied in the spec:** R-ARCH-008 sketch.

### Q-ARCH-212 · A storage hook holds the opaque transaction, a package writer and the data port; the writer raises the revision

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a hook must create package and version rows, lock the package, link items and, for npm, write side rows through its port, and every one of
those changes must raise `packages.rev_seq` (R-ARCH-009). R-ARCH-008 forbids SQL in a format, and `meta.Tx` cannot run SQL by its own rule. The
format's port (Q-ARCH-193) takes the transaction per call.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Tx` plus a `PackageWriter` and the port in the event (chosen) | The hook signatures of the sketch stay; `PackageWriter` is bound to the hook's transaction and raises `rev_seq` once per package per transaction on every call that writes generic rows; `BumpRevision` covers a change of side rows only | The most common mistake, a generic-row write without the revision, cannot be made; the kit measures the rest (check 9); one port shape per format | The writer and the port are two places per format |
| (b) A writer only, with tx-bound copies of the port | `OnCommit(ctx, c)` and ports bound per transaction | One handle | Two port shapes per format, one for reads and one bound to a transaction |
| (c) `Tx` only | The sketch as it was | Smallest contract | A hook could not write the generic rows without SQL |

**Choice:** (a). The writer is `internal/packages` (a domain package over `packages`, `package_versions`, `items.version_id` and `item_properties`, with
sqlc statements only); `internal/formathost` adapts its types. The raise is recorded in a slot of `meta.Tx.Value`, which belongs to one attempt of the
transaction: a retry raises again, a rollback raises nothing. A package that a call creates starts at revision 1, and that counts as the
transaction's raise. `SetProperties` raises the revision of the package the item belongs to. The writer of a fill or a job has no request, so
`Claim` and `AutoClaim` refuse on it (Q-ARCH-215).

**Rationale:** the revision is what clients hold as an ETag and what the merged-metadata cache compares; making the writer raise it turns a
silent staleness into something no format can do. The development agent would revisit this if a format needs two raises of one package in one
transaction, which no ETag scheme wants.

**Applied in the spec:** R-ARCH-008 sketch, R-ARCH-009 sketch and its revision rule, storage.md hooks table.

### Q-ARCH-213 · `ContractVersion` stays 1 until the freeze

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-025 says the version changes when a method changes meaning. E3.3 names 1 as the frozen version of the contract at the end of M3.
Until then the contract changes with every format that lands, and at most two formats exist.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stay at 1, every change touches all formats in its change (chosen) | A format declares the literal; `Register` panics on a mismatch | The frozen version is 1, as E3.3 says; the mismatch check still works for a stale build of a format | A format outside the tree would not notice a change; there is none |
| (b) Bump per change | 1, 2, 3 before the freeze | Each change is visible | The freeze would not be 1 |

**Choice:** (a).

**Rationale:** no format is built outside the tree before the freeze, so the number guards nothing yet and costs a rename of the literal in every
format on every change. The development agent would revisit this if a format is built outside this repository.

**Applied in the spec:** R-ARCH-010.

### Q-ARCH-214 · The refusals of the chain's stages on a package path are rendered by the format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-NPM-005 asks for npm's body and challenge on every error response of an npm endpoint. The `401`, `421`, `503 server_busy` and `429` of the chain are
written by stages that run before the dispatcher and know no format.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An error-shape table by path prefix (chosen) | `Router.HandleErrorShape("/api/<id>/", f.WriteError)`; `httpserver.Problem` finds the table through the context the first stage sets | Every stage renders through `Problem` already; the order of the chain stays; one registration per format | A format's `WriteError` must not call `Problem` |
| (b) problem+json from the stages | As before | No change | Violates R-NPM-005; a package client cannot parse it |
| (c) Authentication behind the router | The dispatcher authenticates | The format sees every refusal | Breaks the order of R-ARCH-005 and the `401`-before-the-body rule |

**Choice:** (a). The table belongs to the router, so an endpoint whose surface does not hold the `packages` family drops the shape with its patterns.

**Rationale:** the stages already agree on one function for errors; the only missing fact is which protocol a path speaks, and the path says it. The
development agent would revisit this if a family other than `/api/<id>/` needs a shape (the OCI reservation `/v2/` will).

**Applied in the spec:** R-ARCH-005 (error shape of a format prefix).

### Q-ARCH-215 · Package ownership and the namespace auto claim are seams that refuse until they are bound

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a publish checks and records ownership in its commit transaction (R-AUTHZ-018, R-AUTHZ-015) and applies the auto claim of R-REPO-023. Both
rules are written in a work package of their own that has not landed. The contract needs the call now, because the kit and the first format are
written against it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A seam in `internal/formathost` that refuses until bound (chosen) | `Ownership.EnforceTx(ctx, tx, OwnershipCheck{Index, Subject, Coordinate, Write})` and `AutoClaimer.AutoClaimTx`, bound by the composition root; unbound, `Claim` and `AutoClaim` answer an internal error | A publish that cannot be checked is not allowed; no format asks before the rule exists | A format that claims cannot publish until the binding |
| (b) Unbound means allowed | The seam answers nil | npm could publish before the rule lands | A forgotten binding is an open door |
| (c) Wait for the ownership package | The contract waits | No stand-in | Blocks the kit and the first format |

**Choice:** (a). `OwnershipWrite{Publish, FirstVersion}` says what kind of write it is: a change of a dist-tag is not a publish. A writer without a request
(a fill, a job) refuses always.

**Rationale:** the failure that matters is a package taken over by a caller who should not own it; refusing costs a visible error in a test that
has no binding, allowing costs a hole nobody sees. The development agent would revisit this when the ownership package lands: the binding replaces the refusal.

**Applied in the spec:** R-ARCH-009 sketch (`Claim`, `AutoClaim`).

### Q-ARCH-216 · The conformance kit imports what the contract imports

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: check 7 of R-ARCH-010 asserts the content headers of a route's response, and R-ARCH-007 says the core, not the handler, sets them.
The kit's in-memory `Host` also holds a subject, a request and an opaque transaction. The archtest allowlist of a format package refuses
`internal/authz`, `internal/repository`, `internal/meta` and `internal/httpserver/content`; the contract package imports them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The kit may import what the contract imports (chosen) | `formattest` runs the real `formats.ServeRoute` and reads the expected headers from `content.Headers`; it builds a `formats.Request` with a `repository.Repository` | The kit tests the code that runs in production; a header set changed in one place is changed for the kit as well | The kit sees core types a format does not; its allowlist is the contract's |
| (b) The kit stays on the format allowlist | It re-implements the header wrapper and the request | A narrower kit | A mirror of production code: the kit would pass while production fails, which is the failure the check exists to catch |
| (c) The kit imports the production host | It runs against `internal/formathost` over the stores | Nothing mirrored | Needs PostgreSQL and the object store; `go test ./internal/formats/...` could no longer run alone (R-ARCH-009 acceptance) |

**Choice:** (a). The allowlist test (`internal/archtest`, `TestFormatAllowlistSplit` and the kit cases of `TestFormatImportAllowlist`) names an
edge from the kit to `internal/formathost`, to the dispatcher, to storage, to the item store, to `internal/packages` or to a concrete format.
The kit's subpackages `crafted` (standard library only) and `testformat` (the contract, the kit and `untrusted`) sit in the same tree.

**Rationale:** the check is only worth something when the thing it asserts about is the thing production runs. The in-memory `Host` is the
one place that mirrors production (the package writer's revision rule), and it is tested against the writer's documented rules in its own
tests. The development agent would revisit this when the contract package splits the header wrapper out of `ServeRoute`, which would let the
kit import the wrapper alone.

**Applied in the spec:** R-ARCH-010 (the kit's shape), the repository tree.

### Q-ARCH-217 · The bounds R-ARCH-028 leaves without a number

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-028 demands depth limits for JSON and XML and a bounded pass over archives that are only counted, and gives numbers for
entries, bytes, ratio, manifest entry and deadline. The readers need a number for each of the others; a format may only tighten them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) JSON depth 64, 2,000,000 values, XML depth 64, 2 GiB for a pass that keeps nothing (chosen) | Four defaults in `untrusted.DefaultLimits`; `Tighten` lowers them | the package documents of npm and NuGet nest far below 64 levels, and a document of 20 MB is estimated (not measured) to hold under one million values; 2 GiB is the bound of npm's `dist.unpackedSize` pass (R-NPM-007) | A document with more values is refused until a format page argues for a higher number |
| (b) JSON depth 256, no value count | Closer to `encoding/json`'s own 10,000 | Fewer refusals | A document of a few megabytes with millions of values costs hundreds of megabytes of tree; the byte cap alone does not bound that |
| (c) Per-format bounds only | The helpers take no default | Each format fits its documents | A format that forgets one is unbounded; the failure is silent |

**Choice:** (a). A depth or value count that is crossed is `TooLarge` (413), like every other field of `Limits`; a document that does not
decode, a document type declaration and every ambiguous archive are `Unprocessable` (422). An unset field of the `Limits` a reader receives
takes the default, so a zero `Limits` is never unbounded.

**Rationale:** the byte cap bounds the text and the scan bounds the tree built from it; the two are different resources. 64 and 2,000,000
are the smallest round numbers above real documents; both are far below what makes a decoder recurse deeply or hold a gigabyte. The
development agent would revisit the value count when a format page shows a legitimate document above 2,000,000 values.

**Applied in the spec:** R-ARCH-028 (the default list and the helpers).

### Q-ARCH-218 · Node.js 26 is the one Node.js of the repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `web/.nvmrc` and the digest-pinned clients image (`E2E_CLIENTS_NODE_IMAGE`, R-NPM-045) name one Node.js version, and the
Renovate pull request for Node.js 26 moves both. The spec's npm page records Node.js 26 as the active LTS line from 2026-10-28
([npm.md](formats/npm.md), the clients table). Checked on 2026-10-30: the `engines` of npm 12.2.1 (`^22.22.2 || ^24.15.0 || >=26.0.0`),
of Angular 22.2.4 (`^22.22.3 || ^24.15.0 || >=26.0.0`) and of the other six clients accept 26.13.0; the clients image built on
`node:26.13.0-trixie-slim` runs all eight rows at their pinned versions; `perf/npm/reference` is unchanged under the npm that image
bundles (11.20.0).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stay on Node.js 24.22.2 | The pins of `web/.nvmrc` and the clients image do not move | No change; every row and the web build already run on it | The repository's one Node.js is the line before the active LTS; the Node.js 26 pull request stays open |
| (b) Node.js 26.13.0 for both pins (chosen) | `web/.nvmrc` and `E2E_CLIENTS_NODE_IMAGE` move together to 26.13.0 | One pin, the active LTS line; every consumer's `engines` accepts it, verified above; the reference lockfile's writer moves to npm 11.20.0 without a change of the lockfile | The host of a developer on 26.5.0 has to move to 26.13.0; the spec's npm decision (Q-NPM-45) names the Node.js of its time and is updated with it |
| (c) One Node.js per client row | Node 22 for npm 10, 24 for npm 11, 26 for npm 12, and so on | Each client runs on the Node it is bundled with | Three or more Node pins and images; it breaks the one-version rule for Node.js (the rejected option (b) of Q-NPM-45) |

**Choice:** (b). The repository keeps one Node.js for the web build, the clients and the reference lockfile, and that one is the active LTS line.

**Rationale:** The one-version rule decides between (b) and (c), and the engines of every consumer decide that the active line is usable without
any code change. The development agent would revisit the pin when a row fails on 26.13.0 only: then that row gets its own image and pin, as
Q-NPM-45 already states, and the one-version rule is re-decided on the page that owns the row.

**Applied in the spec:** [npm.md](formats/npm.md) (Q-NPM-45 names 26.13.0 and its verification); `web/.nvmrc` and `tools/versions.env`
hold the pins; [docs/developer/e2e.md](../developer/e2e.md) and [perf/README.md](../../perf/README.md) name the version. Node.js 26 links `libatomic.so.1`, which Node.js 24 did not; the CI jobs that set up the pinned Node.js on the self-hosted runners install `libatomic1` first, until the runner image ships it. Release Tooling and Semantic Release read the pin as well; until 2026-11-02 they asked for `lts/*`, which failed with exit 127 once the LTS line became 26.

### Q-ARCH-219 · Which PostgreSQL version does the integration tier and the dev profile run?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Renovate proposed PostgreSQL 18.6 for the integration stack (`docker.io/library/postgres`) and for the dev profile
(`ghcr.io/cloudnative-pg/postgresql`). Both pins were 16.15, the floor of R-HA-013 (Q-HA-2). Three integration tests asserted
`server_version_num` in the 16 range (`test/integration/postgres/migrate_test.go`, `env_test.go`, `test/integration/smoke/postgres_test.go`).
R-HA-013 requires every supported version (16, 17, 18) to be covered by the CI matrix, and CI covers one version today. The owner's
instruction of 2026-10-30 is to take all pending changes, and the coordinator chose option (b) on that basis. The digests
were resolved on 2026-10-30: `postgres:18.6` `sha256:979bccfd…`, `cloudnative-pg/postgresql:18.6-standard-trixie` `sha256:39add80f…`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep 16 | Both pins stay at 16.15; the floor is what CI runs | The floor is what CI runs against a real server; no test changes | 18 is supported (R-HA-013) and no CI run uses it; the Renovate pull requests stay open |
| (b) Move to 18 (chosen) | The integration stack and the dev profile run 18.6; the three assertions name 18; the floor is no longer run against a real server | One version in every environment (the one-version rule); the newest supported line is what CI exercises; `PGDATA` moves to the tmpfs path the stack already uses | 16 and 17 have no real-server run in CI until the matrix exists; the refusal below 16 has no test |
| (c) A 16 and an 18 leg | Two integration legs, one per version, both in CI | Covers every supported version, as R-HA-013 requires | Two PostgreSQL versions in use, against the one-version rule, unless the owner grants a matrix exception; a second stack in the compose file |

**Choice:** (b). The stack, the dev profile and the assertions move to 18.6 in one change.

**Rationale:** The one-version rule is binding: when two versions of a dependency are in use, the newer one goes everywhere, and
(c) would put 16 and 18 in use at once. The owner's instruction names the same direction. The cost is real and is recorded on
R-HA-013 ("Version under test"): the floor is no longer exercised by a real server, and the refusal below 160000 has no test.
The development agent would revisit the choice when one of these happens: the owner grants an exception that allows a matrix, so (c)
is built with a 16 leg and the refusal test; a feature of the spec needs a PostgreSQL version that the stack does not run; or
PostgreSQL 18 leaves support (2030-11-14, as R-HA-013 records).

**Applied in the spec:** R-HA-013 (the "Version under test" bullet names the gap); `test/integration/compose.yaml` (the header and
`PGDATA`); `tools/versions.env` (`INTEGRATION_POSTGRES_IMAGE`, `DEV_CNPG_POSTGRES_IMAGE`); the three assertions; the pages of
[developer](../developer/build-and-test.md) and [local profile](../developer/local-profile.md) name 18.

### Q-ARCH-220 · P-13 starts its clock at the commit, and its p99 is enforced in the nightly performance run

Status: **Decided by the owner on 2026-10-30** — option (c), after the development agent's own choice (b) failed in CI; the owner keeps the CPU limit of the runner pods.

Context: `TestChangePropagation` failed on the self-hosted runners with `n=200 p50=2.762ms p99=112.515ms` (Actions run 41061536927). The
test started its clock before the writer's transaction, so each sample held the writer's two statements and the `COMMIT`. Stamping
each stage (integration stack, host load average 15 to 40) showed the transaction at p50 3.0 ms and p99 9 to 120 ms, and the leg
P-13 names (from the return of `COMMIT` until the eviction on the other replica) at p50 1.2 to 1.3 ms and p99 3.5 to 33 ms, with
no window, batch or timer in it: change messages bypass the coalescer, the queue wait before `PUBLISH` is 30 to 180 µs, the delivery
from the subscriber's reader to the cache 3 to 30 µs, and `MaxFlushDelay` of the client is 0. With 24 CPU burners on 20 cores
the leg showed p50 4 to 9 ms and single `PUBLISH` round trips of 20 to 80 ms with every other stage unchanged. Option (b) below
(clock at the commit, p99 bound kept per change) shipped and failed again on the runners: propagation with Valkey `n=200 p50=1.532ms
p99=122.489ms` (equal to the maximum), pick-up with Valkey p99 97.6 ms in the same run, an earlier run p99 112.5 ms. Tails of 100 to
120 ms fit the throttling period of the Linux CFS bandwidth controller (100 ms) under the CPU limit of the runner pods, not a delay
in the message path. The owner keeps that limit because the cluster is shared.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the clock before the transaction | The sample holds the writer's transaction | Simple | It bounds PostgreSQL commit latency, which P-1 and the database budget bound, not P-13 |
| (b) Clock at the return of `COMMIT`; the p99 bound stays per change | An After hook stamps the commit | Measures the leg P-13 states | Failed in CI again: a throttled runner stalls the process for a period of 100 ms and the p99 of 200 samples is the second largest |
| (c) Per change: delivery and p50; the p99 bound in a nightly performance run (chosen) | The integration test asserts that every change reaches replica B and that the p50 stays within 20 ms; the p99 bound of 100 ms is enforced when `TROVE_PERF_GATE=1`, which the workflow Performance (nightly) sets | The per-change tier stays free of a tail that the runner's CPU limit makes; the bound is enforced exactly as before, where it is a statement about the system; the p50 and delivery still fail a message path that is lost, polled or slow | A regression of the tail alone shows up the next night, not on the change |
| (d) Remove the CPU limit from the runner pods | The p99 stays per change | Strictest | The cluster is shared; the owner keeps the limit |

**Choice:** (c). `TestChangePropagation` (P-13 with Valkey) and `TestJobPickup` (P-14 with Valkey) always log p50, p99 and the maximum.
Per change they fail when a change does not reach replica B within 10 s, when the p50 of the propagation exceeds 20 ms or the p50 of the
pick-up exceeds 100 ms. The p99 bounds (100 ms and 1 s) fail the test only with `TROVE_PERF_GATE=1`; without it a p99 above the bound is
logged. The bounds without Valkey (`epoch_poll_interval` + 1 s, `jobs.poll_interval` + 1 s) are not tail-sensitive and stay per change.

**Rationale:** The numbers come from the measurements above. With the poll at 2 s (propagation) and 5 s (pick-up) a p50 of a few
milliseconds can only be explained by the message, so the p50 bound keeps the per-change test able to notice a lost or late message. 20 ms
is 15 times the quiet p50 of 1.3 ms and more than twice the highest p50 seen at host load 30 and with 24 CPU burners (9 ms); 100 ms for the
pick-up is 20 times its p50 of 5 ms. The bounds P-13 and P-14 state are unchanged and are enforced in the nightly job
(`.github/workflows/performance.yml`, `make test-performance`). The development agent would revisit the choice when the runners
that run the nightly job have no CPU limit (dedicated performance runners): then the p99 returns to every change, as in (b).

**Applied in the spec:** the P-13 and P-14 rows of R-ARCH-027; [docs/developer/coordination.md](../developer/coordination.md) ("Bounds P-13
and P-14"); [docs/developer/releasing.md](../developer/releasing.md) ("Performance (nightly)"); [docs/developer/build-and-test.md](../developer/build-and-test.md).
Applied on 2026-10-31 to the same bounds on kind: E0.6 (`TestE06Lease` in [e06_lease_test.go](../../test/e2e/e06_lease_test.go)) asserts delivery and the
p50 bounds per change and the p99 bounds with `TROVE_PERF_GATE=1`, after a run on throttled runner pods measured P-14 p99 = 1395 ms with n = 24.
Applied on 2026-10-31 to two targets of the per-change integration tier that the runners missed by a few percent (Actions run 41218966825):
R-NPM-010's 10 ms for a cached document of 1,000 versions (measured 14 ms; per change a hit must stay below half a miss) and R-API-014's 1.5 s
for a concurrent duplicate (measured up to 1.59 s; per change at most 2.5 s, while the first request holds its row until the test ends). Both
targets are enforced with `TROVE_PERF_GATE=1` by `make test-performance`, which runs them nightly.

### Q-ARCH-226 · The host answers an early, read-only ownership question

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: C-14 lets a format ask the ownership rule only through `PackageWriter.Claim`, inside the commit transaction. A publish therefore read, spooled and validated a body before the rule could refuse it (S-4 of the security review).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Host.CheckOwnership(ctx, rq, target, write)`: the same rule on the same facts in a transaction of its own that locks and writes nothing; the commit decides again | A format refuses before the body | The rule stays one function (`authz.Ownership.PrecheckWrite`); the contract gains one method | A method on the contract; a second read of the claim rows |
| (b) A format calls `Host.Write` with a function that claims and rolls back | No contract change | A write transaction and a row lock for a question | A rolled-back insert per first publish, and a lock a flood can queue on |
| (c) Move the rule before the body in the dispatcher | Every format is covered | One place | The dispatcher does not know the write a route is, nor whether it is the first version |

**Choice:** (a). `formats.Host.CheckOwnership`, `formathost.Ownership.Precheck` and `authz.Ownership.PrecheckWrite` carry it; a refusal is counted and audited like the commit's. A host with no ownership rule bound refuses, as `Claim` does. The in-memory host of `formattest` has an option `Precheck` for it.

**Rationale:** the early answer cannot be final, since a claim is decided under the claim row lock, so it takes no lock and claims nothing. The development agent would revisit the choice when a second format writes: the dispatcher could then ask it for routes that declare their write.

**Applied in the spec:** R-ARCH-009 (the `Host` listing); [formats/npm.md](formats/npm.md) R-NPM-007 step 1 (Q-NPM-56).

### Q-ARCH-227 · The reseal walk keeps its cursor and counts per attempt

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `ResealSecrets` advanced its cursor and its counters inside the closure of the batch transaction. `InTx` runs the closure again after a serialization failure or a deadlock, and the second attempt started after the last row the failed attempt had read, so the batch's values stayed under the old key, and the counts included rows that were rolled back. The task reported success with an inflated count, and only the status check of rotation step 4 would have shown the values left behind.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The attempt works on copies of the cursor and the counts, which are taken over after `InTx` returned | A retried batch starts where the batch started | Exact counts; no new state | Two more variables |
| (b) Re-read the cursor from the database | No in-memory state | Needs no copy | A column for the cursor; the walk is stateless by design |

**Choice:** (a). `TestRemoteSecrets` of `test/integration/repository` runs the walk through a runner that rolls back every first attempt and checks the counts and the rows.

**Rationale:** the rule is that nothing outside a transaction closure changes inside it. The development agent would revisit the choice if another walk of the sealed-column registry needs the same discipline: a helper then holds it.

**Applied in the spec:** R-ARCH-021 (rotation step 3).

### Q-ARCH-223 · `/files/` deploys only into a format whose protocol it is

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the generic layer had no say of the format for a folder, and npm's `OnCommit` refused files only after their body was
spooled. `PUT /files/npm-local/@acme/ui/-/ui-2.0.0.tgz/` created a folder at the path of a tarball the owner had not published
yet, so the owner's publish of 2.0.0 failed with a path conflict, repeatably and without any ownership question
([formats/npm.md](formats/npm.md) Q-NPM-24, R-AUTHZ-018).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse every deploy, file and folder, into a format whose protocol is not `/files/`, before the body | `files.Formats.RawDeploy` answers from the format's `ProtocolBasePath`: `/files/{key}/` (the generic format) takes deploys, every other format is `409 conflict` naming its protocol address, after the decision and before the body is read | Nothing reaches an npm repository beside its publish; no body is spooled and no `100 Continue` sent for a refusal; one rule from a fact the formats already declare | R-NPM-036 (P1) will need a descriptor fact of its own |
| (b) A folder hook in the format contract | `OnFolder` for every format | The format decides per path | A contract change for a refusal that has no exception today; files still spooled before `OnCommit` refuses them |
| (c) `405 method_not_allowed` | As (a) with the status of a class that takes no deploy | The status of a remote repository | R-STOR-007 and R-NPM-036 name `409` for "a format rule refuses the path"; `Allow` would have to list `DELETE` for a format but not for a class |

**Choice:** (a), with the generic code `conflict` and a detail that names the repository and the format's protocol address
("The repository npm-local takes content only through the npm protocol under /api/npm/npm-local/; /files/ does not deploy into
it."). A format this replica does not know takes nothing.

**Rationale:** a format with a protocol of its own builds its documents from what that protocol receives; a raw file or folder
beside them is content no document lists, or a path that blocks the protocol's next write. Deciding it before the body costs
nothing and closes both. The development agent would revisit this with R-NPM-036, which turns a raw `.tgz` into a publish and
then needs a descriptor fact ("takes raw deploys") instead of the protocol path.

**Applied in the spec:** [storage.md](storage.md) R-STOR-007, [api-and-ui.md](api-and-ui.md) R-API-025 (by reference),
[formats/npm.md](formats/npm.md) R-NPM-036. Code: `internal/files/{formats.go,deploy.go}`, `internal/formathost/hooks.go`
(`FilesSeam.RawDeploy`).

### Q-ARCH-224 · Upstream credentials travel over https only, and an https upstream is never left for http

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the hop policy of a remote repository sent the credentials, the client certificate and the custom headers to every hop
of a `sendCredentials` host and, with `forwardAuthOnRedirect`, to every redirect hop, whatever its scheme; and `Client.Allowed`
accepted an `http` tarball URL on the host of an `https` upstream ("the same host over the other default port"). A document of
the upstream could so send a fetch, and with a `sendCredentials` extra host the credential, onto cleartext.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No credential on a hop that is not `https` unless the origin is `http`; `Allowed` refuses `https` → `http` | `CredentialPolicy.allowed` refuses a cleartext hop when the origin is `https` (or unknown); `Client.Allowed` answers `502 insecure_redirect` for an `http` URL of an `https` upstream | Credentials stay encrypted wherever the operator chose encryption; the URL rule is the redirect rule of R-REPO-039 | An `https` upstream whose documents name `http` mirrors stops working for those files |
| (b) Credentials only | (a) without the URL rule | Old documents keep working | Content of an `https` upstream is fetched in cleartext and can be read or swapped on the way (npm verifies integrity, a format without announced digests does not) |
| (c) Refuse `http` upstreams entirely | — | Simplest | An operator's internal cleartext registry is a valid choice the spec allows |

**Choice:** (a). An `http` upstream keeps every rule as written: its own hops, its `sendCredentials` hosts and its forwarded
redirects carry the credential the operator configured for cleartext, and its documents may move to `https`. The description of
`insecure_redirect` names both causes, a redirect and a URL in metadata.

**Rationale:** the operator's choice of scheme is the upstream URL; nothing the upstream sends should weaken it. The development
agent would revisit this if an ecosystem's public registry serves `http` download URLs from `https` metadata.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-033, R-REPO-039, R-REPO-043. Code:
`internal/outbound/hop.go` (`CredentialPolicy.allowed`), `internal/proxy/upstream/client.go` (`Client.Allowed`),
`internal/errs/codes.go`.

### Q-ARCH-225 · A format's write is decided again when it commits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-AUTHZ-015 wants writes evaluated again at commit, on the index the committing replica holds then. `/files/` does it
(Q-ARCH-208); npm's publish decided its write on the index the request began with, before a body of up to `npm.publish.max_tarball_size`
arrived, and its ownership check in the commit transaction used the same index (H-104).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host re-checks every write a request was allowed, before the transaction opens | `formathost.Host.recheck` takes a new index (`Decider.Begin`), decides every allowed write coordinate of the request again, records the new decisions, and moves the request to that index; `Ingest().Commit` and `Host.Write` call it | Every format, every write road (publish, dist-tags, unpublish), no change in a format; the ownership check and the auto claim in the transaction decide on the new index | One index lookup and one decision per write coordinate more per write |
| (b) Each format calls a re-check | A contract method the format calls after its body | The format chooses the moment | Every format must remember it; npm's publish is one place among several |
| (c) Re-decide inside the transaction | — | Closest to the commit | A stale index would make the decision wait with the transaction open (R-ARCH-019) |

**Choice:** (a). A request that was allowed no write takes no new index. A refusal is the decision's error, `403` for a removed
grant.

**Rationale:** the host already holds the request's allowed writes, for the audit row of the transaction; deciding them again where
they are recorded is the one place every format passes. The development agent would revisit this if a format needs to decide a
write that it did not ask the host about before (none does).

**Applied in the spec:** [authorization.md](authorization.md) R-AUTHZ-015 (the commit-time re-check). Code:
`internal/formathost/{state.go,ingest.go,host.go}`.

### Q-ARCH-228 · One request, one transaction: a commit carries the companion files of its lead

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-009 says everything one client request writes goes through one transaction. The item store committed one file per transaction.
npm's provenance publish therefore took two (Q-NPM-22 chose to commit the tarball first): the tarball's commit made the version visible, and a
failure of the bundle's commit left the version published without it and answered 500. NuGet's push stores the `.nupkg` and the generated
`.nuspec` in one commit (R-NUGET-005 step 6), and its symbol and icon files will follow.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit carries companions | `Commit.Companions` (`items.CommitSpec.Companions`): the lead and further files; the blobs of all go through one link transaction (`blob.Store.CommitAll`), the lead's link and hook run first, then each companion's, each with its own `OnCommit` and audit row | The lead/derived relation of both formats; a companion's hook finds the version the lead's hook wrote a moment before; `Generated` marks a derived file; the existing one-file commit is the case with no companion | A request with several files and no lead has no shape (none exists) |
| (b) `Ingestor.CommitMany` | A list of equal commits | No lead | The order of hooks is the caller's, and "the version exists" is not a property of the call; every hook must cope with any order |
| (c) The format holds a transaction across commits | A handle like `Host.Write` | Any order | The blob store's commit protocol owns the transaction while it holds the blob row's lock (Q-ARCH-207), and a second owner would break R-STOR-005 |
| (d) Keep two transactions and repair | A job reconciles the missing bundle | No change | The window stays; a version without a bundle is visible to every client until the job runs |

**Choice:** (a). `blob.CommitAll` writes the objects that are missing first, outside any transaction, and then runs one link function in one transaction that holds
the `FOR SHARE` lock of every blob row; it locks and inserts the rows in digest order, so two commits over overlapping sets do not wait on each other's insert in opposite
orders, and two files of one content are one blob. The items store links the lead, and only when the lead changed a row the companions: a lead identical to the
content at its path is a retry that changes nothing, and its companions with it (npm decides a retry from the facts it reads, which the bundle is part of; a bundle that
differs is no retry). The companions take the lead's origin, actor, `CreateOnly` and `MayOverwrite`: the committer decided the write as a whole. A failure after the first file
leaves nothing: no item, no folder, no audit row; the digests that were written are queued for the collector as after any failed commit.

**Rationale:**
- The two formats that need it have a lead and files that belong to it; the hook order lead-first is what npm's bundle needs (it links itself to the version the tarball's hook created).
- The rollback of an object the commit wrote is the collector's, as for any failed commit (R-STOR-005); no new state.
- The development agent would revisit it if a request must write several files that have no lead (a batch import): `CommitMany` over the same `CommitAll` would then be the shape, and the
  hook would learn all items of the transaction at once.

**Applied in the spec:** R-ARCH-009 (the Host's `Ingest().Commit`), [storage.md](storage.md) R-STOR-005 (the commit protocol over several blobs), [formats/npm.md](formats/npm.md) R-NPM-006 and R-NPM-007 steps 5 and 6 (Q-NPM-58),
[formats/nuget.md](formats/nuget.md) R-NUGET-005 step 6. Code: `internal/blob/commit.go`, `internal/items/commit.go`, `internal/formats/{host.go,hooks.go}`, `internal/formathost/ingest.go`.

### Q-ARCH-229 · A format's settings schema is a catalog fact, and the repository service validates against it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `Descriptor.SettingsSchema` was declared (R-ARCH-008) and set by no format, read by nothing. The repository model that has to validate a document is a domain package and cannot
import the format contract (edge). The management API refused a member named after a format, so npm's settings could be written only by SQL.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The schema is in `catalog.Facts`; the catalog compiles it and validates | `Entry.ValidateSettings(class, raw)` in the leaf; the repository service calls it with the member of the repository's format | The facts every layer reads are in one leaf, as the name rule is (Q-ARCH-188); the same schema serves the form, the validation and the format list | A third-party validator in the leaf |
| (b) The schema stays in the descriptor; the composition root injects a validate function into the repository service | A callback | The leaf stays free of a library | A second path between format and model, and the model cannot validate in a unit test without wiring |
| (c) Hand-written checks per format in the repository package | Go code | No library | The domain learns every format's settings; the schema in the format list would be a second statement |

**Choice:** (a), with `github.com/santhosh-tekuri/jsonschema/v6`: the library `kin-openapi` already pulls in at the version `go.mod` pins, so the repository gains no second schema dependency
(it is a direct requirement now, one version). The schema is compiled at `Register`, which panics on a schema that does not compile, whose root is no object, or whose root does not set
`additionalProperties: false` (a misspelt setting would be dropped silently, R-REPO-008). A property may carry `x-trove-classes`, the classes it applies to: the service reports a setting of another
class as `not_applicable`, the way R-REPO-004 treats the class sections. A violation is a field error at `/<format>/<json pointer>`, `invalid_value`; the list answers 422 `validation_failed`.
`GET /api/v1/formats` serves the schema as `settingsSchema` (null for a format without settings).

**Rationale:** the leaf already holds what the model and the decision point must agree on, and a schema is data. The development agent would revisit it for a setting whose validity depends on
another repository (a list of repositories that must exist): a hook of the format (roadmap M3, remote configuration) would then run after the schema.

**Applied in the spec:** R-ARCH-008 (`SettingsSchema` is a catalog fact), R-ARCH-004 (the form is rendered from the served schema), [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-004 (Q-REPO-122, Q-REPO-124),
[api-and-ui.md](api-and-ui.md) (`GET /api/v1/formats`). Code: `internal/formats/catalog/settings.go`, `internal/repository/{document.go,validate.go}`, `internal/formats/npm/settings.schema.json`.

### Q-ARCH-230 · A part the wiring needs is an input of its step, and a missing one stops the start-up

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-209 made a part the wiring does not provide a start-up error, and four places still ran on convention: the package screens' services were assembled outside the step list and were
the zero value (503) when a part was missing or the README renderer could not be built from `ui.readme.*`; the virtual engine read the item store from a `late` value and returned an empty
area without it; the Host then took a stub resolver whose shadowing check let a first publish proceed (R-REPO-048); and the listener check did not list the engines.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Typed inputs and a step per part | `packages-api` is an area with its own step; `virtual-engine` takes the files area; `wireFormatHost` requires the engine; the stub's check refuses; `requireServing` lists both engines and the package services | The plan proves the order; a missing part names itself at start-up | One more area |
| (b) Keep the zero values and report the area as not ready | A readiness check per area | No new step | A replica that is ready for some routes and 503 for others |
| (c) Panic | — | Loud | The same as (a) without a message that names the area |

**Choice:** (a). The stub resolver of a Host built without the engine (unit tests, the in-memory kit) answers every call with an error, `ShadowCheck` included: a check that cannot run does
not let a publish through. Component constructors keep their documented defaults for tests (`proxy.New` without an invalidation matcher treats every entry as valid; `formathost.New` without engines
takes the refusing stubs); the composition root requires every one of them, so no replica runs on a default.

**Rationale:** the failing case of the stub was dormant (web and all always ran the files step first), but the structure allowed it; a typed input removes the case instead of ordering it away. The development agent
would revisit the component defaults if a production caller other than the composition root appears.

**Applied in the spec:** R-ARCH-024 (step 8), [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 (the shadowing check does not proceed without the engine). Code: `internal/app/{wire_packages.go,wire_virtual_engine.go,wire_formats.go,app.go,steps.go}`,
`internal/formathost/stubs.go`.

### Q-ARCH-231 · `late[T]` stays for job handlers and for the one reader the Host cannot avoid

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-210 scoped `late[T]` to job handlers, whose registry is frozen before the areas are wired. The files seam also reads the Host at request time through `a.host`; the package services and the
virtual engine did too, until Q-ARCH-230 gave them typed inputs.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The seam keeps its `late` Host, documented | `FilesSeam` takes a function that returns the Host | The Host is built from the item store, which is built with the seam; the cycle has one edge, and it is the seam's | A request-time read of a value set once |
| (b) The seam takes the Host through a setter after it is built | Mutable seam | No `late` | A seam that can be used before it is complete; the same race, as a field |
| (c) Split the item store from the seam | Two stores | No cycle | `items.Store` would be built twice or the seam would need its own reads; far larger than the problem |

**Choice:** (a). The one request-time reader is named in the developer documentation; a request that arrives before the Host is set (the listener opens after the step) cannot happen, and the seam answers 500
`internal` if it does.

**Rationale:** the edge is real and has one reader. The development agent would revisit it if a second request-time reader of a `late` value appears.

**Applied in the spec:** Q-ARCH-210 (scope), R-ARCH-024 (step 8). Code: `internal/app/{steps.go,wire_formats.go}`.

### Q-ARCH-232 · The rule against uploading derived metadata is a rule about `Upload` routes, and `Register` enforces it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-012 said "No route accepts a write to a path that `Classify` marks `Computed`". npm's dist-tag routes match `-/package/...`, which `Classify` marks `Computed`, and nothing enforced the rule.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The rule names `Upload` routes; `Register` refuses one whose example `Classify` marks `Computed` | A check in `checkRoutes` | The dist-tag is a write of rows, an `Admin` route; what the rule protects (a client storing a document that trove renders) is checked | The rule is about the route's kind |
| (b) Keep the wording, move the dist-tag routes to a path that is not computed | Change npm's protocol | The words stay true | npm clients call `-/package/<name>/dist-tags/<tag>`; the protocol is not ours |
| (c) Leave it unenforced | — | Nothing to build | A rule nobody checks |

**Choice:** (a). The conformance kit runs `Register`'s checks, so every format meets it.

**Rationale:** the rule's purpose is that a client cannot plant derived bytes. The development agent would revisit it if a format has an `Admin` write that stores bytes the core serves back.

**Applied in the spec:** R-ARCH-012. Code: `internal/formats/formats.go` (`checkRoutes`).

### Q-ARCH-233 · A claim of the spec that the code does not meet yet is a status line with a milestone, not a silent claim

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the review of milestone M2 found places where the spec states what the contract will do for NuGet and the code of M2 does not do it: the pass-through of `Volatile` requests (R-ARCH-013 step 6), the use of the URL map for
metadata (R-REPO-043), the narrowing of what a format can name (Q-ARCH-189), the configuration a format sees (R-ARCH-009), the rendition and its validator derived from one set of inputs (the doc of `RenditionKey`), and `Volatile` search that returns package metadata (R-ARCH-007).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The spec keeps the target and says what is built, with the milestone that builds the rest | A status line at each place, and a row in roadmap M3 "Contract additions before the freeze" | The spec stays the source of truth for the freeze; the gap is visible where a reader meets the claim | A page with a "not yet" in it |
| (b) Weaken the claim to the code | Rewrite the sentence | No gap on the page | The freeze would freeze the weaker contract; the NuGet work would not know it has to build the rest |
| (c) Build all of it in this change | — | No gap | Each is a design of its own (A-2 to A-4, A-6 to A-9 of the review), and NuGet's decisions are what shape them |

**Choice:** (a). Each place states the gap and points at the roadmap; the code comment of `RenditionKey` says what the code does (the cache keys on every field; the format keeps its ETag in step by hand).

**Rationale:** a contract frozen at the end of M3 must carry the additions; marking them where they are claimed makes the spec and the roadmap the same list. The development agent would revisit it if M3 drops one of them: the claim then
changes in the spec in the change that drops it.

**Applied in the spec:** R-ARCH-007 (the sentence on `Volatile`), R-ARCH-009, R-ARCH-013 step 6, Q-ARCH-189 (the `Target` comment of R-ARCH-008), [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-043, [coverage/roadmap.md](../coverage/roadmap.md) M3.

### Q-ARCH-234 · `Newest` resolves as `Merge` until the revalidation exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded by [Q-ARCH-257](#q-arch-257--a-minimal-contract-the-npm-concepts-leave-the-shared-types): the constant is removed.

Context: R-ARCH-008 says `Newest` is `Merge` with the remote members revalidated first. The engine resolves it as `Merge` and revalidates nothing; no format returns `Newest`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Say so in the contract and the engine | The doc comments and R-ARCH-008 state the behaviour; roadmap M3 has the row | Honest; no format depends on it | A format that returns it today gets a merge of possibly stale remote documents |
| (b) The engine refuses `Newest` | An error where the strategy is read | A format that asks for it learns in its tests | `Strategy` returns no error; the signature would change before the freeze anyway |
| (c) Build the revalidation | `EnsureCurrent` on every remote member before the merge | The contract is true | A design for a request no format has (NuGet's registration needs a different merge, roadmap M3) |

**Choice:** (a).

**Rationale:** nothing returns it; building it for an imagined caller would fix the wrong shape. The development agent would revisit it when the first format's merge needs "newest first": the freeze either carries a working `Newest` or no `Newest`.

**Applied in the spec:** R-ARCH-008 (the `Strategy` constants). Code: `internal/virtual/types.go`, `internal/formats/virtual.go`.

### Q-ARCH-235 · The guide is a method of the format contract

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner requires a guide for every registry variant (api-and-ui.md Q-UI-83, R-UI-049), and NuGet joins in M3 without new screens. Who writes what each format's repositories do, and where is it checked that every format writes it?

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Guide() []GuideSection` in the contract, Markdown templates over `GuideData`, embedded files in the format's package; `Base` returns none | The format writes its guide next to its setup templates and behaviour | "Every format ships its guide" becomes a kit rule; the text lives beside the code it describes; the API renders and fills addresses | A method more before the freeze |
| (b) Static content in the UI, one page per format | `web/src/app/features/guides/npm.html` | No contract change | A new format needs a new screen; the text is far from the code; no kit rule |
| (c) An optional interface found by type assertion | `formats.Guider` | No change of the frozen surface | A format can forget it; the kit could only check when it is there |

**Choice:** (a). `ContractVersion` stays 1 (Q-ARCH-213): the change is applied to every format in the same change: generic and the kit's test format inherit or implement it, npm ships its guide, and the test format has the named defects of Q-ARCH-236. `GuideData` embeds `SetupData`, so a guide reaches the same addresses as a template and nothing more; `formats.ExecuteGuide` is the one parser the API and the kit use.

**Rationale:** A guide that a format must ship is a contract fact, and only the contract lets the kit fail a format that has clients and no guide. The development agent revisits this at the freeze (E3.3): if WP3.0j moves dormant methods to optional interfaces, `Guide` stays in the frozen set, because every format with clients must have it.

**Applied in the spec:** R-ARCH-008 (the method and the types), R-ARCH-010 (check 8); [api-and-ui.md](api-and-ui.md) R-UI-049, R-API-053. Code: `internal/formats/guide.go`, `internal/formats/npm/guide.go` with `guide/*.md`.

### Q-ARCH-236 · Kit check 8 holds the guide to the rules of the setup templates

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: check 8 asked that every client have configure and install templates and that no template spell a credential (`token`, `password`, `_auth` outside the option names the fixture lists). A guide's prose must name tokens; its code blocks are what a reader pastes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Extend check 8: required topics per class and client, configure and install attach the setup snippets, the credential rule on fenced code blocks, the shape of a token nowhere, execution over every reader the kit tries | One check, "setup templates and guide" | The rule that every variant is covered is mechanical; a code block cannot carry a credential value; the guide's snippets are the templates' | Prose is not scanned, by design |
| (b) An eleventh check | A new number | Separate reporting | Renumbers the checks the spec, the subtests and check 11 of M4b name |
| (c) Scan the whole text for the words | The template rule verbatim | Simple | A guide could not say "token" |

**Choice:** (a). The named defects `NoGuide` (the remote class has no section) and `GuideCredential` (`_authToken=changeme` in a code block of the configure section) fail check 8 and no other check, as the kit's rule demands of every defect.

**Rationale:** A required topic that says nothing, or a section that only some readers see, would pass a presence test; running each section over the variants catches both. The development agent revisits this when a format needs a required topic the list lacks.

**Applied in the spec:** R-ARCH-010 (check 8). Code: `internal/formats/formattest/guide.go`, `testformat` (`NoGuide`, `GuideCredential`).

### Q-ARCH-240 · A replica admits what its memory holds: memory slots and a heap gate in step 4

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded in part by [Q-ARCH-328](#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets): the slots count bytes, and a download that knows its size hands back what a large object would have held.

Context: the leg `npm-ha` offered P-8's 1,000 requests/s of a 50 KiB tarball to one `web` pod of the dev profile (memory limit 1 GiB, CPU request 250m, `GOMAXPROCS` 1) on a CI runner. The pod served 587/s at a p50 of 3.7 s and was OOM-killed; k6 saw 1,998 unexpected ends of a connection and 761 refused connections. A slow replica keeps more requests in flight (587/s × 3.7 s ≈ 2,000, k6's virtual users), and nothing tied that number to memory: `server.max_in_flight` was 4,096, and a download held about 650 KiB (Q-ARCH-241). R-HA-036's `debug.SetMemoryLimit` was specified but not called, so the garbage collector did not work harder near the limit either. Measured with the binary on the integration stack, before this decision: a download held open costs 1.2 MiB of live heap; 800 of them took the heap to 960 MiB and what the runtime held from the system to 2 GiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Memory slots and a heap gate, replica-wide | Step 4 asks, after the endpoint's `max_in_flight`, a guard every endpoint shares: one of ⌊0.8 × `L` ÷ request bytes⌋ slots per request, and none while the live heap plus stacks is at 80 % of the Go limit `L`, until below 70 % | The slots bound a burst before any GC cycle sees it; the gate bounds requests that hold more than a download; both are an atomic read or a channel send per request | Two numbers instead of one; the gate reads a value that is as old as the last GC cycle (Q-ARCH-242) |
| (b) Derive `max_in_flight` from the memory limit only | The endpoint limit becomes the smaller of the configured one and the memory-derived one | One mechanism | A request that holds a document of several MiB is not a download; a per-request budget large enough for it would refuse most downloads |
| (c) Heap gate only | 503 while the live heap is high | Measures what matters | Admission happens before the request allocates: a burst of 2,000 is admitted inside one GC cycle and allocates past the limit before the gate can close |
| (d) A lower `max_in_flight` default | 1,024 instead of 4,096 | No code | Still unrelated to the memory a replica has; wrong for every size but one |

**Choice:** (a). The request bytes are what one download may hold (640 KiB with the defaults, Q-ARCH-241; 128 KiB of it the overhead measured beside the buffer and the window); a request that holds more is the heap gate's. The slots are replica-wide because the endpoints of a replica share its memory; each endpoint keeps its own `max_in_flight` (R-EP-002). Without a known limit (no `runtime.memory_limit_bytes`, no `GOMEMLIMIT`, or `GOMEMLIMIT=off`) there is no guard. The refusal is the one `max_in_flight` already gave, `503 server_busy` with `Retry-After: 1`, which a client retries and the Service sends to another replica. The shares 80 % and 70 % are constants of the spec, not keys: the operator's lever is the memory limit.

**Rationale:** The defect is that the admitted concurrency followed the replica's speed instead of its memory. The slots make the bound a function of the memory limit and the configured buffers, so it holds however slow the replica is, and the gate covers what the slots cannot see. The development agent revisits this when a request kind other than downloads becomes common enough to need slots of its own, or when the 128 KiB overhead stops matching a measurement of a held download.

**Applied in the spec:** R-ARCH-005 (step 4, the memory guard, an acceptance criterion), the load-protection row of R-ARCH-032, OPS-064 in Deferred, H-322 to H-324; [ha-and-helm.md](ha-and-helm.md) R-HA-036, R-HA-029 (Q-HA-70); [operations-and-observability.md](operations-and-observability.md) R-OPS-008. Code: `internal/httpserver/memory.go`, `internal/app/memory.go`.

### Q-ARCH-241 · A download holds one buffer of its own size, and the object store sends no more than one window ahead

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a heap profile of 400 downloads held open (1 MiB file, defaults) attributed the 1.2 MiB per download to four buffers: the verifying reader's read buffer (`storage.download.buffer_size`, 256 KiB, allocated at full size whatever the file's size), its working copy of the withheld tail and the new bytes (up to 320 KiB), the copy buffer of the response (another 256 KiB) and the HTTP/2 receive buffer of the object-store client, which accepts up to 4 MiB per stream ahead of the reader (237 KiB per download on average here; the S3 gateway of the stack speaks HTTP/2). None of the buffers was pooled: a 50 KiB download allocated 761 KiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One pooled buffer, right-sized, and a bounded receive window | The verifying reader reads into one buffer of `buffer_size` (at least twice `hold_back`, at most the object plus one byte), keeps the withheld tail at its front and writes the cleared bytes from it (`io.WriterTo`), so the response needs no copy buffer; buffers come from pools of power-of-two classes; `storage.s3.http.receive_window` (256 KiB) bounds the HTTP/2 window per object | A held download costs one buffer and one window; a 50 KiB file takes a 64 KiB buffer; a finished download hands its buffer to the next | The tail moves to the front on every refill (at most `hold_back` per `buffer_size` − `hold_back` bytes cleared); a window of 256 KiB caps one stream at 256 KiB per round trip on an HTTP/2 object store far away |
| (b) Pool the existing buffers | Same structure, `sync.Pool` | Smallest change | A held download still holds three buffers and the window |
| (c) Spool slow downloads to disk | Hand the object to the spool and serve from there | Memory independent of the client | Disk I/O for every slow client, and R-STOR-012 forbids spooling a download |

**Choice:** (a). The receive window is a key of `storage.s3.http` because it is a transport setting of the object store; AWS S3 itself speaks HTTP/1.1, where the window does not apply and the kernel's socket buffer holds what is in flight.

**Rationale:** What a download holds is now what the slots of Q-ARCH-240 count: one buffer and one window. Measured the same way after the change: a download held open costs 553 KiB of live heap (the 256 KiB buffer, 220 KiB in the receive window, 81 KiB the rest) and 50 KiB of goroutine stacks, instead of 1.2 MiB; 800 of them take 454 MiB instead of 960 MiB; a 50 KiB download allocates 75 KiB instead of 761 KiB, so the collector runs a tenth as often under the same load. A pooled buffer is overwritten by the next read and only the bytes read for this download are ever written out, so reuse leaks nothing between requests; net/http's writers do not keep the slice after `Write` returns. The development agent revisits the window when an installation's object store is HTTP/2 and far enough away that 256 KiB per round trip limits a download.

**Applied in the spec:** R-ARCH-005 (the request bytes); [storage.md](storage.md) R-STOR-012 (one bounded buffer), R-STOR-033 (`receive_window`). Code: `internal/blob/download.go`, `internal/blob/buffers.go`, `internal/api/edge/content.go`, `internal/outbound` (`WithStreamReceiveWindow`).

### Q-ARCH-242 · A closed heap gate runs a GC cycle when none came for a second

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the live heap is known only at the end of a GC cycle. Under load cycles come every few milliseconds, but once the load that closed the gate stops, a replica that sheds everything allocates nothing, no cycle runs, and the gate would see the old high value until the runtime's forced cycle two minutes later.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A cycle after 1 s without one, while the gate is closed | The sampling goroutine calls `runtime.GC` | The gate opens within about a second of the load ending | A forced cycle costs CPU, at most one per second and only while the gate is closed and nothing else triggers one |
| (b) Gate on the allocated heap instead of the live one | `/memory/classes/heap/objects:bytes` | Always current | Includes garbage: near the Go limit it is high by design, the gate would close under any steady load |
| (c) Open the gate after a fixed time | Close for n seconds | Simple | Opens into a heap that may still be full |

**Choice:** (a).

**Rationale:** The gate must decide on the live heap, and a cycle is the only way to learn it; one per second while refusing is cheap next to a replica that refuses for two minutes. The development agent revisits this if the forced cycles show up in the CPU of a replica that stays at its high-water mark.

**Applied in the spec:** R-ARCH-005 (the heap gate). Code: `MemoryGuard.Sample` in `internal/httpserver/memory.go`.

### Q-ARCH-243 · The memory slots leave the caches' byte budgets aside

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded in part by [Q-ARCH-328](#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets): the admission leaves aside the bytes the caches hold now, not their budgets; the budgets remain the floor the chart checks.

Context: the closing run of phase 3 (Actions run 41235811145, leg `npm-ha-closing`, two kind clusters on one runner) had `web` pods
OOMKilled under the npm load although the memory guard of Q-ARCH-240 was on. The slots shared 80 % of the Go limit among requests, but the
rendition cache (256 MiB), the merged-metadata cache (256 MiB) and the proxy's metadata cache (128 MiB) hold up to 640 MiB of the same heap (the pass-through cache of Q-ARCH-270 adds 8 MiB to that budget)
once full; with the dev profile's 1 GiB limit, full caches and 1,114 admitted downloads needed more than the pod had. The heap gate closes only
at a GC cycle, after the admitted requests have already allocated.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Subtract the cache budgets before sizing the slots (chosen) | ⌊(0.8 × `L` − Σ budgets) ÷ request bytes⌋, at least 1; the chart checks the same | Full caches and full slots fit the limit by construction; the operator sees the trade in one formula | A replica with large caches admits fewer requests at once |
| (b) Size the caches from the limit | Each cache a share of `L` | Nothing to configure | Changes every cache's documented default and its hit rate per pod size |
| (c) Rely on the heap gate | Unchanged | No change | The gate learns of the heap only after a GC cycle; the OOM kills show that is too late |

**Choice:** (a).

**Rationale:** The caches are bounded by bytes already, so their budget is the memory they may take; reserving it is the only way the slot
count is a promise and not an estimate. A start with budgets above 80 % of `L` admits one request at a time and logs an error; the chart
refuses a pod that would have fewer than 64 slots. The development agent revisits this if a cache without a byte budget is added (it would
need one) or if measurements show a cache's overhead well above its budget.

**Applied in the spec:** R-ARCH-005 (memory slots); [ha-and-helm.md](ha-and-helm.md) R-HA-029 and Q-HA-70 (the chart's check). Code:
`cacheBudgets` in `internal/app/memory.go`, `MemoryGuardConfig.Reserved` in `internal/httpserver/memory.go`, `_validate.tpl`.

### Q-ARCH-250 · A p99 bound in microseconds is a nightly gate, and the per-change test asserts allocations and the median

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `TestR_EP_013_CeilingPerformance` (R-EP-013 "Performance", the 1 ms target of R-AUTHZ-015) failed in a full `go test ./...` with
`p99 1.0995 ms` and passed three times alone. Go runs the tests of different packages in parallel processes, and the machine is shared. Measured on
the development machine (20 cores, load average 15 to 60 from other work): the same test, run alone and unchanged, gave p99 of 0.95, 1.1 and 3.8 ms
in three runs; with the matcher of Q-EP-64 (mean 20 µs per `Decide` for the literal rule shape, 140 µs for the wildcard shape) the wall-clock p99 was
0.03 to 1.5 ms (literal) and 0.2 to 20 ms (wildcard) across eleven runs on the same loaded machine, with the median unchanged at 20 and 140 µs. Q-ARCH-220 (owner decision of 2026-10-30) already settles the policy for tails of milliseconds: a per-change test does not gate on a tail
latency on shared CPU, the bound is enforced where it is a statement about the system, in the nightly run with `TROVE_PERF_GATE=1`. A bound of
1 ms at the 99th percentile over 2,000 samples is the same kind of statement at a smaller scale: the 20 slowest samples decide, and one time slice
of a loaded machine is longer than the whole call.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the p99 per change | The test fails when the wall-clock p99 is above 1 ms | Strictest | Fails on a loaded machine without a regression; the failure that prompted this block |
| (b) Retry or raise the bound | Run again until green, or 5 ms | Quiet | A bound that says nothing; retrying hides a real regression |
| (c) Per change: allocations and median; the p99 under `TROVE_PERF_GATE=1` (chosen) | The test fails per change when one `Decide` allocates more than 100 times or its median CPU time is above 1 ms; the p99 fails only with the gate, which `make test-performance` sets and the nightly workflow runs | The allocation count does not depend on the machine and catches the regression that mattered (16,042 allocations per decision); the median is 20 to 150 µs, so a factor 7 to 50 below the bound; the bound itself is enforced as before | A regression of the tail alone shows up the next night |
| (d) A benchmark outside the tests | No test, `go test -bench` by hand | No flakiness | Nothing fails; the target is no longer checked |

**Choice:** (c). The test runs two rule shapes (a literal first segment, and a wildcard first segment, which every rule evaluates) with 1,000 rules each.
The race detector enforces neither time, as before (its instrumentation slows a decision by an order of magnitude). `make test-performance` runs the
test a second time after the integration latency tests, with `TROVE_PERF_GATE=1`, and the report goes to `.out/test-performance.json`.

**Rationale:** The decision follows Q-ARCH-220 and adds what it left open: a unit test, not an integration test. The measurement that backs the
choice: 16,042 allocations and 194 KB per `Decide` before Q-EP-64, 42 allocations and 1.7 KB after; the allocation count is identical in every run. The
development agent would revisit the choice when the unit tests run on runners without a CPU limit and a quiet core (then the p99 in CPU time, Q-EP-65, can return to
every change), or when the allocation bound of 100 proves too tight for a legitimate change to the decision (it is 2.4 times the measured 42).

**Applied in the spec:** R-EP-013 ("Performance"); [docs/developer/build-and-test.md](../developer/build-and-test.md) (`test-performance`);
[docs/developer/releasing.md](../developer/releasing.md) ("Performance (nightly)"); [docs/developer/authorization.md](../developer/authorization.md).
Code: `TestR_EP_013_CeilingPerformance` and `BenchmarkCeilingDecide` in `internal/authz/exposure_test.go`, the `test-performance` target of the `Makefile`.

### Q-ARCH-251 · The coverage generator reads a requirement ID in a Go function name in its underscore form

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a Go function name cannot hold a hyphen, so 427 of the 961 test functions (unit and integration tier) carry their ID as `TestR_API_053_Name`, and the generator read hyphen IDs only
(`R-API-053`). The tests passed and counted for nothing: R-API-053 and R-API-054 were missing from `docs/coverage/implemented.txt`. Reading the function
list of the whole repository (unit and integration tier) with the new parser shows 15 requirements that gain evidence from top-level functions alone (R-API-029, R-API-053, R-API-054, R-AUTH-022, R-GENERIC-016, R-NPM-004, 015, 019, 025, 026, 029, 030, 031, 044, 047) and no function whose
underscore ID fails to parse.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The generator reads both forms (chosen) | `R_API_053` reads as `R-API-053`, with the same standalone rule as the hyphen form, plus `Test`, `Benchmark`, `Fuzz` and `Example` directly before it | Every existing test counts, nothing is renamed; the idiomatic Go name keeps its ID at the front | The parser has a second form to keep strict |
| (b) Rename the tests to carry a hyphen ID | `t.Run("R-API-053 …")` in each, or a hyphen in a subtest | One form | 427 functions and every future one; a function name with a hyphen is impossible, so a subtest wrapper in each |
| (c) Both, and a lint that forbids the underscore form | — | One form in the end | Same cost as (b) |

**Choice:** (a). The underscore form is `R_<AREA>_<digits>` with an upper-case area of one segment and one number, the shape of every requirement in
`docs/spec`; it is narrower than the hyphen form on purpose, so that `TestR_AUTH_027_E1_2_Token` reads R-AUTH-027 and not an ID `R-AUTH-027-E1-2`. It must
stand alone: it starts at the beginning of the name or of a subtest, after anything but a letter, digit or hyphen (an underscore included), or directly after the prefix of a
test function; it ends at the end or before anything but a letter, digit or hyphen. `PR_API_053`, `TestPR_API_053`, `TestR_API_053x`, `TestR_api_053` and `TestR_API_` carry nothing.
An ID the spec does not define is reported as unknown, as for the hyphen form.

**Rationale:** The underscore ID is the repository's convention, 427 functions deep; the cost of the second form is one regular expression and the boundary rules,
held by 40 cases in `TestIDsInTestNames`. The development agent would revisit the choice if a spec ID gets a second segment between the area and the number (then the
underscore form is ambiguous with trailing words, and tests move to the hyphen form in a subtest).

**Applied in the spec:** [docs/developer/coverage.md](../developer/coverage.md) ("Implementation status: the test-naming rule"),
[docs/developer/build-and-test.md](../developer/build-and-test.md). Code: `idsIn`, `underscoreID`, `startsUnderscoreID` in `internal/tools/coverage/evidence.go`.

### Q-ARCH-252 · A member request is derived, and the addressed repository cannot be named by a format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `formats.Request.Via` (the virtual repository the client addressed) was an exported, writable field. Two places built member requests by copying the request and setting `Repo` (`tiers.forMember` in `formathost/virtual.go`, and the audit forwarder and `virtualWriteDocument` of npm), and two places turned the field into `Coordinate.Addressed` (`populateCoordinate`; `coordinate()` did not, so the read check of the audit forwarder's member was decided without the addressed repository and a token scoped to the virtual repository by its exact key was refused, fail closed). Q-AUTHZ-165 named the revisit condition: "if member requests are built at more than one place". It was met (found in the architecture review of 2026-11-01).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Unexport the field; `Addressed()`, `Member(m)` and `WriteTarget(t)` (chosen) | `via` is set only by `Member`, which copies the request, sets `Repo`, keeps the outermost addressed key and refuses a repository that is no member of `rq.Repo`; `Addressed()` is the key (the request's own repository when not derived) and the one thing both coordinate builders read; the re-target of a read that stands for a write has its own method, without the key | A format cannot name another repository as the addressed one; the forwarder is correct by construction; the difference between a member and a write target is visible in the code | Two derivation methods on the frozen surface |
| (b) Keep `Via` exported, document it | Comment only | No change | The field freezes with the contract; any format can set it and so lend a request the standing of a virtual repository it does not belong to |
| (c) Carry the key in the context | `context.WithValue` | No field | An invisible input to a security decision |

**Choice:** (a). `Member` checks membership against `rq.Repo.Members()`, so a request derived once more (a member of a member, R-REPO-018, not built) keeps the first key because `Addressed()` is read from the request it derives from. `WriteTarget` clears the key: a write-intent read (`GET {name}?write=true`, R-NPM-029) is decided on the deployment target itself, so the scope must name the target and the lift of Q-AUTHZ-163, which is for reads through a virtual repository only, does not apply. `virtual.RemoteCall.Via` is renamed `Addressed`, so the concept has one name (`Request.Addressed`, `RemoteCall.Addressed`, `Coordinate.Addressed`).

**Rationale:** a decision about a member must be made on what the client named, and only the host may say what that is. The development agent revisits this when nested virtual repositories are built: `Member` then walks the closure instead of the direct members.

**Applied in the spec:** R-ARCH-008 (the `Request` block with `Addressed`, `Member`, `WriteTarget`, `CacheView`, `Log`, `HostState`), Q-AUTHZ-165 (amended). Code: `internal/formats/route.go`, `internal/formathost/virtual.go` (`forMember`), `internal/formathost/host.go` (`coordinate`), `internal/formathost/remote.go` (`populateCoordinate`), `internal/formats/npm/handlers_audit.go`, `handlers_virtual.go`, `internal/virtual/types.go` (`RemoteCall.Addressed`). Tests: `TestR_AUTHZ_013_AMemberRequestCarriesTheOutermostAddressedKeyIntoBothCoordinates`, `TestR_AUTHZ_013_TheOutermostAddressedKeyIsKeptOnADerivedRequest`, `TestR_AUTHZ_013_AMemberRequestIsRefusedForARepositoryThatIsNoMember`, `TestR_AUTHZ_013_TheDeployTargetReadCarriesNoAddressedKey` (formathost), `TestR_AUTHZ_013_TheAuditForwarderAsksForTheMemberOnBehalfOfTheAddressedVirtualRepository` (npm), `TestR_AUTHZ_013_ASetupTokenRunsTheAuditThroughTheVirtualRepository` (integration).

### Q-ARCH-253 · The stored versions are a method of the view contract, and the source is built before the rule service

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-EP-61 made `Stored` an optional interface (`formats.StoredLister`) found by a type assertion in `internal/app`, bridged to `authz.StoredPackages` there and installed on the rule service afterwards (`SetStored`, an atomic pointer), because the formats were built after the rules. Q-ARCH-235 decided the opposite for `Guide`: a capability every format with the feature must have is a method of the contract, so the kit can fail a format that forgets it. The same review found that "newest" and "serves" were defined nowhere, that `Tags` was read for every format, and that a nil `MayRead` admitted everything.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Stored` is a method of `ViewAdapter`; the bridge is `formathost.ViewAdapters`; a new wiring area `formatports` builds the data ports and the adapters before the exposure area; `ExposureServiceOptions.Stored` is a constructor input (chosen) | `ViewAdapter.Stored(ctx, ports, StoredQuery)`; `ViewAdapters` implements `authz.StoredPackages` and calls it for local and remote repositories only (a virtual one and a format without an adapter answer unlisted); `SetStored` and its atomic pointer are gone; the packages area takes the same adapters | One mechanism for every capability of the view; the kit checks it (check 11); no late-bound state | A wiring area more; the data ports are bound one step earlier |
| (b) Keep the optional interface, move only the bridge | Smaller change | No area | The type assertion stays; the kit cannot fail a format that has screens and cannot list |
| (c) Build the rule service late | `wireExposure` after the formats | No new area | The repository delete hook and the decision point need the rule service before the formats exist (a cycle) |

**Choice:** (a). `StoredQuery.Tags` is set only when the catalog says the format has tags (`catalog.Exposure.Tags`), so an adapter reads nothing for them otherwise; `ViewAdapter.Stored` defines "newest first" and "serves" in its comment and in Q-EP-67. Kit check 11 (`R-ARCH-010`) holds the view adapter: `Compare` is a total order over the fixture's sample versions, `Withheld` and `Stored` answer an empty repository and an unknown package without an error, and over a seeded package `Stored` lists exactly what the repository serves, newest arrival first, within its limit, with tags only when asked. The vulnerability check of M4b, which the spec numbered 11, becomes 12 (it is not built). Q-ARCH-236 had rejected an eleventh check to avoid that renumbering; the view adapter is a contract of its own and does not fit check 8.

**Rationale:** the review's point was that the contract was written in three places (a method, an optional interface, a bridge). The development agent revisits this when a second format with a view is built and the kit's sample cannot express its order.

**Applied in the spec:** R-ARCH-008 (the `ViewAdapter` block), R-ARCH-010 (check 11), Q-EP-61 and Q-EP-63 (amended), Q-EP-66, Q-EP-67. Code: `internal/formats/view.go`, `internal/formathost/view.go`, `internal/formathost/bind.go` (`FormatData`), `internal/app/wire_formats.go` (`wireFormatPorts`), `internal/app/wire_exposure.go`, `internal/authz/exposure_service.go`, `internal/formats/formattest/checks_view.go`, `internal/formats/npm/npmtest/view.go`. Tests: `TestR_EP_016_ViewAdaptersListStoredVersionsOfLocalAndRemoteRepositories`, `TestR_ARCH_010_Check11ViewAdapter`, `TestR_ARCH_010_Conformance` (npm, check 11), `TestR_EP_016_NPMAdapterListsStoredVersions`.

### Q-ARCH-254 · The written contract equals the code, and a capability is not found by a type assertion

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the R-ARCH-008 block lacked `CacheView`, `Log`, `HostState` and `VerifyAnnouncedDigests`, and the spec said "Action" where the field is `authz.Action`; nothing compared them. Two places in `formathost` asked a remote adapter what else it could do (`formats.ArtifactGate`, `formats.TypeExpecter`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A test parses the Go blocks of R-ARCH-008/009 and compares them with the package; an archtest rule bans the assertion; the two optional interfaces become methods of `RemoteAdapter` with defaults in `RemoteBase` (chosen) | `TestR_ARCH_008_TheWrittenContractEqualsTheCode`: for every type the blocks declare, the exported fields and interface methods, the methods the code attaches, the functions and constants; `TestR_ARCH_008_EveryExportedTypeOfTheContractIsNamedInTheSpec`; `archtest.CheckContractAssertions` fails an assertion or a type-switch case to an interface of `internal/formats` outside `Register` | A drift fails with one line per difference at the commit that causes it; a capability cannot be forgotten silently | The blocks are parsed, so they stay valid Go |
| (b) Generate the block from the code | A generator | No drift | The block is prose-annotated; the generator would drop the comments the contract needs |
| (c) Record the two capabilities at `Register` | A struct of capabilities | Format code stays unchanged | A second place to know; the kit could not fail a missing one |

**Choice:** (a). Neither assertion was a genuine extension point: `TypeExpecter` and `ArtifactGate` are what every remote format answers (npm implements both, others need the defaults), so they are `RemoteAdapter.ExpectsType` and `RemoteAdapter.BeforeArtifact`, and the interfaces are gone. The rule's allow-list holds `internal/formats.Register` only, with its reason, and takes further entries with a comment. (Q-ARCH-256 moved the one assertion into `formats.CapabilitiesOf`, which `Register` calls; the allow-list now holds that function.) The anonymous `interface{ Gate(...) }` assertion on the remote fetcher in `formathost/virtual.go` is an interface of `formathost`, not of the contract; the rule does not cover it.

**Rationale:** the contract freezes at the end of M3 (E3.3); this is the cheapest moment to make the written and the compiled contract one thing. The development agent revisits this when a block needs a construct the parser rejects, then with a golden file.

**Applied in the spec:** R-ARCH-008 (blocks and the paragraph after them), R-ARCH-010. Code: `internal/formats/contract_spec_test.go`, `internal/archtest/contract.go`, `internal/formats/remote.go`, `internal/formathost/remote.go`, `internal/formats/npm/remote.go`. Tests: `TestContractAssertions`, `TestRealTree`.

### Q-ARCH-255 · Shared code says tags, not dist-tags

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the exposure rules and the check use "dist-tags", an npm word, as a Go identifier in shared code (`authz.ExposureRuleInput.DistTags`, `ExposureCheckResult.DistTags`, `ruleDims.DistTags`, the fields of the management API's rule and check types), while the catalog speaks of `Exposure.Tags` and the API of `distTags`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rename the Go identifiers to `Tags`; leave the JSON name `distTags`, the database column `dist_tags` and the generated types (chosen) | `Tags` in `internal/authz` and `internal/api/v1` | No format word in shared code; no change a client or a migration sees | The wire name and the column keep the npm word |
| (b) Rename everything | JSON, column, OpenAPI | One word | A breaking API change before 1.0 for a naming reason; a migration |
| (c) Leave it | — | No work | Every second format with tags inherits the npm word |

**Choice:** (a). The public API field `distTags` stays (R-API-003 compatibility); the column and the sqlc-generated rows stay as the schema names them (`dist_tags`); `ruleRow` mirrors the sqlc row and keeps the column's name.

**Rationale:** the Go names are the part a second format reads. The development agent revisits this with the OpenAPI revision that adds a second format with tags.

**Applied in the spec:** none (no behaviour). Code: `internal/authz/exposure.go`, `exposure_service.go`, `internal/api/v1/endpoints_api.go`.

### Q-ARCH-256 · The dormant methods leave the frozen contract, and the contract grows by optional interfaces

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0j. Five parts of the contract of M2 have no caller in the core and cannot be implemented as written: `Format.Indexes` with `IndexBuilder`, `RebuildOptions` and `RebuildResult` (no format has a rebuildable index, Q-ARCH-12); `Format.RemoveVersion` and `Format.VersionGroup` with `VersionRef` (retention and caps do not exist); `Format.OnItemsRestored` (the trash can's restore does not exist, so the item store never calls it; only the kit did); `PackageWriter.Enqueue` with `JobSpec` (a job needs a handler, and a format has no way to register one); and `Descriptor.RepositoryIndex` (nothing reads it). The contract freezes at the end of M3 (E3.3).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Take them out; `OnItemsRestored` becomes the optional `ItemsRestorer` that `CapabilitiesOf` finds; R-ARCH-025 gets the additive-evolution rule (chosen) | The frozen contract is the smaller one; a new behaviour is an optional interface with a field in `Capabilities`, the registry records it at `Register`, the core asks `CapabilitiesFor` | No frozen method nobody calls; the freeze needs no guess at the shape of retention, of the worker-side host or of a job registry; `ContractVersion` stays 1 until a meaning changes | A format that has a restore hook is found by a type assertion, in one function the architecture test allows |
| (b) Design the seams now | A job-handler registration in the descriptor, a worker-side host with ranged reads over stored items, `IndexBuilder`, `RemoveVersion` and `OnItemsRestored` with the transaction, writer and port of `OnCommit` | The P1 work finds its seams | Five designs for callers that do not exist; the wrong shape is frozen (the reasoning of Q-ARCH-234) |
| (c) Keep them as stubs and raise `ContractVersion` when a caller arrives | Today's `Base` defaults | No work now | A frozen method that no test can exercise; a version bump for every one of them; `Base` hides that no format implements them |

**Choice:** (a). Removed: `Format.Indexes`, `IndexBuilder`, `RebuildOptions`, `RebuildResult`, `Format.RemoveVersion`, `Format.VersionGroup`, `VersionRef`, `PackageWriter.Enqueue`, `JobSpec`, `Descriptor.RepositoryIndex` and, with `Enqueue`, `formathost.Deps.Jobs`. `Format.OnItemsRestored` becomes `ItemsRestorer`; npm keeps its method, the kit's `Restore` calls it through `CapabilitiesOf`, and `formats.CapabilitiesOf` is the one function the architecture test allows to assert to an interface of `internal/formats` (it replaces `Register` in the allow-list of Q-ARCH-254, and `Register` calls it). The P1 items that need a seam bring it with them: the symbol-index job of Q-NUGET-11 and the reindex of R-NUGET-025 add `PackageWriter.Enqueue` back (a service of the core that formats call: adding it breaks no format) and a job-handler registration as an optional interface; retention adds `RemoveVersion` and caps `VersionGroup` the same way, each with a kit check.

**Rationale:** the contract that freezes should hold what the core calls. The development agent revisits this when the first of those callers is built (retention, the trash can's restore, the NuGet symbol-index job): that change adds its optional interface, the kit check and the block of R-ARCH-008, and `ContractVersion` rises only if an existing method changes meaning.

**Applied in the spec:** R-ARCH-008 (the blocks, the paragraph "Optional behaviour"), R-ARCH-009 (`PackageWriter`), R-ARCH-012 (the repository-wide index declaration), R-ARCH-025 ("Evolution of the format contract after the freeze"), [storage.md](storage.md) (the hook table, restore, retention and caps). Code: `internal/formats/formats.go`, `hooks.go`, `exposure.go`, `serve.go`, `internal/formats/formattest/store.go`, `internal/formathost/writer.go`, `host.go`, `internal/archtest/contract.go`. Tests: `TestQ_ARCH_256_AnOptionalCapabilityIsFoundOnceAndRecordedAtRegister`, `TestContractAssertions`, `TestR_ARCH_008_TheWrittenContractEqualsTheCode`.

### Q-ARCH-257 · A minimal contract: the npm concepts leave the shared types

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0q. The shared types carry npm's vocabulary: `ExposureView.Tag` (a dist-tag), `SetupData.Scope` and `SetupData.RegistryKey` (an npm scope and the key of an `.npmrc` credential line), `ExposureAdapter.FilterDocument` (called by no core code: npm calls its own), `Strategy.Newest` (returned by no format, resolved as `Merge` by the engine, Q-ARCH-234) and `ViewAdapter.Prerelease` (a second definition of what `catalog.Versioning.Prerelease` already says).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Move what is npm's into npm, drop what the core does not call, derive what exists twice (chosen) | `ExposureView` has `Package` and `Version`; npm reads `Tag` through its own `tagView` and treats a view without it as one that hides all; `FilterDocument` becomes npm's `filterDocument`; `Newest` and `ViewAdapter.Prerelease` go; the screens take "is a pre-release" from the format's exposure adapter; `SetupData.Scope` becomes the format-neutral `Variant` and `RegistryKey` leaves `SetupData` for a template function | The second format finds no word of the first in the contract; one definition of a pre-release | The decision point's value still implements `Tag` for every format with tags; npm asserts to an interface of its own |
| (b) Declare them optional in the contract | `Tag` and the setup fields stay, documented as "may be empty" | No change to the formats | The freeze keeps the npm words |
| (c) Leave them | — | No work | The freeze keeps the npm words and a dead method |

**Choice:** (a). Applied: `ExposureView`, `ExposureAdapter` (no `FilterDocument`), `Strategy`, `ViewAdapter` (no `Prerelease`; `viewBridge.Prerelease` asks `Format.Exposure().Prerelease`, a format without an exposure adapter has no pre-release), npm's `tagView`, `filterDocument` and view. `SetupData.Scope` became `Variant` (the value the request's `scope` input selects for a `configure` snippet; npm: a scope without `@`), `RegistryKey` left `SetupData`, and the template function set (`shell`, `powershell`, `cmd`, `xml`) gained `schemeless`, which drops the scheme of a URL and is how npm's templates spell the key of a credential line; npm's `setup.go`, the kit's `checks.go` and `guide.go` and the API's `setup_snippets.go` follow.

**Rationale:** a contract frozen with another format's words teaches the third format the wrong model. The development agent revisits this when a second format needs a tag-like pointer in exposure rules (OCI): the view then gains an optional method of its own, as `Tag` is for npm today.

**Applied in the spec:** R-ARCH-008 (blocks), [endpoints.md](endpoints.md) R-EP-018 (the adapter and the view), [formats/npm.md](formats/npm.md) (`filterDocument`), Q-ARCH-234 (superseded). Code: `internal/formats/exposure.go`, `view.go`, `virtual.go`, `generic/generic.go`, `internal/formats/npm/exposure.go`, `view.go`, `handlers_remote.go`, `internal/formathost/view.go`, `virtual.go`. Tests: `TestR_NPM_048_TheViewOfARequestAnswersForDistTagsOrHidesAll`, `TestR_API_029_ViewAdaptersBridgeTheRegisteredFormats`.

### Q-ARCH-258 · A `Volatile` route returns package-derived content under the headers of `Metadata`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0d. NuGet's search is a `Volatile` route (a query, passed through, never cached) whose answer carries titles, descriptions and icon URLs that an uploader chose. R-ARCH-007 called a `Volatile` route that returns package-derived bytes a contract violation, so the search could not be written.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The core wraps a `Volatile` route like a `Metadata` route: the headers of R-ARCH-007 without `Content-Disposition`, whatever the handler does (chosen) | `RouteKind.WrapsContent`; `ServeRoute` and kit check 7 use it | The guarantee stays the core's, not the handler's; one place; the search is a route like any other | A `Volatile` route that serves a stored item is still a violation, and the kit says so |
| (b) NuGet's search is a `Metadata` route | The kind that is wrapped | No change to the contract | A search would be classed as a cacheable document: R-REPO-003 gives it the request kind `search`, which is never cached |
| (c) The handler sets the headers | A rule for the format's code | No change to the core | The handler can forget or weaken them; exactly what R-ARCH-007 forbids |

**Choice:** (a). `RouteKind.ServesContent` keeps its meaning (a route that serves stored content or documents); `WrapsContent` adds `Volatile`. The kit's check 7 asserts the headers on a `Volatile` route as on a `Metadata` one, and still fails an `Upload`, `Delete` or `Admin` route that returns package bytes and a `Volatile` route that serves a stored item.

**Rationale:** the text of a search answer is as hostile as a package document. The development agent revisits this if a `Volatile` route ever has to return HTML: it would need its own header set and its own kind.

**Applied in the spec:** R-ARCH-007, R-ARCH-008 (`WrapsContent`), R-ARCH-010 (check 7 and its criteria). Code: `internal/formats/serve.go`, `internal/formats/formattest/checks.go`, `host.go`, `testformat/format.go`. Tests: `TestR_ARCH_007_ServeRouteWrapsTheContentRoutes` (the Volatile cases), `TestR_ARCH_007_FormatRoutesCarryTheContentHeaders`, `TestQ_ARCH_258_OnlyContentRoutesAndVolatileOnesMayReturnPackageBytes`. The stale sentence on `RouteKind.ServesContent` in `route.go` is the owner of that file's to fix.

### Q-ARCH-259 · The package writer signals a raised package, and `Notify` is limited to declared areas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0k. Six call sites in npm attach `metadata_changed` with `PackageChanged(repoID, name)` after a change of a package's rows, by hand; a format that forgets one leaves a stale merged document until its entry expires. `PackageWriter.Notify(area, payload)` accepted any area the cluster knew, change areas included. The name of the signal and the payload function existed three times (`cluster`, `formats`, `virtual`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The package writer attaches the signal once per package it raised; `Notify` accepts the areas of `Descriptor.NotifyAreas` only, signal areas of `catalog.FormatAreas`; the name and the payload function live in the leaf `catalog` (chosen) | `packages.Writer` calls a `Raised` function at the first raise of a package in a transaction; the host attaches the signal, once per (area, payload); npm's six calls go; npm declares `metadata_changed` for its remote fill | No format repeats the call; a rollback sends nothing; a format cannot raise an area of the core | A raise whose package ref the writer does not know costs one query (`GetPackageRef`) |
| (b) `Notify` leaves the contract | Remote fills need another seam (a method that says "this remote document changed") | Smallest surface | A second contract change for the one caller left |
| (c) Leave `Notify` open | — | No work | A format can write the change log of an area it does not own |

**Choice:** (a). The signal follows `packages.rev_seq`: whatever raises it signals it (`EnsureLocked` of a new package, `InsertVersion`, `TrashOrDeleteVersion`, `SetPackageAttributes`, `SetVersionAttributes`, `LinkItem`, `SetProperties` of an item with a package, `BumpRevision`). A change that raises no package row - npm's replacement of a stored remote document in `OnFill` - calls `Notify` for the area it declared. Left as it is (the "k rest" of the roadmap): `virtual.PackageChanged` and its parser stay a second copy of `catalog.PackageChanged`, pinned to it by `TestR_REPO_020_PackageChangedIsThePayloadOfTheEngine`, until the owner of `internal/virtual/mcache.go` makes them delegate (a one-line change each); and the remote engine still attaches nothing itself (Q-REPO-116): a remote fill signals through the format's `OnFill`, because the engine does not know a package name before the format's `PrepareFill` has read the document.

**Rationale:** the revision and the signal are one fact. The development agent revisits this when a second format's remote fill raises package rows (NuGet's cached `.nupkg` rows, R-NUGET-015): its fill then signals through the writer and needs no declared area.

**Applied in the spec:** R-ARCH-008 (`Descriptor.NotifyAreas`), R-ARCH-009 (`PackageWriter`), [virtual.md](../developer/virtual.md). Code: `internal/formats/catalog/changed.go`, `internal/packages/writer.go`, `internal/meta/postgres/queries/packages.sql` (`GetPackageRef`), `internal/formathost/writer.go`, `hooks.go`, `internal/cluster/areas.go`, `internal/formats/npm`, the kit's `store.go`. Tests: `TestQ_ARCH_259_NotifyIsLimitedToTheAreasTheFormatDeclared`, `TestQ_ARCH_259_APackageTheTransactionRaisedSignalsOnce`, `TestQ_ARCH_259_ThePackageWriterSignalsARaisedPackageOnce` (integration), `TestQ_ARCH_259_NotifyAreasAreSignalAreasOfTheCatalog`.

### Q-ARCH-260 · A format names only its own repository: the request's scope, and a writer bound to its event

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0e. `EnsureLocked`, `DeleteItems` and the read stores take a `RepoRef` that the format builds, so a format can name any repository by id; `rq.Repo` is the whole repository model, including `Members()`, which a search over members would iterate and so skip the per-member read check (R-AUTHZ-009 rule 2, R-NUGET-018).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The scope of a request is its repository and the members below it; the stores refuse a repository outside it; a writer is bound to the request's or the event's repository; no signature changes (chosen) | `ServeRoute` puts the scope in the handler's context; `formats.RepositoryAllowed` is the check of the stores; `newWriter` binds the writer; the kit's host does the same | A format cannot name another tenant's repository; no contract change; the kit sees it | A member the format took from `rq.Repo.Members()` without `rq.Member` is inside the scope (H-329) |
| (b) The scope is the record of what `rq.Member` handed out | `Member` and `WriteTarget` add to a set shared by the derived requests | Exactly the members the format was given | The engines hand members to a format without `Member` (`ServeCheck`, `LocalDocument`, `Merge`), and those calls would be refused; the record lives in `Request`, which `route.go` owns |
| (c) Narrow the signatures: no `RepoRef` parameter on a write, a narrowed repository view instead of `*repository.Repository` | The request's repository is implied | The check is structural | A breaking change of every store and writer signature and of every format: contract version 2 |

**Choice:** (a), and (c) is the "e rest": it stays out of version 1 and is a version 2 change, to be taken by an ADR when the narrowed view exists (ADR-136). The read check applies to a context that carries a scope, which `ServeRoute` gives every handler; a hook, a fill and a job run without one and act on their event's repository, which the writer is bound to (`writerForEvent`; the fill path in `internal/formathost/remote.go` calls `writerFor` with no repository today and is handed over to the owner of that file). The kit's host refuses with the same error.

**Rationale:** (a) closes the cross-repository road for formats and costs no signature; what it cannot close is the road inside the family of a virtual repository, which only (b) or (c) close. The development agent revisits this when `Resolver.Plan` (roadmap WP3.0c) hands out the members a caller may read: the scope can then be that list.

**Applied in the spec:** R-ARCH-008 (`Target` comment, `RepositoryAllowed`), R-ARCH-009 (`PackageWriter`). Code: `internal/formats/scope.go`, `serve.go`, `internal/formathost/ingest.go`, `writer.go`, `hooks.go`, the kit's `store.go`. Tests: `TestQ_ARCH_260_TheStoresRefuseARepositoryOutsideTheRequest`, `TestQ_ARCH_260_AWriterWritesToItsOwnRepositoryOnly`, `TestQ_ARCH_260_AFormatNamesItsOwnRepositoryOnly`. The open gap is [H-329](../security/repository-model.md#h-329--a-format-that-reads-a-member-without-rqmember-skips-the-per-member-read-check).

### Q-ARCH-261 · The rendition ETag, the typed environment and the engine value types stay as they are across the freeze

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0f, WP3.0i and WP3.0m. (f) The ETag of a rendered document is the format's own: it hashes the inputs it keeps and keeps them in step with `RenditionKey` by hand; the roadmap would derive one weak ETag from the key and a serving function that owns the conditional request, the cache, gzip and the headers. (i) `Bind(host, *config.Config, metrics)` is a duck-typed method on a process-wide format, which hands a format the whole configuration. (m) Digests, entry state, currency, cache status, URL mapping and the fill event are declared in `internal/formats` and mirrored by the engine; a repository's numeric id is parsed in several places. Each is a refactor of what sits behind the contract, not a change of what a format must implement.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Do none of them before the freeze; each is added or aliased later (chosen) | (f) NuGet keeps its weak ETag in its own `document` leaf, as npm does; (i) NuGet implements `Bind` as npm does; (m) the types stay | No change to the frozen files; the three are source compatible when they arrive: a serving function is a helper, a typed environment is an optional interface that replaces `Bind` per format, a type alias changes no format | The formats carry the hand-kept ETag and the duck-typed `Bind` until then |
| (b) Do them in M3 | Derive the ETag, build the typed environment, alias the types | The contract is complete | Three refactors under the same freeze as the format that needs the contract; a second ETag path to test before the first NuGet release |
| (c) Do only (f) | Move npm onto the serving function | The comment of `RenditionKey` becomes true | The same risk for npm's cache and `304` behaviour, with NuGet as the only reason |

**Choice:** (a). The comment of `RenditionKey` says what the code does (the cache keys on every field, the ETag is the format's). `Bind` is not in the frozen blocks; it is the composition root's duck-typed call (`hostBinder` in `wire_formats.go`), and R-ARCH-009 says so.

**Rationale:** none of the three changes what a format implements, so none needs the freeze to wait. The development agent revisits (f) with NuGet's `document` leaf in phase 3.1f (if the two formats' ETags are the same function, it moves into `internal/formats` as a helper), (i) when a format needs a section of the configuration that npm's `Bind` does not cover (`nuget.search.*`, `nuget.archive.*`, R-NUGET-030), and (m) with the first change that touches those types for another reason.

**Applied in the spec:** R-ARCH-009 (the status lines on `Bind` and on the ETag), R-ARCH-012. Code: none.

### Q-ARCH-262 · The remote configuration probe, the populate decision through a virtual repository and the defaults for missing dependencies are not contract changes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: roadmap WP3.0g (the part after the catalog fact), WP3.0s and WP3.0t. (g) A check of the probe's status and bounded body that the repository service runs on create, on update and on the upstream test, so that a NuGet remote whose service index lacks `PackageBaseAddress/3.0.0` is refused with 422. (s) The populate decision of a fill through a virtual repository is the decision that allowed the write, where the bridge builds an allow itself for the audit event (R-AUTHZ-029). (t) Components keep a default for a missing dependency (`proxy.New` without an invalidation matcher treats every entry as valid) that only tests use, and the files seam reads the Host at request time.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The catalog fact `checksumPolicy: require` unsupported -> 422 is the part of (g) that is built; the probe check, (s) and (t) stay out of the contract (chosen) | (g rest) a remote NuGet repository whose index lacks the resources answers 502 on its first fetch, as the NuGet page's Q-NUGET-14 decides; (s) is the bridge's and the decision point's, phase 7; (t) is phase 7 | The frozen files change for none of them | A misconfigured NuGet remote is found on the first fetch, not on the save; an audit event records an allow the bridge built; two defaults stay |
| (b) Build the probe check and a hook for it in the descriptor | `Descriptor` gains a validation function | The 422 at save | A contract addition for one format; the check would make the repository service open an outbound connection, which today only the proxy and the probe of the upstream test do |
| (c) Build (s) and (t) now | Return the deciding decision from the check; make the missing dependency a refusal | Honest audit; no test-only defaults | Both touch the engines (`internal/virtual`, `internal/proxy`) in the wave that rewrites them |

**Choice:** (a). The contract needs none of the four, so the freeze does not wait for them.

**Rationale:** each is a property of an engine or of the composition, not of what a format implements. The development agent revisits (g rest) when a user reports a remote saved with a bad index (the check then becomes an optional interface of the remote adapter, `Validate(probe)`), and (s) and (t) in phase 7, where the audit and the composition root are revisited.

**Applied in the spec:** none beyond this block and the roadmap rows. Code: none.

### Q-ARCH-263 · A refusal learns the repository key and the base URL from the request it is rendered with

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: NuGet answers every unmatched path with 404 `nuget_v2_unsupported` and a message naming the absolute URL of the repository's service index, and a key of a repository of another format with a message saying that it is not a NuGet repository (R-NUGET-001). `Format.WriteError(w, r, err)` has neither a repository nor a link builder: the `Request` and the `Host` exist only after routing, and the chain's own refusals render before that. `Format.WriteError` is part of the frozen contract.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A `Refusal` value in the context of the request the dispatcher passes to `WriteError` (chosen) | `formats.WithRefusal` / `RefusalFrom`; set as soon as the key has the shape of a key | No signature change of the frozen `WriteError`; the chain's shape table (`HandleErrorShape`) receives the same request; a refusal before the key is known simply has none | A value in the context instead of a parameter; a format must handle its absence |
| (b) Add the key and the base URL as parameters of `WriteError` | A new signature | Explicit | A breaking change of a method every format implements, and the chain's stages, which render through the table with the request only, do not know the key |
| (c) The format reads `rq.Host.Links` in the handler and renders the refusal itself | No change | Needs nothing new | The unmatched path and the other-format key are refused before any `Request` exists; the format would be told in two places |

**Choice:** (a). The base URL is `httpserver.RequestInfo.BaseURL` of the request, the one source of every absolute URL (R-ARCH-006); the key is the segment as addressed, so the cache view of a remote repository links to itself. A repository of another format stays a 404 `repository_not_found`, now with the message "The repository is not a <format> repository.": the coarse check allowed the subject an action on that key before the lookup, so the caller may name the key; a key the caller has no right on is still the plain 404.

**Rationale:** the context carries what the dispatcher knows when it knows it and costs the contract nothing. The development agent revisits this when a second format needs more than the key and the base URL in a refusal.

**Applied in the spec:** R-ARCH-008 (`Refusal`, `WithRefusal`, `RefusalFrom`; the `WriteError` comment). Code: `internal/formats/route.go`, `internal/api/dispatch/dispatcher.go`. Tests: `TestR_NUGET_001_ARefusalKnowsTheRepositoryTheClientAddressed`, `TestR_NUGET_001_ARepositoryOfAnotherFormatSaysSo` (dispatch).

### Q-ARCH-264 · The declared length of a spooled body is an option of the spool

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `Ingestor.Spool` took the declared length from `rq.ContentLength`. A body that is not the request body has another length: the decoded attachment of an npm publish (base64 inside JSON), one part of NuGet's `multipart/form-data` push. npm worked around it with `withoutLength`, a copy of the request that sets the length to -1; NuGet's part would have needed the same copy.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `IngestOptions.Length`; 0 or less is unknown (chosen) | The format states the length of the reader it passes | No request copy; the zero value of the option set is the safe case (unknown); a part with a known size can state it | A handler that passes `rq.Body` must now state `rq.ContentLength` itself |
| (b) `Length` as `*int64` or a "known" flag | Zero is a length | Distinguishes a declared empty body | An empty body declared as 0 and one of unknown length read the same bytes to the same end; the extra state buys nothing |
| (c) Keep `rq.ContentLength` and copy the request | The status quo | No change | A copy of the request per part, and a spool that reads a request field it does not own |

**Choice:** (a). The host maps 0 or less to the spool's "unknown" (-1); the length check and the early limit check of the spool stay for a stated length.

**Rationale:** the reader and its length belong together; the request's length belongs to the request. The development agent revisits this if a format needs to declare an empty body as a checked fact.

**Applied in the spec:** R-ARCH-014 step 3. Code: `internal/formats/hooks.go` (`IngestOptions.Length`), `internal/formathost/ingest.go`, `internal/formats/npm/publish.go` (`withoutLength` removed), `internal/formats/formattest/testformat/format.go`. Tests: `TestR_ARCH_014_TheDeclaredLengthOfASpooledBodyIsAnOptionNotTheRequestsLength` (formathost).

### Q-ARCH-265 · The host begins a request through the Host interface and keeps its state in the request's context

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the dispatcher called `formathost.Prepare(rq)` and `formathost.Finish(rq)` on the concrete package, and the host kept its per-request state (the allowed write decisions, the spool files) in the untyped `Request.HostState` slot of the frozen contract. The dispatcher therefore imported the implementation, and the slot was a field every format could see and must not touch (E3.3).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Host.Begin(rq) (finish func())`; the state travels in the context of `rq.Request` (chosen) | The host replaces `rq.Request` by a copy whose context holds the state; every request derived with `Member` or `WriteTarget` copies the pointer and shares it | No untyped slot; the dispatcher imports the interface only; one finish function, so that no caller can forget the pair | `Begin` mutates the request it is given (once, before the handler); the state is found by a context lookup |
| (b) `Begin` returns the new `*Request` | No mutation | Pure | The dispatcher must use the returned request in place of its own; every test double wraps it |
| (c) Keep `HostState`, type it as an interface of the contract | A typed slot | Explicit | The contract names an implementation detail |

**Choice:** (a). `Begin` is called after the dispatcher's checks and the construction of the request, once per request; a request that was not begun refuses a write with an internal error, as before.

**Rationale:** the state belongs to the request and dies with it, and the context is the request's own place for that. The development agent revisits this if a second Host implementation needs state a context value cannot carry.

**Applied in the spec:** R-ARCH-008 (`HostState` removed from `Request`), R-ARCH-009 (`Host.Begin`). Code: `internal/formats/host.go`, `internal/formats/route.go`, `internal/formathost/state.go`, `internal/api/dispatch/dispatcher.go`. Tests: `TestR_ARCH_011_TheDispatcherBeginsTheRequestOnTheHostAndFinishesItOnEveryPath` (dispatch), `TestR_ARCH_011_ARequestDerivedFromABegunRequestSharesItsState` (formathost).

### Q-ARCH-266 · A route declares that it accepts the API-key header

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: NuGet clients send the credential of a push, an unlist and a relist in `X-NuGet-ApiKey` (R-NUGET-012), on exactly four routes. The authentication stage has to know on which requests to examine the header, and it runs before the dispatcher routes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Route.APIKey bool`, consumed by the stage that matches the route (chosen) | The format declares it per route; the field names no header: the header is `X-NuGet-ApiKey`, switched by `auth.clients.nuget_api_key_header` | The set of routes is the format's own fact, next to the route; a test over the route table shows it | The stage must learn the route before it authenticates (the later stage change, WP3.0x) |
| (b) A list of paths in the auth configuration | The stage matches paths | No contract field | A second table that drifts from the routes |
| (c) The header on every NuGet route | The stage examines it everywhere | Simple | A credential header on a download route that no client sends there; R-NUGET-012 limits it to the API-key endpoints |

**Choice:** (a). This change adds the field and its documentation only; no stage reads it yet.

**Rationale:** whether a route takes an API key is a property of the protocol operation. The development agent revisits this when the stage change lands.
The stage change landed with Q-AUTH-186 (the stage learns the declaration from the dispatcher's match) and Q-AUTH-187 (no session cookie on a route that
declares it) in [identity-and-authentication.md](identity-and-authentication.md); the choice stands.

**Applied in the spec:** R-ARCH-008 (`Route.APIKey`). Code: `internal/formats/route.go`, read by `Dispatcher.APIKeyRoute` (`internal/api/dispatch`).

### Q-ARCH-267 · A format inspects a fetched artifact before it is stored, and no byte goes out before it did

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: NuGet parses every cached `.nupkg` like an upload and refuses one whose declared identity differs from the requested one with 502 `upstream_package_mismatch` (R-NUGET-015). The engine streamed the artifact to the leader's client while it spooled, withholding only the last 64 KiB until the commit (`internal/proxy/artifact.go`), so a refusal after the body arrived could no longer answer 502: the status line was out and the connection was cut. NuGet v3 announces no content digest (H-35), so the declared identity is the only check.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An optional interface `FillInspector` of the remote adapter, read at `Register`; for a path it declares, the engine does not stream to the leader and runs the hook between the digest check and the commit (chosen) | `InspectsFill(p)` and `InspectFill(ctx, rc, p, sp)`; the result travels as `CommitEvent.Inspection`, the error is the flight's answer | A refusal answers 502 to every request of the flight and leaves nothing; a format without the interface changes nothing (npm streams as before); the capability is a fact of the adapter, not a flag of the core | The leader of a hooked artifact waits for the whole transfer before its first byte, as a waiter does |
| (b) A method of `RemoteAdapter` with a default in `RemoteBase` | Same hook, but every adapter has the method | The kit sees a missing method | Q-ARCH-254 made methods of what every remote format answers; only some formats inspect, and npm's and the test format's adapters would have to embed `RemoteBase` for a capability they never use |
| (c) Keep streaming and reserve the 502 for a failure before the first byte | The hook runs after the tail was withheld | No latency change | The hook needs the whole body; it cannot run before the first byte leaves, so a refusal would be a cut transfer, which R-REPO-040 refuses for the same reason |

**Choice:** (a). The interface is `formats.FillInspector`; `Register` reads it off the adapter once into `formats.RemoteCaps`, and `formathost` reads `formats.RemoteCapabilities(id)`, so the rule of Q-ARCH-254 (no assertion to a contract interface outside `Register`) holds. The engine asks `proxy.Adapter.InspectsFill(path)` after `AcceptResponse`; when true it starts no stream for the leader, runs `InspectFill` on the spooled artifact (outside any transaction, after the digest check) and puts the result into `CommitSpec.Payload`, which `formathost` wraps like an upload's inspection so that `OnCommit` sees it as `CommitEvent.Inspection`. A refusal returns `flightResult{err}`: no blob, item, entry or negative entry, no circuit failure (the upstream answered), the same 502 for the leader and every waiter of the flight. A refused package is logged by the engine; the format counts its own refusals (R-NUGET-028). With the object store unavailable, the verified and inspected body is replayed to the leader's client from the spool (the pass-through of R-REPO-025), so the hook costs no availability. Across replicas the protocol of R-REPO-026 step 4 stands: the holder of the flight failed, so a replica that waited fetches for itself, and a deterministic refusal costs one upstream request per replica at most.

**Rationale:** the hook is the only way to keep both "no unverified byte" (R-REPO-041) and "a refusal answers 502". Latency is the price: the leader of a `.nupkg` now waits for the transfer like its waiters do. The development agent revisits this when a second format needs the hook for large artifacts where that wait matters, then with an incremental inspection that can refuse early.

**Applied in the spec:** R-ARCH-008 (the `FillInspector` and `RemoteCaps` block), R-ARCH-013 (step 4), R-NUGET-015. Code: `internal/formats/remote.go`, `internal/formats/formats.go` (`Register`), `internal/formathost/remote.go`, `internal/proxy/artifact.go`, `internal/proxy/types.go`, `internal/proxy/proxytest/adapter.go`. Tests: `TestFillHook` (`test/integration/proxy`), `TestR_ARCH_008_RegisterReadsTheOptionalInterfacesOfARemoteAdapter`, `TestR_NUGET_015_AdapterRunsTheFillHook`.

### Q-ARCH-268 · Upstream addressing follows a discovery document the engine resolves, and the URL map holds metadata paths

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a NuGet remote addresses nothing by the repository URL and a path. Every resource URL comes from the upstream's service index (`PackageBaseAddress`, the registration hive), and a registration index names pages the engine cannot derive (R-NUGET-014, R-NUGET-015). The engine built every upstream URL from `UpstreamBase` alone and read the URL map for artifacts only (`internal/proxy/fetch.go`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An optional interface `Discoverer`; the engine resolves the named document as a metadata entry before the fetch takes its slot, the adapter parses it, and the result reaches every call of the fetch as `RemoteContext.Discovery`; the URL map applies to metadata paths (chosen) | `DiscoveryDocument()` names path and variant, `ParseDiscovery` parses | The document is an ordinary cache entry: stored, revalidated, collapsed, counted, stale-served, shared by all replicas; one code path for the fetch of any path | The engine fetches a second document before the first; the parse repeats for every fetch |
| (b) The format fetches the document itself through `Host.Remote()` inside `UpstreamRequest` | No engine change | Nothing | The engine is inside a flight with a slot held: a nested fetch deadlocks a repository limited to one concurrent fetch, and the document would bypass the circuit, the populate check and the metrics |
| (c) The format stores the discovered addresses in its own rows at fill | The service index's `PrepareFill`/`OnFill` writes a table | Parsing happens once | A second copy of a cache entry that the invalidation set (R-REPO-045) and the flush do not reach; the format needs a data port for it |

**Choice:** (a). `formats.Discoverer` (`DiscoveryDocument`, `ParseDiscovery`), read at `Register`. In the engine `fetcher.discover` runs at the start of a fetch, a `HEAD`, and a probe, before the slot: it calls `Engine.Metadata` for the document with the request's mode, populate check and caller, so a caller that may not fill the cache gets a stored document or the refusal, and the document is `Stale`-served by the ordinary rules; `proxy.Adapter.Discover` returns the adapter bound to the parsed data, which `UpstreamRequest`, `PrepareFill`, `OnFill`, `InspectFill` and the mapping recorder of the fetch all use. A request for the discovery document itself carries no discovery. A missing, unparsable or unreachable document answers 502 (or the engine's own refusal: offline, circuit, populate) and no other request is sent. A probe resolves the document as the system with an allow-all populate check, because the address of the package document is unknown without it; the stored document is the one exception to "a probe stores nothing". `request()` reads `remote_url_map` for every path, not for artifacts only, and the foreign-host rule applies to the mapped URL as to any other. Alongside, `Request.MaxBytes` (a format's `MaxDocumentBytes`) now bounds metadata only: it bounded artifacts too, which would have refused a `.nupkg` above the document limit.

**Rationale:** the discovery document is data of the upstream, and the engine already owns the caching of upstream data. Known limit: the populate event that the fill of the discovery document records carries the coordinate of the request that needed it (the artifact path), not the document's. The development agent revisits this when the audit trail needs the document's own path, then with a coordinate built by the format.

**Applied in the spec:** R-ARCH-008 (the `Discoverer` block, `RemoteContext`), R-ARCH-013 (step 2), R-REPO-043 (the URL-map status note), R-NUGET-014. Code: `internal/formats/remote.go`, `internal/formats/formats.go`, `internal/formathost/remote.go`, `internal/proxy/fetch.go`, `internal/proxy/probe.go`, `internal/proxy/artifact.go`, `internal/proxy/types.go`. Tests: `TestDiscoveryDocument`, `TestFetchBounds` (`test/integration/proxy`), `TestR_NUGET_014_AdapterBindsTheParsedDiscoveryDocument`.

### Q-ARCH-269 · A format can declare that `checksumPolicy: require` is unsupported

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `require` refuses an artifact without an announced SHA-256 or stronger digest (R-REPO-041). NuGet v3 announces none until R-NUGET-029 (P1), so a NuGet remote with `require` would refuse every package at its first fetch with 502 `upstream_digest_missing`; the configuration was accepted and failed only at use.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A catalog fact `ChecksumRequireUnsupported`; the repository validation answers 422 `not_applicable` at the member (chosen) | One bool in `catalog.Facts`, one condition in `remoteResolver.integrity` | The refusal is at create and update, named by pointer, like the other settings not available yet; zero value keeps npm and the generic format as they are; R-NUGET-029 turns it off by deleting one line | A format fact for one setting |
| (b) Refuse at the first fetch (the behaviour of today) | None | No change | The error shows when a build runs, not when the repository is configured |
| (c) A callback of the format that validates the whole remote section | A validation hook with the probe's answer | Room for the probe check (WP3.0g rest) | A hook for one fact; the probe check is a separate decision (Q-NUGET-14) |

**Choice:** (a). The probe check of WP3.0g (R-NUGET-014's 422 when the service index lacks `PackageBaseAddress/3.0.0`) is not built here and is decided in Q-NUGET-14: the first fetch answers 502 `upstream_error` through `ParseDiscovery`, and the upstream test of R-REPO-024 shows the same.

**Rationale:** the fact is cheap, local and reversible. The development agent revisits this when R-NUGET-029 supplies digests, then by clearing the fact in the NuGet descriptor.

**Applied in the spec:** R-REPO-041 (the table row of `require`). Code: `internal/formats/catalog/catalog.go`, `internal/repository/validate_remote.go`. Tests: `TestR_REPO_041_ChecksumRequireIsRefusedForAFormatWithoutAnnouncedDigests`.

### Q-ARCH-270 · A pass-through answer is cached by the engine, per replica, in a registered cache the format only sizes by period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-013 step 6 says that a `Volatile` path passes through without being stored, and no code did it: the remote engine fetches artifacts and metadata documents only. NuGet's search (R-NUGET-016) forwards a query to the upstream and wants the answer kept for a short period, unfiltered, so that a claim or a grant change acts on cached results at once (Q-NUGET-8). Open were who owns the cache, what it is keyed by, who may make the engine contact the upstream, and what counts toward the memory budget.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine owns one registered cache, keyed by repository, configuration version, method and the upstream URL; the format passes the period per call; the size is `proxy.query_cache_bytes` (chosen) | `RemoteFetcher.Query`, `proxy.Engine.Query`, cache `proxy_query_results` in area `repo_config`, filled by `Cache.Load` (the fill rule of R-ARCH-018), identical queries collapsed in process | Egress, credential policy, slots, circuit, metrics and the audit events of a failed contact are the fetch's, once; every future `Volatile` route (a second format's search) gets the cache without writing one; the cache is in the R-ARCH-032 table and the archtest rules for state apply | A format cannot size its own cache; `nuget.search.remote_result_cache_bytes` of Q-NUGET-8 becomes a proxy key (Q-NUGET-16) |
| (b) The format owns its cache | A map or a registered cache inside `formats/nuget` | The format chooses the size | Every format repeats the egress, circuit and collapse logic or skips it; the format would need the upstream client, which the contract hides (R-ARCH-009) |
| (c) `Exchange` plus a format cache | The existing `Exchange` sends the request outside the cache | No engine change | `Exchange` reads and moves no circuit and takes no slot by design (Q-NPM-62): a search storm would bypass R-REPO-040 and a dead upstream would never open the circuit |

**Choice:** (a), with these rules. A current copy is served to any caller and in any offline mode, because it is the upstream's answer as received and what a caller may see of it is the format's to filter. Without a copy the engine contacts the upstream only when the repository is online, the request is not the cache view or `only-if-cached`, and the populate check allows it (R-AUTHZ-008, R-EP-019: a read-only endpoint's upstream contacts stay prevented and are counted in `trove_endpoint_cache_fill_refused_total`); otherwise it answers `ErrOffline`. The URL must be on the upstream's host or on an `extraHosts` entry (R-REPO-043), so a discovery document cannot steer a search to an arbitrary host, and the client's hop policy alone adds a credential. A 2xx counts toward the circuit as a success and a failure as a fetch's does; a 404 or 410 counts for nothing and writes no negative entry. An expired entry is not served stale. The period is cut to one hour, which is the cache's `SafetyTTL`. `proxy.query_cache_bytes` defaults to 8 MiB, `0` turns the cache off, and the budget is part of the cache budgets of the memory slots (R-ARCH-005, Q-ARCH-243), which moves their default sum from 640 to 648 MiB. The default is not the 32 MiB Q-NUGET-8 proposed: with 32 MiB a pool pod with a 1 GiB limit admits 38 concurrent requests, below the floor of 64 that the chart enforces (R-HA-029), and with 8 MiB it admits 77; the size is an operator key, and the development agent revisits the default with the measured size of a search page once NuGet's search runs against a real upstream. Metrics are the fetch's, with `kind="query"` on `trove_remote_cache_lookups_total`.

**Rationale:** the pass-through is the same act as a fetch, minus the store; building it beside the fetch keeps one set of rules for what may leave the replica. The development agent revisits this when a `Volatile` route needs a body (a POST search), which the key would have to include, or when the upstream rate-limits forwarded searches, which is Q-NUGET-8's condition.

**Applied in the spec:** R-ARCH-013 step 6, R-ARCH-032 (the row of the pass-through answers), R-ARCH-005 (the budgets), R-REPO-025 (the bullet after the metadata bytes), R-NUGET-016, R-NUGET-028, R-OPS metrics (`kind`). Code: `internal/proxy/query.go`, `internal/formathost/query.go`, `internal/formats/remote.go`, `internal/config/outbound.go`, `internal/app/memory.go`, `deploy/helm/trove/templates/_validate.tpl`. Tests: `TestR_ARCH_013_Query*` in `test/integration/proxy`, `TestR_ARCH_013_QueryThroughTheHost`.

Amended by [Q-NUGET-94](formats/nuget.md#q-nuget-94--a-forwarded-answer-fetched-under-the-remotes-current-configuration-outlives-both-applications-of-a-change) (2026-11-02): a change of `repo_config` drops the copies of the repository's older configuration versions only (every copy on a deletion and on a payload without a version), and a fetched answer is stored with `Cache.Set`, without the fill rule's epoch check, because the key carries the configuration version.

### Q-ARCH-271 · The fan-out over the members of a virtual repository is three methods of the Resolver

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: NuGet's merged search (R-NUGET-018) queries the members of a virtual repository itself, local members from their rows and remote members through `Query`. A format that reads `rq.Repo.Members()` would skip the per-member read check (R-AUTHZ-009 rule 2), the pin rules, the claims and the populate right, and could not tell which claims keep a search term away from a member (R-NUGET-016 withheld queries).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Resolver.Plan`, `Resolver.KeepsAway`, `Resolver.ReadScope` (chosen) | The plan is the engine's R-REPO-019 steps 1 to 5 for the caller; the other two answer the withheld-term and the listing questions | The format receives members the caller may read, in order, with tier and fill right, and can neither name another repository nor see a claim; the virtual engine stays the one place that decides | Three methods more in the frozen contract |
| (b) The members and the decision point through `Host` | `Host.Members(rq)`, `Host.ReadScope` | One interface | The Host would answer virtual questions; `Plan` needs the engine's route, claims and fingerprint |
| (c) The format filters with `Authorize` per member | Authorize per member and package | No new method | An audit event per member and row for a listing; `Authorize` decides a request's repository and cannot name a member (Q-ARCH-189) |

**Choice:** (a). `Plan` returns `VirtualPlan{Admitted, Members []PlannedMember{Member, Tier, Position, MayFill}}`; a member the caller cannot read is absent. `KeepsAway(rq, member, term)` takes a member of the plan, or the request's own repository when it is not virtual, and refuses any other repository, so a format cannot probe the claims of one it has nothing to do with. `ReadScope(rq, repos)` is the decision point's unaudited read scope (R-API-029, Q-EP-66): no event is written for rows a listing found itself. The shapes of `internal/authz` are converted, not aliased, because `formats` imports no data package.

**Rationale:** these are the three questions a fan-out asks that only the engine can answer. The development agent revisits the placement of `ReadScope`, which is not virtual, when a listing format that has no virtual class needs it, and moves it to the Host then.

**Applied in the spec:** R-ARCH-008 (the Host block comment and the supporting types), R-NUGET-018 step 1. Code: `internal/formats/virtual.go`, `internal/formathost/virtual.go`, `internal/virtual/engine.go`. Tests: `TestR_NUGET_018_BridgePlan`, `TestR_NUGET_016_BridgeKeepsAway`, `TestR_API_029_BridgeReadScope`.

### Q-ARCH-272 · The virtual bridge knows no format layout, and the artifact gate is a method of the remote fetcher

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `formathost/virtual.go` derived the directory of a package's cached artifacts from the document path, with a case for a file called `index.json` (NuGet's version list), and reached the format's artifact gate through an anonymous interface asserted on whatever `Host.Remote()` returned. A wrapper around the fetcher would have dropped the gate without a compile error, and a withdrawn version would have been served from a cached tier.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `VirtualAdapter.ArtifactPrefix(name)` and `RemoteFetcher.Gate` (chosen) | Both are methods; the bridge calls them | A format cannot forget either unnoticed; no layout in shared code; the archtest rule of Q-ARCH-254 has nothing left to find | A required method on `VirtualAdapter` (no default); testformat and npm gain one |
| (b) A default in a `VirtualBase` and an optional interface | Found at `Register` | Formats with the standard layout write nothing | Two layouts hide behind the default; an optional capability is the thing Q-ARCH-254 removed |
| (c) A catalog fact "artifacts below the directory of the document" | Declarative | No code | A third layout needs a new fact; the answer depends on the name's folding, which is code |

**Choice:** (a). `ArtifactPrefix` returns "" for a format whose cached artifacts are not below one prefix per package, and the merge then lists no cached version. `Gate` is the fetcher's own method (the host's wrapper implements it over `RemoteAdapter.BeforeArtifact`), and a Host without the remote engine refuses it, so a gate that cannot run never lets an artifact pass.

**Rationale:** both were decisions about a format taken in code that knows none. The development agent revisits (b) when a third format has the same layout as npm.

**Applied in the spec:** R-ARCH-008 (blocks), R-NPM-026 (no change in behaviour). Code: `internal/formats/virtual.go`, `internal/formats/remote.go`, `internal/formathost/virtual.go`, `internal/formathost/remote.go`, `internal/formats/npm/virtual.go`, `internal/formats/formattest/`. Tests: `TestR_NUGET_018_TheDirectoryOfCachedVersionsComesFromTheVirtualAdapter`, `TestR_NPM_026_ACachedArtifactOfARemoteMemberMeetsTheGate`.

### Q-ARCH-273 · How is the OpenAPI breaking-change checker pinned and run?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-025 requires an OpenAPI breaking-change check of `api/openapi.yaml` against the latest release, with a pinned tool, one version of it (ADR-125). The repository runs its other Go-built tools (golangci-lint, gocyclo, actionlint) as binaries at a pin in `tools/versions.env`. The product module's `go.mod` must not take on the checker's requirements. A module install of oasdiff reports `main` from `--version`, so the version has to come from the build information.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A binary installed by `go install` at `OASDIFF_VERSION` in `tools/versions.env`, built into `.bin/` under a name with the pin and the Go toolchain; the test reads the module version with `go version -m` (chosen) | `$(OASDIFF_BIN)` and `compat-check` in the Makefile; a line in `versions-check`; the test helper `oasdiffBinary` | The same mechanism as the other Go-built tools; Renovate reads the pin through the existing `# renovate:` regex; `go.mod` stays as it is | The first run builds the tool from the module proxy |
| (b) A `tool` directive in `go.mod`, run with `go tool oasdiff` | As sqlc and oapi-codegen | One pin in `go.mod`, read like the other `tool` directives | The checker's requirements join the `go.mod` of the product module, for a test that only runs the binary |
| (c) An npm devDependency in `web/package.json`, run through npx | A Node wrapper of a Go binary | Renovate's npm manager already groups web updates | The Go-side check would depend on the web workspace's lock file |
| (d) A container image pinned by digest, as Trivy is | `docker run` in the compat job | Isolated from the runner's tools | The job needs Docker, which no other check of the job needs |

**Choice:** (a). The checker is a test-time tool outside the product, and its pin is read from the one file that the Makefile and CI already include. (b) would be the choice if the test imported oasdiff as a library; the test runs the binary and gains nothing from the requirements.

**Rationale:** the same route as the linters and actionlint keeps one rule for every tool (ADR-125), and the pin changes in one line that Renovate updates. The development agent revisits this when the test needs oasdiff as a library, or when a second API tool joins the checks.

**Applied in the spec:** R-ARCH-025 (the acceptance bullet on the checker). Code: `tools/versions.env` (`OASDIFF_VERSION`), `Makefile` (`OASDIFF_BIN`, `compat-check`, `versions-check`), `test/compat/helpers_test.go` (`oasdiffBinary`). Test: `TestR_ARCH_025_CheckerIsThePinnedVersion`.

### Q-ARCH-274 · What is the baseline, and what do the checks accept?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-ARCH-025 and R-ARCH-026 promise compatibility with what users installed, and what they installed is a release tag. The baseline has to be a release and not a moving commit. The CI check runs on the release commit too, where HEAD carries the tag. R-ARCH-025 promises additive changes only within `/api/v1`, so a breaking change has no exception.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The newest `v0.*` tag whose commit is not HEAD's; no waiver; a migration file may only be added, and the additions continue the numbering (chosen) | `git tag --list 'v0.*' --sort=-v:refname`, the first tag whose commit is not HEAD's; no baseline fails the test | Compares with what users installed; a re-run on the release commit compares with the release before it; no second copy to keep in step | The clone needs the tags (`fetch-tags`); a change of major version changes the pattern |
| (b) The newest tag, HEAD included | The same list, without the skip | The simplest rule | On the release commit the check compares the release with itself and always passes |
| (c) A committed snapshot of the spec and of the migration list, refreshed at each release | A file under `test/compat` | No git history needed | A second copy of the spec; a release that forgets the refresh moves the baseline silently |
| (d) The previous commit of main | `HEAD~1` or the merge base | No tags needed | The baseline moves with every push: a breaking change pushed in one commit and kept in the next is never compared with a release |
| (e) A list of waived breaking changes | An allow-list in the test | Allows a deliberate change | R-ARCH-025 has no deliberate breaking change within v1; the list is a second decision for the same change |

**Choice:** (a). A baseline that cannot be found is a failure, not a skip: a check that did not run is not a pass. The migration rule is the runner's own rule, in the other direction: an applied file is never edited (checksum), and a new file takes the next number (`steps.go`).

**Rationale:** the promise is against releases, and the tag names one. The skip of HEAD keeps the check meaningful on the release commit, and the failure without a tag keeps a shallow clone from passing silently. The development agent revisits the pattern when the major version changes (ADR-124 names the owner's decision on it).

**Applied in the spec:** R-ARCH-025 and R-ARCH-026 (the acceptance bullets). Code: `test/compat/helpers_test.go` (`baselineTag`), `test/compat/migrations_test.go` (`checkMigrationChanges`). Tests: `TestR_ARCH_025_NoBreakingChangeAgainstLatestRelease`, `TestR_ARCH_025_RemovedOperationFails`, `TestR_ARCH_025_AddedOperationPasses`, `TestR_ARCH_026_MigrationsOnlyAddedSinceRelease`, `TestR_ARCH_026_ChangedFilesFail`, `TestR_ARCH_026_NumberingContinuesTheSequence`.

### Q-ARCH-275 · How is the TypeScript 7 update held?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Renovate PR #42 proposes typescript 6.0.3 → 7.0.2 in `web/package.json`. Verified on 2026-11-01: the newest Angular 22 patches declare the peer range `typescript >=6.0 <6.1` (`@angular/compiler-cli` 22.2.4 and `@angular/build` 22.2.5, from `npm view`), and typescript-eslint 8.72.1 declares `>=4.8.4 <6.1.0`. A lock file resolved in a scratch copy takes 7.0.2 only by overriding those peer ranges (`npm install --package-lock-only` exits 0 with "overriding peer dependency" warnings). The package root of TypeScript 7.0.2 exports only the version (`lib/version.cjs`), and the bundles of `@angular/compiler-cli` 22.2.4 import the `typescript` module for `SyntaxKind`, `factory` and `Program`. Not verified: a build of the Angular workspace with 7.0.2, which was not run.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A Renovate `allowedVersions: "<6.1"` rule for the `typescript` package, with the reason in its description (chosen) | `packageRules` entry in `renovate.json` | Renovate itself refuses the update, and the rule states its reason and its revisit condition | A later 6.1 is held as well, which the peer ranges also refuse; lifting the rule takes a person |
| (b) Group `typescript` with the `@angular` packages | One pull request for both | The Angular set is reviewed together | A group cannot change peer ranges: the pull request would still not build |
| (c) Leave PR #42 open and close it by hand | No configuration | Nothing to maintain | A red pull request that a person must remember; the next run proposes the same update again |
| (d) `ignoreDeps` for `typescript` | A permanent exception | No version bound to write | Unbounded: a later 6.0.x would be ignored too, and the rule records no reason |

**Choice:** (a). It enforces the constraint the update would break, and it bounds the hold to the 6.x line, so 6.0.x patches still arrive. (b) and (c) do not stop the update, and (d) holds more than the constraint does.

**Rationale:** the hold is a dependency policy with a condition that a person can check (the Angular peer range), and the rule carries both. The development agent revisits it when `@angular/compiler-cli`, `@angular/build` and `typescript-eslint` admit typescript 7 (ADR-140).

**Applied in the spec:** none (dependency policy). Code: `renovate.json` (the `typescript` package rule). Documented in [releasing.md](../developer/releasing.md#renovate) and [ADR-140](../decisions.md#adr-140--typescript-is-held-below-61-until-angulars-peer-range-admits-7x). Not verified: that Renovate closes PR #42 on its next run, because the rule has not run.

### Q-ARCH-276 · How does an e2e set that names no test pass, and when does it fail?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `deploy/dev/e2e.sh` fails a set whose `-run` expression matches no test, so that a mistyped set name cannot pass. The NuGet
sets `nuget-closing` and `nuget-ha` run before any test of their name exists (the NuGet suite of this commit has `TestNuGetClients` only),
and the nightly job must run its set. Before this change the script also required the kind cluster before it read the set, so an empty
set would have brought a cluster up to run nothing.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A per-set flag `empty_ok`; the set passes with a notice when no `func Test...` in `test/e2e/*_test.go` matches its expression, checked before the cluster is required (chosen) | `deploy/dev/e2e.sh`: `nuget-closing` and `nuget-ha` carry the flag; the names come from the source, so neither a cluster nor a compile is needed | Every other set keeps the check; the exemption is named where it applies; once a test of the name exists, the rule applies to it | A flag per set to maintain; a test name that the source scan does not see (a name written by a generator) would count as absent |
| (b) Every set with no matching test passes | Drop the check | Nothing to maintain | A mistyped set name passes silently in every workflow |
| (c) A placeholder test named `TestNuGetHA...` that skips | A skip counts as a run, so the rule passes | No script change | The empty set hides behind a test that checks nothing |
| (d) `go test -list` as the check | The compiled list of the package | Exact, as the test binary sees it | `TestMain` of `test/e2e` requires the cluster environment and starts `kubectl proxy`, so the check would need the cluster it is meant to avoid |
| (e) No nightly job until a test exists | Leave `nuget-ha` out of the schedule | Nothing runs that proves nothing | Contradicts the request that the nightly job run the set; the wiring would stay untested until a test lands |

**Choice:** (a). The exemption is named in the script for the two sets that need it, and the check reads the expression the set runs.

**Rationale:** Verified in this change, without a cluster: `deploy/dev/e2e.sh nuget-ha` and `nuget-closing` log the notice and exit 0, and
`deploy/dev/e2e.sh nuget` with a cluster name that does not exist stops at `require_cluster` before anything is applied. Verified by a read of the
script only: `TROVE_E2E_RUN` set disables the flag, so an override is never an empty pass. A run that finds a test and runs none of it still fails
(the count of passed tests). Not verified: the flag path on a live cluster; the first nightly run of `nuget-ha` is the first one.
The development agent revisits this when a third tier of empty sets appears (then a list instead of flags), or when a test of `test/e2e` is
compiled under a name the source scan does not show (a generated name, a build tag that changes the file set).

**Applied in the spec:** none (script and CI behavior). Code: [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) (`empty_ok`, the check after the case
statement, `require_cluster` below it). Elsewhere: [e2e.md](../developer/e2e.md#the-nuget-clients), [releasing.md](../developer/releasing.md#ci).

### Q-ARCH-277 · Which legs run the NuGet sets, in which jobs, on which ports?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The NuGet sets are `nuget` (every change), `nuget-closing` (a closing dispatch) and `nuget-ha` (nightly, [Q-NUGET-34](formats/nuget.md#q-nuget-34--the-e2e-sets-of-the-nuget-tests)).
The owner moved the HA legs out of Test and Release on 2026-11-01. Every kind leg needs a cluster name and an edge port of its own, distinct from
all others of both workflows: 8080 and 8086 (`e2e`), 8082 to 8084 (`e2e-npm` and its closing legs), 8081 (`failover`), 8085 (`npm-ha-soak`) and 8087
(`npm-ha`) are taken.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One job `e2e-nuget` per workflow, with the matrix shape of `e2e-npm`: `nuget` on 8088 and, on a closing dispatch, `nuget-closing` on 8089 in Test and Release; `nuget-ha` on 8090 in its own job in performance.yml (chosen) | `.github/workflows/release.yml`, `.github/workflows/performance.yml` | The shape of the npm leg, reviewed once; the closing leg reuses the job's steps and the `fromJSON` matrix of `e2e-npm`; the nightly leg follows the owner's split of 2026-11-01; 8088 to 8090 are the first ports unused by both workflows | A third copy of the job's steps in each workflow |
| (b) `nuget-closing` in a job of its own in Test and Release | One job per set | Each leg has its own timeouts | Two jobs with the same steps, and the closing matrix would not be where the npm legs are |
| (c) `nuget` and `nuget-ha` in one nightly job | One workflow for every NuGet leg | Fewer jobs | Removes the per-change gate that R-NUGET-027 requires for every change |
| (d) `nuget-ha` in the closing matrix of Test and Release | The npm-ha-closing leg of before 2026-11-01 | One place for the closing run | The owner moved the HA legs out of Test and Release on 2026-11-01 |

**Choice:** (a). Cluster names carry the set (`trove-nuget-<run id>`, `trove-nuget-closing-<run id>`, `trove-nuget-ha-<run id>`), so legs on one runner
host do not collide, and the port is the only other value a leg needs.

**Rationale:** the leg has the shape the other kind legs have, so a change to one of them is a change to the family. The first free ports keep the
rule of the file ("the ports differ from the others so that the jobs can share a runner host") without a new scheme. Verified by a read of both
workflow files: no other port or cluster name uses 8088 to 8090 or `trove-nuget-`. Not verified: the legs themselves, which need a kind cluster the
development agent did not use.

**Revisit:** when a NuGet set needs its own image set or timeout that the matrix cannot express (then a job of its own, option (b)), or when the owner
moves the NuGet closing legs to another workflow.

**Applied in the spec:** none (CI wiring). Elsewhere: [release.yml](../../.github/workflows/release.yml) (job `e2e-nuget`, `semantic-release` needs),
[performance.yml](../../.github/workflows/performance.yml) (job `e2e-nuget`), [releasing.md](../developer/releasing.md#ci),
[e2e.md](../developer/e2e.md#the-nuget-clients).

### Q-ARCH-278 · How is the NuGet fuzz run of the closing run laid out?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `make fuzz-nuget` runs the six targets of `NUGET_FUZZ_TARGETS` one after another. At 30 minutes each that is three hours, longer than any job
of the workflow may run. The closing run has to fuzz the NuGet readers (`nupkg`, `nuspec`), which parse untrusted archives and XML, and the version and ID rules
(`version`, `id`), which define the order of every version.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One leg per target, six legs side by side, 30 minutes each (chosen) | A matrix of six `pkg`/`fuzz` pairs; each leg runs `make fuzz-nuget NUGET_FUZZ_TARGETS=<pkg>:<target> NUGET_FUZZTIME=30m` | The wall clock is 30 minutes; a crash fails its own leg and uploads its own input | Six runner slots for 30 minutes in the closing run only |
| (b) One leg per package | `nupkg`, `nuspec`, `version` (three targets, 90 minutes in sequence) and `id` | Fewer legs | The `version` leg runs 90 minutes, longer than the 50-minute job budget of the fuzz job |
| (c) Three legs as `fuzz-npm` has them, each sequential | Same as (b) with the sequence inside a leg | The shape of the npm job | The same budget problem: two targets of 30 minutes each in one leg, and six in all |
| (d) The root package's `FuzzR_ARCH_028_Match` joins the run | A target of the root package | Covers the classifier of NuGet | `fuzz-nuget` takes `package:Target` pairs under `internal/formats/nuget/<pkg>`, which the root package is not |

**Choice:** (a). The matrix names the same six pairs as `NUGET_FUZZ_TARGETS` in the Makefile, so the closing run fuzzes what the local target fuzzes.

**Rationale:** the wall clock stays at 30 minutes, and a finding is isolated to one leg with its input. The cost is a second copy of the list: a target
added to `NUGET_FUZZ_TARGETS` is fuzzed in the closing run only when the matrix names it too.

**Revisit:** when the runner pool cannot hold six legs at once, or when the copy of the list drifts (then the workflow reads the list from the Makefile).
Not decided here: whether the root package's match fuzz belongs to the closing run (option (d)).

**Applied in the spec:** none (CI wiring). Elsewhere: [release.yml](../../.github/workflows/release.yml) (job `fuzz-nuget`),
[releasing.md](../developer/releasing.md#ci), [nuget.md](../developer/nuget.md#fuzzing), [build-and-test.md](../developer/build-and-test.md).

### Q-ARCH-279 · How are the NuGet SDK pins and the reference solution kept in step?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Two lines of [tools/versions.env](../../tools/versions.env) pin the same .NET SDK image: `NUGETVECTORS_SDK_IMAGE` (the golden vectors)
and `E2E_NUGETCLIENTS_SDK_IMAGE` (the clients image). The file takes literal values only, so one line cannot read the other. `make versions-check`
(`test/e2e/nugetclients/check-pins.mjs`) fails when they differ, but only in the pull request that moved one of them. The reference solution
[perf/nuget/reference](../../perf/nuget/reference) is generated by `nugetstub reference` from the counting upstream's package set; its unit test already
compares it with a regeneration, and no CI job runs that comparison on its own.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One Renovate group on the depName `mcr.microsoft.com/dotnet/sdk` in `tools/versions.env` (`.NET SDK (NuGet tests)`), with `versions-check` as the guard; the reference check is a step of the existing `generate` job (chosen) | [renovate.json](../../renovate.json), [release.yml](../../.github/workflows/release.yml) (job `generate`), `Makefile` (`nuget-reference-check`) | One pull request moves both lines, and the guard holds for any pull request that moves one of them; the job that checks generated trees already has Go | Relies on Renovate's grouping, which has not run yet |
| (b) Status quo: Renovate updates one line and a person moves the other | None | No configuration | The `versions-check` failure lands in Renovate's own pull request, which must then be fixed by hand |
| (c) One pin that the other line reads | A variable reference | One value | `tools/versions.env` takes literal values, by its own header and by `load-pins` |
| (d) The clients image takes its SDK from the vectors' image at build time | A build argument from one pin | One value in use | The clients image would no longer be checked against the pin it is built from |
| (e) The reference check as a job of its own | A new runner for one command | Isolated | A runner for a command that runs in under a second, while the `generate` job already has Go |

**Choice:** (a). Renovate groups by the depName, so a third .NET image of the same name would join the group too.

**Rationale:** verified here: `check-pins.mjs` exits 0 on equal pins and 1 with a message on a different tag (`10.0.404` against `10.0.403`).
`make nuget-reference-check` passes on the committed tree and fails on a copy with one byte added to `nuget.config`
(`nugetstub: reference: differs from a regeneration: nuget.config`). Not verified: that Renovate applies the group, because the rule has not run;
the file parses as JSON.

**Revisit:** when Renovate's regex manager stops reading the `_IMAGE` lines of `tools/versions.env`, the group does nothing and `versions-check` is the
only guard; when the reference solution is generated by `go generate`, the step moves with it.

**Applied in the spec:** none (dependency policy and CI wiring). Code: [renovate.json](../../renovate.json) (rule `.NET SDK (NuGet tests)`),
[tools/versions.env](../../tools/versions.env) (comment), `Makefile` (`nuget-reference-check`). Elsewhere: [releasing.md](../developer/releasing.md#renovate).

### Q-ARCH-280 · How is the frozen format contract guarded, so that no change passes without its ADR?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.3 freezes the format contract at the end of M3 and makes every later change an ADR. The tests in place at the
freeze review (WP3.5, 2026-11-02) compared, member by member and in both directions, only the types the Go blocks of
R-ARCH-008 and R-ARCH-009 declare. The other exported types of `internal/formats` (`RemoteFetcher`, `Resolver`,
`CommitEvent`, `Ingestor`, `ItemStore` and about sixty more) only had to be named on a spec page; functions and constants
were compared from the spec to the code and not back, and five functions and twenty-two constants were named on no page
(`CatalogEntry`, `Descriptors`, `GuideTopics`, `RequiredGuideTopics`, `SetupTokenRefs`, the guide topics, the aliased
request kinds and pattern subjects, the tiers). `catalog.Facts`, which every `Descriptor` embeds, had no check at all. A
method added to `RemoteFetcher` or a field added to `catalog.Facts` passed every test.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A surface file: a test writes the exported surface of `internal/formats` and `internal/formats/catalog`, one line per member, and compares it with `internal/formats/testdata/contract.txt`, whose first line is `ContractVersion`; `-update-contract` regenerates it. A second test asks a spec page to name every exported function and value (chosen) | `TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile`, `TestR_ARCH_008_EveryExportedFunctionAndValueOfTheContractIsNamedInTheSpec` | Every change of the surface, an addition included, shows as a diff next to its ADR; the file is generated, so it cannot drift; no new tool | A second written form of the contract, though a generated one; it cannot tell an addition from a breaking change |
| (b) Every type in the spec blocks | The blocks declare all exported types | One written form | The spec page becomes an API listing, and a change still carries no signal that it needs an ADR |
| (c) An API compatibility checker against the last release tag in the `compat` job | `apidiff` at a pin | Classifies compatible and incompatible changes | A new pinned tool; an addition passes it although it needs an ADR here; the formats are compiled in, so the compiler already finds a break |
| (d) Review alone | The rule stays prose | No work | The rule of E3.3 has no test |

**Choice:** (a). The surface holds every exported type with its fields, interface methods and methods, every function with
its signature, and every constant and variable with its type and value; a composite literal is written whole, and a
constant that repeats the expression before it (`iota`) is written as its place in its group, so a reordering shows. The
catalog leaf is included because a descriptor embeds its `Facts` and a format declares them; the conformance kit is not,
because the kit grows with the checks R-ARCH-025 asks for and binds no format at run time.

**Rationale:** after the freeze the question about a change is not whether it compiles (it must, for every format, in the
same change) but whether it was decided; a diff line that a reviewer sees beside the ADR makes the decision unavoidable at
the lowest cost. Verified here: the test passes on the committed file and fails with a `-`/`+` pair when one field type in
the file is altered. The development agent revisits this when a second module consumes the contract (then a compatibility
checker against the release), or when the values of repeated constants must be compared as numbers (then a rendering
through `go/types`).

**Applied in the spec:** R-ARCH-008 (the paragraph of supporting names, "The freeze"), R-ARCH-025 (the evolution rules:
every change is an ADR, the criterion between an optional interface and a method with a default, what raises the
version; an acceptance criterion). Code: [internal/formats/contract_spec_test.go](../../internal/formats/contract_spec_test.go),
[internal/formats/testdata/contract.txt](../../internal/formats/testdata/contract.txt), the comments of
`internal/formats/doc.go` and `formats.go`. Elsewhere: [ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1).

### Q-ARCH-281 · How does the conformance kit keep a format from passing a check it does not run?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: E3.3 asks that both formats pass the conformance kit. Neither npm nor NuGet skips a check, but the freeze review
found five ways a check passed without looking: (1) check 7 checks the content headers only on an answer below 400, and a
route whose content cases all expect a refusal counted as covered (`npm.audit.bulk`, `npm.audit.legacy`,
`npm.deferred.get`, `nuget.flatcontainer.embedded`, `nuget.autocomplete`, `nuget.symbols`); (2) check 9 asked a change
case of every write route but not of every storage hook, and no change case of either format drove the restore hook
(`ItemsRestorer`) both have; (3) the kit accepted a skip of check 6 for a format with an upload route and of check 9 for
one with package names; (4) the fill hook of Q-ARCH-267 (`FillInspector`) reads what an upstream sent, as untrusted as an
upload, and came without the kit check R-ARCH-025 asks of every new behaviour; (5) check 3 passed over a route example
without a canonical path while its comment said such a route was held to its kind.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tighten the kit, and make every place a check does not look a reasoned fixture entry (chosen) | `Fixture.NoContent` for content routes whose cases only refuse; the hooks in check 9 with `NoRevision` entries for a hook no change can drive; the skip guards of checks 6 and 9; check 6 runs the crafted inputs through `InspectFill` under the same bounds; a `Download` example must address a canonical path | What is not checked is written where a reviewer reads the fixture, with its reason, as skips and `NoRevision` already are; a format that later builds a listed route fails until the entry goes | Two fixture fields to keep in step |
| (b) Leave the kit and list the gaps on the format pages | Prose | No code | The kit keeps passing what it does not check; a new format inherits the same holes |
| (c) Require a served case for every content route, with no list | Fail the P1 routes until they are built | No list | The P1 routes answer 501 by design until M6; a fixture would have to fake an answer to pass |

**Choice:** (a). The fill hook is held to the time bound, the heap bound and to no panic, not to an answer: what a fill hook
answers is the format's (NuGet serves and caches an archive it cannot read as it came, Q-NUGET-53). The entries at the
freeze: npm lists `npm.audit.bulk` (the kit's repository is local, which answers 501; the forwarded answer is tested in
`handlers_audit_test.go`), `npm.audit.legacy` (501 by design) and `npm.deferred.get` (404 by design, R-NPM-043); NuGet
lists its three P1 routes (R-NUGET-011, R-NUGET-021, R-NUGET-024). Both fixtures gained a change case that restores items
from the trash; both pass with no check skipped, which `TestR_ARCH_010_NoCheckIsSkipped` pins.

**Rationale:** a check with nothing to look at should say so where its fixture is written; that is the rule the kit
already applied to skips and to `NoRevision`, carried to the two places it did not reach. The development agent revisits
the `npm.audit.bulk` entry when a content case can name the class of its repository.

**Applied in the spec:** R-ARCH-008 (acceptance criterion), R-ARCH-010 (checks 3, 6, 7 and 9, the skip rule, the fixture
paragraph, an acceptance criterion). Code: `internal/formats/formattest/kit.go`, `checks.go`, `host.go`, `store.go`;
`internal/formats/npm/conformance_test.go`, `internal/formats/nuget/conformance_test.go`. Tests:
`TestQ_ARCH_281_AContentRouteThatOnlyRefusesIsListed`, `TestQ_ARCH_281_EveryStorageHookIsDrivenByAChange`,
`TestQ_ARCH_281_TheFillHookMeetsTheBoundsOfCheck6`, `TestR_ARCH_010_KitSkips`, `TestR_ARCH_010_NoCheckIsSkipped`,
`TestR_ARCH_010_Conformance`.

### Q-ARCH-282 · How does the road of a pass-through query resolve the discovery document of a format that has one?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine resolves the discovery document of a `Discoverer` at the start of every fetch, before the slot, and
hands the parsed form to every call of the fetch as `RemoteContext.Discovery` (Q-ARCH-268). The pass-through of a
`Volatile` request (Q-ARCH-270) has no adapter in the engine: `proxy.QueryRequest` carries two functions, `Build` and
`Accept`, and the host built the format's `RemoteContext` without the discovery document. NuGet's search resource is
announced in the service index, so the forwarded search (R-NUGET-016) could not build its request; Q-NUGET-55 left
the host side to the package that forwards.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host's `Build` resolves the document through the engine's `Metadata`, with the caller's mode and a populate check that refuses without an audit event, parses it with the format's `Discoverer` and hands it to `UpstreamQuery` (chosen) | `internal/formathost/query.go` `discovery`, `quietPopulate` | No change of the engine's API or of the frozen contract; the document is the cache entry fetches use, stored, revalidated and collapsed by the same rules; the caller's mode and populate check decide as they do for a fetch | The engine looks the document up for every query, also one answered from the per-replica cache, because `Build` runs before the lookup (the URL is the key) |
| (b) The engine resolves it: `QueryRequest` gains the format's adapter and `Query` calls the fetcher's `discover` | One place for discovery | The fetch and the query share the code | The pass-through gains the adapter Q-ARCH-270 kept it free of; a change of `proxy`'s API and its tests for one format |
| (c) The format resolves it through `Host.Remote().Metadata` and passes the search address as a parameter | No host change | Works today | Rejected in Q-NUGET-55 (c): the cached answer's key would hold an address the format put in, a second way to address the upstream beside the service index |

**Choice:** (a). A refusal of the populate check for the document (a caller who may not fill the remote, no stored
document) answers the query with `ErrOffline`, so the format answers from its cache, and writes no
`populate_not_permitted` event: a query brings no content into the repository, and the engine answers a caller who may
not fill a query in the same way. A negative, missing or unparsable document is an upstream failure, which the format
counts and answers from its cache (R-NUGET-016). A format without a `Discoverer` builds its request as before.

**Rationale:** the address a query is sent to must come from the same document, under the same rules, as the address a
fetch is sent to; (a) gets that with one call of the engine's existing road and no new surface. The cost of (a) is one
lookup of the document's cache entry per query; its bytes come from the replica's metadata cache
(`proxy.metadata_cache_bytes`) while it holds them. The
development agent revisits this when that read shows in profiles of a search-heavy installation, or when a second format
with a `Discoverer` passes queries through; (b) is the next step then.

**Applied in the spec:** R-ARCH-013 step 6, R-ARCH-008 (the `Discoverer` block). Code: `internal/formathost/query.go`.
Tests: `TestR_NUGET_016_ARemoteForwardsItsSearchAndFallsBackToItsCache`, `TestR_NUGET_018_AVirtualRepositoryForwardsThroughItsRemoteMember`
(`test/integration/nuget`), `TestR_ARCH_013_QueryThroughTheHost` (`test/integration/proxy`, a format without a `Discoverer`).

### Q-ARCH-283 · Is a migration on main immutable, and what does the check hold to?

Status: **Decided by the owner on 2026-11-02** — option (b). The owner's direction after the first message: the installation is
pre-alpha, so a database reset before the beta is an accepted cost; from the completion of the beta plan
([BETA.md](../projektplan/BETA.md)) on, reliability of installed data is priority 1, and this rule enforces the part of it that
costs nothing now.

Context: on 2026-11-02 `00019_projects.sql`, first committed in `007c3dfc` and on `main`, was rewritten in `f5273866` into the
expand steps 00019 to 00025. The owner's dev cluster had migrated from `main` before that rewrite. Its rollouts then failed with
`migration 00019_projects.sql was changed after it was applied (checksum mismatch)`, and the database was reset. The check of
R-ARCH-026 compared the embedded files only with the latest `v0.*` tag, so a change on `main` that no tag held passed. The
tags `v0.9.0` to `v0.12.0` are commits of `main` dated 2026-11-02; `v0.11.0`, `v0.11.1` and `v0.12.0` hold the rewritten file and
no earlier version of it, so the baseline cannot see an edit made before the newest tag.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The latest release only (the check as it was) | `checkMigrationChanges` against the `v0.*` tag, no history | No history needed; the tags are what a user installed | An edit made before the newest tag passes: the 00019 rewrite passed, and every tag cut from `main` after it holds the rewrite |
| (b) Immutable once on `main`, checked against the history of `main` (chosen) | For every file `main` has held, its content and commits after its first commit on `main` must be none, except the allowlist `historicalEdits` of `test/compat/migrations_history_test.go`; a removal or a rename is a change | Catches an edit at the pull request, before any database applies it; reads what the repository already holds; one target, `make compat-check` | Needs the whole history (`fetch-depth: 0`, checked by `--is-shallow-repository`); a force-push to `main` would remove the commits the check reads |
| (c) Immutable once applied anywhere | A registry of applied checksums, or a database check at start-up | Knows what a database actually holds | No check can know which databases applied which file; the runner's `verifyApplied` already refuses a mismatch in the database that applied it, after the damage of the rollout |

**Choice:** (b), the owner's decision. The rule as written: a migration file on `main` is never edited, not even in a comment or in
whitespace, because the runner checksums the bytes and a comment-only allowance would need a parser to tell it apart from an edit;
it is never renamed or removed; a defect is fixed forward with a new file of the next number in an R-HA-018 class; a change that
must reach an applied database is an expand step, a contract step follows one minor later.

**Rationale:** the incident is the case. The runner refuses an edited file only in a database that has applied it, so the first
database to find the edit is the one a rollout breaks. The history check moves that finding to the pull request, where it costs a
failed job. The allowlist names the two historical edits by their commits: `00009_api.sql` in `dbb8d18f` (ten lines appended before
the rule; the owner's dev cluster reported 00019 and not 00009, and `verifyApplied` checks in ascending order, so its applied 00009
matched) and `00019_projects.sql` in `f5273866` (the rewrite that caused the incident). An entry names one commit, so a later edit of
the same file is not covered; an entry that names no edit of `main` fails the check, so the list cannot outlive its history.

**Applied in the spec:** R-ARCH-026 (the immutability bullet in Files, the history criterion in the acceptance criteria), R-HA-018
(the immutability sentence of the file headers). Code: `test/compat/migrations_history_test.go` (`migrationHistoryProblems`,
`historicalEdits`); the job `compat` and `make compat-check` run it. Tests: `TestR_ARCH_026_MigrationsOnMainAreNeverEdited`,
`TestR_ARCH_026_HistoryEditAfterFirstCommitFails`, `TestR_ARCH_026_HistoryNewMigrationPasses`,
`TestR_ARCH_026_HistoryRemovedMigrationFails`, `TestR_ARCH_026_HistoryRenamedMigrationFails`,
`TestR_ARCH_026_HistoryAllowlistedEditPasses`, `TestR_ARCH_026_HistoryEditOnBranchFails`,
`TestR_ARCH_026_HistoryStaleAllowlistFails`, `TestR_ARCH_026_HistoryBranchBeforeMigrationPasses`.

**Revisit condition:** the beta plan is complete (reliability of installed data becomes priority 1, and a database-level guard may
then be asked for), or the history of `main` is rewritten, or a migration must change after it reached a database, which the rule
does not allow.

### Q-ARCH-284 · The mirror's discovery meets the formats through a leaf contract

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the mirror engine of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-053 is a domain package whose
passes run as jobs on the queue `mirror`; the formats are edge packages, and the format contract is frozen at version 1 (ADR-141). The engine needs, per
format, the names an upstream offers and the artifacts of a package.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An optional interface of the remote adapter whose types live in the leaf `internal/mirror/discover`; `formats.MirrorLister` is an alias of it; `CapabilitiesOf` records it, `CatalogEntry` derives `Entry.Mirror` (chosen) | ADR-150's form | The engine takes a format's lister without importing the contract; no format that cannot discover changes; the repository model refuses the mode from a catalog fact | The leaf joins the formats' allow-list and the contract's surface file |
| (b) A port declared in `internal/mirror` and an adapter in `internal/formathost` | The proxy's form (`proxy.Adapter`) | No new leaf | Every type twice, one copy per side, and a host adapter for four methods |
| (c) A method of `RemoteAdapter` with a default in `RemoteBase` | Every format has it | No assertion | A changed method set of a frozen interface raises the contract version (Q-ARCH-254) |

**Choice:** (a). The engine lends the format a `discover.Upstream` (its `Get` and `Document` go through the repository's client
and the remote engine with the background priority), so a format sends nothing that bypasses the egress rules, the slots or the
throttling; a rate-limit answer reaches it as an error that `discover.AsRateLimited` finds and that it returns unchanged.

**Rationale:** the vulnerability adapter showed that a leaf contract keeps the layers and the frozen contract intact for a
capability only some formats have. The development agent would revisit (b) if the leaf grows types that only the engine needs.

**Applied in the spec:** R-ARCH-008 (the `MirrorLister` and `RemoteCaps` block, the optional behaviour), R-ARCH-003 (the
layout); [ADR-152](../decisions.md#adr-152--the-format-contract-gains-the-optional-mirror-lister). Code:
`internal/mirror/discover`, `internal/formats/remote.go`, `internal/formats/formats.go`, `internal/formats/catalog/catalog.go`,
`internal/archtest/layers.go`, `internal/archtest/imports.go`, `internal/formats/testdata/contract.txt`.

### Q-ARCH-285 · The mirror's jobs run in a queue of their own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the process topology may still change (more than the two Deployments `web` and `worker`). The mirror's passes must
not depend on which process runs them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The queue `maintenance` | Beside garbage collection and the advisory sync | No configuration | One slot per process by default: a pass of hours blocks collection, and collection blocks the dispatch |
| (b) The queue `default` | Beside the evaluation and the rescans | Four slots | Several long passes take every slot of the evaluation |
| (c) A queue `mirror` with `jobs.workers.mirror: 2` (chosen) | Passes and their dispatch apart; whatever process claims the queue runs them | Long passes never delay other work; the dispatch is never behind a pass of another queue; a pool of its own can claim the queue alone (R-HA-062), whatever the topology becomes | One more queue to size |

**Choice:** (c). The specification binds the passes to the queue, not to a Deployment.

**Rationale:** a mirror pass is the longest-running work trove has, its throughput is bounded by the upstream, not by the process,
and a named queue is the unit that a split of the service can assign. The development agent would revisit the default of 2
when installations run more mirrors than claiming processes times two.

**Applied in the spec:** R-ARCH-016 (the kinds and the `jobs.workers` block); [repositories-and-proxy.md](repositories-and-proxy.md)
R-REPO-053. Code: `internal/config/jobs.go` (`JobsWorkers.Mirror`), `internal/mirror/mirror.go` (the names),
`internal/app/wire_mirror.go` (the kinds with handlers that do nothing until the engine's work package).

### Q-ARCH-286 · Clients pre-empt the background across replicas through a signal area

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A signal area `mirror_yield`; a client-serving replica raises it when a client fetch of a repository waits for a slot, at most once per second and repository (chosen) | Valkey Pub/Sub, at most once | Milliseconds with Valkey; nothing written to PostgreSQL; correct without it, because the background keeps within its share, which the bound counts | Lost while Valkey is unavailable |
| (b) A change area | Through the change log | Survives Valkey outages | A PostgreSQL write per second of client pressure, for a hint |
| (c) The replica registry's heartbeat carries the queue lengths | No new mechanism | Durable | Seconds of lag, and the heartbeat is not per repository |

**Choice:** (a).

**Rationale:** the signal is a wake-up that client traffic produces, which is what signal areas are for (R-ARCH-018); losing it
costs performance, never correctness. The development agent would revisit (c) if clients queue measurably behind mirrors while
Valkey is unavailable.

**Applied in the spec:** R-ARCH-018 (the signal areas); [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055,
R-REPO-058. The area is registered in [internal/cluster/areas.go](../../internal/cluster/areas.go) (`AreaMirrorYield`, Q-ARCH-309).

### Q-ARCH-287 · A pass is fenced by its job and the process that claimed it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a pass outlives its jobs (Q-REPO-135); a process that lost its lease may still be running when the next job takes the
pass over.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job's lease alone (`SaveCheckpoint`'s check) | The framework's fence | No column | The pass's state is in its own tables, which the lease does not guard |
| (b) `pass_job_id` and `pass_holder` (the claiming process's instance id) in `mirror_state`; every write of the pass is conditional on both (chosen) | A fence on the row | A late write of a dead process or of a released attempt matches no row; one comparison per statement | Two columns |
| (c) A session advisory lease per repository | R-ARCH-017's form | Exclusive while held | A session connection per pass for days, and leases carry registered numbers only |

**Choice:** (b). The claim sets both in one statement; a job released at shutdown and claimed again by another process writes its
new holder, so the first process, which has returned, can write nothing more.

**Rationale:** the fence belongs next to the state it guards. The development agent would revisit it if the jobs framework
exposes a fencing token of its own.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-053 (step 1, the data model). The tables
are added by the engine's work package (Q-ARCH-288).

### Q-ARCH-288 · The seams of the mirror

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the contract wave of phase 6b lays only the shared pieces that bind no process topology and are cheap to change: the
format's discovery interface, the typed rate-limit outcome, the fetch-slot priority, the configuration members with their
validation, and the names of the job kinds and their queue. The tables, the operations and the events are specified and laid by
the work packages that build them, once the owner has settled the topology (2026-11-04). [docs/developer/mirror.md](../developer/mirror.md)
holds the details and the test plan.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Four work packages along the layers: npm discovery, NuGet discovery, the engine, the API and UI, meeting at the contract wave's types (chosen) | Each owns its directories | No two packages edit one file; each tests against fakes of the others' side | The engine's integration tests need a fake lister until the formats land |
| (b) One work package per requirement | R-REPO-050 to R-REPO-059 | Traceable | Every requirement touches the engine; the packages would collide |

**Choice:** (a):

| Work package | Owns | Implements | Consumes | Tests it writes |
|---|---|---|---|---|
| A · npm discovery | `internal/formats/npm/mirror*.go` (replaces the stub `mirror.go`) | `discover.Lister` on npm's remote adapter: the methods, `_all_docs` key ranges and `_changes` of the replication host, exact names, the plan from the full document with `latest` pinned, `MirrorHosts` | `discover.Upstream`, `discover.AsRateLimited`; no import of `internal/mirror` or `internal/proxy` (the architecture test) | Unit tests over a fake `Upstream`; the npm stub of R-REPO-059 and the npm criteria of [formats/npm.md](formats/npm.md#mirror-discovery) in `test/integration/mirror` |
| B · NuGet discovery | `internal/formats/nuget/mirror*.go` | `discover.Lister` on NuGet's remote adapter: catalog, search listing with its truncation, exact IDs, the plan from the version list and the registration | as A | Unit tests over a fake `Upstream`; the NuGet stub with and without `Catalog/3.0.0` and the criteria of [formats/nuget.md](formats/nuget.md#mirror-discovery) |
| C · engine | `internal/mirror` (the engine beside the names of `mirror.go`, which it may extend), the migration of `mirror_state` and `mirror_pending` with their export classes, `internal/proxy` (pre-emption in `slots.go`, the background rules in `fetch.go`: no circuit move, no probe, `upstream.RateLimitOf` and `*upstream.RateLimitError`, `flight_role: mirror`), `internal/formathost` (the `discover.Upstream` of a pass and the artifact fill through the format's gate), `internal/app/wire_mirror.go`, the engine's queries, the metrics, the signal area `mirror_yield`, the pass events | dispatch, passes, fence, throttling and window, bounds, pre-emption, gauges | `formats.RemoteCapabilities(id).Mirror` through the composition root, `proxy.PriorityBackground`, `proxy.BackgroundShare` | Unit tests with a fake clock (window, back-off, long wait) and a fake lister; `test/integration/mirror`: counting stubs answering `429` with `Retry-After` in seconds and as a date, `503` with and without `SlowDown`, `403` with `X-RateLimit-Remaining: 0`, release and resume, lease loss and the fence, the bound, pre-emption in one process and across a client-serving process and one that claims the queue; e2e set `mirror`: a pass while `npm ci` and `dotnet restore` run (clients first), the drain of the process that runs a pass |
| D · API and UI | `internal/api/v1/mirror_api.go`, the operations and the status schemas in `api/openapi.yaml`, the format descriptor's `mirror`, the API's queries, `web/` (the mirror section of the edit form, the status tab with pause, resume and run), the manual's pages, `mirror.pass_requested` | `GET …/mirror`, `POST …/mirror/run` with their permissions | `mirror.KindPass`, `mirror.PassKey`, `mirror.PassArgs`, the tables of C | Handler tests with permission-narrowed callers and a project administrator; `test/integration/mirrorapi` over a seeded `mirror_state`; the browser journey with axe in the e2e set `mirror` |

The contract wave's shared pieces change only with a note to the other packages: the leaf `internal/mirror/discover` (part of the
frozen format contract, ADR-152), the typed rate-limit outcome, the fetch priority, the members `remote.mode` and `remote.mirror`
with their validation and the error codes `mirror_unbounded` and `mirror_restricted`, the OpenAPI schemas `RemoteSettings` and
`MirrorSettings`, and the names of the kinds and the queue `mirror`.

**Rationale:** the layers already separate the four concerns, and the contract wave puts every type two packages share in one
place without fixing what the topology may still move. The development agent would revisit the cut if the engine needs a format
fact the leaf does not carry.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-050 to R-REPO-059;
[coverage/roadmap.md](../coverage/roadmap.md#phase-6b--remote-full-mirror) (WP6b.1 to WP6b.6).

### Q-ARCH-290 · What does a download of a file of a virtual repository cost in statements?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a finding of the phase 6 work was that a download spent more statements than the budget of R-ARCH-027. The budget named a local download and a remote hit
(1 each) and said nothing of a file of a virtual repository, whose resolution asks the members in order (R-REPO-019): the local members, then the
caches of the remote members. Counted on the PostgreSQL log with `TestR_ARCH_027_ADownloadCostsOneStatement` (a virtual repository over one local and
one remote member, the local member first), before the fix: a file of the local member 3 statements (npm: `GetLiveItem`, `NPMTombstoned` x2; NuGet:
`GetLiveItem`, `NuGetServeState` x2), a file of the remote member 6 for npm (`GetLiveItem` x2, `ProxyEntryGet`, `NPMRemoteTarballFacts` x3) and 3 for NuGet
(`GetLiveItem` x2, `ProxyEntryGet`). The local member's miss is one of them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One statement per member the resolution asks (chosen) | Each tier is the one lookup of its member ([Q-ARCH-291](#q-arch-291--how-does-a-download-read-the-rows-its-serve-check-decides-on-without-a-statement-of-its-own)); the member that has the file is the budget's 1, and every member asked before it adds its miss | Holds for every member and every format with no change to the resolution; the number is exact: asked members | A file of the second member costs 2, of the third 3 |
| (b) One statement for the whole walk | A lookup over the ids of all members, with the eligibility of each (pin rules, claims, exposure, the caller's grant per member) decided before it, and the remote members' entries and facts joined in | A virtual download costs 1 | The walk decides per member (R-REPO-019, R-AUTHZ-008) and a remote member may have to be fetched; a statement over all members moves that decision into SQL or reads rows the caller may not read; a large change of the resolution for the first-member case it does not help |
| (c) A per-replica memory of "member has no such package" | Skip the miss of a local member from a cache | The miss costs nothing | A publish on any replica must be seen by the next request (read-your-writes, R-ARCH-012): a new kind of per-replica state with an invalidation area per package, for a statement that is an indexed lookup |

**Choice:** (a). The budget of R-ARCH-027 reads: file of a virtual repository, 1 query per member the resolution asks until one has the file.
Measured after the fix: npm and NuGet, the file in the local member 1, the file in the remote member (local member first) 2.

**Rationale:** the miss at a local member is the answer to the question the resolution asks (does this member hold the file), and (a) asks it once
per member. The cost that mattered, the same lookup repeated by the serve checks of a member that has the file, is gone: each tier's checks
decide on the rows its own lookup read. Revisit when a profile of virtual artifact requests shows the misses of many local members, or a
virtual repository has many members: then (b) for the local members only, whose rows share a table, is the next step.

**Applied in the spec:** R-ARCH-027 (the database budget per request, the acceptance criterion). Code: `internal/formathost/virtual.go` (`tiers.RemoteCached`:
`GateWith`). Tests: `TestR_ARCH_027_ADownloadCostsOneStatement` (the four virtual routes).

### Q-ARCH-291 · How does a download read the rows its serve check decides on, without a statement of its own?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the serve-time check of a format (R-ARCH-008) decides on rows the item row does not carry: the tombstone of npm and NuGet versions, the
NuGet version row, the facts of an npm remote version (the record of the version, the state of the package document's cache entry,
`serveRemovedVersions`). Each was a statement of its own, run after the item lookup: `NPMTombstoned`, `NuGetServeState`, `NPMRemoteTarballFacts`
(twice for a remote hit: the artifact gate and the serve check each read it). The phase 7 note of BETA.md names a per-replica cache of the immutable
tarball facts as a way out.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The item lookup reads them (chosen) | `GetLiveItem` takes a `servefacts.Key` (kind, package, version) and returns the rows of that kind with the item; the format names the key (`ServeFactsKeyer`, `Capabilities.ServeFacts`), `ServeCheck` and the artifact gate (`FactsGate`, `RemoteCaps.Gate`) decide on `ItemRef.Facts` when they were read for their own key and ask the data port otherwise | 1 statement, no state between requests (R-ARCH-032), the tombstone is as current as the item; a route that passes no key stays correct | A shared statement carries three kinds of a format's rows; the contract grows by two optional interfaces and a field |
| (b) A per-replica cache of the facts | A registered cache keyed by repository, package and version | No statement for a repeated download | The facts are not immutable: a tombstone (unpublish, a restore's replay), a removal mark (the upstream document changed), the currency of the package document (expiry, a restore) and `serveRemovedVersions` all change them. Each needs a change area, an invalidation in the write that changes it, and a row in the R-ARCH-032 table; a stale tombstone serves bytes that were withdrawn |
| (c) One request-scoped memory of the first read | The first of the statements stores its result in the request; the later ones take it | No new statement text | The first statement of the request is still a statement of its own (the gate runs before the engine's lookup); a memory inside a request hides which read a decision rests on |

**Choice:** (a). `internal/servefacts` (a leaf with no imports) holds `Key`, `Kind` (`npm_local`, `npm_remote`, `nuget_local`) and `Facts`; the facts carry the key
they were read for and `Facts.For(key)` is the only way a check may use them. The kinds are the rows of the statements they replace, with the same
conditions, so a decision on the facts equals the decision on the statement. The data-port statements stay as the fallback of every route that
passes no key (fakes, the cache view of a format that does not name one).

**Rationale:** the facts of a version are as mutable as the item they sit beside, and a download already reads the item from PostgreSQL every time
(R-API-052); reading them in the same statement makes them exactly as current, which a cache of "immutable facts" would not. The key travels with the
facts so that a route which looked up one version cannot serve on the facts of another. The contract change is additive (an optional interface, a field, a
host method), so `ContractVersion` stays 1 (R-ARCH-025). Revisit when the statement's plan shows in a profile (the kinds not asked are
`CASE` branches and laterals with a parameter condition) or when a format needs rows that are not found by repository, package and version.

**Applied in the spec:** R-ARCH-008 (`ServeFactsKeyer`, `FactsGate`, `RemoteCaps.Gate`, `Capabilities.ServeFacts`), R-ARCH-027 (the database budget). Code:
`internal/servefacts`, `internal/meta/postgres/queries/files.sql` (`GetLiveItem`), `internal/items` (`RepoRef.Facts`, `Item.Facts`),
`internal/formats` (`ItemRef.Facts`, `RemoteFetcher.GateWith`), `internal/formats/npm/servecheck.go`, `remote.go`, `internal/formats/nuget/servecheck.go`,
`internal/formathost` (`serveKeyOf`, `itemStore.Stat`, `remote.gateWith`, `FilesSeam.ServeFactsKey`), `internal/files/download.go`. Tests:
`TestR_ARCH_027_TheServeFactsKeyNamesTheRowsOfTheCheck`, `TestR_ARCH_027_ALocalServeCheckDecidesOnTheFactsOfTheLookup`,
`TestR_ARCH_027_ARemoteServeCheckDecidesOnTheFactsOfTheLookup`, `TestR_ARCH_027_TheGateDecidesOnTheFactsOfTheLookupAndReturnsThem`,
`TestR_ARCH_027_TheNuGetServeFactsKeyNamesTheRowsOfTheCheck`, `TestR_ARCH_027_TheNuGetServeCheckDecidesOnTheFactsOfTheLookup`,
`TestR_ARCH_027_ADownloadCostsOneStatement`.

### Q-ARCH-292 · How does the cache entry of a remote hit join the item lookup?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a remote hit read the artifact's cache entry (`ProxyEntryGet`), then the item (`GetLiveItem`), then the facts of the format (Q-ARCH-291). The
entry says that the item is a cache entry of this remote repository; the item carries the content.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The item lookup probes the entry (chosen) | `GetLiveItem` takes `entry_probe` and `entry_variant` and returns `entry_stored`: the entry exists at the item's path and is not marked gone. A hit is an item whose entry exists; with no item the engine asks `ProxyEntryGet` once, to tell a miss from an entry whose item is gone (the 404 it answered before), and reads the item again when it finds the entry, because a fill can have committed between the two statements (the first version did not, and answered 404 to the clients that raced a fill) | 1 statement for the hit; the engine's steps and answers are unchanged | A miss costs two more statements (the host's read of the cached artifact before the gate, and the engine's own lookup with its entry check; before: the gate's facts and the entry), in front of an upstream fetch |
| (b) `ProxyEntryGet` returns the item's columns | The entry statement joins the item and returns its row | The entry stays the first read | A second 30-column row type for the item, kept in step with `GetLiveItem` and the scanning's lateral join |
| (c) Skip the entry: an item at the path is a hit | The entry is not read | 1 statement, simpler | An item that a local upload put at the path of a remote repository, or an item whose entry was invalidated, would be served as a cache hit; the entry is the cache's record of the item |

**Choice:** (a). The artifact entries are never marked gone (`remote_cache_entries` has `CHECK (kind = 'metadata' OR upstream_gone_at IS NULL)`), so the
condition on `upstream_gone_at` is a guard, not a rule that changes an answer. `formathost.remote.Artifact` reads the cached artifact first with
`Engine.Cached`, gives its facts to the format's gate and hands the item to the engine as `Request.Hit`; the gate returns the facts the serve check is to
use, which are the ones it read again after a revalidation, so a withdrawal that the revalidation brought is not served from facts read before it. A
hit whose package document is expired, or was validated before a restore, costs the statements of that revalidation besides (one conditional request in one
flight, Q-NPM-13).

**Rationale:** (a) keeps the engine's decisions where they were and moves only where they are read. The extra statements of a miss are small against the
upstream request it precedes (R-ARCH-027 P-6 is measured beyond upstream time and transfer). Revisit when a remote repository's miss path shows the
statement in a profile.

**Applied in the spec:** R-ARCH-027 (remote hit: 1 query). Code: `internal/proxy/artifact.go` (`lookupHit`, `serveItem`, `Cached`), `internal/proxy/types.go`
(`Request.Facts`, `Request.Hit`), `internal/items` (`RepoRef.Entry`, `Item.EntryStored`), `internal/formathost/remote.go` (`Artifact`, `gateWith`, `GateWith`),
`internal/formathost/virtual.go` (`tiers.RemoteCached`). Tests: `TestR_ARCH_027_ADownloadCostsOneStatement` (the remote hits), `TestR_ARCH_027_AnEntryWhoseItemIsGoneIsRefusedAndServedAgainWithIt`
(the 404 of an entry without its item), `TestHitRace_ALookupBeforeACommitAndAnEntryAfterItIsServed` (an item lookup that ran before a fill committed and an entry found after it is served, not refused), `TestR_ARCH_027_ConcurrentColdRequestsThroughAVirtualRepositoryAreAllServed`, the existing engine and format suites (the unchanged answers).

### Q-ARCH-293 · Where a wall-clock target of a functional test is enforced

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-42. Beside the load of several agents on the shared integration stack, two per-change tests failed on
wall-clock bounds. `TestR_NPM_010_ThousandVersions` took 403 ms against 200 ms on a miss and 41 ms against 10 ms from the cache;
under 60 busy goroutines on 20 cores (reproduced) a miss took 0.8 to 3.5 s, a hit median 40 to 175 ms and a `304` up to 240 ms (the transfer of the 1.5 MiB body is most of a hit, so a hit came within 1.5 times a miss), while
the same miss over the idle stack takes 37 to 62 ms. The case "a holder that dies without releasing" of `TestCollapse` served its
waiter after 4.6 s against a bound of the lock's life (3 s) plus 1.5 s. Q-ARCH-220 already moved the 10 ms of the hit to the nightly
run; the 200 ms of the miss and the 50 ms of the `304` were still per change. In the collapse case the protocol is not at fault: the
waiter polls every 100 ms (`flightLock.Wait`), finds the expired key and fetches the body itself, and that fetch (upstream GET of
400 kB, object store, database) is CPU work that grows with the load, which the fixed margin did not allow for.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Widen the fixed bounds | 2 s for the miss, a larger margin for the waiter | Smallest change | A number that is only right for one machine; the next loaded run exceeds it |
| (b) Every target only with `TROVE_PERF_GATE=1`, nothing per change | The functional test checks status and content only | No flake | A rendering or a wake-up that regresses by orders of magnitude passes every change |
| (c) Targets of the reference environment under the gate; per change a bound relative to the cost of the same work under the same load (chosen) | The 200 ms, 10 ms and 50 ms of R-NPM-010 only with the gate; per change a hit not slower than a miss and a miss under 200 ms plus 100 times the median `304` of the document (medians of 5 misses, each with a `304`); the waiter under the lock's life plus 1 s plus four times a cold fetch measured right after | Follows the load the way F-39 does; still fails on a cache that does not serve, a rendering that grows faster than its input, or a waiter woken by the wait bound | Two numbers (100, 4) that are tuned to measurements |
| (d) Dedicated quiet runners for the integration tier | The fixed bounds stay | Strictest | The runners are shared (Q-ARCH-220) |

**Choice:** (c). In `TestR_NPM_010_ThousandVersions` the miss (200 ms), the hit (10 ms; under half a miss) and the `304` (50 ms) are
enforced with `TROVE_PERF_GATE=1`, which `make test-performance` sets; per change the hit may not be slower than the miss and the miss
must be within `200 ms + 100 x` the median `304`; both sides are medians of 5 (misses on a revision moved each time, a `304` after each). In the "holder dies" case of `TestCollapse` the bound is `ttl + 1 s + 4 x` the time a cold
fetch of a second path of the same size takes right after the waiter.

**Rationale:** the miss over the idle stack is 17 to 26 times a `304` (both are one authenticated request with the revision lookup;
only the miss renders), and under load the ratio of the medians stayed between 4 and 28 in 40 runs (single samples ranged from 1 to 290 times, which is why both sides are medians), because the load scales both. A rendering that
is 4 times slower than today, or one that is quadratic in the versions, exceeds 100 times; a cache that renders on every request
fails the hit check. The waiter's added work is a fetch, so a multiple of a fetch under the same load is the measure; a waiter woken
by the wait bound (seconds later) or the lock's TTL still exceeds it. The development agent would revisit the choice when the
integration tier runs on dedicated runners without a CPU limit (then the fixed bounds return, as Q-ARCH-220 says) or when a
regression slips through the factors.

**Applied in the spec:** R-NPM-010 (test note under the acceptance criteria); [releasing.md](../developer/releasing.md) (what
`make test-performance` enforces). Tests: `TestR_NPM_010_ThousandVersions` in `test/integration/npm/documents_test.go`, `TestCollapse`
in `test/integration/proxy/collapse_test.go`.

### Q-ARCH-294 · Which wiring areas the roles `api` and `packages` build

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [ADR-155](../decisions.md#adr-155--three-roles-api-packages-and-worker-variants-are-pools) fixes the route families of
each role and what `api` holds; [internal/app/steps.go](../../internal/app/steps.go) builds every listener area on `web`
(`withListener`). Verified in the code: `wirePackagesAPI` takes the format host only in its input check
([wire_packages.go](../../internal/app/wire_packages.go)); `wireAuth` takes the session store as a required input
([wire_identity.go](../../internal/app/wire_identity.go)); `wireVirtualEngine` takes the proxy engine, and the explanation of
R-REPO-019 asks the remote members only `Cached` and `EntryState` ([internal/virtual/explain.go](../../internal/virtual/explain.go));
`wireFiles` takes no engine ([wire_files.go](../../internal/app/wire_files.go)). Not verified, and left to WP6a.1: whether any
operation of the management API other than the explanation reaches the virtual or the remote engine.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) By family, with the fill path off `api` (chosen) | `packages` builds the engines and the format host, not `sessions`, `login`, `ci_exchange`, `packages-api`, `vuln-api`; `api` builds those, the virtual engine over a read-only view of the remote members, and neither `proxy-engine` nor `formathost` | "api contacts an upstream only for the upstream test" (ADR-155) becomes a property of the wiring that `planSteps` and the role-table test prove; `packages` cannot reach the session store or a login secret by construction | Three code changes: `packages-api` drops its format-host check, the virtual engine takes a read-only `Remotes` on `api`, and the authentication area becomes buildable without the sessions area |
| (b) By family, `api` keeps the engines | `api` builds `proxy-engine` and `formathost` because `packages-api` and the virtual engine take them; no route of `api` calls them | No change to the engines' wiring | A fill path exists on `api` and is held off only by the route list; a later operation that resolves through the engine would fill from `api` without a start-up error |
| (c) Every listener area on both roles; the router alone separates | The surface filter of Q-ARCH-298 is the whole cut | Smallest change | `packages` builds the session store and would read the login secrets if they were mounted: the trust split of ADR-155 is a convention, not a property |

**Choice:** (a). The tables of R-ARCH-001 list each area per role; the authentication stage on `packages` carries no session
credential and ignores a session cookie as absent, as R-EP-010 rule 1 does for an endpoint without `ui`.

**Rationale:** the owner's cut is a trust cut: humans and their secrets in `api`, machine traffic with tokens in `packages`. Of
the three, only (a) makes both halves of it hold by construction, and the step framework (Q-ARCH-209) turns a violation into a
start-up error that names the area. The three changes are small and local to the composition root and the virtual engine. The
development agent would revisit this if the read-only view of the remote members needs more of the engine than an adapter over
the item store, the cache entries, the negative store and the circuit state: then (b), with the role-table test asserting that
`api` registers no route of `packages` or `files`.

**Applied in the spec:** R-ARCH-001 (the role, subsystem and area tables, the MUST NOT lists, the acceptance criterion of the
role table); [coverage/roadmap.md](../coverage/roadmap.md#phase-6a--task-areas) WP6a.1.

### Q-ARCH-295 · What becomes of the role name `web`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: every installation before the cut runs `<fullname>-web` with `--role web` behind the Service `<fullname>`, and its
values files set `web.*`. R-HA-020 promises a zero-downtime rolling upgrade from the previous minor, and R-ARCH-025 keeps a
values key until a major. Helm applies a release in one pass: it creates the new objects, patches the existing ones (the
Ingress among them) and deletes the objects the new release no longer has, without waiting for a new pod to be ready
([Helm's upgrade](https://helm.sh/docs/helm/helm_upgrade/)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `web` stays as `api` and `packages` in one process, and the chart renders it only as the bridge of the upgrade (chosen) | The binary keeps the role (the union of the two area sets); `web.enabled: true` keeps the old shape for one step while the new Deployments start ([ha-and-helm.md](ha-and-helm.md) R-HA-064) | The only gap-free path from the old shape: the edge keeps routing the host whole to the old Service until `api` and `packages` are ready; existing `web.*` values stay valid; one entry in `SubsystemsFor` | Two `helm upgrade` runs for the upgrade across the cut; a role that a fresh installation never uses |
| (b) `web` is removed | The binary refuses it; the chart refuses `web.*` with a message naming `api.*` and `packages.*` | One shape, one step | In one pass Helm points the edge at Deployments whose pods have not started and deletes `<fullname>-web`: the UI, the API and every package route answer `503` for the start time of the new pods, so R-HA-020's upgrade fails; removing `web.*` breaks R-ARCH-025's alias rule |
| (c) `web` as a permanent chart shape for small installations | A value renders one Deployment for both roles | One pod type fewer on a small installation | A second shape to render, test and drain; the trust split becomes optional; contradicts ADR-153's "each task area its own Deployment" |
| (d) `web.*` as a deprecated alias of `packages.*` | Old values configure the packages Deployment | Old values files keep rendering | The values silently mean something else (the UI on two default pods), and the gap of (b) remains |

**Choice:** (a).

**Rationale:** a Deployment cannot be renamed and an Ingress cannot switch its backend to pods that do not exist yet, so the
upgrade needs a moment in which a process serves every family behind the old Service while the new roles start beside it. That
process is `web`; keeping it costs the union of two area lists and a Deployment the chart already renders. A small installation
runs the three roles with one replica each and `highAvailability.enforce: false`, which keeps one shape for every size. The
development agent would revisit this when the owner cuts a major release (ADR-124), which may remove the bridge, or when a
release of trove no longer supports an upgrade from a release before the cut.

**Applied in the spec:** INV-2, R-ARCH-001 (the role `web`), [ha-and-helm.md](ha-and-helm.md) R-HA-002, R-HA-021, R-HA-031
(`web.enabled`), R-HA-064 (new).

### Q-ARCH-296 · The queues of the worker pools, the advisory sync's queue and the setting that restricts a process

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the queues today are `default`, `maintenance`, `index`, `delivery` and `mirror`
([internal/config/jobs.go](../../internal/config/jobs.go)), and every worker claims all of them. The three advisory-sync kinds
run on `maintenance` ([internal/app/wire_vuln_sync.go](../../internal/app/wire_vuln_sync.go), `vuln.QueueMaintenance`), whose
single default slot they share with garbage collection, the cleanups, the change-log prune and the session cleanup. Phase 6's
lesson: a full OSV sync runs for minutes (V-1: 7.1 min on the reference environment, more than 40 min on the CI runner, F-43)
and holds that slot meanwhile, so the housekeeping waits behind it. ADR-155 names pools by queue: `maintenance`, `index`,
`vuln`, `mirror`. The setting that restricts a process to queues is left to R-ARCH-016 by R-HA-062.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A queue `vuln` of its own for the three sync kinds, one slot by default; `jobs.queues` (claim only these) and `jobs.exclude_queues` (claim all but these), mutually exclusive (chosen) | The sync never takes the `maintenance` slot; a pool can claim `vuln` alone; the chart sets `jobs.queues` on a pool and `jobs.exclude_queues` on the main worker | The housekeeping no longer waits for a sync; the main worker claims every queue no pool lists, including one a later release adds, without a value | One more queue and one more goroutine per worker |
| (b) Keep `maintenance`, raise its default slots to 2 | No new queue | Smaller change | Two long jobs (a sync and a full collection) still fill both slots; a pool for the sync alone is impossible |
| (c) The sync on `default` | Shares the four API-operation slots | No new queue | Competes with rescans and evaluations, which a sync itself enqueues |
| (d) Restrict a process with zero slots (`jobs.workers.<queue>: 0`) | The existing minimum of 0 | No new key | A pool must zero every other queue, and a queue a later release adds gets its default slots on the pool, which then claims it unasked |

**Choice:** (a). `vuln-evaluate` and `vuln-rescan` stay on `default` and `vuln-findings-retention` on `maintenance`: the
evaluation is the work a sync causes, not the sync, and the retention is housekeeping.

**Rationale:** the slot of `maintenance` is one on purpose, so that housekeeping never runs in parallel with itself; a sync that
runs for minutes is a different kind of work and belongs in a slot of its own. A deny list for the main worker is what makes
"every queue that no pool lists" hold without the chart knowing the queues of a later release. The development agent would
revisit the slot count if a sync and an import (`mode: repository`) need to run side by side, and the queue if the evaluation
turns out to need isolation from the API operations as well.

**Applied in the spec:** R-ARCH-016 (the queue table, `jobs.workers.vuln`, `jobs.queues`, `jobs.exclude_queues`, the kinds,
the criteria); [vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-004 (the queue);
[ha-and-helm.md](ha-and-helm.md) R-HA-062 (the worker pools).

### Q-ARCH-304 · How a list-valued key reaches the process through its environment variable

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-HA-062 gives a worker pool `TROVE_JOBS_QUEUES` and the main worker `TROVE_JOBS_EXCLUDE_QUEUES`, both lists. Verified
in the code: [internal/config/value.go](../../internal/config/value.go) (`parseText`) decodes the text of an environment
variable for a slice or map key as one YAML document into the key's type, so `["index","vuln"]` and `[index, vuln]` are lists
and a bare `index` or a comma-separated `index,vuln` is refused with "want JSON", naming the key. The same rule holds for every
list key (for example `vulnerabilities.sources.osv.ecosystems`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the loader's form: one JSON list in the variable (chosen) | `TROVE_JOBS_QUEUES='["index"]'`; the chart renders it with `toJson` | No new rule; one form for every list key, also for lists of structures; a name with a comma cannot be misread | A hand-written variable needs the brackets and quotes |
| (b) Accept a comma-separated text for lists of strings | `TROVE_JOBS_QUEUES=index,vuln` | Easier to type | A second form that only lists of strings have; the loader has to guess between the two; changes the meaning of text that is a valid YAML scalar today |
| (c) One variable per element | `TROVE_JOBS_QUEUES_0=index` | Flat | Gaps and order rules; no other key works so |

**Choice:** (a).

**Rationale:** the loader's form is the one every list key already has, the refusal names the variable, and the chart can
produce it without a rule of its own. The development agent would revisit it if operators set many list-valued keys by hand
in container environments, which would justify (b) for every list of strings at once.

**Applied in the spec:** R-ARCH-016 (the settings); [ha-and-helm.md](ha-and-helm.md) R-HA-062 (the value of the variables);
[internal/config/jobs_test.go](../../internal/config/jobs_test.go) (`TestJobsQueuesFromTheEnvironment`).

### Q-ARCH-305 · Which rows a restricted worker claims, and in whose slots a row of a moved kind runs

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the claim query filters by queue (`$1`) and by kind (`$2`). Before this change `$2` held every registered kind whose
own queue had at least one slot. With `jobs.queues`, a kind's queue may not be among the process's queues, and a kind that
a release moves to another queue (the sync, from `maintenance` to `vuln`) leaves rows of the old queue behind.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `$1` is the process's queues with a slot; `$2` is every registered kind; a row runs in the slots of its row's queue (chosen) | The queue decides which rows a process takes; the registry decides which it can run | A row of the old queue of a moved kind is run by any process that claims that queue and has the handler, with the slots of that queue; no kind list to keep per process | A kind whose own queue has no slots on this process still runs from a row of a queue that has |
| (b) `$2` only the kinds whose own queue the process claims | A moved kind's old rows wait for a process that claims the new queue | Rows always run in the queue of their kind | A row of an old release's queue waits for a pool that may not exist (a pool claims `vuln` only when configured), and the housekeeping queue's process never drains it |
| (c) Rewrite the old rows by a migration | The release that moves a kind updates its queued rows | One queue per kind at all times | A migration per move, immutable on main (R-ARCH-026), and it does not help a mixed-version window |

**Choice:** (a). `Worker.Start` still refuses a kind whose queue is not one of the release's queues.

**Rationale:** the roadmap's open fact, whether a row of a moved kind runs in the slots of its row's queue, is answered by the
claim itself: `perQueue` counts by the row's `queue` column, so the slot of the queue it waited in is the one it takes. The
development agent would revisit it if a kind gets a handler that must not run outside its own queue's concurrency limit.

**Applied in the spec:** R-ARCH-016 (the moved kind); [internal/jobs/worker.go](../../internal/jobs/worker.go) (`Worker.Start`,
`resolveQueues`); `TestJobQueues` in [test/integration/coordination/queues_test.go](../../test/integration/coordination/queues_test.go).

### Q-ARCH-297 · Which replicas share a remote repository's fetch budget after the cut

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-040 shares `limits.maxConcurrentFetches` out as
`ceil(budget ÷ n)` per replica, n being the live replicas "that serve client traffic (`web`, or `all`)" (Q-REPO-8, Q-REPO-97).
After the cut, client misses and shadowing probes are filled on `packages`; `api` sends the upstream test of R-REPO-024 and
nothing else (Q-ARCH-294); a mirror pass runs on a `worker` within its share B (R-REPO-055). Verified in the code: the upstream
test takes no fetch slot ([internal/proxy/upstream/probe.go](../../internal/proxy/upstream/probe.go),
[internal/repository/upstream.go](../../internal/repository/upstream.go)), so the expectation that it takes one does not
match what is built.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) n counts the live replicas of `packages`, `web` and `all`, their pools included; the upstream test takes no slot, as built (chosen) | The shares go to the processes that fill | The whole budget serves client fills; the bound is `maxConcurrentFetches + n − 1` plus the upstream tests running at that moment | An administrator's test can add one connection above the bound for its duration |
| (b) n counts `api` as well, and the upstream test takes a slot of `api`'s share | Every process that can contact an upstream holds a share | The bound holds for tests too | `api` holds shares it almost never uses: with a budget of 64, three `packages` and two `api` replicas, each `packages` replica gets 13 slots instead of 22 |
| (c) n counts `packages` only, and the test takes a slot of a `packages` share | — | — | Impossible: the test runs on `api`, which has no share |

**Choice:** (a).

**Rationale:** the budget protects an upstream from the bursts of client fills; one GET of the probe resource per administrator
action, bounded by the management API's rate-limit classes (R-API-016), is not such a burst. The development agent would revisit
this if an operation that tests many remotes at once appears, which then takes slots of a share of its own.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-040 (n and the test), R-REPO-055 (the
client-serving process is `packages`, the one that claims `mirror` a `worker`);
[operations-and-observability.md](operations-and-observability.md) R-OPS-003 (the count the registry gives).

### Q-ARCH-298 · How a role restricts the routes of an endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: an endpoint's ServeMux registers only the families of its `surface` and answers every other path with the `404` of
an unknown path ([endpoints.md](endpoints.md) R-EP-005, `NewEndpointRouter` and `CheckSurface` in
[internal/httpserver/surface.go](../../internal/httpserver/surface.go)). A role now serves only some families, on the default
endpoint and on further ones alike.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The effective surface is the endpoint's surface intersected with the role's families; a listener opens only when the intersection is not empty (chosen) | The router and the route-table test take the intersection as they take the surface today | One mechanism: a family the role does not serve is a family the endpoint does not serve, with the same `404`; the route-table test proves what each role registers | A pod no longer opens an endpoint whose families its role does not serve, which the chart must know (R-EP-008) |
| (b) A middleware per role that answers `404` for the other families | The router keeps every family | No change to the router | Two filters; the route-table test no longer shows what a role serves; the other role's handlers are built and reachable through a bug in the middleware |
| (c) A router per role written in code | Each role registers its own routes | Explicit | Duplicates the surface mechanism and the family classification |

**Choice:** (a).

**Rationale:** the families already partition every route, and the surface filter already answers a family that is not served
exactly as an unknown path. Intersecting with the role adds one set and keeps one place that decides what a listener serves. The
development agent would revisit this if a family ever has to be served by two roles with different handlers.

**Applied in the spec:** R-ARCH-001, R-ARCH-024 step 9; [endpoints.md](endpoints.md) R-EP-002, R-EP-005, R-EP-008.

### Q-ARCH-299 · How phase 6a is cut into work packages

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: phase 6a changes the composition root, the job queue's claim, the chart, the registry and status, the UI's content
transfers and the end-to-end profile. Several builders work in parallel; R-ARCH-001, R-ARCH-016, R-HA-002, R-HA-037, R-HA-042,
R-HA-062 and R-HA-064 are the contract between them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Five work packages along the layers, meeting at the role names, the family table, the queue names, the chart's object names and the values (chosen) | The binary's roles; the worker queues; the chart; operations and the UI; the tests and the dev profile | Each owns its directories; the binary and the chart can be built side by side against the names this page and ha-and-helm.md fix | The e2e package waits for the other four |
| (b) One work package per role | `api`, `packages`, `worker` | Follows the roles | Every role edits `roles.go`, `steps.go`, the chart's Deployment template and the values: the packages collide in every file |

**Choice:** (a), with the work packages and their dependencies in [coverage/roadmap.md](../coverage/roadmap.md#phase-6a--task-areas).

**Rationale:** the layers already separate the concerns, and the names that cross them are fixed in the spec before any of
them is built. The development agent would revisit the cut if WP6a.1 has to change an environment variable or a label the
chart already renders.

**Applied in the spec:** [coverage/roadmap.md](../coverage/roadmap.md#phase-6a--task-areas) (WP6a.1 to WP6a.5, E6a.1 to E6a.8).

### Q-ARCH-300 · How an area is built with or without a part of another area under typed wiring

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-294 has `packages` build the authentication stage without the session store and `api` build the virtual
engine without the remote engine. Typed wiring (Q-ARCH-209) gives a step exactly the results of the areas it lists, and
`planSteps` refuses a step whose input comes from an area the role does not enable. Verified in the code:
`wireAuth` took `sessionsArea` as a required input ([wire_identity.go](../../internal/app/wire_identity.go)), and
`auth.Options.Sessions` already means "nil: the replica has no session store" and ignores a session cookie then
([internal/auth/stage.go](../../internal/auth/stage.go)); `wireVirtualEngine` took the remote engine in its input check.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A step marks an input optional (`withOptional`) (chosen) | The role that enables the other area hands the step its result and `planSteps` still checks the order; a role that does not hands it the zero value. The step decides what the absence means and the role's areas decide whether it may be absent | Typed wiring stays: no global lookup, no nil that is not a decision of the role; the step list shows the optional inputs; the role-table test names them | One more option on `newStep` |
| (b) Two steps per area, one per variant | `auth` and `auth-tokens` with different result types | No optional input | Two areas for one stage, a role table with two names for one column, and every consumer takes one of two types |
| (c) The step reads the part with `lookup[T]` from the wiring | No signature change | Smallest change | `planSteps` no longer proves the order; the step can run before the area it reads, and a nil becomes a quiet degradation, the thing Q-ARCH-209 removed |
| (d) The `sessions` area on `packages` with a nil store | Every role builds the area | Both stages from one signature | `packages` builds `sessions`, which R-ARCH-001 forbids, and a test that proves the trust split by construction cannot be written |

**Choice:** (a).

**Rationale:** the cut is a statement about what a role builds, so what a step may leave out has to be a statement about the
role too. With (a) the stage on `packages` is built from the token service, the failure limiter and no session store, and
`wireAuth` requires the store exactly when `a.sub.Has(AreaSessions)`; the virtual engine takes the engine when the role has
the `proxy-engine` area and the read-only view when it does not. The development agent would revisit this if a third step
needs an optional input in a way the zero value cannot express (a part that has two variants, not one and none).

**Applied in the spec:** R-ARCH-001 (the wiring areas, the optional input); [internal/app/steps.go](../../internal/app/steps.go);
[developer/architecture.md](../developer/architecture.md) ("How to add a wiring step").

### Q-ARCH-301 · Which operations reach the engines on `api`, and what the files seam reads there

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-294 left two facts open. Verified in the code: (1) the remote engine (`proxyEngineArea`) is taken by
`wireVirtualEngine` and `wireFormatHost` only, and the virtual engine reaches the management API through
`resolutionExplainer` only, which serves `GET /api/v1/repositories/{repoKey}/resolution`; no other operation takes an engine
(`wirePackagesAPI` took the format host in its input check only, `cacheAPI` takes the invalidation set, the repository
operations the snapshot). The explanation asks the remote members `Cached` and `EntryState` (and `Peek` through the seam)
([internal/virtual/explain.go](../../internal/virtual/explain.go)). (2) The `files` area builds a seam, and the serve check of
the item store calls the format's `ServeCheck` with the Host, which reads the stored marks through `FormatData` only
([formathost/hooks.go](../../internal/formathost/hooks.go), [formats/npm/servecheck.go](../../internal/formats/npm/servecheck.go)).
The seam took the Host from the format host, which `api` does not build; the content routes of R-API-025 (WP6a.4) call it, so `api` gets the serve-check host of `wireServeCheckHost` ([wire_files.go](../../internal/app/wire_files.go)), and the routes answer on `api` and `web` and are an unknown path on `packages`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The read-only view `proxy.View` for the explanation, and a Host without engines for the seam, both on the roles that lack the format host (chosen) | `proxy.NewView` shares the engine's readers and has no method that fetches; the `files` area sets a `formathost.New` without `Engine`, `Resolver` or dispatcher where `formathost` is not enabled | `api` has no fill path by type; the serve check works for every format; nothing changes in the format contract | A second construction site of the Host |
| (b) Build `proxy-engine` and `formathost` on `api` and register no route that calls them | Smallest change | No new type | The property of Q-ARCH-294 becomes a route list again |
| (c) Change `ServeCheck` to take the data port instead of the Host | No Host on `api` | No second Host | Changes the format contract (R-ARCH-008, ContractVersion) for a seam that only `api` meets |

**Choice:** (a). The Host of the seam answers a call that fetches, resolves or writes for a protocol with the error of a Host
built without that part (`unsupportedRemote`, `unsupportedResolver`); no route that makes such a call exists on the role.

**Rationale:** (a) keeps R-ARCH-001's two MUST NOTs true as properties of the wiring and of the types. The development agent
would revisit it when an operation of the management API other than the explanation needs the remote engine (a bulk
"warm this package" operation, say): that operation belongs on `packages` or on a `worker`, not on `api`.

**Applied in the spec:** R-ARCH-001 (the MUST NOT list of `api`); [developer/proxy.md](../developer/proxy.md) and
[developer/virtual.md](../developer/virtual.md) (the view).

### Q-ARCH-302 · A route of a family the process does not serve is not reachable without a credential

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-EP-005 and R-ARCH-001 say a request of a family the role does not serve answers the `404` problem of an unknown
path, so that nothing tells an outside audience which routes a process serves. Found by the integration test of the roles:
the authentication stage lets a request without a credential pass as anonymous on the routes that are always open (the UI
shell, the login tree, the CI exchange, npm's login start, `/`) and on the public operations, before the router. On an
endpoint that defers to the anonymous switch, with the switch off, `GET /does-not-exist` answers `401` and `GET /ui/` on
`packages` answered `404`: the pair tells the caller that `packages` has no UI.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The stage asks the endpoint whether the process serves the route's family (`EndpointRules.Served`, `httpserver.Endpoint.ServesRequest`); an unserved route is neither always open nor public (chosen) | The request is decided like an unknown path: the anonymous switch, then `401` or the router's `404` | One rule at the place that makes the exception; the answer is the unknown path's by construction | The stage learns one more input |
| (b) Answer an unserved family `404` before authentication | The chain short-cuts | No stage change | `GET /does-not-exist` answers `401` and `GET /ui/` `404`: the difference stays, in the other direction |
| (c) Leave it | — | — | The `404`/`401` pair reveals the role, against R-EP-005 |

**Choice:** (a). The exchange routes (`isExchangeRoute`) are covered too: a `POST` to an exchange path on `packages` carries an
`Authorization` header that the stage then evaluates as any credential.

**Rationale:** the exception "this route needs no credential" is true only where the route exists. The development agent
would revisit this if a family ever has to be reachable without a credential on a process that does not serve it, which
nothing in the family table suggests.

**Applied in the spec:** R-ARCH-001 (the `404` bullet and its acceptance criterion); [endpoints.md](endpoints.md) R-EP-005,
R-EP-010; [internal/auth/stage.go](../../internal/auth/stage.go), [internal/httpserver/surface.go](../../internal/httpserver/surface.go).

**Extended by Q-ARCH-306.** The decision covered the routes that are open without a credential. The security review of phase
6a found the other path rules of the chain that answered an unserved family otherwise than `/does-not-exist`: the headers,
CORS answers and rate-limit classes of the management API, the session cookie's path rule (an ended session's cookie on
`/files/x` answered `session_expired` on `api`), the Basic challenge of `/files/` and the package protocols, `error="invalid_token"`
of `/api/v1/`, and the hard limit on an unsafe anonymous request to `/api/v1/`. Q-ARCH-306 applies the same question to them.

### Q-ARCH-303 · The replica count of the limiters per role and pool, and the Bastion's forward marking on `packages`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: two facts left open by WP6a.1. (1) `replicaCount` divided the fallback share of every limiter by the live `web`
replicas of the pool and every `all` replica (`EndpointsCountServingReplicas`); with `api` and `packages` in the same pool
(the main one) that count would add the replicas of the other role, whose clients are different. The same count fed the
proxy's fetch share (`Replicas: lim.replicas.Get`), while R-REPO-040 shares the budget over every replica that fills
(Q-ARCH-297). (2) Whether the forward marking of the Bastion (`bastionServices`, `bastionStages`) needs an area that
`packages` does not build. Verified in the code: the recognition stage needs the token service and the failed-credential
limiter, the marking stage the decision point and the metrics, the four interface services the database, the settings store
and the audit recorder; all are areas of `packages`. The interface routes (`/api/v1/bastion/…`) are in the family `api` and
are dropped from the router of `packages`; the forwards are `packages` and `files` requests.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The limiters divide by the live replicas of their own role in their own pool, plus every `all` replica; the proxy takes a count of its own (chosen) | `EndpointsCountServingReplicas` takes the role | A limiter's denominator is the replicas its clients are balanced over; the fetch share's denominator is the replicas that fill, which is not the same set once there are pools | The proxy's count is a second query, built with the registry (WP6a.4) |
| (b) One count for every client-serving replica of the pool | Keeps the query | Simple | `api` and `packages` replicas divide each other's budgets: a Valkey outage halves the limits of a role that has nothing to do with the other |
| (c) One count per limiter | Each limiter counts what it limits | Exact | Many counters for the fallback of a fallback (Q-ARCH-147 removed them) |

**Choice:** (a) for the limiters, built; the Bastion's marking needs no area `packages` lacks, so nothing changes there. The
count of the replicas that fill (`packages`, `web`, `all`, pools included) for the proxy's fetch share is Q-ARCH-297 and is
built with the registry in WP6a.4: `cluster.FillCount` is started as a component of the remote engine and its `Get` is the engine's
`Replicas` ([wire_proxy_engine.go](../../internal/app/wire_proxy_engine.go), Q-OPS-165), so a deployment with pools spends the budget once.
The gap this closed is H-519 of [security/egress.md](../security/egress.md), removed there.

**Rationale:** the denominator of a share has to be the set the share is shared over. The development agent would revisit it
if a limiter guards a resource that `api` and `packages` replicas both consume (a shared upstream), which then counts both.

**Applied in the spec:** R-ARCH-034 (the fallback bullet reads "live replicas of the role in the replica's pool");
[operations-and-observability.md](operations-and-observability.md) R-OPS-003 (the count the registry gives);
[bastion.md](bastion.md) R-BAS-044 (`bastion.main.packages_url`, built).

### Q-ARCH-306 · Every path rule of a chain stage applies only to a family the process serves

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-EP-005 and R-ARCH-001 say a request of a family the process does not serve is an unknown path, and Q-ARCH-302 made
the routes that are open without a credential ask whether the family is served. The security review of phase 6a found the
other rules of the chain that a path selects and that never asked: on `packages`, a `404` for `/api/v1/x` carried
`Cache-Control`, `Content-Security-Policy` and `Referrer-Policy` (R-API-017) and `RateLimit-Policy` and `RateLimit` (R-API-016),
and with `api.cors.allowed_origins` set, `OPTIONS /api/v1/x` answered the `204` of a preflight (R-API-018); on `api`, `/files/x`
and `/api/<format>/x` carried the rate-limit headers, answered a package client's missing credential with the Basic challenge
where `/does-not-exist` answers Bearer, and answered an ended session's cookie with `401 session_expired` where
`/does-not-exist` ignores the cookie (`credentials_required`, or the anonymous pass and the `404`). A rejected token on
`/api/v1/x` on `packages` got `error="invalid_token"` in the challenge, and with anonymous access on an unsafe request to
`/api/v1/x` on `packages` got the `401` of R-AUTH-037's hard limit where `POST /does-not-exist` gets the `404`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One question per request, asked by every path rule: `httpserver.Endpoint.ServesRequest` of the listener's endpoint; the authentication stage reads its rules over the path of `rulePath`, `""` for an unserved family; the preflight and stage 7 ask `familyServed` (chosen) | An unserved family meets no rule of its own family in any stage | The answer is the unknown path's by construction, in every stage; the family table stays in one place (`surface.go`) | Each stage that adds a path rule has to ask; the integration test of R-ARCH-001 is what catches one that does not |
| (b) Answer an unserved family `404` before every stage | One short-cut at step 1 | No rule needs to know | `GET /does-not-exist` answers `401` without a credential on an endpoint without anonymous access: the difference stays, in the other direction (Q-ARCH-302 (b)) |
| (c) Rewrite the request path of an unserved family to `/does-not-exist` before the stages | The stages see an unknown path | No stage changes | The request log, the metrics label and `instance` would name a path the client did not send; the router would lose the path it needs for its own `404` |

**Choice:** (a). The stage's rules read the empty path for an unserved family: no exchange route, no always-open route, no
public operation, no path rule of the session cookie (so an ended session's cookie is ignored as on an unknown path), no hard
limit of `/api/v1/` (so an anonymous unsafe request passes the stage as on an unknown path and meets the router's `404`, which
runs no handler), and the challenge of an unknown path. The preflight, the headers of the management API, CORS and the general
rate-limit classes apply where the endpoint serves the family. The endpoint's own class (R-EP-015) and the in-flight limit apply
to every path, an unknown one included, and stay. A request of an unserved family is limited no more than an unknown path is;
the router answers both without a handler.

**Rationale:** a rule of a family is a property of the family where it exists; elsewhere the request belongs to no family the
process has. The development agent would revisit this if a rule had to hold for a family on a process that does not serve it,
which nothing in the family table suggests. The rules outside stages 6 and 7 that a path selects are
[H-521](#h-521--two-path-rules-outside-the-stages-of-authentication-and-rate-limits-do-not-ask-whether-the-family-is-served).

**Applied in the spec:** R-ARCH-001 (the bullet and an acceptance criterion); Q-ARCH-302 (extended);
[internal/auth/stage.go](../../internal/auth/stage.go) (`rulePath`), [internal/api/v1/ratelimit.go](../../internal/api/v1/ratelimit.go)
(`familyServed`), [internal/api/v1/cors.go](../../internal/api/v1/cors.go). R-API-016, R-API-017, R-API-018 and R-AUTH-001,
R-AUTH-002, R-AUTH-037 are read through R-ARCH-001's bullet; their pages are not edited by this change.

### Q-ARCH-307 · The content routes answer a method they do not offer like an unknown path except to the session

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-API-025 says a request to a content route that the session cookie did not authenticate answers the `404` problem
of an unknown path. The routes register `GET`, `HEAD` and `PUT`; for any other method the router's fallback probes the methods
of the path (R-ARCH-005, "wrong method → 405 + Allow") and answered a token's `DELETE` or `POST` with
`405` and `Allow: GET, HEAD, PUT`, so a token caller learned that the path is a route its `GET` had just denied. R-ARCH-005 fixes
the `405` as the router's answer for a path that exists for another method; it does not decide it for a route that hides
itself from a caller.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The route tells the router whom it hides from: `Router.HideMethods(prefix, hidden)`; the fallback answers the `404` of an unknown path instead of the `405` when `hidden` reports the request (chosen) | The content routes hide their methods from every caller the session cookie did not authenticate | R-API-025 holds for every method; the session's `DELETE` keeps its `405`; the `405` rule stays the router's | One more table in the router |
| (b) Correct the promise: a token's `DELETE` is the `405` | Spec edit only | No code | The route answers a token `404` for `GET` and `405` for `DELETE`; R-API-025's sentence becomes "for the methods it offers" |
| (c) Register every method on the content path and answer the `405` in the handler | The handler decides | No router change | `DELETE` and `POST` become registered routes the document must list (`Document.CheckRoutes`), for operations that do not exist |

**Choice:** (a). The predicate runs in the fallback after the authentication stage and reads the principal's credential method.

**Rationale:** the `404` for a token caller keeps the bulk traffic of token clients on `packages` (Q-UI-106); a `405` that names
the route's methods tells them where it is. The development agent would revisit this if the routes offered `DELETE`, which
then answers a token `404` from the handler like the other methods.

**Applied in the spec:** none beyond R-API-025, whose sentence the code now meets for every method;
[internal/httpserver/router.go](../../internal/httpserver/router.go) (`HideMethods`),
[internal/api/rawfiles/content.go](../../internal/api/rawfiles/content.go) (`Register`).

### Q-ARCH-308 · The mirror's host is built on first use; a process without the remote engine builds its own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the passes run in whatever process claims the queue `mirror` (Q-ARCH-285), and `mirror.Host` (Q-ARCH-288) needs the remote
engine and the format host. A client-serving process (`packages`, `web`, `all`) has both (R-ARCH-001); a `worker` has neither, and the
role table is a decision of its own (Q-ARCH-294) that this work package does not change.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `App.mirrorHost()` returns a host that resolves on its first call: the engine and the format host of the process when it has them, else an engine and a format host of its own, built from the areas every role has and started as components (chosen) | A pass in a client-serving process shares its replica's slots with its clients; one in a worker has its own slots | No change of the role table; the job registry, which is frozen before the areas are wired, can hold the handlers | A worker runs an engine whose slots no client uses; it is not one of the replicas that fill client misses (`cluster.FillsClientMisses` excludes it), so its share of a repository is not divided by them (Q-ARCH-317). Superseded in its wiring by Q-ARCH-316 |
| (b) Add the remote engine and the format host to the worker's areas | The worker has them at start | Plain wiring | Changes R-ARCH-001's table, which the owner is reconsidering |
| (c) The pass asks a client-serving replica to fetch for it | No engine in the worker | None to build | Q-REPO-140 (d): the background would take client slots |

**Choice:** (a). A process that cannot build the host fails every call with the same error.

**Rationale:** the host is the seam; where the engine behind it runs is the topology, which stays open. The development agent would
revisit (b) once the owner has settled the topology.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055 ("Who runs what"). Code:
[internal/app/wire_mirror_host.go](../../internal/app/wire_mirror_host.go).

### Q-ARCH-309 · Every process that runs background fetches handles `mirror_yield`, the sender included

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine subscribes to `mirror_yield` in `Start` and handles every message; the payload is the repository's numeric id, a coarse batch counts for every repository, and a replica raises the signal at most once a second per repository; the area is registered in `internal/cluster/areas.go` (chosen) | One handler for local and remote signals; the signal path delivers a replica's own signal to its local subscribers directly | Nothing to special-case; a process that serves clients and claims the queue also yields through the same step | A client fetch that waits also closes the repository to the process's own background for 30 s |
| (b) The raiser pre-empts in its own process only and publishes for the others | Two paths | The sender's window stays open for the pre-emption it did itself | Two paths to keep equal; in role `all` the local wait is also pressure |
| (c) A change area | Through the change log | Survives Valkey outages | A PostgreSQL write per second of client pressure (Q-ARCH-286 (b)) |

**Choice:** (a). The subscription failing, or no signal path, leaves the engine yielding to the clients of its own process only.

**Rationale:** a client that waits for a slot is pressure on the repository wherever the pass runs. The development agent would
revisit the sender's own 30 s if role `all` shows passes that stall for long on a busy installation.

**Applied in the spec:** R-ARCH-018 (the signal areas); [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055, R-REPO-058.
Code: [internal/cluster/areas.go](../../internal/cluster/areas.go) (`AreaMirrorYield`),
[internal/proxy/background.go](../../internal/proxy/background.go) (`raiseYield`, `onYield`, `listenYield`).

### Q-ARCH-310 · The yield window is 30 s from the last signal, read on the engine's clock and polled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each signal sets the end of the repository's window to now + 30 s; a background fetch that wants to start checks it on every wake-up and at least every 250 ms; the clock is the engine's (`Options.Now`) (chosen) | Plain state under the slots' lock; a test with a fake clock moves it | No timers to cancel; the window moves with every signal | A waiting background fetch notices the end of the window within 250 ms |
| (b) A timer per repository that releases the waiters | Exact end | No polling | Timers to stop on every signal and at shutdown |

**Choice:** (a). A coarse signal opens a window for every repository.

**Rationale:** the wait is 30 s; 250 ms is noise. The development agent would revisit it if the background waiters of very many
repositories cost measurable wake-ups.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055. Code:
[internal/proxy/slots.go](../../internal/proxy/slots.go) (`yield`, `yieldOpen`).

### Q-ARCH-311 · A format's own requests during a pass are background requests because the context carries the pass

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a format's gate (`BeforeArtifact`) revalidates the package document through `Host.Remote()`, and the formats find their
per-replica state by the identity of the `Host` they are handed (npm's `replicaOf`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The host's context carries the pass under an unexported key; the client path's wrapper over the engine reads it and builds a background request (chosen) | The format is handed the real `Host`; a request through `Host.Remote()` is the pass's | A format sees no difference and cannot set the key; its per-replica state is found | The key is read in the wrapper of the client path |
| (b) A `Host` whose `Remote()` is the pass's fetcher | An explicit object | No context value | A different `Host` identity: the format's per-replica state (metrics, settings) would be found by neither |
| (c) The format calls a `Remote().Background(...)` method | Explicit in the contract | Visible | A change of the frozen format contract for every format |

**Choice:** (a).

**Rationale:** the contract stays as it is and the rule cannot be forgotten by a format, because it never sees it. The development
agent would revisit (c) if a format needs to know that it works for a pass.

**Applied in the spec:** [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-055. Code:
[internal/formathost/mirror_host.go](../../internal/formathost/mirror_host.go) (`withBackground`),
[internal/formathost/remote.go](../../internal/formathost/remote.go) (`request`).

### Q-ARCH-312 · The mirror engine is an area of the roles worker and all, built lazily at the first job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Q-ARCH-285 puts the passes in a queue of their own run by whatever process claims it. The engine needs the repository snapshot and the database, which every role has, and a host, which needs the remote engine, which `worker` does not build today.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The area `mirror` runs on every role, registers the counters and the gauge source when it is wired (the gauges must reach the alerts whatever claims the queue, Q-OPS-171) and builds the engine when the first job of a process that claims one runs, so that the host sees every part the composition root has built; the host's `Ready` is asked at every dispatch and pass, and an error fails the job with its cause (chosen) | Counters and gauges exist exactly where the queue is claimed; the host's needs can change without moving the area | A first job pays the construction; a failing host fails that job, and the next one tries again | The area table of R-ARCH-001 has one more row |
| (b) Build the engine when the area is wired | Fail at start-up | Early errors | The host's inputs would have to precede the area in the step list, and the owner is still reconsidering the topology |
| (c) On every role | Uniform | Simple table | A `web` or `api` replica would hold an engine that nothing runs, and its counters would never move |

**Choice:** (a). The handlers of both kinds are registered by every role, because a client-serving process enqueues; a process that claims no job never builds the engine. A host that is not ready (`Host.Ready` returns the cause, for example the remote engine is not built in a role) fails the dispatch and the pass with that cause, visibly, instead of skipping the repositories (Q-ARCH-320).

**Rationale:** the queue, not the Deployment, decides where passes run. The development agent would revisit it if the pass moves to a pool that has no area of its own.

**Applied in the spec:** R-ARCH-001 (the area table below), [docs/developer/architecture.md](../developer/architecture.md). Code: [wire_mirror.go](../../internal/app/wire_mirror.go), `steps.go`. Test: `TestR_ARCH_001_RoleTableIsData`.

### Q-ARCH-313 · A pass attempt that is released or whose lease is lost writes nothing, and the fence compares uuids

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-ARCH-024 cancels the handlers of the jobs 11 s before the shutdown deadline (Q-ARCH-329) and releases them; a lost lease cancels the handler with its own cause. A handler that wrote on its way out would write under a fence it may no longer hold.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) When the job's context ends, the attempt returns the context's cause at once and writes nothing; the in-flight fetches are cancelled with it, nothing partial is stored by the engine; the next job takes the fence over. The fence columns are compared as uuids, because the instance id of a process is its 16 random bytes in hex without hyphens (chosen) | The framework's release and the fence agree; a late write cannot happen from this process | A written summary of the interrupted attempt is lost (the pending names and the counters are the state) | None for the comparison |
| (b) Write the attempt's summary on the way out | A trace of the attempt | More state | A release writes under the fence at the moment it is least certain |
| (c) Compare the text of the columns | No cast | Index-friendly on a uuid cast | The hyphen-less holder never matches its own row |

**Choice:** (a).

**Rationale:** the fence belongs to the job that holds the lease (Q-ARCH-287). The development agent would revisit it if the framework gave handlers a token to write under.

**Applied in the spec:** R-REPO-053 (drain and restart, crash and failover), R-ARCH-024. Code: `pass.go` (`run`), `queries/mirror.sql`. Tests: `TestR_REPO_053_AReleasedAttemptLeavesThePassForAnotherProcess`, integration `TestR_REPO_053_AShutdownReleasesTheJobAndASecondEngineEndsThePass`, `TestR_REPO_053_ALostLeaseIsContinuedByTheNextDispatchAndTheOldFenceWritesNothing`.

### Q-ARCH-314 · The engine's time and randomness are interfaces of its options, and a test replaces the host through the composition root

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The back-offs, the waits and the window are specified in seconds and minutes; a test that runs them in real time would run for hours.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `mirror.Clock` (`Now`, `Sleep`) and `mirror.Jitter` (`Float64`) in `mirror.Options`; `app.Options.Mirror` (`MirrorOverride`: host, clock, jitter) for the integration tests that start replicas (chosen) | A pass of hours runs in milliseconds in a unit test; the integration tests use the real job framework with a counting host | Two interfaces | A test double of the host stands where the remote engine will |
| (b) Real time, short settings | No seam | Nothing to inject | The settings have ranges (an interval of at least 300 s) |
| (c) A global clock | One switch | No option | Parallel tests share it |

**Choice:** (a).

**Rationale:** the same seam the vulnerability scanning uses for its sync. The development agent would revisit it if a clock moves into the job framework.

**Applied in the spec:** none in the spec pages beyond the test plan of R-REPO-059. Code: `policy.go`, `internal/app/wire_mirror.go`, `test/integration/testenv/replica.go` (`ReplicaOptions.Mirror`).

### Q-ARCH-315 · The engine reaches PostgreSQL through a store port with one statement per step

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-REPO-053 asks for the claim as one statement and for the delete of a pending row and the counters in one transaction; the unit tests need the semantics of the fence without a database.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `mirror.Store` with a PostgreSQL implementation (every write of a pass one statement carrying the fence, the claim and the package results as statements with data-modifying CTEs) and an in-memory one in the unit tests (chosen) | The fence and the transaction boundaries are the statements'; the engine reads as a flow; the in-memory store is checked by the integration tests against the real one | Two implementations to keep in step | The statements are long |
| (b) The engine runs SQL itself | Fewer layers | No port | No unit test without a database, and the 10,000-name and the fence cases are the ones that need many runs |
| (c) A transaction per step in Go | Plain statements | Short SQL | A round trip per statement of the claim and the package result; the fence check is separate from the write |

**Choice:** (a).

**Rationale:** the integration tests exist to keep the two stores equal. The development agent would revisit it if the statements need rows the port cannot give.

**Applied in the spec:** R-REPO-053 (the steps and the data model). Code: [store.go](../../internal/mirror/store.go), [store_pg.go](../../internal/mirror/store_pg.go), [mirror.sql](../../internal/meta/postgres/queries/mirror.sql).

### Q-ARCH-316 · The remote engine of a worker is an area of the role table

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-308 let a `worker` build an engine and a format host of its own at the first job, outside the area table (R-ARCH-001),
so that `App.Areas`, the role table test and the docs did not show it. The build also needed an invalidation set, which a worker does
not have, and failed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The area `mirror-engine` on `worker` and `all`: a builder prepared when the area is wired and run at the first call of the host; in role `all` it hands out the engine and the host the role has, in role `worker` it builds its own, including a set of cache invalidations (chosen) | The engine is in the table and its inputs are explicit | The worker's engine honours R-REPO-045 | The area table has one more row |
| (b) Add `proxy-engine` and `formathost` to `worker` | The areas of the client path | No new area | Serving areas on a role that serves nothing, and the sizes of a client engine |
| (c) Keep it outside the table | Nothing changes | None | The thing the review found |

**Choice:** (a). The step cannot fail; building can, and the first job that needs the host reports it.

**Rationale:** what a process runs must be in the table that says what a process runs. The development agent would revisit it with the
owner's decision on the topology.

**Applied in the spec:** R-ARCH-001 (the area table), [docs/developer/architecture.md](../developer/architecture.md). Code:
[internal/app/wire_mirror_host.go](../../internal/app/wire_mirror_host.go), `steps.go`. Test: `TestR_ARCH_001_RoleTableIsData`,
`TestMirrorEngineArea`, and the worker pass of `test/integration/mirror/worker_role_test.go`.

### Q-ARCH-317 · The worker's background engine has small caches and a slot limit that does not divide

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine of a client replica has a 128 MiB metadata cache and 100,000 negative entries, and divides the repository's budget
by the replicas that fill client misses (R-REPO-040). A worker is not one of them, and its limit is 1 GiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 16 MiB of metadata bytes, 10,000 negative entries, no query cache, as upper bounds on the configured values; no division of the budget (`Replicas` is one): the repository's share on the worker is the budget, the background share B is the ceiling (chosen) | Fits in the worker's limit; B is reachable whatever n is | Constants, no new keys | A mirror of very large documents misses the cache more |
| (b) New configuration keys for the worker's engine | Tunable | Operators decide | Keys for a mechanism that is not asked for yet |
| (c) The client-sized defaults | Nothing to decide | None | 128 MiB and 100,000 entries per worker for a process that serves nobody |

**Choice:** (a). The replica limit `proxy.max_concurrent_fetches` still applies.

**Rationale:** a pass reads each document about once; the caches are for repeated hits. The development agent would revisit it if
the pass's documents show hit rates that a larger cache would raise.

**Applied in the spec:** R-REPO-055. Code: [internal/app/wire_mirror_host.go](../../internal/app/wire_mirror_host.go) (`backgroundProxyConfig`),
[deploy/helm/trove/values.yaml](../../deploy/helm/trove/values.yaml) (the comment of the worker's resources). Test: `TestMirrorEngineArea`.

### Q-ARCH-318 · The job worker is quiesced before the engines, and a request the shutdown cancelled stops the pass

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the shutdown quiesces the components in reverse start order (R-ARCH-024 step 4). The remote engine starts after the job
worker, so it was cancelled first: every fill returned `shutting_down` while the job's context was live, and the pass recorded
failures.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A component that marks itself `QuiescesFirst` (the job worker) is quiesced first and waited for; the others keep the reverse order; the host also maps the engine's cancelled request to `mirror.ErrShuttingDown` (chosen) | The engines are cancelled after the work that uses them is released; a straggler is still not a failure | Two layers | A marker interface |
| (b) Start the engines before the worker | The reverse order does it | No new interface | The start order is constrained by this one rule |
| (c) Only the mapping | The engine stops the pass on `ErrShuttingDown` | Smallest | Every other job that runs on the engines still races |

**Choice:** (a). `Ready` answers `ErrShuttingDown` while the engine drains, so no pass starts in a process that shuts down.

**Rationale:** a release and a cancel are different outcomes for a job, and the first must come first. The development agent would
revisit it if another component needs the same.

**Applied in the spec:** R-ARCH-024 (step 4). Code: [internal/app/app.go](../../internal/app/app.go) (`quiesceOrder`),
[component.go](../../internal/app/component.go) (`QuiescesFirst`), [internal/jobs/worker.go](../../internal/jobs/worker.go). Test:
`TestQuiesceOrder`.

### Q-ARCH-319 · The host is built in stages that are kept, and a build error is retried

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: Q-ARCH-312 says that a failing host fails that job and the next one tries again. The first build kept its error behind a
`sync.Once`, and a second attempt would have registered the engine's caches twice (a panic).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The builder keeps what succeeded: the flight lock, the invalidation set, the engine, its start, the format host; an error is returned by `Ready` and the next call continues with what is missing (chosen) | A retry never builds a stage twice | `Ready` is the cause the dispatch and the pass report | A mutex and four fields |
| (b) Keep the error forever | Simple | None | Q-ARCH-312's retry is gone until a restart |
| (c) Rebuild everything on each attempt | Simple | None | The caches and the metrics register once per registry |

**Choice:** (a).

**Rationale:** the stages that register things cannot be repeated, so they are remembered. The development agent would revisit it if a
stage is found that cannot be completed after a partial failure.

**Applied in the spec:** R-REPO-053. Code: [internal/app/wire_mirror_host.go](../../internal/app/wire_mirror_host.go) (`mirrorHostBuilder`).
Test: `TestMirrorEngineArea`.

### Q-ARCH-320 · A request that the shutdown cancelled stops the pass without a write, and a host that is not ready fails the job

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The seam of the engine now names `ErrShuttingDown` (a request the process's shutdown cancelled) and `Host.Ready` (whether the host can serve passes in this process). The engine counted the first as a failed item and skipped every repository on the second.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ErrShuttingDown` halts the pass as if the job's context had ended: nothing is written, no item counts, the attempt waits for the framework's release (a minute at most) and returns its cause, so that the job is released with its attempt unchanged; `Ready` is asked at every dispatch and pass and an error fails the job with the cause and is logged (chosen) | A drain loses no attempt and no counter; a role that cannot serve passes says so at the job | The attempt waits for the framework's release (the cancel comes 5 s before the deadline) | A dispatch that cannot serve is a failed job once a minute |
| (b) A failed item | The merged behaviour | No change | A shutdown writes failures and burns attempts of packages that were fine |
| (c) Skip repositories while the host is not ready | Quiet | No failed jobs | A role that never serves passes looks healthy and nothing runs |

**Choice:** (a).

**Rationale:** the fence belongs to the job that holds the lease (Q-ARCH-287, Q-ARCH-313). The development agent would revisit the minute if the framework released handlers itself on a signal.

**Applied in the spec:** R-REPO-053 (drain), Q-ARCH-312, Q-ARCH-313. Code: `request.go`, `end.go` (`shutdownEnd`), `engine.go`, `pass.go`. Tests: `TestR_REPO_053_AShutdownErrorWritesNothingAndFailsNoItem`, `TestR_REPO_053_AHostThatIsNotReadyFailsTheDispatchAndThePass`.

### Q-ARCH-321 · The dispatch runs on the maintenance queue

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The dispatch and the passes shared the two slots of the queue `mirror`; two passes of hours held both, and the one-minute task that enqueues the next passes (and so a pass after a failure, a deferral or a crash) waited behind them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `mirror-dispatch` is registered on `maintenance` (`mirror.QueueDispatch`), `mirror-pass` stays on `mirror` (chosen) | The dispatch always has a slot; Q-ARCH-305 lets a kind move | A process that claims `mirror` alone does not run the dispatch; the main worker does | The dispatch shares `maintenance` with the collector and the retention tasks (a minute's task) |
| (b) A third slot on `mirror` reserved for it | Same queue | No move | A reserved slot is a framework feature that does not exist |
| (c) Leave it | No change | None | A busy mirror stops scheduling its own passes |

**Choice:** (a).

**Rationale:** the scheduler must not wait for the work it schedules. The development agent would revisit it if the framework reserved slots.

**Applied in the spec:** R-REPO-053 (dispatch), R-ARCH-016 (the queue table and the task row). Code: [mirror.go](../../internal/mirror/mirror.go), [wire_mirror.go](../../internal/app/wire_mirror.go). Test: `TestR_REPO_053_TheDispatchRunsOnTheMaintenanceQueue`.

### Q-ARCH-322 · The engine imports no proxy package: the background share comes through its options and the status of a rate limit through an interface

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The engine imported `internal/proxy` for `BackgroundShare` and `internal/proxy/upstream` for the status of a rate-limit error, which tied the engine to the remote engine's package although the seam (`mirror.Host`) exists to prevent that.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Options.Share func(budget, concurrency int) int`, bound by the composition root to `proxy.BackgroundShare`, which keeps the one definition; the status is read through `interface{ HTTPStatus() int }` on the error; an architecture rule bans `internal/mirror` from `internal/proxy` (chosen) | One formula; the engine and the remote engine meet only at the seam | A required option; a status of 0 until the error implements the method | A test double of the formula in the unit tests |
| (b) Copy the formula into the engine | No option | Simple | Two definitions that must stay equal |
| (c) Move the formula to the leaf `discover` | Both import it | No option | The frozen contract grows by a function that is not about discovery |

**Choice:** (a).

**Rationale:** a boundary kept by a rule is kept. The development agent would revisit it if the share became a property of the host.

**Applied in the spec:** R-REPO-055 (the share), R-ARCH-002. Code: `engine.go`, `internal/archtest/imports.go`. Tests: `TestR_REPO_054_TheStatusOfARateLimitComesFromTheErrorsHTTPStatus`, the module-ban test of `internal/archtest`.

### Q-ARCH-323 · A wait that ends the attempt halts the window before it is visible, and a sleeping worker wakes with the halt

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A rate-limit answer with a wait over 300 s ends the attempt. The workers that were blocked behind the window woke after the answer, saw the new wait and slept it on the clock, past the halt that came after the state write: the job kept its slot, held by sleeping workers, for the length of the wait.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The throttle halts itself, under its lock, in the call that starts a wait over `LongWait`, before any worker can see the wait; a worker that sleeps a wait sleeps it with a context that the halt cancels (chosen) | No worker can sleep a long wait; a halt for any reason wakes the sleepers | A goroutine per sleep | None |
| (b) Only the pass-level halt | The merged behaviour | No change | A race between the answer and the write that holds the slot for hours |
| (c) Sleep in short slices and look | No goroutine | Simple | Polling, and a slice of lag |

**Choice:** (a).

**Rationale:** R-REPO-054 promises that no job slot is held through a long wait. The development agent would revisit it if the clock offered a wake-up channel.

**Applied in the spec:** R-REPO-054 (a long wait ends the attempt). Code: `throttle.go`. Test: `TestR_REPO_054_ALongWaitReleasesEveryWorkerAtOnce`.

### Q-ARCH-324 · How is one leg of the nightly run dispatched?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `HA and Performance (nightly)` has eleven jobs and sixteen legs. A manual dispatch ran all of them, 38 to 53 minutes and up to six kind clusters at once, so a look at one leg paid for the rest. The owner asked for a dispatch that names the legs, and the schedule keeps running everything. GitHub expressions have no split function and no `matrix` context in a job's `if`, so the filter needs a step in each matrix job, or a matrix built by a job before it.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One input `sets`, a comma-separated list of set names or job keys; a job-level `if` for the jobs without a matrix; in each matrix job a first step (`.github/actions/select-leg`) whose output conditions every later step (chosen) | `gh workflow run "HA and Performance (nightly)" --ref main -f sets=upgrade,ha-suite` runs two legs | One input for every leg, decided in one place; the matrix keeps its entries, names and ports; actionlint checks the file as it did | A matrix job whose legs are all dropped still starts its runner pod for the seconds of checkout and the decision; every step of a matrix job carries one more condition |
| (b) One boolean input per leg | One checkbox per leg in the Actions form | Typed by the form | One input per leg to keep in step with the matrix; `gh` needs one `-f` per leg; a new leg is a new input |
| (c) A first job that emits the matrix with `fromJSON` | The matrix holds the selected legs only | Only the selected legs start a pod | The entries leave the file, so actionlint cannot check them; one more job before every leg |
| (d) One workflow file per leg or per group | Separate files with their own dispatch | Each file reads on its own | The e2e legs are several hundred lines each; copies drift |

**Choice:** (a).

**Rationale:** The names a dispatch takes are the ones the logs and the reports already carry: the sets of [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) and the job keys. A list with a space fails in every matrix leg, so the run is red and visible, because the job-level conditions compare the raw string. Verified in this change: actionlint 1.7.12 (the pin) passes [performance.yml](../../.github/workflows/performance.yml); the decision block of the action passes thirteen dispatch cases run with bash (the empty list, a single leg, a job key, a prefix and a substring that must not match, a list with a space). Not verified: a dispatch on GitHub, which runs the file of its ref, so the input exists once the change is on `main`. Revisit when the list needs a value GitHub must check (then a choice input), or when a dropped matrix job costs visible time on the large class.

**Applied in the spec:** none (CI wiring). Code: [performance.yml](../../.github/workflows/performance.yml) (input `sets`, the job-level `if`, the step `Select the leg`), [select-leg/action.yml](../../.github/actions/select-leg/action.yml). Elsewhere: [releasing.md](../developer/releasing.md#ha-and-performance-nightly).

### Q-ARCH-325 · Where do the perf jobs run relative to the kind legs?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The jobs `latency` (P-13 and P-14) and `vuln-perf` (R-VULN-028, V-1 to V-9) ran at the same time as the kind legs on the large class, which holds six pods, so each measured on a VM where the kind clusters of the same night were running. F-43 records V-4 (p99 bound 5 ms) and V-7b (300 ms) missed on some nights and met on others with the same code. Measured over the six nightly runs 41739280012 to 42126196109 (the table in [releasing.md](../developer/releasing.md#ci)): `latency` was green in six of six runs, `vuln-perf` red in six of six; V-1 did not finish in 41748522448 and 42002617340 (F-43).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Both perf jobs after every kind leg (`needs` on the nine kind jobs, `if: !cancelled()`), `vuln-perf` after `latency`, so no perf job shares the VM with a kind cluster or with the other perf job (chosen) | The nightly ends with the longest kind leg, then the two perf jobs | The perf jobs measure without this workflow's clusters beside them; one `needs` list per job | About 13 minutes more per night: 5 min 17 s for `latency` and 7 min 22 s for `vuln-perf` in 42126196109, which ran beside other jobs; the nightly ends later |
| (b) Status quo: in parallel with the kind legs on large | No change | No wall time | The load of our own clusters stays in the measurement; F-43's misses are not explained |
| (c) GitHub-hosted `ubuntu-24.04` for the perf jobs | Each job gets its own VM, no CPU limit | Free for the public repository; no wall time | Measured: `vuln-perf` V-4 at 7.7 ms missed (41902592855) and 2.98 ms met (42002617340); V-7b 329 ms missed and 205 ms met; the hosted runs miss for the same reason as the large run (V-4 9.7 ms in 42126196109), so no gain is measured |
| (d) A concurrency group over the large jobs, one at a time | Every large job waits for the one before | Nothing else of ours shares the VM | The kind legs would run in sequence, about two hours instead of 50 minutes |
| (e) A runner class of its own for the perf jobs | A third self-hosted class, dedicated | The measurement shares nothing | The organisation has the small and large classes only; a class is the owner's decision to provision |

**Choice:** (a).

**Rationale:** The one load that is ours and under our control is the neighbour of the perf job, and (a) removes it. It does not claim to remove the variance of the VM. In 42126196109 `vuln-perf` ran beside the other large legs and missed V-4 (9.7 ms) and V-7b (340 ms). In the two hosted runs it ran beside no job of ours and still missed V-4 in one of two runs, so the data does not show that our neighbours are the whole cause. Verified: actionlint passes; the `needs` lists of both perf jobs name all nine kind jobs, the new `e2e-upgrade` among them. Not verified: the wall time of the new order and the V-4 result under it (no nightly run has them yet), and whether a Test and Release run or another repository of the organisation shares the large VM at the same time; the order removes this workflow's clusters from the perf jobs' neighbours and nothing else. Revisit after five nightly runs in the new order: if V-4 or V-7b still misses in each of them on large, the bound is read against the reference environment ([H-491](../security/vulnerability-scanning.md#h-491--the-performance-run-is-not-the-reference-environment)) or option (e) is asked of the owner; if the perf jobs pass in all of them, (a) stays.

**Applied in the spec:** none (CI wiring). Code: [performance.yml](../../.github/workflows/performance.yml) (jobs `latency` and `vuln-perf`). Elsewhere: [releasing.md](../developer/releasing.md#ha-and-performance-nightly), [Q-VULN-57](vulnerability-scanning.md#q-vuln-57--the-performance-run-is-a-test-package-on-the-integration-stack-gated-and-reported-by-the-nightly-workflow).

### Q-ARCH-326 · Which runner class runs which leg? The runner review of the nightly run

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The nightly legs ran on three assignments in six runs: the CPU-limited self-hosted pods (41739280012, 41748522448, 41804996351), GitHub-hosted runners for every job (41902592855, 42002617340), and the assignment of the owner's note of 2026-11-05 (42126196109: large for the legs without the NuGet clients image, hosted for those with it). The table of leg by run, with the class and the result of each job, is in [releasing.md](../developer/releasing.md#ci). The code changed between the runs (the NuGet dump fix of 2026-11-05 in F-43, the drain per role added in 41902592855), so the class and the code cannot be separated with six nights. Per class over the six runs: self-hosted 20 of 33 jobs green, hosted 25 of 31, large 6 of 7 (its one red job is the perf job `vuln-perf`). `npm-ha` was red in all five runs off large and green once on large.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the assignment of 42126196109 (chosen): the npm-load and drain legs on large, the legs with the NuGet clients image and the new `upgrade` on `ubuntu-24.04`, `ha-suite` on large | No change of any class | The best measured night; the NuGet image cannot go on large (its Docker storage, 30 GiB, evicted the legs in 42046280787); the one leg with a class signal (`npm-ha`) points to large | One large night is one night; `ha-suite` adds a fourth npm-load leg to the large wave, which fills the six pods of the class |
| (b) Move `npm-ha`, `npm-ha-soak` and `drain-roles` to `ubuntu-24.04` | No shared VM, no CPU limit | More CPU headroom in principle | `npm-ha` was red in both hosted runs; the hosted runner has 16 GB of RAM and 4 vCPU, against 20 GiB and 16 CPUs of the large pod |
| (c) Move the legs with the NuGet clients image to large | One class for every leg | Fewer hosted minutes | Their clients image (2.3 GB) copied into four kind nodes exceeds the 30 GiB of Docker storage of a large pod (eviction in run 42046280787) |
| (d) `max-parallel: 1` on job `e2e-npm`, so `npm-ha` and `ha-suite` run one after the other | Two npm-load legs fewer at a time | Less load on the VM | The nightly grows by the first leg's time (21 minutes for `npm-ha` in 42126196109, 27 to 53 minutes in the runs off large); no measured failure of the pair |
| (e) A small self-hosted pod for the short kind legs | Ten GiB for the runner and dind together | Cheap | The drain per role under the npm load held 9.9 GiB in Docker (measured, [releasing.md](../developer/releasing.md#ci)), which leaves no room for the runner container |

**Choice:** (a).

**Rationale:** No leg has a measured case for a move. Per leg, the only signal across classes is `npm-ha`, red in five of five runs off large and green on large once, so it stays on large. The hosted reds are one perf job, one leg lost after eleven minutes with no test run (F-40, `bastion`, 42002617340), one leg whose tests all failed behind one `EOF` of the in-cluster npm test upstream during setup (`vuln`, 42002617340, not classified further), and `npm-ha` (two). The small class cannot carry a kind leg (option e). Verified: the assignment in the workflow files (`runs-on`) and the job classes in the API of the six runs. Not verified: that the class explains the differences, because the code changed between the runs. Revisit after five nightly runs on this assignment, or when a red leg on large has a runner cause (a lost pod, an eviction, a DNS failure): then option (b) or (d) is decided on that evidence.

**Applied in the spec:** none (CI wiring). Code: [performance.yml](../../.github/workflows/performance.yml) (`runs-on` of each job, unchanged). Elsewhere: [releasing.md](../developer/releasing.md#ci) (the runner table and the measured basis), [releasing.md](../developer/releasing.md#ha-and-performance-nightly).

### Q-ARCH-327 · How do the legs that load both client images get them, and who builds them?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: The set `nuget-ha` now starts the npm client pods as well (the drain of R-HA-059 under the npm and NuGet load together), so job `e2e-nuget` needs both clients images, and so does the new job `e2e-upgrade`. Until now the nightly `e2e-nuget` let [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh) build the NuGet image inside the set, and the npm-only legs (`npm-ha`, `drain-roles`) do the same for the npm image.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job builds both images in its own steps (`make npm-clients-image`, `make nuget-clients-image`) and sets `TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD` and `TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD` to `0`, as the jobs `e2e-ci`, `e2e-bastion`, `e2e-vuln` and `e2e-mirror` of Test and Release do (chosen) | Named steps before the set; the set loads the images and builds none | The build has its own step and log, and its time is outside the set's 80-minute budget; one way for every job that loads both | Two more steps per job |
| (b) The set builds the images it needs (the status quo of the npm-only legs and of the nightly `e2e-nuget` before this change) | No steps, no flags | Fewer lines | The build runs inside the set's timeout and its log is mixed with the test's |
| (c) One job builds the images and passes them to the legs as artifacts | One build for all legs | Builds happen once | The NuGet clients image (2.3 GB) would be uploaded and downloaded per leg; not measured, rejected for that transfer |

**Choice:** (a) for the jobs that load both images (`e2e-nuget` and `e2e-upgrade` here). The npm-only legs keep (b): changing them is not needed for this change.

**Rationale:** The same two steps and flags as the jobs of Test and Release mean that a reader finds the build in the same place in every job. Verified: the flags `TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD` and `TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD` are the ones the Test and Release jobs set, and the targets `npm-clients-image` and `nuget-clients-image` exist in the Makefile (the Test and Release jobs run them). Not verified: that `nuget-ha` loads the npm pods without a rebuild, because the set's npm pod support is the other agents' change to deploy/dev/e2e.sh. Revisit when a leg needs an image on a runner that cannot hold it, or when the npm-only legs move to (a) with the next change of their job.

**Applied in the spec:** none (CI wiring). Code: [performance.yml](../../.github/workflows/performance.yml) (jobs `e2e-nuget` and `e2e-upgrade`). Elsewhere: [releasing.md](../developer/releasing.md#ha-and-performance-nightly).

### Q-ARCH-328 · Admission charges the bytes a request holds and the caches hold now, not a worst case per request and the caches' budgets

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Superseded in part by [Q-ARCH-330](#q-arch-330--the-charge-of-a-request-is-the-overhead-and-the-readers-it-has-open-now): what a request is charged after it settles. Revises [Q-ARCH-240](#q-arch-240--a-replica-admits-what-its-memory-holds-memory-slots-and-a-heap-gate-in-step-4) and [Q-ARCH-243](#q-arch-243--the-memory-slots-leave-the-caches-byte-budgets-aside) in part.

Context: with the kind nodes held to 6 CPUs, the drain of the leg `npm-ha` got 1,062 `503 server_busy`, every one counted as `memory_slots`, on a `web` pod whose heap was 111 MB of a 1 GiB limit. Two things in Q-ARCH-240 and Q-ARCH-243 made the slots few: every request was charged a download of a large object (640 KiB), and the three caches' byte budgets (648 MiB with the defaults) were taken out of the 0.8 × `L` whether the caches held anything or not, which left 48 MiB, 77 slots, on a 1 GiB container.

Measured on the integration stack (`TestHeldDownloadCost`: a fresh replica per size, caches off, an 8 GiB limit, `GOGC=10` so that the heap in use is close to the live heap; 100 slow downloads held in their writes; the numbers are per held download against the idle replica):

| File | Heap in use | Goroutine stacks | Resident set |
|---|---|---|---|
| 1 MiB | 586 KiB | 74 KiB | 639 KiB |
| 2 MiB | 563 KiB | 73 KiB | 642 KiB |
| 5 MiB | 586 KiB | 73 KiB | 661 KiB |

The table is the development agent's run on macOS. On Linux, where the test runs in Test and Release, the replica's send buffer of a loopback connection takes a whole 1 MiB file (autotuned up to `tcp_wmem`'s 4 MiB; run 42146436982 held 0 of 100), so the test measures 2, 5 and 8 MiB and fails when fewer than half of the downloads are still held when it reads the cost; the same run measured 460 KiB (2 MiB) and 660 KiB (5 MiB) of heap and stacks per held download.

A download of 1 to 5 MiB therefore holds 636 to 659 KiB whatever its size, against the 640 KiB charged: for a large object the worst case is the real case (the 256 KiB buffer, 220 to 256 KiB of window, 110 KiB of heap beside them and 73 KiB of stacks). A file of 50 KiB cannot be held open this way, because the kernel takes it whole into the socket buffer; what it holds follows from the code (`blob.DownloadCost`): a buffer of the class that fits it, 64 KiB, its own 50 KiB in the receive window, and the 128 KiB: about 242 KiB for as long as the request is in flight, which under a CPU-starved replica is long. So the worst case is wrong for the small tarballs of an npm install and for every request that is no download, and the reservation of the caches' budgets is wrong for every replica whose caches are not full.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Charge a download of a large object at step 4, hand back the difference when the blob store knows the size, and count the caches by the bytes they hold at the last heap sample (chosen) | `MemoryGuard` counts bytes; `Ticket.Settle` lowers the request's charge to the 128 KiB plus `blob.DownloadCost`; the registry's weighted sizes replace the budgets | A refusal is still at the door, before authentication and database reads; a charge grows only for readers open together, so no request is refused half way; a held tarball costs what it holds; empty caches reserve nothing; metrics, error code and refusal unchanged | The charge of a request that is no download stays at 640 KiB until it ends (short requests); the caches' growth is not charged (H-539) |
| (b) Charge only the overhead at step 4 and the download at `Open`, refusing there | Late refusal at the blob store | Exact accounting for every request | Measured, with 1,000 downloads of 1 MiB offered to a 256 MiB replica: the runtime held 237 MiB against 211 MiB with (a), because the 722 refused requests had already authenticated and run the handler up to the blob store, and the refusal is a 503 in the middle of a handler |
| (c) Admit by the live heap only | Drop the charges | One number | The live heap is as old as the last GC cycle: a burst of 2,000 is admitted inside one cycle (Q-ARCH-240 option (c)) |
| (d) Keep the budgets, lower them in the dev profile | Configuration only | No code | The operator pays a fixed reservation for caches that are empty; the small-tarball cost stays at the worst case |
| (e) Make every cache charge its growth to the guard | A cache asks before it inserts and gives back when it evicts | The sum of the charges is a promise again | Four caches in three packages and their eviction paths change; a refused insert is a cache miss on the next request. Not done: H-539 records what it would close |

**Choice:** (a). The request bytes stay 640 KiB with the defaults and the overhead 128 KiB, since the measurement is within 3 % of them. `trove_http_memory_slots` and `trove_http_memory_slots_in_use` keep their names and become the byte accounting in units of the request bytes: slots are (0.8 × `L` − the caches' bytes) ÷ the request bytes, in use is the charges rounded up. The chart's check of 64 slots (R-HA-029, Q-HA-70) keeps computing with the full budgets: it is the floor for a replica whose caches are full. Without a known limit there is no guard, as before.

**Rationale:** The promise of R-ARCH-005, a replica sheds and is never killed, is kept by two numbers that fail in different ways: the charges bound what is admitted before any GC cycle can show it, and the heap gate bounds everything the charges do not see. With empty caches the charges are exact for downloads (measured above) and conservative for everything else; with full caches they leave what Q-ARCH-243 left. What changes is the case between: a cache that grows after requests were admitted can push the heap past 0.8 × `L` by what it grows, and the heap gate closes then (H-539). The margin to the container limit is the 15 % of R-HA-036 plus the 20 % above the high-water mark, 328 MiB on a 1 GiB container. The development agent revisits this when the heap gate closes in a run with filled caches (the growth is then real, and option (e) is due), or when a request kind other than a download is held long enough to need a settle of its own.

**Applied in the spec:** R-ARCH-005 (memory slots, visibility, two acceptance criteria), [operations-and-observability.md](operations-and-observability.md) R-OPS-008 (the two gauges), [ha-and-helm.md](ha-and-helm.md) R-HA-036 (admission follows the memory), H-322, H-539. Code: `internal/httpserver/memory.go` (`MemoryGuard`, `Ticket`, `SettleMemory`), `internal/httpserver/chain.go`, `internal/blob/buffers.go` (`DownloadCost`), `internal/blob/download.go` (`Store.Open`), `internal/app/memory.go` (`cacheBytes`). Tests: `TestMemoryCharges`, `TestMemoryGuard`, `TestBurstBesideEmptyCaches`, `TestMemoryAdmission`, `TestHeldDownloadCost`.

### Q-ARCH-329 · The job worker is cancelled early enough that the release writes of its jobs get their whole settle deadline

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a job that ends writes its end with a fresh context of `jobs.SettleDeadline` (15 s), retries included: complete, fail, cancel and, at a shutdown, the release that returns it to the queue without counting the attempt (R-ARCH-016). The shutdown plan cancelled the handlers 5 s before the end of the server phase and waited for the worker until the close deadline, 4 s after it (5 s margin − 0.5 s slack − 0.5 s ops reserve). The release writes therefore had 9 s of their 15 s; a write that needed longer, as one does while PostgreSQL switches over, was abandoned with the process, and the job waited for its lease to expire and the reaper (R-HA-005) instead of returning at once. Reproduced by `TestShutdownReleaseWrites`: a release write of 1.8 s against a settle deadline of 2 s was cut by the close deadline.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Cancel the worker `SettleDeadline` before the close deadline, the other quiescers at 5 s before the end of the server phase as before (chosen) | The plan has a time of its own for the components that quiesce first (`ReleaseAt` = `CloseBy` − 15 s, not before the drain ends, not after `QuiesceAt`) | The write gets the deadline it was written with; engines and fetches keep their 5 s | Jobs lose 6 s of run time at a shutdown (11 s instead of 5 s before the end of the server phase); a shutdown timeout under 11 s cancels the worker when the drain ends |
| (b) Lower the settle deadline to the 9 s the plan gives | One constant | No plan change | A release during a PostgreSQL switchover, which takes 10 to 30 s, loses 6 s of tolerance in normal operation as well |
| (c) Give the writes more of the grace period | A larger margin | None needed | The margin is the 5 s the chart keeps for the exit before the kubelet's `SIGKILL` (R-HA-011) |
| (d) Run the release writes in a goroutine that outlives the close phase | Abandon nothing | None | The process exits before the kubelet's `SIGKILL`; a write still running dies with it, which is the case |

**Choice:** (a). The remaining quiescers wait for the worker as Q-ARCH-318 says and are cancelled at the later of their own time and the worker's return.

**Rationale:** The job framework promises that a drained worker hands its jobs back at once (R-HA-011, R-ARCH-016), and the promise holds only if the write that does it can finish. Eleven seconds still leave long jobs time to end by themselves: the worker stops claiming at `SIGTERM` and `Settle` waits for the running jobs until then. The development agent revisits this when the settle deadline changes, or when a job kind needs more than 11 s to persist its checkpoint (the handler's return counts against the same 15 s).

**Applied in the spec:** R-ARCH-024 (step 4), R-HA-011 (workers). Code: `internal/app/shutdown.go` (`shutdownPlan.ReleaseAt`, `shutdownBudget.Release`), `internal/app/app.go` (`quiesceGroups`), `internal/jobs/worker.go` (`SettleDeadline`). Tests: `TestShutdownReleaseWrites`, `TestShutdownPlan`.

### Q-ARCH-330 · The charge of a request is the overhead and the readers it has open now

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Revises [Q-ARCH-328](#q-arch-328--admission-charges-the-bytes-a-request-holds-and-the-caches-hold-now-not-a-worst-case-per-request-and-the-caches-budgets).

Context: a code review of Q-ARCH-328 found that every `Settle` after the first added to the request's charge and was never taken off, and that a ranged read was charged as a read of the whole object. The NuGet package page reads a README through the ranged reader of `internal/app/wire_nuget_blobs.go`, one `Open` per `ReadAt`; the zip reader makes about 215 of them, at 512 KiB each about 107 MiB for one page view, so three to six views on a 1 GiB pod would have refused every download with `memory_slots`. The registration pages of NuGet and the merges of virtual metadata open one object after the other the same way. Separately, a flight that outlives its request (`endGone`, serve-stale) carries the request's context, so its `Open` settled the ticket of a request that had already released it: the bytes stayed charged for the life of the process.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Settle` returns an undo that the reader's `Close` calls; the charge is the overhead plus the costs of the readers open now, a ranged read costs its length, a released ticket and a flight's context settle nothing (chosen) | `Ticket` keeps `open` and a reader count; the first settle lowers the charge from the admission charge, the undo takes a reader's bytes off | Sequential reads never pass the admission charge; the ranged reads of a README cost 8 KiB each, one at a time | Readers open together exceed the admission charge without admission (bounded by the readers the code opens together, none today) |
| (b) Settle only the first `Open` of a request | The rest are free | Simple | Concurrent large readers of one request are not counted |
| (c) Charge every `Open` with admission and refuse late | A refused `Open` is a 503 | Exact | Measured in Q-ARCH-328 option (b): refusals half way through a handler and more memory held |
| (d) Keep the peak of the request | Never lower the charge again | Cannot overshoot its own peak | A README view stays at the cost of its largest read for the rest of the request |

**Choice:** (a). `blob.Store.Open` calls the settle function before it asks the object store and hands the undo to the reader: `verifyReader.Close` for a verified download, a wrapper for a ranged or unverified body; a failed open undoes at once. A ranged read costs `blob.RangeCost`: the size class of a buffer of its length, at most `storage.download.buffer_size`, plus its length up to the receive window. A ticket knows when its request has ended, and `Settle` and the undos of an ended request do nothing. `proxy.Options.Detach` (`httpserver.WithoutTicket` in the composition root) strips the ticket from the context of a flight, whose memory belongs to the flight. The charge a request has freed by closing a reader may be taken by another request meanwhile, so a later reader of the first can bring the sum past the budget by at most the admission charge of that request; that is H-539's kind of overshoot and the heap gate's.

**Rationale:** The charge has to track what a request holds, and a request that reads a package one range after another holds one range. The development agent revisits this when a code path opens several large readers of one request at once (the charge then needs admission, option (c)).

**Applied in the spec:** R-ARCH-005 (a download settles). Code: `internal/httpserver/memory.go` (`Ticket.Settle`, `WithoutTicket`), `internal/blob/download.go`, `internal/blob/buffers.go` (`RangeCost`), `internal/proxy/flight.go` (`detached`), `internal/app/wire_proxy_engine.go`. Tests: `TestMemoryChargeSequence`, `TestMemoryCharges`, `TestDownloadVerification`, `TestDownloadBuffers`, `TestNuGetReadmePathMemory`, `TestDetachedContext`.

### Q-ARCH-331 · The quiesce of the shutdown is stopped when the server phase ends, or waited for

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Revises [Q-ARCH-329](#q-arch-329--the-job-worker-is-cancelled-early-enough-that-the-release-writes-of-its-jobs-get-their-whole-settle-deadline).

Context: the quiesce runs from a timer (`time.AfterFunc`) that was stopped only when `Shutdown` returned. With the worker cancelled 6 s earlier (Q-ARCH-329) a timer that came due after the server phase had ended, or a quiesce still running when it ended, could cancel handlers and engines while the components close, or after `Shutdown` returned.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stop the timer when the server phase ends; if its callback has started, wait for it up to the close deadline (chosen) | A quiesce is either finished before step 6 or never starts | Nothing is cancelled during the close | A quiesce that ignores its context holds the start of step 6 until the close deadline, which the plan bounds |
| (b) Leave it to the end of `Shutdown` | As before | No change | The defect |

**Choice:** (a).

**Rationale:** The plan is absolute (R-ARCH-024): the close phase starts when the server phase ends and nothing of step 4 may run in it. The development agent revisits this if a component's `Quiesce` is found to block past `ServerBy` in practice.

**Applied in the spec:** R-ARCH-024 (step 4). Code: `Shutdown` in `internal/app/app.go`. Test: `TestShutdownQuiesceTimer`.

### Q-ARCH-332 · Where the shared asset store of the UI sits in the code

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the shared asset store of [ha-and-helm.md](ha-and-helm.md#q-ha-120--how-a-replica-serves-a-hashed-ui-file-of-the-other-release)
reads and writes PostgreSQL and the blob store, is used by the UI handler of `internal/webui`, and runs a publish at start-up and a loop.
`internal/webui` imports no other package of the module (its package comment), so that it can be tested over an `fs.FS` alone.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An area `ui-assets` and a package `internal/webui/shared` (chosen) | The step `ui-assets` (`wireUIAssets`) on the roles that serve the UI (`api`, `web`, `all`) builds the store over the `infra` area's database and blob store, publishes, and starts the keeper; `internal/webui` defines the interface `Shared` and the hashed pattern; `internal/webui/shared` (layer edge) implements the store with the sqlc statements and the blob store | `internal/webui` stays free of the database and the blob store; the role table says where the store exists (`packages` serves no UI and builds none); `planSteps` proves the inputs | One more area and package |
| (b) The store inside `internal/webui` | One package | Fewer files | The handler's package imports the blob store and the database, and its tests need them |
| (c) The store in `internal/app` | The composition root holds it | No new package | The composition root would hold statements and logic of its own |

**Choice:** (a).

**Rationale:** it keeps each package's dependencies where they were and makes the roles that build the store data of the step list,
like every other area. Revisit if another role starts serving the UI.

**Applied in the spec:** the area table (`ui-assets`), the component table (webui). Code: [wire_ui_assets.go](../../internal/app/wire_ui_assets.go),
[steps.go](../../internal/app/steps.go), [internal/webui/shared](../../internal/webui/shared/). Test: `TestR_ARCH_001_RoleTableIsData`.

## What this does not cover


### H-1 · Format code runs in-process without isolation

Stage: `Inspect`, the storage hooks, `Rewrite` and `Merge`, on every upload and every upstream
document, for every registered format (npm and NuGet at MVP).

Live on every upload and every cache fill of a remote repository.

A logic flaw in a format's parser runs with everything the replica holds: database credentials,
object-store credentials and the stored-secret key ring. R-ARCH-028 bounds memory, time and archive
expansion, and Go's memory safety rules out a class of exploits. Neither confines a flaw to the
format. No sandbox is specified: no separate process, no seccomp profile, no reduced credentials
for parsing.

### H-2 · The upload spool is not encrypted by trove

Stage: the spool of the upload pipeline ([storage.md](storage.md)), used by R-ARCH-013 and
R-ARCH-014.

Live for every upload and every cache fill, for the duration of the transfer. After a crash it
remains live until that replica starts again and empties its spool.

The bytes of private packages rest unencrypted on the replica's spool volume. Their protection
depends on the platform: node disk encryption, and access to the volume backing the pod. trove
neither encrypts the spool nor wipes spool files securely.

### H-3 · UI and package content share one origin

Stage: every response from `/files/` and every format content or metadata route (R-ARCH-007), on the
UI's host name.

Live in the MVP for every installation, for every repository that holds files a browser can render
(HTML, SVG, XML, PDF), and for package metadata that echoes author-controlled text. From P1 it stays
live wherever `server.content_base_url` is unset, which is the default. With it set (R-ARCH-029),
browser requests for content leave the UI's origin, and H-3 is reduced to clients without fetch
metadata that carry no session cookie.

The defence on the UI's origin is a set of response headers:
- `nosniff`;
- CSP `sandbox` with `default-src 'none'`;
- `Cross-Origin-Resource-Policy: same-site`;
- `attachment`.

The core sets them in a writer wrapper that format handlers cannot bypass, and the conformance kit
tests them (Q-ARCH-10). Uploaded content still becomes script running on the UI's origin in either
case:
- a proxy in front of trove strips the headers;
- a browser ignores both `attachment` and CSP `sandbox`.

The content origin that removes the session cookie from that path is optional and P1 (R-ARCH-029,
Q-ARCH-10). A content origin on the UI's registrable domain keeps the cookies out only because every
trove cookie carries the `__Host-` prefix
([identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-2). It also stays
same-site with the UI, which R-ARCH-029 reports with a start-up warning.

### H-49 · Write access to Valkey denies service

Stage: the keys and channels under trove's Valkey prefix (R-ARCH-030): negative entries, flight
locks, rate-limit counters, throttle exemption markers, change messages and signals.

Live in the MVP for every installation, because Valkey is part of every production topology
(INV-12). Subject: the availability of remote repositories and of rate-limited routes, and cache
efficiency, for every repository and principal.

An attacker who can write under the prefix can:
- write negative entries, so that a package or file of a remote repository answers `404` until the
  entry's checked lifetime ends (`notFoundTtlSeconds`, 30 minutes by default) or an invalidation
  covers it. Repeated writes keep it hidden;
- hold flight locks, so that collapsed requests wait up to `proxy.fetch_wait_timeout_seconds`
  before the proxy goes on without the lock. For expired metadata the client is served the stale
  copy meanwhile, and the refresh follows without the lock, so a held lock cannot keep a document
  stale beyond `proxy.fetch_wait_timeout_seconds`
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-026 step 4);
- raise rate-limit counters to refuse a principal or address with `429`, or delete them to lift the
  cluster-wide budget. The per-replica in-flight limit still applies;
- set throttle exemption markers for credentials it guesses
  ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-038). The effect equals
  deleting the failure counter: more PostgreSQL lookups, and no credential is accepted, because a
  marker only lets a credential be validated;
- publish change messages and signals, which cause extra evictions and wake-ups. They cannot
  suppress or reorder a change, because only the PostgreSQL change log advances a replica's applied
  epoch (R-ARCH-018).

Valkey grants nothing: no access decision, no served content or metadata and no credential is read
from it (R-ARCH-030). It holds IDs and hashes only. The rate-limit bucket keys of client addresses
are unkeyed SHA-256 hashes, so a reader can recover an address by enumerating the address space.

What makes it real: a Valkey reachable from other workloads, without a NetworkPolicy that limits it
to trove's pods and the Sentinels; an ACL user shared with other applications, or the `default`
user; TLS switched off on a network an attacker can observe, which exposes the ACL password.

### H-55 · Key files of the stored-secret key ring are read once at start-up

Stage: the key files of `secrets.encryption_key_files`, read by the key-ring loader at start-up
(R-ARCH-021, [ha-and-helm.md](ha-and-helm.md) R-HA-027), for every sealed column.

Live from the first rotation: a key file that is replaced, or removed from the list, keeps its key in
memory on every replica until that replica restarts. A key that is suspected compromised therefore
stays in use on a replica that was not restarted.

Subject: the confidentiality of the sealed values under that key. What makes it real: an operator
rotates a key file and expects the old key to stop decrypting, or a key file is copied into a Secret
that other workloads can read. Mitigation today is the restart of every replica after a rotation.

### H-56 · The object-store endpoint skips the egress address policy

Stage: every object-store client of purpose `objectstore` (`internal/outbound`, used by
`internal/blob/s3`, R-ARCH-022, [storage.md](storage.md) R-STOR-030).

Live for every installation whose `storage.s3.endpoint` resolves to an address that the egress policy
would refuse, which includes every private-network object store. The endpoint comes from the
configuration the operator writes. Subject: the set of addresses that a replica may connect to with the
object-store credentials.

What makes it real: a write to the configuration, or a compromise of the operator's configuration
management, can point the object-store client at an internal address, and the policy that refuses the
same address for upstreams does not apply. Upstream requests stay under the policy.

### H-322 · Slow requests of one client take the memory slots of every client

Stage: step 4 of R-ARCH-005, the memory slots of Q-ARCH-240, on every client listener of a `web`
replica.

Live in the MVP for every installation with a known memory limit. Subject: the availability of the
replica for every principal, anonymous or not.

A request holds its slot until it ends, and step 4 runs before authentication (step 6), so it cannot
prefer anyone. A client that keeps as many requests open as the replica has slots, for example
downloads it reads at a trickle or uploads it sends at one, gets every other request answered `503`
until its own end. `server.transfer_stall_timeout` ends a request that moves no byte for 60 s, not
one that moves a byte now and then. The number to reach is the charge of a large download in slots, which the bytes in the caches lower (Q-ARCH-328):
2,228 on the chart's 2 GiB container with empty caches, 1,191 with the default caches full, and 77 on 1 GiB with full caches, where `server.max_in_flight` alone allowed 4,096.

What makes it real: a listener reachable by an unauthenticated client, a repository readable
anonymously or a token with `read`, and enough connections from one place. Rate limits (step 7)
count requests, not their duration. Mitigation today: a limit on concurrent connections per client
address at the edge, and more replicas.

### H-323 · A replica that retains memory above the high-water mark refuses every request

Stage: the heap gate of R-ARCH-005 (Q-ARCH-240, Q-ARCH-242).

Live for every installation whose caches and other retained state can reach 70 % of the Go memory
limit: the sizes of the in-process caches are configured independently of the container limit.
Subject: the availability of that replica.

The gate cannot tell memory that requests hold from memory the replica keeps. Once requests push the
live heap to 80 % and the gate closes, a replica whose retained memory alone stays above 70 % never
falls below the low-water mark: it answers every request `503 server_busy` while `/readyz` stays
`200`, so the Service keeps sending it traffic. Before the guard existed such a replica was
OOM-killed instead.

What makes it real: cache sizes (`proxy.metadata_cache_bytes`, `proxy.merged_metadata_cache_bytes`,
the rendition cache) that together approach the memory limit. The operator sees it in
`trove_http_requests_shed_total{reason="heap"}` and the gate's log line.

### H-324 · Connections that send no request are outside the memory guard

Stage: the client listeners (`net/http` and TLS) before step 1 of R-ARCH-005.

Live for every installation. Subject: the memory and file descriptors of a `web` replica.

The guard counts requests. An open connection that has not sent a request holds its TLS state, its
read and write buffers and one or two goroutines, a few tens of KiB, until
`server.read_header_timeout` (10 s) or `server.idle_timeout` (120 s) ends it. Nothing bounds the
number of connections a replica accepts.

What makes it real: a client that opens connections faster than the timeouts close them, tens of
thousands at a time. Mitigation today: connection limits at the edge and the pod's file-descriptor
limit.

### H-539 · A cache that grows after a request was admitted is outside the memory charges

Stage: the memory slots of R-ARCH-005 step 4 (Q-ARCH-328), on every client listener of a `web` replica with a known memory limit.

Live for every such installation whose caches are not yet full. Subject: the memory headroom of the replica between the high-water mark and the container limit.

The charges count the bytes the caches held at the last heap sample (every 50 ms) and the requests admitted since. A cache that grows afterwards, because admitted requests fill it, takes memory the admission has already given to those requests: the sum can pass 0.8 × `L` by what the caches grow, at most their budgets less what they held (648 MiB with the defaults). The heap gate closes when the live heap reaches 0.8 × `L` and refuses new requests until it falls below 0.7 × `L`; the 15 % that R-HA-036 keeps between `L` and the container limit and the 20 % between the mark and `L` are what carries the replica meanwhile. Before Q-ARCH-328 the full budgets were reserved, so the sum held by construction (Q-ARCH-243).

What makes it real: a burst of requests that fill the metadata caches while many downloads are held, on a container whose limit leaves less than the growth: the 1 GiB dev profile with the default 648 MiB of budgets. Mitigation today: the heap gate, the 64-slot floor of the chart's check (R-HA-029) computed with the full budgets, and smaller cache budgets. What would close it is a cache that charges its growth to the guard before it inserts (option (e) of Q-ARCH-328).

### H-517 · The cut into roles separates the login secrets, not the data credentials

Stage: the role table of R-ARCH-001 (built in the binary; the chart renders the roles with phase 6a's WP6a.3, and a `web`
replica holds everything below and the login secrets as well, see
[H-518](../security/roles-and-trust-boundaries.md#h-518--the-bridge-and-the-development-role-hold-both-halves-of-the-cut)).

Live for every installation that runs the roles `api` and `packages`. Subject: the database login, the object-store and Valkey credentials, the
stored-secret key ring, and through it the upstream credentials of every remote repository (R-ARCH-021), all of which a
`packages` replica holds as an `api` replica does.

The cut keeps the session store, the OIDC client secrets and the break-glass password out of `packages`, so a flaw reachable
through a package protocol cannot forge or read a session or sign in as the break-glass administrator. It does not narrow what
the process reaches in PostgreSQL: every role connects with the same runtime login and the same grants
([ha-and-helm.md](ha-and-helm.md) R-HA-014), and every role builds the key ring (the area `keyring`), so a compromised
`packages` replica reads the session table, the token hashes and the sealed upstream credentials as any other replica could.
ADR-153 weighed a role with data access of its own (option (c)) and rejected it for the transactions that hold security
guarantees in one database.

What makes it real: an exploitable flaw in a code path of the `packages` or `files` family (request parsing, format code, H-1).
The mitigation is the same as before the cut: the narrow runtime grants of R-HA-014 and the pod security context of R-HA-033.

### H-521 · Two path rules outside the stages of authentication and rate limits do not ask whether the family is served

Stage: step 5b of R-ARCH-005 (the Bastion interface's recognition, [bastion.md](bastion.md) R-BAS-008) and the error shape of a
format prefix (`Router.HandleErrorShape`, Q-ARCH-214), on the default endpoint of `api` and of `packages`. Q-ARCH-306 made the
rules of the authentication stage, the preflight and stage 7 ask `Endpoint.ServesRequest`; these two read the path alone.

Live for every installation that runs the roles. Subjects and what each tells apart from `/does-not-exist`:

- **The Bastion recognition, for the holder of a valid Bastion token.** On `packages`, an interface call to `/api/v1/bastion/…`
  is recognised and answers the router's `404` with `Trove-Bastion-Interface`, where the same token in `Authorization` on
  `/does-not-exist` is a credential the authentication stage rejects (`401`). On `api`, a forward to `/files/…` or
  `/api/<format>/…` passes the forward backstop and reaches the authentication stage (`401` without a client credential), where
  a forward to `/does-not-exist` gets the backstop's `404`. Only a registered Bastion holds such a token, and its configuration
  names the installation's URLs (R-BAS-044), so the difference tells it nothing about the topology it was not given.
- **The error shape of a family nested below a served format prefix.** On `packages`, npm's registry-relative trusted-publishing
  exchange (`/api/npm/<repoKey>/-/npm/v1/oidc/…`, family `ci_exchange`) is answered in npm's error shape (the dispatcher's `404`,
  the stage's `401` through the shape table of `/api/npm/`), not in the `problem+json` of `/does-not-exist`. Anyone sees it; it
  tells that the process serves npm's prefix, which every other path below `/api/npm/` on `packages` tells as well.

What makes it real: a caller that compares these answers to locate the role of a replica behind the edge. Neither carries data,
and the first needs a Bastion's credential. What would close it is the question of Q-ARCH-306 in `bastionmain.Recognize` (an
interface path or a forwardable family the endpoint does not serve leaves the request ordinary) and in the shape lookup of
`httpserver.Problem` together with the dispatcher's family check (an unserved family below a format prefix renders
`problem+json`).
