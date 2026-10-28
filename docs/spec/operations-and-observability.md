# Operations and observability

Status: specification — not implemented

This page specifies how trove is run, watched and automated from outside. It covers:

- Health and readiness semantics on the operations listener; ping, version and system information.
- The Prometheus metrics catalog, with its naming and cardinality rules.
- The structured log stream: application, request, upstream and audit records, plus secret
  redaction.
- The audit event catalog, with its storage, query, export and retention.
- OpenTelemetry tracing.
- Administrative operations: scheduled maintenance tasks, the garbage-collection trigger, remote
  cache invalidation, metadata reindex, the storage summary, read-only maintenance mode, the system
  banner, lock diagnostics and support bundles.
- The configuration file and how environment variables map onto it.
- Configuration as code: declarative documents with diff and apply semantics.
- Outgoing mail and content types for raw downloads.
- Webhooks: the event outbox, envelope, event types, subscriptions, signing, templated requests,
  retries through the job queue, outbound address protection, test delivery and the delivery log.

Backup and restore belong to [ha-and-helm.md](ha-and-helm.md). This page only lists what it adds
to them.

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
- **Schedules.** Five-field cron expressions evaluated in UTC, in the syntax of
  [Kubernetes CronJobs](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#schedule-syntax),
  plus the macros `@hourly`, `@daily` and `@weekly` (Q-OPS-7).
- **Cross-replica signals** use PostgreSQL
  [`LISTEN`/`NOTIFY`](https://www.postgresql.org/docs/current/sql-listen.html) (INV-12). The channel
  names on this page are proposals; [architecture.md](architecture.md) may rename them.
- **Permissions** used on this page. [authorization.md](authorization.md) defines them; the names
  are proposals. Token scopes that narrow them (TOK-014) belong to
  [identity-and-authentication.md](identity-and-authentication.md).

| Permission (proposed name) | Grants |
|---|---|
| system administrator | Every operation on this page |
| `system.read` | Read replicas, system information, tasks, jobs, locks, effective configuration, log levels |
| `audit.read` | Query and export audit events (the auditor role, IAM-006) |
| `storage.read` | Read the storage summary |
| `config.read` | Run configuration diff and export |
| `webhooks.read`, `webhooks.manage` | Webhook subscriptions and delivery log (R-OPS-039) |
| repository action `manage` (INV-9) | Cache invalidation and reindex of that repository |

## Health and system information

### R-OPS-001 · Liveness and readiness probes
Priority: **MVP** · Covers: OPS-002, OPS-003, OPS-004, ADMAPI-069, HA-023

Every replica MUST serve `GET /healthz` (liveness) and `GET /readyz` (readiness) on the operations
listener (INV-6) without authentication. Neither path exists on the client listener.

A probe request MUST answer from cached check results. It MUST NOT open a database connection or
call object storage itself, so a probe storm cannot load the dependencies. A background loop runs
every check every `health.check_interval`, each bounded by `health.check_timeout`.

**Liveness** reflects the process only. It returns `200` while the HTTP server answers and the check
loop has completed a round within the last three intervals; otherwise `503`. It MUST NOT depend on
PostgreSQL or object storage. Restarting replicas does not repair a dependency outage, and coupling
liveness to one would restart every replica at the same moment.

**Readiness** returns `200` only while every check below passes. A check counts as failed after
`health.failure_threshold` consecutive failures and as passed again after
`health.success_threshold` consecutive successes.

| Check | Passes when | Why it gates traffic |
|---|---|---|
| `startup` | Listeners are bound, workers started, in-process caches initialised | No half-started replica receives traffic |
| `schema` | The applied schema version is inside the window this binary supports (expand/contract, INV-3) | A replica never serves against a schema it does not understand |
| `database` | `SELECT 1` on the connection pool succeeds within the timeout | Nothing works without PostgreSQL (INV-3) |
| `objectStore` | A `HEAD` request on the bucket (or on a sentinel object under the configured prefix) succeeds within the timeout; skipped when `health.require_object_store` is `false` | INV-12; see Q-OPS-2 |
| `notify` | The replica's `LISTEN` connection is established | Without it the replica misses cache invalidations, including token revocations (INV-8); on reconnect it drops every in-process cache before it reports ready again |
| `configBootstrap` | Startup configuration documents were applied (R-OPS-031); passes trivially when none are configured | An invalid document stalls a rollout instead of breaking the service |
| `draining` | No termination signal was received | Removes the replica from the Service before it stops |

`health.liveness_unready_grace` couples liveness to readiness. It defaults to `0`, which means the
two are never coupled. When it is greater than zero, liveness fails once readiness has failed
without a break for that long (Q-OPS-1).

**Shutdown.** On `SIGTERM` the replica MUST:

1. Fail the `draining` check, so readiness answers `503` at once.
2. Keep serving for `health.shutdown_drain_delay`.
3. Stop accepting connections and let in-flight requests finish within the shutdown budget.
   [ha-and-helm.md](ha-and-helm.md) sets the pod's termination grace period;
   [architecture.md](architecture.md) owns the server shutdown timeout.

Liveness stays `200` while the replica drains. Workers stop claiming jobs, and jobs still running at
the deadline are released back to the queue.

**API (operations listener, no authentication):**

```http
GET /readyz              → 200 {"status":"ready"}
GET /readyz              → 503 {"status":"unready","failing":["database"]}
GET /readyz?verbose=1    → 200 | 503
GET /healthz             → 200 {"status":"alive"} | 503 {"status":"stuck"}
```

```json
{
  "status": "ready",
  "replica": "trove-6f9c7d8b5-x2v4q",
  "checkedAt": "2026-10-28T12:00:05.120Z",
  "checks": {
    "startup": {"status": "pass"},
    "schema": {"status": "pass", "applied": 42, "supported": [41, 42]},
    "database": {"status": "pass", "latencyMs": 2},
    "objectStore": {"status": "pass", "latencyMs": 11},
    "notify": {"status": "pass"},
    "configBootstrap": {"status": "pass"},
    "draining": {"status": "pass"}
  }
}
```

Failure text in verbose output MUST be sanitised. It never contains a connection string,
credentials or a presigned URL (R-OPS-013).

**Configuration:**

```yaml
health:
  check_interval: 5s              # default (proposed)
  check_timeout: 2s               # default (proposed)
  failure_threshold: 2            # default (proposed)
  success_threshold: 1            # default (proposed)
  require_object_store: true      # default (proposed); INV-12
  liveness_unready_grace: 0s      # default (proposed); 0 = never couple liveness to readiness
  shutdown_drain_delay: 5s        # default (proposed)
```

**Acceptance criteria:**

- `kubectl -n trove port-forward pod/<replica> 8081:8081` followed by
  `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8081/readyz` prints `200`. The same
  path on port 8080 returns `404`.
- Apply a NetworkPolicy that denies egress from the trove pods to PostgreSQL. Within 12 s
  (interval × threshold + timeout), `/readyz` answers `503` with `"failing":["database"]`, while
  `/healthz` keeps answering `200`. Remove the policy, and `/readyz` returns to `200` within 5 s.
- Run `npm ci --registry https://trove.example.com/api/npm/npm-all/` in a loop while
  `kubectl -n trove rollout restart deploy/trove` runs. No iteration fails.
- Terminate the `LISTEN` session of one replica with `pg_terminate_backend`, then revoke a token
  through another replica. After the replica reconnects, the revoked token is rejected there too.
- 1,000 parallel requests to `/readyz` cause no additional PostgreSQL sessions
  (`pg_stat_activity` count unchanged).

### R-OPS-002 · Ping, version and identification headers
Priority: **MVP** · Covers: OPS-001, OPS-005, OPS-006, ADMAPI-070

`GET /api/v1/system/ping` on the client listener answers `200 text/plain` with `OK` while the
replica is ready, and `503` with `UNAVAILABLE` otherwise. Anonymous callers MUST be accepted even
when anonymous repository access is off (AUTHN-036). Credentials that are presented MUST be
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
`always` sends it to everyone, `never` suppresses it. No response carries the product version in a
header.

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
- An anonymous call to `/api/v1/system/version` gets `401`. The same call with a valid token gets
  `200`, and the response has an `X-Trove-Replica` header that the anonymous response lacks.

### R-OPS-003 · Replica registry and service status
Priority: **P1** · Covers: OPS-008

Every replica MUST register itself in PostgreSQL at startup and refresh its row every
`system.heartbeat_interval`. A row whose heartbeat is older than `system.heartbeat_stale_after` is
shown as `lost`. The `replica-registry-prune` task (R-OPS-019) deletes rows that have been stale
for 24 h. On a clean shutdown the replica deletes its own row.

```sql
CREATE TABLE replica (
  instance_uid  uuid        PRIMARY KEY,          -- random per process start
  name          text        NOT NULL,             -- pod name (downward API), else host name
  version       text        NOT NULL,
  revision      text        NOT NULL,
  roles         text[]      NOT NULL,             -- {api,worker} (INV-2)
  state         text        NOT NULL CHECK (state IN ('starting','ready','unready','draining')),
  checks        jsonb       NOT NULL DEFAULT '{}',-- last readiness check results, sanitised
  leases        jsonb       NOT NULL DEFAULT '[]',-- [{"name":"scheduler","since":"…"}]
  started_at    timestamptz NOT NULL,
  heartbeat_at  timestamptz NOT NULL
);
```

API: `GET /api/v1/system/replicas` → `200`. Requires `system.read`.

```json
{
  "items": [
    {
      "name": "trove-6f9c7d8b5-x2v4q",
      "instanceUid": "8a0b…",
      "version": "0.3.0",
      "revision": "4f1c2a9e0d7b…",
      "roles": ["api", "worker"],
      "state": "ready",
      "lost": false,
      "startedAt": "2026-10-28T08:01:12.004Z",
      "heartbeatAt": "2026-10-28T12:00:10.311Z",
      "leases": [{"name": "scheduler", "since": "2026-10-28T08:01:15.880Z"}],
      "checks": {"database": "pass", "objectStore": "pass", "notify": "pass"}
    }
  ]
}
```

The UI shows this list as the service status screen ([api-and-ui.md](api-and-ui.md)). Mixed versions
during a rolling upgrade are visible here.

```yaml
system:
  heartbeat_interval: 10s         # default (proposed)
  heartbeat_stale_after: 30s      # default (proposed)
```

**Acceptance criteria:** with three replicas, the list shows three `ready` entries. After
`kubectl delete pod --grace-period=0 --force <replica>`, that entry shows `"lost": true` within 30 s
and is replaced by the new pod's entry.

### R-OPS-004 · System information
Priority: **MVP** · Covers: ADMAPI-070, OPS-007

`GET /api/v1/system/info` → `200`. Requires `system.read`. It returns the facts an operator needs to
describe the deployment, with every secret omitted.

```json
{
  "installationId": "0b7c3f5e-…",
  "version": {"version": "0.3.0", "revision": "4f1c2a9e0d7b…", "goVersion": "go1.27.3"},
  "baseUrl": "https://trove.example.com",
  "baseUrlSource": "config",
  "maintenanceMode": "normal",
  "replicas": {"total": 3, "ready": 3, "versions": {"0.3.0": 3}},
  "database": {
    "serverVersion": "17.6",
    "schemaVersion": 42,
    "supportedSchemaVersions": [41, 42],
    "pool": {"max": 20, "inUse": 3, "idle": 5}
  },
  "objectStore": {
    "endpoint": "https://s3.eu-central-1.amazonaws.com",
    "bucket": "trove-prod",
    "prefix": "blobs",
    "credentialSource": "workload-identity"
  },
  "features": {"tracing": false, "webhooks": true, "configAsCode": true},
  "formats": ["npm", "nuget"],
  "runtime": {"goos": "linux", "goarch": "amd64", "gomaxprocs": 4, "memoryLimitBytes": 2147483648}
}
```

`installationId` is a UUID generated by the first migration and stored in `system_state`
(R-OPS-024). Support bundles use it, and it is the stable identifier of the installation in
exported data.

**Acceptance criteria:** the response contains no field whose value is a password, key, token or
presigned URL. `grep -iE 'secret|password|trv_'` over the response finds only key names, never
values.

### R-OPS-005 · External base URL and forwarded hosts
Priority: **MVP** · Covers: OPS-051, OPS-052, ADMAPI-071

`server.base_url` is the canonical external URL. Every absolute URL that trove generates MUST be
built from it: npm tarball URLs, NuGet service-index resources, the OIDC redirect URI, UI links,
`Location` headers, webhook `source` (R-OPS-036) and links in mail.

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

## Metrics

### R-OPS-006 · Metrics endpoint
Priority: **MVP** · Covers: OPS-011, API-033

Every replica MUST expose `GET /metrics` on the operations listener in the
[Prometheus text format 0.0.4](https://prometheus.io/docs/instrumenting/exposition_formats/). When
the request's `Accept` header asks for `application/openmetrics-text`, it uses the
[OpenMetrics 1.0](https://openmetrics.io/) format, which carries exemplars (R-OPS-018).

- Each replica exposes only its own process. Prometheus scrapes every pod, through a ServiceMonitor
  rendered by the chart ([ha-and-helm.md](ha-and-helm.md)), never through the Service or the
  Ingress.
- `metrics.auth: none` (default) relies on the NetworkPolicy of INV-13 to keep the listener
  cluster-internal (H-41). `metrics.auth: static` requires `Authorization: Bearer <value>`, where the
  value is read from `metrics.bearer_token_file` and compared in constant time.
- Scraping deliberately does not use trove tokens (TOK-014). Validating one needs PostgreSQL, and
  metrics must stay readable during exactly the database outage they are needed for.
- Metrics are not offered on the client listener.
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
  PostgreSQL is unreachable.

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
  | `handler` | Registered handler names (`npm.packument`, `nuget.push`, `files.get`, `api.repositories.list`, `unmatched`, …) |
  | `method` | Standard HTTP methods; anything else becomes `OTHER` |
  | `code` | HTTP status code, on counters |
  | `code_class` | `1xx` to `5xx`, on histograms |
  | `format` | Registered format names |
  | `class` | `local`, `remote`, `virtual` |
  | `repo` | Repository key; capped, see below |
  | `subscription` | Webhook subscription key; capped, see below |
  | `operation`, `kind`, `result`, `outcome`, `task`, `cache`, `state`, `direction`, `auth_method`, `query`, `name`, `channel`, `reason`, `mode`, `family`, `label` | Enumerations fixed in code |
  | `version`, `revision`, `goversion` | Only on `trove_build_info` |

- **Forbidden labels.** User names, principal or token IDs, IP addresses, request paths, package
  names, versions, trace IDs and error messages MUST NOT appear as label values.
- **Repository cap.** A family with a `repo` label gives at most `metrics.label_limits.repository`
  distinct keys their own series per replica. Keys matching `metrics.repository_label_include`
  patterns get theirs first, the rest in order of first observation. Every further key is reported
  as `repo="_other"`. The same rule applies to `subscription`. Each overflow increments
  `trove_metrics_label_overflow_total{family,label}`.
- **Budget.** With default settings, 500 repositories and 50 subscriptions, one replica SHOULD
  expose fewer than 25,000 series.
- **Values read from PostgreSQL.** Inventory and queue gauges are read from PostgreSQL, so every
  replica reports the same value. Dashboards aggregate them with `max`, not `sum`, and their help
  text says so.

```yaml
metrics:
  label_limits:
    repository: 500               # default (proposed)
    subscription: 100             # default (proposed)
  repository_label_include: []    # default (proposed); example ["npm-*", "nuget-*"]
  db_query_histograms: false      # default (proposed); see R-OPS-008
  native_histograms: false        # default (proposed); also expose native histograms
```

**Acceptance criteria:**

- A load test against 600 repositories yields exactly 500 distinct `repo` values plus `_other` in
  `trove_repository_requests_total`, and a non-zero overflow counter.
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
| `trove_build_info` | gauge (=1) | `version`, `revision`, `goversion` | Build of the running binary |
| `trove_http_requests_total` | counter | `listener`, `handler`, `method`, `code` | Completed requests |
| `trove_http_request_duration_seconds` | histogram | `listener`, `handler`, `method`, `code_class` | Time from first byte received to last byte sent |
| `trove_http_requests_in_flight` | gauge | `listener` | Requests in progress |
| `trove_http_received_bytes_total` | counter | `handler` | Request body bytes |
| `trove_http_sent_bytes_total` | counter | `handler` | Response body bytes |
| `trove_repository_requests_total` | counter | `repo`, `format`, `class`, `operation` (download, upload, metadata, delete, search), `outcome` (success, not_found, denied, error) | Package-protocol and raw-file operations per repository |
| `trove_repository_transferred_bytes_total` | counter | `repo`, `format`, `class`, `direction` (in, out) | Bytes per repository |
| `trove_remote_cache_lookups_total` | counter | `repo`, `kind` (metadata, artifact), `result` (hit, miss, stale, negative_hit) | Pull-through cache outcomes (INV-11) |
| `trove_remote_upstream_requests_total` | counter | `repo`, `code` (status code or `error`) | Requests sent to an upstream |
| `trove_remote_upstream_request_duration_seconds` | histogram | `repo` | Upstream response time including body |
| `trove_remote_upstream_up` | gauge | `repo` | 1 while the upstream counts as reachable, 0 while it is treated as unreachable ([repositories-and-proxy.md](repositories-and-proxy.md)) |
| `trove_remote_collapsed_requests_total` | counter | `repo` | Cache misses that waited for an in-flight fetch instead of fetching themselves |
| `trove_blobstore_operations_total` | counter | `operation` (put, get, head, delete, list, multipart), `outcome` | Object-storage calls |
| `trove_blobstore_operation_duration_seconds` | histogram | `operation` | Object-storage latency |
| `trove_blobstore_transferred_bytes_total` | counter | `direction` | Bytes to and from object storage |
| `trove_blobstore_dedup_hits_total` | counter | — | Uploads whose content address already existed (INV-4) |
| `trove_spool_used_bytes`, `trove_spool_free_bytes` | gauge | — | Local temporary upload spool |
| `trove_storage_blobs`, `trove_storage_blob_bytes`, `trove_storage_logical_bytes` | gauge | — | From the storage summary (R-OPS-023); same value on every replica |
| `trove_repository_files`, `trove_repository_bytes` | gauge | `repo`, `format`, `class` | From the storage summary |
| `trove_repositories` | gauge | `format`, `class` | Number of repositories |
| `trove_storage_summary_age_seconds` | gauge | — | Age of the cached storage summary |
| `trove_gc_runs_total` | counter | `outcome` | Garbage-collection runs ([storage.md](storage.md)) |
| `trove_gc_deleted_blobs_total`, `trove_gc_reclaimed_bytes_total` | counter | — | Garbage-collection effect |
| `trove_gc_last_success_timestamp_seconds` | gauge | — | End of the last successful run |
| `trove_db_pool_connections` | gauge | `state` (acquired, idle, constructing) | Connection-pool state |
| `trove_db_pool_max_connections` | gauge | — | Pool size |
| `trove_db_pool_acquire_duration_seconds` | histogram | — | Wait for a connection |
| `trove_db_pool_empty_acquires_total` | counter | — | Acquisitions that had to wait |
| `trove_db_slow_queries_total` | counter | `query` (name of the query in code) | Queries slower than `metrics.slow_query_threshold` |
| `trove_db_query_duration_seconds` | histogram | `query` | Only with `metrics.db_query_histograms: true` |
| `trove_jobs_queued` | gauge | `kind` | Queued jobs (from PostgreSQL) |
| `trove_jobs_oldest_queued_age_seconds` | gauge | `kind` | Age of the oldest queued job; the primary alerting signal for a stuck queue |
| `trove_jobs_running` | gauge | `kind` | Jobs this replica executes |
| `trove_jobs_completed_total` | counter | `kind`, `outcome` (succeeded, failed, retried, cancelled) | Job executions |
| `trove_jobs_duration_seconds` | histogram | `kind` | Job execution time |
| `trove_task_last_success_timestamp_seconds` | gauge | `task` | Last successful run of a scheduled task (R-OPS-019) |
| `trove_lease_held` | gauge | `name` | 1 on the replica that holds a singleton lease (INV-12) |
| `trove_notify_connected` | gauge | — | 1 while the `LISTEN` connection is up |
| `trove_notify_received_total` | counter | `channel` | Notifications received |
| `trove_cache_requests_total` | counter | `cache`, `result` (hit, miss) | In-process caches ([architecture.md](architecture.md)) |
| `trove_cache_entries` | gauge | `cache` | Entries per in-process cache |
| `trove_cache_flushes_total` | counter | `cache`, `reason` (notify, reconnect, size) | Cache invalidations |
| `trove_auth_attempts_total` | counter | `auth_method` (bearer, basic, nuget_apikey, session, oidc_login, ci_exchange), `outcome` (success, invalid, expired, revoked, denied) | Authentication outcomes |
| `trove_authz_decisions_total` | counter | `operation`, `outcome` (allow, deny) | Authorization decisions (INV-9) |
| `trove_audit_events_total` | counter | `kind` (category), `outcome` | Audit events recorded |
| `trove_audit_events_dropped_total` | counter | — | Access events lost to a full buffer (R-OPS-015) |
| `trove_webhook_deliveries_total` | counter | `subscription`, `outcome` (succeeded, failed, retrying, abandoned) | Delivery attempts |
| `trove_webhook_delivery_duration_seconds` | histogram | — | Receiver response time |
| `trove_webhook_render_duration_seconds` | histogram | — | Time to prepare a request (template, signing) |
| `trove_webhook_pending_deliveries` | gauge | — | Deliveries waiting or retrying (from PostgreSQL) |
| `trove_events_undispatched` | gauge | — | Outbox rows not yet dispatched (R-OPS-035) |
| `trove_tracing_spans_dropped_total` | counter | — | Spans lost to a full export queue |
| `trove_metrics_label_overflow_total` | counter | `family`, `label` | Values folded into `_other` |
| `trove_maintenance_mode` | gauge | `mode` | 1 for the current mode (R-OPS-024) |

</details>

**Acceptance criteria:**

- After a cold `npm install express --registry https://trove.example.com/api/npm/npm-all/`,
  `trove_remote_cache_lookups_total{repo="npm-remote",kind="artifact",result="miss"}` is greater
  than zero.
- After `npm cache clean --force` and a second install in a fresh directory, the `result="hit"`
  series of the same family grows instead.
- `trove_jobs_oldest_queued_age_seconds` rises when every worker is stopped (`roles: [api]` on
  all replicas) and a task is triggered.

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
| `replica` | Replica name |
| `trace_id`, `span_id` | Present whenever the record belongs to a request or a job (R-OPS-018) |
| `error` | Error text, when the record reports an error |
| `source` | `file:line`, only with `log.add_source: true` |

- **Attribute keys** are `snake_case` and stable. A renamed key is a breaking change for log
  pipelines, and the release notes MUST name it.
- **`log.format: text`** switches to slog's text handler for local development. Field names stay
  the same.
- **The ring buffer.** Every replica keeps its last `log.ring_buffer_records` records in memory,
  after redaction. Support bundles (R-OPS-027) take their logs from it, because trove itself
  holds no log files.

```yaml
log:
  format: json                    # default (proposed); json | text
  level: info                     # default (proposed); debug | info | warn | error
  levels: {}                      # default (proposed); example {proxy: debug, auth: warn}
  add_source: false               # default (proposed)
  ring_buffer_records: 5000       # default (proposed)
```

**Acceptance criteria:** `kubectl -n trove logs deploy/trove | jq -e 'has("time") and has("level") and has("log") and has("replica")'`
succeeds for every line.

### R-OPS-010 · Request log
Priority: **MVP** · Covers: OPS-019

Every completed request on the client listener MUST produce one `log: request` record at `INFO`.
The field set is fixed. A field that does not apply is `null`, and an unknown length is `-1`, so
consumers can rely on every key being present.

<details>
<summary>Fields of the request log record (21)</summary>

| Field | Content |
|---|---|
| `time` | Completion time |
| `trace_id` | Trace ID; also sent to the client as `X-Request-Id` |
| `listener` | `client` or `ops` |
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
| `principal` | User name, service-account name, `ci:<mapping>` or `anonymous` |
| `principal_type` | `user`, `service_account`, `ci`, `breakglass`, `anonymous` |
| `principal_id` | Opaque principal ID, `null` for anonymous callers |
| `token_id` | ID of the token used (never the token), `null` if none |
| `auth_method` | `bearer`, `basic`, `nuget_apikey`, `session`, `none` |
| `repo`, `format` | Repository key and format, if the request addressed one |
| `cache` | `hit`, `miss`, `stale`, `negative_hit` or `null` (remote and virtual reads) |
| `deny_reason` | Short code for `401`/`403` responses (`no_credentials`, `invalid_token`, `not_permitted`, …), else `null` |

</details>

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
| `purpose` | `remote` (pull-through fetch), `webhook`, `oidc_discovery`, `jwks`, `mail` |
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
| `collapsed_waiters` | Number of concurrent misses served by this fetch (INV-11) |

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
- **Propagation.** The override is stored in `system_state` (R-OPS-024) and announced by
  `NOTIFY trove_system_state`. A replica that starts later reads it, and each replica reverts it
  locally at `expiresAt`, so no timer needs coordinating.
- **Audit.** Each set and clear writes `system.log_levels_changed`.
- **Redaction.** Debug output follows R-OPS-013 like every other level.

```yaml
log:
  override_max_duration: 24h      # default (proposed)
```

**Acceptance criteria:** with three replicas, `PUT …/override` with
`{"levels":{"proxy":"debug"},"duration":"2m"}` makes `"component":"proxy","level":"DEBUG"` records
appear on all three within 5 s. They stop after 2 min without further calls.

### R-OPS-013 · Secret redaction
Priority: **MVP** · Covers: trove-only

No secret may leave the process through a log record, an audit record, a span, a support bundle, a
delivery-log entry or an error message. These rules are mandatory, and code review checks them
([docs/developer/](../developer/)).

1. **Secret-typed values.** Every secret held in memory (tokens, passwords, client secrets, webhook
   secrets, the object-store secret key) uses a dedicated type whose `LogValue()`
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

- **name**: `<domain>.<event>`, lower snake case, past tense, for example `repository.created` or
  `package.published`;
- **category**: `auth`, `token`, `identity`, `authz`, `repository`, `content`, `system`, `config`,
  `webhook`;
- **audit mode**: `transactional`, `async` or `none` (R-OPS-015);
- **webhook**: whether subscriptions may select the event (R-OPS-037);
- **data schema**: a JSON Schema of the event data, published through R-OPS-037.

Names are a public contract: audit exports, SIEM rules and webhook receivers parse them. A name is
never reused. A rename keeps the old name as an alias, emitted next to the new one for at least one
minor release, and the release notes MUST list it. Other spec pages add their events to the
registry under these rules; [storage.md](storage.md), for example, adds `retention_policy.*`.

**Acceptance criteria:** a test fails when a registry entry has no data schema, or when two entries
share a name.

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
  therefore never block request handling.
- **Log copy.** After the commit, each event is also written as a `log: audit` record with the same
  fields. The database copy is authoritative. A crash between commit and log write can lose the log
  copy, not the record.
- **Deduplication.** `authz.denied` is deduplicated per principal, action, repository and path
  within `audit.denial_dedup_window`: the first denial is recorded, and the record's
  `details.count` is raised for repeats. `auth.token_rejected` is deduplicated per token ID within
  `audit.token_rejection_dedup_window`.
- **Anonymous denials** are recorded only with `audit.record_anonymous_denials: true`. Otherwise
  they appear only in the request log and the metrics, so anonymous scanning cannot flood the
  table.
- **Successful downloads** are recorded only with `audit.record_downloads: true`, as asynchronous
  `artifact.downloaded` events. The request log holds them in every case.
- **Allow decisions.** Every authorization decision can be traced (INV-9). With the `authz`
  component at `debug` level, allow decisions are logged, though never stored as audit events.
- **Change details.** For an update, `details.changes` lists the changed fields as
  `{"field": "<path in the API representation>", "from": …, "to": …}`. A secret field appears only
  as `{"field": "…", "change": "set" | "changed" | "cleared"}`.

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
The `audit-retention` task (R-OPS-019) keeps the current month and the next three months created
ahead. The first migration creates the current and the next month, so an insert never finds a
missing partition.

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
- Ten pushes with a read-only token within one minute yield one `authz.denied` event with
  `details.count` = 10.

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
| `auth.token_rejected` | A | — | A well-formed trove token (valid checksum) was unknown, revoked or expired | `token_id` if known, `token_type`, `reason`, `count` |
| `token.created` | T | — | Any trove token was issued, including CI exchanges | `token_type`, `scope`, `expires_at`, `ci_mapping`, `ci_claims` |
| `token.revoked` | T | — | A token was revoked | `token_type`, `revoked_by` |
| `session.revoked` | T | — | An administrator ended another user's session | `session_id` |
| `user.created` | T | — | A user was created (just in time from ID-token claims, or by an administrator) | `source` (`jit`, `api`) |
| `user.updated` | T | — | Profile or group membership changed, including changes from claims at login | `changes` |
| `user.disabled`, `user.enabled`, `user.deleted` | T | — | User state change | — |
| `group.created`, `group.updated`, `group.deleted` | T | — | Group change, including claim-to-group mappings | `changes` |
| `service_account.created`, `service_account.updated`, `service_account.deleted` | T | — | Service-account change | `changes` |
| `admin_role.granted`, `admin_role.revoked` | T | — | System administration role or delegated system permission changed | `permission` |
| `permission_policy.created`, `permission_policy.updated`, `permission_policy.deleted` | T | — | Permission policy change (INV-9) | `changes` |
| `authz.denied` | A | — | An authenticated principal was refused an action | `operation`, `required_action`, `count` |
| `oidc_provider.created`, `oidc_provider.updated`, `oidc_provider.deleted` | T | — | Trusted issuer change | `changes` |
| `identity_mapping.created`, `identity_mapping.updated`, `identity_mapping.deleted` | T | — | CI identity mapping change | `changes` |
| `repository.created`, `repository.updated`, `repository.deleted` | T | — | Repository configuration change | `changes` |
| `repository.cache_invalidated` | T | — | R-OPS-021 | `path`, `scope`, `count` |
| `package.published` | T | yes | A package version was accepted (npm publish, NuGet push) | `package`, `version`, `files` |
| `package.deleted` | T | yes | A package version was deleted or unpublished | `package`, `version` |
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
  "actor": {"type": "ci", "id": "5f0e…", "name": "ci:acme-publish", "tokenId": "c41d…", "authMethod": "bearer"},
  "source": {"ip": "10.12.4.7", "userAgent": "npm/11.6.0 node/v24.11.0 linux x64 workspaces/false"},
  "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
  "replica": "trove-6f9c7d8b5-x2v4q",
  "resource": {"type": "package", "id": "npm-local/@acme/ui@2.4.0"},
  "repository": "npm-local",
  "details": {"package": "@acme/ui", "version": "2.4.0", "files": [{"path": "@acme/ui/-/ui-2.4.0.tgz", "sha256": "…", "size": 48213}]}
}
```

- **Exports are audited.** Every export writes `audit.exported`.
- **Push to a SIEM** goes through the `log: audit` stream and the platform's log shipping. The
  export endpoint covers pulls and backfills.
- **Retention.** The `audit-retention` task drops whole partitions whose newest possible timestamp
  is older than `audit.retention_days`. `0` keeps events for ever. Values below 90 log a warning at
  startup.
- **Restore.** Audit events live in PostgreSQL, so a database restore also restores them
  ([ha-and-helm.md](ha-and-helm.md)).
- **UI.** The UI offers an audit view backed by this API ([api-and-ui.md](api-and-ui.md)).

```yaml
audit:
  retention_days: 365             # default (proposed); 0 = keep for ever
```

**Acceptance criteria:**

- `curl -s -H "Authorization: Bearer $AUDITOR_TOKEN" 'https://trove.example.com/api/v1/audit/events/export?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z' | jq -c .action | sort | uniq -c`
  lists the month's actions. The export itself appears as `audit.exported`.
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

**Export** (`tracing.enabled: true`). Spans go over OTLP (gRPC or HTTP/protobuf) through a batch
processor. When the trove keys are unset, the standard `OTEL_EXPORTER_OTLP_*` variables of the
[OTLP exporter specification](https://opentelemetry.io/docs/specs/otel/protocol/exporter/) apply.
Resource attributes:

- `service.name=trove`;
- `service.version`;
- `service.instance.id` = replica name;
- Kubernetes attributes, where the downward API provides them.

**Spans:**

- One server span per request, named and attributed after the
  [HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/), with
  `http.route` set to the handler template.
- Child spans for authentication, authorization, database queries (statement with placeholders
  only), object-storage calls and upstream fetches.
- Collapsed cache misses add a span link to the fetching trace.
- Job spans link to the trace that enqueued the job, whose `traceparent` is stored with the job.
- Webhook deliveries get a span per attempt.

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
  - a client span to the npm upstream.
- The trace ID equals the response's `X-Request-Id` and the request log's `trace_id`.
- No exported attribute matches `trv_`.
- A request sent directly from outside the trusted proxies with a forged `traceparent` gets a new
  trace ID.

## Maintenance and administrative operations

### R-OPS-019 · Scheduled maintenance tasks and job visibility
Priority: **P1** · Covers: OPS-065, ART-051

**Tasks and leases.** A *task* is a named, recurring maintenance job. A singleton scheduler
enqueues due tasks into the job queue. The scheduler runs on the replica that holds the
`scheduler` advisory-lock lease (INV-12), and any worker executes the job
([architecture.md](architecture.md) owns the queue).

**Exactly-once enqueue.** Each due run carries the key `(task, scheduled_for)`. A unique constraint
on that key means a lease hand-over can never enqueue the same run twice.

**Missed runs** after a full outage are not caught up. Only the latest missed slot runs, once.

**Mutual exclusion.** A task does not start while another run of the same task is queued or
running. A manual trigger in that state returns the existing job with `409`.

| Task | Semantics owned by | Default schedule |
|---|---|---|
| `gc` | [storage.md](storage.md) (R-OPS-020 is the trigger) | set by storage.md; example `0 */4 * * *` |
| `storage-summary` | R-OPS-023 | `17 * * * *` (proposed) |
| `audit-retention` | R-OPS-017; also creates partitions ahead | `30 3 * * *` (proposed) |
| `webhook-retention` | R-OPS-044 and the outbox of R-OPS-035 | `*/15 * * * *` (proposed) |
| `support-bundle-expiry` | R-OPS-027 | `0 4 * * *` (proposed) |
| `replica-registry-prune` | R-OPS-003 | `*/10 * * * *` (proposed) |
| `remote-cache-cleanup` | [storage.md](storage.md), [repositories-and-proxy.md](repositories-and-proxy.md) (RET-036, RMT-021) | set by those pages |
| `token-cleanup`, `session-cleanup` | [identity-and-authentication.md](identity-and-authentication.md) | set by that page |

Retention policies ([storage.md](storage.md)) carry their own schedules and appear in the job list,
not in the task list.

**Schedules are configuration.** They live in `maintenance.tasks.<name>`, not in the database. The
API shows them and can trigger a run, but it cannot edit them.

API:

| Method and path | Request | Response | Permission |
|---|---|---|---|
| `GET /api/v1/system/tasks` | — | `200` per task: `name`, `schedule`, `enabled`, `nextRunAt`, `running`, `lastRun{jobId,startedAt,finishedAt,outcome,replica}` | `system.read` |
| `POST /api/v1/system/tasks/{name}/run` | optional task parameters, e.g. `{"dryRun":true}` | `202 {"job":{…}}`; `409 {"job":{…}}` if already queued or running; `404` unknown task; `503` in read-only mode for mutating tasks (R-OPS-024) | system administrator |
| `GET /api/v1/system/jobs` | `kind`, `state` (`queued`, `running`, `succeeded`, `failed`, `cancelled`), `limit`, `cursor` | `200` jobs with `id`, `kind`, `state`, `replica`, `enqueuedAt`, `startedAt`, `finishedAt`, `attempt`, `progress`, `error`, `triggeredBy` | `system.read` |
| `GET /api/v1/system/jobs/{id}` | — | `200` one job, including its result summary | `system.read` |
| `POST /api/v1/system/jobs/{id}/cancel` | — | `202`; the job stops at its next checkpoint; `409` if finished | system administrator |

A job reference returned by any trigger on this page has this shape:

```json
{"job": {"id": "01933b2a-…", "kind": "gc", "state": "queued", "href": "/api/v1/system/jobs/01933b2a-…"}}
```

```yaml
maintenance:
  tasks:
    storage-summary: {enabled: true, schedule: "17 * * * *"}       # default (proposed)
    audit-retention: {enabled: true, schedule: "30 3 * * *"}       # default (proposed)
    webhook-retention: {enabled: true, schedule: "*/15 * * * *"}   # default (proposed)
    support-bundle-expiry: {enabled: true, schedule: "0 4 * * *"}  # default (proposed)
    replica-registry-prune: {enabled: true, schedule: "*/10 * * * *"}  # default (proposed)
```

**Acceptance criteria:**

- With three replicas, each scheduled run of `storage-summary` appears exactly once in the job list
  over 24 h.
- Deleting the pod that holds the `scheduler` lease moves `trove_lease_held{name="scheduler"}`
  to another replica within one lease period, with no duplicate runs.
- `POST …/tasks/storage-summary/run` twice in a row returns `202`, then `409` with the same job ID.

### R-OPS-020 · Garbage-collection trigger
Priority: **MVP** · Covers: API-113

Garbage collection itself is specified in [storage.md](storage.md). This page specifies how it is
started and observed.

- `POST /api/v1/system/tasks/gc/run` (R-OPS-019) starts a run. Its body is
  `{"dryRun": false}` (default). A dry run reports what it would delete and deletes nothing.
- The job result contains at least: blobs examined, blobs deleted (or, in a dry run, blobs that
  would be deleted), bytes reclaimed, blobs skipped for the grace period, and errors. These figures
  feed the `trove_gc_*` families (R-OPS-008).
- Starting a run writes `system.task_triggered`.
- Permission: system administrator.

**Acceptance criteria:** delete a package version, wait for the grace period
([storage.md](storage.md)), then run `POST …/tasks/gc/run` with `{"dryRun":true}`. The job result
lists its blob as deletable, and the object still exists in the bucket
(`aws s3api head-object --bucket … --key <prefix>/sha256/…` succeeds). A real run then removes it,
and `trove_gc_deleted_blobs_total` grows.

### R-OPS-021 · Remote cache invalidation
Priority: **P1** · Covers: API-028

`POST /api/v1/repositories/{key}/cache/invalidate`

```json
{"path": "left-pad", "scope": "metadata"}
```

- `path` is a path or package-name prefix within the repository. Empty means the whole repository.
- `scope` is one of:
  - `metadata` (default): cached metadata expires now, and the next request revalidates it against
    the upstream. The stale copy is still served if the upstream fails (INV-11).
  - `negative`: cached misses are removed.
  - `all`: both of the above.
- Cached binaries are content-addressed and immutable, so invalidation never removes them.
  Removing them is a delete ([repositories-and-proxy.md](repositories-and-proxy.md)).
- On a **virtual** repository, the merged metadata cached for the path prefix is dropped. Its
  remote members are not touched.
- On a **local** repository, the call returns `400`.
- The change is a database update followed by `NOTIFY`, so every replica drops matching
  in-process entries within 2 s.
- Response: `200 {"invalidated": 12}`.
- Permission: repository action `manage` on `{key}`, or system administrator.
- Audit: `repository.cache_invalidated`.

**Acceptance criteria:** after
`curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"path":"left-pad"}' https://trove.example.com/api/v1/repositories/npm-remote/cache/invalidate`,
the next `npm view left-pad --registry …/npm-remote/` produces a `"purpose":"remote"` upstream
record. A second view within the metadata cache period produces none.

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
always get the cached result, never a live computation.

- **Source of the counts.** The aggregates come from the metadata schema of
  [storage.md](storage.md). storage.md MAY maintain incremental counters, which the task then reads
  instead of scanning.
- **Response format.** Numbers are plain integers and ratios. They are not formatted strings,
  unlike the catalog's behaviour.
- **What is counted.** The figures cover what PostgreSQL references. The bucket's billed usage
  differs: it also holds objects inside the garbage-collection grace period, orphans, and versions
  kept by bucket versioning. S3-compatible stores have no capacity, so the summary reports none.

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
| `GET /api/v1/system/storage/summary` | `200` (below); `404` before the first computation | `storage.read` |
| `POST /api/v1/system/storage/summary/refresh` | `202 {"job":{…}}`, or `409` with the running job | system administrator |

```json
{
  "computedAt": "2026-10-28T11:17:04.002Z",
  "durationMs": 5321,
  "blobs": {"count": 182344, "bytes": 912345678901},
  "logical": {"files": 240112, "bytes": 1203456789012},
  "deduplicationRatio": 0.758,
  "repositories": [
    {"key": "npm-remote", "format": "npm", "class": "remote", "files": 120034, "bytes": 493210000000, "packages": 8123, "versions": 45012, "shareOfLogicalBytes": 0.4098}
  ]
}
```

**Acceptance criteria:** after publishing a 1 MiB package to `npm-local` and refreshing, the
`npm-local` entry's `bytes` grows by the tarball size. `trove_storage_summary_age_seconds` drops
below 60, and a token without `storage.read` gets `403`.

### R-OPS-024 · Read-only maintenance mode
Priority: **P1** · Covers: trove-only

A system-wide mode stops content and configuration writes while reads keep working, for example
during a storage migration, an incident, or a contract migration (INV-3).

```sql
CREATE TABLE system_state (
  key         text        PRIMARY KEY,   -- 'installation_id', 'maintenance_mode', 'banner', 'log_level_override'
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
  - this endpoint, log-level overrides, support bundles;
  - audit writes and webhook delivery state.
- **Pull-through caching** continues while `cacheFill: true`. With `cacheFill: false`, remote
  repositories serve only what they already cache, as in global offline mode
  ([repositories-and-proxy.md](repositories-and-proxy.md), RMT-031).
- **Background work.** The scheduler does not enqueue mutating tasks (`gc`, retention, reindex,
  `remote-cache-cleanup`). Runs already in progress finish.
- **Readiness** is unaffected.
- **UI.** The UI shows the mode as a banner (R-OPS-025).

The mode is stored under `system_state` key `maintenance_mode` and propagated with
`NOTIFY trove_system_state`. Every replica also re-reads it every 30 s as a safety net. Each
change writes `system.maintenance_mode_changed`.

**Acceptance criteria:**

- In `read_only` mode, `npm publish --registry …/npm-local/` fails with `503` and shows the
  message, and `dotnet nuget push …` fails with `503`.
- `npm install` through `npm-all` succeeds, a new personal access token can be created, and an
  existing one revoked.
- All replicas enforce the mode within 2 s of the `PUT`.

### R-OPS-025 · System banner
Priority: **P1** · Covers: OPS-055

An administrator sets a message that the UI shows at the top of every screen.

- **Shape.** `{"enabled": true, "severity": "info" | "warning" | "critical", "title": "…", "message": "…"}`.
- **Message format.** The message is plain text with links in Markdown inline syntax
  `[text](https://…)`. The UI renders only text and `https` links, never HTML.
- **Storage.** It is stored under `system_state` key `banner`.
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
- A banner set on one replica is returned by every replica within 2 s.

### R-OPS-026 · Coordination lock diagnostics
Priority: **P1** · Covers: ADMAPI-078, OPS-009

`GET /api/v1/system/locks?category=` (requires `system.read`) shows the PostgreSQL
[advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS) held
or awaited by trove, so stuck leases and contended cache fills can be diagnosed without database
access.

- **How it is read.** The handler queries
  [`pg_locks`](https://www.postgresql.org/docs/current/view-pg-locks.html) (`locktype = 'advisory'`)
  joined with `pg_stat_activity`. It does not fan out to other replicas.
- **Requirements handed to [architecture.md](architecture.md):**
  - every trove connection sets `application_name` to `trove:<replica>:<pool>`, which maps a
    lock to its replica;
  - advisory locks use the two-key form, with the first key naming a registered category
    (`lease`, `migration`, `fetch`, `config`, …).
- **What it shows:**
  - category;
  - key: the registered name for leases, otherwise the hash in hex;
  - replica, backend PID, whether the lock is granted;
  - `waitStart` for waiters;
  - `heldSince` for leases, taken from the replica registry (R-OPS-003); `null` where PostgreSQL
    does not record it.
- **Limits.** The result is a point-in-time snapshot and not atomic.

```json
{
  "snapshotAt": "2026-10-28T12:01:00.000Z",
  "items": [
    {"category": "lease", "key": "scheduler", "replica": "trove-6f9c7d8b5-x2v4q", "pid": 4211, "granted": true, "heldSince": "2026-10-28T08:01:15.880Z", "waitStart": null},
    {"category": "fetch", "key": "9f2c11a0", "replica": "trove-6f9c7d8b5-k8m2p", "pid": 4310, "granted": false, "heldSince": null, "waitStart": "2026-10-28T12:00:58.120Z"}
  ]
}
```

**Acceptance criteria:**

- The `scheduler` lease appears exactly once, with the replica that `trove_lease_held` names.
- Ten parallel cold `npm install` runs of the same package show one granted `fetch` lock and
  waiters while the fetch runs.

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

1. The request creates the bundle row and enqueues a collection job, then announces the bundle with
   `NOTIFY trove_support`.
2. Every live replica writes its own part and uploads it to the blob store: ring-buffer logs,
   goroutine dumps and profiles from [`runtime/pprof`](https://pkg.go.dev/runtime/pprof), a metrics
   snapshot, readiness details.
3. The job adds the cluster part:
   - system information;
   - effective configuration (R-OPS-028);
   - a configuration export without secrets (R-OPS-032);
   - replicas, tasks, the last 100 failed jobs, locks;
   - schema version, table sizes and trove's sessions from `pg_stat_activity`;
   - with `slowQueries`, the top 20 statements from
     [`pg_stat_statements`](https://www.postgresql.org/docs/current/pgstatstatements.html) where
     that extension is installed.
4. The bundle turns `ready` when all live replicas have reported, or `partial` after
   `support.collect_timeout`.

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

- With three replicas, a bundle reaches `ready` and its tar contains `MANIFEST.json`, `cluster.tar.gz`
  and three `replica/<name>.tar.gz` parts.
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
4. the few command-line flags (`--config`, `--log-level`).

**Environment mapping.** Every key has exactly one variable: `TROVE_` followed by the key path,
upper-cased, with path segments joined by `_`. Example: `log.request.enabled` maps to
`TROVE_LOG_REQUEST_ENABLED`.

- **Ambiguity.** Keys contain underscores themselves, so a variable is never split naively. The
  names are generated from the configuration schema and matched exactly.
- **Value types.** Lists and maps in variables are JSON (`TROVE_LOG_LEVELS='{"proxy":"debug"}'`),
  booleans are `true`/`false`, durations are Go durations.

**Strict validation.** An unknown key in the file or an unknown `TROVE_` variable stops startup
with an error that names it and the closest valid key. So does a type or range error. Every error
is reported in one pass. A mistyped override therefore fails loudly instead of being dropped. The
only exempt variables are the reserved ones: `TROVE_CONFIG_FILE`, and `TROVE_CAC_*` for
configuration-as-code secret references (R-OPS-029).

**Secrets.** Every secret-typed key `<name>` has a companion `<name>_file`, and both can be set
through the environment. A secret value written inline into the YAML file MUST stop startup with an
error, because the file is typically a ConfigMap (INV-15). Setting both the value and `_file` is an
error.

**Changes take effect on restart.** The only live-adjustable item is the log-level override
(R-OPS-012). The chart rolls the pods when the rendered file changes
([ha-and-helm.md](ha-and-helm.md)).

**Commands and API:**

- `trove admin config validate [--config <file>]` validates the file plus the environment and
  prints the effective configuration, redacted, with each value's source.
- `GET /api/v1/system/config` returns the same data (`system.read`):
  `{"values": {"log.level": {"value": "info", "source": "default"}, "mail.password": {"value": "[REDACTED]", "source": "file:/var/run/secrets/trove/smtp"}}}`.

**Sections and their owners.** Each owner page lists its own keys. This page lists the keys of the
sections it owns, in the requirements above and below.

| Section | Owner |
|---|---|
| `server` | [architecture.md](architecture.md) (listeners, timeouts, shutdown); this page (`base_url`, `trusted_proxies`, `additional_base_urls`, `replica_header`) |
| `database`, `migrate`, `backup`, `runtime` | [ha-and-helm.md](ha-and-helm.md) |
| `storage`, `retention` | [storage.md](storage.md) |
| `auth` | [identity-and-authentication.md](identity-and-authentication.md) |
| `authz` | [authorization.md](authorization.md) |
| `ci` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `proxy` (including `proxy.outbound`), `repositories` | [repositories-and-proxy.md](repositories-and-proxy.md) |
| `npm`, `nuget` | [formats/npm.md](formats/npm.md), [formats/nuget.md](formats/nuget.md) |
| `api`, `ui` | [api-and-ui.md](api-and-ui.md) |
| `jobs`, `cluster`, `cache`, `secrets` | [architecture.md](architecture.md); [ha-and-helm.md](ha-and-helm.md) adds `jobs.background_migrations` |
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
  require_object_store: true                       # default (proposed)
  liveness_unready_grace: 0s                       # default (proposed)
  shutdown_drain_delay: 5s                         # default (proposed)
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
  oidcProviders:                   # trusted issuers for CI exchange (gitlab-ci-oidc.md)
    - name: gitlab
      issuer: https://gitlab.example.com
      audiences: ["https://trove.example.com"]
  groups:
    - name: developers
      claimValues: ["acme/developers"]   # group-claim mapping (identity-and-authentication.md)
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
  identityMappings:
    - name: acme-publish
      provider: gitlab
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
| `oidcProviders`, `identityMappings` | [gitlab-ci-oidc.md](gitlab-ci-oidc.md) |
| `retentionPolicies` | [storage.md](storage.md) |
| `webhooks` | R-OPS-038 |

So the document needs no second schema: the OpenAPI document generates its JSON Schema.

**An object is the complete desired state of that object.** Omitted fields take their defaults and
are not merged with the stored values. This makes the result independent of history, and it is
what a Git review shows. There is no partial-patch form, and there is no `null`-means-delete
convention: an object is deleted by removing it from a document that has `prune: true`.

**Ownership.** Each object is owned by at most one document, recorded in
`config_managed_object`.

- **Conflicts.** A document that declares an object owned by another document, or an existing
  unmanaged object with the same key, has a conflict (R-OPS-030), unless the apply request sets
  `adopt: true`.
- **Locking.** With `config_as_code.lock_managed_objects: true`, the API and UI reject changes to
  a managed object's configuration with `409` and name the owning document (Q-OPS-8).
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
field.

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

- Apply runs under the advisory lock `config` in one PostgreSQL transaction. Every change commits,
  or none does.
- Creates and updates run in dependency order: `oidcProviders`, `groups`, `serviceAccounts`, local
  and remote `repositories`, virtual `repositories`, `identityMappings`, `permissionPolicies`,
  `webhooks`, `retentionPolicies`. Deletes run in reverse order.
- Other replicas learn of the changes through the normal `NOTIFY` invalidation.

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

**Acceptance criteria:**

- `curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/yaml' --data-binary @baseline.yaml https://trove.example.com/api/v1/config/diff | jq .summary`
  shows the expected counts. Applying with the returned `planHash` succeeds, and a second diff
  shows only `unchanged`.
- Changing one repository through the UI between diff and apply makes the apply return `412`.
- Applying a document with an empty `spec` and `prune: true` returns `409` and changes nothing.
- A fault injected in the middle of an apply leaves the database without any of its changes.

### R-OPS-031 · Startup bootstrap from configuration documents
Priority: **P1** · Covers: OPS-046, HA-037

When `config_as_code.files` names files (glob patterns, applied in lexical order), each replica
does the following at startup, after the schema check and before it reports ready:

1. **Lock and apply.** It takes the advisory lock `config`, then applies each document. Replicas
   that start together therefore apply once, and the rest see an empty diff.
2. **Mode.**
   - `mode: apply` reconciles: create, update, and prune per document. Changes that need
     `allowDestructive` are never applied at startup.
   - `mode: create_only` creates missing objects and never updates or deletes. Objects created this
     way are not managed, which gives a seed for a fresh installation that is then maintained
     through the UI.
3. **Failure.** When a document is invalid, has conflicts or contains blocked changes, the replica
   logs the plan, keeps the `configBootstrap` readiness check failing, and retries every minute. In
   a rolling update the new pods never become ready, so the rollout stalls while the old replicas
   keep serving. The operator resolves the problem with the CLI or API (R-OPS-030).

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
UI, so every raw download MUST carry:

- `X-Content-Type-Options: nosniff`;
- `Content-Security-Policy: sandbox; default-src 'none'`;
- for active types (`text/html`, `application/xhtml+xml`, `image/svg+xml`, `text/xml`,
  `application/xml`, `text/javascript`, `application/javascript`, `application/pdf`),
  `Content-Disposition: attachment`.

An override cannot remove these headers.

Package-protocol endpoints set the content types their clients expect (format pages), not this
table.

```yaml
content_types:
  overrides: {}                   # default (proposed); example {".mjs": "text/javascript", ".sbom.json": "application/json"}
```

**Acceptance criteria:**

- `curl -sI …/files/generic-local/docs/index.html` shows `Content-Disposition: attachment`,
  `Content-Security-Policy: sandbox; default-src 'none'` and `X-Content-Type-Options: nosniff`.
- `curl -sI …/files/npm-local/@acme/ui/-/ui-2.4.0.tgz` shows `Content-Type: application/gzip`.

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
memory, refreshed through `NOTIFY trove_webhooks`. When no subscription covers a domain, no
outbox row is written, so webhooks cost nothing until someone uses them.

**When a new subscription starts.** A subscription receives events committed after it is enabled,
plus the propagation delay (≤ 2 s). Earlier events are not delivered.

**Dispatch.**

- Committing an outbox row sends `NOTIFY trove_events`, and any worker dispatches.
- The dispatcher claims undispatched rows with `FOR UPDATE SKIP LOCKED` in batches of up to 100.
- For each event it evaluates every enabled subscription (filter and owner permission, R-OPS-038
  and R-OPS-039).
- It inserts one `webhook_delivery` row and one delivery job per match, then marks the event
  dispatched, all in one transaction.
- A five-second poll covers lost notifications.

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
- With one subscription, killing every worker for five minutes and then restarting them delivers
  every event published meanwhile exactly once or more, and never loses one.
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
  "trovereplica": "trove-6f9c7d8b5-x2v4q",
  "trovetest": false,
  "data": {
    "repository": {"key": "npm-local", "format": "npm", "class": "local"},
    "package": {"name": "@acme/ui", "version": "2.4.0"},
    "files": [{"path": "@acme/ui/-/ui-2.4.0.tgz", "sha256": "9b74c9897bac770ffc029102a200c5de…", "size": 48213}],
    "actor": {
      "type": "ci",
      "id": "5f0e…",
      "name": "ci:acme-publish",
      "tokenAuthenticated": true,
      "authMethod": "bearer",
      "ci": {"issuer": "https://gitlab.example.com", "project_path": "acme/ui", "ref": "main", "pipeline_id": "184467", "job_id": "9921034"}
    }
  }
}
```

- **Standard attributes.**
  - `source` is `server.base_url`.
  - `type` is `trove.` followed by the registry name (Q-OPS-5).
  - `subject` is `<repository>/<path or package@version>`.
- **Extension attributes** (lower-case, as CloudEvents requires):
  - `trovesubscription`: the matching subscription;
  - `trovereplica`: the replica that produced the event;
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
- **Deleting a folder** emits one `artifact.deleted` per file. One npm publish emits one
  `package.published` and one `artifact.deployed`.
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
| `GET /api/v1/webhooks/{key}` | `200`; `404` |
| `PUT /api/v1/webhooks/{key}` | Full replacement of everything except `key` and `owner`; `200`; `404`; `412` on an `If-Match` revision mismatch |
| `DELETE /api/v1/webhooks/{key}` | `204`; pending deliveries are abandoned |
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

**Template language.** Go [`text/template`](https://pkg.go.dev/text/template), executed with
`missingkey=error`.

- **Root fields.**
  - `.event`: the whole envelope (R-OPS-036);
  - `.data`: shortcut to `.event.data`;
  - `.actor`: shortcut to `.event.data.actor`;
  - `.secrets.<name>`: the handler secrets.
- **Functions.** The `text/template` built-ins, plus:
  - `json`: encodes any value as JSON, including correct string escaping;
  - `yaml`;
  - `base64`;
  - `default`.

  Template authors use `json` for values placed in JSON bodies. An unescaped scalar inside a JSON
  string is the author's error, and the test delivery shows it.
- **Forbidden actions.** `define`, `template` and `block` are rejected when the template is stored
  (`422`), so templates cannot recurse.
- **Limits.**
  - template source ≤ 64 KiB;
  - rendered body ≤ 1 MiB;
  - header values are rendered with the same data.
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

### R-OPS-042 · Delivery, retries and outbound protection
Priority: **P1** · Covers: EVT-001, EVT-028, EVT-029

**Jobs.** Each `webhook_delivery` row is executed by a job of kind `webhook.deliver` in the job
queue ([architecture.md](architecture.md) R-ARCH-016). A retry reschedules the same job with a
later `run_at`.

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

**Concurrency.** Each replica runs at most `webhooks.workers` deliveries. Each subscription has at
most `webhooks.max_in_flight_per_subscription` deliveries in flight across all replicas: the claim
query skips subscriptions at their limit. A slow receiver therefore delays only its own
subscription.

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
  workers: 8                            # default (proposed); per replica
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
- Each principal may run 10 tests per minute; beyond that the call returns `429`.

**Acceptance criteria:**

- Testing the GitLab recipe against a project with a valid trigger token returns
  `"success": true`, and a pipeline appears.
- The recorded request shows `«redacted:trigger_token»` instead of the token.

### R-OPS-044 · Delivery log
Priority: **P1** · Covers: EVT-032, EVT-033, API-155

Delivery records live in PostgreSQL (INV-3; no separate key-value store).

**Recording.**

- Failed and abandoned deliveries are always kept.
- Successful deliveries are kept only when the subscription has `recordSuccessfulDeliveries`.
  Otherwise their rows are deleted when they succeed.
- One record per delivery. Retries update the record and append to its attempt list (at most the
  last 10 attempts).

**Masking.** Before storage, every value of the subscription's secrets in the URL, headers and
body is replaced with `«redacted:<name>»`. So are its base64 and URL-encoded forms. The values of
`Authorization`, `Cookie` and signature headers are replaced entirely (R-OPS-013, H-42).

**Retention.** `webhook-retention` deletes:

- failed and abandoned records after `failed_retention_days`;
- successful records after `success_retention_days`;
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
  created_at        timestamptz NOT NULL DEFAULT now(),
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
```

API (requires `webhooks.read`, or ownership of the subscription):

| Method and path | Parameters / response |
|---|---|
| `GET /api/v1/webhooks/deliveries` | `subscription`, `state`, `eventType`, `target` (host), `from`, `to`, `limit`, `cursor` → `200` summaries, newest first |
| `GET /api/v1/webhooks/deliveries?follow=true` | Same filters. `200 text/event-stream` ([Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)): one `delivery` event per new or updated record, driven by `NOTIFY`, with a comment line every 15 s to keep the connection open |
| `GET /api/v1/webhooks/deliveries/{id}` | `200` full record with request, response, attempts and the event envelope |
| `POST /api/v1/webhooks/deliveries/{id}/redeliver` | `202`; resets a `failed` or `abandoned` record to `pending` and queues a new attempt series for the same event, with the attempt list continuing; `409` while the record is pending or in flight; requires the right to manage the subscription; writes `webhook.redelivered` |
| `GET /api/v1/webhooks/{key}/status` | `200 {"lastDelivery":{…},"pending":3,"failedLast24h":1}` |

```yaml
webhooks:
  delivery_log:
    failed_retention_days: 14           # default (proposed)
    success_retention_days: 3           # default (proposed)
    max_records_per_subscription: 10000 # default (proposed)
    max_body_bytes: 65536               # default (proposed)
```

**Acceptance criteria:**

- A failing receiver produces one record with an attempt list that grows per retry.
- `curl -N -H "Authorization: Bearer $TOKEN" 'https://trove.example.com/api/v1/webhooks/deliveries?follow=true&subscription=gitlab-trigger-npm'`
  shows a `delivery` event within 2 s of a publish.
- `redeliver` of an abandoned record produces another attempt with the same `webhook-id`, on the same record.

## Backup and restore

[ha-and-helm.md](ha-and-helm.md) owns backup and disaster recovery (OPS-033 and its own
requirements). Its likely means are PostgreSQL point-in-time recovery from the platform (INV-12)
and S3 bucket versioning or replication. This page adds the following facts the backup design
needs.

- **What is in PostgreSQL.** Everything this page stores lives there: audit events, `system_state`,
  configuration documents and ownership, webhook subscriptions and secrets (encrypted),
  outbox and delivery log, support-bundle metadata and the storage summary. A database restore
  brings all of it back to the restore point.
- **The stored-secret key.** Encrypted values (webhook secrets, upstream credentials) can only be
  read with that key (IAM-045). The key is backed up separately from the database dump, and a
  restore runbook that forgets it loses every encrypted value. Where the key lives:
  [architecture.md](architecture.md) and [ha-and-helm.md](ha-and-helm.md).
- **Blobs against a restored database.** Blobs referenced by a restored database may already be
  gone, deleted by garbage collection after the restore point. Either the bucket keeps deleted
  object versions at least as long as the database point-in-time window, or garbage collection's
  grace period ([storage.md](storage.md)) is at least that long. ha-and-helm.md picks one and
  states it.
- **After a restore**, run `trove migrate`, then start with read-only mode on (R-OPS-024) until
  checks pass. Before switching to `normal`, verify with a blob-reference check
  ([storage.md](storage.md)) that every referenced blob exists. Webhook deliveries that were in
  flight at the restore point are delivered again, which is consistent with at-least-once delivery
  (R-OPS-035).
- **Configuration as code.** Documents kept in Git (R-OPS-029) and exports (R-OPS-032) are a
  logical backup of the database-held configuration that survives the loss of the database. The
  configuration file and Kubernetes Secrets are part of the Helm release.

## Handed over to other pages

MVP and P1 features of the catalog pages above that this page does not specify:

| Catalog ID | Feature | Owner |
|---|---|---|
| OPS-033 | Component backup and disaster recovery | [ha-and-helm.md](ha-and-helm.md) |
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
| HA-046, HA-047, HA-055 | Chart probes, termination, log and metrics integration | [ha-and-helm.md](ha-and-helm.md); it consumes R-OPS-001, R-OPS-006 and R-OPS-009 |

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
| OPS-032, OPS-034 to OPS-039 | P2 | Scheduled full backups, system and repository export and import, instance migration by export |
| OPS-041, OPS-042 | P2 | Advanced runtime properties and a whole-configuration descriptor; the configuration file and configuration documents replace both |
| OPS-047, OPS-048, OPS-049 | P2 | Authentication-provider bootstrap file, identity-service configuration documents, OAuth application integrations |
| OPS-053, OPS-054, OPS-056 | P2 | Server name and date format, custom logo, login notice |
| OPS-058, OPS-059 | P2 | Folder download limits, global replication blocking |
| OPS-061 | P2 | Reverse proxy configuration generator |
| OPS-064 | P2 | Load protection with per-activity throttling (`429`) |
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
  readiness reveals dependency latency. `metrics.auth: static` adds a bearer check that keeps
  working during a database outage (H-41).
- **Revocation depends on `LISTEN`.** Prompt token revocation (INV-8) relies on cross-replica
  invalidation. A replica whose `LISTEN` connection is down turns unready, and drops its caches
  before it reports ready again (R-OPS-001). Without that rule, a revoked token could stay valid on
  one replica for the cache lifetime.
- **Forwarded headers.** They are believed only from `server.trusted_proxies`. That rule governs
  URL generation (R-OPS-005), client addresses in logs and audit (R-OPS-010, R-OPS-015) and the
  continuation of trace context (R-OPS-018). A too-wide `trusted_proxies` lets clients forge their
  audit address.
- **Audit integrity.** Every state change and its audit record are atomic (R-OPS-015). Access
  events can be dropped under overload, which the dropped counter makes visible. The records are
  not tamper-evident (H-39).
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
  `urlquery` to keep it from breaking the receiver's syntax (R-OPS-041).
- **Raw downloads** are served with `nosniff`, a sandboxing CSP and `attachment` for active
  types, so uploaded HTML or SVG never runs in the UI's origin (R-OPS-034).
- **Support bundles** are administrator-only and audited. Their manifest is meant to be reviewed
  before sharing. They exclude identity data, tokens, audit events and package content
  (R-OPS-027).

## Open questions

### Q-OPS-1 · Couple liveness to prolonged unreadiness?

The catalog couples them by default (OPS-004).

- **(a)** Never couple. Liveness reflects only the process.
- **(b)** Couple after a grace period for every check.
- **(c)** Couple only for internal checks (`notify`, `startup`), never for dependency checks.

**Recommendation: (a) as the default, with `health.liveness_unready_grace` as an opt-in (b).** A
dependency outage affects every replica at once, and restarting them all adds load at the worst
moment. Option (c) needs per-check policy for little gain, because the `notify` check already
recovers by reconnecting.

### Q-OPS-2 · Should object-storage reachability gate readiness?

INV-12 says yes. But if S3 is unreachable, every replica turns unready at once. The Service then
has no endpoints, even though logins, token exchange, metadata of local repositories and cached
metadata could still be served.

- **(a)** Keep the invariant: S3 failure makes replicas unready.
- **(b)** Gate only on PostgreSQL. On S3 failure, blob reads and writes answer `503` per request,
  and a `degraded` flag is shown.
- **(c)** Make it configurable with (a) as the default.

**Recommendation: (c), implemented as `health.require_object_store`, with (b) put to the owner as a
change to INV-12.** Partial service is better for CI pipelines that mostly resolve metadata, and
INV-12 stays intact until the owner decides.

### Q-OPS-3 · Anonymous access to the version endpoint

The catalog answers anonymously (OPS-005).

- **(a)** Anonymous by default.
- **(b)** Authenticated by default, with `system.public_version` as an opt-in.

**Recommendation: (b).** npm and NuGet clients never call it, and an exact version helps an
attacker pick known vulnerabilities. Ping stays anonymous for reachability checks.

### Q-OPS-4 · Tamper evidence for audit records

- **(a)** Rely on shipping the `log: audit` stream to an external append-only store.
- **(b)** Hash-chain the audit rows in PostgreSQL, and verify them in the export.
- **(c)** Both.

**Recommendation: (a) for P1, (b) as a P2 hardening.** trove's own database role must be able to
drop partitions for retention, so it can also delete rows. Only an external copy protects against
someone holding that role, and a hash chain without an external anchor detects only partial edits.

### Q-OPS-5 · CloudEvents `type` prefix

CloudEvents recommends a reverse-DNS prefix. The prefix becomes a public contract at the first
release.

- **(a)** `trove.` as specified.
- **(b)** The reverse DNS of a project domain, once the project owns one.

**Recommendation: (a)**, unless the owner names a domain before the first release that ships
webhooks.

### Q-OPS-6 · The human-login OIDC client in configuration documents

The browser-login OIDC client (INV-7) could live in two places.

- **(a)** The configuration file only: available before any administrator exists, client secret
  from a file.
- **(b)** Also a `spec.loginProviders` kind in configuration documents.

**Recommendation: (a)**, and [identity-and-authentication.md](identity-and-authentication.md)
decides. A login provider that only a database apply can create cannot recover a lost login, and
the file path already serves GitOps.

### Q-OPS-7 · One cron syntax for the whole system

- **(a)** Five-field Kubernetes CronJob syntax with macros, in UTC.
- **(b)** Six-field syntax with a seconds field, as in the catalog (OPS-044).

**Recommendation: (a)**, used identically by [storage.md](storage.md) for retention policies.
Operators already know it from Kubernetes, and no trove task needs second precision. Two syntaxes
in one product would be a certain source of errors.

### Q-OPS-8 · Locking objects managed by configuration documents

- **(a)** Locked against API and UI changes by default (`lock_managed_objects: true`).
- **(b)** Unlocked, with drift reported by the next diff.
- **(c)** A per-document setting.

**Recommendation: (a), with the global switch.** Silent drift that the next apply reverts is worse
than a clear `409` naming the document. Per-document control (c) can come later if teams mix both
styles.

### Q-OPS-9 · Ownership of the job API

R-OPS-019 specifies `/api/v1/system/jobs`, but the job model belongs to
[architecture.md](architecture.md).

- **(a)** This page keeps the read and cancel API, and architecture.md the model.
- **(b)** Move both to architecture.md.

**Recommendation: (a)**, confirmed in the consistency review. Job visibility is an operator
concern, and the API is a thin view over the model.

### Q-OPS-10 · Default cap for the `repo` metric label

- **(a)** 500 per family and replica, as specified.
- **(b)** 100.
- **(c)** No label by default; only repositories that match `repository_label_include`.

**Recommendation: (a).** Most installations stay below it, so dashboards work without
configuration. The overflow counter shows when an installation outgrows it, and the include list
then makes the labelling deterministic.

## What this does not cover

### H-39 · Audit records are not tamper-evident

Audit events in PostgreSQL can be altered or deleted by anyone holding trove's database role or a
superuser role. The role needs `DROP` on partitions for retention. Integrity rests on shipping the
`log: audit` stream to a store outside trove's control, for example object storage with object
lock or a SIEM. Neither trove nor the chart enforces that (Q-OPS-4). The gap is live from the
first release for any installation that does not ship logs externally.

### H-40 · Outbound address checks are weaker through an outbound proxy

When a webhook handler names an outbound proxy (R-OPS-042), the proxy resolves and connects to the
target. trove can only check its own resolution of the name before sending, so a name that
resolves differently for the proxy, or changes in between, reaches whatever address the proxy
chooses. Proxy-side egress rules are the only full control. The gap is live only for subscriptions
that use a proxy.

### H-41 · The operations listener relies on the NetworkPolicy

With the default `metrics.auth: none`, any pod that can reach port 8081 reads metrics: repository
keys, request and byte volumes, queue depth. It also reads verbose readiness: dependency latency,
schema version. The chart's NetworkPolicy (INV-13) is the control. Clusters whose CNI does not
enforce NetworkPolicy, or installations that switch it off, expose this data cluster-wide unless
`metrics.auth: static` is set.

### H-42 · Secret masking in the delivery log matches known encodings only

The delivery log masks secret values in plain, base64 and URL-encoded form, and replaces the values
of authorization-type headers entirely (R-OPS-044). A template that transforms a secret further,
for example by hashing it or embedding it in a JWT, puts a derived value into the stored request
that masking does not recognise. That derived value is readable by holders of `webhooks.read`. The
gap is live for templated handlers with such templates. Review templates, and grant `webhooks.read`
narrowly.
