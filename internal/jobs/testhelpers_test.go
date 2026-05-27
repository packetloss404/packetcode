package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// scriptedProvider replays a fixed sequence of stream-event batches,
// one batch per ChatCompletion call. It is the same fake the agent
// package uses in its tests; duplicated here so we don't import a test
// file across packages.
type scriptedProvider struct {
	turns     [][]provider.StreamEvent
	turnIdx   int32
	holdOpen  bool // when true, ignore turns and stream until ctx cancels
	pricing   func(string) (float64, float64)
	models    []provider.Model
	listErr   error
	listCalls int32
	mu        sync.Mutex
	requests  []provider.ChatRequest
}

func (s *scriptedProvider) Name() string                                  { return "scripted" }
func (s *scriptedProvider) Slug() string                                  { return "scripted" }
func (s *scriptedProvider) BrandColor() lipgloss.Color                    { return lipgloss.Color("#000000") }
func (s *scriptedProvider) ValidateKey(_ context.Context, _ string) error { return nil }
func (s *scriptedProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	atomic.AddInt32(&s.listCalls, 1)
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.models == nil {
		return []provider.Model{{ID: "scripted-model"}}, nil
	}
	return s.models, nil
}
func (s *scriptedProvider) Pricing(model string) (float64, float64) {
	if s.pricing != nil {
		return s.pricing(model)
	}
	return 1.0, 5.0
}
func (s *scriptedProvider) ContextWindow(string) int  { return 100_000 }
func (s *scriptedProvider) SupportsTools(string) bool { return true }

func (s *scriptedProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.holdOpen {
		ch := make(chan provider.StreamEvent)
		go func() {
			defer close(ch)
			// Emit a tiny bit of text to prove the stream is live.
			select {
			case ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "(running)"}:
			case <-ctx.Done():
				return
			}
			// Then block until ctx is cancelled. The provider must close
			// its channel promptly when ctx is done — that's the
			// agent-loop contract.
			<-ctx.Done()
		}()
		return ch, nil
	}
	idx := atomic.AddInt32(&s.turnIdx, 1) - 1
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

func (s *scriptedProvider) snapshotRequests() []provider.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.ChatRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// scriptedAlias registers the scripted provider under a custom slug.
// Useful when tests need multiple providers with distinct slugs.
type scriptedAlias struct {
	*scriptedProvider
	slug string
}

func (a *scriptedAlias) Slug() string { return a.slug }

// fakeApprover records every approval call so tests can assert on
// the prefixed tool-call name.
type fakeApprover struct {
	mu       sync.Mutex
	calls    []agent.ApprovalRequest
	decision agent.ApprovalDecision
}

func (f *fakeApprover) Approve(_ context.Context, req agent.ApprovalRequest) agent.ApprovalDecision {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.decision.Approved || f.decision.Reason != "" {
		return f.decision
	}
	return agent.ApprovalDecision{Approved: true, EditedParams: req.Params}
}

func (f *fakeApprover) snapshotCalls() []agent.ApprovalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.ApprovalRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

type blockingApprover struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingApprover) Approve(ctx context.Context, req agent.ApprovalRequest) agent.ApprovalDecision {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return agent.ApprovalDecision{Approved: true, EditedParams: req.Params}
	case <-ctx.Done():
		return agent.ApprovalDecision{Approved: false, Reason: ctx.Err().Error()}
	}
}

// noopTool is a minimal Tool used to verify the registry-builder
// includes/excludes things correctly.
type noopTool struct {
	name     string
	approval bool
	executed int32
	executor func(ctx context.Context, raw json.RawMessage) (tools.ToolResult, error)
}

func (n *noopTool) Name() string            { return n.name }
func (n *noopTool) Description() string     { return "test tool " + n.name }
func (n *noopTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (n *noopTool) RequiresApproval() bool  { return n.approval }
func (n *noopTool) Execute(ctx context.Context, raw json.RawMessage) (tools.ToolResult, error) {
	atomic.AddInt32(&n.executed, 1)
	if n.executor != nil {
		return n.executor(ctx, raw)
	}
	return tools.ToolResult{Content: "ok"}, nil
}

// newTestManager builds a Manager wired against the scripted provider
// with all directories rooted in t.TempDir. It returns the Manager
// plus the cost tracker so callers can assert on aggregate cost.
func newTestManager(t *testing.T, prov provider.Provider, opts ...func(*Config)) (*Manager, *cost.Tracker) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.Register(prov)
	require.NoError(t, reg.SetActive(prov.Slug(), "scripted-model"))

	tally := filepath.Join(t.TempDir(), "tally.json")
	tr, err := cost.NewTracker(tally, func(string, string) (float64, float64) { return 1.0, 5.0 })
	require.NoError(t, err)

	cfg := Config{
		Registry:      reg,
		Tools:         tools.NewRegistry(),
		SessionsDir:   t.TempDir(),
		BackupsDir:    t.TempDir(),
		JobsDir:       t.TempDir(),
		CostTracker:   tr,
		PricingFor:    func(string, string) (float64, float64) { return 1.0, 5.0 },
		MaxConcurrent: 4,
		MaxDepth:      2,
		MaxTotal:      32,
		Approver:      agent.AutoApprove(),
		Root:          t.TempDir(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	mgr, _, err := NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = mgr.Shutdown(2 * time.Second)
	})
	return mgr, tr
}

// scriptedHello returns the stream events for a one-turn "hello → done"
// run. Useful for the happy-path lifecycle tests.
func scriptedHello() [][]provider.StreamEvent {
	return [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "hello"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}},
		},
	}
}

// waitFor polls predicate up to timeout. Test helper to bridge the
// async transitions.
func waitFor(t *testing.T, timeout time.Duration, msg string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor timed out after %s: %s", timeout, msg)
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "packetcode-test@example.com")
	runGit(t, root, "config", "user.name", "Packetcode Test")
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644))
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
