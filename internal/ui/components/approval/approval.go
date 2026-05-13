// Package approval renders the destructive-action confirmation prompt
// the agent loop pauses on for write_file, patch_file, and execute_command.
//
// The component is presentation-only: it has no opinions about
// approver bookkeeping. The App shell wires the result back to the
// agent's Approver.
package approval

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// Result is what bubbles out of the approval prompt's Update.
type Result int

const (
	Pending Result = iota
	Approved
	Rejected
)

// ResultMsg is the tea.Msg the App listens for to know an approval has
// resolved. It carries a copy of the originating tool call so the App
// can route the decision to the right pending Approver call.
type ResultMsg struct {
	Result   Result
	ToolCall provider.ToolCall
}

type Model struct {
	visible    bool
	tool       tools.Tool
	toolCall   provider.ToolCall
	width      int
	result     Result
	queueDepth int
}

func New() Model { return Model{} }

// Show makes the prompt visible for the given tool call. The caller
// should ensure no other modal is competing for input — App handles that.
func (m *Model) Show(tool tools.Tool, call provider.ToolCall) {
	m.tool = tool
	m.toolCall = call
	m.visible = true
	m.result = Pending
	m.queueDepth = 1
}

func (m *Model) Hide()         { m.visible = false }
func (m *Model) Visible() bool { return m.visible }

func (m *Model) SetWidth(w int) { m.width = w }

func (m *Model) SetQueueDepth(n int) {
	if n < 1 {
		n = 1
	}
	m.queueDepth = n
}

// Update handles approve/reject keys.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y", "Y":
			m.result = Approved
			m.visible = false
			return m, emit(ResultMsg{Result: Approved, ToolCall: m.toolCall})
		case "n", "N", "esc":
			m.result = Rejected
			m.visible = false
			return m, emit(ResultMsg{Result: Rejected, ToolCall: m.toolCall})
		}
	}
	return m, nil
}

func emit(msg ResultMsg) tea.Cmd {
	return func() tea.Msg { return msg }
}

func (m Model) View() string {
	if !m.visible || m.tool == nil {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	displayName := m.tool.Name()
	if m.toolCall.Name != "" && m.toolCall.Name != displayName {
		displayName = m.toolCall.Name
	}
	source, action := splitApprovalDisplay(displayName)
	headerText := action
	if source != "" {
		headerText = source + " · " + action
	}
	header := theme.LabelBadge(headerText, theme.Warning)
	if m.queueDepth > 1 {
		header += " " + theme.StyleDim.Render(fmt.Sprintf("1 of %d pending approvals", m.queueDepth))
	}
	var body string
	if r, ok := renderers[m.tool.Name()]; ok {
		body = r(RenderContext{
			Tool:      m.tool,
			Arguments: m.toolCall.Arguments,
			Width:     width - 4,
		})
	} else {
		body = summariseParams(m.toolCall.Arguments)
	}
	actions := strings.Join([]string{
		theme.StyleAccent.Render("[Y]") + theme.StylePrimary.Render(" Approve"),
		theme.StyleAccent.Render("[N]") + theme.StylePrimary.Render(" Reject"),
		theme.StyleDim.Render("[Esc] Cancel"),
	}, "   ")

	content := strings.Join([]string{header, "", body, "", actions}, "\n")
	return theme.StyleApprovalPrompt.Width(width - 4).Render(content)
}

func splitApprovalDisplay(name string) (source, action string) {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "[job:") {
		if end := strings.Index(name, "]"); end >= 0 {
			return name[:end+1], strings.TrimSpace(name[end+1:])
		}
	}
	return "", name
}

// summariseParams renders the tool's JSON arguments as a readable two-line
// preview. We deliberately show the full JSON for now — the design system
// proposes specialised renderings (diffs for write_file, $ for execute)
// which are post-MVP polish.
func summariseParams(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return theme.StyleDim.Render("(no parameters)")
	}
	var pretty any
	if err := json.Unmarshal([]byte(trimmed), &pretty); err == nil {
		buf, _ := json.MarshalIndent(pretty, "", "  ")
		return theme.StylePrimary.Render(string(buf))
	}
	return fmt.Sprintf("%s", trimmed)
}
