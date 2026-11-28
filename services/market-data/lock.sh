#!/bin/sh
# Locks Python dependencies with hashes, ignoring releases newer than the cutoff (two weeks before locking) so a
# freshly published, possibly compromised release cannot slip in. Run through `make market-data-lock`.
set -eu
CUTOFF="${CUTOFF:-$(date -u -d '14 days ago' +%Y-%m-%dT00:00:00Z)}"
pip install -q --no-cache-dir uv==0.5.11 2>/dev/null
uv pip compile -q --python-version 3.12 --generate-hashes --exclude-newer "$CUTOFF" \
  --custom-compile-command "make market-data-lock" requirements.in -o requirements.txt
uv pip compile -q --python-version 3.12 --generate-hashes --exclude-newer "$CUTOFF" \
  --custom-compile-command "make market-data-lock" requirements-dev.in -o requirements-dev.txt
echo "locked with cutoff $CUTOFF"
