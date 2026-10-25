package computers

import (
	"context"
	"io"
	"io/fs"

	"github.com/packetcode/packetcode/internal/procrun"
)

// Both workspace backends bound whole-file reads independently of the tools'
// rendered line limits. A file that grows while being read is bounded too.
const maxReadFileBytes = 16 << 20

// RuntimeBackend is the workspace boundary used by PacketCode's core tools.
// Implementations may be local or remote; tools deliberately do not know
// which transport is underneath them.
type RuntimeBackend interface {
	ComputerID() string
	Kind() Kind
	Root() string

	// Resolve confines path to Root. forWrite permits a missing leaf but still
	// requires every existing ancestor and an existing leaf to resolve inside
	// the workspace.
	Resolve(ctx context.Context, path string, forWrite bool) (string, error)
	// ReadFile accepts regular files only, up to maxReadFileBytes.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	ReadDir(ctx context.Context, path string) ([]FileEntry, error)
	Execute(ctx context.Context, command, cwd string, output io.Writer) (ExecResult, error)
	Close() error
}

// FileEntry is the transport-neutral subset of fs.FileInfo needed by the
// directory and search tools.
type FileEntry struct {
	Name  string
	Size  int64
	Mode  fs.FileMode
	IsDir bool
}

// ExecResult describes a completed command. A non-zero exit status is a
// normal result; transport/start failures are returned as errors.
type ExecResult struct {
	ExitCode int
	// Teardown carries what is known about killing the process tree when the
	// command was cancelled, and is nil when nothing was torn down. A backend
	// that cannot observe a teardown must report an unconfirmed outcome
	// rather than leaving this nil, because nil reads as "nothing to say"
	// and the difference matters to whoever is told the job stopped.
	Teardown *procrun.KillOutcome
}
