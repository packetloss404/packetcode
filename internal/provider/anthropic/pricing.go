package anthropic

type pricingEntry struct {
	Input         float64
	Output        float64
	ContextWindow int
	MaxOutput     int
	SupportsTools bool
}

// Prices and limits last verified against Anthropic's public Claude API
// models/pricing docs in May 2026.
var pricingTable = map[string]pricingEntry{
	"claude-opus-4-7":           {Input: 5.00, Output: 25.00, ContextWindow: 1_000_000, MaxOutput: 128_000, SupportsTools: true},
	"claude-opus-4-6":           {Input: 5.00, Output: 25.00, ContextWindow: 1_000_000, MaxOutput: 128_000, SupportsTools: true},
	"claude-opus-4-5":           {Input: 5.00, Output: 25.00, ContextWindow: 1_000_000, MaxOutput: 128_000, SupportsTools: true},
	"claude-sonnet-4-6":         {Input: 3.00, Output: 15.00, ContextWindow: 1_000_000, MaxOutput: 64_000, SupportsTools: true},
	"claude-sonnet-4-5":         {Input: 3.00, Output: 15.00, ContextWindow: 1_000_000, MaxOutput: 64_000, SupportsTools: true},
	"claude-haiku-4-5":          {Input: 1.00, Output: 5.00, ContextWindow: 200_000, MaxOutput: 64_000, SupportsTools: true},
	"claude-haiku-4-5-20251001": {Input: 1.00, Output: 5.00, ContextWindow: 200_000, MaxOutput: 64_000, SupportsTools: true},
}
