package custom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestProvider_ChatCompletionUsesConfiguredBaseURLHeadersAndKey(t *testing.T) {
	var gotAuth, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Workspace")
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewOpenAICompatible(Config{
		Slug:           "localai",
		DisplayName:    "LocalAI",
		BaseURL:        server.URL + "/v1",
		APIKey:         "sk-local",
		APIKeyRequired: true,
		Headers:        map[string]string{"X-Workspace": "packetcode"},
	})

	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "coder",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	for range ch {
	}
	assert.Equal(t, "Bearer sk-local", gotAuth)
	assert.Equal(t, "packetcode", gotHeader)
}

func TestProvider_ListModelsFallsBackToStaticModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no models endpoint"}}`))
	}))
	defer server.Close()

	supportsTools := false
	p := NewOpenAICompatible(Config{
		Slug:           "custom",
		BaseURL:        server.URL + "/v1",
		APIKeyRequired: false,
		DefaultModel:   "offline-model",
		Models: []ModelConfig{{
			ID:            "offline-model",
			ContextWindow: 65536,
			SupportsTools: &supportsTools,
			InputPer1M:    0.1,
			OutputPer1M:   0.2,
		}},
	})

	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "offline-model", models[0].ID)
	assert.Equal(t, 65536, p.ContextWindow("offline-model"))
	assert.False(t, p.SupportsTools("offline-model"))
	in, out := p.Pricing("offline-model")
	assert.Equal(t, 0.1, in)
	assert.Equal(t, 0.2, out)
}

func TestProvider_KeylessValidationCanUseStaticModels(t *testing.T) {
	p := NewOpenAICompatible(Config{
		Slug:           "local",
		BaseURL:        "http://127.0.0.1:9/v1",
		APIKeyRequired: false,
		DefaultModel:   "local-model",
	})

	require.NoError(t, p.ValidateKey(context.Background(), ""))
}

func TestProvider_InvalidBaseURLSurfacesEvenWithStaticModels(t *testing.T) {
	p := NewOpenAICompatible(Config{
		Slug:         "bad",
		BaseURL:      "localhost:8080/v1",
		DefaultModel: "local-model",
	})

	_, err := p.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base_url")
	_, err = p.ChatCompletion(context.Background(), provider.ChatRequest{Model: "local-model"})
	require.Error(t, err)
}

func TestProvider_ListModelsEnrichesRemoteModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "remote-model"}},
		})
	}))
	defer server.Close()

	p := NewOpenAICompatible(Config{
		Slug:         "remote",
		BaseURL:      server.URL + "/v1",
		DefaultModel: "remote-model",
		Models:       []ModelConfig{{ID: "remote-model", ContextWindow: 12345, InputPer1M: 1.5}},
	})

	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "remote-model", models[0].ID)
	assert.Equal(t, 12345, models[0].ContextWindow)
	assert.True(t, models[0].SupportsTools)
	assert.Equal(t, 1.5, models[0].InputPer1M)
}
