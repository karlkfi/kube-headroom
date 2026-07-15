# Development process

Lightweight development-process docs for kube-headroom, scaled to a solo
project. Each page is short and links its canonical source rather than
restating it.

| Doc | Covers |
|---|---|
| [testing.md](testing.md) | The three test tiers (unit / envtest / kind e2e) and how to pick and pace them. |
| [kubernetes-conventions.md](kubernetes-conventions.md) | Enum-keyword labels, keys-as-consts, condition ladder, event and metric patterns. Informs Q7. |
| [kind-iteration.md](kind-iteration.md) | The fast inner loop against a reused kind cluster. |
| [documentation-standards.md](documentation-standards.md) | Anti-slop rules, canonical-home-plus-link, one term per concept. |
| [technical-debt.md](technical-debt.md) | The fix / flag / defer / decline policy. |
| [releasing.md](releasing.md) | Cutting a release: tag → pipeline → verify → announce, and the bare-version image-tag invariant. |

For *what* to build and in what order, see [../STATUS.md](../STATUS.md) (the
backlog). For *why* it is built this way, see the design doc (`../design.md`,
once adopted) and the plan docs under [../plan/](../plan/). This tree is *how*
we work day to day.
