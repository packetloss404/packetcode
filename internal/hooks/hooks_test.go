package hooks

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/config"
)

func TestRunUserPromptSubmit_CollectsStdout(t *testing.T) {
	command := "input=$(cat); case \"$input\" in *hello*) printf injected-context;; *) exit 1;; esac"
	timeoutSec := 2
	if runtime.GOOS == "windows" {
		command = "$data = [Console]::In.ReadToEnd(); if ($data -match 'hello') { 'injected-context' } else { exit 1 }"
		timeoutSec = 5
	}
	r := New(config.HooksConfig{
		UserPromptSubmit: []config.HookConfig{{Command: command, TimeoutSec: timeoutSec}},
	}, t.TempDir())

	out, err := r.RunUserPromptSubmit(context.Background(), PromptPayload{Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "injected-context", out)
}

func TestRunPreToolUse_MatcherCanBlock(t *testing.T) {
	command := "echo blocked >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "Write-Error blocked; exit 7"
	}
	r := New(config.HooksConfig{
		PreToolUse: []config.HookConfig{{Matcher: "execute_command", Command: command, TimeoutSec: 2}},
	}, t.TempDir())

	_, err := r.RunPreToolUse(context.Background(), ToolPayload{ToolName: "execute_command"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")

	_, err = r.RunPreToolUse(context.Background(), ToolPayload{ToolName: "read_file"})
	require.NoError(t, err)
}

func TestRunPreToolUse_TimeoutMessage(t *testing.T) {
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 5"
	}
	r := New(config.HooksConfig{
		PreToolUse: []config.HookConfig{{Matcher: "execute_command", Command: command, TimeoutSec: 1}},
	}, t.TempDir())

	start := time.Now()
	_, err := r.RunPreToolUse(context.Background(), ToolPayload{ToolName: "execute_command"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out after 1s")
	assert.Contains(t, err.Error(), "process tree cancellation requested")
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestRunPostToolUse_TruncatesStdoutAndStderr(t *testing.T) {
	command := "(yes o | head -c 70000); (yes e | head -c 70000 >&2); exit 3"
	if runtime.GOOS == "windows" {
		command = "$out = 'o' * 70000; $err = 'e' * 70000; [Console]::Out.Write($out); [Console]::Error.Write($err); exit 3"
	}
	r := New(config.HooksConfig{
		PostToolUse: []config.HookConfig{{Matcher: "execute_command", Command: command, TimeoutSec: 5}},
	}, t.TempDir())

	out, err := r.RunPostToolUse(context.Background(), ToolPayload{ToolName: "execute_command"})
	require.NoError(t, err)
	assert.Contains(t, out, "stdout truncated at 64KB")
	assert.Contains(t, out, "stderr truncated at 64KB")
	assert.Less(t, len(out), 140*1024)
	assert.False(t, strings.Contains(out, strings.Repeat("o", 70000)))
}
