# Configuration

packetcode reads `~/.packetcode/config.toml`. The file is written atomically with user-only permissions.

## Full Example

```toml
[default]
provider = "openai"
model = "gpt-5.5"

[providers.openai]
api_key = "sk-..."
default_model = "gpt-5.5"

[providers.gemini]
api_key = "AI..."
default_model = "gemini-2.5-pro"

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
background_default_provider = ""
background_default_model = ""

[statusline]
command = ""
timeout_sec = 2

[mcp.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
enabled = true
timeout_sec = 10
```

## Sections

`[default]` selects the provider/model used at startup.

`[providers.<slug>]` stores API keys, saved default models, and the Ollama host.

`[behavior]` controls trust mode, input height, auto-compaction threshold, and background-agent limits.

`[statusline]` configures an optional shell command that replaces the built-in bottom bar. See [Hooks and statusline](hooks-and-statusline.md).

`[mcp.<name>]` declares stdio MCP servers. See [MCP servers](mcp.md).

## Custom Prompt Commands

Markdown prompt commands live in:

- `~/.packetcode/commands/<name>.md`
- `.packetcode/commands/<name>.md`

Project commands override user commands with the same name. Built-in slash commands cannot be shadowed.

```markdown
---
description: Review the selected code
---
Review this code and call out correctness risks:

$ARGUMENTS
```

## Themes

packetcode reads `~/.packetcode/theme.toml` when present. Presets live in `docs/themes/`.

```bash
cp docs/themes/high-contrast.toml ~/.packetcode/theme.toml
```

A missing theme file is ignored. A malformed theme logs one warning and packetcode keeps the built-in theme.
