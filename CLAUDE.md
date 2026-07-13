# kube-headroom

**Headroom** is a Kubernetes controller that dynamically sets container **CPU
limits** as a function of node slack, recomputed on scheduling events, via the
GA in-place pod resize subresource. Requests-driven (deterministic, low-churn),
CPU-only by design, opt-in per namespace. Full design: `docs/design.md`. User
docs: `docs/runbook.md` (operators), `docs/tenant-guide.md` (app teams),
`docs/applicability.md` (when to use / not use).

## Workflow

- **Feature branches + PRs**, never direct commits to `main`. Branch per task
  (`feat/…`, `chore/…`, `fix/…`), push, open a PR with `gh pr create`; Karl
  merges. CI (lint, test, e2e, backlog) runs on the PR.
- **Conventional commits** (`feat:`, `fix:`, `chore:`, `docs:`, `test:`).
- **No licensing** — do not add license/copyright headers or a `LICENSE` file
  until Karl explicitly asks. `hack/boilerplate.go.txt` is intentionally empty
  so generated files stay header-free; keep it that way.

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
- `tools/` — separate Go module (`//go:build tools` pattern) pinning the
  build-tool versions (controller-gen, kustomize, golangci-lint, govulncheck).
  The Makefile `go build`s each from here; bump versions in `tools/go.mod`, not
  the Makefile.

Build: `make manifests generate` after editing API types; `make build` /
`make test` (envtest). `make run` runs the manager against the current
kubecontext.

## Working in this repo (kubebuilder v4)

- **Never hand-edit generated files** — they are overwritten by `make`:
  `**/zz_generated.*.go`, `config/crd/bases/*.yaml`, `config/rbac/role.yaml`,
  `config/webhook/manifests.yaml`, and `PROJECT`. Change the source markers
  instead, then regenerate.
- **After editing `api/**/_types.go` or markers**, run `make manifests generate`.
  After editing `.go`, run `make test` (unit tests use envtest — a real
  apiserver + etcd; Ginkgo/Gomega, see `suite_test.go`).
- **Don't delete `// +kubebuilder:scaffold:*` comments** — the CLI injects code
  at them. Scaffold new APIs/webhooks with `kubebuilder create api|webhook`
  rather than creating files by hand.
- **e2e requires a dedicated kind cluster** (never a real dev/prod context).
- **Editing `.githooks/*` from a git worktree?** `core.hooksPath` is an
  absolute path to the *main* checkout's `.githooks`, so `git commit` in a
  worktree runs the main copy, not your edited one — test the change by
  invoking the script directly (`bash .githooks/pre-commit`) rather than
  trusting a commit to exercise it.
- Full kubebuilder reference: https://book.kubebuilder.io.

> `AGENTS.md` is a symlink to this file — one source of truth for every agent
> tool. Edit `CLAUDE.md`.

## Commands

| Task | Command |
|---|---|
| Policy unit tests (fast, no cluster) | `go test ./internal/policy/` |
| Full unit tests (envtest) | `make test` |
| Regenerate CRDs/RBAC + deepcopy | `make manifests generate` |
| Build the manager binary | `make build` |
| Lint | `make lint` (fix: `make lint-fix`) |
| Run against current kubecontext | `make run` |
| e2e on a dedicated kind cluster | `make setup-test-e2e test-e2e` |
| Apply the sample config | `kubectl apply -k config/samples` |
| Lint the backlog | `bash scripts/lint-backlog.sh docs/STATUS.md` |

**Verify a change** end-to-end before opening a PR: `make test` for anything
touching Go, plus `make manifests generate` (and commit the regenerated files)
whenever API markers change. The sample (`name: cluster`) exercises the
singleton CEL rule.
