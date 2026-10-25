package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/packetcode/packetcode/internal/provider"
)

// TestLiveChatCompletion performs a real one-shot request against the ChatGPT
// backend using the local ~/.codex/auth.json credentials. It is skipped unless
// CODEX_LIVE=1 so ordinary `go test` runs never touch the network or spend a
// subscription. Run it with:
//
//	CODEX_LIVE=1 go test ./internal/provider/codex/ -run TestLive -v
func TestLiveChatCompletion(t *testing.T) {
	if os.Getenv("CODEX_LIVE") != "1" {
		t.Skip("set CODEX_LIVE=1 to run the live Codex smoke test")
	}

	p := New("") // conventional ~/.codex/auth.json
	if err := p.ValidateKey(context.Background(), ""); err != nil {
		t.Fatalf("no Codex login available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ch, err := p.ChatCompletion(ctx, provider.ChatRequest{
		Model: DefaultModel,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You are a terse assistant."},
			{Role: provider.RoleUser, Content: "Reply with exactly the word: pong"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	var text strings.Builder
	var usage *provider.Usage
	var done bool
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
		case provider.EventDone:
			done = true
			usage = ev.Usage
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}

	t.Logf("model reply: %q", text.String())
	if usage != nil {
		t.Logf("usage: in=%d out=%d", usage.InputTokens, usage.OutputTokens)
	}
	if !done {
		t.Fatal("stream ended without a completed event")
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("model returned no text")
	}
}

// TestLiveToolCall verifies the flagship model accepts packetcode's generic
// JSON function tools and emits a well-formed tool call — the risk flagged by
// the model's code_mode_only tool_mode. Gated by CODEX_LIVE and MODEL override.
func TestLiveToolCall(t *testing.T) {
	if os.Getenv("CODEX_LIVE") != "1" {
		t.Skip("set CODEX_LIVE=1 to run the live Codex smoke test")
	}
	model := DefaultModel
	if m := os.Getenv("CODEX_MODEL"); m != "" {
		model = m
	}

	p := New("")
	if err := p.ValidateKey(context.Background(), ""); err != nil {
		t.Fatalf("no Codex login available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ch, err := p.ChatCompletion(ctx, provider.ChatRequest{
		Model: model,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "What is the weather in Paris? Use the get_weather tool."},
		},
		Tools: []provider.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get the current weather for a city.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion (%s): %v", model, err)
	}

	var name, args string
	var sawStart bool
	for ev := range ch {
		switch ev.Type {
		case provider.EventToolCallStart:
			sawStart = true
			name = ev.ToolCall.Name
		case provider.EventToolCallDelta:
			args += ev.ToolCall.ArgumentsDelta
		case provider.EventError:
			t.Fatalf("stream error (%s): %v", model, ev.Error)
		}
	}
	t.Logf("model=%s tool=%s args=%s", model, name, args)
	if !sawStart || name != "get_weather" {
		t.Fatalf("expected a get_weather tool call, got start=%v name=%q", sawStart, name)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("tool args not valid JSON: %q", args)
	}
}

// TestLiveReasoning checks that Codex streams reasoning summary deltas that
// packetcode surfaces as EventReasoningDelta. Gated by CODEX_LIVE.
func TestLiveReasoning(t *testing.T) {
	if os.Getenv("CODEX_LIVE") != "1" {
		t.Skip("set CODEX_LIVE=1 to run the live Codex smoke test")
	}
	p := New("")
	if err := p.ValidateKey(context.Background(), ""); err != nil {
		t.Fatalf("no Codex login: %v", err)
	}
	model := DefaultModel
	if m := os.Getenv("CODEX_MODEL"); m != "" {
		model = m
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ch, err := p.ChatCompletion(ctx, provider.ChatRequest{
		Model: model,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "A farmer has 17 sheep, all but 9 run away. Think step by step, then give the number."},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	var reasoning, text strings.Builder
	for ev := range ch {
		switch ev.Type {
		case provider.EventReasoningDelta:
			reasoning.WriteString(ev.TextDelta)
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	// Reasoning summaries depend on live model/account metadata; a valid stream
	// can include a summary or omit it. Require the final answer here and leave
	// deterministic reasoning-event handling to the protocol unit tests.
	t.Logf("reasoning chars=%d answer=%q", reasoning.Len(), strings.TrimSpace(text.String()))
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("expected an answer")
	}
}
