package minimax

type pricingEntry struct {
	Input         float64
	Output        float64
	ContextWindow int
	SupportsTools bool
}

// Prices last verified against MiniMax's public Pay-as-You-Go pricing in
// May 2026. Context windows come from the MiniMax text model overview.
var pricingTable = map[string]pricingEntry{
	"MiniMax-M2.7-highspeed": {Input: 0.60, Output: 2.40, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2.7":           {Input: 0.30, Output: 1.20, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2.5-highspeed": {Input: 0.60, Output: 2.40, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2.5":           {Input: 0.30, Output: 1.20, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2.1-highspeed": {Input: 0.60, Output: 2.40, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2.1":           {Input: 0.30, Output: 1.20, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-M2":             {Input: 0.30, Output: 1.20, ContextWindow: 204_800, SupportsTools: true},
	"MiniMax-Text-01":        {Input: 0.20, Output: 1.10, ContextWindow: 1_000_000, SupportsTools: true},
	"abab6.5s-chat":          {Input: 1.00, Output: 1.00, ContextWindow: 245_000, SupportsTools: false},
	"abab6.5-chat":           {Input: 5.00, Output: 5.00, ContextWindow: 245_000, SupportsTools: false},
}

// fallbackModels are surfaced when the API doesn't expose a model-list
// endpoint we can use. Listing keeps the model selector functional even
// with zero discovery support upstream.
var fallbackModels = []string{
	"MiniMax-M2.7-highspeed",
	"MiniMax-M2.7",
	"MiniMax-M2.5-highspeed",
	"MiniMax-M2.5",
	"MiniMax-M2.1-highspeed",
	"MiniMax-M2.1",
	"MiniMax-M2",
	"MiniMax-Text-01",
}
