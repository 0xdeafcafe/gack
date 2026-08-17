package loginui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/0xdeafcafe/gack/internal/auth"
)

func TestSetupOpensCreatorThenFocusesClientID(t *testing.T) {
	opened := false
	model := New(context.Background(), Config{OpenCreator: func() error {
		opened = true
		return nil
	}})
	if model.stage != stageSetup || model.focus != focusCreator {
		t.Fatalf("unexpected initial state: stage=%d focus=%d", model.stage, model.focus)
	}

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.openingCreator || command == nil {
		t.Fatal("Enter did not begin opening the app creator")
	}
	result := runBatchCommand(t, command, func(message tea.Msg) bool {
		_, ok := message.(creatorResult)
		return ok
	})
	model.Update(result)
	if !opened || !model.creatorOpened || model.focus != focusClientID || !model.input.Focused() {
		t.Fatalf("creator completion did not focus input: opened=%v focus=%d", opened, model.focus)
	}
}

func TestClientIDValidationAndConfirmation(t *testing.T) {
	model := New(context.Background(), Config{})
	model.setSetupFocus(focusClientID)

	model.input.SetValue("xoxb-secret-value")
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.stage != stageSetup || !strings.Contains(model.errText, "looks like a token") {
		t.Fatalf("token was not rejected safely: stage=%d error=%q", model.stage, model.errText)
	}

	model.input.SetValue("123456789.987654321")
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.stage != stageConfirm || model.clientID != "123456789.987654321" {
		t.Fatalf("valid Client ID did not advance: stage=%d client=%q", model.stage, model.clientID)
	}
}

func TestPastedTokenIsClearedBeforeRendering(t *testing.T) {
	model := New(context.Background(), Config{})
	model.setSetupFocus(focusClientID)
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("xoxp-do-not-show-this")})
	if model.input.Value() != "" {
		t.Fatalf("token-like input was retained: %q", model.input.Value())
	}
	if strings.Contains(model.View(), "do-not-show-this") {
		t.Fatal("token-like input appeared in the rendered view")
	}
}

func TestSavedClientIDStartsAtConfirmation(t *testing.T) {
	model := New(context.Background(), Config{ClientID: "123.456"})
	if model.stage != stageConfirm {
		t.Fatalf("stage = %d, want confirmation", model.stage)
	}
	view := ansi.Strip(model.View())
	if !strings.Contains(view, "Ready to sign in") || !strings.Contains(view, "123.456") {
		t.Fatalf("confirmation view is missing context:\n%s", view)
	}
}

func TestInvalidConfiguredClientIDIsNeverRendered(t *testing.T) {
	model := New(context.Background(), Config{ClientID: "xoxb-do-not-render-this"})
	if model.stage != stageSetup || model.focus != focusClientID || !model.input.Focused() {
		t.Fatalf("invalid configured value did not return to focused setup: stage=%d focus=%d", model.stage, model.focus)
	}
	if model.input.Value() != "" || strings.Contains(model.View(), "do-not-render-this") {
		t.Fatal("invalid token-like configured Client ID was retained or rendered")
	}
}

func TestLoginProgressFailureRetryAndSuccess(t *testing.T) {
	want := auth.Credential{
		ClientID: "123.456", AccessToken: "xoxp-never-render-me", RefreshToken: "xoxe-also-secret",
		TeamID: "T123", TeamName: "Acme",
	}
	calls := 0
	model := New(context.Background(), Config{
		ClientID: "123.456",
		Login: func(context.Context, string) (auth.Credential, error) {
			calls++
			if calls == 1 {
				return auth.Credential{}, errors.New("temporary Slack error")
			}
			return want, nil
		},
	})

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.stage != stageAuthorizing || command == nil {
		t.Fatal("confirmation did not start asynchronous login")
	}
	model.Update(browserWaitingMsg{attempt: model.attempt})
	if !model.browserWaiting || !strings.Contains(ansi.Strip(model.View()), "Waiting for approval") {
		t.Fatal("browser wait state was not rendered")
	}
	result := runBatchCommand(t, command, func(message tea.Msg) bool {
		_, ok := message.(loginResult)
		return ok
	})
	model.Update(result)
	if model.stage != stageFailure || !strings.Contains(model.errText, "temporary Slack error") {
		t.Fatalf("login failure was not presented: stage=%d error=%q", model.stage, model.errText)
	}

	_, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = runBatchCommand(t, command, func(message tea.Msg) bool {
		_, ok := message.(loginResult)
		return ok
	})
	model.Update(result)
	if model.stage != stageSuccess {
		t.Fatalf("retry did not succeed: stage=%d", model.stage)
	}
	credential, ok := model.Credential()
	if !ok || credential != want {
		t.Fatalf("Credential() = %#v, %v", credential, ok)
	}
	view := model.View()
	if strings.Contains(view, want.AccessToken) || strings.Contains(view, want.RefreshToken) {
		t.Fatal("success view exposed a Slack token")
	}
}

func TestCancelIgnoresLateLoginResult(t *testing.T) {
	model := New(context.Background(), Config{ClientID: "123.456"})
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	oldAttempt := model.attempt
	model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.stage != stageConfirm {
		t.Fatalf("cancel returned to stage %d, want confirmation", model.stage)
	}
	model.Update(loginResult{attempt: oldAttempt, credential: auth.Credential{AccessToken: "xoxp-late"}})
	if _, ok := model.Credential(); ok || model.stage != stageConfirm {
		t.Fatal("late result from canceled login changed model")
	}
}

func TestSafeErrorRedactsTokens(t *testing.T) {
	got := safeError(errors.New("request failed for xoxb-123456789-secret-value"))
	if strings.Contains(got, "secret") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("safeError() = %q", got)
	}
}

func TestViewsFitTerminal(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"compact", 28, 10},
		{"normal", 80, 24},
		{"wide", 120, 32},
	}
	stages := []stage{stageSetup, stageConfirm, stageAuthorizing, stageFailure, stageSuccess}
	for _, test := range tests {
		for _, wizardStage := range stages {
			t.Run(test.name+"/stage-"+string(rune('0'+wizardStage)), func(t *testing.T) {
				model := New(context.Background(), Config{ClientID: "123.456"})
				model.width, model.height, model.stage = test.width, test.height, wizardStage
				model.errText = "A useful failure message"
				model.credential = auth.Credential{TeamName: "Acme", AccessToken: "xoxp-secret"}
				model.spin.Spinner = spinner.Dot
				view := model.View()
				lines := strings.Split(view, "\n")
				if len(lines) > test.height {
					t.Fatalf("view has %d lines, terminal has %d\n%s", len(lines), test.height, ansi.Strip(view))
				}
				for row, line := range lines {
					if got := lipgloss.Width(line); got > test.width {
						t.Fatalf("row %d has width %d, terminal has %d: %q", row, got, test.width, ansi.Strip(line))
					}
				}
				if strings.Contains(view, "xoxp-secret") {
					t.Fatal("view exposed access token")
				}
			})
		}
	}
}

func runBatchCommand(t *testing.T, command tea.Cmd, match func(tea.Msg) bool) tea.Msg {
	t.Helper()
	message := command()
	if match(message) {
		return message
	}
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command returned %T, want matching message or tea.BatchMsg", message)
	}
	for _, child := range batch {
		message := child()
		if match(message) {
			return message
		}
	}
	t.Fatal("batch did not contain expected message")
	return nil
}
