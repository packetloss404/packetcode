package agentview

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snap(id string, state string, created time.Time, prompt string) Job {
	j := Job{
		ID:        id,
		Provider:  "gemini",
		Model:     "gemini-2.5-flash",
		State:     state,
		CreatedAt: created,
		Prompt:    prompt,
	}
	j.Tokens.Input = 120
	j.Tokens.Output = 45
	if state != StateQueued && state != StateRunning {
		j.FinishedAt = created.Add(12 * time.Second)
	}
	return j
}

func key(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	require.NotNil(t, cmd)
	return cmd()
}

func TestAgentView_RendersGroupedJobRows(t *testing.T) {
	now := time.Now()
	m := New()
	m.Resize(120, 30)
	m.Show([]Job{
		snap("done1111", StateCompleted, now.Add(-4*time.Minute), "summarise auth"),
		snap("run11111", StateRunning, now.Add(-1*time.Minute), "find call sites"),
		snap("fail1111", StateFailed, now.Add(-2*time.Minute), "audit tests"),
		snap("cancel11", StateCancelled, now.Add(-3*time.Minute), "try old path"),
	})

	out := m.View()
	assert.Contains(t, out, "Agent View")
	assert.Contains(t, out, "ACTIVE")
	assert.Contains(t, out, "COMPLETED")
	assert.Contains(t, out, "FAILED")
	assert.Contains(t, out, "CANCELLED")
	assert.Contains(t, out, "run11111")
	assert.Contains(t, out, "done1111")
	assert.Contains(t, out, "gemini/gemini")
	assert.Contains(t, out, "find call sites")
	assert.Contains(t, out, "p peek")
}

func TestAgentView_SelectionMovesAcrossGroups(t *testing.T) {
	now := time.Now()
	m := New()
	m.Resize(100, 20)
	m.Show([]Job{
		snap("done1111", StateCompleted, now.Add(-2*time.Minute), "done"),
		snap("run11111", StateRunning, now.Add(-1*time.Minute), "run"),
	})

	assert.Equal(t, "run11111", m.SelectedID())
	next, _ := m.Update(key("down"))
	assert.Equal(t, "done1111", next.SelectedID(), "down should skip the completed group header")

	next, _ = next.Update(key("up"))
	assert.Equal(t, "run11111", next.SelectedID(), "up should skip the active group header")
}

func TestAgentView_CloseEmitsMessageAndHides(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.Show([]Job{snap("run11111", StateRunning, time.Now(), "run")})

	next, cmd := m.Update(key("esc"))
	assert.False(t, next.Visible())
	_, ok := runCmd(t, cmd).(CloseMsg)
	assert.True(t, ok)
}

func TestAgentView_ActionMessagesUseSelectedJob(t *testing.T) {
	now := time.Now()
	m := New()
	m.Resize(100, 20)
	m.Show([]Job{
		snap("done1111", StateCompleted, now.Add(-2*time.Minute), "done"),
		snap("run11111", StateRunning, now.Add(-1*time.Minute), "run"),
	})
	next, _ := m.Update(key("down"))
	require.Equal(t, "done1111", next.SelectedID())

	_, cmd := next.Update(key("p"))
	assert.Equal(t, PeekMsg{JobID: "done1111"}, runCmd(t, cmd))

	_, cmd = next.Update(key("enter"))
	assert.Equal(t, OpenMsg{JobID: "done1111"}, runCmd(t, cmd))

	_, cmd = next.Update(key("c"))
	assert.Equal(t, CancelMsg{JobID: "done1111"}, runCmd(t, cmd))

	_, cmd = next.Update(key("i"))
	assert.Equal(t, InjectMsg{JobID: "done1111"}, runCmd(t, cmd))
}

func TestAgentView_EmptyShowsPlaceholderAndNoAction(t *testing.T) {
	m := New()
	m.Resize(80, 20)
	m.Show(nil)

	assert.Equal(t, "", m.SelectedID())
	assert.True(t, strings.Contains(m.View(), "no background agents"))

	next, cmd := m.Update(key("enter"))
	assert.Equal(t, "", next.SelectedID())
	assert.Nil(t, cmd)
}
