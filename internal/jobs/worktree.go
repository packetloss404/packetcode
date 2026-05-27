package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type worktreeInfo struct {
	Root   string
	Path   string
	Branch string
	Base   string
	Note   string
}

func (m *Manager) prepareWorktree(ctx context.Context, j *Job) (worktreeInfo, error) {
	info := worktreeInfo{Root: m.cfg.Root}
	if !j.AllowWrite {
		return info, nil
	}
	created, err := createWorktree(ctx, m.cfg.Root, m.worktreesDir(), j.ID)
	if err != nil {
		m.setWorktree(j, worktreeInfo{Root: m.cfg.Root, Note: err.Error()})
		return info, err
	}
	if created.Root == "" {
		created.Root = m.cfg.Root
	}
	if created.Path != "" || created.Note != "" {
		m.setWorktree(j, created)
	}
	return created, nil
}

func (m *Manager) worktreesDir() string {
	if m.cfg.WorktreesDir != "" {
		return m.cfg.WorktreesDir
	}
	if m.cfg.JobsDir != "" {
		return filepath.Join(filepath.Dir(m.cfg.JobsDir), "worktrees")
	}
	return filepath.Join(os.TempDir(), "packetcode-worktrees")
}

func (m *Manager) setWorktree(j *Job, info worktreeInfo) {
	m.mu.Lock()
	if j.State.IsTerminal() {
		m.mu.Unlock()
		return
	}
	j.WorktreePath = info.Path
	j.WorktreeBranch = info.Branch
	j.WorktreeBase = info.Base
	j.WorktreeNote = info.Note
	activity := "worktree ready"
	message := info.Path
	if info.Path == "" {
		activity = "worktree unavailable"
		message = info.Note
	}
	m.stampSnapshotLocked(j, time.Now().UTC(), activity, message, false, false)
	subs := snapshotCallbacks(m.subscribers)
	snap := snapshotOf(j)
	persisted := toPersisted(j)
	m.mu.Unlock()

	_ = m.savePersistedSnapshot(persisted)
	m.fanOut(snap, subs)
}

func createWorktree(ctx context.Context, root, worktreesDir, id string) (worktreeInfo, error) {
	info := worktreeInfo{Root: root}
	if strings.TrimSpace(root) == "" {
		return info, fmt.Errorf("project root unavailable")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return info, fmt.Errorf("git not found; write-enabled background jobs require git worktree isolation")
	}
	repoRoot, out, err := gitOutput(ctx, gitPath, root, "rev-parse", "--show-toplevel")
	if err != nil {
		detail := strings.ToLower(out + " " + err.Error())
		if strings.Contains(detail, "not a git repository") || strings.Contains(detail, "not a git repo") {
			return info, fmt.Errorf("not a git repository; write-enabled background jobs require git worktree isolation")
		}
		if strings.Contains(detail, "dubious ownership") {
			return info, fmt.Errorf("git rejected repository ownership; run git status in the project and explicitly trust the repository if appropriate")
		}
		return info, fmt.Errorf("worktree repo check: %s", nonEmptyString(strings.TrimSpace(out), err.Error()))
	}
	base, out, err := gitOutput(ctx, gitPath, repoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return info, fmt.Errorf("worktree base ref: %s", nonEmptyString(strings.TrimSpace(out), err.Error()))
	}
	if err := os.MkdirAll(worktreesDir, 0o700); err != nil {
		return info, fmt.Errorf("worktree mkdir: %w", err)
	}
	worktreesDir, err = validateWorktreesDir(worktreesDir)
	if err != nil {
		return info, err
	}
	repoKey := repoWorktreeKey(repoRoot)
	repoWorktreesDir := filepath.Join(worktreesDir, repoKey)
	if err := ensureChildPath(worktreesDir, repoWorktreesDir); err != nil {
		return info, err
	}
	repoWorktreesDir, err = ensureRealDir(repoWorktreesDir, "worktree repo dir")
	if err != nil {
		return info, err
	}
	path := filepath.Join(repoWorktreesDir, id)
	if err := ensureChildPath(repoWorktreesDir, path); err != nil {
		return info, err
	}
	branch := "packetcode-job-" + id
	if err := validateBranch(ctx, gitPath, repoRoot, branch); err != nil {
		return info, err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if os.IsExist(err) {
			return info, fmt.Errorf("worktree path already exists: %s", path)
		}
		return info, fmt.Errorf("worktree path mkdir: %w", err)
	}
	if err := validateExistingDir(path, "worktree path"); err != nil {
		return info, err
	}
	_, out, err = gitOutput(ctx, gitPath, repoRoot, "worktree", "add", "-b", branch, path, base)
	if err != nil {
		_ = os.Remove(path)
		return info, fmt.Errorf("git worktree add: %s", nonEmptyString(strings.TrimSpace(out), err.Error()))
	}
	if err := writeWorktreeMetadata(worktreesDir, repoKey, id, repoRoot, branch, base, path); err != nil {
		return info, err
	}
	return worktreeInfo{
		Root:   path,
		Path:   path,
		Branch: branch,
		Base:   base,
	}, nil
}

func validateWorktreesDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("worktree dir: %w", err)
	}
	return ensureRealDir(abs, "worktree dir")
}

func validateBranch(ctx context.Context, gitPath, repoRoot, branch string) error {
	if !strings.HasPrefix(branch, "packetcode-job-") || strings.ContainsAny(branch, "\x00\r\n") || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid worktree branch %q", branch)
	}
	_, out, err := gitOutput(ctx, gitPath, repoRoot, "check-ref-format", "--branch", branch)
	if err != nil {
		return fmt.Errorf("invalid worktree branch %q: %s", branch, nonEmptyString(strings.TrimSpace(out), err.Error()))
	}
	return nil
}

type worktreeSentinel struct {
	JobID     string    `json:"job_id"`
	RepoKey   string    `json:"repo_key"`
	RepoRoot  string    `json:"repo_root"`
	Branch    string    `json:"branch"`
	Base      string    `json:"base"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

func writeWorktreeMetadata(worktreesDir, repoKey, id, repoRoot, branch, base, path string) error {
	metadataDir := filepath.Join(worktreesDir, "metadata")
	if err := ensureChildPath(worktreesDir, metadataDir); err != nil {
		return err
	}
	metadataDir, err := ensureRealDir(metadataDir, "worktree metadata dir")
	if err != nil {
		return err
	}
	repoMetadataDir := filepath.Join(metadataDir, repoKey)
	if err := ensureChildPath(metadataDir, repoMetadataDir); err != nil {
		return err
	}
	repoMetadataDir, err = ensureRealDir(repoMetadataDir, "worktree metadata repo dir")
	if err != nil {
		return err
	}
	metadataPath := filepath.Join(repoMetadataDir, id+".json")
	if err := ensureChildPath(repoMetadataDir, metadataPath); err != nil {
		return err
	}
	data, err := json.MarshalIndent(worktreeSentinel{
		JobID:     id,
		RepoKey:   repoKey,
		RepoRoot:  repoRoot,
		Branch:    branch,
		Base:      base,
		Path:      path,
		CreatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree sentinel marshal: %w", err)
	}
	f, err := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("worktree sentinel create: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(metadataPath)
		return fmt.Errorf("worktree sentinel write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(metadataPath)
		return fmt.Errorf("worktree sentinel close: %w", err)
	}
	return nil
}

func gitOutput(parent context.Context, gitPath, dir string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmdArgs := []string{"-C", dir}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, gitPath, cmdArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), string(out), err
}

func repoWorktreeKey(repoRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return hex.EncodeToString(sum[:])[:12]
}

func ensureRealDir(path, label string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("%s mkdir: %w", label, err)
	}
	if err := validateExistingDir(abs, label); err != nil {
		return "", err
	}
	_ = os.Chmod(abs, 0o700)
	return abs, nil
}

func validateExistingDir(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s stat: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symlink: %s", label, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", label, path)
	}
	return nil
}

func ensureChildPath(root, child string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, childAbs)
	if err != nil {
		return err
	}
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("worktree path escapes root: %s", child)
	}
	return nil
}

func nonEmptyString(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
