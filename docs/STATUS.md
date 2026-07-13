# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q30

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q24"></a>Q24 | Zero-request containers break steady state | `controller` `policy` | 🔲 | S | Managed pod with a zero-request sidecar gets `limits.cpu:"0"` re-patched every cycle (reads back as unset), breaking §7 zero-churn. Skip request-less containers in split/apply, as the webhook does. |
| <a id="Q21"></a>Q21 | [Migrate packaging from Kustomize to Helm](plan/kustomize-helm-migration.md) | `infra` | 🔲 | L | Two hand-authored Helm charts (CRD + operator) published to ghcr OCI, replacing the `config/` kustomize deploy path; rewrite the Makefile deploy targets to `helm`. Base `config/` fixes (Q17–Q20) have landed; ready to start (plan doc). |
| <a id="Q25"></a>Q25 | Wire up dead config knobs (debounce, client QPS/burst) | `controller` `infra` | 🔲 | M | `debouncePeriod` and `rateLimits.clientQPS/Burst` are populated but never applied (hardcoded 2s debounce, unset rest QPS/Burst), so §8.6 write-pressure bounds are unenforced. Wire them in. |
| <a id="Q26"></a>Q26 | Populate HeadroomConfig status (stub reconciler) | `controller` `observability` | 🔲 | M | `headroomconfig_controller.Reconcile` is the scaffold TODO, so declared status (ManagedPods, ObservedGeneration, Ready) and its printcolumn are always empty. Implement aggregation or drop the fields. |
| <a id="Q27"></a>Q27 | Ship missing §8.1 metrics + Grafana dashboard | `observability` | 🔲 | M | §8.1's `headroom_pod_limit_cores{pod}`, `headroom_pods_managed`, and `dashboards/headroom.json` are absent. Add the per-pod gauge (with series cleanup on delete), managed-pods metric, and dashboard. |
| <a id="Q28"></a>Q28 | e2e coverage for webhook birth-limit path | `tests` | 🔲 | S | §10's "Job gets a boosted limit at admission" criterion is untested; the webhook is only unit-tested via `Default()`, not through the apiserver. Add an e2e Job asserting the birth limit at CREATE. |
| <a id="Q29"></a>Q29 | Fix §5.2 prose overstating the floor under default cap | `docs` | 🔲 | S | §5.2's "10m pod gets +1 core on an empty node" is defeated by the default `maxMultiplier: 10` (→100m), which §5.3 says wins; code is correct. Reword so the example doesn't contradict the cap. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
| <a id="Q22"></a>Q22 | [Promote HeadroomConfig API to v1beta1](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** the spec goes a full release cycle with no field changes and Q21 (Helm) has shipped — or **Demand:** an external consumer needs a supported (non-alpha) API. |
| <a id="Q23"></a>Q23 | [Promote HeadroomConfig API to v1 (GA)](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** v1beta1 (Q22) soaks ≥2 releases with no incompatible change required, and **Decision:** Karl commits to GA backward-compat indefinitely. |
