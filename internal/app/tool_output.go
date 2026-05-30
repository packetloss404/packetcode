package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scheduleToolOutputFlush returns a command that fires a single
// toolOutputFlushMsg after toolOutputThrottle. It is the timer half of the
// live tool-output throttle: bufferToolOutput arms it (at most one in
// flight at a time), and flushToolOutput drains the coalesced buffer into
// the conversation's live region when it fires. Using a one-shot Tick
// (re-armed only while output keeps arriving) means the timer goes fully
// idle between commands rather than ticking forever.
func scheduleToolOutputFlush() tea.Cmd {
	return tea.Tick(toolOutputThrottle, func(time.Time) tea.Msg {
		return toolOutputFlushMsg{}
	})
}
