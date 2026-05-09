// Package session manages on-disk conversation persistence and the file
// backup stack that backs the /undo slash command.
//
// Layout under ~/.packetcode:
//
//	sessions/<id>.json      one file per session, atomic writes
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

	"github.com/packetcode/packetcode/internal/provider"
)

// Session is the in-memory + on-disk record of a single conversation.
type Session struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	Provider   string             `json:"provider"`
	Model      string             `json:"model"`
	Messages   []provider.Message `json:"messages"`
	TokenUsage TokenUsage         `json:"token_usage"`
	Cost       CostInfo           `json:"cost"`
}

type TokenUsage struct {
	TotalInput  int `json:"total_input"`
	TotalOutput int `json:"total_output"`
}

type CostInfo struct {
	TotalUSD float64 `json:"total_usd"`
}

// Summary is a lightweight projection of Session for list views.
type Summary struct {
	ID        string
	Name      string
	UpdatedAt time.Time
	Provider  string
	Model     string
}

// Manager owns the active session and reads/writes session files.
// Methods on Manager are safe for concurrent use.
type Manager struct {
	dir     string
	mu      sync.RWMutex
	current *Session
}

func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// New creates a fresh session with a UUID and the given provider/model
// pair, sets it as current, and persists an initial empty record.
func (m *Manager) New(providerSlug, model string) (*Session, error) {
	now := time.Now().UTC()
	s := &Session{
		ID:        uuid.NewString(),
		Name:      "untitled",
		CreatedAt: now,
		UpdatedAt: now,
		Provider:  providerSlug,
		Model:     model,
		Messages:  []provider.Message{},
	}
	m.mu.Lock()
	m.current = s
	m.mu.Unlock()
	if err := m.Save(); err != nil {
		return nil, err
	}
	return s, nil
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
	data, err := os.ReadFile(path)
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
	m.mu.Lock()
	m.current = &s
	m.mu.Unlock()
	return &s, nil
}

// Save writes the current session to disk atomically.
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.current
	if s == nil {
		return fmt.Errorf("save session: no current session")
	}
	if err := validateSessionID(s.ID); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.dir, s.ID+".json")
	tmp, err := os.CreateTemp(m.dir, ".session.*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// AddMessage appends m to the current session and auto-saves.
func (m *Manager) AddMessage(msg provider.Message) error {
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
	m.current.Cost.TotalUSD = float64(m.current.TokenUsage.TotalInput)*inputPer1M/1_000_000 +
		float64(m.current.TokenUsage.TotalOutput)*outputPer1M/1_000_000
	m.mu.Unlock()
	return m.Save()
}

// ReplaceMessages swaps the current session transcript and saves it.
func (m *Manager) ReplaceMessages(messages []provider.Message) error {
	m.mu.Lock()
	if m.current == nil {
		m.mu.Unlock()
		return fmt.Errorf("replace messages: no current session")
	}
	m.current.Messages = cloneMessages(messages)
	m.mu.Unlock()
	return m.Save()
}

// List returns every session sorted newest-first.
func (m *Manager) List() ([]Summary, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Summary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		if err := validateSessionID(s.ID); err != nil || e.Name() != s.ID+".json" {
			continue
		}
		out = append(out, Summary{
			ID:        s.ID,
			Name:      s.Name,
			UpdatedAt: s.UpdatedAt,
			Provider:  s.Provider,
			Model:     s.Model,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
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
