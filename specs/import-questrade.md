# Import: Questrade

Implements: FR-3 for Questrade. Decisions: D-8, D-15, D-20, D-22, D-26, D-27, D-28, D-29.
Parser: `services/importer/parsers/questrade.py`. Real exports live in `data/questrade/` (private, D-20); every
account number and amount below is invented.

## Input
"Activities" export, XLSX or CSV, one sheet `Activities`, header row 1. The XLSX has no stylesheet; open with
openpyxl/pandas and ignore the warning. One file can cover **several accounts** (all accounts of the login,
including ones the user does not track) and any date range.

| Column | Type | Use |
| --- | --- | --- |
| Transaction Date | string `YYYY-MM-DD hh:mm:ss AM` | trade_date (date part) |
| Settlement Date | same | settle_date |
| Action | string, may be blank | primary type signal |
| Symbol | string, may be empty | ticker with `.TO` suffix for TSX, none for US; may be an internal code (letter + 6 digits, e.g. `H012345`) or a GIC code |
| Description | string | fallback for symbol and type; holds share counts, account numbers, maturity data; kept in raw |
| Quantity | decimal string | negative on sells and outgoing corporate-action legs |
| Price | decimal string | per share; per-share rate on distributions |
| Gross Amount | decimal string | negative on buys |
| Commission | decimal string | negative or 0 |
| Net Amount | decimal string | authoritative cash effect |
| Currency | CAD / USD | currency of the cash effect (the account's CAD or USD side) |
| Account # | string | maps to accounts.external_number; unknown numbers are skipped (D-22) |
| Activity Type | string | secondary type signal |
| Account Type | string, e.g. `Individual RRSP` | proposes account type for new accounts |

## Mapping

| Activity Type | Action | Ledger type | Rules |
| --- | --- | --- | --- |
| Trades | Buy, Sell | BUY, SELL | quantity = abs(Quantity), commission = abs(Commission) |
| Trades | BUY (upper case, price 0, quantity = principal) | BUY of a GIC | D-26: GIC security, units at 1.00 |
| Dividends | DIV or blank | DIVIDEND (DISTRIBUTION when Description contains `DIST`) | gross = Net Amount when Gross is 0; "ON n SHS" in Description feeds D-29 |
| Dividends | DIS with "STK SPLIT" in Description | SPLIT (quantity delta) | delta = Quantity; cost unchanged |
| Deposits | CON | DEPOSIT, or internal TRANSFER_IN when Description names a tracked account (D-27) | e.g. "FHSA CONTRIBUTION FROM 1234567801" |
| Withdrawals | WDR | WITHDRAWAL, or internal TRANSFER_OUT per D-27 | e.g. "TFSA WITHDRAWAL TO 1234567801" |
| Transfers | TF6 | TRANSFER_IN | "TRANSFER FROM ACCOUNT 123-45678-01" -> internal (D-27); "TRANSFER <institution>" -> external, behaves as contribution (D-23) |
| Transfers | TFO | TRANSFER_OUT | same rules as TF6 |
| FX conversion | FXT | FX_CONVERSION | pair the CAD and USD legs of the same account and date into one group_id |
| Fees and rebates | FCH | FEE | robo management fee and its GST/HST |
| Fees and rebates | ADJ with "NRT REBATE" | WITHHOLDING_TAX (negative) | refund of withholding; symbol parsed from Description ("... REBATE ABCD") |
| Fees and rebates / Other | LFJ | INTEREST (securities lending) | "Securities lending int/reb payment ... Symbol-ABCD"; income linked to that security |
| Interest | INT | INTEREST | GIC interest is linked to the GIC security |
| Corporate actions | NAC | SYMBOL_CHANGE (pair) | "-n" leg with the old code and "+n" leg with the new symbol on the same date |
| Corporate actions | LIQ | SELL | fund liquidation: quantity = abs(Quantity), proceeds = Net Amount, price = proceeds / quantity |
| Corporate actions | RDM, RED | SELL of a GIC | redemption at 1.00 per unit |
| Other | BRW | JOURNAL (pair) | moves a position between the account's CAD and USD sides; quantity nets to zero, no cash, no flow |
| Other | GRT, EGT | GRANT | government grant (RESP); income-like inflow, external flow for TWR |
| any other | any | error row | user maps it in preview; mapping saved as an import rule |

## Several files, overlapping ranges
Exports with overlapping dates are normal (e.g. an older export that reaches further back than a newer one).
All files go through the same duplicate rule (D-8): rows already in the ledger are reported as duplicates, the
rest are added (D-28). Rows of untracked accounts are skipped and counted (D-22).

## Symbol resolution
1. Strip and upper-case; `XGRO.TO` -> symbol XGRO, exchange TSX; `AP.UN.TO` -> AP.UN, TSX; no suffix -> US.
2. Look up `security_aliases` (broker questrade), then `security_symbols` valid on trade_date.
3. Internal code (letter + 6 digits): match the Description's security name against securities the account has
   traded or holds (e.g. code for "ISHARES TRUST ISHARES IBONDS DEC 2025 TERM" -> IBDQ); propose as a warning;
   the user confirms -> alias saved. Symbol-change legs (NAC) and split legs (DIS) usually carry such codes.
4. GIC codes: one security per GIC (issuer, rate, maturity parsed from Description), type `gic` (D-26).
5. Still unknown -> error row, blocks commit until resolved or excluded.

## Account resolution
Account # matches a tracked account -> use it. Unknown -> skipped and listed as "not tracked" (D-22); the preview
offers to create the account instead (broker Questrade, type from Account Type: `Individual RRSP` -> RRSP,
`Individual TFSA` -> TFSA, `Individual FHSA` -> FHSA, `Individual family RESP` -> RESP, margin/cash ->
NON_REGISTERED; Questwealth accounts -> their tax type with `management = robo`, D-14).

## Validation
- Gross + Commission = Net within 0.01 for trades; otherwise warning.
- Quantity * Price = |Gross| within 0.01; otherwise warning.
- FX legs or corporate-action legs that cannot be paired -> warning, imported unlinked.
- Dividend share count ("ON n SHS") differs from the ledger's quantity on the record date -> data-quality issue
  `dividend_share_mismatch` (D-29); a strong sign of missing history.
- Natural key and duplicate rule per D-8.

## Fixtures
`testdata/questrade/synthetic_activities.xlsx` and `.csv`: invented rows in the real column layout covering every
row of the mapping table, two accounts plus one untracked account, an internal transfer pair, a GIC round trip,
and a second file overlapping the first. Expected normalized rows in `.expected.json`. Never a copy of a real
export (D-20).
