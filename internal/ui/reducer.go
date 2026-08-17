package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/gack"
)

// reduce is the sole state writer for application events. It may describe the
// next effect, but it never performs I/O itself.
func (m *Model) reduce(event applicationEvent) tea.Cmd {
	switch event := event.(type) {
	case bootstrapResult:
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.snapshot = event.snapshot
		m.channels = config.ApplyOrder(event.snapshot.Conversations, func(channel gack.Conversation) string { return channel.ID }, m.order)
		m.order = m.channelOrder()
		m.sortChannels(m.sidebarSort)
		m.activity = append([]gack.ActivityItem(nil), event.snapshot.Activity...)
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
	case versionResult:
		if event.err == nil {
			m.updateAvailable = event.latest
		}
	case messagesResult:
		if event.channel != m.currentChannelID() {
			return nil
		}
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.err = ""
		m.messages = event.messages
		m.message = len(m.messages) - 1
	case threadResult:
		if event.channel != m.currentChannelID() || event.thread != m.threadTS {
			return nil
		}
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.thread = event.replies
		m.threadAt = len(m.thread) - 1
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
		m.busy = ""
		if event.err != nil {
			m.err = event.err.Error()
			return nil
		}
		m.activity = event.items
		m.activityAt = min(m.activityAt, max(0, len(m.filteredActivity())-1))
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
	}
	return nil
}
