// Package jobs renders the `/jobs <id>` transcript modal: a bordered
// overlay showing one background agent's full message history,
// including user prompts, assistant replies, and tool calls.
//
// The component is presentation-only: it does not call into
// jobs.Manager. Callers (App) resolve the Snapshot and transcript via
// jobs.Manager.Get / Transcript and pass them to Show(). Esc or `q`
// closes the panel; `j/k` / `up/down` scroll.
//
// Styling deliberately mirrors the conversation pane so the transcript
// feels familiar — user messages in an accent-bordered bubble,
// assistant messages in a neutral-bordered bubble, tool messages as a
// dim `[tool:<name>]` line. The outer frame uses a rounded cyan
// border (theme.AccentPrimary) to distinguish it as a modal.
package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// Model is the modal component. Construct with New(); show with
// Show(snap, msgs). Keep as a value type (not a pointer) to match the
// Bubble Tea component conventions already used by approval and
// spinner.
type Model struct {
	visible bool
	snap    jobs.Snapshot
	msgs    []provider.Message
	vp      viewport.Model
	width   int
	height  int
	mode    string
	title   string
	meta    string
}

// New returns an empty, hidden Model. The caller typically holds it as
// a field on the App struct and toggles visibility via Show / Hide.
func New() Model {
	vp := viewport.New(0, 0)
	return Model{vp: vp}
}

// Show makes the panel visible for the given job snapshot and its
// transcript. The transcript slice is copied defensively so later
// mutations by the caller don't race with rendering.
func (m *Model) Show(snap jobs.Snapshot, msgs []provider.Message) {
	m.snap = snap
	m.msgs = append([]provider.Message(nil), msgs...)
	m.mode = "job"
	m.title = ""
	m.meta = ""
	m.visible = true
	m.refresh()
}

// ShowSession opens the same transcript viewer for the active chat
// session. The header stays outside the viewport, which gives the
// modal a useful sticky anchor while the body scrolls.
func (m *Model) ShowSession(title, meta string, msgs []provider.Message) {
	m.snap = jobs.Snapshot{}
	m.msgs = append([]provider.Message(nil), msgs...)
	m.mode = "session"
	m.title = title
	m.meta = meta
	m.visible = true
	m.refresh()
}

// Hide closes the panel. No-op if already hidden.
func (m *Model) Hide() { m.visible = false }

// Visible reports whether the panel is currently on-screen. Used by
// the App to gate overlay precedence and message routing.
func (m Model) Visible() bool { return m.visible }

// Resize updates the outer frame dimensions. Called by App.resize when
// the terminal is resized. The inner viewport sizes itself against
// these bounds on the next View() call.
func (m *Model) Resize(w, h int) {
	m.width = w
	m.height = h
	// Leave room for the border (2) + padding (2) + header (3) + footer (1).
	innerW := w - 4
	if innerW < 10 {
		innerW = 10
	}
	innerH := h - 6
	if innerH < 3 {
		innerH = 3
	}
	m.vp.Width = innerW
	m.vp.Height = innerH
	m.refresh()
}

// Update handles modal-local input: Esc / q close the panel; j/k and
// arrow keys scroll the viewport.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc", "q", "Q":
			m.visible = false
			return m, nil
		case "j", "down":
			m.vp.LineDown(1)
			return m, nil
		case "k", "up":
			m.vp.LineUp(1)
			return m, nil
		case "g":
			m.vp.GotoTop()
			return m, nil
		case "G":
			m.vp.GotoBottom()
			return m, nil
		case "pgdown", " ":
			m.vp.HalfViewDown()
			return m, nil
		case "pgup":
			m.vp.HalfViewUp()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// View renders the modal. Returns "" when hidden so the layout
// package's overlay slot can elide it. The outer frame uses a rounded
// cyan border to match the design system (AccentPrimary, not purple).
func (m Model) View() string {
	if !m.visible {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}

	header := m.renderHeader()
	body := m.vp.View()
	footer := theme.StyleDim.Render(m.renderFooter())

	content := strings.Join([]string{header, body, footer}, "\n")

	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.AccentPrimary).
		Padding(0, 1)

	return frame.Width(width - 4).Render(content)
}

// renderHeader builds the single-line modal header:
//
//	[job:7f3a] state · provider/model · age · $cost
//
// Each field is rendered in the theme's secondary-text style so the
// ID stays the visual anchor.
func (m Model) renderHeader() string {
	if m.mode == "session" {
		title := strings.TrimSpace(m.title)
		if title == "" {
			title = "current session"
		}
		head := theme.StyleAccent.Render(title)
		meta := strings.TrimSpace(m.meta)
		if meta == "" {
			return head
		}
		return head + " " + theme.StyleSecondary.Render(meta)
	}
	s := m.snap
	prov := s.Provider
	if s.Model != "" {
		if prov != "" {
			prov += "/" + s.Model
		} else {
			prov = s.Model
		}
	}
	age := ""
	if !s.CreatedAt.IsZero() {
		ref := s.FinishedAt
		if ref.IsZero() {
			ref = time.Now()
		}
		age = shortDuration(ref.Sub(s.CreatedAt))
	}
	state := s.State.String()
	id := theme.StyleAccent.Render(fmt.Sprintf("[job:%s]", s.ID))
	rest := theme.StyleSecondary.Render(fmt.Sprintf(
		"%s · %s · %s · $%.4f",
		state, prov, age, s.CostUSD,
	))
	header := id + " " + rest
	if wt := m.renderWorktreeLine(); wt != "" {
		header += "\n" + theme.StyleDim.Render(wt)
	}
	return header
}

func (m Model) renderWorktreeLine() string {
	s := m.snap
	if s.WorktreePath == "" {
		if s.AllowWrite && s.WorktreeNote != "" {
			return "worktree unavailable: " + s.WorktreeNote
		}
		return ""
	}
	parts := []string{"worktree: " + s.WorktreePath}
	if s.WorktreeBranch != "" {
		parts = append(parts, "branch "+s.WorktreeBranch)
	}
	if s.WorktreeBase != "" {
		parts = append(parts, "base "+s.WorktreeBase)
	}
	return strings.Join(parts, " · ")
}

func (m Model) renderFooter() string {
	total := m.vp.TotalLineCount()
	if total <= m.vp.Height || m.vp.Height <= 0 {
		return "Esc / q close · j/k scroll · G newest"
	}
	top := m.vp.YOffset + 1
	bottom := m.vp.YOffset + m.vp.Height
	if bottom > total {
		bottom = total
	}
	return fmt.Sprintf("Esc / q close · j/k scroll · g top · G newest · lines %d-%d/%d", top, bottom, total)
}

// refresh re-renders the transcript into the inner viewport. Called
// from Show and Resize.
func (m *Model) refresh() {
	if m.vp.Width <= 0 {
		return
	}
	var b strings.Builder
	contentW := m.vp.Width
	if m.mode == "job" {
		if manifest := jobs.ArtifactManifest(m.snap.Artifacts, 12); manifest != "" {
			b.WriteString(theme.StyleSecondary.Bold(true).Render("artifacts"))
			b.WriteByte('\n')
			b.WriteString(theme.StyleDim.Render(manifest))
			if len(m.msgs) > 0 {
				b.WriteString("\n\n")
			}
		}
	}
	for i, msg := range m.msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderMessage(msg, contentW))
	}
	if len(m.msgs) == 0 {
		b.WriteString(theme.StyleDim.Render("(no messages yet)"))
	}
	m.vp.SetContent(b.String())
	m.vp.GotoBottom()
}

// renderMessage formats a single provider.Message for the modal body.
// User messages get an accent-bordered bubble, assistant messages a
// neutral-bordered bubble, tool messages a dim inline line, and system
// messages italic secondary text.
func renderMessage(msg provider.Message, width int) string {
	switch msg.Role {
	case provider.RoleUser:
		return renderBubble("user", theme.AccentPrimary, msg.Content, theme.StyleUserMessage, width)
	case provider.RoleAssistant:
		content := strings.TrimSpace(msg.Content)
		if content == "" && len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Name)
			}
			content = theme.StyleDim.Render(
				fmt.Sprintf("(tool calls: %s)", strings.Join(names, ", ")),
			)
		}
		return renderBubble("assistant", theme.TextPrimary, content, theme.StyleAgentMessage, width)
	case provider.RoleTool:
		name := msg.Name
		if name == "" {
			name = "tool"
		}
		head := theme.StyleDim.Render(fmt.Sprintf("[tool:%s]", name))
		body := strings.TrimSpace(msg.Content)
		if body == "" {
			return head
		}
		return head + "\n" + theme.StyleDim.Render(body)
	case provider.RoleSystem:
		return theme.StyleSystemMessage.Render(msg.Content)
	}
	// Unknown role: render the content as secondary text so we never
	// drop it silently.
	return theme.StyleSecondary.Render(msg.Content)
}

// renderBubble mirrors conversation.renderBubble so the transcript
// styling matches the main pane.
func renderBubble(author string, color lipgloss.Color, body string, base lipgloss.Style, width int) string {
	header := lipgloss.NewStyle().Foreground(color).Bold(true).Render(author)
	content := header + "\n" + body
	w := width - 2
	if w < 8 {
		w = 8
	}
	return base.Width(w).Render(content)
}

// shortDuration renders a duration as "12s" / "1m03s" — same shape as
// the inline job notifications in the conversation pane.
func shortDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}
