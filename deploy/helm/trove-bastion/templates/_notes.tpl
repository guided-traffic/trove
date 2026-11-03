{{/* The text of NOTES.txt, a template of its own so that a test can render it. Argument: the root context. */}}
{{- define "trove-bastion.notes" -}}
{{- $fullname := include "trove-bastion.fullname" . -}}
trove Bastion {{ $fullname }} is installed as release {{ .Release.Name }} in namespace {{ .Release.Namespace }}.

  public URL      {{ dig "server" "base_url" "" .Values.config }}
  main endpoint   {{ dig "bastion" "main" "url" "" .Values.config }}
  cache           {{ dig "bastion" "cache" "backend" "volume" .Values.config }}{{ if eq (include "trove-bastion.workloadKind" .) "StatefulSet" }} (persistent volume per pod){{ end }}
  replicas        {{ include "trove-bastion.replicas" . }}{{ if .Values.autoscaling.enabled }} at most (autoscaling){{ end }}

The Bastion serves a read only after the main installation has said that the caller may read it. Register it at the main
installation and issue its token there first (the registration's base URL must equal config.server.base_url); the token
goes into the Secret {{ .Values.secrets.mainToken.existingSecret }}, key {{ .Values.secrets.mainToken.key }}, which this chart never creates. A
replaced token is picked up within config.bastion.main.token_reload_interval without a restart.
{{- if .Values.ingress.enabled }}

The Ingress carries annotations of HAProxy Ingress (haproxy-ingress.github.io/…) for the edge limit
{{- if .Values.cacheVolume.routeByPath }} and for routing by path{{ end }}. Any other ingress controller ignores them: the Bastion's own limits and
blocking (config.bastion.limits, config.bastion.blocking) still hold, the coarse limit in front of them does not{{ if .Values.cacheVolume.routeByPath }}, and
a replica fills its own copy of every path{{ end }}. HAProxy Ingress counts per TCP peer address: behind a load balancer that does not preserve the
client address, every caller shares one budget (set the controller's proxy protocol option, or turn edgeLimit.enabled off).
{{- end }}
{{- if not .Values.tls.enabled }}

Warning: tls.enabled is false. The Bastion's listeners, its probes and its scrape endpoint run plain HTTP inside the cluster.
{{- end }}
{{- if not .Values.networkPolicy.enabled }}

Warning: networkPolicy.enabled is false. Nothing restricts what an attacker who takes over a Bastion pod can reach (R-BAS-047).
{{- else if not .Values.networkPolicy.egress.enabled }}

Warning: networkPolicy.egress.enabled is false. The Bastion pods may reach any address.
{{- end }}
{{- end -}}
