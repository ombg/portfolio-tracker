# Contributing: branches, pull requests, releases

One repository holds all services (decision D-30 in `specs/decisions.md`).

## Branches

| Branch | Purpose | Who writes to it |
| --- | --- | --- |
| `main` | Releases only. Every commit on `main` is a release and carries a tag. | Release merge from `dev`, then tag |
| `dev` | Integration branch: experimental but always builds and passes `make test`. | Merged pull requests only |
| `feature/<short-name>` | One feature or fix, branched from `dev`. | The developer (Claude or Oliver) |

Examples: `feature/m1-auth`, `feature/questrade-parser`, `fix/twr-weekend-flows`, `docs/branching`.

## Flow

```
dev ──●───────────●────────────●──────────── (integration)
       \         / \          /
        ●──●──●─●   ●──●──●──●                feature branches, one PR each
                                \
main ────────────────────────────●  tag ver-0.1.0   (release merge from dev)
```

1. Branch from the latest `dev`: `git switch dev && git pull && git switch -c feature/<name>`.
2. Commit in small steps. Every behaviour change updates its spec in the same branch.
3. Before opening a pull request: rebase on `dev`, `make test` green, new fixtures for new behaviour.
4. Open a pull request into `dev` on GitHub with: what changed, which spec items / acceptance criteria it covers,
   how it was tested, and anything the reviewer should look at first.
5. Oliver reviews. Changes are pushed to the same branch; when approved, the PR is merged with **squash merge**
   (one commit per feature on `dev`) and the branch is deleted.
6. A feature branch should be reviewable in one sitting: aim for one logical change, split large milestones into
   several branches.

## Releases
- Version format `ver-x.y.z`. Milestone n of `specs/roadmap.md` is `ver-0.n.0`; fixes on top bump `z`;
  `ver-1.0.0` when every PRD acceptance criterion passes.
- Release steps: on `dev`, update `CHANGELOG.md` and `make test && make e2e` pass; open a pull request
  `dev` -> `main` titled `Release ver-x.y.z`; after Oliver's approval, merge with a **merge commit** (so `main` has
  exactly one commit per release), tag it `ver-x.y.z` on `main`, push the tag, then merge `main` back into `dev`.
- `main` is protected on GitHub: no direct pushes, pull request required. `dev` likewise requires a pull request.

## Commits
- Imperative subject line under 72 characters, e.g. `Add Questrade activity parser`.
- Body: why, and which spec items (FR-n, D-n) it implements.
- Never commit anything from `data/`, `backups/` or `.env` (see `CLAUDE.md` rule 7).
