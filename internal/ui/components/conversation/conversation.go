// Package conversation renders the transcript of a session. Each
// finalised message (user turn, assistant reply, tool call + result,
// system note) is committed to the terminal's native scrollback via
// tea.Println; the component itself only holds a single "pending" slot
// for the message currently being streamed or awaiting a tool result,
// which the App renders into its live region below the topbar.
//
// Design:
//   - Append* / Complete* / Finalise* mutate state and push a rendered
//     string onto the internal emits queue.
//   - The App calls DrainEmits() each Update tick and wraps each entry
//     in tea.Println so they land in terminal scrollback above the live
//     region.
//   - PendingView() returns the live-region render of whatever is
//     streaming or awaiting a tool result.
//   - View() is retained for tests: concatenates queued emits + pending.
package conversation

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/diff"
	"github.com/packetcode/packetcode/internal/ui/components/welcome"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// MessageKind discriminates how a Message renders.
type MessageKind int

const (
	KindUser MessageKind = iota
	KindAgent
	KindSystem
	KindToolCall
)

// Message is the conversation's atomic display unit. Tool calls and tool
// results are merged into a single block so the output reads linearly.
type Message struct {
	Kind    MessageKind
	Author  string
	Color   lipgloss.Color
	Content string

	// ToolCall fields populated when Kind == KindToolCall.
	ToolName   string
	ToolArgs   string
	ToolResult string
	IsError    bool
}

// Model is the conversation state: a pending in-flight message (if any)
// and a queue of rendered emits awaiting DrainEmits.
type Model struct {
	width   int
	height  int
	version string

	pending        *Message
	welcomePrinted bool

	// emits is the FIFO queue of rendered strings awaiting DrainEmits.
	// Production: drained each Update cycle and each entry becomes a
	// tea.Println that commits to terminal scrollback.
	emits []string
	// seen mirrors every entry that has ever passed through emit(), kept
	// for test harnesses that want to assert against the cumulative
	// transcript (production code never reads it). Unbounded growth is
	// acceptable because /clear replaces the whole Model.
	seen []string
}

// New constructs an empty conversation.
func New() Model {
	return Model{}
}

// SetVersion sets the version label used on the welcome splash.
func (m *Model) SetVersion(v string) { m.version = v }

// IsEmpty reports whether nothing has been emitted yet and no message is
// pending. Used by tests and by /clear to decide whether a splash is
// needed.
func (m *Model) IsEmpty() bool {
	return m.pending == nil && len(m.seen) == 0
}

// Resize records the terminal dimensions. Width is used by render
// helpers for wrapping; height is retained for API compatibility with
// the previous viewport-based model but is otherwise unused.
func (m *Model) Resize(width, height int) {
	m.width = width
	m.height = height
}

// EmitWelcomeSplash pushes the one-shot welcome splash onto the emits
// queue so the App's DrainEmits wrapper commits it to scrollback via
// tea.Println on the next Update cycle. No-op once already emitted, or
// when width is not yet known (defer until first WindowSizeMsg).
func (m *Model) EmitWelcomeSplash() {
	if m.welcomePrinted || m.width <= 0 {
		return
	}
	m.welcomePrinted = true
	m.emit(welcome.RenderInline(m.width, m.version))
}

// AppendUser commits a user message to scrollback.
func (m *Model) AppendUser(content string) {
	m.emit(renderMessage(Message{
		Kind:    KindUser,
		Author:  "You",
		Color:   theme.AccentPrimary,
		Content: content,
	}, m.contentWidth()))
}

// AppendQueuedUser commits an optimistic user message while another
// foreground operation is still running. The App later sends the same
// text without emitting a duplicate user bubble.
func (m *Model) AppendQueuedUser(content string) {
	m.emit(renderMessage(Message{
		Kind:    KindUser,
		Author:  "You (queued)",
		Color:   theme.Warning,
		Content: content,
	}, m.contentWidth()))
}

// AppendAgentText appends a streaming chunk to the pending agent
// message, creating it if absent. Not committed yet — the live region
// shows the in-progress render via PendingView.
func (m *Model) AppendAgentText(model, providerSlug, chunk string) {
	if m.pending != nil && m.pending.Kind == KindAgent {
		m.pending.Content += chunk
		return
	}
	m.flushPending()
	m.pending = &Message{
		Kind:    KindAgent,
		Author:  fmt.Sprintf("packetcode (%s)", model),
		Color:   theme.ProviderColor(providerSlug),
		Content: chunk,
	}
}

// FinaliseAgent commits the pending agent message (if any) to
// scrollback. Called after agent.EventDone.
func (m *Model) FinaliseAgent() {
	if m.pending != nil && m.pending.Kind == KindAgent {
		m.emit(renderMessage(*m.pending, m.contentWidth()))
		m.pending = nil
	}
}

// AppendToolCall starts a pending tool call. Awaits CompleteToolCall.
// If another message is pending, it is flushed first.
func (m *Model) AppendToolCall(toolName, args string) {
	if m.pending != nil && m.pending.Kind == KindAgent {
		m.pending = nil
	} else {
		m.flushPending()
	}
	m.pending = &Message{
		Kind:     KindToolCall,
		ToolName: toolName,
		ToolArgs: args,
	}
}

// CompleteToolCall fills in the tool result and commits the tool-call
// block to scrollback. Matches by name against the pending tool call.
// Silently no-ops if there's no matching pending call.
func (m *Model) CompleteToolCall(toolName string, res tools.ToolResult) {
	if m.pending == nil || m.pending.Kind != KindToolCall || m.pending.ToolName != toolName {
		return
	}
	m.pending.ToolResult = res.Content
	m.pending.IsError = res.IsError
	m.emit(renderMessage(*m.pending, m.contentWidth()))
	m.pending = nil
}

// AppendSystem commits a system note to scrollback.
func (m *Model) AppendSystem(content string) {
	m.emit(renderMessage(Message{Kind: KindSystem, Content: content}, m.contentWidth()))
}

// PendingView renders the current pending message for the live region.
// Returns "" when nothing is pending.
func (m Model) PendingView() string {
	if m.pending == nil {
		return ""
	}
	return renderMessage(*m.pending, m.contentWidth())
}

// DrainEmits returns the FIFO queue of finalised rendered messages and
// clears it. The App wraps each entry in tea.Println to commit to
// terminal scrollback.
func (m *Model) DrainEmits() []string {
	out := m.emits
	m.emits = nil
	return out
}

// View returns the cumulative transcript (every committed message ever
// emitted, whether or not it has been drained for tea.Println) plus the
// current pending message. Retained for test harnesses that snapshot
// the full conversation; production uses DrainEmits + tea.Println for
// committed content and PendingView for the live region.
func (m Model) View() string {
	if len(m.seen) == 0 && m.pending == nil {
		return ""
	}
	parts := make([]string, 0, len(m.seen)+1)
	parts = append(parts, m.seen...)
	if m.pending != nil {
		parts = append(parts, renderMessage(*m.pending, m.contentWidth()))
	}
	return strings.Join(parts, "\n")
}

// Update consumes Bubble Tea messages. Inline rendering relies on native
// terminal scrollback — no in-app scroll keys, no viewport — so this is
// a no-op. Kept so the component still participates in Bubble Tea's
// Update routing without needing a special case in the App.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) { return m, nil }

// emit pushes a rendered string onto the FIFO queue. No-op for empty
// strings (e.g. system message with empty content).
func (m *Model) emit(rendered string) {
	if rendered == "" {
		return
	}
	m.emits = append(m.emits, rendered)
	m.seen = append(m.seen, rendered)
}

// flushPending commits any pending message to scrollback. Used when a
// new pending slot is about to overwrite the current one (e.g. a tool
// call proposed while agent text was still streaming).
func (m *Model) flushPending() {
	if m.pending == nil {
		return
	}
	m.emit(renderMessage(*m.pending, m.contentWidth()))
	m.pending = nil
}

// contentWidth is the effective render width for a message bubble —
// terminal width minus a small gutter. Falls back to a sane default
// before the first WindowSizeMsg arrives.
func (m Model) contentWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return w - 2
}

// ────────────────────────────────────────────────────────────────────────────
// Per-message rendering
// ────────────────────────────────────────────────────────────────────────────

func renderMessage(msg Message, width int) string {
	switch msg.Kind {
	case KindUser:
		return renderBubble(msg.Author, msg.Color, msg.Content, theme.StyleUserMessage, width)
	case KindAgent:
		return renderBubble(msg.Author, msg.Color, msg.Content, theme.StyleAgentMessage, width)
	case KindSystem:
		if msg.Content == "" {
			return ""
		}
		w := width - 4
		if w < 10 {
			w = 10
		}
		return theme.StyleSystemMessage.Width(w).Render(msg.Content)
	case KindToolCall:
		return renderToolCall(msg, width)
	}
	return ""
}

func renderBubble(author string, color lipgloss.Color, body string, base lipgloss.Style, width int) string {
	header := lipgloss.NewStyle().Foreground(color).Bold(true).Render(author)
	content := header + "\n" + body
	return base.Width(width - 2).Render(content)
}

func renderToolCall(msg Message, width int) string {
	header := theme.LabelBadge(msg.ToolName, theme.AccentPrimary)
	args := truncate(msg.ToolArgs, 200)
	parts := []string{header + theme.StyleDim.Render("  "+args)}

	if msg.ToolResult != "" {
		divider := theme.StyleDim.Render(strings.Repeat("─", 24))
		parts = append(parts, divider, renderToolResultBody(msg, width-4))
	}
	return theme.StyleToolCall.Width(width - 2).Render(strings.Join(parts, "\n"))
}

func renderToolResultBody(msg Message, width int) string {
	if msg.IsError {
		return theme.StyleError.Render(msg.ToolResult)
	}
	if msg.ToolName == "patch_file" {
		if rendered, ok := tryRenderDiffResult(msg.ToolResult, width); ok {
			return rendered
		}
	}
	return msg.ToolResult
}

// tryRenderDiffResult looks for a unified-diff marker inside a tool
// result and, if found, renders everything after it via the diff
// component.
func tryRenderDiffResult(content string, width int) (string, bool) {
	idx := strings.Index(content, "--- ")
	if idx < 0 {
		idx = strings.Index(content, "@@ ")
	}
	if idx < 0 {
		return "", false
	}
	prefix := strings.TrimRight(content[:idx], "\n")
	m, err := diff.Parse(content[idx:])
	if err != nil || m.Empty() {
		return "", false
	}
	m = m.SetWidth(width).SetMaxRows(200)
	out := m.View()
	if prefix != "" {
		return theme.StyleDim.Render(prefix) + "\n" + out, true
	}
	return out, true
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
