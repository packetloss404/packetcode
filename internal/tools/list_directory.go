package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const listDirectorySchema = `{
  "type": "object",
  "properties": {
    "path":      { "type": "string", "description": "Directory relative to project root. Defaults to '.'." },
    "recursive": { "type": "boolean", "description": "Recurse into subdirectories. Default false." },
    "max_depth": { "type": "integer", "description": "Maximum recursion depth (default 3, max 10)." },
    "max_entries": { "type": "integer", "description": "Maximum entries to render (default 500, hard cap 2000)." }
  }
}`

const (
	defaultListDepth = 3
	maxListDepth     = 10
	defaultListMax   = 500
	hardListMax      = 2000
)

type ListDirectoryTool struct {
	Root string
}

func NewListDirectoryTool(root string) *ListDirectoryTool {
	return &ListDirectoryTool{Root: root}
}

func (*ListDirectoryTool) Name() string            { return "list_directory" }
func (*ListDirectoryTool) RequiresApproval() bool  { return false }
func (*ListDirectoryTool) Schema() json.RawMessage { return json.RawMessage(listDirectorySchema) }
func (*ListDirectoryTool) Description() string {
	return "List a directory as a capped tree. Skips conventional ignore paths (node_modules, .git, etc.)."
}

type listParams struct {
	Path       string `json:"path,omitempty"`
	Recursive  bool   `json:"recursive,omitempty"`
	MaxDepth   int    `json:"max_depth,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
}

func (t *ListDirectoryTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p listParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ToolResult{}, fmt.Errorf("list_directory: parse params: %w", err)
		}
	}
	if p.Path == "" {
		p.Path = "."
	}
	depth := p.MaxDepth
	if depth <= 0 {
		depth = defaultListDepth
	}
	if depth > maxListDepth {
		depth = maxListDepth
	}
	if !p.Recursive {
		depth = 1
	}
	maxEntries := p.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultListMax
	}
	if maxEntries > hardListMax {
		maxEntries = hardListMax
	}

	abs, err := resolveExistingInRoot(t.Root, p.Path)
	if err != nil {
		return ToolResult{Content: err.Error(), IsError: true}, nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("list_directory: %s", err), IsError: true}, nil
	}
	if !info.IsDir() {
		return ToolResult{Content: fmt.Sprintf("list_directory: %s is not a directory", p.Path), IsError: true}, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s/\n", p.Path)
	totalEntries := 0
	truncated := false
	if err := walkTree(abs, "", depth, maxEntries, &b, &totalEntries, &truncated); err != nil {
		return ToolResult{Content: fmt.Sprintf("list_directory: %s", err), IsError: true}, nil
	}
	if truncated {
		fmt.Fprintf(&b, "... output truncated at %d entries; narrow path/depth or raise max_entries\n", maxEntries)
	}
	return ToolResult{
		Content: b.String(),
		Metadata: map[string]any{
			"path":          p.Path,
			"entries_shown": totalEntries,
			"depth":         depth,
			"truncated":     truncated,
		},
	}, nil
}

// walkTree renders one directory level into b using box-drawing chars and
// recurses up to depth-1 more levels.
func walkTree(dir, prefix string, depth, maxEntries int, b *strings.Builder, total *int, truncated *bool) error {
	if depth <= 0 || *truncated {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// Filter and sort: directories first, then files; within each, alpha.
	visible := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if shouldSkipDir(e.Name()) {
			continue
		}
		visible = append(visible, e)
	}
	sort.Slice(visible, func(i, j int) bool {
		di, dj := visible[i].IsDir(), visible[j].IsDir()
		if di != dj {
			return di
		}
		return visible[i].Name() < visible[j].Name()
	})

	for i, e := range visible {
		if *total >= maxEntries {
			*truncated = true
			return nil
		}
		*total++
		isLast := i == len(visible)-1
		branch := "├── "
		nextPrefix := prefix + "│   "
		if isLast {
			branch = "└── "
			nextPrefix = prefix + "    "
		}
		name := e.Name()
		if e.IsDir() {
			fmt.Fprintf(b, "%s%s%s/\n", prefix, branch, name)
			if err := walkTree(joinPath(dir, name), nextPrefix, depth-1, maxEntries, b, total, truncated); err != nil {
				return err
			}
		} else {
			info, err := e.Info()
			if err == nil {
				fmt.Fprintf(b, "%s%s%s (%s)\n", prefix, branch, name, formatSize(info.Size()))
			} else {
				fmt.Fprintf(b, "%s%s%s\n", prefix, branch, name)
			}
		}
	}
	return nil
}

func joinPath(a, b string) string {
	if strings.HasSuffix(a, "/") || strings.HasSuffix(a, "\\") {
		return a + b
	}
	return a + string(os.PathSeparator) + b
}

// formatSize renders a byte count as B/KB/MB. Resolution doesn't matter
// here — this is a human-facing display, not a measurement.
func formatSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
