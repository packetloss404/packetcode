package app

// Keymap descriptions exposed via /help. The actual key handling lives in
// the App.handleKeypress switch — these strings are purely documentation.
var (
	GlobalKeys = []KeyHelp{
		{"Ctrl+P", "Open provider picker"},
		{"Ctrl+M", "Open model picker"},
		{"Ctrl+C", "Cancel current generation; press twice to exit"},
		{"Ctrl+L", "Clear screen (keep session)"},
		{"Esc", "Close jobs modal / approval / picker / autocomplete popup"},
	}
	ConversationKeys = []KeyHelp{
		{"terminal scrollback", "Review finalized turns with your terminal or tmux"},
		{"live region", "Shows the current streaming reply or tool call"},
	}
	ApprovalKeys = []KeyHelp{
		{"Y", "Approve"},
		{"N / Esc", "Reject"},
	}
	InputKeys = []KeyHelp{
		{"Enter", "Send message"},
		{"Shift+Enter", "Insert newline"},
	}
	// AutocompleteKeys documents the slash-command autocomplete popup's
	// bindings. The popup opens automatically when the input buffer
	// starts with "/" and closes when a space lands after the verb.
	AutocompleteKeys = []KeyHelp{
		{"/", "Open popup (when buffer is empty or starts with /)"},
		{"↑/↓", "Move cursor"},
		{"Ctrl+N/P", "Move cursor (down/up)"},
		{"Ctrl+J/K", "Move cursor (down/up)"},
		{"Tab", "Accept highlighted suggestion"},
		{"Enter", "Accept when buffer is a bare verb; otherwise submit"},
		{"Esc", "Dismiss popup (keep typed text)"},
	}
	// PickerKeys documents the generic picker modal's bindings. The
	// provider (Ctrl+P) and model (Ctrl+M) pickers both honour every
	// row below.
	PickerKeys = []KeyHelp{
		{"↑/↓", "Move cursor"},
		{"Ctrl+N/P", "Move cursor (down/up)"},
		{"PgUp/PgDn", "Move a half page"},
		{"Enter", "Select"},
		{"Ctrl+A", "Set/update provider API key"},
		{"Esc", "Close"},
		{"Ctrl+U", "Clear filter"},
		{"r", "Retry (error state)"},
		{"type", "Filter items"},
	}
	// SlashCommands enumerates every slash command the user can type
	// into the input bar. Displayed by /help; the actual parsing lives
	// in internal/app/slashcmd.go.
	SlashCommands = []KeyHelp{
		{"/spawn <prompt>", "Spawn a background agent"},
		{"/jobs", "List background jobs"},
		{"/jobs <id>", "View a job's transcript"},
		{"/cancel <id|all>", "Cancel a job"},
		{"/provider [add [slug]|slug]", "Open picker, add key, or switch active"},
		{"/model [id]", "List models or switch active"},
		{"/sessions", "List sessions (resume|delete subcommands)"},
		{"/undo", "Undo the most recent file change"},
		{"/compact [--keep N]", "Summarise older messages to reclaim context"},
		{"/cost", "Show cost breakdown (reset --yes to clear)"},
		{"/trust [on|off]", "Toggle auto-approval of destructive tools"},
		{"/help", "Show this help message"},
		{"/clear", "Clear the transcript pane"},
		{"/statusline", "Show or refresh the configured statusline command"},
		{"/mcp", "List configured MCP servers"},
		{"/mcp logs <name>", "Tail an MCP server's stderr log"},
		{"/exit", "Quit packetcode"},
		{"/quit", "Quit packetcode"},
	}
)

type KeyHelp struct {
	Key  string
	Desc string
}
