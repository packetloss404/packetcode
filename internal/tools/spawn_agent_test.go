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

	spawned    atomic.Int32
	waited     atomic.Int32
	cancelled  atomic.Int32
	lastReq    JobSpawnRequest
	lastWait   string
	lastCancel string
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
	if got, _ := res.Metadata["state"].(string); got != "completed" {
		t.Fatalf("metadata state = %v, want completed", res.Metadata["state"])
	}
	if got, _ := res.Metadata["waited"].(bool); !got {
		t.Fatalf("metadata waited = false, want true")
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
