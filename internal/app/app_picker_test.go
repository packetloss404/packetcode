package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/config"
	jobspkg "github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/provider/codex"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/picker"
)

// routeKey simulates a bare keypress through App.handleKey. Returns
// the cmd produced so tests can resolve async loaders.
//
// Alt+M has a distinct v1 KeyMsg representation, so route it through the same
// handleKey path as a terminal event. Ctrl+M is deliberately absent because
// Bubble Tea v1 aliases it to Enter and could submit a draft.
func routeKey(t *testing.T, a *App, s string) tea.Cmd {
	t.Helper()
	var km tea.KeyMsg
	switch s {
	case "ctrl+p":
		km = tea.KeyMsg{Type: tea.KeyCtrlP}
	case "alt+m":
		km = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}
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

func TestProviderItems_CustomKeylessSugarProvider(t *testing.T) {
	cfg := config.Default()
	disabled := false
	keyRequired := false
	cfg.Sugar.Enabled = &disabled
	cfg.Providers["sugar"] = config.ProviderConfig{
		Type: "openai_compatible", DefaultModel: "custom-model", APIKeyRequired: &keyRequired,
	}

	items := providerItems([]provider.Provider{
		&fakeProvider{slug: "sugar", name: "Custom Sugar Slug"},
	}, cfg, "")
	if len(items) != 1 || !strings.Contains(items[0].Detail, "keyless") {
		t.Fatalf("custom keyless Sugar detail = %#v", items)
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

// TestApp_AltM_OpensModelPicker verifies Alt+M opens the model
// picker on the active provider.
func TestApp_AltM_OpensModelPicker(t *testing.T) {
	r := newTestApp(t)
	routeKey(t, r.app, "alt+m")
	if !r.app.picker.Visible() {
		t.Fatalf("picker should be visible after Alt+M")
	}
}

func TestApp_AltM_ReplacesAutocompleteAndOneEscapeClosesPicker(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}})
	r.app.input.SetValue("/co")
	r.app.refreshAutocomplete()
	if !r.app.autocomplete.Visible() {
		t.Fatal("precondition: autocomplete should be visible")
	}

	routeKey(t, r.app, "alt+m")
	if r.app.autocomplete.Visible() || !r.app.picker.Visible() {
		t.Fatalf("after Alt+M: autocomplete=%v picker=%v", r.app.autocomplete.Visible(), r.app.picker.Visible())
	}
	routeKey(t, r.app, "esc")
	if r.app.picker.Visible() {
		t.Fatal("one Escape should close the visible picker")
	}
}

// Cached models remain usable immediately while a fresh catalog is requested.
func TestApp_AltM_CachedListRefreshesInBackground(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
	before := atomic.LoadInt32(&r.prov.listCalls)

	cmd := routeKey(t, r.app, "alt+m")
	if cmd == nil {
		t.Fatal("cached open must request a background refresh")
	}
	after := atomic.LoadInt32(&r.prov.listCalls)
	if after != before {
		t.Fatalf("ListModels was called despite cache hit: before=%d after=%d", before, after)
	}
	if !r.app.picker.Visible() {
		t.Fatalf("picker should be visible")
	}
	if r.app.picker.Loading() || !r.app.picker.Refreshing() {
		t.Fatal("cached choices should remain available while refreshing")
	}
	if !strings.Contains(r.app.picker.View(), "fake-mini") {
		t.Fatal("cached models disappeared during refresh")
	}
	r.prov.models = append(r.prov.models, provider.Model{ID: "brand-new-model"})
	resolveCmd(t, r.app, cmd)
	if !strings.Contains(r.app.picker.View(), "brand-new-model") {
		t.Fatal("new model did not appear after background refresh")
	}
	if got := atomic.LoadInt32(&r.prov.listCalls); got != before+1 {
		t.Fatalf("expected one discovery request, got %d", got-before)
	}
}

func TestApp_ModelPickerReopeningDiscoversNewModels(t *testing.T) {
	r := newTestApp(t)
	resolveCmd(t, r.app, routeKey(t, r.app, "alt+m"))
	routeKey(t, r.app, "esc")
	r.prov.models = append(r.prov.models, provider.Model{ID: "new-since-first-open"})
	resolveCmd(t, r.app, routeKey(t, r.app, "alt+m"))
	if !strings.Contains(r.app.picker.View(), "new-since-first-open") {
		t.Fatal("reopening used the old catalog")
	}
}

func TestApp_ModelPickerRefreshesWhileOpen(t *testing.T) {
	r := newTestApp(t)
	cmd := routeKey(t, r.app, "alt+m")
	r.app.picker.SetRefreshInterval(time.Nanosecond)
	_, timer := r.app.Update(cmd())
	if timer == nil {
		t.Fatal("model discovery did not schedule periodic refresh")
	}
	r.prov.models = append(r.prov.models, provider.Model{ID: "new-while-picker-open"})
	_, refresh := r.app.Update(timer())
	if refresh == nil {
		t.Fatal("refresh timer did not request the provider catalog")
	}
	resolveCmd(t, r.app, refresh)
	if !strings.Contains(r.app.picker.View(), "new-while-picker-open") {
		t.Fatal("periodic refresh did not add the new model to the open picker")
	}
}

func TestApp_CodexPickerRefreshReadsUpdatedCLICatalog(t *testing.T) {
	r := newTestApp(t)
	authPath := filepath.Join(r.tmp, "auth.json")
	cachePath := filepath.Join(r.tmp, "models_cache.json")
	if err := os.WriteFile(cachePath, []byte(`{"models":[{"slug":"old-codex","visibility":"list"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.reg.Register(codex.New(authPath))
	if err := r.reg.SetActive("codex", "old-codex"); err != nil {
		t.Fatal(err)
	}
	resolveCmd(t, r.app, routeKey(t, r.app, "alt+m"))
	if err := os.WriteFile(cachePath, []byte(`{"models":[{"slug":"old-codex","visibility":"list"},{"slug":"new-codex","visibility":"list"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cmd := r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	resolveCmd(t, r.app, cmd)
	if !strings.Contains(r.app.picker.View(), "new-codex") {
		t.Fatal("refresh did not pick up the new model in the Codex CLI cache")
	}
	if got := r.app.picker.CursorID(); got != "old-codex" {
		t.Fatalf("refresh moved focus away from the current model: %q", got)
	}
}

func TestApp_ModelPickerRefreshFailureKeepsCachedModels(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
	r.prov.listErr = errors.New("offline")
	resolveCmd(t, r.app, routeKey(t, r.app, "alt+m"))
	if r.app.picker.LoadErr() != nil || r.app.picker.RefreshErr() == nil {
		t.Fatal("background failure should retain a usable cached list")
	}
	if !strings.Contains(r.app.picker.View(), "fake-mini") {
		t.Fatal("refresh failure discarded cached models")
	}
	models, ok := r.reg.CachedModels("fake")
	if !ok || len(models) != 2 {
		t.Fatal("refresh failure discarded the registry cache")
	}
	r.app.picker, _ = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyDown})
	resolveCmd(t, r.app, routeKey(t, r.app, "enter"))
	if _, model := r.reg.Active(); model != "fake-mini" {
		t.Fatalf("cached model could not be selected: %q", model)
	}
}

func TestApp_ModelPickerFocusesCurrentModel(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "fresh"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			r := newTestApp(t)
			if err := r.reg.SetActive("fake", "fake-mini"); err != nil {
				t.Fatal(err)
			}
			if cached {
				r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
			}
			resolveCmd(t, r.app, routeKey(t, r.app, "alt+m"))
			if got := r.app.picker.CursorID(); got != "fake-mini" {
				t.Fatalf("picker focused %q, want current model fake-mini", got)
			}
		})
	}
}

// TestApp_AltM_FreshLoadFromListModels verifies an absent cache
// triggers the loader and populates items on resolution.
func TestApp_AltM_FreshLoadFromListModels(t *testing.T) {
	r := newTestApp(t)
	cmd := routeKey(t, r.app, "alt+m")
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

// TestApp_AltM_SelectAppliesModelSwitch verifies picking a row routes
// through applyModelSwitch.
func TestApp_AltM_SelectAppliesModelSwitch(t *testing.T) {
	r := newTestApp(t)
	r.reg.SetCachedModels("fake", []provider.Model{{ID: "fake-model"}, {ID: "fake-mini"}})
	routeKey(t, r.app, "alt+m")

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

// TestApp_AltM_CachePopulatedAfterLoad verifies a successful async
// load warms Registry.CachedModels.
func TestApp_AltM_CachePopulatedAfterLoad(t *testing.T) {
	r := newTestApp(t)
	if _, ok := r.reg.CachedModels("fake"); ok {
		t.Fatalf("precondition: cache should be cold")
	}
	cmd := routeKey(t, r.app, "alt+m")
	resolveCmd(t, r.app, cmd)

	cached, ok := r.reg.CachedModels("fake")
	if !ok || len(cached) == 0 {
		t.Fatalf("cache not populated after loader success")
	}
}

// TestApp_AltM_ListError verifies a loader error lands in error state
// with "r retry" visible.
func TestApp_AltM_ListError(t *testing.T) {
	r := newTestApp(t)
	r.prov.listErr = errors.New("offline")

	cmd := routeKey(t, r.app, "alt+m")
	resolveCmd(t, r.app, cmd)

	out := r.app.picker.View()
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected error line in View, got:\n%s", out)
	}
	if !strings.Contains(out, "r retry") {
		t.Fatalf("expected retry footer, got:\n%s", out)
	}
}

// TestApp_AltM_RetryFiresAgain verifies pressing 'r' in error state
// re-fires the loader.
func TestApp_AltM_RetryFiresAgain(t *testing.T) {
	r := newTestApp(t)
	r.prov.listErr = errors.New("offline")

	cmd := routeKey(t, r.app, "alt+m")
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

// TestApp_AltM_RefusesToStackOverApproval — same, for Alt+M.
func TestApp_AltM_RefusesToStackOverApproval(t *testing.T) {
	r := newTestApp(t)
	showApprovalForTest(r.app)
	routeKey(t, r.app, "alt+m")
	if r.app.picker.Visible() {
		t.Fatalf("picker should NOT open over approval")
	}
}

// TestApp_CtrlP_RefusesToStackOverJobsPanel verifies the visible workspace
// keeps ownership rather than revealing a surprise picker after Escape.
func TestApp_CtrlP_RefusesToStackOverJobsPanel(t *testing.T) {
	r := newTestApp(t)
	// Fake the jobs panel into visible. We can't easily construct a
	// jobs.Snapshot here, so we poke Show with minimal data.
	r.app.jobsPanel.Resize(80, 24)
	// Forge visibility via Show on a zero snapshot.
	r.app.jobsPanel.Show(fakeJobsSnapForTest(), nil)

	routeKey(t, r.app, "ctrl+p")
	if r.app.picker.Visible() {
		t.Fatalf("picker should NOT open while jobs panel is visible")
	}
}

func TestApp_PickerShortcutsDoNotStackOverOtherWorkspaces(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*App)
	}{
		{"secret prompt", func(a *App) { a.prompt.Open("fake", "API key", "enter key", true) }},
		{"agent view", func(a *App) { a.agentView.Show(nil) }},
		{"workflow view", func(a *App) { a.workflowView.Show(nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestApp(t)
			tc.setup(r.app)
			for _, key := range []string{"ctrl+p", "alt+m"} {
				r.app.picker = picker.New("", "")
				routeKey(t, r.app, key)
				if r.app.picker.Visible() {
					t.Fatalf("%s opened a picker beneath %s", key, tc.name)
				}
			}
		})
	}
}

func TestApp_SecretPromptOwnsTerminalGeometry(t *testing.T) {
	r := newTestApp(t)
	r.app.resize(80, 24)
	r.app.prompt.Open("fake", "API key", "enter key", true)

	view := r.app.View()
	if got := lipgloss.Height(view); got > 24 {
		t.Fatalf("prompt view height = %d, want <= 24:\n%s", got, view)
	}
	if strings.Contains(view, "❯") {
		t.Fatalf("composer rendered beneath full-screen prompt:\n%s", view)
	}
}

func TestApp_ClearShortcutDoesNotMutateBeneathPrompt(t *testing.T) {
	r := newTestApp(t)
	r.app.conversation.AppendSystem("keep this visible")
	r.app.prompt.Open("fake", "API key", "enter key", true)

	_, cmd := r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd != nil {
		t.Fatal("Ctrl+L beneath prompt returned a command")
	}
	if !strings.Contains(convText(r.app), "keep this visible") {
		t.Fatal("Ctrl+L cleared conversation beneath prompt")
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
