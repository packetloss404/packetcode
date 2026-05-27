package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const listSymbolsSchema = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Optional file or directory path relative to the project root. Defaults to the project root." },
    "query": { "type": "string", "description": "Optional case-insensitive substring filter for symbol names." },
    "file_glob": { "type": "string", "description": "Optional glob filter such as '*.go' or '**/*.ts'." },
    "max_results": { "type": "integer", "description": "Maximum symbols to return (default 100, hard cap 1000)." }
  }
}`

const findDefinitionSchema = `{
  "type": "object",
  "properties": {
    "symbol": { "type": "string", "description": "Optional symbol name to locate, such as NewServer or Server.Serve. If omitted, path and line are required." },
    "path": { "type": "string", "description": "Optional file path used with line/column to infer the symbol at a source location." },
    "line": { "type": "integer", "description": "Optional 1-indexed source line used with path." },
    "column": { "type": "integer", "description": "Optional 1-indexed source column used with path/line. If omitted, the first identifier on the line is used." },
    "character": { "type": "integer", "description": "Alias for column, for editor/LSP-style callers." },
    "file_glob": { "type": "string", "description": "Optional glob filter such as '*.go' or '**/*.ts'." },
    "max_results": { "type": "integer", "description": "Maximum definitions to return (default 50, hard cap 500)." }
  }
}`

const findReferencesSchema = `{
  "type": "object",
  "properties": {
    "symbol": { "type": "string", "description": "Optional identifier or literal symbol to search for. If omitted, path and line are required." },
    "path": { "type": "string", "description": "Optional file path used with line/column to infer the symbol at a source location." },
    "line": { "type": "integer", "description": "Optional 1-indexed source line used with path." },
    "column": { "type": "integer", "description": "Optional 1-indexed source column used with path/line. If omitted, the first identifier on the line is used." },
    "character": { "type": "integer", "description": "Alias for column, for editor/LSP-style callers." },
    "scope_path": { "type": "string", "description": "Optional file or directory path that narrows the reference scan. Defaults to the project root." },
    "include_declaration": { "type": "boolean", "description": "Whether to include likely definition lines in reference results. Default true." },
    "file_glob": { "type": "string", "description": "Optional glob filter such as '*.go' or '**/*.ts'." },
    "max_results": { "type": "integer", "description": "Maximum references to return (default 100, hard cap 1000)." }
  }
}`

const getDiagnosticsSchema = `{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Optional file or directory path relative to the project root. Defaults to the project root." },
    "file_glob": { "type": "string", "description": "Optional glob filter such as '*.go'." },
    "max_results": { "type": "integer", "description": "Maximum diagnostics to return (default 100, hard cap 1000)." }
  }
}`

const (
	defaultSymbolLimit     = 100
	maxSymbolLimit         = 1000
	defaultDefinitionLimit = 50
	maxDefinitionLimit     = 500
	defaultReferenceLimit  = 100
	maxReferenceLimit      = 1000
	defaultDiagnosticLimit = 100
	maxDiagnosticLimit     = 1000
	maxCodeIntelFileBytes  = 1024 * 1024
	maxCodeIntelSnippet    = 300
	maxCodeIntelOutput     = 64 * 1024
)

type ListSymbolsTool struct {
	Root string
}

func NewListSymbolsTool(root string) *ListSymbolsTool { return &ListSymbolsTool{Root: root} }

func (*ListSymbolsTool) Name() string            { return "list_symbols" }
func (*ListSymbolsTool) RequiresApproval() bool  { return false }
func (*ListSymbolsTool) Schema() json.RawMessage { return json.RawMessage(listSymbolsSchema) }
func (*ListSymbolsTool) Description() string {
	return "List functions, methods, classes, types, constants, and variables in a file, directory, or the whole project. Uses Go AST for Go files and safe regex fallbacks for common languages."
}

type FindDefinitionTool struct {
	Root string
}

func NewFindDefinitionTool(root string) *FindDefinitionTool {
	return &FindDefinitionTool{Root: root}
}

func (*FindDefinitionTool) Name() string            { return "find_definition" }
func (*FindDefinitionTool) RequiresApproval() bool  { return false }
func (*FindDefinitionTool) Schema() json.RawMessage { return json.RawMessage(findDefinitionSchema) }
func (*FindDefinitionTool) Description() string {
	return "Find likely definitions for a symbol across the project. Uses parsed Go symbols and language-aware fallbacks for common source files."
}

type FindReferencesTool struct {
	Root string
}

func NewFindReferencesTool(root string) *FindReferencesTool {
	return &FindReferencesTool{Root: root}
}

func (*FindReferencesTool) Name() string            { return "find_references" }
func (*FindReferencesTool) RequiresApproval() bool  { return false }
func (*FindReferencesTool) Schema() json.RawMessage { return json.RawMessage(findReferencesSchema) }
func (*FindReferencesTool) Description() string {
	return "Find references to an identifier across the project with whole-identifier matching and bounded output."
}

type GetDiagnosticsTool struct {
	Root string
}

func NewGetDiagnosticsTool(root string) *GetDiagnosticsTool {
	return &GetDiagnosticsTool{Root: root}
}

func (*GetDiagnosticsTool) Name() string            { return "get_diagnostics" }
func (*GetDiagnosticsTool) RequiresApproval() bool  { return false }
func (*GetDiagnosticsTool) Schema() json.RawMessage { return json.RawMessage(getDiagnosticsSchema) }
func (*GetDiagnosticsTool) Description() string {
	return "Return bounded syntax diagnostics for a file, directory, or workspace. Currently uses local parsers and never starts external language servers."
}

type listSymbolsParams struct {
	Path       string `json:"path,omitempty"`
	Query      string `json:"query,omitempty"`
	FileGlob   string `json:"file_glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type findDefinitionParams struct {
	Symbol     string `json:"symbol"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
	Character  int    `json:"character,omitempty"`
	FileGlob   string `json:"file_glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type findReferencesParams struct {
	Symbol             string `json:"symbol"`
	Path               string `json:"path,omitempty"`
	Line               int    `json:"line,omitempty"`
	Column             int    `json:"column,omitempty"`
	Character          int    `json:"character,omitempty"`
	ScopePath          string `json:"scope_path,omitempty"`
	IncludeDeclaration bool   `json:"include_declaration,omitempty"`
	FileGlob           string `json:"file_glob,omitempty"`
	MaxResults         int    `json:"max_results,omitempty"`
}

type getDiagnosticsParams struct {
	Path       string `json:"path,omitempty"`
	FileGlob   string `json:"file_glob,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type codeSymbol struct {
	Name   string
	Kind   string
	Path   string
	Line   int
	Column int
	Detail string
}

type referenceMatch struct {
	Path   string
	Line   int
	Column int
	Text   string
}

type codeDiagnostic struct {
	Path     string
	Line     int
	Column   int
	Severity string
	Message  string
}

type codeIntelEngines struct {
	Symbols                map[string]bool
	References             map[string]bool
	Diagnostics            map[string]bool
	CheckedDiagnostics     int
	UnsupportedDiagnostics int
}

func (t *ListSymbolsTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p listSymbolsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ToolResult{}, fmt.Errorf("list_symbols: parse params: %w", err)
		}
	}
	limit := boundedInt(p.MaxResults, defaultSymbolLimit, maxSymbolLimit)
	symbols, truncated, err := collectSymbols(ctx, t.Root, p.Path, p.FileGlob, p.Query, limit)
	if err != nil {
		return ToolResult{Content: "list_symbols: " + err.Error(), IsError: true}, nil
	}
	return renderSymbols("symbols", symbols, truncated, limit, map[string]any{
		"symbol_count": len(symbols),
		"limit":        limit,
		"truncated":    truncated,
		"engine":       "go-ast+lexical-fallback",
		"confidence":   "medium",
	})
}

func (t *FindDefinitionTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p findDefinitionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("find_definition: parse params: %w", err)
	}
	symbol, inferErr := resolveSymbolAtPosition(t.Root, p.Symbol, p.Path, p.Line, p.Column, p.Character)
	if inferErr != nil {
		return ToolResult{Content: "find_definition: " + inferErr.Error(), IsError: true}, nil
	}
	limit := boundedInt(p.MaxResults, defaultDefinitionLimit, maxDefinitionLimit)
	all, sourceTruncated, err := collectSymbols(ctx, t.Root, "", p.FileGlob, "", maxDefinitionLimit*20)
	if err != nil {
		return ToolResult{Content: "find_definition: " + err.Error(), IsError: true}, nil
	}
	matches := make([]codeSymbol, 0)
	for _, s := range all {
		if symbolMatchesDefinition(symbol, s.Name, s.Detail) {
			matches = append(matches, s)
			if len(matches) >= limit {
				break
			}
		}
	}
	truncated := len(matches) >= limit
	res, err := renderSymbols("definition candidates for "+symbol, matches, truncated, limit, map[string]any{
		"symbol":           symbol,
		"definition_count": len(matches),
		"limit":            limit,
		"truncated":        truncated || sourceTruncated,
		"engine":           "go-ast+lexical-fallback",
		"confidence":       "medium",
		"path":             p.Path,
		"line":             p.Line,
		"column":           normalizedColumn(p.Column, p.Character),
	})
	if err != nil {
		return res, err
	}
	if sourceTruncated {
		res.Content += "\nSymbol scan was truncated before the full workspace was inspected; narrow file_glob or query.\n"
	}
	return res, nil
}

func (t *FindReferencesTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p findReferencesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ToolResult{}, fmt.Errorf("find_references: parse params: %w", err)
	}
	symbol, inferErr := resolveSymbolAtPosition(t.Root, p.Symbol, p.Path, p.Line, p.Column, p.Character)
	if inferErr != nil {
		return ToolResult{Content: "find_references: " + inferErr.Error(), IsError: true}, nil
	}
	limit := boundedInt(p.MaxResults, defaultReferenceLimit, maxReferenceLimit)
	includeDeclaration := true
	if rawContainsFalse(raw, "include_declaration") {
		includeDeclaration = false
	}
	matches, truncated, err := collectReferences(ctx, t.Root, p.ScopePath, symbol, p.FileGlob, includeDeclaration, limit)
	if err != nil {
		return ToolResult{Content: "find_references: " + err.Error(), IsError: true}, nil
	}
	if len(matches) == 0 {
		meta := map[string]any{
			"symbol":              symbol,
			"reference_count":     0,
			"limit":               limit,
			"truncated":           false,
			"engine":              "lexical-fallback",
			"confidence":          "low",
			"path":                p.Path,
			"line":                p.Line,
			"column":              normalizedColumn(p.Column, p.Character),
			"scope_path":          p.ScopePath,
			"include_declaration": includeDeclaration,
		}
		return ToolResult{Content: fmt.Sprintf("No references found for %q.\n%s\n", symbol, engineNote(meta)), Metadata: meta}, nil
	}
	meta := map[string]any{
		"symbol":              symbol,
		"reference_count":     len(matches),
		"limit":               limit,
		"truncated":           truncated,
		"engine":              "lexical-fallback",
		"confidence":          "low",
		"path":                p.Path,
		"line":                p.Line,
		"column":              normalizedColumn(p.Column, p.Character),
		"scope_path":          p.ScopePath,
		"include_declaration": includeDeclaration,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d reference(s) for %q:\n\n", len(matches), symbol)
	if note := engineNote(meta); note != "" {
		fmt.Fprintf(&b, "%s\n\n", note)
	}
	outputTruncated := false
	for _, m := range matches {
		line := fmt.Sprintf("%s:%d:%d: %s\n", m.Path, m.Line, m.Column, truncateRunes(strings.TrimSpace(m.Text), maxCodeIntelSnippet))
		if b.Len()+len(line) > maxCodeIntelOutput {
			outputTruncated = true
			break
		}
		b.WriteString(line)
	}
	if truncated || outputTruncated {
		fmt.Fprintf(&b, "... output truncated at %d references; narrow file_glob or raise max_results\n", limit)
	}
	meta["truncated"] = truncated || outputTruncated
	return ToolResult{Content: b.String(), Metadata: meta}, nil
}

func (t *GetDiagnosticsTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p getDiagnosticsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ToolResult{}, fmt.Errorf("get_diagnostics: parse params: %w", err)
		}
	}
	limit := boundedInt(p.MaxResults, defaultDiagnosticLimit, maxDiagnosticLimit)
	diagnostics, engines, truncated, err := collectDiagnostics(ctx, t.Root, p.Path, p.FileGlob, limit)
	if err != nil {
		return ToolResult{Content: "get_diagnostics: " + err.Error(), IsError: true}, nil
	}
	if len(diagnostics) == 0 {
		content := "No diagnostics found."
		if len(engines.Diagnostics) == 0 {
			content += "\nNo supported diagnostics engine ran. Local diagnostics currently support Go syntax only."
		} else if engines.UnsupportedDiagnostics > 0 {
			content += fmt.Sprintf("\nSkipped %d unsupported source file(s). Local diagnostics currently support Go syntax only.", engines.UnsupportedDiagnostics)
		}
		return ToolResult{Content: content, Metadata: map[string]any{
			"diagnostic_count":  0,
			"limit":             limit,
			"truncated":         false,
			"engine":            engineList(engines.Diagnostics),
			"confidence":        diagnosticConfidence(engines.Diagnostics),
			"checked_files":     engines.CheckedDiagnostics,
			"unsupported_files": engines.UnsupportedDiagnostics,
		}}, nil
	}
	meta := map[string]any{
		"diagnostic_count":  len(diagnostics),
		"limit":             limit,
		"truncated":         truncated,
		"engine":            engineList(engines.Diagnostics),
		"confidence":        diagnosticConfidence(engines.Diagnostics),
		"checked_files":     engines.CheckedDiagnostics,
		"unsupported_files": engines.UnsupportedDiagnostics,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d diagnostic(s):\n\n", len(diagnostics))
	if note := engineNote(meta); note != "" {
		fmt.Fprintf(&b, "%s\n\n", note)
	}
	for _, d := range diagnostics {
		fmt.Fprintf(&b, "%s:%d:%d: %s: %s\n", d.Path, d.Line, d.Column, d.Severity, d.Message)
	}
	if truncated {
		fmt.Fprintf(&b, "... output truncated at %d diagnostics; narrow path/file_glob or raise max_results\n", limit)
	}
	if engines.UnsupportedDiagnostics > 0 {
		fmt.Fprintf(&b, "Skipped %d unsupported source file(s). Local diagnostics currently support Go syntax only.\n", engines.UnsupportedDiagnostics)
	}
	return ToolResult{Content: b.String(), Metadata: meta}, nil
}

func collectSymbols(ctx context.Context, root, targetPath, glob, query string, limit int) ([]codeSymbol, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, fmt.Errorf("resolve root: %w", err)
	}
	start := rootAbs
	if strings.TrimSpace(targetPath) != "" {
		start, err = resolveExistingInRoot(root, targetPath)
		if err != nil {
			return nil, false, err
		}
	}
	info, err := os.Stat(start)
	if err != nil {
		return nil, false, err
	}
	var out []codeSymbol
	truncated := false
	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d != nil && d.IsDir() {
			if path != start && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d != nil && d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(rootAbs, path)
		if relErr != nil {
			return nil
		}
		if glob != "" {
			ok, matchErr := matchSearchGlob(glob, rel, filepath.Base(path))
			if matchErr != nil || !ok {
				return nil
			}
		}
		if !isCodeIntelSource(path) {
			return nil
		}
		if fileTooLarge(path, maxCodeIntelFileBytes) {
			return nil
		}
		symbols, symErr := symbolsForFile(rootAbs, path)
		if symErr != nil {
			return nil
		}
		for _, s := range symbols {
			if query != "" && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(query)) && !strings.Contains(strings.ToLower(s.Detail), strings.ToLower(query)) {
				continue
			}
			out = append(out, s)
			if len(out) >= limit {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	}
	if !info.IsDir() {
		err := visit(start, nil, nil)
		if err == filepath.SkipAll {
			err = nil
		}
		return out, truncated, err
	}
	err = filepath.WalkDir(start, visit)
	if err == filepath.SkipAll {
		err = nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	return out, truncated, err
}

func symbolsForFile(rootAbs, path string) ([]codeSymbol, error) {
	rel := relativeSlash(rootAbs, path)
	if filepath.Ext(path) == ".go" {
		return goSymbols(rootAbs, path)
	}
	data, err := readFileBounded(path)
	if err != nil || !utf8.ValidString(data) {
		return nil, err
	}
	return fallbackSymbols(rel, data, filepath.Ext(path)), nil
}

func goSymbols(rootAbs, path string) ([]codeSymbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if file == nil {
		return nil, err
	}
	rel := relativeSlash(rootAbs, path)
	var out []codeSymbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			pos := fset.Position(d.Name.Pos())
			kind := "function"
			detail := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				recv := exprName(d.Recv.List[0].Type)
				if recv != "" {
					detail = recv + "." + d.Name.Name
				}
			}
			out = append(out, codeSymbol{Name: d.Name.Name, Kind: kind, Path: rel, Line: pos.Line, Column: pos.Column, Detail: detail})
		case *ast.GenDecl:
			kind := strings.ToLower(d.Tok.String())
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					pos := fset.Position(s.Name.Pos())
					out = append(out, codeSymbol{Name: s.Name.Name, Kind: "type", Path: rel, Line: pos.Line, Column: pos.Column, Detail: s.Name.Name})
				case *ast.ValueSpec:
					for _, name := range s.Names {
						pos := fset.Position(name.Pos())
						out = append(out, codeSymbol{Name: name.Name, Kind: kind, Path: rel, Line: pos.Line, Column: pos.Column, Detail: name.Name})
					}
				}
			}
		}
	}
	return out, nil
}

func exprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprName(e.X)
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}

func fallbackSymbols(rel, data, ext string) []codeSymbol {
	patterns := symbolPatterns(ext)
	if len(patterns) == 0 {
		return nil
	}
	var out []codeSymbol
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		for _, p := range patterns {
			m := p.re.FindStringSubmatch(line)
			if len(m) < 2 {
				continue
			}
			name := m[1]
			col := strings.Index(line, name) + 1
			if col <= 0 {
				col = 1
			}
			out = append(out, codeSymbol{Name: name, Kind: p.kind, Path: rel, Line: lineNo, Column: col, Detail: name})
			break
		}
	}
	return out
}

type symbolPattern struct {
	kind string
	re   *regexp.Regexp
}

func symbolPatterns(ext string) []symbolPattern {
	id := `([A-Za-z_$][A-Za-z0-9_$]*)`
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx":
		return []symbolPattern{
			{"function", regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+` + id + `\b`)},
			{"class", regexp.MustCompile(`^\s*(?:export\s+)?class\s+` + id + `\b`)},
			{"type", regexp.MustCompile(`^\s*(?:export\s+)?(?:interface|type|enum)\s+` + id + `\b`)},
			{"variable", regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+` + id + `\b`)},
			{"method", regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+|async\s+)*` + id + `\s*\([^)]*\)\s*(?::[^{]+)?\{?`)},
		}
	case ".py":
		return []symbolPattern{
			{"function", regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
			{"class", regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		}
	case ".rs":
		return []symbolPattern{
			{"function", regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
			{"type", regexp.MustCompile(`^\s*(?:pub\s+)?(?:struct|enum|trait|type)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
			{"const", regexp.MustCompile(`^\s*(?:pub\s+)?(?:const|static)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		}
	case ".java", ".kt", ".cs", ".c", ".h", ".cpp", ".hpp":
		return []symbolPattern{
			{"type", regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|internal\s+|static\s+|abstract\s+|final\s+)*(?:class|interface|struct|enum)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
			{"function", regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|internal\s+|static\s+|virtual\s+|override\s+|inline\s+|extern\s+)*[A-Za-z_][A-Za-z0-9_<>,\s:*&[\]]+\s+([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*\)\s*(?:\{|$)`)},
		}
	case ".rb":
		return []symbolPattern{
			{"function", regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_!?=]*)\b`)},
			{"class", regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_:]*)\b`)},
			{"module", regexp.MustCompile(`^\s*module\s+([A-Za-z_][A-Za-z0-9_:]*)\b`)},
		}
	case ".php":
		return []symbolPattern{
			{"function", regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+|static\s+)*function\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
			{"class", regexp.MustCompile(`^\s*(?:abstract\s+|final\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)\b`)},
		}
	default:
		return nil
	}
}

func collectReferences(ctx context.Context, root, targetPath, symbol, glob string, includeDeclaration bool, limit int) ([]referenceMatch, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, false, fmt.Errorf("resolve root: %w", err)
	}
	start := rootAbs
	if strings.TrimSpace(targetPath) != "" {
		start, err = resolveExistingInRoot(root, targetPath)
		if err != nil {
			return nil, false, err
		}
	}
	info, err := os.Stat(start)
	if err != nil {
		return nil, false, err
	}
	re, err := referenceRegexp(symbol)
	if err != nil {
		return nil, false, err
	}
	var out []referenceMatch
	truncated := false
	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d != nil && d.IsDir() {
			if path != start && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d != nil && d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel := relativeSlash(rootAbs, path)
		if glob != "" {
			ok, matchErr := matchSearchGlob(glob, rel, filepath.Base(path))
			if matchErr != nil || !ok {
				return nil
			}
		}
		if !isCodeIntelSource(path) {
			return nil
		}
		if fileTooLarge(path, maxCodeIntelFileBytes) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxCodeIntelFileBytes {
			return nil
		}
		data, readErr := readFileBounded(path)
		if readErr != nil || !utf8.ValidString(data) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		for i, line := range strings.Split(data, "\n") {
			locs := re.FindAllStringIndex(line, -1)
			if len(locs) == 0 {
				continue
			}
			if !includeDeclaration && lineDefinesSymbol(line, symbol, ext) {
				continue
			}
			for _, loc := range locs {
				out = append(out, referenceMatch{Path: rel, Line: i + 1, Column: loc[0] + 1, Text: line})
				if len(out) >= limit {
					truncated = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	}
	if !info.IsDir() {
		err := visit(start, nil, nil)
		if err == filepath.SkipAll {
			err = nil
		}
		return out, truncated, err
	}
	walkErr := filepath.WalkDir(start, visit)
	if walkErr == filepath.SkipAll {
		walkErr = nil
	}
	return out, truncated, walkErr
}

func collectDiagnostics(ctx context.Context, root, targetPath, glob string, limit int) ([]codeDiagnostic, codeIntelEngines, bool, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, codeIntelEngines{}, false, fmt.Errorf("resolve root: %w", err)
	}
	engines := codeIntelEngines{Diagnostics: make(map[string]bool)}
	start := rootAbs
	if strings.TrimSpace(targetPath) != "" {
		start, err = resolveExistingInRoot(root, targetPath)
		if err != nil {
			return nil, engines, false, err
		}
	}
	info, err := os.Stat(start)
	if err != nil {
		return nil, engines, false, err
	}
	var out []codeDiagnostic
	truncated := false
	visit := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d != nil && d.IsDir() {
			if path != start && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d != nil && d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel := relativeSlash(rootAbs, path)
		if !isCodeIntelSource(path) {
			if strings.TrimSpace(targetPath) != "" && rel == relativeSlash(rootAbs, start) {
				engines.UnsupportedDiagnostics++
			}
			return nil
		}
		if fileTooLarge(path, maxCodeIntelFileBytes) {
			return nil
		}
		if glob != "" {
			ok, matchErr := matchSearchGlob(glob, rel, filepath.Base(path))
			if matchErr != nil || !ok {
				return nil
			}
		}
		if strings.ToLower(filepath.Ext(path)) != ".go" {
			engines.UnsupportedDiagnostics++
			return nil
		}
		engines.CheckedDiagnostics++
		engines.Diagnostics["go-parser"] = true
		diags := diagnosticsForFile(rootAbs, path)
		for _, d := range diags {
			out = append(out, d)
			if len(out) >= limit {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	}
	if !info.IsDir() {
		err := visit(start, nil, nil)
		if err == filepath.SkipAll {
			err = nil
		}
		return out, engines, truncated, err
	}
	err = filepath.WalkDir(start, visit)
	if err == filepath.SkipAll {
		err = nil
	}
	return out, engines, truncated, err
}

func diagnosticsForFile(rootAbs, path string) []codeDiagnostic {
	if strings.ToLower(filepath.Ext(path)) != ".go" {
		return nil
	}
	if fileTooLarge(path, maxCodeIntelFileBytes) {
		return nil
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, nil, parser.AllErrors|parser.SkipObjectResolution)
	if err == nil {
		return nil
	}
	if list, ok := err.(scanner.ErrorList); ok {
		out := make([]codeDiagnostic, 0, len(list))
		for _, item := range list {
			out = append(out, codeDiagnostic{
				Path:     relativeSlash(rootAbs, path),
				Line:     item.Pos.Line,
				Column:   item.Pos.Column,
				Severity: "error",
				Message:  item.Msg,
			})
		}
		return out
	}
	pos := fset.Position(token.NoPos)
	return []codeDiagnostic{{
		Path:     relativeSlash(rootAbs, path),
		Line:     pos.Line,
		Column:   pos.Column,
		Severity: "error",
		Message:  err.Error(),
	}}
}

func fileTooLarge(path string, max int64) bool {
	info, err := os.Stat(path)
	return err != nil || info.Size() > max
}

func referenceRegexp(symbol string) (*regexp.Regexp, error) {
	quoted := regexp.QuoteMeta(symbol)
	if isIdentifier(symbol) {
		return regexp.Compile(`\b` + quoted + `\b`)
	}
	return regexp.Compile(quoted)
}

func renderSymbols(title string, symbols []codeSymbol, truncated bool, limit int, metadata map[string]any) (ToolResult, error) {
	if len(symbols) == 0 {
		content := "No " + title + " found."
		if note := engineNote(metadata); note != "" {
			content += "\n" + note
		}
		return ToolResult{Content: content, Metadata: metadata}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d %s:\n\n", len(symbols), title)
	if note := engineNote(metadata); note != "" {
		fmt.Fprintf(&b, "%s\n\n", note)
	}
	for _, s := range symbols {
		detail := s.Detail
		if detail == "" || detail == s.Name {
			detail = s.Name
		}
		fmt.Fprintf(&b, "%s:%d:%d: %s %s\n", s.Path, s.Line, s.Column, s.Kind, detail)
	}
	if truncated {
		fmt.Fprintf(&b, "... output truncated at %d results; narrow path/query/file_glob or raise max_results\n", limit)
	}
	return ToolResult{Content: b.String(), Metadata: metadata}, nil
}

func resolveSymbolAtPosition(root, symbol, path string, line, column, character int) (string, error) {
	if s := strings.TrimSpace(symbol); s != "" {
		return s, nil
	}
	path = strings.TrimSpace(path)
	if path == "" || line <= 0 {
		return "", fmt.Errorf("symbol or path and line are required")
	}
	resolved, err := resolveExistingInRoot(root, path)
	if err != nil {
		return "", err
	}
	if fileTooLarge(resolved, maxCodeIntelFileBytes) {
		return "", fmt.Errorf("%s is too large for code intelligence", path)
	}
	data, err := readFileBounded(resolved)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(data) {
		return "", fmt.Errorf("%s is not valid UTF-8", path)
	}
	lines := strings.Split(data, "\n")
	if line > len(lines) {
		return "", fmt.Errorf("line %d is outside %s (%d lines)", line, path, len(lines))
	}
	col := normalizedColumn(column, character)
	identifier := identifierAt(lines[line-1], col)
	if identifier == "" {
		return "", fmt.Errorf("no identifier found at %s:%d:%d", path, line, col)
	}
	return identifier, nil
}

func normalizedColumn(column, character int) int {
	if character > 0 {
		return character
	}
	return column
}

type identifierSpan struct {
	start int
	end   int
	text  string
}

func identifierAt(line string, column int) string {
	spans := identifierSpans(line)
	if len(spans) == 0 {
		return ""
	}
	if column <= 0 {
		return spans[0].text
	}
	pos := column - 1
	for _, span := range spans {
		if pos >= span.start && pos < span.end {
			return span.text
		}
	}
	for _, span := range spans {
		if pos < span.start {
			return span.text
		}
	}
	return spans[len(spans)-1].text
}

func identifierSpans(line string) []identifierSpan {
	runes := []rune(line)
	var spans []identifierSpan
	for i := 0; i < len(runes); {
		if !isIdentifierStartRune(runes[i]) {
			i++
			continue
		}
		start := i
		i++
		for i < len(runes) && isIdentifierPartRune(runes[i]) {
			i++
		}
		spans = append(spans, identifierSpan{start: start, end: i, text: string(runes[start:i])})
	}
	return spans
}

func isIdentifierStartRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isIdentifierPartRune(r rune) bool {
	return isIdentifierStartRune(r) || unicode.IsDigit(r)
}

func rawContainsFalse(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	v, ok := obj[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false
	}
	return !b
}

func lineDefinesSymbol(line, symbol, ext string) bool {
	name := strings.TrimPrefix(symbol, "*")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return false
	}
	quoted := regexp.QuoteMeta(name)
	switch ext {
	case ".go":
		patterns := []string{
			`^\s*func\s+` + quoted + `\s*\(`,
			`^\s*func\s*\([^)]*\)\s*` + quoted + `\s*\(`,
			`^\s*type\s+` + quoted + `\b`,
			`^\s*(?:const|var)\s+(?:\([^)]*\b` + quoted + `\b|` + quoted + `\b)`,
		}
		for _, pattern := range patterns {
			if regexp.MustCompile(pattern).MatchString(line) {
				return true
			}
		}
	default:
		patterns := []string{
			`^\s*(?:export\s+)?(?:async\s+)?function\s+` + quoted + `\b`,
			`^\s*(?:export\s+)?(?:class|interface|type|enum)\s+` + quoted + `\b`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+` + quoted + `\b`,
			`^\s*(?:pub\s+)?fn\s+` + quoted + `\b`,
			`^\s*def\s+` + quoted + `\b`,
		}
		for _, pattern := range patterns {
			if regexp.MustCompile(pattern).MatchString(line) {
				return true
			}
		}
	}
	return false
}

func engineList(engines map[string]bool) string {
	if len(engines) == 0 {
		return "none"
	}
	names := make([]string, 0, len(engines))
	for name := range engines {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

func diagnosticConfidence(engines map[string]bool) string {
	if len(engines) == 0 {
		return "none"
	}
	if engines["go-parser"] {
		return "high"
	}
	return "low"
}

func engineNote(metadata map[string]any) string {
	engine := metadataString(metadata, "engine")
	confidence := metadataString(metadata, "confidence")
	if engine == "" && confidence == "" {
		return ""
	}
	if confidence == "" {
		return "Engine: " + engine
	}
	if engine == "" {
		return "Confidence: " + confidence
	}
	return fmt.Sprintf("Engine: %s; confidence: %s", engine, confidence)
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return v
	}
	return ""
}

func symbolMatchesDefinition(query, name, detail string) bool {
	query = strings.TrimPrefix(query, "*")
	detail = strings.TrimPrefix(detail, "*")
	if query == name || query == detail {
		return true
	}
	return false
}

func isCodeIntelSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".cs", ".c", ".h", ".cpp", ".hpp", ".rb", ".php", ".swift":
		return true
	default:
		return false
	}
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func relativeSlash(rootAbs, path string) string {
	rel, err := filepath.Rel(rootAbs, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func boundedInt(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
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
