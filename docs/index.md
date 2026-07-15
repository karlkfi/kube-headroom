# Headroom documentation

Headroom is a Kubernetes controller that dynamically sets container **CPU
limits** as a function of node slack — recomputed on scheduling events, applied
via the GA in-place pod resize subresource. Requests-driven, CPU-only by
design, opt-in per namespace.

Start with the page that matches your role:

## Architecture

- **[Design doc](design.md)** — architecture, policy, and rationale. The source
  of truth; everything else links here.

## Operators

- **[Runbook](runbook.md)** — preflight, rollout, and day-2 triage.
- **[Helm migration](helm-migration.md)** — moving from `kubectl apply -k` to
  the Helm charts.

## App teams

- **[Tenant guide](tenant-guide.md)** — the contract for teams whose namespaces
  are enrolled, plus the VPA coexistence recipe and the JVM/quota caveat.

## Adoption

- **[Applicability matrix](applicability.md)** — when to use Headroom, and when
  *not* to: workload classes, scheduler modes, and interactions with
  HPA/VPA/quota tooling.

## Contributing

- **[Development process](development/README.md)** — the process doc hub:
  [testing](development/testing.md),
  [the kind inner loop](development/kind-iteration.md),
  [Kubernetes conventions](development/kubernetes-conventions.md),
  [documentation standards](development/documentation-standards.md), the
  [technical debt policy](development/technical-debt.md), and
  [releasing](development/releasing.md).

Work is tracked in the repo-local backlog,
[docs/STATUS.md](https://github.com/karlkfi/kube-headroom/blob/main/docs/STATUS.md).
