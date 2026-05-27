package jobs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/session"
	"github.com/packetcode/packetcode/internal/tools"
)

// makeMainRegistry returns a tools.Registry populated with the canonical
// per-job tool set (read-only + destructive). Helper for the registry
// tests.
func makeMainRegistry(t *testing.T, root string) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(tools.NewReadFileTool(root))
	reg.Register(tools.NewSearchCodebaseTool(root))
	reg.Register(tools.NewListDirectoryTool(root))
	reg.Register(tools.NewWriteFileTool(root, nil))
	reg.Register(tools.NewPatchFileTool(root, nil))
	reg.Register(tools.NewExecuteCommandTool(root))
	return reg
}

// TestRegistry_ReadOnlyByDefault asserts the read-only tools are
// included and destructive tools are NOT, when allowWrite=false.
func TestRegistry_ReadOnlyByDefault(t *testing.T) {
	root := t.TempDir()
	mgr := &Manager{cfg: Config{Tools: makeMainRegistry(t, root), Root: root}, pathLocks: pathLockMap{}}

	bm := session.NewBackupManager(t.TempDir(), "test-session")
	reg := mgr.buildJobToolRegistry(0, false /* allowWrite */, "abc12345", bm, nil)

	for _, name := range []string{"read_file", "search_codebase", "list_directory"} {
		_, ok := reg.Get(name)
		assert.True(t, ok, "read-only tool %s should be present", name)
	}
	for _, name := range []string{"write_file", "patch_file", "execute_command"} {
		_, ok := reg.Get(name)
		assert.False(t, ok, "destructive tool %s should be absent in read-only mode", name)
	}
}

// TestRegistry_AllowWriteIncludesDestructive asserts that allowWrite
// = true brings write_file/patch_file/execute_command into the per-job
// registry. write_file/patch_file should be wrapped by pathLockTool;
// execute_command is a plain instance (no lock in v1).
func TestRegistry_AllowWriteIncludesDestructive(t *testing.T) {
	root := t.TempDir()
	mgr := &Manager{cfg: Config{Tools: makeMainRegistry(t, root), Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")
	reg := mgr.buildJobToolRegistry(0, true, "abc12345", bm, nil)

	for _, name := range []string{"read_file", "search_codebase", "list_directory", "write_file", "patch_file", "execute_command"} {
		_, ok := reg.Get(name)
		assert.True(t, ok, "tool %s should be present in allowWrite mode", name)
	}
	wf, _ := reg.Get("write_file")
	_, isLockedTool := wf.(*pathLockTool)
	assert.True(t, isLockedTool, "write_file must be wrapped in pathLockTool")
	pf, _ := reg.Get("patch_file")
	_, isLockedTool = pf.(*pathLockTool)
	assert.True(t, isLockedTool, "patch_file must be wrapped in pathLockTool")
}

// TestRegistry_ExtraToolsAppended verifies the extraTools slot used to
// plug spawn_agent in without an import cycle.
func TestRegistry_ExtraToolsAppended(t *testing.T) {
	root := t.TempDir()
	mgr := &Manager{cfg: Config{Tools: makeMainRegistry(t, root), Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")

	extra := &noopTool{name: "spawn_agent", approval: true}
	reg := mgr.buildJobToolRegistry(0, false, "abc12345", bm, []tools.Tool{extra})

	got, ok := reg.Get("spawn_agent")
	require.True(t, ok)
	assert.Equal(t, extra, got, "extraTools should be wired through verbatim")
}

func TestRegistry_ReadOnlyDoesNotForwardUnknownTools(t *testing.T) {
	root := t.TempDir()
	parent := makeMainRegistry(t, root)
	parent.Register(&noopTool{name: "custom_safe", approval: false})
	mgr := &Manager{cfg: Config{Tools: parent, Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")

	reg := mgr.buildJobToolRegistry(0, false, "abc12345", bm, nil)

	_, ok := reg.Get("custom_safe")
	assert.False(t, ok, "read-only jobs must not inherit unknown main-session tools")
}

func TestRegistry_AllowWriteDoesNotForwardUnknownTools(t *testing.T) {
	root := t.TempDir()
	parent := makeMainRegistry(t, root)
	extra := &noopTool{name: "custom_safe", approval: false}
	parent.Register(extra)
	mgr := &Manager{cfg: Config{Tools: parent, Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")

	reg := mgr.buildJobToolRegistry(0, true, "abc12345", bm, nil)

	got, ok := reg.Get("custom_safe")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestRegistry_AllowWriteRootOverrideUsesWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	worktreeRoot := t.TempDir()
	mgr := &Manager{cfg: Config{Tools: makeMainRegistry(t, root), Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")

	reg := mgr.buildJobToolRegistry(0, true, "abc12345", bm, nil, worktreeRoot)

	read, ok := reg.Get("read_file")
	require.True(t, ok)
	assert.Equal(t, worktreeRoot, read.(*tools.ReadFileTool).Root)

	search, ok := reg.Get("search_codebase")
	require.True(t, ok)
	assert.Equal(t, worktreeRoot, search.(*tools.SearchCodebaseTool).Root)

	list, ok := reg.Get("list_directory")
	require.True(t, ok)
	assert.Equal(t, worktreeRoot, list.(*tools.ListDirectoryTool).Root)

	write, ok := reg.Get("write_file")
	require.True(t, ok)
	writeLocked := write.(*pathLockTool)
	assert.Equal(t, worktreeRoot, writeLocked.root)
	assert.Equal(t, worktreeRoot, writeLocked.inner.(*tools.WriteFileTool).Root)

	patch, ok := reg.Get("patch_file")
	require.True(t, ok)
	patchLocked := patch.(*pathLockTool)
	assert.Equal(t, worktreeRoot, patchLocked.root)
	assert.Equal(t, worktreeRoot, patchLocked.inner.(*tools.PatchFileTool).Root)

	execTool, ok := reg.Get("execute_command")
	require.True(t, ok)
	assert.Equal(t, worktreeRoot, execTool.(*tools.ExecuteCommandTool).Root)
}

func TestRegistry_DoesNotInheritMainSpawnAgent(t *testing.T) {
	root := t.TempDir()
	parent := makeMainRegistry(t, root)
	parent.Register(&noopTool{name: "spawn_agent", approval: true})
	mgr := &Manager{cfg: Config{Tools: parent, Root: root}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")

	reg := mgr.buildJobToolRegistry(1, false, "abc12345", bm, nil)

	_, ok := reg.Get("spawn_agent")
	assert.False(t, ok, "spawn_agent must only be included by the depth-aware extraTools factory")
}

// TestRegistry_NilParentToolsStillReturnsExtras lets tests that don't
// care about the canonical tool set wire only extras.
func TestRegistry_NilParentToolsStillReturnsExtras(t *testing.T) {
	mgr := &Manager{cfg: Config{Tools: nil}, pathLocks: pathLockMap{}}
	bm := session.NewBackupManager(t.TempDir(), "s")
	extra := &noopTool{name: "only_me"}
	reg := mgr.buildJobToolRegistry(0, false, "id", bm, []tools.Tool{extra})
	_, ok := reg.Get("only_me")
	assert.True(t, ok)
}
