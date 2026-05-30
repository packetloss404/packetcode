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

[providers.localai]
type = "openai_compatible"
display_name = "LocalAI"
base_url = "http://localhost:8080/v1"
default_model = "coder-large"
api_key_required = false

[[providers.localai.models]]
id = "coder-large"
context_window = 32768
supports_tools = true

[behavior]
trust_mode = false
auto_compact_threshold = 80
max_input_rows = 10
background_max_concurrent = 4
background_max_depth = 2
background_max_total = 32
background_default_provider = ""
background_default_model = ""
provider_max_retries = 3
provider_stall_timeout = 60

[permissions]
profile = "balanced"

[permissions.profiles.balanced]
default = "ask"
read_file = "allow"
search_codebase = "allow"
list_directory = "allow"
list_symbols = "allow"
find_definition = "allow"
find_references = "allow"
get_diagnostics = "allow"
write_file = "ask"
patch_file = "ask"
execute_command = "ask"
spawn_agent = "ask"
mcp = "ask"

[[permissions.rules]]
tool = "execute_command"
action = "deny"
command_prefix = ["rm", "-rf"]
reason = "refuse broad recursive deletes"

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

`[providers.<slug>]` stores API keys, saved default models, the Ollama host, and custom OpenAI-compatible endpoint settings. `PACKETCODE_OLLAMA_HOST` overrides `[providers.ollama].host` at runtime. Custom providers use `type = "openai_compatible"`, `base_url`, optional `api_key_env`, optional `api_key_required = false` for keyless local endpoints, optional `headers`, and optional `[[providers.<slug>.models]]` fallback metadata.

`[behavior]` controls trust mode, input height, auto-compaction threshold, and background-agent limits.

Background-agent settings affect both `/spawn` and the `spawn_agent` tool:

- `background_max_concurrent` limits how many jobs can run at once; extra jobs stay queued.
- `background_max_depth` limits nested `spawn_agent` calls.
- `background_max_total` caps jobs created during one packetcode run.
- `background_default_provider` and `background_default_model` override the foreground provider/model for jobs when set; empty values inherit the active provider/model.

Provider resilience settings:

- `provider_max_retries` — how many times to retry a failed provider request (default 3).
- `provider_stall_timeout` — abort a provider stream that goes silent for this many seconds (default 60).

Write-capable background agents create git worktrees under `~/.packetcode/worktrees/<repo-key>/<job-id>` using branch `packetcode-job-<job-id>` and the current `HEAD` commit as the base. This state directory is internal; there is no config key for it yet. Read-only jobs do not create worktrees.

Background job snapshots under `~/.packetcode/jobs/` also persist compact artifact metadata. Artifact previews are bounded and intended for manifests, not raw log or diff storage.

`[permissions]` controls tool-call policy. `profile` can name a built-in profile (`balanced`/`ask`, `accept_edits`, `read_only`, or `bypass`) or a custom `[permissions.profiles.<name>]` table.

- `balanced` / `ask` allows read/search/list and prompts for writes, shell commands, background-agent spawns, and MCP tools.
- `accept_edits` auto-approves `write_file` and `patch_file`, but asks for `execute_command`, `spawn_agent`, and MCP tools.
- `read_only` allows read/search/list and denies everything else.
- `bypass` auto-approves tools unless an explicit deny rule matches.

Custom profile values are `ask`, `allow`, and `deny`. Use `default` as the fallback, exact tool names for native tools, and `mcp = "ask"` for all MCP aliases.

`[permissions.tools]` is still accepted as a backward-compatible inline rule table, but new config should prefer named profiles plus `[[permissions.rules]]`.

`[[permissions.rules]]` adds ordered policy rules. Later rules win when more than one matches. `command` matches an exact `execute_command` string, and `command_prefix` matches shell command fields from the beginning.

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
