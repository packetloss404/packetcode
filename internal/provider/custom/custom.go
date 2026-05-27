// Package custom implements user-configured OpenAI-compatible providers.
package custom

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/provider/openaicompat"
)

// Config describes a user-defined OpenAI-compatible endpoint.
type Config struct {
	Slug           string
	DisplayName    string
	BaseURL        string
	APIKey         string
	APIKeyRequired bool
	BrandColor     string
	Headers        map[string]string
	DefaultModel   string
	Models         []ModelConfig
}

// ModelConfig is the custom-provider model metadata shape.
type ModelConfig struct {
	ID            string
	DisplayName   string
	ContextWindow int
	SupportsTools *bool
	InputPer1M    float64
	OutputPer1M   float64
}

// Provider wraps the shared OpenAI-compatible client with config-driven
// identity, headers, key policy, and static model fallback.
type Provider struct {
	slug           string
	displayName    string
	brandColor     lipgloss.Color
	apiKeyRequired bool
	defaultModel   string
	staticModels   []provider.Model
	client         *openaicompat.Client
	configErr      error
}

// NewOpenAICompatible constructs a custom provider. Configuration errors
// are stored and surfaced through Provider methods so callers can still
// render the provider row and doctor can report actionable diagnostics.
func NewOpenAICompatible(cfg Config) *Provider {
	slug := strings.TrimSpace(cfg.Slug)
	displayName := strings.TrimSpace(cfg.DisplayName)
	if displayName == "" {
		displayName = slug
	}
	color := lipgloss.Color("#64748B")
	if strings.TrimSpace(cfg.BrandColor) != "" {
		color = lipgloss.Color(strings.TrimSpace(cfg.BrandColor))
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	p := &Provider{
		slug:           slug,
		displayName:    displayName,
		brandColor:     color,
		apiKeyRequired: cfg.APIKeyRequired,
		defaultModel:   strings.TrimSpace(cfg.DefaultModel),
		staticModels:   staticModels(cfg),
		client:         openaicompat.NewClient(baseURL, cfg.APIKey),
	}
	if err := validateBaseURL(baseURL); err != nil {
		p.configErr = err
	}
	headers := copyHeaders(cfg.Headers)
	if len(headers) > 0 {
		p.client.ExtraHeaders = func(req *http.Request) {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}
	return p
}

func (p *Provider) Name() string               { return p.displayName }
func (p *Provider) Slug() string               { return p.slug }
func (p *Provider) BrandColor() lipgloss.Color { return p.brandColor }

func (p *Provider) ValidateKey(ctx context.Context, apiKey string) error {
	if p.configErr != nil {
		return p.configErr
	}
	if p.apiKeyRequired && strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("api key is empty")
	}
	if strings.TrimSpace(apiKey) != "" {
		return p.client.ValidateKey(ctx, apiKey)
	}
	if len(p.staticModels) > 0 {
		return nil
	}
	_, err := p.client.ListModels(ctx)
	return err
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	raw, err := p.client.ListModels(ctx)
	if err != nil || len(raw) == 0 {
		if len(p.staticModels) > 0 {
			return cloneModels(p.staticModels), nil
		}
		if err != nil {
			return nil, err
		}
	}
	out := make([]provider.Model, 0, len(raw))
	for _, m := range raw {
		out = append(out, p.enrichModel(m))
	}
	return prioritizeDefault(out, p.defaultModel), nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	return p.client.ChatCompletion(ctx, req)
}

func (p *Provider) Pricing(modelID string) (float64, float64) {
	for _, m := range p.staticModels {
		if m.ID == modelID {
			return m.InputPer1M, m.OutputPer1M
		}
	}
	return 0, 0
}

func (p *Provider) ContextWindow(modelID string) int {
	for _, m := range p.staticModels {
		if m.ID == modelID && m.ContextWindow > 0 {
			return m.ContextWindow
		}
	}
	return 128_000
}

func (p *Provider) SupportsTools(modelID string) bool {
	for _, m := range p.staticModels {
		if m.ID == modelID {
			return m.SupportsTools
		}
	}
	return true
}

func (p *Provider) enrichModel(m provider.Model) provider.Model {
	for _, configured := range p.staticModels {
		if configured.ID != m.ID {
			continue
		}
		if m.DisplayName == "" || m.DisplayName == m.ID {
			m.DisplayName = configured.DisplayName
		}
		if m.ContextWindow == 0 {
			m.ContextWindow = configured.ContextWindow
		}
		m.SupportsTools = configured.SupportsTools
		if m.InputPer1M == 0 {
			m.InputPer1M = configured.InputPer1M
		}
		if m.OutputPer1M == 0 {
			m.OutputPer1M = configured.OutputPer1M
		}
		return m
	}
	if m.ContextWindow == 0 {
		m.ContextWindow = 128_000
	}
	m.SupportsTools = true
	return m
}

func staticModels(cfg Config) []provider.Model {
	out := make([]provider.Model, 0, len(cfg.Models)+1)
	seen := map[string]bool{}
	for _, m := range cfg.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		supportsTools := true
		if m.SupportsTools != nil {
			supportsTools = *m.SupportsTools
		}
		display := strings.TrimSpace(m.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, provider.Model{
			ID:            id,
			DisplayName:   display,
			ContextWindow: m.ContextWindow,
			SupportsTools: supportsTools,
			InputPer1M:    m.InputPer1M,
			OutputPer1M:   m.OutputPer1M,
		})
		seen[id] = true
	}
	if cfg.DefaultModel != "" && !seen[cfg.DefaultModel] {
		out = append(out, provider.Model{
			ID:            cfg.DefaultModel,
			DisplayName:   cfg.DefaultModel,
			ContextWindow: 128_000,
			SupportsTools: true,
		})
	}
	return prioritizeDefault(out, cfg.DefaultModel)
}

func cloneModels(in []provider.Model) []provider.Model {
	out := make([]provider.Model, len(in))
	copy(out, in)
	return out
}

func prioritizeDefault(models []provider.Model, defaultModel string) []provider.Model {
	if defaultModel == "" {
		return models
	}
	for i, m := range models {
		if m.ID != defaultModel {
			continue
		}
		if i == 0 {
			return models
		}
		out := make([]provider.Model, 0, len(models))
		out = append(out, m)
		out = append(out, models[:i]...)
		out = append(out, models[i+1:]...)
		return out
	}
	return models
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url must use http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("base_url must include a host")
	}
	return nil
}

func copyHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	return out
}
