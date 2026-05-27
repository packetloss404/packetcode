// Package agent is the orchestrator that drives a conversation forward:
// user message → LLM stream → tool calls → approval → tool execution →
// LLM stream → … until the LLM returns no more tool calls.
//
// The agent emits a typed channel of AgentEvent values that the TUI (or
// any other consumer) renders. It deliberately knows nothing about the
// terminal — Approver and the event channel are the only seams.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

// maxToolIterations caps the back-and-forth between LLM and tools per
// user message. Without a cap a misbehaving model could loop forever
// (e.g. retrying read_file on a path that keeps not existing). 25 is high
// enough for legitimate multi-step tasks and low enough to fail fast.
const maxToolIterations = 25

// EventType discriminates AgentEvent payloads.
type EventType int

const (
	EventTextDelta        EventType = iota
	EventToolCallProposed           // LLM emitted a complete tool call (pre-approval)
	EventToolCallApproved           // user approved (or trust mode auto-approved)
	EventToolCallRejected           // user rejected
	EventToolCallExecuted           // tool finished, result available
	EventUsageUpdate                // usage tokens recorded
	EventDone                       // turn complete (no more tool calls)
	EventError                      // unrecoverable error; channel about to close
)

// AgentEvent is the unified message the agent emits to the TUI.
type AgentEvent struct {
	Type       EventType
	Text       string            // EventTextDelta
	ToolCall   provider.ToolCall // EventToolCall*
	ToolResult tools.ToolResult  // EventToolCallExecuted
	Usage      provider.Usage    // EventUsageUpdate
	Error      error             // EventError
}

// Agent owns the long-lived dependencies required to run a turn.
// Run() is safe to call repeatedly but not concurrently — the conversation
// is intrinsically serial.
type Agent struct {
	registry     *provider.Registry
	toolRegistry *tools.Registry
	session      *session.Manager
	costTracker  *cost.Tracker
	approver     Approver
	policy       *permissions.Policy
	systemPrompt string
	hooks        *hooks.Runner
}

// Config bundles the agent's required dependencies.
type Config struct {
	Registry     *provider.Registry
	Tools        *tools.Registry
	Session      *session.Manager
	CostTracker  *cost.Tracker
	Approver     Approver
	Policy       *permissions.Policy
	SystemPrompt string
	Hooks        *hooks.Runner
}

// New constructs an Agent. Approver defaults to AutoReject if omitted —
// the safer default; production code must supply a real one.
func New(cfg Config) *Agent {
	if cfg.Approver == nil {
		cfg.Approver = AutoReject("no approver configured")
	}
	if cfg.Policy == nil {
		cfg.Policy = permissions.DefaultPolicy()
	}
	return &Agent{
		registry:     cfg.Registry,
		toolRegistry: cfg.Tools,
		session:      cfg.Session,
		costTracker:  cfg.CostTracker,
		approver:     cfg.Approver,
		policy:       cfg.Policy,
		systemPrompt: cfg.SystemPrompt,
		hooks:        cfg.Hooks,
	}
}

// SetApprover swaps the approver at runtime — used by /trust to flip
// between user-prompted and auto-approve modes mid-conversation.
func (a *Agent) SetApprover(approver Approver) {
	a.approver = approver
}

func (a *Agent) SetPolicy(policy *permissions.Policy) {
	if policy == nil {
		policy = permissions.DefaultPolicy()
	}
	a.policy = policy
}

// Run processes a single user message through the full agentic loop.
// The returned channel is closed once the turn completes (or errors).
// Cancelling ctx interrupts streaming and any in-flight approval.
func (a *Agent) Run(ctx context.Context, userMessage string) <-chan AgentEvent {
	events := make(chan AgentEvent, 16)
	go a.run(ctx, userMessage, events)
	return events
}

func (a *Agent) run(ctx context.Context, userMessage string, events chan<- AgentEvent) {
	defer close(events)

	submittedMessage := userMessage
	if a.hooks != nil {
		cur := a.session.Current()
		sessionID := ""
		if cur != nil {
			sessionID = cur.ID
		}
		out, err := a.hooks.RunUserPromptSubmit(ctx, hooks.PromptPayload{
			SessionID: sessionID,
			Prompt:    userMessage,
		})
		if err != nil {
			events <- AgentEvent{Type: EventError, Error: fmt.Errorf("user prompt hook: %w", err)}
			return
		}
		if out != "" {
			submittedMessage += "\n\n[UserPromptSubmit hook output]\n" + out
		}
	}

	if err := a.session.AddMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: submittedMessage,
	}); err != nil {
		events <- AgentEvent{Type: EventError, Error: fmt.Errorf("save user message: %w", err)}
		return
	}

	for iter := 0; iter < maxToolIterations; iter++ {
		more, err := a.oneTurn(ctx, events)
		if err != nil {
			events <- AgentEvent{Type: EventError, Error: err}
			return
		}
		if !more {
			events <- AgentEvent{Type: EventDone}
			return
		}
	}
	events <- AgentEvent{Type: EventError, Error: fmt.Errorf("exceeded %d tool iterations", maxToolIterations)}
}

// oneTurn streams one assistant response and processes any tool calls.
// Returns (true, nil) if more turns are needed (i.e. tool calls were
// executed and the LLM should respond to their results), (false, nil) if
// the LLM emitted no tool calls (turn complete), or (_, err) on failure.
func (a *Agent) oneTurn(ctx context.Context, events chan<- AgentEvent) (bool, error) {
	prov, modelID := a.registry.Active()
	if prov == nil {
		return false, errors.New("no active provider")
	}

	req := provider.ChatRequest{
		Model:    modelID,
		Messages: a.buildMessages(),
		Stream:   true,
	}
	if prov.SupportsTools(modelID) {
		req.Tools = a.toolRegistry.Definitions()
	} else if a.toolRegistry != nil && len(a.toolRegistry.Definitions()) > 0 {
		req.Messages = append([]provider.Message{{
			Role:    provider.RoleSystem,
			Content: unsupportedToolsMessage(prov.Name(), modelID),
		}}, req.Messages...)
	}

	stream, err := prov.ChatCompletion(ctx, req)
	if err != nil {
		return false, fmt.Errorf("chat completion: %w", err)
	}

	asm := newCallAssembler()
	var fullText string
	var lastUsage *provider.Usage

	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			fullText += ev.TextDelta
			events <- AgentEvent{Type: EventTextDelta, Text: ev.TextDelta}

		case provider.EventToolCallStart:
			asm.start(ev.ToolCall)

		case provider.EventToolCallDelta:
			asm.append(ev.ToolCall)

		case provider.EventToolCallEnd:
			asm.end(ev.ToolCall.Index)

		case provider.EventDone:
			if ev.Usage != nil {
				lastUsage = ev.Usage
			}

		case provider.EventError:
			return false, ev.Error
		}
	}

	calls := asm.finalize()
	for _, call := range calls {
		if err := validateToolCall(call); err != nil {
			return false, err
		}
	}

	if fullText != "" || len(calls) > 0 {
		// Persist the assistant message (text + completed tool calls).
		content := fullText
		if len(calls) > 0 {
			content = ""
		}
		assistantMsg := provider.Message{
			Role:      provider.RoleAssistant,
			Content:   content,
			ToolCalls: calls,
		}
		if err := a.session.AddMessage(assistantMsg); err != nil {
			return false, fmt.Errorf("save assistant message: %w", err)
		}
	}

	if lastUsage != nil {
		inRate, outRate := prov.Pricing(modelID)
		_ = a.session.UpdateUsage(*lastUsage, inRate, outRate)
		if a.costTracker != nil {
			cur := a.session.Current()
			if cur != nil {
				_ = a.costTracker.RecordUsage(cur.ID, prov.Slug(), modelID,
					cur.TokenUsage.TotalInput, cur.TokenUsage.TotalOutput)
			}
		}
		events <- AgentEvent{Type: EventUsageUpdate, Usage: *lastUsage}
	}

	if len(calls) == 0 {
		return false, nil
	}

	for _, call := range calls {
		if err := a.handleToolCall(ctx, call, events); err != nil {
			return false, err
		}
	}
	return true, nil
}

func unsupportedToolsMessage(providerName, modelID string) string {
	return fmt.Sprintf("Native tool calling is unavailable for %s model %q. Do not claim to use tools or request file/command actions; explain the limitation and ask the user to switch to a tool-capable model if tool access is required.", providerName, modelID)
}

// handleToolCall runs the approval flow and either executes the tool or
// records a rejection message. Either way a tool-role message is appended
// to the session so the LLM has full visibility into what happened.
func (a *Agent) handleToolCall(ctx context.Context, call provider.ToolCall, events chan<- AgentEvent) error {
	events <- AgentEvent{Type: EventToolCallProposed, ToolCall: call}

	tool, ok := a.toolRegistry.Get(call.Name)
	if !ok {
		// Unknown tool — feed the error back to the LLM and continue.
		events <- AgentEvent{Type: EventToolCallExecuted, ToolCall: call, ToolResult: tools.ToolResult{
			Content: fmt.Sprintf("unknown tool: %s", call.Name),
			IsError: true,
		}}
		return a.session.AddMessage(provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("unknown tool: %s", call.Name),
		})
	}

	params := json.RawMessage(call.Arguments)
	policyResult := a.policy.Decide(permissions.Request{
		ToolName:         call.Name,
		RequiresApproval: tool.RequiresApproval(),
		Params:           params,
	})
	if policyResult.Decision == permissions.DecisionDeny {
		rejection := "permission denied: " + policyResult.Reason
		events <- AgentEvent{Type: EventToolCallRejected, ToolCall: call, Text: rejection}
		return a.session.AddMessage(provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    rejection,
		})
	}
	if a.hooks != nil {
		preOut, preErr := a.hooks.RunPreToolUse(ctx, hooks.ToolPayload{
			SessionID:  a.currentSessionID(),
			ToolName:   call.Name,
			ToolCallID: call.ID,
			Arguments:  params,
		})
		if preErr != nil {
			rejection := "pre-tool hook blocked " + call.Name + ": " + preErr.Error()
			if preOut != "" {
				rejection += "\n" + preOut
			}
			events <- AgentEvent{Type: EventToolCallRejected, ToolCall: call, Text: rejection}
			return a.session.AddMessage(provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    rejection,
			})
		}
	}
	if policyResult.Decision == permissions.DecisionAsk {
		decision := a.approver.Approve(ctx, ApprovalRequest{
			Tool:     tool,
			ToolCall: call,
			Params:   params,
		})
		if !decision.Approved {
			rejection := decision.Reason
			if rejection == "" {
				rejection = "user rejected the proposed action"
			}
			events <- AgentEvent{Type: EventToolCallRejected, ToolCall: call, Text: rejection}
			return a.session.AddMessage(provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    rejection,
			})
		}
		events <- AgentEvent{Type: EventToolCallApproved, ToolCall: call}
		if len(decision.EditedParams) > 0 {
			params = decision.EditedParams
			editedPolicyResult := a.policy.Decide(permissions.Request{
				ToolName:         call.Name,
				RequiresApproval: tool.RequiresApproval(),
				Params:           params,
			})
			if editedPolicyResult.Decision == permissions.DecisionDeny {
				rejection := "permission denied after approval edit: " + editedPolicyResult.Reason
				events <- AgentEvent{Type: EventToolCallRejected, ToolCall: call, Text: rejection}
				return a.session.AddMessage(provider.Message{
					Role:       provider.RoleTool,
					ToolCallID: call.ID,
					Name:       call.Name,
					Content:    rejection,
				})
			}
		}
	}

	res, err := tool.Execute(ctx, params)
	if err != nil {
		if isContextCancellation(err) {
			return err
		}
		// Distinguish "tool returned an error result" (res.IsError) from
		// "tool itself failed to run" (err != nil). The latter still
		// becomes a tool-role message so the LLM can adapt.
		res = tools.ToolResult{Content: fmt.Sprintf("tool execution failed: %s", err), IsError: true}
	}
	if a.hooks != nil {
		hookOut, hookErr := a.hooks.RunPostToolUse(ctx, hooks.ToolPayload{
			SessionID:  a.currentSessionID(),
			ToolName:   call.Name,
			ToolCallID: call.ID,
			Arguments:  params,
			Result: &hooks.ToolResult{
				Content:  res.Content,
				IsError:  res.IsError,
				Metadata: res.Metadata,
			},
		})
		if hookOut != "" || hookErr != nil {
			res.Content = appendHookOutput(res.Content, hookOut, hookErr)
		}
	}
	events <- AgentEvent{Type: EventToolCallExecuted, ToolCall: call, ToolResult: res}
	return a.session.AddMessage(provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    res.Content,
	})
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (a *Agent) currentSessionID() string {
	if a == nil || a.session == nil || a.session.Current() == nil {
		return ""
	}
	return a.session.Current().ID
}

func appendHookOutput(content, out string, err error) string {
	var b strings.Builder
	b.WriteString(content)
	if out != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[PostToolUse hook output]\n")
		b.WriteString(out)
	}
	if err != nil {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[PostToolUse hook error]\n")
		b.WriteString(err.Error())
	}
	return b.String()
}

func validateToolCall(call provider.ToolCall) error {
	if call.Name == "" {
		return fmt.Errorf("tool call missing name")
	}
	if !json.Valid([]byte(call.Arguments)) {
		return fmt.Errorf("tool call %q arguments are invalid JSON", call.Name)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &obj); err != nil {
		return fmt.Errorf("tool call %q arguments must be a JSON object", call.Name)
	}
	if obj == nil {
		return fmt.Errorf("tool call %q arguments must be a JSON object", call.Name)
	}
	return nil
}

// buildMessages assembles the message array sent to the provider:
// optional system prompt + the session's accumulated messages.
func (a *Agent) buildMessages() []provider.Message {
	cur := a.session.Current()
	var msgs []provider.Message
	if a.systemPrompt != "" {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleSystem,
			Content: a.systemPrompt,
		})
	}
	if cur != nil {
		msgs = append(msgs, normalizeToolTranscript(cur.Messages)...)
	}
	return msgs
}

// ────────────────────────────────────────────────────────────────────────────
// Tool call assembler
// ────────────────────────────────────────────────────────────────────────────

// callAssembler reassembles streaming tool-call deltas into complete
// provider.ToolCall values, indexed by the provider's `Index` field.
//
// Some providers stream tool calls token by token (OpenAI); some emit
// them whole (Gemini, Ollama). The assembler handles both — the only
// invariant is that each call gets a Start, zero-or-more Deltas, and an
// End at some point in the stream.
type callAssembler struct {
	calls map[int]*provider.ToolCall
}

func newCallAssembler() *callAssembler {
	return &callAssembler{calls: map[int]*provider.ToolCall{}}
}

func (a *callAssembler) start(d *provider.ToolCallDelta) {
	if d == nil {
		return
	}
	if _, ok := a.calls[d.Index]; ok {
		return
	}
	a.calls[d.Index] = &provider.ToolCall{
		ID:   d.ID,
		Name: d.Name,
	}
}

func (a *callAssembler) append(d *provider.ToolCallDelta) {
	if d == nil {
		return
	}
	c, ok := a.calls[d.Index]
	if !ok {
		// Some providers skip the explicit Start event and emit the first
		// chunk as a Delta. Treat it as an implicit Start.
		c = &provider.ToolCall{ID: d.ID, Name: d.Name}
		a.calls[d.Index] = c
	}
	if d.ID != "" && c.ID == "" {
		c.ID = d.ID
	}
	if d.Name != "" && c.Name == "" {
		c.Name = d.Name
	}
	c.Arguments += d.ArgumentsDelta
}

func (a *callAssembler) end(_ int) {
	// Nothing to do — finalisation happens in finalize().
}

// finalize returns the assembled calls in Index order.
func (a *callAssembler) finalize() []provider.ToolCall {
	if len(a.calls) == 0 {
		return nil
	}
	indices := make([]int, 0, len(a.calls))
	for i := range a.calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]provider.ToolCall, len(indices))
	for i, idx := range indices {
		c := a.calls[idx]
		if c.Arguments == "" {
			c.Arguments = "{}"
		}
		out[i] = *c
	}
	return out
}
