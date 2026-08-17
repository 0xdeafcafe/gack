package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// applicationEvent is the internal one-way data boundary. Reactors may perform
// I/O, but they can only return immutable events. The reducer is the sole place
// where those events are allowed to change Model state.
type applicationEvent interface {
	applicationEvent()
}

func (bootstrapResult) applicationEvent()    {}
func (messagesResult) applicationEvent()     {}
func (threadResult) applicationEvent()       {}
func (postResult) applicationEvent()         {}
func (editResult) applicationEvent()         {}
func (searchResult) applicationEvent()       {}
func (activityResult) applicationEvent()     {}
func (activityPollTick) applicationEvent()   {}
func (notificationResult) applicationEvent() {}
func (interactionResult) applicationEvent()  {}
func (reactionResult) applicationEvent()     {}
func (sidebarSaved) applicationEvent()       {}
func (copiedResult) applicationEvent()       {}
func (versionResult) applicationEvent()      {}

// applicationEffect isolates a side effect from the reducer. Bubble Tea runs
// the command outside Update and feeds the resulting event back into the same
// serialized queue. There is no broadcast bus or goroutine per subscriber.
type applicationEffect struct {
	timeout time.Duration
	run     func(context.Context) applicationEvent
}

func (effect applicationEffect) command() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		cancel := func() {}
		if effect.timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, effect.timeout)
		}
		defer cancel()
		return effect.run(ctx)
	}
}
