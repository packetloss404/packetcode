package statusline

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

func TestRunner_RenderPassesJSONOnStdin(t *testing.T) {
	command := "read input; case \"$input\" in *gpt-test*) printf custom-status;; *) exit 1;; esac"
	timeoutSec := 2
	if runtime.GOOS == "windows" {
		command = "$data = [Console]::In.ReadToEnd(); if ($data -match 'gpt-test') { 'custom-status' } else { exit 1 }"
		timeoutSec = 5
	}
	r := New(config.StatusLineConfig{Command: command, TimeoutSec: timeoutSec}, t.TempDir())
	require.NotNil(t, r)

	out, err := r.Render(context.Background(), Snapshot{
		Provider: ProviderInfo{Slug: "openai", DisplayName: "OpenAI"},
		Model:    ModelInfo{ID: "gpt-test"},
	})
	require.NoError(t, err)
	assert.Equal(t, "custom-status", out)
}

func TestNew_DisabledWithoutCommand(t *testing.T) {
	assert.Nil(t, New(config.StatusLineConfig{}, t.TempDir()))
}

func TestRunner_RenderTimeoutMessage(t *testing.T) {
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 5"
	}
	r := New(config.StatusLineConfig{Command: command, TimeoutSec: 1}, t.TempDir())
	require.NotNil(t, r)

	start := time.Now()
	_, err := r.Render(context.Background(), Snapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out after 1s")
	assert.Contains(t, err.Error(), "process tree cancellation requested")
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestRunner_RenderTruncatesStdout(t *testing.T) {
	command := "yes s | head -c 70000"
	if runtime.GOOS == "windows" {
		command = "$out = 's' * 70000; [Console]::Out.Write($out)"
	}
	r := New(config.StatusLineConfig{Command: command, TimeoutSec: 5}, t.TempDir())
	require.NotNil(t, r)

	out, err := r.Render(context.Background(), Snapshot{})
	require.NoError(t, err)
	assert.Contains(t, out, "stdout truncated at 64KB")
	assert.Less(t, len(out), 70*1024)
	assert.False(t, strings.Contains(out, strings.Repeat("s", 70000)))
}
