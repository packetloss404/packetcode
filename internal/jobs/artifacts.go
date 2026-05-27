package jobs

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

const (
	maxArtifactsPerJob   = 64
	maxArtifactPreview   = 4096
	maxArtifactLineWidth = 140
)

type Artifact struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	Summary    string         `json:"summary,omitempty"`
	Path       string         `json:"path,omitempty"`
	SourceTool string         `json:"source_tool,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
	SizeBytes  int            `json:"size_bytes,omitempty"`
	Preview    string         `json:"preview,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at,omitempty"`
}

func cloneArtifacts(in []Artifact) []Artifact {
	if len(in) == 0 {
		return nil
	}
	out := make([]Artifact, len(in))
	copy(out, in)
	for i := range out {
		if in[i].Metadata != nil {
			out[i].Metadata = cloneMetadata(in[i].Metadata)
		}
	}
	return out
}

func cloneMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func compactArtifactMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch strings.ToLower(k) {
		case "artifacts", "preview", "content", "output", "stdout", "stderr", "diff":
			continue
		}
		if compact, ok := compactMetadataValue(v); ok {
			out[k] = compact
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactMetadataValue(v any) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case bool, int, int64, float64, json.Number:
		return x, true
	case string:
		return truncateRunes(x, 512), true
	case []string:
		return capStringSlice(x, 50), true
	case []any:
		out := make([]any, 0, min(len(x), 50))
		for _, item := range x {
			if compact, ok := compactMetadataValue(item); ok {
				out = append(out, compact)
			}
			if len(out) >= 50 {
				break
			}
		}
		return out, len(out) > 0
	case map[string]any:
		return nil, false
	default:
		return fmt.Sprintf("%v", x), true
	}
}

func capStringSlice(in []string, max int) []string {
	if len(in) == 0 || max <= 0 {
		return nil
	}
	if len(in) > max {
		in = in[:max]
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = truncateRunes(s, 512)
	}
	return out
}

func appendToolArtifact(artifacts []Artifact, call provider.ToolCall, res tools.ToolResult, at time.Time) []Artifact {
	if len(artifacts) >= maxArtifactsPerJob {
		return artifacts
	}
	a, ok := artifactFromTool(len(artifacts)+1, call, res, at)
	if !ok {
		return artifacts
	}
	return append(artifacts, a)
}

func appendTextArtifact(artifacts []Artifact, kind, title, summary, sourceTool string, isError bool, at time.Time) []Artifact {
	if len(artifacts) >= maxArtifactsPerJob || strings.TrimSpace(summary) == "" {
		return artifacts
	}
	a := Artifact{
		ID:         artifactID(len(artifacts) + 1),
		Kind:       kind,
		Title:      truncateRunes(strings.TrimSpace(title), maxArtifactLineWidth),
		Summary:    truncateRunes(strings.TrimSpace(summary), maxArtifactPreview),
		SourceTool: sourceTool,
		IsError:    isError,
		CreatedAt:  at,
	}
	if len(summary) > maxArtifactPreview {
		a.Truncated = true
	}
	return append(artifacts, a)
}

func artifactFromTool(seq int, call provider.ToolCall, res tools.ToolResult, at time.Time) (Artifact, bool) {
	args := decodeArgs(call.Arguments)
	meta := compactArtifactMetadata(res.Metadata)
	toolName := call.Name
	a := Artifact{
		ID:         artifactID(seq),
		Kind:       "tool",
		Title:      toolName,
		SourceTool: toolName,
		IsError:    res.IsError,
		SizeBytes:  len(res.Content),
		Metadata:   meta,
		CreatedAt:  at,
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if boolValue(meta, "truncated") || len(res.Content) > maxArtifactPreview {
		a.Truncated = true
	}

	switch toolName {
	case "write_file":
		path := stringValue(meta, "path")
		if path == "" {
			path = stringValue(args, "path")
		}
		a.Kind = "file_change"
		a.Path = path
		a.Title = nonEmptyString(path, "file written")
		a.Summary = fmt.Sprintf("wrote %s", nonEmptyString(path, "file"))
		if b := intValue(meta, "bytes"); b > 0 {
			a.Summary += fmt.Sprintf(" (%d bytes", b)
			if l := intValue(meta, "lines"); l > 0 {
				a.Summary += fmt.Sprintf(", %d lines", l)
			}
			a.Summary += ")"
		}
	case "patch_file":
		path := stringValue(meta, "path")
		if path == "" {
			path = stringValue(args, "path")
		}
		a.Kind = "file_change"
		a.Path = path
		a.Title = nonEmptyString(path, "file patched")
		patchCount := intValue(meta, "patch_count")
		if patchCount <= 0 {
			patchCount = 1
		}
		a.Summary = fmt.Sprintf("applied %d patch(es) to %s", patchCount, nonEmptyString(path, "file"))
		a.Preview, a.Truncated = cappedPreview(extractDiff(res.Content), maxArtifactPreview)
	case "execute_command":
		command := stringValue(args, "command")
		exitCode := intValue(meta, "exit_code")
		if isTestCommand(command) {
			a.Kind = "test"
			a.Title = nonEmptyString(command, "test command")
		} else {
			a.Kind = "command"
			a.Title = nonEmptyString(command, "command")
		}
		a.Summary = fmt.Sprintf("%s [exit %d]", nonEmptyString(command, "command"), exitCode)
		if boolValue(meta, "timed_out") {
			a.Summary += " timed out"
			a.Truncated = true
		}
		if boolValue(meta, "canceled") {
			a.Summary += " canceled"
		}
	case "search_codebase":
		pattern := stringValue(args, "pattern")
		a.Kind = "search"
		a.Title = "search " + nonEmptyString(pattern, "(pattern)")
		a.Summary = fmt.Sprintf("%d match(es)", intValue(meta, "match_count"))
		if pattern != "" {
			a.Summary += " for " + pattern
		}
	case "read_file":
		path := stringValue(meta, "path")
		a.Kind = "file_read"
		a.Path = path
		a.Title = nonEmptyString(path, "file read")
		a.Summary = fmt.Sprintf("read lines %d-%d of %d", intValue(meta, "start_line"), intValue(meta, "end_line"), intValue(meta, "total_lines"))
	case "list_directory":
		path := stringValue(meta, "path")
		a.Kind = "directory_listing"
		a.Path = path
		a.Title = "list " + nonEmptyString(path, ".")
		a.Summary = fmt.Sprintf("%d entries shown", intValue(meta, "entries_shown"))
	case "spawn_agent":
		jobID := stringValue(meta, "job_id")
		a.Kind = "spawned_job"
		a.Title = "spawned job " + nonEmptyString(jobID, "(unknown)")
		a.Summary = "spawned background job"
		if state := stringValue(meta, "state"); state != "" {
			a.Summary += " " + state
		}
	default:
		if !res.IsError {
			return Artifact{}, false
		}
		a.Kind = "error"
		a.Title = toolName + " error"
		a.Summary, a.Truncated = cappedPreview(res.Content, maxArtifactPreview)
	}

	if a.Summary == "" {
		a.Summary, a.Truncated = cappedPreview(res.Content, maxArtifactPreview)
	}
	a.Title = truncateRunes(strings.TrimSpace(a.Title), maxArtifactLineWidth)
	a.Summary = truncateRunes(strings.TrimSpace(a.Summary), maxArtifactPreview)
	if a.Preview != "" && len([]rune(a.Preview)) >= maxArtifactPreview {
		a.Truncated = true
	}
	return a, true
}

func artifactID(seq int) string {
	return fmt.Sprintf("A%d", seq)
}

func decodeArgs(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func extractDiff(content string) string {
	idx := strings.Index(content, "\n\n")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(content[idx+2:])
}

func cappedPreview(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || max <= 0 {
		return "", false
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s, false
	}
	return string(rs[:max]), true
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max])
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

func intValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	}
	return 0
}

func boolValue(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	}
	return false
}

func isTestCommand(command string) bool {
	c := strings.ToLower(strings.TrimSpace(command))
	if c == "" {
		return false
	}
	prefixes := []string{
		"go test", "npm test", "npm run test", "pnpm test", "pnpm run test",
		"yarn test", "pytest", "python -m pytest", "cargo test", "cargo nextest",
		"bun test", "deno test",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(c, p) || strings.Contains(c, "; "+p) || strings.Contains(c, "&& "+p) {
			return true
		}
	}
	return false
}

func ArtifactDigest(artifacts []Artifact) string {
	if len(artifacts) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, a := range artifacts {
		counts[a.Kind]++
	}
	parts := []string{}
	add := func(kind, label string) {
		if n := counts[kind]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(label, n)))
		}
	}
	add("file_change", "changed file")
	add("worktree_diff", "worktree change")
	add("test", "test run")
	add("command", "command")
	add("search", "search")
	add("spawned_job", "child job")
	add("tool_rejection", "tool rejection")
	add("file_read", "file read")
	add("directory_listing", "directory listing")
	add("error", "error")
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(artifacts), plural("artifact", len(artifacts))))
	}
	return strings.Join(parts, " · ")
}

func ArtifactManifest(artifacts []Artifact, limit int) string {
	if len(artifacts) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(artifacts) {
		limit = len(artifacts)
	}
	lines := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		a := artifacts[i]
		label := a.Kind
		if label == "" {
			label = "artifact"
		}
		body := strings.TrimSpace(a.Summary)
		if body == "" {
			body = a.Title
		}
		if a.Path != "" && !strings.Contains(body, a.Path) {
			body += " (" + a.Path + ")"
		}
		if a.Truncated {
			body += " [truncated]"
		}
		lines = append(lines, fmt.Sprintf("- %s %s: %s", nonEmptyString(a.ID, artifactID(i+1)), label, truncateRunes(body, maxArtifactLineWidth)))
	}
	if len(artifacts) > limit {
		lines = append(lines, fmt.Sprintf("- ... %d more", len(artifacts)-limit))
	}
	return strings.Join(lines, "\n")
}

func plural(s string, n int) string {
	if n == 1 {
		return s
	}
	switch {
	case strings.HasSuffix(s, "y"):
		return strings.TrimSuffix(s, "y") + "ies"
	case strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"), strings.HasSuffix(s, "x"):
		return s + "es"
	case strings.HasSuffix(s, "s"):
		return s
	default:
		return s + "s"
	}
}
