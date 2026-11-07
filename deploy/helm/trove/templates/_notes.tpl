{{/* The text of NOTES.txt (R-EP-008, R-HA-016, R-HA-064), a template of its own so that a test can render it. Argument: the root context. */}}
{{- define "trove.notes" -}}
{{- $names := include "trove.endpointNames" . | fromYamlArray -}}
{{- $fullname := include "trove.fullname" . -}}
{{- $bridge := include "trove.bridgeRouted" . -}}
{{- $counts := include "trove.connectionCounts" . | fromYaml -}}
{{- $db := dig "database" (dict) .Values.config -}}
{{- $maxConns := int (dig "pool" "max_conns" 20 $db) -}}
{{- $lease := int (dig "lease_pool_size" 4 $db) -}}
{{- $reserved := int (dig "preflight" "reserved_connections" 10 $db) -}}
{{- $pooled := eq (dig "pooler" "mode" "" $db) "transaction" -}}
{{- $clients := mul (add $counts.serving $counts.worker) $maxConns -}}
{{- $session := mul $counts.worker $lease -}}
{{- $budget := add (ternary (int (dig "pooler" "server_connections" 0 $db)) $clients $pooled) $session 2 4 $reserved 3 -}}
trove is installed as release {{ .Release.Name }} in namespace {{ .Release.Namespace }}.
{{- if $bridge }}

The cut into roles is not complete (step 1 of 3): web.enabled is true, so {{ $fullname }}-web and the Service {{ $fullname }} still carry every path, and {{ $fullname }}-api, {{ $fullname }}-packages and {{ $fullname }}-worker run beside them without edge traffic. Step 2: once they are ready, run helm upgrade with --set web.enabled=true --set web.routed=false, which moves the edge onto them while {{ $fullname }}-web keeps running. Step 3 removes the bridge (R-HA-064).
{{- else if .Values.web.enabled }}

The cut into roles is not complete (step 2 of 3): the edge routes to {{ $fullname }}-api and {{ $fullname }}-packages, and {{ $fullname }}-web and the Service {{ $fullname }} still run without edge traffic. Move in-cluster callers to the Services {{ $fullname }}-api and {{ $fullname }}-packages, then run step 3: helm upgrade with web.enabled: false (the default), which removes them (R-HA-064).
{{- end }}

{{- if .Values.postRestore.enabled }}

postRestore.enabled is true: the hook Job {{ $fullname }}-post-restore-* cleans up after the platform restore to {{ .Values.postRestore.restoredTo }} (R-HA-025). Leave every role at zero until it has succeeded, then run helm upgrade with postRestore.enabled: false (the default) and the normal replica counts. A further upgrade with postRestore.enabled left on repeats the same restore point, which the command answers without running its steps again; a different postRestore.restoredTo runs them.
{{- end }}

Roles
  api        Service {{ $fullname }}-api:{{ .Values.service.port }}        UI, management API, sign-in, CI exchange
  packages   Service {{ $fullname }}-packages:{{ .Values.service.port }}   package protocols and raw files
  worker     no client Service (operations port {{ .Values.service.opsPort }} only)
{{- range $role := list "api" "packages" }}
{{- range $p := (index $.Values $role).pools }}
  {{ $role }} pool {{ $p.name }}   Service {{ $fullname }}-{{ $role }}-{{ $p.name }}   paths: {{ join ", " $p.paths }}
{{- end }}
{{- end }}
{{- range $p := .Values.worker.pools }}
  worker pool {{ $p.name }}   Deployment {{ $fullname }}-worker-{{ $p.name }}   queues: {{ join ", " $p.queues }}
{{- end }}

Database connections: PostgreSQL needs max_connections of at least {{ $budget }} ({{ $counts.serving }} serving and {{ $counts.worker }} worker replicas at most, surge included; R-HA-016).
{{- if $pooled }}
The pooler must accept {{ $clients }} client connections on its request pool and {{ add $counts.serving $counts.worker }} on its control pool.
{{- end }}

Endpoints
{{- $dh := list (include "trove.baseHost" .) -}}
{{- range (dig "server" "additional_base_urls" (list) .Values.config) }}{{ $dh = append $dh ((urlParse .).hostname | lower) }}{{ end -}}
{{- range (dig "default" "hosts" (list) .Values.endpoints) }}{{ $dh = append $dh (lower .) }}{{ end }}
  default   Services {{ if $bridge }}{{ $fullname }}{{ else }}{{ $fullname }}-api, {{ $fullname }}-packages{{ end }}:{{ .Values.service.port }}   hosts: {{ join ", " (uniq $dh) }}
{{- range $name := $names }}
{{- $e := include "trove.endpoint" (dict "root" $ "name" $name) | fromYaml }}
{{- $hosts := include "trove.endpointHosts" $e | fromYamlArray }}
{{- $roles := ternary (list "web") (include "trove.surfaceRoles" (dict "root" $ "name" $name) | fromYamlArray) (not (empty $bridge)) }}
  {{ $name }}   Service {{ $fullname }}-ep-{{ $name }}{{ if and (eq (len $roles) 2) (not $bridge) }}, {{ $fullname }}-api-ep-{{ $name }}{{ end }}:{{ $e.port }}   hosts: {{ join ", " $hosts }}{{ with $e.wildcardHost }}, {{ . }}{{ end }}   pods: {{ if $e.pool }}{{ range $i, $role := $roles }}{{ if $i }}, {{ end }}pool {{ $fullname }}-{{ $role }}-ep-{{ $name }}{{ end }}{{ else }}the main {{ join ", " $roles }} Deployment{{ if gt (len $roles) 1 }}s{{ end }}{{ end }}
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
