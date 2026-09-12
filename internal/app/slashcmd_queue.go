package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (a *App) handleQueueCommand(args []string) (tea.Model, tea.Cmd) {
	sub, index, err := parseQueueArgs(args)
	if err != nil {
		a.conversation.AppendSystem("queue: " + err.Error())
		return a, nil
	}
	switch sub {
	case "":
		a.conversation.AppendSystem(a.renderQueue())
	case "resume":
		if len(a.queuedInputs) == 0 {
			a.queuePaused = false
			a.conversation.AppendSystem("queue: no queued prompts")
			return a, nil
		}
		if a.streaming {
			a.conversation.AppendSystem("queue: wait for the current turn to finish, then use /queue resume")
			return a, nil
		}
		a.queuePaused = false
		a.conversation.AppendSystem("resuming queued prompts")
		return a.startNextQueuedInput()
	case "clear":
		if a.clearQueuedInputs() == 0 {
			a.conversation.AppendSystem("queue: no queued prompts")
		}
	case "drop":
		if index > len(a.queuedInputs) {
			a.conversation.AppendSystem(fmt.Sprintf("queue: index %d out of range", index))
			return a, nil
		}
		dropped := a.queuedInputs[index-1]
		copy(a.queuedInputs[index-1:], a.queuedInputs[index:])
		a.queuedInputs = a.queuedInputs[:len(a.queuedInputs)-1]
		if len(a.queuedInputs) == 0 {
			a.queuePaused = false
		}
		a.refreshTopBar()
		a.conversation.AppendSystem(fmt.Sprintf("dropped queued prompt %d: %s", index, truncOneLine(dropped.Label(), 80)))
	}
	return a, nil
}

func (a *App) renderQueue() string {
	if len(a.queuedInputs) == 0 {
		return "queue: no queued prompts"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "queued prompts (%d)\n", len(a.queuedInputs))
	if a.queuePaused {
		b.WriteString("paused after a failed turn; /queue resume to continue, /queue clear to discard\n")
	}
	now := time.Now()
	limit := len(a.queuedInputs)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		q := a.queuedInputs[i]
		fmt.Fprintf(&b, "%2d  %s  %s\n", i+1, padRight(roundedAge(q.At, now), 6), truncOneLine(q.Label(), 100))
	}
	if len(a.queuedInputs) > limit {
		fmt.Fprintf(&b, "... %d more\n", len(a.queuedInputs)-limit)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *App) pauseQueuedInputs() {
	if len(a.queuedInputs) == 0 || a.queuePaused {
		return
	}
	a.queuePaused = true
	a.conversation.AppendSystem(fmt.Sprintf("paused %d queued %s after the turn stopped; /queue to review, /queue resume to continue, /queue clear to discard", len(a.queuedInputs), plural(len(a.queuedInputs), "prompt", "prompts")))
	a.refreshTopBar()
}

// truncOneLine collapses s to a single space-separated line and clips it to
// n runes, spending the last three on an ellipsis when there is room for one.
// Rune-based throughout: byte slicing would split a multi-byte character and
// emit a replacement glyph into a table cell, and the padRight helpers that
// align those cells count runes too.
func truncOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	if n <= 3 {
		return trunc(s, n)
	}
	return trunc(s, n-3) + "..."
}
