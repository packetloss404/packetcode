package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

func TestAppendToolArtifact_ExtractsKnownToolMetadata(t *testing.T) {
	call := provider.ToolCall{
		Name:      "execute_command",
		Arguments: `{"command":"go test ./internal/jobs"}`,
	}
	res := tools.ToolResult{
		Content: "$ go test ./internal/jobs\nok\n[exit 0]",
		Metadata: map[string]any{
			"exit_code": 0,
			"cwd":       "D:/repo",
		},
	}

	artifacts := appendToolArtifact(nil, call, res, nowForArtifactTest())
	require.Len(t, artifacts, 1)
	assert.Equal(t, "A1", artifacts[0].ID)
	assert.Equal(t, "test", artifacts[0].Kind)
	assert.Equal(t, "go test ./internal/jobs [exit 0]", artifacts[0].Summary)
	assert.Equal(t, "execute_command", artifacts[0].SourceTool)
}

func TestAppendToolArtifact_CapsPatchPreview(t *testing.T) {
	longDiff := stringsRepeat("diff line\n", maxArtifactPreview)
	call := provider.ToolCall{Name: "patch_file", Arguments: `{"path":"main.go"}`}
	res := tools.ToolResult{
		Content: "Applied 1 patch(es) to main.go.\n\n" + longDiff,
		Metadata: map[string]any{
			"path":        "main.go",
			"patch_count": 1,
		},
	}

	artifacts := appendToolArtifact(nil, call, res, nowForArtifactTest())
	require.Len(t, artifacts, 1)
	assert.Equal(t, "file_change", artifacts[0].Kind)
	assert.Equal(t, "main.go", artifacts[0].Path)
	assert.True(t, artifacts[0].Truncated)
	assert.LessOrEqual(t, len([]rune(artifacts[0].Preview)), maxArtifactPreview)
}

func TestAppendToolArtifact_CompactsPersistedMetadata(t *testing.T) {
	call := provider.ToolCall{Name: "spawn_agent", Arguments: `{"prompt":"child","wait":true}`}
	res := tools.ToolResult{
		Content: "[job:child1 - completed] done",
		Metadata: map[string]any{
			"job_id":    "child1",
			"state":     "completed",
			"artifacts": []any{map[string]any{"preview": strings.Repeat("x", 1024)}},
			"stdout":    strings.Repeat("log", 1024),
		},
	}

	artifacts := appendToolArtifact(nil, call, res, nowForArtifactTest())
	require.Len(t, artifacts, 1)
	assert.Equal(t, "spawned_job", artifacts[0].Kind)
	assert.Equal(t, "child1", artifacts[0].Metadata["job_id"])
	assert.NotContains(t, artifacts[0].Metadata, "artifacts")
	assert.NotContains(t, artifacts[0].Metadata, "stdout")
}

func TestAppendToolArtifact_CodeIntelSummaryIncludesScopeAndEngine(t *testing.T) {
	call := provider.ToolCall{
		Name:      "find_references",
		Arguments: `{"symbol":"NewServer","scope_path":"internal","file_glob":"**/*.go"}`,
	}
	res := tools.ToolResult{
		Content: "Found 2 reference(s) for \"NewServer\"",
		Metadata: map[string]any{
			"symbol":          "NewServer",
			"reference_count": 2,
			"engine":          "lexical-fallback",
			"confidence":      "low",
			"truncated":       true,
		},
	}

	artifacts := appendToolArtifact(nil, call, res, nowForArtifactTest())
	require.Len(t, artifacts, 1)
	assert.Equal(t, "code_intel", artifacts[0].Kind)
	assert.True(t, artifacts[0].Truncated)
	assert.Contains(t, artifacts[0].Summary, "2 reference(s) for NewServer")
	assert.Contains(t, artifacts[0].Summary, "scope internal")
	assert.Contains(t, artifacts[0].Summary, "glob **/*.go")
	assert.Contains(t, artifacts[0].Summary, "via lexical-fallback low confidence")
	assert.Contains(t, artifacts[0].Summary, "truncated")
}

func TestAppendWorktreeArtifacts_CapturesCommandGeneratedFiles(t *testing.T) {
	root := initTestGitRepo(t)
	worktreesDir := t.TempDir()
	info, err := createWorktree(context.Background(), root, worktreesDir, "abc12345")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(info.Path, "generated.txt"), []byte("hello\n"), 0o644))

	j := &Job{ID: "abc12345", WorktreePath: info.Path}
	artifacts := appendWorktreeArtifacts(context.Background(), nil, j)

	require.Len(t, artifacts, 1)
	assert.Equal(t, "worktree_diff", artifacts[0].Kind)
	assert.Contains(t, artifacts[0].Summary, "changed")
	assert.Contains(t, artifacts[0].Preview, "generated.txt")
}

func nowForArtifactTest() time.Time {
	return time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
}

func stringsRepeat(s string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(s)
	}
	return b.String()
}
