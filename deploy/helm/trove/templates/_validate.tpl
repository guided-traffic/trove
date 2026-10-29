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
{{- if and (not (dig "storage" "s3" "bucket" "" $cfg)) (not (and $v.secrets.s3.existingSecret $v.secrets.s3.bucketKey)) -}}
{{- fail "trove: config.storage.s3.bucket is required (example: trove-blobs), or name the key of the bucket in the S3 Secret with secrets.s3.existingSecret and secrets.s3.bucketKey (example: S3_BUCKET)" -}}
{{- end -}}
{{- if and (or $v.secrets.s3.bucketKey $v.secrets.s3.regionKey $v.secrets.s3.endpointKey) (not $v.secrets.s3.existingSecret) -}}
{{- fail "trove: secrets.s3.bucketKey, secrets.s3.regionKey and secrets.s3.endpointKey read keys of the S3 Secret and need secrets.s3.existingSecret" -}}
{{- end -}}
{{- if and (eq (dig "storage" "s3" "credentials" "source" "" $cfg) "static") (not $v.secrets.s3.existingSecret) -}}
{{- fail "trove: config.storage.s3.credentials.source: static needs secrets.s3.existingSecret" -}}
{{- end -}}
{{- if or (not $v.secrets.encryption.existingSecret) (not $v.secrets.encryption.keys) -}}
{{- fail (printf "trove: secrets.encryption.existingSecret and secrets.encryption.keys are required. The chart never generates the key (a regenerated key would make every sealed value undecryptable). Create the Secret before the first install:\n\n  kubectl -n %s create secret generic trove-encryption-keys \\\n    --from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)\n\nthen set secrets.encryption.existingSecret=trove-encryption-keys and secrets.encryption.keys={2026-10.key}. The file name without .key is the key ID." $ns) -}}
{{- end -}}

{{- /* Login secrets (R-AUTH-004, R-AUTH-014). */ -}}
{{- $oidcSeen := dict -}}
{{- range $i, $o := $v.secrets.oidc -}}
{{- if not (regexMatch "^[a-z0-9][a-z0-9-]{0,31}$" ($o.providerId | default "")) -}}
{{- fail (printf "trove: secrets.oidc[%d].providerId %q must match ^[a-z0-9][a-z0-9-]{0,31}$ (the provider ID of config.auth.oidc.providers, example: corp)" $i ($o.providerId | default "")) -}}
{{- end -}}
{{- if hasKey $oidcSeen $o.providerId -}}
{{- fail (printf "trove: secrets.oidc has two entries for providerId %q" $o.providerId) -}}
{{- end -}}
{{- $_ := set $oidcSeen $o.providerId true -}}
{{- if not $o.existingSecret -}}
{{- fail (printf "trove: secrets.oidc[%d].existingSecret is required for providerId %q (example: trove-oidc-%s)" $i $o.providerId $o.providerId) -}}
{{- end -}}
{{- if not (hasKey (dig "auth" "oidc" "providers" (dict) $cfg) $o.providerId) -}}
{{- fail (printf "trove: secrets.oidc[%d].providerId %q has no provider of that ID in config.auth.oidc.providers" $i $o.providerId) -}}
{{- end -}}
{{- if dig "auth" "oidc" "providers" $o.providerId "client_secret_file" "" $cfg -}}
{{- fail (printf "trove: config.auth.oidc.providers.%s.client_secret_file is managed by the chart while secrets.oidc names the provider and must not be set" $o.providerId) -}}
{{- end -}}
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

{{- /* TLS (R-HA-063, Q-HA-32). */ -}}
{{- $t := $v.tls -}}
{{- if $t.enabled -}}
{{- if not $t.secretName -}}
{{- fail "trove: tls.secretName is required while tls.enabled is true (default); the chart never creates the Secret. Name an existing kubernetes.io/tls Secret with tls.crt and tls.key (a cert-manager Certificate's secretName, example: trove-tls), or opt out of TLS on trove's listeners with tls.enabled: false (plain HTTP inside the cluster)" -}}
{{- end -}}
{{- if $v.ingress.enabled -}}
{{- $bt := $v.ingress.backendTLS -}}
{{- if and $bt.verify (or (eq $bt.controller "haproxy") (eq $bt.controller "ingress-nginx")) (not $bt.caSecret) -}}
{{- fail (printf "trove: ingress.backendTLS.caSecret is required for controller %s while ingress.backendTLS.verify is true: name a Secret in the release namespace with the CA as ca.crt (a cert-manager Secret carries it; example: the value of tls.secretName), or set ingress.backendTLS.verify: false to accept an unverified connection" $bt.controller) -}}
{{- end -}}
{{- if and $bt.verify (eq $bt.controller "traefik") (not $bt.serversTransport) -}}
{{- fail "trove: ingress.backendTLS.serversTransport is required for controller traefik while ingress.backendTLS.verify is true (<namespace>-<name>@kubernetescrd of a ServersTransport that verifies trove), or set ingress.backendTLS.verify: false" -}}
{{- end -}}
{{- if and $t.clientCA.secretName (eq $bt.controller "haproxy") (not $bt.clientCertSecret) -}}
{{- fail "trove: tls.clientCA requires ingress.backendTLS.clientCertSecret with controller haproxy: trove demands a client certificate and HAProxy Ingress needs a Secret with tls.crt and tls.key to present" -}}
{{- end -}}
{{- end -}}
{{- if and $v.tls.backendTLSPolicy.enabled (not $t.backendTLSPolicy.caConfigMapName) -}}
{{- fail "trove: tls.backendTLSPolicy.caConfigMapName is required with tls.backendTLSPolicy.enabled (a ConfigMap with the CA as ca.crt)" -}}
{{- end -}}
{{- else -}}
{{- if or $t.clientCA.secretName $t.ops.secretName $t.backendTLSPolicy.enabled -}}
{{- fail "trove: tls.clientCA, tls.ops and tls.backendTLSPolicy need tls.enabled: true" -}}
{{- end -}}
{{- end -}}

{{- /* Keys the chart manages (R-HA-030): setting one inside config is refused. */ -}}
{{- $managedPaths := list "storage.spool.dir" "database.username_file" "database.password_file" "database.ssl_root_cert_file" "database.ssl_cert_file" "database.ssl_key_file" "valkey.username_file" "valkey.password_file" "valkey.sentinel.username_file" "valkey.sentinel.password_file" "valkey.tls.ca_file" "valkey.tls.cert_file" "valkey.tls.key_file" "storage.s3.ca_file" "storage.s3.credentials.access_key_id_file" "storage.s3.credentials.secret_access_key_file" "metrics.bearer_token_file" "auth.break_glass.password_file" "secrets.encryption_key_files" "secrets.active_key_id" "server.endpoints" -}}
{{- if $t.enabled -}}
{{- $managedPaths = concat $managedPaths (list "server.tls" "server.ops_tls") -}}
{{- end -}}
{{- range $path := $managedPaths -}}
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
{{- fail (printf "trove: config.%s is managed by the chart and must not be set (secret file paths come from the secrets values, TLS comes from the tls values, the spool directory is fixed, endpoints come from the endpoints values)" $path) -}}
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

{{- include "trove.validateEndpoints" . -}}
{{- end -}}

{{/*
Endpoints (R-EP-008, R-HA-029): every failure names the value. Argument: the root context.
*/}}
{{- define "trove.validateEndpoints" -}}
{{- $v := .Values -}}
{{- $fullname := include "trove.fullname" . -}}
{{- $ports := dict -}}
{{- $hostOwner := dict -}}
{{- $wild := dict -}}
{{- $baseHost := include "trove.baseHost" . -}}
{{- if $baseHost -}}
{{- $_ := set $hostOwner $baseHost "config.server.base_url" -}}
{{- end -}}
{{- range (dig "server" "additional_base_urls" (list) $v.config) -}}
{{- $_ := set $hostOwner ((urlParse .).hostname | lower) "config.server.additional_base_urls" -}}
{{- end -}}
{{- range $h := (dig "default" "hosts" (list) $v.endpoints) -}}
{{- $_ := set $hostOwner (lower $h) "endpoints.default.hosts" -}}
{{- end -}}
{{- $names := include "trove.endpointNames" . | fromYamlArray -}}
{{- range $name := $names -}}
{{- $raw := index $v.endpoints $name -}}
{{- $key := printf "endpoints.%s" $name -}}
{{- if not (regexMatch "^[a-z][a-z0-9-]{0,19}$" $name) -}}
{{- fail (printf "trove: %s: the endpoint name must match ^[a-z][a-z0-9-]{0,19}$" $key) -}}
{{- end -}}
{{- if gt (add (len $fullname) 4 (len $name)) 63 -}}
{{- fail (printf "trove: %s: the Service name %s-ep-%s has %d characters, at most 63 are allowed; shorten the endpoint name or set fullnameOverride" $key $fullname $name (add (len $fullname) 4 (len $name))) -}}
{{- end -}}
{{- $e := include "trove.endpoint" (dict "root" $ "name" $name) | fromYaml -}}
{{- if not (hasKey $raw "port") -}}
{{- fail (printf "trove: %s.port is required (example: 8443)" $key) -}}
{{- end -}}
{{- if or (eq (int $e.port) 8080) (eq (int $e.port) 8081) -}}
{{- fail (printf "trove: %s.port %d is taken by the default client listener (8080) or the operations listener (8081)" $key (int $e.port)) -}}
{{- end -}}
{{- if hasKey $ports (toString (int $e.port)) -}}
{{- fail (printf "trove: %s.port %d is already the port of endpoint %s" $key (int $e.port) (get $ports (toString (int $e.port)))) -}}
{{- end -}}
{{- $_ := set $ports (toString (int $e.port)) $name -}}
{{- if not $e.baseUrl -}}
{{- fail (printf "trove: %s.baseUrl is required (example: https://packages.example.com)" $key) -}}
{{- end -}}
{{- $u := urlParse $e.baseUrl -}}
{{- if or (not (has $u.scheme (list "http" "https"))) (not $u.host) (and $u.path (ne $u.path "/")) $u.query $u.fragment $u.userinfo -}}
{{- fail (printf "trove: %s.baseUrl %q must be an origin: a scheme (http or https), a host and an optional port, nothing else" $key $e.baseUrl) -}}
{{- end -}}
{{- /* Surface: ui belongs to the default endpoint (Q-EP-13). */ -}}
{{- if has "ui" $e.surface -}}
{{- fail (printf "trove: %s.surface lists ui; sign-in and the UI belong to the default endpoint (surface_not_supported)" $key) -}}
{{- end -}}
{{- /* Hosts: no host in two endpoints (R-EP-001). */ -}}
{{- range $h := include "trove.endpointHosts" $e | fromYamlArray -}}
{{- if hasKey $hostOwner $h -}}
{{- fail (printf "trove: %s: the host %s is also a host of %s" $key $h (get $hostOwner $h)) -}}
{{- end -}}
{{- $_ := set $hostOwner $h (printf "endpoints.%s" $name) -}}
{{- end -}}
{{- if $e.wildcardHost -}}
{{- if not (regexMatch "^\\*\\.[a-z0-9-]+(\\.[a-z0-9-]+)+$" $e.wildcardHost) -}}
{{- fail (printf "trove: %s.wildcardHost %q must be *.<domain> with at least two labels in the domain (example: *.packages.example.com)" $key $e.wildcardHost) -}}
{{- end -}}
{{- if hasKey $wild (lower $e.wildcardHost) -}}
{{- fail (printf "trove: %s.wildcardHost %q is also the wildcard host of endpoint %s" $key $e.wildcardHost (get $wild (lower $e.wildcardHost))) -}}
{{- end -}}
{{- $_ := set $wild (lower $e.wildcardHost) $name -}}
{{- end -}}
{{- if and (eq $e.wildcardRoot "label") (not $e.wildcardHost) -}}
{{- fail (printf "trove: %s.wildcardRoot is label, which needs %s.wildcardHost" $key $key) -}}
{{- end -}}
{{- /* TLS inputs need TLS on trove's listeners (R-HA-063). */ -}}
{{- if and (not $v.tls.enabled) (or $e.tlsSecret $e.tlsExtraSecrets $e.tlsClientCaSecret) -}}
{{- fail (printf "trove: %s.tls.existingSecret, extraSecrets and clientCaSecret need tls.enabled: true" $key) -}}
{{- end -}}
{{- if and $e.tlsClientCaSecret (not (or $e.tlsSecret $v.tls.secretName)) -}}
{{- fail (printf "trove: %s.tls.clientCaSecret needs a certificate: set %s.tls.existingSecret or tls.secretName" $key $key) -}}
{{- end -}}
{{- /* The endpoint's Ingress follows the rules of the default one: a merged backendTLS block that verifies needs its CA (R-HA-063). */ -}}
{{- if and $v.tls.enabled $e.ingressEnabled -}}
{{- $bt := include "trove.endpointBackendTLS" (dict "root" $ "name" $name) | fromYaml -}}
{{- if and $bt.verify (or (eq $bt.controller "haproxy") (eq $bt.controller "ingress-nginx")) (not $bt.caSecret) -}}
{{- fail (printf "trove: %s.ingress.backendTLS.caSecret is required for controller %s while verify is true (set it here or in ingress.backendTLS): name a Secret in the release namespace with the CA as ca.crt, or set %s.ingress.backendTLS.verify: false to accept an unverified connection" $key $bt.controller $key) -}}
{{- end -}}
{{- if and $bt.verify (eq $bt.controller "traefik") (not $bt.serversTransport) -}}
{{- fail (printf "trove: %s.ingress.backendTLS.serversTransport is required for controller traefik while verify is true (set it here or in ingress.backendTLS; <namespace>-<name>@kubernetescrd of a ServersTransport that verifies trove), or set %s.ingress.backendTLS.verify: false" $key $key) -}}
{{- end -}}
{{- if and $e.tlsClientCaSecret (eq $bt.controller "haproxy") (not $bt.clientCertSecret) -}}
{{- fail (printf "trove: %s.tls.clientCaSecret requires %s.ingress.backendTLS.clientCertSecret (or ingress.backendTLS.clientCertSecret) with controller haproxy: trove demands a client certificate on this endpoint and HAProxy Ingress needs a Secret with tls.crt and tls.key to present" $key $key) -}}
{{- end -}}
{{- end -}}
{{- /* A pool follows the rules of a web pool (R-HA-062). */ -}}
{{- if $e.poolEnabled -}}
{{- $r := include "trove.endpointPoolValues" (dict "root" $ "name" $name) | fromYaml -}}
{{- if and $v.highAvailability.enforce (lt (int $r.replicaCount) 2) -}}
{{- fail (printf "trove: highAvailability.enforce needs %s.pool.replicaCount of at least 2 (Deployment %s-web-ep-%s)" $key $fullname $name) -}}
{{- end -}}
{{- if and $v.highAvailability.enforce (not $r.podDisruptionBudget.enabled) -}}
{{- fail (printf "trove: highAvailability.enforce needs a disruption budget for the pool of %s (%s.pool.podDisruptionBudget.enabled)" $key $key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* An exact host of one endpoint that another endpoint's wildcard host covers. */ -}}
{{- range $wh, $owner := $wild -}}
{{- range $h, $hOwner := $hostOwner -}}
{{- if and (ne $hOwner (printf "endpoints.%s" $owner)) (regexMatch (printf "^[a-z0-9-]+%s$" (trimPrefix "*" $wh | replace "." "\\.")) $h) -}}
{{- fail (printf "trove: endpoints.%s.wildcardHost %q covers the host %s of %s" $owner $wh $h $hOwner) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
