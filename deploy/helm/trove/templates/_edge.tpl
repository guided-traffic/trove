{{/*
The edge's path rules (R-HA-037, Q-HA-71, R-EP-008, Q-EP-71) as data, so that the Ingress of the
default endpoint, the Ingress of each further endpoint and the BackendTLSPolicies read one table. The
rules are fixed, not values: every one is a Prefix match (whole path elements, the longest match
wins), and a pool's paths are the only data that goes in.

Argument: dict "root" . "endpoint" "" (the default endpoint) or the name of a further endpoint.
Returns a YAML list of {path, service}, most specific first. The Service's port is the port named
http for the default endpoint and the endpoint's port number for a further one.
*/}}
{{- define "trove.edgeRules" -}}
{{- $root := .root -}}
{{- $fullname := include "trove.fullname" $root -}}
{{- $bridge := $root.Values.web.enabled -}}
{{- $rules := list -}}
{{- if not .endpoint -}}
{{- if $bridge -}}
{{- /* The bridge keeps the edge of the releases before the cut: every host whole to <fullname> (R-HA-064). */ -}}
{{- $rules = append $rules (dict "path" "/" "service" $fullname) -}}
{{- else -}}
{{- range $role := list "api" "packages" -}}
{{- range $p := (index $root.Values $role).pools -}}
{{- range $path := $p.paths -}}
{{- $rules = append $rules (dict "path" $path "service" (printf "%s-%s-%s" $fullname $role $p.name)) -}}
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
{{- $api := printf "%s-api" $svc -}}
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
