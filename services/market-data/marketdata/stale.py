"""Stale prices (D-3): older than two refresh intervals during market hours, otherwise older than the latest close
(less the same two intervals, so the last intraday price before the bell still counts until the close snapshot)."""

from __future__ import annotations

from datetime import datetime, timedelta

from . import calendar

INTERVAL = timedelta(minutes=30)


def reference_time(now: datetime, exchange: str) -> datetime:
    """The time a fresh price would have been observed at: now while the exchange is open, else its latest close."""
    return now if calendar.is_open(now, exchange) else calendar.latest_close(now, exchange)


def is_stale(observed_at: datetime, now: datetime, exchange: str, interval: timedelta = INTERVAL) -> bool:
    return observed_at < reference_time(now, exchange) - 2 * interval
