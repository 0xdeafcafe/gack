package ui

import (
	"fmt"
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
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 36}, {196, 60}, {42, 18}, {30, 10}} {
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
			want: []string{"Conversation › message", "◆", "ACTIVE PANE", "SELECTED", "CONVERSATION"},
		},
		{
			name: "sidebar item",
			set:  func() { model.focus = focusSidebar },
			want: []string{"Sidebar ›", "ACTIVE PANE", "SIDEBAR", "s sort"},
		},
		{
			name: "thread reply",
			set: func() {
				model.threadTS = "thread"
				model.thread = append([]gack.Message(nil), model.messages...)
				model.threadAt = 1
				model.focus = focusThread
			},
			want: []string{"Thread › reply 2/", "ACTIVE PANE", "SELECTED", "THREAD"},
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

	for _, want := range []string{"COMMAND PALETTE", "Jump to a channel", "Maya Chen"} {
		if !strings.Contains(view, want) {
			t.Errorf("floating palette view is missing %q", want)
		}
	}
	assertViewFits(t, model)
}

func TestComposerAndPaletteFitCompactTerminals(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {42, 18}, {30, 12}} {
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
	for _, want := range []string{"Dialog › Environment (1/2)", "INTERACTIVE DIALOG · Deploy release", "FIELD 1 OF 2", "Environment EDITING THIS FIELD"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog view is missing %q", want)
		}
	}
	assertViewFits(t, model)
}

func TestWideConversationUsesReadableSelectedMessageCard(t *testing.T) {
	model := readyDemoModel(t, 196, 60)
	model.focus = focusMessages
	view := ansi.Strip(model.View())
	for _, want := range []string{"YOU ARE HERE", "ACTIVE PANE", "SELECTED", "CONVERSATION"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view is missing %q", want)
		}
	}

	message := model.renderMessage(gack.Message{
		Username: "A very readable person",
		Text:     strings.Repeat("This should never become a cinema-wide line. ", 20),
	}, 94, true, 0)
	for lineNumber, line := range strings.Split(message, "\n") {
		if got := lipgloss.Width(line); got > 94 {
			t.Errorf("selected card line %d is %d columns; reading measure is 94", lineNumber+1, got)
		}
	}
	assertViewFits(t, model)
}

func TestLongSidebarShowsVirtualWindowSortAndDestination(t *testing.T) {
	model := readyDemoModel(t, 196, 40)
	for index := 0; index < 80; index++ {
		model.channels = append(model.channels, gack.Conversation{ID: fmt.Sprintf("C_%03d", index), Name: fmt.Sprintf("project-with-a-useful-name-%03d", index), IsMember: true})
	}
	model.focus = focusSidebar
	model.sidebarAt = 42
	sidebar := ansi.Strip(model.renderSidebar(model.sidebarWidth(), 36))
	for _, want := range []string{"CHANNELS", " OF 84", "s: MANUAL", "ACTIVE PANE", "›"} {
		if !strings.Contains(sidebar, want) {
			t.Errorf("long sidebar is missing %q", want)
		}
	}
}

func TestHelpExplainsWritingAndSidebarKeys(t *testing.T) {
	model := readyDemoModel(t, 120, 40)
	model.overlay = overlayHelp
	view := ansi.Strip(model.View())
	for _, want := range []string{
		"Ctrl+K", "Command palette",
		"s", "Sort sidebar",
		"Enter (write)", "Insert newline",
		"Ctrl+S", "Send / submit",
		"Cmd+K: map the chord to Ctrl+K",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("help view is missing %q", want)
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
