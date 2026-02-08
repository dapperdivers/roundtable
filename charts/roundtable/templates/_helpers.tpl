{{/*
Expand the name of the chart.
*/}}
{{- define "roundtable.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "roundtable.fullname" -}}
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

{{/*
Common labels
*/}}
{{- define "roundtable.labels" -}}
helm.sh/chart: {{ include "roundtable.name" . }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: roundtable
{{- end }}

{{/*
Knight labels
*/}}
{{- define "roundtable.knightLabels" -}}
{{ include "roundtable.labels" .ctx }}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/component: knight
roundtable/domain: {{ .knight.domain }}
roundtable/fleet: {{ .knight.fleetId }}
{{- end }}

{{/*
Knight selector labels
*/}}
{{- define "roundtable.knightSelectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/component: knight
{{- end }}
