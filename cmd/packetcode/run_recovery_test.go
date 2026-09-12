package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/packetcode/packetcode/internal/mcp"
)

func TestRunFailureReportsSessionAndSafeDiagnostics(t *testing.T) {
	errText := "provider unavailable\x1b]52;c;ZXZpbA==\a; try later"
	withRunExecutor(t, func(context.Context, runCommandOptions, io.Writer) (runResult, error) {
		return runResult{SessionID: "session-1", Output: "unfinished answer"}, errors.New(errText)
	})
	for _, jsonMode := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		args := []string{"prompt"}
		if jsonMode {
			args = append([]string{"--json"}, args...)
		}
		if code := runRunCommand(args, &stdout, &stderr); code != runExitError {
			t.Fatalf("exit = %d", code)
		}
		for _, want := range []string{"provider unavailable", "session-1", "--resume", "saved history"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("missing %q in failure diagnostic: %q", want, stderr.String())
			}
		}
		if strings.ContainsAny(stderr.String(), "\x1b\a") {
			t.Errorf("terminal control in failure diagnostic: %q", stderr.String())
		}
		if jsonMode {
			var result runResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK || result.Error != errText || result.Output != "unfinished answer" {
				t.Errorf("machine-readable failure changed: %+v", result)
			}
		} else if stdout.Len() != 0 {
			t.Errorf("partial answer presented on plain stdout: %q", stdout.String())
		}
	}
}

func TestRunFailureResumeCommandUsesOnlyCanonicalUUID(t *testing.T) {
	for _, id := range []string{"ef8c22bb-3fc2-4274-a452-0232d72ed571", "$(untrusted)", "legacy-session"} {
		var out bytes.Buffer
		writeRunFailure(&out, runResult{SessionID: id}, context.Canceled)
		commandID := "ID"
		if strings.HasPrefix(id, "ef8c22bb") {
			commandID = id
		}
		if !strings.Contains(out.String(), "Use packetcode --resume "+commandID+" from this directory") {
			t.Errorf("unexpected recovery command: %q", out.String())
		}
	}
}

func TestRunMCPDiagnosticsStripTerminalControls(t *testing.T) {
	var out bytes.Buffer
	writeRunMCPReports(&out, []mcp.StartupReport{{Name: "server\x1b[2K", Status: "failed", Err: "broken\x1b]52;c;ZXZpbA==\a"}})
	if strings.ContainsAny(out.String(), "\x1b\a") || !strings.Contains(out.String(), "server: failed — broken") {
		t.Fatalf("unsafe or missing diagnostic: %q", out.String())
	}
}

func TestRunApprovalFailureExplainsInteractiveRecovery(t *testing.T) {
	withRunExecutor(t, func(context.Context, runCommandOptions, io.Writer) (runResult, error) {
		return runResult{}, errRunApprovalUnavailable
	})
	var stdout, stderr bytes.Buffer
	if code := runRunCommand([]string{"prompt"}, &stdout, &stderr); code != runExitApprovalUnavailable {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "interactive") || !strings.Contains(stderr.String(), "review") {
		t.Errorf("approval failure has no recovery guidance: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "--resume") {
		t.Errorf("offered session recovery without a session: %q", stderr.String())
	}
}
