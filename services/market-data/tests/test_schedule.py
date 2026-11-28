"""Refresh slots (D-3)."""

from datetime import date, datetime

from marketdata import schedule
from marketdata.calendar import TZ


def at(y, m, d, hh, mm=0):
    return datetime(y, m, d, hh, mm, tzinfo=TZ)


def test_slots_of_a_trading_day():
    s = schedule.slots(date(2025, 11, 26))
    assert [x.at.strftime("%H:%M") for x in s if x.kind == "intraday"] == [
        "09:30", "10:00", "10:30", "11:00", "11:30", "12:00", "12:30", "13:00", "13:30", "14:00", "14:30", "15:00",
        "15:30", "16:00", "16:30"]
    assert [(x.at.strftime("%H:%M"), x.kind) for x in s[-1:]] == [("17:15", "close")]


def test_days_without_slots_and_one_exchange_open():
    assert schedule.slots(date(2025, 11, 29)) == []  # Saturday
    assert schedule.slots(date(2025, 12, 25)) == []  # both closed
    assert len(schedule.slots(date(2025, 11, 27))) == 16  # US Thanksgiving: TSX trades
    assert len(schedule.slots(date(2025, 10, 13))) == 16  # Canadian Thanksgiving: NYSE trades


def test_next_and_due_slots():
    assert schedule.next_slot(at(2025, 11, 26, 16, 40)) == schedule.Slot(at(2025, 11, 26, 17, 15), "close")
    assert schedule.next_slot(at(2025, 11, 26, 17, 15)).at == at(2025, 11, 27, 9, 30)  # strictly after
    assert schedule.next_slot(at(2025, 12, 24, 18)).at == at(2025, 12, 26, 9, 30)  # NYSE trades on Boxing Day
    assert schedule.due_slot(at(2025, 11, 26, 9)) is None
    assert schedule.due_slot(at(2025, 11, 26, 10, 10)) == schedule.Slot(at(2025, 11, 26, 10), "intraday")
    assert schedule.due_slot(at(2025, 11, 26, 20)).kind == "close"
    assert schedule.market_open(at(2025, 11, 27, 11)) and not schedule.market_open(at(2025, 11, 27, 16, 5))
