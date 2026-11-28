"""Trading days and the stale rule (D-3)."""

from datetime import date, datetime, timedelta

import pytest

from marketdata import calendar
from marketdata.calendar import TZ
from marketdata.stale import is_stale


def closed_weekdays(exchange: str, year: int) -> list[date]:
    d, out = date(year, 1, 1), []
    while d.year == year:
        if d.weekday() < 5 and not calendar.is_trading_day(d, exchange):
            out.append(d)
        d += timedelta(days=1)
    return out


@pytest.mark.parametrize("exchange,year,want", [
    ("TSX", 2025, ["01-01", "02-17", "04-18", "05-19", "07-01", "08-04", "09-01", "10-13", "12-25", "12-26"]),
    ("TSX", 2026, ["01-01", "02-16", "04-03", "05-18", "07-01", "08-03", "09-07", "10-12", "12-25", "12-28"]),
    ("US", 2025, ["01-01", "01-09", "01-20", "02-17", "04-18", "05-26", "06-19", "07-04", "09-01", "11-27", "12-25"]),
    ("US", 2026, ["01-01", "01-19", "02-16", "04-03", "05-25", "06-19", "07-03", "09-07", "11-26", "12-25"]),
])
def test_published_holidays(exchange, year, want):
    assert [d.strftime("%m-%d") for d in closed_weekdays(exchange, year)] == want


def test_weekend_observance():
    # 2022: New Year on a Saturday (TSX closes Monday, NYSE does not close Friday); Christmas on a Sunday.
    assert not calendar.is_trading_day(date(2022, 1, 3), "TSX")
    assert calendar.is_trading_day(date(2021, 12, 31), "US") and calendar.is_trading_day(date(2022, 1, 3), "US")
    assert [d.day for d in closed_weekdays("TSX", 2022) if d.month == 12] == [26, 27]
    assert [d.day for d in closed_weekdays("US", 2022) if d.month == 12] == [26]
    # 2021: Christmas on a Saturday: TSX closes the 27th and 28th, NYSE the 24th.
    assert [d.day for d in closed_weekdays("TSX", 2021) if d.month == 12] == [27, 28]
    assert [d.day for d in closed_weekdays("US", 2021) if d.month == 12] == [24]


def at(y, m, d, hh, mm=0):
    return datetime(y, m, d, hh, mm, tzinfo=TZ)


def test_sessions_and_latest_close():
    assert calendar.is_open(at(2025, 11, 27, 11), "TSX")
    assert not calendar.is_open(at(2025, 11, 27, 11), "US")  # US Thanksgiving
    assert not calendar.is_open(at(2025, 11, 26, 16), "TSX")  # the close itself is after the session
    assert calendar.latest_close(at(2025, 11, 27, 11), "US") == at(2025, 11, 26, 16)
    assert calendar.latest_close(at(2025, 11, 29, 12), "TSX") == at(2025, 11, 28, 16)  # Saturday
    assert calendar.next_session_open(at(2025, 11, 28, 17), "TSX") == at(2025, 12, 1, 9, 30)


def test_stale_rule():
    now = at(2025, 11, 27, 11)  # TSX open, NYSE closed for Thanksgiving
    assert not is_stale(at(2025, 11, 27, 10), now, "TSX")  # exactly two intervals old
    assert is_stale(at(2025, 11, 27, 9, 59), now, "TSX")
    assert not is_stale(at(2025, 11, 26, 15, 30), now, "US")  # Wednesday's last price before the bell
    assert is_stale(at(2025, 11, 26, 14, 59), now, "US")
    saturday = at(2025, 11, 29, 12)
    assert not is_stale(at(2025, 11, 28, 16), saturday, "TSX")
    assert is_stale(at(2025, 11, 27, 16), saturday, "TSX")
