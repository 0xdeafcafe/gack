package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/gack"
)

// reduce is the sole state writer for application events. It may describe the
// next effect, but it never performs I/O itself.
func (m *Model) reduce(event applicationEvent) tea.Cmd {
	switch event := event.(type) {
	case bootstrapResult:
		if event.request != 0 {
			if event.request != m.bootstrapRequest || event.request <= m.bootstrapApplied {
				return nil
			}
			m.bootstrapApplied = event.request
		}
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		snapshot := event.snapshot
		if event.progressive {
			// User hydration is independent and may win the race. Never replace a
			// successfully hydrated map with the deliberately sparse core result.
			if m.usersReady {
				snapshot.Users = m.snapshot.Users
			}
			hydrateSnapshotUsers(&snapshot, snapshot.Users)
		} else {
			m.usersReady = true
			m.usersLoading = false
			m.usersErr = ""
		}
		m.snapshot = snapshot
		m.channels = config.ApplyOrder(snapshot.Conversations, func(channel gack.Conversation) string { return channel.ID }, m.order)
		m.order = m.channelOrder()
		m.sortChannels(m.sidebarSort)
		m.activity = append([]gack.ActivityItem(nil), snapshot.Activity...)
		m.ready = true
		if len(m.channels) == 0 {
			m.err = "No joined conversations are visible to this token"
			return nil
		}
		m.channel = firstInterestingChannel(m.channels)
		m.sidebarAt = m.channel + 2
		m.focus = focusMessages
		m.busy = "Loading messages…"
		return messagesCmd(m.backend, m.channels[m.channel].ID)
	case usersResult:
		if event.request != m.usersRequest || event.request <= m.usersApplied {
			return nil
		}
		m.usersApplied = event.request
		m.usersLoading = false
		if event.err != nil {
			m.usersErr = event.err.Error()
			if m.ready {
				m.status = ""
			}
			return nil
		}
		m.usersReady = true
		m.usersErr = ""
		hydrateSnapshotUsers(&m.snapshot, event.users)
		hydrateConversationUsers(m.channels, event.users)
		if m.ready {
			m.status = "People details updated"
		}
	case versionResult:
		if event.err == nil {
			m.updateAvailable = event.latest
		}
	case messagesResult:
		if event.channel != m.currentChannelID() {
			return nil
		}
		m.loadingMoreMessages = false
		if !event.more {
			m.busy = ""
		}
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.err = ""
		m.messageCursor = event.nextCursor
		if event.more {
			added := prependUniqueMessages(&m.messages, event.messages)
			if added > 0 {
				// The request was triggered by moving above row zero. Land on the
				// nearest newly loaded message so one key press produces one move.
				m.message = added - 1
			}
			m.status = historyLoadedNotice(added, m.messageCursor, "older messages")
		} else {
			m.messages = event.messages
			m.message = len(m.messages) - 1
		}
	case threadResult:
		if event.channel != m.currentChannelID() || event.thread != m.threadTS {
			return nil
		}
		m.loadingMoreThread = false
		if !event.more {
			m.busy = ""
		}
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.threadCursor = event.nextCursor
		if event.more {
			oldLength := len(m.thread)
			wasAtEnd := m.threadAt == oldLength-1
			added, dropped := appendUniqueMessages(&m.thread, event.replies)
			if wasAtEnd && added > 0 {
				m.threadAt = min(len(m.thread)-1, oldLength-dropped)
			} else {
				m.threadAt = max(0, m.threadAt-dropped)
			}
			m.status = historyLoadedNotice(added, m.threadCursor, "replies")
		} else {
			m.thread = event.replies
			m.threadAt = len(m.thread) - 1
			m.status = ""
		}
		m.focus = focusThread
	case postResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.err = ""
		m.status = "Message sent"
		if event.channel == m.currentChannelID() {
			if event.thread != "" && event.thread == m.threadTS {
				m.thread = appendBounded(m.thread, event.message, 100)
				m.threadAt = len(m.thread) - 1
			} else if event.thread == "" {
				m.messages = appendBounded(m.messages, event.message, 100)
				m.message = len(m.messages) - 1
			}
		}
	case editResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.err = ""
		m.status = "Message updated"
		if event.channel == m.currentChannelID() {
			m.replaceMessage(event.ts, event.message)
		}
	case searchResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.searchResults = event.results
		m.searchRan = true
		m.searchAt = 0
	case activityResult:
		m.activityPolling = false
		if !event.background {
			m.busy = ""
		}
		if event.err != nil {
			if !event.background {
				m.err = event.err.Error()
			}
			return nil
		}
		var notifications []tea.Cmd
		for _, item := range event.items {
			if !m.rememberActivity(item) {
				continue
			}
			if event.background && m.activityPrimed && item.Unread && m.notify != nil && len(notifications) < 3 {
				title := "gack · #" + item.ChannelName
				if item.Actor != "" {
					title += " · " + item.Actor
				}
				notifications = append(notifications, notificationCmd(m.notify, title, item.Text))
			}
		}
		m.activityPrimed = true
		m.activity = event.items
		m.activityAt = min(m.activityAt, max(0, len(m.filteredActivity())-1))
		return tea.Batch(notifications...)
	case activityPollTick:
		commands := []tea.Cmd{scheduleActivityPoll(30 * time.Second)}
		if m.ready && !m.activityPolling {
			m.activityPolling = true
			commands = append(commands, activityCmd(m.backend, true))
		}
		return tea.Batch(commands...)
	case notificationResult:
		// Native notifications are best-effort. A missing desktop notifier must
		// never turn a healthy Slack session into a persistent red error.
		return nil
	case interactionResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.err = ""
		if len(event.result.Errors) > 0 && m.dialog != nil {
			m.dialog.setErrors(event.result.Errors)
			return nil
		}
		if event.result.Replace != nil {
			next := newDialogState(*event.result.Replace, m.width)
			next.parent = m.dialog
			m.dialog = next
			return nil
		}
		if event.result.View != nil {
			m.dialog = newDialogState(*event.result.View, m.width)
			return nil
		}
		m.dialog = nil
		if event.result.Notice != "" {
			m.status = event.result.Notice
		}
		if channel := m.currentChannelID(); channel != "" {
			return messagesCmd(m.backend, channel)
		}
	case reactionResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.applyReaction(event)
		m.status = ":" + event.emoji + ": updated"
	case sidebarSaved:
		if m.saveState != nil && event.revision != m.saveState.latest.Load() {
			return nil
		}
		if event.err != nil {
			m.err = "Could not save sidebar preferences: " + event.err.Error()
		} else {
			m.status = event.notice
		}
	case copiedResult:
		if event.err != nil {
			m.err = "Could not copy message: " + event.err.Error()
		} else {
			m.status = "Message copied"
		}
	case externalURLResult:
		if event.err != nil {
			m.status = ""
			m.err = "Could not open " + event.label + ": " + event.err.Error()
		} else {
			m.err = ""
			m.status = event.label + " opened in your browser"
		}
	}
	return nil
}

func hydrateSnapshotUsers(snapshot *gack.Snapshot, users map[string]gack.User) {
	if users == nil {
		users = map[string]gack.User{}
	}
	snapshot.Users = users
	if self, ok := users[snapshot.Self.ID]; ok {
		snapshot.Self = self
	}
	hydrateConversationUsers(snapshot.Conversations, users)
}

func hydrateConversationUsers(conversations []gack.Conversation, users map[string]gack.User) {
	for index := range conversations {
		conversation := &conversations[index]
		if !conversation.IsDM {
			continue
		}
		user, ok := users[conversation.UserID]
		if !ok {
			continue
		}
		if user.Name != "" {
			conversation.Name = user.Name
		}
		conversation.DisplayName = "@" + user.DisplayName()
	}
}
