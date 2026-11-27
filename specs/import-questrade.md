# Import: Questrade

Implements: FR-3 for Questrade. Decisions: D-8, D-15, D-20, D-22, D-26, D-27, D-28, D-29.
Parser: `services/importer/parsers/questrade.py`. Real exports live in `data/questrade/` (private, D-20); every
account number and amount below is invented.

## Input
"Activities" export, XLSX or CSV, one sheet `Activities`, header row 1. The XLSX has no stylesheet; open with
openpyxl and ignore the warning. Every cell is text, including numbers; the export contains fully empty rows, which
are ignored. Row numbers in the preview are spreadsheet rows (header = 1). Dates inside descriptions are month first:
`REC 03/14/25 PAY 03/31/25`, GIC dates `01/15/2026`. One file can cover **several accounts** (all accounts of the login,
including ones the user does not track) and any date range.

| Column | Type | Use |
| --- | --- | --- |
| Transaction Date | string `YYYY-MM-DD hh:mm:ss AM` | trade_date (date part) |
| Settlement Date | same | settle_date |
| Action | string, may be blank | primary type signal |
| Symbol | string, may be empty | ticker with `.TO` suffix for TSX, none for US on trades; dividend rows drop the `.TO` (plain `QZGR`, or `.QZGR` on DIV rows, `SYNU.UN` for units); may be an internal code (letter + 6 digits, e.g. `H012345`) or a GIC code |
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
| Trades | BUY (upper case, GIC code as symbol, price 1.00, quantity = principal) | BUY of a GIC | D-26: GIC security, units at 1.00; Gross is 0 and is not used |
| Dividends | blank, Description contains `TAX WITHHELD`, Net < 0 | WITHHOLDING_TAX | linked (`related_row`) to the dividend of the same account, security, pay date and currency in the file; otherwise a warning and linked at commit or by hand |
| Dividends | DIV or blank | DIVIDEND (DISTRIBUTION when Description contains the word `DIST`) | gross = Net Amount when Gross is 0; record and pay date from `REC .. PAY ..`; "ON n SHS" feeds D-29 (`shares_on`); a DIV row mentioning `TAX WITHHELD` with Net >= 0 is the gross dividend (the tax is its own row); a negative dividend is a warning |
| Dividends | DIS with "STK SPLIT" in Description | SPLIT (quantity delta) | delta = Quantity; cost unchanged |
| Deposits | CON | DEPOSIT, or internal TRANSFER_IN when Description names a tracked account (D-27) | e.g. "FHSA CONTRIBUTION FROM 1234567801" |
| Withdrawals | WDR | WITHDRAWAL, or internal TRANSFER_OUT per D-27 | e.g. "TFSA WITHDRAWAL TO 1234567801" |
| Transfers | TF6 | TRANSFER_IN | "TRANSFER FROM ACCOUNT 123-45678-01" -> internal (D-27); "TRANSFER <institution>" -> external, behaves as contribution (D-23) |
| Transfers | TFO | TRANSFER_OUT | same rules as TF6 |
| FX conversion | FXT | FX_CONVERSION | pair the CAD and USD legs of the same account and date into one group_id |
| Fees and rebates | FCH | FEE | robo management fee and its GST/HST |
| Fees and rebates | ADJ with "NRT REBATE" | WITHHOLDING_TAX (negative) | refund of withholding; symbol parsed from Description ("... REBATE ABCD") |
| Fees and rebates / Other | LFJ | INTEREST (securities lending) | "Securities lending int payment as of ..."; the exports seen name no symbol, so the row is unlinked; a `Symbol-ABCD` token, if present, links it |
| Interest | INT | INTEREST | GIC interest is linked to the GIC security |
| Corporate actions | NAC | SYMBOL_CHANGE (pair) | "-n" leg with the old code and "+n" leg with the new symbol on the same date |
| Corporate actions | LIQ | SELL | fund liquidation: quantity = abs(Quantity), proceeds = Net Amount, price = proceeds / quantity |
| Corporate actions | RDM, RED | SELL of a GIC | redemption at 1.00 per unit (quantity = principal) |
| Other | BRW | JOURNAL (pair) | moves a position between the account's CAD and USD sides; quantity nets to zero, no cash, no flow |
| Other | GRT, EGT | GRANT | government grant (RESP); income-like inflow, external flow for TWR; negative = repayment |
| Fees and rebates | ADJ without "NRT REBATE" | error row | other rebates exist; the user maps them |
| any other | any | error row | user maps it in preview; mapping saved as an action rule keyed `Activity Type|Action` (D-36) |

Quantity and price are kept for share events only: BUY/SELL (absolute quantity, commission as a positive number),
SPLIT (signed delta), SYMBOL_CHANGE and JOURNAL legs (signed), in-kind transfers. Cash rows have neither.
`EXCHANGE RATE x` in a trade description is stored as `fx_rate`, `fx_source = broker`. The security name is the
description up to the first of "WE ACTED AS AGENT", "CASH DIV", "DIST ON", "STK SPLIT", "TAX WITHHELD",
"NAME CHANGE", "LIQUIDATION", "JOURNAL" and similar markers (provisional until market data fills it, M4).

## Several files, overlapping ranges
Exports with overlapping dates are normal (e.g. an older export that reaches further back than a newer one).
All files go through the same duplicate rule (D-8): rows already in the ledger are reported as duplicates, the
rest are added (D-28). Rows of untracked accounts are skipped and counted (D-22).

## Symbol resolution
1. Strip and upper-case; `QZGR.TO` -> symbol QZGR, exchange TSX; `AP.UN.TO` -> AP.UN, TSX. Without `.TO` (dividend
   rows), drop a leading `.`, then: the file names `<symbol>.TO` on any row -> that TSX listing (info
   `tsx_from_file`); else a USD row without a period -> US; else a CAD row -> proposed on the TSX
   (`proposed_security`), as the TSX security this file traded under the same name (matched as in step 3) or
   else the symbol itself. A CAD row is never read as US by default (D-37). Anything else -> step 5.
2. Look up `security_aliases` (broker questrade), then `security_symbols` valid on trade_date.
3. Internal code (letter + 6 digits): a saved alias resolves it. Otherwise match the Description's security name
   against securities traded under a listed symbol in the same file (exact, or one name a prefix of the other, a
   single candidate); propose it as a warning (`proposed_security`); the user confirms or chooses -> symbol rule
   saved (D-36), and the alias is written when the batch is committed; committing the warning row as it is accepts
   the proposal (D-37). Names are not matched against securities already in the ledger; a code the file does not
   explain is chosen in the preview. Symbol-change legs (NAC), split legs
   (DIS), journals (BRW) and liquidations (LIQ) usually carry such codes.
4. GIC codes (digit, five letters or digits, digit, e.g. `1SYNB07`): one security per GIC, type `gic` (D-26);
   issuer = text before " GIC" (or the first date), rate = first percentage, maturity = latest date in the
   Description. No alias needed.
5. Still unknown -> error row, blocks commit until resolved or excluded.

## Account resolution
Account # matches a tracked account (digits compared) -> use it. Unknown -> status `skipped`, message
`untracked_account`, and the row carries the proposal (D-22): broker Questrade, type from Account Type
(`Individual RRSP` -> RRSP, `Individual TFSA` -> TFSA, `Individual FHSA` -> FHSA, `Individual family RESP` -> RESP,
margin/cash -> NON_REGISTERED, anything else -> OTHER), `management = self_directed`. The export does not say
which accounts are Questwealth; the user sets `management = robo` (D-14) when creating the account. With a target
account chosen at upload, rows of the user's other accounts are skipped (`other_account`).

Internal transfers (D-27) are detected only on CON, WDR, TF6 and TFO rows: a run of digits (dashes ignored) that
starts with another tracked account's number and has at most two more digits (`9000000101`, `900-00001-01` for
account `90000001`) gives TRANSFER_IN/OUT with `counterparty_account_id` and `transfer_status =
awaiting_counterpart`; the legs are paired at commit.

Within one file, the two FX_CONVERSION legs of an account and day (different currencies, opposite signs), the two
SYMBOL_CHANGE legs (opposite quantities) and the two JOURNAL legs (same security, opposite quantities) get one
`group` key; a leg without a partner is a warning (`unpaired_fx_leg`, `unpaired_symbol_change`,
`unpaired_journal`).

## Validation
- Gross + Commission = Net within 0.01 for trades with a Gross; otherwise warning `net_mismatch`.
- Quantity * Price = |Gross| within 0.01; otherwise warning `gross_mismatch`.
- FX legs or corporate-action legs that cannot be paired -> warning, imported unlinked.
- Dividend share count ("ON n SHS") differs from the ledger's quantity on the record date -> data-quality issue
  `dividend_share_mismatch` (D-29); a strong sign of missing history.
- Natural key and duplicate rule per D-8.

## Fixtures
`testdata/questrade/synthetic_activities/`: `make_inputs.py` holds the invented rows and writes
`input/activities.{csv,xlsx}` and the overlapping `input/activities_older.{csv,xlsx}` in the real layout;
`input/context.json` is the parse context (three tracked accounts, two saved broker codes); `expected/*.json` is the
hand-written output. Covers every mapping row, an untracked account, internal transfers (contribution, withdrawal,
transfer), an external transfer, a GIC round trip, withholding linked to its dividend, symbol-change and journal
pairs, an FX pair and an unpaired leg, a proposed and an unknown broker code, unmapped actions, an empty row, and
overlap with equal natural keys. CSV and XLSX must give identical rows. Never a copy of a real export (D-20).

Parser: `questrade-activities`, version 1 (`services/importer/importer/parsers/questrade.py`). Natural key (D-8):
SHA-256 of `questrade|<account id>|<trade date>|<Action, or Activity Type when blank>|<Symbol>|<Quantity>|<Price>|
<Net Amount>|<Currency>`, decimals without trailing zeros.
