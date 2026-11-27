"""Wealthsimple parser against the hand-written golden fixture (specs/import-wealthsimple.md, specs/testing.md)."""

from __future__ import annotations

import hashlib
import re
import uuid
from dataclasses import replace
from decimal import Decimal

import pytest
from fastapi.testclient import TestClient

from importer.app import create_app
from importer.parsers.base import ParseError
from importer.parsers.wealthsimple import WealthsimpleParser

from .conftest import ACCOUNT_A_WS, USER_A
from .golden import TESTDATA, check, expected, load_context, natural_keys

FIXTURE = TESTDATA / "wealthsimple" / "synthetic_activities"


def parse(name: str, target_account_id=None):
    data = (FIXTURE / "input" / name).read_bytes()
    return WealthsimpleParser().parse(data, name, load_context(FIXTURE, target_account_id))


def test_main_file_matches_golden_output():
    check(parse("activities.csv"), expected(FIXTURE, "activities.json"))


def test_overlapping_file_matches_golden_output():
    check(parse("activities_older.csv"), expected(FIXTURE, "activities_older.json"))


def test_allocated_tax_sums_exactly():
    row = next(r for r in parse("activities.csv") if r.row_no == 18)
    parts = sum(Decimal(a["amount"]) for a in row.normalized["allocations"])
    assert parts == Decimal(row.normalized["net_amount"])


def test_natural_keys():
    main = natural_keys(parse("activities.csv"))
    assert all(re.fullmatch(r"[0-9a-f]{64}", k) for k in main.values())
    assert len(set(main.values())) == len(main)
    older = natural_keys(parse("activities_older.csv"))
    for older_row, main_row in expected(FIXTURE, "activities_older.json")["same_natural_key_as_main"].items():
        assert older[int(older_row)] == main[main_row]
    assert older[2] not in main.values()
    want = hashlib.sha256(
        "wealthsimple|21000000-0000-0000-0000-000000000001|2025-01-06|Trade/BUY|XQZG|10|25.1|-251|CAD".encode()
    ).hexdigest()
    assert main[3] == want


def test_usd_sub_account_is_the_same_account():
    rows = {r.row_no: r for r in parse("activities.csv")}
    assert rows[19].normalized["account_id"] == rows[2].normalized["account_id"]
    assert rows[19].raw["account_id"].endswith("USD")


def test_target_account_skips_other_tracked_accounts():
    fhsa = uuid.UUID("21000000-0000-0000-0000-000000000002")
    rows = parse("activities.csv", target_account_id=fhsa)
    assert {r.row_no for r in rows if r.status != "skipped"} == {31}


@pytest.mark.parametrize(
    ("data", "reason"),
    [
        (b"", "empty"),
        (b"Account Name,Account Type,Symbol\nA,TFSA,X\n", "missing columns"),  # e.g. the holdings report
        (b"PK\x03\x04xlsx", "spreadsheet"),
        (b"\xff\xfe\x00bad", "not UTF-8"),
    ],
)
def test_other_files_are_refused_as_a_whole(data, reason):
    with pytest.raises(ParseError, match=reason):
        WealthsimpleParser().parse(data, "x", load_context(FIXTURE))


def test_upload_with_the_real_parser(settings, seeded, owner):
    """End to end through the importer with the real parser: user A tracks one of the fixture's accounts."""
    owner.execute("UPDATE core.accounts SET external_number = 'SY12AB346CAD' WHERE id = %s", (ACCOUNT_A_WS,))
    client = TestClient(create_app(replace(settings, max_upload_bytes=1024 * 1024)))
    data = (FIXTURE / "input" / "activities.csv").read_bytes()
    res = client.post("/internal/imports", data={"broker": "wealthsimple"},
                      headers={"X-Service-Token": settings.service_token, "X-User-Id": str(USER_A)},
                      files={"file": ("activities.csv", data, "text/csv")})
    assert res.status_code == 201, res.text
    # User A tracks only the FHSA (SY12AB346): row 31 is staged, the other 37 rows are skipped.
    assert res.json()["counts"] == {"ok": 1, "skipped": 37}
    assert res.json()["status"] == "parsed"


def test_rules_map_unmapped_actions_and_unknown_listings():
    """D-36: an action rule fills a gap in the mapping table; a symbol rule settles a listing the file leaves open."""
    plain = parse("activities.csv")
    unmapped = next(r for r in plain if any(m["code"] == "unmapped_action" for m in r.messages))
    unlisted = next(r for r in plain if r.normalized and (r.normalized.get("security") or {}).get("symbol")
                    and r.normalized["security"]["exchange"] is None)
    parser = WealthsimpleParser()
    action, symbol = parser.rule_keys(unmapped.raw).action, parser.rule_keys(unlisted.raw).symbol
    ctx = replace(load_context(FIXTURE), action_rules={action: "FEE"}, symbol_rules={symbol: ("QZRL", "NEO")})
    ruled = {r.row_no: r for r in parser.parse((FIXTURE / "input" / "activities.csv").read_bytes(), "a.csv", ctx)}

    r = ruled[unmapped.row_no]
    assert r.status == "ok" and r.normalized["type"] == "FEE" and [m["code"] for m in r.messages] == ["action_rule"]
    sec = ruled[unlisted.row_no].normalized["security"]
    assert (sec["symbol"], sec["exchange"], sec["trading_currency"]) == ("QZRL", "NEO", "CAD")
    assert "symbol_rule" in [m["code"] for m in ruled[unlisted.row_no].messages]
    # Rows with neither the action nor the symbol do not change.
    for p in plain:
        if p.row_no != unmapped.row_no and p.raw.get("symbol") != unlisted.raw["symbol"]:
            r = ruled[p.row_no]
            assert (r.status, r.normalized, r.messages) == (p.status, p.normalized, p.messages), p.row_no
