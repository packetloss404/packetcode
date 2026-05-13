package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParseSlashCommand inspects a raw input line and, if it starts with "/"
// and names a recognised verb, returns the command name (without the
// leading slash) and its whitespace-split arguments. ok=false means the
// input is not a slash command and should be treated as a normal user
// prompt.
func ParseSlashCommand(text string) (cmd string, args []string, ok bool) {
	return NewBuiltinSlashRegistry().Parse(text)
}

func parseSlashCommandFields(text string) (cmd string, args []string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return "", nil, false
	}
	body := strings.TrimPrefix(trimmed, "/")
	if body == "" {
		return "", nil, false
	}
	fields := strings.Fields(body)
	return fields[0], fields[1:], true
}

func slashCommandText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//")
}

func escapedSlashPrompt(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "//") {
		return "", false
	}
	return strings.TrimPrefix(trimmed, "/"), true
}

func slashCommandArguments(text, cmd string) string {
	trimmed := strings.TrimSpace(text)
	body := strings.TrimPrefix(trimmed, "/")
	if body == cmd {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(body, cmd))
}

// ParseSpawnFlags handles the argument tail of a /spawn command. Accepted
// flags (in any order, preceding the prompt):
//
//	--provider <slug>
//	--model    <id>
//	--write
//
// After the last flag value, every remaining token (joined by single
// spaces) forms the prompt. An empty prompt is a user error.
func ParseSpawnFlags(args []string) (provSlug, modelID string, allowWrite bool, prompt string, err error) {
	var rest []string
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "--provider":
			if i+1 >= len(args) {
				return "", "", false, "", errors.New("--provider requires a value")
			}
			provSlug = args[i+1]
			i += 2
		case "--model":
			if i+1 >= len(args) {
				return "", "", false, "", errors.New("--model requires a value")
			}
			modelID = args[i+1]
			i += 2
		case "--write":
			allowWrite = true
			i++
		default:
			// First non-flag token starts the prompt. Everything after
			// it is also part of the prompt, even if it looks like a
			// flag — matches the spec's "flag-then-prompt" simplicity.
			rest = args[i:]
			i = len(args)
		}
	}
	prompt = strings.TrimSpace(strings.Join(rest, " "))
	if prompt == "" {
		return "", "", false, "", errors.New("prompt is required")
	}
	return provSlug, modelID, allowWrite, prompt, nil
}

// parseCompactFlags reads the optional `--keep <N>` flag for /compact.
// Default keep is 10. Extra positional tokens are an error so typos like
// "/compact 5" (missing --keep) don't silently succeed.
func parseCompactFlags(args []string) (keep int, err error) {
	keep = 10
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--keep":
			if i+1 >= len(args) {
				return 0, errors.New("--keep requires a value")
			}
			n, convErr := strconv.Atoi(args[i+1])
			if convErr != nil {
				return 0, fmt.Errorf("--keep must be a positive integer")
			}
			if n <= 0 {
				return 0, fmt.Errorf("--keep must be a positive integer")
			}
			keep = n
			i += 2
		default:
			return 0, fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	return keep, nil
}

// parseSessionsArgs parses the /sessions argument tail. Accepted shapes:
//
//	<nothing>
//	resume <id>
//	delete <id>
//	delete <id> --yes
//
// The handler resolves <id> to a full session ID; this parser only cares
// about shape.
func parseSessionsArgs(args []string) (sub, id string, yes bool, err error) {
	if len(args) == 0 {
		return "", "", false, nil
	}
	sub = args[0]
	if sub != "resume" && sub != "delete" {
		return "", "", false, fmt.Errorf("unknown subcommand %q (want \"resume\" or \"delete\")", sub)
	}
	if len(args) < 2 {
		return "", "", false, fmt.Errorf("%s: missing session id", sub)
	}
	id = args[1]
	for _, a := range args[2:] {
		switch a {
		case "--yes":
			yes = true
		default:
			return "", "", false, fmt.Errorf("unexpected argument %q", a)
		}
	}
	return sub, id, yes, nil
}

// parseCostArgs parses the /cost argument tail. Accepted shapes:
//
//	<nothing>
//	reset
//	reset --yes
func parseCostArgs(args []string) (reset, yes bool, err error) {
	if len(args) == 0 {
		return false, false, nil
	}
	if args[0] != "reset" {
		return false, false, fmt.Errorf("unknown subcommand %q (want \"reset\")", args[0])
	}
	reset = true
	for _, a := range args[1:] {
		switch a {
		case "--yes":
			yes = true
		default:
			return false, false, fmt.Errorf("unexpected argument %q", a)
		}
	}
	return reset, yes, nil
}

// parseMCPArgs parses the /mcp argument tail. Accepted shapes:
//
//	<nothing>             — list configured servers
//	logs <name>           — tail the named server's stderr log
//	status <name>         — show server health/config detail
//	tools <name>          — list tools exposed by one server
//
// /mcp restart <name> is deferred to Round 8; it is parsed just enough
// to return a friendly "not yet supported" error rather than falling
// through to "unknown subcommand".
func parseMCPArgs(args []string) (sub, name string, err error) {
	if len(args) == 0 {
		return "", "", nil
	}
	switch args[0] {
	case "logs", "status", "tools":
		if len(args) < 2 {
			return "", "", fmt.Errorf("%s requires a server name", args[0])
		}
		if len(args) > 2 {
			return "", "", fmt.Errorf("unexpected argument %q", args[2])
		}
		return args[0], args[1], nil
	case "restart":
		return "", "", fmt.Errorf("restart not yet supported — restart packetcode to reconnect")
	default:
		return "", "", fmt.Errorf("unknown subcommand %q", args[0])
	}
}

// parseTrustArgs parses the /trust argument tail. Accepted shapes:
//
//	<nothing>      — query current state
//	on             — enable trust mode
//	off            — disable trust mode
//
// When args is empty, set=false; otherwise set=true and value encodes
// the on/off choice.
func parseTrustArgs(args []string) (set, value bool, err error) {
	if len(args) == 0 {
		return false, false, nil
	}
	switch args[0] {
	case "on":
		if len(args) > 1 {
			return false, false, fmt.Errorf("unexpected argument %q", args[1])
		}
		return true, true, nil
	case "off":
		if len(args) > 1 {
			return false, false, fmt.Errorf("unexpected argument %q", args[1])
		}
		return true, false, nil
	default:
		return false, false, fmt.Errorf("unknown value %q (want \"on\" or \"off\")", args[0])
	}
}
