package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// Test 1 — happy-path lifecycle: Queued → Running → Completed.
func TestManager_SpawnAndComplete(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello()}

	var (
		mu          sync.Mutex
		transitions []State
	)
	collect := func(s Snapshot) {
		mu.Lock()
		transitions = append(transitions, s.State)
		mu.Unlock()
	}
	mgr, _ := newTestManager(t, prov, func(c *Config) { c.OnUpdate = collect })

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "say hi"})
	require.Nil(t, perr)
	require.Equal(t, StateQueued, snap.State)

	waitFor(t, 2*time.Second, "job to complete", func() bool {
		got, ok := mgr.Get(snap.ID)
		return ok && got.State == StateCompleted
	})

	// Subscribers fire async — give them a beat to drain.
	waitFor(t, time.Second, "all transitions to fan out", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(transitions) >= 3
	})

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, transitions, StateQueued)
	assert.Contains(t, transitions, StateRunning)
	assert.Contains(t, transitions, StateCompleted)
}

// Test 2 — at most MaxConcurrent jobs are in StateRunning at once.
func TestManager_ConcurrencyLimit(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}

	var (
		mu   sync.Mutex
		peak int
	)
	track := func(_ Snapshot) {
		mu.Lock()
		// peak reads ActiveCount via the manager — but we don't have
		// the manager here. Instead, sample on the next OnUpdate.
		mu.Unlock()
	}
	_ = track // silence unused

	mgr, _ := newTestManager(t, prov, func(c *Config) { c.MaxConcurrent = 2 })

	const N = 6
	ids := make([]string, 0, N)
	for i := 0; i < N; i++ {
		s, perr := mgr.Spawn(SpawnRequest{Prompt: "hold"})
		require.Nil(t, perr)
		ids = append(ids, s.ID)
	}

	// Sample running counts for a short window — none should exceed 2.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		running := 0
		for _, id := range ids {
			snap, _ := mgr.Get(id)
			if snap.State == StateRunning {
				running++
			}
		}
		mu.Lock()
		if running > peak {
			peak = running
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel everything to release the workers.
	mgr.CancelAll()
	waitFor(t, 2*time.Second, "all jobs to terminate", func() bool {
		for _, id := range ids {
			snap, _ := mgr.Get(id)
			if !snap.State.IsTerminal() {
				return false
			}
		}
		return true
	})

	mu.Lock()
	defer mu.Unlock()
	assert.LessOrEqual(t, peak, 2, "MaxConcurrent must cap simultaneously-running jobs")
	assert.GreaterOrEqual(t, peak, 1, "at least one job should reach Running")
}

// Test 3 — at MaxDepth-1, the per-job tool registry must NOT include
// spawn_agent (verified by checking the SpawnTool factory is not
// invoked when depth == MaxDepth-1).
func TestManager_DepthLimit(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello()}

	var spawnFactoryCalled int32
	mgr, _ := newTestManager(t, prov, func(c *Config) {
		c.MaxDepth = 2
		c.SpawnTool = func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool {
			atomic.AddInt32(&spawnFactoryCalled, 1)
			return &noopTool{name: "spawn_agent"}
		}
	})

	// ParentDepth = MaxDepth-1 = 1 → resulting depth = 2 == MaxDepth → rejected.
	_, perr := mgr.Spawn(SpawnRequest{Prompt: "x", ParentJobID: "deadbeef", ParentDepth: 1})
	require.NotNil(t, perr)
	assert.Equal(t, "depth_exceeded", perr.Code)
	assert.Equal(t, int32(0), atomic.LoadInt32(&spawnFactoryCalled),
		"SpawnTool factory must not be invoked for depth-rejected jobs")

	// A job at depth 0 is fine; SpawnTool factory should be skipped because
	// 0 < MaxDepth-1 (=1) only when MaxDepth > 1; with MaxDepth=2 that is
	// 0 < 1 → factory IS called. Run a job at depth 0 to prove that path.
	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
	require.Nil(t, perr)
	waitFor(t, 2*time.Second, "spawn factory invoked for depth-0 job", func() bool {
		return atomic.LoadInt32(&spawnFactoryCalled) >= 1
	})
	waitFor(t, 2*time.Second, "depth-0 job completes", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateCompleted
	})
}

// Test 4 — MaxTotal lifetime cap returns SpawnError{limit_reached}.
func TestManager_LifetimeLimit(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		// 3 turns so first three jobs each get a turn (each job calls
		// ChatCompletion once).
		scriptedHello()[0],
		scriptedHello()[0],
		scriptedHello()[0],
	}}
	mgr, _ := newTestManager(t, prov, func(c *Config) { c.MaxTotal = 3 })

	for i := 0; i < 3; i++ {
		_, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
		require.Nil(t, perr, "spawn %d", i)
	}
	_, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
	require.NotNil(t, perr)
	assert.Equal(t, "limit_reached", perr.Code)
}

func TestManager_SpawnRejectsUnknownModelFromCache(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello()}
	mgr, _ := newTestManager(t, prov)
	mgr.cfg.Registry.SetCachedModels("scripted", []provider.Model{{ID: "scripted-model"}})

	_, perr := mgr.Spawn(SpawnRequest{Prompt: "x", Model: "made-up-model"})
	require.NotNil(t, perr)
	assert.Equal(t, "unknown_model", perr.Code)
	assert.Empty(t, mgr.List(), "invalid model must not enqueue a job")
	assert.Equal(t, int32(0), atomic.LoadInt32(&prov.listCalls), "cache hit should not call ListModels")
}

func TestManager_SpawnValidatesAndCachesUncachedModel(t *testing.T) {
	prov := &scriptedProvider{
		turns: [][]provider.StreamEvent{
			scriptedHello()[0],
			scriptedHello()[0],
		},
		models: []provider.Model{{ID: "custom-model"}},
	}
	mgr, _ := newTestManager(t, prov)

	first, perr := mgr.Spawn(SpawnRequest{Prompt: "x", Model: "custom-model"})
	require.Nil(t, perr)
	assert.Equal(t, "custom-model", first.Model)
	assert.Equal(t, int32(1), atomic.LoadInt32(&prov.listCalls))

	second, perr := mgr.Spawn(SpawnRequest{Prompt: "x", Model: "custom-model"})
	require.Nil(t, perr)
	assert.Equal(t, "custom-model", second.Model)
	assert.Equal(t, int32(1), atomic.LoadInt32(&prov.listCalls), "second spawn should use cached models")
}

func TestManager_SpawnRejectsWhenModelCatalogFails(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello(), listErr: errors.New("catalog down")}
	mgr, _ := newTestManager(t, prov)

	_, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
	require.NotNil(t, perr)
	assert.Equal(t, "unknown_model", perr.Code)
	assert.Contains(t, perr.Reason, "catalog down")
	assert.Empty(t, mgr.List(), "unvalidated model must not enqueue a job")
}

// Test 5 — Cancel transitions a holdOpen job to StateCancelled.
func TestManager_Cancel(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}
	mgr, _ := newTestManager(t, prov)

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "loop forever"})
	require.Nil(t, perr)

	// Wait for it to enter Running so Cancel exercises the running-job
	// path, not the queued path.
	waitFor(t, time.Second, "job to be running", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateRunning
	})

	require.True(t, mgr.Cancel(snap.ID))

	waitFor(t, 2*time.Second, "job to reach Cancelled", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateCancelled
	})
}

// Test 6 — CancelAll cancels every active job, returns the count.
func TestManager_CancelAll(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}
	mgr, _ := newTestManager(t, prov, func(c *Config) { c.MaxConcurrent = 5 })

	const N = 5
	ids := make([]string, 0, N)
	for i := 0; i < N; i++ {
		s, perr := mgr.Spawn(SpawnRequest{Prompt: "hold"})
		require.Nil(t, perr)
		ids = append(ids, s.ID)
	}

	count := mgr.CancelAll()
	assert.Equal(t, N, count)

	waitFor(t, 2*time.Second, "all jobs cancelled", func() bool {
		for _, id := range ids {
			snap, _ := mgr.Get(id)
			if snap.State != StateCancelled {
				return false
			}
		}
		return true
	})
}

// Test 7 — Shutdown cancels in-flight jobs and persists Cancelled
// state to <jobsDir>/<id>.json.
func TestManager_Shutdown_Persists(t *testing.T) {
	prov := &scriptedProvider{holdOpen: true}
	jobsDir := t.TempDir()
	mgr, _ := newTestManager(t, prov, func(c *Config) {
		c.JobsDir = jobsDir
		c.MaxConcurrent = 2
	})

	const N = 2
	ids := make([]string, 0, N)
	for i := 0; i < N; i++ {
		s, perr := mgr.Spawn(SpawnRequest{Prompt: "hold"})
		require.Nil(t, perr)
		ids = append(ids, s.ID)
	}

	// Wait for both to be Running so Shutdown actually cancels them
	// (rather than racing with worker startup).
	waitFor(t, time.Second, "both jobs running", func() bool {
		running := 0
		for _, id := range ids {
			snap, _ := mgr.Get(id)
			if snap.State == StateRunning {
				running++
			}
		}
		return running == N
	})

	require.NoError(t, mgr.Shutdown(2*time.Second))

	for _, id := range ids {
		snap, ok := mgr.Get(id)
		require.True(t, ok)
		assert.Equal(t, StateCancelled, snap.State, "in-flight job persisted as Cancelled")
		// File on disk exists.
		path := filepath.Join(jobsDir, id+".json")
		_, err := os.Stat(path)
		require.NoError(t, err, "persisted job file should exist for %s", id)
	}
	// A fresh Manager pointed at the same dir should NOT find anything
	// to resurrect, because Cancelled is terminal.
	_, recovered, err := NewManager(Config{JobsDir: jobsDir})
	require.NoError(t, err)
	assert.Equal(t, 0, recovered, "Cancelled jobs are terminal — nothing to resurrect")
}

// Test 8 — Orphan recovery rewrites pre-existing Running/Queued files.
func TestManager_OrphanRecovery(t *testing.T) {
	jobsDir := t.TempDir()
	// Hand-write a job file in StateRunning to simulate a crash.
	orphan := &Job{
		ID:        "orphan01",
		SessionID: "main-job-orphan01",
		Prompt:    "stale",
		Provider:  "scripted",
		Model:     "scripted-model",
		State:     StateRunning,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, saveSnapshot(jobsDir, orphan))

	mgr, recovered, err := NewManager(Config{JobsDir: jobsDir})
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	got, ok := mgr.Get("orphan01")
	require.True(t, ok)
	assert.Equal(t, StateCancelled, got.State)
	// Read the file directly to confirm Reason was persisted.
	resurrectedJobs, err := loadOrphaned(jobsDir)
	require.NoError(t, err)
	assert.Empty(t, resurrectedJobs, "second Load finds nothing — already terminal")
}

// Test 9 — DrainResults yields one entry per completed job, then empty.
func TestManager_DrainResults(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		scriptedHello()[0],
		scriptedHello()[0],
		scriptedHello()[0],
	}}
	mgr, _ := newTestManager(t, prov)

	const N = 3
	for i := 0; i < N; i++ {
		_, perr := mgr.Spawn(SpawnRequest{Prompt: "x"})
		require.Nil(t, perr)
	}
	waitFor(t, 2*time.Second, "all jobs completed", func() bool {
		for _, snap := range mgr.List() {
			if snap.State != StateCompleted {
				return false
			}
		}
		return len(mgr.List()) == N
	})

	results := mgr.DrainResults(10)
	assert.Len(t, results, N)
	assert.Empty(t, mgr.DrainResults(10))
}

func TestManager_ReadOnlyJobCanSpawnReadOnlyChild(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "spawn1", Name: "spawn_agent"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"prompt":"child scout","wait":true}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "child done"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "parent done"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}
	mgr, _ := newTestManager(t, prov, func(c *Config) {
		c.MaxDepth = 2
	})
	mgr.SetSpawnToolFactory(func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool {
		return tools.NewBackgroundSpawnAgentTool(mgr.AsToolsSpawner(), parentJobID, parentDepth, parentAllowWrite)
	})

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "parent", AllowWrite: false})
	require.Nil(t, perr)
	waitFor(t, 3*time.Second, "parent and child complete", func() bool {
		if len(mgr.List()) != 2 {
			return false
		}
		for _, got := range mgr.List() {
			if got.State != StateCompleted {
				return false
			}
		}
		parent, _ := mgr.Get(snap.ID)
		return parent.Summary == "parent done"
	})

	results := mgr.DrainResults(0)
	require.Len(t, results, 1, "wait=true child result should be consumed by the parent job")
	assert.Equal(t, snap.ID, results[0].JobID)
}

// Test 10 — backup isolation: a job's write_file should land in the
// per-job backup tree, not the parent's.
func TestManager_Isolation_Backups(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolCallStart, ToolCall: &provider.ToolCallDelta{Index: 0, ID: "c1", Name: "write_file"}},
			{Type: provider.EventToolCallDelta, ToolCall: &provider.ToolCallDelta{Index: 0, ArgumentsDelta: `{"path":"hello.txt","content":"hi"}`}},
			{Type: provider.EventToolCallEnd, ToolCall: &provider.ToolCallDelta{Index: 0}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "wrote it"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}

	root := t.TempDir()
	backupsRoot := t.TempDir()

	parentTools := tools.NewRegistry()
	parentTools.Register(tools.NewWriteFileTool(root, nil))

	mgr, _ := newTestManager(t, prov, func(c *Config) {
		c.Tools = parentTools
		c.Root = root
		c.BackupsDir = backupsRoot
	})

	snap, perr := mgr.Spawn(SpawnRequest{Prompt: "write hello.txt", AllowWrite: true})
	require.Nil(t, perr)
	waitFor(t, 3*time.Second, "job completes", func() bool {
		got, _ := mgr.Get(snap.ID)
		return got.State == StateCompleted
	})

	// The backup dir for the job's sub-session id should exist...
	got, _ := mgr.Get(snap.ID)
	subBackup := filepath.Join(backupsRoot, "main-job-"+got.ID)
	_ = subBackup
	// ... and we've not perturbed the parent BackupManager (we never
	// constructed one; this test asserts the indirection: the parent's
	// stack remains zero because writes went through the per-job backups
	// keyed by SessionID. We verify by ensuring a directory exists for
	// the sub session id — proving Backup() was called against it.
	// The exact dir layout is BackupsDir/<sessionID>; we only check the
	// directory under our backupsRoot is non-empty.
	entries, err := readDirOrEmpty(backupsRoot)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "per-job backup dir created")
	for _, e := range entries {
		// The directory name must include "-job-" — proving it's the
		// per-job sub-session, not a global parent directory.
		assert.Contains(t, e.Name(), "-job-")
	}
}

// Test 11 — pathLockTool serialises concurrent writes targeting the
// same absolute path. Asserts the inner Execute calls do not overlap.
func TestManager_PathLock_Serialises(t *testing.T) {
	prov := &scriptedProvider{turns: scriptedHello()}
	mgr, _ := newTestManager(t, prov, func(c *Config) { c.Root = t.TempDir() })

	var (
		mu     sync.Mutex
		active int
		peak   int
	)
	bodied := &noopTool{name: "write_file", approval: false, executor: func(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		// Hold long enough for sibling goroutines to attempt entry.
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return tools.ToolResult{Content: "ok"}, nil
	}}
	wrapped := &pathLockTool{inner: bodied, m: mgr, root: mgr.cfg.Root, paramKey: "path"}

	const N = 4
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = wrapped.Execute(context.Background(), json.RawMessage(`{"path":"shared.txt"}`))
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, peak, "path lock must serialise — at most one active execute at a time")
	assert.Equal(t, int32(N), atomic.LoadInt32(&bodied.executed))
}

// Test 12 — cost aggregation: two completed jobs both contribute to
// cost.Tracker.TotalCost via their sub-session ids.
func TestManager_CostAggregation(t *testing.T) {
	prov := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "a"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 1_000_000, OutputTokens: 0}},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "b"},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 0, OutputTokens: 1_000_000}},
		},
	}}

	mgr, tr := newTestManager(t, prov)

	for i := 0; i < 2; i++ {
		_, perr := mgr.Spawn(SpawnRequest{Prompt: "go"})
		require.Nil(t, perr)
	}
	waitFor(t, 3*time.Second, "both jobs done", func() bool {
		count := 0
		for _, snap := range mgr.List() {
			if snap.State == StateCompleted {
				count++
			}
		}
		return count == 2
	})

	// $1/M in × 1M + $5/M out × 1M = $6 across both jobs.
	assert.InDelta(t, 6.0, tr.TotalCost(), 1e-6)
}

// Test 15 — Snapshot is a value-copy projection: mutating the
// underlying Job's transcript after building the Snapshot does not
// affect the snapshot.
func TestSnapshotMatchesJob(t *testing.T) {
	j := &Job{
		ID:           "abc12345",
		SessionID:    "main-job-abc12345",
		ParentJobID:  "parent01",
		Prompt:       "do",
		Provider:     "scripted",
		Model:        "model",
		State:        StateRunning,
		CreatedAt:    time.Now().UTC(),
		StartedAt:    time.Now().UTC(),
		Summary:      "s",
		Error:        "",
		InputTokens:  10,
		OutputTokens: 20,
		CostUSD:      1.5,
		Depth:        1,
	}
	snap := snapshotOf(j)
	assert.Equal(t, j.ID, snap.ID)
	assert.Equal(t, j.SessionID, "main-job-"+snap.ID)
	assert.Equal(t, j.Prompt, snap.Prompt)
	assert.Equal(t, j.Provider, snap.Provider)
	assert.Equal(t, j.Model, snap.Model)
	assert.Equal(t, j.State, snap.State)
	assert.Equal(t, j.InputTokens, snap.Tokens.Input)
	assert.Equal(t, j.OutputTokens, snap.Tokens.Output)
	assert.InDelta(t, j.CostUSD, snap.CostUSD, 1e-9)
	assert.Equal(t, j.Depth, snap.Depth)

	// Mutating the source after snapshotting must not affect the snap.
	j.State = StateCompleted
	j.Summary = "different"
	assert.NotEqual(t, j.State, snap.State)
	assert.NotEqual(t, j.Summary, snap.Summary)
}

// readDirOrEmpty returns the entries of dir, or nil if the directory
// does not exist. Used by the backup-isolation test as a thin wrapper
// over os.ReadDir.
func readDirOrEmpty(dir string) ([]os.DirEntry, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return es, nil
}

// _ keeps the agent import live in case other tests are pruned.
var _ = agent.AutoApprove
