package jobs

import (
	"fmt"
	"time"
)

// MaxResubmitPromptBytes bounds the saved input a recovered job may be
// resubmitted from. Oversize prompts are refused rather than truncated:
// a silently shortened prompt would produce a different run than the one
// the user believes they are re-running.
const MaxResubmitPromptBytes = 32 << 10

// Resubmit starts a NEW background job from a recovered job's saved input.
//
// It deliberately does not resume anything. The previous process is gone,
// its agent loop cannot be continued, and pretending otherwise would be a
// lie the rest of the job record cannot support. Instead:
//
//   - the recovered job keeps the terminal state reconciliation gave it
//     (Abandoned for work that had begun, Cancelled for work that provably
//     never ran), its "previous app exit" reason, and all of its evidence
//     (artifacts, transcript, worktree references, token and cost totals);
//   - a brand-new job is spawned from the saved prompt/provider/model;
//   - the two records are linked in both directions (ResubmittedAs on the
//     old job, ResubmitOf on the new one) so the lineage stays inspectable.
//
// Only jobs marked Recovered are eligible, and only once — a second call
// returns "already_resubmitted" pointing at the existing successor.
func (m *Manager) Resubmit(id string) (Snapshot, *SpawnError) {
	m.mu.Lock()
	old, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Snapshot{}, &SpawnError{Code: "unknown_job", Reason: fmt.Sprintf("no job %q", id)}
	}
	if !old.Recovered {
		m.mu.Unlock()
		return Snapshot{}, &SpawnError{
			Code:   "not_recovered",
			Reason: fmt.Sprintf("job %s was not abandoned by a previous app exit", id),
		}
	}
	if old.ResubmittedAs != "" {
		successor := old.ResubmittedAs
		m.mu.Unlock()
		return Snapshot{}, &SpawnError{
			Code:   "already_resubmitted",
			Reason: fmt.Sprintf("job %s was already resubmitted as %s", id, successor),
		}
	}
	if !old.State.IsTerminal() {
		m.mu.Unlock()
		return Snapshot{}, &SpawnError{
			Code:   "not_terminal",
			Reason: fmt.Sprintf("job %s is still %s", id, old.State),
		}
	}
	if m.resubmitting[id] {
		m.mu.Unlock()
		return Snapshot{}, &SpawnError{Code: "resubmit_in_progress", Reason: fmt.Sprintf("job %s is already being resubmitted; wait for that request to finish", id)}
	}
	if m.resubmitting == nil {
		m.resubmitting = make(map[string]bool)
	}
	m.resubmitting[id] = true
	defer func() {
		m.mu.Lock()
		delete(m.resubmitting, id)
		m.mu.Unlock()
	}()

	req := SpawnRequest{
		Prompt:      old.Prompt,
		Provider:    old.Provider,
		Model:       old.Model,
		AllowWrite:  old.AllowWrite,
		ParentJobID: old.ParentJobID,
	}
	originalWorkspace := workspaceOfJob(old, m.cfg.Root)
	savedWorkingDir := old.WorkingDir
	if old.ComputerID != "" {
		// Resolve by stable id, not display name. The resolver must compare the
		// current endpoint/root identity before a new run is allowed.
		req.Computer = old.ComputerID
	}
	// Spawn derives depth as ParentDepth+1 for parented jobs, so step back
	// one to land the successor at the same depth as the original.
	if req.ParentJobID != "" && old.Depth > 0 {
		req.ParentDepth = old.Depth - 1
	} else {
		req.ParentDepth = old.Depth
	}
	m.mu.Unlock()

	if originalWorkspace.ComputerID != "" {
		resolved, workspaceErr := m.resolveWorkspaceSelector(originalWorkspace.ComputerID)
		if workspaceErr != nil {
			return Snapshot{}, workspaceErr
		}
		if !sameWorkspace(resolved, originalWorkspace) {
			return Snapshot{}, &SpawnError{
				Code: "workspace_identity_mismatch",
				Reason: fmt.Sprintf(
					"job %s was bound to %s; the current registry resolves that id to %s",
					id, workspaceLabel(originalWorkspace), workspaceLabel(resolved),
				),
			}
		}
	} else {
		current, workspaceErr := m.resolveSpawnWorkspace(SpawnRequest{})
		if workspaceErr != nil {
			return Snapshot{}, workspaceErr
		}
		if current.ComputerID != "" {
			return Snapshot{}, &SpawnError{
				Code: "workspace_unbound",
				Reason: fmt.Sprintf(
					"job %s was local or has no Packet Computer binding; it cannot be resubmitted into %s",
					id, workspaceLabel(current),
				),
			}
		}
		// Jobs created after workspace binding shipped carry a local root.
		// Preserve it exactly; only legacy records with no root retain the
		// historical current-local-root behavior.
		if savedWorkingDir != "" && current.WorkingDir != savedWorkingDir {
			return Snapshot{}, &SpawnError{
				Code: "workspace_identity_mismatch",
				Reason: fmt.Sprintf(
					"job %s was bound to local root %s, not %s",
					id, savedWorkingDir, current.WorkingDir,
				),
			}
		}
	}

	if req.Prompt == "" {
		return Snapshot{}, &SpawnError{
			Code:   "empty_prompt",
			Reason: fmt.Sprintf("job %s has no saved prompt to resubmit", id),
		}
	}
	if len(req.Prompt) > MaxResubmitPromptBytes {
		return Snapshot{}, &SpawnError{
			Code: "prompt_too_large",
			Reason: fmt.Sprintf(
				"job %s saved prompt is %d bytes (limit %d); start it again manually rather than resubmitting a truncated prompt",
				id, len(req.Prompt), MaxResubmitPromptBytes,
			),
		}
	}

	snap, perr := m.Spawn(req)
	if perr != nil {
		return Snapshot{}, perr
	}

	// Link both directions and persist. The successor's link is set before
	// it can finish, and the predecessor is rewritten in place so a restart
	// still shows the lineage.
	m.mu.Lock()
	var oldPersisted, newPersisted persistedJob
	var haveOld, haveNew bool
	if o, ok := m.jobs[id]; ok {
		o.ResubmittedAs = snap.ID
		o.UpdatedAt = time.Now().UTC()
		oldPersisted, haveOld = toPersisted(o), true
	}
	if n, ok := m.jobs[snap.ID]; ok {
		n.ResubmitOf = id
		newPersisted, haveNew = toPersisted(n), true
		snap = snapshotOf(n)
	}
	m.mu.Unlock()

	if haveOld {
		_ = m.savePersistedSnapshotImmediate(oldPersisted)
	}
	if haveNew {
		_ = m.savePersistedSnapshotImmediate(newPersisted)
	}
	return snap, nil
}

// RecoveredResubmittable lists recovered jobs that have not yet been
// resubmitted, newest first by creation time. Used by the /jobs surface to
// point the user at work the previous process abandoned.
func (m *Manager) RecoveredResubmittable() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Snapshot
	for _, j := range m.jobs {
		if j.Recovered && j.ResubmittedAs == "" && j.Prompt != "" && len(j.Prompt) <= MaxResubmitPromptBytes {
			out = append(out, snapshotOf(j))
		}
	}
	sortSnapshotsByCreatedDesc(out)
	return out
}

func sortSnapshotsByCreatedDesc(s []Snapshot) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].CreatedAt.After(s[j-1].CreatedAt); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
