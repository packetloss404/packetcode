# Slash Commands — Round 1 Design Spec

## Summary

Extend packetcode's slash-command surface from the three verbs already wired (`/spawn`, `/jobs`, `/cancel`) to the full nine promised by the UI keymap and README: `/provider`, `/model`, `/sessions`, `/undo`, `/compact`, `/cost`, `/trust`, `/help`, `/clear`. Each handler is a thin adapter over existing backend APIs (`provider.Registry`, `session.Manager`, `session.BackupManager`, `cost.Tracker`, `agent.ContextManager`, `uiApprover`) and appends its output as a monospace system message via `conversation.AppendSystem`, reusing the ASCII-table aesthetic `/jobs` introduced. No new UI components, no confirmation modals: destructive verbs either take an explicit `--yes` flag or are judged safe enough to execute immediately.

## Behaviour — per command

### `/provider`

- **Syntax.** `/provider` | `/provider <slug>`
- **Args.** 0 or 1 positional.
- **Behaviour.**
  - 0 args: list registered providers from `Registry.List()`. Mark the active one with `*`. Columns: slug, display name, active-default-model (from `cfg.Providers[slug].DefaultModel`), status dot.
  - 1 arg: treat as target slug. Resolve via `Registry.Get(slug)`. If unknown, error. Otherwise:
    1. Pick the model: try `cfg.Providers[slug].DefaultModel`; if empty, call `prov.ListModels(ctx)` (2s timeout) and pick the first entry's `ID`; if that call errors or returns empty, fail with a message telling the user to pass a model via `/model`.
    2. Call `Registry.SetActive(slug, modelID)`.
    3. Refresh top bar via `a.refreshTopBar()`.
    4. Emit `switched provider: <slug> (<modelID>)`.
  - Rationale for the fallback: users shouldn't have to remember a two-step dance; defaults in config already exist for this exact reason.
- **Output format (no arg).**
  ```
  PROVIDER   NAME           DEFAULT MODEL               ACTIVE
  * openai   OpenAI         gpt-5.5                     yes
    gemini   Google Gemini  gemini-2.5-flash            no
    ollama   Ollama         (none)                      no
  ```
  Fixed column widths: slug=10, name=14, model=28, active=5. `(none)` when `DefaultModel` is empty.
- **Error cases.**
  - Unknown slug: `provider: unknown provider "<slug>"`.
  - No default model and `ListModels` fails/empty: `provider: <slug> has no default model; run /model <id> after switching`.
  - `SetActive` error: surface as `provider: <err>`.

### `/model`

- **Syntax.** `/model` | `/model <id>`
- **Args.** 0 or 1 positional.
- **Behaviour.**
  - 0 args: fetch models from the active provider via `prov.ListModels(ctx)` (2s timeout). Render as a table.
  - 1 arg: call `Registry.SetActive(activeSlug, <id>)`. No validation against `ListModels` — matches `SetActive`'s existing contract. Refresh top bar. Emit `switched model: <slug>/<id>`.
- **Output format (no arg).**
  ```
  MODEL                        CONTEXT    TOOLS  IN/1M    OUT/1M
  * gpt-5.5                    1050000    yes    $5.00    $30.00
    gpt-4.1                    1048576    yes    $2.00    $8.00
    gpt-4.1-mini               1048576    yes    $0.40    $1.60
  ```
  Active model marked with `*`. `IN/1M`/`OUT/1M` pulled from `Model.InputPer1M`/`OutputPer1M`; render as `$%.2f` or `free` when zero. Context column is the `Model.ContextWindow` integer, or `?` when 0.
- **Error cases.**
  - No active provider: `model: no active provider`.
  - `ListModels` error: `model: list failed: <err>`.

### `/sessions`

- **Syntax.** `/sessions` | `/sessions resume <id>` | `/sessions delete <id>` | `/sessions delete <id> --yes`
- **Args.** `args[0] ∈ {"resume","delete"}` optionally; `args[1]` is the session id.
- **Behaviour.**
  - Bare `/sessions`: `Manager.List()`, sort newest-first (already does), render the top 20 with id-prefix (8 chars), age, provider/model, name. Mark the current session with `*`.
  - `resume <id>`: exact-match on full ID first; if no hit, accept any unique 8-char prefix; ambiguous prefix → error. Call `Manager.Load(fullID)`. Refresh top bar. Emit `resumed session <name> (<id-prefix>) — N messages`.
  - `delete <id>` without `--yes`: refuse with `sessions: refusing to delete without --yes; re-run: /sessions delete <id> --yes`.
  - `delete <id> --yes`: same ID-prefix resolution. Call `Manager.Delete(fullID)`. If the deleted ID was current, `Current()` returns nil afterwards. Emit `deleted session <id-prefix>`.
- **Output format (bare `/sessions`).**
  ```
  ID        NAME                                     AGE    PROV/MODEL             ACTIVE
  * 7f3a12ab refactor-userservice                    3m     openai/gpt-5.5         yes
    9b21ff00 audit-test-suite                        2h     gemini/2.5-flash       no
    c4dd0001 rust-port-experiment                    1d     ollama/qwen2.5-coder   no
  ```
  Widths: id=8, name=40, age=6, prov/model=22, active=5. Truncate longer names with `...`. Age uses a promoted `roundedDuration` helper that handles days (`1d` / `2h` / `15m` / `45s`).
- **Error cases.**
  - Unknown/ambiguous id: `sessions: no session matches "<id>"` / `sessions: ambiguous prefix "<id>" — matches N sessions`.
  - `List` error: `sessions: list failed: <err>`.
  - `Load`/`Delete` error: `sessions: <err>`.
  - Unknown subcommand: `sessions: unknown subcommand "<x>" (want "resume" or "delete")`.

### `/undo`

- **Syntax.** `/undo`
- **Args.** None.
- **Behaviour.**
  - Requires a `session.BackupManager` reachable via a new `a.backups` field populated from `Deps.Backups`. `main.go` already constructs one per session.
  - Call `backups.Undo()`. If the returned path is empty and err nil, emit `nothing to undo`. Otherwise emit `restored <path> (depth now: N)` where N = `backups.Depth()`.
  - Execute immediately (no confirmation). Rationale: the user explicitly approved the original write; reverting it is lower-risk than the write itself.
- **Error cases.**
  - No BackupManager wired: `undo: backups not available`.
  - `Undo` error: `undo: <err>`.

### `/compact`

- **Syntax.** `/compact` | `/compact --keep <N>`
- **Args.** Optional `--keep <int>` (default 10). Any other token → error.
- **Behaviour.** Blocks the UI for the round-trip.
  1. Resolve active provider/model via `a.deps.Registry.Active()`. Error if none.
  2. Resolve the context manager: one instance stored on `App` at construction, `a.contextMgr = agent.NewContextManager(cfg.Behavior.AutoCompactThreshold)`.
  3. Read `before := a.deps.Sessions.Current().Messages`. Error if `Current() == nil`.
  4. Compute `beforeTok := a.contextMgr.EstimateTokens(before)`.
  5. Emit `compacting context... (~<beforeTok> tokens)` as a system message immediately so the user sees progress.
  6. Call `a.contextMgr.Compact(ctx, prov, modelID, before, keep)` with a 120s `context.WithTimeout`.
  7. On success, mutate the session: replace `Current().Messages`. Call `Sessions.Save()` to persist.
  8. Compute `afterTok := a.contextMgr.EstimateTokens(after)` and emit `compacted: <beforeTok> → <afterTok> tokens (kept <keep> recent messages)`.
  9. Refresh top bar to pick up the reduced `TokenUsage` display.
- **Error cases.**
  - No active provider: `compact: no active provider`.
  - No current session: `compact: no session loaded`.
  - `keep` not a positive int: `compact: --keep must be a positive integer`.
  - Compact returned error or timeout: `compact: <err>`.

### `/cost`

- **Syntax.** `/cost` | `/cost reset` | `/cost reset --yes`
- **Args.** 0 args, or `"reset"` with optional `"--yes"`.
- **Behaviour.**
  - Bare `/cost`:
    1. Total: `tracker.TotalCost()`.
    2. Breakdown: `tracker.Breakdown()`; sort descending by USD; take top 5.
    3. Format table as below. If `len(Breakdown()) == 0` render `no usage recorded yet`.
  - `reset` without `--yes`: `cost: refusing to reset without --yes; re-run: /cost reset --yes`.
  - `reset --yes`: call `tracker.Reset()`. Refresh top bar. Emit `cost tally cleared`.
- **Output format (bare `/cost`).**
  ```
  Total: $1.2345 (since 2026-04-10 14:30 UTC)

  SESSION   PROV/MODEL              TOK(IN/OUT)   USD
  7f3a12ab  openai/gpt-5.5          84K/12K       $0.8400
  9b21ff00  gemini/2.5-flash        120K/18K      $0.2100
  c4dd0001  openai/gpt-4.1-mini     30K/5K        $0.0800
  [showing top 5 of 14 sessions]
  ```
  N = 5 rows. Session ID truncated to 8 chars. Tokens rendered with `K` suffix at ≥10k (rounded to integer). USD uses `$%.4f` for precision. Only show the "[showing top 5 of N]" footer when total entries > 5.
- **Error cases.**
  - No `CostTracker` wired: `cost: cost tracker not available`.
  - `reset` error: `cost: reset failed: <err>`.
  - Unknown subcommand: `cost: unknown subcommand "<x>" (want "reset")`.

### `/trust`

- **Syntax.** `/trust` | `/trust on` | `/trust off`
- **Args.** 0 or 1.
- **Behaviour.**
  - Bare: emit `trust mode: on` or `trust mode: off` based on `a.approver.IsTrusted()`.
  - `on`: `a.approver.SetTrust(true)`; emit `trust mode enabled — destructive tools will auto-approve`.
  - `off`: `a.approver.SetTrust(false)`; emit `trust mode disabled — destructive tools will prompt`.
  - No `--yes` required either direction.
- **Error cases.**
  - Unknown arg: `trust: unknown value "<x>" (want "on" or "off")`.

### `/help`

- **Syntax.** `/help`
- **Args.** None accepted; extras silently ignored.
- **Behaviour.** Render every group from `keymap.go` as sections. Hard-coded section order + headers.
- **Output format.** Single system message, the five sections concatenated with a blank line between each. Key column width: 20 chars. Overflow keys wrap to the next line with an empty key slot.
- **Error cases.** None.

### `/clear`

- **Syntax.** `/clear`
- **Args.** None.
- **Behaviour.** Identical to the existing `Ctrl+L` path: replace `a.conversation` with a fresh `conversation.New()`, reapply version. Do not touch the session's on-disk messages. Execute immediately.
- **Output format.** None (the visible effect is the cleared pane).
- **Error cases.** None.

## Parser changes

Decision: **flatten the parser to a single allow-list, push sub-arg parsing into handlers.**

```go
var knownSlashCommands = map[string]struct{}{
    "spawn":    {},
    "jobs":     {},
    "cancel":   {},
    "provider": {},
    "model":    {},
    "sessions": {},
    "undo":     {},
    "compact":  {},
    "cost":     {},
    "trust":    {},
    "help":     {},
    "clear":    {},
}

func ParseSlashCommand(text string) (cmd string, args []string, ok bool) {
    trimmed := strings.TrimSpace(text)
    if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
        return "", nil, false
    }
    body := strings.TrimPrefix(trimmed, "/")
    if body == "" {
        return "", nil, false
    }
    fields := strings.Fields(body)
    name := fields[0]
    if _, hit := knownSlashCommands[name]; !hit {
        return "", nil, false
    }
    return name, fields[1:], true
}
```

New small parsers co-located in `internal/app/slashcmd.go`:

- `parseCompactFlags(args []string) (keep int, err error)`
- `parseSessionsArgs(args []string) (sub, id string, yes bool, err error)`
- `parseCostArgs(args []string) (reset, yes bool, err error)`
- `parseTrustArgs(args []string) (set, value bool, err error)`

## Confirmation policy

| Command              | Policy        | Justification                                                                          |
| -------------------- | ------------- | -------------------------------------------------------------------------------------- |
| `/clear`             | immediate     | Conversation history is persisted to disk; clearing only the pane is cosmetic.         |
| `/undo`              | immediate     | Reversing a single file change the user explicitly approved is low-risk.               |
| `/trust on`          | immediate     | User is escalating their own session only.                                             |
| `/trust off`         | immediate     | De-escalation, strictly safer than the current state.                                  |
| `/provider <slug>`   | immediate     | Switch affects only future turns; reversible.                                          |
| `/model <id>`        | immediate     | Same reasoning.                                                                        |
| `/compact`           | immediate     | Cost is bounded (one LLM round trip).                                                  |
| `/sessions resume`   | immediate     | Load is reversible by resuming a different id.                                         |
| `/sessions delete`   | **`--yes` required** | Irreversible.                                                                   |
| `/cost reset`        | **`--yes` required** | Resets the global tally and `StartTime`; unrecoverable.                         |

No `ConfirmModal` component is introduced this round.

## File-by-file change list

### Bucket A — Implementation

| Path | What changes |
|---|---|
| `internal/app/slashcmd.go` | Replace the hard-coded switch in `ParseSlashCommand` with the `knownSlashCommands` allow-list. Add the four small arg parsers. |
| `internal/app/app.go` | (a) Add fields: `backups *session.BackupManager`, `contextMgr *agent.ContextManager`. (b) In `New`, build `contextMgr = agent.NewContextManager(cfg.Behavior.AutoCompactThreshold)`. (c) Extend `Deps` with `Backups *session.BackupManager`. (d) Drop any `a.jobs == nil` short-circuit at the top of `handleSlashCommand`. (e) Extend the switch in `handleSlashCommand` with nine new cases. |
| `internal/app/slashcmd_provider.go` | **NEW.** `handleProviderCommand(args)` and `handleModelCommand(args)`. |
| `internal/app/slashcmd_sessions.go` | **NEW.** `handleSessionsCommand(args)` + `resolveSessionID(prefix)`. |
| `internal/app/slashcmd_undo.go` | **NEW.** `handleUndoCommand(args)`. |
| `internal/app/slashcmd_compact.go` | **NEW.** `handleCompactCommand(args)`. |
| `internal/app/slashcmd_cost.go` | **NEW.** `handleCostCommand(args)` + `fmtTokensShort(int)`. |
| `internal/app/slashcmd_trust.go` | **NEW.** `handleTrustCommand(args)`. |
| `internal/app/slashcmd_help.go` | **NEW.** `handleHelpCommand(args)`. |
| `internal/app/slashcmd_clear.go` | **NEW.** `handleClearCommand(args)` — refactor Ctrl+L to call this helper too. |
| `cmd/packetcode/main.go` | Pass the session's `BackupManager` into `app.Deps` as `Backups:`. |

### Bucket B — Docs + tests + commit

| Path | What changes |
|---|---|
| `internal/app/keymap.go` | Extend `SlashCommands` with the nine new entries. |
| `internal/app/slashcmd_test.go` | Add parse-case tests per below. |
| `internal/app/app_slashcmd_test.go` | **NEW.** Integration tests per handler. |
| `README.md` | Add "Slash commands" subsection under Features. |
| `CHANGELOG.md` | Under `[Unreleased] → Added`: bullet for slash commands. Remove from Deferred. |
| `docs/roadmap-deferred.md` | Mark Round 1 as landed. |

## Tests

### Parse-layer tests (`internal/app/slashcmd_test.go`)

- `TestParseSlashCommand_Provider` — `/provider`, `/provider gemini`, whitespace tolerance, `/providergemini` → not a command.
- `TestParseSlashCommand_Model` — `/model`, `/model gpt-4.1`, `/model gpt-4.1 extra`.
- `TestParseSlashCommand_Sessions` — `/sessions`, `/sessions resume 7f3a`, `/sessions delete 7f3a --yes`.
- `TestParseSlashCommand_Undo`.
- `TestParseSlashCommand_Compact` — variants with/without `--keep`.
- `TestParseSlashCommand_Cost` — bare, `reset`, `reset --yes`.
- `TestParseSlashCommand_Trust`.
- `TestParseSlashCommand_Help`.
- `TestParseSlashCommand_Clear`.
- `TestParseSlashCommand_UnknownStillReturnsFalse`.

Sub-arg parser tests:

- `TestParseCompactFlags` — default, valid, missing value, non-integer, negative, trailing junk.
- `TestParseSessionsArgs` — empty, resume/delete variants, missing id, with/without `--yes`, unknown sub.
- `TestParseCostArgs` — empty, `reset`, `reset --yes`, bogus.
- `TestParseTrustArgs` — empty, `on`, `off`, unknown.

### Handler-integration tests (`internal/app/app_slashcmd_test.go`)

Helper `newTestApp` constructs an App with fakes. Tests per command:

- **Provider**: list with active marker, switch with default model, fallback to ListModels, unknown slug, no-model fallback.
- **Model**: list, switch, ListModels error.
- **Sessions**: list, resume by full ID, resume by prefix (unique vs ambiguous), resume unknown, delete without `--yes`, delete with `--yes`.
- **Undo**: nothing on empty stack, restore + depth, no BackupManager.
- **Compact**: no session, no provider, succeeds (assert before/after message, file saved), `--keep` invalid.
- **Cost**: empty breakdown, breakdown with footer, reset without `--yes`, reset with `--yes`.
- **Trust**: query reports current, on, off, unknown value.
- **Help**: contains all sections, lists itself, lists all nine new.
- **Clear**: equivalent to Ctrl+L.
- **Dispatch sanity**: unknown slash (defensive), non-jobs verbs work when `a.jobs == nil`.

### Regression tests to keep green

`TestParseSlashCommand_Spawn`, `TestParseSlashCommand_Jobs`, `TestParseSlashCommand_Cancel`, `TestParseSlashCommand_NotACommand`, `TestApp_SpawnInjectsResultIntoNextTurn` — must still pass untouched.

## Out of scope (Round 2+)

- Ctrl+P / Ctrl+M modals (Round 2).
- Slash-command autocomplete popup (Round 3).
- Reusable `ConfirmModal` component.
- Persisted undo stack across restarts.
- `/sessions rename`.
- `/cost --at <date>` historical repricing.
- Autocomplete of session IDs inside `/sessions resume`.
- `/help <verb>`.
