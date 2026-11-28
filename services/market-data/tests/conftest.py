import os
from pathlib import Path

import httpx
import pytest

from marketdata.providers.http import Fetcher, RecordedTransport

TESTDATA = Path(os.environ.get("TESTDATA", "/testdata"))
RECORDED = TESTDATA / "market" / "recorded"


@pytest.fixture
def transport() -> RecordedTransport:
    return RecordedTransport(RECORDED)


@pytest.fixture
def sleeps() -> list[float]:
    return []


@pytest.fixture
def fetcher(transport, sleeps) -> Fetcher:
    return Fetcher(httpx.Client(transport=transport), retries=2, backoff=1.0, sleep=sleeps.append)
