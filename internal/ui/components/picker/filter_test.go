package picker

import "testing"

func TestMatches_EmptyFilter(t *testing.T) {
	it := Item{ID: "openai", Label: "OpenAI", Detail: "gpt-5.5"}
	if !matches(it, "") {
		t.Fatalf("empty filter must match every item")
	}
	if !matches(it, "   ") {
		t.Fatalf("whitespace-only filter must match every item")
	}
}

func TestMatches_CaseInsensitiveSubstring(t *testing.T) {
	it := Item{ID: "gpt-5.5", Label: "OpenAI gpt-5.5"}
	if !matches(it, "GPT") {
		t.Fatalf("uppercase filter should match lowercase id")
	}
	if !matches(it, "openai") {
		t.Fatalf("lowercase filter should match mixed-case label")
	}
}

func TestMatches_WhitespaceToDash(t *testing.T) {
	it := Item{ID: "gpt-5.5", Label: "OpenAI gpt-5.5"}
	if !matches(it, "gpt 5.5") {
		t.Fatalf("filter with space should match dashed id")
	}
}

func TestMatches_MultipleTokens(t *testing.T) {
	it := Item{ID: "anthropic/claude-sonnet-3.7", Label: "Claude Sonnet 3.7"}
	if !matches(it, "claude sonnet") {
		t.Fatalf("multi-word filter should match concatenated id")
	}
}

func TestMatches_NoMatch(t *testing.T) {
	it := Item{ID: "gpt-5.5", Label: "OpenAI gpt-5.5", Detail: "reasoning"}
	if matches(it, "foo") {
		t.Fatalf("filter %q should not match", "foo")
	}
}

func TestMatches_SearchesAllFields(t *testing.T) {
	it := Item{ID: "alpha", Label: "Bravo", Detail: "Charlie", Status: "key needed"}
	if !matches(it, "alpha") {
		t.Fatalf("should match on ID")
	}
	if !matches(it, "bravo") {
		t.Fatalf("should match on Label")
	}
	if !matches(it, "charlie") {
		t.Fatalf("should match on Detail")
	}
	if !matches(it, "KEY NEEDED") {
		t.Fatalf("should match on Status")
	}
}

// TestNormalize_Exported smoke-tests the exported Normalize wrapper.
// Other packages (autocomplete) depend on this behaviour, so the
// exported surface needs its own direct test.
func TestNormalize_Exported(t *testing.T) {
	if got := Normalize("GPT 5.5"); got != "gpt-5.5" {
		t.Fatalf("Normalize(\"GPT 5.5\") = %q, want %q", got, "gpt-5.5")
	}
	if got := Normalize(""); got != "" {
		t.Fatalf("Normalize(\"\") = %q, want empty", got)
	}
}

// TestMatches_Exported smoke-tests the exported Matches wrapper
// delegates to the existing substring logic.
func TestMatches_Exported(t *testing.T) {
	it := Item{ID: "gpt-5.5", Label: "OpenAI gpt-5.5"}
	if !Matches(it, "GPT") {
		t.Fatalf("exported Matches should be case-insensitive")
	}
	if Matches(it, "zzz") {
		t.Fatalf("exported Matches should return false on miss")
	}
}
