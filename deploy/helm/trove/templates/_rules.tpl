{{/*
The alerts of the PrometheusRule (R-HA-045) as a YAML list, before disabledAlerts is applied. Names are
stable. Only the alerts of the vulnerability scanning are rendered so far (R-VULN-024): the other rows of
R-HA-045 are not implemented in this chart (docs/developer/helm-chart.md lists them).

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
{{- end -}}

{{/* The names of every alert the chart can render, for the check of disabledAlerts. */}}
{{- define "trove.rules.names" -}}
{{- toJson (list "TroveVulnerabilityDataStale" "TroveVulnerabilitySyncFailing" "TroveVulnerabilityClaimedNameMalicious") -}}
{{- end -}}
