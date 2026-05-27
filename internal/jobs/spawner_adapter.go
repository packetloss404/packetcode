package jobs

import (
	"sort"
	"sync"
	"time"

	"github.com/packetcode/packetcode/internal/tools"
)

// AsToolsSpawner exposes this Manager through the narrow tools.JobSpawner
// contract. The returned adapter translates between the tools-local
// mirror types and the jobs package's native types so the spawn_agent
// tool can remain in internal/tools without creating an import cycle.
func (m *Manager) AsToolsSpawner() tools.JobSpawner {
	return &spawnerAdapter{m: m}
}

// spawnerAdapter implements tools.JobSpawner by delegating to a Manager
// and mapping its native types onto the tools mirror structs.
type spawnerAdapter struct {
	m *Manager
}

func (a *spawnerAdapter) Spawn(req tools.JobSpawnRequest) (tools.JobSpawnResult, *tools.JobSpawnError) {
	snap, err := a.m.Spawn(SpawnRequest{
		Prompt:       req.Prompt,
		ParentJobID:  req.ParentJobID,
		ParentDepth:  req.ParentDepth,
		Provider:     req.Provider,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
		AllowWrite:   req.AllowWrite,
	})
	if err != nil {
		return tools.JobSpawnResult{}, &tools.JobSpawnError{Code: err.Code, Reason: err.Reason}
	}
	return tools.JobSpawnResult{
		ID:             snap.ID,
		Provider:       snap.Provider,
		Model:          snap.Model,
		Prompt:         snap.Prompt,
		Depth:          snap.Depth,
		WorktreePath:   snap.WorktreePath,
		WorktreeBranch: snap.WorktreeBranch,
	}, nil
}

func (a *spawnerAdapter) Cancel(id string) bool {
	return a.m.Cancel(id)
}

func (a *spawnerAdapter) WaitForJob(id string, timeout time.Duration) (tools.JobWaitResult, bool) {
	res, ok := a.m.WaitForJob(id, timeout)
	if !ok {
		return tools.JobWaitResult{}, false
	}
	return tools.JobWaitResult{
		JobID:          res.JobID,
		ParentJobID:    res.ParentJobID,
		Provider:       res.Provider,
		Model:          res.Model,
		Prompt:         res.Prompt,
		Summary:        res.Summary,
		Error:          res.Error,
		Reason:         res.Reason,
		State:          res.State.String(),
		Depth:          res.Depth,
		DurationMS:     res.DurationMS,
		InputTokens:    res.InputTokens,
		OutputTokens:   res.OutputTokens,
		CostUSD:        res.CostUSD,
		Artifacts:      toToolArtifacts(res.Artifacts),
		WorktreePath:   res.WorktreePath,
		WorktreeBranch: res.WorktreeBranch,
		WorktreeBase:   res.WorktreeBase,
	}, true
}

func (a *spawnerAdapter) CollectResults(req tools.JobCollectRequest) ([]tools.JobWaitResult, bool) {
	ids := compactCollectIDs(req.JobIDs)
	if len(ids) == 0 {
		ids = a.collectScopedIDs(req.ParentJobID, req.Scope)
	}
	if len(ids) == 0 {
		return nil, false
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ids = a.authorizedCollectIDs(req.ParentJobID, req.Scope, ids)
	if len(ids) == 0 {
		return nil, false
	}

	results := make(map[string]tools.JobWaitResult, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(ids))
	for _, id := range ids {
		id := id
		go func() {
			defer wg.Done()
			res, ok := a.WaitForJob(id, timeout)
			if !ok {
				return
			}
			mu.Lock()
			results[id] = res
			mu.Unlock()
		}()
	}
	wg.Wait()

	out := make([]tools.JobWaitResult, 0, len(results))
	for _, id := range ids {
		if res, ok := results[id]; ok {
			out = append(out, res)
		}
	}
	return out, len(out) > 0
}

func (a *spawnerAdapter) collectScopedIDs(parentID, scope string) []string {
	a.m.mu.RLock()
	defer a.m.mu.RUnlock()
	ids := []string{}
	for id, j := range a.m.jobs {
		if parentID == "" {
			if j.ParentJobID == "" {
				ids = append(ids, id)
			}
			continue
		}
		if scope == "descendants" {
			if a.isDescendantLocked(id, parentID) {
				ids = append(ids, id)
			}
			continue
		}
		if j.ParentJobID == parentID {
			ids = append(ids, id)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		left := jobResultSortTime(a.m.jobs[ids[i]])
		right := jobResultSortTime(a.m.jobs[ids[j]])
		return left.Before(right)
	})
	return ids
}

func (a *spawnerAdapter) authorizedCollectIDs(parentID, scope string, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if a.canCollect(parentID, scope, id) {
			out = append(out, id)
		}
	}
	return out
}

func (a *spawnerAdapter) canCollect(parentID, scope, id string) bool {
	if parentID == "" {
		return true
	}
	a.m.mu.RLock()
	defer a.m.mu.RUnlock()
	if scope == "descendants" {
		return a.isDescendantLocked(id, parentID)
	}
	j := a.m.jobs[id]
	return j != nil && j.ParentJobID == parentID
}

func (a *spawnerAdapter) isDescendantLocked(id, parentID string) bool {
	for {
		j := a.m.jobs[id]
		if j == nil || j.ParentJobID == "" {
			return false
		}
		if j.ParentJobID == parentID {
			return true
		}
		id = j.ParentJobID
	}
}

func compactCollectIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func toToolArtifacts(in []Artifact) []tools.JobArtifact {
	if len(in) == 0 {
		return nil
	}
	out := make([]tools.JobArtifact, len(in))
	for i, a := range in {
		out[i] = tools.JobArtifact{
			ID:         a.ID,
			Kind:       a.Kind,
			Title:      a.Title,
			Summary:    a.Summary,
			Path:       a.Path,
			SourceTool: a.SourceTool,
			IsError:    a.IsError,
			Truncated:  a.Truncated,
			SizeBytes:  a.SizeBytes,
			Preview:    a.Preview,
			Metadata:   cloneMetadata(a.Metadata),
		}
	}
	return out
}
