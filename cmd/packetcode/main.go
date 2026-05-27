// Command packetcode is a keyboard-first, multi-provider AI coding agent
// for the terminal.
//
// Usage:
//
//	packetcode                              start the TUI in the cwd
//	packetcode --version                    print version and exit
//	packetcode --provider gemini --model gemini-2.5-pro
//	packetcode --resume <session-id>        resume a saved session
//	packetcode --trust                      auto-approve all tool actions
//	packetcode --permission-mode ask        override approval profile
//	packetcode doctor                       diagnose local setup
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/app"
	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/git"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/mcp"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/provider/anthropic"
	"github.com/packetcode/packetcode/internal/provider/gemini"
	"github.com/packetcode/packetcode/internal/provider/minimax"
	"github.com/packetcode/packetcode/internal/provider/ollama"
	"github.com/packetcode/packetcode/internal/provider/openai"
	"github.com/packetcode/packetcode/internal/provider/openrouter"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// version/commit are populated at build time via -ldflags. Defaults are
// used during `go run` and local development.
var (
	version = "dev"
	commit  = "none"
)

const systemPrompt = `You are packetcode, a keyboard-first AI coding agent running in the user's terminal. You have direct access to the user's project via tools (read_file, write_file, patch_file, execute_command, search_codebase, list_directory). File modifications, command executions, background-agent spawns, and MCP tool calls are governed by the user's current permission policy.

Be concise. Prefer small, surgical edits. When the user asks you to do something, propose a plan, gather context with read tools as needed, then make the changes one tool call at a time. After tool execution, briefly summarize what changed.`

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	providerFlag := flag.String("provider", "", "override default provider for this session")
	modelFlag := flag.String("model", "", "override default model for this session")
	resumeFlag := flag.String("resume", "", "resume a saved session by ID")
	trustFlag := flag.Bool("trust", false, "auto-approve all tool actions for this session")
	permissionFlag := flag.String("permission-mode", "", "override permission profile for this session (ask, accept-edits, read-only, bypass)")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("packetcode %s (%s)\n", version, commit)
		return
	}
	if code, ok := dispatchSubcommand(flag.Args(), os.Stdout, os.Stderr); ok {
		os.Exit(code)
	}

	if err := run(*providerFlag, *modelFlag, *resumeFlag, *trustFlag, *permissionFlag); err != nil {
		fmt.Fprintf(os.Stderr, "packetcode: %s\n", err)
		os.Exit(1)
	}
}

func dispatchSubcommand(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "doctor":
		return runDoctorCommand(args[1:], stdout, stderr), true
	default:
		return 0, false
	}
}

func run(providerOverride, modelOverride, resumeID string, trust bool, permissionMode string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if permissionMode != "" {
		profile, err := permissions.ParseProfile(permissionMode)
		if err != nil {
			return err
		}
		cfg.Permissions.Profile = permissions.ProfileConfigName(profile)
		cfg.Permissions.Default = ""
	}

	// Optional user theme. A missing file is silent; a parse error
	// logs one stderr line and falls through to the built-in Terminal
	// Noir defaults — a bad theme never prevents packetcode from
	// starting.
	themePath, err := config.ThemePath()
	if err == nil {
		if t, err := theme.Load(themePath); err != nil {
			fmt.Fprintf(os.Stderr, "packetcode: failed to load theme: %v; falling back to defaults\n", err)
		} else {
			theme.Apply(t)
		}
	}

	factories := app.FactoryMap{
		"openai":     func(key string) provider.Provider { return openai.New(key) },
		"anthropic":  func(key string) provider.Provider { return anthropic.New(key) },
		"gemini":     func(key string) provider.Provider { return gemini.New(key) },
		"minimax":    func(key string) provider.Provider { return minimax.New(key) },
		"openrouter": func(key string) provider.Provider { return openrouter.New(key) },
		"ollama":     func(_ string) provider.Provider { return ollama.New(ollamaHost(cfg)) },
	}

	activeSlug := cfg.Default.Provider
	activeModel := cfg.Default.Model
	if providerOverride != "" {
		activeSlug = providerOverride
	}
	if modelOverride != "" {
		activeModel = modelOverride
	}

	// First-run: no saved default provider yet → walk through setup.
	// An explicit --provider is a session override, so respect it before
	// deciding whether onboarding is needed. If that override is not
	// configured, startup reports the normal "active provider is not
	// configured" error below instead of forcing unrelated setup.
	if shouldRunSetup(cfg, providerOverride) {
		_, err := app.RunSetup(os.Stdin, os.Stdout, cfg, factories)
		if err != nil {
			return err
		}
		// Reload the now-saved config so in-memory state matches disk.
		cfg, err = config.Load()
		if err != nil {
			return err
		}
		activeSlug = cfg.Default.Provider
		activeModel = cfg.Default.Model
		if providerOverride != "" {
			activeSlug = providerOverride
		}
		if modelOverride != "" {
			activeModel = modelOverride
		}
	}

	if trust {
		cfg.Behavior.TrustMode = true
	}
	if permissionMode != "" {
		profile, err := permissions.ParseProfile(permissionMode)
		if err != nil {
			return err
		}
		cfg.Permissions.Profile = permissions.ProfileConfigName(profile)
		cfg.Permissions.Default = ""
	}
	permissionPolicy, err := permissions.FromConfig(cfg)
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}

	// Build the provider registry. Only register providers the user has
	// actually configured — listing every provider would clutter the
	// switcher with non-functional options.
	reg := provider.NewRegistry()
	for slug, factory := range factories {
		key := cfg.GetProviderKey(slug)
		if slug != "ollama" && key == "" {
			continue
		}
		reg.Register(factory(key))
	}
	// Resolve the working directory to the git repo root if we're in one.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := git.RepoRoot(cwd)

	// Tool registry. write_file and patch_file get a backup manager
	// scoped to the active session — wired below once we know the ID.
	toolReg := tools.NewRegistry()
	toolReg.Register(tools.NewReadFileTool(root))
	toolReg.Register(tools.NewSearchCodebaseTool(root))
	toolReg.Register(tools.NewListDirectoryTool(root))
	toolReg.Register(tools.NewExecuteCommandTool(root))

	// Sessions.
	sessionsDir, err := config.SessionsDir()
	if err != nil {
		return err
	}
	sessions := session.NewManager(sessionsDir)
	if resumeID != "" {
		loaded, err := sessions.Load(resumeID)
		if err != nil {
			return fmt.Errorf("resume %s: %w", resumeID, err)
		}
		if providerOverride == "" && loaded.Provider != "" {
			activeSlug = loaded.Provider
		}
		if modelOverride == "" && loaded.Model != "" {
			activeModel = loaded.Model
		}
	}
	if _, ok := reg.Get(activeSlug); !ok {
		return fmt.Errorf("active provider %q is not configured; run packetcode without --provider to set one up", activeSlug)
	}
	if activeModel == "" {
		// Fall back to the provider's configured default model.
		activeModel = cfg.Providers[activeSlug].DefaultModel
	}
	if err := reg.SetActive(activeSlug, activeModel); err != nil {
		return err
	}
	if resumeID == "" {
		if _, err := sessions.New(activeSlug, activeModel); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}

	// Backup manager keyed by session ID.
	backupsDir, err := config.BackupsDir()
	if err != nil {
		return err
	}
	bk := session.NewBackupManager(backupsDir, sessions.Current().ID)
	toolReg.Register(tools.NewWriteFileTool(root, bk))
	toolReg.Register(tools.NewPatchFileTool(root, bk))

	hookRunner := hooks.New(cfg.Hooks, root)

	// Cost tracker — pricing closure delegates to whichever provider is
	// active *now* (post hot-switch), not the one when a token was
	// recorded.
	tallyPath, err := config.CostTallyPath()
	if err != nil {
		return err
	}
	tracker, err := cost.NewTracker(tallyPath, func(slug, modelID string) (float64, float64) {
		if p, ok := reg.Get(slug); ok {
			return p.Pricing(modelID)
		}
		return 0, 0
	})
	if err != nil {
		return err
	}

	// MCP servers — spawn external tool processes declared in
	// ~/.packetcode/config.toml. Failures are logged but never block
	// startup. We spawn here (after theme + tool registry bootstrap,
	// before jobs.NewManager) so tools are discovered in time to land
	// in toolReg before the first Agent turn.
	mcpLogDir, _ := config.HomeDir()
	mcpMgr := mcp.NewManager(mcp.Config{
		Servers:    mcpServerConfigsFrom(cfg),
		LogDir:     mcpLogDir,
		ClientInfo: mcp.ClientInfo{Name: "packetcode", Version: welcomeVersion()},
	})
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	mcpReports := mcpMgr.Start(startupCtx)
	cancelStartup()
	for _, r := range mcpReports {
		switch r.Status {
		case "running":
			fmt.Fprintf(os.Stderr, "packetcode: mcp %s: %d tools, pid %d\n", r.Name, r.ToolCount, r.PID)
		case "failed":
			fmt.Fprintf(os.Stderr, "packetcode: mcp %s: failed — %s\n", r.Name, r.Err)
		}
	}
	defer mcpMgr.Shutdown(2 * time.Second)

	// Background-agents manager. Constructed before app.New so Deps can
	// carry it in; the Approver and SpawnTool factory are wired post-
	// construction because they depend on pieces we won't have until
	// app.New returns and the Manager itself exists, respectively.
	jobsDir, err := config.JobsDir()
	if err != nil {
		return err
	}
	worktreesDir, err := config.WorktreesDir()
	if err != nil {
		return err
	}
	jobsMgr, recovered, err := jobs.NewManager(jobs.Config{
		Registry:     reg,
		Tools:        toolReg,
		MainSessions: sessions,
		SessionsDir:  sessionsDir,
		BackupsDir:   backupsDir,
		JobsDir:      jobsDir,
		WorktreesDir: worktreesDir,
		CostTracker:  tracker,
		PricingFor: func(slug, modelID string) (float64, float64) {
			if p, ok := reg.Get(slug); ok {
				return p.Pricing(modelID)
			}
			return 0, 0
		},
		SystemPromptFor: func(parentDepth int) string {
			return systemPrompt + "\n\nYou are a background sub-agent. Be concise and direct. Do not ask the user clarifying questions — make reasonable assumptions and act. Your final assistant message becomes your delivered result."
		},
		MaxConcurrent:    cfg.Behavior.BackgroundMaxConcurrent,
		MaxDepth:         cfg.Behavior.BackgroundMaxDepth,
		MaxTotal:         cfg.Behavior.BackgroundMaxTotal,
		DefaultProvider:  cfg.Behavior.BackgroundDefaultProvider,
		DefaultModel:     cfg.Behavior.BackgroundDefaultModel,
		PermissionPolicy: permissionPolicy,
		Root:             root,
		Hooks:            hookRunner,
		// Approver and SpawnTool are set below, once jobsMgr and the
		// App's uiApprover exist. Leaving them nil here is fine: the
		// manager guards both before use.
	})
	if err != nil {
		return err
	}
	if recovered > 0 {
		fmt.Fprintf(os.Stderr, "packetcode: recovered %d orphan job(s) from previous run\n", recovered)
	}
	// Install the SpawnTool factory: each per-job tool registry gets its
	// own spawn_agent tool bound to the spawning job's id/depth so the
	// Manager can enforce recursion limits and annotate child-of-child
	// approvals correctly.
	jobsMgr.SetSpawnToolFactory(func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool {
		return tools.NewBackgroundSpawnAgentTool(jobsMgr.AsToolsSpawner(), parentJobID, parentDepth, parentAllowWrite)
	})
	defer jobsMgr.Shutdown(5 * time.Second)

	// Register spawn_agent into the main tool registry so the foreground
	// LLM can call it too. ParentJobID="" / ParentDepth=0 for main-
	// session spawns.
	toolReg.Register(tools.NewSpawnAgentTool(jobsMgr.AsToolsSpawner(), "", 0))

	// Register MCP tools AFTER every native tool + spawn_agent so the
	// Agent's initial tool enumeration (on its first turn) sees them.
	for _, r := range mcp.RegisterTools(toolReg, mcpMgr.Clients()) {
		if r.Status == "skipped" {
			fmt.Fprintf(os.Stderr, "packetcode: mcp %s.%s skipped alias %s — %s\n", r.Server, r.Tool, r.Alias, r.Err)
		}
	}

	a, err := app.New(app.Deps{
		Config:           cfg,
		Registry:         reg,
		Tools:            toolReg,
		Sessions:         sessions,
		CostTracker:      tracker,
		Jobs:             jobsMgr,
		Backups:          bk,
		MCP:              mcpMgr,
		PermissionPolicy: permissionPolicy,
		WorkingDir:       root,
		SystemPrompt:     systemPrompt,
		Hooks:            hookRunner,
		Version:          welcomeVersion(),
		Factories:        factories,
	})
	if err != nil {
		return err
	}

	// The App owns the uiApprover; pipe it into the jobs.Manager so
	// destructive sub-agent tool calls (when AllowWrite is on) prompt
	// the main user through the existing modal.
	jobsMgr.SetApprover(a.Approver())

	prog := tea.NewProgram(a) // inline rendering — native terminal scrollback, no mouse support
	// Let the App post async messages (jobs.Manager Subscribe callbacks)
	// into the Bubble Tea Update loop.
	a.SetSendFunc(func(m tea.Msg) { prog.Send(m) })
	if _, err := prog.Run(); err != nil {
		return err
	}
	return nil
}

func shouldRunSetup(cfg *config.Config, providerOverride string) bool {
	if cfg == nil {
		return true
	}
	if providerOverride != "" {
		return false
	}
	if cfg.Default.Provider == "" {
		return true
	}
	if cfg.Default.Provider == "ollama" {
		return false
	}
	return cfg.GetProviderKey(cfg.Default.Provider) == ""
}

// welcomeVersion returns the label shown on the welcome splash. We
// prefer the linker-injected version; "dev" builds get a friendlier "v1"
// so the screen looks like a release rather than a debug artefact.
func welcomeVersion() string {
	if version == "" || version == "dev" {
		return "v1"
	}
	if version[0] == 'v' {
		return version
	}
	return "v" + version
}

// ollamaHost returns the configured Ollama base URL. Env wins over config
// so a machine-specific daemon address can stay out of committed files.
// If unset, the Ollama provider uses its generic localhost default.
func ollamaHost(cfg *config.Config) string {
	if host := os.Getenv("PACKETCODE_OLLAMA_HOST"); host != "" {
		return host
	}
	if pc, ok := cfg.Providers["ollama"]; ok && pc.Host != "" {
		return pc.Host
	}
	return ""
}

// avoid "imported and not used" if filepath is conditionally referenced.
var _ = filepath.Join
