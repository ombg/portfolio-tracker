.PHONY: up down test migrate logs

up: .env
	docker compose up -d --build

down:
	docker compose down

# Parser, calculation, database and API tests. Database tests start Postgres with testcontainers (needs Docker).
test:
	cd services/api && go vet ./... && go test -count=1 ./...

migrate: .env
	docker compose run --rm --build api migrate

logs:
	docker compose logs -f api

.env:
	@echo "Missing .env: copy .env.example to .env and set the values." >&2
	@exit 1
