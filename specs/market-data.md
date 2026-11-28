# Market data

Implements: FR-5 (provider metadata), FR-6, FR-10 (dividend events). Decisions: D-3, D-6, D-9, D-10.
Service: `services/market-data`.

## Providers
Interface `Provider` with `quotes(symbols)`, `history(symbol, start, end)`, `metadata(symbol)`, `dividends(symbol)`,
`fx(base, quote, start, end)`. Each returned observation includes `source` and `observed_at`.

| Provider | Used for | Notes |
| --- | --- | --- |
| Yahoo Finance chart API (`httpx`, `marketdata/providers/yahoo.py`) | latest quotes, daily closes, dividend and split history, intraday FX | free, unofficial, rate-limited; one request per symbol, a failure is recorded for that symbol and the run goes on; retry with backoff on 429/5xx; sector/industry metadata (quoteSummary) follows with M7 |
| Bank of Canada Valet API | official daily USD/CAD and EUR/CAD history | published once per business day; used for transaction-date and historical FX |

Symbol format for Yahoo: TSX `QZGR.TO`, TSX units `AP-UN.TO`, US as is.

Implementation notes (#26): Yahoo is called directly (`/v8/finance/chart/<symbol>`) rather than through `yfinance`,
so tests replay recorded responses byte for byte, values stay decimals parsed from the JSON text (`yfinance` returns
pandas floats) and pandas is not needed. Yahoo's numbers are binary floats (27.510000228881836): prices are rounded
to 4 places, FX rates to 6. Yahoo's daily closes and dividends are split-adjusted; the provider undoes every later
split so a close matches the shares held that day, as the ledger records them. Bank of Canada rates are observed
at 16:30 Eastern on their day. Trading days come from the TSX and NYSE holiday rules in `marketdata/calendar.py`
(weekend observance and announced one-off closures included; early closes count as full days), not a library.
`make market-probe SYMBOLS="..."` checks the live providers by hand.

## Schedule (D-3)
- Every 30 minutes, 09:30-16:30 America/Toronto, on TSX/NYSE trading days (holiday calendar via
  `pandas_market_calendars` or `exchange_calendars`).
- Close snapshot at 17:15 America/Toronto: daily close per security + Bank of Canada rates for the day.
- Backfill on demand from portfolio-api for new securities: daily history from the first transaction date.
- Universe: distinct securities with a non-zero position in any account, plus those in pending backfills.

## Refresh status (FR-6)
Each run writes `market.refresh_runs`. API exposes `GET /api/market/status`:
`{last_success_at, last_run_status, next_run_at, market_open, stale_symbols[]}`.

| Status | Meaning |
| --- | --- |
| Success | every symbol refreshed |
| Partial | at least one failed; failed symbols listed |
| Failed | none refreshed |
| Stale (per valuation) | price older than 2 intervals in market hours, or older than previous trading day's close |
| Market closed | outside schedule; last close shown with its date |

## Metadata
Provider sector strings are mapped to GICS through `core.sector_map` (D-9). Market cap (stocks only) is refreshed
weekly into `securities.market_cap`; the group thresholds are in D-19. Unknown strings -> "Unclassified" plus a
data-quality issue. Fund domicile and asset class come from the provider when available, else default by security
type and must be confirmed by the user (data-quality issue `missing_metadata`).

## Overrides
Price and FX overrides are stored in `core` (portfolio-api), never in `market`, so provider data stays intact.
- A price override is (security, date, price, reason) for one user. It is that day's close for that user; the
  current valuation uses it while it is at least as recent as the newest provider price (D-39).
- An FX override is (base, quote, date, rate, reason), used the same way for that date and for current values.
- Removing an override (it is superseded, not deleted, NFR-3) brings the provider value back. Every valuation names
  its basis: `provider` with source and time, or `override` with the reason.

## Service and access (D-39)
- The only service with internet access. It sends **ticker symbols and currency pairs only** (Yahoo symbol, e.g.
  `QZGR.TO`; BoC series `FXUSDCAD`); never accounts, quantities, amounts, user ids or names.
- It connects as its own role `pt_market`: writes the `market` schema, reads the security master, and learns the
  universe through one `SECURITY DEFINER` function `market.universe()` -> (security_id, yahoo_symbol,
  trading_currency, first_trade_date, held). No user ids or quantities cross that boundary (D-1, D-32 pattern).
- `MARKET_DATA_PROVIDER=yahoo` (default), `fixture` (recorded responses from a folder, for tests and e2e) or `off`
  (no network; everything shows "No price"). The clock is injectable (`MARKET_DATA_NOW`) for tests.
- Internal HTTP on the Docker network with the service token: `POST /internal/backfill {security_ids}`,
  `POST /internal/refresh` (run now), `GET /internal/status`.

## Yahoo symbols
`<symbol>` -> `<symbol with . replaced by ->` plus `.TO` for TSX (`AP.UN` -> `AP-UN.TO`), `.V` for TSXV, `.NE` for
NEO, `.CN` for CSE; US as is with `.` -> `-` (`BRK.B` -> `BRK-B`). GICs are not requested: price 1.00 (D-26).
A symbol Yahoo does not know is a failed symbol in the run and `missing_price` for valuations, until the user enters
a price override.

## What M4 fills in (D-38, D-39)
Computed when the holdings are read (the daily cache arrives with M5, D-13):
- Market value = quantity x latest price (override or provider) x FX to CAD (Yahoo intraday during market hours,
  else the latest Bank of Canada rate, or an override), with `basis` {price source, price time, FX source, FX time}.
  Unrealized gain = market value - book value (CAD); weight = market value / total of priced positions in scope.
- Stale (D-3): the price is older than 2 intervals in market hours or older than the previous trading day's close.
  Stale values are shown with their timestamp in amber and still counted, with a note; "No price" stays excluded.
- `fx_missing`: a USD ledger row without a broker rate takes the Bank of Canada rate for its trade date (previous
  business day when none), basis `boc` (D-6). The flag then disappears; the rate shows on the row.
- `cost_unknown`: an in-kind transfer in without a value is valued at that day's close x quantity, labelled
  "estimated" (D-23, D-31).
