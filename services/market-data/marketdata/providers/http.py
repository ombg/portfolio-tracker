"""HTTP plumbing shared by the providers: JSON numbers parsed as Decimal (never float, rule 3), retries with backoff
for rate limits and server errors, and a transport that answers from recorded files (tests and MARKET_DATA_PROVIDER=
fixture, D-39)."""

from __future__ import annotations

import json
import time
from decimal import Decimal
from pathlib import Path
from typing import Any, Callable

import httpx

from ..model import NotFound, ProviderError

RETRY_STATUS = {429, 500, 502, 503, 504}
USER_AGENT = "portfolio-tracker-market-data/0.1"  # generic: no user or instance details (privacy)


def decode(text: str) -> Any:
    return json.loads(text, parse_float=Decimal, parse_int=Decimal)


class Fetcher:
    """GET with retries. `sleep` is injectable so tests do not wait."""

    def __init__(self, client: httpx.Client, retries: int = 3, backoff: float = 1.0,
                 sleep: Callable[[float], None] = time.sleep):
        self.client, self.retries, self.backoff, self.sleep = client, retries, backoff, sleep

    def get_json(self, url: str, params: dict[str, Any] | None = None) -> Any:
        last = ""
        for attempt in range(self.retries + 1):
            try:
                res = self.client.get(url, params=params, headers={"User-Agent": USER_AGENT})
            except httpx.TransportError as e:
                last = f"{type(e).__name__}"
            else:
                if res.status_code == 404:
                    raise NotFound(_message(res) or "not found")
                if res.status_code not in RETRY_STATUS:
                    if res.status_code >= 400:
                        raise ProviderError(f"HTTP {res.status_code}: {_message(res)}".rstrip(": "))
                    try:
                        return decode(res.text)
                    except ValueError as e:
                        raise ProviderError(f"not JSON: {e}") from e
                last = f"HTTP {res.status_code}"
            if attempt < self.retries:
                self.sleep(self.backoff * 2**attempt)
        raise ProviderError(f"gave up after {self.retries + 1} attempts: {last}")


def _message(res: httpx.Response) -> str:
    """The provider's own error text, if it sent one (Yahoo: chart.error.description)."""
    try:
        body = decode(res.text)
    except ValueError:
        return ""
    err = (body.get("chart") or {}).get("error") if isinstance(body, dict) else None
    return str(err.get("description") or err.get("code") or "") if isinstance(err, dict) else ""


class RecordedTransport(httpx.BaseTransport):
    """Answers from files under `root`, keyed by host and path: `<root>/<host>/<path>.json` (query ignored). A missing
    file is a 404 with Yahoo's not-found body; `<file>.status` holds another status code to replay (e.g. 503)."""

    def __init__(self, root: Path):
        self.root = Path(root)
        self.requests: list[httpx.Request] = []

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path = self.root / request.url.host / (request.url.path.strip("/") + ".json")
        status = path.with_suffix(".status")
        if status.is_file():
            return httpx.Response(int(status.read_text().strip()), text="", request=request)
        if not path.is_file():
            body = {"chart": {"result": None, "error": {"code": "Not Found",
                                                        "description": "No data found, symbol may be delisted"}}}
            return httpx.Response(404, json=body, request=request)
        return httpx.Response(200, text=path.read_text(), headers={"Content-Type": "application/json"},
                              request=request)
