# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q15

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q2"></a>Q2 | Phase 0 resize spikes on kind ≥1.35 | `spike` | 🔲 | S | §10: CPU limit-only resize latency; can a limit be *added* to a Burstable pod; resize vs ResourceQuota `limits.cpu` admission; VPA `RequestsOnly` coexistence. Each answer changes eligibility or docs. |
| <a id="Q10"></a>Q10 | Enum-keyword label values | `policy` | 🔲 | S | Switch label values from booleans (`enabled: "true"`) to enum keywords (dodges YAML 1.1 coercion, fail-closed)? Affects §6.3, `labels.go`, sample; settle before Q5. |
| <a id="Q4"></a>Q4 | [Node reconciler](plan/node-reconciler.md) | `controller` | 🔲 | M | Pod/node informers keyed on `spec.nodeName`, debounce, hysteresis, per-node rate-limited resize-subresource patching. Wires the pure policy core into controller-runtime (§6.2). |
| <a id="Q5"></a>Q5 | Pod eligibility + dry-run mode | `controller` | 🔲 | S | §6.3 eligibility gates (namespace opt-in, Burstable, resizePolicy≠RestartContainer, exclusions); dry-run computes+annotates+meters without patching (§9.3, ship first). |
| <a id="Q6"></a>Q6 | Mutating admission webhook | `webhook` | 🔲 | S | §6.5 `failurePolicy: Ignore`, namespace-scoped; seeds birth limits for short-lived/boot-time-quota pods and removes the must-pre-declare-limit eligibility rule. |
| <a id="Q7"></a>Q7 | Observability: annotations, events, metrics | `observability` | 🚫 | S | Blocked by [Q4](#Q4). §8.1 two-command explainability: per-pod status annotation, `CPULimitAdjusted` events, Prometheus metrics (node factor/slack, resizes_total, reconcile duration). Follow the [conventions doc](plan/adopt-dev-docs.md) (Q11). |
| <a id="Q8"></a>Q8 | e2e tests on kind ≥1.35 | `tests` | 🚫 | M | Blocked by [Q4](#Q4). §10 exit criteria: empty-node pod unthrottled; neighbor schedule shrinks limit within 5s; controller kill leaves cluster safe; zero steady-state patches. |
| <a id="Q9"></a>Q9 | Adopt design doc + write user docs | `docs` | 🔲 | S | Move `tmp/project-plan.md` (gitignored) to `docs/design.md` (reflect §5.2/§5.3 resolution: caps win); write runbook, tenant guide, applicability matrix (§8.4), when-not-to-use (§8.5). |
| <a id="Q11"></a>Q11 | [Port development-process docs](plan/adopt-dev-docs.md) | `docs` | 🔲 | M | `docs/development/`: test tiers, kind inner-loop, k8s conventions (enum labels, condition/metric/event patterns → informs Q7), tech-debt policy, doc standards. |
| <a id="Q12"></a>Q12 | plan-hygiene CI check | `infra` | 🔲 | S | CI: every `docs/plan/*.md` is STATUS-referenced or archived, and Go code references no plan-doc paths (plan-hygiene). |
| <a id="Q13"></a>Q13 | SECURITY.md + private vuln reporting | `security` | 🔲 | S | Add `SECURITY.md` and enable GitHub private vulnerability reporting; note the no-security-regression expectation. Low urgency (pre-release). |
| <a id="Q14"></a>Q14 | gofmt-on-staged-Go pre-commit hook | `infra` | 🔲 | S | Extend `.githooks/pre-commit` to gofmt staged `.go` files (sub-second); complements the existing backlog-lint hook. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
