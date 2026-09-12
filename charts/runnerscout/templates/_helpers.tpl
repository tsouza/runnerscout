{{- define "runnerscout.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "runnerscout.fullname" -}}
{{- default (printf "%s-%s" .Release.Name (include "runnerscout.name" .)) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "runnerscout.sa" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "runnerscout.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when create=false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
{{- define "runnerscout.image" -}}
{{- $repo := required "image.repository is required" .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (required "image.tag or image.digest is required" .Values.image.tag) -}}
{{- end -}}
{{- end -}}
{{- define "runnerscout.selector" -}}
app.kubernetes.io/name: {{ include "runnerscout.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "runnerscout.labels" -}}
{{ include "runnerscout.selector" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}
{{- define "runnerscout.config" -}}
{{- if empty .Values.config -}}{{ fail "config must contain the controller configuration" }}{{- end -}}
{{- if hasKey .Values.config "namespace" -}}{{ fail "config.namespace is derived from the release namespace" }}{{- end -}}
{{- $cfg := deepCopy .Values.config -}}
{{- $_ := set $cfg "namespace" .Release.Namespace -}}
{{- if .Values.catalog.existingConfigMap -}}
{{- $_ := set $cfg "catalogPath" "/etc/runnerscout/catalog/catalog.json" -}}
{{- end -}}
{{- toJson $cfg -}}
{{- end -}}
