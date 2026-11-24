# Roadmap

Milestones map to PRD section 9 acceptance criteria (AC-n). Each ends with something usable and a release tag on
`main` (`CONTRIBUTING.md`): M1 = `ver-0.1.0`, M2 = `ver-0.2.0`, ... M8 = `ver-0.8.0`; `ver-1.0.0` once all
acceptance criteria pass. Fixes between milestones bump the patch number.

| Milestone | Scope | Acceptance criteria |
| --- | --- | --- |
| M1 Foundation | repo, compose, migrations, auth + RLS, accounts CRUD, `ptctl` | AC-1 |
| M2 Imports | importer service, file store, Questrade and Wealthsimple parsers, preview/correct/commit, duplicate rules, import history | AC-2, AC-3, AC-4 |
| M3 Holdings & cash | ledger replay, average cost, cash, holdings views by account and security | AC-8 |
| M4 Market data | providers, 30-min refresh, backfill, status card, price/FX overrides | AC-12, AC-13 (price, FX) |
| M5 Performance | daily cache, TWR, XIRR, registered/non-registered summaries, dashboard | AC-6, AC-7 |
| M6 Income | paid income, withholding, DRIP, trailing, forward, yield on cost, schedule | AC-9 |
| M7 Diversification | GICS mapping, overrides, targets and drift, region, currency, market-cap group, concentration | AC-10, AC-11, AC-13 (classification) |
| M8 Reconciliation | Questrade and Wealthsimple snapshot import, manual snapshots, opening positions, history-gap check, reconciliation view, backups (D-18) | AC-5 |

All samples needed for M2 and M8 are available (private, `data/`).
