"""Upload handling: store the original, register it, parse it, stage the rows (FR-3, D-8, D-12, D-22). Preview
corrections: rules and row edits, applied by parsing the stored original again (D-36)."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from typing import Any
from uuid import UUID

import re

import psycopg
from psycopg.types.json import Jsonb

from . import storage
from .config import Settings
from .db import user_tx
from .parsers.base import (
    EXCHANGE_CURRENCY, LEDGER_TYPES, ROW_STATUSES, ParseContext, ParseError, Parser, Row, TrackedAccount,
)

# A file whose latest batch has one of these statuses counts as imported; failed or discarded batches may be retried
# with the same file (D-8).
LIVE_BATCH_STATUSES = ("parsed", "needs_review", "committed")
# Batches the user can still correct, discard or commit.
OPEN_BATCH_STATUSES = ("parsed", "needs_review")
LISTED_SYMBOL = re.compile(r"[A-Z][A-Z0-9]*(\.[A-Z0-9]+)*")
# Messages that mark a row whose action or security the user may set by a rule (D-36).
ACTION_FIXABLE = {"unmapped_action", "action_rule"}
SECURITY_FIXABLE = {"unknown_broker_code", "proposed_security", "unknown_symbol", "symbol_rule"}


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

        ctx = _context(conn, broker_id, up.broker, up.account_id)
        try:
            rows = parser.parse(up.data, up.filename, ctx)
            _check_rows(rows)
            counts, status = _summary(rows)
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


def _context(conn: psycopg.Connection, broker_id: UUID, broker: str, target: UUID | None) -> ParseContext:
    """What a parser may know: the user's accounts at the broker, aliases and the user's import rules."""
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
    action_rules: dict[str, str] = {}
    symbol_rules: dict[str, tuple[str, str]] = {}
    for r in conn.execute(
        "SELECT kind, source_key, target FROM imports.import_rules WHERE broker_id = %s", (broker_id,)
    ).fetchall():
        if r["kind"] == "action":
            action_rules[r["source_key"]] = r["target"]["type"]
        else:
            symbol_rules[r["source_key"]] = (r["target"]["symbol"], r["target"]["exchange"])
    return ParseContext(broker=broker, accounts=accounts, target_account_id=target, aliases=aliases,
                        action_rules=action_rules, symbol_rules=symbol_rules)


def _summary(rows: list[Row]) -> tuple[dict[str, Any], str]:
    counts: dict[str, Any] = dict(Counter(r.status for r in rows))
    return counts, "needs_review" if counts.get("error") or counts.get("warning") else "parsed"


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


# ---------------------------------------------------------------------------------------------------------------
# Preview corrections (D-36)


@dataclass(frozen=True)
class RowEdit:
    type: str | None = None  # ledger type for the row's action, saved as an action rule
    security: tuple[str, str] | None = None  # (symbol, exchange) for the row's symbol or code, saved as a symbol rule
    excluded: bool | None = None  # leave the row out of the commit, or take it back in


def reparse(settings: Settings, parsers: dict[str, Parser], user_id: UUID, batch_id: UUID) -> dict[str, Any]:
    """Parses the stored original again with the current accounts, aliases and rules (after the user created an
    account, for example) and restages every row in place."""
    with user_tx(settings, user_id) as conn:
        return _reparse(settings, conn, parsers, _open_batch(conn, batch_id))


def edit_row(settings: Settings, parsers: dict[str, Parser], user_id: UUID, batch_id: UUID, row_no: int,
             edit: RowEdit) -> dict[str, Any]:
    """Saves the fix as a rule (reusable) or a row edit, then re-parses the batch so every row it touches changes."""
    with user_tx(settings, user_id) as conn:
        b = _open_batch(conn, batch_id)
        row = conn.execute(
            "SELECT raw, normalized, status, messages, user_edits FROM imports.import_rows "
            "WHERE batch_id = %s AND row_no = %s",
            (batch_id, row_no),
        ).fetchone()
        if row is None:
            raise ImportError_(404, "not_found", "row not found")
        keys = _parser(parsers, b).rule_keys(row["raw"])
        codes = {m.get("code") for m in row["messages"]}

        if edit.type is not None:
            if edit.type not in LEDGER_TYPES:
                raise ImportError_(422, "invalid_type", f"{edit.type!r} is not a transaction type")
            if not keys.action or not codes & ACTION_FIXABLE:
                raise ImportError_(422, "not_applicable", "this row's action already has a mapping")
            _save_rule(conn, user_id, b["broker_id"], "action", keys.action, {"type": edit.type})

        if edit.security is not None:
            symbol, exchange = edit.security[0].strip().upper(), edit.security[1].strip().upper()
            if exchange not in EXCHANGE_CURRENCY or not LISTED_SYMBOL.fullmatch(symbol):
                raise ImportError_(422, "invalid_security",
                                   f"expected a listed symbol and one of {', '.join(EXCHANGE_CURRENCY)}")
            sec = (row["normalized"] or {}).get("security") or {}
            unknown_listing = bool(sec) and sec.get("exchange") is None and sec.get("security_type") != "gic"
            if not keys.symbol or not (codes & SECURITY_FIXABLE or unknown_listing):
                raise ImportError_(422, "not_applicable", "this row's security is already known")
            _save_rule(conn, user_id, b["broker_id"], "symbol", keys.symbol, {"symbol": symbol, "exchange": exchange})

        if edit.excluded is not None:
            if row["status"] == "skipped":
                raise ImportError_(422, "not_applicable", "rows of untracked or other accounts are skipped already")
            edits = dict(row["user_edits"] or {})
            if edit.excluded:
                edits["excluded"] = True
            else:
                edits.pop("excluded", None)
            conn.execute(
                "UPDATE imports.import_rows SET user_edits = %s WHERE batch_id = %s AND row_no = %s",
                (Jsonb(edits) if edits else None, batch_id, row_no),
            )
        return _reparse(settings, conn, parsers, b)


def _open_batch(conn: psycopg.Connection, batch_id: UUID) -> dict[str, Any]:
    b = conn.execute(
        """SELECT b.id, b.status, b.parser, b.account_id, b.broker_id, br.code AS broker,
                  f.stored_path, f.sha256, f.original_name
           FROM imports.import_batches b
           JOIN core.brokers br ON br.id = b.broker_id
           JOIN imports.source_files f ON f.id = b.source_file_id
           WHERE b.id = %s
           FOR UPDATE OF b""",
        (batch_id,),
    ).fetchone()
    if b is None:  # RLS: another user's batch answers exactly like a missing one
        raise ImportError_(404, "not_found", "import not found")
    if b["status"] not in OPEN_BATCH_STATUSES:
        raise ImportError_(409, "batch_closed", f"the import is {b['status']} and can no longer change")
    return b


def _parser(parsers: dict[str, Parser], b: dict[str, Any]) -> Parser:
    parser = parsers.get(b["broker"])
    if parser is None or parser.name != b["parser"]:
        raise ImportError_(409, "parser_unavailable", f"parser {b['parser']} is not available any more")
    return parser


def _save_rule(conn: psycopg.Connection, user_id: UUID, broker_id: UUID, kind: str, key: str,
               target: dict[str, str]) -> None:
    conn.execute(
        """INSERT INTO imports.import_rules (user_id, broker_id, kind, source_key, target) VALUES (%s, %s, %s, %s, %s)
           ON CONFLICT ON CONSTRAINT import_rules_key DO UPDATE SET target = EXCLUDED.target, updated_at = now()""",
        (user_id, broker_id, kind, key, Jsonb(target)),
    )


def _reparse(settings: Settings, conn: psycopg.Connection, parsers: dict[str, Parser],
             b: dict[str, Any]) -> dict[str, Any]:
    parser = _parser(parsers, b)
    data = storage.load(settings.files_dir, b["stored_path"], b["sha256"])
    ctx = _context(conn, b["broker_id"], b["broker"], b["account_id"])
    try:
        rows = parser.parse(data, b["original_name"], ctx)
    except ParseError as e:  # it parsed when uploaded; failing now is a bug, not user input
        raise RuntimeError(f"stored file of batch {b['id']} no longer parses: {e}") from e
    _check_rows(rows)
    edits = {
        r["row_no"]: r["user_edits"]
        for r in conn.execute("SELECT row_no, user_edits FROM imports.import_rows WHERE batch_id = %s", (b["id"],))
    }
    if set(edits) != {r.row_no for r in rows}:
        raise RuntimeError(f"re-parsing batch {b['id']} gave different rows")
    for r in rows:
        if (edits[r.row_no] or {}).get("excluded") and r.status != "skipped":
            r.status = "excluded"
            r.messages.append({"level": "info", "code": "excluded", "text": "left out by you"})
    counts, status = _summary(rows)
    with conn.cursor() as cur:
        cur.executemany(
            """UPDATE imports.import_rows SET normalized = %s, status = %s, messages = %s
               WHERE batch_id = %s AND row_no = %s""",
            [(Jsonb(r.normalized) if r.normalized is not None else None, r.status, Jsonb(r.messages), b["id"], r.row_no)
             for r in rows],
        )
    conn.execute(
        "UPDATE imports.import_batches SET status = %s, counts = %s, parser_version = %s WHERE id = %s",
        (status, Jsonb(counts), parser.version, b["id"]),
    )
    return {"batch_id": str(b["id"]), "status": status, "counts": counts}
