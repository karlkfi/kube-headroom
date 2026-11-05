#!/usr/bin/env bash
#
# lint-plan-hygiene.sh — plan-doc hygiene checks (Q12).
#
# Plan docs under docs/plan/ are working notes for M/L backlog items. They rot
# in two ways this script prevents:
#
#   1. Orphaned plan docs. Every docs/plan/*.md must be either referenced from
#      docs/queue/ (an active or deferred backlog item links it) or archived
#      under docs/plan/archive/. A doc that is neither has outlived its task —
#      the work shipped or was dropped and nobody archived the notes.
#
#   2. Plan-doc paths in Go code. `docs/plan/...` must not appear in any *.go
#      source. Plan docs are ephemeral working notes, not part of the code's
#      contract; a comment pointing at one dangles the moment the doc is
#      archived or deleted. Reference docs/design.md (or docs/) instead.
#
# Archive convention: once a plan doc's task has landed (or been abandoned) and
# no backlog item links it, move the doc to docs/plan/archive/ to keep it
# for history. Archived docs are exempt from check 1.
#
# Offline and dependency-free; inspects git-tracked files only. Exits non-zero
# on any violation, emitting GitHub Actions ::error annotations under CI.
#
# Usage: lint-plan-hygiene.sh

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

QUEUE="docs/queue"
bad=0

emit() { # emit <message>
    if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
        printf '::error::%s\n' "$1"
    else
        printf 'lint-plan-hygiene: %s\n' "$1" >&2
    fi
    bad=1
}

if [[ ! -d "$QUEUE" ]]; then
    printf 'lint-plan-hygiene: %s not found\n' "$QUEUE" >&2
    exit 2
fi

# --- Invariant 1: every top-level plan doc is referenced by an item or archived ---
# The docs/plan/*.md pathspec matches recursively, so exclude the archive dir.
# Items link plan docs as ../plan/foo.md, which contains the docs/-relative path.
while IFS= read -r doc; do
    [[ -n "$doc" ]] || continue
    rel="${doc#docs/}"  # plan/foo.md
    if ! grep -rqF "$rel" "$QUEUE"; then
        emit "$doc is neither referenced from $QUEUE nor archived (link it from a backlog item, or move it to docs/plan/archive/)"
    fi
done < <(git ls-files 'docs/plan/*.md' ':!:docs/plan/archive/**')

# --- Invariant 2: no Go source references a plan-doc path ---
while IFS= read -r hit; do
    [[ -n "$hit" ]] || continue
    emit "Go source references a plan-doc path (plan docs are working notes, not a code contract — link docs/design.md instead): $hit"
done < <(git grep -nF 'docs/plan' -- '*.go' || true)

if (( bad )); then
    exit 1
fi
printf 'lint-plan-hygiene: ok (%s)\n' "$QUEUE"
