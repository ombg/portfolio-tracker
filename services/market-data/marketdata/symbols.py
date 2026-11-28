"""Ledger listing -> Yahoo symbol (specs/market-data.md, Yahoo symbols)."""

from __future__ import annotations

SUFFIX = {"TSX": ".TO", "TSXV": ".V", "NEO": ".NE", "CSE": ".CN", "US": ""}


def yahoo_symbol(symbol: str, exchange: str | None) -> str | None:
    """`AP.UN` on TSX -> `AP-UN.TO`; `BRK.B` on US -> `BRK-B`. None when the listing cannot be priced (no exchange,
    an unknown exchange, or no symbol, e.g. a GIC)."""
    if not symbol or exchange not in SUFFIX:
        return None
    return symbol.strip().upper().replace(".", "-") + SUFFIX[exchange]


def fx_symbol(base: str, quote: str) -> str:
    return f"{base}{quote}=X"
