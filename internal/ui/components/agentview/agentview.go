// Package agentview renders a selectable overview of background-agent jobs.
//
// The component is presentation-only: callers pass jobs.Snapshot values in and
// listen for typed Bubble Tea messages when the user asks to close, peek, open,
// cancel, or inject a job result.
package agentview

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	jobspkg "github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Job is the display projection Agent View needs. App code can map this from
// jobs.Snapshot without coupling this component to manager internals.
type Job struct {
	ID, ParentJobID, Prompt, Provider, Model string
	State                                    string
	ResultStatus                             string
	Summary, Error                           string
	LastActivity, LastMessage                string
	CreatedAt, UpdatedAt, FinishedAt         time.Time
	Tokens                                   struct{ Input, Output int }
	CostUSD                                  float64
	Depth                                    int
	NeedsInput, NeedsApproval                bool
}

// CloseMsg is emitted when the user dismisses the agent view.
type CloseMsg struct{}

// PeekMsg is emitted when the user requests a lightweight preview of a job.
type PeekMsg struct{ JobID string }

// OpenMsg is emitted when the user requests the full job view.
type OpenMsg struct{ JobID string }

// CancelMsg is emitted when the user requests cancellation for a job.
type CancelMsg struct{ JobID string }

// InjectMsg is emitted when the user requests injecting a job result into the
// foreground conversation context.
type InjectMsg struct{ JobID string }

type group int

const (
	groupActive group = iota
	groupCompleted
	groupFailed
	groupCancelled
)

type rowKind int

const (
	rowHeader rowKind = iota
	rowJob
)

type row struct {
	kind  rowKind
	group group
	job   Job
}

// Model is the Agent View list component. It follows the existing value-return
// Update convention used by the other Bubble Tea components in this repo.
type Model struct {
	visible      bool
	title        string
	jobs         []Job
	rows         []row
	cursor       int
	scrollOffset int
	width        int
	height       int
}

// New returns an empty, hidden Agent View.
func New() Model {
	return Model{title: "Agent View", cursor: -1}
}

// Show makes the component visible and replaces its job list. It accepts either
// []Job or []jobs.Snapshot so callers can use the component directly from the
// manager while tests can build lightweight display rows.
func (m *Model) Show(items any) {
	m.visible = true
	m.SetJobs(items)
}

// Hide closes the component. It is safe to call when already hidden.
func (m *Model) Hide() { m.visible = false }

// Visible reports whether the component should currently be rendered.
func (m Model) Visible() bool { return m.visible }

// Resize stores terminal dimensions for row clipping and scrolling.
func (m *Model) Resize(w, h int) {
	m.width = w
	m.height = h
	m.ensureCursorVisible()
}

// SetJobs replaces the displayed jobs. Selection is preserved by job ID when
// possible, otherwise it moves to the first selectable row.
func (m *Model) SetJobs(items any) {
	selected := m.SelectedID()
	jobs := normalizeJobs(items)
	m.jobs = append(m.jobs[:0], jobs...)
	m.rebuildRows()
	if selected != "" && m.selectID(selected) {
		return
	}
	m.cursor = m.firstJobRow()
	m.ensureCursorVisible()
}

func normalizeJobs(items any) []Job {
	switch v := items.(type) {
	case nil:
		return nil
	case []Job:
		out := make([]Job, len(v))
		copy(out, v)
		return out
	case []jobspkg.Snapshot:
		out := make([]Job, len(v))
		for i, s := range v {
			out[i] = fromSnapshot(s)
		}
		return out
	default:
		return nil
	}
}

func fromSnapshot(s jobspkg.Snapshot) Job {
	j := Job{
		ID:            s.ID,
		ParentJobID:   s.ParentJobID,
		Prompt:        s.Prompt,
		Provider:      s.Provider,
		Model:         s.Model,
		State:         s.State.String(),
		ResultStatus:  s.ResultStatus.String(),
		Summary:       s.Summary,
		Error:         s.Error,
		LastActivity:  s.LastActivity,
		LastMessage:   s.LastMessage,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
		FinishedAt:    s.FinishedAt,
		CostUSD:       s.CostUSD,
		Depth:         s.Depth,
		NeedsInput:    s.NeedsInput,
		NeedsApproval: s.NeedsApproval,
	}
	j.Tokens.Input = s.Tokens.Input
	j.Tokens.Output = s.Tokens.Output
	return j
}

// SelectedID returns the ID under the cursor, or "" when there is no selectable
// row.
func (m Model) SelectedID() string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ""
	}
	r := m.rows[m.cursor]
	if r.kind != rowJob {
		return ""
	}
	return r.job.ID
}

// Update handles list navigation and emits action messages for the selected
// job.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "q", "Q":
		m.visible = false
		return m, emit(CloseMsg{})
	case "up", "k", "ctrl+p":
		m.move(-1)
		return m, nil
	case "down", "j", "ctrl+n":
		m.move(1)
		return m, nil
	case "home", "g":
		m.cursor = m.firstJobRow()
		m.ensureCursorVisible()
		return m, nil
	case "end", "G":
		m.cursor = m.lastJobRow()
		m.ensureCursorVisible()
		return m, nil
	case "pgup":
		m.page(-1)
		return m, nil
	case "pgdown", " ":
		m.page(1)
		return m, nil
	case "p", "P":
		return m, m.emitForSelection(func(id string) tea.Msg { return PeekMsg{JobID: id} })
	case "enter", "o", "O":
		return m, m.emitForSelection(func(id string) tea.Msg { return OpenMsg{JobID: id} })
	case "c", "C":
		return m, m.emitForSelection(func(id string) tea.Msg { return CancelMsg{JobID: id} })
	case "i", "I":
		return m, m.emitForSelection(func(id string) tea.Msg { return InjectMsg{JobID: id} })
	}
	return m, nil
}

// View renders the grouped job table. Returns "" while hidden so callers can
// wire it into an overlay slot without extra branching.
func (m Model) View() string {
	if !m.visible {
		return ""
	}
	w := m.modalWidth()
	innerW := w - 4
	if innerW < 20 {
		innerW = 20
	}

	title := theme.StyleAccent.Render(m.title)
	meta := theme.StyleSecondary.Render(fmt.Sprintf("%d jobs", len(m.jobs)))
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, theme.StyleDim.Render("  "), meta)
	body := m.renderRows(innerW)
	footer := theme.StyleDim.Render("↑/↓ move · p peek · enter open · c cancel · i inject · Esc close")

	content := strings.Join([]string{header, body, footer}, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.AccentPrimary).
		Padding(0, 1).
		Width(innerW + 2).
		Render(content)
}

func emit(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

func (m Model) emitForSelection(fn func(string) tea.Msg) tea.Cmd {
	id := m.SelectedID()
	if id == "" {
		return nil
	}
	return emit(fn(id))
}

func (m *Model) rebuildRows() {
	groups := map[group][]Job{}
	for _, j := range m.jobs {
		groups[groupForState(j.State)] = append(groups[groupForState(j.State)], j)
	}

	order := []group{groupActive, groupCompleted, groupFailed, groupCancelled}
	m.rows = m.rows[:0]
	for _, g := range order {
		items := groups[g]
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			return newestTime(items[i]).After(newestTime(items[j]))
		})
		m.rows = append(m.rows, row{kind: rowHeader, group: g})
		for _, job := range items {
			m.rows = append(m.rows, row{kind: rowJob, group: g, job: job})
		}
	}
	if len(m.rows) == 0 {
		m.cursor = -1
		m.scrollOffset = 0
	}
}

func groupForState(s string) group {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StateCompleted, "done", "success", "succeeded":
		return groupCompleted
	case StateFailed, "error":
		return groupFailed
	case StateCancelled, "canceled":
		return groupCancelled
	default:
		return groupActive
	}
}

func groupLabel(g group) string {
	switch g {
	case groupActive:
		return "Active"
	case groupCompleted:
		return "Completed"
	case groupFailed:
		return "Failed"
	case groupCancelled:
		return "Cancelled"
	default:
		return "Jobs"
	}
}

func (m *Model) selectID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range m.rows {
		if r.kind == rowJob && r.job.ID == id {
			m.cursor = i
			m.ensureCursorVisible()
			return true
		}
	}
	return false
}

func (m Model) firstJobRow() int {
	for i, r := range m.rows {
		if r.kind == rowJob {
			return i
		}
	}
	return -1
}

func (m Model) lastJobRow() int {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if m.rows[i].kind == rowJob {
			return i
		}
	}
	return -1
}

func (m *Model) move(delta int) {
	if m.cursor < 0 {
		m.cursor = m.firstJobRow()
		m.ensureCursorVisible()
		return
	}
	for i := m.cursor + delta; i >= 0 && i < len(m.rows); i += delta {
		if m.rows[i].kind == rowJob {
			m.cursor = i
			m.ensureCursorVisible()
			return
		}
	}
}

func (m *Model) page(direction int) {
	steps := m.listHeight() / 2
	if steps < 1 {
		steps = 1
	}
	for i := 0; i < steps; i++ {
		before := m.cursor
		m.move(direction)
		if m.cursor == before {
			break
		}
	}
}

func (m *Model) ensureCursorVisible() {
	h := m.listHeight()
	if h <= 0 || m.cursor < 0 {
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
	maxOffset := len(m.rows) - h
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
}

func (m Model) modalWidth() int {
	w := m.width
	if w <= 0 {
		w = 88
	}
	if w > 112 {
		w = 112
	}
	if w < 44 {
		w = 44
	}
	return w
}

func (m Model) modalHeight() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	if h > 32 {
		h = 32
	}
	if h < 8 {
		h = 8
	}
	return h
}

func (m Model) listHeight() int {
	h := m.modalHeight() - 4
	if h < 1 {
		return 1
	}
	return h
}

func (m Model) renderRows(w int) string {
	h := m.listHeight()
	if len(m.rows) == 0 {
		lines := make([]string, 0, h)
		msg := theme.StyleDim.Render("no background agents")
		pad := h / 2
		for i := 0; i < pad; i++ {
			lines = append(lines, "")
		}
		lines = append(lines, lipgloss.PlaceHorizontal(w, lipgloss.Center, msg))
		for len(lines) < h {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	rows := make([]string, 0, h)
	end := m.scrollOffset + h
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := m.scrollOffset; i < end; i++ {
		r := m.rows[i]
		if r.kind == rowHeader {
			rows = append(rows, m.renderHeader(r.group, w))
			continue
		}
		rows = append(rows, m.renderJobRow(r.job, i == m.cursor, w))
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

func (m Model) renderHeader(g group, w int) string {
	label := " " + strings.ToUpper(groupLabel(g)) + " "
	line := theme.StyleDim.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label))))
	return theme.StyleSecondary.Bold(true).Render(label) + line
}

func (m Model) renderJobRow(j Job, selected bool, w int) string {
	cursor := "  "
	if selected {
		cursor = "▶ "
	}
	state := renderState(j.State, 10)
	id := theme.StyleAccent.Render(padOrTrunc(j.ID, 8))
	prov := providerLabel(j)
	age := roundedAge(j)
	tokens := fmt.Sprintf("%d/%d", j.Tokens.Input, j.Tokens.Output)

	fixedW := lipgloss.Width(cursor) + 1 + 8 + 1 + 10 + 1 + 22 + 1 + 6 + 1 + 11 + 1
	promptW := w - fixedW
	if promptW < 8 {
		promptW = 8
	}
	prompt := rowMessage(j)
	prompt = truncate(strings.TrimSpace(prompt), promptW)
	if prompt == "" {
		prompt = "(no prompt)"
	}

	line := strings.Join([]string{
		cursor + id,
		state,
		padOrTrunc(prov, 22),
		padOrTrunc(age, 6),
		padOrTrunc(tokens, 11),
		prompt,
	}, " ")
	line = truncate(line, w)
	if selected {
		return lipgloss.NewStyle().Background(theme.BaseSurfaceBright).Render(line)
	}
	return line
}

func renderState(s string, w int) string {
	state := strings.ToLower(strings.TrimSpace(s))
	if state == "" {
		state = "unknown"
	}
	label := padOrTrunc(state, w)
	switch state {
	case StateRunning:
		return lipgloss.NewStyle().Foreground(theme.Info).Render(label)
	case StateQueued:
		return lipgloss.NewStyle().Foreground(theme.Warning).Render(label)
	case StateCompleted, "done", "success", "succeeded":
		return lipgloss.NewStyle().Foreground(theme.Success).Render(label)
	case StateFailed, "error":
		return lipgloss.NewStyle().Foreground(theme.Error).Render(label)
	case StateCancelled, "canceled":
		return theme.StyleSecondary.Render(label)
	default:
		return theme.StyleDim.Render(label)
	}
}

func providerLabel(j Job) string {
	if j.Provider == "" {
		return j.Model
	}
	if j.Model == "" {
		return j.Provider
	}
	return j.Provider + "/" + j.Model
}

func roundedAge(j Job) string {
	if j.CreatedAt.IsZero() {
		return "0s"
	}
	ref := time.Now()
	if !j.FinishedAt.IsZero() {
		ref = j.FinishedAt
	}
	return shortDuration(ref.Sub(j.CreatedAt))
}

func newestTime(j Job) time.Time {
	if !j.UpdatedAt.IsZero() {
		return j.UpdatedAt
	}
	if !j.FinishedAt.IsZero() {
		return j.FinishedAt
	}
	return j.CreatedAt
}

func rowMessage(j Job) string {
	if j.NeedsApproval {
		return "needs approval: " + nonEmpty(j.LastMessage, j.Prompt)
	}
	if j.NeedsInput {
		return "needs input: " + nonEmpty(j.LastMessage, j.Prompt)
	}
	if strings.EqualFold(strings.TrimSpace(j.LastMessage), "started") && j.Prompt != "" {
		return j.Prompt
	}
	if j.LastMessage != "" {
		return j.LastMessage
	}
	if j.Summary != "" {
		return j.Summary
	}
	if j.Error != "" {
		return j.Error
	}
	return j.Prompt
}

func nonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

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

func padOrTrunc(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[:w])
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

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
