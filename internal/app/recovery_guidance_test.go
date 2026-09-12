package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/jobs"
	"github.com/packetcode/packetcode/internal/mcp"
	"github.com/packetcode/packetcode/internal/testwait"
)

func TestJobRecoveryUsesFullIDsAndReportsClosedManager(t *testing.T) {
	r := newTestApp(t)
	dir := filepath.Join(r.tmp, "jobs")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ab123456.json"), []byte(`{"id":"ab123456","session_id":"main-job-ab123456","prompt":"recover this","provider":"fake","model":"fake-model","state":"running"}`), 0600))
	m := wireJobsManagerForSlashTest(t, r)
	t.Cleanup(func() { require.NoError(t, m.Shutdown(testwait.Timeout(time.Second))) })
	r.app.handleJobsResubmit(nil)
	require.Contains(t, convText(r.app), "/jobs resubmit ab123456")
	require.Contains(t, renderJobsTable(m.List()), "ab123456")
	require.NoError(t, m.Shutdown(testwait.Timeout(time.Second)))
	r.app.handleJobsResubmit([]string{"ab123456"})
	require.Contains(t, convText(r.app), "background jobs have shut down; restart PacketCode")
}

func TestJobRecoveryExplainsUnusableSavedPrompts(t *testing.T) {
	for _, prompt := range []string{"", strings.Repeat("x", jobs.MaxResubmitPromptBytes+1)} {
		summary := reconcileSummary(jobs.Snapshot{ID: "ab123456", State: jobs.StateAbandoned, Recovered: true, Prompt: prompt})
		require.NotContains(t, summary, "/jobs resubmit")
		require.Contains(t, summary, "/jobs ab123456")
	}
	summary := reconcileSummary(jobs.Snapshot{ID: "ab123456", State: jobs.StateCancelled, Recovered: true, Prompt: "work"})
	require.Contains(t, summary, "cancelled before starting")
	require.NotContains(t, summary, "abandoned")
}

func TestJobRecoveryRejectsExtraArguments(t *testing.T) {
	r := newTestApp(t)
	r.app.handleJobsResubmit([]string{"ab123456", "unexpected"})
	require.Contains(t, convText(r.app), "exactly one job")
}

func TestMCPRecoveryGuidanceDistinguishesDisabledAndFailed(t *testing.T) {
	for _, state := range []string{"disabled", "failed", "exited"} {
		reports := []mcp.StartupReport{{Name: "example", Status: state}}
		status, ok := renderMCPStatus("example", reports, nil)
		require.True(t, ok)
		toolList, ok := renderMCPTools("example", reports, nil)
		require.True(t, ok)
		for _, output := range []string{status, toolList} {
			if state == "disabled" {
				require.Contains(t, output, "enabled = true")
				require.Contains(t, output, "restart PacketCode")
				require.NotContains(t, output, "/mcp restart example")
			} else {
				require.Contains(t, output, "/mcp logs example")
				require.Contains(t, output, "/mcp restart example")
				require.Contains(t, output, "does not rerun a failed tool call")
			}
		}
	}
}

func TestMCPRecoveryNamesAndFailureAreActionable(t *testing.T) {
	name := "company-internal-github"
	report := mcp.StartupReport{Name: name, Status: "failed"}
	require.Contains(t, renderMCPTable([]mcp.StartupReport{report}, nil), name)
	message := formatMCPRestartError(name, report, errors.New("executable not found"))
	require.Contains(t, message, "executable not found")
	require.Contains(t, message, "/mcp logs "+name)
	require.Contains(t, message, "/mcp restart "+name)
}
