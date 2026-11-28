"""Yahoo Finance chart API (D-3, D-39): latest quote, daily history with dividends and splits, intraday FX.
Only the provider symbol is sent (`QZGR.TO`, `USDCAD=X`)."""

from __future__ import annotations

from datetime import date, datetime, time, timedelta, timezone
from decimal import Decimal
from typing import Any, Callable
from zoneinfo import ZoneInfo

from ..model import (FX_PLACES, PRICE_PLACES, Close, Dividend, History, NotFound, ProviderError, Quote, QuoteResult,
                     Split)
from .http import Fetcher

SOURCE = "yahoo"
BASE = "https://query1.finance.yahoo.com/v8/finance/chart/"


def _utc(ts: Decimal | int) -> datetime:
    return datetime.fromtimestamp(int(ts), tz=timezone.utc)


class Yahoo:
    def __init__(self, fetcher: Fetcher, now: Callable[[], datetime] = lambda: datetime.now(timezone.utc)):
        self.fetcher, self.now = fetcher, now

    def _chart(self, symbol: str, params: dict[str, Any]) -> dict[str, Any]:
        body = self.fetcher.get_json(BASE + symbol, params)
        try:
            chart = body["chart"]
            if chart.get("error"):
                err = chart["error"]
                raise (NotFound if err.get("code") == "Not Found" else ProviderError)(
                    str(err.get("description") or err.get("code")))
            return chart["result"][0]
        except (KeyError, IndexError, TypeError) as e:
            raise ProviderError(f"unexpected chart response: {type(e).__name__}") from e

    def quote(self, symbol: str, places: Decimal = PRICE_PLACES) -> Quote:
        meta = self._chart(symbol, {"range": "1d", "interval": "1d"}).get("meta") or {}
        price, at = meta.get("regularMarketPrice"), meta.get("regularMarketTime")
        if not isinstance(price, Decimal) or price <= 0 or at is None:
            raise ProviderError("no market price in the response")
        return Quote(symbol=symbol, price=price.quantize(places), currency=str(meta.get("currency") or ""),
                     observed_at=_utc(at), source=SOURCE)

    def quotes(self, symbols: list[str]) -> QuoteResult:
        """One request per symbol; a failure is recorded for that symbol and the rest go on (Partial, FR-6)."""
        out = QuoteResult()
        for s in symbols:
            try:
                out.ok[s] = self.quote(s)
            except NotFound as e:
                out.failed[s] = f"not found: {e}"
            except ProviderError as e:
                out.failed[s] = str(e)
        return out

    def fx_quote(self, base: str, quote: str) -> Quote:
        q = self.quote(f"{base}{quote}=X", places=FX_PLACES)
        return Quote(symbol=q.symbol, price=q.price, currency=quote, observed_at=q.observed_at, source=SOURCE)

    def history(self, symbol: str, start: date, end: date) -> History:
        """Daily closes from `start` to `end` (inclusive), unadjusted for splits, with dividends and splits."""
        res = self._chart(symbol, {
            "period1": int(datetime.combine(start, time(0), timezone.utc).timestamp()),
            "period2": int(datetime.combine(end + timedelta(days=1), time(0), timezone.utc).timestamp()),
            "interval": "1d", "events": "div,split", "includeAdjustedClose": "false"})
        meta = res.get("meta") or {}
        tz = ZoneInfo(str(meta.get("exchangeTimezoneName") or "America/Toronto"))
        currency = str(meta.get("currency") or "")
        day = lambda ts: _utc(ts).astimezone(tz).date()  # noqa: E731
        events = res.get("events") or {}
        splits = sorted((Split(day=day(v["date"]), ratio=Decimal(v["numerator"]) / Decimal(v["denominator"]))
                         for v in (events.get("splits") or {}).values()
                         if v.get("numerator") and v.get("denominator")), key=lambda s: s.day)
        try:
            closes_raw = res["indicators"]["quote"][0]["close"]
            stamps = res.get("timestamp") or []
        except (KeyError, IndexError, TypeError) as e:
            raise ProviderError("no closes in the response") from e
        closes: list[Close] = []
        for ts, c in zip(stamps, closes_raw):
            if c is None:  # a day without a trade
                continue
            d = day(ts)
            if not start <= d <= end:
                continue
            # Yahoo's close is split-adjusted; undo every split after the day so it matches the shares then held.
            factor = Decimal(1)
            for s in splits:
                if s.day > d:
                    factor *= s.ratio
            closes.append(Close(day=d, price=(c * factor).quantize(PRICE_PLACES)))
        dividends = []
        for v in (events.get("dividends") or {}).values():
            d = day(v["date"])
            factor = Decimal(1)
            for s in splits:
                if s.day > d:
                    factor *= s.ratio
            if start <= d <= end:
                dividends.append(Dividend(ex_date=d, amount=(v["amount"] * factor).quantize(FX_PLACES),
                                          currency=currency))
        dividends.sort(key=lambda x: x.ex_date)
        return History(symbol=symbol, currency=currency, closes=closes, dividends=dividends,
                       splits=[s for s in splits if start <= s.day <= end], source=SOURCE, fetched_at=self.now())
