# Portfolio Tracker

Self-hosted, multi-user portfolio tracker for Canadian brokerage accounts (Questrade, Wealthsimple). Imports broker
exports into an immutable transaction ledger and reports holdings, TWR/XIRR performance, dividend income and
diversification. Runs locally with Docker Compose.

## Where things are
- `chatgpt/docs/portfolio-tracker-requirements.md`: product requirements (PRD). What, not how.
- `CONTRIBUTING.md`: branches, pull requests, release tags.
- `specs/`: how. Start with `specs/README.md`; decisions and their IDs are in `specs/decisions.md`.
- `context/`: the original Google Sheets tracker (reference only, do not import, do not track the original spreadsheet with git or commit to a public repo). Its holdings are dummy data; it is a feature reference (D-19).
- `data/`: real broker exports and `data/accounts.md` (the real account inventory). Private financial data and
  git-ignored: never commit, never send to external services, never copy into `testdata/` (D-20).
- `services/{web,api,importer,market-data}`, `db/migrations`, `testdata/`: code (to be created, see roadmap).

## Rules that must not be broken
1. The ledger (`core.transactions`) is written only by portfolio-api and imported rows are never updated in place;
   corrections go to `transaction_corrections` (D-7).
2. Every user-owned table has `user_id` and an RLS policy; every API query runs with `app.user_id` set (D-1).
3. Money and quantities are decimals end to end (`NUMERIC`, `decimal.Decimal`, `shopspring/decimal`). No floats.
4. Missing or stale data is shown as a flagged condition, never as 0 or success (NFR-4, `specs/data-quality.md`).
5. Every displayed value can be traced to transactions, market observations (source + timestamp) or overrides.
6. Behaviour changes update the matching spec in the same change. New decisions get the next D-number.
7. Nothing from `data/` (account numbers, names, dates, amounts, holdings) appears in code, specs, fixtures, logs or
   commit messages. Fixtures are synthetic (D-20, `specs/testing.md`). Reading `data/` locally to learn formats is fine.
8. Git: never commit to `main` or `dev` directly. Work on a feature branch from `dev` and open a pull request;
   releases are merged to `main` and tagged `ver-x.y.z` (`CONTRIBUTING.md`).

## Stack
Go (portfolio-api: chi, pgx, sqlc, golang-migrate), Python 3.12 (importer: FastAPI, pandas; market-data: yfinance,
APScheduler), React + TypeScript (Vite, TanStack Query, ECharts), PostgreSQL 16, Caddy.

## Commands (once scaffolded)
- `make up` / `make down`: start/stop the stack
- `make test`: parser, calculation, database and API tests; `make e2e`: Playwright acceptance tests
- `make smoke-private`: local-only check of the real exports in `data/` (prints counts only)
- `make migrate`: apply migrations; `docker compose exec api ptctl user create`: add a user

## Working style
- Before implementing a milestone, read its specs and add golden fixtures first (`specs/testing.md`).
- Open questions are listed at the end of `specs/decisions.md`; ask Oliver rather than guessing.
