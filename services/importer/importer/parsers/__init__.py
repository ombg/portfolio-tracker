"""Registered parsers by broker code. The Questrade (#15) and Wealthsimple (#16) parsers are added here."""

from __future__ import annotations

from .base import Parser


def default_parsers() -> dict[str, Parser]:
    return {}
