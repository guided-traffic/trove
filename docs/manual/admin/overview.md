# How trove runs

This page explains the moving parts of a trove installation: the process roles, the three services trove depends on and what each of them holds, why any replica of a role can answer any request, which listeners a replica opens, how the schema migration runs, and what the Helm chart deploys. Read it before you install trove or plan its capacity.

## At a glance

```text
  browsers, package managers, scripts
                 │ HTTPS
       ingress controller (edge)
                 │ HTTPS, requests spread at random
                 ▼
  api replicas            packages replicas            worker replicas
    UI, management API      package protocols            background jobs
    sign-in, CI exchange    raw files, tokens only       (queues, pools)
    client listener :8080   client listener :8080        ops listener :8081
    ops listener :8081      ops listener :8081
          │                       │                            │
          └───────────────────────┴──────────── TLS ───────────┘
                                  │
       ┌──────────────────────┼──────────────────────┐
       ▼                      ▼                      ▼
  PostgreSQL            Valkey with Sentinel    S3 object storage
  system of record      shared ephemeral state  blobs

  migration Job: trove migrate up, before every install and upgrade, PostgreSQL only
```

## One binary, several roles

trove is a single binary with the web UI built in. What a process does is set by its role, given as `trove serve --role <role>` (the chart sets it per Deployment) or as `process.role` in the configuration. The role has no default.

| Role | What it runs | Where it runs |
|---|---|---|
| `api` | The client listener with the UI, the management API, sign-in and the CI token exchange, and the UI's file transfer (the content routes under `/api/v1/projects/<projectKey>/repositories/<repoKey>/content/`); the ops listener. It holds the sessions and the sign-in secrets (`secrets.breakGlass`, `secrets.oidc[]`). It puts background work into the job queue but does not run it. | The `api` Deployment and, optionally, a pool Deployment per path prefix or endpoint |
| `packages` | The client listener with the package protocols (`/api/<format>/<projectKey>/<repoKey>/`, and `/v2/` for the OCI registry) and the raw file transfer (`/files/<projectKey>/<repoKey>/`), for clients that present a token; the ops listener. It ignores a session cookie, mounts no sign-in secret and fills the cache of remote repositories from their upstreams. | The `packages` Deployment and, optionally, a pool Deployment per path prefix or endpoint |
| `worker` | The background jobs: garbage collection of stored content, repository and folder deletions, token and session cleanup, audit retention, the sync of the advisory data and the other maintenance tasks. It opens the ops listener only, never a client port. A worker pool claims only the queues you assign to it. | The `worker` Deployment and, optionally, a pool Deployment per queue set |
| `web` | Everything `api` and `packages` run, in one process: the role of the releases before the split and the bridge of the upgrade across it. A fresh installation never runs it; see [Upgrades](upgrades.md#upgrade-from-a-release-before-the-split-into-roles). | The `web` Deployment, only while `web.enabled` is `true` |
| `all` | Every role above in one process. It logs a warning at start because it is meant for local development only. | Never rendered by the chart |

The ingress controller sends each request to the Service of the role that serves its path: the UI, sign-in and the management API to `api`, the package protocols and `/files/` to `packages`. Nothing else tells a role from the other: a path that a role does not serve answers like any unknown path, with `404`.

Besides `serve`, the binary has these commands:

| Command | Purpose |
|---|---|
| `trove migrate up` | Applies the database migrations. The chart runs it as a Job before every install and upgrade. |
| `trove migrate status` | Prints the migration state as JSON and changes nothing; `GET /api/v1/system/migrations` answers the same report ([Upgrades](upgrades.md#how-the-migrations-work)). |
| `trove admin …` | Bootstrap and recovery: validate the configuration, the break-glass account, service accounts and their tokens, ending sessions, disabling a user. See [Installation](installation.md#bootstrap-and-recovery-commands). |
| `trove backup` | The backup of the database and the blobs to a target bucket of its own: `create`, `list`, `verify`, `delete`, `prune` and `restore`. The chart runs `create` as the CronJob `<fullname>-backup` once `backup.enabled: true` is set (off by default) and `restore` as the Job `<fullname>-restore` with `restore.enabled`. See [Backup and restore](backup-and-restore.md).

The web UI is built into the binary with the licences of the software it bundles. Every installation serves their notices as plain text at `/ui/<locale>/3rdpartylicenses.txt`, for example `https://trove.example.com/ui/en/3rdpartylicenses.txt`, with the same English text under `/ui/de/`. The Installation card of the system status screen links the file of its build. The file ends with the format logos the UI shows beside the names of Debian, NuGet and OCI: each logo's owner, source and licence, and the statement that the owners do not endorse trove.

## The dependencies

trove keeps no state on its own disks. Its pods use only an ephemeral spool directory and `/tmp`, and nothing is lost when a pod is replaced. Every piece of state lives in one of three services, none of which the chart installs.

| Service | Role | What it holds | What happens without it |
|---|---|---|---|
| PostgreSQL 16 or later | System of record | Repositories and their settings, the item tree with its files, packages and versions, users, groups, service accounts, hashed tokens, sessions, permission policies and role bindings, endpoint exposure rules, the audit journal, the job queue, the change log that keeps the replicas' caches in step, and the schema state | Replicas turn unready after `health.database_outage_grace` (20 s by default) and requests fail with `503`. Nothing is served from anywhere else. |
| Valkey 9.0 or later, with Sentinel | Shared ephemeral state | Cluster-wide rate-limit counters, short-lived markers of recently checked tokens, the locks that collapse simultaneous fetches from an upstream registry into one, the negative cache of upstream misses, and Pub/Sub messages that evict caches early | trove keeps serving in a degraded mode: rate limits fall back to each replica's share, fetch collapsing works only within a replica, and cache invalidations arrive through the PostgreSQL change log instead of the faster message. You lose performance, not data, because Valkey never holds the only copy of anything. |
| S3-compatible object storage | Blob store | The content of every file and package, stored once under its SHA-256 digest | Downloads and uploads of content fail. A replica checks the store before its first readiness; afterwards the check only gates readiness when `health.require_object_store` is on. |

Every connection to these services runs TLS by default, and the server certificate is verified, against the system roots or against the CA you configure. Credential and certificate files are read again while trove runs, so a rotated Secret takes effect without a restart. [Configuration](configuration.md#what-needs-a-restart-and-what-applies-live) lists what applies live.

## Any replica, any request

No replica of `api` or `packages` keeps state that a later request needs. A sign-in is three requests (start, identity provider, callback) and each step is recorded in PostgreSQL, so each of them may reach a different replica. Sessions, tokens and permissions are read from PostgreSQL and cached per replica. Every replica polls the PostgreSQL change log and drops a cached value that a write on another replica made stale; a revoked token or an ended session stops working on every replica within `auth.revocation_bound` (2 s by default).

The role Services spread requests over the pods of their role at random and have no session affinity. Do not add cookie or source-address affinity at the ingress controller: trove does not need it, and it would defeat the even spread of load.

Background jobs are claimed by `worker` replicas under leases with heartbeats. A job whose worker disappears is picked up by another one when the lease runs out.

## Listeners

| Listener | Port | Serves | Opened by |
|---|---|---|---|
| Client listener (the default endpoint) | `8080` (`server.listen`) | `api`: the UI under `/ui/`, the management API under `/api/v1/` (the UI's file transfer included) and sign-in under `/auth/`. `packages`: raw files under `/files/` and the package protocols under `/api/<format>/<projectKey>/<repoKey>/`, and the OCI registry under `/v2/`. | `api` and `packages`, each its own routes |
| Further endpoints | One port each, set per endpoint | Only the route families the endpoint is configured for (packages by default) that the role serves, with its own hosts, certificates and accepted credentials | `api` or `packages`, or the endpoint's own pool |
| Ops listener | `8081` (`server.ops_listen`) | `GET /healthz` (liveness), `GET /readyz` (readiness, `?verbose=1` lists the checks) and `GET /metrics` (Prometheus) | `api`, `packages` and `worker` |

An endpoint is identified by the listener a request arrives on, not by its `Host` header; a host the endpoint does not serve answers `421`. [Endpoints and exposure rules](endpoints.md) describes further endpoints. With the chart, every listener serves HTTPS by default and reloads its certificate without a restart; see [TLS and certificates](tls-and-certificates.md).

The ops listener has no authentication of its own unless `metrics.auth` is set to `static`. The chart's NetworkPolicy admits port 8081 only from the `monitoring` namespace by default.

## Start, readiness and shutdown

A replica opens its ops listener first and answers `/readyz` with `503` until its checks pass: PostgreSQL reachable on the primary, the schema at the version this binary needs, the TLS material loaded, the object store checked once and the client listeners bound. Valkey is reported but holds back only the first readiness, and only when its settings are wrong (a wrong password, an unknown primary name, a rejected certificate). A replica that cannot reach PostgreSQL stays alive and unready and keeps trying.

On `SIGTERM`, a replica turns unready at once. An `api` or `packages` replica keeps serving for `health.shutdown_drain_delay` (5 s by default) so the ingress controller can drop it, lets in-flight requests finish and closes within the pod's termination grace period (120 s for `api` and `packages`, 300 s for `worker` in the chart). A `worker` stops claiming jobs and releases what it holds. This is what lets a node drain or a rolling restart pass without failed requests.

## The migration Job

The schema is versioned with the binary. `trove migrate up` applies the migrations embedded in the binary in order, each in its own transaction, under a PostgreSQL advisory lock, so two runs never overlap. The chart runs it as the Job `<fullname>-migrate`, a Helm hook before every install and upgrade; the Deployments change only after it has succeeded. The Job connects only to PostgreSQL: it mounts no Valkey, object-store or encryption-key Secret and opens no listener.

A replica checks the schema at start (the compatibility gate):

- A schema that is behind this binary keeps the replica unready until the migration has run.
- A schema that requires a newer binary than this one makes the replica exit with `schema requires trove >= <version>`.
- Migrations that only add to the schema never make an older binary incompatible, so a running replica of the previous version stays ready after such a migration.

[Upgrades](upgrades.md) describes the order of an upgrade and what is verified about it.

## What the chart deploys

`<fullname>` is the release name, or `<release>-trove` when the release name does not contain `trove`.

| Object | Name | Purpose |
|---|---|---|
| Deployment | `<fullname>-api` | `trove serve --role api`, 2 replicas by default |
| Deployment | `<fullname>-packages` | `trove serve --role packages`, 3 replicas by default |
| Deployment | `<fullname>-worker` | `trove serve --role worker`, 2 replicas by default |
| Deployment, Service | `<fullname>-api-<pool>`, `<fullname>-packages-<pool>`, `<fullname>-worker-<pool>` | One per entry of `api.pools`, `packages.pools` and `worker.pools`; a worker pool has no Service |
| Deployment, Service | `<fullname>-web` and `<fullname>` | Only with `web.enabled`, the bridge of the upgrade from a release before the split |
| Job (Helm hook) | `<fullname>-migrate` | `trove migrate up` before install and upgrade, with its own ConfigMap and ServiceAccount |
| Service | `<fullname>-api`, `<fullname>-packages` | Port 8080 of the pods of that role's main Deployment, no session affinity; the Ingress routes by path to them |
| Service | `<fullname>-ops` | Port 8081 of every `api`, `packages`, `worker` and `web` pod |
| ConfigMap | `<fullname>-config` | `trove.yaml`, rendered from the `config` value |
| ServiceAccount | `<fullname>` | Without a Kubernetes API token: trove never calls the Kubernetes API |
| PodDisruptionBudget | one per Deployment | At most one pod of a role unavailable at a time |
| NetworkPolicy | one per Deployment (`<fullname>-api`, `<fullname>-packages`, `<fullname>-worker`, each pool), `<fullname>-migrate` | Ingress rules only: 8080 from the configured peers (anywhere by default), 8081 from the `monitoring` namespace, nothing to the Job |
| Ingress | `<fullname>` | Only with `ingress.enabled`; HTTPS to the backend |
| Service, Ingress, NetworkPolicy, optional Deployment | `<fullname>-ep-<name>`, `<fullname>-packages-ep-<name>`, `<fullname>-api-ep-<name>` | One set per further endpoint |

The pods run as a non-root user with a read-only root file system and satisfy the restricted Pod Security Standard. They are spread across nodes and zones.

The chart does not deploy PostgreSQL, Valkey, the object store, the ingress controller, certificates or any Secret: every Secret it mounts must exist before the install. It also renders no autoscaler, no egress NetworkPolicy, no ServiceMonitor, and no Gateway API route; the backup CronJob only with `backup.enabled: true`.
