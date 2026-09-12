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
	"github.com/charmbracelet/lipgloss"

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
	// RequestID identifies the approver envelope this prompt was raised for.
	// The App refuses a decision whose id is no longer the displayed one, so a
	// prompt that was replaced between the keypress and the message cannot
	// have the user's answer applied to its successor.
	RequestID uint64
	// Remember is set when the user chose a session allowance — the App adds a
	// session permission rule so this tool (or command) isn't asked again.
	Remember bool
}

type Model struct {
	visible    bool
	tool       tools.Tool
	toolCall   provider.ToolCall
	requestID  uint64
	width      int
	result     Result
	queueDepth int
	cursor     int
}

func New() Model { return Model{} }

// Show makes the prompt visible for the given tool call. The caller
// should ensure no other modal is competing for input — App handles that.
func (m *Model) Show(tool tools.Tool, call provider.ToolCall) {
	m.tool = tool
	m.toolCall = call
	// Cleared, not carried over: a stale id would let this prompt's answer
	// resolve the envelope the previous one belonged to.
	m.requestID = 0
	m.visible = true
	m.result = Pending
	m.queueDepth = 1
	m.cursor = 0
}

// SetRequestID binds the visible prompt to the approver envelope it was
// raised for. Call it immediately after Show.
func (m *Model) SetRequestID(id uint64) { m.requestID = id }

// RequestID reports the envelope the visible prompt belongs to.
func (m Model) RequestID() uint64 { return m.requestID }

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
		case "up", "k", "shift+tab":
			m.cursor = (m.cursor + 2) % 3
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % 3
		case "enter":
			return m.resolveCursor()
		case "1", "y", "Y":
			return m.resolve(Approved, false)
		case "2", "a", "A":
			return m.resolve(Approved, true)
		case "3", "n", "N", "esc":
			return m.resolve(Rejected, false)
		}
	}
	return m, nil
}

func (m Model) resolveCursor() (Model, tea.Cmd) {
	switch m.cursor {
	case 0:
		return m.resolve(Approved, false)
	case 1:
		return m.resolve(Approved, true)
	default:
		return m.resolve(Rejected, false)
	}
}

func (m Model) resolve(result Result, remember bool) (Model, tea.Cmd) {
	m.result = result
	m.visible = false
	return m, emit(ResultMsg{Result: result, ToolCall: m.toolCall, RequestID: m.requestID, Remember: remember})
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
	padding := min(2, (width-1)/2)
	contentWidth := max(1, width-2*padding)
	bodyIndent := min(2, contentWidth-1)
	bodyWidth := max(1, contentWidth-bodyIndent)
	displayName := visibleApprovalText(m.tool.Name())
	if m.toolCall.Name != "" && m.toolCall.Name != displayName {
		displayName = visibleApprovalText(m.toolCall.Name)
	}
	// Renderers escape decoded text for display. Cleaning the JSON before
	// parsing misses escaped controls and can change the path being previewed.
	arguments := m.toolCall.Arguments
	source, action := splitApprovalDisplay(displayName)
	action = approvalActionLabel(action)
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
			Arguments: arguments,
			Width:     bodyWidth,
		})
	} else {
		body = summariseParams(arguments)
	}
	rememberLabel := "Allow this tool this session"
	scope := "Option 2 covers all arguments and paths for " + visibleApprovalText(m.tool.Name()) + "."
	if m.tool.Name() == "execute_command" {
		rememberLabel = "Allow exact command this session"
		scope = "Option 2 matches the exact command text, in any working directory."
	}
	if source != "" {
		scope += " Running jobs keep their existing permission policy."
	}
	choices := []string{"1. Allow once", "2. " + rememberLabel, "3. Reject this request"}
	for i, choice := range choices {
		prefix := "  "
		style := theme.StyleSecondary
		if i == m.cursor {
			prefix = "❯ "
			style = theme.StyleAccent
		}
		choices[i] = prefix + style.Render(choice)
	}
	question := theme.StylePrimary.Bold(true).Render("Allow this request?")
	footer := theme.StyleDim.Render("Esc rejects this request · ↑/↓ select · Enter confirms")
	revoke := theme.StyleDim.Render("/permissions reviews rules · /permissions reset revokes all session rules")
	body = lipgloss.NewStyle().Width(bodyWidth).Render(body)
	content := strings.Join([]string{header, "", indent(body, strings.Repeat(" ", bodyIndent)), "", question, strings.Join(choices, "\n"), theme.StyleDim.Render(scope), revoke, "", footer}, "\n")
	return lipgloss.NewStyle().Padding(0, padding).Width(width).Render(content)
}

func approvalActionLabel(action string) string {
	switch strings.TrimSpace(action) {
	case "execute_command":
		return "Shell command"
	case "write_file":
		return "Write file"
	case "patch_file":
		return "Edit file"
	default:
		return action
	}
}

func indent(body, prefix string) string {
	if body == "" {
		return prefix + theme.StyleDim.Render("(no details)")
	}
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
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
	return visibleApprovalText(trimmed)
}
