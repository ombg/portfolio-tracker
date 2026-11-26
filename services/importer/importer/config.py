"""Settings from the environment (specs/architecture.md, Configuration)."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path

MIN_TOKEN_BYTES = 32


class ConfigError(Exception):
    pass


@dataclass(frozen=True)
class Settings:
    service_token: str
    files_dir: Path
    # psycopg connection keywords for the pt_importer role (D-35); never the owner.
    db: dict[str, str] = field(repr=False)
    max_upload_bytes: int = 25 * 1024 * 1024

    @classmethod
    def from_env(cls) -> Settings:
        token = os.environ.get("SERVICE_TOKEN", "")
        if len(token.encode()) < MIN_TOKEN_BYTES:
            raise ConfigError(
                f"SERVICE_TOKEN must be at least {MIN_TOKEN_BYTES} bytes; generate one with: openssl rand -base64 48"
            )
        password = os.environ.get("DB_PASSWORD", "")
        if not password:
            raise ConfigError("DB_PASSWORD is required (IMPORTER_DB_PASSWORD in .env)")
        return cls(
            service_token=token,
            files_dir=Path(os.environ.get("FILES_DIR", "/data/files")),
            db={
                "host": os.environ.get("DB_HOST", "db"),
                "port": os.environ.get("DB_PORT", "5432"),
                "dbname": os.environ.get("DB_NAME", "portfolio"),
                "user": os.environ.get("DB_USER", "pt_importer"),
                "password": password,
            },
        )
