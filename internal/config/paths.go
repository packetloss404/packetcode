package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dirName is the name of packetcode's config/state directory under $HOME.
const dirName = ".packetcode"

// HomeDir returns ~/.packetcode, creating it (with 0700) if it does not exist.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home: %w", err)
	}
	dir := filepath.Join(home, dirName)
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ConfigPath returns ~/.packetcode/config.toml.
// The directory is created if missing; the file is not.
func ConfigPath() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// SessionsDir returns ~/.packetcode/sessions/, creating it if missing.
func SessionsDir() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	sessions := filepath.Join(dir, "sessions")
	if err := EnsureDir(sessions); err != nil {
		return "", err
	}
	return sessions, nil
}

// BackupsDir returns ~/.packetcode/backups/, creating it if missing.
func BackupsDir() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	backups := filepath.Join(dir, "backups")
	if err := EnsureDir(backups); err != nil {
		return "", err
	}
	return backups, nil
}

// JobsDir returns ~/.packetcode/jobs/, creating it if missing.
// Background-job metadata snapshots live here; see internal/jobs.
func JobsDir() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	jobs := filepath.Join(dir, "jobs")
	if err := EnsureDir(jobs); err != nil {
		return "", err
	}
	return jobs, nil
}

// WorktreesDir returns ~/.packetcode/worktrees/, creating it if missing.
// Write-enabled background agents create per-job git worktrees here.
func WorktreesDir() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	worktrees := filepath.Join(dir, "worktrees")
	if err := EnsureDir(worktrees); err != nil {
		return "", err
	}
	return worktrees, nil
}

// CostTallyPath returns ~/.packetcode/cost-tally.json.
// The directory is created if missing; the file is not.
func CostTallyPath() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cost-tally.json"), nil
}

// ThemePath returns ~/.packetcode/theme.toml.
// The directory is created if missing; the file is not. When the file
// is absent, the theme loader falls back to the built-in Terminal Noir
// palette.
func ThemePath() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "theme.toml"), nil
}

// UserCommandsDir returns ~/.packetcode/commands/, creating it if missing.
func UserCommandsDir() (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	commands := filepath.Join(dir, "commands")
	if err := EnsureDir(commands); err != nil {
		return "", err
	}
	return commands, nil
}

// ProjectCommandsDir returns <working-dir>/.packetcode/commands/.
// Project command directories are optional, so this helper does not create it.
func ProjectCommandsDir(workingDir string) string {
	if strings.TrimSpace(workingDir) == "" {
		workingDir = "."
	}
	return filepath.Join(workingDir, dirName, "commands")
}

// MCPLogPath returns ~/.packetcode/mcp-<name>.log. The directory is
// created if missing; the file is not. Names are deliberately restricted
// to filename-safe characters so a configured MCP server cannot escape
// the packetcode state directory via path separators or traversal.
func MCPLogPath(name string) (string, error) {
	dir, err := HomeDir()
	if err != nil {
		return "", err
	}
	file, err := MCPLogFileName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, file), nil
}

// MCPLogFileName returns the sanitized per-server log filename for name.
func MCPLogFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("invalid MCP server name %q", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return "", fmt.Errorf("invalid MCP server name %q: use only letters, digits, '_' or '-'", name)
		}
	}
	return "mcp-" + name + ".log", nil
}

// EnsureDir creates the directory (with parents) at 0700 if it does not exist.
// On Windows the perm bits are best-effort; the OS does not enforce POSIX modes.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create dir %s: %w", path, err)
	}
	return nil
}
