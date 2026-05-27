package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// displayOrder controls the order providers appear in the selector modal.
// It matches the order in the vision doc so the UI feels consistent.
var displayOrder = []string{"openai", "anthropic", "gemini", "minimax", "openrouter", "ollama"}

// DisplayOrder returns the canonical provider display order used by the TUI.
func DisplayOrder() []string {
	out := make([]string, len(displayOrder))
	copy(out, displayOrder)
	return out
}

// Registry holds the set of available providers and tracks the active
// (provider, model) pair. Hot-switching mutates only the active fields;
// the underlying Provider instances are long-lived.
type Registry struct {
	mu           sync.RWMutex
	providers    map[string]Provider
	active       Provider
	activeModel  string
	cachedModels map[string][]Model
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		providers:    map[string]Provider{},
		cachedModels: map[string][]Model{},
	}
}

// Register adds a provider keyed by its slug. Registering the same slug
// twice replaces the previous entry — useful for tests that swap mocks.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Slug()] = p
	if r.active != nil && r.active.Slug() == p.Slug() {
		r.active = p
	}
	delete(r.cachedModels, p.Slug())
}

// Get returns a provider by slug.
func (r *Registry) Get(slug string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[slug]
	return p, ok
}

// List returns providers in the canonical display order. Unknown slugs
// (anything not in displayOrder) come last in alphabetical order.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Provider, 0, len(r.providers))
	seen := map[string]bool{}
	for _, slug := range displayOrder {
		if p, ok := r.providers[slug]; ok {
			out = append(out, p)
			seen[slug] = true
		}
	}
	extras := make([]string, 0)
	for slug := range r.providers {
		if !seen[slug] {
			extras = append(extras, slug)
		}
	}
	sort.Strings(extras)
	for _, slug := range extras {
		out = append(out, r.providers[slug])
	}
	return out
}

// Slugs returns the registered provider slugs in display order.
func (r *Registry) Slugs() []string {
	list := r.List()
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.Slug()
	}
	return out
}

// SetActive switches the active provider and model atomically. The model is
// not validated against the provider's ListModels — callers (the slash
// command handler and the setup flow) are responsible for offering only
// valid choices.
func (r *Registry) SetActive(slug, modelID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[slug]
	if !ok {
		return fmt.Errorf("provider %q not registered", slug)
	}
	r.active = p
	r.activeModel = modelID
	return nil
}

// Active returns the currently active provider and model. The provider is
// nil and modelID is empty before SetActive has been called.
func (r *Registry) Active() (Provider, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active, r.activeModel
}

// CachedModels returns the most recent cached ListModels result for a
// provider slug, if one has been stored via SetCachedModels. The
// returned slice is a defensive copy so callers cannot mutate the
// cached entry.
func (r *Registry) CachedModels(slug string) ([]Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ms, ok := r.cachedModels[slug]
	if !ok {
		return nil, false
	}
	out := make([]Model, len(ms))
	copy(out, ms)
	return out, true
}

// SetCachedModels stores a snapshot of ms under the provider slug. A
// defensive copy is made so callers can re-use their slice afterwards.
// Passing a nil slice stores an empty entry — use InvalidateCachedModels
// to remove the key entirely.
func (r *Registry) SetCachedModels(slug string, ms []Model) {
	cp := make([]Model, len(ms))
	copy(cp, ms)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cachedModels[slug] = cp
}

// InvalidateCachedModels drops the cached entry for slug, forcing the
// next CachedModels call to miss.
func (r *Registry) InvalidateCachedModels(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cachedModels, slug)
}

// InitResult records what happened when initializing a single provider.
type InitResult struct {
	Slug  string
	Err   error // nil = healthy
	Model []Model
}

// InitializeAll attempts to validate every registered provider's key in
// parallel. Failures are returned as InitResult entries with non-nil Err so
// the caller can mark them unavailable in the UI without aborting startup.
//
// The keyFor callback resolves the API key for a slug — typically wired to
// config.Config.GetProviderKey so env-var overrides apply.
func (r *Registry) InitializeAll(ctx context.Context, keyFor func(slug string) string) []InitResult {
	r.mu.RLock()
	provs := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		provs = append(provs, p)
	}
	r.mu.RUnlock()

	results := make([]InitResult, len(provs))
	var wg sync.WaitGroup
	for i, p := range provs {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			res := InitResult{Slug: p.Slug()}
			key := keyFor(p.Slug())
			if err := p.ValidateKey(ctx, key); err != nil {
				res.Err = err
				results[i] = res
				return
			}
			models, err := p.ListModels(ctx)
			if err != nil {
				res.Err = err
				results[i] = res
				return
			}
			res.Model = models
			results[i] = res
		}(i, p)
	}
	wg.Wait()
	return results
}
