package approval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

// stripANSI removes lipgloss styling so tests can assert on visible
// characters without caring about colour escape codes.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// showFor builds a Model with the given tool + arguments and returns
// stripped-ANSI view output.
func showFor(t *testing.T, tool tools.Tool, args string) string {
	t.Helper()
	m := New()
	m.SetWidth(120)
	m.Show(tool, provider.ToolCall{ID: "t1", Name: tool.Name(), Arguments: args})
	return stripANSI(m.View())
}

// ────────────────────────────────────────────────────────────────────────────
// write_file branches
// ────────────────────────────────────────────────────────────────────────────

func TestRenderWriteFile_NewFile(t *testing.T) {
	root := t.TempDir()
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"brand/new.go","content":"package brand\n\nfunc Hi() {}\n"}`)
	assert.Contains(t, out, "(new file)")
	assert.Contains(t, out, "package brand")
	assert.Contains(t, out, "func Hi()")
	assert.Contains(t, out, "+3")
}

func TestRenderWriteFile_Overwrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("old line\n"), 0o644))
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"a.txt","content":"new line\n"}`)
	assert.Contains(t, out, "a.txt")
	assert.Contains(t, out, "- old line")
	assert.Contains(t, out, "+ new line")
}

func TestRenderWriteFile_IdenticalContent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("same\n"), 0o644))
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"a.txt","content":"same\n"}`)
	assert.Contains(t, out, "no changes")
}

func TestRenderWriteFile_BinaryFallback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bin.dat")
	require.NoError(t, os.WriteFile(target, []byte{0xff, 0xfe, 0x00, 0x80}, 0o644))
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"bin.dat","content":"text"}`)
	assert.Contains(t, out, "could not compute diff")
	assert.Contains(t, out, "binary")
}

func TestRenderWriteFile_PathTraversalFallback(t *testing.T) {
	root := t.TempDir()
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"../escape.txt","content":"x"}`)
	assert.Contains(t, out, "could not compute diff")
	assert.Contains(t, out, "outside project root")
}

func TestRenderWriteFile_MalformedJSONFallback(t *testing.T) {
	root := t.TempDir()
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{broken json`)
	assert.Contains(t, out, "could not compute diff")
}

// ────────────────────────────────────────────────────────────────────────────
// patch_file branches
// ────────────────────────────────────────────────────────────────────────────

func TestRenderPatchFile_Valid(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("hello world\n"), 0o644))
	tool := tools.NewPatchFileTool(root, tools.NoopBackupManager())
	out := showFor(t, tool, `{"path":"a.txt","patches":[{"search":"hello world","replace":"hi there"}]}`)
	assert.Contains(t, out, "- hello world")
	assert.Contains(t, out, "+ hi there")
	assert.Contains(t, out, "+1")
}

func TestRenderPatchFile_SearchNotFoundFallback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("foo\n"), 0o644))
	tool := tools.NewPatchFileTool(root, tools.NoopBackupManager())
	out := showFor(t, tool, `{"path":"a.txt","patches":[{"search":"bar","replace":"baz"}]}`)
	assert.Contains(t, out, "could not compute diff")
	assert.Contains(t, out, "not found")
	// fallback body shows the search/replace pair via summariseParams
	assert.Contains(t, out, "bar")
	assert.Contains(t, out, "baz")
}

func TestRenderPatchFile_AmbiguousFallback(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("dup\ndup\n"), 0o644))
	tool := tools.NewPatchFileTool(root, tools.NoopBackupManager())
	out := showFor(t, tool, `{"path":"a.txt","patches":[{"search":"dup","replace":"uniq"}]}`)
	assert.Contains(t, out, "could not compute diff")
	assert.Contains(t, out, "matches 2 times")
}

func TestRenderPatchFile_MalformedJSONFallback(t *testing.T) {
	root := t.TempDir()
	tool := tools.NewPatchFileTool(root, tools.NoopBackupManager())
	out := showFor(t, tool, `{broken`)
	assert.Contains(t, out, "could not compute diff")
}

// ────────────────────────────────────────────────────────────────────────────
// Registry / unregistered-tool behaviour
// ────────────────────────────────────────────────────────────────────────────

func TestRegisterAddsCustomRenderer(t *testing.T) {
	// Use a freshly-registered renderer, then delete so other tests
	// aren't affected. (In production Register is call-once at init time.)
	called := false
	Register("__test_tool", func(RenderContext) string {
		called = true
		return "custom-body"
	})
	t.Cleanup(func() { delete(renderers, "__test_tool") })

	m := New()
	m.SetWidth(80)
	m.Show(fakeTool{name: "__test_tool"}, provider.ToolCall{Name: "__test_tool", Arguments: "{}"})
	out := stripANSI(m.View())
	assert.True(t, called)
	assert.Contains(t, out, "custom-body")
}

func TestUnregisteredToolFallsThroughToSummariseParams(t *testing.T) {
	m := New()
	m.SetWidth(80)
	m.Show(fakeTool{name: "custom_shell"}, provider.ToolCall{
		Name:      "custom_shell",
		Arguments: `{"cmd":"ls -la"}`,
	})
	out := stripANSI(m.View())
	// summariseParams pretty-prints JSON
	assert.Contains(t, out, `"cmd"`)
	assert.Contains(t, out, "ls -la")
}

func TestApprovalHeaderShowsJobSourceAndQueueDepth(t *testing.T) {
	m := New()
	m.SetWidth(100)
	m.Show(fakeTool{name: "write_file"}, provider.ToolCall{
		Name:      "[job:abcd1234] write_file",
		Arguments: `{"path":"x.txt","content":"y"}`,
	})
	m.SetQueueDepth(3)
	out := stripANSI(m.View())
	assert.Contains(t, out, "[job:abcd1234] · write_file")
	assert.Contains(t, out, "1 of 3 pending approvals")
}

func TestRenderExecuteCommand_ShowsRuntimeSafety(t *testing.T) {
	out := showFor(t, fakeTool{name: "execute_command"}, `{"command":"dir","timeout_sec":5}`)
	assert.Contains(t, out, "$ dir")
	assert.Contains(t, out, "timeout: 5s")
	assert.Contains(t, out, "runtime:")
	assert.Contains(t, out, "Review shell syntax")
}

func TestView_HeaderAndActionsPresent(t *testing.T) {
	root := t.TempDir()
	tool := tools.NewWriteFileTool(root, nil)
	out := showFor(t, tool, `{"path":"x.txt","content":"y"}`)
	assert.Contains(t, out, "write_file")
	assert.Contains(t, out, "[Y]")
	assert.Contains(t, out, "[N]")
}

// ────────────────────────────────────────────────────────────────────────────
// fakeTool for Register / unregistered tests
// ────────────────────────────────────────────────────────────────────────────

type fakeTool struct {
	name string
}

func (f fakeTool) Name() string            { return f.name }
func (f fakeTool) Description() string     { return "fake" }
func (f fakeTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (f fakeTool) RequiresApproval() bool  { return true }
func (f fakeTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
