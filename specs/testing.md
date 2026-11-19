# Testing

Purpose: make "correct" testable, not just "visible" (PRD amendment 10).

## Golden fixtures (`testdata/`)
Each fixture = input files + expected outputs as JSON, hand-checked.

| Fixture | Exercises |
| --- | --- |
| questrade/rrsp_2020_2021 | parser mapping, blank-action dividends, H007816 alias, FX pair, TF6 transfer |
| synthetic/avg_cost | buys, partial sell, DRIP, split 2:1, return of capital -> qty, avg cost, realized gain |
| synthetic/twr_flows | deposits/withdrawals mid-period, known daily values -> TWR to 1e-6 |
| synthetic/internal_transfer | TFSA -> non-registered cash transfer -> all-accounts TWR unchanged, subtotals see flows |
| synthetic/xirr | textbook cash flows with known IRR |
| synthetic/duplicates | re-import overlap and two legitimate identical trades (D-8) |
| synthetic/stale_data | missing price/FX -> flags, no zeros |

## Test layers
- Go: table-driven unit tests for `internal/calc`; integration tests against Postgres (testcontainers) for RLS:
  user B can never read user A's rows through any endpoint.
- Python: pytest per parser with fixtures; provider clients tested with recorded responses (no live network in CI).
- End to end: Playwright for acceptance criteria 1-13 of the PRD.
