#!/usr/bin/env bash
# make e2e: Playwright acceptance tests against a throwaway Docker Compose stack (specs/testing.md).
# Uses its own project name, port and volumes, so it never touches the stack started with make up.
# Users and passwords are synthetic and generated per run (D-20).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT=pt-e2e
PORT="${E2E_PORT:-8090}"
TMP="$(mktemp -d)"
ENV_FILE="$TMP/e2e.env"

dc() { docker compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$ROOT/docker-compose.yml" "$@"; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "e2e failed; last API log lines:" >&2
    dc logs --tail 30 api >&2 || true
  fi
  dc down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$TMP"
  exit "$status"
}
trap cleanup EXIT

{
  echo "POSTGRES_PASSWORD=$(openssl rand -hex 16)"
  echo "SESSION_SECRET=$(openssl rand -base64 48)"
  echo "IMPORTER_DB_PASSWORD=$(openssl rand -hex 24)"
  echo "SERVICE_TOKEN=$(openssl rand -hex 32)"
  echo "WEB_PORT=$PORT"
} > "$ENV_FILE"

echo "Starting stack $PROJECT on http://localhost:$PORT"
# --wait: every service with a health check (db, importer) must become healthy.
dc up -d --build --quiet-pull --wait

for _ in $(seq 60); do
  if curl -fsS "http://localhost:$PORT/api/health" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://localhost:$PORT/api/health" >/dev/null

# Users per Playwright project (browser), so the browsers' runs do not see each other's data: A and B for accounts
# and isolation (AC-1), C for imports (AC-2..4), D for holdings (AC-8).
PASSWORD_A="e2e $(openssl rand -hex 12)"
PASSWORD_B="e2e $(openssl rand -hex 12)"
PASSWORD_C="e2e $(openssl rand -hex 12)"
PASSWORD_D="e2e $(openssl rand -hex 12)"
for browser in chromium firefox webkit; do
  echo "$PASSWORD_A" | dc exec -T api ptctl user create --email "e2e-a-$browser@example.test" --name "E2E User A" >/dev/null
  echo "$PASSWORD_B" | dc exec -T api ptctl user create --email "e2e-b-$browser@example.test" --name "E2E User B" >/dev/null
  echo "$PASSWORD_C" | dc exec -T api ptctl user create --email "e2e-c-$browser@example.test" --name "E2E User C" >/dev/null
  echo "$PASSWORD_D" | dc exec -T api ptctl user create --email "e2e-d-$browser@example.test" --name "E2E User D" >/dev/null
done

(
  cd "$ROOT/services/web"
  E2E_BASE_URL="http://localhost:$PORT" E2E_PASSWORD_A="$PASSWORD_A" E2E_PASSWORD_B="$PASSWORD_B" \
    E2E_PASSWORD_C="$PASSWORD_C" E2E_PASSWORD_D="$PASSWORD_D" npx playwright test "$@"
)

# AC-2: the uploaded originals are stored unchanged. Every stored file must be byte-identical to one of the
# synthetic inputs (its name is its SHA-256) and read-only (D-12).
EXPECTED="$(cd "$ROOT/testdata" && find questrade/synthetic_activities/input questrade/synthetic_holdings wealthsimple/synthetic_activities/input \
  \( -name '*.csv' -o -name '*.xlsx' \) -exec shasum -a 256 {} + | awk '{print $1}' | sort -u | tr '\n' ' ')"
dc exec -T importer python - "$EXPECTED" <<'PY'
import hashlib, pathlib, stat, sys
expected = set(sys.argv[1].split())
files = [p for p in pathlib.Path("/data/files").rglob("*") if p.is_file()]
bad = []
for p in files:
    digest = hashlib.sha256(p.read_bytes()).hexdigest()
    mode = stat.S_IMODE(p.stat().st_mode)
    if digest != p.stem or digest not in expected or mode & 0o222:
        bad.append(f"{p.name}: sha256 {digest[:12]}, mode {oct(mode)}")
print(f"stored originals: {len(files)} files, {len(bad)} changed or writable")
if bad:
    print("\n".join(bad), file=sys.stderr)
    sys.exit(1)
PY
