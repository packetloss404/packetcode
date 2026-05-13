// Package statusline runs the optional user-configured command that can
// replace packetcode's built-in bottom status bar.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/procrun"
)

const (
	defaultTimeout           = 2 * time.Second
	maxStatusLineOutputBytes = 64 * 1024
)

type Runner struct {
	cfg config.StatusLineConfig
	cwd string
}

type Snapshot struct {
	SessionID       string        `json:"session_id,omitempty"`
	WorkingDir      string        `json:"working_dir,omitempty"`
	Project         string        `json:"project,omitempty"`
	GitBranch       string        `json:"git_branch,omitempty"`
	Provider        ProviderInfo  `json:"provider"`
	Model           ModelInfo     `json:"model"`
	ContextWindow   ContextInfo   `json:"context_window"`
	Cost            CostInfo      `json:"cost"`
	Jobs            JobsInfo      `json:"jobs"`
	Operation       OperationInfo `json:"operation"`
	DurationSeconds int           `json:"duration_seconds"`
	Version         string        `json:"version,omitempty"`
}

type ProviderInfo struct {
	Slug        string `json:"slug,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type ModelInfo struct {
	ID string `json:"id,omitempty"`
}

type ContextInfo struct {
	Used           int `json:"used"`
	Max            int `json:"max"`
	UsedPercentage int `json:"used_percentage"`
}

type CostInfo struct {
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type JobsInfo struct {
	Active int `json:"active"`
}

type OperationInfo struct {
	Active         bool   `json:"active"`
	Label          string `json:"label,omitempty"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
	QueuedInputs   int    `json:"queued_inputs"`
}

func New(cfg config.StatusLineConfig, cwd string) *Runner {
	if !cfg.IsEnabled() {
		return nil
	}
	return &Runner{cfg: cfg, cwd: cwd}
}

func (r *Runner) Enabled() bool { return r != nil && r.cfg.IsEnabled() }

func (r *Runner) Render(ctx context.Context, snap Snapshot) (string, error) {
	if !r.Enabled() {
		return "", nil
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("marshal statusline snapshot: %w", err)
	}
	timeout := time.Duration(r.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := shellCommand(runCtx, r.cfg.Command)
	if r.cwd != "" {
		cmd.Dir = r.cwd
	}
	cmd.Stdin = bytes.NewReader(data)
	stdout := procrun.NewBoundedBuffer(maxStatusLineOutputBytes)
	stderr := procrun.NewBoundedBuffer(maxStatusLineOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		msg := statuslineErrorMessage(runCtx, timeout, stdout.String(), stderr.String(), err)
		if stderr.Truncated() {
			msg += "\n...[stderr truncated at 64KB]..."
		}
		return "", fmt.Errorf("statusline command failed: %s", msg)
	}
	out := strings.TrimRight(stdout.String(), "\r\n")
	if stdout.Truncated() {
		out += "\n...[stdout truncated at 64KB]..."
	}
	return out, nil
}

func statuslineErrorMessage(ctx context.Context, timeout time.Duration, stdout, stderr string, err error) string {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("timed out after %s; process tree cancellation requested", timeout)
	}
	if ctx.Err() == context.Canceled {
		return "canceled; process tree cancellation requested"
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		msg = err.Error()
	}
	return msg
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	procrun.ConfigureTreeCancel(cmd)
	return cmd
}
