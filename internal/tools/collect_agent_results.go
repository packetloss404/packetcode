package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const collectAgentResultsSchema = `{
  "type": "object",
  "properties": {
    "job_ids": { "type": "array", "description": "Specific background job ids to collect. If omitted, collects jobs in scope.", "items": { "type": "string" } },
    "scope": { "type": "string", "description": "Collection scope: children or descendants. Defaults to children." },
    "timeout_sec": { "type": "integer", "description": "Maximum seconds to wait for running jobs. Default 300, max 600." }
  }
}`

type CollectAgentResultsTool struct {
	Spawner     JobSpawner
	ParentJobID string
	ParentDepth int
}

func NewCollectAgentResultsTool(spawner JobSpawner, parentJobID string, parentDepth int) Tool {
	return &CollectAgentResultsTool{Spawner: spawner, ParentJobID: parentJobID, ParentDepth: parentDepth}
}

func (*CollectAgentResultsTool) Name() string { return "collect_agent_results" }
func (t *CollectAgentResultsTool) RequiresApproval() bool {
	// Foreground collection changes the active model's context, so keep
	// the user's explicit approval gate there. Background parents can
	// collect their own children without interrupting the user.
	return t.ParentJobID == ""
}
func (*CollectAgentResultsTool) Schema() json.RawMessage {
	return json.RawMessage(collectAgentResultsSchema)
}
func (*CollectAgentResultsTool) Description() string {
	return "Collect completed background-agent results by job id or by child scope. Returns compact summaries and artifact manifests; raw logs and diffs are not included."
}

type collectAgentResultsParams struct {
	JobIDs     []string `json:"job_ids,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

func (t *CollectAgentResultsTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t.Spawner == nil {
		return ToolResult{Content: "collect_agent_results: no spawner wired", IsError: true}, nil
	}
	var p collectAgentResultsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ToolResult{}, fmt.Errorf("collect_agent_results: parse params: %w", err)
		}
	}
	timeout := time.Duration(p.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = spawnAgentDefaultWaitTimeout
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	scope := strings.TrimSpace(strings.ToLower(p.Scope))
	if scope == "" {
		scope = "children"
	}
	if scope != "children" && scope != "descendants" {
		return ToolResult{Content: "collect_agent_results: scope must be children or descendants", IsError: true}, nil
	}
	jobIDs := compactJobIDs(p.JobIDs)

	resultCh := make(chan collectOutcome, 1)
	go func() {
		results, ok := t.Spawner.CollectResults(JobCollectRequest{
			ParentJobID: t.ParentJobID,
			ParentDepth: t.ParentDepth,
			JobIDs:      jobIDs,
			Scope:       scope,
			Timeout:     timeout,
		})
		resultCh <- collectOutcome{results: results, ok: ok}
	}()

	select {
	case <-ctx.Done():
		return ToolResult{Content: "collect_agent_results: cancelled: " + ctx.Err().Error(), IsError: true}, nil
	case out := <-resultCh:
		if !out.ok {
			content := "collect_agent_results: no matching jobs found or wait timed out"
			missing := []string(nil)
			if len(jobIDs) > 0 {
				missing = append([]string(nil), jobIDs...)
				content += "\n\nMissing jobs: " + strings.Join(missing, ", ") + " (not found, not authorized, or timed out)"
			}
			return ToolResult{
				Content: content,
				IsError: true,
				Metadata: map[string]any{
					"job_ids":         jobIDs,
					"missing_job_ids": missing,
					"scope":           scope,
				},
			}, nil
		}
		missing := missingRequestedJobIDs(jobIDs, out.results)
		content := renderCollectedResults(out.results)
		if len(missing) > 0 {
			content += "\n\nMissing jobs: " + strings.Join(missing, ", ") + " (not found, not authorized, or timed out)"
		}
		return ToolResult{
			Content: content,
			IsError: anyCollectedError(out.results) || len(missing) > 0,
			Metadata: map[string]any{
				"count":           len(out.results),
				"requested_count": len(jobIDs),
				"missing_job_ids": missing,
				"scope":           scope,
			},
		}, nil
	}
}

type collectOutcome struct {
	results []JobWaitResult
	ok      bool
}

func renderCollectedResults(results []JobWaitResult) string {
	if len(results) == 0 {
		return "No background job results."
	}
	blocks := make([]string, 0, len(results))
	for _, res := range results {
		summary := strings.TrimSpace(res.Summary)
		if summary == "" {
			summary = strings.TrimSpace(res.Error)
		}
		if summary == "" {
			summary = strings.TrimSpace(res.Reason)
		}
		if summary == "" {
			summary = "(no summary)"
		}
		block := fmt.Sprintf("[job:%s — %s] %s", res.JobID, res.State, summary)
		if manifest := jobArtifactManifest(res.Artifacts, 8); manifest != "" {
			block += "\nArtifacts:\n" + manifest
		}
		if wt := waitWorktreeSummary(res); wt != "" {
			block += "\n" + wt
		}
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n\n")
}

func anyCollectedError(results []JobWaitResult) bool {
	for _, res := range results {
		if res.State == "failed" || res.State == "cancelled" {
			return true
		}
	}
	return false
}

func compactJobIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func missingRequestedJobIDs(requested []string, results []JobWaitResult) []string {
	if len(requested) == 0 {
		return nil
	}
	found := map[string]bool{}
	for _, res := range results {
		found[res.JobID] = true
	}
	var missing []string
	for _, id := range requested {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return missing
}
