package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/compat"
	"github.com/packetcode/packetcode/internal/config"
)

// approvalsFile is where per-workspace skill approvals are recorded.
const approvalsFile = "skill-approvals.json"

// approvalFormatVersion is written into the file so a later format can be
// recognised rather than guessed at. An unrecognised version is treated as
// "nothing is approved", which fails closed.
const approvalFormatVersion = compat.SkillApprovalVersion

// Approvals records which foreign project-scope skills a person has agreed to
// load, per workspace.
//
// The unit of approval is one skill in one workspace at one body. Anything
// coarser is wrong: approving a repository wholesale would carry over to
// skills it adds later, and approving a name globally would carry your
// decision about one repository's `deploy` into every other repository's.
//
// The body digest is what makes this an approval of content rather than of a
// filename. A repository that rewrites an approved skill after you approved it
// gets asked again, which is the entire point -- otherwise the first benign
// version buys permanent trust for every version after it.
type Approvals struct {
	path    string
	entries map[string]approvalEntry
}

type approvalEntry struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	Origin    string `json:"origin,omitempty"`
}

type approvalFile struct {
	Version   int             `json:"version"`
	Approvals []approvalEntry `json:"approvals"`
}

// LoadApprovals reads the approval store. A missing file is an empty store,
// not an error: nothing approved yet is the normal starting state.
//
// A store that cannot be read or parsed also yields an empty store plus the
// error. The caller decides whether to report it, but the resolved set is
// empty either way -- an unreadable approval file must never be read as
// "everything is approved".
func LoadApprovals() (*Approvals, error) {
	home, err := config.ResolveHomeDir()
	if err != nil {
		return &Approvals{entries: map[string]approvalEntry{}}, err
	}
	path := filepath.Join(home, approvalsFile)
	a := &Approvals{path: path, entries: map[string]approvalEntry{}}

	data, err := atomicfile.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, nil
		}
		return a, fmt.Errorf("read skill approvals: %w", err)
	}
	var parsed approvalFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return a, fmt.Errorf("parse skill approvals: %w", err)
	}
	if parsed.Version != approvalFormatVersion {
		return a, fmt.Errorf("skill approvals: unsupported format version %d (this build understands %d); "+
			"no skills are treated as approved", parsed.Version, approvalFormatVersion)
	}
	for _, e := range parsed.Approvals {
		a.entries[approvalKey(e.Workspace, e.Name)] = e
	}
	return a, nil
}

// Approved reports whether this exact body, of this skill, in this workspace,
// has been approved.
//
// workspace must already be canonical -- see CanonicalWorkspace. Canonicalising
// here instead would put a filesystem call on the path of every skill in every
// scan, and, worse, would let a caller pass a raw path to the read side while
// the write side stored a resolved one. That mismatch is silent: nothing errors,
// approvals simply never match, and every approved skill quietly reverts to
// pending. Both sides take the canonical form so they cannot drift.
func (a *Approvals) Approved(workspace, name, body string) bool {
	if a == nil {
		return false
	}
	e, ok := a.entries[approvalKey(workspace, name)]
	return ok && e.Digest == BodyDigest(body)
}

// Approve records an approval and writes the store.
//
// Written immediately rather than at exit: the decision the user just made has
// to survive a crash, and a store flushed at shutdown is a store that is empty
// exactly when something went wrong enough to matter.
func (a *Approvals) Approve(workspace, name, origin, body string) error {
	if a == nil {
		return fmt.Errorf("no approval store")
	}
	if a.entries == nil {
		a.entries = map[string]approvalEntry{}
	}
	ws, err := CanonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	a.entries[approvalKey(ws, name)] = approvalEntry{
		Workspace: ws,
		Name:      name,
		Digest:    BodyDigest(body),
		Origin:    origin,
	}
	return a.save()
}

// Revoke removes an approval. Absent is not an error: the caller asked for a
// state, and that state is what they get.
func (a *Approvals) Revoke(workspace, name string) error {
	if a == nil {
		return fmt.Errorf("no approval store")
	}
	ws, err := CanonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	delete(a.entries, approvalKey(ws, name))
	return a.save()
}

func (a *Approvals) save() error {
	if strings.TrimSpace(a.path) == "" {
		// An in-memory store, used by callers that resolved no home. Nothing
		// to write, and reporting an error would turn a working session into
		// a failing one over a file nobody asked for.
		return nil
	}
	out := approvalFile{Version: approvalFormatVersion}
	for _, e := range a.entries {
		out.Approvals = append(out.Approvals, e)
	}
	// Sorted so the file does not churn between runs over map order alone,
	// which would make it useless to keep under version control or to diff.
	sort.Slice(out.Approvals, func(i, j int) bool {
		if out.Approvals[i].Workspace != out.Approvals[j].Workspace {
			return out.Approvals[i].Workspace < out.Approvals[j].Workspace
		}
		return out.Approvals[i].Name < out.Approvals[j].Name
	})
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	// Sync and replace with the same sharing-aware persistence used by jobs
	// and sessions; retain the exact sorted JSON and trailing newline.
	return atomicfile.Write(a.path, append(data, '\n'), 0o600, ".skill-approvals-*")
}

// BodyDigest is the content identity an approval is bound to.
func BodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func approvalKey(workspace, name string) string {
	// The workspace path can contain anything a path can, so the separator has
	// to be one it cannot. A NUL is not legal in a path on any platform this
	// runs on, which is what makes the key unambiguous.
	return workspace + "\x00" + strings.TrimSpace(name)
}

// CanonicalWorkspace normalises a working directory so the same project does
// not accumulate several approvals under different spellings of its path.
//
// Both the read and the write side go through this. On Windows especially, the
// same directory routinely arrives as an 8.3 short name and as its long form,
// and a store keyed by whichever one happened to be passed matches nothing.
func CanonicalWorkspace(workspace string) (string, error) {
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		return "", fmt.Errorf("no workspace")
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}
	// EvalSymlinks so /var and /private/var, or a symlinked checkout, resolve
	// to one entry. It fails on a path that does not exist, which is not a
	// reason to refuse -- fall back to the absolute form.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}
