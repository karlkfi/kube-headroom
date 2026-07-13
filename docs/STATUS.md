# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q22

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q21"></a>Q21 | [Migrate packaging from Kustomize to Helm](plan/kustomize-helm-migration.md) | `infra` | 🔲 | L | Two hand-authored Helm charts (CRD + operator) published to ghcr OCI, replacing the `config/` kustomize deploy path; rewrite the Makefile deploy targets to `helm`. Start after the open `config/` fixes (Q17–Q20) settle (plan doc). |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
