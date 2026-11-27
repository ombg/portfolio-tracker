"""Writes the synthetic Wealthsimple activities fixture (D-20: every account, symbol, name and amount is invented).

The rows below are the source of truth; this script writes them as CSV in Wealthsimple's layout. Row numbers in
expected/*.json are file lines (header = 1). Run inside the importer test image:
    docker run --rm -v "$PWD":/w -w /w pt-importer-test python make_inputs.py
"""

import csv

HEADER = ["effective_date", "effective_time", "settlement_date", "account_id", "account_name", "account_type",
          "activity_type", "activity_sub_type", "description", "direction", "symbol", "name", "currency", "quantity",
          "unit_price", "commission", "net_cash_amount"]

TFSA, TFSA_USD, FHSA, ROBO = "SY12AB345CAD", "SY12AB345USD", "SY12AB346CAD", "SY12AB347CAD"  # ROBO is not tracked
ACCT = {TFSA: ("TFSA", "TFSA"), TFSA_USD: ("TFSA", "TFSA"), FHSA: ("FHSA", "FHSA"),
        ROBO: ("Synthetic Growth portfolio", "TFSA")}


def row(eff, settle, acct, atype, sub, desc, symbol="", name="", cur="CAD", qty="", price="", comm="", net="",
        direction=""):
    aname, akind = ACCT[acct]
    return [eff, "10:00:00.000000", settle, acct, aname, akind, atype, sub, desc, direction, symbol, name, cur, qty,
            price, comm, net]


GROWTH = ("XQZG", "Synthetic Growth ETF")
ZEDCO = ("ZEDQ", "Zedco Synthetic Inc")
WYUS = ("WYUS", "Wyus Synthetic Corp")


def trade(eff, settle, acct, sub, sec, qty, price, net, executed, fx=None, desc_qty=None, cur="CAD"):
    verb = "Bought" if sub == "BUY" else "Sold"
    shown = desc_qty or f"{abs(float(qty)):.4f}"
    desc = f"{sec[0]} - {sec[1]}: {verb} {shown} shares at ${price} per share (executed at {executed})"
    if fx:
        desc += f", FX Rate: {fx}"
    return row(eff, settle, acct, "Trade", sub, desc, sec[0], sec[1], cur, qty, price, "0.00", net, "LONG")


def dividend(eff, acct, sec, received, record, amount, cur="CAD"):
    desc = f"{sec[0]} - {sec[1]}: Cash dividend distribution, received on {received}, record date of {record}"
    return row(eff, "", acct, "Dividend", "-", desc, sec[0], sec[1], cur, amount, "", "", amount)


def nrt(eff, acct, executed, amount, cur="CAD"):
    return row(eff, "", acct, "Tax", "NRT", f"Non-resident tax (executed at {executed})", cur=cur, qty=amount, net=amount)


MAIN = [
    row("2025-01-02", "", TFSA, "MoneyMovement", "EFT", "Deposit", qty="1000.00", net="1000.00"),            # 2
    trade("2025-01-06", "2025-01-07", TFSA, "BUY", GROWTH, "10.0000", "25.10", "-251.00", "2025-01-06"),    # 3
    trade("2025-01-06", "2025-01-08", TFSA, "BUY", ZEDCO, "2.0000", "207.00", "-414.00", "2025-01-07", fx="1.3800"),  # 4
    trade("2025-02-10", "2025-02-11", TFSA, "SELL", GROWTH, "-4.0000", "26.00", "104.00", "2025-02-10"),    # 5
    trade("2025-02-10", "2025-02-11", TFSA, "SELL", GROWTH, "-1.0000", "26.00", "25.00", "2025-02-10"),     # 6 amount off
    trade("2025-02-11", "2025-02-12", TFSA, "SELL", GROWTH, "-1.5000", "26.00", "39.00", "2025-02-11", desc_qty="2.0000"),  # 7
    dividend("2025-03-30", TFSA, GROWTH, "2025-03-31", "2025-03-14", "1.20"),                               # 8
    # NRT rule 1: one candidate (the TSX-listed dividend the same day is not a candidate), tax one day later.
    dividend("2025-04-14", TFSA, ZEDCO, "2025-04-15", "2025-04-01", "2.00"),                                # 9
    dividend("2025-04-14", TFSA, GROWTH, "2025-04-15", "2025-04-01", "0.80"),                               # 10
    nrt("2025-04-15", TFSA, "2025-04-16", "-0.30"),                                                          # 11
    trade("2025-05-01", "2025-05-02", TFSA, "BUY", WYUS, "5.0000", "70.00", "-350.00", "2025-05-01", fx="1.3700"),  # 12
    # NRT rule 2: two candidates, only ZEDQ's ratio (0.60 / 4.00 = 15%) is within 13-17%.
    dividend("2025-05-14", TFSA, ZEDCO, "2025-05-15", "2025-05-01", "4.00"),                                # 13
    dividend("2025-05-14", TFSA, WYUS, "2025-05-15", "2025-05-01", "10.00"),                                # 14
    nrt("2025-05-14", TFSA, "2025-05-15", "-0.60"),                                                          # 15
    # NRT rule 3: two candidates, neither ratio within 13-17% -> allocated pro rata.
    dividend("2025-06-15", TFSA, ZEDCO, "2025-06-16", "2025-06-02", "3.00"),                                # 16
    dividend("2025-06-15", TFSA, WYUS, "2025-06-16", "2025-06-02", "1.00"),                                 # 17
    nrt("2025-06-15", TFSA, "2025-06-16", "-0.90"),                                                          # 18
    # USD sub-account of the TFSA: same account, USD cash.
    dividend("2025-07-20", TFSA_USD, ZEDCO, "2025-07-21", "2025-07-07", "1.00", cur="USD"),                 # 19
    nrt("2025-07-20", TFSA_USD, "2025-07-21", "-0.15", cur="USD"),                                           # 20
    nrt("2025-08-19", TFSA, "2025-08-20", "-0.50"),                                                          # 21 no dividend
    row("2025-05-30", "", TFSA, "Fee", "-", "Management fee (executed at 2025-05-31)", qty="-1.50", net="-1.50"),  # 22
    row("2025-06-30", "", TFSA, "Fee", "-", "Subscription fee for the period 2025-06-01 to 2025-06-30",
        qty="-10.00", net="-10.00"),                                                                         # 23
    row("2025-06-01", "", TFSA, "AdministrativePayment", "MANAGEMENT_FEE_REFUND", "CAD Refund (executed at 2025-06-02)",
        qty="0.50", net="0.50"),                                                                             # 24
    row("2025-06-09", "", TFSA, "AdministrativePayment", "MANAGEMENT_FEE_REFUND", "CAD Refund (executed at 2025-06-10)",
        qty="-0.30", net="-0.30"),                                                                           # 25 reversal
    row("2025-06-09", "", TFSA, "AdministrativePayment", "MANAGEMENT_FEE_REFUND", "CAD Refund (executed at 2025-06-10)",
        qty="0.30", net="0.30"),                                                                             # 26
    row("2025-07-01", "", TFSA, "MoneyMovement", "EFT", "Withdrawal (executed at 2025-07-02)", qty="-200.00", net="-200.00"),  # 27
    row("2025-07-03", "", TFSA, "MoneyMovement", "E_TRFIN", "Synthetic e-Transfer Received", qty="300.00", net="300.00"),  # 28
    row("2025-07-09", "", TFSA_USD, "MoneyMovement", "TRANSFER_TF", "Institutional transfer into the account (executed at 2025-07-10)",
        cur="USD", qty="500.00", net="500.00"),                                                              # 29
    row("2025-07-10", "", TFSA, "MoneyMovement", "TRANSFER_TF", "Institutional transfer out of the account (executed at 2025-07-11)",
        qty="-100.00", net="-100.00"),                                                                       # 30
    row("2025-08-04", "2025-08-06", FHSA, "InternalSecurityTransfer", "-",
        "WYUS - Wyus Synthetic Corp: Institutional transfer of 3.0000 shares into the account (executed at 2025-08-05)",
        "WYUS", "Wyus Synthetic Corp", "USD", "3.0000", "", "", "300.00"),                                   # 31
    row("2025-08-05", "2025-08-07", TFSA, "InternalSecurityTransfer", "-",
        "ZEDQ - Zedco Synthetic Inc: Institutional transfer of 1.0000 shares out of the account (executed at 2025-08-06)",
        "ZEDQ", "Zedco Synthetic Inc", "USD", "-1.0000", "", "", "-210.00"),                                 # 32
    row("2025-08-31", "", TFSA, "CorporateAction", "SUBDIVISION",
        "XQZG - Synthetic Growth ETF - Stock Split Dividend: Corrected quantity of shares by 6.0000 (executed at 2025-09-01)",
        "XQZG", "Synthetic Growth ETF", "", "6.0000", "", "", "0.00", "LONG"),                               # 33
    row("2025-09-30", "", TFSA, "LegacyCorporateAction", "NAME_CHANGE",
        "OLDQ - Oldname Synthetic Corp: Distribution of -20.0000 shares (executed at 2025-10-01)",
        "OLDQ", "Oldname Synthetic Corp", "", "-20.0000", "", "", "0.00", "LONG"),                           # 34
    row("2025-09-30", "", TFSA, "LegacyCorporateAction", "NAME_CHANGE",
        "NEWQ.TO - Newname Synthetic Corp: Distribution of 20.0000 shares (executed at 2025-10-01)",
        "NEWQ.TO", "Newname Synthetic Corp", "", "20.0000", "", "", "0.00", "LONG"),                         # 35
    row("2025-11-02", "", TFSA, "LegacyCorporateAction", "NAME_CHANGE",
        "RVSQ - Reverse Synthetic Fund: Corrected quantity of shares by -9.0000 (executed at 2025-11-03)",
        "RVSQ", "Reverse Synthetic Fund", "", "-9.0000", "", "", "0.00", "LONG"),                            # 36
    row("2025-11-02", "", TFSA, "LegacyCorporateAction", "NAME_CHANGE",
        "RVSQ - Reverse Synthetic Fund: Corrected quantity of shares by 3.0000 (executed at 2025-11-03)",
        "RVSQ", "Reverse Synthetic Fund", "", "3.0000", "", "", "0.00", "LONG"),                             # 37
    trade("2025-01-06", "2025-01-07", ROBO, "BUY", GROWTH, "3.0000", "25.10", "-75.30", "2025-01-06"),      # 38 untracked robo
    row("2025-11-29", "", TFSA, "Interest", "-", "Interest (executed at 2025-11-30)", qty="0.10", net="0.10"),  # 39 unmapped
]

# An older export overlapping the main one (D-28): its rows 3 and 4 equal main rows 2 and 3.
OLDER = [
    row("2024-12-20", "", TFSA, "MoneyMovement", "EFT", "Deposit", qty="500.00", net="500.00"),
    MAIN[0],
    MAIN[1],
]

for name, rows in (("activities", MAIN), ("activities_older", OLDER)):
    with open(f"input/{name}.csv", "w", newline="", encoding="utf-8") as f:
        csv.writer(f).writerows([HEADER, *rows])
