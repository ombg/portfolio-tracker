"""Parser interface. Each broker format is one parser with a version recorded on every batch, so new mappings never
change historical data (FR-3 item 8)."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any, Protocol
from uuid import UUID

# Status of a staged row (specs/data-model.md, imports.import_rows).
ROW_STATUSES = frozenset({"ok", "warning", "error", "duplicate", "skipped", "excluded"})

# core.transactions.type: the targets an action rule may map to (D-36).
LEDGER_TYPES = frozenset({
    "BUY", "SELL", "DIVIDEND", "DISTRIBUTION", "DRIP", "DEPOSIT", "WITHDRAWAL", "TRANSFER_IN", "TRANSFER_OUT", "FEE",
    "INTEREST", "FX_CONVERSION", "SPLIT", "SYMBOL_CHANGE", "JOURNAL", "MERGER", "SPIN_OFF", "RETURN_OF_CAPITAL",
    "WITHHOLDING_TAX", "GRANT", "CORRECTION_ADJUSTMENT",
})
# core.security_symbols.exchange, and the currency a listing trades in.
EXCHANGE_CURRENCY = {"TSX": "CAD", "TSXV": "CAD", "NEO": "CAD", "CSE": "CAD", "US": "USD"}


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
    # Broker codes with a saved alias (security_aliases: global, then the user's own) -> security id.
    aliases: Mapping[str, UUID] = field(default_factory=dict)
    # The user's import rules for this broker (imports.import_rules, D-36): action key -> ledger type, used only where
    # the parser has no mapping; file symbol or broker code -> (symbol, exchange).
    action_rules: Mapping[str, str] = field(default_factory=dict)
    symbol_rules: Mapping[str, tuple[str, str]] = field(default_factory=dict)


@dataclass(frozen=True)
class RuleKeys:
    """What a rule for this source row is keyed on; None where the row has nothing to key on."""

    action: str | None
    symbol: str | None


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

    def rule_keys(self, raw: dict[str, Any]) -> RuleKeys: ...
