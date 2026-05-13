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

[providers.anthropic]
api_key = "sk-ant-..."
default_model = "claude-opus-4-7"

[providers.gemini]
api_key = "AI..."
default_model = "gemini-2.5-pro"

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

`[providers.<slug>]` stores API keys, saved default models, and the Ollama host. `PACKETCODE_OLLAMA_HOST` overrides `[providers.ollama].host` at runtime.

`[behavior]` controls trust mode, input height, auto-compaction threshold, and background-agent limits.

Background-agent settings affect both `/spawn` and the `spawn_agent` tool:

- `background_max_concurrent` limits how many jobs can run at once; extra jobs stay queued.
- `background_max_depth` limits nested `spawn_agent` calls.
- `background_max_total` caps jobs created during one packetcode run.
- `background_default_provider` and `background_default_model` override the foreground provider/model for jobs when set; empty values inherit the active provider/model.

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
