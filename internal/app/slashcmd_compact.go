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
	sessionID string
	beforeTok int
	keep      int
	after     []provider.Message
	err       error
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

	cmd := runCompact(ctx, a.contextMgr, prov, modelID, cur.ID, before, beforeTok, keep)
	return a, tea.Batch(a.spinner.Start("Compacting..."), cmd)
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
) tea.Cmd {
	before = append([]provider.Message(nil), before...)
	return func() tea.Msg {
		after, err := contextMgr.Compact(ctx, prov, modelID, before, keep)
		return compactDoneMsg{
			sessionID: sessionID,
			beforeTok: beforeTok,
			keep:      keep,
			after:     after,
			err:       err,
		}
	}
}

func (a *App) handleCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	a.streaming = false
	a.spinner.Stop()
	if a.cancelTurn != nil {
		a.cancelTurn()
		a.cancelTurn = nil
	}

	if msg.err != nil {
		if isCancellation(msg.err) {
			a.conversation.AppendSystem("compact cancelled")
		} else {
			a.conversation.AppendSystem("compact: " + msg.err.Error())
		}
		return a, nil
	}

	cur := a.deps.Sessions.Current()
	if cur == nil || cur.ID != msg.sessionID {
		a.conversation.AppendSystem("compact: session changed before save; discarded result")
		return a, nil
	}

	if saveErr := a.deps.Sessions.ReplaceMessages(msg.after); saveErr != nil {
		a.conversation.AppendSystem("compact: save failed: " + saveErr.Error())
		return a, nil
	}

	afterTok := a.contextMgr.EstimateTokens(msg.after)
	a.conversation.AppendSystem(fmt.Sprintf(
		"compacted: %d -> %d tokens (kept %d recent messages)",
		msg.beforeTok, afterTok, msg.keep,
	))
	a.refreshTopBar()
	return a, nil
}
