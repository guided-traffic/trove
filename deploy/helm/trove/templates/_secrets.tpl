{{/*
The Secret mounts of a pod (R-HA-030) as a YAML list. Argument: dict "root" . "scope" "runtime"
(web and worker pods) or "migrate" (the migration Job). Each entry names one existing Secret,
the keys that are projected and the file name each key gets. Secrets mount read-only with
defaultMode 0440, with items and without subPath, so a rotated Secret reaches the files.
Secrets never become environment variables. The login secrets (secrets.oidc, secrets.breakGlass) are
mounted into web pods only.
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
{{- if $s.s3CA.existingSecret -}}
{{- $out = append $out (dict "name" "s3-ca" "secret" $s.s3CA.existingSecret "path" "/var/run/secrets/trove/s3-ca" "items" (list (dict "key" $s.s3CA.key "path" "ca.crt"))) -}}
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
{{- /* The login secrets belong to the pods that serve the client listener (R-AUTH-014, R-AUTH-042): web pods and pool pods, never worker or the migration Job. */ -}}
{{- if eq (.role | default "") "web" -}}
{{- range $s.oidc -}}
{{- $out = append $out (dict "name" (printf "oidc-%s" .providerId) "secret" .existingSecret "path" (printf "/var/run/secrets/trove/oidc-%s" .providerId) "items" (list (dict "key" (.key | default "client-secret") "path" "client-secret"))) -}}
{{- end -}}
{{- if $s.breakGlass.existingSecret -}}
{{- $out = append $out (dict "name" "break-glass" "secret" $s.breakGlass.existingSecret "path" "/var/run/secrets/trove/break-glass" "items" (list (dict "key" $s.breakGlass.key "path" "password"))) -}}
{{- end -}}
{{- end -}}
{{- if eq (dig "metrics" "auth" "" .root.Values.config) "static" -}}
{{- $out = append $out (dict "name" "metrics" "secret" $s.metricsToken.existingSecret "path" "/var/run/secrets/trove/metrics" "items" (list (dict "key" $s.metricsToken.key "path" "token"))) -}}
{{- end -}}
{{- /*
TLS material of the listeners (R-HA-063): only the pods whose process loads it; the Job serves no
listener. The listener pair of tls.secretName is read by the ops listener of every pod unless tls.ops
gives it its own pair, and by the client listener (web pods of the main pool) and by every further
endpoint without a certificate of its own (the pods that serve it). The client CA belongs to the
client listener of the default endpoint only. A pod that loads none of them does not get the private key.
*/ -}}
{{- $t := .root.Values.tls -}}
{{- if $t.enabled -}}
{{- $role := .role | default "" -}}
{{- $pool := .pool | default "" -}}
{{- $client := and (eq $role "web") (not $pool) -}}
{{- $mainPair := or $client (not $t.ops.secretName) -}}
{{- if and (eq $role "web") (not $mainPair) -}}
{{- range $name := include "trove.endpointNames" .root | fromYamlArray -}}
{{- if eq (include "trove.endpointPool" (dict "root" $.root "name" $name)) $pool -}}
{{- $e := include "trove.endpoint" (dict "root" $.root "name" $name) | fromYaml -}}
{{- if not $e.tlsSecret -}}
{{- $mainPair = true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $mainPair -}}
{{- $out = append $out (dict "name" "tls" "secret" $t.secretName "path" "/var/run/secrets/trove/tls" "items" (list (dict "key" $t.certKey "path" "tls.crt") (dict "key" $t.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- if and $client $t.clientCA.secretName -}}
{{- $out = append $out (dict "name" "tls-client-ca" "secret" $t.clientCA.secretName "path" "/var/run/secrets/trove/tls-client-ca" "items" (list (dict "key" $t.clientCA.key "path" "ca.crt"))) -}}
{{- end -}}
{{- if $t.ops.secretName -}}
{{- $out = append $out (dict "name" "tls-ops" "secret" $t.ops.secretName "path" "/var/run/secrets/trove/tls-ops" "items" (list (dict "key" $t.ops.certKey "path" "tls.crt") (dict "key" $t.ops.keyKey "path" "tls.key"))) -}}
{{- end -}}
{{- end -}}
{{- /* The Secrets of the endpoints this pod serves (R-EP-004, R-EP-008); worker pods and the Job serve none. */ -}}
{{- $out = concat $out (include "trove.endpointSecretMounts" (dict "root" .root "role" (.role | default "") "pool" (.pool | default "")) | fromYamlArray) -}}
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
