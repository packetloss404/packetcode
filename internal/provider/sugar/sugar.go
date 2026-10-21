// Package sugar implements the built-in Sugar provider. Sugar exposes a
// private OpenAI-compatible API whose live model catalog includes Conduit, the
// task-aware router, plus every directly selectable model in the current
// stable.
package sugar

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/provider/openaicompat"
)

const (
	Slug           = "sugar"
	DisplayName    = "Sugar"
	DefaultBaseURL = "http://localhost:3211/api/v1"
	DefaultModel   = "sugar/conduit"
)

// HostedService is the Sugar deployment offered during sign-in on a machine
// that has never connected to one. DefaultBaseURL stays local so no runtime
// path silently reaches the public internet; this value is only ever a
// suggestion the operator confirms. It is a var so a fork can point its own
// builds elsewhere with -ldflags "-X ...sugar.HostedService=https://...".
var HostedService = "https://usesugar.dev"

var brandColor = lipgloss.Color("#A8D34F")

type Provider struct {
	client          *openaicompat.Client
	taskBridgeError error
	runtimeMu       sync.RWMutex
	runtime         RuntimeHooks
}

func New(apiKey string) *Provider {
	return NewWithBaseURL(DefaultBaseURL, apiKey)
}

func NewWithBaseURL(baseURL, apiKey string) *Provider {
	taskRoot, taskErr := taskBridgeBaseURL(os.Getenv("SUGAR_TASK_API_ROOT"))
	if taskRoot != "" {
		baseURL = taskRoot
	}
	client := openaicompat.NewClient(NormalizeBaseURL(baseURL), apiKey)
	client.ExtraChatFields = sugarChatFields
	return &Provider{client: client, runtime: DisabledRuntimeHooks{}, taskBridgeError: taskErr}
}

type sugarCacheWire struct {
	ConversationID       string `json:"conversation_id"`
	PrefixFingerprint    string `json:"prefix_fingerprint"`
	StablePrefixMessages int    `json:"stable_prefix_messages"`
	CompactionGeneration int    `json:"compaction_generation"`
	Mode                 string `json:"mode"`
	Retention            string `json:"retention"`
	Privacy              string `json:"privacy"`
}

func sugarChatFields(req provider.ChatRequest) (map[string]any, error) {
	if req.SugarCache == nil {
		return nil, nil
	}
	cache := *req.SugarCache
	if cache.Mode == "" {
		cache.Mode = provider.SugarCacheAuto
	}
	if cache.Retention == "" {
		cache.Retention = provider.SugarCacheProviderDefault
	}
	if cache.Privacy == "" {
		cache.Privacy = provider.SugarPrivacyStandard
	}
	if err := validateSugarCache(cache); err != nil {
		return nil, err
	}
	if cache.StablePrefixMessages > len(req.Messages) {
		return nil, fmt.Errorf("sugar_cache stable_prefix_messages exceeds the message count")
	}
	for i := 0; i < cache.StablePrefixMessages; i++ {
		if req.Messages[i].Role != provider.RoleSystem {
			return nil, fmt.Errorf("sugar_cache stable prefix may only include leading system messages")
		}
	}
	return map[string]any{"sugar_cache": sugarCacheWire{
		ConversationID:       cache.ConversationID,
		PrefixFingerprint:    cache.PrefixFingerprint,
		StablePrefixMessages: cache.StablePrefixMessages,
		CompactionGeneration: cache.CompactionGeneration,
		Mode:                 string(cache.Mode),
		Retention:            string(cache.Retention),
		Privacy:              string(cache.Privacy),
	}}, nil
}

func validateSugarCache(cache provider.SugarCacheMetadata) error {
	if !validOpaqueID(cache.ConversationID, 128) {
		return fmt.Errorf("sugar_cache conversation_id must be 1-128 URL-safe characters")
	}
	const fingerprintPrefix = "sha256:"
	if !strings.HasPrefix(cache.PrefixFingerprint, fingerprintPrefix) || len(cache.PrefixFingerprint) != len(fingerprintPrefix)+64 {
		return fmt.Errorf("sugar_cache prefix_fingerprint must be sha256:<64 lowercase hex characters>")
	}
	digest := cache.PrefixFingerprint[len(fingerprintPrefix):]
	if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != 32 || strings.ToLower(digest) != digest {
		return fmt.Errorf("sugar_cache prefix_fingerprint must be sha256:<64 lowercase hex characters>")
	}
	if cache.CompactionGeneration < 0 {
		return fmt.Errorf("sugar_cache compaction_generation must be non-negative")
	}
	if cache.StablePrefixMessages < 0 {
		return fmt.Errorf("sugar_cache stable_prefix_messages must be non-negative")
	}
	if cache.Mode != provider.SugarCacheAuto && cache.Mode != provider.SugarCacheOff {
		return fmt.Errorf("sugar_cache mode is invalid")
	}
	switch cache.Retention {
	case provider.SugarCacheProviderDefault, provider.SugarCache5Minutes, provider.SugarCache1Hour, provider.SugarCache30Minutes:
	default:
		return fmt.Errorf("sugar_cache retention is invalid")
	}
	if cache.Privacy != provider.SugarPrivacyStandard && cache.Privacy != provider.SugarPrivacyZDRRequired {
		return fmt.Errorf("sugar_cache privacy is invalid")
	}
	return nil
}

func validOpaqueID(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}

func NormalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return DefaultBaseURL
	}
	if strings.HasSuffix(base, "/api/v1") {
		return base
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/api/v1"
}

func (p *Provider) Name() string               { return DisplayName }
func (p *Provider) Slug() string               { return Slug }
func (p *Provider) BrandColor() lipgloss.Color { return brandColor }

func (p *Provider) ValidateKey(ctx context.Context, apiKey string) error {
	return p.client.ValidateKey(ctx, apiKey)
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.Model, error) {
	if p.taskBridgeError != nil {
		return nil, p.taskBridgeError
	}
	models, err := p.client.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i] = enrich(models[i])
	}
	return prioritizeConduit(models), nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if p.taskBridgeError != nil {
		return nil, p.taskBridgeError
	}
	return p.client.ChatCompletion(ctx, req)
}

// RuntimeHooks exposes the disabled-by-default Conduit runtime seam. It is
// intentionally not invoked by ChatCompletion; shadowing requires opt-in.
func (p *Provider) RuntimeHooks() RuntimeHooks {
	p.runtimeMu.RLock()
	defer p.runtimeMu.RUnlock()
	if p.runtime == nil {
		return DisabledRuntimeHooks{}
	}
	return p.runtime
}

func (p *Provider) SetRuntimeHooks(hooks RuntimeHooks) {
	if hooks == nil {
		hooks = DisabledRuntimeHooks{}
	}
	p.runtimeMu.Lock()
	defer p.runtimeMu.Unlock()
	p.runtime = hooks
}

// Sugar meters upstream usage at the service. Packetcode reports zero local
// API cost so it never presents a second, misleading bill.
func (p *Provider) Pricing(string) (float64, float64) { return 0, 0 }

func (p *Provider) ContextWindow(modelID string) int {
	switch modelID {
	case "sugar/kimi":
		return 262_144
	case "sugar/conduit", "sugar/glm", "sugar/deepseek", "sugar/minimax":
		return 200_000
	default:
		return 128_000
	}
}

func (p *Provider) SupportsTools(string) bool { return true }

func enrich(model provider.Model) provider.Model {
	switch model.ID {
	case "sugar/conduit":
		model.DisplayName = "Conduit (automatic)"
	case "sugar/kimi":
		model.DisplayName = "Kimi"
	case "sugar/glm":
		model.DisplayName = "GLM"
	case "sugar/deepseek":
		model.DisplayName = "DeepSeek"
	case "sugar/minimax":
		model.DisplayName = "MiniMax"
	}
	model.ContextWindow = (&Provider{}).ContextWindow(model.ID)
	model.SupportsTools = true
	model.InputPer1M = 0
	model.OutputPer1M = 0
	return model
}

func prioritizeConduit(models []provider.Model) []provider.Model {
	for i, model := range models {
		if model.ID != DefaultModel || i == 0 {
			continue
		}
		out := make([]provider.Model, 0, len(models))
		out = append(out, model)
		out = append(out, models[:i]...)
		out = append(out, models[i+1:]...)
		return out
	}
	return models
}
