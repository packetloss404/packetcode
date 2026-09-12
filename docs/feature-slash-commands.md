# Slash Commands

Slash commands are parsed and handled locally before a prompt reaches the model. Type `/` for completion or `/help` for the runtime-generated key/command reference.

## Providers and Models

| Command | Action |
| --- | --- |
| `/provider` / `/providers` | Open provider picker. |
| `/provider <slug>` | Switch provider. |
| `/provider add [slug]` | Open provider picker/key entry. |
| `/model` / `/models` | Open model picker. |
| `/model <id>` | Switch model. |
| `/effort [level]` | Show or set a model's reasoning effort; `default` resets it. |

## Sessions and Foreground Work

| Command | Action |
| --- | --- |
| `/sessions` | List sessions. |
| `/sessions resume <id>` | Resume by full ID or unique prefix. |
| `/sessions rename <name>` | Rename current session. |
| `/sessions delete <id> --yes` | Delete a session. |
| `/queue` | List queued prompts. |
| `/queue drop <n>` | Drop one queued prompt. |
| `/queue resume` | Continue a paused queue while idle. |
| `/queue clear` | Clear queued prompts. |
| `/compact [--keep N]` | Summarize older context. |
| `/undo` | Restore the latest file backup. |
| `/cost` / `/cost reset --yes` | Show/reset API cost totals. |
| `/transcript` | Open the saved transcript. |
| `/clear` | Clear visible output only. |

Failed turns and compaction pause pending prompts. New prompts join the paused
queue; `/queue clear` or dropping the last entry clears the pause. Clearing
the display or switching sessions does not resume queued work.

## Agents and Orchestration

| Command | Action |
| --- | --- |
| `/spawn [--computer C] [--provider P] [--model M] [--write] <prompt>` | Start a local or Packet Computer background job. |
| `/agents [id]` | Open Agent View or a transcript. |
| `/jobs [id]` | List jobs or open a transcript. |
| `/jobs resubmit [id]` | List eligible recovered jobs or start one new run using its full ID. The original is not resumed. |
| `/cancel <id\|all>` | Cancel jobs. |
| `/computers` | List registered Packet Computers. |
| `/computers status <name>` | Show one computer's stored record. |
| `/computers register <name> <root>` | Register a local computer record. |
| `/computers ssh <name> <user@host> <root> --fingerprint <SHA256:...>` | Register a pinned SSH computer for `packetcode --computer <name>`. |
| `/computers remove <name> --yes` | Remove a computer record. |
| `/workflows` | Open Workflow View. |
| `/workflows run [--computer C] <name> [key=value...]` | Start a local or Packet Computer workflow. |
| `/workflows validate <name>` | Validate schema and templates without starting agents. |
| `/workflows list` | List definitions/runs. |
| `/workflows stop [id\|all]` | Cancel workflows. |
| `/loop [interval] <prompt\|/command>` | Start a repeating loop. |
| `/loop list` / `/loop stop [id\|all]` | Manage loops. |

## Permissions, Local Models, and Extensions

| Command | Action |
| --- | --- |
| `/plan [on\|off]` | Toggle read-only planning. |
| `/trust [on\|off]` | Show/set Bypass Permissions. |
| `/permissions` | Show current policy. |
| `/permissions profile <name>` | Change session profile. |
| `/permissions rule <tool> <ask\|allow\|deny>` | Add session rule. |
| `/permissions reset` | Revoke session rules and restore startup policy. |
| `/ollama [status\|models\|ps\|pull <model>]` | Inspect/manage Ollama. |
| `/mcp` | List MCP servers. |
| `/mcp status <name>` | Show health/config. |
| `/mcp tools <name>` | Show callable aliases. |
| `/mcp logs <name>` | Show redacted stderr tail. |
| `/mcp restart <name>` | Reconnect one stdio server. |
| `/statusline [refresh]` | Inspect/refresh statusline. |
| `/help` | Show runtime help. |
| `/exit` / `/quit` | Exit. |

## Custom Prompt Commands

Markdown commands load from `~/.packetcode/commands/*.md` and `.packetcode/commands/*.md`; project commands win. Optional frontmatter supplies `description`, and `$ARGUMENTS` expands to the text after the command.

Built-ins cannot be shadowed. A custom command expands into a normal prompt and
queues if a turn is active or the foreground queue is paused.
