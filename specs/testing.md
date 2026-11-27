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
| API tests | Go `httptest` against a real Postgres | endpoints, auth, scopes, error and "data-quality" responses; import routes against a recording fake importer (streamed upload, upload CSRF header, refusals passed on, 404 for another user's batch) | `make test` |
| Importer service tests | pytest in Docker against a throwaway migrated Postgres (`scripts/importer-test.sh`), as `pt_importer` | upload flow: originals stored unchanged, file duplicates (D-8), per-user isolation, error codes, refusal to run as a role that bypasses RLS; preview corrections with the real parsers: action and symbol rules, exclusion, re-parse after a new account, rules per user (D-36) | `make test` |
| Provider tests | pytest with recorded HTTP responses | Yahoo / Bank of Canada parsing, partial failures, stale detection | `make test` |
| End-to-end | Playwright against `docker compose` | the 13 PRD acceptance criteria, clicked through in a browser with synthetic users and files | `make e2e`, before each milestone is closed |
| Type check | `tsc` | the web app and Playwright tests compile | `make test` |
| Private smoke test | `make smoke-private` (local only) | real exports in `data/` parse without crashing, with counts per account and status; the replayed ledger reconciling to the real holdings files follows in M8 | on Oliver's machine, on demand |

## Golden fixtures (`testdata/`)

| Fixture | Exercises |
| --- | --- |
| questrade/synthetic_activities | every action code in the mapping table, internal symbol codes (saved, proposed, unknown), TSX dividends without `.TO` (plain, dot-prefixed, units, proposed by name and by ticker), FX pair and unpaired leg, GIC round trip, journal and symbol-change pairs, withholding linked to its dividend, internal and external transfers, untracked account skipped, CSV and XLSX identical, two overlapping files with equal natural keys |
| wealthsimple/synthetic_activities | multi-account file, executed/received/effective date rules, FX rate in description, NRT linking (single, ratio, pro rata to the cent, TSX excluded, unlinked), USD sub-account, fee refund and reversal pair, in-kind transfer value, SUBDIVISION delta, NAME_CHANGE symbol change and split legs, robo detection, overlapping file with equal natural keys |
| snapshots/questrade_summary, snapshots/wealthsimple_holdings | Balances/Positions parsing, footer as-of, untracked accounts skipped, cash lines |
| synthetic/duplicates | commit (D-8, D-28, D-37): overlapping re-import, two legitimate identical trades, a third copy that is new, withholding linked through a duplicate dividend, an FX pair, a re-import where every row is a duplicate |
| synthetic/avg_cost | buys, partial sell, DRIP, split as ratio and as delta, return of capital, USD with and without a broker rate, same-day buy and sell -> qty, avg cost, realized gain, income, cash |
| synthetic/twr_flows | deposits/withdrawals mid-period, known daily values -> TWR to 1e-6 |
| synthetic/internal_transfer | in-kind and cash transfer between two tracked accounts (D-27), receiving leg dated first -> cost carried (M3); all-accounts TWR unchanged, subtotals see flows (M5) |
| synthetic/untracked_transfer | in-kind out and back in from an untracked account -> flows at market value, new average cost (D-23); a transfer in without a value -> cost unknown (D-38) |
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
FR-2 edit, close and archive, and the message shown when the browser drops the session cookie; AC-2, AC-3 and AC-4
(`ac2-imports.spec.ts`) with the synthetic Questrade and Wealthsimple activity fixtures, run by a third user per
browser: upload, review, fixes saved as rules, untracked accounts linked or tracked, a commit refused for unknown
exchanges, commit, the same file refused, overlapping and re-uploaded files adding only new rows, discard, and
another user seeing none of it. After Playwright, `scripts/e2e.sh` checks that every stored original is
byte-identical to one of the fixture files and read-only (AC-2, D-12).

## Private smoke test
`make smoke-private` (`scripts/smoke-private.sh`, `importer/smoke.py`) runs the importer's parsers on everything in
`data/`, on Oliver's machine only:
1. accounts come from `data/accounts.md` (columns `Account Number`, `Type`, `Broker`); every `.csv`/`.xlsx` in
   `data/questrade/` and `data/wealthsimple/` goes to that broker's parser, and files it refuses (holdings reports,
   summaries) are counted as "not an activities export";
2. per file: rows that would be added and rows the duplicate rule (D-8) would drop as already stored by an earlier
   file; per account: rows by status (ok / warning / error), plus unmapped rows and rows of untracked accounts; the
   importer's warning and error codes with counts; per broker: the ledger rows a commit of every file would hold;
3. files and accounts are numbered ("file 2 of 3", "account 1 (RRSP)", the position in `accounts.md` and its type),
   never named: no account numbers, names, file names, dates, symbols or amounts, so the output is safe to paste
   into a chat. A crashing parser is reported by exception type and source line only, and fails the run.
It runs in a throwaway importer container with `data/` mounted read-only, no network, a read-only file system and no
database, so nothing from `data/` is written anywhere. `tests/test_smoke.py` runs it on the synthetic fixtures and
checks that no value from them reaches the output. Excluded from CI. Replaying the ledger against the newest
holdings files follows with reconciliation (M8).

## Continuous integration
When a remote repo exists: GitHub Actions (or similar) runs `make test` on every push and `make e2e` on the main
branch. Until then, run them locally before each commit.
