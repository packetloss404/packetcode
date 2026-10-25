//go:build linux || darwin

package computers

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/testwait"
)

func TestLocalReadFileRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "pipe")
	require.NoError(t, syscall.Mkfifo(name, 0o600))
	backend, err := NewLocalBackend(root)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, readErr := backend.ReadFile(context.Background(), "pipe")
		done <- readErr
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "regular file")
	case <-time.After(testwait.Timeout(2 * time.Second)):
		// Release an old implementation blocked in open/read before failing,
		// so the regression itself does not leave a goroutine behind.
		writer, openErr := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if openErr == nil {
			_ = writer.Close()
		}
		t.Fatal("ReadFile blocked on a FIFO with no writer")
	}
}
