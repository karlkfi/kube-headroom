#!/usr/bin/env python3
"""Copy the hand-written landing pages into an assembled site dir, stamping the release.

Offline and dependency-free. `website/release.json` — read from the landing
dir's parent — is the single source for the release the site advertises. The
landing HTML writes it as `{{VERSION}}` and `{{ANNOUNCEMENT}}`; substitution
happens here, on the way into `dist/`.

The version reaches index.html seven times across five lines — JSON-LD
`softwareVersion`, the announce banner, the ON AIR panel — so a hand-edit that
catches six of them ships a site advertising two versions at once. A bare semver
left in the sources is therefore an error, not a smell.

Usage: stamp-landing.py <landing-dir> <dist-dir>
"""
from __future__ import annotations

import json
import os
import re
import shutil
import sys

TOKEN_RE = re.compile(r"\{\{([A-Z][A-Z0-9_]*)\}\}")
SEMVER_RE = re.compile(r"\bv?\d+\.\d+\.\d+")
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")


def load_release(path: str) -> tuple[dict[str, str], list[str]]:
    """Token values from release.json, plus whatever is wrong with it."""
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)

    version = data.get("version", "")
    announcement = data.get("announcement", "")
    errors = []

    valid_version = bool(VERSION_RE.match(version))
    if not valid_version:
        errors.append(f"version {version!r} is not a bare semver (no leading 'v')")
    if not announcement or announcement.startswith(("/", "http://", "https://")):
        errors.append(f"announcement {announcement!r} must be a site-relative path")
    elif valid_version and f"v{version}" not in announcement:
        # The news post is named after the release it announces, so a bumped
        # version with the old announcement still attached is a typo, not a
        # deliberate pairing.
        errors.append(f"announcement {announcement!r} does not name v{version}")

    return {"VERSION": version, "ANNOUNCEMENT": announcement}, errors


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: stamp-landing.py <landing-dir> <dist-dir>", file=sys.stderr)
        return 2
    landing, dist = sys.argv[1], sys.argv[2]
    if not os.path.isdir(landing):
        print(f"stamp-landing: not a directory: {landing}", file=sys.stderr)
        return 2

    release_json = os.path.join(os.path.dirname(os.path.abspath(landing)), "release.json")
    try:
        values, errors = load_release(release_json)
    except (OSError, ValueError) as err:
        print(f"stamp-landing: cannot read {release_json}: {err}", file=sys.stderr)
        return 2
    if errors:
        print(f"Problems in {release_json}:")
        for e in errors:
            print(f"  {e}")
        return 1

    shutil.copytree(landing, dist, dirs_exist_ok=True)

    # Only the pages that came from `landing` — `dist` also holds the VitePress
    # output by the time the site is assembled, and that half is not templated.
    sources = []
    for dirpath, _, filenames in os.walk(landing):
        for name in filenames:
            if name.endswith(".html"):
                sources.append(os.path.relpath(os.path.join(dirpath, name), landing))
    sources.sort()

    stamped = 0
    for rel in sources:
        path = os.path.join(dist, rel)
        with open(path, encoding="utf-8") as fh:
            html = fh.read()

        for m in SEMVER_RE.finditer(html):
            errors.append(f"{rel}: hardcoded version {m.group(0)!r} — use {{{{VERSION}}}}")
        for m in TOKEN_RE.finditer(html):
            if m.group(1) not in values:
                errors.append(f"{rel}: unknown token {{{{{m.group(1)}}}}}")

        html, count = TOKEN_RE.subn(lambda m: values.get(m.group(1), m.group(0)), html)
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(html)
        stamped += count

    if errors:
        print("Landing page problems:")
        for e in errors:
            print(f"  {e}")
        return 1
    print(
        f"stamp-landing: ok ({len(sources)} pages, {stamped} tokens stamped, "
        f"v{values['VERSION']})"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
