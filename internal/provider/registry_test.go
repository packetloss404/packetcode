package provider

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider is a minimal Provider stub for registry tests.
type fakeProvider struct {
	slug         string
	name         string
	validateErr  error
	models       []Model
	listModelErr error
}

func (f *fakeProvider) Name() string                                         { return f.name }
func (f *fakeProvider) Slug() string                                         { return f.slug }
func (f *fakeProvider) BrandColor() lipgloss.Color                           { return lipgloss.Color("#000000") }
func (f *fakeProvider) ValidateKey(ctx context.Context, apiKey string) error { return f.validateErr }
func (f *fakeProvider) ListModels(ctx context.Context) ([]Model, error) {
	return f.models, f.listModelErr
}
func (f *fakeProvider) ChatCompletion(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	return nil, nil
}
func (f *fakeProvider) Pricing(modelID string) (float64, float64) { return 0, 0 }
func (f *fakeProvider) ContextWindow(modelID string) int          { return 0 }
func (f *fakeProvider) SupportsTools(modelID string) bool         { return false }

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{slug: "openai", name: "OpenAI"})

	p, ok := r.Get("openai")
	require.True(t, ok)
	assert.Equal(t, "OpenAI", p.Name())

	_, ok = r.Get("missing")
	assert.False(t, ok)
}

func TestRegistry_ListUsesDisplayOrder(t *testing.T) {
	r := NewRegistry()
	// Register in reverse display order plus an unknown slug.
	r.Register(&fakeProvider{slug: "zzz-extra"})
	r.Register(&fakeProvider{slug: "ollama"})
	r.Register(&fakeProvider{slug: "openrouter"})
	r.Register(&fakeProvider{slug: "minimax"})
	r.Register(&fakeProvider{slug: "gemini"})
	r.Register(&fakeProvider{slug: "anthropic"})
	r.Register(&fakeProvider{slug: "openai"})

	got := r.Slugs()
	assert.Equal(t, []string{"openai", "anthropic", "gemini", "minimax", "openrouter", "ollama", "zzz-extra"}, got)
}

func TestRegistry_SetActiveAtomic(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{slug: "openai", name: "OpenAI"})

	require.NoError(t, r.SetActive("openai", "gpt-4.1"))
	p, model := r.Active()
	require.NotNil(t, p)
	assert.Equal(t, "OpenAI", p.Name())
	assert.Equal(t, "gpt-4.1", model)

	err := r.SetActive("does-not-exist", "any")
	require.Error(t, err)
}

func TestRegistry_RegisterReplacesActiveInstance(t *testing.T) {
	r := NewRegistry()
	first := &fakeProvider{slug: "openai", name: "old"}
	second := &fakeProvider{slug: "openai", name: "new"}
	r.Register(first)
	require.NoError(t, r.SetActive("openai", "gpt-4.1"))

	r.Register(second)

	p, model := r.Active()
	require.NotNil(t, p)
	assert.Equal(t, "new", p.Name())
	assert.Equal(t, "gpt-4.1", model)
}

func TestRegistry_ActiveBeforeSet(t *testing.T) {
	r := NewRegistry()
	p, model := r.Active()
	assert.Nil(t, p)
	assert.Equal(t, "", model)
}

func TestRegistry_CacheRoundtrip(t *testing.T) {
	r := NewRegistry()
	models := []Model{{ID: "m1"}, {ID: "m2"}}
	r.SetCachedModels("openai", models)

	got, ok := r.CachedModels("openai")
	require.True(t, ok)
	assert.Equal(t, []Model{{ID: "m1"}, {ID: "m2"}}, got)

	// Defensive copy: mutating the returned slice must not affect the
	// cache.
	got[0].ID = "mutated"
	again, _ := r.CachedModels("openai")
	assert.Equal(t, "m1", again[0].ID)
}

func TestRegistry_CacheMiss(t *testing.T) {
	r := NewRegistry()
	_, ok := r.CachedModels("openai")
	assert.False(t, ok)
}

func TestRegistry_CacheInvalidate(t *testing.T) {
	r := NewRegistry()
	r.SetCachedModels("openai", []Model{{ID: "m1"}})
	_, ok := r.CachedModels("openai")
	require.True(t, ok)

	r.InvalidateCachedModels("openai")
	_, ok = r.CachedModels("openai")
	assert.False(t, ok)

	// Invalidating an absent slug is a no-op.
	r.InvalidateCachedModels("gemini")
}

func TestRegistry_CacheConcurrent(t *testing.T) {
	r := NewRegistry()
	// Seed so readers always see *something*; writers overwrite.
	r.SetCachedModels("openai", []Model{{ID: "seed"}})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.SetCachedModels("openai", []Model{{ID: "w"}})
		}(i)
		go func() {
			defer wg.Done()
			if got, ok := r.CachedModels("openai"); ok {
				_ = got
			}
		}()
	}
	wg.Wait()
}

func TestRegistry_InitializeAllAggregatesFailures(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{slug: "openai", models: []Model{{ID: "gpt-4.1"}}})
	r.Register(&fakeProvider{slug: "gemini", validateErr: errors.New("bad key")})
	r.Register(&fakeProvider{slug: "ollama", listModelErr: errors.New("not running")})

	results := r.InitializeAll(context.Background(), func(string) string { return "key" })
	require.Len(t, results, 3)

	bySlug := map[string]InitResult{}
	for _, res := range results {
		bySlug[res.Slug] = res
	}
	require.NoError(t, bySlug["openai"].Err)
	assert.Equal(t, "gpt-4.1", bySlug["openai"].Model[0].ID)
	require.Error(t, bySlug["gemini"].Err)
	require.Error(t, bySlug["ollama"].Err)
}
