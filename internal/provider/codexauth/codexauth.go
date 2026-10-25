// Package codexauth reads and refreshes the OAuth credentials that the
// official Codex CLI writes to ~/.codex/auth.json when a user signs in with
// their ChatGPT subscription ("Sign in with ChatGPT" / auth_mode chatgpt).
//
// packetcode does not implement its own login flow: it piggybacks on the
// tokens the Codex CLI already obtained. The access token is a short-lived
// bearer credential; when it expires the refresh token is exchanged for a new
// pair against the OpenAI OAuth token endpoint, and the result is written back
// to auth.json so packetcode and the Codex CLI stay in sync.
//
// Only the subscription (ChatGPT) auth mode is supported here. If auth.json
// carries an OPENAI_API_KEY instead of OAuth tokens, callers should fall back
// to the ordinary key-based openai provider.
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/packetcode/packetcode/internal/atomicfile"
	"github.com/packetcode/packetcode/internal/diaglog"
)

const (
	// oauthClientID is the public client identifier the Codex CLI registers
	// with. It is not a secret — it is embedded in the released CLI binary and
	// is required for the refresh_token grant to be accepted.
	oauthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// defaultTokenURL is the OpenAI OAuth token endpoint used for the
	// refresh_token grant.
	defaultTokenURL = "https://auth.openai.com/oauth/token"

	// AuthModeChatGPT is the auth_mode value auth.json carries for a
	// subscription (ChatGPT) sign-in.
	AuthModeChatGPT = "chatgpt"
)

// Tokens is the OAuth credential set stored under the "tokens" key in
// auth.json. AccountID identifies the ChatGPT workspace/account the
// subscription belongs to and is sent as the chatgpt-account-id header.
type Tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// Store provides synchronized read and refresh access to a Codex auth.json
// file. It is safe for concurrent use. The file is re-read from disk on every
// Token call so that refreshes performed by the Codex CLI (or another
// packetcode process) are picked up automatically.
type Store struct {
	mu   sync.Mutex
	path string

	// Injectable for tests; production defaults are used when zero.
	tokenURL string
	clientID string
	http     *http.Client
	now      func() time.Time
}

// New returns a Store bound to the given auth.json path. The file is not read
// until the first Token call, so constructing a Store never fails.
func New(path string) *Store {
	return &Store{
		path:     path,
		tokenURL: defaultTokenURL,
		clientID: oauthClientID,
		http:     &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
	}
}

// DefaultPath returns the conventional Codex auth.json location,
// $CODEX_HOME/auth.json when CODEX_HOME is set, otherwise ~/.codex/auth.json.
func DefaultPath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

// authFile mirrors the on-disk shape. Unknown top-level keys are preserved on
// write via a separate generic decode so we never clobber fields the Codex CLI
// depends on.
type authFile struct {
	AuthMode     string `json:"auth_mode,omitempty"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY,omitempty"`
	Tokens       Tokens `json:"tokens"`
	LastRefresh  string `json:"last_refresh,omitempty"`
}

// Available reports whether the auth.json file exists, is readable, and holds
// subscription OAuth tokens (a non-empty access token). It returns a
// descriptive error otherwise so setup and `doctor` can explain what to do.
func (s *Store) Available() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	af, err := s.read()
	if err != nil {
		return err
	}
	if af.Tokens.AccessToken == "" {
		if af.OpenAIAPIKey != "" {
			return fmt.Errorf("%s holds an API key, not a ChatGPT subscription login; run `codex login` and choose Sign in with ChatGPT", s.path)
		}
		return fmt.Errorf("%s has no ChatGPT access token; run `codex login`", s.path)
	}
	return nil
}

// Token reloads auth.json from disk and returns the current tokens. It does not
// perform a network refresh — callers refresh reactively via Refresh when a
// request is rejected with 401.
func (s *Store) Token(_ context.Context) (Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	af, err := s.read()
	if err != nil {
		return Tokens{}, err
	}
	if af.Tokens.AccessToken == "" {
		return Tokens{}, fmt.Errorf("no ChatGPT access token in %s; run `codex login`", s.path)
	}
	return af.Tokens, nil
}

// Refresh exchanges the stored refresh token for a fresh credential pair and
// writes the result back to auth.json, preserving unrelated fields. The new
// tokens are returned. It is safe to call concurrently; the exchange is
// serialized by the Store mutex.
func (s *Store) Refresh(ctx context.Context) (Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	af, err := s.read()
	if err != nil {
		return Tokens{}, err
	}
	if af.Tokens.RefreshToken == "" {
		return Tokens{}, fmt.Errorf("no refresh token in %s; run `codex login`", s.path)
	}

	refreshed, err := s.exchange(ctx, af.Tokens.RefreshToken)
	if err != nil {
		diaglog.L().Warn("codex.token_refresh", "path", s.path, "error", diaglog.ErrText(err))
		return Tokens{}, err
	}
	diaglog.L().Info("codex.token_refresh", "path", s.path, "rotated_refresh_token", refreshed.RefreshToken != "")

	// The token endpoint may omit a rotated refresh token; keep the old one
	// when that happens. account_id is not part of the OAuth response, so it
	// is always carried forward from the existing file.
	next := Tokens{
		IDToken:      firstNonEmpty(refreshed.IDToken, af.Tokens.IDToken),
		AccessToken:  refreshed.AccessToken,
		RefreshToken: firstNonEmpty(refreshed.RefreshToken, af.Tokens.RefreshToken),
		AccountID:    af.Tokens.AccountID,
	}

	if err := s.write(next); err != nil {
		// The refresh succeeded on the wire; surface the write failure but the
		// returned tokens are still usable for the current process.
		return next, fmt.Errorf("persist refreshed tokens to %s: %w", s.path, err)
	}
	return next, nil
}

// read parses auth.json. Callers must hold s.mu.
func (s *Store) read() (authFile, error) {
	raw, err := atomicfile.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return authFile{}, fmt.Errorf("%s not found; run `codex login` and choose Sign in with ChatGPT", s.path)
		}
		return authFile{}, fmt.Errorf("read %s: %w", s.path, err)
	}
	var af authFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return authFile{}, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return af, nil
}

// write persists the given tokens back to auth.json, preserving any unknown
// top-level fields and updating last_refresh. It writes atomically via a
// temp file + rename and keeps 0600 permissions. Callers must hold s.mu.
func (s *Store) write(tokens Tokens) error {
	// Preserve unknown keys by decoding into a generic map first.
	obj := map[string]json.RawMessage{}
	if raw, err := atomicfile.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &obj)
	}

	tokBytes, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	obj["tokens"] = tokBytes

	ts, err := json.Marshal(s.now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	obj["last_refresh"] = ts

	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}

	return atomicfile.Write(s.path, out, 0o600, ".auth-*.tmp")
}

// tokenResponse is the OAuth token endpoint reply for a refresh_token grant.
type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchange performs the refresh_token grant. Callers must hold s.mu.
func (s *Store) exchange(ctx context.Context, refreshToken string) (tokenResponse, error) {
	payload := map[string]string{
		"client_id":     s.clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         "openid profile email",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return tokenResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(body))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet := readSnippet(resp.Body)
		return tokenResponse{}, fmt.Errorf("refresh token rejected: status %d: %s", resp.StatusCode, snippet)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode refresh response: %w", err)
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("refresh response contained no access token")
	}
	return tr, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// readSnippet returns the leading bytes of an error body for a diagnostic
// message. It reads until the cap rather than taking whatever one Read call
// happens to return: a single Read is allowed to deliver one byte, which would
// truncate the OAuth error description the user needs to act on.
func readSnippet(r io.Reader) string {
	buf, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(buf))
}
