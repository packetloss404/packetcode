# Backlog

packetcode is pre-1.0. This backlog tracks high-value work that is not yet shipped, with an emphasis on getting the terminal agent to a stable v1 and then growing PacketADE-style workflows.

## v1 Stabilization

- Finish a consistent docs pass across README, `docs/`, changelog, and examples.
- Keep provider/model catalogs current for OpenAI, Anthropic, Gemini, MiniMax, OpenRouter, OpenCode-compatible endpoints, and Ollama.
- Add release checklist automation for Windows, Linux, and macOS artifacts.
- Keep full-suite tests reliable on Windows, including process-cancellation and shell-startup timing tests.
- Add more end-to-end smoke tests for slash commands, provider/model switching, background agents, MCP tools, and session resume.
- Clarify compatibility policy for config fields, session files, job files, and MCP server config.

## Agent and TUI Quality

- Continue tightening Agent View around grouped jobs, result injection, approval waits, and per-agent telemetry.
- Add richer transcript search/filtering for current sessions and background agents.
- Improve queued foreground prompt controls: list, reorder, remove, and edit queued prompts.
- Add better cancellation visibility for active provider requests, shell commands, and MCP tool calls.
- Audit all user-facing copy for terse, terminal-friendly wording.

## MCP and Tooling

- Add restart support for individual MCP servers.
- Add MCP server health checks and optional reconnect.
- Surface tool-call timeouts and server death reasons consistently in Agent View and transcripts.
- Expand runtime-aware shell guidance for PowerShell, CMD, WSL, Git Bash, POSIX sh, and bash.
- Add per-tool policy hooks for future Packet Computers and Packet Control work.

## Packet Computers

See [PACKETCOMPUTERS.md](PACKETCOMPUTERS.md) for the research and architecture plan.

Near-term backlog:

- Add `internal/computers` registry types and load/save tests.
- Add `/computers` list/status command backed by local config.
- Design `packetcode daemon` loopback RPC.
- Add local computer heartbeat/status.
- Introduce a runtime/backend abstraction so tools can run locally or on a registered computer.
- Extend jobs with `ComputerID`.
- Add `/spawn --computer <name>`.
- Group Agent View rows by computer.

Deferred:

- SSH-forwarded daemon transport.
- Persistent job reconnect/reconcile.
- Per-job worktrees.
- Named terminals and dev-server process supervision.
- Snapshots/checkpoints.
- Managed cloud computers.

## Packet Control

See [PACKETCOMPUTERS.md](PACKETCOMPUTERS.md) for the research and architecture plan.

Near-term backlog:

- Add `internal/control` manifest, artifact, verdict, and report types.
- Define `~/.packetcode/control/<run-id>/` artifact layout.
- Add `/control runs` and `/control open <id>`.
- Add terminal-first `/verify <claim>` with command output artifacts.
- Add terminal-first `/qa <command> -- <expected behavior>`.
- Integrate control runs into jobs/Agent View or a sibling Control View.

Deferred:

- Playwright or agent-browser-backed browser QA.
- Trace and screenshot previews.
- Demo composition.
- Desktop/computer-use automation.
- Target-level browser/desktop permission policies.

## Security and Trust

- Define a shared policy vocabulary for filesystem, shell, network, browser, desktop, secrets, and approvals.
- Make remote/machine trust boundaries explicit before Packet Computers ships.
- Treat browser and desktop content as untrusted evidence, not instructions.
- Add redaction tests for logs, MCP output, statusline/hooks, and future control artifacts.
- Document safe defaults for `--trust`, MCP servers, Packet Computers, and Packet Control.

