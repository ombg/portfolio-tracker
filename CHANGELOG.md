# Changelog

Releases are tagged `ver-x.y.z` on `main` (`CONTRIBUTING.md`). Newest first.

## Unreleased
- Web app (`services/web`): sign in, accounts table, create and edit accounts (FR-2); served by Caddy, which proxies
  `/api` (the API is no longer published to the host).
- Playwright acceptance tests: `make e2e` covers AC-1 against a throwaway stack.
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
