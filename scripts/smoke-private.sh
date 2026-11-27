#!/usr/bin/env bash
# make smoke-private: the importer's parsers on the real exports in data/ (specs/testing.md, D-20). Local only.
# Prints counts only (files and accounts are numbered, never named), so the output is safe to paste into a chat.
# The container gets data/ read-only, no network, a read-only file system and no database: nothing from data/ is
# written anywhere.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${SMOKE_DATA:-$ROOT/data}"
IMAGE=pt-importer-smoke

if [ ! -d "$DATA" ]; then
  echo "no data folder at $DATA (put the broker exports in data/questrade and data/wealthsimple)" >&2
  exit 1
fi

docker build -q --target runtime -t "$IMAGE" "$ROOT/services/importer" >/dev/null
docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$DATA:/private:ro" "$IMAGE" python -m importer.smoke /private
