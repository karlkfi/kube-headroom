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
- **Distribution: OCI on ghcr.io, charts under a dedicated sub-namespace.**
  Image at `ghcr.io/karlkfi/kube-headroom`; charts pushed to
  `oci://ghcr.io/karlkfi/charts` (→ `…/charts/kube-headroom`,
  `…/charts/kube-headroom-crds`) so image and chart never share a package
  coordinate. Consumers `helm install … oci://…`. No gh-pages index to
  maintain. (`IMG` is still the placeholder `controller:latest`; pinning it to
  the ghcr path is part of this work.)
- **Makefile deploy targets are rewritten to Helm; the kustomize deploy path is
  deleted outright** — no deprecation window (see Makefile below).

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
  - **Delete the kustomize deploy targets and replace their names with Helm
    implementations:** `install` → `helm upgrade --install` the CRD chart;
    `deploy` → `helm upgrade --install` the operator chart (with
    `--set image.tag=…` replacing `kustomize edit set image`);
    `undeploy` → `helm uninstall`; `build-installer` → `helm template` the
    rendered YAML into `dist/`. No `kustomize build`-based deploy path survives;
    drop the `KUSTOMIZE` binary dep from every deploy target (controller-gen
    still generates manifests into `config/**`, `helm-sync` copies them).
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

## CRD API versioning (the future v1beta1 bump)

The API version is **not** encoded in the chart name or path. A CRD is
identified by group+kind (`headroomconfigs.kube-headroom.dev`) and one CRD
object serves many API versions at once via `spec.versions[]` (exactly one
`storage: true`). Moving v1alpha1 → v1beta1 is an *update to that same CRD*,
shipped as a `helm upgrade` of the single `kube-headroom-crds` chart — not a new
chart.

- A `-v1alpha1`-suffixed chart would be actively harmful: two charts both
  templating `headroomconfigs.kube-headroom.dev` collide on Helm's
  `meta.helm.sh/release-*` ownership annotations (only one release may own an
  object), and `resource-policy: keep` guarantees the old chart's CRD lingers to
  trigger exactly that conflict.
- The version axis that moves is the chart's own **Chart version / appVersion**
  (semver), bumped when the schema changes; consumers pin a chart version.
- What the bump will actually need (single crds chart + operator, tracked as a
  follow-up — **out of scope for Q21**, which ships v1alpha1 only):
  - add `v1beta1` to `spec.versions` as served + storage, keep `v1alpha1` served
    with `deprecated: true` for a release or two;
  - a **conversion webhook** (`spec.conversion.strategy: Webhook`) served by the
    manager and CA-injected like the admission webhook, if the schemas aren't
    structurally identical;
  - a **storage-version migration** (re-write stored objects to v1beta1) before
    dropping `v1alpha1` from `status.storedVersions` and the served set.

## Depends on / sequencing

- The `config/`-touching items this migration builds on have **all landed** —
  Q17 (RBAC narrowing), Q18 (multiplier bounds), Q19 (webhook NetworkPolicy),
  Q20 (prod defaults). The chart ports the current `config/` tree; no further
  base churn is expected, so the migration can start when scheduled.
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

1. Confirm the ghcr owner/path — the plan assumes image
   `ghcr.io/karlkfi/kube-headroom` and charts `oci://ghcr.io/karlkfi/charts`.
   Override the owner or the `charts` sub-namespace here if you want something
   else; nothing is pinned yet.
