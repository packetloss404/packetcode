package conversation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/packetcode/packetcode/internal/tools"
)

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestToolResultBody_PatchFileRendersDiff verifies the patch_file
// result body is routed through the diff component rather than being
// dumped verbatim.
func TestToolResultBody_PatchFileRendersDiff(t *testing.T) {
	msg := Message{
		Kind:     KindToolCall,
		ToolName: "patch_file",
		ToolResult: "Applied 1 patch(es) to a.txt.\n\n" +
			"--- a.txt (current)\n" +
			"+++ a.txt (proposed)\n" +
			"@@ -1,2 +1,2 @@\n" +
			"-old\n" +
			"+new\n",
	}
	out := stripANSI(renderToolResultBody(msg, 80))
	assert.Contains(t, out, "Applied 1 patch(es) to a.txt.")
	assert.Contains(t, out, "@@ -1,2 +1,2 @@")
	assert.Contains(t, out, "- old")
	assert.Contains(t, out, "+ new")
	// Gutter present.
	assert.Contains(t, out, " | ")
}

func TestAppendToolCallDiscardsPendingAgentText(t *testing.T) {
	m := New()
	m.Resize(100, 40)

	m.AppendAgentText("model", "openai", `<|python_tag|>{"path":"main.go"}`)
	m.AppendToolCall("read_file", `{"path":"main.go"}`)
	m.CompleteToolCall("read_file", tools.ToolResult{Content: "package main"})

	out := stripANSI(m.View())
	assert.NotContains(t, out, "<|python_tag|>")
	assert.Contains(t, out, "read_file")
	assert.Contains(t, out, "package main")
}

// TestToolResultBody_PatchFileErrorFallsThrough verifies the red
// error branch wins before the diff attempt — a failed patch_file
// shouldn't try to diff an error message.
func TestToolResultBody_PatchFileErrorFallsThrough(t *testing.T) {
	msg := Message{
		Kind:       KindToolCall,
		ToolName:   "patch_file",
		ToolResult: "patch_file: patch #1 search text not found in a.txt",
		IsError:    true,
	}
	out := stripANSI(renderToolResultBody(msg, 80))
	assert.Contains(t, out, "patch #1 search text not found")
	assert.NotContains(t, out, "@@")
}

// TestToolResultBody_NonPatchFileUnchanged verifies the body is
// dumped verbatim for tools that aren't patch_file, even if the text
// contains a `@@` substring (common in shell output).
func TestToolResultBody_NonPatchFileUnchanged(t *testing.T) {
	body := "free output text @@ nothing special"
	msg := Message{
		Kind:       KindToolCall,
		ToolName:   "execute_command",
		ToolResult: body,
	}
	out := renderToolResultBody(msg, 80)
	assert.Equal(t, body, out)
}

// TestAppendToolOutput_RendersInPendingView verifies streamed command
// output shows in the live region while the tool call is still pending.
func TestAppendToolOutput_RendersInPendingView(t *testing.T) {
	m := New()
	m.Resize(100, 40)

	m.AppendToolCallWithID("execute_command", `{"command":"go test ./..."}`, "call-1")
	ok := m.AppendToolOutput("call-1", "ok  pkg/foo\n")
	assert.True(t, ok, "chunk routed to matching pending call")
	ok = m.AppendToolOutput("call-1", "ok  pkg/bar\n")
	assert.True(t, ok)

	out := stripANSI(m.PendingView())
	assert.Contains(t, out, "execute_command")
	assert.Contains(t, out, "ok  pkg/foo")
	assert.Contains(t, out, "ok  pkg/bar")
}

// TestAppendToolOutput_DiscardedOnComplete verifies the committed tool
// result is the single rendered copy — the streamed preview must not
// survive into the committed block (no duplication).
func TestAppendToolOutput_DiscardedOnComplete(t *testing.T) {
	m := New()
	m.Resize(100, 40)

	m.AppendToolCallWithID("execute_command", `{"command":"run"}`, "call-1")
	m.AppendToolOutput("call-1", "ZZRESULTZZ\n")
	m.CompleteToolCall("execute_command", tools.ToolResult{Content: "ZZRESULTZZ\n"})

	// Nothing pending after completion.
	assert.Equal(t, "", m.PendingView())

	// The committed transcript contains the result exactly once — the
	// streamed preview that carried the same text was discarded.
	out := stripANSI(m.View())
	assert.Equal(t, 1, strings.Count(out, "ZZRESULTZZ"), "result rendered once, preview discarded")
}

// TestAppendToolOutput_IgnoresMismatchedCallID verifies a chunk tagged
// for a different running call is not applied to the current pending
// block.
func TestAppendToolOutput_IgnoresMismatchedCallID(t *testing.T) {
	m := New()
	m.Resize(100, 40)

	m.AppendToolCallWithID("execute_command", `{"command":"x"}`, "call-1")
	ok := m.AppendToolOutput("call-2", "stray output\n")
	assert.False(t, ok, "chunk for a different call id is dropped")
	assert.NotContains(t, stripANSI(m.PendingView()), "stray output")
}

// TestAppendToolOutput_NoPendingCallIsNoop verifies chunks arriving with
// no pending tool call (e.g. after completion) are safely ignored.
func TestAppendToolOutput_NoPendingCallIsNoop(t *testing.T) {
	m := New()
	m.Resize(100, 40)
	assert.False(t, m.AppendToolOutput("call-1", "late chunk\n"))
}

// TestAppendToolOutput_TailBounded verifies the live preview buffer is
// tail-capped so a high-output command cannot grow the pending block
// without bound.
func TestAppendToolOutput_TailBounded(t *testing.T) {
	m := New()
	m.Resize(100, 40)
	m.AppendToolCallWithID("execute_command", `{"command":"yes"}`, "call-1")
	big := strings.Repeat("A", maxLiveOutput)
	m.AppendToolOutput("call-1", big)
	m.AppendToolOutput("call-1", "TAILMARKER")
	assert.NotNil(t, m.pending)
	assert.LessOrEqual(t, len(m.pending.LiveOutput), maxLiveOutput)
	// Most-recent output is retained (tail), oldest dropped.
	assert.Contains(t, m.pending.LiveOutput, "TAILMARKER")
}

// TestTryRenderDiff_NoMarkerReturnsFalse covers the "result has no
// diff header" fast path.
func TestTryRenderDiff_NoMarkerReturnsFalse(t *testing.T) {
	_, ok := tryRenderDiffResult("plain summary, no diff", 80)
	assert.False(t, ok)
}

// TestTryRenderDiff_RowCapTruncates verifies the 200-row cap kicks in
// on a gigantic patch_file result so the conversation doesn't hang
// lipgloss.
func TestTryRenderDiff_RowCapTruncates(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a.txt (current)\n+++ a.txt (proposed)\n@@ -1,300 +1,300 @@\n")
	for i := 0; i < 300; i++ {
		b.WriteString("+added\n")
	}
	rendered, ok := tryRenderDiffResult(b.String(), 80)
	assert.True(t, ok)
	out := stripANSI(rendered)
	assert.Contains(t, out, "omitted")
	lines := strings.Split(out, "\n")
	assert.LessOrEqual(t, len(lines), 202, "renders at most ~200 rows + header")
}
