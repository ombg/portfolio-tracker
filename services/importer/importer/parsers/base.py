"""Parser interface. Each broker format is one parser with a version recorded on every batch, so new mappings never
change historical data (FR-3 item 8)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Protocol
from uuid import UUID

# Status of a staged row (specs/data-model.md, imports.import_rows).
ROW_STATUSES = frozenset({"ok", "warning", "error", "duplicate", "skipped", "excluded"})


@dataclass(frozen=True)
class TrackedAccount:
    id: UUID
    external_number: str | None
    name: str
    type: str
    base_currency: str


@dataclass(frozen=True)
class ParseContext:
    broker: str
    accounts: tuple[TrackedAccount, ...]  # the user's accounts at this broker
    target_account_id: UUID | None  # chosen at upload, if any


@dataclass
class Row:
    row_no: int  # 1-based position in the source, for traceability (NFR-2)
    raw: dict[str, Any]
    status: str
    normalized: dict[str, Any] | None = None
    messages: list[dict[str, Any]] = field(default_factory=list)


class ParseError(Exception):
    """The file cannot be read as this parser's format at all (as opposed to single bad rows)."""


class Parser(Protocol):
    broker: str  # core.brokers.code
    name: str
    version: str

    def parse(self, data: bytes, filename: str, ctx: ParseContext) -> list[Row]: ...
