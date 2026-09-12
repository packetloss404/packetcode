package app

import (
	"encoding/json"
	"strings"

	"github.com/packetcode/packetcode/internal/permissions"
	"github.com/packetcode/packetcode/internal/provider"
)

// rememberApproval installs a session permission rule from the approval
// prompt's "always allow" choice. Shell programs are remembered exactly:
// inferring a prefix can authorize additional commands through shell syntax.
//
// Callers must have confirmed that the decision reached a waiting approver
// envelope (see resolveApprovalResult). A standing rule is authority the user
// granted for a specific request; installing one on behalf of a prompt whose
// job had already gone away grants it for a request nobody made.
func (a *App) rememberApproval(call provider.ToolCall) {
	call.Name = stripJobApprovalPrefix(call.Name)
	base := a.sessionPermissionPolicy()
	if call.Name == "execute_command" {
		if command, ok := commandFromArgs(call.Arguments); ok {
			if base.Profile() == permissions.ProfileFull {
				a.preTrustPolicy = a.trustOffPolicy().WithCommandRule(command, permissions.DecisionAllow)
			} else {
				a.preTrustPolicy = nil
			}
			a.setSessionPermissionPolicy(base.WithCommandRule(command, permissions.DecisionAllow))
			a.conversation.AppendSystem("Added a session rule for this exact command text, in any working directory. Review with /permissions; revoke all session rules with /permissions reset.")
		}
		return
	}
	if base.Profile() == permissions.ProfileFull {
		a.preTrustPolicy = a.trustOffPolicy().WithRule(call.Name, permissions.DecisionAllow)
	} else {
		a.preTrustPolicy = nil
	}
	a.setSessionPermissionPolicy(base.WithRule(call.Name, permissions.DecisionAllow))
	a.conversation.AppendSystem("Added a session rule for " + call.Name + " with any arguments or paths. Review with /permissions; revoke all session rules with /permissions reset.")
}

func commandFromArgs(args string) (string, bool) {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil || strings.TrimSpace(parsed.Command) == "" {
		return "", false
	}
	return parsed.Command, true
}
