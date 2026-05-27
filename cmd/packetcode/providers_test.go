package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/config"
)

func TestProviderFactoriesFromConfig_AddsCustomOpenAICompatibleProvider(t *testing.T) {
	keyless := false
	cfg := config.Default()
	cfg.Providers["localai"] = config.ProviderConfig{
		Type:           "openai_compatible",
		BaseURL:        "http://localhost:8080/v1",
		DisplayName:    "LocalAI",
		APIKeyRequired: &keyless,
		DefaultModel:   "local-model",
	}

	factories := providerFactoriesFromConfig(cfg)
	factory, ok := factories["localai"]
	require.True(t, ok)

	prov := factory("")
	assert.Equal(t, "localai", prov.Slug())
	assert.Equal(t, "LocalAI", prov.Name())
	assert.True(t, providerRequiresAPIKey(cfg, "openai"))
	assert.False(t, providerRequiresAPIKey(cfg, "localai"))
}

func TestProviderFactoriesFromConfig_SkipsNonCustomUnknownProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["typo"] = config.ProviderConfig{DefaultModel: "m"}

	factories := providerFactoriesFromConfig(cfg)
	_, ok := factories["typo"]
	assert.False(t, ok)
}

func TestProviderFactoriesFromConfig_DoesNotOverrideBuiltInSlug(t *testing.T) {
	keyRequired := false
	cfg := config.Default()
	cfg.Providers["openai"] = config.ProviderConfig{
		Type:           "openai_compatible",
		BaseURL:        "http://localhost:8080/v1",
		APIKeyRequired: &keyRequired,
	}

	factories := providerFactoriesFromConfig(cfg)
	prov := factories["openai"]("sk-test")

	assert.Equal(t, "openai", prov.Slug())
	assert.Equal(t, "OpenAI", prov.Name())
	assert.True(t, providerRequiresAPIKey(cfg, "openai"))
}
