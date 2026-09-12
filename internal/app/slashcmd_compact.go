package app

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/provider"
)

type compactDoneMsg struct {
	sessionID  string
	beforeTok  int
	keep       int
	after      []provider.Message
	usage      *provider.Usage
	inputRate  float64
	outputRate float64
	err        error
}

// handleCompactCommand summarises the middle of the conversation via a
// single LLM round trip. The LLM call runs as a Bubble Tea command so
// the TUI can keep ticking and Ctrl+C can cancel the in-flight request.
func (a *App) handleCompactCommand(args []string) (tea.Model, tea.Cmd) {
	if a.streaming {
		a.conversation.AppendSystem("compact: turn already running; press Ctrl+C to cancel before compacting")
		return a, nil
	}

	keep, err := parseCompactFlags(args)
	if err != nil {
		a.conversation.AppendSystem("compact: " + err.Error())
		return a, nil
	}
	prov, modelID := a.deps.Registry.Active()
	if prov == nil {
		a.conversation.AppendSystem("compact: no active provider")
		return a, nil
	}
	cur := a.deps.Sessions.Current()
	if cur == nil {
		a.conversation.AppendSystem("compact: no session loaded")
		return a, nil
	}

	before := cur.Messages
	beforeTok := a.contextMgr.EstimateTokens(before)
	a.conversation.AppendSystem(fmt.Sprintf("compacting context... (~%d tokens)", beforeTok))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	a.streaming = true
	a.cancelTurn = cancel
	a.setOperation("compacting")

	inputRate, outputRate := prov.Pricing(modelID)
	cmd := runCompact(ctx, a.contextMgr, prov, modelID, cur.ID, before, beforeTok, keep, inputRate, outputRate)
	return a, tea.Batch(a.spinner.Start("Compacting…"), cmd)
}

func runCompact(
	ctx context.Context,
	contextMgr *agent.ContextManager,
	prov provider.Provider,
	modelID string,
	sessionID string,
	before []provider.Message,
	beforeTok int,
	keep int,
	inputRate float64,
	outputRate float64,
) tea.Cmd {
	before = append([]provider.Message(nil), before...)
	return func() tea.Msg {
		after, usage, err := contextMgr.CompactWithUsage(ctx, prov, modelID, before, keep)
		return compactDoneMsg{
			sessionID:  sessionID,
			beforeTok:  beforeTok,
			keep:       keep,
			after:      after,
			usage:      usage,
			inputRate:  inputRate,
			outputRate: outputRate,
			err:        err,
		}
	}
}

func (a *App) handleCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	a.streaming = false
	a.spinner.Stop()
	a.clearOperation()
	if a.cancelTurn != nil {
		a.cancelTurn()
		a.cancelTurn = nil
	}

	if msg.err != nil {
		a.skipAutoCompactOnce = false
		if isCancellation(msg.err) {
			a.conversation.AppendSystem("compact cancelled")
		} else {
			a.conversation.AppendSystem("compact: " + msg.err.Error())
		}
		a.pauseQueuedInputs()
		return a, nil
	}

	cur := a.deps.Sessions.Current()
	if cur == nil || cur.ID != msg.sessionID {
		a.conversation.AppendSystem("compact: session changed before save; discarded result")
		a.skipAutoCompactOnce = false
		a.pauseQueuedInputs()
		return a, nil
	}

	if saveErr := a.deps.Sessions.ReplaceMessagesAfterCompaction(msg.after); saveErr != nil {
		a.conversation.AppendSystem("compact: save failed: " + saveErr.Error())
		a.skipAutoCompactOnce = false
		a.pauseQueuedInputs()
		return a, nil
	}
	if msg.usage != nil {
		if usageErr := a.deps.Sessions.UpdateUsage(*msg.usage, msg.inputRate, msg.outputRate); usageErr != nil {
			a.conversation.AppendSystem("compact: usage update failed: " + usageErr.Error())
		}
	}

	afterTok := a.contextMgr.EstimateRequest(a.deps.SystemPrompt, msg.after, a.activeToolDefinitions()).Total
	if usageErr := a.deps.Sessions.SetContextTokens(afterTok); usageErr != nil {
		a.conversation.AppendSystem("compact: context update failed: " + usageErr.Error())
	}
	a.conversation.AppendSystem(fmt.Sprintf(
		"compacted: %d -> %d tokens (kept %d recent messages)",
		msg.beforeTok, afterTok, msg.keep,
	))
	a.refreshTopBar()
	return a.startNextQueuedInput()
}

func (a *App) activeToolDefinitions() []provider.ToolDefinition {
	prov, modelID := a.deps.Registry.Active()
	if prov == nil || a.deps.Tools == nil || !prov.SupportsTools(modelID) {
		return nil
	}
	return a.deps.Tools.Definitions()
}
