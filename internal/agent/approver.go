package agent

import (
	"context"
	"encoding/json"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// Approver decides whether an approval-gated tool call may proceed.
//
// The interface decouples the agent loop from the TUI: in production the
// App layer wires a channel-based approver to its approval prompt
// component; in tests we use AutoApprove / AutoReject / a custom stub.
//
// Approve() blocks until a decision is made. The agent guarantees a
// derived context is passed in so that cancellation (Ctrl+C) interrupts
// any pending approval.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) ApprovalDecision
}

// ToolDecider is an optional extension for approvers that can apply
// policy before deciding whether a tool call should be allowed, denied,
// or routed through the normal approval prompt. handled=false means the
// agent should use the legacy RequiresApproval behavior.
type ToolDecider interface {
	DecideTool(ctx context.Context, req ApprovalRequest, requiresApproval bool) (decision ApprovalDecision, handled bool)
}

// ApprovalRequest is the payload handed to the approver. We pass the Tool
// itself (not just its name) so the UI can describe the action richly —
// e.g. render a diff for write_file or the command for execute_command.
type ApprovalRequest struct {
	Tool     tools.Tool
	ToolCall provider.ToolCall
	Params   json.RawMessage
}

// ApprovalDecision carries the user's response back. EditedParams may
// override the LLM-supplied params (e.g. the user edited the command
// before approving). Reason is a free-form rejection message that gets
// fed back to the LLM as the tool result on rejection.
type ApprovalDecision struct {
	Approved     bool
	EditedParams json.RawMessage
	Reason       string
}

// AutoApprove returns an Approver that accepts every request unmodified.
// Used for trust mode and for tests of the agent's happy path.
func AutoApprove() Approver { return autoApprover{} }

type autoApprover struct{}

func (autoApprover) Approve(_ context.Context, req ApprovalRequest) ApprovalDecision {
	return ApprovalDecision{Approved: true, EditedParams: req.Params}
}

// AutoReject returns an Approver that rejects every request with the
// given reason. Useful for testing rejection paths.
func AutoReject(reason string) Approver { return autoRejector{reason: reason} }

type autoRejector struct{ reason string }

func (a autoRejector) Approve(_ context.Context, _ ApprovalRequest) ApprovalDecision {
	return ApprovalDecision{Approved: false, Reason: a.reason}
}
