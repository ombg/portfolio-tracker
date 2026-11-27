"""Writes the synthetic Questrade activities fixture (D-20: every account, symbol, name and amount is invented).

The rows below are the source of truth; this script writes them as CSV and as XLSX in Questrade's layout (sheet
`Activities`, header row 1, every value a string, as in the real export). Run inside the importer test image:
    docker run --rm -v "$PWD":/w -w /w pt-importer-test python make_inputs.py
Row numbers in expected/*.json are the spreadsheet row numbers (header = 1).
"""

import csv

from openpyxl import Workbook

HEADER = ["Transaction Date", "Settlement Date", "Action", "Symbol", "Description", "Quantity", "Price",
          "Gross Amount", "Commission", "Net Amount", "Currency", "Account #", "Activity Type", "Account Type"]

TFSA, FHSA, RRSP, RESP = "90000001", "90000002", "90000003", "90000004"  # RRSP is not tracked (D-22)
TYPES = {TFSA: "Individual TFSA", FHSA: "Individual FHSA", RRSP: "Individual RRSP", RESP: "Individual family RESP"}


def d(day):
    return f"{day} 12:00:00 AM"


def row(trade, settle, action, symbol, desc, qty, price, gross, comm, net, cur, acct, activity):
    return [d(trade), d(settle), action, symbol, desc, qty, price, gross, comm, net, cur, acct, activity, TYPES[acct]]


Z = "0.00000000"
MAIN = [
    row("2025-01-02", "2025-01-02", "CON", "", "CONT 2000000001", Z, Z, "0.00", "0.00", "5000.00", "CAD", TFSA, "Deposits"),
    row("2025-01-06", "2025-01-07", "Buy", "QZGR.TO", "SYNTHETIC GROWTH ETF WE ACTED AS AGENT", "100.00000000", "25.10000000", "-2510.00", "-4.95", "-2514.95", "CAD", TFSA, "Trades"),
    row("2025-01-06", "2025-01-07", "Buy", "ZZUS", "ZEDCO SYNTHETIC INC WE ACTED AS AGENT", "10.00000000", "150.00000000", "-1500.00", "-4.95", "-1504.95", "USD", TFSA, "Trades"),
    row("2025-01-03", "2025-01-03", "FXT", "", "CONVERSION - SYNTHETIC", Z, Z, "0.00", "0.00", "-2000.00", "CAD", TFSA, "FX conversion"),
    row("2025-01-03", "2025-01-03", "FXT", "", "CONVERSION - SYNTHETIC", Z, Z, "0.00", "0.00", "1400.00", "USD", TFSA, "FX conversion"),
    row("2025-02-10", "2025-02-11", "Sell", "QZGR.TO", "SYNTHETIC GROWTH ETF WE ACTED AS AGENT", "-40.00000000", "26.00000000", "1040.00", "-4.95", "1035.05", "CAD", TFSA, "Trades"),
    row("2025-02-10", "2025-02-11", "Sell", "ZZUS", "ZEDCO SYNTHETIC INC WE ACTED AS AGENT EXCHANGE RATE 1.38500000", "-5.00000000", "160.00000000", "800.00", "-4.95", "790.00", "USD", TFSA, "Trades"),
    # Dividend rows drop the .TO that trades use: a leading dot (DIV) or a plain ticker (blank action), in CAD.
    row("2025-03-31", "2025-03-31", "DIV", ".QZGR", "SYNTHETIC GROWTH ETF CASH DIV ON 60 SHS REC 03/14/25 PAY 03/31/25", Z, "0.12000000", "0.00", "0.00", "7.20", "CAD", TFSA, "Dividends"),
    row("2025-06-30", "2025-06-30", "", "QZGR", "SYNTHETIC GROWTH ETF DIST ON 60 SHS REC 06/13/25 PAY 06/30/25", Z, "0.10000000", "0.00", "0.00", "6.00", "CAD", TFSA, "Dividends"),
    row("2025-06-16", "2025-06-16", "DIV", "ZZUS", "ZEDCO SYNTHETIC INC CASH DIV ON 5 SHS REC 06/02/25 PAY 06/16/25 NON-RES TAX WITHHELD", Z, "0.40000000", "0.00", "0.00", "2.00", "USD", TFSA, "Dividends"),
    row("2025-06-16", "2025-06-16", "", "ZZUS", "ZEDCO SYNTHETIC INC NON-RES TAX WITHHELD REC 06/02/25 PAY 06/16/25", Z, Z, "0.00", "0.00", "-0.30", "USD", TFSA, "Dividends"),
    row("2025-07-08", "2025-07-08", "DIS", "H000777", "ZEDCO SYNTHETIC INC STK SPLIT ON 5 SHS REC 07/01/25 PAY 07/08/25", "5.00000000", Z, "0.00", "0.00", "0.00", "USD", TFSA, "Dividends"),
    row("2025-08-11", "2025-08-11", "NAC", "H000123", "OLDNAME SYNTHETIC CORP NAME CHANGE @ 1:1 FROM OLDN TO NEWN", "-30.00000000", Z, "0.00", "0.00", "0.00", "CAD", TFSA, "Corporate actions"),
    row("2025-08-11", "2025-08-11", "NAC", "NEWN.TO", "NEWNAME SYNTHETIC CORP RECEIVED AS RESULT OF NAME CHANGE", "30.00000000", Z, "0.00", "0.00", "0.00", "CAD", TFSA, "Corporate actions"),
    row("2025-03-20", "2025-03-20", "ADJ", "", "2025 NRT REBATE ZZUS", Z, Z, "0.00", "0.00", "0.15", "USD", TFSA, "Fees and rebates"),
    row("2025-03-20", "2025-03-20", "ADJ", "", "2025 FEE REBATE QZGR", Z, Z, "0.00", "0.00", "1.00", "CAD", TFSA, "Fees and rebates"),
    row("2025-10-01", "2025-10-01", "LFJ", "", "Securities lending int payment as of Last Month", Z, Z, "0.00", "0.00", "0.42", "CAD", TFSA, "Other"),
    row("2025-01-15", "2025-01-15", "BUY", "1SYNB07", "SYNTHETIC SAVINGS BANK GIC ANNUAL PAY 01/15/2026 3.50% ISSUED ON 01/15/2025 INT 3.500% SYNTHETIC SAVINGS BANK", "1000.00000000", "1.00000000", "0.00", "0.00", "-1000.00", "CAD", FHSA, "Trades"),
    row("2026-01-15", "2026-01-15", "INT", "1SYNB07", "SYNTHETIC SAVINGS BANK GIC ANNUAL PAY 01/15/2026 3.50% ISSUED ON 01/15/2025 INT 3.500% GIC INTEREST", Z, Z, "0.00", "0.00", "35.00", "CAD", FHSA, "Interest"),
    row("2026-01-15", "2026-01-15", "RED", "1SYNB07", "SYNTHETIC SAVINGS BANK GIC ANNUAL PAY 01/15/2026 3.50% ISSUED ON 01/15/2025 INT 3.500% GIC REDEMPTION", "-1000.00000000", "1.00000000", "0.00", "0.00", "1000.00", "CAD", FHSA, "Corporate actions"),
    row("2025-04-01", "2025-04-01", "CON", "", "FHSA CONTRIBUTION FROM 9000000101", Z, Z, "0.00", "0.00", "1000.00", "CAD", FHSA, "Deposits"),
    row("2025-04-01", "2025-04-01", "WDR", "", "TFSA WITHDRAWAL TO 9000000201", Z, Z, "0.00", "0.00", "-1000.00", "CAD", TFSA, "Withdrawals"),
    row("2025-05-01", "2025-05-01", "TF6", "", "500.00 TRANSFER SYNTHETIC TRUST CO", Z, Z, "0.00", "0.00", "500.00", "CAD", TFSA, "Transfers"),
    row("2025-05-02", "2025-05-02", "TFO", "", "250.00 TRANSFER TO ACCOUNT 900-00002-01", Z, Z, "0.00", "0.00", "-250.00", "CAD", TFSA, "Transfers"),
    row("2025-05-31", "2025-05-31", "FCH", "", "PORTFOLIO MANAGEMENT FEE FOR 9000000101", Z, Z, "0.00", "0.00", "-3.50", "CAD", TFSA, "Fees and rebates"),
    row("2025-09-02", "2025-09-02", "BRW", "H000555", "SYNTHETIC MANAGEMENT INC JOURNAL TO USD", "-10.00000000", "50.00000000", "0.00", "0.00", "0.00", "CAD", TFSA, "Other"),
    row("2025-09-02", "2025-09-02", "BRW", "H000555", "SYNTHETIC MANAGEMENT INC JOURNAL FROM CAD AT AN FX RATE $500.00 OF 1.38889", "10.00000000", "36.00000000", "0.00", "0.00", "0.00", "USD", TFSA, "Other"),
    row("2025-12-15", "2025-12-15", "LIQ", "H000888", "SYNTHETIC TRUST SYNTHETIC 2025 TERM ETF LIQUIDATION @ USD $24.500000 PER SHARE", "-20.00000000", Z, "0.00", "0.00", "490.00", "USD", TFSA, "Corporate actions"),
    row("2025-03-31", "2025-03-31", "GRT", "", "PAYMENT OF 03/31/2025 AT 20 PCT CESG GRANT RECEIVED THIS MONTH", Z, Z, "0.00", "0.00", "100.00", "CAD", RESP, "Other"),
    row("2025-03-31", "2025-03-31", "EGT", "", "PAYMENT OF 03/31/2025 AT 10 PCT PROV GRANT RECEIVED THIS MONTH", Z, Z, "0.00", "0.00", "50.00", "CAD", RESP, "Other"),
    row("2025-03-01", "2025-03-01", "CON", "", "CONT 12 RESP FAMILY PLAN CONTRIBUTION", Z, Z, "0.00", "0.00", "500.00", "CAD", RESP, "Deposits"),
    row("2025-01-06", "2025-01-07", "Buy", "QZGR.TO", "SYNTHETIC GROWTH ETF WE ACTED AS AGENT", "10.00000000", "25.10000000", "-251.00", "-4.95", "-255.95", "CAD", RRSP, "Trades"),
    row("2025-10-15", "2025-10-15", "XYZ", "", "SYNTHETIC UNKNOWN ACTIVITY", Z, Z, "0.00", "0.00", "1.00", "CAD", TFSA, "Other"),
    [""] * len(HEADER),  # the real exports contain empty rows; they are not staged
    row("2025-08-01", "2025-08-01", "FXT", "", "CONVERSION - SYNTHETIC", Z, Z, "0.00", "0.00", "-100.00", "CAD", TFSA, "FX conversion"),
    # TSX units: traded as SYNU.UN.TO, distributed as SYNU.UN.
    row("2025-04-10", "2025-04-11", "Buy", "SYNU.UN.TO", "SYNTHETIC UNITS REIT WE ACTED AS AGENT", "20.00000000", "10.00000000", "-200.00", "-4.95", "-204.95", "CAD", TFSA, "Trades"),
    row("2025-05-15", "2025-05-15", "", "SYNU.UN", "SYNTHETIC UNITS REIT DIST ON 20 SHS REC 04/30/25 PAY 05/15/25", Z, "0.05000000", "0.00", "0.00", "1.00", "CAD", TFSA, "Dividends"),
    # CAD dividends whose ticker the file never trades: one named like a TSX security of the file, one unknown.
    row("2025-09-30", "2025-09-30", "", "QZGX", "SYNTHETIC GROWTH ETF CASH DIV ON 60 SHS REC 09/12/25 PAY 09/30/25", Z, "0.13000000", "0.00", "0.00", "7.80", "CAD", TFSA, "Dividends"),
    row("2025-10-31", "2025-10-31", "DIV", "QZNT", "SYNTHETIC NORTHERN TRUST CASH DIV ON 50 SHS REC 10/15/25 PAY 10/31/25", Z, "0.10000000", "0.00", "0.00", "5.00", "CAD", TFSA, "Dividends"),
]

# An older export of the same login that overlaps the main file (D-28): its rows 3 and 4 equal main rows 2 and 3.
OLDER = [
    row("2024-12-20", "2024-12-20", "CON", "", "CONT 2000000000", Z, Z, "0.00", "0.00", "1000.00", "CAD", TFSA, "Deposits"),
    MAIN[0],
    MAIN[1],
]


def write(name, rows):
    with open(f"input/{name}.csv", "w", newline="", encoding="utf-8") as f:
        csv.writer(f).writerows([HEADER, *rows])
    wb = Workbook()
    ws = wb.active
    ws.title = "Activities"
    for r in [HEADER, *rows]:
        ws.append([v if v != "" else None for v in r])
    wb.save(f"input/{name}.xlsx")


write("activities", MAIN)
write("activities_older", OLDER)
