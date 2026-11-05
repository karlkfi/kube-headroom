#!/usr/bin/env python3
"""Generate the site's backlog page from the item store.

Writes docs/queue/index.md, which is gitignored: the page is built, never
committed, so no session completing an item has a reason to edit it. VitePress
serves it at /docs/queue/, beside the item pages its links point at.

The docs column is too narrow for every field `queue.py render` prints, so the
page drops Labels and links each title to its item rather than to the item's
`target`, which usually names a file the site does not publish.
"""
from __future__ import annotations

import posixpath
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
BLOB = "https://github.com/karlkfi/kube-headroom/blob/main/"
CELL_RE = re.compile(r"(?<!\\)\|")
LINK_TEXT_RE = re.compile(r"\[(.*?)\]\(.*?\)")
OUTSIDE_STORE_RE = re.compile(r"\]\((\.\./[^)]+)\)")

INTRO = """# Backlog

Open work on Headroom, highest priority first. 🔲 is ready to pick up, 🚫 is
blocked, and 💤 is deferred until the trigger in its note fires. Each title opens
the full item. Items live as files in
[`docs/queue/`](https://github.com/karlkfi/kube-headroom/tree/main/docs/queue)
and are deleted when the work ships.

| ID | Item | St | Sz | Notes |
|---|---|---|---|---|
"""


def row(line: str) -> str:
    _, item_id, title, _labels, status, size, notes, _ = CELL_RE.split(line)
    item_id = LINK_TEXT_RE.sub(r"\1", item_id).strip()
    title = LINK_TEXT_RE.sub(r"\1", title).strip()
    notes = OUTSIDE_STORE_RE.sub(
        lambda m: "](" + BLOB + posixpath.normpath("docs/queue/" + m.group(1)) + ")", notes
    )
    return f"| {item_id} | [{title}]({item_id}.md) |{status}|{size}|{notes}|\n"


def main() -> int:
    table = subprocess.run(
        [sys.executable, str(REPO / "scripts/queue.py"), "--store", str(REPO / "docs/queue"),
         "render", "--format", "table", "--all"],
        check=True, stdout=subprocess.PIPE, text=True,
    ).stdout
    rows = table.splitlines()[2:]  # past the header and its rule
    (REPO / "docs/queue/index.md").write_text(INTRO + "".join(map(row, rows)), encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
