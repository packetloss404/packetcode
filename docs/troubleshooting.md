# Troubleshooting

## `active provider "..." is not configured`

The provider has no usable key in config or environment. Run packetcode without `--provider`, or open `Ctrl+P` / `/provider`, focus the provider, and press `Ctrl+A` to save a key. `/provider add` opens the same picker, and `/provider add <slug>` opens the same key prompt directly.

## Add Or Update A Provider Key

Use the provider picker:

1. `Ctrl+P` or `/provider`
2. Focus the provider row
3. `Ctrl+A`
4. Paste and validate the key

## `/clear` Did Not Delete My Session

That is expected. `/clear` and `Ctrl+L` clear packetcode's live transcript pane only. Sessions still live under `~/.packetcode/sessions/` and can be resumed with `--resume` or `/sessions resume <id>`.

## I Cannot Scroll Inside packetcode

Finalized output is printed into your terminal scrollback. Use your terminal's scroll, `Shift+PageUp`, or tmux copy mode. The app does not keep a separate scrollable transcript viewport.

Tool output printed in history is not expandable/collapsible after it is committed. Current in-flight output appears in the live region.

## Unknown Slash Command

Unknown commands show an error. To send a normal prompt that starts with `/`, type two slashes:

```text
//explain this route
```

packetcode sends `/explain this route`.

## Model Switch Fails

Use `/model` or `Ctrl+M` to load the active provider's model list, then choose an exact model ID. If the provider's model API is temporarily unavailable, packetcode may still allow a direct `/model <id>` switch and let the next chat request surface the provider error.

## Ollama Is Unreachable

Start Ollama and confirm the host:

```bash
ollama serve
```

packetcode defaults to `http://localhost:11434`. If you use a different host, set it in config:

```toml
[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"
```

For a per-machine override without editing config:

```bash
PACKETCODE_OLLAMA_HOST=ollama.internal packetcode --provider ollama
```

## Hooks Or Statusline Fail

Hooks and statusline commands run through PowerShell on Windows and `sh -c` elsewhere. Keep commands short, deterministic, and project-local. Increase `timeout_sec` for slow commands.

Use `/statusline` to see whether the custom statusline is active and `/statusline refresh` to rerun it.

## MCP Server Does Not Start

Run `/mcp` to see server state. Then inspect logs:

```text
/mcp logs <name>
```

Logs are stored at `~/.packetcode/mcp-<name>.log`. After editing `[mcp.<name>]` config, restart packetcode.
