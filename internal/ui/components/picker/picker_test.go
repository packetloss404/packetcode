package picker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// threeItems is a shared fixture for picker tests that need a list
// wider than one row.
func threeItems() []Item {
	return []Item{
		{ID: "alpha", Label: "Alpha", Detail: "first"},
		{ID: "bravo", Label: "Bravo", Detail: "second"},
		{ID: "charlie", Label: "Charlie", Detail: "third"},
	}
}

// runKey pushes a single keystroke through Update and returns the new
// Model plus the Cmd. Tests ignore the returned cmd unless they care
// about the message it emits.
func runKey(m Model, km tea.KeyMsg) (Model, tea.Cmd) {
	return m.Update(km)
}

// openSync opens the picker with preloaded items, matching the
// provider-picker flow.
func openSync(items []Item) Model {
	m := New("test", "Pick one")
	m.Resize(120, 30)
	m.SetItems(items)
	m.Open(nil)
	return m
}

// TestPicker_NewHidden — New returns a Model that is not visible until
// Open is called.
func TestPicker_NewHidden(t *testing.T) {
	m := New("x", "Title")
	assert.False(t, m.Visible(), "New should return a hidden picker")
	assert.Equal(t, "", m.View())
}

// TestPicker_OpenSyncPopulates — loader == nil + preloaded items.
func TestPicker_OpenSyncPopulates(t *testing.T) {
	m := New("test", "Pick one")
	m.Resize(120, 30)
	m.SetItems(threeItems())
	cmd := m.Open(nil)
	assert.Nil(t, cmd, "synchronous open returns a nil tea.Cmd")
	assert.True(t, m.Visible())
	assert.False(t, m.loading)
	assert.Equal(t, 3, len(m.filtered))
}

// TestPicker_OpenAsyncShowsLoading — loader != nil flips loading=true
// and returns a non-nil tea.Cmd.
func TestPicker_OpenAsyncShowsLoading(t *testing.T) {
	m := New("test", "Pick one")
	m.Resize(120, 30)
	loader := func(ctx context.Context) ([]Item, error) { return threeItems(), nil }
	cmd := m.Open(loader)
	require.NotNil(t, cmd, "async open must return a tea.Cmd")
	assert.True(t, m.Visible())
	assert.True(t, m.loading)
	assert.Contains(t, m.View(), "loading")
}

// TestPicker_ItemsLoadedMsgPopulates — loaded items display after the
// loader command resolves.
func TestPicker_ItemsLoadedMsgPopulates(t *testing.T) {
	m := New("test", "Pick one")
	m.Resize(120, 30)
	loader := func(ctx context.Context) ([]Item, error) { return threeItems(), nil }
	cmd := m.Open(loader)
	msg := cmd()
	m, _ = m.Update(msg)
	assert.False(t, m.loading)
	assert.Equal(t, 3, len(m.filtered))
	assert.Contains(t, m.View(), "Alpha")
}

// TestPicker_ItemsLoadedError — a loader error shows the retry footer
// and keeps the picker visible.
func TestPicker_ItemsLoadedError(t *testing.T) {
	m := New("test", "Pick one")
	m.Resize(120, 30)
	loader := func(ctx context.Context) ([]Item, error) { return nil, errors.New("boom") }
	cmd := m.Open(loader)
	msg := cmd()
	m, _ = m.Update(msg)
	assert.True(t, m.Visible())
	assert.False(t, m.loading)
	require.Error(t, m.loadErr)
	out := m.View()
	assert.Contains(t, out, "error: boom")
	assert.Contains(t, out, "r retry")
}

// TestPicker_ArrowsMoveCursor — up/down arrows move the cursor and
// clamp at 0 / len-1.
func TestPicker_ArrowsMoveCursor(t *testing.T) {
	m := openSync(threeItems())
	assert.Equal(t, 0, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 1, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)

	// Clamp at bottom.
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 1, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, m.cursor)

	// Clamp at top.
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, 0, m.cursor)
}

// TestPicker_CtrlNPAliases — Ctrl+N == down, Ctrl+P == up.
func TestPicker_CtrlNPAliases(t *testing.T) {
	m := openSync(threeItems())

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	assert.Equal(t, 1, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	assert.Equal(t, 2, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyCtrlP})
	assert.Equal(t, 1, m.cursor)
}

// TestPicker_EnterEmitsSelectMsg — Enter selects the cursor row and
// hides the picker.
func TestPicker_EnterEmitsSelectMsg(t *testing.T) {
	m := openSync(threeItems())
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown}) // cursor → "bravo"

	m, cmd := runKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	msg := cmd()
	sel, ok := msg.(SelectMsg)
	require.True(t, ok, "expected SelectMsg, got %T", msg)
	assert.Equal(t, "test", sel.PickerID)
	assert.Equal(t, "bravo", sel.Item.ID)
	assert.False(t, m.Visible(), "Enter should hide picker")
}

// TestPicker_EscEmitsCloseMsg — Esc emits CloseMsg + hides.
func TestPicker_EscEmitsCloseMsg(t *testing.T) {
	m := openSync(threeItems())
	m, cmd := runKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)
	msg := cmd()
	c, ok := msg.(CloseMsg)
	require.True(t, ok, "expected CloseMsg, got %T", msg)
	assert.Equal(t, "test", c.PickerID)
	assert.False(t, m.Visible())
}

// TestPicker_FilterNarrowsList — typing a rune filters the list to
// matching items only.
func TestPicker_FilterNarrowsList(t *testing.T) {
	m := openSync(threeItems())
	assert.Equal(t, 3, len(m.filtered))

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	assert.Equal(t, 1, len(m.filtered), "filter should narrow to items containing 'b'")
	assert.Equal(t, "bravo", m.items[m.filtered[0]].ID)
}

// TestPicker_FilterClampsCursor — filtering with a cursor past the
// new list length must clamp it.
func TestPicker_FilterClampsCursor(t *testing.T) {
	m := openSync(threeItems())
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, 2, m.cursor)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	// "a" matches alpha (row 0) and charlie (row 2 — 'a' in "charlie"),
	// or just alpha. Whichever — cursor must be in range.
	assert.Less(t, m.cursor, len(m.filtered))
}

// TestPicker_BackspaceEditsFilter — backspace drops the last rune.
func TestPicker_BackspaceEditsFilter(t *testing.T) {
	m := openSync(threeItems())
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	assert.Equal(t, "br", m.filter)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "b", m.filter)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "", m.filter)

	// Extra backspace on empty filter is a no-op.
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "", m.filter)
}

// TestPicker_CtrlUClearsFilter — Ctrl+U resets filter to "".
func TestPicker_CtrlUClearsFilter(t *testing.T) {
	m := openSync(threeItems())
	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	assert.Equal(t, "b", m.filter)

	m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	assert.Equal(t, "", m.filter)
	assert.Equal(t, 3, len(m.filtered))
}

// TestPicker_SetActiveSeedsCursor — SetActive(id) moves cursor to
// the matching row.
func TestPicker_SetActiveSeedsCursor(t *testing.T) {
	m := New("test", "Pick")
	m.Resize(120, 30)
	m.SetItems(threeItems())
	m.Open(nil)
	m.SetActive("charlie")
	assert.Equal(t, 2, m.cursor)
}

// TestPicker_MarkerAppearsOnActive — an item with Marker="●" renders
// its marker in the View.
func TestPicker_MarkerAppearsOnActive(t *testing.T) {
	items := threeItems()
	items[1].Marker = "●"
	m := openSync(items)
	out := m.View()
	assert.Contains(t, out, "●", "active marker should appear in View")
}

// TestPicker_EmptyFilteredShowsNoMatches — filtering to zero rows
// renders a "no matches" placeholder.
func TestPicker_EmptyFilteredShowsNoMatches(t *testing.T) {
	m := openSync(threeItems())
	// Type a string that matches nothing.
	for _, r := range "zzzzz" {
		m, _ = runKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	assert.Equal(t, 0, len(m.filtered))
	assert.Contains(t, m.View(), "no matches")
}

// TestPicker_RetryOnErrorState — in error state, pressing "r" re-fires
// the loader.
func TestPicker_RetryOnErrorState(t *testing.T) {
	calls := 0
	loader := func(ctx context.Context) ([]Item, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("first fail")
		}
		return threeItems(), nil
	}
	m := New("test", "Pick")
	m.Resize(120, 30)
	cmd := m.Open(loader)
	// Resolve first loader: error.
	m, _ = m.Update(cmd())
	require.Error(t, m.loadErr)

	// "r" should restart the loader.
	m, retryCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	require.NotNil(t, retryCmd)
	assert.True(t, m.loading)
	assert.Nil(t, m.loadErr)

	m, _ = m.Update(retryCmd())
	assert.False(t, m.loading)
	assert.Nil(t, m.loadErr)
	assert.Equal(t, 3, len(m.filtered))
	assert.Equal(t, 2, calls)
}

// TestPicker_ResizeRecomputesWidth — Resize(w,h) stores the new size
// and the rendered modal widens/narrows accordingly.
func TestPicker_ResizeRecomputesWidth(t *testing.T) {
	m := openSync(threeItems())
	wide := m.View()
	m.Resize(200, 60)
	wider := m.View()

	// Sanity: width changed in the render. We lean on rendered width
	// of the longest line as the signal, since the outer frame
	// expands.
	widestLine := func(s string) int {
		max := 0
		for _, l := range strings.Split(s, "\n") {
			if n := len([]rune(l)); n > max {
				max = n
			}
		}
		return max
	}
	assert.GreaterOrEqual(t, widestLine(wider), widestLine(wide),
		"wider terminal should produce equal-or-wider modal")
}

func TestPicker_OpenKeepsActiveSelection(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%t", async), func(t *testing.T) {
			m := New("test", "Pick")
			m.Resize(80, 24)
			m.SetItems(threeItems())
			m.SetActive("charlie")
			var loader Loader
			if async {
				loader = func(context.Context) ([]Item, error) { return threeItems(), nil }
			}
			if cmd := m.Open(loader); cmd != nil {
				m, _ = m.Update(cmd())
			}
			assert.Equal(t, "charlie", m.CursorID())
			m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			require.NotNil(t, cmd)
			assert.Equal(t, "charlie", cmd().(SelectMsg).Item.ID)
		})
	}
}

func TestPicker_LayoutFitsTerminal(t *testing.T) {
	items := make([]Item, 20)
	for i := range items {
		items[i] = Item{
			ID: fmt.Sprint(i), Label: strings.Repeat("模型👩🏽‍💻", 20),
			Detail: strings.Repeat("long detail ", 30),
		}
	}
	for _, size := range [][2]int{{120, 40}, {100, 30}, {72, 24}, {40, 12}, {20, 8}} {
		for _, state := range []string{"list", "search", "empty", "loading", "error", "refreshing", "refresh error"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], state), func(t *testing.T) {
				m := openSync(items)
				m.SetSubtitle("A provider with a very long descriptive name")
				m.SetHelp("Ctrl+A API key")
				m.Resize(size[0], size[1])
				switch state {
				case "search":
					m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("模型", 60))})
				case "empty":
					m.SetItems(nil)
				case "loading":
					m.loading = true
				case "error":
					m.loadErr = errors.New(strings.Repeat("long error\n", 30))
				case "refreshing":
					m.refreshing = true
					m.SetHelp("Ctrl+R refresh")
				case "refresh error":
					m.refreshErr = errors.New("offline")
				}
				view := m.View()
				assert.LessOrEqual(t, lipgloss.Width(view), size[0], "overflows horizontally:\n%s", view)
				assert.Equal(t, m.modalWidth(), lipgloss.Width(view))
				assert.Equal(t, m.modalHeight(), lipgloss.Height(view), "geometry disagrees with rendering:\n%s", view)
				assert.LessOrEqual(t, lipgloss.Height(view), size[1])
			})
		}
	}
}

func TestPicker_SearchVisibleAndHeightStable(t *testing.T) {
	m := openSync(threeItems())
	height := lipgloss.Height(m.View())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("br")})
	assert.Contains(t, ansi.Strip(m.View()), "› br▏")
	assert.Equal(t, height, lipgloss.Height(m.View()))
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 100) + "tail")})
	assert.Contains(t, ansi.Strip(m.View()), "tail▏")
}

func TestPicker_LongModelIDAndCurrentStatusStayVisible(t *testing.T) {
	id := "anthropic/claude-sonnet-4-20250514"
	m := openSync([]Item{{ID: id, Label: id, Detail: "200k context · tools", Marker: "●"}})
	view := ansi.Strip(m.View())
	assert.Contains(t, view, id)
	assert.Contains(t, view, "current")
	assert.Contains(t, view, "200k context · tools")
}

func TestPicker_ResizeKeepsFocusedRowVisible(t *testing.T) {
	items := make([]Item, 30)
	for i := range items {
		items[i] = Item{ID: fmt.Sprintf("model-%02d", i), Label: fmt.Sprintf("model-%02d", i)}
	}
	m := openSync(items)
	m.SetActive("model-29")
	for _, size := range [][2]int{{100, 30}, {40, 12}, {72, 24}} {
		m.Resize(size[0], size[1])
		assert.Contains(t, m.View(), "model-29")
		assert.Equal(t, "model-29", m.CursorID())
	}
}
