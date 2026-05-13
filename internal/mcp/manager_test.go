package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBinaryPath is set by TestMain after compiling internal/mcp/cmd/stub.
var stubBinaryPath string

// TestMain compiles the stub MCP server binary used by the manager
// tests. If the Go toolchain is not on PATH the manager tests skip
// themselves rather than fail.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcp-stub-")
	if err == nil {
		bin := filepath.Join(dir, "stub")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		// We rely on the test process's working directory still being
		// inside the module — Go test sets cwd to the package dir,
		// which is internal/mcp/, so the source path is relative.
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/stub")
		// Suppress any GOFLAGS that might break the build.
		cmd.Env = os.Environ()
		if err := cmd.Run(); err == nil {
			stubBinaryPath = bin
		}
	}
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}

func TestManager_Start_RejectsUnsafeServerName(t *testing.T) {
	requireStub(t)
	mgr := NewManager(Config{
		Servers: []ServerConfig{{
			Name:       "../evil",
			Command:    stubBinaryPath,
			Enabled:    true,
			TimeoutSec: 2,
		}},
		LogDir:     t.TempDir(),
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})

	reports := mgr.Start(context.Background())
	require.Len(t, reports, 1)
	assert.Equal(t, "failed", reports[0].Status)
	assert.Contains(t, reports[0].Err, "invalid MCP server name")
}

func TestManager_Start_RejectsProtocolMismatch(t *testing.T) {
	requireStub(t)
	mgr := NewManager(Config{
		Servers: []ServerConfig{{
			Name:       "old",
			Command:    stubBinaryPath,
			Env:        map[string]string{"PACKETCODE_STUB_PROTOCOL_VERSION": "1900-01-01"},
			Enabled:    true,
			TimeoutSec: 2,
		}},
		LogDir:     t.TempDir(),
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})

	reports := mgr.Start(context.Background())
	require.Len(t, reports, 1)
	assert.Equal(t, "failed", reports[0].Status)
	assert.Contains(t, reports[0].Err, "unsupported protocol version")
}

func TestClient_DeathReason_PreservesNonZeroExit(t *testing.T) {
	requireStub(t)
	mgr := NewManager(Config{
		Servers: []ServerConfig{{
			Name:       "crashy",
			Command:    stubBinaryPath,
			Env:        map[string]string{"PACKETCODE_STUB_EXIT_AFTER_TOOLS": "7"},
			Enabled:    true,
			TimeoutSec: 2,
		}},
		LogDir:     t.TempDir(),
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})
	defer mgr.Shutdown(2 * time.Second)
	reports := mgr.Start(context.Background())
	require.Len(t, reports, 1)
	require.Equal(t, "running", reports[0].Status, reports[0].Err)
	cli, ok := mgr.Client("crashy")
	require.True(t, ok)

	for i := 0; i < 100 && cli.IsAlive(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, cli.IsAlive())
	reason := cli.DeathReason()
	require.Error(t, reason)
	assert.True(t, strings.Contains(reason.Error(), "exit status 7") || strings.Contains(reason.Error(), "ExitStatus 7"), "death reason = %v", reason)
}

func requireStub(t *testing.T) {
	t.Helper()
	if stubBinaryPath == "" {
		t.Skip("stub MCP binary not built (go toolchain unavailable?)")
	}
}

// TestManager_Start_MixedStatuses asserts the report slice mirrors the
// input order with one running, one disabled, and one failed entry.
func TestManager_Start_MixedStatuses(t *testing.T) {
	requireStub(t)
	logDir := t.TempDir()
	mgr := NewManager(Config{
		Servers: []ServerConfig{
			{
				Name:       "ok",
				Command:    stubBinaryPath,
				Enabled:    true,
				TimeoutSec: 5,
			},
			{
				Name:    "off",
				Command: stubBinaryPath,
				Enabled: false,
			},
			{
				Name:       "broken",
				Command:    filepath.Join(t.TempDir(), "does-not-exist"),
				Enabled:    true,
				TimeoutSec: 2,
			},
		},
		LogDir:     logDir,
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})
	defer mgr.Shutdown(2 * time.Second)

	reports := mgr.Start(context.Background())
	require.Len(t, reports, 3)
	assert.Equal(t, "ok", reports[0].Name)
	assert.Equal(t, "running", reports[0].Status)
	assert.Contains(t, reports[0].Command, filepath.Base(stubBinaryPath))
	assert.Equal(t, "off", reports[1].Name)
	assert.Equal(t, "disabled", reports[1].Status)
	assert.Equal(t, "broken", reports[2].Name)
	assert.Equal(t, "failed", reports[2].Status)
	assert.NotEmpty(t, reports[2].Err)

	clients := mgr.Clients()
	require.Len(t, clients, 1)
	assert.Equal(t, "ok", clients[0].Name())

	// Reports() returns a defensive copy.
	cached := mgr.Reports()
	require.Len(t, cached, 3)
	cached[0].Name = "mutated"
	assert.Equal(t, "ok", mgr.Reports()[0].Name)
}

func TestManager_StartAgainClosesPreviousClients(t *testing.T) {
	requireStub(t)
	logDir := t.TempDir()
	mgr := NewManager(Config{
		Servers: []ServerConfig{{
			Name:       "ok",
			Command:    stubBinaryPath,
			Enabled:    true,
			TimeoutSec: 5,
		}},
		LogDir:     logDir,
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})
	defer mgr.Shutdown(2 * time.Second)

	reports := mgr.Start(context.Background())
	require.Equal(t, "running", reports[0].Status, reports[0].Err)
	first, ok := mgr.Client("ok")
	require.True(t, ok)
	require.True(t, first.IsAlive())

	reports = mgr.Start(context.Background())
	require.Equal(t, "running", reports[0].Status, reports[0].Err)
	assert.False(t, first.IsAlive(), "previous client should be closed when Start runs again")
	second, ok := mgr.Client("ok")
	require.True(t, ok)
	assert.NotSame(t, first, second)
}

// TestManager_Start_ParallelSpawn uses a short delay on each stub so we
// can prove they spawn concurrently. With 4 stubs each at 200 ms delay,
// serial would take ~800 ms; parallel should finish well under 600 ms.
func TestManager_Start_ParallelSpawn(t *testing.T) {
	requireStub(t)
	logDir := t.TempDir()

	servers := []ServerConfig{}
	for i := 0; i < 4; i++ {
		servers = append(servers, ServerConfig{
			Name:    "p" + string(rune('a'+i)),
			Command: stubBinaryPath,
			Env:     map[string]string{"PACKETCODE_STUB_DELAY_MS": "200"},
			Enabled: true, TimeoutSec: 5,
		})
	}
	mgr := NewManager(Config{
		Servers:    servers,
		LogDir:     logDir,
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})
	defer mgr.Shutdown(2 * time.Second)

	start := time.Now()
	reports := mgr.Start(context.Background())
	elapsed := time.Since(start)

	require.Len(t, reports, 4)
	for _, r := range reports {
		assert.Equal(t, "running", r.Status, "expected all running, got %+v", r)
	}
	// 4 × 200ms parallel ≈ 200-400 ms; serial would be > 800ms. Use a
	// generous 700 ms ceiling so a slow CI machine doesn't flake.
	assert.Less(t, elapsed, 700*time.Millisecond, "parallel spawn took %s — likely serial", elapsed)
}

// TestManager_Shutdown_AllClients confirms Shutdown closes every alive
// client and that subsequent Clients() returns no entries.
func TestManager_Shutdown_AllClients(t *testing.T) {
	requireStub(t)
	logDir := t.TempDir()
	mgr := NewManager(Config{
		Servers: []ServerConfig{
			{Name: "a", Command: stubBinaryPath, Enabled: true, TimeoutSec: 5},
			{Name: "b", Command: stubBinaryPath, Enabled: true, TimeoutSec: 5},
		},
		LogDir:     logDir,
		ClientInfo: ClientInfo{Name: "packetcode-test", Version: "0.0.0"},
	})
	reports := mgr.Start(context.Background())
	for _, r := range reports {
		require.Equal(t, "running", r.Status, "%s failed: %s", r.Name, r.Err)
	}
	require.Len(t, mgr.Clients(), 2)

	require.NoError(t, mgr.Shutdown(2*time.Second))

	// After Shutdown the underlying clients should be marked dead.
	for _, name := range []string{"a", "b"} {
		c, ok := mgr.Client(name)
		require.True(t, ok, "client %s missing after Shutdown", name)
		assert.False(t, c.IsAlive(), "client %s should be marked dead", name)
	}
	assert.Empty(t, mgr.Clients())
}
