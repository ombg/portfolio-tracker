"""Settings from the environment (specs/architecture.md, Configuration; D-39)."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

MIN_TOKEN_BYTES = 32
PROVIDERS = ("yahoo", "fixture", "off")


class ConfigError(Exception):
    pass


@dataclass(frozen=True)
class Settings:
    service_token: str
    provider: str  # yahoo | fixture | off
    fixture_dir: Path
    # A fixed clock (MARKET_DATA_NOW, ISO 8601): no scheduler; one catch-up run at start and on request (tests, e2e).
    frozen_now: datetime | None
    db: dict[str, str] = field(repr=False)  # psycopg keywords for pt_market; never the owner
    intraday_days_kept: int = 7

    def now(self) -> datetime:
        return self.frozen_now or datetime.now(timezone.utc)

    @classmethod
    def from_env(cls) -> Settings:
        token = os.environ.get("SERVICE_TOKEN", "")
        if len(token.encode()) < MIN_TOKEN_BYTES:
            raise ConfigError(f"SERVICE_TOKEN must be at least {MIN_TOKEN_BYTES} bytes")
        provider = os.environ.get("MARKET_DATA_PROVIDER", "yahoo")
        if provider not in PROVIDERS:
            raise ConfigError(f"MARKET_DATA_PROVIDER must be one of {', '.join(PROVIDERS)}")
        password = os.environ.get("DB_PASSWORD", "")
        if not password:
            raise ConfigError("DB_PASSWORD is required (MARKET_DB_PASSWORD in .env)")
        frozen = os.environ.get("MARKET_DATA_NOW", "")
        try:
            frozen_now = datetime.fromisoformat(frozen) if frozen else None
        except ValueError as e:
            raise ConfigError("MARKET_DATA_NOW must be an ISO 8601 time with a zone") from e
        if frozen_now is not None and frozen_now.tzinfo is None:
            raise ConfigError("MARKET_DATA_NOW must include a zone, e.g. 2025-11-27T22:00:00+00:00")
        return cls(
            service_token=token, provider=provider,
            fixture_dir=Path(os.environ.get("MARKET_DATA_FIXTURES", "/fixtures")), frozen_now=frozen_now,
            db={"host": os.environ.get("DB_HOST", "db"), "port": os.environ.get("DB_PORT", "5432"),
                "dbname": os.environ.get("DB_NAME", "portfolio"), "user": os.environ.get("DB_USER", "pt_market"),
                "password": password},
        )
