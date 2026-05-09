# Hooks And Statusline

Hooks and the custom statusline run shell commands that you configure. On Windows they run through PowerShell; elsewhere they run through `sh -c`. Each command receives JSON on stdin and runs with the project root as its working directory.

## Statusline

```toml
[statusline]
command = "jq -r '\"\\(.provider.display_name) / \\(.model.id) / \\(.context_window.used_percentage)% / $\\(.cost.total_cost_usd)\"'"
enabled = true
timeout_sec = 2
```

`enabled` is optional and defaults to true when `command` is set. If the command fails, times out, or prints nothing, packetcode falls back to the built-in status bar.

Statusline stdin:

```json
{
  "session_id": "...",
  "working_dir": "/path/to/project",
  "project": "packetcode",
  "git_branch": "main",
  "provider": { "slug": "openai", "display_name": "OpenAI" },
  "model": { "id": "gpt-5.5" },
  "context_window": { "used": 12000, "max": 400000, "used_percentage": 3 },
  "cost": { "total_cost_usd": 0.42 },
  "jobs": { "active": 1 },
  "duration_seconds": 360,
  "version": "v0.0.0"
}
```

Use `/statusline` to show the active output and `/statusline refresh` to force a rerender.

## Hooks

```toml
[[hooks.user_prompt_submit]]
command = "cat .packetcode-context 2>/dev/null || true"
timeout_sec = 2

[[hooks.pre_tool_use]]
matcher = "execute_command"
command = "python scripts/guard-command.py"
timeout_sec = 5

[[hooks.post_tool_use]]
matcher = "patch_file"
command = "gofmt -w $(git diff --name-only -- '*.go') 2>/dev/null || true"
timeout_sec = 10
```

Hook fields:

| Field | Meaning |
| --- | --- |
| `command` | Shell command to run. Required. |
| `matcher` | Tool name for tool hooks. Empty or `*` matches all tools. |
| `enabled` | Optional; defaults to true. |
| `timeout_sec` | Optional; defaults to 10 seconds. |

Hook behavior:

- `user_prompt_submit` runs before the prompt is sent. Successful stdout is injected as extra context.
- `pre_tool_use` runs before approval/tool execution. A non-zero exit blocks the tool call.
- `post_tool_use` runs after a tool returns. Successful stdout is appended to the tool result. Failures are reported in the appended hook output.

Prompt hook stdin:

```json
{
  "event": "UserPromptSubmit",
  "session_id": "...",
  "working_dir": "/path/to/project",
  "prompt": "user text"
}
```

Tool hook stdin:

```json
{
  "event": "PreToolUse",
  "session_id": "...",
  "working_dir": "/path/to/project",
  "tool_name": "execute_command",
  "tool_call_id": "...",
  "arguments": { "command": "go test ./..." }
}
```

`PostToolUse` payloads also include `result`:

```json
{
  "content": "tool output",
  "is_error": false,
  "metadata": {}
}
```

Stdout and stderr are capped at 64 KB per hook or statusline command.
