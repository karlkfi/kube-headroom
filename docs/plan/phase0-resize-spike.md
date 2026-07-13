# Phase 0 resize spike — findings (Q2)

Validate the in-place CPU-limit resize mechanism on real Kubernetes **before**
writing the controller (design doc §10, Phase 0). Four questions, each of which
"changes eligibility or docs." All four are now answered empirically.

## Environment

- kind v0.31.0, node image `kindest/node:v1.35.5`, single-node cluster
  (`kube-headroom-spike`, separate from the e2e cluster).
- Kubernetes **v1.35.5** — in-place pod resize is **GA**, so the
  `InPlacePodVerticalScaling` feature gate is on by default.
- cgroup v2, containerd 2.3.1. Test pods: `busybox:1.36` with
  `resizePolicy: [{resourceName: cpu, restartPolicy: NotRequired}]`.
- Reproduction scripts: `exp-a`..`exp-d` (attached to the PR / scratchpad; not
  committed — they need a live 1.35 cluster).

**Method note that bit us:** patch the resize subresource with a **strategic**
merge patch (the `kubectl patch` default), *not* `--type merge`. JSON merge
patch replaces the whole `containers` array, dropping `resizePolicy` and other
fields, which trips `spec: Forbidden: only cpu and memory resources are mutable`.
Send only the changed leaf (`limits.cpu`).

---

## Q2a — CPU limit-only resize latency & rapid repeated patches ✅ safe

Single limit-only resize (200m → 700m, `NotRequired`):

- **~1.8 s** from patch to `status.containerStatuses[].resources` reflecting the
  new limit; the in-container `cgroup cpu.max` changed `20000 100000` →
  `70000 100000` (i.e. the CFS quota was actually rewritten).
- **`restartCount` stayed 0**; pod `Ready` throughout.

40 alternating resizes (300m ↔ 900m) submitted back-to-back in ~3.8 s:

- All applied cleanly, converged to the last desired value (cgroup matched),
  **0 restarts**, pod stayed `Running`, resize conditions clear.

**Impact.** The core mechanism works and is cheap. The observed latency is
dominated by status propagation, not the cgroup write, and sits comfortably
under the design's few-seconds / 5 s exit-criterion budget (§7, §10). A
`NotRequired` limit-only resize is safe to issue repeatedly — **debounce
(§6.2) is a write-amplification optimization, not a safety requirement.**

## Q2b — Can a CPU limit be *added* to a Burstable pod via resize? ✅ yes

Burstable pod with `requests.cpu: 100m` and **no** limit (cgroup
`cpu.max = "max 100000"`, unbounded). Adding `limits.cpu: 500m` via the resize
subresource:

- **Succeeded** (`exit 0`); spec + status show `limits.cpu: 500m`; cgroup became
  `50000 100000`; QoS stayed **Burstable**; `restartCount` 0.

Control — BestEffort pod (no requests, no limits), add `limits.cpu`:

- **Rejected**: `Pod QOS Class may not change as a result of resizing`
  (BestEffort → Burstable is forbidden).

**Impact — relaxes §6.3 eligibility.** Drop *"every app container has a CPU
limit already set"* as a hard gate: the controller can **add** the limit itself
on first reconcile. The gate that must remain is **CPU request > 0** (a pod with
no request is BestEffort, and adding a limit there is refused). This also means
the mutating webhook (§6.5) is **not** required to make *long-lived* pods
manageable — it stays load-bearing only for short-lived / boot-time-quota pods,
which the controller can never reach in time.

## Q2c — Resize vs. ResourceQuota `limits.cpu` admission ⚠️ quota gates resizes

Namespace with `ResourceQuota{hard: {limits.cpu: "1", requests.cpu: "1"}}`,
pod at `limits.cpu: 200m`:

- Within-quota resize 200m → 800m: **succeeded**, and quota accounting updated
  (`used.limits.cpu: 800m`). Quota is **delta-accounted** on resize.
- Over-quota resize 800m → 5000m: **rejected by admission** —
  `Error from server (Forbidden): exceeded quota: q, requested:
  limits.cpu=4200m, used: limits.cpu=800m, limited: limits.cpu=1`. Spec/status
  stayed 800m.

**Impact — confirms §8.3 with a concrete failure mode.** A `limits.cpu`
ResourceQuota in a managed namespace makes Headroom's raises consume tenant
quota and makes over-budget resizes **fail admission with a 403** (not
`Infeasible`). Consequences:

1. **Docs / preflight requirement (hard):** managed namespaces must quota on
   `requests.cpu` only, never `limits.cpu`. Ship this as a preflight check and a
   documented constraint.
2. **Controller error path:** treat a quota `403 Forbidden` on the resize patch
   like `Infeasible` (§6.4) — mark the pod ineligible for `backoffPeriod`, emit
   a warning event, increment `headroom_resizes_total{result="quota-denied"}`
   (or fold into `infeasible`). It is a distinct signal from kubelet
   `Infeasible`/`Deferred`.

## Q2d — VPA `RequestsOnly` coexistence: two SSA managers, disjoint fields ✅ no conflict loop

Modelled two controllers via server-side apply on the resize subresource:
`vpa-sim` owning `requests.cpu`, `headroom` owning `limits.cpu`.

- **First write must force.** A fresh pod's `requests`/`limits` leaves are owned
  by its creator (`kubectl-client-side-apply` here; **`kube-controller-manager`**
  in production, via the Deployment). Each controller's first SSA needs
  `Force=true` to take its own leaf — a **one-time ownership transfer, not a
  loop.**
- **Steady state is conflict-free.** 10 rounds of alternating re-applies of the
  same values: **zero conflicts, no field flapping**, final `req=180m
  limit=600m`, `restartCount` 0.
- **Ownership is granular per leaf** (`--show-managed-fields`):
  - `manager=headroom  op=Apply sub=resize` owns exactly
    `spec.containers[app].resources.limits.cpu`
  - `manager=vpa-sim   op=Apply sub=resize` owns exactly
    `spec.containers[app].resources.requests.cpu`
  - no overlap.

**Impact — confirms §11.2 / §8.3(a).** VPA (`controlledValues: RequestsOnly`,
in-place) and Headroom can patch disjoint resource fields of the same pod via
the resize subresource without fighting. Implementation notes for Q4:

- Use SSA with **`Force=true`** (controller-runtime `client.ForceOwnership`) and
  a stable fieldManager (`headroom`, already assumed in the node-reconciler
  plan). The force is required to wrest `limits.cpu` from `kube-controller-manager`
  on the first patch; it does not cause churn thereafter.
- Headroom owns `limits.cpu` and nothing else — matches the §6.4 ownership
  claim. A recommended combined VPA+Headroom recipe belongs in the Q9 tenant
  guide.

---

## Net effect on the backlog

- **Q5 (eligibility + dry-run):** drop the "must pre-declare a CPU limit" gate;
  keep "CPU request > 0"; the controller adds the limit on first reconcile
  (Q2b). Add a quota-`403` → back-off/ineligible path (Q2c).
- **Q4 (node reconciler):** SSA resize patch with `Force=true`, fieldManager
  `headroom`; handle `403 Forbidden` (quota) alongside `Infeasible`/`Deferred`
  (Q2c, Q2d).
- **Q6 (webhook):** confirmed *not* needed for long-lived-pod manageability
  (Q2b) — scope it to short-lived / boot-time-quota pods only.
- **Q9 (docs):** managed namespaces quota `requests.cpu` only (Q2c); VPA
  `RequestsOnly` coexistence recipe (Q2d); applicability matrix can state
  limit-add and quota behavior as verified, not assumed.
