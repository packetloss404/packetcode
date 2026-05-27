package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/packetcode/packetcode/internal/ui/components/prompt"
)

// providerKeyPromptID routes prompt.SubmitMsg back into the provider-
// key flow. Any other prompt use case must pick a distinct id.
const providerKeyPromptID = "provider-key"

// providerKeyValidatedMsg is posted by the background validation
// goroutine spawned from handlePromptSubmit. The App handler then
// persists the key, re-seeds the registry, and reopens the picker.
type providerKeyValidatedMsg struct {
	slug string
	key  string
	err  error
}

// providerHasKey reports whether the named provider already has a
// usable key via env var or on-disk config. Ollama never needs one.
func (a *App) providerHasKey(slug string) bool {
	if !a.providerRequiresKey(slug) {
		return true
	}
	if a.deps.Config == nil {
		return false
	}
	return a.deps.Config.GetProviderKey(slug) != ""
}

// openProviderKeyPrompt hides the picker and shows the prompt modal
// asking the user to paste an API key for slug. Handles the Ollama
// short-circuit (keyless provider, message the user and re-open the
// picker) and the missing-factory case (cannot rebuild → cannot set
// a new key, so we explain instead of opening a dead-end prompt).
func (a *App) openProviderKeyPrompt(slug string) tea.Cmd {
	a.picker.Hide()
	if !a.providerRequiresKey(slug) {
		a.conversation.AppendSystem(fmt.Sprintf("provider: %s is keyless — no API key required", slug))
		return a.openProviderPicker()
	}
	if a.deps.Factories == nil {
		a.conversation.AppendSystem("provider: cannot set key — factories not wired")
		return nil
	}
	if _, ok := a.deps.Factories[slug]; !ok {
		a.conversation.AppendSystem(fmt.Sprintf("provider: unknown slug %q", slug))
		return nil
	}
	a.prompt = prompt.New(providerKeyPromptID)
	a.prompt.Resize(a.width, a.height)
	title := fmt.Sprintf("Set API key — %s", slug)
	desc := fmt.Sprintf("Paste your %s API key. It will be validated and saved to ~/.packetcode/config.toml.", slug)
	a.prompt.Open(slug, title, desc, true)
	return nil
}

func (a *App) providerRequiresKey(slug string) bool {
	if a.deps.Config == nil {
		return slug != "ollama"
	}
	if pc, ok := a.deps.Config.Providers[slug]; ok {
		return pc.RequiresAPIKey(slug)
	}
	return slug != "ollama"
}

// handlePromptSubmit dispatches prompt.SubmitMsg. Currently only the
// provider-key flow uses the prompt; other ids are ignored defensively.
func (a *App) handlePromptSubmit(msg prompt.SubmitMsg) (tea.Model, tea.Cmd) {
	if msg.PromptID != providerKeyPromptID {
		a.prompt.Hide()
		return a, nil
	}
	key := strings.TrimSpace(msg.Value)
	if key == "" {
		a.prompt.SetError("API key cannot be empty")
		return a, nil
	}
	slug := msg.Context
	factory, ok := a.deps.Factories[slug]
	if !ok {
		a.prompt.SetError(fmt.Sprintf("unknown provider %q", slug))
		return a, nil
	}
	// Kick validation off on a background goroutine so the TUI stays
	// responsive — OpenAI's /v1/models ping can take a few seconds on a
	// cold network. We leave the prompt visible with no error text so
	// the user sees they're waiting; the returned tea.Cmd resolves to
	// providerKeyValidatedMsg.
	a.prompt.SetError("validating…")
	return a, func() tea.Msg {
		prov := factory(key)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := prov.ValidateKey(ctx, key)
		return providerKeyValidatedMsg{slug: slug, key: key, err: err}
	}
}

// handleProviderKeyValidated finalises the flow once the background
// ValidateKey resolves. On success: persist to config, re-seed the
// registry with a fresh Provider instance carrying the new key, close
// the prompt, and reopen the provider picker so the user sees the row
// update to "key present". On failure: keep the prompt open with the error
// so the user can retry or paste a different key.
func (a *App) handleProviderKeyValidated(msg providerKeyValidatedMsg) (tea.Model, tea.Cmd) {
	if !a.prompt.Visible() || a.prompt.Context() != msg.slug {
		// User cancelled or moved on before validation returned. Drop
		// the result silently.
		return a, nil
	}
	if msg.err != nil {
		a.prompt.SetError("invalid key: " + msg.err.Error())
		return a, nil
	}
	if a.deps.Config == nil {
		a.prompt.SetError("no config loaded — cannot save key")
		return a, nil
	}
	if err := a.deps.Config.SetProviderKey(msg.slug, msg.key); err != nil {
		a.prompt.SetError("save failed: " + err.Error())
		return a, nil
	}
	// Replace the provider in the registry with a fresh instance that
	// carries the new key. Register() is upsert-by-slug, so this works
	// whether the slug was previously registered or not.
	factory := a.deps.Factories[msg.slug]
	newProv := factory(msg.key)
	a.deps.Registry.Register(newProv)
	a.deps.Registry.InvalidateCachedModels(msg.slug)
	a.conversation.AppendSystem(fmt.Sprintf("provider: %s key saved ✓", msg.slug))
	a.prompt.Hide()
	return a, a.openProviderPicker()
}
