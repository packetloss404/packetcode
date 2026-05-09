// Package autocomplete renders a small filter-as-you-type popup above
// the input bar whenever the user has typed "/" but not yet completed a
// verb with whitespace. The App drives open/close based on the input
// buffer; this component owns presentation, filter bucketing, and
// cursor navigation.
//
// Deliberately NOT a reuse of internal/ui/components/picker: the
// geometry is different (borderless-ish helper above the input, not a
// centred modal) and the accept / dismiss semantics live at the App
// layer (Tab and Enter coordinate with the input buffer). We reuse the
// picker's Normalize helper so "does the filter match the haystack?"
// stays in one place.
package autocomplete

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/ui/components/picker"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// Entry is a single slash-command row. Verb is the completion payload
// ("spawn", no slash); Usage is the display label ("/spawn <prompt>");
// Desc is the short description shown to the right.
type Entry struct {
	Verb  string
	Usage string
	Desc  string
}

// SelectMsg is reserved for symmetry with picker.SelectMsg, but the App
// currently handles Tab / Enter directly rather than routing a message.
// Kept exported so future refactors can switch to the message path
// without breaking callers.
type SelectMsg struct{ Verb string }

// Model is the popup state. Construct with New(entries); drive open /
// close via the helpers. The zero value is a valid hidden model.
type Model struct {
	entries  []Entry
	filter   string
	filtered []int
	cursor   int
	visible  bool
	width    int
}

// New returns a hidden Model seeded with the given entries. The entry
// list is the canonical order used when the filter is empty.
func New(entries []Entry) Model {
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	return Model{entries: cp}
}

// Open makes the popup visible and applies the given filter. Resets
// cursor to 0 (best match).
func (m *Model) Open(filter string) {
	m.visible = true
	m.SetFilter(filter)
}

// Close hides the popup. Safe to call when already hidden.
func (m *Model) Close() { m.visible = false }

// Visible reports whether the popup is currently on-screen.
func (m Model) Visible() bool { return m.visible }

// SetFilter applies a new filter string, rebuilds the bucketed filtered
// index, and resets the cursor to the top (best match). A leading "/"
// on the filter is stripped so callers can pass the raw input buffer.
func (m *Model) SetFilter(filter string) {
	m.filter = filter
	m.rebuild()
	m.cursor = 0
}

// Filter returns the current filter string (verbatim, with any leading
// "/" still present — we only strip it for matching).
func (m Model) Filter() string { return m.filter }

// Count reports how many entries currently pass the filter.
func (m Model) Count() int { return len(m.filtered) }

// SelectedVerb returns the verb under the cursor, or "" when the
// filtered list is empty.
func (m Model) SelectedVerb() string {
	if len(m.filtered) == 0 {
		return ""
	}
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return ""
	}
	return m.entries[m.filtered[m.cursor]].Verb
}

// SetWidth stores the current terminal width so View() can clamp the
// popup into [40, 60] columns.
func (m *Model) SetWidth(w int) { m.width = w }

// Update handles navigation keys (arrows / Ctrl+N/P / Ctrl+J/K). Tab,
// Enter, and Esc are deliberately NOT handled here — the App layer
// coordinates those with the input buffer.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "up", "ctrl+p", "ctrl+k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "ctrl+n", "ctrl+j":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	}
	return m, nil
}

// rebuild recomputes the filtered index using the two-tier prefix-first
// bucketing. Tier 1: entries whose verb starts with the filter. Tier 2:
// entries whose verb+desc substring-matches the filter.
func (m *Model) rebuild() {
	m.filtered = m.filtered[:0]
	needle := picker.Normalize(strings.TrimPrefix(m.filter, "/"))
	if needle == "" {
		// No filter → every entry in original (keymap) order.
		for i := range m.entries {
			m.filtered = append(m.filtered, i)
		}
		return
	}
	var tier1, tier2 []int
	for i, e := range m.entries {
		verbNorm := picker.Normalize(e.Verb)
		if strings.HasPrefix(verbNorm, needle) {
			tier1 = append(tier1, i)
			continue
		}
		hay := picker.Normalize(e.Verb + " " + e.Desc)
		if strings.Contains(hay, needle) {
			tier2 = append(tier2, i)
		}
	}
	sort.SliceStable(tier1, func(a, b int) bool {
		return m.entries[tier1[a]].Verb < m.entries[tier1[b]].Verb
	})
	sort.SliceStable(tier2, func(a, b int) bool {
		return m.entries[tier2[a]].Verb < m.entries[tier2[b]].Verb
	})
	m.filtered = append(m.filtered, tier1...)
	m.filtered = append(m.filtered, tier2...)
}

// View renders the popup. Returns "" when hidden OR when the filtered
// list is empty; the App submit path renders the friendly unknown-command
// message if the user sends that unmatched slash command.
func (m Model) View() string {
	if !m.visible || len(m.filtered) == 0 {
		return ""
	}
	outerW := m.clampedWidth()
	innerW := outerW - 4 // border (2) + padding (2)
	if innerW < 10 {
		innerW = 10
	}

	const maxRows = 6
	rowsShown := len(m.filtered)
	if rowsShown > maxRows {
		rowsShown = maxRows
	}
	// Scroll so the cursor stays visible inside the window.
	offset := 0
	if m.cursor >= rowsShown {
		offset = m.cursor - rowsShown + 1
	}

	usageW := innerW / 2
	if usageW > 22 {
		usageW = 22
	}
	if usageW < 8 {
		usageW = 8
	}
	// marker (3) + usage (usageW) + space (1) + desc (rest)
	descW := innerW - 3 - usageW - 1
	if descW < 4 {
		descW = 4
	}

	lines := make([]string, 0, rowsShown+1)
	for i := 0; i < rowsShown; i++ {
		idx := offset + i
		if idx >= len(m.filtered) {
			break
		}
		e := m.entries[m.filtered[idx]]
		lines = append(lines, m.renderRow(e, idx == m.cursor, usageW, descW))
	}
	if len(m.filtered) > maxRows {
		hidden := len(m.filtered) - maxRows
		footer := theme.StyleDim.Render(fmt.Sprintf("+%d more", hidden))
		lines = append(lines, footer)
	}

	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.BaseBorder).
		Padding(0, 1).
		Width(innerW + 2)
	return frame.Render(strings.Join(lines, "\n"))
}

// renderRow renders a single usage/desc line, prefixing a 3-column
// gutter for the cursor marker.
func (m Model) renderRow(e Entry, cursorOn bool, usageW, descW int) string {
	gutter := "  "
	if cursorOn {
		gutter = "▶ "
	}
	gutter += " " // trailing column so the total is 3

	usage := padOrTrunc(e.Usage, usageW)
	usageCol := theme.StyleAccent.Render(usage)
	desc := truncate(e.Desc, descW)
	descCol := theme.StyleSecondary.Render(desc)

	line := gutter + usageCol + " " + descCol
	if cursorOn {
		return lipgloss.NewStyle().Background(theme.BaseSurfaceBright).Render(line)
	}
	return line
}

// clampedWidth returns the outer popup width clamped to [40, 60] and
// never wider than m.width-4. Falls back to 40 when width is unset.
func (m Model) clampedWidth() int {
	if m.width <= 0 {
		return 40
	}
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	if w > 60 {
		w = 60
	}
	return w
}

// padOrTrunc returns s padded with spaces or truncated with an ellipsis
// so the result has exactly w runes. Used for fixed-width columns.
func padOrTrunc(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w >= 1 {
			return string(r[:w-1]) + "…"
		}
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-len(r))
}

// truncate clips s to at most w runes, adding an ellipsis on overflow.
func truncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	return string(r[:w-1]) + "…"
}
