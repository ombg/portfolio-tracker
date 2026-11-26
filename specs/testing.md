# Testing

Purpose: prove the numbers are right, not just that screens render (PRD amendment 10). Decisions: D-20.

## Principles
1. **Spec first, fixture first.** Before a feature is coded, its expected results are written down as a fixture
   (input files + expected JSON), checked by hand or in a spreadsheet. The code is done when it reproduces them.
2. **Synthetic data in the repo, real data never.** Fixtures use invented accounts, dates and amounts in the
   brokers' real formats (D-20). Real exports in `data/` are only used by the private smoke test, which runs
   locally and prints counts, never values.
3. **Deterministic.** No live network in tests: market-data providers are replaced by recorded responses; "today"
   and the refresh clock are injected.
4. **Exact money.** Assertions compare decimals exactly (or to a stated tolerance such as 1e-6 for returns).

## Layers

| Layer | Tool | What it proves | Runs |
| --- | --- | --- | --- |
| Parser tests | pytest | every row type in each broker format maps to the expected normalized row, warnings and errors included | `make test`, every commit |
| Calculation tests | Go `testing`, table-driven | quantity, average cost, realized gain, cash, TWR, XIRR, income, allocation on golden ledgers | `make test` |
| Database tests | Go + testcontainers (Postgres in Docker) | migrations apply; RLS: user B never reads user A's rows through any query or endpoint; ledger immutability | `make test` |
| API tests | Go `httptest` against a real Postgres | endpoints, auth, scopes, error and "data-quality" responses | `make test` |
| Provider tests | pytest with recorded HTTP responses | Yahoo / Bank of Canada parsing, partial failures, stale detection | `make test` |
| End-to-end | Playwright against `docker compose` | the 13 PRD acceptance criteria, clicked through in a browser with synthetic users and files | `make e2e`, before each milestone is closed |
| Type check | `tsc` | the web app and Playwright tests compile | `make test` |
| Private smoke test | `make smoke-private` (local only) | real exports in `data/` import without errors, and the replayed ledger reconciles to the real holdings files | on Oliver's machine, on demand |

## Golden fixtures (`testdata/`)

| Fixture | Exercises |
| --- | --- |
| questrade/synthetic_activities | every action code in the mapping table, internal symbol codes, FX pair, GIC round trip, journal pair, untracked account skipped, two overlapping files |
| wealthsimple/synthetic_activities | multi-account file, executed-date rule, FX rate in description, NRT linking (1, ratio, pro-rata), fee refund pair, in-kind transfer value, SUBDIVISION delta, NAME_CHANGE symbol change, robo detection |
| snapshots/questrade_summary, snapshots/wealthsimple_holdings | Balances/Positions parsing, footer as-of, untracked accounts skipped, cash lines |
| synthetic/avg_cost | buys, partial sell, DRIP, split as ratio and as delta, return of capital -> qty, avg cost, realized gain |
| synthetic/twr_flows | deposits/withdrawals mid-period, known daily values -> TWR to 1e-6 |
| synthetic/internal_transfer | transfer between two tracked accounts detected from the description, paired across files -> all-accounts TWR unchanged, subtotals see flows |
| synthetic/untracked_transfer | in-kind out and back in from an untracked account -> flows at market value, new average cost (D-23) |
| synthetic/xirr | textbook cash flows with known IRR |
| synthetic/duplicates | re-import overlap and two legitimate identical trades (D-8) |
| synthetic/stale_data | missing price/FX -> flags, no zeros |
| synthetic/history_gap | sale before any buy, late first transaction -> negative_holding, history_gap_start, fixed by an opening-positions CSV |

Each fixture folder holds `input/` files and `expected/*.json`. A failing test prints the first differing field.

## End-to-end runs
`make e2e` runs `scripts/e2e.sh`: it starts a separate Compose project (`pt-e2e`, port 8090, own volumes) with
random secrets, creates two synthetic users per browser with `ptctl`, runs Playwright in Chromium, Firefox and
WebKit (Safari's engine, D-34) from `services/web/e2e`, and removes the stack and its volumes afterwards, also on
failure. Row checks compare every table cell exactly so one
column cannot hide a wrong value in another. Covered so far: AC-1 (`ac1-accounts.spec.ts`), plus FR-1 isolation and
FR-2 edit, close and archive, and the message shown when the browser drops the session cookie.

## Private smoke test
`make smoke-private` runs the importer and the calculation engine on everything in `data/` inside the local stack:
1. parse every activity export; report rows by status (ok / warning / error) per account;
2. replay the ledger and compare quantities and cash with the newest holdings files per account;
3. print only counts and pass/fail per account (e.g. "account 2 of 3: 21/23 positions match, cash matches"),
   never account numbers, symbols or amounts, so the output is safe to paste into a chat.
It never writes outside the local database and is excluded from CI.

## Continuous integration
When a remote repo exists: GitHub Actions (or similar) runs `make test` on every push and `make e2e` on the main
branch. Until then, run them locally before each commit.
