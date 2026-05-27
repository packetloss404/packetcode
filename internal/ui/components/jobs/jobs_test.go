package jobs

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"

	jobspkg "github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
)

// fakeSnap builds a minimal Snapshot for the rendering tests. We stay
// in the completed state so the header's duration calculation runs
// the non-zero branch.
func fakeSnap() jobspkg.Snapshot {
	s := jobspkg.Snapshot{
		ID:         "7f3a0b12",
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		State:      jobspkg.StateCompleted,
		CreatedAt:  time.Now().Add(-30 * time.Second),
		StartedAt:  time.Now().Add(-28 * time.Second),
		FinishedAt: time.Now().Add(-1 * time.Second),
		CostUSD:    0.0031,
		Summary:    "fake summary",
	}
	s.Tokens.Input = 120
	s.Tokens.Output = 45
	return s
}

// TestJobsPanel_RendersTranscript verifies the modal shows each
// transcript message's authoring role / name and content. We feed
// three messages — user, assistant, tool — and look for each marker
// in the rendered view.
func TestJobsPanel_RendersTranscript(t *testing.T) {
	m := New()
	m.Resize(100, 30)

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "find auth call sites"},
		{Role: provider.RoleAssistant, Content: "inspecting internal/ ..."},
		{Role: provider.RoleTool, Name: "search_codebase", Content: "42 matches in 8 files"},
	}
	m.Show(fakeSnap(), msgs)

	out := m.View()
	// Header anchors the id + state.
	assert.Contains(t, out, "[job:7f3a0b12]")
	assert.Contains(t, out, "completed")
	assert.Contains(t, out, "gemini/gemini-2.5-flash")

	// Each transcript message contributes an identifiable chunk.
	assert.Contains(t, out, "user")
	assert.Contains(t, out, "find auth call sites")
	assert.Contains(t, out, "assistant")
	assert.Contains(t, out, "inspecting internal/")
	assert.Contains(t, out, "[tool:search_codebase]")
	assert.Contains(t, out, "42 matches in 8 files")

	// Footer keybinding hint is always visible.
	assert.Contains(t, out, "Esc / q close")
	assert.Contains(t, out, "G newest")
}

func TestJobsPanel_RendersWorktreeHeaderLine(t *testing.T) {
	m := New()
	m.Resize(120, 24)
	snap := fakeSnap()
	snap.AllowWrite = true
	snap.WorktreePath = "wt/7f3a0b12"
	snap.WorktreeBranch = "packetcode-job-7f3a0b12"
	snap.WorktreeBase = "deadbeef"
	m.Show(snap, nil)

	out := m.View()
	assert.Contains(t, out, "worktree: wt/7f3a0b12")
	assert.Contains(t, out, "branch packetcode-job-7f3a0b12")
	assert.Contains(t, out, "base deadbeef")
}

func TestJobsPanel_RendersArtifactIndex(t *testing.T) {
	m := New()
	m.Resize(120, 24)
	snap := fakeSnap()
	snap.Artifacts = []jobspkg.Artifact{{
		ID:      "A1",
		Kind:    "test",
		Summary: "go test ./... [exit 0]",
	}, {
		ID:      "A2",
		Kind:    "file_change",
		Summary: "wrote main.go",
		Path:    "main.go",
	}}
	m.Show(snap, []provider.Message{{Role: provider.RoleAssistant, Content: "done"}})

	out := m.View()
	assert.Contains(t, out, "artifacts")
	assert.Contains(t, out, "A1 test")
	assert.Contains(t, out, "A2 file_change")
	assert.Contains(t, out, "assistant")
}

func TestJobsPanel_RendersWorktreeUnavailableHeaderLine(t *testing.T) {
	m := New()
	m.Resize(120, 24)
	snap := fakeSnap()
	snap.AllowWrite = true
	snap.WorktreeNote = "git rejected repository ownership"
	m.Show(snap, nil)

	assert.Contains(t, m.View(), "worktree unavailable: git rejected repository ownership")
}

// TestJobsPanel_EscClosesPanel verifies Esc toggles Visible() off. We
// push the key through Update the same way the App shell does.
func TestJobsPanel_EscClosesPanel(t *testing.T) {
	m := New()
	m.Resize(80, 24)
	m.Show(fakeSnap(), nil)
	assert.True(t, m.Visible(), "panel should be visible after Show")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, next.Visible(), "Esc should hide the panel")
}

// TestJobsPanel_QClosesPanel verifies `q` also closes. Separate from
// Esc so a regression that breaks one shortcut but leaves the other
// intact doesn't slip past.
func TestJobsPanel_QClosesPanel(t *testing.T) {
	m := New()
	m.Resize(80, 24)
	m.Show(fakeSnap(), nil)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	assert.False(t, next.Visible(), "q should hide the panel")
}

// TestJobsPanel_HiddenRendersEmpty sanity check: View() on a fresh
// Model returns empty so the layout's overlay slot can skip rendering
// entirely.
func TestJobsPanel_HiddenRendersEmpty(t *testing.T) {
	m := New()
	assert.Equal(t, "", m.View())
}

// TestJobsPanel_EmptyTranscriptShowsPlaceholder so a running job with
// no messages yet doesn't present a blank bordered box.
func TestJobsPanel_EmptyTranscriptShowsPlaceholder(t *testing.T) {
	m := New()
	m.Resize(80, 24)
	m.Show(fakeSnap(), nil)

	out := m.View()
	assert.True(t, strings.Contains(out, "(no messages yet)"),
		"empty transcript should render a placeholder")
}

func TestJobsPanel_ShowSessionRendersStickySessionHeader(t *testing.T) {
	m := New()
	m.Resize(100, 20)
	m.ShowSession("[session:abcd1234]", "openai/gpt · 2 messages · $0.0100", []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
	})

	out := m.View()
	assert.Contains(t, out, "[session:abcd1234]")
	assert.Contains(t, out, "openai/gpt")
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "G newest")
}
