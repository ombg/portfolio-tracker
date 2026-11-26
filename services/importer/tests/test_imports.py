"""POST /internal/imports: originals kept, duplicates refused, rows staged per user (FR-3, D-8, D-12, D-35)."""

from __future__ import annotations

import hashlib
import os
import stat
import uuid

import pytest
from fastapi.testclient import TestClient

from importer import db
from importer.app import create_app
from importer.config import Settings
from importer.parsers.base import Row

from .conftest import ACCOUNT_A_QT, ACCOUNT_A_WS, ACCOUNT_B_QT, USER_A, USER_B

CONTENT = b"buy\nwarning\nsell\n"


def upload(client, settings, *, user=USER_A, data=CONTENT, name="activities.csv", broker="questrade",
           account=None, token=None):
    headers = {"X-Service-Token": settings.service_token if token is None else token, "X-User-Id": str(user)}
    form = {"broker": broker}
    if account is not None:
        form["account_id"] = str(account)
    return client.post("/internal/imports", headers=headers, data=form, files={"file": (name, data, "text/csv")})


def count(owner, table: str) -> int:
    return owner.execute(f"SELECT count(*) FROM {table}").fetchone()[0]


@pytest.mark.parametrize("token", ["", "wrong-token-of-sufficient-length-xxxxxxxx"])
def test_service_token_required(client, settings, owner, token):
    res = upload(client, settings, token=token)
    assert res.status_code == 401
    assert res.json()["error"] == "unauthenticated"
    assert count(owner, "imports.source_files") == 0


def test_user_id_required(client, settings):
    res = client.post("/internal/imports", headers={"X-Service-Token": settings.service_token},
                      data={"broker": "questrade"}, files={"file": ("a.csv", CONTENT, "text/csv")})
    assert res.status_code == 400
    assert res.json() == {"error": "bad_request", "message": "invalid or missing: x-user-id"}


def test_upload_keeps_original_and_stages_rows(client, settings, owner, parser):
    res = upload(client, settings, account=ACCOUNT_A_QT)
    assert res.status_code == 201, res.text
    body = res.json()
    assert body["status"] == "needs_review"  # one warning row
    assert body["counts"] == {"ok": 2, "warning": 1}

    sha = hashlib.sha256(CONTENT).hexdigest()
    f = owner.execute(
        "SELECT user_id, sha256, original_name, size, media_type, stored_path FROM imports.source_files WHERE id = %s",
        (body["source_file_id"],),
    ).fetchone()
    assert f == (USER_A, sha, "activities.csv", len(CONTENT), "text/csv", f"{sha[:2]}/{sha}.csv")
    stored = settings.files_dir / f[5]
    assert stored.read_bytes() == CONTENT  # AC-2: the source file is not changed
    assert not stored.stat().st_mode & (stat.S_IWUSR | stat.S_IWGRP | stat.S_IWOTH)

    b = owner.execute(
        """SELECT user_id, source_file_id, account_id, parser, parser_version, status, committed_at
           FROM imports.import_batches WHERE id = %s""",
        (body["batch_id"],),
    ).fetchone()
    assert b == (USER_A, uuid.UUID(body["source_file_id"]), ACCOUNT_A_QT, "fake-questrade", "0.1", "needs_review", None)
    rows = owner.execute(
        "SELECT row_no, user_id, raw, normalized, status, messages FROM imports.import_rows WHERE batch_id = %s "
        "ORDER BY row_no",
        (body["batch_id"],),
    ).fetchall()
    assert rows == [
        (1, USER_A, {"line": "buy"}, {"n": 1}, "ok", []),
        (2, USER_A, {"line": "warning"}, {"n": 2}, "warning", [{"level": "warning", "text": "synthetic warning"}]),
        (3, USER_A, {"line": "sell"}, {"n": 3}, "ok", []),
    ]

    # The parser sees only this user's accounts at this broker, plus the chosen target.
    ctx = parser.contexts[-1]
    assert [a.id for a in ctx.accounts] == [ACCOUNT_A_QT]
    assert ctx.accounts[0].external_number == "90000001"
    assert ctx.target_account_id == ACCOUNT_A_QT


def test_clean_file_is_parsed(client, settings):
    res = upload(client, settings, data=b"buy\nsell\n")
    assert res.status_code == 201
    assert res.json()["status"] == "parsed"


def test_same_file_twice_is_refused_with_earlier_import(client, settings, owner):
    first = upload(client, settings).json()
    res = upload(client, settings, name="renamed.csv")
    assert res.status_code == 409
    assert res.json() == {
        "error": "duplicate_file", "message": "this file was already imported",
        "batch_id": first["batch_id"], "source_file_id": first["source_file_id"],
    }
    assert count(owner, "imports.source_files") == 1
    assert count(owner, "imports.import_batches") == 1


def test_another_user_may_import_the_same_bytes(client, settings, owner):
    a = upload(client, settings).json()
    b = upload(client, settings, user=USER_B)
    assert b.status_code == 201, b.text
    assert b.json()["source_file_id"] != a["source_file_id"]
    paths = owner.execute("SELECT DISTINCT stored_path FROM imports.source_files").fetchall()
    assert len(paths) == 1  # one stored copy, two owners


def test_failed_parse_can_be_retried_with_the_same_file(client, settings, owner):
    first = upload(client, settings, data=b"unreadable")
    assert first.status_code == 201
    assert first.json()["status"] == "failed"
    assert first.json()["counts"] == {"failure": "not a Questrade activities export"}
    retry = upload(client, settings, data=b"unreadable")
    assert retry.status_code == 201
    assert retry.json()["source_file_id"] == first.json()["source_file_id"]
    assert retry.json()["batch_id"] != first.json()["batch_id"]
    assert count(owner, "imports.import_rows") == 0


def test_discarded_import_can_be_retried(client, settings, owner):
    first = upload(client, settings).json()
    owner.execute("UPDATE imports.import_batches SET status = 'discarded' WHERE id = %s", (first["batch_id"],))
    assert upload(client, settings).status_code == 201


@pytest.mark.parametrize(
    ("account", "code"),
    [(ACCOUNT_B_QT, "unknown_account"), (uuid.uuid4(), "unknown_account"), (ACCOUNT_A_WS, "account_broker_mismatch")],
)
def test_target_account_must_be_own_and_at_the_broker(client, settings, owner, account, code):
    res = upload(client, settings, account=account)
    assert res.status_code == 422
    assert res.json()["error"] == code
    assert count(owner, "imports.source_files") == 0
    assert not settings.files_dir.exists() or not any(settings.files_dir.rglob("*.csv"))


def test_broker_without_parser(client, settings, owner):
    res = upload(client, settings, broker="wealthsimple")
    assert res.status_code == 422
    assert res.json()["error"] == "unsupported_broker"
    assert count(owner, "imports.source_files") == 0


def test_empty_and_oversized_files(client, settings, owner):
    assert upload(client, settings, data=b"").json()["error"] == "empty_file"
    res = upload(client, settings, data=b"x" * (settings.max_upload_bytes + 1))
    assert res.status_code == 413
    assert res.json()["error"] == "file_too_large"
    assert count(owner, "imports.source_files") == 0


def test_importer_sees_only_the_users_rows(client, settings):
    upload(client, settings)
    for user, want in ((USER_A, 1), (USER_B, 0)):
        with db.user_tx(settings, user) as conn:
            for table in ("imports.source_files", "imports.import_batches"):
                assert conn.execute(f"SELECT count(*) AS n FROM {table}").fetchone()["n"] == want, (table, user)
            assert conn.execute("SELECT count(*) AS n FROM imports.import_rows").fetchone()["n"] == 3 * want


def test_refuses_a_role_that_bypasses_rls(settings):
    owner_settings = Settings(
        service_token=settings.service_token, files_dir=settings.files_dir,
        db={**settings.db, "user": "portfolio", "password": os.environ["OWNER_PASSWORD"]},
    )
    with pytest.raises(db.UnsafeRoleError):
        db.connect(owner_settings)


def test_health(client):
    assert client.get("/internal/health").json() == {"status": "ok", "database": "ok"}


def test_parser_bug_is_not_staged(settings, seeded, owner):
    class Broken:
        broker, name, version = "questrade", "broken", "0"

        def parse(self, data, filename, ctx):
            return [Row(row_no=1, raw={}, status="ok"), Row(row_no=1, raw={}, status="ok")]

    client = TestClient(create_app(settings, {"questrade": Broken()}), raise_server_exceptions=False)
    assert upload(client, settings).status_code == 500
    # The whole transaction rolled back: no file row, no batch.
    assert count(owner, "imports.source_files") == 0
    assert count(owner, "imports.import_batches") == 0
