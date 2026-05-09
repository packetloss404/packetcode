package jobs

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

// summaryMaxLen caps the auto-extracted job summary surfaced to the
// main conversation. Per spec ~280 chars (one tweet's worth).
const summaryMaxLen = 280

// runJob is the per-job worker goroutine. It blocks on the manager's
// semaphore (honouring MaxConcurrent), then builds the per-job session,
// backups, tool registry, and agent. It consumes the agent's event
// channel until terminal and then publishes the final snapshot via
// markTerminal. Panics inside the agent loop are recovered and
// translated to StateFailed.
//
// jobCtx is allocated by Manager.Spawn (with its CancelFunc already
// registered into m.cancel) so /cancel works while the job is still
// queued for a sem slot.
func (m *Manager) runJob(j *Job, req SpawnRequest, jobCtx context.Context) {
	defer m.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			m.markTerminal(j, StateFailed,
				"", fmt.Sprintf("worker panic: %v\n%s", r, stack), "panic",
				j.InputTokens, j.OutputTokens, j.CostUSD, j.Transcript)
		}
	}()

	// Acquire the semaphore (Queued → Running barrier). Watch jobCtx
	// so /cancel on a queued job aborts before consuming a slot.
	select {
	case m.sem <- struct{}{}:
	case <-jobCtx.Done():
		// Cancelled while queued.
		m.markTerminal(j, StateCancelled, "", "", "cancelled while queued",
			j.InputTokens, j.OutputTokens, j.CostUSD, nil)
		return
	case <-m.baseCtx.Done():
		// Manager shut down before we got a slot.
		m.markTerminal(j, StateCancelled, "", "", "manager shutdown before start",
			j.InputTokens, j.OutputTokens, j.CostUSD, nil)
		return
	}
	defer func() { <-m.sem }()

	// jobCtx already wired into m.cancel by Spawn(). No additional
	// registration needed; just check it hasn't been cancelled while
	// we were waiting.
	if jobCtx.Err() != nil {
		m.markTerminal(j, StateCancelled, "", "", "cancelled before start",
			j.InputTokens, j.OutputTokens, j.CostUSD, nil)
		return
	}

	m.markRunning(j)

	// Build the per-job dependencies.
	subSession, sessErr := m.openSubSession(j)
	if sessErr != nil {
		m.markTerminal(j, StateFailed, "", "open sub-session: "+sessErr.Error(), "",
			j.InputTokens, j.OutputTokens, j.CostUSD, nil)
		return
	}
	backups := session.NewBackupManager(m.cfg.BackupsDir, j.SessionID)

	subRegistry, regErr := m.buildJobProviderRegistry(j)
	if regErr != nil {
		m.markTerminal(j, StateFailed, "", "build provider registry: "+regErr.Error(), "",
			j.InputTokens, j.OutputTokens, j.CostUSD, nil)
		return
	}

	// Snapshot the late-bindable config fields under the read lock so
	// SetSpawnToolFactory/SetApprover (which take the write lock) don't
	// race with our reads here.
	m.mu.RLock()
	spawnToolFactory := m.cfg.SpawnTool
	systemPromptFor := m.cfg.SystemPromptFor
	parentApprover := m.cfg.Approver
	hookRunner := m.cfg.Hooks
	maxDepth := m.cfg.MaxDepth
	m.mu.RUnlock()

	// Conditionally include spawn_agent only when the new job's depth
	// is below MaxDepth-1 (so its children would still be inside the
	// cap). Bucket B passes its SpawnAgentTool factory through here.
	var extraTools []tools.Tool
	if spawnToolFactory != nil && j.Depth < maxDepth-1 {
		if t := spawnToolFactory(j.ID, j.Depth, j.AllowWrite); t != nil {
			extraTools = append(extraTools, t)
		}
	}
	toolReg := m.buildJobToolRegistry(j.Depth, j.AllowWrite, j.ID, backups, extraTools)

	systemPrompt := req.SystemPrompt
	if systemPrompt == "" && systemPromptFor != nil {
		systemPrompt = systemPromptFor(j.Depth)
	}

	approver := NewJobApprover(parentApprover, j.ID, j.AllowWrite)

	a := agent.New(agent.Config{
		Registry:     subRegistry,
		Tools:        toolReg,
		Session:      subSession,
		CostTracker:  m.cfg.CostTracker,
		Approver:     approver,
		SystemPrompt: systemPrompt,
		Hooks:        hookRunner,
	})

	events := a.Run(jobCtx, j.Prompt)
	m.consumeEvents(j, jobCtx, events, subSession)
}

// consumeEvents drains the agent event channel, updating job
// counters as usage events arrive and recording the final assistant
// text for the summary. On EventDone we mark Completed; on EventError
// we mark Failed; on ctx cancellation we mark Cancelled.
func (m *Manager) consumeEvents(j *Job, ctx context.Context, events <-chan agent.AgentEvent, sess *session.Manager) {
	var lastAssistantText strings.Builder
	var inflightAssistant strings.Builder
	var lastErr error
	var sawDone bool

	for ev := range events {
		switch ev.Type {
		case agent.EventTextDelta:
			inflightAssistant.WriteString(ev.Text)
		case agent.EventToolCallExecuted:
			// A tool call ends the current "assistant turn"; reset the
			// inflight buffer so we capture only the FINAL assistant
			// text (the one preceding EventDone).
			inflightAssistant.Reset()
		case agent.EventUsageUpdate:
			m.applyUsage(j, ev.Usage)
		case agent.EventDone:
			sawDone = true
			// Flush the assistant text accumulated since the last tool
			// call into the "last assistant text" capture.
			if inflightAssistant.Len() > 0 {
				lastAssistantText.Reset()
				lastAssistantText.WriteString(inflightAssistant.String())
				inflightAssistant.Reset()
			}
		case agent.EventError:
			lastErr = ev.Error
		}
	}

	transcript := snapshotTranscript(sess)

	// Order of precedence for terminal state:
	//   1. ctx cancelled (Cancel/CancelAll/Shutdown) → Cancelled
	//   2. EventError received → Failed
	//   3. EventDone received → Completed
	//   4. Channel closed without Done → Failed (treat as silent error)
	if ctx.Err() != nil {
		m.markTerminal(j, StateCancelled, summarise(lastAssistantText.String()), "", "",
			j.InputTokens, j.OutputTokens, j.CostUSD, transcript)
		return
	}
	if lastErr != nil {
		m.markTerminal(j, StateFailed, summarise(lastAssistantText.String()), lastErr.Error(), "",
			j.InputTokens, j.OutputTokens, j.CostUSD, transcript)
		return
	}
	if sawDone {
		m.markTerminal(j, StateCompleted, summarise(lastAssistantText.String()), "", "",
			j.InputTokens, j.OutputTokens, j.CostUSD, transcript)
		return
	}
	m.markTerminal(j, StateFailed, summarise(lastAssistantText.String()),
		"agent stream closed without Done event", "",
		j.InputTokens, j.OutputTokens, j.CostUSD, transcript)
}

// applyUsage records a usage delta from a stream completion against the
// job's running totals and the shared cost tracker. The job's per-token
// counts are running highs (we accumulate deltas), since per-stream
// usage in this codebase is typically a per-stream total — see
// agent.run's lastUsage behaviour. We mirror what session.Manager does
// internally.
func (m *Manager) applyUsage(j *Job, usage provider.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.InputTokens += usage.InputTokens
	j.OutputTokens += usage.OutputTokens
	if m.cfg.PricingFor != nil {
		in, out := m.cfg.PricingFor(j.Provider, j.Model)
		j.CostUSD = float64(j.InputTokens)*in/1_000_000 + float64(j.OutputTokens)*out/1_000_000
	}
}

// openSubSession creates the per-job session.Manager rooted at
// SessionsDir, deriving its session id from the parent's main id (or
// "main" if there is none) plus the job's short id. The resulting
// Manager has Current() set; callers can pass it straight into
// agent.New.
//
// session.Manager.New() generates a fresh UUID with no public
// override, so we hand-write the initial session file under our
// deterministic id and then Load() it. This keeps the Backup directory
// and cost-tracker key in sync with j.SessionID.
func (m *Manager) openSubSession(j *Job) (*session.Manager, error) {
	if m.cfg.SessionsDir == "" {
		// Tests that don't care about persistence may leave SessionsDir
		// empty — fall back to a brand-new in-memory manager.
		sm := session.NewManager("")
		_, err := sm.New(j.Provider, j.Model)
		return sm, err
	}
	if err := writeInitialSubSession(m.cfg.SessionsDir, j); err != nil {
		return nil, err
	}
	sm := session.NewManager(m.cfg.SessionsDir)
	if _, err := sm.Load(j.SessionID); err != nil {
		return nil, err
	}
	return sm, nil
}

// snapshotTranscript copies the current session's messages into a
// fresh slice so the Job's transcript field shares no mutable state
// with the underlying session.Manager.
func snapshotTranscript(sm *session.Manager) []provider.Message {
	if sm == nil {
		return nil
	}
	cur := sm.Current()
	if cur == nil {
		return nil
	}
	out := make([]provider.Message, len(cur.Messages))
	copy(out, cur.Messages)
	return out
}

// summarise extracts the final user-facing summary from the last
// assistant text. We simply trim and cap at summaryMaxLen, appending an
// ellipsis when truncated.
func summarise(text string) string {
	t := strings.TrimSpace(text)
	if len(t) <= summaryMaxLen {
		return t
	}
	// Trim on a rune boundary by walking back to the previous word
	// break — avoids slicing inside a multibyte sequence and keeps the
	// result readable.
	cut := summaryMaxLen
	for cut > 0 && (t[cut]&0xC0) == 0x80 {
		cut--
	}
	out := strings.TrimRight(t[:cut], " \t\n")
	return out + "…"
}
