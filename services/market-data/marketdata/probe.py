"""Live check of the providers, run by hand: `make market-probe SYMBOLS="XIU.TO AAPL"`. Sends only the given Yahoo
symbols and the Bank of Canada USD series (D-39) and prints what came back, so a change in the providers' formats
shows up before the scheduler depends on it. Not part of make test (no network there)."""

from __future__ import annotations

import sys
from datetime import date, timedelta

import httpx

from .providers.boc import BankOfCanada
from .providers.http import Fetcher
from .providers.yahoo import Yahoo


def main(symbols: list[str]) -> int:
    fetcher = Fetcher(httpx.Client(timeout=15))
    yahoo, boc = Yahoo(fetcher), BankOfCanada(fetcher)
    res = yahoo.quotes(symbols + ["USDCAD=X"])
    for s, q in res.ok.items():
        print(f"{s}: {q.price} {q.currency} at {q.observed_at.isoformat()} ({q.source})")
    for s, err in res.failed.items():
        print(f"{s}: FAILED {err}")
    end = date.today()
    for s in symbols[:1]:
        try:
            h = yahoo.history(s, end - timedelta(days=10), end)
            print(f"{s} history: {len(h.closes)} closes, last {h.closes[-1] if h.closes else None}")
        except Exception as e:  # noqa: BLE001 - a probe reports everything
            print(f"{s} history: FAILED {e}")
    try:
        rates = boc.rates("USD", end - timedelta(days=10), end)
        print(f"Bank of Canada USD/CAD: {len(rates)} days, last {rates[-1].day} {rates[-1].rate}" if rates else
              "Bank of Canada USD/CAD: no rates")
    except Exception as e:  # noqa: BLE001
        print(f"Bank of Canada: FAILED {e}")
    print(f"status: {res.status}")
    return 0 if res.status == "success" else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:] or ["XIU.TO"]))
