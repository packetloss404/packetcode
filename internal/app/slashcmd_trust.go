package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/permissions"
)

// handleTrustCommand toggles or reports the session's trust mode. With
// trust mode on, destructive tool calls auto-approve; with it off, the
// approval modal is raised. Escalation in either direction is immediate
// — the user is only ever mutating their own session's behaviour.
func (a *App) handleTrustCommand(args []string) (tea.Model, tea.Cmd) {
	set, value, err := parseTrustArgs(args)
	if err != nil {
		a.conversation.AppendSystem("trust: " + err.Error())
		return a, nil
	}
	if !set {
		state := "off"
		if a.currentPermissionPolicy().Profile() == permissions.ProfileFull {
			state = "on"
		}
		a.conversation.AppendSystem("trust mode: " + state)
		return a, nil
	}
	if value {
		if a.currentPermissionPolicy().Profile() != permissions.ProfileFull {
			a.preTrustPolicy = a.currentPermissionPolicy()
		}
		a.approver.SetTrust(false)
		a.setPermissionPolicy(a.currentPermissionPolicy().WithProfile(permissions.ProfileFull))
		a.conversation.AppendSystem("trust mode enabled — prompted tools will auto-approve unless policy denies them")
	} else {
		a.approver.SetTrust(false)
		restore := a.preTrustPolicy
		if restore == nil {
			restore = a.permissionBase
		}
		if restore == nil {
			restore = a.currentPermissionPolicy().WithProfile(permissions.ProfileAsk)
		}
		a.preTrustPolicy = nil
		a.setPermissionPolicy(restore)
		a.conversation.AppendSystem("trust mode disabled — destructive tools will prompt")
	}
	return a, nil
}
