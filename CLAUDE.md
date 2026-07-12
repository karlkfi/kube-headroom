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

- `internal/policy/` — pure, k8s-free policy core (`ComputeNode`); table +
  property tests cover every §5 invariant.
- `docs/plan/` — plan docs for M/L backlog items.
