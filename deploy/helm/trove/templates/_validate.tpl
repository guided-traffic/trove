{{/*
Fail-fast validation (R-HA-029). Every failure names the offending value. Rows of R-HA-029 that
belong to features this chart does not render yet (pooler, backup, restore, gateway, monitoring,
pools) are added with those features.
*/}}
{{- define "trove.validate" -}}
{{- $v := .Values -}}
{{- $cfg := $v.config -}}
{{- $ns := .Release.Namespace -}}

{{- if not (dig "server" "base_url" "" $cfg) -}}
{{- fail "trove: config.server.base_url is required (example: https://trove.example.com)" -}}
{{- end -}}
{{- if not (dig "database" "host" "" $cfg) -}}
{{- fail "trove: config.database.host is required (example: trove-db-rw.trove.svc)" -}}
{{- end -}}
{{- if not $v.secrets.database.existingSecret -}}
{{- fail "trove: secrets.database.existingSecret is required; the chart never creates a Secret (example: trove-db-app)" -}}
{{- end -}}
{{- if not (dig "storage" "s3" "bucket" "" $cfg) -}}
{{- fail "trove: config.storage.s3.bucket is required (example: trove-blobs)" -}}
{{- end -}}
{{- if and (eq (dig "storage" "s3" "credentials" "source" "" $cfg) "static") (not $v.secrets.s3.existingSecret) -}}
{{- fail "trove: config.storage.s3.credentials.source: static needs secrets.s3.existingSecret" -}}
{{- end -}}
{{- if or (not $v.secrets.encryption.existingSecret) (not $v.secrets.encryption.keys) -}}
{{- fail (printf "trove: secrets.encryption.existingSecret and secrets.encryption.keys are required. The chart never generates the key (a regenerated key would make every sealed value undecryptable). Create the Secret before the first install:\n\n  kubectl -n %s create secret generic trove-encryption-keys \\\n    --from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)\n\nthen set secrets.encryption.existingSecret=trove-encryption-keys and secrets.encryption.keys={2026-10.key}. The file name without .key is the key ID." $ns) -}}
{{- end -}}

{{- /* Valkey (R-HA-056). */ -}}
{{- if not $v.secrets.valkey.existingSecret -}}
{{- fail "trove: secrets.valkey.existingSecret is required (example: trove-valkey)" -}}
{{- end -}}
{{- $mode := dig "valkey" "mode" "sentinel" $cfg -}}
{{- if eq $mode "sentinel" -}}
{{- $addrs := dig "valkey" "sentinel" "addresses" (list) $cfg -}}
{{- if not $addrs -}}
{{- fail "trove: config.valkey.sentinel.addresses is required with config.valkey.mode: sentinel" -}}
{{- end -}}
{{- if not (dig "valkey" "sentinel" "primary_name" "" $cfg) -}}
{{- fail "trove: config.valkey.sentinel.primary_name is required with config.valkey.mode: sentinel (example: trove)" -}}
{{- end -}}
{{- if and $v.highAvailability.enforce (lt (len $addrs) 3) -}}
{{- fail "trove: highAvailability.enforce needs at least 3 entries in config.valkey.sentinel.addresses" -}}
{{- end -}}
{{- else if eq $mode "standalone" -}}
{{- if $v.highAvailability.enforce -}}
{{- fail "trove: highAvailability.enforce refuses config.valkey.mode: standalone; use config.valkey.mode: sentinel" -}}
{{- end -}}
{{- if not (dig "valkey" "address" "" $cfg) -}}
{{- fail "trove: config.valkey.address is required with config.valkey.mode: standalone" -}}
{{- end -}}
{{- else -}}
{{- fail (printf "trove: config.valkey.mode %q is not supported; use sentinel (or standalone for development)" $mode) -}}
{{- end -}}
{{- if and (eq (dig "valkey" "tls" "mode" "verify-full" $cfg) "disable") (not (dig "valkey" "allow_insecure" false $cfg)) -}}
{{- fail "trove: config.valkey.tls.mode: disable needs config.valkey.allow_insecure: true" -}}
{{- end -}}
{{- if and (eq (dig "metrics" "auth" "none" $cfg) "static") (not $v.secrets.metricsToken.existingSecret) -}}
{{- fail "trove: config.metrics.auth: static needs secrets.metricsToken.existingSecret" -}}
{{- end -}}

{{- /* Keys the chart manages (R-HA-030): setting one inside config is refused. */ -}}
{{- range $path := list "storage.spool.dir" "database.username_file" "database.password_file" "database.ssl_root_cert_file" "database.ssl_cert_file" "database.ssl_key_file" "valkey.username_file" "valkey.password_file" "valkey.sentinel.username_file" "valkey.sentinel.password_file" "valkey.tls.ca_file" "valkey.tls.cert_file" "valkey.tls.key_file" "storage.s3.credentials.access_key_id_file" "storage.s3.credentials.secret_access_key_file" "metrics.bearer_token_file" "secrets.encryption_key_files" "secrets.active_key_id" -}}
{{- $cur := $cfg -}}
{{- $found := true -}}
{{- range splitList "." $path -}}
{{- if and $found (kindIs "map" $cur) (hasKey $cur .) -}}
{{- $cur = index $cur . -}}
{{- else -}}
{{- $found = false -}}
{{- end -}}
{{- end -}}
{{- if $found -}}
{{- fail (printf "trove: config.%s is managed by the chart and must not be set (secret file paths come from the secrets values, the spool directory is fixed)" $path) -}}
{{- end -}}
{{- end -}}

{{- /* Roles (R-HA-002, R-HA-032, R-HA-011). */ -}}
{{- if lt (int $v.worker.replicaCount) 1 -}}
{{- fail "trove: worker.replicaCount must be at least 1; without a worker no scheduled work runs (R-HA-002)" -}}
{{- end -}}
{{- $drain := include "trove.seconds" (dig "health" "shutdown_drain_delay" "5s" $cfg) | int64 -}}
{{- range $role := list "web" "worker" -}}
{{- $r := index $v $role -}}
{{- if and $r.podDisruptionBudget.minAvailable $r.podDisruptionBudget.maxUnavailable -}}
{{- fail (printf "trove: %s.podDisruptionBudget.minAvailable and %s.podDisruptionBudget.maxUnavailable are mutually exclusive; set one and null the other" $role $role) -}}
{{- end -}}
{{- if lt (int64 $r.terminationGracePeriodSeconds) (add $drain 15) -}}
{{- fail (printf "trove: %s.terminationGracePeriodSeconds (%v) must be at least config.health.shutdown_drain_delay + 15 s (%d s)" $role $r.terminationGracePeriodSeconds (add $drain 15) ) -}}
{{- end -}}
{{- if $v.highAvailability.enforce -}}
{{- if lt (int $r.replicaCount) 2 -}}
{{- fail (printf "trove: highAvailability.enforce needs %s.replicaCount of at least 2 (Deployment %s-%s)" $role (include "trove.fullname" $) $role) -}}
{{- end -}}
{{- if not $r.podDisruptionBudget.enabled -}}
{{- fail (printf "trove: highAvailability.enforce needs %s.podDisruptionBudget.enabled: true (Deployment %s-%s)" $role (include "trove.fullname" $) $role) -}}
{{- end -}}
{{- $hostOk := false -}}
{{- if $r.topologySpreadConstraints -}}
{{- range $r.topologySpreadConstraints -}}
{{- if and (eq .topologyKey "kubernetes.io/hostname") (eq .whenUnsatisfiable "DoNotSchedule") (eq (.nodeTaintsPolicy | default "") "Honor") -}}
{{- $hostOk = true -}}
{{- end -}}
{{- end -}}
{{- else if and $r.topologySpread.enabled (eq $r.topologySpread.host.whenUnsatisfiable "DoNotSchedule") (eq $r.topologySpread.nodeTaintsPolicy "Honor") -}}
{{- $hostOk = true -}}
{{- end -}}
{{- if not $hostOk -}}
{{- fail (printf "trove: highAvailability.enforce needs a host spread constraint with whenUnsatisfiable DoNotSchedule and nodeTaintsPolicy Honor for Deployment %s-%s (%s.topologySpread.host.whenUnsatisfiable, %s.topologySpread.nodeTaintsPolicy)" (include "trove.fullname" $) $role $role $role) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
Spool budget (R-HA-035): the largest upload must fit the web spool budget (R-HA-029). A worker
accepts no uploads, so its spool is not checked against storage.upload.max_size, and the binary
applies that rule to the roles web and all only.
*/ -}}
{{- $upload := include "trove.bytes" (dig "storage" "upload" "max_size" "10GiB" $cfg) | int64 -}}
{{- $budget := include "trove.spoolBudget" (dict "root" $ "role" "web") | int64 -}}
{{- if gt $upload $budget -}}
{{- fail (printf "trove: config.storage.upload.max_size (%v) is larger than the web spool budget of %d bytes (spool size minus spool.headroom); raise spool.sizeLimit or web.spool.sizeLimit, or lower config.storage.upload.max_size" (dig "storage" "upload" "max_size" "10GiB" $cfg) $budget) -}}
{{- end -}}
{{- end -}}
