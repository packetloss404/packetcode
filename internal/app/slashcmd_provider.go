package app

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleProviderCommand opens the provider picker modal (0 args) or
// switches to a specific provider (1 arg). Bare /provider shares the
// openProviderPicker flow with Ctrl+P so users get a searchable modal
// including per-row API-key setup via ctrl+a.
func (a *App) handleProviderCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return a, a.openProviderPicker()
	}
	if args[0] == "add" {
		switch len(args) {
		case 1:
			a.conversation.AppendSystem("provider add: choose a provider, then press Ctrl+A to set or update its key")
			return a, a.openProviderPicker()
		case 2:
			return a, a.openProviderKeyPrompt(args[1])
		default:
			a.conversation.AppendSystem("provider add: usage: /provider add [slug]")
			return a, nil
		}
	}
	if err := a.applyProviderSwitch(args[0]); err != nil {
		a.conversation.AppendSystem("provider: " + err.Error())
	}
	return a, nil
}

// handleModelCommand opens the model picker modal (0 args) or switches
// to a specific model (1 arg). The picker open path is shared with the
// Ctrl+M keybind via openModelPicker — same async loader, cache reuse,
// and error surface.
func (a *App) handleModelCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		return a, a.openModelPicker()
	}
	if err := a.applyModelSwitch(args[0]); err != nil {
		a.conversation.AppendSystem("model: " + err.Error())
	}
	return a, nil
}

// renderProvidersTable builds the ASCII table shown by bare /provider.
// Fixed column widths: slug=10, name=14, default model=28, active=5.
func (a *App) renderProvidersTable() string {
	provs := a.deps.Registry.List()
	if len(provs) == 0 {
		return "no providers registered"
	}
	active, _ := a.deps.Registry.Active()
	activeSlug := ""
	if active != nil {
		activeSlug = active.Slug()
	}
	var b strings.Builder
	// Leading two spaces in the header accounts for the active marker
	// column ("* " or "  ") that prefixes each row.
	b.WriteString("  PROVIDER   NAME           DEFAULT MODEL                ACTIVE\n")
	for _, p := range provs {
		marker := "  "
		activeCol := "no"
		if p.Slug() == activeSlug {
			marker = "* "
			activeCol = "yes"
		}
		defModel := "(none)"
		if a.deps.Config != nil {
			if pc, ok := a.deps.Config.Providers[p.Slug()]; ok && pc.DefaultModel != "" {
				defModel = pc.DefaultModel
			}
		}
		fmt.Fprintf(&b, "%s%s %s %s %s\n",
			marker,
			padRight(trunc(p.Slug(), 10), 10),
			padRight(trunc(p.Name(), 14), 14),
			padRight(trunc(defModel, 28), 28),
			padRight(activeCol, 5),
		)
	}
	return strings.TrimRight(b.String(), "\n")
}
