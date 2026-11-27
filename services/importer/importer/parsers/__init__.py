"""Registered parsers by broker code. The Wealthsimple parser (#16) is added here next."""

from __future__ import annotations

from .base import Parser
from .questrade import QuestradeParser


def default_parsers() -> dict[str, Parser]:
    return {"questrade": QuestradeParser()}
