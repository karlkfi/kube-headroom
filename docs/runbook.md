# Headroom operator runbook

Operating Headroom on a cluster: preflight, rollout, day-2 triage, and failure
handling. Audience: the platform/cluster operators who install and own the
controller. App teams want the [tenant guide](tenant-guide.md) instead; whether
Headroom fits a given workload is the [applicability matrix](applicability.md).
Background for every "why" here is the [design doc](design.md).

Headroom sets container **`limits.cpu`** as a function of node slack, via the
in-place pod resize subresource. It never touches requests, memory, or any other
field. It is **opt-in per namespace** and ships **`dryRun: true`** by default.

---

## Preflight

<a id="preflight"></a>
Check these before enrolling any namespace. Most are one-time cluster
properties; the ResourceQuota one is the sharp edge.

- **Kubernetes ≥ 1.35 on managed nodes.** In-place pod resize is GA at 1.35
  (the `InPlacePodVerticalScaling` gate is on by default). Older nodes cannot
  actuate a resize.
- **No static CPU/Memory Manager policy on managed nodes.** Resize is prohibited
  under static manager policies; these nodes are excluded structurally (and the
  controller handles a stray `Infeasible` defensively). NUMA-pinned training
  pools are the usual case — leave them unmanaged.
- **No Windows nodes in the managed set.** In-place resize is unsupported there.
- **ResourceQuota: `requests.cpu` only — never `limits.cpu`.** A `limits.cpu`
  quota in a managed namespace makes Headroom's raises consume tenant quota, and
  an **over-budget raise fails admission with a `403 Forbidden`** (verified,
  Phase 0 spike Q2c). Quota is delta-accounted on resize, so a within-budget
  raise silently eats quota too. Audit every managed namespace:

  ```sh
  kubectl get resourcequota -A -o json \
    | jq -r '.items[] | select(.spec.hard["limits.cpu"]) | "\(.metadata.namespace)/\(.metadata.name)"'
  ```

  Any hit must drop its `limits.cpu` hard limit (keep `requests.cpu`) before
  enrollment, or the namespace will see resize 403s.
- **LimitRange `max` caps what Headroom can set.** A namespace `max.limits.cpu`
  (or a per-container `max`) clamps the achievable ceiling. This is safe (raises
  just stop at the cap) but means the "empty-node personality" is bounded;
  document it for tenants who wonder why their limit plateaus.
- **VPA, if present, must be `controlledValues: RequestsOnly`** on managed pods.
  Default-mode VPA (`RequestsAndLimits`) scales limits and conflicts directly —
  exclude those pods. See the [tenant guide](tenant-guide.md#vpa) for the
  coexistence recipe.

---

## Install and roll out

The rollout is deliberately staged: **dry-run → observe → enforce**, one
namespace at a time.

1. **Install the CRD, RBAC, and manager.** (Deploy manifests land with the
   controller; until then, `make run` against the target context.) The
   `HeadroomConfig` singleton is named `cluster` and defaults to `dryRun: true`:

   ```sh
   kubectl apply -k config/samples   # creates HeadroomConfig/cluster (dryRun: true)
   kubectl get hcfg cluster
   ```

2. **Enroll one namespace and watch dry-run output.** Dry-run computes targets,
   writes the status annotation, and emits metrics — but issues **no** resize
   patches. This is the validation harness:

   ```sh
   kubectl label ns team-a kube-headroom.dev/mode=managed
   # give the reconciler a scheduling event or two, then inspect a pod:
   kubectl get pod -n team-a <pod> \
     -o jsonpath='{.metadata.annotations.kube-headroom\.dev/status}' | jq
   ```

   The annotation shows the `factor`, `slack`, `managedRequests`, and the
   `computedAt` timestamp the controller *would* have applied. Sanity-check that
   targets sit between each pod's request and its cap, and that
   `headroom_resizes_total{result="dry-run"}` climbs while real resize counters
   stay flat.

3. **Flip to enforcing.** When the dry-run targets look right, set `dryRun:
   false` — globally, since it is a single cluster config:

   ```sh
   kubectl patch hcfg cluster --type merge -p '{"spec":{"dryRun":false}}'
   ```

   Now watch `CPULimitAdjusted` events and `headroom_resizes_total{result="ok"}`.
   A managed pod alone on a node should climb toward allocatable; scheduling a
   neighbor should shrink it within a few seconds.

4. **Add namespaces incrementally.** Repeat step 2 per namespace. There is no
   need to re-toggle dry-run; enrollment is per-namespace via the label.

**Rollback** is always available and always safe (§ [Failure modes](#failure-modes)):
set `dryRun: true` again (stops all patching; limits freeze at their last
values), or remove the namespace label (the namespace's pods stop being managed;
their current limits stay put — Headroom never *removes* a limit).

---

## Day-2: "my pod is throttled"

The design rule is that **any throttle is explainable in two `kubectl`
commands**. The answer is never "an agent decided based on a metric you can't
see."

1. **Is the node full of *requests*?** Throttling under Headroom means the node
   is booked, so the pod's ceiling has collapsed toward its request — working as
   designed.

   ```sh
   kubectl get pod -n team-a <pod> \
     -o jsonpath='{.metadata.annotations.kube-headroom\.dev/status}' | jq
   # low "slack" + factor near 1.0  ->  node is booked; ceiling == request-ish
   ```

2. **Read the last adjustment event:**

   ```sh
   kubectl describe pod -n team-a <pod> | grep -A2 CPULimitAdjusted
   # e.g. "1500m -> 3000m (node factor 2.00, slack 8/16 cores)"
   ```

The fix is self-service and lives in the [tenant guide](tenant-guide.md): raise
the request (buys guaranteed capacity, a bigger CFS weight, *and* a bigger slack
share), or move to a less-booked pool. Deciding *what* to raise it to is VPA's
job, not Headroom's.

**The money graph:** correlate `headroom_pod_limit_cores` with
`container_cpu_cfs_throttled_periods_total`. It answers "you were throttled
because the node was 94% booked; here's who booked it."

---

## Resize outcomes

Per-pod resize results the controller handles (design §6.4), each with a distinct
`headroom_resizes_total{result=...}` label:

| Result | Meaning | Controller action |
|---|---|---|
| `ok` | resize applied, cgroup rewritten | record annotation + `CPULimitAdjusted` event |
| `dry-run` | target computed, not applied (`dryRun: true`) | annotation + metric only |
| `Deferred` | kubelet will retry (transient) | track a gauge; a *sustained* deferred count on limit-only raises is an alerting signal |
| `Infeasible` | kubelet cannot satisfy it (should be near-impossible — targets are capped at allocatable) | mark pod ineligible for `backoffPeriod`, warning event |
| `quota-denied` | `403 Forbidden` from a namespace `limits.cpu` ResourceQuota | back off like `Infeasible`; **the real fix is the preflight** — quota on `requests.cpu` only |

A rising `quota-denied` count means a managed namespace still has a `limits.cpu`
quota. Fix the quota (see [Preflight](#preflight)); do not tune around it.

---

## Metrics and dashboards

Prometheus series (design §8.1):

- `headroom_node_factor{node}` — the per-node slack factor `F = 1 + S/M`.
- `headroom_node_slack_cores{node}` — unbooked CPU on the node.
- `headroom_pod_limit_cores{pod}` — the computed/applied ceiling.
- `headroom_resizes_total{result}` — `ok` / `dry-run` / `Deferred` /
  `Infeasible` / `quota-denied`.
- `headroom_reconcile_duration_seconds`, `headroom_pods_managed`.

Ship the Grafana dashboard (`dashboards/headroom.json`, lands with observability
in Q7) that overlays `headroom_pod_limit_cores` on
`container_cpu_cfs_throttled_periods_total`.

**Alerts worth having:** sustained `result="Deferred"` (kubelet not converging),
any `result="quota-denied"` (a namespace violates the quota preflight), and
`headroom_reconcile_duration_seconds` p99 climbing (cache or API pressure).

---

## Failure modes

<a id="failure-modes"></a>
The core safety property (design §8.6): **no failure mode is worse than not
running Headroom.** Because targets are a pure function of API-server state
(requests, never usage), a stalled controller is just stale — and stale limits
are safe.

- **Controller down / paused (`dryRun`):** limits freeze at their last values.
  Frozen-generous limits on a node that then fills degrade to "generous static
  limits," and `cpu.weight` still enforces request-proportional sharing under
  contention. Frozen-tight limits on a node that then empties give unnecessary
  throttling — i.e., plain Kubernetes. Neither is worse than not running it.
- **API-server pressure:** rate limits (`perNodePatchesPerSecond`,
  `clientQPS`/`clientBurst`) bound the write load; the degraded mode is
  staleness, which is safe per above. If you must shed load fast, raise the
  deadband or set `dryRun: true`.
- **Two leaders (split brain):** leader election prevents it; even if violated,
  both replicas compute the same pure function over the same cache and issue
  convergent patches.
- **Thundering herd on restart:** the initial reconcile finds most limits
  already within deadband, so few patches fire; initial sync is jittered.

When in doubt, `dryRun: true` is the safe stop button — it never removes a limit,
it only stops changing them.
