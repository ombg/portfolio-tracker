# questrade/synthetic_holdings

Synthetic Questrade activities export for the holdings acceptance test (AC-8, `ac8-holdings.spec.ts`). Everything is
invented (D-20). Accounts: 91000001 TFSA, 91000002 FHSA, 91000003 RRSP (tracked as a managed account in the test).

Worked by hand (`specs/calculations.md`):

| Account | Security | Quantity | Average cost | Book value | Realized gain | Income | Cash |
| --- | --- | --- | --- | --- | --- | --- | --- |
| TFSA | QZHG (TSX) | 60 | 10.0495 CAD | 602.97 CAD | 73.07 | 6.00 | 1,336.10 CAD, 0.00 USD |
| TFSA | ZZHU (US) | 5 | 20.00 USD | 100.00 USD; CAD unknown (no broker rate) | 0.00 | 0.00 | |
| FHSA | QZHG (TSX) | 50 | 11.00 CAD | 550.00 CAD | 0.00 | 0.00 | 450.00 CAD |
| RRSP (managed) | QZHG (TSX) | 10 | 10.50 CAD | 105.00 CAD | 0.00 | 0.00 | 395.00 CAD |

- TFSA QZHG: 100 x 10.00 + 4.95 = 1,004.95; selling 40 removes 401.98 and realizes 480 - 4.95 - 401.98 = 73.07;
  60 left at 602.97. The dividend row names `QZHG` without `.TO` and takes the TSX listing from the trades (#58).
- By security (managed account left out, D-38): QZHG 110 at 1,152.97 CAD, informational average 10.4815, 2 accounts.
- Totals: book value 1,257.97 CAD excluding 1 position (ZZHU), realized 73.07, income 6.00, cash 2,181.10 CAD and
  0.00 USD.
