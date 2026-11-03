{{/*
Fail-fast validation (R-BAS-045). Every failure names the offending value. The keys of config that the
binary's role schema does not know are refused by values.schema.json, whose config property is the
generated schema of the role bastion (docs/developer/helm-chart.md, Bastion chart).
*/}}
{{- define "trove-bastion.validate" -}}
{{- $v := .Values -}}
{{- $cfg := $v.config -}}
{{- $fullname := include "trove-bastion.fullname" . -}}
{{- $backend := dig "bastion" "cache" "backend" "volume" $cfg -}}
{{- $valkey := dig "valkey" "mode" "none" $cfg -}}

{{- /* The URLs (R-BAS-044): both https. */ -}}
{{- $base := dig "server" "base_url" "" $cfg -}}
{{- if not $base -}}
{{- fail "trove-bastion: config.server.base_url is required (example: https://packages.example.com)" -}}
{{- end -}}
{{- if not (regexMatch "^https://[^/?#]+/?$" $base) -}}
{{- fail (printf "trove-bastion: config.server.base_url %q must be an https origin: https, a host and an optional port, nothing else" $base) -}}
{{- end -}}
{{- $main := dig "bastion" "main" "url" "" $cfg -}}
{{- if not $main -}}
{{- fail "trove-bastion: config.bastion.main.url is required (example: https://trove.example.com, the main endpoint of the main installation)" -}}
{{- end -}}
{{- if not (hasPrefix "https://" $main) -}}
{{- fail (printf "trove-bastion: config.bastion.main.url %q must be an https URL; the Bastion refuses to start with http (R-BAS-044)" $main) -}}
{{- end -}}
{{- if not $v.secrets.mainToken.existingSecret -}}
{{- fail "trove-bastion: secrets.mainToken.existingSecret is required; the chart never creates a Secret (example: trove-bastion-token, the trv_bst_ token of the registration, R-BAS-006)" -}}
{{- end -}}

{{- /* Cache backend (R-BAS-030). */ -}}
{{- if not (has $backend (list "volume" "bucket")) -}}
{{- fail (printf "trove-bastion: config.bastion.cache.backend %q is not supported; use volume or bucket" $backend) -}}
{{- end -}}
{{- if eq $backend "bucket" -}}
{{- if not (dig "bastion" "cache" "bucket" "bucket" "" $cfg) -}}
{{- fail "trove-bastion: config.bastion.cache.bucket.bucket is required with config.bastion.cache.backend: bucket (example: trove-bastion-partners)" -}}
{{- end -}}
{{- $src := dig "bastion" "cache" "bucket" "credentials" "source" "static" $cfg -}}
{{- if not (has $src (list "static" "webIdentity" "default")) -}}
{{- fail (printf "trove-bastion: config.bastion.cache.bucket.credentials.source %q is not supported; use static, webIdentity or default" $src) -}}
{{- end -}}
{{- if and (eq $src "static") (not $v.secrets.cacheBucket.existingSecret) -}}
{{- fail "trove-bastion: config.bastion.cache.bucket.credentials.source: static needs secrets.cacheBucket.existingSecret (example: trove-bastion-s3)" -}}
{{- end -}}
{{- end -}}
{{- if not (has $v.cacheVolume.type (list "ephemeral" "persistent" "emptyDir")) -}}
{{- fail (printf "trove-bastion: cacheVolume.type %q is not supported; use ephemeral, persistent or emptyDir" $v.cacheVolume.type) -}}
{{- end -}}
{{- if and (eq $backend "bucket") (eq $v.cacheVolume.type "persistent") -}}
{{- fail "trove-bastion: cacheVolume.type: persistent needs config.bastion.cache.backend: volume; the bucket backend keeps nothing on a volume" -}}
{{- end -}}

{{- /* Valkey (R-BAS-034). */ -}}
{{- if not (has $valkey (list "none" "sentinel" "standalone")) -}}
{{- fail (printf "trove-bastion: config.valkey.mode %q is not supported; use none, sentinel or standalone (development only)" $valkey) -}}
{{- end -}}
{{- if eq $valkey "sentinel" -}}
{{- $addrs := dig "valkey" "sentinel" "addresses" (list) $cfg -}}
{{- if not $addrs -}}
{{- fail "trove-bastion: config.valkey.sentinel.addresses is required with config.valkey.mode: sentinel" -}}
{{- end -}}
{{- if not (dig "valkey" "sentinel" "primary_name" "" $cfg) -}}
{{- fail "trove-bastion: config.valkey.sentinel.primary_name is required with config.valkey.mode: sentinel (example: trove-bastion)" -}}
{{- end -}}
{{- if and $v.highAvailability.enforce (lt (len $addrs) 3) -}}
{{- fail "trove-bastion: highAvailability.enforce needs at least 3 entries in config.valkey.sentinel.addresses" -}}
{{- end -}}
{{- else if eq $valkey "standalone" -}}
{{- if $v.highAvailability.enforce -}}
{{- fail "trove-bastion: highAvailability.enforce refuses config.valkey.mode: standalone; use config.valkey.mode: sentinel" -}}
{{- end -}}
{{- if not (dig "valkey" "address" "" $cfg) -}}
{{- fail "trove-bastion: config.valkey.address is required with config.valkey.mode: standalone" -}}
{{- end -}}
{{- end -}}
{{- if and (or (eq $backend "bucket") (ne $valkey "none")) (not $v.secrets.signingKey.existingSecret) -}}
{{- fail "trove-bastion: secrets.signingKey.existingSecret is required with config.bastion.cache.backend: bucket or a Valkey (config.valkey.mode other than none); the records in them are signed (R-BAS-032). Example: trove-bastion-signing, 32 random bytes" -}}
{{- end -}}

{{- /* The trust bundle (R-BAS-044): one file, because bastion.main.ca_file is one file. */ -}}
{{- if and $v.trustedCA.existingSecret $v.trustedCA.existingConfigMap -}}
{{- fail "trove-bastion: trustedCA.existingSecret and trustedCA.existingConfigMap are mutually exclusive; set one" -}}
{{- end -}}
{{- if and (or $v.trustedCA.existingSecret $v.trustedCA.existingConfigMap) (ne (len $v.trustedCA.keys) 1) -}}
{{- fail "trove-bastion: trustedCA.keys must name exactly one key: the bundle is one file (config.bastion.main.ca_file); put every CA into one bundle" -}}
{{- end -}}

{{- /* TLS of the listener (R-BAS-044). */ -}}
{{- if $v.tls.enabled -}}
{{- if and (not $v.tls.existingSecret) (not $v.tls.certManager.enabled) -}}
{{- fail "trove-bastion: tls.enabled is true (default) and no certificate source is set: set tls.existingSecret (a kubernetes.io/tls Secret) or tls.certManager.enabled: true, or opt out of TLS on the listeners with tls.enabled: false (plain HTTP inside the cluster)" -}}
{{- end -}}
{{- if and (not $v.tls.existingSecret) $v.tls.certManager.enabled (not $v.tls.certManager.issuerRef.name) -}}
{{- fail "trove-bastion: tls.certManager.issuerRef.name is required with tls.certManager.enabled while tls.existingSecret is empty (example: internal-ca), or name the listener's Secret with tls.existingSecret" -}}
{{- end -}}
{{- if and $v.metrics.serviceMonitor.enabled $v.metrics.serviceMonitor.namespace (ne $v.metrics.serviceMonitor.namespace .Release.Namespace) -}}
{{- fail (printf "trove-bastion: metrics.serviceMonitor.namespace %q differs from the release namespace %q while tls.enabled is true; the ServiceMonitor reads the CA from a Secret in its own namespace. Leave it empty, or set tls.enabled: false" $v.metrics.serviceMonitor.namespace .Release.Namespace) -}}
{{- end -}}
{{- end -}}

{{- /* Ingress annotations (R-BAS-033, R-BAS-038): the chart's own keys cannot be overridden. */ -}}
{{- if $v.ingress.enabled -}}
{{- $reserved := list -}}
{{- if $v.edgeLimit.enabled -}}
{{- $reserved = concat $reserved (list "limit-rps" "limit-connections" "limit-whitelist") -}}
{{- end -}}
{{- if $v.cacheVolume.routeByPath -}}
{{- $reserved = concat $reserved (list "balance-algorithm" "assign-backend-server-id" "config-backend") -}}
{{- end -}}
{{- if $v.tls.enabled -}}
{{- $reserved = concat $reserved (list "secure-backends" "secure-sni" "secure-verify-ca-secret") -}}
{{- end -}}
{{- range $k := $reserved -}}
{{- if hasKey $v.ingress.annotations (printf "haproxy-ingress.github.io/%s" $k) -}}
{{- fail (printf "trove-bastion: ingress.annotations sets haproxy-ingress.github.io/%s, which the chart renders itself (edgeLimit, cacheVolume.routeByPath, ingress.backendTLS, tls.enabled); change those values instead" $k) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /* Routing by path (R-BAS-033). */ -}}
{{- if $v.cacheVolume.routeByPath -}}
{{- if ne $backend "volume" -}}
{{- fail "trove-bastion: cacheVolume.routeByPath needs config.bastion.cache.backend: volume; with the bucket backend every replica reads the same objects" -}}
{{- end -}}
{{- if not $v.ingress.enabled -}}
{{- fail "trove-bastion: cacheVolume.routeByPath needs ingress.enabled: true; the annotations that pin a path to a replica are rendered on the Ingress" -}}
{{- end -}}
{{- end -}}

{{- /* The edge limit is wider than the Bastion's own (R-BAS-038). */ -}}
{{- if $v.edgeLimit.enabled -}}
{{- $own := dig "bastion" "limits" "authenticated" "requests_per_second" 50 $cfg -}}
{{- if le (float64 $v.edgeLimit.requestsPerSecond) (float64 $own) -}}
{{- fail (printf "trove-bastion: edgeLimit.requestsPerSecond (%v) must be above config.bastion.limits.authenticated.requests_per_second (%v): the Bastion's own stage, which knows credentials and misses, decides first and the ingress only cuts what would overload the Bastion itself" $v.edgeLimit.requestsPerSecond $own) -}}
{{- end -}}
{{- if lt (int $v.edgeLimit.connections) 1 -}}
{{- fail "trove-bastion: edgeLimit.connections must be at least 1" -}}
{{- end -}}
{{- end -}}

{{- /* Replicas, disruption budget and spreading (R-BAS-043). */ -}}
{{- if lt (int $v.replicaCount) 1 -}}
{{- fail "trove-bastion: replicaCount must be at least 1" -}}
{{- end -}}
{{- if and $v.podDisruptionBudget.minAvailable $v.podDisruptionBudget.maxUnavailable -}}
{{- fail "trove-bastion: podDisruptionBudget.minAvailable and podDisruptionBudget.maxUnavailable are mutually exclusive; set one and null the other" -}}
{{- end -}}
{{- if $v.autoscaling.enabled -}}
{{- if lt (int $v.autoscaling.maxReplicas) (int $v.autoscaling.minReplicas) -}}
{{- fail "trove-bastion: autoscaling.maxReplicas must not be below autoscaling.minReplicas" -}}
{{- end -}}
{{- if and (not $v.autoscaling.targetCPUUtilizationPercentage) (not $v.autoscaling.targetMemoryUtilizationPercentage) (not $v.autoscaling.metrics) -}}
{{- fail "trove-bastion: autoscaling.enabled needs a metric: autoscaling.targetCPUUtilizationPercentage, autoscaling.targetMemoryUtilizationPercentage or autoscaling.metrics" -}}
{{- end -}}
{{- end -}}
{{- if $v.highAvailability.enforce -}}
{{- $min := ternary $v.autoscaling.minReplicas $v.replicaCount $v.autoscaling.enabled -}}
{{- if lt (int $min) 2 -}}
{{- fail (printf "trove-bastion: highAvailability.enforce needs %s of at least 2 (workload %s)" (ternary "autoscaling.minReplicas" "replicaCount" $v.autoscaling.enabled) $fullname) -}}
{{- end -}}
{{- if not $v.podDisruptionBudget.enabled -}}
{{- fail (printf "trove-bastion: highAvailability.enforce needs podDisruptionBudget.enabled: true (workload %s)" $fullname) -}}
{{- end -}}
{{- $hostOk := false -}}
{{- if $v.topologySpreadConstraints -}}
{{- range $v.topologySpreadConstraints -}}
{{- if and (eq .topologyKey "kubernetes.io/hostname") (eq .whenUnsatisfiable "DoNotSchedule") (eq (.nodeTaintsPolicy | default "") "Honor") -}}
{{- $hostOk = true -}}
{{- end -}}
{{- end -}}
{{- else if and $v.topologySpread.enabled (eq $v.topologySpread.host.whenUnsatisfiable "DoNotSchedule") (eq $v.topologySpread.nodeTaintsPolicy "Honor") -}}
{{- $hostOk = true -}}
{{- end -}}
{{- if not $hostOk -}}
{{- fail (printf "trove-bastion: highAvailability.enforce needs a host spread constraint with whenUnsatisfiable DoNotSchedule and nodeTaintsPolicy Honor (topologySpread.host.whenUnsatisfiable, topologySpread.nodeTaintsPolicy) for workload %s" $fullname) -}}
{{- end -}}
{{- end -}}

{{- /* Network policy (R-BAS-047): an empty "to" or "from" would mean anywhere. */ -}}
{{- if $v.networkPolicy.enabled -}}
{{- if not $v.networkPolicy.ingress.https.from -}}
{{- fail "trove-bastion: networkPolicy.ingress.https.from must not be empty: the Bastion is reached through its Ingress, never directly; name the ingress controller's namespace (an ipBlock of 0.0.0.0/0 is the explicit way to admit everyone)" -}}
{{- end -}}
{{- if $v.networkPolicy.egress.enabled -}}
{{- if not $v.networkPolicy.egress.dns.to -}}
{{- fail "trove-bastion: networkPolicy.egress.dns.to is required with networkPolicy.egress.enabled" -}}
{{- end -}}
{{- if not $v.networkPolicy.egress.main.to -}}
{{- fail "trove-bastion: networkPolicy.egress.main.to is required with networkPolicy.egress.enabled: the address of the main installation (example: [{ipBlock: {cidr: 198.51.100.10/32}}])" -}}
{{- end -}}
{{- if and (ne $valkey "none") (not $v.networkPolicy.egress.valkey.to) -}}
{{- fail "trove-bastion: networkPolicy.egress.valkey.to is required while config.valkey.mode is not none (example: [{podSelector: {matchLabels: {app.kubernetes.io/name: trove-bastion-valkey}}}])" -}}
{{- end -}}
{{- if and (eq $backend "bucket") (not $v.networkPolicy.egress.cacheBucket.to) -}}
{{- fail "trove-bastion: networkPolicy.egress.cacheBucket.to is required with config.bastion.cache.backend: bucket (example: [{ipBlock: {cidr: 0.0.0.0/0}}])" -}}
{{- end -}}
{{- if and (dig "tracing" "enabled" false $cfg) (not $v.networkPolicy.egress.tracing.to) -}}
{{- fail "trove-bastion: networkPolicy.egress.tracing.to is required while config.tracing.enabled is true" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /* The PrometheusRule (R-BAS-041): a name in disabledAlerts that is no alert is a typo. */ -}}
{{- $names := include "trove-bastion.rules.names" . | fromJsonArray -}}
{{- range $v.metrics.prometheusRule.disabledAlerts -}}
{{- if not (has . $names) -}}
{{- fail (printf "trove-bastion: metrics.prometheusRule.disabledAlerts names %q, which is no alert of the chart (%s)" . (join ", " $names)) -}}
{{- end -}}
{{- end -}}

{{- /* Keys the chart manages (R-BAS-002, R-BAS-046): setting one inside config is refused. The list is chart-managed-keys.yaml, generated from the x-trove-chart-managed keys of the role schema (go generate ./internal/config). */ -}}
{{- $managedPaths := .Files.Get "chart-managed-keys.yaml" | fromYamlArray -}}
{{- if not $managedPaths -}}
{{- fail "trove-bastion: chart-managed-keys.yaml is missing or empty in the chart; regenerate it with make generate" -}}
{{- end -}}
{{- range $path := $managedPaths -}}
{{- if or $v.tls.enabled (not (hasPrefix "server." $path)) -}}
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
{{- /* A managed subtree (server.tls) is refused at its first key, so the message names the key that was set. */ -}}
{{- $named := $path -}}
{{- range until 8 -}}
{{- if and (kindIs "map" $cur) $cur -}}
{{- $first := $cur | keys | sortAlpha | first -}}
{{- $named = printf "%s.%s" $named $first -}}
{{- $cur = index $cur $first -}}
{{- end -}}
{{- end -}}
{{- fail (printf "trove-bastion: config.%s is managed by the chart and must not be set (secret file paths come from the secrets values, TLS from the tls values, the replica count from replicaCount or autoscaling.maxReplicas, the cache directory is fixed)" $named) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
