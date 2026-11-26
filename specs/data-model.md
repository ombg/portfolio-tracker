# Data model

Implements: PRD 4.1, 4.2, FR-2, FR-5, FR-8, NFR-2, NFR-3. Decisions: D-1, D-2, D-6..D-15, D-19, D-20, D-35.
Types: money `NUMERIC(24,8)`, quantities `NUMERIC(24,8)`, ids `uuid`, times `timestamptz` (UTC).
Every table marked (U) has `user_id uuid NOT NULL` with a forced RLS policy `user_id = core.current_user_id()`, where
`core.current_user_id()` is `NULLIF(current_setting('app.user_id', true), '')::uuid`: an unset `app.user_id` matches
no row (fails closed). Request transactions run as the role `pt_app` (no `BYPASSRLS`, no `DELETE` on user tables).
Rows that reference a user-owned parent use a composite foreign key `(parent_id, user_id)`, so a row can never point
at another user's portfolio or account. The importer uses its own role `pt_importer` under the same policies; it
writes only the `imports` schema (D-35). Neither role may DELETE or TRUNCATE anything (NFR-3), and neither may
UPDATE `transactions` or `transaction_corrections` (D-7); a database test guards this for every table.

## core schema (owner: portfolio-api)

| Table | Columns (key ones) | Notes |
| --- | --- | --- |
| users | id, email, password_hash, display_name, reporting_currency, created_at, disabled_at | argon2id |
| sessions | id, user_id, created_at, expires_at, last_seen_at | cookie holds id |
| portfolios (U) | id, user_id, name | one per user in v1 (D-2) |
| brokers | id, code (`questrade`, `wealthsimple`), name | global; more brokers = new rows + parser |
| accounts (U) | id, user_id, portfolio_id, broker_id, external_number, name, type, is_registered, base_currency, status (`open`,`closed`,`archived`), management (`self_directed`,`robo`), analysis_level (`security`,`account`), opened_on, closed_on, notes, migration_meta jsonb | type in TFSA, FHSA, RRSP, RESP, NON_REGISTERED, OTHER; is_registered derived from type except OTHER; robo implies analysis_level account (D-14) |
| currencies | code, name, minor_units | CAD, USD, EUR seeded |
| securities | id, name, security_type (`stock`,`etf`,`fund`,`reit`,`cash_equivalent`,`gic`), trading_currency, asset_class, issuer_country, fund_domicile, market_cap, market_cap_as_of, identifiers jsonb (isin, cusip, figi), gic_issuer, gic_rate, gic_maturity | global; provider-filled; market-cap group derived (D-19); GIC terms only on `gic` rows (D-26); created by portfolio-api at commit (D-35) |
| security_symbols | security_id, symbol, exchange (`TSX`,`TSXV`,`NEO`,`CSE`,`US`), valid_from, valid_to (exclusive) | D-15; periods of one symbol on one exchange never overlap (exclusion constraint) |
| security_aliases (U nullable) | security_id, broker_id, alias, user_id | broker codes; `user_id` NULL = global (owner-maintained, readable by all), else visible to and written by that user only; unique per (user, broker, alias) |
| sector_map | provider, provider_sector, gics_sector | D-9 |
| security_overrides (U) | id, user_id, security_id, field, value, reason, created_at, superseded_at | sector, industry, region, exposure_region, asset_class, name... (FR-5) |
| price_overrides (U) | user_id, security_id, date, close, reason, created_at | FR-5 |
| fx_overrides (U) | user_id, base, quote, date, rate, reason, created_at | FR-5 |
| transactions (U) | id, user_id, account_id, type, trade_date, settle_date, record_date NULL, security_id NULL, quantity, split_ratio NULL, price, commission, gross_amount, net_amount, currency, fx_rate_to_cad NULL, fx_source, withholding_tax, related_id NULL, link_basis NULL, group_id NULL, transfer_status NULL, counterparty_account_id NULL, origin (`import`,`manual`,`opening_balance`), source_file_id NULL, source_row_no NULL, natural_key, raw jsonb, imported_at, import_batch_id NULL | immutable (D-7); source fields required when origin = import |
| transaction_corrections (U) | id, user_id, transaction_id, field, old_value jsonb, new_value jsonb, reason, created_at | latest row per field wins; JSON null clears a field; void = field `voided` (`true`/`false`); also records automatic links of late transfer legs (D-35) |
| allocation_targets (U) | user_id, portfolio_id, dimension (`sector`,`region`,`asset_class`,`currency`), bucket, target_pct | FR-11 |
| broker_snapshots (U) | id, user_id, account_id, as_of, source (`import`,`manual`), source_file_id NULL | D-11, D-22 |
| snapshot_lines (U) | snapshot_id, security_id NULL, currency, quantity NULL, cash NULL, broker_book_value NULL, broker_market_value NULL, value_currency NULL | security NULL = cash line; broker values informational |
| recalc_queue | account_id, from_date, enqueued_at | D-13 |
| quality_issues (U) | id, user_id, kind, scope, ref, details jsonb, first_seen, resolved_at, resolution_note | see data-quality.md |
| daily_positions (U) | user_id, account_id, security_id, date, quantity, avg_cost_native, book_value_native, book_value_rc, price, price_source, price_observed_at, fx_rate, fx_source, market_value_rc, flags | rc = reporting currency CAD; other currencies converted at read time |
| daily_values (U) | user_id, account_id, date, market_value_rc, cash_rc, external_flow_rc, income_rc, fees_rc, flags | input to TWR |

### Transaction types
BUY, SELL, DIVIDEND, DISTRIBUTION, DRIP, DEPOSIT, WITHDRAWAL, TRANSFER_IN, TRANSFER_OUT, FEE, INTEREST,
FX_CONVERSION, SPLIT, SYMBOL_CHANGE, JOURNAL, MERGER, SPIN_OFF, RETURN_OF_CAPITAL, WITHHOLDING_TAX, GRANT,
CORRECTION_ADJUSTMENT. WITHHOLDING_TAX rows may be negative (rebates).
FEE rows may be negative (fee refunds, D-21).

### Linking rules
- **DRIP**: one DIVIDEND (income) and one DRIP (share acquisition) share a `group_id` (PRD 4.3).
- **FX conversion**: two FX_CONVERSION rows (one per currency) share a `group_id`; implied rate = |leg2 / leg1|.
- **Internal transfer**: TRANSFER_OUT and TRANSFER_IN share a `group_id` and set `counterparty_account_id`
  (detected from account numbers in the description, D-27; `transfer_status` = `linked` or `awaiting_counterpart`).
  A transfer from an institution outside the user's accounts has no counterparty and is an external flow.
  In-kind transfers store the broker-reported market value in `gross_amount` when the export provides it.
- **Split / merger / spin-off**: SPLIT carries either a ratio (`split_ratio`, new per old; Questrade style) or a
  quantity delta in `quantity` (Wealthsimple style); exactly one is set. SYMBOL_CHANGE records a ticker change of the
  same security (adds a `security_symbols` row). MERGER and SPIN_OFF link old and new security via `related_id` with
  cost allocation in `raw`.
- **Withholding tax**: separate WITHHOLDING_TAX row linked to its DIVIDEND by `related_id`; when the broker gives no
  symbol, the importer's linking rule applies and an allocated link is marked `link_basis = 'allocated'`.

### Views
- `v_ledger`: transactions with corrections applied, voided rows removed, plus `corrected` (any correction). Defined
  with `security_invoker`, so the caller's RLS applies.
- `v_security_effective`: provider metadata with the user's latest overrides applied, plus a `basis` column per field
  (`provider`, `override`, `default`).
- `v_cash`: cash balance per account per currency = sum of cash effects in `v_ledger`.

## imports schema (owner: importer)

| Table | Columns | Notes |
| --- | --- | --- |
| source_files (U) | id, user_id, sha256, original_name, size, media_type, stored_path, uploaded_at, deleted_at | D-12; unique (user, sha256) while not deleted (D-8) |
| import_batches (U) | id, user_id, source_file_id, broker_id, account_id NULL, parser, parser_version, status (`parsed`,`needs_review`,`committed`,`discarded`,`failed`), counts jsonb, created_at, committed_at | FR-3 history; `counts` = rows per status, or `failure` (reason) for a failed batch; portfolio-api may update only status, committed_at and counts (commit, discard) |
| import_rows (U) | batch_id, row_no, user_id, raw jsonb, normalized jsonb, status (`ok`,`warning`,`error`,`duplicate`,`skipped`,`excluded`), messages jsonb, user_edits jsonb | preview + correction; `skipped` = untracked account (D-22), `excluded` = left out by the user; edited only by the importer |

### Normalized import row
`import_rows.normalized` is the parser's proposal for one ledger row; commit (#18) turns it into
`core.transactions`. All decimals are strings without exponent or trailing zeros; dates are ISO strings.

| Field | Meaning |
| --- | --- |
| account_id | tracked account (uuid) |
| type | ledger type (see Transaction types) |
| trade_date, settle_date, record_date, pay_date | dates; record and pay date for income and splits |
| security | null, or `{symbol, exchange, broker_code, security_id, proposed, name, security_type, trading_currency, gic}`: a listed symbol (`exchange` TSX/US), or a broker code resolved through an alias (`security_id`) or proposed from the file (`proposed = {symbol, exchange, from_row}`); GICs carry `gic = {issuer, rate, maturity}` |
| quantity, split_ratio, price, commission, gross_amount, net_amount, currency | as in `transactions` |
| fx_rate, fx_source | broker-stated rate, if any |
| group | `{kind, key}` linking legs within the file (`fx`, `symbol_change`, `journal`) |
| related_row | row number this row belongs to (withholding tax -> dividend) |
| counterparty_account_id, transfer_status | internal transfer (D-27) |
| shares_on | share count the broker paid income on (D-29) |
| natural_key | duplicate key (D-8), per broker spec |

A `skipped` row for an untracked account holds `{"untracked_account": {number, account_type, proposed_type,
proposed_management}}` instead (D-22). Error rows that could not be mapped at all have `normalized = null`.

## market schema (owner: market-data)

| Table | Columns | Notes |
| --- | --- | --- |
| prices | security_id, observed_at, trade_date, price, kind (`intraday`,`close`), source | intraday kept 7 days, closes forever |
| fx_rates | base, quote, observed_at, rate_date, rate, kind, source | BoC daily + Yahoo intraday |
| refresh_runs | id, started_at, finished_at, status (`success`,`partial`,`failed`), symbols_ok, symbols_failed, errors jsonb, next_run_at | FR-6 status |
| provider_metadata | security_id, source, fetched_at, payload jsonb | sector, industry, country, dividends |
| dividend_events | security_id, ex_date, pay_date, amount, currency, source | estimated income schedule |
