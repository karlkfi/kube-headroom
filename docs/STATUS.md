# Project Status

Single source of truth for progress and priorities. Pick the next task from
the top of the Queue.

**Status:** 🔲 ready · 🚫 blocked
**Size:**   S = one session/PR · M = 2–3 sessions · L = needs a plan doc under `docs/plan/`
**Labels:** `policy` `controller` `webhook` `observability` `tests` `docs` `infra` `security` `spike`
**Next ID:** Q44

## Queue

| ID | Item | Labels | St | Sz | Notes |
|---|---|---|---|---|---|
| <a id="Q41"></a>Q41 | Apache-2.0 licensing | `docs` `infra` | 🔲 | S | Karl lifted the no-license rule (2026-07). Root `LICENSE` (no per-file headers), README section (© Karl Isenberg), chart/package.json license fields, policy-doc rewrites. |
| <a id="Q42"></a>Q42 | Release automation: v* tag → image + charts + GitHub Release | `infra` | 🔲 | S | release.yml: multi-arch image (bare-version tag = chart appVersion), charts gated on image, `gh release create`. Then ops: flip public, enable Pages, tag v0.1.0. |
| <a id="Q43"></a>Q43 | v0.1.0 announcement: landing banner, News page, status copy | `docs` | 🔲 | S | `docs/news/` (index + v0.1.0 post), News nav/sidebar in VitePress, announce banner on landing, retire "not yet deployable" copy in README + landing status card. Merge only after the v0.1.0 tag exists. |
| <a id="Q40"></a>Q40 | Project website on GitHub Pages | `docs` `infra` | 🔲 | M | `website/`: hand-crafted landing (A3 mark, night-drive hero, live slack widget) + VitePress docs + Pages workflow. v2: OG image, custom domain, tenant pages. |
| <a id="Q39"></a>Q39 | Helm chart ergonomics polish | `infra` | 🔲 | S | Two chart fixes: gate the PDB render on replicas>1 (avoids wedging drains at replicas:1), and support `image.digest` pinning. The audit's crds.keep item is moot — Q33 removed that knob. |

## Deferred

| ID | Item | Labels | Sz | Trigger to revive |
|---|---|---|---|---|
| <a id="Q22"></a>Q22 | [Promote HeadroomConfig API to v1beta1](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** the spec goes a full release cycle with no field changes and Q21 (Helm) has shipped — or **Demand:** an external consumer needs a supported (non-alpha) API. |
| <a id="Q23"></a>Q23 | [Promote HeadroomConfig API to v1 (GA)](plan/api-version-progression.md) | `webhook` `docs` | L | **Event:** v1beta1 (Q22) soaks ≥2 releases with no incompatible change required, and **Decision:** Karl commits to GA backward-compat indefinitely. |
