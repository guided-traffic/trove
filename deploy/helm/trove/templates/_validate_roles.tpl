{{/*
Fail-fast validation of the roles, the pools and the upgrade bridge (R-HA-002, R-HA-029, R-HA-032,
R-HA-035, R-HA-036, R-HA-062, R-HA-064). Every failure names the offending value. Argument: the
root context.
*/}}
{{- define "trove.validateRoles" -}}
{{- $v := .Values -}}
{{- $cfg := $v.config -}}
{{- $fullname := include "trove.fullname" . -}}
{{- include "trove.validatePools" . -}}

{{- $drain := include "trove.seconds" (dig "health" "shutdown_drain_delay" "5s" $cfg) | int64 -}}
{{- $upload := include "trove.bytes" (dig "storage" "upload" "max_size" "10GiB" $cfg) | int64 -}}
{{- /* The UI's content route admits up to ui.deploy.max_upload_bytes, or storage.upload.max_size when that is 0 (Q-UI-106). */ -}}
{{- $uiBound := dig "ui" "deploy" "max_upload_bytes" 104857600 $cfg -}}
{{- $uiUpload := ternary $upload (include "trove.bytes" $uiBound | int64) (eq (include "trove.bytes" $uiBound) "0") -}}
{{- $vuln := dig "vulnerabilities" (dict) $cfg -}}
{{- $osv := dig "sources" "osv" (dict) $vuln -}}
{{- $osvOn := and (dig "enabled" true $vuln) (ne (dig "mode" "online" $osv) "off") -}}
{{- $ratio := dig "runtime" "memory_limit_ratio" 0.85 $cfg | float64 -}}
{{- $reserved := add (include "trove.bytes" (dig "cache" "rendition_bytes" "256MiB" $cfg) | int64) (include "trove.bytes" (dig "proxy" "metadata_cache_bytes" "128MiB" $cfg) | int64) (include "trove.bytes" (dig "proxy" "merged_metadata_cache_bytes" "256MiB" $cfg) | int64) (include "trove.bytes" (dig "proxy" "query_cache_bytes" "8MiB" $cfg) | int64) -}}
{{- $perRequest := include "trove.requestBytes" . | int64 -}}

{{- range $u := include "trove.units" . | fromYamlArray -}}
{{- $r := $u.r -}}
{{- $dep := printf "%s-%s" $fullname $u.name -}}
{{- $min := include "trove.unitMinReplicas" $r | int -}}
{{- $minKey := ternary (printf "%s.autoscaling.minReplicas" $u.key) (printf "%s.replicaCount" $u.key) (dig "autoscaling" "enabled" false $r) -}}
{{- if and (eq $u.kind "main") (lt $min 1) -}}
{{- if eq $u.role "worker" -}}
{{- fail (printf "trove: %s must be at least 1; without a worker no scheduled work runs (R-HA-002)" $minKey) -}}
{{- end -}}
{{- fail (printf "trove: %s must be at least 1; without a replica the edge has no backend for the families of the role %s (R-HA-002)" $minKey $u.role) -}}
{{- end -}}
{{- if and (eq $u.kind "pool") (eq $u.role "worker") (lt $min 1) -}}
{{- fail (printf "trove: %s must be at least 1; with no replica the queues %s of the pool are claimed by nobody, because the main worker does not claim them (R-HA-062)" $minKey (join ", " $u.queues)) -}}
{{- end -}}
{{- if and $r.podDisruptionBudget.minAvailable $r.podDisruptionBudget.maxUnavailable -}}
{{- fail (printf "trove: %s.podDisruptionBudget.minAvailable and %s.podDisruptionBudget.maxUnavailable are mutually exclusive; set one and null the other" $u.key $u.key) -}}
{{- end -}}
{{- if lt (int64 $r.terminationGracePeriodSeconds) (add $drain 15) -}}
{{- fail (printf "trove: %s.terminationGracePeriodSeconds (%v) must be at least config.health.shutdown_drain_delay + 15 s (%d s)" $u.key $r.terminationGracePeriodSeconds (add $drain 15)) -}}
{{- end -}}
{{- with $r.autoscaling -}}
{{- if .enabled -}}
{{- if lt (int .maxReplicas) (int .minReplicas) -}}
{{- fail (printf "trove: %s.autoscaling.maxReplicas (%v) must not be below %s.autoscaling.minReplicas (%v)" $u.key .maxReplicas $u.key .minReplicas) -}}
{{- end -}}
{{- if not (or .targetCPUUtilizationPercentage .targetMemoryUtilizationPercentage .metrics) -}}
{{- fail (printf "trove: %s.autoscaling is enabled without a metric: set targetCPUUtilizationPercentage, targetMemoryUtilizationPercentage or metrics" $u.key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $v.highAvailability.enforce -}}
{{- if lt $min 2 -}}
{{- fail (printf "trove: highAvailability.enforce needs %s of at least 2 (Deployment %s)" $minKey $dep) -}}
{{- end -}}
{{- if not $r.podDisruptionBudget.enabled -}}
{{- fail (printf "trove: highAvailability.enforce needs %s.podDisruptionBudget.enabled: true (Deployment %s)" $u.key $dep) -}}
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
{{- fail (printf "trove: highAvailability.enforce needs a host spread constraint with whenUnsatisfiable DoNotSchedule and nodeTaintsPolicy Honor for Deployment %s (%s.topologySpread.host.whenUnsatisfiable, %s.topologySpread.nodeTaintsPolicy)" $dep $u.key $u.key) -}}
{{- end -}}
{{- end -}}

{{- /* Spool budgets (R-HA-035): a packages (or web) pod spools the largest upload, an api pod the UI's, and a worker that claims the queue vuln one advisory archive. */ -}}
{{- $budget := include "trove.spoolBudget" (dict "root" $ "role" $u.role "r" $r "key" $u.key) | int64 -}}
{{- if and (has $u.role (list "packages" "web")) (gt $upload $budget) -}}
{{- fail (printf "trove: config.storage.upload.max_size (%v) is larger than the spool budget of %d bytes of Deployment %s (spool size minus spool.headroom); raise spool.sizeLimit or %s.spool.sizeLimit, or lower config.storage.upload.max_size" (dig "storage" "upload" "max_size" "10GiB" $cfg) $budget $dep $u.key) -}}
{{- end -}}
{{- if and (eq $u.role "api") (gt $uiUpload $budget) -}}
{{- fail (printf "trove: the upload limit of the UI's content route (config.ui.deploy.max_upload_bytes, or config.storage.upload.max_size when it is 0: %d bytes) is larger than the spool budget of %d bytes of Deployment %s (spool size minus spool.headroom); raise spool.sizeLimit or %s.spool.sizeLimit, or lower config.ui.deploy.max_upload_bytes" $uiUpload $budget $dep $u.key) -}}
{{- end -}}
{{- if eq $u.role "worker" -}}
{{- $claimsVuln := false -}}
{{- if $u.queues -}}
{{- $claimsVuln = has "vuln" $u.queues -}}
{{- else -}}
{{- $claimsVuln = not (has "vuln" ($u.excludeQueues | default list)) -}}
{{- end -}}
{{- if and $claimsVuln $osvOn -}}
{{- $archive := include "trove.bytes" (dig "max_archive_size" "1GiB" $osv) | int64 -}}
{{- if gt $archive $budget -}}
{{- fail (printf "trove: config.vulnerabilities.sources.osv.max_archive_size (%v) is larger than the spool budget of %d bytes of Deployment %s, which claims the queue vuln (spool size minus spool.headroom); raise worker.spool.sizeLimit or spool.sizeLimit, lower the archive limit, or set config.vulnerabilities.sources.osv.mode: off (R-VULN-025)" (dig "max_archive_size" "1GiB" $osv) $budget $dep) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
Memory (R-HA-036, Q-HA-70, Q-ARCH-243): a serving pod sizes what it admits from its memory limit, (0.8 ×
runtime.memory_limit_ratio × limits.memory − the caches' byte budgets) / the bytes of one request
(R-ARCH-005 step 4). A pod without a limit would size it from the node's memory; one that holds fewer
than 64 requests cannot serve a handful of clients at once.
*/ -}}
{{- if ne $u.role "worker" -}}
{{- $mem := dig "limits" "memory" "" ($r.resources | default dict) -}}
{{- if not $mem -}}
{{- fail (printf "trove: %s.resources.limits.memory is required: a serving replica sizes what it admits from its memory limit, and without one Deployment %s would read the node's memory (R-HA-036)" $u.key $dep) -}}
{{- end -}}
{{- $slots := floor (divf (subf (mulf 0.8 $ratio (include "trove.quantityBytes" $mem | float64)) $reserved) $perRequest) | int64 -}}
{{- if lt $slots 64 -}}
{{- fail (printf "trove: %s.resources.limits.memory (%v) admits %d concurrent requests, fewer than 64: (0.8 × config.runtime.memory_limit_ratio (%v) × the limit − %d bytes of cache budgets) / %d bytes per request (config.cache.rendition_bytes, config.proxy.metadata_cache_bytes, merged_metadata_cache_bytes and query_cache_bytes; config.storage.download.buffer_size and hold_back, config.storage.s3.http.receive_window; R-ARCH-005); raise the limit or lower those values" $u.key $mem $slots $ratio $reserved $perRequest) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
The bridge guard (R-HA-064, Q-HA-73, Q-HA-90): with web.enabled false, a release whose namespace still holds
<fullname>-web must see every serving Deployment of the target shape fully ready before the edge switches
to them: the main api and packages Deployments, the path pools and the endpoint pools. lookup reads the
cluster under helm upgrade and helm install --dry-run=server; under helm template it returns nothing and
the guard is inert. It validates only and never changes what renders. The guard runs only while web.enabled
is false; a release that stays at step 1 keeps the web pods (H-518), which the alert TroveBridgeLeftOn reports.
*/ -}}
{{- if not $v.web.enabled -}}
{{- $web := lookup "apps/v1" "Deployment" .Release.Namespace (printf "%s-web" $fullname) -}}
{{- if $web -}}
{{- range $u := include "trove.units" . | fromYamlArray -}}
{{- if ne $u.role "worker" -}}
{{- $d := lookup "apps/v1" "Deployment" $.Release.Namespace (printf "%s-%s" $fullname $u.name) -}}
{{- if or (not $d) (lt (int (dig "status" "readyReplicas" 0 $d)) (int (dig "spec" "replicas" 0 $d))) -}}
{{- fail (printf "trove: web.enabled is false while the namespace holds the Deployment %s-web of the releases before the cut and %s-%s is missing or not fully ready. The cut takes two upgrades: first helm upgrade with --set web.enabled=true and wait until every Deployment of the target shape (api, packages, their path pools and endpoint pools) is ready, then helm upgrade with web.enabled: false (R-HA-064)" $fullname $fullname $u.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The pools of api, packages and worker (R-HA-062): names, path prefixes inside the families of the
role, queues. Runs before the Deployments are listed. Argument: the root context.
*/}}
{{- define "trove.validatePools" -}}
{{- $v := .Values -}}
{{- $fullname := include "trove.fullname" . -}}
{{- $queues := list "default" "maintenance" "index" "delivery" "mirror" "vuln" -}}
{{- $seenPath := dict -}}
{{- $seenQueue := dict -}}
{{- range $role := list "api" "packages" "worker" -}}
{{- $seenName := dict -}}
{{- range $p := (index $v $role).pools -}}
{{- $key := printf "%s.pools[%s]" $role $p.name -}}
{{- if hasPrefix "ep-" $p.name -}}
{{- fail (printf "trove: %s.name must not start with ep-, which names the pool of an endpoint (R-HA-062)" $key) -}}
{{- end -}}
{{- if hasKey $seenName $p.name -}}
{{- fail (printf "trove: %s.pools has two pools named %s (R-HA-062)" $role $p.name) -}}
{{- end -}}
{{- $_ := set $seenName $p.name true -}}
{{- if gt (len (printf "%s-%s-%s" $fullname $role $p.name)) 63 -}}
{{- fail (printf "trove: %s.name: the Deployment name %s-%s-%s has more than 63 characters; shorten the pool name or set fullnameOverride" $key $fullname $role $p.name) -}}
{{- end -}}
{{- if eq $role "worker" -}}
{{- range $q := $p.queues -}}
{{- if not (has $q $queues) -}}
{{- fail (printf "trove: %s.queues names %q, which is no queue (%s; architecture.md R-ARCH-016)" $key $q (join ", " $queues)) -}}
{{- end -}}
{{- if hasKey $seenQueue $q -}}
{{- fail (printf "trove: %s.queues: the queue %s is already claimed by the pool %s; a queue belongs to one pool (R-HA-062)" $key $q (get $seenQueue $q)) -}}
{{- end -}}
{{- $_ := set $seenQueue $q $p.name -}}
{{- end -}}
{{- else -}}
{{- range $path := $p.paths -}}
{{- $inside := false -}}
{{- if eq $role "api" -}}
{{- $inside = or (hasPrefix "/ui/" $path) (hasPrefix "/auth/" $path) (and (hasPrefix "/api/v1/" $path) (gt (len $path) 8)) (eq $path "/ui") (eq $path "/auth") -}}
{{- else -}}
{{- $inside = or (and (hasPrefix "/api/" $path) (ne $path "/api/v1") (not (hasPrefix "/api/v1/" $path))) (hasPrefix "/files/" $path) (hasPrefix "/v2/" $path) (hasPrefix "/.well-known/" $path) -}}
{{- end -}}
{{- if or (not $inside) (hasSuffix "/" $path) (has $path (list "/" "/api/v1" "/api" "/files" "/v2" "/.well-known")) -}}
{{- fail (printf "trove: %s.paths: %q is not a path prefix inside the families of the role %s that differs from the role's own prefixes (R-HA-037, R-HA-062): api pools take /ui, /auth or a prefix below /api/v1; packages pools a prefix below /api (not /api/v1), /files, /v2 or /.well-known" $key $path $role) -}}
{{- end -}}
{{- if hasKey $seenPath $path -}}
{{- fail (printf "trove: %s.paths: the prefix %s is already routed to the pool %s (R-HA-062)" $key $path (get $seenPath $path)) -}}
{{- end -}}
{{- $_ := set $seenPath $path (printf "%s.pools[%s]" $role $p.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* The main worker excludes the queues of the pools and must keep one (internal/config/jobs.go refuses to start otherwise). */ -}}
{{- if and $seenQueue (eq (len $seenQueue) (len $queues)) -}}
{{- fail (printf "trove: worker.pools claim every queue (%s), so the main worker's exclude list leaves it none and trove refuses to start; leave at least one queue to the main worker (R-HA-062)" (join ", " $queues)) -}}
{{- end -}}
{{- /* A role whose Deployments serve no endpoint: the default endpoint's surface holds no family of it and no further endpoint is served by it (R-EP-008, Q-EP-70). */ -}}
{{- $dsurface := dig "default" "surface" (list) $v.endpoints -}}
{{- if $dsurface -}}
{{- $served := dict -}}
{{- range $name := include "trove.endpointNames" . | fromYamlArray -}}
{{- range $sr := include "trove.surfaceRoles" (dict "root" $ "name" $name) | fromYamlArray -}}
{{- $_ := set $served $sr true -}}
{{- end -}}
{{- end -}}
{{- $families := dict "api" (list "api" "ci_exchange" "ui") "packages" (list "packages" "files") -}}
{{- range $role := list "api" "packages" -}}
{{- $has := false -}}
{{- range $f := get $families $role -}}
{{- if has $f $dsurface -}}{{- $has = true -}}{{- end -}}
{{- end -}}
{{- if and (not $has) (not (hasKey $served $role)) -}}
{{- fail (printf "trove: endpoints.default.surface (%s) holds no family of the role %s and no further endpoint is served by it, so the %s Deployments would serve nothing; add a family of the role (%s) to endpoints.default.surface or serve a further endpoint from it" (join ", " $dsurface) $role $role (join ", " (get $families $role))) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
