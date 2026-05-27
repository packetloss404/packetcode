package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/packetcode/packetcode/internal/config"
)

func TestDoctorJSONOutputNoConfig(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q, stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	if report.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", report.SchemaVersion)
	}
	if report.Status != doctorWarn {
		t.Fatalf("status = %q, want warn", report.Status)
	}
	assertDoctorCheck(t, report, "config.file", doctorWarn)
	assertDoctorCheck(t, report, "providers.none", doctorWarn)
	assertDoctorCheck(t, report, "mcp.none", doctorOK)
}

func TestDoctorPlainOutputDoesNotLeakSecrets(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	cfg := config.Default()
	cfg.Default.Provider = "ollama"
	cfg.Default.Model = "gpt-4.1"
	cfg.Providers["openai"] = config.ProviderConfig{APIKey: "sk-secret-value", DefaultModel: "gpt-4.1"}
	cfg.Providers["ollama"] = config.ProviderConfig{Host: "http://user:host-secret@localhost:11434?token=query-secret", DefaultModel: "gpt-4.1"}
	cfg.MCP["secret"] = config.MCPServerConfig{
		Command: doctorTempExecutable(t),
		Args:    []string{"--token", "arg-secret-token", "--api-key=arg-secret-key"},
		Env:     map[string]string{"API_TOKEN": "top-secret-token"},
	}
	requireSaveConfig(t, cfg)

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q, stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Packetcode Doctor") {
		t.Fatalf("plain output missing header:\n%s", out)
	}
	for _, secret := range []string{"sk-secret-value", "top-secret-token", "arg-secret-token", "arg-secret-key", "host-secret", "query-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("doctor leaked secret %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"--token [REDACTED]", "--api-key=[REDACTED]", "user:%5BREDACTED%5D@localhost", "token=%5BREDACTED%5D"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing redaction marker %q:\n%s", want, out)
		}
	}
}

func TestDoctorDispatchSubcommand(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	code, ok := dispatchSubcommand([]string{"doctor", "--json", "--check", "version"}, &stdout, &stderr)
	if !ok {
		t.Fatal("doctor subcommand was not dispatched")
	}
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"id": "version.binary"`) {
		t.Fatalf("doctor dispatch did not run doctor command:\n%s", stdout.String())
	}

	if _, ok := dispatchSubcommand([]string{"unknown"}, &stdout, &stderr); ok {
		t.Fatal("unknown subcommand should not dispatch")
	}
}

func TestDoctorConfigParseErrorIsActionable(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	dir, err := config.HomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[default\nprovider = "), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1; stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	check := assertDoctorCheck(t, report, "config.file", doctorFail)
	if !strings.Contains(check.Detail, "config.toml") || check.Fix == "" {
		t.Fatalf("parse error is not actionable: %+v", check)
	}
}

func TestDoctorMissingDefaultProviderKeyFails(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	cfg := config.Default()
	cfg.Default.Provider = "openai"
	cfg.Default.Model = "gpt-4.1"
	cfg.Providers["openai"] = config.ProviderConfig{DefaultModel: "gpt-4.1"}
	requireSaveConfig(t, cfg)

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1; stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	check := assertDoctorCheck(t, report, "config.default_provider", doctorFail)
	if !strings.Contains(check.Fix, "PACKETCODE_OPENAI_API_KEY") {
		t.Fatalf("missing key fix not useful: %+v", check)
	}
}

func TestDoctorOllamaNeedsNoAPIKey(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	cfg := config.Default()
	cfg.Default.Provider = "ollama"
	cfg.Default.Model = "qwen2.5-coder:14b"
	cfg.Providers["ollama"] = config.ProviderConfig{DefaultModel: "qwen2.5-coder:14b"}
	requireSaveConfig(t, cfg)

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	assertDoctorCheck(t, report, "config.default_provider", doctorOK)
	assertDoctorCheck(t, report, "providers.ollama", doctorOK)
}

func TestDoctorMCPStaticChecks(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	cmdPath := doctorTempExecutable(t)
	disabled := false
	cfg := config.Default()
	cfg.Default.Provider = "ollama"
	cfg.Default.Model = "model"
	cfg.MCP["ok"] = config.MCPServerConfig{Command: cmdPath, Env: map[string]string{"TOKEN": "secret"}}
	cfg.MCP["disabled"] = config.MCPServerConfig{Command: "missing-disabled-command", Enabled: &disabled}
	cfg.MCP["missing"] = config.MCPServerConfig{Command: "missing-packetcode-doctor-command"}
	cfg.MCP["bad.name"] = config.MCPServerConfig{Command: cmdPath}
	requireSaveConfig(t, cfg)

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1; stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	ok := assertDoctorCheck(t, report, "mcp.ok", doctorOK)
	if strings.Contains(ok.Detail, "secret") || !strings.Contains(ok.Detail, "auth:env:TOKEN") {
		t.Fatalf("MCP auth summary leaked or omitted env key: %+v", ok)
	}
	assertDoctorCheck(t, report, "mcp.disabled", doctorSkip)
	assertDoctorCheck(t, report, "mcp.missing.command", doctorFail)
	assertDoctorCheck(t, report, "mcp.bad.name.name", doctorFail)
}

func TestResolveCommandRejectsNonExecutablePathOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows executability is extension/PATHEXT based")
	}
	path := filepath.Join(t.TempDir(), "mcp-server")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCommand(path, t.TempDir()); err == nil {
		t.Fatal("expected non-executable path command to fail")
	}
}

func TestDoctorCheckFilterLimitsSections(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--check", "config", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q stdout=%s", code, stderr.String(), stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor json: %v\n%s", err, stdout.String())
	}
	if len(report.Checks) == 0 {
		t.Fatal("expected filtered checks")
	}
	for _, check := range report.Checks {
		if check.Section != "config" {
			t.Fatalf("filtered report included non-config check: %+v", check)
		}
	}
}

func TestDoctorCheckFilterRejectsUnknown(t *testing.T) {
	restore := isolateDoctorEnv(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := runDoctorCommand([]string{"--check", "network"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("doctor exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown doctor check "network"`) {
		t.Fatalf("stderr missing unknown check error: %s", stderr.String())
	}
}

func isolateDoctorEnv(t *testing.T) func() {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PACKETCODE_OPENAI_API_KEY", "")
	t.Setenv("PACKETCODE_ANTHROPIC_API_KEY", "")
	t.Setenv("PACKETCODE_GEMINI_API_KEY", "")
	t.Setenv("PACKETCODE_MINIMAX_API_KEY", "")
	t.Setenv("PACKETCODE_OPENROUTER_API_KEY", "")
	t.Setenv("PACKETCODE_OLLAMA_HOST", "")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatal(err)
		}
	}
}

func requireSaveConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
}

func assertDoctorCheck(t *testing.T, report doctorReport, id, status string) doctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			if check.Status != status {
				t.Fatalf("%s status = %q, want %q; check=%+v", id, check.Status, status, check)
			}
			return check
		}
	}
	t.Fatalf("missing doctor check %q in %#v", id, report.Checks)
	return doctorCheck{}
}

func doctorTempExecutable(t *testing.T) string {
	t.Helper()
	name := "doctor-exec"
	content := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		name = "doctor-exec.bat"
		content = "@echo off\r\nexit /b 0\r\n"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
