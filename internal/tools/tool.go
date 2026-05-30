// Package tools defines the agent's executable tool set: read_file,
// write_file, patch_file, execute_command, search_codebase, list_directory.
//
// Each tool is a self-contained Tool implementation. The agent loop wires
// the registry into the LLM provider as a list of ToolDefinition values
// and dispatches incoming tool calls back to Execute().
//
// Approval semantics: any Tool with RequiresApproval()==true must be gated
// by the TUI's approval prompt before Execute is called. The Tool itself
// does not enforce this — the caller does.
package tools

import (
	"context"
	"encoding/json"
)

// Tool is the contract implemented by every agent-callable action.
type Tool interface {
	Name() string
	Description() string
	// Schema returns the JSON Schema document describing the tool's
	// parameters. The agent loop forwards this to LLM providers as the
	// tool's parameter definition.
	Schema() json.RawMessage
	// Execute runs the tool. params is the JSON arguments object emitted
	// by the LLM. The returned ToolResult is sent back to the LLM as a
	// tool-role message.
	Execute(ctx context.Context, params json.RawMessage) (ToolResult, error)
	// RequiresApproval reports whether this tool's invocations must be
	// confirmed by the user before Execute is called. Read-only tools
	// (read_file, search_codebase, list_directory) return false; anything
	// that mutates the filesystem or shells out returns true.
	RequiresApproval() bool
}

// ToolResult is what gets serialized back to the LLM. Content should be a
// human-readable string the model can reason about; IsError flags model
// errors (file not found, command failed) so the LLM can adjust its plan.
type ToolResult struct {
	Content  string
	IsError  bool
	Metadata map[string]any
}

// OutputSink receives incremental output chunks while a StreamingTool runs.
// It is purely for live UI display: chunks are NOT the model-facing result and
// do NOT participate in the bounded-buffer cap that produces ToolResult.Content.
//
// WriteChunk may be called many times from a background goroutine and must be
// safe for concurrent use with the tool's own work. Implementations should
// return promptly (e.g. a non-blocking channel send); a slow sink must never
// stall the underlying process's output draining.
type OutputSink interface {
	WriteChunk(chunk string)
}

// StreamingTool is an optional interface a Tool may implement to surface partial
// output as it runs. The agent loop calls ExecuteStreaming (passing a sink that
// forwards chunks to the TUI) when a tool implements this interface; tools that
// do not implement it are driven through the plain Execute path unchanged.
//
// ExecuteStreaming must still return the same bounded final ToolResult that
// Execute would: the sink is an additive live feed, not a replacement for the
// result. A nil sink must behave exactly like Execute.
type StreamingTool interface {
	Tool
	ExecuteStreaming(ctx context.Context, params json.RawMessage, sink OutputSink) (ToolResult, error)
}
