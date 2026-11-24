.PHONY: up down test web-check e2e generate sqlc-check migrate logs

SQLC = docker run --rm -v "$(CURDIR)":/src -w /src/services/api sqlc/sqlc:1.31.1

up: .env
	docker compose up -d --build

down:
	docker compose down

# Parser, calculation, database and API tests. Database tests start Postgres with testcontainers (needs Docker).
test: sqlc-check web-check
	cd services/api && go vet ./... && go test -count=1 ./...

# Type-checks the web app and its Playwright tests.
web-check:
	cd services/web && npm ci --ignore-scripts --no-audit --no-fund && npm run typecheck

# Playwright acceptance tests against a throwaway stack on localhost:8090 (scripts/e2e.sh).
e2e:
	cd services/web && npm ci --ignore-scripts --no-audit --no-fund && npx playwright install chromium
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
	docker compose logs -f api

.env:
	@echo "Missing .env: copy .env.example to .env and set the values." >&2
	@exit 1
