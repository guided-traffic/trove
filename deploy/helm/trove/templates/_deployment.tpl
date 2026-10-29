{{/*
One Deployment of a role (R-HA-002) or of an endpoint pool (R-EP-008). Argument: dict "root" .
"role" "web" "pool" "" "name" "web" "r" <the role's effective values>. The main web and worker
Deployments pass the values of their role; a pool passes the web values with its own overrides.
*/}}
{{- define "trove.deployment" -}}
{{- $ := .root }}
{{- $role := .role }}
{{- $pool := .pool }}
{{- $name := .name }}
{{- $r := .r }}
{{- $ctx := dict "root" $ "role" $role "r" $r "pool" $pool }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "trove.fullname" $ }}-{{ $name }}
  labels:
    {{- include "trove.labels" (dict "root" $ "component" $role "pool" $pool) | nindent 4 }}
  {{- with (include "trove.commonAnnotations" $) }}
  annotations:
    {{- . | nindent 4 }}
  {{- end }}
spec:
  replicas: {{ $r.replicaCount }}
  minReadySeconds: {{ $r.minReadySeconds }}
  revisionHistoryLimit: {{ $r.revisionHistoryLimit }}
  strategy:
    {{- toYaml $r.strategy | nindent 4 }}
  selector:
    matchLabels:
      {{- include "trove.selectorLabels" (dict "root" $ "component" $role "pool" $pool) | nindent 6 }}
  template:
    metadata:
      annotations:
        checksum/config: {{ include "trove.config" $ | sha256sum }}
        checksum/encryption-keys: {{ $.Values.secrets.encryption.keys | toJson | sha256sum }}
        {{- with $r.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      labels:
        {{- include "trove.labels" (dict "root" $ "component" $role "pool" $pool) | nindent 8 }}
        trove/serve: "true"
        {{- with $r.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "trove.serviceAccountName" $ }}
      automountServiceAccountToken: {{ $.Values.serviceAccount.automountServiceAccountToken }}
      enableServiceLinks: false
      terminationGracePeriodSeconds: {{ $r.terminationGracePeriodSeconds }}
      {{- include "trove.imagePullSecrets" $ | nindent 6 }}
      securityContext:
        {{- toYaml $.Values.podSecurityContext | nindent 8 }}
      {{- with $r.priorityClassName }}
      priorityClassName: {{ . }}
      {{- end }}
      {{- with $r.schedulerName }}
      schedulerName: {{ . }}
      {{- end }}
      {{- with $r.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $r.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      tolerations:
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoExecute
          tolerationSeconds: {{ $r.nodeFailureTolerationSeconds }}
        - key: node.kubernetes.io/unreachable
          operator: Exists
          effect: NoExecute
          tolerationSeconds: {{ $r.nodeFailureTolerationSeconds }}
        {{- with $r.tolerations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- include "trove.topologySpread" $ctx | nindent 6 }}
      containers:
        - name: trove
          image: {{ include "trove.image" $ | quote }}
          imagePullPolicy: {{ $.Values.image.pullPolicy }}
          args:
            - serve
            - --role
            - {{ $role }}
          securityContext:
            {{- toYaml $.Values.securityContext | nindent 12 }}
          ports:
            {{- if and (eq $role "web") (not $pool) }}
            - name: http
              containerPort: 8080
              protocol: TCP
            {{- end }}
            {{- /* The ports of the endpoints this pod serves (R-EP-008); no other pod listens on them. */}}
            {{- range $port := include "trove.endpointPorts" (dict "root" $ "role" $role "pool" $pool) | fromYamlArray }}
            - containerPort: {{ $port }}
              protocol: TCP
            {{- end }}
            - name: ops
              containerPort: 8081
              protocol: TCP
          env:
            {{- include "trove.podEnv" $ | nindent 12 }}
            {{- if $pool }}
            - name: TROVE_POOL_NAME
              value: {{ $pool | quote }}
            {{- end }}
            {{- with (include "trove.s3EnvFromSecret" $) }}
            {{- . | nindent 12 }}
            {{- end }}
            - name: TROVE_RUNTIME_MEMORY_LIMIT_BYTES
              valueFrom:
                resourceFieldRef:
                  resource: limits.memory
                  divisor: "1"
            - name: TROVE_RUNTIME_TERMINATION_GRACE_PERIOD
              value: {{ printf "%ds" (int64 $r.terminationGracePeriodSeconds) | quote }}
            {{- if not (dig "storage" "spool" "max_bytes" "" $.Values.config) }}
            - name: TROVE_STORAGE_SPOOL_MAX_BYTES
              value: {{ include "trove.spoolBudget" $ctx | quote }}
            {{- end }}
            {{- if not (dig "limits" "cpu" "" $r.resources) }}
            - name: GOMAXPROCS
              valueFrom:
                resourceFieldRef:
                  resource: requests.cpu
                  divisor: "1"
            {{- end }}
            - name: TMPDIR
              value: /tmp
            {{- with $.Values.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- with $r.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          {{- with $.Values.extraEnvFrom }}
          envFrom:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- include "trove.probes" $ | nindent 10 }}
          resources:
            {{- toYaml $r.resources | nindent 12 }}
          volumeMounts:
            - name: config
              mountPath: /etc/trove/trove.yaml
              subPath: trove.yaml
              readOnly: true
            - name: spool
              mountPath: /var/lib/trove/spool
            - name: tmp
              mountPath: /tmp
            {{- include "trove.secretVolumeMounts" (dict "root" $ "scope" "runtime" "role" $role "pool" $pool) | nindent 12 }}
            {{- with $.Values.extraVolumeMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "trove.fullname" $ }}-config
        - name: spool
          {{- if eq $.Values.spool.type "ephemeral" }}
          ephemeral:
            volumeClaimTemplate:
              spec:
                accessModes:
                  {{- toYaml $.Values.spool.ephemeral.accessModes | nindent 18 }}
                {{- with $.Values.spool.ephemeral.storageClassName }}
                storageClassName: {{ . }}
                {{- end }}
                resources:
                  requests:
                    storage: {{ $.Values.spool.ephemeral.size }}
          {{- else }}
          emptyDir:
            sizeLimit: {{ include "trove.spoolSize" $ctx }}
            {{- with $.Values.spool.medium }}
            medium: {{ . }}
            {{- end }}
          {{- end }}
        - name: tmp
          emptyDir:
            sizeLimit: {{ $.Values.tmp.sizeLimit }}
        {{- include "trove.secretVolumes" (dict "root" $ "scope" "runtime" "role" $role "pool" $pool) | nindent 8 }}
        {{- with $.Values.extraVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
{{- end -}}
