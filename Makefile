.PHONY: up down test web-check importer-test importer-lock e2e smoke-private generate sqlc-check migrate logs

SQLC = docker run --rm -v "$(CURDIR)":/src -w /src/services/api sqlc/sqlc:1.31.1

up: .env
	docker compose up -d --build

down:
	docker compose down

# Parser, calculation, database and API tests. Database tests start Postgres with testcontainers (needs Docker).
test: sqlc-check web-check importer-test
	cd services/api && go vet ./... && go test -count=1 ./...

# Importer tests (pytest in Docker) against a throwaway migrated Postgres (scripts/importer-test.sh).
importer-test:
	./scripts/importer-test.sh

# Re-lock importer dependencies with hashes, ignoring releases from the last 14 days.
importer-lock:
	docker run --rm -v "$(CURDIR)/services/importer":/w -w /w python:3.12.16-slim ./lock.sh

# Type-checks the web app and its Playwright tests.
web-check:
	cd services/web && npm ci --ignore-scripts --no-audit --no-fund && npm run typecheck

# Parsers on the real exports in data/; prints counts only (scripts/smoke-private.sh, D-20). Local only, not in CI.
smoke-private:
	./scripts/smoke-private.sh

# Playwright acceptance tests against a throwaway stack on localhost:8090 (scripts/e2e.sh).
e2e:
	cd services/web && npm ci --ignore-scripts --no-audit --no-fund && npx playwright install chromium firefox webkit
	./scripts/e2e.sh

# Regenerate typed Go from services/api/internal/db/queries/*.sql and db/migrations.
generate:
	$(SQLC) generate

# Fails when the generated code does not match the queries and migrations (run `make generate`).
sqlc-check:
	$(SQLC) diff

migrate: .env
	docker compose run --rm --build api migrate

logs:
	docker compose logs -f api importer

.env:
	@echo "Missing .env: copy .env.example to .env and set the values." >&2
	@exit 1
