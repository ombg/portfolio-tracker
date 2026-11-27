"""Questrade parser against the hand-written golden fixture (specs/import-questrade.md, specs/testing.md)."""

from __future__ import annotations

import hashlib
import re
import uuid
from dataclasses import replace

import pytest

from importer.parsers.base import ParseContext, ParseError
from importer.parsers.questrade import QuestradeParser

from .conftest import USER_A
from .golden import TESTDATA, check, load_context, natural_keys
from .golden import expected as golden_expected

FIXTURE = TESTDATA / "questrade" / "synthetic_activities"


def context(target_account_id=None, **over) -> ParseContext:
    return replace(load_context(FIXTURE, target_account_id), **over)


def parse(name: str, **ctx):
    return QuestradeParser().parse((FIXTURE / "input" / name).read_bytes(), name, context(**ctx))


def expected(name: str) -> dict:
    return golden_expected(FIXTURE, name)


@pytest.mark.parametrize("ext", ["csv", "xlsx"])
def test_main_file_matches_golden_output(ext):
    check(parse(f"activities.{ext}"), expected("activities.json"))


@pytest.mark.parametrize("ext", ["csv", "xlsx"])
def test_overlapping_file_matches_golden_output(ext):
    check(parse(f"activities_older.{ext}"), expected("activities_older.json"))


def test_csv_and_xlsx_give_identical_rows():
    a, b = parse("activities.csv"), parse("activities.xlsx")
    assert [(r.row_no, r.status, r.normalized, r.messages) for r in a] == \
           [(r.row_no, r.status, r.normalized, r.messages) for r in b]


def test_raw_row_is_kept_verbatim():
    row = next(r for r in parse("activities.csv") if r.row_no == 3)
    assert row.raw["Description"] == "SYNTHETIC GROWTH ETF WE ACTED AS AGENT"
    assert row.raw["Quantity"] == "100.00000000"  # source text, not the normalized value


def test_natural_keys():
    main = natural_keys(parse("activities.csv"))
    assert all(re.fullmatch(r"[0-9a-f]{64}", k) for k in main.values())
    assert len(set(main.values())) == len(main), "distinct rows must have distinct keys"

    older = natural_keys(parse("activities_older.csv"))
    for older_row, main_row in expected("activities_older.json")["same_natural_key_as_main"].items():
        assert older[int(older_row)] == main[main_row]  # D-8, D-28: overlap is recognised
    assert older[2] not in main.values()

    # The documented formula, built independently: broker|account|trade date|action|symbol|qty|price|net|currency.
    want = hashlib.sha256(
        "questrade|20000000-0000-0000-0000-000000000001|2025-01-06|Buy|QZGR.TO|100|25.1|-2514.95|CAD".encode()
    ).hexdigest()
    assert main[3] == want


def test_target_account_skips_other_tracked_accounts():
    fhsa = uuid.UUID("20000000-0000-0000-0000-000000000002")
    rows = parse("activities.csv", target_account_id=fhsa)
    kept = {r.row_no for r in rows if r.status != "skipped"}
    assert kept == {19, 20, 21, 22}
    other = next(r for r in rows if r.row_no == 2)
    assert other.status == "skipped" and other.messages[0]["code"] == "other_account"


@pytest.mark.parametrize(
    ("data", "reason"),
    [
        (b"", "empty"),
        (b"Date,Amount\n2025-01-01,1\n", "missing columns"),
        (b"PK\x03\x04 not really a zip", "unreadable XLSX"),
        (b"\xff\xfe\x00bad", "neither XLSX nor UTF-8 CSV"),
    ],
)
def test_other_files_are_refused_as_a_whole(data, reason):
    with pytest.raises(ParseError, match=reason):
        QuestradeParser().parse(data, "x", context())


def test_upload_with_the_real_parser(settings, seeded, owner):
    from dataclasses import replace

    """End to end through the importer: user A tracks only 90000001, and a broker code saved in the database
    resolves (H000123 via a global alias; H000555 via the user's own)."""
    from fastapi.testclient import TestClient

    from importer.app import create_app

    sec = uuid.UUID("30000000-0000-0000-0000-000000000555")
    owner.execute("INSERT INTO core.securities (id, name, security_type, trading_currency) VALUES "
                  "(%s, 'Synthetic Management', 'stock', 'CAD'), "
                  "('30000000-0000-0000-0000-000000000123', 'Synthetic Old', 'stock', 'CAD')", (sec,))
    owner.execute("""INSERT INTO core.security_aliases (user_id, broker_id, alias, security_id)
                     SELECT u, b.id, a, s FROM (VALUES (NULL::uuid, 'H000123', '30000000-0000-0000-0000-000000000123'::uuid),
                                                     (%s::uuid, 'H000555', %s::uuid)) v(u, a, s)
                     CROSS JOIN (SELECT id FROM core.brokers WHERE code = 'questrade') b""", (USER_A, sec))
    client = TestClient(create_app(replace(settings, max_upload_bytes=1024 * 1024)))
    data = (FIXTURE / "input" / "activities.csv").read_bytes()
    res = client.post("/internal/imports", data={"broker": "questrade"},
                      headers={"X-Service-Token": settings.service_token, "X-User-Id": str(USER_A)},
                      files={"file": ("activities.csv", data, "text/csv")})
    assert res.status_code == 201, res.text
    body = res.json()
    # 38 staged rows: 8 skipped (FHSA, RESP, RRSP not tracked by user A), 3 errors (rows 17, 29, 34),
    # 5 warnings (rows 8, 13, 36, 39, 40), the rest ok.
    assert body["counts"] == {"ok": 22, "warning": 5, "error": 3, "skipped": 8}
    assert body["status"] == "needs_review"
    row14 = owner.execute("SELECT status, normalized FROM imports.import_rows WHERE batch_id = %s AND row_no = 14",
                          (body["batch_id"],)).fetchone()
    assert row14[0] == "ok"
    assert row14[1]["security"]["security_id"] == "30000000-0000-0000-0000-000000000123"


def test_symbol_rules_never_remap_a_gic_but_beat_an_alias():
    """D-36 with D-26: a GIC code keeps its own security; a user's rule for an internal code wins over an alias."""
    plain = {r.row_no: r for r in parse("activities.csv")}
    aliased = next(r for r in plain.values()
                   if r.normalized and (r.normalized.get("security") or {}).get("security_id"))
    code = aliased.normalized["security"]["broker_code"]
    ruled = {r.row_no: r for r in parse("activities.csv", symbol_rules={"1SYNB07": ("QZGI", "TSX"),
                                                                         code: ("QZAL", "TSX")})}
    for n, r in plain.items():
        if r.normalized and (r.normalized.get("security") or {}).get("broker_code") == "1SYNB07":
            assert ruled[n].normalized == r.normalized and ruled[n].messages == r.messages, n
    sec = ruled[aliased.row_no].normalized["security"]
    assert (sec["symbol"], sec["exchange"], sec["broker_code"], sec["security_id"]) == ("QZAL", "TSX", code, None)


def _activities(*rows: tuple[str, str, str, str, str, str]) -> bytes:
    """A small activities CSV for account 90000001: (activity, action, symbol, description, net, currency)."""
    header = ("Transaction Date,Settlement Date,Action,Symbol,Description,Quantity,Price,Gross Amount,Commission,"
              "Net Amount,Currency,Account #,Activity Type,Account Type")
    z = "0.00000000"
    lines = [header] + [
        f"2025-03-03 12:00:00 AM,2025-03-03 12:00:00 AM,{action},{symbol},{desc},"
        f"{qty},{z},{gross},0.00,{net},{cur},90000001,{activity},Individual TFSA"
        for activity, action, symbol, desc, net, cur in rows
        for qty, gross in [("10.00000000", net) if activity == "Trades" else (z, "0.00")]
    ]
    return ("\n".join(lines) + "\n").encode()


def test_tsx_dividend_symbols():
    """specs/import-questrade.md, Symbol resolution step 1: the cases the golden fixture does not cover."""
    rows = QuestradeParser().parse(_activities(
        ("Trades", "Buy", "ZZUS", "ZEDCO SYNTHETIC INC WE ACTED AS AGENT", "-100.00", "USD"),             # 2
        ("Dividends", "", "ZZUS", "ZEDCO SYNTHETIC INC CASH DIV ON 10 SHS", "1.00", "USD"),                # 3
        ("Dividends", "", "ZZUX", "ZEDCO SYNTHETIC INC CASH DIV ON 10 SHS", "1.00", "CAD"),                # 4
        ("Dividends", "DIV", ".QZRL", "SYNTHETIC RULED CORP CASH DIV ON 10 SHS", "1.00", "CAD"),          # 5
        ("Dividends", "", "ZZ.B", "ZEDCO CLASS B CASH DIV ON 10 SHS", "1.00", "USD"),                      # 6
        ("Dividends", "", "QZ-X", "SYNTHETIC ODD CASH DIV ON 10 SHS", "1.00", "CAD"),                      # 7
    ), "a.csv", context(symbol_rules={".QZRL": ("QZRL", "TSX")}))
    by = {r.row_no: r for r in rows}
    sec = {n: r.normalized and r.normalized["security"] for n, r in by.items()}
    codes = {n: [m["code"] for m in r.messages] for n, r in by.items()}
    # USD without .TO evidence stays a US listing, as before.
    assert (sec[3]["symbol"], sec[3]["exchange"], by[3].status) == ("ZZUS", "US", "ok")
    # CAD is never US by default: a name traded on a US listing is not proposed; the ticker itself is, on the TSX.
    assert (sec[4]["symbol"], sec[4]["exchange"], sec[4]["proposed"]) == (None, None,
                                                                           {"symbol": "ZZUX", "exchange": "TSX",
                                                                            "from_row": None})
    assert codes[4] == ["proposed_security"] and by[4].status == "warning"
    # A rule on the raw symbol (leading dot included) wins, and settles the row.
    assert (sec[5]["symbol"], sec[5]["exchange"], codes[5], by[5].status) == ("QZRL", "TSX", ["symbol_rule"], "ok")
    # A dotted ticker in USD and a malformed ticker stay errors to choose in the preview.
    assert codes[6] == ["unknown_symbol"] and codes[7] == ["unknown_symbol"]
