package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSymbolsTool_GoASTAndQuery(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "main.go", `package main

const answer = 42
var globalThing = answer
type Server struct{}
func NewServer() *Server { return &Server{} }
func (s *Server) Serve() {}
`)
	tool := NewListSymbolsTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"main.go","query":"serv"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "main.go:5")
	assert.Contains(t, res.Content, "type Server")
	assert.Contains(t, res.Content, "function NewServer")
	assert.Contains(t, res.Content, "method *Server.Serve")
	assert.NotContains(t, res.Content, "answer")
	assert.Equal(t, 3, res.Metadata["symbol_count"])
}

func TestListSymbolsTool_FallbackLanguages(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/app.ts", `export interface Account {}
export class Ledger {}
export function reconcile() {}
const localValue = 1
`)
	tool := NewListSymbolsTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"src","file_glob":"**/*.ts"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "src/app.ts:1")
	assert.Contains(t, res.Content, "type Account")
	assert.Contains(t, res.Content, "class Ledger")
	assert.Contains(t, res.Content, "function reconcile")
	assert.Contains(t, res.Content, "variable localValue")
}

func TestFindDefinitionTool_FindsGoMethodAndSymbol(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "server.go", `package main

type Server struct{}
func (s *Server) Serve() {}
func NewServer() *Server { return &Server{} }
`)
	tool := NewFindDefinitionTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"Server.Serve"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "server.go:4")
	assert.Contains(t, res.Content, "method *Server.Serve")
	assert.Equal(t, "Server.Serve", res.Metadata["symbol"])
	assert.Equal(t, 1, res.Metadata["definition_count"])
}

func TestFindDefinitionTool_InfersSymbolFromPosition(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "server.go", `package main

func NewServer() {}
func main() {
    NewServer()
}
`)
	tool := NewFindDefinitionTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"server.go","line":5,"character":5}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "server.go:3")
	assert.Contains(t, res.Content, "function NewServer")
	assert.Contains(t, res.Content, "Engine: go-ast+lexical-fallback")
	assert.Equal(t, "NewServer", res.Metadata["symbol"])
	assert.Equal(t, "go-ast+lexical-fallback", res.Metadata["engine"])
}

func TestFindReferencesTool_WholeIdentifierAndTruncation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.go", `package main
func NewServer() {}
func main() {
	NewServer()
	NewServerExtra()
}
`)
	tool := NewFindReferencesTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"NewServer","max_results":1}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "a.go:2")
	assert.NotContains(t, res.Content, "NewServerExtra")
	assert.Contains(t, res.Content, "output truncated")
	assert.Equal(t, 1, res.Metadata["reference_count"])
	assert.Equal(t, true, res.Metadata["truncated"])
}

func TestFindReferencesTool_PositionAndMultipleMatchesPerLine(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.go", `package main
func NewServer() {}
func main() {
    NewServer(); NewServer()
}
`)
	tool := NewFindReferencesTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"a.go","line":4,"column":5,"include_declaration":false}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, 2, res.Metadata["reference_count"])
	assert.Equal(t, false, res.Metadata["include_declaration"])
	assert.Equal(t, "lexical-fallback", res.Metadata["engine"])
	assert.Equal(t, 2, strings.Count(res.Content, "a.go:4:"))
	assert.NotContains(t, res.Content, "a.go:2")
}

func TestGetDiagnosticsTool_GoSyntaxDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "bad.go", `package main

func broken( {
`)
	tool := NewGetDiagnosticsTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"bad.go"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "bad.go:3")
	assert.Contains(t, res.Content, "error:")
	assert.Greater(t, res.Metadata["diagnostic_count"].(int), 0)
}

func TestGetDiagnosticsTool_ReportsUnsupportedSources(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "src/app.ts", `export function reconcile() {}
`)
	tool := NewGetDiagnosticsTool(root)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"src/app.ts"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "No supported diagnostics engine ran")
	assert.Equal(t, 0, res.Metadata["diagnostic_count"])
	assert.Equal(t, 1, res.Metadata["unsupported_files"])
	assert.Equal(t, "none", res.Metadata["engine"])
	assert.Equal(t, "none", res.Metadata["confidence"])
}

func TestCodeIntelligenceTools_PathSafety(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\n"), 0o644))

	res, err := NewListSymbolsTool(root).Execute(context.Background(), json.RawMessage(`{"path":"../secret.go"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content, "outside project root")
}

func TestCodeIntelligenceTools_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation often requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\n"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	res, err := NewListSymbolsTool(root).Execute(context.Background(), json.RawMessage(`{"path":"link"}`))
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content, "outside project root")
}

func TestFindReferencesTool_SkipsSymlinkEscapeDuringWorkspaceScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation often requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\nfunc SecretSymbol() {}\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "secret.go")))

	res, err := NewFindReferencesTool(root).Execute(context.Background(), json.RawMessage(`{"symbol":"SecretSymbol"}`))
	require.NoError(t, err)
	require.False(t, res.IsError, res.Content)
	assert.NotContains(t, res.Content, "SecretSymbol")
	assert.Equal(t, 0, res.Metadata["reference_count"])
}

func TestCodeIntelligenceTools_SchemasAndApproval(t *testing.T) {
	tools := []Tool{
		NewListSymbolsTool(t.TempDir()),
		NewFindDefinitionTool(t.TempDir()),
		NewFindReferencesTool(t.TempDir()),
		NewGetDiagnosticsTool(t.TempDir()),
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			assert.False(t, tool.RequiresApproval())
			var doc map[string]any
			require.NoError(t, json.Unmarshal(tool.Schema(), &doc))
			assert.Equal(t, "object", doc["type"])
			assert.NotEmpty(t, strings.TrimSpace(tool.Description()))
		})
	}
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
