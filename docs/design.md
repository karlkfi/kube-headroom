# Headroom: Dynamic CPU Limits Proportional to Node Slack

**Status:** Adopted — living design doc and source of truth for `kube-headroom`.
**Project name:** Headroom · **Repo:** `kube-headroom` (disambiguates from headroom.js; improves searchability)
**Author:** Karl (design developed with Claude)
**Date:** 2026-07-11 · **Adopted:** 2026-07-12 (Q9)
**Scope:** This is the architectural design. Prioritized work is tracked in
[`STATUS.md`](STATUS.md); in-flight plan docs live under [`plan/`](plan/). User
docs: [runbook](runbook.md), [tenant guide](tenant-guide.md), [applicability
matrix](applicability.md).

> **Note on prefixes.** The label/annotation prefix is `kube-headroom.dev`,
> defined once as `v1alpha1.GroupName` (§9.1). Earlier drafts used a
> `headroom.io/*` placeholder; every key below has been updated to the adopted
> prefix and the enum-keyword `mode` label that shipped (Q10).

---

## 1. Summary

Headroom is a Kubernetes controller that dynamically sets container CPU **limits** as a function of how oversubscribed a node is, recomputed whenever a pod is scheduled onto or removed from the node. On an empty node, a pod's limit approaches the node's allocatable CPU (no pointless throttling). As the node fills with requests, limits converge toward each pod's request (predictable fair sharing). The limit is computed from **requests** (booked capacity), not live usage, so it changes only on scheduling events — deterministic, low-churn, and debuggable.

Core formula (proportional policy):

```
slack(node)   = allocatable_cpu − Σ requests(all pods on node)
limit(pod_i)  = request_i + slack × (request_i / Σ requests(managed pods))
              = request_i × (1 + slack / Σ requests(managed pods))
```

with a per-pod floor and cap (Section 5.3). The mechanism for applying the limit is the **in-place pod resize subresource**, which is GA in Kubernetes 1.35 (stable, enabled by default; CPU resizes apply via a cgroup write with no container restart).

**Not a fair-sharing implementation.** The kernel already provides work-conserving, request-proportional fair sharing via cgroup `cpu.weight` (which CPU requests map to; the weight-proportional model is unchanged under EEVDF, CFS's successor in kernels 6.6+). Headroom deliberately builds nothing there. The only thing that throttles is the quota (`cpu.max`, i.e., the limit), and Headroom answers exactly one question the kernel cannot: *what should that ceiling be right now?* If your cluster doesn't need ceilings at all, don't run Headroom — omit limits instead (§8.5).

**Not DRA.** The original idea suggested DRA, but DRA is the wrong primitive here — see Section 4.2 for the evaluation. In-place resize is the correct, now-GA mechanism, and it keeps the pod spec as the source of truth (critical for debuggability).

---

## 2. Problem statement

Static CPU limits force a bad choice on users of multi-tenant clusters:

1. **Set a tight limit** → the pod is throttled by CFS quota even when the node is idle. CFS throttling is notoriously hard to debug (latency spikes with no visible resource pressure; `container_cpu_cfs_throttled_periods_total` is the only tell).
2. **Set a generous limit or none** → no isolation ceiling; one tenant's spike degrades neighbors, and there is no predictable behavior under contention.
3. **Set requests == limits (Guaranteed)** → wastes the gap between average and peak usage; nodes bin-pack poorly.

The kernel already solves part of this: CPU **requests** map to cgroup `cpu.weight` (CFS shares), which provides work-conserving, proportional-to-request fair sharing under contention *without any throttling*. The only thing that throttles is the quota (`cpu.max`), i.e., the limit. So the real question is: **what should the ceiling be at any given moment?**

Headroom's answer: the ceiling should be your request plus your proportional share of the node's *unbooked* capacity. When nothing else is booked, your ceiling is effectively the whole node. When the node is fully booked, your ceiling is your request — exactly what you paid for, and exactly what `cpu.weight` would give you under full contention anyway. The limit and the weight tell the same story at both extremes, which makes behavior predictable and incentives coherent.

### 2.1 Incentive design goal

The scheme should make honest, adequate requests the dominant strategy:

- **Raising your request** buys three things at once: more guaranteed schedulable capacity, a larger CFS weight under contention, and a larger share of node slack (higher dynamic limit). If your workload is throttled at the current dynamic limit, the fix is visible and self-service: raise your request.
- **Lowballing your request** is not punished when nodes are empty (the floor and the large slack multiplier still give you room), but it stops paying off as nodes fill — your ceiling shrinks toward your (small) request. There is no way to get sustained large CPU by requesting little.
- **Overstating your request** costs you: it consumes your namespace ResourceQuota and makes your pods harder to schedule. Self-correcting.

---

## 3. Goals and non-goals

### Goals

- Eliminate throttling attributable to unused node capacity: if CPU is unbooked, someone can burst into it.
- Provide predictable, request-proportional fair sharing on contended nodes for multi-tenant clusters.
- Recompute limits on scheduling events (pod add/remove/resize on a node), not on usage fluctuations.
- Keep the pod spec truthful: `kubectl get pod` must show the actual enforced limit. No hidden node-agent cgroup state.
- First-class debuggability: every limit change is explainable from observable inputs (annotation + event + metrics).
- Low API-server overhead via hysteresis, debouncing, and rate limiting.
- Opt-in per namespace; safe to run alongside unmanaged workloads on the same nodes.

### Non-goals (v1)

- Memory management of any kind. Memory resize can restart containers or OOM; out of scope permanently unless revisited.
- Usage-based reclamation (redistributing *requested-but-idle* CPU via limits). The kernel already lets actual usage flow to whoever needs it, under each pod's ceiling; chasing usage would add churn for little gain. Possible future mode.
- Managing Guaranteed QoS pods (impossible: resize cannot change QoS class, and Guaranteed requires requests == limits).
- Scheduling changes. Headroom never affects where pods land; it only adjusts limits after placement.
- Per-tenant (namespace-level) slack weighting. Noted as future work in Section 11.
- Windows nodes (in-place resize unsupported) and nodes using static CPU/Memory Manager policies (resize prohibited).

### 3.1 Scope philosophy: why CPU-only, permanently

A natural question is whether this should grow into a unified resource controller (CPU + memory + GPU + ephemeral storage). It should not, for a mechanical reason: the safety invariant (§8.6 — "no failure mode worse than not running Headroom") exists *only* because CPU is compressible. Exceeding a CPU ceiling throttles; exceeding the others kills:

- **Memory** limits are a ratchet, not a dial — raising is free, but shrinking below current usage is best-effort (kubelet skips the resize if usage exceeds the target) and the failure mode is an OOM kill. "Limit tracks unused capacity" is incoherent for a non-reclaimable resource.
- **GPU** has no runtime burst mechanism: devices bind at scheduling (device plugin / DRA); MIG/MPS are partitioning decisions, not dynamic quotas. Nothing to actuate.
- **Ephemeral storage** enforces limits by *eviction* and is not in-place resizable at all (only CPU and memory are).

The ecosystem framing is **one writer per field**: HPA owns replica count, VPA/rightsizers own requests, node autoscalers own capacity, and Headroom fills the otherwise-empty slot — `limits.cpu` as a function of booked node slack. Staying single-purpose keeps the RBAC surface minimal (patch `pods/resize`, nothing else), the trust review short, and the tool composable with everything in §8.3 instead of competing with it. Unified multi-resource optimization is an existing product category (ScaleOps, Cast AI, StormForge, PerfectScale); Headroom composes under the ownership rule rather than reinventing it.

---

## 4. Mechanism research: how to change a limit at runtime

Four candidate mechanisms were evaluated.

### 4.1 In-place pod resize (CHOSEN)

Kubernetes 1.35 (Dec 2025) graduated In-Place Pod Resize to stable. Properties relevant to this design, confirmed against current docs (kubernetes.io, Jan 2026):

- Resizes are submitted via the pod's **`resize` subresource** (`kubectl patch pod X --subresource resize`, or the equivalent client-go call). kubectl ≥ v1.32; cluster ≥ 1.33 with the gate on, GA at 1.35.
- CPU changes with `resizePolicy: NotRequired` (the default) are applied as a live cgroup write — same PID, restartCount unchanged.
- `spec.containers[*].resources` is the *desired* state; `status.containerStatuses[*].resources` is the *actual* state. Kubelet reports progress via `PodResizePending` (reason `Deferred` or `Infeasible`) and `PodResizeInProgress` conditions, with `observedGeneration` for tracking which spec generation the kubelet has acknowledged.
- Constraints that shape Headroom:
  - **QoS class is immutable.** Burstable must stay Burstable (requests and limits may not become equal for both CPU and memory simultaneously). BestEffort pods cannot have resources added at all.
  - **Resources cannot be removed once set** — a limit can be changed, never deleted. A CPU limit *can* be **added** to a Burstable pod that lacks one (Phase 0 spike Q2b, [`plan/archive/phase0-resize-spike.md`](plan/archive/phase0-resize-spike.md)): the resize succeeds, QoS stays Burstable, and `restartCount` is unchanged — so the controller can seed the limit itself on first reconcile. Adding a limit to a **BestEffort** pod is refused (`Pod QOS Class may not change`), which is why "CPU request > 0" stays a hard eligibility gate (§6.3).
  - Pods under static CPU/Memory Manager policies, Windows pods, and non-restartable init/ephemeral containers cannot be resized. Sidecar containers can.
  - Only CPU and memory are resizable.

**Pros:** spec is the source of truth; kubelet owns the cgroup write path (correct across runtimes); observability is native (`kubectl describe` shows conditions and events); no privileged node agent. **Cons:** every limit change is an API-server write (addressed with hysteresis/debounce, Section 7); Deferred/Infeasible states must be handled (Section 6.4).

### 4.2 DRA (REJECTED)

DRA (GA since 1.34) allocates *devices* to pods at scheduling time through ResourceClaims, DeviceClasses, and ResourceSlices. It answers "which discrete resource instances does this pod get," bound at admission/scheduling. It has no mechanism for mutating a running container's CFS quota, no post-scheduling reconciliation loop over cgroup parameters, and modeling "fractional share of node slack" as a claimable device would be a fiction fighting the API at every step (claims are not recomputed when unrelated pods come and go — which is the entire point of this project). The one thing DRA could theoretically add — making burst capacity visible to the scheduler — is explicitly a non-goal: limits shouldn't affect placement. **Verdict: wrong tool; in-place resize is purpose-built for this.**

### 4.3 Node-agent writing cgroups directly (REJECTED for v1, partial future role)

A privileged DaemonSet writing `cpu.max` under the kubelet: fastest reaction, zero API churn. Rejected because it makes the pod spec lie — the enforced limit would be invisible to `kubectl`, to admission systems, and to anyone debugging throttling, which directly contradicts the debuggability goal. It also races the kubelet (which rewrites cgroups on container restart and on resize) and requires privileged host access. Prior art that takes this path (Koordinator's CPU Burst, Alibaba's work) accepts the observability cost; we don't.

One capability only an agent can provide: cgroup v2 `cpu.max.burst` (CFS burst banking — letting a container briefly exceed quota using accumulated unused quota, which smooths 100 ms-window throttling). Kubernetes does not expose this field. If sub-second burst smoothing turns out to matter after v1, an optional agent that sets *only* `cpu.max.burst` (never `cpu.max`) is a clean, spec-consistent Phase 3 addition.

### 4.4 Disable CFS quota entirely (REJECTED, but instructive baseline)

Kubelet's `--cpu-cfs-quota=false` removes all throttling and lets `cpu.weight` do all the arbitration. This is the "just trust shares" position, and it's a legitimate one — Headroom must beat it to justify existing. What quota adds over pure shares: a hard ceiling bounds the *worst-case latency interference* a bursting neighbor can inflict at short timescales (shares arbitrate fairly on average, but a pod with huge instantaneous demand still steals scheduler latency from latency-sensitive neighbors between scheduling periods), it bounds the blast radius of runaway bugs (spin loops), and it gives tenants a *predictable* ceiling they can capacity-plan against. Headroom is the middle path: ceilings exist, but they're never lower than the node can justify.

### 4.5 Prior art summary

- **Koordinator / Crane / Alibaba CPU Burst:** node agents adjusting quota or `cpu.max.burst` based on usage; powerful but spec-divergent and usage-driven (churny). Headroom differs: request-driven, spec-truthful.
- **VPA (InPlaceOrRecreate mode, beta):** adjusts *requests* based on observed usage — complementary but conflicting if run on the same pods (Section 8.3).
- **CPU Startup Boost (AEP-7862, in discussion upstream):** temporary CPU boost at startup via resize — evidence the ecosystem is converging on resize as the actuation mechanism for exactly this kind of controller.

---

## 5. Policy design

### 5.1 Equal burst vs. proportional burst (the key policy question)

Two candidate ways to divide node slack `S` among the `N` managed pods:

**Equal split:** `limit_i = request_i + S/N`

**Proportional split:** `limit_i = request_i × (1 + S/M)` where `M = Σ managed requests`

Worked example — 16-core allocatable node, three managed pods requesting 0.5, 1.5, and 6 cores (`R = M = 8`, `S = 8`):

| Pod | Request | Equal-split limit | Proportional limit |
|-----|---------|-------------------|--------------------|
| A   | 0.5     | 3.17              | 1.0                |
| B   | 1.5     | 4.17              | 3.0                |
| C   | 6.0     | 8.67              | 12.0               |

**Equal split — pros:** simple to explain ("everyone gets the same bonus"); generous to small pods; acts as a built-in floor.
**Equal split — cons, and they're disqualifying for multi-tenancy:**

1. *Gameable by pod-splitting.* A tenant who splits one 4-core-request workload into eight 0.5-core-request pods multiplies their share of slack by 8. The policy rewards fragmentation, which also bloats scheduler and kubelet load.
2. *Rewards under-requesting.* A 10m-request pod gets the same burst bonus as a 4-core-request pod, so the cheapest path to burst capacity is a token request — the exact behavior this project exists to discourage.
3. *Incoherent with the kernel.* Under real contention, `cpu.weight` shares CPU proportionally to requests. An equal-split ceiling means a pod's behavior changes *shape*, not just scale, as the node fills: small-request pods experience a cliff (huge ceiling, tiny contended share), which is the least debuggable possible behavior.
4. *Unstable.* Every pod count change shifts every pod's limit by `S/N − S/(N±1)`, so churn is intrinsically higher.

**Proportional split — pros:** incentive-aligned (double your request → double your guaranteed capacity, your contended share, *and* your burst ceiling); scale-invariant (splitting a workload into k pods changes nothing in aggregate — no fragmentation incentive); coherent with `cpu.weight` at every utilization level (the ceiling is always the same multiple of the contended share, so degradation under load is a smooth, uniform scale-down); computationally trivial (one factor `F = 1 + S/M` per node — the per-node decision "did F move enough to act?" is a single comparison).
**Proportional — cons:** a genuinely tiny pod (10m request) gets a tiny absolute burst even on an empty node.

**Decision: proportional, with a floor to fix its one weakness.** This matches the stated requirement — incentivize raising requests, don't penalize small requests when nodes aren't bin-packed.

### 5.2 The floor (and why it must be slack-aware)

Naive floor ("everyone gets at least +1 core of burst") re-introduces equal-split's gaming problem and can promise burst a full node doesn't have. Instead:

```
burst_i = max( S × request_i / M,  min(minBurstFloor, S / N) )
limit_i = request_i + burst_i
```

The floor is the *smaller* of a configured absolute floor (default `1000m`) and the equal share of *actual remaining slack*. On an empty node, a 200m pod gets `request + 1 core` (1200m) — real, useful burst. The one caveat is the §5.3 `maxMultiplier` cap (default 10×), which binds *before* the additive floor for very small requests: the full 1-core boost only lands once `request + 1 core ≤ request × maxMultiplier`, i.e. `request ≥ ~112m` at the default 10×. Below that the cap is the binding constraint — a 10m pod is clamped to `request × 10 = 100m`, not `request + 1 core`. On a nearly full node, the floor collapses to `S/N`, which collapses to ~0, preserving the proportional incentive exactly when it matters (under contention). Splitting pods to farm the floor is capped at `S` total, and only pays when slack is abundant and therefore cheap — acceptable.

### 5.3 Caps and clamps

Applied in order **after** the base computation and the §5.2 floor — so the caps
always win: the floor can raise a limit, but a subsequent cap clamps it back
down. In particular a slack-aware floor can never push a limit above
`request × maxMultiplier` or above `min(nodeAllocatable, userCap)`; the clamps
below are the last word.

1. `limit_i ≥ request_i` — always (also required to stay Burstable-legal).
2. `limit_i ≤ request_i × maxMultiplier` (default 10×) — bounds how far behavior on an idle node can diverge from behavior on a busy one, so a pod's "empty-node personality" isn't wildly misleading about its "busy-node personality." Tunable; set to `0` to disable (the CRD's `maxMultiplier: "0"` sentinel).
3. `limit_i ≤ min(nodeAllocatable, userCap_i)` where `userCap_i` is an optional per-pod annotation (`kube-headroom.dev/max-cpu`) for workloads that genuinely shouldn't exceed some parallelism (e.g., GOMAXPROCS-pinned Go services — note limits don't change GOMAXPROCS automatically; runtime tuning is the user's job and worth a docs callout, since a Go binary that read GOMAXPROCS from its limit at startup won't exploit a raised ceiling anyway).
4. Rounding: quantize to `10m` to avoid patch churn from arithmetic noise.

### 5.4 What counts in the sums

- `allocatable_cpu`: from node status (already excludes system-reserved/kube-reserved).
- `Σ requests` (for slack): **all** non-terminal pods bound to the node — managed, unmanaged, Guaranteed, DaemonSets, kube-system. Slack is physical truth.
- `M` (for distribution): managed pods only. Unmanaged pods keep whatever limits they declared; their requests reduce `S` but they receive no share of it.
- Pod-level: sum container requests (including sidecars per their effective-request rules); apply the pod's aggregate burst to containers pro-rata by their CPU requests. Containers with zero CPU request in a managed pod receive no computed burst (their limit, if any, is left untouched).
- If `S ≤ 0` (node request-overcommitted — possible with unmanaged pressure): all managed limits = requests. Degenerates to pure `cpu.weight` sharing, which is the correct behavior.

### 5.5 Requests-driven, not usage-driven (explicit trade-off)

Because limits derive from requests, they change only when the node's booking changes. Pros: deterministic (auditable from `kubectl get pods -o wide` on the node at any timestamp), low churn, no metric-pipeline dependency, no feedback oscillation. Con: if a pod *requests* 8 cores and *uses* 0.1, those 7.9 cores don't raise anyone's *ceiling* — but they are still fully usable in practice, because CFS is work-conserving under each pod's ceiling and ceilings already include all unbooked capacity. The only lost opportunity is a pod that wants to burst *beyond* its Headroom ceiling into requested-idle capacity; that pod's remedy is to raise its request, which is the incentive working as intended. A usage-informed mode (e.g., PSI-gated ceiling relaxation) is deliberately deferred (Section 11).

---

## 6. Architecture

### 6.1 Components

**v1 is a single deployment:** the `headroom-controller` (controller-runtime manager, leader-elected, ≥2 replicas). Phase 2 adds an optional mutating admission webhook (6.5). No node agent, no CRDs required for MVP (cluster config via a single `HeadroomConfig` CRD or ConfigMap — CRD preferred for validation; decide at implementation time, CRD sketch in Section 9).

### 6.2 Reconciliation model: node-scoped

The unit of reconciliation is the **node**, not the pod — every scheduling event on a node invalidates every managed limit on that node, so per-pod reconcile would thrash.

1. Pod informer, indexed by `spec.nodeName`. Events that enqueue a node key: pod bound (nodeName set), pod deleted / entered terminal phase, pod CPU requests changed (resize by someone else, e.g., VPA on unmanaged pods).
2. Node informer: allocatable changes, cordon+drain don't matter (pods leaving already fire), node deletion cleans queue state.
3. **Debounce:** node keys are dequeued no more often than `debouncePeriod` (default 2s), so a burst of scheduling (deployment rollout landing 10 pods on a node) computes once.
4. Reconcile(node):
   a. List cached pods on node; compute `S`, `M`, `F`, and per-pod target limits (pure function, Section 9).
   b. **Hysteresis:** for each managed pod, skip if `|target − current| / current < deadband` (default 10%) — with one exception: a *shrink* that would bring an above-entitlement limit down toward request on a node where `S` decreased is applied at a tighter deadband (default 5%), because stale-generous limits are a fairness bug while stale-stingy limits are only an efficiency bug.
   c. Issue resize patches (server-side apply on the `resize` subresource, dedicated fieldManager `headroom`) through a per-node token bucket (default 10 patches/s) and a global client QPS budget.
   d. Record annotation + event per changed pod (Section 8.1).

### 6.3 Pod eligibility

A pod is managed iff **all** of: its namespace is labeled `kube-headroom.dev/mode: managed` (or matched by `spec.namespaceSelector`); the pod itself does not set `kube-headroom.dev/mode: unmanaged`; QoS class Burstable; every app container has a CPU **request > 0**; not on a Windows node or a node with static CPU/Memory Manager policy (detect via node labels/config, plus handle `Infeasible` defensively); pod not owned by anything in an operator-configured exclusion list (kube-system excluded by default).

The `mode` gate uses **enum keywords** (`managed`/`unmanaged`), not booleans, so an
unquoted YAML 1.1 token (`true`/`yes`) cannot silently coerce; it is
**fail-closed** — any absent or unrecognized value means "not managed" (Q10).

A CPU limit does **not** need to be pre-declared. The Phase 0 spike confirmed the
controller can **add** a limit to a Burstable pod via resize (§4.1, Q2b), so a
container with a request but no limit is eligible — the controller seeds the
limit on first reconcile. The hard gate that remains is **CPU request > 0**:
adding a limit to a BestEffort pod (no request) is refused by the apiserver. The
Phase 2 webhook (§6.5) remains useful only for pods too short-lived for the
controller to reach in time.

### 6.4 Resize outcome handling

- **`Infeasible`:** should be near-impossible since targets are capped at allocatable, but if it occurs (e.g., manager-policy node slipped through eligibility): mark pod ineligible for `backoffPeriod`, emit warning event, increment `headroom_resizes_total{result="infeasible"}`.
- **`Deferred`:** kubelet retries on its own; controller does nothing but track a gauge. A deferred *limit-only increase* should be rare (no allocation change), so a sustained deferred count is an alerting signal.
- **Quota `403 Forbidden`:** a `limits.cpu` ResourceQuota in the namespace makes an over-budget raise fail **admission** with a 403 — a distinct signal from kubelet `Infeasible` (Phase 0 spike Q2c, [`plan/archive/phase0-resize-spike.md`](plan/archive/phase0-resize-spike.md); quota is delta-accounted on resize). Treat it like `Infeasible`: mark the pod ineligible for `backoffPeriod`, emit a warning event, increment `headroom_resizes_total{result="quota-denied"}`. The durable fix is operational: managed namespaces must quota on `requests.cpu` only (§8.3, preflight).
- **Conflict / stale generation:** requeue the node; the next reconcile recomputes from current state. `status.observedGeneration` distinguishes "kubelet hasn't seen it" from "kubelet rejected it."
- **Pod resized by another actor:** the eligibility check plus fieldManager keeps ownership clean; Headroom owns `limits.cpu` on managed pods and nothing else. Document that manual `--subresource resize` edits to managed pods' CPU limits will be overwritten.

### 6.5 Mutating admission webhook (MVP component — promoted from Phase 2)

Originally a UX nicety, the webhook is load-bearing for two use cases and therefore belongs in the MVP:

1. **Short-lived pods (CI, batch):** a 30-second job can complete before debounce + reconcile + kubelet actuation ever reaches it. The only moment Headroom can affect such pods is admission. The webhook computes the node-independent part it can know at CREATE time and sets `limits.cpu = requests.cpu × initialMultiplier` when absent (it cannot know the target node pre-scheduling, so the initial value is a config default, e.g. the cluster-typical factor; the controller corrects it post-bind for pods that live long enough to matter).
2. **Boot-time-quota runtimes (JVM, boot-read GOMAXPROCS):** these size themselves from the limit at container start; a generous birth limit is the only limit they ever act on.

It also removes the "must pre-declare a CPU limit" eligibility requirement, guaranteeing every pod in an opted-in namespace is born manageable and Burstable. Webhook risk is managed with `failurePolicy: Ignore` scoped by the namespace label (a missed mutation degrades to v1-eligibility behavior, never blocks pod creation).

---

## 7. Performance characteristics

**Write amplification is the main cost.** One scheduling event on a node with `P` managed pods is worst-case `P` resize patches. Mitigations, in order of effect:

1. *Single-factor property of the proportional policy:* all managed pods on a node share one factor `F`; hysteresis on limits is (nearly) hysteresis on `F`, so the common case is "F moved 3%, nothing to do" — zero patches. With a 10% deadband, a node needs ~10% booking change to trigger a wave. Steady-state clusters see very few patches.
2. *Debounce* collapses rollout bursts into one computation.
3. *Rate limiting* bounds worst-case API pressure: `10 patches/s/node`, global client QPS default 50 (tunable). A 5,000-node cluster with heavy churn is bounded by the global budget; staleness during bursts degrades gracefully (Section 8.6 failure analysis — stale limits are safe).

**Kubelet cost:** a CPU-only, `NotRequired` resize is a CRI `UpdateContainerResources` → one cgroup write. Cheap; PLEG improvements since 1.33 made resize acknowledgment fast.

**Controller memory/CPU:** pod informer over all pods (or label-selected namespaces via cache filters — do this: `cache.Options.ByObject` with namespace selectors keeps memory proportional to managed namespaces, though slack computation needs *all* pods per node, so the node-slack path needs an unfiltered lightweight metadata-only informer on pods — use `metadata.name/namespace/nodeName/phase` + a PartialObjectMetadata transform won't carry requests… **implementation note:** requests are needed for all pods, so run one full pod informer but strip unneeded fields with a cache transform function to cut memory ~80%).

**Latency of adaptation:** debounce (2s) + patch + kubelet actuation ≈ limits track scheduling within a few seconds. During the gap a new pod runs briefly under neighbors' stale-generous ceilings — CFS weights still guarantee it its proportional share, so the gap is benign.

---

## 8. Usability, debuggability, and interactions

### 8.1 Explaining every throttle

Design rule: **any observed throttling must be explainable in two kubectl commands.**

- Annotation on every managed pod, updated on change:
  `kube-headroom.dev/status: {"factor":"2.00","slack":"8000m","managedRequests":"8000m","nodePods":14,"computedAt":"...","policy":"proportional-v1"}`
- Kubernetes event per applied change: `CPULimitAdjusted: 1500m → 3000m (node factor 2.00, slack 8/16 cores)`.
- Metrics: `headroom_node_factor{node}`, `headroom_node_slack_cores{node}`, `headroom_pod_limit_cores{pod}`, `headroom_resizes_total{result}`, `headroom_reconcile_duration_seconds`, `headroom_pods_managed`. Ship a Grafana dashboard correlating `headroom_pod_limit_cores` with `container_cpu_cfs_throttled_periods_total` — the money graph: "you were throttled because the node was 94% booked; here's who booked it."
- Runbook doc: "My pod is throttled" → is the node full of *requests*? → raise your request or move to a less-booked pool. The answer is never "some agent decided based on a metric you can't see."
- Future: `kubectl headroom explain <pod>` plugin printing the arithmetic, including a **what-if attribution**: correlate `container_cpu_cfs_throttled_periods_total` (queried at read time from Prometheus) with the policy math to answer "what ceiling would request X have produced under this node's booking." This is the one request-related signal Headroom can provide that VPA cannot.

**Boundary — request recommendations belong to VPA, not Headroom.** Deciding *what a pod's request should be* requires distinguishing sustained demand above request (raise it) from occasional bursts clipped on busy nodes (working as intended) — a usage-histogram problem that is VPA's recommender, and rebuilding it would both duplicate VPA and risk a request-inflation arms race if naively triggered by every ceiling touch. The supported answer to "raise it to what?" is VPA's `status.recommendation` (usable in `updateMode: Off` for recommendation-only, or `controlledValues: RequestsOnly` for auto-apply, §8.3). Headroom's controller consequently never reads usage metrics — it stays a pure function of API-server state, which is what the failure-mode analysis (§8.6) depends on; usage enters only at read time in dashboards and the explain plugin.

### 8.2 Tenant-facing contract (one paragraph users must understand)

"Your CPU request is guaranteed. Your CPU limit is your request plus a fair share of whatever CPU the node hasn't promised to anyone else — high on a quiet node, back to your request on a full one. If you need more sustained CPU, request more. Your limit shrinking is not an incident; it means the node got busier, and you still have everything you requested."

### 8.3 Interactions with other systems

- **HPA:** CPU-utilization HPA computes against *requests* — structurally unaffected. One behavioral change to document loudly: throttling previously acted as a silent cap on the HPA signal (a pod pinned at its limit reports bounded usage). Unthrottled pods reveal true demand, so utilization can exceed 100% of request and HPA may scale out earlier/more aggressively. This is *correct* (demand is no longer masked), but teams tuning HPA thresholds will notice; recommend reviewing stabilization windows on adoption. Anything custom keyed on *limits* (rare) will see them move.
- **VPA:** two supported postures. (a) *Composition* — VPA with `resourcePolicy.containerPolicies[].controlledValues: RequestsOnly` manages requests while leaving limits alone; Headroom owns `limits.cpu`. VPA's request changes are just Headroom inputs (request-change events already enqueue the node, §6.2). Both actors use the resize subresource with distinct SSA fieldManagers on disjoint fields — **coexistence is verified** (Phase 0 spike Q2d: `vpa-sim` owning `requests.cpu`, `headroom` owning `limits.cpu`, ownership granular per leaf, zero conflicts in steady state; each controller's *first* write forces ownership once, which is a one-time transfer, not a loop). The recommended combined recipe is in the [tenant guide](tenant-guide.md). (b) *Mutual exclusion* — VPA in its default `RequestsAndLimits` mode (which scales limits proportionally with requests) conflicts directly; eligibility check refuses pods matched by such a VPA.
- **Google GKE Multidimensional Pod Autoscaler (MPA, beta):** compatible by construction — MPA scales horizontally on CPU utilization and vertically on *memory requests* (`containerControlledResources: [memory]`); zero field overlap with `limits.cpu`. The HPA-signal note above applies to MPA's horizontal-CPU dimension identically.
- **Commercial rightsizers (ScaleOps, Cast AI, StormForge, PerfectScale, etc.):** same rule as VPA — compatible when they manage requests, conflicting when they set or strip limits in managed namespaces. Per-namespace opt-in gives clean territory division. Node-agent-based tools that write cgroups directly (Koordinator, Crane) must not run on Headroom-managed nodes.
- **ResourceQuota:** `limits.cpu` quotas in managed namespaces make Headroom's raises consume tenant quota, and an over-budget raise **fails admission with a 403** (not `Infeasible`) — verified in the Phase 0 spike (Q2c: quota is delta-accounted on resize; a within-quota raise succeeds and updates `used.limits.cpu`, an over-quota raise is rejected). **Hard requirement:** managed namespaces must quota on `requests.cpu` only, never `limits.cpu`. Ship this as a preflight check and a documented constraint (see [runbook](runbook.md#preflight)); the controller treats the 403 as a back-off signal (§6.4).
- **LimitRange:** `max.limit` constraints in a namespace cap what Headroom can set; respect them by reading LimitRanges into the clamp step, or document exclusion.
- **Cluster autoscaler / Karpenter:** operate on requests; unaffected. Scale-down consolidation that drains a node fires normal pod-delete events; fine.
- **Scheduler:** limits are ignored by scheduling; because Headroom never touches requests, the documented scheduler behavior around pending resizes (max of desired/allocated/actual *requests*) is not triggered.

### 8.4 Workload applicability matrix

Headroom is opt-in per namespace precisely because fit varies by workload class:

| Workload class | Fit | Notes |
|---|---|---|
| Multi-tenant stateless services (Burstable) | **Primary target** | Full benefit; coherent degradation under load |
| CI / batch jobs (incl. GPU CI) | **Strong, needs webhook** | Short-lived pods can finish before the reconcile loop reaches them; admission-time initial limit (webhook, §6.5) is load-bearing here, not a nicety. Compile/test bursts on idle nodes are the showcase use case |
| GPU inference services | **Strong** | CPU-throttled dataloaders/pre-processing starving GPUs is a classic failure; unthrottling CPU when nodes have slack directly raises GPU utilization |
| Distributed synchronous training (gang-scheduled) | **Opt out** | Job speed = slowest worker; workers on differently-booked nodes get different ceilings → stragglers. Run Guaranteed or set `kube-headroom.dev/mode: unmanaged`. A "uniform group ceiling" mode (min-of-group across nodes) is possible future work but requires cross-node coordination (§11) |
| NUMA-pinned / static CPU Manager nodes (common on dedicated training nodes) | **Excluded structurally** | In-place resize is prohibited with static CPU/Memory Manager policies; these nodes are typically dedicated and fully booked anyway |
| Guaranteed QoS pods | **Excluded structurally** | Resize cannot change QoS class; requests must stay == limits |
| BestEffort pods | **Excluded structurally** | Resources cannot be added via resize; no request → no weight basis either |
| Runtimes that read CPU quota once at startup (JVM ergonomics, boot-time automaxprocs) | **Partial benefit — document** | The cgroup ceiling rises but the runtime's thread-pool sizing doesn't follow. Admission-time seeding gives a good birth limit; full benefit needs quota re-reading in the runtime. Tenant guide must cover this |
| Windows pods | **Excluded structurally** | In-place resize unsupported |

Scheduling-mode behavior (Headroom never influences placement — limits are invisible to schedulers, and requests are never touched — so there is no feedback loop in any mode):

- **Spread / LeastAllocated (default):** abundant per-node slack; largest and most visible bursts. New pods land on the least-booked nodes, which is exactly where incumbents' ceilings shrink most — correct, just dynamic.
- **Bin-pack / MostAllocated / Karpenter consolidation:** full nodes → `S ≈ 0` → limits ≈ requests. This is the honest ceiling, not a failure; value concentrates in utilization troughs and on not-yet-packed nodes. Consolidation moves generate normal pod churn → recomputes.
- **Custom schedulers (Volcano, Kueue, YuniKorn, scheduler-plugins):** fully compatible — the controller keys off `spec.nodeName` binding regardless of which scheduler bound it. Gang bindings arrive as a burst that the per-node debounce collapses into one recompute. Kueue quota admission operates on requests, untouched. The gang-*training* caveat above is a workload property, not a scheduler property.

### 8.5 When NOT to use Headroom (honest alternative)

On a trusted, single-tenant cluster, simply omitting CPU limits (or running kubelet with `--cpu-cfs-quota=false`) delivers most of the benefit with zero moving parts: `cpu.weight` provides work-conserving proportional sharing and nothing ever throttles. Headroom earns its complexity only where ceilings are required — hostile or contractual multi-tenancy, blast-radius bounds for runaway bugs, and predictable capacity planning per tenant. This should appear in the README so adopters self-select correctly.

### 8.6 Failure modes

- **Controller down:** limits freeze at last values. Safety analysis: frozen-generous limits on a node that then fills → worst case is pre-Headroom behavior with generous static limits, and `cpu.weight` still enforces request-proportional sharing under contention. Frozen-tight limits on a node that then empties → unnecessary throttling, i.e., status-quo Kubernetes. **No failure mode is worse than not running Headroom.** This property is the strongest argument for the requests-driven design and should be preserved in all future changes.
- **API server pressure:** rate limits bound it; degraded mode is staleness, which is safe per above.
- **Split brain (two leaders):** leader election prevents; even if violated, both compute the same pure function of the same cache → convergent patches.
- **Thundering herd on controller restart:** initial reconcile of all nodes finds most limits already within deadband → few patches. Add jittered initial sync anyway.

---

## 9. Implementation sketch

### 9.1 Repo layout (kubebuilder v4, Go 1.24+)

```
kube-headroom/
  cmd/main.go
  api/v1alpha1/headroomconfig_types.go     # cluster-scoped config CRD
  internal/controller/node_reconciler.go   # informers, debounce, patching
  internal/policy/policy.go                # PURE FUNCTIONS — no k8s deps
  internal/policy/policy_test.go           # table tests, property tests
  internal/eligibility/eligibility.go
  internal/webhook/
  test/e2e/                                # kind-based, k8s >= 1.35
  docs/design.md                           # this doc
  docs/runbook.md docs/tenant-guide.md docs/applicability.md
  dashboards/headroom.json
```

Naming note: Kubernetes convention requires annotation/label prefixes to be a DNS domain the project controls. The prefix is **`kube-headroom.dev`**, defined once as `v1alpha1.GroupName` and equal to the CRD's API group (cert-manager style). Every label/annotation key derives from it (`kube-headroom.dev/mode`, `/status`, `/max-cpu`); it is a single constant precisely because migrating deployed tenant annotations later is painful.

### 9.2 The policy core (keep it boring and pure)

```go
package policy

type PodInput struct {
    Key        string
    RequestMilli int64
    CurrentLimitMilli int64
    Managed    bool
    UserCapMilli int64 // 0 = none
}

type Config struct {
    MinBurstFloorMilli int64   // default 1000
    MaxMultiplier      float64 // default 10.0
    DeadbandGrow       float64 // default 0.10
    DeadbandShrink     float64 // default 0.05
    QuantumMilli       int64   // default 10
}

type Decision struct {
    Key             string
    TargetLimitMilli int64
    Apply           bool   // false if within deadband
    Reason          string // for the event/annotation
}

// ComputeNode is deterministic and side-effect free.
func ComputeNode(allocatableMilli int64, pods []PodInput, cfg Config) []Decision
```

Every policy question in Section 5 becomes a table test against this function; property tests assert the invariants: `request ≤ limit ≤ min(allocatable, caps)`, scale-invariance under pod-splitting, monotonicity (adding a pod never *raises* anyone else's limit), and `S ≤ 0 ⇒ all limits = requests`.

### 9.3 HeadroomConfig CRD (cluster-scoped, singleton)

Spec fields: `policy: Proportional` (enum for future), `minBurstFloor`, `maxMultiplier`, `deadband{grow,shrink}`, `debouncePeriod`, `rateLimits{perNodePatchesPerSecond, clientQPS}`, `namespaceSelector` (default: label `kube-headroom.dev/mode=managed`), `excludedNamespaces` (default `[kube-system]`), `dryRun: bool` (compute + annotate + metric, no patches — **ship dry-run first**, it's the adoption path and the validation harness).

### 9.4 Key implementation constraints (for the coding session)

1. Patch via the **resize subresource** with SSA and fieldManager `headroom`; never touch requests, never touch memory, never touch any field but `spec.containers[*].resources.limits.cpu` of eligible containers.
2. Set nothing if the container's `resizePolicy` for CPU is `RestartContainer` — treat as ineligible (a limit change must never restart a workload).
3. Informer cache transform to strip pod fields (keep metadata, nodeName, phase, QoS, container resources, resizePolicy, ownerRefs).
4. All timing knobs jittered.
5. RBAC: get/list/watch pods+nodes+namespaces+limitranges+vpa; patch on `pods/resize` (the CPU limit) **and** patch on `pods` (metadata only, for the `kube-headroom.dev/status` annotation of §8.1); events create. Nothing else — notably no pod `update`/`delete`, and the `pods` patch never touches any field but the status annotation.

---

## 10. Phased scope

**Phase 0 — spike (validate before writing the controller): ✅ complete.** On a kind 1.35 cluster ([`plan/archive/phase0-resize-spike.md`](plan/archive/phase0-resize-spike.md)): (a) CPU limit-only resize latency & rapid repeated patches — ~1.8s, 0 restarts, safe to repeat (debounce is an optimization, not a safety requirement); (b) a CPU limit **can** be added to a Burstable pod via resize (BestEffort refused); (c) resize vs. ResourceQuota `limits.cpu` — quota is delta-accounted and an over-budget raise 403s (→ quota `requests.cpu` only); (d) rollout churn — 40 back-to-back resizes converge cleanly, 0 restarts; (e) VPA (`controlledValues: RequestsOnly`) and Headroom patch disjoint leaves with no conflict loop. Each answer fed back into eligibility (§6.3), outcome handling (§6.4), and these docs.

**Phase 1 — MVP:** policy core + node reconciler + mutating webhook (`failurePolicy: Ignore`, §6.5 — required for short-lived-pod coverage) + dry-run mode + metrics + annotations/events + docs (including the applicability matrix §8.4 and the "when not to use this" note §8.5). Exit criteria: on a test cluster, a pod on an empty node runs unthrottled at ~node capacity; scheduling neighbors shrinks its limit within 5s; a 20-second Job in a managed namespace gets a boosted limit at admission; controller kill mid-operation leaves cluster safe; zero patches at steady state.

**Phase 2 — adoption hardening:** LimitRange awareness; VPA-exclusion check; Grafana dashboard; `kubectl` plugin; per-pod `kube-headroom.dev/max-cpu`.

**Phase 3 — optional/experimental:** `cpu.max.burst` node agent (spec-consistent burst banking only); per-tenant slack weighting; usage/PSI-informed policy mode; pod-level resources support as that API matures.

---

## 11. Open questions / future work

1. **Per-tenant fairness across pods:** proportional-per-pod means a tenant running many big-request pods on one node collects most of its slack — arguably correct (they booked it), but hierarchical (namespace → pod) slack division is worth revisiting for hostile multi-tenancy.
2. **VPA composition hardening:** the `controlledValues: RequestsOnly` posture (§8.3) was **confirmed** in the Phase 0 spike (Q2d — two fieldManagers on disjoint leaves, no conflict loop); the recommended combined-deployment recipe now lives in the [tenant guide](tenant-guide.md). Remaining future work is stress-testing it under real VPA churn at scale.
3. **Should shrinks lead grows?** Current design: tighter deadband for shrinks. Alternative: apply shrinks immediately, batch grows. Measure in Phase 1.
4. **Node pools with different personalities** (e.g., batch pools with `maxMultiplier=∞`, latency pools with 2×) — per-nodepool config via node selectors in HeadroomConfig.
5. **GOMAXPROCS/runtime awareness:** raised ceilings only help runtimes that can use them; many read quota once at boot (JVM ergonomics, boot-time automaxprocs). A docs pattern (periodic quota re-reading) matters for realized benefit; admission-time seeding (§6.5) covers the birth limit.
6. **Uniform group ceilings for gang workloads:** synchronous distributed jobs suffer stragglers when workers on differently-booked nodes get different ceilings. A `kube-headroom.dev/group-uniform: true` mode setting min-of-group across nodes is feasible but requires cross-node coordination in the reconciler (group index keyed by owner); deferred — opt-out is the v1 answer.

## 12. Requirements traceability

| Requirement (from brief) | Where addressed |
|---|---|
| No throttling on empty nodes | §5.1 formula: F large when S large; §5.2 floor for tiny requests |
| Limit = ratio of requested + unrequested CPU | §5.1 proportional policy, exactly `request × (1 + S/M)` |
| Update on pod schedule/remove | §6.2 node-scoped reconcile on pod bind/delete |
| DRA evaluation | §4.2 — evaluated and rejected with rationale; §4.1 chosen instead |
| Multi-tenant fair sharing | §5.1 coherence with cpu.weight; §8.2 contract; §11.1 future per-tenant work |
| Throttling debuggability | §8.1 two-command explainability; §5.5 determinism |
| Equal vs. proportional burst evaluation | §5.1 full comparison with worked example; proportional + floor chosen |
| Incentivize raising requests, don't punish low requests on empty nodes | §2.1, §5.2 slack-aware floor |
| Pros/cons, usability, performance | §4 (mechanisms), §7 (performance), §8 (usability, failure modes) |

---

## References

- In-Place Pod Resize GA announcement (K8s 1.35): https://kubernetes.io/blog/2025/12/19/kubernetes-v1-35-in-place-pod-resize-ga/
- Resize task doc (constraints, conditions, QoS rules): https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources/
- 1.33 beta post (resize subresource, conditions design): https://kubernetes.io/blog/2025/05/16/kubernetes-v1-33-in-place-pod-resize-beta/
- CPU Startup Boost proposal: AEP-7862 (kubernetes/autoscaler)
- Prior art: Koordinator CPU Burst; VPA InPlaceOrRecreate (AEP-4016)
