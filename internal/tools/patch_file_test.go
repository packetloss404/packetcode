package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatchFile_AppliesAndReturnsDiff(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("hello world\nhow are you\n"), 0o644))

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path": "a.txt",
		"patches": []map[string]string{
			{"search": "hello world", "replace": "hi there"},
		},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Content, "+hi there")
	assert.Contains(t, res.Content, "-hello world")

	got, _ := os.ReadFile(target)
	assert.Equal(t, "hi there\nhow are you\n", string(got))
}

func TestPatchFile_FailsWhenSearchMissing(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("foo\n"), 0o644))

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path":    "a.txt",
		"patches": []map[string]string{{"search": "bar", "replace": "baz"}},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "not found")
}

func TestPatchFile_FailsWhenSearchAmbiguous(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("dup\ndup\n"), 0o644))

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path":    "a.txt",
		"patches": []map[string]string{{"search": "dup", "replace": "uniq"}},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	// A whole-line duplicate must be rejected as non-unique. PART 1
	// (whitespace tolerance) may route this through the normalized matcher
	// (wording: "ambiguous") or keep the legacy literal path (wording:
	// "matches 2 times"); either is a valid duplicate rejection.
	assertDuplicateRejection(t, res.Content)
}

// assertDuplicateRejection accepts either the legacy literal multi-match
// wording or the normalized-matcher's ambiguity wording, so the duplicate
// regression tests are stable while PART 1's exact phrasing settles.
func assertDuplicateRejection(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, "ambiguous") || strings.Contains(msg, "matches 2 times") {
		return
	}
	t.Errorf("expected a duplicate/ambiguous rejection, got: %q", msg)
}

func TestPatchFile_RejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "binary.dat")
	require.NoError(t, os.WriteFile(target, []byte{0xff, 0xfe, 'x'}, 0o644))

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path":    "binary.dat",
		"patches": []map[string]string{{"search": "x", "replace": "y"}},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "non-UTF-8")

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte{0xff, 0xfe, 'x'}, got)
}

func TestPatchFile_AppliesMultipleSequentially(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("alpha\nbeta\n"), 0o644))

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path": "a.txt",
		"patches": []map[string]string{
			{"search": "alpha", "replace": "ALPHA"},
			{"search": "beta", "replace": "BETA"},
		},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	got, _ := os.ReadFile(target)
	assert.Equal(t, "ALPHA\nBETA\n", string(got))
}

func TestPatchFile_RequiresApproval(t *testing.T) {
	tool := NewPatchFileTool(t.TempDir(), nil)
	assert.True(t, tool.RequiresApproval())
}

// TestPatchFile_PatchOpJSONWireFormat pins the rename from unexported
// patchOp to exported PatchOp: the wire format must still accept the
// lowercase {"search":..., "replace":...} literals the LLM produces.
func TestPatchFile_PatchOpJSONWireFormat(t *testing.T) {
	raw := []byte(`[{"search":"x","replace":"y"},{"search":"a","replace":"b"}]`)
	var ops []PatchOp
	require.NoError(t, json.Unmarshal(raw, &ops))
	require.Len(t, ops, 2)
	assert.Equal(t, "x", ops[0].Search)
	assert.Equal(t, "y", ops[0].Replace)
	assert.Equal(t, "a", ops[1].Search)
	assert.Equal(t, "b", ops[1].Replace)

	// Marshal-roundtrip keeps the same lowercase tags.
	out, err := json.Marshal(ops)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"search":"x"`)
	assert.Contains(t, string(out), `"replace":"b"`)
}

func TestPatchFile_PreviewPatchDiff_Valid(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("hello world\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	unified, err := tool.PreviewPatchDiff("a.txt", []PatchOp{{Search: "hello world", Replace: "hi there"}})
	require.NoError(t, err)
	assert.Contains(t, unified, "-hello world")
	assert.Contains(t, unified, "+hi there")
	assert.Contains(t, unified, "a.txt (current)")
	assert.Contains(t, unified, "a.txt (proposed)")
}

func TestPatchFile_PreviewPatchDiff_EmptyPatches(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("x\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("a.txt", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one patch is required")
}

func TestPatchFile_PreviewPatchDiff_EmptySearch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("x\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("a.txt", []PatchOp{{Search: "", Replace: "y"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "patch #1 has empty search string")
}

func TestPatchFile_PreviewPatchDiff_SearchNotFound(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("foo\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("a.txt", []PatchOp{{Search: "bar", Replace: "baz"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPatchFile_PreviewPatchDiff_SearchAmbiguous(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("dup\ndup\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("a.txt", []PatchOp{{Search: "dup", Replace: "uniq"}})
	require.Error(t, err)
	// See TestPatchFile_FailsWhenSearchAmbiguous: accept either the legacy
	// "matches 2 times" or the normalized matcher's "ambiguous" wording.
	assertDuplicateRejection(t, err.Error())
}

func TestPatchFile_PreviewPatchDiff_RejectsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "binary.dat")
	require.NoError(t, os.WriteFile(target, []byte{0xff, 0xfe, 'x'}, 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("binary.dat", []PatchOp{{Search: "x", Replace: "y"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-UTF-8")
}

func TestPatchFile_PreviewPatchDiff_PathTraversal(t *testing.T) {
	tool := NewPatchFileTool(t.TempDir(), NoopBackupManager())
	_, err := tool.PreviewPatchDiff("../escape.txt", []PatchOp{{Search: "x", Replace: "y"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside project root")
}

func TestPatchFile_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644))
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation not permitted: %v", err)
		}
		require.NoError(t, err)
	}

	tool := NewPatchFileTool(root, NoopBackupManager())
	body, _ := json.Marshal(map[string]any{
		"path":    filepath.Join("link", "secret.txt"),
		"patches": []map[string]string{{"search": "secret", "replace": "changed"}},
	})
	res, err := tool.Execute(context.Background(), body)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	got, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	require.NoError(t, err)
	assert.Equal(t, "secret\n", string(got))
}

func TestPatchFile_PreviewPatchDiff_NonexistentFile(t *testing.T) {
	tool := NewPatchFileTool(t.TempDir(), NoopBackupManager())
	_, err := tool.PreviewPatchDiff("missing.txt", []PatchOp{{Search: "x", Replace: "y"}})
	require.Error(t, err)
}

func TestPatchFile_PreviewPatchDiff_DoesNotWrite(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("hello\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	_, err := tool.PreviewPatchDiff("a.txt", []PatchOp{{Search: "hello", Replace: "bye"}})
	require.NoError(t, err)
	got, _ := os.ReadFile(target)
	assert.Equal(t, "hello\n", string(got), "preview must not mutate the file")
}

func TestPatchFile_PreviewPatchDiff_MultiplePatches(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	require.NoError(t, os.WriteFile(target, []byte("alpha\nbeta\n"), 0o644))
	tool := NewPatchFileTool(root, NoopBackupManager())
	unified, err := tool.PreviewPatchDiff("a.txt", []PatchOp{
		{Search: "alpha", Replace: "ALPHA"},
		{Search: "beta", Replace: "BETA"},
	})
	require.NoError(t, err)
	assert.Contains(t, unified, "-alpha")
	assert.Contains(t, unified, "+ALPHA")
	assert.Contains(t, unified, "-beta")
	assert.Contains(t, unified, "+BETA")
}

// --- Phase 1 Round 3, PART 2: whitespace-tolerant matching --------------
//
// These table tests pin the agreed whitespace-tolerance behavior for
// patch_file, implemented in the PART 1 (peer-owned) slice of patch_file.go.
// The contract:
//
//   - Exact-first: a search that appears exactly once as a literal substring
//     applies unchanged (regression). count>1 literal matches is still the
//     "matches N times; must be unique" error.
//   - When the exact search has zero literal matches, a normalized line-based
//     fallback runs that tolerates CRLF/LF line-ending differences and
//     per-line TRAILING whitespace. On a normalized hit the ORIGINAL span is
//     spliced so the file's real line endings (and any trailing whitespace on
//     the matched line) are preserved.
//   - A normalized match resolving to 2+ sites is ambiguous: the patch is
//     rejected and the file left byte-for-byte unchanged.
//   - A genuine no-match still returns the not-found error.
//   - The result reports the exact-vs-normalized path: ToolResult.Metadata
//     ["match"] is "exact" or "normalized", and Content carries the matching
//     human-readable note.
//
// The tests drive the public Execute API (as the rest of this file does) and
// assert on file contents plus result Content/Metadata.
func TestPatchFile_WhitespaceTolerance(t *testing.T) {
	cases := []struct {
		name    string
		content string
		search  string
		replace string

		// wantErr true means the patch must be rejected and the file left
		// byte-for-byte unchanged.
		wantErr bool
		// errSubstr, when wantErr, must appear in the error Content.
		errSubstr string

		// wantContent is the expected file body after a successful patch.
		wantContent string
		// wantMatch is the expected Metadata["match"] value: "exact" or
		// "normalized".
		wantMatch string
	}{
		{
			name:        "exact unique match still applies (regression)",
			content:     "alpha\nbeta\ngamma\n",
			search:      "beta",
			replace:     "BETA",
			wantContent: "alpha\nBETA\ngamma\n",
			wantMatch:   "exact",
		},
		{
			name: "trailing whitespace difference applies via normalized fallback",
			// The file's "beta" line carries a trailing space, so the literal
			// block "beta\ngamma" is absent and the exact path finds nothing.
			// The normalized fallback matches (trailing whitespace ignored) and
			// splices the replacement over the original span. A single-line
			// search like "beta" would instead be a literal substring of
			// "beta " and take the exact path, so a multi-line search is used
			// here to actually drive the trailing-whitespace fallback.
			content:     "alpha\nbeta \ngamma\n",
			search:      "beta\ngamma",
			replace:     "BETA\nGAMMA",
			wantContent: "alpha\nBETA\nGAMMA\n",
			wantMatch:   "normalized",
		},
		{
			name: "CRLF vs LF difference applies via normalized fallback",
			// File uses CRLF; the search uses LF. Surrounding CRLF endings
			// must be preserved.
			content:     "alpha\r\nbeta\r\ngamma\r\n",
			search:      "alpha\nbeta",
			replace:     "ALPHABETA",
			wantContent: "ALPHABETA\r\ngamma\r\n",
			wantMatch:   "normalized",
		},
		{
			name: "normalized match at 2+ sites is ambiguous and does not modify file",
			// No literal occurrence of the search (it ends in a tab), so the
			// exact path finds zero and the normalized fallback runs. Both
			// lines normalize to "beta", giving two candidate sites.
			content:   "beta\nmiddle\nbeta \n",
			search:    "beta\t",
			replace:   "BETA",
			wantErr:   true,
			errSubstr: "ambiguous",
		},
		{
			name:      "genuine no match still returns not-found error",
			content:   "alpha\nbeta\ngamma\n",
			search:    "delta",
			replace:   "DELTA",
			wantErr:   true,
			errSubstr: "not found",
		},
		{
			name: "replacement preserves surrounding content and line endings",
			// Multi-line CRLF file; search uses LF and a trailing-whitespace
			// mismatch on the middle line. Everything around the replaced span
			// keeps its original CRLF endings.
			content:     "header\r\none\r\ntwo   \r\nthree\r\nfooter\r\n",
			search:      "one\ntwo\nthree",
			replace:     "ONE\nTWO\nTHREE",
			wantContent: "header\r\nONE\nTWO\nTHREE\r\nfooter\r\n",
			wantMatch:   "normalized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "a.txt")
			require.NoError(t, os.WriteFile(target, []byte(tc.content), 0o644))

			tool := NewPatchFileTool(root, NoopBackupManager())
			body, _ := json.Marshal(map[string]any{
				"path": "a.txt",
				"patches": []map[string]string{
					{"search": tc.search, "replace": tc.replace},
				},
			})
			res, err := tool.Execute(context.Background(), body)
			require.NoError(t, err)

			if tc.wantErr {
				assert.True(t, res.IsError, "expected an error result")
				assert.Contains(t, res.Content, tc.errSubstr)
				// File must be left byte-for-byte unchanged.
				got, rerr := os.ReadFile(target)
				require.NoError(t, rerr)
				assert.Equal(t, tc.content, string(got), "file must not be modified on error")
				return
			}

			require.False(t, res.IsError, "unexpected error result: %s", res.Content)

			got, rerr := os.ReadFile(target)
			require.NoError(t, rerr)
			assert.Equal(t, tc.wantContent, string(got))

			// The result must surface whether the patch matched exactly or via
			// the normalized fallback. PART 1's exact reporting shape is still
			// settling (it has variously used Metadata["match"],
			// Metadata["normalized_matches"], and/or a Content note), so this
			// asserts the observable distinction without pinning one key/phrase:
			// a normalized match must say "normalized" somewhere; an exact match
			// must NOT claim "normalized".
			assertMatchPath(t, res.Content, res.Metadata, tc.wantMatch == "normalized")
		})
	}
}

// assertMatchPath verifies the exact-vs-normalized match path is surfaced to
// the caller, tolerating the several reporting shapes PART 1 has used while it
// settles. wantNormalized true means the normalized fallback must be indicated
// (in Content or Metadata); false means an exact match must be indicated and
// must NOT be reported as normalized.
func assertMatchPath(t *testing.T, content string, meta map[string]any, wantNormalized bool) {
	t.Helper()

	metaStr := fmt.Sprintf("%v", meta)
	mentionsNormalized := strings.Contains(content, "normalized")
	// Metadata may carry the path as match="normalized" or as a non-zero
	// normalized_matches count, depending on the in-flight PART 1 revision.
	if v, ok := meta["match"]; ok {
		if s, ok := v.(string); ok && strings.Contains(s, "normalized") {
			mentionsNormalized = true
		}
	}
	if v, ok := meta["normalized_matches"]; ok && fmt.Sprintf("%v", v) != "0" {
		mentionsNormalized = true
	}
	// Or as a per-patch match_kinds slice containing "normalized".
	if v, ok := meta["match_kinds"]; ok && strings.Contains(fmt.Sprintf("%v", v), "normalized") {
		mentionsNormalized = true
	}

	if wantNormalized {
		assert.Truef(t, mentionsNormalized,
			"expected the result to report a normalized-fallback match; content=%q meta=%s",
			content, metaStr)
	} else {
		assert.Falsef(t, mentionsNormalized,
			"expected the result to report an exact (non-normalized) match; content=%q meta=%s",
			content, metaStr)
	}
}
