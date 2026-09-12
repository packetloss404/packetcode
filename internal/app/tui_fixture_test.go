package app

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTUIFixtureStatesRenderProductionComponents(t *testing.T) {
	wants := map[string]string{
		"user-assistant": "context accounting",
		"thinking":       "Thinking",
		"streaming":      "failure occurs",
		"tool-running":   "Bash(go test",
		"tool-result":    "Read 115 lines",
		"approval":       "Allow exact command this session",
		"error":          "timed out",
		"cancelled":      "turn cancelled",
		"queued":         "(queued)",
		"compacting":     "Compacting",
		"compacted":      "218000 -> 62400",
		"normal":         "manual mode on",
		"accept-edits":   "accept edits on",
		"auto":           "auto mode on",
		"plan":           "plan mode on",
		"bypass":         "bypass permissions on",
		"agents":         "packetcode agents",
		"workflows":      "packetcode workflows",
	}
	if len(wants) != len(TUIFixtureStates) {
		t.Fatalf("fixture test covers %d states, registry has %d", len(wants), len(TUIFixtureStates))
	}
	for _, size := range []struct{ width, height int }{{72, 24}, {80, 24}, {100, 30}, {120, 40}} {
		for _, state := range TUIFixtureStates {
			name := fmt.Sprintf("%s/%dx%d", state, size.width, size.height)
			t.Run(name, func(t *testing.T) {
				model, err := NewTUIFixture(state)
				if err != nil {
					t.Fatal(err)
				}
				fixture := model.(*tuiFixtureModel)
				_, _ = fixture.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
				view := fixture.View()
				if !strings.Contains(view, wants[state]) {
					t.Fatalf("fixture %q missing %q:\n%s", state, wants[state], view)
				}
				if state == "user-assistant" && !strings.Contains(view, "café · 中文 · 👩🏽‍💻") {
					t.Fatalf("fixture %q lost its complete Unicode cluster:\n%s", state, view)
				}
				if state != "workflows" && (!strings.Contains(view, "❯") || (state != "agents" && !strings.Contains(view, "272K"))) {
					t.Fatalf("fixture %q missing shared input/status chrome:\n%s", state, view)
				}
				for lineNo, line := range strings.Split(view, "\n") {
					if got := lipgloss.Width(line); got > size.width {
						t.Fatalf("fixture %q line %d is %d cells wide at %d columns:\n%s", state, lineNo+1, got, size.width, view)
					}
				}
			})
		}
	}
}

func TestTUIFixtureRejectsUnknownState(t *testing.T) {
	if _, err := NewTUIFixture("not-a-state"); err == nil {
		t.Fatal("expected unknown fixture error")
	}
}

func TestTUIFixtureResizeRecomposesStatusLineForTargetWidth(t *testing.T) {
	resizedModel, err := NewTUIFixture("user-assistant")
	if err != nil {
		t.Fatal(err)
	}
	resized := resizedModel.(*tuiFixtureModel)
	_, _ = resized.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, _ = resized.Update(tea.WindowSizeMsg{Width: 72, Height: 24})

	directModel, err := NewTUIFixture("user-assistant")
	if err != nil {
		t.Fatal(err)
	}
	direct := directModel.(*tuiFixtureModel)
	_, _ = direct.Update(tea.WindowSizeMsg{Width: 72, Height: 24})

	if got, want := resized.topbar.CustomLine(), direct.topbar.CustomLine(); got != want {
		t.Fatalf("resized status line retained wide layout\n got: %q\nwant: %q", got, want)
	}
}
