package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestWorkspaceNameIsAKeyboardAccessibleMenu(t *testing.T) {
	model := readyDemoModel(t, 120, 36)
	view := ansi.Strip(model.View())
	if strings.Contains(view, "YOU ARE HERE") {
		t.Fatalf("legacy breadcrumb label is still visible:\n%s", view)
	}
	for _, want := range []string{"Acme Engineering", "▾", "w workspace"} {
		if !strings.Contains(view, want) {
			t.Errorf("workspace control is missing %q", want)
		}
	}

	opened := ""
	model.SetURLOpener(func(_ context.Context, target string) error {
		opened = target
		return nil
	})
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if model.overlay != overlayWorkspace {
		t.Fatal("w did not open the workspace menu")
	}
	menu := ansi.Strip(model.View())
	for _, want := range []string{"Workspace menu › option 1/3", "Manage apps", "Add custom emoji", "Close"} {
		if !strings.Contains(menu, want) {
			t.Errorf("workspace menu is missing %q:\n%s", want, menu)
		}
	}
	assertViewFits(t, model)

	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || opened != "" || model.overlay != overlayNone {
		t.Fatalf("selecting emoji shortcut: command=%v opened=%q overlay=%d", command != nil, opened, model.overlay)
	}
	event, ok := command().(externalURLResult)
	if !ok || event.url != slackCustomEmojiURL || opened != slackCustomEmojiURL {
		t.Fatalf("browser event=%#v opened=%q", event, opened)
	}
	model.reduce(event)
	if model.err != "" || model.status != "Add custom emoji opened in your browser" {
		t.Fatalf("browser success state: status=%q err=%q", model.status, model.err)
	}
}

func TestWorkspaceMenuMouseAndBrowserFailureAreReduced(t *testing.T) {
	model := readyDemoModel(t, 100, 30)
	openErr := errors.New("no browser available")
	model.SetURLOpener(func(context.Context, string) error { return openErr })

	model.updateMouse(tea.MouseMsg{X: 1, Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if model.overlay != overlayWorkspace {
		t.Fatal("clicking the workspace control did not open its menu")
	}
	bodyHeight := max(3, model.height-3)
	_, itemStart, _ := model.workspaceMenuLayout(bodyHeight)
	manageAppsY := 2 + 1 + itemStart
	model.updateMouse(tea.MouseMsg{X: 3, Y: manageAppsY, Action: tea.MouseActionMotion})
	model.updateMouse(tea.MouseMsg{X: 3, Y: manageAppsY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	_, command := model.updateMouse(tea.MouseMsg{X: 3, Y: manageAppsY, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if command == nil || model.overlay != overlayNone {
		t.Fatal("clicking Manage apps did not close the menu and describe an effect")
	}
	event, ok := command().(externalURLResult)
	if !ok || event.url != slackManageAppsURL || !errors.Is(event.err, openErr) {
		t.Fatalf("browser failure event = %#v", event)
	}
	model.reduce(event)
	if !strings.Contains(model.err, "Could not open Manage apps: no browser available") {
		t.Fatalf("browser error was not reduced into visible state: %q", model.err)
	}
}

func TestWorkspaceMenuFitsCompactTerminals(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {42, 18}, {30, 10}} {
		model := readyDemoModel(t, size.width, size.height)
		model.openWorkspaceMenu()
		view := ansi.Strip(model.View())
		for _, want := range []string{"Manage apps", "Add custom emoji", "Close"} {
			if !strings.Contains(view, want) {
				t.Errorf("%dx%d workspace menu is missing %q", size.width, size.height, want)
			}
		}
		assertViewFits(t, model)
	}
}
