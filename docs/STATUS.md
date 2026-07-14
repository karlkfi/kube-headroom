# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q40

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q33"></a>Q33 | Helm: CRD-registration race with crds.install + headroomConfig.create | `infra` | 🔲 | S | CRD lives in templates/, so Helm doesn't wait for Established before creating the CR — a both-flags-on install can fail "no matches for kind HeadroomConfig". Order the CR via a post-install hook. |
| <a id="Q34"></a>Q34 | Test the per-node rate-limiter break path | `tests` | 🔲 | S | Token-bucket break + RequeueAfter (§7 write-pressure bound) is untested. Add an envtest with low perNodePPS and N>1 pods on a node, asserting bounded patches and a non-zero RequeueAfter. |
| <a id="Q36"></a>Q36 | Add values.schema.json to both Helm charts | `infra` | 🔲 | S | Neither chart validates values, so a mistyped toggle (e.g. `webhook.enabled` vs `webhook.enable`) silently no-ops. Add a schema covering the toggles, image.*, replicas, resources, selectors. |
| <a id="Q37"></a>Q37 | Backoff state lost on restart → duplicate failing writes | `controller` | 🔲 | S | Backoff is in-memory only; after restart/failover a backed-off pod (quota-403/Infeasible) is retried at once, re-emitting the failing patch + warning. Rebuild from pod conditions, or document it. |
| <a id="Q39"></a>Q39 | Helm chart ergonomics polish | `infra` | 🔲 | S | Small chart fixes: document the `crds.keep` knob in the operator values.yaml, gate the PDB render on replicas>1 (avoids wedging drains at replicas:1), and support `image.digest` pinning. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
| <a id="Q22"></a>Q22 | [Promote HeadroomConfig API to v1beta1](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** the spec goes a full release cycle with no field changes and Q21 (Helm) has shipped — or **Demand:** an external consumer needs a supported (non-alpha) API. |
| <a id="Q23"></a>Q23 | [Promote HeadroomConfig API to v1 (GA)](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** v1beta1 (Q22) soaks ≥2 releases with no incompatible change required, and **Decision:** Karl commits to GA backward-compat indefinitely. |
