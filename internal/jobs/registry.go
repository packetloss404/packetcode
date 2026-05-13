package jobs

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

// readOnlyToolNames is the set of tools always available to a background
// job, regardless of allowWrite. spawn_agent is added separately and
// gated by depth — see buildJobToolRegistry.
var readOnlyToolNames = map[string]bool{
	"read_file":       true,
	"search_codebase": true,
	"list_directory":  true,
}

// destructiveToolNames is the set of tools only available when the job
// was explicitly opted in to writes (--write or allow_write=true). buildJobToolRegistry
// re-instantiates these with the job-local BackupManager and a path-lock
// wrapper for write_file/patch_file.
var destructiveToolNames = map[string]bool{
	"write_file":      true,
	"patch_file":      true,
	"execute_command": true,
}

// buildJobToolRegistry constructs a fresh *tools.Registry tailored to a
// single background job:
//   - Read-only tools are always included, freshly instantiated against
//     the project root so they don't share mutable state with the main
//     registry.
//   - Destructive tools (write_file, patch_file, execute_command) are
//     included only when allowWrite=true. write_file and patch_file are
//     wired to the per-job BackupManager and wrapped with a path-lock to
//     serialise concurrent writes from sibling jobs.
//   - extraTools are appended after the core set. The background spawn
//     integration uses this to add spawn_agent without creating an
//     import cycle (jobs ↔ tools).
//
// parentDepth is informational here; depth-based gating of spawn_agent
// itself happens in the caller (Manager.runJob), which decides whether
// to pass a SpawnAgentTool in extraTools.
func (m *Manager) buildJobToolRegistry(
	parentDepth int,
	allowWrite bool,
	jobID string,
	backups *session.BackupManager,
	extraTools []tools.Tool,
) *tools.Registry {
	out := tools.NewRegistry()

	// Walk the main registry and emit fresh per-job copies. We don't
	// reuse the source Tool instances so that field-level mutation in one
	// job (e.g. ripgrep path caching) doesn't leak into another.
	src := m.cfg.Tools
	if src == nil {
		// Permitted in tests that wire only extraTools.
		for _, t := range extraTools {
			out.Register(t)
		}
		return out
	}

	root := m.cfg.Root
	for _, t := range src.All() {
		name := t.Name()
		switch {
		case readOnlyToolNames[name]:
			out.Register(cloneReadOnlyTool(name, root, t))
		case destructiveToolNames[name]:
			if !allowWrite {
				continue
			}
			out.Register(cloneDestructiveTool(name, root, backups, m, t))
		case name == "spawn_agent":
			// spawn_agent is wired only through extraTools, where the
			// worker has already applied depth and parent-write gates.
			continue
		default:
			if allowWrite {
				out.Register(t)
			}
		}
	}
	for _, t := range extraTools {
		out.Register(t)
	}
	return out
}

// cloneReadOnlyTool builds a fresh instance of the named read-only tool
// against the supplied project root. Falls back to the source instance
// if the name is unknown (forward compatibility).
func cloneReadOnlyTool(name, root string, src tools.Tool) tools.Tool {
	switch name {
	case "read_file":
		return tools.NewReadFileTool(root)
	case "search_codebase":
		return tools.NewSearchCodebaseTool(root)
	case "list_directory":
		return tools.NewListDirectoryTool(root)
	}
	return src
}

// cloneDestructiveTool builds a fresh instance of the named destructive
// tool, wired to the per-job BackupManager. write_file/patch_file are
// further wrapped in pathLockTool so concurrent jobs writing to the same
// absolute path serialise via Manager.acquirePathLock.
func cloneDestructiveTool(name, root string, backups *session.BackupManager, m *Manager, src tools.Tool) tools.Tool {
	switch name {
	case "write_file":
		return &pathLockTool{inner: tools.NewWriteFileTool(root, backups), m: m, root: root, paramKey: "path"}
	case "patch_file":
		return &pathLockTool{inner: tools.NewPatchFileTool(root, backups), m: m, root: root, paramKey: "path"}
	case "execute_command":
		// No path-lock for execute_command in v1; spec says "kernel
		// handles process isolation".
		return tools.NewExecuteCommandTool(root)
	}
	return src
}

// ─────────────────────────────────────────────────────────────────────────
// Path-lock wrapper
// ─────────────────────────────────────────────────────────────────────────

// pathLockTool wraps a tool whose Execute targets a single file path
// (carried in params under paramKey). Before delegating, it acquires the
// per-path mutex on the Manager so concurrent jobs writing to the same
// path serialise. Last write wins; writes are atomic at the filesystem
// layer (write_file uses temp-file-then-rename).
type pathLockTool struct {
	inner    tools.Tool
	m        *Manager
	root     string
	paramKey string
}

func (p *pathLockTool) Name() string            { return p.inner.Name() }
func (p *pathLockTool) Description() string     { return p.inner.Description() }
func (p *pathLockTool) Schema() json.RawMessage { return p.inner.Schema() }
func (p *pathLockTool) RequiresApproval() bool  { return p.inner.RequiresApproval() }

func (p *pathLockTool) Execute(ctx context.Context, raw json.RawMessage) (tools.ToolResult, error) {
	abs := p.extractPath(raw)
	if abs != "" && p.m != nil {
		mu := p.m.acquirePathLock(abs)
		mu.Lock()
		defer mu.Unlock()
	}
	return p.inner.Execute(ctx, raw)
}

// extractPath pulls the "path" param out of the raw arguments and
// resolves it relative to root. Returns "" on parse failure or missing
// path — the inner tool's own validation will then surface the error.
func (p *pathLockTool) extractPath(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	v, ok := obj[p.paramKey]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return ""
	}
	abs, err := resolveAbsoluteForLock(p.root, s)
	if err != nil {
		return ""
	}
	return abs
}

// pathLockMap is a small alias documenting the shared lock map's intent.
type pathLockMap map[string]*sync.Mutex
