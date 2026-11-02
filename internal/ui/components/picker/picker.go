package picker

import (
	"context"
	"sync/atomic"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Item is a single selectable row. Extra is caller-owned and survives
// the round-trip through SelectMsg so handlers can stash a typed
// payload (e.g. a provider.Model) without reflecting on ID.
type Item struct {
	ID     string
	Label  string
	Detail string
	Marker string         // single-rune visual marker (e.g. "●" for active)
	Color  lipgloss.Color // optional tint for the marker
	Status string         // optional short status, e.g. "key needed"
	Extra  any
}

// Loader produces a fresh item list. Preloaded items remain available while
// it runs; without preloaded items the picker shows its initial loading state.
type Loader func(ctx context.Context) ([]Item, error)

// SelectMsg is emitted when the user hits Enter on a row. PickerID is
// the id the caller passed to New(), so a single App-level handler can
// route multiple pickers by switching on it.
type SelectMsg struct {
	PickerID string
	Item     Item
}

// CloseMsg is emitted when the user hits Esc. PickerID identifies the
// originating picker for the same reason as SelectMsg.
type CloseMsg struct{ PickerID string }

// itemsLoadedMsg is the internal message the loader tea.Cmd returns.
// The App routes it back into the picker's Update when the picker is
// visible; the picker populates items / loadErr accordingly.
type itemsLoadedMsg struct {
	pickerID string
	instance uint64
	request  uint64
	items    []Item
	err      error
}

type refreshMsg struct {
	instance uint64
	request  uint64
}

var nextInstance atomic.Uint64

// Model is the picker modal. Construct with New(); show via Open(...).
// Follows the same value-receiver-on-Update convention as approval and
// jobs so it composes cleanly into App.Update.
type Model struct {
	id           string
	visible      bool
	title        string
	subtitle     string
	help         string
	activeID     string
	filter       string
	items        []Item
	filtered     []int
	cursor       int
	scrollOffset int
	width        int
	height       int
	loading      bool
	loadErr      error
	loader       Loader
	refreshing   bool
	refreshErr   error
	interval     time.Duration
	instance     uint64
	request      uint64
	cancel       context.CancelFunc
}

// New returns a hidden Model with the given id and title. The id lets
// a single SelectMsg / CloseMsg handler disambiguate multiple pickers
// sharing the same App.
func New(id, title string) Model {
	return Model{id: id, title: title, instance: nextInstance.Add(1)}
}

// Open makes the picker visible. If loader is nil the picker is
// presented synchronously. Otherwise the returned command loads fresh items
// while keeping any preloaded choices available for selection.
func (m *Model) Open(loader Loader) tea.Cmd {
	m.stopLoad()
	m.visible = true
	m.loadErr = nil
	m.refreshErr = nil
	m.filter = ""
	m.cursor = 0
	m.scrollOffset = 0
	m.loader = loader
	m.rebuildFiltered()
	m.SetActive(m.activeID)
	if loader == nil {
		return nil
	}
	return m.beginLoad()
}

func (m *Model) beginLoad() tea.Cmd {
	if m.loader == nil || m.loading || m.refreshing {
		return nil
	}
	m.request++
	m.loadErr = nil
	m.refreshErr = nil
	m.loading = len(m.items) == 0
	m.refreshing = !m.loading
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	loader, id, instance, request := m.loader, m.id, m.instance, m.request
	return func() tea.Msg {
		defer cancel()
		items, err := loader(ctx)
		return itemsLoadedMsg{pickerID: id, instance: instance, request: request, items: items, err: err}
	}
}

func (m *Model) stopLoad() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.request++ // invalidate outstanding replies and refresh timers
	m.loading = false
	m.refreshing = false
}

// SetRefreshInterval enables periodic loading while the picker is visible.
// The next interval starts after a request finishes, so requests cannot pile up.
// A non-positive interval disables periodic refresh.
func (m *Model) SetRefreshInterval(interval time.Duration) { m.interval = interval }

func (m Model) nextRefresh() tea.Cmd {
	if m.interval <= 0 || m.loader == nil {
		return nil
	}
	instance, request := m.instance, m.request
	return tea.Tick(m.interval, func(time.Time) tea.Msg {
		return refreshMsg{instance: instance, request: request}
	})
}

// SetItems replaces the current item list and rebuilds the filter
// view. Resets cursor to 0 (callers that want to pre-seed the cursor
// call SetActive after).
func (m *Model) SetItems(items []Item) {
	m.items = items
	m.loading = false
	m.loadErr = nil
	m.cursor = 0
	m.scrollOffset = 0
	m.rebuildFiltered()
}

// SetActive remembers the current choice and moves the cursor to its row
// when present. The choice is restored on Open and after async loading.
func (m *Model) SetActive(id string) {
	m.activeID = id
	m.focusID(id)
}

func (m *Model) focusID(id string) bool {
	if id == "" {
		return false
	}
	for i, idx := range m.filtered {
		if m.items[idx].ID == id {
			m.cursor = i
			m.ensureCursorVisible()
			return true
		}
	}
	return false
}

// SetSubtitle adds context beneath the title on terminals with enough room.
func (m *Model) SetSubtitle(subtitle string) { m.subtitle = subtitle }

// SetHelp adds a caller-specific shortcut to the standard navigation hints.
func (m *Model) SetHelp(help string) { m.help = help }

// Hide dismisses the picker. Safe to call when already hidden.
func (m *Model) Hide() {
	m.visible = false
	m.stopLoad()
}

// Visible reports whether the picker is currently on-screen.
func (m Model) Visible() bool { return m.visible }

// ID returns the identifier passed to New. Lets the App disambiguate
// which picker is active when intercepting keys before Update.
func (m Model) ID() string { return m.id }

// CursorID returns the ID of the currently-highlighted row, or ""
// when the filtered list is empty. Useful for tests and for callers
// that want to mirror the cursor somewhere else (e.g. a preview).
func (m Model) CursorID() string {
	if len(m.filtered) == 0 {
		return ""
	}
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return ""
	}
	return m.items[m.filtered[m.cursor]].ID
}

// Loading reports whether the picker is currently waiting for its
// async loader to resolve.
func (m Model) Loading() bool { return m.loading }

// Refreshing reports whether cached choices are being refreshed in the background.
func (m Model) Refreshing() bool { return m.refreshing }

// RefreshErr reports a failed background refresh. Cached choices remain usable.
func (m Model) RefreshErr() error { return m.refreshErr }

// LoadErr returns the last loader error, if any. Nil in the success
// and initial states.
func (m Model) LoadErr() error { return m.loadErr }

// Resize stores the terminal dimensions. The picker computes its
// modal width/height off these each View().
func (m *Model) Resize(w, h int) {
	m.width = w
	m.height = h
	m.ensureCursorVisible()
}

// Update routes input while the picker is visible. Ignored entirely
// when hidden so the App can forward messages unconditionally.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	switch msg := msg.(type) {
	case refreshMsg:
		if msg.instance != m.instance || msg.request != m.request || m.interval <= 0 {
			return m, nil
		}
		cmd := m.beginLoad()
		return m, cmd
	case itemsLoadedMsg:
		if msg.pickerID != m.id || msg.instance != m.instance || msg.request != m.request {
			return m, nil
		}
		focused := m.CursorID()
		if focused == "" {
			focused = m.activeID
		}
		m.cancel = nil
		m.loading = false
		m.refreshing = false
		if msg.err != nil {
			if len(m.items) > 0 {
				m.refreshErr = msg.err
			} else {
				m.loadErr = msg.err
			}
			return m, m.nextRefresh()
		}
		m.loadErr = nil
		m.refreshErr = nil
		m.items = msg.items
		m.rebuildFiltered()
		if !m.focusID(focused) {
			m.clampCursor()
		}
		return m, m.nextRefresh()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handleKey is Update's KeyMsg branch, factored out for readability.
func (m Model) handleKey(km tea.KeyMsg) (Model, tea.Cmd) {
	key := km.String()

	// Error-state retry: only "r" (lowercase) re-fires the loader.
	if m.loadErr != nil && key == "r" && m.loader != nil {
		cmd := m.beginLoad()
		return m, cmd
	}

	switch key {
	case "ctrl+r":
		cmd := m.beginLoad()
		return m, cmd
	case "esc":
		m.Hide()
		id := m.id
		return m, func() tea.Msg { return CloseMsg{PickerID: id} }
	case "enter":
		if m.loading || m.loadErr != nil {
			return m, nil
		}
		if len(m.filtered) == 0 {
			return m, nil
		}
		item := m.items[m.filtered[m.cursor]]
		m.Hide()
		id := m.id
		return m, func() tea.Msg { return SelectMsg{PickerID: id, Item: item} }
	case "up", "ctrl+p", "ctrl+k":
		if m.cursor > 0 {
			m.cursor--
			m.ensureCursorVisible()
		}
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
			m.ensureCursorVisible()
		}
		return m, nil
	case "pgup":
		step := m.halfPage()
		m.cursor -= step
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureCursorVisible()
		return m, nil
	case "pgdown":
		step := m.halfPage()
		m.cursor += step
		if m.cursor > len(m.filtered)-1 {
			m.cursor = len(m.filtered) - 1
			if m.cursor < 0 {
				m.cursor = 0
			}
		}
		m.ensureCursorVisible()
		return m, nil
	case "home":
		m.cursor = 0
		m.ensureCursorVisible()
		return m, nil
	case "end":
		m.cursor = len(m.filtered) - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureCursorVisible()
		return m, nil
	case "backspace":
		if n := len(m.filter); n > 0 {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.rebuildFiltered()
			m.clampCursor()
		}
		return m, nil
	case "ctrl+u":
		if m.filter != "" {
			m.filter = ""
			m.rebuildFiltered()
			m.clampCursor()
		}
		return m, nil
	case " ", "space":
		// Space is a legitimate filter rune when typing "gpt 4".
		m.filter += " "
		m.rebuildFiltered()
		m.clampCursor()
		return m, nil
	}

	// Printable rune (no control modifier) → append to filter.
	if km.Type == tea.KeyRunes && len(km.Runes) > 0 {
		if isPrintable(km.Runes) {
			m.filter += string(km.Runes)
			m.rebuildFiltered()
			m.clampCursor()
		}
	}
	return m, nil
}

// isPrintable returns true when every rune is printable and not a
// control character. Guards the "any typed rune is filter input"
// branch from accidentally accepting bell or ESC.
func isPrintable(rs []rune) bool {
	for _, r := range rs {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// halfPage returns the pg-up/pg-down step size based on current height.
func (m Model) halfPage() int {
	// Use the viewport height of the list area — roughly height/2 per
	// the spec, clamped to at least 1 so a tiny terminal still moves.
	h := m.listHeight()
	if h < 2 {
		return 1
	}
	return h / 2
}

// clampCursor pulls cursor into the [0, len(filtered)-1] range and
// re-aligns scrollOffset so it remains visible.
func (m *Model) clampCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		m.scrollOffset = 0
		return
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureCursorVisible()
}

// ensureCursorVisible adjusts scrollOffset so cursor falls inside the
// list window. Direct-render scrolling (no viewport wrapper).
func (m *Model) ensureCursorVisible() {
	h := m.listHeight()
	if h <= 0 {
		m.scrollOffset = 0
		return
	}
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+h {
		m.scrollOffset = m.cursor - h + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	maxOffset := len(m.filtered) - h
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
}

// rebuildFiltered rebuilds the filtered-index slice from the current
// filter string. Called whenever items or filter change.
func (m *Model) rebuildFiltered() {
	m.filtered = m.filtered[:0]
	for i, it := range m.items {
		if matches(it, m.filter) {
			m.filtered = append(m.filtered, i)
		}
	}
}
