package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestProvider_Identity(t *testing.T) {
	p := New("sk-test")
	assert.Equal(t, "openai", p.Slug())
	assert.Equal(t, "OpenAI", p.Name())
	assert.NotEmpty(t, string(p.BrandColor()))
}

func TestProvider_PricingKnownAndUnknown(t *testing.T) {
	p := New("")
	in, out := p.Pricing(DefaultModel)
	assert.Equal(t, 5.00, in)
	assert.Equal(t, 30.00, out)

	in, out = p.Pricing("totally-made-up")
	assert.Equal(t, 3.00, in, "unknown models should hit conservative fallback")
	assert.Equal(t, 15.00, out)
}

func TestProvider_ContextWindowAndSupportsTools(t *testing.T) {
	p := New("")
	assert.Equal(t, 1_050_000, p.ContextWindow(DefaultModel))
	assert.True(t, p.SupportsTools(DefaultModel))

	// Unknown but matching a supported prefix → assumes tools.
	assert.True(t, p.SupportsTools("gpt-4o-2024-08-06"))
	assert.False(t, p.SupportsTools("text-embedding-3-small"))
}

func TestProvider_ValidateKey_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "Bearer sk-good", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "")
	require.NoError(t, p.ValidateKey(context.Background(), "sk-good"))
}

func TestProvider_ValidateKey_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "")
	err := p.ValidateKey(context.Background(), "sk-bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestProvider_ValidateKey_EmptyRejected(t *testing.T) {
	p := New("")
	err := p.ValidateKey(context.Background(), "")
	require.Error(t, err)
}

func TestProvider_ListModels_FiltersUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-4.1"},
				{"id": "gpt-5.5"},
				{"id": "gpt-4.1-mini"},
				{"id": "text-embedding-3-small"},
				{"id": "tts-1"},
				{"id": "o3"}
			]
		}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-test")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)

	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	assert.Subset(t, ids, []string{"gpt-5.5", "gpt-4.1", "gpt-4.1-mini", "o3"})
	assert.NotContains(t, ids, "gpt-5.2", "pricing-only entries should not seed the catalog")
	assert.NotContains(t, ids, "text-embedding-3-small")
	assert.NotContains(t, ids, "tts-1")
	assert.Equal(t, DefaultModel, models[0].ID)
	assert.Equal(t, 1_050_000, models[0].ContextWindow)
}

// TestProvider_ListModels_ExcludesProFamily confirms the "-pro" filter
// that hides Responses-API-only models (o1-pro, o3-pro, gpt-5.5-pro).
// Plain (non-pro) variants and their dated snapshots still pass. The
// pricing table enriches returned models but does not seed extra entries.
func TestProvider_ListModels_ExcludesProFamily(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-5.5"},
				{"id": "gpt-5.5-2026-04-23"},
				{"id": "gpt-5.5-pro"},
				{"id": "gpt-5.5-pro-2026-04-23"},
				{"id": "o3-pro"},
				{"id": "o4-mini"}
			]
		}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-test")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)

	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	assert.Contains(t, ids, "gpt-5.5")
	assert.Contains(t, ids, "gpt-5.5-2026-04-23")
	assert.Contains(t, ids, "o4-mini")
	assert.NotContains(t, ids, "gpt-5.5-pro")
	assert.NotContains(t, ids, "gpt-5.5-pro-2026-04-23")
	assert.NotContains(t, ids, "o3-pro")
	assert.Equal(t, DefaultModel, models[0].ID)
}

func TestProvider_ChatCompletion_StreamsTextAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var captured chatRequestBodyForTest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		assert.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-test")
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4.1",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	require.NoError(t, err)

	var text strings.Builder
	var sawDone bool
	var lastUsage *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
		case provider.EventDone:
			sawDone = true
			lastUsage = ev.Usage
		case provider.EventError:
			t.Fatalf("unexpected error event: %v", ev.Error)
		}
	}
	assert.Equal(t, "Hello world", text.String())
	assert.True(t, sawDone)
	require.NotNil(t, lastUsage)
	assert.Equal(t, 12, lastUsage.InputTokens)
	assert.Equal(t, 3, lastUsage.OutputTokens)

	assert.Equal(t, "gpt-4.1", captured.Model)
	require.Len(t, captured.Messages, 1)
	assert.Equal(t, "user", captured.Messages[0].Role)
}

func TestProvider_ChatCompletion_StreamsToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-test")
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4.1",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read main.go"}},
	})
	require.NoError(t, err)

	var startCount, deltaCount, endCount int
	var args strings.Builder
	var name, id string
	for ev := range ch {
		switch ev.Type {
		case provider.EventToolCallStart:
			startCount++
			name = ev.ToolCall.Name
			id = ev.ToolCall.ID
		case provider.EventToolCallDelta:
			deltaCount++
			args.WriteString(ev.ToolCall.ArgumentsDelta)
		case provider.EventToolCallEnd:
			endCount++
		}
	}
	assert.Equal(t, 1, startCount)
	assert.GreaterOrEqual(t, deltaCount, 2)
	assert.Equal(t, 1, endCount)
	assert.Equal(t, "read_file", name)
	assert.Equal(t, "call_1", id)
	assert.Equal(t, `{"path":"main.go"}`, args.String())
}

func TestProvider_ChatCompletion_NonStreamErrorReturnedSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-test")
	_, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "gpt-4.1",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
}

// chatRequestBodyForTest mirrors openaicompat's wire format for assertion.
// Kept private to this test file so we don't widen the openaicompat API.
type chatRequestBodyForTest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools  []any `json:"tools,omitempty"`
	Stream bool  `json:"stream"`
}
