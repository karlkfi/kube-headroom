#!/usr/bin/env bash
# helm-render-matrix.sh — render the operator chart across the values toggle
# matrix so CI exercises more than just default values.
#
# The kustomize→Helm migration acceptance criterion requires every feature
# toggle to render a valid, installable manifest set independently — webhook
# off, prometheus off, network-policy off, and cert-manager off. The last one
# (Q30) is the trap this guards: with cert-manager off but the webhook on, the
# manager must mount a bring-your-own cert Secret and the
# MutatingWebhookConfiguration must carry a caBundle, or the Pod blocks forever
# on a never-created Secret. This script renders each combination (piping to
# kubeconform when present) and asserts the no-BYO-cert case fails fast rather
# than shipping an unstartable Deployment.
set -euo pipefail

HELM="${HELM:-helm}"
OPERATOR_CHART="${OPERATOR_CHART:-charts/kube-headroom}"
OPERATOR_RELEASE="${OPERATOR_RELEASE:-kube-headroom}"
HELM_NAMESPACE="${HELM_NAMESPACE:-kube-headroom-system}"

if command -v kubeconform >/dev/null 2>&1; then
  validate() { kubeconform -strict -ignore-missing-schemas -summary; }
else
  echo "kubeconform not installed; rendering only (CI validates with kubeconform)."
  validate() { cat >/dev/null; }
fi

FAIL=0

# render "<label>" [--set ...] — must render and validate cleanly.
render() {
  local label="$1"
  shift
  printf '  render: %s\n' "$label"
  if ! "$HELM" template "$OPERATOR_RELEASE" "$OPERATOR_CHART" -n "$HELM_NAMESPACE" "$@" | validate; then
    echo "  FAILED: $label" >&2
    FAIL=1
  fi
}

render "defaults (all toggles on)"
render "webhook off" --set webhook.enable=false
render "cert-manager off + webhook BYO cert" \
  --set certmanager.enable=false \
  --set webhook.certSecretName=byo-webhook-tls \
  --set webhook.caBundle=TEVTVCBDQQ==
render "cert-manager off + webhook off" \
  --set certmanager.enable=false --set webhook.enable=false
render "prometheus on" --set prometheus.enable=true
render "network-policy off" --set networkPolicy.enable=false
render "metrics cert-manager TLS" --set metrics.certManagerTLS=true
# The operator chart never renders the CRD (it ships as the standalone
# kube-headroom-crds chart). headroomConfig.create renders the singleton CR as a
# normal resource, against that already-registered CRD.
render "headroomConfig on (CRD pre-installed)" --set headroomConfig.create=true

# Negative case: cert-manager off + webhook on with NO BYO cert must FAIL to
# render — the guard (kube-headroom.webhookCertSecretName) that stops the chart
# from shipping a Deployment that can never start.
printf '  render (expect failure): cert-manager off + webhook on, no BYO cert\n'
if "$HELM" template "$OPERATOR_RELEASE" "$OPERATOR_CHART" -n "$HELM_NAMESPACE" \
    --set certmanager.enable=false >/dev/null 2>&1; then
  echo "  FAILED: expected a render error for cert-manager off + webhook on without a BYO cert, but rendering succeeded" >&2
  FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
  echo "helm render matrix: one or more cases failed" >&2
  exit 1
fi
echo "helm render matrix: all cases OK"
