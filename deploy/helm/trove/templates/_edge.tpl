{{/*
The edge's path rules (R-HA-037, Q-HA-71, R-EP-008, Q-EP-71) as data, so that the Ingress of the
default endpoint, the Ingress of each further endpoint and the BackendTLSPolicies read one table. The
rules are fixed, not values: every one is a Prefix match (whole path elements, the longest match
wins), and a pool's paths are the only data that goes in.

Argument: dict "root" . "endpoint" "" (the default endpoint) or the name of a further endpoint, and
optionally "bridge" false for the rules of the target shape whatever the values say. Returns a YAML list
of {path, service}, most specific first. The Service's port is the port named http for the default
endpoint and the endpoint's port number for a further one.
*/}}
{{- define "trove.edgeRules" -}}
{{- $root := .root -}}
{{- $fullname := include "trove.fullname" $root -}}
{{- $bridge := include "trove.bridgeRouted" $root -}}
{{- if hasKey . "bridge" -}}
{{- $bridge = .bridge -}}
{{- end -}}
{{- $rules := list -}}
{{- if not .endpoint -}}
{{- if $bridge -}}
{{- /* The bridge keeps the edge of the releases before the cut: every host whole to <fullname> (R-HA-064). */ -}}
{{- $rules = append $rules (dict "path" "/" "service" $fullname) -}}
{{- else -}}
{{- range $role := list "api" "packages" -}}
{{- range $p := (index $root.Values $role).pools -}}
{{- /* routed: false renders the pool without its route, the first step of adding a pool (Q-HA-90). */ -}}
{{- if dig "routed" true $p -}}
{{- range $path := $p.paths -}}
{{- $rules = append $rules (dict "path" $path "service" (printf "%s-%s-%s" $fullname $role $p.name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $api := printf "%s-api" $fullname -}}
{{- $packages := printf "%s-packages" $fullname -}}
{{- $rules = concat $rules (list (dict "path" "/api/v1" "service" $api) (dict "path" "/api" "service" $packages) (dict "path" "/files" "service" $packages) (dict "path" "/v2" "service" $packages) (dict "path" "/.well-known" "service" $packages) (dict "path" "/" "service" $api)) -}}
{{- end -}}
{{- else -}}
{{- $name := .endpoint -}}
{{- $e := include "trove.endpoint" (dict "root" $root "name" $name) | fromYaml -}}
{{- $roles := include "trove.surfaceRoles" (dict "root" $root "name" $name) | fromYamlArray -}}
{{- $svc := printf "%s-ep-%s" $fullname $name -}}
{{- if or $bridge (eq (len $roles) 1) -}}
{{- /* Within one role, or in the bridge: the whole host to the one Service. */ -}}
{{- $rules = append $rules (dict "path" "/" "service" $svc) -}}
{{- else -}}
{{- /* A surface with families of both roles: the rows of the table whose families it holds. */ -}}
{{- $api := printf "%s-api-ep-%s" $fullname $name -}}
{{- $s := $e.surface -}}
{{- if or (has "api" $s) (has "ci_exchange" $s) -}}
{{- $rules = append $rules (dict "path" "/api/v1" "service" $api) -}}
{{- end -}}
{{- if has "packages" $s -}}
{{- $rules = append $rules (dict "path" "/api" "service" $svc) -}}
{{- end -}}
{{- if has "files" $s -}}
{{- $rules = append $rules (dict "path" "/files" "service" $svc) -}}
{{- end -}}
{{- if has "packages" $s -}}
{{- $rules = concat $rules (list (dict "path" "/v2" "service" $svc) (dict "path" "/.well-known" "service" $svc)) -}}
{{- end -}}
{{- $rules = append $rules (dict "path" "/" "service" $api) -}}
{{- end -}}
{{- end -}}
{{- toYaml $rules -}}
{{- end -}}

{{/* The Services the default endpoint's edge names, in order and without repeats, as a YAML list. Argument: the root context. */}}
{{- define "trove.edgeServices" -}}
{{- $out := list -}}
{{- range $r := include "trove.edgeRules" (dict "root" . "endpoint" "") | fromYamlArray -}}
{{- $out = append $out $r.service -}}
{{- end -}}
{{- toYaml (uniq $out) -}}
{{- end -}}

{{/*
The Services an edge of the default endpoint may route to while this release is applied, as a YAML list: those
of the target shape, and with web.enabled also <fullname>, whether the chart's Ingress routes to it (step 1 of
R-HA-064) or no longer does (step 2). A route kept outside the chart (a Gateway API HTTPRoute) moves between the
two sets at a time the chart does not know, so both stay verifiable for the whole bridge. Argument: the root context.
*/}}
{{- define "trove.edgeBackendServices" -}}
{{- $out := list -}}
{{- if .Values.web.enabled -}}
{{- $out = append $out (include "trove.fullname" .) -}}
{{- end -}}
{{- range $r := include "trove.edgeRules" (dict "root" . "endpoint" "" "bridge" false) | fromYamlArray -}}
{{- $out = append $out $r.service -}}
{{- end -}}
{{- toYaml (uniq $out) -}}
{{- end -}}

{{/*
"true" while the edge of the releases before the cut is rendered: the bridge of R-HA-064 runs (web.enabled) and
still carries the edge (web.routed, default true). Step 2 of the cut sets web.routed false, so the edge moves to
the target shape while the bridge's pods and the Service <fullname> keep running; step 3 (web.enabled false)
removes them once no route names them. Empty otherwise. Argument: the root context.
*/}}
{{- define "trove.bridgeRouted" -}}
{{- if and .Values.web.enabled (dig "routed" true .Values.web) -}}true{{- end -}}
{{- end -}}
