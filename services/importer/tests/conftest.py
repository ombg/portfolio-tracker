"""Test setup. scripts/importer-test.sh provides a migrated Postgres, the pt_importer login and OWNER_DSN (the owner,
used only to seed synthetic fixtures, D-20)."""

from __future__ import annotations

import os
import uuid
from dataclasses import replace

import psycopg
import pytest
from fastapi.testclient import TestClient

from importer.app import create_app
from importer.config import Settings
from importer.parsers.base import ParseContext, ParseError, Row

USER_A = uuid.UUID("00000000-0000-0000-0000-00000000000a")
USER_B = uuid.UUID("00000000-0000-0000-0000-00000000000b")
ACCOUNT_A_QT = uuid.UUID("20000000-0000-0000-0000-0000000000a1")
ACCOUNT_A_WS = uuid.UUID("20000000-0000-0000-0000-0000000000a2")
ACCOUNT_B_QT = uuid.UUID("20000000-0000-0000-0000-0000000000b1")


class FakeParser:
    """One row per line. A line `error` or `warning` gives that status; content `unreadable` fails the whole file."""

    broker = "questrade"
    name = "fake-questrade"
    version = "0.1"

    def __init__(self) -> None:
        self.contexts: list[ParseContext] = []

    def parse(self, data: bytes, filename: str, ctx: ParseContext) -> list[Row]:
        self.contexts.append(ctx)
        text = data.decode()
        if text.strip() == "unreadable":
            raise ParseError("not a Questrade activities export")
        rows = []
        for i, line in enumerate(text.splitlines(), start=1):
            status = line if line in ("error", "warning") else "ok"
            msgs = [{"level": status, "text": f"synthetic {status}"}] if status != "ok" else []
            rows.append(Row(row_no=i, raw={"line": line}, status=status, normalized={"n": i}, messages=msgs))
        return rows


@pytest.fixture(scope="session")
def base_settings() -> Settings:
    return Settings.from_env()


@pytest.fixture
def settings(base_settings: Settings, tmp_path) -> Settings:
    return replace(base_settings, files_dir=tmp_path / "files", max_upload_bytes=1024)


@pytest.fixture
def owner():
    with psycopg.connect(os.environ["OWNER_DSN"], autocommit=True) as conn:
        yield conn


@pytest.fixture
def seeded(owner) -> None:
    owner.execute("TRUNCATE core.users, core.securities CASCADE")
    owner.execute(
        """INSERT INTO core.users (id, email, password_hash, display_name)
           VALUES (%s, 'a@example.test', 'x', 'User A'), (%s, 'b@example.test', 'x', 'User B')""",
        (USER_A, USER_B),
    )
    owner.execute(
        """INSERT INTO core.portfolios (id, user_id, name) VALUES
           ('10000000-0000-0000-0000-00000000000a', %s, 'Main'), ('10000000-0000-0000-0000-00000000000b', %s, 'Main')""",
        (USER_A, USER_B),
    )
    owner.execute(
        """INSERT INTO core.accounts (id, user_id, portfolio_id, broker_id, external_number, name, type, is_registered,
                                      base_currency)
           SELECT v.id, v.u, p.id, b.id, v.num, v.name, 'TFSA', true, 'CAD'
           FROM (VALUES (%s::uuid, %s::uuid, 'questrade', '90000001', 'A QT TFSA'),
                        (%s::uuid, %s::uuid, 'wealthsimple', 'WS0001CAD', 'A WS TFSA'),
                        (%s::uuid, %s::uuid, 'questrade', '90000002', 'B QT TFSA')) v(id, u, broker, num, name)
           JOIN core.brokers b ON b.code = v.broker
           JOIN core.portfolios p ON p.user_id = v.u""",
        (ACCOUNT_A_QT, USER_A, ACCOUNT_A_WS, USER_A, ACCOUNT_B_QT, USER_B),
    )


@pytest.fixture
def parser() -> FakeParser:
    return FakeParser()


@pytest.fixture
def client(settings: Settings, parser: FakeParser, seeded) -> TestClient:
    return TestClient(create_app(settings, {"questrade": parser}))
