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
- **Queries** in portfolio-api are written as SQL in `services/api/internal/db/queries/*.sql` and compiled by sqlc
  (pinned Docker image, `make generate`) into `internal/db/dbq`, which is checked in. `make test` runs `sqlc diff`
  first and fails when the generated code is stale. Only transaction setup (`SET LOCAL ROLE`, `app.user_id`) and
  tests use hand-written SQL.
- **User isolation**: portfolio-api connects as the database owner only to run migrations; every request
  transaction starts with `SET LOCAL ROLE pt_app` and `SET LOCAL app.user_id = $1`, and forced RLS policies on every
  user-owned table filter by it (D-1, `data-model.md`). Internal calls carry the user id explicitly; the importer
  connects as its own role `pt_importer` and sets `app.user_id` from that id (D-35).
- **Money**: never floats. Go uses `decimal.Decimal`, Python uses `decimal.Decimal`, Postgres `NUMERIC(24,8)`.
- **Time**: timestamps in UTC (`timestamptz`); trade and settlement dates are `date`; market schedule in
  America/Toronto.

## Key flows

### Authentication (FR-1, D-1, D-32, D-33, D-34)
1. An admin creates users with `docker compose exec api ptctl user create --email E --name N [--currency CAD]`
   (password prompted twice); this also creates the user's portfolio (D-2). `ptctl user set-password --email E`
   resets a password and ends that user's sessions. ptctl connects as the database owner.
2. `POST /api/auth/login` `{"email","password"}` -> 204 and the `pt_session` cookie, or 401
   `{"error":"invalid_credentials"}`. Lookup and session creation go through `core.auth_user_for_login` and
   `core.session_create`, run as `pt_app`.
3. Every authenticated route resolves the cookie with `core.session_user_id` (also records activity), then runs its
   queries in `WithUser` (`pt_app` + `app.user_id`). Missing, forged, expired or idle sessions get 401
   `{"error":"unauthenticated"}`.
4. `GET /api/me` returns id, email, display name and reporting currency (`core.current_user_profile`).
5. `POST /api/auth/logout` deletes the session (`core.session_revoke`) and clears the cookie.

Errors from every endpoint have the shape `{"error": "<stable code>", "message": "<text>"}`.

### Web app (M1)
`services/web`: React + TypeScript (Vite, TanStack Query), built into a Caddy image. Caddy serves the SPA and proxies
`/api/*` to portfolio-api, which is no longer published to the host. Compose publishes Caddy on
`127.0.0.1:${WEB_PORT:-8080}` over plain HTTP, so the session cookie is sent without the Secure flag by default
(Safari drops Secure cookies on plain-HTTP localhost; D-34). TLS, with `COOKIE_SECURE=true`, is required before the
app is reachable from another machine. After login the sign-in page checks the session and reports a dropped cookie. Caddy sets a strict
Content-Security-Policy (`default-src 'self'`) and `X-Frame-Options: DENY`.
M1 screens: sign in; accounts table (open and closed, archived behind "Show archived", robo accounts labelled
"Managed, account-level only"); one form to create and edit accounts that shows the server's field errors.

### Accounts (FR-2, D-2, D-14)
All routes require a session and run in `WithUser`; another user's account answers 404 exactly like a missing one.
- `GET /api/brokers`, `GET /api/currencies`: reference data for the account form.
- `GET /api/accounts[?include_archived=true]` -> `{"accounts": [...]}`, open first, then closed (archived only when
  asked), by name.
- `POST /api/accounts` -> 201 + `Location`. Required: `broker` (code), `name`, `type`. Defaults: `base_currency` CAD,
  `management` self_directed, `status` open. The account goes into the user's single portfolio (D-2).
- `GET /api/accounts/{id}`; `PATCH /api/accounts/{id}`: absent fields stay, `null` clears nullable fields (`notes`,
  `external_number`, `opened_on`, `closed_on`, `migration_meta`). Close/reopen/archive = PATCH `status`.
- Derived values: `is_registered` follows `type` (client chooses only for OTHER, default false); `management =
  robo` sets `analysis_level = account`. Changing `type` or `management` re-derives them unless the same PATCH sets
  them; a contradicting value is 422.
- Rules: `broker` cannot change after creation (transactions will depend on it); setting `status` to `open` clears
  `closed_on`; `closed_on` not before `opened_on`; `external_number` unique per user and broker (409
  `duplicate_account_number`); `migration_meta` is a JSON object (FR-2 migration metadata). No delete: accounts are
  archived (NFR-3).
- Errors: 422 `{"error":"validation_failed","field":..., "message":...}`; 400 for malformed JSON or unknown fields.

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

portfolio-api reads `DATABASE_URL` (owner connection, built by Compose from `POSTGRES_PASSWORD`), `MIGRATIONS_DIR`
(default `/migrations`, baked into the image from `db/migrations`), `LISTEN_ADDR` (default `:8080`),
`SESSION_SECRET` (required to serve, at least 32 bytes) and `COOKIE_SECURE` (the binary defaults to `true`; Compose
sets `false` by default because it serves plain HTTP on 127.0.0.1, and it must be `true` behind HTTPS; D-34). It applies
pending migrations on every start; `api migrate` (`make migrate`) applies them and exits. `GET /api/health` returns
503 when the database is unreachable. `.dockerignore` keeps `data/`, `backups/`, `context/` and `.env` out of every
build context (D-20).

## Backups (D-18)
A `backup` container runs nightly at 02:00 local time: `pg_dump -Fc` and a copy of the `files` volume into
`./backups/YYYY-MM-DD/` on the host (git-ignored), deleting folders older than 30 days.
Restore: `make restore DATE=YYYY-MM-DD` stops the API, runs `pg_restore --clean`, copies files back, restarts.

## Private data (D-20)
`data/` holds real exports and the account inventory and is git-ignored. Nothing from it goes into code, specs,
fixtures or commits.
