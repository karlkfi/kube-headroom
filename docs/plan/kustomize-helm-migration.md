# Plan: Migrate packaging from Kustomize to Helm (Q21)

Replace the `config/` kustomize tree as the **deployment** artifact with a Helm
chart, so operators install and upgrade with `helm` and tune the deployment
through `values.yaml` instead of kustomize overlays. The chart must reach
parity with today's `make deploy` and stay in sync with kubebuilder-generated
manifests.

## Intent

Kustomize is fine for a fixed in-repo overlay but weak for redistribution:
every consumer knowledge-forks the tree to change the image, namespace,
replicas, or feature toggles. Helm gives us a versioned, publishable artifact
with real values, conditionals, and an upgrade lifecycle. The constraint is
that several manifests (`config/crd/bases/*`, `config/rbac/role.yaml`,
`config/webhook/manifests.yaml`) are **generated** by `make manifests generate`
and must never be hand-edited — the chart cannot be a hand-forked copy that
drifts from the markers.

## Key decision: generator strategy (resolve first)

Two viable shapes; pick one before building anything:

- **A — kubebuilder helm plugin as generator (recommended).**
  `kubebuilder edit --plugins=helm/v1-alpha` renders a chart from `config/`.
  `config/` stays as the generation source (markers → `make manifests` →
  chart), so the "never hand-edit generated" rule holds and CRD/RBAC/webhook
  stay authoritative. Trade: kustomize doesn't fully disappear — it becomes the
  chart's input, not the deploy path. Less idiomatic templating, but
  low-maintenance.
- **B — hand-authored chart under `charts/kube-headroom/`, delete `config/`.**
  Fully idiomatic Helm (helpers, rich conditionals), but we lose kubebuilder's
  regeneration and must build a pipeline to copy the generated CRD/RBAC/webhook
  into the chart on every `make manifests`. Higher control, higher upkeep.

Recommendation: **A**, refined with a real `values.yaml` for the toggles. This
plan assumes A; Karl to confirm (see Open questions).

## Scope

- **Chart skeleton:** `Chart.yaml` (semver, appVersion = manager image tag),
  `values.yaml`, `templates/`, `_helpers.tpl` with a `fullname`/labels helper.
  Location `dist/chart/` (plugin default) or `charts/kube-headroom/` — decide
  with the hosting question.
- **Values surface** (replacing kustomize edits): `image.repository`/`tag`/
  `pullPolicy`, `replicas`, `resources`, `nodeSelector`/`tolerations`/
  `affinity`, and feature toggles `webhook.enable`, `certmanager.enable`,
  `prometheus.enable`, `networkPolicy.enable`, `crds.install`/`crds.keep`. Use
  `.Release.Namespace` instead of the hardcoded `kube-headroom-system`, and the
  fullname helper instead of the `kube-headroom-` nameprefix.
- **CRD lifecycle (the hard part):** Helm's `crds/` dir installs but never
  upgrades or deletes. Put the CRD in `templates/` with
  `helm.sh/resource-policy: keep` and a `crds.install` gate so `helm upgrade`
  can roll new schema versions without dropping existing `HeadroomConfig`s.
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
- **Makefile:** add `helm-lint`/`helm-template`/`helm-package` targets; decide
  the fate of `deploy`/`install`/`undeploy`/`build-installer` — either retire
  them or repoint `build-installer` to `helm template`.
- **CI:** `helm lint` + `helm template | kubeconform` (optionally
  chart-testing `ct`); switch the e2e path to `helm install`.
- **Distribution:** `helm package` + publish (OCI to ghcr.io, or a gh-pages
  helm repo).
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

- `helm install` on a fresh kind ≥1.35 cluster brings up the manager, CRD,
  RBAC, webhook (working cert), metrics, and admits the sample singleton CR —
  behavioral parity with `make deploy`.
- `helm upgrade` across a chart version preserves existing `HeadroomConfig`s
  (CRD `keep` policy verified).
- Every toggle works independently: webhook off, prometheus off,
  network-policy off, cert-manager off — each renders a valid, installable
  manifest set.
- `helm template` output passes `kubeconform`, and a diff against the old
  `kustomize build config/default` shows only intended differences (namespace,
  naming, values).
- `make manifests generate` still regenerates CRD/RBAC/webhook and the chart
  picks the changes up with no hand-edit drift.
- CI lints and renders the chart on every PR; e2e installs via Helm.
- The migration doc lets an operator move an existing install to Helm without
  dropping managed CRs.

## Open questions (for Karl)

1. Generator strategy **A** (kubebuilder helm plugin, keep `config/` as input)
   vs **B** (hand-authored chart, delete `config/`)?
2. Chart hosting: OCI registry (ghcr.io) or a gh-pages Helm repo?
3. Fully retire the kustomize `deploy`/`install` targets, or keep them for
   local dev alongside Helm during a deprecation window?
