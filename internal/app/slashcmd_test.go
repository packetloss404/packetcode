package app

import (
	"reflect"
	"testing"
)

// TestParseSlashCommand_Spawn exercises happy-path, flag, and malformed
// /spawn variants.

func TestParseSlashCommand_Spawn(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/spawn hello")
		if !ok || cmd != "spawn" {
			t.Fatalf("ParseSlashCommand = %q %v %v", cmd, args, ok)
		}
		if !reflect.DeepEqual(args, []string{"hello"}) {
			t.Fatalf("args = %v", args)
		}
		prov, model, write, prompt, err := ParseSpawnFlags(args)
		if err != nil {
			t.Fatalf("ParseSpawnFlags: %v", err)
		}
		if prov != "" || model != "" || write {
			t.Fatalf("expected no flags, got prov=%q model=%q write=%v", prov, model, write)
		}
		if prompt != "hello" {
			t.Fatalf("prompt = %q", prompt)
		}
	})

	t.Run("flags and multiword prompt", func(t *testing.T) {
		args := []string{"--provider", "gemini", "--model", "g-flash", "hello", "there"}
		prov, model, write, prompt, err := ParseSpawnFlags(args)
		if err != nil {
			t.Fatalf("ParseSpawnFlags: %v", err)
		}
		if prov != "gemini" || model != "g-flash" {
			t.Fatalf("prov=%q model=%q", prov, model)
		}
		if write {
			t.Fatalf("--write not present but allowWrite=true")
		}
		if prompt != "hello there" {
			t.Fatalf("prompt = %q", prompt)
		}
	})

	t.Run("--write", func(t *testing.T) {
		_, _, write, prompt, err := ParseSpawnFlags([]string{"--write", "hello"})
		if err != nil {
			t.Fatalf("ParseSpawnFlags: %v", err)
		}
		if !write {
			t.Fatalf("allowWrite = false")
		}
		if prompt != "hello" {
			t.Fatalf("prompt = %q", prompt)
		}
	})

	t.Run("flag without value", func(t *testing.T) {
		_, _, _, _, err := ParseSpawnFlags([]string{"--provider"})
		if err == nil {
			t.Fatalf("expected error for bare --provider")
		}
	})

	t.Run("empty prompt", func(t *testing.T) {
		_, _, _, _, err := ParseSpawnFlags([]string{"--write"})
		if err == nil {
			t.Fatalf("expected error for empty prompt")
		}
	})

	t.Run("flags order swapped", func(t *testing.T) {
		prov, model, write, prompt, err := ParseSpawnFlags(
			[]string{"--model", "m1", "--write", "--provider", "p1", "do", "stuff"})
		if err != nil {
			t.Fatalf("ParseSpawnFlags: %v", err)
		}
		if prov != "p1" || model != "m1" || !write || prompt != "do stuff" {
			t.Fatalf("unexpected: prov=%q model=%q write=%v prompt=%q", prov, model, write, prompt)
		}
	})

	t.Run("prompt preserves flag-like tokens once started", func(t *testing.T) {
		_, _, _, prompt, err := ParseSpawnFlags([]string{"hello", "--write"})
		if err != nil {
			t.Fatalf("ParseSpawnFlags: %v", err)
		}
		// After the first non-flag token the rest is prompt verbatim.
		if prompt != "hello --write" {
			t.Fatalf("prompt = %q", prompt)
		}
	})
}

// TestParseSlashCommand_Jobs exercises /jobs list/detail parsing.

func TestParseSlashCommand_Jobs(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/jobs")
		if !ok || cmd != "jobs" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("with id", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/jobs 7f3a")
		if !ok || cmd != "jobs" {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
		if !reflect.DeepEqual(args, []string{"7f3a"}) {
			t.Fatalf("args = %v", args)
		}
	})
	t.Run("whitespace tolerated", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("   /jobs    abc  ")
		if !ok || cmd != "jobs" || !reflect.DeepEqual(args, []string{"abc"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

func TestParseSlashCommand_Agents(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/agents")
		if !ok || cmd != "agents" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("with id", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/agents 7f3a")
		if !ok || cmd != "agents" || !reflect.DeepEqual(args, []string{"7f3a"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// TestParseSlashCommand_Cancel exercises single-job and cancel-all parsing.

func TestParseSlashCommand_Cancel(t *testing.T) {
	t.Run("by id", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cancel 7f3a")
		if !ok || cmd != "cancel" || !reflect.DeepEqual(args, []string{"7f3a"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("all", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cancel all")
		if !ok || cmd != "cancel" || !reflect.DeepEqual(args, []string{"all"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("missing arg still parses; handler should reject", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cancel")
		if !ok || cmd != "cancel" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// Bonus: non-slash / unknown command.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_NotACommand(t *testing.T) {
	for _, in := range []string{"hello", " ", "", "not/slash", "/unknown foo"} {
		if _, _, ok := ParseSlashCommand(in); ok {
			t.Fatalf("input %q: expected ok=false", in)
		}
	}
}

// ---------------------------------------------------------------------------
// /provider parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Provider(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/provider")
		if !ok || cmd != "provider" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("with slug", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/provider gemini")
		if !ok || cmd != "provider" || !reflect.DeepEqual(args, []string{"gemini"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("add key shortcut", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/provider add gemini")
		if !ok || cmd != "provider" || !reflect.DeepEqual(args, []string{"add", "gemini"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("whitespace tolerated", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("   /provider    openai  ")
		if !ok || cmd != "provider" || !reflect.DeepEqual(args, []string{"openai"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("concatenated is not a command", func(t *testing.T) {
		if _, _, ok := ParseSlashCommand("/providergemini"); ok {
			t.Fatalf("expected ok=false for /providergemini")
		}
	})
}

// TestParseSlashCommand_PluralAliases verifies /providers and /models
// parse as recognised commands (they dispatch to the same handlers as
// their singular forms).
func TestParseSlashCommand_PluralAliases(t *testing.T) {
	t.Run("providers", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/providers")
		if !ok || cmd != "providers" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("models", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/models")
		if !ok || cmd != "models" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// /model parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Model(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/model")
		if !ok || cmd != "model" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("with id", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/model gpt-4.1")
		if !ok || cmd != "model" || !reflect.DeepEqual(args, []string{"gpt-4.1"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("trailing extra token", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/model gpt-4.1 extra")
		if !ok || cmd != "model" || !reflect.DeepEqual(args, []string{"gpt-4.1", "extra"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// /sessions parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Sessions(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/sessions")
		if !ok || cmd != "sessions" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("resume", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/sessions resume 7f3a")
		if !ok || cmd != "sessions" || !reflect.DeepEqual(args, []string{"resume", "7f3a"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("delete with yes", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/sessions delete 7f3a --yes")
		if !ok || cmd != "sessions" {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
		if !reflect.DeepEqual(args, []string{"delete", "7f3a", "--yes"}) {
			t.Fatalf("args = %v", args)
		}
	})
}

// ---------------------------------------------------------------------------
// /undo parse.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Undo(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/undo")
	if !ok || cmd != "undo" || len(args) != 0 {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
}

// ---------------------------------------------------------------------------
// /compact parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Compact(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/compact")
		if !ok || cmd != "compact" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("with --keep", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/compact --keep 5")
		if !ok || cmd != "compact" || !reflect.DeepEqual(args, []string{"--keep", "5"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// /cost parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Cost(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cost")
		if !ok || cmd != "cost" || len(args) != 0 {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("reset", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cost reset")
		if !ok || cmd != "cost" || !reflect.DeepEqual(args, []string{"reset"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
	t.Run("reset --yes", func(t *testing.T) {
		cmd, args, ok := ParseSlashCommand("/cost reset --yes")
		if !ok || cmd != "cost" || !reflect.DeepEqual(args, []string{"reset", "--yes"}) {
			t.Fatalf("parse = %q %v %v", cmd, args, ok)
		}
	})
}

// ---------------------------------------------------------------------------
// /trust parse variants.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Trust(t *testing.T) {
	for _, in := range []string{"/trust", "/trust on", "/trust off"} {
		cmd, _, ok := ParseSlashCommand(in)
		if !ok || cmd != "trust" {
			t.Fatalf("parse %q = %q %v", in, cmd, ok)
		}
	}
}

// ---------------------------------------------------------------------------
// /help parse.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Help(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/help")
	if !ok || cmd != "help" || len(args) != 0 {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
}

// ---------------------------------------------------------------------------
// /clear parse.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_Clear(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/clear")
	if !ok || cmd != "clear" || len(args) != 0 {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
}

func TestParseSlashCommand_StatusLine(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/statusline refresh")
	if !ok || cmd != "statusline" || !reflect.DeepEqual(args, []string{"refresh"}) {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
}

func TestParseSlashCommand_Transcript(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/transcript")
	if !ok || cmd != "transcript" || len(args) != 0 {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
}

// ---------------------------------------------------------------------------
// Regression: autocomplete's acceptAutocomplete leaves a trailing space in
// the input buffer ("/spawn "). The parser must tolerate that so the
// submit-without-args path (user hits Enter immediately after accepting)
// still parses as the bare verb.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_TrailingSpaceAfterVerb(t *testing.T) {
	cmd, args, ok := ParseSlashCommand("/spawn ")
	if !ok || cmd != "spawn" {
		t.Fatalf("parse = %q %v %v", cmd, args, ok)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want empty", args)
	}
}

// ---------------------------------------------------------------------------
// Unknown verbs must still be rejected by the allow-list.
// ---------------------------------------------------------------------------

func TestParseSlashCommand_UnknownStillReturnsFalse(t *testing.T) {
	for _, in := range []string{"/foo", "/bar baz", "/sessio resume"} {
		if _, _, ok := ParseSlashCommand(in); ok {
			t.Fatalf("input %q: expected ok=false", in)
		}
	}
}

// ---------------------------------------------------------------------------
// Sub-arg parser tests.
// ---------------------------------------------------------------------------

func TestParseCompactFlags(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		keep, err := parseCompactFlags(nil)
		if err != nil || keep != 10 {
			t.Fatalf("parseCompactFlags(nil) = %d %v", keep, err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		keep, err := parseCompactFlags([]string{"--keep", "20"})
		if err != nil || keep != 20 {
			t.Fatalf("got %d %v, want 20 nil", keep, err)
		}
	})
	t.Run("missing value", func(t *testing.T) {
		_, err := parseCompactFlags([]string{"--keep"})
		if err == nil {
			t.Fatalf("expected error for --keep without value")
		}
	})
	t.Run("non-integer", func(t *testing.T) {
		_, err := parseCompactFlags([]string{"--keep", "abc"})
		if err == nil {
			t.Fatalf("expected error for non-integer --keep value")
		}
	})
	t.Run("negative", func(t *testing.T) {
		_, err := parseCompactFlags([]string{"--keep", "-3"})
		if err == nil {
			t.Fatalf("expected error for negative --keep value")
		}
	})
	t.Run("zero", func(t *testing.T) {
		_, err := parseCompactFlags([]string{"--keep", "0"})
		if err == nil {
			t.Fatalf("expected error for zero --keep value")
		}
	})
	t.Run("trailing junk", func(t *testing.T) {
		_, err := parseCompactFlags([]string{"--keep", "5", "extra"})
		if err == nil {
			t.Fatalf("expected error for trailing junk")
		}
	})
}

func TestParseSessionsArgs(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		sub, id, yes, err := parseSessionsArgs(nil)
		if err != nil || sub != "" || id != "" || yes {
			t.Fatalf("parseSessionsArgs(nil) = %q %q %v %v", sub, id, yes, err)
		}
	})
	t.Run("resume with id", func(t *testing.T) {
		sub, id, yes, err := parseSessionsArgs([]string{"resume", "7f3a"})
		if err != nil || sub != "resume" || id != "7f3a" || yes {
			t.Fatalf("got %q %q %v %v", sub, id, yes, err)
		}
	})
	t.Run("delete without yes", func(t *testing.T) {
		sub, id, yes, err := parseSessionsArgs([]string{"delete", "7f3a"})
		if err != nil || sub != "delete" || id != "7f3a" || yes {
			t.Fatalf("got %q %q %v %v", sub, id, yes, err)
		}
	})
	t.Run("delete with yes", func(t *testing.T) {
		sub, id, yes, err := parseSessionsArgs([]string{"delete", "7f3a", "--yes"})
		if err != nil || sub != "delete" || id != "7f3a" || !yes {
			t.Fatalf("got %q %q %v %v", sub, id, yes, err)
		}
	})
	t.Run("missing id", func(t *testing.T) {
		_, _, _, err := parseSessionsArgs([]string{"resume"})
		if err == nil {
			t.Fatalf("expected error for missing id")
		}
	})
	t.Run("unknown sub", func(t *testing.T) {
		_, _, _, err := parseSessionsArgs([]string{"purge"})
		if err == nil {
			t.Fatalf("expected error for unknown subcommand")
		}
	})
}

func TestParseCostArgs(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		reset, yes, err := parseCostArgs(nil)
		if err != nil || reset || yes {
			t.Fatalf("got reset=%v yes=%v err=%v", reset, yes, err)
		}
	})
	t.Run("reset", func(t *testing.T) {
		reset, yes, err := parseCostArgs([]string{"reset"})
		if err != nil || !reset || yes {
			t.Fatalf("got reset=%v yes=%v err=%v", reset, yes, err)
		}
	})
	t.Run("reset --yes", func(t *testing.T) {
		reset, yes, err := parseCostArgs([]string{"reset", "--yes"})
		if err != nil || !reset || !yes {
			t.Fatalf("got reset=%v yes=%v err=%v", reset, yes, err)
		}
	})
	t.Run("bogus", func(t *testing.T) {
		_, _, err := parseCostArgs([]string{"wipe"})
		if err == nil {
			t.Fatalf("expected error for unknown subcommand")
		}
	})
}

func TestParseTrustArgs(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		set, value, err := parseTrustArgs(nil)
		if err != nil || set || value {
			t.Fatalf("got set=%v value=%v err=%v", set, value, err)
		}
	})
	t.Run("on", func(t *testing.T) {
		set, value, err := parseTrustArgs([]string{"on"})
		if err != nil || !set || !value {
			t.Fatalf("got set=%v value=%v err=%v", set, value, err)
		}
	})
	t.Run("off", func(t *testing.T) {
		set, value, err := parseTrustArgs([]string{"off"})
		if err != nil || !set || value {
			t.Fatalf("got set=%v value=%v err=%v", set, value, err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		_, _, err := parseTrustArgs([]string{"maybe"})
		if err == nil {
			t.Fatalf("expected error for unknown value")
		}
	})
}
