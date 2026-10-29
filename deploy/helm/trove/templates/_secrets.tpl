{{/*
The Secret mounts of a pod (R-HA-030) as a YAML list. Argument: dict "root" . "scope" "runtime"
(web and worker pods) or "migrate" (the migration Job). Each entry names one existing Secret,
the keys that are projected and the file name each key gets. Secrets mount read-only with
defaultMode 0440, with items and without subPath, so a rotated Secret reaches the files.
Secrets never become environment variables.
*/}}
{{- define "trove.secretMounts" -}}
{{- $s := .root.Values.secrets -}}
{{- $out := list -}}
{{- $dbSecret := $s.database -}}
{{- if and (eq .scope "migrate") $s.databaseMigration.existingSecret -}}
{{- $dbSecret = $s.databaseMigration -}}
{{- end -}}
{{- $out = append $out (dict "name" "database" "secret" $dbSecret.existingSecret "path" "/var/run/secrets/trove/database" "items" (list (dict "key" $dbSecret.usernameKey "path" "username") (dict "key" $dbSecret.passwordKey "path" "password"))) -}}
{{- if $s.databaseCA.existingSecret -}}
{{- $out = append $out (dict "name" "database-ca" "secret" $s.databaseCA.existingSecret "path" "/var/run/secrets/trove/database-ca" "items" (list (dict "key" $s.databaseCA.key "path" "ca.crt"))) -}}
{{- end -}}
{{- if $s.databaseClientCert.existingSecret -}}
{{- $out = append $out (dict "name" "database-client" "secret" $s.databaseClientCert.existingSecret "path" "/var/run/secrets/trove/database-client" "items" (list (dict "key" $s.databaseClientCert.certKey "path" "tls.crt") (dict "key" $s.databaseClientCert.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- if eq .scope "runtime" -}}
{{- /* The key ring is mounted into replicas only: the migration Job never seals or opens a value (R-HA-030). */ -}}
{{- $encItems := list -}}
{{- range $s.encryption.keys -}}
{{- $encItems = append $encItems (dict "key" . "path" .) -}}
{{- end -}}
{{- $out = append $out (dict "name" "encryption" "secret" $s.encryption.existingSecret "path" "/var/run/secrets/trove/encryption" "items" $encItems) -}}
{{- if eq (dig "storage" "s3" "credentials" "source" "" .root.Values.config) "static" -}}
{{- $out = append $out (dict "name" "s3" "secret" $s.s3.existingSecret "path" "/var/run/secrets/trove/s3" "items" (list (dict "key" $s.s3.accessKeyIdKey "path" "access-key-id") (dict "key" $s.s3.secretAccessKeyKey "path" "secret-access-key"))) -}}
{{- end -}}
{{- $vkItems := list -}}
{{- if $s.valkey.usernameKey -}}
{{- $vkItems = append $vkItems (dict "key" $s.valkey.usernameKey "path" "username") -}}
{{- end -}}
{{- $vkItems = append $vkItems (dict "key" $s.valkey.passwordKey "path" "password") -}}
{{- $out = append $out (dict "name" "valkey" "secret" $s.valkey.existingSecret "path" "/var/run/secrets/trove/valkey" "items" $vkItems) -}}
{{- if $s.valkeySentinel.existingSecret -}}
{{- $snItems := list -}}
{{- if $s.valkeySentinel.usernameKey -}}
{{- $snItems = append $snItems (dict "key" $s.valkeySentinel.usernameKey "path" "username") -}}
{{- end -}}
{{- $snItems = append $snItems (dict "key" $s.valkeySentinel.passwordKey "path" "password") -}}
{{- $out = append $out (dict "name" "valkey-sentinel" "secret" $s.valkeySentinel.existingSecret "path" "/var/run/secrets/trove/valkey-sentinel" "items" $snItems) -}}
{{- end -}}
{{- if $s.valkeyCA.existingSecret -}}
{{- $out = append $out (dict "name" "valkey-ca" "secret" $s.valkeyCA.existingSecret "path" "/var/run/secrets/trove/valkey-ca" "items" (list (dict "key" $s.valkeyCA.key "path" "ca.crt"))) -}}
{{- end -}}
{{- if $s.valkeyClientCert.existingSecret -}}
{{- $out = append $out (dict "name" "valkey-client" "secret" $s.valkeyClientCert.existingSecret "path" "/var/run/secrets/trove/valkey-client" "items" (list (dict "key" $s.valkeyClientCert.certKey "path" "tls.crt") (dict "key" $s.valkeyClientCert.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- if eq (dig "metrics" "auth" "" .root.Values.config) "static" -}}
{{- $out = append $out (dict "name" "metrics" "secret" $s.metricsToken.existingSecret "path" "/var/run/secrets/trove/metrics" "items" (list (dict "key" $s.metricsToken.key "path" "token"))) -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{- define "trove.secretVolumes" -}}
{{- range fromYamlArray (include "trove.secretMounts" .) }}
- name: secret-{{ .name }}
  secret:
    secretName: {{ .secret }}
    defaultMode: 0440
    items:
    {{- range .items }}
    - key: {{ .key }}
      path: {{ .path }}
    {{- end }}
{{- end }}
{{- end -}}

{{- define "trove.secretVolumeMounts" -}}
{{- range fromYamlArray (include "trove.secretMounts" .) }}
- name: secret-{{ .name }}
  mountPath: {{ .path }}
  readOnly: true
{{- end }}
{{- end -}}
