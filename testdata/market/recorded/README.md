# market/recorded

Recorded provider responses (synthetic symbols and values, D-20) in the providers' real JSON shape, served by
`marketdata.providers.http.RecordedTransport` for the market-data tests and for `MARKET_DATA_PROVIDER=fixture`
(e2e, D-39). Layout: `<host>/<url path>.json`; the query string is ignored. A missing file answers 404 with Yahoo's
"not found" body; `<name>.status` replays that HTTP status instead (e.g. 503 for a provider outage).

- `QZGR.TO`: CAD ETF, five January 2025 sessions (no trade on the 10th), a 2:1 split on the 9th and a dividend on the
  7th, so Yahoo's split-adjusted history must be undone. Latest price 27.51 at the 2025-11-27 close.
- `ZZUS`: USD stock, the same sessions; latest 161.42.
- `USDCAD=X`: intraday FX quote 1.3936.
- `QZFL.TO`: answers 503 (retried, then reported as failed).
- Bank of Canada `FXUSDCAD`: 6-10 January 2025 with the 9th missing (previous business day rule, D-6).
