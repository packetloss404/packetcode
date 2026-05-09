package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

const spawnModelValidationTimeout = 5 * time.Second

// Config bundles the dependencies a Manager needs. Most are shared with
// the main App (Registry, Tools, MainSessions, CostTracker, Approver) so
// background jobs see the same provider/tool surface and feed cost
// totals into the same Tracker.
type Config struct {
	// Registry is the main provider registry. Each job gets a
	// derived single-provider registry built from the resolved
	// (provider, model) pair so hot-switching the main session does
	// not retroactively affect a running job.
	Registry *provider.Registry

	// Tools is the main tool registry. The Manager clones it per job
	// (see buildJobToolRegistry); the source instances are never
	// mutated.
	Tools *tools.Registry

	// MainSessions provides the parent session id used to derive the
	// sub-session id (mainID + "-job-" + shortID). Optional: nil-safe,
	// falls back to "main".
	MainSessions *session.Manager

	// SessionsDir / BackupsDir / JobsDir are the per-tree state dirs.
	// Each job creates its own session.Manager rooted at SessionsDir.
	SessionsDir string
	BackupsDir  string
	JobsDir     string

	// CostTracker is shared with the main App; jobs key into it under
	// their own sub-session ids so totals naturally aggregate.
	CostTracker *cost.Tracker

	// PricingFor looks up (input/M, output/M) for a (provider, model)
	// pair. Used when applying per-stream usage updates to the
	// per-job session record.
	PricingFor cost.PricingFunc

	// SystemPromptFor builds the per-job system prompt given the
	// parent's depth. Returning "" yields no system prompt.
	SystemPromptFor func(parentDepth int) string

	// Caps. Zero values fall back to spec defaults.
	MaxConcurrent int // default 4
	MaxDepth      int // default 2
	MaxTotal      int // default 32

	// DefaultProvider/Model override the main registry's Active() when
	// the SpawnRequest doesn't specify its own. Either-or-both may be
	// empty.
	DefaultProvider string
	DefaultModel    string

	// Approver is the main session's Approver. The per-job adapter
	// (jobApprover) gates destructive tools through it.
	Approver agent.Approver

	// Hooks is shared with the foreground agent so background jobs obey
	// the same lifecycle hook configuration.
	Hooks *hooks.Runner

	// Root is the project root (typically the working directory). Used
	// to instantiate per-job tool clones so they target the right
	// codebase.
	Root string

	// OnUpdate fires asynchronously on every state transition. The
	// Manager invokes it from a separate goroutine so a slow
	// subscriber can't block the worker.
	OnUpdate func(Snapshot)

	// SpawnTool is the optional factory Bucket B uses to plug a
	// spawn_agent tool into each per-job tool registry without
	// creating an import cycle (jobs ↔ tools). Returning nil omits
	// the tool. The factory is called only for jobs whose depth is
	// strictly less than MaxDepth-1, since deeper spawns would breach
	// the recursion cap.
	SpawnTool func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool
}

// SpawnRequest is the input to Manager.Spawn. ParentJobID/ParentDepth
// describe the spawning context; AllowWrite opts the new job into
// destructive tools (write_file/patch_file/execute_command).
type SpawnRequest struct {
	Prompt       string
	ParentJobID  string
	ParentDepth  int
	Provider     string
	Model        string
	SystemPrompt string
	AllowWrite   bool
}

// SpawnError discriminates programmatic Spawn failures so the caller
// (slash command handler, spawn_agent tool) can render the right
// message. Code is one of:
//   - "limit_reached"     — MaxTotal lifetime cap hit
//   - "depth_exceeded"    — request would exceed MaxDepth
//   - "unknown_provider"  — provider slug not registered
//   - "unknown_model"     — model id not exposed by provider
//   - "no_provider"       — neither request nor defaults nor Active()
//     yields a (provider, model) pair
//   - "manager_closed"    — Spawn called after Shutdown
type SpawnError struct {
	Code   string
	Reason string
}

func (e *SpawnError) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return e.Code + ": " + e.Reason
}

// Result is the post-completion summary the spawn_agent tool surfaces
// back to its parent and that DrainResults yields to the App for inline
// notification.
type Result struct {
	JobID        string
	Provider     string
	Model        string
	Summary      string
	State        State
	DurationMS   int64
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Manager owns the lifecycle of every background job. Construction is
// per-app-instance — there is no global state.
type Manager struct {
	cfg Config

	mu           sync.RWMutex
	jobs         map[string]*Job
	cancel       map[string]context.CancelFunc
	results      []Result
	subscribers  []func(Snapshot)
	pathLocks    pathLockMap
	closed       bool
	totalSpawned int

	// sem bounds concurrent runJob workers to MaxConcurrent.
	sem chan struct{}

	// baseCtx is the parent context for every per-job ctx. Cancelling
	// it (via Shutdown) propagates to every running job.
	baseCtx    context.Context
	cancelBase context.CancelFunc

	// wg tracks live worker goroutines so Shutdown can wait.
	wg sync.WaitGroup

	// terminal signals that a specific job has reached a terminal
	// state. WaitForJob blocks on this. Keyed by job id; a goroutine
	// closes the channel on terminal transition.
	terminalCh map[string]chan struct{}

	// resultIDs tracks which results have already been drained so we
	// can keep a stable ordering even after multiple Drain calls.
	// (results slice itself is the queue; we just track length.)
}

// NewManager constructs a Manager with sane fallbacks for unset Config
// fields. It also runs orphan recovery against cfg.JobsDir so that any
// jobs left in non-terminal states by a previous app exit are rewritten
// as Cancelled and surfaced via List(). The recovered count is returned
// alongside any error encountered while reading existing job files.
func NewManager(cfg Config) (*Manager, int, error) {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = 2
	}
	if cfg.MaxTotal <= 0 {
		cfg.MaxTotal = 32
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	m := &Manager{
		cfg:        cfg,
		jobs:       map[string]*Job{},
		cancel:     map[string]context.CancelFunc{},
		pathLocks:  pathLockMap{},
		sem:        make(chan struct{}, cfg.MaxConcurrent),
		baseCtx:    baseCtx,
		cancelBase: cancelBase,
		terminalCh: map[string]chan struct{}{},
	}
	if cfg.OnUpdate != nil {
		m.subscribers = append(m.subscribers, cfg.OnUpdate)
	}
	recovered, err := loadOrphaned(cfg.JobsDir)
	if err != nil {
		// Non-fatal: caller may continue with an empty job list.
		return m, 0, err
	}
	for _, j := range recovered {
		m.jobs[j.ID] = j
	}
	return m, len(recovered), nil
}

// Subscribe registers an additional snapshot callback. Used by the App
// to attach UI fan-out after the Manager is constructed (e.g. to plumb
// into tea.Program.Send). Callbacks run in their own goroutine.
func (m *Manager) Subscribe(fn func(Snapshot)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	m.subscribers = append(m.subscribers, fn)
	m.mu.Unlock()
}

// SetSpawnToolFactory installs (or replaces) the SpawnTool factory
// after Manager construction. This is what Bucket B uses to close the
// chicken-and-egg between "the factory needs *Manager" and "the config
// is passed by value into NewManager". Safe to call before any jobs
// have been spawned; calling it concurrently with running workers is
// safe but may affect subsequent job boots only.
func (m *Manager) SetSpawnToolFactory(fn func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool) {
	m.mu.Lock()
	m.cfg.SpawnTool = fn
	m.mu.Unlock()
}

// SetApprover installs (or replaces) the per-job parent Approver after
// Manager construction. Bucket B uses this when the App constructs its
// uiApprover internally — main.go wires that approver back into the
// Manager after app.New returns. As with SetSpawnToolFactory, the new
// value takes effect on subsequent job boots.
func (m *Manager) SetApprover(a agent.Approver) {
	m.mu.Lock()
	m.cfg.Approver = a
	m.mu.Unlock()
}

// ActiveCount returns the number of jobs in StateQueued or StateRunning.
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, j := range m.jobs {
		if j.State == StateQueued || j.State == StateRunning {
			n++
		}
	}
	return n
}

// Get returns a snapshot of the named job. ok=false when id is unknown.
func (m *Manager) Get(id string) (Snapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return Snapshot{}, false
	}
	return snapshotOf(j), true
}

// List returns snapshots of every known job sorted newest-first.
func (m *Manager) List() []Snapshot {
	m.mu.RLock()
	out := make([]Snapshot, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, snapshotOf(j))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Transcript returns the message history captured for a job. For jobs
// in a terminal state the slice is the snapshot taken at completion.
// For running jobs it returns the live-so-far history copied at call
// time.
func (m *Manager) Transcript(id string) ([]provider.Message, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	out := make([]provider.Message, len(j.Transcript))
	copy(out, j.Transcript)
	return out, true
}

// Cancel signals a single job. Returns false if the id is unknown or
// the job is already terminal.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || j.State.IsTerminal() {
		m.mu.Unlock()
		return false
	}
	cancel, ok := m.cancel[id]
	m.mu.Unlock()
	if !ok || cancel == nil {
		return false
	}
	cancel()
	return true
}

// CancelAll cancels every running or queued job. Returns the number of
// cancel signals sent. Each Job has its cancel func registered at
// Spawn time (not after sem acquire), so this count includes queued
// jobs whose workers haven't yet started.
func (m *Manager) CancelAll() int {
	m.mu.Lock()
	cancellers := make([]context.CancelFunc, 0, len(m.cancel))
	for id, j := range m.jobs {
		if j.State.IsTerminal() {
			continue
		}
		if c, ok := m.cancel[id]; ok {
			cancellers = append(cancellers, c)
		}
	}
	m.mu.Unlock()
	for _, c := range cancellers {
		c()
	}
	return len(cancellers)
}

// Shutdown cancels every active job and waits up to timeout for the
// workers to exit. Returns an error if any worker is still running when
// the timeout elapses (the manager still considers itself closed).
func (m *Manager) Shutdown(timeout time.Duration) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	m.CancelAll()
	m.cancelBase()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("jobs.Shutdown: %d workers still running after %s", m.activeWorkerCount(), timeout)
	}
}

func (m *Manager) activeWorkerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, j := range m.jobs {
		if !j.State.IsTerminal() {
			n++
		}
	}
	return n
}

// DrainResults removes up to n entries from the internal results queue
// and returns them oldest-first. The App calls this at the start of
// each main-session turn to inject background results as RoleUser
// messages. Passing n <= 0 drains everything.
func (m *Manager) DrainResults(n int) []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.results) == 0 {
		return nil
	}
	if n <= 0 || n > len(m.results) {
		n = len(m.results)
	}
	out := make([]Result, n)
	copy(out, m.results[:n])
	m.results = m.results[n:]
	return out
}

// WaitForJob blocks until the named job reaches a terminal state or
// timeout elapses. Returns ok=false if the id is unknown or the timeout
// fires before the job completes. The returned Result is consumed from
// the pending results queue so a waited child is not delivered again by
// DrainResults.
func (m *Manager) WaitForJob(id string, timeout time.Duration) (Result, bool) {
	m.mu.Lock()
	j, exists := m.jobs[id]
	if !exists {
		m.mu.Unlock()
		return Result{}, false
	}
	if j.State.IsTerminal() {
		out := m.consumeResultLocked(j)
		m.mu.Unlock()
		return out, true
	}
	ch, ok := m.terminalCh[id]
	m.mu.Unlock()
	if !ok {
		// No channel set up — job is queued but worker hasn't
		// initialised it yet. Poll briefly via a tiny ticker so we
		// don't busy-loop. This branch is rare in practice (Spawn
		// allocates the channel before returning).
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		for {
			select {
			case <-ticker.C:
				m.mu.RLock()
				j2, stillExists := m.jobs[id]
				if !stillExists {
					m.mu.RUnlock()
					return Result{}, false
				}
				if j2.State.IsTerminal() {
					m.mu.RUnlock()
					m.mu.Lock()
					j3 := m.jobs[id]
					if j3 == nil {
						m.mu.Unlock()
						return Result{}, false
					}
					out := m.consumeResultLocked(j3)
					m.mu.Unlock()
					return out, true
				}
				m.mu.RUnlock()
			case <-deadline.C:
				return Result{}, false
			}
		}
	}
	select {
	case <-ch:
		m.mu.Lock()
		j2 := m.jobs[id]
		if j2 == nil {
			m.mu.Unlock()
			return Result{}, false
		}
		out := m.consumeResultLocked(j2)
		m.mu.Unlock()
		return out, true
	case <-time.After(timeout):
		return Result{}, false
	}
}

// consumeResultLocked returns j's terminal Result and removes the first
// queued result for that job, if markTerminal has already enqueued it.
// Caller must hold m.mu for writing.
func (m *Manager) consumeResultLocked(j *Job) Result {
	for i, r := range m.results {
		if r.JobID == j.ID {
			copy(m.results[i:], m.results[i+1:])
			m.results = m.results[:len(m.results)-1]
			return r
		}
	}
	return resultFromJob(j)
}

// resultFromJob projects a Job into the Result tuple. Caller should
// have read-locked the manager (or otherwise know the Job is terminal).
func resultFromJob(j *Job) Result {
	dur := int64(0)
	if !j.StartedAt.IsZero() && !j.FinishedAt.IsZero() {
		dur = j.FinishedAt.Sub(j.StartedAt).Milliseconds()
	}
	return Result{
		JobID:        j.ID,
		Provider:     j.Provider,
		Model:        j.Model,
		Summary:      j.Summary,
		State:        j.State,
		DurationMS:   dur,
		InputTokens:  j.InputTokens,
		OutputTokens: j.OutputTokens,
		CostUSD:      j.CostUSD,
	}
}

// Spawn enqueues a new job and starts its worker goroutine. The
// returned Snapshot reflects the StateQueued transition. Callers
// receive a *SpawnError (not a generic error) so they can switch on
// Code without string parsing.
func (m *Manager) Spawn(req SpawnRequest) (Snapshot, *SpawnError) {
	depth := req.ParentDepth
	if req.ParentJobID != "" {
		// When spawned by another job, depth is parent + 1.
		depth = req.ParentDepth + 1
	}

	m.mu.Lock()
	if perr := m.checkSpawnAllowedLocked(depth); perr != nil {
		m.mu.Unlock()
		return Snapshot{}, perr
	}
	m.mu.Unlock()

	provSlug, modelID, perr := m.resolveProviderModel(req)
	if perr != nil {
		return Snapshot{}, perr
	}

	m.mu.Lock()
	if perr := m.checkSpawnAllowedLocked(depth); perr != nil {
		m.mu.Unlock()
		return Snapshot{}, perr
	}

	id := newShortID()
	for _, exists := m.jobs[id]; exists; _, exists = m.jobs[id] {
		id = newShortID()
	}

	mainID := "main"
	if m.cfg.MainSessions != nil {
		if cur := m.cfg.MainSessions.Current(); cur != nil {
			mainID = cur.ID
		}
	}
	subID := mainID + "-job-" + id

	now := time.Now().UTC()
	job := &Job{
		ID:          id,
		SessionID:   subID,
		ParentJobID: req.ParentJobID,
		Prompt:      req.Prompt,
		Provider:    provSlug,
		Model:       modelID,
		State:       StateQueued,
		CreatedAt:   now,
		Depth:       depth,
		AllowWrite:  req.AllowWrite,
	}
	// Allocate the per-job ctx and cancel func eagerly so /cancel works
	// while the job is still in StateQueued (i.e. before its worker has
	// acquired the semaphore).
	jobCtx, cancel := context.WithCancel(m.baseCtx)
	m.jobs[id] = job
	m.cancel[id] = cancel
	m.totalSpawned++
	m.terminalCh[id] = make(chan struct{})
	snap := snapshotOf(job)
	subscribers := snapshotCallbacks(m.subscribers)
	m.mu.Unlock()

	// Persist the queued state best-effort.
	_ = saveSnapshot(m.cfg.JobsDir, job)

	m.fanOut(snap, subscribers)

	m.wg.Add(1)
	go m.runJob(job, req, jobCtx)

	return snap, nil
}

func (m *Manager) checkSpawnAllowedLocked(depth int) *SpawnError {
	if m.closed {
		return &SpawnError{Code: "manager_closed"}
	}
	if m.totalSpawned >= m.cfg.MaxTotal {
		return &SpawnError{
			Code:   "limit_reached",
			Reason: fmt.Sprintf("max %d background jobs per app run", m.cfg.MaxTotal),
		}
	}
	if depth >= m.cfg.MaxDepth {
		return &SpawnError{
			Code:   "depth_exceeded",
			Reason: fmt.Sprintf("max background depth is %d", m.cfg.MaxDepth),
		}
	}
	return nil
}

// buildJobProviderRegistry constructs a fresh single-provider Registry
// containing only the (provider, model) pair the job is bound to. This
// matches the spec's isolation philosophy: a hot-switch in the main
// session does not retroactively switch a running job's model.
func (m *Manager) buildJobProviderRegistry(j *Job) (*provider.Registry, error) {
	if m.cfg.Registry == nil {
		return nil, fmt.Errorf("no provider registry configured")
	}
	prov, ok := m.cfg.Registry.Get(j.Provider)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", j.Provider)
	}
	sub := provider.NewRegistry()
	sub.Register(prov)
	if err := sub.SetActive(j.Provider, j.Model); err != nil {
		return nil, err
	}
	return sub, nil
}

// resolveProviderModel walks the spec's precedence ladder to bind a job
// to a single (provider, model) pair at spawn time:
//  1. Explicit SpawnRequest overrides.
//  2. Manager Config defaults.
//  3. The main Registry's Active() pair.
//
// At each step, missing slots fall through to the next.
func (m *Manager) resolveProviderModel(req SpawnRequest) (string, string, *SpawnError) {
	m.mu.RLock()
	reg := m.cfg.Registry
	defaultProvider := m.cfg.DefaultProvider
	defaultModel := m.cfg.DefaultModel
	baseCtx := m.baseCtx
	m.mu.RUnlock()

	provSlug := req.Provider
	modelID := req.Model

	if provSlug == "" {
		provSlug = defaultProvider
	}
	if modelID == "" {
		modelID = defaultModel
	}

	if (provSlug == "" || modelID == "") && reg != nil {
		if activeProv, activeModel := reg.Active(); activeProv != nil {
			if provSlug == "" {
				provSlug = activeProv.Slug()
			}
			if modelID == "" {
				modelID = activeModel
			}
		}
	}
	if provSlug == "" || modelID == "" {
		return "", "", &SpawnError{Code: "no_provider", Reason: "no default (provider, model) pair available"}
	}
	if reg != nil {
		prov, ok := reg.Get(provSlug)
		if !ok {
			return "", "", &SpawnError{Code: "unknown_provider", Reason: provSlug}
		}
		if perr := validateSpawnModel(baseCtx, reg, prov, modelID); perr != nil {
			return "", "", perr
		}
	}
	return provSlug, modelID, nil
}

func validateSpawnModel(ctx context.Context, reg *provider.Registry, prov provider.Provider, modelID string) *SpawnError {
	models, ok := reg.CachedModels(prov.Slug())
	if !ok {
		listCtx, cancel := context.WithTimeout(ctx, spawnModelValidationTimeout)
		fetched, err := prov.ListModels(listCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return &SpawnError{Code: "manager_closed"}
			}
			return &SpawnError{
				Code:   "unknown_model",
				Reason: fmt.Sprintf("could not validate %s/%s: %v", prov.Slug(), modelID, err),
			}
		}
		reg.SetCachedModels(prov.Slug(), fetched)
		models = fetched
	}
	for _, m := range models {
		if m.ID == modelID {
			return nil
		}
	}
	return &SpawnError{
		Code:   "unknown_model",
		Reason: fmt.Sprintf("%q is not exposed by provider %q", modelID, prov.Slug()),
	}
}

// fanOut dispatches snap to every subscriber from a separate goroutine
// per subscriber so a slow callback can't block other subscribers or
// the worker. Callers must invoke fanOut with the manager lock NOT held.
func (m *Manager) fanOut(snap Snapshot, subs []func(Snapshot)) {
	for _, fn := range subs {
		fn := fn
		go func() {
			defer func() {
				_ = recover() // an exploding subscriber must not kill us
			}()
			fn(snap)
		}()
	}
}

// markTerminal flips the job's state to a terminal value, persists,
// signals the terminalCh, and fans out the final snapshot. It must be
// called while NOT holding m.mu.
func (m *Manager) markTerminal(j *Job, newState State, summary, errMsg, reason string, finalUsageInput, finalUsageOutput int, finalCost float64, transcript []provider.Message) {
	m.mu.Lock()
	if j.State.IsTerminal() {
		m.mu.Unlock()
		return
	}
	j.State = newState
	if summary != "" {
		j.Summary = summary
	}
	if errMsg != "" {
		j.Error = errMsg
	}
	if reason != "" {
		j.Reason = reason
	}
	if finalUsageInput > j.InputTokens {
		j.InputTokens = finalUsageInput
	}
	if finalUsageOutput > j.OutputTokens {
		j.OutputTokens = finalUsageOutput
	}
	if finalCost > j.CostUSD {
		j.CostUSD = finalCost
	}
	if len(transcript) > 0 {
		j.Transcript = transcript
	}
	j.FinishedAt = time.Now().UTC()
	delete(m.cancel, j.ID)
	res := resultFromJob(j)
	m.results = append(m.results, res)
	subs := snapshotCallbacks(m.subscribers)
	ch := m.terminalCh[j.ID]
	snap := snapshotOf(j)
	m.mu.Unlock()

	_ = saveSnapshot(m.cfg.JobsDir, j)

	if ch != nil {
		// Best-effort close (channel may have been closed by a racing
		// caller during shutdown). Recover protects against re-close.
		func() {
			defer func() { _ = recover() }()
			close(ch)
		}()
	}
	m.fanOut(snap, subs)
}

// markRunning flips the job from Queued to Running and fans out.
func (m *Manager) markRunning(j *Job) {
	m.mu.Lock()
	if j.State.IsTerminal() {
		m.mu.Unlock()
		return
	}
	j.State = StateRunning
	j.StartedAt = time.Now().UTC()
	subs := snapshotCallbacks(m.subscribers)
	snap := snapshotOf(j)
	m.mu.Unlock()
	_ = saveSnapshot(m.cfg.JobsDir, j)
	m.fanOut(snap, subs)
}

// acquirePathLock returns (creating if needed) the mutex guarding the
// given absolute path. Used by pathLockTool to serialise concurrent
// writes from sibling jobs targeting the same file.
func (m *Manager) acquirePathLock(abs string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.pathLocks[abs]
	if !ok {
		mu = &sync.Mutex{}
		m.pathLocks[abs] = mu
	}
	return mu
}

// snapshotCallbacks returns a defensive copy of the subscriber slice
// the caller can iterate without holding the manager lock. Reads of
// nil yield nil safely.
func snapshotCallbacks(src []func(Snapshot)) []func(Snapshot) {
	if len(src) == 0 {
		return nil
	}
	out := make([]func(Snapshot), len(src))
	copy(out, src)
	return out
}

// newShortID returns an 8-char hex id derived from a fresh UUID. Short
// enough for the /jobs table; long enough that collisions in a single
// app run are vanishingly unlikely (we still loop on collision in
// Spawn for paranoia).
func newShortID() string {
	id := uuid.NewString()
	// UUID is 36 chars with dashes; first 8 hex chars are uniformly
	// random and don't include any dashes.
	out := make([]byte, 0, 8)
	for i := 0; i < len(id) && len(out) < 8; i++ {
		c := id[i]
		if c == '-' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// ─────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────

var (
	// ErrJobNotFound is returned when a Manager API is called with an id
	// that no job currently has. Most public methods use ok-bool returns
	// instead of errors; this exists for callers that need a typed
	// sentinel (e.g. logging).
	ErrJobNotFound = errors.New("jobs: not found")
)
