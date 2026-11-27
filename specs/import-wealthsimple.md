# Import: Wealthsimple

Implements: FR-3 for Wealthsimple. Decisions: D-6, D-8, D-14, D-15, D-20, D-21.
Parser: `services/importer/parsers/wealthsimple.py`. Real samples live in `data/wealthsimple/` (private, D-20);
examples below are invented.

## Input
"Activities export" CSV (UTF-8, comma-separated, header row 1). One file covers **all accounts** of the login,
so the importer splits rows by `account_id` and proposes one target account per id. Description dates are ISO
(`executed at 2025-01-06`). The holdings report (a different header) is refused by this parser; it is a snapshot
source (`import-snapshots.md`).

| Column | Type | Use |
| --- | --- | --- |
| effective_date, effective_time | date, time | fallback date only (see Dates) |
| settlement_date | date, often empty | settle_date |
| account_id | string, e.g. `AB12CD345CAD` | maps to accounts.external_number; trailing currency code = sub-account currency |
| account_name | string | `FHSA`, `TFSA`, ... for self-directed; a portfolio name (`<Name> portfolio`) for robo accounts |
| account_type | `TFSA`, `FHSA`, `RRSP`, `RESP`, `NON_REGISTERED`, ... | account tax type |
| activity_type, activity_sub_type | string | type mapping (below) |
| description | string | contains executed date, FX rate, record date, share counts |
| direction | `LONG` or empty | ignored in v1 (no short positions) |
| symbol | string, may be empty | ticker; format varies (below) |
| name | string | security name, used for resolution |
| currency | `CAD`, `USD`, or empty on corporate actions | currency of `net_cash_amount` |
| quantity | decimal | shares for trades/transfers/corporate actions; **the cash amount** for dividends, fees, tax and money movements |
| unit_price | decimal | per share in `currency` (trades only); already converted when the trade settles in CAD, so quantity x unit_price = net_cash_amount even with an FX Rate |
| commission | decimal | trades only |
| net_cash_amount | decimal | cash effect; for in-kind transfers it is the **market value**, not cash |

## Dates
`effective_date` is often one day before the real event. Use, in order:
1. `executed at YYYY-MM-DD` in `description` (trades, fees, tax, transfers, corporate actions);
2. `received on YYYY-MM-DD` (dividends: pay date); the `record date of` value is kept as record_date;
3. `effective_date`.
Keep `effective_date` and `effective_time` in `raw`. Subscription fees carry a period instead of "executed at" and
fall back to rule 3. In the real exports the description date is the effective date or one day later.

## Mapping

| activity_type | activity_sub_type | Ledger type | Rules |
| --- | --- | --- | --- |
| Trade | BUY / SELL | BUY / SELL | quantity = abs(quantity); price = unit_price in `currency`; fractional shares allowed. If `FX Rate: x` is in description, the security trades in USD and the trade was settled in CAD at rate x: store fx_rate and fx_source `broker` |
| Dividend | - | DIVIDEND | amount = net_cash_amount (gross, before non-resident tax); pay date from "received on", record date from "record date of". The export says "Cash dividend distribution" for every payer, so funds are not told apart here; DISTRIBUTION is derived from the security type later (M6) |
| Tax | NRT | WITHHOLDING_TAX | no symbol; link to a dividend (below) |
| Fee | - | FEE | management fees (robo) and subscription fees; D-21 |
| AdministrativePayment | MANAGEMENT_FEE_REFUND | FEE (negative) | refund; a negative row followed by a positive one on the same day is a reversal pair, both imported |
| MoneyMovement | EFT, E_TRFIN | DEPOSIT (amount > 0) / WITHDRAWAL (< 0) | external flow; E_TRFIN is an e-transfer received |
| MoneyMovement | TRANSFER_TF | TRANSFER_IN / TRANSFER_OUT (cash) | institution transfer; other side untracked -> behaves as contribution / withdrawal (D-23) unless the user links it |
| InternalSecurityTransfer | - | TRANSFER_IN / TRANSFER_OUT (in kind) | quantity = abs(shares); net_cash_amount = market value in `currency`, stored as `gross_amount` (signed) and used as the flow value; `net_amount` = 0 because no cash moves; untracked other side -> D-23 |
| CorporateAction | SUBDIVISION | SPLIT (quantity delta) | description says "Corrected quantity of shares by n": store delta n; cost unchanged |
| LegacyCorporateAction | NAME_CHANGE, a +n / -n pair with two different symbols | SYMBOL_CHANGE | one security, new row in `security_symbols` from that date; quantity and cost carry over |
| LegacyCorporateAction | NAME_CHANGE, same symbol, "Corrected quantity" deltas | SPLIT, one row per delta | e.g. a reverse split shows as +a and -b on one day; the day's legs share a `split` group and their deltas add up |
| LegacyCorporateAction | NAME_CHANGE legs that are neither | error row | `unrecognised_name_change` |
| anything else | | error row | user maps it in preview; mapping saved as an import rule |

Corporate-action rows have no currency; they move no cash and take the sub-account's currency (the `account_id`
suffix). Trades: quantity = abs(quantity), price = unit_price, commission = abs(commission); warnings
`amount_mismatch` and `description_quantity_mismatch` per Validation.

## Linking non-resident tax to dividends
Candidates: dividends in the same account and currency whose pay date is within +/- 1 day of the tax row's date,
except securities known to be listed in Canada (exchange TSX after symbol resolution; an unknown listing stays a
candidate). Most tax rows are in CAD: Wealthsimple pays US dividends in CAD into CAD accounts, so currency alone does
not identify US securities.
1. One candidate -> `related_row`, `link_basis = single`.
2. Several -> the unique candidate whose tax / dividend ratio is within 13-17% (US treaty rate 15%) ->
   `link_basis = ratio`.
3. Otherwise `link_basis = allocated` and `allocations = [{row, amount}]`: the tax split pro rata to the dividends,
   rounded half-up to the cent, the last part taking the remainder so the parts sum exactly to the tax.
4. No candidate -> warning `unlinked_withholding`; the user links it.
In real data about half of the tax rows have more than one candidate, so rule 2 and 3 matter.

## Symbol resolution
Formats seen: plain ticker for both TSX and US listings (`XIC`, `VTI`), `.TO` suffix sometimes (`ABC.TO`), other
TSX suffixes (`ZUAG.F`), and the same company with and without `.TO` across a symbol change.
1. `.TO` -> TSX; `.F`, `.U` and similar class suffixes -> TSX with that class.
2. Plain ticker: if any trade or in-kind transfer of it in the file is in USD or carries an `FX Rate` -> US
   listing; if it is traded in CAD without one -> TSX (Wealthsimple states the rate whenever a US listing settles
   in CAD); otherwise the exchange is left empty and resolved at commit against the security master (both
   candidates, the one whose provider name matches `name`). The security name is the `name` column.
3. Ambiguous or unknown -> warning or error in preview; the user's choice is saved as a `security_aliases` row.

## Account resolution
`account_id` matches a tracked account, compared without the trailing currency code on either side (`AB12CD345`,
`AB12CD345CAD` and `AB12CD345USD` are one account) -> use it. Unknown ids are skipped and listed as "not tracked" (D-22);
the preview offers to create the account instead, proposing: broker Wealthsimple, type from `account_type`,
`management = robo` when `account_name` differs from `account_type` and ends in "portfolio" (confirmed by
`Account Classification = Managed` in the holdings report, `import-snapshots.md`), base currency from the
`account_id` suffix (`proposed_base_currency`). A USD sub-account (rows in `USD` for the same account) is the same
account with USD cash. With a target account chosen at upload, rows of the user's other accounts are skipped.

## Validation
- Trades: |quantity * unit_price + commission| = |net_cash_amount| within 0.01, else warning.
- Description share count ("Bought 5.0000 shares") equals quantity, else warning.
- Running quantity per security must not go negative (FR-4); a negative result usually means missing history
  before the export's first date or an unlinked transfer.

## Fixtures
`testdata/wealthsimple/synthetic_activities/`: `make_inputs.py` holds the invented rows and writes
`input/activities.csv` and the overlapping `input/activities_older.csv`; `input/context.json` tracks a TFSA (with
suffix) and an FHSA (without); `expected/*.json` is the hand-written output. Covers every mapping row, the
executed/received/effective date rules, FX rate on trades, all three tax-linking rules plus a TSX dividend excluded
and an unlinked tax, the USD sub-account, fee refund and reversal pair, in-kind transfers in and out, SUBDIVISION,
both NAME_CHANGE shapes, an untracked robo account, an unmapped activity and two mismatch warnings. Never a copy of a
real export (D-20).

Parser: `wealthsimple-activities`, version 1 (`services/importer/importer/parsers/wealthsimple.py`). Natural key
(D-8): SHA-256 of `wealthsimple|<account id>|<effective_date>|<activity_type>/<activity_sub_type>|<symbol>|
<quantity>|<unit_price>|<net_cash_amount>|<currency>`, decimals without trailing zeros. `effective_date` (a source
value) is used rather than the description date so the key depends only on source fields.
