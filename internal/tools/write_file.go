package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const writeFileSchema = `{
  "type": "object",
  "properties": {
    "path":    { "type": "string", "description": "File path relative to the project root. Parent directories are created as needed." },
    "content": { "type": "string", "description": "Complete file contents. Overwrites any existing file." }
  },
  "required": ["path", "content"]
}`

type WriteFileTool struct {
	Root    string
	Backups BackupManager
}

func NewWriteFileTool(root string, backups BackupManager) *WriteFileTool {
	if backups == nil {
		backups = NoopBackupManager()
	}
	return &WriteFileTool{Root: root, Backups: backups}
}

func (*WriteFileTool) Name() string            { return "write_file" }
func (*WriteFileTool) RequiresApproval() bool  { return true }
func (*WriteFileTool) Schema() json.RawMessage { return json.RawMessage(writeFileSchema) }
func (*WriteFileTool) Description() string {
	return "Write a complete file (creating it or overwriting an existing file). Requires user approval. Backs up the previous contents so /undo can revert."
}

type writeFileParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Execute writes atomically: it streams content to a temp file in the
// destination directory, closes it, then renames it into place. This
// guards against half-written files if the process is killed mid-write.
func (t *WriteFileTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p writeFileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("write_file: parse params: %w", err)
	}
	abs, err := resolveWritePath(t.Root, p.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}

	if err := t.Backups.Backup(abs); err != nil {
		return ToolResult{Content: fmt.Sprintf("write_file: backup failed: %s", err), IsError: true}, nil
	}

	if err := atomicWrite(abs, []byte(p.Content)); err != nil {
		rollbackBackup(t.Backups, abs)
		return ToolResult{Content: fmt.Sprintf("write_file: %s", err), IsError: true}, nil
	}

	lineCount := strings.Count(p.Content, "\n")
	if !strings.HasSuffix(p.Content, "\n") && len(p.Content) > 0 {
		lineCount++
	}
	return ToolResult{
		Content: fmt.Sprintf("Wrote %s (%d bytes, %d lines).", p.Path, len(p.Content), lineCount),
		Metadata: map[string]any{
			"path":  p.Path,
			"bytes": len(p.Content),
			"lines": lineCount,
		},
	}, nil
}

// PreviewDiff computes the unified diff between the on-disk contents
// of path and the proposed content, without writing anything. Used by
// the approval renderer to show a real diff in the confirmation
// modal.
//
// Return shape:
//   - unified == "" && newFile == true  → path does not yet exist
//     (caller should render with diff.NewFile)
//   - unified == "" && newFile == false → proposed == current, no-op
//   - unified != ""                     → normal overwrite diff
//
// Mirrors read_file's binary guard so approval never attempts to
// render an invalid-UTF-8 diff.
func (t *WriteFileTool) PreviewDiff(path, content string) (string, bool, error) {
	abs, err := resolveWritePath(t.Root, path)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("preview_diff: %w", err)
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("preview_diff: %s appears to be binary", path)
	}
	if string(data) == content {
		return "", false, nil
	}
	unified, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(data)),
		B:        difflib.SplitLines(content),
		FromFile: path + " (current)",
		ToFile:   path + " (proposed)",
		Context:  3,
	})
	return unified, false, nil
}
