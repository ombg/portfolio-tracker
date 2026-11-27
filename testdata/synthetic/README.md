# Synthetic ledgers

Hand-computed golden ledgers for the calculations (`specs/calculations.md`, `specs/testing.md`). Everything is
invented (D-20). Each folder holds `input/ledger.json` (rows as `v_ledger` gives them, short ids) and
`expected/*.json` worked out by hand; the comment in each expected file shows the arithmetic.

- `duplicates`: import commit and the duplicate rule (D-8), see its README.
- `avg_cost`: buys, partial sell, DRIP, split as ratio and as delta, return of capital, USD with and without a
  broker rate, a same-day buy and sell.
- `internal_transfer`: in-kind and cash transfer between two tracked accounts (D-27); cost carried.
- `untracked_transfer`: in kind out to and back from an untracked account at market value (D-23); a transfer in
  without a value.
