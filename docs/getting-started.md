# Getting Started

## Build And Run

Requires Go 1.24.2+.

```bash
go build -o bin/packetcode ./cmd/packetcode
./bin/packetcode
```

On Windows:

```powershell
go build -o bin/packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe
```

First run starts a setup wizard. Pick a provider, paste an API key if that provider needs one, choose a model, and packetcode saves `~/.packetcode/config.toml`.

Ollama is keyless. packetcode defaults to `http://localhost:11434`; start or expose Ollama there, then choose `ollama` during setup. For a remote daemon, set `[providers.ollama].host` or `PACKETCODE_OLLAMA_HOST`.

## Starting Later

```bash
packetcode
packetcode --provider gemini --model gemini-2.5-pro
packetcode --resume <session-id>
packetcode --trust
packetcode doctor
```

`--provider` only works for providers already configured in `~/.packetcode/config.toml` or available without a key. Use the provider picker to add missing keys. To add a custom OpenAI-compatible endpoint, add a `[providers.<slug>]` block with `type = "openai_compatible"` and `base_url`, then run `packetcode doctor`.
Use `packetcode doctor` before starting the TUI when setup, permissions, git, provider config, or MCP startup looks suspect. Add `--json` for machine-readable output.

## Everyday Keys

| Key | Action |
| --- | --- |
| `Enter` | Send the prompt. |
| `Shift+Enter` | Insert a newline. |
| `Ctrl+C` | Cancel the current turn; press again while idle to quit. |
| `Ctrl+L` | Clear packetcode's live transcript pane. |
| `Ctrl+P` | Open the provider picker. |
| `Ctrl+M` | Open the model picker. |

Finalized output is printed into your terminal scrollback. Use your terminal scroll, `Shift+PageUp`, or tmux copy mode to review older turns.

## Approvals

packetcode asks before file writes, patches, shell commands, background-agent writes, and MCP tool calls.

| Key | Action |
| --- | --- |
| `Y` | Approve. |
| `N` / `Esc` | Reject. |

Trust mode skips approvals for the current session:

```bash
packetcode --trust
```

or:

```toml
[behavior]
trust_mode = true
```

## Slash Commands

Type `/` to open autocomplete. Useful commands:

| Command | Action |
| --- | --- |
| `/help` | Show available keys and commands. |
| `/provider` | Open the provider picker. |
| `/model` | Open the model picker. |
| `/spawn <prompt>` | Start a read-only background agent. |
| `/spawn --write <prompt>` | Start a write-capable background agent in an isolated git worktree. |
| `/agents` | Open Agent View for live background-agent status and actions. |
| `/agents <id>` | Open one background-agent transcript. |
| `/jobs` / `/jobs <id>` | List jobs or open a transcript. |
| `/cancel <id\|all>` | Cancel one active job or all active jobs. |
| `/mcp` | Inspect configured MCP servers and tools. |
| `/sessions` | List saved sessions. |
| `/sessions rename <name>` | Rename the current session. |
| `/queue` | Inspect queued foreground prompts. |
| `/compact` | Summarize older conversation context. |
| `/clear` | Clear the live pane only; saved session data remains. |
| `/exit` | Quit. |

Unknown slash commands show an error. Use `//text` when you want to send a prompt that starts with `/`.

The agent also has read-only code-intelligence tools for symbols, likely definitions, references, and syntax diagnostics. These are model-facing tools rather than slash commands, so you can ask natural questions like "find the definition of X" or "show references before editing".

Agent View keys: `p` peeks, `Enter` or `o` opens a transcript, `c` cancels active jobs, `i` injects a completed result into the next foreground turn, `x` ignores a completed result, and `Esc` or `q` closes the dashboard.

Write-capable background agents require git worktree isolation. They create `~/.packetcode/worktrees/<repo-key>/<job-id>` on branch `packetcode-job-<job-id>` from the current `HEAD`; uncommitted foreground changes are not copied. Packetcode leaves the worktree for review after completion.

Completed jobs include a bounded artifact manifest for changed files, commands, tests, searches, spawned children, and worktree changes. Use `/agents` or `/jobs <id>` to inspect the manifest, then inject a compact handoff into the foreground conversation only when you choose. Model-initiated `collect_agent_results` is approval-gated in the foreground and only returns compact summaries/manifests.
