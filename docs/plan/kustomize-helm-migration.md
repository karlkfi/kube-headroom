# Plan: Migrate packaging from Kustomize to Helm (Q21)

Replace the `config/` kustomize tree as the **deployment** artifact with two
Helm charts — a **CRD chart** (`kube-headroom-crds`) and an **operator chart**
(`kube-headroom`) — published to **ghcr.io as OCI artifacts**. Operators install
and upgrade with `helm` and tune the deployment through `values.yaml` instead of
kustomize overlays. The charts must reach parity with today's `make deploy`, the
Makefile deploy targets are rewritten to drive `helm`, and the charts stay in
sync with kubebuilder-generated manifests.

## Intent

Kustomize is fine for a fixed in-repo overlay but weak for redistribution:
every consumer knowledge-forks the tree to change the image, namespace,
replicas, or feature toggles. Helm gives us a versioned, publishable artifact
with real values, conditionals, and an upgrade lifecycle. The constraint is
that several manifests (`config/crd/bases/*`, `config/rbac/role.yaml`,
`config/webhook/manifests.yaml`) are **generated** by `make manifests generate`
and must never be hand-edited — the chart cannot be a hand-forked copy that
drifts from the markers.

## Decisions (settled)

- **Two hand-authored charts** under `charts/`: `kube-headroom-crds` and
  `kube-headroom`. Splitting CRDs into their own chart lets a cluster admin
  install/upgrade the schema once, cluster-wide, on a lifecycle independent of
  the (namespaced) operator release — the standard pattern for operators whose
  CRDs outlive any single install. It also sidesteps Helm's `crds/`-dir
  limitation (see CRD lifecycle below).
- **This rules out the kubebuilder `helm/v1-alpha` plugin**, which emits a
  single chart with CRDs under `templates/crd`. So the charts are hand-authored
  under `charts/`, but `config/` is **kept only as the generation source**:
  `make manifests generate` (controller-gen) still writes
  `config/crd/bases/*`, `config/rbac/role.yaml`, and
  `config/webhook/manifests.yaml`, and a `make` sync step copies those into the
  charts. Generated files are never hand-edited — the sync keeps the
  "authoritative markers" rule intact while the deploy path becomes Helm.
- **Distribution: OCI on ghcr.io.** `helm push` both charts to
  `oci://ghcr.io/karlkfi/charts`; consumers `helm install ... oci://…`. No
  gh-pages index to maintain.
- **Makefile deploy targets are rewritten to Helm** (kustomize deploy path
  retired — see Makefile below).

## Scope

- **`charts/kube-headroom-crds/`:** the `HeadroomConfig` CRD only, as a normal
  templated resource (not under `crds/`) carrying
  `helm.sh/resource-policy: keep`, so `helm upgrade` rolls new schema versions
  and `helm uninstall` never drops live CRs. Minimal `values.yaml` (`keep`
  toggle). `make manifests` output at `config/crd/bases/*` is synced in.
- **`charts/kube-headroom/`:** the operator — `Chart.yaml` (semver, appVersion =
  manager image tag), `values.yaml`, `templates/`, `_helpers.tpl` with a
  `fullname`/labels helper. Optionally declares `kube-headroom-crds` as an
  **optional dependency** gated by `crds.install` (default `false`), so
  admins install the CRD chart separately by default but a single
  `helm install` can do both when wanted.
- **Values surface** (operator chart, replacing kustomize edits):
  `image.repository`/`tag`/`pullPolicy`, `replicas`, `resources`,
  `nodeSelector`/`tolerations`/`affinity`, feature toggles `webhook.enable`,
  `certmanager.enable`, `prometheus.enable`, `networkPolicy.enable`, and
  `crds.install` (pull in the CRD subchart). Use `.Release.Namespace` instead of
  the hardcoded `kube-headroom-system`, and the fullname helper instead of the
  `kube-headroom-` nameprefix.
- **Webhook + cert-manager:** template the Issuer/Certificate and the
  `cert-manager.io/inject-ca-from` annotation on the webhook configs, all
  behind `webhook.enable`/`certmanager.enable`. Preserve the Q19 fix
  (apiserver→webhook ingress) when porting `allow-webhook-traffic`.
- **RBAC + SA + leader election:** template from `config/rbac`. Preserve the
  Q17 narrowing (`get;list;watch` + `/status` on headroomconfigs) — port the
  generated `role.yaml`, don't widen it.
- **Metrics:** `metrics_service`, the ServiceMonitor (gated on
  `prometheus.enable`, requires Prometheus-Operator CRDs), and the
  `allow-metrics-traffic` NetworkPolicy (gated on `networkPolicy.enable`).
- **PDB** and the manager Deployment, incl. the Q20 prod logging defaults.
- **Sample CR:** keep `config/samples` out of the chart by default; optionally
  expose `headroomConfig.create` rendering the `name: cluster` singleton.
- **Makefile (rewrite deploy path to Helm):**
  - `HELM ?=` pinned via `tools/` like the other build tools; add a
    `helm-sync` target that copies generated CRD/RBAC/webhook manifests from
    `config/**` into the two charts (runs after `manifests generate`).
  - `helm-lint` → `helm lint` both charts; `helm-template` →
    `helm template | kubeconform`; `helm-package` → package both;
    `helm-push` → `helm push` both to `oci://ghcr.io/karlkfi/charts`.
  - **Repoint the existing targets:** `install` → `helm upgrade --install` the
    CRD chart; `deploy` → `helm upgrade --install` the operator chart (with
    `--set image.tag=…` replacing `kustomize edit set image`);
    `undeploy` → `helm uninstall`; `build-installer` → `helm template` the
    rendered YAML into `dist/`. Drop the `KUSTOMIZE` binary dep from the deploy
    targets (controller-gen still generates; kustomize is no longer on the
    deploy path).
- **CI:** `helm lint` + `helm template | kubeconform` on every PR (optionally
  chart-testing `ct`); switch the e2e path to `helm install`; a release job
  `helm push`es both charts to ghcr on tag.
- **Distribution: OCI on ghcr.io.** Both charts pushed to
  `oci://ghcr.io/karlkfi/charts`; `helm push` authenticates with
  `GITHUB_TOKEN`/`packages: write`. Document the `helm install oci://…` command
  in the runbook. No chart index/gh-pages to maintain.
- **Migration/adoption doc:** how an existing kustomize-deployed cluster moves
  to Helm — adopt in place via `meta.helm.sh/release-*` labels +
  `app.kubernetes.io/managed-by: Helm`, or uninstall/reinstall (the CRD keep
  policy protects live CRs).

## Depends on / sequencing

- Land the open `config/`-touching items first to avoid rebasing the chart
  onto a moving base: Q17 (RBAC narrowing), Q18/Q19 (webhook/network-policy,
  PRs #33/#32 in flight), Q20 (prod defaults). Start after they settle.
- Design context: `docs/design.md` (§7 informers, §9.3 singleton). Install
  docs to update: `docs/runbook.md`, `README`, and the `config/` layout note
  in `CLAUDE.md`.

## Acceptance criteria

- On a fresh kind ≥1.35 cluster, `helm install` of the CRD chart then the
  operator chart brings up the manager, RBAC, webhook (working cert), metrics,
  and admits the sample singleton CR — behavioral parity with the old
  `make deploy`. The rewritten `make install`/`make deploy` do this end to end.
- `helm upgrade` of the operator across a chart version, and of the CRD chart
  across a schema version, both preserve existing `HeadroomConfig`s (CRD `keep`
  policy verified); `helm uninstall` of the operator leaves CRs and CRD intact.
- Both charts push to and install from `oci://ghcr.io/karlkfi/charts`.
- Every toggle works independently: webhook off, prometheus off,
  network-policy off, cert-manager off — each renders a valid, installable
  manifest set.
- `helm template` output passes `kubeconform`, and a diff against the old
  `kustomize build config/default` shows only intended differences (namespace,
  naming, values).
- `make manifests generate && make helm-sync` regenerates CRD/RBAC/webhook and
  the charts pick the changes up with no hand-edit drift (CI fails if `helm-sync`
  would produce a diff).
- CI lints and renders both charts on every PR; e2e installs via Helm.
- The migration doc lets an operator move an existing install to Helm without
  dropping managed CRs.

## Open questions (for Karl)

1. OCI namespace — `oci://ghcr.io/karlkfi/charts` assumed; confirm the exact
   repo path and whether the charts share it with (or sit beside) the manager
   image `ghcr.io/karlkfi/kube-headroom`.
2. Keep the retired kustomize `deploy`/`install` invocable during a deprecation
   window, or delete them outright once Helm parity is verified?
