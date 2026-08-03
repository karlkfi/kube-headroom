#!/usr/bin/env python3
"""Check the assembled site's per-page SEO metadata.

Offline and dependency-free. Walks a built site directory (the `dist/` the
website workflow assembles from the landing pages plus the VitePress output)
and asserts that every indexable page carries an absolute canonical URL and a
meta description of its own, that no two pages share a description, and that
error pages claim no canonical at all.

Guards a regression class the site shipped with: no robots.txt, no sitemap, and
one site-wide description repeated across all 16 docs pages. Docs descriptions
live in `website/.vitepress/config.mjs` keyed by path, so a newly added page
that nobody adds an entry for silently falls back to that shared description —
which reads as fine in review and is exactly what this catches.

Usage: check-site-metadata.py <dist-dir>
"""
from __future__ import annotations

import collections
import os
import re
import sys

META_RE = re.compile(r"<meta\b([^>]*)>", re.I)
LINK_RE = re.compile(r"<link\b([^>]*)>", re.I)
ATTR_RE = re.compile(r'([\w:-]+)\s*=\s*"([^"]*)"')

# GitHub Pages serves 404.html at arbitrary missing paths. Those pages carry
# noindex, and a canonical on one would advertise it as a duplicate of whatever
# page it names.
ERROR_PAGES = {"404.html"}

REQUIRED_AT_ROOT = ("robots.txt", "sitemap.xml")


def attrs_of(fragment: str) -> dict[str, str]:
    return {k.lower(): v for k, v in ATTR_RE.findall(fragment)}


def meta_description(html: str) -> str | None:
    """The page's <meta name="description"> content, tolerant of attribute order."""
    for m in META_RE.finditer(html):
        a = attrs_of(m.group(1))
        if a.get("name", "").lower() == "description":
            return a.get("content", "")
    return None


def canonical(html: str) -> str | None:
    for m in LINK_RE.finditer(html):
        a = attrs_of(m.group(1))
        if a.get("rel", "").lower() == "canonical":
            return a.get("href", "")
    return None


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-site-metadata.py <dist-dir>", file=sys.stderr)
        return 2
    root = sys.argv[1]
    if not os.path.isdir(root):
        print(f"check-site-metadata: not a directory: {root}", file=sys.stderr)
        return 2

    pages = []
    for dirpath, _, filenames in os.walk(root):
        for name in filenames:
            if name.endswith(".html"):
                pages.append(os.path.relpath(os.path.join(dirpath, name), root))
    pages.sort()

    errors: list[str] = []
    by_description: dict[str, list[str]] = collections.defaultdict(list)
    checked = 0

    for rel in pages:
        with open(os.path.join(root, rel), encoding="utf-8") as fh:
            html = fh.read()
        href = canonical(html)

        if os.path.basename(rel) in ERROR_PAGES:
            if href:
                errors.append(f"{rel}: error page claims canonical '{href}'")
            continue

        checked += 1
        description = meta_description(html)
        if not description or not description.strip():
            errors.append(f"{rel}: no meta description")
        else:
            by_description[description.strip()].append(rel)

        if not href:
            errors.append(f"{rel}: no canonical URL")
        elif not href.startswith("https://"):
            errors.append(f"{rel}: canonical is not absolute: '{href}'")

    for description, where in sorted(by_description.items()):
        if len(where) > 1:
            errors.append(
                f"{len(where)} pages share one description ({', '.join(where)}): "
                f"{description[:60]}..."
            )

    for name in REQUIRED_AT_ROOT:
        if not os.path.isfile(os.path.join(root, name)):
            errors.append(f"missing {name} at the site root")

    if errors:
        print("Site metadata problems:")
        for e in errors:
            print(f"  {e}")
        return 1
    print(f"check-site-metadata: ok ({checked} indexable pages)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
