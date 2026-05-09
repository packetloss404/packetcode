package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestThemePath_UnderHomeDir pins the returned theme path to
// `<home>/.packetcode/theme.toml`. `t.Setenv` on both HOME and
// USERPROFILE keeps the test cross-platform (Windows prefers
// USERPROFILE; Unix prefers HOME).
// TestMCPLogPath asserts the log path resolves under the packetcode
// home directory and that the home directory is created on demand.
func TestMCPLogPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	got, err := MCPLogPath("git")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".packetcode", "mcp-git.log"), got)
}

func TestMCPLogPath_RejectsUnsafeName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	for _, name := range []string{"../evil", `foo\\bar`, "", "name.with.dot"} {
		_, err := MCPLogPath(name)
		require.Error(t, err, "name %q", name)
	}
}

func TestThemePath_UnderHomeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	got, err := ThemePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".packetcode", "theme.toml"), got)
}

func TestCommandsDirs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	user, err := UserCommandsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".packetcode", "commands"), user)

	project := ProjectCommandsDir(filepath.Join(dir, "work"))
	assert.Equal(t, filepath.Join(dir, "work", ".packetcode", "commands"), project)
}
