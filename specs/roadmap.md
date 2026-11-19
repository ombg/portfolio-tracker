# Roadmap

Milestones map to PRD section 9 acceptance criteria (AC-n). Each ends with something usable.

| Milestone | Scope | Acceptance criteria |
| --- | --- | --- |
| M1 Foundation | repo, compose, migrations, auth + RLS, accounts CRUD, `ptctl` | AC-1 |
| M2 Questrade import | importer service, file store, preview/correct/commit, duplicate rules, import history | AC-2 (Questrade), AC-3, AC-4 |
| M3 Holdings & cash | ledger replay, average cost, cash, holdings views by account and security | AC-8 |
| M4 Market data | providers, 30-min refresh, backfill, status card, price/FX overrides | AC-12, AC-13 (price, FX) |
| M5 Performance | daily cache, TWR, XIRR, registered/non-registered summaries, dashboard | AC-6, AC-7 |
| M6 Income | paid income, withholding, DRIP, trailing, forward, yield on cost, schedule | AC-9 |
| M7 Diversification | GICS mapping, overrides, targets and drift, region, currency, concentration | AC-10, AC-11, AC-13 (classification) |
| M8 Wealthsimple + reconciliation | Wealthsimple parser (needs Q-3), snapshots, reconciliation view | AC-2 (Wealthsimple), AC-5 |

M8's Wealthsimple parser can move earlier as soon as samples exist.
