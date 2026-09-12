package app

// Keymap descriptions exposed via /help. The actual key handling lives in
// the App.handleKeypress switch — these strings are purely documentation.
var (
	GlobalKeys = []KeyHelp{
		{"Ctrl+P", "Open provider picker"},
		{"/model", "Open model picker (Alt+M when Alt is reported distinctly)"},
		{"Shift+Tab", "Cycle permission mode, including during an active turn (normal → accept edits → auto → plan)"},
		{"Ctrl+C", "Cancel generation, clear a draft, or quit from an empty prompt"},
		{"Ctrl+D", "Quit from an empty prompt"},
		{"Ctrl+L", "Clear screen (keep session)"},
		{"Esc", "Close jobs modal / approval / picker / autocomplete popup"},
	}
	ConversationKeys = []KeyHelp{
		{"terminal scrollback", "Review finalized turns with your terminal or tmux"},
		{"live region", "Shows the current streaming reply or tool call"},
	}
	ApprovalKeys = []KeyHelp{
		{"Y", "Approve"},
		{"A", "Approve + don't ask again this session"},
		{"N / Esc", "Reject"},
	}
	InputKeys = []KeyHelp{
		{"Enter", "Send message"},
		{"Alt+Enter / \\ then Enter", "Insert newline (Ctrl+J also works while completion is closed)"},
		{"↑/↓", "Recall previous / next prompt (when caret is at top/bottom)"},
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
	// provider (Ctrl+P) and model (Alt+M) pickers both honour every
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
		{"/spawn [--computer <name>] [--write] <prompt>", "Spawn a local or Packet Computer background agent"},
		{"/agents", "List background agents"},
		{"/agents <id>", "View a background agent transcript"},
		{"/jobs", "List background jobs"},
		{"/jobs <id>", "View a job's transcript"},
		{"/jobs resubmit [id]", "Re-run a job abandoned by a previous app exit (new run, not a resumption)"},
		{"/cancel <id|all>", "Cancel a job"},
		{"/computers", "List registered Packet Computers"},
		{"/computers status <name>", "Show a registered computer's stored record"},
		{"/computers ssh <name> <user@host> <root> --fingerprint <SHA256:...>", "Register a pinned SSH computer"},
		{"/computers remove <name> --yes", "Remove a computer record"},
		{"/provider [add [slug]|slug]", "Open picker, add key, or switch active"},
		{"/providers", "Alias for /provider — open the provider picker"},
		{"/model [id]", "List models or switch active"},
		{"/models", "Alias for /model — open the model picker"},
		{"/effort [level]", "Show or set model reasoning effort"},
		{"/resume", "Pick a previous session to resume"},
		{"/resume <id>", "Resume a session by full ID or 8-char prefix"},
		{"/sessions", "List sessions"},
		{"/sessions resume <id>", "Resume a session by full ID or 8-char prefix"},
		{"/sessions rename <name>", "Rename the current session"},
		{"/sessions delete <id> --yes", "Delete a saved session"},
		{"/queue", "List queued foreground prompts"},
		{"/queue resume", "Resume prompts paused after a failed turn"},
		{"/queue clear", "Clear queued foreground prompts"},
		{"/queue drop <n>", "Drop one queued prompt"},
		{"/undo", "Undo the most recent file change"},
		{"/compact [--keep N]", "Summarise older messages to reclaim context"},
		{"/cost", "Show cost breakdown (reset --yes to clear)"},
		{"/plan [on|off]", "Read-only research mode: propose a plan before editing"},
		{"/loop [interval] <prompt|/cmd>", "Repeat a prompt/command (interval or self-paced); /loop list, /loop stop"},
		{"/workflows [run [--computer <name>] <name>|validate <name>|list|stop [id|all]|<id>]", "Run, validate, and monitor local or remote multi-agent workflows"},
		{"/trust [on|off]", "Toggle 'bypass permissions' (auto-approve all tools; not in the Shift+Tab cycle)"},
		{"/permissions", "Show or change tool approval policy"},
		{"/help", "Show this help message"},
		{"/clear", "Clear the transcript pane"},
		{"/statusline", "Show or refresh the configured statusline command"},
		{"/mcp", "List configured MCP servers"},
		{"/mcp status <name>", "Show MCP server health details"},
		{"/mcp tools <name>", "List tools exposed by an MCP server"},
		{"/mcp logs <name>", "Tail an MCP server's stderr log"},
		{"/mcp restart <name>", "Reconnect one MCP server process"},
		{"/skills", "List resolved skills (builtin, user, project) and load warnings"},
		{"/skills <name>", "Show one skill's scope, path, trust, and description"},
		{"/ollama [status|models|ps|pull <model>]", "Local Ollama status, models, and management"},
		{"/transcript", "Open the current session transcript"},
		{"/exit", "Quit packetcode"},
		{"/quit", "Quit packetcode"},
	}
)

type KeyHelp struct {
	Key  string
	Desc string
}
