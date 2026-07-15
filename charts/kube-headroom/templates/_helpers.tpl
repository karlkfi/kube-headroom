{{/*
Chart name (overridable), truncated to the 63-char DNS label limit.
*/}}
{{- define "kube-headroom.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. This is the prefix for every rendered resource,
replacing the kustomize `namePrefix: kube-headroom-`. With the default release
name `kube-headroom` (see the Makefile deploy targets) this resolves to
`kube-headroom`, so resources are named `kube-headroom-<component>` — matching
the names the e2e suite and runbook expect.
*/}}
{{- define "kube-headroom.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "kube-headroom.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every resource's metadata.
*/}}
{{- define "kube-headroom.labels" -}}
helm.sh/chart: {{ include "kube-headroom.chart" . }}
{{ include "kube-headroom.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels. These are immutable on a Deployment selector, so they must NOT
include release- or version-specific values. They match the kustomize base
labels (`control-plane` + `app.kubernetes.io/name`) so `-l control-plane=controller-manager`
and the metrics/webhook Services keep selecting the manager Pods.
*/}}
{{- define "kube-headroom.selectorLabels" -}}
control-plane: controller-manager
app.kubernetes.io/name: {{ include "kube-headroom.name" . }}
{{- end -}}

{{/*
ServiceAccount name used by the manager Pods and RBAC bindings.
*/}}
{{- define "kube-headroom.serviceAccountName" -}}
{{- printf "%s-controller-manager" (include "kube-headroom.fullname" .) -}}
{{- end -}}

{{/*
Manager container image reference. image.digest (immutable pin) takes
precedence over image.tag; image.tag defaults to the chart appVersion.
*/}}
{{- define "kube-headroom.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Name of the Secret holding the webhook server's serving cert, and the single
place that validates the webhook TLS wiring. Only evaluated when the webhook is
enabled.

  - cert-manager on: cert-manager issues the `webhook-server-cert` Secret (see
    certmanager.yaml) and injects the CA into the MutatingWebhookConfiguration.
  - cert-manager off: the operator brings its own. `webhook.certSecretName` names
    a pre-created TLS Secret (keys tls.crt/tls.key) in the release namespace,
    mounted in place of the cert-manager Secret, and `webhook.caBundle` supplies
    the CA for the webhook clientConfig (see webhook.yaml).

Rendering fails fast when the webhook is on, cert-manager is off, and no BYO cert
Secret is given — otherwise the manager Pod would block forever on a Secret that
nothing creates.
*/}}
{{- define "kube-headroom.webhookCertSecretName" -}}
{{- if .Values.certmanager.enable -}}
webhook-server-cert
{{- else if .Values.webhook.certSecretName -}}
{{- .Values.webhook.certSecretName -}}
{{- else -}}
{{- fail "webhook.enable=true with certmanager.enable=false requires a bring-your-own cert: set webhook.certSecretName to a pre-created TLS Secret (and webhook.caBundle so the MutatingWebhookConfiguration trusts it). Otherwise set certmanager.enable=true or webhook.enable=false." -}}
{{- end -}}
{{- end -}}
