# Headroom v0.2.0 — quiet-node and event fixes

*2026-11*

[v0.2.0](https://github.com/karlkfi/kube-headroom/releases/tag/v0.2.0) fixes
the defects found by auditing v0.1.0 and picks up the Go, `x/net`, grpc and otel
vulnerability fixes. Everyone on v0.1.0 should upgrade. The
[release notes](https://github.com/karlkfi/kube-headroom/releases/tag/v0.2.0)
have the full list, the upgrade steps and what was tested.

## What changes for you

- **Limits now follow your settings on quiet nodes.** In v0.1.0, flipping
  `dryRun`, enrolling a namespace, setting `max-cpu` or adding a LimitRange did
  nothing until another pod landed on the node. Now each of those takes effect
  within seconds. This is a behaviour change: a config edit can resize every
  managed pod in the cluster, bounded only by the existing rate limits.
- **Events reach the API server.** v0.1.0's role granted the wrong API group, so
  `CPULimitAdjusted` and the refusal events were rejected on every install. The
  upgrade changes the manager's ClusterRole to fix this.
- **Full nodes shrink the pods they should.** Pods whose memory request equals
  their limit, and pods with native sidecars, used to be refused on resize. They
  now resize.
- **LimitRanges cap raises instead of refusing them.** A refusal now says which
  object refused it, through two new `headroom_resizes_total` results:
  `limitrange-denied` and `forbidden`. Other 403s no longer count as
  `quota-denied`, so check any alert on that label.
- **Slack matches what the scheduler books.** Pod overhead and init-container
  peaks now count, so limits drop on nodes running those pods.
- **`webhook.enable=false` works.** It used to leave the controller
  crash-looping.

## Pods that Headroom no longer manages

Pods with pod-level `spec.resources` are now left alone on every Kubernetes
version, because their resizes are refused or capped depending on the version.
They get no birth limit and no resizes, so one with no pod-level CPU limit and a
container with no CPU limit has no CPU ceiling at all. The
[runbook](../runbook.md#limitrange-default) covers the optional ways to close
that gap. A pod Headroom had already resized keeps that limit until it is
recreated.

## Known limits

A pod refused a resize keeps its share of the node while it is refused, so that
slack sits unused until the cause is fixed. Under a fractional
`maxLimitRequestRatio`, the cap is safe rather than tight, and a small request
can hold a pod near its request. Q70 in the [backlog](../queue/) tracks that.

## Upgrade

```sh
helm upgrade --install kube-headroom-crds \
  oci://ghcr.io/karlkfi/charts/kube-headroom-crds --version 0.2.0

helm upgrade --install kube-headroom \
  oci://ghcr.io/karlkfi/charts/kube-headroom --version 0.2.0 \
  --namespace kube-headroom-system --create-namespace
```

Before enrolling more namespaces, run `scripts/preflight-namespaces.sh`, new in
this release. It reports `limits.cpu` quotas and LimitRange caps. The API is
still `v1alpha1`, and the `HeadroomConfig` schema did not change in this release.
