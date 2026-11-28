"""Providers against recorded responses (testdata/market/recorded): no network (specs/testing.md)."""

from datetime import date, datetime, timezone
from decimal import Decimal

import pytest

from marketdata.calendar import TZ
from marketdata.model import NotFound, ProviderError
from marketdata.providers.boc import BankOfCanada, rate_on
from marketdata.providers.http import USER_AGENT
from marketdata.providers.yahoo import Yahoo
from marketdata.symbols import fx_symbol, yahoo_symbol

NOW = datetime(2025, 11, 27, 22, 0, tzinfo=timezone.utc)


@pytest.mark.parametrize("symbol,exchange,want", [
    ("QZGR", "TSX", "QZGR.TO"), ("AP.UN", "TSX", "AP-UN.TO"), ("BRK.B", "US", "BRK-B"), ("ZZUS", "US", "ZZUS"),
    ("QZV", "TSXV", "QZV.V"), ("QZN", "NEO", "QZN.NE"), ("QZC", "CSE", "QZC.CN"), ("QZGR", None, None),
    ("", "TSX", None),
])
def test_yahoo_symbols(symbol, exchange, want):
    assert yahoo_symbol(symbol, exchange) == want
    assert fx_symbol("USD", "CAD") == "USDCAD=X"


def test_quote_is_decimal_from_the_text(fetcher):
    q = Yahoo(fetcher).quote("QZGR.TO")
    assert q.price == Decimal("27.5100") and isinstance(q.price, Decimal)
    assert (q.currency, q.source) == ("CAD", "yahoo")
    assert q.observed_at == datetime(2025, 11, 27, 16, 0, tzinfo=TZ)


def test_quotes_report_partial_failure(fetcher, sleeps, transport):
    res = Yahoo(fetcher).quotes(["QZGR.TO", "ZZUS", "QZNO.TO", "QZFL.TO"])
    assert sorted(res.ok) == ["QZGR.TO", "ZZUS"]
    assert res.ok["ZZUS"].price == Decimal("161.4200")
    assert res.failed["QZNO.TO"].startswith("not found: No data found")
    assert res.failed["QZFL.TO"] == "gave up after 3 attempts: HTTP 503"
    assert sleeps == [1.0, 2.0]  # backoff between the three attempts on the 503, none for the 404
    assert res.status == "partial"
    assert len([r for r in transport.requests if r.url.path.endswith("QZFL.TO")]) == 3


def test_batch_status():
    from marketdata.model import QuoteResult
    assert QuoteResult(ok={"a": None}).status == "success"
    assert QuoteResult(failed={"a": "x"}).status == "failed"


def test_history_undoes_split_adjustment(fetcher):
    h = Yahoo(fetcher, now=lambda: NOW).history("QZGR.TO", date(2025, 1, 6), date(2025, 1, 10))
    # Yahoo halves the closes before the 2:1 split on the 9th; held shares closed at twice that.
    assert [(c.day.day, str(c.price)) for c in h.closes] == [(6, "25.1000"), (7, "25.2000"), (8, "25.3000"),
                                                             (9, "25.4100")]
    assert [(d.ex_date, d.amount) for d in h.dividends] == [(date(2025, 1, 7), Decimal("0.120000"))]
    assert [(s.day, s.ratio) for s in h.splits] == [(date(2025, 1, 9), Decimal(2))]
    assert (h.currency, h.source, h.fetched_at) == ("CAD", "yahoo", NOW)


def test_history_window_and_missing(fetcher):
    h = Yahoo(fetcher).history("ZZUS", date(2025, 1, 8), date(2025, 1, 9))
    assert [c.day.day for c in h.closes] == [8, 9]
    with pytest.raises(NotFound):
        Yahoo(fetcher).history("QZNO.TO", date(2025, 1, 6), date(2025, 1, 10))


def test_fx_quote(fetcher):
    q = Yahoo(fetcher).fx_quote("USD", "CAD")
    assert (q.symbol, q.price, q.currency) == ("USDCAD=X", Decimal("1.393600"), "CAD")


def test_bank_of_canada_rates_and_previous_business_day(fetcher):
    rates = BankOfCanada(fetcher).rates("USD", date(2025, 1, 6), date(2025, 1, 10))
    assert [(r.day.day, str(r.rate)) for r in rates] == [(6, "1.438900"), (7, "1.430100"), (8, "1.435200"),
                                                         (10, "1.442000")]
    assert rates[0].source == "boc" and rates[0].observed_at == datetime(2025, 1, 6, 16, 30, tzinfo=TZ)
    assert rate_on(rates, date(2025, 1, 9)).day == date(2025, 1, 8)  # no rate on the 9th (D-6)
    assert rate_on(rates, date(2025, 1, 5)) is None
    with pytest.raises(ValueError):
        BankOfCanada(fetcher).rates("USD", date(2025, 1, 6), date(2025, 1, 10), quote="EUR")


def test_only_symbols_and_dates_leave_the_machine(fetcher, transport):
    """D-39: requests carry the provider symbol or series and dates, a generic user agent, and nothing else."""
    Yahoo(fetcher).quote("QZGR.TO")
    Yahoo(fetcher).history("ZZUS", date(2025, 1, 6), date(2025, 1, 10))
    BankOfCanada(fetcher).rates("USD", date(2025, 1, 6), date(2025, 1, 10))
    allowed = {"range", "interval", "period1", "period2", "events", "includeAdjustedClose", "start_date", "end_date"}
    for r in transport.requests:
        assert set(r.url.params.keys()) <= allowed, r.url
        assert r.headers["User-Agent"] == USER_AGENT
        assert "cookie" not in {k.lower() for k in r.headers.keys()}
    assert [r.url.host for r in transport.requests] == ["query1.finance.yahoo.com"] * 2 + ["www.bankofcanada.ca"]


def test_malformed_answers_are_provider_errors(tmp_path, sleeps):
    import httpx

    from marketdata.providers.http import Fetcher, RecordedTransport

    chart = tmp_path / "query1.finance.yahoo.com" / "v8" / "finance" / "chart"
    chart.mkdir(parents=True)
    (chart / "QZBAD.TO.json").write_text('{"chart": {"result": [{"meta": {"currency": "CAD"}}], "error": null}}')
    (chart / "QZTXT.TO.json").write_text("<html>rate limited</html>")
    f = Fetcher(httpx.Client(transport=RecordedTransport(tmp_path)), retries=0, sleep=sleeps.append)
    with pytest.raises(ProviderError, match="no market price"):
        Yahoo(f).quote("QZBAD.TO")
    with pytest.raises(ProviderError, match="not JSON"):
        Yahoo(f).quote("QZTXT.TO")
