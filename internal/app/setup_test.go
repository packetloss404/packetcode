package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/provider"
)

func TestPromptProvider_EmptyFactoriesReturnsError(t *testing.T) {
	var out bytes.Buffer
	_, err := promptProvider(bufio.NewReader(strings.NewReader("\n")), &out, nil)
	if err == nil {
		t.Fatalf("expected error for empty factories")
	}
	if !strings.Contains(err.Error(), "no providers available") {
		t.Fatalf("error = %v", err)
	}
}

func TestPromptProvider_SkipsNilFactories(t *testing.T) {
	var out bytes.Buffer
	got, err := promptProvider(bufio.NewReader(strings.NewReader("\n")), &out, FactoryMap{
		"dead": nil,
		"ok":   func(string) provider.Provider { return nil },
	})
	if err != nil {
		t.Fatalf("promptProvider: %v", err)
	}
	if got != "ok" {
		t.Fatalf("provider = %q, want ok", got)
	}
}

func TestPromptProvider_UsesCanonicalDisplayOrder(t *testing.T) {
	var out bytes.Buffer
	got, err := promptProvider(bufio.NewReader(strings.NewReader("\n")), &out, FactoryMap{
		"zz-custom":  func(string) provider.Provider { return nil },
		"anthropic":  func(string) provider.Provider { return nil },
		"openai":     func(string) provider.Provider { return nil },
		"aa-custom":  func(string) provider.Provider { return nil },
		"openrouter": func(string) provider.Provider { return nil },
	})
	if err != nil {
		t.Fatalf("promptProvider: %v", err)
	}
	if got != "openai" {
		t.Fatalf("default provider = %q, want openai", got)
	}
	text := out.String()
	openAI := strings.Index(text, "openai")
	anthropic := strings.Index(text, "anthropic")
	custom := strings.Index(text, "aa-custom")
	if openAI < 0 || anthropic < 0 || custom < 0 || !(openAI < anthropic && anthropic < custom) {
		t.Fatalf("unexpected provider order:\n%s", text)
	}
}

func TestPromptKey_EmptyRetries(t *testing.T) {
	var out bytes.Buffer
	cfg := config.Default()
	key, err := promptKey(bufio.NewReader(strings.NewReader("\n  sk-test  \n")), &out, cfg, "openai")
	if err != nil {
		t.Fatalf("promptKey: %v", err)
	}
	if key != "sk-test" {
		t.Fatalf("key = %q, want sk-test", key)
	}
	if !strings.Contains(out.String(), "cannot be empty") {
		t.Fatalf("missing empty-key feedback:\n%s", out.String())
	}
}

func TestPromptModel_AcceptsExactIDAndFilter(t *testing.T) {
	models := []provider.Model{{ID: "gpt-5"}, {ID: "claude-sonnet"}, {ID: "gemini-pro"}}

	var out bytes.Buffer
	got, err := promptModel(bufio.NewReader(strings.NewReader("gemini-pro\n")), &out, models)
	if err != nil {
		t.Fatalf("promptModel exact: %v", err)
	}
	if got != "gemini-pro" {
		t.Fatalf("model = %q, want gemini-pro", got)
	}

	out.Reset()
	got, err = promptModel(bufio.NewReader(strings.NewReader("claude\n1\n")), &out, models)
	if err != nil {
		t.Fatalf("promptModel filter: %v", err)
	}
	if got != "claude-sonnet" {
		t.Fatalf("filtered model = %q, want claude-sonnet", got)
	}
}

func TestPromptModel_NumericSelectionLimitedToVisibleRows(t *testing.T) {
	models := make([]provider.Model, 25)
	for i := range models {
		models[i] = provider.Model{ID: "model-" + string(rune('a'+i))}
	}
	var out bytes.Buffer
	got, err := promptModel(bufio.NewReader(strings.NewReader("25\nmodel-y\n")), &out, models)
	if err != nil {
		t.Fatalf("promptModel: %v", err)
	}
	if got != "model-y" {
		t.Fatalf("model = %q, want model-y", got)
	}
	if !strings.Contains(out.String(), "visible list") {
		t.Fatalf("missing visible-list feedback:\n%s", out.String())
	}
}

func TestReadSetupSecret_FallbackReader(t *testing.T) {
	got, err := readSetupSecret(bufio.NewReader(strings.NewReader("sk-test\n")), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("readSetupSecret: %v", err)
	}
	if got != "sk-test\n" {
		t.Fatalf("secret = %q", got)
	}
}

func TestRunSetup_HappyPathSavesConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	cfg := config.Default()
	result, err := RunSetup(strings.NewReader("1\nsk-test\n2\n"), &bytes.Buffer{}, cfg, FactoryMap{
		"openai": func(key string) provider.Provider {
			return setupProvider{slug: "openai", key: key, models: []provider.Model{{ID: "gpt-5"}, {ID: "gpt-5-mini"}}}
		},
	})
	if err != nil {
		t.Fatalf("RunSetup: %v", err)
	}
	if result.Slug != "openai" || result.Model != "gpt-5-mini" {
		t.Fatalf("result = %+v", result)
	}
	if cfg.GetProviderKey("openai") != "sk-test" || cfg.Default.Provider != "openai" || cfg.Default.Model != "gpt-5-mini" {
		t.Fatalf("cfg not saved in memory: %+v", cfg)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.GetProviderKey("openai") != "sk-test" || reloaded.Default.Model != "gpt-5-mini" {
		t.Fatalf("reloaded cfg = %+v", reloaded)
	}
}

type setupProvider struct {
	slug        string
	key         string
	validateErr error
	models      []provider.Model
}

func (s setupProvider) Name() string                      { return s.slug }
func (s setupProvider) Slug() string                      { return s.slug }
func (s setupProvider) BrandColor() lipgloss.Color        { return lipgloss.Color("#000000") }
func (s setupProvider) Pricing(string) (float64, float64) { return 0, 0 }
func (s setupProvider) ContextWindow(string) int          { return 100_000 }
func (s setupProvider) SupportsTools(string) bool         { return true }
func (s setupProvider) ValidateKey(context.Context, string) error {
	if s.key == "bad" {
		return errors.New("bad key")
	}
	return s.validateErr
}
func (s setupProvider) ListModels(context.Context) ([]provider.Model, error) { return s.models, nil }
func (s setupProvider) ChatCompletion(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	return nil, nil
}
