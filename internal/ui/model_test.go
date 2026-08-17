package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/0xdeafcafe/gack/internal/demo"
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
