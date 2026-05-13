package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// defaultInitTimeoutSec is the fall-back timeout applied to initialize,
// tools/list, and tools/call when ServerConfig.TimeoutSec is zero or negative.
const defaultInitTimeoutSec = 10

// ServerInfo is the inner serverInfo block of an initialize response.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult mirrors the MCP `initialize` response.
type initializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	ServerInfo      ServerInfo                 `json:"serverInfo"`
}

// ServerTool is a single entry from a tools/list response.
type ServerTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsListResult mirrors the MCP `tools/list` response.
type toolsListResult struct {
	Tools []ServerTool `json:"tools"`
}

// ContentItem is one entry from a tools/call result content array. Only
// type=="text" is decoded; other types are flattened by the adapter.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCallResult is the decoded payload of a successful tools/call
// response. The adapter flattens Content into a tools.ToolResult.
type ToolCallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// rpcResponse is what the reader goroutine ships through pending channels.
type rpcResponse struct {
	Result json.RawMessage
	Err    error
}

// Client drives a single MCP server over stdio. All public methods are
// safe for concurrent use.
type Client struct {
	name        string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	logFile     *os.File
	wmu         sync.Mutex // serialises stdin writes
	deathMu     sync.Mutex
	nextID      atomic.Int64
	pending     sync.Map // map[int64]chan rpcResponse
	dead        atomic.Bool
	deadErr     atomic.Value // error
	serverInfo  ServerInfo
	tools       []ServerTool
	callTimeout time.Duration
}

// NewClient spawns the configured server, performs the MCP handshake
// (initialize → notifications/initialized → tools/list), and returns the
// fully connected client. Any failure during handshake kills the child
// process before returning.
func NewClient(ctx context.Context, cfg ServerConfig, logDir string, info ClientInfo) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd, stdin, stdout, logFile, err := spawnServerProcess(cfg, logDir)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if cfg.TimeoutSec <= 0 {
		timeout = defaultInitTimeoutSec * time.Second
	}

	c := &Client{
		name:        cfg.Name,
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		logFile:     logFile,
		callTimeout: timeout,
	}

	go c.readerLoop()
	go c.reaperLoop()

	if err := c.handshake(ctx, timeout, info); err != nil {
		// Kill + drain so we don't leak the child.
		_ = c.killAndWait(2 * time.Second)
		return nil, err
	}
	return c, nil
}

// NewClientFromIO is a test seam: it wires an existing pair of pipes
// (and an optional logFile/cmd) into a Client and runs the handshake.
// Production callers go through NewClient. logFile may be nil.
func newClientFromIO(ctx context.Context, name string, stdin io.WriteCloser, stdout io.ReadCloser, logFile *os.File, cmd *exec.Cmd, timeout time.Duration, info ClientInfo) (*Client, error) {
	c := &Client{
		name:        name,
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		logFile:     logFile,
		callTimeout: timeout,
	}
	go c.readerLoop()
	if cmd != nil {
		go c.reaperLoop()
	}
	if err := c.handshake(ctx, timeout, info); err != nil {
		c.markDead(err)
		_ = c.stdin.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context, timeout time.Duration, info ClientInfo) error {
	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	initParams := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    info.Name,
			"version": info.Version,
		},
	}
	raw, err := c.callTimed(initCtx, "initialize", initParams)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("initialize timeout")
		}
		return fmt.Errorf("initialize: %w", err)
	}
	var ir initializeResult
	if err := json.Unmarshal(raw, &ir); err != nil {
		return fmt.Errorf("decode initialize result: %w", err)
	}
	if _, ok := ir.Capabilities["tools"]; !ok {
		return fmt.Errorf("server does not expose tools")
	}
	if ir.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %q (want %q)", ir.ProtocolVersion, ProtocolVersion)
	}
	c.serverInfo = ir.ServerInfo

	// notifications/initialized — fire-and-forget.
	if err := c.sendNotification("notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("notifications/initialized: %w", err)
	}

	listCtx, cancel2 := context.WithTimeout(ctx, timeout)
	defer cancel2()
	rawList, err := c.callTimed(listCtx, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	var tl toolsListResult
	if err := json.Unmarshal(rawList, &tl); err != nil {
		return fmt.Errorf("decode tools/list result: %w", err)
	}
	c.tools = tl.Tools
	return nil
}

// Name returns the configured server name.
func (c *Client) Name() string { return c.name }

// Tools returns the cached tool list captured at handshake time.
func (c *Client) Tools() []ServerTool {
	out := make([]ServerTool, len(c.tools))
	copy(out, c.tools)
	return out
}

// ServerInfo returns the server info captured at initialize time.
func (c *Client) ServerInfo() ServerInfo { return c.serverInfo }

// PID returns the child process pid if known, or -1 if not.
func (c *Client) PID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return -1
	}
	return c.cmd.Process.Pid
}

// IsAlive reports whether the reader goroutine has not yet seen EOF and
// the reaper has not yet recorded an exit.
func (c *Client) IsAlive() bool { return !c.dead.Load() }

// DeathReason returns the error recorded when the client was marked
// dead, or nil if still alive.
func (c *Client) DeathReason() error {
	if v := c.deadErr.Load(); v != nil {
		if e, ok := v.(error); ok {
			return e
		}
	}
	return nil
}

// CallTool invokes tools/call against the server and decodes the result.
// Returns ErrServerExited if the server exits before responding.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolCallResult, error) {
	if !c.IsAlive() {
		return ToolCallResult{}, ErrServerExited
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if !json.Valid(args) {
		return ToolCallResult{}, fmt.Errorf("invalid MCP tool arguments JSON")
	}
	params := map[string]any{
		"name":      name,
		"arguments": json.RawMessage(args),
	}
	timeout := c.callTimeout
	if timeout <= 0 {
		timeout = defaultInitTimeoutSec * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := c.callTimed(callCtx, "tools/call", params)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return ToolCallResult{}, fmt.Errorf("%w after %s", ErrToolCallTimeout, timeout)
		}
		return ToolCallResult{}, err
	}
	var res ToolCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return ToolCallResult{}, fmt.Errorf("decode tools/call result: %w", err)
	}
	return res, nil
}

// callTimed sends a request and blocks for the matching response or
// ctx.Done(). It guarantees pending-map cleanup on every exit path.
func (c *Client) callTimed(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	req := newRequest(id, method, params)
	if err := c.write(req); err != nil {
		if !c.IsAlive() {
			return nil, ErrServerExited
		}
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Err != nil {
			return nil, resp.Err
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sendNotification writes a notification (no response expected). If the
// underlying writer is closed because the server is dead, that's
// reported as ErrServerExited.
func (c *Client) sendNotification(method string, params any) error {
	if !c.IsAlive() {
		return ErrServerExited
	}
	n := newNotification(method, params)
	if err := c.write(n); err != nil {
		if !c.IsAlive() {
			return ErrServerExited
		}
		return err
	}
	return nil
}

// write serialises stdin under wmu.
func (c *Client) write(msg any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeLine(c.stdin, msg)
}

// readerLoop is the per-client reader goroutine. It dispatches responses
// by id, replies method-not-found to server-initiated requests, ignores
// notifications, and on EOF closes pending channels with ErrServerExited.
func (c *Client) readerLoop() {
	scanner := newScanner(c.stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Peek to discriminate between response (has id, no method) and
		// request/notification (has method).
		var probe struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  *ErrorObj       `json:"error,omitempty"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			// Drop garbage lines silently — log file already captures
			// stderr, and we don't want a malformed message to kill
			// the client.
			continue
		}
		switch {
		case probe.Method == "" && len(probe.ID) > 0:
			// Response to one of our requests.
			id, err := numericID(probe.ID)
			if err != nil {
				continue
			}
			c.deliverResponse(id, probe.Result, probe.Error)
		case probe.Method != "" && len(probe.ID) > 0:
			// Server-initiated request — refuse politely.
			c.replyMethodNotFound(probe.ID)
		default:
			// Notification — silently ignore.
		}
	}
	// EOF or scanner error → mark dead and unblock all pending callers.
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.markDead(eofExit(err))
	c.flushPendingExited()
}

func numericID(raw json.RawMessage) (int64, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return n.Int64()
}

// eofExit produces a death-reason error that wraps both ErrServerExited
// and the underlying io error. closeExitErr inspects the chain to
// decide whether the exit was clean (EOF after stdin close).
func eofExit(underlying error) error {
	return &serverExitError{underlying: underlying}
}

// serverExitError is the structured error stored in deadErr by the
// reader goroutine. Its Unwrap chain visits ErrServerExited first
// (via errors.Is) and the underlying io error second.
type serverExitError struct {
	underlying error
}

func (e *serverExitError) Error() string {
	return ErrServerExited.Error() + ": " + e.underlying.Error()
}

// Is matches both the sentinel and any wrapped underlying error.
func (e *serverExitError) Is(target error) bool {
	if target == ErrServerExited {
		return true
	}
	return errors.Is(e.underlying, target)
}

// Unwrap returns the underlying io error so callers can errors.Is(err, io.EOF).
func (e *serverExitError) Unwrap() error { return e.underlying }

// deliverResponse forwards the result/error pair to the channel awaiting
// the matching id, then deletes the entry. A missing entry (already
// timed out / cancelled) is silently dropped.
func (c *Client) deliverResponse(id int64, result json.RawMessage, errObj *ErrorObj) {
	v, ok := c.pending.LoadAndDelete(id)
	if !ok {
		return
	}
	ch, ok := v.(chan rpcResponse)
	if !ok {
		return
	}
	resp := rpcResponse{Result: result}
	if errObj != nil {
		resp.Err = errObj
	}
	select {
	case ch <- resp:
	default:
	}
}

// replyMethodNotFound writes a JSON-RPC error response refusing the
// server's request.
func (c *Client) replyMethodNotFound(id json.RawMessage) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      append(json.RawMessage(nil), id...),
		Error: &ErrorObj{
			Code:    ErrCodeMethodNotFound,
			Message: "method not supported",
		},
	}
	_ = c.write(resp)
}

// flushPendingExited delivers ErrServerExited to every channel still in
// the pending map and removes them.
func (c *Client) flushPendingExited() {
	c.pending.Range(func(k, v any) bool {
		c.pending.Delete(k)
		ch, ok := v.(chan rpcResponse)
		if !ok {
			return true
		}
		select {
		case ch <- rpcResponse{Err: ErrServerExited}:
		default:
		}
		return true
	})
}

// markDead sets the dead flag and the reason. Idempotent. Closing
// stdin here unblocks any goroutine stuck in c.write() so its CallTool
// returns ErrServerExited promptly instead of hanging forever.
func (c *Client) markDead(err error) {
	if c.dead.CompareAndSwap(false, true) {
		c.deathMu.Lock()
		defer c.deathMu.Unlock()
		if err == nil {
			err = ErrServerExited
		}
		c.deadErr.Store(err)
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
	}
}

// reaperLoop calls cmd.Wait so the child is reaped on exit and we get
// the exit status to surface via DeathReason.
func (c *Client) reaperLoop() {
	if c.cmd == nil {
		return
	}
	err := c.cmd.Wait()
	if err == nil {
		c.markDead(eofExit(io.EOF))
	} else {
		c.markDead(eofExit(err))
		c.deathMu.Lock()
		c.deadErr.Store(eofExit(err))
		c.deathMu.Unlock()
	}
	c.flushPendingExited()
}

// Close shuts the client down: close stdin (canonical MCP shutdown
// signal), wait for the reaper up to timeout, otherwise SIGKILL and
// wait briefly more. Closes the log file. Returns the wait error if
// the child exited with non-zero status, or a timeout error if the
// process refused to die.
func (c *Client) Close(timeout time.Duration) error {
	if c.dead.Load() {
		c.closeLogFile()
		return nil
	}
	_ = c.stdin.Close()

	done := make(chan struct{}, 1)
	go func() {
		// We poll the dead flag rather than calling cmd.Wait directly
		// because reaperLoop has exclusive ownership of cmd.Wait. The
		// 20 ms tick is short enough to feel responsive and long
		// enough not to burn CPU.
		for !c.dead.Load() {
			time.Sleep(20 * time.Millisecond)
		}
		done <- struct{}{}
	}()
	select {
	case <-done:
		c.closeLogFile()
		// A clean exit (EOF or zero exit code) shows up as either
		// ErrServerExited or no wrapped exit-code. Surface only real
		// exit-code errors here; EOF after a stdin-close is the normal
		// case and should not be reported as failure.
		return closeExitErr(c.DeathReason())
	case <-time.After(timeout):
		// Hard kill.
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-done:
			c.closeLogFile()
			return closeExitErr(c.DeathReason())
		case <-time.After(500 * time.Millisecond):
			c.markDead(fmt.Errorf("%w: close timeout", ErrServerExited))
			c.closeLogFile()
			return fmt.Errorf("mcp: server %q did not exit within %s", c.name, timeout+500*time.Millisecond)
		}
	}
}

// closeExitErr filters the death reason recorded by reaperLoop. A bare
// ErrServerExited (or one wrapping io.EOF) means the server shut down
// cleanly in response to our stdin close. A wrapped exec.ExitError or
// arbitrary other error is surfaced.
func closeExitErr(reason error) error {
	if reason == nil {
		return nil
	}
	if errors.Is(reason, io.EOF) {
		return nil
	}
	if reason == ErrServerExited {
		return nil
	}
	return reason
}

// killAndWait is the failure-path companion to Close used during
// handshake errors. Best-effort kill + Wait + close log file.
func (c *Client) killAndWait(timeout time.Duration) error {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	deadline := time.After(timeout)
	for !c.dead.Load() {
		select {
		case <-deadline:
			c.closeLogFile()
			return fmt.Errorf("mcp: server %q did not exit within %s", c.name, timeout)
		case <-time.After(20 * time.Millisecond):
		}
	}
	c.closeLogFile()
	return nil
}

func (c *Client) closeLogFile() {
	if c.logFile != nil {
		_ = c.logFile.Close()
	}
}
