# Decisions and PRD review

Status: proposed 2026-11-19, pending Oliver's review.
Source of requirements: `chatgpt/docs/portfolio-tracker-requirements.md` (the PRD).
This file records (a) where the PRD needs clarification or a change and (b) every design decision taken to satisfy it.
Each decision has an ID; specs reference them as `D-n`. Superseded decisions stay here, marked as such.

## Verdict on the PRD

The PRD is sound and implementable. It fixes the right priorities: ledger as system of record, auditability,
"never show a fake zero", and imports without broker credentials. It changes the earlier design in five big ways:

| Change vs. first design | Impact |
| --- | --- |
| Multi-user with authentication and isolation | `user_id` on every owned row, login, sessions |
| Time-weighted return is the primary metric | Needs a daily valuation series back to the first transaction |
| 30-minute price/FX refresh with visible status | Scheduler, refresh-run log, staleness rules |
| Import preview, correction, reconciliation | Staging tables, overlay corrections, broker snapshots |
| ETF look-through, ACB tax and benchmarks deferred | Smaller v1; resolves the XGRO question for now |

## Recommended PRD amendments

These are gaps or ambiguities. Each is resolved by a decision below; Oliver should confirm or override.

1. **Transfers across the registered / non-registered boundary.** PRD 4.3 only defines internal transfers for the
   all-portfolio scope. A TFSA -> non-registered transfer is internal to "all accounts" but external to both
   subtotals and to each account. -> D-4.
2. **Reconciliation needs a broker snapshot.** FR-4 compares against "broker-reported holdings/cash snapshots" but
   no snapshot input is in scope. Add snapshot import (positions CSV) and manual snapshot entry. -> D-11.
3. **30-minute refresh vs. data reality.** Official FX (Bank of Canada) is daily. Refresh should run during market
   hours only; outside hours the status is "Market closed", not "Stale". -> D-3.
4. **Sector view and funds.** An eleven-sector GICS view cannot place XGRO, XEF or KILO. Add non-GICS buckets for
   funds and allow a manual single-sector assignment for sector ETFs (XIT, FIE). -> D-9.
5. **Region by fund domicile is misleading.** XEF (EAFE equities) is Canadian-domiciled and would show as Canada.
   Keep domicile as the PRD default but allow a manual "exposure region" override per fund. -> D-10.
6. **Duplicate rows that are legitimate.** Two identical buys on the same day are real trades, not duplicates.
   Define the duplicate rule precisely. -> D-8.
7. **"Valuations in CAD, USD and EUR".** Read as a reporting-currency switch (default CAD), not three columns
   everywhere. -> D-6.
8. **Per-security TWR "where meaningful".** Define it: only over periods with a non-zero position. -> D-4.
9. **Spreadsheet features missing from the PRD.** Beta, market-cap group, dividend-growth rating, the
   yield x growth matrix, "next payment" and the index benchmark are absent. Benchmark is a phase-2 item;
   confirm the others are intentionally dropped. -> Open question Q-1.
10. **Correctness acceptance.** Acceptance criteria say "view X" but never "X is correct". Add golden-file
    fixtures with hand-computed results (see `specs/testing.md`).
11. **Security identity over time.** Ticker changes, mergers and spin-offs need a stable internal security id
    with symbol history. -> D-15.
12. **Samples.** No Wealthsimple export exists in `data/` yet; the Wealthsimple mapping spec is blocked on it.

## Decisions

### D-1 Users and authentication
- Local accounts stored in Postgres; passwords hashed with argon2id; HTTP-only, SameSite=Lax session cookie.
- No self-signup in v1. Users are created with an admin CLI (`ptctl user create`); password reset via the same CLI.
  This answers the PRD's account-recovery open item for v1.
- Every user-owned table has `user_id NOT NULL`. The API sets `app.user_id` per transaction and Postgres
  row-level security policies enforce isolation as a second line of defence.
- Only `portfolio-api` talks to the browser for authenticated data; other services are internal (D-16).

### D-2 Portfolios
One portfolio per user in the v1 UI. The `portfolios` table exists so more can be added without migration.

### D-3 Market data and refresh
- Provider interface with two implementations at start: Yahoo Finance via `yfinance` (quotes, daily history,
  metadata, dividends) and Bank of Canada Valet API (official daily USD/CAD, EUR/CAD).
- Intraday FX for the current valuation comes from Yahoo (`USDCAD=X`, `EURCAD=X`); historical and transaction-date
  FX use Bank of Canada. Every observation stores its source and timestamp.
- Schedule: every 30 minutes 09:30-16:30 America/Toronto on exchange trading days, plus a close snapshot at 17:15.
  Outside that window no refresh runs and status shows "Market closed - last close <date>".
- Status per run: Success (all symbols), Partial (some failed), Failed (none). A valuation is Stale when its price
  is older than 2 refresh intervals during market hours, or older than the previous trading day's close otherwise.
- Missing price: last known price is carried forward and flagged; never zero (NFR-4).

### D-4 Time-weighted return
- Daily chain-linked TWR in the reporting currency. External flows are assumed at the start of the day:
  `r_d = V_d / (V_{d-1} + F_d) - 1`, `TWR = prod(1 + r_d) - 1`.
- A flow is external relative to the scope being measured. Transfers between two accounts inside the scope net
  to zero; transfers that cross the scope boundary are external flows of that scope. This applies to all-accounts,
  registered, non-registered, broker and account scopes alike.
- In-kind transfers are flows at market value on the transfer date.
- Commissions, account fees and withholding tax are not flows; they reduce return.
- Periods: YTD, each completed calendar year, since inception (annualised when longer than one year, and labelled).
- Per-security TWR: same formula on the position, with buys/sells as flows and income as return, computed only over
  days with a non-zero position.
- Days without a valid price or FX rate are flagged; a period containing a flagged day shows a data-quality badge.

### D-5 Money-weighted return (XIRR)
External flows of the scope plus terminal value; Newton-Raphson with bisection fallback. If fewer than two flows or
no convergence, show "Not available" with the reason, never 0%.

### D-6 Currencies
- Currencies are rows (CAD, USD, EUR seeded). Every money value is stored in its native currency with ISO code.
- Reporting currency is a per-user setting (default CAD), switchable to USD or EUR in the UI.
- Transaction-date FX: the broker's implied rate when the export provides both legs, else Bank of Canada for that
  date (previous business day if missing), else flagged.

### D-7 Corrections and immutability
Imported ledger rows are immutable. User corrections are stored in `transaction_corrections` (field, old value,
new value, user, time, reason). The effective ledger is the view `v_ledger`. Voiding a row is a correction.

### D-8 Duplicate detection
- File level: SHA-256 of the uploaded bytes; an identical file is rejected with a link to its earlier import.
- Row level: natural key = hash(broker, account, trade date, action, symbol, quantity, price, net amount, currency).
  If the incoming file has k rows with the same natural key and the ledger already has m, insert max(0, k - m)
  and report the rest as duplicates. This keeps legitimate identical trades and makes overlapping re-imports safe.

### D-9 Sector taxonomy
- Canonical: the 11 GICS sectors. A mapping table translates provider names
  (Yahoo "Technology" -> Information Technology, "Financial Services" -> Financials, "Consumer Cyclical" ->
  Consumer Discretionary, "Consumer Defensive" -> Consumer Staples, "Healthcare" -> Health Care,
  "Basic Materials" -> Materials).
- Extra buckets outside GICS for funds and cash: Multi-sector fund, Fixed income, Commodity, Cash.
- Default for a broad ETF (XGRO, XEF) is Multi-sector fund. A user override can assign one sector to a sector
  ETF (XIT -> Information Technology, FIE -> Financials). Look-through stays deferred (PRD 3.2).

### D-10 Region
Default region = issuer domicile (stocks) or fund domicile (funds), per FR-11. Optional manual `exposure_region`
override per security (e.g. XEF -> International Developed). Reports show which basis each row uses.

### D-11 Reconciliation input
Broker snapshots are imported from a positions/balances export or entered by hand, per account and as-of date.
Default tolerances: quantity within 0.0001 units, cash within 0.01 of the currency. Configurable per account.

### D-12 File storage
Original uploads are stored unchanged on a Docker volume, content-addressed (`files/ab/abcdef....xlsx`), with
metadata in `source_files`. Deletion only through an explicit retention action (NFR-3).

### D-13 Valuation cache
`daily_positions` and `daily_values` (per account, per security, per day) are materialised. Any import, correction,
override or price backfill marks the account dirty from the earliest affected date; a background job in
`portfolio-api` recomputes forward. Reports read only from these tables plus live quotes for today.

### D-14 Questwealth (robo) account
Account has `analysis_level = 'account'`. Its trades are imported normally so value and cash are correct, but
reports show it as one line ("Questwealth portfolio", asset class Managed portfolio, sector Multi-sector fund)
with a visible "account-level only" label (FR-12).

### D-15 Security identity
Securities have an internal id. `security_symbols` keeps (symbol, exchange, valid_from, valid_to);
`security_aliases` maps broker codes (Questrade `H007816` -> IYW). Mergers and spin-offs are ledger events linking
old and new security ids.

### D-16 Architecture
Four application services plus Postgres, Docker Compose, unchanged in spirit from the first design but with
auth centralised: the browser talks only to `portfolio-api` (through Caddy); the API proxies uploads to the
`importer` on the internal network. See `specs/architecture.md`.

### D-17 Cost basis
Average cost per account (FR-8), tracked in native currency and in reporting currency at transaction-date FX.
The aggregate view shows a weighted informational average. No tax ACB pool in v1; schema keeps full history.

## Open questions

- Q-1 Drop beta, market-cap group, dividend-growth rating, yield x growth matrix and "next payment" from v1?
- Q-2 Which accounts exist at Wealthsimple and Questrade, and from what year does history start?
- Q-3 Wealthsimple export samples (activity CSV and holdings CSV) are needed for `specs/import-wealthsimple.md`.
- Q-4 Is Questrade's "positions" export available to drive reconciliation, or will snapshots be typed in?
- Q-5 Backup policy: proposed nightly `pg_dump` + file-volume copy to a folder you choose, 30 days retained.
- Q-6 The old spreadsheet's RBC account: in scope as a third broker, or history only?
