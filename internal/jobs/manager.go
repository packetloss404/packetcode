package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/packetcode/packetcode/internal/agent"
	"github.com/packetcode/packetcode/internal/cost"
	"github.com/packetcode/packetcode/internal/hooks"
	"github.com/packetcode/packetcode/internal/permissions"
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
	// PermissionPolicy is shared with the foreground agent so background
	// jobs obey the same allow/ask/deny profile and rules.
	PermissionPolicy *permissions.Policy

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

	// SpawnTool is the optional factory used to plug a spawn_agent
	// tool into each per-job tool registry without
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
	Error        string
	Reason       string
	State        State
	Status       ResultStatus
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
	liveSessions map[string]*session.Manager
	pathLocks    pathLockMap
	persistMu    sync.Mutex
	persistSeq   map[string]int64
	closed       bool
	totalSpawned int
	seq          int64

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
		cfg:          cfg,
		jobs:         map[string]*Job{},
		cancel:       map[string]context.CancelFunc{},
		liveSessions: map[string]*session.Manager{},
		pathLocks:    pathLockMap{},
		persistSeq:   map[string]int64{},
		sem:          make(chan struct{}, cfg.MaxConcurrent),
		baseCtx:      baseCtx,
		cancelBase:   cancelBase,
		terminalCh:   map[string]chan struct{}{},
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
		m.persistSeq[j.ID] = j.Seq
		if j.Seq > m.seq {
			m.seq = j.Seq
		}
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
// after Manager construction. This closes the chicken-and-egg between
// "the factory needs *Manager" and "the config is passed by value into
// NewManager". Safe to call before any jobs have been spawned; calling
// it concurrently with running workers is safe but may affect
// subsequent job boots only.
func (m *Manager) SetSpawnToolFactory(fn func(parentJobID string, parentDepth int, parentAllowWrite bool) tools.Tool) {
	m.mu.Lock()
	m.cfg.SpawnTool = fn
	m.mu.Unlock()
}

// SetApprover installs (or replaces) the per-job parent Approver after
// Manager construction. The App uses this when it constructs its
// uiApprover internally; main.go wires that approver back into the
// Manager after app.New returns. As with SetSpawnToolFactory, the new
// value takes effect on subsequent job boots.
func (m *Manager) SetApprover(a agent.Approver) {
	m.mu.Lock()
	m.cfg.Approver = a
	m.mu.Unlock()
}

// SetPermissionPolicy installs the effective foreground permission
// policy for subsequently spawned jobs. Running jobs keep the immutable
// policy snapshot they were started with.
func (m *Manager) SetPermissionPolicy(policy *permissions.Policy) {
	m.mu.Lock()
	m.cfg.PermissionPolicy = policy
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
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		if out[i].Seq != out[j].Seq {
			return out[i].Seq > out[j].Seq
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Transcript returns the message history captured for a job. For jobs
// with an open sub-session it reads directly from that live session. If
// the worker has already exited, it reloads the persisted job session
// before falling back to the terminal in-memory snapshot.
func (m *Manager) Transcript(id string) ([]provider.Message, bool) {
	m.mu.RLock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.RUnlock()
		return nil, false
	}
	sessionID := j.SessionID
	liveSession := m.liveSessions[id]
	fallback := cloneTranscriptMessages(j.Transcript)
	sessionsDir := m.cfg.SessionsDir
	m.mu.RUnlock()

	if liveSession != nil {
		return snapshotTranscript(liveSession), true
	}
	if transcript, ok := loadSessionTranscript(sessionsDir, sessionID); ok {
		return transcript, true
	}
	return fallback, true
}

func (m *Manager) attachLiveSession(id string, sm *session.Manager) {
	if sm == nil {
		return
	}
	m.mu.Lock()
	if j := m.jobs[id]; j != nil && !j.State.IsTerminal() {
		m.liveSessions[id] = sm
	}
	m.mu.Unlock()
}

func (m *Manager) detachLiveSession(id string) {
	m.mu.Lock()
	delete(m.liveSessions, id)
	m.mu.Unlock()
}

func loadSessionTranscript(sessionsDir, sessionID string) ([]provider.Message, bool) {
	if sessionsDir == "" || sessionID == "" {
		return nil, false
	}
	sm := session.NewManager(sessionsDir)
	if _, err := sm.Load(sessionID); err != nil {
		return nil, false
	}
	return snapshotTranscript(sm), true
}

func cloneTranscriptMessages(messages []provider.Message) []provider.Message {
	if messages == nil {
		return nil
	}
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if messages[i].ToolCalls != nil {
			out[i].ToolCalls = append([]provider.ToolCall(nil), messages[i].ToolCalls...)
		}
	}
	return out
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
	m.stampSnapshotLocked(j, time.Now().UTC(), "cancelling", "cancellation requested", false, false)
	snap := snapshotOf(j)
	subs := snapshotCallbacks(m.subscribers)
	cancel, ok := m.cancel[id]
	m.mu.Unlock()
	if !ok || cancel == nil {
		return false
	}
	m.fanOut(snap, subs)
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

// PendingResults returns up to n undecided terminal results oldest-first
// without consuming them. Passing n <= 0 returns every pending/seen
// result. Ignored and injected results are final and are not returned.
func (m *Manager) PendingResults(n int) []Result {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return copyResults(m.results, n)
}

// MarkResultSeen records that a terminal result has been surfaced to
// the user but has not been injected into Agent View. Seen results stay
// available for a later explicit ignore/inject decision.
func (m *Manager) MarkResultSeen(id string) (Result, bool) {
	return m.markResultStatus(id, ResultStatusSeen)
}

// MarkResultIgnored finalises a terminal result without injecting it
// into Agent View.
func (m *Manager) MarkResultIgnored(id string) (Result, bool) {
	return m.markResultStatus(id, ResultStatusIgnored)
}

// MarkResultInjected finalises a terminal result as injected into Agent
// View and returns the Result payload the caller should add to the
// foreground session.
func (m *Manager) MarkResultInjected(id string) (Result, bool) {
	return m.markResultStatus(id, ResultStatusInjected)
}

// Result returns the current terminal result payload without changing
// its lifecycle status. Ignored and injected results are considered
// final and are not returned for another user decision.
func (m *Manager) Result(id string) (Result, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok || !j.State.IsTerminal() {
		return Result{}, false
	}
	current := normalizeResultStatus(j.ResultStatus)
	if current == ResultStatusIgnored || current == ResultStatusInjected {
		return Result{}, false
	}
	return resultFromJob(j), true
}

func (m *Manager) markResultStatus(id string, status ResultStatus) (Result, bool) {
	status = normalizeResultStatus(status)
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || !j.State.IsTerminal() {
		m.mu.Unlock()
		return Result{}, false
	}
	current := normalizeResultStatus(j.ResultStatus)
	if current == status {
		res := resultFromJob(j)
		m.mu.Unlock()
		return res, true
	}
	if current == ResultStatusIgnored || current == ResultStatusInjected {
		res := resultFromJob(j)
		m.mu.Unlock()
		return res, false
	}
	j.ResultStatus = status
	m.stampSnapshotLocked(j, time.Now().UTC(), "result "+status.String(), "", false, false)
	res := resultFromJob(j)
	if status == ResultStatusIgnored || status == ResultStatusInjected {
		m.removeQueuedResultLocked(id)
	} else {
		m.upsertQueuedResultLocked(res)
	}
	subs := snapshotCallbacks(m.subscribers)
	snap := snapshotOf(j)
	persisted := toPersisted(j)
	m.mu.Unlock()

	_ = m.savePersistedSnapshot(persisted)
	m.fanOut(snap, subs)
	return res, true
}

// DrainResults removes up to n undecided entries from the internal
// results queue and returns them oldest-first. This is the legacy drain
// path retained for migration compatibility; new Agent View flows should
// prefer PendingResults plus MarkResultSeen/MarkResultIgnored/
// MarkResultInjected so injection is an explicit decision. Drained
// results are marked injected.
func (m *Manager) DrainResults(n int) []Result {
	m.mu.Lock()
	if len(m.results) == 0 {
		m.mu.Unlock()
		return nil
	}
	if n <= 0 || n > len(m.results) {
		n = len(m.results)
	}
	out := make([]Result, 0, n)
	toSave := make([]persistedJob, 0, n)
	var snaps []Snapshot
	subs := snapshotCallbacks(m.subscribers)
	for _, r := range m.results[:n] {
		if j := m.jobs[r.JobID]; j != nil && j.State.IsTerminal() {
			j.ResultStatus = ResultStatusInjected
			m.stampSnapshotLocked(j, time.Now().UTC(), "result injected", "", false, false)
			out = append(out, resultFromJob(j))
			toSave = append(toSave, toPersisted(j))
			snaps = append(snaps, snapshotOf(j))
		} else {
			r.Status = ResultStatusInjected
			out = append(out, r)
		}
	}
	m.results = m.results[n:]
	m.mu.Unlock()

	for _, p := range toSave {
		_ = m.savePersistedSnapshot(p)
	}
	for _, snap := range snaps {
		m.fanOut(snap, subs)
	}
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
		out := m.consumeResultLocked(j, ResultStatusInjected)
		persisted := toPersisted(j)
		m.mu.Unlock()
		_ = m.savePersistedSnapshot(persisted)
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
					out := m.consumeResultLocked(j3, ResultStatusInjected)
					persisted := toPersisted(j3)
					m.mu.Unlock()
					_ = m.savePersistedSnapshot(persisted)
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
		out := m.consumeResultLocked(j2, ResultStatusInjected)
		persisted := toPersisted(j2)
		m.mu.Unlock()
		_ = m.savePersistedSnapshot(persisted)
		return out, true
	case <-time.After(timeout):
		return Result{}, false
	}
}

// consumeResultLocked returns j's terminal Result and removes the first
// queued result for that job, if markTerminal has already enqueued it.
// Caller must hold m.mu for writing.
func (m *Manager) consumeResultLocked(j *Job, status ResultStatus) Result {
	j.ResultStatus = normalizeResultStatus(status)
	m.stampSnapshotLocked(j, time.Now().UTC(), "result "+j.ResultStatus.String(), "", false, false)
	for i, r := range m.results {
		if r.JobID == j.ID {
			copy(m.results[i:], m.results[i+1:])
			m.results = m.results[:len(m.results)-1]
			return resultFromJob(j)
		}
	}
	return resultFromJob(j)
}

func copyResults(src []Result, n int) []Result {
	if len(src) == 0 {
		return nil
	}
	if n <= 0 || n > len(src) {
		n = len(src)
	}
	out := make([]Result, n)
	copy(out, src[:n])
	return out
}

func (m *Manager) removeQueuedResultLocked(id string) {
	for i, r := range m.results {
		if r.JobID == id {
			copy(m.results[i:], m.results[i+1:])
			m.results = m.results[:len(m.results)-1]
			return
		}
	}
}

func (m *Manager) upsertQueuedResultLocked(res Result) {
	for i, r := range m.results {
		if r.JobID == res.JobID {
			m.results[i] = res
			return
		}
	}
	m.results = append(m.results, res)
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
		Error:        j.Error,
		Reason:       j.Reason,
		State:        j.State,
		Status:       normalizeResultStatus(j.ResultStatus),
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
		UpdatedAt:   now,
		Depth:       depth,
		AllowWrite:  req.AllowWrite,
	}
	m.stampSnapshotLocked(job, now, "queued", req.Prompt, false, false)
	// Allocate the per-job ctx and cancel func eagerly so /cancel works
	// while the job is still in StateQueued (i.e. before its worker has
	// acquired the semaphore).
	jobCtx, cancel := context.WithCancel(m.baseCtx)
	m.jobs[id] = job
	m.cancel[id] = cancel
	m.totalSpawned++
	m.terminalCh[id] = make(chan struct{})
	snap := snapshotOf(job)
	persisted := toPersisted(job)
	subscribers := snapshotCallbacks(m.subscribers)
	m.mu.Unlock()

	// Persist the queued state best-effort.
	_ = m.savePersistedSnapshot(persisted)

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

func trimActivityMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 160 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= 160 {
		return s
	}
	return string(rs[:157]) + "..."
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

// stampSnapshotLocked advances the job's externally visible snapshot
// metadata. Caller must hold m.mu for writing.
func (m *Manager) stampSnapshotLocked(j *Job, at time.Time, activity, message string, needsInput, needsApproval bool) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !j.UpdatedAt.IsZero() && !at.After(j.UpdatedAt) {
		at = j.UpdatedAt.Add(time.Nanosecond)
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = at
	}
	j.UpdatedAt = at
	if activity != "" {
		j.LastActivity = activity
	}
	if message != "" {
		j.LastMessage = trimActivityMessage(message)
	}
	j.NeedsInput = needsInput
	j.NeedsApproval = needsApproval
	if m.seq < j.Seq {
		m.seq = j.Seq
	}
	m.seq++
	j.Seq = m.seq
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
	activity := newState.String()
	if newState == StateCompleted {
		activity = "ready for review"
	}
	if summary != "" {
		j.Summary = summary
		j.LastMessage = summary
	}
	if errMsg != "" {
		j.Error = errMsg
		j.LastMessage = errMsg
	}
	if reason != "" {
		j.Reason = reason
		if j.LastMessage == "" {
			j.LastMessage = reason
		}
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
	now := time.Now().UTC()
	j.FinishedAt = now
	m.stampSnapshotLocked(j, now, activity, j.LastMessage, false, false)
	delete(m.cancel, j.ID)
	res := resultFromJob(j)
	m.results = append(m.results, res)
	subs := snapshotCallbacks(m.subscribers)
	ch := m.terminalCh[j.ID]
	snap := snapshotOf(j)
	persisted := toPersisted(j)
	m.mu.Unlock()

	_ = m.savePersistedSnapshot(persisted)

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
	now := time.Now().UTC()
	j.StartedAt = now
	m.stampSnapshotLocked(j, now, "working", "started", false, false)
	subs := snapshotCallbacks(m.subscribers)
	snap := snapshotOf(j)
	persisted := toPersisted(j)
	m.mu.Unlock()
	_ = m.savePersistedSnapshot(persisted)
	m.fanOut(snap, subs)
}

func (m *Manager) updateActivity(j *Job, activity, message string, needsInput, needsApproval bool) {
	m.mu.Lock()
	if j.State.IsTerminal() {
		m.mu.Unlock()
		return
	}
	m.stampSnapshotLocked(j, time.Now().UTC(), activity, message, needsInput, needsApproval)
	subs := snapshotCallbacks(m.subscribers)
	snap := snapshotOf(j)
	persisted := toPersisted(j)
	m.mu.Unlock()
	_ = m.savePersistedSnapshot(persisted)
	m.fanOut(snap, subs)
}

func (m *Manager) savePersistedSnapshot(p persistedJob) error {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	if p.Seq > 0 && p.Seq < m.persistSeq[p.ID] {
		return nil
	}
	if p.Seq > m.persistSeq[p.ID] {
		m.persistSeq[p.ID] = p.Seq
	}
	return savePersistedSnapshot(m.cfg.JobsDir, p)
}

func (m *Manager) touchJobLocked(j *Job, activity, message string) {
	m.stampSnapshotLocked(j, time.Now().UTC(), activity, message, j.NeedsInput, j.NeedsApproval)
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
