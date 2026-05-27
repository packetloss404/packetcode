// Package openrouter implements provider.Provider against OpenRouter, the
// hosted proxy that exposes hundreds of models behind a single OpenAI-compatible
// endpoint.
//
// Two things distinguish OpenRouter from a vanilla OpenAI-compatible host:
//  1. Two HTTP headers (HTTP-Referer, X-Title) are required for proper
//     attribution and rate-limit treatment.
//  2. Pricing is per-model and changes constantly, so we read it from
//     OpenRouter's /models endpoint at startup and cache it in-process.
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/provider/openaicompat"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	slug           = "openrouter"
	displayName    = "OpenRouter"

	// referer / title satisfy OpenRouter's attribution requirement. These
	// are visible on their leaderboard at openrouter.ai/rankings.
	referer = "https://github.com/packetloss404/packetcode"
	title   = "packetcode"
)

// brandColor — was #9B59B6 (purple); switched to rose to keep the UI
// palette purple-free per design feedback.
var brandColor = lipgloss.Color("#EC4899")

type Provider struct {
	client     *openaicompat.Client
	httpClient *http.Client

	mu       sync.RWMutex
	pricing  map[string]priceEntry
	contexts map[string]int
	tools    map[string]bool
}

type priceEntry struct {
	Input  float64
	Output float64
}

func New(apiKey string) *Provider {
	return NewWithBaseURL(defaultBaseURL, apiKey)
}

func NewWithBaseURL(baseURL, apiKey string) *Provider {
	p := &Provider{
		client:     openaicompat.NewClient(baseURL, apiKey),
		httpClient: &http.Client{},
		pricing:    map[string]priceEntry{},
		contexts:   map[string]int{},
		tools:      map[string]bool{},
	}
	p.client.ExtraHeaders = func(req *http.Request) {
		req.Header.Set("HTTP-Referer", referer)
		req.Header.Set("X-Title", title)
	}
	return p
}

func (p *Provider) Name() string               { return displayName }
func (p *Provider) Slug() string               { return slug }
func (p *Provider) BrandColor() lipgloss.Color { return brandColor }

func (p *Provider) ValidateKey(ctx context.Context, apiKey string) error {
	return p.client.ValidateKey(ctx, apiKey)
}

// orModelsResponse is OpenRouter's /models payload. Pricing strings come
// back as USD per token (so 0.00000300 is $3 per 1M tokens).
type orModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextLength int    `json:"context_length"`
		Pricing       struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
		SupportedParameters []string `json:"supported_parameters"`
	} `json:"data"`
}

// ListModels fetches the full OpenRouter catalog and caches per-model
// pricing/context-window/tool-support metadata for later Pricing() calls.
func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	url := strings.TrimRight(p.client.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("HTTP-Referer", referer)
	req.Header.Set("X-Title", title)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list openrouter models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body := provider.ReadErrorBody(resp.Body)
		return nil, fmt.Errorf("list openrouter models: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed orModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode openrouter models: %w", err)
	}

	out := make([]provider.Model, 0, len(parsed.Data))
	pricing := map[string]priceEntry{}
	contexts := map[string]int{}
	tools := map[string]bool{}

	for _, m := range parsed.Data {
		inPer1M := per1MFromPerToken(m.Pricing.Prompt)
		outPer1M := per1MFromPerToken(m.Pricing.Completion)
		supportsTools := containsString(m.SupportedParameters, "tools")

		pricing[m.ID] = priceEntry{Input: inPer1M, Output: outPer1M}
		contexts[m.ID] = m.ContextLength
		tools[m.ID] = supportsTools

		out = append(out, provider.Model{
			ID:            m.ID,
			DisplayName:   m.Name,
			ContextWindow: m.ContextLength,
			SupportsTools: supportsTools,
			InputPer1M:    inPer1M,
			OutputPer1M:   outPer1M,
		})
	}

	p.mu.Lock()
	p.pricing = pricing
	p.contexts = contexts
	p.tools = tools
	p.mu.Unlock()

	return out, nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	return p.client.ChatCompletion(ctx, req)
}

func (p *Provider) Pricing(modelID string) (float64, float64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if entry, ok := p.pricing[modelID]; ok {
		return entry.Input, entry.Output
	}
	return 3.00, 15.00
}

func (p *Provider) ContextWindow(modelID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if ctxWindow, ok := p.contexts[modelID]; ok {
		return ctxWindow
	}
	return 128_000
}

func (p *Provider) SupportsTools(modelID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if supports, ok := p.tools[modelID]; ok {
		return supports
	}
	return true
}

// per1MFromPerToken converts OpenRouter's per-token decimal string into
// per-1M-tokens dollars. Returns 0 on parse failure or empty input.
func per1MFromPerToken(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 1_000_000
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
