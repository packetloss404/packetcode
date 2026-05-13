package mcp

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/packetcode/packetcode/internal/tools"
)

// McpTool adapts a single MCP server tool to the tools.Tool interface
// so it can be registered alongside built-in tools and exercised by the
// agent loop.
type McpTool struct {
	client     *Client
	serverName string
	toolName   string
	safeName   string
	desc       string
	schema     json.RawMessage
}

// ToolRegistrationReport records whether an MCP tool alias was added to
// the global tool registry or skipped because it would collide.
type ToolRegistrationReport struct {
	Server string
	Tool   string
	Alias  string
	Status string
	Err    string
}

// NewMcpTool constructs an adapter for the given (client, serverTool)
// pair. The exposed tool name is provider-safe; Execute still forwards
// calls to the original MCP tool name.
func NewMcpTool(c *Client, t ServerTool) *McpTool {
	schema := t.InputSchema
	if len(bytes.TrimSpace(schema)) == 0 {
		// Default to an empty object schema so providers that require a
		// non-null parameter spec are happy.
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return &McpTool{
		client:     c,
		serverName: c.Name(),
		toolName:   t.Name,
		safeName:   safeToolName(c.Name(), t.Name),
		desc:       t.Description,
		schema:     schema,
	}
}

// RegisterTools adds every tool from clients to reg, skipping aliases
// that already exist. This prevents MCP aliases from silently replacing
// built-ins or other MCP tools after sanitization.
func RegisterTools(reg *tools.Registry, clients []*Client) []ToolRegistrationReport {
	if reg == nil {
		return nil
	}
	var reports []ToolRegistrationReport
	for _, c := range clients {
		if c == nil {
			continue
		}
		for _, st := range c.Tools() {
			mt := NewMcpTool(c, st)
			report := ToolRegistrationReport{Server: c.Name(), Tool: st.Name, Alias: mt.Name(), Status: "registered"}
			if _, exists := reg.Get(mt.Name()); exists {
				report.Status = "skipped"
				report.Err = "tool alias already registered"
				reports = append(reports, report)
				continue
			}
			reg.Register(mt)
			reports = append(reports, report)
		}
	}
	return reports
}

// Name returns the provider-safe public name for this MCP tool.
func (t *McpTool) Name() string { return t.safeName }

// Description returns the server-supplied description (may be empty).
func (t *McpTool) Description() string { return t.desc }

// Schema returns the inputSchema verbatim from the server.
func (t *McpTool) Schema() json.RawMessage { return t.schema }

// RequiresApproval is always true for MCP tools. A configured MCP
// server is trusted local code at spawn time, but individual tool calls
// may still have side effects. Trust mode auto-approves.
func (t *McpTool) RequiresApproval() bool { return true }

// Execute forwards the call to the underlying client and flattens the
// content array into a tools.ToolResult.
func (t *McpTool) Execute(ctx context.Context, params json.RawMessage) (tools.ToolResult, error) {
	if !t.client.IsAlive() {
		return tools.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("MCP server %q has exited — restart packetcode to reconnect", t.serverName),
		}, nil
	}

	args := params
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return tools.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("%s.%s: invalid JSON arguments", t.serverName, t.toolName),
		}, nil
	}

	res, err := t.client.CallTool(ctx, t.toolName, args)
	if err != nil {
		if errors.Is(err, ErrServerExited) {
			return tools.ToolResult{
				IsError: true,
				Content: fmt.Sprintf("MCP server %q has exited — restart packetcode to reconnect", t.serverName),
			}, nil
		}
		if errors.Is(err, ErrToolCallTimeout) {
			return tools.ToolResult{
				IsError: true,
				Content: fmt.Sprintf("%s.%s: %s", t.serverName, t.toolName, err),
			}, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tools.ToolResult{}, err
		}
		return tools.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("%s.%s: %s", t.serverName, t.toolName, err),
		}, nil
	}

	parts := make([]string, 0, len(res.Content))
	for _, item := range res.Content {
		switch item.Type {
		case "text":
			parts = append(parts, item.Text)
		default:
			parts = append(parts, fmt.Sprintf("[%s content omitted]", item.Type))
		}
	}
	return tools.ToolResult{
		Content: strings.Join(parts, "\n"),
		IsError: res.IsError,
	}, nil
}

func safeToolName(serverName, toolName string) string {
	raw := serverName + "__" + toolName
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		out = "mcp_tool"
	}
	if len(out) <= 64 {
		return out
	}
	sum := sha1.Sum([]byte(raw))
	suffix := hex.EncodeToString(sum[:])[:8]
	return strings.TrimRight(out[:55], "_-") + "_" + suffix
}
