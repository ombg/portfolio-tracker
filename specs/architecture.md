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
| market-data | Python 3.12, httpx (Yahoo chart API, Bank of Canada Valet) | prices, fx_rates, refresh_runs, provider security metadata | 30-minute refresh, history backfill, metadata and dividend data |
| db | PostgreSQL 16 | all | Single database, one schema per owner (`core`, `imports`, `market`) |

### Rules
- **Only portfolio-api is reachable from the browser** (through Caddy). importer and market-data listen on the
  internal Docker network only and require a shared service token (`X-Service-Token`).
- **Only market-data reaches the internet** (D-39). Compose puts every service on `backend` (`internal: true`, no
  route out); market-data alone also joins `egress`, and web alone joins `edge` to publish its port on 127.0.0.1.
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
M2 screens (hash routes `#/accounts`, `#/imports`, `#/imports/<id>`, so Caddy serves one static bundle):
- Imports: upload form (broker, optional target account, file; sends `X-Requested-With`, D-36) and the history
  table (uploaded, file, broker, accounts, status, counts). A repeated file shows the server's refusal with a link to
  the earlier import.
- Preview: status and counts; a notice that names the blocking error rows, or how many rows (and how many with
  warnings) a commit would write. Rows table filtered by status, each with date, account, type, security,
  quantity, amount, the importer's messages and the source row (NFR-2). "Fix" appears only where the importer
  accepts one (unmapped action -> transaction type; unknown, proposed or unlisted security -> symbol and exchange)
  and explains that the fix is saved as a rule; "Exclude"/"Include" on every row that is not skipped. Untracked
  account numbers (D-22) offer "Track this account" (account form prefilled from the importer's proposal, then the
  file is parsed again) or a link to an existing account of the broker without a number.
- Commit and discard each need a second click on a confirmation that says what happens; nothing is committed
  silently. A refused commit (`unresolved_securities`) lists the rows and opens their fix. After commit each row
  shows inserted or duplicate, with the result counts.

M3 screens (D-38; routes `#/holdings`, `#/securities/<id>`; nav gains "Holdings"):
- Holdings: a scope picker (All accounts, Registered, Non-registered, one account) and a view switch "By account" /
  "By security".
  - By account: one section per account (name, type, broker; robo accounts labelled "Managed, account-level only").
    Rows per security: symbol and exchange (link to the security), name, quantity, average cost and book value in
    the book currency, book value in CAD, realized gain (CAD), income received (CAD), market value. Then one cash row
    per currency. Positions sold to zero are behind "Show closed positions" (they still carry realized gain and
    income).
  - By security: one row per security across the scope's accounts: total quantity, informational average cost
    (labelled so, FR-8), book value CAD, realized gain, income, number of accounts. Robo accounts are left out with
    a note ("excludes 1 managed account, account-level only").
  - Until market data exists (M4) market value, unrealized gain, total return and weight show "No price" with the
    reason, and totals say what they exclude; never 0 (NFR-4). Flags appear as badges on the row and are explained
    on hover and on the security page: `negative_holding` (position excluded from values from that date),
    `cost_unknown` (in-kind transfer in without a value), `fx_missing` (CAD book value not known yet),
    `estimated_cost` (D-31). An unknown value reads "Unknown" (in the warning colour) and a closed position's average
    cost reads "–": nothing is held, which is not the same as unknown. Decimals are rounded for display with string
    arithmetic, never floats (money 2 places, per-share cost up to 4).
- Security detail: header (symbol and exchange with earlier symbols, name, type or "Not classified yet", trading
  currency); the combined line; one row per account (account, registered or not, quantity, average cost, book
  value, realized gain, income); and every ledger row of the security in those accounts (date, account, type,
  quantity, price, amount, currency, and its source: file and row, linking to the import, or "manual"/"opening
  balance"), so each number on the page can be traced (rule 5). Corrected rows are marked.

M4 screens (D-39; route `#/market`, nav gains "Market data"):
- Status card on Holdings and on the Market data page (AC-12): last successful refresh, next scheduled refresh,
  run status (Success, Partial, Failed), "Market closed - last close <date>" outside the schedule, and the number of
  stale or unpriced securities with a link to the list.
- Market data page: the status card, recent runs (time, status, symbols ok and failed with the provider's error),
  securities without a price or with a stale one, and the user's price and FX overrides (add, remove).
- Holdings and security pages: market value, unrealized gain and weight; each market value shows its basis on hover
  and on the security page (price, source, time; FX rate, source, time), amber when stale, "Override" when it is one.
- Security page: "Override price" (date, price, reason) and the list of this security's overrides.

### Market API (FR-6, FR-5, D-39)
- `GET /api/market/status` -> `{last_success_at, last_run_status, next_run_at, market_open, last_close_date,
  stale[], unpriced[], runs[]}` (stale and unpriced limited to the caller's securities).
- `GET/POST /api/overrides/prices`, `DELETE /api/overrides/prices/{id}`; the same for `/api/overrides/fx`.
  A POST needs a reason; a DELETE supersedes (the row stays, NFR-3). RLS per user.

### Holdings API (FR-7, D-38)
Both routes need a session; the request runs in `WithUser`, so RLS limits ledger and accounts to the user. The whole
ledger is replayed (`internal/calc`) and then cut to the scope, because carried cost crosses accounts. Decimals are
JSON strings; an unknown value is `null` with a flag or reason, never 0 (NFR-4).
- `GET /api/holdings?scope=all|registered|non_registered|account:<id>` (default `all`; a bad scope is 400
  `invalid_scope`, another user's account 404) -> `{scope, as_of, accounts, securities, excluded, totals}`:
  - `as_of`: the latest trade date in the scope.
  - `accounts[]`: `{account: {id, name, type, broker, is_registered, management, status}, positions[], cash[]}`;
    archived accounts only when they have ledger rows. A position: `security` (id, symbol, exchange, name, type,
    trading_currency; symbol null for a GIC), `quantity`, `book_currency`, `avg_cost`, `book_value`, `avg_cost_cad`,
    `book_value_cad`, `realized_gain`, `realized_gain_cad`, `income[]` (per currency), `income_cad`, `market_value`
    (null until M4) with `market_value_reason` (`no_price`; empty for a closed position), `flags[]` (`{code, date,
    transaction_id}`), `open`. Listed securities first, by symbol.
  - `securities[]`: per security across the scope's self-directed accounts: `quantity`, `book_currency` and native
    `avg_cost`/`book_value` only when every account keeps the same book currency, `avg_cost_cad` (informational,
    FR-8), `book_value_cad`, `realized_gain_cad`, `income_cad`, `accounts`, `flags[]` (codes), `open`.
  - `excluded[]`: accounts left out of `securities` (`managed_account`, D-14).
  - `totals`: `book_value_cad` (open positions), `realized_gain_cad`, `income_cad`, `market_value` (with reason
    `no_price`), each `{amount, excludes}` where `excludes` counts the positions whose value is unknown; and `cash[]`
    per currency.
- `GET /api/securities/{id}` -> `{security, symbols[], positions[], combined, excluded[], transactions[]}`: every
  symbol period; the user's position per account (managed accounts included, and named in `excluded` for the
  combined line); every `v_ledger` row of the security with `origin`, `corrected` and `source` (file id and name,
  row number, import id; null for manual and opening rows). A security the user has no ledger row of answers 404
  like a missing one: the security master is global, positions are not.

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
   `ok | warning | error | duplicate | skipped`. See "importer internal API" below.
3. Browser shows preview (`GET /api/imports/{id}`); user fixes action, symbol or account with
   `PATCH /api/imports/{id}/rows/{row}`; action and symbol fixes are saved as import rules and the batch is parsed
   again (D-36).
4. `POST /api/imports/{id}/commit`: portfolio-api reads staged `ok`/`warning` rows and inserts them into
   `core.transactions` in one transaction (D-37); marking affected accounts dirty from the earliest date (D-13)
   follows with the valuation cache (M5).

### Import API (FR-3, D-22, D-36)
All routes require a session; another user's import answers 404 exactly like a missing one. Errors from the
importer (4xx, same shape) are passed on; when it cannot be reached or fails, 502 `importer_unavailable` (details
only in the log); without `SERVICE_TOKEN` configured, uploads and corrections answer 503.
- `POST /api/imports` (multipart: `file`, `broker`, optional `account_id`; header `X-Requested-With:
  portfolio-tracker`, D-36) -> the importer's 201 body and `Location: /api/imports/{batch_id}`. The body is
  re-encoded part by part into a pipe to the importer, never held in memory; other fields, a second file or no file
  give 400, more than 25 MiB gives 413 `file_too_large`.
- `GET /api/imports` -> `{"imports": [...]}`, newest first (at most 500): id, status, broker, `target_account_id`,
  `accounts` (accounts with rows that may reach the ledger), file (id, name, size, sha256, uploaded_at), parser and
  version, counts, `can_commit`, created_at, committed_at. History is per user (RLS), so the user is the caller.
- `GET /api/imports/{id}` -> the same summary plus `rows` (row_no, status, raw, normalized, messages, user_edits)
  and `untracked_accounts` (number, the importer's `proposal`, row count) for D-22.
- `can_commit`: the batch is `parsed` or `needs_review`, has no `error` row and at least one `ok`/`warning` row.
  Errors block commit until fixed or excluded.
- `PATCH /api/imports/{id}/rows/{row}` with any of `type` (ledger type for the row's action), `security`
  (`{"symbol","exchange"}`), `excluded` (bool), `account_id` -> `{"import": summary, "row": row}`. `type`,
  `security` and `excluded` go to the importer; `account_id` (rows of an untracked account only) stores the row's
  account number on that account when it has none (422 `unknown_account`, `account_broker_mismatch`,
  `not_applicable`; 409 `account_has_number`, `duplicate_account_number`), then the batch is parsed again. A batch
  that is not `parsed`/`needs_review` gives 409 `batch_closed`.
- `POST /api/imports/{id}/reparse`: parse again after creating an account -> summary.
- `POST /api/imports/{id}/discard`: status `discarded` (409 `batch_closed` unless open); rows and file stay, and the
  same file can be uploaded again (D-8).
- `POST /api/imports/{id}/commit` -> `{"import": summary, "result": {"inserted", "duplicate", "linked_transfers"}}`
  (D-37). 409 `batch_closed` unless open (so a second commit adds nothing), `not_committable` with error rows or
  nothing to commit, `unresolved_securities` with `rows: [{row, reason}]` (nothing is written). After commit, each
  `ok`/`warning` row in `GET /api/imports/{id}` has `outcome` (`inserted`, `duplicate`) and `transaction_id`.

### importer internal API
Every `/internal/*` call except health needs `X-Service-Token` (compared in constant time) and, for user data,
`X-User-Id`; the importer opens one transaction per request as `pt_importer` with that `app.user_id` and refuses to
run as a role that can bypass RLS (D-35). Errors use the same `{"error", "message"}` shape.
- `GET /internal/health`: 200, or 503 when the database is unreachable.
- `POST /internal/imports` (multipart: `file`, `broker` code, optional `account_id`) -> 201
  `{"batch_id", "source_file_id", "status", "counts"}`. Steps, in one transaction:
  1. The broker must have a parser (422 `unsupported_broker`); the file must be non-empty (422 `empty_file`) and at
     most 25 MiB (413 `file_too_large`); a target account must be the user's and at that broker (422
     `unknown_account`, `account_broker_mismatch`).
  2. SHA-256 of the bytes. If the user already has that file and its latest batch is `parsed`, `needs_review` or
     `committed`: 409 `duplicate_file` with `batch_id` and `source_file_id` of the earlier import (D-8). After a
     `failed` or `discarded` batch the same file can be uploaded again and gets a new batch.
  3. A new file is written once to `FILES_DIR/<first two hex digits>/<sha256>.<ext>`, read-only, and re-hashed after
     writing; an existing file is reused only if it still matches its hash (D-12).
  4. The broker's parser gets the bytes, the user's accounts at that broker and the target account. Its rows are
     staged with their source row numbers. Batch status: `needs_review` if any row is `error` or `warning`, else
     `parsed`; `counts` holds rows per status. A file the parser cannot read at all gives status `failed` with
     `counts.failure` (the reason) and no rows.
  Parsers are versioned (`parser`, `parser_version` on each batch), so new mappings never change earlier batches
  (FR-3 item 8). Questrade and Wealthsimple parsers are added in #15 and #16.
- `POST /internal/imports/{id}/reparse` -> 200 `{"batch_id", "status", "counts"}`: parses the stored original
  again (re-hashed first) with the current accounts, aliases and import rules, applies row edits, and updates every
  row in place; the batch takes the parser's current version. 404 `not_found` (also another user's batch), 409
  `batch_closed` unless `parsed`/`needs_review`.
- `PATCH /internal/imports/{id}/rows/{row}` with `type`, `security` and/or `excluded` -> as reparse, after saving
  an action rule (422 `invalid_type`; `not_applicable` unless the row's action is unmapped or ruled), a symbol rule
  (422 `invalid_security`: listed symbol and exchange TSX, TSXV, NEO, CSE or US; `not_applicable` unless the row's
  code or symbol is unknown, proposed, ruled or its listing open) or the row's exclusion (`not_applicable` for
  skipped rows). 400 `bad_request` for an empty or unknown field (D-36).

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
services/market-data/     Python package (providers/yahoo.py, providers/boc.py, calendar.py, stale.py)
testdata/                 golden fixtures (see specs/testing.md)
docker-compose.yml, Makefile, .env.example
data/                     private real exports (git-ignored, D-20)
backups/                  nightly backups (git-ignored, D-18)
```

## Configuration
`.env`: `POSTGRES_PASSWORD`, `SERVICE_TOKEN` (at least 32 bytes), `IMPORTER_DB_PASSWORD` and `MARKET_DB_PASSWORD`
(at least 24 characters each), `SESSION_SECRET`, `MARKET_DATA_PROVIDER` (`yahoo` default, `off`),
`REPORTING_CURRENCY_DEFAULT=CAD`, `FILES_DIR=/data/files`, `TZ_MARKET=America/Toronto`.
Volumes: `pgdata`, `files` (mounted only into the importer).

portfolio-api reads `DATABASE_URL` (owner connection, built by Compose from `POSTGRES_PASSWORD`), `MIGRATIONS_DIR`
(default `/migrations`, baked into the image from `db/migrations`), `LISTEN_ADDR` (default `:8080`),
`SESSION_SECRET` (required to serve, at least 32 bytes), `IMPORTER_DB_PASSWORD` (set as the `pt_importer`
password after every migration run; empty removes its login, D-35) and `COOKIE_SECURE` (the binary defaults to `true`; Compose
sets `false` by default because it serves plain HTTP on 127.0.0.1, and it must be `true` behind HTTPS; D-34),
`SERVICE_TOKEN` and `IMPORTER_URL` (`http://importer:8000`) for the importer's internal API, `MARKET_DB_PASSWORD`
(the `pt_market` password, likewise, D-39) and `MARKET_DATA_URL` (`http://market-data:8000`): after a commit it asks
market-data to backfill, in the background, without waiting or failing on it. It applies
pending migrations on every start; `api migrate` (`make migrate`) applies them and exits. `GET /api/health` returns
503 when the database is unreachable.

The importer reads `DB_HOST`, `DB_PORT` (5432), `DB_NAME` (`portfolio`), `DB_USER` (`pt_importer`), `DB_PASSWORD`
(from `IMPORTER_DB_PASSWORD`), `SERVICE_TOKEN` and `FILES_DIR` (`/data/files`), and refuses to start with a missing
password or a short token. It runs as an unprivileged user and is not published to the host. Python dependencies
are locked with hashes in `services/importer/requirements*.txt` by `make importer-lock`, which ignores releases
from the last 14 days. `.dockerignore` keeps `data/`, `backups/`, `context/` and `.env` out of every
build context (D-20).

market-data reads `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER` (`pt_market`), `DB_PASSWORD` (from
`MARKET_DB_PASSWORD`), `SERVICE_TOKEN`, `MARKET_DATA_PROVIDER` (`yahoo` | `fixture` | `off`), `MARKET_DATA_FIXTURES`
(recorded responses for `fixture`) and `MARKET_DATA_NOW` (a fixed clock, tests only), and refuses to start as a role
that could read user data. `make market-data-lock` locks its dependencies like the importer's.
`docker-compose.e2e.yml` switches it to recorded responses and a fixed clock for `make e2e`.

## Backups (D-18)
A `backup` container runs nightly at 02:00 local time: `pg_dump -Fc` and a copy of the `files` volume into
`./backups/YYYY-MM-DD/` on the host (git-ignored), deleting folders older than 30 days.
Restore: `make restore DATE=YYYY-MM-DD` stops the API, runs `pg_restore --clean`, copies files back, restarts.

## Private data (D-20)
`data/` holds real exports and the account inventory and is git-ignored. Nothing from it goes into code, specs,
fixtures or commits.
