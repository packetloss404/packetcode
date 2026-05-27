# MCP (Model Context Protocol) — Round 7 Design Spec

## Summary

Round 7 lets users extend packetcode's tool surface with external MCP servers. Each entry under `[mcp.<name>]` in `~/.packetcode/config.toml` is spawned at startup as a child process; packetcode handshakes over newline-delimited JSON-RPC 2.0 stdio (protocol version `2025-06-18`), lists the server's tools, and registers each as a `tools.Tool` adapter in the main registry. At call time the LLM invokes the adapter exactly as it would any built-in tool — approval flows through the existing `uiApprover`, the adapter forwards `tools/call`, and the server's `content` array is flattened into a `ToolResult`. Strictly additive: native tools work unchanged, and a misbehaving MCP server never blocks startup.

## User stories

1. **Filesystem server.** `[mcp.filesystem]` spawning `npx -y @modelcontextprotocol/server-filesystem /home/alice/projects`. LLM gets `filesystem__read_file` etc.
2. **Git server.** `[mcp.git]` spawning `uvx mcp-server-git --repository .` exposes `git__log`, `git__diff`, etc.
3. **Server crash mid-session.** Adapter returns `"MCP server 'search' has exited — restart packetcode to reconnect"`. Native + other MCP tools keep working.
4. **Missing binary.** Startup prints `"packetcode: mcp server 'fetch': command not found (uvx); skipping"`; everything else starts.
5. **Listing.** `/mcp` shows a table of server name / state / tool count / pid / command.
6. **Debugging.** `/mcp logs git` tails last 50 lines from `~/.packetcode/mcp-git.log`.

## MCP primer

**Protocol version:** `2025-06-18`. **Transport:** stdio newline-delimited JSON-RPC 2.0. Each message is one UTF-8 JSON line terminated by `\n`, no Content-Length framing. Stderr is out-of-band — teed to a per-server log file.

**RPC methods we implement (client-side):**

- `initialize` — request: `{protocolVersion, capabilities:{}, clientInfo:{name:"packetcode", version}}`. Response: `{protocolVersion, capabilities, serverInfo}`. If `capabilities.tools` is missing → skip server with `"server does not expose tools"`.
- `notifications/initialized` — fire-and-forget immediately after `initialize`.
- `tools/list` — no params. Response: `{tools: [{name, description, inputSchema}]}`. `inputSchema` forwarded verbatim.
- `tools/call` — `{name, arguments}`. Response: `{content: [{type, text?}], isError}`. Only `type=="text"` decoded; other types flattened to `"[<type> content omitted]"`.

**Methods we refuse** (server-initiated requests): `sampling/createMessage`, `roots/list`, `logging/setLevel`, `elicitation/create`, `prompts/*`, `resources/*`, `notifications/tools/list_changed`. Reply `{error: {code: -32601, message: "method not supported"}}` for any with an id; silently ignore notifications.

## Architecture

### Package layout

```
internal/mcp/
├── doc.go
├── client.go           # Client: per-server stdio JSON-RPC driver
├── client_test.go
├── config.go           # ServerConfig, Manager Config, protocol version
├── framing.go          # newline-delimited JSON-RPC reader/writer
├── framing_test.go
├── jsonrpc.go          # Request / Response / Notification / ErrorObj
├── manager.go          # Manager: owns N Clients, lifecycle, reports
├── manager_test.go
├── process.go          # spawn + stderr-tee wrappers
├── testing_test.go     # StubServer helper for tests
├── tool.go             # McpTool → tools.Tool adapter
└── tool_test.go
```

### Key types

```go
const ProtocolVersion = "2025-06-18"

type ServerConfig struct {
    Name       string             // map key from TOML
    Command    string             `toml:"command"`
    Args       []string           `toml:"args"`
    Env        map[string]string  `toml:"env"`
    Enabled    bool               `toml:"enabled"`     // default true
    TimeoutSec int                `toml:"timeout_sec"` // default 10
}

type Config struct {
    Servers    []ServerConfig
    LogDir     string
    ClientInfo ClientInfo
}

type ClientInfo struct{ Name, Version string }

type StartupReport struct {
    Name      string
    Status    string  // "running" | "disabled" | "failed"
    ToolCount int
    PID       int
    Err       string
}

type Client struct {
    name       string
    cmd        *exec.Cmd
    stdin      io.WriteCloser
    stdout     io.ReadCloser
    stderr     io.ReadCloser
    logFile    *os.File
    wmu        sync.Mutex     // serialises stdin writes
    nextID     atomic.Int64
    pending    sync.Map       // map[int64]chan rpcResponse
    dead       atomic.Bool
    deadErr    atomic.Value   // error
    serverInfo ServerInfo
    tools      []ServerTool
}

// func NewClient(ctx, cfg, logDir, info) (*Client, error)
// func (c *Client) CallTool(ctx, name, args) (ToolCallResult, error)
// func (c *Client) Close(timeout) error
// func (c *Client) IsAlive() bool

type Manager struct {
    cfg     Config
    mu      sync.RWMutex
    clients map[string]*Client
    reports []StartupReport
}

// func NewManager(cfg) *Manager
// func (m *Manager) Start(ctx) []StartupReport        // spawns in parallel, max 8 concurrent
// func (m *Manager) Clients() []*Client               // alive only
// func (m *Manager) Client(name) (*Client, bool)
// func (m *Manager) Reports() []StartupReport
// func (m *Manager) Shutdown(timeout) error

type McpTool struct {
    client     *Client
    serverName string
    toolName   string
    desc       string
    schema     json.RawMessage
}

// Name() returns "<server>__<tool>" (always prefixed)
// RequiresApproval() returns true (always)
// Execute delegates to client.CallTool, flattens content → ToolResult
```

### Goroutine model (per Client)

1. **Reader** — `bufio.Scanner` (8 MB buffer) over stdout. Each line: parse JSON-RPC. Response with id → dispatch to pending channel. Server-initiated request with id → reply `{error: method not supported}`. Notification (no id) → ignore. EOF → set dead, close pending channels with `ErrServerExited`, close log file.
2. **Writes are synchronous** under `wmu` — no writer goroutine; simpler than the request/response pattern needs.
3. **Stderr-tee** — `io.Copy(logFile, stderr)` in its own goroutine.
4. **Reaper** — `go cmd.Wait()` records exit status; flips `dead` and `deadErr`.

`CallTool(ctx, method, params)` algorithm:
```
id = nextID.Add(1)
ch = make(chan rpcResponse, 1)
pending.Store(id, ch); defer pending.Delete(id)
writeLine({"jsonrpc":"2.0","id":id,"method":method,"params":params})   // under wmu
select {
case resp := <-ch:    return resp.Result, resp.Err
case <-ctx.Done():    return nil, ctx.Err()
}
```

### Adapter behaviour

```go
func (t *McpTool) Execute(ctx, params) (ToolResult, error) {
    if !t.client.IsAlive() {
        return ToolResult{IsError: true,
            Content: fmt.Sprintf("MCP server %q has exited — restart packetcode to reconnect", t.serverName)}, nil
    }
    args := params
    if len(bytes.TrimSpace(args)) == 0 || string(args) == "null" {
        args = json.RawMessage("{}")
    }
    res, err := t.client.CallTool(ctx, t.toolName, args)
    if err != nil {
        if errors.Is(err, ErrServerExited) { /* friendly error */ }
        if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
            return ToolResult{}, err  // agent loop handles
        }
        return ToolResult{IsError: true,
            Content: fmt.Sprintf("%s.%s: %s", t.serverName, t.toolName, err)}, nil
    }
    // Flatten content — text items joined with '\n', others → "[<type> content omitted]"
    ...
}
```

## Configuration

### `[mcp.<name>]` schema

```toml
[mcp.filesystem]
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-filesystem", "/home/alice/projects"]
env     = { HOME = "/home/alice" }
# enabled defaults to true; omit to keep enabled

[mcp.git]
command = "uvx"
args    = ["mcp-server-git", "--repository", "."]
timeout_sec = 20

[mcp.disabled-example]
command = "echo"
enabled = false
```

Added to `config.Config`:

```go
MCP map[string]MCPServerConfig `toml:"mcp"`

type MCPServerConfig struct {
    Command    string            `toml:"command"`
    Args       []string          `toml:"args,omitempty"`
    Env        map[string]string `toml:"env,omitempty"`
    Enabled    *bool             `toml:"enabled,omitempty"`     // pointer → nil means true
    TimeoutSec int               `toml:"timeout_sec,omitempty"` // default 10
}

func (c MCPServerConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }
```

`LoadFrom` initialises the map to `{}` after unmarshal. `Default()` in `defaults.go` sets an empty map.

### Name disambiguation

- MCP tools are ALWAYS prefixed with provider-safe aliases: `<serverName>__<toolName>`.
- Native tools (`read_file`, `write_file`, `patch_file`, `search_codebase`, `list_directory`, `list_symbols`, `find_definition`, `find_references`, `get_diagnostics`, `execute_command`, `spawn_agent`) are never prefixed and never collide.
- If two MCP servers both expose `read_file`, names are `fs__read_file` vs `git__read_file` — still unique.

### Log files

- `~/.packetcode/mcp-<name>.log`, `O_CREATE|O_WRONLY|O_APPEND`, `0600`.
- No rotation; document manual cleanup.
- `/mcp logs` reads only a bounded tail and redacts common secret
  patterns before rendering.
- `config.MCPLogPath(name) (string, error)` helper.

## Lifecycle

### Startup (in `cmd/packetcode/main.go`, after theme load, before `jobs.NewManager`)

```go
mcpMgr := mcp.NewManager(mcp.Config{
    Servers:    mcpServerConfigsFrom(cfg),
    LogDir:     mcpLogDir,
    ClientInfo: mcp.ClientInfo{Name: "packetcode", Version: welcomeVersion()},
})
startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
reports := mcpMgr.Start(startupCtx)
cancel()
for _, r := range reports {
    // log to stderr per-report
}
defer mcpMgr.Shutdown(2 * time.Second)

// After toolReg is fully populated with native + spawn_agent:
for _, c := range mcpMgr.Clients() {
    for _, st := range c.Tools() {
        toolReg.Register(mcp.NewMcpTool(c, st))
    }
}
```

### Per-server startup algorithm

1. If `!cfg.IsEnabled()` → status `"disabled"`, return.
2. `exec.CommandContext(startupCtx, cfg.Command, cfg.Args...)`. Inherit only the MCP launch allowlist from the process environment, then overlay `cfg.Env` (cfg wins).
3. Hook stdin/stdout pipes. Open `mcp-<name>.log` for append.
4. `cmd.Start()`. Err → status `"failed"`, return.
5. Launch stderr-tee, reader, reaper goroutines.
6. `initialize` with `context.WithTimeout(ctx, TimeoutSec*time.Second)` (default 10s). Timeout/err → kill + fail.
7. Check `capabilities.tools` present. Missing → kill + fail with `"server does not expose tools"`.
8. Send `notifications/initialized` (fire-and-forget).
9. `tools/list`. Err → kill + fail.
10. Cache tools on Client. Status `"running"`.

Parallel spawn bounded at 8 concurrent.

### Shutdown

`Client.Close(timeout)`:
1. If already dead → return nil.
2. Close stdin (canonical MCP shutdown signal — no `shutdown` RPC in protocol).
3. Wait up to `timeout` (default 2s) for reaper.
4. Timeout → `cmd.Process.Kill()`. Wait another 500ms.
5. Close log file. Return exit-code error if non-zero.

`Manager.Shutdown(timeout)`: parallel `Close` with deadline `timeout + 1s`. Composite err.

### Crash handling

Reader EOF:
1. `dead.Store(true)`; record `deadErr`.
2. Walk `pending` sync.Map; send `ErrServerExited` to every waiting channel.
3. Tools remain registered — future `Execute` short-circuits with friendly error. No auto-restart.

## Approval

Zero modifications. MCP tools return `true` from `RequiresApproval()`; the existing `uiApprover` handles them. Modal header calls `tool.Name()` which returns `"<server>__<tool>"` — natural display. Trust mode auto-approves MCP calls like any destructive tool.

No per-server trust this round.

Configured MCP servers are trusted local code at process-spawn time:
packetcode starts the configured command as the current user. Approval
gates individual MCP tool calls through the agent loop, but it is not a
process sandbox for the server binary itself.

## Slash commands

### `/mcp` (list)

Monospace table, system message:

```
MCP servers
NAME         STATE      TOOLS  PID     COMMAND
filesystem   running    8      41283   npx -y @modelcontextprotocol/server-filesystem /home/...
git          running    5      41291   uvx mcp-server-git --repository .
fetch        failed     0      -       command not found (uvx)
legacy       disabled   0      -       echo
```

No servers configured → `"no MCP servers configured (add [mcp.<name>] to ~/.packetcode/config.toml)"`.

### `/mcp logs <name>`

Tails last 50 lines of a bounded, redacted tail from `mcp-<name>.log`:

```
── mcp-<name>.log (last 50 lines) ──
<content>
── end ──
```

Errors: `"mcp logs: no server named <name>"`, `"mcp logs: no log file at <path>"`, `"mcp logs: read failed: <err>"`.

### `/mcp restart <name>` — **deferred to Round 8**.

### Integration into existing slash infrastructure

- Add `"mcp"` to `knownSlashCommands`.
- `parseMCPArgs(args) (sub, name string, err error)` — `[]`, `["logs","<name>"]`, error on malformed.
- New `internal/app/slashcmd_mcp.go` with `handleMCPCommand`, `renderMCPTable`, `tailMCPLog`.
- `SlashCommands` entries in `keymap.go`.
- App dispatches via `case "mcp":` in `handleSlashCommand`.
- Thread `MCP *mcp.Manager` through `app.Deps`; nil-safe guard in handler.

## File-by-file change list

### Bucket A — Backend

| Path | Change |
|---|---|
| `internal/mcp/doc.go` | **NEW.** Package docstring. |
| `internal/mcp/jsonrpc.go` | **NEW.** Request/Response/Notification/ErrorObj types + constants. |
| `internal/mcp/framing.go` | **NEW.** `readLine` / `writeLine` with 8 MB scanner buffer. |
| `internal/mcp/framing_test.go` | **NEW.** Round-trip, large-message, embedded-newline tests. |
| `internal/mcp/config.go` | **NEW.** `ProtocolVersion`, `ServerConfig`, `Config`, `ClientInfo`, `StartupReport`. |
| `internal/mcp/process.go` | **NEW.** `spawnServerProcess` spawning + stderr-tee launch. |
| `internal/mcp/client.go` | **NEW.** `Client` + `NewClient` + `CallTool` + `Close` + `IsAlive` + pending dispatch. `ErrServerExited` sentinel. |
| `internal/mcp/client_test.go` | **NEW.** Tests 1-12 (via `io.Pipe` + stub server). |
| `internal/mcp/manager.go` | **NEW.** `Manager` + `Start` (parallel) + `Shutdown` + `Clients` + `Reports`. |
| `internal/mcp/manager_test.go` | **NEW.** Tests 13-15. |
| `internal/mcp/tool.go` | **NEW.** `McpTool` + adapter methods. |
| `internal/mcp/tool_test.go` | **NEW.** Tests 16-21. |
| `internal/mcp/testing_test.go` | **NEW.** `StubServer` helper for same-package tests. |
| `internal/config/config.go` | Add `MCP map[string]MCPServerConfig`, `MCPServerConfig` type, `IsEnabled` method. Nil-guard in `LoadFrom`. |
| `internal/config/defaults.go` | Initialise `MCP: map[string]MCPServerConfig{}`. |
| `internal/config/paths.go` | Add `MCPLogPath(name)`. |
| `internal/config/config_test.go` | Test 22 (round-trip). Test 23 (nil-init). |
| `internal/config/paths_test.go` | Test 24 (MCPLogPath). |

### Bucket B — Integration

| Path | Change |
|---|---|
| `cmd/packetcode/main.go` | Spawn `mcp.Manager` before `jobs.NewManager`. Log startup reports. Register MCP tools into `toolReg` after native + spawn_agent. Pass `MCP` into `app.Deps`. `defer Shutdown(2s)`. |
| `cmd/packetcode/mcp_config.go` | **NEW.** `mcpServerConfigsFrom(*config.Config) []mcp.ServerConfig` — flatten map sorted by name. |
| `cmd/packetcode/mcp_config_test.go` | **NEW.** Test 26. |
| `internal/app/app.go` | Add `MCP *mcp.Manager` to `Deps`; store as `mcp *mcp.Manager`. No Update/View changes. |

### Bucket C — Slash + docs

| Path | Change |
|---|---|
| `internal/app/slashcmd.go` | Add `"mcp"` to `knownSlashCommands`. Add `parseMCPArgs`. |
| `internal/app/slashcmd_mcp.go` | **NEW.** `handleMCPCommand`, `renderMCPTable`, `tailMCPLog`. |
| `internal/app/slashcmd_mcp_test.go` | **NEW.** Tests 27-32. |
| `internal/app/app.go` | Dispatch `case "mcp":` in `handleSlashCommand`. |
| `internal/app/keymap.go` | Add two `SlashCommands` entries. |
| `docs/feature-mcp.md` | This spec. |
| `docs/mcp.md` | **NEW.** User-facing guide with worked examples (filesystem, git, fetch). |
| `README.md` | New "MCP servers" subsection; remove from Roadmap/Later. |
| `CHANGELOG.md` | Added bullet; Deferred entry removed. |
| `docs/roadmap-deferred.md` | Round 7 marked **Landed** — final round of the roadmap. |

## Tests

### Bucket A

1. `TestFraming_RoundTrip` — write 3, read 3.
2. `TestFraming_LargeMessage` — 2 MB payload.
3. `TestClient_InitializeHandshake` — stub responds; `NewClient` succeeds; `Tools()` populated.
4. `TestClient_InitializeTimeout` — stub silent; `NewClient(TimeoutSec:1)` errors with `"initialize timeout"`; process killed.
5. `TestClient_CallTool_Success` — text content round-trip.
6. `TestClient_CallTool_IsError` — isError=true propagated.
7. `TestClient_ConcurrentCalls` — 20 parallel `CallTool` ids routed correctly.
8. `TestClient_StdoutEOF_UnblocksPending` — 2 in-flight; stub closes stdout; both return `ErrServerExited` within 100ms.
9. `TestClient_ServerInitiatedRequest_RespondsMethodNotFound`.
10. `TestClient_ServerNotification_Ignored`.
11. `TestClient_Close_ClosesStdinAndWaits`.
12. `TestClient_Close_KillsHangingServer`.
13. `TestManager_Start_MixedStatuses` — running / disabled / failed in input order.
14. `TestManager_Start_ParallelSpawn` — elapsed < 8s for 4×5s timeouts.
15. `TestManager_Shutdown_AllClients`.
16. `TestMcpTool_AdaptsNameAsPrefixed`.
17. `TestMcpTool_Execute_DeadClient` — friendly error, no Go error.
18. `TestMcpTool_Execute_FlattensContent`.
19. `TestMcpTool_Execute_NullParams` — `null` → `{}`.
20. `TestMcpTool_Execute_CtxCancellation`.
21. `TestMcpTool_Schema_PassesThrough`.
22. `TestConfig_MCPBlockRoundTrip`.
23. `TestConfig_MCPMapInitialisedOnLoad`.
24. `TestMCPLogPath`.

### Bucket B

25. `TestMain_MCPServersWiredIntoToolRegistry` (in `cmd/packetcode` package scope).
26. `TestMCPConfigFlatten_SortedByName`.

### Bucket C

27. `TestParseSlashCommand_MCP`.
28. `TestRenderMCPTable_NoServers`.
29. `TestRenderMCPTable_MixedStatuses`.
30. `TestTailMCPLog_MissingFile`.
31. `TestTailMCPLog_TruncatesToLast50Lines`.
32. `TestSlashHelp_IncludesMCP`.

### End-to-end

33. `TestE2E_MCPToolCalledByAgent` — fake provider proposes `stub__hello`; trust mode on; assert tool ran + conversation updated.

## Out of scope (Round 8+)

- `/mcp restart <name>`, `/mcp status <name>`.
- Remote transports (HTTP+SSE, WebSocket, StreamableHTTP).
- MCP prompts, resources, sampling, elicitation, logging, roots.
- Per-server trust setting.
- Hot-reload on config change.
- Provider-server permission sandboxing.
- Resumable connections.
- Automatic respawn-on-crash.
- Custom per-MCP-tool renderers.
- Non-text content types (image/audio/resource).
