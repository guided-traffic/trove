# Monitoring and operations

This page is for the people who keep a trove installation running. It covers the health and readiness endpoints, the metrics and logs, how to follow one request through them, the System status, Jobs and Audit log screens, what happens when a pod stops or a node is drained, how to scale the two roles, how trove behaves while Valkey, PostgreSQL or the object store is unavailable, and which signals deserve an alert.

## The ops listener

Every `api`, `packages` and `worker` process opens the ops listener on port 8081 (`server.ops_listen`), served over TLS with the chart's defaults. It has three paths and no sign-in of its own; the chart's NetworkPolicy admits port 8081 only from the namespace `monitoring` (`networkPolicy.ingress.ops.from`) and from `networkPolicy.probeCIDRs`.

| Path | Answer |
|---|---|
| `GET /healthz` | Liveness: `200 {"status":"alive"}` while the replica's internal loops make progress, `503` with the names of stuck loops otherwise |
| `GET /readyz` | Readiness: `200 {"status":"ready"}`, with `"degraded":[…]` when a reported check fails; `503 {"status":"unready","failing":[…]}` otherwise |
| `GET /readyz?verbose=1` | The same, plus the replica, its role and every check with its status, a fixed reason code and its latency; never addresses or error texts |
| `GET /metrics` | Prometheus text format; off with `metrics.enabled: false`; with `metrics.auth: static` a `Bearer` token is required |

The chart's probes use these paths: startup and liveness `/healthz`, readiness `/readyz`, all over HTTPS on the named port `ops`. The kubelet does not verify the certificate.

### Readiness checks

| Check | Holds the replica unready |
|---|---|
| `startup` | until start-up has finished |
| `database`, `schema`, `invalidation` | while PostgreSQL is unreachable, the schema is behind this version, or the change log cannot be read. After the first readiness, an outage shorter than `health.database_outage_grace` (20 s) only shows as degraded |
| `tls` | until every listener certificate has loaded once |
| `outbound` | until the CA files of `proxy.outbound.tls.ca_files` have loaded once |
| `objectStore` | until the first readiness; for the whole life of the process with `health.require_object_store: true` |
| `valkey` | never, except a configuration error at start; reason codes such as `connecting`, `no_sentinel`, `timeout`, `auth_failed`, `circuit_open` |
| `draining` | from the start of a shutdown |

## Metrics

The families are named `trove_*`, plus the Go runtime and process families. The label `endpoint` holds the configured endpoint names (`default` for the main listener), so its values are bounded. The chart renders no ServiceMonitor yet; create one yourself, for example through `extraObjects`:

```yaml
apiVersion: monitoring.coreos.com/v1    # example
kind: ServiceMonitor                      # example
metadata:
  name: trove                                     # example
  namespace: monitoring                           # example
spec:
  namespaceSelector: {matchNames: [trove]}        # example: the release namespace
  selector:
    matchLabels:
      app.kubernetes.io/instance: trove           # example: the release name
      app.kubernetes.io/component: ops            # example: the label of the ops Service <fullname>-ops
  endpoints:
    - port: ops                                   # example: the port name of the ops Service
      scheme: https                               # example: https while tls.enabled
      path: /metrics                              # example
      tlsConfig:
        ca: {secret: {name: trove-tls, key: ca.crt}}   # example: a Secret in the monitoring namespace with trove's CA
        serverName: trove-ops.trove.svc           # example: a name the ops certificate holds
```

The ops Service selects every `api`, `packages`, `worker` and `web` pod, so each replica is scraped. `trove_build_info{role}` and the `role` of every log record name the role of the process (`api`, `packages`, `worker`, `web` or `all`); the ServiceMonitor copies `role` and `pool` onto every other series. To require a token, set `config.metrics.auth: static` and name a Secret in `secrets.metricsToken`; trove reads the token file on every scrape, so a rotated token applies without a restart.

<details>
<summary>Metric families by area</summary>

| Area | Families |
|---|---|
| Build and readiness | `trove_build_info` (version, revision, role), `trove_ready` |
| HTTP | `trove_http_requests_total`, `trove_http_request_duration_seconds` (labels `listener`, `endpoint`, `handler`, `method`, `code`), `trove_http_requests_in_flight`, `trove_http_requests_shed_total` (`reason`: `in_flight`, `memory_slots`, `heap`), `trove_http_memory_slots`, `trove_http_transfers_cut_total` |
| Endpoints and exposure | `trove_endpoint_listener_up`, `trove_endpoint_misdirected_total`, `trove_endpoint_forwarded_host_ignored_total`, `trove_endpoint_credentials_refused_total`, `trove_endpoint_cache_fill_refused_total`, `trove_exposure_rules`, `trove_exposure_hidden_total` |
| TLS | `trove_tls_certificate_expiry_timestamp_seconds`, `trove_tls_reloads_total`, `trove_tls_reload_errors_total`, `trove_tls_temporary_certificate` |
| Sign-in and permissions | `trove_auth_attempts_total`, `trove_auth_ratelimited_total`, `trove_auth_csrf_failures_total`, `trove_auth_lastused_flush_failures_total`, `trove_authz_decisions_total`, `trove_authz_index_staleness_seconds`, `trove_authz_stale_waits_total` |
| Rate limits | `trove_api_rate_limited_total`, `trove_ratelimit_decisions_total`, `trove_ratelimit_fallback` |
| Coordination | `trove_cluster_changes_applied_total`, `trove_cluster_poll_errors_total`, `trove_cluster_poll_last_success_timestamp_seconds`, `trove_lease_held`, `trove_lease_lost_total` |
| Valkey | `trove_valkey_available`, `trove_valkey_circuit_open`, `trove_valkey_sentinels_reachable`, `trove_valkey_pubsub_subscribed`, `trove_valkey_primary_changes_total`, `trove_valkey_primary_views`, `trove_valkey_errors_total` |
| Jobs | `trove_jobs_queued`, `trove_jobs_oldest_queued_age_seconds`, `trove_jobs_running`, `trove_jobs_claimed_total`, `trove_jobs_completed_total`, `trove_jobs_duration_seconds`, `trove_jobs_claim_errors_total` |
| PostgreSQL | `trove_db_pool_connections`, `trove_db_pool_max_connections`, `trove_db_pool_empty_acquires_total`, `trove_db_pool_canceled_acquires_total`, `trove_db_pool_connections_opened_total`, `trove_db_pool_acquire_duration_seconds` (label `db_pool`: `request`, `control`, `lease`), `trove_db_retries_total` (`kind`), `trove_db_commit_unknown_total` |
| Upload spool and garbage collection | `trove_spool_used_bytes`, `trove_spool_free_bytes`, `trove_gc_last_success_timestamp_seconds` (no series on a replica until a run there has ended), `trove_gc_deleted_blobs_total`, `trove_gc_reclaimed_bytes_total` |
| Object store and upstreams | `trove_storage_s3_requests_total`, `trove_storage_s3_request_duration_seconds`, `trove_outbound_requests_total`, `trove_outbound_request_duration_seconds`, `trove_outbound_egress_denied_total` |
| Caches | `trove_cache_requests_total`, `trove_cache_entries`, `trove_cache_bytes`, `trove_cache_flushes_total` |
| Vulnerability scanning | `trove_vuln_source_last_success_timestamp_seconds`, `trove_vuln_source_records`, `trove_vuln_source_consecutive_failures` (labels `source`, `ecosystem`), `trove_vuln_sync_runs_total`, `trove_vuln_sync_records_total`, `trove_vuln_unevaluable_ranges_total`, `trove_vuln_evaluations_total`, `trove_vuln_evaluation_duration_seconds`, `trove_vuln_findings` (`severity`, `status`, `kind`), `trove_vuln_downloads_blocked_total`, `trove_vuln_audit_requests_total`, `trove_vuln_claimed_name_matches` (`ecosystem`, `kind`) |
| Audit and telemetry | `trove_audit_events_total`, `trove_audit_events_dropped_total`, `trove_metrics_label_overflow_total`, `trove_tracing_spans_dropped_total` |

Gauges read from PostgreSQL, such as `trove_jobs_queued`, `trove_jobs_oldest_queued_age_seconds` and `trove_valkey_primary_views`, are reported by every replica; aggregate them with `max`, not `sum`.

</details>

## Logs

trove writes one JSON object per line to standard output (`log.format: text` for a readable form). Every record has `time`, `level`, `msg`, `replica`, `role` and `log`, which names the stream:

| `log` | Content |
|---|---|
| `app` | start-up, warnings, errors; `component` names the part of trove |
| `request` | one record per request on a client listener: `endpoint`, `host`, `method`, `path`, `status`, `duration_ms`, `request_bytes`, `response_bytes`, `principal`, `principal_type`, `auth_method`, `token_id`, `repo`, `remote_addr`, `user_agent`, `trace_id` |
| `upstream` | one record per request trove sends to an upstream registry |
| `audit` | a copy of every audit event |

Secrets are redacted in every record: tokens, passwords, `Authorization` and cookie values, URL credentials and the query parameters of `log.request.redact_query_params`. Probe and metrics requests are not logged unless `log.request.ops_listener: true`.

```yaml
log:
  level: info                           # default: debug | info | warn | error
  levels: {}                            # default; per component, example {proxy: debug}
  request:
    enabled: true                       # default
    ops_listener: false                 # default
```

## Follow one request

Every answer carries `X-Request-Id`. Problem answers repeat it as `requestId`, and the web UI shows it as **Request ID** with the hint "Give this ID to your administrator." The same value is the `trace_id` of the request's log records, the trace ID of its spans when tracing is on, and the **Trace ID** of the audit events it caused. To find those events, use `GET /api/v1/audit/events?traceId=<id>`; the screen has no filter for it.

## System status

**Admin area › System › System status** (capability `settings:read`: roles `admin`, `security-admin`, `auditor`) refreshes every 15 seconds; **Pause automatic refresh** stops that. The [Admin dashboard](dashboard.md) has the overview with the load of each role and the counts; this screen has every replica.

- A summary line: **All ready**, **Degraded** or **Replicas unready**, and **Not drain-safe** when a role has fewer than two ready replicas or all of them on one node.
- **Installation**: version, **Base URL** and its source, and **Answered by**, the replica that answered. **Database**: server and schema version, and whether this build supports the schema.
- **Replicas by role**: per replica **Name**, **Node**, **Version**, **State** (ready, starting, draining, unready, lost) with failing checks, **Heartbeat** and **Valkey**. Warnings say when a role has fewer than two ready replicas, when all ready replicas share one node, and when no worker is live, in which case jobs and scheduled tasks are queued but nothing runs them. A notice appears while replicas of two versions run side by side.
- **Valkey**: mode, primary name, configured Sentinels, TLS verification, last primary change, and whether replicas disagree about the primary or cannot reach any Sentinel.
- **Stored-secret keys**: the active key ID, whether the key rings of all replicas are compatible, and **Re-encrypt now**.
- **Scheduled tasks**: each task with **Schedule (UTC)**, **Next run** and **Last run**. **Run now** (capability `settings:write`) queues one run; a worker executes it.
- **Recent jobs**, with a link to **Jobs**.

The screen cannot see disruption budgets or free capacity on other nodes, because trove holds no Kubernetes permissions. The **Backups** link leads to the [Backups](#backups) screen.

### Jobs

**Jobs** lists background jobs newest first, filtered by **Kind (comma separated)** and **State** (queued, running, cancelling, succeeded, failed, cancelled). A holder of `settings:read` sees every job, anyone else only the jobs they started. **Cancel** is offered for queued and running jobs to holders of `settings:write` and to whoever started the job; a running job shows cancelling until it reaches its next checkpoint. Through the API, whoever started the job cancels it with a browser session or a token with all permissions of the owner; a token with a restricted scope needs `settings:write` in its scope (`403` otherwise).

The **Result** column of a job that did not succeed shows the error: its code (`job_failed`, `job_timeout`, `attempts_exhausted`, `cancelled` or `superseded`) and, when there is one, the detail as the server wrote it and **Attempt *n* of *m***. The detail is the text of the failed attempt's error, cut to 2,000 bytes, and is not translated; it names what failed, for example the archive entry or the limit that a sync crossed. A job that is queued or running again after a failed attempt keeps the error of the attempt before, so the attempt shown can be lower than the job's own. The result of a succeeded job is a short summary of its figures; a list in it, such as the ecosystems of an advisory sync, shows its length.

### Backups

The chart's backup is off until you set `backup.enabled: true` and a bucket of its own in `config.backup.target.bucket` ([Installation](installation.md)); while it is off, the screen below shows **No backup yet** and the command to start a backup carries placeholders.

**Admin area › System › System status › Backups** (capability `settings:read`) shows the backup job and changes nothing: no control starts, deletes, verifies or restores a backup. It refreshes on the same interval as the status screen, with the same **Pause automatic refresh** and **Refresh now**.

- **Status**: the last success with its age and the **Expected interval**. **Overdue** appears when the last success is older than 1.5 times that interval, the condition of the `TroveBackupStale` alert; **No backup yet** appears while no run has succeeded. **Last failure** appears when a failed or abandoned run, or a run whose process has stopped, is newer than the last success, with the reason in words and its code. A drain or an eviction ends a run as **Interrupted**, which is not a failure: the Job retries it.
- **Run in progress**: the state, the phase, the tables exported, the blobs and bytes copied against the totals, the pod and the start time. **Stopped** means the run's process has gone without finishing; the next run marks it abandoned.
- **Runs**: the runs newest first, filtered by **Operation** and **State**, with **Load more**. **Details** opens a run with its error and its result.
- **Stored backups**: the complete backups that the newest run found in the target, headed "As recorded by run … at …". Backups written or removed outside trove appear after the next run; `trove backup list` reads the target itself. Next to them: the target, how many of the newest backups are kept, the age below which backups are kept, and the expected interval.
- **Start a backup now**: the `kubectl` command that creates a Job from the CronJob, with a copy button. Run it with access to the cluster. The namespace and the CronJob name come from the chart; if the installation does not provide them, the command shows `<namespace>` and `<cronjob>` for you to replace. The run appears in the list once its pod has started. One backup runs at a time.
- After a restore, a notice names the backup and the snapshot time this installation was restored from.
- The output of the revocation journal replay (`trove backup restore`, `trove admin post-restore`) prints one line per entry with
  its outcome. A repository whose entry could widen what it serves is listed as `suspended` and is **disabled** (the line prints
  the entry); a `vuln-rescan` that a stronger vulnerability setting needs is queued in the restored database and runs once the
  workers are up ([After a restore](repositories.md#after-a-restore)).

## The audit log

**Admin area › System › Audit log** (capability `audit:read`: roles `admin`, `security-admin`, `auditor`) shows events newest first, by default of the last 24 hours. Filters: **Time range**, **Actor (name or ID)**, **Action** (an exact type or a prefix such as `token.*`), **Category**, **Repository** and **Outcome** (success, failure, denied). **Show times in UTC** switches the time zone. Selecting a row opens the event with its actor, token, resource, **Source address**, **Endpoint**, **Replica**, **Event ID**, **Trace ID** and details. **Export CSV** and **Export JSON Lines** download the current filter, oldest first, for a range of at most 400 days; every export is itself recorded as `audit.exported`. Reading the log is not recorded.

<details>
<summary>What is recorded</summary>

| Area | Events |
|---|---|
| Sign-in | successful and failed logins, break-glass login, lock and unlock, logout, ended sessions, CSRF failures, rejected credentials |
| Tokens | created, rotated, revoked, expiring soon, rejected (also on an endpoint that does not accept the token) |
| Identities | users, groups, group memberships and their sync from the identity provider, service accounts |
| Permissions and settings | policy and role-binding changes, package ownership, authentication and permission settings, refused requests (`authz.denied`), allowed writes |
| Repositories and endpoints | repository created, updated, deleted; exposure rules created, updated, deleted |
| Content | files deployed and deleted; npm publish, unpublish and dist-tag changes; remote repository events such as an upstream integrity mismatch or a refused upstream address |
| System | a task run started by hand, a cancelled job, an audit export |

Repeated denials and rejected tokens are folded into one event with a count per `audit.denial_dedup_window` and `audit.token_rejection_dedup_window`.

</details>

```yaml
audit:
  retention_days: 365                   # default; 0 keeps every event
  record_downloads: false               # default; true records every file download
  record_anonymous_denials: false       # default
authz:
  audit:
    reads: denied                       # default: none | denied | all (all also records allowed reads)
```

The retention is applied by `trove migrate`, which the chart runs before every upgrade, and by the daily `audit-retention` task. Each event is also written to the log stream `audit`; ship that stream to a store outside trove if you need a copy that trove's own database users cannot change.

## Shutdown and node drains

When a pod receives `SIGTERM`, trove shuts down in a fixed order within the pod's termination grace period (`api.terminationGracePeriodSeconds` and `packages.terminationGracePeriodSeconds` 120, `worker.terminationGracePeriodSeconds` 300):

1. `/readyz` answers `503` at once and the replica is marked draining.
2. An `api` or `packages` replica keeps serving for `health.shutdown_drain_delay` (5 s) so the ingress controller can remove it; answers carry `Connection: close`.
3. The listeners close and requests in flight finish, until the shutdown timeout (`server.shutdown_timeout`, derived as grace period minus drain delay minus 5 s). An upload that is committing gets up to 3 s more; other requests still running then are cut and counted in `trove_http_transfers_cut_total`.
4. A `worker` stops claiming jobs at once. Shortly before the timeout, running jobs are cancelled and returned to the queue without counting an attempt; another worker continues them from their last checkpoint.
5. Buffered audit events are written and the connections closed.

A second signal ends the process immediately. The chart refuses a grace period shorter than the drain delay plus 15 s. Set the drain delay above the time your ingress controller needs to stop sending to a removed pod; if clients see connection errors during drains, raise it.

For a node drain, the chart's PodDisruptionBudgets (`maxUnavailable: 1` per role) and the spreading of replicas across hosts keep at least one replica per role serving, as long as each role has two or more replicas on different nodes. Check **Not drain-safe** on the System status screen before you drain.

## Scaling api, packages and worker

| Role | Scale it when | Setting |
|---|---|---|
| `api` | the UI or the management API is slow, or requests are shed with `503 server_busy` (`trove_http_requests_shed_total`) | `api.replicaCount` (default 2), `api.resources.limits.memory` |
| `packages` | package clients see `503 server_busy`, or latency grows | `packages.replicaCount` (default 3), `packages.resources.limits.memory` |
| `worker` | `trove_jobs_queued` keeps growing | `worker.replicaCount` (default 2), `config.jobs.workers` (concurrent jobs per queue) |

Any `api` replica can answer any request of its routes, any `packages` replica any package request, and any worker can run any job; scheduled tasks run once for the whole installation. Each `api` and `packages` pod admits as many concurrent requests as its memory limit allows (`trove_http_memory_slots`), so the chart requires `resources.limits.memory` for each. A remote repository's `limits.maxConcurrentFetches` is shared by the `packages` replicas (and the `web` replicas of the bridge, and `all`): the other roles hold no share of it. With `highAvailability.enforce: true` (default) the chart requires at least two replicas per role.

Change replica counts through `helm upgrade` rather than `kubectl scale`: the migration Job then compares the connections all replicas may open with PostgreSQL's `max_connections` (`database.preflight.connection_budget: enforce`) and stops the upgrade if they do not fit.

## When a dependency is unavailable

| Dependency | What happens |
|---|---|
| Valkey | No request fails and replicas stay ready (`/readyz` lists `valkey` as degraded). Remote fetches are collapsed per replica only, the negative cache is per replica, rate limits fall back to a per-replica share of each budget, and changes reach other replicas through the PostgreSQL change log instead of an early message. System status shows **Degraded** |
| PostgreSQL | Outages shorter than `health.database_outage_grace` (20 s) show as degraded; longer ones make the replicas unready. PostgreSQL holds everything durable, so trove cannot serve without it |
| Object store | Requests for stored content fail with a retryable `503` on the affected replica; everything else keeps working. Readiness depends on it only with `health.require_object_store: true` |

**When the object store loses objects.** The bucket can lose objects behind trove's back: a restore of the database to a point older than `storage.gc.grace_period`, a lifecycle rule or a deletion by another tool, an object store that lost its data. The rows that describe the content stay. The first read that finds an object missing marks its blob `missing`, logs `the object of a blob is missing from the object store` with its digest, and counts `trove_storage_blobs_damaged_total{status="missing"}`, which the alert `TroveBlobMissing` reads. What the clients see then:

- **A remote repository** serves an item whose object is back without asking its upstream, and otherwise fetches the item again from its upstream on the next request, a cached package document as well as a tarball or a package, and writes the object again; the log line `the stored object of a cached item is not usable` names the repository, the path and the digest. While it cannot (offline, the upstream down, the cache view, a caller who may not fill the cache, also through a virtual repository), the answer is `503 content_unavailable` with `Retry-After`, never `404`. An artifact keeps the digest it was cached with: when the upstream now serves other bytes, nothing is stored, the answer stays `503`, and the audit event `proxy.upstream_digest_changed` is written once an hour; each replica then asks the upstream for that item again only after 5 minutes (`Retry-After` says when). The same holds for a package document that npm keeps because the upstream's copy is older.
- **A local repository** answers `503 content_unavailable` (with `Retry-After: 30`). For 30 seconds per file it answers from the database alone; then the next read asks the object store again, and a file whose object is back (restored in the bucket, or a wrong `storage.s3.prefix` corrected) is served again and its mark is cleared. A lost object therefore costs the object store one request per file and 30 seconds, however many clients ask. Uploading the same file again also writes the object and clears the mark.
- **After a mistake that hid the objects** (a wrong bucket prefix, a store pointed at an empty bucket): correct it, and the files come back on their own within 30 seconds of their next request; nothing has to be uploaded again. `TroveBlobMissing` fires for as long as new blobs are found missing.
- **Something in front of the store answering 404.** trove takes every `404` of the object store as "no such object", except the codes `NoSuchBucket` and `NoSuchUpload`. A gateway, load balancer or proxy in front of the store that answers `404` during a rollout or a misrouting therefore marks every blob that is read meanwhile, `TroveBlobMissing` fires, and remote repositories fetch those items again from their upstreams. When `TroveBlobMissing` fires and the bucket still holds the objects, look at what stands between trove and the store first; once it answers again, the marks heal within 30 seconds of each file's next read.

## Caches

Each replica keeps caches in memory: sessions, tokens, the permission index, the repository list, rendered package documents and the keys of unknown repositories. A change made on one replica reaches the caches of the others through the PostgreSQL change log, polled every `cluster.epoch_poll_interval` (1 s); Valkey messages only make it faster. Every cache also expires its entries after a safety interval. No cache needs to be cleared by hand.

```yaml
cache:
  rendition_bytes: 256MiB               # default: rendered package documents, per replica
  snapshot_safety_ttl: 5m               # default
  unknown_repo_keys: 10000              # default: per replica
```

The caches of remote repositories (fetched packages and their metadata) are a different matter; see [Managing repositories](repositories.md).

## Alerts

With `metrics.prometheusRule.enabled` the chart renders one PrometheusRule with the alerts below and those of [Vulnerability scanning](#vulnerability-scanning), the mirror mode and the upgrade bridge. It needs the Prometheus operator's custom resource definitions and a scrape of the ops port (the chart's ServiceMonitor, see Metrics above). Every selector is scoped to the release's namespace. Every alert can be left out by name in `metrics.prometheusRule.disabledAlerts`; a name that is no alert of the chart fails the install.

| Alert | Fires when | For | Severity |
|---|---|---|---|
| `TroveReplicasNotReady` | a Deployment of the chart (a role, a pool or the bridge) has fewer than two available replicas. Needs kube-state-metrics. A Deployment you scaled to one replica fires too | 5m | critical |
| `TroveDatabaseUnreachable` | replicas retry their connection to PostgreSQL and answer `503` | 2m | critical |
| `TroveObjectStoreUnreachable` | every replica has the circuit of the object store open | 2m | critical |
| `TroveObjectStoreUnreachableSingleReplica` | some replicas have the circuit open and others do not: the cause is on the path of those pods | 5m | warning |
| `TroveDatabasePoolSaturated` | the 95th percentile of the wait for a connection of a replica's request pool is above 100 ms | 10m | warning |
| `TroveControlConnectionWaits` | the 95th percentile of the wait for the control connection is above one second; readiness checks are close to failing | 5m | warning |
| `TroveJobsStuck` | a queued job has been waiting for more than 15 minutes | 5m | warning |
| `TroveLeaseLost` | a singleton task lost its lease more than three times in 15 minutes | at once | warning |
| `TroveMixedVersions` | replicas of more than one version run for two hours: an upgrade is stuck | 2h | warning |
| `TroveSpoolNearlyFull` | running uploads hold more than nine times the free space of a replica's spool volume | 10m | warning |
| `TroveGCStale` | the last garbage-collection run that ended without error is more than two days old. A worker that has not completed a run since it started reports nothing, so a freshly started installation does not fire | 1h | warning |
| `TroveValkeyUnavailable` | every replica runs without Valkey | 2m | critical |
| `TroveValkeyUnavailableSingleReplica` | some replicas lost Valkey, others did not | 5m | warning |
| `TroveValkeySentinelsDegraded` | a replica reaches fewer than two Sentinels, so no failover can be elected | 5m | warning |
| `TroveValkeyFailoverFlapping` | the Valkey primary changed more than twice within an hour | at once | warning |
| `TroveValkeyPrimaryDisagreement` | replicas name different Valkey primaries | 5m | critical |

The rule also holds the alerts of the backup runs (`TroveBackupStale` for a backup that is overdue or never succeeded, `TroveBackupFailed`, `TroveBackupRunningLong`, `TroveBackupGlobalHoldLong`, `TroveBackupSourceDamage`, `TroveBackupVerifyFindings`), but only while `backup.enabled` is true: with the backup off no run succeeds, and `TroveBackupStale` would fire one `config.backup.expected_interval` (a day by default) after the install. `TroveBackupCheckFailed` (a platform protection you declared in `backup.recovery_window` or `backup.object_versioning` is missing) and the revocation journal's `TroveRevocationJournalBacklog` and `TroveRevocationJournalQuarantined` are in the rule either way.

```yaml
metrics:
  prometheusRule:
    enabled: true                  # default: false
    labels: {release: kube-prometheus-stack}   # example: the label your Prometheus selects rules by
    disabledAlerts: []             # default; example: [TroveJobsStuck]
    extraRules: []                 # default: your own rules, appended after the chart's
```

## Vulnerability scanning

The scanning needs the `worker` role: the sync of the advisory data and the evaluation of the stored versions run there. Nothing on this page makes a replica unready, because no replica can repair advisory data; the system information, the report banner and the alerts show it.

**Alerts.** The chart renders a PrometheusRule when you ask for it (see [Alerts](#alerts) for the settings and the other alerts). The alerts of the scanning:

| Alert | Fires when | Severity |
|---|---|---|
| `TroveVulnerabilityDataStale` | no sync run of a source and ecosystem has ended successfully for longer than `vulnerabilities.stale_after` (default 24h, read from `config.vulnerabilities.stale_after`) for 15 minutes. A source that never synced counts: its last success is 0 | warning |
| `TroveVulnerabilitySyncFailing` | three consecutive runs of one source and ecosystem failed; a cancelled run does not count. The last error is on the sources page of the vulnerability section | warning |
| `TroveVulnerabilityRecordsRejected` | the sync rejected an advisory record above a size bound (larger than `max_record_size`) within the last seven days; the worker's log names it. Records that fail validation are counted separately and do not fire it. Stays on for seven days after the last rejection | warning |
| `TroveVulnerabilityRecordsInvalid` | at least ten records, and more than 1 % of those the sync processed in 24 hours, failed validation and were not applied; a single record that fails is only counted and logged | warning |
| `TroveVulnerabilityMaliciousRecordRejected` | a malicious-package record (`MAL-…`) was rejected, by a size bound or by validation, so the package it names is not flagged or blocked by it | critical |
| `TroveVulnerabilityClaimedNameMalicious` | a malicious-package advisory names a package name that a local repository claims | critical |
| `TroveBlobMissing` | a replica found the object of a blob missing from the object store within the last hour (see "When the object store loses objects" above; a gateway in front of the store that answers 404 fires it too) | critical |

A name in `disabledAlerts` that is no alert of the chart fails the install. If you set `stale_after` only through an environment variable (`extraEnv`), set it in `config.vulnerabilities.stale_after` as well, or the alert keeps the 24-hour default.

**Worker spool.** A sync downloads one archive into the worker's spool. While `vulnerabilities.sources.osv.mode` is `online` or `repository`, the chart refuses an install in which the worker's spool budget (`worker.spool.sizeLimit`, else `spool.sizeLimit`, minus `spool.headroom`) is smaller than `vulnerabilities.sources.osv.max_archive_size` (default `1GiB`; the default worker spool is `5Gi` with `2GiB` headroom). The message names both values.

**After a restore.** The findings and their history are part of a backup; the advisory data is not, because the next sync fetches it again. A restored installation therefore starts with every ecosystem empty:

- every finding keeps its status, and every blocked or malicious version stays refused; a dismissed finding stays dismissed, and its history is intact;
- no finding resolves while the data is missing, and new versions are served unevaluated (or refused, where a repository sets `onMissingData: block`);
- `npm audit` through trove answers `503` and the NuGet vulnerability resource is not listed;
- the system information shows each source with state `empty`, `TroveVulnerabilityDataStale` fires after 15 minutes, and the first `vuln-sync-osv` slot (every 15 minutes by default) imports the data and starts the full evaluation. You can start it at once from the task list.

## What to watch

| Signal | Meaning |
|---|---|
| `trove_ready == 0` for a replica, or **Replicas unready** | the replica takes no traffic; `/readyz?verbose=1` names the check |
| `trove_tls_certificate_expiry_timestamp_seconds - time() < 14 * 86400` | a certificate expires within two weeks |
| `increase(trove_tls_reload_errors_total[15m]) > 0` | a rotated certificate did not load; the old one is still served |
| `trove_valkey_available == 0` | degraded mode, see above |
| `time() - trove_cluster_poll_last_success_timestamp_seconds` above a few seconds | the replica cannot read the change log |
| `increase(trove_http_requests_shed_total[5m]) > 0` | a replica is at its concurrency or memory bound |
| `trove_http_requests_total` with `code` 5xx | server-side failures; the request log names them |
| `trove_jobs_queued` rising, or no live worker on System status | background work is not getting done; `TroveJobsStuck` fires on the age of the oldest queued job |
| `increase(trove_audit_events_dropped_total[15m]) > 0` | audit events reached only the log stream |
| `increase(trove_db_pool_empty_acquires_total[5m])` high | the database pool is too small for the load |
| `TroveVulnerabilityDataStale`, `TroveVulnerabilitySyncFailing` | advisory data of a source is older than `vulnerabilities.stale_after`, or three sync runs in a row failed; see [Vulnerability scanning](#vulnerability-scanning) |
| `TroveVulnerabilityClaimedNameMalicious` | a malicious-package advisory names a package name your installation treats as internal |
| `trove_endpoint_misdirected_total` rising | clients or the ingress send an endpoint hosts it does not serve |

TLS details are in [TLS and certificates](tls-and-certificates.md); endpoint metrics in [Endpoints and exposure rules](endpoints.md).
