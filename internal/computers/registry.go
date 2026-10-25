package computers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/compat"
)

// registryVersion is bumped when the on-disk shape changes incompatibly.
// Readers refuse a newer version rather than silently misinterpreting it.
const registryVersion = compat.ComputerRegistryVersion

// FileName is the registry file inside the computers directory.
const FileName = "registry.json"

type registryFile struct {
	Version   int               `json:"version"`
	Computers []json.RawMessage `json:"computers"`
}

// Registry is the durable set of Packet Computers, backed by
// <home>/computers/registry.json.
//
// It is data-only: loading, listing, and saving records. It never contacts a
// machine, so every method here is local filesystem work.
type Registry struct {
	mu   sync.RWMutex
	dir  string
	byID map[string]Computer
	// unreadable holds rows this build could not decode or validate,
	// verbatim. They are written back on save so that editing one computer
	// on an older build does not silently delete a record a newer build
	// wrote -- the same rule the compat contract applies to every other
	// on-disk format.
	unreadable []json.RawMessage
}

// Unreadable reports how many stored rows this build skipped. They are
// preserved on disk; they are simply not listed.
func (r *Registry) Unreadable() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.unreadable)
}

// Load reads the registry from dir, creating an empty one if the file does
// not exist. A malformed or future-versioned file is an error rather than a
// silent reset — losing a machine list quietly is worse than failing loudly.
func Load(dir string) (*Registry, error) {
	r := &Registry{dir: dir, byID: map[string]Computer{}}
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("computers: empty registry dir")
	}
	data, err := atomicfile.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("computers: read registry: %w", err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("computers: parse registry: %w", err)
	}
	if f.Version > registryVersion {
		return nil, fmt.Errorf(
			"computers: registry version %d is newer than this build supports (%d)",
			f.Version, registryVersion,
		)
	}
	now := time.Now().UTC()
	for _, raw := range f.Computers {
		var c Computer
		if err := json.Unmarshal(raw, &c); err != nil {
			r.unreadable = append(r.unreadable, raw)
			continue
		}
		norm, nerr := c.normalize(now)
		if nerr != nil {
			// Skip an unusable row rather than failing the whole registry;
			// one bad record must not hide the others. Kept verbatim so a
			// later save writes it back rather than dropping it.
			r.unreadable = append(r.unreadable, raw)
			continue
		}
		// Preserve stored timestamps: normalize stamps UpdatedAt as a write
		// helper, which is wrong on a pure read.
		norm.UpdatedAt = c.UpdatedAt
		if !c.CreatedAt.IsZero() {
			norm.CreatedAt = c.CreatedAt
		}
		r.byID[norm.ID] = norm
	}
	return r, nil
}

// Dir returns the directory backing this registry.
func (r *Registry) Dir() string { return r.dir }

// List returns all computers sorted by name.
func (r *Registry) List() []Computer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Computer, 0, len(r.byID))
	for _, c := range r.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Get returns the computer with the given name (case-insensitive).
func (r *Registry) Get(name string) (Computer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := strings.ToLower(strings.TrimSpace(name))
	for _, c := range r.byID {
		if strings.ToLower(c.Name) == want {
			return c, true
		}
	}
	return Computer{}, false
}

// GetByID returns the computer with the exact stable id. Persisted jobs use it
// during resubmission so a historical run is never silently rebound by name.
func (r *Registry) GetByID(id string) (Computer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[strings.TrimSpace(id)]
	return c, ok
}

// WorkspaceIdentity returns a stable, non-secret digest of the endpoint and
// registered root a remote job is bound to. It deliberately excludes the
// mutable display name and registry id while including the pinned host key, so
// resubmission fails if a record is repointed or re-keyed under the same id.
func WorkspaceIdentity(c Computer, workingDir string) string {
	port := c.SSHPort
	if port == 0 {
		port = 22
	}
	root := strings.TrimSpace(workingDir)
	if c.Kind == KindSSH {
		root = path.Clean(root)
	}
	material := strings.Join([]string{
		string(c.Kind),
		strings.TrimSpace(c.SSHUser),
		strings.ToLower(strings.TrimSpace(c.SSHHost)),
		fmt.Sprintf("%d", port),
		strings.TrimSpace(c.SSHHostFingerprint),
		root,
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return fmt.Sprintf("pcws_sha256_%x", sum[:])
}

// Upsert validates and stores c, then persists the registry. Names are
// unique case-insensitively so `/computers <name>` is unambiguous.
func (r *Registry) Upsert(c Computer) (Computer, error) {
	now := time.Now().UTC()
	norm, err := c.normalize(now)
	if err != nil {
		return Computer{}, err
	}

	r.mu.Lock()
	for id, existing := range r.byID {
		if id != norm.ID && strings.EqualFold(existing.Name, norm.Name) {
			r.mu.Unlock()
			return Computer{}, &ErrInvalid{
				Reason: fmt.Sprintf("name %q is already used by computer %s", norm.Name, id),
			}
		}
	}
	if prev, ok := r.byID[norm.ID]; ok && !prev.CreatedAt.IsZero() {
		norm.CreatedAt = prev.CreatedAt
	}
	r.byID[norm.ID] = norm
	r.mu.Unlock()

	if err := r.save(); err != nil {
		return Computer{}, err
	}
	return norm, nil
}

// Remove deletes a computer by name and persists the result.
func (r *Registry) Remove(name string) (bool, error) {
	r.mu.Lock()
	want := strings.ToLower(strings.TrimSpace(name))
	var found string
	for id, c := range r.byID {
		if strings.ToLower(c.Name) == want {
			found = id
			break
		}
	}
	if found == "" {
		r.mu.Unlock()
		return false, nil
	}
	delete(r.byID, found)
	r.mu.Unlock()

	if err := r.save(); err != nil {
		return false, err
	}
	return true, nil
}

// save writes the registry with atomic temp-file-then-rename semantics,
// matching how sessions and jobs persist.
func (r *Registry) save() error {
	r.mu.RLock()
	computers := make([]Computer, 0, len(r.byID))
	for _, c := range r.byID {
		computers = append(computers, c)
	}
	unreadable := append([]json.RawMessage(nil), r.unreadable...)
	dir := r.dir
	r.mu.RUnlock()

	sort.Slice(computers, func(i, j int) bool {
		return strings.ToLower(computers[i].Name) < strings.ToLower(computers[j].Name)
	})

	f := registryFile{Version: registryVersion, Computers: make([]json.RawMessage, 0, len(computers)+len(unreadable))}
	for _, c := range computers {
		row, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("computers: marshal %s: %w", c.Name, err)
		}
		f.Computers = append(f.Computers, row)
	}
	f.Computers = append(f.Computers, unreadable...)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("computers: ensure dir: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("computers: marshal registry: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(dir, FileName), data, 0o600, ".registry.*.json.tmp"); err != nil {
		return fmt.Errorf("computers: %w", err)
	}
	return nil
}
