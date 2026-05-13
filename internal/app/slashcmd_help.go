package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleHelpCommand renders every key group from keymap.go as a system
// message. Extra arguments are silently ignored so typos like
// "/help foo" still produce the help screen.
func (a *App) handleHelpCommand(_ []string) (tea.Model, tea.Cmd) {
	a.conversation.AppendSystem(a.renderHelp())
	return a, nil
}

func (a *App) renderHelp() string {
	out := renderHelpWithSlashCommands(a.slashHelpRows())
	if errs := a.slashRegistry().Errors(); len(errs) > 0 {
		var b strings.Builder
		b.WriteString(out)
		b.WriteString("\n\nSlash command load warnings\n")
		for _, err := range errs {
			writeHelpRow(&b, "warning", err)
		}
		out = strings.TrimRight(b.String(), "\n")
	}
	return out
}

// renderHelp iterates the five keymap groups in a stable order and
// concatenates them into a single monospace block. Keys column is 20
// characters wide; overflow wraps to the next line with an empty key
// slot so the descriptions stay aligned.
func renderHelp() string {
	return renderHelpWithSlashCommands(SlashCommands)
}

func renderHelpWithSlashCommands(slashRows []KeyHelp) string {
	sections := []struct {
		title string
		rows  []KeyHelp
	}{
		{"Global", GlobalKeys},
		{"Conversation", ConversationKeys},
		{"Approval", ApprovalKeys},
		{"Input", InputKeys},
		{"Autocomplete", AutocompleteKeys},
		{"Picker", PickerKeys},
		{"Slash commands", slashRows},
	}
	var b strings.Builder
	for i, sec := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(sec.title)
		b.WriteString("\n")
		for _, row := range sec.rows {
			writeHelpRow(&b, row.Key, row.Desc)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeHelpRow renders one key+description line. Keys longer than the
// 20-char column wrap onto their own line; the description then follows
// on a fresh line with an empty key slot so alignment holds.
func writeHelpRow(b *strings.Builder, key, desc string) {
	const keyCol = 20
	if runeLen(key) <= keyCol {
		b.WriteString(padRight(key, keyCol))
		b.WriteString("  ")
		b.WriteString(desc)
		b.WriteString("\n")
		return
	}
	// Overflow: print the key on its own line, then the description on
	// the next line aligned under the description column.
	b.WriteString(key)
	b.WriteString("\n")
	b.WriteString(padRight("", keyCol))
	b.WriteString("  ")
	b.WriteString(desc)
	b.WriteString("\n")
}

// padRight pads s with spaces to width n (rune-count, not byte-count,
// so multi-byte characters in key labels render correctly).
func padRight(s string, n int) string {
	ln := runeLen(s)
	if ln >= n {
		return s
	}
	return s + strings.Repeat(" ", n-ln)
}

func runeLen(s string) int {
	return len([]rune(s))
}
