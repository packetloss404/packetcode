package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSaveSnapshot_AtomicWrite confirms the temp-file-then-rename
// dance: after Save, only the final file (no .tmp) is present.
func TestSaveSnapshot_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	j := &Job{
		ID:             "abcd1234",
		SessionID:      "main-job-abcd1234",
		Prompt:         "do",
		Provider:       "scripted",
		Model:          "model",
		State:          StateCompleted,
		CreatedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
		WorktreePath:   filepath.Join(dir, "worktrees", "abcd1234"),
		WorktreeBranch: "packetcode-job-abcd1234",
		WorktreeBase:   "0123456789abcdef",
		WorktreeNote:   "ready",
	}
	require.NoError(t, saveSnapshot(dir, j))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "abcd1234.json", entries[0].Name())

	// Round-trip: parse back via the persisted shape.
	data, err := os.ReadFile(filepath.Join(dir, "abcd1234.json"))
	require.NoError(t, err)
	var p persistedJob
	require.NoError(t, json.Unmarshal(data, &p))
	assert.Equal(t, "abcd1234", p.ID)
	assert.Equal(t, "completed", p.State)
	assert.Equal(t, "pending", p.ResultStatus)
	assert.Equal(t, j.WorktreePath, p.WorktreePath)
	assert.Equal(t, j.WorktreeBranch, p.WorktreeBranch)
	assert.Equal(t, j.WorktreeBase, p.WorktreeBase)
	assert.Equal(t, j.WorktreeNote, p.WorktreeNote)

	roundTripped := fromPersisted(p)
	assert.Equal(t, j.WorktreePath, roundTripped.WorktreePath)
	assert.Equal(t, j.WorktreeBranch, roundTripped.WorktreeBranch)
	assert.Equal(t, j.WorktreeBase, roundTripped.WorktreeBase)
	assert.Equal(t, j.WorktreeNote, roundTripped.WorktreeNote)
}

func TestPersistedResultStatusDefaultsToPending(t *testing.T) {
	j := fromPersisted(persistedJob{
		ID:        "legacy01",
		SessionID: "main-job-legacy01",
		Provider:  "p",
		Model:     "m",
		State:     "completed",
		CreatedAt: time.Now().UTC(),
	})
	assert.Equal(t, ResultStatusPending, j.ResultStatus)
}

func TestSavePersistedSnapshotSkipsStaleSeq(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	newer := persistedJob{
		ID:        "seqjob01",
		SessionID: "main-job-seqjob01",
		Provider:  "p",
		Model:     "m",
		State:     "running",
		Seq:       2,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, savePersistedSnapshot(dir, newer))

	older := newer
	older.Seq = 1
	older.State = "queued"
	require.NoError(t, savePersistedSnapshot(dir, older))

	data, err := os.ReadFile(filepath.Join(dir, "seqjob01.json"))
	require.NoError(t, err)
	var got persistedJob
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(2), got.Seq)
	assert.Equal(t, "running", got.State)
}

// TestLoadOrphaned_RewritesRunningAndQueued asserts that any persisted
// job in StateRunning or StateQueued is rewritten as Cancelled with
// reason "previous app exit". Returns the resurrected jobs so callers
// can hydrate their map.
func TestLoadOrphaned_RewritesRunningAndQueued(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// Pre-write 3 jobs: Running, Queued, Completed.
	cases := []struct {
		id    string
		state State
	}{
		{"r1111111", StateRunning},
		{"q2222222", StateQueued},
		{"c3333333", StateCompleted},
	}
	for _, c := range cases {
		j := &Job{ID: c.id, SessionID: "main-job-" + c.id, Provider: "p", Model: "m", State: c.state, CreatedAt: now}
		require.NoError(t, saveSnapshot(dir, j))
	}

	resurrected, err := loadOrphaned(dir)
	require.NoError(t, err)
	require.Len(t, resurrected, 2, "Running + Queued, not Completed")

	ids := map[string]bool{}
	for _, j := range resurrected {
		ids[j.ID] = true
		assert.Equal(t, StateCancelled, j.State)
		assert.Equal(t, "previous app exit", j.Reason)
		assert.False(t, j.FinishedAt.IsZero())
	}
	assert.True(t, ids["r1111111"])
	assert.True(t, ids["q2222222"])
	assert.False(t, ids["c3333333"])

	// A second call should find nothing — they're now terminal.
	again, err := loadOrphaned(dir)
	require.NoError(t, err)
	assert.Empty(t, again)
}

// TestLoadOrphaned_MissingDirReturnsEmpty ensures a non-existent jobs
// dir is not an error — first-run is normal.
func TestLoadOrphaned_MissingDirReturnsEmpty(t *testing.T) {
	resurrected, err := loadOrphaned(filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	assert.Empty(t, resurrected)
}

// TestLoadOrphaned_SkipsNonJSONAndTempFiles ensures stray files don't
// crash the loader.
func TestLoadOrphaned_SkipsNonJSONAndTempFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".job.tmp.json.tmp"), []byte("garbage"), 0o600))
	resurrected, err := loadOrphaned(dir)
	require.NoError(t, err)
	assert.Empty(t, resurrected)
}
