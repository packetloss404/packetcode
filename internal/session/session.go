// Package session manages on-disk conversation persistence and the file
// backup stack that backs the /undo slash command.
//
// Layout under ~/.packetcode:
//
//	sessions/<id>.json      one file per session, written atomically and
//	                        fsynced before the rename
//	backups/<id>/...        per-session file snapshots for /undo
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/compat"
	"github.com/packetcode/packetcode/internal/handoff"
	"github.com/packetcode/packetcode/internal/provider"
)

// Session is the in-memory + on-disk record of a single conversation.
type Session struct {
	FormatVersion int       `json:"format_version,omitempty"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	ComputerID    string    `json:"computer_id,omitempty"`
	WorkingDir    string    `json:"working_dir,omitempty"`
	// WorkspaceIdentity binds new remote sessions to endpoint, pinned host
	// key, and registered root in addition to the user-facing computer id.
	// Legacy sessions may omit it and retain the older id/root validation.
	WorkspaceIdentity string             `json:"workspace_identity,omitempty"`
	Messages          []provider.Message `json:"messages"`
	Cache             CacheState         `json:"cache,omitempty"`
	// SpecialistCapsule is local-only handoff state. It is not part of the
	// model transcript or Sugar Conduit telemetry.
	SpecialistCapsule *handoff.SpecialistCapsule `json:"specialist_capsule,omitempty"`
	TokenUsage        TokenUsage                 `json:"token_usage"`
	Cost              CostInfo                   `json:"cost"`
}

// currentFormatVersion is defined by the compatibility contract rather than
// here, so one file lists every on-disk version. See internal/compat and
// docs/compatibility.md.
const currentFormatVersion = compat.SessionVersion

// CacheState is the persisted cache lineage for a conversation. The session
// ID remains stable across resumes; only explicit transcript compaction
// advances the generation.
type CacheState struct {
	CompactionGeneration int `json:"compaction_generation,omitempty"`
}

type TokenUsage struct {
	// TotalInput and TotalOutput are cumulative across every request in the
	// session — they drive cost, not the context gauge.
	TotalInput  int `json:"total_input"`
	TotalOutput int `json:"total_output"`

	// ContextTokens is the current context-window occupancy: the token count
	// of the most recent request's prompt plus its completion, i.e. roughly
	// what the next request will re-send. Unlike TotalInput it does not
	// accumulate across turns, so the gauge reflects the live conversation
	// size and drops after a /compact. Zero until the first usage report.
	ContextTokens int `json:"context_tokens"`

	// TotalCacheCreation and TotalCacheRead are the cumulative cached-input
	// subsets of TotalInput, not additions to it: providers that bill cache
	// writes and reads at different rates need the split, but summing the
	// three would double-count every cached prompt. Both stay zero for
	// providers that never report cache figures.
	TotalCacheCreation int `json:"total_cache_creation,omitempty"`
	TotalCacheRead     int `json:"total_cache_read,omitempty"`

	// ContextCacheCreation and ContextCacheRead are the cache subsets of the
	// most recent request's prompt, tracking ContextTokens rather than the
	// cumulative totals. The statusline reports live context occupancy, so it
	// needs the split for *this* prompt; cumulative figures would exceed the
	// window. Like ContextTokens they are overwritten, not accumulated.
	ContextCacheCreation int `json:"context_cache_creation,omitempty"`
	ContextCacheRead     int `json:"context_cache_read,omitempty"`
}

type CostInfo struct {
	TotalUSD float64 `json:"total_usd"`
}

// Summary is a lightweight projection of Session for list views.
type Summary struct {
	ID           string
	Name         string
	UpdatedAt    time.Time
	Provider     string
	Model        string
	WorkingDir   string
	MessageCount int
	TokenUsage   TokenUsage
	Cost         CostInfo
}

// Manager owns the active session and reads/writes session files.
// Methods on Manager are safe for concurrent use.
type Manager struct {
	dir                  string
	modelToolResultLimit int
	mu                   sync.RWMutex
	current              *Session
	// cacheRead and cacheWrite override how cached input is priced. Pointers
	// rather than floats so "not configured" is distinguishable from a
	// deliberate zero -- a provider that genuinely serves cache reads free is
	// a legitimate setting, and a plain float could not say it.
	cacheRead  *float64
	cacheWrite *float64
}

func NewManager(dir string) *Manager {
	return NewManagerWithModelToolResultLimit(dir, configuredModelToolResultLimit())
}

// NewManagerWithModelToolResultLimit is primarily an evaluation/testing seam.
// Production uses NewManager and PACKETCODE_MODEL_TOOL_RESULT_LIMIT_BYTES.
func NewManagerWithModelToolResultLimit(dir string, limit int) *Manager {
	if limit <= 0 {
		limit = DefaultModelToolResultLimit
	}
	return &Manager{dir: dir, modelToolResultLimit: limit}
}

// New creates a fresh session with a UUID and the given provider/model
// pair, sets it as current, and persists an initial empty record.
func (m *Manager) New(providerSlug, model string) (*Session, error) {
	now := time.Now().UTC()
	s := &Session{
		FormatVersion: currentFormatVersion,
		ID:            uuid.NewString(),
		Name:          "untitled",
		CreatedAt:     now,
		UpdatedAt:     now,
		Provider:      providerSlug,
		Model:         model,
		Messages:      []provider.Message{},
	}
	m.mu.Lock()
	m.current = s
	m.mu.Unlock()
	if err := m.Save(); err != nil {
		return nil, err
	}
	// A copy, matching Current. Returning the manager's own *Session let a
	// caller mutate state the manager guards with a mutex, from outside it --
	// and Save writes whatever m.current points at, so such a mutation would
	// silently persist. Current was already careful about this; New and Load
	// were the two doors left open.
	return cloneSession(s), nil
}

// BindWorkspace records the remote computer identity for the active session.
// Local sessions deliberately remain unbound for backward compatibility with
// PacketCode's historically portable local transcripts.
func (m *Manager) BindWorkspace(computerID, workingDir string, workspaceIdentity ...string) error {
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("bind workspace: no current session")
	}
	m.current.ComputerID = strings.TrimSpace(computerID)
	m.current.WorkingDir = strings.TrimSpace(workingDir)
	m.current.WorkspaceIdentity = optionalIdentity(workspaceIdentity)
	m.mu.Unlock()
	return m.Save()
}

// ValidateWorkspace prevents a remote transcript from being resumed against a
// different computer (or against the local filesystem) and prevents an older
// local transcript from being silently attached to a remote machine.
func ValidateWorkspace(s *Session, computerID, workingDir string, workspaceIdentity ...string) error {
	if s == nil {
		return fmt.Errorf("session is nil")
	}
	computerID = strings.TrimSpace(computerID)
	workingDir = strings.TrimSpace(workingDir)
	identity := optionalIdentity(workspaceIdentity)
	if s.ComputerID == "" {
		if computerID != "" {
			return fmt.Errorf("session %s is not bound to a Packet Computer; start a new SSH session", s.ID)
		}
		return nil
	}
	if computerID == "" {
		return fmt.Errorf("session %s belongs to Packet Computer %s; restart with --computer", s.ID, s.ComputerID)
	}
	if s.ComputerID != computerID {
		return fmt.Errorf("session %s belongs to Packet Computer %s, not %s", s.ID, s.ComputerID, computerID)
	}
	if s.WorkingDir != "" && workingDir != "" && s.WorkingDir != workingDir {
		return fmt.Errorf("session %s belongs to remote root %s, not %s", s.ID, s.WorkingDir, workingDir)
	}
	if s.WorkspaceIdentity != "" && s.WorkspaceIdentity != identity {
		return fmt.Errorf("session %s belongs to a different Packet Computer endpoint or registered root", s.ID)
	}
	return nil
}

func optionalIdentity(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// Current returns a defensive copy of the active session (nil if none).
func (m *Manager) Current() *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSession(m.current)
}

// Load reads a session by ID and sets it as current.
func (m *Manager) Load(id string) (*Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(m.dir, id+".json")
	data, err := atomicfile.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	if err := validateSessionID(s.ID); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	if s.ID != id {
		return nil, fmt.Errorf("decode session: id mismatch %q != %q", s.ID, id)
	}
	// Refuse a session written by a newer build, before anything can be saved
	// over it.
	//
	// This was the most damaging gap in the whole contract. A newer session
	// decoded cleanly here -- encoding/json discards fields it does not know --
	// and migrateSession, seeing a version above its own, changed nothing and
	// reported nothing. The session then loaded, looking entirely normal, and
	// the next message saved it back through writeSessionFile: everything the
	// newer build had written and this one could not see was gone, from a file
	// the user never touched, with no error at any point.
	if s.FormatVersion > currentFormatVersion {
		return nil, fmt.Errorf("load session %s: %w", id,
			compat.TooNew("session", s.FormatVersion, currentFormatVersion))
	}
	if migrateSession(&s, m.modelToolResultLimit) {
		// Persist the additive projection/version upgrade without making an old
		// conversation appear newly active in the session list.
		if err := writeSessionFile(m.dir, &s); err != nil {
			return nil, fmt.Errorf("migrate session: %w", err)
		}
	}
	m.mu.Lock()
	m.current = &s
	m.mu.Unlock()
	return cloneSession(&s), nil
}

// Save writes the current session to disk atomically.
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.current
	if s == nil {
		return fmt.Errorf("save session: no current session")
	}
	s.UpdatedAt = time.Now().UTC()
	return writeSessionFile(m.dir, s)
}

// AddMessage appends m to the current session and auto-saves.
func (m *Manager) AddMessage(msg provider.Message) error {
	ensureMessageModelProjection(&msg, m.modelToolResultLimit)
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("add message: no current session")
	}
	m.current.Messages = append(m.current.Messages, msg)
	// Auto-name from the first user prompt (40-char window, no path-y chars).
	if m.current.Name == "untitled" && msg.Role == provider.RoleUser && msg.Content != "" {
		m.current.Name = sanitizeName(msg.Content)
	}
	m.mu.Unlock()
	return m.Save()
}

// UpdateUsage adds a usage delta from a stream completion to the current
// session and recomputes the running USD cost using the supplied per-1M
// rates. Auto-saves.
func (m *Manager) UpdateUsage(usage provider.Usage, inputPer1M, outputPer1M float64) error {
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("update usage: no current session")
	}
	m.current.TokenUsage.TotalInput += usage.InputTokens
	m.current.TokenUsage.TotalOutput += usage.OutputTokens
	// Cache counts accumulate alongside the totals but are a subset of
	// TotalInput, which provider.Usage already reports inclusive of cached
	// input — adding them here would bill every cached prompt twice.
	m.current.TokenUsage.TotalCacheCreation += usage.CacheCreationInputTokens
	m.current.TokenUsage.TotalCacheRead += usage.CacheReadInputTokens
	// Current context occupancy = this request's prompt + completion. Each
	// request reports its full prompt size (not a delta), so overwriting —
	// rather than accumulating — tracks the live conversation size. Only
	// update when the provider actually reported input tokens, so a usage
	// report that omits them doesn't zero the gauge mid-session.
	if usage.InputTokens > 0 {
		m.current.TokenUsage.ContextTokens = usage.InputTokens + usage.OutputTokens
		// The cache split belongs to the same request as ContextTokens, so it
		// is overwritten in the same branch: carrying a stale split forward
		// would describe a prompt that is no longer in the window.
		m.current.TokenUsage.ContextCacheCreation = usage.CacheCreationInputTokens
		m.current.TokenUsage.ContextCacheRead = usage.CacheReadInputTokens
	}
	// Cached input is billed at a fraction of fresh input, and this line used
	// to ignore that: it multiplied TotalInput -- which the comment above says
	// is cache-inclusive -- by the full rate, so a session running at 93% cache
	// hits reported roughly six times its real cost. The counts were already
	// being recorded correctly; only this arithmetic was wrong.
	m.current.Cost.TotalUSD = provider.EstimateCost(
		m.current.TokenUsage.TotalInput,
		m.current.TokenUsage.TotalOutput,
		m.current.TokenUsage.TotalCacheRead,
		m.current.TokenUsage.TotalCacheCreation,
		inputPer1M, outputPer1M,
		m.cacheReadMultiplier(), m.cacheWriteMultiplier(),
	)
	m.mu.Unlock()
	return m.Save()
}

// SetCacheMultipliers overrides how cached input is priced relative to fresh
// input for this session's provider.
//
// Optional. Unset, the package defaults apply, which are right for OpenAI and
// right for Anthropic reads; Anthropic charges a premium for cache writes and
// is the reason this exists. Values below zero are ignored -- a negative
// multiplier would subtract from the bill.
func (m *Manager) SetCacheMultipliers(read, write float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if read >= 0 {
		m.cacheRead = &read
	}
	if write >= 0 {
		m.cacheWrite = &write
	}
}

// cacheReadMultiplier and cacheWriteMultiplier resolve the configured value or
// the package default. Callers hold m.mu.
func (m *Manager) cacheReadMultiplier() float64 {
	if m.cacheRead != nil {
		return *m.cacheRead
	}
	return provider.CacheReadMultiplier
}

func (m *Manager) cacheWriteMultiplier() float64 {
	if m.cacheWrite != nil {
		return *m.cacheWrite
	}
	return provider.CacheWriteMultiplier
}

// SetContextTokens immediately updates the live context occupancy without
// changing cumulative billing totals.
func (m *Manager) SetContextTokens(tokens int) error {
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("set context tokens: no current session")
	}
	m.current.TokenUsage.ContextTokens = tokens
	// The caller is supplying a locally estimated occupancy for a prompt no
	// provider has priced yet, so the cache split from the previous request
	// no longer describes it — and left in place it could exceed the new
	// total. The next usage report refills it.
	m.current.TokenUsage.ContextCacheCreation = 0
	m.current.TokenUsage.ContextCacheRead = 0
	m.mu.Unlock()
	return m.Save()
}

// SetSpecialistCapsule persists bounded, local-only handoff state without
// changing Messages or cache lineage.
func (m *Manager) SetSpecialistCapsule(capsule handoff.SpecialistCapsule, maxBytes int) error {
	normalized := handoff.Normalize(capsule, maxBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return fmt.Errorf("set specialist capsule: no current session")
	}
	previous := m.current.SpecialistCapsule
	previousUpdatedAt := m.current.UpdatedAt
	m.current.SpecialistCapsule = &normalized
	m.current.UpdatedAt = time.Now().UTC()
	if err := writeSessionFile(m.dir, m.current); err != nil {
		m.current.SpecialistCapsule = previous
		m.current.UpdatedAt = previousUpdatedAt
		return err
	}
	return nil
}

// ReplaceMessages swaps the current session transcript and saves it.
func (m *Manager) ReplaceMessages(messages []provider.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return fmt.Errorf("replace messages: no current session")
	}
	previousMessages := m.current.Messages
	previousUpdatedAt := m.current.UpdatedAt
	m.current.Messages = cloneMessages(messages)
	ensureModelProjections(m.current.Messages, m.modelToolResultLimit)
	m.current.UpdatedAt = time.Now().UTC()
	if err := writeSessionFile(m.dir, m.current); err != nil {
		m.current.Messages = previousMessages
		m.current.UpdatedAt = previousUpdatedAt
		return err
	}
	return nil
}

// ReplaceMessagesAfterCompaction atomically swaps the transcript and advances
// its cache lineage. A failed disk write rolls both changes back in memory.
func (m *Manager) ReplaceMessagesAfterCompaction(messages []provider.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return fmt.Errorf("replace messages after compaction: no current session")
	}
	previousMessages := m.current.Messages
	previousCache := m.current.Cache
	previousUpdatedAt := m.current.UpdatedAt
	m.current.Messages = cloneMessages(messages)
	ensureModelProjections(m.current.Messages, m.modelToolResultLimit)
	m.current.Cache.CompactionGeneration++
	m.current.UpdatedAt = time.Now().UTC()
	if err := writeSessionFile(m.dir, m.current); err != nil {
		m.current.Messages = previousMessages
		m.current.Cache = previousCache
		m.current.UpdatedAt = previousUpdatedAt
		return err
	}
	return nil
}

// List returns every session sorted newest-first.
// List returns every readable session, newest first.
//
// Files it could not read or parse are reported by ListWithProblems rather
// than surfaced here, so existing callers keep the same shape.
func (m *Manager) List() ([]Summary, error) {
	out, _, err := m.ListWithProblems()
	return out, err
}

// ListWithProblems is List plus the sessions it could not read.
//
// A corrupt or unreadable session file used to be skipped in silence, so it
// simply vanished from /resume -- indistinguishable, from the user's chair,
// from a session that never existed. That is the wrong failure for the one
// command whose entire job is to find a conversation the user knows they had.
// The problems are reported alongside, never in place of, the sessions that
// did load.
func (m *Manager) ListWithProblems() ([]Summary, []string, error) {
	var problems []string
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	out := make([]Summary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := atomicfile.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", e.Name(), err))
			continue
		}
		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", e.Name(), err))
			continue
		}
		if err := validateSessionID(s.ID); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", e.Name(), err))
			continue
		}
		if s.FormatVersion > currentFormatVersion {
			// Reported, not listed. Offering it would put a session in
			// /resume that refuses to open, and the point of reporting
			// problems here is that the user learns why a conversation they
			// remember is not on the list.
			problems = append(problems, fmt.Sprintf("%s: %s", e.Name(),
				compat.TooNew("session", s.FormatVersion, currentFormatVersion)))
			continue
		}
		if e.Name() != s.ID+".json" {
			// A file whose name disagrees with the id inside it. Loading by
			// either spelling would give a different answer, so neither is
			// offered without saying why.
			problems = append(problems, fmt.Sprintf("%s: contains session id %q", e.Name(), s.ID))
			continue
		}
		out = append(out, Summary{
			ID:           s.ID,
			Name:         s.Name,
			UpdatedAt:    s.UpdatedAt,
			Provider:     s.Provider,
			Model:        s.Model,
			WorkingDir:   s.WorkingDir,
			MessageCount: len(s.Messages),
			TokenUsage:   s.TokenUsage,
			Cost:         s.Cost,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	sort.Strings(problems)
	return out, problems, nil
}

// ResolveID accepts either a full session ID (exact match) or a unique
// 8-character prefix.
func (m *Manager) ResolveID(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty session id")
	}
	summaries, err := m.List()
	if err != nil {
		return "", fmt.Errorf("list failed: %w", err)
	}
	for _, s := range summaries {
		if s.ID == ref {
			return s.ID, nil
		}
	}
	if len(ref) != 8 {
		return "", fmt.Errorf("session id prefix %q must be exactly 8 characters or a full session id", ref)
	}
	var matches []string
	for _, s := range summaries {
		if strings.HasPrefix(s.ID, ref) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matches %q", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q — matches %d sessions", ref, len(matches))
	}
}

// Delete removes a session file. Backups are the caller's responsibility
// (BackupManager.Cleanup) since the session package doesn't know the
// backup dir layout.
func (m *Manager) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(m.dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.mu.Lock()
	if m.current != nil && m.current.ID == id {
		m.current = nil
	}
	m.mu.Unlock()
	return nil
}

// Rename updates the session's display name and saves.
func (m *Manager) Rename(name string) error {
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("rename: no current session")
	}
	m.current.Name = sanitizeName(name)
	m.mu.Unlock()
	return m.Save()
}

func validateSessionID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.IsAbs(id) || filepath.Clean(id) != id || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid session id")
	}
	return nil
}

// sanitizeName converts a string to a session-name-safe form: trimmed,
// lowercase, spaces → hyphens, restricted to a-z0-9-_, capped at 40 chars.
func sanitizeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "untitled"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	out := *s
	out.Messages = cloneMessages(s.Messages)
	if s.SpecialistCapsule != nil {
		capsule := *s.SpecialistCapsule
		capsule.Constraints = append([]string(nil), s.SpecialistCapsule.Constraints...)
		capsule.ChangeBuckets = append([]string(nil), s.SpecialistCapsule.ChangeBuckets...)
		capsule.Changes = append([]handoff.Change(nil), s.SpecialistCapsule.Changes...)
		capsule.FailedGates = append([]handoff.FailedGate(nil), s.SpecialistCapsule.FailedGates...)
		capsule.ChangedAPIsSchemas = append([]string(nil), s.SpecialistCapsule.ChangedAPIsSchemas...)
		capsule.UnresolvedDecisions = append([]string(nil), s.SpecialistCapsule.UnresolvedDecisions...)
		capsule.Evidence = append([]handoff.Evidence(nil), s.SpecialistCapsule.Evidence...)
		out.SpecialistCapsule = &capsule
	}
	return &out
}

func cloneMessages(messages []provider.Message) []provider.Message {
	if messages == nil {
		return nil
	}
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if messages[i].ToolCalls != nil {
			out[i].ToolCalls = append([]provider.ToolCall(nil), messages[i].ToolCalls...)
		}
	}
	return out
}

func writeSessionFile(dir string, s *Session) error {
	if err := validateSessionID(s.ID); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	// Every write goes through here, so this is where "never save over a
	// newer session" is actually guaranteed rather than merely intended.
	// Checked against the in-memory version rather than by re-reading the
	// file: a session saves on every message, and a read per save would cost
	// real I/O to re-establish something Load already knows.
	if s.FormatVersion > currentFormatVersion {
		return fmt.Errorf("save session %s: %w", s.ID,
			compat.TooNew("session", s.FormatVersion, currentFormatVersion))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, s.ID+".json")
	// fsynced before the rename. The rename alone makes the write atomic for a
	// reader; it does not stop the rename reaching the disk ahead of the bytes
	// it names, which after a crash leaves a session file that exists and is
	// empty. See internal/atomicfile.
	if err := atomicfile.Write(path, data, 0o600, ".session.*.json.tmp"); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}
