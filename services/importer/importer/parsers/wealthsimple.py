"""Wealthsimple "Activities export" CSV -> normalized rows. Spec: specs/import-wealthsimple.md.

Pure like the Questrade parser: file + ParseContext in, rows out. One file covers every account of the login;
rows are matched to tracked accounts by account_id, ignoring the trailing currency code (a USD sub-account is the
same account with USD cash).
"""

from __future__ import annotations

import csv
import hashlib
import io
import re
from dataclasses import dataclass, field
from datetime import date, timedelta
from decimal import ROUND_HALF_UP, Decimal, InvalidOperation
from typing import Any
from uuid import UUID

from .base import ParseContext, ParseError, Row
from .common import fmt

HEADER = (
    "effective_date", "effective_time", "settlement_date", "account_id", "account_name", "account_type",
    "activity_type", "activity_sub_type", "description", "direction", "symbol", "name", "currency", "quantity",
    "unit_price", "commission", "net_cash_amount",
)
ACCOUNT_TYPES = {"TFSA", "FHSA", "RRSP", "RESP", "NON_REGISTERED"}
TOLERANCE = Decimal("0.01")
CENT = Decimal("0.01")
EXECUTED = re.compile(r"\bexecuted at (\d{4}-\d\d-\d\d)\b")
RECEIVED = re.compile(r"\breceived on (\d{4}-\d\d-\d\d)\b")
RECORD = re.compile(r"\brecord date of (\d{4}-\d\d-\d\d)\b")
FX_RATE = re.compile(r"\bFX Rate: (\d+(?:\.\d+)?)\b")
DESC_SHARES = re.compile(r"\b(?:Bought|Sold) (\d+(?:\.\d+)?) shares\b")
CURRENCY_SUFFIX = re.compile(r"(CAD|USD)$")
LISTED = re.compile(r"^[A-Z][A-Z0-9]*(\.[A-Z0-9]+)*$")
# US treaty rate window for picking one dividend by its tax ratio (import-wealthsimple.md, rule 2).
RATIO_LOW, RATIO_HIGH = Decimal("0.13"), Decimal("0.17")


class WealthsimpleParser:
    broker = "wealthsimple"
    name = "wealthsimple-activities"
    version = "1"

    def parse(self, data: bytes, filename: str, ctx: ParseContext) -> list[Row]:
        return _Parse(ctx).run(_read(data))


def _read(data: bytes) -> list[tuple[int, dict[str, str]]]:
    if data[:4] == b"PK\x03\x04":
        raise ParseError("not a Wealthsimple activities export: this is a spreadsheet, expected CSV")
    try:
        text = data.decode("utf-8-sig")
    except UnicodeDecodeError:
        raise ParseError("not a Wealthsimple activities export: not UTF-8 CSV") from None
    table = list(csv.reader(io.StringIO(text)))
    if not table:
        raise ParseError("not a Wealthsimple activities export: the file is empty")
    header = [h.strip() for h in table[0]]
    missing = [h for h in HEADER if h not in header]
    if missing:
        raise ParseError(f"not a Wealthsimple activities export: missing columns {', '.join(missing)}")
    out = []
    for i, cells in enumerate(table[1:], start=2):
        rec = {h: (cells[j].strip() if j < len(cells) else "") for j, h in enumerate(header) if h}
        if any(rec.values()):
            out.append((i, rec))
    return out


def _base(account_id: str) -> str:
    return CURRENCY_SUFFIX.sub("", account_id.strip().upper())


def natural_key(account_id: UUID, rec: dict[str, str]) -> str:
    """D-8 with Wealthsimple's fields: broker|account|effective date|activity type/sub type|symbol|quantity|unit price|
    net cash amount|currency. Source values only (effective_date, not the date read from the description), canonical
    decimals."""
    parts = [
        "wealthsimple", str(account_id), rec["effective_date"],
        f"{rec['activity_type']}/{rec['activity_sub_type']}", rec["symbol"].upper(),
        fmt(_dec(rec["quantity"], "quantity")), fmt(_dec(rec["unit_price"], "unit_price")),
        fmt(_dec(rec["net_cash_amount"], "net_cash_amount")), rec["currency"].upper(),
    ]
    return hashlib.sha256("|".join(parts).encode()).hexdigest()


def _dec(s: str, field_name: str) -> Decimal:
    try:
        return Decimal(s.replace(",", "") or "0")
    except InvalidOperation:
        raise _RowError("bad_number", f"{field_name} is not a number: {s!r}") from None


def _iso(s: str, field_name: str) -> str:
    try:
        return date.fromisoformat(s).isoformat()
    except ValueError:
        raise _RowError("bad_date", f"{field_name} is not a date: {s!r}") from None


class _RowError(Exception):
    def __init__(self, code: str, text: str):
        super().__init__(text)
        self.code, self.text = code, text


@dataclass
class _Work:
    row: Row
    rec: dict[str, str]
    n: dict[str, Any] | None = None
    msgs: list[dict[str, str]] = field(default_factory=list)

    def warn(self, code: str, text: str) -> None:
        self.msgs.append({"level": "warning", "code": code, "text": text})

    def error(self, code: str, text: str) -> None:
        self.msgs.append({"level": "error", "code": code, "text": text})


class _Parse:
    def __init__(self, ctx: ParseContext):
        self.ctx = ctx
        self.accounts = {_base(a.external_number): a for a in ctx.accounts if a.external_number}

    def run(self, records: list[tuple[int, dict[str, str]]]) -> list[Row]:
        works = [_Work(Row(row_no=i, raw=rec, status="ok"), rec) for i, rec in records]
        self.listings = _listings(records)
        for w in works:
            try:
                self._map(w)
            except _RowError as e:
                w.n = None
                w.error(e.code, e.text)
        self._name_changes(works)
        self._link_withholding(works)
        for w in works:
            if w.row.status != "skipped":
                levels = {m["level"] for m in w.msgs}
                w.row.status = "error" if "error" in levels else "warning" if "warning" in levels else "ok"
                w.row.normalized = w.n
            w.row.messages = w.msgs
        return [w.row for w in works]

    # -- one row ------------------------------------------------------------------------------------------------

    def _map(self, w: _Work) -> None:
        rec = w.rec
        acct = self.accounts.get(_base(rec["account_id"]))
        if acct is None:
            w.row.status = "skipped"
            akind = rec["account_type"].upper()
            robo = rec["account_name"] != rec["account_type"] and rec["account_name"].lower().endswith("portfolio")
            suffix = CURRENCY_SUFFIX.search(rec["account_id"].strip().upper())
            w.row.normalized = {"untracked_account": {
                "number": rec["account_id"], "account_type": rec["account_type"] or None,
                "proposed_type": akind if akind in ACCOUNT_TYPES else "OTHER",
                "proposed_management": "robo" if robo else "self_directed",
                "proposed_base_currency": suffix.group(1) if suffix else "CAD",
            }}
            w.msgs.append({"level": "info", "code": "untracked_account",
                           "text": "account is not tracked; rows are skipped (D-22)"})
            return
        if self.ctx.target_account_id is not None and acct.id != self.ctx.target_account_id:
            w.row.status = "skipped"
            w.msgs.append({"level": "info", "code": "other_account", "text": "not the account chosen for this import"})
            return

        atype, sub, desc = rec["activity_type"], rec["activity_sub_type"], rec["description"]
        qty = _dec(rec["quantity"], "quantity")
        price = _dec(rec["unit_price"], "unit_price")
        comm = _dec(rec["commission"], "commission")
        net = _dec(rec["net_cash_amount"], "net_cash_amount")
        suffix = CURRENCY_SUFFIX.search(rec["account_id"].strip().upper())
        # Corporate actions carry no currency; they move no cash, so the sub-account's currency is used.
        currency = rec["currency"].upper() or (suffix.group(1) if suffix else acct.base_currency)
        if currency not in ("CAD", "USD"):
            raise _RowError("bad_currency", f"unexpected currency {rec['currency']!r}")

        # Dates: executed at, else received on (pay date), else effective_date (import-wealthsimple.md, Dates).
        m = EXECUTED.search(desc) or RECEIVED.search(desc)
        trade_date = _iso(m.group(1), "description date") if m else _iso(rec["effective_date"], "effective_date")

        n: dict[str, Any] = {
            "account_id": str(acct.id), "type": None, "trade_date": trade_date,
            "settle_date": _iso(rec["settlement_date"], "settlement_date") if rec["settlement_date"] else None,
            "record_date": None, "pay_date": None, "security": None, "quantity": None, "split_ratio": None,
            "price": None, "commission": "0", "gross_amount": None, "net_amount": fmt(net), "currency": currency,
            "fx_rate": None, "fx_source": None, "group": None, "related_row": None, "link_basis": None,
            "allocations": None, "counterparty_account_id": None, "transfer_status": None, "shares_on": None,
            "natural_key": natural_key(acct.id, rec),
        }
        t = _type(atype, sub, net)
        if t is None:
            raise _RowError("unmapped_action", f"no mapping for {atype or '-'} / {sub or '-'}; map it in the preview")
        n["type"] = t
        n["security"] = self._security(rec)
        if t in ("BUY", "SELL", "DIVIDEND", "SPLIT", "NAME_CHANGE") and n["security"] is None:
            raise _RowError("missing_symbol", f"{atype} row without a symbol")

        if t in ("BUY", "SELL"):
            n["quantity"], n["price"], n["commission"] = fmt(abs(qty)), fmt(price), fmt(abs(comm))
            if fx := FX_RATE.search(desc):
                n["fx_rate"], n["fx_source"] = fmt(Decimal(fx.group(1))), "broker"
            value = abs(qty * price)
            if min(abs(value + abs(comm) - abs(net)), abs(value - abs(comm) - abs(net))) > TOLERANCE:
                w.warn("amount_mismatch", f"quantity x unit_price and commission do not give net_cash_amount {net}")
            if (shares := DESC_SHARES.search(desc)) and Decimal(shares.group(1)) != abs(qty):
                w.warn("description_quantity_mismatch", f"description says {shares.group(1)} shares, quantity is {abs(qty)}")
        elif t == "DIVIDEND":
            n["gross_amount"] = fmt(net)  # before non-resident tax, which is its own row
            if r := RECEIVED.search(desc):
                n["pay_date"] = _iso(r.group(1), "received on")
            if r := RECORD.search(desc):
                n["record_date"] = _iso(r.group(1), "record date")
        elif t in ("TRANSFER_IN", "TRANSFER_OUT") and atype == "InternalSecurityTransfer":
            # In kind: quantity in shares; net_cash_amount is the market value, not cash (data-model.md, Linking).
            n["quantity"], n["gross_amount"], n["net_amount"] = fmt(abs(qty)), fmt(net), "0"
            if n["security"] is None:
                raise _RowError("missing_symbol", "in-kind transfer without a symbol")
        elif t in ("SPLIT", "NAME_CHANGE"):
            n["quantity"] = fmt(qty)  # quantity delta, signed
        w.n = n

    def _security(self, rec: dict[str, str]) -> dict[str, Any] | None:
        sym = rec["symbol"].strip().upper()
        if not sym:
            return None
        if sym.endswith(".TO"):
            symbol, exchange = sym[:-3], "TSX"
        elif "." in sym and LISTED.match(sym):
            symbol, exchange = sym, "TSX"  # other class suffixes (.F, .U, .UN) are TSX listings
        elif LISTED.match(sym):
            symbol, exchange = sym, self.listings.get(sym)
        else:
            raise _RowError("unknown_symbol", f"unrecognised symbol {rec['symbol']!r}; choose the security in the preview")
        return {
            "symbol": symbol, "exchange": exchange, "broker_code": None, "security_id": None, "proposed": None,
            "name": rec["name"] or None, "security_type": None,
            "trading_currency": {"TSX": "CAD", "US": "USD"}.get(exchange or ""), "gic": None,
        }

    # -- across rows ----------------------------------------------------------------------------------------------

    def _name_changes(self, works: list[_Work]) -> None:
        """NAME_CHANGE rows of one account and day: two different symbols with opposite quantities are a symbol
        change; one symbol with "Corrected quantity" deltas is a split, one SPLIT row per delta (they add up)."""
        groups: dict[tuple, list[_Work]] = {}
        for w in works:
            if w.n and w.n["type"] == "NAME_CHANGE":
                groups.setdefault((w.n["account_id"], w.n["trade_date"]), []).append(w)
        for legs in groups.values():
            symbols = {w.n["security"]["symbol"] for w in legs}
            first = legs[0].row.row_no
            if len(symbols) == 1:
                for w in legs:
                    w.n["type"] = "SPLIT"
                    if len(legs) > 1:
                        w.n["group"] = {"kind": "split", "key": f"split:{first}"}
            elif len(legs) == 2 and Decimal(legs[0].n["quantity"]) == -Decimal(legs[1].n["quantity"]) != 0:
                for w in legs:
                    w.n["type"] = "SYMBOL_CHANGE"
                    w.n["group"] = {"kind": "symbol_change", "key": f"symbol_change:{first}"}
            else:
                for w in legs:
                    w.n = None
                    w.error("unrecognised_name_change", "name change legs do not form a symbol change or a split")

    def _link_withholding(self, works: list[_Work]) -> None:
        """Non-resident tax -> dividend (import-wealthsimple.md, Linking): candidates are dividends of the same
        account and currency within one day, except securities known to be listed in Canada. One candidate: link;
        several: the unique one with a 13-17% tax ratio; otherwise allocate pro rata."""
        dividends = [w for w in works if w.n and w.n["type"] == "DIVIDEND"]
        for w in works:
            n = w.n
            if not n or n["type"] != "WITHHOLDING_TAX":
                continue
            day = date.fromisoformat(n["trade_date"])
            cands = [
                d for d in dividends
                if d.n["account_id"] == n["account_id"] and d.n["currency"] == n["currency"]
                and abs(date.fromisoformat(d.n["pay_date"] or d.n["trade_date"]) - day) <= timedelta(days=1)
                and (d.n["security"] or {}).get("exchange") != "TSX"
            ]
            tax = Decimal(n["net_amount"])
            if not cands:
                w.warn("unlinked_withholding", "no dividend within one day to link this tax to; link it by hand")
            elif len(cands) == 1:
                n["related_row"], n["link_basis"] = cands[0].row.row_no, "single"
            else:
                in_window = [d for d in cands if d.n["gross_amount"] and Decimal(d.n["gross_amount"]) > 0
                             and RATIO_LOW <= abs(tax) / Decimal(d.n["gross_amount"]) <= RATIO_HIGH]
                if len(in_window) == 1:
                    n["related_row"], n["link_basis"] = in_window[0].row.row_no, "ratio"
                else:
                    n["link_basis"], n["allocations"] = "allocated", _pro_rata(tax, cands)


def _pro_rata(tax: Decimal, cands: list[_Work]) -> list[dict[str, Any]]:
    """Splits tax across the dividends by amount, to the cent; the last share takes the remainder so the parts sum
    exactly to the tax."""
    total = sum(Decimal(d.n["gross_amount"]) for d in cands)
    parts, left = [], tax
    for i, d in enumerate(cands):
        amount = left if i == len(cands) - 1 else (tax * Decimal(d.n["gross_amount"]) / total).quantize(CENT, ROUND_HALF_UP)
        left -= amount
        parts.append({"row": d.row.row_no, "amount": fmt(amount)})
    return parts


def _listings(records: list[tuple[int, dict[str, str]]]) -> dict[str, str]:
    """Plain tickers: US when any trade or transfer of it is in USD or states an FX rate; TSX when it is traded in
    CAD without one (Wealthsimple states the rate whenever a US listing settles in CAD). Otherwise unknown, resolved
    against the security master at commit (import-wealthsimple.md, Symbol resolution)."""
    us, cad = set(), set()
    for _, r in records:
        sym = r["symbol"].strip().upper()
        if not sym or "." in sym:
            continue
        if r["activity_type"] in ("Trade", "InternalSecurityTransfer"):
            if r["currency"].upper() == "USD" or FX_RATE.search(r["description"]):
                us.add(sym)
            elif r["activity_type"] == "Trade" and r["currency"].upper() == "CAD":
                cad.add(sym)
    return {**{s: "TSX" for s in cad - us}, **{s: "US" for s in us}}


def _type(atype: str, sub: str, net: Decimal) -> str | None:
    if atype == "Trade" and sub in ("BUY", "SELL"):
        return sub
    if atype == "Dividend":
        return "DIVIDEND"
    if atype == "Tax" and sub == "NRT":
        return "WITHHOLDING_TAX"
    if atype == "Fee":
        return "FEE"
    if atype == "AdministrativePayment" and sub == "MANAGEMENT_FEE_REFUND":
        return "FEE"  # a refund is a negative fee (D-21): positive cash
    if atype == "MoneyMovement" and sub in ("EFT", "E_TRFIN"):
        return "DEPOSIT" if net > 0 else "WITHDRAWAL"
    if atype == "MoneyMovement" and sub == "TRANSFER_TF":
        return "TRANSFER_IN" if net > 0 else "TRANSFER_OUT"
    if atype == "InternalSecurityTransfer":
        return "TRANSFER_IN" if net > 0 else "TRANSFER_OUT"
    if atype == "CorporateAction" and sub == "SUBDIVISION":
        return "SPLIT"
    if atype == "LegacyCorporateAction" and sub == "NAME_CHANGE":
        return "NAME_CHANGE"  # resolved to SYMBOL_CHANGE or SPLIT across rows
    return None
