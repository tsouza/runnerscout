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

{{- define "runnerscout.mode" -}}
{{- if .Values.crd.scaleSetName -}}crd{{- else -}}mounted{{- end -}}
{{- end -}}

{{- define "runnerscout.stateName" -}}
{{- if .Values.crd.scaleSetName -}}{{ .Values.crd.scaleSetName }}{{- else -}}{{ .Values.config.name }}{{- end -}}
{{- end -}}

{{- define "runnerscout.validateOwnership" -}}
{{- if .Release.IsUpgrade -}}
{{- $deployments := lookup "apps/v1" "Deployment" .Release.Namespace "" -}}
{{- range $existing := $deployments.items -}}
{{- if eq (index (default dict $existing.metadata.labels) "app.kubernetes.io/instance") $.Release.Name -}}
{{- $annotations := default dict $existing.metadata.annotations -}}
{{- $oldMode := default "mounted" (index $annotations "runnerscout.io/configuration-mode") -}}
{{- $oldName := default "" (index $annotations "runnerscout.io/state-name") -}}
{{- if and (eq $oldName "") (eq $oldMode "mounted") -}}
{{- $config := lookup "v1" "ConfigMap" $.Release.Namespace (printf "%s-config" $existing.metadata.name) -}}
{{- if $config -}}
{{- $oldConfig := fromJson (index $config.data "config.json") -}}
{{- $oldName = default "" $oldConfig.name -}}
{{- end -}}
{{- end -}}
{{- if or (ne $existing.metadata.name (include "runnerscout.fullname" $)) (ne (index $existing.spec.selector.matchLabels "app.kubernetes.io/name") (include "runnerscout.name" $)) (ne $oldMode (include "runnerscout.mode" $)) (ne $oldName (include "runnerscout.stateName" $)) -}}
{{- fail "controller identity, configuration mode and scale-set state name cannot change during upgrade; finish existing cleanup before reinstalling" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "runnerscout.podPolicySelector" -}}
runnerscout.io/instance: {{ .Release.Name | quote }}
{{- end -}}
