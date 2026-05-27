package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFrom_MissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Default.Provider)
	assert.Equal(t, 80, cfg.Behavior.AutoCompactThreshold)
	assert.Equal(t, 10, cfg.Behavior.MaxInputRows)
	assert.False(t, cfg.Behavior.TrustMode)
	assert.NotNil(t, cfg.Providers)
}

func TestLoadFrom_ParsesValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `
[default]
provider = "openai"
model = "gpt-4.1"

[providers.openai]
api_key = "sk-test"
default_model = "gpt-4.1"

[providers.ollama]
host = "http://localhost:11434"
default_model = "qwen2.5-coder:14b"

[behavior]
trust_mode = true
auto_compact_threshold = 75
max_input_rows = 8

[permissions]
profile = "balanced"

[permissions.profiles.balanced]
default = "ask"
read_file = "allow"
search_codebase = "allow"
list_directory = "allow"
mcp = "ask"

[[permissions.rules]]
tool = "execute_command"
action = "deny"
command_prefix = ["rm", "-rf"]
reason = "block broad deletes"

[statusline]
command = "echo packetcode"
timeout_sec = 3

[[hooks.user_prompt_submit]]
command = "echo prompt-context"

[[hooks.pre_tool_use]]
matcher = "execute_command"
command = "echo guard"
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "openai", cfg.Default.Provider)
	assert.Equal(t, "gpt-4.1", cfg.Default.Model)
	assert.Equal(t, "sk-test", cfg.Providers["openai"].APIKey)
	assert.Equal(t, "http://localhost:11434", cfg.Providers["ollama"].Host)
	assert.True(t, cfg.Behavior.TrustMode)
	assert.Equal(t, 75, cfg.Behavior.AutoCompactThreshold)
	assert.Equal(t, 8, cfg.Behavior.MaxInputRows)
	assert.Equal(t, "balanced", cfg.Permissions.Profile)
	assert.Equal(t, "allow", cfg.Permissions.Profiles["balanced"]["read_file"])
	require.Len(t, cfg.Permissions.Rules, 1)
	assert.Equal(t, "execute_command", cfg.Permissions.Rules[0].Tool)
	assert.Equal(t, "deny", cfg.Permissions.Rules[0].Action)
	assert.Equal(t, []string{"rm", "-rf"}, cfg.Permissions.Rules[0].CommandPrefix)
	assert.Equal(t, "echo packetcode", cfg.StatusLine.Command)
	assert.Equal(t, 3, cfg.StatusLine.TimeoutSec)
	require.Len(t, cfg.Hooks.UserPromptSubmit, 1)
	assert.Equal(t, "echo prompt-context", cfg.Hooks.UserPromptSubmit[0].Command)
	require.Len(t, cfg.Hooks.PreToolUse, 1)
	assert.Equal(t, "execute_command", cfg.Hooks.PreToolUse[0].Matcher)
}

func TestSaveTo_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	original := Default()
	original.Default.Provider = "gemini"
	original.Default.Model = "gemini-2.5-pro"
	original.Providers["gemini"] = ProviderConfig{
		APIKey:       "AI-test",
		DefaultModel: "gemini-2.5-pro",
	}
	original.Behavior.TrustMode = true
	original.Permissions.Profile = "read_only"
	original.Permissions.Profiles["ci"] = PermissionProfile{"default": "ask", "execute_command": "allow"}
	original.Permissions.Rules = []PermissionRule{{Tool: "spawn_agent", Action: "deny"}}

	require.NoError(t, original.SaveTo(path))

	loaded, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, original.Default, loaded.Default)
	assert.Equal(t, original.Providers["gemini"], loaded.Providers["gemini"])
	assert.Equal(t, original.Behavior, loaded.Behavior)
	assert.Equal(t, original.Permissions, loaded.Permissions)
}

func TestSaveTo_FilePermissions0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	require.NoError(t, Default().SaveTo(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestGetProviderKey_EnvVarOverride(t *testing.T) {
	cfg := Default()
	cfg.Providers["openai"] = ProviderConfig{APIKey: "from-config"}

	t.Setenv("PACKETCODE_OPENAI_API_KEY", "from-env")

	assert.Equal(t, "from-env", cfg.GetProviderKey("openai"))
}

func TestGetProviderKey_FallsBackToConfig(t *testing.T) {
	cfg := Default()
	cfg.Providers["openai"] = ProviderConfig{APIKey: "from-config"}

	t.Setenv("PACKETCODE_OPENAI_API_KEY", "")

	assert.Equal(t, "from-config", cfg.GetProviderKey("openai"))
}

func TestGetProviderKey_MissingProviderReturnsEmpty(t *testing.T) {
	cfg := Default()
	t.Setenv("PACKETCODE_GEMINI_API_KEY", "")
	assert.Equal(t, "", cfg.GetProviderKey("gemini"))
}

func TestSetProviderKey_PersistsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // Windows

	// Ensure no env var override interferes.
	t.Setenv("PACKETCODE_OPENAI_API_KEY", "")

	cfg, err := Load()
	require.NoError(t, err)

	require.NoError(t, cfg.SetProviderKey("openai", "sk-new-key"))

	reloaded, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "sk-new-key", reloaded.GetProviderKey("openai"))
}

func TestIsFirstRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	assert.True(t, IsFirstRun(), "fresh temp home should be first run")

	require.NoError(t, Default().Save())

	assert.False(t, IsFirstRun(), "after Save the config should exist")
}

// TestConfig_MCPBlockRoundTrip writes two [mcp.<name>] blocks (one
// disabled, one with default-true) and reads them back verbatim. The
// pointer-bool round-trip is the load-bearing assertion.
func TestConfig_MCPBlockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `
[mcp.git]
command = "uvx"
args = ["mcp-server-git", "--repository", "."]
timeout_sec = 20

[mcp.disabled-example]
command = "echo"
enabled = false
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.Contains(t, cfg.MCP, "git")
	require.Contains(t, cfg.MCP, "disabled-example")

	gitEntry := cfg.MCP["git"]
	assert.Equal(t, "uvx", gitEntry.Command)
	assert.Equal(t, []string{"mcp-server-git", "--repository", "."}, gitEntry.Args)
	assert.Equal(t, 20, gitEntry.TimeoutSec)
	assert.True(t, gitEntry.IsEnabled(), "missing enabled key should default to true")
	assert.Nil(t, gitEntry.Enabled)

	disabled := cfg.MCP["disabled-example"]
	require.NotNil(t, disabled.Enabled)
	assert.False(t, *disabled.Enabled)
	assert.False(t, disabled.IsEnabled())

	// Save + reload preserves the blocks.
	roundTripPath := filepath.Join(dir, "config-out.toml")
	require.NoError(t, cfg.SaveTo(roundTripPath))
	cfg2, err := LoadFrom(roundTripPath)
	require.NoError(t, err)
	require.Contains(t, cfg2.MCP, "git")
	require.Contains(t, cfg2.MCP, "disabled-example")
	assert.Equal(t, gitEntry.Command, cfg2.MCP["git"].Command)
	require.NotNil(t, cfg2.MCP["disabled-example"].Enabled)
	assert.False(t, *cfg2.MCP["disabled-example"].Enabled)
}

// TestConfig_MCPMapInitialisedOnLoad confirms loading a config file
// with no [mcp] section still yields a non-nil MCP map.
func TestConfig_MCPMapInitialisedOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `
[default]
provider = "openai"
model = "gpt-4.1"
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.MCP)
	assert.Empty(t, cfg.MCP)
}

func TestEnsureDir_CreatesNested(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	require.NoError(t, EnsureDir(nested))

	info, err := os.Stat(nested)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}
