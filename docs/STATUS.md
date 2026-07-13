# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q17

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q5"></a>Q5 | Pod eligibility + dry-run mode | `controller` | 🔲 | S | §6.3 gates (ns opt-in, Burstable, **CPU request > 0**, resizePolicy≠RestartContainer, exclusions); controller adds the limit, pre-declare gate dropped ([spike](plan/phase0-resize-spike.md)); dry-run meters, no patch (§9.3, ship first). |
| <a id="Q6"></a>Q6 | Mutating admission webhook | `webhook` | 🔲 | S | §6.5 `failurePolicy: Ignore`, namespace-scoped; seeds birth limits for short-lived/boot-time-quota pods. Not needed for long-lived-pod manageability — the controller adds limits itself ([spike](plan/phase0-resize-spike.md)). |
| <a id="Q7"></a>Q7 | Observability: annotations, events, metrics | `observability` | 🔲 | S | §8.1 two-command explainability: per-pod status annotation, `CPULimitAdjusted` events, Prometheus metrics (node factor/slack, resizes_total, reconcile duration). Follow the [conventions doc](plan/adopt-dev-docs.md) (Q11). |
| <a id="Q8"></a>Q8 | e2e tests on kind ≥1.35 | `tests` | 🔲 | M | §10 exit criteria: empty-node pod unthrottled; neighbor schedule shrinks limit within 5s; controller kill leaves cluster safe; zero steady-state patches. |
| <a id="Q9"></a>Q9 | Adopt design doc + write user docs | `docs` | 🔲 | S | Move `tmp/project-plan.md` → `docs/design.md` (§5.2/§5.3: caps win); runbook, tenant guide, applicability matrix + when-not-to-use (§8.4–8.5). Fold in [spike](plan/phase0-resize-spike.md): quota `requests.cpu` only, VPA `RequestsOnly` recipe. |
| <a id="Q11"></a>Q11 | [Port development-process docs](plan/adopt-dev-docs.md) | `docs` | 🔲 | M | `docs/development/`: test tiers, kind inner-loop, k8s conventions (enum labels, condition/metric/event patterns → informs Q7), tech-debt policy, doc standards. |
| <a id="Q12"></a>Q12 | plan-hygiene CI check | `infra` | 🔲 | S | CI: every `docs/plan/*.md` is STATUS-referenced or archived, and Go code references no plan-doc paths (plan-hygiene). |
| <a id="Q13"></a>Q13 | SECURITY.md + private vuln reporting | `security` | 🔲 | S | Add `SECURITY.md` and enable GitHub private vulnerability reporting; note the no-security-regression expectation. Low urgency (pre-release). |
| <a id="Q14"></a>Q14 | gofmt-on-staged-Go pre-commit hook | `infra` | 🔲 | S | Extend `.githooks/pre-commit` to gofmt staged `.go` files (sub-second); complements the existing backlog-lint hook. |
| <a id="Q15"></a>Q15 | Pin build tools in a `tools/` submodule | `infra` | 🔲 | S | Move the Makefile's go-installed tools (controller-gen, kustomize, golangci-lint, govulncheck) into a `tools/` submodule that pins versions via `//go:build tools`. Retires `govulncheck@latest`. |
| <a id="Q16"></a>Q16 | Automate tool-version bumps | `infra` | 🚫 | S | Blocked by [Q15](#Q15). Add a Dependabot `gomod` entry for the `tools/` module so tool versions get automated weekly bump PRs — impossible while they're Makefile string vars. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
