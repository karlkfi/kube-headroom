# Plan: Port github-actions-gateway development docs (Q11)

Adopt the lightweight development-doc practices from the sibling operator
github-actions-gateway (GAG), scaled to a solo project. One tracked item; each
bullet below is a short doc under `docs/development/` (create `README.md` to
index them).

## Scope

- **`testing.md`** — the three test tiers (unit / envtest-integration / kind
  e2e), the "pick the narrowest test that observes the bug" principle, and how
  fast each tier should stay.
- **`kubernetes-conventions.md`** — enum-keyword label values (see Q10),
  label/annotation keys as consts, recommended `app.kubernetes.io/*` labels via
  a shared helper (never as selectors), the two-tier condition ladder,
  events on lifecycle transitions only (stable PascalCase `Reason` mirroring
  metric names), and Prometheus gauges mirroring alertable conditions. **Informs
  Q7.**
- **`kind-iteration.md`** — the fast inner loop (reuse the cluster, unique image
  tags to defeat cache, `kubectl set image`, targeted debug pods). Relevant once
  the controller exists (Q4/Q8).
- **`documentation-standards.md`** — anti-slop rules (specifics over adjectives,
  honest about "not yet implemented"), canonical-home-plus-link (no
  transclusion), one term per concept, spell out acronyms; plus the
  doc-update-matrix idea (change-type → which docs must update) once
  `docs/operations/` exists. **Coordinate with Q9.**
- **`technical-debt.md`** — the fix / flag / defer / decline policy; flake fixes
  go to the top of the queue; secure-by-default is non-negotiable.

## Acceptance

- Each doc is short and specific — link the canonical source, don't transclude.
- `docs/development/README.md` indexes them; Q7 and Q9 reference the relevant
  convention docs.

## Out of scope (GAG practices deliberately skipped)

Go workspaces, coverage ratchet, parallel multi-session dispatch, updatecli tool
pinning, license-notices (no license by choice), desktop build throttling.
