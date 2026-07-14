#!/usr/bin/env python3
"""Check that relative Markdown links and heading anchors resolve.

Offline and dependency-free. Scans tracked *.md files (via `git ls-files`),
validates that relative link targets exist on disk, and that `#fragment`
anchors match either a heading slug (GitHub-style) or an explicit HTML
`id=`/`name=` anchor in the target file. External links (http/https/mailto)
and site-absolute paths (/foo) are skipped. Exits non-zero on any broken link.
"""
from __future__ import annotations

import os
import re
import subprocess
import sys

LINK_RE = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")
ID_RE = re.compile(r'(?:id|name)=["\']([^"\']+)["\']')


def slug(text: str) -> str:
    """Approximate GitHub's heading-to-anchor slug."""
    text = text.strip().lower()
    text = re.sub(r"<[^>]+>", "", text)          # strip inline HTML
    text = re.sub(r"[`*_~]", "", text)           # strip md emphasis
    text = re.sub(r"[^\w\s-]", "", text)         # drop punctuation
    return text.strip().replace(" ", "-")


def anchors_of(path: str) -> set[str]:
    """Return the set of valid anchors in a file: heading slugs + explicit ids."""
    found: set[str] = set()
    counts: dict[str, int] = {}
    in_fence = False
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if FENCE_RE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            found.update(ID_RE.findall(line))  # explicit id="..." / name="..."
            m = HEADING_RE.match(line)
            if m:
                base = slug(m.group(2))
                n = counts.get(base, 0)
                found.add(base if n == 0 else f"{base}-{n}")
                counts[base] = n + 1
    return found


def links_of(path: str):
    in_fence = False
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            if FENCE_RE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for m in LINK_RE.finditer(line):
                yield lineno, m.group(1).strip()


def main() -> int:
    # Exclude vendor/: it holds checked-in third-party Markdown (READMEs,
    # CHANGELOGs) whose relative links we neither own nor can fix.
    files = subprocess.check_output(
        ["git", "ls-files", "*.md", ":(exclude)vendor/**"], text=True
    ).split()
    anchor_cache: dict[str, set[str]] = {}
    errors: list[str] = []

    for f in files:
        for lineno, target in links_of(f):
            # strip an optional link title: [x](url "title")
            t = target.split()[0] if " " in target else target
            if t.startswith(("http://", "https://", "mailto:", "tel:", "#!")):
                continue
            path_part, _, anchor = t.partition("#")

            if path_part == "":
                target_file = f
            elif path_part.startswith("/"):
                continue  # site-absolute, out of scope
            else:
                target_file = os.path.normpath(os.path.join(os.path.dirname(f), path_part))
                if not os.path.exists(target_file):
                    errors.append(f"{f}:{lineno}: missing target '{target}' -> {target_file}")
                    continue

            if anchor and target_file.endswith(".md") and os.path.exists(target_file):
                if target_file not in anchor_cache:
                    anchor_cache[target_file] = anchors_of(target_file)
                valid = anchor_cache[target_file]
                if anchor not in valid and anchor.lower() not in valid:
                    errors.append(f"{f}:{lineno}: missing anchor '#{anchor}' in {target_file}")

    if errors:
        print("Broken documentation links:")
        for e in errors:
            print(f"  {e}")
        return 1
    print(f"check-doc-links: ok ({len(files)} markdown files)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
