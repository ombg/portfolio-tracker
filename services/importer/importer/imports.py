"""Upload handling: store the original, register it, parse it, stage the rows (FR-3, D-8, D-12, D-22)."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from typing import Any
from uuid import UUID

import psycopg
from psycopg.types.json import Jsonb

from . import storage
from .config import Settings
from .db import user_tx
from .parsers.base import ROW_STATUSES, ParseContext, ParseError, Parser, Row, TrackedAccount

# A file whose latest batch has one of these statuses counts as imported; failed or discarded batches may be retried
# with the same file (D-8).
LIVE_BATCH_STATUSES = ("parsed", "needs_review", "committed")


class ImportError_(Exception):
    """A request the importer refuses, with a stable error code (`{"error", "message"}`)."""

    def __init__(self, status: int, code: str, message: str, **extra: Any):
        super().__init__(message)
        self.status, self.code, self.message, self.extra = status, code, message, extra


@dataclass(frozen=True)
class Upload:
    user_id: UUID
    broker: str
    account_id: UUID | None
    filename: str
    media_type: str
    data: bytes


def import_file(settings: Settings, parser: Parser, up: Upload) -> dict[str, Any]:
    sha = storage.sha256_hex(up.data)
    with user_tx(settings, up.user_id) as conn:
        broker = conn.execute("SELECT id FROM core.brokers WHERE code = %s", (up.broker,)).fetchone()
        if broker is None:
            raise ImportError_(422, "unknown_broker", f"unknown broker {up.broker!r}")
        broker_id = broker["id"]

        if up.account_id is not None:
            # RLS hides other users' accounts, so they answer exactly like missing ones.
            acct = conn.execute("SELECT broker_id FROM core.accounts WHERE id = %s", (up.account_id,)).fetchone()
            if acct is None:
                raise ImportError_(422, "unknown_account", "account not found")
            if acct["broker_id"] != broker_id:
                raise ImportError_(422, "account_broker_mismatch", "the account belongs to another broker")

        source_file_id = _register_file(settings, conn, sha, up)

        accounts = tuple(
            TrackedAccount(**r)
            for r in conn.execute(
                """SELECT id, external_number, name, type, base_currency FROM core.accounts
                   WHERE broker_id = %s ORDER BY name""",
                (broker_id,),
            ).fetchall()
        )
        # RLS returns global aliases and the user's own; ordering puts the user's last so they win.
        aliases = {
            r["alias"]: r["security_id"]
            for r in conn.execute(
                """SELECT alias, security_id FROM core.security_aliases WHERE broker_id = %s
                   ORDER BY user_id NULLS FIRST""",
                (broker_id,),
            ).fetchall()
        }
        ctx = ParseContext(broker=up.broker, accounts=accounts, target_account_id=up.account_id, aliases=aliases)
        try:
            rows = parser.parse(up.data, up.filename, ctx)
            _check_rows(rows)
            counts: dict[str, Any] = dict(Counter(r.status for r in rows))
            status = "needs_review" if counts.get("error") or counts.get("warning") else "parsed"
        except ParseError as e:
            rows, counts, status = [], {"failure": str(e)}, "failed"

        batch_id = conn.execute(
            """INSERT INTO imports.import_batches
                   (user_id, source_file_id, broker_id, account_id, parser, parser_version, status, counts)
               VALUES (%s, %s, %s, %s, %s, %s, %s, %s) RETURNING id""",
            (up.user_id, source_file_id, broker_id, up.account_id, parser.name, parser.version, status, Jsonb(counts)),
        ).fetchone()["id"]
        with conn.cursor() as cur:
            cur.executemany(
                """INSERT INTO imports.import_rows (batch_id, row_no, user_id, raw, normalized, status, messages)
                   VALUES (%s, %s, %s, %s, %s, %s, %s)""",
                [
                    (batch_id, r.row_no, up.user_id, Jsonb(r.raw),
                     Jsonb(r.normalized) if r.normalized is not None else None, r.status, Jsonb(r.messages))
                    for r in rows
                ],
            )
    return {"batch_id": str(batch_id), "source_file_id": str(source_file_id), "status": status, "counts": counts}


def _register_file(settings: Settings, conn: psycopg.Connection, sha: str, up: Upload) -> UUID:
    existing = conn.execute(
        "SELECT id FROM imports.source_files WHERE sha256 = %s AND deleted_at IS NULL", (sha,)
    ).fetchone()
    if existing is not None:
        live = conn.execute(
            """SELECT id FROM imports.import_batches WHERE source_file_id = %s AND status = ANY(%s)
               ORDER BY created_at DESC LIMIT 1""",
            (existing["id"], list(LIVE_BATCH_STATUSES)),
        ).fetchone()
        if live is not None:
            raise ImportError_(
                409, "duplicate_file", "this file was already imported",
                batch_id=str(live["id"]), source_file_id=str(existing["id"]),
            )
        return existing["id"]

    # Written before the row: if the transaction fails the file stays as an unreferenced, content-addressed blob
    # that the next upload of the same bytes reuses.
    _, rel = storage.store(settings.files_dir, up.data, up.filename)
    try:
        with conn.transaction():  # savepoint, so a lost race leaves the outer transaction usable
            return conn.execute(
                """INSERT INTO imports.source_files (user_id, sha256, original_name, size, media_type, stored_path)
                   VALUES (%s, %s, %s, %s, %s, %s) RETURNING id""",
                (up.user_id, sha, up.filename, len(up.data), up.media_type, rel),
            ).fetchone()["id"]
    except psycopg.errors.UniqueViolation:
        raise ImportError_(409, "duplicate_file", "this file is being imported already") from None


def _check_rows(rows: list[Row]) -> None:
    """Parser contract: unique positive row numbers and known statuses. A violation is a bug, not user input."""
    seen: set[int] = set()
    for r in rows:
        if r.row_no < 1 or r.row_no in seen:
            raise RuntimeError(f"parser produced invalid or repeated row number {r.row_no}")
        if r.status not in ROW_STATUSES:
            raise RuntimeError(f"parser produced unknown row status {r.status!r}")
        seen.add(r.row_no)
