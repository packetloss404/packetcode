package app

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/mcp"
)

// tailLogLineCount is the number of trailing stderr log lines shown by
// /mcp logs <name>. Matches the spec's 50-line window.
const tailLogLineCount = 50

// maxMCPLogTailBytes bounds how much of an append-only MCP stderr log
// /mcp logs reads into memory. The command is a diagnostic tail, not a
// full log viewer.
const maxMCPLogTailBytes = 256 * 1024

var (
	mcpBearerSecretRE = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	mcpKeyValueRE     = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)(["']?\s*[:=]\s*["']?)[^"',\s}]+`)
)

// handleMCPCommand routes the /mcp slash command. Empty args renders
// the configured-servers table; `logs <name>` tails the named server's
// stderr log.
func (a *App) handleMCPCommand(args []string) (tea.Model, tea.Cmd) {
	if a.mcp == nil {
		a.conversation.AppendSystem("mcp: manager not available")
		return a, nil
	}
	sub, name, err := parseMCPArgs(args)
	if err != nil {
		a.conversation.AppendSystem("mcp: " + err.Error())
		return a, nil
	}
	switch sub {
	case "":
		a.conversation.AppendSystem(renderMCPTable(a.mcp.Reports(), a.mcp.Clients()))
		return a, nil
	case "logs":
		if _, ok := a.mcp.Client(name); !ok && !mcpReportExists(a.mcp.Reports(), name) {
			a.conversation.AppendSystem(fmt.Sprintf("mcp logs: no server named %s", name))
			return a, nil
		}
		out, err := tailMCPLog(name, tailLogLineCount)
		if err != nil {
			a.conversation.AppendSystem("mcp logs: " + err.Error())
			return a, nil
		}
		a.conversation.AppendSystem(out)
		return a, nil
	}
	// Unreachable: parseMCPArgs rejects any other shape.
	a.conversation.AppendSystem("mcp: unexpected subcommand")
	return a, nil
}

func mcpReportExists(reports []mcp.StartupReport, name string) bool {
	for _, r := range reports {
		if r.Name == name {
			return true
		}
	}
	return false
}

// renderMCPTable formats a monospace ASCII table of configured MCP
// servers. Widths: NAME 12, STATE 10, TOOLS 6, PID 7, COMMAND fills
// the remainder (truncated with "..." on overflow). Empty reports
// produces the "nothing configured" sentinel.
func renderMCPTable(reports []mcp.StartupReport, clients []*mcp.Client) string {
	if len(reports) == 0 {
		return "no MCP servers configured (add [mcp.<name>] to ~/.packetcode/config.toml)"
	}
	// Build a name→client map so we can pull live pid + command details
	// from the Client (rather than rely solely on the static report).
	byName := map[string]*mcp.Client{}
	for _, c := range clients {
		if c != nil {
			byName[c.Name()] = c
		}
	}

	var b strings.Builder
	b.WriteString("MCP servers\n")
	b.WriteString(padRight("NAME", 12))
	b.WriteString(" ")
	b.WriteString(padRight("STATE", 10))
	b.WriteString(" ")
	b.WriteString(padRight("TOOLS", 6))
	b.WriteString(" ")
	b.WriteString(padRight("PID", 7))
	b.WriteString(" ")
	b.WriteString("COMMAND")
	b.WriteString("\n")

	for _, r := range reports {
		status := r.Status
		pid := "-"
		if status == "running" {
			if c, ok := byName[r.Name]; !ok || c == nil || !c.IsAlive() {
				status = "exited"
			} else if r.PID > 0 {
				pid = fmt.Sprintf("%d", r.PID)
			}
		}
		tools := fmt.Sprintf("%d", r.ToolCount)

		r.Status = status
		command := commandForReport(r)

		fmt.Fprintf(&b, "%s %s %s %s %s\n",
			padRight(trunc(r.Name, 12), 12),
			padRight(trunc(status, 10), 10),
			padRight(tools, 6),
			padRight(pid, 7),
			command,
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

// commandForReport returns the COMMAND column text for a StartupReport.
// Failed and exited servers prefer their error message. Other states
// show the configured command when available.
func commandForReport(r mcp.StartupReport) string {
	const maxWidth = 48
	switch r.Status {
	case "failed", "exited":
		msg := r.Err
		if msg == "" {
			msg = r.Status
		}
		return trunc(msg, maxWidth)
	case "disabled":
		if r.Command == "" {
			return "(disabled)"
		}
		return trunc(r.Command, maxWidth)
	default:
		return trunc(r.Command, maxWidth)
	}
}

// trunc is defined in app.go. padRight lives in slashcmd_help.go. Both
// are package-level helpers and are reused here.

// tailMCPLog reads the per-server stderr log file and returns its last
// `n` lines framed by a header + footer so the user can see where the
// snippet starts and ends. Missing-log-file produces an error whose
// message includes the expected path.
func tailMCPLog(name string, n int) (string, error) {
	path, err := config.MCPLogPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve log path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no log file at %s", path)
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a log file", path)
	}
	lines, err := readLastLines(path, n)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "── mcp-%s.log (last %d lines) ──\n", name, n)
	for _, ln := range lines {
		b.WriteString(redactMCPLogLine(ln))
		b.WriteString("\n")
	}
	b.WriteString("── end ──")
	return b.String(), nil
}

// readLastLines reads at most the last maxMCPLogTailBytes from path and
// returns its last n lines in original order.
func readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 || n <= 0 {
		return nil, nil
	}

	start := int64(0)
	if size > maxMCPLogTailBytes {
		start = size - maxMCPLogTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	if text == "" {
		return nil, nil
	}
	all := strings.Split(text, "\n")
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

func redactMCPLogLine(line string) string {
	line = mcpBearerSecretRE.ReplaceAllString(line, "Bearer [REDACTED]")
	return mcpKeyValueRE.ReplaceAllString(line, `${1}${2}[REDACTED]`)
}
