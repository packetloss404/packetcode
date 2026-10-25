package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// A reader and a writer contending on one path must both succeed.
//
// Without the retry this failed on Windows in roughly a quarter of attempts:
// os.Rename onto a path a reader has open returns ERROR_ACCESS_DENIED, and a
// read landing inside a rename returns ERROR_SHARING_VIOLATION. Neither means
// the file is bad, and callers that treated them as permanent lost records.
//
// On POSIX this asserts the same invariant, where it has always held.
func TestWriteAndReadContendOnOnePathWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	if err := os.WriteFile(path, []byte(`{"state":"running"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writes = 300
	var readErr, writeErr atomic.Value
	var reads int64

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				data, err := ReadFile(path)
				if err != nil {
					readErr.Store(err)
					return
				}
				if !bytes.Equal(data, []byte(`{"state":"running"}`)) && !bytes.Equal(data, []byte(`{"state":"completed"}`)) {
					readErr.Store(fmt.Errorf("reader observed a partial snapshot: %q", data))
					return
				}
				atomic.AddInt64(&reads, 1)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < writes; i++ {
			if err := Write(path, []byte(`{"state":"completed"}`), 0o600, ".record.*.json.tmp"); err != nil {
				writeErr.Store(err)
				return
			}
		}
	}()
	wg.Wait()

	if err, ok := writeErr.Load().(error); ok && err != nil {
		t.Fatalf("a write lost a race with a concurrent reader: %v", err)
	}
	if err, ok := readErr.Load().(error); ok && err != nil {
		t.Fatalf("a read lost a race with a concurrent writer: %v", err)
	}
	if got := atomic.LoadInt64(&reads); got == 0 {
		t.Fatal("the reader never completed a read, so nothing was contended")
	}
	// No temp files may be left behind by a retried rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "record.json" {
			t.Errorf("leftover file after contended writes: %s", e.Name())
		}
	}
}
