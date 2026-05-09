package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownSlashCommand_LoadsFrontmatterAndArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	if err := os.WriteFile(path, []byte("---\ndescription: Review the selected code\n---\nCheck this:\n$ARGUMENTS\n"), 0o600); err != nil {
		t.Fatalf("write command: %v", err)
	}

	cmd, err := loadMarkdownSlashCommand(path, "user")
	if err != nil {
		t.Fatalf("loadMarkdownSlashCommand: %v", err)
	}
	if cmd.Name != "review" || cmd.Description != "Review the selected code" {
		t.Fatalf("unexpected metadata: %#v", cmd)
	}
	if cmd.Usage != "/review [arguments]" {
		t.Fatalf("usage = %q", cmd.Usage)
	}
	if got := cmd.Expand("internal/app"); got != "Check this:\ninternal/app" {
		t.Fatalf("Expand = %q", got)
	}
}

func TestSlashRegistry_ProjectOverridesUserButNotBuiltin(t *testing.T) {
	home := isolateHome(t)
	work := t.TempDir()
	userDir := filepath.Join(home, ".packetcode", "commands")
	projectDir := filepath.Join(work, ".packetcode", "commands")
	for _, dir := range []string{userDir, projectDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(userDir, "audit.md"), []byte("---\ndescription: User audit\n---\nuser $ARGUMENTS\n"), 0o600); err != nil {
		t.Fatalf("write user command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "audit.md"), []byte("---\ndescription: Project audit\n---\nproject $ARGUMENTS\n"), 0o600); err != nil {
		t.Fatalf("write project command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "help.md"), []byte("---\ndescription: Bad shadow\n---\nshadow\n"), 0o600); err != nil {
		t.Fatalf("write shadow command: %v", err)
	}

	reg := LoadSlashRegistry(work)
	cmd, ok := reg.Lookup("audit")
	if !ok {
		t.Fatalf("custom command missing")
	}
	if cmd.Source != "project" || cmd.Description != "Project audit" {
		t.Fatalf("project command did not override user command: %#v", cmd)
	}
	help, _ := reg.Lookup("help")
	if !help.Builtin {
		t.Fatalf("custom command shadowed built-in /help: %#v", help)
	}
	if parsed, args, ok := reg.Parse("/audit src pkg"); !ok || parsed != "audit" || strings.Join(args, " ") != "src pkg" {
		t.Fatalf("Parse custom = %q %v %v", parsed, args, ok)
	}

	helpRows := reg.HelpRows()
	entries := buildAutocompleteEntries(helpRows)
	foundHelp := false
	foundEntry := false
	for _, row := range helpRows {
		if row.Key == "/audit [arguments]" && row.Desc == "Project audit" {
			foundHelp = true
		}
	}
	for _, entry := range entries {
		if entry.Verb == "audit" && entry.Usage == "/audit [arguments]" && entry.Desc == "Project audit" {
			foundEntry = true
		}
	}
	if !foundHelp || !foundEntry {
		t.Fatalf("custom command missing from help/autocomplete: help=%v entry=%v", foundHelp, foundEntry)
	}
}
