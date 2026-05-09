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

Ollama is local and keyless. Start Ollama first, then choose `ollama` during setup.

## Starting Later

```bash
packetcode
packetcode --provider gemini --model gemini-2.5-pro
packetcode --resume <session-id>
packetcode --trust
```

`--provider` only works for providers already configured in `~/.packetcode/config.toml` or available without a key. Use the provider picker to add missing keys.

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
| `/sessions` | List saved sessions. |
| `/compact` | Summarize older conversation context. |
| `/clear` | Clear the live pane only; saved session data remains. |
| `/exit` | Quit. |

Unknown slash commands show an error. Use `//text` when you want to send a prompt that starts with `/`.
