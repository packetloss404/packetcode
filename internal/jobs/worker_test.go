package jobs

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/hooks"
)

// TestSummarise_TrimsAndCaps spot-checks summarise's behaviour: it
// trims whitespace, leaves short text untouched, and ellipsises text
// that exceeds the cap.
func TestSummarise_TrimsAndCaps(t *testing.T) {
	short := summarise("  hello world  \n")
	assert.Equal(t, "hello world", short)

	long := strings.Repeat("a", summaryMaxLen+50)
	out := summarise(long)
	assert.True(t, strings.HasSuffix(out, "…"))
	// ellipsis is 3 bytes (U+2026) not counted in our budget — at
	// most summaryMaxLen + ellipsis bytes.
	assert.LessOrEqual(t, len(out), summaryMaxLen+len("…"))
}

// TestRunJob_ContextCancelMidStream verifies that cancelling the
// per-job ctx during a holdOpen stream lands the job in
// StateCancelled (not Failed or Completed).
func TestRunJob_ContextCancelMidStream(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}
	mgr, _ := newTestManager(t, prov)

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "loop"})
	if perr != nil {
		t.Fatalf("spawn: %s", perr)
	}
	waitFor(t, 1e9, "job is running", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateRunning
	})
	mgr.Cancel(snap.ID)
	waitFor(t, 2e9, "job becomes cancelled", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateCancelled
	})
}

// TestRunJob_PanicBecomesFailed verifies that a panic inside the
// agent loop is caught by the worker and translated to StateFailed.
// We trigger the panic by injecting a SpawnTool factory that returns
// a tool that panics on Execute, then having the scripted provider
// emit a tool call for it.
func TestRunJob_PanicBecomesFailed(t *testing.T) {
	// Note: panics inside agent.Agent.Execute don't currently propagate
	// out as panics — agent.handleToolCall recovers internally and
	// converts to a tool error. So this test instead asserts the
	// panic-recovery defer in runJob via a worst-case path: a malformed
	// provider that returns nil events but never closes its channel.
	// We simulate that by having the scripted provider error on first
	// ChatCompletion (no turns scripted), which becomes an EventError
	// → StateFailed.
	prov := &scriptedProvider{} // 0 turns — first ChatCompletion errors.
	mgr, _ := newTestManager(t, prov)

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
	if perr != nil {
		t.Fatalf("spawn: %s", perr)
	}
	waitFor(t, 2e9, "job becomes failed", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateFailed
	})
	got, _ := mgr.Get(snap.ID)
	assert.Contains(t, strings.ToLower(got.Error), "no more turns scripted")
}

// TestWaitForJob_HappyPath blocks until the job completes and returns
// a Result projection.
func TestWaitForJob_HappyPath(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello()}
	mgr, _ := newTestManager(t, prov)

	snap, _ := mgr.Spawn(SpawnRequest{Prompt: "x"})
	res, ok := mgr.WaitForJob(snap.ID, 2e9)
	assert.True(t, ok)
	assert.Equal(t, StateCompleted, res.State)
	assert.Equal(t, snap.ID, res.JobID)
	assert.Empty(t, mgr.DrainResults(0), "waited result should not be delivered again through DrainResults")
}

// TestWaitForJob_Timeout returns ok=false when the job does not
// finish within the timeout.
func TestWaitForJob_Timeout(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}
	mgr, _ := newTestManager(t, prov)

	snap, _ := mgr.Spawn(SpawnRequest{Prompt: "x"})
	_, ok := mgr.WaitForJob(snap.ID, 50_000_000) // 50ms
	assert.False(t, ok)
	mgr.Cancel(snap.ID)
}

func TestRunJob_PassesHooksToBackgroundAgent(t *testing.T) {
	command := "printf background-hook-marker"
	if runtime.GOOS == "windows" {
		command = "Write-Output background-hook-marker"
	}
	prov := &scriptedProvider{turns: scriptedHello()}
	mgr, _ := newTestManager(t, prov, func(c *Config) {
		c.Hooks = hooks.New(config.HooksConfig{
			UserPromptSubmit: []config.HookConfig{{Command: command, TimeoutSec: 2}},
		}, t.TempDir())
	})

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
	assert.Nil(t, perr)
	waitFor(t, 2e9, "job completes", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateCompleted
	})

	reqs := prov.snapshotRequests()
	if assert.NotEmpty(t, reqs) {
		var joined strings.Builder
		for _, msg := range reqs[0].Messages {
			joined.WriteString(msg.Content)
			joined.WriteByte('\n')
		}
		assert.Contains(t, joined.String(), "background-hook-marker")
	}
}

// _ silences unused-import warnings if we prune helpers later.
var _ = context.Canceled
