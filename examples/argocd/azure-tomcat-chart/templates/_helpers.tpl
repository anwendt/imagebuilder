{{- define "azureTomcat.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "azureTomcat.namespace" -}}
{{- default .Release.Namespace .Values.namespace.name -}}
{{- end -}}

{{- define "azureTomcat.labels" -}}
app.kubernetes.io/name: {{ include "azureTomcat.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "azureTomcat.credentialsJson" -}}
{{- $creds := .Values.providerConfig.credentials.values -}}
{{- dict "subscriptionId" $creds.subscriptionId "tenantId" $creds.tenantId "clientId" $creds.clientId "clientSecret" $creds.clientSecret "storageAccountKey" $creds.storageAccountKey | toJson -}}
{{- end -}}
