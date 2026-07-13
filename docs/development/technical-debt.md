# Technical debt policy

Every shortcut, rough edge, or deferred cleanup gets one of four dispositions.
Decide explicitly — un-triaged debt is the kind that hurts.

## Fix / flag / defer / decline

- **Fix** — do it now. The cost to fix is small, or the cost of leaving it is
  compounding (it will make the next change harder, or it's a correctness or
  security issue). Default to fix for anything cheap.
- **Flag** — can't fix now, but it's real and someone will hit it. File a
  backlog row in [../STATUS.md](../STATUS.md) with enough context to act on
  later, and leave a code comment linking the row if the debt lives at a
  specific site. A flag is a *tracked* promise, not a `// TODO` that rots.
- **Defer** — real but not yet worth doing, with a concrete revive trigger. Put
  it in the **Deferred** table in STATUS.md with the condition that should bring
  it back (e.g. "revive when `docs/operations/` exists"). Deferring without a
  trigger is just declining with extra steps.
- **Decline** — a deliberate non-goal. Say so, once, in the canonical place (the
  design doc's out-of-scope list or a plan doc), so it doesn't get re-proposed.
  CPU-only, requests-driven, no license-until-asked are declines, not
  oversights.

When unsure between flag and defer: flag is "we will do this, unscheduled,"
defer is "we will do this only if X happens." Both are backlog rows; the
difference is whether there's a trigger.

## Flake fixes go to the top of the queue

A flaky test is worse than a missing one: it trains everyone to ignore red,
and then a real failure hides in the noise. When a test flakes, fixing it is
**not** deferrable — it jumps to the top of the queue ahead of feature work.
See [testing.md](testing.md) for the tier budgets that keep tests trustworthy in
the first place.

## Secure-by-default is non-negotiable

Security posture is never traded for convenience or velocity. The gates are
**fail-closed** (an unrecognized `mode` label means *not managed*, never
managed by accident — see
[kubernetes-conventions.md](kubernetes-conventions.md)); the webhook is
`failurePolicy: Ignore` so it can never wedge scheduling; the controller stays
scoped to the namespaces that opted in. A change that weakens any of these is a
**decline**, not a debt to be flagged and paid down later. "We'll secure it in a
follow-up" is not an option this project accepts.
