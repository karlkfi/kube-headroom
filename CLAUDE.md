# kube-headroom

**Headroom** is a Kubernetes controller that dynamically sets container **CPU
limits** as a function of node slack, recomputed on scheduling events, via the
GA in-place pod resize subresource. Requests-driven (deterministic, low-churn),
CPU-only by design, opt-in per namespace. Full design: `docs/design.md` (until
adopted, `tmp/project-plan.md`).

## Backlog

Work is tracked in `docs/STATUS.md` (repo-local backlog, priority-ordered
Queue). Conventions:

- Pick the next task from the **top of the Queue**; run `gh pr list` before
  picking (an open PR is the in-flight signal).
- Commit `docs/STATUS.md` changes in **isolated** `docs(status):` commits —
  never mixed with code or plan-doc changes.
- The `**Next ID:**` counter allocates new Q-IDs; bump it in the same edit.
  Reference rows by bare Q-ID (`Q4`), never `#4`.

## Layout

kubebuilder v4 project. API group / annotation prefix: **`kube-headroom.dev`**
(defined once as `v1alpha1.GroupName`).

- `internal/policy/` — pure, k8s-free policy core (`ComputeNode`); table +
  property tests cover every §5 invariant.
- `api/v1alpha1/` — `HeadroomConfig` CRD (cluster-scoped singleton, name
  `cluster`; §9.3) and the label/annotation key constants.
- `internal/controller/` — controllers (node reconciler lands in Q4).
- `config/` — kustomize manifests (CRD, RBAC, manager).
- `docs/plan/` — plan docs for M/L backlog items.

Build: `make manifests generate` after editing API types; `make build` /
`make test` (envtest). `make run` runs the manager against the current
kubecontext.
