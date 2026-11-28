"""What market-data reads and writes, as pt_market (D-39): the universe through market.universe() and
market.fx_start(), everything else in the market schema."""

from __future__ import annotations

import json
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from datetime import date, datetime
from typing import Any
from uuid import UUID

import psycopg
from psycopg.rows import dict_row

from .model import Close, Dividend, FxRate, Quote, Split


class UnsafeRoleError(RuntimeError):
    """The configured role could read every user's data (superuser or BYPASSRLS)."""


@dataclass(frozen=True)
class Listed:
    security_id: UUID
    symbol: str | None
    exchange: str | None
    trading_currency: str
    first_trade_date: date
    held: bool


class Store:
    def __init__(self, db: dict[str, str]):
        self.db = db

    @contextmanager
    def tx(self) -> Iterator[psycopg.Connection]:
        conn = psycopg.connect(**self.db, row_factory=dict_row, connect_timeout=5)
        try:
            with conn.transaction():
                yield conn
        finally:
            conn.close()

    def check_role(self) -> None:
        with self.tx() as c:
            row = c.execute("SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").fetchone()
        if row is None or row["rolsuper"] or row["rolbypassrls"]:
            raise UnsafeRoleError("market-data must connect as pt_market, which cannot read user data (D-39)")

    # -- what to fetch ----------------------------------------------------------------------------------------

    def universe(self) -> list[Listed]:
        with self.tx() as c:
            rows = c.execute("SELECT * FROM market.universe() ORDER BY symbol NULLS LAST, security_id").fetchall()
        return [Listed(**r) for r in rows]

    def fx_start(self) -> dict[str, date]:
        with self.tx() as c:
            return {r["currency"]: r["first_date"] for r in c.execute("SELECT * FROM market.fx_start()")}

    def backfills(self) -> dict[UUID, dict[str, Any]]:
        with self.tx() as c:
            return {r["security_id"]: r for r in c.execute("SELECT * FROM market.backfills")}

    def earliest_daily_fx(self, base: str, source: str = "boc") -> date | None:
        with self.tx() as c:
            row = c.execute("SELECT min(rate_date) AS d FROM market.fx_rates WHERE base = %s AND quote = 'CAD' "
                            "AND kind = 'daily' AND source = %s", (base, source)).fetchone()
        return row["d"]

    # -- writes -----------------------------------------------------------------------------------------------

    def save_quotes(self, quotes: list[tuple[UUID, Quote]], trade_date_of) -> None:
        with self.tx() as c:
            for sid, q in quotes:
                c.execute("""INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency,
                             source) VALUES (%s, 'intraday', %s, %s, %s, %s, %s) ON CONFLICT DO NOTHING""",
                          (sid, trade_date_of(q.observed_at), q.observed_at, q.price, q.currency, q.source))

    def save_history(self, sid: UUID, currency: str, source: str, closes: list[Close], close_at,
                     dividends: list[Dividend], splits: list[Split]) -> None:
        with self.tx() as c:
            for cl in closes:
                c.execute("""INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency,
                             source) VALUES (%s, 'close', %s, %s, %s, %s, %s)
                             ON CONFLICT (security_id, trade_date, source) WHERE kind = 'close'
                             DO UPDATE SET price = EXCLUDED.price, observed_at = EXCLUDED.observed_at,
                                           fetched_at = now()""",
                          (sid, cl.day, close_at(cl.day), cl.price, currency, source))
            for d in dividends:
                c.execute("""INSERT INTO market.dividend_events (security_id, ex_date, amount, currency, source)
                             VALUES (%s, %s, %s, %s, %s) ON CONFLICT (security_id, ex_date, source)
                             DO UPDATE SET amount = EXCLUDED.amount, fetched_at = now()""",
                          (sid, d.ex_date, d.amount, d.currency or currency, source))
            for s in splits:
                c.execute("""INSERT INTO market.split_events (security_id, day, ratio, source)
                             VALUES (%s, %s, %s, %s) ON CONFLICT (security_id, day, source)
                             DO UPDATE SET ratio = EXCLUDED.ratio, fetched_at = now()""", (sid, s.day, s.ratio, source))

    def save_daily_fx(self, rates: list[FxRate]) -> None:
        with self.tx() as c:
            for r in rates:
                c.execute("""INSERT INTO market.fx_rates (base, quote, kind, rate_date, observed_at, rate, source)
                             VALUES (%s, %s, 'daily', %s, %s, %s, %s)
                             ON CONFLICT (base, quote, rate_date, source) WHERE kind = 'daily'
                             DO UPDATE SET rate = EXCLUDED.rate, observed_at = EXCLUDED.observed_at,
                                           fetched_at = now()""",
                          (r.base, r.quote, r.day, r.observed_at, r.rate, r.source))

    def save_fx_quote(self, base: str, quote: str, q: Quote, trade_date: date) -> None:
        with self.tx() as c:
            c.execute("""INSERT INTO market.fx_rates (base, quote, kind, rate_date, observed_at, rate, source)
                         VALUES (%s, %s, 'intraday', %s, %s, %s, %s) ON CONFLICT DO NOTHING""",
                      (base, quote, trade_date, q.observed_at, q.price, q.source))

    def set_backfill(self, sid: UUID, from_date: date, through: date, at: datetime, error: str | None) -> None:
        with self.tx() as c:
            c.execute("""INSERT INTO market.backfills (security_id, from_date, through_date, fetched_at, error)
                         VALUES (%s, %s, %s, %s, %s) ON CONFLICT (security_id) DO UPDATE SET
                         from_date = EXCLUDED.from_date, through_date = EXCLUDED.through_date,
                         fetched_at = EXCLUDED.fetched_at, error = EXCLUDED.error""",
                      (sid, from_date, through, at, error))

    def prune_intraday(self, before: datetime) -> int:
        with self.tx() as c:
            n = c.execute("DELETE FROM market.prices WHERE kind = 'intraday' AND observed_at < %s", (before,)).rowcount
            c.execute("DELETE FROM market.fx_rates WHERE kind = 'intraday' AND observed_at < %s", (before,))
        return n

    # -- runs -------------------------------------------------------------------------------------------------

    def start_run(self, kind: str, at: datetime) -> UUID:
        with self.tx() as c:
            return c.execute("INSERT INTO market.refresh_runs (kind, started_at, status) VALUES (%s, %s, 'running') "
                             "RETURNING id", (kind, at)).fetchone()["id"]

    def finish_run(self, run_id: UUID, at: datetime, status: str, ok: int, failed: dict[str, str],
                   next_run_at: datetime | None) -> None:
        with self.tx() as c:
            c.execute("""UPDATE market.refresh_runs SET finished_at = %s, status = %s, symbols_ok = %s,
                         symbols_failed = %s, errors = %s, next_run_at = %s WHERE id = %s""",
                      (at, status, ok, len(failed), json.dumps(failed), next_run_at, run_id))

    def runs(self, limit: int = 10) -> list[dict[str, Any]]:
        with self.tx() as c:
            return c.execute("SELECT * FROM market.refresh_runs ORDER BY started_at DESC LIMIT %s",
                             (limit,)).fetchall()

    def last_started(self, kind: str) -> datetime | None:
        with self.tx() as c:
            row = c.execute("SELECT max(started_at) AS t FROM market.refresh_runs WHERE kind = %s",
                            (kind,)).fetchone()
        return row["t"]
