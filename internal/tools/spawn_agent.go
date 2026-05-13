package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const spawnAgentSchema = `{
  "type": "object",
  "properties": {
    "prompt":   { "type": "string", "description": "The task for the background agent. Be specific and self-contained — the spawned agent has no shared memory with you." },
    "provider": { "type": "string", "description": "Optional provider slug override (openai, anthropic, gemini, minimax, openrouter, ollama). Defaults to the parent's provider." },
    "model":    { "type": "string", "description": "Optional model id override. Defaults to the parent's model." },
    "wait":     { "type": "boolean", "description": "If true, this tool call blocks until the spawned job completes and returns its result inline. If false (default), returns the job id immediately and the result surfaces later." },
    "allow_write": { "type": "boolean", "description": "If true, opt the child job into destructive tools (write_file, patch_file, execute_command). This is independent of wait and may be unavailable from read-only parent jobs." }
  },
  "required": ["prompt"]
}`

// spawnAgentDefaultWaitTimeout is the default timeout applied when the
// tool is invoked with wait=true. The caller's context can still cancel
// the wait earlier (we select on ctx.Done() alongside the timeout).
const spawnAgentDefaultWaitTimeout = 5 * time.Minute

// SpawnAgentTool is the LLM-facing entry point for launching a
// background agent. Autonomy: the foreground model calls this tool
// directly (subject to approval) when it wants to delegate work to a
// parallel sub-agent.
//
// ParentJobID and ParentDepth describe the spawning context. For the
// top-level (main-session) instance they are both zero-valued. When
// the tool is wired into a sub-agent's tool registry (see
// jobs.Manager.buildJobToolRegistry via the SpawnTool factory), the
// factory fills them in with the sub-agent's own job id and depth so
// recursion limits can be enforced server-side.
type SpawnAgentTool struct {
	Spawner     JobSpawner
	ParentJobID string
	ParentDepth int
	// AllowWriteAllowed controls whether this spawning context may create
	// write-enabled children. Main-session spawns may; read-only
	// background jobs may not escalate themselves through a child.
	AllowWriteAllowed bool
	WaitTimeout       time.Duration // zero → spawnAgentDefaultWaitTimeout
}

// NewSpawnAgentTool is the standard constructor for main-session spawns
// (ParentJobID="", ParentDepth=0). Main-session callers may explicitly
// opt children into writes with allow_write=true.
func NewSpawnAgentTool(spawner JobSpawner, parentJobID string, parentDepth int) Tool {
	return &SpawnAgentTool{
		Spawner:           spawner,
		ParentJobID:       parentJobID,
		ParentDepth:       parentDepth,
		AllowWriteAllowed: true,
	}
}

// NewBackgroundSpawnAgentTool constructs a spawn_agent tool for a
// background job. parentAllowWrite decides whether allow_write requests
// are honored; read-only parents can still spawn read-only children.
func NewBackgroundSpawnAgentTool(spawner JobSpawner, parentJobID string, parentDepth int, parentAllowWrite bool) Tool {
	return &SpawnAgentTool{
		Spawner:           spawner,
		ParentJobID:       parentJobID,
		ParentDepth:       parentDepth,
		AllowWriteAllowed: parentAllowWrite,
	}
}

func (*SpawnAgentTool) Name() string            { return "spawn_agent" }
func (*SpawnAgentTool) Schema() json.RawMessage { return json.RawMessage(spawnAgentSchema) }
func (*SpawnAgentTool) Description() string {
	return "Spawn an independent background agent to handle a subtask in parallel. Returns a job id immediately, or with wait=true blocks until the sub-agent completes and returns its summary inline."
}

// RequiresApproval returns true so the user is asked before a parallel
// agent is launched. Trust mode auto-approves.
func (*SpawnAgentTool) RequiresApproval() bool { return true }

type spawnAgentParams struct {
	Prompt     string `json:"prompt"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Wait       bool   `json:"wait,omitempty"`
	AllowWrite bool   `json:"allow_write,omitempty"`
}

func (t *SpawnAgentTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t.Spawner == nil {
		return ToolResult{
			Content: "spawn_agent: no spawner wired",
			IsError: true,
		}, nil
	}
	var p spawnAgentParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ToolResult{}, fmt.Errorf("spawn_agent: parse params: %w", err)
		}
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return ToolResult{
			Content: "spawn_agent: prompt is required",
			IsError: true,
		}, nil
	}

	if p.AllowWrite && !t.AllowWriteAllowed {
		return ToolResult{
			Content: "spawn_agent: allow_write is not available from this read-only background job",
			IsError: true,
			Metadata: map[string]any{
				"error_code": "allow_write_denied",
			},
		}, nil
	}

	req := JobSpawnRequest{
		Prompt:      p.Prompt,
		Provider:    p.Provider,
		Model:       p.Model,
		ParentJobID: t.ParentJobID,
		ParentDepth: t.ParentDepth,
		AllowWrite:  p.AllowWrite,
	}

	snap, spawnErr := t.Spawner.Spawn(req)
	if spawnErr != nil {
		return ToolResult{
			Content: fmt.Sprintf("spawn_agent: spawn failed: %s", spawnErr.Error()),
			IsError: true,
			Metadata: map[string]any{
				"error_code":   spawnErr.Code,
				"error_reason": spawnErr.Reason,
			},
		}, nil
	}

	// Best-effort prov/model label for the echo message; both fields
	// come back populated after resolveProviderModel but guard anyway.
	provLabel := snap.Provider
	if snap.Model != "" {
		if provLabel != "" {
			provLabel += "/" + snap.Model
		} else {
			provLabel = snap.Model
		}
	}

	if !p.Wait {
		content := fmt.Sprintf(
			"Spawned job %s (%s). Result will appear when the job completes.",
			snap.ID, nonEmpty(provLabel, "unknown"),
		)
		return ToolResult{
			Content: content,
			Metadata: map[string]any{
				"job_id":   snap.ID,
				"provider": snap.Provider,
				"model":    snap.Model,
				"waited":   false,
			},
		}, nil
	}

	// wait=true: block on the spawner until the job reaches a terminal
	// state, the parent context is cancelled, or the default timeout
	// elapses — whichever comes first.
	timeout := t.WaitTimeout
	if timeout <= 0 {
		timeout = spawnAgentDefaultWaitTimeout
	}
	return t.waitAndReport(ctx, snap, timeout)
}

// waitAndReport blocks on the spawner until the job terminates, ctx
// cancels, or timeout fires. We race a goroutine that calls WaitForJob
// against ctx.Done(); on ctx cancellation we call Spawner.Cancel to
// cascade the cancellation down to the sub-agent so we don't leave an
// orphan worker running after returning an IsError result to the parent.
func (t *SpawnAgentTool) waitAndReport(ctx context.Context, snap JobSpawnResult, timeout time.Duration) (ToolResult, error) {
	resultCh := make(chan waitOutcome, 1)
	go func() {
		res, ok := t.Spawner.WaitForJob(snap.ID, timeout)
		resultCh <- waitOutcome{res: res, ok: ok}
	}()

	select {
	case <-ctx.Done():
		// Cascade: tell the manager to kill the sub-agent. The racing
		// WaitForJob goroutine will then return promptly as the job
		// flips to StateCancelled and we can safely drop its result.
		t.Spawner.Cancel(snap.ID)
		return ToolResult{
			Content: fmt.Sprintf("spawn_agent: cancelled while waiting on job %s: %s",
				snap.ID, ctx.Err()),
			IsError: true,
			Metadata: map[string]any{
				"job_id":    snap.ID,
				"waited":    true,
				"cancelled": true,
			},
		}, nil
	case out := <-resultCh:
		if !out.ok {
			return ToolResult{
				Content: fmt.Sprintf("spawn_agent: wait timed out after %s for job %s",
					timeout, snap.ID),
				IsError: true,
				Metadata: map[string]any{
					"job_id":    snap.ID,
					"waited":    true,
					"timed_out": true,
				},
			}, nil
		}
		res := out.res
		summary := strings.TrimSpace(res.Summary)
		if summary == "" {
			summary = "(no summary)"
		}
		content := fmt.Sprintf("[job:%s — %s] %s", res.JobID, res.State, summary)
		isError := res.State == "failed" || res.State == "cancelled"
		return ToolResult{
			Content: content,
			IsError: isError,
			Metadata: map[string]any{
				"job_id":        res.JobID,
				"state":         res.State,
				"provider":      res.Provider,
				"model":         res.Model,
				"duration_ms":   res.DurationMS,
				"input_tokens":  res.InputTokens,
				"output_tokens": res.OutputTokens,
				"cost_usd":      res.CostUSD,
				"summary":       summary,
				"waited":        true,
			},
		}, nil
	}
}

// waitOutcome bundles the channel-returned pair from WaitForJob.
type waitOutcome struct {
	res JobWaitResult
	ok  bool
}

// nonEmpty returns v if non-empty, else fallback.
func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
