#!/usr/bin/env bash
#
# lint-plan-hygiene.sh — plan-doc hygiene checks (Q12).
#
# Plan docs under docs/plan/ are working notes for M/L backlog items. They rot
# in two ways this script prevents:
#
#   1. Orphaned plan docs. Every docs/plan/*.md must be either referenced from
#      docs/STATUS.md (an active or deferred backlog row links it) or archived
#      under docs/plan/archive/. A doc that is neither has outlived its task —
#      the work shipped or was dropped and nobody archived the notes.
#
#   2. Plan-doc paths in Go code. `docs/plan/...` must not appear in any *.go
#      source. Plan docs are ephemeral working notes, not part of the code's
#      contract; a comment pointing at one dangles the moment the doc is
#      archived or deleted. Reference docs/design.md (or docs/) instead.
#
# Archive convention: once a plan doc's task has landed (or been abandoned) and
# STATUS.md no longer links it, move the doc to docs/plan/archive/ to keep it
# for history. Archived docs are exempt from check 1.
#
# Offline and dependency-free; inspects git-tracked files only. Exits non-zero
# on any violation, emitting GitHub Actions ::error annotations under CI.
#
# Usage: lint-plan-hygiene.sh

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

STATUS="docs/STATUS.md"
bad=0

emit() { # emit <message>
    if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
        printf '::error::%s\n' "$1"
    else
        printf 'lint-plan-hygiene: %s\n' "$1" >&2
    fi
    bad=1
}

if [[ ! -f "$STATUS" ]]; then
    printf 'lint-plan-hygiene: %s not found\n' "$STATUS" >&2
    exit 2
fi

# --- Invariant 1: every top-level plan doc is STATUS-referenced or archived ---
# The docs/plan/*.md pathspec matches recursively, so exclude the archive dir.
# STATUS.md links plan docs by their docs/-relative path (e.g. plan/foo.md).
while IFS= read -r doc; do
    [[ -n "$doc" ]] || continue
    rel="${doc#docs/}"  # plan/foo.md — the form STATUS.md links by
    if ! grep -qF "$rel" "$STATUS"; then
        emit "$doc is neither referenced from $STATUS nor archived (link it from a backlog row, or move it to docs/plan/archive/)"
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
printf 'lint-plan-hygiene: ok (%s)\n' "$STATUS"
