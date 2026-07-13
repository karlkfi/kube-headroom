# Documentation standards

How we write docs in this repo. Short rules, aimed at keeping the doc set
honest and non-redundant.

## Specifics over adjectives (anti-slop)

Write the number, the command, the file path — not the adjective. "Runs in
sub-second time" beats "blazingly fast"; "`failurePolicy: Ignore`, namespace
-scoped" beats "robust and flexible." If a sentence survives deleting its
adjectives, delete them. Marketing verbs (*seamless*, *powerful*, *robust*,
*leverage*) are a smell that a claim isn't backed by a specific.

## Be honest about "not yet implemented"

This project is in early development and the docs say so plainly. If a feature
isn't built, mark it — "not yet implemented," "planned for Q7," or a link to the
backlog row — rather than describing the intended behavior in the present tense.
A reader must never have to guess whether a doc describes today's code or a
future goal. The README's status banner is the model.

## Canonical home, plus a link — never transclude

Every fact has exactly **one** canonical home:

- Design rationale → the design doc (`design.md`).
- What/when/priority → [../STATUS.md](../STATUS.md).
- API contract → the Go source and its doc comments (e.g.
  [`labels.go`](../../api/v1alpha1/labels.go)).
- How we work → this `docs/development/` tree.

Everywhere else, **link** to the canonical home; do not copy its content. Copied
prose drifts out of sync the moment the original changes, and then the reader
can't tell which copy is right. A one-line summary plus a link is fine — a
paragraph duplicated from another page is not.

## One term per concept

Pick one name for each concept and use it everywhere: *managed namespace* (not
"enrolled" / "opted-in" / "watched" interchangeably), *the policy core* (not
"the engine" / "the calculator"), *the node reconciler* (not "the node
controller" / "the watcher"). Before introducing a noun for a concept, `rg` for
an existing name and reuse it. Renaming a term means renaming it across the
whole doc set in one change, not just on the page you're editing.

## Spell out acronyms on first use

Expand an acronym the first time it appears on a page — "Completely Fair
Scheduler (CFS) quota," "Vertical Pod Autoscaler (VPA)" — then use the short
form. Exceptions are terms a Kubernetes reader never spells out (CPU, YAML,
API, CRD). When in doubt, expand it.

## The doc-update matrix (planned)

Once `docs/operations/` exists (runbook, tenant guide — Q9), add a small
change-type → docs matrix here: e.g. "changed a policy invariant → update the
design doc + policy tests"; "added a metric → update the operations runbook +
[kubernetes-conventions.md](kubernetes-conventions.md)." The matrix turns "which
docs must I touch?" from memory into a lookup. Deferred until there are enough
operational docs to make it worth the table; coordinate with Q9.
