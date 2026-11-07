# Epic: Languages, Model Picker, Reading Level

Extends [north-star.md](../north-star.md) (v1). Product invariants I1–I12 live in
[concept.md](../concept.md); layer rules in [architecture.md](../architecture.md); UI rules in
[ui-architecture.md](../ui-architecture.md) with the v1 exception recorded in north-star.md.

## North Star

**Goal.** DiffDuck compresses diffs in Go, Python, JavaScript/TypeScript, Rust, C#, and Java;
the person picks which model writes the summaries; and summaries are written at a reading
level the person chooses.

### Expected behavior

1. **Languages.** Python (`.py`), JS/TS (`.js .jsx .mjs .cjs .ts .tsx .mts .cts`), Rust (`.rs`),
   C# (`.cs`), and Java (`.java`) files produce code-unit trees (package/module > type >
   method/field, package/module > function) like Go does. Go keeps `go/parser`. A file that
   fails to parse is still represented as a plain file node, as today (I1).
2. **Model picker.** The Model settings popover offers four providers:
   - **Claude Code** — the local, logged-in `claude` CLI (existing `claudecli` adapter).
   - **Codex** — the local, logged-in `codex` CLI.
   - **Anthropic API** — needs an API key.
   - **OpenAI API** — needs an API key.

   A CLI provider whose binary is not on `PATH` is shown unavailable. API keys are stored in
   the OS keychain; the browser can learn only whether a key is saved, never read it back.
   A saved key can be cleared. Each provider uses one default model; the CLI `--model` flag
   still overrides it.
3. **Reading level.** The five slider levels — 2nd grade, 6th grade, High school, College, PhD —
   persist app-wide (default: 6th grade). Every new diff instructs the model to write its
   summaries, parent summaries, and outcomes at that level.
4. **Settings are app-wide** and apply to the next diff started; a running diff keeps the
   settings it started with. Each saved diff's generation info records its provider and
   reading level.

### Domain concepts

`ModelProvider` (claude-code, codex, anthropic-api, openai-api), `ReadingLevel` (five ordered
levels), `ModelSettings` (provider + reading level), plus the existing `Generation`.

### Invariants

- **K1** An API key never reaches SQLite, logs, error messages, the response cache key, HTTP
  responses, or the browser after it is submitted. It lives only in the OS keychain and,
  transiently, in process memory (building a model, or checking a key exists).
- **K2** Analyzed code cannot trigger tool use in any provider (extends A12). Where a provider
  cannot fully disable tools (Codex), it runs read-only, sandboxed, with approvals never
  requested, in an empty scratch directory, and the residual risk is documented.
- **K3** Reading level text is loaded from Markdown in the prompt variant directory, not
  hard-coded (extends A11), and changes the cache identity, so a cached summary written at one
  level is never served for another.
- **K4** Structural facts for every new language come only from parsing (I11); a parse failure
  degrades to a file node, never invented structure.
- **K5** Settings validation lives in Domain: unknown providers or levels are rejected.

### Constraints

- No cgo. Tree-sitter grammars run as WASM in a pure-Go runtime (wazero). If this proves
  infeasible, stop and escalate — do not silently switch to cgo.
- OS keychain through a pure-Go library (`github.com/zalando/go-keyring`).
- Local app stays loopback-only with the existing Host/Origin checks; the new settings
  endpoints get the same protection.
- `--adapter` passed explicitly to `serve` pins the provider (verification uses
  `scripted:*`); the API reports the pin and the UI shows the picker as set by the server.

### Non-goals

Choosing a model ID in the UI; per-diff settings; Google as a provider; cross-language move or
extraction matching; separating refactors from behavior changes.

### Acceptance criteria

| # | Criterion |
| --- | --- |
| B1 | A fixture diff touching each new language yields unit nodes with correct names, kinds, nesting, and line ranges; coverage stays 100% |
| B2 | A syntactically broken file in any new language appears as a file node with a warning |
| B3 | Choosing each provider in the popover and saving makes the next diff use it (visible in its generation info) |
| B4 | An unavailable CLI provider cannot be chosen; an API provider without a saved key cannot be saved |
| B5 | A saved key survives restart, is never returned by the API, and can be cleared |
| B6 | Reading level persists across restart, reaches every prompt (leaf, parent, outcomes), and changes the cache identity |
| B7 | `serve --adapter X` pins the provider and the UI says so |
| B8 | Settings endpoints refuse cross-site and foreign-Host requests (A17) |
| B9 | `make verify` passes |

## Plan (Architecture)

```text
Domain:       settings.go — ModelProvider, ReadingLevel, ModelSettings (+validation).
              Generation gains Provider and ReadingLevel. New UnitKinds as needed for
              classes/enums/traits/etc.
Services:     SettingsKeeper — read/save settings, save/clear keys, report availability.
              DiffLibrary — at run start, read settings and open a model via ModelOpener.
              DiffCompressor/SemanticCompressor/OutcomeIdentifier — append reading-level
              instructions to each system prompt.
Ports:        SettingsStore (persist ModelSettings), SecretStore (save/has/clear key per
              provider), ModelOpener (ModelSettings -> LanguageModel, availability).
              PromptSource gains reading-level loading (or reading levels load as named prompts).
Repositories: SettingsStore implemented by sqlitestore.
Adapters:     treesitter (WASM runtime + one extractor per language), codexcli, openaiapi,
              anthropicapi (explicit key option), keyring (SecretStore), sqlitestore settings.
Controller:   GET/PUT /api/settings, DELETE /api/settings/keys/{provider}; ModelOpener
              composition (cli package); --reading-level flag on one-shot CLI; analyzers list.
Client UI:    ProviderMenu wired to the settings API.
Tests:        per layer, plus fixtures per language and Playwright for the popover.
```

## Steps

| # | Step | Depends | Blocks |
| --- | --- | --- | --- |
| S1 | Domain settings types and Generation fields | — | S4, S5 |
| S2 | Tree-sitter WASM runtime + Python analyzer + new UnitKinds + multi-analyzer wiring | — | S7 |
| S3 | Model adapters: codexcli, openaiapi, anthropicapi explicit key | — | S5 |
| S4 | Settings persistence: SettingsStore (SQLite), SecretStore (keyring), SettingsKeeper | S1 | S5, S6 |
| S5 | Run-time model selection (ModelOpener) and reading level in prompts + cache identity | S1, S3, S4 | — |
| S6 | Settings HTTP API and ProviderMenu UI | S4 | — |
| S7 | JS/TS, Rust, C#, Java analyzers (parallel, one per language) | S2 | — |

### Ownership decisions (where two steps could claim the same thing)

- `internal/domain/compressed.go`: S1 adds Generation fields; S2 adds UnitKind constants.
  Neither touches the other's lines.
- `LibraryDeps.Analyzer` becoming a list: **S2**.
- `LibraryDeps.Model` becoming a `ModelOpener`: **S5**.
- Viewer labels/icons for new UnitKinds: **S2**. ProviderMenu: **S6**.
- Default model IDs per provider: **S5** (one constant per provider, in the controller).
- Provider availability (binary on PATH): adapters expose it; **S5** wires it into ModelOpener;
  S6 only displays it.

### Settings API contract (S4 service shape, S6 transport)

```text
GET /api/settings ->
  { "provider": "claude-code"|"codex"|"anthropic-api"|"openai-api",
    "readingLevel": "2nd-grade"|"6th-grade"|"high-school"|"college"|"phd",
    "pinned": "" | "<adapter set by --adapter>",
    "providers": [ { "id": "...", "label": "...", "available": bool,
                     "needsKey": bool, "keySaved": bool } ] }
PUT /api/settings  { "provider", "readingLevel", "apiKey"?: string } -> same as GET
DELETE /api/settings/keys/{provider} -> same as GET
```

`apiKey`, when present and non-empty, is saved for the chosen provider. Saving an API
provider with no key saved and none given is a 400.

## Handoff notes

- **S1 done** (branch `worktree-agent-a357a53ac4e4d559e`, 4cbd6ad). The run-metadata struct is
  `domain.Generation`, not GenerationInfo.
- **For S5:** `diff_compressor.go` (around line 158) replaces `result.Generation` with the
  runner's stats and copies only Model, PromptVariant, and Prompts. Set Provider and
  ReadingLevel *after* that swap, and test that both survive the model path.
- **For S5 (K3):** load reading-level Markdown and build cache identity only from a level that
  passed `domain.ParseReadingLevel`, never a raw string (path traversal).
- **For S6:** the viewer's TS `Generation` type needs `provider` and `readingLevel`.

- **S4 done** (merged). Ports: `SettingsStore`, `SecretStore`, `KeySource` (LoadKey, only for
  model construction), `ProviderAvailability`. Service `SettingsKeeper`: `Settings`, `Current`,
  `Save(provider, level, apiKey)`, `ClearKey(provider)`; errors `ErrInvalidSettings`,
  `ErrProviderUnavailable`, `ErrKeyRequired` (all -> 400). `SettingsView` JSON matches the API
  contract. Keychain adapter: `keyring.New()` implements SecretStore and KeySource.
- **For S5:** compose `keyring.New()` as KeySource for model building; implement
  `ProviderAvailability` once and share it with ModelOpener (no second availability method);
  call `SettingsKeeper.Settings` at run start. A selected API provider whose key was cleared
  must fail the run with a clear "no API key saved" error (`ports.ErrKeyNotSaved`).
- **For S6:** never log or echo request bodies; `http.MaxBytesReader` on PUT; Host/Origin checks
  on PUT and DELETE; saving while pinned is allowed (the pin only overrides the provider).
