package provider

import (
	"context"

	"github.com/charmbracelet/lipgloss"
)

// Provider is the contract every LLM backend implements. Implementations
// must be safe for concurrent use — the agent loop can issue parallel
// ListModels and ChatCompletion calls during startup.
type Provider interface {
	// Identity. Slug is the lowercase token used in config files and slash
	// commands; Name is the human display label; BrandColor is the dot in
	// the provider selector and top bar.
	Name() string
	Slug() string
	BrandColor() lipgloss.Color

	// ValidateKey performs a cheap authenticated request (typically a
	// model list) to confirm the key is accepted. Returns nil on success.
	// Keyless providers can use this to validate reachability instead.
	ValidateKey(ctx context.Context, apiKey string) error

	// ListModels enumerates models available to the current key. The
	// result populates the model selector and the cost tracker pricing
	// fallback.
	ListModels(ctx context.Context) ([]Model, error)

	// ChatCompletion opens a streaming completion. The returned channel is
	// closed by the provider when the stream terminates (Done or Error
	// event). Cancelling ctx must close the channel promptly.
	ChatCompletion(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)

	// Pricing returns USD per 1M input/output tokens for the named model.
	// Returns (0, 0) for free or local models, or for unknown IDs.
	Pricing(modelID string) (inputPer1M float64, outputPer1M float64)

	// ContextWindow returns the model's maximum input context in tokens.
	// Returns 0 if unknown.
	ContextWindow(modelID string) int

	// SupportsTools reports whether the model can handle native function
	// calling. False for some local Ollama models; callers omit native
	// tool definitions and may warn the model that tools are unavailable.
	SupportsTools(modelID string) bool
}
