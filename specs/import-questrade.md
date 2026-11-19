# Import: Questrade

Implements: FR-3 for Questrade. Decisions: D-8, D-15. Parser: `services/importer/parsers/questrade.py`.
Sample: `data/rrsp_q/All_Activities_for_01Jan2020_to_31Dec2021.xlsx` (39 rows, one RRSP account). The PNG in the same
folder is a screenshot of the same trades and is not imported.

## Input
"All Activities" export, XLSX or CSV, one sheet `Activities`, header row 1. The XLSX has no stylesheet; open with
openpyxl/pandas and ignore the warning.

| Column | Type | Use |
| --- | --- | --- |
| Transaction Date | string `YYYY-MM-DD hh:mm:ss AM` | trade_date (date part) |
| Settlement Date | same | settle_date |
| Action | string, may be blank | primary type signal |
| Symbol | string | ticker with `.TO` suffix for TSX, none for US; may be an internal code (`H007816`) |
| Description | string | fallback for symbol and type; kept in raw |
| Quantity | decimal string | negative on sells |
| Price | decimal string | per share; per-share rate on distributions |
| Gross Amount | decimal string | negative on buys |
| Commission | decimal string | negative or 0 |
| Net Amount | decimal string | authoritative cash effect |
| Currency | CAD / USD | |
| Account # | string | maps to accounts.external_number |
| Activity Type | string | secondary type signal |
| Account Type | string, e.g. `Individual RRSP` | proposes account type for new accounts |

## Mapping

| Activity Type | Action | Ledger type | Rules |
| --- | --- | --- | --- |
| Trades | Buy | BUY | quantity as is, commission = abs(Commission) |
| Trades | Sell | SELL | quantity = abs(Quantity) |
| Dividends | DIV or blank | DIVIDEND (DISTRIBUTION when Description contains `DIST`) | gross = Net Amount when Gross is 0; quantity = 0 |
| Deposits | CON | DEPOSIT | external flow |
| FX conversion | FXT | FX_CONVERSION | pair the CAD and USD legs on the same date into one group_id |
| Transfers | TF6 | TRANSFER_IN | Description names the source institution; no counterparty -> external flow |
| any other | any | error row | user maps it in preview; mapping saved as an import rule |

Observed in the sample: Buy 15, Sell 4, CON 6, DIV 2, blank-action dividends 9, FXT 2, TF6 1.

## Symbol resolution
1. Strip and upper-case; `XGRO.TO` -> symbol XGRO, exchange TSX; `AP.UN.TO` -> AP.UN, TSX; no suffix -> US.
2. Look up `security_aliases` (broker questrade), then `security_symbols` valid on trade_date.
3. Unknown code (e.g. `H007816`): parse the fund name from Description (`ISHARES U S TECHNOLOGY ETF`) and match
   against securities traded by the same account; propose the match as a warning; user confirms -> alias saved.
4. Still unknown -> error row, blocks commit until resolved or excluded.

## Account resolution
Account # matches an existing account -> use it. Otherwise propose a new account (broker Questrade, type from
Account Type: `Individual RRSP` -> RRSP, `Individual TFSA` -> TFSA, `Individual margin`/`cash` -> NON_REGISTERED,
`Questwealth` -> MANAGED with analysis_level account) for the user to confirm.

## Validation
- Gross + Commission = Net within 0.01 for trades; otherwise warning.
- Quantity * Price = |Gross| within 0.01; otherwise warning.
- FX legs that cannot be paired -> warning, imported unlinked.
- Natural key and duplicate rule per D-8.

## Fixtures
`testdata/questrade/rrsp_2020_2021.xlsx` (copy of the sample) with expected normalized rows in
`testdata/questrade/rrsp_2020_2021.expected.json`.
