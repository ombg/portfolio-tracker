# Changelog

Releases are tagged `ver-x.y.z` on `main` (`CONTRIBUTING.md`). Newest first.

## Unreleased
- Import API (FR-3, D-36, proposed): `POST /api/imports` streams the upload to the importer; `GET /api/imports`
  (history) and `GET /api/imports/{id}` (preview with rows, untracked accounts and `can_commit`);
  `PATCH /api/imports/{id}/rows/{row}` fixes an action or a symbol (saved as import rules for later files),
  excludes a row, or links an untracked account number to an existing account; re-parse and discard.
- importer: `POST /internal/imports/{id}/reparse` and `PATCH /internal/imports/{id}/rows/{row}`; both parsers apply
  the user's import rules. Migration 000004 adds `imports.import_rules`.
- Uploads need the header `X-Requested-With: portfolio-tracker` (CSRF, D-36); portfolio-api now gets
  `SERVICE_TOKEN` in Compose.
- Wealthsimple activities parser (CSV): every activity in `specs/import-wealthsimple.md`, description dates,
  non-resident tax linked to its dividend (single, 13-17% ratio, or pro rata to the cent), USD sub-accounts, in-kind
  transfers at market value, both name-change shapes, untracked robo accounts proposed (D-14, D-22), natural keys
  (D-8). Golden fixture in `testdata/wealthsimple/synthetic_activities`.
- Normalized rows gain `link_basis` and `allocations`; Questrade withholding links are `single`.
- Questrade activities parser (XLSX and CSV): every action in `specs/import-questrade.md`, withholding tax linked to
  its dividend, internal transfers (D-27), GICs (D-26), FX, symbol-change and journal pairs, untracked accounts
  skipped with a proposal (D-22), natural keys (D-8). Golden fixture in `testdata/questrade/synthetic_activities`.
- Spec corrections from the real format: blank-action `TAX WITHHELD` rows are withholding tax, GICs trade at 1.00,
  description dates are month first, securities-lending rows carry no symbol.
- importer service (`services/importer`, Python 3.12, FastAPI): `POST /internal/imports` stores the original file
  unchanged and content-addressed, refuses a file the user already imported (D-8, D-12), runs the broker's parser and
  stages its rows; internal network only, service token required. Parsers follow in the next changes.
- portfolio-api sets the `pt_importer` password from `IMPORTER_DB_PASSWORD` after migrating (D-35, confirmed).
- `.env` needs `IMPORTER_DB_PASSWORD` and a `SERVICE_TOKEN` of at least 32 bytes (see `.env.example`).
- `make test` also runs the importer tests (`make importer-test`); `make importer-lock` re-locks its dependencies.
- Ledger and import schema (migration 000003): securities, symbols with non-overlapping validity, aliases,
  immutable `transactions` with `transaction_corrections` and the `v_ledger` view, and the `imports` staging tables;
  forced RLS throughout and a `pt_importer` role that can write only `imports` (D-7, D-8, D-35).
- Generated Go uses `decimal.Decimal` for NUMERIC columns and `*time.Time` for nullable timestamps.

## ver-0.1.0 (2026-11-26)
M1 Foundation: AC-1 passes in the browser.
- Web app (`services/web`): sign in, accounts table, create and edit accounts (FR-2); served by Caddy, which proxies
  `/api` (the API is no longer published to the host).
- Sign-in works in Safari: Compose sends the session cookie without the Secure flag while the app is served over
  plain HTTP on 127.0.0.1 (`COOKIE_SECURE`, default `false`; set `true` behind HTTPS) (D-34). The sign-in page says
  when the browser did not keep the session cookie instead of silently showing sign-in again.
- Playwright acceptance tests: `make e2e` covers AC-1 against a throwaway stack in Chromium, Firefox and WebKit.
- `make test` also type-checks the web app.
- portfolio-api queries are generated with sqlc from `services/api/internal/db/queries` (`make generate`);
  `make test` fails when the generated code is out of date.

## ver-0.0.0 (2026-11-24)
First tagged state: specifications and the M1 back end so far. Not the M1 milestone release.
- v1 specifications in `specs/`, decisions D-1 to D-33.
- Database foundation: schemas `core`, `imports`, `market`; users, sessions, portfolios, brokers, currencies,
  accounts; forced row-level security that fails closed (D-1).
- portfolio-api: login, logout, session cookie, `GET /api/me` (D-32, D-33); accounts CRUD with brokers and
  currencies reference data (FR-2, D-14); `GET /api/health`.
- `ptctl user create` and `ptctl user set-password`.
- Docker Compose (db + api), Makefile (`up`, `down`, `test`, `migrate`).
