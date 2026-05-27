package main

import (
	"sort"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/mcp"
)

// defaultMCPTimeoutSec is the per-server startup timeout used when the
// user's [mcp.<name>] block does not override it. Matches the spec's
// "10s default initialize + tools/list budget".
const defaultMCPTimeoutSec = 10

// mcpServerConfigsFrom flattens the Config.MCP map into a slice sorted
// alphabetically by server name. A stable order matters for startup
// reporting and for the /mcp table — map iteration in Go is
// deliberately randomised.
func mcpServerConfigsFrom(cfg *config.Config) []mcp.ServerConfig {
	if cfg == nil || len(cfg.MCP) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.MCP))
	for name := range cfg.MCP {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]mcp.ServerConfig, 0, len(names))
	for _, name := range names {
		entry := cfg.MCP[name]
		timeout := entry.TimeoutSec
		if timeout <= 0 {
			timeout = defaultMCPTimeoutSec
		}
		out = append(out, mcp.ServerConfig{
			Name:       name,
			Command:    entry.Command,
			Args:       append([]string(nil), entry.Args...),
			Env:        copyEnv(entry.Env),
			EnvFrom:    append([]string(nil), entry.EnvFrom...),
			Enabled:    entry.IsEnabled(),
			TimeoutSec: timeout,
		})
	}
	return out
}

// copyEnv returns a defensive copy of the map so callers can't mutate
// the Config's in-memory state through the returned ServerConfig.
func copyEnv(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
