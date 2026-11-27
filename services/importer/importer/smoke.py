"""Private smoke test (make smoke-private, specs/testing.md, D-20).

Runs the broker parsers over the real exports in a read-only `data/` folder and prints counts only: rows per account
and status, the importer's own message codes, and how many rows the duplicate rule (D-8) would drop across files.
Account numbers, names, file names, dates, symbols and amounts are never printed; files and accounts are numbered.
Nothing is written anywhere: no database, no file store, no network (the script runs the container with
--network none and a read-only file system).

Layout: `<root>/accounts.md` (a Markdown table with the columns `Account Number`, `Type` and `Broker`) and one folder
per broker (`questrade/`, `wealthsimple/`) holding its exports. Files a parser refuses (holdings reports, summaries)
are counted as "not an activities export".
"""

from __future__ import annotations

import sys
import traceback
import uuid
from collections import Counter
from dataclasses import dataclass, field
from pathlib import Path

from .parsers import default_parsers
from .parsers.base import ParseContext, ParseError, Row, TrackedAccount

EXTENSIONS = {".csv", ".xlsx"}
UNTRACKED = "untracked accounts"
UNMAPPED = "unmapped rows"
STATUS_ORDER = ("ok", "warning", "error", "skipped")
ACCOUNT_TYPES = ("TFSA", "FHSA", "RRSP", "RESP")
# Synthetic account ids: stable per number within a run, never derived into output.
_NAMESPACE = uuid.UUID("6f1d3c52-8a4e-4c1b-9a57-2f6a1c0e9b11")


@dataclass
class Account:
    label: str  # "account 2 (TFSA)": position in accounts.md for that broker, and its type
    tracked: TrackedAccount


@dataclass
class FileReport:
    label: str
    rows: int = 0
    by_account: dict[str, Counter[str]] = field(default_factory=dict)
    codes: Counter[str] = field(default_factory=Counter)
    duplicates: int = 0  # rows already stored by an earlier file (D-8)
    new: int = 0  # ok/warning rows a commit would add


def read_accounts(path: Path) -> dict[str, list[Account]]:
    """accounts.md -> accounts per broker code, in the file's order."""
    out: dict[str, list[Account]] = {}
    if not path.is_file():
        return out
    header: list[str] | None = None
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line.startswith("|"):
            header = None if not line else header
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if header is None:
            header = [c.lower() for c in cells]
            continue
        if all(set(c) <= set("-: ") for c in cells):
            continue
        rec = dict(zip(header, cells))
        number, broker = rec.get("account number", ""), rec.get("broker", "").lower()
        if not number or not broker:
            continue
        kind = next((t for t in ACCOUNT_TYPES if t in rec.get("type", "").upper()), "OTHER")
        accounts = out.setdefault(broker, [])
        label = f"account {len(accounts) + 1} ({kind})"
        accounts.append(Account(label, TrackedAccount(
            id=uuid.uuid5(_NAMESPACE, f"{broker}|{number}"), external_number=number, name=label, type=kind,
            base_currency="CAD")))
    return out


def run(root: Path, out=sys.stdout) -> int:
    """Prints the report; returns the exit status (1 when a parser crashed or nothing was found)."""
    parsers = default_parsers()
    accounts = read_accounts(root / "accounts.md")
    say = lambda s="": print(s, file=out)  # noqa: E731
    crashed = 0
    activity_files = 0
    if not accounts:
        say("accounts.md: missing or without accounts; every row counts as an untracked account")

    for broker, parser in parsers.items():
        folder = root / broker
        files = sorted(p for p in folder.glob("*") if p.is_file() and p.suffix.lower() in EXTENSIONS) \
            if folder.is_dir() else []
        tracked = accounts.get(broker, [])
        labels = {str(a.tracked.id): a.label for a in tracked}
        ctx = ParseContext(broker=broker, accounts=tuple(a.tracked for a in tracked), target_account_id=None)
        say(f"{broker}: {len(tracked)} tracked accounts, {len(files)} files")

        stored: Counter[str] = Counter()  # natural key -> rows the ledger would hold after the files so far
        refused = 0
        for i, path in enumerate(files, 1):
            label = f"file {i} of {len(files)}"
            try:
                rows = parser.parse(path.read_bytes(), path.name, ctx)
            except ParseError:
                refused += 1
                say(f"  {label}: not an activities export (skipped)")
                continue
            except Exception as e:  # a bug: report where, never the message (it may quote the file)
                crashed += 1
                frame = traceback.extract_tb(e.__traceback__)[-1]
                say(f"  {label}: parser crashed ({type(e).__name__} at {Path(frame.filename).name}:{frame.lineno})")
                continue
            activity_files += 1
            report = summarize(label, rows, labels, stored)
            print_report(report, say)
        totals = sum(stored.values())
        say(f"  {broker} total: a commit of every file in this order would hold {totals} ledger rows"
            + (f"; {refused} {'file was' if refused == 1 else 'files were'} not an activities export" if refused else ""))
        say()

    if activity_files == 0:
        say("no activities export found")
        return 1
    say("result: " + ("FAIL, a parser crashed" if crashed else "parsers ran on every file"))
    return 1 if crashed else 0


def summarize(label: str, rows: list[Row], labels: dict[str, str], stored: Counter[str]) -> FileReport:
    report = FileReport(label, rows=len(rows))
    in_file: Counter[str] = Counter()
    for r in rows:
        n = r.normalized or {}
        if r.status == "skipped":
            acct = UNTRACKED
        elif r.normalized is None:
            acct = UNMAPPED  # the parser could not map the row at all, so it names no account
        else:
            acct = labels.get(str(n.get("account_id")), UNTRACKED)
        report.by_account.setdefault(acct, Counter())[r.status] += 1
        for m in r.messages:
            if m.get("level") in ("warning", "error"):
                report.codes[f'{m["level"]} {m["code"]}'] += 1
        if r.status in ("ok", "warning") and n.get("natural_key"):
            in_file[n["natural_key"]] += 1
    # D-8: of k rows with one key, the first m are the m rows already stored.
    for key, k in in_file.items():
        dup = min(k, stored[key])
        report.duplicates += dup
        report.new += k - dup
        stored[key] = max(stored[key], k)
    return report


def print_report(r: FileReport, say) -> None:
    say(f"  {r.label}: {r.rows} rows; {r.new} would be added, {r.duplicates} duplicates of earlier files")
    for acct in sorted(r.by_account, key=lambda a: (a in (UNMAPPED, UNTRACKED), a)):
        c = r.by_account[acct]
        parts = [f"{s} {c[s]}" for s in STATUS_ORDER if c[s]]
        say(f"    {acct}: " + ", ".join(parts))
    for code, k in sorted(r.codes.items()):
        say(f"    {code}: {k}")


if __name__ == "__main__":
    sys.exit(run(Path(sys.argv[1] if len(sys.argv) > 1 else "/private")))
