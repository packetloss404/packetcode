package approval

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/packetcode/packetcode/internal/computers"
	"github.com/packetcode/packetcode/internal/provider"
	"github.com/packetcode/packetcode/internal/tools"
)

func TestApprovalExplainsRememberedScope(t *testing.T) {
	for _, name := range []string{"execute_command", "write_file", "example__publish"} {
		t.Run(name, func(t *testing.T) {
			out := showFor(t, fakeTool{name: name}, `{"command":"git status"}`)
			assert.Contains(t, out, "Allow once")
			assert.Contains(t, out, "this session")
			assert.Contains(t, out, "/permissions reset revokes all session rules")
			if name == "execute_command" {
				assert.Contains(t, out, "Allow exact command this session")
				assert.Contains(t, out, "any working directory")
				assert.NotContains(t, out, "Bash command")
			} else {
				assert.Contains(t, out, "Allow this tool this session")
				assert.Contains(t, out, "all arguments and paths for "+name)
			}
		})
	}
}

func TestApprovalFitsNarrowWidths(t *testing.T) {
	for _, width := range []int{12, 20, 40, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := New()
			m.SetWidth(width)
			m.Show(fakeTool{name: "execute_command"}, provider.ToolCall{
				Name: "[job:abcdefgh] execute_command", Arguments: `{"command":"echo a_very_long_unbroken_argument_to_review"}`,
			})
			m.SetQueueDepth(3)
			out := m.View()
			for _, line := range strings.Split(out, "\n") {
				assert.LessOrEqual(t, ansi.StringWidth(line), width, "line exceeds available terminal columns: %q", line)
			}
			// Wrapping must not truncate the action or the end of the command.
			compact := strings.Join(strings.Fields(ansi.Strip(out)), "")
			assert.Contains(t, compact, "exactcommandthissession")
			assert.Contains(t, compact, "a_very_long_unbroken_argument_to_review")
			assert.Contains(t, compact, "/permissionsreset")
		})
	}
}

func TestApprovalEscapesDecodedControlsWithoutChangingRequest(t *testing.T) {
	args, err := json.Marshal(map[string]string{
		"command": "echo before\x1b[2Kafter\rhidden",
		"cwd":     "sub\x1b]52;c;clipboard\a",
	})
	require.NoError(t, err)
	m := New()
	m.SetWidth(160)
	m.Show(fakeTool{name: "execute_command"}, provider.ToolCall{Name: "execute_command", Arguments: string(args)})
	out := m.View()
	assert.NotContains(t, out, "\x1b[2K")
	assert.NotContains(t, out, "\x1b]52;")
	assert.Contains(t, out, `before\x1b[2Kafter\rhidden`)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	require.NotNil(t, cmd)
	result := cmd().(ResultMsg)
	assert.Equal(t, string(args), result.ToolCall.Arguments)
	assert.Equal(t, Approved, result.Result)
	assert.False(t, result.Remember)
}

type recordedPreviewTool struct {
	fakeTool
	path, content string
}

func (p *recordedPreviewTool) PreviewDiff(path, content string) (string, bool, error) {
	p.path, p.content = path, content
	return "", true, nil
}

func TestApprovalDiffPreservesPreviewInputsAndEscapesDisplay(t *testing.T) {
	recorded := &recordedPreviewTool{fakeTool: fakeTool{name: "write_file"}}
	path, content := "file\x1b[2K.go", "visible\x1b]52;c;clipboard\a\r\n"
	args, err := json.Marshal(map[string]string{"path": path, "content": content})
	require.NoError(t, err)
	out := showFor(t, recorded, string(args))
	assert.Equal(t, path, recorded.path)
	assert.Equal(t, content, recorded.content)
	assert.Contains(t, out, `file\x1b[2K.go`)
	assert.Contains(t, out, `visible\x1b]52;c;clipboard\a`)
}

type remoteApprovalBackend struct{ computers.RuntimeBackend }

func (remoteApprovalBackend) Root() string         { return "/workspace" }
func (remoteApprovalBackend) Kind() computers.Kind { return computers.KindSSH }

func TestApprovalShowsRemoteShellInsteadOfLocalRuntime(t *testing.T) {
	tool := tools.NewExecuteCommandToolWithBackend(remoteApprovalBackend{})
	out := showFor(t, tool, `{"command":"ls","timeout_sec":5}`)
	assert.Contains(t, out, "remote POSIX login shell")
	assert.NotContains(t, out, "cmd /C")
}

func TestApprovalExistingShortcutsKeepTheirMeaning(t *testing.T) {
	for _, tc := range []struct {
		key      string
		result   Result
		remember bool
	}{{"1", Approved, false}, {"y", Approved, false}, {"2", Approved, true}, {"a", Approved, true}, {"3", Rejected, false}, {"n", Rejected, false}} {
		t.Run(tc.key, func(t *testing.T) {
			m := New()
			m.Show(fakeTool{name: "execute_command"}, provider.ToolCall{Name: "execute_command"})
			m.SetRequestID(7)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
			require.NotNil(t, cmd)
			result := cmd().(ResultMsg)
			assert.Equal(t, tc.result, result.Result)
			assert.Equal(t, tc.remember, result.Remember)
			assert.Equal(t, uint64(7), result.RequestID)
		})
	}
}
