# Headroom applicability matrix

Headroom is **opt-in per namespace** precisely because fit varies by workload
class. Use this page to decide whether to enroll a namespace; then see the
[tenant guide](tenant-guide.md) to run under it or the [runbook](runbook.md) to
operate it. Rationale for everything here is the [design doc](design.md) (§8.4–8.5).

## When to use it — and when not to

Headroom earns its complexity only where CPU **ceilings are genuinely required**:

- **Hostile or contractual multi-tenancy** — tenants who must not be able to
  degrade each other, or SLAs you have to bound.
- **Blast-radius bounds** — a hard ceiling caps the damage a runaway bug (spin
  loop) can do to neighbors between scheduling periods.
- **Predictable per-tenant capacity planning** — a ceiling tenants can plan
  against.

**If none of those apply, do not run Headroom.** On a trusted, single-tenant
cluster, simply **omitting CPU limits** (or running kubelet with
`--cpu-cfs-quota=false`) delivers most of the benefit with zero moving parts:
the kernel's `cpu.weight` provides work-conserving, request-proportional sharing
and nothing ever throttles. Headroom is the middle path — ceilings exist, but
they are never lower than the node can justify — and it is only worth its
operational surface where ceilings are a requirement, not a preference.

## Workload applicability

| Workload class | Fit | Notes |
|---|---|---|
| Multi-tenant stateless services (Burstable) | **Primary target** | Full benefit; coherent degradation under load. |
| CI / batch jobs (incl. GPU CI) | **Strong, needs webhook** | Short-lived pods can finish before the reconcile loop reaches them; the admission-time initial limit (webhook, design §6.5) is load-bearing here, not a nicety. Compile/test bursts on idle nodes are the showcase use case. |
| GPU inference services | **Strong** | CPU-throttled dataloaders/pre-processing starving GPUs is a classic failure; unthrottling CPU when nodes have slack directly raises GPU utilization. |
| Distributed synchronous training (gang-scheduled) | **Opt out** | Job speed = slowest worker; workers on differently-booked nodes get different ceilings → stragglers. Run Guaranteed or set `kube-headroom.dev/mode: unmanaged`. A "uniform group ceiling" mode is possible future work (design §11). |
| NUMA-pinned / static CPU Manager nodes (common on dedicated training nodes) | **Excluded structurally** | In-place resize is prohibited under static CPU/Memory Manager policies; these nodes are typically dedicated and fully booked anyway. |
| Guaranteed QoS pods | **Excluded structurally** | Resize cannot change QoS class; requests must stay == limits. |
| BestEffort pods | **Excluded structurally** | Resources cannot be *added* via resize, and no request means no `cpu.weight` basis either. |
| Runtimes that read CPU quota once at startup (JVM ergonomics, boot-time automaxprocs) | **Partial benefit — read the caveat** | The cgroup ceiling rises but the runtime's thread-pool sizing doesn't follow. Admission-time seeding gives a good birth limit; full benefit needs quota re-reading in the runtime. See the [tenant guide](tenant-guide.md#runtimes-that-read-cpu-quota-once-at-startup). |
| Windows pods | **Excluded structurally** | In-place resize unsupported. |

The "excluded structurally" rows are not policy choices — the platform cannot
resize those pods at all, so Headroom leaves them alone (and defends against a
stray `Infeasible` if one slips through eligibility).

## Behavior by scheduling mode

Headroom never influences placement — limits are invisible to schedulers, and
requests are never touched — so there is **no feedback loop in any mode**. What
changes across modes is only how much slack exists to distribute:

- **Spread / LeastAllocated (default):** abundant per-node slack; the largest and
  most visible bursts. New pods land on the least-booked nodes, which is exactly
  where incumbents' ceilings shrink most — correct, just dynamic.
- **Bin-pack / MostAllocated / Karpenter consolidation:** full nodes → slack ≈ 0
  → limits ≈ requests. This is the honest ceiling, not a failure; the value
  concentrates in utilization troughs and on not-yet-packed nodes. Consolidation
  moves generate normal pod churn, which triggers recomputes.
- **Custom schedulers (Volcano, Kueue, YuniKorn, scheduler-plugins):** fully
  compatible — the controller keys off `spec.nodeName` regardless of which
  scheduler bound the pod. Gang bindings arrive as a burst that the per-node
  debounce collapses into one recompute. Kueue quota admission operates on
  requests, untouched. The gang-*training* straggler caveat above is a workload
  property, not a scheduler property.

## Interactions with other systems (quick reference)

Full detail is in design §8.3; the operational preflight is in the
[runbook](runbook.md#preflight).

- **HPA / MPA:** unaffected structurally (they key off requests). Expect
  utilization to reveal true demand once throttling no longer caps it — retune
  thresholds if needed (see [tenant guide](tenant-guide.md#a-note-on-hpa)).
- **VPA:** compose with `controlledValues: RequestsOnly` (VPA owns requests,
  Headroom owns limits) — [recipe](tenant-guide.md#vpa). Default
  `RequestsAndLimits` mode conflicts; exclude those pods.
- **Commercial rightsizers (ScaleOps, Cast AI, StormForge, PerfectScale):** same
  rule as VPA — compatible when they manage requests, conflicting when they set
  or strip limits. Node-agent tools that write cgroups directly (Koordinator,
  Crane) must **not** run on Headroom-managed nodes.
- **ResourceQuota:** managed namespaces must quota on `requests.cpu` **only** — a
  `limits.cpu` quota makes raises 403 (see the [runbook preflight](runbook.md#preflight)).
- **LimitRange:** a `max` on CPU limits caps what Headroom can set; safe, but the
  ceiling plateaus at the `max`.
- **Cluster autoscaler / Karpenter:** operate on requests; unaffected.
