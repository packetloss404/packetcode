package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/provider"
)

func TestProvider_Identity(t *testing.T) {
	p := New("")
	assert.Equal(t, "ollama", p.Slug())
	assert.Equal(t, "Ollama", p.Name())
}

func TestProvider_NewDefaultsHost(t *testing.T) {
	p := New("")
	assert.Equal(t, "http://localhost:11434", p.baseURL)
}

func TestProvider_NewNormalizesHost(t *testing.T) {
	assert.Equal(t, "http://ollama.internal:11434", New("ollama.internal").baseURL)
	assert.Equal(t, "http://ollama.internal:11434", New("http://ollama.internal").baseURL)
	assert.Equal(t, "http://ollama.internal:11435", New("http://ollama.internal:11435/").baseURL)
}

func TestProvider_PricingIsZero(t *testing.T) {
	p := New("")
	in, out := p.Pricing("anything")
	assert.Equal(t, 0.0, in)
	assert.Equal(t, 0.0, out)
}

func TestDetectToolSupport(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"qwen2.5-coder:14b", true},
		{"qwen2.5-coder", true},
		{"llama3.3:70b-instruct-q4_K_M", true},
		{"deepseek-coder", false},
		{"codellama:13b", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, detectToolSupport(tt.model))
		})
	}
}

func TestProvider_ValidateKey_OllamaUnreachable(t *testing.T) {
	// Use a port nothing is listening on.
	p := New("http://127.0.0.1:1")
	err := p.ValidateKey(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not reachable")
}

func TestProvider_ValidateKey_OllamaReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	p := New(server.URL)
	require.NoError(t, p.ValidateKey(context.Background(), ""))
}

func TestProvider_ListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"models":[
				{"name":"qwen2.5-coder:14b","model":"qwen2.5-coder:14b","size":9000000000},
				{"name":"deepseek-coder:6.7b","model":"deepseek-coder:6.7b","size":4000000000}
			]
		}`))
	}))
	defer server.Close()

	p := New(server.URL)
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2)

	byID := map[string]provider.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	assert.True(t, byID["qwen2.5-coder:14b"].SupportsTools)
	assert.False(t, byID["deepseek-coder:6.7b"].SupportsTools)
}

func TestProvider_ChatCompletion_NDJSONStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"model":"qwen2.5-coder:14b","message":{"role":"assistant","content":"Hello"},"done":false}`,
		`{"model":"qwen2.5-coder:14b","message":{"role":"assistant","content":" world"},"done":false}`,
		`{"model":"qwen2.5-coder:14b","message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":11,"eval_count":2}`,
		``,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := New(server.URL)
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "qwen2.5-coder:14b",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "say hello"}},
	})
	require.NoError(t, err)

	var got strings.Builder
	var done bool
	var usage *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			got.WriteString(ev.TextDelta)
		case provider.EventDone:
			done = true
			usage = ev.Usage
		}
	}
	assert.Equal(t, "Hello world", got.String())
	assert.True(t, done)
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.InputTokens)
	assert.Equal(t, 2, usage.OutputTokens)
}

// TestOllama_ChatCompletion_CancellationStopsStream verifies the
// per-iteration ctx.Err() guard in parseOllamaStream: cancelling the
// ctx passed to ChatCompletion closes the NDJSON channel within 1s and
// surfaces an EventError wrapping context.Canceled.
func TestOllama_ChatCompletion_CancellationStopsStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not implement Flusher")
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < 50; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			if _, err := fmt.Fprintf(w,
				"{\"model\":\"qwen2.5-coder:14b\",\"message\":{\"role\":\"assistant\",\"content\":\"chunk %d \"},\"done\":false}\n",
				i); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(server.URL)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := p.ChatCompletion(ctx, provider.ChatRequest{
		Model:    "qwen2.5-coder:14b",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "stream please"}},
	})
	require.NoError(t, err)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelDrain()

	var events []provider.StreamEvent
	var channelClosed bool
loop:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				channelClosed = true
				break loop
			}
			events = append(events, ev)
		case <-drainCtx.Done():
			break loop
		}
	}

	assert.True(t, channelClosed, "channel must close within 1s of cancel")
	var sawCancelErr bool
	for _, ev := range events {
		if ev.Type == provider.EventError && ev.Error != nil && errors.Is(ev.Error, context.Canceled) {
			sawCancelErr = true
			break
		}
	}
	assert.True(t, sawCancelErr, "expected EventError wrapping context.Canceled; got events: %+v", events)
}

func TestProvider_ChatCompletion_ToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"main.go"}}}]},"done":true,"prompt_eval_count":15,"eval_count":8}`,
		``,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := New(server.URL)
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "qwen2.5-coder:14b",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read main.go"}},
	})
	require.NoError(t, err)

	var starts, ends int
	var name, args string
	for ev := range ch {
		switch ev.Type {
		case provider.EventToolCallStart:
			starts++
			name = ev.ToolCall.Name
		case provider.EventToolCallDelta:
			args += ev.ToolCall.ArgumentsDelta
		case provider.EventToolCallEnd:
			ends++
		}
	}
	assert.Equal(t, 1, starts)
	assert.Equal(t, 1, ends)
	assert.Equal(t, "read_file", name)
	assert.JSONEq(t, `{"path":"main.go"}`, args)
}

func TestProvider_ChatCompletion_SuppressesTextOnToolCallChunk(t *testing.T) {
	stream := strings.Join([]string{
		`{"message":{"role":"assistant","content":"<|python_tag|>{\"path\":\"main.go\"}","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"main.go"}}}]},"done":true,"prompt_eval_count":15,"eval_count":8}`,
		``,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := New(server.URL)
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "qwen2.5-coder:14b",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read main.go"}},
	})
	require.NoError(t, err)

	var text, args strings.Builder
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
		case provider.EventToolCallDelta:
			args.WriteString(ev.ToolCall.ArgumentsDelta)
		case provider.EventError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	assert.Empty(t, text.String())
	assert.JSONEq(t, `{"path":"main.go"}`, args.String())
}
