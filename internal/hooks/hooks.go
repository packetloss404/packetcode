// Package hooks runs user-configured shell hooks at selected agent lifecycle
// points. Hooks are opt-in config: packetcode never executes these commands
// unless the user declares them in config.toml.
package hooks

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
	defaultTimeout     = 10 * time.Second
	maxHookOutputBytes = 64 * 1024
)

type Runner struct {
	cfg config.HooksConfig
	cwd string
}

type PromptPayload struct {
	Event      string `json:"event"`
	SessionID  string `json:"session_id,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
	Prompt     string `json:"prompt"`
}

type ToolPayload struct {
	Event      string          `json:"event"`
	SessionID  string          `json:"session_id,omitempty"`
	WorkingDir string          `json:"working_dir,omitempty"`
	ToolName   string          `json:"tool_name"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     *ToolResult     `json:"result,omitempty"`
}

type ToolResult struct {
	Content  string         `json:"content"`
	IsError  bool           `json:"is_error"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Result struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Canceled        bool
}

func New(cfg config.HooksConfig, cwd string) *Runner {
	if len(cfg.UserPromptSubmit) == 0 && len(cfg.PreToolUse) == 0 && len(cfg.PostToolUse) == 0 {
		return nil
	}
	return &Runner{cfg: cfg, cwd: cwd}
}

func (r *Runner) RunUserPromptSubmit(ctx context.Context, payload PromptPayload) (string, error) {
	if r == nil {
		return "", nil
	}
	payload.Event = "UserPromptSubmit"
	if payload.WorkingDir == "" {
		payload.WorkingDir = r.cwd
	}
	return r.runCollect(ctx, r.cfg.UserPromptSubmit, "", payload, true)
}

func (r *Runner) RunPreToolUse(ctx context.Context, payload ToolPayload) (string, error) {
	if r == nil {
		return "", nil
	}
	payload.Event = "PreToolUse"
	if payload.WorkingDir == "" {
		payload.WorkingDir = r.cwd
	}
	return r.runCollect(ctx, r.cfg.PreToolUse, payload.ToolName, payload, true)
}

func (r *Runner) RunPostToolUse(ctx context.Context, payload ToolPayload) (string, error) {
	if r == nil {
		return "", nil
	}
	payload.Event = "PostToolUse"
	if payload.WorkingDir == "" {
		payload.WorkingDir = r.cwd
	}
	return r.runCollect(ctx, r.cfg.PostToolUse, payload.ToolName, payload, false)
}

func (r *Runner) runCollect(ctx context.Context, cfgs []config.HookConfig, toolName string, payload any, failFast bool) (string, error) {
	var outputs []string
	for _, cfg := range cfgs {
		if !cfg.IsEnabled() || !matchesTool(cfg.Matcher, toolName) {
			continue
		}
		res, err := r.runOne(ctx, cfg, payload)
		stdout := strings.TrimSpace(res.Stdout)
		if res.StdoutTruncated {
			stdout += "\n...[stdout truncated at 64KB]..."
		}
		if stdout != "" {
			outputs = append(outputs, stdout)
		}
		if err != nil {
			msg := hookErrorMessage(res, err)
			if res.StderrTruncated {
				msg += "\n...[stderr truncated at 64KB]..."
			}
			if failFast {
				return strings.Join(outputs, "\n\n"), fmt.Errorf("hook %q failed: %s", cfg.Command, msg)
			}
			outputs = append(outputs, fmt.Sprintf("hook %q failed: %s", cfg.Command, msg))
		}
	}
	return strings.Join(outputs, "\n\n"), nil
}

func (r *Runner) runOne(ctx context.Context, cfg config.HookConfig, payload any) (Result, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := shellCommand(runCtx, cfg.Command)
	if r.cwd != "" {
		cmd.Dir = r.cwd
	}
	cmd.Stdin = bytes.NewReader(data)
	stdout := procrun.NewBoundedBuffer(maxHookOutputBytes)
	stderr := procrun.NewBoundedBuffer(maxHookOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	res := Result{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		TimedOut:        runCtx.Err() == context.DeadlineExceeded,
		Canceled:        runCtx.Err() == context.Canceled,
	}
	if res.TimedOut {
		return res, fmt.Errorf("timed out after %s; process tree cancellation requested", timeout)
	}
	if res.Canceled {
		return res, fmt.Errorf("canceled; process tree cancellation requested")
	}
	return res, err
}

func hookErrorMessage(res Result, err error) string {
	if res.TimedOut || res.Canceled {
		return err.Error()
	}
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if msg == "" {
		msg = err.Error()
	}
	return msg
}

func matchesTool(matcher, toolName string) bool {
	matcher = strings.TrimSpace(matcher)
	return matcher == "" || matcher == "*" || matcher == toolName
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
