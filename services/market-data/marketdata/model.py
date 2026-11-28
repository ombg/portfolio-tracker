"""Observations as the providers return them. Every value is a Decimal parsed from the provider's text (rule 3) and
carries its source and the time it was observed (NFR-2)."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date, datetime
from decimal import Decimal

PRICE_PLACES = Decimal("0.0001")  # Yahoo sends binary floats (27.510000228881836); prices keep 4 places
FX_PLACES = Decimal("0.000001")


@dataclass(frozen=True)
class Quote:
    symbol: str  # provider symbol, e.g. QZGR.TO or USDCAD=X
    price: Decimal
    currency: str
    observed_at: datetime  # aware, UTC
    source: str


@dataclass(frozen=True)
class Close:
    day: date  # exchange-local trading day
    price: Decimal  # unadjusted for splits: what one share as held on that day closed at


@dataclass(frozen=True)
class Dividend:
    ex_date: date
    amount: Decimal  # per share as held on the ex-date
    currency: str


@dataclass(frozen=True)
class Split:
    day: date
    ratio: Decimal  # new shares per old share (2 for 2:1, 0.1 for 1:10)


@dataclass(frozen=True)
class History:
    symbol: str
    currency: str
    closes: list[Close]
    dividends: list[Dividend]
    splits: list[Split]
    source: str
    fetched_at: datetime


@dataclass(frozen=True)
class FxRate:
    base: str
    quote: str
    day: date
    rate: Decimal
    source: str
    observed_at: datetime


@dataclass
class QuoteResult:
    """A batch: what came back and, per symbol, why the rest did not (a run is Partial when both are non-empty)."""

    ok: dict[str, Quote] = field(default_factory=dict)
    failed: dict[str, str] = field(default_factory=dict)

    @property
    def status(self) -> str:
        if not self.failed:
            return "success"
        return "partial" if self.ok else "failed"


class ProviderError(Exception):
    """A request that failed after retries, or an answer that does not have the expected shape."""


class NotFound(ProviderError):
    """The provider does not know the symbol or series."""
