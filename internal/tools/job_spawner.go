package tools

import "time"

// JobSpawner is the narrow contract the spawn_agent tool needs. It lives
// in internal/tools (not internal/jobs) to avoid an import cycle: the
// jobs package already imports tools (to build per-job registries), so
// tools cannot import jobs back.
//
// *jobs.Manager satisfies this interface via a tiny adapter (see
// internal/jobs/spawner_adapter.go). Tests that want to drive the tool
// in isolation provide their own fake implementation.
//
// All types referenced by JobSpawner are mirror structs defined below.
// Their fields mirror the native jobs.* types one-for-one so the
// adapter translation is mechanical.
type JobSpawner interface {
	// Spawn enqueues a new background job. On success returns a
	// JobSpawnResult describing the queued state (notably Result.ID).
	// On failure returns a non-nil *JobSpawnError.
	Spawn(req JobSpawnRequest) (JobSpawnResult, *JobSpawnError)

	// WaitForJob blocks until the named job reaches a terminal state
	// or timeout elapses. ok=false when the job is unknown or the
	// timeout fires first.
	WaitForJob(id string, timeout time.Duration) (JobWaitResult, bool)

	// Cancel signals the named job to terminate. Returns true if a
	// cancellation was dispatched, false if the job is unknown or
	// already in a terminal state. Used by spawn_agent (wait=true) to
	// cascade parent-context cancellation down to the sub-agent so a
	// cancelled wait doesn't leave an orphan worker running.
	Cancel(id string) bool
}

// JobSpawnRequest mirrors jobs.SpawnRequest.
type JobSpawnRequest struct {
	Prompt       string
	ParentJobID  string
	ParentDepth  int
	Provider     string
	Model        string
	SystemPrompt string
	AllowWrite   bool
}

// JobSpawnResult mirrors jobs.Snapshot (the success return of Spawn).
// Only the fields the spawn_agent tool actually consumes are mirrored;
// callers that need more can extend this struct alongside the adapter.
type JobSpawnResult struct {
	ID             string
	Provider       string
	Model          string
	Prompt         string
	Depth          int
	WorktreePath   string
	WorktreeBranch string
}

// JobSpawnError mirrors jobs.SpawnError.
type JobSpawnError struct {
	Code   string
	Reason string
}

func (e *JobSpawnError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return e.Code
	}
	return e.Code + ": " + e.Reason
}

// JobWaitResult mirrors jobs.Result. State is carried as a string label
// (e.g. "completed", "failed", "cancelled") so the tools package doesn't
// have to re-declare the State enum.
type JobWaitResult struct {
	JobID          string
	Provider       string
	Model          string
	Summary        string
	State          string
	DurationMS     int64
	InputTokens    int
	OutputTokens   int
	CostUSD        float64
	WorktreePath   string
	WorktreeBranch string
}
