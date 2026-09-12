package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/git"
	"github.com/packetcode/packetcode/internal/mcp"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/ui/terminaltext"
)

const (
	runExitError               = 1
	runExitUsage               = 2
	runExitApprovalUnavailable = 3
	runExitCanceled            = 130
)

var errRunApprovalUnavailable = errors.New("an action requires approval, but packetcode run is non-interactive")

type runCommandOptions struct {
	Provider       string
	Model          string
	PermissionMode string
	ResumeID       string
	JSON           bool
	Prompt         string
}

type runUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type runResult struct {
	SchemaVersion int      `json:"schema_version"`
	OK            bool     `json:"ok"`
	SessionID     string   `json:"session_id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Output        string   `json:"output"`
	ElapsedMS     int64    `json:"elapsed_ms"`
	Usage         runUsage `json:"usage"`
	Error         string   `json:"error,omitempty"`
}

// executeRunCommand is a seam for command-contract tests. The production
// implementation below owns the full runtime lifecycle; tests can exercise
// parsing and output without credentials, provider I/O, or durable sessions.
var executeRunCommand = executeRunWithRuntime

func runRunCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerFlag := fs.String("provider", "", "override the configured provider")
	modelFlag := fs.String("model", "", "override the configured model")
	permissionFlag := fs.String("permission-mode", "", "override permission profile (ask, accept-edits, auto, read-only, bypass)")
	resumeFlag := fs.String("resume", "", "resume a saved session by ID")
	jsonFlag := fs.Bool("json", false, "write one machine-readable JSON result")
	// stdout is the answer stream for automation, so usage goes there only
	// when it was asked for; a flag error prints it to stderr.
	fs.Usage = func() { printRunUsage(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printRunUsage(stdout)
			return 0
		}
		return runExitUsage
	}
	if fs.NArg() == 0 || strings.TrimSpace(strings.Join(fs.Args(), " ")) == "" {
		fmt.Fprintln(stderr, "packetcode run: prompt is required")
		fmt.Fprintln(stderr, "usage: packetcode run [--provider NAME] [--model MODEL] [--permission-mode MODE] [--resume ID] [--json] <prompt...>")
		return runExitUsage
	}
	if *permissionFlag != "" {
		if _, err := permissions.ParseProfile(*permissionFlag); err != nil {
			fmt.Fprintf(stderr, "packetcode run: %v\n", err)
			return runExitUsage
		}
	}

	opts := runCommandOptions{
		Provider:       *providerFlag,
		Model:          *modelFlag,
		PermissionMode: *permissionFlag,
		ResumeID:       *resumeFlag,
		JSON:           *jsonFlag,
		Prompt:         strings.Join(fs.Args(), " "),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	started := time.Now()
	result, err := executeRunCommand(ctx, opts, stderr)
	result.SchemaVersion = 1
	result.ElapsedMS = time.Since(started).Milliseconds()
	result.OK = err == nil
	if err != nil {
		result.Error = err.Error()
		writeRunFailure(stderr, result, err)
	}

	if opts.JSON {
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			fmt.Fprintf(stderr, "packetcode run: write JSON result: %v\n", encodeErr)
			return runExitError
		}
	} else if err == nil {
		if _, writeErr := fmt.Fprintln(stdout, strings.TrimRight(terminaltext.Clean(result.Output), "\r\n")); writeErr != nil {
			fmt.Fprintf(stderr, "packetcode run: write response: %v\n", writeErr)
			return runExitError
		}
	}

	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		return runExitCanceled
	case errors.Is(err, errRunApprovalUnavailable):
		return runExitApprovalUnavailable
	default:
		return runExitError
	}
}

// Keep the machine-readable error intact, but never send provider-controlled
// escape sequences to the user's terminal. Recovery opens saved history;
// it does not silently repeat a prompt or any tool actions.
func writeRunFailure(w io.Writer, result runResult, err error) {
	fmt.Fprintf(w, "packetcode run: %s\n", terminaltext.Clean(err.Error()))
	if errors.Is(err, errRunApprovalUnavailable) {
		fmt.Fprintln(w, "packetcode run: open an interactive session to review and approve the requested action.")
	}
	if result.SessionID != "" {
		resumeID := "ID"
		// Newly created sessions use UUIDs. Keep legacy/custom IDs as quoted
		// data rather than interpolating shell syntax into a suggested command.
		if id, parseErr := uuid.Parse(result.SessionID); parseErr == nil && id.String() == result.SessionID {
			resumeID = result.SessionID
		}
		fmt.Fprintf(w, "packetcode run: session ID %q. Use packetcode --resume %s from this directory to review saved history before continuing.\n", result.SessionID, resumeID)
		fmt.Fprintln(w, "packetcode run: completed tool actions may still be present; the failed run did not roll them back.")
	}
}

func printRunUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  packetcode run [--provider NAME] [--model MODEL] [--permission-mode MODE] [--resume ID] [--json] <prompt...>

Run one agent turn without the interactive interface. If the permission policy
requires a user decision, the action is rejected and the command exits 3.

Flags:
  --provider NAME          override the configured provider
  --model MODEL            override the configured model
  --permission-mode MODE   ask, accept-edits, auto, read-only, or bypass
  --resume ID              resume a saved session by ID
  --json                   write one machine-readable JSON result
`)
}

// nonInteractiveApprover records every attempted prompt before rejecting it.
// The command checks Asked after the model finishes because a model can accept
// the rejection and produce a normal-looking answer; automation must still be
// told that the requested run could not proceed as authorized.
type nonInteractiveApprover struct{ asked atomic.Bool }

func (a *nonInteractiveApprover) Approve(_ context.Context, _ agent.ApprovalRequest) agent.ApprovalDecision {
	a.asked.Store(true)
	return agent.ApprovalDecision{Reason: errRunApprovalUnavailable.Error()}
}

func (a *nonInteractiveApprover) Asked() bool { return a.asked.Load() }

func executeRunWithRuntime(ctx context.Context, opts runCommandOptions, stderr io.Writer) (result runResult, retErr error) {
	cfg, err := config.Load()
	if err != nil {
		return result, fmt.Errorf("load config: %w", err)
	}
	for _, problem := range cfg.DotEnvProblems() {
		fmt.Fprintf(stderr, "packetcode run: .env %s\n", problem)
	}
	for _, problem := range cfg.CompatProblems() {
		fmt.Fprintf(stderr, "packetcode run: config %s\n", problem)
	}
	for _, problem := range cfg.ValidationProblems() {
		fmt.Fprintf(stderr, "packetcode run: config %s\n", problem)
	}
	if opts.PermissionMode != "" {
		profile, parseErr := permissions.ParseProfile(opts.PermissionMode)
		if parseErr != nil {
			return result, parseErr
		}
		cfg.Permissions.Profile = permissions.ProfileConfigName(profile)
		cfg.Permissions.Default = ""
		cfg.Behavior.TrustMode = false
	}
	policy, err := permissions.FromConfig(cfg)
	if err != nil {
		return result, fmt.Errorf("permissions: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return result, fmt.Errorf("working directory: %w", err)
	}
	sessionsDir, err := config.SessionsDir()
	if err != nil {
		return result, fmt.Errorf("resolve sessions directory: %w", err)
	}
	backupsDir, err := config.BackupsDir()
	if err != nil {
		return result, fmt.Errorf("resolve backups directory: %w", err)
	}
	runtime, err := buildPacketRuntime(ctx, packetRuntimeConfig{
		Config:            cfg,
		Root:              git.RepoRoot(cwd),
		ProviderOverride:  opts.Provider,
		ModelOverride:     opts.Model,
		ResumeID:          opts.ResumeID,
		PermissionPolicy:  policy,
		SessionsDir:       sessionsDir,
		BackupsDir:        backupsDir,
		MCPServers:        mcpServerConfigsFrom(cfg),
		MCPServersSet:     true,
		MCPClientName:     "packetcode-run",
		EnableCostTracker: true,
		SystemPrompt:      systemPrompt,
		Diagnostics:       stderr,
		DiagnosticPrefix:  "packetcode run: ",
	})
	if err != nil {
		return result, err
	}
	defer func() {
		// Reported, not promoted: by the time Close runs the turn is done
		// and the session is saved. An MCP server that ignores stdin EOF and
		// has to be killed used to turn that into exit 1 with the answer
		// withheld from stdout.
		if closeErr := runtime.Close(); closeErr != nil {
			fmt.Fprintf(stderr, "packetcode run: close runtime: %s\n", terminaltext.Clean(closeErr.Error()))
		}
	}()
	writeRunMCPReports(stderr, runtime.MCPReports)

	result.SessionID = runtime.SessionID
	result.Provider = runtime.Provider
	result.Model = runtime.Model
	approver := &nonInteractiveApprover{}
	result.Output, result.Usage, err = collectRunEvents(ctx, runtime.NewAgent(approver).Run(ctx, opts.Prompt))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if approver.Asked() {
		return result, errRunApprovalUnavailable
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func writeRunMCPReports(w io.Writer, reports []mcp.StartupReport) {
	for _, report := range reports {
		switch report.Status {
		case "running":
			fmt.Fprintf(w, "packetcode run: mcp %s: %d tools, pid %d\n", terminaltext.Clean(report.Name), report.ToolCount, report.PID)
		default:
			fmt.Fprintf(w, "packetcode run: mcp %s: %s — %s\n", terminaltext.Clean(report.Name), terminaltext.Clean(report.Status), terminaltext.Clean(report.Err))
		}
	}
}

func collectRunEvents(ctx context.Context, events <-chan agent.AgentEvent) (string, runUsage, error) {
	var output strings.Builder
	var usage runUsage
	for {
		select {
		case <-ctx.Done():
			return output.String(), usage, ctx.Err()
		case event, ok := <-events:
			if !ok {
				return output.String(), usage, errors.New("agent event stream ended without a terminal event")
			}
			switch event.Type {
			case agent.EventTextDelta:
				output.WriteString(event.Text)
			case agent.EventToolCallProposed:
				// Text on a tool-calling provider turn is intermediate. The next
				// assistant text is the final candidate for stdout.
				output.Reset()
			case agent.EventUsageUpdate:
				usage.InputTokens += event.Usage.InputTokens
				usage.OutputTokens += event.Usage.OutputTokens
				usage.CacheCreationInputTokens += event.Usage.CacheCreationInputTokens
				usage.CacheReadInputTokens += event.Usage.CacheReadInputTokens
			case agent.EventDone:
				return output.String(), usage, nil
			case agent.EventError:
				if event.Error == nil {
					return output.String(), usage, errors.New("agent failed without an error")
				}
				return output.String(), usage, event.Error
			}
		}
	}
}
