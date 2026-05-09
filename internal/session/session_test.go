package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestManager_NewAndSave(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	s, err := m.New("openai", "gpt-4.1")
	require.NoError(t, err)
	assert.NotEmpty(t, s.ID)
	assert.Equal(t, "untitled", s.Name)

	files, _ := os.ReadDir(dir)
	require.Len(t, files, 1)
	assert.Equal(t, s.ID+".json", files[0].Name())
}

func TestManager_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	s, err := m.New("gemini", "gemini-2.5-pro")
	require.NoError(t, err)

	require.NoError(t, m.AddMessage(provider.Message{Role: provider.RoleUser, Content: "Refactor the auth middleware to JWT"}))
	require.NoError(t, m.AddMessage(provider.Message{Role: provider.RoleAssistant, Content: "Reading file..."}))

	m2 := NewManager(dir)
	loaded, err := m2.Load(s.ID)
	require.NoError(t, err)
	assert.Equal(t, s.ID, loaded.ID)
	require.Len(t, loaded.Messages, 2)
	assert.Equal(t, "refactor-the-auth-middleware-to-jwt", loaded.Name)
}

func TestManager_UpdateUsageComputesCost(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.New("openai", "gpt-4.1")
	require.NoError(t, err)

	require.NoError(t, m.UpdateUsage(provider.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}, 2.00, 8.00))
	got := m.Current()
	assert.InDelta(t, 6.00, got.Cost.TotalUSD, 1e-9, "cost = 1M*$2/M + 0.5M*$8/M")
}

func TestManager_ListSortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	m1 := NewManager(dir)
	s1, _ := m1.New("openai", "gpt-4.1")
	require.NoError(t, m1.AddMessage(provider.Message{Role: provider.RoleUser, Content: "first"}))

	m2 := NewManager(dir)
	s2, _ := m2.New("gemini", "gemini-2.5-pro")
	require.NoError(t, m2.AddMessage(provider.Message{Role: provider.RoleUser, Content: "second"}))

	listings, err := m2.List()
	require.NoError(t, err)
	require.Len(t, listings, 2)
	// Most recently updated session should be first.
	assert.Equal(t, s2.ID, listings[0].ID)
	assert.Equal(t, s1.ID, listings[1].ID)
}

func TestManager_Delete(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	s, _ := m.New("openai", "gpt-4.1")
	require.NoError(t, m.Delete(s.ID))

	_, err := os.Stat(filepath.Join(dir, s.ID+".json"))
	assert.True(t, os.IsNotExist(err))
	assert.Nil(t, m.Current())
}

func TestSanitizeName(t *testing.T) {
	tests := map[string]string{
		"":                          "untitled",
		"   ":                       "untitled",
		"Refactor Auth Middleware!": "refactor-auth-middleware",
		"path/with/slashes":         "pathwithslashes",
		"already-slug-form":         "already-slug-form",
	}
	for input, want := range tests {
		assert.Equal(t, want, sanitizeName(input), input)
	}
}

func TestBackupManager_BackupAndUndoExistingFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("// original\n"), 0o644))

	bk := NewBackupManager(t.TempDir(), "session-1")
	require.NoError(t, bk.Backup(target))
	assert.Equal(t, 1, bk.Depth())

	require.NoError(t, os.WriteFile(target, []byte("// modified\n"), 0o644))

	restored, err := bk.Undo()
	require.NoError(t, err)
	assert.Equal(t, target, restored)
	assert.Equal(t, 0, bk.Depth())

	got, _ := os.ReadFile(target)
	assert.Equal(t, "// original\n", string(got))
}

func TestBackupManager_UndoCreateRemovesFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "fresh.txt")

	bk := NewBackupManager(t.TempDir(), "session-x")
	// File doesn't exist yet — record an entry.
	require.NoError(t, bk.Backup(target))

	require.NoError(t, os.WriteFile(target, []byte("new content"), 0o644))
	_, err := os.Stat(target)
	require.NoError(t, err)

	restored, err := bk.Undo()
	require.NoError(t, err)
	assert.Equal(t, target, restored)
	_, err = os.Stat(target)
	assert.True(t, os.IsNotExist(err), "undo of a creation should delete the file")
}

func TestBackupManager_StackOrder(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	require.NoError(t, os.WriteFile(a, []byte("A0"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("B0"), 0o644))

	bk := NewBackupManager(t.TempDir(), "s")
	require.NoError(t, bk.Backup(a))
	require.NoError(t, os.WriteFile(a, []byte("A1"), 0o644))
	require.NoError(t, bk.Backup(b))
	require.NoError(t, os.WriteFile(b, []byte("B1"), 0o644))

	// First undo restores the most recent backup (b).
	restored, err := bk.Undo()
	require.NoError(t, err)
	assert.Equal(t, b, restored)
	got, _ := os.ReadFile(b)
	assert.Equal(t, "B0", string(got))

	// Second undo restores a.
	restored, err = bk.Undo()
	require.NoError(t, err)
	assert.Equal(t, a, restored)
	got, _ = os.ReadFile(a)
	assert.Equal(t, "A0", string(got))

	// Stack empty.
	restored, err = bk.Undo()
	require.NoError(t, err)
	assert.Empty(t, restored)
}

func TestBackupManager_Cleanup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "x.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))

	backupsDir := t.TempDir()
	bk := NewBackupManager(backupsDir, "sess")
	require.NoError(t, bk.Backup(target))
	require.NoError(t, bk.Cleanup())
	assert.Equal(t, 0, bk.Depth())

	_, err := os.Stat(filepath.Join(backupsDir, "sess"))
	assert.True(t, os.IsNotExist(err))
}

func TestBackupManager_UndoPopsOnlyAfterSuccessfulDelete(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "fresh")

	bk := NewBackupManager(t.TempDir(), "session-x")
	require.NoError(t, bk.Backup(target))
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "child.txt"), []byte("x"), 0o644))

	_, err := bk.Undo()
	require.Error(t, err)
	assert.Equal(t, 1, bk.Depth())

	require.NoError(t, os.Remove(filepath.Join(target, "child.txt")))
	restored, err := bk.Undo()
	require.NoError(t, err)
	assert.Equal(t, target, restored)
	assert.Equal(t, 0, bk.Depth())
}

func TestBackupManager_RollbackBackupRemovesPendingEntry(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	bk := NewBackupManager(t.TempDir(), "session-rollback")
	require.NoError(t, bk.Backup(target))
	assert.Equal(t, 1, bk.Depth())
	require.NoError(t, bk.RollbackBackup(target))
	assert.Equal(t, 0, bk.Depth())

	restored, err := bk.Undo()
	require.NoError(t, err)
	assert.Empty(t, restored)
}

func TestBackupManager_RollbackBackupIgnoresNonLastEntry(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	require.NoError(t, os.WriteFile(a, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("b"), 0o644))

	bk := NewBackupManager(t.TempDir(), "session-rollback-order")
	require.NoError(t, bk.Backup(a))
	require.NoError(t, bk.Backup(b))
	require.NoError(t, bk.RollbackBackup(a))
	assert.Equal(t, 2, bk.Depth())
}

func TestBackupManager_UndoRestoresMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permissions are not reliable on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "script.sh")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o755))

	bk := NewBackupManager(t.TempDir(), "session-mode")
	require.NoError(t, bk.Backup(target))
	require.NoError(t, os.WriteFile(target, []byte("new"), 0o600))

	_, err := bk.Undo()
	require.NoError(t, err)
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestManager_LoadRejectsTraversalID(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.Load(filepath.Join("..", "escape"))
	require.Error(t, err)
}

func TestManager_LoadRejectsDecodedTraversalID(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	path := filepath.Join(dir, "safe.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"../escape","name":"x","messages":[]}`), 0o600))

	_, err := m.Load("safe")
	require.Error(t, err)
	assert.Nil(t, m.Current())
}

func TestManager_LoadRejectsDecodedIDMismatch(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	path := filepath.Join(dir, "safe.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"other","name":"x","messages":[]}`), 0o600))

	_, err := m.Load("safe")
	require.Error(t, err)
	assert.Nil(t, m.Current())
}

func TestManager_SaveRejectsUnsafeCurrentID(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.current = &Session{ID: filepath.Join("..", "escape"), Name: "x"}

	err := m.Save()
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "..", "escape.json"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestManager_ListSkipsUnsafeDecodedIDs(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "safe.json"), []byte(`{"id":"safe","name":"safe","messages":[]}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"id":"../escape","name":"bad","messages":[]}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mismatch.json"), []byte(`{"id":"other","name":"bad","messages":[]}`), 0o600))

	got, err := m.List()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "safe", got[0].ID)
}

func TestManager_DeleteRejectsTraversalID(t *testing.T) {
	m := NewManager(t.TempDir())
	err := m.Delete(filepath.Join("..", "escape"))
	require.Error(t, err)
}

func TestBackupManager_RejectsInitialTraversalSessionID(t *testing.T) {
	root := t.TempDir()
	backupsDir := filepath.Join(root, "backups")
	target := filepath.Join(root, "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))

	bk := NewBackupManager(backupsDir, filepath.Join("..", "escape"))
	require.Error(t, bk.Backup(target))

	_, err := os.Stat(filepath.Join(root, "escape"))
	assert.True(t, os.IsNotExist(err))
}

func TestBackupManager_SwitchSessionRejectsTraversalAndKeepsRoot(t *testing.T) {
	root := t.TempDir()
	backupsDir := filepath.Join(root, "backups")
	target := filepath.Join(root, "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))

	bk := NewBackupManager(backupsDir, "safe")
	require.Error(t, bk.SwitchSession(filepath.Join("..", "escape")))
	require.NoError(t, bk.Backup(target))

	entries, err := os.ReadDir(filepath.Join(backupsDir, "safe"))
	require.NoError(t, err)
	assert.NotEmpty(t, entries)
	_, err = os.Stat(filepath.Join(root, "escape"))
	assert.True(t, os.IsNotExist(err))
}

func TestBackupManager_CleanupSessionRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	backupsDir := filepath.Join(root, "backups")
	outside := filepath.Join(root, "escape")
	require.NoError(t, os.MkdirAll(outside, 0o700))

	bk := NewBackupManager(backupsDir, "safe")
	require.Error(t, bk.CleanupSession(filepath.Join("..", "escape")))

	_, err := os.Stat(outside)
	require.NoError(t, err)
}
