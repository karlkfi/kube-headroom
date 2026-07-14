{{/*
Common labels for the CRD chart. Kept minimal: the CRD is a cluster-scoped,
long-lived object, so we avoid release-specific labels that would churn on
every reinstall.
*/}}
{{- define "kube-headroom-crds.labels" -}}
app.kubernetes.io/name: kube-headroom
app.kubernetes.io/component: crds
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}
