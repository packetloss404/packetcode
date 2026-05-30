package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellEcho emits a portable echo invocation for the test command.
// Windows cmd.exe uses double quotes around the echoed string differently
// than POSIX sh, so we keep both branches simple.
func shellEcho(s string) string {
	if runtime.GOOS == "windows" {
		return "echo " + s
	}
	return "echo '" + s + "'"
}

func TestExecuteCommand_RunsAndCapturesStdout(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	body, _ := json.Marshal(map[string]any{"command": shellEcho("hello-world")})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "hello-world")
	assert.Contains(t, res.Content, "[exit 0]")
}

func TestExecuteCommand_NonZeroExit(t *testing.T) {
	cmd := "exit 7"
	if runtime.GOOS == "windows" {
		cmd = "cmd /C exit 7"
	}
	tool := NewExecuteCommandTool(t.TempDir())
	body, _ := json.Marshal(map[string]any{"command": cmd})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "[exit 7]")
}

func TestExecuteCommand_Timeout(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		command = "ping -n 6 127.0.0.1 >NUL"
	}
	body, _ := json.Marshal(map[string]any{
		"command":     command,
		"timeout_sec": 1,
	})
	start := time.Now()
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "timed out")
	assert.Contains(t, res.Content, "process tree cancellation requested")
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestExecuteCommand_RejectsCWDOutsideRoot(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	body, _ := json.Marshal(map[string]any{
		"command": shellEcho("hi"),
		"cwd":     "../escape",
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "outside project root")
}

func TestExecuteCommand_RequiresApproval(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	assert.True(t, tool.RequiresApproval())
}

func TestExecuteCommand_DescriptionAndSchemaMentionRuntimeSafety(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	assert.Contains(t, tool.Description(), "Requires user approval")
	assert.Contains(t, tool.Description(), "Output is truncated past 100KB")

	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.Schema(), &schema))
	props := schema["properties"].(map[string]any)
	command := props["command"].(map[string]any)
	desc := command["description"].(string)
	if runtime.GOOS == "windows" {
		assert.Contains(t, desc, "cmd /C")
		assert.Contains(t, desc, "PowerShell")
		assert.Contains(t, desc, "WSL")
		assert.Contains(t, desc, "Git Bash")
	} else {
		assert.Contains(t, desc, "sh -c")
	}
}

// TestExecuteCommand_ContextCancelKillsProcess proves that cancelling
// the ctx handed to Execute promptly tears down the underlying process.
// Round 5 relies on this: Ctrl+C at the App layer cancels the turn
// ctx, which the agent passes through to tool.Execute, which must kill
// anything mid-flight.
func TestExecuteCommand_ContextCancelKillsProcess(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 31 127.0.0.1 >NUL"
	}
	body, _ := json.Marshal(map[string]any{"command": command})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := tool.Execute(ctx, body)
	elapsed := time.Since(start)

	require.NoError(t, err, "Execute should swallow the killed-process error into a ToolResult")
	assert.True(t, res.IsError, "cancelled run should be flagged as an error")
	assert.Contains(t, res.Content, "canceled")
	assert.NotContains(t, res.Content, "[exit 0]")
	assert.Less(t, elapsed, 1500*time.Millisecond, "Execute must return promptly after ctx cancel; took %s", elapsed)
}

func TestExecuteCommand_NonZeroExitIsNotCancellation(t *testing.T) {
	cmd := "exit 7"
	if runtime.GOOS == "windows" {
		cmd = "cmd /C exit 7"
	}
	tool := NewExecuteCommandTool(t.TempDir())
	body, _ := json.Marshal(map[string]any{"command": cmd})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "[exit 7]")
	assert.NotContains(t, res.Content, "canceled")
	assert.NotContains(t, res.Content, "timed out")
}

func TestExecuteCommand_TruncatesCapturedOutput(t *testing.T) {
	root := t.TempDir()
	bigFile := root + string(os.PathSeparator) + "big.txt"
	require.NoError(t, os.WriteFile(bigFile, []byte(strings.Repeat("x", 120000)), 0o600))
	command := "cat big.txt"
	if runtime.GOOS == "windows" {
		command = "type big.txt"
	}
	tool := NewExecuteCommandTool(root)
	body, _ := json.Marshal(map[string]any{"command": command})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "output truncated at 100KB")
	assert.Less(t, len(res.Content), 106*1024)
	assert.Equal(t, true, res.Metadata["truncated"])
}

// --- Phase 1 Round 4: output streaming (producer side) ---
//
// These tests pin down the OBSERVABLE behavior the streaming round must
// preserve. The incremental-streaming hook itself (Part 1) is not yet final —
// execute_command currently buffers into a procrun.BoundedBuffer and returns on
// exit (see execute_command.go). Rather than assert against an unstable API
// surface, these tests assert the invariants that must hold both today and
// after streaming lands: a slow, multi-line command runs to completion with all
// output captured, the bounded cap survives high-volume output, and
// cancellation still tears the process down promptly. See
// TestExecuteCommand_StreamsIncrementally_Stub below for the streaming-specific
// assertion that should be enabled once the Part 1 hook is finalized.

// slowMultiLineCommand emits several lines spaced out in time so that a true
// incremental streamer would deliver early lines well before process exit.
func slowMultiLineCommand() string {
	if runtime.GOOS == "windows" {
		// Each ping -n 2 waits ~1s; echo between them produces interleaved lines.
		return "echo line1 & ping -n 2 127.0.0.1 >NUL & echo line2 & ping -n 2 127.0.0.1 >NUL & echo line3"
	}
	return "printf 'line1\\n'; sleep 0.3; printf 'line2\\n'; sleep 0.3; printf 'line3\\n'"
}

// TestExecuteCommand_SlowMultiLineCapturesAll verifies that a slow, multi-line
// command completes successfully and that every emitted line is present in the
// final captured output. This is the producer-side guarantee the streaming
// round must not regress: streaming the lines incrementally must still leave the
// full (uncapped-here) content in the returned ToolResult.
func TestExecuteCommand_SlowMultiLineCapturesAll(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	body, _ := json.Marshal(map[string]any{"command": slowMultiLineCommand()})

	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError, "slow multi-line command should exit 0: %s", res.Content)
	for _, line := range []string{"line1", "line2", "line3"} {
		assert.Contains(t, res.Content, line, "captured output must include every streamed line")
	}
	assert.Contains(t, res.Content, "[exit 0]")
	assert.Equal(t, false, res.Metadata["truncated"], "small multi-line output must not be truncated")
}

// TestExecuteCommand_StreamingPreservesBoundedCap verifies that high-volume
// output is still truncated at the 100KB cap. The streaming round delivers
// chunks to the UI as they arrive, but the FINAL tool result must remain
// bounded — the streamed view is unbounded, the captured/returned buffer is not.
func TestExecuteCommand_StreamingPreservesBoundedCap(t *testing.T) {
	root := t.TempDir()
	bigFile := root + string(os.PathSeparator) + "big.txt"
	// 120KB of data > the 100KB cap. Written line-wise so a streaming producer
	// would emit many chunks before hitting the cap.
	require.NoError(t, os.WriteFile(bigFile, []byte(strings.Repeat("packetcode-stream-line\n", 6000)), 0o600))
	command := "cat big.txt"
	if runtime.GOOS == "windows" {
		command = "type big.txt"
	}
	tool := NewExecuteCommandTool(root)
	body, _ := json.Marshal(map[string]any{"command": command})

	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "output truncated at 100KB", "bounded cap must survive streaming")
	assert.Equal(t, true, res.Metadata["truncated"])
	// Content is the cap plus a small fixed header/footer; well under 106KB.
	assert.Less(t, len(res.Content), 106*1024, "returned content must stay bounded even under high-volume output")
}

// TestExecuteCommand_StreamingStillCancels verifies cancellation continues to
// work for a slow command. Streaming output incrementally must not break the
// Round 2 guarantee that a cancelled ctx promptly kills the process tree.
func TestExecuteCommand_StreamingStillCancels(t *testing.T) {
	tool := NewExecuteCommandTool(t.TempDir())
	// A long, slow producer: would stream many lines if allowed to run.
	command := "for i in 1 2 3 4 5 6 7 8 9 10; do printf 'tick %d\\n' \"$i\"; sleep 1; done"
	if runtime.GOOS == "windows" {
		command = "for /L %i in (1,1,10) do (echo tick %i & ping -n 2 127.0.0.1 >NUL)"
	}
	body, _ := json.Marshal(map[string]any{"command": command})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res, err := tool.Execute(ctx, body)
	elapsed := time.Since(start)

	require.NoError(t, err, "Execute should fold the killed-process error into a ToolResult")
	assert.True(t, res.IsError, "cancelled streaming run should be flagged as an error")
	assert.Contains(t, res.Content, "canceled")
	assert.NotContains(t, res.Content, "[exit 0]")
	assert.Less(t, elapsed, 2*time.Second, "cancellation must return promptly while streaming; took %s", elapsed)
}

// TestExecuteCommand_StreamsIncrementally_Stub is the streaming-specific
// assertion for Part 1. It is intentionally skipped until the incremental
// streaming hook is finalized, because the exact callback/sink API is not yet
// settled (today execute_command writes to a procrun.BoundedBuffer and returns
// only on exit).
//
// When Part 1 lands, enable this test and assert against the real hook, e.g.:
//
//	var firstChunkAt time.Time
//	sink := func(chunk []byte) {
//	        if firstChunkAt.IsZero() { firstChunkAt = time.Now() }
//	}
//	start := time.Now()
//	_, _ = tool.ExecuteStreaming(ctx, body, sink) // or whatever the final API is
//	// The first chunk must arrive well before the ~0.6s command exits:
//	assert.Less(t, firstChunkAt.Sub(start), 400*time.Millisecond)
//
// The producer-side invariants this stub depends on (every line captured,
// cap preserved, cancellation works) are already covered by the three tests
// above, so they are not regressed in the meantime.
func TestExecuteCommand_StreamsIncrementally_Stub(t *testing.T) {
	t.Skip("Part 1 incremental-streaming hook not finalized; enable once execute_command exposes a stream sink. Producer invariants covered by TestExecuteCommand_SlowMultiLineCapturesAll / _StreamingPreservesBoundedCap / _StreamingStillCancels.")
}

func TestExecuteCommand_CancelsPOSIXProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows descendant enumeration is environment-dependent; taskkill path is covered by fast cancel test")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep unavailable")
	}
	root := t.TempDir()
	pidFile := root + string(os.PathSeparator) + "child.pid"
	command := "sleep 30 & printf %s $! > " + strconv.Quote(pidFile) + "; wait"
	tool := NewExecuteCommandTool(root)
	body, _ := json.Marshal(map[string]any{"command": command})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ToolResult, 1)
	go func() {
		res, err := tool.Execute(ctx, body)
		require.NoError(t, err)
		done <- res
	}()

	var pidBytes []byte
	require.Eventually(t, func() bool {
		var err error
		pidBytes, err = os.ReadFile(pidFile)
		return err == nil && strings.TrimSpace(string(pidBytes)) != ""
	}, time.Second, 20*time.Millisecond)
	cancel()
	res := <-done
	assert.True(t, res.IsError)

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err)
	assert.Eventually(t, func() bool {
		return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() != nil
	}, time.Second, 20*time.Millisecond, "child process should be killed with the shell process group")
}
