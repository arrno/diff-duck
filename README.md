<p align="center">
  <img src="clients/viewer/favicon.svg" alt="" width="96" height="96">
</p>

<h1 align="center">DiffDuck</h1>

<p align="center"><strong>A compressed diff.</strong> Know what changed, and where, like a duck to water.</p>

<p align="center">
  <img src="docs/preview.png" alt="DiffDuck showing a change: a one-paragraph overview, outcomes on the left, and the package, type, and function tree on the right, each with a one-line description" width="900">
</p>

## About

A raw diff is a wall of red and green in file order. You have to read all of it to learn what it
does. DiffDuck compresses the same diff into something you can take in at a glance, without
hiding any of it.

It slices the change two ways:

- **By outcome: what changed.** A few plain-language results, like "Users can now log in with
  Okta". Pick one and the diff narrows to just the code behind it.
- **By structure: where it changed.** Package, then type, then function, each with a one-line
  description of its change. Start at the top and open only as deep as you need, down to the
  raw lines.

Every line is written for comprehension: short, direct, and easy to skim. Hard facts are kept
exact:

- **Nothing is dropped.** Every changed line traces to a node.
- **Nothing is invented.** Names, locations, and the shape of the tree come from the code, never
  from the model, and anything the model couldn't explain is marked as unexplained.

Also:

- **Local.** Runs on your machine, keeps your diffs in a private local database, and listens only
  on `127.0.0.1`.
- **Agent friendly.** One command builds a diff and prints its link, so your coding agent can
  "diffduck it" for you.

Go code gets the full structural tree today. Other files appear as file-level changes.

## Install

```sh
go install github.com/arrno/diff-duck/cmd/diffduck@latest
```

Or from a clone: `make build`, which writes `bin/diffduck`.

Summaries come from Claude through the `claude` CLI by default (`--adapter anthropic-api` uses
`ANTHROPIC_API_KEY` instead).

## Use

```sh
diffduck up                                # start the app in the background
diffduck diff                              # diff HEAD~1..HEAD of this repo and open it
diffduck diff ~/code/api -f main -t feature --wait
diffduck diff list
diffduck diff open <id>
diffduck diff delete <id>
diffduck reset                             # delete every saved diff
diffduck down
```

**Your diffs are at http://127.0.0.1:7474/.** `diffduck up` and `diffduck status` print that
address, and `diffduck diff` prints a direct link to the diff it builds. In the app you can also
pick commits by hand. Use `diffduck up --port <n>` for another port.

`diffduck diff` starts the app if it is not running. Run `diffduck -h` for everything, including
the one-shot form that writes a standalone HTML file without the app.

## From a coding agent

Add this to your `CLAUDE.md` or `AGENTS.md`:

```md
When I ask you to "diffduck" a change, commit it if it isn't committed, then run
`diffduck diff -f <base> -t <head> --wait` from the repo (defaults: HEAD~1 and HEAD) and give me
the URL it prints.
```

## Data

Saved diffs, the server record, and its log live in the DiffDuck home: `diff-duck` in your user
config directory, or `$DIFFDUCK_HOME`. The server listens only on `127.0.0.1`.
