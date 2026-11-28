"""Bank of Canada Valet API (D-3, D-6): official daily rates to CAD (series FXUSDCAD, FXEURCAD). Published once per
business day; days without a rate (holidays) are simply absent."""

from __future__ import annotations

from datetime import date, datetime, time
from decimal import Decimal, InvalidOperation

from ..calendar import TZ
from ..model import FX_PLACES, FxRate, ProviderError
from .http import Fetcher

SOURCE = "boc"
BASE = "https://www.bankofcanada.ca/valet/observations/"
# Published around 16:30 Eastern; that is when a day's rate is observed.
PUBLISHED = time(16, 30)


class BankOfCanada:
    def __init__(self, fetcher: Fetcher):
        self.fetcher = fetcher

    def rates(self, base: str, start: date, end: date, quote: str = "CAD") -> list[FxRate]:
        if quote != "CAD":
            raise ValueError("the Bank of Canada publishes rates to CAD only")
        series = f"FX{base}CAD"
        body = self.fetcher.get_json(f"{BASE}{series}/json",
                                     {"start_date": start.isoformat(), "end_date": end.isoformat()})
        try:
            observations = body["observations"]
        except (KeyError, TypeError) as e:
            raise ProviderError("unexpected Valet response") from e
        out = []
        for o in observations:
            v = (o.get(series) or {}).get("v")
            if v in (None, ""):
                continue
            try:
                day = date.fromisoformat(o["d"])
                rate = Decimal(str(v))
            except (KeyError, ValueError, InvalidOperation) as e:
                raise ProviderError(f"bad observation: {type(e).__name__}") from e
            if start <= day <= end and rate > 0:
                out.append(FxRate(base=base, quote="CAD", day=day, rate=rate.quantize(FX_PLACES), source=SOURCE,
                                  observed_at=datetime.combine(day, PUBLISHED, TZ)))
        return sorted(out, key=lambda r: r.day)


def rate_on(rates: list[FxRate], day: date) -> FxRate | None:
    """The rate for `day`, else the previous business day's (D-6); None when none is earlier."""
    best = None
    for r in rates:
        if r.day <= day and (best is None or r.day > best.day):
            best = r
    return best

