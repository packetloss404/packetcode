//go:build windows

package atomicfile

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep a real reader open across replacement. Retrying cannot make this test
// pass: a reader granting delete-sharing must allow the rename to proceed
// without first releasing its old snapshot.
func TestWindowsReadHandlePermitsAtomicReplacement(t *testing.T) {
	for _, kind := range []string{"absolute", "relative", "unicode", "long"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if kind == "long" {
				for len(dir) < 300 {
					dir = filepath.Join(dir, strings.Repeat("subdir", 6))
				}
				require.NoError(t, os.MkdirAll(dir, 0o700))
			}
			path := filepath.Join(dir, "record.json")
			if kind == "unicode" {
				path = filepath.Join(dir, "会話-état.json")
			}
			if kind == "relative" {
				t.Chdir(dir)
				path = "record.json"
			}
			old := []byte(`{"state":"running"}`)
			updated := []byte(`{"state":"completed","result":"complete new snapshot"}`)
			require.NoError(t, os.WriteFile(path, old, 0o600))
			reader, err := openReadFile(path)
			require.NoError(t, err)
			defer reader.Close()

			require.NoError(t, Write(path, updated, 0o600, ".record.*.tmp"))
			current, err := ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, updated, current)
			previous, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, old, previous, "a reader already holding the previous file must retain a complete old snapshot")
		})
	}
}

// An unrelated application may deliberately deny delete sharing. Keep that
// protection intact: failure must leave its original record and no temp file.
func TestWindowsWritePreservesDestinationHeldWithoutDeleteSharing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	old := []byte(`{"state":"running"}`)
	require.NoError(t, os.WriteFile(path, old, 0o600))
	reader, err := os.Open(path)
	require.NoError(t, err)
	defer reader.Close()

	err = Write(path, []byte(`{"state":"completed"}`), 0o600, ".record.*.tmp")
	require.Error(t, err)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, old, current)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "record.json", entries[0].Name())
}

func TestWindowsReadFileSupportsLongPathsAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	for len(dir) < 300 {
		dir = filepath.Join(dir, strings.Repeat("subdir", 6))
	}
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "record.json")
	_, err := ReadFile(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, Write(path, []byte(`{"state":"complete"}`), 0o600, ".record.*.tmp"))
	data, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"state":"complete"}`, string(data))
}

func TestWindowsReadFilePreservesPathErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"", "missing.json", "会話-absent.json"} {
		_, err := ReadFile(name)
		require.ErrorIs(t, err, os.ErrNotExist)
		var pathErr *os.PathError
		require.ErrorAs(t, err, &pathErr)
		assert.Equal(t, name, pathErr.Path)
	}
	require.NoError(t, os.WriteFile("record.json", []byte("complete"), 0o600))
	for _, name := range []string{"record.json", `record.json\`} {
		expected, expectedErr := os.ReadFile(name)
		actual, actualErr := ReadFile(name)
		assert.Equal(t, expected, actual)
		assert.Equal(t, expectedErr == nil, actualErr == nil, "preserve os.ReadFile path semantics for %q", name)
	}
}

func TestWindowsWritePreservesReadOnlyDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readonly.json")
	old := []byte(`{"state":"original"}`)
	require.NoError(t, os.WriteFile(path, old, 0o600))
	require.NoError(t, os.Chmod(path, 0o400))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	err := Write(path, []byte(`{"state":"replacement"}`), 0o600, ".record.*.tmp")
	require.Error(t, err)
	var linkErr *os.LinkError
	require.ErrorAs(t, err, &linkErr)
	assert.Equal(t, path, linkErr.New)
	assert.Equal(t, "rename", linkErr.Op)
	current, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, old, current)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
