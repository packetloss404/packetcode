package mcp

import (
	"runtime"
	"strings"
	"testing"
)

func TestServerEnv_FiltersProcessEnvAndPreservesConfiguredEnv(t *testing.T) {
	got := serverEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/alice",
		"PACKETCODE_OPENAI_API_KEY=sk-secret",
		"SHELL=/bin/bash",
	}, map[string]string{
		"CUSTOM_TOKEN": "server-owned",
		"PATH":         "/custom/bin",
	}, nil)

	env := envMap(got)
	if env["PATH"] != "/custom/bin" {
		t.Fatalf("PATH = %q, want configured override", env["PATH"])
	}
	if env["HOME"] != "/home/alice" {
		t.Fatalf("HOME = %q, want inherited home", env["HOME"])
	}
	if env["CUSTOM_TOKEN"] != "server-owned" {
		t.Fatalf("CUSTOM_TOKEN = %q, want configured env preserved", env["CUSTOM_TOKEN"])
	}
	if _, ok := env["PACKETCODE_OPENAI_API_KEY"]; ok {
		t.Fatalf("provider API key leaked into MCP server env: %v", got)
	}
	if _, ok := env["SHELL"]; ok {
		t.Fatalf("unlisted shell state leaked into MCP server env: %v", got)
	}
}

func TestServerEnv_EnvFromCopiesOnlyNamedProcessSecrets(t *testing.T) {
	got := serverEnv([]string{
		"PATH=/usr/bin",
		"GITHUB_TOKEN=gh-secret",
		"PACKETCODE_OPENAI_API_KEY=sk-secret",
	}, map[string]string{
		"GITHUB_TOKEN": "configured-wins",
	}, []string{"GITHUB_TOKEN", "PACKETCODE_OPENAI_API_KEY"})

	env := envMap(got)
	if env["GITHUB_TOKEN"] != "configured-wins" {
		t.Fatalf("configured env should override env_from value, got %q", env["GITHUB_TOKEN"])
	}
	if env["PACKETCODE_OPENAI_API_KEY"] != "sk-secret" {
		t.Fatalf("explicit env_from secret missing: %v", got)
	}
}

func TestMergeEnv_ReplacesCaseInsensitiveOnWindows(t *testing.T) {
	base := []string{"Path=/usr/bin"}
	got := mergeEnv(base, map[string]string{"PATH": "/custom/bin"})
	env := envMap(got)

	if runtime.GOOS == "windows" {
		if len(got) != 1 || env["PATH"] != "/custom/bin" {
			t.Fatalf("windows merge = %v, want one PATH override", got)
		}
		return
	}
	if len(got) != 2 {
		t.Fatalf("non-windows merge = %v, want distinct Path and PATH entries", got)
	}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			out[k] = v
			if runtime.GOOS == "windows" {
				out[strings.ToUpper(k)] = v
			}
		}
	}
	return out
}
