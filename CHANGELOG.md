# Changelog

All notable changes to packetcode are recorded here.

packetcode has not cut a stable 1.0 release yet. Entries under `Unreleased` describe the current main branch.

## [Unreleased]

### Added

- Multi-provider chat through one provider interface: OpenAI, Google Gemini, MiniMax, OpenRouter, and local Ollama.
- Agent tool loop with `read_file`, `search_codebase`, `list_directory`, `write_file`, `patch_file`, and `execute_command`; mutating tools require approval unless trust mode is enabled.
- Sessions, cost tracking, `/undo` file backups, context compaction, and git-aware status information.
- Keyboard-first Bubble Tea TUI with inline terminal scrollback, approval prompts, provider/model pickers, slash-command autocomplete, and markdown-backed custom prompt commands.
- Background agents via `/spawn`, `/jobs`, `/cancel`, and the `spawn_agent` tool. Background jobs are read-only by default and request normal approvals when launched with `--write`.
- MCP stdio server support through `[mcp.<name>]` config blocks. MCP tools are registered as `<server>__<tool>` aliases and always go through approval.
- Optional custom statusline command under `[statusline]`.
- Optional lifecycle hooks under `[[hooks.user_prompt_submit]]`, `[[hooks.pre_tool_use]]`, and `[[hooks.post_tool_use]]`.
- User theme overrides through `~/.packetcode/theme.toml`, with presets under `docs/themes/`.

### Changed

- Documentation now treats the project as pre-release / active development instead of calling the current feature set `v1`.
- User docs now describe the real provider setup path: use `Ctrl+P`, `/provider`, or `/provider add`; focus a provider row and press `Ctrl+A`, or run `/provider add <slug>` to open the key prompt directly.
- Transcript docs now match the inline-rendering model: finalized output is committed to terminal scrollback, `/clear` only clears packetcode's live pane, and historical tool blocks are not toggled after they are printed.

### Testing

- The Go test suite covers provider adapters, config loading, sessions, tools, the agent loop, cancellation, slash commands, pickers, jobs, MCP, hooks, statusline, and UI components.
