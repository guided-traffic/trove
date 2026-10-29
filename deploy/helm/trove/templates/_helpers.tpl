{{/*
Name of the chart (app.kubernetes.io/name).
*/}}
{{- define "trove.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 50 | trimSuffix "-" -}}
{{- end -}}

{{/*
<fullname> (R-HA-028): the release name, or <release>-trove when the release name does not
contain the chart name; fullnameOverride wins. Cut at 50 characters so that every derived name
(<fullname>-worker, <fullname>-migrate) stays a valid DNS label.
*/}}
{{- define "trove.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "trove.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "trove.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "trove.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Selector labels of one component. Argument: dict "root" . "component" "web".
web and worker pods carry trove/pool (R-HA-028): the empty value for the main Deployments, so a
pool added later never overlaps their selectors. Selector labels are immutable, which is why the
label is there from the first release.
*/}}
{{- define "trove.selectorLabels" -}}
app.kubernetes.io/name: {{ include "trove.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- if or (eq .component "web") (eq .component "worker") }}
trove/pool: {{ .pool | default "" | quote }}
{{- end }}
{{- end -}}

{{/*
Labels on every object. Argument: dict "root" . "component" "web".
*/}}
{{- define "trove.labels" -}}
{{ include "trove.selectorLabels" . }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | replace "+" "_" | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ include "trove.chart" .root }}
{{- with .root.Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Annotations on every object. Argument: the root context. */}}
{{- define "trove.commonAnnotations" -}}
{{- with .Values.commonAnnotations }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* The image reference; a digest wins over the tag. */}}
{{- define "trove.image" -}}
{{- $registry := default .Values.image.registry .Values.global.imageRegistry -}}
{{- $repo := .Values.image.repository -}}
{{- $ref := ternary (printf "%s/%s" $registry $repo) $repo (ne $registry "") -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $ref .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $ref (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "trove.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Bytes of a quantity: an integer, or an integer with a binary unit (Ki, Mi, Gi, Ti, optionally
followed by B). Fails for anything else, so a typo in a size never becomes a wrong budget.
*/}}
{{- define "trove.bytes" -}}
{{- $s := "" -}}
{{- if kindIs "float64" . -}}
{{- $s = printf "%.0f" . -}}
{{- else -}}
{{- $s = toString . | trim -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+(Ki|Mi|Gi|Ti)?B?$" $s) -}}
{{- fail (printf "trove: %q is not a byte quantity; use an integer or a binary unit such as 512Mi, 4Gi or 2GiB" $s) -}}
{{- end -}}
{{- $n := regexFind "^[0-9]+" $s | int64 -}}
{{- $u := regexFind "[A-Za-z]+$" $s | trimSuffix "B" -}}
{{- $mult := dict "" 1 "Ki" 1024 "Mi" 1048576 "Gi" 1073741824 "Ti" 1099511627776 -}}
{{- mul $n (get $mult $u | int64) -}}
{{- end -}}

{{/*
Seconds of a duration made of whole hours, minutes and seconds (1h30m, 90s, 5s).
*/}}
{{- define "trove.seconds" -}}
{{- $s := toString . | trim -}}
{{- if not (regexMatch "^([0-9]+h)?([0-9]+m)?([0-9]+s)?$" $s) -}}
{{- fail (printf "trove: %q is not a duration of whole hours, minutes and seconds (for example 5s or 1m30s)" $s) -}}
{{- end -}}
{{- $h := regexFind "[0-9]+h" $s | trimSuffix "h" | default "0" | int64 -}}
{{- $m := regexFind "[0-9]+m" $s | trimSuffix "m" | default "0" | int64 -}}
{{- $sec := regexFind "[0-9]+s" $s | trimSuffix "s" | default "0" | int64 -}}
{{- add (mul $h 3600) (mul $m 60) $sec -}}
{{- end -}}

{{/* The size of the spool volume of a role. Argument: dict "root" . "role" "web". */}}
{{- define "trove.spoolSize" -}}
{{- $v := .root.Values -}}
{{- $r := index $v .role -}}
{{- if eq $v.spool.type "ephemeral" -}}
{{- $v.spool.ephemeral.size -}}
{{- else if $r.spool.sizeLimit -}}
{{- $r.spool.sizeLimit -}}
{{- else -}}
{{- $v.spool.sizeLimit -}}
{{- end -}}
{{- end -}}

{{/*
storage.spool.max_bytes of a role: spool size minus headroom (R-HA-035).
*/}}
{{- define "trove.spoolBudget" -}}
{{- $size := include "trove.bytes" (include "trove.spoolSize" .) | int64 -}}
{{- $headroom := include "trove.bytes" .root.Values.spool.headroom | int64 -}}
{{- if le $size $headroom -}}
{{- fail (printf "trove: spool.headroom (%s) must be smaller than the spool size of %s (%s)" .root.Values.spool.headroom .role (include "trove.spoolSize" .)) -}}
{{- end -}}
{{- sub $size $headroom -}}
{{- end -}}

{{/*
The configuration file (R-HA-030): config merged with the keys the chart manages. The managed
keys win; setting one inside config is refused by trove.validate.
*/}}
{{- define "trove.config" -}}
{{- $s := .Values.secrets -}}
{{- $managed := dict -}}
{{- $_ := set $managed "storage" (dict "spool" (dict "dir" "/var/lib/trove/spool")) -}}
{{- $db := dict "username_file" "/var/run/secrets/trove/database/username" "password_file" "/var/run/secrets/trove/database/password" -}}
{{- if $s.databaseCA.existingSecret -}}
{{- $_ := set $db "ssl_root_cert_file" "/var/run/secrets/trove/database-ca/ca.crt" -}}
{{- end -}}
{{- if $s.databaseClientCert.existingSecret -}}
{{- $_ := set $db "ssl_cert_file" "/var/run/secrets/trove/database-client/tls.crt" -}}
{{- $_ := set $db "ssl_key_file" "/var/run/secrets/trove/database-client/tls.key" -}}
{{- end -}}
{{- $_ := set $managed "database" $db -}}
{{- $vk := dict -}}
{{- if $s.valkey.usernameKey -}}
{{- $_ := set $vk "username_file" "/var/run/secrets/trove/valkey/username" -}}
{{- end -}}
{{- $_ := set $vk "password_file" "/var/run/secrets/trove/valkey/password" -}}
{{- $sentinel := dict -}}
{{- if $s.valkeySentinel.existingSecret -}}
{{- if $s.valkeySentinel.usernameKey -}}
{{- $_ := set $sentinel "username_file" "/var/run/secrets/trove/valkey-sentinel/username" -}}
{{- end -}}
{{- $_ := set $sentinel "password_file" "/var/run/secrets/trove/valkey-sentinel/password" -}}
{{- end -}}
{{- $_ := set $vk "sentinel" $sentinel -}}
{{- $tls := dict -}}
{{- if $s.valkeyCA.existingSecret -}}
{{- $_ := set $tls "ca_file" "/var/run/secrets/trove/valkey-ca/ca.crt" -}}
{{- end -}}
{{- if $s.valkeyClientCert.existingSecret -}}
{{- $_ := set $tls "cert_file" "/var/run/secrets/trove/valkey-client/tls.crt" -}}
{{- $_ := set $tls "key_file" "/var/run/secrets/trove/valkey-client/tls.key" -}}
{{- end -}}
{{- $_ := set $vk "tls" $tls -}}
{{- $_ := set $managed "valkey" $vk -}}
{{- if eq (dig "storage" "s3" "credentials" "source" "" .Values.config) "static" -}}
{{- $_ := set (get $managed "storage") "s3" (dict "credentials" (dict "access_key_id_file" "/var/run/secrets/trove/s3/access-key-id" "secret_access_key_file" "/var/run/secrets/trove/s3/secret-access-key")) -}}
{{- end -}}
{{- if eq (dig "metrics" "auth" "" .Values.config) "static" -}}
{{- $_ := set $managed "metrics" (dict "bearer_token_file" "/var/run/secrets/trove/metrics/token") -}}
{{- end -}}
{{- $keys := $s.encryption.keys -}}
{{- if $keys -}}
{{- $files := list -}}
{{- range $keys -}}
{{- $files = append $files (printf "/var/run/secrets/trove/encryption/%s" .) -}}
{{- end -}}
{{- $_ := set $managed "secrets" (dict "encryption_key_files" $files "active_key_id" (first $keys | trimSuffix ".key")) -}}
{{- end -}}
{{- /*
TLS (R-HA-063): server.tls for the client listener and, when the ops listener must not carry the
client-certificate requirement or has its own Secret, an explicit server.ops_tls block. Without either,
server.ops_tls keeps its default (inherit: true). Probes and scrapers present no client certificate,
which is why a client CA on :8080 never extends to :8081.
*/ -}}
{{- if .Values.tls.enabled -}}
{{- $t := .Values.tls -}}
{{- $block := dict "min_version" $t.minVersion "reload_interval" $t.reloadInterval -}}
{{- $main := list (dict "cert_file" "/var/run/secrets/trove/tls/tls.crt" "key_file" "/var/run/secrets/trove/tls/tls.key") -}}
{{- $srv := deepCopy $block -}}
{{- $_ := set $srv "certificates" $main -}}
{{- if $t.clientCA.secretName -}}
{{- $_ := set $srv "client_ca_file" "/var/run/secrets/trove/tls-client-ca/ca.crt" -}}
{{- $_ := set $srv "client_auth" $t.clientCA.auth -}}
{{- end -}}
{{- $server := dict "tls" $srv -}}
{{- if $t.ops.secretName -}}
{{- $ops := deepCopy $block -}}
{{- $_ := set $ops "inherit" false -}}
{{- $_ := set $ops "certificates" (list (dict "cert_file" "/var/run/secrets/trove/tls-ops/tls.crt" "key_file" "/var/run/secrets/trove/tls-ops/tls.key")) -}}
{{- $_ := set $server "ops_tls" $ops -}}
{{- else if $t.clientCA.secretName -}}
{{- $ops := deepCopy $block -}}
{{- $_ := set $ops "inherit" false -}}
{{- $_ := set $ops "certificates" $main -}}
{{- $_ := set $server "ops_tls" $ops -}}
{{- end -}}
{{- $_ := set $managed "server" $server -}}
{{- end -}}
{{- $managed | mergeOverwrite (deepCopy .Values.config) | toYaml -}}
{{- end -}}
