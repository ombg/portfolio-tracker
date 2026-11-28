"""Refresh runs (FR-6, D-3, D-39): intraday quotes, the close snapshot, and history backfill. Each run writes one
market.refresh_runs row with its status: success (every symbol), partial (some failed), failed (none refreshed)."""

from __future__ import annotations

import logging
import threading
from dataclasses import dataclass, field
from datetime import date, datetime, timedelta
from typing import Callable

from . import calendar, schedule
from .calendar import TZ
from .model import NotFound, ProviderError
from .providers.boc import BankOfCanada
from .providers.yahoo import Yahoo
from .store import Listed, Store
from .symbols import yahoo_symbol

log = logging.getLogger("marketdata")
CLOSE_LOOKBACK = timedelta(days=10)  # the close snapshot refetches recent days, so a missed day heals itself
RETRY_FAILED_BACKFILL = timedelta(days=1)


@dataclass
class Summary:
    kind: str
    status: str = "success"
    ok: int = 0
    failed: dict[str, str] = field(default_factory=dict)

    def finish(self) -> Summary:
        self.status = "success" if not self.failed else "partial" if self.ok else "failed"
        return self


def exchange_of(item: Listed) -> str:
    return "TSX" if item.exchange in ("TSX", "TSXV", "NEO", "CSE") else "US"


def close_time(exchange: str) -> Callable[[date], datetime]:
    return lambda d: datetime.combine(d, calendar.CLOSE, TZ)


class Refresher:
    def __init__(self, store: Store, yahoo: Yahoo, boc: BankOfCanada, now: Callable[[], datetime],
                 intraday_days_kept: int = 7):
        self.store, self.yahoo, self.boc, self.now = store, yahoo, boc, now
        self.keep = timedelta(days=intraday_days_kept)
        self.lock = threading.Lock()  # runs never overlap

    def run(self, kind: str) -> Summary:
        with self.lock:
            started = self.now()
            run_id = self.store.start_run(kind, started)
            try:
                summary = {"intraday": self._intraday, "close": self._close, "backfill": self._backfill}[kind]()
            except Exception as e:  # the database or a bug: the run is failed, the scheduler goes on
                log.exception("%s run failed", kind)
                summary = Summary(kind, failed={"*": f"{type(e).__name__}"}).finish()
            nxt = schedule.next_slot(self.now()).at if kind != "backfill" else None
            self.store.finish_run(run_id, self.now(), summary.status, summary.ok, summary.failed, nxt)
            log.info("%s run: %s, %d ok, %d failed", kind, summary.status, summary.ok, len(summary.failed))
            return summary

    def _priced(self, held_only: bool) -> tuple[list[tuple[Listed, str]], dict[str, str]]:
        """Securities with a Yahoo symbol; the rest are reported (a listing without an exchange cannot be priced)."""
        out, skipped = [], {}
        for item in self.store.universe():
            if held_only and not item.held:
                continue
            ys = yahoo_symbol(item.symbol or "", item.exchange)
            if ys is None:
                skipped[item.symbol or str(item.security_id)] = "cannot be priced: no listed symbol and exchange"
                continue
            out.append((item, ys))
        return out, skipped

    def _intraday(self) -> Summary:
        s = Summary("intraday")
        items, s.failed = self._priced(held_only=True)
        res = self.yahoo.quotes([ys for _, ys in items])
        by_symbol = {ys: item for item, ys in items}
        self.store.save_quotes([(by_symbol[ys].security_id, q) for ys, q in res.ok.items()],
                               lambda at: at.astimezone(TZ).date())
        s.ok += len(res.ok)
        s.failed.update(res.failed)
        for base in self._foreign():
            try:
                q = self.yahoo.fx_quote(base, "CAD")
                self.store.save_fx_quote(base, "CAD", q, q.observed_at.astimezone(TZ).date())
                s.ok += 1
            except ProviderError as e:
                s.failed[f"{base}CAD=X"] = str(e)
        return s.finish()

    def _close(self) -> Summary:
        s = Summary("close")
        today = self.now().astimezone(TZ).date()
        items, s.failed = self._priced(held_only=True)
        for item, ys in items:
            try:
                h = self.yahoo.history(ys, today - CLOSE_LOOKBACK, today)
            except ProviderError as e:
                s.failed[ys] = f"not found: {e}" if isinstance(e, NotFound) else str(e)
                continue
            self.store.save_history(item.security_id, h.currency, h.source, h.closes, close_time(exchange_of(item)),
                                    h.dividends, h.splits)
            s.ok += 1
        for base in self._foreign():
            try:
                self.store.save_daily_fx(self.boc.rates(base, today - CLOSE_LOOKBACK, today))
                s.ok += 1
            except ProviderError as e:
                s.failed[f"BoC FX{base}CAD"] = str(e)
        self.store.prune_intraday(self.now() - self.keep)
        return s.finish()

    def _backfill(self) -> Summary:
        """Daily history from each security's first trade date (D-39), and Bank of Canada rates from the first
        foreign-currency ledger row (D-6). Only what is missing is fetched."""
        s = Summary("backfill")
        now = self.now()
        today = now.astimezone(TZ).date()
        state = self.store.backfills()
        items, _ = self._priced(held_only=False)  # unpriceable ones are reported by the scheduled runs
        for item, ys in items:
            st = state.get(item.security_id)
            if st and st["from_date"] <= item.first_trade_date and not st["error"]:
                continue
            if st and st["error"] and now - st["fetched_at"] < RETRY_FAILED_BACKFILL:
                continue
            try:
                h = self.yahoo.history(ys, item.first_trade_date, today)
            except ProviderError as e:
                msg = f"not found: {e}" if isinstance(e, NotFound) else str(e)
                s.failed[ys] = msg
                self.store.set_backfill(item.security_id, item.first_trade_date, today, now, msg)
                continue
            self.store.save_history(item.security_id, h.currency, h.source, h.closes, close_time(exchange_of(item)),
                                    h.dividends, h.splits)
            self.store.set_backfill(item.security_id, item.first_trade_date, today, now, None)
            s.ok += 1
        for base, first in self.store.fx_start().items():
            have = self.store.earliest_daily_fx(base)
            if have is not None and have <= first + timedelta(days=4):  # weekends and holidays have no rate
                continue
            try:
                self.store.save_daily_fx(self.boc.rates(base, first - timedelta(days=7), today))
                s.ok += 1
            except (ProviderError, ValueError) as e:
                s.failed[f"BoC FX{base}CAD"] = str(e)
        return s.finish()

    def _foreign(self) -> list[str]:
        return sorted(c for c in self.store.fx_start() if c != "CAD")
