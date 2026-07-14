# Migrating a kustomize install to Helm

Headroom's deploy artifact is now two Helm charts (`kube-headroom-crds` and
`kube-headroom`), published to `oci://ghcr.io/karlkfi/charts`. If you deployed an
earlier build with `kubectl apply -k config/default`, this guide moves that
install onto Helm without dropping your `HeadroomConfig` or disrupting the
managed workloads. Background and the deploy commands are in the
[runbook](runbook.md#install-and-roll-out).

There are two paths. **Adopt in place** keeps the running objects and is the
zero-downtime option; **uninstall/reinstall** is simpler if a brief control-plane
gap is acceptable. Either way the CRD (and therefore every `HeadroomConfig`) is
safe: the CRD chart carries `helm.sh/resource-policy: keep`.

Headroom is opt-in and defaults to `dryRun: true`, and the birth-limit webhook
is `failurePolicy: Ignore` — so even if the manager is briefly absent,
pod creation is never blocked and no workload is throttled.

## What changes

The chart reproduces the kustomize resource names exactly (the kustomize
`namePrefix: kube-headroom-` becomes the chart's fullname prefix), so
`kube-headroom-controller-manager`, `kube-headroom-webhook-service`,
`kube-headroom-mutating-webhook-configuration`, etc. keep their names. What Helm
needs is **ownership metadata** on each existing object:

- label `app.kubernetes.io/managed-by: Helm`
- annotation `meta.helm.sh/release-name: <release>`
- annotation `meta.helm.sh/release-namespace: <namespace>`

A `helm upgrade --install` refuses to take over an object that lacks these
(the classic "invalid ownership metadata" error), which is what the adopt-in-place
steps below add.

## Option A — adopt in place (no downtime)

Use the same release names and namespace the charts default to
(`kube-headroom-crds`, `kube-headroom`, namespace `kube-headroom-system`).

1. **Annotate + label the CRD for the CRD release.** The CRD is cluster-scoped;
   its release namespace is where you will install the CRD chart.

   ```sh
   kubectl label crd headroomconfigs.kube-headroom.dev \
     app.kubernetes.io/managed-by=Helm --overwrite
   kubectl annotate crd headroomconfigs.kube-headroom.dev \
     meta.helm.sh/release-name=kube-headroom-crds \
     meta.helm.sh/release-namespace=kube-headroom-system --overwrite
   ```

2. **Annotate + label the operator objects for the operator release.** Sweep the
   namespaced and cluster-scoped resources the old kustomize tree created:

   ```sh
   ns=kube-headroom-system
   # Namespaced objects in the operator namespace:
   for kv in deployment/kube-headroom-controller-manager \
             serviceaccount/kube-headroom-controller-manager \
             service/kube-headroom-controller-manager-metrics-service \
             service/kube-headroom-webhook-service \
             poddisruptionbudget/kube-headroom-controller-manager \
             role/kube-headroom-leader-election-role \
             rolebinding/kube-headroom-leader-election-rolebinding \
             networkpolicy/kube-headroom-allow-metrics-traffic \
             networkpolicy/kube-headroom-allow-webhook-traffic \
             issuer.cert-manager.io/kube-headroom-selfsigned-issuer \
             certificate.cert-manager.io/kube-headroom-serving-cert; do
     kubectl -n "$ns" label   "$kv" app.kubernetes.io/managed-by=Helm --overwrite
     kubectl -n "$ns" annotate "$kv" \
       meta.helm.sh/release-name=kube-headroom \
       meta.helm.sh/release-namespace="$ns" --overwrite
   done
   # Cluster-scoped objects (no namespace):
   for kv in clusterrole/kube-headroom-manager-role \
             clusterrolebinding/kube-headroom-manager-rolebinding \
             clusterrole/kube-headroom-metrics-auth-role \
             clusterrolebinding/kube-headroom-metrics-auth-rolebinding \
             clusterrole/kube-headroom-metrics-reader \
             mutatingwebhookconfiguration/kube-headroom-mutating-webhook-configuration; do
     kubectl label   "$kv" app.kubernetes.io/managed-by=Helm --overwrite
     kubectl annotate "$kv" \
       meta.helm.sh/release-name=kube-headroom \
       meta.helm.sh/release-namespace="$ns" --overwrite
   done
   ```

   (If you enabled the Prometheus ServiceMonitor, adopt
   `servicemonitor/kube-headroom-controller-manager-metrics-monitor` the same
   way. Skip any resource your old install didn't create.)

3. **`helm upgrade --install` both charts.** Helm now finds the ownership
   metadata and takes over the objects in place — it patches rather than
   recreates, so the manager Pods and webhook keep serving:

   ```sh
   helm upgrade --install kube-headroom-crds \
     oci://ghcr.io/karlkfi/charts/kube-headroom-crds \
     --namespace kube-headroom-system

   helm upgrade --install kube-headroom \
     oci://ghcr.io/karlkfi/charts/kube-headroom \
     --namespace kube-headroom-system
   ```

4. **Reconcile any drift.** `helm get manifest kube-headroom | kubectl diff -f -`
   surfaces fields the chart sets differently from your old overlay (image tag,
   replicas, toggles). Re-run the upgrade with the matching `--set` values until
   the diff is empty. If your overlay pinned an image, pass
   `--set image.repository=… --set image.tag=…`.

## Option B — uninstall/reinstall (simpler, brief gap)

If a short window without the manager is acceptable, delete the old objects and
install fresh. Keep the CRD so live `HeadroomConfig`s survive.

```sh
# Remove the old operator objects but NOT the CRD or the HeadroomConfig:
kubectl delete -k config/default --ignore-not-found \
  --selector 'app.kubernetes.io/name=kube-headroom' || \
  kubectl delete deployment,service,serviceaccount,poddisruptionbudget,networkpolicy \
    -n kube-headroom-system -l app.kubernetes.io/name=kube-headroom

# Then install the charts (the CRD chart adopts/keeps the existing CRD):
helm upgrade --install kube-headroom-crds \
  oci://ghcr.io/karlkfi/charts/kube-headroom-crds -n kube-headroom-system
helm upgrade --install kube-headroom \
  oci://ghcr.io/karlkfi/charts/kube-headroom -n kube-headroom-system --create-namespace
```

Your `HeadroomConfig/cluster` is untouched throughout (it's a CR, not part of the
operator release). Confirm with `kubectl get hcfg cluster` after the reinstall.

## Verify

- `helm list -n kube-headroom-system` shows both releases `deployed`.
- `kubectl get hcfg cluster` still returns your config, unchanged.
- The manager Pods are `Running` and the webhook CA bundle is injected
  (`kubectl get mutatingwebhookconfiguration
  kube-headroom-mutating-webhook-configuration -o
  jsonpath='{.webhooks[0].clientConfig.caBundle}'` is non-empty).
- A later `helm uninstall kube-headroom` removes only the operator; the CRD and
  every `HeadroomConfig` remain (`resource-policy: keep`).
