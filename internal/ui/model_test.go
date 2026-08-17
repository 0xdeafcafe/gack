package ui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

func readyDemoModel(t *testing.T, width, height int) *Model {
	t.Helper()
	model := New(demo.New(), nil, nil)
	model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model.Init()
	_, load := model.Update(bootstrapCmd(model.backend)())
	if load == nil {
		t.Fatal("bootstrap did not request messages")
	}
	model.Update(load())
	return model
}

func TestConnectingAndRecoveryStatesAreActionable(t *testing.T) {
	model := New(demo.New(), nil, nil)
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	model.Init()
	connecting := ansi.Strip(model.View())
	for _, want := range []string{"CONNECTING TO SLACK", "Opening your workspace", "session and joined conversations", "q cancel"} {
		if !strings.Contains(connecting, want) {
			t.Errorf("connecting view is missing %q", want)
		}
	}

	model.Update(bootstrapResult{err: fmt.Errorf("load conversations: %w", context.DeadlineExceeded)})
	recovery := ansi.Strip(model.View())
	for _, want := range []string{"SLACK TOOK TOO LONG TO ANSWER", "R  TRY AGAIN", "L  SIGN IN AGAIN", "R retry", "Nothing was changed"} {
		if !strings.Contains(recovery, want) {
			t.Errorf("recovery view is missing %q", want)
		}
	}
	if strings.Count(recovery, "load conversations: context deadline exceeded") != 1 {
		t.Fatalf("technical error should appear once:\n%s", recovery)
	}
	_, retry := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if retry == nil || model.err != "" || model.busy == "" {
		t.Fatal("retry did not restart the connection")
	}

	model.Update(bootstrapResult{err: context.DeadlineExceeded})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if model.RequestedExit() != ExitLogin {
		t.Fatalf("requested exit = %d, want login", model.RequestedExit())
	}
	assertViewFits(t, model)
}

func TestConnectingAndRecoveryFitCompactTerminals(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {42, 18}, {30, 10}} {
		model := New(demo.New(), nil, nil)
		model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		model.Init()
		assertViewFits(t, model)
		model.Update(bootstrapResult{err: context.DeadlineExceeded})
		assertViewFits(t, model)
	}
}

func TestAvailableUpdateAppearsInHeaderAndCanBeSelected(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.Update(versionResult{latest: "v0.4.0"})
	if view := ansi.Strip(model.View()); !strings.Contains(view, "v0.4.0") || !strings.Contains(view, "u update") {
		t.Fatalf("update banner missing:\n%s", view)
	}
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	if model.RequestedExit() != ExitUpdate {
		t.Fatalf("requested exit = %d, want update", model.RequestedExit())
	}
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

func TestVirtualMessageWindowBackfillsAndGroupsWithoutBlankRows(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	now := time.Now()
	messages := []gack.Message{
		{TS: "1", UserID: "U1", Username: "Alex", Time: now, Text: strings.Repeat("long earlier message ", 80)},
		{TS: "2", UserID: "U1", Username: "Alex", Time: now.Add(time.Minute), Text: "follow-up"},
		{TS: "3", UserID: "U2", Username: "Maya", Time: now.Add(10 * time.Minute), Text: "latest"},
	}
	rendered := model.virtualMessages(messages, 2, 18, 48, true)
	if got := lipgloss.Height(rendered); got != 18 {
		t.Fatalf("backfilled window height = %d, want 18\n%s", got, ansi.Strip(rendered))
	}
	compact := []gack.Message{
		{TS: "1", UserID: "U1", Username: "Alex", Time: now, Text: "one"},
		{TS: "2", UserID: "U1", Username: "Alex", Time: now.Add(time.Minute), Text: "two"},
	}
	grouped := ansi.Strip(model.virtualMessages(compact, 0, 18, 48, true))
	if strings.Count(grouped, "Alex") != 1 || !strings.Contains(grouped, "↳") || strings.Contains(grouped, "\n\n\n") {
		t.Fatalf("message grouping is not compact:\n%s", grouped)
	}
}

func TestUpKeyMovesConversationSelection(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.focus = focusMessages
	model.message = len(model.messages) - 1
	before := model.message
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	if model.message != before-1 {
		t.Fatalf("up moved message %d → %d", before, model.message)
	}
}

func TestOpeningThreadUsesRootAndSkipsEmptyThreadRequest(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.channel = 0

	reply := gack.Message{TS: "reply", ThreadTS: "root", ReplyCount: 2, Text: "reply"}
	command := model.openThread(reply)
	if command == nil || model.threadTS != "root" || model.focus != focusThread {
		t.Fatalf("reply opened with thread=%q focus=%d command=%v", model.threadTS, model.focus, command)
	}

	root := gack.Message{TS: "new-root", Text: "start a thread"}
	command = model.openThread(root)
	if command != nil || model.threadTS != "new-root" || len(model.thread) != 1 || model.focus != focusThread {
		t.Fatalf("empty thread should open locally: thread=%q replies=%d focus=%d command=%v", model.threadTS, len(model.thread), model.focus, command)
	}
}

func TestReselectingActiveChannelOnlyMovesFocus(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.threadTS = "root"
	model.thread = []gack.Message{{TS: "root"}, {TS: "reply", ThreadTS: "root"}}
	model.focus = focusSidebar
	before := append([]gack.Message(nil), model.messages...)

	if command := model.openChannel(model.channel); command != nil {
		t.Fatal("active channel selection unexpectedly reloaded messages")
	}
	if model.focus != focusMessages || model.threadTS != "root" || len(model.thread) != 2 || !reflect.DeepEqual(model.messages, before) {
		t.Fatalf("active channel selection discarded local state: focus=%d thread=%q replies=%d", model.focus, model.threadTS, len(model.thread))
	}
}

func TestChannelHistoryCacheIsBoundedAndRestoresThread(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.threadTS = "root"
	model.thread = []gack.Message{{TS: "root"}}
	activeID := model.currentChannelID()
	model.cacheCurrentConversation()
	model.messages = nil
	model.threadTS, model.thread = "", nil

	if !model.restoreConversation(activeID) || len(model.messages) == 0 || model.threadTS != "root" || len(model.thread) != 1 {
		t.Fatalf("cached view did not restore: messages=%d thread=%q replies=%d", len(model.messages), model.threadTS, len(model.thread))
	}
	for index := 0; index < 10; index++ {
		model.channels = append(model.channels, gack.Conversation{ID: fmt.Sprintf("cache-%d", index), Name: "cache"})
		model.channel = len(model.channels) - 1
		model.messages = []gack.Message{{TS: fmt.Sprintf("%d", index)}}
		model.cacheCurrentConversation()
	}
	if len(model.conversationViews) != 8 || len(model.conversationCache) != 8 {
		t.Fatalf("cache grew to views=%d order=%d", len(model.conversationViews), len(model.conversationCache))
	}
}

func TestPagedHistoryAdvancesIntoNewlyLoadedMessages(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.messages = []gack.Message{{TS: "3"}, {TS: "4"}}
	model.message = 0
	model.messageCursor = "next"
	model.reduce(messagesResult{
		channel: model.currentChannelID(), more: true, nextCursor: "",
		messages: []gack.Message{{TS: "1"}, {TS: "2"}, {TS: "3"}},
	})
	if len(model.messages) != 4 || model.message != 1 || model.messages[model.message].TS != "2" || model.messageCursor != "" {
		t.Fatalf("paged history = %#v selected=%d cursor=%q", model.messages, model.message, model.messageCursor)
	}
}

func TestMouseHoverAndClicksIdentifyRealTargets(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	model.View() // establishes the virtualized sidebar row coordinates

	model.updateMouse(tea.MouseMsg{X: 3, Y: 5, Action: tea.MouseActionMotion})
	if model.hoverPane != focusSidebar || model.hoverSidebarAt != 0 || !strings.Contains(ansi.Strip(model.View()), "POINTER › Notifications") {
		t.Fatalf("notification hover = pane %d row %d", model.hoverPane, model.hoverSidebarAt)
	}

	model.updateMouse(tea.MouseMsg{X: model.sidebarWidth() + 4, Y: 4, Action: tea.MouseActionMotion})
	if model.hoverPane != focusMessages || model.hoverMessage < 0 {
		t.Fatalf("conversation hover = pane %d message %d", model.hoverPane, model.hoverMessage)
	}
	hovered := model.hoverMessage
	model.updateMouse(tea.MouseMsg{X: model.sidebarWidth() + 4, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if model.focus != focusMessages || model.message != hovered {
		t.Fatalf("conversation click = focus %d message %d, want %d", model.focus, model.message, hovered)
	}
}

func TestRegexSidebarGroupsKeepChannelOrderWithinSections(t *testing.T) {
	model := NewWithSidebar(demo.New(), config.SidebarPreferences{Groups: []config.SidebarGroup{
		{Name: "Engineering", Pattern: `^(eng|dev)-`},
		{Name: "Alerts", Pattern: `alerts?$`},
	}}, nil)
	model.channels = []gack.Conversation{
		{ID: "1", Name: "eng-api"},
		{ID: "2", Name: "random"},
		{ID: "3", Name: "dev-tools"},
		{ID: "4", Name: "prod-alerts"},
	}
	rows := model.sidebarDisplayRows()
	var labels []string
	var indexes []int
	for _, row := range rows {
		if row.channelIndex < 0 {
			labels = append(labels, row.label)
		} else {
			indexes = append(indexes, row.channelIndex)
		}
	}
	if !reflect.DeepEqual(labels, []string{"Engineering (2)", "Alerts (1)", "Other (1)"}) || !reflect.DeepEqual(indexes, []int{0, 2, 3, 1}) {
		t.Fatalf("group rows labels=%v indexes=%v", labels, indexes)
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
	for _, want := range []string{"CHANNELS", " OF 84", "SORT · MANUAL", "[s]", "ACTIVE PANE", "›"} {
		if !strings.Contains(sidebar, want) {
			t.Errorf("long sidebar is missing %q", want)
		}
	}
}

func TestNarrowSidebarKeepsChannelTotalAndSortWhole(t *testing.T) {
	model := readyDemoModel(t, 150, 40)
	for index := 0; index < 93; index++ {
		model.channels = append(model.channels, gack.Conversation{ID: fmt.Sprintf("C_%03d", index), Name: fmt.Sprintf("project-%03d", index), IsMember: true})
	}
	model.focus = focusSidebar
	model.sidebarAt = 70
	sidebar := ansi.Strip(model.renderSidebar(30, 36))
	for _, want := range []string{"CHANNELS ", " OF 97", "SORT · MANUAL  [s]"} {
		if !strings.Contains(sidebar, want) {
			t.Errorf("30-column sidebar is missing %q:\n%s", want, sidebar)
		}
	}
	if strings.Contains(sidebar, "CHANNELS 55–80 OF  ") {
		t.Fatalf("channel total was clipped:\n%s", sidebar)
	}
}

func TestBlockKitTextReplacesFallbackWithoutLosingLinksOrControls(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	blocks, err := gack.ParseBlocks([]byte(`[
  {"type":"rich_text","elements":[{"type":"rich_text_section","elements":[
    {"type":"broadcast","range":"here"},{"type":"text","text":" Read "},
    {"type":"link","url":"https://example.com/runbook","text":"the runbook"}
  ]}]},
  {"type":"actions","elements":[{"type":"button","action_id":"ack","text":{"type":"plain_text","text":"Acknowledge"}}]}
]`))
	if err != nil {
		t.Fatal(err)
	}
	rendered := ansi.Strip(model.renderMessage(gack.Message{
		Username: "Deploy bot",
		Text:     "FALLBACK ONLY <!here> Read the runbook https://example.com/runbook",
		Blocks:   blocks,
	}, 90, true, 0))
	if strings.Contains(rendered, "FALLBACK ONLY") {
		t.Fatalf("fallback was rendered alongside Block Kit:\n%s", rendered)
	}
	for _, want := range []string{"@here Read the runbook (https://example.com/runbook)", "[1 Acknowledge]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered Block Kit is missing %q:\n%s", want, rendered)
		}
	}

	controlsOnly := ansi.Strip(model.renderMessage(gack.Message{
		Username: "Deploy bot",
		Text:     "Choose a deployment action",
		Blocks:   []gack.Block{{Type: "actions", Elements: []gack.Element{{Type: "button", ActionID: "go", Text: "Deploy"}}}},
	}, 90, false, 0))
	for _, want := range []string{"Choose a deployment action", "[Deploy]"} {
		if !strings.Contains(controlsOnly, want) {
			t.Errorf("controls-only message is missing %q:\n%s", want, controlsOnly)
		}
	}

	textOnly := ansi.Strip(model.renderMessage(gack.Message{Username: "Human", Text: "plain Slack text"}, 90, false, 0))
	if !strings.Contains(textOnly, "plain Slack text") {
		t.Fatalf("text-only message disappeared:\n%s", textOnly)
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
