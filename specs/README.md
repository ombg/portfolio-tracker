# specs/

Behavioural source of truth for the portfolio tracker. Code follows these files; when behaviour changes, the spec
changes in the same commit. Requirements come from `chatgpt/docs/portfolio-tracker-requirements.md` (the PRD);
specs say *how* each requirement is met and reference PRD items (FR-n, NFR-n, AC-n) and decisions (D-n).

| File | What it is | Read it when |
| --- | --- | --- |
| decisions.md | PRD review, recommended amendments, numbered design decisions, open questions | before changing any rule; to see why something is the way it is |
| architecture.md | services, ownership rules, key flows, repo layout, config | adding a service, endpoint or cross-service call |
| data-model.md | every table, column, type, link rule and view | writing migrations or queries |
| calculations.md | holdings, average cost, cash, valuation, TWR, XIRR, income, diversification formulas | touching `internal/calc` or any number on screen |
| import-questrade.md | Questrade export format and field mapping | working on the Questrade parser |
| import-wealthsimple.md | Wealthsimple mapping (blocked on samples) | when samples arrive |
| market-data.md | providers, refresh schedule, status semantics | working on market-data or status UI |
| data-quality.md | data-quality issues and reconciliation workflow | anything that could show a stale or missing value |
| testing.md | golden fixtures and test layers | adding a feature (add its fixture first) |
| roadmap.md | milestones M1-M8 mapped to acceptance criteria | planning the next piece of work |

Status of every decision: see the top of decisions.md.
