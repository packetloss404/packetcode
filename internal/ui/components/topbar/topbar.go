// Package topbar renders the always-visible status row at the top of the
// TUI: brand, active provider/model, context-window gauge, project name,
// git branch, session duration, and (when any background agents are
// alive) an active-jobs counter.
//
// The top bar is responsive — when the terminal is narrow, it sheds
// segments in this priority order (right-most first):
//
//	duration → git branch → project name → context %
//
// Provider/model and the brand are always shown. The jobs segment is
// deliberately placed FIRST in the droppable slice (so it's the last
// thing dropped as the terminal narrows) — active background jobs are
// time-sensitive information the user wants to see as long as any
// chrome survives. See TestTopbar_JobsSegment_SurvivesNarrowDrops and
// docs/feature-background-agents.md for the choice.
package topbar

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/ui/theme"
)

type Model struct {
	width int

	provider     string
	model        string
	providerSlug string

	contextPct int
	tokensUsed int
	tokensMax  int

	projectName string
	gitBranch   string
	startTime   time.Time

	activeJobs        int
	customLine        string
	permissionProfile string

	operationActive  bool
	operationLabel   string
	operationStarted time.Time
	queuedInputs     int
}

func New() Model {
	return Model{startTime: time.Now()}
}

// SetWidth tells the bar how much horizontal space it has. Triggers
// segment shedding for narrow terminals.
func (m *Model) SetWidth(w int) { m.width = w }

// SetProvider updates the provider/model identity segment.
func (m *Model) SetProvider(slug, displayName, model string) {
	m.providerSlug = slug
	m.provider = displayName
	m.model = model
}

// SetContext updates the context-window gauge.
func (m *Model) SetContext(used, max int) {
	m.tokensUsed = used
	m.tokensMax = max
	if max > 0 {
		m.contextPct = used * 100 / max
		if m.contextPct > 100 {
			m.contextPct = 100
		}
	} else {
		m.contextPct = 0
	}
}

func (m *Model) SetProject(name, branch string) {
	m.projectName = name
	m.gitBranch = branch
}

// SetJobs updates the active-background-jobs counter. n ≤ 0 hides the
// segment entirely; positive values render as "⚙ N job(s)".
func (m *Model) SetJobs(n int) {
	if n < 0 {
		n = 0
	}
	m.activeJobs = n
}

// Jobs returns the most recently set active-jobs count. Primarily used
// by tests to assert the App has called SetJobs after a jobs.Manager
// state transition without having to parse the rendered view.
func (m Model) Jobs() int { return m.activeJobs }

func (m *Model) SetPermissionProfile(profile string) {
	m.permissionProfile = strings.TrimSpace(profile)
}

// SetOperation updates the foreground operation segment. label should
// be a short gerund such as "thinking" or "compacting"; queued is the
// number of user prompts waiting behind that operation.
func (m *Model) SetOperation(active bool, label string, started time.Time, queued int) {
	m.operationActive = active
	m.operationLabel = strings.TrimSpace(label)
	m.operationStarted = started
	if queued < 0 {
		queued = 0
	}
	m.queuedInputs = queued
}

func (m *Model) SetCustomLine(line string) {
	m.customLine = strings.TrimRight(line, "\r\n")
}

func (m Model) CustomLine() string { return m.customLine }

// View renders the top bar. Width must be set first; if it's 0, a sane
// minimum is used so the call doesn't panic during early initialisation.
func (m Model) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	if m.customLine != "" {
		return theme.StyleTopBar.Width(width - 2).Render(m.customLine)
	}

	brand := theme.LabelBadge("⚡ packetcode", theme.AccentPrimary)

	provider := strings.TrimSpace(m.provider)
	if provider == "" {
		provider = "no provider"
	}
	dot := lipgloss.NewStyle().Foreground(theme.ProviderColor(m.providerSlug)).Render("●")
	providerSeg := dot + " " + theme.StylePrimary.Render(provider)
	if m.model != "" {
		providerSeg += theme.StyleSecondary.Render(" / " + m.model)
	}

	contextSeg := m.renderContext()
	projectSeg := ""
	if m.projectName != "" {
		projectSeg = theme.StyleSecondary.Render("📂 " + m.projectName)
	}
	gitSeg := ""
	if m.gitBranch != "" {
		gitSeg = theme.StyleSecondary.Render("⎇ " + m.gitBranch)
	}
	durSeg := theme.StyleDim.Render("⏱ " + formatDuration(time.Since(m.startTime)))
	opSeg := m.renderOperation()
	permissionSeg := ""
	if m.permissionProfile != "" {
		permissionSeg = theme.StyleSecondary.Render("perm " + m.permissionProfile)
	}

	jobsSeg := ""
	if m.activeJobs > 0 {
		noun := "jobs"
		if m.activeJobs == 1 {
			noun = "job"
		}
		jobsSeg = lipgloss.NewStyle().
			Foreground(theme.AccentPrimary).
			Bold(true).
			Render(fmt.Sprintf("⚙ %d %s", m.activeJobs, noun))
	}

	// Always-shown vs droppable in priority order. The right-most entry
	// in `droppable` is dropped first when the terminal is too narrow,
	// so lower-priority segments live at the TAIL and higher-priority
	// ones at the HEAD. We place jobsSeg first — it survives longer
	// than every other droppable because an active background agent is
	// time-sensitive, so we'd rather shed duration / git / project /
	// context than hide the ⚙ N jobs counter. Concretely, the
	// narrow-mode drop sequence becomes:
	//   duration → git → project → context → jobs
	required := []string{brand, providerSeg}
	droppable := []string{jobsSeg, opSeg, permissionSeg, contextSeg, projectSeg, gitSeg, durSeg}

	// Drop right-most droppable segments until the line fits inside the
	// content area (width minus border + padding budget).
	contentWidth := width - 4 // border (2) + padding (2)
	if contentWidth < 20 {
		contentWidth = 20
	}
	segments := append(required, droppable...)
	for {
		joined := joinWithSep(segments)
		if lipgloss.Width(joined) <= contentWidth || len(segments) <= len(required) {
			break
		}
		segments = segments[:len(segments)-1]
	}
	line := joinWithSep(segments)
	return theme.StyleTopBar.Width(width - 2).Render(line)
}

func (m Model) renderOperation() string {
	if !m.operationActive && m.queuedInputs == 0 {
		return ""
	}
	parts := []string{}
	if m.operationActive {
		label := m.operationLabel
		if label == "" {
			label = "working"
		}
		elapsed := time.Duration(0)
		if !m.operationStarted.IsZero() {
			elapsed = time.Since(m.operationStarted)
		}
		parts = append(parts, fmt.Sprintf("%s %s", label, formatSeconds(elapsed)))
	}
	if m.queuedInputs > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", m.queuedInputs))
	}
	return lipgloss.NewStyle().
		Foreground(theme.Warning).
		Bold(true).
		Render("◷ " + strings.Join(parts, " · "))
}

func (m Model) renderContext() string {
	if m.tokensMax <= 0 {
		return ""
	}
	icon := "🟢"
	colorStyle := theme.StyleSecondary
	switch {
	case m.contextPct >= 95:
		icon = "🔴"
		colorStyle = theme.StyleError
	case m.contextPct >= 80:
		icon = "🟡"
		colorStyle = theme.StyleWarning
	}
	return colorStyle.Render(fmt.Sprintf("%s %d%% (%s/%s)",
		icon, m.contextPct,
		humanTokens(m.tokensUsed),
		humanTokens(m.tokensMax),
	))
}

func formatSeconds(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}

func joinWithSep(segments []string) string {
	out := []string{}
	sep := theme.StyleDim.Render(" │ ")
	for _, s := range segments {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, sep)
}

func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatDuration(d time.Duration) string {
	mins := int(d.Minutes())
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	hours := mins / 60
	mins = mins % 60
	return fmt.Sprintf("%dh%dm", hours, mins)
}
