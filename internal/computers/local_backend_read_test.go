package computers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalReadFileSizeLimit(t *testing.T) {
	root := t.TempDir()
	backend, err := NewLocalBackend(root)
	require.NoError(t, err)
	file, err := os.Create(filepath.Join(root, "large.bin"))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxReadFileBytes+1))
	require.NoError(t, file.Close())

	data, err := backend.ReadFile(context.Background(), "large.bin")
	require.ErrorContains(t, err, "read limit")
	assert.Nil(t, data, "oversized files must fail before their contents reach the tool")

	require.NoError(t, os.Truncate(filepath.Join(root, "large.bin"), maxReadFileBytes))
	data, err = backend.ReadFile(context.Background(), "large.bin")
	require.NoError(t, err)
	assert.Len(t, data, maxReadFileBytes)
}

func TestLocalReadFileRejectsDirectoriesAndCancelledReads(t *testing.T) {
	root := t.TempDir()
	backend, err := NewLocalBackend(root)
	require.NoError(t, err)
	_, err = backend.ReadFile(context.Background(), ".")
	require.ErrorContains(t, err, "regular file")

	require.NoError(t, os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o600))
	data, err := backend.ReadFile(context.Background(), "empty.txt")
	require.NoError(t, err)
	assert.Empty(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = backend.ReadFile(ctx, "empty.txt")
	require.ErrorIs(t, err, context.Canceled)
}
