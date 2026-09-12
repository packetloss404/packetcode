# Getting Started

## Build and Run

packetcode requires Go 1.26.0 or newer.

```bash
make build
./bin/packetcode
```

The first run creates `~/.packetcode/config.toml`. Choose a provider, enter an API key when required, and select a model.

For a Codex ChatGPT subscription, sign in through the official Codex CLI first:

```bash
codex login
packetcode --provider codex
```

For local Ollama, start the standard daemon; packetcode uses `http://localhost:11434` with no key or extra configuration:

```bash
ollama serve
packetcode --provider ollama
```

Useful startup commands:

```bash
packetcode
packetcode --provider codex --model gpt-5.6-sol
packetcode --resume <session-id>
packetcode --permission-mode auto
packetcode --trust
packetcode doctor
packetcode doctor --json
packetcode run --permission-mode read-only --json "summarize this repository"
packetcode skills list
packetcode acp
packetcode sugar login
```

`--provider` requires an already-configured provider, except keyless Codex/Ollama. Use `Ctrl+P`, `/provider`, or `/provider add <slug>` to add a hosted provider key.
The default invocation opens the TUI; `run`, `doctor`, `skills`, `acp`, and
`sugar` are the other public command families.

`packetcode run [--provider NAME] [--model MODEL] [--permission-mode MODE]
[--resume ID] [--json] <prompt...>` runs one headless turn. It does not read a
prompt from stdin. Approval requests fail closed with exit 3; cancellation exits
130. Plain stdout contains only the final response, while JSON returns one
versioned object with identity, output, elapsed time, usage, and any error.
If a run fails after creating a session, stderr shows its ID and how to open
saved history with `packetcode --resume ID` from the same directory. Review
completed actions before continuing: cancellation or failure does not undo
tool actions. An approval-blocked run can be continued interactively after
reviewing the requested action.

## Everyday Keys

| Key | Action |
| --- | --- |
| `Enter` | Send. |
| `Ctrl+J` / `\` then Enter | Portable newline; `Alt+Enter` also works when Alt is reported distinctly. Shift+Enter works only when the terminal maps it to Ctrl+J. |
| `Up` / `Down` | Prompt history at the input boundary. |
| `Shift+Tab` | Cycle Manual → Accept Edits → Auto → Plan, even during a turn. |
| `Left` on an empty prompt | Open Agent View. |
| `Ctrl+P` / `/model` | Provider/model picker; `Alt+M` also works when Alt is reported distinctly. |
| `Ctrl+C` | Cancel active work, clear a draft, or quit from an empty prompt. |
| `Ctrl+L` | Clear visible output, keep the saved session. |

Finalized output goes to terminal scrollback. The bottom live region contains only current thinking, streaming text, tool output, input, context status, and permission mode.

## Prompts and Context

- Type `/` for command completion.
- Type `@` at a token boundary for fuzzy project-file completion.
- Submit during an active turn to queue the prompt.
- Use `//text` to send a prompt beginning with a literal `/`.
- Use `/compact` to summarize older history manually; automatic compaction also runs at the configured threshold.

The context gauge is current occupancy, not cumulative billed tokens.

## Approvals and Modes

The numbered approval menu supports arrows, Enter, `1`/`2`/`3`, and the legacy `Y`/`A`/`N` shortcuts. Option 2 remembers a session rule; shell approvals remember the exact command. `/permissions reset` revokes session rules and restores the startup policy.

Shift+Tab remains active while the model is thinking or streaming. If an approval is already visible, switching to a mode that allows or denies the action resolves it immediately. Already-running processes are not interrupted.

Bypass Permissions is intentionally separate from the Shift+Tab cycle:

```bash
packetcode --trust
```

or `/trust on` for the current session. Explicit deny rules remain effective.

## Agents, Loops, and Workflows

```text
/spawn audit the current diff
/spawn --write fix the focused tests
/agents
/workflows run review target="the staged diff"
/loop Continue improving the implementation until complete
/loop 15m /workflows run review
```

Read-only agents share the project root. Write-capable agents receive a dedicated git worktree. Results remain outside foreground model context until explicitly collected or injected.

## Where Next

- [Providers and models](providers.md)
- [Configuration](configuration.md)
- [Security and permissions](security.md)
- [Background agents and workflows](feature-background-agents.md)
- [Troubleshooting](troubleshooting.md)
