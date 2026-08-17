package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/0xdeafcafe/gack/internal/config"
	"github.com/0xdeafcafe/gack/internal/gack"
)

type viewMode int

const (
	viewConversation viewMode = iota
	viewActivity
	viewNotifications
)

type focus int

const (
	focusSidebar focus = iota
	focusMessages
	focusThread
	focusComposer
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayGlobalSearch
	overlayFind
	overlayAction
	overlayReaction
	overlayHelp
)

type pickerOption struct {
	label string
	value string
}

type Model struct {
	backend   gack.Backend
	saveOrder func([]string) error
	order     []string

	width  int
	height int
	ready  bool
	busy   string
	status string
	err    string

	snapshot gack.Snapshot
	channels []gack.Conversation
	channel  int
	messages []gack.Message
	message  int

	threadTS string
	thread   []gack.Message
	threadAt int

	activity   []gack.ActivityItem
	activityAt int
	mode       viewMode
	focus      focus
	sidebarAt  int

	searchInput   textinput.Model
	searchResults []gack.SearchResult
	searchAt      int
	searchRan     bool
	findInput     textinput.Model
	composeInput  textinput.Model
	composeThread string
	overlay       overlayMode

	pickerElement *struct {
		BlockID string
		Element gack.Element
	}
	pickerOptions []pickerOption
	pickerAt      int

	dialog *dialogState

	dragFrom  int
	dragAt    int
	dragMoved bool

	visibleChannelStart int
	channelRowStart     int
}

func New(backend gack.Backend, order []string, saveOrder func([]string) error) *Model {
	search := textinput.New()
	search.Prompt = "Search › "
	search.Placeholder = "channels and messages"
	find := textinput.New()
	find.Prompt = "Find › "
	find.Placeholder = "text in this conversation"
	compose := textinput.New()
	compose.Prompt = "Message › "
	compose.Placeholder = "Write a message"
	return &Model{
		backend: backend, saveOrder: saveOrder, order: append([]string(nil), order...),
		channel: -1, message: -1, threadAt: -1, activityAt: 0,
		focus: focusSidebar, searchInput: search, findInput: find,
		composeInput: compose, dragFrom: -1, dragAt: -1,
	}
}

func (m *Model) Init() tea.Cmd {
	m.busy = "Connecting…"
	return bootstrapCmd(m.backend)
}

type bootstrapResult struct {
	snapshot gack.Snapshot
	err      error
}

type messagesResult struct {
	channel  string
	messages []gack.Message
	err      error
}

type threadResult struct {
	channel string
	thread  string
	replies []gack.Message
	err     error
}

type postResult struct {
	channel string
	thread  string
	message gack.Message
	err     error
}

type searchResult struct {
	results []gack.SearchResult
	err     error
}

type activityResult struct {
	items []gack.ActivityItem
	err   error
}

type interactionResult struct {
	result gack.InteractionResult
	err    error
}

type reactionResult struct {
	channel string
	thread  string
	ts      string
	emoji   string
	remove  bool
	err     error
}

type orderSaved struct{ err error }

func bootstrapCmd(backend gack.Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snapshot, err := backend.Bootstrap(ctx)
		return bootstrapResult{snapshot: snapshot, err: err}
	}
}

func messagesCmd(backend gack.Backend, channel string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		messages, err := backend.Messages(ctx, channel)
		return messagesResult{channel: channel, messages: messages, err: err}
	}
}

func threadCmd(backend gack.Backend, channel, thread string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		replies, err := backend.Thread(ctx, channel, thread)
		return threadResult{channel: channel, thread: thread, replies: replies, err: err}
	}
}

func postCmd(backend gack.Backend, channel, thread, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		message, err := backend.PostMessage(ctx, channel, thread, text)
		return postResult{channel: channel, thread: thread, message: message, err: err}
	}
}

func searchCmd(backend gack.Backend, query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		results, err := backend.Search(ctx, query)
		return searchResult{results: results, err: err}
	}
}

func activityCmd(backend gack.Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		items, err := backend.Activity(ctx)
		return activityResult{items: items, err: err}
	}
}

func interactionCmd(backend gack.Backend, interaction gack.Interaction) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := backend.Interact(ctx, interaction)
		return interactionResult{result: result, err: err}
	}
}

func reactionCmd(backend gack.Backend, channel, thread, ts, emoji string, remove bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := backend.ToggleReaction(ctx, channel, ts, emoji, remove)
		return reactionResult{channel: channel, thread: thread, ts: ts, emoji: emoji, remove: remove, err: err}
	}
}

func saveOrderCmd(save func([]string) error, order []string) tea.Cmd {
	return func() tea.Msg {
		if save == nil {
			return orderSaved{}
		}
		return orderSaved{err: save(order)}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		inputWidth := max(20, min(72, m.width-12))
		m.searchInput.Width = inputWidth
		m.findInput.Width = inputWidth
		m.composeInput.Width = max(12, m.width-36)
		m.resizeDialogInputs()
		return m, nil
	case bootstrapResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.channels = config.ApplyOrder(msg.snapshot.Conversations, func(channel gack.Conversation) string { return channel.ID }, m.order)
		m.activity = append([]gack.ActivityItem(nil), msg.snapshot.Activity...)
		m.ready = true
		if len(m.channels) == 0 {
			m.err = "No joined conversations are visible to this token"
			return m, nil
		}
		m.channel = firstInterestingChannel(m.channels)
		m.sidebarAt = m.channel + 2
		m.focus = focusMessages
		m.busy = "Loading messages…"
		return m, messagesCmd(m.backend, m.channels[m.channel].ID)
	case messagesResult:
		if msg.channel != m.currentChannelID() {
			return m, nil
		}
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.messages = msg.messages
		m.message = len(m.messages) - 1
		return m, nil
	case threadResult:
		if msg.channel != m.currentChannelID() || msg.thread != m.threadTS {
			return m, nil
		}
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.thread = msg.replies
		m.threadAt = len(m.thread) - 1
		m.focus = focusThread
		return m, nil
	case postResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		m.status = "Message sent"
		if msg.channel == m.currentChannelID() {
			if msg.thread != "" && msg.thread == m.threadTS {
				m.thread = appendBounded(m.thread, msg.message, 100)
				m.threadAt = len(m.thread) - 1
			} else if msg.thread == "" {
				m.messages = appendBounded(m.messages, msg.message, 100)
				m.message = len(m.messages) - 1
			}
		}
		return m, nil
	case searchResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.searchResults = msg.results
		m.searchRan = true
		m.searchAt = 0
		return m, nil
	case activityResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.activity = msg.items
		m.activityAt = min(m.activityAt, max(0, len(m.filteredActivity())-1))
		return m, nil
	case interactionResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.err = ""
		if len(msg.result.Errors) > 0 && m.dialog != nil {
			m.dialog.setErrors(msg.result.Errors)
			return m, nil
		}
		if msg.result.Replace != nil {
			next := newDialogState(*msg.result.Replace, m.width)
			next.parent = m.dialog
			m.dialog = next
			return m, nil
		}
		if msg.result.View != nil {
			m.dialog = newDialogState(*msg.result.View, m.width)
			return m, nil
		}
		m.dialog = nil
		if msg.result.Notice != "" {
			m.status = msg.result.Notice
		}
		if channel := m.currentChannelID(); channel != "" {
			return m, messagesCmd(m.backend, channel)
		}
		return m, nil
	case reactionResult:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.applyReaction(msg)
		m.status = ":" + msg.emoji + ": updated"
		return m, nil
	case orderSaved:
		if msg.err != nil {
			m.err = "Could not save channel order: " + msg.err.Error()
		} else {
			m.status = "Channel order saved"
		}
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.dialog != nil {
		return m, m.updateDialog(msg)
	}
	switch m.overlay {
	case overlayGlobalSearch:
		return m, m.updateSearch(msg)
	case overlayFind:
		return m, m.updateFind(msg)
	case overlayAction, overlayReaction:
		return m, m.updatePicker(msg)
	case overlayHelp:
		if key == "esc" || key == "?" || key == "enter" {
			m.overlay = overlayNone
		}
		return m, nil
	}
	if m.focus == focusComposer {
		return m, m.updateComposer(msg)
	}
	switch key {
	case "ctrl+k":
		m.openSearch()
		return m, nil
	case "ctrl+f":
		if m.mode == viewConversation {
			m.overlay = overlayFind
			m.findInput.Focus()
		}
		return m, nil
	case "?":
		m.overlay = overlayHelp
		return m, nil
	case "q":
		return m, tea.Quit
	case "R":
		m.err = ""
		if m.mode == viewActivity || m.mode == viewNotifications {
			m.busy = "Refreshing activity…"
			return m, activityCmd(m.backend)
		}
		if channel := m.currentChannelID(); channel != "" {
			m.busy = "Refreshing messages…"
			return m, messagesCmd(m.backend, channel)
		}
		return m, nil
	case "tab":
		m.cycleFocus(1)
		return m, nil
	case "shift+tab":
		m.cycleFocus(-1)
		return m, nil
	case "a":
		return m, m.openActivity(false)
	case "n":
		return m, m.openActivity(true)
	case "esc":
		if m.threadTS != "" {
			m.threadTS, m.thread = "", nil
			m.focus = focusMessages
		} else if m.mode != viewConversation {
			m.mode = viewConversation
		}
		return m, nil
	}
	if m.focus == focusSidebar {
		return m, m.updateSidebar(key)
	}
	if m.mode == viewActivity || m.mode == viewNotifications {
		return m, m.updateActivity(key)
	}
	return m, m.updateConversation(key)
}

func (m *Model) updateSidebar(key string) tea.Cmd {
	selected := m.sidebarSelection()
	maximum := len(m.channels) + 1
	switch key {
	case "up", "k":
		selected = max(0, selected-1)
	case "down", "j":
		selected = min(maximum, selected+1)
	case "K":
		if selected >= 2 && selected-2 > 0 {
			m.reorderChannel(selected-2, selected-3)
			return saveOrderCmd(m.saveOrder, m.channelOrder())
		}
	case "J":
		if selected >= 2 && selected-2 < len(m.channels)-1 {
			m.reorderChannel(selected-2, selected-1)
			return saveOrderCmd(m.saveOrder, m.channelOrder())
		}
	case "enter", "right", "l":
		if selected == 0 {
			return m.openActivity(true)
		}
		if selected == 1 {
			return m.openActivity(false)
		}
		return m.openChannel(selected - 2)
	default:
		return nil
	}
	m.setSidebarSelection(selected)
	return nil
}

func (m *Model) updateConversation(key string) tea.Cmd {
	messages, selected := m.focusedMessages()
	if len(messages) == 0 {
		if key == "c" {
			m.openComposer("")
		}
		return nil
	}
	switch key {
	case "up", "k":
		m.setFocusedMessage(max(0, selected-1))
	case "down", "j":
		m.setFocusedMessage(min(len(messages)-1, selected+1))
	case "home", "g":
		m.setFocusedMessage(0)
	case "end", "G":
		m.setFocusedMessage(len(messages) - 1)
	case "t", "enter", "right", "l":
		if m.focus == focusThread {
			m.openComposer(m.threadTS)
			return nil
		}
		return m.openThread(messages[selected])
	case "left", "h":
		if m.focus == focusThread {
			m.focus = focusMessages
		} else {
			m.focus = focusSidebar
		}
	case "c":
		thread := ""
		if m.focus == focusThread {
			thread = m.threadTS
		}
		m.openComposer(thread)
	case "r":
		m.openReactionPicker(messages[selected])
	case "i":
		return m.openActionPicker(messages[selected])
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			index := int(key[0] - '1')
			return m.activateElement(messages[selected], index)
		}
	}
	return nil
}

func (m *Model) updateActivity(key string) tea.Cmd {
	items := m.filteredActivity()
	if len(items) == 0 {
		return nil
	}
	switch key {
	case "up", "k":
		m.activityAt = max(0, m.activityAt-1)
	case "down", "j":
		m.activityAt = min(len(items)-1, m.activityAt+1)
	case "enter", "right", "l":
		item := items[m.activityAt]
		for i, channel := range m.channels {
			if channel.ID == item.ChannelID {
				return m.openChannel(i)
			}
		}
	case "left", "h":
		m.focus = focusSidebar
	}
	return nil
}

func (m *Model) updateComposer(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.composeInput.Blur()
		m.composeInput.SetValue("")
		if m.composeThread != "" {
			m.focus = focusThread
		} else {
			m.focus = focusMessages
		}
		return nil
	case "enter":
		text := strings.TrimSpace(m.composeInput.Value())
		if text == "" {
			return nil
		}
		channel, thread := m.currentChannelID(), m.composeThread
		m.composeInput.SetValue("")
		m.composeInput.Blur()
		if thread != "" {
			m.focus = focusThread
		} else {
			m.focus = focusMessages
		}
		m.busy = "Sending…"
		return postCmd(m.backend, channel, thread, text)
	}
	var cmd tea.Cmd
	m.composeInput, cmd = m.composeInput.Update(msg)
	return cmd
}

func (m *Model) updateSearch(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+k":
		m.closeSearch()
		return nil
	case "up", "ctrl+p":
		m.searchAt = max(0, m.searchAt-1)
		return nil
	case "down", "ctrl+n":
		m.searchAt = min(max(0, m.searchItemCount()-1), m.searchAt+1)
		return nil
	case "enter":
		if m.searchRan {
			if m.searchAt < len(m.searchResults) {
				result := m.searchResults[m.searchAt]
				m.closeSearch()
				for i, channel := range m.channels {
					if channel.ID == result.ChannelID {
						return m.openChannel(i)
					}
				}
			}
			return nil
		}
		channels := m.filteredChannels(m.searchInput.Value())
		query := strings.TrimSpace(m.searchInput.Value())
		if len(channels) > 0 && m.searchAt < len(channels) {
			id := channels[m.searchAt].ID
			m.closeSearch()
			for i, channel := range m.channels {
				if channel.ID == id {
					return m.openChannel(i)
				}
			}
		}
		if query != "" {
			m.busy = "Searching…"
			m.searchRan = true
			return searchCmd(m.backend, query)
		}
		return nil
	}
	previous := m.searchInput.Value()
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	if m.searchInput.Value() != previous {
		m.searchRan = false
		m.searchResults = nil
		m.searchAt = 0
	}
	return cmd
}

func (m *Model) updateFind(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+f":
		m.overlay = overlayNone
		m.findInput.Blur()
		return nil
	case "enter", "ctrl+n":
		m.findNext(1)
		return nil
	case "ctrl+p":
		m.findNext(-1)
		return nil
	}
	var cmd tea.Cmd
	m.findInput, cmd = m.findInput.Update(msg)
	return cmd
}

func (m *Model) updatePicker(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.closePicker()
	case "up", "k":
		m.pickerAt = max(0, m.pickerAt-1)
	case "down", "j":
		m.pickerAt = min(max(0, len(m.pickerOptions)-1), m.pickerAt+1)
	case "enter":
		if len(m.pickerOptions) == 0 {
			return nil
		}
		option := m.pickerOptions[m.pickerAt]
		if m.overlay == overlayReaction {
			message := m.currentSelectedMessage()
			remove := reactionIsMine(message, option.value)
			m.closePicker()
			m.busy = "Updating reaction…"
			return reactionCmd(m.backend, m.currentChannelID(), message.ThreadTS, message.TS, option.value, remove)
		}
		if m.pickerElement != nil {
			picked := *m.pickerElement
			m.closePicker()
			return m.runInteraction(picked.BlockID, picked.Element, option.value)
		}
	}
	return nil
}

func (m *Model) openChannel(index int) tea.Cmd {
	if index < 0 || index >= len(m.channels) {
		return nil
	}
	m.channel = index
	m.sidebarAt = index + 2
	m.messages = nil
	m.message = -1
	m.threadTS, m.thread = "", nil
	m.mode = viewConversation
	m.focus = focusMessages
	m.busy = "Loading messages…"
	m.err = ""
	return messagesCmd(m.backend, m.channels[index].ID)
}

func (m *Model) openThread(message gack.Message) tea.Cmd {
	m.threadTS = message.TS
	m.thread = nil
	m.threadAt = -1
	m.busy = "Loading thread…"
	return threadCmd(m.backend, m.currentChannelID(), message.TS)
}

func (m *Model) openActivity(notificationsOnly bool) tea.Cmd {
	if notificationsOnly {
		m.mode = viewNotifications
		m.sidebarAt = 0
	} else {
		m.mode = viewActivity
		m.sidebarAt = 1
	}
	m.focus = focusMessages
	m.activityAt = 0
	m.busy = "Loading activity…"
	return activityCmd(m.backend)
}

func (m *Model) openComposer(thread string) {
	m.composeThread = thread
	m.focus = focusComposer
	m.composeInput.Placeholder = "Write a message"
	if thread != "" {
		m.composeInput.Placeholder = "Reply in thread"
	}
	m.composeInput.Focus()
}

func (m *Model) openSearch() {
	m.overlay = overlayGlobalSearch
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.searchResults = nil
	m.searchAt = 0
	m.searchRan = false
}

func (m *Model) closeSearch() {
	m.overlay = overlayNone
	m.searchInput.Blur()
	m.searchResults = nil
	m.searchRan = false
}

func (m *Model) openReactionPicker(message gack.Message) {
	emojis := []pickerOption{{"👍  +1", "+1"}, {"❤️  heart", "heart"}, {"🎉  tada", "tada"}, {"👀  eyes", "eyes"}, {"✅  white_check_mark", "white_check_mark"}, {"🚀  rocket", "rocket"}, {"🙌  raised_hands", "raised_hands"}, {"😂  joy", "joy"}}
	m.overlay = overlayReaction
	m.pickerOptions = emojis
	m.pickerAt = 0
}

func (m *Model) openActionPicker(message gack.Message) tea.Cmd {
	elements := gack.InteractiveElements(message.Blocks)
	if len(elements) == 0 {
		m.status = "This message has no interactive Block Kit elements"
		return nil
	}
	if len(elements) == 1 {
		return m.activateElement(message, 0)
	}
	// A compact chooser for the common case. Selecting a select element opens
	// its options in a second step.
	m.overlay = overlayAction
	m.pickerOptions = nil
	for index, element := range elements {
		label := element.Element.Text
		if label == "" {
			label = element.Element.Placeholder
		}
		if label == "" {
			label = element.Element.ActionID
		}
		m.pickerOptions = append(m.pickerOptions, pickerOption{label: fmt.Sprintf("%d  %s", index+1, label), value: strconv.Itoa(index)})
	}
	m.pickerElement = &struct {
		BlockID string
		Element gack.Element
	}{BlockID: "__choose_element__", Element: gack.Element{ActionID: "__choose_element__", Value: message.TS}}
	m.pickerAt = 0
	return nil
}

func (m *Model) activateElement(message gack.Message, index int) tea.Cmd {
	elements := gack.InteractiveElements(message.Blocks)
	if index < 0 || index >= len(elements) {
		return nil
	}
	picked := elements[index]
	if len(picked.Element.Options) > 0 && picked.Element.Type != "button" {
		m.overlay = overlayAction
		m.pickerOptions = nil
		for _, option := range picked.Element.Options {
			m.pickerOptions = append(m.pickerOptions, pickerOption{label: option.Text, value: option.Value})
		}
		m.pickerElement = &struct {
			BlockID string
			Element gack.Element
		}{BlockID: picked.BlockID, Element: picked.Element}
		m.pickerAt = 0
		return nil
	}
	return m.runInteraction(picked.BlockID, picked.Element, picked.Element.Value)
}

func (m *Model) runInteraction(blockID string, element gack.Element, value string) tea.Cmd {
	if blockID == "__choose_element__" {
		index, _ := strconv.Atoi(value)
		return m.activateElement(m.currentSelectedMessage(), index)
	}
	message := m.currentSelectedMessage()
	m.busy = "Running “" + firstNonEmpty(element.Text, element.Placeholder, element.ActionID) + "”…"
	return interactionCmd(m.backend, gack.Interaction{
		Type: "block_actions", UserID: m.snapshot.Self.ID, ChannelID: m.currentChannelID(),
		MessageTS: message.TS, ThreadTS: message.ThreadTS, BlockID: blockID,
		ActionID: element.ActionID, ActionType: element.Type, Value: value,
	})
}

func (m *Model) closePicker() {
	m.overlay = overlayNone
	m.pickerOptions = nil
	m.pickerElement = nil
	m.pickerAt = 0
}

func (m *Model) findNext(direction int) {
	query := strings.ToLower(strings.TrimSpace(m.findInput.Value()))
	if query == "" {
		return
	}
	messages, at := m.focusedMessages()
	for offset := 1; offset <= len(messages); offset++ {
		candidate := (at + direction*offset) % len(messages)
		if candidate < 0 {
			candidate += len(messages)
		}
		if strings.Contains(strings.ToLower(messages[candidate].Text), query) {
			m.setFocusedMessage(candidate)
			m.status = fmt.Sprintf("Match %d of %d", candidate+1, len(messages))
			return
		}
	}
	m.status = "No match for “" + m.findInput.Value() + "”"
}

func (m *Model) cycleFocus(direction int) {
	available := []focus{focusSidebar, focusMessages}
	if m.threadTS != "" {
		available = append(available, focusThread)
	}
	index := slices.Index(available, m.focus)
	if index < 0 {
		index = 0
	}
	index = (index + direction + len(available)) % len(available)
	m.focus = available[index]
}

func (m *Model) sidebarSelection() int {
	return max(0, min(len(m.channels)+1, m.sidebarAt))
}

func (m *Model) setSidebarSelection(selected int) {
	m.sidebarAt = max(0, min(len(m.channels)+1, selected))
}

func (m *Model) focusedMessages() ([]gack.Message, int) {
	if m.focus == focusThread && len(m.thread) > 0 {
		return m.thread, max(0, m.threadAt)
	}
	return m.messages, max(0, m.message)
}

func (m *Model) setFocusedMessage(index int) {
	if m.focus == focusThread {
		m.threadAt = index
	} else {
		m.message = index
	}
}

func (m *Model) currentSelectedMessage() gack.Message {
	messages, index := m.focusedMessages()
	if index >= 0 && index < len(messages) {
		return messages[index]
	}
	return gack.Message{}
}

func (m *Model) currentChannelID() string {
	if m.channel < 0 || m.channel >= len(m.channels) {
		return ""
	}
	return m.channels[m.channel].ID
}

func (m *Model) filteredActivity() []gack.ActivityItem {
	if m.mode != viewNotifications {
		return m.activity
	}
	result := make([]gack.ActivityItem, 0, len(m.activity))
	for _, item := range m.activity {
		if item.Unread {
			result = append(result, item)
		}
	}
	return result
}

func (m *Model) filteredChannels(query string) []gack.Conversation {
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]gack.Conversation, 0, min(10, len(m.channels)))
	for _, channel := range m.channels {
		if query == "" || strings.Contains(strings.ToLower(channel.Label()+" "+channel.Topic), query) {
			result = append(result, channel)
			if len(result) == 10 {
				break
			}
		}
	}
	return result
}

func (m *Model) searchItemCount() int {
	if m.searchRan {
		return len(m.searchResults)
	}
	count := len(m.filteredChannels(m.searchInput.Value()))
	if strings.TrimSpace(m.searchInput.Value()) != "" {
		count++ // explicit "search messages" row
	}
	return count
}

func (m *Model) reorderChannel(from, to int) {
	if from < 0 || to < 0 || from >= len(m.channels) || to >= len(m.channels) || from == to {
		return
	}
	selectedID := m.currentChannelID()
	cursorID := ""
	if m.sidebarAt >= 2 && m.sidebarAt-2 < len(m.channels) {
		cursorID = m.channels[m.sidebarAt-2].ID
	}
	item := m.channels[from]
	if from < to {
		copy(m.channels[from:to], m.channels[from+1:to+1])
	} else {
		copy(m.channels[to+1:from+1], m.channels[to:from])
	}
	m.channels[to] = item
	for i := range m.channels {
		if m.channels[i].ID == selectedID {
			m.channel = i
		}
		if m.channels[i].ID == cursorID {
			m.sidebarAt = i + 2
		}
	}
}

func (m *Model) channelOrder() []string {
	result := make([]string, len(m.channels))
	for i, channel := range m.channels {
		result[i] = channel.ID
	}
	return result
}

func (m *Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil || m.overlay != overlayNone || !m.ready {
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelUp {
		if m.mode == viewConversation && m.focus != focusSidebar {
			messages, selected := m.focusedMessages()
			if len(messages) > 0 {
				m.setFocusedMessage(max(0, selected-2))
			}
		}
		return m, nil
	}
	if msg.Button == tea.MouseButtonWheelDown {
		if m.mode == viewConversation && m.focus != focusSidebar {
			messages, selected := m.focusedMessages()
			if len(messages) > 0 {
				m.setFocusedMessage(min(len(messages)-1, selected+2))
			}
		}
		return m, nil
	}
	channelIndex := m.visibleChannelStart + msg.Y - m.channelRowStart
	insideChannelList := msg.X >= 0 && msg.X < m.sidebarWidth() && channelIndex >= 0 && channelIndex < len(m.channels)
	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button == tea.MouseButtonLeft && insideChannelList {
			m.dragFrom, m.dragAt, m.dragMoved = channelIndex, channelIndex, false
			m.focus = focusSidebar
			m.sidebarAt = channelIndex + 2
		}
	case tea.MouseActionMotion:
		if m.dragFrom >= 0 && insideChannelList && channelIndex != m.dragAt {
			m.reorderChannel(m.dragAt, channelIndex)
			m.dragAt = channelIndex
			m.dragMoved = true
		}
	case tea.MouseActionRelease:
		if m.dragFrom >= 0 {
			moved := m.dragMoved
			at := m.dragAt
			m.dragFrom, m.dragAt, m.dragMoved = -1, -1, false
			if moved {
				return m, saveOrderCmd(m.saveOrder, m.channelOrder())
			}
			return m, m.openChannel(at)
		}
	}
	return m, nil
}

func (m *Model) applyReaction(result reactionResult) {
	update := func(messages []gack.Message) {
		for i := range messages {
			if messages[i].TS != result.ts {
				continue
			}
			for j := range messages[i].Reactions {
				if messages[i].Reactions[j].Name != result.emoji {
					continue
				}
				if result.remove {
					messages[i].Reactions[j].Mine = false
					messages[i].Reactions[j].Count--
				} else {
					messages[i].Reactions[j].Mine = true
					messages[i].Reactions[j].Count++
				}
				return
			}
			if !result.remove {
				messages[i].Reactions = append(messages[i].Reactions, gack.Reaction{Name: result.emoji, Count: 1, Mine: true})
			}
		}
	}
	update(m.messages)
	update(m.thread)
}

func firstInterestingChannel(channels []gack.Conversation) int {
	for i, channel := range channels {
		if channel.Mentions > 0 {
			return i
		}
	}
	for i, channel := range channels {
		if channel.Unread > 0 {
			return i
		}
	}
	return 0
}

func appendBounded(messages []gack.Message, message gack.Message, limit int) []gack.Message {
	if len(messages) >= limit {
		copy(messages, messages[len(messages)-limit+1:])
		messages = messages[:limit-1]
	}
	return append(messages, message)
}

func reactionIsMine(message gack.Message, emoji string) bool {
	for _, reaction := range message.Reactions {
		if reaction.Name == emoji {
			return reaction.Mine
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "interaction"
}
