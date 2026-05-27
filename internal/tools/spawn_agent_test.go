package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSpawner is a channel-driven JobSpawner used to unit-test the
// SpawnAgentTool without booting the real jobs.Manager. Tests configure
// the sequence of Spawn / WaitForJob responses up front and assert on
// the requests the tool issued.
type fakeSpawner struct {
	// spawnResult is returned by Spawn; spawnErr overrides it when non-nil.
	spawnResult JobSpawnResult
	spawnErr    *JobSpawnError

	// waitResult is returned by WaitForJob after waitDelay and only if
	// waitOK is true.
	waitResult JobWaitResult
	waitOK     bool
	waitDelay  time.Duration
	// waitCtx can be wired from the test to let WaitForJob unblock on an
	// external signal (e.g. a close on a test-owned channel) — but for our
	// purposes the delay suffices.

	spawned     atomic.Int32
	waited      atomic.Int32
	collected   atomic.Int32
	cancelled   atomic.Int32
	lastReq     JobSpawnRequest
	lastWait    string
	lastCollect JobCollectRequest
	lastCancel  string

	collectResults []JobWaitResult
	collectOK      bool
}

func (f *fakeSpawner) Cancel(id string) bool {
	f.cancelled.Add(1)
	f.lastCancel = id
	return true
}

func (f *fakeSpawner) Spawn(req JobSpawnRequest) (JobSpawnResult, *JobSpawnError) {
	f.spawned.Add(1)
	f.lastReq = req
	if f.spawnErr != nil {
		return JobSpawnResult{}, f.spawnErr
	}
	return f.spawnResult, nil
}

func (f *fakeSpawner) WaitForJob(id string, timeout time.Duration) (JobWaitResult, bool) {
	f.waited.Add(1)
	f.lastWait = id
	if f.waitDelay > 0 {
		t := time.NewTimer(f.waitDelay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-time.After(timeout):
			return JobWaitResult{}, false
		}
	}
	return f.waitResult, f.waitOK
}

func (f *fakeSpawner) CollectResults(req JobCollectRequest) ([]JobWaitResult, bool) {
	f.collected.Add(1)
	f.lastCollect = req
	return f.collectResults, f.collectOK
}

// TestSpawnAgentTool_NoWait verifies wait=false returns the job id and
// metadata without blocking for completion.

func TestSpawnAgentTool_NoWait(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "abc123", Provider: "gemini", Model: "gemini-2.5-flash", Prompt: "hi"},
	}
	tool := NewSpawnAgentTool(f, "", 0)

	if tool.Name() != "spawn_agent" {
		t.Fatalf("Name() = %q, want spawn_agent", tool.Name())
	}
	if !tool.RequiresApproval() {
		t.Fatalf("RequiresApproval() = false, want true")
	}

	raw := json.RawMessage(`{"prompt":"hi"}`)
	start := time.Now()
	res, err := tool.Execute(context.Background(), raw)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("wait=false should return immediately; took %s", elapsed)
	}
	if res.IsError {
		t.Fatalf("IsError=true unexpected: %+v", res)
	}
	if !strings.Contains(res.Content, "abc123") {
		t.Fatalf("Content %q missing job id", res.Content)
	}
	if gotID, _ := res.Metadata["job_id"].(string); gotID != "abc123" {
		t.Fatalf("metadata job_id = %v, want abc123", res.Metadata["job_id"])
	}
	if gotWaited, _ := res.Metadata["waited"].(bool); gotWaited {
		t.Fatalf("metadata waited = true, want false")
	}
	if f.spawned.Load() != 1 {
		t.Fatalf("spawn called %d times, want 1", f.spawned.Load())
	}
	if f.waited.Load() != 0 {
		t.Fatalf("WaitForJob called %d times, want 0", f.waited.Load())
	}
	if f.lastReq.AllowWrite {
		t.Fatalf("wait=false must not force AllowWrite=true")
	}
}

// TestSpawnAgentTool_Wait_Completed verifies wait=true blocks until the
// spawner's WaitForJob returns and echoes the summary back as tool content.

func TestSpawnAgentTool_Wait_Completed(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "deadbeef", Provider: "gemini", Model: "flash"},
		waitResult: JobWaitResult{
			JobID: "deadbeef", Provider: "gemini", Model: "flash",
			Summary: "found 3 call sites", State: "completed",
			DurationMS: 1234, InputTokens: 100, OutputTokens: 50, CostUSD: 0.001,
			Artifacts: []JobArtifact{{
				ID:      "A1",
				Kind:    "search",
				Summary: "3 match(es) for Authenticate",
			}},
			WorktreeBase: "deadbeefbase",
		},
		waitOK:    true,
		waitDelay: 50 * time.Millisecond,
	}
	tool := NewSpawnAgentTool(f, "parent-01", 1)

	raw := json.RawMessage(`{"prompt":"do the thing","wait":true,"provider":"gemini","model":"flash"}`)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError=true unexpected: %+v", res)
	}
	if !strings.Contains(res.Content, "found 3 call sites") {
		t.Fatalf("Content %q missing summary", res.Content)
	}
	if !strings.Contains(res.Content, "Artifacts:") || !strings.Contains(res.Content, "A1 search") {
		t.Fatalf("Content %q missing artifact manifest", res.Content)
	}
	if got, _ := res.Metadata["state"].(string); got != "completed" {
		t.Fatalf("metadata state = %v, want completed", res.Metadata["state"])
	}
	if got, _ := res.Metadata["waited"].(bool); !got {
		t.Fatalf("metadata waited = false, want true")
	}
	if got, _ := res.Metadata["artifact_count"].(int); got != 1 {
		t.Fatalf("metadata artifact_count = %v, want 1", res.Metadata["artifact_count"])
	}
	if got, _ := res.Metadata["worktree_base"].(string); got != "deadbeefbase" {
		t.Fatalf("metadata worktree_base = %v, want deadbeefbase", res.Metadata["worktree_base"])
	}
	if f.spawned.Load() != 1 || f.waited.Load() != 1 {
		t.Fatalf("spawn=%d wait=%d, want 1 / 1", f.spawned.Load(), f.waited.Load())
	}
	// Waiting is result routing only; write access is an explicit
	// allow_write opt-in.
	if f.lastReq.AllowWrite {
		t.Fatalf("wait=true must not set AllowWrite without allow_write")
	}
	// Parent context propagated into the SpawnRequest.
	if f.lastReq.ParentJobID != "parent-01" || f.lastReq.ParentDepth != 1 {
		t.Fatalf("parent info not propagated: %+v", f.lastReq)
	}
}

func TestSpawnAgentTool_Wait_FailedIncludesError(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "badc0de1", Provider: "openai", Model: "gpt"},
		waitResult: JobWaitResult{
			JobID: "badc0de1", Provider: "openai", Model: "gpt",
			State: "failed", Error: "prepare worktree: git rejected repository ownership",
		},
		waitOK: true,
	}
	tool := NewSpawnAgentTool(f, "", 0)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"edit","wait":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected failed child to return IsError")
	}
	if !strings.Contains(res.Content, "git rejected repository ownership") {
		t.Fatalf("Content %q missing child error", res.Content)
	}
	if got, _ := res.Metadata["error"].(string); got == "" {
		t.Fatalf("metadata error should be present: %#v", res.Metadata)
	}
}

// TestSpawnAgentTool_Wait_Cancelled verifies parent ctx cancellation
// returns an IsError result with "cancelled" text.

func TestSpawnAgentTool_Wait_Cancelled(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "abcd1234", Provider: "gemini", Model: "flash"},
		waitOK:      true,
		waitResult:  JobWaitResult{JobID: "abcd1234", State: "completed", Summary: "never delivered"},
		waitDelay:   5 * time.Second, // long enough that ctx.Done wins
	}
	tool := NewSpawnAgentTool(f, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so Execute gets to launch the waiter.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	raw := json.RawMessage(`{"prompt":"hang","wait":true}`)
	start := time.Now()
	res, err := tool.Execute(ctx, raw)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on cancellation, got %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Content), "cancel") {
		t.Fatalf("Content %q should mention cancellation", res.Content)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancellation path took %s; should return promptly", elapsed)
	}
	// Cascade: the parent's cancellation must have propagated to the
	// spawner so the sub-agent is told to terminate too. Without this,
	// /cancel on a wait=true spawn would leave an orphan worker running.
	if f.cancelled.Load() != 1 {
		t.Fatalf("Spawner.Cancel called %d times, want 1 (cascade)", f.cancelled.Load())
	}
	if f.lastCancel != "abcd1234" {
		t.Fatalf("Spawner.Cancel called with id %q, want abcd1234", f.lastCancel)
	}
}

// TestSpawnAgentTool_Schema verifies the schema is valid JSON and
// carries the required fields.

func TestSpawnAgentTool_Schema(t *testing.T) {
	f := &fakeSpawner{}
	tool := NewSpawnAgentTool(f, "", 0)

	raw := tool.Schema()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	if doc["type"] != "object" {
		t.Fatalf("schema type = %v, want object", doc["type"])
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing properties")
	}
	for _, name := range []string{"prompt", "provider", "model", "wait", "allow_write"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("schema missing property %q", name)
		}
	}
	required, _ := doc["required"].([]any)
	foundPrompt := false
	for _, r := range required {
		if s, _ := r.(string); s == "prompt" {
			foundPrompt = true
		}
	}
	if !foundPrompt {
		t.Fatalf("schema required should include 'prompt': %v", required)
	}
}

func TestSpawnAgentTool_AllowWriteExplicit(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "abc123", Provider: "gemini", Model: "flash"},
	}
	tool := NewSpawnAgentTool(f, "", 0)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"edit","allow_write":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError=true unexpected: %+v", res)
	}
	if !f.lastReq.AllowWrite {
		t.Fatalf("allow_write=true should set AllowWrite on SpawnRequest")
	}
}

func TestBackgroundSpawnAgentTool_ReadOnlyDeniesAllowWrite(t *testing.T) {
	f := &fakeSpawner{
		spawnResult: JobSpawnResult{ID: "abc123", Provider: "gemini", Model: "flash"},
	}
	tool := NewBackgroundSpawnAgentTool(f, "parent", 0, false)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"edit","allow_write":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when read-only parent requests allow_write")
	}
	if f.spawned.Load() != 0 {
		t.Fatalf("spawn called %d times, want 0", f.spawned.Load())
	}
	if !strings.Contains(res.Content, "allow_write") {
		t.Fatalf("Content %q should mention allow_write", res.Content)
	}
}

// ---------------------------------------------------------------------------
// Bonus: Spawn failures propagate as IsError without panicking.
// ---------------------------------------------------------------------------

func TestSpawnAgentTool_SpawnError(t *testing.T) {
	f := &fakeSpawner{spawnErr: &JobSpawnError{Code: "limit_reached", Reason: "max 32"}}
	tool := NewSpawnAgentTool(f, "", 0)

	raw := json.RawMessage(`{"prompt":"x"}`)
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true on spawn failure")
	}
	if !strings.Contains(res.Content, "limit_reached") {
		t.Fatalf("Content %q should mention error code", res.Content)
	}
}

func TestCollectAgentResultsTool_ScopeAndManifest(t *testing.T) {
	f := &fakeSpawner{
		collectOK: true,
		collectResults: []JobWaitResult{{
			JobID:       "child-1",
			ParentJobID: "parent-1",
			State:       "completed",
			Summary:     "updated parser",
			Artifacts: []JobArtifact{{
				ID:      "A1",
				Kind:    "file_change",
				Summary: "wrote internal/parser.go",
				Path:    "internal/parser.go",
			}},
			WorktreePath:   "C:/tmp/wt",
			WorktreeBranch: "packetcode-job-child-1",
			WorktreeBase:   "abc123",
		}},
	}
	tool := NewCollectAgentResultsTool(f, "parent-1", 1)
	if tool.RequiresApproval() {
		t.Fatalf("background collect_agent_results should not require approval")
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"descendants","timeout_sec":1}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError=true unexpected: %+v", res)
	}
	if !strings.Contains(res.Content, "updated parser") || !strings.Contains(res.Content, "A1 file_change") {
		t.Fatalf("Content %q missing result or artifact manifest", res.Content)
	}
	if !strings.Contains(res.Content, "worktree: C:/tmp/wt") {
		t.Fatalf("Content %q missing worktree summary", res.Content)
	}
	if f.lastCollect.ParentJobID != "parent-1" || f.lastCollect.ParentDepth != 1 {
		t.Fatalf("parent context not propagated: %+v", f.lastCollect)
	}
	if f.lastCollect.Scope != "descendants" {
		t.Fatalf("scope = %q, want descendants", f.lastCollect.Scope)
	}
	if f.lastCollect.Timeout != time.Second {
		t.Fatalf("timeout = %s, want 1s", f.lastCollect.Timeout)
	}
	if got, _ := res.Metadata["count"].(int); got != 1 {
		t.Fatalf("metadata count = %v, want 1", res.Metadata["count"])
	}
}

func TestCollectAgentResultsTool_ForegroundRequiresApproval(t *testing.T) {
	tool := NewCollectAgentResultsTool(&fakeSpawner{}, "", 0)
	if !tool.RequiresApproval() {
		t.Fatalf("foreground collect_agent_results should require approval")
	}
}

func TestCollectAgentResultsTool_FailedResultIsError(t *testing.T) {
	f := &fakeSpawner{
		collectOK:      true,
		collectResults: []JobWaitResult{{JobID: "child-2", State: "failed", Error: "tests failed"}},
	}
	tool := NewCollectAgentResultsTool(f, "", 0)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"job_ids":["child-2"]}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("failed child should make collection IsError")
	}
	if !strings.Contains(res.Content, "tests failed") {
		t.Fatalf("Content %q missing child error", res.Content)
	}
	if len(f.lastCollect.JobIDs) != 1 || f.lastCollect.JobIDs[0] != "child-2" {
		t.Fatalf("job_ids not propagated: %+v", f.lastCollect.JobIDs)
	}
}

func TestCollectAgentResultsTool_PartialExplicitCollectionReportsMissing(t *testing.T) {
	f := &fakeSpawner{
		collectOK:      true,
		collectResults: []JobWaitResult{{JobID: "child-1", State: "completed", Summary: "done"}},
	}
	tool := NewCollectAgentResultsTool(f, "", 0)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"job_ids":["child-1","child-2"]}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("partial explicit collection should be IsError")
	}
	if !strings.Contains(res.Content, "Missing jobs: child-2") {
		t.Fatalf("Content %q missing partial collection warning", res.Content)
	}
	missing, _ := res.Metadata["missing_job_ids"].([]string)
	if len(missing) != 1 || missing[0] != "child-2" {
		t.Fatalf("missing_job_ids = %#v, want child-2", res.Metadata["missing_job_ids"])
	}
}
