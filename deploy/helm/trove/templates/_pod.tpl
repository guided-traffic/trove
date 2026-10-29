{{/*
The pod pieces web and worker share. Argument: dict "root" . "role" "web".
*/}}

{{/* Probes (R-HA-034). Argument: the root context. */}}
{{- define "trove.probes" -}}
{{- range $name := list "startup" "liveness" "readiness" }}
{{- $p := get $.Values.probes $name }}
{{- if $p.enabled }}
{{ $name }}Probe:
  {{- if $p.custom }}
  {{- toYaml $p.custom | nindent 2 }}
  {{- else }}
  httpGet:
    path: {{ $p.path }}
    port: ops
    {{- if $.Values.tls.enabled }}
    scheme: HTTPS
    {{- end }}
  periodSeconds: {{ $p.periodSeconds }}
  timeoutSeconds: {{ $p.timeoutSeconds }}
  failureThreshold: {{ $p.failureThreshold }}
  {{- if eq $name "readiness" }}
  successThreshold: {{ $p.successThreshold }}
  {{- end }}
  {{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
Topology spread (R-HA-032): zone (ScheduleAnyway) and host (DoNotSchedule). Both honour node
taints, so a cordoned node does not count as a domain and the replacement of an evicted pod can
schedule (R-HA-059), and both compute the skew per ReplicaSet (matchLabelKeys), so a rollout does
not pile new pods onto the nodes the old ones leave. Non-empty topologySpreadConstraints replace
the generated ones and carry their own labelSelector.
*/}}
{{- define "trove.topologySpread" -}}
{{- $r := index .root.Values .role -}}
{{- if $r.topologySpreadConstraints }}
topologySpreadConstraints:
{{- toYaml $r.topologySpreadConstraints | nindent 2 }}
{{- else if $r.topologySpread.enabled }}
topologySpreadConstraints:
{{- range $key, $c := dict "topology.kubernetes.io/zone" $r.topologySpread.zone "kubernetes.io/hostname" $r.topologySpread.host }}
  - topologyKey: {{ $key }}
    maxSkew: {{ $c.maxSkew }}
    whenUnsatisfiable: {{ $c.whenUnsatisfiable }}
    nodeTaintsPolicy: {{ $r.topologySpread.nodeTaintsPolicy }}
    matchLabelKeys:
      - pod-template-hash
    labelSelector:
      matchLabels:
        {{- include "trove.selectorLabels" (dict "root" $.root "component" $.role) | nindent 8 }}
{{- end }}
{{- end }}
{{- end -}}

{{/* The environment variables the chart sets (R-HA-030). */}}
{{- define "trove.podEnv" -}}
- name: TROVE_POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: TROVE_POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: TROVE_NODE_NAME
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName
{{- end -}}
