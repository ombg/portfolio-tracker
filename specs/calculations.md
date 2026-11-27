# Calculations

Implements: FR-7, FR-8, FR-9, FR-10, FR-11, NFR-2, NFR-4. Decisions: D-4, D-5, D-6, D-9, D-10, D-17.
All calculations live in `services/api/internal/calc` and run on `v_ledger` in (trade_date, id) order.
Every result carries `basis` metadata (price source + time, FX source + time, flags) so the UI can show it (NFR-2).

## Reporting scopes (PRD 4.1)
`all`, `registered`, `non_registered`, `broker:<id>`, `account:<id>`, `security:<id>`.
A scope is a set of accounts (plus, for security scope, one security). Registered and non-registered subtotals are
always returned alongside `all`.

## Holdings and average cost (per account; FR-7, FR-8)
Running state per (account, security): `qty`, `cost_native`, `cost_rc`.

| Event | qty | cost_native | cost_rc |
| --- | --- | --- | --- |
| BUY, DRIP | + q | + q*price + commission | + (q*price + commission) * fx_trade |
| SELL | - q | - q * avg_native | - q * avg_rc |
| TRANSFER_IN (in kind) | + q | + broker-reported value, else market value at date (carried cost if internal) | same in rc |
| TRANSFER_OUT (in kind) | - q | - q * avg_native | - q * avg_rc |
| SPLIT ratio k | * k | unchanged | unchanged |
| SPLIT delta n (quantity adjustment) | + n | unchanged | unchanged |
| SYMBOL_CHANGE | unchanged | unchanged | unchanged (same security, new symbol) |
| JOURNAL (CAD/USD side move) | unchanged (legs net to zero) | unchanged | unchanged |
| GIC BUY / redemption (D-26) | +/- principal units | at 1.00 per unit | at 1.00 per unit |
| RETURN_OF_CAPITAL | 0 | - amount | - amount * fx |
| MERGER / SPIN_OFF | per event cost allocation | | |

`avg = cost / qty`. Realized gain on SELL = proceeds - commission - q * avg (native and rc).

Native means the position's **book currency**: the currency its acquisitions were paid in (D-38). It is
usually the trading currency, but a US security bought in a CAD account (Wealthsimple settles in CAD) has book
currency CAD. An amount in another currency than the book currency converts with the row's `fx_rate_to_cad`; without
one the native cost becomes unknown (`fx_missing`) from that row on, quantity is still tracked. rc (CAD) cost: CAD
amounts as they are, other currencies times the row's `fx_rate_to_cad`; without one the rc cost is unknown
(`fx_missing`) until M4 supplies the Bank of Canada rate for that date. Unknown is null with the flag, never 0.
An in-kind TRANSFER_IN without a value (no `gross_amount`, not internal) makes the cost unknown (`cost_unknown`)
until M4 supplies the market value on that date (D-23, D-31: then labelled estimated).
SYMBOL_CHANGE legs on two security ids (the new ticker was already a separate security, D-37) move quantity and cost
from the old to the new security within the group. Income received per position = DIVIDEND, DISTRIBUTION and
INTEREST rows of that account and security, net of their WITHHOLDING_TAX rows, in rc.
Internal in-kind transfer carries cost from the source account. If qty would go negative: stop, flag
`negative_holding` for that account/security from that date (FR-4), and exclude its value rather than show it.

Aggregate security view: sum qty and cost across accounts; `avg = sum cost / sum qty`, labelled informational.

## Cash
Per account and currency, the sum of `net_amount` over `v_ledger` (in-kind transfers carry 0). The cash effects are: BUY -(gross+commission), SELL +net, DIVIDEND/INTEREST +net,
WITHHOLDING_TAX -, FEE - (a negative fee, i.e. a refund, adds cash), DEPOSIT +, WITHDRAWAL -, TRANSFER cash legs +/-, FX_CONVERSION legs in their own
currency. When the broker provides `net_amount`, it is authoritative for cash.

## Valuation (FR-6)
`market_value_rc = qty * price * fx(security currency -> rc)`. GICs: price 1.00 (principal, D-26). Price = user override > latest provider price.
FX = user override > Bank of Canada daily (history) / Yahoo intraday (today). Missing price or FX: last known value
carried forward with flag `stale_price` / `stale_fx`; if none ever known, value is NULL and shown as "No price".

## Time-weighted return (FR-9, D-4)
Per day d, for the scope's accounts:
- `V_d` = market value + cash at end of day, in rc.
- `F_d` = external flows on day d (DEPOSIT +, WITHDRAWAL -, GRANT +, transfers crossing the scope boundary +/-).

```
r_d  = V_d / (V_{d-1} + F_d) - 1
TWR  = prod_{d in period} (1 + r_d) - 1
annualised (period > 365 days) = (1 + TWR)^(365 / days) - 1
```

- Days where `V_{d-1} + F_d = 0` are skipped (no capital at work).
- In-kind transfers crossing the scope boundary are flows at the broker-reported value, else at market value.
- FEE rows (including subscription fees and refunds) are not flows; they reduce return (D-21).
- Weekends/holidays: value carried forward; r_d = 0 unless FX moves.
- Periods: YTD, each completed calendar year, since inception (first day with capital).
- Per security: V_d = position market value + income received that day, F_d = buys (+) and sells (-) at trade value;
  only days with qty > 0.
- Any day in the period with a flag -> the result carries `data_quality: degraded` and the list of flags.

## Money-weighted return (FR-9, D-5)
Flows: external flows of the scope (deposits negative from the investor's view) plus `V_end` as final positive flow
(and `-V_start` as first flow for a period that starts after inception). Solve

```
sum_i CF_i / (1 + r)^((d_i - d_0) / 365) = 0
```

Newton-Raphson from r=0.1, bisection fallback on [-0.9999, 10]. Not available if fewer than 2 flows or no root.

## Income (FR-10)
- Paid income: DIVIDEND, DISTRIBUTION, INTEREST rows (incl. GIC interest and securities-lending payments);
  gross = gross_amount (or net + linked withholding), withholding = linked WITHHOLDING_TAX net of rebates, net = cash received. DRIP amount and shares come from the linked DRIP row.
- Groupings: month, quarter, year, account, security, sector, currency; trailing 12 months (gross and net).
- Concentration: share of trailing-12-month income by issuer and by sector; top 10 income holdings.
- Trailing yield = trailing-12-month income per share / current price.
- Forward (indicated) income = qty * latest declared amount * payments per year; labelled "estimate".
- Yield on cost = forward income / book value; labelled "historical personal metric".
- Upcoming schedule: from `market.dividend_events` with future pay dates for held securities; else omitted.

## Diversification (FR-11, D-9, D-10)
- Weights by current market value in rc, filtered by scope.
- Dimensions: GICS sector (+ fund buckets), industry, security, asset class, market-cap group (D-19), broker,
  account type, trading currency, region, issuer country, fund domicile.
- Sector table columns: actual %, value, target %, variance (pp) = actual - target, variance ($) = value -
  target % * total.
- Concentration: largest single security %, top-10 holdings %.
- Accounts with `analysis_level = 'account'` contribute one line (D-14).
- Rows without a classification appear as "Unclassified" with a data-quality flag, never dropped.
