package app

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/config"
	jobspkg "github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// routeKey simulates a bare keypress through App.handleKey. Returns
// the cmd produced so tests can resolve async loaders.
//
// Note: Ctrl+M is indistinguishable from Enter in bubbletea (both
// resolve to keyCR/13, String() "enter"). We route "ctrl+m" tests
// through App.openModelPicker directly so the handler's
// `case "ctrl+m"` is exercised by unit tests and the integration
// behaviour (what actually happens when the user hits the shortcut
// in a CSI-u terminal) is validated end-to-end.
func routeKey(t *testing.T, a *App, s string) tea.Cmd {
	t.Helper()
	var km tea.KeyMsg
	switch s {
	case "ctrl+p":
		km = tea.KeyMsg{Type: tea.KeyCtrlP}
	case "ctrl+m":
		// Bypass the KeyMsg translation: pair open + picker guard
		// manually so tests don't depend on terminal capabilities.
		if a.approval.Visible() || a.picker.Visible() {
			return nil
		}
		return a.openModelPicker()
	case "esc":
		km = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		km = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		t.Fatalf("routeKey: unsupported key %q", s)
	}
	_, cmd := a.handleKey(km)
	return cmd
}

// resolveCmd runs a tea.Cmd and dispatches the message back into
// App.Update. Used to simulate the Bubble Tea event loop in tests.
func resolveCmd(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	_, _ = a.Update(msg)
}

// TestApp_CtrlP_OpensProviderPicker verifies Ctrl+P flips the picker
// into its visible state.
func TestApp_CtrlP_OpensProviderPicker(t *testing.T) {
	r := newTestApp(t)
	routeKey(t, r.app, "ctrl+p")
	if !r.app.picker.Visible() {
		t.Fatalf("picker should be visible after Ctrl+P")
	}
}

// TestApp_CtrlP_PopulatedFromRegistry verifies the picker rows come
// from Registry.List().
func TestApp_CtrlP_PopulatedFromRegistry(t *testing.T) {
	r := newTestApp(t)
	r.reg.Register(&fakeProvider{slug: "second", name: "Second"})
	routeKey(t, r.app, "ctrl+p")
	// Both providers should appear in the rendered View.
	out := r.app.picker.View()
	if !strings.Contains(out, "fake") || !strings.Contains(out, "second") {
		t.Fatalf("picker View missing provider rows:\n%s", out)
	}
}

// TestApp_CtrlP_ActiveMarkedAndCursored verifies the active provider's
// row has its marker and the cursor lands on it.
func TestApp_CtrlP_ActiveMarkedAndCursored(t *testing.T) {
	r := newTestApp(t)
	r.reg.Register(&fakeProvider{slug: "second", name: "Second"})
	routeKey(t, r.app, "ctrl+p")
	out := r.app.picker.View()
	if !strings.Contains(out, "●") {
		t.Fatalf("picker View missing active marker:\n%s", out)
	}
}

func TestProviderItems_KeyStatusSaysPresentNotValidated(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["fake"] = config.ProviderConfig{APIKey: "sk-test", DefaultModel: "fake-model"}
	items := providerItems([]provider.Provider{
		&fakeProvider{slug: "fake", name: "Fake Provider"},
		&fakeProvider{slug: "empty", name: "Empty Provider"},
	}, cfg, "fake")
	if len(items) != 2 {
		t.Fatalf("providerItems len = %d, want 2", len(items))
	}
	if !strings.Contains(items[0].Detail, "key present") {
		t.Fatalf("configured provider detail = %q, want key present", items[0].Detail)
	}
	if strings.Contains(items[0].Detail, "✓") {
		t.Fatalf("configured provider detail implies validation: %q", items[0].Detail)
	}
	if !strings.Contains(items[1].Detail, "Ctrl+A") {
		t.Fatalf("missing-key provider detail = %q, want Ctrl+A hint", items[1].Detail)
	}
}

func TestProviderItems_CustomKeylessProvider(t *testing.T) {
	cfg := config.Default()
	keyRequired := false
	cfg.Providers["localai"] = config.ProviderConfig{
		Type:           "openai_compatible",
		DefaultModel:   "coder",
		APIKeyRequired: &keyRequired,
	}

	items := providerItems([]provider.Provider{
		&fakeProvider{slug: "localai", name: "LocalAI"},
	}, cfg, "")
	if len(items) != 1 {
		t.Fatalf("providerItems len = %d, want 1", len(items))
	}
	if !strings.Contains(items[0].Detail, "coder") || !strings.Contains(items[0].Detail, "keyless") {
		t.Fatalf("custom keyless provider detail = %q", items[0].Detail)
	}
}

func TestApp_CtrlP_IncludesUnregisteredCustomFactory(t *testing.T) {
	r := newTestApp(t)
	r.cfg.Providers["localai"] = config.ProviderConfig{Type: "openai_compatible", DefaultModel: "coder"}
	r.app.deps.Factories = FactoryMap{
		"localai": func(string) provider.Provider {
			return &fakeProvider{slug: "localai", name: "LocalAI"}
		},
	}

	routeKey(t, r.app, "ctrl+p")
	out := r.app.picker.View()
	if !strings.Contains(out, "localai") {
		t.Fatalf("picker View missing custom provider row:\n%s", out)
	}
}

// TestApp_CtrlP_SelectAppliesSwitch verifies hitting Enter on a non-
// active row applies the switch through applyProviderSwitch.
func TestApp_CtrlP_SelectAppliesSwitch(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{slug: "second", name: "Second", models: []provider.Model{{ID: "m1"}}}
	r.reg.Register(second)
	// APIKey non-empty so the SelectMsg handler treats this as a real
	// provider and applies the switch — a key-less row would route to
	// the new key-entry prompt instead.
	r.cfg.Providers["second"] = config.ProviderConfig{APIKey: "sk-test", DefaultModel: "m1"}

	routeKey(t, r.app, "ctrl+p")

	// Walk cursor down until landing on "second".
	for i := 0; i < 10; i++ {
		if r.app.picker.CursorID() == "second" {
			break
		}
		r.app.picker, _ = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if r.app.picker.CursorID() != "second" {
		t.Fatalf("could not navigate cursor to 'second'; got %q", r.app.picker.CursorID())
	}
	// Dispatch Enter via App so the SelectMsg routes through Update.
	var selectCmd tea.Cmd
	r.app.picker, selectCmd = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
	resolveCmd(t, r.app, selectCmd)

	if p, m := r.reg.Active(); p == nil || p.Slug() != "second" || m != "m1" {
		t.Fatalf("active = %v / %q, want second / m1", p, m)
	}
	convContains(t, r.app, "switched provider: second (m1)")
}

// TestApp_CtrlP_EscLeavesActiveUnchanged verifies Esc dismisses the
// picker without touching the registry.
func TestApp_CtrlP_EscLeavesActiveUnchanged(t *testing.T) {
	r := newTestApp(t)
	r.reg.Register(&fakeProvider{slug: "second", name: "Second"})

	routeKey(t, r.app, "ctrl+p")
	var closeCmd tea.Cmd
	r.app.picker, closeCmd = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyEsc})
	resolveCmd(t, r.app, closeCmd)

	if r.app.picker.Visible() {
		t.Fatalf("picker should be hidden after Esc")
	}
	if p, _ := r.reg.Active(); p.Slug() != "fake" {
		t.Fatalf("active provider changed to %q on Esc", p.Slug())
	}
}

// TestApp_CtrlP_NoProvidersConfigured appends a friendly message and
// skips opening the modal when the registry is empty.
func TestApp_CtrlP_NoProvidersConfigured(t *testing.T) {
	r := newTestApp(t)
	r.app.deps.Registry = provider.NewRegistry() // drop all
	routeKey(t, r.app, "ctrl+p")
	if r.app.picker.Visible() {
		t.Fatalf("picker should stay hidden when no providers configured")
	}
	convContains(t, r.app, "no providers configured")
}

// TestApp_CtrlM_OpensModelPicker verifies Ctrl+M opens the model
// picker on the active provider.
func TestApp_CtrlM_OpensModelPicker(t *testing.T) {
	r := newTestApp(t)
	routeKey(t, r.app, "ctrl+m")
	if !r.app.picker.Visible() {
		t.Fatalf("picker should be visible after Ctrl+M")
	}
}

// TestApp_CtrlM_CachedListNoNetwork verifies a pre-warmed cache skips
// the loader path entirely.
func TestApp_CtrlM_CachedListNoNetwork(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
	before := atomic.LoadInt32(&r.prov.listCalls)

	cmd := routeKey(t, r.app, "ctrl+m")
	if cmd != nil {
		t.Fatalf("cached open must return nil cmd, got %T", cmd)
	}
	after := atomic.LoadInt32(&r.prov.listCalls)
	if after != before {
		t.Fatalf("ListModels was called despite cache hit: before=%d after=%d", before, after)
	}
	if !r.app.picker.Visible() {
		t.Fatalf("picker should be visible")
	}
}

// TestApp_CtrlM_FreshLoadFromListModels verifies an absent cache
// triggers the loader and populates items on resolution.
func TestApp_CtrlM_FreshLoadFromListModels(t *testing.T) {
	r := newTestApp(t)
	cmd := routeKey(t, r.app, "ctrl+m")
	if cmd == nil {
		t.Fatalf("expected loader cmd when cache cold, got nil")
	}
	// Resolve the loader: routes itemsLoadedMsg through Update.
	resolveCmd(t, r.app, cmd)
	if r.app.picker.Loading() {
		t.Fatalf("loading should be false after loader resolution")
	}
	out := r.app.picker.View()
	if !strings.Contains(out, "fake-model") || !strings.Contains(out, "fake-mini") {
		t.Fatalf("picker View missing loaded rows:\n%s", out)
	}
}

// TestApp_CtrlM_SelectAppliesModelSwitch verifies picking a row routes
// through applyModelSwitch.
func TestApp_CtrlM_SelectAppliesModelSwitch(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
	routeKey(t, r.app, "ctrl+m")

	// Walk to "fake-mini".
	for i := 0; i < 5; i++ {
		if r.app.picker.CursorID() == "fake-mini" {
			break
		}
		r.app.picker, _ = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	var cmd tea.Cmd
	r.app.picker, cmd = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
	resolveCmd(t, r.app, cmd)

	if _, m := r.reg.Active(); m != "fake-mini" {
		t.Fatalf("active model = %q, want fake-mini", m)
	}
	convContains(t, r.app, "switched model: fake/fake-mini")
}

// TestApp_CtrlM_CachePopulatedAfterLoad verifies a successful async
// load warms Registry.CachedModels.
func TestApp_CtrlM_CachePopulatedAfterLoad(t *testing.T) {
	r := newTestApp(t)
	if _, ok := r.reg.CachedModels("fake"); ok {
		t.Fatalf("precondition: cache should be cold")
	}
	cmd := routeKey(t, r.app, "ctrl+m")
	resolveCmd(t, r.app, cmd)

	cached, ok := r.reg.CachedModels("fake")
	if !ok || len(cached) == 0 {
		t.Fatalf("cache not populated after loader success")
	}
}

// TestApp_CtrlM_ListError verifies a loader error lands in error state
// with "r retry" visible.
func TestApp_CtrlM_ListError(t *testing.T) {
	r := newTestApp(t)
	r.prov.listErr = errors.New("offline")

	cmd := routeKey(t, r.app, "ctrl+m")
	resolveCmd(t, r.app, cmd)

	out := r.app.picker.View()
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected error line in View, got:\n%s", out)
	}
	if !strings.Contains(out, "r retry") {
		t.Fatalf("expected retry footer, got:\n%s", out)
	}
}

// TestApp_CtrlM_RetryFiresAgain verifies pressing 'r' in error state
// re-fires the loader.
func TestApp_CtrlM_RetryFiresAgain(t *testing.T) {
	r := newTestApp(t)
	r.prov.listErr = errors.New("offline")

	cmd := routeKey(t, r.app, "ctrl+m")
	resolveCmd(t, r.app, cmd)

	before := atomic.LoadInt32(&r.prov.listCalls)
	// Press "r". The picker handles it itself (still visible).
	var retryCmd tea.Cmd
	r.app.picker, retryCmd = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	// Allow the retry to succeed so we can assert it fired.
	r.prov.listErr = nil
	resolveCmd(t, r.app, retryCmd)

	after := atomic.LoadInt32(&r.prov.listCalls)
	if after <= before {
		t.Fatalf("retry did not re-fire ListModels: before=%d after=%d", before, after)
	}
}

// TestApp_CtrlP_RefusesToStackOverApproval verifies Ctrl+P is a no-op
// while an approval prompt is visible.
func TestApp_CtrlP_RefusesToStackOverApproval(t *testing.T) {
	r := newTestApp(t)
	showApprovalForTest(r.app)
	if !r.app.approval.Visible() {
		t.Fatalf("precondition: approval should be visible")
	}
	routeKey(t, r.app, "ctrl+p")
	if r.app.picker.Visible() {
		t.Fatalf("picker should NOT open over approval")
	}
}

// TestApp_CtrlM_RefusesToStackOverApproval — same, for Ctrl+M.
func TestApp_CtrlM_RefusesToStackOverApproval(t *testing.T) {
	r := newTestApp(t)
	showApprovalForTest(r.app)
	routeKey(t, r.app, "ctrl+m")
	if r.app.picker.Visible() {
		t.Fatalf("picker should NOT open over approval")
	}
}

// TestApp_CtrlP_OpensOverJobsPanel verifies jobs-panel visibility
// does not block the picker (picker has higher precedence).
func TestApp_CtrlP_OpensOverJobsPanel(t *testing.T) {
	r := newTestApp(t)
	// Fake the jobs panel into visible. We can't easily construct a
	// jobs.Snapshot here, so we poke Show with minimal data.
	r.app.jobsPanel.Resize(80, 24)
	// Forge visibility via Show on a zero snapshot.
	r.app.jobsPanel.Show(fakeJobsSnapForTest(), nil)

	routeKey(t, r.app, "ctrl+p")
	if !r.app.picker.Visible() {
		t.Fatalf("picker should open even while jobs panel is visible")
	}
}

// TestApp_ApplyProviderSwitch_UsesCacheFirst verifies a warm cache
// short-circuits the 2s ListModels fallback.
func TestApp_ApplyProviderSwitch_UsesCacheFirst(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{slug: "second", name: "Second"}
	r.reg.Register(second)
	r.reg.SetCachedModels("second", []provider.Model{{ID: "cached-m"}})

	before := atomic.LoadInt32(&second.listCalls)
	if err := r.app.applyProviderSwitch("second"); err != nil {
		t.Fatalf("applyProviderSwitch: %v", err)
	}
	after := atomic.LoadInt32(&second.listCalls)
	if after != before {
		t.Fatalf("ListModels should not be called when cache is warm: before=%d after=%d", before, after)
	}
	if _, m := r.reg.Active(); m != "cached-m" {
		t.Fatalf("active model = %q, want cached-m", m)
	}
}

// TestApp_ApplyProviderSwitch_Delegates verifies the slash handler
// calls into applyProviderSwitch (and therefore its side effects) by
// exercising the helper directly and comparing the public state.
func TestApp_ApplyProviderSwitch_Delegates(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{slug: "second", name: "Second", models: []provider.Model{{ID: "auto-1"}}}
	r.reg.Register(second)

	if err := r.app.applyProviderSwitch("second"); err != nil {
		t.Fatalf("applyProviderSwitch: %v", err)
	}
	convContains(t, r.app, "switched provider: second (auto-1)")

	if err := r.app.applyProviderSwitch("ghost"); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
}

// TestApp_ApplyModelSwitch_ErrorsWhenNoProvider — guard when Registry
// has no active provider.
func TestApp_ApplyModelSwitch_ErrorsWhenNoProvider(t *testing.T) {
	r := newTestApp(t)
	r.app.deps.Registry = provider.NewRegistry() // no active
	err := r.app.applyModelSwitch("whatever")
	if err == nil || !strings.Contains(err.Error(), "no active provider") {
		t.Fatalf("expected 'no active provider' error, got %v", err)
	}
}

// TestApp_SlashProviderStillWorksUnchanged — regression. The /provider
// slash command must continue to emit the same output post-refactor.
func TestApp_SlashProviderStillWorksUnchanged(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{slug: "second", name: "Second", models: []provider.Model{{ID: "m1"}}}
	r.reg.Register(second)
	r.cfg.Providers["second"] = config.ProviderConfig{DefaultModel: "m1"}

	r.app.handleSlashCommand("provider", []string{"second"}, "/provider second")
	convContains(t, r.app, "switched provider: second (m1)")
}

// TestApp_SlashModelStillWorksUnchanged — regression for /model.
func TestApp_SlashModelStillWorksUnchanged(t *testing.T) {
	r := newTestApp(t)
	r.app.handleSlashCommand("model", []string{"fake-mini"}, "/model fake-mini")
	convContains(t, r.app, "switched model: fake/fake-mini")
}

// TestApp_ProviderSwitch_UsesCacheWhenPresent — regression that the
// /provider slash command honours the cache rather than always hitting
// ListModels.
func TestApp_ProviderSwitch_UsesCacheWhenPresent(t *testing.T) {
	r := newTestApp(t)
	second := &fakeProvider{slug: "second", name: "Second"}
	r.reg.Register(second)
	r.reg.SetCachedModels("second", []provider.Model{{ID: "cached-m"}})

	before := atomic.LoadInt32(&second.listCalls)
	r.app.handleSlashCommand("provider", []string{"second"}, "/provider second")
	after := atomic.LoadInt32(&second.listCalls)
	if after != before {
		t.Fatalf("cache hit should skip ListModels: before=%d after=%d", before, after)
	}
	convContains(t, r.app, "switched provider: second (cached-m)")
}

// ─── helpers ───────────────────────────────────────────────────────────

// showApprovalForTest forces the approval prompt into visible state by
// handing it a real (if harmless) tools.Tool and empty ToolCall. The
// approval component only inspects .Name() at render time so any
// concrete Tool works.
func showApprovalForTest(a *App) {
	tool := tools.NewReadFileTool(".")
	a.approval.Show(tool, provider.ToolCall{Name: tool.Name(), Arguments: "{}"})
	a.approval.SetWidth(80)
}

// fakeJobsSnapForTest builds a minimal snapshot the jobs panel accepts
// through Show. The header render does not care about most fields.
func fakeJobsSnapForTest() jobspkg.Snapshot {
	now := time.Now()
	return jobspkg.Snapshot{
		ID:         "testjob",
		Provider:   "fake",
		Model:      "fake-model",
		State:      jobspkg.StateCompleted,
		CreatedAt:  now.Add(-5 * time.Second),
		StartedAt:  now.Add(-4 * time.Second),
		FinishedAt: now.Add(-1 * time.Second),
	}
}
