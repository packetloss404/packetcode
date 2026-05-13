package openai

// pricingEntry is USD per 1M tokens for input and output respectively.
type pricingEntry struct {
	Input  float64
	Output float64
	// ContextWindow is the model's max input tokens; 0 means unknown.
	ContextWindow int
	// SupportsTools is best-effort — every modern OpenAI chat model does.
	SupportsTools bool
}

// pricingTable is keyed by exact OpenAI model ID. Lookups for unknown IDs
// fall through to a conservative default in Pricing/ContextWindow, so a
// missing entry never blocks a model from being used — it just shows
// pessimistic cost estimates until the table catches up.
//
// Prices last verified against OpenAI's public price list in May 2026.
var pricingTable = map[string]pricingEntry{
	"gpt-5.5":      {Input: 5.00, Output: 30.00, ContextWindow: 1_050_000, SupportsTools: true},
	"gpt-5.2":      {Input: 1.75, Output: 14.00, ContextWindow: 400_000, SupportsTools: true},
	"gpt-5.1":      {Input: 1.25, Output: 10.00, ContextWindow: 400_000, SupportsTools: true},
	"gpt-5":        {Input: 1.25, Output: 10.00, ContextWindow: 400_000, SupportsTools: true},
	"gpt-4.1":      {Input: 2.00, Output: 8.00, ContextWindow: 1_000_000, SupportsTools: true},
	"gpt-4.1-mini": {Input: 0.40, Output: 1.60, ContextWindow: 1_000_000, SupportsTools: true},
	"gpt-4.1-nano": {Input: 0.10, Output: 0.40, ContextWindow: 1_000_000, SupportsTools: true},
	"o3":           {Input: 10.00, Output: 40.00, ContextWindow: 200_000, SupportsTools: true},
	"o4-mini":      {Input: 1.10, Output: 4.40, ContextWindow: 200_000, SupportsTools: true},
}

// nonChatIndicators are substrings that identify a model as NOT a chat
// completion model (embeddings, audio, image generation, moderation,
// legacy completion-only models, and the Responses-API-only "-pro"
// family). Anything whose ID does not contain one of these passes
// through the filter, so new chat families (GPT-5, GPT-6, o5, etc.)
// surface automatically without a code change here.
//
// Note on "-pro": OpenAI ships o1-pro, o3-pro, gpt-5.5-pro and similar
// variants that only work via /v1/responses, not /v1/chat/completions
// (the endpoint packetcode speaks). The /v1/models catalog doesn't
// distinguish, so we exclude them by suffix. The plain (non-pro) model
// and its dated snapshots still work on chat completions.
var nonChatIndicators = []string{
	"embedding",
	"tts",
	"whisper",
	"dall-e",
	"dalle",
	"moderation",
	"transcribe",
	"realtime",
	"davinci-002",
	"babbage-002",
	"image",
	"-pro", // Responses-API-only; see note above.
}
