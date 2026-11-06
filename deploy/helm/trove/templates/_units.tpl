{{/*
The Deployments of the chart as data (R-HA-002, R-HA-062, R-HA-064, R-EP-008). One "unit" per
Deployment: the main Deployments api, packages and worker, the path pools of api and packages,
the worker pools, the endpoint pools and, with web.enabled, the bridge Deployment web with its
endpoint pools. Every template that renders something per Deployment (the Deployment itself, its
PodDisruptionBudget, HorizontalPodAutoscaler and NetworkPolicy) ranges over the same list, so the
objects of one Deployment always agree on its name, labels and selector.

A unit is a map with:
  name           the Deployment's suffix: api, packages-npm, worker-vuln, api-ep-customers, web
  role           the argument of --role: api, packages, worker or web
  pool           "" for a main Deployment, the pool's name, or ep-<endpoint> for an endpoint pool
  kind           main | pool | endpoint | bridge
  key            the values path, for messages
  r              the effective values (the role's values with the pool's overrides on top)
  paths          the path prefixes of a path pool
  queues         the queues a worker pool claims alone
  excludeQueues  the queues the main worker does not claim (the union of the worker pools' queues)
*/}}

{{/* "true" when a pod of this role and pool opens the default client listener :8080. Argument: dict "role" "pool". */}}
{{- define "trove.opensDefault" -}}
{{- if and (ne .role "worker") (not (hasPrefix "ep-" (.pool | default ""))) -}}true{{- end -}}
{{- end -}}

{{/*
The roles that serve the families of an endpoint's surface (Q-EP-70, Q-EP-71): packages for
packages and files, api for api and ci_exchange; packages first. Argument: dict "root" . "name" "customers".
Returns a YAML list with one or two entries.
*/}}
{{- define "trove.surfaceRoles" -}}
{{- $e := include "trove.endpoint" . | fromYaml -}}
{{- $roles := list -}}
{{- if or (has "packages" $e.surface) (has "files" $e.surface) -}}
{{- $roles = append $roles "packages" -}}
{{- end -}}
{{- if or (has "api" $e.surface) (has "ci_exchange" $e.surface) -}}
{{- $roles = append $roles "api" -}}
{{- end -}}
{{- if not $roles -}}
{{- $roles = list "packages" -}}
{{- end -}}
{{- toYaml $roles -}}
{{- end -}}

{{/*
The names of the further endpoints a pod of this role and pool opens a listener for, as a YAML
list (R-EP-002): a pod of an endpoint pool opens the endpoints of that pool; any other serving pod
(a main Deployment or a path pool) opens the endpoints without a pool whose surface holds a family
of its role; the bridge role web opens every endpoint of its pool; a worker opens none.
Argument: dict "root" . "role" "packages" "pool" "".
*/}}
{{- define "trove.unitEndpoints" -}}
{{- $out := list -}}
{{- if ne .role "worker" -}}
{{- $root := .root -}}
{{- $role := .role -}}
{{- $pool := .pool | default "" -}}
{{- range $name := include "trove.endpointNames" $root | fromYamlArray -}}
{{- $epool := include "trove.endpointPool" (dict "root" $root "name" $name) -}}
{{- $fits := or (eq $role "web") (has $role (include "trove.surfaceRoles" (dict "root" $root "name" $name) | fromYamlArray)) -}}
{{- if and $fits (eq (ternary $pool "" (hasPrefix "ep-" $pool)) $epool) -}}
{{- $out = append $out $name -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/* The container ports a pod serves for endpoints, as a YAML list. Argument: as trove.unitEndpoints. */}}
{{- define "trove.unitEndpointPorts" -}}
{{- $ports := list -}}
{{- $root := .root -}}
{{- range $name := include "trove.unitEndpoints" . | fromYamlArray -}}
{{- $ports = append $ports (int (dig "port" 0 (index $root.Values.endpoints $name))) -}}
{{- end -}}
{{- toYaml $ports -}}
{{- end -}}

{{/*
The values of a pool: the role's values (without its pools and the bridge's two flags) with the entry's
own keys on top, key by key. Argument: dict "base" <role values> "entry" <pool entry>. Returns YAML.
*/}}
{{- define "trove.poolValues" -}}
{{- include "trove.mergeValues" (dict "base" (omit .base "pools" "enabled" "routed") "over" (omit .entry "name" "paths" "queues" "enabled" "routed")) -}}
{{- end -}}

{{/* Every Deployment as a YAML list of units. Argument: the root context. */}}
{{- define "trove.units" -}}
{{- $ := . -}}
{{- $v := .Values -}}
{{- $bridge := $v.web.enabled -}}
{{- $units := list -}}
{{- $poolQueues := list -}}
{{- range $p := $v.worker.pools -}}
{{- range $p.queues -}}
{{- $poolQueues = append $poolQueues . -}}
{{- end -}}
{{- end -}}
{{- $poolQueues = uniq $poolQueues | sortAlpha -}}
{{- range $role := list "api" "packages" "worker" -}}
{{- $unit := dict "name" $role "role" $role "pool" "" "kind" "main" "key" $role "r" (omit (index $v $role) "pools" "enabled") -}}
{{- if and (eq $role "worker") $poolQueues -}}
{{- $_ := set $unit "excludeQueues" $poolQueues -}}
{{- end -}}
{{- $units = append $units $unit -}}
{{- end -}}
{{- range $role := list "api" "packages" -}}
{{- range $p := (index $v $role).pools -}}
{{- $units = append $units (dict "name" (printf "%s-%s" $role $p.name) "role" $role "pool" $p.name "kind" "pool" "key" (printf "%s.pools[%s]" $role $p.name) "r" (include "trove.poolValues" (dict "base" (index $v $role) "entry" $p) | fromYaml) "paths" $p.paths) -}}
{{- end -}}
{{- end -}}
{{- range $p := $v.worker.pools -}}
{{- $units = append $units (dict "name" (printf "worker-%s" $p.name) "role" "worker" "pool" $p.name "kind" "pool" "key" (printf "worker.pools[%s]" $p.name) "r" (include "trove.poolValues" (dict "base" $v.worker "entry" $p) | fromYaml) "queues" $p.queues) -}}
{{- end -}}
{{- if $bridge -}}
{{- $units = append $units (dict "name" "web" "role" "web" "pool" "" "kind" "bridge" "key" "web" "r" (omit $v.web "pools" "enabled" "routed")) -}}
{{- end -}}
{{- range $name := include "trove.endpointNames" . | fromYamlArray -}}
{{- if include "trove.endpointPool" (dict "root" $ "name" $name) -}}
{{- /* The bridge renders the pools of the roles beside the web pool, so the second step switches onto pods that are ready (R-HA-064); no Service selects them before. */ -}}
{{- $roles := include "trove.surfaceRoles" (dict "root" $ "name" $name) | fromYamlArray -}}
{{- if $bridge -}}
{{- $roles = concat (list "web") $roles -}}
{{- end -}}
{{- range $role := $roles -}}
{{- $units = append $units (dict "name" (printf "%s-ep-%s" $role $name) "role" $role "pool" (printf "ep-%s" $name) "kind" "endpoint" "key" (printf "endpoints.%s.pool" $name) "r" (include "trove.endpointPoolValues" (dict "root" $ "name" $name "role" $role) | fromYaml)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $units -}}
{{- end -}}

{{/* The number of replicas a unit runs at least: the autoscaler's minimum, else replicaCount. */}}
{{- define "trove.unitMinReplicas" -}}
{{- if dig "autoscaling" "enabled" false . -}}{{ .autoscaling.minReplicas }}{{- else -}}{{ .replicaCount }}{{- end -}}
{{- end -}}

{{/*
The number of replicas a unit may run at most, plus its surge (R-HA-016): maxReplicas with an
autoscaler, replicaCount without, plus rollingUpdate.maxSurge (an integer, or a percentage of the
replicas rounded up).
*/}}
{{- define "trove.unitMaxReplicas" -}}
{{- $n := ternary (int .autoscaling.maxReplicas) (int .replicaCount) (dig "autoscaling" "enabled" false . ) -}}
{{- $surge := dig "strategy" "rollingUpdate" "maxSurge" 0 . -}}
{{- if eq .strategy.type "Recreate" -}}
{{- $surge = 0 -}}
{{- end -}}
{{- if kindIs "string" $surge -}}
{{- $pct := trimSuffix "%" $surge | int -}}
{{- $surge = ceil (divf (mul $n $pct) 100) | int -}}
{{- end -}}
{{- add $n (int $surge) -}}
{{- end -}}

{{/*
serving_max and worker_max of the connection budget (R-HA-016): the sums of trove.unitMaxReplicas over
the Deployments without and with a lease pool. Argument: the root context. Returns YAML {serving, worker}.
*/}}
{{- define "trove.connectionCounts" -}}
{{- $serving := 0 -}}
{{- $worker := 0 -}}
{{- range $u := include "trove.units" . | fromYamlArray -}}
{{- $n := include "trove.unitMaxReplicas" $u.r | int -}}
{{- if eq $u.role "worker" -}}
{{- $worker = add $worker $n | int -}}
{{- else -}}
{{- $serving = add $serving $n | int -}}
{{- end -}}
{{- end -}}
{{- dict "serving" $serving "worker" $worker | toYaml -}}
{{- end -}}
