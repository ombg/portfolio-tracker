# Import: broker snapshots (holdings files)

Implements: FR-4 reconciliation input, AC-5. Decisions: D-11, D-20, D-22.
Parser: `services/importer/parsers/snapshots/{questrade.py,wealthsimple.py}`. Real samples are private (`data/`).

## Common rules
- Output per file: one `broker_snapshots` row per **tracked** account (matched on account number) with
  `snapshot_lines` (security quantity lines and cash lines per currency). Rows whose account number matches no
  account of the user are skipped and reported as "not tracked: n accounts"; they never enter totals (D-22).
- `as_of`: from the file when present, else from the file name date, confirmed by the user in preview.
- Book value and market value from the broker are stored on the line as informational fields (`broker_book_value`,
  `broker_market_value`, currency) and shown next to the app's figures, but only quantity and cash are reconciled.
- Securities are resolved with the same alias rules as activity imports; the broker's exchange code (when given)
  is used to pick the listing and is saved as an alias.
- Snapshots are stored like any import: original file kept (D-12), preview before commit, duplicate file rejected.

## Questrade: Investment Summary (XLSX)
File name `InvestmentSummary_YYYYMMDD.xlsx`; no as-of inside the file, so `as_of` = the name's date.

| Sheet | Used | Columns |
| --- | --- | --- |
| Balances | yes: cash lines | Account Number, Account Type, FX Rate, Cash in CAD, Cash in USD, Cash in CAD Combined, Market Value in CAD / USD / CAD Combined (header typo "Market Valuein" exists: match headers case- and space-insensitively), Total Equity in CAD / USD / CAD Combined |
| Positions | yes: security lines | Equity Symbol, Equity Description, Account Number, Currency, Cost Basis, Asset Class, Quantity, Cost Per Share, Position Cost, Market Price, Market Value, Profit And Loss, % Return, % of Portfolio |
| AllocationsOwnedSD, SecuritiesOwnedSD, AllocationsOwnedPIQ, SecuritiesOwnedPIQ | no | broker summaries for self-directed (SD) and Questwealth (PIQ); ignored |

- Cash lines: one per currency from `Cash in CAD` and `Cash in USD`; the "Combined" columns are ignored (computed).
- Positions: `Equity Symbol` has no exchange suffix; listing chosen from `Currency` (CAD -> TSX, USD -> US) and the
  description; fractional quantities occur in Questwealth accounts.
- `Asset Class` is `STK`, `ETF`, `ADR` for self-directed accounts and categories like "Fixed income" for Questwealth;
  stored as a metadata hint, not as the security's asset class.

## Wealthsimple: holdings report (CSV)
File name `holdings-report-YYYY-MM-DD.csv`. Last non-empty line is a footer `"As of YYYY-MM-DD HH:MM GMT-07:00"`,
which gives `as_of` (date in America/Toronto after converting the offset). Rows without an account number are skipped.

| Column | Use |
| --- | --- |
| Account Name, Account Type | display only; the robo account can show its tax type here while the activities export shows a portfolio name, so match on number only |
| Account Classification | `Trade` = self-directed, `Managed` = robo; confirms `management` (D-14) |
| Account Number | match key (same value as `account_id` in the activities export) |
| Symbol, Exchange, MIC, Name | security; MIC (`XTSE`, `XNYS`, `XNAS`, `BATS`) resolves the listing unambiguously |
| Security Type | `CURRENCY` rows are cash lines (Symbol = currency code, Quantity = amount); `EQUITY`, `EXCHANGE_TRADED_FUND` are positions |
| Quantity, Position Direction | quantity (LONG only in v1) |
| Market Price, Market Price Currency, Market Value, Market Value Currency | informational |
| Book Value (CAD), Book Value (Market) + their currency columns | informational |
| Market Unrealized Returns (+ currency) | ignored |

## Validation
- Sum of position market values + cash per account vs. the broker's total (Questrade Total Equity) within 0.05:
  warning only, it checks the parse.
- A tracked account missing from the file: warning ("not in this snapshot").
