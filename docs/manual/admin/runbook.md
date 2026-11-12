# Runbook

This page is for the operator who has an alert or an unhappy user in front of them. Each entry starts from what you see, says what it means and what to do, and says when to hand over. It points to the page that explains a mechanism in full rather than repeating it.

## Start here

| Look at | How | What it answers |
|---|---|---|
| The pods | `kubectl -n trove get pods,pdb` | Is a role down, restarting or stuck in `Terminating`; would a drain be blocked |
| One replica's own view | `/readyz?verbose=1` on the ops port (below) | Which check fails, with a fixed reason code and its latency; it works while PostgreSQL is down |
| **System status** | **Admin area › System › System status** ([System status](operations.md#system-status)) | Every replica, its state, failing and degraded checks, Valkey, scheduled tasks, recent jobs. It needs PostgreSQL |
| The request | `X-Request-Id` of the answer, the log record with that `trace_id` | What failed for this request ([Follow one request](operations.md#follow-one-request)) |
| A problem code | `GET /api/v1/problems/<code>`, no sign-in | The description of any code |

```shell
kubectl -n trove port-forward pod/trove-api-6f7c9d-x2k8p 8081:8081 &   # example: any pod of a role
curl -sk "https://localhost:8081/readyz?verbose=1" | jq .                # -k: the ops certificate names the Service, not localhost
```

Every answer of trove on a client listener carries `X-Request-Id`. A `503` without it came from the ingress controller or another proxy in front of trove, most often because the Service has no ready endpoints.

## PostgreSQL unavailable or failing over

**What you see.** `TroveDatabaseUnreachable`. Clients get `503 database_unavailable` (or `database_timeout`, `authz_stale`) with `Retry-After`, after waiting for up to the retry budget. `/readyz?verbose=1` shows `database`, `schema` or `invalidation` failing or degraded. **System status** and sign-in need PostgreSQL and answer `503` themselves while it is gone, so use the ops port.

**What it means.** trove holds everything durable in PostgreSQL and cannot serve without it. A switchover shorter than `health.database_outage_grace` (20 s) only shows as degraded; requests stall and are retried for up to `database.retry.max_elapsed` (30 s), and those that exceed it get `503`. A longer outage makes every replica unready, and the edge answers `503` itself. Liveness stays `200`, so nothing restarts, and readiness returns when PostgreSQL does. A write whose connection dropped after `COMMIT` is reported as `503` with its outcome unknown; repeating it is safe, because a repeat of the same content is a no-op.

**What to do.**

1. Do not restart trove. A restart repairs nothing and discards the replicas' caches.
2. Check the primary, the Service that points to it and the pooler in front of it. On CloudNativePG, `kubectl cnpg status <cluster>`.
3. Read `trove_db_retries_total` by `kind`: `connection` and `shutdown` are the outage itself, `read_only` is a demoted primary the pool reached, `too_many_connections` is the connection budget ([Scaling](operations.md#scaling-api-packages-and-worker)).
4. A pod that stays unready after PostgreSQL is back: read its `/readyz?verbose=1` and log for a wrong credential or CA in `secrets.database`, `secrets.databaseCA`.
5. With asynchronous replication a failover can lose the newest commits; trove flushes the caches of what went backwards on its own, and there is no step for it. If the data is lost for good, see [Backup and restore](backup-and-restore.md).

**When to escalate.** To the team that runs PostgreSQL when the primary is not back within the retry budget. To the maintainers when PostgreSQL answers, the credentials are right, and the replicas stay unready: attach `/readyz?verbose=1` and the log.

## Valkey unavailable or failing over

**What you see.** `TroveValkeyUnavailable`, `TroveValkeyUnavailableSingleReplica`, `TroveValkeySentinelsDegraded`, `TroveValkeyFailoverFlapping` or `TroveValkeyPrimaryDisagreement`. **System status** shows **Degraded** and the **Valkey** card. `/readyz` lists `valkey` as degraded, with a reason such as `connecting`, `no_sentinel`, `timeout`, `auth_failed` or `circuit_open`.

**What it means.** No client request fails: Valkey holds nothing that is the only copy. Until it returns, remote fetches are collapsed per replica only, rate limits fall back to a per-replica share, and changes reach other replicas through the change-log poll instead of an early message ([When a dependency is unavailable](operations.md#when-a-dependency-is-unavailable)). A Sentinel failover is automatic within Sentinel's `down-after-milliseconds` plus the failover; when the election of the remaining Sentinels is split, Sentinel starts the next failover only after twice its `failover-timeout`, and trove runs degraded until then.

**What to do.**

1. Check the Sentinels and the primary, and that at least two Sentinels are reachable: below that no failover can be elected.
2. `TroveValkeyUnavailableSingleReplica`: the cause is on the path of that pod (a node, a network policy, DNS). Compare the pods on `trove_valkey_available`, and delete the pod if the path does not heal.
3. `auth_failed`: a rotated credential or certificate does not match. Running replicas keep working in degraded mode; new pods stay unready until `secrets.valkey` or `secrets.valkeyCA` is right, so a rollout stalls.
4. `TroveValkeyPrimaryDisagreement`: a replica is bound to a demoted primary. `GET /api/v1/system/replicas` shows each replica's primary; restart the one that names the old one.
5. There is nothing to repair in Valkey afterwards.

**When to escalate.** To the team that runs Valkey when the failover above has had its time and Valkey is still gone, or when failovers repeat (`TroveValkeyFailoverFlapping`): upstream load rises while the degraded mode lasts.

## The object store unavailable

**What you see.** `TroveObjectStoreUnreachable` (every replica) or `TroveObjectStoreUnreachableSingleReplica` (some of them). Clients get `503 object_store_unavailable` on new uploads and on downloads of stored content, with `Retry-After`. Metadata, listings, the UI, sign-in, deletes and deduplicated uploads keep working, and replicas stay ready unless `health.require_object_store` is `true`.

**What it means.** Each replica has a circuit breaker: after `storage.s3.circuit_breaker.failure_threshold` (5) failures, blob requests fail fast with `503` and a `Retry-After` of the breaker's open duration, at least 5 seconds. Expired or revoked credentials count as failures. The circuit closes by itself when the store answers. Garbage collection and the orphan sweep pause their object-store phase while it is open, so an outage never turns into lost blobs, and no blob is marked missing because of errors. A pod that starts during the outage stays not ready until its start-up check passes.

**What to do.**

1. Check the endpoint, its certificate and the credential in the Secret. A warning line `the object store rejects the credential` names the credential source and the store's code.
2. For the single-replica alert, the cause is on the path of those pods: compare them on `trove_storage_s3_circuit_open`. Delete the pod if the path does not heal.
3. When the store is back, check that `trove_storage_s3_circuit_open` is `0`; uploads and downloads resume within the open duration plus a few seconds.
4. Objects that are really gone are a different entry: `TroveBlobMissing` and [When the object store loses objects](operations.md#when-a-dependency-is-unavailable).

**When to escalate.** To the team that runs the store. To the maintainers when the store answers and the circuit does not close. `TroveRevocationJournalBacklog` during the outage is expected and clears afterwards; if it does not, see the [Alerts](#alerts) below.

## A node drain, and a replica that does not stop in time

**What you see.** `kubectl drain` waits with a message about a PodDisruptionBudget, or a pod stays in `Terminating`. A replica shows **draining** on **System status**; **Not drain-safe** appears when a role has fewer than two ready replicas or all of them on one node. Clients see connection errors during drains, and long transfers are cut (`trove_http_transfers_cut_total`).

**What it means.** On `SIGTERM` a replica shuts down in a fixed order within its grace period ([Shutdown and node drains](operations.md#shutdown-and-node-drains)): `api.terminationGracePeriodSeconds` and `packages.terminationGracePeriodSeconds` 120, `worker.terminationGracePeriodSeconds` 300. Past the grace period the kubelet sends `SIGKILL`: open requests end, and a job's row is released when its lease of `jobs.lease_duration` (60 s) runs out, so another worker starts it again from its last checkpoint. Audit events still in the replica's buffer are lost ([Known limitations](limitations.md#audit-trail)). The PodDisruptionBudgets allow one disruption per role and evict unready pods without waiting. Backup and restore pods hold up no drain.

**What to do.**

1. Before you drain, check **Not drain-safe**. Scale the role, or wait until a second replica is ready on another node.
2. A drain that waits: `kubectl -n trove get pdb` shows which role has no disruption left. Find out why its other replica is not ready rather than deleting the budget.
3. A pod that exceeds its grace period: nothing to do but wait; the kubelet kills it. A second signal also ends a replica at once.
4. A pod that stays `Terminating` past its grace period means the node or the kubelet does not act. Force-delete it only when the node is really gone: `kubectl -n trove delete pod <pod> --grace-period=0 --force`.
5. Clients that see connection errors on every drain: raise `health.shutdown_drain_delay` above your ingress controller's endpoint removal time. The chart refuses a grace period shorter than the drain delay plus 15 s, so raise the grace period with it; `server.shutdown_timeout` follows.

**When to escalate.** To the platform team when a drain or a kill leaves a pod `Terminating` on a healthy node. To the maintainers when every drain exceeds the grace period with no long transfer running: attach the replica's log.

## Replicas shown as lost

**What you see.** On **System status** a replica is **lost** (in `GET /api/v1/system/replicas`, `"lost": true`), and its leases are no longer listed. The screen has no separate "stale" state: **lost** is what a stale heartbeat is called.

**What it means.** The replica's row was last refreshed longer ago than `system.heartbeat_stale_after` (30 s), judged by PostgreSQL's clock. A clean shutdown deletes the row, so a lost replica either died without one (a crash, an `OOMKilled`, a `SIGKILL` after its grace period, a lost node) or is alive and cannot write its heartbeat, because it is cut off from PostgreSQL or its node is paused. A lost replica holds nothing: its locks ended with its session and its job leases expire. The task `replica-registry-prune`, which runs every ten minutes, removes a row 24 hours after its last heartbeat.

**What to do.**

1. Match the name with `kubectl -n trove get pod <name>`. Is the pod gone: nothing to do, the row goes away by itself. A leftover row does not affect any request.
2. Is the pod still there: read `/readyz?verbose=1` and the log. A `database` check that fails means the pod is cut off from PostgreSQL ([PostgreSQL unavailable](#postgresql-unavailable-or-failing-over)).
3. Is the pod being killed for memory (`kubectl describe pod`: `OOMKilled`): raise `resources.limits.memory` of the role ([Known limitations](limitations.md#upgrades-and-high-availability) names the case known so far).
4. Several replicas lost at once, or **no live worker** on the screen: treat it as a PostgreSQL or a node problem, not as a trove replica problem. Jobs queue but nothing runs them until a worker returns.

**When to escalate.** To the platform team when several nodes are involved. To the maintainers when a replica is lost repeatedly with neither memory nor node trouble to explain it: attach its log and `/readyz?verbose=1`.

## Requests answered 503

**What you see.** A `503` with a `Retry-After` and, in a problem or npm body, a `code`. **What it means** and **what to do** depend on the code. Most are temporary and repeat on another replica; escalate per the entry the row points to. A NuGet client prints only the status, so ask for the body with `curl`. An OCI client gets the registry error `UNAVAILABLE`, or `TOOMANYREQUESTS` for an upstream rate limit, with trove's sentence as the message. An APT client prints the status line only.

| Code | Meaning | What to do |
|---|---|---|
| `database_unavailable` | PostgreSQL could not be reached within the retry budget | [PostgreSQL](#postgresql-unavailable-or-failing-over) |
| `database_timeout` | A statement exceeded its `statement_timeout` or `lock_timeout` | Transient: read `trove_db_slow_queries_total` and the database's locks if it persists |
| `authz_stale` | The replica could not confirm that its permission index is current within `authz.cache.max_staleness` (10 s) | [PostgreSQL](#postgresql-unavailable-or-failing-over), or `TroveControlConnectionWaits`; another replica may answer |
| `object_store_unavailable` | The object store cannot be reached | [The object store](#the-object-store-unavailable) |
| `content_unavailable` | The stored object of this file is missing; a remote repository fetches it again and answers this only while it cannot | [When the object store loses objects](operations.md#when-a-dependency-is-unavailable) |
| `server_busy` | The replica is at `server.max_in_flight`, or at its memory slots or heap limit (`trove_http_requests_shed_total` by `reason`), or too many local logins are being verified | [Scaling](operations.md#scaling-api-packages-and-worker); it can succeed on another replica |
| `shutting_down` | The replica is draining | Retry; expected during drains and rollouts |
| `unavailable` | The catch-all: the sentence in the body says which | See below |
| `upstream_assumed_offline` | The circuit of a remote repository's upstream is open and the path is not cached | Retry after `Retry-After`; check the upstream and its repository |
| `upstream_rate_limited` | The upstream answered `429`; `Retry-After` is the upstream's | Wait; a stored copy, if any, is served instead |
| `fetch_queue_timeout` | Every upstream fetch slot of the repository stayed busy for `proxy.queue_timeout_seconds` (`Retry-After: 5`) | Raise `limits.maxConcurrentFetches` of the repository if the upstream takes it, or retry |
| `shadow_check_unavailable` | Whether a remote repository serves a name could not be determined; nothing was stored | Fix the upstream, or publish with the manage permission; see [Errors and troubleshooting](../user/troubleshooting.md) |
| `vulnerability_data_unavailable` | The advisory data of this ecosystem is not imported yet, so an audit has nothing to answer from | Wait for the first import; [A stuck advisory sync](#an-advisory-sync-that-is-stuck) |
| `provider_unavailable` | The identity provider's discovery document has not loaded | Browser sign-in only; tokens keep working. Check the provider |
| `too_many_pending_logins` | More login flows are pending than `auth.rate_limit.max_pending_flows` allows | Retry; look for a client that starts flows without finishing them |
| `break_glass_rotating` | The break-glass password was rotated a moment ago | Retry after `Retry-After` |
| `main_unreachable`, `bastion_busy` | A Bastion cannot reach the main installation, or its budget of requests to it is used up | [The trove Bastion](bastion.md) |
| `temporarily_unavailable` | The CI token exchange could not finish | The body's `reason` names the cause: `signing_keys_unavailable`, `database_unavailable` or `configuration_changed` ([Troubleshoot refused exchanges](gitlab-ci.md#troubleshoot-refused-exchanges)) |

`unavailable` by its sentence:

| The sentence says | Meaning | What to do |
|---|---|---|
| Your maintenance message, or the default refusal text | Read-only maintenance mode refuses every write | Switch it off under **General settings** when the work is done ([Banner and maintenance mode](operations.md#banner-and-maintenance-mode)) |
| `this replica has no spool budget left for the upload`, `the spool volume of this replica is nearly full` | The spool cannot take the upload | [Disk or spool full](#disk-or-spool-full) |
| `The path is being changed by concurrent requests`, `The upload is being completed by another request` | A collision between writes to the same path | Retry; nothing is lost |
| `the content is being removed by garbage collection` | The upload's content matched a blob that is being deleted | Retry |
| `… is not available on this replica` | This replica has no such service; a pod of another role would have it | Check that the path reaches the right role (the ingress or a custom route) |

## Disk or spool full

**What you see.** `TroveSpoolNearlyFull` (more than 90 % used for 10 minutes). Uploads answer `503 unavailable` with a sentence about the spool, and clients retry. `trove_spool_used_bytes` and `trove_spool_free_bytes` per replica. A pod that is `Evicted`: `kubectl describe pod` names the emptyDir that passed its limit. A mirror pass waits 5 seconds and repeats the item; an advisory sync that needs the space fails; an image scan waits and is then put off (below).

**What it means.** Every pod has one spool volume (`spool.sizeLimit` 20 GiB by default; `worker.spool.sizeLimit` 8 GiB). The usable budget is the size minus `spool.headroom` (2 GiB), and is held per replica. The worker's budget is shared by the cache fills of jobs, the artifacts of a mirror pass, one advisory archive while a sync runs, and the image scanning.

The working space of an image scan (`vulnerabilities.images.scan_dir`, below the spool) is counted in the same budget: the unpacked advisory database once (about 1.6 GiB on 2026-11-10) and, while a scan runs, the size of its image plus 64 MiB. A scan that does not fit waits two minutes and is then put off with the code `spool_busy` (the image shows `pending_scan`, the scan is tried again after a minute); a scan that cannot fit next to the database even in an empty spool is refused with `spool_too_small` (tried again after an hour), and its reason names the sizes. The chart refuses a budget below `max_image_size` + 2 GiB + 512 MiB for the pool that claims the queue `vuln`. A database that no longer fits ends `vuln-image-scan` with a `skipped` result that says so.

**What to do.**

1. Find the replica on `trove_spool_used_bytes`. Look for uploads that do not end (the request log, `trove_http_requests_in_flight`).
2. Raise the size of the role: `api.spool.sizeLimit`, `packages.spool.sizeLimit`, `worker.spool.sizeLimit`, or `spool.sizeLimit` for all of them, by `helm upgrade`. The chart refuses a size whose budget is below `storage.upload.max_size` and, for a pod that claims the queue `vuln`, below the advisory archive limit.
3. If the worker is short because of image scans, raise the size of the pool that claims the queue `vuln`, or give the scans a pool of their own with a larger volume, or lower `vulnerabilities.images.max_image_size` (a larger image then shows `scan_failed`); the chart refuses a size that cannot hold the scanning:

   ```yaml
   worker:
     pools:
       - name: vuln                   # example
         queues: [vuln]               # example
         replicaCount: 2              # example
         spool:
           sizeLimit: 20Gi            # example: at least the largest image plus the database and the budget it needs
   ```

4. A node under disk pressure evicts pods whatever the limit says: that is the platform's.

**When to escalate.** To the platform team for a node that runs out of disk. To the maintainers when a replica's spool fills with no upload in flight: attach the replica's log and the two gauges.

## A certificate that does not rotate

**What you see.** A new certificate is in the Secret but clients still get the old one; `trove_tls_reload_errors_total` rises; an expiry alert fires; or a client refuses an expired certificate.

**What it means.** trove re-reads every certificate and CA file and keeps the old material in service when a new file does not load, so a broken renewal shows as an old certificate, not as an outage. The kubelet updates a mounted Secret only after its sync period, and a mount with `subPath` is never updated. A certificate keeps being served after it expires; only the expiry metric warns.

**What to do.**

1. Follow [Rotate a listener certificate](tls-and-certificates.md#rotate-a-listener-certificate), and check `trove_tls_reloads_total` and `trove_tls_certificate_expiry_timestamp_seconds`.
2. `trove_tls_reload_errors_total` rising: the error log record names the file. Compare the key and the certificate, and check that a Secret was not written half way. The old material stays in service meanwhile.
3. A pod whose `/readyz?verbose=1` shows `tls` failing, or that serves a temporary certificate (`trove_tls_temporary_certificate`): the listener's Secret is missing or wrong at start ([Common failures](tls-and-certificates.md#common-failures)).
4. `tracing.ca_file` and the stored-secret key files are read only at start: those need a restart.
5. A connection already open keeps the certificate it negotiated until it closes.

**When to escalate.** To whoever runs the certificate issuer when the Secret does not change at all. To the maintainers when the new file loads (`trove_tls_reloads_total` rises) and clients still get the old one after a reconnect.

## A mirror that is stuck

**What you see.** `TroveMirrorFailing` or `TroveMirrorStale`. The section **Mirror** of the repository's settings ([The status of a mirror](repositories.md#the-status-of-a-mirror)) shows **Failing**, **Waiting** or a **Next pass** long in the past, or the Jobs screen shows `mirror-pass` jobs of one repository queued and not running.

**What it means.** Passes run in the queue `mirror`, on a worker that claims it (two at a time per replica by default, `config.jobs.workers.mirror`). A pass keeps its place; after a failure the dispatch, which runs every minute, continues it after a back-off. **Waiting** is a rate-limit wait, a failure or a release; **Bound reached** is a storage bound. A pass that the spool refuses waits 5 seconds and repeats the item.

**What to do.**

1. Read **Last error** (the code, and hosts but never credentials), **Rate-limit waits** and **Bound** on the status.
2. No progress and no error: check that a worker claims the queue `mirror` (`worker.pools` take queues away from the main worker) and that the worker pods are ready; `TroveJobsStuck` fires on an unclaimed queue.
3. `TroveMirrorStale` with a rate limit: the upstream holds it back, and the passes continue by themselves when its limit allows. A pass that hit a bound: raise `remote.mirror.maxBytes` or the repository's quota.
4. **Run a pass now** queues one, and **Run a full pass** discovers every name again instead of reading the feed from its cursor. **Pause** and **Resume** stop and start passes. A `mirror-pass` job that hangs can be cancelled from the **Jobs** screen.

**When to escalate.** To the owner of the upstream for rate limits and authentication errors. To the maintainers for an error code that does not change with the upstream: attach **Last error** and the job's result.

## An advisory sync that is stuck

**What you see.** `TroveVulnerabilityDataStale` or `TroveVulnerabilitySyncFailing`. The **Advisory sources** screen shows the last error and how old the data is; the report shows a banner.

**What it means.** The sync runs on a worker claiming the queue `vuln` (one job at a time per replica by default, `config.jobs.workers.vuln`). A failed run keeps the previous data and is tried at the next slot, every 15 minutes for `vuln-sync-osv`; the job is not retried within its slot. Enforcement and evaluation continue on the stored data, and no replica becomes unready for this. A run that a drain interrupts continues on another worker, and a worker that is killed records nothing: the staleness alert is then the only signal.

**What to do.**

1. Read the last error on the screen. The codes are in [Refused archives](vulnerability-sources.md#refused-archives) and [Check that it works](vulnerability-sources.md#check-that-it-works).
2. Check that a worker claims the queue `vuln`, and that the worker pods can reach the sources on port 443 ([Advisory sources](vulnerability-sources.md#settings)).
3. A failure about the archive's size or the spool: [Disk or spool full](#disk-or-spool-full).
4. Start a run by hand with `POST /api/v1/system/tasks/vuln-sync-osv/run`; a `409` names the run already going. Cancel a running `vuln-sync-osv` job from the **Jobs** screen when it has to give way: a cancel does not count as a failure.
5. `shrink_refused` and `too_many_invalid_records` say the source looks truncated or tampered with: check the source before you use `{"acceptShrink": true}`.

**When to escalate.** To the owner of the source for an outage of its own. To your security contact for repeated `archive_digest_mismatch` or a `shrink_refused` that the source does not explain: the data may be tampered with.

## Alerts

The chart's PrometheusRule holds the alerts below when `metrics.prometheusRule.enabled` is set ([Alerts](operations.md#alerts) for the settings, and for leaving one out by name). `TroveReplicasNotReady` needs kube-state-metrics. The alerts of the backup runs exist only while `backup.enabled` is true.

| Alert | Severity | What it means | What to look at first |
|---|---|---|---|
| `TroveReplicasNotReady` | critical | A role or pool has fewer than two available replicas | `kubectl get pods`, then the pod's `/readyz?verbose=1` and its events |
| `TroveDatabaseUnreachable` | critical | Replicas retry PostgreSQL and answer `503` | [PostgreSQL](#postgresql-unavailable-or-failing-over) |
| `TroveDatabasePoolSaturated` | warning | A replica waits over 100 ms for a request connection | Database CPU; raise `database.pool.max_conns` within the connection budget |
| `TroveControlConnectionWaits` | warning | A replica waits over a second for its control connection | The pooler endpoint of that connection and the database latency |
| `TroveObjectStoreUnreachable` | critical | Every replica has the object-store circuit open | [The object store](#the-object-store-unavailable) |
| `TroveObjectStoreUnreachableSingleReplica` | warning | Some replicas have the circuit open | The path of those pods, on `trove_storage_s3_circuit_open` |
| `TroveBlobMissing` | critical | A read found a blob's object missing within the last hour | The log line with the digest; the bucket, its lifecycle rules and anything in front of it ([Loses objects](operations.md#when-a-dependency-is-unavailable)) |
| `TroveJobsStuck` | warning | A queued job has waited over 15 minutes | Worker pods; the queue named by the label `kind`; the Jobs screen |
| `TroveLeaseLost` | warning | A singleton task lost its lease more than three times in 15 minutes | A transaction-mode pooler on the session endpoint, idle timeouts, resets between the worker and the database |
| `TroveMixedVersions` | warning | Two trove versions have run side by side for two hours | The image of each Deployment; a stuck rollout |
| `TroveSpoolNearlyFull` | warning | A replica's spool is over 90 % used | [Disk or spool full](#disk-or-spool-full) |
| `TroveGCStale` | warning | No garbage-collection run has succeeded for two days | The `gc` jobs and the object store |
| `TroveBridgeLeftOn` | warning | A `web` replica of the upgrade bridge has run for 24 hours | Finish the cut ([Upgrades](upgrades.md)) |
| `TroveValkeyUnavailable` | critical | Every replica runs without Valkey | [Valkey](#valkey-unavailable-or-failing-over) |
| `TroveValkeyUnavailableSingleReplica` | warning | Some replicas lost Valkey | The path of those pods, on `trove_valkey_available` |
| `TroveValkeySentinelsDegraded` | warning | A replica reaches fewer than two Sentinels | The Sentinel pods and the path to them |
| `TroveValkeyFailoverFlapping` | warning | The primary changed more than twice within an hour | Valkey pods (restarts, memory, node); `down-after-milliseconds` against the network latency |
| `TroveValkeyPrimaryDisagreement` | critical | Replicas name different primaries | `GET /api/v1/system/replicas`; restart the one on the old primary |
| `TroveBackupCheckFailed` | critical | A platform protection you declared does not hold | `trove admin backup check`; the label `check` names it ([Prepare the platform](backup-and-restore.md#prepare-the-platform)) |
| `TroveRevocationJournalBacklog` | warning | Journal entries have waited 15 minutes to ship | Worker pods, the object-store alerts, write access of the credential to `_journal/`; a restore now would miss those entries |
| `TroveRevocationJournalQuarantined` | critical | A change could not be written to the journal and was set aside | The shipper's log; repair the change and take a new backup |
| `TroveBackupStale` | critical | No backup succeeded in 1.5 times the expected interval, or none ever | The Backups screen, the CronJob (suspended, missed its deadline) and the last Job's log |
| `TroveBackupFailed` | critical | The newest backup run failed and none has succeeded since | The run's error on the Backups screen and the Job's log; the [exit codes](backup-and-restore.md#reference) |
| `TroveBackupRunningLong` | warning | A backup or delete run has lasted over half the expected interval | The progress on the Backups screen; a stuck run ends itself after `config.backup.stall_timeout` |
| `TroveBackupGlobalHoldLong` | warning | A backup has held all garbage collection for 30 minutes | The active run; it should last minutes |
| `TroveBackupSourceDamage` | critical | The newest backup found blobs damaged or missing | The run's result; restore the objects from the bucket's versions or upload them, then back up again |
| `TroveBackupVerifyFindings` | critical | The newest `verify` found missing or damaged objects | Its output; take a new backup and look at the target's lifecycle and deletions |
| `TroveVulnerabilityDataStale` | warning | No sync run of a source and ecosystem succeeded within `vulnerabilities.stale_after` | [Advisory sync](#an-advisory-sync-that-is-stuck) |
| `TroveVulnerabilitySyncFailing` | warning | Three sync runs in a row failed | The sources screen, the job's error |
| `TroveVulnerabilityRecordsRejected` | warning | A record above a size bound was rejected in the last seven days | The worker log; raise `max_record_size` if it is legitimate |
| `TroveVulnerabilityRecordsInvalid` | warning | Over 1 % of records failed validation in a day | The worker log, message `an advisory record was rejected` |
| `TroveVulnerabilityMaliciousRecordRejected` | critical | A malicious-package record was rejected, so that package is not flagged | The worker log; the entry, by size or by validation |
| `TroveVulnerabilityClaimedNameMalicious` | critical | A malicious advisory names a package name you treat as internal | `GET /api/v1/vulnerabilities/claimed-name-matches`; treat it as a dependency-confusion attempt |
| `TroveVulnerabilityImageScansFailing` | warning | Images are being scanned and none has finished | The result codes of `trove_vuln_image_scans_total`; the image's `scan_failed` |
| `TroveMirrorFailing` | warning | A mirror failed three pass attempts in a row | [Mirror](#a-mirror-that-is-stuck) |
| `TroveMirrorStale` | warning | A mirror is over an hour behind its schedule | [Mirror](#a-mirror-that-is-stuck); the queue `mirror` is claimed |

## What this page does not cover

A failed upgrade is in [Upgrades](upgrades.md), and a Bastion's own trouble in [The trove Bastion](bastion.md). trove holds no Kubernetes permissions, so every step above that changes the platform is yours to run; nothing here restarts a dependency for you.
