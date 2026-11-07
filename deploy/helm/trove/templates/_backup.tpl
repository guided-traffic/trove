{{/*
The Jobs of `trove backup` (R-HA-060, R-BAK-012): the CronJob <fullname>-backup and the restore Job <fullname>-restore.
They are not roles (Q-ARCH-342): they serve nothing, open no port, have no probe and mount no volume but /tmp. A Job runs
as a ServiceAccount of its own, so a workload identity maps it to a cloud role of its own (R-BAK-015), and mounts the
Secrets of its scope only (templates/_secrets.tpl): the backup pod the database login of the backup (or the runtime
login), the blob store's read credential and the target; the restore Job the login of the migration Job, the blob
store, the target and the key ring. None of them mounts a Valkey Secret, and the pods carry no trove/serve label, so no
Service selects them.
*/}}

{{/*
The pod failure policy of both Jobs (R-BAK-012, R-BAK-013): a disruption (a drain, a preemption) is ignored first, so it
never consumes backoffLimit; the exit codes 2 (usage or configuration), 3 (another run is live), 4 (a foreign target) and
6 (a precondition does not hold) fail the Job, because a retry cannot change them. 75 (temporary) and 1 (failed) are
retried within backoffLimit. Argument: the container name.
*/}}
{{- define "trove.jobPodFailurePolicy" -}}
podFailurePolicy:
  rules:
    - action: Ignore
      onPodConditions:
        - type: DisruptionTarget
    - action: FailJob
      onExitCodes:
        containerName: {{ . }}
        operator: In
        values: [2, 3, 4, 6]
{{- end -}}

{{/*
The pod template of a Job. Argument: dict "root" . "kind" "backup" | "restore" | "post-restore" "key" <the values key, kind
unless "postRestore"> "args" <list of container arguments>. The post-restore Job also mounts the credentials file that the chart
renders for the journal replay (templates/restore.yaml, templates/post-restore.yaml).
*/}}
{{- define "trove.jobPod" -}}
{{- $ := .root -}}
{{- $kind := .kind -}}
{{- $r := get $.Values (.key | default $kind) -}}
{{- $labels := include "trove.labels" (dict "root" $ "component" $kind) -}}
metadata:
  labels:
    {{- $labels | nindent 4 }}
    {{- with $r.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  annotations:
    {{- /* Only the cluster autoscaler's scale-down reads this: a consolidation does not restart a run of many hours. kubectl drain and the Eviction API ignore it, so a drain never waits (R-BAK-012). */}}
    cluster-autoscaler.kubernetes.io/safe-to-evict: "false"
    {{- with $r.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  restartPolicy: Never
  serviceAccountName: {{ include "trove.jobServiceAccountName" (dict "root" $ "kind" $kind "key" .key) }}
  automountServiceAccountToken: false
  enableServiceLinks: false
  terminationGracePeriodSeconds: {{ $r.terminationGracePeriodSeconds }}
  {{- include "trove.imagePullSecrets" $ | nindent 2 }}
  securityContext:
    {{- toYaml $.Values.podSecurityContext | nindent 4 }}
  {{- with $r.priorityClassName }}
  priorityClassName: {{ . }}
  {{- end }}
  {{- with $r.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $r.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $r.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  containers:
    - name: {{ $kind }}
      image: {{ include "trove.image" $ | quote }}
      imagePullPolicy: {{ $.Values.image.pullPolicy }}
      args:
        {{- range .args }}
        - {{ . | quote }}
        {{- end }}
      securityContext:
        {{- toYaml $.Values.securityContext | nindent 8 }}
      env:
        {{- include "trove.podEnv" $ | nindent 8 }}
        {{- if eq $kind "backup" }}
        - name: TROVE_JOB_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.labels['job-name']
        {{- end }}
        {{- with (include "trove.s3EnvFromSecret" $) }}
        {{- . | nindent 8 }}
        {{- end }}
        - name: TMPDIR
          value: /tmp
        {{- with $r.extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      resources:
        {{- toYaml $r.resources | nindent 8 }}
      volumeMounts:
        - name: config
          mountPath: /etc/trove/trove.yaml
          subPath: trove.yaml
          readOnly: true
        - name: tmp
          mountPath: /tmp
        {{- if and (has $kind (list "restore" "post-restore")) (ne $r.journalCredentials.source "runtime") }}
        - name: journal-credentials
          mountPath: /etc/trove/{{ $kind }}
          readOnly: true
        {{- end }}
        {{- include "trove.secretVolumeMounts" (dict "root" $ "scope" $kind) | nindent 8 }}
  volumes:
    - name: config
      configMap:
        name: {{ include "trove.fullname" $ }}-config
    - name: tmp
      emptyDir:
        sizeLimit: {{ $.Values.backup.tmpSizeLimit }}
    {{- if and (has $kind (list "restore" "post-restore")) (ne $r.journalCredentials.source "runtime") }}
    - name: journal-credentials
      configMap:
        name: {{ include "trove.fullname" $ }}-{{ $kind }}
    {{- end }}
    {{- include "trove.secretVolumes" (dict "root" $ "scope" $kind) | nindent 4 }}
{{- end -}}

{{/*
The credentials file of --credentials-file and --journal-credentials-file (internal/journal/replay LoadCredentials), a ConfigMap
<fullname>-<kind>. The file is what turns the listing of object versions on, so a delete marker or a deletion by the holder of the
runtime credential cannot hide a journal entry (R-HA-054); the source default lets the Job's workload identity do it, static reads
secrets.journalReader. Argument: dict "root" . "kind" "restore" | "post-restore" "values" <the Job's values>.
*/}}
{{- define "trove.journalCredentialsConfigMap" -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "trove.fullname" .root }}-{{ .kind }}
  labels:
    {{- include "trove.labels" (dict "root" .root "component" .kind) | nindent 4 }}
  {{- with (include "trove.commonAnnotations" .root) }}
  annotations:
    {{- . | nindent 4 }}
  {{- end }}
data:
  credentials.yaml: |
    source: {{ .values.journalCredentials.source }}
    {{- if eq .values.journalCredentials.source "static" }}
    access_key_id_file: /var/run/secrets/trove/journal-reader/access-key-id
    secret_access_key_file: /var/run/secrets/trove/journal-reader/secret-access-key
    {{- end }}
{{- end -}}
