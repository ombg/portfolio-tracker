"""Helpers shared by the broker parsers."""

from __future__ import annotations

from decimal import Decimal


def fmt(d: Decimal | None) -> str | None:
    """Canonical decimal text: no exponent, no trailing zeros ("25.10000000" -> "25.1")."""
    if d is None:
        return None
    s = format(d.normalize(), "f")
    return "0" if s in ("-0", "") else s
