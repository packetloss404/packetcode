package permissions

import (
	"encoding/json"
	"testing"

	"github.com/packetcode/packetcode/internal/config"
)

func TestPolicy_DefaultProfilePromptsDestructiveAllowsRead(t *testing.T) {
	p := Must(config.PermissionConfig{})

	assertDecision(t, p, Request{ToolName: "read_file"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "list_symbols"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "find_definition"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "find_references"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "get_diagnostics"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "write_file", RequiresApproval: true}, DecisionAsk)
	assertDecision(t, p, Request{ToolName: "execute_command", RequiresApproval: true}, DecisionAsk)
	assertDecision(t, p, Request{ToolName: "filesystem__write_file", RequiresApproval: true}, DecisionAsk)
}

func TestPolicy_FullProfileAllowsAllExceptExplicitDeny(t *testing.T) {
	p := Must(config.PermissionConfig{
		Profile: string(ProfileFull),
		Rules: []config.PermissionRule{{
			Tool:   "execute_command",
			Action: string(DecisionDeny),
			Reason: "shell disabled",
		}},
	})

	assertDecision(t, p, Request{ToolName: "write_file", RequiresApproval: true}, DecisionAllow)
	res := p.Decide(Request{ToolName: "execute_command", RequiresApproval: true})
	if res.Decision != DecisionDeny || res.Reason != "shell disabled" {
		t.Fatalf("deny rule = %+v", res)
	}
}

func TestPolicy_SafeProfileDeniesDestructiveAndAllowsRead(t *testing.T) {
	p := Must(config.PermissionConfig{Profile: string(ProfileSafe)})

	assertDecision(t, p, Request{ToolName: "search_codebase"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "find_references"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "get_diagnostics"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "patch_file", RequiresApproval: true}, DecisionDeny)
	assertDecision(t, p, Request{ToolName: "spawn_agent", RequiresApproval: true}, DecisionDeny)
	assertDecision(t, p, Request{ToolName: "filesystem__read_file", RequiresApproval: true}, DecisionDeny)
}

func TestPolicy_RuleSpecificityAndOrder(t *testing.T) {
	p := Must(config.PermissionConfig{
		Profile: string(ProfileSafe),
		Rules: []config.PermissionRule{
			{Tool: "mcp:*", Action: string(DecisionAsk)},
			{Tool: "filesystem__read_file", Action: string(DecisionAllow)},
		},
	})

	assertDecision(t, p, Request{ToolName: "filesystem__read_file", RequiresApproval: true}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "filesystem__write_file", RequiresApproval: true}, DecisionAsk)
}

func TestPolicy_CommandPrefixMatchesFields(t *testing.T) {
	p := Must(config.PermissionConfig{
		Profile: string(ProfileSafe),
		Rules: []config.PermissionRule{{
			Tool:          "execute_command",
			Action:        string(DecisionAsk),
			CommandPrefix: []string{"git", "status"},
		}},
	})

	params, _ := json.Marshal(map[string]any{"command": "git status --short"})
	assertDecision(t, p, Request{ToolName: "execute_command", RequiresApproval: true, Params: params}, DecisionAsk)

	params, _ = json.Marshal(map[string]any{"command": "git status-rm"})
	assertDecision(t, p, Request{ToolName: "execute_command", RequiresApproval: true, Params: params}, DecisionDeny)
}

func TestPolicy_CustomProfileAndRules(t *testing.T) {
	p := Must(config.PermissionConfig{
		Profile: "balanced-plus",
		Profiles: map[string]config.PermissionProfile{
			"balanced-plus": {
				"default":         "ask",
				"read_file":       "allow",
				"search_codebase": "allow",
				"list_directory":  "allow",
				"mcp":             "ask",
			},
		},
		Rules: []config.PermissionRule{{
			Tool:    "execute_command",
			Action:  "deny",
			Command: "rm -rf *",
			Reason:  "destructive delete",
		}},
	})

	assertDecision(t, p, Request{ToolName: "read_file"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "filesystem__read_file", RequiresApproval: true}, DecisionAsk)
	params, _ := json.Marshal(map[string]any{"command": "rm -rf *"})
	res := p.Decide(Request{ToolName: "execute_command", RequiresApproval: true, Params: params})
	if res.Decision != DecisionDeny || res.Reason != "destructive delete" {
		t.Fatalf("exact command deny = %+v", res)
	}
}

func TestPolicy_CustomProfileDefaultDenyIsTrueFallback(t *testing.T) {
	p := Must(config.PermissionConfig{
		Profile: "locked",
		Profiles: map[string]config.PermissionProfile{
			"locked": {
				"default":   "deny",
				"read_file": "allow",
			},
		},
	})

	assertDecision(t, p, Request{ToolName: "read_file"}, DecisionAllow)
	assertDecision(t, p, Request{ToolName: "search_codebase"}, DecisionDeny)
	assertDecision(t, p, Request{ToolName: "execute_command", RequiresApproval: true}, DecisionDeny)
}

func TestPolicy_MapBackedRulesAreDeterministicAndSpecificWins(t *testing.T) {
	cfg := config.PermissionConfig{
		Profile: "custom",
		Profiles: map[string]config.PermissionProfile{
			"custom": {
				"default":          "ask",
				"mcp":              "allow",
				"server__danger":   "deny",
				"filesystem__safe": "allow",
			},
		},
		Tools: map[string]string{
			"filesystem__*":      "allow",
			"filesystem__danger": "deny",
		},
	}
	for i := 0; i < 50; i++ {
		p := Must(cfg)
		assertDecision(t, p, Request{ToolName: "filesystem__read_file", RequiresApproval: true}, DecisionAllow)
		assertDecision(t, p, Request{ToolName: "filesystem__danger", RequiresApproval: true}, DecisionDeny)
		assertDecision(t, p, Request{ToolName: "server__read", RequiresApproval: true}, DecisionAllow)
		assertDecision(t, p, Request{ToolName: "server__danger", RequiresApproval: true}, DecisionDeny)
	}
}

func assertDecision(t *testing.T, p *Policy, req Request, want Decision) {
	t.Helper()
	if got := p.Decide(req); got.Decision != want {
		t.Fatalf("Decide(%+v) = %+v, want %s", req, got, want)
	}
}
