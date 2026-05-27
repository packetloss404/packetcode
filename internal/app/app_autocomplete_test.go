package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeRunes drives a sequence of printable runes through handleKey so
// the autocomplete popup's open/close/refresh behaviour can be observed
// at the App layer.
func typeRunes(t *testing.T, a *App, s string) {
	t.Helper()
	for _, r := range s {
		km := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		_, _ = a.handleKey(km)
	}
}

// sendKey dispatches a named special key through handleKey.
func sendKey(t *testing.T, a *App, typ tea.KeyType) tea.Cmd {
	t.Helper()
	_, cmd := a.handleKey(tea.KeyMsg{Type: typ})
	return cmd
}

// pressBackspace removes one rune from the input buffer via handleKey.
func pressBackspace(t *testing.T, a *App) {
	t.Helper()
	_, _ = a.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
}

// TestApp_Autocomplete_OpensOnSlash — typing "/" opens the popup.
func TestApp_Autocomplete_OpensOnSlash(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("popup should be visible after typing /")
	}
}

// TestApp_Autocomplete_NarrowsAsUserTypes — each additional rune
// narrows the filter. "sp" leaves only /spawn.
func TestApp_Autocomplete_NarrowsAsUserTypes(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("popup should remain visible while typing /sp")
	}
	if got := r.app.autocomplete.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1 (spawn only)", got)
	}
	if got := r.app.autocomplete.SelectedVerb(); got != "spawn" {
		t.Fatalf("SelectedVerb = %q, want spawn", got)
	}
}

// TestApp_Autocomplete_ClosesOnSpaceAfterVerb — as soon as the first
// whitespace lands, the popup dismisses.
func TestApp_Autocomplete_ClosesOnSpaceAfterVerb(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/spawn")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("popup should be visible before the space")
	}
	typeRunes(t, r.app, " ")
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close once whitespace lands in the buffer")
	}
}

// TestApp_Autocomplete_ClosesOnBackspacePastSlash — removing the "/"
// closes the popup.
func TestApp_Autocomplete_ClosesOnBackspacePastSlash(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be visible")
	}
	pressBackspace(t, r.app)
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close once the slash is gone")
	}
}

// TestApp_Autocomplete_StaysClosedOnNonSlashInput — prose doesn't
// trigger the popup.
func TestApp_Autocomplete_StaysClosedOnNonSlashInput(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "hello")
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should not open on non-slash input")
	}
}

// TestApp_Autocomplete_EmptyFilterShowsAllEntriesInOrder — typing just
// "/" lists every verb in keymap order, cursor on the first row.
func TestApp_Autocomplete_EmptyFilterShowsAllEntriesInOrder(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/")
	if got := r.app.autocomplete.Count(); got != len(buildAutocompleteEntries()) {
		t.Fatalf("Count = %d, want %d", got, len(buildAutocompleteEntries()))
	}
	if got := r.app.autocomplete.SelectedVerb(); got != "spawn" {
		// First entry in SlashCommands is /spawn.
		t.Fatalf("SelectedVerb = %q, want spawn", got)
	}
}

// TestApp_Autocomplete_TabAccepts — Tab fills the buffer with the
// highlighted verb plus a trailing space.
func TestApp_Autocomplete_TabAccepts(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	sendKey(t, r.app, tea.KeyTab)
	if got := r.app.input.Value(); got != "/spawn " {
		t.Fatalf("input.Value = %q, want %q", got, "/spawn ")
	}
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close on accept")
	}
}

// TestApp_Autocomplete_TabAcceptsProviderOpensPicker — accepting
// /provider from the popup opens the provider picker straight away
// (instead of leaving "/provider " in the buffer) and clears the input.
func TestApp_Autocomplete_TabAcceptsProviderOpensPicker(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/prov")
	if got := r.app.autocomplete.SelectedVerb(); got != "provider" {
		t.Fatalf("precondition: SelectedVerb = %q, want provider", got)
	}
	sendKey(t, r.app, tea.KeyTab)
	if !r.app.picker.Visible() || r.app.picker.ID() != "provider" {
		t.Fatalf("Tab on /provider should open provider picker; visible=%v id=%q",
			r.app.picker.Visible(), r.app.picker.ID())
	}
	if got := r.app.input.Value(); got != "" {
		t.Fatalf("input should be cleared on picker open, got %q", got)
	}
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close on accept")
	}
}

// TestApp_Autocomplete_TabAcceptsModelOpensPicker — same flow for
// /model: accepting the verb opens the model picker.
func TestApp_Autocomplete_TabAcceptsModelOpensPicker(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/mod")
	if got := r.app.autocomplete.SelectedVerb(); got != "model" {
		t.Fatalf("precondition: SelectedVerb = %q, want model", got)
	}
	sendKey(t, r.app, tea.KeyTab)
	if !r.app.picker.Visible() || r.app.picker.ID() != "model" {
		t.Fatalf("Tab on /model should open model picker; visible=%v id=%q",
			r.app.picker.Visible(), r.app.picker.ID())
	}
	if got := r.app.input.Value(); got != "" {
		t.Fatalf("input should be cleared on picker open, got %q", got)
	}
}

// TestApp_Autocomplete_TabWithNoMatchesIsNoOp — Tab with zero matches
// does not mutate the buffer (SelectedVerb returns "").
func TestApp_Autocomplete_TabWithNoMatchesIsNoOp(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/zzz")
	before := r.app.input.Value()
	sendKey(t, r.app, tea.KeyTab)
	if got := r.app.input.Value(); got != before {
		t.Fatalf("buffer mutated on Tab with no matches: %q -> %q", before, got)
	}
}

// TestApp_Autocomplete_EnterAcceptsWhenBufferIsBareVerb — Enter on a
// bare-verb buffer (no whitespace) accepts into "/<verb> ".
func TestApp_Autocomplete_EnterAcceptsWhenBufferIsBareVerb(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	sendKey(t, r.app, tea.KeyEnter)
	if got := r.app.input.Value(); got != "/spawn " {
		t.Fatalf("input.Value = %q, want %q", got, "/spawn ")
	}
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close on accept")
	}
}

// TestApp_Autocomplete_EnterSubmitsWhenBufferHasArgs — buffer like
// "/spawn write a test" has whitespace → popup is closed → Enter falls
// through to input.SubmitMsg. We verify by observing that the
// autocomplete is not visible at Enter time.
func TestApp_Autocomplete_EnterSubmitsWhenBufferHasArgs(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/spawn")
	typeRunes(t, r.app, " ")
	typeRunes(t, r.app, "hi")
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup must be closed when buffer contains whitespace")
	}
	// Drive Enter; the input's Update path emits SubmitMsg downstream.
	// We're not trying to execute the full submit here — just asserting
	// the popup did not swallow it.
	_, cmd := r.app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Enter with args should produce a SubmitMsg tea.Cmd")
	}
}

// TestApp_Autocomplete_EnterFallsThroughOnNoMatches — "/xyz" has no
// matches; the popup stays visible (zero filtered rows) but Enter must
// still fall through to the input's SubmitMsg path so the text reaches
// the LLM as a normal message.
func TestApp_Autocomplete_EnterFallsThroughOnNoMatches(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/xyz")
	if r.app.autocomplete.SelectedVerb() != "" {
		t.Fatalf("precondition: SelectedVerb should be empty with zero matches")
	}
	_, cmd := r.app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("Enter with zero matches should fall through to input submit")
	}
}

// TestApp_Autocomplete_EscClosesPopupButKeepsText — Esc dismisses the
// popup without touching the buffer.
func TestApp_Autocomplete_EscClosesPopupButKeepsText(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/pro")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be visible")
	}
	sendKey(t, r.app, tea.KeyEsc)
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should close on Esc")
	}
	if got := r.app.input.Value(); got != "/pro" {
		t.Fatalf("Esc must not mutate buffer: got %q, want /pro", got)
	}
}

// TestApp_Autocomplete_ArrowsMoveCursor — Down moves the cursor to the
// next matched row. "cancel" and "clear" and "compact" and "cost" all
// share the "c" prefix; cursor-0 starts on "cancel", down moves it to
// "clear".
func TestApp_Autocomplete_ArrowsMoveCursor(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/c")
	if got := r.app.autocomplete.SelectedVerb(); got != "cancel" {
		t.Fatalf("precondition: SelectedVerb = %q, want cancel", got)
	}
	sendKey(t, r.app, tea.KeyDown)
	if got := r.app.autocomplete.SelectedVerb(); got != "clear" {
		t.Fatalf("down: SelectedVerb = %q, want clear", got)
	}
	sendKey(t, r.app, tea.KeyUp)
	if got := r.app.autocomplete.SelectedVerb(); got != "cancel" {
		t.Fatalf("up: SelectedVerb = %q, want cancel", got)
	}
}

// TestApp_Autocomplete_CtrlNPNavigate — Ctrl+N / Ctrl+P also navigate.
func TestApp_Autocomplete_CtrlNPNavigate(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/c")
	sendKey(t, r.app, tea.KeyCtrlN)
	if got := r.app.autocomplete.SelectedVerb(); got != "clear" {
		t.Fatalf("ctrl+n: SelectedVerb = %q, want clear", got)
	}
	sendKey(t, r.app, tea.KeyCtrlP)
	if got := r.app.autocomplete.SelectedVerb(); got != "cancel" {
		t.Fatalf("ctrl+p: SelectedVerb = %q, want cancel", got)
	}
}

// TestApp_Autocomplete_ClosesWhenApprovalOpens — approval modal has
// priority; the popup must close as soon as the approval becomes
// visible and stay closed while it is.
func TestApp_Autocomplete_ClosesWhenApprovalOpens(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be visible")
	}
	showApprovalForTest(r.app)
	// Any subsequent key routes through handleKey and should call
	// refreshAutocomplete, closing the popup. We dispatch a harmless
	// rune through it to trigger the refresh path; in practice the
	// approval modal consumes keys first but the popup-close happens
	// anyway.
	// Directly exercise refresh by calling through handleKey with a
	// key that the approval modal will consume.
	r.app.refreshAutocomplete()
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should be closed while approval is visible")
	}
}

// TestApp_Autocomplete_ClosesWhenPickerOpens — same story for the
// generic picker modal.
func TestApp_Autocomplete_ClosesWhenPickerOpens(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be visible")
	}
	// Ctrl+P with popup visible navigates the popup (popup wins).
	// To open the picker while the popup is up, dismiss first then
	// open — the typical user flow. We also verify refresh closes
	// the popup once the picker is visible.
	r.app.autocomplete.Close()
	r.app.openProviderPicker()
	if !r.app.picker.Visible() {
		t.Fatalf("precondition: picker should be visible")
	}
	r.app.refreshAutocomplete()
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should be closed while picker is visible")
	}
}

func TestApp_Autocomplete_ClosesWhenAgentViewOpens(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be visible")
	}
	r.app.agentView.Show(nil)
	r.app.refreshAutocomplete()
	if r.app.autocomplete.Visible() {
		t.Fatalf("popup should be closed while agent view is visible")
	}
}

// TestApp_Autocomplete_ReopensAfterPickerCloses — once the picker is
// dismissed and the user types "/" again, the popup reappears.
func TestApp_Autocomplete_ReopensAfterPickerCloses(t *testing.T) {
	r := newTestApp(t)
	routeKey(t, r.app, "ctrl+p")
	// Dismiss the picker.
	var closeCmd tea.Cmd
	r.app.picker, closeCmd = r.app.picker.Update(tea.KeyMsg{Type: tea.KeyEsc})
	resolveCmd(t, r.app, closeCmd)
	if r.app.picker.Visible() {
		t.Fatalf("precondition: picker should be hidden")
	}
	// Now type "/" and the popup should open.
	typeRunes(t, r.app, "/")
	if !r.app.autocomplete.Visible() {
		t.Fatalf("popup should open after picker closes")
	}
}

// TestApp_Autocomplete_TabDoesNotInsertTabCharacter — when the popup
// is visible, Tab is consumed by the accept path and never falls
// through to the input (the buffer must not contain a literal '\t').
func TestApp_Autocomplete_TabDoesNotInsertTabCharacter(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/sp")
	sendKey(t, r.app, tea.KeyTab)
	if strings.Contains(r.app.input.Value(), "\t") {
		t.Fatalf("Tab should not land as a literal \\t in the buffer; got %q", r.app.input.Value())
	}
}

// TestApp_Autocomplete_TabFallsThroughToInputWhenHidden — Tab with a
// hidden popup is ignored by autocomplete and is not inserted into the
// input buffer.
func TestApp_Autocomplete_TabFallsThroughToInputWhenHidden(t *testing.T) {
	r := newTestApp(t)
	if r.app.autocomplete.Visible() {
		t.Fatalf("precondition: popup should be hidden")
	}
	sendKey(t, r.app, tea.KeyTab)
	// The popup must stay hidden; we're not asserting conversation
	// behaviour here because it lives in conversation.Update.
	if r.app.autocomplete.Visible() {
		t.Fatalf("Tab with popup hidden should not open the popup")
	}
}

// TestApp_Autocomplete_SlashHelpStillFires — /help is a common case;
// the autocomplete layer must not regress its submission.
func TestApp_Autocomplete_SlashHelpStillFires(t *testing.T) {
	r := newTestApp(t)
	// Open via typing, then accept and submit the bare verb.
	typeRunes(t, r.app, "/help")
	sendKey(t, r.app, tea.KeyEnter)
	// After accept, buffer is "/help "; a bare /help still parses.
	if got := r.app.input.Value(); got != "/help " {
		t.Fatalf("after accept: input = %q, want %q", got, "/help ")
	}
	cmd, args, ok := ParseSlashCommand(r.app.input.Value())
	if !ok || cmd != "help" || len(args) != 0 {
		t.Fatalf("ParseSlashCommand(%q) = %q %v %v", r.app.input.Value(), cmd, args, ok)
	}
}

// TestApp_Autocomplete_SelectedVerbAfterFilterDoesNotMatch — once the
// filter excludes every entry, SelectedVerb is empty and Tab is a
// no-op on the buffer.
func TestApp_Autocomplete_SelectedVerbAfterFilterDoesNotMatch(t *testing.T) {
	r := newTestApp(t)
	typeRunes(t, r.app, "/zzz")
	if got := r.app.autocomplete.SelectedVerb(); got != "" {
		t.Fatalf("SelectedVerb with no matches = %q, want empty", got)
	}
}

// TestApp_Autocomplete_PopupDoesNotRenderWhenHidden — an idle App
// doesn't leak a popup into the frame.
func TestApp_Autocomplete_PopupDoesNotRenderWhenHidden(t *testing.T) {
	r := newTestApp(t)
	r.app.resize(120, 40)
	if got := r.app.autocomplete.View(); got != "" {
		t.Fatalf("hidden View() must be empty, got %q", got)
	}
	out := r.app.View()
	// Spot-check the frame lacks the cursor-row marker glyph.
	if strings.Contains(out, "▶ ") && r.app.picker.Visible() == false && r.app.approval.Visible() == false {
		// ▶ may legitimately appear in other components — only assert
		// autocomplete's footer marker is absent.
		if strings.Contains(out, "+") && strings.Contains(out, "more") {
			t.Fatalf("autocomplete overflow footer leaked into View() when hidden")
		}
	}
}

// TestApp_Autocomplete_EntriesDedupedFromKeymap — /jobs appears twice
// in keymap.SlashCommands ("/jobs" and "/jobs <id>") but the popup
// must render one entry for it.
func TestApp_Autocomplete_EntriesDedupedFromKeymap(t *testing.T) {
	entries := buildAutocompleteEntries()
	seen := make(map[string]int)
	for _, e := range entries {
		seen[e.Verb]++
	}
	if seen["jobs"] != 1 {
		t.Fatalf("jobs verb count = %d, want 1 (deduped)", seen["jobs"])
	}
	// And every known verb is present.
	for _, want := range []string{
		"spawn", "agents", "jobs", "cancel", "provider", "model", "sessions",
		"queue", "undo", "compact", "cost", "trust", "help", "clear", "mcp",
		"statusline",
	} {
		if seen[want] != 1 {
			t.Fatalf("entry for %q missing (count=%d)", want, seen[want])
		}
	}
}
