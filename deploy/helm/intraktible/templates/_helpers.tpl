{{- define "intraktible.name" -}}{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}{{- end -}}
{{- define "intraktible.fullname" -}}{{- printf "%s-%s" .Release.Name (include "intraktible.name" .) | trunc 63 | trimSuffix "-" -}}{{- end -}}
{{- define "intraktible.labels" -}}
app.kubernetes.io/name: {{ include "intraktible.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "intraktible.image" -}}{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}{{- end -}}
{{- define "intraktible.secretName" -}}{{ .Values.secret.existingSecret | default (printf "%s-secrets" (include "intraktible.fullname" .)) }}{{- end -}}
