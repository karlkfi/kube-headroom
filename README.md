# Headroom

**Dynamic CPU limits proportional to node slack — recomputed on scheduling
events, applied via the in-place pod resize subresource.**

On an empty node a pod's CPU limit approaches the node's allocatable CPU (no
pointless throttling). As the node fills with requests, limits converge toward
each pod's request (predictable, request-proportional fair sharing). The limit
is derived from **requests** (booked capacity), not live usage, so it changes
only on scheduling events — deterministic, low-churn, and debuggable.

> **Status: early development.** The policy core is implemented and tested; the
> controller is in progress and Headroom is **not yet deployable**. Roadmap and
> priorities live in [docs/STATUS.md](docs/STATUS.md).

## The problem

Static CPU limits force a bad choice in multi-tenant clusters:

- **Tight limit** → the pod is throttled by CFS quota even when the node is
  idle (and CFS throttling is notoriously hard to debug).
- **Generous / no limit** → no isolation ceiling; one tenant's spike degrades
  neighbors, with no predictable behavior under contention.
- **requests == limits (Guaranteed)** → wastes the gap between average and peak
  usage; nodes bin-pack poorly.

The kernel already handles part of this: CPU **requests** map to cgroup
`cpu.weight`, which gives work-conserving, request-proportional sharing under
contention *without throttling*. The only thing that throttles is the quota
(`cpu.max`, i.e. the limit). So the real question is: **what should the ceiling
be right now?**

## How it works

Headroom's answer: your ceiling is your request plus your proportional share of
the node's *unbooked* capacity.

```
slack(node)  = allocatable_cpu − Σ requests(all pods on node)
limit(pod_i) = request_i × (1 + slack / Σ requests(managed pods))
```

with a slack-aware floor (so tiny requests still get useful burst on empty
nodes) and per-pod caps. When the node is fully booked, slack is 0 and every
limit collapses to its request — exactly what `cpu.weight` would enforce under
contention anyway, so behavior is coherent at both extremes.

The limit is applied through Kubernetes' **in-place pod resize** subresource
(GA in 1.35): a live cgroup write, no container restart. The pod spec stays the
source of truth — `kubectl get pod` shows the actually-enforced limit.

## Key properties

- **Requests-driven, not usage-driven** — targets change only when a node's
  booking changes. Deterministic, auditable, no metric-pipeline dependency, no
  feedback oscillation.
- **Safe by construction** — if the controller stops, limits freeze at their
  last values; `cpu.weight` still enforces fair sharing. *No failure mode is
  worse than not running Headroom.*
- **CPU-only, by design** — CPU is compressible (exceeding the ceiling
  throttles); memory/GPU/storage are not (they kill), so the "ceiling floats
  with slack" model only makes sense for CPU.
- **Opt-in per namespace**, safe to run alongside unmanaged workloads.
- **Debuggable** — every limit change is explainable from observable inputs
  (annotation + event + metrics).

## When to use it — and when not to

Headroom earns its complexity only where CPU **ceilings are actually required**:
hostile or contractual multi-tenancy, bounding the blast radius of runaway
workloads, and predictable per-tenant capacity planning.

On a trusted, single-tenant cluster, simply **omitting CPU limits** (or running
kubelet with `--cpu-cfs-quota=false`) delivers most of the benefit with zero
moving parts — `cpu.weight` provides work-conserving proportional sharing and
nothing ever throttles. If that describes your cluster, you don't need Headroom.

## Configuration

A single cluster-scoped `HeadroomConfig` (name `cluster`) holds the policy: the
burst floor, max multiplier, deadband/hysteresis, debounce, rate limits, and the
namespace selector. It **defaults to `dryRun: true`** — Headroom computes
targets, annotates pods, and emits metrics, but issues no resize patches until
you flip it off. See [config/samples](config/samples/) for a fully-populated
example.

## Non-goals

Memory/GPU/storage management, usage-based reclamation, managing Guaranteed or
BestEffort pods, and any influence on scheduling (Headroom only adjusts limits
*after* placement). Windows nodes and nodes using static CPU/Memory Manager
policies are structurally excluded (in-place resize is unavailable there).

## Development

Requires Go (see `go.mod`), Docker, and kind ≥ 1.35 for e2e. See
[CLAUDE.md](CLAUDE.md) for the full command reference and conventions.

```sh
go test ./internal/policy/   # fast policy unit tests (no cluster)
make test                    # full unit tests (envtest)
make manifests generate      # regenerate CRDs/RBAC/deepcopy after API changes
make run                     # run the manager against the current kubecontext
```

## Documentation

- **[Design doc](docs/design.md)** — architecture, policy, and rationale (the
  source of truth).
- **[Operator runbook](docs/runbook.md)** — preflight, rollout, and day-2 triage.
- **[Tenant guide](docs/tenant-guide.md)** — the contract for app teams, plus the
  VPA coexistence recipe.
- **[Applicability matrix](docs/applicability.md)** — when to use Headroom, and
  when not to.

In-flight plan docs live under `docs/plan/`.
