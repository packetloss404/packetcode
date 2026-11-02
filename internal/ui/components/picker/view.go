package picker

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/packetcode/packetcode/internal/ui/terminaltext"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// Geometry includes the entire frame, including its border and padding.
func (m Model) modalWidth() int {
	if m.width <= 0 {
		return 80
	}
	return min(m.width, min(88, max(60, m.width*4/5)))
}

func (m Model) compact() bool { return m.height > 0 && m.height < 22 }

func (m Model) rowHeight() int {
	if m.compact() {
		return 1
	}
	return 2
}

func (m Model) chromeHeight() int {
	if m.compact() {
		return 5 // title, search, footer, and borders
	}
	h := 7 // title, search, two dividers, footer, and borders
	if m.subtitle != "" {
		h++
	}
	return h
}

func (m Model) listHeight() int {
	limit := 24
	if m.height > 0 {
		// Leave room for the input and status beneath this inline modal.
		limit = max(m.chromeHeight()+m.rowHeight(), min(limit, m.height-6))
	}
	capacity := max(1, (limit-m.chromeHeight())/m.rowHeight())
	// Filtering should not make the whole modal jump up and down.
	return min(capacity, max(3, len(m.items)))
}

func (m Model) modalHeight() int {
	return m.chromeHeight() + m.listHeight()*m.rowHeight()
}

func (m Model) View() string {
	if !m.visible {
		return ""
	}
	w := max(1, m.modalWidth()-4)
	count := fmt.Sprintf("%d available", len(m.items))
	if strings.TrimSpace(m.filter) != "" {
		count = fmt.Sprintf("%d / %d matches", len(m.filtered), len(m.items))
	}
	if m.loading {
		count = "loading…"
	} else if m.loadErr != nil {
		count = "unavailable"
	} else if m.refreshing {
		count += " · refreshing…"
	} else if m.refreshErr != nil {
		count = "refresh failed · cached"
	}
	lines := []string{columns(theme.StylePrimary.Bold(true).Render(singleLine(m.title)), theme.StyleSecondary.Render(count), w, lipgloss.NewStyle())}
	if m.subtitle != "" && !m.compact() {
		lines = append(lines, theme.StyleSecondary.Render(truncate(singleLine(m.subtitle), w)))
	}
	lines = append(lines, m.renderSearch(w))
	if !m.compact() {
		lines = append(lines, m.divider(w, ""))
	}
	switch {
	case m.loading:
		lines = append(lines, m.renderState(w, "loading…", "Fetching available choices", theme.StyleAccent))
	case m.loadErr != nil:
		lines = append(lines, m.renderState(w, "Could not load choices", "error: "+singleLine(m.loadErr.Error()), theme.StyleError))
	case len(m.filtered) == 0:
		label, hint := "No choices available", "Try again later"
		if strings.TrimSpace(m.filter) != "" {
			label, hint = "no matches", "Try another search or Ctrl+U to clear"
		}
		lines = append(lines, m.renderState(w, label, hint, theme.StyleSecondary))
	default:
		lines = append(lines, m.renderList(w))
	}
	if !m.compact() {
		rangeLabel := ""
		if len(m.filtered) > m.listHeight() && !m.loading && m.loadErr == nil {
			rangeLabel = fmt.Sprintf(" %d–%d of %d ", m.scrollOffset+1, min(m.scrollOffset+m.listHeight(), len(m.filtered)), len(m.filtered))
		}
		lines = append(lines, m.divider(w, rangeLabel))
	}
	lines = append(lines, theme.StyleSecondary.Render(m.footer(w)))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.BaseBorderBright).
		Padding(0, 1).
		Width(w + 2).
		Render(strings.Join(lines, "\n"))
}

func (m Model) renderSearch(w int) string {
	style := lipgloss.NewStyle().Background(theme.BaseSurfaceBright)
	prompt := style.Foreground(theme.AccentPrimary).Bold(true).Render("› ")
	text := "Search by name or details…"
	if m.filter != "" {
		text = m.filter
		if ansi.StringWidth(text) > w-3 {
			text = ansi.TruncateLeft(text, ansi.StringWidth(text)-max(1, w-4), "…")
		}
		text += "▏"
	}
	color := theme.TextSecondary
	if m.filter != "" {
		color = theme.TextPrimary
	}
	return style.Width(w).Render(truncate(prompt+style.Foreground(color).Render(text), w))
}

func (m Model) divider(w int, label string) string {
	label = truncate(label, w)
	return lipgloss.NewStyle().Foreground(theme.BaseBorderBright).Render(strings.Repeat("─", max(0, w-ansi.StringWidth(label))) + label)
}

func (m Model) footer(w int) string {
	if m.loadErr != nil {
		return truncate("r retry · Esc close", w)
	}
	if m.loading {
		return truncate("Esc close", w)
	}
	hint := "↑↓ move · Enter select · Esc close"
	help := m.help
	if m.refreshErr != nil {
		help = "Ctrl+R retry"
	}
	if help != "" {
		hint += " · " + help
	}
	if strings.TrimSpace(m.filter) != "" && ansi.StringWidth(hint+" · Ctrl+U clear") <= w {
		hint += " · Ctrl+U clear"
	}
	if ansi.StringWidth(hint) > w {
		hint = "↑↓ · Enter · Esc"
		if help != "" && ansi.StringWidth(hint+" · "+help) <= w {
			hint += " · " + help
		}
	}
	return truncate(hint, w)
}

func (m Model) renderState(w int, label, hint string, style lipgloss.Style) string {
	h := m.listHeight() * m.rowHeight()
	lines := make([]string, h)
	line := max(0, (h-2)/2)
	if h == 1 {
		lines[0] = truncate(style.Render(label)+" · "+theme.StyleSecondary.Render(hint), w)
		return lines[0]
	}
	lines[line] = lipgloss.PlaceHorizontal(w, lipgloss.Center, style.Render(truncate(label, w)))
	lines[line+1] = lipgloss.PlaceHorizontal(w, lipgloss.Center, theme.StyleSecondary.Render(truncate(hint, w)))
	return strings.Join(lines, "\n")
}

func (m Model) renderList(w int) string {
	rows := make([]string, 0, m.listHeight())
	end := min(m.scrollOffset+m.listHeight(), len(m.filtered))
	for i := m.scrollOffset; i < end; i++ {
		rows = append(rows, m.renderRow(m.items[m.filtered[i]], i == m.cursor, w))
	}
	for len(rows) < m.listHeight() {
		rows = append(rows, strings.Repeat("\n", m.rowHeight()-1))
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderRow(it Item, focused bool, w int) string {
	base := lipgloss.NewStyle()
	if focused {
		base = base.Background(theme.BaseSurfaceBright)
	}
	labelStyle := base.Foreground(theme.TextPrimary).Bold(focused)
	detailStyle := base.Foreground(theme.TextSecondary)
	markerColor := theme.TextDim
	if it.Color != "" {
		markerColor = it.Color
	}
	marker := singleLine(it.Marker)
	if marker == "" {
		marker = "·"
	}
	cursor := "  "
	if focused {
		cursor = base.Foreground(theme.AccentPrimary).Render("› ")
	}
	prefix := cursor + base.Foreground(markerColor).Render(truncate(marker, 1)) + base.Render(" ")
	status := singleLine(it.Status)
	statusStyle := base.Foreground(theme.Warning)
	if it.Marker == "●" {
		status = "current"
		statusStyle = base.Foreground(theme.AccentPrimary)
	}
	label := labelStyle.Render(singleLine(it.Label))
	line := prefix + columns(label, statusStyle.Render(status), max(1, w-4), base)
	if m.compact() {
		// Details share the row only when there is room to keep the name intact.
		if status == "" && ansi.StringWidth(prefix+label)+12 < w {
			line = columns(prefix+label, detailStyle.Render(singleLine(it.Detail)), w, base)
		}
	} else {
		line += "\n" + base.Width(w).Render("    "+detailStyle.Render(truncate(singleLine(it.Detail), max(0, w-4))))
	}
	return base.Width(w).Render(line)
}

// columns gives the primary text all remaining space, rather than reserving
// a fixed fraction that clips long model IDs even in a wide terminal.
func columns(left, right string, w int, fill lipgloss.Style) string {
	rightW := ansi.StringWidth(right)
	if rightW == 0 || w-rightW-2 < 12 {
		return padOrTrunc(left, w, fill)
	}
	return padOrTrunc(left, w-rightW-2, fill) + fill.Render("  ") + right
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(terminaltext.Clean(s)), " ")
}

// Terminal cell widths keep ANSI styles, wide glyphs, and graphemes intact.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

func padOrTrunc(s string, w int, fill lipgloss.Style) string {
	s = truncate(s, w)
	return s + fill.Render(strings.Repeat(" ", max(0, w-ansi.StringWidth(s))))
}
