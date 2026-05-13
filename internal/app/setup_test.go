package app

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

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
