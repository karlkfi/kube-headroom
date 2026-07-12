<!-- Keep this short. Delete sections that don't apply. -->

## What & why

<!-- What does this change and why. Link the backlog item, e.g. "Closes Q4". -->

## How tested

<!-- Commands run and what you observed. e.g. `make check`; `make test-e2e`. -->

## Checklist

- [ ] `make check` passes locally
- [ ] Generated files regenerated and committed (if API types / `+kubebuilder:` markers changed)
- [ ] Docs updated (if behavior, config, or CLI surface changed)
- [ ] No debug prints or stray `TODO`s left in
- [ ] Backlog updated in an isolated `docs(status):` commit (if this completes or adds a Q-item)
- [ ] Any path-gated heavy CI (e2e) **actually ran** — a check that was skipped is not the same as passing
