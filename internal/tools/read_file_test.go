package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFile_FullFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644))

	tool := NewReadFileTool(root)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"hello.txt"}`))
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "    1 | alpha")
	assert.Contains(t, res.Content, "    3 | gamma")
	assert.Contains(t, res.Content, "(lines 1-3 of 3)")
}

func TestReadFile_LineRange(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("1\n2\n3\n4\n5\n"), 0o644))

	tool := NewReadFileTool(root)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"a.txt","start_line":2,"end_line":4}`))
	require.NoError(t, err)
	assert.Contains(t, res.Content, "    2 | 2")
	assert.Contains(t, res.Content, "    4 | 4")
	assert.NotContains(t, res.Content, "    5 | 5")
}

func TestReadFile_DefaultOutputIsCapped(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= maxReadFileLines+5; i++ {
		b.WriteString("line\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte(b.String()), 0o644))

	tool := NewReadFileTool(root)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "output truncated")
	assert.Equal(t, true, res.Metadata["truncated"])
	assert.Equal(t, maxReadFileLines, res.Metadata["end_line"])
}

func TestReadFile_LineRangeCanReadPastDefaultCap(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= maxReadFileLines+10; i++ {
		b.WriteString(fmt.Sprintf("%03d\n", i))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte(b.String()), 0o644))

	tool := NewReadFileTool(root)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt","start_line":405,"end_line":406}`))
	require.NoError(t, err)
	assert.Contains(t, res.Content, "  405 | 405")
	assert.Contains(t, res.Content, "  406 | 406")
	assert.NotContains(t, res.Content, "  404 | 404")
}

func TestReadFile_Missing(t *testing.T) {
	tool := NewReadFileTool(t.TempDir())
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"missing.txt"}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestReadFile_Traversal(t *testing.T) {
	tool := NewReadFileTool(t.TempDir())
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"../etc/passwd"}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "outside project root")
}

func TestReadFile_BinaryRejected(t *testing.T) {
	root := t.TempDir()
	// Invalid UTF-8.
	require.NoError(t, os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644))

	tool := NewReadFileTool(root)
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"blob.bin"}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "binary")
}

func TestReadFile_Schema(t *testing.T) {
	tool := NewReadFileTool(t.TempDir())
	var doc map[string]any
	require.NoError(t, json.Unmarshal(tool.Schema(), &doc))
	assert.Equal(t, "object", doc["type"])
}
