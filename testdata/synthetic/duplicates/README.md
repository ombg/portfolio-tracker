# synthetic/duplicates

Commit (#18) and the row duplicate rule (D-8, D-28, D-35) on staged batches, as the importer leaves them in
`imports.import_rows`. Everything is invented (D-20): one account, made-up symbols, natural keys are arbitrary
64-hex strings (real ones are SHA-256 of the broker fields).

- `input/older.json`: an older export. Rows 3 and 4 are two legitimate identical trades (same natural key `k2`).
- `input/newer.json`: a newer export that overlaps it: three identical trades (one new), the dividend and its
  withholding again, plus a sale and an FX pair that are new. Row 6's withholding points at row 5's dividend,
  which is a duplicate, so it must link to the dividend already in the ledger.
- `input/again.json`: the newer rows once more, in a file with different bytes: everything is a duplicate.
- `expected/outcomes.json`: per batch, which rows are inserted and which are duplicates, and ledger facts after
  each commit.

Natural keys used: `k1` deposit, `k2` buy, `k3` dividend, `k4` withholding, `k5` sell, `k6`/`k7` FX legs; each
`kN` stands for `N` repeated 64 times in hex (`k1` = `111...1`).
