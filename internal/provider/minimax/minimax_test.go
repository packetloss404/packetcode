package minimax

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_Identity(t *testing.T) {
	p := New("k")
	assert.Equal(t, "minimax", p.Slug())
	assert.Equal(t, "MiniMax", p.Name())
}

func TestProvider_PricingFallback(t *testing.T) {
	p := New("")
	in, out := p.Pricing(DefaultModel)
	assert.Equal(t, 0.60, in)
	assert.Equal(t, 2.40, out)
	assert.Equal(t, 204_800, p.ContextWindow(DefaultModel))
	assert.True(t, p.SupportsTools(DefaultModel))

	in, out = p.Pricing("totally-unknown")
	assert.Equal(t, 1.00, in)
	assert.Equal(t, 1.00, out)
}

func TestProvider_ListModels_FallbackOnEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "k")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, models, "should fall back to curated list when upstream is empty")
	assert.Equal(t, DefaultModel, models[0].ID)
}

func TestProvider_ListModels_FallbackOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "k")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err, "fallback should suppress upstream errors")
	assert.NotEmpty(t, models)
}

func TestProvider_ListModels_PassesThroughUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"MiniMax-M2.7"},{"id":"MiniMax-M2.7-highspeed"},{"id":"abab6.5s-chat"}]}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "k")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, DefaultModel, models[0].ID)
	assert.Equal(t, 204_800, models[0].ContextWindow)
	assert.True(t, models[0].SupportsTools)
	assert.False(t, models[2].SupportsTools)
	for _, m := range models {
		in, out := p.Pricing(m.ID)
		assert.Equal(t, in, m.InputPer1M)
		assert.Equal(t, out, m.OutputPer1M)
		assert.Equal(t, p.ContextWindow(m.ID), m.ContextWindow)
		assert.Equal(t, p.SupportsTools(m.ID), m.SupportsTools)
	}
}
