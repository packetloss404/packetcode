package mcp

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// StubHandler dispatches a single MCP method on the StubServer. Either
// result or err must be non-nil. Returning err sends an ErrorObj reply;
// returning result marshals it as the response result.
type StubHandler func(params json.RawMessage) (result any, err *ErrorObj)

// StubServer is an in-process MCP server used by tests. It owns two
// io.Pipe pairs that simulate the child's stdin/stdout. Wire it into a
// Client by handing the client side to NewClientWithStub.
type StubServer struct {
	handlers map[string]StubHandler
	captured chan json.RawMessage

	// stdinReader is what the StubServer reads requests from (the
	// client's stdin writer side).
	stdinReader *io.PipeReader
	stdinWriter *io.PipeWriter

	// stdoutReader is what the client reads responses from (the
	// StubServer's stdout writer side).
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter

	wmu sync.Mutex // serialises stdoutWriter writes
	wg  sync.WaitGroup
}

// NewStubServer returns a StubServer wired up with the provided method
// handlers. Call Start() to launch the dispatcher goroutine.
func NewStubServer(handlers map[string]StubHandler) *StubServer {
	if handlers == nil {
		handlers = map[string]StubHandler{}
	}
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	return &StubServer{
		handlers:     handlers,
		stdinReader:  stdinR,
		stdinWriter:  stdinW,
		stdoutReader: stdoutR,
		stdoutWriter: stdoutW,
	}
}

// StdinWriter is the side the client writes its requests to.
func (s *StubServer) StdinWriter() io.WriteCloser { return s.stdinWriter }

// StdoutReader is the side the client reads server messages from.
func (s *StubServer) StdoutReader() io.ReadCloser { return s.stdoutReader }

// Start launches the dispatcher goroutine. Each line read from the
// client's stdin is parsed as a JSON-RPC envelope and routed to the
// matching handler; unknown methods reply with method-not-found.
func (s *StubServer) Start() {
	s.wg.Add(1)
	go s.loop()
}

// Stop closes both ends of the pipes and waits for the dispatcher to
// exit. Safe to call multiple times.
func (s *StubServer) Stop() {
	_ = s.stdoutWriter.Close()
	_ = s.stdinReader.Close()
	s.wg.Wait()
}

// CloseStdout closes the server-to-client direction. The client's
// reader will hit EOF on the next Scan and mark the client dead.
func (s *StubServer) CloseStdout() { _ = s.stdoutWriter.Close() }

// SendNotification pushes a server-initiated notification to the client.
func (s *StubServer) SendNotification(method string, params any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeLine(s.stdoutWriter, newNotification(method, params))
}

// SendRequest pushes a server-initiated request to the client.
func (s *StubServer) SendRequest(id int64, method string, params any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeLine(s.stdoutWriter, newRequest(id, method, params))
}

// SendRequestWithStringID pushes a server request whose JSON-RPC id is a
// string. MCP permits string ids for server-initiated requests, and the
// client must preserve the id exactly in its error response.
func (s *StubServer) SendRequestWithStringID(id, method string, params any) error {
	msg := map[string]any{"jsonrpc": JSONRPCVersion, "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeLine(s.stdoutWriter, msg)
}

func (s *StubServer) loop() {
	defer s.wg.Done()
	scanner := newScanner(s.stdinReader)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
			Result json.RawMessage `json:"result,omitempty"`
			Error  *ErrorObj       `json:"error,omitempty"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if probe.Method != "" && len(probe.ID) == 0 {
			// Notification — let the test observe it via a registered
			// handler if any (handlers for notification methods are
			// invoked only for their side effects).
			if h, ok := s.handlers[probe.Method]; ok {
				_, _ = h(probe.Params)
			}
			continue
		}
		if probe.Method == "" && len(probe.ID) > 0 {
			// Response to a server-initiated request — drop.
			if s.captured != nil {
				select {
				case s.captured <- append(json.RawMessage(nil), line...):
				default:
				}
			}
			continue
		}
		// Standard client → server request.
		id, err := numericID(probe.ID)
		if err != nil {
			continue
		}
		h, ok := s.handlers[probe.Method]
		if !ok {
			s.writeError(id, ErrCodeMethodNotFound, "method not found")
			continue
		}
		result, errObj := h(probe.Params)
		if errObj != nil {
			s.writeRawError(id, errObj)
			continue
		}
		s.writeResult(id, result)
	}
}

func (s *StubServer) writeResult(id int64, result any) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(fmtInt64(id)),
	}
	if result != nil {
		raw, err := json.Marshal(result)
		if err == nil {
			resp.Result = raw
		}
	} else {
		resp.Result = json.RawMessage(`{}`)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = writeLine(s.stdoutWriter, resp)
}

func (s *StubServer) writeError(id int64, code int, msg string) {
	s.writeRawError(id, &ErrorObj{Code: code, Message: msg})
}

func (s *StubServer) writeRawError(id int64, e *ErrorObj) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      json.RawMessage(fmtInt64(id)),
		Error:   e,
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = writeLine(s.stdoutWriter, resp)
}

func (s *StubServer) CaptureClientResponses(buffer int) <-chan json.RawMessage {
	if buffer <= 0 {
		buffer = 1
	}
	s.captured = make(chan json.RawMessage, buffer)
	return s.captured
}

// DefaultInitializeHandler returns a StubHandler that responds to
// `initialize` with a tools-capable serverInfo block. Tests compose
// this with their own tools/list and tools/call handlers.
func DefaultInitializeHandler(serverName string) StubHandler {
	return func(params json.RawMessage) (any, *ErrorObj) {
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": "0.0.1-stub",
			},
		}, nil
	}
}

// DefaultToolsListHandler returns a StubHandler that responds to
// tools/list with the provided tools slice.
func DefaultToolsListHandler(tools []ServerTool) StubHandler {
	return func(params json.RawMessage) (any, *ErrorObj) {
		return map[string]any{"tools": tools}, nil
	}
}

// NewClientWithStub is a test helper that wires the given stub into a
// Client and runs the handshake. Returns the connected client.
func NewClientWithStub(name string, stub *StubServer, info ClientInfo, timeoutSec int) (*Client, error) {
	stub.Start()
	timeout := defaultInitTimeoutSec
	if timeoutSec > 0 {
		timeout = timeoutSec
	}
	return newClientFromIO(
		context.Background(),
		name,
		stub.StdinWriter(),
		stub.StdoutReader(),
		nil, nil,
		time.Duration(timeout)*time.Second,
		info,
	)
}

// bytesReader is a tiny io.Reader over a []byte. We avoid importing
// bytes here only to keep the test helper import set minimal.
type bytesReader struct {
	buf []byte
	off int
}

func newBytesReader(b []byte) *bytesReader { return &bytesReader{buf: b} }

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.off >= len(r.buf) {
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	return n, nil
}
