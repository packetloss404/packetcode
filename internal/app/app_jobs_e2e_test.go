package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/approval"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
	"github.com/packetcode/packetcode/internal/ui/components/input"
	jobs_ui "github.com/packetcode/packetcode/internal/ui/components/jobs"
	"github.com/packetcode/packetcode/internal/ui/components/spinner"
	"github.com/packetcode/packetcode/internal/ui/components/topbar"
)

// scriptedE2EProvider is an in-memory provider that emits a fixed
// single-turn "hello → done" stream. We copy the shape from the jobs
// package's scriptedProvider rather than importing it (cross-package
// test fakes aren't exported by design).
type scriptedE2EProvider struct {
	turns   [][]provider.StreamEvent
	turnIdx int32
	blockCh chan struct{} // when non-nil, ChatCompletion waits on close(blockCh) before returning
}

func (s *scriptedE2EProvider) Name() string                                  { return "scripted" }
func (s *scriptedE2EProvider) Slug() string                                  { return "scripted" }
func (s *scriptedE2EProvider) BrandColor() lipgloss.Color                    { return lipgloss.Color("#000000") }
func (s *scriptedE2EProvider) ValidateKey(_ context.Context, _ string) error { return nil }
func (s *scriptedE2EProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	return []provider.Model{{ID: "scripted-model", ContextWindow: 100_000, SupportsTools: true}}, nil
}
func (s *scriptedE2EProvider) Pricing(_ string) (float64, float64) { return 0, 0 }
func (s *scriptedE2EProvider) ContextWindow(_ string) int          { return 100_000 }
func (s *scriptedE2EProvider) SupportsTools(_ string) bool         { return true }

func (s *scriptedE2EProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	// Block until the test closes blockCh if one is provided. This lets
	// us observe "job is Running" before letting it complete.
	if s.blockCh != nil {
		select {
		case <-s.blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	idx := atomic.AddInt32(&s.turnIdx, 1) - 1
	if int(idx) >= len(s.turns) {
		return nil, errors.New("scriptedE2EProvider: no more turns")
	}
	batch := s.turns[idx]
	ch := make(chan provider.StreamEvent, len(batch))
	for _, ev := range batch {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// TestE2E_SpawnAgentToolViaSlashCommand is the Bucket C smoke test
// (spec test 27). It boots a minimal App with a real jobs.Manager
// backed by a fake provider, dispatches a `/spawn hi` via the slash
// command handler, and asserts:
//
//  1. The queued-echo system message appears in the conversation pane.
//  2. The topbar's active-jobs counter reaches 1 while the job is
//     alive.
//  3. Once the fake provider's stream completes, handleJobUpdate fires
//     (via the Manager Subscribe callback) and the counter drops to 0.
//
// We deliberately exercise the slash-command + Subscribe paths
// directly rather than routing through tea.Program; the end-to-end
// Bubble Tea loop would add flakiness (goroutine scheduling of
// tea.Cmd) without testing anything unique to this feature.
func TestE2E_SpawnAgentToolViaSlashCommand(t *testing.T) {
	tmp := t.TempDir()
	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("USERPROFILE", tmp)

	sessionsDir := filepath.Join(tmp, "sessions")
	backupsDir := filepath.Join(tmp, "backups")
	jobsDir := filepath.Join(tmp, "jobs")
	for _, d := range []string{sessionsDir, backupsDir, jobsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	sessions := session.NewManager(sessionsDir)
	if _, err := sessions.New("scripted", "scripted-model"); err != nil {
		t.Fatalf("session.New: %v", err)
	}

	// Scripted provider: emits a single "hi back" text delta then
	// Done. The blockCh lets us observe the Running state before
	// completion.
	prov := &scriptedE2EProvider{
		blockCh: make(chan struct{}),
		turns: [][]provider.StreamEvent{
			{
				{Type: provider.EventTextDelta, TextDelta: "hi back"},
				{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 3, OutputTokens: 2}},
			},
		},
	}
	reg := provider.NewRegistry()
	reg.Register(prov)
	if err := reg.SetActive(prov.Slug(), "scripted-model"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	tallyPath := filepath.Join(tmp, "tally.json")
	tracker, err := cost.NewTracker(tallyPath, func(string, string) (float64, float64) { return 0, 0 })
	if err != nil {
		t.Fatalf("cost.NewTracker: %v", err)
	}

	toolReg := tools.NewRegistry()

	mgr, _, err := jobs.NewManager(jobs.Config{
		Registry:      reg,
		Tools:         toolReg,
		MainSessions:  sessions,
		SessionsDir:   sessionsDir,
		BackupsDir:    backupsDir,
		JobsDir:       jobsDir,
		CostTracker:   tracker,
		MaxConcurrent: 1,
		MaxDepth:      2,
		MaxTotal:      8,
		Root:          tmp,
		Approver:      agent.AutoApprove(),
	})
	if err != nil {
		t.Fatalf("jobs.NewManager: %v", err)
	}
	defer mgr.Shutdown(2 * time.Second)

	// Build a minimal App by hand so we don't have to stand up every
	// Deps field. The fields we need for this test are: deps.Jobs,
	// deps.Sessions, deps.Registry, topbar, conversation, input,
	// approval, jobsPanel, spinner, and the jobs.Manager subscribe
	// hook (which handleJobUpdate uses to tick the top bar).
	app := &App{
		deps: Deps{
			Jobs:     mgr,
			Sessions: sessions,
			Registry: reg,
		},
		topbar:       topbar.New(),
		conversation: conversation.New(),
		input:        input.New(),
		approval:     approval.New(),
		jobsPanel:    jobs_ui.New(),
		spinner:      spinner.New(),
		jobs:         mgr,
	}

	// Replicate the New()-time subscription: every snapshot calls
	// handleJobUpdate synchronously (we don't have a tea.Program to
	// send through). That's fine — handleJobUpdate is pure UI mutation.
	mgr.Subscribe(func(snap jobs.Snapshot) {
		// The real Bubble Tea path funnels this through Update; we
		// call the handler directly so the test doesn't spin up the
		// event loop.
		_, _ = app.handleJobUpdate(snap)
	})

	// Fire `/spawn hi` through the slash-command dispatcher the same
	// way input.SubmitMsg does in Update.
	cmd, args, ok := ParseSlashCommand("/spawn hi")
	if !ok {
		t.Fatalf("ParseSlashCommand rejected /spawn hi")
	}
	if _, _ = app.handleSlashCommand(cmd, args, "/spawn hi"); false {
		// silence unused-result lint; return values are meaningful to
		// the Bubble Tea loop but not to us here
	}

	// (1) Conversation got the queued echo.
	if !conversationContains(app, "queued") {
		t.Fatalf("expected queued echo in conversation; got %#v", app.conversation)
	}

	// (2) Top-bar counter reaches 1 while the job is running. After
	// handleSpawnCommand runs, refreshTopBar has already ticked the
	// counter to 1 synchronously — but under load the Subscribe
	// callback (running in its own goroutine) may race. We give it a
	// generous 5s so CI doesn't flake.
	waitForEq(t, 5*time.Second, "topbar jobs == 1", func() int {
		return app.topbar.Jobs()
	}, 1)

	// (3) Release the fake provider's stream. After the agent loop
	// sees EventDone, the job transitions to StateCompleted and the
	// Subscribe callback drops the counter to 0.
	close(prov.blockCh)

	waitForEq(t, 5*time.Second, "topbar jobs == 0 after completion", func() int {
		return app.topbar.Jobs()
	}, 0)

	// A terminal-state system line should have been appended by
	// handleJobUpdate. We only assert on the [job:...] prefix because
	// the trailing duration / cost fields are non-deterministic.
	if !conversationContains(app, "[job:") {
		t.Fatalf("expected terminal-state system message in conversation")
	}

	// And draining results yields one entry for the slash-command
	// path's injection-on-next-turn contract.
	results := mgr.DrainResults(0)
	if len(results) != 1 {
		t.Fatalf("DrainResults = %d, want 1", len(results))
	}
	if results[0].State != jobs.StateCompleted {
		t.Fatalf("result state = %s, want completed", results[0].State)
	}
}

// conversationContains walks the conversation Model's rendered view
// for a substring. Using View() keeps the check public-facing.
func conversationContains(a *App, needle string) bool {
	a.conversation.Resize(120, 40)
	return strings.Contains(a.conversation.View(), needle)
}

// waitForEq polls fn up to timeout and fails the test if it doesn't
// return want in time. Mirrors internal/jobs/waitFor, minus the
// boolean-predicate shape.
func waitForEq(t *testing.T, timeout time.Duration, msg string, fn func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitForEq timed out after %s: %s (got %d, want %d)", timeout, msg, fn(), want)
}
