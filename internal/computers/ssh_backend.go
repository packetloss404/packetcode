package computers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/packetcode/packetcode/internal/diaglog"
	"github.com/packetcode/packetcode/internal/procrun"
)

const (
	sshDialTimeout        = 10 * time.Second
	sshKeepaliveEvery     = 30 * time.Second
	sshCancelDrainTimeout = 2 * time.Second
	sshMaxReadFileBytes   = maxReadFileBytes
)

// SSHBackend maintains one authenticated SSH connection and one SFTP client.
// Command executions use independent SSH channels on the persistent
// connection, while file tools share the SFTP subsystem.
type SSHBackend struct {
	computer Computer
	root     string
	client   *ssh.Client
	sftp     *sftp.Client
	agent    io.ReadWriteCloser

	// sftpMu serializes access to the shared SFTP client. SSH command
	// sessions deliberately do not hold this lock: ssh.Client multiplexes
	// independent channels, and a long-running command must not block file
	// reads, keepalives, or transport shutdown.
	sftpMu    sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
}

// NewSSHBackend connects to a pinned SSH computer. Authentication uses the
// SSH_AUTH_SOCK and a configured identity path (or conventional ~/.ssh
// identities). Passwords and private-key contents are never persisted by
// PacketCode.
func NewSSHBackend(ctx context.Context, computer Computer) (*SSHBackend, error) {
	if computer.Kind != KindSSH {
		return nil, fmt.Errorf("ssh backend: computer %q is %s, not ssh", computer.Name, computer.Kind)
	}
	norm, err := computer.normalize(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(norm.SSHUser) == "" {
		return nil, fmt.Errorf("ssh backend: computer %q has no SSH user", norm.Name)
	}
	if len(norm.ProjectRoots) == 0 || !path.IsAbs(norm.ProjectRoots[0]) {
		return nil, fmt.Errorf("ssh backend: project root must be an absolute POSIX path")
	}

	auth, agentConn, err := sshAuthMethods(norm.SSHIdentityFile)
	if err != nil {
		return nil, err
	}
	callback, err := pinnedHostKeyCallback(norm.SSHHostFingerprint)
	if err != nil {
		if agentConn != nil {
			_ = agentConn.Close()
		}
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            norm.SSHUser,
		Auth:            auth,
		HostKeyCallback: callback,
		Timeout:         sshDialTimeout,
	}
	addr := net.JoinHostPort(norm.SSHHost, fmt.Sprintf("%d", norm.SSHPort))
	dialer := net.Dialer{Timeout: sshDialTimeout, KeepAlive: sshKeepaliveEvery}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if agentConn != nil {
			_ = agentConn.Close()
		}
		diaglog.L().Warn("ssh.connect", "computer", norm.Name, "addr", addr, "user", norm.SSHUser, "stage", "dial", "error", err.Error())
		return nil, fmt.Errorf("ssh %s: dial: %w", norm.Name, err)
	}
	_ = raw.SetDeadline(time.Now().Add(sshDialTimeout))
	conn, chans, reqs, err := ssh.NewClientConn(raw, addr, config)
	if err != nil {
		_ = raw.Close()
		if agentConn != nil {
			_ = agentConn.Close()
		}
		diaglog.L().Warn("ssh.connect", "computer", norm.Name, "addr", addr, "user", norm.SSHUser, "stage", "handshake", "error", err.Error())
		return nil, fmt.Errorf("ssh %s: handshake: %w", norm.Name, err)
	}
	_ = raw.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, chans, reqs)
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		if agentConn != nil {
			_ = agentConn.Close()
		}
		return nil, fmt.Errorf("ssh %s: start sftp: %w", norm.Name, err)
	}
	resolvedRoot, err := sftpClient.RealPath(path.Clean(norm.ProjectRoots[0]))
	if err != nil {
		_ = sftpClient.Close()
		_ = client.Close()
		if agentConn != nil {
			_ = agentConn.Close()
		}
		return nil, fmt.Errorf("ssh %s: resolve project root: %w", norm.Name, err)
	}
	info, err := sftpClient.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		_ = sftpClient.Close()
		_ = client.Close()
		if agentConn != nil {
			_ = agentConn.Close()
		}
		if err != nil {
			return nil, fmt.Errorf("ssh %s: inspect project root: %w", norm.Name, err)
		}
		return nil, fmt.Errorf("ssh %s: project root %s is not a directory", norm.Name, resolvedRoot)
	}
	b := &SSHBackend{
		computer: norm,
		root:     path.Clean(resolvedRoot),
		client:   client,
		sftp:     sftpClient,
		agent:    agentConn,
		closed:   make(chan struct{}),
	}
	diaglog.L().Info("ssh.connect", "computer", norm.Name, "addr", addr, "user", norm.SSHUser,
		"root", b.root, "agent", agentConn != nil)
	go b.keepalive()
	return b, nil
}

func (b *SSHBackend) ComputerID() string { return b.computer.ID }
func (b *SSHBackend) Kind() Kind         { return KindSSH }
func (b *SSHBackend) Root() string       { return b.root }

func (b *SSHBackend) Close() error {
	var first error
	b.closeOnce.Do(func() {
		close(b.closed)
		// Close the SSH transport first. This interrupts command and SFTP
		// channels without waiting for sftpMu, so Close remains a reliable
		// escape hatch when an operation is blocked in network I/O.
		if b.client != nil {
			first = b.client.Close()
		}
		if b.sftp != nil {
			if err := b.sftp.Close(); first == nil {
				first = err
			}
		}
		if b.agent != nil {
			if err := b.agent.Close(); first == nil {
				first = err
			}
		}
	})
	return first
}

func (b *SSHBackend) keepalive() {
	ticker := time.NewTicker(sshKeepaliveEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if b.client != nil {
				_, _, _ = b.client.SendRequest("keepalive@openssh.com", true, nil)
			}
		case <-b.closed:
			return
		}
	}
}

func pinnedHostKeyCallback(want string) (ssh.HostKeyCallback, error) {
	want = strings.TrimSpace(want)
	if !strings.HasPrefix(want, "SHA256:") {
		return nil, fmt.Errorf("ssh backend: a SHA256 host-key fingerprint is required; refusing unpinned host")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if got != want {
			return fmt.Errorf("host key mismatch for %s (%s): got %s, want %s", hostname, remote, got, want)
		}
		return nil
	}, nil
}

func sshAuthMethods(identityFile string) ([]ssh.AuthMethod, io.ReadWriteCloser, error) {
	var methods []ssh.AuthMethod
	var agentConn io.ReadWriteCloser
	socket := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK"))
	if socket == "" && runtime.GOOS == "windows" {
		socket = `\\.\pipe\openssh-ssh-agent`
	}
	if socket != "" {
		if conn, err := dialSSHAgent(socket); err == nil {
			agentConn = conn
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	paths := []string{}
	if strings.TrimSpace(identityFile) != "" {
		paths = append(paths, identityFile)
	} else if home, err := os.UserHomeDir(); err == nil {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			paths = append(paths, filepath.Join(home, ".ssh", name))
		}
	}
	var keyErrors []string
	for _, keyPath := range paths {
		expanded, err := expandIdentityPath(keyPath)
		if err != nil {
			keyErrors = append(keyErrors, err.Error())
			continue
		}
		data, err := os.ReadFile(expanded)
		if err != nil {
			if !os.IsNotExist(err) || strings.TrimSpace(identityFile) != "" {
				keyErrors = append(keyErrors, fmt.Sprintf("%s: %v", expanded, err))
			}
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			var passphraseErr *ssh.PassphraseMissingError
			if errors.As(err, &passphraseErr) {
				keyErrors = append(keyErrors, fmt.Sprintf("%s is encrypted; load it into ssh-agent", expanded))
			} else {
				keyErrors = append(keyErrors, fmt.Sprintf("%s: %v", expanded, err))
			}
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
		if strings.TrimSpace(identityFile) != "" {
			break
		}
	}
	if len(methods) == 0 {
		if agentConn != nil {
			_ = agentConn.Close()
		}
		detail := strings.Join(keyErrors, "; ")
		if detail != "" {
			detail = ": " + detail
		}
		return nil, nil, fmt.Errorf("ssh backend: no usable public-key authentication%s", detail)
	}
	return methods, agentConn, nil
}

func dialSSHAgent(socket string) (io.ReadWriteCloser, error) {
	if runtime.GOOS == "windows" && strings.HasPrefix(strings.ToLower(socket), `\\.\pipe\`) {
		return os.OpenFile(socket, os.O_RDWR, 0)
	}
	return net.Dial("unix", socket)
}

func expandIdentityPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("identity path is empty")
	}
	if name == "~" || strings.HasPrefix(name, "~/") || strings.HasPrefix(name, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if name == "~" {
			return home, nil
		}
		name = filepath.Join(home, name[2:])
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func (b *SSHBackend) Resolve(ctx context.Context, name string, forWrite bool) (string, error) {
	b.sftpMu.Lock()
	defer b.sftpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return b.resolveLocked(name, forWrite)
}

func (b *SSHBackend) resolveLocked(name string, forWrite bool) (string, error) {
	candidate, err := remoteCandidate(b.root, name)
	if err != nil {
		return "", err
	}
	if !forWrite {
		resolved, err := b.sftp.RealPath(candidate)
		if err != nil {
			return "", err
		}
		if !remotePathWithin(b.root, resolved) {
			return "", fmt.Errorf("path %q resolves outside project root", name)
		}
		return path.Clean(resolved), nil
	}
	if _, err := b.sftp.Lstat(candidate); err == nil {
		resolved, err := b.sftp.RealPath(candidate)
		if err != nil {
			return "", err
		}
		if !remotePathWithin(b.root, resolved) {
			return "", fmt.Errorf("path %q resolves outside project root", name)
		}
		return path.Clean(resolved), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	ancestor := path.Dir(candidate)
	for {
		if _, err := b.sftp.Lstat(ancestor); err == nil {
			resolved, err := b.sftp.RealPath(ancestor)
			if err != nil {
				return "", err
			}
			if !remotePathWithin(b.root, resolved) {
				return "", fmt.Errorf("path %q has an ancestor outside project root", name)
			}
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := path.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("path %q has no existing ancestor", name)
		}
		ancestor = parent
	}
}

func remoteCandidate(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		name = "."
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", name)
	}
	clean := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path outside project root: %s", name)
	}
	candidate := path.Clean(path.Join(root, clean))
	if !remotePathWithin(root, candidate) {
		return "", fmt.Errorf("path outside project root: %s", name)
	}
	return candidate, nil
}

func remotePathWithin(root, target string) bool {
	root = path.Clean(root)
	target = path.Clean(target)
	return target == root || strings.HasPrefix(target, strings.TrimSuffix(root, "/")+"/")
}

func (b *SSHBackend) ReadFile(ctx context.Context, name string) ([]byte, error) {
	b.sftpMu.Lock()
	defer b.sftpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := b.resolveLocked(name, false)
	if err != nil {
		return nil, err
	}
	info, err := b.sftp.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("remote read requires a regular file")
	}
	if info.Size() > sshMaxReadFileBytes {
		return nil, fmt.Errorf("remote file exceeds %d-byte read limit", sshMaxReadFileBytes)
	}
	f, err := b.sftp.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, sshMaxReadFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > sshMaxReadFileBytes {
		return nil, fmt.Errorf("remote file exceeds %d-byte read limit", sshMaxReadFileBytes)
	}
	return data, nil
}

func (b *SSHBackend) WriteFile(ctx context.Context, name string, data []byte) error {
	b.sftpMu.Lock()
	defer b.sftpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := b.resolveLocked(name, true)
	if err != nil {
		return err
	}
	if err := b.sftp.MkdirAll(path.Dir(resolved)); err != nil {
		return fmt.Errorf("create remote parent dir: %w", err)
	}
	// Re-confine after directory creation to catch a symlink introduced in an
	// ancestor before opening the temporary file.
	resolved, err = b.resolveLocked(name, true)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, statErr := b.sftp.Stat(resolved); statErr == nil {
		mode = info.Mode().Perm()
	}
	tmp := path.Join(path.Dir(resolved), fmt.Sprintf(".packetcode-write-%d.tmp", time.Now().UnixNano()))
	f, err := b.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return fmt.Errorf("create remote temp: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = b.sftp.Remove(tmp)
	}
	if err := f.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod remote temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write remote temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = b.sftp.Remove(tmp)
		return fmt.Errorf("close remote temp: %w", err)
	}
	if err := b.sftp.PosixRename(tmp, resolved); err != nil {
		if fallbackErr := b.sftp.Rename(tmp, resolved); fallbackErr != nil {
			_ = b.sftp.Remove(tmp)
			return fmt.Errorf("rename remote temp: %w (fallback: %v)", err, fallbackErr)
		}
	}
	return nil
}

func (b *SSHBackend) ReadDir(ctx context.Context, name string) ([]FileEntry, error) {
	b.sftpMu.Lock()
	defer b.sftpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := b.resolveLocked(name, false)
	if err != nil {
		return nil, err
	}
	info, err := b.sftp.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", name)
	}
	entries, err := b.sftp.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, FileEntry{Name: entry.Name(), Size: entry.Size(), Mode: entry.Mode(), IsDir: entry.IsDir()})
	}
	return out, nil
}

func (b *SSHBackend) Execute(ctx context.Context, command, cwd string, output io.Writer) (ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecResult{ExitCode: -1}, nil
	}

	// Resolve and inspect cwd through the shared SFTP channel, then release
	// it before opening the command channel. This keeps SFTP operations
	// serialized without making a long-running command a global backend lock.
	b.sftpMu.Lock()
	resolved := b.root
	var err error
	if strings.TrimSpace(cwd) != "" && cwd != "." {
		resolved, err = b.resolveLocked(cwd, false)
		if err != nil {
			b.sftpMu.Unlock()
			return ExecResult{}, err
		}
	}
	info, err := b.sftp.Stat(resolved)
	if err != nil || !info.IsDir() {
		b.sftpMu.Unlock()
		if err != nil {
			return ExecResult{}, err
		}
		return ExecResult{}, fmt.Errorf("remote cwd %s is not a directory", cwd)
	}
	b.sftpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return ExecResult{ExitCode: -1}, nil
	}

	session, err := b.client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("open ssh session: %w", err)
	}
	defer session.Close()
	locked := &lockedWriter{dst: output}
	session.Stdout = locked
	session.Stderr = locked
	remoteCommand := "cd -- " + shellQuote(resolved) + " && " + command
	if err := session.Start(remoteCommand); err != nil {
		return ExecResult{}, fmt.Errorf("start remote command: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case runErr := <-done:
		if runErr == nil {
			return ExecResult{ExitCode: 0}, nil
		}
		if exitErr, ok := runErr.(*ssh.ExitError); ok {
			return ExecResult{ExitCode: exitErr.ExitStatus()}, nil
		}
		return ExecResult{}, runErr
	case <-ctx.Done():
		// One SIGTERM to the channel leader, which many sshd configurations
		// ignore outright, and no remote process group to fall back on. This
		// is reported as unconfirmed rather than success-shaped, because a
		// detached remote descendant may still be running.
		signalErr := session.Signal(ssh.SIGTERM)
		_ = session.Close()
		drainSSHSession(done, sshCancelDrainTimeout)
		return ExecResult{ExitCode: -1, Teardown: sshTeardownOutcome(signalErr)}, nil
	}
}

// drainSSHSession gives ssh.Session.Wait a bounded opportunity to observe a
// cancellation. A broken server or transport must not hold a worker (and in
// turn application shutdown) forever after Session.Close has been issued.
// Closing SSHBackend remains the authoritative way to tear down the shared
// transport if the session goroutine does not exit within this window.
// sshTeardownOutcome describes a remote cancellation honestly. There is no
// remote job object and no remote process group here, so nothing this
// function can say is ever Confirmed; the value exists so callers can tell
// "we could not verify" apart from "there was nothing to stop".
func sshTeardownOutcome(signalErr error) *procrun.KillOutcome {
	reason := "SIGTERM was sent to the remote session and the channel closed, but SSH offers no process-group teardown and sshd may ignore channel signals; a detached remote descendant may still be running"
	if signalErr != nil {
		reason = "the remote session could not even be signalled (" + signalErr.Error() + "); " + reason
	}
	return &procrun.KillOutcome{Method: procrun.KillMethodNone, Reason: reason}
}

func drainSSHSession(done <-chan error, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

type lockedWriter struct {
	mu  sync.Mutex
	dst io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}
