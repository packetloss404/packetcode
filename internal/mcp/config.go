package mcp

import "errors"

// ProtocolVersion is the MCP wire-protocol version this client implements.
const ProtocolVersion = "2025-06-18"

// ServerConfig describes a single MCP server entry. It is the flattened
// form of a TOML [mcp.<name>] block plus the map key carried in Name.
type ServerConfig struct {
	Name       string
	Command    string
	Args       []string
	Env        map[string]string
	Enabled    bool
	TimeoutSec int
}

// Config is the input to NewManager.
type Config struct {
	Servers    []ServerConfig
	LogDir     string
	ClientInfo ClientInfo
}

// ClientInfo identifies the MCP client to the server during initialize.
type ClientInfo struct {
	Name    string
	Version string
}

// StartupReport is the per-server result of Manager.Start. Status is
// one of "running", "disabled", "failed", or "exited".
type StartupReport struct {
	Name       string
	Status     string
	ToolCount  int
	PID        int
	Command    string
	Err        string
	TimeoutSec int
	Auth       string
}

// ErrServerExited is returned by CallTool (and surfaced by the McpTool
// adapter) when the underlying server process has exited and the
// pending channel is closed without a reply.
var ErrServerExited = errors.New("mcp: server exited")

// ErrToolCallTimeout is returned by CallTool when the MCP server does
// not answer tools/call within the configured server timeout.
var ErrToolCallTimeout = errors.New("mcp: tool call timeout")
