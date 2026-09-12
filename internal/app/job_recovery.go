package app

import (
	"strings"

	"github.com/packetcode/packetcode/internal/jobs"
)

func jobResubmitError(err *jobs.SpawnError) string {
	message := strings.TrimSpace(err.Reason)
	if message == "" {
		message = err.Error()
	}
	switch err.Code {
	case "manager_closed":
		return "background jobs have shut down; restart PacketCode before resubmitting"
	case "unknown_job":
		return message + "; use /jobs to find the full job ID"
	case "limit_reached":
		return message + "; this limit applies to the whole app run, so finish other jobs before restarting PacketCode"
	default:
		return message
	}
}
