# Headroom v0.1.0 — first public release

*2026-07*

Headroom is now open source under the
[Apache License 2.0](https://github.com/karlkfi/kube-headroom/blob/main/LICENSE),
and [v0.1.0](https://github.com/karlkfi/kube-headroom/releases/tag/v0.1.0) is
the first installable release.

Headroom is a Kubernetes controller that dynamically sets container **CPU
limits** as a function of node slack: on an empty node a pod's limit approaches
the node's allocatable CPU, and as the node fills with requests, limits converge
toward each pod's request. Limits are derived from **requests** (booked
capacity), not live usage, so they change only on scheduling events —
deterministic, low-churn, and debuggable — and are applied through the GA
in-place pod resize subresource, so nothing restarts. The
[design doc](../design.md) is the source of truth.

## What's in v0.1.0

- **The controller** — node-level reconciliation on scheduling events, resize
  actuation with deadband/hysteresis, debounce, and rate limits.
- **`HeadroomConfig` CRD** (`v1alpha1`, cluster-scoped singleton) — one object
  holds the whole policy: burst floor, max multiplier, damping, and the
  namespace opt-in selector.
- **Opt-in birth-limit admission webhook** — new pods in enrolled namespaces
  start with a computed limit instead of waiting for the first reconcile.
- **Two Helm charts** as OCI artifacts — `kube-headroom-crds` (cluster-wide,
  CRDs survive uninstall) and `kube-headroom` (the namespaced operator).
- **Multi-arch image** — `ghcr.io/karlkfi/kube-headroom:0.1.0`
  (linux/amd64, linux/arm64).
- **Observability** — per-pod annotations, events, and Prometheus metrics, so
  every limit change is explainable from observable inputs.

## Dry-run by default

A fresh install issues **no resizes**. `HeadroomConfig` defaults to
`dryRun: true`: Headroom computes targets, annotates pods, and emits metrics —
so you can watch exactly what it *would* do against your real workloads — and
only starts patching limits when you flip the switch. The
[runbook](../runbook.md) covers preflight checks and the rollout sequence.

## Install

Requires Kubernetes ≥ 1.35 (in-place pod resize GA) and cert-manager (webhook
serving cert).

```sh
# CRD chart — cluster-wide, on its own lifecycle:
helm upgrade --install kube-headroom-crds \
  oci://ghcr.io/karlkfi/charts/kube-headroom-crds --version 0.1.0

# Operator chart — into its namespace:
helm upgrade --install kube-headroom \
  oci://ghcr.io/karlkfi/charts/kube-headroom --version 0.1.0 \
  --namespace kube-headroom-system --create-namespace
```

Then create the `HeadroomConfig` singleton and enroll a namespace — the
[runbook](../runbook.md) walks through it step by step.

## Should you run it?

Headroom earns its complexity only where CPU **ceilings are actually
required** — hostile or contractual multi-tenancy, bounding the blast radius of
runaway workloads. On a trusted single-tenant cluster, omitting CPU limits gets
you most of the benefit with zero moving parts. The
[applicability matrix](../applicability.md) is honest about when *not* to use
it; app teams whose namespaces get enrolled should start with the
[tenant guide](../tenant-guide.md).

## What's next

The API is `v1alpha1` — the schema may still change between releases.
Priorities live in the
[backlog](https://github.com/karlkfi/kube-headroom/blob/main/docs/STATUS.md);
promotion to `v1beta1` is gated on the spec surviving a release cycle
unchanged. Found a bug or a sharp edge? Open an
[issue](https://github.com/karlkfi/kube-headroom/issues).
