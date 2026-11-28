"""When runs happen (D-3): every 30 minutes from 09:30 to 16:30 America/Toronto, and a close snapshot at 17:15, on
days when the TSX or NYSE trades. Pure functions of the time, so tests inject the clock."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime, time, timedelta

from . import calendar
from .calendar import TZ

FIRST, LAST, CLOSE = time(9, 30), time(16, 30), time(17, 15)
INTERVAL = timedelta(minutes=30)


@dataclass(frozen=True)
class Slot:
    at: datetime
    kind: str  # intraday | close


def trading_day(d: date) -> bool:
    return any(calendar.is_trading_day(d, x) for x in calendar.EXCHANGES)


def slots(d: date) -> list[Slot]:
    if not trading_day(d):
        return []
    out, t = [], datetime.combine(d, FIRST, TZ)
    while t.timetz() <= LAST.replace(tzinfo=TZ):
        out.append(Slot(t, "intraday"))
        t += INTERVAL
    out.append(Slot(datetime.combine(d, CLOSE, TZ), "close"))
    return out


def next_slot(now: datetime) -> Slot:
    """The first slot strictly after `now`."""
    d = now.astimezone(TZ).date()
    for _ in range(15):
        for s in slots(d):
            if s.at > now:
                return s
        d += timedelta(days=1)
    raise ValueError("no slot in the next 15 days")


def due_slot(now: datetime) -> Slot | None:
    """The latest slot at or before `now` today, if any: what a run started now catches up on."""
    due = [s for s in slots(now.astimezone(TZ).date()) if s.at <= now]
    return due[-1] if due else None


def market_open(now: datetime) -> bool:
    return any(calendar.is_open(now, x) for x in calendar.EXCHANGES)
