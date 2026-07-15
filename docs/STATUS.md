# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q45

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q39"></a>Q39 | Helm chart ergonomics polish | `infra` | 🔲 | S | Two chart fixes: gate the PDB render on replicas>1 (avoids wedging drains at replicas:1), and support `image.digest` pinning. The audit's crds.keep item is moot — Q33 removed that knob. |
| <a id="Q44"></a>Q44 | Website v2 polish | `docs` | 🔲 | S | Leftovers from Q40/Q43 (site live at kube-headroom.dev since 2026-07-15): OG image PNG, more tenant pages, fix nav/hero overlap at ~375px width. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
| <a id="Q22"></a>Q22 | [Promote HeadroomConfig API to v1beta1](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** the spec goes a full release cycle with no field changes and Q21 (Helm) has shipped — or **Demand:** an external consumer needs a supported (non-alpha) API. |
| <a id="Q23"></a>Q23 | [Promote HeadroomConfig API to v1 (GA)](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** v1beta1 (Q22) soaks ≥2 releases with no incompatible change required, and **Decision:** Karl commits to GA backward-compat indefinitely. |
