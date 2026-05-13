package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/input"
)

// ────────────────────────────────────────────────────────────────────────────
// Fixtures
// ────────────────────────────────────────────────────────────────────────────

// hangingProvider's ChatCompletion emits one text delta, then blocks on
// ctx.Done(). On cancel it sends EventError(ctx.Err()) and closes the
// channel — mirroring the real provider parser contract after Round 5.
type hangingProvider struct {
	started int32
}

func (h *hangingProvider) Name() string                                         { return "hang" }
func (h *hangingProvider) Slug() string                                         { return "hang" }
func (h *hangingProvider) BrandColor() lipgloss.Color                           { return lipgloss.Color("#000000") }
func (h *hangingProvider) ValidateKey(context.Context, string) error            { return nil }
func (h *hangingProvider) ListModels(context.Context) ([]provider.Model, error) { return nil, nil }
func (h *hangingProvider) Pricing(string) (float64, float64)                    { return 0, 0 }
func (h *hangingProvider) ContextWindow(string) int                             { return 100_000 }
func (h *hangingProvider) SupportsTools(string) bool                            { return true }

func (h *hangingProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	atomic.AddInt32(&h.started, 1)
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		// Emit one delta so the App sees streaming started.
		ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "..."}
		<-ctx.Done()
		ch <- provider.StreamEvent{Type: provider.EventError, Error: ctx.Err()}
	}()
	return ch, nil
}

type releaseProvider struct {
	started int32
	release chan struct{}
}

func (r *releaseProvider) Name() string                                         { return "release" }
func (r *releaseProvider) Slug() string                                         { return "release" }
func (r *releaseProvider) BrandColor() lipgloss.Color                           { return lipgloss.Color("#000000") }
func (r *releaseProvider) ValidateKey(context.Context, string) error            { return nil }
func (r *releaseProvider) ListModels(context.Context) ([]provider.Model, error) { return nil, nil }
func (r *releaseProvider) Pricing(string) (float64, float64)                    { return 0, 0 }
func (r *releaseProvider) ContextWindow(string) int                             { return 100_000 }
func (r *releaseProvider) SupportsTools(string) bool                            { return true }

func (r *releaseProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	idx := atomic.AddInt32(&r.started, 1)
	ch := make(chan provider.StreamEvent, 4)
	go func() {
		defer close(ch)
		if idx == 1 {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "first"}
			select {
			case <-r.release:
			case <-ctx.Done():
				ch <- provider.StreamEvent{Type: provider.EventError, Error: ctx.Err()}
				return
			}
			ch <- provider.StreamEvent{Type: provider.EventDone}
			return
		}
		ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "second done"}
		ch <- provider.StreamEvent{Type: provider.EventDone}
	}()
	return ch, nil
}

// approvalProvider emits a write_file tool call on turn 0 — used to
// drive the App into a state where the approval modal is visible.
type approvalProvider struct {
	turnIdx int32
}

func (approvalProvider) Name() string                                         { return "appr" }
func (approvalProvider) Slug() string                                         { return "appr" }
func (approvalProvider) BrandColor() lipgloss.Color                           { return lipgloss.Color("#000000") }
func (approvalProvider) ValidateKey(context.Context, string) error            { return nil }
func (approvalProvider) ListModels(context.Context) ([]provider.Model, error) { return nil, nil }
func (approvalProvider) Pricing(string) (float64, float64)                    { return 0, 0 }
func (approvalProvider) ContextWindow(string) int                             { return 100_000 }
func (approvalProvider) SupportsTools(string) bool                            { return true }

func (a *approvalProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	atomic.AddInt32(&a.turnIdx, 1)
	ch := make(chan provider.StreamEvent, 8)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "fake_write"}}
		ch <- provider.StreamEvent{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{}`}}
		ch <- provider.StreamEvent{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}}
		ch <- provider.StreamEvent{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}}
		<-ctx.Done()
	}()
	return ch, nil
}

// fakeWriteTool requires approval so the agent's approver.Approve path
// fires. Execute blocks on ctx so the test can verify the approval
// modal's Hide on cancel without the tool ever running.
type fakeWriteTool struct{}

func (fakeWriteTool) Name() string            { return "fake_write" }
func (fakeWriteTool) Description() string     { return "requires approval, never runs" }
func (fakeWriteTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (fakeWriteTool) RequiresApproval() bool  { return true }
func (fakeWriteTool) Execute(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	<-ctx.Done()
	return tools.ToolResult{Content: "cancelled", IsError: true}, nil
}

// wireAgent installs a real agent.Agent on the test rig using the
// given provider (replacing any existing fakeProvider) and an
// AutoApprove approver unless one has already been set.
func wireAgent(r *testAppRig, prov provider.Provider, ts ...tools.Tool) {
	// Swap the registry's active provider so Agent.Run picks up prov.
	r.reg = provider.NewRegistry()
	r.reg.Register(prov)
	_ = r.reg.SetActive(prov.Slug(), "hang-model")
	r.app.deps.Registry = r.reg

	toolReg := tools.NewRegistry()
	for _, tool := range ts {
		toolReg.Register(tool)
	}
	r.app.deps.Tools = toolReg

	r.app.agent = agent.New(agent.Config{
		Registry: r.reg,
		Tools:    toolReg,
		Session:  r.sessions,
		Approver: r.app.approver,
	})
}

// drainPump is a mini Bubble Tea runtime: it threads tea.Cmds into
// tea.Msgs and back through Update, so the agent's event channel
// actually drains. Tests call it via RunUntil which owns the pump's
// state across multiple predicates (streaming-started, then streaming-
// finished) within a single turn.
type drainPump struct {
	t    *testing.T
	app  *App
	cmds []tea.Cmd
}

func newDrainPump(t *testing.T, a *App, initial tea.Cmd) *drainPump {
	t.Helper()
	return &drainPump{t: t, app: a, cmds: []tea.Cmd{initial}}
}

// RunUntil pumps Update until pred() returns true or the deadline fires.
// It preserves its cmds queue across calls so sequential gates observe
// the same event stream.
func (p *drainPump) RunUntil(deadline time.Duration, pred func() bool) {
	p.t.Helper()
	stop := time.After(deadline)
	for {
		if pred() {
			return
		}
		select {
		case <-stop:
			return
		default:
		}
		if len(p.cmds) == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		next := p.cmds[0]
		p.cmds = p.cmds[1:]
		if next == nil {
			continue
		}
		msg := next()
		if msg == nil {
			continue
		}
		// tea.Batch produces a BatchMsg holding sub-commands. The real
		// Bubble Tea runtime unwraps these and runs each; our pump
		// needs to do the same so follow-up cmds (like the next agent
		// event read) aren't lost inside a Println+event batch.
		if batch, ok := msg.(tea.BatchMsg); ok {
			p.cmds = append(p.cmds, batch...)
			continue
		}
		_, follow := p.app.Update(msg)
		if follow != nil {
			p.cmds = append(p.cmds, follow)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────────────────

// TestApp_CtrlC_DuringStream_CancelsTurn: first Ctrl+C while streaming
// cancels the turn ctx (a.cancelTurn cleared synchronously), the
// goroutine drains, and a.streaming flips to false on agentDoneMsg.
func TestApp_CtrlC_DuringStream_CancelsTurn(t *testing.T) {
	r := newTestApp(t)
	prov := &hangingProvider{}
	wireAgent(r, prov)

	model, cmd := r.app.Update(input.SubmitMsg{Text: "hi"})
	a := model.(*App)
	if !a.streaming {
		t.Fatalf("expected streaming=true after submit")
	}
	if a.cancelTurn == nil {
		t.Fatalf("expected cancelTurn to be non-nil after startTurn")
	}

	pump := newDrainPump(t, a, cmd)
	pump.RunUntil(500*time.Millisecond, func() bool {
		return atomic.LoadInt32(&prov.started) > 0
	})

	// Ctrl+C. Synchronous expectations: cancelTurn cleared.
	_, _ = a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if a.cancelTurn != nil {
		t.Fatalf("expected cancelTurn to be nil after Ctrl+C")
	}

	// Continue draining; eventually agentDoneMsg arrives and flips
	// streaming to false.
	pump.RunUntil(2*time.Second, func() bool {
		return !a.streaming
	})
	if a.streaming {
		t.Fatalf("expected streaming=false after agentDoneMsg")
	}
}

// TestApp_CtrlC_WhenIdle_Quits: Ctrl+C with no active turn produces
// tea.Quit.
func TestApp_CtrlC_WhenIdle_Quits(t *testing.T) {
	r := newTestApp(t)
	if r.app.streaming {
		t.Fatalf("fresh rig should not be streaming")
	}
	_, cmd := r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected non-nil cmd from idle Ctrl+C")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

// TestApp_CtrlC_HidesApprovalModal: when the approval modal is up,
// Ctrl+C during streaming hides it. (We simulate by calling Show
// directly — driving the approval flow through the agent requires a
// polling pump the test rig doesn't provide.)
func TestApp_CtrlC_HidesApprovalModal(t *testing.T) {
	r := newTestApp(t)
	prov := &hangingProvider{}
	wireAgent(r, prov)

	_, cmd := r.app.Update(input.SubmitMsg{Text: "hi"})
	if !r.app.streaming {
		t.Fatalf("expected streaming=true after submit")
	}
	pump := newDrainPump(t, r.app, cmd)
	pump.RunUntil(500*time.Millisecond, func() bool {
		return atomic.LoadInt32(&prov.started) > 0
	})

	// Simulate a pending approval by showing the modal directly.
	r.app.approval.Show(fakeWriteTool{}, provider.ToolCall{ID: "c1", Name: "fake_write", Arguments: "{}"})
	if !r.app.approval.Visible() {
		t.Fatalf("modal should be visible after Show")
	}

	_, _ = r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if r.app.approval.Visible() {
		t.Fatalf("approval modal should be hidden after Ctrl+C")
	}
	if r.app.cancelTurn != nil {
		t.Fatalf("cancelTurn should be nil after Ctrl+C")
	}
}

// TestApp_CtrlC_RendersTurnCancelledLine: after Ctrl+C mid-stream and a
// full drain, the conversation ends with a "turn cancelled" system
// line.
func TestApp_CtrlC_RendersTurnCancelledLine(t *testing.T) {
	r := newTestApp(t)
	prov := &hangingProvider{}
	wireAgent(r, prov)

	_, cmd := r.app.Update(input.SubmitMsg{Text: "hi"})
	a := r.app
	pump := newDrainPump(t, a, cmd)
	pump.RunUntil(500*time.Millisecond, func() bool {
		return atomic.LoadInt32(&prov.started) > 0
	})

	_, _ = a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})

	pump.RunUntil(2*time.Second, func() bool {
		return !a.streaming
	})

	txt := convText(a)
	if !strings.Contains(txt, "turn cancelled") {
		t.Fatalf("conversation missing 'turn cancelled' system line:\n%s", txt)
	}
	// And it should NOT have rendered the raw error text.
	if strings.Contains(strings.ToLower(txt), "error: context canceled") {
		t.Fatalf("conversation leaked raw ctx error:\n%s", txt)
	}
}

// TestApp_DoubleCtrlC_DuringShutdown_DoesNotQuit: first Ctrl+C cancels;
// before agentDoneMsg drains, second Ctrl+C must NOT produce tea.Quit
// (cancelTurn is already nil, but streaming is still true, so we're in
// the "cancel no-op" branch).
func TestApp_DoubleCtrlC_DuringShutdown_DoesNotQuit(t *testing.T) {
	r := newTestApp(t)
	prov := &hangingProvider{}
	wireAgent(r, prov)

	_, cmd := r.app.Update(input.SubmitMsg{Text: "hi"})
	a := r.app
	pump := newDrainPump(t, a, cmd)
	pump.RunUntil(500*time.Millisecond, func() bool {
		return atomic.LoadInt32(&prov.started) > 0
	})

	// First Ctrl+C.
	_, first := a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	_ = first

	// Intentionally do NOT drain agentDoneMsg. a.streaming stays true;
	// cancelTurn is now nil. Second Ctrl+C must be a no-op.
	_, second := a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if second != nil {
		// If any cmd was returned, assert it is not a Quit.
		msg := second()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatalf("second Ctrl+C during shutdown must not produce tea.QuitMsg")
		}
	}
	if !a.streaming {
		t.Fatalf("streaming should still be true during shutdown window")
	}
	// And the ctx is truly cancelled under the hood, so draining finishes.
	pump.RunUntil(2*time.Second, func() bool { return !a.streaming })
}

func TestApp_SubmitWhileStreamingQueuesAndRunsNext(t *testing.T) {
	r := newTestApp(t)
	prov := &releaseProvider{release: make(chan struct{})}
	wireAgent(r, prov)
	r.app.resize(120, 40)

	_, cmd := r.app.Update(input.SubmitMsg{Text: "first prompt"})
	a := r.app
	pump := newDrainPump(t, a, cmd)
	pump.RunUntil(500*time.Millisecond, func() bool {
		return atomic.LoadInt32(&prov.started) == 1
	})
	if !a.streaming {
		t.Fatalf("expected first turn to be streaming")
	}

	_, follow := a.Update(input.SubmitMsg{Text: "second prompt"})
	if follow != nil {
		pump.cmds = append(pump.cmds, follow)
	}
	if got := len(a.queuedInputs); got != 1 {
		t.Fatalf("queuedInputs = %d, want 1", got)
	}
	if a.input.Value() != "" {
		t.Fatalf("input should reset after queued submit, got %q", a.input.Value())
	}
	if !strings.Contains(convText(a), "You (queued)") {
		t.Fatalf("queued user bubble missing:\n%s", convText(a))
	}
	if !strings.Contains(a.topbar.View(), "1 queued") {
		t.Fatalf("topbar missing queued count:\n%s", a.topbar.View())
	}

	close(prov.release)
	pump.RunUntil(2*time.Second, func() bool {
		return atomic.LoadInt32(&prov.started) == 2 && !a.streaming
	})
	if a.streaming {
		t.Fatalf("expected queued turn to finish")
	}
	if got := len(a.queuedInputs); got != 0 {
		t.Fatalf("queuedInputs after drain = %d, want 0", got)
	}
	if !strings.Contains(convText(a), "second done") {
		t.Fatalf("queued turn did not run:\n%s", convText(a))
	}
}

// Sanity: confirm isCancellation walks wrapped chains.
func TestIsCancellation_WalksErrorChain(t *testing.T) {
	wrapped := errors.New("outer")
	if isCancellation(wrapped) {
		t.Fatalf("plain error should not be cancellation")
	}
	chain := errors.Join(errors.New("x"), context.Canceled)
	if !isCancellation(chain) {
		t.Fatalf("joined chain containing context.Canceled should be cancellation")
	}
}
