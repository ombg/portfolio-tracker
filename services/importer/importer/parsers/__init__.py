"""Registered parsers by broker code."""

from __future__ import annotations

from .base import Parser
from .questrade import QuestradeParser
from .wealthsimple import WealthsimpleParser


def default_parsers() -> dict[str, Parser]:
    return {"questrade": QuestradeParser(), "wealthsimple": WealthsimpleParser()}
