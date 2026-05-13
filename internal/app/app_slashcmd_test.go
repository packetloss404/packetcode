package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/agentview"
	"github.com/packetcode/packetcode/internal/ui/components/approval"
	"github.com/packetcode/packetcode/internal/ui/components/autocomplete"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
	"github.com/packetcode/packetcode/internal/ui/components/input"
	jobs_ui "github.com/packetcode/packetcode/internal/ui/components/jobs"
	"github.com/packetcode/packetcode/internal/ui/components/spinner"
	"github.com/packetcode/packetcode/internal/ui/components/topbar"
)

// ─── test fixtures ────────────────────────────────────────────────────

// fakeProvider is a minimal Provider implementation with scripted
// ListModels and ChatCompletion behaviour, used by the slash-command
// handler tests. Distinct from scriptedE2EProvider because we often
// need ListModels to fail or return specific rows.
type fakeProvider struct {
	slug          string
	name          string
	models        []provider.Model
	listErr       error
	listCalls     int32
	turns         [][]provider.StreamEvent
	turnIdx       int32
	pricing       func(string) (float64, float64)
	ctxWindow     int
	supportsTools bool
}

func (f *fakeProvider) Name() string                                  { return f.name }
func (f *fakeProvider) Slug() string                                  { return f.slug }
func (f *fakeProvider) BrandColor() lipgloss.Color                    { return lipgloss.Color("#000000") }
func (f *fakeProvider) ValidateKey(_ context.Context, _ string) error { return nil }
func (f *fakeProvider) ListModels(_ context.Context) ([]provider.Model, error) {
	atomic.AddInt32(&f.listCalls, 1)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.models, nil
}
func (f *fakeProvider) Pricing(m string) (float64, float64) {
	if f.pricing != nil {
		return f.pricing(m)
	}
	return 0, 0
}
func (f *fakeProvider) ContextWindow(_ string) int { return f.ctxWindow }
func (f *fakeProvider) SupportsTools(_ string) bool {
	return f.supportsTools
}
func (f *fakeProvider) ChatCompletion(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	idx := atomic.AddInt32(&f.turnIdx, 1) - 1
	if int(idx) >= len(f.turns) {
		return nil, errors.New("fakeProvider: no more turns scripted")
	}
	batch := f.turns[idx]
	ch := make(chan provider.StreamEvent, len(batch))
	for _, ev := range batch {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

type blockingCompactProvider struct {
	started chan struct{}
	release chan struct{}
	text    string
}

func newBlockingCompactProvider(text string) *blockingCompactProvider {
	return &blockingCompactProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		text:    text,
	}
}

func (b *blockingCompactProvider) Name() string                              { return "slow" }
func (b *blockingCompactProvider) Slug() string                              { return "slow" }
func (b *blockingCompactProvider) BrandColor() lipgloss.Color                { return lipgloss.Color("#000000") }
func (b *blockingCompactProvider) ValidateKey(context.Context, string) error { return nil }
func (b *blockingCompactProvider) ListModels(context.Context) ([]provider.Model, error) {
	return nil, nil
}
func (b *blockingCompactProvider) Pricing(string) (float64, float64) { return 0, 0 }
func (b *blockingCompactProvider) ContextWindow(string) int          { return 100_000 }
func (b *blockingCompactProvider) SupportsTools(string) bool         { return true }
func (b *blockingCompactProvider) ChatCompletion(ctx context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 2)
	close(b.started)
	go func() {
		defer close(ch)
		select {
		case <-b.release:
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: b.text}
			ch <- provider.StreamEvent{Type: provider.EventDone}
		case <-ctx.Done():
			ch <- provider.StreamEvent{Type: provider.EventError, Error: ctx.Err()}
		}
	}()
	return ch, nil
}

func wireCompactProvider(t *testing.T, r *testAppRig, prov provider.Provider) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.Register(prov)
	if err := reg.SetActive(prov.Slug(), "slow-model"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	r.reg = reg
	r.app.deps.Registry = reg
}

func runTeaCmdAsync(cmd tea.Cmd) <-chan tea.Msg {
	out := make(chan tea.Msg, 16)
	go runTeaCmdInto(cmd, out)
	return out
}

func runTeaCmdInto(cmd tea.Cmd, out chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			go runTeaCmdInto(sub, out)
		}
		return
	}
	if msg != nil {
		out <- msg
	}
}

func waitCompactDone(t *testing.T, msgs <-chan tea.Msg, timeout time.Duration) compactDoneMsg {
	t.Helper()
	timer := time.After(timeout)
	for {
		select {
		case msg := <-msgs:
			if done, ok := msg.(compactDoneMsg); ok {
				return done
			}
		case <-timer:
			t.Fatalf("timed out waiting for compactDoneMsg")
		}
	}
}

// testAppRig groups everything a handler test needs so individual tests
// only have to drill in the fields they care about.
type testAppRig struct {
	app      *App
	sessions *session.Manager
	reg      *provider.Registry
	tracker  *cost.Tracker
	backups  *session.BackupManager
	prov     *fakeProvider
	cfg      *config.Config
	tmp      string
}

// newTestApp builds a minimal App for slash-command handler tests.
// Temp dirs are created under t.TempDir, a lone fakeProvider is
// registered and set active, a fresh session is created, and the App's
// dependencies are wired end-to-end — but no jobs.Manager, since the
// Round 1 verbs don't need one.
func newTestApp(t *testing.T) *testAppRig {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	sessionsDir := filepath.Join(tmp, "sessions")
	backupsDir := filepath.Join(tmp, "backups")
	for _, d := range []string{sessionsDir, backupsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	sessions := session.NewManager(sessionsDir)
	if _, err := sessions.New("fake", "fake-model"); err != nil {
		t.Fatalf("session.New: %v", err)
	}

	prov := &fakeProvider{
		slug: "fake",
		name: "Fake Provider",
		models: []provider.Model{
			{ID: "fake-model", DisplayName: "Fake", ContextWindow: 100_000, SupportsTools: true, InputPer1M: 2.00, OutputPer1M: 8.00},
			{ID: "fake-mini", DisplayName: "Fake Mini", ContextWindow: 100_000, SupportsTools: true, InputPer1M: 0.40, OutputPer1M: 1.60},
		},
		ctxWindow:     100_000,
		supportsTools: true,
	}
	reg := provider.NewRegistry()
	reg.Register(prov)
	if err := reg.SetActive(prov.Slug(), "fake-model"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	tallyPath := filepath.Join(tmp, "tally.json")
	tracker, err := cost.NewTracker(tallyPath, func(_, _ string) (float64, float64) { return 0, 0 })
	if err != nil {
		t.Fatalf("cost.NewTracker: %v", err)
	}

	bk := session.NewBackupManager(backupsDir, sessions.Current().ID)
	toolReg := tools.NewRegistry()
	toolReg.Register(tools.NewWriteFileTool(tmp, bk))
	toolReg.Register(tools.NewPatchFileTool(tmp, bk))

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			// APIKey non-empty so provider-picker flows treat this as a
			// configured provider (not a key-entry target).
			"fake": {APIKey: "sk-test", DefaultModel: "fake-model"},
		},
		Behavior: config.BehaviorConfig{AutoCompactThreshold: 80},
	}

	app := &App{
		deps: Deps{
			Config:      cfg,
			Registry:    reg,
			Tools:       toolReg,
			Sessions:    sessions,
			CostTracker: tracker,
			Backups:     bk,
			WorkingDir:  tmp,
			Version:     "v-test",
		},
		topbar:        topbar.New(),
		conversation:  conversation.New(),
		input:         input.New(),
		approval:      approval.New(),
		jobsPanel:     jobs_ui.New(),
		agentView:     agentview.New(),
		spinner:       spinner.New(),
		autocomplete:  autocomplete.New(buildAutocompleteEntries()),
		slashCommands: NewBuiltinSlashRegistry(),
		approver:      newUIApprover(),
		backups:       bk,
		contextMgr:    agent.NewContextManager(80),
	}

	return &testAppRig{
		app:      app,
		sessions: sessions,
		reg:      reg,
		tracker:  tracker,
		backups:  bk,
		prov:     prov,
		cfg:      cfg,
		tmp:      tmp,
	}
}

func wireJobsManagerForSlashTest(t *testing.T, r *testAppRig) *jobs.Manager {
	t.Helper()
	jobsDir := filepath.Join(r.tmp, "jobs")
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	mgr, _, err := jobs.NewManager(jobs.Config{
		Registry:      r.reg,
		Tools:         r.app.deps.Tools,
		MainSessions:  r.sessions,
		SessionsDir:   filepath.Join(r.tmp, "sessions"),
		BackupsDir:    filepath.Join(r.tmp, "backups"),
		JobsDir:       jobsDir,
		CostTracker:   r.tracker,
		MaxConcurrent: 1,
		MaxTotal:      4,
		Root:          r.tmp,
		Approver:      agent.AutoApprove(),
	})
	if err != nil {
		t.Fatalf("jobs.NewManager: %v", err)
	}
	r.app.jobs = mgr
	r.app.deps.Jobs = mgr
	return mgr
}

// convText renders the current conversation to a string so tests can
// assert against its contents.
func convText(a *App) string {
	a.conversation.Resize(200, 80)
	return a.conversation.View()
}

func convContains(t *testing.T, a *App, needle string) {
	t.Helper()
	if !strings.Contains(convText(a), needle) {
		t.Fatalf("conversation does not contain %q; got:\n%s", needle, convText(a))
	}
}

// ─── /provider ────────────────────────────────────────────────────────

// TestApp_Provider_ListOpensPicker verifies bare /provider opens the
// centred picker modal (same code path as Ctrl+P). The table output
// from the old handler is replaced by the picker's View.
func TestApp_Provider_ListOpensPicker(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("provider", nil, "/provider")
	if !r.app.picker.Visible() {
		t.Fatalf("/provider should open the picker; picker is not visible")
	}
	if r.app.picker.ID() != "provider" {
		t.Fatalf("wrong picker opened: id=%q", r.app.picker.ID())
	}
}

func TestApp_ProviderAdd_OpensPicker(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("provider", []string{"add"}, "/provider add")
	if !r.app.picker.Visible() || r.app.picker.ID() != "provider" {
		t.Fatalf("/provider add should open provider picker, visible=%v id=%q", r.app.picker.Visible(), r.app.picker.ID())
	}
	convContains(t, r.app, "provider add: choose a provider")
}

func TestApp_ProviderAddSlug_OpensKeyPrompt(t *testing.T) {
	r := newTestApp(t)
	r.app.deps.Factories = FactoryMap{
		"second": func(_ string) provider.Provider {
			return &fakeProvider{slug: "second", name: "Second"}
		},
	}

	r.app.handleSlashCommand("provider", []string{"add", "second"}, "/provider add second")
	if !r.app.prompt.Visible() {
		t.Fatalf("/provider add second should open key prompt")
	}
	if got := r.app.prompt.Context(); got != "second" {
		t.Fatalf("prompt context = %q, want second", got)
	}
}

func TestApp_Provider_SwitchWithDefaultModel(t *testing.T) {
	r := newTestApp(t)
	// Register a second provider with a config default model.
	second := &fakeProvider{slug: "second", name: "Second", models: []provider.Model{{ID: "m1"}}}
	r.reg.Register(second)
	r.cfg.Providers["second"] = config.ProviderConfig{DefaultModel: "m1"}

	r.app.handleSlashCommand("provider", []string{"second"}, "/provider second")
	convContains(t, r.app, "switched provider: second (m1)")
	if p, m := r.reg.Active(); p == nil || p.Slug() != "second" || m != "m1" {
		t.Fatalf("active = %v / %q, want second / m1", p, m)
	}
}

func TestApp_Provider_FallbackToListModels(t *testing.T) {
	r := newTestApp(t)
	// Register a provider with NO default model in config; ListModels
	// supplies the fallback.
	second := &fakeProvider{slug: "second", name: "Second", models: []provider.Model{{ID: "auto-1"}}}
	r.reg.Register(second)

	r.app.handleSlashCommand("provider", []string{"second"}, "/provider second")
	convContains(t, r.app, "switched provider: second (auto-1)")
}

func TestApp_Provider_UnknownSlug(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("provider", []string{"nope"}, "/provider nope")
	convContains(t, r.app, `provider: unknown provider "nope"`)
}

func TestApp_Provider_NoModelFallback(t *testing.T) {
	r := newTestApp(t)
	// Register a provider with no default model and an empty ListModels.
	second := &fakeProvider{slug: "second", name: "Second", models: nil}
	r.reg.Register(second)

	r.app.handleSlashCommand("provider", []string{"second"}, "/provider second")
	convContains(t, r.app, "provider: second has no default model")
}

// ─── /model ────────────────────────────────────────────────────────────

// TestApp_Model_List verifies bare /model opens the centred picker
// modal rather than printing a table into the conversation. Loader
// error handling is covered by TestApp_CtrlM_ListError, which shares
// the same openModelPicker code path.
func TestApp_Model_List(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("model", nil, "/model")
	if !r.app.picker.Visible() {
		t.Fatalf("/model should open the picker; picker is not visible")
	}
}

func TestApp_Model_Switch(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("model", []string{"fake-mini"}, "/model fake-mini")
	convContains(t, r.app, "switched model: fake/fake-mini")
	if _, m := r.reg.Active(); m != "fake-mini" {
		t.Fatalf("active model = %q, want fake-mini", m)
	}
}

// TestApp_Model_Switch_RejectsUnknownID ensures validateModelID blocks
// phantom IDs that would otherwise fail silently on every turn. Also
// confirms near-match suggestions surface when the typo is a substring
// of a real model.
func TestApp_Model_Switch_RejectsUnknownID(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("model", []string{"fake-mega"}, "/model fake-mega")
	convContains(t, r.app, `unknown model "fake-mega"`)
	// Active model must not have changed.
	if _, m := r.reg.Active(); m != "fake-model" {
		t.Fatalf("active model = %q, expected unchanged (fake-model)", m)
	}
}

// TestApp_Model_Switch_SuggestsNearMatches confirms the substring-match
// hint fires when the user types a prefix/suffix of a real model ID.
func TestApp_Model_Switch_SuggestsNearMatches(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("model", []string{"mini"}, "/model mini")
	convContains(t, r.app, "did you mean")
	convContains(t, r.app, "fake-mini")
}

// TestApp_Model_Switch_SkipsValidationOnListError — if the catalog is
// unreachable, we trust the user rather than locking them out of model
// changes on a transient network blip.
func TestApp_Model_Switch_SkipsValidationOnListError(t *testing.T) {
	r := newTestApp(t)
	// Drop any cache so validation would call ListModels, then force it
	// to error.
	r.reg.InvalidateCachedModels("fake")
	r.prov.listErr = errors.New("boom")
	r.app.handleSlashCommand("model", []string{"gpt-definitely-fake"}, "/model gpt-definitely-fake")
	convContains(t, r.app, "switched model: fake/gpt-definitely-fake")
}

// ─── /sessions ─────────────────────────────────────────────────────────

func TestApp_Sessions_List(t *testing.T) {
	r := newTestApp(t)
	// Create a second session so the list has >1 row.
	cur := r.sessions.Current()
	curID := cur.ID
	if _, err := r.sessions.New("fake", "fake-model"); err != nil {
		t.Fatalf("session.New: %v", err)
	}
	// Switch back to the first so the "active" marker lands on a known row.
	if _, err := r.sessions.Load(curID); err != nil {
		t.Fatalf("Load: %v", err)
	}

	r.app.handleSlashCommand("sessions", nil, "/sessions")
	convContains(t, r.app, "ID")
	convContains(t, r.app, curID[:8])
}

func TestApp_Sessions_ResumeByFullID(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	fullID := cur.ID

	// Start a new session, then resume the old one.
	if _, err := r.sessions.New("fake", "fake-model"); err != nil {
		t.Fatalf("session.New: %v", err)
	}
	r.app.handleSlashCommand("sessions", []string{"resume", fullID}, "/sessions resume "+fullID)
	convContains(t, r.app, "resumed session")
	if got := r.sessions.Current().ID; got != fullID {
		t.Fatalf("current = %s, want %s", got, fullID)
	}
}

func TestApp_Sessions_ResumeByPrefix(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	fullID := cur.ID
	if _, err := r.sessions.New("fake", "fake-model"); err != nil {
		t.Fatalf("session.New: %v", err)
	}

	prefix := fullID[:8]
	r.app.handleSlashCommand("sessions", []string{"resume", prefix}, "/sessions resume "+prefix)
	convContains(t, r.app, "resumed session")
	if got := r.sessions.Current().ID; got != fullID {
		t.Fatalf("current = %s, want %s", got, fullID)
	}
}

func TestApp_Sessions_ResumeRestoresAppState(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{
		slug:          "second",
		name:          "Second",
		models:        []provider.Model{{ID: "second-model", ContextWindow: 42_000, SupportsTools: true}},
		ctxWindow:     42_000,
		supportsTools: true,
	}
	r.reg.Register(second)

	resumed, err := r.sessions.New("second", "second-model")
	if err != nil {
		t.Fatalf("session.New resumed: %v", err)
	}
	if err := r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "hello from resumed session"}); err != nil {
		t.Fatalf("AddMessage user: %v", err)
	}
	if err := r.sessions.AddMessage(provider.Message{Role: provider.RoleAssistant, Content: "assistant remembers this"}); err != nil {
		t.Fatalf("AddMessage assistant: %v", err)
	}

	current, err := r.sessions.New("fake", "fake-model")
	if err != nil {
		t.Fatalf("session.New current: %v", err)
	}
	if err := r.backups.SwitchSession(current.ID); err != nil {
		t.Fatalf("SwitchSession current: %v", err)
	}
	oldTarget := filepath.Join(r.tmp, "old.txt")
	if err := os.WriteFile(oldTarget, []byte("before"), 0o600); err != nil {
		t.Fatalf("write old target: %v", err)
	}
	if err := r.backups.Backup(oldTarget); err != nil {
		t.Fatalf("backup old target: %v", err)
	}
	if err := os.WriteFile(oldTarget, []byte("after"), 0o600); err != nil {
		t.Fatalf("modify old target: %v", err)
	}

	r.app.handleSlashCommand("sessions", []string{"resume", resumed.ID}, "/sessions resume "+resumed.ID)

	convContains(t, r.app, "resumed session")
	convContains(t, r.app, "hello from resumed session")
	convContains(t, r.app, "assistant remembers this")
	if got := r.sessions.Current().ID; got != resumed.ID {
		t.Fatalf("current = %s, want %s", got, resumed.ID)
	}
	if p, m := r.reg.Active(); p == nil || p.Slug() != "second" || m != "second-model" {
		t.Fatalf("active = %v / %q, want second / second-model", p, m)
	}
	snap := r.app.statusLineSnapshot()
	if snap.SessionID != resumed.ID || snap.Provider.Slug != "second" || snap.Model.ID != "second-model" {
		t.Fatalf("status snapshot = session %q provider %q model %q", snap.SessionID, snap.Provider.Slug, snap.Model.ID)
	}
	if depth := r.backups.Depth(); depth != 0 {
		t.Fatalf("backup stack depth after resume = %d, want 0", depth)
	}

	writeTool, ok := r.app.deps.Tools.Get("write_file")
	if !ok {
		t.Fatalf("write_file tool missing")
	}
	newTarget := filepath.Join(r.tmp, "new.txt")
	if err := os.WriteFile(newTarget, []byte("old content"), 0o600); err != nil {
		t.Fatalf("write new target: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"path": "new.txt", "content": "new content"})
	if res, err := writeTool.Execute(context.Background(), raw); err != nil || res.IsError {
		t.Fatalf("write_file Execute = res=%+v err=%v", res, err)
	}
	if entries, err := os.ReadDir(filepath.Join(r.tmp, "backups", resumed.ID)); err != nil || len(entries) == 0 {
		t.Fatalf("expected backup under resumed session: entries=%d err=%v", len(entries), err)
	}
	r.app.handleSlashCommand("undo", nil, "/undo")
	got, err := os.ReadFile(newTarget)
	if err != nil {
		t.Fatalf("read new target: %v", err)
	}
	if string(got) != "old content" {
		t.Fatalf("undo restored %q, want old content", got)
	}
}

func TestApp_Sessions_ResumeAmbiguous(t *testing.T) {
	r := newTestApp(t)
	// Manually write two session files with IDs sharing a prefix.
	dir := filepath.Join(r.tmp, "sessions")
	// Make sure we flush the current session first.
	if err := r.sessions.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	idA := "aaaaaaaaaaaa-aaa-aaa-aaa-aaaaaaaaaaaa"
	idB := "aaaaaaaaaaaa-bbb-bbb-bbb-bbbbbbbbbbbb"
	for _, id := range []string{idA, idB} {
		path := filepath.Join(dir, id+".json")
		if err := os.WriteFile(path, []byte(`{"id":"`+id+`","name":"x","messages":[]}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	r.app.handleSlashCommand("sessions", []string{"resume", "aaaaaaaa"}, "/sessions resume aaaaaaaa")
	convContains(t, r.app, "ambiguous prefix")
}

func TestApp_Sessions_ResumeRejectsShortPrefix(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("sessions", []string{"resume", "aaaa"}, "/sessions resume aaaa")
	convContains(t, r.app, "must be exactly 8 characters")
}

func TestApp_Sessions_ResumeUnknown(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("sessions", []string{"resume", "deadbeef"}, "/sessions resume deadbeef")
	convContains(t, r.app, `no session matches "deadbeef"`)
}

func TestApp_Sessions_DeleteWithoutYes(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	r.app.handleSlashCommand("sessions", []string{"delete", cur.ID}, "/sessions delete "+cur.ID)
	convContains(t, r.app, "refusing to delete without --yes")
	// Session still exists on disk.
	if _, err := os.Stat(filepath.Join(r.tmp, "sessions", cur.ID+".json")); err != nil {
		t.Fatalf("session should still exist: %v", err)
	}
}

func TestApp_Sessions_DeleteWithYes(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	path := filepath.Join(r.tmp, "sessions", cur.ID+".json")
	target := filepath.Join(r.tmp, "active-delete.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := r.backups.Backup(target); err != nil {
		t.Fatalf("backup: %v", err)
	}

	r.app.handleSlashCommand("sessions", []string{"delete", cur.ID, "--yes"}, "/sessions delete "+cur.ID+" --yes")
	convContains(t, r.app, "deleted session "+cur.ID[:8])
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session file should be gone: err=%v", err)
	}
	if got := r.sessions.Current(); got == nil || got.ID == cur.ID {
		t.Fatalf("active session after delete = %+v, want replacement", got)
	}
	if _, err := os.Stat(filepath.Join(r.tmp, "backups", cur.ID)); !os.IsNotExist(err) {
		t.Fatalf("backup directory should be gone: err=%v", err)
	}
}

func TestApp_Sessions_DeleteInactiveCleansBackups(t *testing.T) {
	r := newTestApp(t)
	inactive := r.sessions.Current()
	target := filepath.Join(r.tmp, "inactive-delete.txt")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := r.backups.Backup(target); err != nil {
		t.Fatalf("backup: %v", err)
	}
	active, err := r.sessions.New("fake", "fake-model")
	if err != nil {
		t.Fatalf("session.New active: %v", err)
	}
	if err := r.backups.SwitchSession(active.ID); err != nil {
		t.Fatalf("SwitchSession active: %v", err)
	}

	r.app.handleSlashCommand("sessions", []string{"delete", inactive.ID, "--yes"}, "/sessions delete "+inactive.ID+" --yes")
	convContains(t, r.app, "deleted session "+inactive.ID[:8])
	if got := r.sessions.Current(); got == nil || got.ID != active.ID {
		t.Fatalf("current = %+v, want %s", got, active.ID)
	}
	if _, err := os.Stat(filepath.Join(r.tmp, "sessions", inactive.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("session file should be gone: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(r.tmp, "backups", inactive.ID)); !os.IsNotExist(err) {
		t.Fatalf("backup directory should be gone: err=%v", err)
	}
}

func TestApp_Sessions_DeleteActiveWhileStreaming(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	r.app.streaming = true

	r.app.handleSlashCommand("sessions", []string{"delete", cur.ID, "--yes"}, "/sessions delete "+cur.ID+" --yes")
	convContains(t, r.app, "turn already running")
	if got := r.sessions.Current(); got == nil || got.ID != cur.ID {
		t.Fatalf("current = %+v, want %s", got, cur.ID)
	}
	if _, err := os.Stat(filepath.Join(r.tmp, "sessions", cur.ID+".json")); err != nil {
		t.Fatalf("session should still exist: %v", err)
	}
}

// ─── /undo ─────────────────────────────────────────────────────────────

func TestApp_Undo_EmptyStack(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("undo", nil, "/undo")
	convContains(t, r.app, "nothing to undo")
}

func TestApp_Undo_RestoreAndDepth(t *testing.T) {
	r := newTestApp(t)
	// Create a file, back it up, overwrite it, then /undo.
	target := filepath.Join(r.tmp, "sample.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.backups.Backup(target); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := os.WriteFile(target, []byte("modified"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	r.app.handleSlashCommand("undo", nil, "/undo")
	convContains(t, r.app, "restored ")
	convContains(t, r.app, "depth now: 0")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("restored content = %q, want original", data)
	}
}

func TestApp_Undo_NoBackupManager(t *testing.T) {
	r := newTestApp(t)
	r.app.backups = nil
	r.app.handleSlashCommand("undo", nil, "/undo")
	convContains(t, r.app, "undo: backups not available")
}

// ─── /compact ──────────────────────────────────────────────────────────

func TestApp_Compact_KeepInvalid(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("compact", []string{"--keep", "abc"}, "/compact --keep abc")
	convContains(t, r.app, "compact: --keep must be a positive integer")
}

func TestApp_Compact_NoSession(t *testing.T) {
	r := newTestApp(t)
	// Force "no session" by deleting the current session from the manager.
	cur := r.sessions.Current()
	_ = r.sessions.Delete(cur.ID)
	r.app.handleSlashCommand("compact", nil, "/compact")
	convContains(t, r.app, "compact: no session loaded")
}

func TestApp_Compact_NoProvider(t *testing.T) {
	r := newTestApp(t)
	// Unregister by pointing to a fresh empty registry.
	r.app.deps.Registry = provider.NewRegistry()
	r.app.handleSlashCommand("compact", nil, "/compact")
	convContains(t, r.app, "compact: no active provider")
}

func TestApp_Compact_Succeeds(t *testing.T) {
	r := newTestApp(t)
	// Seed a handful of messages so compaction has something to do.
	for i := 0; i < 20; i++ {
		_ = r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "msg"})
	}
	// Script a single-shot Done+summary stream for the Compact round-trip.
	r.prov.turns = [][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, TextDelta: "summary text"},
		{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 5, OutputTokens: 2}},
	}}

	_, cmd := r.app.handleSlashCommand("compact", []string{"--keep", "3"}, "/compact --keep 3")
	pump := newDrainPump(t, r.app, cmd)
	pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming })
	if r.app.streaming {
		t.Fatalf("expected compact to finish")
	}

	txt := convText(r.app)
	if !strings.Contains(txt, "compacting context...") {
		t.Fatalf("pre message missing:\n%s", txt)
	}
	if !strings.Contains(txt, "compacted:") || !strings.Contains(txt, "kept 3 recent messages") {
		t.Fatalf("post message missing:\n%s", txt)
	}

	// Current session should now hold fewer messages (summary + 3 tail).
	cur := r.sessions.Current()
	if len(cur.Messages) >= 20 {
		t.Fatalf("messages not compacted: len = %d", len(cur.Messages))
	}

	// Save() should have persisted the new messages.
	// Re-load the session file from disk to confirm.
	if err := r.sessions.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := r.sessions.Load(cur.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded.Messages) != len(cur.Messages) {
		t.Fatalf("reloaded count = %d, want %d", len(reloaded.Messages), len(cur.Messages))
	}
}

func TestApp_Compact_StartsAsyncAndShowsProgress(t *testing.T) {
	r := newTestApp(t)
	for i := 0; i < 20; i++ {
		_ = r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "msg"})
	}
	prov := newBlockingCompactProvider("summary text")
	wireCompactProvider(t, r, prov)

	_, cmd := r.app.handleSlashCommand("compact", []string{"--keep", "3"}, "/compact --keep 3")
	if cmd == nil {
		t.Fatalf("expected compact command")
	}
	if !r.app.streaming {
		t.Fatalf("expected streaming=true while compact is running")
	}
	if r.app.cancelTurn == nil {
		t.Fatalf("expected cancelTurn while compact is running")
	}
	if !r.app.spinner.Active() {
		t.Fatalf("expected spinner to be active while compact is running")
	}
	txt := convText(r.app)
	if !strings.Contains(txt, "compacting context...") {
		t.Fatalf("start message missing:\n%s", txt)
	}
	if strings.Contains(txt, "compacted:") {
		t.Fatalf("done message rendered before compact finished:\n%s", txt)
	}

	msgs := runTeaCmdAsync(cmd)
	select {
	case <-prov.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("compact provider was not started")
	}
	select {
	case msg := <-msgs:
		if _, ok := msg.(compactDoneMsg); ok {
			t.Fatalf("compact finished before provider was released")
		}
	case <-time.After(50 * time.Millisecond):
	}

	close(prov.release)
	done := waitCompactDone(t, msgs, time.Second)
	_, _ = r.app.Update(done)

	if r.app.streaming {
		t.Fatalf("expected streaming=false after compact done")
	}
	if r.app.spinner.Active() {
		t.Fatalf("expected spinner to stop after compact done")
	}
	convContains(t, r.app, "compacted:")
}

func TestApp_Compact_CtrlCCancels(t *testing.T) {
	r := newTestApp(t)
	for i := 0; i < 20; i++ {
		_ = r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "msg"})
	}
	prov := newBlockingCompactProvider("summary text")
	wireCompactProvider(t, r, prov)

	_, cmd := r.app.handleSlashCommand("compact", nil, "/compact")
	msgs := runTeaCmdAsync(cmd)
	select {
	case <-prov.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("compact provider was not started")
	}

	_, _ = r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if r.app.cancelTurn != nil {
		t.Fatalf("expected cancelTurn to be nil after Ctrl+C")
	}
	if !r.app.streaming {
		t.Fatalf("streaming should remain true until compactDoneMsg drains")
	}

	done := waitCompactDone(t, msgs, time.Second)
	_, _ = r.app.Update(done)
	if r.app.streaming {
		t.Fatalf("expected streaming=false after cancelled compact drains")
	}
	txt := convText(r.app)
	if !strings.Contains(txt, "compact cancelled") {
		t.Fatalf("conversation missing compact cancellation line:\n%s", txt)
	}
	if strings.Contains(strings.ToLower(txt), "context canceled") {
		t.Fatalf("conversation leaked raw ctx cancellation:\n%s", txt)
	}
}

func TestApp_Compact_ErrorStopsProgress(t *testing.T) {
	r := newTestApp(t)
	for i := 0; i < 20; i++ {
		_ = r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "msg"})
	}
	r.prov.turns = [][]provider.StreamEvent{{
		{Type: provider.EventError, Error: errors.New("summary failed")},
	}}

	_, cmd := r.app.handleSlashCommand("compact", nil, "/compact")
	pump := newDrainPump(t, r.app, cmd)
	pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming })
	if r.app.streaming {
		t.Fatalf("expected streaming=false after compact error")
	}
	if r.app.spinner.Active() {
		t.Fatalf("expected spinner to stop after compact error")
	}
	txt := convText(r.app)
	if !strings.Contains(txt, "compacting context...") || !strings.Contains(txt, "summary failed") {
		t.Fatalf("conversation missing compact start/error messages:\n%s", txt)
	}
}

// ─── /cost ─────────────────────────────────────────────────────────────

func TestApp_Cost_EmptyBreakdown(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("cost", nil, "/cost")
	convContains(t, r.app, "no usage recorded yet")
}

func TestApp_Cost_BreakdownWithFooter(t *testing.T) {
	r := newTestApp(t)
	// Install a pricing function so non-zero USD lands on each row.
	r.tracker, _ = cost.NewTracker(
		filepath.Join(r.tmp, "tally-priced.json"),
		func(_, _ string) (float64, float64) { return 1.0, 2.0 },
	)
	r.app.deps.CostTracker = r.tracker

	// 7 sessions so the top-5 footer appears.
	for i := 0; i < 7; i++ {
		sid := "sess-" + string(rune('a'+i))
		if err := r.tracker.RecordUsage(sid, "fake", "fake-model", 1000*(i+1), 500*(i+1)); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}
	r.app.handleSlashCommand("cost", nil, "/cost")
	txt := convText(r.app)
	if !strings.Contains(txt, "Total: $") {
		t.Fatalf("missing total line:\n%s", txt)
	}
	if !strings.Contains(txt, "SESSION") {
		t.Fatalf("missing header row:\n%s", txt)
	}
	if !strings.Contains(txt, "[showing top 5 of 7 sessions]") {
		t.Fatalf("missing footer:\n%s", txt)
	}
}

func TestApp_Cost_ResetWithoutYes(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("cost", []string{"reset"}, "/cost reset")
	convContains(t, r.app, "refusing to reset without --yes")
}

func TestApp_Cost_ResetWithYes(t *testing.T) {
	r := newTestApp(t)
	if err := r.tracker.RecordUsage("sess-a", "fake", "fake-model", 100, 50); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	r.app.handleSlashCommand("cost", []string{"reset", "--yes"}, "/cost reset --yes")
	convContains(t, r.app, "cost tally cleared")
	if e := r.tracker.Breakdown(); len(e) != 0 {
		t.Fatalf("tally not cleared: %v", e)
	}
}

// ─── /trust ────────────────────────────────────────────────────────────

func TestApp_Trust_Query(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("trust", nil, "/trust")
	convContains(t, r.app, "trust mode: off")
}

func TestApp_Trust_On(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("trust", []string{"on"}, "/trust on")
	convContains(t, r.app, "trust mode enabled")
	if !r.app.approver.IsTrusted() {
		t.Fatalf("approver should be trusted")
	}
}

func TestApp_Trust_Off(t *testing.T) {
	r := newTestApp(t)
	r.app.approver.SetTrust(true)
	r.app.handleSlashCommand("trust", []string{"off"}, "/trust off")
	convContains(t, r.app, "trust mode disabled")
	if r.app.approver.IsTrusted() {
		t.Fatalf("approver should NOT be trusted")
	}
}

func TestApp_Trust_UnknownValue(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("trust", []string{"maybe"}, "/trust maybe")
	convContains(t, r.app, `trust: unknown value "maybe"`)
}

// ─── /help ─────────────────────────────────────────────────────────────

func TestApp_Help_ContainsAllSections(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("help", nil, "/help")
	for _, section := range []string{"Global", "Conversation", "Approval", "Input", "Slash commands"} {
		convContains(t, r.app, section)
	}
	convContains(t, r.app, "Ctrl+A")
	convContains(t, r.app, "Set/update provider API key")
	// Lists itself and all nine new verbs.
	for _, verb := range []string{"/help", "/clear", "/agents", "/provider", "/model", "/sessions", "/undo", "/compact", "/cost", "/trust"} {
		convContains(t, r.app, verb)
	}
}

// ─── /clear ────────────────────────────────────────────────────────────

func TestApp_Clear_EquivalentToCtrlL(t *testing.T) {
	r := newTestApp(t)
	// Drop some content into the pane.
	r.app.conversation.AppendUser("hello")
	r.app.conversation.AppendSystem("something")
	convContains(t, r.app, "hello")

	// /clear wipes it.
	r.app.handleSlashCommand("clear", nil, "/clear")
	if strings.Contains(convText(r.app), "hello") {
		t.Fatalf("conversation not cleared:\n%s", convText(r.app))
	}
	if !r.app.conversation.IsEmpty() {
		t.Fatalf("conversation should be empty post-clear")
	}
}

// ─── /agents ──────────────────────────────────────────────────────────

func TestApp_Agents_ListUsesBackgroundJobs(t *testing.T) {
	r := newTestApp(t)
	mgr := wireJobsManagerForSlashTest(t, r)
	defer mgr.Shutdown(2 * time.Second)

	snap, spawnErr := mgr.Spawn(jobs.SpawnRequest{
		Prompt:   "audit fixtures",
		Provider: "fake",
		Model:    "fake-model",
	})
	if spawnErr != nil {
		t.Fatalf("Spawn: %v", spawnErr)
	}

	r.app.handleSlashCommand("agents", nil, "/agents")
	if !r.app.agentView.Visible() {
		t.Fatalf("/agents should open the agent view")
	}
	out := r.app.agentView.View()
	if !strings.Contains(out, snap.ID) {
		t.Fatalf("agent view missing spawned job:\n%s", out)
	}
}

func TestApp_Agents_DetailOpensJobsPanel(t *testing.T) {
	r := newTestApp(t)
	mgr := wireJobsManagerForSlashTest(t, r)
	defer mgr.Shutdown(2 * time.Second)

	snap, spawnErr := mgr.Spawn(jobs.SpawnRequest{
		Prompt:   "inspect flaky test",
		Provider: "fake",
		Model:    "fake-model",
	})
	if spawnErr != nil {
		t.Fatalf("Spawn: %v", spawnErr)
	}

	r.app.handleSlashCommand("agents", []string{snap.ID}, "/agents "+snap.ID)
	if !r.app.jobsPanel.Visible() {
		t.Fatalf("/agents <id> should open the existing jobs transcript panel")
	}
}

func TestApp_Agents_NotFoundUsesAgentLabel(t *testing.T) {
	r := newTestApp(t)
	mgr := wireJobsManagerForSlashTest(t, r)
	defer mgr.Shutdown(2 * time.Second)

	r.app.handleSlashCommand("agents", []string{"missing"}, "/agents missing")
	convContains(t, r.app, "[agent:missing not found]")
}

func TestApp_Agents_ViewDoesNotOverrideJobsPanelOverlay(t *testing.T) {
	r := newTestApp(t)
	mgr := wireJobsManagerForSlashTest(t, r)
	defer mgr.Shutdown(2 * time.Second)

	snap, spawnErr := mgr.Spawn(jobs.SpawnRequest{
		Prompt:   "inspect overlay",
		Provider: "fake",
		Model:    "fake-model",
	})
	if spawnErr != nil {
		t.Fatalf("Spawn: %v", spawnErr)
	}

	r.app.resize(120, 40)
	r.app.handleSlashCommand("agents", nil, "/agents")
	r.app.handleSlashCommand("jobs", []string{snap.ID}, "/jobs "+snap.ID)
	out := r.app.View()
	if !strings.Contains(out, "[job:"+snap.ID+"]") {
		t.Fatalf("jobs panel should remain above agent view overlay:\n%s", out)
	}
}

func TestApp_HandleJobUpdate_IgnoresStaleSeqAndDedupesTerminal(t *testing.T) {
	r := newTestApp(t)
	mgr := wireJobsManagerForSlashTest(t, r)
	defer mgr.Shutdown(2 * time.Second)

	snap, spawnErr := mgr.Spawn(jobs.SpawnRequest{
		Prompt:   "finish once",
		Provider: "fake",
		Model:    "fake-model",
	})
	if spawnErr != nil {
		t.Fatalf("Spawn: %v", spawnErr)
	}
	waitForEq(t, 2*time.Second, "job terminal", func() int {
		got, _ := mgr.Get(snap.ID)
		if got.State.IsTerminal() {
			return 1
		}
		return 0
	}, 1)
	terminal, _ := mgr.Get(snap.ID)

	r.app.handleJobUpdate(terminal)
	r.app.handleJobUpdate(terminal)
	if got := strings.Count(convText(r.app), "[job:"+terminal.ID); got != 1 {
		t.Fatalf("terminal line count = %d, want 1\n%s", got, convText(r.app))
	}

	stale := terminal
	stale.Seq--
	stale.Summary = "stale summary should be ignored"
	r.app.handleJobUpdate(stale)
	if strings.Contains(convText(r.app), stale.Summary) {
		t.Fatalf("stale update rendered:\n%s", convText(r.app))
	}
}

// ─── dispatch sanity ───────────────────────────────────────────────────

func TestApp_Dispatch_NonJobsVerbsWorkWithoutJobsManager(t *testing.T) {
	r := newTestApp(t)
	// Explicitly confirm a.jobs is nil — the rig does not wire one.
	if r.app.jobs != nil {
		t.Fatalf("expected no jobs manager in test rig")
	}
	// Every non-jobs verb should either succeed or emit its own
	// command-specific error; none should crash or print the
	// "background jobs are disabled" guard.
	verbs := [][]string{
		{"provider"},
		{"model"},
		{"sessions"},
		{"undo"},
		{"cost"},
		{"trust"},
		{"help"},
		{"clear"},
	}
	for _, v := range verbs {
		r.app.handleSlashCommand(v[0], nil, "/"+v[0])
		if strings.Contains(convText(r.app), "background jobs are disabled") {
			t.Fatalf("verb %s incorrectly triggered the jobs guard:\n%s", v[0], convText(r.app))
		}
	}
}

func TestApp_Dispatch_JobsVerbsGuardOnMissingManager(t *testing.T) {
	r := newTestApp(t)
	for _, v := range []string{"spawn", "agents", "jobs", "cancel"} {
		r.app.handleSlashCommand(v, []string{"hello"}, "/"+v+" hello")
		convContains(t, r.app, "background")
	}
}

func TestApp_SubmitUnknownSlashCommandShowsEscapeHatch(t *testing.T) {
	r := newTestApp(t)
	_, _ = r.app.updateInner(input.SubmitMsg{Text: "/frobnicate now"})
	convContains(t, r.app, "unknown slash command /frobnicate")
	convContains(t, r.app, "//frobnicate")
}
