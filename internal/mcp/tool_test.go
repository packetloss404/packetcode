package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/packetcode/packetcode/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMcpTool_AdaptsNameAsProviderSafe asserts the adapter exposes
// a provider-safe public name while retaining the original MCP tool for
// execution.
func TestMcpTool_AdaptsNameAsProviderSafe(t *testing.T) {
	stub := makeBasicStub(t, "fs", []ServerTool{
		{Name: "read_file", Description: "read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil)
	defer stub.Stop()
	cli, err := NewClientWithStub("fs", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	mt := NewMcpTool(cli, cli.Tools()[0])
	assert.Equal(t, "fs__read_file", mt.Name())
	assert.Equal(t, "read a file", mt.Description())
	assert.True(t, mt.RequiresApproval())
}

func TestMcpTool_SafeNameSanitizesAndCaps(t *testing.T) {
	got := safeToolName("my.server", "read/file")
	assert.Equal(t, "my_server__read_file", got)

	long := safeToolName(strings.Repeat("server", 20), strings.Repeat("tool", 20))
	assert.LessOrEqual(t, len(long), 64)
	assert.NotContains(t, long, ".")
	assert.NotContains(t, long, "/")
}

func TestRegisterTools_SkipsAliasCollisions(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fakeTool{name: "fs__read_file"})
	client := &Client{name: "fs", tools: []ServerTool{
		{Name: "read_file"},
		{Name: "write_file"},
		{Name: "write/file"},
	}}

	reports := RegisterTools(reg, []*Client{client})
	require.Len(t, reports, 3)
	assert.Equal(t, "skipped", reports[0].Status)
	assert.Equal(t, "registered", reports[1].Status)
	assert.Equal(t, "skipped", reports[2].Status)

	all := reg.All()
	assert.Len(t, all, 2)
	_, ok := reg.Get("fs__read_file")
	assert.True(t, ok)
	_, ok = reg.Get("fs__write_file")
	assert.True(t, ok)
}

// TestMcpTool_Execute_DeadClient asserts that calls against a dead
// client surface as an IsError ToolResult, not as a Go-level error.
func TestMcpTool_Execute_DeadClient(t *testing.T) {
	stub := makeBasicStub(t, "fs", []ServerTool{{Name: "x"}}, nil)
	cli, err := NewClientWithStub("fs", stub, stubInfo, 5)
	require.NoError(t, err)
	mt := NewMcpTool(cli, cli.Tools()[0])

	stub.CloseStdout()
	// Wait briefly for the reader to register EOF.
	for i := 0; i < 50 && cli.IsAlive(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, cli.IsAlive())

	res, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "MCP server")
	assert.Contains(t, res.Content, "exited")
	stub.Stop()
}

// TestMcpTool_Execute_FlattensContent asserts:
//   - text items are joined with '\n'
//   - non-text items render as "[<type> content omitted]"
func TestMcpTool_Execute_FlattensContent(t *testing.T) {
	stub := makeBasicStub(t, "srv", []ServerTool{{Name: "many"}}, map[string]StubHandler{
		"tools/call": func(_ json.RawMessage) (any, *ErrorObj) {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "hello"},
					{"type": "image"},
					{"type": "text", "text": "world"},
				},
				"isError": false,
			}, nil
		},
	})
	defer stub.Stop()
	cli, err := NewClientWithStub("srv", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)
	mt := NewMcpTool(cli, cli.Tools()[0])

	res, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "hello\n[image content omitted]\nworld", res.Content)
}

// TestMcpTool_Execute_NullParams asserts that a literal `null` (or
// empty) params blob is rewritten to `{}` before being forwarded.
func TestMcpTool_Execute_NullParams(t *testing.T) {
	captured := atomic.Value{}
	stub := makeBasicStub(t, "srv", []ServerTool{{Name: "t"}}, map[string]StubHandler{
		"tools/call": func(params json.RawMessage) (any, *ErrorObj) {
			var p struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(params, &p)
			captured.Store(string(p.Arguments))
			return map[string]any{"content": []any{}, "isError": false}, nil
		},
	})
	defer stub.Stop()
	cli, err := NewClientWithStub("srv", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)
	mt := NewMcpTool(cli, cli.Tools()[0])

	_, err = mt.Execute(context.Background(), json.RawMessage(`null`))
	require.NoError(t, err)
	assert.Equal(t, "{}", captured.Load())

	_, err = mt.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", captured.Load())
}

// TestMcpTool_Execute_CtxCancellation asserts that ctx cancellation
// surfaces as a Go error so the agent loop can abort the turn.
func TestMcpTool_Execute_CtxCancellation(t *testing.T) {
	hold := make(chan struct{})
	stub := makeBasicStub(t, "srv", []ServerTool{{Name: "blocky"}}, map[string]StubHandler{
		"tools/call": func(_ json.RawMessage) (any, *ErrorObj) {
			<-hold
			return map[string]any{"content": []any{}, "isError": false}, nil
		},
	})
	defer func() { close(hold); stub.Stop() }()

	cli, err := NewClientWithStub("srv", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)
	mt := NewMcpTool(cli, cli.Tools()[0])

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = mt.Execute(ctx, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "context canceled"),
		"unexpected error: %v", err)
}

// TestMcpTool_Execute_ToolTimeout surfaces the MCP server timeout as
// a tool error result instead of aborting the agent turn.
func TestMcpTool_Execute_ToolTimeout(t *testing.T) {
	hold := make(chan struct{})
	stub := makeBasicStub(t, "srv", []ServerTool{{Name: "blocky"}}, map[string]StubHandler{
		"tools/call": func(_ json.RawMessage) (any, *ErrorObj) {
			<-hold
			return map[string]any{"content": []any{}, "isError": false}, nil
		},
	})
	stub.Start()
	defer func() {
		close(hold)
		stub.Stop()
	}()

	cli, err := newClientFromIO(
		context.Background(),
		"srv",
		stub.StdinWriter(),
		stub.StdoutReader(),
		nil, nil,
		50*time.Millisecond,
		stubInfo,
	)
	require.NoError(t, err)
	mt := NewMcpTool(cli, cli.Tools()[0])

	res, err := mt.Execute(context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "mcp: tool call timeout")
	assert.Contains(t, res.Content, "srv.blocky")
}

// TestMcpTool_Schema_PassesThrough asserts the inputSchema is forwarded
// to callers verbatim.
func TestMcpTool_Schema_PassesThrough(t *testing.T) {
	custom := json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"]}`)
	stub := makeBasicStub(t, "srv", []ServerTool{
		{Name: "t", InputSchema: custom},
	}, nil)
	defer stub.Stop()
	cli, err := NewClientWithStub("srv", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)
	mt := NewMcpTool(cli, cli.Tools()[0])
	assert.JSONEq(t, string(custom), string(mt.Schema()))
}

type fakeTool struct{ name string }

func (f fakeTool) Name() string            { return f.name }
func (f fakeTool) Description() string     { return "fake" }
func (f fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fakeTool) RequiresApproval() bool  { return false }
func (f fakeTool) Execute(context.Context, json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
