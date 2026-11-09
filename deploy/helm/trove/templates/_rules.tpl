{{/*
The alerts of the PrometheusRule (R-HA-045) as a YAML list, before disabledAlerts is applied. Names are
stable. Rendered: the alerts of the vulnerability scanning (R-VULN-024), the two of the mirror mode (R-OPS-046,
Q-OPS-160, Q-OPS-171), the one of a bridge left on (R-HA-064, Q-HA-92), TroveBlobMissing (R-HA-024, Q-STOR-152) and
the sixteen rows of R-HA-045's table whose metrics the binary exports, TroveBackupCheckFailed (the platform check
task backup-check, R-HA-023), TroveRevocationJournalBacklog (R-HA-054) and the six TroveBackup* alerts of the backup runs
(R-BAK-014): every alert of the table.

The six alerts of the backup runs are rendered only while backup.enabled is true (Q-HA-17): without the CronJob no run
ever succeeds, and the clause of TroveBackupStale for a backup that never succeeded would fire one interval after every
installation that leaves the backup off. TroveBackupCheckFailed and the journal's alerts do not depend on it: the platform
check and the journal serve the platform restore too.

The backup alerts handle the gauges that have no series until the event happened (Q-OPS-180): TroveBackupFailed reads
the last success as "or vector(0)", and TroveBackupStale has a clause for a success that never was: no series for
longer than config.backup.expected_interval, which the chart reads for the window, because the alert has no series to
read the interval from before the first success.

Every selector is scoped to the namespace of the release, because one Prometheus can scrape several
installations and a max() over all of them would blend their states. A scrape through the Prometheus
operator (the chart's ServiceMonitor, or a PodMonitor) always carries the namespace label.

The stale threshold is config.vulnerabilities.stale_after, the value the binary reads (default 24h), so that
the alert and the report banner agree (R-VULN-004). It is a duration parsed by trove.seconds.
*/}}
{{- define "trove.rules.all" -}}
{{- $ns := printf "namespace=%q" .Release.Namespace -}}
{{- $roles := printf "%s-(api|packages|worker|web)(-.*)?" (include "trove.fullname" .) -}}
{{- $stale := include "trove.seconds" (dig "vulnerabilities" "stale_after" "24h" .Values.config) -}}
{{- $interval := include "trove.seconds" (dig "backup" "expected_interval" "24h" .Values.config) -}}
- alert: TroveReplicasNotReady
  expr: min by (deployment) (kube_deployment_status_replicas_available{ {{ $ns }},deployment=~{{ $roles | quote }} }) < 2
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: The Deployment {{ "{{ $labels.deployment }}" }} has fewer than two available replicas.
    description: A role or pool with fewer than two available replicas has no redundancy, and a node drain or a rollout takes it away (R-HA-001, R-HA-059). Look at the pods of the Deployment, their readiness (GET /readyz?verbose=1) and the events. Needs kube-state-metrics. A Deployment scaled to one replica by choice fires too.
- alert: TroveDatabaseUnreachable
  expr: sum(rate(trove_db_retries_total{ {{ $ns }},kind="connection" }[5m])) > 0 and sum(rate(trove_http_requests_total{ {{ $ns }},code="503" }[5m])) > 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: The replicas keep retrying their connection to PostgreSQL and answer 503.
    description: The primary is unreachable beyond the retry budget (database.retry, R-HA-009, R-HA-015), or the pooler in front of it is. Check the PostgreSQL primary, the Service that points to it and the pooler, and the database check of GET /readyz?verbose=1.
- alert: TroveObjectStoreUnreachable
  expr: min(trove_storage_s3_circuit_open{ {{ $ns }} }) == 1
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: Every replica has the circuit of the object store open.
    description: Blob reads and writes fail on all replicas (R-STOR-033, R-HA-010). Metadata and cached content that does not need the store still answer. Check the S3 endpoint, its certificate and the credentials in the Secret.
- alert: TroveObjectStoreUnreachableSingleReplica
  expr: max(trove_storage_s3_circuit_open{ {{ $ns }} }) == 1 and min(trove_storage_s3_circuit_open{ {{ $ns }} }) == 0
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: The circuit of the object store is open on some replicas and closed on others.
    description: The store is reachable from part of the replicas, so the cause is on the path of the affected pods (a node, a network policy, DNS, an egress rule), not the store (R-STOR-033, R-HA-010). Compare the pods by the label pod on trove_storage_s3_circuit_open.
- alert: TroveDatabasePoolSaturated
  expr: histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{ {{ $ns }},db_pool="request" }[5m])) > 0.1
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: A replica waits more than 100 ms for a connection of its request pool (95th percentile).
    description: The request pool, or every server connection of the request pooler, is busy (R-HA-016). If the database CPU is idle while the wait grows, raise database.pool.max_conns within the connection budget; otherwise the database is the limit. trove_db_pool_canceled_acquires_total counts the waits that ended in 503 database_timeout.
- alert: TroveControlConnectionWaits
  expr: histogram_quantile(0.95, rate(trove_db_pool_acquire_duration_seconds_bucket{ {{ $ns }},db_pool="control" }[5m])) > 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: A replica waits more than a second for its control connection (95th percentile).
    description: The loops that readiness and job ownership depend on (change-log poll, database check, registry heartbeat, job heartbeats) wait for the one control connection (Q-HA-20). If this goes on, the invalidation and database checks fail on every replica (R-OPS-046). Check the pooler endpoint of the control connection and the database latency.
- alert: TroveJobsStuck
  expr: max(trove_jobs_oldest_queued_age_seconds{ {{ $ns }} }) > 900
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: A queued job has been waiting for more than 15 minutes.
    description: The queue is not being claimed or the workers are behind (R-ARCH-016). Check that worker pods are ready and claim the queue named by the label kind of the series, and the jobs list on System status. The series is read from PostgreSQL by every replica, so it keeps reporting while no worker runs.
- alert: TroveLeaseLost
  expr: increase(trove_lease_lost_total{ {{ $ns }} }[15m]) > 3
  labels:
    severity: warning
  annotations:
    summary: The singleton task {{ "{{ $labels.task }}" }} lost its lease more than three times in 15 minutes.
    description: The worker that holds the lease loses its session connection to PostgreSQL repeatedly (R-HA-006, R-ARCH-017), so the task moves between replicas or stops. Look for a pooler in transaction mode on the session endpoint, idle-session timeouts and connection resets between the worker and the database.
- alert: TroveMixedVersions
  expr: count(count by (version) (trove_build_info{ {{ $ns }} })) > 1
  for: 2h
  labels:
    severity: warning
  annotations:
    summary: Replicas of more than one trove version have been running for two hours.
    description: A rolling upgrade is stuck, or a Deployment was left on an old image. Compare the label version of trove_build_info by pod and the image of each Deployment.
- alert: TroveSpoolNearlyFull
  expr: max(trove_spool_used_bytes{ {{ $ns }} } / (trove_spool_used_bytes{ {{ $ns }} } + trove_spool_free_bytes{ {{ $ns }} })) > 0.9
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: The upload spool of a replica is more than 90 percent used.
    description: Running uploads hold more than nine times what the spool volume has left (R-STOR-004). New uploads are refused with 503 once the budget or the minimum free space is exceeded. Raise spool.sizeLimit of the role, or lower config.storage.spool.max_bytes; look for uploads that do not end.
- alert: TroveVulnerabilityDataStale
  expr: time() - max by (source, ecosystem) (trove_vuln_source_last_success_timestamp_seconds{ {{ $ns }} }) > {{ $stale }}
  for: 15m
  labels:
    severity: warning
  annotations:
    summary: The advisory data of {{ "{{ $labels.source }}" }} {{ "{{ $labels.ecosystem }}" }} is older than vulnerabilities.stale_after.
    description: No sync run of this source and ecosystem ended successfully within vulnerabilities.stale_after. Enforcement and evaluation continue on the stored data (R-VULN-024). A source that never synced has the value 0 and fires too. Check the worker pods, the task vuln-sync-osv or vuln-sync-kev, and the egress to the source.
- alert: TroveVulnerabilitySyncFailing
  expr: max by (source, ecosystem) (trove_vuln_source_consecutive_failures{ {{ $ns }} }) >= 3
  labels:
    severity: warning
  annotations:
    summary: Three consecutive sync runs of {{ "{{ $labels.source }}" }} {{ "{{ $labels.ecosystem }}" }} failed.
    description: The last error is in GET /api/v1/vulnerabilities/sources and in the audit event vulnerability.source.failed (R-VULN-024). The data is not refreshed until a run succeeds.
- alert: TroveVulnerabilityRecordsRejected
  expr: sum by (source, ecosystem) (increase(trove_vuln_sync_records_total{ {{ $ns }},result="rejected" }[7d])) > 0
  labels:
    severity: warning
  annotations:
    summary: Advisory records of {{ "{{ $labels.source }}" }} {{ "{{ $labels.ecosystem }}" }} were rejected by a size bound within the last seven days.
    description: A record above a bound of one entry is not applied, and a record that was stored before keeps its older copy; the package it names may be missing from the data (R-VULN-004, Q-VULN-116). Records that fail validation are the result "invalid" and do not fire this alert. The worker logs each one at WARN with its entry, size and limit. If a legitimate record is too large, raise vulnerabilities.sources.osv.max_record_size.
- alert: TroveVulnerabilityRecordsInvalid
  expr: |-
    sum by (source, ecosystem) (increase(trove_vuln_sync_records_total{ {{ $ns }},result="invalid" }[24h])) >= 10
    and
    sum by (source, ecosystem) (increase(trove_vuln_sync_records_total{ {{ $ns }},result="invalid" }[24h]))
    / sum by (source, ecosystem) (increase(trove_vuln_sync_records_total{ {{ $ns }},result=~"written|skipped|rejected|invalid" }[24h])) > 0.01
  labels:
    severity: warning
  annotations:
    summary: More than 1 percent of the advisory records of {{ "{{ $labels.source }}" }} {{ "{{ $labels.ecosystem }}" }} failed validation within a day.
    description: At least ten records, and more than one percent of those the sync processed in 24 hours, failed the rules of R-VULN-006 and were not applied; a record that was stored before keeps its older copy. A single record that fails is only counted and logged. Look at the worker log (message "an advisory record was rejected", attribute reason) for the first (R-VULN-004, Q-VULN-122).
- alert: TroveVulnerabilityMaliciousRecordRejected
  expr: sum by (source, ecosystem) (increase(trove_vuln_sync_records_total{ {{ $ns }},result="rejected_malicious" }[7d])) > 0
  labels:
    severity: critical
  annotations:
    summary: A malicious-package advisory record of {{ "{{ $labels.source }}" }} {{ "{{ $labels.ecosystem }}" }} was rejected, by a bound or by validation, within the last seven days.
    description: The record named MAL-... is not applied, so the package it names is not flagged or blocked by it (R-VULN-004, Q-VULN-122). Find the entry in the worker log (message "an advisory record was rejected"); the cause is the bound of one entry (its size) or a rule of R-VULN-006 (it failed validation).
- alert: TroveVulnerabilityClaimedNameMalicious
  expr: max(trove_vuln_claimed_name_matches{ {{ $ns }},kind="malicious" }) > 0
  labels:
    severity: critical
  annotations:
    summary: A malicious-package advisory names a package name that this installation treats as internal.
    description: Look at GET /api/v1/vulnerabilities/claimed-name-matches. A public package of that name exists, which is how a dependency-confusion attempt looks (R-VULN-010, R-VULN-024).
- alert: TroveMirrorFailing
  expr: max by (repo) (trove_mirror_consecutive_failures{ {{ $ns }} }) >= 3
  labels:
    severity: warning
  annotations:
    summary: The mirror {{ "{{ $labels.repo }}" }} has failed three pass attempts in a row.
    description: The last error is in GET /api/v1/repositories/{key}/mirror and in the audit event mirror.pass_failed (R-REPO-053, R-REPO-057). The pass in progress stays, and the dispatch continues it after the back-off; a complete pass resets the count. A paused mirror is not reported.
- alert: TroveMirrorStale
  expr: time() - max by (repo) (trove_mirror_next_pass_timestamp_seconds{ {{ $ns }} }) > 3600
  for: 30m
  labels:
    severity: warning
  annotations:
    summary: The mirror {{ "{{ $labels.repo }}" }} is more than an hour behind its schedule.
    description: Its next pass was due an hour ago and nothing started it, or the pass in progress has not written for an hour. No process claims the queue mirror, the passes are held back by a rate limit that lasts hours, or the job is lost (R-REPO-053, R-REPO-054). Check the pods that claim the queue, the status of the repository and trove_mirror_pending_packages. A paused mirror is not reported.
- alert: TroveBridgeLeftOn
  expr: count(trove_build_info{ {{ $ns }},role="web" }) > 0
  for: 24h
  labels:
    severity: warning
  annotations:
    summary: A web replica of the upgrade bridge has been running for 24 hours.
    description: web.enabled is still true, so the web pods stay registered and hold the login secrets (R-HA-064, H-518). Finish the cut, once api, packages and their pools are ready; step 2 with web.routed false moves the edge onto them, step 3 with web.enabled false removes the bridge.
- alert: TroveBlobMissing
  expr: sum(increase(trove_storage_blobs_damaged_total{ {{ $ns }},status="missing" }[1h])) > 0
  labels:
    severity: critical
  annotations:
    summary: The object store has lost the object of at least one blob within the last hour.
    description: A read found no object behind a blob row and marked the blob missing (R-HA-024, R-STOR-027). The replicas log every such blob with its digest ("the object of a blob is missing from the object store") and every cached item of a remote repository with its repository and path. A remote repository fills its items again from the upstream; a file of a local repository answers 503 content_unavailable until its object is restored or the same content is uploaded again. Check the bucket, its lifecycle rules and who else writes to it.
- alert: TroveGCStale
  expr: time() - max(trove_gc_last_success_timestamp_seconds{ {{ $ns }} }) > 172800
  for: 1h
  labels:
    severity: warning
  annotations:
    summary: No garbage-collection run has ended successfully for more than two days.
    description: The tasks gc and gc-full do not finish (R-STOR-025). Blobs that nothing references stay in the object store until a run succeeds. The series exists after the first successful run of a worker process, so a worker that never completed a run reports nothing. Check the jobs of the task and the object store.
- alert: TroveBackupCheckFailed
  expr: min(trove_backup_check_ok{ {{ $ns }} }) == 0
  for: 1h
  labels:
    severity: critical
  annotations:
    summary: A platform protection that the installation declares does not hold.
    description: The task backup-check (R-HA-023) found that the point-in-time recovery of PostgreSQL or the versioning of the blob bucket that backup.recovery_window and backup.object_versioning declare is missing, or that the grace period of the garbage collection does not cover the window. The label check names it (archive_mode, object_versioning, noncurrent_expiry, grace_period); trove admin backup check prints the reason. A check the credential may not read, or that is not declared, has no series and does not fire.
- alert: TroveRevocationJournalBacklog
  expr: max(trove_revocation_journal_pending{ {{ $ns }} }) > 0
  for: 15m
  labels:
    severity: warning
  annotations:
    summary: Entries of the revocation journal have waited for shipping to the blob bucket for 15 minutes.
    description: The outbox grows while the object store is out and ships afterwards, so a backlog of 15 minutes means the shipper does not write the _journal/ prefix of the blob bucket, or writes slower than entries arrive (R-HA-054). A restore taken now would replay a journal that lacks those entries, and a revocation among them would not be applied again. Check that worker pods are ready, the object store alerts, the shipper's log records and the write access of the credential to the prefix.
- alert: TroveRevocationJournalQuarantined
  expr: sum(increase(trove_revocation_journal_quarantined_total{ {{ $ns }} }[1h])) > 0
  labels:
    severity: critical
  annotations:
    summary: A change could not be written to the revocation journal and was set aside.
    description: An outbox row that cannot be encoded is moved to system_state.journal_quarantined so that it does not block the shipping of the rows behind it (R-HA-054). Any increase means that a change, possibly a revocation, is missing from the journal, and a restore from a backup or a platform restore would not apply it again. trove admin post-restore prints the quarantined rows; read the shipper's log records for the row and its error, repair or re-create the change, and take a new backup.
- alert: TroveValkeyUnavailable
  expr: max(trove_valkey_available{ {{ $ns }} }) == 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: Every replica runs without Valkey.
    description: Every replica reports its valkey check degraded (R-HA-057, INV-12). Rate limits fall back to per-replica shares, flights run without the cross-replica lock and cache invalidation relies on the change-log poll. A failover of the primary lasts well below two minutes; check the Sentinels, the primary and the credentials.
- alert: TroveValkeyUnavailableSingleReplica
  expr: min(trove_valkey_available{ {{ $ns }} }) == 0 and max(trove_valkey_available{ {{ $ns }} }) == 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: Some replicas have lost Valkey while others have it.
    description: The cause is on the path of the affected pods, not Valkey (a node, a network policy, DNS) (R-HA-057). Compare the pods by the label pod on trove_valkey_available and read the reason of the valkey check in GET /api/v1/system/replicas.
- alert: TroveValkeySentinelsDegraded
  expr: min(trove_valkey_sentinels_reachable{ {{ $ns }} }) < 2
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: A replica reaches fewer than two Sentinels.
    description: Without a majority of three Sentinels no failover can be elected (R-HA-056). The gauge counts the answers to a probe of every configured Sentinel (R-HA-057). Check the Sentinel pods and the path from the replica to each of them.
- alert: TroveValkeyFailoverFlapping
  expr: max(max_over_time(trove_valkey_primary_changes_total{ {{ $ns }} }[1h]) - min_over_time(trove_valkey_primary_changes_total{ {{ $ns }} }[1h])) > 2
  labels:
    severity: warning
  annotations:
    summary: The Valkey primary changed more than twice within an hour.
    description: Failovers repeat (Q-OPS-15). Look at the Valkey pods (restarts, memory, the node), the Sentinels' down-after-milliseconds against the network latency, and the log records "valkey primary changed".
- alert: TroveValkeyPrimaryDisagreement
  expr: max(trove_valkey_primary_views{ {{ $ns }} }) > 1
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: Replicas name different Valkey primaries.
    description: At least one replica is bound to a demoted primary (Q-OPS-15, R-ARCH-031), so locks, rate limits and cache signals are split between two servers. The registry in GET /api/v1/system/replicas shows each replica's primary; a restart of the replica that names the old one rebinds it.
{{- if .Values.backup.enabled }}
- alert: TroveBackupStale
  expr: time() - max(trove_backup_last_success_timestamp_seconds{ {{ $ns }} }) > 1.5 * max(trove_backup_expected_interval_seconds{ {{ $ns }} }) or (max(absent(trove_backup_last_success_timestamp_seconds{ {{ $ns }} })) and on() max(trove_backup_expected_interval_seconds{ {{ $ns }} } offset {{ $interval }}s))
  labels:
    severity: critical
  annotations:
    summary: The last successful backup is older than one and a half times backup.expected_interval, or none has succeeded in a whole interval.
    description: No create run has ended successfully within 1.5 times config.backup.expected_interval, the condition of the overdue mark in the API and the UI (R-BAK-014). On an installation whose first backup never succeeded the alert fires one expected interval after the replicas first reported. Look at GET /api/v1/system/backups, the CronJob (suspended? missed its starting deadline?) and its Jobs, and the log of the last pod.
- alert: TroveBackupFailed
  expr: max(trove_backup_last_failure_timestamp_seconds{ {{ $ns }} }) > (max(trove_backup_last_success_timestamp_seconds{ {{ $ns }} }) or vector(0))
  labels:
    severity: critical
  annotations:
    summary: The newest backup run failed and no backup has succeeded since.
    description: A create run ended failed or abandoned, or its lease expired in an active state, after the last success (R-BAK-014). A run interrupted by a drain does not count. The alert resolves with the next success. The cause is in GET /api/v1/system/backups (error code) and the log of the Job's pod; the exit codes are in R-BAK-013.
- alert: TroveBackupRunningLong
  expr: max(trove_backup_running_seconds{ {{ $ns }} }) > 0.5 * max(trove_backup_expected_interval_seconds{ {{ $ns }} })
  labels:
    severity: warning
  annotations:
    summary: A backup or delete run has been active for more than half of backup.expected_interval.
    description: The run holds the slot of the target and pins blobs against garbage collection while it lasts (R-BAK-007, R-BAK-010). A first backup of a large installation takes long; a run that makes no progress ends itself after config.backup.stall_timeout. Look at the progress in GET /api/v1/system/backups and the log of the pod.
- alert: TroveBackupGlobalHoldLong
  expr: max(trove_backup_gc_hold{ {{ $ns }},mode="all" }) == 1
  for: 30m
  labels:
    severity: warning
  annotations:
    summary: A backup has held garbage collection for all blobs for 30 minutes.
    description: The hold mode all stops the collection of every blob while a backup run opens its snapshot or writes its pins (R-BAK-007). It is meant to last minutes. Look at the active run in GET /api/v1/system/backups; a run that is stuck ends itself after config.backup.stall_timeout.
- alert: TroveBackupSourceDamage
  expr: max(trove_backup_source_blobs_damaged{ {{ $ns }} }) > 0
  labels:
    severity: critical
  annotations:
    summary: The newest successful backup found blobs of the installation damaged or missing.
    description: The backup could not copy the objects of these blobs correctly (R-BAK-009, R-BAK-014), so a restore of it lacks them. The list is in the result of the run (GET /api/v1/system/backups) and the log. Restore the objects from the bucket's versions or upload the content again, then take a new backup.
- alert: TroveBackupVerifyFindings
  expr: max(trove_backup_last_verify_findings{ {{ $ns }} }) > 0
  labels:
    severity: critical
  annotations:
    summary: The newest verification of a backup found missing, damaged or mismatching objects.
    description: trove backup verify exited 5 (R-BAK-019, R-BAK-014). The findings are in its output and the run record. A backup with findings may not restore; take a new one and look at the target (lifecycle rules, deletions, a credential that can delete).
{{- end }}
{{- end -}}

{{/* The names of every alert the chart can render, for the check of disabledAlerts. */}}
{{- define "trove.rules.names" -}}
{{- toJson (list "TroveReplicasNotReady" "TroveDatabaseUnreachable" "TroveObjectStoreUnreachable" "TroveObjectStoreUnreachableSingleReplica" "TroveDatabasePoolSaturated" "TroveControlConnectionWaits" "TroveJobsStuck" "TroveLeaseLost" "TroveMixedVersions" "TroveSpoolNearlyFull" "TroveVulnerabilityDataStale" "TroveVulnerabilitySyncFailing" "TroveVulnerabilityRecordsRejected" "TroveVulnerabilityRecordsInvalid" "TroveVulnerabilityMaliciousRecordRejected" "TroveVulnerabilityClaimedNameMalicious" "TroveMirrorFailing" "TroveMirrorStale" "TroveBridgeLeftOn" "TroveBlobMissing" "TroveGCStale" "TroveBackupCheckFailed" "TroveRevocationJournalBacklog" "TroveRevocationJournalQuarantined" "TroveValkeyUnavailable" "TroveValkeyUnavailableSingleReplica" "TroveValkeySentinelsDegraded" "TroveValkeyFailoverFlapping" "TroveValkeyPrimaryDisagreement" "TroveBackupStale" "TroveBackupFailed" "TroveBackupRunningLong" "TroveBackupGlobalHoldLong" "TroveBackupSourceDamage" "TroveBackupVerifyFindings") -}}
{{- end -}}
