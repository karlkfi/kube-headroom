# Kubernetes conventions

House rules for how Headroom presents itself on the API surface — labels,
annotations, conditions, events, and metrics. These keep the controller
explainable with two commands (`kubectl describe` + a metrics scrape) and keep
the observability work (Q7) consistent. **This doc informs Q7**; treat it as the
spec for those patterns until they land.

## Enum-keyword label values, never booleans

Label and annotation *values* that select behavior are enum keywords
(`managed`, `unmanaged`), not booleans. Canonical source and rationale:
[`api/v1alpha1/labels.go`](../../api/v1alpha1/labels.go).

The reason is YAML 1.1 coercion: an unquoted `true` / `false` / `yes` / `no` is
parsed as a boolean by many tools before Headroom ever sees the string, so a
boolean-valued gate can silently flip meaning. Enum keywords can't coerce. The
gate is **fail-closed**: any absent or unrecognized value means "not managed."
Q10 established this; new selecting values follow it.

## Keys are constants, derived from one prefix

Every label and annotation key is a `const` in `labels.go`, derived from the
single `GroupName` (`kube-headroom.dev`). Never write a key as a string literal
in a reconciler or test — reference the const. This makes the prefix a one-line
migration and makes `rg` find every use.

## Recommended `app.kubernetes.io/*` labels — via a helper, never as selectors

Apply the standard `app.kubernetes.io/*` recommended labels
([k8s docs](https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/))
to objects Headroom creates, through **one shared helper** so the set stays
identical everywhere. Do **not** use them in label selectors or watch filters:
recommended labels are descriptive metadata that operators and tooling may
rewrite, so selecting on them makes the controller's correctness depend on
values it doesn't own. Select on Headroom's own `kube-headroom.dev/*` keys.

## The two-tier condition ladder

Status conditions (`[]metav1.Condition` on `HeadroomConfigStatus`, and later on
pods) come in two tiers:

- **Availability** — one top-line condition (e.g. `Ready` / `Available`) that
  answers "is this working right now?" This is what an operator and an alert
  read first.
- **Detail** — narrower conditions that explain *why* the top-line is what it
  is (e.g. degraded input, an infeasible resize, a missing config singleton).

Set conditions with `meta.SetStatusCondition` so `observedGeneration` and
transition timestamps are handled correctly. Keep `Reason` a stable PascalCase
token (see below) and put human detail in `Message`.

## Events on lifecycle transitions only

Emit an Event when something *changes* — a limit is adjusted, a resize is
rejected as infeasible, a namespace is enrolled — not on every reconcile. A
reconcile that observes no change emits nothing; otherwise the event stream
becomes noise and rate-limits itself into uselessness.

Event `Reason` is a **stable PascalCase** token (e.g. `CPULimitAdjusted`,
`ResizeInfeasible`) and **mirrors the metric name** for the same event, so an
operator who sees `CPULimitAdjusted` in `kubectl describe` can find
`..._cpu_limit_adjusted_total` in the metrics without a translation table. Never
rewrite a `Reason` string casually — it is part of the contract, like an API
field.

Emit through the **events/v1 recorder**, not the deprecated core one. Use
`mgr.GetEventRecorder(name)` (returns `k8s.io/client-go/tools/events`'s
`EventRecorder`) and its `Eventf(regarding, related, eventtype, reason, action,
note, args...)`. The kubebuilder scaffold still wires the older
`mgr.GetEventRecorderFor` / `k8s.io/client-go/tools/record` API, but this repo's
`staticcheck` gate rejects it (`SA1019`), so a reconciler that copies the
scaffold verbatim fails CI — reach for `GetEventRecorder` from the start. The
extra `action` argument is the events/v1 machine-readable operation verb (e.g.
`Resizing`); keep `reason` the same PascalCase token described above. Tests use
`events.NewFakeRecorder`, whose `.Events` channel still yields
`"<type> <reason> <note>"` strings.

## Prometheus metrics mirror alertable conditions

Every condition worth alerting on has a Prometheus counterpart, so alerts can be
written against a scrape instead of `kubectl`:

- **Gauges mirror alertable conditions** — for each detail condition an operator
  would page on (degraded input, resize infeasible), expose a gauge that is `1`
  while the condition is active. Also expose the raw inputs the policy consumes
  (node slack, node factor) as gauges for debugging.
- **Counters for lifecycle events** — `..._total` counters whose names mirror
  event `Reason`s (resizes applied, resizes rejected).
- **Histograms for durations** — reconcile latency as a histogram.

Register through controller-runtime's metrics registry. The guiding rule:
**anyone reading a condition, an event, and a metric for the same fact should
see three consistent names for it**, not three vocabularies.
