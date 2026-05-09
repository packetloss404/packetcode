package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubInfo is the ClientInfo every test in this file uses.
var stubInfo = ClientInfo{Name: "packetcode-test", Version: "0.0.0"}

// makeBasicStub wires up a StubServer with the standard initialize +
// tools/list pair and any extra handlers the caller supplies.
func makeBasicStub(t *testing.T, name string, tools []ServerTool, extra map[string]StubHandler) *StubServer {
	t.Helper()
	handlers := map[string]StubHandler{
		"initialize":                DefaultInitializeHandler(name),
		"notifications/initialized": func(_ json.RawMessage) (any, *ErrorObj) { return nil, nil },
		"tools/list":                DefaultToolsListHandler(tools),
	}
	for k, v := range extra {
		handlers[k] = v
	}
	return NewStubServer(handlers)
}

// TestClient_InitializeHandshake confirms the happy path: spin up the
// stub, wire it into a client, and assert the cached tool list matches.
func TestClient_InitializeHandshake(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{
		{Name: "hello", Description: "say hi", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	tools := cli.Tools()
	require.Len(t, tools, 1)
	assert.Equal(t, "hello", tools[0].Name)
	assert.Equal(t, "say hi", tools[0].Description)
	assert.Equal(t, "stub", cli.ServerInfo().Name)
	assert.True(t, cli.IsAlive())
}

func TestClient_InitializeRejectsProtocolMismatch(t *testing.T) {
	stub := NewStubServer(map[string]StubHandler{
		"initialize": func(_ json.RawMessage) (any, *ErrorObj) {
			return map[string]any{
				"protocolVersion": "1900-01-01",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "old", "version": "0.0.1"},
			}, nil
		},
	})
	defer stub.Stop()

	cli, err := NewClientWithStub("old", stub, stubInfo, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported protocol version")
	assert.Nil(t, cli)
}

// TestClient_InitializeTimeout asserts that a server that never answers
// initialize causes NewClient to fail with the canonical timeout error.
func TestClient_InitializeTimeout(t *testing.T) {
	// All handlers absent: the stub will reply with method-not-found
	// for initialize (because we wired no handler), which would NOT
	// trigger the timeout path. We need to actually *not* reply. Build
	// a stub with an initialize handler that never returns.
	block := make(chan struct{})
	stub := NewStubServer(map[string]StubHandler{
		"initialize": func(_ json.RawMessage) (any, *ErrorObj) {
			<-block
			return map[string]any{}, nil
		},
	})
	defer func() {
		close(block)
		stub.Stop()
	}()

	cli, err := NewClientWithStub("slow", stub, stubInfo, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialize timeout")
	assert.Nil(t, cli)
}

// TestClient_CallTool_Success exercises a single tools/call round-trip
// against a stub that echoes a text content item.
func TestClient_CallTool_Success(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{
		{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, map[string]StubHandler{
		"tools/call": func(params json.RawMessage) (any, *ErrorObj) {
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(params, &p)
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "hello world"},
				},
				"isError": false,
			}, nil
		},
	})
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	res, err := cli.CallTool(context.Background(), "echo", json.RawMessage(`{"q":"x"}`))
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "text", res.Content[0].Type)
	assert.Equal(t, "hello world", res.Content[0].Text)
	assert.False(t, res.IsError)
}

// TestClient_CallTool_IsError asserts isError=true is preserved.
func TestClient_CallTool_IsError(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{
		{Name: "fail"},
	}, map[string]StubHandler{
		"tools/call": func(params json.RawMessage) (any, *ErrorObj) {
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "boom"},
				},
				"isError": true,
			}, nil
		},
	})
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	res, err := cli.CallTool(context.Background(), "fail", nil)
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

// TestClient_CallTool_TimeoutDistinguishesParentContext asserts a
// server-side tools/call timeout uses the MCP timeout sentinel.
func TestClient_CallTool_TimeoutDistinguishesParentContext(t *testing.T) {
	hold := make(chan struct{})
	stub := makeBasicStub(t, "stub", []ServerTool{{Name: "slow"}}, map[string]StubHandler{
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
		"stub",
		stub.StdinWriter(),
		stub.StdoutReader(),
		nil, nil,
		50*time.Millisecond,
		stubInfo,
	)
	require.NoError(t, err)

	_, err = cli.CallTool(context.Background(), "slow", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolCallTimeout), "unexpected error: %v", err)
	assert.False(t, errors.Is(err, context.DeadlineExceeded), "tool timeout should use the MCP timeout sentinel")
}

// TestClient_ConcurrentCalls fires 20 parallel CallTool requests and
// asserts that every reply is routed to the right caller (the stub
// echoes the request id back as text).
func TestClient_ConcurrentCalls(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{
		{Name: "echoid"},
	}, map[string]StubHandler{
		"tools/call": func(params json.RawMessage) (any, *ErrorObj) {
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(params, &p)
			var args struct {
				Tag string `json:"tag"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			return map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": args.Tag},
				},
				"isError": false,
			}, nil
		},
	})
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	const N = 20
	var wg sync.WaitGroup
	mismatches := atomic.Int32{}
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			tag := "tag-" + intToStr(i)
			args, _ := json.Marshal(map[string]any{"tag": tag})
			res, err := cli.CallTool(context.Background(), "echoid", args)
			if err != nil || len(res.Content) != 1 || res.Content[0].Text != tag {
				mismatches.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(0), mismatches.Load())
}

// TestClient_StdoutEOF_UnblocksPending opens 2 in-flight calls, then
// closes the stub's stdout. Both calls must return ErrServerExited
// within a small bounded time.
func TestClient_StdoutEOF_UnblocksPending(t *testing.T) {
	hold := make(chan struct{})
	stub := makeBasicStub(t, "stub", []ServerTool{{Name: "blocky"}}, map[string]StubHandler{
		"tools/call": func(params json.RawMessage) (any, *ErrorObj) {
			<-hold
			return map[string]any{"content": []any{}, "isError": false}, nil
		},
	})

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)

	type result struct{ err error }
	resCh := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := cli.CallTool(context.Background(), "blocky", nil)
			resCh <- result{err: err}
		}()
	}

	// Give the calls a moment to issue requests.
	time.Sleep(50 * time.Millisecond)
	stub.CloseStdout()
	defer func() { close(hold); stub.Stop() }()

	deadline := time.After(500 * time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case r := <-resCh:
			require.True(t, errors.Is(r.err, ErrServerExited), "want ErrServerExited, got %v", r.err)
		case <-deadline:
			t.Fatalf("call %d did not unblock within deadline", i)
		}
	}
	assert.False(t, cli.IsAlive())
}

// TestClient_ServerInitiatedRequest_RespondsMethodNotFound asserts the
// reader replies to a server-initiated request with method-not-found.
func TestClient_ServerInitiatedRequest_RespondsMethodNotFound(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{}, nil)
	captured := stub.CaptureClientResponses(1)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	require.NoError(t, stub.SendRequest(99, "sampling/createMessage", map[string]any{}))

	select {
	case raw := <-captured:
		var resp struct {
			ID    json.RawMessage `json:"id"`
			Error *ErrorObj       `json:"error"`
		}
		require.NoError(t, json.Unmarshal(raw, &resp))
		assert.JSONEq(t, `99`, string(resp.ID))
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeMethodNotFound, resp.Error.Code)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for method-not-found response")
	}
}

func TestClient_ServerInitiatedRequest_StringIDRespondsMethodNotFound(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{}, nil)
	captured := stub.CaptureClientResponses(1)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	require.NoError(t, stub.SendRequestWithStringID("server-1", "sampling/createMessage", map[string]any{}))

	select {
	case raw := <-captured:
		var resp struct {
			ID    json.RawMessage `json:"id"`
			Error *ErrorObj       `json:"error"`
		}
		require.NoError(t, json.Unmarshal(raw, &resp))
		assert.JSONEq(t, `"server-1"`, string(resp.ID))
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeMethodNotFound, resp.Error.Code)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for method-not-found response")
	}
}

// TestClient_ServerNotification_Ignored sends a notification from the
// stub and asserts the client neither errors nor crashes.
func TestClient_ServerNotification_Ignored(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{}, nil)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)
	defer cli.Close(time.Second)

	require.NoError(t, stub.SendNotification("notifications/tools/list_changed", map[string]any{}))

	time.Sleep(50 * time.Millisecond)
	assert.True(t, cli.IsAlive())
}

// TestClient_Close_ClosesStdinAndWaits asserts a clean Close on a stub
// that exits when its stdin is closed.
func TestClient_Close_ClosesStdinAndWaits(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{}, nil)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)

	// Close the stub's outbound side concurrently so the client's
	// reader hits EOF and the Close call's "wait for dead" loop
	// terminates promptly.
	go func() {
		time.Sleep(20 * time.Millisecond)
		stub.CloseStdout()
	}()
	err = cli.Close(time.Second)
	require.NoError(t, err)
	assert.False(t, cli.IsAlive())
}

// TestClient_Close_KillsHangingServer asserts that when the server
// never exits (no reaper, no stdout EOF) Close still returns within the
// timeout window.
//
// Because our stub runs in-process there is no real "process" to kill;
// instead we assert that Close returns an error within the expected
// time when the stub never closes its stdout.
func TestClient_Close_KillsHangingServer(t *testing.T) {
	stub := makeBasicStub(t, "stub", []ServerTool{}, nil)
	defer stub.Stop()

	cli, err := NewClientWithStub("stub", stub, stubInfo, 5)
	require.NoError(t, err)

	start := time.Now()
	err = cli.Close(150 * time.Millisecond)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "Close should bail within bounded time")
}

// intToStr is a tiny helper used by TestClient_ConcurrentCalls to avoid
// pulling strconv into the top-level imports of this test file.
func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
