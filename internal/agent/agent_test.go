package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

// ────────────────────────────────────────────────────────────────────────────
// Test fixtures
// ────────────────────────────────────────────────────────────────────────────

// scriptedProvider replays a fixed sequence of stream-event batches, one
// batch per ChatCompletion call. Lets us script multi-turn conversations
// (LLM responds → tool runs → LLM responds again) without an HTTP server.
type scriptedProvider struct {
	turns        [][]provider.StreamEvent
	turnIdx      int32
	chatCount    int32
	lastRequest  provider.ChatRequest
	disableTools bool
}

func (s *scriptedProvider) Name() string                                           { return "scripted" }
func (s *scriptedProvider) Slug() string                                           { return "scripted" }
func (s *scriptedProvider) BrandColor() lipgloss.Color                             { return lipgloss.Color("#000000") }
func (s *scriptedProvider) ValidateKey(_ context.Context, _ string) error          { return nil }
func (s *scriptedProvider) ListModels(_ context.Context) ([]provider.Model, error) { return nil, nil }
func (s *scriptedProvider) Pricing(string) (float64, float64)                      { return 1.0, 5.0 }
func (s *scriptedProvider) ContextWindow(string) int                               { return 100_000 }
func (s *scriptedProvider) SupportsTools(string) bool                              { return !s.disableTools }

func (s *scriptedProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	atomic.AddInt32(&s.chatCount, 1)
	idx := atomic.AddInt32(&s.turnIdx, 1) - 1
	s.lastRequest = req
	if int(idx) >= len(s.turns) {
		return nil, errors.New("scriptedProvider: no more turns scripted")
	}
	ch := make(chan provider.StreamEvent, len(s.turns[idx]))
	for _, ev := range s.turns[idx] {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// recordingTool exposes whether Execute was called and what params it
// saw. Used to verify the agent dispatches with the LLM-supplied (or
// approver-edited) arguments.
type recordingTool struct {
	name      string
	approval  bool
	executed  int32
	lastInput string
	result    tools.ToolResult
}

func (r *recordingTool) Name() string            { return r.name }
func (r *recordingTool) Description() string     { return "test tool" }
func (r *recordingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *recordingTool) RequiresApproval() bool  { return r.approval }
func (r *recordingTool) Execute(_ context.Context, p json.RawMessage) (tools.ToolResult, error) {
	atomic.AddInt32(&r.executed, 1)
	r.lastInput = string(p)
	res := r.result
	if res.Content == "" {
		res.Content = "ok"
	}
	return res, nil
}

func newAgentRig(t *testing.T, prov provider.Provider, ts ...tools.Tool) (*Agent, *session.Manager, *cost.Tracker) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.Register(prov)
	require.NoError(t, reg.SetActive(prov.Slug(), "scripted-model"))

	tr := tools.NewRegistry()
	for _, tool := range ts {
		tr.Register(tool)
	}

	sessDir := t.TempDir()
	sm := session.NewManager(sessDir)
	_, err := sm.New(prov.Slug(), "scripted-model")
	require.NoError(t, err)

	tally := filepath.Join(t.TempDir(), "tally.json")
	ct, err := cost.NewTracker(tally, func(string, string) (float64, float64) { return 1.0, 5.0 })
	require.NoError(t, err)

	a := New(Config{
		Registry:    reg,
		Tools:       tr,
		Session:     sm,
		CostTracker: ct,
		Approver:    AutoApprove(),
	})
	return a, sm, ct
}

func collect(events <-chan AgentEvent) []AgentEvent {
	var out []AgentEvent
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────────────────

func TestAgent_TextOnlyTurn(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "Hello"},
			{Type: provider.EventTextDelta, TextDelta: " there"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}},
		},
	}}

	a, sm, _ := newAgentRig(t, prov)
	events := collect(a.Run(context.Background(), "hi"))

	var text string
	var sawDone, sawUsage bool
	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			text += ev.Text
		case EventDone:
			sawDone = true
		case EventUsageUpdate:
			sawUsage = true
			assert.Equal(t, 10, ev.Usage.InputTokens)
		}
	}
	assert.Equal(t, "Hello there", text)
	assert.True(t, sawDone)
	assert.True(t, sawUsage)

	cur := sm.Current()
	require.Len(t, cur.Messages, 2, "user + assistant message persisted")
	assert.Equal(t, provider.RoleUser, cur.Messages[0].Role)
	assert.Equal(t, provider.RoleAssistant, cur.Messages[1].Role)
	assert.Equal(t, "Hello there", cur.Messages[1].Content)
	assert.Equal(t, 10, cur.TokenUsage.TotalInput)
}

func TestAgent_ToolCallApprovedAndExecuted(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			// Turn 1: LLM proposes a tool call.
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "do_thing"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"x":1}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 7}},
		},
		{
			// Turn 2: LLM responds to the tool result with text and stops.
			{Type: provider.EventTextDelta, TextDelta: "All done"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 12, OutputTokens: 3}},
		},
	}}

	rt := &recordingTool{name: "do_thing", approval: true, result: tools.ToolResult{Content: "tool ran"}}
	a, sm, _ := newAgentRig(t, prov, rt)

	evs := collect(a.Run(context.Background(), "do the thing"))

	var sawProposed, sawApproved, sawExecuted bool
	for _, ev := range evs {
		switch ev.Type {
		case EventToolCallProposed:
			sawProposed = true
			assert.Equal(t, "do_thing", ev.ToolCall.Name)
		case EventToolCallApproved:
			sawApproved = true
		case EventToolCallExecuted:
			sawExecuted = true
			assert.Equal(t, "tool ran", ev.ToolResult.Content)
		}
	}
	assert.True(t, sawProposed)
	assert.True(t, sawApproved)
	assert.True(t, sawExecuted)
	assert.Equal(t, int32(1), atomic.LoadInt32(&rt.executed))
	assert.JSONEq(t, `{"x":1}`, rt.lastInput)

	// Session should now have user, assistant(tool_call), tool, assistant(text) = 4 messages.
	cur := sm.Current()
	require.Len(t, cur.Messages, 4)
	assert.Equal(t, provider.RoleUser, cur.Messages[0].Role)
	assert.Equal(t, provider.RoleAssistant, cur.Messages[1].Role)
	require.Len(t, cur.Messages[1].ToolCalls, 1)
	assert.Equal(t, provider.RoleTool, cur.Messages[2].Role)
	assert.Equal(t, "tool ran", cur.Messages[2].Content)
	assert.Equal(t, provider.RoleAssistant, cur.Messages[3].Role)
	assert.Equal(t, "All done", cur.Messages[3].Content)
}

func TestAgent_DropsTextOnToolCallTurn(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: `<|python_tag|>{"path":"main.go"}`},
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "do_thing"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"path":"main.go"}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "done"},
			{Type: provider.EventDone},
		},
	}}

	rt := &recordingTool{name: "do_thing", result: tools.ToolResult{Content: "tool ran"}}
	a, sm, _ := newAgentRig(t, prov, rt)

	evs := collect(a.Run(context.Background(), "do it"))
	var sawLeakedText bool
	for _, ev := range evs {
		if ev.Type == EventTextDelta && strings.Contains(ev.Text, "<|python_tag|>") {
			sawLeakedText = true
		}
	}
	assert.True(t, sawLeakedText, "text still streams live; UI/session drop it when a tool call follows")

	cur := sm.Current()
	require.Len(t, cur.Messages, 4)
	assert.Equal(t, "", cur.Messages[1].Content)
	require.Len(t, cur.Messages[1].ToolCalls, 1)
	assert.Equal(t, "do_thing", cur.Messages[1].ToolCalls[0].Name)
	assert.Equal(t, "done", cur.Messages[3].Content)
}

func TestAgent_UnsupportedModelOmitsNativeTools(t *testing.T) {
	prov := &scriptedProvider{
		turns: [][]provider.StreamEvent{{
			{Type: provider.EventTextDelta, TextDelta: "plain response"},
			{Type: provider.EventDone},
		}},
	}
	prov.disableTools = true

	rt := &recordingTool{name: "do_thing"}
	a, _, _ := newAgentRig(t, prov, rt)

	_ = collect(a.Run(context.Background(), "hi"))
	assert.Empty(t, prov.lastRequest.Tools)
	require.NotEmpty(t, prov.lastRequest.Messages)
	assert.Equal(t, provider.RoleSystem, prov.lastRequest.Messages[0].Role)
	assert.Contains(t, prov.lastRequest.Messages[0].Content, "Native tool calling is unavailable")
	assert.Contains(t, prov.lastRequest.Messages[0].Content, "scripted-model")
}

func TestAgent_InvalidToolCallArgumentsError(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "do_thing"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"path":`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone},
		},
	}}

	rt := &recordingTool{name: "do_thing"}
	a, _, _ := newAgentRig(t, prov, rt)

	evs := collect(a.Run(context.Background(), "do it"))
	var gotErr error
	for _, ev := range evs {
		if ev.Type == EventError {
			gotErr = ev.Error
		}
	}
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "invalid JSON")
	assert.Equal(t, int32(0), atomic.LoadInt32(&rt.executed))
}

func TestAgent_ToolCallRejected(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "danger"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 5}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "OK, skipping"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}},
		},
	}}

	rt := &recordingTool{name: "danger", approval: true}

	reg := provider.NewRegistry()
	reg.Register(prov)
	require.NoError(t, reg.SetActive("scripted", "scripted-model"))
	tr := tools.NewRegistry()
	tr.Register(rt)
	sm := session.NewManager(t.TempDir())
	_, _ = sm.New("scripted", "scripted-model")
	a := New(Config{
		Registry: reg,
		Tools:    tr,
		Session:  sm,
		Approver: AutoReject("nope"),
	})

	evs := collect(a.Run(context.Background(), "be dangerous"))

	var rejected bool
	for _, ev := range evs {
		if ev.Type == EventToolCallRejected {
			rejected = true
		}
	}
	assert.True(t, rejected)
	assert.Equal(t, int32(0), atomic.LoadInt32(&rt.executed), "rejected tool must not be executed")

	// The rejection message ends up in the conversation as a tool-role
	// message so the LLM sees it.
	cur := sm.Current()
	var found bool
	for _, m := range cur.Messages {
		if m.Role == provider.RoleTool && m.Content == "nope" {
			found = true
		}
	}
	assert.True(t, found, "rejection reason should be in session as a tool-role message")
}

func TestAgent_ReadOnlyToolSkipsApproval(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "peek"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "done"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 2, OutputTokens: 1}},
		},
	}}

	rt := &recordingTool{name: "peek", approval: false}
	a, _, _ := newAgentRig(t, prov, rt)
	// Use AutoReject so that *if* approval were called, the tool would not
	// run — proves the agent didn't ask for approval.
	a.SetApprover(AutoReject("would be rejected"))

	collect(a.Run(context.Background(), "peek"))

	assert.Equal(t, int32(1), atomic.LoadInt32(&rt.executed), "non-approval tools must run regardless of approver")
}

func TestAgent_UnknownToolReportsError(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "missing"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 5}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "I'll try something else"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 8, OutputTokens: 3}},
		},
	}}

	a, sm, _ := newAgentRig(t, prov)
	collect(a.Run(context.Background(), "do missing"))

	cur := sm.Current()
	var foundErrMsg bool
	for _, m := range cur.Messages {
		if m.Role == provider.RoleTool && m.Content == "unknown tool: missing" {
			foundErrMsg = true
		}
	}
	assert.True(t, foundErrMsg)
}

func TestAgent_NoActiveProviderErrors(t *testing.T) {
	reg := provider.NewRegistry()
	tr := tools.NewRegistry()
	sm := session.NewManager(t.TempDir())
	_, _ = sm.New("none", "none")

	a := New(Config{
		Registry: reg,
		Tools:    tr,
		Session:  sm,
		Approver: AutoApprove(),
	})

	evs := collect(a.Run(context.Background(), "hi"))
	require.NotEmpty(t, evs)
	last := evs[len(evs)-1]
	assert.Equal(t, EventError, last.Type)
}

func TestAgent_CostTrackerUpdated(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "hi"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}},
		},
	}}
	a, sm, ct := newAgentRig(t, prov)
	collect(a.Run(context.Background(), "hi"))

	id := sm.Current().ID
	in, out := ct.SessionTokens(id)
	assert.Equal(t, 1_000_000, in)
	assert.Equal(t, 500_000, out)

	// Pricing in newAgentRig is $1/M in, $5/M out → $1 + $2.50 = $3.50.
	assert.InDelta(t, 3.50, ct.SessionCost(id), 1e-9)
}

func TestAgent_ParallelToolCallsDispatched(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c0", Name: "alpha"}},
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 1, ID: "c1", Name: "beta"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"a":1}`}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 1, ArgumentsDelta: `{"b":2}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 1}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 5}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "both done"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}},
		},
	}}

	alpha := &recordingTool{name: "alpha", approval: false}
	beta := &recordingTool{name: "beta", approval: false}
	a, sm, _ := newAgentRig(t, prov, alpha, beta)

	collect(a.Run(context.Background(), "do both"))

	assert.Equal(t, int32(1), atomic.LoadInt32(&alpha.executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&beta.executed))

	cur := sm.Current()
	require.Len(t, cur.Messages, 5, "user, assistant(2 tool_calls), tool0, tool1, assistant(text)")
	require.Len(t, cur.Messages[1].ToolCalls, 2)
	assert.Equal(t, "alpha", cur.Messages[1].ToolCalls[0].Name)
	assert.Equal(t, "beta", cur.Messages[1].ToolCalls[1].Name)
}

// ────────────────────────────────────────────────────────────────────────────
// Cancellation — Round 5
// ────────────────────────────────────────────────────────────────────────────

// cancellableProvider hands back a stream channel whose lifetime is
// bounded by the ChatCompletion ctx. The goroutine emits EventError
// (context.Canceled) as soon as ctx is done, mirroring what real
// providers do under the parser-level ctx.Err() guard added in Round 5.
type cancellableProvider struct{}

func (cancellableProvider) Name() string                                         { return "cancellable" }
func (cancellableProvider) Slug() string                                         { return "cancellable" }
func (cancellableProvider) BrandColor() lipgloss.Color                           { return lipgloss.Color("#000000") }
func (cancellableProvider) ValidateKey(context.Context, string) error            { return nil }
func (cancellableProvider) ListModels(context.Context) ([]provider.Model, error) { return nil, nil }
func (cancellableProvider) Pricing(string) (float64, float64)                    { return 1.0, 5.0 }
func (cancellableProvider) ContextWindow(string) int                             { return 100_000 }
func (cancellableProvider) SupportsTools(string) bool                            { return true }

func (cancellableProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		// Drip a single text delta so the test can see the stream
		// actually started, then block on ctx.
		ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "tick"}
		<-ctx.Done()
		ch <- provider.StreamEvent{Type: provider.EventError, Error: ctx.Err()}
	}()
	return ch, nil
}

// TestAgent_Run_CancelDuringChatCompletion drives a turn against a
// provider that blocks on ctx, cancels the ctx after the first delta,
// and asserts the events channel closes promptly with EventError whose
// cause is context.Canceled. This is the agent-level contract Round 5
// relies on: %w wrapping all the way through oneTurn / run.
func TestAgent_Run_CancelDuringChatCompletion(t *testing.T) {
	a, _, _ := newAgentRig(t, cancellableProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	events := a.Run(ctx, "hang forever")

	// Read the first event to confirm streaming actually started, then
	// cancel.
	first, ok := <-events
	require.True(t, ok, "expected at least one event before cancel")
	assert.Equal(t, EventTextDelta, first.Type)
	cancel()

	deadline := time.After(200 * time.Millisecond)
	var lastMeaningful AgentEvent
	var sawCancelErr bool
	var channelClosed bool
drain:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				channelClosed = true
				break drain
			}
			lastMeaningful = ev
			if ev.Type == EventError && ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
				sawCancelErr = true
			}
		case <-deadline:
			break drain
		}
	}
	assert.True(t, channelClosed, "events channel must close within 200ms of cancel")
	assert.True(t, sawCancelErr, "last meaningful event should be EventError wrapping context.Canceled; got %+v", lastMeaningful)
}

// blockingApprover blocks Approve on ctx.Done() — i.e. it never returns
// of its own accord. Used to prove the agent unblocks the approver when
// Run's ctx is cancelled.
type blockingApprover struct {
	called int32
}

func (b *blockingApprover) Approve(ctx context.Context, _ ApprovalRequest) ApprovalDecision {
	atomic.AddInt32(&b.called, 1)
	<-ctx.Done()
	return ApprovalDecision{Approved: false, Reason: "cancelled"}
}

type cancelingTool struct {
	started  chan struct{}
	executed int32
}

func (c *cancelingTool) Name() string            { return "slow_tool" }
func (c *cancelingTool) Description() string     { return "test tool" }
func (c *cancelingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (c *cancelingTool) RequiresApproval() bool  { return false }
func (c *cancelingTool) Execute(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	atomic.AddInt32(&c.executed, 1)
	close(c.started)
	<-ctx.Done()
	return tools.ToolResult{}, ctx.Err()
}

// TestAgent_Run_CancelDuringApproval drives a turn that reaches the
// approval gate and never resolves, then cancels the ctx. The agent
// should unblock the approver (via ctx), record the rejection, and
// close the events channel promptly.
func TestAgent_Run_CancelDuringApproval(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "danger"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}
	rt := &recordingTool{name: "danger", approval: true}
	app := &blockingApprover{}

	reg := provider.NewRegistry()
	reg.Register(prov)
	require.NoError(t, reg.SetActive("scripted", "scripted-model"))
	tr := tools.NewRegistry()
	tr.Register(rt)
	sm := session.NewManager(t.TempDir())
	_, _ = sm.New("scripted", "scripted-model")
	a := New(Config{
		Registry: reg,
		Tools:    tr,
		Session:  sm,
		Approver: app,
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := a.Run(ctx, "be dangerous")

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Drain the channel; require close within 500ms of cancel.
	deadline := time.After(1 * time.Second)
	var channelClosed bool
drain:
	for {
		select {
		case _, ok := <-events:
			if !ok {
				channelClosed = true
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	assert.True(t, channelClosed, "events channel must close once approval unblocks on cancel")
	assert.GreaterOrEqual(t, atomic.LoadInt32(&app.called), int32(1), "approver should have been invoked")
	assert.Equal(t, int32(0), atomic.LoadInt32(&rt.executed), "rejected-on-cancel tool must not execute")
}

func TestAgent_Run_CancelDuringTool(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "slow_tool"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}
	ct := &cancelingTool{started: make(chan struct{})}
	hookMarker := filepath.Join(t.TempDir(), "post-hook-ran")

	reg := provider.NewRegistry()
	reg.Register(prov)
	require.NoError(t, reg.SetActive("scripted", "scripted-model"))
	tr := tools.NewRegistry()
	tr.Register(ct)
	sm := session.NewManager(t.TempDir())
	_, _ = sm.New("scripted", "scripted-model")
	a := New(Config{
		Registry: reg,
		Tools:    tr,
		Session:  sm,
		Approver: AutoApprove(),
		Hooks: hooks.New(config.HooksConfig{
			PostToolUse: []config.HookConfig{{
				Matcher:    "slow_tool",
				Command:    touchCommand(hookMarker),
				TimeoutSec: 2,
			}},
		}, t.TempDir()),
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := a.Run(ctx, "run slow tool")

	select {
	case <-ct.started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tool did not start")
	}
	cancel()

	deadline := time.After(1 * time.Second)
	var channelClosed bool
	var sawCancelErr bool
	var sawExecuted bool
drain:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				channelClosed = true
				break drain
			}
			if ev.Type == EventError && ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
				sawCancelErr = true
			}
			if ev.Type == EventToolCallExecuted {
				sawExecuted = true
			}
		case <-deadline:
			break drain
		}
	}

	assert.True(t, channelClosed, "events channel must close once tool observes cancel")
	assert.True(t, sawCancelErr, "tool cancellation should propagate as EventError(context.Canceled)")
	assert.False(t, sawExecuted, "cancelled tool must not be reported as a completed tool result")
	assert.Equal(t, int32(1), atomic.LoadInt32(&ct.executed), "tool should have started exactly once")
	assert.NoFileExists(t, hookMarker, "post-tool hook must not run after tool cancellation")

	cur := sm.Current()
	for _, m := range cur.Messages {
		if m.Role == provider.RoleTool {
			t.Fatalf("cancelled tool must not be saved as a tool-role failure message: %+v", m)
		}
	}
}

func touchCommand(path string) string {
	if runtime.GOOS == "windows" {
		return "New-Item -ItemType File -Path '" + strings.ReplaceAll(path, "'", "''") + "' -Force | Out-Null"
	}
	return "touch '" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func TestContextManager_EstimateAndPercent(t *testing.T) {
	cm := NewContextManager(80)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello world"},
		{Role: provider.RoleAssistant, Content: "hi"},
	}
	tokens := cm.EstimateTokens(msgs)
	assert.Greater(t, tokens, 0)
	assert.Less(t, tokens, 100)

	pct := cm.UsagePercent(msgs, 100)
	assert.GreaterOrEqual(t, pct, 0)
	assert.LessOrEqual(t, pct, 100)

	assert.Equal(t, 0, cm.UsagePercent(msgs, 0), "zero max → unknown → return 0")
}

func TestContextManager_ShouldSuggestCompact(t *testing.T) {
	cm := NewContextManager(50)
	long := make([]byte, 2000)
	for i := range long {
		long[i] = 'x'
	}
	msgs := []provider.Message{{Role: provider.RoleUser, Content: string(long)}}
	// 2000 chars / 4 = ~500 tokens; in a 1000-token window that's 50%.
	assert.True(t, cm.ShouldSuggestCompact(msgs, 1000))
	assert.False(t, cm.ShouldSuggestCompact(msgs, 10_000))
}

func TestContextManager_CompactPreservesSystemAndTail(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "summary text"},
			{Type: provider.EventDone},
		},
	}}

	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "you are helpful"},
		{Role: provider.RoleUser, Content: "msg 1"},
		{Role: provider.RoleAssistant, Content: "reply 1"},
		{Role: provider.RoleUser, Content: "msg 2"},
		{Role: provider.RoleAssistant, Content: "reply 2"},
		{Role: provider.RoleUser, Content: "msg 3"},
		{Role: provider.RoleAssistant, Content: "reply 3"},
	}

	cm := NewContextManager(80)
	out, err := cm.Compact(context.Background(), prov, "scripted-model", msgs, 2)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(out), 3)
	assert.Equal(t, provider.RoleSystem, out[0].Role)
	assert.Equal(t, "you are helpful", out[0].Content)
	assert.Contains(t, out[1].Content, "summary text")
	// Last two messages of the original input must be preserved verbatim.
	tail := out[len(out)-2:]
	assert.Equal(t, "msg 3", tail[0].Content)
	assert.Equal(t, "reply 3", tail[1].Content)
}

func TestContextManager_CompactTailStartingOnToolMessageKeepsGroup(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "summary text"},
			{Type: provider.EventDone},
		},
	}}

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "before"},
		{Role: provider.RoleAssistant, Content: "old reply"},
		{Role: provider.RoleUser, Content: "run both tools"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call-a", Name: "alpha", Arguments: `{}`},
			{ID: "call-b", Name: "beta", Arguments: `{}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "call-a", Name: "alpha", Content: "alpha result"},
		{Role: provider.RoleTool, ToolCallID: "call-b", Name: "beta", Content: "beta result"},
		{Role: provider.RoleAssistant, Content: "done"},
	}

	cm := NewContextManager(80)
	out, err := cm.Compact(context.Background(), prov, "scripted-model", msgs, 2)
	require.NoError(t, err)

	require.Len(t, out, 5)
	assert.Contains(t, out[0].Content, "summary text")
	assert.Equal(t, provider.RoleAssistant, out[1].Role)
	require.Len(t, out[1].ToolCalls, 2)
	assert.Equal(t, provider.RoleTool, out[2].Role)
	assert.Equal(t, "call-a", out[2].ToolCallID)
	assert.Equal(t, provider.RoleTool, out[3].Role)
	assert.Equal(t, "call-b", out[3].ToolCallID)
	assert.Equal(t, "done", out[4].Content)
}

func TestAgent_BuildMessagesNormalizesSplitToolCallGroups(t *testing.T) {
	sm := session.NewManager(t.TempDir())
	_, err := sm.New("scripted", "scripted-model")
	require.NoError(t, err)
	require.NoError(t, sm.ReplaceMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "before"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call-a", Name: "alpha", Arguments: `{}`},
			{ID: "call-b", Name: "beta", Arguments: `{}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "call-a", Name: "alpha", Content: "alpha result"},
		{Role: provider.RoleUser, Content: "interrupted"},
		{Role: provider.RoleTool, ToolCallID: "call-b", Name: "beta", Content: "beta result"},
		{Role: provider.RoleAssistant, Content: "after"},
	}))

	a := New(Config{Session: sm})
	msgs := a.buildMessages()

	require.Len(t, msgs, 3)
	assert.Equal(t, "before", msgs[0].Content)
	assert.Equal(t, "interrupted", msgs[1].Content)
	assert.Equal(t, "after", msgs[2].Content)
	for _, msg := range msgs {
		assert.NotEqual(t, provider.RoleTool, msg.Role)
		assert.Empty(t, msg.ToolCalls)
	}
}
