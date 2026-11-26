#!/usr/bin/env bash
# Importer tests (pytest) against a throwaway, migrated Postgres. Part of make test (specs/testing.md).
# Own Compose project and volumes; random secrets; fixtures are synthetic (D-20).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT=pt-itest
TMP="$(mktemp -d)"
ENV_FILE="$TMP/itest.env"
IMAGE=pt-importer-test

dc() { docker compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$ROOT/docker-compose.yml" "$@"; }

cleanup() {
  status=$?
  dc down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$TMP"
  exit "$status"
}
trap cleanup EXIT

POSTGRES_PASSWORD="$(openssl rand -hex 16)"
IMPORTER_DB_PASSWORD="$(openssl rand -hex 24)"
SERVICE_TOKEN="$(openssl rand -hex 32)"
{
  echo "POSTGRES_PASSWORD=$POSTGRES_PASSWORD"
  echo "SESSION_SECRET=$(openssl rand -base64 48)"
  echo "IMPORTER_DB_PASSWORD=$IMPORTER_DB_PASSWORD"
  echo "SERVICE_TOKEN=$SERVICE_TOKEN"
} > "$ENV_FILE"

docker build -q --target test -t "$IMAGE" "$ROOT/services/importer" >/dev/null
dc up -d --wait --quiet-pull db >/dev/null 2>&1
# Applies migrations and enables the pt_importer login, exactly as the stack does at start-up.
dc run --rm --build --quiet-pull api migrate >/dev/null 2>&1 || dc run --rm api migrate

docker run --rm --network "${PROJECT}_default" -v "$ROOT/testdata:/testdata:ro" -e TESTDATA=/testdata \
  -e DB_HOST=db -e DB_PASSWORD="$IMPORTER_DB_PASSWORD" -e SERVICE_TOKEN="$SERVICE_TOKEN" \
  -e OWNER_DSN="postgresql://portfolio:$POSTGRES_PASSWORD@db:5432/portfolio" -e OWNER_PASSWORD="$POSTGRES_PASSWORD" \
  "$IMAGE" python -m pytest "$@"
