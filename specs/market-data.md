# Market data

Implements: FR-5 (provider metadata), FR-6, FR-10 (dividend events). Decisions: D-3, D-6, D-9, D-10.
Service: `services/market-data`.

## Providers
Interface `Provider` with `quotes(symbols)`, `history(symbol, start, end)`, `metadata(symbol)`, `dividends(symbol)`,
`fx(base, quote, start, end)`. Each returned observation includes `source` and `observed_at`.

| Provider | Used for | Notes |
| --- | --- | --- |
| Yahoo Finance (`yfinance`) | intraday quotes, daily closes, sector/industry/country, dividend history, intraday FX | free, unofficial, rate-limited; batch symbols; retry with backoff |
| Bank of Canada Valet API | official daily USD/CAD and EUR/CAD history | published once per business day; used for transaction-date and historical FX |

Symbol format for Yahoo: TSX `XGRO.TO`, TSX units `AP-UN.TO`, US as is.

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
Provider sector strings are mapped to GICS through `core.sector_map` (D-9). Unknown strings -> "Unclassified" plus a
data-quality issue. Fund domicile and asset class come from the provider when available, else default by security
type and must be confirmed by the user (data-quality issue `missing_metadata`).

## Overrides
Price and FX overrides are stored in `core` (portfolio-api), never in `market`, so provider data stays intact.
