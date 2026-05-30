package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const patchFileSchema = `{
  "type": "object",
  "properties": {
    "path":    { "type": "string", "description": "File path relative to the project root. Must already exist." },
    "patches": {
      "type": "array",
      "description": "Ordered list of search/replace operations applied in sequence.",
      "items": {
        "type": "object",
        "properties": {
          "search":  { "type": "string", "description": "Exact text to find. Must match exactly once." },
          "replace": { "type": "string", "description": "Replacement text." }
        },
        "required": ["search", "replace"]
      }
    }
  },
  "required": ["path", "patches"]
}`

type PatchFileTool struct {
	Root    string
	Backups BackupManager
}

func NewPatchFileTool(root string, backups BackupManager) *PatchFileTool {
	if backups == nil {
		backups = NoopBackupManager()
	}
	return &PatchFileTool{Root: root, Backups: backups}
}

func (*PatchFileTool) Name() string            { return "patch_file" }
func (*PatchFileTool) RequiresApproval() bool  { return true }
func (*PatchFileTool) Schema() json.RawMessage { return json.RawMessage(patchFileSchema) }
func (*PatchFileTool) Description() string {
	return "Apply one or more search/replace patches to an existing file. Each search must appear exactly once. Returns a unified diff. Requires user approval."
}

// PatchOp is a single search/replace operation. Exported so callers
// outside this package (the approval renderer) can type-check their
// decoded Patches slice. JSON tags are unchanged — the wire format is
// identical to the pre-rename unexported struct.
type PatchOp struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

type patchFileParams struct {
	Path    string    `json:"path"`
	Patches []PatchOp `json:"patches"`
}

// applyPatches is the shared core used by both Execute (which then
// writes the file) and PreviewPatchDiff (which does not). Validation
// errors are returned verbatim — callers prepend their own tool-name
// prefix when building a ToolResult.Content. Diff labels use the
// "(current)" / "(proposed)" wording so the approval modal and the
// post-apply conversation block read the same way.
//
// Matching is exact-first. A search that appears exactly once as a literal
// substring is applied UNCHANGED (the original behavior). A literal substring
// that appears 2+ times is still the "matches N times; must be unique" error.
//
// When the exact search has ZERO literal matches, a normalized line-based
// fallback runs that tolerates (a) CRLF vs LF line endings and (b) per-line
// trailing whitespace. The normalized match must still be UNIQUE; 2+ candidate
// sites is an ambiguous error and never a guess. On a normalized hit the
// ORIGINAL (un-normalized) span is spliced so the file's real line endings
// elsewhere are preserved.
//
// normalizedCount returns how many of the patches were resolved via the
// normalized fallback (0 means every patch took the exact path), so callers
// can surface which path matched.
func applyPatches(original string, patches []PatchOp, path string) (updated, unified string, normalizedCount int, err error) {
	if len(patches) == 0 {
		return "", "", 0, fmt.Errorf("at least one patch is required")
	}
	updated = original
	for i, op := range patches {
		if op.Search == "" {
			return "", "", 0, fmt.Errorf("patch #%d has empty search string", i+1)
		}

		count := strings.Count(updated, op.Search)
		if count > 1 {
			return "", "", 0, fmt.Errorf("patch #%d search text matches %d times in %s; ambiguous, must be unique", i+1, count, path)
		}
		if count == 1 && isWholeLineMatch(updated, op.Search) {
			// Primary path: exact, unique, line-aligned literal match —
			// unchanged behavior. The line-alignment guard means a search that
			// only appears as a partial fragment of a line (e.g. "beta" inside
			// "beta   \n", where the file carries trailing whitespace the
			// search omits) is NOT claimed here; it is routed to the
			// normalized fallback so the result correctly reports a normalized
			// match and any surrounding whitespace is handled deliberately.
			updated = strings.Replace(updated, op.Search, op.Replace, 1)
			continue
		}

		// Fallback path: no clean line-aligned exact match. Try a normalized,
		// line-based match tolerant of CRLF/LF and trailing whitespace per
		// line. The ORIGINAL (un-normalized) span is replaced so line endings
		// elsewhere in the file are preserved.
		start, end, ok, ferr := findNormalizedSpan(updated, op.Search)
		if ferr != nil {
			return "", "", 0, fmt.Errorf("patch #%d %s in %s", i+1, ferr, path)
		}
		if ok {
			updated = updated[:start] + op.Replace + updated[end:]
			normalizedCount++
			continue
		}

		// Neither path matched. If a literal substring exists but is not
		// line-aligned and the normalized matcher did not claim it, apply it
		// as a legacy mid-line edit so existing exact-substring behavior is
		// preserved; otherwise it is genuinely not found.
		if count == 1 {
			updated = strings.Replace(updated, op.Search, op.Replace, 1)
			continue
		}
		return "", "", 0, fmt.Errorf("patch #%d search text not found in %s", i+1, path)
	}
	unified, _ = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(original),
		B:        difflib.SplitLines(updated),
		FromFile: path + " (current)",
		ToFile:   path + " (proposed)",
		Context:  3,
	})
	return updated, unified, normalizedCount, nil
}

// isWholeLineMatch reports whether the (assumed unique) literal occurrence of
// search in content is line-aligned: it begins at the start of the file or
// immediately after a '\n', and ends at end-of-file or immediately before a
// line terminator ('\r' or '\n'). A non-line-aligned occurrence (such as a
// search that omits trailing whitespace the file actually has) is deliberately
// not treated as a clean exact match so it can be routed to the normalized
// fallback and reported as such.
func isWholeLineMatch(content, search string) bool {
	idx := strings.Index(content, search)
	if idx < 0 {
		return false
	}
	end := idx + len(search)
	atLineStart := idx == 0 || content[idx-1] == '\n'
	atLineEnd := end == len(content) || content[end] == '\n' || content[end] == '\r'
	return atLineStart && atLineEnd
}

// findNormalizedSpan locates a unique contiguous run of ORIGINAL lines in
// content whose normalized form equals the normalized search, and returns the
// byte offsets [start, end) of that original span so the caller can splice in
// the replacement while preserving the file's real line endings elsewhere.
//
// Normalization compares line-by-line after stripping each line's trailing
// carriage return and trailing whitespace (spaces and tabs). This tolerates
// CRLF vs LF and trailing-whitespace differences only — interior content must
// still match. The match must be unique; 2+ candidate sites returns an error
// so the caller can refuse to guess.
//
// Span boundaries are chosen to preserve as much of the original file as
// possible:
//   - It begins at the start of the first matched line.
//   - It ends at the normalized (trailing-whitespace-stripped) end of the LAST
//     matched line, so trailing whitespace AFTER the matched run is left in
//     place (e.g. "beta   \n" with search "beta" -> only "beta" is replaced,
//     keeping the "   "). Trailing whitespace on interior matched lines is
//     inside the span and is consumed by the replacement.
//   - The matched lines' line terminators (\r\n or \n) are EXCLUDED, so the
//     file's existing EOL bytes survive the splice — unless the search text
//     itself ended with a newline, in which case the final matched line's
//     terminator is included in the span.
func findNormalizedSpan(content, search string) (start, end int, ok bool, err error) {
	contentLines := splitLinesWithOffsets(content)
	searchLines := splitAndStripCR(search)

	// A search that ends in a newline yields a trailing empty element from
	// Split. Drop it for line matching and remember to include the final
	// matched line's terminator in the replaced span.
	searchEndsWithNewline := false
	if n := len(searchLines); n > 1 && searchLines[n-1] == "" {
		searchLines = searchLines[:n-1]
		searchEndsWithNewline = true
	}
	if len(searchLines) == 0 {
		return 0, 0, false, nil
	}

	normSearch := make([]string, len(searchLines))
	for i, l := range searchLines {
		normSearch[i] = strings.TrimRight(l, " \t")
	}

	var matches [][2]int // [startOffset, endOffset)
	window := len(normSearch)
	for i := 0; i+window <= len(contentLines); i++ {
		good := true
		for j := 0; j < window; j++ {
			if contentLines[i+j].normalized != normSearch[j] {
				good = false
				break
			}
		}
		if !good {
			continue
		}
		startOff := contentLines[i].start
		last := contentLines[i+window-1]
		endOff := last.normEnd // strip trailing ws on the last matched line
		if searchEndsWithNewline {
			endOff = last.eolEnd // include the final line's terminator
		}
		matches = append(matches, [2]int{startOff, endOff})
	}

	switch len(matches) {
	case 0:
		return 0, 0, false, nil
	case 1:
		return matches[0][0], matches[0][1], true, nil
	default:
		return 0, 0, false, fmt.Errorf("normalized search matched %d locations; ambiguous, refusing to guess", len(matches))
	}
}

// splitAndStripCR splits on \n (preserving a trailing empty element when the
// input ends in \n) and strips a trailing \r from each line so LF and CRLF
// search text compare equal.
func splitAndStripCR(s string) []string {
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\r")
	}
	return parts
}

type lineSpan struct {
	normalized string // line text with trailing \r and trailing ws stripped
	start      int    // byte offset where the line begins
	normEnd    int    // byte offset after the normalized text (before trailing ws / EOL)
	eolEnd     int    // byte offset after the line's terminator (or EOF)
}

// splitLinesWithOffsets splits content into lines on \n, recording byte
// offsets so the ORIGINAL span (with its real CRLF/LF endings) can be spliced
// precisely. It handles CRLF, LF, and a missing final newline. Content that
// ends in a newline does not yield a trailing synthetic empty line.
func splitLinesWithOffsets(content string) []lineSpan {
	var lines []lineSpan
	i := 0
	n := len(content)
	for {
		start := i
		nl := strings.IndexByte(content[i:], '\n')

		// Determine the end of this line's raw content (before any EOL).
		var rawEnd, eolEnd int
		if nl < 0 {
			rawEnd = n // final line, no trailing newline
			eolEnd = n
		} else {
			eolStart := i + nl // index of '\n'
			rawEnd = eolStart
			if rawEnd > start && content[rawEnd-1] == '\r' {
				rawEnd-- // exclude the \r from the raw line content
			}
			eolEnd = eolStart + 1
		}

		text := content[start:rawEnd]
		normalized := strings.TrimRight(text, " \t")
		lines = append(lines, lineSpan{
			normalized: normalized,
			start:      start,
			normEnd:    start + len(normalized),
			eolEnd:     eolEnd,
		})

		if nl < 0 {
			break
		}
		i = eolEnd
		if i >= n {
			// Content ended exactly on a newline: no synthetic empty line.
			break
		}
	}
	return lines
}

func (t *PatchFileTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p patchFileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("patch_file: parse params: %w", err)
	}
	if len(p.Patches) == 0 {
		return ToolResult{Content: "patch_file: at least one patch is required", IsError: true}, nil
	}

	abs, err := resolveExistingInRoot(t.Root, p.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}
	original, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("patch_file: %s", err), IsError: true}, nil
	}
	if !utf8.Valid(original) {
		return ToolResult{Content: "patch_file: refusing to patch binary or non-UTF-8 file", IsError: true}, nil
	}

	updated, diff, normalizedCount, err := applyPatches(string(original), p.Patches, p.Path)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("patch_file: %s", err), IsError: true}, nil
	}

	if err := t.Backups.Backup(abs); err != nil {
		return ToolResult{Content: fmt.Sprintf("patch_file: backup failed: %s", err), IsError: true}, nil
	}
	if err := atomicWrite(abs, []byte(updated)); err != nil {
		rollbackBackup(t.Backups, abs)
		return ToolResult{Content: fmt.Sprintf("patch_file: %s", err), IsError: true}, nil
	}

	// Surface which match path was taken so behavior stays auditable.
	matchNote := "all exact matches"
	if normalizedCount == len(p.Patches) {
		matchNote = "normalized match (whitespace/line-ending tolerant)"
		if len(p.Patches) > 1 {
			matchNote = "all normalized matches (whitespace/line-ending tolerant)"
		}
	} else if normalizedCount > 0 {
		matchNote = fmt.Sprintf("%d exact, %d normalized (whitespace/line-ending tolerant) matches",
			len(p.Patches)-normalizedCount, normalizedCount)
	}

	return ToolResult{
		Content: fmt.Sprintf("Applied %d patch(es) to %s (%s).\n\n%s", len(p.Patches), p.Path, matchNote, diff),
		Metadata: map[string]any{
			"path":               p.Path,
			"patch_count":        len(p.Patches),
			"original_size":      len(original),
			"updated_size":       len(updated),
			"normalized_matches": normalizedCount,
		},
	}, nil
}

// PreviewPatchDiff computes the unified diff a Execute call would
// produce, without writing the file. Used by the approval renderer so
// the user sees a real diff in the confirmation modal. Validation
// errors bubble out as-is so the caller can distinguish "bad patches"
// from "diff ready".
func (t *PatchFileTool) PreviewPatchDiff(path string, patches []PatchOp) (string, error) {
	abs, err := resolveExistingInRoot(t.Root, path)
	if err != nil {
		return "", err
	}
	original, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(original) {
		return "", fmt.Errorf("refusing to patch binary or non-UTF-8 file")
	}
	_, unified, _, err := applyPatches(string(original), patches, path)
	if err != nil {
		return "", err
	}
	return unified, nil
}
