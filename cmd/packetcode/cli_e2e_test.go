package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/packetcode/packetcode/internal/config"
)

// These tests execute the built binary, not command dispatch seams. The only
// service is a loopback protocol fixture; no account or developer state is used.
func TestCLIEndToEnd(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "packetcode")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	// A parent task bridge must not redirect the isolated Sugar fixture.
	t.Setenv("SUGAR_TASK_API_ROOT", "https://host-task.example/api/v1")
	workspace, home := t.TempDir(), t.TempDir()
	fixture := &cliProviderFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	cfg := config.Default()
	cfg.Default.Provider, cfg.Default.Model = "e2e", "e2e-model"
	cfg.Behavior.ProviderMaxRetries = 1
	cfg.Providers = map[string]config.ProviderConfig{"e2e": {
		Type: "openai_compatible", BaseURL: server.URL + "/v1",
		APIKeyEnv: "PACKETCODE_E2E_API_KEY", DefaultModel: "e2e-model",
		Models: []config.ProviderModelConfig{{ID: "e2e-model"}},
	}}
	if err := cfg.SaveTo(filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	command := func(test *testing.T, args ...string) *exec.Cmd {
		test.Helper()
		ctx, cancel := context.WithTimeout(test.Context(), 30*time.Second)
		test.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			key = strings.ToUpper(key)
			if strings.HasPrefix(key, "PACKETCODE_") || key == "HOME" || key == "USERPROFILE" || key == "SUGAR_TASK_API_ROOT" {
				continue
			}
			cmd.Env = append(cmd.Env, entry)
		}
		cmd.Env = append(cmd.Env, "PACKETCODE_HOME="+home, "HOME="+home, "USERPROFILE="+home,
			"PACKETCODE_E2E_API_KEY=e2e-token")
		return cmd
	}
	run := func(test *testing.T, wantCode int, args ...string) (string, string) {
		test.Helper()
		var stdout, stderr bytes.Buffer
		cmd := command(test, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				test.Fatalf("run %q: %v", args, err)
			}
			code = exit.ExitCode()
		}
		if code != wantCode {
			test.Fatalf("%q exit=%d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, &stdout, &stderr)
		}
		return stdout.String(), stderr.String()
	}

	t.Run("advertised_help", func(t *testing.T) {
		for _, args := range [][]string{
			{"--help"}, {"run", "--help"}, {"doctor", "--help"}, {"skills", "--help"},
			{"skills", "list", "--help"}, {"skills", "install", "--help"},
			{"skills", "remove", "--help"}, {"skills", "path", "--help"},
			{"acp", "--help"}, {"sugar", "--help"}, {"sugar", "login", "--help"},
		} {
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				stdout, stderr := run(t, 0, args...)
				if stdout == "" || stderr != "" {
					t.Fatalf("help must be on stdout only: stdout=%q stderr=%q", stdout, stderr)
				}
			})
		}
	})

	t.Run("invalid_arguments_do_not_start_work", func(t *testing.T) {
		for _, args := range [][]string{
			{"--permission-mode", "read-only", "run", "should not execute"},
			{"run"}, {"run", "--unknown"}, {"doctor", "unexpected"},
			{"skills", "list", "unexpected"}, {"skills", "path", "unexpected"},
			{"sugar", "login", "unexpected"},
		} {
			stdout, stderr := run(t, 2, args...)
			if stdout != "" || stderr == "" {
				t.Fatalf("usage failure must be on stderr only: stdout=%q stderr=%q", stdout, stderr)
			}
		}
		if fixture.count() != 0 {
			t.Fatal("invalid/help command contacted the model")
		}
	})

	t.Run("doctor_isolated_json", func(t *testing.T) {
		stdout, _ := run(t, 0, "doctor", "--check", "config", "--json")
		var report doctorReport
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatal(err)
		}
		if report.SchemaVersion != 1 || report.EffectiveHome != home || report.Status == doctorFail {
			t.Fatalf("doctor report = %+v", report)
		}
	})

	t.Run("headless_json_and_persisted_resume", func(t *testing.T) {
		stdout, _ := run(t, 0, "run", "--json", "--permission-mode", "read-only", "first e2e prompt")
		var first, second runResult
		if err := json.Unmarshal([]byte(stdout), &first); err != nil {
			t.Fatal(err)
		}
		if !first.OK || first.SchemaVersion != 1 || first.SessionID == "" || first.Output != "e2e answer" || first.Usage.InputTokens != 11 {
			t.Fatalf("first result = %+v", first)
		}
		stdout, _ = run(t, 0, "run", "--json", "--resume", first.SessionID, "second e2e prompt")
		if err := json.Unmarshal([]byte(stdout), &second); err != nil {
			t.Fatal(err)
		}
		if !second.OK || second.SessionID != first.SessionID || second.Output != "e2e answer" {
			t.Fatalf("resumed result = %+v", second)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		last := fixture.requests[len(fixture.requests)-1]
		if !wireMessagesContainInOrder(last.Messages, []runWireMessage{
			{Role: "user", Content: "first e2e prompt"},
			{Role: "assistant", Content: "e2e answer"},
			{Role: "user", Content: "second e2e prompt"},
		}) {
			t.Fatalf("resumed wire history = %+v", last.Messages)
		}
	})

	t.Run("native_write_approval_and_failure_json", func(t *testing.T) {
		artifact := filepath.Join(workspace, "e2e-proof.txt")
		stdout, _ := run(t, 0, "run", "--permission-mode", "accept-edits", "E2E_WRITE")
		if strings.TrimSpace(stdout) != "e2e tool complete" {
			t.Fatalf("plain stdout includes unexpected text: %q", stdout)
		}
		data, err := os.ReadFile(artifact)
		if err != nil || string(data) != "written by actual CLI" {
			t.Fatalf("artifact = %q, %v", data, err)
		}
		if err := os.Remove(artifact); err != nil {
			t.Fatal(err)
		}
		stdout, stderr := run(t, 3, "run", "--permission-mode", "ask", "--json", "E2E_WRITE")
		var failure runResult
		if err := json.Unmarshal([]byte(stdout), &failure); err != nil {
			t.Fatal(err)
		}
		if failure.OK || failure.SessionID == "" || !strings.Contains(failure.Error, "requires approval") || !strings.Contains(stderr, failure.SessionID) {
			t.Fatalf("approval result=%+v stderr=%q", failure, stderr)
		}
		if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unapproved write occurred: %v", err)
		}
	})

	t.Run("skills_install_list_remove", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "e2e-skill")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: e2e-skill\ndescription: Deterministic CLI fixture\n---\nInspect the project.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, 0, "skills", "install", source, "--project")
		stdout, _ := run(t, 0, "skills", "list", "--json")
		var list struct {
			Skills []skillListEntry `json:"skills"`
		}
		if err := json.Unmarshal([]byte(stdout), &list); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, skill := range list.Skills {
			if skill.Name == "e2e-skill" && skill.Loaded && skill.UserInvocable && skill.Source == "project" {
				found = true
			}
		}
		if !found {
			t.Fatalf("installed skill absent/inert: %s", stdout)
		}
		run(t, 0, "skills", "remove", "e2e-skill", "--project")
		if _, err := os.Stat(filepath.Join(workspace, ".packetcode", "skills", "e2e-skill")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed skill still exists: %v", err)
		}
		broken := filepath.Join(workspace, ".packetcode", "skills", "broken")
		if err := os.MkdirAll(broken, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("---\nname: broken\n---\nMissing required description.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _ = run(t, 1, "skills", "list", "--json")
		var invalid struct {
			Errors []string `json:"errors"`
		}
		if err := json.Unmarshal([]byte(stdout), &invalid); err != nil || len(invalid.Errors) == 0 {
			t.Fatalf("malformed skills must fail with JSON diagnostics: %s (%v)", stdout, err)
		}
		if err := os.RemoveAll(broken); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acp_stdio_native_turn", func(t *testing.T) {
		cmd := command(t, "acp", "--permission-mode", "read-only")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		request := func(id int, method string, params any) map[string]json.RawMessage {
			t.Helper()
			if err := json.NewEncoder(stdin).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
				t.Fatal(err)
			}
			for scanner.Scan() {
				var response map[string]json.RawMessage
				if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if string(response["id"]) != fmt.Sprint(id) {
					continue
				}
				if response["error"] != nil {
					t.Fatalf("ACP %s: %s", method, response["error"])
				}
				return response
			}
			t.Fatalf("ACP closed before %s: %v", method, scanner.Err())
			return nil
		}
		request(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
		created := request(2, "session/new", map[string]any{"cwd": workspace, "mcpServers": []any{}})
		var session struct {
			ID string `json:"sessionId"`
		}
		if err := json.Unmarshal(created["result"], &session); err != nil || session.ID == "" {
			t.Fatalf("ACP session: %s (%v)", created["result"], err)
		}
		response := request(3, "session/prompt", map[string]any{"sessionId": session.ID, "prompt": []any{map[string]any{"type": "text", "text": "ACP e2e prompt"}}})
		if !bytes.Contains(response["result"], []byte("end_turn")) {
			t.Fatalf("ACP prompt result = %s", response["result"])
		}
		request(4, "session/close", map[string]any{"sessionId": session.ID})
		if err := stdin.Close(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("ACP exit: %v, stderr=%s", err, &stderr)
		}
	})

	t.Run("sugar_device_login_persists_discovered_model", func(t *testing.T) {
		var base string
		service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/auth/device/code":
				fmt.Fprintf(w, `{"device_code":"fixture-device","user_code":"E2E-CODE","verification_uri":"%s/connect","expires_in":30,"interval":1}`, base)
			case "/api/v1/auth/device/token":
				fmt.Fprint(w, `{"token":"sgr_e2e_fixture","name":"CLI fixture"}`)
			case "/api/v1/models":
				if r.Header.Get("Authorization") != "Bearer sgr_e2e_fixture" {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				fmt.Fprint(w, `{"data":[{"id":"sugar/conduit","object":"model"}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer service.Close()
		base = service.URL
		stdout, _ := run(t, 0, "sugar", "login", "--server", base, "--name", "e2e-machine", "--no-browser")
		if !strings.Contains(stdout, "1 live models available") || strings.Contains(stdout, "sgr_e2e_fixture") {
			t.Fatalf("login output = %q", stdout)
		}
		saved, err := config.LoadFrom(filepath.Join(home, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if saved.Providers["sugar"].APIKey != "sgr_e2e_fixture" || saved.Default.Provider != "sugar" || saved.Default.Model != "sugar/conduit" {
			t.Fatal("login did not persist credential and selected model")
		}
	})
}

type cliProviderFixture struct {
	mu       sync.Mutex
	requests []runWireRequest
}

func (f *cliProviderFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }

func (f *cliProviderFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer e2e-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var request runWireRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	lastUser, sawTool := "", false
	for _, message := range request.Messages {
		if message.Role == "user" {
			lastUser, sawTool = message.Content, false
		}
		if message.Role == "tool" {
			sawTool = true
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(value any) { data, _ := json.Marshal(value); fmt.Fprintf(w, "data: %s\n\n", data) }
	finish := "stop"
	delta := map[string]any{"role": "assistant", "content": "e2e answer"}
	if strings.Contains(lastUser, "E2E_WRITE") && !sawTool {
		finish = "tool_calls"
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "e2e-write", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"e2e-proof.txt","content":"written by actual CLI"}`}}}}
	} else if sawTool {
		delta["content"] = "e2e tool complete"
	}
	send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
	send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
	send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}
