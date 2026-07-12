# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `spike`
**Next ID:** Q10

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q2"></a>Q2 | Phase 0 resize spikes on kind ≥1.35 | `spike` | 🔲 | S | §10: CPU limit-only resize latency; can a limit be *added* to a Burstable pod; resize vs ResourceQuota `limits.cpu` admission; VPA `RequestsOnly` coexistence. Each answer changes eligibility or docs. |
| <a id="Q4"></a>Q4 | [Node reconciler](plan/node-reconciler.md) | `controller` | 🔲 | M | Pod/node informers keyed on `spec.nodeName`, debounce, hysteresis, per-node rate-limited resize-subresource patching. Wires the pure policy core into controller-runtime (§6.2). |
| <a id="Q5"></a>Q5 | Pod eligibility + dry-run mode | `controller` | 🔲 | S | §6.3 eligibility gates (namespace opt-in, Burstable, resizePolicy≠RestartContainer, exclusions); dry-run computes+annotates+meters without patching (§9.3, ship first). |
| <a id="Q6"></a>Q6 | Mutating admission webhook | `webhook` | 🔲 | S | §6.5 `failurePolicy: Ignore`, namespace-scoped; seeds birth limits for short-lived/boot-time-quota pods and removes the must-pre-declare-limit eligibility rule. |
| <a id="Q7"></a>Q7 | Observability: annotations, events, metrics | `observability` | 🚫 | S | Blocked by [Q4](#Q4). §8.1 two-command explainability: per-pod status annotation, `CPULimitAdjusted` events, Prometheus metrics (node factor/slack, resizes_total, reconcile duration). |
| <a id="Q8"></a>Q8 | e2e tests on kind ≥1.35 | `tests` | 🚫 | M | Blocked by [Q4](#Q4). §10 exit criteria: empty-node pod unthrottled; neighbor schedule shrinks limit within 5s; controller kill leaves cluster safe; zero steady-state patches. |
| <a id="Q9"></a>Q9 | Adopt design doc + write user docs | `docs` | 🔲 | S | Move `tmp/project-plan.md` (gitignored) to `docs/design.md` (reflect §5.2/§5.3 resolution: caps win); write runbook, tenant guide, applicability matrix (§8.4), when-not-to-use (§8.5). |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
