package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// persistedJob is the on-disk shape for ~/.packetcode/jobs/<id>.json.
// Mirrors Job but uses a stable JSON form so future versions can decode
// it without depending on Go field order.
type persistedJob struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	ParentJobID    string    `json:"parent_job_id,omitempty"`
	Prompt         string    `json:"prompt"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	State          string    `json:"state"`
	Seq            int64     `json:"seq,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	LastActivity   string    `json:"last_activity,omitempty"`
	LastMessage    string    `json:"last_message,omitempty"`
	NeedsInput     bool      `json:"needs_input,omitempty"`
	NeedsApproval  bool      `json:"needs_approval,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	Error          string    `json:"error,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	CostUSD        float64   `json:"cost_usd"`
	Depth          int       `json:"depth"`
	AllowWrite     bool      `json:"allow_write"`
	ResultStatus   string    `json:"result_status,omitempty"`
	WorktreePath   string    `json:"worktree_path,omitempty"`
	WorktreeBranch string    `json:"worktree_branch,omitempty"`
	WorktreeBase   string    `json:"worktree_base,omitempty"`
	WorktreeNote   string    `json:"worktree_note,omitempty"`
}

func toPersisted(j *Job) persistedJob {
	return persistedJob{
		ID:             j.ID,
		SessionID:      j.SessionID,
		ParentJobID:    j.ParentJobID,
		Prompt:         j.Prompt,
		Provider:       j.Provider,
		Model:          j.Model,
		State:          j.State.String(),
		Seq:            j.Seq,
		CreatedAt:      j.CreatedAt,
		UpdatedAt:      j.UpdatedAt,
		StartedAt:      j.StartedAt,
		FinishedAt:     j.FinishedAt,
		LastActivity:   j.LastActivity,
		LastMessage:    j.LastMessage,
		NeedsInput:     j.NeedsInput,
		NeedsApproval:  j.NeedsApproval,
		Summary:        j.Summary,
		Error:          j.Error,
		Reason:         j.Reason,
		InputTokens:    j.InputTokens,
		OutputTokens:   j.OutputTokens,
		CostUSD:        j.CostUSD,
		Depth:          j.Depth,
		AllowWrite:     j.AllowWrite,
		ResultStatus:   normalizeResultStatus(j.ResultStatus).String(),
		WorktreePath:   j.WorktreePath,
		WorktreeBranch: j.WorktreeBranch,
		WorktreeBase:   j.WorktreeBase,
		WorktreeNote:   j.WorktreeNote,
	}
}

func parseState(s string) State {
	switch s {
	case "queued":
		return StateQueued
	case "running":
		return StateRunning
	case "completed":
		return StateCompleted
	case "failed":
		return StateFailed
	case "cancelled":
		return StateCancelled
	}
	return StateFailed
}

func parseResultStatus(s string) ResultStatus {
	switch ResultStatus(s) {
	case ResultStatusPending, ResultStatusSeen, ResultStatusIgnored, ResultStatusInjected:
		return ResultStatus(s)
	default:
		return ResultStatusPending
	}
}

func fromPersisted(p persistedJob) *Job {
	updatedAt := p.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = p.FinishedAt
	}
	if updatedAt.IsZero() {
		updatedAt = p.StartedAt
	}
	if updatedAt.IsZero() {
		updatedAt = p.CreatedAt
	}
	return &Job{
		ID:             p.ID,
		SessionID:      p.SessionID,
		ParentJobID:    p.ParentJobID,
		Prompt:         p.Prompt,
		Provider:       p.Provider,
		Model:          p.Model,
		State:          parseState(p.State),
		Seq:            p.Seq,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      updatedAt,
		StartedAt:      p.StartedAt,
		FinishedAt:     p.FinishedAt,
		LastActivity:   p.LastActivity,
		LastMessage:    p.LastMessage,
		NeedsInput:     p.NeedsInput,
		NeedsApproval:  p.NeedsApproval,
		Summary:        p.Summary,
		Error:          p.Error,
		Reason:         p.Reason,
		InputTokens:    p.InputTokens,
		OutputTokens:   p.OutputTokens,
		CostUSD:        p.CostUSD,
		Depth:          p.Depth,
		AllowWrite:     p.AllowWrite,
		ResultStatus:   parseResultStatus(p.ResultStatus),
		WorktreePath:   p.WorktreePath,
		WorktreeBranch: p.WorktreeBranch,
		WorktreeBase:   p.WorktreeBase,
		WorktreeNote:   p.WorktreeNote,
	}
}

// saveSnapshot persists a Job to <jobsDir>/<id>.json with atomic
// temp-file-then-rename semantics, mirroring session.Manager.Save.
func saveSnapshot(jobsDir string, j *Job) error {
	return savePersistedSnapshot(jobsDir, toPersisted(j))
}

func savePersistedSnapshot(jobsDir string, p persistedJob) error {
	if jobsDir == "" {
		return nil
	}
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		return fmt.Errorf("save job: ensure dir: %w", err)
	}
	final := filepath.Join(jobsDir, p.ID+".json")
	if p.Seq > 0 {
		if existing, ok := readPersistedJob(final); ok && existing.Seq > p.Seq {
			return nil
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("save job: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(jobsDir, ".job.*.json.tmp")
	if err != nil {
		return fmt.Errorf("save job: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save job: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save job: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save job: rename: %w", err)
	}
	return nil
}

func readPersistedJob(path string) (persistedJob, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedJob{}, false
	}
	var p persistedJob
	if err := json.Unmarshal(data, &p); err != nil {
		return persistedJob{}, false
	}
	return p, true
}

// loadOrphaned scans jobsDir for any persisted jobs that were Queued or
// Running when the previous app instance exited, rewrites them as
// Cancelled with reason "previous app exit", and returns the count plus
// the resurrected Jobs (so callers can hydrate the in-memory map). The
// resurrected jobs are already in a terminal state.
func loadOrphaned(jobsDir string) ([]*Job, error) {
	if jobsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load orphans: %w", err)
	}
	var resurrected []*Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Skip our own temp-file pattern from interrupted writes.
		if strings.HasPrefix(e.Name(), ".job.") {
			continue
		}
		path := filepath.Join(jobsDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p persistedJob
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		state := parseState(p.State)
		if state != StateQueued && state != StateRunning {
			continue
		}
		// Rewrite as Cancelled with the orphan reason.
		j := fromPersisted(p)
		j.State = StateCancelled
		j.Reason = "previous app exit"
		if j.FinishedAt.IsZero() {
			j.FinishedAt = time.Now().UTC()
		}
		if j.UpdatedAt.IsZero() || j.FinishedAt.After(j.UpdatedAt) {
			j.UpdatedAt = j.FinishedAt
		}
		j.LastActivity = "cancelled"
		j.LastMessage = j.Reason
		j.NeedsInput = false
		j.NeedsApproval = false
		if err := saveSnapshot(jobsDir, j); err == nil {
			resurrected = append(resurrected, j)
		}
	}
	return resurrected, nil
}
