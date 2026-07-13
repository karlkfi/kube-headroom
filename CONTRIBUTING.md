# Contributing

This is currently a solo project, but it's structured so that both humans and AI
coding agents can work in it predictably. `CLAUDE.md` (symlinked as `AGENTS.md`)
is the agent-facing companion to this guide; the two intentionally overlap.

## Workflow

- **Feature branches + PRs.** No direct commits to `main`. Branch per task
  (`feat/…`, `fix/…`, `chore/…`, `docs/…`), push, and open a PR — CI runs there.
- **Conventional Commits**: `feat:`, `fix:`, `chore:`, `docs:`, `test:`,
  `ci:`. Keep commits small and focused; never commit knowingly-broken code.
- **Design-first.** For non-trivial work, read the [design doc](docs/design.md)
  and any relevant `docs/plan/` doc. User-facing behavior is documented in
  [`docs/runbook.md`](docs/runbook.md), [`docs/tenant-guide.md`](docs/tenant-guide.md),
  and [`docs/applicability.md`](docs/applicability.md).

## Before you open a PR

Run the one-command gate, which mirrors CI:

```sh
make check
```

It runs: `golangci-lint`, generated-file drift check, backlog lint, shellcheck,
doc-link check, and the unit tests (envtest). **CI is authoritative** — some
tools (`shellcheck`, and heavier tiers) are skipped locally if absent, so a
green `make check` is a strong signal, not a guarantee.

For controller changes, also run the e2e suite against a throwaway kind cluster:

```sh
make test-e2e   # never against a real dev/prod cluster
```

## Build, test, generate

| Task | Command |
|---|---|
| Fast policy unit tests | `go test ./internal/policy/` |
| Full unit tests (envtest) | `make test` |
| Regenerate CRDs/RBAC/deepcopy | `make manifests generate` |
| Vulnerability scan | `make govulncheck` |
| Run against current kubecontext | `make run` |

**After editing API types or `+kubebuilder:` markers**, run
`make manifests generate` and commit the result — the `generate-drift` CI check
fails otherwise. Never hand-edit generated files (`**/zz_generated*.go`,
`config/crd`, `config/rbac`, `PROJECT`).

## Backlog

Work is tracked in [`docs/STATUS.md`](docs/STATUS.md) (a priority-ordered
Queue). Pick from the top; run `gh pr list` first (an open PR is the in-flight
signal). Commit `docs/STATUS.md` changes in **isolated** `docs(status):` commits
— never mixed with code. The pre-commit hook (installed via `git config
core.hooksPath .githooks`) enforces this and lints the file.

## Security

Found a vulnerability? Don't open a public issue — follow
[`SECURITY.md`](SECURITY.md) (GitHub private vulnerability reporting). Changes
are expected not to regress security posture; CI runs `govulncheck` on every PR.

## Licensing

This repo intentionally has **no license** yet — do not add license/copyright
headers or a `LICENSE` file until the maintainer asks. `hack/boilerplate.go.txt`
is deliberately empty so generated files stay header-free.
