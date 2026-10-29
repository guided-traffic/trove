{{/* The text of NOTES.txt (R-EP-008), a template of its own so that a test can render it. Argument: the root context. */}}
{{- define "trove.notes" -}}
{{- $names := include "trove.endpointNames" . | fromYamlArray -}}
{{- $fullname := include "trove.fullname" . -}}
trove is installed as release {{ .Release.Name }} in namespace {{ .Release.Namespace }}.

Endpoints
{{- $dh := list (include "trove.baseHost" .) -}}
{{- range (dig "server" "additional_base_urls" (list) .Values.config) }}{{ $dh = append $dh ((urlParse .).hostname | lower) }}{{ end -}}
{{- range (dig "default" "hosts" (list) .Values.endpoints) }}{{ $dh = append $dh (lower .) }}{{ end }}
  default   Service {{ $fullname }}:{{ .Values.service.port }}   hosts: {{ join ", " (uniq $dh) }}
{{- range $name := $names }}
{{- $e := include "trove.endpoint" (dict "root" $ "name" $name) | fromYaml }}
{{- $hosts := include "trove.endpointHosts" $e | fromYamlArray }}
  {{ $name }}   Service {{ $fullname }}-ep-{{ $name }}:{{ $e.port }}   hosts: {{ join ", " $hosts }}{{ with $e.wildcardHost }}, {{ . }}{{ end }}   pods: {{ if $e.pool }}pool {{ $fullname }}-web-ep-{{ $name }}{{ else }}{{ $fullname }}-web{{ end }}
{{- end }}
{{- range $name := $names }}
{{- $e := include "trove.endpoint" (dict "root" $ "name" $name) | fromYaml }}
{{- if and (eq $e.exposure "rules") (not $e.pool) }}

Warning: endpoint {{ $name }} has exposure: rules and shares the pods of the default endpoint. Give it a pool (endpoints.{{ $name }}.pool.enabled) so load and a defect on one cannot reach the other.
{{- end }}
{{- if and (eq $e.exposure "rules") (not $e.networkPolicyFrom) $.Values.networkPolicy.enabled }}

Warning: endpoint {{ $name }} has exposure: rules and an empty networkPolicy.from, so its port {{ $e.port }} accepts connections from every pod. List the peers of its ingress controller in endpoints.{{ $name }}.networkPolicy.from.
{{- end }}
{{- end }}
{{- if and $names (not (hasKey (dig "default" (dict) .Values.endpoints) "hosts")) }}

The default endpoint answers 421 for a host it does not list. Clients that reach it by another name, such as the in-cluster Service name, need that name in endpoints.default.hosts.
{{- end }}
{{- end -}}
