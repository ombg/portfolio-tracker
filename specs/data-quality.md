# Data quality and reconciliation

Implements: FR-4, NFR-4. Decisions: D-11. Owner: portfolio-api (`internal/quality`).

## Issues
Issues are computed, not stored by hand: a job re-derives them after each import, recalculation and refresh, and
stores the current set in `core.quality_issues (user_id, kind, scope, ref, first_seen, details, resolved_at)`.

| Kind | Detected when | Effect on display |
| --- | --- | --- |
| unknown_symbol | staged row cannot resolve a security | blocks commit of that row |
| duplicate_conflict | same natural key, different amounts | warning in preview |
| negative_holding | running quantity < 0 | position excluded, red badge on account |
| cash_difference | snapshot cash vs ledger cash beyond tolerance | badge on account, reconciliation shows delta |
| quantity_difference | snapshot quantity vs ledger beyond tolerance | same |
| missing_price | no price ever for a held security | value "No price", excluded from totals with a note |
| stale_price / stale_fx | per market-data rules | timestamp shown in amber |
| missing_metadata | no sector, region, asset class or currency | bucket "Unclassified" |
| refresh_failed | last run Failed or Partial | dashboard status card |

Rule: an unresolved issue is never displayed as zero, success or "reconciled" (NFR-4). Totals that exclude
something say so ("excludes 1 holding without a price").

## Reconciliation workflow (D-11)
1. User imports a broker positions/balances file or enters a snapshot (account, as-of date, lines).
2. API computes ledger holdings and cash at the as-of date and compares line by line.
3. Result per line: matched / quantity_difference / cash_difference / missing_in_ledger / missing_in_snapshot.
4. User resolves by importing missing history, adding a correction (D-7), or accepting with a note.
   Accepted differences stay visible in the reconciliation history.

Default tolerances: quantity 0.0001, cash 0.01 of the currency; editable per account.
