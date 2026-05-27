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
	"github.com/packetcode/packetcode/internal/ui/layout"
)

// agentEventMsg wraps a single agent.AgentEvent so we can route it
// through the Bubble Tea Update loop.
type agentEventMsg struct{ ev agent.AgentEvent }

// agentDoneMsg signals the agent's event channel has closed.
type agentDoneMsg struct{}

// pollApproverMsg fires periodically so the App can pick up pending
// approval requests posted by the agent goroutine.
type pollApproverMsg struct{}

// tickTopbarMsg updates the duration counter in the top bar.
type tickTopbarMsg struct{}

type statusLineMsg struct {
	seq    int
	line   string
	err    error
	manual bool
}

type queuedInput struct {
	Text string
	At   time.Time
}

// jobUpdateMsg is dispatched from the jobs.Manager Subscribe callback
// (which runs in its own goroutine) into the Bubble Tea Update loop via
// tea.Program.Send. The App uses it to refresh the top bar and, on
// terminal transitions, append a system message describing the outcome.
type jobUpdateMsg struct{ Snap jobs.Snapshot }

// Deps bundles everything App needs from main(). main() owns the lifecycle
// of these objects; App just borrows them.
type Deps struct {
	Config           *config.Config
	Registry         *provider.Registry
	Tools            *tools.Registry
	Sessions         *session.Manager
	CostTracker      *cost.Tracker
	Jobs             *jobs.Manager
	Backups          *session.BackupManager
	MCP              *mcp.Manager
	PermissionPolicy *permissions.Policy
	WorkingDir       string
	SystemPrompt     string
	Hooks            *hooks.Runner
	Version          string // shown on the welcome splash; e.g. "v1" or "v0.1.0"
	ResumeHydrate    bool   // render the current session transcript at startup

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
	picker        picker.Model
	prompt        prompt.Model
	spinner       spinner.Model
	autocomplete  autocomplete.Model
	slashCommands *SlashCommandRegistry

	// Agent + bridge.
	agent            *agent.Agent
	approver         *uiApprover
	permissionPolicy *permissions.Policy
	permissionBase   *permissions.Policy
	preTrustPolicy   *permissions.Policy

	// Background-agents manager. Non-nil when deps.Jobs is set. All
	// job-related UI code paths guard on `a.jobs != nil`.
	jobs *jobs.Manager

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
	err       string

	// cancelTurn cancels the in-flight agent.Run context for the current
	// streaming turn. Set in startTurn, cleared in agentDoneMsg / on
	// EventError / on Ctrl+C. A non-nil cancelTurn plus streaming==true
	// means "turn is live"; cancelTurn==nil plus streaming==true means
	// "cancel requested, waiting for goroutine drain" — in that window a
	// second Ctrl+C is a no-op (not a quit). Single-writer from Update.
	cancelTurn         context.CancelFunc
	startedAt          time.Time
	operationLabel     string
	operationStarted   time.Time
	queuedInputs       []queuedInput
	statusSeq          int
	statusLineInFlight int
	statusLineLastRun  time.Time
	lastStatusLineErr  error
	jobSeqSeen         map[string]int64
	jobTerminalSeen    map[string]bool
	jobWorktreeSeen    map[string]bool

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

	a := agent.New(agent.Config{
		Registry:     deps.Registry,
		Tools:        deps.Tools,
		Session:      deps.Sessions,
		CostTracker:  deps.CostTracker,
		Approver:     approver,
		Policy:       policy,
		SystemPrompt: deps.SystemPrompt,
		Hooks:        deps.Hooks,
	})

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
	if deps.Config != nil {
		statusRunner = statusline.New(deps.Config.StatusLine, deps.WorkingDir)
	}

	slashCommands := LoadSlashRegistry(deps.WorkingDir)
	app := &App{
		deps:             deps,
		topbar:           topbar.New(),
		conversation:     conv,
		input:            input.New(),
		approval:         approval.New(),
		jobsPanel:        jobs_ui.New(),
		agentView:        agentview.New(),
		picker:           picker.New("", ""),
		prompt:           prompt.New(""),
		spinner:          spinner.New(),
		autocomplete:     autocomplete.New(buildAutocompleteEntries(slashCommands.HelpRows())),
		slashCommands:    slashCommands,
		agent:            a,
		approver:         approver,
		permissionPolicy: policy,
		permissionBase:   basePolicy,
		jobs:             deps.Jobs,
		backups:          deps.Backups,
		mcp:              deps.MCP,
		contextMgr:       ctxMgr,
		statusLine:       statusRunner,
		startedAt:        time.Now(),
		jobSeqSeen:       map[string]int64{},
		jobTerminalSeen:  map[string]bool{},
		jobWorktreeSeen:  map[string]bool{},
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

	app.refreshTopBar()
	if deps.ResumeHydrate {
		if cur := deps.Sessions.Current(); cur != nil {
			app.showResumedSession(cur)
		}
	}
	return app, nil
}

// SetSendFunc wires the tea.Program.Send bridge. Host (main.go) calls
// this between tea.NewProgram and prog.Run so off-thread callbacks (the
// jobs.Manager subscriber) can post messages into the Update loop.
func (a *App) SetSendFunc(fn func(tea.Msg)) {
	a.sendMsg = fn
}

// Approver returns the App's uiApprover so the host can inject it as the
// jobs.Manager parent approver. Hidden behind the agent.Approver
// interface because that's what jobs.Manager wants.
func (a *App) Approver() agent.Approver {
	return a.approver
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		pollApprover(),
		tickTopbar(),
		a.renderStatusLine(false),
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
	cmds := make([]tea.Cmd, 0, len(drained)+1)
	for _, s := range drained {
		cmds = append(cmds, tea.Println(s))
	}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return model, tea.Batch(cmds...)
}

func (a *App) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.resize(msg.Width, msg.Height)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(msg)

	case input.SubmitMsg:
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
		return a.handleJobUpdate(msg.Snap)

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
		return a.startNextQueuedInput()

	case compactDoneMsg:
		return a.handleCompactDone(msg)

	case approval.ResultMsg:
		switch msg.Result {
		case approval.Approved:
			a.approver.Resolve(agent.ApprovalDecision{Approved: true})
		case approval.Rejected:
			a.approver.Resolve(agent.ApprovalDecision{Approved: false, Reason: "user rejected"})
		}
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

	case providerKeyValidatedMsg:
		return a.handleProviderKeyValidated(msg)

	case pollApproverMsg:
		if a.approval.Visible() {
			a.approval.SetQueueDepth(a.approver.QueueDepth())
			return a, pollApprover()
		}
		if req, ok := a.approver.Pending(); ok {
			a.approval.Show(req.Tool, req.ToolCall)
			a.approval.SetWidth(a.width)
			a.approval.SetQueueDepth(a.approver.QueueDepth())
		}
		return a, pollApprover()

	case tickTopbarMsg:
		a.refreshTopBar()
		return a, tea.Batch(tickTopbar(), a.renderStatusLine(false))

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
				return a, a.acceptAutocomplete(verb)
			}
			return a, nil
		case "enter":
			verb := a.autocomplete.SelectedVerb()
			text := a.input.Value()
			bufferIsBareVerb := strings.HasPrefix(text, "/") &&
				!strings.ContainsAny(text, " \t\n")
			if verb != "" && bufferIsBareVerb {
				return a, a.acceptAutocomplete(verb)
			}
			// Fall through to the input's SubmitMsg path (no matches,
			// or buffer already contains args — let the user send it).
		case "up", "down", "ctrl+n", "ctrl+p", "ctrl+k", "ctrl+j":
			var cmd tea.Cmd
			a.autocomplete, cmd = a.autocomplete.Update(msg)
			return a, cmd
		}
	}

	switch msg.String() {
	case "ctrl+p":
		// Refuse to stack on approval (more urgent) or an existing picker.
		// Ctrl+P DOES open over the jobs panel — picker's higher
		// precedence masks it until dismissed.
		if a.approval.Visible() || a.picker.Visible() {
			return a, nil
		}
		return a, a.openProviderPicker()
	case "ctrl+m":
		if a.approval.Visible() || a.picker.Visible() {
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
			if a.cancelTurn != nil {
				a.cancelTurn()
				a.cancelTurn = nil
			}
			a.spinner.Stop()
			a.markOperationCancelling()
			if a.approval.Visible() {
				a.approval.Hide()
				a.approver.Resolve(agent.ApprovalDecision{Approved: false, Reason: "cancelled"})
			}
			a.clearQueuedInputs()
			return a, nil
		}
		if a.prompt.Visible() {
			a.prompt.Hide()
			return a, nil
		}
		if a.approval.Visible() {
			a.approval.Hide()
			a.approver.Resolve(agent.ApprovalDecision{Approved: false, Reason: "cancelled"})
			return a, nil
		}
		return a, tea.Quit
	case "ctrl+l":
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
		var cmd tea.Cmd
		a.agentView, cmd = a.agentView.Update(msg)
		return a, cmd
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
	if a.approval.Visible() || a.picker.Visible() || a.jobsPanel.Visible() || a.agentView.Visible() {
		a.autocomplete.Close()
		return
	}
	text := a.input.Value()
	if !strings.HasPrefix(text, "/") {
		a.autocomplete.Close()
		return
	}
	if strings.ContainsAny(text, " \t\n") {
		a.autocomplete.Close()
		return
	}
	filter := strings.TrimPrefix(text, "/")
	a.autocomplete.SetWidth(a.width)
	if a.autocomplete.Visible() {
		a.autocomplete.SetFilter(filter)
	} else {
		a.autocomplete.Open(filter)
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

func (a *App) View() string {
	if a.width <= 0 || a.height <= 0 {
		return ""
	}

	// Inline rendering: finalised messages live in the terminal's native
	// scrollback (committed via tea.Println on DrainEmits). The View()
	// return is only the live region that redraws at the bottom of the
	// terminal: pending streaming content, any overlay modal, the
	// autocomplete popup, input, and topbar.
	status := a.topbar.View()
	in := a.input.View()
	pending := a.conversation.PendingView()

	overlay := ""
	if a.prompt.Visible() {
		overlay = a.prompt.View()
	} else if a.approval.Visible() {
		overlay = a.approval.View()
	} else if a.picker.Visible() {
		overlay = a.picker.View()
	} else if a.jobsPanel.Visible() {
		overlay = a.jobsPanel.View()
	} else if a.agentView.Visible() {
		overlay = a.agentView.View()
	} else if a.spinner.Active() {
		overlay = a.spinner.View()
	}

	aboveInput := ""
	if overlay == "" && a.autocomplete.Visible() {
		aboveInput = a.autocomplete.View()
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
	a.picker.Resize(w, h)
	a.prompt.Resize(w, h)
	a.autocomplete.SetWidth(w)
	a.conversation.Resize(w, h)
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
			used = cur.TokenUsage.TotalInput
		}
		a.topbar.SetContext(used, ctxMax)
	}

	root := a.deps.WorkingDir
	a.topbar.SetProject(filepath.Base(root), git.Branch(root))

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
	if a.permissionPolicy != nil {
		a.topbar.SetPermissionProfile(permissions.ProfileConfigName(a.permissionPolicy.Profile()))
	} else {
		a.topbar.SetPermissionProfile("")
	}
	a.topbar.SetOperation(a.streaming, a.operationLabel, a.operationStarted, len(a.queuedInputs))
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
	branch := git.Branch(root)
	var sessionID string
	var used int
	if cur := a.deps.Sessions.Current(); cur != nil {
		sessionID = cur.ID
		used = cur.TokenUsage.TotalInput
	}
	var provSlug, provName, modelID string
	var max int
	if prov, activeModel := a.deps.Registry.Active(); prov != nil {
		provSlug = prov.Slug()
		provName = prov.Name()
		modelID = activeModel
		max = prov.ContextWindow(activeModel)
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
		SessionID:     sessionID,
		WorkingDir:    root,
		Project:       project,
		GitBranch:     branch,
		Provider:      statusline.ProviderInfo{Slug: provSlug, DisplayName: provName},
		Model:         statusline.ModelInfo{ID: modelID},
		ContextWindow: statusline.ContextInfo{Used: used, Max: max, UsedPercentage: pct},
		Cost:          statusline.CostInfo{TotalCostUSD: totalCost},
		Jobs:          statusline.JobsInfo{Active: activeJobs},
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

func (a *App) startTurn(text string, emitUser bool) (tea.Model, tea.Cmd) {
	a.input.Reset()
	if emitUser {
		a.conversation.AppendUser(text)
	}
	a.streaming = true
	a.setOperation("thinking")

	// The ctx is cancellable so Ctrl+C can tear down the in-flight
	// provider HTTP request, kill any running tool, and unblock any
	// pending approval prompt. The CancelFunc is stashed on App so the
	// key handler and EventError / agentDoneMsg paths can reach it.
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelTurn = cancel
	stream := a.agent.Run(ctx, text)

	return a, tea.Batch(a.spinner.Start("Thinking..."), readAgentEvent(stream))
}

func (a *App) queueInput(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		a.input.Reset()
		return
	}
	a.input.Reset()
	a.queuedInputs = append(a.queuedInputs, queuedInput{Text: text, At: time.Now()})
	a.conversation.AppendQueuedUser(text)
	a.refreshTopBar()
}

func (a *App) clearQueuedInputs() int {
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
	if len(a.queuedInputs) == 0 {
		a.refreshTopBar()
		return a, nil
	}
	next := a.queuedInputs[0]
	copy(a.queuedInputs, a.queuedInputs[1:])
	a.queuedInputs = a.queuedInputs[:len(a.queuedInputs)-1]
	return a.startTurn(next.Text, false)
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
		if !a.spinner.Active() {
			// First token arrived → silence the spinner.
			a.spinner.Stop()
		}
		a.conversation.AppendAgentText(modelID, providerSlug, ev.Text)

	case agent.EventToolCallProposed:
		a.conversation.AppendToolCall(ev.ToolCall.Name, ev.ToolCall.Arguments)

	case agent.EventToolCallExecuted:
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
		} else {
			a.conversation.AppendSystem("error: " + ev.Error.Error())
		}
		if a.cancelTurn != nil {
			a.cancelTurn()
			a.cancelTurn = nil
		}
	}
	return a, nil
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

// injectPendingJobResults is the legacy drain path retained for
// migration safety. New UI flows should call injectJobResultForAgent
// with an explicit job id so background summaries do not silently enter
// the next model turn.
func (a *App) injectPendingJobResults() {
	if a.jobs == nil {
		return
	}
	results := a.jobs.DrainResults(32)
	if len(results) == 0 {
		return
	}
	for _, r := range results {
		_ = a.addJobResultToSession(r)
	}
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

// formatTerminalJobLine renders a single-line inline notification for a
// job that has just reached a terminal state. Matches the spec:
//
//	[job:7f3a — done · 12s · gemini/2.5-flash · $0.0031]
//	14 call sites in 8 files; …
func formatTerminalJobLine(snap jobs.Snapshot) string {
	label := "done"
	switch snap.State {
	case jobs.StateFailed:
		label = "failed"
	case jobs.StateCancelled:
		label = "cancelled"
	case jobs.StateCompleted:
		label = "done"
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
	head := fmt.Sprintf("[job:%s — %s · %s · %s · $%.4f]",
		snap.ID, label, roundedDuration(dur), prov, snap.CostUSD)
	body := strings.TrimSpace(snap.Summary)
	if snap.State == jobs.StateFailed && snap.Error != "" {
		if body != "" {
			body += "\n"
		}
		body += "error: " + snap.Error
	}
	if body == "" {
		body = worktreeSummary(snap)
		if body == "" {
			return head
		}
	} else if wt := worktreeSummary(snap); wt != "" {
		body += "\n" + wt
	}
	if digest := jobs.ArtifactDigest(snap.Artifacts); digest != "" {
		body += "\nartifacts: " + digest + " · /agents " + snap.ID
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
	if snap.State == jobs.StateFailed && snap.Error != "" {
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
	head := fmt.Sprintf("[agent:%s — %s · %s]", snap.ID, snap.State.String(), prov)
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
	case "cancel":
		return a.handleCancelCommand(args)
	case "provider", "providers":
		return a.handleProviderCommand(args)
	case "model", "models":
		return a.handleModelCommand(args)
	case "sessions":
		return a.handleSessionsCommand(args)
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
	case "transcript":
		return a.handleTranscriptCommand(args)
	case "exit", "quit":
		return a, tea.Quit
	}
	if custom, ok := a.slashRegistry().Lookup(cmd); ok && !custom.Builtin {
		if a.streaming {
			a.queueInput(custom.Expand(slashCommandArguments(original, cmd)))
			return a, nil
		}
		return a.startTurn(custom.Expand(slashCommandArguments(original, cmd)), true)
	}
	a.conversation.AppendSystem(unknownSlashCommandMessage(original))
	return a, nil
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
	provSlug, modelID, allowWrite, prompt, err := ParseSpawnFlags(args)
	if err != nil {
		a.conversation.AppendSystem("spawn: " + err.Error())
		return a, nil
	}
	snap, spawnErr := a.jobs.Spawn(jobs.SpawnRequest{
		Prompt:      prompt,
		Provider:    provSlug,
		Model:       modelID,
		ParentJobID: "",
		ParentDepth: 0,
		AllowWrite:  allowWrite,
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
	if allowWrite {
		mode = "write · worktree pending"
	}
	a.conversation.AppendSystem(fmt.Sprintf("[job:%s queued — %s · %s] %s", snap.ID, prov, mode, snap.Prompt))
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
	return a.openJobTranscript(args[0], "job")
}

func (a *App) handleAgentsCommand(args []string) (tea.Model, tea.Cmd) {
	if a.jobs == nil {
		a.conversation.AppendSystem("agents: background agents are disabled (no jobs.Manager wired)")
		return a, nil
	}
	if len(args) == 0 {
		a.agentView.Show(a.jobs.List())
		return a, nil
	}
	return a.openJobTranscript(args[0], "agent")
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
	b.WriteString("ID    STATE      ROOT      PROV/MODEL              AGE    TOK(IN/OUT)  PROMPT\n")
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
		prompt := s.Prompt
		if len(prompt) > 50 {
			prompt = prompt[:47] + "..."
		}
		rootMode := "main"
		if s.WorktreePath != "" {
			rootMode = "worktree"
		} else if s.AllowWrite {
			if s.State.IsTerminal() {
				if s.State == jobs.StateFailed {
					rootMode = "failed"
				} else {
					rootMode = "none"
				}
			} else {
				rootMode = "pending"
			}
		}
		fmt.Fprintf(&b, "%-5s %-10s %-9s %-23s %-6s %-12s %s\n",
			trunc(s.ID, 5), trunc(s.State.String(), 10), trunc(rootMode, 9), trunc(prov, 23), age, trunc(tok, 12), prompt)
		if wt := worktreeSummary(s); wt != "" {
			fmt.Fprintf(&b, "      %s\n", wt)
		}
		if digest := jobs.ArtifactDigest(s.Artifacts); digest != "" {
			fmt.Fprintf(&b, "      artifacts: %s\n", digest)
		}
	}
	return strings.TrimRight(b.String(), "\n")
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

func (a *App) factoryDisplaySlugs(seen map[string]struct{}) []string {
	if a.deps.Factories == nil {
		return nil
	}
	var out []string
	for _, slug := range []string{"openai", "anthropic", "gemini", "minimax", "openrouter", "ollama"} {
		if _, exists := a.deps.Factories[slug]; exists {
			out = append(out, slug)
		}
	}
	var customSlugs []string
	for slug := range a.deps.Factories {
		if _, alreadyListed := seen[slug]; alreadyListed {
			continue
		}
		switch slug {
		case "openai", "anthropic", "gemini", "minimax", "openrouter", "ollama":
			continue
		default:
			customSlugs = append(customSlugs, slug)
		}
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

func pollApprover() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return pollApproverMsg{}
	})
}

func tickTopbar() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickTopbarMsg{}
	})
}
