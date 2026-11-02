package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/statusline"
	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/agentview"
	"github.com/packetcode/packetcode/internal/ui/components/approval"
	"github.com/packetcode/packetcode/internal/ui/components/conversation"
	"github.com/packetcode/packetcode/internal/ui/components/input"
	"github.com/packetcode/packetcode/internal/ui/components/picker"
	"github.com/packetcode/packetcode/internal/ui/components/spinner"
	"github.com/packetcode/packetcode/internal/ui/components/topbar"
	"github.com/packetcode/packetcode/internal/ui/components/workflowview"
	"github.com/packetcode/packetcode/internal/ui/layout"
	"github.com/packetcode/packetcode/internal/ui/theme"
	"github.com/packetcode/packetcode/internal/workflow"
)

// TUIFixtureStates is the credential-free lifecycle set understood by
// --tui-fixture. Each state is built from production components so PTY captures
// exercise the same rendering paths as the real app without loading config,
// sessions, providers, keychains, hooks, MCP servers, or project files.
var TUIFixtureStates = []string{
	"user-assistant",
	"thinking",
	"streaming",
	"tool-running",
	"tool-result",
	"approval",
	"error",
	"cancelled",
	"queued",
	"compacting",
	"compacted",
	"normal",
	"accept-edits",
	"auto",
	"plan",
	"bypass",
	"agents",
	"workflows",
	"provider-picker",
	"model-picker",
}

type tuiFixtureModel struct {
	state        string
	width        int
	height       int
	ready        bool
	mode         permMode
	conversation conversation.Model
	input        input.Model
	topbar       topbar.Model
	spinner      spinner.Model
	approval     approval.Model
	agentView    agentview.Model
	workflowView workflowview.Model
	picker       picker.Model
}

// NewTUIFixture returns a standalone Bubble Tea model for deterministic visual
// regression captures. It deliberately has no App/agent/provider dependencies.
func NewTUIFixture(state string) (tea.Model, error) {
	state = strings.TrimSpace(state)
	if !containsFixtureState(state) {
		return nil, fmt.Errorf("unknown TUI fixture %q (valid: %s)", state, strings.Join(TUIFixtureStates, ", "))
	}
	return &tuiFixtureModel{
		state:        state,
		mode:         modeNormal,
		conversation: conversation.New(),
		input:        input.New(),
		topbar:       topbar.New(),
		spinner:      spinner.New(),
		approval:     approval.New(),
		agentView:    agentview.New(),
		workflowView: workflowview.New(),
	}, nil
}

func containsFixtureState(state string) bool {
	for _, candidate := range TUIFixtureStates {
		if candidate == state {
			return true
		}
	}
	return false
}

func (m *tuiFixtureModel) Init() tea.Cmd { return nil }

func (m *tuiFixtureModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.conversation.Resize(msg.Width, msg.Height)
		m.input.Resize(msg.Width, msg.Height)
		m.topbar.SetWidth(msg.Width)
		m.approval.SetWidth(msg.Width)
		m.agentView.Resize(msg.Width, msg.Height)
		m.workflowView.Resize(msg.Width, msg.Height)
		m.picker.Resize(msg.Width, msg.Height)
		if !m.ready {
			m.populate()
			m.ready = true
		}
		m.refreshStatusLine()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" || (msg.String() == "q" && !m.picker.Visible()) {
			return m, tea.Quit
		}
		if m.picker.Visible() {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *tuiFixtureModel) View() string {
	if !m.ready {
		return ""
	}
	overlay := ""
	switch m.state {
	case "thinking", "compacting":
		overlay = m.spinner.View()
	case "approval":
		overlay = m.approval.View()
	case "agents":
		overlay = m.agentView.View()
	case "workflows":
		overlay = m.workflowView.View()
	case "provider-picker", "model-picker":
		overlay = m.picker.View()
	}
	status := m.topbar.View() + "\n  " + renderPermModeHint(m.mode)
	in := m.input.View()
	switch m.state {
	case "approval", "provider-picker", "model-picker":
		in = m.input.ViewBlurred()
	case "agents":
		in = m.input.ViewWithPlaceholder("press n to dispatch a new agent")
		status = ""
	case "workflows":
		in = ""
		status = ""
	}
	return layout.Frame(m.conversation.View(), overlay, "", in, status)
}

func (m *tuiFixtureModel) refreshStatusLine() {
	m.topbar.SetCustomLine(statusline.RenderDefaultWidth(statusline.Snapshot{
		Provider:      statusline.ProviderInfo{DisplayName: "Codex (ChatGPT)"},
		Model:         statusline.ModelInfo{ID: "gpt-5.6-sol", ReasoningEffort: "high"},
		ContextWindow: statusline.ContextInfo{Used: 33_000, Max: 272_000, UsedPercentage: 12},
		Project:       "packetcode",
		GitBranch:     "main",
	}, m.width-4))
}

func (m *tuiFixtureModel) populate() {
	switch m.state {
	case "provider-picker":
		m.picker = picker.New("provider", "Select provider")
		m.picker.SetSubtitle("Choose a provider or connect an API key.")
		m.picker.SetHelp("Ctrl+A API key")
		m.picker.SetItems([]picker.Item{
			{ID: "openai", Label: "OpenAI", Detail: "openai · key present · gpt-5.6-sol", Color: theme.ProviderColor("openai")},
			{ID: "codex", Label: "Codex (ChatGPT)", Detail: "codex · ChatGPT login · gpt-5.6-sol", Marker: "●", Color: theme.ProviderColor("openai")},
			{ID: "anthropic", Label: "Anthropic", Detail: "anthropic · Ctrl+A to set key", Status: "key needed", Color: theme.ProviderColor("anthropic")},
			{ID: "gemini", Label: "Google Gemini", Detail: "gemini · Ctrl+A to set key", Status: "key needed", Color: theme.ProviderColor("gemini")},
			{ID: "openrouter", Label: "OpenRouter", Detail: "openrouter · key present", Color: theme.ProviderColor("openrouter")},
			{ID: "ollama", Label: "Ollama", Detail: "ollama · local · qwen3-coder:30b", Color: theme.ProviderColor("ollama")},
		})
		m.picker.SetActive("codex")
		m.picker.Resize(m.width, m.height)
		m.picker.Open(nil)
	case "model-picker":
		m.picker = picker.New("model", "Select model")
		m.picker.SetSubtitle("Codex (ChatGPT) · Choose a model for this provider.")
		m.picker.SetHelp("Ctrl+R refresh")
		m.picker.SetItems([]picker.Item{
			{ID: "gpt-5.6-sol", Label: "gpt-5.6-sol", Detail: "272k context · tools · free", Marker: "●", Color: theme.ProviderColor("openai")},
			{ID: "gpt-5.6", Label: "gpt-5.6", Detail: "272k context · tools · free", Color: theme.ProviderColor("openai")},
			{ID: "gpt-5.3-codex", Label: "gpt-5.3-codex", Detail: "272k context · tools · free", Color: theme.ProviderColor("openai")},
			{ID: "gpt-5.3-codex-spark", Label: "gpt-5.3-codex-spark", Detail: "128k context · tools · free", Color: theme.ProviderColor("openai")},
			{ID: "gpt-5.1-codex-mini", Label: "gpt-5.1-codex-mini", Detail: "272k context · tools · free", Color: theme.ProviderColor("openai")},
			{ID: "gpt-5.1-codex-max", Label: "gpt-5.1-codex-max", Detail: "272k context · tools · free", Color: theme.ProviderColor("openai")},
		})
		m.picker.SetActive("gpt-5.6-sol")
		m.picker.Resize(m.width, m.height)
		m.picker.Open(nil)
	case "user-assistant":
		m.conversation.AppendUser("Summarize the current change with Unicode: café · 中文 · 👩🏽‍💻.")
		m.conversation.AppendAgentText("gpt-5.6-sol", "codex", "The context accounting and TUI parity changes are ready for review.")
		m.conversation.FinaliseAgent()
	case "thinking":
		m.conversation.AppendUser("Review the implementation for correctness.")
		m.topbar.SetOperation(true, "thinking", time.Now().Add(-4*time.Second), 0)
		m.spinner.Start("Thinking…")
	case "streaming":
		m.conversation.AppendUser("Explain the failing test.")
		m.conversation.AppendAgentReasoning("gpt-5.6-sol", "codex", "Checking the state transition and its tests…")
		m.conversation.AppendAgentText("gpt-5.6-sol", "codex", "The failure occurs because the live region is")
	case "tool-running":
		m.conversation.AppendUser("Run the focused tests.")
		m.conversation.AppendToolCallWithID("execute_command", `{"command":"go test ./internal/app/..."}`, "fixture-call")
		m.conversation.AppendToolOutput("fixture-call", "ok  github.com/packetcode/packetcode/internal/app\n")
	case "tool-result":
		m.conversation.AppendUser("Read the relevant configuration.")
		m.conversation.AppendToolCall("read_file", `{"path":"docs/configuration.md"}`)
		m.conversation.CompleteToolCall("read_file", tools.ToolResult{Content: "Read 115 lines from docs/configuration.md"})
	case "approval":
		m.conversation.AppendUser("Run the release build.")
		m.approval.Show(fixtureTool{name: "execute_command"}, provider.ToolCall{
			ID: "fixture-approval", Name: "execute_command",
			Arguments: `{"command":"go build ./...","timeout_sec":120}`,
		})
	case "error":
		m.conversation.AppendUser("Continue the review.")
		m.conversation.AppendError("provider request failed: connection timed out; retrying did not recover")
	case "cancelled":
		m.conversation.AppendUser("Run the full test suite.")
		m.conversation.AppendSystem("turn cancelled")
	case "queued":
		m.conversation.AppendUser("Review the implementation.")
		m.conversation.AppendQueuedUser("Then run the focused tests.")
		m.topbar.SetOperation(true, "thinking", time.Now().Add(-2*time.Second), 1)
	case "compacting":
		m.conversation.AppendSystem("automatic context compaction triggered")
		m.conversation.AppendSystem("compacting context... (~218000 tokens)")
		m.spinner.Start("Compacting…")
	case "compacted":
		m.conversation.AppendSystem("compacted: 218000 -> 62400 tokens (kept 10 recent messages)")
	case "normal":
		m.mode = modeNormal
	case "accept-edits":
		m.mode = modeAcceptEdits
	case "auto":
		m.mode = modeAuto
	case "plan":
		m.mode = modePlan
	case "bypass":
		m.mode = modeBypass
	case "agents":
		m.agentView.Show(fixtureJobs())
		m.input.Blur()
	case "workflows":
		m.workflowView.Show(fixtureWorkflows())
	}
}

type fixtureTool struct{ name string }

func (f fixtureTool) Name() string            { return f.name }
func (f fixtureTool) Description() string     { return "fixture tool" }
func (f fixtureTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f fixtureTool) RequiresApproval() bool  { return true }
func (f fixtureTool) Execute(context.Context, json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}

func fixtureJobs() []agentview.Job {
	now := time.Now()
	return []agentview.Job{
		{ID: "a1b2c3d4", Prompt: "Review context accounting", Provider: "codex", Model: "gpt-5.6-sol", State: agentview.StateRunning, LastActivity: "testing", LastMessage: "running focused tests", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now},
		{ID: "e5f6a7b8", Prompt: "Compare Claude Code lifecycle UI", Provider: "codex", Model: "gpt-5.6-sol", State: agentview.StateCompleted, Summary: "PTY comparison complete", CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now.Add(-time.Minute), FinishedAt: now.Add(-time.Minute)},
	}
}

func fixtureWorkflows() []workflow.RunSnapshot {
	now := time.Now()
	job := jobs.Snapshot{ID: "a1b2c3d4", Prompt: "Review context accounting", Provider: "codex", Model: "gpt-5.6-sol", State: jobs.StateRunning, LastActivity: "testing", LastMessage: "running focused tests", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now}
	return []workflow.RunSnapshot{{
		ID: "run00001", Workflow: "review", State: workflow.RunRunning, StartedAt: now.Add(-2 * time.Minute),
		Phases: []workflow.PhaseSnapshot{{Name: "analysis", Steps: []workflow.StepSnapshot{{Name: "fan-out review", Mode: workflow.StepParallel, Agents: []workflow.AgentSnapshot{{JobID: job.ID, Job: job, HasJob: true}}}}}},
	}}
}
