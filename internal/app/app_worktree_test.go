package app

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
)

func TestRenderJobsTable_WorktreeStates(t *testing.T) {
	now := time.Now().Add(-10 * time.Second)
	snaps := []jobs.Snapshot{
		{
			ID:             "ok123456",
			State:          jobs.StateCompleted,
			Provider:       "openai",
			Model:          "gpt-5",
			Prompt:         "write docs",
			AllowWrite:     true,
			CreatedAt:      now,
			WorktreePath:   "wt/ok123456",
			WorktreeBranch: "packetcode-job-ok123456",
			WorktreeBase:   "0123456789abcdef",
		},
		{
			ID:           "bad12345",
			State:        jobs.StateFailed,
			Provider:     "openai",
			Model:        "gpt-5",
			Prompt:       "write code",
			AllowWrite:   true,
			CreatedAt:    now,
			WorktreeNote: "git rejected repository ownership",
		},
	}

	out := renderJobsTable(snaps)
	assert.Contains(t, out, "ROOT")
	assert.Contains(t, out, "worktree")
	assert.Contains(t, out, "failed")
	assert.Contains(t, out, "worktree: wt/ok123456")
	assert.Contains(t, out, "branch packetcode-job-ok123456")
	assert.Contains(t, out, "worktree unavailable: git rejected repository ownership")
	assert.NotContains(t, failedRow(out, "bad12"), "pending")
}

func TestAgentResultBodyIncludesWorktreeBranchAndBase(t *testing.T) {
	body := agentResultBody(jobs.Result{
		JobID:   "abc12345",
		State:   jobs.StateCompleted,
		Summary: "updated files",
		Artifacts: []jobs.Artifact{{
			ID:      "A1",
			Kind:    "file_change",
			Summary: "wrote main.go",
			Path:    "main.go",
		}},
		WorktreePath:   "wt/abc12345",
		WorktreeBranch: "packetcode-job-abc12345",
		WorktreeBase:   "deadbeef",
	})

	assert.Contains(t, body, "[Background job abc12345 handoff]")
	assert.Contains(t, body, "Outcome: completed")
	assert.Contains(t, body, "updated files")
	assert.Contains(t, body, "worktree: wt/abc12345")
	assert.Contains(t, body, "branch packetcode-job-abc12345")
	assert.Contains(t, body, "base deadbeef")
	assert.Contains(t, body, "Artifacts:")
	assert.Contains(t, body, "A1 file_change: wrote main.go")
}

func TestWorktreeNotificationsOnlyEmitOnce(t *testing.T) {
	a := &App{
		deps: Deps{
			Registry:   provider.NewRegistry(),
			Sessions:   session.NewManager(""),
			WorkingDir: t.TempDir(),
		},
		conversation:    conversation.New(),
		jobSeqSeen:      map[string]int64{},
		jobWorktreeSeen: map[string]bool{},
		jobTerminalSeen: map[string]bool{},
	}
	snap := jobs.Snapshot{
		ID:             "abc12345",
		State:          jobs.StateRunning,
		Seq:            1,
		AllowWrite:     true,
		WorktreePath:   "wt/abc12345",
		WorktreeBranch: "packetcode-job-abc12345",
		WorktreeBase:   "deadbeef",
	}

	_, _ = a.handleJobUpdate(snap)
	snap.Seq = 2
	_, _ = a.handleJobUpdate(snap)

	a.conversation.Resize(120, 20)
	out := a.conversation.View()
	assert.Equal(t, 1, strings.Count(out, "[job:abc12345 worktree]"))
	assert.Contains(t, out, "branch packetcode-job-")
	assert.Contains(t, out, "abc12345")
}

func failedRow(table, idPrefix string) string {
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, idPrefix) {
			return line
		}
	}
	return ""
}
