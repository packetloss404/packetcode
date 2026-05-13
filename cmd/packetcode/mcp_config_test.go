package main

import (
	"testing"

	"github.com/packetcode/packetcode/internal/config"
	"github.com/packetcode/packetcode/internal/mcp"
)

// TestMCPConfigFlatten_SortedByName asserts the flattener returns a
// slice in alphabetic order regardless of the map's insertion order.
// Go map iteration is randomised, so even though we insert in a
// deliberately non-alphabetic sequence the returned slice must be
// sorted.
func TestMCPConfigFlatten_SortedByName(t *testing.T) {
	cfg := &config.Config{
		MCP: map[string]config.MCPServerConfig{
			"zulu":   {Command: "z"},
			"alpha":  {Command: "a"},
			"mike":   {Command: "m"},
			"bravo":  {Command: "b"},
			"yankee": {Command: "y"},
		},
	}
	got := mcpServerConfigsFrom(cfg)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	want := []string{"alpha", "bravo", "mike", "yankee", "zulu"}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("got[%d].Name = %q, want %q (full: %v)", i, got[i].Name, name, names(got))
		}
	}
}

// TestMCPConfigFlatten_DefaultsTimeout asserts zero/missing TimeoutSec
// defaults to the 10-second per-server budget.
func TestMCPConfigFlatten_DefaultsTimeout(t *testing.T) {
	cfg := &config.Config{
		MCP: map[string]config.MCPServerConfig{
			"zero":     {Command: "a"},
			"explicit": {Command: "b", TimeoutSec: 42},
		},
	}
	got := mcpServerConfigsFrom(cfg)
	byName := map[string]int{}
	for _, sc := range got {
		byName[sc.Name] = sc.TimeoutSec
	}
	if byName["zero"] != defaultMCPTimeoutSec {
		t.Errorf("zero.TimeoutSec = %d, want %d", byName["zero"], defaultMCPTimeoutSec)
	}
	if byName["explicit"] != 42 {
		t.Errorf("explicit.TimeoutSec = %d, want 42", byName["explicit"])
	}
}

// TestMCPConfigFlatten_Empty covers the nil-safe and zero-entry paths.
func TestMCPConfigFlatten_Empty(t *testing.T) {
	if got := mcpServerConfigsFrom(nil); got != nil {
		t.Errorf("nil cfg -> %v, want nil", got)
	}
	cfg := &config.Config{MCP: map[string]config.MCPServerConfig{}}
	if got := mcpServerConfigsFrom(cfg); got != nil {
		t.Errorf("empty map -> %v, want nil", got)
	}
}

// TestMCPConfigFlatten_IsEnabledPreserved proves the Enabled pointer-
// bool contract (nil = enabled) is honoured when flattening.
func TestMCPConfigFlatten_IsEnabledPreserved(t *testing.T) {
	disabled := false
	enabled := true
	cfg := &config.Config{
		MCP: map[string]config.MCPServerConfig{
			"on":      {Command: "a", Enabled: &enabled},
			"off":     {Command: "b", Enabled: &disabled},
			"default": {Command: "c"}, // nil pointer → enabled
		},
	}
	got := mcpServerConfigsFrom(cfg)
	byName := map[string]bool{}
	for _, sc := range got {
		byName[sc.Name] = sc.Enabled
	}
	if !byName["on"] || byName["off"] || !byName["default"] {
		t.Errorf("Enabled map = %v; want on=true off=false default=true", byName)
	}
}

func TestMCPConfigFlatten_EnvCopied(t *testing.T) {
	cfg := &config.Config{
		MCP: map[string]config.MCPServerConfig{
			"server": {Command: "cmd", Env: map[string]string{"TOKEN": "configured"}},
		},
	}
	got := mcpServerConfigsFrom(cfg)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	got[0].Env["TOKEN"] = "mutated"
	if cfg.MCP["server"].Env["TOKEN"] != "configured" {
		t.Fatalf("flattened Env aliases source config: %v", cfg.MCP["server"].Env)
	}
}

func TestShouldRunSetup_RespectsProviderOverride(t *testing.T) {
	cfg := config.Default()
	if shouldRunSetup(cfg, "openai") {
		t.Fatalf("explicit provider override should skip first-run setup")
	}
}

func TestShouldRunSetup_DefaultProviderMissingKey(t *testing.T) {
	cfg := config.Default()
	cfg.Default.Provider = "openai"
	if !shouldRunSetup(cfg, "") {
		t.Fatalf("saved default without key should run setup")
	}
}

func TestShouldRunSetup_DefaultProviderConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Default.Provider = "openai"
	cfg.Providers["openai"] = config.ProviderConfig{APIKey: "sk-test"}
	if shouldRunSetup(cfg, "") {
		t.Fatalf("saved default with a present key should skip setup")
	}
}

func TestShouldRunSetup_OllamaNeedsNoKey(t *testing.T) {
	cfg := config.Default()
	cfg.Default.Provider = "ollama"
	if shouldRunSetup(cfg, "") {
		t.Fatalf("ollama should skip setup without an API key")
	}
}

func TestOllamaHostEnvOverridesConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["ollama"] = config.ProviderConfig{Host: "http://localhost:11434"}
	t.Setenv("PACKETCODE_OLLAMA_HOST", "ollama.internal")

	if got := ollamaHost(cfg); got != "ollama.internal" {
		t.Fatalf("ollamaHost() = %q, want env override", got)
	}
}

// names returns the slice of server names from the flattened config —
// helper for error messages.
func names(cfgs []mcp.ServerConfig) []string {
	out := make([]string, len(cfgs))
	for i, sc := range cfgs {
		out[i] = sc.Name
	}
	return out
}
