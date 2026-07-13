# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q21

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q17"></a>Q17 | Narrow controller RBAC on headroomconfigs | `security` | 🔲 | S | ClusterRole grants full CRUD on `headroomconfigs`, but the controller only reads the singleton. Narrow the marker in `headroomconfig_controller.go` to `get;list;watch`+`/status`, regenerate. |
| <a id="Q18"></a>Q18 | Bound the birth-limit multiplier | `webhook` `security` | 🔲 | S | CRD multiplier fields have no max and webhook `seedBirthLimits` applies `request × multiplier` uncapped. Add validation ceilings and clamp seeding to `MaxMultiplier`. |
| <a id="Q19"></a>Q19 | Fix allow-webhook-traffic NetworkPolicy | `infra` `security` | 🔲 | S | Policy admits ingress only from `webhook: enabled` namespaces, but apiserver→webhook traffic is not, so enabling it (now commented out) breaks admission. Fix the selector before wiring it in. |
| <a id="Q20"></a>Q20 | Replace scaffold production defaults | `infra` | 🔲 | S | `cmd/main.go` sets `zap.Options{Development: true}` (stacktraces, non-JSON); set false for prod. Document the metrics cert-manager wiring so scrapers aren't stuck on self-signed certs. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
