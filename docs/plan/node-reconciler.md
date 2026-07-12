# Plan: Node reconciler (Q4)

Wire the pure `internal/policy` core into a controller-runtime manager. This is
the heart of the controller (design doc §6.2).

## Intent

Reconcile at the **node** granularity — every scheduling event on a node
invalidates every managed limit on that node, so per-pod reconcile would thrash.
Recompute targets from API-server state (requests, not usage) and apply them via
the in-place resize subresource.

## Scope

- **Informers:** pod informer indexed by `spec.nodeName`; node informer for
  allocatable changes and deletion cleanup. One full pod informer with a cache
  transform stripping unneeded fields (§7) — requests are needed for *all* pods
  to compute slack, but the rest of the pod object is not.
- **Enqueue → node key** on: pod bound, pod deleted/terminal, pod CPU request
  changed. Debounce node keys (`debouncePeriod`, default 2s) so a rollout
  landing N pods computes once.
- **Reconcile(node):** list cached pods → `policy.Compute` → for each
  `Decision{Apply:true}` split the pod target across containers pro-rata by CPU
  request → patch `pods/resize` via SSA, fieldManager `headroom`, through a
  per-node token bucket + global client QPS budget.
- **Resize outcome handling (§6.4):** `Infeasible` → mark ineligible for a
  backoff, warn; `Deferred` → track a gauge, kubelet retries; conflict/stale
  generation → requeue; never touch requests, memory, or any field but
  `limits.cpu` of eligible containers.

## Depends on / integrates

- Q1 scaffold (manager, CRD, RBAC) must land first.
- Q5 eligibility gates which pods are `Managed` in the policy input, and dry-run
  short-circuits the patch step.
- Q7 hangs annotations/events/metrics off each applied decision.

## Acceptance criteria

- On a kind ≥1.35 cluster: a managed pod alone on a node runs at ~allocatable;
  scheduling a neighbor shrinks its limit within ~5s; removing the neighbor
  restores it.
- Steady state (no scheduling churn) issues **zero** patches (deadband holds).
- Controller kill mid-reconcile leaves the cluster in a safe state (frozen
  limits, §8.6).
- A burst of N pods onto one node collapses to a single recompute (debounce).
