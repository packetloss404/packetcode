# Completed Deferred Roadmap

This is the execution plan for the items that were listed under
**Deferred to a future release** in `CHANGELOG.md`. Each round was a
cohesive, independently-shippable slice: a planning agent + 1–3
implementation agents + a commit, mirroring the pipeline used for the
background-agents feature.

**All seven rounds complete — see git log for the commits.**

Rounds were ordered by a mix of user value and dependency chain — the
first two were "pay off the promise" rounds (finishing slash commands
the UI already hinted at), and rounds 5–7 were bigger architectural
bets.

---

## Round 1 — Slash-command parsing for the remaining commands

**Landed.** See `docs/feature-slash-commands.md` for the spec and the
git log for the commit that shipped `/provider`, `/model`, `/sessions`,
`/undo`, `/compact`, `/cost`, `/trust`, `/help`, and `/clear`.

---

## Round 2 — Provider and model selector modals (Ctrl+P / Ctrl+M)

**Landed.** See `docs/feature-picker-modals.md` for the spec and the
git log for the commit that shipped Ctrl+P / Ctrl+M selector modals,
the generic `picker` component, and the shared `applyProviderSwitch` /
`applyModelSwitch` helpers.

---

## Round 3 — Slash-command autocomplete popup

**Landed.** See `docs/feature-slash-autocomplete.md` for the spec and
the git log for the commit that shipped the autocomplete popup, the new
`internal/ui/components/autocomplete` component, and the `aboveInput`
layout slot.

---

## Round 4 — Standalone diff component + richer tool-call rendering

**Landed.** See `docs/feature-diff-component.md` for the full design
spec and the git log for the commit that shipped the
`internal/ui/components/diff` package, the diff-aware approval
renderers for `write_file` and `patch_file` (via new
`WriteFileTool.PreviewDiff` / `PatchFileTool.PreviewPatchDiff`
helpers), and the conversation-side parity rendering for the
completed `patch_file` tool-result block. Rounds 5–7 below are
unchanged.

---

## Round 5 — Real HTTP cancellation on Ctrl+C

**Landed.** See `docs/feature-http-cancellation.md` for the full design
spec and the git log for the commit that shipped the
`App.cancelTurn context.CancelFunc` lifecycle, the per-iteration
`ctx.Err()` guard inside `parseSSE` / `parseGeminiSSE` /
`parseOllamaStream`, the `isCancellation` helper rendering a friendly
"turn cancelled" system line, and the state-machine property that
makes double Ctrl+C during shutdown a safe no-op. Background
`/spawn`'d jobs are not cascaded — their ctx trees derive from the
`jobs.Manager` root.

---

## Round 6 — User-customisable theme via `~/.packetcode/theme.toml`

**Landed.** See `docs/feature-theming.md` for the full design spec and
the git log for the commit that shipped the `internal/ui/theme/loader.go`
loader (Load + Apply + parseHex + rebuildStyles), the `providerColors`
map refactor of `ProviderColor`, `config.ThemePath()`, the startup
wire-up in `cmd/packetcode/main.go`, and the four example presets under
`docs/themes/` (dark-terminal-noir / light / high-contrast /
solarized-dark).

---

## Round 7 — MCP / plugin system

**Landed — final round.** See `docs/feature-mcp.md` for the full
design spec, `docs/mcp.md` for the user-facing guide with worked
examples, and the git log for the commit that shipped the
`internal/mcp` package (Client + Manager + McpTool adapter + stdio
JSON-RPC driver), the `[mcp.<name>]` config block with the
`IsEnabled` pointer-bool contract, the startup wire-up in
`cmd/packetcode/main.go` (parallel spawn, bounded concurrency,
graceful shutdown), and the `/mcp` + `/mcp logs <name>` slash
commands.

---

## Remaining Future Ideas

- **Resumable background jobs across restart.** The session JSONs are
  already persisted, so this is "feed them back into a fresh Agent".
- **Per-job worktree isolation** (concurrent edit-to-different-branches
  story). Genuinely hard; revisit with MCP in mind since MCP workflows
  will push on it.
- **Streaming sub-agent output into the main conversation in real time.**
  Requires a concurrency model change; v1 delivers the summary on
  completion.
- **Cross-job dependencies / DAG scheduling.** Future.
- **Per-tool trust setting** (always-allow `spawn_agent` etc.). Small
  extension to the approval policy.
- **Sub-agent → user questions.** Would require a notification channel
  orthogonal to the current approval modal. Future.

---

## Historical Round Workflow

The pattern the background-agents feature validated:

1. **Plan subagent** writes `docs/feature-<name>.md` with a
   file-by-file change list grouped into implementation buckets.
2. **Backend / backend-adjacent agent** implements the lowest-level
   package with tests. No UI edits.
3. **Integration agent** wires the new backend into App + CLI +
   tools. No UI edits beyond what the handler needs.
4. **TUI + docs + commit agent** does the visible component, README
   and CHANGELOG deltas, commits with a detailed message.

Each agent reads the spec doc, reports deviations precisely so the
next agent knows the real API, and does not overstep the bucket
boundary.
