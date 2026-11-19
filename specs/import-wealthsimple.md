# Import: Wealthsimple

Implements: FR-3 for Wealthsimple. Parser: `services/importer/parsers/wealthsimple.py`.

**Status: blocked on samples (Q-3).** Place exports under `data/wealthsimple/<account>/` and this spec gets the
field-level mapping, written the same way as `import-questrade.md`.

Needed:
- Activity / transaction CSV for each account (TFSA, RRSP, non-registered, etc.), full history.
- A holdings or positions export (for reconciliation, D-11), if Wealthsimple provides one.
- Note whether the file came from the web app or from monthly statements; Wealthsimple has changed formats over
  time, so the parser will detect format version from the header row and keep one mapping per version.
