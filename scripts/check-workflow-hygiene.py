#!/usr/bin/env python3
"""Check GitHub Actions workflows for supply-chain hygiene.

Offline and dependency-free. Scans tracked workflow and composite-action files
and asserts two invariants Q45 had to restore by hand:

  1. Every `uses:` is pinned to a full 40-character commit SHA, carrying a
     trailing `# vX.Y.Z` comment. A tag or branch ref is mutable — whoever can
     move `v4` can run their code with this repo's token — and a bare SHA with
     no version comment is unreviewable, so Dependabot writes the comment too.

  2. Every `actions/checkout` step sets `persist-credentials: false`. Checkout
     otherwise leaves the job's token in `.git/config`, where any later step
     (an action, a build script, a test) can read it and push with it.

Both drifted unnoticed in website.yml — it was added after the rest of the
workflows were pinned, and nothing was watching.

Usage: check-workflow-hygiene.py
"""
from __future__ import annotations

import re
import subprocess
import sys

USES_RE = re.compile(r"^(\s*)(-\s+)?uses:\s*(\S+)\s*(.*)$")
ITEM_RE = re.compile(r"^(\s*)-\s")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")


def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip())


def step_block(lines: list[str], idx: int, key_indent: int) -> list[str]:
    """The YAML sequence item owning the `uses:` at `idx`.

    A step's keys sit two columns right of the `-` that opens the item, so the
    item starts at the nearest preceding dash at `key_indent - 2` and ends at
    the next non-blank line indented no further than that dash.
    """
    item_indent = key_indent - 2
    start = idx
    while start >= 0:
        m = ITEM_RE.match(lines[start])
        if m and len(m.group(1)) == item_indent:
            break
        start -= 1
    else:
        return [lines[idx]]

    end = start + 1
    while end < len(lines):
        if lines[end].strip() and indent_of(lines[end]) <= item_indent:
            break
        end += 1
    return lines[start:end]


def check(path: str, errors: list[str]) -> int:
    """Append this file's violations to `errors`; return its `uses:` count."""
    with open(path, encoding="utf-8") as fh:
        lines = fh.read().splitlines()

    pins = 0
    for i, line in enumerate(lines):
        m = USES_RE.match(line)
        if not m:
            continue
        indent, dash, ref, trailer = m.groups()
        key_indent = len(indent) + (len(dash) if dash else 0)
        where = f"{path}:{i + 1}"

        if ref.startswith("./"):
            continue  # a local action is this repo's own code, already pinned
        pins += 1

        if ref.startswith("docker://"):
            if not DIGEST_RE.search(ref):
                errors.append(f"{where}: image is not pinned by digest: '{ref}'")
            continue

        action, _, version = ref.partition("@")
        if not SHA_RE.match(version):
            errors.append(
                f"{where}: '{action}' is pinned to '{version or '(nothing)'}', "
                "not a 40-character commit SHA"
            )
        elif not trailer.startswith("#") or not trailer.lstrip("# ").strip():
            errors.append(
                f"{where}: '{action}' pin has no version comment "
                "(append `# v1.2.3` so the SHA is reviewable)"
            )

        if action == "actions/checkout":
            block = step_block(lines, i, key_indent)
            if not any("persist-credentials: false" in b for b in block):
                errors.append(
                    f"{where}: checkout does not set `persist-credentials: false` "
                    "(the job token stays in .git/config for every later step)"
                )
    return pins


def main() -> int:
    files = subprocess.check_output(
        [
            "git", "ls-files",
            ".github/workflows/*.yml", ".github/workflows/*.yaml",
            ".github/actions/**/action.yml", ".github/actions/**/action.yaml",
        ],
        text=True,
    ).split()
    if not files:
        print("check-workflow-hygiene: no workflows found", file=sys.stderr)
        return 2

    errors: list[str] = []
    pins = sum(check(f, errors) for f in sorted(files))

    if errors:
        print("Workflow hygiene problems:")
        for e in errors:
            print(f"  {e}")
        return 1
    print(f"check-workflow-hygiene: ok ({len(files)} files, {pins} pinned actions)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
