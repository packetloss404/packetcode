package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/procrun"
)

const (
	defaultExecTimeout = 60 * time.Second
	maxExecTimeout     = 10 * time.Minute
	maxExecOutputBytes = 100 * 1024
)

type ExecuteCommandTool struct {
	Root string
}

func NewExecuteCommandTool(root string) *ExecuteCommandTool {
	return &ExecuteCommandTool{Root: root}
}

func (*ExecuteCommandTool) Name() string            { return "execute_command" }
func (*ExecuteCommandTool) RequiresApproval() bool  { return true }
func (*ExecuteCommandTool) Schema() json.RawMessage { return executeCommandSchema() }
func (*ExecuteCommandTool) Description() string {
	return "Execute a shell command and capture stdout+stderr. Requires user approval. " + ExecuteRuntimeSafetyText()
}

type executeCommandParams struct {
	Command    string `json:"command"`
	CWD        string `json:"cwd,omitempty"`
	TimeoutSec int    `json:"timeout_sec,omitempty"`
}

func (t *ExecuteCommandTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p executeCommandParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("execute_command: parse params: %w", err)
	}
	if strings.TrimSpace(p.Command) == "" {
		return ToolResult{Content: "execute_command: command is empty", IsError: true}, nil
	}

	cwd := t.Root
	if p.CWD != "" {
		resolved, err := resolveExistingInRoot(t.Root, p.CWD)
		if err != nil {
			return ToolResult{Content: err.Error(), IsError: true}, nil
		}
		cwd = resolved
	}

	timeout := defaultExecTimeout
	if p.TimeoutSec > 0 {
		timeout = time.Duration(p.TimeoutSec) * time.Second
		if timeout > maxExecTimeout {
			timeout = maxExecTimeout
		}
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := buildShellCommand(cmdCtx, p.Command)
	cmd.Dir = cwd

	out := procrun.NewBoundedBuffer(maxExecOutputBytes)
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	timedOut := cmdCtx.Err() == context.DeadlineExceeded
	canceled := cmdCtx.Err() == context.Canceled
	truncated := out.Truncated()
	outBytes := out.Bytes()

	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", p.Command)
	if cwd != t.Root {
		fmt.Fprintf(&b, "(cwd: %s)\n", cwd)
	}
	if len(outBytes) > 0 {
		b.Write(outBytes)
		if !strings.HasSuffix(string(outBytes), "\n") {
			b.WriteByte('\n')
		}
	}
	if truncated {
		b.WriteString("...[output truncated at 100KB]...\n")
	}
	switch {
	case timedOut:
		fmt.Fprintf(&b, "[timed out after %s; process tree cancellation requested]\n", timeout)
	case canceled:
		b.WriteString("[canceled; process tree cancellation requested]\n")
	case exitCode == 0:
		b.WriteString("[exit 0]")
	default:
		fmt.Fprintf(&b, "[exit %d]", exitCode)
	}

	isError := timedOut || canceled || exitCode != 0
	return ToolResult{
		Content: b.String(),
		IsError: isError,
		Metadata: map[string]any{
			"exit_code": exitCode,
			"timed_out": timedOut,
			"canceled":  canceled,
			"truncated": truncated,
			"cwd":       cwd,
		},
	}, nil
}

// buildShellCommand picks the right invocation per OS. We deliberately use
// the shell rather than direct argv splitting so the LLM can use pipes,
// redirects, env-var expansion, etc. — closer to what a developer would type.
func buildShellCommand(ctx context.Context, command string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	procrun.ConfigureTreeCancel(cmd)
	return cmd
}

func executeCommandSchema() json.RawMessage {
	desc := "Shell command to execute. " + ExecuteRuntimeSafetyText()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": desc},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory relative to project root. Defaults to project root.",
			},
			"timeout_sec": map[string]any{
				"type":        "integer",
				"description": "Maximum execution time in seconds. Default 60, max 600.",
			},
		},
		"required": []string{"command"},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

func ExecuteRuntimeSafetyText() string {
	return shellRuntimeInfo().SafetyText()
}

type ShellRuntimeInfo struct {
	OS          string
	Default     string
	PowerShell  bool
	CMD         bool
	WSL         bool
	GitBash     bool
	GitBashPath string
}

func DetectShellRuntime() ShellRuntimeInfo {
	return shellRuntimeInfo()
}

func shellRuntimeInfo() ShellRuntimeInfo {
	info := ShellRuntimeInfo{OS: runtime.GOOS}
	if runtime.GOOS == "windows" {
		info.Default = "cmd /C"
		_, info.CMD = lookPath("cmd")
		_, info.PowerShell = lookPath("powershell")
		if !info.PowerShell {
			_, info.PowerShell = lookPath("pwsh")
		}
		_, info.WSL = lookPath("wsl")
		if p, ok := lookPath("bash"); ok {
			info.GitBash = strings.Contains(strings.ToLower(p), "git")
			info.GitBashPath = p
		}
		return info
	}
	info.Default = "sh -c"
	if p, ok := lookPath("bash"); ok {
		info.GitBash = true
		info.GitBashPath = p
	}
	return info
}

func (i ShellRuntimeInfo) SafetyText() string {
	if i.OS != "windows" {
		return "Runs through sh -c on this host; use explicit interpreters for shell-specific syntax. Output is truncated past 100KB."
	}
	available := []string{}
	if i.PowerShell {
		available = append(available, "PowerShell")
	}
	if i.CMD {
		available = append(available, "CMD")
	}
	if i.WSL {
		available = append(available, "WSL")
	}
	if i.GitBash {
		available = append(available, "Git Bash")
	}
	if len(available) == 0 {
		available = append(available, "CMD-compatible shell")
	}
	return fmt.Sprintf("Runs through %s on this Windows host. For PowerShell, WSL, or Git Bash syntax, invoke that runtime explicitly (for example powershell -NoProfile -Command ..., wsl ..., or bash -lc ...). Available: %s. Output is truncated past 100KB.", i.Default, strings.Join(available, ", "))
}

func lookPath(file string) (string, bool) {
	p, err := exec.LookPath(file)
	if err == nil {
		return p, true
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(file), ".exe") {
		if p, err := exec.LookPath(file + ".exe"); err == nil {
			return p, true
		}
	}
	if runtime.GOOS == "windows" && file == "bash" {
		for _, base := range []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			os.Getenv("LOCALAPPDATA"),
		} {
			if base == "" {
				continue
			}
			candidate := base + `\Git\bin\bash.exe`
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
		}
	}
	return "", false
}
