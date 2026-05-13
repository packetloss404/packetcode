package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/packetcode/packetcode/internal/mcp"
)

// TestParseSlashCommand_MCP asserts the /mcp parser accepts the shapes
// documented in docs/feature-mcp.md.
func TestParseSlashCommand_MCP(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/mcp")
		if !ok || cmd != "mcp" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
		sub, name, err := parseMCPArgs(args)
		if err != nil || sub != "" || name != "" {
			t.Fatalf("parseMCPArgs(nil) = %q %q %v", sub, name, err)
		}
	})
	t.Run("logs with name", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/mcp logs foo")
		if !ok || cmd != "mcp" {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
		sub, name, err := parseMCPArgs(args)
		if err != nil || sub != "logs" || name != "foo" {
			t.Fatalf("parseMCPArgs = %q %q %v", sub, name, err)
		}
	})
	t.Run("logs without name", func(t *testing.T) {
		_, _, err := parseMCPArgs([]string{"logs"})
		if err == nil {
			t.Fatalf("expected error for /mcp logs with no name")
		}
	})
	t.Run("restart defers", func(t *testing.T) {
		_, _, err := parseMCPArgs([]string{"restart", "foo"})
		if err == nil {
			t.Fatalf("expected error for /mcp restart (Round 8)")
		}
	})
	t.Run("unknown sub", func(t *testing.T) {
		_, _, err := parseMCPArgs([]string{"reload"})
		if err == nil {
			t.Fatalf("expected error for unknown subcommand")
		}
	})
}

// TestRenderMCPTable_NoServers asserts the sentinel string when no
// servers are configured.
func TestRenderMCPTable_NoServers(t *testing.T) {
	got := renderMCPTable(nil, nil)
	if !strings.Contains(got, "no MCP servers configured") {
		t.Fatalf("expected sentinel string, got:\n%s", got)
	}
}

// TestRenderMCPTable_MixedStatuses drives three reports (running,
// disabled, failed) through the table renderer and asserts every row
// shows up plus the header.
func TestRenderMCPTable_MixedStatuses(t *testing.T) {
	reports := []mcp.StartupReport{
		{Name: "filesystem", Status: "running", ToolCount: 8, PID: 41283, Command: "npx mcp-filesystem"},
		{Name: "legacy", Status: "disabled", Command: "legacy-mcp"},
		{Name: "fetch", Status: "failed", Err: "command not found (uvx)"},
	}
	got := renderMCPTable(reports, nil)
	lines := strings.Split(got, "\n")
	// Expect: title line, header row, three body rows = 5 lines.
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "MCP servers") {
		t.Errorf("line 0 = %q, want title", lines[0])
	}
	if !strings.Contains(lines[1], "NAME") || !strings.Contains(lines[1], "STATE") || !strings.Contains(lines[1], "TOOLS") {
		t.Errorf("line 1 = %q, want column headers", lines[1])
	}
	for _, want := range []string{"filesystem", "legacy", "fetch"} {
		found := false
		for _, ln := range lines[2:] {
			if strings.Contains(ln, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected row for %q in:\n%s", want, got)
		}
	}
}

func TestRenderMCPTable_RunningWithoutLiveClientShowsExited(t *testing.T) {
	reports := []mcp.StartupReport{
		{Name: "ghost", Status: "running", ToolCount: 1, PID: 1234, Command: "ghost-mcp"},
	}
	got := renderMCPTable(reports, nil)
	if !strings.Contains(got, "exited") {
		t.Fatalf("expected stale running report to render as exited:\n%s", got)
	}
	if strings.Contains(got, "1234") {
		t.Fatalf("stale pid should not be shown:\n%s", got)
	}
}

func TestMCPReportExists_FailedServer(t *testing.T) {
	reports := []mcp.StartupReport{{Name: "broken", Status: "failed", Err: "start: no such file"}}
	if !mcpReportExists(reports, "broken") {
		t.Fatalf("failed configured server should be accepted for /mcp logs lookup")
	}
}

// TestTailMCPLog_MissingFile asserts a helpful error when the log file
// doesn't exist. We isolate HOME so tailMCPLog resolves to a temp dir
// where the file definitely doesn't exist.
func TestTailMCPLog_MissingFile(t *testing.T) {
	isolateHome(t)
	_, err := tailMCPLog("ghost", 50)
	if err == nil {
		t.Fatalf("expected error for missing log file")
	}
	if !strings.Contains(err.Error(), "no log file at") {
		t.Errorf("error = %v, want it to mention missing log file", err)
	}
}

// TestTailMCPLog_TruncatesToLast50Lines writes 100 lines to a temp
// log file and asserts the tailer returns lines 51..100 with the
// header + footer markers in place.
func TestTailMCPLog_TruncatesToLast50Lines(t *testing.T) {
	home := isolateHome(t)
	logPath := filepath.Join(home, ".packetcode", "mcp-bar.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	out, err := tailMCPLog("bar", 50)
	if err != nil {
		t.Fatalf("tailMCPLog: %v", err)
	}
	if !strings.Contains(out, "── mcp-bar.log (last 50 lines) ──") {
		t.Errorf("missing header in:\n%s", out)
	}
	if !strings.Contains(out, "── end ──") {
		t.Errorf("missing footer in:\n%s", out)
	}
	if !strings.Contains(out, "line 100") {
		t.Errorf("expected last line (line 100) in output:\n%s", out)
	}
	if !strings.Contains(out, "line 51") {
		t.Errorf("expected first kept line (line 51) in output:\n%s", out)
	}
	if strings.Contains(out, "line 50\n") {
		t.Errorf("expected dropped line (line 50) absent; output:\n%s", out)
	}
}

func TestTailMCPLog_RedactsCommonSecrets(t *testing.T) {
	home := isolateHome(t)
	logPath := filepath.Join(home, ".packetcode", "mcp-secrets.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	log := strings.Join([]string{
		`api_key=sk-live-secret`,
		`{"token":"tok_123"}`,
		`Authorization: Bearer abc.def.ghi`,
	}, "\n")
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	out, err := tailMCPLog("secrets", 50)
	if err != nil {
		t.Fatalf("tailMCPLog: %v", err)
	}
	for _, leaked := range []string{"sk-live-secret", "tok_123", "abc.def.ghi"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("secret %q leaked in:\n%s", leaked, out)
		}
	}
	if strings.Count(out, "[REDACTED]") < 3 {
		t.Fatalf("expected redaction markers in:\n%s", out)
	}
}

func TestReadLastLines_BoundsLargeLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	content := strings.Repeat("old\n", (maxMCPLogTailBytes/4)+100) + "new 1\nnew 2\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	lines, err := readLastLines(path, 2)
	if err != nil {
		t.Fatalf("readLastLines: %v", err)
	}
	if got, want := strings.Join(lines, "\n"), "new 1\nnew 2"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
}

// TestSlashHelp_IncludesMCP asserts the /help output exposes both the
// bare /mcp and the /mcp logs <name> shapes so users can discover the
// command without reading the spec.
func TestSlashHelp_IncludesMCP(t *testing.T) {
	got := renderHelp()
	for _, want := range []string{"/mcp", "/mcp logs <name>"} {
		if !strings.Contains(got, want) {
			t.Errorf("help missing %q; got:\n%s", want, got)
		}
	}
}

// isolateHome points HOME (and USERPROFILE on Windows) at a fresh
// t.TempDir so tests that touch ~/.packetcode don't stomp on the real
// user's files. Returns the temp dir.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}
