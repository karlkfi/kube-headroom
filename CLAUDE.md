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
- **Licensing** — Apache-2.0 (root `LICENSE`, copyright Karl Isenberg). Do
  **not** add per-file license/copyright headers: `hack/boilerplate.go.txt` is
  intentionally empty so generated files stay header-free; keep it that way.
- **Releases** — a `v*` tag runs `.github/workflows/release.yml` (image →
  charts → GitHub Release). Process and verification:
  `docs/development/releasing.md`. Key invariant: image tags are the bare
  version (`0.1.0`, no `v`) to match the stamped chart `appVersion`.

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
- `config/` — kustomize manifests (CRD, RBAC, manager). Since Q21 these are the
  **generation source only**, not the deploy path: `make manifests generate`
  still owns `config/crd/bases`, `config/rbac/role.yaml`, and
  `config/webhook/manifests.yaml`, and `make helm-sync` copies those into the
  charts. Deploy is Helm (`charts/`), not `kubectl apply -k`.
- `charts/` — two hand-authored Helm charts, the deploy artifact (Q21):
  `kube-headroom-crds` (the CRD, cluster-wide, `resource-policy: keep`) and
  `kube-headroom` (the namespaced operator). `scripts/helm-sync.sh` syncs the
  generated CRD/RBAC/webhook manifests in; never hand-edit the synced files
  (`charts/**/files/*`, the CRD templates) — regenerate with
  `make manifests generate helm-sync`. Published as OCI artifacts to
  `oci://ghcr.io/karlkfi/charts`.
- `docs/plan/` — plan docs for M/L backlog items.
- `website/` — the public site at `kube-headroom.dev`, a **Node** project
  (not Go, not the Makefile): `website/landing/` is hand-written HTML owning
  the site root, and `website/.vitepress/` builds `docs/**` into the `/docs/`
  subsite. `.github/workflows/website.yml` assembles the two into one `dist/`
  and deploys to GitHub Pages. The landing half is *stamped*, not copied:
  `make site-landing` substitutes `{{VERSION}}` and `{{ANNOUNCEMENT}}` from
  `website/release.json` and rejects a bare version written into the HTML, so
  bump `release.json` at release time and never a version literal. Requires
  Node 22 + `npm ci` in `website/` —
  without it there is no local preview and changes can only be verified from
  CI's build artifact.
- `tools/` — separate Go module (`//go:build tools` pattern) pinning the
  build-tool versions (controller-gen, kustomize, golangci-lint, govulncheck,
  helm). The Makefile `go build`s each from here; bump versions in
  `tools/go.mod`, not the Makefile.

Build: `make manifests generate` after editing API types; `make build` /
`make test` (envtest). `make run` runs the manager against the current
kubecontext.

## Working in this repo (kubebuilder v4)

- **Never hand-edit generated files** — they are overwritten by `make`:
  `**/zz_generated.*.go`, `config/crd/bases/*.yaml`, `config/rbac/role.yaml`,
  `config/webhook/manifests.yaml`, `PROJECT`, and the helm-synced files under
  `charts/**` (`charts/*/templates/*crd*.yaml`, `charts/kube-headroom/files/*`).
  Change the source markers instead, then regenerate.
- **After editing `api/**/_types.go` or markers**, run
  `make manifests generate helm-sync` (the `helm-sync` step re-copies the
  generated CRD/RBAC/webhook into the charts; CI's `verify-helm-sync` fails on
  drift). After editing `.go`, run `make test` (unit tests use envtest — a real
  apiserver + etcd; Ginkgo/Gomega, see `suite_test.go`).
- **Don't delete `// +kubebuilder:scaffold:*` comments** — the CLI injects code
  at them. Scaffold new APIs/webhooks with `kubebuilder create api|webhook`
  rather than creating files by hand.
- **Dependencies are vendored** — the root module's deps live in `vendor/`
  (checked in), so `go`/`make` build in `-mod=vendor` mode. Never hand-edit
  `vendor/`. After changing imports or `go.mod`, run `make vendor` (`go mod tidy`
  + `go mod vendor`) and commit `go.mod`, `go.sum`, and `vendor/` together; CI's
  `verify-vendor` fails on drift. The separate `tools/` module is **not**
  vendored — its build tools resolve from the module cache.
- **e2e requires a dedicated kind cluster** (never a real dev/prod context).
- **Editing `.githooks/*` from a git worktree?** `core.hooksPath` is an
  absolute path to the *main* checkout's `.githooks`, so `git commit` in a
  worktree runs the main copy, not your edited one — test the change by
  invoking the script directly (`bash .githooks/pre-commit`) rather than
  trusting a commit to exercise it.
- **Adding a page under `docs/`?** Add its meta description to the
  `descriptions` map in `website/.vitepress/config.mjs`, keyed by path relative
  to `docs/`. Descriptions live there rather than in markdown frontmatter
  because `docs/` is read on GitHub too, where a frontmatter block renders as a
  stray table above the first heading. A page with no entry silently inherits
  the site-wide description — CI's site-metadata check fails on the resulting
  duplicate. Also add it to the sidebar in the same file.
- Full kubebuilder reference: https://book.kubebuilder.io.

> `AGENTS.md` is a symlink to this file — one source of truth for every agent
> tool. Edit `CLAUDE.md`.

## Commands

| Task | Command |
|---|---|
| Policy unit tests (fast, no cluster) | `go test ./internal/policy/` |
| Full unit tests (envtest) | `make test` |
| Regenerate CRDs/RBAC + deepcopy, sync charts | `make manifests generate helm-sync` |
| Refresh vendored deps (after import/`go.mod` changes) | `make vendor` |
| Build the manager binary | `make build` |
| Lint | `make lint` (fix: `make lint-fix`) |
| Lint / render the charts | `make helm-lint helm-template` |
| Run against current kubecontext | `make run` |
| Install CRD chart / deploy operator | `make install` / `make deploy IMG=…` |
| e2e on a dedicated kind cluster | `make setup-test-e2e test-e2e` |
| Apply the sample config | `kubectl apply -k config/samples` |
| Lint the backlog | `bash scripts/lint-backlog.sh docs/STATUS.md` |
| Check workflow supply-chain hygiene | `make workflow-hygiene` |
| Install website deps (Node 22) | `cd website && npm ci` |
| Preview the docs subsite | `cd website && npm run docs:dev` |
| Build the docs subsite | `cd website && npm run docs:build` |
| Assemble the landing pages, stamping the release | `make site-landing DIST=dist` |
| Check a built site's SEO metadata | `make site-metadata DIST=dist` |

**Verify a change** end-to-end before opening a PR: `make test` for anything
touching Go, plus `make manifests generate helm-sync` (and commit the
regenerated files) whenever API markers change — CI's `verify-helm-sync` fails
if the charts drift from `config/**`. The sample (`name: cluster`) exercises the
singleton CEL rule.
