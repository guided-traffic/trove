{{/*
The alerts of the PrometheusRule (R-HA-045) as a YAML list, before disabledAlerts is applied. Names are
stable. Only the alerts of the vulnerability scanning (R-VULN-024), the two alerts of the mirror mode (R-OPS-046,
Q-OPS-160, Q-OPS-171), the alert of a bridge left on (R-HA-064, Q-HA-92) and TroveBlobMissing (R-HA-024, Q-STOR-152) are
rendered so far: the other rows of R-HA-045 are not implemented in this chart (docs/developer/helm-chart.md lists them).

Every selector is scoped to the namespace of the release, because one Prometheus can scrape several
installations and a max() over all of them would blend their states. A scrape through the Prometheus
operator (ServiceMonitor or PodMonitor) always carries the namespace label. The chart has no ServiceMonitor
of its own yet, so the scrape is the operator's.

The stale threshold is config.vulnerabilities.stale_after, the value the binary reads (default 24h), so that
the alert and the report banner agree (R-VULN-004). It is a duration parsed by trove.seconds.
*/}}
{{- define "trove.rules.all" -}}
{{- $ns := printf "namespace=%q" .Release.Namespace -}}
{{- $stale := include "trove.seconds" (dig "vulnerabilities" "stale_after" "24h" .Values.config) -}}
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
{{- end -}}

{{/* The names of every alert the chart can render, for the check of disabledAlerts. */}}
{{- define "trove.rules.names" -}}
{{- toJson (list "TroveVulnerabilityDataStale" "TroveVulnerabilitySyncFailing" "TroveVulnerabilityClaimedNameMalicious" "TroveMirrorFailing" "TroveMirrorStale" "TroveBridgeLeftOn" "TroveBlobMissing") -}}
{{- end -}}
