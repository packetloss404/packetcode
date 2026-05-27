package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

type approverTestTool struct{}

func (approverTestTool) Name() string            { return "test_tool" }
func (approverTestTool) Description() string     { return "test" }
func (approverTestTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (approverTestTool) RequiresApproval() bool  { return true }
func (approverTestTool) Execute(context.Context, json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}

func TestUIApproverRoutesDecisionToVisibleRequest(t *testing.T) {
	u := newUIApprover()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dec1 := make(chan agent.ApprovalDecision, 1)
	go func() {
		dec1 <- u.Approve(ctx, approvalReq("first"))
	}()

	req := waitPendingApproval(t, u)
	if req.ToolCall.ID != "first" {
		t.Fatalf("first pending id = %q, want first", req.ToolCall.ID)
	}

	dec2 := make(chan agent.ApprovalDecision, 1)
	go func() {
		dec2 <- u.Approve(ctx, approvalReq("second"))
	}()

	if _, ok := u.Pending(); ok {
		t.Fatalf("second request surfaced while first approval was active")
	}
	u.Resolve(agent.ApprovalDecision{Approved: true, Reason: "first decision"})
	got1 := waitDecision(t, dec1)
	if !got1.Approved || got1.Reason != "first decision" {
		t.Fatalf("first decision = %+v", got1)
	}

	req = waitPendingApproval(t, u)
	if req.ToolCall.ID != "second" {
		t.Fatalf("second pending id = %q, want second", req.ToolCall.ID)
	}
	u.Resolve(agent.ApprovalDecision{Approved: false, Reason: "second decision"})
	got2 := waitDecision(t, dec2)
	if got2.Approved || got2.Reason != "second decision" {
		t.Fatalf("second decision = %+v", got2)
	}
}

func TestUIApproverPermissionPolicyAllowAndDeny(t *testing.T) {
	u := newUIApprover()
	u.SetPermissionPolicy(permissions.DefaultPolicy().WithRule("test_tool", permissions.ActionDeny))
	denied := u.Approve(context.Background(), approvalReq("deny"))
	if denied.Approved || denied.Reason == "" {
		t.Fatalf("denied decision = %+v", denied)
	}
	if _, ok := u.Pending(); ok {
		t.Fatalf("denied policy request should not reach approval queue")
	}

	u.SetPermissionPolicy(permissions.DefaultPolicy().WithRule("test_tool", permissions.ActionAllow))
	allowed := u.Approve(context.Background(), approvalReq("allow"))
	if !allowed.Approved || string(allowed.EditedParams) != `{}` {
		t.Fatalf("allowed decision = %+v", allowed)
	}
	if _, ok := u.Pending(); ok {
		t.Fatalf("allowed policy request should not reach approval queue")
	}
}

func approvalReq(id string) agent.ApprovalRequest {
	return agent.ApprovalRequest{
		Tool: approverTestTool{},
		ToolCall: provider.ToolCall{
			ID:        id,
			Name:      "test_tool",
			Arguments: `{}`,
		},
		Params: json.RawMessage(`{}`),
	}
}

func waitPendingApproval(t *testing.T, u *uiApprover) agent.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if req, ok := u.Pending(); ok {
			return req
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for pending approval")
	return agent.ApprovalRequest{}
}

func waitDecision(t *testing.T, ch <-chan agent.ApprovalDecision) agent.ApprovalDecision {
	t.Helper()
	select {
	case dec := <-ch:
		return dec
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for approval decision")
		return agent.ApprovalDecision{}
	}
}
