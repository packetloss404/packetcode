package anthropic

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
	p := New("sk-ant-test")
	assert.Equal(t, "anthropic", p.Slug())
	assert.Equal(t, "Anthropic", p.Name())
	assert.NotEmpty(t, string(p.BrandColor()))
}

func TestProvider_PricingContextAndTools(t *testing.T) {
	p := New("")
	in, out := p.Pricing(DefaultModel)
	assert.Equal(t, 5.00, in)
	assert.Equal(t, 25.00, out)
	assert.Equal(t, 1_000_000, p.ContextWindow(DefaultModel))
	assert.True(t, p.SupportsTools(DefaultModel))

	in, out = p.Pricing("claude-new")
	assert.Equal(t, 5.00, in)
	assert.Equal(t, 25.00, out)
	assert.Equal(t, 200_000, p.ContextWindow("claude-new"))
	assert.True(t, p.SupportsTools("claude-new"))
}

func TestProvider_ValidateKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "sk-ant-good", r.Header.Get("x-api-key"))
		assert.Equal(t, anthropicVersion, r.Header.Get("anthropic-version"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "")
	require.NoError(t, p.ValidateKey(context.Background(), "sk-ant-good"))
	require.Error(t, p.ValidateKey(context.Background(), ""))
}

func TestProvider_ListModels_MetadataAndDefaultFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6","max_input_tokens":1000000,"max_tokens":64000,"capabilities":{"tools":true}},
				{"id":"claude-opus-4-7","display_name":"Claude Opus 4.7","max_input_tokens":1000000,"max_tokens":128000,"capabilities":{"tools":true}}
			]
		}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-ant-test")
	models, err := p.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, DefaultModel, models[0].ID)
	assert.Equal(t, "Claude Opus 4.7", models[0].DisplayName)
	assert.Equal(t, 1_000_000, models[0].ContextWindow)
	assert.True(t, models[0].SupportsTools)
	assert.Equal(t, 5.00, models[0].InputPer1M)
	assert.Equal(t, 25.00, models[0].OutputPer1M)
}

func TestToWireRequest_RoleAndToolMapping(t *testing.T) {
	wr, err := toWireRequest(provider.ChatRequest{
		Model: DefaultModel,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "you are helpful"},
			{Role: provider.RoleUser, Content: "read main.go"},
			{Role: provider.RoleAssistant, Content: "I will look.", ToolCalls: []provider.ToolCall{
				{ID: "toolu_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
			{Role: provider.RoleTool, ToolCallID: "toolu_1", Name: "read_file", Content: "package main\n"},
		},
		Tools: []provider.ToolDefinition{
			{Name: "read_file", Description: "read a file", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, DefaultModel, wr.Model)
	assert.Equal(t, "you are helpful", wr.System)
	assert.True(t, wr.Stream)
	assert.Equal(t, defaultMaxTokens, wr.MaxTokens)
	require.Len(t, wr.Messages, 3)
	assert.Equal(t, "user", wr.Messages[0].Role)
	assert.JSONEq(t, `[{"type":"text","text":"read main.go"}]`, string(wr.Messages[0].Content))
	assert.Equal(t, "assistant", wr.Messages[1].Role)
	assert.JSONEq(t, `[{"type":"text","text":"I will look."},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"main.go"}}]`, string(wr.Messages[1].Content))
	assert.Equal(t, "user", wr.Messages[2].Role)
	assert.JSONEq(t, `[{"type":"tool_result","tool_use_id":"toolu_1","content":"package main\n"}]`, string(wr.Messages[2].Content))
	require.Len(t, wr.Tools, 1)
	assert.Equal(t, "read_file", wr.Tools[0].Name)
	assert.JSONEq(t, `{"type":"object"}`, string(wr.Tools[0].InputSchema))
}

func TestProvider_ChatCompletion_StreamsTextAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":25,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":4}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var captured wireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))
		assert.Equal(t, "/messages", r.URL.Path)
		assert.Equal(t, "sk-ant-test", r.Header.Get("x-api-key"))
		assert.Equal(t, anthropicVersion, r.Header.Get("anthropic-version"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-ant-test")
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    DefaultModel,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)

	var text strings.Builder
	var done bool
	var usage *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
		case provider.EventDone:
			done = true
			usage = ev.Usage
		case provider.EventError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	assert.Equal(t, "Hello world", text.String())
	assert.True(t, done)
	require.NotNil(t, usage)
	assert.Equal(t, 25, usage.InputTokens)
	assert.Equal(t, 4, usage.OutputTokens)
	assert.Equal(t, DefaultModel, captured.Model)
}

func TestProvider_ChatCompletion_StreamsToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-ant-test")
	ch, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    DefaultModel,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read main.go"}},
	})
	require.NoError(t, err)

	var starts, ends int
	var id, name string
	var args strings.Builder
	for ev := range ch {
		switch ev.Type {
		case provider.EventToolCallStart:
			starts++
			id = ev.ToolCall.ID
			name = ev.ToolCall.Name
		case provider.EventToolCallDelta:
			args.WriteString(ev.ToolCall.ArgumentsDelta)
		case provider.EventToolCallEnd:
			ends++
		case provider.EventError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	assert.Equal(t, 1, starts)
	assert.Equal(t, 1, ends)
	assert.Equal(t, "toolu_1", id)
	assert.Equal(t, "read_file", name)
	assert.JSONEq(t, `{"path":"main.go"}`, args.String())
}

func TestProvider_ChatCompletion_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer server.Close()

	p := NewWithBaseURL(server.URL, "sk-ant-test")
	_, err := p.ChatCompletion(context.Background(), provider.ChatRequest{
		Model:    "bogus",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "bad model")
}
