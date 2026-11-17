{{- define "v8s.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "v8s.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "v8s.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "v8s.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "v8s.selectorLabels" -}}
app.kubernetes.io/name: {{ include "v8s.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "v8s.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "v8s.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* The rendered server configuration. listen and dataDir belong to the chart (port, volume). */}}
{{- define "v8s.config" -}}
{{- $_ := required "config.publicUrl is required (https origin of the install)" .Values.config.publicUrl -}}
{{- $_ := required "config.oidc.issuer is required" .Values.config.oidc.issuer -}}
{{- $_ := required "config.oidc.clientId is required" .Values.config.oidc.clientId -}}
{{- $base := dict "listen" ":7931" "dataDir" .Values.persistence.mountPath -}}
{{- $cfg := mergeOverwrite (deepCopy .Values.config) $base -}}
{{- if not $cfg.clusterApp }}{{- $_ := unset $cfg "clusterApp" }}{{- end -}}
{{- toYaml $cfg -}}
{{- end }}
