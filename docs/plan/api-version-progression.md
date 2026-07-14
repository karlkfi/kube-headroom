# Plan: HeadroomConfig API version progression (v1alpha1 → v1beta1 → v1)

Promote the `HeadroomConfig` CRD up the Kubernetes API maturity ladder as the
spec and behavior stabilize. This plan covers **both** increments — the shared
mechanics once, then the per-step blockers that gate each promotion. Tracked as
two deferred backlog items (v1beta1, v1) because neither is near-term; both wait
on concrete triggers below.

## Why a ladder, not a rename

Kubernetes API-version suffixes are a **contract about stability**, not cosmetic
labels:

- **v1alpha1 (today):** may change incompatibly at any release, no deprecation
  window owed, may drop data. This is where we *learn* the right API shape.
- **v1beta1:** enabled and trusted by default; upgrade-safe; conversion between
  served versions works; changes/removal owe a **deprecation window**. Signals
  "this API is going to be supported, details may still shift."
- **v1 (GA):** stable and backward-compatible indefinitely. A **one-way door** —
  once GA we can essentially never make an incompatible change.

Each step up is a promise we can only make once the evidence supports it, which
is why they're gated rather than scheduled.

## Shared mechanics (both increments)

One CRD object (`headroomconfigs.kube-headroom.dev`) serves multiple versions
via `spec.versions[]`; exactly one is `storage: true`. A promotion adds a
version and (eventually) moves storage — it never forks the CRD or the chart
(see the chart-naming rationale in `archive/kustomize-helm-migration.md`). The
kubebuilder multi-version workflow:

1. `kubebuilder create api --version <new> --kind HeadroomConfig` scaffolds
   `api/<new>/`. Designate one version the **conversion Hub** (the storage
   version); other versions are spokes implementing `ConvertTo`/`ConvertFrom`.
2. Mark exactly one `+kubebuilder:storageversion`; keep the prior version
   `served: true` through its deprecation window (`deprecated: true` +
   `deprecationWarning`).
3. **Conversion webhook** (`kubebuilder create webhook --conversion`) whenever
   the shapes differ non-trivially. `strategy: None` only survives purely
   additive-optional changes — any rename, move, or semantic shift needs the
   webhook, served by the manager and CA-injected exactly like the admission
   webhook.
4. Keep the k8s-free `internal/policy` core version-agnostic — it operates on
   plain values, so conversion stays at the API boundary and the policy
   invariants (§5) don't move. This is the property that keeps blast radius low.
5. **Storage-version migration** before dropping any old version: re-write every
   stored object to the new storage version, then prune the old entry from the
   CRD's `status.storedVersions`. The apiserver refuses the drop otherwise.
6. Update samples, `docs/*`, and envtest to exercise conversion round-trips.
   RBAC is per-resource, not per-version — unaffected.

## Increment 1 — v1alpha1 → v1beta1

### What ships
`v1beta1` added as served + storage, `v1alpha1` kept served-but-deprecated for a
window, a conversion webhook (assume the shapes diverge), and conversion tests.

### Blockers (all must clear before promoting)
- **B1 — Spec stability.** The field set must stop churning. Open items still
  shaping it (e.g. Q18 multiplier ceilings) and the core controller behavior
  (§6–8) must be complete enough that no incompatible field change is
  foreseen. A beta that immediately needs a breaking change wastes the
  conversion machinery and the deprecation window.
- **B2 — Conversion + multi-version delivery (depends on Q21).** Multi-version
  `spec.versions` and the conversion `clientConfig` CA injection are packaging
  concerns. Cleanest to template **once in the Helm CRD chart** after Q21
  ships, rather than wiring it under kustomize and then re-wiring under Helm.
- **B3 — Defaulting & validation finalized.** Beta implies stable defaults and
  CEL validation, including the singleton rule (§9.3). Locked, not in flux.
- **B4 — Real-cluster validation.** Evidence from real usage (the §6.2/§8
  acceptance scenarios on live clusters) that the API shape holds. Alpha is
  where we discover this; beta is the commitment that follows the evidence.

## Increment 2 — v1beta1 → v1 (GA)

### What ships
`v1` added as served + storage, `v1beta1` deprecated for a window, `v1alpha1`
removed (after its storage migration), and the docs reworked to present v1 as
the stable API.

### Blockers (all of Increment 1, plus)
- **C1 — Beta soak.** v1beta1 served ≥2 releases with **no** incompatible change
  required — the empirical proof the API is stable. GA is a one-way door; this
  is the evidence that lets us walk through it.
- **C2 — Storage-migration proven.** The beta→GA storage flip demonstrates the
  migrate-then-drop path end to end (kube-storage-version-migrator or a
  documented re-apply sweep + `storedVersions` prune), so old versions can be
  retired safely.
- **C3 — Backward-compat commitment (Karl decision).** No known spec gaps in the
  design; feature set complete; explicit willingness to support v1 indefinitely.
- **C4 — Docs complete.** `runbook.md`, `tenant-guide.md`, and `applicability.md`
  all present v1 as the supported surface.

## Dependency graph

```
Q21 (Helm) ─┐
            ├─▶ v1beta1 ─▶ v1
spec-stable ┘             ▲
                          └─ beta soak (≥2 releases, no breaks)
```

- v1beta1 is gated by Q21 (conversion/multi-version delivery) and the
  spec-stability gate (B1/B3/B4).
- v1 is gated by v1beta1 shipping and then soaking (C1), plus a GA sign-off
  (C3). It cannot skip beta — the soak *is* the evidence.

## Acceptance criteria (per increment)

- Both versions served; `kubectl get headroomconfig.<old>` and `.<new>` return
  the same object, round-tripped through the conversion webhook with no data
  loss.
- Storage flips to the new version; a migration re-writes stored objects; the
  retired version drops cleanly from `status.storedVersions`.
- envtest covers conversion in both directions; the singleton CEL rule holds on
  every served version.
- Existing `HeadroomConfig`s survive the `helm upgrade` of the CRD chart
  untouched (`resource-policy: keep`).
- Deprecated versions emit the deprecation warning; docs name the current
  supported version.

## Out of scope / open

- Exact release count for the beta soak (C1) and whether v1alpha1 is dropped at
  the v1beta1 or the v1 step — decide when B1 evidence is in hand.
- Whether v1beta1 → v1 needs a conversion webhook at all (it won't, if the
  shapes are identical by then — the whole point of a well-soaked beta).
