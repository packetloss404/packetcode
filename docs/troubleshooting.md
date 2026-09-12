# Troubleshooting

Start with:

```bash
packetcode doctor
packetcode doctor --json
```

The doctor checks config, credential sources, providers, state permissions, git/worktrees, native tools, permission policy, and MCP definitions without starting the TUI.

## Provider Is Not Configured

Open `Ctrl+P` or `/provider`, focus the row, and press Ctrl+A. `/provider add <slug>` opens key entry directly.

Keyless exceptions:

- `codex` requires an official Codex CLI ChatGPT login in `~/.codex/auth.json` (`codex login`).
- `ollama` requires a reachable daemon, normally `localhost:11434`.

Anthropic, Gemini, MiniMax, DeepSeek, Grok/xAI, Mistral, OpenAI API, and OpenRouter require developer API keys. Consumer app/CLI subscriptions are not copied into packetcode.

## Gemini CLI No Longer Works

The packetcode `gemini` provider uses Google's developer API directly and does not reuse Gemini CLI authentication. Configure `PACKETCODE_GEMINI_API_KEY` or add the key through the provider picker. If the developer API/model is unavailable to the key, switch providers; the CLI login state is unrelated.

## Model Switch Fails

Use `/model` to load the active account's exact model IDs (`Alt+M` also works when Alt is reported distinctly). Curated fallback catalogs keep some providers selectable when `/models` is unavailable, but the next request remains authoritative. Run `packetcode doctor --check providers` for credential/connectivity failures.

## Ollama Is Unreachable or Slow

```bash
ollama serve
```

Then run `/ollama status`, `/ollama models`, and `/ollama ps`. packetcode defaults to `http://localhost:11434`; override with:

```bash
PACKETCODE_OLLAMA_HOST=ollama.internal packetcode --provider ollama
```

or `[providers.ollama].host`. A CPU-spill warning means the model/context does not fit the available unified-memory GPU budget. Reduce `num_ctx` or choose a smaller quantization/model.

## Context Gauge Looks Wrong

The gauge is current request occupancy, not cumulative tokens. It includes the latest prompt/completion occupancy reported by the provider and can drop after `/compact`. `/cost` remains cumulative.

If a custom statusline disagrees, run `/statusline refresh` and confirm the script uses `context_window.used`/`max` (or the Claude-compatible aliases) rather than accumulating values itself.

If context grows too quickly:

- use `/compact`;
- avoid repeatedly attaching large `@` files;
- inspect unusually large tool/MCP results;
- set background/workflow token budgets;
- verify the model's context metadata in the picker or `/ollama models`.

## Shift+Tab Does Not Change Auto Mode

Shift+Tab cycles Manual → Accept Edits → Auto → Plan and works while a foreground turn is active. Two presses from Manual select Auto. A visible shell approval remains in Accept Edits and resolves after the second press to Auto.

Picker, transcript, Agent, and Workflow workspaces own their keyboard while open; return to chat first. An already-running shell process is not retroactively changed, but later tool actions use the new mode.

## A Tool Was Denied or Auto-Approved Unexpectedly

```bash
packetcode doctor --check permissions
```

Run `/permissions` in the TUI. Session/config rules normally refine profile defaults, but explicit denies and Plan's read-only boundary are safety floors. Option 2 in the approval menu remembers a session rule; shell commands are remembered exactly. Use `/permissions reset` to revoke session changes and restore the startup policy, or `/permissions profile ask` to change only the active profile.

## `/spawn --write` Failed

Write jobs require a trusted git repository and worktree support. Run:

```bash
packetcode doctor --check project,state.worktrees
git status
git worktree list
```

The job fails closed rather than editing the foreground checkout. Inspect successful worktrees with the path shown in `/agents` or `/jobs <id>`.

## Prompts Stay Queued After an Error

A failed turn or compaction pauses pending prompts so dependent work cannot
start after a failed prerequisite. New prompts join the paused queue. Run
`/queue` to inspect it, `/queue drop N` to remove an entry, and `/queue resume`
while idle to continue. `/queue clear` discards pending work and lets you start
fresh. `/clear` only clears the display and does not resume or discard the queue.

## A Headless Run Failed

If a session exists, stderr shows its ID and how to open it interactively with
`packetcode --resume ID` from the same directory. Review saved history before
continuing; completed file edits, commands, or external actions are not rolled
back. An approval-blocked run exits 3 and needs an interactive decision.
Cancellation exits 130. Plain stdout is empty on failure; JSON may retain
incomplete output with `ok: false`.

## Work Was Interrupted by an App Exit

Run `/jobs resubmit` to list eligible recovered jobs using full IDs. Inspect
`/jobs <id>` before rerunning the saved prompt with `/jobs resubmit <id>`.
This starts a new job and preserves the original evidence. Running jobs recover
as abandoned; queued jobs recover as cancelled before starting. Empty or
oversized prompts need a new manual request after inspection. Concurrent
resubmission requests cannot launch duplicate successors.

If new work reports `persistence_failed`, it has not started. Restore writable
storage; do not delete saved jobs as a generic repair. A shutdown save failure
requires restoring storage and retrying shutdown while the process is alive.

## Missing or Truncated Agent Output

Artifact manifests and model-facing tool results are intentionally bounded. Open `/jobs <id>` for the persisted transcript and inspect the worktree for full changes. Older oversized tool output is compacted only in requests sent back to the model; the persisted session remains complete.

## A Workflow Hangs or Fails

Use `/workflows` for live state and `/agents` for child jobs. `/workflows stop <id>` cascades cancellation. Check global job caps, the workflow's 16-agent guard, token budget, provider credentials, and malformed project/user TOML. Project workflow files override user/built-in files and parse errors are surfaced.

## MCP Server Does Not Start

Run `/mcp`, `/mcp status <name>`, `/mcp logs <name>`, and
`/mcp restart <name>`. Logs live at `~/.packetcode/mcp-<name>.log` and are displayed through
a bounded redacted tail. Restart one crashed process in place; restart
PacketCode after changing MCP configuration.
Disabled servers must be enabled in their configuration before restarting the
app. `/mcp status <name>` and `/mcp tools <name>` explain the applicable recovery
path. Reconnection does not repeat failed tool calls.

## Hooks or Statusline Fail

Commands run through PowerShell on Windows and `sh -c` elsewhere, with the project root as working directory. Keep commands deterministic and increase `timeout_sec` if appropriate. packetcode falls back to its native statusline when a custom command fails.

## Cannot Scroll

Finalized output is in terminal-native scrollback. Use terminal scrolling, Shift+PageUp, or tmux copy mode. `/transcript` opens persisted session history. `/clear` and Ctrl+L clear only visible packetcode output.

For key and geometry differences across Windows Terminal, macOS, Linux, WSL,
MSYS, and tmux, see [Supported terminals](supported-terminals.md).

## Unknown Slash Command

Type `/` or `/help` for current commands. Use `//literal` to send a prompt beginning with `/`. Markdown custom commands belong in `~/.packetcode/commands/` or `.packetcode/commands/`.
