package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
)

// handleSessionsCommand lists, resumes, or deletes sessions. The bare
// form shows the top 20 newest-first; resume/delete accept either a
// full ID or any unique 8-char prefix; delete is gated on --yes because
// it is irreversible.
func (a *App) handleSessionsCommand(args []string) (tea.Model, tea.Cmd) {
	sub, id, yes, err := parseSessionsArgs(args)
	if err != nil {
		a.conversation.AppendSystem("sessions: " + err.Error())
		return a, nil
	}

	if sub == "" {
		summaries, listErr := a.deps.Sessions.List()
		if listErr != nil {
			a.conversation.AppendSystem("sessions: list failed: " + listErr.Error())
			return a, nil
		}
		currentID := ""
		if cur := a.deps.Sessions.Current(); cur != nil {
			currentID = cur.ID
		}
		a.conversation.AppendSystem(renderSessionsTable(summaries, currentID))
		return a, nil
	}

	fullID, resolveErr := a.resolveSessionID(id)
	if resolveErr != nil {
		a.conversation.AppendSystem("sessions: " + resolveErr.Error())
		return a, nil
	}

	switch sub {
	case "resume":
		if a.streaming {
			a.conversation.AppendSystem("sessions: turn already running; press Ctrl+C to cancel before resuming")
			return a, nil
		}
		prev := a.deps.Sessions.Current()
		s, loadErr := a.deps.Sessions.Load(fullID)
		if loadErr != nil {
			a.conversation.AppendSystem("sessions: " + loadErr.Error())
			return a, nil
		}
		if s.Provider == "" || s.Model == "" {
			a.restorePreviousSession(prev)
			a.conversation.AppendSystem("sessions: resumed session has no provider/model metadata")
			return a, nil
		}
		if err := a.deps.Registry.SetActive(s.Provider, s.Model); err != nil {
			a.restorePreviousSession(prev)
			a.conversation.AppendSystem("sessions: " + err.Error())
			return a, nil
		}
		if err := a.rebindSessionScopedTools(s.ID); err != nil {
			a.restorePreviousSession(prev)
			a.conversation.AppendSystem("sessions: " + err.Error())
			return a, nil
		}
		a.refreshTopBar()
		a.showResumedSession(s)
		return a, a.renderStatusLine(false)

	case "delete":
		if !yes {
			a.conversation.AppendSystem(fmt.Sprintf(
				"sessions: refusing to delete without --yes; re-run: /sessions delete %s --yes",
				id,
			))
			return a, nil
		}
		current := a.deps.Sessions.Current()
		deletingActive := current != nil && current.ID == fullID
		if deletingActive && a.streaming {
			a.conversation.AppendSystem("sessions: turn already running; press Ctrl+C to cancel before deleting the active session")
			return a, nil
		}
		var replacement *session.Session
		if deletingActive {
			providerSlug, modelID := current.Provider, current.Model
			if providerSlug == "" || modelID == "" {
				if prov, activeModel := a.deps.Registry.Active(); prov != nil {
					providerSlug = prov.Slug()
					modelID = activeModel
				}
			}
			if providerSlug == "" || modelID == "" {
				a.conversation.AppendSystem("sessions: cannot delete active session without provider/model metadata")
				return a, nil
			}
			var newErr error
			replacement, newErr = a.deps.Sessions.New(providerSlug, modelID)
			if newErr != nil {
				a.conversation.AppendSystem("sessions: create replacement session: " + newErr.Error())
				return a, nil
			}
		}
		if delErr := a.deps.Sessions.Delete(fullID); delErr != nil {
			if replacement != nil {
				_, _ = a.deps.Sessions.Load(fullID)
				_ = a.deps.Sessions.Delete(replacement.ID)
				_ = a.rebindSessionScopedTools(fullID)
			}
			a.conversation.AppendSystem("sessions: " + delErr.Error())
			return a, nil
		}
		if cleanupErr := a.cleanupSessionBackups(fullID); cleanupErr != nil {
			a.conversation.AppendSystem("sessions: backup cleanup failed: " + cleanupErr.Error())
			return a, nil
		}
		if replacement != nil {
			if err := a.rebindSessionScopedTools(replacement.ID); err != nil {
				a.conversation.AppendSystem("sessions: " + err.Error())
				return a, nil
			}
		}
		a.refreshTopBar()
		a.conversation.AppendSystem("deleted session " + shortID(fullID))
		return a, nil
	}

	// Unreachable: parseSessionsArgs rejects anything else.
	a.conversation.AppendSystem("sessions: unexpected subcommand " + sub)
	return a, nil
}

func (a *App) restorePreviousSession(prev *session.Session) {
	if prev == nil {
		return
	}
	_, _ = a.deps.Sessions.Load(prev.ID)
}

func (a *App) rebindSessionScopedTools(sessionID string) error {
	bk := a.backups
	if bk == nil {
		bk = a.deps.Backups
	}
	if bk == nil {
		return nil
	}
	if err := bk.SwitchSession(sessionID); err != nil {
		return fmt.Errorf("rebind backups: %w", err)
	}
	a.backups = bk
	a.deps.Backups = bk
	if a.deps.Tools != nil {
		if t, ok := a.deps.Tools.Get("write_file"); ok {
			if wt, ok := t.(*tools.WriteFileTool); ok {
				wt.Backups = bk
			}
		}
		if t, ok := a.deps.Tools.Get("patch_file"); ok {
			if pt, ok := t.(*tools.PatchFileTool); ok {
				pt.Backups = bk
			}
		}
	}
	return nil
}

func (a *App) cleanupSessionBackups(sessionID string) error {
	bk := a.backups
	if bk == nil {
		bk = a.deps.Backups
	}
	if bk == nil {
		return nil
	}
	return bk.CleanupSession(sessionID)
}

func (a *App) showResumedSession(s *session.Session) {
	conv := conversation.New()
	if a.deps.Version != "" {
		conv.SetVersion(a.deps.Version)
	} else {
		conv.SetVersion("v1")
	}
	conv.Resize(a.width, a.height)
	a.conversation = conv
	a.conversation.AppendSystem(fmt.Sprintf(
		"resumed session %s (%s) — %s/%s — %d messages",
		s.Name, shortID(s.ID), s.Provider, s.Model, len(s.Messages),
	))
	a.appendSessionTranscript(s.Provider, s.Model, s.Messages)
}

func (a *App) appendSessionTranscript(providerSlug, modelID string, messages []provider.Message) {
	consumedToolResults := map[int]bool{}
	for i, msg := range messages {
		switch msg.Role {
		case provider.RoleUser:
			a.conversation.AppendUser(msg.Content)
		case provider.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				a.conversation.AppendAgentText(modelID, providerSlug, msg.Content)
				a.conversation.FinaliseAgent()
			}
			for _, call := range msg.ToolCalls {
				res, idx, ok := matchingToolResult(messages, i+1, call)
				if !ok {
					a.conversation.AppendSystem(fmt.Sprintf("tool call pending: %s %s", call.Name, call.Arguments))
					continue
				}
				consumedToolResults[idx] = true
				a.conversation.AppendToolCall(call.Name, call.Arguments)
				a.conversation.CompleteToolCall(call.Name, tools.ToolResult{Content: res.Content})
			}
		case provider.RoleTool:
			if consumedToolResults[i] {
				continue
			}
			name := msg.Name
			if name == "" {
				name = "tool"
			}
			a.conversation.AppendSystem(fmt.Sprintf("%s result: %s", name, msg.Content))
		case provider.RoleSystem:
			a.conversation.AppendSystem(msg.Content)
		}
	}
}

func matchingToolResult(messages []provider.Message, start int, call provider.ToolCall) (provider.Message, int, bool) {
	for i := start; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != provider.RoleTool {
			return provider.Message{}, 0, false
		}
		if call.ID != "" && msg.ToolCallID == call.ID {
			return msg, i, true
		}
		if call.ID == "" && msg.Name == call.Name {
			return msg, i, true
		}
	}
	return provider.Message{}, 0, false
}

// resolveSessionID accepts either a full session ID (exact match) or a
// unique 8-character prefix. Returns an error when nothing matches or
// when the prefix is ambiguous.
func (a *App) resolveSessionID(prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("empty session id")
	}
	summaries, err := a.deps.Sessions.List()
	if err != nil {
		return "", fmt.Errorf("list failed: %w", err)
	}
	// Exact match first — full UUIDs are always unambiguous.
	for _, s := range summaries {
		if s.ID == prefix {
			return s.ID, nil
		}
	}
	if len(prefix) != 8 {
		return "", fmt.Errorf("session id prefix %q must be exactly 8 characters or a full session id", prefix)
	}
	// Prefix match. The table shows 8 characters, so only that shortened
	// form is accepted to avoid surprising partial matches.
	var matches []string
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matches %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q — matches %d sessions", prefix, len(matches))
	}
}

// renderSessionsTable formats bare /sessions output. Widths: id=8,
// name=40, age=6, prov/model=22, active=5. The top 20 sessions render;
// any overflow is dropped silently (we only expose this list to guide
// users to a specific id).
func renderSessionsTable(summaries []session.Summary, currentID string) string {
	if len(summaries) == 0 {
		return "no sessions"
	}
	if len(summaries) > 20 {
		summaries = summaries[:20]
	}
	var b strings.Builder
	b.WriteString("  ID       NAME                                     AGE    PROV/MODEL             ACTIVE\n")
	now := time.Now()
	for _, s := range summaries {
		marker := "  "
		active := "no"
		if s.ID == currentID {
			marker = "* "
			active = "yes"
		}
		name := s.Name
		if len(name) > 40 {
			name = name[:37] + "..."
		}
		provModel := s.Provider
		if s.Model != "" {
			if provModel != "" {
				provModel += "/" + s.Model
			} else {
				provModel = s.Model
			}
		}
		if provModel == "" {
			provModel = "(none)"
		}
		age := roundedAge(s.UpdatedAt, now)
		fmt.Fprintf(&b, "%s%s %s %s %s %s\n",
			marker,
			padRight(shortID(s.ID), 8),
			padRight(name, 40),
			padRight(age, 6),
			padRight(trunc(provModel, 22), 22),
			padRight(active, 5),
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

// shortID returns the first 8 characters of a session UUID, suitable
// for display in tables.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// roundedAge renders the age of a session as "45s" / "15m" / "2h" / "1d".
func roundedAge(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		s := int(d.Seconds())
		if s < 1 {
			s = 1
		}
		return fmt.Sprintf("%ds", s)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}
