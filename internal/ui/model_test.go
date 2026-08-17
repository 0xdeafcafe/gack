package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

func readyDemoModel(t *testing.T, width, height int) *Model {
	t.Helper()
	model := New(demo.New(), nil, nil)
	model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	bootstrap := model.Init()
	_, load := model.Update(bootstrap())
	if load == nil {
		t.Fatal("bootstrap did not request messages")
	}
	model.Update(load())
	return model
}

func TestViewFitsTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 36}, {42, 18}} {
		model := readyDemoModel(t, size.width, size.height)
		view := model.View()
		t.Logf("%dx%d header=%d body=%d footer=%d total=%d", size.width, size.height, lipgloss.Height(model.renderHeader()), lipgloss.Height(model.renderBody(max(3, size.height-2))), lipgloss.Height(model.renderFooter()), lipgloss.Height(view))
		if got := lipgloss.Height(view); got > size.height {
			t.Errorf("%dx%d view is %d lines tall", size.width, size.height, got)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Errorf("%dx%d line %d is %d columns wide", size.width, size.height, lineNumber+1, got)
			}
		}
	}
}

func TestVirtualMessageRendererOnlyFormatsVisibleWindow(t *testing.T) {
	model := readyDemoModel(t, 80, 24)
	model.message = len(model.messages) - 1
	rendered := model.virtualMessages(model.messages, model.message, 8, 48, true)
	if lipgloss.Height(rendered) > 8 {
		t.Fatalf("virtualized message list exceeded viewport: %d lines", lipgloss.Height(rendered))
	}
}

func TestViewMakesEveryFocusLevelExplicit(t *testing.T) {
	model := readyDemoModel(t, 120, 36)

	tests := []struct {
		name string
		set  func()
		want []string
	}{
		{
			name: "conversation message",
			set:  func() { model.focus = focusMessages },
			want: []string{"Conversation › message", "●", "FOCUS", "CONVERSATION"},
		},
		{
			name: "sidebar item",
			set:  func() { model.focus = focusSidebar },
			want: []string{"Sidebar ›", "FOCUS", "SIDEBAR"},
		},
		{
			name: "thread reply",
			set: func() {
				model.threadTS = "thread"
				model.thread = append([]gack.Message(nil), model.messages...)
				model.threadAt = 1
				model.focus = focusThread
			},
			want: []string{"Thread › reply 2/", "FOCUS", "THREAD"},
		},
		{
			name: "composer",
			set:  func() { model.openComposer(model.threadTS) },
			want: []string{"Thread › Composer", "● COMPOSER", "REPLYING IN THREAD", "Enter newline"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.set()
			view := ansi.Strip(model.View())
			for _, want := range test.want {
				if !strings.Contains(view, want) {
					t.Errorf("view does not identify %q; missing %q", test.name, want)
				}
			}
			assertViewFits(t, model)
		})
	}
}

func TestCommandPaletteFloatsAboveDimmedWorkspace(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.openSearch()
	view := ansi.Strip(model.View())

	for _, want := range []string{"COMMAND PALETTE", "Jump to a channel", "Notifications", "Maya Chen"} {
		if !strings.Contains(view, want) {
			t.Errorf("floating palette view is missing %q", want)
		}
	}
	assertViewFits(t, model)
}

func TestComposerAndPaletteFitCompactTerminals(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {42, 18}} {
		model := readyDemoModel(t, size.width, size.height)
		model.openComposer("")
		assertViewFits(t, model)

		model.focus = focusMessages
		model.openSearch()
		assertViewFits(t, model)
	}
}

func TestDialogIdentifiesFocusedField(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.dialog = newDialogState(gack.View{
		Title: "Deploy release",
		Blocks: []gack.Block{
			{Type: "input", BlockID: "environment", Label: "Environment", Elements: []gack.Element{{Type: "plain_text_input", ActionID: "environment_name"}}},
			{Type: "input", BlockID: "severity", Label: "Severity", Elements: []gack.Element{{Type: "static_select", ActionID: "severity_value", Options: []gack.Option{{Text: "SEV-1", Value: "sev1"}}}}},
		},
	}, model.width)

	view := ansi.Strip(model.View())
	for _, want := range []string{"Dialog › Environment (1/2)", "DIALOG · Deploy release", "FIELD 1 OF 2", "Environment  ← FOCUSED"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog view is missing %q", want)
		}
	}
	assertViewFits(t, model)
}

func assertViewFits(t *testing.T, model *Model) {
	t.Helper()
	view := model.View()
	if got := lipgloss.Height(view); got > model.height {
		t.Errorf("view is %d lines tall in a %d-line terminal", got, model.height)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > model.width {
			t.Errorf("line %d is %d columns wide in a %d-column terminal", lineNumber+1, got, model.width)
		}
	}
}
