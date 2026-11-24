# Architecture

Implements: PRD NFR-1..5, FR-1, FR-3, FR-6. Decisions: D-1, D-3, D-12, D-13, D-16.

## Services

Runs locally with `docker compose up`. One Postgres, four application containers.

```
browser --HTTPS--> web (Caddy + React SPA)
                     | /api/*  (all authenticated traffic)
                     v
               portfolio-api (Go) ---- internal HTTP ----> importer (Python)
                     |   ^                                    |
                     |   | internal HTTP (refresh triggers)   |
                     v   |                                    v
                 PostgreSQL  <----------------------------  market-data (Python)
```

| Service | Stack | Owns (tables) | Responsibilities |
| --- | --- | --- | --- |
| web | TypeScript, React, Vite, TanStack Query, Apache ECharts; Caddy | none | SPA; Caddy serves static files and reverse-proxies `/api` to portfolio-api |
| portfolio-api | Go 1.23+, chi, pgx v5 + sqlc, golang-migrate, shopspring/decimal | users, sessions, portfolios, accounts, transactions, corrections, overrides, targets, snapshots, daily_* caches | Auth, REST API, ledger, every calculation (holdings, average cost, TWR, XIRR, income, allocation), valuation cache job, migrations |
| importer | Python 3.12, FastAPI, pandas, openpyxl, pydantic | source_files, import_batches, import_rows | Parse broker files, map to normalized rows, validate, stage for preview; never writes the ledger directly |
| market-data | Python 3.12, yfinance, httpx, APScheduler | prices, fx_rates, refresh_runs, provider security metadata | 30-minute refresh, history backfill, metadata and dividend data |
| db | PostgreSQL 16 | all | Single database, one schema per owner (`core`, `imports`, `market`) |

### Rules
- **Only portfolio-api is reachable from the browser** (through Caddy). importer and market-data listen on the
  internal Docker network only and require a shared service token (`X-Service-Token`).
- **Table ownership**: a service writes only its own schema. Others read via views or ask the owner over HTTP.
  The ledger (`core.transactions`) is written only by portfolio-api.
- **Migrations** for all schemas live in `db/migrations` and are applied by portfolio-api at start-up.
- **User isolation**: portfolio-api connects as the database owner only to run migrations; every request
  transaction starts with `SET LOCAL ROLE pt_app` and `SET LOCAL app.user_id = $1`, and forced RLS policies on every
  user-owned table filter by it (D-1, `data-model.md`). Internal calls carry the user id explicitly.
- **Money**: never floats. Go uses `decimal.Decimal`, Python uses `decimal.Decimal`, Postgres `NUMERIC(24,8)`.
- **Time**: timestamps in UTC (`timestamptz`); trade and settlement dates are `date`; market schedule in
  America/Toronto.

## Key flows

### Import (FR-3)
1. Browser uploads file to `POST /api/imports` (portfolio-api), choosing broker and, optionally, target account.
2. portfolio-api streams it to importer `POST /internal/imports`. importer stores the original (D-12), hashes it,
   rejects exact duplicates (D-8), parses, maps and validates rows into `imports.import_rows` with status
   `ok | warning | error | duplicate`.
3. Browser shows preview (`GET /api/imports/{id}`); user fixes mappings (symbol alias, account, action) with
   `PATCH /api/imports/{id}/rows/{row}`; fixes that are reusable (aliases) are saved as overrides.
4. `POST /api/imports/{id}/commit`: portfolio-api reads staged `ok`/`warning` rows and inserts them into
   `core.transactions` in one transaction, then marks affected accounts dirty from the earliest date (D-13).

### Market data (FR-6)
1. market-data scheduler runs every 30 minutes in market hours (D-3) for every security held by any user.
2. Writes `market.prices` / `market.fx_rates` with source + observed_at, and one `market.refresh_runs` row.
3. New securities or backfill requests from portfolio-api (`POST /internal/backfill`) fetch daily history from the
   security's first transaction date.

### Valuation cache (D-13)
A goroutine worker in portfolio-api drains `core.recalc_queue (account_id, from_date)` and rebuilds
`daily_positions` and `daily_values` forward from `from_date`. Today's value is computed live from the latest quote.

## Repository layout

One repository for all services (D-30); workflow in `CONTRIBUTING.md`.

```
CLAUDE.md                 project guide for Claude Code
specs/                    this folder; source of truth for behaviour
db/migrations/            SQL migrations (golang-migrate)
services/web/             React app + Caddyfile
services/api/             Go module (cmd/api, cmd/ptctl, internal/...)
services/importer/        Python package (parsers/questrade.py, parsers/wealthsimple.py)
services/market-data/     Python package (providers/yahoo.py, providers/boc.py)
testdata/                 golden fixtures (see specs/testing.md)
docker-compose.yml, Makefile, .env.example
data/                     private real exports (git-ignored, D-20)
backups/                  nightly backups (git-ignored, D-18)
```

## Configuration
`.env`: `POSTGRES_PASSWORD`, `SERVICE_TOKEN`, `SESSION_SECRET`, `REPORTING_CURRENCY_DEFAULT=CAD`,
`FILES_DIR=/data/files`, `TZ_MARKET=America/Toronto`. Volumes: `pgdata`, `files`.

## Backups (D-18)
A `backup` container runs nightly at 02:00 local time: `pg_dump -Fc` and a copy of the `files` volume into
`./backups/YYYY-MM-DD/` on the host (git-ignored), deleting folders older than 30 days.
Restore: `make restore DATE=YYYY-MM-DD` stops the API, runs `pg_restore --clean`, copies files back, restarts.

## Private data (D-20)
`data/` holds real exports and the account inventory and is git-ignored. Nothing from it goes into code, specs,
fixtures or commits.
