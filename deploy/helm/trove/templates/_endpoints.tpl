{{/*
Endpoints (R-EP-008). values.endpoints holds one entry per endpoint: the entry "default" tunes the
default endpoint (hosts, surface and the policy keys only), every other entry is a further
endpoint with its own listener port, Service, Ingress, NetworkPolicy and, optionally, its own pool
of web pods. Objects are named <fullname>-ep-<name>; the pool Deployment is
<fullname>-web-ep-<name> and its pool label is ep-<name>.
*/}}

{{/* The names of the further endpoints, sorted, as a YAML list. Argument: the root context. */}}
{{- define "trove.endpointNames" -}}
{{- $names := list -}}
{{- range $name, $_ := .Values.endpoints -}}
{{- if ne $name "default" -}}
{{- $names = append $names $name -}}
{{- end -}}
{{- end -}}
{{- toYaml (sortAlpha $names) -}}
{{- end -}}

{{/*
One further endpoint with every default applied, as YAML. Argument: dict "root" . "name" "customers".
The defaults are those of an endpoint for an audience outside the organization (Q-EP-5, Q-EP-10).
Each key is looked up on its own, because a merge would let a default true win over an explicit
false.
*/}}
{{- define "trove.endpoint" -}}
{{- $e := index .root.Values.endpoints .name -}}
{{- $out := dict "name" .name "pool" "" -}}
{{- $_ := set $out "port" (int (dig "port" 0 $e)) -}}
{{- $_ := set $out "baseUrl" (dig "baseUrl" "" $e) -}}
{{- $_ := set $out "hosts" (dig "hosts" (list) $e) -}}
{{- $_ := set $out "wildcardHost" (dig "wildcardHost" "" $e) -}}
{{- $_ := set $out "hostRoots" (dig "hostRoots" (dict) $e) -}}
{{- $_ := set $out "wildcardRoot" (dig "wildcardRoot" "none" $e) -}}
{{- $_ := set $out "surface" (dig "surface" (list "packages") $e) -}}
{{- $_ := set $out "tokenTypes" (dig "auth" "tokenTypes" (list "sat") $e) -}}
{{- $_ := set $out "boundTokensOnly" (dig "auth" "boundTokensOnly" true $e) -}}
{{- $_ := set $out "anonymous" (dig "auth" "anonymous" false $e) -}}
{{- $_ := set $out "exposure" (dig "exposure" "rules" $e) -}}
{{- $_ := set $out "cacheFill" (dig "cacheFill" false $e) -}}
{{- $_ := set $out "rateLimitClass" (dig "rateLimitClass" "external" $e) -}}
{{- $_ := set $out "replicaHeader" (dig "replicaHeader" "never" $e) -}}
{{- $_ := set $out "maxInFlight" (int (dig "maxInFlight" 1024 $e)) -}}
{{- $_ := set $out "trustedProxies" (dig "trustedProxies" (list) $e) -}}
{{- $_ := set $out "tlsSecret" (dig "tls" "existingSecret" "" $e) -}}
{{- $_ := set $out "tlsExtraSecrets" (dig "tls" "extraSecrets" (list) $e) -}}
{{- $_ := set $out "tlsClientCaSecret" (dig "tls" "clientCaSecret" "" $e) -}}
{{- $_ := set $out "poolEnabled" (dig "pool" "enabled" false $e) -}}
{{- if $out.poolEnabled -}}
{{- $_ := set $out "pool" (printf "ep-%s" .name) -}}
{{- end -}}
{{- $_ := set $out "serviceType" (dig "service" "type" "ClusterIP" $e) -}}
{{- $_ := set $out "serviceAnnotations" (dig "service" "annotations" (dict) $e) -}}
{{- $_ := set $out "loadBalancerSourceRanges" (dig "service" "loadBalancerSourceRanges" (list) $e) -}}
{{- $_ := set $out "ingressEnabled" (dig "ingress" "enabled" false $e) -}}
{{- $_ := set $out "ingressClassName" (dig "ingress" "className" "" $e) -}}
{{- $_ := set $out "ingressAnnotations" (dig "ingress" "annotations" (dict) $e) -}}
{{- $_ := set $out "ingressTLS" (dig "ingress" "tls" (list) $e) -}}
{{- $_ := set $out "networkPolicyFrom" (dig "networkPolicy" "from" (list) $e) -}}
{{- toYaml $out -}}
{{- end -}}

{{/* The pool name ("ep-<name>") of an endpoint, empty when it runs on the main web pods. Argument: dict "root" . "name" "customers". */}}
{{- define "trove.endpointPool" -}}
{{- if dig "pool" "enabled" false (index .root.Values.endpoints .name) -}}
{{- printf "ep-%s" .name -}}
{{- end -}}
{{- end -}}

{{/*
Merge over onto base, key by key and recursively; a key of over wins, also when its value is false,
0 or empty (sprig's merge functions treat those as unset). Argument: dict "base" <map> "over" <map>.
Returns YAML.
*/}}
{{- define "trove.mergeValues" -}}
{{- $out := deepCopy .base -}}
{{- range $k, $v := .over -}}
{{- if and (kindIs "map" $v) (kindIs "map" (get $out $k)) -}}
{{- $_ := set $out $k (include "trove.mergeValues" (dict "base" (get $out $k) "over" $v) | fromYaml) -}}
{{- else -}}
{{- $_ := set $out $k $v -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
The values of an endpoint's pool Deployment: the web values with the pool's own keys on top (every
key of web may be overridden, R-EP-008). Argument: dict "root" . "name" "customers". Returns YAML.
*/}}
{{- define "trove.endpointPoolValues" -}}
{{- $pool := dig "pool" (dict) (index .root.Values.endpoints .name) -}}
{{- $over := omit $pool "enabled" -}}
{{- include "trove.mergeValues" (dict "base" .root.Values.web "over" $over) -}}
{{- end -}}

{{/*
The container ports a pod serves for endpoints, as a YAML list. Argument: dict "root" . "role" "web"
"pool" "". The main web pods serve the endpoints without a pool, a pool's pods serve their one
endpoint, worker pods serve none (R-EP-002, R-EP-008).
*/}}
{{- define "trove.endpointPorts" -}}
{{- $ports := list -}}
{{- if eq .role "web" -}}
{{- $root := .root -}}
{{- $pool := .pool -}}
{{- range $name := include "trove.endpointNames" $root | fromYamlArray -}}
{{- if eq (include "trove.endpointPool" (dict "root" $root "name" $name)) $pool -}}
{{- $ports = append $ports (int (dig "port" 0 (index $root.Values.endpoints $name))) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $ports -}}
{{- end -}}

{{/* The host of config.server.base_url. */}}
{{- define "trove.baseHost" -}}
{{- (urlParse (dig "server" "base_url" "" .Values.config)).hostname | lower -}}
{{- end -}}

{{/* The hosts of an endpoint for Ingress rules and routes: the host of its base URL, its exact hosts. Argument: the resolved endpoint dict. Returns a YAML list. */}}
{{- define "trove.endpointHosts" -}}
{{- $hosts := list ((urlParse .baseUrl).hostname | lower) -}}
{{- range .hosts -}}
{{- $hosts = append $hosts (lower .) -}}
{{- end -}}
{{- toYaml (uniq $hosts) -}}
{{- end -}}

{{/*
server.endpoints of trove.yaml (R-EP-008): an entry per further endpoint, and the default endpoint's
entry. With at least one further endpoint the default endpoint's hosts are the hosts of
config.server.base_url and config.server.additional_base_urls unless endpoints.default.hosts is
set, so traffic that reaches :8080 for another endpoint's host answers 421 (H-59).
Argument: the root context. Returns YAML, "{}" without endpoints.
*/}}
{{- define "trove.endpointsConfig" -}}
{{- $root := . -}}
{{- $out := dict -}}
{{- $names := include "trove.endpointNames" . | fromYamlArray -}}
{{- $dflt := dig "default" (dict) .Values.endpoints -}}
{{- if or $names $dflt -}}
{{- $d := dict -}}
{{- if hasKey $dflt "hosts" -}}
{{- $_ := set $d "hosts" $dflt.hosts -}}
{{- else if $names -}}
{{- $hosts := list (include "trove.baseHost" $root) -}}
{{- range (dig "server" "additional_base_urls" (list) $root.Values.config) -}}
{{- $hosts = append $hosts ((urlParse .).hostname | lower) -}}
{{- end -}}
{{- $_ := set $d "hosts" (uniq $hosts) -}}
{{- end -}}
{{- with $dflt.wildcardHost -}}{{- $_ := set $d "wildcard_host" . -}}{{- end -}}
{{- with $dflt.hostRoots -}}{{- $_ := set $d "host_roots" . -}}{{- end -}}
{{- with $dflt.wildcardRoot -}}{{- $_ := set $d "wildcard_root" . -}}{{- end -}}
{{- with $dflt.surface -}}{{- $_ := set $d "surface" . -}}{{- end -}}
{{- if hasKey $dflt "auth" -}}
{{- $a := dict -}}
{{- if hasKey $dflt.auth "tokenTypes" -}}{{- $_ := set $a "token_types" $dflt.auth.tokenTypes -}}{{- end -}}
{{- if hasKey $dflt.auth "boundTokensOnly" -}}{{- $_ := set $a "bound_tokens_only" $dflt.auth.boundTokensOnly -}}{{- end -}}
{{- if hasKey $dflt.auth "anonymous" -}}{{- $_ := set $a "anonymous" $dflt.auth.anonymous -}}{{- end -}}
{{- $_ := set $d "auth" $a -}}
{{- end -}}
{{- with $dflt.exposure -}}{{- $_ := set $d "exposure" . -}}{{- end -}}
{{- if hasKey $dflt "cacheFill" -}}{{- $_ := set $d "cache_fill" $dflt.cacheFill -}}{{- end -}}
{{- if hasKey $dflt "rateLimitClass" -}}{{- $_ := set $d "rate_limit_class" $dflt.rateLimitClass -}}{{- end -}}
{{- with $dflt.replicaHeader -}}{{- $_ := set $d "replica_header" . -}}{{- end -}}
{{- $_ := set $out "default" $d -}}
{{- end -}}
{{- range $name := $names -}}
{{- $e := include "trove.endpoint" (dict "root" $root "name" $name) | fromYaml -}}
{{- $c := dict "listen" (printf ":%d" (int $e.port)) "base_url" $e.baseUrl -}}
{{- if $e.hosts -}}{{- $_ := set $c "hosts" $e.hosts -}}{{- end -}}
{{- if $e.wildcardHost -}}{{- $_ := set $c "wildcard_host" $e.wildcardHost -}}{{- end -}}
{{- if $e.hostRoots -}}{{- $_ := set $c "host_roots" $e.hostRoots -}}{{- end -}}
{{- $_ := set $c "wildcard_root" $e.wildcardRoot -}}
{{- if $e.trustedProxies -}}{{- $_ := set $c "trusted_proxies" $e.trustedProxies -}}{{- end -}}
{{- if $e.pool -}}{{- $_ := set $c "pool" $e.pool -}}{{- end -}}
{{- $_ := set $c "max_in_flight" (int $e.maxInFlight) -}}
{{- $_ := set $c "surface" $e.surface -}}
{{- $_ := set $c "auth" (dict "token_types" $e.tokenTypes "bound_tokens_only" $e.boundTokensOnly "anonymous" $e.anonymous) -}}
{{- $_ := set $c "exposure" $e.exposure -}}
{{- $_ := set $c "cache_fill" $e.cacheFill -}}
{{- $_ := set $c "rate_limit_class" $e.rateLimitClass -}}
{{- $_ := set $c "replica_header" $e.replicaHeader -}}
{{- /* TLS (R-EP-004): position 0 is the endpoint's own Secret, else the listener pair of tls.secretName when further pairs follow. */ -}}
{{- if $root.Values.tls.enabled -}}
{{- $t := dict -}}
{{- $certs := list -}}
{{- if or $e.tlsSecret $e.tlsExtraSecrets -}}
{{- if $e.tlsSecret -}}
{{- $certs = append $certs (dict "cert_file" (printf "/var/run/secrets/trove/endpoints/%s/0/tls.crt" $name) "key_file" (printf "/var/run/secrets/trove/endpoints/%s/0/tls.key" $name)) -}}
{{- else -}}
{{- $certs = append $certs (dict "cert_file" "/var/run/secrets/trove/tls/tls.crt" "key_file" "/var/run/secrets/trove/tls/tls.key") -}}
{{- end -}}
{{- range $i, $_ := $e.tlsExtraSecrets -}}
{{- $certs = append $certs (dict "cert_file" (printf "/var/run/secrets/trove/endpoints/%s/%d/tls.crt" $name (add $i 1)) "key_file" (printf "/var/run/secrets/trove/endpoints/%s/%d/tls.key" $name (add $i 1))) -}}
{{- end -}}
{{- $_ := set $t "certificates" $certs -}}
{{- end -}}
{{- if $e.tlsClientCaSecret -}}
{{- $_ := set $t "client_ca_file" (printf "/var/run/secrets/trove/endpoints/%s/client-ca/ca.crt" $name) -}}
{{- end -}}
{{- if $t -}}{{- $_ := set $c "tls" $t -}}{{- end -}}
{{- end -}}
{{- $_ := set $out $name $c -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
The Secret mounts of the endpoints a pod serves, as a YAML list in the shape of trove.secretMounts.
Argument: dict "root" . "role" "web" "pool" "". Only web pods mount them.
*/}}
{{- define "trove.endpointSecretMounts" -}}
{{- $out := list -}}
{{- $root := . -}}
{{- if and .root.Values.tls.enabled (eq .role "web") -}}
{{- $root = .root -}}
{{- $pool := .pool -}}
{{- range $name := include "trove.endpointNames" $root | fromYamlArray -}}
{{- if eq (include "trove.endpointPool" (dict "root" $root "name" $name)) $pool -}}
{{- $e := include "trove.endpoint" (dict "root" $root "name" $name) | fromYaml -}}
{{- $base := printf "/var/run/secrets/trove/endpoints/%s" $name -}}
{{- if $e.tlsSecret -}}
{{- $out = append $out (dict "name" (printf "ep-%s-0" $name) "secret" $e.tlsSecret "path" (printf "%s/0" $base) "items" (list (dict "key" "tls.crt" "path" "tls.crt") (dict "key" "tls.key" "path" "tls.key"))) -}}
{{- end -}}
{{- range $i, $s := $e.tlsExtraSecrets -}}
{{- $out = append $out (dict "name" (printf "ep-%s-%d" $name (add $i 1)) "secret" $s "path" (printf "%s/%d" $base (add $i 1)) "items" (list (dict "key" "tls.crt" "path" "tls.crt") (dict "key" "tls.key" "path" "tls.key"))) -}}
{{- end -}}
{{- if $e.tlsClientCaSecret -}}
{{- $out = append $out (dict "name" (printf "ep-%s-client-ca" $name) "secret" $e.tlsClientCaSecret "path" (printf "%s/client-ca" $base) "items" (list (dict "key" "ca.crt" "path" "ca.crt"))) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
The effective backend-TLS settings of an endpoint's Ingress: ingress.backendTLS with the keys of
the endpoint's own ingress.backendTLS on top. Argument: dict "root" . "name" "customers". Returns YAML.
*/}}
{{- define "trove.endpointBackendTLS" -}}
{{- $own := dig "ingress" "backendTLS" (dict) (index .root.Values.endpoints .name) -}}
{{- include "trove.mergeValues" (dict "base" .root.Values.ingress.backendTLS "over" $own) -}}
{{- end -}}

{{/*
Non-secret S3 settings that the provisioner's Secret carries (bucket, region, endpoint), read into
environment variables by secretKeyRef. Credentials stay files. Argument: the root context.
*/}}
{{- define "trove.s3EnvFromSecret" -}}
{{- $s3 := .Values.secrets.s3 -}}
{{- if $s3.existingSecret -}}
{{- range $var, $key := dict "TROVE_STORAGE_S3_BUCKET" $s3.bucketKey "TROVE_STORAGE_S3_ENDPOINT" $s3.endpointKey "TROVE_STORAGE_S3_REGION" $s3.regionKey -}}
{{- if $key }}
- name: {{ $var }}
  valueFrom:
    secretKeyRef:
      name: {{ $s3.existingSecret }}
      key: {{ $key }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
