"""TSX and NYSE trading days and sessions (D-3). Rules, not a dependency: the two exchanges' published holidays,
weekend observance included. Sessions are 09:30-16:00 America/Toronto (= New York time) on both; early closes are
treated as full days, which only makes the stale rule (stale.py) slightly lenient on those afternoons."""

from __future__ import annotations

from datetime import date, datetime, time, timedelta
from functools import lru_cache
from zoneinfo import ZoneInfo

TZ = ZoneInfo("America/Toronto")
OPEN, CLOSE = time(9, 30), time(16, 0)
EXCHANGES = ("TSX", "US")

# One-off closures announced by the exchange (national days of mourning).
SPECIAL_CLOSURES = {"US": {date(2025, 1, 9)}, "TSX": set()}


def easter(year: int) -> date:
    """Gregorian Easter Sunday (anonymous algorithm)."""
    a, b, c = year % 19, year // 100, year % 100
    d, e = b // 4, b % 4
    f = (b + 8) // 25
    g = (b - f + 1) // 3
    h = (19 * a + b - d - g + 15) % 30
    i, k = c // 4, c % 4
    l = (32 + 2 * e + 2 * i - h - k) % 7
    m = (a + 11 * h + 22 * l) // 451
    month = (h + l - 7 * m + 114) // 31
    day = (h + l - 7 * m + 114) % 31 + 1
    return date(year, month, day)


def nth_weekday(year: int, month: int, weekday: int, n: int) -> date:
    """n-th (1-based) weekday (Mon=0) of a month; n=-1 is the last."""
    if n > 0:
        first = date(year, month, 1)
        return first + timedelta(days=(weekday - first.weekday()) % 7 + 7 * (n - 1))
    nxt = date(year + (month == 12), month % 12 + 1, 1)
    last = nxt - timedelta(days=1)
    return last - timedelta(days=(last.weekday() - weekday) % 7)


def _next_monday_if_weekend(d: date) -> date:
    return d + timedelta(days=(7 - d.weekday()) % 7) if d.weekday() >= 5 else d


def _nearest_weekday(d: date) -> date:  # US rule: Saturday -> Friday, Sunday -> Monday
    return d - timedelta(days=1) if d.weekday() == 5 else d + timedelta(days=1) if d.weekday() == 6 else d


def _christmas_boxing(year: int) -> set[date]:
    """Canada: Christmas and Boxing Day, each moved past the weekend and past each other."""
    out: set[date] = set()
    for d in (date(year, 12, 25), date(year, 12, 26)):
        while d.weekday() >= 5 or d in out:
            d += timedelta(days=1)
        out.add(d)
    return out


@lru_cache(maxsize=None)
def holidays(exchange: str, year: int) -> frozenset[date]:
    """Closed weekdays observed in `year`, including a holiday of the next year observed early (a Saturday 1 January
    moved to Friday 31 December would land here; NYSE does not move it, which the tests check)."""
    return frozenset(d for y in (year, year + 1) for d in _rules(exchange, y) if d.year == year)


def _rules(exchange: str, year: int) -> set[date]:
    good_friday = easter(year) - timedelta(days=2)
    if exchange == "TSX":
        days = {
            _next_monday_if_weekend(date(year, 1, 1)),
            nth_weekday(year, 2, 0, 3),  # Family Day
            good_friday,
            date(year, 5, 25) - timedelta(days=date(year, 5, 25).weekday() or 7),  # Victoria Day: Monday before 25
            _next_monday_if_weekend(date(year, 7, 1)),  # Canada Day
            nth_weekday(year, 8, 0, 1),  # Civic Holiday
            nth_weekday(year, 9, 0, 1),  # Labour Day
            nth_weekday(year, 10, 0, 2),  # Thanksgiving
        } | _christmas_boxing(year)
    elif exchange == "US":
        new_year = date(year, 1, 1)
        days = {
            nth_weekday(year, 1, 0, 3),  # Martin Luther King Jr. Day
            nth_weekday(year, 2, 0, 3),  # Washington's Birthday
            good_friday,
            nth_weekday(year, 5, 0, -1),  # Memorial Day
            _nearest_weekday(date(year, 7, 4)),
            nth_weekday(year, 9, 0, 1),  # Labor Day
            nth_weekday(year, 11, 3, 4),  # Thanksgiving
            _nearest_weekday(date(year, 12, 25)),
        }
        if new_year.weekday() != 5:  # a Saturday New Year is not observed on the Friday before
            days.add(_nearest_weekday(new_year))
        if year >= 2022:
            days.add(_nearest_weekday(date(year, 6, 19)))  # Juneteenth
    else:
        raise ValueError(f"unknown exchange {exchange!r}")
    return days | {d for d in SPECIAL_CLOSURES[exchange] if d.year == year}


def is_trading_day(d: date, exchange: str) -> bool:
    return d.weekday() < 5 and d not in holidays(exchange, d.year)


def session(d: date, exchange: str) -> tuple[datetime, datetime] | None:
    if not is_trading_day(d, exchange):
        return None
    return datetime.combine(d, OPEN, TZ), datetime.combine(d, CLOSE, TZ)


def is_open(now: datetime, exchange: str) -> bool:
    s = session(now.astimezone(TZ).date(), exchange)
    return s is not None and s[0] <= now < s[1]


def latest_close(now: datetime, exchange: str) -> datetime:
    """The close of the most recent session that has ended by `now`."""
    d = now.astimezone(TZ).date()
    for _ in range(15):
        s = session(d, exchange)
        if s is not None and s[1] <= now:
            return s[1]
        d -= timedelta(days=1)
    raise ValueError("no session in the last 15 days")  # never with these rules


def next_session_open(now: datetime, exchange: str) -> datetime:
    d = now.astimezone(TZ).date()
    for _ in range(15):
        s = session(d, exchange)
        if s is not None and s[0] > now:
            return s[0]
        d += timedelta(days=1)
    raise ValueError("no session in the next 15 days")
