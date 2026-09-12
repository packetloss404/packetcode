// Package app is the top-level Bubble Tea model. It orchestrates the
// composed UI components and translates their messages into agent and
// session actions.
//
// The flow is straightforward:
//  1. User types in the input bar → Enter → SubmitMsg.
//  2. App runs agent.Run(), which returns a channel of AgentEvent.
//  3. A goroutine forwards each AgentEvent to the Bubble Tea program
//     via Send(). Update() routes them to the conversation pane.
//  4. When the agent needs approval, the uiApprover bridge posts the
//     pending request, App raises the approval modal, the user hits y/n,
//     and the decision is sent back to the agent.
package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/git"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/mcp"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/skills"
	"github.com/packetcode/packetcode/internal/statusline"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/agentview"
	"github.com/packetcode/packetcode/internal/ui/components/approval"
	"github.com/packetcode/packetcode/internal/ui/components/autocomplete"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
	"github.com/packetcode/packetcode/internal/ui/components/input"
	jobs_ui "github.com/packetcode/packetcode/internal/ui/components/jobs"
	"github.com/packetcode/packetcode/internal/ui/components/picker"
	"github.com/packetcode/packetcode/internal/ui/components/prompt"
	"github.com/packetcode/packetcode/internal/ui/components/spinner"
	"github.com/packetcode/packetcode/internal/ui/components/topbar"
	"github.com/packetcode/packetcode/internal/ui/components/workflowview"
	"github.com/packetcode/packetcode/internal/ui/layout"
	"github.com/packetcode/packetcode/internal/workflow"
)

// agentEventMsg wraps a single agent.AgentEvent so we can route it
// through the Bubble Tea Update loop.
type agentEventMsg struct{ ev agent.AgentEvent }

// agentDoneMsg signals the agent's event channel has closed.
type agentDoneMsg struct{}

// approvalPendingMsg is pushed when an agent queues an approval request.
type approvalPendingMsg struct{}

// tickTopbarMsg updates the duration counter in the top bar.
type tickTopbarMsg struct{}

// gitBranchMsg returns an off-thread branch lookup. Git startup can take tens
// of milliseconds on Windows, so it must not run in Bubble Tea's key/event
// loop on every status tick.
type gitBranchMsg struct{ branch string }

// toolOutputFlushMsg fires on a throttle interval while a long-running
// command is streaming. On receipt the App drains the coalesced
// tool-output buffer into the conversation's live region, rebuilding the
// pending tool block at most once per interval rather than once per chunk.
type toolOutputFlushMsg struct{}

// toolOutputThrottle is the minimum interval between live-region rebuilds
// for streamed command output. High-output commands (test suites, builds)
// can emit chunks far faster than a human reads; coalescing to ~10 fps
// keeps the renderer idle without making progress feel laggy.
const toolOutputThrottle = 100 * time.Millisecond

const gitBranchRefreshInterval = 15 * time.Second

type statusLineMsg struct {
	seq    int
	line   string
	err    error
	manual bool
}

type queuedInput struct {
	// Text is what the model receives. Display is what the transcript shows
	// and what /queue lists; they differ for a skill expansion, where Text is
	// the framed body and Display is the command the user typed.
	Text    string
	Display string
	// Authored marks Text the user did not type, so it is not re-scanned for
	// @-mentions when the turn finally starts. See turnOptions.authored.
	Authored bool
	Attached []string
	// LoopID is the self-paced loop that owns this turn. See turnOptions.
	LoopID       string
	SourceLoopID string // identifies interval iterations too, for removing queued work
	SkillGrant   *skills.Skill
	At           time.Time
}

// Label is what a human should be shown for this entry. Never Text: for a
// skill expansion that is a framed 64KB document, and a queue listing that
// prints the first hundred characters of it tells the reader nothing about
// which of their queued prompts this is.
func (q queuedInput) Label() string {
	if strings.TrimSpace(q.Display) != "" {
		return q.Display
	}
	return q.Text
}

// jobUpdateMsg is dispatched from the jobs.Manager Subscribe callback
// (which runs in its own goroutine) into the Bubble Tea Update loop via
// tea.Program.Send. The App uses it to refresh the top bar and, on
// terminal transitions, append a system message describing the outcome.
type jobUpdateMsg struct{ Snap jobs.Snapshot }

// workflowUpdateMsg is dispatched from the workflow.Engine Subscribe callback
// (which runs on its own goroutine) into the Bubble Tea Update loop via
// tea.Program.Send. The App uses it to refresh the workflow view and, on
// terminal run transitions, append a system message describing the outcome.
type workflowUpdateMsg struct{ Run workflow.RunSnapshot }

type mcpRestartedMsg struct {
	Name     string
	Report   mcp.StartupReport
	Client   *mcp.Client
	Previous *mcp.Client
	Err      error
}

// Deps bundles everything App needs from main(). main() owns the lifecycle
// of these objects; App just borrows them.
type Deps struct {
	Config      *config.Config
	Registry    *provider.Registry
	Tools       *tools.Registry
	Sessions    *session.Manager
	CostTracker *cost.Tracker
	Jobs        *jobs.Manager
	Workflow    *workflow.Engine
	Backups     *session.BackupManager
	MCP         *mcp.Manager
	// Skills is the resolved registry, shared with the skill tool and the
	// system-prompt index so the three surfaces cannot disagree about what
	// exists. Borrowed, never re-Loaded here: a listing that re-scanned disk
	// would describe a set the running turn never saw.
	Skills            *skills.Registry
	PermissionPolicy  *permissions.Policy
	WorkingDir        string
	RemoteWorkspace   bool   // true when workspace I/O is provided by a non-local backend
	ComputerID        string // non-empty for a remote Packet Computer session
	WorkspaceIdentity string // immutable endpoint/root binding for remote session resume
	SystemPrompt      string
	Hooks             *hooks.Runner
	Version           string // shown on the welcome splash; e.g. "v1" or "v0.1.0"
	ResumeHydrate     bool   // render the current session transcript at startup
	// AgentFactory lets the host provide an agent built from the same runtime
	// definition used by non-TUI frontends. Optional for tests and embedders
	// that still provide the individual dependencies above.
	AgentFactory func(agent.Approver) *agent.Agent

	// Factories maps provider slug → constructor. Used at runtime when
	// the user sets or updates an API key through the provider picker,
	// so the registry can be re-seeded with a fresh Provider instance
	// carrying the new key. Optional — handlers guard on nil.
	Factories FactoryMap
}

type App struct {
	deps Deps

	// UI components.
	topbar        topbar.Model
	conversation  conversation.Model
	input         input.Model
	approval      approval.Model
	jobsPanel     jobs_ui.Model
	agentView     agentview.Model
	workflowView  workflowview.Model
	picker        picker.Model
	prompt        prompt.Model
	spinner       spinner.Model
	autocomplete  autocomplete.Model
	slashCommands *SlashCommandRegistry

	// Autocomplete entry sets. slashEntries is the canonical "/command"
	// list, stashed so file-mention mode can swap it back out when the
	// user returns to a slash buffer. fileIndex is the lazily-built
	// "@file" list (built once from WorkingDir on the first @-mention).
	// mentionStart/mentionEnd are the byte span of the @token currently under
	// the caret, so accepting completion can replace that token in place.
	slashEntries   []autocomplete.Entry
	fileIndex      []autocomplete.Entry
	fileIndexBuilt bool
	mentionStart   int
	mentionEnd     int
	// agentDispatchFocused separates Agent View list shortcuts from its task
	// composer. Without an explicit focus, printable actions such as p/c/i/x
	// are swallowed by the textarea and Enter closes the workspace instead of
	// opening the selected agent.
	agentDispatchFocused bool

	// Agent + bridge.
	agent    *agent.Agent
	approver *uiApprover
	// approvalID is the approver envelope the visible modal was raised for,
	// and approvalOrigin says whose work it blocks. Every decision routes
	// through the id: without it, a keypress resolves "whatever is showing",
	// which is a different request the moment a cancelled job's prompt is
	// replaced. 0 means no approval is bound.
	approvalID       uint64
	approvalOrigin   approvalOrigin
	permissionPolicy *permissions.Policy
	permissionBase   *permissions.Policy
	preTrustPolicy   *permissions.Policy

	// planMode holds the read-only research mode. When on, the policy is
	// forced to read_only and turns carry a "propose a plan" instruction;
	// planPrevProfile is the profile to restore when plan mode exits.
	planMode        bool
	planPrevProfile permissions.Profile

	// Prompt history for Up/Down recall in the input bar. promptHistory is
	// the list of submitted inputs (oldest first); historyIdx points at the
	// current recall position (== len means "not navigating, showing the
	// live draft"); historyDraft stashes the in-progress buffer while the
	// user pages back through history so Down can restore it. See history.go.
	promptHistory []string
	historyIdx    int
	historyDraft  string

	// Background-agents manager. Non-nil when deps.Jobs is set. All
	// job-related UI code paths guard on `a.jobs != nil`.
	jobs *jobs.Manager

	// Workflow engine + spec loader. Non-nil when deps.Workflow is set;
	// every /workflows code path guards on `a.workflow != nil`.
	workflow       *workflow.Engine
	workflowLoader *workflow.Loader

	// backups is the session's BackupManager. Non-nil when deps.Backups
	// is set. /undo guards on it.
	backups *session.BackupManager

	// mcp is the MCP manager. Non-nil when deps.MCP is set. /mcp slash
	// commands guard on it.
	mcp *mcp.Manager

	// contextMgr handles /compact token accounting and summary round-
	// trips. Constructed in New from cfg.Behavior.AutoCompactThreshold.
	contextMgr *agent.ContextManager
	statusLine *statusline.Runner

	// sendMsg is the tea.Program.Send bridge set by the host (main.go)
	// after tea.NewProgram so callbacks originating off the Bubble Tea
	// thread (notably the jobs.Manager Subscribe callback) can deliver
	// messages into Update. Nil-safe: if unset, async updates are
	// silently dropped (sync code paths still work).
	sendMsg func(tea.Msg)

	width     int
	height    int
	streaming bool

	// Coalesced live tool-output streaming. Chunks (EventToolOutputChunk)
	// land in toolOutputPending keyed by the running call id; a single
	// flush timer (toolOutputFlushScheduled) drains them into the
	// conversation's live region at most once per toolOutputThrottle, so a
	// flood of chunks rebuilds the pending block ~10x/sec instead of per
	// chunk. Single-writer from Update.
	toolOutputCallID         string
	toolOutputPending        strings.Builder
	toolOutputFlushScheduled bool

	// cancelTurn cancels the in-flight agent.Run context for the current
	// streaming turn. Set in startTurn, cleared in agentDoneMsg / on
	// EventError / on Ctrl+C. A non-nil cancelTurn plus streaming==true
	// means "turn is live"; cancelTurn==nil plus streaming==true means
	// "cancel requested, waiting for goroutine drain" — in that window a
	// second Ctrl+C is a no-op (not a quit). Single-writer from Update.
	cancelTurn          context.CancelFunc
	startedAt           time.Time
	operationLabel      string
	operationStarted    time.Time
	queuedInputs        []queuedInput
	queuePaused         bool // failures retain pending prompts until /queue resume
	skipAutoCompactOnce bool

	// /loop state. loops holds active loops by id; activeLoopID names the loop
	// that owns the currently-running self-paced turn (so agentDoneMsg can
	// decide whether to re-run the body).
	loops        map[string]*loopState
	loopSeq      int
	activeLoopID string
	// turnFailed records that the current turn ended in an error or a
	// cancellation. A self-paced loop reads it to stop rather than re-run:
	// Ctrl+C could not end a loop, and a 401 became 25 back-to-back retries.
	turnFailed bool
	// activeSkillGrant is the permission widening skills asked for, held only
	// for the turn that invoked it. See skillgrant.go.
	activeSkillGrant   *skillGrant
	skillTurnID        uint64
	lastAgentText      string // accumulated assistant text for the current turn (loop sentinel check)
	statusSeq          int
	statusLineInFlight int
	statusLineLastRun  time.Time
	lastStatusLineErr  error
	gitBranch          string
	gitBranchLastRun   time.Time
	gitBranchInFlight  bool
	jobSeqSeen         map[string]int64
	jobTerminalSeen    map[string]bool
	jobWorktreeSeen    map[string]bool

	// workflowTerminalSeen dedupes the one-shot "run finished" system
	// message per run id (the engine may fan out several terminal snapshots).
	workflowTerminalSeen map[string]bool

	providerKeyValidationSeq    uint64
	providerKeyValidationActive bool
	providerKeyValidationSlug   string
	providerKeyValidationKey    string
}

// isCancellation reports whether err is (or wraps) a context cancellation
// or deadline, so the App can render a friendlier "turn cancelled" line
// instead of the raw error text.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// New constructs the App and registers the active provider/model from
// config. Returns an error if no provider is configured (caller should
// run the setup flow first).
func New(deps Deps) (*App, error) {
	if deps.Registry == nil || deps.Tools == nil || deps.Sessions == nil {
		return nil, fmt.Errorf("app: missing required dependencies")
	}
	if deps.WorkingDir == "" {
		deps.WorkingDir = "."
	}

	policy := deps.PermissionPolicy
	if policy == nil {
		var err error
		policy, err = permissions.FromConfig(deps.Config)
		if err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
	}
	if policy == nil {
		policy = permissions.DefaultPolicy()
	}
	basePolicy := policy
	if deps.Config != nil && deps.Config.Behavior.TrustMode {
		cfgCopy := *deps.Config
		cfgCopy.Behavior.TrustMode = false
		if p, err := permissions.FromConfig(&cfgCopy); err == nil && p != nil {
			basePolicy = p
		}
	}
	approver := newUIApprover()
	approver.SetPermissionPolicy(policy)

	var a *agent.Agent
	if deps.AgentFactory != nil {
		a = deps.AgentFactory(approver)
	}
	if a == nil {
		a = agent.New(agent.Config{
			LoopDetection: agentLoopDetection(deps.Config),
			Registry:      deps.Registry,
			Tools:         deps.Tools,
			Session:       deps.Sessions,
			CostTracker:   deps.CostTracker,
			Approver:      approver,
			Policy:        policy,
			SystemPrompt:  deps.SystemPrompt,
			Hooks:         deps.Hooks,
			SugarCache:    sugarCacheAgentConfig(deps.Config),
			ConduitShadow: conduitShadowAgentConfig(deps.Config),
		})
	}

	conv := conversation.New()
	if deps.Version != "" {
		conv.SetVersion(deps.Version)
	} else {
		conv.SetVersion("v1")
	}

	// Context manager threshold comes from config; fall back to the
	// library default (80%) when no config is wired (tests).
	threshold := 0
	if deps.Config != nil {
		threshold = deps.Config.Behavior.AutoCompactThreshold
	}
	ctxMgr := agent.NewContextManager(threshold)

	var statusRunner *statusline.Runner
	if deps.Config != nil && !deps.RemoteWorkspace {
		statusRunner = statusline.New(deps.Config.StatusLine, deps.WorkingDir)
	}

	slashCommands := LoadSlashRegistry(deps.WorkingDir, deps.Skills)
	slashEntries := buildAutocompleteEntries(slashCommands.HelpRows())
	inputModel := input.New()
	if deps.Config != nil {
		inputModel.SetMaxRows(deps.Config.Behavior.MaxInputRows)
	}
	workflowLoader := workflow.NewLoader(deps.WorkingDir)
	if deps.RemoteWorkspace {
		// Remote project workflow files require asynchronous backend loading;
		// until that exists, expose only built-ins and local user definitions.
		workflowLoader = workflow.NewRemoteLoader()
	}
	app := &App{
		deps:                 deps,
		topbar:               topbar.New(),
		conversation:         conv,
		input:                inputModel,
		approval:             approval.New(),
		jobsPanel:            jobs_ui.New(),
		agentView:            agentview.New(),
		workflowView:         workflowview.New(),
		picker:               picker.New("", ""),
		prompt:               prompt.New(""),
		spinner:              spinner.New(),
		autocomplete:         autocomplete.New(slashEntries),
		slashCommands:        slashCommands,
		slashEntries:         slashEntries,
		agent:                a,
		approver:             approver,
		permissionPolicy:     policy,
		permissionBase:       basePolicy,
		jobs:                 deps.Jobs,
		workflow:             deps.Workflow,
		workflowLoader:       workflowLoader,
		backups:              deps.Backups,
		mcp:                  deps.MCP,
		contextMgr:           ctxMgr,
		statusLine:           statusRunner,
		startedAt:            time.Now(),
		jobSeqSeen:           map[string]int64{},
		jobTerminalSeen:      map[string]bool{},
		jobWorktreeSeen:      map[string]bool{},
		workflowTerminalSeen: map[string]bool{},
	}

	if deps.Jobs != nil {
		// Fan every snapshot transition from the manager into Update.
		// The callback runs off the Bubble Tea thread; sendMsg is set
		// by the host after tea.NewProgram (see main.go).
		deps.Jobs.Subscribe(func(snap jobs.Snapshot) {
			if app.sendMsg != nil {
				app.sendMsg(jobUpdateMsg{Snap: snap})
			}
		})
	}

	if deps.Workflow != nil {
		// Fan every workflow run transition into Update via the same
		// off-thread sendMsg bridge used for background jobs.
		deps.Workflow.Subscribe(func(u workflow.RunUpdate) {
			if app.sendMsg != nil {
				app.sendMsg(workflowUpdateMsg{Run: u.Run})
			}
		})
	}

	app.refreshTopBar()
	if deps.ResumeHydrate {
		if cur := deps.Sessions.Current(); cur != nil {
			app.showResumedSession(cur)
		}
	}
	return app, nil
}

func sugarCacheAgentConfig(cfg *config.Config) agent.SugarCacheConfig {
	if cfg == nil || !cfg.SugarIsEnabled() {
		return agent.SugarCacheConfig{}
	}
	return agent.SugarCacheConfig{
		Enabled:   cfg.Sugar.EffectiveCacheMode() != "off",
		Mode:      provider.SugarCacheMode(cfg.Sugar.EffectiveCacheMode()),
		Retention: provider.SugarCacheRetention(cfg.Sugar.EffectiveCacheRetention()),
		Privacy:   provider.SugarPrivacyMode(cfg.Sugar.EffectivePrivacy()),
	}
}

func conduitShadowAgentConfig(cfg *config.Config) agent.ConduitShadowConfig {
	if cfg == nil || !cfg.SugarIsEnabled() {
		return agent.ConduitShadowConfig{}
	}
	return agent.ConduitShadowConfig{
		Enabled:         cfg.ConduitIsEnabled(),
		Timeout:         time.Duration(cfg.Conduit.TimeoutMS) * time.Millisecond,
		CapsuleMaxBytes: cfg.Conduit.CapsuleMaxBytes,
	}
}

// SetSendFunc wires the tea.Program.Send bridge. Host (main.go) calls
// this between tea.NewProgram and prog.Run so off-thread callbacks (the
// jobs.Manager subscriber) can post messages into the Update loop.
func (a *App) SetSendFunc(fn func(tea.Msg)) {
	a.sendMsg = fn
	a.approver.SetNotify(func() {
		if a.sendMsg != nil {
			a.sendMsg(approvalPendingMsg{})
		}
	})
	// Wired here rather than at construction: the hook posts a message, and
	// there is nowhere to post one until the bridge exists.
	a.wireSkillGrants()
}

// Approver returns the App's uiApprover so the host can inject it as the
// jobs.Manager parent approver. Hidden behind the agent.Approver
// interface because that's what jobs.Manager wants.
func (a *App) Approver() agent.Approver {
	return a.approver
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tickTopbar(),
		a.refreshGitBranch(),
		func() tea.Msg { return approvalPendingMsg{} },
	)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := a.updateInner(msg)
	// Convert any rendered messages the inner handlers queued on the
	// conversation into tea.Println commands so they commit to the
	// terminal's native scrollback above the live region. This is the
	// single choke-point that knows about Println; all call sites stay
	// as plain `a.conversation.Append*` etc.
	drained := a.conversation.DrainEmits()
	if len(drained) == 0 {
		return model, cmd
	}
	// One print message preserves FIFO ordering. tea.Batch-ing one Println per
	// block lets the runtime deliver them concurrently and visibly reorder a
	// rejection/system note pair under load.
	printCmd := tea.Println(strings.Join(drained, "\n"))
	if cmd != nil {
		return model, tea.Batch(printCmd, cmd)
	}
	return model, printCmd
}

func (a *App) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.resize(msg.Width, msg.Height)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)

	case input.SubmitMsg:
		// Record every submission for Up/Down recall before it is dispatched.
		a.recordHistory(msg.Text)
		// Force-close the autocomplete popup at the start of the submit
		// path so it doesn't linger across a send (the buffer is about
		// to be reset anyway, but be explicit — cheaper than audit).
		a.autocomplete.Close()
		// Slash commands are UI-side concerns: they don't hit the LLM.
		// Intercept them before startTurn so /spawn, /jobs, /cancel etc.
		// take effect immediately without invoking agent.Run.
		if cmd, args, ok := a.slashRegistry().Parse(msg.Text); ok {
			return a.handleSlashCommand(cmd, args, msg.Text)
		}
		if slashCommandText(msg.Text) {
			a.input.Reset()
			a.conversation.AppendSystem(unknownSlashCommandMessage(msg.Text))
			return a, nil
		}
		if prompt, ok := escapedSlashPrompt(msg.Text); ok {
			if a.streaming {
				a.queueInput(prompt)
				return a, nil
			}
			return a.startTurn(prompt, true)
		}
		if a.streaming {
			a.queueInput(msg.Text)
			return a, nil
		}
		return a.startTurn(msg.Text, true)

	case jobUpdateMsg:
		if a.agentView.Visible() && a.jobs != nil {
			a.agentView.SetJobs(a.jobs.List())
		}
		if a.jobsPanel.Visible() && a.jobs != nil && a.jobsPanel.JobID() == msg.Snap.ID {
			if transcript, ok := a.jobs.Transcript(msg.Snap.ID); ok {
				a.jobsPanel.RefreshJob(msg.Snap, transcript)
			}
		}
		if a.workflowView.Visible() && a.workflow != nil {
			// A workflow's agents are ordinary jobs; refresh the live
			// view so agent rows track job-state transitions too.
			a.workflowView.SetRuns(a.workflow.List())
		}
		return a.handleJobUpdate(msg.Snap)

	case workflowUpdateMsg:
		if a.workflowView.Visible() && a.workflow != nil {
			a.workflowView.SetRuns(a.workflow.List())
		}
		return a.handleWorkflowUpdate(msg.Run)

	case agentEventMsg:
		return a.handleAgentEvent(msg.ev)

	case agentEventBatch:
		return a.reentrantHandle(msg)

	case agentDoneMsg:
		a.streaming = false
		a.spinner.Stop()
		a.conversation.FinaliseAgent()
		a.clearOperation()
		// Release the turn ctx now that the goroutine has drained. In
		// the normal-exit path this is a no-op (ctx already done); in
		// the error-exit path EventError already cleared it. This is
		// the canonical clear.
		if a.cancelTurn != nil {
			a.cancelTurn()
			a.cancelTurn = nil
		}
		// However the turn ended -- completed, cancelled, errored -- a widening
		// it was given does not outlive it.
		if note := a.releaseSkillGrant(); note != "" {
			a.conversation.AppendSystem("skills: " + note)
		}
		// A self-paced loop that owns this turn decides whether to run again
		// -- unless the turn did not finish on its own terms.
		if a.activeLoopID != "" {
			if a.turnFailed {
				a.stopLoopAfterFailedTurn()
			} else if cmd := a.onLoopTurnDone(); cmd != nil {
				return a, cmd
			}
		}
		if a.turnFailed {
			a.pauseQueuedInputs()
		}
		return a.startNextQueuedInput()

	case skillLoadedMsg:
		if msg.applied != nil {
			defer close(msg.applied)
		}
		if !a.streaming || msg.turnID != a.skillTurnID || (msg.ctx != nil && msg.ctx.Err() != nil) {
			return a, nil
		}
		// The model selected a skill mid-turn. Same containment as the typed
		// path: trusted scopes only, deny floors intact, released when the turn
		// ends. Applied here rather than in the tool because this is the
		// goroutine that may touch App state.
		if note := a.applySkillGrant(msg.skill); note != "" {
			a.conversation.AppendSystem("skills: " + note)
		}
		return a, nil

	case loopTickMsg:
		ls, ok := a.loops[msg.id]
		if !ok || ls.stopped || ls.mode != loopInterval {
			return a, nil // loop stopped or gone → stop ticking
		}
		if a.streaming || a.queuePaused {
			// The previous body is still running. Queueing another copy on
			// every tick grew the queue without bound when the body outlasts
			// the interval; skip this tick and try again on the next one.
			return a, loopTick(ls.id, ls.interval)
		}
		return a, tea.Batch(a.runLoopBody(ls), loopTick(ls.id, ls.interval))

	case compactDoneMsg:
		return a.handleCompactDone(msg)

	case approval.ResultMsg:
		a.resolveApprovalResult(msg)
		return a, nil

	case mcpRestartedMsg:
		unregisterMCPClientTools(a.deps.Tools, msg.Previous)
		if msg.Err != nil {
			a.conversation.AppendSystem(formatMCPRestartError(msg.Name, msg.Report, msg.Err))
			return a, nil
		}
		registrations := mcp.RegisterTools(a.deps.Tools, []*mcp.Client{msg.Client})
		registered := 0
		for _, registration := range registrations {
			if registration.Status == "registered" {
				registered++
			}
		}
		a.conversation.AppendSystem(fmt.Sprintf(
			"mcp restart: %s running (pid %d, %d tools registered)",
			msg.Name,
			msg.Report.PID,
			registered,
		))
		return a, nil

	case picker.SelectMsg:
		switch msg.PickerID {
		case "provider":
			// Selecting a provider that has no key yet opens the key
			// prompt instead of attempting a switch that would fail at
			// the first turn. Everything else goes through the normal
			// switch path.
			if !a.providerHasKey(msg.Item.ID) {
				return a, a.openProviderKeyPrompt(msg.Item.ID)
			}
			if err := a.applyProviderSwitch(msg.Item.ID); err != nil {
				a.conversation.AppendSystem("provider: " + err.Error())
			}
		case "model":
			if err := a.applyModelSwitch(msg.Item.ID); err != nil {
				a.conversation.AppendSystem("model: " + err.Error())
			}
		case "session":
			// Selecting the session already loaded would tear down and rebuild
			// the one the user is sitting in, losing nothing but achieving
			// nothing either. Say so instead.
			if cur := a.deps.Sessions.Current(); cur != nil && cur.ID == msg.Item.ID {
				a.conversation.AppendSystem("resume: already in this session")
				return a, nil
			}
			return a.resumeSessionByID(msg.Item.ID, "resume")
		}
		return a, nil

	case picker.CloseMsg:
		return a, nil

	case prompt.SubmitMsg:
		return a.handlePromptSubmit(msg)

	case prompt.CancelMsg:
		a.prompt.Hide()
		a.providerKeyValidationSeq++
		a.providerKeyValidationActive = false
		return a, nil

	case agentview.CloseMsg:
		a.agentDispatchFocused = false
		a.input.Focus()
		return a, nil

	case agentview.OpenMsg:
		return a.openJobTranscript(msg.JobID, "agent")

	case agentview.PeekMsg:
		return a.handleAgentPeek(msg.JobID)

	case agentview.CancelMsg:
		return a.handleAgentCancel(msg.JobID)

	case agentview.InjectMsg:
		return a.handleAgentInject(msg.JobID)

	case agentview.IgnoreMsg:
		return a.handleAgentIgnore(msg.JobID)

	case workflowview.CloseMsg:
		return a, nil

	case workflowview.OpenMsg:
		return a.openJobTranscript(msg.JobID, "agent")

	case workflowview.CancelMsg:
		return a.handleWorkflowCancel(msg.RunID)

	case providerKeyValidatedMsg:
		return a.handleProviderKeyValidated(msg)

	case approvalPendingMsg:
		a.showPendingApproval()
		if a.sendMsg == nil && a.streaming {
			return a, pollApproverFallback()
		}
		return a, nil

	case toolOutputFlushMsg:
		return a, a.flushToolOutput()

	case tickTopbarMsg:
		// Cheap liveness sweep. The approver notifies when an envelope is
		// abandoned, but an embedder without a Send bridge never receives that
		// notify — the tick is the backstop that still closes a dead modal.
		a.showPendingApproval()
		a.refreshTopBar()
		branchCmd := a.refreshGitBranch()
		var statusCmd tea.Cmd
		if !a.gitBranchInFlight {
			statusCmd = a.renderStatusLine(false)
		}
		return a, tea.Batch(tickTopbar(), branchCmd, statusCmd)

	case gitBranchMsg:
		a.gitBranchInFlight = false
		a.gitBranch = msg.branch
		a.refreshTopBar()
		// The first external statusline render may have raced the initial branch
		// lookup. Let it run once more with the populated snapshot.
		if a.statusLine != nil && a.statusLine.Enabled() {
			a.statusLineLastRun = time.Time{}
			return a, a.renderStatusLine(false)
		}
		return a, nil

	case statusLineMsg:
		if msg.seq == a.statusLineInFlight {
			a.statusLineInFlight = 0
		}
		if msg.seq == a.statusSeq {
			if msg.err == nil {
				a.lastStatusLineErr = nil
				a.topbar.SetCustomLine(msg.line)
				if msg.manual {
					a.conversation.AppendSystem("statusline: refreshed")
				}
			} else {
				a.lastStatusLineErr = msg.err
				a.topbar.SetCustomLine("")
				if msg.manual {
					a.conversation.AppendSystem("statusline: error: " + msg.err.Error())
				}
			}
		}
		return a, nil

	case ollamaInfoMsg:
		if msg.err != nil {
			a.conversation.AppendSystem("ollama: " + msg.err.Error())
		} else {
			a.conversation.AppendSystem(msg.text)
		}
		return a, nil
	}

	// Delegate to the focused subcomponent. Focus precedence:
	//   approval > picker > jobsPanel > agentView > conversation/input.
	// The approval prompt blocks the agent loop; the picker covers
	// everything beneath it while it owns the keyboard; the jobs
	// transcript modal scrolls on j/k when open; otherwise the
	// conversation + input consume.
	var cmds []tea.Cmd
	if a.approval.Visible() {
		var cmd tea.Cmd
		a.approval, cmd = a.approval.Update(msg)
		cmds = append(cmds, cmd)
	} else if a.picker.Visible() {
		var cmd tea.Cmd
		a.picker, cmd = a.picker.Update(msg)
		cmds = append(cmds, cmd)
	} else if a.jobsPanel.Visible() {
		var cmd tea.Cmd
		a.jobsPanel, cmd = a.jobsPanel.Update(msg)
		cmds = append(cmds, cmd)
	} else if a.agentView.Visible() {
		var cmd tea.Cmd
		a.agentView, cmd = a.agentView.Update(msg)
		cmds = append(cmds, cmd)
	} else if a.workflowView.Visible() {
		var cmd tea.Cmd
		a.workflowView, cmd = a.workflowView.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
		a.conversation, cmd = a.conversation.Update(msg)
		cmds = append(cmds, cmd)
		// The input may have mutated on this path (printable-rune
		// messages arrive as generic tea.Msg, not KeyMsg). Refresh so
		// the popup tracks what's in the buffer.
		a.refreshAutocomplete()
	}
	if a.spinner.Active() {
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}
	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Autocomplete popup takes precedence over BOTH the global
	// shortcuts and the modal-visible guards for the keys that
	// coordinate with its selection (Esc / Tab / Enter / arrows /
	// Ctrl+N/P/J/K). Ctrl+P in particular collides with the provider
	// picker's global shortcut — while the popup is up, the user is
	// obviously navigating the popup, so route it there first. Any
	// other key falls through so the input still receives it and the
	// popup tracks the edit.
	if a.autocomplete.Visible() {
		switch msg.String() {
		case "esc":
			a.autocomplete.Close()
			return a, nil
		case "tab":
			if verb := a.autocomplete.SelectedVerb(); verb != "" {
				if a.autocomplete.Kind() == autocomplete.KindFile {
					return a, a.acceptMentionAutocomplete(verb)
				}
				return a, a.acceptAutocomplete(verb)
			}
			return a, nil
		case "enter":
			verb := a.autocomplete.SelectedVerb()
			if a.autocomplete.Kind() == autocomplete.KindFile {
				// File mentions: Enter accepts whenever a row is
				// highlighted (the token is mid-buffer, so there is no
				// "bare verb" restriction — accepting splices the path and
				// leaves the rest of the line intact).
				if verb != "" {
					return a, a.acceptMentionAutocomplete(verb)
				}
				// No match → fall through to the normal submit path.
			} else {
				text := a.input.Value()
				bufferIsBareVerb := strings.HasPrefix(text, "/") &&
					!strings.ContainsAny(text, " \t\n")
				if verb != "" && bufferIsBareVerb {
					return a, a.acceptAutocomplete(verb)
				}
			}
			// Fall through to the input's SubmitMsg path (no matches,
			// or buffer already contains args — let the user send it).
		case "up", "down", "ctrl+n", "ctrl+p", "ctrl+k", "ctrl+j":
			var cmd tea.Cmd
			a.autocomplete, cmd = a.autocomplete.Update(msg)
			return a, cmd
		}
	}

	// Permission mode remains live for the duration of a turn. Keep this ahead
	// of the approval prompt so manual → accept-edits/auto can release an
	// approval that is already waiting. Other modal workspaces retain ownership
	// of their keyboard shortcuts.
	if msg.String() == "shift+tab" && !a.prompt.Visible() && !a.picker.Visible() && !a.jobsPanel.Visible() && !a.agentView.Visible() && !a.workflowView.Visible() {
		a.cyclePermissionMode()
		return a, nil
	}

	switch msg.String() {
	case "ctrl+p":
		if a.modalOwnsKeyboard() {
			return a, nil
		}
		return a, a.openProviderPicker()
	case "alt+m":
		// Bubble Tea v1 cannot distinguish Ctrl+M from Enter. Alt+M has a
		// distinct event when the terminal reports Alt, so it cannot be
		// mistaken for a draft-submitting Enter.
		if a.modalOwnsKeyboard() {
			return a, nil
		}
		return a, a.openModelPicker()
	case "ctrl+c":
		if a.streaming {
			// First Ctrl+C while streaming: cancel the in-flight turn
			// ctx (kills the provider HTTP request, unblocks any pending
			// approval, kills any running tool), clear the CancelFunc so
			// a *second* Ctrl+C during the goroutine's drain window is a
			// no-op instead of a quit, and visually settle. We
			// deliberately do NOT clear a.streaming here — agentDoneMsg
			// owns that transition once the channel closes. State
			// machine: (streaming && cancelTurn!=nil) -> first press
			// cancels; (streaming && cancelTurn==nil) -> second press
			// is a no-op; (!streaming) -> quit.
			// The provider may already have buffered success. Record the user's
			// cancellation now so a self-paced loop cannot restart after drain.
			a.turnFailed = true
			if a.cancelTurn != nil {
				a.cancelTurn()
				a.cancelTurn = nil
			}
			a.spinner.Stop()
			a.markOperationCancelling()
			// Ctrl+C cancels the user's own turn. A background job's approval
			// is not part of that turn: rejecting it here would stop work the
			// user never asked to stop, just because its prompt happened to be
			// the one on screen.
			if a.approval.Visible() && a.approvalOrigin == originForeground {
				a.rejectVisibleApproval("cancelled")
			}
			a.clearQueuedInputs()
			a.showPendingApproval()
			return a, nil
		}
		if a.prompt.Visible() {
			a.prompt.Hide()
			return a, nil
		}
		if a.approval.Visible() {
			// Nothing else is in flight, so Ctrl+C can only mean the prompt on
			// screen. Unlike the streaming branch this is a deliberate answer
			// to what is displayed, so it applies whatever the origin.
			a.rejectVisibleApproval("cancelled")
			a.showPendingApproval()
			return a, nil
		}
		if !a.picker.Visible() && !a.jobsPanel.Visible() && !a.agentView.Visible() && !a.workflowView.Visible() && a.input.Value() != "" {
			a.input.Reset()
			a.autocomplete.Close()
			return a, nil
		}
		return a, tea.Quit
	case "ctrl+d":
		if !a.streaming && !a.prompt.Visible() && !a.approval.Visible() && !a.picker.Visible() && !a.jobsPanel.Visible() && !a.agentView.Visible() && !a.workflowView.Visible() && a.input.Value() == "" {
			return a, tea.Quit
		}
	case "ctrl+l":
		if a.modalOwnsKeyboard() {
			return a, nil
		}
		return a.handleClearCommand(nil)
	}

	if a.prompt.Visible() {
		var cmd tea.Cmd
		a.prompt, cmd = a.prompt.Update(msg)
		return a, cmd
	}
	if a.approval.Visible() {
		var cmd tea.Cmd
		a.approval, cmd = a.approval.Update(msg)
		return a, cmd
	}
	if a.picker.Visible() {
		// Intercept ctrl+a on the provider picker to jump into the
		// API-key-entry flow for the focused row. Everything else falls
		// through to the picker's own Update.
		if msg.String() == "ctrl+a" && a.picker.ID() == "provider" {
			if slug := a.picker.CursorID(); slug != "" {
				return a, a.openProviderKeyPrompt(slug)
			}
		}
		var cmd tea.Cmd
		a.picker, cmd = a.picker.Update(msg)
		return a, cmd
	}
	if a.jobsPanel.Visible() {
		var cmd tea.Cmd
		a.jobsPanel, cmd = a.jobsPanel.Update(msg)
		return a, cmd
	}
	if a.agentView.Visible() {
		// Agent View has two explicit focus states. List focus owns navigation
		// and row actions; n enters the task composer. This prevents printable
		// shortcuts (p/c/i/x/o) from becoming accidental draft text.
		if !a.agentDispatchFocused {
			if msg.String() == "n" {
				a.agentDispatchFocused = true
				a.input.Reset()
				a.input.Focus()
				return a, nil
			}
			var cmd tea.Cmd
			a.agentView, cmd = a.agentView.Update(msg)
			if !a.agentView.Visible() {
				a.agentDispatchFocused = false
				a.input.Focus()
			}
			return a, cmd
		}

		if msg.String() == "enter" {
			text := strings.TrimSpace(a.input.Value())
			if text == "" {
				a.agentDispatchFocused = false
				a.input.Blur()
				return a, nil
			}
			a.input.Reset()
			model, cmd := a.handleSpawnCommand([]string{text})
			if a.jobs != nil {
				a.agentView.Show(a.jobs.List())
			}
			a.agentDispatchFocused = false
			a.input.Blur()
			return model, cmd
		}
		if msg.String() == "esc" {
			a.input.Reset()
			a.agentDispatchFocused = false
			a.input.Blur()
			return a, nil
		}
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		return a, cmd
	}
	if a.workflowView.Visible() {
		var cmd tea.Cmd
		a.workflowView, cmd = a.workflowView.Update(msg)
		return a, cmd
	}
	// Claude Code exposes Agent View from the empty prompt with Left Arrow and
	// advertises the shortcut in every permission-mode footer. Preserve normal
	// cursor movement whenever the input contains text.
	if msg.String() == "left" && a.jobs != nil && !a.streaming && a.input.Value() == "" {
		a.showAgentView()
		return a, nil
	}
	// Up/Down page through previously submitted prompts, shell-style — but
	// only when the caret is at the top/bottom of the buffer, so multi-line
	// editing keeps normal caret movement. The autocomplete popup, when
	// visible, consumes Up/Down earlier and never reaches here.
	switch msg.String() {
	case "up":
		if a.input.AtFirstLine() && a.historyPrev() {
			return a, nil
		}
	case "down":
		if a.input.AtLastLine() && a.historyNext() {
			return a, nil
		}
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	// After input has consumed the key, refresh the popup so it opens
	// on "/" and closes when a space lands or the slash disappears.
	a.refreshAutocomplete()
	return a, cmd
}

// refreshAutocomplete recomputes the popup state from the current input
// buffer. Called after every input-mutating key path so the popup
// tracks what the user is typing. Closes when any modal is up (they
// block the input anyway), when the buffer no longer starts with "/",
// or when whitespace landed after the verb.
func (a *App) refreshAutocomplete() {
	if a.approval.Visible() || a.picker.Visible() || a.jobsPanel.Visible() || a.agentView.Visible() || a.workflowView.Visible() {
		a.autocomplete.Close()
		return
	}
	text := a.input.Value()

	// Slash completer: the whole buffer is a bare "/verb" with no
	// whitespace yet. These are the pre-existing conditions — unchanged.
	if strings.HasPrefix(text, "/") && !strings.ContainsAny(text, " \t\n") {
		// Restore the slash entry set if we'd swapped in the file index.
		if a.autocomplete.Kind() == autocomplete.KindFile {
			a.autocomplete.SetEntries(a.slashEntries)
		}
		a.autocomplete.SetKind(autocomplete.KindSlash)
		filter := strings.TrimPrefix(text, "/")
		a.autocomplete.SetWidth(a.width)
		if a.autocomplete.Visible() {
			a.autocomplete.SetFilter(filter)
		} else {
			a.autocomplete.Open(filter)
		}
		return
	}

	// File-mention completer: the token ending at the caret is "@query".
	start, end, query, ok := activeMentionTokenAtCursor(text, a.input.CursorByteOffset())
	if !ok {
		a.autocomplete.Close()
		return
	}
	if a.deps.RemoteWorkspace {
		// @file expansion currently reads local files. Remote workspaces use
		// read_file explicitly until mention indexing is backend-aware.
		a.autocomplete.Close()
		return
	}
	if !a.fileIndexBuilt {
		a.fileIndex = buildMentionEntries(a.deps.WorkingDir)
		a.fileIndexBuilt = true
	}
	a.mentionStart = start
	a.mentionEnd = end
	// Only swap the (potentially large) file list in when entering file
	// mode; on subsequent keystrokes the entries are already loaded and we
	// just re-filter.
	if a.autocomplete.Kind() != autocomplete.KindFile {
		a.autocomplete.SetEntries(a.fileIndex)
		a.autocomplete.SetKind(autocomplete.KindFile)
	}
	a.autocomplete.SetWidth(a.width)
	if a.autocomplete.Visible() {
		a.autocomplete.SetFilter(query)
	} else {
		a.autocomplete.Open(query)
	}
}

// acceptAutocomplete handles the user accepting a highlighted popup row
// (Tab, or Enter on a bare verb). For verbs whose only job is to open a
// selection modal — /provider and /model and their plural aliases — we
// skip the fill-the-buffer dance and open the picker straight away, so
// the user picks from a list instead of guessing a slug/id. Every other
// verb swaps the buffer for "/<verb> ": the trailing space feels natural
// to continue typing args after and trips refreshAutocomplete's close
// path on the next keystroke. Returns a tea.Cmd for the picker open
// (nil for the buffer-fill case).
func (a *App) acceptAutocomplete(verb string) tea.Cmd {
	a.autocomplete.Close()
	switch verb {
	case "provider", "providers":
		a.input.Reset()
		return a.openProviderPicker()
	case "model", "models":
		a.input.Reset()
		return a.openModelPicker()
	}
	a.input.SetValue("/" + verb + " ")
	return nil
}

// acceptMentionAutocomplete handles the user accepting a highlighted file row
// (Tab, or Enter). It splices "@<path> " over the active "@query" token in the
// buffer, closes the popup, and re-refreshes so the popup reflects the new
// buffer (the trailing space means the mention token is complete, so the
// popup stays closed until the next "@").
func (a *App) acceptMentionAutocomplete(path string) tea.Cmd {
	a.input.ReplaceMention(a.mentionStart, a.mentionEnd, path)
	a.autocomplete.Close()
	a.refreshAutocomplete()
	return nil
}

func (a *App) View() string {
	if a.width <= 0 || a.height <= 0 {
		return ""
	}
	// The secret-entry prompt already owns the full terminal rectangle.
	// Rendering the composer/status beneath it makes the inline frame taller
	// than the terminal and clips the modal on short windows.
	if a.prompt.Visible() {
		return a.prompt.View()
	}

	// Inline rendering: finalised messages live in the terminal's native
	// scrollback (committed via tea.Println on DrainEmits). The View()
	// return is only the live region that redraws at the bottom of the
	// terminal: pending streaming content, any overlay modal, the
	// autocomplete popup, input, and topbar.
	status := a.topbar.View()
	in := a.input.View()
	if a.approval.Visible() || a.picker.Visible() || a.jobsPanel.Visible() {
		in = a.input.ViewBlurred()
	}
	if a.agentView.Visible() {
		// Agent View owns the screen's lifecycle summary and shortcut footer.
		// Keep the input geometry but use its task-oriented placeholder.
		placeholder := "press n to describe a task for a new agent"
		if a.agentDispatchFocused {
			placeholder = "describe a task for a new agent"
		}
		in = a.input.ViewWithPlaceholder(placeholder)
		status = ""
	} else if a.workflowView.Visible() {
		// Workflows are a full-screen navigator with their own footer.
		in = ""
		status = ""
	}
	pending := a.conversation.PendingView()

	overlay := ""
	if a.approval.Visible() {
		overlay = a.approval.View()
	} else if a.picker.Visible() {
		overlay = a.picker.View()
	} else if a.jobsPanel.Visible() {
		overlay = a.jobsPanel.View()
	} else if a.agentView.Visible() {
		overlay = a.agentView.View()
	} else if a.workflowView.Visible() {
		overlay = a.workflowView.View()
	} else if a.spinner.Active() {
		overlay = a.spinner.View()
	}

	aboveInput := ""
	if overlay == "" && a.autocomplete.Visible() {
		aboveInput = a.autocomplete.View()
	}

	// Claude Code-style permission-mode footer, below the statusline. Suppress
	// it while a modal overlay owns the screen to avoid clutter.
	if overlay == "" {
		if hint := a.permModeHint(); hint != "" {
			if status != "" {
				status += "\n"
			}
			status += "  " + hint
		}
	}

	return layout.Frame(pending, overlay, aboveInput, in, status)
}

// ────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ────────────────────────────────────────────────────────────────────────────

// resize stores the new terminal dimensions and propagates width to
// components that wrap text. Inline rendering means the live region is
// just its natural height at the bottom of the terminal — the
// conversation no longer owns a fullscreen viewport, so we don't need
// to compute a body budget.
func (a *App) resize(w, h int) {
	a.width = w
	a.height = h
	a.topbar.SetWidth(w)
	a.input.Resize(w, 0)
	a.approval.SetWidth(w)
	modalH := h - 8
	if modalH < 8 {
		modalH = 8
	}
	a.jobsPanel.Resize(w, modalH)
	a.agentView.Resize(w, modalH)
	a.workflowView.Resize(w, modalH)
	a.picker.Resize(w, h)
	a.prompt.Resize(w, h)
	a.autocomplete.SetWidth(w)
	a.conversation.Resize(w, h)
	a.refreshDefaultStatusLine()
	// First WindowSizeMsg after startup: commit the welcome splash to
	// scrollback via the conversation's emit queue (picked up by
	// DrainEmits in Update).
	a.conversation.EmitWelcomeSplash()
}

func (a *App) refreshTopBar() {
	if prov, modelID := a.deps.Registry.Active(); prov != nil {
		a.topbar.SetProvider(prov.Slug(), prov.Name(), modelID)

		// Context window from active model. We keep this best-effort —
		// providers like Ollama report 0 (unknown).
		ctxMax := prov.ContextWindow(modelID)
		used := 0
		if cur := a.deps.Sessions.Current(); cur != nil {
			// Gauge current context occupancy, not the cumulative session
			// total — the latter climbs past the window every few turns.
			used = cur.TokenUsage.ContextTokens
		}
		a.topbar.SetContext(used, ctxMax)
	}

	root := a.deps.WorkingDir
	a.topbar.SetProject(filepath.Base(root), a.gitBranch)

	// The ⚙ N jobs counter reflects StateQueued + StateRunning jobs. We
	// pass 0 when no manager is wired so the segment stays hidden in
	// configurations where background agents are disabled. ActiveCount
	// is lock-guarded, so calling it on every refreshTopBar tick (15s)
	// and every job state transition is cheap.
	if a.jobs != nil {
		a.topbar.SetJobs(a.jobs.ActiveCount())
	} else {
		a.topbar.SetJobs(0)
	}
	if a.planMode {
		a.topbar.SetPermissionProfile("plan")
	} else if a.permissionPolicy != nil {
		a.topbar.SetPermissionProfile(permissions.ProfileConfigName(a.permissionPolicy.Profile()))
	} else {
		a.topbar.SetPermissionProfile("")
	}
	a.topbar.SetOperation(a.streaming, a.operationLabel, a.operationStarted, len(a.queuedInputs))

	// When no external statusline command is configured, render packetcode's
	// built-in Claude Code-style statusline natively (no jq/subprocess) and
	// feed it through the top bar's custom-line slot. The per-second refresh
	// keeps the live operation timer current; resize also recomposes the
	// width-prioritized segments. An external [statusline].command, when set,
	// owns the custom line instead (see renderStatusLine).
	a.refreshDefaultStatusLine()
}

func (a *App) refreshDefaultStatusLine() {
	if a.statusLine != nil && a.statusLine.Enabled() {
		return
	}
	contentWidth := a.width - 4 // topbar has two columns of padding per side
	a.topbar.SetCustomLine(statusline.RenderDefaultWidth(a.statusLineSnapshot(), contentWidth))
}

func (a *App) renderStatusLine(manual bool) tea.Cmd {
	if a.statusLine == nil || !a.statusLine.Enabled() {
		return nil
	}
	if !manual {
		if a.statusLineInFlight != 0 {
			return nil
		}
		if !a.statusLineLastRun.IsZero() && time.Since(a.statusLineLastRun) < 15*time.Second {
			return nil
		}
	}
	a.statusSeq++
	seq := a.statusSeq
	a.statusLineInFlight = seq
	a.statusLineLastRun = time.Now()
	snap := a.statusLineSnapshot()
	return func() tea.Msg {
		line, err := a.statusLine.Render(context.Background(), snap)
		return statusLineMsg{seq: seq, line: line, err: err, manual: manual}
	}
}

func (a *App) statusLineSnapshot() statusline.Snapshot {
	root := a.deps.WorkingDir
	project := filepath.Base(root)
	branch := a.gitBranch
	var sessionID string
	var used, cacheCreation, cacheRead int
	if cur := a.deps.Sessions.Current(); cur != nil {
		sessionID = cur.ID
		// Current context occupancy (see refreshTopBar) — matches the top
		// bar so the two gauges never disagree.
		used = cur.TokenUsage.ContextTokens
		// The context-scoped cache split, not the cumulative one: these
		// describe the same request as ContextTokens and so stay within it.
		cacheCreation = cur.TokenUsage.ContextCacheCreation
		cacheRead = cur.TokenUsage.ContextCacheRead
	}
	var provSlug, provName, modelID, reasoningEffort string
	var max int
	if prov, activeModel := a.deps.Registry.Active(); prov != nil {
		provSlug = prov.Slug()
		provName = prov.Name()
		modelID = activeModel
		max = prov.ContextWindow(activeModel)
		if controller, ok := prov.(provider.ReasoningEffortController); ok {
			reasoningEffort = controller.ReasoningEffort(activeModel)
		}
	}
	pct := 0
	if max > 0 {
		pct = used * 100 / max
		if pct > 100 {
			pct = 100
		}
	}
	totalCost := 0.0
	if a.deps.CostTracker != nil {
		totalCost = a.deps.CostTracker.TotalCost()
	}
	activeJobs := 0
	if a.jobs != nil {
		activeJobs = a.jobs.ActiveCount()
	}
	opElapsed := 0
	if a.streaming && !a.operationStarted.IsZero() {
		opElapsed = int(time.Since(a.operationStarted).Seconds())
	}
	return statusline.Snapshot{
		SessionID:  sessionID,
		WorkingDir: root,
		Project:    project,
		GitBranch:  branch,
		Provider:   statusline.ProviderInfo{Slug: provSlug, DisplayName: provName},
		Model:      statusline.ModelInfo{ID: modelID, ReasoningEffort: reasoningEffort},
		ContextWindow: statusline.ContextInfo{
			Used:           used,
			Max:            max,
			UsedPercentage: pct,
			CacheCreation:  cacheCreation,
			CacheRead:      cacheRead,
		},
		Cost: statusline.CostInfo{TotalCostUSD: totalCost},
		Jobs: statusline.JobsInfo{Active: activeJobs},
		Operation: statusline.OperationInfo{
			Active:         a.streaming,
			Label:          a.operationLabel,
			ElapsedSeconds: opElapsed,
			QueuedInputs:   len(a.queuedInputs),
		},
		DurationSeconds: int(time.Since(a.startedAt).Seconds()),
		Version:         a.deps.Version,
	}
}

func (a *App) refreshGitBranch() tea.Cmd {
	if a.deps.RemoteWorkspace {
		return nil
	}
	if a.gitBranchInFlight || (!a.gitBranchLastRun.IsZero() && time.Since(a.gitBranchLastRun) < gitBranchRefreshInterval) {
		return nil
	}
	a.gitBranchInFlight = true
	a.gitBranchLastRun = time.Now()
	root := a.deps.WorkingDir
	return func() tea.Msg {
		return gitBranchMsg{branch: git.Branch(root)}
	}
}

// showPendingApproval reconciles the modal with the approver queue. It is
// called on every approvalPendingMsg and on the top bar tick, so an approval
// that dies while it is displayed cannot hold the screen.
func (a *App) showPendingApproval() {
	if a.approval.Visible() {
		// approvalID == 0 means the modal was raised outside the approver
		// (the --tui-fixture screens do this). There is no envelope to check
		// liveness against, and reclaiming it would erase a screen that is
		// doing exactly what it was asked to.
		if a.approvalID == 0 || a.approver.IsLive(a.approvalID) {
			a.approval.SetQueueDepth(a.approver.QueueDepth())
			return
		}
		// The job behind this prompt was cancelled or timed out. Nobody is
		// waiting for the answer any more, and leaving it up blocks the input
		// and every other job's request behind a dead question.
		a.hideApproval()
	}
	next, ok := a.approver.Next()
	if !ok {
		return
	}
	a.autocomplete.Close()
	a.approvalID = next.id
	a.approvalOrigin = next.origin
	a.approval.Show(next.req.Tool, next.req.ToolCall)
	a.approval.SetRequestID(next.id)
	a.approval.SetWidth(a.width)
	a.approval.SetQueueDepth(a.approver.QueueDepth())
}

// hideApproval drops the modal and the identity it was bound to together.
// Clearing them separately is how a later decision finds a stale id to match.
func (a *App) hideApproval() {
	a.approval.Hide()
	a.approvalID = 0
	a.approvalOrigin = originForeground
}

// rejectVisibleApproval resolves the displayed envelope as rejected. The id is
// captured before the modal is hidden so the rejection cannot be delivered to
// a successor prompt.
func (a *App) rejectVisibleApproval(reason string) {
	id := a.approvalID
	a.hideApproval()
	a.approver.ResolveID(id, agent.ApprovalDecision{Approved: false, Reason: reason})
}

// resolveApprovalResult applies the user's answer to the envelope that answer
// was made about, and to no other.
func (a *App) resolveApprovalResult(msg approval.ResultMsg) {
	decision := agent.ApprovalDecision{Approved: false, Reason: "user rejected"}
	if msg.Result == approval.Approved {
		decision = agent.ApprovalDecision{Approved: true}
	}
	if msg.RequestID != a.approvalID {
		// The prompt the user answered is no longer the bound one. Drop the
		// decision rather than apply it to whatever took its place.
		a.showPendingApproval()
		return
	}
	resolved := a.approver.ResolveID(msg.RequestID, decision)
	a.approvalID = 0
	a.approvalOrigin = originForeground
	// "Don't ask again" installs a standing session rule. It must only follow
	// a decision that actually reached a waiting caller: remembering on behalf
	// of an abandoned prompt grants authority nobody asked for.
	if resolved && msg.Result == approval.Approved && msg.Remember {
		a.rememberApproval(msg.ToolCall)
	}
	a.showPendingApproval()
}

func (a *App) startTurn(text string, emitUser bool) (tea.Model, tea.Cmd) {
	return a.startTurnWith(turnOptions{display: text, text: text, emitUser: emitUser})
}

// turnOptions describes one turn's two texts and where its text came from.
type turnOptions struct {
	// display is the transcript line; text is what the model receives.
	display  string
	text     string
	emitUser bool
	// authored marks text the user did not type -- today, a skill body
	// expanded by /<name>. It suppresses @-mention expansion over that text.
	//
	// This is the difference between a boundary and a suggestion. A project
	// skill body is untrusted repository content wrapped in a label that says
	// so, and expandFileMentions PREPENDS the files it finds, undefanged and
	// above that label, under a heading that says "the user referenced these
	// files". A hostile repo shipping a skill whose body is a single
	// `@notes/setup.md` therefore gets arbitrary in-repo content into the turn
	// outside the only thing marking it untrusted, attributed to the user. The
	// body does not get to reach into the file tree; the person typing does.
	authored bool
	// attached names files the caller already resolved, so the "attached N
	// files" line still appears for the arguments the user did type.
	attached []string
	// loopID is the self-paced loop that owns this turn, claimed when the turn
	// actually starts rather than when it is created.
	//
	// A loop body typed during a stream is queued, and ownership has to travel
	// with it: claiming it at creation would hand the running turn to the loop,
	// and claiming it nowhere -- which is what happened -- left agentDoneMsg
	// with nothing to re-run, so the loop registered, listed forever, and did
	// nothing.
	loopID       string
	sourceLoopID string
	skillGrant   *skills.Skill
}

func (a *App) startTurnWith(opt turnOptions) (tea.Model, tea.Cmd) {
	if a.queuePaused {
		a.queueTurn(opt)
		return a, nil
	}
	return a.startTurnResolved(opt)
}

// startTurnResolved is the one place a turn begins.
//
// The transcript line and the model-facing text are allowed to differ. They
// already did for @-mentions, where the visible message keeps the literal
// @path and the model gets the file inlined. A skill expansion is the same
// shape and a much bigger one: /deploy sends up to MaxBodyBytes of framed
// skill body, and pasting that into the conversation pane buries the exchange
// the user is actually having under a document they did not write. What they
// typed is what they should see.
func (a *App) startTurnResolved(opt turnOptions) (tea.Model, tea.Cmd) {
	display, text, emitUser := opt.display, opt.text, opt.emitUser
	// Resolve model-facing additions before checking the threshold. Large file
	// mentions and the plan-mode instruction must count toward the upcoming
	// request even though the visible user message keeps the original text.
	turnText := text
	attached := opt.attached
	if !opt.authored && !a.deps.RemoteWorkspace {
		expanded, files := expandFileMentions(text, a.deps.WorkingDir)
		attached = files
		if len(attached) > 0 {
			turnText = expanded
		}
	}
	if a.planMode {
		turnText = planModeInstruction + turnText
	}

	if !a.skipAutoCompactOnce && a.shouldAutoCompact(turnText) {
		// Requeued whole. The compaction path re-runs this turn later, and
		// dropping any of these would make the same command behave one way
		// normally and another way after a compact -- including re-scanning an
		// already-resolved skill body for @-mentions.
		a.queueTurn(opt)
		a.skipAutoCompactOnce = true
		a.conversation.AppendSystem("automatic context compaction triggered")
		return a.handleCompactCommand(nil)
	}
	a.skipAutoCompactOnce = false
	a.input.Reset()
	if emitUser {
		a.conversation.AppendUser(display)
	}

	// @-file mentions: the displayed user message keeps the literal @path, but
	// the model receives the referenced files' contents inlined as context.
	if len(attached) > 0 {
		a.conversation.AppendSystem("attached " + plural(len(attached), "file", "files") + ": " + strings.Join(attached, ", "))
	}

	a.streaming = true
	// Compaction can defer or fail before this turn starts. Claim ownership
	// only now, and clear any previous owner for an ordinary prompt.
	a.activeLoopID = opt.loopID
	a.skillTurnID++
	if opt.skillGrant != nil {
		if note := a.applySkillGrant(*opt.skillGrant); note != "" {
			a.conversation.AppendSystem("skills: " + note)
		}
	}
	a.turnFailed = false
	a.lastAgentText = ""
	a.setOperation("thinking")

	// The ctx is cancellable so Ctrl+C can tear down the in-flight
	// provider HTTP request, kill any running tool, and unblock any
	// pending approval prompt. The CancelFunc is stashed on App so the
	// key handler and EventError / agentDoneMsg paths can reach it.
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, skillTurnKey{}, a.skillTurnID)
	a.cancelTurn = cancel
	stream := a.agent.Run(ctx, turnText)

	cmds := []tea.Cmd{a.spinner.Start("Thinking…"), readAgentEvent(stream)}
	// Embedders and direct model tests may not wire tea.Program.Send. Keep a
	// turn-scoped compatibility poll for them; the desktop/CLI path is entirely
	// event-driven and does no idle approval redraws.
	if a.sendMsg == nil {
		cmds = append(cmds, pollApproverFallback())
	}
	return a, tea.Batch(cmds...)
}

func (a *App) shouldAutoCompact(text string) bool {
	cur := a.deps.Sessions.Current()
	prov, modelID := a.deps.Registry.Active()
	if cur == nil || prov == nil || !a.contextMgr.CanCompact(cur.Messages, defaultCompactKeep) {
		return false
	}
	maxTokens := prov.ContextWindow(modelID)
	if maxTokens <= 0 {
		return false
	}
	used := cur.TokenUsage.ContextTokens
	if used > 0 {
		used += a.contextMgr.EstimateTokens([]provider.Message{{Role: provider.RoleUser, Content: text}})
	} else {
		messages := append([]provider.Message(nil), cur.Messages...)
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: text})
		var definitions []provider.ToolDefinition
		if a.deps.Tools != nil && prov.SupportsTools(modelID) {
			definitions = a.deps.Tools.Definitions()
		}
		used = a.contextMgr.EstimateRequest(a.deps.SystemPrompt, messages, definitions).Total
	}
	return used*100 >= a.contextMgr.Threshold()*maxTokens
}

func (a *App) queueInput(text string) {
	a.queueTurn(turnOptions{display: text, text: text})
}

// queueTurn defers a turn, keeping every fact the turn would have used so the
// deferred run is identical to the immediate one.
func (a *App) queueTurn(opt turnOptions) {
	if strings.TrimSpace(opt.text) == "" {
		a.input.Reset()
		return
	}
	a.input.Reset()
	q := queuedInput{
		Text:         opt.text,
		Display:      opt.display,
		Authored:     opt.authored,
		Attached:     opt.attached,
		LoopID:       opt.loopID,
		SourceLoopID: opt.sourceLoopID,
		SkillGrant:   opt.skillGrant,
		At:           time.Now(),
	}
	a.queuedInputs = append(a.queuedInputs, q)
	a.conversation.AppendQueuedUser(q.Label())
	if a.queuePaused {
		a.conversation.AppendSystem("added to the paused queue; /queue resume to continue, /queue clear to start fresh")
	}
	a.refreshTopBar()
}

// modalOwnsKeyboard is the single guard for global shortcuts that open or
// mutate content beneath overlays. Escape/cancel and live permission cycling
// have their own explicit routing because those operations intentionally act
// on the visible modal or pending approval.
func (a *App) modalOwnsKeyboard() bool {
	return a.prompt.Visible() || a.approval.Visible() || a.picker.Visible() ||
		a.jobsPanel.Visible() || a.agentView.Visible() || a.workflowView.Visible()
}

func (a *App) clearQueuedInputs() int {
	a.queuePaused = false
	if len(a.queuedInputs) == 0 {
		return 0
	}
	n := len(a.queuedInputs)
	a.queuedInputs = nil
	a.refreshTopBar()
	a.conversation.AppendSystem(fmt.Sprintf("cleared %d queued %s", n, plural(n, "prompt", "prompts")))
	return n
}

func (a *App) startNextQueuedInput() (tea.Model, tea.Cmd) {
	if a.queuePaused {
		return a, nil
	}
	if len(a.queuedInputs) == 0 {
		a.refreshTopBar()
		return a, nil
	}
	next := a.queuedInputs[0]
	copy(a.queuedInputs, a.queuedInputs[1:])
	a.queuedInputs = a.queuedInputs[:len(a.queuedInputs)-1]
	// Every field, not just Text. Dropping Display here sent the framed skill
	// body back through the auto-compact branch, which re-queues by display --
	// so a /deploy that happened to cross the compaction threshold pasted its
	// whole body into the pane, which is the exact thing Display exists to
	// prevent.
	return a.startTurnWith(turnOptions{
		display:      next.Label(),
		text:         next.Text,
		emitUser:     false,
		authored:     next.Authored,
		attached:     next.Attached,
		loopID:       next.LoopID,
		sourceLoopID: next.SourceLoopID,
		skillGrant:   next.SkillGrant,
	})
}

func (a *App) setOperation(label string) {
	a.operationLabel = label
	a.operationStarted = time.Now()
	a.refreshTopBar()
}

func (a *App) markOperationCancelling() {
	a.operationLabel = "cancelling"
	if a.operationStarted.IsZero() {
		a.operationStarted = time.Now()
	}
	a.refreshTopBar()
}

func (a *App) clearOperation() {
	a.operationLabel = ""
	a.operationStarted = time.Time{}
	a.refreshTopBar()
}

func (a *App) handleAgentEvent(ev agent.AgentEvent) (tea.Model, tea.Cmd) {
	prov, modelID := a.deps.Registry.Active()
	providerSlug := ""
	if prov != nil {
		providerSlug = prov.Slug()
	}

	switch ev.Type {
	case agent.EventTextDelta:
		if a.spinner.Active() {
			// First token arrived → silence the spinner.
			a.spinner.Stop()
		}
		a.lastAgentText += ev.Text
		a.conversation.AppendAgentText(modelID, providerSlug, ev.Text)

	case agent.EventReasoningDelta:
		// The reasoning summary is the model's live "thinking" — showing it
		// replaces the generic spinner.
		if a.spinner.Active() {
			a.spinner.Stop()
		}
		a.conversation.AppendAgentReasoning(modelID, providerSlug, ev.Text)

	case agent.EventToolCallProposed:
		// A proposed tool is visible progress and replaces generic thinking.
		a.spinner.Stop()
		// Carry the provider call id so streamed output chunks
		// (EventToolOutputChunk) can be routed to this exact pending block.
		a.conversation.AppendToolCallWithID(ev.ToolCall.Name, ev.ToolCall.Arguments, ev.ToolCall.ID)

	case agent.EventToolOutputChunk:
		// Incremental stdout/stderr from a running command (Part 1
		// producer). Coalesce into a buffer keyed by call id and schedule a
		// single throttled flush rather than mutating the live region per
		// chunk — this is the throttle that keeps high-output commands from
		// flooding the renderer. Returns a flush cmd only when no flush is
		// already pending.
		return a, a.bufferToolOutput(ev.CallID, ev.Chunk)

	case agent.EventToolCallExecuted:
		// The authoritative result is about to commit. Drop any unflushed
		// streamed preview for this call so it cannot render twice (the
		// committed result is the single copy).
		a.discardBufferedToolOutput()
		a.conversation.CompleteToolCall(ev.ToolCall.Name, ev.ToolResult)

	case agent.EventToolCallRejected:
		reason := ev.Text
		if reason == "" {
			reason = "user rejected the proposed action"
		}
		a.conversation.CompleteToolCall(ev.ToolCall.Name, tools.ToolResult{Content: reason, IsError: true})
		a.conversation.AppendSystem(fmt.Sprintf("✗ rejected %s", ev.ToolCall.Name))

	case agent.EventUsageUpdate:
		a.refreshTopBar()

	case agent.EventDone:
		// EventDone is the channel-close signal at the agent level. The
		// channel close itself produces agentDoneMsg.

	case agent.EventError:
		// A ctx.Canceled chain (from Ctrl+C) renders as a dim system
		// line reading "turn cancelled" rather than the alarming
		// "error: context canceled" text. Provider errors wrap with
		// %w, so errors.Is walks the whole chain.
		if isCancellation(ev.Error) {
			a.conversation.AppendSystem("turn cancelled")
		} else if ev.Error != nil {
			a.conversation.AppendError(ev.Error.Error())
		} else {
			a.conversation.AppendError("turn failed without an error description")
		}
		a.turnFailed = true
		if a.cancelTurn != nil {
			a.cancelTurn()
			a.cancelTurn = nil
		}
	}
	return a, nil
}

// bufferToolOutput coalesces a streamed tool-output chunk. It appends to
// the per-call buffer and, if no flush is already scheduled, returns a
// throttled flush command. Chunks for a different call id (a new running
// command) reset the buffer first. Returning the tick only when none is
// in flight is what bounds rebuilds to one per toolOutputThrottle window.
func (a *App) bufferToolOutput(callID, chunk string) tea.Cmd {
	if chunk == "" {
		return nil
	}
	if a.toolOutputCallID != "" && callID != "" && a.toolOutputCallID != callID {
		// New running call — flush nothing stale, just start fresh. (The
		// previous call's EventToolCallExecuted already discarded its
		// buffer; this guards the rare interleave.)
		a.toolOutputPending.Reset()
	}
	if callID != "" {
		a.toolOutputCallID = callID
	}
	a.toolOutputPending.WriteString(chunk)
	if a.toolOutputFlushScheduled {
		return nil
	}
	a.toolOutputFlushScheduled = true
	return scheduleToolOutputFlush()
}

// flushToolOutput drains the coalesced buffer into the conversation's live
// region (rebuilding the pending tool block once). If more output has
// accumulated by the next tick the timer re-arms; otherwise it goes idle.
func (a *App) flushToolOutput() tea.Cmd {
	a.toolOutputFlushScheduled = false
	if a.toolOutputPending.Len() == 0 {
		return nil
	}
	chunk := a.toolOutputPending.String()
	a.toolOutputPending.Reset()
	a.conversation.AppendToolOutput(a.toolOutputCallID, chunk)
	return nil
}

// discardBufferedToolOutput drops any unflushed streamed preview, used
// when the authoritative result commits so the preview cannot render in
// addition to the result. The flush timer (if armed) becomes a cheap
// no-op when it fires.
func (a *App) discardBufferedToolOutput() {
	a.toolOutputPending.Reset()
	a.toolOutputCallID = ""
}

// readAgentEvent reads one event from the agent's channel and converts
// it to a tea.Msg. Returns agentDoneMsg when the channel closes.
// Recursive: every time we deliver an event we schedule another read.
func readAgentEvent(stream <-chan agent.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-stream
		if !ok {
			return agentDoneMsg{}
		}
		return agentEventBatch{first: ev, rest: stream}
	}
}

// agentEventBatch is a self-rescheduling cursor over the agent stream.
// When Update receives one, it dispatches `first` and schedules another
// read of `rest`. This keeps the Bubble Tea event loop responsive while
// preserving event order.
type agentEventBatch struct {
	first agent.AgentEvent
	rest  <-chan agent.AgentEvent
}

// Wire agentEventBatch into Update as if it were agentEventMsg, then
// schedule the next read.
func (a *App) reentrantHandle(b agentEventBatch) (tea.Model, tea.Cmd) {
	model, cmd := a.handleAgentEvent(b.first)
	next := readAgentEvent(b.rest)
	if cmd == nil {
		return model, next
	}
	return model, tea.Batch(cmd, next)
}

// injectJobResultForAgent explicitly marks one terminal job result as
// injected and appends it as a user-role context message. The agent's
// next ChatRequest picks it up via buildMessages. We deliberately use
// RoleUser (not RoleSystem) so providers that disallow multi-system
// messages still accept the payload.
func (a *App) injectJobResultForAgent(id string) bool {
	if a.jobs == nil {
		return false
	}
	_, ok, err := a.jobs.InjectResult(id, a.addJobResultToSession)
	return ok && err == nil
}

func (a *App) addJobResultToSession(r jobs.Result) error {
	if a.deps.Sessions == nil {
		return fmt.Errorf("sessions not available")
	}
	return a.deps.Sessions.AddMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: agentResultBody(r),
	})
}

// handleJobUpdate is the UI-side handler for a jobs.Snapshot transition.
// Refreshes the top bar counter and, on terminal states, appends a
// system message summarising the outcome.
func (a *App) handleJobUpdate(snap jobs.Snapshot) (tea.Model, tea.Cmd) {
	if snap.Seq > 0 {
		if a.jobSeqSeen == nil {
			a.jobSeqSeen = map[string]int64{}
		}
		if prev, ok := a.jobSeqSeen[snap.ID]; ok && snap.Seq <= prev {
			return a, nil
		}
		a.jobSeqSeen[snap.ID] = snap.Seq
	}
	a.refreshTopBar()
	if wt := worktreeSummary(snap); wt != "" {
		if a.jobWorktreeSeen == nil {
			a.jobWorktreeSeen = map[string]bool{}
		}
		if !a.jobWorktreeSeen[snap.ID] {
			a.conversation.AppendSystem(fmt.Sprintf("[job:%s worktree] %s", snap.ID, wt))
			a.jobWorktreeSeen[snap.ID] = true
		}
	}
	if snap.State.IsTerminal() {
		if a.jobTerminalSeen == nil {
			a.jobTerminalSeen = map[string]bool{}
		}
		if !a.jobTerminalSeen[snap.ID] {
			a.conversation.AppendSystem(formatTerminalJobLine(snap))
			a.jobTerminalSeen[snap.ID] = true
		}
		if a.jobs != nil {
			a.jobs.MarkResultSeen(snap.ID)
		}
	}
	return a, nil
}

// hasErrorDetail reports whether a snapshot's Error text should be surfaced.
// Abandoned jobs qualify alongside failed ones: the transport or shutdown
// error is preserved on the record and is usually the only evidence the user
// has about why the outcome was never confirmed.
func hasErrorDetail(snap jobs.Snapshot) bool {
	if strings.TrimSpace(snap.Error) == "" {
		return false
	}
	return snap.State == jobs.StateFailed || snap.State == jobs.StateAbandoned
}

// formatTerminalJobLine renders a single-line inline notification for a
// job that has just reached a terminal state. Matches the spec:
//
//	[job:7f3a — done · 12s · local · gemini/2.5-flash · $0.0031]
//	14 call sites in 8 files; …
func formatTerminalJobLine(snap jobs.Snapshot) string {
	// The default is the state's own name, not "done". A hardcoded success
	// default means any state this switch has not been taught about announces
	// itself in the conversation as a completed run.
	label := snap.State.String()
	switch snap.State {
	case jobs.StateCompleted:
		label = "done"
	case jobs.StateAbandoned:
		// The cause is the only thing that distinguishes "the app exited" from
		// "the transport died and a remote agent may still be running", so it
		// belongs in the line the user actually reads.
		if cause := snap.AbandonCause; cause != "" && cause != jobs.AbandonCauseUnknown {
			label += " (" + string(cause) + ")"
		}
	}
	dur := time.Duration(0)
	if !snap.StartedAt.IsZero() && !snap.FinishedAt.IsZero() {
		dur = snap.FinishedAt.Sub(snap.StartedAt)
	}
	prov := snap.Provider
	if snap.Model != "" {
		if prov != "" {
			prov += "/" + snap.Model
		} else {
			prov = snap.Model
		}
	}
	target := "local"
	if snap.ComputerName != "" {
		target = snap.ComputerName
	}
	head := fmt.Sprintf("[job:%s — %s · %s · %s · %s · $%.4f]",
		snap.ID, label, roundedDuration(dur), target, prov, snap.CostUSD)
	body := strings.TrimSpace(snap.Summary)
	if hasErrorDetail(snap) {
		if body != "" {
			body += "\n"
		}
		body += "error: " + snap.Error
	}
	// Every part is gathered before anything decides the line is empty. The
	// early return that used to sit inside this first branch skipped the
	// artifacts block below it, so a job that finished with artifacts but no
	// summary, error or worktree lost the one line naming what it produced --
	// and the pointer to /agents for reading it. formatAgentPeek builds its
	// body the same way, which is why it never had this bug.
	if body == "" {
		body = worktreeSummary(snap)
	} else if wt := worktreeSummary(snap); wt != "" {
		body += "\n" + wt
	}
	if digest := jobs.ArtifactDigest(snap.Artifacts); digest != "" {
		if body != "" {
			body += "\n"
		}
		body += "artifacts: " + digest + " · /agents " + snap.ID
	}
	if body == "" {
		return head
	}
	return head + "\n" + body
}

func formatAgentPeek(snap jobs.Snapshot) string {
	prov := snap.Provider
	if snap.Model != "" {
		if prov != "" {
			prov += "/" + snap.Model
		} else {
			prov = snap.Model
		}
	}
	body := strings.TrimSpace(snap.Summary)
	if hasErrorDetail(snap) {
		if body != "" {
			body += "\n"
		}
		body += "error: " + snap.Error
	}
	if body == "" {
		body = strings.TrimSpace(snap.Prompt)
	}
	if wt := worktreeSummary(snap); wt != "" {
		if body != "" {
			body += "\n"
		}
		body += wt
	}
	if manifest := jobs.ArtifactManifest(snap.Artifacts, 8); manifest != "" {
		if body != "" {
			body += "\n"
		}
		body += "Artifacts:\n" + manifest
	}
	target := "local"
	if snap.ComputerName != "" {
		target = snap.ComputerName
	}
	head := fmt.Sprintf("[agent:%s — %s · %s · %s]", snap.ID, snap.State.String(), target, prov)
	if body == "" {
		return head
	}
	return head + "\n" + body
}

func worktreeSummary(snap jobs.Snapshot) string {
	if snap.WorktreePath != "" {
		parts := []string{"worktree: " + snap.WorktreePath}
		if snap.WorktreeBranch != "" {
			parts = append(parts, "branch "+snap.WorktreeBranch)
		}
		if snap.WorktreeBase != "" {
			parts = append(parts, "base "+snap.WorktreeBase)
		}
		return strings.Join(parts, " · ")
	}
	if snap.AllowWrite && snap.WorktreeNote != "" {
		return "worktree unavailable: " + snap.WorktreeNote
	}
	return ""
}

func agentResultBody(r jobs.Result) string {
	summary := strings.TrimSpace(r.Summary)
	if summary == "" {
		summary = strings.TrimSpace(r.Error)
	}
	if summary == "" {
		summary = strings.TrimSpace(r.Reason)
	}
	if summary == "" {
		summary = "(no summary)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Background job %s handoff]\n", r.JobID)
	fmt.Fprintf(&b, "Outcome: %s\n", r.State.String())
	if r.ComputerName != "" {
		fmt.Fprintf(&b, "Computer: %s (%s)\n", r.ComputerName, r.WorkingDir)
	}
	fmt.Fprintf(&b, "Summary: %s", summary)
	if r.WorktreePath != "" {
		b.WriteString("\n")
		b.WriteString(resultWorktreeSummary(r))
	}
	if manifest := jobs.ArtifactManifest(r.Artifacts, 10); manifest != "" {
		b.WriteString("\nArtifacts:\n")
		b.WriteString(manifest)
	}
	return b.String()
}

func resultWorktreeSummary(r jobs.Result) string {
	if r.WorktreePath == "" {
		return ""
	}
	parts := []string{"worktree: " + r.WorktreePath}
	if r.WorktreeBranch != "" {
		parts = append(parts, "branch "+r.WorktreeBranch)
	}
	if r.WorktreeBase != "" {
		parts = append(parts, "base "+r.WorktreeBase)
	}
	return strings.Join(parts, " · ")
}

// roundedDuration renders a duration as a short "12s" / "1m03s" string
// for the one-line terminal-job notification. We round to the nearest
// second so output doesn't drift between runs.
func roundedDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}

// handleSlashCommand dispatches a parsed slash command from the input
// line. Returns the tea.Model / tea.Cmd pair so the caller can thread
// it back through Update.
//
// The jobs-manager guard is per-handler (each of the three jobs verbs
// checks `a.jobs == nil` itself) so the new non-jobs verbs still work
// when background agents are disabled.
func (a *App) handleSlashCommand(cmd string, args []string, original string) (tea.Model, tea.Cmd) {
	a.input.Reset()

	switch cmd {
	case "spawn":
		return a.handleSpawnCommand(args)
	case "agents":
		return a.handleAgentsCommand(args)
	case "jobs":
		return a.handleJobsCommand(args)
	case "computers":
		return a.handleComputersCommand(args)
	case "cancel":
		return a.handleCancelCommand(args)
	case "provider", "providers":
		return a.handleProviderCommand(args)
	case "model", "models":
		return a.handleModelCommand(args)
	case "effort":
		return a.handleEffortCommand(args)
	case "sessions":
		return a.handleSessionsCommand(args)
	case "skills":
		return a.handleSkillsCommand(args)
	case "resume":
		return a.handleResumeCommand(args)
	case "queue":
		return a.handleQueueCommand(args)
	case "undo":
		return a.handleUndoCommand(args)
	case "compact":
		return a.handleCompactCommand(args)
	case "cost":
		return a.handleCostCommand(args)
	case "trust":
		return a.handleTrustCommand(args)
	case "permissions":
		return a.handlePermissionsCommand(args)
	case "help":
		return a.handleHelpCommand(args)
	case "clear":
		return a.handleClearCommand(args)
	case "statusline":
		return a.handleStatusLineCommand(args)
	case "mcp":
		return a.handleMCPCommand(args)
	case "ollama":
		return a.handleOllamaCommand(args)
	case "plan":
		return a.handlePlanCommand(args)
	case "loop":
		return a.handleLoopCommand(args)
	case "workflows", "workflow":
		return a.handleWorkflowCommand(args)
	case "transcript":
		return a.handleTranscriptCommand(args)
	case "exit", "quit":
		return a, tea.Quit
	}
	if custom, ok := a.slashRegistry().Lookup(cmd); ok && !custom.Builtin {
		return a.startCustomCommand(custom, original, cmd)
	}
	a.conversation.AppendSystem(unknownSlashCommandMessage(original))
	return a, nil
}

// startCustomCommand runs a markdown command or a skill.
//
// The two differ in one way that matters and one that only looks like it.
//
// What matters: a markdown command body is a prompt the user wrote, so it is
// their text and is treated as such -- @-mentions in it resolve, and the
// expansion is what the transcript shows, because seeing what was actually
// sent is the point of writing one. A skill body is neither theirs nor short.
// It is up to MaxBodyBytes, and for a project skill it is repository content
// carrying an untrusted label that @-mention expansion would hoist files above.
// So the body is marked authored-elsewhere, and only the arguments the user
// actually typed are scanned for mentions.
//
// What only looks like it: both accept arguments. The user typed them either
// way and they must reach the turn either way.
func (a *App) startCustomCommand(custom SlashCommand, original, cmd string) (tea.Model, tea.Cmd) {
	args := slashCommandArguments(original, cmd)
	opt := turnOptions{emitUser: true}
	if custom.Skill {
		opt.authored = true
		// Mentions resolve over the arguments alone. expandFileMentions
		// prepends what it finds, so the files land between the closing
		// </skill> marker and the user's words -- outside the untrusted label,
		// in the position the user's own message occupies.
		if !a.deps.RemoteWorkspace {
			expandedArgs, files := expandFileMentions(args, a.deps.WorkingDir)
			if len(files) > 0 {
				args = expandedArgs
				opt.attached = files
			}
		}
		opt.display = strings.TrimSpace(original)
	}
	opt.text = custom.Expand(args)
	if !custom.Skill {
		opt.display = opt.text
	}
	if custom.Skill {
		// Carry consent with its turn through queueing and auto-compaction.
		// Applying it here would widen an unrelated turn already running.
		if s, ok := a.skillsRegistry().Lookup(custom.Name); ok {
			opt.skillGrant = &s
		}
	}
	if a.streaming {
		a.queueTurn(opt)
		return a, nil
	}
	return a.startTurnWith(opt)
}

func (a *App) slashRegistry() *SlashCommandRegistry {
	if a == nil || a.slashCommands == nil {
		return NewBuiltinSlashRegistry()
	}
	return a.slashCommands
}

func (a *App) slashHelpRows() []KeyHelp {
	return a.slashRegistry().HelpRows()
}

func unknownSlashCommandMessage(text string) string {
	name, _, ok := parseSlashCommandFields(text)
	if !ok || name == "" {
		return `empty slash command; type // to send a prompt that starts with "/"`
	}
	return fmt.Sprintf("unknown slash command /%s; type //%s to send it as a prompt", name, name)
}

func (a *App) handleSpawnCommand(args []string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("spawn: background jobs are disabled (no jobs.Manager wired)")
		return a, nil
	}
	opts, err := ParseSpawnOptions(args)
	if err != nil {
		a.conversation.AppendSystem("spawn: " + err.Error())
		return a, nil
	}
	snap, spawnErr := a.jobs.Spawn(jobs.SpawnRequest{
		Prompt:      opts.Prompt,
		Provider:    opts.Provider,
		Model:       opts.Model,
		Computer:    opts.Computer,
		ParentJobID: "",
		ParentDepth: 0,
		AllowWrite:  opts.AllowWrite,
	})
	if spawnErr != nil {
		a.conversation.AppendSystem(fmt.Sprintf("spawn failed: %s", spawnErr.Error()))
		return a, nil
	}
	prov := snap.Provider
	if snap.Model != "" {
		if prov != "" {
			prov += "/" + snap.Model
		} else {
			prov = snap.Model
		}
	}
	mode := "read-only"
	if opts.AllowWrite {
		mode = "write · worktree pending"
	}
	target := "local"
	if snap.ComputerName != "" {
		target = snap.ComputerName
	}
	a.conversation.AppendSystem(fmt.Sprintf("[job:%s queued — %s · %s · %s] %s", snap.ID, prov, target, mode, snap.Prompt))
	// Reflect the new job on the top bar immediately. The Subscribe
	// fanout will do this too, but asynchronously on a goroutine —
	// bumping the counter here is synchronous and matches the user's
	// mental model (they typed the command, they see the counter).
	a.refreshTopBar()
	return a, nil
}

func (a *App) handleJobsCommand(args []string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("jobs: background jobs are disabled (no jobs.Manager wired)")
		return a, nil
	}
	if len(args) == 0 {
		a.conversation.AppendSystem(renderJobsTable(a.jobs.List()))
		return a, nil
	}
	if args[0] == "resubmit" {
		return a.handleJobsResubmit(args[1:])
	}
	return a.openJobTranscript(args[0], "job")
}

// handleJobsResubmit re-runs a job abandoned by a previous app exit. The
// original is never resumed — it keeps its cancelled state and evidence,
// and a separate job is spawned from its saved prompt.
func (a *App) handleJobsResubmit(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 {
		pending := a.jobs.RecoveredResubmittable()
		if len(pending) == 0 {
			a.conversation.AppendSystem("jobs resubmit: no recovered jobs are waiting to be resubmitted")
			return a, nil
		}
		var b strings.Builder
		b.WriteString("jobs resubmit: usage /jobs resubmit <id>\nrecovered jobs available to re-run:\n")
		for _, s := range pending {
			fmt.Fprintf(&b, "  /jobs resubmit %s — %s\n", s.ID, truncOneLine(s.Prompt, 60))
		}
		b.WriteString("Inspect /jobs <id> before rerunning: previous work may have changed files or external services.\n")
		a.conversation.AppendSystem(strings.TrimRight(b.String(), "\n"))
		return a, nil
	}
	if len(args) != 1 {
		a.conversation.AppendSystem("jobs resubmit: usage /jobs resubmit <id>; exactly one job can be resubmitted at a time")
		return a, nil
	}
	snap, err := a.jobs.Resubmit(args[0])
	if err != nil {
		a.conversation.AppendSystem("jobs resubmit: " + jobResubmitError(err))
		return a, nil
	}
	a.conversation.AppendSystem(fmt.Sprintf(
		"jobs resubmit: started %s as a new run from %s's saved prompt — %s was not resumed and keeps its own record",
		snap.ID, snap.ResubmitOf, snap.ResubmitOf,
	))
	return a, nil
}

func (a *App) handleAgentsCommand(args []string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	if len(args) == 0 {
		a.showAgentView()
		return a, nil
	}
	return a.openJobTranscript(args[0], "agent")
}

func (a *App) showAgentView() {
	if a.jobs == nil {
		return
	}
	a.agentDispatchFocused = false
	a.input.Reset()
	a.input.Blur()
	a.agentView.Show(a.jobs.List())
}

func (a *App) handleAgentPeek(id string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	snap, ok := a.jobs.Get(id)
	if !ok {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s not found]", id))
		return a, nil
	}
	a.conversation.AppendSystem(formatAgentPeek(snap))
	return a, nil
}

func (a *App) handleAgentCancel(id string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	if a.jobs.Cancel(id) {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s — cancellation requested]", id))
	} else {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s not found or already terminal]", id))
	}
	a.refreshTopBar()
	a.agentView.SetJobs(a.jobs.List())
	return a, nil
}

func (a *App) handleAgentInject(id string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	snap, ok := a.jobs.Get(id)
	if !ok {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s not found]", id))
		return a, nil
	}
	if !snap.State.IsTerminal() {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s is %s; wait for completion before injecting]", id, snap.State.String()))
		return a, nil
	}
	if a.deps.Sessions == nil {
		a.conversation.AppendSystem("agents: sessions not available")
		return a, nil
	}
	if !a.injectJobResultForAgent(id) {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s result not available]", id))
		return a, nil
	}
	a.agentView.SetJobs(a.jobs.List())
	a.conversation.AppendSystem(fmt.Sprintf("[agent:%s injected into next turn]", id))
	return a, nil
}

func (a *App) handleAgentIgnore(id string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	snap, ok := a.jobs.Get(id)
	if !ok {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s not found]", id))
		return a, nil
	}
	if !snap.State.IsTerminal() {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s is %s; wait for completion before ignoring]", id, snap.State.String()))
		return a, nil
	}
	if _, ok := a.jobs.MarkResultIgnored(id); !ok {
		a.conversation.AppendSystem(fmt.Sprintf("[agent:%s result not available]", id))
		return a, nil
	}
	a.agentView.SetJobs(a.jobs.List())
	a.conversation.AppendSystem(fmt.Sprintf("[agent:%s ignored]", id))
	return a, nil
}

func (a *App) openJobTranscript(id, label string) (tea.Model, tea.Cmd) {
	snap, ok := a.jobs.Get(id)
	if !ok {
		a.conversation.AppendSystem(fmt.Sprintf("[%s:%s not found]", label, id))
		return a, nil
	}
	transcript, _ := a.jobs.Transcript(id)
	// /jobs <id> and /agents <id> open the transcript modal. The
	// underlying component is still jobs-oriented because background
	// agents are represented by jobs.Manager snapshots.
	a.jobsPanel.Show(snap, transcript)
	return a, nil
}

func (a *App) handleTranscriptCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) > 0 {
		a.conversation.AppendSystem("transcript: unexpected argument " + args[0])
		return a, nil
	}
	if a.deps.Sessions == nil {
		a.conversation.AppendSystem("transcript: sessions not available")
		return a, nil
	}
	cur := a.deps.Sessions.Current()
	if cur == nil {
		a.conversation.AppendSystem("transcript: no current session")
		return a, nil
	}
	title := fmt.Sprintf("[session:%s]", shortID(cur.ID))
	prov := cur.Provider
	if cur.Model != "" {
		if prov != "" {
			prov += "/" + cur.Model
		} else {
			prov = cur.Model
		}
	}
	meta := fmt.Sprintf("%s · %d messages · $%.4f", prov, len(cur.Messages), cur.Cost.TotalUSD)
	a.jobsPanel.ShowSession(title, meta, cur.Messages)
	return a, nil
}

func (a *App) handleCancelCommand(args []string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("cancel: background jobs are disabled (no jobs.Manager wired)")
		return a, nil
	}
	if len(args) == 0 {
		a.conversation.AppendSystem("cancel: missing job id (or 'all')")
		return a, nil
	}
	target := args[0]
	if target == "all" {
		n := a.jobs.CancelAll()
		a.conversation.AppendSystem(fmt.Sprintf("[cancelled %d jobs]", n))
		a.refreshTopBar()
		if a.agentView.Visible() {
			a.agentView.SetJobs(a.jobs.List())
		}
		return a, nil
	}
	if a.jobs.Cancel(target) {
		a.conversation.AppendSystem(fmt.Sprintf("[job:%s — cancellation requested]", target))
	} else {
		a.conversation.AppendSystem(fmt.Sprintf("[job:%s not found or already terminal]", target))
	}
	a.refreshTopBar()
	return a, nil
}

// renderJobsTable returns a monospace ASCII table of snapshots.
// Newest-first; prompt truncated to 50 chars.
func renderJobsTable(snaps []jobs.Snapshot) string {
	if len(snaps) == 0 {
		return "no background jobs"
	}
	// jobs.Manager.List() already sorts newest-first; still, be defensive
	// if a subset is passed in.
	sort.SliceStable(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.After(snaps[j].CreatedAt)
	})
	var b strings.Builder
	b.WriteString("ID       STATE      TARGET       ROOT      PROV/MODEL              AGE    TOK(IN/OUT)  PROMPT\n")
	now := time.Now()
	for _, s := range snaps {
		prov := s.Provider
		if s.Model != "" {
			if prov != "" {
				prov += "/" + s.Model
			} else {
				prov = s.Model
			}
		}
		age := roundedDuration(now.Sub(s.CreatedAt))
		tok := fmt.Sprintf("%d/%d", s.Tokens.Input, s.Tokens.Output)
		// truncOneLine, not a byte slice: a prompt is arbitrary user text, so
		// it can carry multi-byte runes (which byte slicing would cut in half)
		// and newlines (which would break the row out of the table).
		prompt := truncOneLine(s.Prompt, 50)
		rootMode := "main"
		if s.WorktreePath != "" {
			rootMode = "worktree"
		} else if s.AllowWrite {
			if s.State.IsTerminal() {
				// Abandoned counts as failed here, not "none". "none" claims
				// the root was released cleanly, and an unconfirmed outcome is
				// exactly the case where that claim cannot be made.
				switch s.State {
				case jobs.StateFailed, jobs.StateAbandoned:
					rootMode = "failed"
				default:
					rootMode = "none"
				}
			} else {
				rootMode = "pending"
			}
		}
		target := "local"
		if s.ComputerName != "" {
			target = s.ComputerName
		}
		fmt.Fprintf(&b, "%-8s %-10s %-12s %-9s %-23s %-6s %-12s %s\n",
			s.ID, trunc(s.State.String(), 10), trunc(target, 12), trunc(rootMode, 9), trunc(prov, 23), age, trunc(tok, 12), prompt)
		if wt := worktreeSummary(s); wt != "" {
			fmt.Fprintf(&b, "      %s\n", wt)
		}
		if digest := jobs.ArtifactDigest(s.Artifacts); digest != "" {
			fmt.Fprintf(&b, "      artifacts: %s\n", digest)
		}
		if line := reconcileSummary(s); line != "" {
			fmt.Fprintf(&b, "      %s\n", line)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// reconcileSummary describes a job's abandoned/resubmitted lineage. It is
// deliberately explicit that nothing resumed: the previous process exited
// and the work was never continued, only re-run as a separate job.
func reconcileSummary(s jobs.Snapshot) string {
	recoveredState := "abandoned at previous app exit"
	if s.State == jobs.StateCancelled {
		recoveredState = "cancelled before starting at previous app exit"
	}
	switch {
	case s.ResubmitOf != "":
		return fmt.Sprintf("resubmitted from recovered job %s (new run, not a resumption)", s.ResubmitOf)
	case s.Recovered && s.ResubmittedAs != "":
		return fmt.Sprintf("%s; resubmitted as %s", recoveredState, s.ResubmittedAs)
	case s.Recovered:
		if s.Prompt == "" {
			return recoveredState + "; no saved prompt to resubmit; inspect /jobs " + s.ID
		}
		if len(s.Prompt) > jobs.MaxResubmitPromptBytes {
			return recoveredState + "; saved prompt is too large to resubmit; inspect /jobs " + s.ID + " and start a new job manually"
		}
		return fmt.Sprintf("%s; inspect /jobs %s before /jobs resubmit %s starts a new run", recoveredState, s.ID, s.ID)
	}
	return ""
}

// trunc returns s truncated to n runes (not bytes), adding nothing — just
// clips. Used for table-cell formatting.
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// openProviderPicker constructs the provider picker synchronously
// (Registry.List is in-memory) and returns the tea.Cmd produced by
// Open. Appends a system message and returns nil if no providers are
// registered — we never want to show an empty modal.
func (a *App) openProviderPicker() tea.Cmd {
	a.autocomplete.Close()
	provs := a.pickerProviders()
	if len(provs) == 0 {
		a.conversation.AppendSystem("provider picker: no providers configured")
		return nil
	}
	activeSlug := ""
	if p, _ := a.deps.Registry.Active(); p != nil {
		activeSlug = p.Slug()
	}
	a.picker = picker.New("provider", "Select provider")
	a.picker.Resize(a.width, a.height)
	a.picker.SetItems(providerItems(provs, a.deps.Config, activeSlug))
	a.picker.SetActive(activeSlug)
	return a.picker.Open(nil)
}

// pickerProviders returns every provider the picker should show:
// registered ones first, then any factory-known provider the user
// hasn't configured yet (as a placeholder constructed with an empty
// key) so the ctrl+a setup flow can still target it.
func (a *App) pickerProviders() []provider.Provider {
	registered := a.deps.Registry.List()
	seen := make(map[string]struct{}, len(registered))
	for _, p := range registered {
		seen[p.Slug()] = struct{}{}
	}
	out := append([]provider.Provider{}, registered...)
	if a.deps.Factories == nil {
		return out
	}
	for _, slug := range a.factoryDisplaySlugs(seen) {
		if _, ok := seen[slug]; ok {
			continue
		}
		if factory, ok := a.deps.Factories[slug]; ok {
			out = append(out, factory(""))
		}
	}
	return out
}

// knownProviderDisplayOrder is the fixed presentation order for the providers
// packetcode ships with. It is the single source of truth for "is this slug a
// built-in?": listing a slug here but omitting it from the membership test
// would show that provider twice in the picker.
var knownProviderDisplayOrder = []string{
	"sugar", "openai", "codex", "anthropic", "gemini",
	"minimax", "deepseek", "grok", "mistral", "openrouter", "ollama",
}

func isKnownProviderSlug(slug string) bool {
	for _, known := range knownProviderDisplayOrder {
		if known == slug {
			return true
		}
	}
	return false
}

func (a *App) factoryDisplaySlugs(seen map[string]struct{}) []string {
	if a.deps.Factories == nil {
		return nil
	}
	var out []string
	for _, slug := range knownProviderDisplayOrder {
		if _, exists := a.deps.Factories[slug]; exists {
			out = append(out, slug)
		}
	}
	var customSlugs []string
	for slug := range a.deps.Factories {
		if _, alreadyListed := seen[slug]; alreadyListed {
			continue
		}
		if isKnownProviderSlug(slug) {
			continue
		}
		customSlugs = append(customSlugs, slug)
	}
	sort.Strings(customSlugs)
	return append(out, customSlugs...)
}

// openModelPicker constructs the model picker. When Registry has a
// fresh cache for the active provider the modal opens synchronously;
// otherwise it opens in the loading state and the returned tea.Cmd
// fires a ListModels on a background goroutine, warming the cache on
// success.
func (a *App) openModelPicker() tea.Cmd {
	a.autocomplete.Close()
	prov, active := a.deps.Registry.Active()
	if prov == nil {
		a.conversation.AppendSystem("model picker: no active provider")
		return nil
	}
	a.picker = picker.New("model", fmt.Sprintf("Select model — %s", prov.Name()))
	a.picker.Resize(a.width, a.height)
	if cached, ok := a.deps.Registry.CachedModels(prov.Slug()); ok {
		a.picker.SetItems(modelItems(cached, active, prov))
		a.picker.SetActive(active)
		return a.picker.Open(nil)
	}
	slug := prov.Slug()
	loader := func(ctx context.Context) ([]picker.Item, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		models, err := prov.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		a.deps.Registry.SetCachedModels(slug, models)
		return modelItems(models, active, prov), nil
	}
	return a.picker.Open(loader)
}

func tickTopbar() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickTopbarMsg{}
	})
}

func pollApproverFallback() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return approvalPendingMsg{}
	})
}

// agentLoopDetection translates the behaviour config into the agent's loop
// settings. A nil config keeps the defaults, which is the same thing an
// unconfigured install gets.
func agentLoopDetection(cfg *config.Config) agent.LoopDetectionConfig {
	if cfg == nil {
		return agent.LoopDetectionConfig{}
	}
	return agent.LoopDetectionSettings(
		cfg.Behavior.LoopDetectionDisabled,
		cfg.Behavior.LoopDetectionWindow,
		cfg.Behavior.LoopDetectionThreshold,
	)
}
