package app

import (
	"strings"

	"github.com/packetcode/packetcode/internal/ui/components/autocomplete"
)

// buildAutocompleteEntries derives the autocomplete popup's entry list
// from slash-command help rows. Each row's Key is parsed into a verb (the
// first whitespace-delimited token with a leading "/" stripped). Verbs that
// appear more than once collapse to a single entry, keeping the FIRST
// occurrence's Usage + Desc so the popup shows the canonical form.
func buildAutocompleteEntries(rows ...[]KeyHelp) []autocomplete.Entry {
	source := SlashCommands
	if len(rows) > 0 {
		source = rows[0]
	}
	seen := make(map[string]struct{}, len(source))
	entries := make([]autocomplete.Entry, 0, len(source))
	for _, row := range source {
		verb := verbOf(row.Key)
		if verb == "" {
			continue
		}
		if _, dup := seen[verb]; dup {
			continue
		}
		seen[verb] = struct{}{}
		entries = append(entries, autocomplete.Entry{
			Verb:  verb,
			Usage: row.Key,
			Desc:  row.Desc,
		})
	}
	return entries
}

// verbOf extracts the verb (first whitespace-delimited token, leading
// slash stripped) from a SlashCommands.Key string like "/spawn <prompt>".
func verbOf(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	// First whitespace-delimited token.
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	tok := strings.TrimPrefix(fields[0], "/")
	return tok
}
