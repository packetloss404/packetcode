package topbar

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTopBar_RendersProvider(t *testing.T) {
	m := New()
	m.SetWidth(120)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetContext(50_000, 200_000)
	m.SetProject("my-app", "main")

	out := m.View()
	assert.Contains(t, out, "packetcode")
	assert.Contains(t, out, "OpenAI")
	assert.Contains(t, out, "gpt-4.1")
	assert.Contains(t, out, "my-app")
	assert.NotContains(t, out, "$")
}

func TestTopBar_DropsSegmentsWhenNarrow(t *testing.T) {
	m := New()
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetContext(50_000, 200_000)
	m.SetProject("very-long-project-name-here", "feature/long-branch-name-here")

	wide := m.copyWith(120).View()
	narrow := m.copyWith(40).View()

	// Narrow rendering must drop at least one segment.
	assert.True(t,
		!strings.Contains(narrow, "very-long-project") ||
			!strings.Contains(narrow, "feature/long-branch-name-here"),
		"narrow rendering should shed at least one droppable segment",
	)
	// Wide rendering shouldn't drop those segments.
	assert.Contains(t, wide, "OpenAI")
}

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{
		500:       "500",
		1500:      "1K",
		1_500_000: "1.5M",
	}
	for in, want := range cases {
		assert.Equal(t, want, humanTokens(in), in)
	}
}

// copyWith returns a copy of m with the width changed — keeps tests
// isolated from each other.
func (m Model) copyWith(width int) Model {
	m.width = width
	return m
}

// TestTopbar_JobsSegment_HiddenWhenZero verifies that the ⚙ N jobs
// counter is elided entirely when no background jobs are active, so
// the status bar isn't cluttered for the common no-spawn case.
func TestTopbar_JobsSegment_HiddenWhenZero(t *testing.T) {
	m := New()
	m.SetWidth(200)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetJobs(0)

	out := m.View()
	assert.NotContains(t, out, "⚙")
	assert.NotContains(t, out, "jobs")
}

// TestTopbar_JobsSegment_ShownWhenActive verifies the plural rendering
// when two or more jobs are active.
func TestTopbar_JobsSegment_ShownWhenActive(t *testing.T) {
	m := New()
	m.SetWidth(200)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetJobs(3)

	out := m.View()
	assert.Contains(t, out, "⚙ 3 jobs")
}

// TestTopbar_JobsSegment_Singular verifies English pluralisation: 1
// job renders as "job" (no trailing 's'), so the counter reads
// naturally while still drawing the user's attention.
func TestTopbar_JobsSegment_Singular(t *testing.T) {
	m := New()
	m.SetWidth(200)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetJobs(1)

	out := m.View()
	assert.Contains(t, out, "⚙ 1 job")
	// Must not accidentally render as the plural "jobs".
	assert.NotContains(t, out, "⚙ 1 jobs")
}

// TestTopbar_JobsSegment_SurvivesNarrowDrops asserts the design
// decision: because an active background agent is time-sensitive, the
// jobs segment survives longer than every other droppable on a narrow
// terminal. In the implementation the droppable slice is ordered so
// that `jobsSeg` is the last entry (right-most drops first), meaning
// duration / git / project / context all get shed before the jobs
// counter.
func TestTopbar_JobsSegment_SurvivesNarrowDrops(t *testing.T) {
	m := New()
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetContext(50_000, 200_000)
	m.SetProject("very-long-project-name-here", "feature/long-branch-name-here")
	m.SetJobs(2)

	// Narrow enough that at least one droppable (duration) gets dropped.
	narrow := m.copyWith(50).View()

	assert.Contains(t, narrow, "⚙ 2 jobs",
		"jobs segment should survive narrow rendering (last-to-drop)")
}

func TestTopbar_CustomLineOverridesBuiltInSegments(t *testing.T) {
	m := New()
	m.SetWidth(120)
	m.SetProvider("openai", "OpenAI", "gpt-5.5")
	m.SetCustomLine("custom status")

	out := m.View()
	assert.Contains(t, out, "custom status")
	assert.NotContains(t, out, "OpenAI")
}

func TestTopbar_OperationShowsElapsedAndQueuedInputs(t *testing.T) {
	m := New()
	m.SetWidth(200)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetOperation(true, "thinking", time.Now().Add(-12*time.Second), 2)

	out := m.View()
	assert.Contains(t, out, "thinking")
	assert.Contains(t, out, "12s")
	assert.Contains(t, out, "2 queued")
}

func TestTopbar_PermissionProfileSegment(t *testing.T) {
	m := New()
	m.SetWidth(200)
	m.SetProvider("openai", "OpenAI", "gpt-4.1")
	m.SetPermissionProfile("ask")

	out := m.View()
	assert.Contains(t, out, "perm ask")
}
