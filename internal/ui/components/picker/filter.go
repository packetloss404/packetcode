// Package picker provides a generic filter-as-you-type list modal used
// by the provider / model pickers (Ctrl+P, Alt+M) and reusable for
// the forthcoming slash-command autocomplete and theme picker.
package picker

import "strings"

// normalize lowercases s and collapses any run of whitespace to a single
// dash. This is the canonical form used on both sides of the filter
// match so "gpt 5.5" and "gpt-5.5" compare equal.
func normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !inSpace {
				b.WriteByte('-')
				inSpace = true
			}
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// matches reports whether item satisfies the given filter string. An
// empty filter matches any item. Otherwise the filter is normalised and
// checked as a substring against the haystack built from ID, Label, Detail,
// and Status joined with spaces (which normalization turns into dashes).
func matches(item Item, filter string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	needle := normalize(filter)
	haystack := normalize(item.ID + " " + item.Label + " " + item.Detail + " " + item.Status)
	return strings.Contains(haystack, needle)
}

// Normalize is the exported alias for the picker's lowercase-and-dash
// canonicalisation. Other components (notably the slash-command
// autocomplete popup) reuse the same matching rules via this helper so
// the semantics stay in one place.
func Normalize(s string) string { return normalize(s) }

// Matches is the exported alias for the picker's substring match. Used
// by the autocomplete popup when it needs to fall back on the same
// "does the haystack contain the filter" check the picker does.
func Matches(item Item, filter string) bool { return matches(item, filter) }
