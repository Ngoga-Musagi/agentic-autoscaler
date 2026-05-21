{{/*
Expand the name of the chart.
*/}}
{{- define "agentic-autoscaler.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "agentic-autoscaler.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "agentic-autoscaler.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/name: {{ include "agentic-autoscaler.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels — used in Deployment.spec.selector and Service.spec.selector.
*/}}
{{- define "agentic-autoscaler.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agentic-autoscaler.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The operator image reference, defaulting tag to .Chart.AppVersion.
*/}}
{{- define "agentic-autoscaler.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Name of the ServiceAccount.
*/}}
{{- define "agentic-autoscaler.serviceAccountName" -}}
{{- .Values.serviceAccount.name | default (include "agentic-autoscaler.fullname" .) }}
{{- end }}
