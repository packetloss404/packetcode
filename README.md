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
packetcode --trust
packetcode --version
```

`--provider` and `--model` override the saved default for the current run. The provider must already be configured, except for Ollama.

## Docs

- [Getting started](docs/getting-started.md)
- [Providers and models](docs/providers.md)
- [Configuration](docs/configuration.md)
- [Hooks and statusline](docs/hooks-and-statusline.md)
- [MCP servers](docs/mcp.md)
- [Troubleshooting](docs/troubleshooting.md)

## Core Workflow

Type a prompt and press `Enter`. Use `Shift+Enter` for a newline. During a generation, `Ctrl+C` cancels the current provider request, running tool, or approval prompt; pressing `Ctrl+C` again while idle exits.

Destructive actions show an approval prompt:

- `Y` approves.
- `N` or `Esc` rejects.
- `--trust` or `trust_mode = true` auto-approves for the session.

Finalized messages are printed into your terminal scrollback. Use your terminal scroll, `Shift+PageUp`, or tmux copy mode to review older output. `/clear` and `Ctrl+L` clear packetcode's live transcript pane; they do not delete the saved session.

## Providers

Configured providers can be switched without restarting:

- `Ctrl+P` or `/provider` opens the provider picker.
- `Ctrl+M` or `/model` opens the model picker for the active provider.
- `/provider <slug>` switches directly.
- `/model <id>` switches directly.

To add or update a provider key, open the provider picker with `Ctrl+P` or `/provider`, focus the provider row, then press `Ctrl+A`. `/provider add` opens the same picker, and `/provider add <slug>` opens the same key prompt for a provider.

API keys can also be set in the environment:

```text
PACKETCODE_OPENAI_API_KEY
PACKETCODE_ANTHROPIC_API_KEY
PACKETCODE_GEMINI_API_KEY
PACKETCODE_MINIMAX_API_KEY
PACKETCODE_OPENROUTER_API_KEY
```

Environment variables take precedence over `~/.packetcode/config.toml`.

## MCP Servers

MCP servers are configured under `[mcp.<name>]` and exposed as
provider-safe `<server>__<tool>` aliases. packetcode starts each
configured command as your user, so treat MCP servers as trusted local
code; approval prompts gate tool calls, not the child process itself.
MCP children inherit only a small launch environment allowlist by
default, plus any per-server `env` values you configure. The
`/mcp logs <name>` command shows a bounded, redacted tail of the
server stderr log.

## Slash Commands

Built-in commands:

| Command | Purpose |
| --- | --- |
| `/spawn <prompt>` | Start a read-only background agent. |
| `/spawn --write <prompt>` | Start a background agent that may request write/command approval. |
| `/agents` / `/agents <id>` | Open Agent View or a selected agent transcript. |
| `/jobs` / `/jobs <id>` | List jobs or open a job transcript. |
| `/cancel <id\|all>` | Cancel one job or all jobs. |
| `/provider [slug]` | Open the provider picker or switch provider. |
| `/provider add [slug]` | Open the provider picker or key prompt. |
| `/model [id]` | Open the model picker or switch model. |
| `/sessions` | List recent sessions. |
| `/sessions resume <id>` | Resume by full ID or unique prefix. |
| `/sessions delete <id> --yes` | Delete a saved session. |
| `/undo` | Restore the most recent file snapshot. |
| `/compact [--keep N]` | Summarize older conversation messages. |
| `/cost` / `/cost reset --yes` | Show or reset cost totals. |
| `/trust [on\|off]` | Show or set trust mode. |
| `/help` | Show keybindings and commands. |
| `/clear` | Clear the transcript pane only. |
| `/statusline [refresh]` | Show or refresh a custom statusline. |
| `/mcp` / `/mcp logs <name>` | Inspect configured MCP servers. |
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

## License

MIT - see [LICENSE](LICENSE).
