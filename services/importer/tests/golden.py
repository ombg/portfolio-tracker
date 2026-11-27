"""Shared comparison for the parsers' golden fixtures (specs/testing.md): expected rows list only fields that differ
from these defaults, and a mismatch reports the first differing field."""

from __future__ import annotations

import json
import os
import uuid
from pathlib import Path

from importer.parsers.base import ParseContext, TrackedAccount

TESTDATA = Path(os.environ.get("TESTDATA", "/testdata"))

DEFAULTS = {
    "account_id": None, "type": None, "trade_date": None, "settle_date": None, "record_date": None, "pay_date": None,
    "security": None, "quantity": None, "split_ratio": None, "price": None, "commission": "0", "gross_amount": None,
    "net_amount": None, "currency": None, "fx_rate": None, "fx_source": None, "group": None, "related_row": None,
    "link_basis": None, "allocations": None, "counterparty_account_id": None, "transfer_status": None,
    "shares_on": None,
}
SECURITY_DEFAULTS = {
    "symbol": None, "exchange": None, "broker_code": None, "security_id": None, "proposed": None, "name": None,
    "security_type": None, "trading_currency": None, "gic": None,
}


def load_context(fixture: Path, target_account_id=None) -> ParseContext:
    c = json.loads((fixture / "input" / "context.json").read_text())
    return ParseContext(
        broker=c["broker"],
        accounts=tuple(TrackedAccount(**{**a, "id": uuid.UUID(a["id"])}) for a in c["accounts"]),
        target_account_id=target_account_id,
        aliases={k: uuid.UUID(v) for k, v in c["aliases"].items()},
    )


def expected(fixture: Path, name: str) -> dict:
    return json.loads((fixture / "expected" / name).read_text())


def expand(row: dict):
    if row["status"] == "skipped":
        return {"untracked_account": row["untracked"]} if "untracked" in row else None
    n = row["normalized"]
    if n is None:
        return None
    n = {**DEFAULTS, **n}
    if n["security"] is not None:
        n["security"] = {**SECURITY_DEFAULTS, **n["security"]}
    return n


def first_difference(want, got, path=""):
    if isinstance(want, dict) and isinstance(got, dict):
        for k in sorted(set(want) | set(got)):
            if (d := first_difference(want.get(k, "<missing>"), got.get(k, "<missing>"), f"{path}.{k}")) is not None:
                return d
        return None
    return None if want == got else f"{path}: want {want!r}, got {got!r}"


def check(rows, exp: dict) -> None:
    got = {r.row_no: r for r in rows}
    assert sorted(got) == [e["row_no"] for e in exp["rows"]], "staged row numbers differ"
    for e in exp["rows"]:
        r = got[e["row_no"]]
        actual = dict(r.normalized) if r.normalized is not None else None
        if actual is not None:
            actual.pop("natural_key", None)
        diff = first_difference(
            {"status": e["status"], "messages": e.get("messages", []), "normalized": expand(e)},
            {"status": r.status, "messages": [[m["level"], m["code"]] for m in r.messages], "normalized": actual},
        )
        assert diff is None, f"row {e['row_no']}{diff}"


def natural_keys(rows) -> dict[int, str]:
    return {r.row_no: r.normalized["natural_key"] for r in rows if r.normalized and "natural_key" in r.normalized}
