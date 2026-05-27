package approval

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/packetcode/packetcode/internal/tools"
	"github.com/packetcode/packetcode/internal/ui/components/diff"
	"github.com/packetcode/packetcode/internal/ui/theme"
)

// RenderContext is the input to a BodyRenderer. The registry dispatch
// keeps the approval Model itself decoupled from tool specifics — a
// future `execute_command` renderer (shell syntax highlighting, say)
// would just drop a new entry into the map.
type RenderContext struct {
	Tool      tools.Tool
	Arguments string
	Width     int
}

// BodyRenderer returns the body portion of the approval modal. The
// header + action row are added by Model.View around this.
type BodyRenderer func(RenderContext) string

var renderers = map[string]BodyRenderer{
	"write_file":      renderWriteFile,
	"patch_file":      renderPatchFile,
	"execute_command": renderExecuteCommand,
}

// Register is the extension point for future tool-specific renderers.
// Not used today but documented so the next person doesn't have to
// learn the registry shape by archaeology.
func Register(toolName string, r BodyRenderer) { renderers[toolName] = r }

// maxApprovalDiffRows caps the height of the diff preview inside the
// approval modal. The modal has fixed chrome — more than ~40 lines
// and the buttons fall below the terminal floor.
const maxApprovalDiffRows = 40

// maxFallbackPreviewLines caps the raw-args preview rendered alongside
// an error fallback. Same logic as the diff cap, looser ceiling.
const maxFallbackPreviewLines = 40

type writeDiffPreviewer interface {
	PreviewDiff(path, content string) (string, bool, error)
}

type patchDiffPreviewer interface {
	PreviewPatchDiff(path string, patches []tools.PatchOp) (string, error)
}

func renderWriteFile(ctx RenderContext) string {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(ctx.Arguments), &params); err != nil {
		return renderDiffErrorFallback(err, ctx.Arguments)
	}
	wt, ok := ctx.Tool.(writeDiffPreviewer)
	if !ok {
		return summariseParams(ctx.Arguments)
	}

	unified, newFile, err := wt.PreviewDiff(params.Path, params.Content)
	if err != nil {
		return renderDiffErrorFallback(err, params.Content)
	}

	switch {
	case newFile:
		m := diff.NewFile(params.Path, params.Content).SetWidth(ctx.Width).SetMaxRows(maxApprovalDiffRows)
		return renderDiffWithHeader(m, params.Path+" (new file)")
	case unified == "":
		return theme.StyleDim.Render(fmt.Sprintf("%s — no changes (proposed content matches current file)", params.Path))
	default:
		m, parseErr := diff.Parse(unified)
		if parseErr != nil {
			return renderDiffErrorFallback(parseErr, params.Content)
		}
		m = m.SetWidth(ctx.Width).SetMaxRows(maxApprovalDiffRows)
		return renderDiffWithHeader(m, params.Path)
	}
}

func renderPatchFile(ctx RenderContext) string {
	var params struct {
		Path    string          `json:"path"`
		Patches []tools.PatchOp `json:"patches"`
	}
	if err := json.Unmarshal([]byte(ctx.Arguments), &params); err != nil {
		return renderDiffErrorFallback(err, ctx.Arguments)
	}
	pt, ok := ctx.Tool.(patchDiffPreviewer)
	if !ok {
		return summariseParams(ctx.Arguments)
	}

	unified, err := pt.PreviewPatchDiff(params.Path, params.Patches)
	if err != nil {
		return renderDiffErrorFallback(err, summariseParams(ctx.Arguments))
	}

	m, parseErr := diff.Parse(unified)
	if parseErr != nil {
		return renderDiffErrorFallback(parseErr, summariseParams(ctx.Arguments))
	}
	m = m.SetWidth(ctx.Width).SetMaxRows(maxApprovalDiffRows)
	return renderDiffWithHeader(m, params.Path)
}

func renderExecuteCommand(ctx RenderContext) string {
	var params struct {
		Command    string `json:"command"`
		CWD        string `json:"cwd"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.Unmarshal([]byte(ctx.Arguments), &params); err != nil {
		return summariseParams(ctx.Arguments)
	}
	timeout := params.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 600 {
		timeout = 600
	}
	cwd := strings.TrimSpace(params.CWD)
	if cwd == "" {
		cwd = "(project root)"
	}
	info := tools.DetectShellRuntime()
	lines := []string{
		theme.StylePrimary.Render("$ " + params.Command),
		theme.StyleDim.Render(fmt.Sprintf("cwd: %s · timeout: %ds · runtime: %s", cwd, timeout, info.Default)),
		theme.StyleWarning.Render("Review shell syntax and side effects before approving. On Windows, PowerShell/WSL/Git Bash commands must invoke that runtime explicitly."),
	}
	return strings.Join(lines, "\n")
}

// renderDiffWithHeader lays out header badge + stats line + blank line
// + rendered diff. The label is the tool-specific subject (path, or
// "path (new file)").
func renderDiffWithHeader(m diff.Model, label string) string {
	added, removed := m.Stats()
	stats := theme.StyleDim.Render(fmt.Sprintf("+%d \u2212%d", added, removed))
	subject := theme.StylePrimary.Render(label)
	body := m.View()
	parts := []string{subject + "  " + stats}
	if body != "" {
		parts = append(parts, "", body)
	}
	return strings.Join(parts, "\n")
}

// renderDiffErrorFallback is the "we couldn't diff, but here's the raw
// content" rescue path. The `!` prefix is styled red so it stands out;
// the preview is capped so a 10k-line write doesn't blow through the
// modal.
func renderDiffErrorFallback(err error, preview string) string {
	head := theme.StyleError.Render("! could not compute diff: " + err.Error())
	if preview == "" {
		return head
	}
	lines := strings.Split(preview, "\n")
	if len(lines) > maxFallbackPreviewLines {
		trimmed := lines[:maxFallbackPreviewLines]
		trimmed = append(trimmed, theme.StyleDim.Render(fmt.Sprintf("\u2026 %d more lines \u2026", len(lines)-maxFallbackPreviewLines)))
		return head + "\n\n" + strings.Join(trimmed, "\n")
	}
	return head + "\n\n" + preview
}
