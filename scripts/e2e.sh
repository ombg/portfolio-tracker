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

# One pair of users per Playwright project (browser), so the browsers' runs do not see each other's accounts.
PASSWORD_A="e2e $(openssl rand -hex 12)"
PASSWORD_B="e2e $(openssl rand -hex 12)"
for browser in chromium firefox webkit; do
  echo "$PASSWORD_A" | dc exec -T api ptctl user create --email "e2e-a-$browser@example.test" --name "E2E User A" >/dev/null
  echo "$PASSWORD_B" | dc exec -T api ptctl user create --email "e2e-b-$browser@example.test" --name "E2E User B" >/dev/null
done

cd "$ROOT/services/web"
E2E_BASE_URL="http://localhost:$PORT" E2E_PASSWORD_A="$PASSWORD_A" E2E_PASSWORD_B="$PASSWORD_B" \
  npx playwright test "$@"
