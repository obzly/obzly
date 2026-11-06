{{- define "obzly.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "obzly.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains (include "obzly.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "obzly.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Selector labels for one tier: (dict "root" $ "component" "api") */}}
{{- define "obzly.selectorLabels" -}}
app.kubernetes.io/name: {{ include "obzly.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "obzly.labels" -}}
{{ include "obzly.selectorLabels" . }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end -}}

{{- define "obzly.image" -}}
{{- printf "%s:%s" .Values.image.repository (default (printf "v%s" .Chart.AppVersion) .Values.image.tag) -}}
{{- end -}}

{{- define "obzly.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "obzly.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "obzly.secretName" -}}
{{- default (printf "%s-secrets" (include "obzly.fullname" .)) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "obzly.natsUrl" -}}
{{- printf "nats://%s-broker.%s.svc:4222" (include "obzly.fullname" .) .Release.Namespace -}}
{{- end -}}

{{- define "obzly.jetstreamReplicas" -}}
{{- if ge (int .Values.broker.replicas) 3 }}3{{ else }}1{{ end -}}
{{- end -}}

{{/* Pod-level settings for one tier: (dict "root" $ "tier" .Values.api "component" "api") */}}
{{- define "obzly.podSpec" -}}
serviceAccountName: {{ include "obzly.serviceAccountName" .root }}
{{- with .root.Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .root.Values.podSecurityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with (default .root.Values.nodeSelector .tier.nodeSelector) }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with (default .root.Values.tolerations .tier.tolerations) }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with (default .root.Values.affinity .tier.affinity) }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* Pod template metadata for one tier. */}}
{{- define "obzly.podMetadata" -}}
labels:
  {{- include "obzly.selectorLabels" . | nindent 2 }}
annotations:
  checksum/config: {{ include (print .root.Template.BasePath "/configmap.yaml") .root | sha256sum }}
  {{- if and .root.Values.metrics.enabled .root.Values.metrics.podAnnotations .metrics }}
  prometheus.io/scrape: "true"
  prometheus.io/port: {{ .root.Values.metrics.port | quote }}
  prometheus.io/path: /metrics
  {{- end }}
  {{- with .root.Values.podAnnotations }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
{{- end -}}

{{/* The image, pull policy, config and secret every obzly container uses. */}}
{{- define "obzly.containerBase" -}}
image: {{ include "obzly.image" . }}
imagePullPolicy: {{ .Values.image.pullPolicy }}
{{- with .Values.securityContext }}
securityContext:
  {{- toYaml . | nindent 2 }}
{{- end }}
envFrom:
  - configMapRef:
      name: {{ include "obzly.fullname" . }}-config
  - secretRef:
      name: {{ include "obzly.secretName" . }}
  {{- if include "obzly.valkeyAuth" . }}
  {{- /* Last, so its VALKEY_PASSWORD wins over one in an existingSecret. */}}
  - secretRef:
      name: {{ include "obzly.fullname" . }}-valkey
  {{- end }}
{{- end -}}

{{/* Non-empty when the bundled Valkey requires a password. */}}
{{- define "obzly.valkeyAuth" -}}
{{- if and .Values.valkey.enabled .Values.valkey.auth.enabled }}true{{ end -}}
{{- end -}}

{{/* Ingress from this release's pods only (NetworkPolicy peer). */}}
{{- define "obzly.releasePods" -}}
- podSelector:
    matchLabels:
      app.kubernetes.io/name: {{ include "obzly.name" . }}
      app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* The HTTP tier's probes; migrations run at startup, hence the budget. */}}
{{- define "obzly.httpProbes" -}}
startupProbe:
  httpGet:
    path: /api/v1/system/health
    port: http
  periodSeconds: 5
  failureThreshold: 42
readinessProbe:
  httpGet:
    path: /api/v1/system/ready
    port: http
  periodSeconds: 10
livenessProbe:
  httpGet:
    path: /api/v1/system/live
    port: http
  periodSeconds: 15
  failureThreshold: 3
{{- end -}}

{{- define "obzly.metricsPort" -}}
{{- if .Values.metrics.enabled -}}
- name: metrics
  containerPort: {{ .Values.metrics.port }}
{{- end -}}
{{- end -}}

{{/* OTLP/gRPC TLS (tls.otlpGrpc) for a pod serving 4317; each part empty when off. */}}
{{- define "obzly.otlpTlsEnv" -}}
{{- if .Values.tls.otlpGrpc.secretName -}}
- name: OTLP_GRPC_TLS_CERT_FILE
  value: /etc/obzly/otlp-grpc-tls/tls.crt
- name: OTLP_GRPC_TLS_KEY_FILE
  value: /etc/obzly/otlp-grpc-tls/tls.key
{{- end -}}
{{- end -}}

{{/* A directory mount, not subPath: a subPath mount never sees a renewed Secret. */}}
{{- define "obzly.otlpTlsMount" -}}
{{- if .Values.tls.otlpGrpc.secretName -}}
- name: otlp-grpc-tls
  mountPath: /etc/obzly/otlp-grpc-tls
  readOnly: true
{{- end -}}
{{- end -}}

{{- define "obzly.otlpTlsVolume" -}}
{{- if .Values.tls.otlpGrpc.secretName -}}
- name: otlp-grpc-tls
  secret:
    secretName: {{ .Values.tls.otlpGrpc.secretName }}
{{- end -}}
{{- end -}}

{{/* The name api pods verify querier certificates against (tls.queryResults). */}}
{{- define "obzly.queryResultsServerName" -}}
{{- default (printf "%s-querier.%s.svc" (include "obzly.fullname" .) .Release.Namespace) .Values.tls.queryResults.serverName -}}
{{- end -}}
