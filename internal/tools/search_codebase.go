package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const searchCodebaseSchema = `{
  "type": "object",
  "properties": {
    "pattern":     { "type": "string", "description": "Regular expression to search for." },
    "file_glob":   { "type": "string", "description": "Optional glob filter (e.g. '*.go', '**/*.ts'). Passed to ripgrep --glob." },
    "max_results": { "type": "integer", "description": "Maximum matches to return (default 50, hard cap 500)." }
  },
  "required": ["pattern"]
}`

const (
	defaultSearchMax = 50
	maxSearchMax     = 500
)

type SearchCodebaseTool struct {
	Root string
	// rgPath caches the ripgrep binary lookup. Empty until first use.
	rgPath string
	rgOnce sync.Once
}

func NewSearchCodebaseTool(root string) *SearchCodebaseTool {
	return &SearchCodebaseTool{Root: root}
}

func (*SearchCodebaseTool) Name() string            { return "search_codebase" }
func (*SearchCodebaseTool) RequiresApproval() bool  { return false }
func (*SearchCodebaseTool) Schema() json.RawMessage { return json.RawMessage(searchCodebaseSchema) }
func (*SearchCodebaseTool) Description() string {
	return "Regex-search the codebase. Uses ripgrep when available (much faster) and a Go fallback otherwise. Results are formatted as path:line:match."
}

type searchParams struct {
	Pattern    string `json:"pattern"`
	FileGlob   string `json:"file_glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func (t *SearchCodebaseTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p searchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("search_codebase: parse params: %w", err)
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return ToolResult{Content: "search_codebase: pattern is empty", IsError: true}, nil
	}
	limit := p.MaxResults
	if limit <= 0 {
		limit = defaultSearchMax
	}
	if limit > maxSearchMax {
		limit = maxSearchMax
	}

	if rg := t.ripgrepPath(); rg != "" {
		out, err := t.searchWithRipgrep(ctx, rg, p.Pattern, p.FileGlob, limit)
		if err == nil {
			return out, nil
		}
		// Fall through to Go-native fallback on rg failure.
	}
	return t.searchWithGo(ctx, p.Pattern, p.FileGlob, limit)
}

func (t *SearchCodebaseTool) ripgrepPath() string {
	t.rgOnce.Do(func() {
		if path, err := exec.LookPath("rg"); err == nil {
			t.rgPath = path
		}
	})
	return t.rgPath
}

func (t *SearchCodebaseTool) searchWithRipgrep(ctx context.Context, rg, pattern, glob string, limit int) (ToolResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := []string{"--no-heading", "--with-filename", "--line-number", "--color=never", "--max-filesize", "1M"}
	for dir := range skippedDirs {
		args = append(args, "--glob", "!"+dir+"/**")
		args = append(args, "--glob", "!**/"+dir+"/**")
	}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, "--", pattern, t.Root)

	cmd := exec.CommandContext(runCtx, rg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ToolResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return ToolResult{}, err
	}

	var lines []string
	hitLimit := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= limit {
			hitLimit = true
			cancel()
			break
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if scanErr != nil && !hitLimit && ctx.Err() == nil {
		return ToolResult{}, scanErr
	}
	if waitErr != nil && !hitLimit {
		if ctx.Err() != nil {
			return ToolResult{}, ctx.Err()
		}
		// ripgrep exits 1 when no matches; treat that as an empty result,
		// not an error.
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ToolResult{Content: fmt.Sprintf("No matches for /%s/.", pattern)}, nil
		}
		return ToolResult{}, waitErr
	}
	return formatSearchLines(lines, t.Root, pattern, limit), nil
}

// formatSearchOutput converts ripgrep's path:line:text output into a
// project-relative form and applies a hard line cap.
func formatSearchOutput(out []byte, root, pattern string, limit int) ToolResult {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return formatSearchLines(lines, root, pattern, limit)
}

func formatSearchLines(lines []string, root, pattern string, limit int) ToolResult {
	rootAbs, _ := filepath.Abs(root)
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ToolResult{Content: fmt.Sprintf("No matches for /%s/.", pattern)}
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d match(es) for /%s/:\n\n", len(lines), pattern)
	for _, l := range lines {
		l = strings.ReplaceAll(l, rootAbs+string(filepath.Separator), "")
		l = strings.ReplaceAll(l, rootAbs+"/", "")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return ToolResult{Content: b.String(), Metadata: map[string]any{
		"match_count": len(lines),
		"limit":       limit,
	}}
}

// searchWithGo is the no-dependency fallback. Slower than ripgrep but
// works on systems without it. Uses skipDir to avoid common large dirs.
func (t *SearchCodebaseTool) searchWithGo(ctx context.Context, pattern, glob string, limit int) (ToolResult, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("search_codebase: invalid regex: %s", err), IsError: true}, nil
	}

	var matches []string
	rootAbs, _ := filepath.Abs(t.Root)

	walkErr := filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(rootAbs, path)
		if glob != "" {
			match, matchErr := matchSearchGlob(glob, rel, d.Name())
			if matchErr != nil || !match {
				return nil
			}
		}
		// Skip files larger than 1MB to avoid eating binaries.
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > 1024*1024 {
			return nil
		}

		data, readErr := readFileBounded(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(data, "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return ToolResult{Content: fmt.Sprintf("search_codebase: %s", walkErr), IsError: true}, nil
	}
	if len(matches) == 0 {
		return ToolResult{Content: fmt.Sprintf("No matches for /%s/.", pattern)}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d match(es) for /%s/ (Go fallback — install ripgrep for faster search):\n\n", len(matches), pattern)
	for _, m := range matches {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	return ToolResult{Content: b.String(), Metadata: map[string]any{
		"match_count": len(matches),
		"engine":      "go-fallback",
	}}, nil
}

// skippedDirs are conventional names that almost always contain bulk junk
// rather than source code. Saves the fallback walker from grinding through
// node_modules and friends.
var skippedDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"node_modules": true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"dist":         true,
	"build":        true,
	"target":       true, // Rust
	".idea":        true,
	".vscode":      true,
}

func shouldSkipDir(name string) bool { return skippedDirs[name] }

func matchSearchGlob(glob, rel, base string) (bool, error) {
	rel = filepath.ToSlash(rel)
	if ok, err := path.Match(glob, rel); err != nil || ok {
		return ok, err
	}
	if strings.HasPrefix(glob, "**/") {
		if ok, err := path.Match(strings.TrimPrefix(glob, "**/"), base); err != nil || ok {
			return ok, err
		}
	}
	return path.Match(glob, base)
}
