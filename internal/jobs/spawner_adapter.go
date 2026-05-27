package jobs

import (
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
		Provider:       res.Provider,
		Model:          res.Model,
		Summary:        res.Summary,
		State:          res.State.String(),
		DurationMS:     res.DurationMS,
		InputTokens:    res.InputTokens,
		OutputTokens:   res.OutputTokens,
		CostUSD:        res.CostUSD,
		WorktreePath:   res.WorktreePath,
		WorktreeBranch: res.WorktreeBranch,
	}, true
}
