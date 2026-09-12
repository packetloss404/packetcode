package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/ui/components/input"
)

func TestTurnFailureDoesNotStartDependentQueuedPrompt(t *testing.T) {
	r := newTestApp(t)
	r.prov.turns = [][]provider.StreamEvent{{{Type: provider.EventDone}}}
	wireAgent(r, r.prov)
	r.app.streaming = true
	r.app.queueInput("deploy after the tests pass")
	r.app.handleAgentEvent(agent.AgentEvent{Type: agent.EventError, Error: errors.New("tests failed")})
	_, cmd := r.app.Update(agentDoneMsg{})
	pump := newDrainPump(t, r.app, cmd)
	t.Cleanup(func() { pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming }) })
	assert.False(t, r.app.streaming, "failed prerequisite must not start the queued prompt")
	assert.Len(t, r.app.queuedInputs, 1, "queued prompt must remain recoverable")
}

func TestCtrlCMarksTurnFailedBeforeBufferedCompletion(t *testing.T) {
	r := newTestApp(t)
	r.app.streaming = true
	r.app.cancelTurn = func() {}
	r.app.activeLoopID = "loop1"
	r.app.loops = map[string]*loopState{"loop1": {id: "loop1", mode: loopSelfPaced, body: "keep editing"}}
	r.app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.True(t, r.app.turnFailed, "cancel intent must survive a buffered success event")
	r.app.Update(agentDoneMsg{})
	require.False(t, r.app.streaming)
	require.Empty(t, r.app.loops)
}

func TestStoppingLoopRemovesItsQueuedTurn(t *testing.T) {
	for _, args := range [][]string{{"keep", "editing"}, {"1m", "keep", "editing"}} {
		for _, target := range []string{"loop1", "all"} {
			t.Run(args[0]+"/"+target, func(t *testing.T) {
				r := newTestApp(t)
				r.app.streaming = true
				r.app.queueInput("unrelated prompt")
				r.app.handleLoopCommand(args)
				require.Len(t, r.app.queuedInputs, 2)
				r.app.stopLoop([]string{target})
				require.Len(t, r.app.queuedInputs, 1)
				require.Equal(t, "unrelated prompt", r.app.queuedInputs[0].Text)
			})
		}
	}
}

func TestQueueResumeIsExplicitAndRunsOnce(t *testing.T) {
	r := newTestApp(t)
	r.prov.turns = [][]provider.StreamEvent{{{Type: provider.EventDone}}}
	wireAgent(r, r.prov)
	r.app.queueInput("next")
	r.app.pauseQueuedInputs()
	require.Contains(t, r.app.renderQueue(), "paused")
	r.app.streaming = true
	r.app.handleQueueCommand([]string{"resume"})
	require.True(t, r.app.queuePaused)
	require.Len(t, r.app.queuedInputs, 1)
	r.app.streaming = false
	_, cmd := r.app.handleQueueCommand([]string{"resume"})
	require.False(t, r.app.queuePaused)
	require.Empty(t, r.app.queuedInputs)
	pump := newDrainPump(t, r.app, cmd)
	pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming })
	require.False(t, r.app.streaming)
	require.EqualValues(t, 1, atomic.LoadInt32(&r.prov.turnIdx))
	r.app.handleQueueCommand([]string{"resume"})
	require.EqualValues(t, 1, atomic.LoadInt32(&r.prov.turnIdx))
}

func TestQueueResumeParser(t *testing.T) {
	sub, _, err := parseQueueArgs([]string{"resume"})
	require.NoError(t, err)
	require.Equal(t, "resume", sub)
	_, _, err = parseQueueArgs([]string{"resume", "unexpected"})
	require.Error(t, err)
}

func TestTurnErrorWithoutDescriptionDoesNotPanic(t *testing.T) {
	r := newTestApp(t)
	r.app.handleAgentEvent(agent.AgentEvent{Type: agent.EventError})
	require.True(t, r.app.turnFailed)
	convContains(t, r.app, "turn failed without an error description")
}

func TestPausedQueueRetainsNewPromptsUntilExplicitResume(t *testing.T) {
	r := newTestApp(t)
	r.app.queueInput("first")
	r.app.pauseQueuedInputs()
	r.app.Update(input.SubmitMsg{Text: "second"})
	require.False(t, r.app.streaming)
	require.Len(t, r.app.queuedInputs, 2)
	require.Equal(t, "second", r.app.queuedInputs[1].Text)
	convContains(t, r.app, "added to the paused queue")
	// UI clearing and a successful completion must never resume old work.
	r.app.handleClearCommand(nil)
	r.app.Update(agentDoneMsg{})
	require.True(t, r.app.queuePaused)
	require.Len(t, r.app.queuedInputs, 2)
	r.app.handleQueueCommand([]string{"drop", "1"})
	require.True(t, r.app.queuePaused)
	r.app.handleQueueCommand([]string{"drop", "1"})
	require.False(t, r.app.queuePaused)
	r.app.queueInput("third")
	r.app.pauseQueuedInputs()
	r.app.handleQueueCommand([]string{"clear"})
	require.False(t, r.app.queuePaused)
	require.Empty(t, r.app.queuedInputs)
}

func TestCompactionFailurePausesPendingPrompts(t *testing.T) {
	for _, failed := range []bool{true, false} {
		t.Run(map[bool]string{true: "provider failure", false: "changed session"}[failed], func(t *testing.T) {
			r := newTestApp(t)
			r.app.streaming = true
			r.app.skipAutoCompactOnce = true
			r.app.queueInput("large pending prompt")
			msg := compactDoneMsg{sessionID: "different-session"}
			if failed {
				msg.err = errors.New("provider unavailable")
			}
			r.app.handleCompactDone(msg)
			require.True(t, r.app.queuePaused)
			require.False(t, r.app.streaming)
			require.False(t, r.app.skipAutoCompactOnce)
			require.Len(t, r.app.queuedInputs, 1)
		})
	}
}

func TestPausedQueueDoesNotAccumulateIntervalTicks(t *testing.T) {
	r := newTestApp(t)
	r.app.queueInput("pending")
	r.app.pauseQueuedInputs()
	r.app.handleLoopCommand([]string{"1m", "check", "build"})
	require.Len(t, r.app.queuedInputs, 2)
	r.app.Update(loopTickMsg{id: "loop1"})
	r.app.Update(loopTickMsg{id: "loop1"})
	require.Len(t, r.app.queuedInputs, 2)
	require.Equal(t, 1, r.app.loops["loop1"].iterations)
}

func TestStoppingLoopRemovesQueuedSkillInvocation(t *testing.T) {
	r, _ := skillRig(t, "Deploy it.")
	r.app.streaming = true
	r.app.handleLoopCommand([]string{"1m", "/deploy"})
	require.Len(t, r.app.queuedInputs, 1)
	r.app.stopLoop([]string{"loop1"})
	require.Empty(t, r.app.queuedInputs)
}

func TestCompactionSaveFailurePausesPendingPrompts(t *testing.T) {
	r := newTestApp(t)
	cur := r.sessions.Current()
	path := filepath.Join(r.tmp, "sessions", cur.ID+".json")
	require.NoError(t, os.Remove(path))
	// A directory at the target filename makes atomic publication fail.
	require.NoError(t, os.Mkdir(path, 0o700))
	r.app.streaming = true
	r.app.skipAutoCompactOnce = true
	r.app.queueInput("pending")
	r.app.handleCompactDone(compactDoneMsg{sessionID: cur.ID, after: []provider.Message{{Role: provider.RoleUser, Content: "summary"}}})
	require.True(t, r.app.queuePaused)
	require.False(t, r.app.skipAutoCompactOnce)
	require.Len(t, r.app.queuedInputs, 1)
	convContains(t, r.app, "compact: save failed")
}

func TestSessionSwitchDoesNotResumePausedQueue(t *testing.T) {
	r := newTestApp(t)
	previous := r.sessions.Current().ID
	next, err := r.sessions.New("fake", "fake-model")
	require.NoError(t, err)
	_, err = r.sessions.Load(previous)
	require.NoError(t, err)
	r.app.queueInput("old pending prompt")
	r.app.pauseQueuedInputs()
	r.app.resumeSessionByID(next.ID, "resume")
	require.Equal(t, next.ID, r.sessions.Current().ID)
	require.True(t, r.app.queuePaused)
	require.Len(t, r.app.queuedInputs, 1)
	require.False(t, r.app.streaming)
}

func TestFailedLoopCompactionDoesNotOwnNextOrdinaryTurn(t *testing.T) {
	for _, discard := range [][]string{{"clear"}, {"drop", "1"}} {
		t.Run(discard[0], func(t *testing.T) {
			r := newTestApp(t)
			r.prov.turns = [][]provider.StreamEvent{{{Type: provider.EventDone}}}
			wireAgent(r, r.prov)
			for i := 0; i <= defaultCompactKeep; i++ {
				require.NoError(t, r.sessions.AddMessage(provider.Message{Role: provider.RoleUser, Content: "history"}))
			}
			require.NoError(t, r.sessions.SetContextTokens(90_000))
			r.app.handleLoopCommand([]string{"keep", "editing"})
			require.True(t, r.app.streaming)
			require.Len(t, r.app.queuedInputs, 1)
			r.app.handleCompactDone(compactDoneMsg{sessionID: r.sessions.Current().ID, err: errors.New("provider unavailable")})
			require.Empty(t, r.app.activeLoopID, "failed compaction never began the loop's agent turn")
			r.app.handleQueueCommand(discard)
			require.False(t, r.app.queuePaused)
			require.Empty(t, r.app.queuedInputs)
			require.NoError(t, r.sessions.SetContextTokens(1))
			_, cmd := r.app.Update(input.SubmitMsg{Text: "ordinary prompt"})
			pump := newDrainPump(t, r.app, cmd)
			t.Cleanup(func() {
				if r.app.cancelTurn != nil {
					r.app.cancelTurn()
				}
				pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming })
			})
			require.Empty(t, r.app.activeLoopID)
			pump.RunUntil(2*time.Second, func() bool { return !r.app.streaming })
			require.False(t, r.app.streaming)
			require.EqualValues(t, 1, atomic.LoadInt32(&r.prov.turnIdx), "ordinary completion must not restart the discarded loop")
			require.Equal(t, 1, r.app.loops["loop1"].iterations)
		})
	}
}
