# Security And Permissions

packetcode runs local tools as your user. The permission policy controls tool calls that can mutate files, run commands, start background agents, or call MCP tools.

## Profiles

Set a profile for one run:

```bash
packetcode --permission-mode accept-edits
```

Persist a profile in `~/.packetcode/config.toml`:

```toml
[permissions]
profile = "ask"
default = "ask"
```

Profiles:

- `ask`: prompt before every approval-gated tool.
- `accept_edits`: auto-approve `write_file` and `patch_file`; ask for `execute_command`, `spawn_agent`, and MCP tools.
- `read_only`: deny approval-gated tools.
- `bypass`: auto-approve approval-gated tools.

`--trust` and `trust_mode = true` auto-approve actions that the policy would otherwise ask for. Explicit `deny` rules still deny.

## Rules

Tool rules override the profile:

```toml
[permissions.tools]
write_file = "allow"
patch_file = "allow"
execute_command = "ask"
spawn_agent = "ask"
"mcp:*" = "ask"

[[permissions.rules]]
tool = "execute_command"
action = "deny"
command_prefix = ["rm", "-rf"]
reason = "refuse broad recursive deletes"
```

Valid actions are `ask`, `allow`, and `deny`. `[permissions.tools]` keys can be exact tool names, prefix patterns like `filesystem__*`, `"mcp:*"` for all MCP tools, or `"*"`. `[[permissions.rules]]` entries can also match an exact shell `command` or a tokenized `command_prefix`.

Use `/permissions` inside the TUI to inspect the active session policy. Use `/permissions profile <mode>` or `/permissions rule <tool-or-pattern> <action>` to change the current session without editing config.

## Background Write Jobs

Read-only background agents can inspect the project without a worktree. Write-capable jobs (`/spawn --write` or `spawn_agent` with `allow_write=true`) require git worktree isolation and fail closed if packetcode cannot create one. Worktrees live under `~/.packetcode/worktrees/<repo-key>/<job-id>` on branch `packetcode-job-<job-id>` and are based on the current `HEAD`, not uncommitted foreground edits.

Packetcode does not automatically merge or delete completed worktrees. Inspect them with `git -C <path> status` and `git -C <path> diff` before deciding what to keep. Remove a completed worktree with `git worktree remove <path>` from the source repository, then delete the `packetcode-job-<job-id>` branch when it is no longer needed.

## MCP

MCP server processes start as local child processes when packetcode launches. Approval prompts gate MCP tool calls, not server startup. Configure MCP servers only from sources you trust, keep secrets in per-server `env` entries, and use `/mcp logs <name>` for a bounded redacted log tail.

## Checks

Run `packetcode doctor --check permissions` to validate the configured profile and rules without starting the TUI.
