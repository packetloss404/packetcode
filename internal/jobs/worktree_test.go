package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateWorktree_NamespacesPathAndMetadataByRepo(t *testing.T) {
	root := initTestGitRepo(t)
	worktreesDir := t.TempDir()
	id := "abc12345"

	info, err := createWorktree(context.Background(), root, worktreesDir, id)
	require.NoError(t, err)

	repoKey := repoWorktreeKey(gitRepoRootForTest(t, root))
	assert.Equal(t, filepath.Join(worktreesDir, repoKey, id), info.Path)
	assert.DirExists(t, info.Path)
	assert.FileExists(t, filepath.Join(worktreesDir, "metadata", repoKey, id+".json"))
	assert.Equal(t, "packetcode-job-"+id, info.Branch)
	assert.NotEmpty(t, info.Base)
}

func TestCreateWorktree_RejectsPreexistingChildPath(t *testing.T) {
	root := initTestGitRepo(t)
	worktreesDir := t.TempDir()
	id := "abc12345"
	repoDir := filepath.Join(worktreesDir, repoWorktreeKey(gitRepoRootForTest(t, root)))
	require.NoError(t, os.MkdirAll(repoDir, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(repoDir, id), 0o700))

	_, err := createWorktree(context.Background(), root, worktreesDir, id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree path already exists")
}

func gitRepoRootForTest(t *testing.T, root string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)
	repoRoot, out, err := gitOutput(context.Background(), gitPath, root, "rev-parse", "--show-toplevel")
	require.NoError(t, err, out)
	return repoRoot
}

func TestWriteWorktreeMetadata_RejectsSymlinkMetadataDir(t *testing.T) {
	worktreesDir := t.TempDir()
	target := t.TempDir()
	err := os.Symlink(target, filepath.Join(worktreesDir, "metadata"))
	if err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	err = writeWorktreeMetadata(worktreesDir, "repo123", "abc12345", "repo", "branch", "base", "path")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "metadata dir") && strings.Contains(err.Error(), "symlink"))
}
