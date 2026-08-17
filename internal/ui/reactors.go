package ui

import (
	"context"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/gack"
)

// Reactors are the only UI-core functions that call the backend or another
// external service. Each produces exactly one applicationEvent for the reducer.

func checkUpdateCmd(check func(context.Context) (string, error)) tea.Cmd {
	return (applicationEffect{timeout: 5 * time.Second, run: func(ctx context.Context) applicationEvent {
		latest, err := check(ctx)
		return versionResult{latest: latest, err: err}
	}}).command()
}

func bootstrapCmd(backend gack.Backend) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		snapshot, err := backend.Bootstrap(ctx)
		return bootstrapResult{snapshot: snapshot, err: err}
	}}).command()
}

func messagesCmd(backend gack.Backend, channel string) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		messages, err := backend.Messages(ctx, channel)
		return messagesResult{channel: channel, messages: messages, err: err}
	}}).command()
}

func threadCmd(backend gack.Backend, channel, thread string) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		replies, err := backend.Thread(ctx, channel, thread)
		return threadResult{channel: channel, thread: thread, replies: replies, err: err}
	}}).command()
}

func postCmd(backend gack.Backend, channel, thread, text string) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		message, err := backend.PostMessage(ctx, channel, thread, text)
		return postResult{channel: channel, thread: thread, message: message, err: err}
	}}).command()
}

func editCmd(backend gack.Backend, channel, ts, text string) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		message, err := backend.EditMessage(ctx, channel, ts, text)
		return editResult{channel: channel, ts: ts, message: message, err: err}
	}}).command()
}

func copyCmd(value string) tea.Cmd {
	return (applicationEffect{run: func(context.Context) applicationEvent {
		return copiedResult{err: clipboard.WriteAll(value)}
	}}).command()
}

func searchCmd(backend gack.Backend, query string) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		results, err := backend.Search(ctx, query)
		return searchResult{results: results, err: err}
	}}).command()
}

func activityCmd(backend gack.Backend) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		items, err := backend.Activity(ctx)
		return activityResult{items: items, err: err}
	}}).command()
}

func interactionCmd(backend gack.Backend, interaction gack.Interaction) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		result, err := backend.Interact(ctx, interaction)
		return interactionResult{result: result, err: err}
	}}).command()
}

func reactionCmd(backend gack.Backend, channel, thread, ts, emoji string, remove bool) tea.Cmd {
	return (applicationEffect{timeout: 30 * time.Second, run: func(ctx context.Context) applicationEvent {
		err := backend.ToggleReaction(ctx, channel, ts, emoji, remove)
		return reactionResult{channel: channel, thread: thread, ts: ts, emoji: emoji, remove: remove, err: err}
	}}).command()
}

func saveSidebarCmd(state *sidebarSaveState, save func(config.SidebarPreferences) error, preferences config.SidebarPreferences, notice string) tea.Cmd {
	revision := state.latest.Add(1)
	return (applicationEffect{run: func(context.Context) applicationEvent {
		state.mutex.Lock()
		defer state.mutex.Unlock()
		// Effects run asynchronously. If a newer preference event already
		// exists, skip this write so stale state cannot reappear after restart.
		if state.latest.Load() != revision {
			return sidebarSaved{revision: revision}
		}
		if save == nil {
			return sidebarSaved{revision: revision, notice: notice}
		}
		preferences.ChannelOrder = append([]string(nil), preferences.ChannelOrder...)
		return sidebarSaved{revision: revision, notice: notice, err: save(preferences)}
	}}).command()
}
