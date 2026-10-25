package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/acp"
	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/app"
	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/mcp"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
)

type packetACPFactory struct {
	cfg      *config.Config
	provider string
	model    string
	policy   *permissions.Policy
	// ceiling is the most permissive profile a client-requested permissionMode
	// may select; the operator's startup configuration sets it.
	ceiling     permissions.Profile
	sessionsDir string
	backupsDir  string
	log         io.Writer
}

// profileRank orders the built-in profiles from least to most permissive for
// the escalation ceiling on client-requested permission modes.
var profileRank = map[permissions.Profile]int{
	permissions.ProfileSafe: 0,
	permissions.ProfileAsk:  1,
	permissions.ProfileEdit: 2,
	permissions.ProfileAuto: 3,
	permissions.ProfileFull: 4,
}

// serverPermissionCeiling derives the escalation ceiling from the effective
// startup configuration (after any --permission-mode flag was applied — the
// flag writes cfg.Permissions.Profile).
//
// The ceiling is the profile the server-wide policy actually runs under, so a
// client can narrow its session but never widen it past what the operator
// started with. Two cases used to answer "unrestricted" instead:
//
//   - An empty profile. permissions.New treats "" as "balanced" (ask), so the
//     policy was ask while the ceiling said full -- and config.Default() sets
//     "balanced" explicitly, so the only way to reach "" was to write
//     profile = "" by hand.
//   - A custom profile. Its base is ask with the operator's rules layered on
//     (permissions.configProfile), so ask is the exact ceiling, not a guess.
//
// Both now resolve through ParseProfile the way the policy itself does, and
// fall back to ask. Trust mode remains the one explicit way to raise the
// ceiling to full, because it already raises the policy to full.
func serverPermissionCeiling(cfg *config.Config) permissions.Profile {
	ceiling := permissions.ProfileAsk
	if profile, err := permissions.ParseProfile(cfg.Permissions.Profile); err == nil {
		ceiling = profile
	}
	if cfg.Behavior.TrustMode {
		ceiling = permissions.ProfileFull
	}
	return ceiling
}

// defaultPermissionMode names, in the ACP wire vocabulary, the profile a
// session runs under when session/new carries no override. Advertised at
// initialize so a client can label its permission control with what the
// engine will actually do instead of assuming "ask".
func defaultPermissionMode(cfg *config.Config) string {
	profile := permissions.ProfileAsk
	if cfg.Permissions.Profile != "" {
		if parsed, err := permissions.ParseProfile(cfg.Permissions.Profile); err == nil {
			profile = parsed
		}
	}
	if cfg.Behavior.TrustMode {
		profile = permissions.ProfileFull
	}
	// Map back to the wire spelling; a custom profile has no wire name.
	for _, mode := range acp.PermissionModes {
		if parsed, err := permissions.ParseProfile(mode); err == nil && parsed == profile {
			return mode
		}
	}
	return ""
}

// allowedPermissionModes filters the wire vocabulary to modes at or below the
// ceiling, so clients are never offered an escalation the factory rejects.
func allowedPermissionModes(ceiling permissions.Profile) []string {
	out := make([]string, 0, len(acp.PermissionModes))
	for _, mode := range acp.PermissionModes {
		profile, err := permissions.ParseProfile(mode)
		if err != nil {
			continue
		}
		if profileRank[profile] <= profileRank[ceiling] {
			out = append(out, mode)
		}
	}
	return out
}

func runACPCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerFlag := fs.String("provider", "", "override the configured provider")
	modelFlag := fs.String("model", "", "override the configured model")
	permissionFlag := fs.String("permission-mode", "", "override permission profile (ask, accept-edits, auto, read-only, bypass)")
	if err := parseCommandFlags(fs, args, stdout, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: packetcode acp [--provider NAME] [--model MODEL] [--permission-mode MODE]")
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "packetcode acp: load config: %v\n", err)
		return 1
	}
	if !cfg.ACP.IsEnabled() {
		fmt.Fprintln(stderr, "packetcode acp: ACP integration is disabled; enable [acp].enabled or set PACKETCODE_ACP_ENABLED=true")
		return 1
	}
	for _, problem := range cfg.DotEnvProblems() {
		fmt.Fprintf(stderr, "packetcode acp: .env %s\n", problem)
	}
	for _, problem := range cfg.CompatProblems() {
		fmt.Fprintf(stderr, "packetcode acp: config %s\n", problem)
	}
	for _, problem := range cfg.ValidationProblems() {
		fmt.Fprintf(stderr, "packetcode acp: config %s\n", problem)
	}
	if *permissionFlag != "" {
		profile, err := permissions.ParseProfile(*permissionFlag)
		if err != nil {
			fmt.Fprintf(stderr, "packetcode acp: %v\n", err)
			return 2
		}
		cfg.Permissions.Profile = permissions.ProfileConfigName(profile)
		cfg.Permissions.Default = ""
		cfg.Behavior.TrustMode = false
	}
	policy, err := permissions.FromConfig(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "packetcode acp: permissions: %v\n", err)
		return 1
	}
	activeProvider := cfg.Default.Provider
	activeModel := cfg.Default.Model
	if *providerFlag != "" {
		activeProvider = *providerFlag
	}
	if *modelFlag != "" {
		activeModel = *modelFlag
	}
	if activeModel == "" && activeProvider != "" {
		activeModel = cfg.Providers[activeProvider].DefaultModel
	}
	sessionsDir, err := config.SessionsDir()
	if err != nil {
		fmt.Fprintf(stderr, "packetcode acp: resolve sessions directory: %v\n", err)
		return 1
	}
	backupsDir, err := config.BackupsDir()
	if err != nil {
		fmt.Fprintf(stderr, "packetcode acp: resolve backups directory: %v\n", err)
		return 1
	}

	retryAttempts := cfg.Behavior.ProviderMaxRetries
	if retryAttempts <= 0 {
		retryAttempts = 3
	}
	provider.SetConfiguredRetry(provider.RetryConfigForAttempts(retryAttempts))
	stall := cfg.Behavior.ProviderStallTimeout
	if stall <= 0 {
		stall = 60
	}
	provider.SetConfiguredStallTimeout(time.Duration(stall) * time.Second)

	ceiling := serverPermissionCeiling(cfg)
	factory := &packetACPFactory{
		cfg: cfg, provider: activeProvider, model: activeModel, policy: policy,
		ceiling:     ceiling,
		sessionsDir: sessionsDir, backupsDir: backupsDir, log: stderr,
	}
	server := acp.NewServer(stdin, stdout, stderr, factory, welcomeVersion())
	server.SetSessionLister(&packetSessionLister{dir: sessionsDir})
	server.SetSessionRenamer(&packetSessionRenamer{dir: sessionsDir})
	server.SetUsageReader(&packetUsageReader{dir: sessionsDir})
	server.SetModelCatalog(&packetModelCatalog{cfg: cfg, activeProvider: activeProvider, activeModel: activeModel})
	server.SetMCPLister(&packetMCPLister{cfg: cfg})
	server.SetCommandCatalog(&packetCommandCatalog{})
	server.SetProjectFileIndex(&packetProjectFileIndex{})
	server.SetPermissionModes(allowedPermissionModes(ceiling))
	server.SetDefaultPermissionMode(defaultPermissionMode(cfg))
	if err := server.Serve(context.Background()); err != nil {
		fmt.Fprintf(stderr, "packetcode acp: %v\n", err)
		return 1
	}
	return 0
}

// packetModelCatalog serves the configured provider/model choices to ACP
// clients via the _packetcode/models/list extension. The active provider and
// model (config defaults plus any CLI override) are flagged Default so
// clients can preselect them.
type packetModelCatalog struct {
	cfg            *config.Config
	activeProvider string
	activeModel    string
}

func (c *packetModelCatalog) ListModels() ([]acp.ModelOption, error) {
	out := make([]acp.ModelOption, 0, len(c.cfg.Providers)+1)
	seen := make(map[string]struct{})
	add := func(providerSlug, model string) {
		if providerSlug == "" || model == "" {
			return
		}
		key := providerSlug + "\x00" + model
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, acp.ModelOption{
			Provider: providerSlug,
			Model:    model,
			Default:  providerSlug == c.activeProvider && model == c.activeModel,
		})
	}
	// The active pair leads the list even when it is absent from config
	// (e.g. a --model CLI override).
	add(c.activeProvider, c.activeModel)
	factories := providerFactoriesFromConfig(c.cfg)
	slugs := make([]string, 0, len(c.cfg.Providers))
	for slug := range c.cfg.Providers {
		if _, ok := factories[slug]; ok {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		pc := c.cfg.Providers[slug]
		add(slug, pc.DefaultModel)
		for _, m := range pc.Models {
			add(slug, m.ID)
		}
	}
	return out, nil
}

// packetSessionLister serves persisted session history to ACP clients via the
// _packetcode/sessions/list extension.
type packetSessionLister struct {
	dir string
}

func (l *packetSessionLister) ListSessions() ([]acp.SessionSummary, error) {
	summaries, err := session.NewManager(l.dir).List()
	if err != nil {
		return nil, err
	}
	out := make([]acp.SessionSummary, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, acp.SessionSummary{
			SessionID:    s.ID,
			Name:         s.Name,
			UpdatedAt:    s.UpdatedAt,
			Provider:     s.Provider,
			Model:        s.Model,
			WorkingDir:   s.WorkingDir,
			MessageCount: s.MessageCount,
			CostUSD:      s.Cost.TotalUSD,
		})
	}
	return out, nil
}

// sessionPolicy resolves the permission policy one session runs under. An
// empty mode keeps the server-wide policy; otherwise the mode maps to a
// profile via permissions.ParseProfile and a fresh policy is built from the
// config with that profile, mirroring the --permission-mode CLI override (the
// inline default rule and trust mode are cleared so the chosen mode wins).
func (f *packetACPFactory) sessionPolicy(mode string) (*permissions.Policy, error) {
	if mode == "" {
		return f.policy, nil
	}
	profile, err := permissions.ParseProfile(mode)
	if err != nil {
		return nil, fmt.Errorf("%w %q", acp.ErrUnknownPermissionMode, mode)
	}
	// Escalation ceiling: a client may narrow its session's permissions but
	// never exceed what the operator configured the server with.
	if f.ceiling != "" && profileRank[profile] > profileRank[f.ceiling] {
		return nil, fmt.Errorf(
			"%w: %q exceeds the server's configured permission profile %q",
			acp.ErrPermissionModeDenied, mode, f.ceiling,
		)
	}
	var cfg config.Config
	if f.cfg != nil {
		cfg = *f.cfg
	}
	cfg.Permissions.Default = ""
	cfg.Behavior.TrustMode = false
	return permissions.FromConfigWithProfile(&cfg, profile)
}

// packetSessionRenamer persists display-name changes for ACP clients via the
// _packetcode/sessions/rename extension. Each call builds its own Manager, so
// Load only sets that throwaway Manager's current session and Rename+Save act
// on it alone — a live runtime's Manager (created per NewSession call) is
// never touched. Caveat: a runtime keeps its whole Session in memory and
// Save persists all of it, so renaming a session that has an active runtime
// is reverted by that runtime's next Save (e.g. the next turn's AddMessage).
// The name is normalized by session.Manager.Rename (sanitizeName: lowercase,
// spaces to hyphens, a-z0-9-_ only, 40 chars max).
type packetSessionRenamer struct {
	dir string
}

func (r *packetSessionRenamer) RenameSession(id, name string) error {
	manager := session.NewManager(r.dir)
	if _, err := manager.Load(id); err != nil {
		return err
	}
	return manager.Rename(name)
}

// packetUsageReader serves per-session token/cost usage to ACP clients via
// the _packetcode/sessions/usage extension and prompt-result enrichment. It
// re-reads the persisted session file on every call: the agent saves usage
// after each stream completion, so the file is authoritative, and a full
// parse per turn is acceptable.
type packetUsageReader struct {
	dir string
}

func (r *packetUsageReader) ReadUsage(sessionID string) (acp.SessionUsage, error) {
	loaded, err := session.NewManager(r.dir).Load(sessionID)
	if err != nil {
		return acp.SessionUsage{}, err
	}
	return acp.SessionUsage{
		ContextTokens: loaded.TokenUsage.ContextTokens,
		TotalInput:    loaded.TokenUsage.TotalInput,
		TotalOutput:   loaded.TokenUsage.TotalOutput,
		CostUSD:       loaded.Cost.TotalUSD,
	}, nil
}

// packetMCPLister serves the operator's configured MCP servers to ACP clients
// via the _packetcode/mcp/list extension. It reports configuration, not live
// process state — a session's actual fleet is reported from its Runtime when
// the client passes a sessionId.
type packetMCPLister struct {
	cfg *config.Config
}

func (l *packetMCPLister) ListMCPServers() ([]acp.MCPServerStatus, error) {
	servers := mcpServerConfigsFrom(l.cfg)
	out := make([]acp.MCPServerStatus, 0, len(servers))
	for _, server := range servers {
		status := "configured"
		if !server.Enabled {
			status = "disabled"
		}
		out = append(out, acp.MCPServerStatus{
			Name: server.Name, Status: status, Source: "agent", Command: server.Command,
		})
	}
	return out, nil
}

// packetCommandCatalog serves invocable slash commands to ACP clients via the
// _packetcode/commands/list extension.
//
// It deliberately reports ONLY markdown commands discovered under
// ~/.packetcode/commands and <cwd>/.packetcode/commands, never a built-in.
// Every entry in app.SlashCommands is a TUI affordance: /model and /provider
// open modal pickers, /clear and /transcript manipulate panes, /queue drives
// the TUI's input queue, /exit quits the program. None of them exist on the
// ACP surface, which speaks only initialize, session/*, and _packetcode/* —
// there is no channel over which the server could honour them, and several
// already have first-class ACP equivalents (_packetcode/models/list,
// _packetcode/sessions/{list,rename,usage}, the session/new permissionMode
// override) that a client should use instead of a text command. Advertising
// them would hand clients a menu of commands that silently do nothing.
//
// Markdown commands are different: they are pure prompt templates, so the ACP
// server can honour one by expanding it in session/prompt (see
// acp.Server.expandSlashCommand), which is why CommandInfo.Body is carried.
//
// The wire vocabulary keeps "builtin" as a legal source so a later server that
// grows a genuinely ACP-honourable built-in can add it without a schema break.
//
// Discovery is re-run per call rather than cached, so a command file added
// mid-session shows up in the client's next menu.
type packetCommandCatalog struct{}

func (c *packetCommandCatalog) ListCommands(cwd string) ([]acp.CommandInfo, error) {
	// nil skills, deliberately. A user-invocable skill is a prompt expansion
	// an ACP client could honour, but CommandInfo.Source is a closed
	// vocabulary ("builtin"/"user"/"project") that clients group menus by, and
	// a builtin skill would arrive as a "builtin" entry -- the one thing this
	// catalogue promises never to emit. Offering skills here needs that
	// vocabulary extended on the wire first, which is a protocol change and
	// not a side effect of the TUI gaining /<skill-name>.
	registry := app.LoadSlashRegistry(cwd, nil)
	commands := registry.Commands()
	out := make([]acp.CommandInfo, 0, len(commands))
	for _, cmd := range commands {
		if cmd.Builtin {
			continue
		}
		// Every custom command accepts arguments now: $ARGUMENTS places them
		// if the body has the placeholder, and they are appended if it does
		// not. Reporting a hint only for placeholder bodies told clients that
		// most commands take none, which was true before Expand stopped
		// dropping them and is not true now.
		hint := "[arguments]"
		out = append(out, acp.CommandInfo{
			Name:         cmd.Name,
			Description:  cmd.Description,
			Source:       cmd.Source,
			ArgumentHint: hint,
			Body:         cmd.Body,
		})
	}
	return out, nil
}

// mcpServersForSession resolves which MCP servers one ACP session runs with,
// and reports whether the client chose them.
//
// The ACP contract is that the client owns the session's MCP fleet, so a
// client-supplied list — even an explicitly empty one — is used verbatim. But
// "the client said nothing" is not the same as "the client said none": ACP
// clients that do not manage MCP themselves (the PacketCode desktop app) omit
// the field, and answering that with an empty fleet is what made a user's
// configured [mcp.<name>] tools silently absent from desktop sessions while the
// TUI had them. An omitted field therefore inherits the agent's own config.
func mcpServersForSession(sc acp.SessionConfig, cfg *config.Config) (servers []mcp.ServerConfig, clientSupplied bool) {
	if !sc.MCPServersSet {
		return mcpServerConfigsFrom(cfg), false
	}
	out := make([]mcp.ServerConfig, 0, len(sc.MCPServers))
	for _, server := range sc.MCPServers {
		out = append(out, mcp.ServerConfig{
			Name: server.Name, Command: server.Command, Args: server.Args,
			Env: server.Env, Enabled: true,
		})
	}
	return out, true
}

// packetProjectFileIndex answers @-mention file searches via the
// _packetcode/project/files extension. It reuses app.ListProjectFiles, so the
// candidate set and ignore rules are identical to the TUI's @ menu: git's own
// view of the tree (tracked + untracked, .gitignore applied) when cwd is a
// repo, else a bounded walk that skips VCS internals, vendored/build trees,
// dotdirs, and obvious binaries.
//
// This is a client-facing convenience, not a tool: the agent's own
// search_codebase/list_directory tools are reachable only from inside a turn,
// so a client building a mention menu has nothing else to call.
type packetProjectFileIndex struct{}

func (i *packetProjectFileIndex) SearchFiles(cwd, query string, limit int) ([]string, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cwd is not a directory: %s", cwd)
	}
	return rankProjectFiles(app.ListProjectFiles(root), query, limit), nil
}

// rankProjectFiles filters and orders candidates the way the TUI's mention
// autocomplete does (internal/ui/components/autocomplete.rebuild): tier 1 is a
// prefix match on the full relative path or on the basename, tier 2 is a
// substring match anywhere in the path; each tier is sorted lexically so the
// order is stable across calls. An empty query returns the head of the list.
func rankProjectFiles(paths []string, query string, limit int) []string {
	if limit <= 0 {
		return []string{}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		if len(paths) > limit {
			paths = paths[:limit]
		}
		return append([]string{}, paths...)
	}
	var prefix, substring []string
	for _, p := range paths {
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, needle) || strings.HasPrefix(path.Base(lower), needle) {
			prefix = append(prefix, p)
			continue
		}
		if strings.Contains(lower, needle) {
			substring = append(substring, p)
		}
	}
	sort.Strings(prefix)
	sort.Strings(substring)
	out := append(prefix, substring...)
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		return []string{}
	}
	return out
}

func (f *packetACPFactory) NewSession(ctx context.Context, cfg acp.SessionConfig, approver agent.Approver) (*acp.Runtime, error) {
	// Resolve the per-session policy first so an invalid permissionMode fails
	// before any session state is created.
	policy, err := f.sessionPolicy(cfg.PermissionMode)
	if err != nil {
		return nil, err
	}
	if cfg.Provider != "" {
		factories := providerFactoriesFromConfig(f.cfg)
		if _, ok := factories[cfg.Provider]; !ok {
			return nil, fmt.Errorf("%w %q", acp.ErrUnknownProvider, cfg.Provider)
		}
	}
	root := filepath.Clean(cfg.CWD)
	servers, clientSupplied := mcpServersForSession(cfg, f.cfg)
	rt, err := buildPacketRuntime(ctx, packetRuntimeConfig{
		Config:           f.cfg,
		Root:             root,
		DefaultProvider:  f.provider,
		DefaultModel:     f.model,
		ProviderOverride: cfg.Provider,
		ModelOverride:    cfg.Model,
		ResumeID:         cfg.SessionID,
		PermissionPolicy: policy,
		SessionsDir:      f.sessionsDir,
		BackupsDir:       f.backupsDir,
		MCPServers:       servers,
		MCPServersSet:    true,
		MCPClientName:    "packetcode-acp",
		FailOnMCPError:   clientSupplied,
		SystemPrompt:     systemPrompt,
		Diagnostics:      f.log,
		DiagnosticPrefix: "packetcode acp: ",
	})
	if err != nil {
		return nil, err
	}
	source := "agent"
	if clientSupplied {
		source = "client"
	}
	mcpStatuses := make([]acp.MCPServerStatus, 0, len(rt.MCPReports))
	for _, report := range rt.MCPReports {
		switch report.Status {
		case "running":
			fmt.Fprintf(f.log, "packetcode acp: mcp %s: %d tools, pid %d\n", report.Name, report.ToolCount, report.PID)
		default:
			fmt.Fprintf(f.log, "packetcode acp: mcp %s: %s — %s\n", report.Name, report.Status, report.Err)
		}
		mcpStatuses = append(mcpStatuses, acp.MCPServerStatus{
			Name: report.Name, Status: report.Status, ToolCount: report.ToolCount,
			Source: source, Command: report.Command, Error: report.Err,
		})
	}
	result := &acp.Runtime{
		ID: rt.SessionID, Runner: rt.NewAgent(approver), MCPServers: mcpStatuses,
		Close: rt.Close,
	}
	if rt.Resumed {
		result.History = rt.CurrentSession().Messages
	}
	return result, nil
}
