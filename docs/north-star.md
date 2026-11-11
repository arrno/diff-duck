# North Star: diffduck v1

Product intent and invariants live in [concept.md](concept.md) (I1–I12). This document only
records what v1 includes, the decisions made for it, and how it is accepted.

## Goal

`diffduck <repo-url-or-path> <base> <head>` turns the Git diff between any two commits
into a compressed diff: structured JSON plus one self-contained HTML viewer.

The CLI doubles as the evaluation harness: it runs against any Git project and any commit pair.

`diffduck serve` is the same pipeline as a local web app: point it at a repo, pick two commits,
and open, list, or delete the diffs it keeps.

`diffduck up`, `down`, `status`, and `reset` run that app in the background, and `diffduck diff`
drives it from a shell, so a person or a coding agent can say "diffduck it" and get a page:
`diffduck diff [repo] -f <from> -t <to>`, `diff list`, `diff show`, `diff open`, `diff delete`, `settings` (`set`, `clear-key`), `version`, and `update`.

## Decisions

| Topic | Decision |
| --- | --- |
| Language | Go through `go/parser`. Python, JavaScript/TypeScript, Rust, C#, and Java through tree-sitter grammars compiled to WASM and run by wazero, so the build stays cgo-free (one module per language, embedded; see [the epic](epics/languages-models-reading-level.md)). Other languages add a `SourceAnalyzer` adapter. |
| Input | A GitHub (or other https/ssh) repo URL, or a local path to a Git repo. |
| Quality goal | Structural facts are deterministic and strictly tested. Summaries are judged for quality, not determinism. |
| Prompts | Markdown files, one directory per variant, injected with `--prompts`. Every run records the variant and prompt hashes. |
| Model access | A `LanguageModel` port, opened per run by a `ModelOpener` from app-wide settings. Providers: Claude Code (`claude-cli`, default), Codex (`codex-cli`), Anthropic API, OpenAI API; API keys live in the OS keychain. `--adapter` pins a provider. Each run has a call limit. |
| Reading level | Five levels (2nd grade to PhD), saved app-wide, default 6th grade. Each level's instructions are Markdown in the prompt variant and part of the cache identity. |
| UI | React and shadcn/ui in `clients/viewer`, built into one HTML page. Standalone, the page holds one diff and needs no server. Under `diffduck serve`, the same page is the app. This is an agreed exception to `ui-architecture.md`: no Next.js or login, because the app is local to one person. Its component, theming, accessibility, and Playwright rules still apply. |
| Local app | Listens only on `127.0.0.1`. Every request must name this server in `Host` and must not come from another site, because the API runs git and spends model calls. Every diff is saved when requested, one runs at a time, and a failed or stopped run stays listed. |
| App commands | The CLI is a client of the app's own HTTP API, so the browser and the shell share one server, one queue, and one database. The running server records its PID and port in `server.json` in the diffduck home (`$DIFFDUCK_HOME`, default `diffduck` in the user config directory); a command trusts the record only when `/api/health` answers with that PID. `diff` starts the app when none is running. Relative repo paths are resolved by the caller, not the server. |
| Storage | SQLite through `modernc.org/sqlite`, which needs no cgo, behind the `DiffStore` port. Saved diffs hold source code, so the file is private to the user. |
| Review | One integrated review before verification, instead of one per step. |

## v1 scope

In: everything in concept sections 3, 4, 5 and 6, with Go code units and file-level nodes for all
other files, identical-body rename detection, semantic aliases, uncertainty flags, and a response
cache so prompt tuning does not re-bill unchanged calls.

Out: move and extraction matching beyond identical bodies, separating refactors from behavior
changes, configurable summary depth, GitHub PR integration, hosting, and login. Other users of the
same machine can reach the loopback port, so the app assumes a single-user machine.

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
| A14 | From the UI, a person can load a repo's commits, pick two, and open the resulting diff | Goal |
| A15 | Diffs are saved automatically and survive a restart; deleting asks first and removes them for good | Goal |
| A16 | A failed or interrupted run stays listed with its reason and can be retried | Decision above |
| A17 | Requests from other sites and through other host names are refused | Decision above |
| A18 | From a shell, `up`, `diff`, `diff list`, `diff delete`, `reset`, and `down` drive the same app as the UI; a stale record is ignored; `reset` never deletes without `--yes` or a typed yes | Decision above |

## Non-goals

See concept section 9. A summary explains apparent behavior; it does not verify correctness (I12).
