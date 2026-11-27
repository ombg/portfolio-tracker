"""Database access as pt_importer with app.user_id set per transaction, so RLS applies (D-1, D-35)."""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from uuid import UUID

import psycopg
from psycopg.rows import dict_row

from .config import Settings


class UnsafeRoleError(RuntimeError):
    """The configured database role could bypass row-level security."""


def connect(settings: Settings) -> psycopg.Connection:
    conn = psycopg.connect(**settings.db, row_factory=dict_row, connect_timeout=5)
    row = conn.execute(
        "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user"
    ).fetchone()
    if row is None or row["rolsuper"] or row["rolbypassrls"]:
        conn.close()
        raise UnsafeRoleError("the importer's database role must not bypass row-level security (D-35)")
    conn.rollback()
    return conn


@contextmanager
def user_tx(settings: Settings, user_id: UUID) -> Iterator[psycopg.Connection]:
    """One transaction scoped to user_id; commits on success, rolls back on any exception."""
    conn = connect(settings)
    try:
        with conn.transaction():
            conn.execute("SELECT set_config('app.user_id', %s, true)", (str(user_id),))
            yield conn
    finally:
        conn.close()
