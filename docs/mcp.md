# MCP Servers

packetcode can extend its tool surface with external **MCP (Model
Context Protocol) servers**. Each server is an external binary that
speaks MCP over stdio JSON-RPC 2.0; packetcode spawns it as a child
process at startup, discovers its tools over the handshake, and
exposes each discovered tool to the LLM exactly like a built-in tool.
Tool calls are forwarded as `tools/call` RPCs; the server's reply is
surfaced to the conversation pane and counts against the existing
approval flow.

MCP support uses **stdio transport only**. HTTP+SSE, WebSocket, and
Streamable HTTP remotes are not supported yet. The required security design is
approved in the [Streamable HTTP MCP trust contract](mcp-http-trust-contract.md),
but no URL/transport configuration or network client has landed.

---

## Configuring a server

Every server lives under a `[mcp.<name>]` block in
`~/.packetcode/config.toml`. The `<name>` becomes the prefix on every
tool the server exposes — i.e. the LLM sees `<name>__<tool>`, so a
`read_file` tool on the filesystem server shows up as
`filesystem__read_file`.

```toml
[mcp.<name>]
command     = "binary-name-or-absolute-path"
args        = ["--flag", "value"]          # optional
env         = { KEY = "value" }            # optional
env_from    = ["SOME_SERVICE_TOKEN"]       # optional
enabled     = true                         # optional; defaults to true
timeout_sec = 10                           # optional; initialize budget
```

Fields:

- **command** — the executable packetcode spawns. If it's a bare name
  it must be on `$PATH`. Absolute paths are fine.
- **args** — command-line arguments passed in order.
- **env** — extra environment variables passed to this server. The
  server inherits only a small launch allowlist from packetcode's
  environment (path, home/cache/temp dirs, proxy, locale, and cert
  settings); values here win on conflict.
- **env_from** — names of variables to copy from packetcode's process
  environment into this server. Use this for secrets you do not want to
  store in `config.toml`. Only the named
  variables are copied; provider API keys are not inherited unless you
  explicitly name them here.
- **enabled** — set to `false` to keep the block on disk but skip
  spawning at startup. Omit the field to keep it enabled.
- **timeout_sec** — how long packetcode waits for the server to reply
  to `initialize` and `tools/list`. Bump this for slow-starting servers
  (npm-cold-cached, docker-pull-then-run, etc.). Defaults to 10.

A failure to spawn a server — binary missing, handshake timeout,
`tools/list` error — is logged to stderr and the `/mcp` table, but
**never prevents packetcode from starting**. Native tools and other
MCP servers keep working.

Treat every configured MCP server as trusted local code. packetcode
starts the configured command as your user, so approval prompts protect
tool calls in the agent loop but do not sandbox the child process
itself. Only add servers and command arguments you would be comfortable
running in your terminal.

---

## Example: filesystem server

[`@modelcontextprotocol/server-filesystem`](https://www.npmjs.com/package/@modelcontextprotocol/server-filesystem)
is the reference read/write filesystem server from the MCP
organisation. It exposes `read_file`, `write_file`, `list_directory`,
`search_files`, and a few others scoped to one or more directory
roots.

```toml
[mcp.filesystem]
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "/home/alice/projects"]
```

After packetcode starts, the LLM sees `filesystem__read_file`,
`filesystem__write_file`, etc. MCP tools participate in the normal permission
policy. Manual, Accept Edits, and Auto ask by default; Bypass or an explicit
allow rule can approve them automatically.

---

## Example: git server

[`mcp-server-git`](https://github.com/modelcontextprotocol/servers/tree/main/src/git)
wraps `git` with a tool surface: `git__log`, `git__diff`,
`git__show`, `git__blame`, etc.

```toml
[mcp.git]
command     = "uvx"
args        = ["mcp-server-git", "--repository", "."]
timeout_sec = 20
```

`uvx` is the one-shot launcher bundled with
[`uv`](https://github.com/astral-sh/uv); it downloads the package the
first time, which is why the timeout is bumped to 20.

---

## Example: fetch server

[`mcp-server-fetch`](https://github.com/modelcontextprotocol/servers/tree/main/src/fetch)
lets the LLM GET URLs and receive the body back as a string —
similar to an approval-gated `curl` run by the configured MCP server.

```toml
[mcp.fetch]
command = "uvx"
args    = ["mcp-server-fetch"]
```

No extra args, no extra env — `uvx` handles the pip install-cum-run.
By default, each `fetch__fetch` call asks for approval.

---

## Tool names and approval

- MCP tools are ALWAYS prefixed with provider-safe aliases:
  `<server>__<tool>`. Two servers that both expose `read_file` won't
  collide — you'll see e.g. `filesystem__read_file` and
  `git__read_file`.
- Native tools (`read_file`, `write_file`, `patch_file`,
  `search_codebase`, `list_directory`, `list_symbols`,
  `find_definition`, `find_references`, `get_diagnostics`, `execute_command`,
  `spawn_agent`) are never prefixed and never collide with MCP tools.
- Every MCP tool returns `true` from `RequiresApproval()`, no matter
  what the server is. The active profile and explicit rules decide whether
  that requirement asks, allows, or denies.

The approval menu shows the exact tool name (`filesystem__write_file`)
and proposed arguments before you allow once, allow that tool for this session,
or reject the request. A tool-wide session rule covers all arguments and paths.
Use `/permissions reset` to revoke remembered/session rules and restore the
startup policy.

---

## Debugging

**`/mcp`** — list configured servers, their state, tool count, pid,
and command. Example output:

```
MCP servers
NAME         STATE      TOOLS  PID     COMMAND
filesystem   running    8      41283  npx @modelcontextprotocol/server-filesystem .
git          running    5      41291  uvx mcp-server-git
fetch        failed     0      -       command not found (uvx)
legacy       disabled   0      -       legacy-mcp
```

**`/mcp logs <name>`** — tail the last 50 lines of the server's stderr
log. The log lives at `~/.packetcode/mcp-<name>.log` and is appended
across runs (no auto-rotation — delete it manually when it grows).
The display path reads only a bounded tail and redacts common secret
patterns before rendering. Use this when a server fails the handshake;
a lot of servers print diagnostics to stderr before exiting.

**`/mcp restart <name>`** — close and reconnect one configured server,
refreshing its registered tool adapters while leaving the rest of the fleet
running. Restart uses the configuration loaded at PacketCode startup; restart
PacketCode itself after changing `config.toml`.

`/mcp status <name>` and `/mcp tools <name>` show recovery instructions for
failed or exited servers. Disabled servers need `enabled = true` in their
configuration followed by an app restart. Reconnection does not rerun a failed
tool call; review any partial effects before requesting it again.

---

## MCP in ACP sessions (`packetcode acp`)

Under the Agent Client Protocol the *client* nominally owns a session's MCP
fleet: `session/new` and `session/load` carry an `mcpServers` array. PacketCode
reads that field three ways, so a desktop client does not have to re-declare
what is already in your `config.toml`:

| `mcpServers` in the request | Session runs with |
| --- | --- |
| omitted entirely | your `[mcp.<name>]` blocks — the same fleet the TUI starts |
| `[]` | nothing; the client explicitly asked for no MCP servers |
| `[{...}]` | exactly the client's list; your config is not merged in |

Startup failures follow the same split. A server the *client* named fails the
session — it asked for that server by name. A server from *your config* is
logged to stderr and skipped, exactly as in the TUI, so one bad `[mcp.<name>]`
block cannot make every desktop session uncreatable.

Clients can read the fleet back with the `_packetcode/mcp/list` extension:
with no parameters it returns the configured servers, with `{"sessionId": ...}`
it returns that live session's servers and their `running`/`failed`/`disabled`
status and tool counts. Agents that support the omission advertise
`agentCapabilities._packetcode.mcpDefaults: true` in their `initialize`
response; clients talking to an older agent must keep sending `[]`.

---

## Known limits

- **No hot-reload.** Add or change a `[mcp.<name>]` block and you need
  to restart packetcode for it to take effect.
- **No automatic reconnect.** If a server crashes mid-session, calls to its
  tools return a friendly `/mcp restart <name>` recovery instruction; native
  tools and other MCP servers keep working.
- **stdio transport only.** Streamable HTTP has a reviewed, fail-closed trust
  contract, but the transport and its configuration remain deferred.
- **No MCP prompts, resources, sampling, elicitation, logging, or
  roots.** packetcode implements tools-only. Server-initiated requests
  for those surfaces are refused with a JSON-RPC `-32601` (method not
  supported), so the server stays healthy; packetcode just ignores
  them.
- **No separate per-server UI.** Use permission rules such as
  `filesystem__*` or `mcp:*` to set server/tool policy.
- **Text content only.** Server responses carrying image/audio/
  resource content are flattened to `[<type> content omitted]` in
  the tool result. Tools can still be useful — they just can't
  surface non-text payloads to the LLM.

See [MCP implementation notes](feature-mcp.md) for the maintained stdio
protocol contract and the [Streamable HTTP MCP trust contract](mcp-http-trust-contract.md)
for the network design gate.
