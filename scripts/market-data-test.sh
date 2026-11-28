#!/usr/bin/env bash
# market-data tests (pytest) against recorded provider responses. Part of make test (specs/testing.md).
# --network none: the tests cannot reach Yahoo or the Bank of Canada, so a test that tried would fail.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE=pt-market-data-test
docker build -q --target test -t "$IMAGE" "$ROOT/services/market-data" >/dev/null
docker run --rm --network none -v "$ROOT/testdata:/testdata:ro" -e TESTDATA=/testdata "$IMAGE" python -m pytest "$@"
