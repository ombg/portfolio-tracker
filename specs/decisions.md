# Decisions and PRD review

Status: proposed 2026-11-19; Oliver's answers folded in on 2026-11-20, 2026-11-21, 2026-11-23, 2026-11-24 and 2026-11-25; D-35 confirmed 2026-11-26; D-36 proposed 2026-11-26.
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
| ETF look-through, ACB tax and benchmarks deferred | Smaller v1; resolves the all-in-one ETF question for now |

## Recommended PRD amendments

These are gaps or ambiguities. Each is resolved by a decision below; Oliver should confirm or override.

1. **Transfers across the registered / non-registered boundary.** PRD 4.3 only defines internal transfers for the
   all-portfolio scope. A TFSA -> non-registered transfer is internal to "all accounts" but external to both
   subtotals and to each account. -> D-4.
2. **Reconciliation needs a broker snapshot.** FR-4 compares against "broker-reported holdings/cash snapshots" but
   no snapshot input is in scope. Add snapshot import (positions CSV) and manual snapshot entry. -> D-11.
3. **30-minute refresh vs. data reality.** Official FX (Bank of Canada) is daily. Refresh should run during market
   hours only; outside hours the status is "Market closed", not "Stale". -> D-3.
4. **Sector view and funds.** An eleven-sector GICS view cannot place QZGR, XEF or KILO. Add non-GICS buckets for
   funds and allow a manual single-sector assignment for sector ETFs (XIT, FIE). -> D-9.
5. **Region by fund domicile is misleading.** XEF (EAFE equities) is Canadian-domiciled and would show as Canada.
   Keep domicile as the PRD default but allow a manual "exposure region" override per fund. -> D-10.
6. **Duplicate rows that are legitimate.** Two identical buys on the same day are real trades, not duplicates.
   Define the duplicate rule precisely. -> D-8.
7. **"Valuations in CAD, USD and EUR".** Read as a reporting-currency switch (default CAD), not three columns
   everywhere. -> D-6.
8. **Per-security TWR "where meaningful".** Define it: only over periods with a non-zero position. -> D-4.
9. **Spreadsheet features missing from the PRD.** Beta, market-cap group, dividend-growth rating, the
   yield x growth matrix, "next payment" and the index benchmark are absent. Benchmark is a phase-2 item.
   Resolved: keep market-cap group, drop the rest. -> D-19.
10. **Correctness acceptance.** Acceptance criteria say "view X" but never "X is correct". Add golden-file
    fixtures with hand-computed results (see `specs/testing.md`).
11. **Security identity over time.** Ticker changes, mergers and spin-offs need a stable internal security id
    with symbol history. -> D-15.
12. **Samples.** Resolved: Wealthsimple activities and holdings, Questrade full-history activities and investment
    summary are in `data/` (private).
13. **Robo accounts at any broker.** FR-12 only names Questwealth, but robo accounts exist at Wealthsimple too, and a
    robo account is also a TFSA/RRSP/etc. Model "managed" as a flag next to the tax type, not as an account type.
    -> D-14.
14. **GICs in history.** GICs are out of scope in PRD 3.2, but short GICs appear in tracked accounts' history; without
    minimal support cash cannot reconcile. -> D-26.

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
- Default for a broad ETF (QZGR, XEF) is Multi-sector fund. A user override can assign one sector to a sector
  ETF (XIT -> Information Technology, FIE -> Financials). Look-through stays deferred (PRD 3.2).

### D-10 Region
Default region = issuer domicile (stocks) or fund domicile (funds), per FR-11. Optional manual `exposure_region`
override per security (e.g. XEF -> International Developed). Reports show which basis each row uses.

### D-11 Reconciliation input
Reconciliation answers: "does what the app computed from my transaction history match what the broker says I
hold?" The broker side is a snapshot per account and as-of date (quantity per security, cash per currency).
It comes from a holdings file downloaded from the broker (D-22) or is typed in from a statement.
Default tolerances: quantity within 0.0001 units, cash within 0.01 of the currency. Configurable per account.

### D-12 File storage
Original uploads are stored unchanged on a Docker volume, content-addressed (`files/ab/abcdef....xlsx`), with
metadata in `source_files`. Deletion only through an explicit retention action (NFR-3).

### D-13 Valuation cache
`daily_positions` and `daily_values` (per account, per security, per day) are materialised. Any import, correction,
override or price backfill marks the account dirty from the earliest affected date; a background job in
`portfolio-api` recomputes forward. Reports read only from these tables plus live quotes for today.

### D-14 Robo (managed) accounts
Applies to any broker's robo account (Questwealth, Wealthsimple managed portfolios). The account keeps its tax
type (TFSA, RRSP, ...) and has `management = 'robo'`, which implies `analysis_level = 'account'`. Its trades are
imported normally so value, cash, fees and performance are correct, but holdings, income and diversification
reports show it as one line ("<account name> portfolio", asset class Managed portfolio, sector Multi-sector fund)
with a visible "account-level only" label (FR-12). Registered/non-registered subtotals use its tax type.

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

### D-18 Backups
Nightly `pg_dump` of the database plus a copy of the `files` volume into `./backups/` on the host (git-ignored),
30 days retained. Restore procedure documented in `specs/architecture.md`.

### D-19 Spreadsheet features
From the old spreadsheet, keep **market-cap group** (Mega > $200B, Large > $10B, Mid > $2B, Small > $0.3B, else
Micro; funds get "Fund") as a diversification dimension. Drop beta, dividend-growth rating, the yield x growth matrix
and "next payment". The spreadsheet's holdings are dummy data: use it only as a feature reference, never import it.
No RBC broker support in v1.

### D-20 Private account data
The real account inventory lives in `data/accounts.md`, and real exports in `data/`; the folder is git-ignored.
Nothing derived from them (account numbers, names, opening dates, balances, holdings) may appear in specs, code,
tests, fixtures or commit messages. Accounts are created in the app through the UI or `ptctl`, and test fixtures are
synthetic files that mimic each broker's format.

### D-21 Fees paid from an account
Every FEE row reduces return, including subscription fees paid from an investment account; refunds of fees
(e.g. management-fee refunds) are negative fees. Not external cash flows. Confirmed by Oliver.

### D-22 Snapshot sources (and untracked accounts in any file)
Broker snapshots for reconciliation come from the brokers' holdings files, specified in `specs/import-snapshots.md`:
Questrade "Investment Summary" XLSX (sheets Balances and Positions) and the Wealthsimple holdings report CSV.
These files, and activity exports too, can contain accounts the tracker does not know. Rows for unknown account
numbers are skipped and listed as "not tracked"; every total is computed from tracked accounts only. The preview
offers to create the account if the user wants to track it after all.

### D-23 Transfers to or from untracked accounts
A transfer whose other side is not a tracked account behaves exactly like a withdrawal (out) or a contribution (in):
an external flow for every scope. Cash transfers use the cash amount; in-kind transfers use the broker-reported
market value, else the market value on the transfer date. Shares that come back in start a new average cost at that
value, so book value can differ from the broker's (the broker may keep the original cost); reconciliation compares
quantities and cash, not book value. If the other side later becomes a tracked account, the user links the two legs
and they turn into an internal transfer.

### D-24 History start vs. account opening date
Each account has `opened_on`. After every import, a data-quality check compares it with the earliest transaction of
the account: a gap of more than 31 days raises `history_gap_start`, and any position or cash that cannot be
explained (e.g. a sale without a prior buy) raises `negative_holding`. The fix is a fuller export or **opening
positions**: security, quantity, cost and date (cash too), entered in the UI or imported from a CSV (format in
`specs/data-quality.md`), stored as TRANSFER_IN rows with `origin = 'opening_balance'`, external flows on that
date (FR-2 migration metadata). Oliver's opening positions are in `data/opening-positions.csv` (private).

### D-25 Questrade format stability
Questrade's activity export keeps one column layout across date ranges and accounts (confirmed with the
full-history export). New action codes may appear; unknown codes become error rows that the user maps once.

### D-26 Minimal GIC support
GICs found in history are imported so cash and value reconcile: one security per GIC (`security_type = 'gic'`,
issuer, rate, maturity from the description), bought and redeemed at 1.00 per unit, valued at principal (no accrued
interest), interest as INTEREST income, asset class Fixed income, sector bucket Cash. No GIC analytics beyond that;
full GIC support stays phase 2 (PRD 3.2).

### D-27 Detecting internal transfers
A deposit, withdrawal or transfer row whose description contains another tracked account's number (digits only,
broker suffixes and dashes ignored) is an internal transfer. The importer pairs it with the opposite row in the other
account (same date +/- 3 days, same amount and currency) under one `group_id`; if the other side is not imported
yet, the row stays "awaiting counterpart" and is linked automatically when it arrives. Internal transfers are not
external flows for scopes containing both accounts (D-4), even when the tax wrapper treats them as a withdrawal and
a contribution (e.g. TFSA to FHSA).

### D-28 Overlapping exports
Several activity files per broker may overlap in time and accounts. They are imported one by one; the duplicate
rule (D-8) drops rows already in the ledger and keeps the rest, so older and newer exports can be combined.

### D-29 Dividend share-count check
Where the broker states the shares a dividend was paid on (Questrade: "ON n SHS"), the importer compares it with the
ledger's quantity on the record date. A mismatch raises `dividend_share_mismatch`, which usually points to missing
history or a missing opening position.

### D-30 Repository and branching
One GitHub repository (monorepo) with one folder per service; branching, versioning and pull-request rules are in
`CONTRIBUTING.md`. `main` holds only tagged releases (`ver-x.y.z`), `dev` is the integration branch, every change
lands on `dev` through a reviewed pull request from a feature branch.

### D-31 Opening positions without a known cost
When the real purchase cost of an opening position is unknown, the user may enter it without cost: the app uses the
market value on the opening date as cost, labels it "estimated cost" everywhere it appears, and the user can replace
it later (a correction, D-7).

### D-32 Access to users and sessions
`core.users` and `core.sessions` hold no portfolio data and are needed before a user id is known (login, session
lookup), so they have no RLS policy. Instead the request role `pt_app` has no privileges on them at all; the API
reaches them only through a few `SECURITY DEFINER` functions with a fixed purpose (verify login, create, touch and
revoke a session, read the signed-in user's profile). User creation and password reset stay in `ptctl`, which
connects as the database owner (D-1). Confirmed by Oliver 2026-11-24.

### D-33 Sessions, passwords and CSRF
Answers the PRD's open "password policy" item for v1. Confirmed by Oliver 2026-11-24.
- Passwords: at least 12 characters (no composition rules), at most 1024 bytes; argon2id, 64 MiB, 1 pass, 4 lanes
  (RFC 9106), stored in PHC format so parameters can be raised later. At most 4 hashes run at once.
- Sessions: absolute lifetime 30 days, ended after 7 days without a request; a password reset (`ptctl user
  set-password`) ends all sessions of that user; disabling a user ends them too. Expired rows are removed when the
  same user logs in again.
- Cookie `pt_session` = session id + HMAC-SHA256 with `SESSION_SECRET` (at least 32 bytes); HttpOnly, Secure (unless
  the transport is plain HTTP on loopback, D-34), SameSite=Lax, Path=/.
- Login failures for an unknown email, a disabled user and a wrong password give the same 401 after the same
  hashing work. Emails and passwords are never logged.
- CSRF: every POST/PUT/PATCH/DELETE must be `application/json` (415 otherwise); browsers cannot send that cross-site
  without a CORS preflight, which the API never grants. Together with SameSite=Lax this replaces CSRF tokens.
- Not in v1: login rate limiting and lockout (the app runs on a private network; revisit before exposing it).

### D-34 Secure cookie flag follows the transport
Amends D-33's cookie flags. Fix approved by Oliver 2026-11-25. Found while testing M1: Safari drops `Secure` cookies on
`http://localhost` and `http://127.0.0.1`, so sign-in silently failed there; Chromium and Firefox keep them.
- Compose serves the app over plain HTTP on 127.0.0.1 and sets `COOKIE_SECURE=false` by default. On loopback the flag
  protects nothing: the cookie never leaves the machine. HttpOnly, SameSite=Lax and the HMAC signature are unchanged.
- `COOKIE_SECURE=true` is required once the app is served over HTTPS, which in turn is required before it is
  reachable from another machine. The API binary on its own still defaults to `true`.
- The sign-in page checks that the session works right after login and says so when the browser did not keep the
  cookie, instead of showing the sign-in page again without a message (NFR-4).
- `make e2e` runs in Chromium, Firefox and WebKit (Safari's engine).

### D-35 Database roles and write paths for the ledger and imports
Confirmed by Oliver 2026-11-26 (M2, issue #13).
- **Importer role.** The importer parses untrusted files, so it never gets the owner's credentials. It connects as
  its own login role `pt_importer` (no BYPASSRLS) and sets `app.user_id` per transaction from the user id that
  portfolio-api passes with each internal call, so the same RLS policies apply. The migration creates the role
  without login; portfolio-api enables login with `IMPORTER_DB_PASSWORD` when the importer service is added (#14).
- **Who writes what.** pt_importer: SELECT on the reference tables, accounts, securities, symbols, aliases and
  transactions (to resolve rows and pre-check duplicates); SELECT, INSERT, UPDATE on `imports.*`. pt_app: inserts
  transactions, corrections, securities, symbols and its own aliases; reads `imports.*`; updates only a batch's
  status, committed_at and counts (commit, discard). Row corrections in the preview go through the importer, which
  re-validates the row. Neither role may DELETE anything or UPDATE the ledger.
- **Security master is shared.** Securities and symbols are global facts, so any user's import may add them, and a
  SYMBOL_CHANGE may close a symbol period (`valid_to`), visible to every user. Accepted for a private, invited-users
  install; per-user aliases cover broker codes. Global aliases are maintained by the owner only.
- **Late transfer legs.** When the other leg of an internal transfer arrives (D-27), the link (`group_id`,
  `counterparty_account_id`, `transfer_status`) is written as corrections with reason "linked automatically
  (D-27)", never as an UPDATE of the stored row (D-7).
- **Duplicates count stored rows.** The row duplicate rule (D-8) counts `core.transactions`, not `v_ledger`, so
  re-importing a file does not bring back rows the user voided.

### D-36 Preview corrections: rules, exclusions and account links
Proposed 2026-11-26 (M2, issue #17); awaiting Oliver's confirmation (Q-19).
- **Fixes are rules, not row patches.** What a user fixes in the preview is almost always a fact about the broker's
  file that recurs: an action the parser has no mapping for, or a symbol or broker code it cannot place. Both are
  saved per user and broker in `imports.import_rules` and the batch is parsed again from the stored original, so
  every row with the same action or code changes at once, and later files need no fix. An action rule
  (`"Activity Type|Action"` for Questrade, `"activity_type|activity_sub_type"` for Wealthsimple -> ledger type) is
  used only where the parser has no mapping of its own; a symbol rule (file symbol or code -> symbol + exchange)
  wins over an alias, never applies to GIC codes (D-26), and is shown on the row (`action_rule`, `symbol_rule`).
  Rules can be changed, not deleted (D-35: nobody deletes); they never change committed rows (FR-3 item 8).
- **Aliases follow at commit.** A broker code's rule names a listing, not a security id; the security may not exist
  before the first commit. Commit (#18) resolves the listing against the security master and then writes the
  user's `security_aliases` row, so `security_aliases` stays the commit-time source of truth.
- **Exclusion** is the only per-row edit (`import_rows.user_edits = {"excluded": true}`); an excluded row keeps its
  messages, does not block commit and can be taken back in. Skipped rows (untracked or other accounts) cannot be
  excluded.
- **Untracked accounts.** The preview lists each untracked account number with the importer's proposal. The user
  either creates the account (accounts API, number filled in) and asks for a re-parse, or links the number to an
  existing account that has none (`PATCH` with `account_id`; an account with another number is refused, not
  overwritten). The number is stored on the account, so every later file finds it.
- **Write paths (D-35).** Rules and row edits are written by the importer, which re-validates; portfolio-api only
  checks the session and the batch, sets the account number and discards batches.
- **Upload CSRF.** Uploads are `multipart/form-data`, which a cross-site form can send. `POST /api/imports` must
  carry `X-Requested-With: portfolio-tracker`: forms cannot set headers and a cross-site fetch with one needs a CORS
  preflight the API never grants. Every other write still has to be `application/json` (D-33).

## Open questions

- **Q-19** Confirm D-36 (preview corrections as rules; aliases written at commit; exclusion as the only row edit).

Closed: Q-15 (GitHub push access) and Q-17 (D-33 confirmation), 2026-11-24; Q-18 (D-35
confirmation), 2026-11-26.
