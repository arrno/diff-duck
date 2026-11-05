# North Star: duck-diff v1

Product intent and invariants live in [concept.md](concept.md) (I1–I12). This document only
records what v1 includes, the decisions made for it, and how it is accepted.

## Goal

`duck-diff <repo-url-or-path> <base> <head>` turns the Git diff between any two commits
into a compressed diff: structured JSON plus one self-contained HTML viewer.

The CLI doubles as the evaluation harness: it runs against any Git project and any commit pair.

## Decisions

| Topic | Decision |
| --- | --- |
| Language | Go first, through `go/parser`. Other languages add a `SourceAnalyzer` adapter. |
| Input | A GitHub (or other https/ssh) repo URL, or a local path to a Git repo. |
| Quality goal | Structural facts are deterministic and strictly tested. Summaries are judged for quality, not determinism. |
| Prompts | Markdown files, one directory per variant, injected with `--prompts`. Every run records the variant and prompt hashes. |
| Model access | A `LanguageModel` port. Adapters: `claude-cli` (default, cheaper) and `anthropic-api`. Default model: Sonnet. Each run has a call limit. |
| UI | React and shadcn/ui in `clients/viewer`, built into one self-contained HTML page with no server. This is an agreed exception to `ui-architecture.md`: no Next.js, backend API, or login, because the tool has no backend. Its component, theming, accessibility, and Playwright rules still apply. |
| Review | One integrated review before verification, instead of one per step. |

## v1 scope

In: everything in concept sections 3, 4, 5 and 6, with Go code units and file-level nodes for all
other files, identical-body rename detection, semantic aliases, uncertainty flags, and a response
cache so prompt tuning does not re-bill unchanged calls.

Out: move and extraction matching beyond identical bodies, separating refactors from behavior
changes, configurable summary depth, GitHub PR integration, hosting, and login.

## Domain concepts

`FileChange` (parsed diff), `Unit` (code unit in one revision), `Node` (compressed tree entry),
`Outcome`, `Coverage`, `CompressedDiff`.

## Acceptance criteria

| # | Criterion | Invariant |
| --- | --- | --- |
| A1 | Every changed line and every changed file traces to a node; the run reports coverage and it is 100% on the fixture | I1, I7 |
| A2 | The tree mirrors the source: package > type > method or field, package > function | I2 |
| A3 | Names, signatures, operations, and locations come only from source analysis; a model cannot alter them | I11 |
| A4 | Parents distinguish direct from inherited change | concept 4.5 |
| A5 | Identical-body renames are CHANGE with the previous identity; anything less certain stays REMOVE plus ADD | I10, concept 5.5 |
| A6 | Formatting and comment changes do not read as code changes but remain traceable | I1, concept 4.4 |
| A7 | Summaries are produced bottom-up; a model failure leaves the node visibly unsummarized and never invents text | I3, I4, I10 |
| A8 | Outcomes reference only real nodes; unassociated leaves are listed as unassigned | I8 |
| A9 | Selecting an outcome filters the view with ancestors kept; clearing it restores the full tree; the model is unchanged | I9 |
| A10 | The viewer reaches the raw diff of any node, works by keyboard, passes an automated accessibility check, and supports both themes | I5, I6 |
| A11 | Prompt variants load from Markdown without code changes, and the run records which was used | Decision above |
| A12 | The model adapter is swappable by flag; analyzed code cannot trigger tool use | Decision above |
| A13 | Works from a local path and from a clone URL, between any two commits | Goal |

## Non-goals

See concept section 9. A summary explains apparent behavior; it does not verify correctness (I12).
