package app

import (
	"context"
	"strings"
	"sync"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// uiApprover is the bridge between the agent's blocking Approver call
// and the TUI's event-loop-driven approval prompt.
//
// Mechanism: Approve() pushes the request onto pendingCh and blocks on a
// per-request response channel. The App's tea.Update() reads pendingCh,
// raises the approval modal, waits for the user to hit y/n, and resolves
// only the request currently shown. Background agents can ask for approvals
// concurrently, so decisions must never share one global response channel.
type uiApprover struct {
	pendingCh chan approvalEnvelope

	mu        sync.Mutex
	autoTrust bool // when true, every Approve returns Approved without prompting
	policy    *permissions.Policy
	nextID    uint64
	active    *approvalEnvelope
}

type approvalEnvelope struct {
	id     uint64
	ctx    context.Context
	req    agent.ApprovalRequest
	result chan agent.ApprovalDecision
}

func newUIApprover() *uiApprover {
	return &uiApprover{
		pendingCh: make(chan approvalEnvelope, 16),
		policy:    permissions.DefaultPolicy(),
	}
}

func (u *uiApprover) Approve(ctx context.Context, req agent.ApprovalRequest) agent.ApprovalDecision {
	return u.decideOrPrompt(ctx, req, true)
}

func (u *uiApprover) DecideTool(ctx context.Context, req agent.ApprovalRequest, requiresApproval bool) (agent.ApprovalDecision, bool) {
	decision := u.policyDecision(req, requiresApproval)
	switch decision.Decision {
	case permissions.DecisionDeny:
		return agent.ApprovalDecision{
			Approved: false,
			Reason:   "permission policy denied " + req.ToolCall.Name + " (" + decision.Reason + ")",
		}, true
	case permissions.DecisionAllow:
		if requiresApproval {
			return agent.ApprovalDecision{Approved: true, EditedParams: req.Params}, true
		}
		return agent.ApprovalDecision{}, false
	case permissions.DecisionAsk:
		return u.decideOrPrompt(ctx, req, requiresApproval), true
	default:
		return agent.ApprovalDecision{}, false
	}
}

func (u *uiApprover) policyDecision(req agent.ApprovalRequest, requiresApproval bool) permissions.Result {
	u.mu.Lock()
	policy := u.policy
	u.mu.Unlock()
	if policy == nil {
		policy = permissions.DefaultPolicy()
	}
	name := ""
	if req.Tool != nil {
		name = req.Tool.Name()
	}
	if name == "" {
		name = stripJobApprovalPrefix(req.ToolCall.Name)
	}
	return policy.Decide(permissions.Request{
		ToolName:         name,
		RequiresApproval: requiresApproval,
		Params:           req.Params,
	})
}

func (u *uiApprover) decideOrPrompt(ctx context.Context, req agent.ApprovalRequest, requiresApproval bool) agent.ApprovalDecision {
	decision := u.policyDecision(req, requiresApproval)
	switch decision.Decision {
	case permissions.DecisionDeny:
		return agent.ApprovalDecision{
			Approved: false,
			Reason:   "permission policy denied " + req.ToolCall.Name + " (" + decision.Reason + ")",
		}
	case permissions.DecisionAllow:
		return agent.ApprovalDecision{Approved: true, EditedParams: req.Params}
	}
	u.mu.Lock()
	trusted := u.autoTrust
	u.nextID++
	id := u.nextID
	u.mu.Unlock()
	if trusted {
		return agent.ApprovalDecision{Approved: true, EditedParams: req.Params}
	}
	env := approvalEnvelope{
		id:     id,
		ctx:    ctx,
		req:    req,
		result: make(chan agent.ApprovalDecision, 1),
	}

	select {
	case u.pendingCh <- env:
	case <-ctx.Done():
		return agent.ApprovalDecision{Approved: false, Reason: "cancelled"}
	}
	select {
	case dec := <-env.result:
		return dec
	case <-ctx.Done():
		u.clearActive(id)
		return agent.ApprovalDecision{Approved: false, Reason: "cancelled"}
	}
}

// Pending returns the next pending request without blocking. Returns
// (zero, false) if the queue is empty. The App polls this from its
// Update loop.
func (u *uiApprover) Pending() (agent.ApprovalRequest, bool) {
	u.mu.Lock()
	if u.active != nil {
		if u.active.ctx.Err() == nil {
			u.mu.Unlock()
			return agent.ApprovalRequest{}, false
		}
		u.active = nil
	}
	u.mu.Unlock()

	for {
		select {
		case env := <-u.pendingCh:
			if env.ctx.Err() != nil {
				continue
			}
			u.mu.Lock()
			u.active = &env
			u.mu.Unlock()
			return env.req, true
		default:
			return agent.ApprovalRequest{}, false
		}
	}
}

func (u *uiApprover) QueueDepth() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	depth := len(u.pendingCh)
	if u.active != nil && u.active.ctx.Err() == nil {
		depth++
	}
	if depth < 1 {
		return 1
	}
	return depth
}

// Resolve posts the user's decision back to the approval currently visible.
func (u *uiApprover) Resolve(decision agent.ApprovalDecision) {
	u.mu.Lock()
	env := u.active
	u.active = nil
	u.mu.Unlock()
	if env == nil {
		return
	}
	select {
	case env.result <- decision:
	default:
	}
}

func (u *uiApprover) clearActive(id uint64) {
	u.mu.Lock()
	if u.active != nil && u.active.id == id {
		u.active = nil
	}
	u.mu.Unlock()
}

// SetTrust toggles trust mode. When enabled, future Approve() calls
// return immediately without raising the modal.
func (u *uiApprover) SetTrust(trust bool) {
	u.mu.Lock()
	u.autoTrust = trust
	u.mu.Unlock()
}

// IsTrusted reports trust mode.
func (u *uiApprover) IsTrusted() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.autoTrust || (u.policy != nil && u.policy.Profile() == permissions.ProfileFull)
}

func (u *uiApprover) SetPermissionPolicy(policy *permissions.Policy) {
	u.mu.Lock()
	u.policy = policy
	u.mu.Unlock()
}

func (u *uiApprover) PermissionPolicy() *permissions.Policy {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.policy == nil {
		return permissions.DefaultPolicy()
	}
	return u.policy
}

func stripJobApprovalPrefix(name string) string {
	if strings.HasPrefix(name, "[job:") {
		if _, rest, ok := strings.Cut(name, "] "); ok {
			return rest
		}
	}
	return name
}

// describeRequest is a small helper for the conversation log.
func describeRequest(t tools.Tool, c provider.ToolCall) string {
	return t.Name() + ": " + c.Arguments
}
