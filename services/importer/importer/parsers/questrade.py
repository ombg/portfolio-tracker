"""Questrade "Activities" export (XLSX or CSV) -> normalized rows. Spec: specs/import-questrade.md.

The parser is pure: it reads the file and the ParseContext (the user's accounts at Questrade and known broker codes)
and never touches the database. Linking across rows of the same file (FX legs, symbol-change and journal pairs,
withholding tax to its dividend) happens here; pairing with rows already in the ledger happens at commit.
"""

from __future__ import annotations

import csv
import hashlib
import io
import re
import warnings
from dataclasses import dataclass, field
from datetime import date, datetime
from decimal import Decimal, InvalidOperation
from typing import Any
from uuid import UUID

from .base import EXCHANGE_CURRENCY, ParseContext, ParseError, Row, RuleKeys
from .common import fmt

HEADER = (
    "Transaction Date", "Settlement Date", "Action", "Symbol", "Description", "Quantity", "Price", "Gross Amount",
    "Commission", "Net Amount", "Currency", "Account #", "Activity Type", "Account Type",
)

# Account Type -> proposed tracker type for an untracked account (D-22).
ACCOUNT_TYPES = {
    "INDIVIDUAL TFSA": "TFSA", "INDIVIDUAL FHSA": "FHSA", "INDIVIDUAL RRSP": "RRSP",
    "INDIVIDUAL FAMILY RESP": "RESP", "INDIVIDUAL RESP": "RESP", "FAMILY RESP": "RESP",
    "INDIVIDUAL MARGIN": "NON_REGISTERED", "INDIVIDUAL CASH": "NON_REGISTERED", "MARGIN": "NON_REGISTERED",
    "CASH": "NON_REGISTERED",
}

TOLERANCE = Decimal("0.01")
GIC_CODE = re.compile(r"^\d[A-Z0-9]{5}\d$")  # e.g. 1SYNB07 (D-26)
INTERNAL_CODE = re.compile(r"^[A-Z]\d{6}$")  # e.g. H000123 (import-questrade.md, symbol resolution step 3)
LISTED = re.compile(r"^[A-Z][A-Z0-9]*(\.[A-Z0-9]+)*$")
SHARES_ON = re.compile(r"\bON\s+(\d+(?:\.\d+)?)\s+SHS\b")
REC_PAY = re.compile(r"\bREC\s+(\d\d)/(\d\d)/(\d\d)\s+PAY\s+(\d\d)/(\d\d)/(\d\d)\b")  # month first
LONG_DATE = re.compile(r"\b(\d\d)/(\d\d)/(\d{4})\b")  # month first
PERCENT = re.compile(r"(\d+(?:\.\d+)?)%")
EXCHANGE_RATE = re.compile(r"\bEXCHANGE RATE\s+(\d+(?:\.\d+)?)\b")
NRT_REBATE = re.compile(r"\bNRT REBATE\s+([A-Z][A-Z0-9.]*)\s*$")
LENDING_SYMBOL = re.compile(r"\bSymbol-([A-Z][A-Z0-9.]*)\b")
DIGIT_RUNS = re.compile(r"\d[\d-]*\d")
# Description text after the security name; the name is what precedes the first of these.
NAME_END = re.compile(
    r"\s+(?:WE ACTED AS AGENT|CASH DIV\b|DIST ON\b|DIST PAY\b|PAY ON\b|STK SPLIT\b|(?:NON-RES )?TAX WITHHELD\b|"
    r"NAME CHANGE\b|LIQUIDATION\b|JOURNAL\b|EXCHANGE RATE\b|GIC INTEREST\b|GIC REDEMPTION\b)"
)
NAME_TRAILING_WORDS = {"RECEIVED", "AS", "A", "RESULT", "OF"}

# Types whose quantity is shares (the rest are pure cash rows: quantity and price stay null).
SECURITY_TYPES_REQUIRED = {"BUY", "SELL", "DIVIDEND", "DISTRIBUTION", "SPLIT", "SYMBOL_CHANGE", "JOURNAL"}


class QuestradeParser:
    broker = "questrade"
    name = "questrade-activities"
    version = "1"

    def parse(self, data: bytes, filename: str, ctx: ParseContext) -> list[Row]:
        records = _read(data)
        return _Parse(ctx).run(records)

    def rule_keys(self, raw: dict[str, Any]) -> RuleKeys:
        return RuleKeys(action=action_key(raw), symbol=raw.get("Symbol", "").strip().upper() or None)


def action_key(rec: dict[str, Any]) -> str:
    """Key of an action rule (D-36): the source's Activity Type and Action, as written."""
    return f"{rec.get('Activity Type', '')}|{rec.get('Action', '')}"


# --------------------------------------------------------------------------------------------------------------
# Reading


def _read(data: bytes) -> list[tuple[int, dict[str, str]]]:
    """(spreadsheet row number, {column: text}) for every non-empty data row. Header must be the Questrade layout."""
    if data[:4] == b"PK\x03\x04":
        table = _read_xlsx(data)
    else:
        try:
            text = data.decode("utf-8-sig")
        except UnicodeDecodeError:
            raise ParseError("not a Questrade activities export: neither XLSX nor UTF-8 CSV") from None
        table = list(csv.reader(io.StringIO(text)))
    if not table:
        raise ParseError("not a Questrade activities export: the file is empty")
    header = [str(h or "").strip() for h in table[0]]
    missing = [h for h in HEADER if h not in header]
    if missing:
        raise ParseError(f"not a Questrade activities export: missing columns {', '.join(missing)}")
    out = []
    for i, cells in enumerate(table[1:], start=2):
        rec = {h: _cell(cells[j]) if j < len(cells) else "" for j, h in enumerate(header) if h}
        if any(rec.values()):
            out.append((i, rec))
    return out


def _read_xlsx(data: bytes) -> list[list[Any]]:
    from openpyxl import load_workbook

    with warnings.catch_warnings():
        warnings.simplefilter("ignore")  # the export has no stylesheet; openpyxl warns about it
        try:
            wb = load_workbook(io.BytesIO(data), read_only=True, data_only=True)
        except Exception as e:  # noqa: BLE001 - any read failure means "not this format"
            raise ParseError(f"not a Questrade activities export: unreadable XLSX ({type(e).__name__})") from None
    if "Activities" not in wb.sheetnames:
        raise ParseError("not a Questrade activities export: no sheet named Activities")
    return [list(r) for r in wb["Activities"].iter_rows(values_only=True)]


def _cell(v: Any) -> str:
    if v is None:
        return ""
    if isinstance(v, float):
        return repr(v)  # the real export stores text; a numeric cell keeps its shortest exact form
    return str(v).strip()


# --------------------------------------------------------------------------------------------------------------
# Helpers


def _dec(s: str, field_name: str) -> Decimal:
    try:
        return Decimal(s or "0")
    except InvalidOperation:
        raise _RowError("bad_number", f"{field_name} is not a number: {s!r}") from None


def _date(s: str, field_name: str, required: bool = True) -> str | None:
    if not s:
        if required:
            raise _RowError("bad_date", f"{field_name} is missing")
        return None
    try:
        return datetime.strptime(s.split(" ")[0], "%Y-%m-%d").date().isoformat()
    except ValueError:
        raise _RowError("bad_date", f"{field_name} is not a date: {s!r}") from None


def _short_date(m: str, d: str, y: str) -> str:
    return date(2000 + int(y), int(m), int(d)).isoformat()


def _digits(s: str) -> str:
    return re.sub(r"\D", "", s)


def security_name(description: str) -> str | None:
    name = NAME_END.split(description, maxsplit=1)[0].strip()
    words = name.split()
    while words and words[-1] in NAME_TRAILING_WORDS:
        words.pop()
    return " ".join(words) or None


def natural_key(account_id: UUID, rec: dict[str, str]) -> str:
    """D-8: hash(broker, account, trade date, action, symbol, quantity, price, net amount, currency). Raw action
    and symbol, canonical decimals, so the key is stable across parser versions and number formatting."""
    action = rec["Action"] or rec["Activity Type"]
    parts = [
        "questrade", str(account_id), rec["Transaction Date"].split(" ")[0], action, rec["Symbol"].upper(),
        fmt(_dec(rec["Quantity"], "Quantity")), fmt(_dec(rec["Price"], "Price")),
        fmt(_dec(rec["Net Amount"], "Net Amount")), rec["Currency"].upper(),
    ]
    return hashlib.sha256("|".join(parts).encode()).hexdigest()


class _RowError(Exception):
    def __init__(self, code: str, text: str):
        super().__init__(text)
        self.code, self.text = code, text


@dataclass
class _Work:
    """A row being parsed: the output Row plus facts used for linking."""

    row: Row
    rec: dict[str, str]
    n: dict[str, Any] | None = None  # normalized
    msgs: list[dict[str, str]] = field(default_factory=list)

    def warn(self, code: str, text: str) -> None:
        self.msgs.append({"level": "warning", "code": code, "text": text})

    def error(self, code: str, text: str) -> None:
        self.msgs.append({"level": "error", "code": code, "text": text})


# --------------------------------------------------------------------------------------------------------------
# Parsing


class _Parse:
    def __init__(self, ctx: ParseContext):
        self.ctx = ctx
        self.accounts = {_digits(a.external_number): a for a in ctx.accounts if a.external_number}
        self.aliases = {k.upper(): v for k, v in ctx.aliases.items()}
        self.names: dict[str, tuple[str, str, int]] = {}  # security name -> (symbol, exchange, row) from this file

    def run(self, records: list[tuple[int, dict[str, str]]]) -> list[Row]:
        works = [_Work(Row(row_no=i, raw=rec, status="ok"), rec) for i, rec in records]
        for w in works:
            try:
                self._map(w)
            except _RowError as e:
                w.n = None
                w.error(e.code, e.text)
        self._resolve_broker_codes(works)
        self._link_withholding(works)
        self._pair(works, "FX_CONVERSION", "fx", self._day_key, _fx_legs,
                   "unpaired_fx_leg", "FX conversion leg without its other currency on the same day")
        self._pair(works, "SYMBOL_CHANGE", "symbol_change", self._day_key, _opposite_qty,
                   "unpaired_symbol_change", "symbol change leg without its opposite leg on the same day")
        self._pair(works, "JOURNAL", "journal", self._journal_key, _opposite_qty,
                   "unpaired_journal", "journal leg without its opposite leg on the same day")
        out = []
        for w in works:
            if w.row.status != "skipped":
                levels = {m["level"] for m in w.msgs}
                w.row.status = "error" if "error" in levels else "warning" if "warning" in levels else "ok"
                w.row.normalized = w.n
            w.row.messages = w.msgs
            out.append(w.row)
        return out

    # -- one row ------------------------------------------------------------------------------------------------

    def _map(self, w: _Work) -> None:
        rec = w.rec
        acct = self.accounts.get(_digits(rec["Account #"]))
        if acct is None:
            w.row.status = "skipped"
            label = rec["Account Type"]
            w.row.normalized = {"untracked_account": {
                "number": rec["Account #"], "account_type": label or None,
                "proposed_type": ACCOUNT_TYPES.get(label.upper(), "OTHER"), "proposed_management": "self_directed",
            }}
            w.msgs.append({"level": "info", "code": "untracked_account",
                           "text": "account is not tracked; rows are skipped (D-22)"})
            return
        if self.ctx.target_account_id is not None and acct.id != self.ctx.target_account_id:
            w.row.status = "skipped"
            w.msgs.append({"level": "info", "code": "other_account", "text": "not the account chosen for this import"})
            return

        action, activity = rec["Action"], rec["Activity Type"]
        desc = rec["Description"]
        qty = _dec(rec["Quantity"], "Quantity")
        price = _dec(rec["Price"], "Price")
        gross = _dec(rec["Gross Amount"], "Gross Amount")
        comm = _dec(rec["Commission"], "Commission")
        net = _dec(rec["Net Amount"], "Net Amount")
        currency = rec["Currency"].upper()
        if currency not in ("CAD", "USD"):
            raise _RowError("bad_currency", f"unexpected currency {rec['Currency']!r}")

        n: dict[str, Any] = {
            "account_id": str(acct.id), "type": None,
            "trade_date": _date(rec["Transaction Date"], "Transaction Date"),
            "settle_date": _date(rec["Settlement Date"], "Settlement Date", required=False),
            "record_date": None, "pay_date": None, "security": None, "quantity": None, "split_ratio": None,
            "price": None, "commission": "0", "gross_amount": None, "net_amount": fmt(net), "currency": currency,
            "fx_rate": None, "fx_source": None, "group": None, "related_row": None, "link_basis": None,
            "allocations": None, "counterparty_account_id": None, "transfer_status": None, "shares_on": None,
            "natural_key": natural_key(acct.id, rec),
        }
        t = self._type(action, activity, desc, rec["Symbol"], net)
        if t is None and (t := self.ctx.action_rules.get(action_key(rec))):
            w.msgs.append({"level": "info", "code": "action_rule",
                           "text": f"{activity or '-'} / {action or '-'} mapped to {t} by your rule"})
        if t is None:
            raise _RowError("unmapped_action", f"no mapping for {activity or '-'} / {action or '-'}; map it in the preview")
        n["type"] = t
        n["security"] = self._security(rec["Symbol"], desc, currency, t)
        if (sec := n["security"]) and (sym := rec["Symbol"].strip().upper()) and self._symbol_rule(sym):
            w.msgs.append({"level": "info", "code": "symbol_rule",
                           "text": f"{sym} mapped to {sec['symbol']} ({sec['exchange']}) by your rule"})
        if t in SECURITY_TYPES_REQUIRED and n["security"] is None:
            raise _RowError("missing_symbol", f"{t} row without a symbol")

        if t in ("BUY", "SELL"):
            n["quantity"] = fmt(abs(qty))
            n["commission"] = fmt(abs(comm))
            n["gross_amount"] = fmt(gross) if gross else None
            if action == "LIQ":
                n["price"] = fmt((abs(net) / abs(qty)).quantize(Decimal("0.00000001"))) if qty else None
            else:
                n["price"] = fmt(price)
            if m := EXCHANGE_RATE.search(desc):
                n["fx_rate"], n["fx_source"] = fmt(Decimal(m.group(1))), "broker"
            if gross:
                if abs(gross + comm - net) > TOLERANCE:
                    w.warn("net_mismatch", f"Gross {gross} + Commission {comm} differs from Net {net}")
                if abs(abs(qty * price) - abs(gross)) > TOLERANCE:
                    w.warn("gross_mismatch", f"Quantity x Price differs from Gross {gross}")
        elif t in ("DIVIDEND", "DISTRIBUTION", "WITHHOLDING_TAX", "SPLIT"):
            if m := REC_PAY.search(desc):
                n["record_date"] = _short_date(*m.group(1, 2, 3))
                n["pay_date"] = _short_date(*m.group(4, 5, 6))
            if (m := SHARES_ON.search(desc)) and t != "WITHHOLDING_TAX":
                n["shares_on"] = fmt(Decimal(m.group(1)))  # D-29, checked against the ledger later
            if t in ("DIVIDEND", "DISTRIBUTION"):
                n["gross_amount"] = fmt(gross if gross else net)
                if net < 0:
                    w.warn("negative_dividend", "negative dividend, probably a reversal")
            if t == "SPLIT":
                n["quantity"] = fmt(qty)  # quantity delta (data-model.md, Splits)
        elif t in ("SYMBOL_CHANGE", "JOURNAL"):
            n["quantity"] = fmt(qty)
            if t == "JOURNAL":
                n["price"] = fmt(price)
        elif t in ("TRANSFER_IN", "TRANSFER_OUT", "DEPOSIT", "WITHDRAWAL"):
            other = self._internal_counterparty(desc, acct.id) if action in ("CON", "WDR", "TF6", "TFO") else None
            if other is not None:
                n["type"] = "TRANSFER_IN" if t in ("TRANSFER_IN", "DEPOSIT") else "TRANSFER_OUT"
                n["counterparty_account_id"], n["transfer_status"] = str(other), "awaiting_counterpart"
            if n["security"] is not None:  # in-kind transfer
                n["quantity"] = fmt(abs(qty))
        w.n = n

        sec = n["security"]
        if sec and sec["symbol"] and sec["name"] and t in ("BUY", "SELL"):
            self.names.setdefault(sec["name"], (sec["symbol"], sec["exchange"], w.row.row_no))

    def _type(self, action: str, activity: str, desc: str, symbol: str, net: Decimal) -> str | None:
        a = action.upper()
        if activity == "Trades":
            if action in ("Buy", "Sell"):
                return action.upper()
            if action == "BUY" and GIC_CODE.match(symbol.upper()):
                return "BUY"
            return None
        if activity == "Dividends":
            if a == "DIS":
                return "SPLIT" if "STK SPLIT" in desc else None
            if a in ("DIV", ""):
                if "TAX WITHHELD" in desc and net < 0:
                    return "WITHHOLDING_TAX"
                return "DISTRIBUTION" if re.search(r"\bDIST\b", desc) else "DIVIDEND"
            return None
        if a == "CON" and activity == "Deposits":
            return "DEPOSIT"
        if a == "WDR" and activity == "Withdrawals":
            return "WITHDRAWAL"
        if a == "TF6" and activity == "Transfers":
            return "TRANSFER_IN"
        if a == "TFO" and activity == "Transfers":
            return "TRANSFER_OUT"
        if a == "FXT":
            return "FX_CONVERSION"
        if a == "FCH":
            return "FEE"
        if a == "ADJ":
            return "WITHHOLDING_TAX" if NRT_REBATE.search(desc) else None
        if a == "LFJ":
            return "INTEREST"
        if a == "INT":
            return "INTEREST"
        if a == "NAC":
            return "SYMBOL_CHANGE"
        if a == "LIQ":
            return "SELL"
        if a in ("RDM", "RED"):
            return "SELL"
        if a == "BRW":
            return "JOURNAL"
        if a in ("GRT", "EGT"):
            return "GRANT"
        return None

    def _security(self, symbol: str, desc: str, currency: str, t: str) -> dict[str, Any] | None:
        sym = symbol.strip().upper()
        if not sym and t == "WITHHOLDING_TAX" and (m := NRT_REBATE.search(desc)):
            sym = m.group(1)
            name = None
        elif not sym and t == "INTEREST" and (m := LENDING_SYMBOL.search(desc)):
            sym = m.group(1)
            name = None
        else:
            name = security_name(desc)
        if not sym:
            return None
        sec: dict[str, Any] = {
            "symbol": None, "exchange": None, "broker_code": None, "security_id": None, "proposed": None,
            "name": name, "security_type": None, "trading_currency": currency, "gic": None,
        }
        if rule := self._symbol_rule(sym):
            sec.update(symbol=rule[0], exchange=rule[1], trading_currency=EXCHANGE_CURRENCY[rule[1]],
                       broker_code=sym if INTERNAL_CODE.match(sym) else None)
        elif GIC_CODE.match(sym):
            gic = _gic_terms(desc)
            sec.update(broker_code=sym, security_type="gic", gic=gic,
                       name=f"{gic['issuer']} GIC {gic['rate']}% {gic['maturity']}" if gic else name)
        elif INTERNAL_CODE.match(sym):
            sec["broker_code"] = sym
        elif sym.endswith(".TO") and LISTED.match(sym[:-3]):
            sec.update(symbol=sym[:-3], exchange="TSX", trading_currency="CAD")
        elif LISTED.match(sym) and "." not in sym:
            sec.update(symbol=sym, exchange="US", trading_currency="USD")
        else:
            raise _RowError("unknown_symbol", f"unrecognised symbol {symbol!r}; choose the security in the preview")
        if sec["broker_code"] and not sec["symbol"] and sec["broker_code"] in self.aliases:
            sec["security_id"] = str(self.aliases[sec["broker_code"]])
        return sec

    def _symbol_rule(self, sym: str) -> tuple[str, str] | None:
        """The user's rule for this symbol or broker code; GIC codes are never remapped (D-26)."""
        return None if GIC_CODE.match(sym) else self.ctx.symbol_rules.get(sym)

    def _internal_counterparty(self, desc: str, own: UUID) -> UUID | None:
        """D-27: a tracked account's number in the description, digits only, broker suffix of up to 2 digits."""
        for run in DIGIT_RUNS.findall(desc):
            digits = _digits(run)
            for number, acct in self.accounts.items():
                if acct.id != own and digits.startswith(number) and len(digits) - len(number) <= 2:
                    return acct.id
        return None

    # -- across rows ----------------------------------------------------------------------------------------------

    def _resolve_broker_codes(self, works: list[_Work]) -> None:
        """Internal codes without a saved alias: propose the security this file traded under the same name
        (step 3), else an error that blocks commit until the user chooses (step 5). GIC codes need no alias."""
        for w in works:
            sec = w.n and w.n["security"]
            if not sec or not sec["broker_code"] or sec["security_id"] or sec["symbol"] or sec["security_type"] == "gic":
                continue
            match = self._match_name(sec["name"])
            if match:
                symbol, exchange, row_no = match
                sec["proposed"] = {"symbol": symbol, "exchange": exchange, "from_row": row_no}
                w.warn("proposed_security", f"broker code {sec['broker_code']} looks like {symbol} (row {row_no}); "
                       "confirm it in the preview")
            else:
                w.error("unknown_broker_code", f"unknown broker code {sec['broker_code']}; choose the security in "
                        "the preview")

    def _match_name(self, name: str | None) -> tuple[str, str, int] | None:
        if not name or len(name) < 6:
            return None
        if name in self.names:
            return self.names[name]
        hits = {v for k, v in self.names.items() if len(k) >= 6 and (k.startswith(name) or name.startswith(k))}
        return hits.pop() if len(hits) == 1 else None

    def _link_withholding(self, works: list[_Work]) -> None:
        """Withholding tax -> the dividend of the same account, security, pay date and currency in this file."""
        dividends: dict[tuple, list[int]] = {}
        for w in works:
            if w.n and w.n["type"] in ("DIVIDEND", "DISTRIBUTION") and w.n["pay_date"]:
                dividends.setdefault(_income_key(w.n), []).append(w.row.row_no)
        for w in works:
            n = w.n
            if not n or n["type"] != "WITHHOLDING_TAX" or not n["pay_date"]:
                continue
            hits = dividends.get(_income_key(n), [])
            if len(hits) == 1:
                n["related_row"], n["link_basis"] = hits[0], "single"
            else:
                w.warn("unlinked_withholding", "no single matching dividend in this file; linked at commit or by hand")

    def _pair(self, works, t, kind, key_fn, match_fn, code, text) -> None:
        groups: dict[tuple, list[_Work]] = {}
        for w in works:
            if w.n and w.n["type"] == t:
                groups.setdefault(key_fn(w.n), []).append(w)
        for legs in groups.values():
            free = list(legs)
            while free:
                a = free.pop(0)
                b = next((x for x in free if match_fn(a.n, x.n)), None)
                if b is None:
                    a.warn(code, text)
                    continue
                free.remove(b)
                g = {"kind": kind, "key": f"{kind}:{a.row.row_no}"}
                a.n["group"], b.n["group"] = g, dict(g)

    @staticmethod
    def _day_key(n):
        return n["account_id"], n["trade_date"]

    @staticmethod
    def _journal_key(n):
        s = n["security"] or {}
        return n["account_id"], n["trade_date"], s.get("broker_code") or s.get("symbol")


def _income_key(n: dict[str, Any]) -> tuple:
    s = n["security"] or {}
    return n["account_id"], n["pay_date"], n["currency"], s.get("symbol"), s.get("exchange"), s.get("broker_code")


def _fx_legs(a: dict[str, Any], b: dict[str, Any]) -> bool:
    """The two legs of one conversion: different currencies, money out of one and into the other."""
    return a["currency"] != b["currency"] and (Decimal(a["net_amount"]) > 0) != (Decimal(b["net_amount"]) > 0)


def _opposite_qty(a: dict[str, Any], b: dict[str, Any]) -> bool:
    qa, qb = Decimal(a["quantity"] or "0"), Decimal(b["quantity"] or "0")
    return qa != 0 and qa == -qb


def _gic_terms(desc: str) -> dict[str, str] | None:
    """Issuer (text before " GIC" or the first date), first rate, latest date as maturity (D-26)."""
    dates = [date(int(y), int(m), int(d)) for m, d, y in LONG_DATE.findall(desc)]
    rate = PERCENT.search(desc)
    if not dates or not rate:
        return None
    first = LONG_DATE.search(desc)
    head = desc[: first.start()] if first else desc
    issuer = re.split(r"\s+GIC\b", head, maxsplit=1)[0].strip()
    return {"issuer": issuer, "rate": fmt(Decimal(rate.group(1))), "maturity": max(dates).isoformat()}
