#!/usr/bin/env bash
# Render the social preview card (og-card.html) to website/landing/og.png.
# Requires Chrome and macOS system fonts (Impact, Menlo, Helvetica Neue) —
# run on a Mac so the type matches the live site. Renders at 2x then
# downsamples to the canonical 1200x630 for crisp type at a scraper-friendly
# file size; og:image:width/height in the pages stay in sync at 1200x630.
set -euo pipefail

cd "$(dirname "$0")"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"

"$CHROME" --headless=new --disable-gpu --hide-scrollbars \
  --force-device-scale-factor=2 --window-size=1200,630 \
  --screenshot="$(pwd)/../landing/og.png" \
  "file://$(pwd)/og-card.html"

sips -z 630 1200 ../landing/og.png >/dev/null

echo "wrote $(cd ../landing && pwd)/og.png"
