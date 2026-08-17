package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/gack"
)

func TestComposerSupportsMultilinePasteAndLineEditing(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if model.focus != focusComposer {
		t.Fatal("c did not focus the composer")
	}
	paste := "```go\nfunc hello() {}\n```"
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	if got := model.composeInput.Value(); got != paste {
		t.Fatalf("multiline paste = %q", got)
	}
	model.composeInput.SetValue("keep this\ndelete this")
	model.composeInput.CursorEnd()
	model.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := model.composeInput.Value(); got != "keep this\n" {
		t.Fatalf("Ctrl+U should delete to start of current line, got %q", got)
	}
}

func TestComposerUsesOptionArrowsForWordMovement(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	model.openComposer("")
	model.composeInput.SetValue("one two three")
	model.composeInput.CursorEnd()
	before := model.composeInput.LineInfo().CharOffset
	model.Update(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	after := model.composeInput.LineInfo().CharOffset
	if after >= before || after != strings.LastIndex("one two three", "three") {
		t.Fatalf("Option+Left moved from %d to %d", before, after)
	}
}

func TestEditLatestOwnMessage(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	model.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if model.focus != focusComposer || model.composeEditTS == "" {
		t.Fatal("Ctrl+Up did not open the latest own message for editing")
	}
	model.composeInput.SetValue("A better version of my message")
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if command == nil {
		t.Fatal("Ctrl+S did not submit the edit")
	}
	model.Update(command())
	if got := model.messages[len(model.messages)-1]; got.Text != "A better version of my message" || !got.Edited {
		t.Fatalf("message was not edited: %#v", got)
	}
}

func TestPaletteTemporarilyTakesFocusFromComposer(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	model.openComposer("")
	model.composeInput.SetValue("draft stays here")
	model.openSearch()
	if model.composeInput.Focused() {
		t.Fatal("composer remained focused beneath the command palette")
	}
	if footer := model.renderFooter(); !strings.Contains(footer, "COMMAND PALETTE") {
		t.Fatalf("palette footer was not active: %q", footer)
	}
	model.closeSearch()
	if !model.composeInput.Focused() || model.composeInput.Value() != "draft stays here" {
		t.Fatal("closing the palette did not restore the composer draft and focus")
	}
}

func TestEditLatestOwnThreadReply(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	model.threadTS = "1710000000.000200"
	model.thread = []gack.Message{
		{TS: "reply-other", ThreadTS: model.threadTS, UserID: "U_OTHER", Text: "other"},
		{TS: "reply-mine", ThreadTS: model.threadTS, UserID: model.snapshot.Self.ID, Text: "mine"},
	}
	model.focus = focusThread
	model.openComposer(model.threadTS)
	model.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if model.composeEditTS != "reply-mine" || model.composeInput.Value() != "mine" {
		t.Fatalf("Ctrl+Up selected %q with text %q", model.composeEditTS, model.composeInput.Value())
	}
}
