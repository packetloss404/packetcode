// Package jobs owns the lifecycle of background ("spawned") agents.
//
// A Job is a fully isolated mini-agent: its own session.Manager, its own
// session.BackupManager, its own tools.Registry (cloned from the main
// one), and its own context.CancelFunc. The Manager governs concurrency
// (sem-bounded MaxConcurrent), recursion (Depth ≤ MaxDepth), the lifetime
// cap (MaxTotal), persistence to ~/.packetcode/jobs/, and the asynchronous
// fan-out of state-transition Snapshots to the UI.
//
// See docs/feature-background-agents.md for the full design.
package jobs

import (
	"time"

	"github.com/packetcode/packetcode/internal/provider"
)

// State enumerates the lifecycle of a Job. Terminal states (Completed,
// Failed, Cancelled) never transition further.
type State int

const (
	StateQueued    State = iota // accepted, not yet running (concurrency limit)
	StateRunning                // worker goroutine started, agent loop active
	StateCompleted              // agent emitted EventDone
	StateFailed                 // EventError or panic
	StateCancelled              // ctx cancelled by user or shutdown
)

// String renders a State for logs and the /jobs panel.
func (s State) String() string {
	switch s {
	case StateQueued:
		return "queued"
	case StateRunning:
		return "running"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether s is a terminal state. The worker stops
// publishing snapshots once a job reaches a terminal state.
func (s State) IsTerminal() bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	}
	return false
}

// ResultStatus records how a terminal job result has been handled after
// completion. Pending/seen results remain available for an explicit
// Agent View decision; ignored/injected/consumed results are final.
type ResultStatus string

const (
	ResultStatusPending  ResultStatus = "pending"
	ResultStatusSeen     ResultStatus = "seen"
	ResultStatusIgnored  ResultStatus = "ignored"
	ResultStatusInjected ResultStatus = "injected"
	ResultStatusConsumed ResultStatus = "consumed"
)

func (s ResultStatus) String() string {
	if s == "" {
		return string(ResultStatusPending)
	}
	return string(s)
}

func normalizeResultStatus(s ResultStatus) ResultStatus {
	switch s {
	case ResultStatusPending, ResultStatusSeen, ResultStatusIgnored, ResultStatusInjected, ResultStatusConsumed:
		return s
	default:
		return ResultStatusPending
	}
}

// Job is the in-memory record for a single background agent run. The
// Manager owns the canonical Job; UI/test code should consume Snapshots
// to avoid sharing mutable state.
type Job struct {
	ID             string // 8-char short id, also the subsession suffix
	SessionID      string // full id of the job's underlying session.Session
	ParentJobID    string // "" when spawned from the main session
	Prompt         string // initial user message
	Provider       string // slug; may differ from main session
	Model          string // model id under that provider
	State          State
	CreatedAt      time.Time
	StartedAt      time.Time
	FinishedAt     time.Time
	UpdatedAt      time.Time
	Summary        string // short result summary surfaced into main convo
	Error          string // populated on StateFailed
	Reason         string // free-form; "previous app exit" / "app shutdown" / etc.
	LastActivity   string // concise activity label for dashboards
	LastMessage    string // latest human-visible text/result snippet
	NeedsInput     bool   // true while a job is blocked on user action
	NeedsApproval  bool   // true while a job is blocked on tool approval
	Seq            int64  // monotonic snapshot sequence for stale-update guards
	InputTokens    int
	OutputTokens   int
	CostUSD        float64
	Depth          int                // 0 for main-spawned, parent.Depth+1 otherwise
	Transcript     []provider.Message // snapshot taken when state becomes terminal
	AllowWrite     bool               // tracks whether destructive tools were enabled
	ResultStatus   ResultStatus       // pending/seen/ignored/injected/consumed after terminal result exists
	Artifacts      []Artifact         // bounded structured refs captured from tool execution
	WorktreePath   string             // per-job git worktree root when write isolation is active
	WorktreeBranch string             // branch checked out by the worktree
	WorktreeBase   string             // base ref/SHA used to create the worktree
	WorktreeNote   string             // fallback or setup note when no worktree was created
}

// Snapshot is a safe-to-copy projection of Job for UI consumption. It
// shares no mutable state with the underlying Job — Manager produces a
// fresh Snapshot on every state transition.
type Snapshot struct {
	ID, ParentJobID, Prompt, Provider, Model, Summary, Error string
	LastActivity, LastMessage                                string
	State                                                    State
	ResultStatus                                             ResultStatus
	CreatedAt, StartedAt, FinishedAt, UpdatedAt              time.Time
	Tokens                                                   struct{ Input, Output int }
	CostUSD                                                  float64
	Depth                                                    int
	NeedsInput, NeedsApproval, AllowWrite                    bool
	Seq                                                      int64
	Artifacts                                                []Artifact
	WorktreePath, WorktreeBranch, WorktreeBase, WorktreeNote string
}

// snapshotOf builds a Snapshot from a Job. Caller must hold the Manager's
// read lock (or otherwise know the Job is not being mutated).
func snapshotOf(j *Job) Snapshot {
	s := Snapshot{
		ID:             j.ID,
		ParentJobID:    j.ParentJobID,
		Prompt:         j.Prompt,
		Provider:       j.Provider,
		Model:          j.Model,
		Summary:        j.Summary,
		Error:          j.Error,
		State:          j.State,
		ResultStatus:   normalizeResultStatus(j.ResultStatus),
		CreatedAt:      j.CreatedAt,
		StartedAt:      j.StartedAt,
		FinishedAt:     j.FinishedAt,
		UpdatedAt:      j.UpdatedAt,
		CostUSD:        j.CostUSD,
		Depth:          j.Depth,
		LastActivity:   j.LastActivity,
		LastMessage:    j.LastMessage,
		NeedsInput:     j.NeedsInput,
		NeedsApproval:  j.NeedsApproval,
		AllowWrite:     j.AllowWrite,
		Seq:            j.Seq,
		Artifacts:      cloneArtifacts(j.Artifacts),
		WorktreePath:   j.WorktreePath,
		WorktreeBranch: j.WorktreeBranch,
		WorktreeBase:   j.WorktreeBase,
		WorktreeNote:   j.WorktreeNote,
	}
	s.Tokens.Input = j.InputTokens
	s.Tokens.Output = j.OutputTokens
	return s
}
