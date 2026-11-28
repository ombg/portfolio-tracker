"""Refresh runs against a migrated database as pt_market, with recorded provider responses (D-39)."""

import os
from datetime import date, datetime, timedelta, timezone
from decimal import Decimal
from uuid import UUID, uuid4

import httpx
import psycopg
import pytest
from fastapi.testclient import TestClient

from marketdata.app import Scheduler, create_app
from marketdata.calendar import TZ
from marketdata.config import Settings
from marketdata.providers.boc import BankOfCanada
from marketdata.providers.http import Fetcher, RecordedTransport
from marketdata.providers.yahoo import Yahoo
from marketdata.refresh import Refresher
from marketdata.store import Store, UnsafeRoleError

from .conftest import RECORDED

QZGR, ZZUS, QZNO, GIC = (UUID(f"40000000-0000-0000-0000-00000000000{i}") for i in (1, 2, 3, 4))
DB = {"host": os.environ.get("DB_HOST", "db"), "port": "5432", "dbname": "portfolio", "user": "pt_market",
      "password": os.environ.get("DB_PASSWORD", "")}


@pytest.fixture
def owner():
    with psycopg.connect(os.environ["OWNER_DSN"], autocommit=True) as c:
        yield c


@pytest.fixture
def seeded(owner):
    """One user holding QZGR (TSX), ZZUS (US), QZNO (TSX, unknown to Yahoo) and a GIC, with a USD cash row."""
    owner.execute("TRUNCATE core.users, core.securities CASCADE")
    owner.execute("TRUNCATE market.refresh_runs, market.fx_rates")
    user, pf, acct = uuid4(), uuid4(), uuid4()
    owner.execute("INSERT INTO core.users (id, email, password_hash, display_name) VALUES (%s, 'm@example.test', "
                  "'x', 'M')", (user,))
    owner.execute("INSERT INTO core.portfolios (id, user_id, name) VALUES (%s, %s, 'Main')", (pf, user))
    owner.execute("""INSERT INTO core.accounts (id, user_id, portfolio_id, broker_id, name, type, is_registered,
                     base_currency) SELECT %s, %s, %s, id, 'TFSA', 'TFSA', true, 'CAD' FROM core.brokers
                     WHERE code = 'questrade'""", (acct, user, pf))
    for sid, name, sym, ex, cur, typ in [(QZGR, "SYNTHETIC GROWTH ETF", "QZGR", "TSX", "CAD", None),
                                         (ZZUS, "ZEDCO SYNTHETIC INC", "ZZUS", "US", "USD", None),
                                         (QZNO, "SYNTHETIC UNKNOWN", "QZNO", "TSX", "CAD", None),
                                         (GIC, "SYNTHETIC GIC", None, None, "CAD", "gic")]:
        owner.execute("INSERT INTO core.securities (id, name, security_type, trading_currency) VALUES (%s, %s, %s, %s)",
                      (sid, name, typ, cur))
        if sym:
            owner.execute("INSERT INTO core.security_symbols (security_id, symbol, exchange) VALUES (%s, %s, %s)",
                          (sid, sym, ex))
    n = 0
    for sid, day, qty, cur in [(QZGR, "2025-01-06", 10, "CAD"), (ZZUS, "2025-01-08", 2, "USD"),
                               (QZNO, "2025-01-07", 1, "CAD"), (GIC, "2025-01-06", 100, "CAD"),
                               (None, "2025-01-06", None, "USD")]:
        n += 1
        owner.execute("""INSERT INTO core.transactions (user_id, account_id, type, trade_date, security_id, quantity,
                         net_amount, currency, origin, natural_key) VALUES (%s, %s, %s, %s, %s, %s, -1, %s,
                         'manual', %s)""", (user, acct, "BUY" if sid else "DEPOSIT", day, sid, qty, cur, f"{n:064x}"))
    return owner


def refresher(now: datetime, sleeps=None) -> tuple[Refresher, RecordedTransport]:
    t = RecordedTransport(RECORDED)
    f = Fetcher(httpx.Client(transport=t), retries=1, backoff=0, sleep=(sleeps or []).append)
    return Refresher(Store(DB), Yahoo(f, now=lambda: now), BankOfCanada(f), lambda: now), t


def rows(owner, sql, *args):
    return owner.execute(sql, args).fetchall()


def test_universe_hides_holders_and_skips_gics(seeded):
    u = Store(DB).universe()
    assert [(x.symbol, x.exchange, str(x.first_trade_date), x.held) for x in u] == [
        ("QZGR", "TSX", "2025-01-06", True), ("QZNO", "TSX", "2025-01-07", True), ("ZZUS", "US", "2025-01-08", True)]
    assert Store(DB).fx_start() == {"USD": date(2025, 1, 6)}


def test_role_check_refuses_the_owner(seeded):
    Store(DB).check_role()
    owner_db = {**DB, "user": "portfolio", "password": os.environ["OWNER_PASSWORD"]}
    with pytest.raises(UnsafeRoleError):
        Store(owner_db).check_role()


def test_intraday_run_is_partial_and_recorded(seeded):
    now = datetime(2025, 11, 27, 18, 0, tzinfo=timezone.utc)  # 13:00 Toronto, TSX open
    r, _ = refresher(now)
    s = r.run("intraday")
    assert (s.status, s.ok) == ("partial", 3)  # QZGR, ZZUS and the USD/CAD quote
    assert list(s.failed) == ["QZNO.TO"] and s.failed["QZNO.TO"].startswith("not found")
    prices = rows(seeded, "SELECT security_id, kind, price, currency, trade_date FROM market.prices ORDER BY price")
    assert prices == [(QZGR, "intraday", Decimal("27.51000000"), "CAD", date(2025, 11, 27)),
                      (ZZUS, "intraday", Decimal("161.42000000"), "USD", date(2025, 11, 27))]
    assert rows(seeded, "SELECT base, quote, kind, rate FROM market.fx_rates") == [
        ("USD", "CAD", "intraday", Decimal("1.3936000000"))]
    run = rows(seeded, "SELECT kind, status, symbols_ok, symbols_failed, errors ? 'QZNO.TO', next_run_at "
                       "FROM market.refresh_runs")
    assert run == [("intraday", "partial", 3, 1, True, datetime(2025, 11, 27, 13, 30, tzinfo=TZ))]


def test_close_run_stores_unadjusted_closes_splits_and_boc_rates(seeded):
    now = datetime(2025, 1, 10, 22, 30, tzinfo=timezone.utc)  # 17:30 Toronto
    r, _ = refresher(now)
    s = r.run("close")
    assert s.status == "partial" and list(s.failed) == ["QZNO.TO"]
    closes = rows(seeded, "SELECT trade_date, price, observed_at FROM market.prices WHERE security_id = %s "
                          "AND kind = 'close' ORDER BY trade_date", QZGR)
    assert [(d.day, str(p)) for d, p, _ in closes] == [(6, "25.10000000"), (7, "25.20000000"), (8, "25.30000000"),
                                                       (9, "25.41000000")]
    assert closes[0][2] == datetime(2025, 1, 6, 16, 0, tzinfo=TZ)
    assert rows(seeded, "SELECT day, ratio FROM market.split_events") == [(date(2025, 1, 9), Decimal("2"))]
    assert rows(seeded, "SELECT ex_date, amount FROM market.dividend_events") == [
        (date(2025, 1, 7), Decimal("0.1200000000"))]
    assert [(d.day, str(v)) for d, v in rows(seeded, "SELECT rate_date, rate FROM market.fx_rates WHERE "
                                                     "kind = 'daily' ORDER BY rate_date")] == [
        (6, "1.4389000000"), (7, "1.4301000000"), (8, "1.4352000000"), (10, "1.4420000000")]
    # Running it again changes nothing (upserts).
    r.run("close")
    assert len(rows(seeded, "SELECT 1 FROM market.prices WHERE kind = 'close'")) == 9  # QZGR 4, ZZUS 5


def test_backfill_fetches_only_what_is_missing(seeded):
    now = datetime(2025, 1, 10, 23, 0, tzinfo=timezone.utc)
    r, t = refresher(now)
    s = r.run("backfill")
    assert (s.status, s.ok) == ("partial", 3)  # QZGR, ZZUS history and BoC USD; QZNO not found
    state = {x[0]: x[1:] for x in rows(seeded, "SELECT security_id, from_date, error IS NULL FROM market.backfills")}
    assert state[QZGR] == (date(2025, 1, 6), True) and state[QZNO] == (date(2025, 1, 7), False)
    before = len(t.requests)
    again = r.run("backfill")
    assert (again.status, again.ok, len(t.requests) - before) == ("success", 0, 0)  # nothing new, QZNO waits a day
    # A newly imported older trade widens the window: only that security is fetched again.
    seeded.execute("""INSERT INTO core.transactions (user_id, account_id, type, trade_date, security_id, quantity,
                      net_amount, currency, origin, natural_key) SELECT user_id, account_id, 'BUY', '2025-01-02',
                      %s, 1, -1, 'CAD', 'manual', repeat('f', 64) FROM core.transactions LIMIT 1""", (QZGR,))
    before = len(t.requests)
    r.run("backfill")
    assert [q.url.path.rsplit("/", 1)[-1] for q in t.requests[before:]] == ["QZGR.TO"]


def test_old_intraday_quotes_are_pruned_at_the_close(seeded):
    seeded.execute("""INSERT INTO market.prices (security_id, kind, trade_date, observed_at, price, currency, source)
                      VALUES (%s, 'intraday', '2024-12-20', '2024-12-20 15:00Z', 1, 'CAD', 'yahoo')""", (QZGR,))
    r, _ = refresher(datetime(2025, 1, 10, 22, 30, tzinfo=timezone.utc))
    r.run("close")
    assert rows(seeded, "SELECT 1 FROM market.prices WHERE kind = 'intraday'") == []


def test_catch_up_runs_backfill_then_the_due_slot_once(seeded):
    now = datetime(2025, 1, 10, 22, 30, tzinfo=timezone.utc)
    r, _ = refresher(now)
    settings = Settings(service_token="t" * 32, provider="fixture", fixture_dir=RECORDED, frozen_now=now, db=DB)
    sched = Scheduler(r, settings)
    sched.catch_up()
    sched.catch_up()
    # The frozen clock gives every run the same start time, so count them: the close ran once, backfill twice.
    assert dict(rows(seeded, "SELECT kind, count(*) FROM market.refresh_runs GROUP BY kind")) == {
        "backfill": 2, "close": 1}


def test_internal_api(seeded):
    now = datetime(2025, 11, 27, 18, 0, tzinfo=timezone.utc)
    token = os.environ["SERVICE_TOKEN"]
    settings = Settings(service_token=token, provider="fixture", fixture_dir=RECORDED, frozen_now=now, db=DB)
    with TestClient(create_app(settings, start_scheduler=False)) as client:
        assert client.get("/internal/health").json() == {"status": "ok", "provider": "fixture"}
        assert client.get("/internal/status").status_code == 401
        assert client.post("/internal/refresh", headers={"X-Service-Token": "wrong" * 8}).status_code == 401
        client.app.state.refresher.run("intraday")
        st = client.get("/internal/status", headers={"X-Service-Token": token}).json()
    assert st["provider"] == "fixture" and st["clock"] == "frozen" and st["market_open"] is True
    assert st["last_run_status"] == "partial" and st["last_success_at"] is None
    assert st["last_close_date"] == "2025-11-26" and st["next_run_at"] is None  # no scheduler with a frozen clock
    # 13:00 Toronto on US Thanksgiving: TSX prices must be from 12:00 on; US prices from Wednesday 15:00 on.
    assert st["stale_before"] == {"TSX": "2025-11-27T17:00:00+00:00", "US": "2025-11-26T20:00:00+00:00"}
    assert st["runs"][0]["errors"]["QZNO.TO"].startswith("not found")


def test_provider_off_makes_no_requests(seeded):
    settings = Settings(service_token="t" * 32, provider="off", fixture_dir=RECORDED, frozen_now=None, db=DB)
    with TestClient(create_app(settings)) as client:
        assert client.post("/internal/backfill", headers={"X-Service-Token": "t" * 32}).status_code == 409
        assert client.app.state.refresher is None and client.app.state.scheduler is None
