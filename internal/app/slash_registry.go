package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/packetcode/packetcode/internal/config"
)

// SlashCommand describes either a built-in UI command or a markdown-backed
// prompt command loaded from a user/project commands directory.
type SlashCommand struct {
	Name        string
	Usage       string
	Description string
	Body        string
	Source      string
	Builtin     bool
}

// SlashCommandRegistry keeps commands in display order while allowing quick
// lookup during parse/dispatch.
type SlashCommandRegistry struct {
	ordered []SlashCommand
	byName  map[string]int
}

func NewBuiltinSlashRegistry() *SlashCommandRegistry {
	r := &SlashCommandRegistry{byName: make(map[string]int)}
	for _, row := range SlashCommands {
		name := verbOf(row.Key)
		if name == "" {
			continue
		}
		r.add(SlashCommand{
			Name:        name,
			Usage:       row.Key,
			Description: row.Desc,
			Source:      "built-in",
			Builtin:     true,
		})
	}
	return r
}

func LoadSlashRegistry(workingDir string) *SlashCommandRegistry {
	r := NewBuiltinSlashRegistry()

	userDir, err := config.UserCommandsDir()
	if err == nil {
		r.loadDir(userDir, "user")
	}
	if workingDir != "" {
		r.loadDir(config.ProjectCommandsDir(workingDir), "project")
	}
	return r
}

func (r *SlashCommandRegistry) Parse(text string) (cmd string, args []string, ok bool) {
	name, args, ok := parseSlashCommandFields(text)
	if !ok {
		return "", nil, false
	}
	if _, hit := r.Lookup(name); !hit {
		return "", nil, false
	}
	return name, args, true
}

func (r *SlashCommandRegistry) Lookup(name string) (SlashCommand, bool) {
	if r == nil {
		r = NewBuiltinSlashRegistry()
	}
	idx, ok := r.byName[name]
	if !ok {
		return SlashCommand{}, false
	}
	return r.ordered[idx], true
}

func (r *SlashCommandRegistry) HelpRows() []KeyHelp {
	if r == nil {
		r = NewBuiltinSlashRegistry()
	}
	rows := make([]KeyHelp, 0, len(r.ordered))
	for _, cmd := range r.ordered {
		rows = append(rows, KeyHelp{Key: cmd.Usage, Desc: cmd.Description})
	}
	return rows
}

func (r *SlashCommandRegistry) add(cmd SlashCommand) {
	if r.byName == nil {
		r.byName = make(map[string]int)
	}
	if _, exists := r.byName[cmd.Name]; exists {
		return
	}
	r.byName[cmd.Name] = len(r.ordered)
	r.ordered = append(r.ordered, cmd)
}

func (r *SlashCommandRegistry) upsertCustom(cmd SlashCommand) {
	if r.byName == nil {
		r.byName = make(map[string]int)
	}
	if idx, exists := r.byName[cmd.Name]; exists {
		if r.ordered[idx].Builtin {
			return
		}
		r.ordered[idx] = cmd
		return
	}
	r.byName[cmd.Name] = len(r.ordered)
	r.ordered = append(r.ordered, cmd)
}

func (r *SlashCommandRegistry) loadDir(dir, source string) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(matches) == 0 {
		return
	}
	sort.Strings(matches)
	for _, path := range matches {
		cmd, err := loadMarkdownSlashCommand(path, source)
		if err != nil {
			continue
		}
		r.upsertCustom(cmd)
	}
}

func loadMarkdownSlashCommand(path, source string) (SlashCommand, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !validSlashCommandName(name) {
		return SlashCommand{}, fmt.Errorf("invalid slash command name %q", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SlashCommand{}, err
	}
	body, desc := parseMarkdownCommandFile(string(data))
	body = strings.TrimSpace(body)
	if body == "" {
		return SlashCommand{}, fmt.Errorf("empty slash command %q", name)
	}
	if desc == "" {
		desc = "Run " + source + " markdown command"
	}
	usage := "/" + name
	if strings.Contains(body, "$ARGUMENTS") {
		usage += " [arguments]"
	}
	return SlashCommand{
		Name:        name,
		Usage:       usage,
		Description: desc,
		Body:        body,
		Source:      source,
		Builtin:     false,
	}, nil
}

func parseMarkdownCommandFile(raw string) (body, description string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimPrefix(raw, "\ufeff")
	if !strings.HasPrefix(raw, "---\n") {
		return raw, ""
	}
	rest := strings.TrimPrefix(raw, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return raw, ""
	}
	fm := rest[:end]
	body = strings.TrimPrefix(rest[end:], "\n---")
	body = strings.TrimPrefix(body, "\n")
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(strings.ToLower(key)) != "description" {
			continue
		}
		description = strings.TrimSpace(val)
		description = strings.Trim(description, `"'`)
		break
	}
	return body, description
}

func (c SlashCommand) Expand(arguments string) string {
	return strings.ReplaceAll(c.Body, "$ARGUMENTS", strings.TrimSpace(arguments))
}

func validSlashCommandName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
