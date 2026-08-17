package ui

import (
	"fmt"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/demo"
	"github.com/0xdeafcafe/gack/internal/gack"
)

func TestAlphabeticalChannelSortIsDeterministicAndPreservesSelection(t *testing.T) {
	model := &Model{
		channels: []gack.Conversation{
			{ID: "z", Name: "Zulu"},
			{ID: "b", Name: "alpha"},
			{ID: "a", Name: "Alpha"},
			{ID: "m", Name: "maya", DisplayName: "@Maya", IsDM: true},
		},
		channel:   0,
		sidebarAt: 3,
		order:     []string{"z", "b", "a"},
	}
	model.sortChannels(config.SidebarSortAlphabetical)

	if got, want := model.channelOrder(), []string{"a", "b", "m", "z"}; !slices.Equal(got, want) {
		t.Fatalf("alphabetical order = %v, want %v", got, want)
	}
	if got := model.currentChannelID(); got != "z" {
		t.Fatalf("active channel changed to %q", got)
	}
	if got := model.channels[model.sidebarAt-2].ID; got != "b" {
		t.Fatalf("sidebar cursor changed to %q", got)
	}
}

func TestAttentionSortPrioritizesMentionsUnreadAndFavorites(t *testing.T) {
	model := &Model{channels: []gack.Conversation{
		{ID: "quiet", Name: "aardvark"},
		{ID: "favorite", Name: "bravo", IsFavorite: true},
		{ID: "unread-low", Name: "charlie", Unread: 1},
		{ID: "unread-high", Name: "delta", Unread: 9},
		{ID: "mention-low", Name: "echo", Unread: 1, Mentions: 1},
		{ID: "mention-high", Name: "foxtrot", Unread: 2, Mentions: 3},
	}}
	model.sortChannels(config.SidebarSortAttention)

	want := []string{"mention-high", "mention-low", "unread-high", "unread-low", "favorite", "quiet"}
	if got := model.channelOrder(); !slices.Equal(got, want) {
		t.Fatalf("attention order = %v, want %v", got, want)
	}
}

func TestSidebarSortCyclesAndPersistsWithoutLosingManualOrder(t *testing.T) {
	var saved []config.SidebarPreferences
	model := NewWithSidebar(demo.New(), config.SidebarPreferences{
		ChannelOrder: []string{"C-PROD", "C-GENERAL"},
		Sort:         config.SidebarSortManual,
	}, func(preferences config.SidebarPreferences) error {
		saved = append(saved, preferences)
		return nil
	})
	model.channels = []gack.Conversation{
		{ID: "C-PROD", Name: "production"},
		{ID: "C-GENERAL", Name: "general"},
		{ID: "C-ALERTS", Name: "alerts", Unread: 4},
	}
	model.order = model.channelOrder()
	model.channel = 0
	model.sidebarAt = 2

	for _, want := range []config.SidebarSort{
		config.SidebarSortAlphabetical,
		config.SidebarSortAttention,
		config.SidebarSortManual,
	} {
		command := model.updateSidebar("s")
		if command == nil {
			t.Fatal("sort cycle did not return a persistence command")
		}
		model.Update(command())
		if model.sidebarSort != want {
			t.Fatalf("sort = %q, want %q", model.sidebarSort, want)
		}
	}

	if got, want := model.channelOrder(), []string{"C-PROD", "C-GENERAL", "C-ALERTS"}; !slices.Equal(got, want) {
		t.Fatalf("manual order after cycling = %v, want %v", got, want)
	}
	if len(saved) != 3 || saved[2].Sort != config.SidebarSortManual {
		t.Fatalf("saved preferences = %#v", saved)
	}
}

func TestReorderingComputedSortCapturesVisibleOrderAsManual(t *testing.T) {
	var saved config.SidebarPreferences
	model := NewWithSidebar(demo.New(), config.SidebarPreferences{Sort: config.SidebarSortAlphabetical}, func(preferences config.SidebarPreferences) error {
		saved = preferences
		return nil
	})
	model.channels = []gack.Conversation{
		{ID: "a", Name: "alpha"},
		{ID: "b", Name: "bravo"},
		{ID: "c", Name: "charlie"},
	}
	model.channel = 0
	model.sidebarAt = 3                   // bravo
	model.order = []string{"c", "b", "a"} // previously saved manual order

	command := model.updateSidebar("J")
	if command == nil {
		t.Fatal("reorder did not return a persistence command")
	}
	model.Update(command())

	if model.sidebarSort != config.SidebarSortManual {
		t.Fatalf("sort after reorder = %q", model.sidebarSort)
	}
	want := []string{"a", "c", "b"}
	if got := model.channelOrder(); !slices.Equal(got, want) {
		t.Fatalf("visible order after reorder = %v, want %v", got, want)
	}
	if saved.Sort != config.SidebarSortManual || !slices.Equal(saved.ChannelOrder, want) {
		t.Fatalf("saved preferences = %#v, want manual %v", saved, want)
	}
}

func TestNewestSidebarPreferenceWinsOutOfOrderCommands(t *testing.T) {
	var saved []config.SidebarPreferences
	model := NewWithSidebar(demo.New(), config.SidebarPreferences{}, func(preferences config.SidebarPreferences) error {
		saved = append(saved, preferences)
		return nil
	})
	model.channels = []gack.Conversation{{ID: "a", Name: "alpha"}, {ID: "b", Name: "bravo"}}
	model.order = model.channelOrder()
	model.channel = 0
	model.sidebarAt = 2

	older := model.updateSidebar("s") // alphabetical
	newer := model.updateSidebar("s") // attention
	model.Update(newer())
	model.Update(older())

	if len(saved) != 1 || saved[0].Sort != config.SidebarSortAttention {
		t.Fatalf("out-of-order commands saved %#v", saved)
	}
}

func TestMouseDragFromComputedSortBecomesManual(t *testing.T) {
	var saved config.SidebarPreferences
	model := NewWithSidebar(demo.New(), config.SidebarPreferences{Sort: config.SidebarSortAlphabetical}, func(preferences config.SidebarPreferences) error {
		saved = preferences
		return nil
	})
	model.width = 100
	model.ready = true
	model.channels = []gack.Conversation{
		{ID: "a", Name: "alpha"},
		{ID: "b", Name: "bravo"},
		{ID: "c", Name: "charlie"},
	}
	model.order = []string{"c", "b", "a"}
	model.channel = 0
	model.visibleChannelStart = 0
	model.channelRowStart = 5

	model.updateMouse(tea.MouseMsg{X: 1, Y: 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	model.updateMouse(tea.MouseMsg{X: 1, Y: 7, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	_, command := model.updateMouse(tea.MouseMsg{X: 1, Y: 7, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if command == nil {
		t.Fatal("drag release did not persist sidebar preferences")
	}
	model.Update(command())

	want := []string{"a", "c", "b"}
	if model.sidebarSort != config.SidebarSortManual || !slices.Equal(model.channelOrder(), want) {
		t.Fatalf("drag resulted in sort %q, order %v", model.sidebarSort, model.channelOrder())
	}
	if saved.Sort != config.SidebarSortManual || !slices.Equal(saved.ChannelOrder, want) {
		t.Fatalf("drag saved %#v, want manual %v", saved, want)
	}
}

func TestSidebarClickDoesNotRecenterUntilReleaseAndClearsStaleHover(t *testing.T) {
	model := readyDemoModel(t, 150, 28)
	for index := 0; index < 60; index++ {
		model.channels = append(model.channels, gack.Conversation{
			ID: fmt.Sprintf("mouse-%02d", index), Name: fmt.Sprintf("mouse-%02d", index),
		})
	}
	model.focus = focusSidebar
	model.sidebarAt = 2
	model.View()
	if len(model.visibleSidebarHits) < 2 {
		t.Fatal("test needs multiple visible channels")
	}
	target := model.visibleSidebarHits[len(model.visibleSidebarHits)-1]
	previousSelection := model.sidebarAt

	model.updateMouse(tea.MouseMsg{X: 2, Y: target.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if model.sidebarAt != previousSelection {
		t.Fatalf("mouse press recentered selection from %d to %d", previousSelection, model.sidebarAt)
	}
	if model.dragFrom != target.channelIndex {
		t.Fatalf("press target = %d, want %d", model.dragFrom, target.channelIndex)
	}

	_, command := model.updateMouse(tea.MouseMsg{X: 2, Y: target.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if command == nil {
		t.Fatal("channel click did not request its conversation")
	}
	if model.channel != target.channelIndex || model.hoverPane != focus(-1) || model.hoverSidebarAt != -1 {
		t.Fatalf("release channel=%d hover=(%d,%d), want channel=%d and cleared hover", model.channel, model.hoverPane, model.hoverSidebarAt, target.channelIndex)
	}
}
