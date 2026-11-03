{{/*
The alerts of the PrometheusRule (R-BAS-041) as a YAML list, before disabledAlerts is applied. Names are
stable. Thresholds come from gauges of the Bastion (trove_bastion_token_warn_before_seconds,
trove_bastion_grace_period_seconds, trove_bastion_answer_period_seconds), never from chart values
(R-HA-045's rule).

Every selector is scoped to this release (namespace and the ops Service), because one Prometheus can
scrape several Bastions and a min() or max() over all of them would blend their states. The series carry
those labels when they are scraped through the ServiceMonitor of this chart.

TroveBastionMainUnreachable fires when no replica reaches the main installation (max of the gauge is 0);
the single-replica alert covers the other case, some replicas reaching it and some not. The sketch of the
spec page used min() for "every replica", which fires on one replica.
*/}}
{{- define "trove-bastion.rules.all" -}}
{{- $fullname := include "trove-bastion.fullname" . -}}
{{- $s := printf "namespace=%q,service=%q" .Release.Namespace (printf "%s-ops" $fullname) -}}
{{- $replicas := "" -}}
{{- if eq (include "trove-bastion.workloadKind" .) "StatefulSet" -}}
{{- $replicas = printf "min(kube_statefulset_status_replicas_ready{namespace=%q,statefulset=%q}) < 2" .Release.Namespace $fullname -}}
{{- else -}}
{{- $replicas = printf "min(kube_deployment_status_replicas_available{namespace=%q,deployment=%q}) < 2" .Release.Namespace $fullname -}}
{{- end -}}
- alert: TroveBastionInterfaceMismatch
  expr: max(trove_bastion_interface_mismatch{ {{ $s }} }) == 1
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: The Bastion and the main installation speak different interface versions.
    description: The Bastion serves nothing it cannot verify with the main installation. Upgrade the side that is behind (R-BAS-028).
- alert: TroveBastionMainUnreachable
  expr: max(trove_bastion_main_reachable{ {{ $s }} }) == 0
  for: 2m
  labels:
    severity: critical
  annotations:
    summary: No Bastion replica reaches the main installation.
    description: Callers are served from answers within the grace period and get 503 afterwards (R-BAS-027).
- alert: TroveBastionMainUnreachableSingleReplica
  expr: min(trove_bastion_main_reachable{ {{ $s }} }) == 0 and max(trove_bastion_main_reachable{ {{ $s }} }) == 1
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: Some Bastion replicas do not reach the main installation while others do.
    description: A network path, a NetworkPolicy or one node is the likely cause.
- alert: TroveBastionGraceExhausted
  expr: time() - min(trove_bastion_main_unreachable_since_timestamp_seconds{ {{ $s }} } > 0) > max(trove_bastion_answer_period_seconds{ {{ $s }} } + trove_bastion_grace_period_seconds{ {{ $s }} })
  labels:
    severity: critical
  annotations:
    summary: The main installation has been unreachable longer than the answer period plus the grace period.
    description: The Bastion refuses every request with 503 until the main installation is reachable again (R-BAS-027).
- alert: TroveBastionBaseURLMismatch
  expr: max(trove_bastion_base_url_mismatch{ {{ $s }} }) == 1
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: config.server.base_url differs from the base URL the main installation registered for this Bastion.
    description: Correct config.server.base_url or the registration (R-BAS-010).
- alert: TroveBastionTokenExpiresSoon
  expr: min(trove_bastion_token_remaining_seconds{ {{ $s }} }) < max(trove_bastion_token_warn_before_seconds{ {{ $s }} })
  for: 1h
  labels:
    severity: warning
  annotations:
    summary: The Bastion token expires within the warning period.
    description: Issue a second token at the main installation and replace the token Secret (R-BAS-006).
- alert: TroveBastionTokenRefused
  expr: max(trove_bastion_main_token_refused{ {{ $s }} }) == 1
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: The main installation refuses the Bastion token.
    description: The token was revoked or expired. Replace the token Secret (R-BAS-006).
- alert: TroveBastionFeedStale
  expr: max(trove_bastion_feed_lag_seconds{ {{ $s }} }) > 30
  for: 2m
  labels:
    severity: warning
  annotations:
    summary: The revocation feed lags more than 30 seconds behind the main installation.
    description: A revocation at the main installation reaches the Bastion late (R-BAS-026).
- alert: TroveBastionMainBudgetSaturated
  expr: sum(rate(trove_bastion_main_budget_exhausted_total{ {{ $s }} }[5m])) > 0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: The concurrency budget towards the main installation is exhausted.
    description: Requests are answered 503 bastion_busy. Raise config.bastion.main.max_concurrent or find the traffic (R-BAS-025).
- alert: TroveBastionAuditEventsDropped
  expr: increase(trove_bastion_audit_events_dropped_total{ {{ $s }} }[15m]) > 0
  labels:
    severity: warning
  annotations:
    summary: Download events were dropped before they reached the main installation's audit.
    description: The audit buffer was full (R-BAS-039, H-391).
- alert: TroveBastionCacheCorrupt
  expr: increase(trove_bastion_cache_corrupt_total{ {{ $s }} }[1h]) > 0
  labels:
    severity: warning
  annotations:
    summary: Cached content failed its integrity check.
    description: The object and its entries were deleted and fetched again (R-BAS-032). Check the cache volume or bucket.
{{- if include "trove-bastion.valkeyOn" . }}
- alert: TroveBastionValkeyUnavailable
  expr: max(trove_valkey_available{ {{ $s }} }) == 0
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: No Bastion replica reaches Valkey.
    description: Blocks, counters and answers work per replica at the divided limits until it is back (R-BAS-034).
{{- end }}
- alert: TroveBastionReplicasNotReady
  expr: {{ $replicas }}
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: Fewer than two Bastion replicas are ready.
    description: Needs kube-state-metrics. The Bastion is meant to run at least two replicas spread across nodes (R-BAS-043).
{{- end -}}

{{/* The names of every alert the chart can render, for the check of disabledAlerts. */}}
{{- define "trove-bastion.rules.names" -}}
{{- toJson (list "TroveBastionInterfaceMismatch" "TroveBastionMainUnreachable" "TroveBastionMainUnreachableSingleReplica" "TroveBastionGraceExhausted" "TroveBastionBaseURLMismatch" "TroveBastionTokenExpiresSoon" "TroveBastionTokenRefused" "TroveBastionFeedStale" "TroveBastionMainBudgetSaturated" "TroveBastionAuditEventsDropped" "TroveBastionCacheCorrupt" "TroveBastionValkeyUnavailable" "TroveBastionReplicasNotReady") -}}
{{- end -}}
