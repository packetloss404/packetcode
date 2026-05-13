package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (a *App) handleStatusLineCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 && args[0] != "refresh" {
		a.conversation.AppendSystem(fmt.Sprintf("statusline: unknown subcommand %q (want refresh)", args[0]))
		return a, nil
	}
	if a.statusLine == nil || !a.statusLine.Enabled() {
		a.conversation.AppendSystem("statusline: using built-in status bar (no [statusline].command configured)")
		return a, nil
	}
	if len(args) > 0 && args[0] == "refresh" {
		a.conversation.AppendSystem("statusline: refresh requested")
		return a, a.renderStatusLine(true)
	}
	if a.lastStatusLineErr != nil {
		a.conversation.AppendSystem("statusline: custom command active\nlast error: " + a.lastStatusLineErr.Error())
		return a, nil
	}
	line := strings.TrimSpace(a.topbar.CustomLine())
	if line == "" {
		line = "(waiting for command output)"
	}
	a.conversation.AppendSystem("statusline: custom command active\n" + line)
	return a, nil
}
