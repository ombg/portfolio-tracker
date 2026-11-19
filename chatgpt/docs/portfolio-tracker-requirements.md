# Portfolio Tracker — Product Requirements Document

## 1. Purpose

Build a self-hosted, private portfolio tracker for investors with accounts at multiple Canadian brokers. The product is the authoritative record of imported investment activity and provides dependable insight into holdings, performance, dividend income, and diversification.

The initial audience is an individual Canadian investor with Wealthsimple and Questrade accounts. The product must support separately authenticated users and keep every user's data isolated.

This document states product requirements and calculation policies. It intentionally does not select an application architecture, database, framework, hosting environment, interface design, or market-data vendor.

## 2. Product goals

The product shall enable a user to:

1. See total investments across brokers while preserving account-level detail.
2. Measure investment performance with a clear, trustworthy annual rate of return.
3. Understand each security's performance and exposure across accounts.
4. Track paid and estimated dividend income.
5. Assess diversification by sector, region, asset class, account type, broker, currency, and concentration.
6. Import and audit full transaction history from broker exports without granting the product broker-login access.
7. Add accounts and future broker CSV formats without reworking historical records.

## 3. Scope

### 3.1 In scope for version 1

- Publicly traded securities, ETFs/funds, and cash.
- Wealthsimple CSV imports.
- Questrade CSV and spreadsheet-export imports.
- Multiple accounts per user and multiple brokers per user.
- Account types: TFSA, FHSA, RRSP, RESP, non-registered, managed/robo, and Other.
- CAD, USD, and EUR support.
- Full historical transaction import.
- Authentication and strict user data isolation.
- Current and historical holdings, valuation, performance, dividend, and diversification reporting.
- Manual overrides for security metadata, prices, and FX rates.

### 3.2 Explicitly out of scope for version 1

- Automatic broker login, account synchronization, or trading.
- Formal Canadian tax reporting or tax filing.
- Full non-registered ACB/tax calculations.
- ETF look-through holdings/exposure analysis.
- Underlying-security analysis for the Questwealth robo account.
- Options, crypto, GICs, bonds, real estate, private assets, and collectibles.
- Shared household portfolios, adviser access, and user-to-user sharing.
- Retirement-gap modelling.
- Investment recommendations or financial advice.

## 4. Core concepts and data policy

### 4.1 Portfolio hierarchy

A user owns one or more portfolios. A portfolio contains broker accounts. An account belongs to exactly one user and has a broker, account type, base currency, status, and name.

The tracker shall support these reporting scopes:

- All accounts combined.
- Registered accounts combined.
- Non-registered accounts combined.
- A selected broker.
- A selected account.
- A selected security.

Registered and non-registered accounts must remain visibly separate in all high-level reporting, even when an all-accounts aggregate is shown.

### 4.2 Transaction ledger

The transaction ledger is the system of record. Derived holdings, returns, income, and reports must be reproducible from imported transactions, market data, and documented overrides.

The ledger shall support at least:

- BUY
- SELL
- DIVIDEND or DISTRIBUTION
- DRIP
- DEPOSIT
- WITHDRAWAL
- INTERNAL TRANSFER
- FEE
- INTEREST
- FX CONVERSION
- SPLIT
- MERGER, SPIN-OFF, RETURN OF CAPITAL, and correction events

Each transaction must retain source broker, account, dates, security where applicable, quantity, price, commission, gross and net amount, currency, FX information where available, source-file identifier, source-row identifier, import time, and any user correction history.

### 4.3 Cash-flow classification

- Deposits and withdrawals external to the user's portfolio are external cash flows.
- Transfers between the user's own accounts are internal portfolio transfers, not external cash flows for all-portfolio performance.
- Dividends and distributions are investment income, not deposits.
- A dividend reinvestment must preserve both the income event and the related acquisition of shares.

## 5. Functional requirements

### FR-1: User authentication and isolation

The system shall require authentication before a user can access any portfolio data.

- Users shall only see, edit, import, and export their own data.
- Data from one user must not be visible to another user through normal product workflows.
- The product shall be capable of supporting more than one user, even though shared portfolios are deferred.

### FR-2: Account management

The system shall allow a user to create, edit, close, and archive accounts.

For each account, the user shall be able to record:

- account name;
- broker;
- account type;
- base currency;
- open/closed status;
- optional notes;
- optional opening-balance or migration metadata.

Adding a new account must not require a new import format or changes to existing account records.

### FR-3: Import workflow

The system shall import Wealthsimple CSV and Questrade CSV/spreadsheet exports.

The import workflow shall:

1. Preserve the original uploaded file without changing it.
2. Identify broker and target account.
3. Parse and map source rows to the normalized transaction ledger.
4. Present a preview of mapped rows, warnings, and errors before committing data.
5. Allow a user to correct unresolved mappings before import.
6. Detect duplicate source files and duplicate transaction rows.
7. Maintain an import history with status, user, time, source file, affected account, and results.
8. Allow additional broker CSV mappings to be introduced later without modifying historical data.

The product shall not require broker credentials or automated broker access.

### FR-4: Reconciliation and data quality

The product shall provide a reconciliation workflow between imported activity and broker-reported holdings/cash snapshots.

The workflow shall identify, at minimum:

- unmatched or unrecognized security symbols;
- duplicate or conflicting transactions;
- negative holdings caused by incomplete or incorrect history;
- cash-balance differences;
- holding-quantity differences;
- missing price, FX, sector, region, or currency metadata;
- stale or failed price refreshes.

No unresolved condition may be presented as a healthy zero or successful reconciliation.

### FR-5: Security master and overrides

The system shall maintain security metadata including:

- ticker and exchange;
- display name;
- security type;
- trading currency;
- asset class;
- sector and industry;
- issuer country;
- fund domicile where applicable;
- region;
- identifiers when available.

Users shall be able to override price, FX rate, ticker mapping, sector, industry, region, and other security metadata. Overrides must be attributable, dated, and distinguishable from imported or provider-supplied data.

### FR-6: Valuation and market data

The product shall maintain current and historical valuations in CAD, USD, and EUR where applicable.

- Default portfolio reporting currency is CAD.
- Every transaction retains its original currency.
- The product shall use transaction-date FX rates where available for historical records and clearly label estimates or overrides.
- Market prices and FX rates shall refresh every 30 minutes.
- Every displayed valuation must show or make available the price timestamp, price source, and FX timestamp/source used.
- The product shall display: last successful refresh time, next scheduled refresh time, and refresh status.
- Refresh status must distinguish Success, Partial, Failed, and Stale.
- A stale or unavailable valuation must be visible; the product must not silently substitute a zero.

### FR-7: Holdings and security reporting

The system shall calculate holdings from the transaction ledger and display them by account and in aggregate.

For each security, the system shall show as applicable:

- quantity and available cash;
- average cost and book value;
- current price and market value;
- realized gain/loss;
- unrealized gain/loss;
- total return;
- income received;
- portfolio weight;
- currency and FX effect where available;
- account-level detail and aggregate detail across accounts.

The aggregate security view must not obscure the account-level holdings, cost, or tax status.

### FR-8: Cost basis policy

Version 1 shall use average cost for performance reporting.

- Average cost is calculated separately within each account.
- The aggregate view may show a weighted informational average across accounts.
- The account-level calculation is the authoritative performance basis for realized/unrealized performance reporting.
- FIFO and lot-selection reporting are deferred.
- Full transaction history must be retained so alternate calculations can be added later.

For a future Canadian tax module, the product must not assume that account-level performance cost is a tax ACB calculation. The future module must be able to form a separate non-registered tax pool by beneficial owner and identical security across applicable taxable accounts.

### FR-9: Performance reporting

The primary performance measure shall be CAD total return, time-weighted, net of actual trading commissions.

The product shall report time-weighted total return for:

- calendar year to date;
- every completed calendar year;
- since inception;
- all accounts combined;
- registered and non-registered subtotals;
- each account;
- each security where the measure is meaningful.

The product shall also report money-weighted return (XIRR) as a secondary measure for the same relevant scopes.

Definitions:

- Time-weighted return measures investment performance while adjusting for user-driven external cash flows.
- Money-weighted return reflects the timing and size of the user's external cash flows.
- Total return includes realized and unrealized price changes and investment income.
- Trading commissions reduce return.
- Internal transfers must not affect all-portfolio return as external cash flows.

The dashboard shall default to an all-accounts view and show separate registered and non-registered summaries alongside it.

### FR-10: Dividend and distribution reporting

The system shall track actual paid income independently from estimated future income.

For actual dividends/distributions, record and report:

- gross amount;
- withholding tax;
- net cash received;
- DRIP amount and shares;
- payment date;
- currency;
- account, security, sector, and region;
- special distributions and return of capital when identifiable.

The product shall report:

- gross and net income by month, quarter, year, account, security, sector, and currency;
- trailing-12-month gross and net income;
- top income-producing holdings;
- income concentration by issuer and sector;
- paid-income trends;
- estimated upcoming payment schedule when the required data is available;
- trailing dividend/distribution yield;
- forward/indicated income and yield, clearly labelled as estimates;
- yield on cost, clearly labelled as a historical personal metric.

### FR-11: Diversification reporting

The system shall show diversification by current market value and provide filtering by reporting scope.

Required views:

- eleven-sector GICS-style sector classification;
- industry;
- security;
- asset class;
- broker;
- account type;
- trading currency;
- issuer/fund region;
- issuer country and fund domicile where applicable.

The sector view shall contain a pie/donut chart and a table showing actual allocation, dollar value, target allocation, percentage-point variance, and dollar variance.

The product shall also report single-security concentration and top-ten-holdings concentration.

Initial regional exposure is based on issuer domicile for individual securities and fund domicile for ETFs/funds. ETF look-through exposure is deferred.

### FR-12: Questwealth robo account

Version 1 shall support a Questwealth robo account as an account-level reporting unit.

- Its value, activity, cash flows, and account-level performance shall be reportable.
- Underlying ETF/security-level analysis is not required in version 1.
- The UI must make the aggregation level clear.

## 6. Reporting requirements

The product shall provide the following initial report areas:

1. Dashboard: portfolio value, YTD time-weighted return, YTD money-weighted return, annual dividend income, cash, sector allocation, top holdings, and data-refresh/import status.
2. Holdings: account- and security-level positions with filters.
3. Performance: annual-return table and portfolio-value history.
4. Income: paid dividends/distributions, trailing income, estimated income, yield, and payment schedule.
5. Diversification: sector, region, asset class, currency, broker, account-type, and concentration views.
6. Imports: upload, preview, warnings, errors, reconciliation, and import history.
7. Accounts: account inventory and account settings.

## 7. Non-functional requirements

### NFR-1: Privacy

The product is self-hosted. It shall not require brokerage credentials for version 1. Data isolation between authenticated users is mandatory.

### NFR-2: Auditability

Calculated figures must be traceable to imported transactions, market-data observations, or explicit user overrides. Users must be able to identify the basis and timestamp of a displayed value.

### NFR-3: Data preservation

Original uploaded files, imported raw rows, normalized transaction records, imports, and user overrides must be retained unless explicitly deleted through an authorized retention/deletion workflow.

### NFR-4: Correctness over apparent completeness

When a required input is absent or stale, the product shall display the data-quality condition rather than fabricate a value, report zero, or label the result current.

### NFR-5: Extensibility

The product must permit future support for additional brokers, import profiles, asset classes, tax analysis, multi-user sharing, ETF look-through, retirement modelling, and benchmarks without invalidating historical data.

## 8. Deferred roadmap

### Phase 2 candidates

- Canadian taxable-account ACB and realized-capital-gain analysis.
- GICs, bonds, crypto, options, real estate, private assets, and collectibles.
- ETF look-through geographic and sector analysis.
- Benchmarks and advanced attribution/risk metrics.
- Retirement-gap planning with conservative/base/optimistic scenarios.
- Household/family groups, adviser access, and sharing.

### Not a product goal

The tracker does not provide investment recommendations, tax advice, tax filing, portfolio management, or trading functionality.

## 9. Acceptance criteria for the version 1 baseline

The version 1 requirements are satisfied when an authenticated user can:

1. Create separate Wealthsimple and Questrade accounts, including registered and non-registered accounts.
2. Import the full available transaction histories from both brokers without overwriting the source files.
3. Review and correct unmapped or invalid import rows before commit.
4. Re-import the same file without duplicating transactions.
5. Reconcile selected imported holdings and cash balances to broker-reported snapshots and see unresolved differences.
6. View all-account, registered, non-registered, and individual-account values and returns.
7. View YTD and annual time-weighted total return, plus money-weighted return, in CAD.
8. View a security's combined exposure and its account-level holdings/performance.
9. View gross/net dividend income, withholding tax, DRIP activity, trailing income, and yield metrics.
10. View sector allocation in a chart and table, including targets and drift.
11. View currency and initial regional allocation.
12. Identify the last refresh, next scheduled refresh, market-data status, and valuation timestamps.
13. Override a missing/incorrect security classification, price, or FX rate without losing the original source data.

## 10. Open requirements to resolve before implementation

The following are intentionally undecided until implementation planning:

- Selected market-price and FX-data provider(s).
- Required market coverage, exchange coverage, and handling of delayed versus real-time quotes.
- Refresh behaviour outside market hours and during an unavailable data-provider window.
- Account recovery, password policy, and administrator requirements.
- Backup, restore, retention, and deletion policy.
- Formal reconciliation tolerance rules.
- Exact broker export samples and field-level mapping specifications.
- Whether multiple portfolios per user are needed at first release or only one.
