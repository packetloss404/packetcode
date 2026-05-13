// Package provider defines the unified abstraction layer that the built-in
// LLM backends (OpenAI, Anthropic, Gemini, MiniMax, OpenRouter, Ollama)
// implement.
//
// The interface is intentionally narrow: identity, key validation, model
// listing, streaming chat completion, and pricing/context metadata. Anything
// provider-specific is hidden inside each implementation.
package provider

import (
	"encoding/json"
)

// Role is the message author identity. The provider implementations are
// responsible for translating these to/from their wire format (e.g. Gemini
// uses "model" instead of "assistant").
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Model describes a model exposed by a provider, with metadata used by the
// model selector UI and the cost tracker.
type Model struct {
	ID            string
	DisplayName   string
	ContextWindow int
	SupportsTools bool
	InputPer1M    float64 // USD per 1M input tokens; 0 for free/local
	OutputPer1M   float64
}

// Message is the unified chat message format. Tool calls and tool responses
// share this struct: assistant messages may carry ToolCalls; tool messages
// carry ToolCallID + Name + textual Content.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolDefinition is sent to the LLM to declare an available tool. Parameters
// is a raw JSON Schema document — the tool registry produces these.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is a complete (assembled) tool invocation returned by the LLM.
// Arguments is a JSON string per the OpenAI/Anthropic convention — providers
// that use a different shape (Gemini's structured args) marshal to this.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is what the agent loop sends to a provider. Stream is always
// true for the MVP; the field exists for future non-streaming use cases
// (validation pings, summarization).
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolDefinition
	Stream   bool
}

// EventType discriminates StreamEvent payloads. Providers emit a sequence
// like: TextDelta* (ToolCallStart ToolCallDelta* ToolCallEnd)* Done.
type EventType int

const (
	EventTextDelta EventType = iota
	EventToolCallStart
	EventToolCallDelta
	EventToolCallEnd
	EventDone
	EventError
)

// String renders an EventType for logs and tests.
func (e EventType) String() string {
	switch e {
	case EventTextDelta:
		return "TextDelta"
	case EventToolCallStart:
		return "ToolCallStart"
	case EventToolCallDelta:
		return "ToolCallDelta"
	case EventToolCallEnd:
		return "ToolCallEnd"
	case EventDone:
		return "Done"
	case EventError:
		return "Error"
	default:
		return "Unknown"
	}
}

// ToolCallDelta represents an in-flight tool call being streamed token by
// token. Index lets us correlate deltas when the model emits parallel calls.
type ToolCallDelta struct {
	Index          int
	ID             string
	Name           string
	ArgumentsDelta string
}

// Usage is the per-completion token accounting. Cache fields are populated
// only by providers that support prompt caching (Anthropic via OpenRouter).
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// StreamEvent is the provider-agnostic envelope yielded by ChatCompletion.
// Exactly one of TextDelta / ToolCall / Usage / Error is meaningful, keyed
// off Type.
type StreamEvent struct {
	Type      EventType
	TextDelta string
	ToolCall  *ToolCallDelta
	Usage     *Usage
	Error     error
}
