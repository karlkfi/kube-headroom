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

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
| <a id="Q22"></a>Q22 | [Promote HeadroomConfig API to v1beta1](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** the spec goes a full release cycle with no field changes and Q21 (Helm) has shipped — or **Demand:** an external consumer needs a supported (non-alpha) API. |
| <a id="Q23"></a>Q23 | [Promote HeadroomConfig API to v1 (GA)](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** v1beta1 (Q22) soaks ≥2 releases with no incompatible change required, and **Decision:** Karl commits to GA backward-compat indefinitely. |
