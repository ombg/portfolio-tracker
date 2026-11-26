"""Preview corrections (D-36): rules for actions and symbols, row exclusion and re-parsing after a new account.
Synthetic rows only (D-20): invented account numbers, codes and amounts."""

from __future__ import annotations

import uuid

import pytest
from fastapi.testclient import TestClient

from importer.app import create_app
from importer.parsers import default_parsers

from .conftest import ACCOUNT_A_QT, USER_A, USER_B

HEADER = ("Transaction Date,Settlement Date,Action,Symbol,Description,Quantity,Price,Gross Amount,Commission,"
          "Net Amount,Currency,Account #,Activity Type,Account Type")
D = "2025-03-03 12:00:00 AM"
ROWS = [
    # 2: an action the parser has no mapping for
    f"{D},{D},ZZQ,,SYNTHETIC SERVICE CHARGE,0,0,0,0,-7.50,CAD,90000001,Other,Individual TFSA",
    # 3 and 4: an internal code nothing in the file explains
    f"{D},{D},Buy,H000123,SYNTHETIC POOLED FUND UNITS WE ACTED AS AGENT,10,20,-200,0,-200,CAD,90000001,Trades,"
    "Individual TFSA",
    f"{D},{D},Buy,H000123,SYNTHETIC POOLED FUND UNITS WE ACTED AS AGENT,5,20,-100,0,-100,CAD,90000001,Trades,"
    "Individual TFSA",
    # 5: fine as it is
    f"{D},{D},CON,,CONT 2000000001,0,0,0,0,500,CAD,90000001,Deposits,Individual TFSA",
    # 6: an account the user does not track yet
    f"{D},{D},CON,,CONT 2000000009,0,0,0,0,300,CAD,90000009,Deposits,Individual TFSA",
]


def csv_bytes(rows: list[str]) -> bytes:
    return ("\n".join([HEADER, *rows]) + "\n").encode()


@pytest.fixture
def qt_client(settings, seeded) -> TestClient:
    from dataclasses import replace

    return TestClient(create_app(replace(settings, max_upload_bytes=1 << 20), default_parsers()))


def headers(settings, user=USER_A) -> dict[str, str]:
    return {"X-Service-Token": settings.service_token, "X-User-Id": str(user)}


def upload(client, settings, data: bytes) -> dict:
    res = client.post("/internal/imports", headers=headers(settings), data={"broker": "questrade"},
                      files={"file": ("activities.csv", data, "text/csv")})
    assert res.status_code == 201, res.text
    return res.json()


def patch(client, settings, batch: str, row: int, body: dict, user=USER_A):
    return client.patch(f"/internal/imports/{batch}/rows/{row}", headers=headers(settings, user), json=body)


def rows(owner, batch: str) -> dict[int, dict]:
    return {
        r[0]: {"status": r[1], "normalized": r[2], "codes": [m.get("code") for m in r[3]], "user_edits": r[4]}
        for r in owner.execute(
            "SELECT row_no, status, normalized, messages, user_edits FROM imports.import_rows WHERE batch_id = %s",
            (batch,),
        )
    }


def test_upload_shows_what_needs_fixing(qt_client, settings, owner):
    body = upload(qt_client, settings, csv_bytes(ROWS))
    assert body["status"] == "needs_review"
    assert body["counts"] == {"error": 3, "ok": 1, "skipped": 1}
    r = rows(owner, body["batch_id"])
    assert r[2]["codes"] == ["unmapped_action"] and r[3]["codes"] == ["unknown_broker_code"]


def test_action_rule_maps_the_row_and_is_saved(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    res = patch(qt_client, settings, batch, 2, {"type": "FEE"})
    assert res.status_code == 200, res.text
    assert res.json() == {"batch_id": batch, "status": "needs_review", "counts": {"ok": 2, "error": 2, "skipped": 1}}
    r = rows(owner, batch)[2]
    assert r["status"] == "ok" and r["normalized"]["type"] == "FEE" and r["normalized"]["net_amount"] == "-7.5"
    assert r["codes"] == ["action_rule"]
    rule = owner.execute("SELECT user_id, kind, source_key, target FROM imports.import_rules").fetchall()
    assert rule == [(USER_A, "action", "Other|ZZQ", {"type": "FEE"})]

    # Changing the mapping replaces the rule rather than adding another.
    assert patch(qt_client, settings, batch, 2, {"type": "WITHDRAWAL"}).status_code == 200
    assert rows(owner, batch)[2]["normalized"]["type"] == "WITHDRAWAL"
    assert owner.execute("SELECT count(*) FROM imports.import_rules").fetchone()[0] == 1


def test_symbol_rule_fixes_every_row_with_that_code(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    res = patch(qt_client, settings, batch, 3, {"security": {"symbol": "qzpf", "exchange": "tsx"}})
    assert res.status_code == 200, res.text
    r = rows(owner, batch)
    for n in (3, 4):
        sec = r[n]["normalized"]["security"]
        assert r[n]["status"] == "ok", r[n]
        assert (sec["symbol"], sec["exchange"], sec["broker_code"], sec["trading_currency"]) == \
            ("QZPF", "TSX", "H000123", "CAD")
        assert r[n]["codes"] == ["symbol_rule"]
    assert owner.execute("SELECT kind, source_key, target FROM imports.import_rules").fetchall() == [
        ("symbol", "H000123", {"symbol": "QZPF", "exchange": "TSX"})]


def test_excluding_unblocks_and_can_be_undone(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    for n in (2, 3, 4):
        assert patch(qt_client, settings, batch, n, {"excluded": True}).status_code == 200
    res = patch(qt_client, settings, batch, 3, {"excluded": True})
    assert res.json() == {"batch_id": batch, "status": "parsed", "counts": {"excluded": 3, "ok": 1, "skipped": 1}}
    r = rows(owner, batch)
    assert r[2]["status"] == "excluded" and r[2]["user_edits"] == {"excluded": True}
    assert r[2]["codes"] == ["unmapped_action", "excluded"]  # the reason stays visible

    res = patch(qt_client, settings, batch, 2, {"excluded": False})
    assert res.json()["counts"] == {"error": 1, "excluded": 2, "ok": 1, "skipped": 1}
    assert rows(owner, batch)[2]["user_edits"] is None
    stored = owner.execute("SELECT status, counts FROM imports.import_batches WHERE id = %s", (batch,)).fetchone()
    assert stored == ("needs_review", {"error": 1, "excluded": 2, "ok": 1, "skipped": 1})


def test_rules_apply_to_the_next_upload(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    patch(qt_client, settings, batch, 2, {"type": "FEE"})
    patch(qt_client, settings, batch, 3, {"security": {"symbol": "QZPF", "exchange": "TSX"}})
    later = upload(qt_client, settings, csv_bytes(ROWS[:3]))  # different bytes, same codes
    assert later["status"] == "parsed" and later["counts"] == {"ok": 3}


def test_rules_belong_to_their_user(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    patch(qt_client, settings, batch, 2, {"type": "FEE"})
    owner.execute("UPDATE core.accounts SET external_number = '90000001' WHERE user_id = %s", (USER_B,))
    res = qt_client.post("/internal/imports", headers=headers(settings, USER_B), data={"broker": "questrade"},
                         files={"file": ("b.csv", csv_bytes(ROWS[:1]), "text/csv")})
    assert res.json()["counts"] == {"error": 1}


def test_reparse_picks_up_a_new_account(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    owner.execute(
        """INSERT INTO core.accounts (user_id, portfolio_id, broker_id, external_number, name, type, is_registered,
                                      base_currency)
           SELECT %s, p.id, b.id, '90000009', 'A QT second', 'TFSA', true, 'CAD'
           FROM core.portfolios p, core.brokers b WHERE p.user_id = %s AND b.code = 'questrade'""",
        (USER_A, USER_A),
    )
    res = qt_client.post(f"/internal/imports/{batch}/reparse", headers=headers(settings))
    assert res.status_code == 200, res.text
    assert res.json()["counts"] == {"error": 3, "ok": 2}
    assert rows(owner, batch)[6]["normalized"]["type"] == "DEPOSIT"


@pytest.mark.parametrize("row, body, status, code", [
    (2, {"type": "TIP"}, 422, "invalid_type"),
    (5, {"type": "FEE"}, 422, "not_applicable"),  # CON already maps to DEPOSIT
    (3, {"security": {"symbol": "QZPF", "exchange": "LSE"}}, 422, "invalid_security"),
    (3, {"security": {"symbol": "QZ PF", "exchange": "TSX"}}, 422, "invalid_security"),
    (5, {"security": {"symbol": "QZPF", "exchange": "TSX"}}, 422, "not_applicable"),  # no symbol at all
    (6, {"excluded": True}, 422, "not_applicable"),  # skipped: untracked account
    (6, {"type": "FEE"}, 422, "not_applicable"),
    (2, {}, 400, "bad_request"),
    (2, {"type": "FEE", "note": "x"}, 400, "bad_request"),
    (99, {"excluded": True}, 404, "not_found"),
])
def test_row_edit_errors(qt_client, settings, owner, row, body, status, code):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    res = patch(qt_client, settings, batch, row, body)
    assert (res.status_code, res.json()["error"]) == (status, code), res.text
    assert owner.execute("SELECT count(*) FROM imports.import_rules").fetchone()[0] == 0


def test_another_users_batch_is_not_found(qt_client, settings, owner):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    res = patch(qt_client, settings, batch, 2, {"type": "FEE"}, user=USER_B)
    assert (res.status_code, res.json()["error"]) == (404, "not_found")
    res = qt_client.post(f"/internal/imports/{batch}/reparse", headers=headers(settings, USER_B))
    assert res.status_code == 404
    res = qt_client.post(f"/internal/imports/{uuid.uuid4()}/reparse", headers=headers(settings))
    assert res.status_code == 404
    assert owner.execute("SELECT count(*) FROM imports.import_rules").fetchone()[0] == 0


@pytest.mark.parametrize("status", ["discarded", "failed"])
def test_closed_batch_cannot_change(qt_client, settings, owner, status):
    batch = upload(qt_client, settings, csv_bytes(ROWS))["batch_id"]
    owner.execute("UPDATE imports.import_batches SET status = %s WHERE id = %s", (status, batch))
    res = patch(qt_client, settings, batch, 2, {"type": "FEE"})
    assert (res.status_code, res.json()["error"]) == (409, "batch_closed")
    assert qt_client.post(f"/internal/imports/{batch}/reparse", headers=headers(settings)).status_code == 409


def test_service_token_required(qt_client, settings):
    res = qt_client.post(f"/internal/imports/{uuid.uuid4()}/reparse",
                         headers={"X-Service-Token": "x" * 40, "X-User-Id": str(USER_A)})
    assert res.status_code == 401


def test_target_account_survives_reparse(qt_client, settings, owner):
    res = qt_client.post("/internal/imports", headers=headers(settings),
                         data={"broker": "questrade", "account_id": str(ACCOUNT_A_QT)},
                         files={"file": ("a.csv", csv_bytes(ROWS[3:]), "text/csv")})
    batch = res.json()["batch_id"]
    res = qt_client.post(f"/internal/imports/{batch}/reparse", headers=headers(settings))
    assert res.json()["counts"] == {"ok": 1, "skipped": 1}


def test_security_of_a_listed_symbol_is_not_remapped(qt_client, settings, owner):
    listed = f"{D},{D},Buy,QZGR.TO,SYNTHETIC GROWTH ETF WE ACTED AS AGENT,1,25,-25,0,-25,CAD,90000001,Trades,Individual TFSA"
    batch = upload(qt_client, settings, csv_bytes([listed]))["batch_id"]
    res = patch(qt_client, settings, batch, 2, {"security": {"symbol": "QZXX", "exchange": "TSX"}})
    assert (res.status_code, res.json()["error"]) == (422, "not_applicable")
