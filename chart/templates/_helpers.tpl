{{/*
Expand the name of the chart.
*/}}
{{- define "chart.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resource name used by Deployment, ServiceAccount, PDB, etc. (anything that
includes `chart.fullname`).

The conventional Helm `<release>-<chart>` prefix is intentionally dropped:
NodeWright is a singleton per namespace (cluster-scoped CRDs, webhook
configurations, finalizers), so a release-name prefix on resource names
adds no value and just makes them noisy. Truncated at 63 chars for the DNS
name limit. `fullnameOverride` is still honored for users who really do
need a custom name.
*/}}
{{- define "chart.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "chart.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "chart.labels" -}}
helm.sh/chart: {{ include "chart.chart" . }}
{{ include "chart.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "chart.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector for the controller-manager Deployment.

Shared by the Deployment's spec.selector and the selector-migration pre-upgrade
hook so both reason about the identical label set. spec.selector is immutable,
so when the chart name or release name changes these labels (e.g. the
skyhook-operator -> nodewright rename), the hook deletes the stale Deployment
and lets helm recreate it rather than failing the upgrade.
*/}}
{{- define "chart.managerSelectorLabels" -}}
control-plane: controller-manager
{{ include "chart.selectorLabels" . }}
{{- end }}

{{/*
Name of the webhook Service.

Also handed to the operator as WEBHOOK_SERVICE_NAME: the operator mints the
webhook serving certificate itself and puts this name in the SAN, so a Service
named anything other than what the operator was told produces a cert the API
server rejects. Truncated at 63 because a Service name is a DNS-1035 label.
*/}}
{{- define "chart.webhookServiceName" -}}
{{- default (printf "%s-webhook-service" (include "chart.fullname" .)) .Values.webhook.serviceName | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Names of the webhook configurations.

NOT handed to the operator: it discovers these objects by the
nodewright.nvidia.com/webhook-config label, so their names are free to change.
The names are still needed here because the manager ClusterRole scopes `update`
to them by resourceNames, and the pre-delete cleanup job deletes them by name.
Both render from these helpers, so they stay in lockstep with the objects.
*/}}
{{- define "chart.validatingWebhookName" -}}
{{- printf "%s-validating-webhook" (include "chart.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "chart.mutatingWebhookName" -}}
{{- printf "%s-mutating-webhook" (include "chart.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "chart.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "chart.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The pull Secret names: imagePullSecret first, then each imagePullSecrets entry, without
duplicates or empty names.
*/}}
{{- define "chart.imagePullSecretNames" -}}
{{- $names := list -}}
{{- if .Values.imagePullSecret -}}
{{- $names = append $names .Values.imagePullSecret -}}
{{- end -}}
{{- range .Values.imagePullSecrets -}}
{{- if or (not (kindIs "map" .)) (not .name) -}}
{{- fail "Error: every imagePullSecrets entry needs a name, as {name: <secret>}." -}}
{{- end -}}
{{- if not (has .name $names) -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}
