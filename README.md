# packetcode

A keyboard-first, multi-provider AI coding agent for the terminal.

> Status: pre-release / active development. This README describes the current main branch, not a tagged 1.0 release.

packetcode runs in your terminal, keeps your project in your hands, and can talk to OpenAI, Anthropic, Gemini, MiniMax, OpenRouter, and Ollama models through one interface. It can read, search, edit, patch, and run commands in the current project. File writes, patches, shell commands, background-agent writes, and MCP tool calls go through approval unless trust mode is enabled.

## Start Here

Install the latest Linux/macOS release:

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | bash
```

Set `INSTALL_DIR=$HOME/.local/bin` to install without sudo. The installer verifies the release checksum before installing and warns if the install directory is not on `PATH`.

```bash
curl -fsSL https://raw.githubusercontent.com/packetloss404/packetcode/main/install.sh | INSTALL_DIR="$HOME/.local/bin" bash
```

Build from source:

```bash
make build
./bin/packetcode
```

On Windows:

```powershell
$commit = git rev-parse --short HEAD
go build -trimpath -ldflags "-s -w -X main.version=dev -X main.commit=$commit" -o bin/packetcode.exe ./cmd/packetcode
.\bin\packetcode.exe
```

First run starts a line-based setup flow: choose a provider, enter an API key if needed, pick a model, and save `~/.packetcode/config.toml`. Ollama does not need a key, but it does need a reachable Ollama server.

Common launch flags:

```bash
packetcode --provider gemini --model gemini-2.5-pro
packetcode --resume <session-id>
packetcode --permission-mode accept-edits
packetcode --trust
packetcode doctor
packetcode doctor --json
packetcode --version
```

`--provider` and `--model` override the saved default for the current run. The provider must already be configured, except for Ollama.
`--permission-mode` overrides the saved approval profile for the current run.
`packetcode doctor` checks local config, providers, state directories, git, native tools, and MCP setup without starting the TUI.

## Docs

- [Getting started](docs/getting-started.md)
- [Providers and models](docs/providers.md)
- [Configuration](docs/configuration.md)
- [Hooks and statusline](docs/hooks-and-statusline.md)
- [MCP servers](docs/mcp.md)
- [Plugins and extension surfaces](docs/plugins.md)
- [Security and permissions](docs/security.md)
- [Agent View](docs/feature-agent-view.md)
- [Packet Computers and Packet Control](PACKETCOMPUTERS.md)
- [Troubleshooting](docs/troubleshooting.md)

## Core Workflow

Type a prompt and press `Enter`. Use `Shift+Enter` for a newline. If you submit while a foreground generation or `/compact` is still running, packetcode queues the prompt, shows it as `You (queued)`, and runs it when the active operation finishes. During a generation, `Ctrl+C` cancels the current provider request, running tool, approval prompt, or queued foreground prompts; pressing `Ctrl+C` again while idle exits.

Destructive actions are governed by the active permission policy. The default policy asks before writes, patches, shell commands, background-agent spawns, and MCP tool calls:

- `Y` approves.
- `N` or `Esc` rejects.
- `--permission-mode accept-edits` auto-approves file edits but still asks for commands, background agents, and MCP.
- `--permission-mode read-only` denies approval-gated tools.
- `--trust`, `trust_mode = true`, or `--permission-mode bypass` auto-approves actions that are not denied by policy rules.
- `/permissions` shows or changes the session policy.

Finalized messages are printed into your terminal scrollback. Use your terminal scroll, `Shift+PageUp`, or tmux copy mode to review older output. `/transcript` opens the current saved session transcript in the transcript viewer. `/clear` and `Ctrl+L` clear packetcode's live transcript pane; they do not delete the saved session.

The top bar shows foreground operation state such as `thinking` or `compacting`, elapsed time, queued prompt count, context usage, active background jobs, and provider/model information. Custom statusline commands receive the same operation data in their JSON snapshot.

## Providers

Configured built-in and custom OpenAI-compatible providers can be switched without restarting:

- `Ctrl+P` or `/provider` (also `/providers`) opens the provider picker.
- `Ctrl+M` or `/model` (also `/models`) opens the model picker for the active provider.
- `/provider <slug>` switches directly.
- `/model <id>` switches directly.
- Accepting `/provider` or `/model` from the `/` autocomplete popup (Tab, or Enter on the bare verb) opens the picker straight away, so you select from a list instead of guessing a slug or id.

To add or update a provider key, open the provider picker with `Ctrl+P` or `/provider`, focus the provider row, then press `Ctrl+A`. `/provider add` opens the same picker, and `/provider add <slug>` opens the same key prompt for a provider. Custom OpenAI-compatible providers are configured under `[providers.<slug>]` with `type = "openai_compatible"` and `base_url`; see [Providers and models](docs/providers.md).

API keys can also be set in the environment:

```text
PACKETCODE_OPENAI_API_KEY
PACKETCODE_ANTHROPIC_API_KEY
PACKETCODE_GEMINI_API_KEY
PACKETCODE_MINIMAX_API_KEY
PACKETCODE_OPENROUTER_API_KEY
```

Environment variables take precedence over `~/.packetcode/config.toml`.

## Code Intelligence

packetcode can use read-only code-intelligence tools to list symbols, find likely definitions, find references, and report syntax diagnostics. Go files are parsed with the standard Go AST; other common languages use bounded lexical heuristics for symbols and references. Results are root-scoped, capped, and formatted as `path:line:column` entries so the agent can navigate large codebases without dumping full files into context. See [Code Intelligence](docs/code-intelligence.md) for limitations.

## Background Agents

`/spawn <prompt>` starts a background agent. Background agents are read-only by default. Use `/spawn --write <prompt>` when a delegated task may need file writes, patches, or shell commands. Write-capable jobs run in a git worktree under `~/.packetcode/worktrees/<repo-key>/<job-id>` on branch `packetcode-job-<job-id>`, based on the current `HEAD`; uncommitted foreground changes are not copied. If git worktree isolation cannot be created, the write job fails instead of editing the main checkout.

`/agents` opens Agent View, which groups jobs by state and shows provider/model, age, token counts, estimated cost, status badges, and recent activity. `Enter` or `/agents <id>` opens the transcript for a selected agent, `p` peeks, `i` injects a completed result, and `c` cancels.

packetcode leaves write-job worktrees in place for inspection. Use `/jobs` or `/agents <id>` to find the path, then inspect with `git -C <path> status` or `git -C <path> diff` before merging or copying changes. To clean one up, run `git worktree remove <path>` from the source repository, then delete the `packetcode-job-<job-id>` branch if you no longer need it.

Completed background jobs keep a compact artifact manifest alongside their summary: changed files, commands or test runs, searches, child jobs, and worktree changes. Packetcode surfaces those artifacts in `/jobs`, Agent View, transcript headers, explicit result injection, `spawn_agent` wait results, and the approval-gated `collect_agent_results` tool for async fan-in. It does not inject raw diffs, command logs, or file contents automatically.

See [Agent View](docs/feature-agent-view.md) for the full workflow.

## MCP Servers

MCP servers are configured under `[mcp.<name>]` and exposed as
provider-safe `<server>__<tool>` aliases. packetcode starts each
configured command as your user, so treat MCP servers as trusted local
code; approval prompts gate tool calls, not the child process itself.
MCP children inherit only a small launch environment allowlist by
default, plus any per-server `env` values or named `env_from`
variables you configure. The
`/mcp` command lists configured servers, `/mcp status <name>` shows
health/config details, `/mcp tools <name>` lists provider-safe callable
tool aliases, and `/mcp logs <name>` shows a bounded, redacted tail of
the server stderr log.

## Slash Commands

Built-in commands:

| Command | Purpose |
| --- | --- |
| `/spawn <prompt>` | Start a read-only background agent. |
| `/spawn --write <prompt>` | Start a background agent that may request write/command approval. |
| `/agents` / `/agents <id>` | Open Agent View or a selected agent transcript. |
| `/jobs` / `/jobs <id>` | List jobs or open a job transcript. |
| `/cancel <id\|all>` | Cancel one job or all jobs. |
| `/provider [slug]` / `/providers` | Open the provider picker or switch provider. |
| `/provider add [slug]` | Open the provider picker or key prompt. |
| `/model [id]` / `/models` | Open the model picker or switch model. |
| `/sessions` | List recent sessions. |
| `/sessions resume <id>` | Resume by full ID or unique prefix. |
| `/sessions rename <name>` | Rename the current session. |
| `/sessions delete <id> --yes` | Delete a saved session. |
| `/queue` | List queued foreground prompts. |
| `/queue clear` | Clear queued foreground prompts. |
| `/undo` | Restore the most recent file snapshot. |
| `/compact [--keep N]` | Summarize older conversation messages. |
| `/cost` / `/cost reset --yes` | Show or reset cost totals. |
| `/trust [on\|off]` | Show or set trust mode. |
| `/permissions` / `/permissions profile <mode>` / `/permissions rule <tool> <action>` | Show or change the session permission policy. |
| `/help` | Show keybindings and commands. |
| `/clear` | Clear the transcript pane only. |
| `/transcript` | Open the current saved session transcript. |
| `/statusline [refresh]` | Show or refresh a custom statusline. |
| `/mcp` / `/mcp status <name>` / `/mcp tools <name>` / `/mcp logs <name>` | Inspect configured MCP servers. |
| `/exit` / `/quit` | Quit packetcode. |

Typing `/` opens command autocomplete. Unknown slash commands show an error; type `//something` to send `/something` as a normal prompt.

You can add markdown-backed prompt commands:

- User commands: `~/.packetcode/commands/<name>.md`
- Project commands: `.packetcode/commands/<name>.md`

Project commands override user commands with the same name. Built-ins cannot be shadowed.

```markdown
---
description: Review the selected code
---
Review this code and call out correctness risks:

$ARGUMENTS
```

`/review internal/app` sends the markdown body as the prompt with `$ARGUMENTS` replaced by `internal/app`.

## Configuration

packetcode reads `~/.packetcode/config.toml` and writes it with user-only permissions. A minimal config looks like:

```toml
[default]
provider = "openai"
model = "gpt-5.5"

[providers.openai]
api_key = "sk-..."
default_model = "gpt-5.5"

[providers.anthropic]
api_key = "sk-ant-..."
default_model = "claude-opus-4-7"

[providers.minimax]
api_key = "sk-..."
default_model = "MiniMax-M2.7-highspeed"

[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"

[behavior]
trust_mode = false
auto_compact_threshold = 80
max_input_rows = 10
background_max_concurrent = 4
background_max_depth = 2
background_max_total = 32

[permissions]
profile = "ask"
default = "ask"

[permissions.tools]
# Exact tool names, server__* prefixes, mcp:*, or *.
execute_command = "ask"
```

See [Configuration](docs/configuration.md) for the full schema.

## Development

Requires Go 1.24.2+.

```bash
make verify
make test
make build
make smoke
make vulncheck
make goreleaser-check
golangci-lint run ./...
```

The repository also contains feature-design notes under `docs/feature-*.md`. User-facing behavior should be checked against the guides linked above and the current code.

## Roadmap Notes

packetcode is pre-1.0. See [BACKLOG.md](BACKLOG.md) for the current backlog and [PACKETCOMPUTERS.md](PACKETCOMPUTERS.md) for the Packet Computers and Packet Control research plan.

## License

MIT - see [LICENSE](LICENSE).
