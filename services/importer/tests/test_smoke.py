"""make smoke-private (importer/smoke.py) on the synthetic fixtures laid out like data/ (D-20): the counts are right and
the output names nothing from the files."""

from __future__ import annotations

import csv
import io
import re
import shutil
from pathlib import Path

import pytest

from importer.smoke import read_accounts, run

from .golden import TESTDATA

QT = TESTDATA / "questrade" / "synthetic_activities" / "input"
WS = TESTDATA / "wealthsimple" / "synthetic_activities" / "input"

ACCOUNTS_MD = """# Active Accounts

| Account Number | Name | Nickname | Opening Date | Closed Date | Type | Broker |
| --- | --- | --- | --- | --- | --- | --- |
| SY12AB345CAD | Synthetic TFSA Name | T-W | 2024-01 | active | TFSA | Wealthsimple |
| SY12AB346 | Synthetic FHSA Name | F-W | 2024-02 | active | FHSA | Wealthsimple |
| 90000001 | Synthetic Questrade TFSA | T-Q | 2020-01 | active | TFSA | Questrade |
| 90000002 | Synthetic Questrade FHSA | F-Q | 2020-01 | active | FHSA | Questrade |
| 90000004 | Synthetic Questrade RESP | R-Q | 2020-01 | active | RESP | Questrade |
"""


@pytest.fixture
def root(tmp_path: Path) -> Path:
    (tmp_path / "accounts.md").write_text(ACCOUNTS_MD)
    (tmp_path / "questrade").mkdir()
    (tmp_path / "wealthsimple").mkdir()
    shutil.copy(QT / "activities.xlsx", tmp_path / "questrade" / "Activities_synthetic.xlsx")
    shutil.copy(QT / "activities_older.csv", tmp_path / "questrade" / "older_synthetic.csv")
    shutil.copy(WS / "activities.csv", tmp_path / "wealthsimple" / "activities-export-synthetic.csv")
    (tmp_path / "wealthsimple" / "holdings-report-synthetic.csv").write_text("Account Name,Symbol\nX,ZZZZ\n")
    (tmp_path / "questrade" / "notes.png").write_bytes(b"\x89PNG")  # not an export: ignored
    return tmp_path


def report(root: Path) -> tuple[int, str]:
    out = io.StringIO()
    status = run(root, out)
    return status, out.getvalue()


def test_accounts_md_is_read_per_broker(root: Path):
    accts = read_accounts(root / "accounts.md")
    assert [a.label for a in accts["questrade"]] == ["account 1 (TFSA)", "account 2 (FHSA)", "account 3 (RESP)"]
    assert [a.tracked.external_number for a in accts["wealthsimple"]] == ["SY12AB345CAD", "SY12AB346"]


def test_counts(root: Path):
    status, text = report(root)
    assert status == 0, text
    assert text.splitlines() == [
        "questrade: 3 tracked accounts, 2 files",
        "  file 1 of 2: 38 rows; 31 would be added, 0 duplicates of earlier files",
        "    account 1 (TFSA): ok 19, warning 5, error 4",
        "    account 2 (FHSA): ok 4",
        "    account 3 (RESP): ok 3",
        "    unmapped rows: error 2",
        "    untracked accounts: skipped 1",
        "    error unknown_broker_code: 4",
        "    error unmapped_action: 2",
        "    warning net_mismatch: 1",
        "    warning proposed_security: 3",
        "    warning unpaired_fx_leg: 1",
        # The older export overlaps: 2 of its 3 rows are already stored (D-8).
        "  file 2 of 2: 3 rows; 1 would be added, 2 duplicates of earlier files",
        "    account 1 (TFSA): ok 3",
        "  questrade total: a commit of every file in this order would hold 32 ledger rows",
        "",
        "wealthsimple: 2 tracked accounts, 2 files",
        "  file 1 of 2: 38 rows; 36 would be added, 0 duplicates of earlier files",
        "    account 1 (TFSA): ok 32, warning 3",
        "    account 2 (FHSA): ok 1",
        "    unmapped rows: error 1",
        "    untracked accounts: skipped 1",
        "    error unmapped_action: 1",
        "    warning amount_mismatch: 1",
        "    warning description_quantity_mismatch: 1",
        "    warning unlinked_withholding: 1",
        "  file 2 of 2: not an activities export (skipped)",
        "  wealthsimple total: a commit of every file in this order would hold 36 ledger rows; "
        "1 file was not an activities export",
        "",
        "result: parsers ran on every file",
    ]


def _tokens() -> set[str]:
    """Values from the synthetic inputs that must never be printed: account numbers, names, symbols, dates,
    amounts and file names."""
    found: set[str] = set()
    for path, cols in [
        (QT / "activities.csv", ("Account #", "Symbol", "Transaction Date", "Net Amount", "Description")),
        (WS / "activities.csv", ("account_id", "symbol", "effective_date", "net_cash_amount", "name")),
    ]:
        with path.open(newline="", encoding="utf-8-sig") as f:
            for rec in csv.DictReader(f):
                for c in cols:
                    v = (rec.get(c) or "").strip()
                    if len(v) >= 3 and not v.isdigit() or (v.isdigit() and len(v) >= 6):
                        found.add(v.split(" ")[0] if c in ("Transaction Date",) else v)
    found |= {"Activities_synthetic", "older_synthetic", "activities-export-synthetic", "holdings-report-synthetic",
              "Synthetic TFSA Name", "Synthetic Questrade", "ZZZZ"}
    return found


def test_output_names_nothing_from_the_files(root: Path):
    _, text = report(root)
    leaked = sorted(t for t in _tokens() if t in text)
    assert leaked == []
    assert not re.search(r"\d+\.\d+", text), "no amount or quantity with decimals"
    assert not re.search(r"\d{4}-\d{2}-\d{2}", text), "no dates"


def test_a_crashing_parser_is_reported_without_its_message(root: Path, monkeypatch):
    from importer import smoke
    from importer.parsers.questrade import QuestradeParser

    def boom(self, data, filename, ctx):
        raise ValueError("SECRET-90000001 in the message")

    monkeypatch.setattr(QuestradeParser, "parse", boom)
    status, text = report(root)
    assert status == 1
    assert "parser crashed (ValueError at test_smoke.py:" in text
    assert "SECRET" not in text and "90000001" not in text
    assert text.splitlines()[-1] == "result: FAIL, a parser crashed"
    assert smoke  # imported for monkeypatch scope


def test_no_accounts_md_counts_everything_as_untracked(root: Path):
    (root / "accounts.md").unlink()
    status, text = report(root)
    assert status == 0
    assert text.startswith("accounts.md: missing or without accounts")
    assert "    untracked accounts: skipped 38" in text


def test_nothing_found(tmp_path: Path):
    status, text = report(tmp_path)
    assert status == 1
    assert "no activities export found" in text
